package justcode

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type fakeExecer struct {
	called bool
	path   string
	argv   []string
	env    []string
}

// skipUnixGuest skips a guest-bootstrap test on Windows. These tests simulate
// the OpenCode bootstrap that runs inside a macOS/Linux guest VM; the guest
// resolver looks executables up by POSIX mode bit (0o111), which has no meaning
// on a Windows host, so the hermetic stubs are never "found" there.
func skipUnixGuest(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("guest bootstrap runs in a macOS/Linux VM and is Unix-coupled (execute-bit lookup); not a Windows behavior")
	}
}

func (f *fakeExecer) Exec(path string, argv []string, env []string) error {
	f.called = true
	f.path = path
	f.argv = argv
	f.env = env
	return nil
}

// guestTest builds a GuestConfig with hermetic paths. bins are executable stubs
// placed on the guest PATH.
func guestTest(t *testing.T, runner Runner, stdin string, bins ...string) (GuestConfig, *fakeExecer) {
	t.Helper()
	cfg, execer, _ := guestTestDir(t, runner, stdin, bins...)
	return cfg, execer
}

// guestTestDir is guestTest plus the bin directory, for tests that simulate an
// installer dropping a binary on PATH.
func guestTestDir(t *testing.T, runner Runner, stdin string, bins ...string) (GuestConfig, *fakeExecer, string) {
	t.Helper()
	home := t.TempDir()
	binDir := t.TempDir()
	for _, b := range bins {
		if err := os.WriteFile(filepath.Join(binDir, b), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	execer := &fakeExecer{}
	cfg := GuestConfig{
		Port:     "4096",
		Username: "opencode",
		MTU:      "auto",
		Stdin:    strings.NewReader(stdin),
		Stdout:   io.Discard,
		Stderr:   io.Discard,
		Runner:   runner,
		Execer:   execer,
		Chdir:    func(string) error { return nil },
		Home:     home,
		Environ:  []string{"PATH=" + binDir},
		HTTPGet:  func(string) ([]byte, error) { return []byte("echo installed\n"), nil },
	}
	return cfg, execer, binDir
}

func TestGuestPathIncludesAdminDirsAndOpencodeBin(t *testing.T) {
	got := guestPath([]string{"PATH=/usr/bin:/bin"}, "/Users/x")
	for _, want := range []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin", filepath.Join("/Users/x", ".opencode", "bin")} {
		if !strings.Contains(got, want) {
			t.Errorf("guestPath = %q, missing %q", got, want)
		}
	}
}

func TestGuestGitIdentityConfiguredAndDefault(t *testing.T) {
	// Configured values win; empty fields fall back per-field, so a
	// partially configured identity keeps the documented default for the
	// missing half only.
	name, email := guestGitIdentity(GuestConfig{GitName: "Luis", GitEmail: "luis@example.gouv.fr"})
	if name != "Luis" || email != "luis@example.gouv.fr" {
		t.Fatalf("configured identity = %q <%s>", name, email)
	}
	name, email = guestGitIdentity(GuestConfig{})
	if name != "Albert Code Agent" || email != "albert-code@noreply.etalab.gouv.fr" {
		t.Fatalf("default identity = %q <%s>", name, email)
	}
	name, email = guestGitIdentity(GuestConfig{GitName: "Luis"})
	if name != "Luis" || email != "albert-code@noreply.etalab.gouv.fr" {
		t.Fatalf("partial identity = %q <%s>", name, email)
	}
}

func TestParseDefaultInterface(t *testing.T) {
	out := "   route to: default\ndestination: default\n       mask: default\n    gateway: 192.168.64.1\n  interface: en0\n      flags: <UP,GATEWAY>\n"
	if got := parseDefaultInterface(out); got != "en0" {
		t.Fatalf("parseDefaultInterface = %q, want en0", got)
	}
	if got := parseDefaultInterface("nothing here"); got != "" {
		t.Fatalf("parseDefaultInterface = %q, want empty", got)
	}
}

func TestGuestMTUApplied(t *testing.T) {
	skipUnixGuest(t)
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "route" {
			return ExecResult{ExitCode: 0, Stdout: "  interface: en7\n"}
		}
		return ExecResult{ExitCode: 0}
	}}
	cfg, execer := guestTest(t, r, "pw\nkey\n", "opencode")
	cfg.MTU = "1400"

	if err := RunGuestBootstrap(context.Background(), cfg); err != nil {
		t.Fatalf("RunGuestBootstrap: %v", err)
	}
	if !r.hasCall("sudo -n ifconfig en7 mtu 1400") {
		t.Fatalf("MTU not applied; calls: %v", r.calls)
	}
	if !execer.called {
		t.Fatal("expected the process to be replaced by opencode serve")
	}
}

