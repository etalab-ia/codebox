package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/etalab-ia/codebox/packages/secrets"
)

// MFAEnabledACRValues lists the assurance levels that satisfy the gateway's
// two-factor requirement. HR-backed ProConnect identity providers return
// eidas2/eidas3; providers without HR provisioning return the `-mfa` or
// self-asserted-2fa variants, so both families are accepted for development
// access. A plain eidas1/eidas0 (password only) is rejected.
var MFAEnabledACRValues = []string{
	"eidas1-mfa",
	"eidas2",
	"eidas3",
	"https://proconnect.gouv.fr/assurance/self-asserted-2fa",
	"https://proconnect.gouv.fr/assurance/consistency-checked-2fa",
}

// MFAACRFamilies lists the ACR prefixes that the gateway requests from
// ProConnect as "essential", so an identity provider must return a
// two-factor assertion.
var MFAACRFamilies = []string{
	"eidas1-mfa",
	"eidas2",
	"eidas3",
}

// ACRSatisfiesMFA reports whether an ACR value proves a second factor was used.
func ACRSatisfiesMFA(acr string) bool {
	norm := strings.TrimSpace(acr)
	for _, allowed := range MFAEnabledACRValues {
		if strings.EqualFold(norm, allowed) {
			return true
		}
	}
	return false
}

// OIDCConfig is the ProConnect client configuration. Sandbox and production
// differ only by Issuer and client credentials.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	// RedirectURL is the absolute callback registered with ProConnect. It must
	// match exactly, including the trailing slash policy.
	RedirectURL string
	// Scopes are requested from ProConnect; `openid` is always included.
	Scopes []string
	// SubjectEmailDomains, when non-empty, restricts who may create a project.
	// This is a creation-time guard, not an access control: existing project
	// ACLs remain authoritative.
	AllowedSIRETs []string
}

