package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	lipgloss "charm.land/lipgloss/v2"
	lipglosstable "charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/x/ansi"
	"github.com/etalab-ia/just-code/internal/justcode"
	"golang.org/x/term"
)

// huh_ui.go renders the interactive surfaces with charmbracelet/huh forms
// (issue #133). The line-based prompts in setup.go/init.go remain the
// non-interactive contract: they answer from a pipe, a script, or a test
// with the same semantics. The routing seam is wizardHuhFn — tests stub it
// to false so the scripted-line tests keep exercising the line path, the
// same pattern as stdinIsTTYFn.

// wizardHuhFn decides which wizard surface runs. Production answers whether
// both stdin and stdout are terminals; a piped stdin (CI, scripts, tests)
// must keep the line path. Tests stub it to false.
var wizardHuhFn = func() bool { return !wizardNoColor && isTTY() && stdoutIsTTY() }

// doctorRendererFn decides whether the assembled diagnostics use the styled
// TUI table. It deliberately does not depend on stdin: a diagnostic must be
// readable when the command is run non-interactively while stdout is a real
// terminal. Tests stub it to force the plain path.
var doctorRendererFn = func() bool { return !wizardNoColor && os.Getenv("TERM") != "dumb" && stdoutIsTTY() }

// wizardNoColor is set by setup --no-color: the huh renderer styles its
// output, so the flag keeps the plain line renderer even on a terminal.
var wizardNoColor bool

// huhRun runs a form with its renderer bound to stdout. The routing check
// (wizardHuhFn) requires stdout to be a terminal, but huh's default
// renderer writes to stderr; binding the renderer to stdout keeps the
// check and the output on the same stream, so `2>file` cannot hide the
// form while it waits for input.
// It is a package variable so tests can capture the form instead of
// rendering it: which fields a project gets is the testable part, and it
// needs no terminal.
var huhRun = func(form *huh.Form) error {
	return form.WithProgramOptions(tea.WithOutput(os.Stdout)).Run()
}

// huhApplyKeyFilter adds the recap's local `d` binding without discarding
// stdout routing or any other program options installed by the runner.
func huhApplyKeyFilter(instructionsChanged bool, showDiff *bool) tea.ProgramOption {
	return tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
		key, ok := msg.(tea.KeyPressMsg)
		if ok && key.String() == "d" && instructionsChanged {
			*showDiff = !*showDiff
			return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
		}
		return msg
	})
}

var huhAccessibleModeFn = func() bool { return os.Getenv("TERM") == "dumb" }

var printInitReviewFn = func(plan justcode.InitPlan) { fmt.Print(justcode.FormatInteractiveInitReview(plan)) }
var printInitInstructionsDiffFn = func(plan justcode.InitPlan) { fmt.Print(justcode.FormatInitInstructionsDiff(plan)) }

// huhApplyRun keeps the program options installed by the recap, including its
// `d` filter. Replacing the options here would silently discard that binding.
var huhApplyRun = func(form *huh.Form) error { return form.Run() }

// huhApplyAccessiblePrompt reads a numeric choice without Huh's accessible
// default-on-EOF behavior. EOF without an answer is never consent, including
// after an invalid answer; an explicit choice without a final newline is valid.
func huhApplyAccessiblePrompt(options []huh.Option[string], in *bufio.Reader) (string, error) {
	for i, option := range options {
		fmt.Printf("%d. %s\n", i+1, option.Key)
	}
	for {
		fmt.Printf("Entrez un nombre entre 1 et %d : ", len(options))
		answer, err := in.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		answer = strings.TrimSpace(answer)
		if answer == "" {
			if errors.Is(err, io.EOF) {
				return "", huh.ErrUserAborted
			}
			return "apply", nil
		}
		if index, parseErr := strconv.Atoi(answer); parseErr == nil && index >= 1 && index <= len(options) {
			return options[index-1].Value, nil
		}
		if errors.Is(err, io.EOF) {
			return "", huh.ErrUserAborted
		}
		fmt.Printf("Réponse non reconnue : entrez un nombre entre 1 et %d.\n", len(options))
	}
}

// huhApplyAccessible uses a numbered diff option because Huh's accessible
// runner does not execute Bubble Tea key filters.
func huhApplyAccessible(plan justcode.InitPlan, showDiff *bool) (string, error) {
	in := bufio.NewReaderSize(os.Stdin, 1)
	for {
		if plan.InstructionsChanged && *showDiff {
			printInitInstructionsDiffFn(plan)
		}
		options := []huh.Option[string]{
			huh.NewOption("Appliquer", "apply"),
			huh.NewOption("Modifier (retour à l'étape 1)", "edit"),
			huh.NewOption("Réglages avancés", "advanced"),
			huh.NewOption("Quitter sans rien enregistrer", "cancel"),
		}
		if plan.InstructionsChanged {
			options = append(options, huh.NewOption("Afficher ou masquer le détail d'AGENTS.md", "diff"))
		}
		choice, err := huhApplyAccessiblePrompt(options, in)
		if err != nil {
			return "", err
		}
		if choice != "diff" {
			return choice, nil
		}
		*showDiff = !*showDiff
	}
}

// The init wizard's MultiSelects carry their selection keys in the
// description: huh toggles with space or x and Enter only confirms the list,
// and nothing else on the screen says so. Testers completed init believing
// Enter had selected the highlighted item, and the project was created with
// nothing (#156).
const multiSelectKeyHint = "Space or x toggles an item; Enter confirms the list; / filters."

// resizeCursorMultiSelect compensates for huh 2.0.3 resizing a MultiSelect's
// viewport without bringing its current option back into view. Non-text
// navigation restores the same row without modifying an active filter.
type resizeCursorMultiSelect struct {
	*huh.MultiSelect[string]
}

