package gateway

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testOIDCClientSecret = "test-client-secret"

func signingTestMaterial(t *testing.T, alg string) (any, []byte) {
	t.Helper()
	switch alg {
	case "RS256":
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("generate RSA key: %v", err)
		}
		return key, jwksFor(t, key, "kid-1")
	case "ES256":
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("generate ECDSA key: %v", err)
		}
		return key, jwksForES256(t, key, "kid-1")
	case "HS256":
		return testOIDCClientSecret, []byte(`{"keys":[]}`)
	default:
		t.Fatalf("unknown test algorithm %q", alg)
		return nil, nil
	}
}

func publicProviderKeys(signingKey any) *providerKeys {
	switch key := signingKey.(type) {
	case *rsa.PrivateKey:
		return &providerKeys{rsaByKid: map[string]*rsa.PublicKey{"kid-1": &key.PublicKey}}
	case *ecdsa.PrivateKey:
		return &providerKeys{ecdsaByKid: map[string]*ecdsa.PublicKey{"kid-1": &key.PublicKey}}
	default:
		return nil
	}
}

func TestOIDCIDTokenSigningAlgorithms(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for _, alg := range []string{"RS256", "ES256", "HS256"} {
		t.Run(alg, func(t *testing.T) {
			signingKey, jwks := signingTestMaterial(t, alg)
			var issuer, idToken string
			jwksRequests := 0
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v2/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]string{
					"issuer":                 issuer,
					"authorization_endpoint": issuer + "/authorize",
					"token_endpoint":         issuer + "/token",
					"userinfo_endpoint":      issuer + "/userinfo",
					"jwks_uri":               issuer + "/jwks",
				})
			})
			mux.HandleFunc("/api/v2/jwks", func(w http.ResponseWriter, _ *http.Request) {
				jwksRequests++
				_, _ = w.Write(jwks)
			})
			mux.HandleFunc("/api/v2/token", func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]string{"id_token": idToken})
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			issuer = server.URL + "/api/v2"
			idToken = signJWT(t, alg, "kid-1", signingKey, map[string]any{
				"iss": issuer, "sub": "sub-" + alg, "aud": "client",
				"email": "user@beta.gouv.fr", "siret": "13002526500013", "acr": "eidas0-mfa",
				"nonce": "nonce-" + alg, "iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Hour).Unix(),
			})
			newClient := func() *OIDCClient {
				client, err := NewOIDCClient(OIDCConfig{
					Issuer: issuer, ClientID: "client", ClientSecret: testOIDCClientSecret,
					RedirectURL: "https://gateway.test/auth/callback",
				}, server.Client(), func() time.Time { return now })
				if err != nil {
					t.Fatalf("NewOIDCClient: %v", err)
				}
				return client
			}

			identity, err := newClient().Exchange(t.Context(), "code-"+alg, "verifier", "nonce-"+alg)
			if err != nil {
				t.Fatalf("Exchange with %s: %v", alg, err)
			}
			if identity.Subject != "sub-"+alg || identity.Email != "user@beta.gouv.fr" || identity.ACR != "eidas0-mfa" {
				t.Fatalf("identity = %+v", identity)
			}
			if identity.IDToken != idToken {
				t.Fatal("the verified raw ID token was not retained for logout")
			}
			if _, err := newClient().Exchange(t.Context(), "wrong-nonce-"+alg, "verifier", "other-nonce"); err == nil {
				t.Fatal("an ID token with a mismatched nonce was accepted")
			}
			if alg == "HS256" && jwksRequests != 0 {
				t.Fatalf("HS256 fetched provider JWKS %d times; only the client secret must be used", jwksRequests)
			}
		})
	}
}

