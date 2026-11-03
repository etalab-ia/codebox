package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"charm.land/huh/v2"
	"github.com/etalab-ia/just-code/internal/justcode"
)

// exitIsTTYFn is the terminal probe for the exit screen and the apply flow.
// It is a var so tests can drive the interactive branches without a terminal,
// the same pattern as stdinIsTTYFn.
var exitIsTTYFn = isTTY

// exitScreen closes an agent session (issue #174). It replaces the old
// "Stop the runtime?" question with three blocks: the agent's unrecovered
// work and how to recover it, the stop question with the cost of each
// answer, and the resume command. The word "runtime" never appears: the user
// manages a session and a machine, not an implementation detail.
func exitScreen(ctx context.Context, d *justcode.Dispatcher, cfg justcode.Config, rt justcode.Runtime, instance string) {
	b, err := d.Backend(rt)
	if err != nil {
		return
	}
	running := false
	if b != nil {
		if r, err := b.IsRunning(ctx); err == nil {
			running = r
		}
	}
	fmt.Println("\nSession terminée.")
	if !running {
		fmt.Println("Pour reprendre cette session : just-code")
		return
	}
	// Only the sealed microsandbox guest holds work worth recovering; the
	// other runtimes get the stop question without the recovery block.
	if msb, ok := b.(*justcode.MicrosandboxRuntime); ok {
		exitRecoveryBlock(ctx, msb, instance, cfg.WorkspaceDir)
	}
	exitStopQuestion(ctx, b, cfg, rt, d)
	fmt.Println("Pour reprendre cette session : just-code")
}

// guestBranchLine renders the branch report of the exit screen (#306). It
// returns "" when there is nothing to report. The states stay distinct:
// pushed with a draft or ready PR, pushed with an UNVERIFIED PR state (gh or
// network unavailable is not "no PR"), pushed without a PR, and a branch
// created but never pushed.
func guestBranchLine(status justcode.GuestBranchStatus) string {
	switch {
	case status.Pushed && status.PRURL != "" && status.PRDraft:
		return fmt.Sprintf("Branche poussée : %s, pull request en brouillon : %s", status.Branch, status.PRURL)
	case status.Pushed && status.PRURL != "":
		return fmt.Sprintf("Branche poussée : %s, pull request : %s", status.Branch, status.PRURL)
	case status.Pushed && !status.PRKnown:
		return fmt.Sprintf("Branche poussée : %s (pull request non vérifiée : gh ou réseau indisponible)", status.Branch)
	case status.Pushed:
		return fmt.Sprintf("Branche poussée : %s", status.Branch)
	case status.Branch != "":
		return fmt.Sprintf("Branche créée dans la machine isolée mais non poussée : %s", status.Branch)
	default:
		return ""
	}
}

// exitRecoveryBlock reports the agent's pushed branch and unrecovered work,
// and offers to run the recovery command (issue #174). It prints nothing
// when there is nothing to report. A branch read failure is reported on
// stderr, never silently read as "nothing pushed" (#306).
func exitRecoveryBlock(ctx context.Context, m *justcode.MicrosandboxRuntime, instance, sourceDir string) {
	status, err := m.GuestBranchStatus(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Impossible de lire les branches de la machine isolée ; l'état du push et de la pull request n'a pas pu être vérifié.")
	} else if line := guestBranchLine(status); line != "" {
		fmt.Println(line)
	}
	// A legacy guest has no recovery base: a clean tree there does NOT mean
	// all work is recovered, because committed guest work is invisible to
	// the legacy diff (#313). Say so instead of implying a full recovery.
	if warning, werr := m.GuestRecoveryWarning(ctx); werr == nil && warning != "" {
		fmt.Fprintln(os.Stderr, warning)
	}
	unrecovered, err := justcode.UnrecoveredGuestChanges(ctx, m)
	if err != nil {
		// A read failure must not read as "nothing to recover": the work is
		// still in the isolated machine, and only the reading failed.
		fmt.Fprintln(os.Stderr, "Impossible de lire les modifications de la machine isolée ; lancez 'just-code workspace apply' pour les vérifier.")
		return
	}
	if len(unrecovered) == 0 {
		return
	}
	noun, agree := "fichier", ""
	if len(unrecovered) > 1 {
		noun, agree = "fichiers", "s"
	}
	if !exitIsTTYFn() {
		fmt.Printf("L'agent a modifié %d %s dans la machine isolée, non récupéré%s dans votre dossier. Lancez 'just-code workspace apply' pour les récupérer.\n", len(unrecovered), noun, agree)
		return
	}
	choice, err := exitRecoveryChoiceFn(len(unrecovered))
	if err != nil || choice != "apply" {
		return
	}
	if _, err := runWorkspaceApply(m, sourceDir, instance, false); err != nil {
		fmt.Fprintf(os.Stderr, "La récupération a échoué : %v\n", err)
	}
}

