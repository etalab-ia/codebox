package main

import (
	"context"
	"fmt"
	"strings"

	"charm.land/huh/v2"
	"github.com/etalab-ia/just-code/internal/justcode"
)

// runWorkspaceApply implements `just-code workspace apply` (issue #173): it
// brings the agent's changes from the isolated machine back into the host
// folder. The list is shown before anything is written, every file is
// appliable on its own, and a file the host also modified is never
// overwritten — its patch is preserved for a manual application instead.
// A file already recovered once is brought to the guest's current content
// directly when the host still sits at the recovered baseline.
//
// The screen texts are the issue's, verbatim.
func runWorkspaceApply(rt *justcode.MicrosandboxRuntime, sourceDir, instance string, applyAll bool) (int, error) {
	ctx := context.Background()
	stateDir := justcode.DefaultStateDir()
	optInStore := justcode.TransferOptInStore{Path: justcode.TransferOptInPath(stateDir, instance), FS: justcode.DefaultFS}
	records, err := optInStore.Load()
	if err != nil {
		return 1, err
	}
	optIn := map[string]bool{}
	for rel := range records {
		optIn[rel] = true
	}

	review, err := justcode.PrepareApplyReview(ctx, rt, sourceDir, optIn)
	if err != nil {
		return 1, err
	}
	if len(review.Changes) == 0 {
		fmt.Println("L'agent n'a pas modifié de fichiers dans la machine isolée.")
		return 0, nil
	}
	fmt.Print(formatApplyList(review.Changes, review.Recovered, review.Refused))
	applicable := review.Applicable()
	if len(applicable) == 0 {
		fmt.Println("\nAucun fichier à appliquer.")
		return 0, nil
	}

	selected := applicable
	if !applyAll && exitIsTTYFn() {
		choice, err := workspaceApplyChoiceFn()
		if err != nil {
			// An aborted question applies nothing; the changes stay in the
			// isolated machine and the command can be run again.
			fmt.Println("\nAucun fichier appliqué.")
			return 0, nil
		}
		switch choice {
		case "all":
		case "detail":
			selected, err = workspaceApplyDetailFn(applicable)
			if err != nil {
				fmt.Println("\nAucun fichier appliqué.")
				return 0, nil
			}
			if len(selected) == 0 {
				fmt.Println("\nAucun fichier sélectionné ; rien n'a été appliqué.")
				return 0, nil
			}
		default: // "none"
			return 0, nil
		}
	}

	justcode.SortGuestChanges(selected)
	applied, conflicted, err := justcode.ApplySelectedChanges(ctx, rt, sourceDir, selected)
	if err != nil {
		return 1, err
	}
	preservePath := ""
	if len(conflicted) > 0 {
		// Synthetic changes carry no patch: preserving them would
		// write an empty entry and print a misleading "patch kept"
		// line for work the user can only redo by hand.
		patchable := make([]justcode.GuestChange, 0, len(conflicted))
		for _, c := range conflicted {
			if !c.Synthetic {
				patchable = append(patchable, c)
			}
		}
		preservePath, err = justcode.PreserveUnappliedPatches(stateDir, instance, patchable)
		if err != nil {
			return 1, err
		}
	}

	if len(applied) > 0 {
		noun, participle := "fichier", "appliqué"
		if len(applied) > 1 {
			noun, participle = "fichiers", "appliqués"
		}
		fmt.Printf("\n%d %s %s à votre dossier. Relisez-les puis commitez quand vous voulez.\n", len(applied), noun, participle)
	}
	for _, c := range conflicted {
		fmt.Printf("\n%s a aussi été modifié dans votre dossier : il n'est pas appliqué.\n", c.Rel)
	}
	if preservePath != "" {
		fmt.Printf("Le patch est conservé dans %s pour une application manuelle.\n", preservePath)
	}
	for _, c := range review.Changes {
		if reason := review.Refused[c.Rel]; reason != "" {
			fmt.Printf("\n%s n'est pas appliqué : %s.\n", c.Rel, reason)
		}
	}
	return 0, nil
}

// formatApplyList renders the file list of the apply screen (issue #173):
// one line per modified file, with its status marker and line counts, and a
// note for the files that cannot be applied.
func formatApplyList(changes []justcode.GuestChange, recovered map[string]bool, refused map[string]string) string {
	var b strings.Builder
	noun := "fichier"
	if len(changes) > 1 {
		noun = "fichiers"
	}
	fmt.Fprintf(&b, "Modifications de l'agent dans la machine isolée : %d %s\n\n", len(changes), noun)
	width := 0
	for _, c := range changes {
		if len(c.Rel) > width {
			width = len(c.Rel)
		}
	}
	for _, c := range changes {
		fmt.Fprintf(&b, "  %s %-*s %s", guestChangeMarker(c.Status), width, c.Rel, guestChangeCounts(c))
		if recovered[c.Rel] {
			b.WriteString("   (déjà récupéré)")
		} else if reason := refused[c.Rel]; reason != "" {
			b.WriteString("   (exclu : " + reason + ")")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func guestChangeMarker(status byte) string {
	switch status {
	case 'A':
		return "+"
	case 'D':
		return "-"
	default:
		return "~"
	}
}

func guestChangeCounts(c justcode.GuestChange) string {
	if c.Binary {
		return "binaire"
	}
	if c.Synthetic {
		return "rétabli"
	}
	return fmt.Sprintf("+%d −%d", c.Additions, c.Deletions)
}

// workspaceApplyChoiceFn asks the main question of the apply screen. It is a
// var so tests can drive the flow without a terminal.
var workspaceApplyChoiceFn = func() (string, error) {
	choice := "all"
	err := huhRun(huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Options(
				huh.NewOption("Appliquer tout à votre dossier", "all"),
				huh.NewOption("Voir le détail (fichier par fichier, avec choix)", "detail"),
				huh.NewOption("Ne rien appliquer", "none"),
			).Value(&choice),
	)))
	return choice, err
}

// workspaceApplyDetailFn asks the per-file question after "Voir le détail".
// Every applicable file starts selected: the screen's default is to apply
// everything, and the detail view narrows it down.
var workspaceApplyDetailFn = func(applicable []justcode.GuestChange) ([]justcode.GuestChange, error) {
	selected := make([]string, 0, len(applicable))
	options := make([]huh.Option[string], 0, len(applicable))
	for _, c := range applicable {
		options = append(options, huh.NewOption(fmt.Sprintf("%s (%s)", c.Rel, guestChangeCounts(c)), c.Rel))
		selected = append(selected, c.Rel)
	}
	err := huhRun(huh.NewForm(huh.NewGroup(
		keepMultiSelectCursorOnResize(huh.NewMultiSelect[string]().Title("Fichiers à appliquer").Height(len(options) + 10).Options(options...).Value(&selected)),
	)))
	if err != nil {
		return nil, err
	}
	var out []justcode.GuestChange
	for _, rel := range selected {
		for _, c := range applicable {
			if c.Rel == rel {
				out = append(out, c)
				break
			}
		}
	}
	return out, nil
}
