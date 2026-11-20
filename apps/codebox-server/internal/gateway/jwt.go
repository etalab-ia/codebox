package gateway

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
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
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// providerKeys keeps asymmetric algorithms in distinct key namespaces. HMAC
// never reads these public keys: HS256 uses only the OIDC client secret.
type providerKeys struct {
	rsaByKid   map[string]*rsa.PublicKey
	ecdsaByKid map[string]*ecdsa.PublicKey
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

// verifiedToken is the validated subset of an ID token's claims.
type verifiedToken struct {
	Subject   string
	Email     string
	SIRET     string
	ACR       string
	AMR       []string
	NameHint  string
	GivenName string
	UsualName string
	UID       string
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
	UID       string          `json:"uid"`
	Nonce     string          `json:"nonce"`
	IssuedAt  int64           `json:"iat"`
	ExpiresAt int64           `json:"exp"`
}

// fetchJWKS retrieves and parses the provider's public signing keys. RSA keys
// are restricted to RS256 and P-256 EC keys to ES256; neither is eligible for
// HMAC verification.
func (c *OIDCClient) fetchJWKS(ctx context.Context, jwksURL string) (*providerKeys, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w : jwks : %w", ErrUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w : jwks a renvoyé HTTP %d", ErrUpstream, resp.StatusCode)
	}
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("%w : la charge jwks n'est pas du JSON valide", ErrUpstream)
	}
	if len(doc.Keys) == 0 {
		return nil, fmt.Errorf("%w : la charge jwks ne contient aucune clé", ErrUpstream)
	}
	keys := &providerKeys{
		rsaByKid:   make(map[string]*rsa.PublicKey),
		ecdsaByKid: make(map[string]*ecdsa.PublicKey),
	}
	for _, k := range doc.Keys {
		if k.Use != "" && !strings.EqualFold(k.Use, "sig") {
			continue
		}
		switch {
		case strings.EqualFold(k.Kty, "RSA") && (k.Alg == "" || strings.EqualFold(k.Alg, "RS256")):
			pub, err := rsaPublicKeyFromJWK(k)
			if err == nil {
				keys.rsaByKid[k.Kid] = pub
			}
		case strings.EqualFold(k.Kty, "EC") && (k.Alg == "" || strings.EqualFold(k.Alg, "ES256")):
			pub, err := ecdsaPublicKeyFromJWK(k)
			if err == nil {
				keys.ecdsaByKid[k.Kid] = pub
			}
		}
	}
	if len(keys.rsaByKid)+len(keys.ecdsaByKid) == 0 {
		return nil, fmt.Errorf("%w : la charge jwks ne contient aucune clé de signature RS256 ou ES256 utilisable", ErrUpstream)
	}
	return keys, nil
}

func rsaPublicKeyFromJWK(k jwk) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil || len(nBytes) == 0 {
		return nil, errors.New("module invalide")
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil || len(eBytes) == 0 {
		return nil, errors.New("exposant invalide")
	}
	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)
	if !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 {
		return nil, errors.New("exposant hors limites")
	}
	return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
}

func ecdsaPublicKeyFromJWK(k jwk) (*ecdsa.PublicKey, error) {
	if k.Crv != "P-256" {
		return nil, errors.New("courbe non prise en charge")
	}
	xBytes, err := base64.RawURLEncoding.DecodeString(k.X)
	if err != nil || len(xBytes) != 32 {
		return nil, errors.New("coordonnée x invalide")
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(k.Y)
	if err != nil || len(yBytes) != 32 {
		return nil, errors.New("coordonnée y invalide")
	}
	x := new(big.Int).SetBytes(xBytes)
	y := new(big.Int).SetBytes(yBytes)
	if !elliptic.P256().IsOnCurve(x, y) {
		return nil, errors.New("point hors de la courbe P-256")
	}
	return &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, nil
}

func parseCompactJWS(token string) ([]string, jwtHeader, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, jwtHeader{}, fmt.Errorf("%w : le jeton d'identité n'est pas un JWS compact", ErrUpstream)
	}
	rawHeader, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, jwtHeader{}, fmt.Errorf("%w : l'en-tête du jeton d'identité n'est pas du base64url", ErrUpstream)
	}
	var header jwtHeader
	if err := json.Unmarshal(rawHeader, &header); err != nil {
		return nil, jwtHeader{}, fmt.Errorf("%w : l'en-tête du jeton d'identité n'est pas du JSON", ErrUpstream)
	}
	return parts, header, nil
}

// verifyToken selects the credential from the registered algorithm before any
// claim is trusted. Public-key algorithms fetch JWKS; HS256 never does.
func (c *OIDCClient) verifyToken(ctx context.Context, jwksURL, token string, expiryRequired bool) (verifiedToken, error) {
	_, header, err := parseCompactJWS(token)
	if err != nil {
		return verifiedToken{}, err
	}
	var keys *providerKeys
	if header.Alg == "RS256" || header.Alg == "ES256" {
		keys, err = c.fetchJWKS(ctx, jwksURL)
		if err != nil {
			return verifiedToken{}, err
		}
	}
	return verifyJWTChecked(token, keys, c.cfg.ClientSecret, c.cfg.Issuer, c.cfg.ClientID, c.now(), expiryRequired)
}

