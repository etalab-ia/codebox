package codebox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"
)

// LaunchUX is the state for the first-launch experience of issue #172.
//
// The contract is one sentence per phase, in French, with the technical detail
// kept out of the terminal: someone who imported a project from Albert Code
// needs to know that the machine is running and why it took a minute, not to
// read a transfer log. Two things implement that split:
//
//   - Quiet suppresses the verbose lines the runtime and the workspace transfer
//     print on their own, because the checklist below replaces them. It is
//     driven by isatty(stderr), the same rule as the wizard: a pipe or a CI log
//     still gets everything.
//   - the report records what actually happened (first start versus cached,
//     files copied and excluded, skills, MCP, model) so the CLI renders one
//     checklist from real state rather than from assumptions.
//
// A nil *LaunchUX is valid and means the legacy behaviour: verbose, no summary.
type LaunchUX struct {
	// Terminal reports that this launch is driving a real terminal, which is
	// where the concise French checklist belongs. On a pipe or in a CI log the
	// verbose per-phase output is left in place instead: nobody is reading a
	// summary there and a log wants the detail.
	Terminal bool

	// ProjectName is the basename of the project root.
	ProjectName string
	// AgentRuntime names the agent in the readiness line ("OpenCode").
	AgentRuntime string
	TimingStore  *LaunchTimingStore
	TimingKey    string

	firstStart        bool
	timingUnavailable bool

	// copiedFiles is how many files landed in the machine; dotenvExcluded and
	// secretExcluded count what the default-deny filter refused.
	copiedFiles    int
	dotenvExcluded int
	secretExcluded int
	// secretsUnscanned records that the secret detector did not run, so the
	// copy line can say so instead of implying a check that never happened.
	secretsUnscanned bool
	transferDone     bool

	skillNames []string
	skillsDone bool

	mcpNames []string
	mcpDone  bool

	// modelDisplay is the human-facing model name ("Qwen3 Coder").
	modelDisplay string

	// warnings carries at most one concise line per distinct cause, in place of
	// the several blocks the first launch used to produce.
	warnings []string
}

// NewLaunchUX builds the report for a launch of projectRoot.
func NewLaunchUX(projectRoot, agentRuntime string) *LaunchUX {
	return &LaunchUX{
		Terminal:     term.IsTerminal(int(os.Stderr.Fd())),
		ProjectName:  filepath.Base(projectRoot),
		AgentRuntime: agentRuntime,
	}
}

// RecordFirstStart marks that the toolchain had to be installed.
func (u *LaunchUX) RecordFirstStart() {
	if u == nil {
		return
	}
	u.firstStart = true
}

// IsFirstStart reports whether the toolchain installation ran during this
// launch. Callers use it to set the expectation before the slow path begins.
func (u *LaunchUX) IsFirstStart() bool {
	return u != nil && u.firstStart
}

// RecordTransfer takes the counts from a workspace manifest: what landed, and
// what the filters refused.
func (u *LaunchUX) RecordTransfer(manifest TransferManifest) {
	if u == nil {
		return
	}
	u.copiedFiles = len(manifest.Included())
	u.dotenvExcluded = manifest.ExclusionCount(TransferExclusionDotenv)
	u.secretExcluded = manifest.ExclusionCount(TransferExclusionSecret)
	u.secretsUnscanned = manifest.GitleaksMissing
	u.transferDone = true
	if u.secretsUnscanned {
		// The copy line cannot say "and nothing looked like a secret" when
		// nothing checked. Say what did not happen, without naming the tool:
		// the user decides about secrets, not about a scanner.
		u.AddWarning("les fichiers du projet n'ont pas pu être vérifiés : aucun secret recherché. Détail dans codebox doctor.")
	}
}

// RecordSkills stores the project skills that are available in the machine.
func (u *LaunchUX) RecordSkills(names []string) {
	if u == nil {
		return
	}
	u.skillNames = append([]string(nil), names...)
	u.skillsDone = true
}

// RecordMCP stores the managed MCP servers selected for this project.
func (u *LaunchUX) RecordMCP(names []string) {
	if u == nil {
		return
	}
	u.mcpNames = append([]string(nil), names...)
	u.mcpDone = true
}

// RecordModel stores the display name of the model the agent will run on.
func (u *LaunchUX) RecordModel(display string) {
	if u == nil {
		return
	}
	u.modelDisplay = display
}

// AddWarning records an attention line. Keep each one to a phrase: several
// causes produce several short lines, never a block of prose.
func (u *LaunchUX) AddWarning(format string, args ...any) {
	if u == nil {
		return
	}
	line := strings.TrimSpace(fmt.Sprintf(format, args...))
	if line == "" {
		return
	}
	for _, prior := range u.warnings {
		if prior == line {
			return
		}
	}
	u.warnings = append(u.warnings, line)
}

// HasWarnings reports whether the checklist should carry attention lines.
func (u *LaunchUX) HasWarnings() bool {
	return u != nil && len(u.warnings) > 0
}