func (m *resizeCursorMultiSelect) Update(msg tea.Msg) (huh.Model, tea.Cmd) {
	_, cmd := m.MultiSelect.Update(msg)
	if _, resizing := msg.(tea.WindowSizeMsg); resizing {
		if _, ok := m.Hovered(); !ok {
			return m, cmd
		}
		atTop := multiSelectCursorOnFirstVisibleOption(ansi.Strip(m.MultiSelect.View()))
		_, _ = m.MultiSelect.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
		_, _ = m.MultiSelect.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
		if atTop {
			_, _ = m.MultiSelect.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
		}
		return m, cmd
	}
	return m, cmd
}

func multiSelectCursorOnFirstVisibleOption(view string) bool {
	for _, line := range strings.Split(view, "\n") {
		if !strings.Contains(line, "•") && !strings.Contains(line, "✓") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "┃"))
		return strings.HasPrefix(line, ">")
	}
	return false
}

func keepMultiSelectCursorOnResize(field *huh.MultiSelect[string]) huh.Field {
	return &resizeCursorMultiSelect{MultiSelect: field}
}

var initSkillDescriptions = map[string]string{
	"official/securite-developpement": "règles de sécurité de l'État, issues des guides ANSSI",
	"official/anssi-guides":           "trouver le bon guide ANSSI et le citer",
	"official/rgaa":                   "accessibilité numérique (RGAA)",
	"official/react-dsfr":             "interfaces React au Design System de l'État (DSFR)",
	"official/lasuite-ui-kit":         "interfaces des applications La Suite (Docs, Drive…)",
	"official/datagouv-apis":          "utiliser les API de data.gouv.fr",
}

var initSkillOrder = []string{
	"official/securite-developpement", "official/anssi-guides", "official/rgaa",
	"official/react-dsfr", "official/lasuite-ui-kit", "official/datagouv-apis",
}

func initSkillOptions(catalogue []justcode.ProjectSkill) []huh.Option[string] {
	known := make(map[string]bool, len(catalogue))
	for _, skill := range catalogue {
		known[skill.ID] = true
	}
	options := make([]huh.Option[string], 0, len(initSkillOrder))
	for _, id := range initSkillOrder {
		if known[id] {
			options = append(options, huh.NewOption(strings.TrimPrefix(id, "official/")+"   "+initSkillDescriptions[id], id))
		}
	}
	return options
}

func initSkillInCatalogue(catalogue []justcode.ProjectSkill, id string) bool {
	for _, skill := range catalogue {
		if skill.ID == id {
			return true
		}
	}
	return false
}

func retainedInitSkillIDs(selected []string, options []huh.Option[string]) []string {
	offered := make(map[string]bool, len(options))
	for _, option := range options {
		offered[option.Value] = true
	}
	var retained []string
	for _, id := range selected {
		if !offered[id] {
			retained = append(retained, id)
		}
	}
	return retained
}

func retainedSkillOptions(options []huh.Option[string], selected []string) []huh.Option[string] {
	retained := append([]huh.Option[string](nil), options...)
	seen := make(map[string]bool, len(options)+len(selected))
	for _, option := range options {
		seen[option.Value] = true
	}
	for _, id := range selected {
		if !seen[id] {
			retained = append(retained, huh.NewOption(id+" (déjà configurée, non proposée)", id))
			seen[id] = true
		}
	}
	return retained
}

func initSkillsStepPrefix(opts initOptions) string {
	for _, field := range []string{"root", "runtime", "isolation", "model", "small-model", "cpus", "memory-mb", "credential-ref", "github"} {
		if opts.Set[field] {
			return ""
		}
	}
	return "[1/3] "
}

func newProjectSkillsMultiSelect(skillOptions []huh.Option[string], value *[]string, stepPrefix string) *huh.MultiSelect[string] {
	return huh.NewMultiSelect[string]().
		TitleFunc(func() string { return initSkillsTitle(len(*value), stepPrefix) }, value).
		Description("Une skill apporte à l'agent un savoir-faire de l'État : règles, références, exemples. Il s'en sert quand votre demande s'y rapporte. " + multiSelectKeyHint).
		Height(len(skillOptions) + 10).
		Filterable(true).
		Options(skillOptions...).
		Value(value)
}

func initSkillsTitle(count int, stepPrefix string) string {
	return fmt.Sprintf("%sSkills (facultatif) — %s", stepPrefix, strings.TrimSuffix(initSkillsCountLabel(count), "."))
}

func keepEmptyInitSkillsForRetry(answers *justcode.InitAnswers) {
	answers.Skills = nil
	answers.SkillsSet = true
}

// commitInitHuhSubmission copies the submitted form values into answers.
// It matches the line wizard's "Enter keeps" semantics: a submission only
// marks a value explicit when it differs from the seeded value, so
// accepting the defaults preserves the project's existing skill pins and
// storage mode. huh rebuilds a MultiSelect's value in its own option
// order, so it compares as sets: a reordered-but-identical selection is
// not a change. Sibling choices are committed before the caller acts on
// the skills decision, so a refused empty-skills retry keeps them.
// The zero-MCP confirmation runs before the MCP and storage write-backs,
// so declining it reopens the form with those answers unchanged, like
// the line wizard. It reports whether the submission needs the
// zero-skills confirmation, and whether the user declined the zero-MCP
// confirmation.
func commitInitHuhSubmission(opts initOptions, answers *justcode.InitAnswers, skills, mcps []string, skillsLocalOnly bool, skillOptions []huh.Option[string], skipSkills bool) (bool, bool, error) {
	needsSkillsConfirm := false
	if !skipSkills {
		selectedSkills := append([]string(nil), skills...)
		answers.SkillsSet = answers.SkillsSet || !equalStringSets(answers.Skills, selectedSkills)
		answers.Skills = selectedSkills
		answers.RetainedSkills = retainedInitSkillIDs(selectedSkills, skillOptions)
		needsSkillsConfirm = len(skills) == 0
	}
	if !opts.Set["mcps"] {
		if len(mcps) == 0 {
			// Same zero-selection confirmation as the line wizard;
			// declining reopens the form with the answers unchanged.
			keep, confirmErr := confirmNoMCPsHuh()
			if confirmErr != nil {
				return false, false, confirmErr
			}
			if !keep {
				return needsSkillsConfirm, true, nil
			}
		}
		answers.MCPsSet = answers.MCPsSet || !equalStringSets(answers.MCPConnectors, mcps)
		answers.MCPConnectors = mcps
	}
	if !opts.Set["skills-storage"] {
		answers.SkillsLocalOnlySet = answers.SkillsLocalOnlySet || answers.SkillsLocalOnly != skillsLocalOnly
		answers.SkillsLocalOnly = skillsLocalOnly
	}
	return needsSkillsConfirm, false, nil
}

