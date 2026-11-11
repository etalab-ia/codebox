package codebox

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// imageRunner fakes limactl for the image check: the capability probe answers
// helpExit, any real validation answers validateExit with validateStderr.
func imageRunner(helpExit, validateExit int, validateStderr string) *fakeRunner {
	return &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "limactl" && len(args) >= 3 && args[0] == "template" && args[1] == "validate" {
			if args[2] == "--help" {
				return ExecResult{ExitCode: helpExit}
			}
			return ExecResult{ExitCode: validateExit, Stderr: validateStderr}
		}
		return ExecResult{ExitCode: 0}
	}}
}

func TestValidateBaseTemplateImageAcceptsResolvableImage(t *testing.T) {
	runner := imageRunner(0, 0, "")
	if err := ValidateBaseTemplateImage(context.Background(), runner, DefaultAgentVMImage); err != nil {
		t.Fatalf("a resolvable image must pass: %v", err)
	}
	if !runner.hasCall("validate " + DefaultAgentVMImage) {
		t.Errorf("the configured image must actually be validated; calls: %v", runner.calls)
	}
}

// The failure a user can act on names the variable, the value, and Lima's own
// explanation. Without the diagnostic this is "exit 1" and nothing else.
func TestValidateBaseTemplateImageReportsLimaDiagnostic(t *testing.T) {
	diagnostic := `time="2026-10-22T14:33:00Z" level=fatal msg="template \"debian-13.yaml\" not found"`
	runner := imageRunner(0, 1, diagnostic)
	err := ValidateBaseTemplateImage(context.Background(), runner, "template://debian-13")
	if err == nil {
		t.Fatal("an unresolvable image must be refused")
	}
	for _, want := range []string{"AGENT_VM_IMAGE", "template://debian-13", "debian-13.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must contain %q: %v", want, err)
		}
	}
}

// A runner that fills only Stdout still explains the failure: OSRunner happens
// to populate both streams, but the helper must not depend on that.
func TestValidateBaseTemplateImageFallsBackToStdout(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if len(args) >= 3 && args[0] == "template" && args[2] == "--help" {
			return ExecResult{ExitCode: 0}
		}
		return ExecResult{ExitCode: 1, Stdout: "fatal: no such template"}
	}}
	err := ValidateBaseTemplateImage(context.Background(), runner, "template://nope")
	if err == nil || !strings.Contains(err.Error(), "no such template") {
		t.Fatalf("expected the stdout diagnostic to be surfaced, got %v", err)
	}
}

// Lima 1.0.x has no `template` subcommand, so it cannot verify a locator. That
// is "cannot check", never "the image is wrong": blocking the build there would
// break every 1.0.x install over a check it does not have.
func TestValidateBaseTemplateImageSkipsWhenLimaCannotValidate(t *testing.T) {
	runner := imageRunner(1, 1, "unknown command")
	if err := ValidateBaseTemplateImage(context.Background(), runner, DefaultAgentVMImage); err != nil {
		t.Fatalf("an old limactl must not fail the build over an unavailable check: %v", err)
	}
	if runner.hasCall("validate " + DefaultAgentVMImage) {
		t.Errorf("validation must not run when the capability probe failed; calls: %v", runner.calls)
	}
}

func TestValidateBaseTemplateImageRejectsEmptyValue(t *testing.T) {
	runner := &fakeRunner{}
	err := ValidateBaseTemplateImage(context.Background(), runner, "  ")
	if err == nil || !strings.Contains(err.Error(), "AGENT_VM_IMAGE") {
		t.Fatalf("an empty image must name the variable, got %v", err)
	}
	if runner.hasCall("template") {
		t.Errorf("an empty value needs no limactl call; calls: %v", runner.calls)
	}
}

// erroringRunner fails every command, standing in for a missing or
// unrunnable limactl binary.
type erroringRunner struct{ err error }

func (r erroringRunner) Run(context.Context, string, ...string) (ExecResult, error) {
	return ExecResult{}, r.err
}

func (r erroringRunner) RunEnv(context.Context, []string, string, ...string) (ExecResult, error) {
	return ExecResult{}, r.err
}

func (r erroringRunner) RunStdin(context.Context, io.Reader, string, ...string) (ExecResult, error) {
	return ExecResult{}, r.err
}

// A runner that cannot be executed at all is an error, not a verdict on the
// image: the caller must not read it as "Lima is missing the capability".
func TestValidateBaseTemplateImagePropagatesRunnerError(t *testing.T) {
	runner := erroringRunner{err: errors.New("boom")}
	if err := ValidateBaseTemplateImage(context.Background(), runner, DefaultAgentVMImage); err == nil {
		t.Fatal("a runner failure must be reported")
	}
}