// exitRecoveryChoiceFn asks the recovery question (issue #174, verbatim
// options). It is a var so tests can drive the flow without a terminal.
var exitRecoveryChoiceFn = func(count int) (string, error) {
	choice := "later"
	noun, agree := "fichier", ""
	if count > 1 {
		noun, agree = "fichiers", "s"
	}
	err := huhRun(huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title(fmt.Sprintf("L'agent a modifié %d %s dans la machine isolée, non récupéré%s dans votre dossier.", count, noun, agree)).
			Options(
				huh.NewOption("Les récupérer maintenant (just-code workspace apply)", "apply"),
				huh.NewOption("Plus tard", "later"),
			).Value(&choice),
	)))
	return choice, err
}

// exitStopQuestion asks whether to stop the isolated machine, printing the
// cost of each answer (issue #174). The default keeps it running: the resume
// is immediate, at the price of the reserved memory.
func exitStopQuestion(ctx context.Context, b justcode.Backend, cfg justcode.Config, rt justcode.Runtime, d *justcode.Dispatcher) {
	if !exitIsTTYFn() {
		fmt.Println("La machine isolée reste démarrée ; lancez 'just-code stop' pour l'arrêter.")
		return
	}
	keep := "Non (recommandé) : la reprise est immédiate"
	if mem := reservedMemoryPhrase(cfg, rt); mem != "" {
		keep += " ; elle garde " + mem + " de mémoire réservés"
	}
	keep += "."
	stop := "Oui : la mémoire est libérée"
	if dur := resumeDurationPhrase(rt, d.Launch); dur != "" {
		stop += " ; la prochaine reprise prend " + dur
	}
	stop += "."
	fmt.Println("\nArrêter la machine isolée ?")
	fmt.Println("  " + keep)
	fmt.Println("  " + stop)
	switch strings.ToLower(strings.TrimSpace(exitStopPromptFn())) {
	case "o", "oui", "y", "yes":
		if err := b.Stop(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "L'arrêt a échoué : %v\n", err)
			return
		}
		fmt.Println("La machine isolée est arrêtée.")
	default:
		fmt.Println("La machine isolée reste démarrée.")
	}
}

// exitStopPromptFn prints the (o/N) prompt and reads the answer. It is a var
// so tests can answer without a terminal.
var exitStopPromptFn = func() string {
	fmt.Print("(o/N) ")
	reply, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return reply
}

// reservedMemoryPhrase names the memory the isolated machine keeps reserved
// while it stays running, or "" when the runtime does not report one.
func reservedMemoryPhrase(cfg justcode.Config, rt justcode.Runtime) string {
	switch rt {
	case justcode.RuntimeMicrosandbox:
		return formatMemoryMB(cfg.MemoryMB)
	case justcode.RuntimeAgentVM:
		return fmt.Sprintf("%d Go", cfg.AgentVMMemoryGB)
	}
	return ""
}

// formatMemoryMB renders a megabyte figure the way the wizard writes it in
// French: whole gibibytes as "4 Go", fractions with a French decimal comma.
func formatMemoryMB(mb int) string {
	if mb <= 0 {
		return ""
	}
	if mb%1024 == 0 {
		return fmt.Sprintf("%d Go", mb/1024)
	}
	frac := (mb % 1024) * 10 / 1024
	if frac == 0 {
		return fmt.Sprintf("%d Go", mb/1024)
	}
	return fmt.Sprintf("%d,%d Go", mb/1024, frac)
}

// resumeDurationPhrase names the cost of the next cold resume, or "" when
// this runtime has none to announce. A microsandbox resume that this machine
// has not measured yet uses the issue's default figure rather than
// pretending to know better. launch is nil when attaching to an
// already-running machine, which is exactly the unmeasured case.
func resumeDurationPhrase(rt justcode.Runtime, launch *justcode.LaunchUX) string {
	if rt != justcode.RuntimeMicrosandbox {
		return ""
	}
	if launch == nil || launch.TimingKey == "" || launch.TimingStore == nil {
		return "environ 30 secondes"
	}
	if _, _, ok := launch.TimingStore.Observation(launch.TimingKey, justcode.LaunchWarm); !ok {
		return "environ 30 secondes"
	}
	return justcode.LaunchDurationPhrase(launch.TimingStore, launch.TimingKey, justcode.LaunchWarm)
}
