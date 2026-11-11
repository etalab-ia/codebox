package codebox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateMCPConnectorIDs(t *testing.T) {
	got, err := ValidateMCPConnectorIDs([]string{" context7 ", "data-gouv", "context7"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "context7,data-gouv" {
		t.Fatalf("connector IDs = %v", got)
	}
	if _, err := ValidateMCPConnectorIDs([]string{"uncurated-tool"}); err == nil {
		t.Fatal("uncurated connector was accepted")
	}
}

func TestComposeConfigContentAddsOnlySelectedRemoteMCPs(t *testing.T) {
	content, err := ComposeConfigContent(ManagedOverlay{MCPConnectors: []string{"data-gouv", "context7"}})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(content), &config); err != nil {
		t.Fatal(err)
	}
	servers := config["mcp"].(map[string]any)
	entry := servers["data-gouv"].(map[string]any)
	if entry["type"] != "remote" || entry["url"] != "https://mcp.data.gouv.fr/mcp" || entry["enabled"] != true {
		t.Errorf("unexpected data-gouv entry: %#v", entry)
	}
	if _, ok := servers["context7"]; ok {
		t.Fatal("withdrawn context7 must stay inactive: no managed entry")
	}
	if strings.Contains(content, "playwright") || strings.Contains(content, "chrome-devtools") {
		t.Fatal("P15 configuration included unrelated browser tooling")
	}
}

func TestWithdrawnMCPSelectionStaysInactiveInOverlay(t *testing.T) {
	content, err := ComposeConfigContent(ManagedOverlay{MCPConnectors: []string{"context7"}})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(content), &config); err != nil {
		t.Fatal(err)
	}
	if servers, ok := config["mcp"].(map[string]any); ok {
		if _, active := servers["context7"]; active {
			t.Fatal("withdrawn context7 became an active managed entry")
		}
	}
}

func TestDetectOverlayConflictsForManagedMCPName(t *testing.T) {
	project := map[string]any{"mcp": map[string]any{"data-gouv": map[string]any{"type": "remote", "url": "https://other.example/mcp"}}}
	conflicts := DetectOverlayConflicts(project, ManagedOverlay{MCPConnectors: []string{"data-gouv"}})
	if len(conflicts) != 1 || conflicts[0].Field != "mcp.data-gouv" {
		t.Fatalf("conflicts = %#v", conflicts)
	}
}

func TestProbeRemoteMCPDistinguishesVerificationAuthenticationAndDrift(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   MCPHealthState
	}{
		{"verified", http.StatusOK, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26"}}`, MCPVerified},
		{"authentication required", http.StatusUnauthorized, `{}`, MCPAuthNeeded},
		{"anonymous access denied", http.StatusForbidden, `{}`, MCPAccessDenied},
		{"endpoint drift", http.StatusNotFound, `{}`, MCPDrift},
		{"schema drift", http.StatusOK, `{"unexpected":true}`, MCPDrift},
		{"mismatched JSON-RPC response ID", http.StatusOK, `{"jsonrpc":"2.0","id":2,"result":{"protocolVersion":"2025-03-26"}}`, MCPDrift},
		{"unsupported protocol version", http.StatusOK, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2030-01-01"}}`, MCPDrift},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("request = %s %s content-type=%q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			result := ProbeRemoteMCP(context.Background(), server.Client(), MCPConnector{ID: "test", Endpoint: server.URL})
			if result.State != tc.want {
				t.Fatalf("state = %q, want %q (%s)", result.State, tc.want, result.Detail)
			}
		})
	}
}

func TestProbeRemoteMCPReportsUnavailableEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := server.URL
	server.Close()
	result := ProbeRemoteMCP(context.Background(), nil, MCPConnector{ID: "offline", Endpoint: endpoint})
	if result.State != MCPOffline {
		t.Fatalf("state = %q, want offline", result.State)
	}
}

// The review screen reads this func's verdict as the reachability line: a
// successful handshake must be a pass, and configured is only a probe's
// initial state — accepting it there turned every reachable endpoint into a
// false access failure (#242 review).
func TestProbeRemoteMCPFuncDistinguishesVerifiedFromFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26"}}`))
	}))
	defer server.Close()
	if err := ProbeRemoteMCPFunc(context.Background(), MCPConnector{ID: "test", Endpoint: server.URL}); err != nil {
		t.Fatalf("a verified handshake must pass the review probe: %v", err)
	}
	offline := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := offline.URL
	offline.Close()
	if err := ProbeRemoteMCPFunc(context.Background(), MCPConnector{ID: "offline", Endpoint: endpoint}); err == nil {
		t.Fatal("an unreachable endpoint must fail the review probe")
	}
}

func TestMCPResponseJSONReadsMultilineAndLongSSEData(t *testing.T) {
	multiline := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\ndata: \"id\":1,\"result\":{\"protocolVersion\":\"2025-03-26\"}}\n\n")
	for name, input := range map[string][]byte{
		"multiline": multiline,
		"long line": []byte("data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":\"2025-03-26\",\"padding\":\"" + strings.Repeat("x", 70_000) + "\"}}\n\n"),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := mcpResponseJSON(input)
			if err != nil || !json.Valid(got) {
				t.Fatalf("mcpResponseJSON = %q, %v", got, err)
			}
		})
	}
}
