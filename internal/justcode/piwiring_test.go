package justcode

import (
	"path/filepath"
	"strings"
	"testing"
)

// A Pi guest must be prepared by the Pi prep script: the OpenCode one installs
// a CLI the harness will never run, and Pi's own prep is what writes the
// managed models.json the guest reads.
func TestMSBStartScriptUsesPiPrepForPi(t *testing.T) {
	script := msbStartScriptFor(IsolationFull, nil, HarnessPi)
	if !strings.Contains(script, "JUST_CODE_PI_VERSION") || !strings.Contains(script, "pi-coding-agent") {
		t.Fatalf("Pi start script does not install Pi:\n%s", script)
	}
	if strings.Contains(script, "@opencode/cli@") {
		t.Fatalf("Pi start script installs OpenCode:\n%s", script)
	}
	if !strings.Contains(script, "exec sleep infinity") {
		t.Fatalf("Pi start script does not keep the guest alive for the TUI:\n%s", script)
	}
	// The OpenCode harness keeps its own prep: the two must not have merged.
	opencode := msbStartScriptFor(IsolationFull, nil, HarnessOpenCode)
	if !strings.Contains(opencode, "@opencode/cli@") || strings.Contains(opencode, "pi-coding-agent") {
		t.Fatalf("OpenCode start script changed:\n%s", opencode)
	}
}

func TestPiSandboxEnvCarriesVersionAndConfiguration(t *testing.T) {
	overlay := albertOverlay()
	env := piSandboxEnv(HarnessPi, overlay)
	if env[PiVersionEnv] != PiVersion {
		t.Fatalf("%s = %q, want the pinned version", PiVersionEnv, env[PiVersionEnv])
	}
	if env[PiSkipVersionCheckEnv] != "1" {
		t.Fatalf("%s = %q", PiSkipVersionCheckEnv, env[PiSkipVersionCheckEnv])
	}
	if !strings.Contains(env[PiModelsEnv], "Qwen/Qwen3-Coder-30B-A3B-Instruct") {
		t.Fatalf("models.json does not carry the API model id: %s", env[PiModelsEnv])
	}
	if !strings.Contains(env[PiSettingsEnv], "enableInstallTelemetry") {
		t.Fatalf("settings.json is missing: %s", env[PiSettingsEnv])
	}
	// The provider key must never be a value here: the environment carries the
	// variable name only, exactly as the OpenCode overlay does.
	if strings.Contains(env[PiModelsEnv], "ALBERT_API_KEY:") {
		t.Fatalf("models.json embeds a literal key: %s", env[PiModelsEnv])
	}
	if got := piSandboxEnv(HarnessOpenCode, overlay); got != nil {
		t.Fatalf("OpenCode harness got a Pi environment: %v", got)
	}
}

// Readiness is harness-specific: waiting for `opencode --version` in a Pi
// guest would never become true, and the launch would time out for the wrong
// reason.
func TestMSBGuestPrepareProbeFollowsTheHarness(t *testing.T) {
	pi := msbGuestPrepareProbeFor(HarnessPi)
	if !strings.Contains(pi, "pi --version") || strings.Contains(pi, "opencode --version") {
		t.Fatalf("Pi probe = %s", pi)
	}
	opencode := msbGuestPrepareProbeFor(HarnessOpenCode)
	if !strings.Contains(opencode, "opencode --version") {
		t.Fatalf("OpenCode probe = %s", opencode)
	}
	// Both probes keep the browser-MCP fingerprint contract the launch relies
	// on, and the marker that says the prep finished.
	for _, probe := range []string{pi, opencode} {
		if !strings.Contains(probe, msbToolchainMarker) || !strings.Contains(probe, msbBrowserMCPFingerprintEnv) {
			t.Fatalf("probe lost the preparation contract: %s", probe)
		}
	}
}

// The harness is a project setting: it must survive a manifest round trip, and
// a manifest without it must keep meaning "OpenCode".
func TestProjectManifestHarnessRoundTrip(t *testing.T) {
	fs := newMapFS()
	path := filepath.Join("/project", ".just-code", "project.json")
	if err := WriteProjectManifest(fs, path, ProjectManifest{Harness: string(HarnessPi)}); err != nil {
		t.Fatal(err)
	}
	pm, err := ReadProjectManifest(fs, path)
	if err != nil {
		t.Fatal(err)
	}
	if pm.Harness != string(HarnessPi) {
		t.Fatalf("harness = %q after a round trip", pm.Harness)
	}
	if !strings.Contains(string(fs.files[path]), `"harness": "pi"`) {
		t.Fatalf("harness is not written to the manifest: %s", fs.files[path])
	}

	legacy := filepath.Join("/legacy", ".just-code", "project.json")
	if err := WriteProjectManifest(fs, legacy, ProjectManifest{Model: "albert/qwen3-coder"}); err != nil {
		t.Fatal(err)
	}
	pm, err = ReadProjectManifest(fs, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if pm.Harness != "" {
		t.Fatalf("a manifest with no harness carries %q; empty means the default", pm.Harness)
	}
	if hm, err := ResolveHarness(pm.Harness); err != nil || hm != HarnessOpenCode {
		t.Fatalf("resolved harness = %q, %v", hm, err)
	}
}

// The launch line is the last thing between the user and the agent: it must
// exec Pi, resume only when there is something to resume, and keep the
// workspace quoted (the Tart share path contains spaces).
func TestPiAgentLaunch(t *testing.T) {
	fresh := piAgentLaunch("/tmp/just-code-agent.env", "/Volumes/My Shared Files/workspace", piResumeArgs(false))
	if !strings.Contains(fresh, "exec pi") || strings.Contains(fresh, "--continue") {
		t.Fatalf("fresh launch = %s", fresh)
	}
	if !strings.Contains(fresh, `'/Volumes/My Shared Files/workspace'`) {
		t.Fatalf("workspace is not shell-quoted: %s", fresh)
	}
	if !strings.Contains(fresh, "set -a; . '/tmp/just-code-agent.env'; set +a") {
		t.Fatalf("the secrets file is not sourced: %s", fresh)
	}
	resumed := piAgentLaunch("/tmp/just-code-agent.env", "/workspace", piResumeArgs(true))
	if !strings.Contains(resumed, "exec pi --continue") {
		t.Fatalf("resumed launch = %s", resumed)
	}
}