func TestGuestMTUAutoSkipsNetworkChanges(t *testing.T) {
	skipUnixGuest(t)
	// A route failure would surface if the MTU logic ran; "auto" must skip it.
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "route" {
			return ExecResult{ExitCode: 1}
		}
		return ExecResult{ExitCode: 0}
	}}
	cfg, _ := guestTest(t, r, "pw\nkey\n", "opencode")
	cfg.MTU = MTUAuto

	if err := RunGuestBootstrap(context.Background(), cfg); err != nil {
		t.Fatalf("RunGuestBootstrap: %v", err)
	}
	if r.hasCall("route") || r.hasCall("sudo") {
		t.Fatalf("auto must not touch the network; calls: %v", r.calls)
	}
}

func TestGuestMTUInvalidFailsBeforeExec(t *testing.T) {
	for _, value := range []string{"1279", "1501", "9000", "01280", "abc", "1280;id"} {
		t.Run(value, func(t *testing.T) {
			r := &fakeRunner{}
			cfg, execer := guestTest(t, r, "pw\nkey\n", "opencode")
			cfg.MTU = value
			err := RunGuestBootstrap(context.Background(), cfg)
			if err == nil || !strings.Contains(err.Error(), "TART_MTU must be") {
				t.Fatalf("expected MTU validation error, got %v", err)
			}
			if execer.called {
				t.Fatal("opencode must not start with an invalid MTU")
			}
		})
	}
}

func TestGuestMissingInterface(t *testing.T) {
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "route" {
			return ExecResult{ExitCode: 0, Stdout: ""}
		}
		return ExecResult{ExitCode: 0}
	}}
	cfg, execer := guestTest(t, r, "pw\nkey\n", "opencode")
	cfg.MTU = "1280"

	err := RunGuestBootstrap(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "cannot determine") {
		t.Fatalf("expected interface error, got %v", err)
	}
	if execer.called {
		t.Fatal("opencode must not start without a resolved interface")
	}
}

func TestGuestSudoFailure(t *testing.T) {
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "route" {
			return ExecResult{ExitCode: 0, Stdout: "  interface: en0\n"}
		}
		if name == "sudo" {
			return ExecResult{ExitCode: 1}
		}
		return ExecResult{ExitCode: 0}
	}}
	cfg, execer := guestTest(t, r, "pw\nkey\n", "opencode")
	cfg.MTU = "1280"

	err := RunGuestBootstrap(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "cannot apply TART_MTU") {
		t.Fatalf("expected sudo error, got %v", err)
	}
	if execer.called {
		t.Fatal("opencode must not start when the MTU could not be applied")
	}
}

func TestGuestEmptyPasswordPreserved(t *testing.T) {
	skipUnixGuest(t)
	cfg, execer := guestTest(t, &fakeRunner{}, "\nkey\n", "opencode")
	if err := RunGuestBootstrap(context.Background(), cfg); err != nil {
		t.Fatalf("RunGuestBootstrap: %v", err)
	}
	if !envHas(execer.env, "OPENCODE_SERVER_PASSWORD=") {
		t.Fatalf("empty password not preserved (no-auth mode); env: %v", execer.env)
	}
	if !envHas(execer.env, "ALBERT_API_KEY=key") {
		t.Fatalf("API key not propagated; env: %v", execer.env)
	}
}

func TestGuestDefaultPasswordWhenStdinEmpty(t *testing.T) {
	skipUnixGuest(t)
	cfg, execer := guestTest(t, &fakeRunner{}, "", "opencode")
	if err := RunGuestBootstrap(context.Background(), cfg); err != nil {
		t.Fatalf("RunGuestBootstrap: %v", err)
	}
	if !envHas(execer.env, "OPENCODE_SERVER_PASSWORD="+DefaultPassword) {
		t.Fatalf("default password not applied; env: %v", execer.env)
	}
}

func TestGuestExecArgvAndEnv(t *testing.T) {
	skipUnixGuest(t)
	cfg, execer := guestTest(t, &fakeRunner{}, "pw\nkey\n", "opencode")
	cfg.Port = "5000"
	cfg.Username = "albert"

	if err := RunGuestBootstrap(context.Background(), cfg); err != nil {
		t.Fatalf("RunGuestBootstrap: %v", err)
	}
	want := []string{"opencode", "serve", "--hostname", "0.0.0.0", "--port", "5000"}
	if strings.Join(execer.argv, " ") != strings.Join(want, " ") {
		t.Fatalf("argv = %v, want %v", execer.argv, want)
	}
	if !strings.HasSuffix(execer.path, "opencode") {
		t.Fatalf("exec path = %q, want the resolved opencode binary", execer.path)
	}
	if !envHas(execer.env, "OPENCODE_SERVER_USERNAME=albert") {
		t.Fatalf("username not propagated; env: %v", execer.env)
	}
	// The composed config (P10) equals the base asset semantically when the
	// overlay carries no selection: the provider block and model fields
	// survive the compose round trip.
	composed, err := ComposeConfigContent(ManagedOverlay{})
	if err != nil {
		t.Fatal(err)
	}
	if !envHas(execer.env, "OPENCODE_CONFIG_CONTENT="+composed) {
		t.Fatal("opencode config content not propagated")
	}
}

