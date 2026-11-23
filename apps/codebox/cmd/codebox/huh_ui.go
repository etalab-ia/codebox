package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"reflect"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"unsafe"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	lipgloss "charm.land/lipgloss/v2"
	lipglosstable "charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/x/ansi"
	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
	"golang.org/x/term"
)

// huh_ui.go renders the interactive surfaces with charmbracelet/huh forms
// (issue #133). The legacy setup and project-wizard helpers retain line prompts
// for their explicit compatibility paths and tests. The combined first run is
// Huh-only: scripted initialization uses flags plus --yes instead of a second
// line renderer (#461).
//
// wizardHuhFn decides which legacy wizard surface runs. Tests stub it to false;
// the combined entry point rejects that mode rather than reading piped answers.
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
	return form.WithTheme(codeboxTheme{}).WithKeyMap(frenchKeyMap()).
		WithProgramOptions(tea.WithOutput(os.Stdout)).Run()
}

// codeboxTheme is the single design shared by every interactive screen
// (#445): DSFR blue for titles and selectors, green for checked options,
// and explicit [ ]/[x] checkboxes so a multi-select cannot be mistaken
// for a plain list. The prefixes are theme-only overrides — huh renders
// SelectedPrefix.String()/UnselectedPrefix.String() directly, so no fork
// or custom field is needed.
type codeboxTheme struct{}

func (codeboxTheme) Theme(isDark bool) *huh.Styles {
	styles := huh.ThemeCharm(isDark)
	blue := lipgloss.Color("#6A6AF4")
	muted := lipgloss.Color("#9C9C9C")
	green := lipgloss.Color("#00A95F")

	styles.Group.Title = styles.Group.Title.Foreground(blue).Bold(true)
	styles.Focused.Title = styles.Focused.Title.Foreground(blue).Bold(true)
	styles.Focused.NoteTitle = styles.Focused.NoteTitle.Foreground(blue).Bold(true)
	styles.Focused.Description = styles.Focused.Description.Foreground(muted)
	styles.Focused.SelectSelector = styles.Focused.SelectSelector.Foreground(blue).Bold(true)
	styles.Focused.MultiSelectSelector = styles.Focused.MultiSelectSelector.Foreground(blue).Bold(true)
	styles.Focused.SelectedOption = styles.Focused.SelectedOption.Foreground(green)
	styles.Focused.SelectedPrefix = styles.Focused.SelectedPrefix.Foreground(green).SetString("[x] ")
	styles.Focused.UnselectedPrefix = styles.Focused.UnselectedPrefix.SetString("[ ] ")
	styles.Blurred.SelectedPrefix = styles.Blurred.SelectedPrefix.Foreground(green).SetString("[x] ")
	styles.Blurred.UnselectedPrefix = styles.Blurred.UnselectedPrefix.SetString("[ ] ")
	styles.Focused.FocusedButton = styles.Focused.FocusedButton.Background(blue).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
	styles.Focused.TextInput.Prompt = styles.Focused.TextInput.Prompt.Foreground(blue)
	return styles
}

// frenchKeyMap translates the help bar and disables single-select filtering:
// the wizard's lists are short, and the stock English "/ filter" hint misled
// testers (#445). Multi-select filtering stays available — the MCP and
// dotenv lists can grow, and the resize regressions exercise it.
func frenchKeyMap() *huh.KeyMap {
	keys := huh.NewDefaultKeyMap()
	keys.Quit.SetHelp("ctrl+c", "quitter")
	keys.Select.Up.SetHelp("↑", "haut")
	keys.Select.Down.SetHelp("↓", "bas")
	keys.Select.Filter.SetEnabled(false)
	keys.Select.SetFilter.SetEnabled(false)
	keys.Select.ClearFilter.SetEnabled(false)
	keys.Select.Next.SetHelp("entrée", "continuer")
	keys.Select.Prev.SetHelp("maj+tab", "précédent")
	keys.Select.Submit.SetHelp("entrée", "continuer")
	keys.Select.HalfPageUp.SetHelp("ctrl+u", "½ page haut")
	keys.Select.HalfPageDown.SetHelp("ctrl+d", "½ page bas")
	keys.Select.GotoTop.SetHelp("g/début", "début")
	keys.Select.GotoBottom.SetHelp("G/fin", "fin")
	keys.MultiSelect.Up.SetHelp("↑", "haut")
	keys.MultiSelect.Down.SetHelp("↓", "bas")
	keys.MultiSelect.Toggle.SetHelp("espace", "cocher")
	keys.MultiSelect.Filter.SetHelp("/", "filtrer")
	keys.MultiSelect.SetFilter.SetHelp("esc", "valider le filtre")
	keys.MultiSelect.ClearFilter.SetHelp("esc", "effacer le filtre")
	keys.MultiSelect.Next.SetHelp("entrée", "continuer")
	keys.MultiSelect.Prev.SetHelp("maj+tab", "précédent")
	keys.MultiSelect.Submit.SetHelp("entrée", "continuer")
	keys.MultiSelect.SelectAll.SetHelp("ctrl+a", "tout cocher")
	keys.MultiSelect.SelectNone.SetHelp("ctrl+a", "tout décocher")
	keys.MultiSelect.HalfPageUp.SetHelp("ctrl+u", "½ page haut")
	keys.MultiSelect.HalfPageDown.SetHelp("ctrl+d", "½ page bas")
	keys.MultiSelect.GotoTop.SetHelp("g/début", "début")
	keys.MultiSelect.GotoBottom.SetHelp("G/fin", "fin")
	keys.Input.AcceptSuggestion.SetHelp("ctrl+e", "compléter")
	keys.Input.Next.SetHelp("entrée", "continuer")
	keys.Input.Prev.SetHelp("maj+tab", "précédent")
	keys.Input.Submit.SetHelp("entrée", "continuer")
	keys.Confirm.Next.SetHelp("entrée", "continuer")
	keys.Confirm.Prev.SetHelp("maj+tab", "précédent")
	keys.Confirm.Submit.SetHelp("entrée", "continuer")
	keys.Confirm.Toggle.SetHelp("←/→", "choisir")
	// Accept/Reject keep their y/n keys and labels: huh's Confirm view
	// rewrites their help from the field's Affirmative/Negative strings on
	// every render, and every codebox confirm already says "Oui"/"Non".
	keys.Note.Next.SetHelp("entrée", "continuer")
	keys.Note.Prev.SetHelp("maj+tab", "précédent")
	keys.Note.Submit.SetHelp("entrée", "continuer")
	return keys
}

