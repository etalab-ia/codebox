package gateway

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/etalab-ia/codebox/packages/secrets"
)

// Config is the gateway's runtime configuration, sourced from the environment.
type Config struct {
	// ListenAddr is the bind address, for example ":8080".
	ListenAddr string
	// PublicURL is the externally reachable origin, used to build the
	// ProConnect redirect and to validate the loopback return port.
	PublicURL string
	// TokenSeedB64 seeds the Ed25519 token key (base64url, no padding).
	TokenSeedB64 string
	// TokenLifetime is the session token lifetime.
	TokenLifetime time.Duration
	// TicketLifetime bounds the terminal paste-the-code flow.
	TicketLifetime time.Duration
	// OIDC is the ProConnect client configuration.
	OIDC OIDCConfig
	// AuditLogger receives structured, value-free audit events.
	AuditLogger *slog.Logger
}

// Server wires the gateway's HTTP surface to its collaborators. Every field is
// injectable so tests exercise the real handlers without network or clocks.
type Server struct {
	cfg      Config
	identity IdentityProvider
	store    SecretStore
	tokens   *TokenIssuer
	attempts *AttemptStore
	limiter  *RateLimiter
	logger   *slog.Logger
	now      func() time.Time
}

// NewServer validates configuration and returns a Server.
func NewServer(cfg Config, identity IdentityProvider, store SecretStore, tokens *TokenIssuer, attempts *AttemptStore, limiter *RateLimiter, now func() time.Time) (*Server, error) {
	if identity == nil || store == nil || tokens == nil || attempts == nil || limiter == nil {
		return nil, errors.New("gateway requires an identity provider, a secret store, a token issuer, an attempt store and a rate limiter")
	}
	if _, err := url.ParseRequestURI(cfg.PublicURL); err != nil {
		return nil, fmt.Errorf("public URL is not a valid absolute URL: %w", err)
	}
	if now == nil {
		now = time.Now
	}
	logger := cfg.AuditLogger
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{cfg: cfg, identity: identity, store: store, tokens: tokens, attempts: attempts, limiter: limiter, logger: logger, now: now}, nil
}

// Handler returns the routed HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /auth/login", s.handleLogin)
	mux.HandleFunc("GET /auth/callback", s.handleCallback)
	mux.HandleFunc("GET /auth/loopback", s.handleLoopback)
	mux.HandleFunc("POST /auth/device/token", s.handleDeviceToken)
	mux.Handle("GET /v1/whoami", s.authenticated(s.handleWhoAmI))
	mux.Handle("POST /v1/projects", s.authenticated(s.handleCreateProject))
	mux.Handle("GET /v1/projects/{id}/members", s.authenticated(s.handleListMembers))
	mux.Handle("PUT /v1/projects/{id}/members", s.authenticated(s.handleUpdateMembers))
	mux.Handle("PUT /v1/projects/{id}/secrets", s.authenticated(s.handlePushSecrets))
	mux.Handle("POST /v1/projects/{id}/resolve", s.authenticated(s.handleResolve))
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "store": s.store.Kind()})
}

// loginParams are parsed from the query string of /auth/login.
type loginParams struct {
	port int
	// terminal selects the paste-the-code flow used over SSH, where the CLI
	// cannot receive a loopback redirect from the user's browser.
	terminal bool
}

