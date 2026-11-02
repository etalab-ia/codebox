package main

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/etalab-ia/just-code/internal/justcode"
)

type multiSelectRenderCase struct {
	name    string
	newForm func(*testing.T) *huh.Form
	options []string
}

func multiSelectRenderCases() []multiSelectRenderCase {
	return []multiSelectRenderCase{
		{name: "setup platforms", newForm: captureSetupPlatformsForm, options: []string{"Albert API", "Mistral AI", "Compatible OpenAI"}},
		{name: "init MCPs", newForm: captureInitMCPsForm, options: []string{"data.gouv.fr", "chrome-devtools"}},
		{name: "workspace apply detail", newForm: captureWorkspaceApplyDetailForm, options: []string{"README.md", "cmd/main.go"}},
	}
}

func TestMultiSelectFormsKeepOptionsVisibleOnShortTerminals(t *testing.T) {
	for _, tc := range multiSelectRenderCases() {
		t.Run(tc.name, func(t *testing.T) {
			for _, height := range []int{8, 12, 24, 40} {
				form := tc.newForm(t)
				form.Init()
				_, _ = form.Update(tea.WindowSizeMsg{Width: 100, Height: height})
				view := ansi.Strip(form.View())
				for _, option := range tc.options {
					if !strings.Contains(view, option) {
						t.Errorf("terminal height %d hides option %q:\n%s", height, option, view)
					}
				}
				if !multiSelectCursorOnOption(view, tc.options[0]) {
					t.Errorf("terminal height %d hides the option cursor:\n%s", height, view)
				}
			}
		})
	}
}

func TestMultiSelectCursorSelectionScrollingAndResize(t *testing.T) {
	options := make([]huh.Option[string], 12)
	for i := range options {
		label := fmt.Sprintf("mcp-%02d", i)
		options[i] = huh.NewOption(label, label)
	}
	selected := []string{}
	form := huh.NewForm(huh.NewGroup(newProjectMCPsMultiSelect(options, &selected)))
	form.Init()
	_, _ = form.Update(tea.WindowSizeMsg{Width: 48, Height: 8})
	for range len(options) - 1 {
		_, _ = form.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	}
	view := ansi.Strip(form.View())
	if !multiSelectCursorOnOption(view, "mcp-11") {
		t.Fatalf("scrolling did not keep the last option and cursor visible:\n%s", view)
	}
	_, _ = form.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	view = ansi.Strip(form.View())
	if !multiSelectSelectionOnOption(view, "mcp-11") {
		t.Fatalf("space did not toggle the visible option:\n%s", view)
	}

	_, _ = form.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	view = ansi.Strip(form.View())
	if !multiSelectCursorOnOption(view, "mcp-11") || !multiSelectSelectionOnOption(view, "mcp-11") {
		t.Fatalf("growing the terminal lost the cursor or selection:\n%s", view)
	}
	_, _ = form.Update(tea.WindowSizeMsg{Width: 48, Height: 8})
	view = ansi.Strip(form.View())
	if !multiSelectCursorOnOption(view, "mcp-11") || !multiSelectSelectionOnOption(view, "mcp-11") {
		t.Fatalf("shrinking the terminal lost the cursor or selection:\n%s", view)
	}
	_, _ = form.Update(tea.KeyPressMsg(tea.Key{Code: 'g', Text: "g"}))
	view = ansi.Strip(form.View())
	if !multiSelectCursorOnOption(view, "mcp-00") {
		t.Fatalf("keyboard navigation back to the top failed:\n%s", view)
	}
	_, _ = form.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
	view = ansi.Strip(form.View())
	if !multiSelectCursorOnOption(view, "mcp-11") {
		t.Fatalf("keyboard navigation to the bottom failed:\n%s", view)
	}
	_, _ = form.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	view = ansi.Strip(form.View())
	if !multiSelectCursorOnOption(view, "mcp-11") {
		t.Fatalf("resize after End moved the cursor:\n%s", view)
	}
}

