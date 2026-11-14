package codebox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newTestAgentVM(t *testing.T, runner Runner) *AgentVM {
	t.Helper()
	return &AgentVM{
		Config: Config{
			AgentVMTemplate: DefaultAgentVMTemplate,
			AgentVMImage:    DefaultAgentVMImage,
			AgentVMVM:       DefaultAgentVMVM,
			Username:        "opencode",
			Password:        "pw",
			PasswordSet:     true,
			WorkspaceDir:    t.TempDir(),
			APIKey:          "key",
			// Tests exercise the runtime; the P09 acknowledgement gate is
			// covered by its own test.
			GuestCredentialsAcknowledged: true,
		},
		Runner:           runner,
		Starter:          &fakeStarter{},
		StateDir:         t.TempDir(),
		KillPollInterval: time.Millisecond,
		KillMaxPolls:     3,
	}
}

func TestParseLimaList(t *testing.T) {
	out := "agent-vm-base|Stopped\nopencode-agent-vm|Running\nother|Broken\n"
	got := ParseLimaList(out)
	if len(got) != 3 {
		t.Fatalf("ParseLimaList = %v, want 3 entries", got)
	}
	if !got["opencode-agent-vm"] {
		t.Errorf("opencode-agent-vm should be running")
	}
	if got["agent-vm-base"] {
		t.Errorf("agent-vm-base is Stopped, must not read as running")
	}
	if got["other"] {
		t.Errorf("Broken state must not read as running")
	}
}

func TestLimaListFailedQueryIsError(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "limactl" && len(args) > 0 && args[0] == "list" {
			return ExecResult{ExitCode: 1, Stderr: "limactl failed"}
		}
		return ExecResult{ExitCode: 0}
	}}
	a := newTestAgentVM(t, runner)
	if _, err := a.vmExists(context.Background()); err == nil {
		t.Errorf("a failed limactl list must be an error, never \"VM absent\"")
	}
}

func TestMountsJSON(t *testing.T) {
	got := mountsJSON("/tmp/ws")
	want := `[{"location":"/tmp/ws","writable":true}]`
	if got != want {
		t.Errorf("mountsJSON = %q, want %q", got, want)
	}
}

func TestAgentVMStartCreatesClonesEditsAndLaunches(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		switch {
		case name == "limactl" && args[0] == "list":
			// Template exists, managed VM does not.
			return ExecResult{ExitCode: 0, Stdout: DefaultAgentVMTemplate + "|Stopped\n"}
		default:
			return ExecResult{ExitCode: 0}
		}
	}}
	a := newTestAgentVM(t, runner)
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !runner.hasCall("limactl clone " + DefaultAgentVMTemplate + " " + DefaultAgentVMVM) {
		t.Errorf("expected clone from template, calls: %v", runner.calls)
	}
	if !runner.hasCall("limactl edit " + DefaultAgentVMVM) {
		t.Errorf("expected mount edit after clone, calls: %v", runner.calls)
	}
	if !runner.hasCall("limactl copy") {
		t.Errorf("expected secrets env push via limactl copy, calls: %v", runner.calls)
	}
	starter := a.Starter.(*fakeStarter)
	if starter.lastName != "limactl" {
		t.Errorf("expected detached limactl launch, got %q", starter.lastName)
	}
	launch := strings.Join(starter.lastArgs, " ")
	if !strings.Contains(launch, "opencode serve") {
		t.Errorf("launch args must run opencode serve: %q", launch)
	}
	if strings.Contains(launch, "pw") || strings.Contains(launch, "key") {
		t.Errorf("secrets must never appear in argv: %q", launch)
	}
}

func TestAgentVMCreateVMRollsBackOnEditFailure(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		switch {
		case name == "limactl" && args[0] == "list":
			return ExecResult{ExitCode: 0, Stdout: DefaultAgentVMTemplate + "|Stopped\n"}
		case name == "limactl" && args[0] == "edit":
			return ExecResult{ExitCode: 1, Stderr: "edit failed"}
		default:
			return ExecResult{ExitCode: 0}
		}
	}}
	a := newTestAgentVM(t, runner)
	err := a.Start(context.Background())
	if err == nil {
		t.Fatal("expected Start to fail when the mount edit fails")
	}
	// The incomplete clone must be deleted so a retry can create it again.
	if !runner.hasCall("limactl delete " + DefaultAgentVMVM + " --force") {
		t.Errorf("expected rollback delete after edit failure, calls: %v", runner.calls)
	}
	if !strings.Contains(err.Error(), "removed") {
		t.Errorf("error should mention the rollback: %v", err)
	}
}

