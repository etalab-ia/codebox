package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
)

// bindingsCmd implements `codebox bindings <subcommand>` (P09): the
// host-local per-project approval of optional credential bindings. An
// optional binding (github, context7) is inert until the project approves
// it; the record lives in host state, never in the repository.
//
//	bindings list
//	bindings approve <github|context7>
//	bindings revoke <github|context7>
func bindingsCmd(args []string, instance string) (int, error) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: codebox bindings list | approve <kind> | revoke <kind>")
		return 2, nil
	}
	stateDir := codebox.DefaultStateDir()
	path := codebox.BindingApprovalsPath(stateDir, instance)
	switch args[0] {
	case "list":
		return bindingsListCmd(path, instance)
	case "approve":
		return bindingsApproveCmd(path, args[1:])
	case "revoke":
		return bindingsRevokeCmd(path, instance, args[1:])
	default:
		return 2, fmt.Errorf("Unknown bindings command: %s (expected list, approve or revoke)", args[0])
	}
}

// bindingsListCmd shows every registered binding with its approval and
// storage state for this project. Values are never shown.
func bindingsListCmd(path, instance string) (int, error) {
	approvals, err := codebox.ReadBindingApprovals(codebox.DefaultFS, path)
	if err != nil {
		return 1, err
	}
	fmt.Printf("Credential bindings for %s:\n", instance)
	for _, b := range codebox.RegisteredBindings() {
		if !b.Optional {
			fmt.Printf("  %-10s always bound as %s (allowed hosts: %s)\n", b.Kind, b.GuestEnv, strings.Join(b.AllowHosts, ", "))
			continue
		}
		state := "not approved"
		if approvals.Approves(b.Kind) {
			state = "approved"
			if _, err := codebox.ReadStoredCredential(context.Background(), b.Kind); err != nil {
				state = "approved, but no credential stored (codebox auth add " + string(b.Kind) + ")"
			}
		}
		fmt.Printf("  %-10s %-12s as %s (allowed hosts: %s)\n", b.Kind, state, b.GuestEnv, strings.Join(b.AllowHosts, ", "))
	}
	return 0, nil
}

// bindingsApproveCmd records the project's approval of an optional binding.
// The binding takes effect at the next start of the project's instance.
func bindingsApproveCmd(path string, args []string) (int, error) {
	if len(args) != 1 {
		return 2, fmt.Errorf("Usage: codebox bindings approve <github|context7>")
	}
	kind, err := codebox.ParseOptionalBindingKind(args[0])
	if err != nil {
		return 2, err
	}
	if kind == codebox.CredentialGithub {
		project, err := codebox.DiscoverProject(".")
		if err != nil {
			return 1, fmt.Errorf("GitHub approval requires a project directory: %w", err)
		}
		if runtime, _ := projectRuntimeIsolation(project.Root); runtime != "" && runtime != codebox.RuntimeMicrosandbox {
			return 1, fmt.Errorf("the protected GitHub guest workflow is available only for Microsandbox projects")
		}
		if _, err := githubInitPreflightFn(project.Root); err != nil {
			return 1, err
		}
	}
	if err := codebox.ApproveBinding(codebox.DefaultFS, path, kind); err != nil {
		return 1, err
	}
	fmt.Printf("Approved the %s binding for this project. It takes effect at the next start; the credential must be stored (codebox auth add %s).\n", kind, kind)
	return 0, nil
}

// bindingsRevokeCmd lifts the approval and revokes the binding from the
// project's Microsandbox instance when one exists (live when running,
// persisted when stopped).
func bindingsRevokeCmd(path, instance string, args []string) (int, error) {
	if len(args) != 1 {
		return 2, fmt.Errorf("Usage: codebox bindings revoke <github|context7>")
	}
	// Parse the kind through the same validation as approve: an
	// unvalidated string would let `bindings revoke albert` strip the
	// mandatory Albert proxy registration from a live sandbox.
	kind, err := codebox.ParseOptionalBindingKind(args[0])
	if err != nil {
		return 2, err
	}
	if err := codebox.RevokeBindingApproval(codebox.DefaultFS, path, kind); err != nil {
		return 1, err
	}
	revoked, live, err := codebox.RevokeInstanceBinding(context.Background(), instance, kind)
	if err != nil {
		return 1, err
	}
	switch {
	case revoked && live:
		fmt.Printf("Revoked the %s binding from the running instance %s. Its guest environment keeps the inert placeholder until the next restart.\n", kind, instance)
	case revoked:
		fmt.Printf("Cleared the persisted %s binding from %s (effective at its next start).\n", kind, instance)
	default:
		fmt.Printf("Lifted the %s approval for this project (no running or stopped instance to update).\n", kind)
	}
	return 0, nil
}