// parseLoginParams validates the login mode. `mode=terminal` stores a zero
// loopback port so the callback issues a ticket instead of redirecting; the
// default browser mode requires the port the CLI is listening on. The port is
// the only caller-supplied value in the flow: it decides where the browser is
// redirected after ProConnect, never who may authenticate.
func parseLoginParams(q url.Values) (loginParams, error) {
	var p loginParams
	switch mode := q.Get("mode"); mode {
	case "", "browser":
	case "terminal":
		p.terminal = true
	default:
		return p, fmt.Errorf("unsupported login mode %q", mode)
	}
	raw := q.Get("port")
	if raw == "" {
		if p.terminal {
			return p, nil
		}
		return p, errors.New("a loopback port is required")
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1024 || port > 65535 {
		return p, errors.New("port must be an integer between 1024 and 65535")
	}
	if p.terminal {
		return p, errors.New("terminal mode does not use a loopback port")
	}
	p.port = port
	return p, nil
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if ok, retry := s.limiter.Allow("login:" + ClientIP(r)); !ok {
		writeError(w, http.StatusTooManyRequests, secrets.ErrCodeRateLimited, "too many login attempts, retry in "+retry.Round(time.Second).String())
		return
	}
	params, err := parseLoginParams(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, secrets.ErrCodeInvalidRequest, err.Error())
		return
	}
	state, err := randomURLToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not start a login")
		return
	}
	nonce, err := randomURLToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not start a login")
		return
	}
	verifier, err := randomURLToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not start a login")
		return
	}
	attempt := &LoginAttempt{State: state, Nonce: nonce, CodeVerifier: verifier, LoopbackPort: params.port}
	if err := s.attempts.Put(attempt); err != nil {
		writeError(w, http.StatusTooManyRequests, secrets.ErrCodeRateLimited, "too many logins in progress; retry shortly")
		return
	}
	challenge := sha256.Sum256([]byte(verifier))
	authorizeURL, err := s.identity.AuthorizeURL(r.Context(), state, nonce, base64.RawURLEncoding.EncodeToString(challenge[:]))
	if err != nil {
		s.audit(r, "login.start_failed", slog.String("reason", "identity_provider_unavailable"))
		writeError(w, http.StatusServiceUnavailable, secrets.ErrCodeUpstreamUnavailable, "ProConnect is unreachable; retry shortly")
		return
	}
	s.audit(r, "login.started", slog.Int("port", params.port), slog.Bool("terminal", params.terminal))
	http.Redirect(w, r, authorizeURL, http.StatusFound)
}

// authenticated rejects tokenless, malformed or expired sessions before the
// handler runs, and hands the handler the verified claims.
func (s *Server) authenticated(next func(http.ResponseWriter, *http.Request, TokenClaims)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || strings.TrimSpace(token) == "" {
			writeError(w, http.StatusUnauthorized, secrets.ErrCodeUnauthorized, "a bearer session token is required")
			return
		}
		claims, err := s.tokens.Verify(strings.TrimSpace(token), tokenPurposeSession)
		if err != nil {
			writeError(w, http.StatusUnauthorized, secrets.ErrCodeUnauthorized, "this session is invalid or expired; run codebox secrets login again")
			return
		}
		s.audit(r, "request.authorized", slog.String("subject", hashPrefix(claims.Email)), slog.String("token", TokenFingerprint(token)), slog.String("path", r.URL.Path))
		next(w, r.WithContext(withClaims(r.Context(), claims)), claims)
	})
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" {
		writeError(w, http.StatusBadRequest, secrets.ErrCodeInvalidRequest, "missing state")
		return
	}
	attempt, ok := s.attempts.Take(state)
	if !ok {
		writeError(w, http.StatusBadRequest, secrets.ErrCodeInvalidRequest, "unknown or expired login; start again from codebox")
		return
	}
	if errCode := q.Get("error"); errCode != "" {
		// Report the provider's error code only: descriptions can embed request
		// fragments, and the user restarts from codebox anyway.
		s.audit(r, "login.provider_error", slog.String("error", errCode))
		writeError(w, http.StatusUnauthorized, secrets.ErrCodeUnauthorized, "ProConnect rejected the login: "+errCode)
		return
	}
	code := q.Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, secrets.ErrCodeInvalidRequest, "missing authorization code")
		return
	}
	identity, err := s.identity.Exchange(r.Context(), code, attempt.CodeVerifier, attempt.Nonce)
	if err != nil {
		if errors.Is(err, ErrMFARequired) {
			s.audit(r, "login.mfa_required")
			writeError(w, http.StatusForbidden, secrets.ErrCodeMFARequired, "two-factor authentication is required; enable it with your organisation account and retry")
			return
		}
		s.audit(r, "login.exchange_failed", slog.String("reason", auditReason(err)))
		writeError(w, http.StatusServiceUnavailable, secrets.ErrCodeUpstreamUnavailable, "ProConnect login could not be verified; retry shortly")
		return
	}
	claims := TokenClaims{Subject: identity.Subject, Email: identity.Email, SIRET: identity.SIRET, ACR: identity.ACR}

	if attempt.LoopbackPort != 0 {
		token, expires, err := s.tokens.IssueSession(claims)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "could not issue a session token")
			return
		}
		target := url.URL{
			Scheme: "http",
			Host:   "127.0.0.1:" + strconv.Itoa(attempt.LoopbackPort),
			Path:   "/callback",
		}
		q := target.Query()
		q.Set("token", token)
		q.Set("expiresAt", expires.UTC().Format(time.RFC3339))
		target.RawQuery = q.Encode()
		s.audit(r, "login.loopback_redirect", slog.String("subject", hashPrefix(claims.Email)))
		// The browser is the user agent here: it must be navigated, not handed
		// JSON. The token therefore travels in the redirect URL to the CLI's
		// loopback listener, which is the only party that reads it.
		http.Redirect(w, r, target.String(), http.StatusFound)
		return
	}

	// Terminal flow: hand the user a one-time code to paste. The paste step is
	// the verifier, and the ticket itself is the bearer: a leaked code is
	// single-use and short-lived, and it only authorises a session for the
	// identity that ProConnect already authenticated.
	ticket, ticketExpiry, err := s.tokens.IssueTicket(claims, s.cfg.TicketLifetime)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not issue a login code")
		return
	}
	s.audit(r, "login.terminal_code_issued", slog.String("subject", hashPrefix(claims.Email)))
	writeJSON(w, http.StatusOK, map[string]string{
		"code":      ticket,
		"expiresAt": ticketExpiry.UTC().Format(time.RFC3339),
	})
}