// The default must be a locator Lima can resolve. `template:debian-13` looked
// similar and was rejected by `limactl create`, so the form itself is pinned
// here rather than only the value. The two-slash spelling is pinned rather
// than required of users: it is the one form Lima 1.x accepts, and Lima 2.0
// accepts both, so a default in this form works on every supported release.
func TestDefaultAgentVMImageIsAResolvableLocator(t *testing.T) {
	if DefaultAgentVMImage != "template://debian-12" {
		t.Errorf("DefaultAgentVMImage = %q, want the pinned template://debian-12", DefaultAgentVMImage)
	}
	if !strings.HasPrefix(DefaultAgentVMImage, "template://") {
		t.Errorf("DefaultAgentVMImage = %q: the default must name a built-in template in the form Lima 1.x also accepts", DefaultAgentVMImage)
	}
}

// A user who copies the `.env.example` template into `.env` gets whatever
// locator that file advertises, so a stale sample silently overrides the
// corrected default. Keep the two from drifting: the documented value must be
// the default, and it must use the same two-slash form, which is the one every
// supported Lima release accepts.
func TestEnvExampleAGENTVMImageMatchesDefault(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", ".env.example"))
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}
	var documented []string
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
		value, ok := strings.CutPrefix(line, "AGENT_VM_IMAGE=")
		if ok {
			documented = append(documented, strings.TrimSpace(value))
		}
	}
	if len(documented) == 0 {
		t.Fatal(".env.example no longer documents AGENT_VM_IMAGE")
	}
	for _, value := range documented {
		if value != DefaultAgentVMImage {
			t.Errorf(".env.example sets AGENT_VM_IMAGE=%q, want the default %q", value, DefaultAgentVMImage)
		}
	}
}

// A failed validate does not always mean a bad image: limactl refuses to run
// at all as root ("must not run as the root user"), and that refusal arrives on
// the same command. Blaming AGENT_VM_IMAGE there would send the user to change
// a value that was never the problem. The image-free `limactl info` witness
// separates the two without depending on Lima's wording.
func TestValidateBaseTemplateImageDistinguishesLimaRefusal(t *testing.T) {
	refusal := `time="2026-10-22T14:51:44Z" level=fatal msg="must not run as the root user"`
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if len(args) >= 3 && args[0] == "template" && args[2] == "--help" {
			return ExecResult{ExitCode: 0}
		}
		// Every real subcommand refuses, including the image-free witness.
		return ExecResult{ExitCode: 1, Stderr: refusal}
	}}
	err := ValidateBaseTemplateImage(context.Background(), runner, "template://debian-13")
	if err == nil {
		t.Fatal("an unvalidatable image must not be silently accepted")
	}
	if strings.Contains(err.Error(), "is not a Lima image") {
		t.Errorf("must not blame the configured image when limactl could not run: %v", err)
	}
	// Still actionable: the real reason travels with the message.
	if !strings.Contains(err.Error(), "must not run as the root user") {
		t.Errorf("the limactl refusal must be surfaced: %v", err)
	}
	// This fake refuses every command, so a witness built from an image would
	// fail exactly like `info` and the assertions above would still pass.
	// Assert the shape of the call instead.
	if !runner.hasCall("limactl info") {
		t.Errorf("the witness must be image-independent; calls: %v", runner.calls)
	}
}

// When limactl runs but rejects the image, the witness passes and the message
// stays the plain "not resolvable" one. This is the shape of a genuinely bad
// AGENT_VM_IMAGE.
func TestValidateBaseTemplateImageAttributesFailureOnDefaultImage(t *testing.T) {
	runner := imageRunner(0, 1, `level=fatal msg="template \"debian-12.yaml\" not found"`)
	err := ValidateBaseTemplateImage(context.Background(), runner, DefaultAgentVMImage)
	if err == nil || !strings.Contains(err.Error(), "is not a Lima image") {
		t.Fatalf("a missing default template must be attributed to the image: %v", err)
	}
	// Recommending the default to someone who already configured it is a
	// circle. The message has to point at the template store instead.
	if strings.Contains(err.Error(), "for example "+DefaultAgentVMImage) {
		t.Errorf("must not recommend the value that already failed: %v", err)
	}
	if !strings.Contains(err.Error(), "template store is incomplete") {
		t.Errorf("the remedy must explain what is actually wrong: %v", err)
	}
}