func TestAgentVMCleanRemovesStagingWithoutVM(t *testing.T) {
	// The VM was deleted manually: clean must still remove the staged
	// secrets env (Albert API key, HTTP password) from the host.
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "limactl" && args[0] == "list" {
			return ExecResult{ExitCode: 0, Stdout: ""}
		}
		return ExecResult{ExitCode: 0}
	}}
	a := newTestAgentVM(t, runner)
	stage := AgentVMStageDir(a.StateDir)
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "opencode.env"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Clean(context.Background()); err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stage, "opencode.env")); !os.IsNotExist(err) {
		t.Errorf("staged secrets env must be removed even without a VM, stat err: %v", err)
	}
}

func TestAgentVMStartBuildsMissingDefaultTemplate(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "limactl" && args[0] == "list" {
			return ExecResult{ExitCode: 0, Stdout: ""}
		}
		return ExecResult{ExitCode: 0}
	}}
	a := newTestAgentVM(t, runner)
	// No error: the missing default template is built rather than reported.
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start must build the missing default template: %v", err)
	}
	for _, want := range []string{
		"limactl create --name=" + DefaultAgentVMTemplate,
		"limactl start " + DefaultAgentVMTemplate,
		"limactl stop " + DefaultAgentVMTemplate,
	} {
		if !runner.hasCall(want) {
			t.Errorf("expected call %q; calls: %v", want, runner.calls)
		}
	}
}

func TestAgentVMStartKeepsUserTemplateFailClosed(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "limactl" && args[0] == "list" {
			return ExecResult{ExitCode: 0, Stdout: ""}
		}
		return ExecResult{ExitCode: 0}
	}}
	a := newTestAgentVM(t, runner)
	// A user-maintained template must never be built or replaced.
	a.Config.AgentVMTemplate = "my-team-base"
	err := a.Start(context.Background())
	if err == nil {
		t.Fatal("expected an error for a missing user template")
	}
	if !strings.Contains(err.Error(), "my-team-base") {
		t.Errorf("error must name the missing template: %v", err)
	}
	if runner.hasCall("limactl create") {
		t.Errorf("a user template must never be created; calls: %v", runner.calls)
	}
}

func TestAgentVMStartRunningDeadBackendRelaunches(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		switch {
		case name == "limactl" && args[0] == "list":
			return ExecResult{ExitCode: 0, Stdout: DefaultAgentVMVM + "|Running\n"}
		case name == "limactl" && len(args) > 2 && args[2] == "pgrep":
			// No opencode process: the stale-backend kill loop must see it exit.
			return ExecResult{ExitCode: 1}
		default:
			return ExecResult{ExitCode: 0}
		}
	}}
	a := newTestAgentVM(t, runner)
	// The health probe fails (nothing listens on the fixed endpoint), so a
	// running VM with a dead backend must be relaunched, not re-cloned.
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start with dead backend must relaunch, got: %v", err)
	}
	if runner.hasCall("limactl clone") {
		t.Errorf("a running VM must not be re-cloned")
	}
	starter := a.Starter.(*fakeStarter)
	if starter.lastName != "limactl" {
		t.Errorf("expected backend relaunch, got %q", starter.lastName)
	}
}

func TestAgentVMStop(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "limactl" && args[0] == "list" {
			return ExecResult{ExitCode: 0, Stdout: DefaultAgentVMVM + "|Running\n"}
		}
		return ExecResult{ExitCode: 0}
	}}
	a := newTestAgentVM(t, runner)
	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !runner.hasCall("limactl stop " + DefaultAgentVMVM) {
		t.Errorf("expected limactl stop, calls: %v", runner.calls)
	}
}