// Identity is the subset of ProConnect claims the gateway uses.
type Identity struct {
	Subject   string
	Email     string
	SIRET     string
	ACR       string
	AMR       []string
	NameHint  string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// OIDCClient performs the authorization-code exchange and ID token verification.
type OIDCClient struct {
	cfg        OIDCConfig
	httpClient *http.Client
	now        func() time.Time
	discovery  *oidcDiscovery

	mu sync.Mutex
}

type oidcDiscovery struct {
	Issuer           string `json:"issuer"`
	AuthorizationURL string `json:"authorization_endpoint"`
	TokenURL         string `json:"token_endpoint"`
	UserInfoURL      string `json:"userinfo_endpoint"`
	JWKSURL          string `json:"jwks_uri"`
}

// NewOIDCClient builds a client. It does not perform network I/O: discovery
// runs on first use so the process can start without ProConnect reachable.
func NewOIDCClient(cfg OIDCConfig, httpClient *http.Client, now func() time.Time) (*OIDCClient, error) {
	if strings.TrimSpace(cfg.Issuer) == "" || strings.TrimSpace(cfg.ClientID) == "" || strings.TrimSpace(cfg.ClientSecret) == "" {
		return nil, errors.New("ProConnect issuer, client ID and client secret are required")
	}
	redirect, err := url.Parse(cfg.RedirectURL)
	if err != nil || redirect.Scheme != "https" || redirect.Host == "" {
		return nil, errors.New("ProConnect redirect URL must be an absolute https URL")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	if now == nil {
		now = time.Now
	}
	return &OIDCClient{cfg: cfg, httpClient: httpClient, now: now}, nil
}

// AuthorizeURL builds the ProConnect authorization redirect for a login attempt.
func (c *OIDCClient) AuthorizeURL(ctx context.Context, state, nonce, codeChallenge string) (string, error) {
	disc, err := c.discover(ctx)
	if err != nil {
		return "", err
	}
	scopes := c.cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{"openid", "email", "given_name", "usual_name", "uid", "siret"}
	}
	claims, err := json.Marshal(map[string]any{
		"id_token": map[string]any{
			"acr": map[string]any{"essential": true, "values": MFAACRFamilies},
			"amr": map[string]any{"essential": true},
		},
	})
	if err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", c.cfg.ClientID)
	q.Set("redirect_uri", c.cfg.RedirectURL)
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("claims", string(claims))
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	return disc.AuthorizationURL + "?" + q.Encode(), nil
}

// Exchange trades an authorization code for a verified identity.
func (c *OIDCClient) Exchange(ctx context.Context, code, codeVerifier, expectedNonce string) (Identity, error) {
	disc, err := c.discover(ctx)
	if err != nil {
		return Identity{}, err
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", c.cfg.RedirectURL)
	form.Set("client_id", c.cfg.ClientID)
	form.Set("client_secret", c.cfg.ClientSecret)
	form.Set("code_verifier", codeVerifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, disc.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: token endpoint: %v", ErrUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Identity{}, fmt.Errorf("%w: token response unreadable", ErrUpstream)
	}
	if resp.StatusCode != http.StatusOK {
		// Never echo the response body: it can contain an error description
		// carrying request fragments.
		return Identity{}, fmt.Errorf("%w: token endpoint returned HTTP %d", ErrUpstream, resp.StatusCode)
	}
	var token struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &token); err != nil || token.IDToken == "" {
		return Identity{}, fmt.Errorf("%w: token response is not a valid OIDC payload", ErrUpstream)
	}

	claims, err := c.verifyIDToken(ctx, disc, token.IDToken, expectedNonce)
	if err != nil {
		return Identity{}, err
	}
	if token.AccessToken != "" && (claims.Email == "" || claims.SIRET == "") {
		// UserInfo is authoritative for profile claims when the ID token is thin.
		if profile, err := c.userInfo(ctx, disc, token.AccessToken); err == nil {
			if claims.Email == "" {
				claims.Email = profile.Email
			}
			if claims.SIRET == "" {
				claims.SIRET = profile.SIRET
			}
			if claims.NameHint == "" {
				claims.NameHint = profile.NameHint
			}
		}
	}
	if claims.Email == "" {
		return Identity{}, fmt.Errorf("%w: ProConnect identity carries no email claim", ErrUpstream)
	}
	if !ACRSatisfiesMFA(claims.ACR) {
		return Identity{}, ErrMFARequired
	}
	return claims, nil
}

// ErrUpstream marks failures of the ProConnect side, mapped to 503.
var ErrUpstream = errors.New("identity provider unavailable")

// ErrMFARequired is returned when the identity provider returned a
// single-factor assertion.
var ErrMFARequired = errors.New("two-factor authentication is required to use the secrets gateway")

func (c *OIDCClient) discover(ctx context.Context) (*oidcDiscovery, error) {
	c.mu.Lock()
	cached := c.discovery
	c.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	endpoint := strings.TrimSuffix(c.cfg.Issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: discovery: %v", ErrUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: discovery returned HTTP %d", ErrUpstream, resp.StatusCode)
	}
	var disc oidcDiscovery
	if err := json.Unmarshal(body, &disc); err != nil {
		return nil, fmt.Errorf("%w: discovery payload is not valid JSON", ErrUpstream)
	}
	if disc.AuthorizationURL == "" || disc.TokenURL == "" || disc.JWKSURL == "" {
		return nil, fmt.Errorf("%w: discovery payload is missing required endpoints", ErrUpstream)
	}
	if disc.Issuer != "" && disc.Issuer != c.cfg.Issuer {
		return nil, fmt.Errorf("%w: discovery issuer %q does not match the configured issuer", ErrUpstream, disc.Issuer)
	}
	c.mu.Lock()
	c.discovery = &disc
	c.mu.Unlock()
	return &disc, nil
}

