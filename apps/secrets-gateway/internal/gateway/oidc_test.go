package gateway

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// signIDToken builds a real RS256 ID token so the verification path is
// exercised against actual cryptography rather than a stub.
func signIDToken(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// jwksFor publishes the key's public half in JWKS form.
func jwksFor(t *testing.T, key *rsa.PrivateKey, kid string) []byte {
	t.Helper()
	doc := map[string]any{
		"keys": []map[string]string{{
			"kty": "RSA",
			"use": "sig",
			"alg": "RS256",
			"kid": kid,
			"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	return raw
}

func TestVerifyRS256JWT(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	keys := map[string]*rsa.PublicKey{"kid-1": &key.PublicKey}
	const issuer = "https://test.proconnect.gouv.fr"

	valid := map[string]any{
		"iss":   issuer,
		"sub":   "sub-123",
		"aud":   "client-id",
		"email": "Dev@Beta.gouv.fr",
		"siret": "13002526500013",
		"acr":   "eidas2",
		"amr":   []string{"pwd", "totp"},
		"iat":   now.Add(-time.Minute).Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	verified, err := verifyRS256JWT(signIDToken(t, key, "kid-1", valid), keys, issuer, "client-id", now)
	if err != nil {
		t.Fatalf("verifyRS256JWT: %v", err)
	}
	if verified.Email != "dev@beta.gouv.fr" || verified.SIRET != "13002526500013" || verified.ACR != "eidas2" {
		t.Fatalf("verified claims = %+v", verified)
	}

	mutations := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"wrong issuer", func(c map[string]any) { c["iss"] = "https://evil.example" }},
		{"missing subject", func(c map[string]any) { c["sub"] = "" }},
		{"expired", func(c map[string]any) { c["exp"] = now.Add(-time.Hour).Unix() }},
		{"no expiry", func(c map[string]any) { c["exp"] = 0 }},
		{"issued in the future", func(c map[string]any) { c["iat"] = now.Add(time.Hour).Unix() }},
	}
	for _, tc := range mutations {
		claims := make(map[string]any, len(valid))
		for k, v := range valid {
			claims[k] = v
		}
		tc.mutate(claims)
		if _, err := verifyRS256JWT(signIDToken(t, key, "kid-1", claims), keys, issuer, "client-id", now); err == nil {
			t.Errorf("%s: token verified but should have failed", tc.name)
		}
	}

	// A signature by an unpublished key under the published kid fails.
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if _, err := verifyRS256JWT(signIDToken(t, otherKey, "kid-1", valid), keys, issuer, "client-id", now); err == nil {
		t.Fatal("a token signed by an unpublished key verified")
	}

	// An audience that does not name this client is refused: `aud` may be a
	// string or an array per OIDC, and neither form may pass for another client.
	for _, aud := range []any{"other-client", []string{"other-client", "second"}, []string{}} {
		claims := make(map[string]any, len(valid))
		for k, v := range valid {
			claims[k] = v
		}
		claims["aud"] = aud
		if _, err := verifyRS256JWT(signIDToken(t, key, "kid-1", claims), keys, issuer, "client-id", now); err == nil {
			t.Errorf("audience %v verified for client-id", aud)
		}
	}
	// An array containing this client is accepted.
	audArray := make(map[string]any, len(valid))
	for k, v := range valid {
		audArray[k] = v
	}
	audArray["aud"] = []string{"other-client", "client-id"}
	if _, err := verifyRS256JWT(signIDToken(t, key, "kid-1", audArray), keys, issuer, "client-id", now); err != nil {
		t.Errorf("an audience array containing the client was refused: %v", err)
	}
	// A missing audience is refused outright.
	noAud := make(map[string]any, len(valid))
	for k, v := range valid {
		if k == "aud" {
			continue
		}
		noAud[k] = v
	}
	if _, err := verifyRS256JWT(signIDToken(t, key, "kid-1", noAud), keys, issuer, "client-id", now); err == nil {
		t.Error("a token with no audience verified")
	}

	// An unknown kid fails rather than falling back to any available key.
	if _, err := verifyRS256JWT(signIDToken(t, key, "kid-unknown", valid), keys, issuer, "client-id", now); err == nil {
		t.Fatal("a token with an unknown kid verified")
	}

	// Non-RS256 algorithms are refused outright (no alg confusion, no HS256).
	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT", "kid": "kid-1"})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	payload, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	hs256 := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".x"
	if _, err := verifyRS256JWT(hs256, keys, issuer, "client-id", now); err == nil {
		t.Fatal("a non-RS256 token verified")
	}
}

func TestOIDCDiscoveryExchangeAndMFAEnforcement(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

	mux := http.NewServeMux()
	var issuer string
	tokens := map[string]string{}
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 issuer,
			"authorization_endpoint": issuer + "/api/v2/authorize",
			"token_endpoint":         issuer + "/api/v2/token",
			"userinfo_endpoint":      issuer + "/api/v2/userinfo",
			"jwks_uri":               issuer + "/api/v2/jwks",
		})
	})
	mux.HandleFunc("/api/v2/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksFor(t, key, "kid-1"))
	})
	mux.HandleFunc("/api/v2/token", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": tokens["current"]})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	issuer = server.URL

	idToken := func(acr, nonce string) string {
		return signIDToken(t, key, "kid-1", map[string]any{
			"iss": issuer, "sub": "sub-9", "aud": "client", "email": "a@beta.gouv.fr",
			"siret": "13002526500013", "acr": acr, "nonce": nonce,
			"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Hour).Unix(),
		})
	}

	newClient := func() *OIDCClient {
		client, err := NewOIDCClient(OIDCConfig{
			Issuer:       issuer,
			ClientID:     "client",
			ClientSecret: "secret",
			RedirectURL:  "https://gateway.test/auth/callback",
		}, server.Client(), func() time.Time { return now })
		if err != nil {
			t.Fatalf("NewOIDCClient: %v", err)
		}
		return client
	}
	client := newClient()

	authorizeURL, err := client.AuthorizeURL(t.Context(), "state-1", "nonce-1", "challenge-1")
	if err != nil {
		t.Fatalf("AuthorizeURL: %v", err)
	}
	for _, want := range []string{
		"response_type=code",
		"code_challenge=challenge-1",
		"code_challenge_method=S256",
		"claims=",
	} {
		if !strings.Contains(authorizeURL, want) {
			t.Errorf("authorize URL %q is missing %q", authorizeURL, want)
		}
	}

	tokens["current"] = idToken("eidas2", "nonce-1")
	identity, err := client.Exchange(t.Context(), "code-1", "verifier-1", "nonce-1")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if identity.Email != "a@beta.gouv.fr" || identity.ACR != "eidas2" {
		t.Fatalf("identity = %+v", identity)
	}

	// A single-factor assertion is refused.
	fresh := newClient()
	tokens["current"] = idToken("eidas1", "nonce-2")
	if _, err := fresh.Exchange(t.Context(), "code-2", "verifier-1", "nonce-2"); !errors.Is(err, ErrMFARequired) {
		t.Fatalf("single-factor exchange error = %v, want ErrMFARequired", err)
	}

	// A nonce mismatch is refused.
	fresh = newClient()
	tokens["current"] = idToken("eidas2", "nonce-3")
	if _, err := fresh.Exchange(t.Context(), "code-3", "verifier-1", "expected-other"); err == nil {
		t.Fatal("an ID token with a mismatched nonce was accepted")
	}
}

