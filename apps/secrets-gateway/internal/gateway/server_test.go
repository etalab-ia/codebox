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

// TestLoopbackLoginRedirectsTheBrowser covers the finding that the callback
// returned JSON: the browser must be navigated to the CLI's loopback listener.
func TestLoopbackLoginRedirectsTheBrowser(t *testing.T) {
	ts := newTestServer(t, Identity{Subject: "s1", Email: "dev@beta.gouv.fr", ACR: "eidas2"})
	handler := ts.server.Handler()

	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, httptest.NewRequest(http.MethodGet, "/auth/login?port=53682", nil))
	if loginRec.Code != http.StatusFound {
		t.Fatalf("login status = %d, want 302", loginRec.Code)
	}

	// Recover the attempt's state from the provider redirect the service issued.
	state := ts.provider.lastState
	if state == "" {
		t.Fatal("no login state was recorded")
	}

	cbRec := httptest.NewRecorder()
	handler.ServeHTTP(cbRec, httptest.NewRequest(http.MethodGet, "/auth/callback?state="+state+"&code=abc", nil))
	if cbRec.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want 302 to the loopback listener (body %s)", cbRec.Code, cbRec.Body.String())
	}
	location := cbRec.Header().Get("Location")
	if !strings.HasPrefix(location, "http://127.0.0.1:53682/callback?") {
		t.Fatalf("callback Location = %q, want the loopback listener", location)
	}
	if !strings.Contains(location, "token=") {
		t.Fatalf("callback Location %q carries no token", location)
	}
}

// TestTerminalLoginIssuesAPasteableCode covers the finding that the terminal
// flow was unreachable because every accepted port was non-zero.
func TestTerminalLoginIssuesAPasteableCode(t *testing.T) {
	ts := newTestServer(t, Identity{Subject: "s1", Email: "dev@beta.gouv.fr", ACR: "eidas2"})
	handler := ts.server.Handler()

	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, httptest.NewRequest(http.MethodGet, "/auth/login?mode=terminal", nil))
	if loginRec.Code != http.StatusFound {
		t.Fatalf("terminal login status = %d, want 302 (body %s)", loginRec.Code, loginRec.Body.String())
	}
	state := ts.provider.lastState

	cbRec := httptest.NewRecorder()
	handler.ServeHTTP(cbRec, httptest.NewRequest(http.MethodGet, "/auth/callback?state="+state+"&code=abc", nil))
	if cbRec.Code != http.StatusOK {
		t.Fatalf("terminal callback status = %d, want 200 with a code (body %s)", cbRec.Code, cbRec.Body.String())
	}
	var issued map[string]string
	decodeJSON(t, cbRec, &issued)
	if issued["code"] == "" || issued["expiresAt"] == "" {
		t.Fatalf("terminal callback issued %+v, want a code and expiry", issued)
	}

	// The pasted code exchanges for a session token.
	exchangeRec := httptest.NewRecorder()
	exchangeReq := newJSONRequest(http.MethodPost, "/auth/device/token", secrets.DeviceTokenExchangeRequest{SessionID: issued["code"]})
	handler.ServeHTTP(exchangeRec, exchangeReq)
	if exchangeRec.Code != http.StatusOK {
		t.Fatalf("device token status = %d, want 200 (body %s)", exchangeRec.Code, exchangeRec.Body.String())
	}
	var token secrets.TokenResponse
	decodeJSON(t, exchangeRec, &token)
	if token.Token == "" || token.Identity.Email != "dev@beta.gouv.fr" {
		t.Fatalf("device token response = %+v", token)
	}
	// And that session works on an authenticated route.
	whoRec := httptest.NewRecorder()
	whoReq := httptest.NewRequest(http.MethodGet, "/v1/whoami", nil)
	whoReq.Header.Set("Authorization", "Bearer "+token.Token)
	handler.ServeHTTP(whoRec, whoReq)
	if whoRec.Code != http.StatusOK {
		t.Fatalf("whoami with the exchanged token = %d, want 200", whoRec.Code)
	}

	// A ticket is single-use: replaying the same code must be refused.
	replayRec := httptest.NewRecorder()
	replayReq := newJSONRequest(http.MethodPost, "/auth/device/token", secrets.DeviceTokenExchangeRequest{SessionID: issued["code"]})
	handler.ServeHTTP(replayRec, replayReq)
	if replayRec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed ticket status = %d, want 401 (body %s)", replayRec.Code, replayRec.Body.String())
	}
	var replayErr secrets.APIError
	decodeJSON(t, replayRec, &replayErr)
	if replayErr.Code != secrets.ErrCodeUnauthorized {
		t.Fatalf("replay error code = %q, want %q", replayErr.Code, secrets.ErrCodeUnauthorized)
	}

	// Terminal mode rejects a loopback port, and browser mode still requires one.
	for _, target := range []string{"/auth/login?mode=terminal&port=53682", "/auth/login?mode=browser"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400", target, rec.Code)
		}
	}
}

// TestMemberListEntriesAndRevisionComeFromOneRead covers the finding that the
// entries and the revision could be read separately and disagree.
func TestMemberListEntriesAndRevisionComeFromOneRead(t *testing.T) {
	ts := newTestServer(t, Identity{})
	owner := TokenClaims{Subject: "s1", Email: "owner@beta.gouv.fr", ACR: "eidas2"}
	projectID := createProjectFor(t, ts, owner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/projects/"+projectID+"/members", nil)
	req.Header.Set("Authorization", "Bearer "+ts.sessionToken(t, owner))
	ts.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var members secrets.MembersResponse
	decodeJSON(t, rec, &members)
	if members.Revision != 1 {
		t.Fatalf("revision = %d, want 1 (the revision the entries were read at)", members.Revision)
	}
	// The returned entries must authorise the owner they claim, i.e. they came
	// from the same snapshot as the revision.
	acl, err := secrets.NewACL(members.Entries)
	if err != nil {
		t.Fatalf("returned entries do not form a valid ACL: %v", err)
	}
	if role, ok := acl.Authorize(owner.Email, ""); !ok || role != secrets.RoleOwner {
		t.Fatalf("returned entries do not authorise the owner: (%q, %v)", role, ok)
	}
}
