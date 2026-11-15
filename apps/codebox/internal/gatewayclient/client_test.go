package gatewayclient

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
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

func TestGatewayHTTPClientsRefusePlaintextRedirect(t *testing.T) {
	tests := []struct {
		name string
		call func(context.Context, string, *http.Client) error
	}{
		{name: "authenticated API", call: func(ctx context.Context, base string, httpClient *http.Client) error {
			client, err := New(base, "session-token", httpClient)
			if err != nil {
				return err
			}
			_, err = client.PushSecrets(ctx, "22222222-2222-4222-8222-222222222222", secrets.PushSecretsRequest{
				BaseRevision: 1,
				Components:   map[string]map[string]string{".env": {"API_KEY": "secret-canary"}},
			})
			return err
		}},
		{name: "ticket exchange", call: func(ctx context.Context, base string, httpClient *http.Client) error {
			_, err := ExchangeTicket(ctx, base, "one-time-ticket", httpClient)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			type replay struct {
				body          string
				authorization string
			}
			replayed := make(chan replay, 1)
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				replayed <- replay{body: string(body), authorization: r.Header.Get("Authorization")}
				_ = json.NewEncoder(w).Encode(map[string]string{"token": "redirected-session"})
			}))
			defer target.Close()
			gateway := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			}))
			defer gateway.Close()

			if err := test.call(context.Background(), gateway.URL, gateway.Client()); err == nil {
				t.Error("plaintext redirect was followed without an error")
			}
			select {
			case got := <-replayed:
				t.Fatalf("redirect replayed body and credentials to plaintext: body=%q authorization=%q", got.body, got.authorization)
			default:
			}
		})
	}
}

func TestClientRefusesCrossOriginHTTPSRedirect(t *testing.T) {
	reached := make(chan string, 1)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached <- r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(secrets.WhoAmIResponse{Email: "attacker@example.com"})
	}))
	defer target.Close()
	gateway := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer gateway.Close()
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	defer transport.CloseIdleConnections()
	client, err := New(gateway.URL, "session-token", &http.Client{Transport: transport})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.WhoAmI(context.Background()); err == nil {
		t.Error("cross-origin HTTPS redirect was followed without an error")
	}
	select {
	case authorization := <-reached:
		t.Fatalf("cross-origin redirect received the bearer token %q", authorization)
	default:
	}
}

func TestClientFollowsSameOriginHTTPSRedirect(t *testing.T) {
	gateway := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/whoami" {
			http.Redirect(w, r, "/v1/whoami-final", http.StatusTemporaryRedirect)
			return
		}
		if r.URL.Path != "/v1/whoami-final" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(secrets.WhoAmIResponse{Email: "dev@beta.gouv.fr"})
	}))
	defer gateway.Close()
	client, err := New(gateway.URL, "session-token", gateway.Client())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	who, err := client.WhoAmI(context.Background())
	if err != nil || who.Email != "dev@beta.gouv.fr" {
		t.Fatalf("same-origin redirect failed: %+v, %v", who, err)
	}
}

func TestLoginURLModes(t *testing.T) {
	client := newForTest("https://gateway.example", "token-1", nil)
	browser := client.LoginURL(53682, "loopback-state")
	if !strings.Contains(browser, "/auth/login?") || !strings.Contains(browser, "port=53682") || !strings.Contains(browser, "loopback_state=loopback-state") {
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
