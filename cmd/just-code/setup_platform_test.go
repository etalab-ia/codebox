package main

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/etalab-ia/just-code/internal/justcode"
)

// TestSetupPlatformStageUnreachableRetryThenContinue pins the retry answer for a
// non-Albert platform: C stores unvalidated, R re-asks.
func TestSetupPlatformStageUnreachableRetryThenContinue(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	fs := justcode.DefaultFS
	j := justcode.SetupJournal{SchemaVersion: 1, Stage: justcode.StageCredential, Platforms: []justcode.SetupPlatform{{Kind: "mistral"}}}
	store := &fakeSetupStore{}
	calls := 0
	w := justcode.SetupWizard{
		FS: fs, StateDir: stateDir,
		Store: func(context.Context) (justcode.SetupStore, error) { return store, nil },
		ValidateProvider: func(context.Context, justcode.InferenceProvider, string) justcode.CredentialProbe {
			calls++
			if calls == 1 {
				return justcode.CredentialProbe{Unreachable: true, Detail: "dial tcp: timeout"}
			}
			return justcode.CredentialProbe{}
		},
	}
	if _, err := setupPlatformStage(context.Background(), w, bufio.NewReader(strings.NewReader("k1\nr\nk2\n")), true, &j, false, fs, stateDir); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if store.puts[justcode.CredentialMistral] != "k2" || calls != 2 {
		t.Fatalf("retry path: puts=%+v calls=%d", store.puts, calls)
	}
}

func TestSetupPlatformStageAbortsOnUnansweredRetry(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	fs := justcode.DefaultFS
	j := justcode.SetupJournal{SchemaVersion: 1, Stage: justcode.StageCredential, Platforms: []justcode.SetupPlatform{{Kind: "mistral"}}}
	store := &fakeSetupStore{}
	w := justcode.SetupWizard{
		FS: fs, StateDir: stateDir,
		Store: func(context.Context) (justcode.SetupStore, error) { return store, nil },
		ValidateProvider: func(context.Context, justcode.InferenceProvider, string) justcode.CredentialProbe {
			return justcode.CredentialProbe{Unreachable: true, Detail: "down"}
		},
	}
	_, err := setupPlatformStage(context.Background(), w, bufio.NewReader(strings.NewReader("k\n")), true, &j, false, fs, stateDir)
	if err == nil {
		t.Fatal("EOF at the retry prompt must cancel, not store an unvalidated key")
	}
	if len(store.puts) != 0 {
		t.Fatalf("nothing may be stored: %+v", store.puts)
	}
	var rejected justcode.RejectedCredentialError
	if errors.As(err, &rejected) {
		t.Fatal("unreachable is not a rejection")
	}
}

func TestChooseSetupPlatformsTextMultiSelectAndEOF(t *testing.T) {
	forceNonTTY(t)
	chosen, err := chooseSetupPlatforms(nil, bufio.NewReader(strings.NewReader("2,3\n")))
	if err != nil {
		t.Fatalf("choose platforms: %v", err)
	}
	if len(chosen) != 2 || chosen[0].Kind != string(justcode.ProviderMistral) || chosen[1].Kind != string(justcode.ProviderOpenAICompatible) {
		t.Fatalf("selection = %+v", chosen)
	}
	if _, err := chooseSetupPlatforms(nil, bufio.NewReader(strings.NewReader(""))); err == nil {
		t.Fatal("EOF must cancel instead of accepting the fresh Albert default")
	}
}

func TestChooseSetupPlatformsTextCanRemoveAlbert(t *testing.T) {
	forceNonTTY(t)
	chosen, err := chooseSetupPlatforms([]justcode.SetupPlatform{{Kind: string(justcode.ProviderAlbert)}, {Kind: string(justcode.ProviderMistral)}}, bufio.NewReader(strings.NewReader("2\n")))
	if err != nil {
		t.Fatalf("choose platforms: %v", err)
	}
	if len(chosen) != 1 || chosen[0].Kind != string(justcode.ProviderMistral) {
		t.Fatalf("Albert was not removed: %+v", chosen)
	}
	if _, err := chooseSetupPlatforms([]justcode.SetupPlatform{{Kind: string(justcode.ProviderAlbert)}}, bufio.NewReader(strings.NewReader("\n"))); err != nil {
		t.Fatalf("an explicit empty-line confirmation should retain current choices: %v", err)
	}
}

