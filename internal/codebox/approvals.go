package codebox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// This file implements the P09 host-local approval record: an optional
// credential binding (github, context7) is inert for a project until that
// project approves it explicitly. The record lives in host state
// (~/.local/state/codebox/instances/<instance>/bindings.json), never in
// the repository: approval is a host-local trust decision and must not
// travel with a clone (issue #74).

// BindingApprovals is the persisted per-instance record of approved optional
// bindings. It carries kind names only — never values.
type BindingApprovals struct {
	SchemaVersion int `json:"schemaVersion"`
	// Approved lists the optional credential kinds this instance may bind.
	Approved []string `json:"approved,omitempty"`
}

const bindingApprovalsSchemaVersion = 1

// bindingApprovalsPath is the per-instance approval record path.
func bindingApprovalsPath(stateDir, instance string) string {
	return filepath.Join(InstanceStateDir(stateDir, instance), "bindings.json")
}

// ReadBindingApprovals loads the approval record. A missing file is not an
// error: it returns an empty record (nothing approved).
func ReadBindingApprovals(fs FS, path string) (BindingApprovals, error) {
	data, err := fs.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return BindingApprovals{SchemaVersion: bindingApprovalsSchemaVersion}, nil
		}
		return BindingApprovals{}, err
	}
	var a BindingApprovals
	if err := json.Unmarshal(data, &a); err != nil {
		return BindingApprovals{}, fmt.Errorf("binding approvals %s: %w", path, err)
	}
	if a.SchemaVersion > bindingApprovalsSchemaVersion {
		return BindingApprovals{}, fmt.Errorf("binding approvals %s: schemaVersion %d is newer than this build supports (%d)",
			path, a.SchemaVersion, bindingApprovalsSchemaVersion)
	}
	return a, nil
}

// WriteBindingApprovals atomically persists the record.
func WriteBindingApprovals(fs FS, path string, a BindingApprovals) error {
	a.SchemaVersion = bindingApprovalsSchemaVersion
	approved := append([]string(nil), a.Approved...)
	sort.Strings(approved)
	a.Approved = approved
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	if err := fs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicWrite(fs, path, append(data, '\n'), 0o600)
}

// Approves reports whether the record approves kind.
func (a BindingApprovals) Approves(kind CredentialKind) bool {
	for _, k := range a.Approved {
		if k == string(kind) {
			return true
		}
	}
	return false
}

// ParseOptionalBindingKind validates a kind supplied on the command line as a
// registered *optional* binding. It is the single gate for the CLI's approve
// and revoke paths: an unvalidated string would let `bindings revoke albert`
// strip the mandatory proxy registration from a live sandbox, and an unknown
// kind would record a name no runtime can bind.
func ParseOptionalBindingKind(name string) (CredentialKind, error) {
	kind := CredentialKind(name)
	b, ok := bindingForKind(kind)
	if !ok {
		return "", fmt.Errorf("unknown credential kind %q (expected github or context7)", name)
	}
	if !b.Optional {
		return "", fmt.Errorf("the %s binding is always active and cannot be approved or revoked", name)
	}
	return kind, nil
}

// ApproveBinding adds kind to the record at path. The kind must be a
// registered optional binding: approving a required binding is meaningless
// (it is always bound) and approving an unknown kind would record a name no
// runtime can bind.
func ApproveBinding(fs FS, path string, kind CredentialKind) error {
	b, ok := bindingForKind(kind)
	if !ok {
		return fmt.Errorf("unknown credential kind %q (expected github or context7)", kind)
	}
	if !b.Optional {
		return fmt.Errorf("the %s binding is always active and needs no approval", kind)
	}
	a, err := ReadBindingApprovals(fs, path)
	if err != nil {
		return err
	}
	if a.Approves(kind) {
		return nil
	}
	a.Approved = append(a.Approved, string(kind))
	return WriteBindingApprovals(fs, path, a)
}

// RevokeBindingApproval removes kind from the record at path. It only lifts
// the approval; revoking a live guest binding is revoke.go's job.
func RevokeBindingApproval(fs FS, path string, kind CredentialKind) error {
	a, err := ReadBindingApprovals(fs, path)
	if err != nil {
		return err
	}
	out := a.Approved[:0]
	for _, k := range a.Approved {
		if k != string(kind) {
			out = append(out, k)
		}
	}
	a.Approved = out
	return WriteBindingApprovals(fs, path, a)
}

// BindingApprovalsPath exposes the per-instance record path for the CLI.
func BindingApprovalsPath(stateDir, instance string) string {
	return bindingApprovalsPath(stateDir, instance)
}
