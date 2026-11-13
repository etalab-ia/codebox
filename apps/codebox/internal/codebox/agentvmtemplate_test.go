package codebox

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBaseTemplateCreateArgs(t *testing.T) {
	got := baseTemplateCreateArgs(BaseTemplateSpec{
		Name: "agent-vm-base", Image: "template:debian-13",
		DiskGB: 20, MemoryGB: 4, CPUs: 2,
	})
	want := []string{
		"create",
		"--name=agent-vm-base",
		"template:debian-13",
		"--set", ".mounts=[]",
		"--disk=20",
		"--memory=4",
		"--cpus=2",
		"--tty=false",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("baseTemplateCreateArgs =\n  %v\nwant\n  %v", got, want)
	}
}

// The base template must be created mountless: a workspace mount baked in here
// would leak into every VM cloned from it.
func TestBaseTemplateCreateArgsHasNoMounts(t *testing.T) {
	got := strings.Join(baseTemplateCreateArgs(BaseTemplateSpec{
		Name: "agent-vm-base", Image: "template:debian-13", DiskGB: 20, MemoryGB: 4, CPUs: 2,
	}), " ")
	if !strings.Contains(got, "--set .mounts=[]") {
		t.Errorf("template must be created with empty mounts, got %q", got)
	}
	// --set consumes the next argv element, so a bare mount value would be a
	// sign the flag was dropped.
	if strings.Contains(got, "writable") {
		t.Errorf("template creation must not carry a workspace mount: %q", got)
	}
}

func TestBuildBaseTemplateLifecycle(t *testing.T) {
	runner := &fakeRunner{}
	var out strings.Builder
	spec := BaseTemplateSpec{Name: "agent-vm-base", Image: "template:debian-13", DiskGB: 20, MemoryGB: 4, CPUs: 2}
	if err := BuildBaseTemplate(context.Background(), runner, spec, filepath.Join(t.TempDir(), "ready"), "echo prep\n", &out); err != nil {
		t.Fatalf("BuildBaseTemplate: %v", err)
	}
	order := []string{
		"limactl delete agent-vm-base --force",
		"limactl create --name=agent-vm-base",
		"limactl start agent-vm-base",
		"limactl shell agent-vm-base sh -s",
		"limactl stop agent-vm-base",
	}
	last := -1
	for _, want := range order {
		idx := -1
		for i, c := range runner.calls {
			if strings.Contains(c, want) {
				idx = i
				break
			}
		}
		if idx == -1 {
			t.Fatalf("missing call %q; calls: %v", want, runner.calls)
		}
		if idx < last {
			t.Errorf("call %q ran out of order; calls: %v", want, runner.calls)
		}
		last = idx
	}
}

// A failed provisioning must not leave a half-built template behind: the next
// start would treat it as valid and clone an image with no OpenCode in it.
func TestBuildBaseTemplateRollsBackOnProvisionFailure(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "limactl" && len(args) > 2 && args[0] == "shell" && args[len(args)-1] == "-s" {
			return ExecResult{ExitCode: 1, Stderr: "apk not found"}
		}
		return ExecResult{ExitCode: 0}
	}}
	var out strings.Builder
	spec := BaseTemplateSpec{Name: "agent-vm-base", Image: "template:debian-13", DiskGB: 20, MemoryGB: 4, CPUs: 2}
	err := BuildBaseTemplate(context.Background(), runner, spec, filepath.Join(t.TempDir(), "ready"), "exit 1\n", &out)
	if err == nil {
		t.Fatal("expected provisioning failure to be reported")
	}
	// Two deletes: the pre-clean and the rollback.
	deletes := 0
	for _, c := range runner.calls {
		if strings.Contains(c, "limactl delete agent-vm-base --force") {
			deletes++
		}
	}
	if deletes < 2 {
		t.Errorf("expected a rollback delete after provisioning failure, got %d delete(s): %v", deletes, runner.calls)
	}
	if strings.Contains(strings.Join(runner.calls, "\n"), "limactl stop") {
		t.Errorf("a failed build must not stop the template; calls: %v", runner.calls)
	}
}

// The provisioning script is piped on stdin, so it must never reach argv:
// argv is visible in the process table.
func TestBuildBaseTemplatePipesScriptOnStdin(t *testing.T) {
	runner := &recordingStdinRunner{}
	var out strings.Builder
	spec := BaseTemplateSpec{Name: "agent-vm-base", Image: "template:debian-13", DiskGB: 20, MemoryGB: 4, CPUs: 2}
	if err := BuildBaseTemplate(context.Background(), runner, spec, filepath.Join(t.TempDir(), "ready"), "SECRET_MARKER\n", &out); err != nil {
		t.Fatalf("BuildBaseTemplate: %v", err)
	}
	for _, c := range runner.calls {
		if strings.Contains(c, "SECRET_MARKER") {
			t.Fatalf("provisioning script leaked into argv: %q", c)
		}
	}
	if !strings.Contains(runner.stdin, "SECRET_MARKER") {
		t.Errorf("provisioning script must be piped on stdin, got %q", runner.stdin)
	}
}

// recordingStdinRunner captures both argv and the stdin payload.
type recordingStdinRunner struct {
	fakeRunner
	stdin string
}