func (c *OIDCClient) verifyIDToken(ctx context.Context, disc *oidcDiscovery, idToken, expectedNonce string) (Identity, error) {
	keys, err := c.fetchJWKS(ctx, disc.JWKSURL)
	if err != nil {
		return Identity{}, err
	}
	verified, err := verifyRS256JWT(idToken, keys, c.cfg.Issuer, c.now())
	if err != nil {
		return Identity{}, err
	}
	if expectedNonce != "" && verified.Nonce != expectedNonce {
		return Identity{}, fmt.Errorf("%w: id token nonce mismatch", ErrUpstream)
	}
	return Identity{
		Subject:   verified.Subject,
		Email:     strings.ToLower(verified.Email),
		SIRET:     verified.SIRET,
		ACR:       verified.ACR,
		AMR:       verified.AMR,
		NameHint:  verified.NameHint,
		IssuedAt:  verified.IssuedAt,
		ExpiresAt: verified.ExpiresAt,
	}, nil
}

func (c *OIDCClient) userInfo(ctx context.Context, disc *oidcDiscovery, accessToken string) (Identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, disc.UserInfoURL, nil)
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: userinfo: %v", ErrUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return Identity{}, fmt.Errorf("%w: userinfo returned HTTP %d", ErrUpstream, resp.StatusCode)
	}
	var profile struct {
		Email string `json:"email"`
		SIRET string `json:"siret"`
		Given string `json:"given_name"`
		Usual string `json:"usual_name"`
		ACR   string `json:"acr"`
		Sub   string `json:"sub"`
		UID   string `json:"uid"`
	}
	if err := json.Unmarshal(body, &profile); err != nil {
		return Identity{}, fmt.Errorf("%w: userinfo payload is not valid JSON", ErrUpstream)
	}
	return Identity{
		Subject:  firstNonEmpty(profile.Sub, profile.UID),
		Email:    strings.ToLower(profile.Email),
		SIRET:    profile.SIRET,
		ACR:      profile.ACR,
		NameHint: strings.TrimSpace(profile.Given + " " + profile.Usual),
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ValidateProjectCreation applies the creation-time eligibility guard: the
// caller must belong to a recognised French public-sector organisation. This
// prevents arbitrary ProConnect users from claiming a project namespace; it
// does not grant access to any existing project.
//
// When an explicit SIRET allowlist is configured it is exclusive: the caller's
// SIRET must appear in it, and the domain fallback does not apply.
func (c *OIDCClient) ValidateProjectCreation(id Identity) error {
	if len(c.cfg.AllowedSIRETs) > 0 {
		if err := secrets.ValidateSIRET(id.SIRET); err == nil {
			for _, allowed := range c.cfg.AllowedSIRETs {
				if strings.TrimSpace(allowed) == id.SIRET {
					return nil
				}
			}
		}
		return errors.New("project creation is restricted to allowlisted organisations; ask an existing owner to add you to the project ACL instead")
	}

	domain := ""
	if idx := strings.LastIndexByte(id.Email, '@'); idx >= 0 {
		domain = strings.ToLower(id.Email[idx+1:])
	}
	switch domain {
	case "numerique.gouv.fr", "beta.gouv.fr", "modernisation.gouv.fr", "finances.gouv.fr", "interieur.gouv.fr", "sante.gouv.fr", "education.gouv.fr", "culture.gouv.fr", "agriculture.gouv.fr", "diplomatie.gouv.fr", "justice.fr", "developpement-durable.gouv.fr", "travail.gouv.fr", "sports.gouv.fr", "egalite-femmes-hommes.gouv.fr", "mer.gouv.fr", "outre-mer.gouv.fr", "anct.gouv.fr":
		return nil
	}
	// A verified ProConnect SIRET is enough even when the mail domain is a
	// contractor's own domain.
	if err := secrets.ValidateSIRET(id.SIRET); err == nil {
		return nil
	}
	return errors.New("project creation is restricted to recognised public-sector organisations; ask an existing owner to add you to the project ACL instead")
}