func TestProjectCreationEligibility(t *testing.T) {
	client := &OIDCClient{cfg: OIDCConfig{}}
	allowed := []Identity{
		{Email: "dev@beta.gouv.fr"},
		{Email: "someone@numerique.gouv.fr"},
		{Email: "contractor@example.com", SIRET: "13002526500013"},
	}
	for _, id := range allowed {
		if err := client.ValidateProjectCreation(id); err != nil {
			t.Errorf("ValidateProjectCreation(%+v) = %v, want nil", id, err)
		}
	}
	denied := Identity{Email: "random@gmail.com", SIRET: "not-a-siret"}
	if err := client.ValidateProjectCreation(denied); err == nil {
		t.Fatal("an ineligible identity was allowed to create a project")
	}

	// An explicit SIRET allowlist narrows acceptance to those SIRETs.
	restricted := &OIDCClient{cfg: OIDCConfig{AllowedSIRETs: []string{"11111111111111"}}}
	if err := restricted.ValidateProjectCreation(Identity{Email: "x@numerique.gouv.fr", SIRET: "13002526500013"}); err == nil {
		t.Fatal("a non-allowlisted SIRET created a project")
	}
	if err := restricted.ValidateProjectCreation(Identity{Email: "x@numerique.gouv.fr", SIRET: "11111111111111"}); err != nil {
		t.Fatalf("an allowlisted SIRET was refused: %v", err)
	}
}