func newInitSkillsGroup(skillOptions []huh.Option[string], skills *[]string, skillsLocalOnly *bool, includeSkills, includeStorage bool, stepPrefix string) *huh.Group {
	fields := make([]huh.Field, 0, 2)
	if includeSkills {
		fields = append(fields, newProjectSkillsMultiSelect(skillOptions, skills, stepPrefix))
	}
	if includeStorage {
		fields = append(fields, huh.NewSelect[bool]().
			Title("Où enregistrer les skills ?").
			Description("Avec le projet : elles sont versionnées et un coéquipier qui clone le projet les retrouve. Sur ce poste uniquement : rien n'est ajouté au dépôt.").
			Options(huh.NewOption("Avec le projet", false), huh.NewOption("Sur ce poste uniquement", true)).
			Value(skillsLocalOnly))
	}
	if len(fields) == 0 {
		return nil
	}
	return huh.NewGroup(fields...).Title("Skills")
}

func newProjectMCPsMultiSelect(mcpOptions []huh.Option[string], value *[]string) huh.Field {
	return keepMultiSelectCursorOnResize(huh.NewMultiSelect[string]().
		Title("MCPs").
		Description("Curated remote services and optional guest-local browser tools. " + multiSelectKeyHint).
		Height(len(mcpOptions) + 10).
		Filterable(true).
		Options(mcpOptions...).
		Value(value))
}

// retainedMCPOptions appends non-selectable options for withdrawn MCP ids
// still referenced by the project's manifest. huh rebuilds a MultiSelect's
// value from its options on submit, so without them a legacy context7 or
// playwright entry would silently vanish and the write-back would flag a
// selection change (--replace rewriting the manifest). The label names the
// withdrawal, matching the review screen's « non repris » row.
func retainedMCPOptions(options []huh.Option[string], selected []string) []huh.Option[string] {
	retained := append([]huh.Option[string](nil), options...)
	for _, id := range selected {
		reason, removed := justcode.RemovedMCPIDs[id]
		if !removed {
			continue
		}
		retained = append(retained, huh.NewOption(id+" — "+reason, id))
	}
	return retained
}

// confirmNoMCPsHuh asks the zero-selection confirmation through huh, matching
// the line wizard's « Continuer sans MCP ? » (O/n, Enter keeps the MCP-less
// project).
func confirmNoMCPsHuh() (bool, error) {
	var keep bool
	err := huhRun(huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Aucun MCP sélectionné. Continuer sans MCP ?").
				Description("L'agent n'aura accès à aucun service distant ni outil de navigateur géré ; un MCP restera ajoutable plus tard.").
				Value(&keep),
		),
	))
	if err != nil {
		return false, err
	}
	return keep, nil
}

// stdoutIsTTY reports whether standard output is a terminal, using the same
// character-device test as isTTY.
func stdoutIsTTY() bool {
	return isTTYFile(os.Stdout)
}

// huhSecret reads one hidden secret through a huh form. It returns
// ErrUserAborted when the user cancels.
func huhSecret(title, description string) (string, error) {
	var value string
	err := huhRun(huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title(title).
				Description(description).
				EchoMode(huh.EchoModePassword).
				Value(&value),
		),
	))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

// huhConfirm asks one yes/no question. It returns ErrUserAborted when the
// user cancels.
func huhConfirm(title, description string, defaultYes bool) (bool, error) {
	var value bool = defaultYes
	err := huhRun(huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title(title).
				Description(description).
				Value(&value),
		),
	))
	if err != nil {
		return false, err
	}
	return value, nil
}

// huhApplyChoice prints the full recap to normal terminal scrollback and
// keeps only the action list in Huh. A short terminal never hides choices
// behind a long note. The d binding prints the diff without rebuilding the
// plan or probing MCPs again; it never applies the configuration.
func huhApplyChoice(plan justcode.InitPlan) (string, error) {
	choice := "apply"
	printInitReviewFn(plan)
	showDiff := false
	if huhAccessibleModeFn() {
		return huhApplyAccessible(plan, &showDiff)
	}
	for {
		previousShowDiff := showDiff
		diffHint := ""
		if plan.InstructionsChanged {
			diffHint = "d : afficher le détail des changements dans AGENTS.md"
			if showDiff {
				printInitInstructionsDiffFn(plan)
				diffHint = "d : masquer le détail des changements dans AGENTS.md"
			}
		}
		form := huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title("Appliquer cette configuration ?").
					Description(diffHint).
					Options(
						huh.NewOption("Appliquer", "apply"),
						huh.NewOption("Modifier (retour à l'étape 1)", "edit"),
						huh.NewOption("Réglages avancés", "advanced"),
						huh.NewOption("Quitter sans rien enregistrer", "cancel"),
					).
					Value(&choice),
			),
		).WithWidth(huhReviewTableWidth()).WithProgramOptions(
			huhApplyKeyFilter(plan.InstructionsChanged, &showDiff),
			tea.WithOutput(os.Stdout),
		)
		err := huhApplyRun(form)
		if showDiff != previousShowDiff {
			continue
		}
		if err != nil {
			return "", err
		}
		return choice, nil
	}
}