func TestChooseSetupPlatformsHuhRunsMultiSelect(t *testing.T) {
	originalHuh, originalRun := wizardHuhFn, huhRun
	wizardHuhFn = func() bool { return true }
	calls := 0
	huhRun = func(form *huh.Form) error {
		calls++
		if form == nil {
			t.Fatal("provider selection form was not built")
		}
		return nil
	}
	t.Cleanup(func() { wizardHuhFn, huhRun = originalHuh, originalRun })
	chosen, err := chooseSetupPlatforms(nil, bufio.NewReader(strings.NewReader("")))
	if err != nil || calls != 1 {
		t.Fatalf("Huh selector: calls=%d err=%v", calls, err)
	}
	if len(chosen) != 1 || chosen[0].Kind != string(justcode.ProviderAlbert) {
		t.Fatalf("Huh default selection = %+v", chosen)
	}
}

// forceLineWizard pins the interactive line renderer (a terminal with huh
// disabled, e.g. --no-color) for a resume test while leaving the REAL platform
// chooser and resume confirmation in place, so the scripted input exercises
// the #305 prompts instead of a stub.
func forceLineWizard(t *testing.T) {
	t.Helper()
	origTTY, origHuh := stdinIsTTYFn, wizardHuhFn
	stdinIsTTYFn = func() bool { return true }
	wizardHuhFn = func() bool { return false }
	t.Cleanup(func() { stdinIsTTYFn, wizardHuhFn = origTTY, origHuh })
}

// forcePipedWizard pins a piped run: no terminal, the line renderer, and the
// real resume confirmation (which must then stay out of the input's way).
func forcePipedWizard(t *testing.T) {
	t.Helper()
	origTTY, origHuh := stdinIsTTYFn, wizardHuhFn
	stdinIsTTYFn = func() bool { return false }
	wizardHuhFn = func() bool { return false }
	t.Cleanup(func() { stdinIsTTYFn, wizardHuhFn = origTTY, origHuh })
}

func resumeTestJournal(platforms []justcode.SetupPlatform, kinds ...string) justcode.SetupJournal {
	return justcode.SetupJournal{
		SchemaVersion: 1, Stage: justcode.StageCredential,
		Platforms: platforms, CredentialKinds: kinds,
	}
}

func resumeTestWizard(stateDir string, store *fakeSetupStore) justcode.SetupWizard {
	return justcode.SetupWizard{
		FS: justcode.DefaultFS, StateDir: stateDir,
		Store: func(context.Context) (justcode.SetupStore, error) { return store, nil },
		ValidateProvider: func(context.Context, justcode.InferenceProvider, string) justcode.CredentialProbe {
			return justcode.CredentialProbe{}
		},
	}
}

// TestConfirmResumePlatformsTextAnswers covers the line renderer: Enter or
// "c" keeps the journaled selection, "m" asks for the chooser, an unknown
// answer re-asks, and EOF keeps the selection so piped runs keep their
// pre-#305 behavior.
func TestConfirmResumePlatformsTextAnswers(t *testing.T) {
	forceLineWizard(t)
	platforms := []justcode.SetupPlatform{{Kind: "albert"}, {Kind: "mistral"}}
	for _, tc := range []struct {
		name, input string
		want        bool
	}{
		{"empty keeps", "\n", true},
		{"c keeps", "c\n", true},
		{"continuer keeps", "continuer\n", true},
		{"m modifies", "m\n", false},
		{"modifier modifies", "modifier\n", false},
		{"unknown then modify", "x\nm\n", false},
		{"EOF keeps", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStdout(t, func() {
				keep, err := confirmResumePlatforms(platforms, bufio.NewReader(strings.NewReader(tc.input)), t.TempDir())
				if err != nil {
					t.Fatalf("confirm: %v", err)
				}
				if keep != tc.want {
					t.Fatalf("keep = %v, want %v", keep, tc.want)
				}
			})
			if !strings.Contains(out, "Plateformes sélectionnées avant l'interruption : Albert API, Mistral AI.") {
				t.Errorf("selection line missing; output:\n%s", out)
			}
			if tc.name == "unknown then modify" && !strings.Contains(out, "Réponse non reconnue.") {
				t.Errorf("unrecognized answer must re-ask; output:\n%s", out)
			}
		})
	}
}

