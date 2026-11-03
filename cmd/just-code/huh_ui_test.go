package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/etalab-ia/just-code/internal/justcode"
)

// The huh form itself needs a TTY, so the regression tests pin the decision
// logic around it: which fields the form must omit so a hidden or failed
// input can never clobber an existing project selection.

func TestShouldSkipSkillsField(t *testing.T) {
	cases := []struct {
		name         string
		skillsFlag   bool
		catalogueErr error
		want         bool
	}{
		{"flag supplied", true, nil, true},
		{"catalogue ok", false, nil, false},
		{"catalogue failed", false, errors.New("offline"), true},
		{"flag supplied even on catalogue failure", true, errors.New("offline"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := initOptions{Set: map[string]bool{"skills": tc.skillsFlag}}
			if got := shouldSkipSkillsField(opts, tc.catalogueErr); got != tc.want {
				t.Fatalf("shouldSkipSkillsField = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestInitMultiSelectsAnnounceSelectionKeys pins #156: the wizard's
// MultiSelects must say how to toggle an item, because Enter only confirms
// the list and testers completed init believing Enter had selected the
// highlighted item. The assertion runs against the rendered group content —
// the text the user sees — not the source constants.
func TestInitMultiSelectsAnnounceSelectionKeys(t *testing.T) {
	assertHint := func(name string, field huh.Field) {
		t.Helper()
		content := huh.NewGroup(field).Content()
		for _, want := range []string{"Espace ou x pour cocher/décocher", "Entrée pour valider la liste"} {
			if !strings.Contains(content, want) {
				t.Fatalf("%s must announce %q in its description, got:\n%s", name, want, content)
			}
		}
	}
	skillsValue := []string{}
	assertHint("Project skills", newProjectSkillsMultiSelect(
		[]huh.Option[string]{huh.NewOption("pdf", "pdf")}, &skillsValue, "[1/3] "))
	mcpValue := []string{}
	assertHint("MCPs", newProjectMCPsMultiSelect(
		[]huh.Option[string]{huh.NewOption("data-gouv", "data-gouv")}, &mcpValue, justcode.MCPStepTitle))
	dotenvValue := []string{}
	assertHint("Dotenv files", newDotenvFilesMultiSelect(
		[]huh.Option[string]{huh.NewOption(".env", ".env")}, &dotenvValue))
}

// TestProjectMCPsScreenUsesWizardTexts pins QA #301 bloquant 2: the huh MCP
// screen must render the #168 French texts, and it must take them from the
// same constants the line wizard prints, so the two renders cannot diverge
// again. The group title is asserted too: the old "MCPs and storage" header
// described a storage question that lives on the skills screen.
func TestProjectMCPsScreenUsesWizardTexts(t *testing.T) {
	mcpValue := []string{}
	field := newProjectMCPsMultiSelect(
		[]huh.Option[string]{huh.NewOption("data-gouv — données publiques", "data-gouv")}, &mcpValue, justcode.MCPStepTitle)
	content := newProjectMCPsGroup([]huh.Field{field}).Content()
	for _, want := range []string{justcode.MCPStepTitle, justcode.MCPStepIntro, "data-gouv"} {
		if !strings.Contains(content, want) {
			t.Errorf("MCP screen must render %q, got:\n%s", want, content)
		}
	}
	for _, forbidden := range []string{"MCPs and storage", "Curated remote services"} {
		if strings.Contains(content, forbidden) {
			t.Errorf("MCP screen must not keep the v0.8 text %q, got:\n%s", forbidden, content)
		}
	}
}

func TestInitSkillsAreTheSixDescribedOfficialEntries(t *testing.T) {
	catalogue := []justcode.ProjectSkill{
		{ID: "experimental/presentation-docs", Experimental: true},
		{ID: "official/datagouv-apis"},
		{ID: "official/lasuite-ui-kit"},
		{ID: "official/react-dsfr"},
		{ID: "official/rgaa"},
		{ID: "official/anssi-guides"},
		{ID: "official/securite-developpement"},
	}
	options := initSkillOptions(catalogue)
	if len(options) != 6 {
		t.Fatalf("skill option count = %d, want 6", len(options))
	}
	for i, option := range options {
		if option.Value != initSkillOrder[i] || !strings.Contains(option.Key, initSkillDescriptions[option.Value]) {
			t.Errorf("option %d = %q (%q), want %q with its short description", i, option.Key, option.Value, initSkillOrder[i])
		}
	}
	selected := []string{"official/securite-developpement"}
	localOnly := false
	content := newInitSkillsGroup(options, &selected, &localOnly, true, true, "[1/3] ").Content()
	for _, want := range []string{"Espace ou x pour cocher/décocher", "Entrée pour valider", "règles, références, exemples"} {
		if !strings.Contains(content, want) {
			t.Errorf("skills screen missing %q: %s", want, content)
		}
	}
	if got := initSkillsTitle(1, "[1/3] "); got != "[1/3] Skills (facultatif) — 1 skill sélectionnée" {
		t.Errorf("one-skill title = %q", got)
	}
	if got := initSkillsTitle(0, ""); !strings.Contains(got, "0 skills sélectionnées") {
		t.Errorf("zero-skill title = %q", got)
	}
	for _, want := range []string{"Où enregistrer les skills ?", "Avec le projet", "Sur ce poste uniquement", "rien n'est ajouté au dépôt"} {
		if !strings.Contains(content, want) {
			t.Errorf("skills screen missing storage detail %q: %s", want, content)
		}
	}
}

func TestInitHuhWizardDefaultsSecuritySkillForNewProject(t *testing.T) {
	root := initTestProject(t)
	originalCatalogue, originalHuhRun := projectSkillCatalogueFn, huhRun
	projectSkillCatalogueFn = func(context.Context) ([]justcode.ProjectSkill, error) {
		return initSkillFixtureCatalogue(), nil
	}
	huhRun = func(*huh.Form) error { return nil }
	t.Cleanup(func() {
		projectSkillCatalogueFn, huhRun = originalCatalogue, originalHuhRun
	})
	set := map[string]bool{
		"root": true, "runtime": true, "isolation": true, "model": true,
		"cpus": true, "memory-mb": true, "credential-ref": true,
		"github": true, "mcps": true, "skills-storage": true,
	}
	opts := initOptions{Set: set, hostCPUs: 8, hostMemoryMB: 16384, maxCPUs: 8, maxMemoryMB: 8192}
	got, err := askInitQuestionsHuh(opts, justcode.InitAnswers{Root: root})
	if err != nil {
		t.Fatalf("askInitQuestionsHuh: %v", err)
	}
	if !got.SkillsSet || !equalStringSlices(got.Skills, []string{"official/securite-developpement"}) {
		t.Fatalf("new-project default selection = %+v", got)
	}
}

// initEmptySkillsSequence runs askInitQuestionsHuh with an explicitly empty
// skill selection and records the form/confirmation sequence (#307). The
// scripted confirmation answers are returned in order.
func initEmptySkillsSequence(t *testing.T, mcpsFlag bool, skills []string, confirms []bool) ([]string, justcode.InitAnswers) {
	t.Helper()
	root := initTestProject(t)
	originalCatalogue, originalHuhRun, originalConfirm := projectSkillCatalogueFn, huhRun, initEmptySkillsConfirmFn
	projectSkillCatalogueFn = func(context.Context) ([]justcode.ProjectSkill, error) {
		return initSkillFixtureCatalogue(), nil
	}
	var events []string
	huhRun = func(*huh.Form) error {
		events = append(events, "form")
		return nil
	}
	remaining := append([]bool(nil), confirms...)
	initEmptySkillsConfirmFn = func() (bool, error) {
		events = append(events, "confirm")
		if len(remaining) == 0 {
			t.Fatal("the zero-skills confirmation ran more often than scripted")
		}
		answer := remaining[0]
		remaining = remaining[1:]
		return answer, nil
	}
	t.Cleanup(func() {
		projectSkillCatalogueFn, huhRun, initEmptySkillsConfirmFn = originalCatalogue, originalHuhRun, originalConfirm
	})
	set := map[string]bool{"root": true, "github": true, "skills-storage": true}
	if mcpsFlag {
		set["mcps"] = true
	}
	opts := initOptions{Set: set, hostCPUs: 8, hostMemoryMB: 16384, maxCPUs: 8, maxMemoryMB: 8192}
	got, err := askInitQuestionsHuh(opts, justcode.InitAnswers{
		Root: root, SkillsSet: true, Skills: skills,
		MCPsSet: true, MCPConnectors: []string{"github"},
	})
	if err != nil {
		t.Fatalf("askInitQuestionsHuh: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("only %d of %d scripted confirmations ran (events: %v)", len(confirms)-len(remaining), len(confirms), events)
	}
	return events, got
}

// TestInitHuhEmptySkillsConfirmsBeforeMCP: leaving an empty skills selection
// triggers exactly ONE confirmation, BEFORE the MCP form (#307), and
// accepting keeps zero skills through the recap.
func TestInitHuhEmptySkillsConfirmsBeforeMCP(t *testing.T) {
	events, got := initEmptySkillsSequence(t, false, nil, []bool{true})
	if !equalStringSlices(events, []string{"form", "confirm", "form"}) {
		t.Fatalf("sequence = %v, want skills form, confirmation, MCP form", events)
	}
	if !got.SkillsSet || len(got.Skills) != 0 {
		t.Fatalf("an accepted empty selection must stay explicitly empty: %+v", got)
	}
	if !equalStringSlices(got.MCPConnectors, []string{"github"}) {
		t.Fatalf("MCP answers lost: %+v", got)
	}
}

// TestInitHuhEmptySkillsConfirmWithoutMCPStep: the same single confirmation
// when no MCP step runs (flag-provided MCPs).
func TestInitHuhEmptySkillsConfirmWithoutMCPStep(t *testing.T) {
	events, got := initEmptySkillsSequence(t, true, nil, []bool{true})
	if !equalStringSlices(events, []string{"form", "confirm"}) {
		t.Fatalf("sequence = %v, want skills form then confirmation only", events)
	}
	if !got.SkillsSet || len(got.Skills) != 0 {
		t.Fatalf("an accepted empty selection must stay explicitly empty: %+v", got)
	}
}

// TestInitHuhEmptySkillsDeclineReopensEmptySelection: declining returns to
// the skills form with the selection still EMPTY (the default is not
// restored), and each transition asks exactly once.
func TestInitHuhEmptySkillsDeclineReopensEmptySelection(t *testing.T) {
	events, got := initEmptySkillsSequence(t, false, nil, []bool{false, true})
	want := []string{"form", "confirm", "form", "confirm", "form"}
	if !equalStringSlices(events, want) {
		t.Fatalf("sequence = %v, want %v", events, want)
	}
	if len(got.Skills) != 0 {
		t.Fatalf("a declined retry must not restore the default skill: %+v", got)
	}
}

// TestInitHuhNonEmptySkillsAskNoConfirmation: a non-empty selection never
// triggers the confirmation.
func TestInitHuhNonEmptySkillsAskNoConfirmation(t *testing.T) {
	events, got := initEmptySkillsSequence(t, false, []string{"official/rgaa"}, nil)
	if !equalStringSlices(events, []string{"form", "form"}) {
		t.Fatalf("sequence = %v, want skills form then MCP form, no confirmation", events)
	}
	if !equalStringSlices(got.Skills, []string{"official/rgaa"}) {
		t.Fatalf("skills = %+v, want the submitted selection", got)
	}
}

// TestInitHuhEmptySkillsAcceptedOnceAcrossMCPRetry drives the real Huh
// selections: clear skills once, clear and decline MCP, then clear and accept
// it on retry. The accepted zero-skills decision must not be asked again (#307).
func TestInitHuhEmptySkillsAcceptedOnceAcrossMCPRetry(t *testing.T) {
	root := initTestProject(t)
	originalCatalogue, originalHuhRun, originalConfirm := projectSkillCatalogueFn, huhRun, initEmptySkillsConfirmFn
	projectSkillCatalogueFn = func(context.Context) ([]justcode.ProjectSkill, error) {
		return initSkillFixtureCatalogue(), nil
	}
	var events []string
	skillsCleared := false
	mcpConfirmations := 0
	huhRun = func(form *huh.Form) error {
		form.Init()
		_, _ = form.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
		view := ansi.Strip(form.View())
		switch {
		case strings.Contains(view, "Aucun MCP sélectionné"):
			mcpConfirmations++
			if mcpConfirmations == 1 {
				events = append(events, "mcp-decline")
				return nil // the confirmation's default is to return to the MCP form
			}
			events = append(events, "mcp-accept")
			_, _ = form.Update(tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"}))
		case strings.Contains(view, "MCP (facultatif)"):
			events = append(events, "mcp-clear")
			_, _ = form.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
		case strings.Contains(view, "Une skill apporte"):
			events = append(events, "skills")
			if !skillsCleared {
				_, _ = form.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
				skillsCleared = true
			}
		default:
			t.Fatalf("unexpected form:\n%s", view)
		}
		return nil
	}
	initEmptySkillsConfirmFn = func() (bool, error) {
		events = append(events, "skills-confirm")
		return true, nil
	}
	t.Cleanup(func() {
		projectSkillCatalogueFn, huhRun, initEmptySkillsConfirmFn = originalCatalogue, originalHuhRun, originalConfirm
	})
	opts := initOptions{Set: map[string]bool{"root": true, "github": true, "skills-storage": true},
		hostCPUs: 8, hostMemoryMB: 16384, maxCPUs: 8, maxMemoryMB: 8192}
	got, err := askInitQuestionsHuh(opts, justcode.InitAnswers{
		Root: root, SkillsSet: true, Skills: []string{"official/securite-developpement"},
		MCPsSet: true, MCPConnectors: []string{"data-gouv"},
	})
	if err != nil {
		t.Fatalf("askInitQuestionsHuh: %v", err)
	}
	wantEvents := []string{"skills", "skills-confirm", "mcp-clear", "mcp-decline", "skills", "mcp-clear", "mcp-accept"}
	if !equalStringSlices(events, wantEvents) {
		t.Fatalf("events = %v, want %v", events, wantEvents)
	}
	if len(got.Skills) != 0 || len(got.MCPConnectors) != 0 {
		t.Fatalf("confirmed empty selections were not preserved: %+v", got)
	}
}

// Regression test for the zero-skills retry loop (#307): the skills and
// storage choices are committed when the user leaves the skills selection,
// BEFORE the empty-selection confirmation, because the next form pass
// reseeds from answers and would otherwise reset them. The MCP choice is
// committed at its own later step and survives just the same.
func TestCommitInitHuhSkillsKeepsSiblingChoicesForEmptySkillsRetry(t *testing.T) {
	options := initSkillOptions(initSkillFixtureCatalogue())
	answers := justcode.InitAnswers{
		Skills:          []string{"official/rgaa"},
		MCPConnectors:   []string{"github"},
		SkillsLocalOnly: false,
	}
	opts := initOptions{Set: map[string]bool{}}
	if !commitInitHuhSkills(opts, &answers, nil, true, options, false) {
		t.Fatal("an empty skills submission must request the zero-skills confirmation")
	}
	if !answers.SkillsSet || len(answers.Skills) != 0 {
		t.Fatalf("empty submission = %+v", answers)
	}
	if !answers.SkillsLocalOnlySet || !answers.SkillsLocalOnly {
		t.Fatalf("sibling storage choice lost before the retry: %+v", answers)
	}
	retryMCPs, err := commitInitHuhMCPs(opts, &answers, []string{"github", "browser"})
	if err != nil {
		t.Fatal(err)
	}
	if retryMCPs {
		t.Fatal("a non-empty MCP submission must not reopen the form")
	}
	if !answers.MCPsSet || !equalStringSlices(answers.MCPConnectors, []string{"github", "browser"}) {
		t.Fatalf("sibling MCP choice lost before the retry: %+v", answers)
	}
}

func TestCommitInitHuhSkillsRecordsRetainedSkills(t *testing.T) {
	options := initSkillOptions(initSkillFixtureCatalogue())
	answers := justcode.InitAnswers{Skills: []string{"official/rgaa", "official/ancienne"}}
	opts := initOptions{Set: map[string]bool{"mcps": true}}
	if commitInitHuhSkills(opts, &answers, []string{"official/rgaa", "official/ancienne"}, false, options, false) {
		t.Fatal("a non-empty skills submission must not request any confirmation")
	}
	if answers.SkillsSet {
		t.Fatalf("an unchanged submission must not mark the selection explicit: %+v", answers)
	}
	if !equalStringSlices(answers.RetainedSkills, []string{"official/ancienne"}) {
		t.Fatalf("retained skills = %v, want the out-of-catalogue selection", answers.RetainedSkills)
	}
}

// Declining the zero-MCP confirmation must reopen the form without writing
// the empty MCP selection back: the next pass reseeds from answers, like the
// line wizard.
func TestCommitInitHuhMCPsDeclinedConfirmationKeepsPreviousSelection(t *testing.T) {
	originalHuhRun := huhRun
	// confirmNoMCPsHuh binds a false keep value, so a no-op stub declines.
	huhRun = func(*huh.Form) error { return nil }
	t.Cleanup(func() { huhRun = originalHuhRun })
	answers := justcode.InitAnswers{
		Skills:        []string{"official/rgaa"},
		MCPConnectors: []string{"github"},
	}
	opts := initOptions{Set: map[string]bool{}}
	retryMCPs, err := commitInitHuhMCPs(opts, &answers, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !retryMCPs {
		t.Fatal("declining the zero-MCP confirmation must reopen the form")
	}
	if !equalStringSlices(answers.MCPConnectors, []string{"github"}) {
		t.Fatalf("declining must keep the previous MCP selection: %+v", answers)
	}
	if answers.MCPsSet {
		t.Fatalf("declining must not mark the MCP selection explicit: %+v", answers)
	}
}

func TestHuhDiagnosticsPreservesLiteralPaths(t *testing.T) {
	path := `C:\Users\Luis\project_with_under`
	got := huhDiagnosticsDescription(justcode.DiagnosticsReport{
		Sections: []justcode.DiagnosticSection{{
			Title: "Host capability",
			Items: []justcode.DiagnosticItem{{Name: "Project path", Value: path, Status: justcode.DiagnosticInfo}},
		}},
	})
	if !strings.Contains(got, path) {
		t.Fatalf("TUI report changed a literal path: %q", got)
	}
	if strings.Contains(got, `\\_`) || strings.Contains(got, `\\\\`) {
		t.Fatalf("TUI report Markdown-escaped copied support data: %q", got)
	}
}

func TestHuhDiagnosticsPreservesWindowsSeparatorsAndUnderscores(t *testing.T) {
	backslash := string(rune(92))
	path := strings.Join([]string{"C:", "Users", "Luis", "project_with_under"}, backslash)
	got := huhDiagnosticsDescription(justcode.DiagnosticsReport{
		Sections: []justcode.DiagnosticSection{{
			Title: "Host capability",
			Items: []justcode.DiagnosticItem{{Name: "Project path", Value: path, Status: justcode.DiagnosticInfo}},
		}},
	})
	if !strings.Contains(got, path) {
		t.Fatalf("TUI report changed a Windows path: %q", got)
	}
	if strings.Contains(got, backslash+backslash) || strings.Contains(got, backslash+"_") {
		t.Fatalf("TUI report escaped literal support data: %q", got)
	}
}

func TestAskInitAdvancedHuhEscKeepsAnswers(t *testing.T) {
	originalRun, originalInstall := runInitAdvancedForm, runtimeInstallHintFn
	runtimeInstallHintFn = func(justcode.Runtime) (bool, string) { return true, "" }
	runInitAdvancedForm = func(*huh.Form) (bool, error) { return true, nil }
	t.Cleanup(func() { runInitAdvancedForm, runtimeInstallHintFn = originalRun, originalInstall })
	before := justcode.InitAnswers{Root: "/tmp/example", Runtime: justcode.RuntimeTart, CPUs: 4}
	opts := initOptions{hostCPUs: 8, hostMemoryMB: 16384, maxCPUs: 8, maxMemoryMB: 16384}
	after, err := askInitAdvancedHuh(opts, before)
	if err != nil {
		t.Fatalf("Escape returns to the recap, got %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("esc must not change a single answer: %+v became %+v", before, after)
	}
}

// TestAskInitAdvancedHuhSubmitKeepsDefaults pins that a completed form leaves
// accepted defaults implicit (zero CPU/memory answers), so the project keeps
// inheriting built-in default changes.
func TestAskInitAdvancedHuhSubmitKeepsDefaults(t *testing.T) {
	originalRun := runInitAdvancedForm
	runInitAdvancedForm = func(form *huh.Form) (bool, error) {
		picker := form.GetFocusedField().(*huh.Select[string])
		picker.Focus()
		for i := 0; i < 6; i++ {
			picker.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		}
		picker.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		return false, nil
	}
	t.Cleanup(func() { runInitAdvancedForm = originalRun })
	originalInstallHint := runtimeInstallHintFn
	runtimeInstallHintFn = func(justcode.Runtime) (bool, string) { return true, "" }
	t.Cleanup(func() { runtimeInstallHintFn = originalInstallHint })
	opts := initOptions{hostCPUs: 8, hostMemoryMB: 16384, maxCPUs: 8, maxMemoryMB: 16384}
	answers, err := askInitAdvancedHuh(opts, justcode.InitAnswers{Root: "/tmp/example"})
	if err != nil {
		t.Fatalf("askInitAdvancedHuh: %v", err)
	}
	if answers.CPUs != 0 || answers.MemoryMB != 0 {
		t.Fatalf("accepted defaults must stay implicit, got %d/%d", answers.CPUs, answers.MemoryMB)
	}
	if answers.Runtime != "" || answers.Isolation != "" {
		t.Fatalf("opening advanced settings must not materialize defaults: %+v", answers)
	}
}

func TestInitAdvancedRetainsAcceptedEditOnEscape(t *testing.T) {
	originalRun, originalInstall := runInitAdvancedForm, runtimeInstallHintFn
	t.Cleanup(func() { runInitAdvancedForm, runtimeInstallHintFn = originalRun, originalInstall })
	runtimeInstallHintFn = func(justcode.Runtime) (bool, string) { return true, "" }
	calls := 0
	runInitAdvancedForm = func(form *huh.Form) (bool, error) {
		calls++
		field := form.GetFocusedField()
		field.Focus()
		switch calls {
		case 1:
			field.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			field.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			field.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		case 2:
			if _, ok := field.(*huh.Input); !ok {
				t.Fatalf("expected CPU editor, got %T", field)
			}
			field.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
			field.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
			field.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		case 3:
			return true, nil
		default:
			t.Fatal("advanced screen failed to return after Escape")
		}
		return false, nil
	}
	opts := initOptions{hostCPUs: 8, hostMemoryMB: 16384, maxCPUs: 8, maxMemoryMB: 16384}
	before := justcode.InitAnswers{Root: "/tmp/example", Skills: []string{"official/rgaa"}, CredentialRef: "work"}
	after, err := askInitAdvancedHuh(opts, before)
	if err != nil {
		t.Fatal(err)
	}
	want := before
	want.CPUs = 3
	if !reflect.DeepEqual(after, want) {
		t.Fatalf("accepted edit lost on Escape: got %+v, want %+v", after, want)
	}
}

func TestInitAdvancedCtrlCCancels(t *testing.T) {
	originalRun, originalInstall := runInitAdvancedForm, runtimeInstallHintFn
	t.Cleanup(func() { runInitAdvancedForm, runtimeInstallHintFn = originalRun, originalInstall })
	runtimeInstallHintFn = func(justcode.Runtime) (bool, string) { return true, "" }
	runInitAdvancedForm = func(*huh.Form) (bool, error) { return false, huh.ErrUserAborted }
	_, err := askInitAdvancedHuh(initOptions{}, justcode.InitAnswers{})
	if !errors.Is(err, huh.ErrUserAborted) {
		t.Fatalf("Ctrl+C must cancel, got %v", err)
	}
}

func TestInitHuhRepairsInheritedResourcesBeforeReview(t *testing.T) {
	root := initTestProject(t)
	writeManifest(t, root, justcode.ProjectManifest{Runtime: "microsandbox", Isolation: "full", CPUs: 64, MemoryMB: 4096})
	originalHuh, originalRun, originalApplyRun, originalAdvanced := wizardHuhFn, huhRun, huhApplyRun, runInitAdvancedForm
	t.Cleanup(func() {
		wizardHuhFn, huhRun, huhApplyRun, runInitAdvancedForm = originalHuh, originalRun, originalApplyRun, originalAdvanced
	})
	wizardHuhFn = func() bool { return true }
	huhRun = func(*huh.Form) error { return nil }
	huhApplyRun = func(*huh.Form) error { return nil }
	calls := 0
	runInitAdvancedForm = func(form *huh.Form) (bool, error) {
		calls++
		field := form.GetFocusedField()
		field.Focus()
		switch calls {
		case 1:
			field.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			field.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			field.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		case 2:
			field.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
			field.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
			field.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		case 3:
			return true, nil
		default:
			t.Fatal("repair reopened after becoming valid")
		}
		return false, nil
	}
	opts := initOptions{Root: root, Replace: true, Set: map[string]bool{"root": true, "github": true, "skills": true, "mcps": true}}
	code, err := initRun(opts, bufio.NewReader(strings.NewReader("")), true)
	if err != nil || code != 0 || calls != 3 {
		t.Fatalf("huh repair failed: code=%d calls=%d err=%v", code, calls, err)
	}
	got, err := justcode.ReadProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(root))
	if err != nil || got.CPUs != 3 {
		t.Fatalf("huh repair not applied: %+v, %v", got, err)
	}
}

func TestInitAdvancedModelEditSurvivesEscape(t *testing.T) {
	originalRun, originalInstall := runInitAdvancedForm, runtimeInstallHintFn
	t.Cleanup(func() { runInitAdvancedForm, runtimeInstallHintFn = originalRun, originalInstall })
	runtimeInstallHintFn = func(justcode.Runtime) (bool, string) { return true, "" }
	stubCatalogueCheck(t, func(root, model string) (string, error) {
		if root != "/tmp/example" || model != "albert/qwen3-coder" {
			t.Fatalf("unexpected catalogue check: %q, %q", root, model)
		}
		return "", nil
	})
	calls := 0
	runInitAdvancedForm = func(form *huh.Form) (bool, error) {
		calls++
		field := form.GetFocusedField()
		field.Focus()
		switch calls {
		case 1:
			for i := 0; i < 4; i++ {
				field.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			}
			field.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		case 2:
			if _, ok := field.(*huh.Input); !ok {
				t.Fatalf("expected model input, got %T", field)
			}
			for _, r := range "qwen3-coder" {
				field.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
			}
		case 3:
			return true, nil
		default:
			t.Fatal("unexpected form after Escape")
		}
		return false, nil
	}
	opts := initOptions{hostCPUs: 8, hostMemoryMB: 16384, maxCPUs: 8, maxMemoryMB: 16384}
	after, err := askInitAdvancedHuh(opts, justcode.InitAnswers{Root: "/tmp/example"})
	if err != nil || after.Model != "albert/qwen3-coder" {
		t.Fatalf("model edit lost: %+v, %v", after, err)
	}
}

func TestInitSmallModelSelectorOffersMinistralAndInheritance(t *testing.T) {
	value := ""
	content := newInitSmallModelSelect("project", &value).View()
	for _, want := range []string{"Ministral 3 8B", "Même modèle que le modèle principal", "titres de session"} {
		if !strings.Contains(content, want) {
			t.Fatalf("small-model selector missing %q:\n%s", want, content)
		}
	}
}

func TestInitAdvancedSmallModelSelectionAndReset(t *testing.T) {
	originalRun, originalInstall := runInitAdvancedForm, runtimeInstallHintFn
	t.Cleanup(func() { runInitAdvancedForm, runtimeInstallHintFn = originalRun, originalInstall })
	runtimeInstallHintFn = func(justcode.Runtime) (bool, string) { return true, "" }
	stubCatalogueCheck(t, func(root, model string) (string, error) {
		if root != "/tmp/example" || model != justcode.MinistralSmallModelRef {
			t.Fatalf("unexpected small-model validation: %q, %q", root, model)
		}
		return "", nil
	})
	for _, tc := range []struct {
		name   string
		before string
		after  string
		down   int
	}{
		{name: "select Ministral", before: "", after: justcode.MinistralSmallModelRef, down: 1},
		{name: "restore main-model inheritance", before: justcode.MinistralSmallModelRef, after: "", down: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			runInitAdvancedForm = func(form *huh.Form) (bool, error) {
				calls++
				field := form.GetFocusedField()
				field.Focus()
				switch calls {
				case 1:
					for i := 0; i < 5; i++ {
						field.Update(tea.KeyPressMsg{Code: tea.KeyDown})
					}
					field.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				case 2:
					if _, ok := field.(*huh.Select[string]); !ok {
						t.Fatalf("expected small-model selector, got %T", field)
					}
					for i := 0; i < tc.down; i++ {
						field.Update(tea.KeyPressMsg{Code: tea.KeyDown})
					}
					for i := 0; i > tc.down; i-- {
						field.Update(tea.KeyPressMsg{Code: tea.KeyUp})
					}
					field.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				case 3:
					return true, nil
				default:
					t.Fatal("advanced screen failed to return after small-model edit")
				}
				return false, nil
			}
			before := justcode.InitAnswers{Root: "/tmp/example", Model: "albert/qwen3-coder", SmallModel: tc.before}
			after, err := askInitAdvancedHuh(initOptions{hostCPUs: 8, hostMemoryMB: 16384, maxCPUs: 8, maxMemoryMB: 16384}, before)
			if err != nil || after.SmallModel != tc.after || after.Model != before.Model {
				t.Fatalf("small-model edit = %+v, want %q, err=%v", after, tc.after, err)
			}
		})
	}
}

func TestInitModelInputDefaultsAndValidation(t *testing.T) {
	calls := 0
	stubCatalogueCheck(t, func(root, model string) (string, error) {
		calls++
		if root != "project" || model != "albert/qwen3-coder" {
			t.Fatalf("unexpected catalogue arguments: %q, %q", root, model)
		}
		return "catalogue unavailable", nil
	})
	for _, value := range []string{"", "  ", "défaut", "default"} {
		warning, err := validateInitModelInput("project", value)
		if warning != "" || err != nil || normalizeInitModelInput(value) != "" {
			t.Fatalf("default %q failed: %s, %v", value, warning, err)
		}
	}
	if calls != 0 {
		t.Fatal("inherited model should not validate an empty identifier")
	}
	warning, err := validateInitModelInput("project", " qwen3-coder ")
	if err != nil || warning != "catalogue unavailable" || calls != 1 {
		t.Fatalf("validation result: %s, %v, calls=%d", warning, err, calls)
	}
}

func TestInitAdvancedEditorEscapeKeepsPriorValue(t *testing.T) {
	originalRun, originalInstall := runInitAdvancedForm, runtimeInstallHintFn
	t.Cleanup(func() { runInitAdvancedForm, runtimeInstallHintFn = originalRun, originalInstall })
	runtimeInstallHintFn = func(justcode.Runtime) (bool, string) { return true, "" }
	calls := 0
	runInitAdvancedForm = func(form *huh.Form) (bool, error) {
		calls++
		field := form.GetFocusedField()
		field.Focus()
		switch calls {
		case 1:
			field.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			field.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			field.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			return false, nil
		case 2:
			field.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
			field.Update(tea.KeyPressMsg{Code: '9', Text: "9"})
			return true, nil
		case 3:
			return true, nil
		default:
			t.Fatal("unexpected form after Escape")
			return false, nil
		}
	}
	before := justcode.InitAnswers{CPUs: 4, MemoryMB: 8192}
	after, err := askInitAdvancedHuh(initOptions{hostCPUs: 8, hostMemoryMB: 16384, maxCPUs: 8, maxMemoryMB: 16384}, before)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("cancelled editor overwrote prior settings: %+v, %v", after, err)
	}
}

func TestInitRuntimeNoticeNamesOnlySharedRuntimes(t *testing.T) {
	original := runtimeInstallHintFn
	t.Cleanup(func() { runtimeInstallHintFn = original })
	for _, rt := range []justcode.Runtime{justcode.RuntimeMicrosandbox, justcode.RuntimeTart, justcode.RuntimeAgentVM} {
		runtimeInstallHintFn = func(got justcode.Runtime) (bool, string) {
			if got != rt {
				t.Fatalf("probe = %q, want %q", got, rt)
			}
			return false, "install-example"
		}
		got := initRuntimeNotice(rt)
		if !strings.Contains(got, "install-example") {
			t.Fatalf("missing installation hint: %s", got)
		}
		shared := strings.Contains(got, "y compris .env")
		if shared != (rt != justcode.RuntimeMicrosandbox) {
			t.Fatalf("incorrect sharing warning for %s: %s", rt, got)
		}
	}
}

func TestHuhResourceInputsCanRestoreImplicitDefaults(t *testing.T) {
	for _, input := range []string{"", "  ", "default", "DEFAULT", "défaut", "DÉFAUT"} {
		cpus, useDefault, err := parseGuestCPUsInput(input, 8)
		if err != nil || !useDefault || cpus != 0 {
			t.Errorf("CPU input %q = %d, %v, %v; want implicit default", input, cpus, useDefault, err)
		}
		memory, useDefault, err := parseGuestMemoryInput(input, 8192)
		if err != nil || !useDefault || memory != 0 {
			t.Errorf("memory input %q = %d, %v, %v; want implicit default", input, memory, useDefault, err)
		}
	}
	if cpus, reset, err := parseGuestCPUsInput("4", 8); err != nil || reset || cpus != 4 {
		t.Fatalf("explicit CPU input = %d, %v, %v; want 4", cpus, reset, err)
	}
	if memory, reset, err := parseGuestMemoryInput("2.5G", 8192); err != nil || reset || memory != 2560 {
		t.Fatalf("explicit memory input = %d, %v, %v; want 2560 MiB", memory, reset, err)
	}
}

func TestHuhApplyChoiceDiffBindingReopensWithoutApplying(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	originalRun := huhApplyRun
	t.Cleanup(func() { huhApplyRun = originalRun })
	plan := justcode.InitPlan{InstructionsChanged: true}
	calls := 0
	showDiff := false
	program := tea.NewProgram(setupModelPicker{}, huhApplyKeyFilter(true, &showDiff),
		tea.WithInput(strings.NewReader("d")), tea.WithOutput(&strings.Builder{}), tea.WithoutRenderer())
	if _, err := program.Run(); err != nil || !showDiff {
		t.Fatalf("real Bubble Tea input did not trigger the d binding: %v, showDiff=%v", err, showDiff)
	}
	showDiff = false
	var output strings.Builder
	huhApplyRun = func(form *huh.Form) error {
		calls++
		input := "\r"
		if calls == 1 {
			input = "d\r"
		}
		form.Init()
		_, _ = form.Update(tea.WindowSizeMsg{Width: 100, Height: 60})
		output.WriteString(form.View())
		return form.WithInput(strings.NewReader(input)).WithOutput(&output).Run()
	}
	choice, err := huhApplyChoice(plan)
	if err != nil || choice != "apply" || calls != 2 {
		t.Fatalf("real d binding then Apply = %q, %v, calls=%d\n%s", choice, err, calls, output.String())
	}
	calls = 0
	huhApplyRun = func(form *huh.Form) error {
		calls++
		return form.WithInput(strings.NewReader("d\r")).WithOutput(&output).Run()
	}
	plan.InstructionsChanged = false
	choice, err = huhApplyChoice(plan)
	if err != nil || choice != "apply" || calls != 1 || showDiff {
		t.Fatalf("d must stay inert without an AGENTS.md change: %q, %v, calls=%d, showDiff=%v", choice, err, calls, showDiff)
	}
}

func TestHuhApplyAccessiblePromptPreservesExplicitAnswers(t *testing.T) {
	options := []huh.Option[string]{huh.NewOption("Appliquer", "apply"), huh.NewOption("Modifier", "edit")}
	for _, tc := range []struct {
		input string
		want  string
	}{
		{input: "\n", want: "apply"},
		{input: "2\n", want: "edit"},
		{input: "2", want: "edit"},
		{input: "99\n2\n", want: "edit"},
	} {
		choice, err := huhApplyAccessiblePrompt(options, bufio.NewReader(strings.NewReader(tc.input)))
		if err != nil || choice != tc.want {
			t.Errorf("input %q = %q, %v; want %q", tc.input, choice, err, tc.want)
		}
	}
	for _, input := range []string{"", "99\n", "abc\n"} {
		if choice, err := huhApplyAccessiblePrompt(options, bufio.NewReader(strings.NewReader(input))); choice != "" || !errors.Is(err, huh.ErrUserAborted) {
			t.Errorf("EOF after %q must cancel: choice=%q err=%v", input, choice, err)
		}
	}
}

func stubAccessibleInput(t *testing.T, content string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = previous; _ = r.Close() })
}

func TestHuhApplyChoiceAccessibleDiffEditAndQuit(t *testing.T) {
	t.Setenv("TERM", "dumb")
	originalHuh := huhAccessibleModeFn
	t.Cleanup(func() { huhAccessibleModeFn = originalHuh })
	huhAccessibleModeFn = func() bool { return true }
	plan := justcode.InitPlan{
		Answers:             justcode.InitAnswers{Root: "/tmp/project"},
		InstructionsChanged: true,
		InstructionsAfter:   "<!-- BEGIN JUST-CODE MANAGED SKILLS -->\nContenu géré.\n<!-- END JUST-CODE MANAGED SKILLS -->\n",
	}
	out := captureStdout(t, func() {
		stubAccessibleInput(t, "5\n1\n")
		choice, err := huhApplyChoice(plan)
		if err != nil || choice != "apply" {
			t.Fatalf("accessible diff then Apply = %q, %v", choice, err)
		}
	})
	if !strings.Contains(out, "Diff de la section just-code dans AGENTS.md") || !strings.Contains(out, "<!-- BEGIN JUST-CODE MANAGED SKILLS -->") || !strings.Contains(out, "Contenu géré.") {
		t.Fatalf("accessible mode did not display the exact managed diff:\n%s", out)
	}
	stubAccessibleInput(t, "2\n")
	if choice, err := huhApplyChoice(plan); err != nil || choice != "edit" {
		t.Fatalf("accessible Edit = %q, %v", choice, err)
	}
	stubAccessibleInput(t, "4\n")
	if choice, err := huhApplyChoice(plan); err != nil || choice != "cancel" {
		t.Fatalf("accessible Quit = %q, %v", choice, err)
	}
	for _, input := range []string{"", "99\n", "not-a-number\n"} {
		stubAccessibleInput(t, input)
		if choice, err := huhApplyChoice(plan); choice != "" || !errors.Is(err, huh.ErrUserAborted) {
			t.Fatalf("accessible EOF after %q must cancel: choice=%q err=%v", input, choice, err)
		}
	}
}

func TestHuhApplyChoiceSmallTerminalKeepsTextAndChoicesReachable(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	originalRun := huhApplyRun
	t.Cleanup(func() { huhApplyRun = originalRun })
	plan := justcode.InitPlan{
		Answers: justcode.InitAnswers{
			Root: "/tmp/project", Runtime: justcode.RuntimeMicrosandbox, Isolation: justcode.IsolationFull,
			MCPConnectors:           []string{"data-gouv", "chrome-devtools"},
			BrowserResourceGuidance: "Navigateur : pic de mémoire non mesuré ; les autres applications du poste doivent conserver de la marge.",
		},
		GuestCPUs: 2, GuestMemoryMB: 4096,
		InstructionsChanged: true,
		InstructionsAfter:   "<!-- BEGIN JUST-CODE MANAGED SKILLS -->\n" + strings.Repeat("Ligne du diff à lire.\n", 20) + "<!-- END JUST-CODE MANAGED SKILLS -->\n",
		Warnings:            []string{"un avertissement qui doit rester lisible"},
	}
	var output strings.Builder
	calls := 0
	huhApplyRun = func(form *huh.Form) error {
		calls++
		_, _ = form.Update(tea.WindowSizeMsg{Width: 48, Height: 15})
		view := form.View()
		if height := lipgloss.Height(view); height > 15 {
			t.Fatalf("action form exceeds the short terminal: %d lines\n%s", height, view)
		}
		for _, want := range []string{"Appliquer", "Modifier", "Réglages avancés", "Quitter"} {
			if !strings.Contains(view, want) {
				t.Fatalf("short terminal hides %q:\n%s", want, view)
			}
		}
		output.WriteString(view)
		input := "d"
		if calls == 2 {
			input = "\x1b[B\x1b[B\x1b[B\r"
		}
		return form.WithInput(strings.NewReader(input)).WithOutput(&output).Run()
	}
	var choice string
	var err error
	out := captureStdout(t, func() {
		choice, err = huhApplyChoice(plan)
	})
	if err != nil || choice != "cancel" || calls != 2 {
		t.Fatalf("diff then Quit = %q, %v, calls=%d", choice, err, calls)
	}
	joined := out + output.String()
	for _, want := range []string{"data-gouv", "chrome-devtools", "pic de mémoire", "un avertissement qui doit rester lisible", "Fichiers modifiés", "Ligne du diff à lire.", "<!-- BEGIN JUST-CODE MANAGED SKILLS -->"} {
		if !strings.Contains(joined, want) {
			t.Errorf("scrollback recap never displays %q:\n%s", want, joined)
		}
	}
}

func TestHuhReviewDescriptionUsesFrenchRecapAndKeepsWarnings(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	plan := justcode.InitPlan{
		Answers: justcode.InitAnswers{
			Root: "/tmp/example_project", Runtime: justcode.RuntimeMicrosandbox,
			Isolation: justcode.IsolationFull, CredentialRef: "work",
			MCPConnectors: []string{"data-gouv"}, Skills: []string{"official/rgaa"},
		},
		GuestCPUs: 2, GuestMemoryMB: 4096,
		MCPChecks: []justcode.InitMCPCheck{{
			ID: "data-gouv", Endpoint: "https://mcp.data.gouv.fr/mcp", Checked: true, Reachable: true,
		}},
		FileChanges: []justcode.InitFileChange{{
			Path: "/tmp/example_project/.just-code/project.json", Description: "configuration du projet",
		}},
		Warnings: []string{"la machine isolée utilisera les 2 processeurs logiques du poste"},
	}
	got := huhReviewDescription(plan)
	for _, want := range []string{`example\_project`, "4 Go", "rgaa", "✅ joignable", "Attention", "les 2 processeurs logiques du poste", "configuration du projet"} {
		if !strings.Contains(got, want) {
			t.Errorf("Huh recap lacks %q: %s", want, got)
		}
	}
	if strings.Contains(got, "None selected") || strings.Contains(got, "sha256") {
		t.Fatalf("Huh recap must use the concise French contract: %s", got)
	}
	t.Setenv("TERM", "dumb")
	if accessible := huhReviewDescription(plan); !strings.Contains(accessible, "example_project") || strings.Contains(accessible, `example\_project`) {
		t.Fatalf("accessible recap must retain the literal project path: %s", accessible)
	}
}

func TestHuhReviewDescriptionEscapesMarkupOnlyForStyledRenderer(t *testing.T) {
	plan := justcode.InitPlan{
		Answers:   justcode.InitAnswers{Root: "/tmp/project_with_under"},
		GuestCPUs: 2, GuestMemoryMB: 4096,
	}
	t.Setenv("TERM", "xterm-256color")
	styled := huhReviewDescription(plan)
	if !strings.Contains(styled, `project\_with\_under`) {
		t.Fatalf("styled Huh note must escape underscore markup: %s", styled)
	}
	t.Setenv("TERM", "dumb")
	accessible := huhReviewDescription(plan)
	if !strings.Contains(accessible, "project_with_under") || strings.Contains(accessible, `project\_with\_under`) {
		t.Fatalf("accessible Huh note must preserve the raw path: %s", accessible)
	}
}

func TestHuhInstructionsDiffPreservesHTMLCommentMarkers(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	plan := justcode.InitPlan{
		InstructionsChanged: true,
		InstructionsAfter:   "<!-- BEGIN JUST-CODE MANAGED SKILLS -->\nmanaged text\n<!-- END JUST-CODE MANAGED SKILLS -->\n",
	}
	styled := escapeHuhNoteMarkup(justcode.FormatInitInstructionsDiff(plan))
	if !strings.Contains(styled, `\<!-- BEGIN JUST-CODE MANAGED SKILLS --\>`) {
		t.Fatalf("styled diff must preserve the leading HTML comment marker: %s", styled)
	}
	t.Setenv("TERM", "dumb")
	plain := escapeHuhNoteMarkup(justcode.FormatInitInstructionsDiff(plan))
	if !strings.Contains(plain, "<!-- BEGIN JUST-CODE MANAGED SKILLS -->") {
		t.Fatalf("plain diff must preserve literal HTML comment markers: %s", plain)
	}
}

func TestIsTTYFileRejectsDevNull(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("cannot open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()
	if isTTYFile(devNull) {
		t.Fatalf("%s must not be accepted as an interactive output terminal", os.DevNull)
	}
}

func TestRetainedSkillOptionsPreserveUnavailablePins(t *testing.T) {
	catalogue := []justcode.ProjectSkill{{ID: "official/securite-developpement"}}
	options := retainedSkillOptions(initSkillOptions(catalogue), []string{
		"official/securite-developpement", "official/rgaa", "experimental/rag-parse",
	})
	if len(options) != 3 {
		t.Fatalf("options = %d, want one offered and two retained", len(options))
	}
	if options[0].Value != "official/securite-developpement" ||
		options[1].Value != "official/rgaa" || options[2].Value != "experimental/rag-parse" {
		t.Fatalf("retained option values = %v, %v, %v", options[0].Value, options[1].Value, options[2].Value)
	}
	for _, option := range options[1:] {
		if !strings.Contains(option.Key, "déjà configurée, non proposée") {
			t.Errorf("retained option %q is not distinguished from new choices", option.Key)
		}
	}
	selected := []string{"official/securite-developpement", "official/rgaa", "experimental/rag-parse"}
	localOnly := false
	content := newInitSkillsGroup(options, &selected, &localOnly, true, false, "").Content()
	for _, id := range []string{"official/rgaa (déjà configurée, non proposée)", "experimental/rag-parse (déjà configurée, non proposée)"} {
		if !strings.Contains(content, id) {
			t.Errorf("retained option is missing from the form: %s", content)
		}
	}
}

func TestInitSkillsStepPrefixIsOmittedForPartialWizard(t *testing.T) {
	if got := initSkillsStepPrefix(initOptions{}); got != "[1/3] " {
		t.Fatalf("full wizard prefix = %q", got)
	}
	if got := initSkillsStepPrefix(initOptions{Set: map[string]bool{"root": true}}); got != "[1/3] " {
		t.Fatalf("a displayed or explicit root must keep the beginner prefix = %q", got)
	}
	for _, field := range []string{"runtime", "isolation", "model", "cpus", "memory-mb", "credential-ref", "github", "skills", "mcps"} {
		if got := initSkillsStepPrefix(initOptions{Set: map[string]bool{field: true}}); got != "" {
			t.Errorf("prefix with %q supplied = %q", field, got)
		}
	}
}

func TestDecliningNoSkillsConfirmationKeepsTheReselectionEmpty(t *testing.T) {
	answers := justcode.InitAnswers{Skills: []string{"official/securite-developpement"}}
	keepEmptyInitSkillsForRetry(&answers)
	if !answers.SkillsSet || len(answers.Skills) != 0 {
		t.Fatalf("retry state = SkillsSet:%v Skills:%v; want explicit empty selection", answers.SkillsSet, answers.Skills)
	}
}

func TestRetainedMCPOptions(t *testing.T) {
	options := []huh.Option[string]{
		huh.NewOption("data-gouv — rechercher les données", "data-gouv"),
		huh.NewOption("chrome-devtools — navigateur", "chrome-devtools"),
	}
	// A withdrawn id still referenced by the manifest is appended as a
	// retained option labelled with its withdrawal reason; offered ids are
	// not duplicated and the input slice is not mutated.
	got := retainedMCPOptions(options, []string{"data-gouv", "context7"})
	if len(got) != 3 {
		t.Fatalf("expected 3 options, got %d", len(got))
	}
	if got[2].Value != "context7" {
		t.Fatalf("expected retained option value context7, got %q", got[2].Value)
	}
	if !strings.Contains(got[2].Key, "n'est plus proposé par just-code") {
		t.Fatalf("retained option label must name the withdrawal, got %q", got[2].Key)
	}
	if len(options) != 2 {
		t.Fatalf("input options mutated: %d", len(options))
	}
	// An unknown id (typo in a hand-edited manifest) is not offered for
	// re-selection: only currently or previously offered ids come back.
	if got = retainedMCPOptions(options, []string{"mcp/memory"}); len(got) != 2 {
		t.Fatalf("unknown id must not gain an option, got %d", len(got))
	}
}

func TestEqualStringSets(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want bool
	}{
		{"identical", []string{"a", "b"}, []string{"a", "b"}, true},
		{"reordered", []string{"b", "a"}, []string{"a", "b"}, true},
		{"different members", []string{"a", "c"}, []string{"a", "b"}, false},
		{"different lengths", []string{"a"}, []string{"a", "b"}, false},
		{"both empty", nil, nil, true},
		{"duplicates differ", []string{"a", "a"}, []string{"a", "b"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := equalStringSets(tc.a, tc.b); got != tc.want {
				t.Fatalf("equalStringSets(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
	// Inputs must not be mutated by the comparison.
	a := []string{"b", "a"}
	equalStringSets(a, []string{"a", "b"})
	if a[0] != "b" || a[1] != "a" {
		t.Fatalf("input mutated: %v", a)
	}
}

func TestEqualStringSlices(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want bool
	}{
		{"both nil", nil, nil, true},
		{"empty vs nil", []string{}, nil, true},
		{"same order", []string{"a", "b"}, []string{"a", "b"}, true},
		{"different order", []string{"a", "b"}, []string{"b", "a"}, false},
		{"different length", []string{"a"}, []string{"a", "b"}, false},
		{"different values", []string{"a", "b"}, []string{"a", "c"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := equalStringSlices(tc.a, tc.b); got != tc.want {
				t.Fatalf("equalStringSlices(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// A reseed after an interactive root change must keep flag-provided values
// explicit: seedInitAnswersFromManifest only overwrites fields the flags did
// not supply, so --skill/--mcp selections survive pointing the wizard at a
// different project.
func TestSeedInitAnswersKeepsFlagValues(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, justcode.ProjectManifest{Skills: []string{"manifest-skill"}, MCPConnectors: []string{"browser"}})
	opts := initOptions{Set: map[string]bool{
		"skills":         true,
		"mcps":           true,
		"skills-storage": true,
	}}
	answers := justcode.InitAnswers{
		Root:            root,
		Skills:          []string{"flagged-skill"},
		SkillsSet:       true,
		MCPConnectors:   []string{"github"},
		MCPsSet:         true,
		SkillsLocalOnly: true,
	}
	seeded := seedInitAnswersFromManifest(opts, answers)
	if !seeded.SkillsSet {
		t.Fatal("flag-supplied skills must stay explicit across a reseed")
	}
	if !equalStringSlices(seeded.Skills, []string{"flagged-skill"}) {
		t.Fatalf("flag-supplied skills overwritten by reseed: %v", seeded.Skills)
	}
	if !seeded.MCPsSet {
		t.Fatal("flag-supplied MCPs must stay explicit across a reseed")
	}
	if !equalStringSlices(seeded.MCPConnectors, []string{"github"}) {
		t.Fatalf("flag-supplied MCPs overwritten by reseed: %v", seeded.MCPConnectors)
	}
	if !seeded.SkillsLocalOnly {
		t.Fatal("flag-supplied storage mode must stay explicit across a reseed")
	}
}

// Without a storage flag, the reseed must load the target manifest's storage
// mode so the form's default matches the project instead of silently
// migrating host-local skills into versioned storage.
func TestSeedInitAnswersLoadsStorageMode(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, justcode.ProjectManifest{SkillsLocalOnly: true})
	opts := initOptions{Set: map[string]bool{}}
	answers := justcode.InitAnswers{Root: root}
	seeded := seedInitAnswersFromManifest(opts, answers)
	if !seeded.SkillsLocalOnly {
		t.Fatal("storage mode not seeded from target manifest")
	}
}

func writeManifest(t *testing.T, root string, manifest justcode.ProjectManifest) {
	t.Helper()
	manifest.SchemaVersion = 4
	dir := filepath.Join(root, ".just-code")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "project.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// initAbandonRun drives askInitQuestionsHuh to an interruption and returns
// the abandon stages actually queued (#310), exercising the interactive
// paths rather than recordInitAbandon directly. abortOn maps 1-based huhRun
// call indices to an abort; confirmErr, when set, fails the zero-skills
// confirmation. Consent is granted or refused through durable settings, so
// the #181 guard is exercised for real.
func initAbandonRun(t *testing.T, skills, mcps []string, abortOn map[int]bool, confirmErr error, consent bool) ([]string, error) {
	t.Helper()
	stateDir, _ := setupTestEnv(t)
	// Queue events without starting a detached copy of the Go test binary.
	previousSpawn := justcode.StatsFlushSpawn
	justcode.StatsFlushSpawn = func() {}
	t.Cleanup(func() { justcode.StatsFlushSpawn = previousSpawn })
	settingsPath, err := justcode.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := justcode.WriteUserSettings(justcode.DefaultFS, settingsPath, justcode.UserSettings{
		SchemaVersion: 1, StatsEnabled: consent, StatsConsentAsked: true,
	}); err != nil {
		t.Fatal(err)
	}
	root := initTestProject(t)
	originalCatalogue, originalHuhRun, originalConfirm := projectSkillCatalogueFn, huhRun, initEmptySkillsConfirmFn
	projectSkillCatalogueFn = func(context.Context) ([]justcode.ProjectSkill, error) {
		return initSkillFixtureCatalogue(), nil
	}
	calls := 0
	huhRun = func(*huh.Form) error {
		calls++
		if abortOn[calls] {
			return huh.ErrUserAborted
		}
		return nil
	}
	initEmptySkillsConfirmFn = func() (bool, error) {
		if confirmErr != nil {
			return false, confirmErr
		}
		return true, nil
	}
	t.Cleanup(func() {
		projectSkillCatalogueFn, huhRun, initEmptySkillsConfirmFn = originalCatalogue, originalHuhRun, originalConfirm
	})
	opts := initOptions{Set: map[string]bool{"root": true, "github": true, "skills-storage": true},
		hostCPUs: 8, hostMemoryMB: 16384, maxCPUs: 8, maxMemoryMB: 8192}
	_, err = askInitQuestionsHuh(opts, justcode.InitAnswers{
		Root: root, SkillsSet: true, Skills: skills,
		MCPsSet: true, MCPConnectors: mcps,
	})
	events, readErr := justcode.PendingStatsEvents(justcode.DefaultFS, stateDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var stages []string
	for _, ev := range events {
		if ev.Kind == "abandon" && ev.Payload.Abandon != nil {
			stages = append(stages, ev.Payload.Abandon.Stage)
		}
	}
	return stages, err
}

// TestInitHuhAbandonStages covers real form interruptions (#310): each abort
// is attributed to the step the user was actually on, never to another one,
// and a completed run records nothing.
func TestInitHuhAbandonStages(t *testing.T) {
	nonEmpty := []string{"official/rgaa"}
	t.Run("abort on the skills form", func(t *testing.T) {
		stages, err := initAbandonRun(t, nonEmpty, []string{"github"}, map[int]bool{1: true}, nil, true)
		if err == nil {
			t.Fatal("an aborted form must return an error")
		}
		if !equalStringSlices(stages, []string{"skills"}) {
			t.Fatalf("stages = %v, want skills only (no invented MCP abandon)", stages)
		}
	})
	t.Run("abort on the MCP form", func(t *testing.T) {
		stages, err := initAbandonRun(t, nonEmpty, []string{"github"}, map[int]bool{2: true}, nil, true)
		if err == nil {
			t.Fatal("an aborted form must return an error")
		}
		if !equalStringSlices(stages, []string{"mcp"}) {
			t.Fatalf("stages = %v, want mcp only", stages)
		}
	})
	t.Run("abort on the zero-skills confirmation", func(t *testing.T) {
		stages, err := initAbandonRun(t, nil, []string{"github"}, nil, huh.ErrUserAborted, true)
		if err == nil {
			t.Fatal("an aborted confirmation must return an error")
		}
		if !equalStringSlices(stages, []string{"skills"}) {
			t.Fatalf("stages = %v, want skills", stages)
		}
	})
	t.Run("abort on the zero-MCP confirmation", func(t *testing.T) {
		stages, err := initAbandonRun(t, nonEmpty, nil, map[int]bool{3: true}, nil, true)
		if err == nil {
			t.Fatal("an aborted confirmation must return an error")
		}
		if !equalStringSlices(stages, []string{"mcp"}) {
			t.Fatalf("stages = %v, want mcp", stages)
		}
	})
	t.Run("no consent means no event", func(t *testing.T) {
		stages, err := initAbandonRun(t, nonEmpty, []string{"github"}, map[int]bool{2: true}, nil, false)
		if err == nil {
			t.Fatal("an aborted form must return an error")
		}
		if len(stages) != 0 {
			t.Fatalf("the #181 consent guard must suppress every event: %v", stages)
		}
	})
	t.Run("a completed run records no abandon", func(t *testing.T) {
		stages, err := initAbandonRun(t, nonEmpty, []string{"github"}, nil, nil, true)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(stages) != 0 {
			t.Fatalf("a successful run must not record an abandon: %v", stages)
		}
	})
}
