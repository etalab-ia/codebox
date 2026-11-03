package justcode

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeRunner scripts host-command results via an optional callback.
type fakeRunner struct {
	onRun   func(name string, args []string) ExecResult
	calls   []string
	lastEnv []string
}

func (f *fakeRunner) run(name string, args ...string) (ExecResult, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	result := ExecResult{ExitCode: 0}
	if f.onRun != nil {
		result = f.onRun(name, args)
	}
	if result.ExitCode == 0 && result.Stdout == "" && strings.HasSuffix(name+" "+strings.Join(args, " "), "opencode --version") {
		result.Stdout = "opencode v" + OpenCodeVersion + "\n"
	}
	return result, nil
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (ExecResult, error) {
	return f.run(name, args...)
}

func (f *fakeRunner) RunEnv(_ context.Context, env []string, name string, args ...string) (ExecResult, error) {
	f.lastEnv = env
	return f.run(name, args...)
}

func (f *fakeRunner) RunStdin(_ context.Context, _ io.Reader, name string, args ...string) (ExecResult, error) {
	return f.run(name, args...)
}

// hasCall reports whether any recorded call contains the given substring.
func (f *fakeRunner) hasCall(substr string) bool {
	for _, c := range f.calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

// fakeStarter records the last started command and its stdin.
type fakeStarter struct {
	lastStdin []byte
	lastName  string
	lastArgs  []string
	starts    int
}

func (f *fakeStarter) Start(stdin io.Reader, _ string, name string, args ...string) error {
	if stdin != nil {
		f.lastStdin, _ = io.ReadAll(stdin)
	}
	f.lastName = name
	f.lastArgs = args
	f.starts++
	return nil
}

func newTestTart(t *testing.T, runner Runner) *Tart {
	t.Helper()
	return &Tart{
		Config: Config{
			TartVM:       "opencode-tahoe-base-latest",
			TartImage:    DefaultTartImage,
			Username:     "opencode",
			Password:     "pw",
			PasswordSet:  true,
			TartMTU:      "1280",
			WorkspaceDir: t.TempDir(),
			APIKey:       "key",
			// Tests exercise the runtime; the P09 acknowledgement gate is
			// covered by its own test.
			GuestCredentialsAcknowledged: true,
		},
		Runner:           runner,
		Starter:          &fakeStarter{},
		StateDir:         t.TempDir(),
		SelfBinary:       writeFakeSelf(t),
		KillPollInterval: time.Millisecond,
		KillMaxPolls:     3,
	}
}

// writeFakeSelf stands in for os.Executable in tests, so staging does not copy
// the (large) test binary.
func writeFakeSelf(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "self")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho just-code\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStageGuestBinaryCopiesSelf(t *testing.T) {
	tt := newTestTart(t, &fakeRunner{})
	// Stand in for os.Executable with a known file.
	src := filepath.Join(t.TempDir(), "self")
	if err := os.WriteFile(src, []byte("#!/bin/sh\necho just-code\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tt.SelfBinary = src

	if err := tt.stageGuestBinary(); err != nil {
		t.Fatalf("stageGuestBinary: %v", err)
	}
	staged := filepath.Join(TartStageDir(tt.StateDir), guestBinaryName)
	data, err := os.ReadFile(staged)
	if err != nil {
		t.Fatalf("staged binary missing: %v", err)
	}
	if string(data) != "#!/bin/sh\necho just-code\n" {
		t.Fatalf("staged binary content = %q", data)
	}
	info, err := os.Stat(staged)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no POSIX execute bit; the staged guest binary runs in the
	// macOS guest, not on the host, so this assertion is only meaningful where
	// the permission bit exists.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("staged binary is not executable: %o", info.Mode().Perm())
	}
}

func TestParseTartList(t *testing.T) {
	out := "local opencode-tahoe-base-latest 192.168.64.2 50.0G stopped\n" +
		"local opencode-sonoma-base-latest 192.168.64.3 50.0G running\n" +
		"local other-vm 192.168.64.4 10.0G running\n" +
		"remote something 1.2.3.4 10G running\n"
	got := ParseTartList(out, "opencode-")
	want := []string{"opencode-sonoma-base-latest"}
	if len(got) != len(want) || (len(got) > 0 && got[0] != want[0]) {
		t.Fatalf("ParseTartList = %v, want %v", got, want)
	}
}

func TestStopBackendAlreadyStopped(t *testing.T) {
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if strings.Contains(strings.Join(args, " "), "pgrep") {
			return ExecResult{ExitCode: 1} // not running
		}
		return ExecResult{ExitCode: 0}
	}}
	tt := newTestTart(t, r)
	if err := tt.StopBackend(context.Background()); err != nil {
		t.Fatalf("StopBackend: %v", err)
	}
	if len(r.calls) == 0 || !strings.HasPrefix(r.calls[0], "tart exec opencode-tahoe-base-latest pkill -x opencode") {
		t.Fatalf("expected SIGTERM pkill first, got %v", r.calls)
	}
}

func TestStopBackendForceKill(t *testing.T) {
	var forceKilled bool
	var pgrepCount int
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "pkill -9"):
			forceKilled = true
			return ExecResult{ExitCode: 0}
		case strings.Contains(joined, "pgrep"):
			pgrepCount++
			if forceKilled {
				return ExecResult{ExitCode: 1} // gone after SIGKILL
			}
			return ExecResult{ExitCode: 0} // still running
		default:
			return ExecResult{ExitCode: 0}
		}
	}}
	tt := newTestTart(t, r)
	if err := tt.StopBackend(context.Background()); err != nil {
		t.Fatalf("StopBackend: %v", err)
	}
	if !forceKilled {
		t.Fatal("expected pkill -9 (SIGKILL) to be issued")
	}
	if pgrepCount != 4 { // three loop polls + one final confirmation
		t.Fatalf("pgrep called %d times, want 4", pgrepCount)
	}
}