// TestConfirmResumePlatformsPipedKeepsWithoutPrompt: a piped run cannot
// answer the resume question (its next line belongs to a key prompt), so the
// journaled selection is kept silently, exactly as before #305.
func TestConfirmResumePlatformsPipedKeepsWithoutPrompt(t *testing.T) {
	forcePipedWizard(t)
	out := captureStdout(t, func() {
		keep, err := confirmResumePlatforms([]justcode.SetupPlatform{{Kind: "mistral"}}, bufio.NewReader(strings.NewReader("mistral-key\n")), t.TempDir())
		if err != nil {
			t.Fatalf("confirm: %v", err)
		}
		if !keep {
			t.Fatal("a piped run must keep the journaled platforms")
		}
	})
	if out != "" {
		t.Fatalf("a piped run must not print the resume prompt: %q", out)
	}
}

// TestConfirmResumePlatformsHuhDefaultsToKeep covers the interactive renderer:
// validating the select without changing it keeps the selection.
func TestConfirmResumePlatformsHuhDefaultsToKeep(t *testing.T) {
	originalHuh, originalRun := wizardHuhFn, huhRun
	origTTY := stdinIsTTYFn
	wizardHuhFn = func() bool { return true }
	stdinIsTTYFn = func() bool { return true }
	calls := 0
	huhRun = func(form *huh.Form) error {
		calls++
		if form == nil {
			t.Fatal("resume form was not built")
		}
		return nil
	}
	t.Cleanup(func() { wizardHuhFn, huhRun, stdinIsTTYFn = originalHuh, originalRun, origTTY })
	keep, err := confirmResumePlatforms([]justcode.SetupPlatform{{Kind: "mistral"}}, bufio.NewReader(strings.NewReader("")), t.TempDir())
	if err != nil || calls != 1 {
		t.Fatalf("huh resume: calls=%d err=%v", calls, err)
	}
	if !keep {
		t.Fatal("the default huh answer must keep the journaled platforms")
	}
}

// TestConfirmResumePlatformsHuhAbortCancelsInFrench pins the explicit Ctrl+C
// behavior: unlike EOF, an abort cancels setup with a French message.
func TestConfirmResumePlatformsHuhAbortCancelsInFrench(t *testing.T) {
	originalHuh, originalRun := wizardHuhFn, huhRun
	origTTY := stdinIsTTYFn
	wizardHuhFn = func() bool { return true }
	stdinIsTTYFn = func() bool { return true }
	huhRun = func(*huh.Form) error { return huh.ErrUserAborted }
	t.Cleanup(func() { wizardHuhFn, huhRun, stdinIsTTYFn = originalHuh, originalRun, origTTY })
	_, err := confirmResumePlatforms([]justcode.SetupPlatform{{Kind: "mistral"}}, bufio.NewReader(strings.NewReader("")), t.TempDir())
	if !errors.Is(err, errSetupCancelled) {
		t.Fatalf("abort must cancel setup, got %v", err)
	}
	if !strings.Contains(err.Error(), "setup annulé") || !strings.Contains(err.Error(), "just-code setup") {
		t.Fatalf("cancellation must be French and point at the resume command: %v", err)
	}
}