// The default image is the one most installs actually configure, so the
// witness must not depend on the configured image differing from it. Lima's
// root refusal arrives on every subcommand, including ones that take no image,
// which is what makes `limactl info` a usable witness here.
func TestValidateBaseTemplateImageDistinguishesRefusalOnDefaultImage(t *testing.T) {
	refusal := `time="2026-10-22T14:51:44Z" level=fatal msg="must not run as the root user"`
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if len(args) >= 3 && args[0] == "template" && args[2] == "--help" {
			return ExecResult{ExitCode: 0}
		}
		// Every real subcommand refuses, including the image-free witness.
		return ExecResult{ExitCode: 1, Stderr: refusal}
	}}
	err := ValidateBaseTemplateImage(context.Background(), runner, DefaultAgentVMImage)
	if err == nil {
		t.Fatal("an unvalidatable image must not be silently accepted")
	}
	if strings.Contains(err.Error(), "is not a Lima image") {
		t.Errorf("must not blame AGENT_VM_IMAGE when limactl could not run: %v", err)
	}
	// The witness can fail for reasons unrelated to the image, so the message
	// must not claim the image was fine — only that no verdict is available.
	if !strings.Contains(err.Error(), "cannot validate AGENT_VM_IMAGE") {
		t.Errorf("the message must say no verdict is available: %v", err)
	}
	if !strings.Contains(err.Error(), "must not run as the root user") {
		t.Errorf("the limactl refusal must be surfaced: %v", err)
	}
	// Both failures are reported: a non-zero witness is not proof that the
	// image check failed for the same reason, so its own diagnostic must not
	// be discarded.
	if !strings.Contains(err.Error(), "the image check reported") {
		t.Errorf("the image diagnostic must be preserved alongside the witness: %v", err)
	}
	// This fake refuses every command, so a witness built from an image would
	// fail exactly like `info` and the assertions above would still pass —
	// which is how the first witness implementation hid its hole. Assert the
	// shape of the call instead: the witness carries no image, and the image
	// is validated once.
	if !runner.hasCall("limactl info") {
		t.Errorf("the witness must be image-independent; calls: %v", runner.calls)
	}
	if n := strings.Count(strings.Join(runner.calls, "\n"), "template validate template://"); n != 1 {
		t.Errorf("exactly one image must be validated, got %d: %v", n, runner.calls)
	}
}

// A healthy limactl that rejects only the image must still be attributed to
// the image, so the witness cannot swallow a real rejection.
func TestValidateBaseTemplateImageAttributesWhenWitnessSucceeds(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		switch {
		case len(args) >= 3 && args[0] == "template" && args[2] == "--help":
			return ExecResult{ExitCode: 0}
		case len(args) > 0 && args[0] == "info":
			return ExecResult{ExitCode: 0, Stdout: `{"version":"v1.2.0"}`}
		}
		return ExecResult{ExitCode: 1, Stderr: `level=fatal msg="template \"debian-13.yaml\" not found"`}
	}}
	err := ValidateBaseTemplateImage(context.Background(), runner, "template://debian-13")
	if err == nil || !strings.Contains(err.Error(), "is not a Lima image") {
		t.Fatalf("a witness that runs proves the image itself is at fault: %v", err)
	}
}

// The witness must not be fooled by state that only affects instances: a
// leftover or unhealthy instance makes `limactl list` fail with
// unmatchedInstancesError, which is why the witness is `info` and not `list`.
// `info` reads the template store and LimaHome, the same inputs the image
// check needs, and touches no instance state.
func TestValidateBaseTemplateImageWitnessIgnoresInstanceState(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		switch {
		case len(args) >= 3 && args[0] == "template" && args[2] == "--help":
			return ExecResult{ExitCode: 0}
		case len(args) > 0 && args[0] == "info":
			return ExecResult{ExitCode: 0, Stdout: `{"version":"v1.2.0"}`}
		}
		return ExecResult{ExitCode: 1, Stderr: `level=fatal msg="template \"nope.yaml\" not found"`}
	}}
	err := ValidateBaseTemplateImage(context.Background(), runner, "template://nope")
	if err == nil || !strings.Contains(err.Error(), "is not a Lima image") {
		t.Fatalf("an unhealthy instance must not stop the image from being blamed: %v", err)
	}
}