func TestSignedUserInfoSigningAlgorithms(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for _, alg := range []string{"RS256", "ES256", "HS256"} {
		t.Run(alg, func(t *testing.T) {
			signingKey, jwks := signingTestMaterial(t, alg)
			var issuer, idToken string
			var profile map[string]any
			jwksRequests := 0
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v2/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]string{
					"issuer":                 issuer,
					"authorization_endpoint": issuer + "/authorize",
					"token_endpoint":         issuer + "/token",
					"userinfo_endpoint":      issuer + "/userinfo",
					"jwks_uri":               issuer + "/jwks",
				})
			})
			mux.HandleFunc("/api/v2/jwks", func(w http.ResponseWriter, _ *http.Request) {
				jwksRequests++
				_, _ = w.Write(jwks)
			})
			mux.HandleFunc("/api/v2/token", func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]string{"id_token": idToken, "access_token": "opaque"})
			})
			mux.HandleFunc("/api/v2/userinfo", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/jwt")
				_, _ = w.Write([]byte(signJWT(t, alg, "kid-1", signingKey, profile)))
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			issuer = server.URL + "/api/v2"
			idToken = signJWT(t, alg, "kid-1", signingKey, map[string]any{
				"iss": issuer, "sub": "sub-9", "aud": "client", "acr": "eidas2",
				"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Hour).Unix(),
			})
			baseProfile := map[string]any{
				"iss": issuer, "sub": "sub-9", "aud": "client", "acr": "eidas2",
				"email": "signed@beta.gouv.fr", "siret": "13002526500013", "uid": "uid-9",
				"given_name": "Ada", "usual_name": "Lovelace",
				"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Hour).Unix(),
			}
			for _, tc := range []struct {
				name    string
				mutate  func(map[string]any)
				wantErr bool
			}{
				{name: "valid"},
				{name: "no expiry", mutate: func(c map[string]any) { delete(c, "exp") }},
				{name: "wrong issuer", mutate: func(c map[string]any) { c["iss"] = "https://evil.example" }, wantErr: true},
				{name: "wrong audience", mutate: func(c map[string]any) { c["aud"] = "other-client" }, wantErr: true},
				{name: "expired", mutate: func(c map[string]any) { c["exp"] = now.Add(-time.Hour).Unix() }, wantErr: true},
				{name: "subject mismatch", mutate: func(c map[string]any) { c["sub"] = "someone-else" }, wantErr: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					profile = make(map[string]any, len(baseProfile))
					for key, value := range baseProfile {
						profile[key] = value
					}
					if tc.mutate != nil {
						tc.mutate(profile)
					}
					client, err := NewOIDCClient(OIDCConfig{
						Issuer: issuer, ClientID: "client", ClientSecret: testOIDCClientSecret,
						RedirectURL: "https://gateway.test/auth/callback",
					}, server.Client(), func() time.Time { return now })
					if err != nil {
						t.Fatalf("NewOIDCClient: %v", err)
					}
					identity, err := client.Exchange(t.Context(), "code-"+tc.name, "verifier", "")
					if tc.wantErr {
						if err == nil {
							t.Fatalf("Exchange accepted invalid signed UserInfo: %+v", identity)
						}
						return
					}
					if err != nil {
						t.Fatalf("Exchange with signed UserInfo: %v", err)
					}
					if identity.Email != "signed@beta.gouv.fr" || identity.SIRET != "13002526500013" || identity.NameHint != "Ada Lovelace" {
						t.Fatalf("signed UserInfo identity = %+v", identity)
					}
				})
			}
			if alg == "HS256" && jwksRequests != 0 {
				t.Fatalf("HS256 signed UserInfo fetched provider JWKS %d times", jwksRequests)
			}
		})
	}
}

