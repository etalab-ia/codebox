package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/etalab-ia/just-code/internal/justcode"
)

func updateCommandSkipsStartupRecovery(args []string) bool {
	for _, arg := range args {
		if arg == "--recover" || arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func updateCmd(args []string, projectRoot string) (int, error) {
	var selected []string
	approve := false
	recover := false
	rollback := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			fmt.Println("Usage: just-code update [--skill <catalogue/id>]... [--yes]\n       just-code update --recover [--rollback] [--yes]\n\nReview selected versioned project skills against the current catalogue.\nWithout --yes, apply requires an interactive confirmation. A non-TTY run\nwithout --yes is preview-only. --recover --rollback restores the journaled\nprevious manifest and lock after backing up current managed files.")
			return 0, nil
		case "--skill":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return 2, fmt.Errorf("--skill requires a catalogue ID such as official/rgaa")
			}
			i++
			selected = append(selected, args[i])
		case "--yes":
			approve = true
		case "--recover":
			recover = true
		case "--rollback":
			rollback = true
		default:
			return 2, fmt.Errorf("unknown update option %q", args[i])
		}
	}
	if rollback && !recover {
		return 2, fmt.Errorf("--rollback requires --recover")
	}
	if recover {
		if len(selected) > 0 {
			return 2, fmt.Errorf("--recover cannot be combined with --skill")
		}
		if !rollback {
			if err := justcode.RecoverProjectUpdate(justcode.DefaultFS, projectRoot); err != nil {
				return 1, fmt.Errorf("%w\nIf the current managed files contain intentional edits, inspect the journal and run 'just-code update --recover --rollback' to restore the previous pair with backups", err)
			}
			fmt.Println("Pending project update recovered, or no recovery was needed.")
			return 0, nil
		}
		if !approve {
			if !isTTY() {
				fmt.Println("Preview only: rollback restores the journaled previous manifest and lock, merging only the managed just-code section in AGENTS.md. Current managed files will be backed up under .just-code/recovery-backups/. Re-run with --yes to approve.")
				return 0, nil
			}
			fmt.Print("Restore the journaled previous project configuration? Current managed files will be backed up first. [y/N] ")
			reply, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if !strings.EqualFold(strings.TrimSpace(reply), "y") && !strings.EqualFold(strings.TrimSpace(reply), "yes") {
				fmt.Println("No changes applied.")
				return 0, nil
			}
		}
		backupDir, err := justcode.RollbackProjectUpdate(justcode.DefaultFS, projectRoot)
		if err != nil {
			return 1, err
		}
		fmt.Printf("Previous project configuration restored. Current managed files backed up under .just-code/recovery-backups/%s.\n", filepath.Base(backupDir))
		return 0, nil
	}
	if err := justcode.RecoverProjectUpdate(justcode.DefaultFS, projectRoot); err != nil {
		return 1, fmt.Errorf("recover project update: %w", err)
	}
	manifest, err := justcode.ReadProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(projectRoot))
	if err != nil {
		return 1, err
	}
	if manifest.SkillsLocalOnly {
		return 1, fmt.Errorf("project skill updates currently require versioned skills; local-only selections were not changed")
	}
	if len(selected) == 0 && len(manifest.Skills) == 0 {
		plan, err := justcode.PlanProjectUpdate(context.Background(), justcode.DefaultFS, projectRoot, nil, "", nil)
		if err != nil {
			return 1, err
		}
		if !plan.NoOp {
			return 1, fmt.Errorf("unexpected update plan for a project without versioned skills")
		}
		fmt.Println("No versioned project skills are selected; nothing to update.")
		return 0, nil
	}
	selectedIDs := make(map[string]bool, len(manifest.Skills))
	for _, id := range manifest.Skills {
		selectedIDs[id] = true
	}
	seen := map[string]bool{}
	for _, id := range selected {
		if !selectedIDs[id] {
			return 2, fmt.Errorf("skill %q is not selected in this project", id)
		}
		if seen[id] {
			return 2, fmt.Errorf("skill %q was selected for update more than once", id)
		}
		seen[id] = true
	}
	revision, err := justcode.LatestProjectSkillsRevision(context.Background())
	if err != nil {
		return 1, err
	}
	plan, err := justcode.PlanProjectUpdate(context.Background(), justcode.DefaultFS, projectRoot, selected, revision, nil)
	if err != nil {
		return 1, err
	}
	if plan.NoOp {
		fmt.Println("Project skills are already current; nothing changed.")
		return 0, nil
	}
	fmt.Printf("Project skill update available at catalogue commit %s:\n", revision)
	for _, change := range plan.Changes {
		from := change.FromRevision
		if from == "" {
			from = "unlocked"
		}
		fromDigest := change.FromSHA256
		if len(fromDigest) > 12 {
			fromDigest = fromDigest[:12]
		}
		toDigest := change.ToSHA256
		if len(toDigest) > 12 {
			toDigest = toDigest[:12]
		}
		fmt.Printf("  %s  %s (%s) -> %s (%s)\n", change.ID, from, fromDigest, change.ToRevision, toDigest)
	}
	managedDiff, err := plan.ManagedInstructionsDiff()
	if err != nil {
		return 1, fmt.Errorf("prepare managed-instructions preview: %w", err)
	}
	if managedDiff != "" {
		fmt.Print(managedDiff)
	}
	if !approve {
		if !isTTY() {
			fmt.Println("Preview only; no project files changed. Re-run with --yes to approve this update.")
			return 0, nil
		}
		fmt.Print("Apply these selected project updates? [y/N] ")
		reply, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if !strings.EqualFold(strings.TrimSpace(reply), "y") && !strings.EqualFold(strings.TrimSpace(reply), "yes") {
			fmt.Println("No changes applied.")
			return 0, nil
		}
	}
	if err := plan.Apply(justcode.DefaultFS); err != nil {
		return 1, err
	}
	fmt.Printf("Mis à jour de %d skills de projet et de leurs instructions gérées.\n", len(plan.Changes))
	fmt.Println("Guest packages and runtime images are not changed or recreated by this command.")
	return 0, nil
}