func TestStopBackendRefusesWhenStillRunning(t *testing.T) {
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if strings.Contains(strings.Join(args, " "), "pgrep") {
			return ExecResult{ExitCode: 0} // never dies
		}
		return ExecResult{ExitCode: 0}
	}}
	tt := newTestTart(t, r)
	tt.KillMaxPolls = 2
	err := tt.StopBackend(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refusing to relaunch") {
		t.Fatalf("expected refuse error, got %v", err)
	}
}

func TestBackendArgsExcludeSecrets(t *testing.T) {
	deepseek := CuratedAlbertModels()[1].AlbertModelConfig()
	args, err := BackendArgs("opencode-tahoe-base-latest",
		"/tmp/just-code-guest", "4096", "opencode", "1280",
		ManagedOverlay{Model: "albert/deepseek-v4-flash", Models: []AlbertModelConfig{deepseek}},
		"Luis", "luis@example.gouv.fr")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "pw") || strings.Contains(joined, "key") {
		t.Fatalf("secrets leaked into argv: %v", args)
	}
	want := []string{"exec", "-i", "opencode-tahoe-base-latest",
		"/tmp/just-code-guest", GuestBootstrapCommand, "4096", "opencode", "1280", "albert/deepseek-v4-flash",
		`{"models":[{"id":"deepseek-v4-flash","modelID":"deepseek-ai/DeepSeek-V4-Flash-0731","name":"DeepSeek V4 Flash","context":131072,"output":65536,"requestBody":{"tool_choice":"auto"}}]}`,
		"Luis", "luis@example.gouv.fr"}
	if len(args) != len(want) {
		t.Fatalf("BackendArgs = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("BackendArgs[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestGuestOverlayPreservesSmallModel(t *testing.T) {
	encoded, err := encodeGuestOverlay(ManagedOverlay{SmallModel: MinistralSmallModelRef})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeGuestOverlay(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.SmallModel != MinistralSmallModelRef {
		t.Fatalf("guest small model = %q, want %q", decoded.SmallModel, MinistralSmallModelRef)
	}
	content, err := ComposeConfigContent(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, `"small_model":"`+MinistralSmallModelRef+`"`) {
		t.Fatalf("guest OpenCode config lost small_model: %s", content)
	}
}

func TestGuestMCPArgumentPositions(t *testing.T) {
	bootstrap := guestBootstrapArgs("4096", "opencode", "1280", "albert/model", `[]`, "Luis", "luis@example.test", []string{"context7", "data-gouv"})
	if got := bootstrap[GuestBootstrapModelsArgIndex]; got != `[]` {
		t.Fatalf("bootstrap model argv[%d] = %q", GuestBootstrapModelsArgIndex, got)
	}
	if got := bootstrap[GuestBootstrapMCPArgIndex]; got != "context7,data-gouv" {
		t.Fatalf("bootstrap MCP argv[%d] = %q", GuestBootstrapMCPArgIndex, got)
	}
	secrets, err := guestSecretsArgs("opencode", ManagedOverlay{Model: "albert/model"}, []string{"context7"})
	if err != nil {
		t.Fatal(err)
	}
	if got := secrets[GuestSecretsMCPArgIndex]; got != "context7" {
		t.Fatalf("guest-secrets MCP argv[%d] = %q", GuestSecretsMCPArgIndex, got)
	}
	if got := secrets[GuestSecretsModelsArgIndex]; got != `{"models":null}` {
		t.Fatalf("guest-secrets model argv[%d] = %q, want null models for an empty list", GuestSecretsModelsArgIndex, got)
	}
	if got := len(guestBootstrapArgs("4096", "opencode", "1280", "albert/model", `[]`, "Luis", "luis@example.test", nil)); got != GuestBootstrapMCPArgIndex {
		t.Fatalf("empty bootstrap argv length = %d, want legacy shape %d", got, GuestBootstrapMCPArgIndex)
	}
	emptySecrets, err := guestSecretsArgs("opencode", ManagedOverlay{Model: "albert/model"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(emptySecrets); got != GuestSecretsMCPArgIndex {
		t.Fatalf("empty guest secrets argv length = %d, want MCP index %d", got, GuestSecretsMCPArgIndex)
	}
}

func TestSecretsReader(t *testing.T) {
	data, err := io.ReadAll(SecretsReader("pw", "key"))
	if err != nil {
		t.Fatalf("SecretsReader: %v", err)
	}
	if string(data) != "pw\nkey\n" {
		t.Fatalf("SecretsReader = %q, want password then key on stdin", data)
	}
}

func TestLaunchBackendCopiesGuestBinaryAndStreamsSecrets(t *testing.T) {
	r := &fakeRunner{}
	tt := newTestTart(t, r)
	qwen := CuratedAlbertModels()[0].AlbertModelConfig()
	tt.OpenCodeOverlay.Models = []AlbertModelConfig{qwen}
	if err := tt.launchBackend(context.Background()); err != nil {
		t.Fatalf("launchBackend: %v", err)
	}

	// The payload must be copied to guest-local disk and made executable before
	// it is run: the share itself is not reliably executable.
	if !r.hasCall("/bin/cp " + guestShareBinary + " " + guestLocalBinary) {
		t.Fatalf("expected a guest cp from the share to local disk; calls: %v", r.calls)
	}
	if !r.hasCall("/bin/chmod 755 " + guestLocalBinary) {
		t.Fatalf("expected a guest chmod 755; calls: %v", r.calls)
	}
	staged := filepath.Join(TartStageDir(tt.StateDir), guestBinaryName)
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("binary was not staged into the share: %v", err)
	}

	fs := tt.Starter.(*fakeStarter)
	if got := string(fs.lastStdin); got != "pw\n{\"ALBERT_API_KEY\":\"key\"}\n" {
		t.Fatalf("stdin = %q, want password and key on stdin", got)
	}
	if !strings.HasPrefix(strings.Join(fs.lastArgs, " "), "exec -i opencode-tahoe-base-latest "+guestLocalBinary+" "+GuestBootstrapCommand) {
		t.Fatalf("unexpected bootstrap argv: %v", fs.lastArgs)
	}
	if strings.Contains(strings.Join(fs.lastArgs, " "), "pw") ||
		strings.Contains(strings.Join(fs.lastArgs, " "), "key") {
		t.Fatalf("secrets leaked into argv: %v", fs.lastArgs)
	}
	qwenJSON := `{"models":[{"id":"qwen3-coder","modelID":"Qwen/Qwen3-Coder-30B-A3B-Instruct","name":"Qwen3 Coder","context":262144}]}`
	if !strings.Contains(strings.Join(fs.lastArgs, " "), qwenJSON) {
		t.Fatalf("bootstrap argv omitted the selected model config: %v", fs.lastArgs)
	}
}

func TestTartRestartPreflightsWorkspace(t *testing.T) {
	// A rejected workspace must abort restart before the destructive Clean,
	// so the VM and its persistent state survive.
	runner := &fakeRunner{}
	tt := &Tart{
		Config: Config{
			APIKey:                       "key",
			WorkspaceDir:                 t.TempDir(),
			Username:                     "opencode",
			Password:                     "pw",
			TartVM:                       "opencode-test",
			TartMTU:                      DefaultTartMTU,
			GuestCredentialsAcknowledged: true,
		},
		Runner:           runner,
		Starter:          &fakeStarter{},
		StateDir:         t.TempDir(),
		KillPollInterval: time.Millisecond,
		KillMaxPolls:     1,
	}
	if err := os.WriteFile(filepath.Join(tt.Config.WorkspaceDir, ".env"), []byte("X=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := tt.Restart(context.Background())
	if err == nil {
		t.Fatal("Restart must refuse a workspace containing .env")
	}
	if !strings.Contains(err.Error(), "refusing to start") {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.hasCall("tart delete") || runner.hasCall("tart stop") {
		t.Fatalf("destructive command ran before the workspace gate: %v", runner.calls)
	}
}

func TestTartRestartRejectsBadConfigBeforeClean(t *testing.T) {
	// A configuration error (missing API key, invalid MTU) must abort
	// restart before the destructive Clean: the VM and its persistent
	// state survive, instead of being deleted and then failing to
	// recreate in Start.
	runner := &fakeRunner{}
	tt := newTestTart(t, runner)
	tt.Config.APIKey = ""
	// The credential store is not what this test exercises: without this,
	// hosts lacking a native store (no secret-tool) would see an unrelated
	// store-unreachable error instead of the missing-key message.
	tt.CredentialRead = func(context.Context, CredentialKind, string) (string, string, error) {
		return "", "", ErrCredentialNotFound
	}
	if err := tt.Restart(context.Background()); err == nil {
		t.Fatal("Restart must refuse a missing ALBERT_API_KEY")
	} else if !strings.Contains(err.Error(), "ALBERT_API_KEY") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("command ran despite the configuration error: %v", runner.calls)
	}
}

func TestTartRestartCreatesMissingWorkspace(t *testing.T) {
	// Restart preflights the workspace before the destructive Clean, but a
	// workspace that does not exist yet (default ./workspace) must be
	// created like Start does, not fail the preflight with a stat error.
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		joined := strings.Join(args, " ")
		if name == "tart" && strings.HasPrefix(joined, "list") {
			return ExecResult{ExitCode: 0, Stdout: "local opencode-tahoe-base-latest 1.2.3.4 50G running\n"}
		}
		return ExecResult{ExitCode: 0}
	}}
	tt := newTestTart(t, r)
	tt.Config.WorkspaceDir = filepath.Join(t.TempDir(), "missing", "workspace")
	// Isolation full makes Start return right after waitForAgent on the
	// already-running fake VM; the point here is the preflight, not the
	// backend lifecycle.
	tt.Config.Isolation = IsolationFull
	if err := tt.Restart(context.Background()); err != nil {
		t.Fatalf("Restart must create a missing workspace, got: %v", err)
	}
	if info, err := os.Stat(tt.Config.WorkspaceDir); err != nil || !info.IsDir() {
		t.Fatalf("workspace was not created: %v", err)
	}
}

func TestTartRunAgentPushesSecretsOnStdinOnly(t *testing.T) {
	// The host identity (P11) is read from the real user settings path;
	// write a disposable one so the exact-argv assertions below observe
	// the values that would travel to the guest.
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
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		joined := strings.Join(args, " ")
		if name == "tart" && strings.Contains(joined, "opencode session list --standalone --format json") {
			return ExecResult{ExitCode: 0, Stdout: `[{"id":"ses_existing"}]`}
		}
		if name == "tart" && strings.Contains(joined, "opencode --help") {
			return ExecResult{ExitCode: 0, Stdout: "--continue continue the last session"}
		}
		return ExecResult{ExitCode: 0}
	}}
	tt := newTestTart(t, r)
	gemma := CuratedAlbertModels()[3].AlbertModelConfig()
	tt.OpenCodeOverlay.Models = []AlbertModelConfig{gemma}
	var interactiveArgs []string
	tt.Interactive = func(name string, args ...string) error {
		interactiveArgs = append([]string{name}, args...)
		return nil
	}
	if err := tt.RunAgent(context.Background()); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	// Secrets push: tart exec -i with the staged binary and the hidden
	// secrets subcommand, secrets on stdin only.
	if !r.hasCall("exec -i opencode-tahoe-base-latest " + guestLocalBinary + " " + GuestSecretsCommand) {
		t.Fatalf("secrets push missing; calls: %v", r.calls)
	}
	// The model definitions travel as non-secret JSON argv alongside the
	// default model; the credentials themselves remain on stdin.
	gemmaJSON := `{"models":[{"id":"gemma-4","modelID":"google/gemma-4-31B-it","name":"Gemma 4","context":262144,"vision":true}]}`
	if !r.hasCall("exec -i opencode-tahoe-base-latest " + guestLocalBinary + " " + GuestSecretsCommand + " opencode albert/qwen3-coder " + gemmaJSON) {
		t.Fatalf("secrets argv must include the selected models; calls: %v", r.calls)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "pw") || strings.Contains(c, "key") {
			t.Fatalf("secret value leaked into argv: %v", r.calls)
		}
	}
	// Prepare step runs the staged binary's prepare subcommand.
	if !r.hasCall("exec opencode-tahoe-base-latest " + guestLocalBinary + " " + GuestPrepareCommand) {
		t.Fatalf("prepare step missing; calls: %v", r.calls)
	}
	// Exact argv for the prepare subcommand: __guest-prepare <username>
	// <gitName> <gitEmail>. The consumer reads Username at 1, GitName at
	// 2, GitEmail at 3; a drift puts the email in user.name.
	if !r.hasCall("exec opencode-tahoe-base-latest " + guestLocalBinary + " " + GuestPrepareCommand + " opencode Albert Code Agent albert-code@noreply.etalab.gouv.fr") {
		t.Fatalf("prepare argv must be __guest-prepare <username> <gitName> <gitEmail>; calls: %v", r.calls)
	}
	if !r.hasCall("exec opencode-tahoe-base-latest /bin/zsh -lc set -a; . " + shellQuote(guestSecretsEnvPath) + "; set +a; cd '" + guestWorkspaceDir + "' && opencode session list --standalone --format json") {
		t.Fatalf("the session-list probe must use the same login-shell PATH as the TUI: %v", r.calls)
	}
	if !r.hasCall("exec opencode-tahoe-base-latest /bin/zsh -lc set -a; . " + shellQuote(guestSecretsEnvPath) + "; set +a; opencode --help") {
		t.Fatalf("the OpenCode help probe must use the TUI's environment: %v", r.calls)
	}
	// Interactive TUI: tart exec -it, zsh login shell, launch line sources
	// the secrets file and resumes the latest OpenCode session in the workspace.
	if len(interactiveArgs) == 0 {
		t.Fatal("interactive TUI step never ran")
	}
	joined := strings.Join(interactiveArgs, " ")
	if !strings.HasPrefix(joined, "tart exec -it opencode-tahoe-base-latest /bin/zsh -lc ") {
		t.Fatalf("interactive argv = %q", joined)
	}
	if !strings.Contains(joined, guestSecretsEnvPath) || !strings.Contains(joined, guestWorkspaceDir) || !strings.Contains(joined, "exec opencode --standalone --continue") {
		t.Fatalf("launch line must source secrets and resume OpenCode in the workspace: %q", joined)
	}
	if strings.Contains(joined, "pw") || strings.Contains(joined, "key") {
		t.Fatalf("secrets leaked into the interactive argv: %q", joined)
	}
}

func TestTartStartRestartsHealthyBackendWhenOverlayChanges(t *testing.T) {
	previousProbe := tartHealthProbeFn
	tartHealthProbeFn = func(context.Context, *http.Client, string, string, string) HealthProbe {
		return HealthProbe{Healthy: true}
	}
	t.Cleanup(func() { tartHealthProbeFn = previousProbe })

	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		joined := strings.Join(args, " ")
		switch {
		case name == "tart" && len(args) > 0 && args[0] == "list":
			return ExecResult{Stdout: "local opencode-test 127.0.0.1 50G running\n"}
		case strings.Contains(joined, "ip --wait"):
			return ExecResult{Stdout: "127.0.0.1"}
		case strings.Contains(joined, "pgrep -x opencode"):
			return ExecResult{ExitCode: 1}
		default:
			return ExecResult{ExitCode: 0}
		}
	}}
	starter := &fakeStarter{}
	tart := &Tart{
		Config: Config{
			TartVM:                       "opencode-test",
			Username:                     "opencode",
			Password:                     "pw",
			PasswordSet:                  true,
			APIKey:                       "key",
			TartMTU:                      DefaultTartMTU,
			WorkspaceDir:                 t.TempDir(),
			GuestCredentialsAcknowledged: true,
		},
		Runner:            runner,
		Starter:           starter,
		StateDir:          t.TempDir(),
		SelfBinary:        writeFakeSelf(t),
		AgentWaitPolls:    1,
		AgentWaitInterval: time.Nanosecond,
		KillMaxPolls:      1,
	}
	choices := CuratedAlbertModels()
	tart.OpenCodeOverlay = ManagedOverlay{
		Model:  choices[0].Ref(),
		Models: []AlbertModelConfig{choices[0].AlbertModelConfig()},
	}

	if err := tart.Start(context.Background()); err != nil {
		t.Fatalf("first Tart start: %v", err)
	}
	if starter.starts != 1 {
		t.Fatalf("first healthy VM should restart its untracked backend, starts=%d", starter.starts)
	}
	if err := tart.Start(context.Background()); err != nil {
		t.Fatalf("unchanged Tart start: %v", err)
	}
	if starter.starts != 1 {
		t.Fatalf("unchanged overlay restarted the backend, starts=%d", starter.starts)
	}
	tart.OpenCodeOverlay.Models = append(tart.OpenCodeOverlay.Models, choices[2].AlbertModelConfig())
	if err := tart.Start(context.Background()); err != nil {
		t.Fatalf("changed Tart start: %v", err)
	}
	if starter.starts != 2 {
		t.Fatalf("changed model list did not restart the backend, starts=%d", starter.starts)
	}
	if !strings.Contains(strings.Join(starter.lastArgs, " "), `gpt-oss-120b`) {
		t.Fatalf("restarted backend args omitted the newly enabled model: %v", starter.lastArgs)
	}
}