func initModelDisplayName(model string) string {
	if model == "" {
		return "modèle par défaut de setup"
	}
	id := canonicalAlbertModelRef(model)
	short := strings.TrimPrefix(id, "albert/")
	for _, choice := range justcode.CuratedAlbertModels() {
		if choice.OpenCodeID == short {
			return choice.Name
		}
	}
	return id
}

func initSmallModelDisplayName(answers justcode.InitAnswers) string {
	if answers.SmallModel != "" {
		return initModelDisplayName(answers.SmallModel)
	}
	return "même modèle que le modèle principal (" + initModelDisplayName(answers.Model) + ")"
}

func newInitSmallModelSelect(root string, value *string) *huh.Select[string] {
	options := []huh.Option[string]{
		huh.NewOption("Même modèle que le modèle principal (défaut)", ""),
		huh.NewOption("Ministral 3 8B — petit modèle généraliste", justcode.MinistralSmallModelRef),
	}
	if *value != "" && *value != justcode.MinistralSmallModelRef {
		options = append(options, huh.NewOption(initModelDisplayName(*value)+" (déjà configuré)", *value))
	}
	return huh.NewSelect[string]().
		Title("Petit modèle").
		Description("Utilisé pour générer les titres de session. Le choix par défaut suit toujours le modèle principal. Ministral nécessite une connexion à Albert API.").
		Options(options...).
		Validate(func(s string) error { _, err := validateInitSmallModelInput(root, s); return err }).
		Value(value)
}

var initAlbertConnectedFn = func() (bool, error) {
	settings, err := resolveProviderSettings()
	if err != nil {
		return false, err
	}
	providers, err := justcode.ResolveProviders(settings)
	if err != nil {
		return false, err
	}
	return providersContain(providers, justcode.ProviderAlbert), nil
}

// validateInitSmallModelInput requires Albert for the curated Albert-only
// model. Empty remains valid with every provider: it follows the main model.
func validateInitSmallModelInput(root, value string) (string, error) {
	model := normalizeInitModelInput(value)
	if model == "" {
		return "", nil
	}
	if model == justcode.MinistralSmallModelRef {
		connected, err := initAlbertConnectedFn()
		if err != nil {
			return "", fmt.Errorf("cannot determine whether Albert API is connected: %w", err)
		}
		if !connected {
			return "", fmt.Errorf("Ministral 3 8B requires Albert API to be connected; run 'just-code setup' to connect it, or keep the main model")
		}
	}
	return catalogueModelWarningFn(root, model)
}

// runtimeInstallHintFn is the seam the advanced settings screen uses to flag
// a chosen runtime that is not installed on this host. Tests stub it so the
// check never depends on the machine running the test.
var runtimeInstallHintFn = justcode.RuntimeInstallCheck

