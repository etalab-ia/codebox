package codebox

import (
	"os"
	"path/filepath"
	"strings"
)

// DefaultStateDir returns the host state directory shared by the backends:
// ~/.local/state/codebox.
func DefaultStateDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "codebox")
	}
	return filepath.Join(".local", "state", "codebox")
}

// InstanceStateDir is the per-instance host state directory: logs, staged
// files and other instance-scoped artifacts live under it so two projects
// never share state. instance is the project-derived instance name (P05).
func InstanceStateDir(stateDir, instance string) string {
	return filepath.Join(stateDir, "instances", instance)
}

// ProjectRegistryPath is the host path of the project -> instance registry
// (P05). It lives outside any workspace.
func ProjectRegistryPath(stateDir string) string {
	return filepath.Join(stateDir, "projects.json")
}

// SetupLockPath is the host path of the per-instance setup lock (P05). It is
// distinct from the project lockfile (resolve.go ProjectLockPath), which is
// the configuration lock inside the project.
func SetupLockPath(stateDir, instance string) string {
	return filepath.Join(InstanceStateDir(stateDir, instance), "setup.lock")
}

// LegacyInstanceLogPath returns the pre-P06 log path for a runtime, used
// when a backend operates on the legacy singleton instance so existing logs
// keep appending where they always did.
func LegacyInstanceLogPath(stateDir, runtime string) string {
	return filepath.Join(stateDir, runtime+".log")
}

// LegacyInstanceStageDir is the pre-P06 stage directory for a runtime, used
// when a backend operates on the legacy singleton instance.
func LegacyInstanceStageDir(stateDir, runtime string) string {
	return filepath.Join(stateDir, runtime)
}

// TartStageDir is the read-only share handed to the Tart guest. It holds only
// the bootstrap script, never the checkout or its .env.
func TartStageDir(stateDir string) string {
	return filepath.Join(stateDir, "tart")
}

// TartLogPath is the host log for the Tart backend.
func TartLogPath(stateDir string) string {
	return filepath.Join(stateDir, "tart.log")
}

// AgentVMStageDir holds the agent-vm backend's staged files (the secrets env
// pushed into the guest). It never contains the checkout or its .env.
func AgentVMStageDir(stateDir string) string {
	return filepath.Join(stateDir, "agent-vm")
}

// AgentVMLogPath is the host log for the agent-vm backend.
func AgentVMLogPath(stateDir string) string {
	return filepath.Join(stateDir, "agent-vm.log")
}

// AgentVMTemplateMarkerPath is the completion marker for a codebox-built
// base template. It is written on the host only after provisioning and the
// final stop succeed, so its presence distinguishes a finished template from
// one left behind by an interrupted build. A marker is needed in addition to
// the in-guest checks because a killed process cannot run its own rollback.
func AgentVMTemplateMarkerPath(stateDir, template string) string {
	return filepath.Join(stateDir, "agent-vm-template-"+safeFileName(template)+".ready")
}

// safeFileName reduces a name to characters safe in a filename, so an unusual
// AGENT_VM_TEMPLATE value cannot escape the state directory or collide with
// another marker.
func safeFileName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "template"
	}
	return b.String()
}
