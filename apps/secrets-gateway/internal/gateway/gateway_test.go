package gateway

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/etalab-ia/codebox/packages/secrets"
)

// TestAuditLogsNeverContainSecretValues is the canary that guards against the
// most damaging regression class: a secret, token or code leaking into logs.
func TestAuditLogsNeverContainSecretValues(t *testing.T) {
	owner := TokenClaims{Subject: "s1", Email: "owner@beta.gouv.fr", ACR: "eidas2"}
	store := newMemoryStore()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	tokens, err := NewTokenIssuer(testSeed, "https://gateway.test", time.Hour, time.Now)
	if err != nil {
		t.Fatalf("NewTokenIssuer: %v", err)
	}
	server, err := NewServer(
		Config{PublicURL: "https://gateway.test", TicketLifetime: time.Minute, AuditLogger: logger},
		&fakeProvider{identity: Identity{Subject: owner.Subject, Email: owner.Email, ACR: owner.ACR}},
		store, tokens,
		NewAttemptStore(time.Minute, 64, time.Now),
		NewRateLimiter(100, time.Minute, 64, time.Now),
		time.Now,
	)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	const (
		secretValue = "canary-value-should-never-be-logged"
		secretName  = "CANARY_TOKEN"
	)
	sessionToken, _, err := tokens.IssueSession(owner)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	createRec := httptest.NewRecorder()
	createReq := newJSONRequest(http.MethodPost, "/v1/projects", secrets.CreateProjectRequest{})
	createReq.Header.Set("Authorization", "Bearer "+sessionToken)
	server.Handler().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d (body %s)", createRec.Code, createRec.Body.String())
	}
	var created secrets.CreateProjectResponse
	decodeJSON(t, createRec, &created)

	pushRec := httptest.NewRecorder()
	pushReq := newJSONRequest(http.MethodPut, "/v1/projects/"+created.ProjectID+"/secrets", secrets.PushSecretsRequest{
		BaseRevision: 1,
		Components:   map[string]map[string]string{".env": {secretName: secretValue}},
	})
	pushReq.Header.Set("Authorization", "Bearer "+sessionToken)
	server.Handler().ServeHTTP(pushRec, pushReq)
	if pushRec.Code != http.StatusOK {
		t.Fatalf("push status = %d (body %s)", pushRec.Code, pushRec.Body.String())
	}

	resolveRec := httptest.NewRecorder()
	resolveReq := newJSONRequest(http.MethodPost, "/v1/projects/"+created.ProjectID+"/resolve", secrets.ResolveRequest{})
	resolveReq.Header.Set("Authorization", "Bearer "+sessionToken)
	server.Handler().ServeHTTP(resolveRec, resolveReq)
	if resolveRec.Code != http.StatusOK {
		t.Fatalf("resolve status = %d (body %s)", resolveRec.Code, resolveRec.Body.String())
	}

	// Positive control: the value really was delivered to the caller.
	if !strings.Contains(resolveRec.Body.String(), secretValue) {
		t.Fatal("the resolved response does not contain the pushed value; the canary proves nothing")
	}
	// Negative assertion through the same plumbing.
	logged := logs.String()
	if logged == "" {
		t.Fatal("no audit events were written; the canary proves nothing")
	}
	for _, forbidden := range []string{secretValue, secretName, sessionToken} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("audit logs contain %q; logs must never carry secret values, names or tokens", forbidden)
		}
	}
	if !strings.Contains(logged, "secrets.resolved") {
		t.Fatal("audit logs do not contain the resolution event")
	}
}

func TestScalewayStoreMapsUpstreamFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	store, err := NewScalewayStore(server.URL, "scw-secret", "project-uuid", "fr-par", server.Client())
	if err != nil {
		t.Fatalf("NewScalewayStore: %v", err)
	}
	_, _, err = store.ReadACL(t.Context(), "33333333-3333-4333-8333-333333333333")
	if !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("ReadACL(upstream 500) = %v, want ErrStoreUnavailable", err)
	}

	// An unreachable endpoint is also an availability failure, never a panic.
	store.httpClient = &http.Client{Timeout: time.Millisecond, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	})}
	if _, _, err := store.ReadACL(t.Context(), "33333333-3333-4333-8333-333333333333"); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("ReadACL(transport error) = %v, want ErrStoreUnavailable", err)
	}

	// Invalid configuration is refused at construction.
	if _, err := NewScalewayStore(server.URL, "", "p", "fr-par", nil); err == nil {
		t.Fatal("an empty secret key was accepted")
	}
	if _, err := NewScalewayStore(server.URL, "k", "p", "mars-1", nil); err == nil {
		t.Fatal("an unsupported region was accepted")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRateLimiterBoundsAndRecovers(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	limiter := NewRateLimiter(2, time.Minute, 8, clock)

	if ok, _ := limiter.Allow("k"); !ok {
		t.Fatal("first request denied")
	}
	if ok, _ := limiter.Allow("k"); !ok {
		t.Fatal("second request denied")
	}
	ok, retry := limiter.Allow("k")
	if ok {
		t.Fatal("third request allowed above the limit")
	}
	if retry <= 0 {
		t.Fatal("denied request reported no retry delay")
	}
	now = now.Add(61 * time.Second)
	if ok, _ := limiter.Allow("k"); !ok {
		t.Fatal("request denied after the window elapsed")
	}
	// Distinct keys are independent.
	if ok, _ := limiter.Allow("other"); !ok {
		t.Fatal("a distinct key was denied")
	}
}

func TestAttemptStoreIsSingleUseAndBounded(t *testing.T) {
	store := NewAttemptStore(time.Minute, 2, time.Now)
	if err := store.Put(&LoginAttempt{State: "a"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, ok := store.Take("a"); !ok {
		t.Fatal("attempt 'a' was not found")
	}
	if _, ok := store.Take("a"); ok {
		t.Fatal("attempt 'a' was usable twice; state must be single-use")
	}
	if err := store.Put(&LoginAttempt{State: "b"}); err != nil {
		t.Fatalf("Put b: %v", err)
	}
	if err := store.Put(&LoginAttempt{State: "c"}); err != nil {
		t.Fatalf("Put c: %v", err)
	}
	if err := store.Put(&LoginAttempt{State: "d"}); err == nil {
		t.Fatal("the attempt store grew past its bound")
	}
}
