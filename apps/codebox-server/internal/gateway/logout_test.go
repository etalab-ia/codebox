package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func startTestLogout(t *testing.T, ts *testServer, sessionToken, idToken string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"idToken": idToken})
	if err != nil {
		t.Fatalf("marshal logout request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	rec := httptest.NewRecorder()
	ts.server.Handler().ServeHTTP(rec, req)
	return rec
}

func TestLogoutCallbackStateIsOutstandingAndSingleUse(t *testing.T) {
	ts := newTestServer(t, Identity{Subject: "sub-1", Email: "dev@beta.gouv.fr"})
	session := ts.sessionToken(t, TokenClaims{Subject: "sub-1", Email: "dev@beta.gouv.fr", ACR: "eidas2"})
	const idToken = "id-token-logout-canary"
	rec := startTestLogout(t, ts, session, idToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout start status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var response struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.URL == "" {
		t.Fatalf("logout response = %q, %v", rec.Body.String(), err)
	}
	state := ts.provider.lastLogoutState
	if len(state) < 32 {
		t.Fatalf("logout state length = %d, want at least 32", len(state))
	}
	if ts.provider.lastLogoutIDToken != idToken {
		t.Fatal("the retained ID token was not passed to the provider logout flow")
	}
	wantOpaqueURL := "https://gateway.test/auth/logout/redirect?state=" + url.QueryEscape(state)
	if response.URL != wantOpaqueURL || strings.Contains(response.URL, idToken) {
		t.Fatalf("logout start returned a non-opaque URL %q, want %q", response.URL, wantOpaqueURL)
	}
	redirect := httptest.NewRecorder()
	ts.server.Handler().ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "/auth/logout/redirect?state="+url.QueryEscape(state), nil))
	if redirect.Code != http.StatusFound {
		t.Fatalf("opaque redirect status = %d (body %s)", redirect.Code, redirect.Body.String())
	}
	providerLocation := redirect.Header().Get("Location")
	if !strings.Contains(providerLocation, "id_token_hint="+idToken) || !strings.Contains(providerLocation, "state="+state) {
		t.Fatalf("provider redirect lacks the documented parameters: %q", providerLocation)
	}

	callback := func(state string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/auth/logout/callback?state="+state, nil)
		rec := httptest.NewRecorder()
		ts.server.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := callback(state); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Déconnexion") {
		t.Fatalf("logout callback = %d %q", rec.Code, rec.Body.String())
	}
	if rec := callback(state); rec.Code != http.StatusBadRequest {
		t.Fatalf("replayed logout state status = %d, want 400", rec.Code)
	}
	if rec := callback("unknown-state-that-is-long-enough-to-be-invalid"); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown logout state status = %d, want 400", rec.Code)
	}
}