// handleDeviceToken exchanges a terminal-flow code for a session token.
func (s *Server) handleDeviceToken(w http.ResponseWriter, r *http.Request) {
	if ok, retry := s.limiter.Allow("device:" + ClientIP(r)); !ok {
		writeError(w, http.StatusTooManyRequests, secrets.ErrCodeRateLimited, "too many attempts, retry in "+retry.Round(time.Second).String())
		return
	}
	var req secrets.DeviceTokenExchangeRequest
	if err := decodeStrictJSON(r, &req, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, secrets.ErrCodeInvalidRequest, err.Error())
		return
	}
	// The pasted code is itself the bearer; the ticket purpose check makes a
	// session token unusable here and vice versa.
	claims, err := s.tokens.Verify(req.SessionID, tokenPurposeTicket)
	if err != nil {
		writeError(w, http.StatusUnauthorized, secrets.ErrCodeUnauthorized, "this login code is invalid or expired; run codebox secrets login again")
		return
	}
	// A ticket is single-use: the first exchange consumes it, so a code
	// recovered from clipboard or shell history stops working immediately.
	if !s.attempts.ConsumeOnce(TokenFingerprint(req.SessionID), s.cfg.TicketLifetime) {
		s.audit(r, "login.ticket_replayed", slog.String("subject", hashPrefix(claims.Email)))
		writeError(w, http.StatusUnauthorized, secrets.ErrCodeUnauthorized, "this login code has already been used; run codebox secrets login again")
		return
	}
	token, expires, err := s.tokens.IssueSession(TokenClaims{
		Subject: claims.Subject, Email: claims.Email, SIRET: claims.SIRET, ACR: claims.ACR,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not issue a session token")
		return
	}
	s.audit(r, "login.session_issued", slog.String("subject", hashPrefix(claims.Email)))
	writeJSON(w, http.StatusOK, secrets.TokenResponse{
		Token:     token,
		ExpiresAt: expires,
		Identity: secrets.WhoAmIResponse{
			Sub: claims.Subject, Email: claims.Email, SIRET: claims.SIRET, ACR: claims.ACR, ExpiresAt: expires,
		},
	})
}

