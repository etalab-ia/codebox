package main

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/etalab-ia/just-code/internal/justcode"
)

// workspaceCmd implements `just-code workspace <subcommand>` (P22): the
// sealed guest workspace. The host checkout is never mounted into the guest,
// so this is the only place content crosses the boundary.
//
//	workspace status          # what would cross, what the filter refuses, and why
//	workspace sync [--force]  # refresh the guest workspace from the host checkout
//	workspace allow <path>    # record a per-file re-inclusion past the filter
//	workspace deny <path>     # drop a recorded re-inclusion
//	workspace export [--out <file>]  # write the guest's diff to the host for review
//	workspace apply [--all]   # apply the agent's changes to the host checkout
//
// The transfer source of status, allow and apply is the configured workspace
// dir (WORKSPACE_DIR, or the discovered project root) — the same source
// provisioning copies from, so the two sides of the boundary agree.
func workspaceCmd(args []string, cfg justcode.Config, instance string) (int, error) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: just-code workspace status | sync [--force] | allow <path> | deny <path> | export [--out <file>] | apply [--all]")
		return 2, nil
	}
	stateDir := justcode.DefaultStateDir()
	store := justcode.TransferOptInStore{Path: justcode.TransferOptInPath(stateDir, instance), FS: justcode.DefaultFS}
	records, err := store.Load()
	if err != nil {
		return 1, err
	}
	optIn := map[string]bool{}
	for rel := range records {
		optIn[rel] = true
	}

	switch args[0] {
	case "status":
		return workspaceStatusCmd(cfg.WorkspaceDir, instance, records)
	case "sync":
		force := false
		for _, a := range args[1:] {
			if a == "--force" {
				force = true
				continue
			}
			// An unrecognized flag must not be ignored: `sync --forc` running
			// an unforced sync is the confusing half of a typo.
			return 2, fmt.Errorf("Unknown workspace sync option: %s (expected --force)", a)
		}
		rt := justcode.NewMicrosandboxRuntimeForInstance(cfg, instance)
		if err := rt.SyncGuestWorkspace(context.Background(), justcode.SyncOptions{Force: force, OptIn: optIn}); err != nil {
			return 1, err
		}
		return 0, nil
	case "allow":
		if len(args) != 2 {
			return 2, fmt.Errorf("workspace allow takes exactly one path relative to the project root")
		}
		return workspaceAllowCmd(store, cfg.WorkspaceDir, args[1])
	case "deny":
		if len(args) != 2 {
			return 2, fmt.Errorf("workspace deny takes exactly one path relative to the project root")
		}
		if err := store.Revoke(justcode.NormalizeTransferPath(args[1])); err != nil {
			return 1, err
		}
		fmt.Printf("%s is back under the default-deny filter.\n", justcode.NormalizeTransferPath(args[1]))
		return 0, nil
	case "export":
		out := ""
		if len(args) > 1 {
			if args[1] != "--out" {
				return 2, fmt.Errorf("Unknown workspace export option: %s (expected --out <file>)", args[1])
			}
			if len(args) < 3 {
				return 2, fmt.Errorf("workspace export --out needs a file path")
			}
			out = args[2]
			if len(args) > 3 {
				return 2, fmt.Errorf("workspace export takes a single --out <file>")
			}
		}
		rt := justcode.NewMicrosandboxRuntimeForInstance(cfg, instance)
		// A legacy guest's "no changes" does not cover committed work
		// (#313): say so before reporting an empty export.
		if warning, werr := rt.GuestRecoveryWarning(context.Background()); werr == nil && warning != "" {
			fmt.Fprintln(os.Stderr, warning)
		}
		path, err := rt.ExportGuestChanges(context.Background(), out)
		if err != nil {
			return 1, err
		}
		if path == "" {
			fmt.Println("The guest workspace has no changes to deliver.")
			return 0, nil
		}
		fmt.Printf("Guest changes written for review: %s\nApply with 'git apply' after reviewing, or push them through the GitHub workflow (P13).\n", path)
		return 0, nil
	case "apply":
		applyAll := false
		for _, a := range args[1:] {
			if a == "--all" {
				applyAll = true
				continue
			}
			// An unrecognized flag must not be ignored: `apply --al`
			// skipping the review screen is the confusing half of a typo.
			return 2, fmt.Errorf("Unknown workspace apply option: %s (expected --all)", a)
		}
		rt := justcode.NewMicrosandboxRuntimeForInstance(cfg, instance)
		return runWorkspaceApply(rt, cfg.WorkspaceDir, instance, applyAll)
	default:
		return 2, fmt.Errorf("Unknown workspace command: %s (expected status, sync, allow, deny, export or apply)", args[0])
	}
}