func TestLogoutCallbackRejectsExpiredState(t *testing.T) {
	ts := newTestServer(t, Identity{Subject: "sub-1", Email: "dev@beta.gouv.fr"})
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	ts.server.attempts = NewAttemptStore(time.Minute, 16, func() time.Time { return now })
	session := ts.sessionToken(t, TokenClaims{Subject: "sub-1", Email: "dev@beta.gouv.fr", ACR: "eidas2"})
	rec := startTestLogout(t, ts, session, "id-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("logout start status = %d", rec.Code)
	}
	now = now.Add(2 * time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/auth/logout/callback?state="+ts.provider.lastLogoutState, nil)
	callback := httptest.NewRecorder()
	ts.server.Handler().ServeHTTP(callback, req)
	if callback.Code != http.StatusBadRequest {
		t.Fatalf("expired logout state status = %d, want 400", callback.Code)
	}
}

func TestLogoutStartAllowsExpiredGatewaySession(t *testing.T) {
	ts := newTestServer(t, Identity{Subject: "sub-1", Email: "dev@beta.gouv.fr"})
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	tokens, err := NewTokenIssuer(testSeed, "https://gateway.test", time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewTokenIssuer: %v", err)
	}
	ts.tokens = tokens
	ts.server.tokens = tokens
	session := ts.sessionToken(t, TokenClaims{Subject: "sub-1", Email: "dev@beta.gouv.fr", ACR: "eidas2"})
	now = now.Add(2 * time.Hour)
	rec := startTestLogout(t, ts, session, "id-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("logout with expired gateway session status = %d (body %s)", rec.Code, rec.Body.String())
	}
}

func TestLogoutStartFailureDoesNotLeaveUsableState(t *testing.T) {
	ts := newTestServer(t, Identity{Subject: "sub-1", Email: "dev@beta.gouv.fr"})
	ts.provider.logoutErr = errors.New("discovery unavailable")
	session := ts.sessionToken(t, TokenClaims{Subject: "sub-1", Email: "dev@beta.gouv.fr", ACR: "eidas2"})
	rec := startTestLogout(t, ts, session, "id-token")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("logout start status = %d, want 503", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/logout/callback?state="+ts.provider.lastLogoutState, nil)
	callback := httptest.NewRecorder()
	ts.server.Handler().ServeHTTP(callback, req)
	if callback.Code != http.StatusBadRequest {
		t.Fatalf("failed logout left a usable callback state: %d", callback.Code)
	}
}

func TestLogoutAuditLogsRedactTokens(t *testing.T) {
	ts := newTestServer(t, Identity{Subject: "sub-1", Email: "dev@beta.gouv.fr"})
	var logs bytes.Buffer
	ts.server.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	session := ts.sessionToken(t, TokenClaims{Subject: "sub-1", Email: "dev@beta.gouv.fr", ACR: "eidas2"})
	const idToken = "id-token-logout-canary"
	rec := startTestLogout(t, ts, session, idToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout start status = %d", rec.Code)
	}
	redirect := httptest.NewRecorder()
	ts.server.Handler().ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "/auth/logout/redirect?state="+url.QueryEscape(ts.provider.lastLogoutState), nil))
	if redirect.Code != http.StatusFound {
		t.Fatalf("opaque redirect status = %d", redirect.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/logout/callback?state="+ts.provider.lastLogoutState, nil)
	callback := httptest.NewRecorder()
	ts.server.Handler().ServeHTTP(callback, req)
	if callback.Code != http.StatusOK {
		t.Fatalf("logout callback status = %d", callback.Code)
	}
	logged := logs.String()
	if !strings.Contains(logged, "logout.started") || !strings.Contains(logged, "logout.redirected") || !strings.Contains(logged, "logout.completed") {
		t.Fatalf("logout audit events are absent: %q", logged)
	}
	for _, forbidden := range []string{idToken, session} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("audit logs contain forbidden token %q", forbidden)
		}
	}
}