// verifyJWT validates an ID token's signature, issuer, audience and lifetime.
// RS256/ES256 use provider public keys; HS256 uses only the client secret.
// expectedAudience is the gateway's own client ID. `aud` may be a string or an
// array per OIDC, and the token is rejected when it does not name this client.
func verifyJWT(token string, keys *providerKeys, clientSecret, issuer, expectedAudience string, now time.Time) (verifiedToken, error) {
	return verifyJWTChecked(token, keys, clientSecret, issuer, expectedAudience, now, true)
}

// verifyJWTClaims verifies signed UserInfo. Such a response must carry sub, iss
// and aud but NOT an expiry; an expiry that is present must still be valid.
func verifyJWTClaims(token string, keys *providerKeys, clientSecret, issuer, expectedAudience string, now time.Time) (verifiedToken, error) {
	return verifyJWTChecked(token, keys, clientSecret, issuer, expectedAudience, now, false)
}

// verifyJWTChecked is the shared verifier. expiryRequired distinguishes an ID
// token (expiry mandatory) from a signed UserInfo response (expiry optional).
func verifyJWTChecked(token string, keys *providerKeys, clientSecret, issuer, expectedAudience string, now time.Time, expiryRequired bool) (verifiedToken, error) {
	parts, header, err := parseCompactJWS(token)
	if err != nil {
		return verifiedToken{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return verifiedToken{}, fmt.Errorf("%w : la signature du jeton d'identité n'est pas du base64url", ErrUpstream)
	}
	signingInput := []byte(parts[0] + "." + parts[1])
	hash := sha256.Sum256(signingInput)
	validSignature := false
	switch header.Alg {
	case "RS256":
		if keys != nil {
			if key, ok := keys.rsaByKid[header.Kid]; ok {
				validSignature = rsa.VerifyPKCS1v15(key, crypto.SHA256, hash[:], signature) == nil
			}
		}
	case "ES256":
		if keys != nil {
			if key, ok := keys.ecdsaByKid[header.Kid]; ok {
				if len(signature) == 64 {
					r := new(big.Int).SetBytes(signature[:32])
					s := new(big.Int).SetBytes(signature[32:])
					validSignature = ecdsa.Verify(key, hash[:], r, s)
				}
			}
		}
	case "HS256":
		if clientSecret != "" {
			mac := hmac.New(sha256.New, []byte(clientSecret))
			_, _ = mac.Write(signingInput)
			validSignature = hmac.Equal(signature, mac.Sum(nil))
		}
	default:
		return verifiedToken{}, fmt.Errorf("%w : algorithme de jeton d'identité non pris en charge %q", ErrUpstream, header.Alg)
	}
	if !validSignature {
		return verifiedToken{}, fmt.Errorf("%w : la signature du jeton d'identité est invalide", ErrUpstream)
	}

	rawPayload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return verifiedToken{}, fmt.Errorf("%w : la charge du jeton d'identité n'est pas du base64url", ErrUpstream)
	}
	var payload jwtPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return verifiedToken{}, fmt.Errorf("%w : la charge du jeton d'identité n'est pas du JSON", ErrUpstream)
	}
	if issuer != "" && payload.Issuer != issuer {
		return verifiedToken{}, fmt.Errorf("%w : l'émetteur du jeton d'identité %q ne correspond pas à %q", ErrUpstream, payload.Issuer, issuer)
	}
	if payload.Subject == "" {
		return verifiedToken{}, fmt.Errorf("%w : le jeton d'identité n'a pas de sujet", ErrUpstream)
	}
	if expectedAudience != "" && !audienceMatches(payload.Audience, expectedAudience) {
		return verifiedToken{}, fmt.Errorf("%w : l'audience du jeton d'identité ne désigne pas ce client", ErrUpstream)
	}
	issued := time.Unix(payload.IssuedAt, 0).UTC()
	expires := time.Unix(payload.ExpiresAt, 0).UTC()
	if payload.ExpiresAt == 0 {
		if expiryRequired {
			return verifiedToken{}, fmt.Errorf("%w : le jeton d'identité n'a pas d'expiration", ErrUpstream)
		}
	} else if !expires.After(now) {
		return verifiedToken{}, fmt.Errorf("%w : le jeton a expiré", ErrUpstream)
	}
	if issued.After(now.Add(5 * time.Minute)) {
		return verifiedToken{}, fmt.Errorf("%w : le jeton d'identité n'est pas encore valide", ErrUpstream)
	}
	return verifiedToken{
		Subject:   payload.Subject,
		Email:     strings.ToLower(payload.Email),
		SIRET:     payload.SIRET,
		ACR:       payload.ACR,
		AMR:       payload.AMR,
		NameHint:  strings.TrimSpace(payload.GivenName + " " + payload.UsualName),
		GivenName: payload.GivenName,
		UsualName: payload.UsualName,
		UID:       payload.UID,
		Nonce:     payload.Nonce,
		IssuedAt:  issued,
		ExpiresAt: expires,
	}, nil
}

// audienceMatches reports whether the token's `aud` claim, which OIDC allows to
// be a string or an array of strings, names expected exactly.
func audienceMatches(raw json.RawMessage, expected string) bool {
	if len(raw) == 0 {
		return false
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return single == expected
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return false
	}
	for _, aud := range many {
		if aud == expected {
			return true
		}
	}
	return false
}