// TestTartAgentLaunchQuotesPaths pins the fix for the workspace share path:
// it contains spaces, and the line runs under `zsh -lc`, so an unquoted `cd`
// would receive multiple arguments and never reach `exec opencode --standalone --continue`.
func TestTartAgentLaunchQuotesPaths(t *testing.T) {
	line := tartAgentLaunch(guestSecretsEnvPath, guestWorkspaceDir, true)
	if !strings.Contains(line, "cd '"+guestWorkspaceDir+"'") {
		t.Fatalf("workspace path must be shell-quoted for the spaced share path: %q", line)
	}
	if !strings.Contains(line, ". '"+guestSecretsEnvPath+"'") {
		t.Fatalf("secrets path must be shell-quoted: %q", line)
	}
	if !strings.Contains(line, "exec opencode --standalone --continue") {
		t.Fatalf("Tart full mode must resume the latest session: %q", line)
	}
	fresh := tartAgentLaunch(guestSecretsEnvPath, guestWorkspaceDir, false)
	if !strings.Contains(fresh, "exec opencode") || strings.Contains(fresh, "--continue") {
		t.Fatalf("unsupported OpenCode should still launch a fresh TUI: %q", fresh)
	}
	// The share path really does contain spaces; guard the premise.
	if !strings.Contains(guestWorkspaceDir, " ") {
		t.Fatalf("guestWorkspaceDir no longer contains a space (%q); the quoting test is moot", guestWorkspaceDir)
	}
}