func TestJWTSignatureAndClaimValidationForRegisteredAlgorithms(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	const issuer = "https://issuer.example/api/v2"
	for _, alg := range []string{"RS256", "ES256", "HS256"} {
		t.Run(alg, func(t *testing.T) {
			signingKey, _ := signingTestMaterial(t, alg)
			keys := publicProviderKeys(signingKey)
			valid := map[string]any{
				"iss": issuer, "sub": "sub-123", "aud": "client-id",
				"email": "Dev@Beta.gouv.fr", "siret": "13002526500013", "acr": "eidas2",
				"nonce": "nonce-123", "iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Hour).Unix(),
			}
			token := signJWT(t, alg, "kid-1", signingKey, valid)
			verified, err := verifyJWT(token, keys, testOIDCClientSecret, issuer, "client-id", now)
			if err != nil {
				t.Fatalf("verifyJWT with %s: %v", alg, err)
			}
			if verified.Subject != "sub-123" || verified.Email != "dev@beta.gouv.fr" || verified.Nonce != "nonce-123" {
				t.Fatalf("verified claims = %+v", verified)
			}

			var wrongKey any
			switch alg {
			case "RS256":
				wrongKey, err = rsa.GenerateKey(rand.Reader, 2048)
			case "ES256":
				wrongKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			case "HS256":
				wrongKey = "wrong-client-secret"
			}
			if err != nil {
				t.Fatalf("generate wrong key: %v", err)
			}
			if _, err := verifyJWT(signJWT(t, alg, "kid-1", wrongKey, valid), keys, testOIDCClientSecret, issuer, "client-id", now); err == nil {
				t.Fatal("a token signed with the wrong key was accepted")
			}

			parts := strings.Split(token, ".")
			rawPayload, err := base64.RawURLEncoding.DecodeString(parts[1])
			if err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			var tamperedClaims map[string]any
			if err := json.Unmarshal(rawPayload, &tamperedClaims); err != nil {
				t.Fatalf("decode claims: %v", err)
			}
			tamperedClaims["sub"] = "tampered-subject"
			rawPayload, err = json.Marshal(tamperedClaims)
			if err != nil {
				t.Fatalf("encode tampered claims: %v", err)
			}
			parts[1] = base64.RawURLEncoding.EncodeToString(rawPayload)
			if _, err := verifyJWT(strings.Join(parts, "."), keys, testOIDCClientSecret, issuer, "client-id", now); err == nil {
				t.Fatal("a token with a tampered payload was accepted")
			}

			for _, tc := range []struct {
				name   string
				mutate func(map[string]any)
			}{
				{name: "wrong issuer", mutate: func(c map[string]any) { c["iss"] = "https://evil.example" }},
				{name: "wrong audience", mutate: func(c map[string]any) { c["aud"] = "other-client" }},
				{name: "missing subject", mutate: func(c map[string]any) { c["sub"] = "" }},
				{name: "expired", mutate: func(c map[string]any) { c["exp"] = now.Add(-time.Hour).Unix() }},
				{name: "no expiry", mutate: func(c map[string]any) { delete(c, "exp") }},
				{name: "issued in the future", mutate: func(c map[string]any) { c["iat"] = now.Add(time.Hour).Unix() }},
			} {
				t.Run(tc.name, func(t *testing.T) {
					claims := make(map[string]any, len(valid))
					for key, value := range valid {
						claims[key] = value
					}
					tc.mutate(claims)
					if _, err := verifyJWT(signJWT(t, alg, "kid-1", signingKey, claims), keys, testOIDCClientSecret, issuer, "client-id", now); err == nil {
						t.Fatal("invalid claims were accepted")
					}
				})
			}
		})
	}
}

func TestHS256NeverUsesPublicJWKSMaterial(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	const issuer = "https://issuer.example/api/v2"
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	keys := &providerKeys{rsaByKid: map[string]*rsa.PublicKey{"kid-1": &rsaKey.PublicKey}}
	claims := map[string]any{
		"iss": issuer, "sub": "sub-123", "aud": "client-id",
		"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Hour).Unix(),
	}

	// Positive control through the same verifier: the configured secret works.
	valid := signJWT(t, "HS256", "kid-1", testOIDCClientSecret, claims)
	if _, err := verifyJWT(valid, keys, testOIDCClientSecret, issuer, "client-id", now); err != nil {
		t.Fatalf("client-secret HS256 token was rejected: %v", err)
	}
	// Public JWKS material is not an HMAC credential and must never verify.
	forged := signJWT(t, "HS256", "kid-1", string(jwksFor(t, rsaKey, "kid-1")), claims)
	if _, err := verifyJWT(forged, keys, testOIDCClientSecret, issuer, "client-id", now); err == nil {
		t.Fatal("HS256 accepted a signature made with public JWKS material")
	}
}
