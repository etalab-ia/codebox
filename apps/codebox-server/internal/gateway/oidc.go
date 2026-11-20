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

// ProConnectSandboxIssuer and ProConnectProductionIssuer are the verified
// ProConnect OIDC issuers. Discovery lives under `<issuer>/.well-known/openid-configuration`,
// so the issuer must include the `/api/v2` path segment.
//
// Verified 2026-11-14 against the published domain table
// (https://partenaires.proconnect.gouv.fr/docs/ressources/valeur_ac_domain) and
// by fetching both discovery documents: the sandbox issuer returns
// `issuer: https://fca.integ01.dev-agentconnect.fr/api/v2` with RS256 among its
// signing algorithms.
const (
	ProConnectSandboxIssuer    = "https://fca.integ01.dev-agentconnect.fr/api/v2"
	ProConnectProductionIssuer = "https://auth.agentconnect.gouv.fr/api/v2"
)

// MFAEnabledACRValues lists the assurance levels that satisfy the gateway's
// two-factor requirement. HR-backed ProConnect identity providers return
// eidas2/eidas3; providers without HR provisioning return the `-mfa` or
// self-asserted-2fa variants, so both families are accepted for development
// access. A plain eidas1/eidas0 (password only) is rejected.
var MFAEnabledACRValues = []string{
	"eidas0-mfa",
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
	"eidas0-mfa",
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
	// IDToken is the verified raw ProConnect assertion retained for logout.
	// It must never be written to audit events.
	IDToken string
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
	EndSessionURL    string `json:"end_session_endpoint"`
	// UserInfoSigningAlgs lists the algorithms the provider may use for signed
	// UserInfo responses. ProConnect documents `application/jwt` UserInfo when a
	// signing algorithm is registered for the client, so the client must be
	// prepared for both formats.
	UserInfoSigningAlgs []string `json:"userinfo_signing_alg_values_supported"`
}

