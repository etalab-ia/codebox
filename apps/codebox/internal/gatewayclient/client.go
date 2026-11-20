// Package gatewayclient talks to the #385 development-secrets server. It is
// host-side only: session tokens are read from the OS credential store and
// secret values are returned to the caller in memory, never written to a
// project file or the guest directly.
package gatewayclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/etalab-ia/codebox/packages/secrets"
)

// Client is a small typed HTTP client for codebox-server.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// New builds a client. baseURL must be an absolute HTTPS URL and token must
// not be empty; those are caller errors, not network errors.
func New(baseURL, token string, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("aucune session de passerelle ; lancez codebox secrets login")
	}
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("l'URL de la passerelle doit être une URL https absolue")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{baseURL: strings.TrimSuffix(u.String(), "/"), token: strings.TrimSpace(token), httpClient: trustedOriginHTTPClient(u, httpClient)}, nil
}

// trustedOriginHTTPClient clones client and enforces the configured gateway
// origin on every redirect hop. The default ten-redirect budget and any
// caller-supplied policy are preserved.
func trustedOriginHTTPClient(trusted *url.URL, client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	cloned := *client
	previous := cloned.CheckRedirect
	cloned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("arrêt après 10 redirections de la passerelle")
		}
		if !sameOrigin(trusted, req.URL) {
			return errors.New("redirection refusée hors de l'origine de confiance de la passerelle")
		}
		if previous != nil {
			return previous(req, via)
		}
		return nil
	}
	return &cloned
}

func sameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil || a.User != nil || b.User != nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && originHostPort(a) == originHostPort(b)
}