// workspaceStatusCmd shows the transfer set. It is read-only: it resolves the
// filter over the host checkout and reports, without sending anything.
func workspaceStatusCmd(sourceDir, instance string, records map[string]justcode.TransferOptInRecord) (int, error) {
	optIn := map[string]bool{}
	for rel := range records {
		optIn[rel] = true
	}
	man, err := justcode.ResolveTransferSet(context.Background(), sourceDir, justcode.TransferOptions{OptIn: optIn})
	if err != nil {
		return 1, err
	}
	fmt.Printf("Sealed guest workspace for %s\n\n", instance)
	fmt.Print(man.Summary())
	// The plan requires the user to SEE the filtered set, not just its size:
	// a review screen that only counts files cannot be reviewed.
	included := man.Included()
	if len(included) > 0 {
		fmt.Println("\nWill cross into the guest:")
		const maxListed = 200
		for i, e := range included {
			if i == maxListed {
				fmt.Printf("  ... and %d more\n", len(included)-maxListed)
				break
			}
			fmt.Printf("  %s (%s)\n", e.Rel, justcode.FormatByteSize(e.Size))
		}
	}
	if len(records) > 0 {
		fmt.Println("\nRecorded per-file re-inclusions:")
		rels := make([]string, 0, len(records))
		for rel := range records {
			rels = append(rels, rel)
		}
		sort.Strings(rels)
		for _, rel := range rels {
			fmt.Printf("  %s (recorded %s)\n", rel, records[rel].RecordedAt)
		}
	}
	fmt.Println("\nThe host checkout is not mounted into the guest; files cross only through this filtered transfer.")
	return 0, nil
}

// workspaceAllowCmd records a per-file re-inclusion. The path must be a
// concrete file the filter refused: a blanket "include everything" is not an
// option, and a path that was not excluded needs no decision.
func workspaceAllowCmd(store justcode.TransferOptInStore, sourceDir, rawPath string) (int, error) {
	rel := justcode.NormalizeTransferPath(rawPath)
	if rel == "" {
		return 2, fmt.Errorf("workspace allow needs a path relative to the project root")
	}
	// Resolve first so the decision is recorded against a path the filter
	// actually evaluated; an unknown path would create a rule that never
	// applies and silently mislead the next reader.
	man, err := justcode.ResolveTransferSet(context.Background(), sourceDir, justcode.TransferOptions{})
	if err != nil {
		return 1, err
	}
	var found *justcode.TransferEntry
	for _, e := range man.Entries {
		if e.Rel == rel {
			entry := e
			found = &entry
			break
		}
	}
	if found == nil {
		return 1, fmt.Errorf("%s is not in the transfer set for this project (paths are relative to the project root, slash-separated)", rel)
	}
	if found.Included {
		fmt.Printf("%s already crosses into the guest; no decision needed.\n", rel)
		return 0, nil
	}
	if !found.Overridable {
		return 1, fmt.Errorf("%s is excluded for a structural reason that a per-file decision cannot override: %s", rel, found.Reason)
	}
	if err := store.Allow(rel, found.Reason); err != nil {
		return 1, err
	}
	fmt.Printf("%s is now re-included (overridden rule: %s).\nIt crosses on the next 'just-code workspace sync'.\n", rel, found.Reason)
	return 0, nil
}
