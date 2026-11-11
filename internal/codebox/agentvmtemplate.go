package codebox

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// BaseTemplateSpec describes the Lima base template codebox builds for the
// agent-vm runtime. Building it ourselves is what removes the dependency on
// the agent-vm tool: `agent-vm setup` cannot be pinned to a release (upstream
// publishes no tags), so executing it would mean running an unpinned
// third-party script with the user's privileges.
type BaseTemplateSpec struct {
	// Name is the Lima instance name of the template.
	Name string
	// Image is the Lima image reference to create it from.
	Image    string
	DiskGB   int
	MemoryGB int
	CPUs     int
}

// baseTemplateCreateArgs builds the `limactl create` argument list. The
// template is created with no mounts: it is a builder image, not a project VM.
// Workspace mounts are applied per VM when the managed instance is cloned,
// which is also why this must stay mountless — a mount baked in here would
// leak into every VM cloned from the template.
func baseTemplateCreateArgs(spec BaseTemplateSpec) []string {
	return []string{
		"create",
		"--name=" + spec.Name,
		spec.Image,
		"--set", ".mounts=[]",
		"--disk=" + strconv.Itoa(spec.DiskGB),
		"--memory=" + strconv.Itoa(spec.MemoryGB),
		"--cpus=" + strconv.Itoa(spec.CPUs),
		"--tty=false",
	}
}

// DeleteBaseTemplate removes a base template. A genuinely absent instance is
// not an error; every other nonzero exit is propagated.
//
// `limactl delete` already exits 0 for an instance it does not know about
// ("Ignoring non-existent instance"), so no exit code needs special-casing.
// The failure worth surfacing is the opposite one: an instance directory that
// exists but cannot be read (permissions, a concurrent clone) exits nonzero
// with "it was NOT deleted", and silently treating that as success would let
// Start accept a broken template based only on its name.
func DeleteBaseTemplate(ctx context.Context, r Runner, name string) error {
	res, err := r.Run(ctx, "limactl", "delete", name, "--force")
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("limactl delete %s failed (exit %d): %s", name, res.ExitCode, res.Stderr)
	}
	return nil
}

// createBaseTemplate runs `limactl create` for the base template and keeps
// Lima's own diagnostic on failure.
//
// `limactl create` is the step that resolves the image, so it is the step whose
// explanation a user needs when AGENT_VM_IMAGE is wrong. runOK discards the
// captured output, which leaves "failed (exit 1)" and nothing to act on.
func createBaseTemplate(ctx context.Context, r Runner, spec BaseTemplateSpec) error {
	res, err := r.Run(ctx, "limactl", baseTemplateCreateArgs(spec)...)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("limactl create failed for %s (exit %d): %s", spec.Name, res.ExitCode, commandDiagnostic(res))
	}
	return nil
}

// BuildBaseTemplate creates, boots, provisions and stops the Lima base
// template, then records a completion marker. It is not resumable: any
// existing instance with this name is removed first, so calling it on a
// populated template rebuilds from scratch. Callers must therefore gate it on
// the template being absent or unmarked (see AgentVM.ensureBaseTemplate).
//
// The marker is written last and is the only signal that the template is
// usable. An interrupted build cannot run its own rollback, so the next start
// needs a way to tell a finished template from an abandoned one.
func BuildBaseTemplate(ctx context.Context, r Runner, spec BaseTemplateSpec, markerPath, script string, out io.Writer) error {
	fmt.Fprintf(out, "Building the %s base template (this takes several minutes)...\n", spec.Name)

	// Clear partial state: an interrupted earlier build leaves an instance
	// Lima knows about but that was never provisioned, and the marker from a
	// previous successful build must not survive a rebuild.
	if err := DeleteBaseTemplate(ctx, r, spec.Name); err != nil {
		return err
	}
	_ = os.Remove(markerPath)

	if err := createBaseTemplate(ctx, r, spec); err != nil {
		return err
	}

	fmt.Fprintf(out, "Starting %s...\n", spec.Name)
	if err := runOK(r, ctx, "limactl", "start", spec.Name); err != nil {
		// Roll back so a retry starts from a clean slate rather than an
		// instance that exists in a broken half-created state.
		_ = DeleteBaseTemplate(ctx, r, spec.Name)
		return fmt.Errorf("limactl start failed for %s: %w", spec.Name, err)
	}

	fmt.Fprintf(out, "Provisioning %s...\n", spec.Name)
	// The script is piped on stdin and runs as the guest user, using sudo
	// internally. A regular user matters: OpenCode installs under $HOME, which
	// would be root's home if the script ran as root.
	if err := runStdinOK(r, ctx, strings.NewReader(script), "limactl", "shell", spec.Name, "sh", "-s"); err != nil {
		_ = DeleteBaseTemplate(ctx, r, spec.Name)
		return fmt.Errorf("provisioning %s failed: %w", spec.Name, err)
	}

	fmt.Fprintf(out, "Stopping %s...\n", spec.Name)
	if err := runOK(r, ctx, "limactl", "stop", spec.Name); err != nil {
		// The template is provisioned but has not been stopped cleanly, so it
		// stays unmarked and the next start rebuilds it rather than cloning a
		// running instance.
		_ = DeleteBaseTemplate(ctx, r, spec.Name)
		return fmt.Errorf("limactl stop failed for %s: %w", spec.Name, err)
	}

	// Written last, and only after every step succeeded.
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(markerPath, []byte(spec.Name+"\n"), 0o644); err != nil {
		return fmt.Errorf("template %s was built but its completion marker could not be written: %w", spec.Name, err)
	}

	fmt.Fprintf(out, "Base template %s is ready.\n", spec.Name)
	return nil
}