func TestTartStartFullModeSkipsServerLaunch(t *testing.T) {
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		joined := strings.Join(args, " ")
		if name == "tart" && strings.HasPrefix(joined, "list") {
			return ExecResult{ExitCode: 0, Stdout: "local opencode-tahoe-base-latest 1.2.3.4 50G running\n"}
		}
		if strings.Contains(joined, "exec "+writeFakeSelf(t)) {
			return ExecResult{ExitCode: 0}
		}
		return ExecResult{ExitCode: 0}
	}}
	tt := newTestTart(t, r)
	tt.Config.Isolation = IsolationFull
	if err := tt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fs := tt.Starter.(*fakeStarter)
	if fs.lastArgs != nil {
		started := strings.Join(fs.lastArgs, " ")
		if strings.Contains(started, GuestBootstrapCommand) {
			t.Fatalf("full mode must not launch the backend server: %v", started)
		}
	}
}

func TestTartRestartIsNonDestructive(t *testing.T) {
	// P07: restart stops and starts the existing VM. It must never delete
	// it — the destructive rebuild is the explicit Recreate.
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		switch {
		case name == "tart" && args[0] == "list":
			return ExecResult{ExitCode: 0, Stdout: "local  opencode-tahoe-base-latest  running\n"}
		case name == "tart" && args[0] == "exec" && args[2] == "pgrep":
			return ExecResult{ExitCode: 1} // no stale backend process
		}
		return ExecResult{ExitCode: 0}
	}}
	tt := newTestTart(t, runner)
	tt.Config.Isolation = IsolationFull
	if err := tt.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if runner.hasCall("tart delete") {
		t.Fatalf("Restart must not delete the VM: %v", runner.calls)
	}
	if !runner.hasCall("tart stop") {
		t.Fatalf("Restart must stop the VM: %v", runner.calls)
	}
}