// A witness made of another image would misreport this case: when the DEFAULT
// image is itself missing from the store while limactl runs fine, validating
// the default as the witness fails too, and the user is told the image "could
// not be checked" when it was checked and found unusable. `info` keeps the
// two apart because it does not depend on any single template.
func TestValidateBaseTemplateImageAttributesMissingDefaultImage(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		switch {
		case len(args) >= 3 && args[0] == "template" && args[2] == "--help":
			return ExecResult{ExitCode: 0}
		case len(args) > 0 && args[0] == "info":
			return ExecResult{ExitCode: 0, Stdout: `{"version":"v1.2.0"}`}
		}
		return ExecResult{ExitCode: 1, Stderr: `level=fatal msg="template \"debian-12.yaml\" not found"`}
	}}
	err := ValidateBaseTemplateImage(context.Background(), runner, DefaultAgentVMImage)
	if err == nil {
		t.Fatal("an unresolvable default image must be refused")
	}
	if strings.Contains(err.Error(), "cannot validate AGENT_VM_IMAGE") {
		t.Errorf("the check ran, so it must not be reported as not performed: %v", err)
	}
	if !strings.Contains(err.Error(), "is not a Lima image") {
		t.Errorf("the missing image must be blamed on AGENT_VM_IMAGE: %v", err)
	}
}

func TestCreateBaseTemplateKeepsLimaDiagnostic(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if len(args) > 0 && args[0] == "create" {
			return ExecResult{ExitCode: 1, Stderr: `level=fatal msg="template \"debian-13.yaml\" not found"`}
		}
		return ExecResult{ExitCode: 0}
	}}
	spec := BaseTemplateSpec{Name: "agent-vm-base", Image: "template://debian-13", DiskGB: 20, MemoryGB: 4, CPUs: 2}
	err := createBaseTemplate(context.Background(), runner, spec)
	if err == nil {
		t.Fatal("a failed create must be reported")
	}
	for _, want := range []string{"agent-vm-base", "exit 1", "debian-13.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("create error must contain %q: %v", want, err)
		}
	}
}

// doctorRunner installs limactl but no template, so Doctor reaches the
// "pending build" branch where the image check lives.
func doctorRunner(validateExit int, validateStderr string) *fakeRunner {
	return &fakeRunner{onRun: func(name string, args []string) ExecResult {
		switch {
		case name != "limactl":
			return ExecResult{ExitCode: 0}
		case args[0] == "--version":
			return ExecResult{ExitCode: 0, Stdout: "limactl version 1.2.0\n"}
		case args[0] == "list":
			return ExecResult{ExitCode: 0}
		case args[0] == "template" && len(args) >= 3 && args[2] == "--help":
			return ExecResult{ExitCode: 0}
		case args[0] == "template":
			return ExecResult{ExitCode: validateExit, Stderr: validateStderr}
		}
		return ExecResult{ExitCode: 0}
	}}
}

// Doctor is the last moment an invalid image can be reported before a start
// rebuilds the template, so it fails there instead of announcing a pending
// build that cannot succeed.
func TestAgentVMDoctorRefusesUnresolvableImage(t *testing.T) {
	runner := doctorRunner(1, `level=fatal msg="template \"debian-13.yaml\" not found"`)
	a := newTestAgentVM(t, runner)
	a.Config.AgentVMImage = "template://debian-13"
	err := a.Doctor(context.Background())
	if err == nil {
		t.Fatal("doctor must refuse an image limactl cannot resolve")
	}
	if !strings.Contains(err.Error(), "AGENT_VM_IMAGE") {
		t.Errorf("the error must name the variable: %v", err)
	}
	if runner.hasCall("limactl create") {
		t.Errorf("doctor stays read-only; calls: %v", runner.calls)
	}
}

func TestAgentVMDoctorReportsResolvableImageAsPendingBuild(t *testing.T) {
	runner := doctorRunner(0, "")
	a := newTestAgentVM(t, runner)
	if err := a.Doctor(context.Background()); err != nil {
		t.Fatalf("a resolvable image pending a build is not an error: %v", err)
	}
	if !runner.hasCall("validate " + DefaultAgentVMImage) {
		t.Errorf("doctor must validate the configured image; calls: %v", runner.calls)
	}
	if runner.hasCall("limactl create") {
		t.Errorf("doctor must not build the template; calls: %v", runner.calls)
	}
}

// ensureBaseTemplate is the path a start takes. It must reject the image
// before BuildBaseTemplate deletes the previous template.
func TestEnsureBaseTemplateValidatesImageBeforeBuilding(t *testing.T) {
	runner := doctorRunner(1, `level=fatal msg="template \"debian-13.yaml\" not found"`)
	a := newTestAgentVM(t, runner)
	a.Config.AgentVMImage = "template://debian-13"
	err := a.ensureBaseTemplate(context.Background())
	if err == nil {
		t.Fatal("an unresolvable image must stop the build")
	}
	if !strings.Contains(err.Error(), "AGENT_VM_IMAGE") {
		t.Errorf("the error must name the variable: %v", err)
	}
	for _, forbidden := range []string{"limactl create", "limactl delete", "limactl shell"} {
		if runner.hasCall(forbidden) {
			t.Errorf("%q ran before the image was validated; calls: %v", forbidden, runner.calls)
		}
	}
}