func TestAgentVMStopNotRunning(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "limactl" && args[0] == "list" {
			return ExecResult{ExitCode: 0, Stdout: DefaultAgentVMVM + "|Stopped\n"}
		}
		return ExecResult{ExitCode: 0}
	}}
	a := newTestAgentVM(t, runner)
	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("Stop on a stopped VM: %v", err)
	}
	if runner.hasCall("limactl stop") {
		t.Errorf("must not stop a VM that is not running")
	}
}

func TestAgentVMClean(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "limactl" && args[0] == "list" {
			return ExecResult{ExitCode: 0, Stdout: DefaultAgentVMVM + "|Running\n"}
		}
		return ExecResult{ExitCode: 0}
	}}
	a := newTestAgentVM(t, runner)
	if err := a.Clean(context.Background()); err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if !runner.hasCall("limactl delete " + DefaultAgentVMVM) {
		t.Errorf("expected limactl delete, calls: %v", runner.calls)
	}
}

func TestAgentVMDoctorMissingLimaCtl(t *testing.T) {
	// commandNotFound path is exercised in tart_test via a missing binary;
	// here the nonzero-exit path.
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		return ExecResult{ExitCode: 1}
	}}
	a := newTestAgentVM(t, runner)
	if err := a.Doctor(context.Background()); err == nil {
		t.Errorf("doctor must fail when limactl is unusable")
	}
}

func TestAgentVMDoctorMissingDefaultTemplate(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "limactl" && args[0] == "list" {
			return ExecResult{ExitCode: 0, Stdout: ""}
		}
		return ExecResult{ExitCode: 0, Stdout: "limactl version 1.0\n"}
	}}
	a := newTestAgentVM(t, runner)
	// Doctor stays read-only: it reports the pending build instead of running
	// a multi-minute provisioning side effect.
	if err := a.Doctor(context.Background()); err != nil {
		t.Fatalf("doctor must not fail on a missing default template: %v", err)
	}
	if runner.hasCall("limactl create") {
		t.Errorf("doctor must not build the template; calls: %v", runner.calls)
	}
}

func TestAgentVMDoctorKeepsUserTemplateFailClosed(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "limactl" && args[0] == "list" {
			return ExecResult{ExitCode: 0, Stdout: ""}
		}
		return ExecResult{ExitCode: 0, Stdout: "limactl version 1.0\n"}
	}}
	a := newTestAgentVM(t, runner)
	a.Config.AgentVMTemplate = "my-team-base"
	err := a.Doctor(context.Background())
	if err == nil {
		t.Fatal("expected doctor to fail when a user template is missing")
	}
	if !strings.Contains(err.Error(), "my-team-base") {
		t.Errorf("error must name the missing template: %v", err)
	}
}