func TestTartRecreateDeletesAndRecreates(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		switch {
		case name == "tart" && args[0] == "list":
			return ExecResult{ExitCode: 0, Stdout: "local  opencode-tahoe-base-latest  running\n"}
		case name == "tart" && args[0] == "exec" && args[2] == "pgrep":
			return ExecResult{ExitCode: 1} // no stale backend process
		}
		return ExecResult{ExitCode: 0}
	}}
	tt := newTestTart(t, runner)
	tt.Config.Isolation = IsolationFull
	if err := tt.Recreate(context.Background()); err != nil {
		t.Fatalf("Recreate: %v", err)
	}
	if !runner.hasCall("tart delete") {
		t.Fatalf("Recreate must delete the VM: %v", runner.calls)
	}
}
func TestTartIsRunningScopedToProjectVM(t *testing.T) {
	// Un autre projet a une VM running ; ce projet n'en a pas : IsRunning
	// doit répondre false (portée à t.VMName(), pas « toute VM gérée »).
	other := "opencode-justcode-other"
	list := "local " + other + " 1.2.3.4 50G running\n"
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "tart" && len(args) > 0 && args[0] == "list" {
			return ExecResult{ExitCode: 0, Stdout: list}
		}
		return ExecResult{ExitCode: 0}
	}}
	tt := newTestTart(t, r)
	tt.Instance = "my-proj"
	if got, err := tt.IsRunning(context.Background()); err != nil || got {
		t.Fatalf("IsRunning = %v, %v; want false (une VM d'un autre projet ne compte pas)", got, err)
	}

	// La VM de ce projet est running : true.
	list = "local " + tt.VMName() + " 1.2.3.4 50G running\n"
	r2 := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "tart" && len(args) > 0 && args[0] == "list" {
			return ExecResult{ExitCode: 0, Stdout: list}
		}
		return ExecResult{ExitCode: 0}
	}}
	tt2 := newTestTart(t, r2)
	tt2.Instance = "my-proj"
	if got, err := tt2.IsRunning(context.Background()); err != nil || !got {
		t.Fatalf("IsRunning = %v, %v; want true (la VM du projet est running)", got, err)
	}
}