// TestGuestDisablesOpenCodeUpdateCheck covers the Tart backend path. `opencode
// serve` runs no updater today, so the assertion is about uniformity: the
// variable belongs to the launcher environment so every process derived from
// it inherits the opt-out. The full-mode TUI, which does run the updater, is
// covered separately by TestGuestSecretsEnvCarriesProviderConfig. The managed
// OPENCODE_CONFIG_CONTENT cannot carry the setting (see
// OpenCodeDisableUpdateEnv).
func TestGuestDisablesOpenCodeUpdateCheck(t *testing.T) {
	skipUnixGuest(t)
	cfg, execer := guestTest(t, &fakeRunner{}, "pw\nkey\n", "opencode")

	if err := RunGuestBootstrap(context.Background(), cfg); err != nil {
		t.Fatalf("RunGuestBootstrap: %v", err)
	}
	if !envHas(execer.env, "OPENCODE_DISABLE_AUTOUPDATE=1") {
		t.Fatalf("update check not disabled; env: %v", execer.env)
	}
}

func TestGuestVersionProbeUsesResolvedPrivateBinary(t *testing.T) {
	skipUnixGuest(t)
	oldDir, home := t.TempDir(), t.TempDir()
	private := filepath.Join(home, ".opencode", "bin")
	if err := os.MkdirAll(private, 0o755); err != nil {
		t.Fatal(err)
	}
	for dir, version := range map[string]string{oldDir: "1.18.32", private: "opencode v" + OpenCodeVersion} {
		if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte("#!/bin/sh\nprintf '%s\\n' '"+version+"'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", oldDir)
	cfg := GuestConfig{Runner: OSRunner{}, Home: home, Environ: []string{"PATH=" + oldDir}, HTTPGet: func(string) ([]byte, error) {
		t.Fatal("correct private v2 binary must not trigger installation")
		return nil, nil
	}}
	applyGuestDefaults(&cfg)
	pathEnv := guestPath(cfg.Environ, home)
	g := &guest{cfg: cfg, pathEnv: pathEnv, env: withEnv(cfg.Environ, "PATH="+pathEnv)}
	if err := g.ensureOpencode(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestGuestUpgradesLegacyOpenCodeInPlace(t *testing.T) {
	skipUnixGuest(t)
	updated := false
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "bash" {
			if !strings.Contains(strings.Join(args, " "), "--version "+OpenCodeVersion+" --no-modify-path") {
				t.Fatal("guest upgrade must pin the v2 installer")
			}
			updated = true
		}
		if filepath.Base(name) == "opencode" && len(args) == 1 && args[0] == "--version" {
			version := "1.19.5"
			if updated {
				version = "opencode v" + OpenCodeVersion
			}
			return ExecResult{Stdout: version}
		}
		return ExecResult{}
	}}
	cfg, _ := guestTest(t, r, "", "opencode")
	if err := RunGuestPrepare(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if !updated || r.hasCall("npm install") || r.hasCall("brew install") {
		t.Fatalf("existing guest binary must migrate via its private installer, not collide with a global bin: %v", r.calls)
	}
}

func TestGuestInstallsPinnedOpenCodeViaNPM(t *testing.T) {
	skipUnixGuest(t)
	// npm present, opencode absent; a pinned v2 install drops it on PATH.
	var binDir string
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "npm" {
			_ = os.WriteFile(filepath.Join(binDir, "opencode"), []byte("#!/bin/sh\n"), 0o755)
		}
		return ExecResult{ExitCode: 0}
	}}
	cfg, execer, dir := guestTestDir(t, r, "pw\nkey\n", "npm")
	binDir = dir

	if err := RunGuestBootstrap(context.Background(), cfg); err != nil {
		t.Fatalf("RunGuestBootstrap: %v", err)
	}
	if !r.hasCall("npm install -g @opencode/cli@" + OpenCodeVersion) {
		t.Fatalf("pinned v2 install not attempted; calls: %v", r.calls)
	}
	if !execer.called {
		t.Fatal("expected opencode to be exec'd after install")
	}
}