// Warnings returns the recorded warning lines in order.
func (u *LaunchUX) Warnings() []string {
	if u == nil {
		return nil
	}
	return append([]string(nil), u.warnings...)
}

// Say writes a phase line. Nothing is written on a terminal: the checklist
// rendered at the end of the launch carries the same information, and the
// phases in between stay silent so the terminal does not fill with English.
func (u *LaunchUX) Say(format string, args ...any) {
	if u == nil || u.Terminal {
		return
	}
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// LaunchLine is the sentence printed before the machine starts, and sets the
// expectation for how long it will take. A cached start is quick and must not
// be announced as an installation.
func (u *LaunchUX) LaunchLine() string {
	if u.IsFirstStart() {
		return "  Premier démarrage : installation des outils, " + LaunchDurationPhrase(u.TimingStore, u.TimingKey, LaunchCold) + "."
	}
	return "  Démarrage : " + LaunchDurationPhrase(u.TimingStore, u.TimingKey, LaunchWarm) + "."
}

// Observe records the complete successful runtime start, including reconciliation.
func (u *LaunchUX) Observe(elapsed time.Duration) {
	if u == nil || u.TimingStore == nil || u.timingUnavailable {
		return
	}
	kind := LaunchWarm
	if u.IsFirstStart() {
		kind = LaunchCold
	}
	if err := u.TimingStore.Observe(u.TimingKey, kind, elapsed); err != nil {
		u.AddWarning("durée non enregistrée ; voir les droits du répertoire d'état")
	}
}

// AnnounceFirstStart records that the toolchain has to be installed and prints
// the expectation for it. It has to be said before the slow path begins, which
// is why the runtime calls it rather than the CLI summarising afterwards.
func (u *LaunchUX) AnnounceFirstStart() {
	if u == nil {
		return
	}
	u.firstStart = true
	if !u.Terminal {
		return
	}
	fmt.Fprintln(os.Stderr, u.LaunchLine())
}

// AnnounceWarmStart prints the expectation for a start that reuses an
// installed toolchain. It must not promise an installation that is not coming.
func (u *LaunchUX) AnnounceWarmStart() {
	if u == nil || !u.Terminal {
		return
	}
	fmt.Fprintln(os.Stderr, u.LaunchLine())
}

// Checklist is the French launch summary. Lines appear only for phases that
// actually ran, so a partial or failed launch never reports more than it did.
func (u *LaunchUX) Checklist() []string {
	if u == nil {
		return nil
	}
	var lines []string
	if u.transferDone {
		lines = append(lines, "  ✓ "+u.transferLine())
	}
	if u.skillsDone {
		if len(u.skillNames) > 0 {
			lines = append(lines, "  ✓ Skills : "+strings.Join(u.skillNames, ", "))
		} else {
			lines = append(lines, "  ✓ Skills : aucune")
		}
	}
	if u.mcpDone {
		if len(u.mcpNames) > 0 {
			lines = append(lines, "  ✓ MCP : "+strings.Join(u.mcpNames, ", "))
		} else {
			lines = append(lines, "  ✓ MCP : aucun")
		}
	}
	lines = append(lines, "  ✓ "+u.agentLine())
	if len(u.warnings) > 0 {
		lines = append(lines, "  ⚠ "+strings.Join(u.warnings, " ; "))
	}
	return lines
}

func (u *LaunchUX) agentLine() string {
	switch {
	case u.AgentRuntime != "" && u.modelDisplay != "":
		return "Agent prêt (" + u.AgentRuntime + ", " + u.modelDisplay + ")"
	case u.AgentRuntime != "":
		return "Agent prêt (" + u.AgentRuntime + ")"
	case u.modelDisplay != "":
		return "Agent prêt (" + u.modelDisplay + ")"
	default:
		return "Agent prêt"
	}
}

// transferLine describes the copied project and, in one phrase, what stayed
// behind. The count of excluded files is given without naming the scanner: a
// user decides about secrets, not about gitleaks.
func (u *LaunchUX) transferLine() string {
	var excluded []string
	if u.dotenvExcluded > 0 {
		excluded = append(excluded, fmt.Sprintf("%d fichiers .env", u.dotenvExcluded))
	}
	if u.secretExcluded > 0 {
		excluded = append(excluded, fmt.Sprintf("%d fichiers sensibles", u.secretExcluded))
	}
	if len(excluded) == 0 {
		return fmt.Sprintf("Copie du projet (%d fichiers)", u.copiedFiles)
	}
	return fmt.Sprintf("Copie du projet (%d fichiers ; %s exclus)", u.copiedFiles, strings.Join(excluded, " et "))
}

// SecretsUnscanned reports whether the secret detector failed to run during the
// transfer, which is a fact the user must be told rather than left to infer.
func (u *LaunchUX) SecretsUnscanned() bool {
	return u != nil && u.secretsUnscanned
}