func (r *recordingStdinRunner) RunStdin(_ context.Context, stdin io.Reader, name string, args ...string) (ExecResult, error) {
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		r.stdin = string(b)
	}
	return r.fakeRunner.run(name, args...)
}

// `limactl delete` already exits 0 for an instance it does not know about, so
// a nonzero exit is never "the template was absent": it is a real failure that
// must propagate, or Start will accept a broken template based on its name.
func TestDeleteBaseTemplatePropagatesFailure(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		return ExecResult{ExitCode: 1, Stderr: "directory exists but its lima.yaml could not be read; it was NOT deleted"}
	}}
	if err := DeleteBaseTemplate(context.Background(), runner, "agent-vm-base"); err == nil {
		t.Error("a nonzero limactl delete exit must be an error, not silent success")
	}
}

func TestDeleteBaseTemplateAcceptsAbsentInstance(t *testing.T) {
	// The real behaviour: absent instance, exit 0, warning on stderr.
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		return ExecResult{ExitCode: 0, Stderr: "Ignoring non-existent instance \"agent-vm-base\""}
	}}
	if err := DeleteBaseTemplate(context.Background(), runner, "agent-vm-base"); err != nil {
		t.Errorf("an absent instance is a clean no-op: %v", err)
	}
}

// The marker is written only after every step succeeds; it is what separates a
// finished template from one abandoned mid-build.
func TestBuildBaseTemplateWritesMarkerLast(t *testing.T) {
	runner := &fakeRunner{}
	marker := filepath.Join(t.TempDir(), "ready")
	var out strings.Builder
	spec := BaseTemplateSpec{Name: "agent-vm-base", Image: "template:debian-13", DiskGB: 20, MemoryGB: 4, CPUs: 2}
	if err := BuildBaseTemplate(context.Background(), runner, spec, marker, "echo prep\n", &out); err != nil {
		t.Fatalf("BuildBaseTemplate: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("a successful build must write the completion marker: %v", err)
	}
}

func TestBuildBaseTemplateDoesNotWriteMarkerOnFailure(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "limactl" && args[0] == "shell" {
			return ExecResult{ExitCode: 1, Stderr: "provisioning failed"}
		}
		return ExecResult{ExitCode: 0}
	}}
	marker := filepath.Join(t.TempDir(), "ready")
	var out strings.Builder
	spec := BaseTemplateSpec{Name: "agent-vm-base", Image: "template:debian-13", DiskGB: 20, MemoryGB: 4, CPUs: 2}
	if err := BuildBaseTemplate(context.Background(), runner, spec, marker, "exit 1\n", &out); err == nil {
		t.Fatal("expected provisioning failure")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("a failed build must not leave a completion marker")
	}
}

// An interrupted build cannot run its own rollback, so the instance survives
// with the name intact. The marker is the only evidence that it is finished.
func TestAgentVMRebuildsUnmarkedOwnedTemplate(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "limactl" && args[0] == "list" {
			// Instance exists (as after `limactl create`), but the build never
			// completed: no marker was written.
			return ExecResult{ExitCode: 0, Stdout: DefaultAgentVMTemplate + "|Stopped\n"}
		}
		return ExecResult{ExitCode: 0}
	}}
	a := newTestAgentVM(t, runner)
	if a.ownedTemplateUsable() {
		t.Fatal("an unmarked template must not be considered usable")
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start must rebuild an unmarked template: %v", err)
	}
	if !runner.hasCall("limactl create --name=" + DefaultAgentVMTemplate) {
		t.Errorf("expected a rebuild; calls: %v", runner.calls)
	}
}

func TestAgentVMOwnedTemplateUsableWithMarker(t *testing.T) {
	a := newTestAgentVM(t, &fakeRunner{})
	marker := a.templateMarkerPath()
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte(DefaultAgentVMTemplate+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !a.ownedTemplateUsable() {
		t.Error("a marked template must be usable")
	}
	if err := a.ensureBaseTemplate(context.Background()); err != nil {
		t.Errorf("ensureBaseTemplate must be a no-op when already usable: %v", err)
	}
}

// A template we did not build has no marker and must still be used as-is.
func TestAgentVMUserTemplateUsableWithoutMarker(t *testing.T) {
	a := newTestAgentVM(t, &fakeRunner{})
	a.Config.AgentVMTemplate = "my-team-base"
	if !a.ownedTemplateUsable() {
		t.Error("a user-maintained template must not require our marker")
	}
}

func TestAgentVMTemplateMarkerPathIsSafe(t *testing.T) {
	a := newTestAgentVM(t, &fakeRunner{})
	a.Config.AgentVMTemplate = "../../escape"
	got := a.templateMarkerPath()
	// The dangerous part of a traversal is the path separator, not the dots:
	// the marker must remain a direct child of the state directory.
	if strings.Contains(strings.TrimPrefix(got, a.StateDir+string(filepath.Separator)), string(filepath.Separator)) {
		t.Errorf("marker path must be a direct child of the state dir: %q", got)
	}
	if filepath.Dir(got) != a.StateDir {
		t.Errorf("marker dir = %q, want %q", filepath.Dir(got), a.StateDir)
	}
	if !strings.HasPrefix(filepath.Base(got), "agent-vm-template-") {
		t.Errorf("marker name lost its prefix: %q", filepath.Base(got))
	}
}
