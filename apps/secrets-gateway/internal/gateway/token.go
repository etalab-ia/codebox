package gateway

import (
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TokenIssuer signs and verifies the gateway's own bearer tokens. Tokens are
// compact JWS (EdDSA) with a fixed typ/alg so a token minted for another
// purpose cannot be replayed here.
type TokenIssuer struct {
	key     ed25519.PrivateKey
	public  ed25519.PublicKey
	issuer  string
	maxLife time.Duration
	now     func() time.Time
}

// TokenClaims is the verified identity carried by a gateway token.
type TokenClaims struct {
	Subject   string
	Email     string
	SIRET     string
	ACR       string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

type tokenHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

type tokenPayload struct {
	Issuer    string `json:"iss"`
	Subject   string `json:"sub"`
	Email     string `json:"email"`
	SIRET     string `json:"siret,omitempty"`
	ACR       string `json:"acr"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
	Purpose   string `json:"purpose"`
}

const (
	tokenPurposeSession = "secrets-gateway-session"
	tokenPurposeTicket  = "secrets-gateway-ticket"
	tokenType           = "JWT"
)

// NewTokenIssuer builds an issuer from a raw 32-byte seed (base64url, no
// padding). Callers generate the seed once and provide it as an environment
// secret so every instance verifies the same tokens.
func NewTokenIssuer(seedB64 string, issuer string, maxLife time.Duration, now func() time.Time) (*TokenIssuer, error) {
	seed, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(seedB64))
	if err != nil {
		return nil, fmt.Errorf("token seed must be base64url without padding: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("token seed must decode to %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	if strings.TrimSpace(issuer) == "" {
		return nil, errors.New("token issuer must not be empty")
	}
	if maxLife <= 0 || maxLife > 24*time.Hour {
		return nil, errors.New("token lifetime must be positive and at most 24h")
	}
	if now == nil {
		now = time.Now
	}
	key := ed25519.NewKeyFromSeed(seed)
	return &TokenIssuer{
		key:     key,
		public:  key.Public().(ed25519.PublicKey),
		issuer:  issuer,
		maxLife: maxLife,
		now:     now,
	}, nil
}

// NewTokenSeed generates a fresh base64url Ed25519 seed for operators to place
// in the environment secret.
func NewTokenSeed() (string, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := crand.Read(seed); err != nil {
		return "", fmt.Errorf("generate token seed: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(seed), nil
}

// IssueSession mints a session token for a verified ProConnect identity.
func (t *TokenIssuer) IssueSession(claims TokenClaims) (string, time.Time, error) {
	return t.issue(claims, tokenPurposeSession, t.maxLife)
}

// IssueTicket mints a short-lived ticket that the browser-ticket flow exchanges
// for a session token. The ticket carries the verified identity, so the device
// endpoint does not need to talk to ProConnect.
func (t *TokenIssuer) IssueTicket(claims TokenClaims, ttl time.Duration) (string, time.Time, error) {
	return t.issue(claims, tokenPurposeTicket, ttl)
}

func (t *TokenIssuer) issue(claims TokenClaims, purpose string, ttl time.Duration) (string, time.Time, error) {
	if claims.Email == "" || claims.Subject == "" {
		return "", time.Time{}, errors.New("token claims require sub and email")
	}
	now := t.now().UTC()
	exp := now.Add(ttl)
	header, err := json.Marshal(tokenHeader{Alg: "EdDSA", Typ: tokenType})
	if err != nil {
		return "", time.Time{}, err
	}
	payload, err := json.Marshal(tokenPayload{
		Issuer:    t.issuer,
		Subject:   claims.Subject,
		Email:     strings.ToLower(claims.Email),
		SIRET:     claims.SIRET,
		ACR:       claims.ACR,
		IssuedAt:  now.Unix(),
		ExpiresAt: exp.Unix(),
		Purpose:   purpose,
	})
	if err != nil {
		return "", time.Time{}, err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	signature := ed25519.Sign(t.key, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), exp, nil
}

// Verify checks a token's signature, issuer, purpose and lifetime.
func (t *TokenIssuer) Verify(token, purpose string) (TokenClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return TokenClaims{}, errors.New("malformed token")
	}
	signingInput := parts[0] + "." + parts[1]
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return TokenClaims{}, errors.New("malformed token signature")
	}
	if !ed25519.Verify(t.public, []byte(signingInput), signature) {
		return TokenClaims{}, errors.New("invalid token signature")
	}
	rawHeader, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return TokenClaims{}, errors.New("malformed token header")
	}
	var header tokenHeader
	if err := json.Unmarshal(rawHeader, &header); err != nil || header.Alg != "EdDSA" || header.Typ != tokenType {
		return TokenClaims{}, errors.New("unsupported token header")
	}
	rawPayload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return TokenClaims{}, errors.New("malformed token payload")
	}
	var payload tokenPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return TokenClaims{}, errors.New("malformed token payload")
	}
	if payload.Issuer != t.issuer || payload.Purpose != purpose {
		return TokenClaims{}, errors.New("token is not valid for this service")
	}
	now := t.now().UTC()
	if payload.ExpiresAt <= now.Unix() {
		return TokenClaims{}, errors.New("token expired")
	}
	if payload.IssuedAt > now.Add(time.Minute).Unix() {
		return TokenClaims{}, errors.New("token issued in the future")
	}
	return TokenClaims{
		Subject:   payload.Subject,
		Email:     payload.Email,
		SIRET:     payload.SIRET,
		ACR:       payload.ACR,
		IssuedAt:  time.Unix(payload.IssuedAt, 0).UTC(),
		ExpiresAt: time.Unix(payload.ExpiresAt, 0).UTC(),
	}, nil
}

// TokenFingerprint returns a stable, non-reversible identifier for a token, so
// audit logs can correlate requests without recording the bearer itself.
func TokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:8])
}

// SubjectKey namespaces a verified identity for rate limiting.
func SubjectKey(claims TokenClaims) string {
	return strings.ToLower(claims.Email)
}

// contextKey avoids collisions for values stored in request contexts.
type contextKey string

const claimsContextKey contextKey = "gateway.claims"

func withClaims(ctx context.Context, claims TokenClaims) context.Context {
	return context.WithValue(ctx, claimsContextKey, claims)
}

func claimsFrom(ctx context.Context) (TokenClaims, bool) {
	claims, ok := ctx.Value(claimsContextKey).(TokenClaims)
	return claims, ok
}