func originHostPort(u *url.URL) string {
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// WhoAmI returns the verified identity behind the session token.
func (c *Client) WhoAmI(ctx context.Context) (secrets.WhoAmIResponse, error) {
	var out secrets.WhoAmIResponse
	err := c.do(ctx, http.MethodGet, "/v1/whoami", nil, &out)
	return out, err
}

// CreateProject creates an ACL and empty secret bundle for a new project.
func (c *Client) CreateProject(ctx context.Context, req secrets.CreateProjectRequest) (secrets.CreateProjectResponse, error) {
	var out secrets.CreateProjectResponse
	err := c.do(ctx, http.MethodPost, "/v1/projects", req, &out)
	return out, err
}

// ListMembers returns the current ACL entries and revision.
func (c *Client) ListMembers(ctx context.Context, projectID string) (secrets.MembersResponse, error) {
	var out secrets.MembersResponse
	err := c.do(ctx, http.MethodGet, "/v1/projects/"+url.PathEscape(projectID)+"/members", nil, &out)
	return out, err
}

// UpdateMembers replaces the ACL at baseRevision.
func (c *Client) UpdateMembers(ctx context.Context, projectID string, req secrets.UpdateMembersRequest) (secrets.MembersResponse, error) {
	var out secrets.MembersResponse
	err := c.do(ctx, http.MethodPut, "/v1/projects/"+url.PathEscape(projectID)+"/members", req, &out)
	return out, err
}

// PushSecrets replaces the project's development-secret bundle at baseRevision.
func (c *Client) PushSecrets(ctx context.Context, projectID string, req secrets.PushSecretsRequest) (secrets.PushSecretsResponse, error) {
	var out secrets.PushSecretsResponse
	err := c.do(ctx, http.MethodPut, "/v1/projects/"+url.PathEscape(projectID)+"/secrets", req, &out)
	return out, err
}

// Resolve returns the project's development-secret bundle, optionally filtered
// to a set of dotenv component paths.
func (c *Client) Resolve(ctx context.Context, projectID string, components []string) (secrets.ResolveResponse, error) {
	var out secrets.ResolveResponse
	req := secrets.ResolveRequest{Components: components}
	err := c.do(ctx, http.MethodPost, "/v1/projects/"+url.PathEscape(projectID)+"/resolve", req, &out)
	return out, err
}

// LoginURL builds the ProConnect login URL for a CLI loopback listener.
func (c *Client) LoginURL(port int, loopbackState string) string {
	return c.LoginURLWithBase(c.baseURL, port, loopbackState)
}

// LoginURLWithBase builds a browser login URL for an explicit gateway base.
func (c *Client) LoginURLWithBase(base string, port int, loopbackState string) string {
	u, _ := url.Parse(strings.TrimSuffix(base, "/"))
	u.Path = strings.TrimSuffix(u.Path, "/") + "/auth/login"
	q := u.Query()
	q.Set("port", strconv.Itoa(port))
	q.Set("loopback_state", loopbackState)
	u.RawQuery = q.Encode()
	return u.String()
}

// TerminalLoginURL builds the SSH/paste-the-code login URL.
func (c *Client) TerminalLoginURL() string {
	return c.TerminalLoginURLWithBase(c.baseURL)
}

// TerminalLoginURLWithBase builds a terminal login URL for an explicit gateway
// base. It is used before a Client exists, during `secrets login --terminal`.
func (c *Client) TerminalLoginURLWithBase(base string) string {
	u, _ := url.Parse(strings.TrimSuffix(base, "/"))
	u.Path = strings.TrimSuffix(u.Path, "/") + "/auth/login"
	q := u.Query()
	q.Set("mode", "terminal")
	u.RawQuery = q.Encode()
	return u.String()
}

// LogoutURL returns the ProConnect end-session URL for the retained ID token.
// The request uses the authenticated gateway session and contains no secret in
// its URL; the provider URL is intended for the user's browser.
func (c *Client) LogoutURL(ctx context.Context, idToken string) (string, error) {
	var out secrets.LogoutResponse
	err := c.do(ctx, http.MethodPost, "/auth/logout", secrets.LogoutRequest{IDToken: idToken}, &out)
	return out.URL, err
}

// ExchangeTicket trades a one-time terminal login code for a session token.
func ExchangeTicket(ctx context.Context, baseURL, code string, httpClient *http.Client) (secrets.TokenResponse, error) {
	var out secrets.TokenResponse
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	trusted, err := url.Parse(strings.TrimSuffix(baseURL, "/"))
	if err != nil {
		return out, fmt.Errorf("URL de connexion à la passerelle : %w", err)
	}
	httpClient = trustedOriginHTTPClient(trusted, httpClient)
	// The gateway treats the ticket itself as the bearer; no PKCE verifier is
	// used on this endpoint. Marshal a minimal body so the lower-layer contract
	// stays untouched by this PR.
	payload, err := json.Marshal(map[string]string{"sessionId": code})
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(baseURL, "/")+"/auth/device/token", bytes.NewReader(payload))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("connexion à la passerelle : %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return out, fmt.Errorf("réponse de connexion à la passerelle illisible : %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return out, decodeAPIError(resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("la réponse de connexion à la passerelle n'est pas du JSON valide : %w", err)
	}
	return out, nil
}

func (c *Client) do(ctx context.Context, method, path string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("échec de l'encodage de la requête vers la passerelle : %w", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("passerelle injoignable : %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("réponse de la passerelle illisible : %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeAPIError(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("la réponse de la passerelle n'est pas du JSON valide : %w", err)
	}
	return nil
}

func decodeAPIError(status int, raw []byte) error {
	var apiErr secrets.APIError
	if err := json.Unmarshal(raw, &apiErr); err == nil && apiErr.Code != "" {
		return fmt.Errorf("passerelle %d : %w", status, apiErr)
	}
	return fmt.Errorf("la passerelle a renvoyé HTTP %d", status)
}

// NewInsecureForTest builds a client against a plaintext httptest URL. It is
// exported within the CLI module because `cmd/codebox` tests drive the real
// command handlers against an httptest gateway; production always goes
// through New, which refuses anything but HTTPS.
func NewInsecureForTest(baseURL, token string, httpClient *http.Client) (*Client, error) {
	return newForTest(baseURL, token, httpClient), nil
}

// newForTest builds a client without TLS validation; httptest serves HTTP,
// while production always goes through New's HTTPS check.
func newForTest(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	u, _ := url.Parse(strings.TrimSuffix(baseURL, "/"))
	return &Client{baseURL: strings.TrimSuffix(baseURL, "/"), token: token, httpClient: trustedOriginHTTPClient(u, httpClient)}
}
