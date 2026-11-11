package codebox

import (
	"path"
	"strings"
)

// managedVMPrefix marks VMs owned by codebox. `stop` removes every local
// Tart VM carrying this prefix, so it must not be reused for VMs created
// outside codebox.
const managedVMPrefix = "opencode-"

// VMName derives the stable Tart VM name from an image reference. It mirrors
// the justfile expression:
//
//	"opencode-" + replace(replace(trim_start_matches(file_name(image), "macos-"), ":", "-"), "@sha256", "-sha256")
func VMName(imageRef string) string {
	base := path.Base(imageRef)
	base = strings.TrimPrefix(base, "macos-")
	base = strings.ReplaceAll(base, ":", "-")
	base = strings.ReplaceAll(base, "@sha256", "-sha256")
	return managedVMPrefix + base
}

// ManagedVMName is the project-derived VM name for a project instance (P06):
// "opencode-" + instance. The prefix is what the lifecycle sweeps filter on,
// so project VMs are owned and enumerated by the same rule as the legacy
// singleton. The instance name already carries a path-derived suffix, so
// two same-named repositories and two worktrees do not collide.
func ManagedVMName(instance string) string {
	return managedVMPrefix + instance
}

// IsManagedVM reports whether name is a codebox-managed VM (carries the
// managed prefix). It is the single ownership test for the lifecycle sweeps.
func IsManagedVM(name string) bool {
	return strings.HasPrefix(name, managedVMPrefix)
}
