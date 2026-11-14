package gateway

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/etalab-ia/codebox/packages/secrets"
)

func TestHealthReportsConfiguredStore(t *testing.T) {
	ts := newTestServer(t, Identity{Email: "dev@beta.gouv.fr", Subject: "sub-1", ACR: "eidas2"})
	rec := httptest.NewRecorder()
	ts.server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"store":"memory"`) {
		t.Fatalf("health body = %s, want the store kind", rec.Body.String())
	}
}

func TestUnauthenticatedRequestIsRejected(t *testing.T) {
	ts := newTestServer(t, Identity{})
	rec := httptest.NewRecorder()
	ts.server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/whoami", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	var apiErr secrets.APIError
	decodeJSON(t, rec, &apiErr)
	if apiErr.Code != secrets.ErrCodeUnauthorized {
		t.Fatalf("error code = %q, want %q", apiErr.Code, secrets.ErrCodeUnauthorized)
	}
}

func TestForgedAndExpiredSessionsAreRejected(t *testing.T) {
	ts := newTestServer(t, Identity{Email: "dev@beta.gouv.fr", Subject: "sub-1", ACR: "eidas2"})
	valid := ts.sessionToken(t, TokenClaims{Subject: "sub-1", Email: "dev@beta.gouv.fr", ACR: "eidas2"})

	tampered := valid[:len(valid)-4] + "AAAA"
	if claims, err := ts.tokens.Verify(tampered, tokenPurposeSession); err == nil {
		t.Fatalf("tampered token verified as %+v", claims)
	}
	// A ticket must not be usable as a session token.
	ticket, _, err := ts.tokens.IssueTicket(TokenClaims{Subject: "sub-1", Email: "dev@beta.gouv.fr"}, time.Minute)
	if err != nil {
		t.Fatalf("IssueTicket: %v", err)
	}
	if _, err := ts.tokens.Verify(ticket, tokenPurposeSession); err == nil {
		t.Fatal("a login ticket was accepted as a session token")
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	base := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	tokens, err := NewTokenIssuer(testSeed, "https://gateway.test", time.Hour, func() time.Time { return base })
	if err != nil {
		t.Fatalf("NewTokenIssuer: %v", err)
	}
	token, _, err := tokens.IssueSession(TokenClaims{Subject: "s", Email: "dev@beta.gouv.fr", ACR: "eidas2"})
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	later, err := NewTokenIssuer(testSeed, "https://gateway.test", time.Hour, func() time.Time { return base.Add(2 * time.Hour) })
	if err != nil {
		t.Fatalf("NewTokenIssuer: %v", err)
	}
	if _, err := later.Verify(token, tokenPurposeSession); err == nil {
		t.Fatal("an expired token verified")
	}
}

func TestResolveRequiresProjectMembership(t *testing.T) {
	ts := newTestServer(t, Identity{})
	owner := TokenClaims{Subject: "s1", Email: "owner@beta.gouv.fr", ACR: "eidas2"}
	projectID := createProjectFor(t, ts, owner)

	// A stranger gets 403, not a 404: the project exists.
	stranger := ts.sessionToken(t, TokenClaims{Subject: "s2", Email: "stranger@example.com", ACR: "eidas2"})
	rec := httptest.NewRecorder()
	req := newJSONRequest(http.MethodPost, "/v1/projects/"+projectID+"/resolve", secrets.ResolveRequest{})
	req.Header.Set("Authorization", "Bearer "+stranger)
	ts.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}

	// The owner resolves the (empty) bundle.
	rec = httptest.NewRecorder()
	req = newJSONRequest(http.MethodPost, "/v1/projects/"+projectID+"/resolve", secrets.ResolveRequest{})
	req.Header.Set("Authorization", "Bearer "+ts.sessionToken(t, owner))
	ts.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resolved secrets.ResolveResponse
	decodeJSON(t, rec, &resolved)
	if resolved.ProjectID != projectID || resolved.Revision != 1 {
		t.Fatalf("resolved = %+v, want the project at revision 1", resolved)
	}
}

func TestPushSecretsIsOwnerOnlyAndRevisionChecked(t *testing.T) {
	ts := newTestServer(t, Identity{})
	owner := TokenClaims{Subject: "s1", Email: "owner@beta.gouv.fr", ACR: "eidas2"}
	projectID := createProjectFor(t, ts, owner)

	// Add a member.
	rec := httptest.NewRecorder()
	req := newJSONRequest(http.MethodPut, "/v1/projects/"+projectID+"/members", secrets.UpdateMembersRequest{
		BaseRevision: 1,
		Entries: []secrets.ACLEntry{
			{Kind: secrets.EntryOwner, Value: owner.Email},
			{Kind: secrets.EntryMember, Value: "reader@beta.gouv.fr"},
		},
	})
	req.Header.Set("Authorization", "Bearer "+ts.sessionToken(t, owner))
	ts.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("member update status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	reader := TokenClaims{Subject: "s2", Email: "reader@beta.gouv.fr", ACR: "eidas2"}
	push := secrets.PushSecretsRequest{
		BaseRevision: 1,
		Components:   map[string]map[string]string{".env": {"API_KEY": "super-secret"}},
	}
	rec = httptest.NewRecorder()
	req = newJSONRequest(http.MethodPut, "/v1/projects/"+projectID+"/secrets", push)
	req.Header.Set("Authorization", "Bearer "+ts.sessionToken(t, reader))
	ts.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member push status = %d, want 403", rec.Code)
	}

	// The owner pushes, then a stale base revision conflicts.
	rec = httptest.NewRecorder()
	req = newJSONRequest(http.MethodPut, "/v1/projects/"+projectID+"/secrets", push)
	req.Header.Set("Authorization", "Bearer "+ts.sessionToken(t, owner))
	ts.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner push status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	req = newJSONRequest(http.MethodPut, "/v1/projects/"+projectID+"/secrets", push)
	req.Header.Set("Authorization", "Bearer "+ts.sessionToken(t, owner))
	ts.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale push status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}

	// The reader can resolve the value the owner pushed.
	rec = httptest.NewRecorder()
	req = newJSONRequest(http.MethodPost, "/v1/projects/"+projectID+"/resolve", secrets.ResolveRequest{})
	req.Header.Set("Authorization", "Bearer "+ts.sessionToken(t, reader))
	ts.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reader resolve status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resolved secrets.ResolveResponse
	decodeJSON(t, rec, &resolved)
	if resolved.Components[".env"]["API_KEY"] != "super-secret" {
		t.Fatalf("resolved components = %+v, want the pushed value", resolved.Components)
	}
}

func TestProductionScopeIsRejected(t *testing.T) {
	ts := newTestServer(t, Identity{})
	owner := TokenClaims{Subject: "s1", Email: "owner@beta.gouv.fr", ACR: "eidas2"}
	projectID := createProjectFor(t, ts, owner)

	for _, scope := range []string{"production", "prod", "api-prod"} {
		rec := httptest.NewRecorder()
		req := newJSONRequest(http.MethodPut, "/v1/projects/"+projectID+"/secrets", secrets.PushSecretsRequest{
			Scope:        scope,
			BaseRevision: 1,
			Components:   map[string]map[string]string{".env": {"A": "1"}},
		})
		req.Header.Set("Authorization", "Bearer "+ts.sessionToken(t, owner))
		ts.server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("scope %q status = %d, want 400", scope, rec.Code)
			continue
		}
		var apiErr secrets.APIError
		decodeJSON(t, rec, &apiErr)
		if apiErr.Code != secrets.ErrCodeProductionForbidden {
			t.Errorf("scope %q code = %q, want %q", scope, apiErr.Code, secrets.ErrCodeProductionForbidden)
		}
	}
}

func TestMemberUpdateCannotRemoveTheLastOwner(t *testing.T) {
	ts := newTestServer(t, Identity{})
	owner := TokenClaims{Subject: "s1", Email: "owner@beta.gouv.fr", ACR: "eidas2"}
	projectID := createProjectFor(t, ts, owner)

	rec := httptest.NewRecorder()
	req := newJSONRequest(http.MethodPut, "/v1/projects/"+projectID+"/members", secrets.UpdateMembersRequest{
		BaseRevision: 1,
		Entries:      []secrets.ACLEntry{{Kind: secrets.EntryMember, Value: "someone@beta.gouv.fr"}},
	})
	req.Header.Set("Authorization", "Bearer "+ts.sessionToken(t, owner))
	ts.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestCreateProjectRequiresEligibility(t *testing.T) {
	ts := newTestServer(t, Identity{})
	ts.provider.createErr = errors.New("project creation is restricted to recognised public-sector organisations")
	claims := TokenClaims{Subject: "s1", Email: "outsider@example.com", ACR: "eidas2"}
	rec := httptest.NewRecorder()
	req := newJSONRequest(http.MethodPost, "/v1/projects", secrets.CreateProjectRequest{})
	req.Header.Set("Authorization", "Bearer "+ts.sessionToken(t, claims))
	ts.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestStoreOutageMapsToServiceUnavailable(t *testing.T) {
	ts := newTestServer(t, Identity{})
	owner := TokenClaims{Subject: "s1", Email: "owner@beta.gouv.fr", ACR: "eidas2"}
	projectID := createProjectFor(t, ts, owner)
	ts.store.readErr = ErrStoreUnavailable

	rec := httptest.NewRecorder()
	req := newJSONRequest(http.MethodPost, "/v1/projects/"+projectID+"/resolve", secrets.ResolveRequest{})
	req.Header.Set("Authorization", "Bearer "+ts.sessionToken(t, owner))
	ts.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestLoginRequiresLoopbackPort(t *testing.T) {
	ts := newTestServer(t, Identity{})
	rec := httptest.NewRecorder()
	ts.server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	for _, port := range []string{"80", "abc", "70000"} {
		rec = httptest.NewRecorder()
		ts.server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/login?port="+port, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("port %q status = %d, want 400", port, rec.Code)
		}
	}
}

func createProjectFor(t *testing.T, ts *testServer, claims TokenClaims) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := newJSONRequest(http.MethodPost, "/v1/projects", secrets.CreateProjectRequest{})
	req.Header.Set("Authorization", "Bearer "+ts.sessionToken(t, claims))
	ts.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	var created secrets.CreateProjectResponse
	decodeJSON(t, rec, &created)
	if err := secrets.ValidateProjectID(created.ProjectID); err != nil {
		t.Fatalf("created project ID %q is invalid: %v", created.ProjectID, err)
	}
	return created.ProjectID
}