// Each submitted editor commits one valid answer. Escape closes the editor
// or returns to the recap without discarding previously accepted edits.
func askInitAdvancedHuh(opts initOptions, answers justcode.InitAnswers) (justcode.InitAnswers, error) {
	choice := "runtime"
	notices := make(map[justcode.Runtime]string)
	modelWarning := ""
	smallModelWarning := ""
	for {
		rt := effectiveInitRuntime(answers)
		notice, checked := notices[rt]
		if !checked {
			notice = initRuntimeNotice(rt)
			notices[rt] = notice
		}
		iso := answers.Isolation
		if iso == "" {
			iso = justcode.IsolationFull
		}
		cpus := resolvedOrDefault(answers.CPUs, justcode.RecommendedDefaultGuestCPUs(opts.hostCPUs))
		memory := resolvedOrDefault(answers.MemoryMB, justcode.RecommendedDefaultGuestMemoryMB(opts.hostMemoryMB))
		var options []huh.Option[string]
		if !opts.Set["runtime"] {
			options = append(options, huh.NewOption("Type de machine isolée : "+string(rt), "runtime"))
		}
		if !opts.Set["isolation"] {
			options = append(options, huh.NewOption("Mode d'isolation : "+isolationDisplayName(iso), "isolation"))
		}
		if !opts.Set["cpus"] {
			options = append(options, huh.NewOption(fmt.Sprintf("Processeurs de la machine isolée : %d", cpus), "cpus"))
		}
		if !opts.Set["memory-mb"] {
			options = append(options, huh.NewOption("Mémoire de la machine isolée : "+justcode.FormatMemorySize(memory), "memory"))
		}
		if !opts.Set["model"] {
			options = append(options, huh.NewOption("Modèle principal : "+initModelDisplayName(answers.Model), "model"))
		}
		if !opts.Set["small-model"] {
			options = append(options, huh.NewOption("Petit modèle : "+initSmallModelDisplayName(answers), "small-model"))
		}
		options = append(options, huh.NewOption("Revenir au récapitulatif", "back"))
		if len(options) == 1 {
			choice = "back"
		}
		description := "Ces valeurs conviennent à la plupart des projets. Modifiez-les seulement si besoin.\n\n" +
			"Agent (harnais) : OpenCode (recommandé). Pi n'est pas encore pris en charge.\n" +
			"Petit modèle : sert aux titres de session.\n" +
			"Défauts : Microsandbox, isolation complète, 2 processeurs, 4 Go.\n" +
			"6 Go conseillés avec chrome-devtools.\n\n" + notice +
			"\n" + modelWarning +
			"\n" + smallModelWarning +
			"\n\n↑ ↓ : choisir un réglage. Entrée : modifier. Échap : revenir au récapitulatif."
		back, err := runInitAdvancedForm(huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().Title("Réglages avancés").Description(description).
				Options(options...).Value(&choice),
		)))
		if err != nil {
			return answers, err
		}
		if back || choice == "back" {
			return answers, nil
		}
		var field huh.Field
		var input string
		switch choice {
		case "runtime":
			input = string(rt)
			field = huh.NewSelect[string]().Title("Type de machine isolée").
				Description("Microsandbox : copie filtrée du projet (.env et secrets exclus), démarrage rapide. Tart et agent-vm : votre dossier est partagé avec la VM.").
				Options(
					huh.NewOption("Microsandbox (recommandé)", string(justcode.RuntimeMicrosandbox)),
					huh.NewOption("Tart : VM macOS complète ; dossier partagé", string(justcode.RuntimeTart)),
					huh.NewOption("agent-vm : VM Lima ; dossier partagé", string(justcode.RuntimeAgentVM)),
				).Validate(func(rt string) error {
				if answers.GitHubWorkflow && rt != string(justcode.RuntimeMicrosandbox) {
					return fmt.Errorf("le workflow GitHub du projet nécessite Microsandbox")
				}
				return nil
			}).Value(&input)
		case "isolation":
			input = string(iso)
			field = huh.NewSelect[string]().Title("Mode d'isolation").
				Description("complet : l'agent et son interface tournent dans la machine isolée. partiel : l'interface tourne sur votre poste, l'agent dans la machine isolée.").
				Options(huh.NewOption("complet (recommandé)", "full"), huh.NewOption("partiel", "backend")).Value(&input)
		case "cpus":
			input = strconv.Itoa(cpus)
			field = huh.NewInput().Title("Processeurs de la machine isolée").
				Description(fmt.Sprintf("De 1 à %d. Vide ou 'défaut' rétablit la valeur par défaut. Échap : annuler cette modification.", opts.maxCPUs)).
				Validate(func(s string) error { _, _, err := parseGuestCPUsInput(s, opts.maxCPUs); return err }).Value(&input)
		case "memory":
			input = justcode.FormatMemorySize(memory)
			field = huh.NewInput().Title("Mémoire de la machine isolée").
				Description(fmt.Sprintf("Maximum sur ce poste : %s. 6 Go conseillés avec chrome-devtools. Vide ou 'défaut' rétablit la valeur par défaut. Échap : annuler cette modification.", justcode.FormatMemorySize(opts.maxMemoryMB))).
				Validate(func(s string) error { _, _, err := parseGuestMemoryInput(s, opts.maxMemoryMB); return err }).Value(&input)
		case "model":
			input = answers.Model
			field = huh.NewInput().Title("Modèle principal").
				Description("Identifiant du modèle Albert ('just-code models' liste les modèles). Vide ou 'défaut' reprend le modèle défini dans setup. Échap : annuler cette modification.").
				Validate(func(s string) error { _, err := validateInitModelInput(answers.Root, s); return err }).Value(&input)
		case "small-model":
			input = answers.SmallModel
			field = newInitSmallModelSelect(answers.Root, &input)
		default:
			return answers, fmt.Errorf("unknown advanced setting %q", choice)
		}
		back, err = runInitAdvancedForm(huh.NewForm(huh.NewGroup(field)))
		if err != nil {
			return answers, err
		}
		if back {
			continue
		}
		switch choice {
		case "runtime":
			answers.Runtime = justcode.Runtime(input)
		case "isolation":
			answers.Isolation = justcode.Isolation(input)
		case "cpus":
			n, reset, err := parseGuestCPUsInput(input, opts.maxCPUs)
			if err != nil {
				return answers, err
			}
			if reset || n != cpus {
				answers.CPUs = n
			}
		case "memory":
			n, reset, err := parseGuestMemoryInput(input, opts.maxMemoryMB)
			if err != nil {
				return answers, err
			}
			if reset || n != memory {
				answers.MemoryMB = n
			}
		case "model":
			warning, err := validateInitModelInput(answers.Root, input)
			if err != nil {
				return answers, err
			}
			answers.Model = normalizeInitModelInput(input)
			modelWarning = warning
		case "small-model":
			warning, err := validateInitSmallModelInput(answers.Root, input)
			if err != nil {
				return answers, err
			}
			answers.SmallModel = normalizeInitModelInput(input)
			smallModelWarning = warning
		}
	}
}

func normalizeInitModelInput(value string) string {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "default") || strings.EqualFold(value, "défaut") {
		return ""
	}
	return canonicalAlbertModelRef(value)
}

func validateInitModelInput(root, value string) (string, error) {
	model := normalizeInitModelInput(value)
	if model == "" {
		return "", nil
	}
	return catalogueModelWarningFn(root, model)
}

// The filter distinguishes Escape (back) from Ctrl+C (cancel init).
var runInitAdvancedForm = func(form *huh.Form) (back bool, err error) {
	err = form.WithShowHelp(false).WithProgramOptions(tea.WithOutput(os.Stdout), tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
		if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "esc" {
			back = true
			return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
		}
		return msg
	})).Run()
	if back && err == huh.ErrUserAborted {
		err = nil
	}
	return back, err
}

func effectiveInitRuntime(answers justcode.InitAnswers) justcode.Runtime {
	if answers.Runtime == "" {
		return justcode.RuntimeMicrosandbox
	}
	return answers.Runtime
}

func initRuntimeNotice(rt justcode.Runtime) string {
	var notice string
	if rt == justcode.RuntimeTart || rt == justcode.RuntimeAgentVM {
		notice = "Avec un dossier partagé, l'agent voit tous les fichiers du projet, y compris .env.\n"
	}
	if installed, hint := runtimeInstallHintFn(rt); !installed {
		notice += fmt.Sprintf("La machine choisie (%s) n'est pas installée. Installez-la : %s", rt, hint)
	}
	return strings.TrimSpace(notice)
}

func parseGuestCPUsInput(value string, maximum int) (guestCPUs int, useDefault bool, err error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "default") || strings.EqualFold(value, "défaut") {
		return 0, true, nil
	}
	guestCPUs, err = strconv.Atoi(value)
	if err != nil || guestCPUs < 1 || guestCPUs > maximum {
		return 0, false, fmt.Errorf("entrez un nombre entier de processeurs (de 1 à %d). Maximum sur ce poste : %d processeurs", maximum, maximum)
	}
	return guestCPUs, false, nil
}

