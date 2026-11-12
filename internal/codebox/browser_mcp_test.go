package codebox

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestBrowserMCPOverlayUsesPinnedGuestLocalCommands(t *testing.T) {
	content, err := ComposeConfigContent(ManagedOverlay{MCPConnectors: []string{"playwright", "chrome-devtools"}})
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		MCP map[string]struct {
			Type    string   `json:"type"`
			Command []string `json:"command"`
			Enabled bool     `json:"enabled"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(content), &config); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"chrome-devtools": {
			"chrome-devtools-mcp",
			"--browserUrl", "http://127.0.0.1:9222",
			"--no-usage-statistics",
			"--no-performance-crux",
		},
	}
	for name, command := range want {
		got, ok := config.MCP[name]
		if !ok || got.Type != "local" || !got.Enabled || strings.Join(got.Command, "\x00") != strings.Join(command, "\x00") {
			t.Errorf("%s config = %+v", name, got)
		}
	}
	if _, ok := config.MCP["playwright"]; ok {
		t.Fatal("withdrawn playwright must stay inactive: no managed entry")
	}
	if strings.Contains(content, "latest") {
		t.Fatal("browser MCP config contains a floating package reference")
	}
	if _, ok := config.MCP["data-gouv"]; ok {
		t.Fatal("browser selection enabled an unrelated remote MCP")
	}
}

func TestDefaultMicrosandboxImageIsDigestPinned(t *testing.T) {
	if strings.Contains(msbImage, ":latest") || !strings.Contains(msbImage, "@sha256:") {
		t.Fatalf("default Microsandbox image is floating: %q", msbImage)
	}
	digest := strings.TrimPrefix(msbImage[strings.LastIndex(msbImage, "@sha256:"):], "@sha256:")
	if !isSHA256(digest) {
		t.Fatalf("default Microsandbox image has an invalid digest: %q", digest)
	}
}

func TestMCPSelectionCombinesRemoteAndBrowserWithoutInstallingUnselectedTools(t *testing.T) {
	ids, err := ValidateMCPNewSelection([]string{"chrome-devtools", "data-gouv"})
	if err != nil || strings.Join(ids, ",") != "chrome-devtools,data-gouv" {
		t.Fatalf("mixed MCP selection = %v, %v", ids, err)
	}
	content, err := ComposeConfigContent(ManagedOverlay{MCPConnectors: ids})
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		MCP map[string]map[string]any `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(content), &config); err != nil {
		t.Fatal(err)
	}
	if len(config.MCP) != 2 || config.MCP["chrome-devtools"]["type"] != "local" || config.MCP["data-gouv"]["type"] != "remote" {
		t.Fatalf("mixed MCP overlay = %#v", config.MCP)
	}
}

func TestBrowserMCPsHavePinnedPackageVersions(t *testing.T) {
	for _, connector := range BrowserMCPs() {
		if connector.Package == "" || connector.Version == "" || strings.Contains(connector.Version, "latest") {
			t.Errorf("browser MCP is not version-pinned: %+v", connector)
		}
	}
}