// handleLoopback serves the desktop callback page: it hands the token to the
// CLI listener and closes the browser tab.
func (s *Server) handleLoopback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>codebox</title>
<p>Login complete. You can close this tab and return to your terminal.</p>`))
}

func (s *Server) handleWhoAmI(w http.ResponseWriter, r *http.Request, claims TokenClaims) {
	writeJSON(w, http.StatusOK, secrets.WhoAmIResponse{
		Sub: claims.Subject, Email: claims.Email, SIRET: claims.SIRET, ACR: claims.ACR, ExpiresAt: claims.ExpiresAt,
	})
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request, claims TokenClaims) {
	if ok, retry := s.limiter.Allow("create:" + SubjectKey(claims)); !ok {
		writeError(w, http.StatusTooManyRequests, secrets.ErrCodeRateLimited, "too many project creations, retry in "+retry.Round(time.Second).String())
		return
	}
	if err := s.identity.ValidateProjectCreation(Identity{SIRET: claims.SIRET, Email: claims.Email}); err != nil {
		s.audit(r, "project.create_refused", slog.String("reason", "eligibility"))
		writeError(w, http.StatusForbidden, secrets.ErrCodeForbidden, err.Error())
		return
	}
	var req secrets.CreateProjectRequest
	if err := decodeStrictJSON(r, &req, 4<<10); err != nil {
		writeError(w, http.StatusBadRequest, secrets.ErrCodeInvalidRequest, err.Error())
		return
	}
	projectID, err := secrets.NewProjectID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not allocate a project identifier")
		return
	}
	if err := s.store.CreateProject(r.Context(), projectID, claims.Email, claims.ACR); err != nil {
		s.writeStoreError(w, err)
		return
	}
	s.audit(r, "project.created", slog.String("project", projectID), slog.String("subject", hashPrefix(claims.Email)))
	writeJSON(w, http.StatusCreated, secrets.CreateProjectResponse{
		ProjectID: projectID, OwnerEmail: claims.Email, ACLRevision: 1, SecretsRevision: 1,
	})
}

// authorizeProject loads the project ACL once and returns the caller's role
// together with the revision that ACL was read at, so a caller can never pair
// stale entries with a fresher revision.
func (s *Server) authorizeProject(ctx context.Context, projectID string, claims TokenClaims) (secrets.ACL, secrets.Role, uint32, error) {
	if err := secrets.ValidateProjectID(projectID); err != nil {
		return secrets.ACL{}, "", 0, err
	}
	data, revision, err := s.store.ReadACL(ctx, projectID)
	if err != nil {
		return secrets.ACL{}, "", 0, err
	}
	acl, err := secrets.ParseACL(string(data))
	if err != nil {
		return secrets.ACL{}, "", 0, fmt.Errorf("%w: stored ACL is unreadable", ErrStoreInvalid)
	}
	role, ok := acl.Authorize(claims.Email, claims.SIRET)
	if !ok {
		return secrets.ACL{}, "", 0, ErrForbidden
	}
	return acl, role, revision, nil
}

// ErrForbidden marks an authorization denial for the caller's identity.
var ErrForbidden = errors.New("this identity is not allowed on the project")

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request, claims TokenClaims) {
	projectID := r.PathValue("id")
	acl, _, revision, err := s.authorizeProject(r.Context(), projectID, claims)
	if err != nil {
		s.writeAuthorizeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, secrets.MembersResponse{ProjectID: projectID, Revision: revision, Entries: acl.Entries()})
}

func (s *Server) handleUpdateMembers(w http.ResponseWriter, r *http.Request, claims TokenClaims) {
	projectID := r.PathValue("id")
	_, role, _, err := s.authorizeProject(r.Context(), projectID, claims)
	if err != nil {
		s.writeAuthorizeError(w, r, err)
		return
	}
	if role != secrets.RoleOwner {
		writeError(w, http.StatusForbidden, secrets.ErrCodeForbidden, "only a project owner may change the member list")
		return
	}
	var req secrets.UpdateMembersRequest
	if err := decodeStrictJSON(r, &req, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, secrets.ErrCodeInvalidRequest, err.Error())
		return
	}
	acl, err := secrets.NewACL(req.Entries)
	if err != nil {
		if errors.Is(err, secrets.ErrNoOwner) {
			writeError(w, http.StatusBadRequest, secrets.ErrCodeInvalidRequest, "a project must retain at least one owner")
			return
		}
		writeError(w, http.StatusBadRequest, secrets.ErrCodeInvalidRequest, err.Error())
		return
	}
	revision, err := s.store.WriteACL(r.Context(), projectID, req.BaseRevision, []byte(acl.Format()))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	s.audit(r, "members.updated", slog.String("project", projectID), slog.Int("entries", len(req.Entries)), slog.String("subject", hashPrefix(claims.Email)))
	writeJSON(w, http.StatusOK, secrets.MembersResponse{ProjectID: projectID, Revision: revision, Entries: acl.Entries()})
}

func (s *Server) handlePushSecrets(w http.ResponseWriter, r *http.Request, claims TokenClaims) {
	projectID := r.PathValue("id")
	_, role, _, err := s.authorizeProject(r.Context(), projectID, claims)
	if err != nil {
		s.writeAuthorizeError(w, r, err)
		return
	}
	if role != secrets.RoleOwner {
		writeError(w, http.StatusForbidden, secrets.ErrCodeForbidden, "only a project owner may push secrets")
		return
	}
	var req secrets.PushSecretsRequest
	if err := decodeStrictJSON(r, &req, secrets.MaxBundleBytes+8<<10); err != nil {
		writeError(w, http.StatusBadRequest, secrets.ErrCodeInvalidRequest, err.Error())
		return
	}
	if err := secrets.RejectProductionScope(req.Scope); err != nil {
		writeError(w, http.StatusBadRequest, secrets.ErrCodeProductionForbidden, err.Error())
		return
	}
	payload, err := secrets.MarshalBundle(req.Components)
	if err != nil {
		if errors.Is(err, secrets.ErrBundleTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, secrets.ErrCodePayloadTooLarge, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, secrets.ErrCodeInvalidRequest, err.Error())
		return
	}
	revision, err := s.store.WriteBundle(r.Context(), projectID, req.BaseRevision, payload)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	// Audit only counts and names: never a value.
	s.audit(r, "secrets.pushed", slog.String("project", projectID), slog.Int("components", len(req.Components)), slog.Uint64("revision", uint64(revision)), slog.String("subject", hashPrefix(claims.Email)))
	writeJSON(w, http.StatusOK, secrets.PushSecretsResponse{ProjectID: projectID, Revision: revision})
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request, claims TokenClaims) {
	projectID := r.PathValue("id")
	_, _, _, err := s.authorizeProject(r.Context(), projectID, claims)
	if err != nil {
		s.writeAuthorizeError(w, r, err)
		return
	}
	if ok, retry := s.limiter.Allow("resolve:" + SubjectKey(claims)); !ok {
		writeError(w, http.StatusTooManyRequests, secrets.ErrCodeRateLimited, "too many resolutions, retry in "+retry.Round(time.Second).String())
		return
	}
	var req secrets.ResolveRequest
	if err := decodeStrictJSON(r, &req, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, secrets.ErrCodeInvalidRequest, err.Error())
		return
	}
	if err := secrets.RejectProductionScope(req.Scope); err != nil {
		writeError(w, http.StatusBadRequest, secrets.ErrCodeProductionForbidden, err.Error())
		return
	}
	data, revision, err := s.store.ReadBundle(r.Context(), projectID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	bundle, err := secrets.UnmarshalBundle(data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, secrets.ErrCodeACLCorrupt, "the stored secret bundle is unreadable; ask a project owner to push it again")
		return
	}
	components := bundle.Components
	if len(req.Components) > 0 {
		wanted := make(map[string]bool, len(req.Components))
		for _, c := range req.Components {
			wanted[c] = true
		}
		filtered := make(map[string]map[string]string, len(req.Components))
		for comp, vars := range components {
			if wanted[comp] {
				filtered[comp] = vars
			}
		}
		components = filtered
	}
	s.audit(r, "secrets.resolved", slog.String("project", projectID), slog.Int("components", len(components)), slog.Uint64("revision", uint64(revision)), slog.String("subject", hashPrefix(claims.Email)))
	writeJSON(w, http.StatusOK, secrets.ResolveResponse{ProjectID: projectID, Revision: revision, Components: components})
}

func (s *Server) writeAuthorizeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		s.audit(r, "access.denied")
		writeError(w, http.StatusForbidden, secrets.ErrCodeForbidden, "this identity is not allowed on the project")
	case errors.Is(err, secrets.ErrInvalidProjectID):
		writeError(w, http.StatusBadRequest, secrets.ErrCodeInvalidRequest, "project identifier is not a valid UUIDv4")
	default:
		s.writeStoreError(w, err)
	}
}

func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrStoreNotFound):
		writeError(w, http.StatusNotFound, secrets.ErrCodeNotFound, "no such project")
	case errors.Is(err, ErrStoreConflict):
		writeError(w, http.StatusConflict, secrets.ErrCodeConflict, "the project changed since this change was prepared; review a fresh state and retry")
	case errors.Is(err, ErrStoreInvalid):
		writeError(w, http.StatusInternalServerError, secrets.ErrCodeACLCorrupt, "the stored data is unreadable")
	default:
		writeError(w, http.StatusServiceUnavailable, secrets.ErrCodeUpstreamUnavailable, "the secret store is unavailable; retry shortly")
	}
}

// audit emits one structured, value-free event per meaningful action.
func (s *Server) audit(r *http.Request, event string, attrs ...slog.Attr) {
	base := []slog.Attr{
		slog.String("event", event),
		slog.String("ip", ClientIP(r)),
	}
	s.logger.LogAttrs(r.Context(), slog.LevelInfo, "secrets-gateway", append(base, attrs...)...)
}

func auditReason(err error) string {
	switch {
	case errors.Is(err, ErrUpstream):
		return "identity_provider"
	case errors.Is(err, ErrMFARequired):
		return "mfa_required"
	default:
		return "verification"
	}
}

// ClientIP derives the caller's address for rate limiting and audit. The
// gateway runs behind a managed load balancer, so the left-most
// X-Forwarded-For entry is a bucketing hint only — never an authorization
// input.
func ClientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		first := strings.TrimSpace(strings.Split(fwd, ",")[0])
		if first != "" {
			return first
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func randomURLToken(bytesLen int) (string, error) {
	buf := make([]byte, bytesLen)
	if _, err := crand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