// TestSetupPlatformStageResumePipedAsksMissingKeys: a piped resume after an
// interruption on the FIRST provider (Albert) skips the resume question and
// reads the key line exactly as before #305.
func TestSetupPlatformStageResumePipedAsksMissingKeys(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forcePipedWizard(t)
	j := resumeTestJournal([]justcode.SetupPlatform{{Kind: "albert"}})
	store := &fakeSetupStore{}
	w := resumeTestWizard(stateDir, store)
	out := captureStdout(t, func() {
		providers, err := setupPlatformStage(context.Background(), w, bufio.NewReader(strings.NewReader("albert-key\n")), true, &j, false, justcode.DefaultFS, stateDir)
		if err != nil {
			t.Fatalf("stage: %v", err)
		}
		if len(providers) != 1 || providers[0].Kind != justcode.ProviderAlbert {
			t.Fatalf("providers = %+v", providers)
		}
	})
	if store.puts[justcode.CredentialAlbert] != "albert-key" || len(store.puts) != 1 {
		t.Fatalf("puts = %+v", store.puts)
	}
	if strings.Contains(out, "[C]ontinuer") {
		t.Errorf("a piped resume must not ask the resume question; output:\n%s", out)
	}
}

// TestSetupPlatformStageResumePipedMistral: same piped compatibility for an
// interruption on the Mistral key.
func TestSetupPlatformStageResumePipedMistral(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forcePipedWizard(t)
	j := resumeTestJournal([]justcode.SetupPlatform{{Kind: "mistral"}})
	store := &fakeSetupStore{}
	w := resumeTestWizard(stateDir, store)
	if _, err := setupPlatformStage(context.Background(), w, bufio.NewReader(strings.NewReader("mistral-key\n")), true, &j, false, justcode.DefaultFS, stateDir); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if store.puts[justcode.CredentialMistral] != "mistral-key" {
		t.Fatalf("puts = %+v", store.puts)
	}
}

// TestSetupPlatformStageResumeModifyDeselectsMistral: interrupted on the
// Mistral key, the user modifies the selection and drops Mistral. The stored
// Albert key is kept and no new key is asked.
func TestSetupPlatformStageResumeModifyDeselectsMistral(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceLineWizard(t)
	j := resumeTestJournal(
		[]justcode.SetupPlatform{{Kind: "albert"}, {Kind: "mistral"}},
		string(justcode.CredentialAlbert),
	)
	store := &fakeSetupStore{}
	w := resumeTestWizard(stateDir, store)
	out := captureStdout(t, func() {
		providers, err := setupPlatformStage(context.Background(), w, bufio.NewReader(strings.NewReader("m\n1\n")), true, &j, false, justcode.DefaultFS, stateDir)
		if err != nil {
			t.Fatalf("stage: %v", err)
		}
		if len(providers) != 1 || providers[0].Kind != justcode.ProviderAlbert {
			t.Fatalf("Mistral was not deselected: %+v", providers)
		}
	})
	if len(store.puts) != 0 {
		t.Fatalf("no new key may be stored: %+v", store.puts)
	}
	if len(j.Platforms) != 1 || j.Platforms[0].Kind != "albert" {
		t.Fatalf("journal platforms = %+v", j.Platforms)
	}
	if !strings.Contains(out, "Clé Albert API (État) déjà enregistrée.") {
		t.Errorf("the stored Albert key must be kept without re-asking; output:\n%s", out)
	}
}

// TestSetupPlatformStageResumeModifyDropsAlbert: the user drops Albert and
// keeps only Mistral, whose key was already stored before the interruption;
// no key is re-asked.
func TestSetupPlatformStageResumeModifyDropsAlbert(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceLineWizard(t)
	j := resumeTestJournal(
		[]justcode.SetupPlatform{{Kind: "albert"}, {Kind: "mistral"}},
		string(justcode.CredentialMistral),
	)
	store := &fakeSetupStore{}
	w := resumeTestWizard(stateDir, store)
	providers, err := setupPlatformStage(context.Background(), w, bufio.NewReader(strings.NewReader("m\n2\n")), true, &j, false, justcode.DefaultFS, stateDir)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if len(providers) != 1 || providers[0].Kind != justcode.ProviderMistral {
		t.Fatalf("providers = %+v", providers)
	}
	if len(store.puts) != 0 {
		t.Fatalf("no new key may be stored: %+v", store.puts)
	}
	if !justcode.ContainsCredentialKind(j.CredentialKinds, string(justcode.CredentialMistral)) {
		t.Fatalf("the stored Mistral key must stay journaled: %+v", j.CredentialKinds)
	}
}