func TestAgentVMWriteSecretsEnv(t *testing.T) {
	a := newTestAgentVM(t, &fakeRunner{})
	dir := t.TempDir()
	path, err := a.writeSecretsEnv(dir, map[string]string{"ALBERT_API_KEY": "key"})
	if err != nil {
		t.Fatalf("writeSecretsEnv: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "OPENCODE_SERVER_PASSWORD='pw'") {
		t.Errorf("env file must carry the quoted password: %q", content)
	}
	if !strings.Contains(content, "ALBERT_API_KEY='key'") {
		t.Errorf("env file must carry the quoted API key: %q", content)
	}
	// The TUI needs the provider definition too; in full mode this file is the
	// only place it can come from, so the config must ride along.
	if !strings.Contains(content, "OPENCODE_CONFIG_CONTENT='") {
		t.Errorf("env file must carry the OpenCode provider config: %q", content)
	}
	if !strings.Contains(content, "albert.api.etalab.gouv.fr") {
		t.Errorf("env file must carry the real provider config content: %q", content)
	}
	// Issue #178: agent-vm runs the OpenCode its template provides. The inline
	// managed config cannot disable the updater (it reads `autoupdate` only from
	// global config), so the guest environment carries the opt-out.
	if !strings.Contains(content, "OPENCODE_DISABLE_AUTOUPDATE=1") {
		t.Errorf("env file must disable the OpenCode update check: %q", content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no Unix permission bits (Go reports 0666 there); agent-vm
	// itself is rejected on Windows, so the 0600 guarantee is only asserted
	// where the runtime exists.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("env file must be 0600, got %v", info.Mode().Perm())
	}
}

func TestShellQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "'plain'"},
		{"two words", "'two words'"},
		{"it's", `'it'\''s'`},
		{"a$(rm -rf)b", `'a$(rm -rf)b'`},
		{"back`tick`", "'back`tick`'"},
		{"semi;colon", "'semi;colon'"},
	}
	for _, c := range cases {
		if got := shellQuote(c.in); got != c.want {
			t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAgentVMWriteSecretsEnvShellSafe(t *testing.T) {
	// A password with shell syntax must survive the round trip: the env file
	// is sourced by sh in the guest, so values must be single-quoted.
	a := newTestAgentVM(t, &fakeRunner{})
	a.Config.Password = "two words$(dangerous)"
	dir := t.TempDir()
	path, err := a.writeSecretsEnv(dir, map[string]string{"ALBERT_API_KEY": "key"})
	if err != nil {
		t.Fatalf("writeSecretsEnv: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "OPENCODE_SERVER_PASSWORD='two words$(dangerous)'") {
		t.Errorf("password must be single-quoted: %q", content)
	}
}

func TestAgentVMEndpoint(t *testing.T) {
	a := newTestAgentVM(t, &fakeRunner{})
	endpoint, err := a.Endpoint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "http://127.0.0.1:4096" {
		t.Errorf("Endpoint = %q, want http://127.0.0.1:4096", endpoint)
	}
}

func TestAgentVMLogPath(t *testing.T) {
	if got := AgentVMLogPath("/state"); got != filepath.Join("/state", "agent-vm.log") {
		t.Errorf("AgentVMLogPath = %q", got)
	}
}

func TestAgentVMV2UpgradeAndCustomTemplatePolicy(t *testing.T) {
	for _, managed := range []bool{true, false} {
		t.Run(fmt.Sprint(managed), func(t *testing.T) {
			updated := false
			r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
				joined := strings.Join(args, " ")
				if strings.HasSuffix(joined, "opencode --version") {
					version := "1.19.5"
					if updated {
						version = "opencode v" + OpenCodeVersion
					}
					return ExecResult{Stdout: version}
				}
				if strings.Contains(joined, "https://opencode.ai/v2/install") {
					if !strings.Contains(joined, "--version "+OpenCodeVersion) {
						t.Fatal("upgrade is not pinned")
					}
					updated = true
				}
				return ExecResult{}
			}}
			template := "user-template"
			if managed {
				template = DefaultAgentVMTemplate
			}
			a := NewAgentVM(Config{AgentVMTemplate: template})
			a.Runner = r
			err := a.ensureOpenCode(context.Background())
			if managed && (err != nil || !updated) {
				t.Fatalf("managed upgrade: %v, calls %v", err, r.calls)
			}
			if !managed && (err == nil || updated) {
				t.Fatalf("custom template must be rejected without modification: %v, calls %v", err, r.calls)
			}
		})
	}
}

func TestAgentVMRunAgentPushesSecretsAndLaunchesTUI(t *testing.T) {
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		joined := strings.Join(args, " ")
		if name == "limactl" && strings.Contains(joined, "opencode session list --standalone --format json") {
			return ExecResult{ExitCode: 0, Stdout: `[{"id":"ses_existing"}]`}
		}
		if name == "limactl" && strings.Contains(joined, "opencode --help") {
			return ExecResult{ExitCode: 0, Stdout: "--continue continue the last session"}
		}
		return ExecResult{ExitCode: 0}
	}}
	a := newTestAgentVM(t, r)
	var interactiveArgs []string
	a.Interactive = func(name string, args ...string) error {
		interactiveArgs = append([]string{name}, args...)
		return nil
	}
	if err := a.RunAgent(context.Background()); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	// Secrets travel via limactl copy of the 0600 env file, never argv.
	if !r.hasCall("copy") {
		t.Fatalf("secrets env file was not pushed; calls: %v", r.calls)
	}
	if !r.hasCall("chmod 600 /tmp/codebox-opencode.env") {
		t.Fatalf("guest env file must be chmod 600; calls: %v", r.calls)
	}
	if !r.hasCall("shell " + DefaultAgentVMVM + " sh -c set -a; . " + agentVMSecretsEnvPath + "; set +a; cd ") {
		t.Fatalf("the session probe must source the same env file as the TUI: %v", r.calls)
	}
	if !r.hasCall("shell " + DefaultAgentVMVM + " sh -c set -a; . " + agentVMSecretsEnvPath + "; set +a; opencode --help") {
		t.Fatalf("the OpenCode help probe must source the same env file as the TUI: %v", r.calls)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "pw") || strings.Contains(c, "key") {
			t.Fatalf("secret value leaked into argv: %v", r.calls)
		}
	}
	if len(interactiveArgs) == 0 {
		t.Fatal("interactive TUI step never ran")
	}
	joined := strings.Join(interactiveArgs, " ")
	if !strings.HasPrefix(joined, "limactl shell "+DefaultAgentVMVM+" sh -c ") {
		t.Fatalf("interactive argv = %q", joined)
	}
	if !strings.Contains(joined, "/tmp/codebox-opencode.env") || !strings.Contains(joined, a.Config.WorkspaceDir) || !strings.Contains(joined, "exec opencode --standalone --continue") {
		t.Fatalf("launch line must source secrets and resume OpenCode in the workspace: %q", joined)
	}
	// The workspace path is user-configurable and lands in a `sh -c` string,
	// so it must be shell-quoted rather than interpolated raw.
	if !strings.Contains(joined, "cd '"+a.Config.WorkspaceDir+"'") {
		t.Fatalf("workspace path must be shell-quoted in the launch line: %q", joined)
	}
}

