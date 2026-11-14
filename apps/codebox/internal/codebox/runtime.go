// Package codebox implements the codebox CLI as a tested Go library — a
// single binary replacing the original justfile across the Microsandbox and
// Tart runtimes.
package codebox

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Runtime identifies a supported sandbox backend.
type Runtime string

const (
	RuntimeMicrosandbox Runtime = "microsandbox"
	RuntimeTart         Runtime = "tart"
	RuntimeAgentVM      Runtime = "agent-vm"
)

// allRuntimes is the deterministic ordering used by `stop` and `check`, matching
// the justfile's _running-runtimes.
var allRuntimes = []Runtime{RuntimeMicrosandbox, RuntimeTart, RuntimeAgentVM}

// currentGOOS is the platform indirection for tests: the fake-backends in
// dispatcher_test need to steer which runtimes get probed without rebuilding
// for Windows. It is not a knob; production callers read runtime.GOOS.
var currentGOOS = runtime.GOOS

// supportedRuntimes is the subset of allRuntimes that exists on this platform.
// Windows runs only the microsandbox runtime (via WHP): Tart is macOS-only, so
// probing it would only surface a raw "executable file not found" error from
// `stop` and `check`.
func supportedRuntimes() []Runtime {
	return supportedRuntimesOn(currentGOOS)
}

func supportedRuntimesOn(goos string) []Runtime {
	if goos == "windows" {
		return []Runtime{RuntimeMicrosandbox}
	}
	return allRuntimes
}

// RuntimeInstallCheck reports whether rt is installed on this host and, when
// it is not, the command that installs it. The init wizard's advanced
// settings screen uses it to flag a runtime that cannot start yet (#170).
// The check is read-only; installation stays with setup.
func RuntimeInstallCheck(rt Runtime) (installed bool, installHint string) {
	switch rt {
	case RuntimeTart:
		_, err := exec.LookPath("tart")
		return err == nil, "brew install openai/tools/tart"
	case RuntimeAgentVM:
		_, err := exec.LookPath("limactl")
		return err == nil, "brew install lima"
	default:
		hint := "codebox setup"
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return validateInstalledMSBRuntime(ctx) == nil, hint
	}
}

// Backend is the lifecycle of a single sandbox backend. Microsandbox and Tart
// each implement it.
type Backend interface {
	ID() Runtime
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	// Restart is non-destructive (P07): it stops and starts the existing
	// instance, preserving disk and guest state. The destructive rebuild is
	// Recreate, which is the only path that may delete an instance.
	Restart(ctx context.Context) error
	// Recreate is the explicit destructive rebuild: it deletes the instance
	// and creates a fresh one, losing guest sessions, guest-installed tools
	// and guest-only files. It names that loss in its output.
	Recreate(ctx context.Context) error
	Clean(ctx context.Context) error
	Doctor(ctx context.Context) error
	// Logs and Shell are interactive and run attached to the terminal.
	Logs() error
	Shell() error
	IsRunning(ctx context.Context) (bool, error)
	// Endpoint returns the host-reachable backend URL when running.
	Endpoint(ctx context.Context) (string, error)
	// RunAgent launches the OpenCode TUI in the foreground inside the guest
	// (isolation full), with the workspace as its working directory and the
	// host terminal passed through. It blocks until the TUI exits.
	RunAgent(ctx context.Context) error
	// Status describes the guest's current state for `check` in isolation
	// full, where there is no health endpoint to probe.
	Status(ctx context.Context) (string, error)
}

// Reconciler is the optional backend capability (P07) that applies
// configuration changes to an existing instance without recreating it,
// resuming interrupted applies from the persisted journal. Backends
// without persisted reconciliation state (Tart, agent-vm) do not
// implement it and keep their plain Start semantics.
type Reconciler interface {
	Reconcile(ctx context.Context) error
}

// StartOrReconcile starts the backend through its reconciliation path
// when it has one, so an existing instance is classified instead of
// blindly started. A nil Reconciler falls back to Start.
func StartOrReconcile(ctx context.Context, b Backend) error {
	if r, ok := b.(Reconciler); ok {
		return r.Reconcile(ctx)
	}
	return b.Start(ctx)
}

// ResolveRuntime picks a runtime from an explicit --<runtime> flag and the
// RUNTIME environment preference. The explicit flag always wins, mirroring the
// justfile contract. Without either, Microsandbox is the built-in default on
// every platform (P12): it is the runtime with the sealed workspace, so the
// zero-flag path is the safe one rather than the historically familiar one.
// Tart and agent-vm remain explicitly selectable.
func ResolveRuntime(flag, preference string) (Runtime, error) {
	return resolveRuntimeOn(flag, preference, runtime.GOOS)
}

// resolveRuntimeOn is ResolveRuntime with the platform as an argument so the
// Windows contract can be exercised from any OS.
func resolveRuntimeOn(flag, preference, goos string) (Runtime, error) {
	if flag != "" {
		return parseRuntimeFlagOn(flag, goos)
	}
	if preference != "" {
		return parseRuntimeNameOn(preference, goos)
	}
	// Microsandbox is the default everywhere. RUNTIME and the --<runtime>
	// flags still override it, so an empty selection never fails.
	return RuntimeMicrosandbox, nil
}

// parseRuntimeFlag accepts --microsandbox or --tart.
func parseRuntimeFlag(flag string) (Runtime, error) {
	return parseRuntimeFlagOn(flag, runtime.GOOS)
}

func parseRuntimeFlagOn(flag, goos string) (Runtime, error) {
	return parseRuntimeNameOn(strings.TrimPrefix(flag, "--"), goos)
}

// parseRuntimeName accepts microsandbox or tart (the RUNTIME env spelling) and
// rejects anything else with a message matching the accepted flag surface. Tart
// is macOS-only; on Windows it is rejected with a pointer at the runtime that
// does work there.
func parseRuntimeName(name string) (Runtime, error) {
	return parseRuntimeNameOn(name, runtime.GOOS)
}

func parseRuntimeNameOn(name, goos string) (Runtime, error) {
	switch name {
	case string(RuntimeMicrosandbox):
		return RuntimeMicrosandbox, nil
	case string(RuntimeTart):
		if goos == "windows" {
			return "", fmt.Errorf("tart is macOS only; use --microsandbox")
		}
		return RuntimeTart, nil
	case string(RuntimeAgentVM):
		if goos == "windows" {
			return "", fmt.Errorf("agent-vm is macOS/Linux only; use --microsandbox")
		}
		return RuntimeAgentVM, nil
	default:
		return "", fmt.Errorf("expected --microsandbox, --tart or --agent-vm (RUNTIME must be microsandbox, tart or agent-vm)")
	}
}
