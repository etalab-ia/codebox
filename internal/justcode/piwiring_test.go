package justcode

import (
	"context"
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
	if !strings.Contains(env[PiModelsEnv], "deepseek-ai/DeepSeek-V4-Flash-0731") {
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
	// The readiness probe compares the guest's marker against this value, so it
	// has to describe the two documents actually being sent.
	if env[PiConfigFingerprintEnv] != PiConfigFingerprint(env[PiModelsEnv], env[PiSettingsEnv]) {
		t.Fatalf("%s = %q does not match the configuration it is sent with",
			PiConfigFingerprintEnv, env[PiConfigFingerprintEnv])
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
	// Both probes keep the marker that says the preparation finished and the
	// process scan that distinguishes "preparing" from "idle".
	for _, probe := range []string{pi, opencode} {
		if !strings.Contains(probe, msbToolchainMarker) || !strings.Contains(probe, msbGuestEntrypoint) {
			t.Fatalf("probe lost the preparation contract: %s", probe)
		}
	}
	// Only the OpenCode preparation writes the browser-MCP marker. Requiring it
	// from a Pi guest would wait for something no Pi preparation produces, and
	// the launch would time out instead of starting.
	if !strings.Contains(opencode, msbBrowserMCPMarker) {
		t.Fatalf("the OpenCode probe no longer waits for the browser profile: %s", opencode)
	}
	if strings.Contains(pi, msbBrowserMCPMarker) {
		t.Fatalf("the Pi probe still waits for a browser profile it never installs: %s", pi)
	}
	// Pi writes its managed configuration after the binary, so probing the
	// binary alone reports a cold guest ready before that configuration exists,
	// and a restarted guest ready on the previous one while the prep rewrites
	// it. Readiness has to require the marker carrying the configuration the
	// host is currently sending.
	if !strings.Contains(pi, msbPiConfigMarker) || !strings.Contains(pi, PiConfigFingerprintEnv) {
		t.Fatalf("the Pi probe does not wait for the Pi configuration: %s", pi)
	}
	if strings.Contains(opencode, msbPiConfigMarker) || strings.Contains(opencode, PiConfigFingerprintEnv) {
		t.Fatalf("the OpenCode probe waits for a Pi configuration: %s", opencode)
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
	if err := WriteProjectManifest(fs, legacy, ProjectManifest{Model: "albert/deepseek-v4-flash"}); err != nil {
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
// exec Pi with the model the project selected, resume only when there is
// something to resume, and keep every value quoted (the Tart share path
// contains spaces).
func TestPiAgentLaunch(t *testing.T) {
	overlay := albertOverlay()
	const ref = "albert/deepseek-ai/DeepSeek-V4-Flash-0731"

	fresh := piAgentLaunch("/tmp/just-code-agent.env", "/Volumes/My Shared Files/workspace", piModelArgs(overlay))
	if !strings.Contains(fresh, "exec pi"+` '--model' '`+ref+`'`) {
		t.Fatalf("fresh launch does not carry the selected model: %s", fresh)
	}
	if strings.Contains(fresh, "--continue") {
		t.Fatalf("a fresh launch must not resume: %s", fresh)
	}
	if !strings.Contains(fresh, `'/Volumes/My Shared Files/workspace'`) {
		t.Fatalf("workspace is not shell-quoted: %s", fresh)
	}
	if !strings.Contains(fresh, "set -a; . '/tmp/just-code-agent.env'; set +a") {
		t.Fatalf("the secrets file is not sourced: %s", fresh)
	}

	resumed := piAgentLaunch("/tmp/just-code-agent.env", "/workspace",
		append(piModelArgs(overlay), piResumeArgs(true)...))
	if !strings.Contains(resumed, `exec pi '--model' '`+ref+`' '--continue'`) {
		t.Fatalf("resumed launch = %s", resumed)
	}

	// No configured platform yields no --model: Pi then uses its own default
	// rather than being handed an empty value.
	if args := piModelArgs(ManagedOverlay{Model: ""}); args != nil {
		if len(args) != 2 || args[1] == "" {
			t.Fatalf("empty overlay produced %v", args)
		}
	}
}

// A Pi guest applies no connector, so it must not carry the browser profile's
// environment, must not select the browser image, and the desired state must
// agree with the guest the spec creates — otherwise every apply reads as an
// image change, or the readiness probe waits for a marker that never appears.
func TestPiGuestAppliesNoBrowserMCPProfile(t *testing.T) {
	pi := newTestMicrosandbox(t, &fakeMSBClient{})
	pi.cfg.Harness = HarnessPi
	pi.OpenCodeOverlay.MCPConnectors = []string{"chrome-devtools"}

	if got := pi.msbApplicableMCPs(); len(got) != 0 {
		t.Fatalf("a Pi guest applies no connector, got %v", got)
	}
	if _, ok := pi.sandboxEnv()[msbBrowserMCPFingerprintEnv]; ok {
		t.Fatalf("a Pi guest must not carry the browser fingerprint: %v", pi.sandboxEnv())
	}
	if _, ok := pi.nextStartEnv()[msbBrowserMCPFingerprintEnv]; ok {
		t.Fatalf("a Pi guest must not carry the browser fingerprint on restart either")
	}
	spec := pi.sandboxSpec(nil)
	if spec.Image != msbImage {
		t.Fatalf("Pi guest image = %q, want the base image", spec.Image)
	}
	if strings.Contains(spec.StartScript, "browser-mcps-ready") {
		t.Fatalf("the Pi start script installs the browser profile:\n%s", spec.StartScript)
	}
	if desired := pi.desiredState(false, nil, nil); desired.Image != spec.Image {
		t.Fatalf("desired image %q disagrees with the created image %q", desired.Image, spec.Image)
	}

	// OpenCode keeps the browser profile for the very same selection.
	opencode := newTestMicrosandbox(t, &fakeMSBClient{})
	opencode.OpenCodeOverlay.MCPConnectors = []string{"chrome-devtools"}
	if _, ok := opencode.sandboxEnv()[msbBrowserMCPFingerprintEnv]; !ok {
		t.Fatal("the OpenCode guest lost its browser fingerprint")
	}
	if opencode.sandboxSpec(nil).Image == msbImage {
		t.Fatal("the OpenCode browser profile no longer selects the browser image")
	}
}

// The prep script and the image are creation-fixed, so a harness change has to
// be classified as such — hashing it into the revision would only schedule a
// restart. An instance whose state predates the setting reads as OpenCode, so
// upgrading does not recreate guests that never chose a harness.
func TestHarnessChangeIsCreationFixed(t *testing.T) {
	desired := func(h Harness) DesiredState {
		return DesiredState{Instance: "p", Isolation: IsolationFull, Image: msbImage, Username: "opencode", Harness: h}
	}
	state := func(h string) *InstanceState {
		return &InstanceState{SchemaVersion: instanceStateSchemaVersion, Instance: "p", Isolation: string(IsolationFull), Image: msbImage, Harness: h}
	}

	// Legacy state (no harness) against an OpenCode project: not a difference.
	if creationFixedChanged(state(""), desired(HarnessOpenCode)) {
		t.Fatal("a guest created before harnesses existed was treated as creation-fixed")
	}
	// Legacy state against a Pi project: recreation is the only way to install
	// Pi, because the entrypoint that installs it was written at creation.
	if !creationFixedChanged(state(""), desired(HarnessPi)) {
		t.Fatal("switching to Pi was not classified as creation-fixed: the guest would restart and never install Pi")
	}
	// Recorded Pi state against an OpenCode project, and the reverse.
	if !creationFixedChanged(state(string(HarnessPi)), desired(HarnessOpenCode)) {
		t.Fatal("switching back to OpenCode was not classified as creation-fixed")
	}
	if creationFixedChanged(state(string(HarnessPi)), desired(HarnessPi)) {
		t.Fatal("an unchanged Pi project was reported as creation-fixed")
	}
	// The harness is creation-fixed but is not a revision fact: hashing it
	// would also schedule a non-destructive restart for the same change. (The
	// composed Pi configuration is a revision fact on its own account, see
	// TestConfigRevisionIncludesPiConfiguration; this asserts the harness field
	// itself, which is what a switch changes.)
	openCode := desired(HarnessOpenCode)
	pi := desired(HarnessPi)
	if openCode.ConfigRevision() != pi.ConfigRevision() {
		t.Fatal("the harness still participates in the configuration revision")
	}

	// The state file carries the harness, so the next run can compare it.
	recorded := pi.toState()
	if recorded.Harness != string(HarnessPi) {
		t.Fatalf("the applied state did not record the harness: %+v", recorded)
	}
	if appliedHarness(recorded.Harness) != HarnessPi || appliedHarness("") != HarnessOpenCode {
		t.Fatal("appliedHarness does not normalize a legacy empty value to the default")
	}
}

// The Tart launch path is the one the macOS guests use: Pi has to be installed
// and configured in the guest, and the TUI has to receive the project's model.
// Asserting through the runtime (not just the helper) is what proves the
// resolved selection reaches the agent at all.
func TestTartPiLaunchInstallsConfiguresAndSelectsTheModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	settingsPath, err := UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteUserSettings(DefaultFS, settingsPath, UserSettings{
		SchemaVersion: 1, GitName: "Albert Code Agent", GitEmail: "albert-code@noreply.etalab.gouv.fr",
	}); err != nil {
		t.Fatal(err)
	}

	r := &fakeRunner{onRun: func(string, []string) ExecResult { return ExecResult{ExitCode: 0} }}
	tt := newTestTart(t, r)
	tt.Config.Harness = HarnessPi
	tt.OpenCodeOverlay = albertOverlay()
	var interactiveArgs []string
	tt.Interactive = func(name string, args ...string) error {
		interactiveArgs = append([]string{name}, args...)
		return nil
	}
	if err := tt.RunAgent(context.Background()); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	// The pinned release is installed, and it is Pi that is installed, not
	// OpenCode: a Pi guest must not spend a download on a CLI it never runs.
	var installed, configured bool
	for _, call := range r.calls {
		// shellQuote quotes the package and the version separately, so the
		// assertion is on the two values and the install verb.
		if strings.Contains(call, "npm install --global --prefix") && strings.Contains(call, PiPackage) && strings.Contains(call, PiVersion) {
			installed = true
		}
		if strings.Contains(call, "/.pi/agent/models.json") && strings.Contains(call, "deepseek-ai/DeepSeek-V4-Flash-0731") {
			configured = true
		}
	}
	if !installed {
		t.Fatalf("the guest was not given the pinned Pi release; calls: %v", r.calls)
	}
	if !configured {
		t.Fatalf("the guest was not given the managed Pi configuration; calls: %v", r.calls)
	}
	if r.hasCall("opencode session list --standalone --format json") {
		t.Fatalf("the Pi path probed OpenCode sessions; calls: %v", r.calls)
	}
	if len(interactiveArgs) == 0 {
		t.Fatal("the TUI was never launched")
	}
	launch := strings.Join(interactiveArgs, " ")
	if !strings.Contains(launch, "exec pi") {
		t.Fatalf("the launch does not exec Pi: %s", launch)
	}
	if !strings.Contains(launch, "--model") || !strings.Contains(launch, "deepseek-ai/DeepSeek-V4-Flash-0731") {
		t.Fatalf("the selected model never reached the agent: %s", launch)
	}
}

// The prep script and the probe have to agree on where readiness is recorded,
// and the marker has to be written after the configuration it describes: a
// marker published earlier reports a guest ready before it has a provider.
func TestPiPrepPublishesTheConfigurationMarkerLast(t *testing.T) {
	models := strings.Index(piGuestPrepScript, `> "$HOME/.pi/agent/models.json"`)
	settings := strings.Index(piGuestPrepScript, `> "$HOME/.pi/agent/settings.json"`)
	marker := strings.Index(piGuestPrepScript, msbPiConfigMarker)
	if models < 0 || settings < 0 || marker < 0 {
		t.Fatalf("the Pi prep script lost a configuration step:\n%s", piGuestPrepScript)
	}
	if marker < models || marker < settings {
		t.Fatalf("the Pi prep script publishes the readiness marker before the configuration it describes")
	}
	if !strings.Contains(piGuestPrepScript, PiConfigFingerprintEnv) {
		t.Fatalf("the Pi prep script does not publish the fingerprint the probe compares")
	}
}

// The fingerprint is what ties a marker to the configuration it was written
// for: two different configurations must not share one, or a restarted guest
// reports ready on the previous configuration while the prep rewrites it.
func TestPiConfigFingerprintTracksTheConfiguration(t *testing.T) {
	base := PiConfigFingerprint(`{"a":1}`, `{"b":2}`)
	if base == "" {
		t.Fatal("fingerprint is empty")
	}
	if base != PiConfigFingerprint(`{"a":1}`, `{"b":2}`) {
		t.Fatal("the same configuration produced two fingerprints")
	}
	if base == PiConfigFingerprint(`{"a":1}`, `{"b":3}`) {
		t.Fatal("a changed setting did not change the fingerprint")
	}
	if base == PiConfigFingerprint(`{"a":9}`, `{"b":2}`) {
		t.Fatal("a changed model document did not change the fingerprint")
	}
	// The two documents must not be able to trade content for one another.
	if PiConfigFingerprint(`{"a":1}`, `{"b":2}`) == PiConfigFingerprint(`{"a":1}{"b":2}`, ``) {
		t.Fatal("the fingerprint does not separate the two documents")
	}
}

// The desired state has to carry the composed configuration, not only the
// revision machinery it feeds. A wiring gap here would compute the same
// revision for a changed catalogue, reconcile a running guest to a no-op, and
// let the launch ask Pi for a model the guest's models.json lacks.
func TestDesiredStateCarriesTheComposedPiConfiguration(t *testing.T) {
	isolateHostState(t)
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	m.OpenCodeOverlay = albertOverlay()

	// Another harness composes no Pi configuration, so an OpenCode project's
	// revision does not move.
	if got := m.desiredState(true, nil, nil).PiConfigFingerprint; got != "" {
		t.Fatalf("an OpenCode project carried a Pi fingerprint: %q", got)
	}

	m.cfg.Harness = HarnessPi
	desired := m.desiredState(true, nil, nil)
	env := piSandboxEnv(HarnessPi, m.OpenCodeOverlay)
	if desired.PiConfigFingerprint == "" {
		t.Fatal("a Pi project's desired state carries no configuration fingerprint")
	}
	if desired.PiConfigFingerprint != env[PiConfigFingerprintEnv] {
		t.Fatalf("fingerprint = %q, want the value the guest is prepared with (%q)",
			desired.PiConfigFingerprint, env[PiConfigFingerprintEnv])
	}

	// A curated model is added: the composition changes, so the guest that is
	// still running the previous document has to be refreshed and restarted.
	applied := desired.toState()
	changed := m.OpenCodeOverlay
	changed.Models = append(append([]AlbertModelConfig(nil), changed.Models...),
		AlbertModelConfig{ID: "gemma-3", ModelID: "google/gemma-3-27b-it", Name: "Gemma 3", Context: 131072})
	m.OpenCodeOverlay = changed
	plan := PlanReconcile(&applied, m.desiredState(true, nil, nil),
		ReconcileFacts{Exists: true, Running: true, Healthy: true})
	if !sameOps(plan.Ops, []ReconcileOp{OpRefreshCredentials, OpRestartVM}) {
		t.Fatalf("a recomposed Pi catalogue must refresh and restart the guest, got %v (%s)", plan.Ops, plan.Reason)
	}
}
