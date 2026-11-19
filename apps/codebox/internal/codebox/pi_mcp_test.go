package codebox

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestComposePiMCPConfigUsesNativeRemoteServers(t *testing.T) {
	raw, err := ComposePiMCPConfig([]string{"chrome-devtools", "context7", "data-gouv"})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("mcp.json is invalid: %v\n%s", err, raw)
	}
	if len(doc.MCPServers) != 1 {
		t.Fatalf("mcpServers = %v, want only data-gouv; browser and withdrawn entries stay inactive", doc.MCPServers)
	}
	server := doc.MCPServers["data-gouv"]
	if server["url"] != "https://mcp.data.gouv.fr/mcp" || server["enabled"] != true {
		t.Fatalf("data-gouv entry = %v", server)
	}
	for _, forbidden := range []string{"headers", "auth", "command", "env"} {
		if _, ok := server[forbidden]; ok {
			t.Fatalf("anonymous curated MCP unexpectedly carries %q: %v", forbidden, server)
		}
	}
}

func TestComposePiMCPConfigRejectsUnknownServer(t *testing.T) {
	if _, err := ComposePiMCPConfig([]string{"unknown"}); err == nil {
		t.Fatal("an unknown MCP selection must fail rather than producing an incomplete guest config")
	}
}

func TestPiMCPSelectionChangesDesiredFingerprint(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	m.cfg.Harness = HarnessPi
	m.OpenCodeOverlay = albertOverlay()
	before := m.desiredState(true, nil, nil).PiConfigFingerprint
	m.OpenCodeOverlay.MCPConnectors = []string{"data-gouv"}
	after := m.desiredState(true, nil, nil).PiConfigFingerprint
	if before == "" || after == "" || before == after {
		t.Fatalf("MCP selection must change the Pi readiness fingerprint: before=%q after=%q", before, after)
	}
}

func TestPiManagedConfigRejectsUnknownMCP(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	m.cfg.Harness = HarnessPi
	m.OpenCodeOverlay = albertOverlay()
	m.OpenCodeOverlay.MCPConnectors = []string{"unknown"}
	if err := m.piManagedConfigError(); err == nil || !strings.Contains(err.Error(), "MCP") {
		t.Fatalf("Pi managed config error = %v, want the MCP selection failure", err)
	}
}

func TestPiPrepWritesMCPBeforeReadinessMarker(t *testing.T) {
	marker := strings.Index(piGuestPrepScript, `printf '%s\n' "$CODEBOX_PI_CONFIG_FINGERPRINT"`)
	for _, path := range []string{"models.json", "settings.json", "mcp.json"} {
		at := strings.Index(piGuestPrepScript, `> "$HOME/.pi/agent/`+path+`"`)
		if at < 0 || marker < 0 || at > marker {
			t.Fatalf("Pi prep must write %s before pi-config-ready:\n%s", path, piGuestPrepScript)
		}
	}
}
