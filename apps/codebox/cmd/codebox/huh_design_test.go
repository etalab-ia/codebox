package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestSharedThemeRendersExplicitCheckboxes pins #445: multi-selects must
// read as checkboxes ([ ]/[x]), not the stock •/✓ bullets that testers
// mistook for a plain list. The assertion runs on the rendered form view —
// the pixels the user sees — through the same huhRun theme wiring
// production uses.
func TestSharedThemeRendersExplicitCheckboxes(t *testing.T) {
	selected := []string{}
	field := newProjectMCPsMultiSelect(
		[]huh.Option[string]{huh.NewOption("data.gouv.fr", "data-gouv"), huh.NewOption("chrome-devtools", "chrome-devtools")},
		&selected, "MCP (facultatif)")
	form := huh.NewForm(huh.NewGroup(field)).WithTheme(codeboxTheme{}).WithKeyMap(frenchKeyMap())
	form.Init()
	_, _ = form.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	view := ansi.Strip(form.View())
	if !strings.Contains(view, "[ ] data.gouv.fr") {
		t.Fatalf("unselected option must render as an empty checkbox:\n%s", view)
	}
	// Option lines only: the help bar legitimately uses • as a separator.
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "data.gouv.fr") || strings.Contains(line, "chrome-devtools") {
			if strings.Contains(line, "•") || strings.Contains(line, "✓") {
				t.Fatalf("stock bullet markers must be gone from option lines:\n%s", line)
			}
		}
	}
	_, _ = form.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	view = ansi.Strip(form.View())
	if !strings.Contains(view, "[x] data.gouv.fr") {
		t.Fatalf("toggled option must render as a checked checkbox:\n%s", view)
	}
	if !strings.Contains(view, "[ ] chrome-devtools") {
		t.Fatalf("untoggled option must stay an empty checkbox:\n%s", view)
	}
}

// TestSharedKeyMapHelpIsFrench pins #445: the help bar must speak French on
// every field type the wizard uses, and single-selects must not offer the
// filter shortcut (short lists). It asserts on the rendered footer, not on
// the keymap object, so a field that ignores the keymap would fail here.
func TestSharedKeyMapHelpIsFrench(t *testing.T) {
	cases := []struct {
		name    string
		field   func() huh.Field
		want    []string
		notWant []string
	}{
		{
			name: "select",
			field: func() huh.Field {
				value := "a"
				return huh.NewSelect[string]().Title("Agent").
					Options(huh.NewOption("OpenCode", "a"), huh.NewOption("Pi", "b")).Value(&value)
			},
			want:    []string{"haut", "bas", "continuer"},
			notWant: []string{"filtrer", "filter"},
		},
		{
			name: "multiselect",
			field: func() huh.Field {
				value := []string{}
				return huh.NewMultiSelect[string]().Title("MCP").
					Options(huh.NewOption("data.gouv.fr", "data-gouv")).Value(&value)
			},
			want:    []string{"cocher", "filtrer", "continuer"},
			notWant: []string{"toggle", "select all"},
		},
		{
			name: "input",
			field: func() huh.Field {
				value := ""
				return huh.NewInput().Title("Clé API").Value(&value)
			},
			want:    []string{"continuer"},
			notWant: []string{"submit", "next"},
		},
		{
			name: "confirm",
			field: func() huh.Field {
				value := true
				return huh.NewConfirm().Title("Continuer ?").Affirmative("Oui").Negative("Non").Value(&value)
			},
			want:    []string{"choisir", "y Oui", "n Non"},
			notWant: []string{"toggle", "y Yes"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := huh.NewForm(huh.NewGroup(tc.field())).WithTheme(codeboxTheme{}).WithKeyMap(frenchKeyMap())
			form.Init()
			_, _ = form.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
			view := ansi.Strip(form.View())
			for _, want := range tc.want {
				if !strings.Contains(view, want) {
					t.Errorf("help bar must contain %q:\n%s", want, view)
				}
			}
			// The help bar is the last non-empty line of the view; scope the
			// negative assertions to it so option labels ("Non") cannot
			// false-positive against help wording.
			lines := strings.Split(strings.TrimSpace(view), "\n")
			helpBar := lines[len(lines)-1]
			for _, notWant := range tc.notWant {
				if strings.Contains(helpBar, notWant) {
					t.Errorf("help bar must not contain %q:\n%s", notWant, helpBar)
				}
			}
		})
	}
}

// TestHuhRunAppliesSharedDesign pins the wiring itself: the production
// huhRun must attach the theme and key map to any form it is given, even
// one built by a caller that knows nothing about the design. It runs the
// real huhRun in accessible mode (no TTY needed) and asserts the design is
// observable in the output: the checked option carries the theme's green
// and the shared checkbox styling path, not the stock theme.
func TestHuhRunAppliesSharedDesign(t *testing.T) {
	var out strings.Builder
	value := []string{"data-gouv"}
	form := huh.NewForm(huh.NewGroup(huh.NewMultiSelect[string]().
		Title("MCP").Options(huh.NewOption("data.gouv.fr", "data-gouv")).Value(&value))).
		WithAccessible(true).WithInput(strings.NewReader("0\n")).WithOutput(&out)
	if err := huhRun(form); err != nil {
		t.Fatalf("huhRun: %v", err)
	}
	view := out.String()
	if !strings.Contains(view, "data.gouv.fr") {
		t.Fatalf("accessible run lost the option:\n%s", view)
	}
	// The shared theme colours the selected option DSFR green (#00A95F);
	// the stock Charm theme uses #02BA84/#02BF87. The ANSI sequence is the
	// observable proof that huhRun attached codeboxTheme, not the default.
	if !strings.Contains(view, "38;2;0;169;95") {
		t.Fatalf("huhRun must apply the shared theme (DSFR green on the selected option):\n%q", view)
	}
}