// TestTartStartRequiresGuestCredentialsAcknowledgement pins the P09 gate
// (issue #74): Tart transports the credential into the guest in plaintext,
// so Start refuses without the explicit acknowledgement flag.
func TestTartStartRequiresGuestCredentialsAcknowledgement(t *testing.T) {
	tt := newTestTart(t, &fakeRunner{})
	tt.Config.GuestCredentialsAcknowledged = false
	err := tt.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--acknowledge-guest-credentials") {
		t.Fatalf("Start must require the acknowledgement: %v", err)
	}
}

// TestTartAcceptsAMistralOnlyConfiguration pins Codex's finding on #287:
// validateConfig used to require an Albert credential even when the host had
// configured only another platform.
func TestTartAcceptsAMistralOnlyConfiguration(t *testing.T) {
	tt := newTestTart(t, &fakeRunner{})
	tt.Config.APIKey = ""
	providers, err := ResolveProviders([]ProviderSettings{{Kind: ProviderMistral}})
	if err != nil {
		t.Fatal(err)
	}
	tt.Config.Providers = providers
	tt.CredentialRead = func(_ context.Context, kind CredentialKind, _ string) (string, string, error) {
		if kind == CredentialMistral {
			return "mistral-key", "native", nil
		}
		return "", "", ErrCredentialNotFound
	}
	if err := tt.validateConfig(context.Background()); err != nil {
		t.Fatalf("a Mistral-only configuration must validate: %v", err)
	}
}

func TestTartReportsTheMissingPlatformKey(t *testing.T) {
	tt := newTestTart(t, &fakeRunner{})
	tt.Config.APIKey = ""
	providers, err := ResolveProviders([]ProviderSettings{{Kind: ProviderMistral}})
	if err != nil {
		t.Fatal(err)
	}
	tt.Config.Providers = providers
	tt.CredentialRead = func(context.Context, CredentialKind, string) (string, string, error) {
		return "", "", ErrCredentialNotFound
	}
	err = tt.validateConfig(context.Background())
	if err == nil || !strings.Contains(err.Error(), "mistral") {
		t.Fatalf("the missing Mistral key must be named: %v", err)
	}
}
