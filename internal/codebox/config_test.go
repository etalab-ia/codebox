package codebox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func lookupFrom(m map[string]string) EnvLookup {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestLoadConfigEmptyPasswordPreserved(t *testing.T) {
	// An explicitly empty password means "no auth", not the default.
	cfg := LoadConfig(lookupFrom(map[string]string{
		"OPENCODE_SERVER_PASSWORD": "",
	}))
	if !cfg.PasswordSet {
		t.Fatal("PasswordSet = false, want true for an explicitly empty password")
	}
	if cfg.Password != "" {
		t.Fatalf("Password = %q, want empty (no auth)", cfg.Password)
	}
}

func TestLoadConfigUnsetPasswordUsesDefault(t *testing.T) {
	cfg := LoadConfig(lookupFrom(map[string]string{}))
	if cfg.PasswordSet {
		t.Fatal("PasswordSet = true, want false when unset")
	}
	if cfg.Password != DefaultPassword {
		t.Fatalf("Password = %q, want %q", cfg.Password, DefaultPassword)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg := LoadConfig(lookupFrom(map[string]string{}))
	if cfg.Username != DefaultUsername {
		t.Errorf("Username = %q, want %q", cfg.Username, DefaultUsername)
	}
	if cfg.TartImage != DefaultTartImage {
		t.Errorf("TartImage = %q, want %q", cfg.TartImage, DefaultTartImage)
	}
	if cfg.TartMTU != DefaultTartMTU {
		t.Errorf("TartMTU = %q, want %q", cfg.TartMTU, DefaultTartMTU)
	}
	if cfg.TartVM != "opencode-tahoe-base-latest" {
		t.Errorf("TartVM = %q, want opencode-tahoe-base-latest", cfg.TartVM)
	}
}

func TestLoadConfigCustom(t *testing.T) {
	cfg := LoadConfig(lookupFrom(map[string]string{
		"OPENCODE_SERVER_USERNAME": "albert",
		"OPENCODE_SERVER_PASSWORD": "s3cret",
		"TART_IMAGE":               "ghcr.io/cirruslabs/macos-sonoma-base:latest",
		"TART_MTU":                 "1400",
		"WORKSPACE_DIR":            "/tmp/proj",
		"ALBERT_API_KEY":           "key123",
	}))
	if cfg.Username != "albert" || cfg.Password != "s3cret" || !cfg.PasswordSet {
		t.Errorf("credentials wrong: %+v", cfg)
	}
	if cfg.TartVM != "opencode-sonoma-base-latest" {
		t.Errorf("TartVM = %q", cfg.TartVM)
	}
	// WorkspaceDir is normalized to an absolute path; compare against the
	// platform's own absolutization of the input so the test holds on Windows.
	wantWorkspace, _ := filepath.Abs("/tmp/proj")
	if cfg.TartMTU != "1400" || cfg.WorkspaceDir != wantWorkspace || cfg.APIKey != "key123" {
		t.Errorf("custom values wrong: %+v", cfg)
	}
}

// TestLoadConfigLegacyProjectDir covers the deprecated PROJECT_DIR fallback.
func TestLoadConfigLegacyProjectDir(t *testing.T) {
	cfg := LoadConfig(lookupFrom(map[string]string{"PROJECT_DIR": "/tmp/legacy"}))
	want, _ := filepath.Abs("/tmp/legacy")
	if cfg.WorkspaceDir != want {
		t.Errorf("WorkspaceDir = %q, want the legacy PROJECT_DIR value %q", cfg.WorkspaceDir, want)
	}
}

// TestLoadConfigWorkspaceDirWins covers precedence when both are set.
func TestLoadConfigWorkspaceDirWins(t *testing.T) {
	cfg := LoadConfig(lookupFrom(map[string]string{
		"PROJECT_DIR":   "/tmp/legacy",
		"WORKSPACE_DIR": "/tmp/current",
	}))
	want, _ := filepath.Abs("/tmp/current")
	if cfg.WorkspaceDir != want {
		t.Errorf("WorkspaceDir = %q, want WORKSPACE_DIR to win (%q)", cfg.WorkspaceDir, want)
	}
}

func TestLoadConfigStartTimeout(t *testing.T) {
	if got := LoadConfig(lookupFrom(nil)).StartTimeout; got != DefaultStartTimeout {
		t.Errorf("default StartTimeout = %v, want %v", got, DefaultStartTimeout)
	}
	cfg := LoadConfig(lookupFrom(map[string]string{"CODEBOX_START_TIMEOUT": "42"}))
	if cfg.StartTimeoutErr != nil {
		t.Fatalf("unexpected parse error: %v", cfg.StartTimeoutErr)
	}
	if cfg.StartTimeout != 42*time.Second {
		t.Errorf("StartTimeout = %v, want 42s", cfg.StartTimeout)
	}
}

// TestLoadConfigInvalidStartTimeoutIsNotFatal records the deferred-validation
// contract: an invalid CODEBOX_START_TIMEOUT is stored rather than returned,
// so commands that never start a runtime are not blocked by it.
func TestLoadConfigInvalidStartTimeoutIsNotFatal(t *testing.T) {
	for _, bad := range []string{"abc", "0", "-5", "1.5"} {
		cfg := LoadConfig(lookupFrom(map[string]string{"CODEBOX_START_TIMEOUT": bad}))
		if cfg.StartTimeoutErr == nil {
			t.Errorf("CODEBOX_START_TIMEOUT=%q: expected a deferred parse error", bad)
		}
		if cfg.StartTimeout != DefaultStartTimeout {
			t.Errorf("CODEBOX_START_TIMEOUT=%q: StartTimeout = %v, want the default", bad, cfg.StartTimeout)
		}
	}
}

func TestLoadConfigAgentVMBaseTemplateDefaults(t *testing.T) {
	cfg := LoadConfig(lookupFrom(nil))
	if cfg.AgentVMImage != DefaultAgentVMImage {
		t.Errorf("AgentVMImage = %q, want %q", cfg.AgentVMImage, DefaultAgentVMImage)
	}
	if cfg.AgentVMDiskGB != DefaultAgentVMDiskGB ||
		cfg.AgentVMMemoryGB != DefaultAgentVMMemoryGB ||
		cfg.AgentVMCPUs != DefaultAgentVMCPUs {
		t.Errorf("resource defaults = %d/%d/%d, want %d/%d/%d",
			cfg.AgentVMDiskGB, cfg.AgentVMMemoryGB, cfg.AgentVMCPUs,
			DefaultAgentVMDiskGB, DefaultAgentVMMemoryGB, DefaultAgentVMCPUs)
	}
}

func TestLoadConfigAgentVMResources(t *testing.T) {
	cfg := LoadConfig(lookupFrom(map[string]string{
		"AGENT_VM_IMAGE":     "template:debian-12",
		"AGENT_VM_DISK_GB":   "40",
		"AGENT_VM_MEMORY_GB": "8",
		"AGENT_VM_CPUS":      "6",
	}))
	if cfg.AgentVMResourcesErr != nil {
		t.Fatalf("unexpected parse error: %v", cfg.AgentVMResourcesErr)
	}
	if cfg.AgentVMImage != "template:debian-12" {
		t.Errorf("AgentVMImage = %q", cfg.AgentVMImage)
	}
	if cfg.AgentVMDiskGB != 40 || cfg.AgentVMMemoryGB != 8 || cfg.AgentVMCPUs != 6 {
		t.Errorf("resources = %d/%d/%d, want 40/8/6", cfg.AgentVMDiskGB, cfg.AgentVMMemoryGB, cfg.AgentVMCPUs)
	}
}

// TestLoadConfigInvalidAgentVMResourcesIsNotFatal records the deferred-error
// contract: a typo in a sizing variable is stored rather than returned, so
// commands that never build a template still run.
func TestLoadConfigInvalidAgentVMResourcesIsNotFatal(t *testing.T) {
	for _, bad := range []string{"abc", "0", "-5", "1.5"} {
		cfg := LoadConfig(lookupFrom(map[string]string{"AGENT_VM_DISK_GB": bad}))
		if cfg.AgentVMResourcesErr == nil {
			t.Errorf("AGENT_VM_DISK_GB=%q: expected a deferred parse error", bad)
		}
		if cfg.AgentVMDiskGB != DefaultAgentVMDiskGB {
			t.Errorf("AGENT_VM_DISK_GB=%q: DiskGB = %d, want the default", bad, cfg.AgentVMDiskGB)
		}
	}
}

// A user-supplied template must be recognisable as not-ours, so it is never
// built or replaced.
func TestAgentVMTemplateOwnership(t *testing.T) {
	a := newTestAgentVM(t, &fakeRunner{})
	if !a.ownsBaseTemplate() {
		t.Errorf("the default template name must be owned by codebox")
	}
	a.Config.AgentVMTemplate = "my-team-base"
	if a.ownsBaseTemplate() {
		t.Errorf("a custom template name must not be owned by codebox")
	}
}

func TestAgentVMBaseTemplateSpecUsesConfigResources(t *testing.T) {
	a := newTestAgentVM(t, &fakeRunner{})
	a.Config.AgentVMImage = "template:debian-13"
	a.Config.AgentVMDiskGB = 33
	a.Config.AgentVMMemoryGB = 7
	a.Config.AgentVMCPUs = 5
	spec := a.BaseTemplateSpec()
	if spec.Name != DefaultAgentVMTemplate || spec.Image != "template:debian-13" ||
		spec.DiskGB != 33 || spec.MemoryGB != 7 || spec.CPUs != 5 {
		t.Errorf("BaseTemplateSpec = %+v, want the configured resources", spec)
	}
}

func TestParseDotenv(t *testing.T) {
	m := parseDotenv("# comment\nALBERT_API_KEY=\nOPENCODE_SERVER_USERNAME=opencode\nexport OPENCODE_SERVER_PASSWORD=\"albert-dev-pass\"\nRUNTIME=tart\n")
	if m["ALBERT_API_KEY"] != "" {
		t.Errorf("empty value not preserved: %q", m["ALBERT_API_KEY"])
	}
	if m["OPENCODE_SERVER_USERNAME"] != "opencode" {
		t.Errorf("username = %q", m["OPENCODE_SERVER_USERNAME"])
	}
	if m["OPENCODE_SERVER_PASSWORD"] != "albert-dev-pass" {
		t.Errorf("quoted value not stripped: %q", m["OPENCODE_SERVER_PASSWORD"])
	}
	if m["RUNTIME"] != "tart" {
		t.Errorf("RUNTIME = %q", m["RUNTIME"])
	}
	if _, ok := m["comment"]; ok {
		t.Error("comment parsed as a key")
	}
}

func TestDotenvDirsSearchesCwdThenExecutable(t *testing.T) {
	dirs := dotenvDirs()
	if len(dirs) == 0 || dirs[0] != "." {
		t.Fatalf("dotenvDirs = %v, want the working directory first", dirs)
	}
	// The executable's directory must be searched too, so a standalone binary
	// finds a .env shipped beside it.
	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path available")
	}
	want := filepath.Dir(exe)
	found := false
	for _, d := range dirs {
		if d == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("dotenvDirs = %v, want it to include %q", dirs, want)
	}
}

func TestApplyDotenvDoesNotOverrideExisting(t *testing.T) {
	t.Setenv("EXISTING", "os-wins")
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("EXISTING=dotenv-wins\nNEWKEY=newval\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ApplyDotenv(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("EXISTING"); got != "os-wins" {
		t.Errorf("EXISTING = %q, want os-wins (os env wins)", got)
	}
	if got := os.Getenv("NEWKEY"); got != "newval" {
		t.Errorf("NEWKEY = %q, want newval", got)
	}
}

// TestWorkspaceDirSetRecordsExplicitConfiguration pins the provenance the
// launch path depends on: only an explicit WORKSPACE_DIR (or the legacy
// PROJECT_DIR) may suppress the project-root default.
func TestWorkspaceDirSetRecordsExplicitConfiguration(t *testing.T) {
	if LoadConfig(lookupFrom(nil)).WorkspaceDirSet {
		t.Fatal("an unset WORKSPACE_DIR must not be reported as explicit")
	}
	if now := LoadConfig(lookupFrom(map[string]string{"WORKSPACE_DIR": "./w"})).WorkspaceDirSet; !now {
		t.Fatal("WORKSPACE_DIR must be recorded as explicit")
	}
	if legacy := LoadConfig(lookupFrom(map[string]string{"PROJECT_DIR": "./w"})).WorkspaceDirSet; !legacy {
		t.Fatal("the legacy PROJECT_DIR must be recorded as explicit too")
	}
	// An explicitly empty value has no meaning: it must not suppress the
	// project-root default while also being unusable as a path.
	if empty := LoadConfig(lookupFrom(map[string]string{"WORKSPACE_DIR": ""})).WorkspaceDirSet; empty {
		t.Fatal("an empty WORKSPACE_DIR must not count as explicit")
	}
}

// TestLoadConfigEnvDoesNotApplyDotenvImplicitly pins the P12 change: a .env in
// the working directory is no longer read behind the user's back, and its
// presence is reported with the migration command instead of being ignored
// silently.
func TestLoadConfigEnvDoesNotApplyDotenvImplicitly(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// The key must be ABSENT, not merely empty: ApplyDotenv deliberately does
	// not override a key that is already present in the environment, so
	// t.Setenv(KEY, "") would make the "not applied" check pass even if the
	// implicit load were reinstated. That is exactly how the first version of
	// this test was vacuous.
	const probeKey = "CODEBOX_MODEL"
	if _, present := os.LookupEnv(probeKey); present {
		t.Skipf("%s is set in this environment; the absence check needs it unset", probeKey)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(probeKey+"=from-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()

	stderr := captureStderr(t, func() {
		_ = LoadConfigEnv()
	})
	if !strings.Contains(stderr, "n'a pas été lu") || !strings.Contains(stderr, "config import-env") {
		t.Fatalf("the skipped .env must be reported with the adoption command: %q", stderr)
	}
	// The note names the file by absolute path. Accept either view of it: the
	// process working directory can be the resolved form of the temp dir
	// (macOS /var -> /private/var), so comparing raw strings would fail on a
	// host where the message is still correct.
	named := strings.Contains(stderr, filepath.Join(dir, ".env"))
	if !named {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			named = strings.Contains(stderr, filepath.Join(resolved, ".env"))
		}
	}
	if !named {
		t.Fatalf("the note must name the ignored file: %q", stderr)
	}
	// The real property: the file's key did not reach the environment at all.
	if value, present := os.LookupEnv(probeKey); present {
		t.Fatalf("the .env must not be applied to the environment, got %s=%q", probeKey, value)
	}
}

func TestLoadConfigEnvTrimsAlbertAPIKey(t *testing.T) {
	t.Setenv("ALBERT_API_KEY", "  environment-secret \n")
	cfg := LoadConfigEnv()
	if cfg.APIKey != "environment-secret" {
		t.Fatalf("APIKey = %q, want the trimmed value", cfg.APIKey)
	}
	t.Setenv("ALBERT_API_KEY", " \n\t ")
	cfg = LoadConfigEnv()
	if cfg.APIKey != "" {
		t.Fatalf("a whitespace-only APIKey must be empty, got %q", cfg.APIKey)
	}
}

// TestSandboxResourceDefaultsAndParsing pins the guest sizing source: the
// built-in default, an explicit variable, and the rejection of a value the
// sandbox SDK cannot carry (its CPU count is a uint8, so 256 would wrap).
func TestSandboxResourceDefaultsAndParsing(t *testing.T) {
	cfg := LoadConfig(lookupFrom(nil))
	if cfg.CPUs != DefaultSandboxCPUs || cfg.MemoryMB != DefaultSandboxMemoryMB {
		t.Fatalf("defaults = %d cpus / %d MB", cfg.CPUs, cfg.MemoryMB)
	}
	cfg = LoadConfig(lookupFrom(map[string]string{"CODEBOX_CPUS": "8", "CODEBOX_MEMORY_MB": "8192"}))
	if cfg.CPUs != 8 || cfg.MemoryMB != 8192 || cfg.SandboxResourcesErr != nil {
		t.Fatalf("explicit sizing = %d cpus / %d MB (err %v)", cfg.CPUs, cfg.MemoryMB, cfg.SandboxResourcesErr)
	}
	cfg = LoadConfig(lookupFrom(map[string]string{"CODEBOX_MEMORY_MB": "2.5G"}))
	if cfg.MemoryMB != 2560 || cfg.SandboxResourcesErr != nil {
		t.Fatalf("readable memory sizing = %d MiB (err %v), want 2560", cfg.MemoryMB, cfg.SandboxResourcesErr)
	}
	cfg = LoadConfig(lookupFrom(map[string]string{"CODEBOX_CPUS": "256"}))
	if cfg.SandboxResourcesErr == nil {
		t.Fatal("a CPU count above the SDK's uint8 range must be reported, not wrapped")
	}
	if cfg.CPUs != DefaultSandboxCPUs {
		t.Fatalf("an invalid value must leave the default, got %d", cfg.CPUs)
	}
	for _, bad := range []string{"0", "-1", "many"} {
		if cfg := LoadConfig(lookupFrom(map[string]string{"CODEBOX_MEMORY_MB": bad})); cfg.SandboxResourcesErr == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}