func TestGuestInstallFallsBackToInstallerScript(t *testing.T) {
	skipUnixGuest(t)
	var binDir string
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "brew" || name == "npm" {
			return ExecResult{ExitCode: 1} // both package managers fail
		}
		if name == "bash" {
			// The vendor installer succeeds and provides the binary.
			_ = os.WriteFile(filepath.Join(binDir, "opencode"), []byte("#!/bin/sh\n"), 0o755)
			return ExecResult{ExitCode: 0}
		}
		return ExecResult{ExitCode: 0}
	}}
	cfg, execer, dir := guestTestDir(t, r, "pw\nkey\n", "brew", "npm")
	binDir = dir

	if err := RunGuestBootstrap(context.Background(), cfg); err != nil {
		t.Fatalf("RunGuestBootstrap: %v", err)
	}
	if !r.hasCall("npm install -g @opencode/cli@" + OpenCodeVersion) {
		t.Fatalf("npm fallback not attempted; calls: %v", r.calls)
	}
	if !r.hasCall("bash ") {
		t.Fatalf("vendor installer not run; calls: %v", r.calls)
	}
	if !execer.called {
		t.Fatal("expected opencode to be exec'd after the installer ran")
	}
}

func TestGuestFailsWhenOpencodeUnavailable(t *testing.T) {
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		return ExecResult{ExitCode: 1} // every install path fails
	}}
	cfg, execer := guestTest(t, r, "pw\nkey\n", "brew", "npm")
	err := RunGuestBootstrap(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "installer failed") {
		t.Fatalf("expected installer failure, got %v", err)
	}
	if execer.called {
		t.Fatal("opencode must not start when it could not be installed")
	}
}

func TestWithEnvReplacesRatherThanDuplicates(t *testing.T) {
	got := withEnv([]string{"PATH=/old", "HOME=/h"}, "PATH=/new")
	joined := strings.Join(got, " ")
	if strings.Contains(joined, "PATH=/old") {
		t.Fatalf("stale PATH retained: %v", got)
	}
	if !envHas(got, "PATH=/new") || !envHas(got, "HOME=/h") {
		t.Fatalf("withEnv = %v", got)
	}
	if strings.Count(joined, "PATH=") != 1 {
		t.Fatalf("duplicate PATH entries: %v", got)
	}
}

func envHas(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

// TestGuestSecretsEnvCarriesProviderConfig pins the Tart full-mode contract:
// the TUI sources this file and nothing else supplies the provider, so the
// OpenCode config content must be in it, shell-quoted like every other value.
func TestGuestSecretsEnvCarriesProviderConfig(t *testing.T) {
	content := guestSecretsEnv("pw", "opencode", map[string]string{"ALBERT_API_KEY": "key"}, ManagedOverlay{})
	if !strings.Contains(content, "OPENCODE_CONFIG_CONTENT='") {
		t.Fatalf("secrets env must carry the provider config: %q", content)
	}
	if !strings.Contains(content, "albert.api.etalab.gouv.fr") {
		t.Fatalf("secrets env must carry the real provider config content: %q", content)
	}
	if !strings.Contains(content, "ALBERT_API_KEY='key'") || !strings.Contains(content, "OPENCODE_SERVER_PASSWORD='pw'") {
		t.Fatalf("secrets env lost a credential line: %q", content)
	}
	// The config body is JSON full of quotes, braces and colons; if it were
	// not single-quoted, sourcing the file would mangle it.
	if strings.Contains(content, `OPENCODE_CONFIG_CONTENT={`) {
		t.Fatalf("provider config must be single-quoted, not raw JSON: %q", content)
	}
	// Issue #178: the full-mode TUI sources this file and nothing else, so the
	// update-check opt-out must be here too.
	if !strings.Contains(content, "OPENCODE_DISABLE_AUTOUPDATE=1") {
		t.Fatalf("secrets env must disable the update check: %q", content)
	}
}

func TestTartAgentLaunchUsesUpgradedPrivateOpenCode(t *testing.T) {
	skipUnixGuest(t)
	home, workspace := t.TempDir(), t.TempDir()
	legacyBin := t.TempDir()
	privateBin := filepath.Join(home, ".opencode", "bin")
	for _, dir := range []string{legacyBin, privateBin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeOpenCode := func(dir, version string) {
		script := "#!/bin/sh\nprintf '%s\\n' '" + version + "'\n"
		if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeOpenCode(legacyBin, "1.18.32")
	writeOpenCode(privateBin, "opencode v"+OpenCodeVersion)

	envPath := filepath.Join(t.TempDir(), "agent.env")
	if err := os.WriteFile(envPath, []byte(guestSecretsEnv("pw", "opencode", nil, ManagedOverlay{})), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", tartAgentLaunch(envPath, workspace, false))
	cmd.Env = []string{"HOME=" + home, "PATH=" + legacyBin}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("launch upgraded guest TUI: %v: %s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "opencode v"+OpenCodeVersion {
		t.Fatalf("Tart launch selected OpenCode %q, want the private upgraded v%s binary", got, OpenCodeVersion)
	}
}