// The init wizard's MultiSelects carry their selection keys in the
// description: huh toggles with space or x and Enter only confirms the list,
// and nothing else on the screen says so. Testers completed init believing
// Enter had selected the highlighted item, and the project was created with
// nothing (#156).
const multiSelectKeyHint = "Espace ou x pour cocher/décocher ; Entrée pour valider la liste ; / pour filtrer."

// resizeCursorMultiSelect compensates for huh 2.0.3 resizing a MultiSelect's
// viewport without bringing its current option back into view.
type resizeCursorMultiSelect struct {
	*huh.MultiSelect[string]
}

func (m *resizeCursorMultiSelect) Update(msg tea.Msg) (huh.Model, tea.Cmd) {
	_, cmd := m.MultiSelect.Update(msg)
	if _, resizing := msg.(tea.WindowSizeMsg); resizing {
		m.ensureCursorVisible()
	}
	return m, cmd
}

// multiSelectViewport accesses huh's existing viewport by field name. Huh does
// not expose a visibility-only resize operation; keeping this in one adapter
// avoids changing the cursor, filter, options, or selection.
func (m *resizeCursorMultiSelect) multiSelectViewport() *viewport.Model {
	field := reflect.ValueOf(m.MultiSelect).Elem().FieldByName("viewport")
	if !field.IsValid() || field.Type() != reflect.TypeOf(viewport.Model{}) {
		return nil
	}
	return (*viewport.Model)(unsafe.Pointer(field.UnsafeAddr()))
}

func (m *resizeCursorMultiSelect) ensureCursorVisible() {
	viewport := m.multiSelectViewport()
	if viewport == nil {
		return
	}
	content := ansi.Strip(viewport.GetContent())
	cursorLine := -1
	for i, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "┃"))
		if strings.HasPrefix(line, ">") && (strings.Contains(line, "[ ]") || strings.Contains(line, "[x]")) {
			cursorLine = i
			break
		}
	}
	if cursorLine < 0 || viewport.Height() <= 0 {
		return
	}
	offset, height := viewport.YOffset(), viewport.Height()
	if cursorLine < offset {
		viewport.ScrollUp(offset - cursorLine)
	} else if cursorLine >= offset+height {
		viewport.ScrollDown(cursorLine + 1 - offset - height)
	}
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