func TestAgentVMRunAgentFallsBackWhenContinueIsUnsupported(t *testing.T) {
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "limactl" && strings.Contains(strings.Join(args, " "), "opencode session list --standalone --format json") {
			return ExecResult{ExitCode: 0, Stdout: `[{"id":"ses_existing"}]`}
		}
		return ExecResult{ExitCode: 0}
	}}
	a := newTestAgentVM(t, r)
	var interactiveArgs []string
	a.Interactive = func(name string, args ...string) error {
		interactiveArgs = append([]string{name}, args...)
		return nil
	}
	if err := a.RunAgent(context.Background()); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	joined := strings.Join(interactiveArgs, " ")
	if !strings.Contains(joined, "exec opencode") || strings.Contains(joined, "--continue") {
		t.Fatalf("an older OpenCode must still start a fresh session: %q", joined)
	}
}

// TestAgentVMRunAgentQuotesWorkspaceWithSpaces covers the quoted path where it
// matters: a workspace directory containing spaces and shell metacharacters.
func TestAgentVMRunAgentQuotesWorkspaceWithSpaces(t *testing.T) {
	r := &fakeRunner{}
	a := newTestAgentVM(t, r)
	a.Config.WorkspaceDir = "/tmp/my project; rm -rf /"
	var interactiveArgs []string
	a.Interactive = func(name string, args ...string) error {
		interactiveArgs = append([]string{name}, args...)
		return nil
	}
	if err := a.RunAgent(context.Background()); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	joined := strings.Join(interactiveArgs, " ")
	if !strings.Contains(joined, `cd '/tmp/my project; rm -rf /'`) {
		t.Fatalf("a workspace path with spaces/metacharacters must be single-quoted: %q", joined)
	}
}

func TestAgentVMStartFullModeSkipsServerLaunch(t *testing.T) {
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "limactl" && len(args) > 0 && args[0] == "list" {
			return ExecResult{ExitCode: 0, Stdout: DefaultAgentVMVM + "|Running\n"}
		}
		return ExecResult{ExitCode: 0}
	}}
	a := newTestAgentVM(t, r)
	a.Config.Isolation = IsolationFull
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fs := a.Starter.(*fakeStarter)
	if fs.lastArgs != nil {
		started := strings.Join(fs.lastArgs, " ")
		if strings.Contains(started, "opencode serve") {
			t.Fatalf("full mode must not launch the backend server: %v", started)
		}
	}
}

func TestAgentVMStatus(t *testing.T) {
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "limactl" && len(args) > 0 && args[0] == "list" {
			return ExecResult{ExitCode: 0, Stdout: DefaultAgentVMVM + "|Running\n"}
		}
		return ExecResult{ExitCode: 0}
	}}
	a := newTestAgentVM(t, r)
	state, err := a.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(state, "running") {
		t.Fatalf("Status = %q", state)
	}
}