// TestSetupPlatformStageResumeOpenAICompatible covers interruption on the
// OpenAI-compatible key: a piped resume asks that key as before #305, and the
// interactive "modify" answer can drop the platform without editing the
// journal by hand.
func TestSetupPlatformStageResumeOpenAICompatible(t *testing.T) {
	openAI := justcode.SetupPlatform{Kind: "openai-compatible", Name: "Scaleway", BaseURL: "https://api.scaleway.ai/v1"}
	t.Run("piped asks the missing key", func(t *testing.T) {
		stateDir, _ := setupTestEnv(t)
		forcePipedWizard(t)
		j := resumeTestJournal([]justcode.SetupPlatform{openAI})
		store := &fakeSetupStore{}
		w := resumeTestWizard(stateDir, store)
		if _, err := setupPlatformStage(context.Background(), w, bufio.NewReader(strings.NewReader("scaleway-key\n")), true, &j, false, justcode.DefaultFS, stateDir); err != nil {
			t.Fatalf("stage: %v", err)
		}
		if store.puts[justcode.CredentialOpenAICompatible] != "scaleway-key" {
			t.Fatalf("puts = %+v", store.puts)
		}
	})
	t.Run("modify drops the platform", func(t *testing.T) {
		stateDir, _ := setupTestEnv(t)
		forceLineWizard(t)
		j := resumeTestJournal(
			[]justcode.SetupPlatform{{Kind: "albert"}, openAI},
			string(justcode.CredentialAlbert),
		)
		store := &fakeSetupStore{}
		w := resumeTestWizard(stateDir, store)
		providers, err := setupPlatformStage(context.Background(), w, bufio.NewReader(strings.NewReader("m\n1\n")), true, &j, false, justcode.DefaultFS, stateDir)
		if err != nil {
			t.Fatalf("stage: %v", err)
		}
		if len(providers) != 1 || providers[0].Kind != justcode.ProviderAlbert {
			t.Fatalf("providers = %+v", providers)
		}
		if len(store.puts) != 0 {
			t.Fatalf("no new key may be stored: %+v", store.puts)
		}
	})
}

// TestSetupPlatformStageResumeHuhModifyDeselectsProvider drives the real
// interactive chooser with keyboard input: each provider can be removed after
// "Modifier", while the retained provider's stored key is not re-asked (#305).
func TestSetupPlatformStageResumeHuhModifyDeselectsProvider(t *testing.T) {
	openAI := justcode.SetupPlatform{Kind: "openai-compatible", Name: "Scaleway", BaseURL: "https://api.scaleway.ai/v1"}
	for _, tc := range []struct {
		name      string
		platforms []justcode.SetupPlatform
		stored    justcode.CredentialKind
		moves     int
		want      justcode.ProviderKind
	}{
		{name: "drop Albert", platforms: []justcode.SetupPlatform{{Kind: "albert"}, {Kind: "mistral"}}, stored: justcode.CredentialMistral, moves: 0, want: justcode.ProviderMistral},
		{name: "drop Mistral", platforms: []justcode.SetupPlatform{{Kind: "albert"}, {Kind: "mistral"}}, stored: justcode.CredentialAlbert, moves: 1, want: justcode.ProviderAlbert},
		{name: "drop OpenAI-compatible", platforms: []justcode.SetupPlatform{{Kind: "albert"}, openAI}, stored: justcode.CredentialAlbert, moves: 2, want: justcode.ProviderAlbert},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir, _ := setupTestEnv(t)
			originalHuh, originalRun := wizardHuhFn, huhRun
			origResume := setupResumePlatformsFn
			wizardHuhFn = func() bool { return true }
			setupResumePlatformsFn = func(_ []justcode.SetupPlatform, _ *bufio.Reader, _ string) (bool, error) {
				return false, nil // the user picked "Modifier la sélection"
			}
			huhRun = func(form *huh.Form) error {
				form.Init()
				_, _ = form.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
				for range tc.moves {
					_, _ = form.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
				}
				_, _ = form.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
				return nil
			}
			t.Cleanup(func() {
				wizardHuhFn, huhRun, setupResumePlatformsFn = originalHuh, originalRun, origResume
			})
			j := resumeTestJournal(tc.platforms, string(tc.stored))
			store := &fakeSetupStore{}
			w := resumeTestWizard(stateDir, store)
			providers, err := setupPlatformStage(context.Background(), w, bufio.NewReader(strings.NewReader("")), true, &j, false, justcode.DefaultFS, stateDir)
			if err != nil {
				t.Fatalf("stage: %v", err)
			}
			if len(providers) != 1 || providers[0].Kind != tc.want {
				t.Fatalf("providers = %+v, want only %s", providers, tc.want)
			}
			if len(store.puts) != 0 {
				t.Fatalf("the retained stored key must not be re-asked: %+v", store.puts)
			}
		})
	}
}