func TestOIDCLogoutURLUsesDocumentedParameters(t *testing.T) {
	var issuer string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 issuer,
			"authorization_endpoint": issuer + "/authorize",
			"token_endpoint":         issuer + "/token",
			"userinfo_endpoint":      issuer + "/userinfo",
			"jwks_uri":               issuer + "/jwks",
			"end_session_endpoint":   issuer + "/session/end",
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	issuer = server.URL + "/api/v2"
	client, err := NewOIDCClient(OIDCConfig{
		Issuer: issuer, ClientID: "client", ClientSecret: "secret",
		RedirectURL: "https://gateway.test/auth/callback",
	}, server.Client(), time.Now)
	if err != nil {
		t.Fatalf("NewOIDCClient: %v", err)
	}
	state := strings.Repeat("s", 43)
	raw, err := client.LogoutURL(t.Context(), state, "header.payload.signature")
	if err != nil {
		t.Fatalf("LogoutURL: %v", err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse logout URL: %v", err)
	}
	if parsed.Path != "/api/v2/session/end" {
		t.Fatalf("end-session path = %q", parsed.Path)
	}
	query := parsed.Query()
	if query.Get("id_token_hint") != "header.payload.signature" || query.Get("state") != state {
		t.Fatalf("logout query = %v", query)
	}
	if got := query.Get("post_logout_redirect_uri"); got != "https://gateway.test/auth/logout/callback" {
		t.Fatalf("post_logout_redirect_uri = %q", got)
	}
	if _, err := client.LogoutURL(t.Context(), "short", "header.payload.signature"); err == nil {
		t.Fatal("a short logout state was accepted")
	}
}

func TestSessionTokenOmitsIDTokenWhileTerminalTicketRetainsIt(t *testing.T) {
	tokens, err := NewTokenIssuer(testSeed, "https://gateway.test", time.Hour, time.Now)
	if err != nil {
		t.Fatalf("NewTokenIssuer: %v", err)
	}
	claims := TokenClaims{Subject: "sub-1", Email: "dev@beta.gouv.fr", IDToken: "id-token-canary"}
	session, _, err := tokens.IssueSession(claims)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	verifiedSession, err := tokens.Verify(session, tokenPurposeSession)
	if err != nil {
		t.Fatalf("Verify session: %v", err)
	}
	if verifiedSession.IDToken != "" {
		t.Fatal("the gateway session retained the ProConnect ID token")
	}
	parts := strings.Split(session, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode session payload: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode session claims: %v", err)
	}
	if _, present := decoded["id_token"]; present {
		t.Fatal("encoded gateway session contains id_token")
	}

	// Positive control through the same token plumbing: terminal login can retain it.
	ticket, _, err := tokens.IssueTicket(claims, time.Minute)
	if err != nil {
		t.Fatalf("IssueTicket: %v", err)
	}
	verifiedTicket, err := tokens.Verify(ticket, tokenPurposeTicket)
	if err != nil {
		t.Fatalf("Verify ticket: %v", err)
	}
	if verifiedTicket.IDToken != "id-token-canary" {
		t.Fatalf("ticket ID token = %q", verifiedTicket.IDToken)
	}
}

func TestLoginRetainsIDTokenForBrowserAndTerminal(t *testing.T) {
	ts := newTestServer(t, Identity{
		Subject: "sub-1", Email: "dev@beta.gouv.fr", ACR: "eidas2", IDToken: "id-token-canary",
	})

	loopbackState := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	login := httptest.NewRecorder()
	ts.server.Handler().ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/auth/login?port=12345&loopback_state="+loopbackState, nil))
	if login.Code != http.StatusFound {
		t.Fatalf("browser login status = %d", login.Code)
	}
	callback := httptest.NewRecorder()
	ts.server.Handler().ServeHTTP(callback, httptest.NewRequest(http.MethodGet, "/auth/callback?state="+url.QueryEscape(ts.provider.lastState)+"&code=browser-code", nil))
	if callback.Code != http.StatusFound {
		t.Fatalf("browser callback status = %d (body %s)", callback.Code, callback.Body.String())
	}
	target, err := url.Parse(callback.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse loopback redirect: %v", err)
	}
	if target.Query().Get("id_token") != "id-token-canary" || target.Query().Get("state") != loopbackState {
		t.Fatalf("loopback query = %v", target.Query())
	}
	session := target.Query().Get("token")
	verifiedSession, err := ts.tokens.Verify(session, tokenPurposeSession)
	if err != nil || verifiedSession.IDToken != "" {
		t.Fatalf("browser session ID token = %q, err = %v", verifiedSession.IDToken, err)
	}

	terminalLogin := httptest.NewRecorder()
	ts.server.Handler().ServeHTTP(terminalLogin, httptest.NewRequest(http.MethodGet, "/auth/login?mode=terminal", nil))
	if terminalLogin.Code != http.StatusFound {
		t.Fatalf("terminal login status = %d", terminalLogin.Code)
	}
	terminalCallback := httptest.NewRecorder()
	ts.server.Handler().ServeHTTP(terminalCallback, httptest.NewRequest(http.MethodGet, "/auth/callback?state="+url.QueryEscape(ts.provider.lastState)+"&code=terminal-code", nil))
	if terminalCallback.Code != http.StatusOK {
		t.Fatalf("terminal callback status = %d", terminalCallback.Code)
	}
	var issued struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(terminalCallback.Body.Bytes(), &issued); err != nil || issued.Code == "" {
		t.Fatalf("terminal ticket = %q, %v", terminalCallback.Body.String(), err)
	}
	body, err := json.Marshal(map[string]string{"sessionId": issued.Code})
	if err != nil {
		t.Fatalf("marshal device exchange: %v", err)
	}
	device := httptest.NewRecorder()
	ts.server.Handler().ServeHTTP(device, httptest.NewRequest(http.MethodPost, "/auth/device/token", bytes.NewReader(body)))
	if device.Code != http.StatusOK {
		t.Fatalf("device exchange status = %d (body %s)", device.Code, device.Body.String())
	}
	var tokenResponse struct {
		Token   string `json:"token"`
		IDToken string `json:"idToken"`
	}
	if err := json.Unmarshal(device.Body.Bytes(), &tokenResponse); err != nil {
		t.Fatalf("decode device exchange: %v", err)
	}
	if tokenResponse.IDToken != "id-token-canary" || tokenResponse.Token == "" {
		t.Fatalf("device exchange did not retain the credentials: %+v", tokenResponse)
	}
	verifiedSession, err = ts.tokens.Verify(tokenResponse.Token, tokenPurposeSession)
	if err != nil || verifiedSession.IDToken != "" {
		t.Fatalf("terminal session ID token = %q, err = %v", verifiedSession.IDToken, err)
	}
}