// TestProConnectIssuerConstantsHaveTheApiV2Path guards the finding that the
// default issuer omitted `/api/v2`, which made discovery 404 and every login
// return 503. The path segment is part of the published issuer value.
func TestProConnectIssuerConstantsHaveTheApiV2Path(t *testing.T) {
	for name, issuer := range map[string]string{
		"sandbox":    ProConnectSandboxIssuer,
		"production": ProConnectProductionIssuer,
	} {
		if !strings.HasSuffix(issuer, "/api/v2") {
			t.Errorf("%s issuer %q must end with /api/v2", name, issuer)
		}
		if strings.Contains(issuer, "test.proconnect.gouv.fr") {
			t.Errorf("%s issuer %q is the partner-portal host, not the OIDC issuer", name, issuer)
		}
	}
}

// TestSignedUserInfoIsVerified covers the finding that a client registered for
// signed UserInfo responses received `application/jwt`, which the JSON decoder
// rejected, so profile claims were silently dropped.
func TestSignedUserInfoIsVerified(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

	mux := http.NewServeMux()
	var issuer string
	var signedUserInfo, forgedUserInfo bool
	mux.HandleFunc("/api/v2/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/authorize",
			"token_endpoint":                        issuer + "/token",
			"userinfo_endpoint":                     issuer + "/userinfo",
			"jwks_uri":                              issuer + "/jwks",
			"userinfo_signing_alg_values_supported": []string{"HS256", "ES256", "RS256"},
		})
	})
	mux.HandleFunc("/api/v2/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(jwksFor(t, key, "kid-1"))
	})
	mux.HandleFunc("/api/v2/token", func(w http.ResponseWriter, _ *http.Request) {
		// The ID token carries no email or SIRET, so the gateway must call
		// UserInfo to obtain them.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id_token": signIDToken(t, key, "kid-1", map[string]any{
				"iss": issuer, "sub": "sub-9", "aud": "client", "acr": "eidas2",
				"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Hour).Unix(),
			}),
			"access_token": "opaque",
		})
	})
	mux.HandleFunc("/api/v2/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if forgedUserInfo {
			w.Header().Set("Content-Type", "application/jwt")
			other, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Errorf("generate key: %v", err)
				return
			}
			_, _ = w.Write([]byte(signIDToken(t, other, "kid-1", map[string]any{
				"iss": issuer, "sub": "sub-9", "aud": "client", "acr": "eidas2",
				"email": "forged@example.com", "iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Hour).Unix(),
			})))
			return
		}
		if signedUserInfo {
			w.Header().Set("Content-Type", "application/jwt")
			_, _ = w.Write([]byte(signIDToken(t, key, "kid-1", map[string]any{
				"iss": issuer, "sub": "sub-9", "aud": "client", "acr": "eidas2",
				"email": "signed@beta.gouv.fr", "siret": "13002526500013", "uid": "uid-9",
				"given_name": "Ada", "usual_name": "Lovelace",
				"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Hour).Unix(),
			})))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"sub": "sub-9", "email": "plain@beta.gouv.fr", "siret": "13002526500013", "uid": "uid-9",
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	issuer = server.URL + "/api/v2"

	newClient := func() *OIDCClient {
		client, err := NewOIDCClient(OIDCConfig{
			Issuer: issuer, ClientID: "client", ClientSecret: "secret",
			RedirectURL: "https://gateway.test/auth/callback",
		}, server.Client(), func() time.Time { return now })
		if err != nil {
			t.Fatalf("NewOIDCClient: %v", err)
		}
		return client
	}

	// JSON UserInfo still works.
	identity, err := newClient().Exchange(t.Context(), "code-1", "v", "")
	if err != nil {
		t.Fatalf("Exchange(JSON userinfo): %v", err)
	}
	if identity.Email != "plain@beta.gouv.fr" {
		t.Fatalf("JSON userinfo email = %q", identity.Email)
	}

	// Signed UserInfo is verified and its claims are used.
	signedUserInfo = true
	identity, err = newClient().Exchange(t.Context(), "code-2", "v", "")
	if err != nil {
		t.Fatalf("Exchange(signed userinfo): %v", err)
	}
	if identity.Email != "signed@beta.gouv.fr" || identity.SIRET != "13002526500013" {
		t.Fatalf("signed userinfo identity = %+v", identity)
	}

	// A signed UserInfo with a bad signature must not be trusted.
	forgedUserInfo = true
	forged, err := newClient().Exchange(t.Context(), "code-3", "v", "")
	if err == nil && forged.Email == "forged@example.com" {
		t.Fatal("a UserInfo JWT signed by an unpublished key was trusted")
	}
}