// The browser MCP server ships with Google usage statistics and CrUX trace-URL
// reporting enabled by default, and checks the npm registry for updates on
// start. Each opt-out is asserted separately so dropping one fails for its own
// reason rather than under a shared "command changed" message (#370).
func TestBrowserMCPDisablesUpstreamTelemetryAndUpdateChecks(t *testing.T) {
	content, err := ComposeConfigContent(ManagedOverlay{MCPConnectors: []string{"chrome-devtools"}})
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		MCP map[string]struct {
			Command []string `json:"command"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(content), &config); err != nil {
		t.Fatal(err)
	}
	command := config.MCP["chrome-devtools"].Command
	if len(command) == 0 {
		t.Fatal("chrome-devtools has no composed command")
	}
	for _, flag := range []string{"--no-usage-statistics", "--no-performance-crux"} {
		if !slices.Contains(command, flag) {
			t.Errorf("chrome-devtools command %v must carry %s: upstream enables it by default", command, flag)
		}
	}

	m := newTestMicrosandbox(t, &fakeMSBClient{})
	m.OpenCodeOverlay.MCPConnectors = []string{"chrome-devtools"}
	spec := m.sandboxSpec(nil)
	if spec.Env[msbBrowserMCPNoUpdateChecksEnv] != "1" {
		t.Errorf("guest env %s = %q, want \"1\"", msbBrowserMCPNoUpdateChecksEnv, spec.Env[msbBrowserMCPNoUpdateChecksEnv])
	}
	// A guest without the browser tool must not carry the browser env at all,
	// so the opt-out cannot silently become an unconditional guest setting.
	plain := newTestMicrosandbox(t, &fakeMSBClient{})
	if _, present := plain.sandboxSpec(nil).Env[msbBrowserMCPNoUpdateChecksEnv]; present {
		t.Error("a project without the browser MCP must not receive the browser telemetry env")
	}
}

func TestBrowserSelectionUsesPinnedGuestProfileAndLoopbackDevTools(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	m.OpenCodeOverlay.MCPConnectors = []string{"chrome-devtools"}
	spec := m.sandboxSpec(nil)
	if spec.Image != msbBrowserImage || !strings.Contains(spec.Image, "@sha256:") {
		t.Fatalf("browser selection image = %q, want digest-pinned %q", spec.Image, msbBrowserImage)
	}
	if spec.Env["CODEBOX_BROWSER_MCP_PACKAGES"] != "chrome-devtools-mcp@1.10.1" {
		t.Fatalf("selected browser package env = %q", spec.Env["CODEBOX_BROWSER_MCP_PACKAGES"])
	}
	if spec.Env[msbBrowserMCPFingerprintEnv] == "" || spec.Env["CODEBOX_BROWSER_PROFILE"] == "" {
		t.Fatal("browser profile and install fingerprint must be set")
	}
	if !strings.Contains(spec.StartScript, "--remote-debugging-address=127.0.0.1") || !strings.Contains(spec.StartScript, "--remote-debugging-port=9222") {
		t.Fatal("Chrome DevTools must start Chromium on guest loopback")
	}
	if strings.Contains(spec.StartScript, "--remote-debugging-address=0.0.0.0") {
		t.Fatal("Chrome DevTools must not listen on guest network interfaces")
	}
	if _, mapped := msbPortMappings(IsolationFull)[9222]; mapped {
		t.Fatal("the guest DevTools port must not be exposed through host port mapping")
	}
	if strings.Contains(spec.StartScript, "latest") {
		t.Fatal("browser guest start script contains a floating version")
	}
	m.OpenCodeOverlay.MCPConnectors = nil
	defaultSpec := m.sandboxSpec(nil)
	if defaultSpec.Image == msbBrowserImage {
		t.Fatal("a selection without a browser MCP must keep the default guest image")
	}
	if defaultSpec.Env[msbBrowserMCPIDsEnv] != "" || defaultSpec.Env[msbBrowserMCPPackagesEnv] != "" {
		t.Fatalf("a selection without a browser MCP must not install browser packages: env=%v", defaultSpec.Env)
	}
}

func TestBrowserGuestProfileChangeRequiresExplicitRecreation(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{exists: true, status: "running"})
	if m.Probe == nil {
		m.Probe = func(context.Context, string, string, string) HealthProbe { return HealthProbe{Healthy: true} }
	}
	applied := m.desiredState(true, nil, nil).toState()
	m.OpenCodeOverlay.MCPConnectors = []string{"chrome-devtools"}
	desired := m.desiredState(true, nil, nil)
	if desired.Image != msbBrowserImage || desired.GuestProfile == "" {
		t.Fatalf("browser desired profile = image %q, profile %q", desired.Image, desired.GuestProfile)
	}
	facts, err := m.reconcileFacts(context.Background(), &applied, desired)
	if err != nil {
		t.Fatalf("reconcileFacts: %v", err)
	}
	if !facts.CreationFixedChanged || !PlanReconcile(&applied, desired, facts).NeedsRecreate() {
		t.Fatal("switching an existing default guest to the browser profile must require explicit recreation")
	}
}

func TestBrowserReadinessRequiresMatchingToolInstallFingerprint(t *testing.T) {
	if !strings.Contains(msbGuestPrepareProbe, msbBrowserMCPMarker) || !strings.Contains(msbGuestPrepareProbe, msbBrowserMCPFingerprintEnv) {
		t.Fatal("guest readiness probe must compare browser install marker with desired fingerprint")
	}
	remoteOnly := browserMCPFingerprint([]string{"data-gouv"})
	both := browserMCPFingerprint([]string{"data-gouv", "chrome-devtools"})
	if remoteOnly != "" || both == "" {
		t.Fatalf("browser install fingerprint must be empty without a browser selection and nonempty with one: %q / %q", remoteOnly, both)
	}
}