func parseGuestMemoryInput(value string, maximum int) (memoryMB int, useDefault bool, err error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "default") || strings.EqualFold(value, "défaut") {
		return 0, true, nil
	}
	memoryMB, err = justcode.ParseMemorySize(value)
	if err != nil {
		return 0, false, fmt.Errorf("format attendu : 4096, 4G ou 2.5G")
	}
	if memoryMB < 1 || memoryMB > maximum {
		return 0, false, fmt.Errorf("au moins 1 MiB. Maximum sur ce poste : %s", justcode.FormatMemorySize(maximum))
	}
	return memoryMB, false, nil
}

func huhReviewDescription(plan justcode.InitPlan) string {
	return escapeHuhNoteMarkup(justcode.FormatInteractiveInitReview(plan))
}

func huhReviewTableWidth() int {
	width := 80
	if detected, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && detected > 0 {
		width = detected - 8
	}
	if width < 48 {
		return 48
	}
	if width > 112 {
		return 112
	}
	return width
}

// huhDiagnosticsDescription renders the assembled support report as styled
// sections. Status is a label, not color alone: the same text survives in the
// accessible/no-color renderer and when a user pastes the output.
func huhDiagnosticsDescription(report justcode.DiagnosticsReport) string {
	var b strings.Builder
	for _, section := range report.Sections {
		t := lipglosstable.New().
			Headers("Check", "Status", "Value").
			Width(huhReviewTableWidth()).
			StyleFunc(func(row, column int) lipgloss.Style {
				if row == lipglosstable.HeaderRow || column == 0 {
					return lipgloss.NewStyle().Bold(true)
				}
				if column == 1 && row >= 0 && row < len(section.Items) {
					color := lipgloss.Color("244")
					switch section.Items[row].Status {
					case justcode.DiagnosticOK:
						color = lipgloss.Color("42")
					case justcode.DiagnosticWarn:
						color = lipgloss.Color("214")
					case justcode.DiagnosticFailed:
						color = lipgloss.Color("196")
					}
					return lipgloss.NewStyle().Bold(true).Foreground(color)
				}
				return lipgloss.NewStyle()
			})
		for _, item := range section.Items {
			value := item.Value
			if item.Detail != "" {
				value += " — " + item.Detail
			}
			t.Row(item.Name, string(item.Status), value)
		}
		b.WriteString(section.Title + "\n" + t.String() + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// Note descriptions support inline markdown; escape those markers in table
// data without changing the plain accessible output used by TERM=dumb.
func escapeHuhNoteMarkup(value string) string {
	if os.Getenv("TERM") == "dumb" {
		return value
	}
	return strings.NewReplacer("\\", "\\\\", "_", "\\_", "*", "\\*", "`", "\\`", "<", "\\<", ">", "\\>").Replace(value)
}

// askInitQuestionsHuh renders the init wizard as one huh form. Groups the
// command line did not supply are omitted structurally (never added to the
// form), so a partial `just-code init --runtime tart` form shows only the
// unanswered questions, matching the line path's skip semantics — and the
// omission also holds in huh's accessible runner, which ignores hide
// predicates.
func askInitQuestionsHuh(opts initOptions, answers justcode.InitAnswers) (justcode.InitAnswers, error) {
	// The catalogue and curated MCP list do not depend on the project
	// root, so they are fetched once outside the reseed loop below.
	var catalogue []justcode.ProjectSkill
	var catalogueErr error
	if !opts.Set["skills"] {
		catalogue, catalogueErr = projectSkillCatalogueFn(context.Background())
		if catalogueErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: the pinned project skills catalogue is unavailable: %v\n", catalogueErr)
		} else {
			for _, skill := range catalogue {
				answers.KnownSkills = append(answers.KnownSkills, skill.ID)
			}
		}
	}

	// A failed catalogue must not clear an existing selection: huh rebuilds
	// a MultiSelect's value from its options on submit, so an empty option
	// list would wipe skills the project already pins. Skip the field
	// instead and leave the answers untouched.
	skipSkills := shouldSkipSkillsField(opts, catalogueErr)

	skillOptions := initSkillOptions(catalogue)
	mcpOptions := make([]huh.Option[string], 0)
	for _, choice := range justcode.MCPSelections() {
		mcpOptions = append(mcpOptions, huh.NewOption(choice.ID+" — "+choice.Description, choice.ID))
	}

	// The form runs in a loop: when the user changes the project root, the
	// answers are reseeded from the target project's manifest (matching
	// the line wizard) and the form reopens with the reseeded defaults.
	for {
		// Local copies the form binds to; written back to answers on success.
		// The root field starts empty each pass: an empty submission keeps the
		// current root, matching the line wizard's default-in-prompt behavior.
		root := ""
		skills := append([]string(nil), answers.Skills...)
		mcps := append([]string(nil), answers.MCPConnectors...)
		haveManifestMCPs := false
		skillsLocalOnly := answers.SkillsLocalOnly

		if !answers.SkillsSet {
			current, err := currentProjectSkillSelection(answers.Root)
			if err != nil {
				return answers, fmt.Errorf("read current project skills: %w", err)
			}
			skills = current
			// The comparison baseline below must match what the form
			// actually shows: an unchanged submission must not look
			// like a new selection and re-resolve existing pins.
			answers.Skills = append([]string(nil), current...)
			if len(current) == 0 && !projectManifestExists(answers.Root) && initSkillInCatalogue(catalogue, "official/securite-developpement") {
				skills = []string{"official/securite-developpement"}
			}
		}
		skillOptionsPass := retainedSkillOptions(skillOptions, skills)
		if !answers.MCPsSet {
			if pc, err := justcode.DiscoverProject(answers.Root); err == nil {
				if manifest, err := justcode.ReadProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(pc.Root)); err == nil {
					mcps = append([]string(nil), manifest.MCPConnectors...)
					answers.MCPConnectors = append([]string(nil), manifest.MCPConnectors...)
					haveManifestMCPs = true
				}
			}
		}
		if len(mcps) == 0 && !haveManifestMCPs && !answers.MCPsSet {
			// Mirror the line wizard: a fresh initialization (no manifest,
			// no flag) preselects the curated default, while a manifest
			// deliberately written with zero MCPs stays empty, and so does
			// an explicitly confirmed empty selection from a prior pass.
			mcps = append([]string(nil), justcode.DefaultFreshMCPIDs...)
			answers.MCPConnectors = append([]string(nil), justcode.DefaultFreshMCPIDs...)
		}
		mcpOptionsPass := retainedMCPOptions(mcpOptions, mcps)

		var rootGroup *huh.Group
		if !opts.Set["root"] {
			rootGroup = huh.NewGroup(
				huh.NewInput().
					Title("Project root").
					Description(fmt.Sprintf("Directory the manifest and sandbox config live in. Leave empty to keep %s.", answers.Root)).
					Placeholder(answers.Root).
					Value(&root),
			).Title("Project")
		}

		// Runtime, isolation, model, resources and the credential reference
		// are expert settings: they no longer appear in the default path and
		// live behind the review's "Réglages avancés" entry (#170).

		skillsGroup := newInitSkillsGroup(skillOptionsPass, &skills, &skillsLocalOnly,
			!opts.Set["skills"] && !skipSkills, !opts.Set["skills-storage"], initSkillsStepPrefix(opts))

		mcpFields := make([]huh.Field, 0, 2)
		if !opts.Set["mcps"] {
			mcpFields = append(mcpFields, newProjectMCPsMultiSelect(mcpOptionsPass, &mcps))
		}
		var mcpGroup *huh.Group
		if len(mcpFields) > 0 {
			mcpGroup = huh.NewGroup(mcpFields...).Title("MCPs and storage")
		}

		// Groups are omitted structurally rather than via WithHideFunc:
		// huh's accessible runner (TERM=dumb) iterates every group and
		// never evaluates hide predicates, so a hidden group would still
		// prompt there and its submitted value would override the flag.
		var groups []*huh.Group
		for _, g := range []*huh.Group{rootGroup, skillsGroup, mcpGroup} {
			if g != nil {
				groups = append(groups, g)
			}
		}
		if err := huhRun(huh.NewForm(groups...)); err != nil {
			recordInitAbandon(answers.Root, "skills")
			return answers, err
		}

		// Write the form's answers back; a root change re-seeds every other
		// answer from the target project's manifest (matching the line
		// wizard) and reopens the form with the reseeded defaults.
		newRoot := strings.TrimSpace(root)
		if newRoot == "" {
			newRoot = answers.Root
		}
		rootChanged := newRoot != answers.Root
		if rootChanged {
			answers.Root = newRoot
			answers = seedInitAnswersFromManifest(opts, answers)
			// Flags keep their explicit selection across a root change;
			// only interactively seeded values are reseeded from the new
			// project's manifest.
			if !opts.Set["skills"] {
				answers.SkillsSet = false
			}
			if !opts.Set["mcps"] {
				answers.MCPsSet = false
			}
			continue
		}
		answers.Root = newRoot
		needsSkillsConfirm, retryMCPs, err := commitInitHuhSubmission(opts, &answers, skills, mcps, skillsLocalOnly, skillOptions, skipSkills)
		if err != nil {
			return answers, err
		}
		if retryMCPs {
			continue
		}
		if needsSkillsConfirm {
			proceed, err := huhConfirm("Aucune skill sélectionnée. Continuer sans skill ? (O/n)", "Appuyez sur Entrée pour continuer, ou choisissez non pour revenir à la sélection.", true)
			if err != nil {
				return answers, err
			}
			if !proceed {
				keepEmptyInitSkillsForRetry(&answers)
				continue
			}
			answers.Skills = nil
			answers.SkillsSet = true
		}
		// The GitHub step is a distinct screen, not a field of the main
		// form: it renders one of the four situations and verifies on that
		// screen (#169). It runs after the main form so the [1/3]..[3/3]
		// order on screen matches the flow, and its own screens never throw
		// away the answers already collected (#155).
		if !opts.Set["github"] {
			var err error
			answers, err = askGitHubStepHuh(answers)
			if err != nil {
				recordInitAbandon(answers.Root, "github")
				return answers, err
			}
		}
		return answers, nil
	}
}

func parsePositiveInt(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 {
		return 0
	}
	return n
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// equalStringSets reports whether two slices hold the same strings
// regardless of order, without mutating its inputs.
func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sortedA := append([]string(nil), a...)
	sortedB := append([]string(nil), b...)
	slices.Sort(sortedA)
	slices.Sort(sortedB)
	return slices.Equal(sortedA, sortedB)
}

// shouldSkipSkillsField reports whether the skills MultiSelect must be
// omitted from the form: either the flag supplied the selection, or the
// catalogue fetch failed and an empty option list would clobber the
// project's existing skills on submit.
func shouldSkipSkillsField(opts initOptions, catalogueErr error) bool {
	return opts.Set["skills"] || catalogueErr != nil
}

// askGitHubStepHuh renders the GitHub step (3.9) as its own screen with one
// distinct form per situation (#169). It verifies on that screen, so a
// refusal or a re-verify never throws away the other answers (#155).
func askGitHubStepHuh(answers justcode.InitAnswers) (justcode.InitAnswers, error) {
	for {
		probe := githubInitProbeFn(answers.Root)
		switch {
		case !gitHubAvailableForRuntime(answers.Runtime):
			// Same rule as the line wizard: Microsandbox-only capability, so
			// the screen states it rather than letting the review fail after
			// every other answer (#155).
			choice := "continue"
			err := huhRun(huh.NewForm(
				huh.NewGroup(
					huh.NewNote().
						Title("[3/3] GitHub (facultatif)").
						Description(fmt.Sprintf("Le partage de modifications par GitHub n'est pas disponible avec %s.\nIl fonctionne avec Microsandbox, qui travaille sur une copie scellée du projet. Avec Tart et agent-vm, le dossier du projet est monté dans la machine : vous récupérez les modifications directement dans votre dossier.", string(answers.Runtime))),
					huh.NewSelect[string]().
						Title("GitHub").
						Options(
							huh.NewOption("Continuer sans GitHub", "continue"),
							huh.NewOption("Quitter pour changer de type de machine", "quit"),
						).
						Value(&choice),
				),
			))
			if err != nil {
				return answers, err
			}
			if choice == "quit" {
				return answers, fmt.Errorf("vous avez quitté l'assistant ; rien n'a été écrit")
			}
			answers.GitHubWorkflow = false
			answers.GitHubRemote = justcode.GitHubRemote{}
			return answers, nil
		case !probe.HasOrigin:
			answers.GitHubCase = 2
			choice := "continue"
			err := huhRun(huh.NewForm(
				huh.NewGroup(
					huh.NewNote().
						Title("[3/3] GitHub (facultatif)").
						Description("Ce projet n'est pas relié à GitHub.\nL'agent travaille sur une copie du projet. Pour récupérer ses modifications dans votre dossier, vous les relirez puis les appliquerez avec : just-code workspace apply"),
					huh.NewSelect[string]().
						Title("Que voulez-vous faire ?").
						Options(
							huh.NewOption("Continuer", "continue"),
							huh.NewOption("Quitter, relier le projet à GitHub, puis relancer : just-code init", "quit"),
						).
						Value(&choice),
				),
			))
			if err != nil {
				return answers, err
			}
			if choice == "quit" {
				return answers, fmt.Errorf("vous avez quitté l'assistant ; rien n'a été écrit")
			}
			answers.GitHubWorkflow = false
			answers.GitHubRemote = justcode.GitHubRemote{}
			return answers, nil
		case probe.TokenError != nil:
			answers.GitHubCase = 3
			// Same rule as the line wizard: a store that cannot be read is
			// not the same situation as having no token, and its remedy is
			// different. GitHub stays optional, so offer a way out.
			choice := "continue"
			err := huhRun(huh.NewForm(
				huh.NewGroup(
					huh.NewNote().
						Title("[3/3] GitHub (facultatif)").
						Description(fmt.Sprintf("Le mot de passe de votre poste protège les identifiants, et il n'a pas pu être lu.\nTant qu'il est verrouillé, just-code ne peut pas savoir si un jeton GitHub est disponible.\n\nDétail : %v", probe.TokenError)),
					huh.NewSelect[string]().
						Title("GitHub").
						Options(
							huh.NewOption("Continuer sans GitHub", "continue"),
							huh.NewOption("Quitter pour déverrouiller, puis relancer", "quit"),
						).
						Value(&choice),
				),
			))
			if err != nil {
				return answers, err
			}
			if choice == "quit" {
				return answers, fmt.Errorf("vous avez quitté l'assistant ; rien n'a été écrit")
			}
			answers.GitHubWorkflow = false
			answers.GitHubRemote = justcode.GitHubRemote{}
			return answers, nil
		case !probe.HasToken:
			answers.GitHubCase = 3
			choice := "later"
			err := huhRun(huh.NewForm(
				huh.NewGroup(
					huh.NewNote().
						Title("[3/3] GitHub (facultatif)").
						Description(fmt.Sprintf("Dépôt détecté : %s\nAucun jeton GitHub n'est enregistré sur ce poste.", gitHubRepoDisplay(probe.Remote))),
					huh.NewSelect[string]().
						Title("Jeton GitHub").
						Options(
							huh.NewOption("Plus tard (just-code auth add github)", "later"),
							huh.NewOption("Ajouter un jeton maintenant", "add"),
						).
						Value(&choice),
				),
			))
			if err != nil {
				return answers, err
			}
			if choice == "add" {
				// The stage reads the terminal directly when the TUI renders,
				// but the same code path also serves the line prompt: give it
				// a reader so it can never dereference nil if the renderer
				// changes under it.
				stored, _, _, err := githubTokenStage(context.Background(), initGitHubAddWizard(), bufio.NewReader(os.Stdin))
				if err != nil {
					return answers, err
				}
				if stored {
					continue
				}
			}
			answers.GitHubWorkflow = false
			answers.GitHubRemote = justcode.GitHubRemote{}
			return answers, nil
		case probe.Rights == justcode.GitHubRepoRightsOK:
			answers.GitHubCase = 1
			yes := true
			err := huhRun(huh.NewForm(
				huh.NewGroup(
					huh.NewNote().
						Title("[3/3] GitHub (facultatif)").
						Description(fmt.Sprintf("Dépôt détecté : %s\nJeton GitHub : %s, droits vérifiés sur ce dépôt ✅", gitHubRepoDisplay(probe.Remote), gitHubAccountLabel(probe))),
					huh.NewConfirm().
						Title("Autoriser l'agent à pousser ses modifications sur une branche et à ouvrir une pull request en brouillon sur ce dépôt ?").
						Description("Vous gardez la main : rien n'est fusionné sans votre accord.").
						Value(&yes),
				),
			))
			if err != nil {
				return answers, err
			}
			answers.GitHubRemote = probe.Remote
			answers.GitHubWorkflow = yes
			return answers, nil
		default:
			answers.GitHubCase = 4
			choice := "continue"
			err := huhRun(huh.NewForm(
				huh.NewGroup(
					huh.NewNote().
						Title("[3/3] GitHub (facultatif)").
						Description(fmt.Sprintf("Dépôt détecté : %s\n%s", gitHubRepoDisplay(probe.Remote), gitHubRightsMessage(probe))),
					huh.NewSelect[string]().
						Title("Jeton GitHub").
						Options(
							huh.NewOption("Revérifier", "reverify"),
							huh.NewOption("Continuer sans GitHub", "continue"),
						).
						Value(&choice),
				),
			))
			if err != nil {
				return answers, err
			}
			if choice == "reverify" {
				continue
			}
			answers.GitHubWorkflow = false
			answers.GitHubRemote = justcode.GitHubRemote{}
			return answers, nil
		}
	}
}