func initSkillOptions(catalogue []codebox.ProjectSkill) []huh.Option[string] {
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

func initSkillInCatalogue(catalogue []codebox.ProjectSkill, id string) bool {
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

// initExpertFlags reports whether the invocation provided an expert setting
// through a flag (#312). The step counters are omitted and the recap keeps
// its technical detail for those runs. The displayed root never skips a step,
// so it is not part of the signal.
func initExpertFlags(opts initOptions) bool {
	for _, field := range []string{"runtime", "isolation", "harness", "model", "small-model", "cpus", "memory-mb", "credential-ref", "github", "skills", "mcps"} {
		if opts.Set[field] {
			return true
		}
	}
	return false
}

// initStepPrefix numbers the legacy five-step project wizard or, for the
// combined flow, its project decisions within six screens ([3/6]..[6/6]).
// When an expert setting or an entire step comes from a flag, the counter is
// omitted rather than risk a misleading number (#312).
func initStepPrefix(opts initOptions, step int) string {
	if initExpertFlags(opts) {
		return ""
	}
	if opts.Combined {
		return fmt.Sprintf("[%d/6] ", step)
	}
	return fmt.Sprintf("[%d/5] ", step)
}

// initHarnessStepTitle is the harness screen title with the same counter rule
// as the other steps (#312).
func initHarnessStepTitle(opts initOptions) string {
	step := 1
	if opts.Combined {
		step = 3
	}
	return initStepPrefix(opts, step) + "Agent (harnais)"
}

// initVMStepTitle is the VM screen title with the same counter rule as the
// other steps (#312).
func initVMStepTitle(opts initOptions) string {
	step := 2
	if opts.Combined {
		step = 4
	}
	return initStepPrefix(opts, step) + "Machine isolée"
}

func initSkillsStepTitle(opts initOptions) string {
	step := 3
	if opts.Combined {
		step = 5
	}
	return initStepPrefix(opts, step)
}

// initMCPStepTitle is the MCP screen title with the same counter rule as the
// other steps (#312); the base wording is codebox.MCPStepTitle's.
func initMCPStepTitle(opts initOptions) string {
	step := 4
	if opts.Combined {
		step = 6
	}
	return initStepPrefix(opts, step) + "MCP (facultatif)"
}

// GitHub is conditional on a repository remote and stays outside the six
// default screens, so the combined flow gives it no contradictory counter.
func initGitHubStepTitle(opts initOptions) string {
	if opts.Combined {
		return "GitHub (facultatif)"
	}
	return initStepPrefix(opts, 5) + "GitHub (facultatif)"
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

func keepEmptyInitSkillsForRetry(answers *codebox.InitAnswers) {
	answers.Skills = nil
	answers.SkillsSet = true
}

// commitInitHuhSkills copies the skills group's submitted values into
// answers. It runs when the user LEAVES the skills selection, before the MCP
// step and before the empty-selection confirmation (#307), so a declined
// confirmation reopens the form with every other answer kept. It matches the
// line wizard's "Enter keeps" semantics: a submission only marks a value
// explicit when it differs from the seeded value, and huh rebuilds a
// MultiSelect's value in its own option order, so it compares as sets.
// It reports whether the submission needs the zero-skills confirmation.
func commitInitHuhSkills(opts initOptions, answers *codebox.InitAnswers, skills []string, skillsLocalOnly bool, skillOptions []huh.Option[string], skipSkills bool) bool {
	needsSkillsConfirm := false
	if !skipSkills {
		selectedSkills := append([]string(nil), skills...)
		answers.SkillsSet = answers.SkillsSet || !equalStringSets(answers.Skills, selectedSkills)
		answers.Skills = selectedSkills
		answers.RetainedSkills = retainedInitSkillIDs(selectedSkills, skillOptions)
		needsSkillsConfirm = !opts.Combined && len(skills) == 0
	}
	if !opts.Set["skills-storage"] {
		answers.SkillsLocalOnlySet = answers.SkillsLocalOnlySet || answers.SkillsLocalOnly != skillsLocalOnly
		answers.SkillsLocalOnly = skillsLocalOnly
	}
	return needsSkillsConfirm
}

// initEmptySkillsConfirmFn asks the zero-skills confirmation (#307). It is a
// var so tests can place the question in the form sequence and script its
// answer, the same pattern as huhRun.
var initEmptySkillsConfirmFn = func() (bool, error) {
	return huhConfirm("Aucune skill sélectionnée. Continuer sans skill ?", "Appuyez sur Entrée pour continuer, ou choisissez non pour revenir à la sélection.", true)
}

// commitInitHuhMCPs copies the MCP submission into answers. The zero-MCP
// confirmation runs before the write-back, so declining it reopens the form
// with those answers unchanged, like the line wizard. It reports whether the
// user declined that confirmation.
func commitInitHuhMCPs(opts initOptions, answers *codebox.InitAnswers, mcps []string) (bool, error) {
	if opts.Set["mcps"] {
		return false, nil
	}
	if !opts.Combined && len(mcps) == 0 {
		// Same zero-selection confirmation as the line wizard;
		// declining reopens the form with the answers unchanged.
		keep, confirmErr := confirmNoMCPsHuh()
		if confirmErr != nil {
			return false, confirmErr
		}
		if !keep {
			return true, nil
		}
	}
	answers.MCPsSet = answers.MCPsSet || !equalStringSets(answers.MCPConnectors, mcps)
	answers.MCPConnectors = mcps
	return false, nil
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

func newProjectMCPsMultiSelect(mcpOptions []huh.Option[string], value *[]string, title string) huh.Field {
	return keepMultiSelectCursorOnResize(huh.NewMultiSelect[string]().
		Title(title).
		Description(codebox.MCPStepIntro + " " + multiSelectKeyHint).
		Height(len(mcpOptions) + 10).
		Filterable(true).
		Options(mcpOptions...).
		Value(value))
}

func newProjectMCPsGroup(fields []huh.Field) *huh.Group {
	return huh.NewGroup(fields...).Title("MCP")
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
		reason, removed := codebox.RemovedMCPIDs[id]
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
				Affirmative("Oui").
				Negative("Non").
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
				Affirmative("Oui").
				Negative("Non").
				Value(&value),
		),
	))
	if err != nil {
		return false, err
	}
	return value, nil
}

func initModelDisplayName(model string) string {
	if model == "" {
		return "modèle par défaut de setup"
	}
	id := canonicalAlbertModelRef(model)
	short := strings.TrimPrefix(id, "albert/")
	for _, choice := range codebox.CuratedAlbertModels() {
		if choice.OpenCodeID == short {
			return choice.Name
		}
	}
	return id
}

func initSmallModelDisplayName(answers codebox.InitAnswers) string {
	if answers.SmallModel != "" {
		return initModelDisplayName(answers.SmallModel)
	}
	return "même modèle que le modèle principal (" + initModelDisplayName(answers.Model) + ")"
}

func newInitSmallModelSelect(root string, value *string) *huh.Select[string] {
	options := []huh.Option[string]{
		huh.NewOption("Même modèle que le modèle principal (défaut)", ""),
		huh.NewOption("Ministral 3 8B — petit modèle généraliste", codebox.MinistralSmallModelRef),
	}
	if *value != "" && *value != codebox.MinistralSmallModelRef {
		options = append(options, huh.NewOption(initModelDisplayName(*value)+" (déjà configuré)", *value))
	}
	return huh.NewSelect[string]().
		Title("Petit modèle").
		Description("Utilisé pour générer les titres de session. Le choix par défaut suit toujours le modèle principal. Ministral nécessite une connexion à Albert API.").
		Options(options...).
		Validate(func(s string) error { _, err := validateInitSmallModelInput(root, s); return err }).
		Value(value)
}

var initAlbertConnectedFn = func(root string) (bool, error) {
	settings, err := resolveProviderSettingsAt(root)
	if err != nil {
		return false, err
	}
	providers, err := codebox.ResolveProviders(settings)
	if err != nil {
		return false, err
	}
	return providersContain(providers, codebox.ProviderAlbert), nil
}

// validateInitSmallModelInput requires Albert for the curated Albert-only
// model. Empty remains valid with every provider: it follows the main model.
func validateInitSmallModelInput(root, value string) (string, error) {
	model := normalizeInitModelInput(value)
	if model == "" {
		return "", nil
	}
	if codebox.ModelRefProvider(model) == codebox.ProviderLocal {
		return "", nil
	}
	if model == codebox.MinistralSmallModelRef {
		connected, err := initAlbertConnectedFn(root)
		if err != nil {
			return "", fmt.Errorf("impossible de déterminer si l'API Albert est connectée : %w", err)
		}
		if !connected {
			return "", fmt.Errorf("Ministral 3 8B exige que l'API Albert soit connectée ; lancez 'codebox setup' pour la connecter, ou conservez le modèle principal")
		}
	}
	return catalogueModelWarningFn(root, model)
}

// runtimeInstallHintFn is the seam the advanced settings screen uses to flag
// a chosen runtime that is not installed on this host. Tests stub it so the
// check never depends on the machine running the test.
var runtimeInstallHintFn = codebox.RuntimeInstallCheck

// Each submitted editor commits one valid answer. Escape closes the editor
// without discarding previously accepted edits.
func askInitAdvancedHuh(opts initOptions, answers codebox.InitAnswers) (codebox.InitAnswers, error) {
	choice := "isolation"
	modelWarning := ""
	smallModelWarning := ""
	for {
		iso := answers.Isolation
		if iso == "" {
			iso = codebox.IsolationFull
		}
		cpus := resolvedOrDefault(answers.CPUs, codebox.RecommendedDefaultGuestCPUs(opts.hostCPUs))
		memory := resolvedOrDefault(answers.MemoryMB, codebox.RecommendedDefaultGuestMemoryMB(opts.hostMemoryMB))
		var options []huh.Option[string]
		if !opts.Set["isolation"] {
			options = append(options, huh.NewOption("Mode d'isolation : "+isolationDisplayName(iso), "isolation"))
		}
		if !opts.Set["cpus"] {
			options = append(options, huh.NewOption(fmt.Sprintf("Processeurs de la machine isolée : %d", cpus), "cpus"))
		}
		if !opts.Set["memory-mb"] {
			options = append(options, huh.NewOption("Mémoire de la machine isolée : "+codebox.FormatMemorySize(memory), "memory"))
		}
		if !opts.Set["model"] {
			options = append(options, huh.NewOption("Modèle principal : "+initModelDisplayName(answers.Model), "model"))
		}
		if !opts.Set["small-model"] {
			options = append(options, huh.NewOption("Petit modèle : "+initSmallModelDisplayName(answers), "small-model"))
		}
		if !opts.Set["skills-storage"] {
			storage := "avec le projet"
			if answers.SkillsLocalOnly {
				storage = "sur ce poste uniquement"
			}
			options = append(options, huh.NewOption("Stockage des skills : "+storage, "skills-storage"))
		}
		options = append(options, huh.NewOption("Terminer", "back"))
		if len(options) == 1 {
			choice = "back"
		}
		description := "Ces valeurs conviennent à la plupart des projets. Modifiez-les seulement si besoin.\n\n" +
			"Petit modèle : sert aux titres de session.\n" +
			"6 Go conseillés avec chrome-devtools.\n\n" +
			modelWarning +
			"\n" + smallModelWarning +
			"\n\n↑ ↓ : choisir un réglage. Entrée : modifier. Échap : terminer."
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
		skillStorageInput := answers.SkillsLocalOnly
		switch choice {
		case "isolation":
			input = string(iso)
			field = huh.NewSelect[string]().Title("Mode d'isolation").
				Description("complet : l'agent et son interface tournent dans la machine isolée. partiel : l'interface tourne sur votre poste, l'agent dans la machine isolée.").
				Options(huh.NewOption("complet (recommandé)", "full"), huh.NewOption("partiel", "backend")).
				Validate(func(value string) error { return validateInitIsolationChoice(value, answers) }).
				Value(&input)
		case "cpus":
			input = strconv.Itoa(cpus)
			field = huh.NewInput().Title("Processeurs de la machine isolée").
				Description(fmt.Sprintf("De 1 à %d. Vide ou 'défaut' rétablit la valeur par défaut. Échap : annuler cette modification.", opts.maxCPUs)).
				Validate(func(s string) error { _, _, err := parseGuestCPUsInput(s, opts.maxCPUs); return err }).Value(&input)
		case "memory":
			input = codebox.FormatMemorySize(memory)
			field = huh.NewInput().Title("Mémoire de la machine isolée").
				Description(fmt.Sprintf("Maximum sur ce poste : %s. 6 Go conseillés avec chrome-devtools. Vide ou 'défaut' rétablit la valeur par défaut. Échap : annuler cette modification.", codebox.FormatMemorySize(opts.maxMemoryMB))).
				Validate(func(s string) error { _, _, err := parseGuestMemoryInput(s, opts.maxMemoryMB); return err }).Value(&input)
		case "model":
			input = answers.Model
			field = huh.NewInput().Title("Modèle principal").
				Description("Identifiant du modèle Albert ('codebox models' liste les modèles). Vide ou 'défaut' reprend le modèle défini dans setup. Échap : annuler cette modification.").
				Validate(func(s string) error {
					_, err := validateInitModelInput(answers.Root, s, initHarnessValue(answers))
					return err
				}).Value(&input)
		case "small-model":
			input = answers.SmallModel
			field = newInitSmallModelSelect(answers.Root, &input)
		case "skills-storage":
			field = huh.NewSelect[bool]().
				Title("Où enregistrer les skills ?").
				Description("Avec le projet : elles sont versionnées avec le projet. Sur ce poste uniquement : rien n'est ajouté au dépôt.").
				Options(huh.NewOption("Avec le projet", false), huh.NewOption("Sur ce poste uniquement", true)).
				Value(&skillStorageInput)
		default:
			return answers, fmt.Errorf("réglage avancé inconnu : %q", choice)
		}
		back, err = runInitAdvancedForm(huh.NewForm(huh.NewGroup(field)))
		if err != nil {
			return answers, err
		}
		if back {
			continue
		}
		switch choice {
		case "isolation":
			answers.Isolation = codebox.Isolation(input)
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
			warning, err := validateInitModelInput(answers.Root, input, initHarnessValue(answers))
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
		case "skills-storage":
			answers.SkillsLocalOnly = skillStorageInput
			answers.SkillsLocalOnlySet = true
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

func validateInitModelInput(root, value string, harness codebox.Harness) (string, error) {
	model := normalizeInitModelInput(value)
	if model == "" {
		return "", nil
	}
	// A model the chosen harness cannot run is refused as it is typed, not at
	// the recap (#347): the answer would otherwise look accepted and then come
	// back as an error after the whole flow.
	if harness == codebox.HarnessPi {
		if reason := codebox.PiUnsupportedModelReason(model); reason != "" {
			return "", fmt.Errorf("%s. Ce modèle est écarté pour Pi : choisissez un autre modèle principal, ou OpenCode", reason)
		}
	}
	return catalogueModelWarningFn(root, model)
}

// The filter distinguishes Escape (back) from Ctrl+C (cancel init).
var runInitAdvancedForm = func(form *huh.Form) (back bool, err error) {
	err = form.WithTheme(codeboxTheme{}).WithKeyMap(frenchKeyMap()).WithShowHelp(false).WithProgramOptions(tea.WithOutput(os.Stdout), tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
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

func effectiveInitRuntime(answers codebox.InitAnswers) codebox.Runtime {
	if answers.Runtime == "" {
		return codebox.RuntimeMicrosandbox
	}
	return answers.Runtime
}

func initRuntimeNotice(rt codebox.Runtime) string {
	var notice string
	if rt == codebox.RuntimeTart || rt == codebox.RuntimeAgentVM {
		notice = "Avec un dossier partagé, l'agent voit tous les fichiers du projet, y compris .env.\n"
	}
	if installed, hint := runtimeInstallHintFn(rt); !installed {
		notice += fmt.Sprintf("La machine choisie (%s) n'est pas installée. Installez-la : %s", rt, hint)
	}
	return strings.TrimSpace(notice)
}

func effectiveInitHarness(answers codebox.InitAnswers) string {
	if answers.Harness == "" {
		return string(codebox.DefaultHarness)
	}
	return answers.Harness
}

// askHarnessStepHuh asks which agent harness to configure (#333, #176). Both
// harnesses are supported; OpenCode is the recommended choice because it is
// the historical one and the only one that can also run with partial
// isolation. The step has no flag: it is the wizard's first decision and
// always runs interactively.
func askHarnessStepHuh(opts initOptions, answers codebox.InitAnswers) (codebox.InitAnswers, error) {
	harness := effectiveInitHarness(answers)
	field := huh.NewSelect[string]().
		Title(initHarnessStepTitle(opts)).
		Description("OpenCode est l'agent historique, avec ses greffons et ses MCP. Pi est un agent en terminal plus léger, installé dans la même machine isolée.").
		Options(
			huh.NewOption("OpenCode (recommandé)", string(codebox.HarnessOpenCode)),
			huh.NewOption("Pi", string(codebox.HarnessPi)),
		).
		Value(&harness)
	if err := huhRun(huh.NewForm(huh.NewGroup(field))); err != nil {
		return answers, err
	}
	answers.Harness = harness
	return answers, nil
}

// validateInitIsolationChoice refuses an isolation mode the chosen harness
// cannot run: Pi has no server mode, so partial isolation would leave the
// interface on the host with nothing to attach to (#176). The message names
// both values, because the user is choosing one of them right now.
func validateInitIsolationChoice(value string, answers codebox.InitAnswers) error {
	iso, err := codebox.ResolveIsolation(value, "")
	if err != nil {
		return err
	}
	if codebox.HarnessRequiresFullIsolation(initHarnessValue(answers)) && iso == codebox.IsolationBackend {
		return fmt.Errorf("Pi exige l'isolation complète : il n'a pas de mode serveur auquel s'attacher depuis votre poste. Choisissez « complet » ou OpenCode")
	}
	return nil
}

// initHarnessValue resolves the wizard's harness answer to the typed value.
// An unrecognised answer cannot reach here (the steps validate it), so the
// default is the honest fallback rather than an error the caller must handle.
func initHarnessValue(answers codebox.InitAnswers) codebox.Harness {
	resolved, err := codebox.ResolveHarness(effectiveInitHarness(answers))
	if err != nil {
		return codebox.DefaultHarness
	}
	return resolved
}

func initRuntimeOptionsForOS(goos string) []huh.Option[string] {
	options := []huh.Option[string]{huh.NewOption("Microsandbox (recommandé)", string(codebox.RuntimeMicrosandbox))}
	if goos == "darwin" {
		options = append(options, huh.NewOption("Tart : VM macOS complète (expérimental)", string(codebox.RuntimeTart)))
	}
	if goos == "darwin" || goos == "linux" {
		options = append(options, huh.NewOption("agent-vm : VM Lima (expérimental)", string(codebox.RuntimeAgentVM)))
	}
	return options
}

// askVMStepHuh asks which isolated machine runs the agent (#333). It is
// skipped when --runtime came from the command line. Microsandbox is
// recommended: it copies the project with .env and secrets excluded; Tart and
// agent-vm share the whole project folder with the VM. The GitHub step adapts
// to the choice at runtime, so no cross-step validation runs here.
func askVMStepHuh(opts initOptions, answers codebox.InitAnswers) (codebox.InitAnswers, error) {
	if opts.Set["runtime"] {
		return answers, nil
	}
	runtime := string(effectiveInitRuntime(answers))
	description := "Microsandbox : copie filtrée du projet (.env et secrets exclus), démarrage rapide. Tart et agent-vm : votre dossier est partagé avec la VM."
	if notice := initRuntimeNotice(codebox.Runtime(runtime)); notice != "" {
		description += "\n" + notice
	}
	field := huh.NewSelect[string]().
		Title(initVMStepTitle(opts)).
		Description(description).
		Options(initRuntimeOptionsForOS(goruntime.GOOS)...).
		Value(&runtime)
	if err := huhRun(huh.NewForm(huh.NewGroup(field))); err != nil {
		return answers, err
	}
	answers.Runtime = codebox.Runtime(runtime)
	return answers, nil
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
	memoryMB, err = codebox.ParseMemorySize(value)
	if err != nil {
		return 0, false, fmt.Errorf("format attendu : 4096, 4G ou 2.5G")
	}
	if memoryMB < 1 || memoryMB > maximum {
		return 0, false, fmt.Errorf("au moins 1 Mo. Maximum sur ce poste : %s", codebox.FormatMemorySize(maximum))
	}
	return memoryMB, false, nil
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
func huhDiagnosticsDescription(report codebox.DiagnosticsReport) string {
	var b strings.Builder
	for _, section := range report.Sections {
		t := lipglosstable.New().
			Headers("Contrôle", "État", "Valeur").
			Width(huhReviewTableWidth()).
			StyleFunc(func(row, column int) lipgloss.Style {
				if row == lipglosstable.HeaderRow || column == 0 {
					return lipgloss.NewStyle().Bold(true)
				}
				if column == 1 && row >= 0 && row < len(section.Items) {
					color := lipgloss.Color("244")
					switch section.Items[row].Status {
					case codebox.DiagnosticOK:
						color = lipgloss.Color("42")
					case codebox.DiagnosticWarn:
						color = lipgloss.Color("214")
					case codebox.DiagnosticFailed:
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

// askInitQuestionsHuh renders the init wizard as one huh form. Groups the
// command line did not supply are omitted structurally (never added to the
// form), so a partial `codebox init --runtime tart` form shows only the
// unanswered questions, matching the line path's skip semantics — and the
// omission also holds in huh's accessible runner, which ignores hide
// predicates.
func askInitQuestionsHuh(opts initOptions, answers codebox.InitAnswers) (codebox.InitAnswers, error) {
	// The catalogue and curated MCP list do not depend on the project
	// root, so they are fetched once outside the reseed loop below.
	var catalogue []codebox.ProjectSkill
	var catalogueErr error
	if !opts.Set["skills"] {
		catalogue, catalogueErr = projectSkillCatalogueFn(context.Background())
		if catalogueErr != nil {
			fmt.Fprintf(os.Stderr, "Attention : le catalogue de skills épinglé du projet est indisponible : %v\n", catalogueErr)
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
	for _, choice := range codebox.MCPSelections() {
		mcpOptions = append(mcpOptions, huh.NewOption(choice.ID+" — "+choice.Description, choice.ID))
	}

	// An accepted empty-skills decision covers later MCP retries in the legacy
	// project wizard; the combined flow submits empty selections directly.
	emptySkillsConfirmed := false
	// The form runs in a loop: when the user changes the project root, the
	// answers are reseeded from the target project's manifest (matching
	// the line wizard) and the form reopens with the reseeded defaults.
	for {
		// The harness and VM steps open the wizard (#333): they run before
		// the project form so the on-screen order matches [1/5]..[5/5]. A
		// root change reseeds the answers and reopens these steps with the
		// new project's manifest defaults.
		var stepErr error
		if opts.Set["harness"] {
			// --harness answered the question: skip it so the counter and the
			// on-screen order match what the user still has to decide.
			answers.Harness = opts.Harness
		} else {
			answers, stepErr = askHarnessStepHuh(opts, answers)
		}
		if stepErr != nil {
			recordInitAbandon(answers.Root, "harness")
			return answers, stepErr
		}
		answers, stepErr = askVMStepHuh(opts, answers)
		if stepErr != nil {
			recordInitAbandon(answers.Root, "vm")
			return answers, stepErr
		}
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
				return answers, fmt.Errorf("impossible de lire les skills actuelles du projet : %w", err)
			}
			skills = current
			// The comparison baseline below must match what the form
			// actually shows: an unchanged submission must not look
			// like a new selection and re-resolve existing pins.
			answers.Skills = append([]string(nil), current...)
			if !opts.Combined && len(current) == 0 && !projectManifestExists(answers.Root) && initSkillInCatalogue(catalogue, "official/securite-developpement") {
				skills = []string{"official/securite-developpement"}
			}
		}
		skillOptionsPass := retainedSkillOptions(skillOptions, skills)
		if !answers.MCPsSet {
			if pc, err := codebox.DiscoverProject(answers.Root); err == nil {
				if manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(pc.Root)); err == nil {
					mcps = append([]string(nil), manifest.MCPConnectors...)
					answers.MCPConnectors = append([]string(nil), manifest.MCPConnectors...)
					haveManifestMCPs = true
				}
			}
		}
		if !opts.Combined && len(mcps) == 0 && !haveManifestMCPs && !answers.MCPsSet {
			// Legacy project initialization preselects the curated default. The
			// combined first run deliberately starts with no MCP selected.
			mcps = append([]string(nil), codebox.DefaultFreshMCPIDs...)
			answers.MCPConnectors = append([]string(nil), codebox.DefaultFreshMCPIDs...)
		}
		mcpOptionsPass := retainedMCPOptions(mcpOptions, mcps)

		var rootGroup *huh.Group
		if !opts.Set["root"] {
			rootGroup = huh.NewGroup(
				huh.NewInput().
					Title("Racine du projet").
					Description(fmt.Sprintf("Dossier qui contient le manifeste et la configuration de la machine isolée. Laissez vide pour conserver %s.", answers.Root)).
					Placeholder(answers.Root).
					Value(&root),
			).Title("Projet")
		}

		// Runtime, isolation, model, resources and the credential reference
		// are expert settings: they no longer appear in the default path and
		// live behind the review's "Réglages avancés" entry (#170).

		skillsGroup := newInitSkillsGroup(skillOptionsPass, &skills, &skillsLocalOnly,
			!opts.Set["skills"] && !skipSkills, !opts.Combined && !opts.Set["skills-storage"], initSkillsStepTitle(opts))

		mcpFields := make([]huh.Field, 0, 2)
		if !opts.Set["mcps"] {
			mcpFields = append(mcpFields, newProjectMCPsMultiSelect(mcpOptionsPass, &mcps, initMCPStepTitle(opts)))
		}
		var mcpGroup *huh.Group
		if len(mcpFields) > 0 {
			mcpGroup = newProjectMCPsGroup(mcpFields)
		}

		// Groups are omitted structurally rather than via WithHideFunc:
		// huh's accessible runner (TERM=dumb) iterates every group and
		// never evaluates hide predicates, so a hidden group would still
		// prompt there and its submitted value would override the flag.
		//
		// The skills groups run in a FIRST form and the MCP group in a
		// second one (#307): the empty-skills confirmation belongs to the
		// moment the user leaves the skills selection, before the MCP
		// step, like the line wizard — not after the whole form.
		var skillsFormGroups []*huh.Group
		for _, g := range []*huh.Group{rootGroup, skillsGroup} {
			if g != nil {
				skillsFormGroups = append(skillsFormGroups, g)
			}
		}
		if len(skillsFormGroups) > 0 {
			if err := huhRun(huh.NewForm(skillsFormGroups...)); err != nil {
				recordInitAbandon(answers.Root, "skills")
				return answers, err
			}
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
			resolved, resolveErr := reseedInitAnswersForRootChange(opts, answers, newRoot)
			if resolveErr != nil {
				return answers, resolveErr
			}
			answers = resolved
			// Flags keep their explicit selection across a root change;
			// only interactively seeded values are reseeded from the new
			// project's manifest.
			if !opts.Set["skills"] {
				answers.SkillsSet = false
			}
			if !opts.Set["mcps"] {
				answers.MCPsSet = false
			}
			emptySkillsConfirmed = false
			continue
		}
		answers.Root = newRoot
		// Commit the skills answers BEFORE the confirmation: a declined
		// confirmation reopens the form, which reseeds from answers, so the
		// storage choice and the other answers must already be safe (#307).
		needsSkillsConfirm := commitInitHuhSkills(opts, &answers, skills, skillsLocalOnly, skillOptions, skipSkills)
		if len(skills) > 0 {
			emptySkillsConfirmed = false
		}
		if needsSkillsConfirm && !emptySkillsConfirmed {
			proceed, err := initEmptySkillsConfirmFn()
			if err != nil {
				recordInitAbandon(answers.Root, "skills")
				return answers, err
			}
			if !proceed {
				keepEmptyInitSkillsForRetry(&answers)
				continue
			}
			answers.Skills = nil
			answers.SkillsSet = true
			emptySkillsConfirmed = true
		}
		if mcpGroup != nil {
			if err := huhRun(huh.NewForm(mcpGroup)); err != nil {
				// The MCP form is a distinct step: an interruption here
				// must not be attributed to skills (#310).
				recordInitAbandon(answers.Root, "mcp")
				return answers, err
			}
		}
		retryMCPs, err := commitInitHuhMCPs(opts, &answers, mcps)
		if err != nil {
			recordInitAbandon(answers.Root, "mcp")
			return answers, err
		}
		if retryMCPs {
			continue
		}
		// The GitHub step is a distinct screen, not a field of the main
		// form: it renders one of the four situations and verifies on that
		// screen (#169). It runs after the main form so the [1/5]..[5/5]
		// order on screen matches the flow, and its own screens never throw
		// away the answers already collected (#155).
		if !opts.Set["github"] {
			if opts.Combined && !githubInitProbeFn(answers.Root).HasOrigin {
				answers.GitHubWorkflow = false
				answers.GitHubRemote = codebox.GitHubRemote{}
			} else {
				var err error
				answers, err = askGitHubStepHuh(answers, initGitHubStepTitle(opts))
				if err != nil {
					recordInitAbandon(answers.Root, "github")
					return answers, err
				}
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
func askGitHubStepHuh(answers codebox.InitAnswers, title string) (codebox.InitAnswers, error) {
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
						Title(title).
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
			answers.GitHubRemote = codebox.GitHubRemote{}
			return answers, nil
		case !probe.HasOrigin:
			answers.GitHubCase = 2
			choice := "continue"
			err := huhRun(huh.NewForm(
				huh.NewGroup(
					huh.NewNote().
						Title(title).
						Description("Ce projet n'est pas relié à GitHub.\nL'agent travaille sur une copie du projet. Pour récupérer ses modifications dans votre dossier, vous les relirez puis les appliquerez avec : codebox workspace apply"),
					huh.NewSelect[string]().
						Title("Que voulez-vous faire ?").
						Options(
							huh.NewOption("Continuer", "continue"),
							huh.NewOption("Quitter, relier le projet à GitHub, puis relancer : codebox init", "quit"),
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
			answers.GitHubRemote = codebox.GitHubRemote{}
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
						Title(title).
						Description(fmt.Sprintf("Le mot de passe de votre poste protège les identifiants, et il n'a pas pu être lu.\nTant qu'il est verrouillé, codebox ne peut pas savoir si un jeton GitHub est disponible.\n\nDétail : %v", probe.TokenError)),
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
			answers.GitHubRemote = codebox.GitHubRemote{}
			return answers, nil
		case !probe.HasToken:
			answers.GitHubCase = 3
			choice := "later"
			err := huhRun(huh.NewForm(
				huh.NewGroup(
					huh.NewNote().
						Title(title).
						Description(fmt.Sprintf("Dépôt détecté : %s\nAucun jeton GitHub n'est enregistré sur ce poste.", gitHubRepoDisplay(probe.Remote))),
					huh.NewSelect[string]().
						Title("Jeton GitHub").
						Options(
							huh.NewOption("Plus tard (codebox auth add github)", "later"),
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
			answers.GitHubRemote = codebox.GitHubRemote{}
			return answers, nil
		case probe.Rights == codebox.GitHubRepoRightsOK:
			answers.GitHubCase = 1
			yes := true
			err := huhRun(huh.NewForm(
				huh.NewGroup(
					huh.NewNote().
						Title(title).
						Description(fmt.Sprintf("Dépôt détecté : %s\nJeton GitHub : %s, droits vérifiés sur ce dépôt ✅", gitHubRepoDisplay(probe.Remote), gitHubAccountLabel(probe))),
					huh.NewConfirm().
						Title("Autoriser l'agent à pousser ses modifications sur une branche et à ouvrir une pull request en brouillon sur ce dépôt ?").
						Description("Vous gardez la main : rien n'est fusionné sans votre accord.").
						Affirmative("Oui").
						Negative("Non").
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
						Title(title).
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
			answers.GitHubRemote = codebox.GitHubRemote{}
			return answers, nil
		}
	}
}