// TestSetupWizardResumeNeverRestoresWithdrawnStatsConsent pins criterion 4 of
// #305 at the full-wizard level: consent withdrawn with `stats off` after the
// interruption must stay withdrawn even when the journal still carries the
// stale "yes" from the interrupted run.
func TestSetupWizardResumeNeverRestoresWithdrawnStatsConsent(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	origHuh := wizardHuhFn
	wizardHuhFn = func() bool { return false }
	t.Cleanup(func() { wizardHuhFn = origHuh })
	settingsPath, err := justcode.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	// Durable state after `just-code stats off`: consent asked, answer no.
	if err := justcode.WriteUserSettings(justcode.DefaultFS, settingsPath, justcode.UserSettings{
		SchemaVersion: 1, StatsEnabled: false, StatsConsentAsked: true,
	}); err != nil {
		t.Fatal(err)
	}
	j, err := justcode.ReadSetupJournal(justcode.DefaultFS, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	j.Stage = justcode.StageCredential
	j.Platforms = []justcode.SetupPlatform{{Kind: "albert"}}
	j.CredentialKinds = []string{string(justcode.CredentialAlbert)}
	// Stale on purpose: the interrupted run had answered yes, and this
	// journal was never re-synchronized. The withdrawal must still win.
	j.StatsConsentAsked = true
	j.StatsEnabled = true
	j.SkipGitHub = true
	j.GitName, j.GitEmail = "Test User", "test@example.test"
	if err := justcode.WriteSetupJournal(justcode.DefaultFS, stateDir, j); err != nil {
		t.Fatal(err)
	}
	code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader("\na\n")),
		func(context.Context, string) error { return nil },
		func(context.Context) error { return nil },
		func(string) string { return "" })
	if code != 0 || err != nil {
		t.Fatalf("resume run: code=%d err=%v", code, err)
	}
	enabled, asked := justcode.ReadStatsConsent(justcode.DefaultFS, settingsPath)
	if !asked || enabled {
		t.Fatalf("withdrawn consent must stay withdrawn: enabled=%v asked=%v", enabled, asked)
	}
}