func TestAgentVMStartPreflightsWorkspace(t *testing.T) {
	// The workspace is mounted read-write into the Lima VM, so Start must
	// refuse it before anything touches the VM, exactly like Tart.
	runner := &fakeRunner{}
	a := newTestAgentVM(t, runner)
	if err := os.WriteFile(filepath.Join(a.Config.WorkspaceDir, ".env"), []byte("X=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := a.Start(context.Background())
	if err == nil {
		t.Fatal("Start must refuse a workspace containing .env")
	}
	if !strings.Contains(err.Error(), "refusing to start") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("limactl ran before the workspace gate: %v", runner.calls)
	}
}

func TestAgentVMRestartPreflightsWorkspace(t *testing.T) {
	// A rejected workspace must abort restart before the destructive Clean,
	// so the VM and its persistent state survive.
	runner := &fakeRunner{}
	a := newTestAgentVM(t, runner)
	if err := os.WriteFile(filepath.Join(a.Config.WorkspaceDir, ".env"), []byte("X=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := a.Restart(context.Background())
	if err == nil {
		t.Fatal("Restart must refuse a workspace containing .env")
	}
	if !strings.Contains(err.Error(), "refusing to start") {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.hasCall("limactl delete") || runner.hasCall("limactl stop") {
		t.Fatalf("destructive command ran before the workspace gate: %v", runner.calls)
	}
}

func TestAgentVMRestartCreatesMissingWorkspace(t *testing.T) {
	// Restart preflights the workspace before the destructive Clean, but a
	// workspace that does not exist yet (default ./workspace) must be
	// created like Start does, not fail the preflight with a stat error.
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "limactl" && len(args) > 0 && args[0] == "list" {
			return ExecResult{ExitCode: 0, Stdout: DefaultAgentVMVM + "|Running\n"}
		}
		return ExecResult{ExitCode: 0}
	}}
	a := newTestAgentVM(t, r)
	a.Config.WorkspaceDir = filepath.Join(t.TempDir(), "missing", "workspace")
	// Isolation full makes Start return right after waitForAgent on the
	// already-running fake VM; the point here is the preflight, not the
	// backend lifecycle.
	a.Config.Isolation = IsolationFull
	if err := a.Restart(context.Background()); err != nil {
		t.Fatalf("Restart must create a missing workspace, got: %v", err)
	}
	if info, err := os.Stat(a.Config.WorkspaceDir); err != nil || !info.IsDir() {
		t.Fatalf("workspace was not created: %v", err)
	}
}

func TestAgentVMRestartIsNonDestructive(t *testing.T) {
	// P07: restart stops and starts the existing VM. It must never delete
	// it — the destructive rebuild is the explicit Recreate.
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "limactl" && len(args) > 0 && args[0] == "list" {
			return ExecResult{ExitCode: 0, Stdout: DefaultAgentVMVM + "|Running\n"}
		}
		return ExecResult{ExitCode: 0}
	}}
	a := newTestAgentVM(t, runner)
	a.Config.Isolation = IsolationFull
	if err := a.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if runner.hasCall("limactl delete") {
		t.Fatalf("Restart must not delete the VM: %v", runner.calls)
	}
	if !runner.hasCall("limactl stop") {
		t.Fatalf("Restart must stop the VM: %v", runner.calls)
	}
}

func TestAgentVMRecreateDeletesAndRecreates(t *testing.T) {
	runner := &fakeRunner{}
	a := newTestAgentVM(t, runner)
	if err := a.Recreate(context.Background()); err != nil {
		t.Fatalf("Recreate: %v", err)
	}
	if !runner.hasCall("limactl delete") {
		t.Fatalf("Recreate must delete the VM: %v", runner.calls)
	}
}

// TestAgentVMStartRequiresGuestCredentialsAcknowledgement pins the P09 gate
// (issue #74): agent-vm transports the credential into the guest in
// plaintext, so Start refuses without the explicit acknowledgement flag.
func TestAgentVMStartRequiresGuestCredentialsAcknowledgement(t *testing.T) {
	a := newTestAgentVM(t, &fakeRunner{})
	a.Config.GuestCredentialsAcknowledged = false
	err := a.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--acknowledge-guest-credentials") {
		t.Fatalf("Start must require the acknowledgement: %v", err)
	}
}
