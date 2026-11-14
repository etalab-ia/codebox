package gatewayclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/etalab-ia/codebox/packages/secrets"
)

func TestClientSendsBearerAndDecodesResolve(t *testing.T) {
	var gotAuth, gotPath, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		raw := make([]byte, 1024)
		n, _ := r.Body.Read(raw)
		gotBody = string(raw[:n])
		_ = json.NewEncoder(w).Encode(secrets.ResolveResponse{
			ProjectID:  "22222222-2222-4222-8222-222222222222",
			Revision:   7,
			Components: map[string]map[string]string{".env": {"API_KEY": "v"}},
		})
	}))
	defer server.Close()

	client, err := New("http://gateway.example", "token-1", server.Client())
	if err == nil {
		t.Fatal("a plaintext http URL was accepted")
	}
	client = newForTest(server.URL, "token-1", server.Client())
	got, err := client.Resolve(context.Background(), "22222222-2222-4222-8222-222222222222", []string{".env"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if gotAuth != "Bearer token-1" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotPath != "/v1/projects/22222222-2222-4222-8222-222222222222/resolve" {
		t.Fatalf("path = %q", gotPath)
	}
	if !strings.Contains(gotBody, `"components":[".env"]`) {
		t.Fatalf("request body = %q", gotBody)
	}
	if got.Revision != 7 || got.Components[".env"]["API_KEY"] != "v" {
		t.Fatalf("response = %+v", got)
	}
}

func TestClientDecodesAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(secrets.APIError{Code: secrets.ErrCodeConflict, Message: "stale revision"})
	}))
	defer server.Close()
	client := newForTest(server.URL, "token-1", server.Client())
	_, err := client.PushSecrets(context.Background(), "id", secrets.PushSecretsRequest{BaseRevision: 1})
	if err == nil || !strings.Contains(err.Error(), secrets.ErrCodeConflict) {
		t.Fatalf("error = %v, want the API code", err)
	}
}

func TestLoginURLModes(t *testing.T) {
	client := newForTest("https://gateway.example", "token-1", nil)
	browser := client.LoginURL(53682)
	if !strings.Contains(browser, "/auth/login?") || !strings.Contains(browser, "port=53682") {
		t.Fatalf("browser login URL = %q", browser)
	}
	terminal := client.TerminalLoginURL()
	if !strings.Contains(terminal, "mode=terminal") || strings.Contains(terminal, "port=") {
		t.Fatalf("terminal login URL = %q", terminal)
	}
}

func TestExchangeTicketOmitsVerifier(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 512)
		n, _ := r.Body.Read(buf)
		body = string(buf[:n])
		_ = json.NewEncoder(w).Encode(secrets.TokenResponse{Token: "session", Identity: secrets.WhoAmIResponse{Email: "dev@beta.gouv.fr"}})
	}))
	defer server.Close()
	got, err := ExchangeTicket(context.Background(), server.URL, "ticket", server.Client())
	if err != nil {
		t.Fatalf("ExchangeTicket: %v", err)
	}
	if got.Token != "session" || got.Identity.Email != "dev@beta.gouv.fr" {
		t.Fatalf("response = %+v", got)
	}
	if strings.Contains(body, "verifier") {
		t.Fatalf("terminal exchange sent a misleading verifier field: %q", body)
	}
}