// TestProviderKeyStagePresentsIncompatibleURLMessage pins the message the
// user actually sees for a 404 on /models (#309): a French URL-compatibility
// explanation and the URL re-prompt, never "check your connection".
func TestProviderKeyStagePresentsIncompatibleURLMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	stateDir, _ := setupTestEnv(t)
	forcePipedWizard(t)
	provider := justcode.InferenceProvider{
		Kind: justcode.ProviderOpenAICompatible, Name: "Test", BaseURL: server.URL,
		CatalogueURL: server.URL + "/models", CredentialKind: justcode.CredentialOpenAICompatible,
		APIKeyEnv: justcode.OpenAICompatibleAPIKeyEnv, Host: "api.example.com",
	}
	store := &fakeSetupStore{}
	w := justcode.SetupWizard{
		FS: justcode.DefaultFS, StateDir: stateDir,
		Store: func(context.Context) (justcode.SetupStore, error) { return store, nil },
		// ValidateProvider is nil on purpose: the real HTTP probe runs
		// against the local 404 server.
	}
	var err error
	out := captureStdout(t, func() {
		// After the incompatibility message, the wizard offers to correct
		// the URL; EOF there cancels without storing anything.
		_, _, err = providerKeyStage(context.Background(), w, bufio.NewReader(strings.NewReader("typed-key\n")), provider, true, stateDir)
	})
	if !errors.Is(err, errSetupCancelled) {
		t.Fatalf("the URL correction prompt must follow the message; err = %v", err)
	}
	if !strings.Contains(out, "n'est pas un catalogue de modèles compatible OpenAI") || !strings.Contains(out, "Vérifiez l'URL de base") {
		t.Errorf("the French URL-compatibility message is missing; output:\n%s", out)
	}
	if !strings.Contains(out, "Nom de la plateforme") {
		t.Errorf("the URL correction prompt must be offered; output:\n%s", out)
	}
	if strings.Contains(out, "Vérifiez votre connexion") {
		t.Errorf("a 404 must not read as a connection problem; output:\n%s", out)
	}
	if len(store.puts) != 0 {
		t.Fatalf("nothing may be stored: %+v", store.puts)
// TestSetupWizardResumeDroppingAlbertRepairsModels covers the full text-rendered
// resume: after Albert is removed, the old Albert model defaults cannot be
// persisted with a Mistral-only provider set (#305).
func TestSetupWizardResumeDroppingAlbertRepairsModels(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceLineWizard(t)
	previousPresent := setupCredentialPresentFn
	setupCredentialPresentFn = func(context.Context, justcode.CredentialKind) bool { return false }
	t.Cleanup(func() { setupCredentialPresentFn = previousPresent })
	j := resumeTestJournal(
		[]justcode.SetupPlatform{{Kind: "albert"}, {Kind: "mistral"}},
		string(justcode.CredentialMistral),
	)
	j.SkipGitHub = true
	j.GitName, j.GitEmail = "Test User", "test@example.test"
	if err := justcode.WriteSetupJournal(justcode.DefaultFS, stateDir, j); err != nil {
		t.Fatal(err)
	}
	// Modify the platforms, keep Mistral only, press Enter on the now-empty
	// model state, then try a bare ID and an explicit Albert reference before
	// providing a provider-valid model and applying.
	input := "m\n2\n\nmistral-large-latest\nalbert/Qwen3-Coder-30B-A3B-Instruct\nmistral/mistral-large-latest\na\n"
	out := captureStdout(t, func() {
		code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
			nil, func(context.Context) error { return nil }, func(string) string { return "" })
		if code != 0 || err != nil {
			t.Fatalf("resume run: code=%d err=%v", code, err)
		}
	})
	if got := strings.Count(out, "Aucun modèle valide n'est actuellement sélectionné"); got != 3 {
		t.Fatalf("the empty state and both provider-invalid answers must require another answer (got %d):\n%s", got, out)
	}
	settingsPath, err := justcode.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	settings, err := justcode.ReadUserSettings(justcode.DefaultFS, settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if settings.DefaultModel != "mistral/mistral-large-latest" {
		t.Fatalf("default model = %q", settings.DefaultModel)
	}
	if len(settings.Models) != 1 || settings.Models[0] != "mistral/mistral-large-latest" {
		t.Fatalf("models = %v", settings.Models)
	}
	if len(settings.Providers) != 1 || settings.Providers[0].Kind != justcode.ProviderMistral {
		t.Fatalf("providers = %+v", settings.Providers)
	}
}

func TestSetupStatsConsentHuhAbortCancelsInFrench(t *testing.T) {
	originalHuh, originalRun := wizardHuhFn, huhRun
	wizardHuhFn = func() bool { return true }
	huhRun = func(*huh.Form) error { return huh.ErrUserAborted }
	t.Cleanup(func() { wizardHuhFn, huhRun = originalHuh, originalRun })
	_, _, err := setupStatsConsent(bufio.NewReader(strings.NewReader("")))
	if !errors.Is(err, errSetupCancelled) {
		t.Fatalf("abort error = %v", err)
	}
	if !strings.Contains(err.Error(), "setup annulé") || strings.Contains(err.Error(), "setup cancelled") {
		t.Fatalf("cancellation must be French: %v", err)
	}
}
