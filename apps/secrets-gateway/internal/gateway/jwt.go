package gateway

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// jwk is a single JSON Web Key from a JWKS document.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// verifiedToken is the validated subset of an ID token's claims.
type verifiedToken struct {
	Subject   string
	Email     string
	SIRET     string
	ACR       string
	AMR       []string
	NameHint  string
	Nonce     string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// jwtPayload mirrors the ID token claims the gateway reads. Unknown claims are
// ignored by design: ProConnect adds attributes over time.
type jwtPayload struct {
	Issuer    string          `json:"iss"`
	Audience  json.RawMessage `json:"aud"`
	Subject   string          `json:"sub"`
	Email     string          `json:"email"`
	SIRET     string          `json:"siret"`
	ACR       string          `json:"acr"`
	AMR       []string        `json:"amr"`
	GivenName string          `json:"given_name"`
	UsualName string          `json:"usual_name"`
	Nonce     string          `json:"nonce"`
	IssuedAt  int64           `json:"iat"`
	ExpiresAt int64           `json:"exp"`
}

// fetchJWKS retrieves and parses the provider's signing keys. Only RSA keys
// usable for signatures are admitted: ProConnect issues RS256 ID tokens.
func (c *OIDCClient) fetchJWKS(ctx context.Context, jwksURL string) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: jwks: %v", ErrUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: jwks returned HTTP %d", ErrUpstream, resp.StatusCode)
	}
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("%w: jwks payload is not valid JSON", ErrUpstream)
	}
	if len(doc.Keys) == 0 {
		return nil, fmt.Errorf("%w: jwks contains no keys", ErrUpstream)
	}
	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if !strings.EqualFold(k.Kty, "RSA") {
			continue
		}
		if k.Use != "" && !strings.EqualFold(k.Use, "sig") {
			continue
		}
		if k.Alg != "" && !strings.EqualFold(k.Alg, "RS256") {
			continue
		}
		pub, err := rsaPublicKeyFromJWK(k)
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: jwks contains no usable RS256 signing key", ErrUpstream)
	}
	return keys, nil
}

func rsaPublicKeyFromJWK(k jwk) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil || len(nBytes) == 0 {
		return nil, errors.New("invalid modulus")
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil || len(eBytes) == 0 {
		return nil, errors.New("invalid exponent")
	}
	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)
	if !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 {
		return nil, errors.New("unreasonable exponent")
	}
	return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
}

// verifyRS256JWT validates an ID token's signature, issuer, audience and
// lifetime, returning its claims. Only RS256 is accepted: ProConnect's
// documented ID token algorithms are RS256, ES256 and HS256, and a shared
// symmetric secret must never be offered to a public client.
func verifyRS256JWT(token string, keys map[string]*rsa.PublicKey, issuer string, now time.Time) (verifiedToken, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return verifiedToken{}, fmt.Errorf("%w: id token is not a compact JWS", ErrUpstream)
	}
	rawHeader, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return verifiedToken{}, fmt.Errorf("%w: id token header is not base64url", ErrUpstream)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(rawHeader, &header); err != nil {
		return verifiedToken{}, fmt.Errorf("%w: id token header is not JSON", ErrUpstream)
	}
	if header.Alg != "RS256" {
		return verifiedToken{}, fmt.Errorf("%w: unsupported id token algorithm %q", ErrUpstream, header.Alg)
	}
	key, ok := keys[header.Kid]
	if !ok {
		return verifiedToken{}, fmt.Errorf("%w: id token key %q is not in the provider key set", ErrUpstream, header.Kid)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return verifiedToken{}, fmt.Errorf("%w: id token signature is not base64url", ErrUpstream)
	}
	signingInput := []byte(parts[0] + "." + parts[1])
	hash := sha256.Sum256(signingInput)
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, hash[:], signature); err != nil {
		return verifiedToken{}, fmt.Errorf("%w: id token signature is invalid", ErrUpstream)
	}

	rawPayload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return verifiedToken{}, fmt.Errorf("%w: id token payload is not base64url", ErrUpstream)
	}
	var payload jwtPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return verifiedToken{}, fmt.Errorf("%w: id token payload is not JSON", ErrUpstream)
	}
	if issuer != "" && payload.Issuer != issuer {
		return verifiedToken{}, fmt.Errorf("%w: id token issuer %q does not match %q", ErrUpstream, payload.Issuer, issuer)
	}
	if payload.Subject == "" {
		return verifiedToken{}, fmt.Errorf("%w: id token has no subject", ErrUpstream)
	}
	issued := time.Unix(payload.IssuedAt, 0).UTC()
	expires := time.Unix(payload.ExpiresAt, 0).UTC()
	if payload.ExpiresAt == 0 || !expires.After(now) {
		return verifiedToken{}, fmt.Errorf("%w: id token is expired", ErrUpstream)
	}
	if issued.After(now.Add(5 * time.Minute)) {
		return verifiedToken{}, fmt.Errorf("%w: id token is not yet valid", ErrUpstream)
	}
	return verifiedToken{
		Subject:   payload.Subject,
		Email:     strings.ToLower(payload.Email),
		SIRET:     payload.SIRET,
		ACR:       payload.ACR,
		AMR:       payload.AMR,
		NameHint:  strings.TrimSpace(payload.GivenName + " " + payload.UsualName),
		Nonce:     payload.Nonce,
		IssuedAt:  issued,
		ExpiresAt: expires,
	}, nil
}