func TestMultiSelectResizePreservesActiveFilter(t *testing.T) {
	options := []huh.Option[string]{huh.NewOption("data.gouv.fr", "data-gouv"), huh.NewOption("chrome-devtools", "chrome-devtools")}
	selected := []string{}
	form := huh.NewForm(huh.NewGroup(newProjectMCPsMultiSelect(options, &selected)))
	form.Init()
	_, _ = form.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	for _, key := range "/data" {
		_, _ = form.Update(tea.KeyPressMsg(tea.Key{Code: key, Text: string(key), ShiftedCode: key}))
	}
	_, _ = form.Update(tea.WindowSizeMsg{Width: 80, Height: 23})
	view := ansi.Strip(form.View())
	if !strings.Contains(view, "/data") || strings.Contains(view, "/dataG") {
		t.Fatalf("resize changed the active filter:\n%s", view)
	}
	if !multiSelectCursorOnOption(view, "data.gouv.fr") {
		t.Fatalf("resize lost the filtered cursor:\n%s", view)
	}
}

func TestMultiSelectResizePreservesSecondDuplicateValue(t *testing.T) {
	options := []huh.Option[string]{huh.NewOption("first", "same"), huh.NewOption("second", "same"), huh.NewOption("third", "other")}
	selected := []string{}
	form := huh.NewForm(huh.NewGroup(newProjectMCPsMultiSelect(options, &selected)))
	form.Init()
	_, _ = form.Update(tea.WindowSizeMsg{Width: 80, Height: 6})
	_, _ = form.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	_, _ = form.Update(tea.WindowSizeMsg{Width: 80, Height: 7})
	view := ansi.Strip(form.View())
	if !multiSelectCursorOnOption(view, "second") {
		t.Fatalf("resize moved the cursor to the first duplicate value:\n%s", view)
	}
}

func TestMultiSelectFormsAccessibleModeKeepsOptions(t *testing.T) {
	for _, tc := range multiSelectRenderCases() {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			form := tc.newForm(t).WithAccessible(true).WithInput(strings.NewReader("0\n")).WithOutput(&output)
			if err := form.Run(); err != nil {
				t.Fatalf("accessible form: %v", err)
			}
			for _, option := range tc.options {
				if !strings.Contains(output.String(), option) {
					t.Errorf("accessible output hides %q:\n%s", option, output.String())
				}
			}
		})
	}
}

func multiSelectCursorOnOption(view, option string) bool {
	return multiSelectOptionLineContains(view, option, ">")
}

func multiSelectSelectionOnOption(view, option string) bool {
	return multiSelectOptionLineContains(view, option, "✓")
}

func multiSelectOptionLineContains(view, option, marker string) bool {
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, option) && strings.Contains(line, marker) {
			return true
		}
	}
	return false
}

func captureSetupPlatformsForm(t *testing.T) *huh.Form {
	t.Helper()
	originalWizard, originalRun := wizardHuhFn, huhRun
	wizardHuhFn = func() bool { return true }
	var captured *huh.Form
	huhRun = func(form *huh.Form) error {
		captured = form
		return nil
	}
	defer func() { wizardHuhFn, huhRun = originalWizard, originalRun }()
	if _, err := chooseSetupPlatforms(nil, bufio.NewReader(strings.NewReader(""))); err != nil {
		t.Fatalf("capture setup form: %v", err)
	}
	return captured
}

func captureInitMCPsForm(_ *testing.T) *huh.Form {
	selected := []string{}
	return huh.NewForm(huh.NewGroup(newProjectMCPsMultiSelect(
		[]huh.Option[string]{huh.NewOption("data.gouv.fr", "data-gouv"), huh.NewOption("chrome-devtools", "chrome-devtools")},
		&selected,
	)))
}

func captureWorkspaceApplyDetailForm(t *testing.T) *huh.Form {
	t.Helper()
	originalRun := huhRun
	var captured *huh.Form
	huhRun = func(form *huh.Form) error {
		captured = form
		return nil
	}
	defer func() { huhRun = originalRun }()
	changes := []justcode.GuestChange{{Rel: "README.md"}, {Rel: "cmd/main.go"}}
	if _, err := workspaceApplyDetailFn(changes); err != nil {
		t.Fatalf("capture workspace apply form: %v", err)
	}
	return captured
}