// NewOIDCClient builds a client. It does not perform network I/O: discovery
// runs on first use so the process can start without ProConnect reachable.
func NewOIDCClient(cfg OIDCConfig, httpClient *http.Client, now func() time.Time) (*OIDCClient, error) {
	if strings.TrimSpace(cfg.Issuer) == "" || strings.TrimSpace(cfg.ClientID) == "" || strings.TrimSpace(cfg.ClientSecret) == "" {
		return nil, errors.New("l'émetteur, l'identifiant client et le secret client ProConnect sont requis")
	}
	redirect, err := url.Parse(cfg.RedirectURL)
	if err != nil || redirect.Scheme != "https" || redirect.Host == "" {
		return nil, errors.New("l'URL de redirection ProConnect doit être une URL https absolue")
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

// LogoutURL builds the documented browser end-session redirect. The return URI
// is derived from the registered login callback origin and must be registered
// exactly as `<GATEWAY_PUBLIC_URL>/auth/logout/callback`.
func (c *OIDCClient) LogoutURL(ctx context.Context, state, idToken string) (string, error) {
	if len(state) < 32 {
		return "", errors.New("l'état de déconnexion doit contenir au moins 32 caractères")
	}
	if strings.TrimSpace(idToken) == "" {
		return "", errors.New("le jeton d'identité ProConnect est requis pour la déconnexion")
	}
	disc, err := c.discover(ctx)
	if err != nil {
		return "", err
	}
	if disc.EndSessionURL == "" {
		return "", fmt.Errorf("%w : discovery omet end_session_endpoint", ErrUpstream)
	}
	postLogout := strings.TrimSuffix(c.cfg.RedirectURL, "/auth/callback")
	if postLogout == c.cfg.RedirectURL {
		return "", errors.New("l'URL de rappel ProConnect doit se terminer par /auth/callback")
	}
	q := url.Values{}
	q.Set("id_token_hint", idToken)
	q.Set("state", state)
	q.Set("post_logout_redirect_uri", postLogout+"/auth/logout/callback")
	return disc.EndSessionURL + "?" + q.Encode(), nil
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
		return Identity{}, fmt.Errorf("%w : point de terminaison de jeton : %w", ErrUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Identity{}, fmt.Errorf("%w : réponse du point de terminaison de jeton illisible", ErrUpstream)
	}
	if resp.StatusCode != http.StatusOK {
		// Never echo the response body: it can contain an error description
		// carrying request fragments.
		return Identity{}, fmt.Errorf("%w : le point de terminaison de jeton a renvoyé HTTP %d", ErrUpstream, resp.StatusCode)
	}
	var token struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &token); err != nil || token.IDToken == "" {
		return Identity{}, fmt.Errorf("%w : la réponse du point de terminaison de jeton n'est pas une charge OIDC valide", ErrUpstream)
	}

	claims, err := c.verifyIDToken(ctx, disc, token.IDToken, expectedNonce)
	if err != nil {
		return Identity{}, err
	}
	if token.AccessToken != "" && (claims.Email == "" || claims.SIRET == "") {
		// UserInfo is authoritative for profile claims when the ID token is thin.
		// Its subject MUST equal the ID token's subject (OIDC Core 5.3.2): the
		// gateway authorises on email and SIRET, so copying profile claims from a
		// response about a different subject would grant this caller another
		// identity's project permissions. A profile failure is fatal when the ID
		// token cannot supply the claims on its own, rather than silently
		// degrading into a confusing "no email" rejection.
		profile, profileErr := c.userInfo(ctx, disc, token.AccessToken)
		if profileErr != nil {
			return Identity{}, profileErr
		}
		if profile.Subject == "" {
			return Identity{}, fmt.Errorf("%w : la réponse UserInfo ne porte aucun sujet à lier au jeton d'identité", ErrUpstream)
		}
		if profile.Subject != claims.Subject {
			return Identity{}, fmt.Errorf("%w : le sujet UserInfo ne correspond pas au sujet du jeton d'identité", ErrUpstream)
		}
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
	if claims.Email == "" {
		return Identity{}, fmt.Errorf("%w : l'identité ProConnect ne porte aucune adresse e-mail", ErrUpstream)
	}
	if !ACRSatisfiesMFA(claims.ACR) {
		return Identity{}, ErrMFARequired
	}
	claims.IDToken = token.IDToken
	return claims, nil
}

// ErrUpstream marks failures of the ProConnect side, mapped to 503.
var ErrUpstream = errors.New("fournisseur d'identité indisponible")

// ErrMFARequired is returned when the identity provider returned a
// single-factor assertion.
var ErrMFARequired = errors.New("l'authentification à deux facteurs est requise pour utiliser le serveur codebox")

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
		return nil, fmt.Errorf("%w : discovery : %w", ErrUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w : discovery a renvoyé HTTP %d", ErrUpstream, resp.StatusCode)
	}
	var disc oidcDiscovery
	if err := json.Unmarshal(body, &disc); err != nil {
		return nil, fmt.Errorf("%w : la charge discovery n'est pas du JSON valide", ErrUpstream)
	}
	if disc.AuthorizationURL == "" || disc.TokenURL == "" || disc.JWKSURL == "" {
		return nil, fmt.Errorf("%w : la charge discovery omet des points de terminaison requis", ErrUpstream)
	}
	if disc.Issuer != "" && disc.Issuer != c.cfg.Issuer {
		return nil, fmt.Errorf("%w : l'émetteur discovery %q ne correspond pas à l'émetteur configuré", ErrUpstream, disc.Issuer)
	}
	c.mu.Lock()
	c.discovery = &disc
	c.mu.Unlock()
	return &disc, nil
}

func (c *OIDCClient) verifyIDToken(ctx context.Context, disc *oidcDiscovery, idToken, expectedNonce string) (Identity, error) {
	verified, err := c.verifyToken(ctx, disc.JWKSURL, idToken, true)
	if err != nil {
		return Identity{}, err
	}
	if expectedNonce != "" && verified.Nonce != expectedNonce {
		return Identity{}, fmt.Errorf("%w : le nonce du jeton d'identité est incohérent", ErrUpstream)
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

// userInfo retrieves the caller's profile. ProConnect returns either a JSON
// document or a signed JWT (`application/jwt`) depending on the algorithm
// registered for the client, and the Accept header does not change that, so
// both forms are handled: a signed response is verified against the provider's
// public keys (RS256/ES256) or the client secret (HS256) before any claim is
// trusted.
func (c *OIDCClient) userInfo(ctx context.Context, disc *oidcDiscovery, accessToken string) (Identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, disc.UserInfoURL, nil)
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json, application/jwt")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("%w : UserInfo : %w", ErrUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return Identity{}, fmt.Errorf("%w : UserInfo a renvoyé HTTP %d", ErrUpstream, resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	signed := strings.HasPrefix(contentType, "application/jwt") || looksLikeJWS(body)
	var profile userInfoClaims
	if signed {
		verified, err := c.verifyToken(ctx, disc.JWKSURL, strings.TrimSpace(string(body)), false)
		if err != nil {
			return Identity{}, err
		}
		profile = userInfoClaims{
			Subject: verified.Subject,
			Email:   verified.Email,
			SIRET:   verified.SIRET,
			ACR:     verified.ACR,
			Given:   verified.GivenName,
			Usual:   verified.UsualName,
			UID:     verified.UID,
		}
	} else {
		if err := json.Unmarshal(body, &profile); err != nil {
			return Identity{}, fmt.Errorf("%w : la charge UserInfo n'est ni du JSON ni un JWT signé", ErrUpstream)
		}
	}
	return Identity{
		Subject:  firstNonEmpty(profile.Subject, profile.UID),
		Email:    strings.ToLower(profile.Email),
		SIRET:    profile.SIRET,
		ACR:      profile.ACR,
		NameHint: strings.TrimSpace(profile.Given + " " + profile.Usual),
	}, nil
}

// userInfoClaims is the subset of the UserInfo response the gateway reads. It
// decodes both the JSON document and the claim set of a signed JWT.
type userInfoClaims struct {
	Subject string `json:"sub"`
	UID     string `json:"uid"`
	Email   string `json:"email"`
	SIRET   string `json:"siret"`
	Given   string `json:"given_name"`
	Usual   string `json:"usual_name"`
	ACR     string `json:"acr"`
}

// looksLikeJWS reports whether a body is a three-segment compact JWS.
func looksLikeJWS(body []byte) bool {
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "{") {
		return false
	}
	return strings.Count(trimmed, ".") == 2 && !strings.ContainsAny(trimmed, " \n\t")
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
		return errors.New("la création de projet est réservée aux organisations présentes dans la liste d'autorisation ; demandez à un propriétaire existant de vous ajouter à l'ACL du projet")
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
	return errors.New("la création de projet est réservée aux organisations du secteur public reconnues ; demandez à un propriétaire existant de vous ajouter à l'ACL du projet")
}
