package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/etalab-ia/just-code/internal/justcode"
)

// albertProvidersForSetup is the historical single-platform configuration.
func albertProvidersForSetup() []justcode.InferenceProvider {
	providers, _ := justcode.ResolveProviders(nil)
	return providers
}

// fakeSetupStore records the credentials the platform stage stores.
type fakeSetupStore struct {
	puts map[justcode.CredentialKind]string
}

func (f *fakeSetupStore) Kind() string { return "fake" }
func (f *fakeSetupStore) Verify(context.Context) error {
	return nil
}
func (f *fakeSetupStore) Put(_ context.Context, kind justcode.CredentialKind, value string) error {
	if f.puts == nil {
		f.puts = map[justcode.CredentialKind]string{}
	}
	f.puts[kind] = value
	return nil
}

func TestSetupPlatformStageStoresAMistralKeyWithoutAlbert(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	fs := justcode.DefaultFS
	j := justcode.SetupJournal{
		SchemaVersion: 1, Stage: justcode.StageCredential,
		Platforms: []justcode.SetupPlatform{{Kind: "mistral"}},
	}
	if err := justcode.WriteSetupJournal(fs, stateDir, j); err != nil {
		t.Fatal(err)
	}
	store := &fakeSetupStore{}
	var probed []string
	w := justcode.SetupWizard{
		FS: fs, StateDir: stateDir,
		Store: func(context.Context) (justcode.SetupStore, error) { return store, nil },
		ValidateProvider: func(_ context.Context, provider justcode.InferenceProvider, key string) justcode.CredentialProbe {
			probed = append(probed, string(provider.Kind)+":"+key)
			return justcode.CredentialProbe{}
		},
	}
	providers, err := setupPlatformStage(context.Background(), w, bufio.NewReader(strings.NewReader("m-key\n")), true, &j, fs, stateDir)
	if err != nil {
		t.Fatalf("platform stage: %v", err)
	}
	if len(providers) != 1 || providers[0].Kind != justcode.ProviderMistral {
		t.Fatalf("providers = %+v", providers)
	}
	if store.puts[justcode.CredentialMistral] != "m-key" {
		t.Fatalf("the Mistral key was not stored: %+v", store.puts)
	}
	if !reflect.DeepEqual(probed, []string{"mistral:m-key"}) {
		t.Fatalf("probes = %v", probed)
	}
	after, err := justcode.ReadSetupJournal(fs, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !justcode.ContainsCredentialKind(after.CredentialKinds, "mistral") {
		t.Fatalf("journal kinds = %v", after.CredentialKinds)
	}
	entry, ok := after.Platform(justcode.ProviderMistral)
	if !ok || entry.Verified == nil || !*entry.Verified {
		t.Fatalf("platform verification missing: %+v", after.Platforms)
	}
}

func TestSetupPlatformStageReasksARejectedProviderKey(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	fs := justcode.DefaultFS
	j := justcode.SetupJournal{
		SchemaVersion: 1, Stage: justcode.StageCredential,
		Platforms: []justcode.SetupPlatform{{Kind: "mistral"}},
	}
	store := &fakeSetupStore{}
	w := justcode.SetupWizard{
		FS: fs, StateDir: stateDir,
		Store: func(context.Context) (justcode.SetupStore, error) { return store, nil },
		ValidateProvider: func(_ context.Context, _ justcode.InferenceProvider, key string) justcode.CredentialProbe {
			if key == "bad" {
				return justcode.CredentialProbe{Rejected: true, Detail: "HTTP 401"}
			}
			return justcode.CredentialProbe{}
		},
	}
	if _, err := setupPlatformStage(context.Background(), w, bufio.NewReader(strings.NewReader("bad\ngood\n")), true, &j, fs, stateDir); err != nil {
		t.Fatalf("platform stage: %v", err)
	}
	if _, stored := store.puts[justcode.CredentialMistral]; !stored || store.puts[justcode.CredentialMistral] != "good" {
		t.Fatalf("a rejected key must not be stored and the retry must: %+v", store.puts)
	}
}

func TestSetupPlatformStagePersistsACorrectedEndpoint(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	fs := justcode.DefaultFS
	j := justcode.SetupJournal{
		SchemaVersion: 1, Stage: justcode.StageCredential,
		Platforms: []justcode.SetupPlatform{{
			Kind: "openai-compatible", Name: "Old", BaseURL: "https://old.example.com/v1",
		}},
	}
	if err := justcode.WriteSetupJournal(fs, stateDir, j); err != nil {
		t.Fatal(err)
	}
	store := &fakeSetupStore{}
	w := justcode.SetupWizard{
		FS: fs, StateDir: stateDir,
		Store: func(context.Context) (justcode.SetupStore, error) { return store, nil },
		ValidateProvider: func(_ context.Context, provider justcode.InferenceProvider, key string) justcode.CredentialProbe {
			if provider.BaseURL == "https://old.example.com/v1" {
				return justcode.CredentialProbe{Incompatible: true, Detail: "not a models listing"}
			}
			return justcode.CredentialProbe{}
		},
	}
	input := "k1\nBetter\nhttps://new.example.com/v1\nk2\n"
	providers, err := setupPlatformStage(context.Background(), w, bufio.NewReader(strings.NewReader(input)), true, &j, fs, stateDir)
	if err != nil {
		t.Fatalf("platform stage: %v", err)
	}
	if providers[0].BaseURL != "https://new.example.com/v1" || providers[0].Name != "Better" {
		t.Fatalf("corrected provider not persisted: %+v", providers[0])
	}
	if store.puts[justcode.CredentialOpenAICompatible] != "k2" {
		t.Fatalf("the corrected endpoint's key was not stored: %+v", store.puts)
	}
	after, err := justcode.ReadSetupJournal(fs, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := after.Platform(justcode.ProviderOpenAICompatible)
	if !ok || entry.BaseURL != "https://new.example.com/v1" || entry.Name != "Better" {
		t.Fatalf("journal kept the old endpoint: %+v", after.Platforms)
	}
}

func TestPromptSetupModelsReplacesADisconnectedPlatformDefault(t *testing.T) {
	previousCatalogue, previousRun := setupProviderCatalogueFn, setupModelPickerRun
	setupProviderCatalogueFn = func(justcode.InferenceProvider) ([]justcode.CatalogueModel, error) {
		return []justcode.CatalogueModel{{ID: "m1"}, {ID: "m2"}}, nil
	}
	var sawExternal bool
	setupModelPickerRun = func(picker setupModelPicker) (setupModelPicker, error) {
		sawExternal = picker.externalDefault
		picker.done = true
		return picker, nil
	}
	t.Cleanup(func() {
		setupProviderCatalogueFn, setupModelPickerRun = previousCatalogue, previousRun
	})
	providers, err := justcode.ResolveProviders([]justcode.ProviderSettings{{Kind: justcode.ProviderMistral}})
	if err != nil {
		t.Fatal(err)
	}
	selected, defaultModel, ok, err := promptSetupModels(providers, []string{"albert/qwen3-coder"}, "albert/qwen3-coder")
	if err != nil || !ok {
		t.Fatalf("prompt: selected=%v default=%q ok=%v err=%v", selected, defaultModel, ok, err)
	}
	if sawExternal {
		t.Fatal("a disconnected managed platform's default must not be treated as external")
	}
	if defaultModel != "mistral/m1" || !containsString(selected, "mistral/m1") {
		t.Fatalf("default must move to the connected platform: default=%q selected=%v", defaultModel, selected)
	}
}

func TestReconcileSetupModelsForProvidersPreservesUnmanagedDefault(t *testing.T) {
	providers, err := justcode.ResolveProviders([]justcode.ProviderSettings{{Kind: justcode.ProviderMistral}})
	if err != nil {
		t.Fatal(err)
	}
	selected := []string{"mistral/mistral-large-latest"}
	models, defaultModel := reconcileSetupModelsForProviders(providers, selected, "anthropic/claude")
	if defaultModel != "anthropic/claude" {
		t.Fatalf("an unmanaged default must survive an unchanged setup: default=%q", defaultModel)
	}
	if len(models) != 1 || models[0] != selected[0] {
		t.Fatalf("the managed model selection must stay unchanged: models=%v", models)
	}
}

func TestReconcileSetupModelsForProvidersReplacesRemovedManagedDefault(t *testing.T) {
	providers, err := justcode.ResolveProviders([]justcode.ProviderSettings{{Kind: justcode.ProviderMistral}})
	if err != nil {
		t.Fatal(err)
	}
	selected := []string{"mistral/mistral-large-latest"}
	_, defaultModel := reconcileSetupModelsForProviders(providers, selected, "albert/qwen3-coder")
	if defaultModel != selected[0] {
		t.Fatalf("a removed managed platform's default must be replaced: default=%q", defaultModel)
	}
}

func TestSetupPlatformStageSeedsFromSavedSettings(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	previousPresent := setupCredentialPresentFn
	setupCredentialPresentFn = func(context.Context, justcode.CredentialKind) bool { return true }
	t.Cleanup(func() { setupCredentialPresentFn = previousPresent })
	settingsPath, err := justcode.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := justcode.WriteUserSettings(justcode.DefaultFS, settingsPath, justcode.UserSettings{
		SchemaVersion: 1,
		Providers: []justcode.ProviderSettings{
			{Kind: justcode.ProviderAlbert},
			{Kind: justcode.ProviderMistral},
			{Kind: justcode.ProviderOpenAICompatible, Name: "Scaleway", BaseURL: "https://api.scaleway.ai/v1"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	fs := justcode.DefaultFS
	j := justcode.SetupJournal{SchemaVersion: 1, Stage: justcode.StageCredential}
	store := &fakeSetupStore{}
	w := justcode.SetupWizard{
		FS: fs, StateDir: stateDir,
		Store: func(context.Context) (justcode.SetupStore, error) { return store, nil },
		ValidateProvider: func(context.Context, justcode.InferenceProvider, string) justcode.CredentialProbe {
			t.Fatal("stored platforms must not be re-probed")
			return justcode.CredentialProbe{}
		},
	}
	providers, err := setupPlatformStage(context.Background(), w, bufio.NewReader(strings.NewReader("")), true, &j, fs, stateDir)
	if err != nil {
		t.Fatalf("platform stage: %v", err)
	}
	if len(providers) != 3 {
		t.Fatalf("saved platforms were not seeded: %+v", providers)
	}
	if providers[2].BaseURL != "https://api.scaleway.ai/v1" || providers[2].Name != "Scaleway" {
		t.Fatalf("custom endpoint was not preserved: %+v", providers[2])
	}
	if len(store.puts) != 0 {
		t.Fatalf("no key should be re-stored: %+v", store.puts)
	}
}

func TestSetupPlatformStageReasksAMissingCredential(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	previousPresent := setupCredentialPresentFn
	setupCredentialPresentFn = func(_ context.Context, kind justcode.CredentialKind) bool {
		return kind == justcode.CredentialAlbert
	}
	t.Cleanup(func() { setupCredentialPresentFn = previousPresent })
	settingsPath, err := justcode.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := justcode.WriteUserSettings(justcode.DefaultFS, settingsPath, justcode.UserSettings{
		SchemaVersion: 1,
		Providers:     []justcode.ProviderSettings{{Kind: justcode.ProviderAlbert}, {Kind: justcode.ProviderMistral}},
	}); err != nil {
		t.Fatal(err)
	}
	fs := justcode.DefaultFS
	j := justcode.SetupJournal{SchemaVersion: 1, Stage: justcode.StageCredential}
	store := &fakeSetupStore{}
	w := justcode.SetupWizard{
		FS: fs, StateDir: stateDir,
		Store: func(context.Context) (justcode.SetupStore, error) { return store, nil },
		ValidateProvider: func(context.Context, justcode.InferenceProvider, string) justcode.CredentialProbe {
			return justcode.CredentialProbe{}
		},
	}
	if _, err := setupPlatformStage(context.Background(), w, bufio.NewReader(strings.NewReader("new-mistral-key\n")), true, &j, fs, stateDir); err != nil {
		t.Fatalf("platform stage: %v", err)
	}
	if store.puts[justcode.CredentialMistral] != "new-mistral-key" {
		t.Fatalf("a missing credential must be asked again: %+v", store.puts)
	}
}

func TestSetupModelPickerAcceptsDefaultsAndChangesDefaultInOneForm(t *testing.T) {
	choices := albertSetupChoices()
	picker := setupModelPicker{
		choices: choices,
		selected: map[string]bool{
			choices[0].Ref(): true,
			choices[1].Ref(): true,
		},
		unavailable:  map[string]bool{},
		defaultModel: choices[0].Ref(),
	}
	updated, _ := picker.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	accepted := updated.(setupModelPicker)
	if !accepted.done || accepted.defaultModel != choices[0].Ref() {
		t.Fatalf("Enter should accept the preselected defaults: %+v", accepted)
	}

	picker.done = false
	picker.cursor = 2
	updated, _ = picker.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	picker = updated.(setupModelPicker)
	if !picker.selected[choices[2].Ref()] {
		t.Fatal("Space should add the highlighted model")
	}
	updated, _ = picker.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	picker = updated.(setupModelPicker)
	if picker.defaultModel != choices[2].Ref() {
		t.Fatalf("right arrow should mark the cursor model as default: %q", picker.defaultModel)
	}
	updated, _ = picker.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	accepted = updated.(setupModelPicker)
	if !accepted.done || accepted.defaultModel != choices[2].Ref() || !accepted.selected[choices[2].Ref()] {
		t.Fatal("Enter should submit the model set and its default together")
	}
}

func TestSetupModelPickerRefusesUnavailableModel(t *testing.T) {
	choices := albertSetupChoices()
	picker := setupModelPicker{
		choices: choices,
		selected: map[string]bool{
			choices[0].Ref(): true,
		},
		unavailable: map[string]bool{
			choices[1].Ref(): true,
		},
		defaultModel: choices[0].Ref(),
		cursor:       1,
	}
	updated, _ := picker.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	picker = updated.(setupModelPicker)
	if picker.selected[choices[1].Ref()] {
		t.Fatal("unavailable model must not be added to the selection")
	}
	if picker.errorMessage == "" {
		t.Fatal("unavailable selection should explain why it cannot be toggled")
	}
}

func TestPromptSetupModelsPreservesLegacyCustomDefault(t *testing.T) {
	previousCatalogue, previousRun := setupModelCatalogueFn, setupModelPickerRun
	choices := justcode.CuratedAlbertModels()
	setupModelCatalogueFn = func() (justcode.Catalogue, bool, error) {
		catalogue := justcode.Catalogue{}
		for _, choice := range choices {
			catalogue.Models = append(catalogue.Models, justcode.CatalogueModel{ID: choice.ID, Type: "text-generation"})
		}
		return catalogue, true, nil
	}
	var sawLegacy bool
	setupModelPickerRun = func(picker setupModelPicker) (setupModelPicker, error) {
		for _, choice := range picker.choices {
			if choice.Ref() == "albert/legacy-coder" {
				sawLegacy = picker.selected[choice.Ref()] && picker.defaultModel == choice.Ref()
			}
		}
		picker.done = true
		return picker, nil
	}
	t.Cleanup(func() {
		setupModelCatalogueFn, setupModelPickerRun = previousCatalogue, previousRun
	})

	model := "albert/legacy-coder"
	selected, defaultModel, ok, err := promptSetupModels(albertProvidersForSetup(), setupModelIDsWithLegacyDefault(model), model)
	if err != nil || !ok {
		t.Fatalf("prompt result: selected=%v default=%q ok=%v err=%v", selected, defaultModel, ok, err)
	}
	if !sawLegacy || !containsString(selected, "legacy-coder") || defaultModel != model {
		t.Fatalf("legacy custom model was not preserved: selected=%v default=%q sawLegacy=%v", selected, defaultModel, sawLegacy)
	}
}

func TestPromptSetupModelsPreservesExternalProviderDefault(t *testing.T) {
	previousCatalogue, previousRun := setupModelCatalogueFn, setupModelPickerRun
	choices := justcode.CuratedAlbertModels()
	setupModelCatalogueFn = func() (justcode.Catalogue, bool, error) {
		catalogue := justcode.Catalogue{}
		for _, choice := range choices {
			catalogue.Models = append(catalogue.Models, justcode.CatalogueModel{ID: choice.ID, Type: "text-generation"})
		}
		return catalogue, true, nil
	}
	var sawExternalDefault bool
	setupModelPickerRun = func(picker setupModelPicker) (setupModelPicker, error) {
		sawExternalDefault = picker.externalDefault && picker.defaultModel == "anthropic/claude"
		picker.done = true
		return picker, nil
	}
	t.Cleanup(func() {
		setupModelCatalogueFn, setupModelPickerRun = previousCatalogue, previousRun
	})

	selected, defaultModel, ok, err := promptSetupModels(albertProvidersForSetup(), defaultSetupModelIDs(), "anthropic/claude")
	if err != nil || !ok {
		t.Fatalf("prompt result: selected=%v default=%q ok=%v err=%v", selected, defaultModel, ok, err)
	}
	if !sawExternalDefault || defaultModel != "anthropic/claude" {
		t.Fatalf("external provider default was not preserved: default=%q sawExternal=%v", defaultModel, sawExternalDefault)
	}
}

func TestCanonicalSetupModelRefMapsLegacyCatalogueID(t *testing.T) {
	choice := justcode.CuratedAlbertModels()[1]
	legacyRef := "albert/" + choice.ID
	if got := canonicalSetupModelRef(legacyRef); got != choice.Ref() {
		t.Fatalf("legacy reference %q mapped to %q, want %q", legacyRef, got, choice.Ref())
	}
	if !setupModelEnabled(legacyRef, defaultSetupModelIDs()) {
		t.Fatalf("legacy full API reference %q should match the selected DeepSeek model", legacyRef)
	}
}

func TestPromptSetupModelsKeepsDefaultsWhenCatalogueIsUnavailable(t *testing.T) {
	previousCatalogue, previousRun := setupModelCatalogueFn, setupModelPickerRun
	setupModelCatalogueFn = func() (justcode.Catalogue, bool, error) {
		return justcode.Catalogue{}, false, errors.New("catalogue offline")
	}
	setupModelPickerRun = func(picker setupModelPicker) (setupModelPicker, error) {
		picker.done = true
		return picker, nil
	}
	t.Cleanup(func() {
		setupModelCatalogueFn, setupModelPickerRun = previousCatalogue, previousRun
	})

	selected, defaultModel, ok, err := promptSetupModels(albertProvidersForSetup(), defaultSetupModelIDs(), defaultSetupModel())
	if err != nil || !ok {
		t.Fatalf("prompt result: selected=%v default=%q ok=%v err=%v", selected, defaultModel, ok, err)
	}
	if !sameStrings(selected, defaultSetupModelIDs()) {
		t.Fatalf("catalogue outage changed default selections: %v", selected)
	}
	if defaultModel != "albert/deepseek-v4-flash" {
		t.Fatalf("default model = %q, want albert/deepseek-v4-flash", defaultModel)
	}
}

func TestPromptSetupModelsDropsUnavailableDefaults(t *testing.T) {
	previousCatalogue, previousRun := setupModelCatalogueFn, setupModelPickerRun
	choices := justcode.CuratedAlbertModels()
	setupModelCatalogueFn = func() (justcode.Catalogue, bool, error) {
		return justcode.Catalogue{Models: []justcode.CatalogueModel{{ID: choices[0].ID, Type: "text-generation"}}}, true, nil
	}
	setupModelPickerRun = func(picker setupModelPicker) (setupModelPicker, error) {
		picker.done = true
		return picker, nil
	}
	t.Cleanup(func() {
		setupModelCatalogueFn, setupModelPickerRun = previousCatalogue, previousRun
	})

	selected, defaultModel, ok, err := promptSetupModels(albertProvidersForSetup(), defaultSetupModelIDs(), defaultSetupModel())
	if err != nil || !ok {
		t.Fatalf("prompt result: selected=%v default=%q ok=%v err=%v", selected, defaultModel, ok, err)
	}
	if !sameStrings(selected, []string{choices[0].ID}) || defaultModel != choices[0].Ref() {
		t.Fatalf("unavailable default remained selected: selected=%v default=%q", selected, defaultModel)
	}
}

func TestPromptSetupModelsKeepsSelectionWhenCatalogueCacheIsStale(t *testing.T) {
	previousCatalogue, previousRun := setupModelCatalogueFn, setupModelPickerRun
	choices := justcode.CuratedAlbertModels()
	setupModelCatalogueFn = func() (justcode.Catalogue, bool, error) {
		return justcode.Catalogue{Models: []justcode.CatalogueModel{{ID: choices[0].ID, Type: "text-generation"}}}, false, nil
	}
	var retained bool
	setupModelPickerRun = func(picker setupModelPicker) (setupModelPicker, error) {
		retained = picker.selected[choices[0].Ref()] && picker.selected[choices[1].Ref()]
		picker.done = true
		return picker, nil
	}
	t.Cleanup(func() {
		setupModelCatalogueFn, setupModelPickerRun = previousCatalogue, previousRun
	})

	selected, _, ok, err := promptSetupModels(albertProvidersForSetup(), defaultSetupModelIDs(), defaultSetupModel())
	if err != nil || !ok {
		t.Fatalf("prompt result: selected=%v ok=%v err=%v", selected, ok, err)
	}
	if !retained || !sameStrings(selected, defaultSetupModelIDs()) {
		t.Fatalf("stale cache should not remove a default selection: retained=%v selected=%v", retained, selected)
	}
}

// setupTestEnv isolates HOME and returns the state dir and settings path.
func setupTestEnv(t *testing.T) (stateDir, settingsPath string) {
	t.Helper()
	home := t.TempDir()
	// os.UserHomeDir reads HOME on unix and USERPROFILE on windows;
	// os.UserConfigDir reads XDG_CONFIG_HOME on unix and AppData on
	// windows. Set both families so the disposable HOME is honored on
	// every CI leg.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	gitConfig := filepath.Join(home, ".gitconfig")
	if err := os.WriteFile(gitConfig, []byte("[user]\n\tname = Test User\n\temail = test@example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	previousIdentity := hostGitIdentityFn
	hostGitIdentityFn = func() (string, string) { return "", "" }
	t.Cleanup(func() { hostGitIdentityFn = previousIdentity })
	// msbRuntimeHome reads HOME; an empty dir means "not installed", which
	// keeps the primitive probe path deterministic.
	return justcode.DefaultStateDir(), ""
}

// forceNonTTY pins the wizard's TTY probe to false for the test (the piped
// path is the one under test); the original is restored on cleanup.
func forceNonTTY(t *testing.T) {
	t.Helper()
	orig := stdinIsTTYFn
	origChoosePlatforms := setupChoosePlatformsFn
	origResumePlatforms := setupResumePlatformsFn
	stdinIsTTYFn = func() bool { return false }
	setupChoosePlatformsFn = func(current []justcode.SetupPlatform, _ *bufio.Reader) ([]justcode.SetupPlatform, error) {
		if len(current) > 0 {
			return current, nil
		}
		return []justcode.SetupPlatform{{Kind: string(justcode.ProviderAlbert)}}, nil
	}
	// A journaled resume keeps its selection without consuming input, so the
	// scripted tests below keep their pre-#305 prompt sequences.
	setupResumePlatformsFn = func(_ []justcode.SetupPlatform, _ *bufio.Reader, _ string) (bool, error) {
		return true, nil
	}
	t.Cleanup(func() {
		stdinIsTTYFn = orig
		setupChoosePlatformsFn = origChoosePlatforms
		setupResumePlatformsFn = origResumePlatforms
	})
}

// forceIntro pins the intro gate for the test. Pass true to simulate a run
// that can show and answer the intro, false for a run without a terminal.
func forceIntro(t *testing.T, show bool) {
	t.Helper()
	orig := setupIntroFn
	setupIntroFn = func() bool { return show }
	t.Cleanup(func() { setupIntroFn = orig })
}

// forceEnter makes the intro's Enter press read from the same piped reader the
// wizard uses, so a piped test still drives the intro. Production reads the
// terminal directly, one byte at a time, so pasted typed-ahead text is left for
// the next prompt.
func forceEnter(t *testing.T, in *bufio.Reader) {
	t.Helper()
	orig := waitForEnterFn
	waitForEnterFn = func() bool {
		_, ok := promptLine(in, "")
		return ok
	}
	t.Cleanup(func() { waitForEnterFn = orig })
}

// forcePreflightPass stubs the host preflight to a non-fatal result, so tests
// can exercise the fresh-start branch (intro, then the first prompt) on a host
// whose real preflight would stop the wizard before any prompt.
func forcePreflightPass(t *testing.T) {
	t.Helper()
	orig := setupPreflightFn
	setupPreflightFn = func(context.Context) justcode.SetupPreflight {
		return justcode.SetupPreflight{Platform: "test/arch", VirtualizationOK: true}
	}
	t.Cleanup(func() { setupPreflightFn = orig })
}

// journalPastPreflight writes a journal whose preflight already passed, so
// the flow starts at the credential stage even on a KVM-less host.
func journalPastPreflight(t *testing.T, stateDir string) {
	t.Helper()
	j, err := justcode.ReadSetupJournal(justcode.DefaultFS, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	j.Stage = justcode.StageCredential
	if err := justcode.WriteSetupJournal(justcode.DefaultFS, stateDir, j); err != nil {
		t.Fatal(err)
	}
}

func TestSetupDoctorPlainOutputSeparatesSections(t *testing.T) {
	setupTestEnv(t)
	origCollect := setupDoctorCollectFn
	setupDoctorCollectFn = func() justcode.DiagnosticsReport {
		return justcode.DiagnosticsReport{
			Platform: "test/arch",
			Sections: []justcode.DiagnosticSection{
				{Title: "Capacité de l'hôte", Items: []justcode.DiagnosticItem{
					{Name: "Virtualisation", Value: "ok", Status: justcode.DiagnosticOK},
				}},
				{Title: "Runtime et état de l'instance du projet", Items: []justcode.DiagnosticItem{
					{Name: "Runtime Microsandbox géré", Value: "ok", Status: justcode.DiagnosticOK},
				}},
				{Title: "Vérification Albert", Items: []justcode.DiagnosticItem{
					{Name: "Point d'accès Albert", Value: "validé", Status: justcode.DiagnosticOK},
				}},
			},
		}
	}
	origRender := doctorRendererFn
	doctorRendererFn = func() bool { return false }
	t.Cleanup(func() { setupDoctorCollectFn = origCollect; doctorRendererFn = origRender })

	out := captureStdout(t, func() {
		code, err := setupDoctorCmd(false)
		if err != nil || code != 0 {
			t.Fatalf("setup doctor: code=%d err=%v", code, err)
		}
	})
	for _, want := range []string{"Capacité de l'hôte:", "Runtime et état de l'instance du projet:", "Vérification Albert:", "Résultat : aucun problème bloquant détecté"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q: %q", want, out)
		}
	}
}

func TestSetupDoctorFailedItemReturnsNonZero(t *testing.T) {
	setupTestEnv(t)
	origCollect := setupDoctorCollectFn
	setupDoctorCollectFn = func() justcode.DiagnosticsReport {
		return justcode.DiagnosticsReport{Platform: "test/arch", Sections: []justcode.DiagnosticSection{{
			Title: "Vérification Albert",
			Items: []justcode.DiagnosticItem{{Name: "Point d'accès Albert", Value: "refusé", Status: justcode.DiagnosticFailed}},
		}}}
	}
	origRender := doctorRendererFn
	doctorRendererFn = func() bool { return false }
	t.Cleanup(func() { setupDoctorCollectFn = origCollect; doctorRendererFn = origRender })

	out := captureStdout(t, func() {
		code, err := setupDoctorCmd(false)
		if err != nil || code != 1 {
			t.Fatalf("failed diagnostics must return code 1: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(out, "au moins un contrôle a échoué") {
		t.Fatalf("doctor output must state the failure: %q", out)
	}
}

func TestSetupDoctorJSONOutputIsMachineReadable(t *testing.T) {
	setupTestEnv(t)
	origCollect := setupDoctorCollectFn
	setupDoctorCollectFn = func() justcode.DiagnosticsReport {
		return justcode.DiagnosticsReport{Platform: "test/arch", Sections: []justcode.DiagnosticSection{{
			Title: "Capacité de l'hôte",
			Items: []justcode.DiagnosticItem{{Name: "Virtualisation", Value: "ok", Status: justcode.DiagnosticOK}},
		}}}
	}
	t.Cleanup(func() { setupDoctorCollectFn = origCollect })

	out := captureStdout(t, func() {
		code, err := setupCmd([]string{"doctor", "--json"})
		if err != nil || code != 0 {
			t.Fatalf("setup doctor --json: code=%d err=%v", code, err)
		}
	})
	var report justcode.DiagnosticsReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("doctor JSON output is invalid: %v: %q", err, out)
	}
	if report.Platform != "test/arch" || len(report.Sections) != 1 || report.Sections[0].Title != "Capacité de l'hôte" {
		t.Fatalf("doctor JSON report = %+v", report)
	}
}

func TestSetupDoctorNoColorUsesPlainOutput(t *testing.T) {
	setupTestEnv(t)
	origCollect, origRender, origNoColor := setupDoctorCollectFn, doctorRendererFn, wizardNoColor
	setupDoctorCollectFn = func() justcode.DiagnosticsReport {
		return justcode.DiagnosticsReport{Platform: "test/arch", Sections: []justcode.DiagnosticSection{{
			Title: "Capacité de l'hôte",
			Items: []justcode.DiagnosticItem{{Name: "Virtualisation", Value: "ok", Status: justcode.DiagnosticOK}},
		}}}
	}
	doctorRendererFn = func() bool { return !wizardNoColor }
	wizardNoColor = false
	t.Cleanup(func() {
		setupDoctorCollectFn, doctorRendererFn, wizardNoColor = origCollect, origRender, origNoColor
	})

	out := captureStdout(t, func() {
		code, err := setupCmd([]string{"doctor", "--no-color"})
		if err != nil || code != 0 {
			t.Fatalf("setup doctor --no-color: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(out, "Capacité de l'hôte:") || strings.Contains(out, "│") {
		t.Fatalf("--no-color did not use the plain doctor renderer: %q", out)
	}
}

func readUserSettingsFile(t *testing.T) map[string]any {
	t.Helper()
	path, err := justcode.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestSetupIntroScreenContent pins the screen shown before any prompt: the
// README banner, the "3 étapes" announcement the [n/3] counter later follows,
// the duration/disk figures, and the Ctrl+C hint.
func TestSetupIntroScreenContent(t *testing.T) {
	for _, want := range []string{
		"(_)_  _______/ /_",
		"just code : coder avec l'IA souveraine",
		"Cette configuration se fait une seule fois sur ce poste.",
		"Durée : quelques minutes pour répondre aux questions. Espace disque : au moins 1 Go libre pour la machine isolée.",
		"3 étapes :",
		"1. Plateformes d'inférence (au moins une)",
		"2. Sélection des modèles à utiliser.",
		"3. GitHub (facultatif)",
		"Vos identifiants sont enregistrés au fil des questions,",
		"pour que la configuration puisse reprendre si elle est interrompue.",
		"Appuyez sur Entrée pour commencer, ou sur Ctrl+C pour quitter.",
	} {
		if !strings.Contains(setupIntroScreen, want) {
			t.Errorf("intro screen is missing %q", want)
		}
	}
}

// TestWaitForEnterLeavesTypedAheadInput is the regression test for the round-3
// finding on #215: the intro must consume exactly the Enter press, so a key the
// user pasted along with it is still there for the next prompt.
//
// It runs the REAL waitForEnter against a pipe: a buffered read would drain the
// whole pipe, so asserting on what remains afterwards is what distinguishes the
// two implementations.
func TestWaitForEnterLeavesTypedAheadInput(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := w.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	// The Enter press is the leading newline; everything the user pasted along
	// with it follows and must still be there afterwards.
	if _, err := w.WriteString("pasted-key\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()

	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })

	if !waitForEnter() {
		t.Fatal("waitForEnter must report that Enter was read")
	}
	// Only the newline may be gone: reading the pipe again must yield nothing.
	rest, _ := io.ReadAll(r)
	if string(rest) != "pasted-key\n" {
		t.Fatalf("waitForEnter consumed typed-ahead input: %q", rest)
	}
}

// TestWaitForEnterStopsAtTheFirstNewline is the complement: input queued
// BEFORE the Enter press belongs to the intro and must be consumed with it.
func TestWaitForEnterStopsAtTheFirstNewline(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := w.WriteString("noise\n\nsecond-line\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()

	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })

	if !waitForEnter() {
		t.Fatal("waitForEnter must report that Enter was read")
	}
	rest, _ := io.ReadAll(r)
	if string(rest) != "\nsecond-line\n" {
		t.Fatalf("waitForEnter must stop at the first newline; left %q", rest)
	}
}

// TestSetupWizardShowsIntroBeforeAnyPrompt pins that on a fresh run the intro
// is printed before the first question, that Enter (the leading empty line of
// the piped answers) starts the wizard, and that the screen precedes the key
// prompt.
func TestSetupWizardShowsIntroBeforeAnyPrompt(t *testing.T) {
	setupTestEnv(t)
	forceNonTTY(t)
	forceIntro(t, true)
	// Fresh run (no journal past preflight), so the intro is reached through
	// the real preflight branch; the preflight itself is stubbed to pass so a
	// KVM-less runner still gets there.
	forcePreflightPass(t)
	// Leading empty line = Enter on the intro; then key, model, no GitHub,
	// defaults. The empty line after the key is the model answer, so the "n"
	// that follows is the GitHub answer, not the model one: the order is key ->
	// models -> GitHub.
	input := "\ntest-key\n\nn\nTest User\ntest@example.test\ny\n"
	// The same reader feeds both the intro's Enter and the wizard body, so the
	// leading empty line is consumed exactly once.
	in := bufio.NewReader(strings.NewReader(input))
	forceEnter(t, in)
	out := captureStdout(t, func() {
		code, err := setupWizardRun(true, in,
			func(context.Context, string) error { return nil },
			func(context.Context) error { return nil },
			func(string) string { return "" })
		if code != 0 || err != nil {
			t.Errorf("wizard run after intro: code=%d err=%v", code, err)
		}
	})
	introAt := strings.Index(out, "3 étapes :")
	if introAt < 0 {
		t.Fatalf("fresh run did not print the intro screen; output:\n%s", out)
	}
	keyAt := strings.Index(out, "[1/3] Clé Albert API")
	if keyAt < 0 {
		t.Fatalf("the Albert prompt never appeared; output:\n%s", out)
	}
	if introAt > keyAt {
		t.Errorf("intro must precede the first prompt (intro at %d, prompt at %d)", introAt, keyAt)
	}
}

// TestSetupWizardResumeSkipsIntro pins that only a first launch shows the
// intro: a resumed run (journal already past preflight) goes straight to the
// resume notice and the first prompt.
func TestSetupWizardResumeSkipsIntro(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	forceIntro(t, true) // a terminal is available; the intro must still be skipped
	journalPastPreflight(t, stateDir)
	out := captureStdout(t, func() {
		// key, model, no GitHub, defaults: the empty line is the model answer,
		// so the "n" that follows answers the GitHub question.
		code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader("test-key\n\nn\nTest User\ntest@example.test\ny\n")),
			func(context.Context, string) error { return nil },
			func(context.Context) error { return nil },
			func(string) string { return "" })
		if code != 0 || err != nil {
			t.Errorf("resume run: code=%d err=%v", code, err)
		}
	})
	if strings.Contains(out, "3 étapes :") {
		t.Errorf("a resumed run must not re-print the intro screen; output:\n%s", out)
	}
	if !strings.Contains(out, "Reprise de la configuration là où elle s'était arrêtée") {
		t.Errorf("resume notice missing; output:\n%s", out)
	}
}

// TestSetupWizardNoIntroWithoutTerminal pins the guard: a run that cannot show
// and answer the intro (piped input) must not print it, because the program
// would otherwise wait for a keypress nobody can send.
func TestSetupWizardNoIntroWithoutTerminal(t *testing.T) {
	setupTestEnv(t)
	forceNonTTY(t)
	// A FRESH run, so the intro gate is actually evaluated: journalling past
	// preflight would take the resume branch instead, where the gate is never
	// consulted and this test would assert nothing about the guard.
	forcePreflightPass(t)
	// The gate is left REAL (stdin/stdout are not terminals under `go test`),
	// which is the condition under test.
	// No leading Enter: the first line is the key. If the wizard waited for an
	// intro keypress, the key would be swallowed and validation would fail.
	// key, model, no GitHub, defaults.
	input := "test-key\n\nn\nTest User\ntest@example.test\ny\n"
	out := captureStdout(t, func() {
		code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
			func(context.Context, string) error { return nil },
			func(context.Context) error { return nil },
			func(string) string { return "" })
		if code != 0 || err != nil {
			t.Errorf("wizard run without intro: code=%d err=%v", code, err)
		}
	})
	if strings.Contains(out, "3 étapes :") {
		t.Errorf("a run without a terminal must not print the intro screen; output:\n%s", out)
	}
}

func TestSetupWizardRejectedKeyStoresNothing(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	// The store seam: a fake that records puts. The wizard's Store func is
	// not injectable per-run through setupWizardRun (only validate and
	// ensure are), so this test uses the file fallback store: a rejected
	// key must leave it absent (nothing was stored).
	// #162 changed the contract: a refusal re-asks instead of ending the run,
	// so the flow only stops when the re-ask is answered with an empty key.
	input := "bad-key\n\n"
	code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
		func(context.Context, string) error {
			return justcode.RejectedCredentialError{Detail: "HTTP 401"}
		},
		func(context.Context) error { return nil },
		func(string) string { return "" })
	// An empty key abandons the run, which reports the resumable cancel
	// message (non-nil) rather than silently succeeding.
	if code == 0 {
		t.Fatalf("abandoning after a rejected key must exit non-zero: code=%d err=%v", code, err)
	}
	// The journal must not claim the credential was stored.
	j, rerr := justcode.ReadSetupJournal(justcode.DefaultFS, stateDir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if justcode.ContainsCredentialKind(j.CredentialKinds, "albert") {
		t.Fatal("a rejected key must not be journaled as stored")
	}
	// The file fallback store must not exist (no Put happened).
	if store, err := justcode.ConsentFileStore(); err == nil {
		if _, serr := os.Stat(store.Path); serr == nil {
			t.Fatal("a rejected key must not create the credential store")
		}
	}
}

func TestSetupCompletionMessageDescribesFullDefault(t *testing.T) {
	message := setupCompletionMessage()
	for _, want := range []string{"Configuration terminée.", "cd mon-projet", "just-code setup doctor"} {
		if !strings.Contains(message, want) {
			t.Fatalf("setup completion message = %q, want %q", message, want)
		}
	}
	if strings.Contains(message, "Microsandbox/full") {
		t.Fatalf("setup completion message exposes internal runtime details: %q", message)
	}
}

func TestSetupRuntimeSummaryDescribesReuseWithoutDownload(t *testing.T) {
	message := setupRuntimeSummary(justcode.SetupPreflight{RuntimeInstalled: true})
	if !strings.Contains(message, "déjà installée") || !strings.Contains(message, "réutilisera") {
		t.Fatalf("runtime reuse summary = %q", message)
	}
	if strings.Contains(message, "2 Go") || strings.Contains(message, "téléchargera") {
		t.Fatalf("reused runtime summary must not promise a download or disk use: %q", message)
	}
	conditional := setupRuntimeSummary(justcode.SetupPreflight{})
	if !strings.Contains(conditional, "si aucune installation valide n'est disponible") ||
		!strings.Contains(conditional, "téléchargement, installation, puis démarrage") ||
		!strings.Contains(conditional, "dépend de votre connexion") || !strings.Contains(conditional, "1 Go libre") {
		t.Fatalf("missing-runtime summary should separate download, install and first launch without promising a duration: %q", conditional)
	}
}

func TestSetupWizardNonTTYAnswersTrimNewlines(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	// Piped answers in wizard order: key with a trailing newline (the raw
	// pipe line), empty model, no GitHub, explicit identity, apply.
	input := "test-key\n\nn\nTest User\ntest@example.test\ny\n"
	var seenKey string
	code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
		func(_ context.Context, key string) error {
			seenKey = key
			return nil
		},
		func(context.Context) error { return nil },
		func(string) string { return "" })
	if code != 0 || err != nil {
		t.Fatalf("wizard run: code=%d err=%v", code, err)
	}
	if seenKey != "test-key" {
		t.Fatalf("the piped key must arrive trimmed, got %q", seenKey)
	}
	// A completed setup removes the journal.
	if _, serr := os.Stat(justcode.SetupJournalPath(stateDir)); !os.IsNotExist(serr) {
		t.Fatal("a completed setup must remove the journal")
	}
}

// TestSetupWizardUsesHostGitIdentity keeps the host's configured commit
// identity instead of attributing commits to a generic just-code account.
func TestSetupWizardUsesHostGitIdentity(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	hostGitIdentityFn = func() (string, string) { return "Test User", "test@example.test" }
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	// The host identity is present, so the wizard skips both identity prompts.
	input := "test-key\n\nn\n\n\n\ny\n"
	code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
		func(context.Context, string) error { return nil },
		func(context.Context) error { return nil },
		func(string) string { return "" })
	if code != 0 || err != nil {
		t.Fatalf("wizard run: code=%d err=%v", code, err)
	}
	m := readUserSettingsFile(t)
	if m == nil {
		t.Fatal("settings file must exist after a completed setup")
	}
	if got, _ := m["gitName"].(string); got != "Test User" {
		t.Fatalf("git name = %q, want host identity", got)
	}
	if got, _ := m["gitEmail"].(string); got != "test@example.test" {
		t.Fatalf("git email = %q, want host identity", got)
	}
}

func TestSetupIdentityFormSkipsEmptyFieldList(t *testing.T) {
	previousRun := huhRun
	called := false
	huhRun = func(*huh.Form) error {
		called = true
		return nil
	}
	t.Cleanup(func() { huhRun = previousRun })
	if err := runSetupIdentityForm(nil); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("identity form should not run when host git config supplied both values")
	}
}

func TestSetupWizardApplyCancelsPendingGitHubReplacement(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	verified := true
	j, err := justcode.ReadSetupJournal(justcode.DefaultFS, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	j.CredentialKinds = []string{"albert", "github"}
	j.AlbertVerified = &verified
	j.GitHubLogin = "old-account"
	j.GitHubVerified = &verified
	j.GitHubReplacePending = true
	j.GitName, j.GitEmail = "Test User", "test@example.test"
	j.DefaultModel = defaultSetupModel()
	j.Models = defaultSetupModelIDs()
	j.ModelChosen = true
	if err := justcode.WriteSetupJournal(justcode.DefaultFS, stateDir, j); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader("\n")), nil,
			func(context.Context) error { return nil }, func(string) string { return "" })
		if code != 0 || err != nil {
			t.Fatalf("apply with the existing token: code=%d err=%v", code, err)
		}
	})
	// The cleanup runs before the apply (#333), so the recap must state the kept
	// token without promising a replacement the run is not making.
	if !strings.Contains(out, "Remplacement GitHub annulé ; le jeton existant sera conservé.") {
		t.Errorf("apply must clear the pending replacement; output:\n%s", out)
	}
	if strings.Contains(out, "remplacement en attente") {
		t.Errorf("the recap must not announce a pending replacement; output:\n%s", out)
	}
	if !strings.Contains(out, "Récapitulatif") {
		t.Errorf("the recap must be printed after the apply; output:\n%s", out)
	}
	if _, err := os.Stat(justcode.SetupJournalPath(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("completed setup must remove its journal, err=%v", err)
	}
}

func TestLegacySetupJournalRecapReportsUnknownCredentialVerification(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	j, err := justcode.ReadSetupJournal(justcode.DefaultFS, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	j.Stage = justcode.StageCredential
	j.CredentialKinds = []string{"albert", "github"}
	j.GitName, j.GitEmail = "Test User", "test@example.test"
	if err := justcode.WriteSetupJournal(justcode.DefaultFS, stateDir, j); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader("\n")), nil,
			func(context.Context) error { return nil }, func(string) string { return "" })
		if code != 0 || err != nil {
			t.Fatalf("completed setup: code=%d err=%v", code, err)
		}
	})
	for _, want := range []string{"état inconnu (journal historique)", "état de vérification inconnu (journal historique)"} {
		if !strings.Contains(out, want) {
			t.Errorf("legacy recap should report unknown status %q; output:\n%s", want, out)
		}
	}
}

// TestAlbertKeyScreenShowsStepAndLinks pins screen 3.2's content: the [1/3]
// counter, what the key is for, that it goes to the keychain (not the
// projects), and both links.
func TestAlbertKeyScreenShowsStepAndLinks(t *testing.T) {
	header := albertKeyScreenHeader(false)
	for _, want := range []string{
		"[1/3] Clé Albert API",
		"L'agent utilise Albert API, l'API d'IA générative de l'État.",
		"La clé est enregistrée dans le trousseau de ce poste, jamais dans vos projets.",
		"Vous n'avez pas encore de clé ?",
		albertCreateKeyURL,
		albertQuickstartURL,
	} {
		if !strings.Contains(header, want) {
			t.Errorf("Albert key screen is missing %q", want)
		}
	}
	if !strings.Contains(albertKeyPrompt, "elle ne s'affiche pas") {
		t.Errorf("the key prompt must say the value is not displayed: %q", albertKeyPrompt)
	}
}

// TestAlbertKeyScreenDescribesTheActualStore pins the fallback wording: with
// --fallback the key goes to a consented owner-only file, not the keychain, so
// the screen must not promise a keychain.
func TestAlbertKeyScreenDescribesTheActualStore(t *testing.T) {
	native := albertKeyScreenHeader(false)
	fallback := albertKeyScreenHeader(true)
	if !strings.Contains(native, "trousseau") {
		t.Errorf("the native store wording must name the keychain: %q", native)
	}
	if strings.Contains(fallback, "trousseau") {
		t.Errorf("the fallback wording must not claim a keychain: %q", fallback)
	}
	if !strings.Contains(fallback, "fichier") {
		t.Errorf("the fallback wording must say the key is stored in a file: %q", fallback)
	}
}

// TestAlbertRejectionMessageDistinguishesFromNetwork pins that a refusal names
// the key page and never reads like the network message.
func TestAlbertRejectionMessageDistinguishesFromNetwork(t *testing.T) {
	msg := albertRejectedMessage("HTTP 401")
	if !strings.Contains(msg, albertCreateKeyURL) {
		t.Errorf("a rejected key must point at the key page: %q", msg)
	}
	if !strings.Contains(msg, "refusé") {
		t.Errorf("a rejected key must say it was refused: %q", msg)
	}
	if msg == albertUnreachableMessage || albertUnreachableMessage == albertRejectedMessage("x") {
		t.Error("refusal and unreachable must be distinct messages")
	}
	if !strings.Contains(albertUnreachableMessage, "proxy") {
		t.Errorf("the unreachable message must mention the administration proxy: %q", albertUnreachableMessage)
	}
}

// TestSetupWizardRejectedKeyReasksInsteadOfExiting pins 3.2's acceptance
// criterion "une erreur ne fait jamais quitter l'assistant": after a refusal
// the wizard asks for the key again, and a valid key entered the second time
// completes setup.
func TestSetupWizardRejectedKeyReasksInsteadOfExiting(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	var attempts []string
	// First key refused, second accepted; then no GitHub and defaults.
	// key, key (re-ask), model, GitHub, name, email, apply.
	input := "bad-key\ngood-key\n\nn\nTest User\ntest@example.test\ny\n"
	out := captureStdout(t, func() {
		code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
			func(_ context.Context, key string) error {
				attempts = append(attempts, key)
				if key == "bad-key" {
					return justcode.RejectedCredentialError{Detail: "HTTP 401"}
				}
				return nil
			},
			func(context.Context) error { return nil },
			func(string) string { return "" })
		if code != 0 || err != nil {
			t.Fatalf("a refused key followed by a valid one must complete setup: code=%d err=%v", code, err)
		}
	})
	if len(attempts) != 2 || attempts[0] != "bad-key" || attempts[1] != "good-key" {
		t.Fatalf("the wizard must re-ask after a refusal; validated keys: %v", attempts)
	}
	if !strings.Contains(out, "refusé") || !strings.Contains(out, "✅ Clé valide.") {
		t.Errorf("expected a refusal notice then a success notice; output:\n%s", out)
	}
}

// TestSetupWizardUnreachableOffersRetryAndContinue pins the network branch:
// [R] re-enters the key, and the key is stored unvalidated when the user
// continues.
func TestSetupWizardUnreachableOffersRetryAndContinue(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	calls := 0
	// key -> unreachable -> R (retry) -> key again -> reachable -> defaults.
	// key, R (retry), key, model, GitHub, name, email, apply.
	input := "first-key\nr\nsecond-key\n\nn\nTest User\ntest@example.test\ny\n"
	out := captureStdout(t, func() {
		code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
			func(context.Context, string) error {
				calls++
				if calls == 1 {
					return fmt.Errorf("could not reach the Albert endpoint: HTTP 503")
				}
				return nil
			},
			func(context.Context) error { return nil },
			func(string) string { return "" })
		if code != 0 || err != nil {
			t.Fatalf("retry then success must complete setup: code=%d err=%v", code, err)
		}
	})
	if calls != 2 {
		t.Fatalf("R must re-validate the key; validation calls=%d", calls)
	}
	if !strings.Contains(out, albertUnreachableMessage) {
		t.Errorf("the unreachable message must be shown; output:\n%s", out)
	}
}

// TestSetupWizardUnreachableContinueStoresUnvalidated pins the [C] branch:
// continuing stores the key without validation and setup proceeds.
func TestSetupWizardUnreachableContinueStoresUnvalidated(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	calls := 0
	// key -> unreachable -> c (continue) -> defaults.
	// key, C (continue), model, GitHub, name, email, apply.
	input := "only-key\nc\n\nn\nTest User\ntest@example.test\ny\n"
	out := captureStdout(t, func() {
		code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
			func(context.Context, string) error {
				calls++
				return fmt.Errorf("could not reach the Albert endpoint: HTTP 503")
			},
			func(context.Context) error { return nil },
			func(string) string { return "" })
		if code != 0 || err != nil {
			t.Fatalf("continue without validation must complete setup: code=%d err=%v", code, err)
		}
	})
	if calls != 1 {
		t.Fatalf("C must not re-validate; validation calls=%d", calls)
	}
	if !strings.Contains(out, "Clé enregistrée sans vérification.") {
		t.Errorf("continuing must say the key was stored unvalidated; output:\n%s", out)
	}
	// A completed setup removes the journal, so assert on the outcome the
	// journal existed for: the credential is in the store and settings were
	// written.
	m := readUserSettingsFile(t)
	if m == nil {
		t.Fatal("a completed setup must write the settings file")
	}
	_ = stateDir
}

// TestSetupWizardFlowIsKeyThenModelsThenGitHub pins the epic's announced order:
// clé Albert API -> modèles -> GitHub. The [n/3] counters and the intro's
// "3 étapes" are only truthful in that order.
func TestSetupWizardFlowIsKeyThenModelsThenGitHub(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	// key, model, GitHub, name, email, apply.
	input := "test-key\n\nn\nTest User\ntest@example.test\ny\n"
	out := captureStdout(t, func() {
		code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
			func(context.Context, string) error { return nil },
			func(context.Context) error { return nil },
			func(string) string { return "" })
		if code != 0 || err != nil {
			t.Fatalf("wizard run: code=%d err=%v", code, err)
		}
	})
	keyAt := strings.Index(out, "[1/3] Clé Albert API")
	modelAt := strings.Index(out, "Models (comma-separated")
	githubAt := strings.Index(out, "[3/3] GitHub (facultatif)")
	if keyAt < 0 || modelAt < 0 || githubAt < 0 {
		t.Fatalf("flow markers missing (key=%d model=%d github=%d); output:\n%s", keyAt, modelAt, githubAt, out)
	}
	if !(keyAt < modelAt && modelAt < githubAt) {
		t.Errorf("order must be key -> models -> GitHub; got key=%d model=%d github=%d", keyAt, modelAt, githubAt)
	}
}

// TestExplicitDefaultModelSurvivesAnAbortAndAnUnansweredResume pins the Kimi
// review finding on #216, across two runs:
//
//  1. a completed install has a saved custom model, and a re-run answers the
//     model prompt with Enter to keep the existing choice but is then
//     aborted at the recap;
//  2. the resumed run's model prompt is never answered (EOF).
//
// The explicit default choice from run 1 must survive: the run may not reload
// the saved model just because the resumed prompt was unanswered. That requires
// journalling the answered-ness, not only the value.
func TestExplicitDefaultModelSurvivesAnAbortAndAnUnansweredResume(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	path, err := justcode.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	us, err := justcode.ReadUserSettings(justcode.DefaultFS, path)
	if err != nil {
		t.Fatal(err)
	}
	us.DefaultModel = "albert/old"
	if err := justcode.WriteUserSettings(justcode.DefaultFS, path, us); err != nil {
		t.Fatal(err)
	}

	// Run 1: key -> Enter (preserve the legacy custom model) -> skip GitHub ->
	// identities -> refuse at the recap.
	_ = captureStdout(t, func() {
		_, _ = setupWizardRun(true, bufio.NewReader(strings.NewReader("the-key\n\n\n\nTest User\ntest@example.test\nn\n")),
			func(context.Context, string) error { return nil },
			func(context.Context) error { return nil },
			func(string) string { return "" })
	})
	j, err := justcode.ReadSetupJournal(justcode.DefaultFS, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defaultModel := "albert/old"
	if !j.ModelChosen || j.DefaultModel != defaultModel || len(j.Models) != 3 || !containsString(j.Models, "old") {
		t.Fatalf("run 1 must journal the default model set: chosen=%v model=%q models=%v", j.ModelChosen, j.DefaultModel, j.Models)
	}

	// Run 2: resume. The key is already stored, so the MODEL prompt is the
	// first question, and the input makes it unanswered (EOF).
	_ = captureStdout(t, func() {
		_, _ = setupWizardRun(true, bufio.NewReader(strings.NewReader("")),
			func(context.Context, string) error { return nil },
			func(context.Context) error { return nil },
			func(string) string { return "" })
	})
	after, err := justcode.ReadUserSettings(justcode.DefaultFS, path)
	if err != nil {
		t.Fatal(err)
	}
	if after.DefaultModel != defaultModel {
		t.Fatalf("the journaled default choice must survive an unanswered resume; got %q", after.DefaultModel)
	}
}

// TestUnansweredModelPromptKeepsTheSavedModel pins the round-6 finding on #216:
// a completed install being reconfigured has no journal, so the prompt starts
// from the saved setting. If the input runs out (EOF, Ctrl-D) the prompt is
// unanswered, and the run must not clear a model the user never asked to change.
func TestUnansweredModelPromptKeepsTheSavedModel(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	// A model already saved by a previous successful setup.
	path, err := justcode.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	us, err := justcode.ReadUserSettings(justcode.DefaultFS, path)
	if err != nil {
		t.Fatal(err)
	}
	us.DefaultModel = "albert/keep-me"
	if err := justcode.WriteUserSettings(justcode.DefaultFS, path, us); err != nil {
		t.Fatal(err)
	}
	// Input runs out right after the key, so every later prompt is unanswered.
	input := "the-key\n"
	_ = captureStdout(t, func() {
		_, _ = setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
			func(context.Context, string) error { return nil },
			func(context.Context) error { return nil },
			func(string) string { return "" })
	})
	after, uerr := justcode.ReadUserSettings(justcode.DefaultFS, path)
	if uerr != nil {
		t.Fatal(uerr)
	}
	if after.DefaultModel != "albert/keep-me" {
		t.Fatalf("an unanswered model prompt must keep the saved model; got %q", after.DefaultModel)
	}
}

// TestEmptyModelAnswerPreservesLegacyCustomChoice covers old settings that
// contain a custom default but no explicit model list.
func TestEmptyModelAnswerPreservesLegacyCustomChoice(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	// Pre-journal a custom model, as an interrupted run would have left it.
	j, err := justcode.ReadSetupJournal(justcode.DefaultFS, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	j.DefaultModel = "albert/previous-choice"
	j.ModelChosen = true
	if err := justcode.WriteSetupJournal(justcode.DefaultFS, stateDir, j); err != nil {
		t.Fatal(err)
	}
	// key -> Enter (keep the current model) -> skip GitHub -> identities
	// -> apply.
	input := "the-key\n\n\nTest User\ntest@example.test\ny\n"
	_ = captureStdout(t, func() {
		code, rerr := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
			func(context.Context, string) error { return nil },
			func(context.Context) error { return nil },
			func(string) string { return "" })
		if code != 0 || rerr != nil {
			t.Fatalf("run: code=%d err=%v", code, rerr)
		}
	})
	defaultModel := "albert/previous-choice"
	path, perr := justcode.UserSettingsPath()
	if perr != nil {
		t.Fatal(perr)
	}
	us, uerr := justcode.ReadUserSettings(justcode.DefaultFS, path)
	if uerr != nil {
		t.Fatal(uerr)
	}
	if us.DefaultModel != defaultModel || len(us.Models) != 3 || !containsString(us.Models, "previous-choice") {
		t.Fatalf("Enter must preserve the legacy model and enable it; got model=%q models=%v", us.DefaultModel, us.Models)
	}
}

// TestModelChoiceSurvivesAbortAfterTheModelStage pins the round-4 finding on
// #216. Model selection was moved ahead of the GitHub and git-identity forms,
// so a user who aborts one of those must not lose a model they already chose:
// the journal has to hold it before those cancellation points.
func TestModelChoiceSurvivesAbortAfterTheModelStage(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	// key -> model -> skip GitHub (empty) -> default identity (two empties)
	// -> refuse at the recap, which aborts after the model stage.
	input := "the-key\nalbert/some-model\n\n\n\nn\n"
	_ = captureStdout(t, func() {
		code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
			func(context.Context, string) error { return nil },
			func(context.Context) error { return nil },
			func(string) string { return "" })
		if code == 0 {
			t.Errorf("an aborted run must not report success: code=%d err=%v", code, err)
		}
	})
	j, err := justcode.ReadSetupJournal(justcode.DefaultFS, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if j.DefaultModel != "albert/some-model" {
		t.Fatalf("the model chosen before the abort must be journaled; got %q", j.DefaultModel)
	}
	// The settings write must still be deferred to Apply.
	path, perr := justcode.UserSettingsPath()
	if perr == nil {
		us, rerr := justcode.ReadUserSettings(justcode.DefaultFS, path)
		if rerr == nil && us.DefaultModel == "albert/some-model" {
			t.Fatal("an aborted run must not write the model into user settings")
		}
	}
}

// TestSetupUnreachableTypoReasksInsteadOfExiting pins the round-3 finding on
// #216: a typo at the [R]/[C] prompt is not a decision and must not end the
// run — the wizard asks again.
func TestSetupUnreachableTypoReasksInsteadOfExiting(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	// key -> unreachable -> typo -> C (continue) -> model, no GitHub, defaults.
	input := "only-key\nz\nc\n\nn\nTest User\ntest@example.test\ny\n"
	out := captureStdout(t, func() {
		code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
			func(context.Context, string) error {
				return fmt.Errorf("could not reach the Albert endpoint: HTTP 503")
			},
			func(context.Context) error { return nil },
			func(string) string { return "" })
		if code != 0 || err != nil {
			t.Fatalf("a typo must not end the run: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(out, "Réponse non reconnue.") {
		t.Errorf("an unrecognized answer must be reported; output:\n%s", out)
	}
	if !strings.Contains(out, "Clé enregistrée sans vérification.") {
		t.Errorf("C must still continue after a typo; output:\n%s", out)
	}
}

// TestSetupUnreachableUnansweredPromptStoresNothing pins the second review
// finding on #216: an end of input at the [R]/[C] prompt is NOT an explicit
// "continue", so it must cancel rather than store an unvalidated key.
func TestSetupUnreachableUnansweredPromptStoresNothing(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	// key, then end of input at the [R]/[C] question.
	input := "only-key\n"
	code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
		func(context.Context, string) error {
			return fmt.Errorf("could not reach the Albert endpoint: HTTP 503")
		},
		func(context.Context) error { return nil },
		func(string) string { return "" })
	if code == 0 {
		t.Errorf("an unanswered retry prompt must not report success: code=%d err=%v", code, err)
	}
	if store, serr := justcode.ConsentFileStore(); serr == nil {
		if _, statErr := os.Stat(store.Path); statErr == nil {
			t.Fatal("an unanswered retry prompt must not store the key")
		}
	}
	j, rerr := justcode.ReadSetupJournal(justcode.DefaultFS, stateDir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if justcode.ContainsCredentialKind(j.CredentialKinds, "albert") {
		t.Fatal("an unanswered retry prompt must not journal albert as stored")
	}
}

// TestSetupUnreachableRetryThenAbandonStoresNothing is the regression test for
// the review finding on #216: choosing R then giving up must not leave the
// first, unvalidated key in the store — otherwise a later `setup` skips this
// screen and uses a key the user explicitly chose not to accept.
func TestSetupUnreachableRetryThenAbandonStoresNothing(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	// key -> unreachable -> R -> empty key (abandon).
	input := "first-key\nr\n\n"
	var negativeCalls int
	_ = captureStdout(t, func() {
		code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
			func(context.Context, string) error {
				negativeCalls++
				return fmt.Errorf("could not reach the Albert endpoint: HTTP 503")
			},
			func(context.Context) error { return nil },
			func(string) string { return "" })
		if code == 0 {
			t.Errorf("abandoning after R must not report success: code=%d err=%v", code, err)
		}
	})
	// The store must not exist: nothing was ever Put.
	if store, err := justcode.ConsentFileStore(); err == nil {
		if _, serr := os.Stat(store.Path); serr == nil {
			t.Fatal("a retry that was abandoned must not leave the key in the store")
		}
	}
	// And the journal must not claim the credential is stored.
	j, rerr := justcode.ReadSetupJournal(justcode.DefaultFS, stateDir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if justcode.ContainsCredentialKind(j.CredentialKinds, "albert") {
		t.Fatal("a retry that was abandoned must not journal albert as stored")
	}
	if negativeCalls == 0 {
		t.Fatal("precondition: the endpoint was never probed")
	}
}

// forceGitHubProbe pins the GitHub token probe for the test; the original is
// restored on cleanup. The probe records every token it is called with.
func forceGitHubProbe(t *testing.T, probe func(string) justcode.CredentialProbe) *[]string {
	t.Helper()
	calls := &[]string{}
	orig := githubTokenProbeFn
	githubTokenProbeFn = func(_ context.Context, token string) justcode.CredentialProbe {
		*calls = append(*calls, token)
		return probe(token)
	}
	t.Cleanup(func() { githubTokenProbeFn = orig })
	return calls
}

func storedGitHubToken(t *testing.T) (string, error) {
	t.Helper()
	store, err := justcode.ConsentFileStore()
	if err != nil {
		t.Fatal(err)
	}
	return store.Get(context.Background(), justcode.CredentialGithub)
}

// TestGitHubScreenText pins the #164 screen: what GitHub is for, what to do
// without it, which token to create, the two rights, and the store wording —
// which must not promise a keychain under --fallback.
func TestGitHubScreenText(t *testing.T) {
	native := githubScreenText(false)
	for _, want := range []string{
		"[3/3] GitHub (facultatif)",
		"just-code workspace apply",
		"jeton GitHub « fine-grained »",
		githubTokenURL,
		"Contents (lecture et écriture), Pull requests (lecture et écriture)",
		"elle peut devoir approuver le jeton",
		"enregistré dans le trousseau de ce poste",
	} {
		if !strings.Contains(native, want) {
			t.Errorf("screen is missing %q", want)
		}
	}
	fallback := githubScreenText(true)
	if strings.Contains(fallback, "trousseau") {
		t.Errorf("the fallback screen must not promise a keychain: %q", fallback)
	}
	if !strings.Contains(fallback, "lisible par votre seul compte") {
		t.Errorf("the fallback screen must describe the file store: %q", fallback)
	}
}

// TestSetupWizardGitHubDefaultsToLater pins the stage's safe default: an
// empty answer (and an EOF) is "Plus tard", nothing is probed or stored, and
// the remedy is printed.
func TestSetupWizardGitHubDefaultsToLater(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	calls := forceGitHubProbe(t, func(string) justcode.CredentialProbe {
		t.Fatal("the probe must not run when the user chooses Plus tard")
		return justcode.CredentialProbe{}
	})
	// key, model (empty = keep), GitHub (empty = Plus tard), name, email, apply.
	input := "the-key\n\n\nTest User\ntest@example.test\ny\n"
	out := captureStdout(t, func() {
		code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
			func(context.Context, string) error { return nil },
			func(context.Context) error { return nil },
			func(string) string { return "" })
		if code != 0 || err != nil {
			t.Errorf("wizard run: code=%d err=%v", code, err)
		}
	})
	if len(*calls) != 0 {
		t.Errorf("the probe ran %d times on a Plus tard answer", len(*calls))
	}
	if !strings.Contains(out, githubLaterMessage) {
		t.Errorf("the Plus tard remedy is missing; output:\n%s", out)
	}
	if _, err := storedGitHubToken(t); err == nil {
		t.Error("no token may be stored on a Plus tard answer")
	}
}

// TestSetupWizardGitHubLegacyNoAnswerKeepsLater pins that "n" — the answer
// the previous yes/no prompt took — still means "Plus tard".
func TestSetupWizardGitHubLegacyNoAnswerKeepsLater(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	forceGitHubProbe(t, func(string) justcode.CredentialProbe {
		t.Fatal("the probe must not run when the user answers n")
		return justcode.CredentialProbe{}
	})
	// key, model, GitHub ("n" = Plus tard), name, email, apply.
	input := "the-key\n\nn\nTest User\ntest@example.test\ny\n"
	out := captureStdout(t, func() {
		code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
			func(context.Context, string) error { return nil },
			func(context.Context) error { return nil },
			func(string) string { return "" })
		if code != 0 || err != nil {
			t.Errorf("wizard run: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(out, githubLaterMessage) {
		t.Errorf("the Plus tard remedy is missing; output:\n%s", out)
	}
}

// TestSetupWizardGitHubValidFineGrainedToken covers the happy path: the
// token is validated before it is stored, the account login is reported, and
// the token itself never appears in the output.
func TestSetupWizardGitHubValidFineGrainedToken(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	calls := forceGitHubProbe(t, func(string) justcode.CredentialProbe {
		return justcode.CredentialProbe{Login: "moncompte"}
	})
	// key, model, GitHub (a = add), token, name, email, apply.
	input := "the-key\n\na\ngithub_pat_secretvalue\nTest User\ntest@example.test\ny\n"
	out := captureStdout(t, func() {
		code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
			func(context.Context, string) error { return nil },
			func(context.Context) error { return nil },
			func(string) string { return "" })
		if code != 0 || err != nil {
			t.Errorf("wizard run: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(out, "✅ Jeton valide : compte moncompte.") {
		t.Errorf("the success line must name the account; output:\n%s", out)
	}
	if strings.Contains(out, "github_pat_secretvalue") {
		t.Errorf("the token must never be displayed; output:\n%s", out)
	}
	for _, want := range []string{"Clé Albert API      enregistrée dans un fichier protégé (vérifiée)", "GitHub              jeton vérifié, compte @moncompte", "téléchargement, installation, puis démarrage"} {
		if !strings.Contains(out, want) {
			t.Errorf("recap is missing %q; output:\n%s", want, out)
		}
	}
	if len(*calls) != 1 {
		t.Errorf("expected exactly one probe call, got %d", len(*calls))
	}
	stored, err := storedGitHubToken(t)
	if err != nil || stored != "github_pat_secretvalue" {
		t.Errorf("stored token = %q, %v", stored, err)
	}
}

// TestSetupWizardGitHubRefusedTokenReasks pins the refusal loop: a refused
// token explains and re-asks; only the second, valid token is stored.
func TestSetupWizardGitHubRefusedTokenReasks(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	forceGitHubProbe(t, func(token string) justcode.CredentialProbe {
		if token == "bad-token" {
			return justcode.CredentialProbe{Rejected: true, Detail: "HTTP 401"}
		}
		return justcode.CredentialProbe{Login: "moncompte"}
	})
	// key, model, add, refused token, valid token, name, email, apply.
	input := "the-key\n\na\nbad-token\ngithub_pat_good\nTest User\ntest@example.test\ny\n"
	out := captureStdout(t, func() {
		code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
			func(context.Context, string) error { return nil },
			func(context.Context) error { return nil },
			func(string) string { return "" })
		if code != 0 || err != nil {
			t.Errorf("wizard run: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(out, "GitHub a refusé ce jeton. Vérifiez qu'il est complet, ou créez-en un nouveau.") {
		t.Errorf("the refusal message is missing; output:\n%s", out)
	}
	stored, err := storedGitHubToken(t)
	if err != nil || stored != "github_pat_good" {
		t.Errorf("stored token = %q, %v", stored, err)
	}
	if strings.Contains(out, "bad-token") {
		t.Errorf("the refused token must never be displayed; output:\n%s", out)
	}
}

// TestSetupWizardGitHubClassicTokenWarning pins the broad-scope warning and
// its two outcomes: [R] re-enters a token, [C] stores the classic one. The
// message text is the issue's wording, [C]/[R] hint included.
func TestSetupWizardGitHubClassicTokenWarning(t *testing.T) {
	for _, tc := range []struct {
		name      string
		answer    string
		wantStore string
	}{
		{"continue stores the classic token", "c", "ghp_classic"},
		{"replace re-asks", "r", "github_pat_fine"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir, _ := setupTestEnv(t)
			forceNonTTY(t)
			journalPastPreflight(t, stateDir)
			forceGitHubProbe(t, func(token string) justcode.CredentialProbe {
				probe := justcode.CredentialProbe{Login: "moncompte"}
				if !strings.HasPrefix(token, "github_pat_") {
					probe.BroadScope = true
				}
				return probe
			})
			// key, model, add, classic token, warning answer, [replacement
			// token], name, email, apply.
			input := "the-key\n\na\nghp_classic\n" + tc.answer + "\ngithub_pat_fine\nTest User\ntest@example.test\ny\n"
			if tc.answer == "c" {
				// No replacement token is prompted for; the empty line is the
				// name answer.
				input = "the-key\n\na\nghp_classic\nc\nTest User\ntest@example.test\ny\n"
			}
			out := captureStdout(t, func() {
				code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
					func(context.Context, string) error { return nil },
					func(context.Context) error { return nil },
					func(string) string { return "" })
				if code != 0 || err != nil {
					t.Errorf("wizard run: code=%d err=%v", code, err)
				}
			})
			if !strings.Contains(out, githubClassicMessage) {
				t.Errorf("the classic-token warning is missing; output:\n%s", out)
			}
			stored, err := storedGitHubToken(t)
			if err != nil || stored != tc.wantStore {
				t.Errorf("stored token = %q, %v; want %q", stored, err, tc.wantStore)
			}
		})
	}
}

// TestSetupWizardGitHubUnreachableContinue stores the token unvalidated when
// the user explicitly continues; retry re-enters instead.
func TestSetupWizardGitHubUnreachable(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	forceGitHubProbe(t, func(token string) justcode.CredentialProbe {
		if token == "tok-offline" {
			return justcode.CredentialProbe{Unreachable: true, Detail: "dial tcp: connection refused"}
		}
		return justcode.CredentialProbe{Login: "moncompte"}
	})
	// key, model, add, unreachable token, R (retry), valid token, name, email, apply.
	input := "the-key\n\na\ntok-offline\nr\ngithub_pat_good\nTest User\ntest@example.test\ny\n"
	out := captureStdout(t, func() {
		code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
			func(context.Context, string) error { return nil },
			func(context.Context) error { return nil },
			func(string) string { return "" })
		if code != 0 || err != nil {
			t.Errorf("wizard run: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(out, githubUnreachableMessage) {
		t.Errorf("the unreachable message is missing; output:\n%s", out)
	}
	if stored, err := storedGitHubToken(t); err != nil || stored != "github_pat_good" {
		t.Errorf("a retry must re-enter the token: stored %q, %v", stored, err)
	}
}

// TestSetupWizardGitHubUnreachableContinueStoresUnvalidated pins that an
// explicit C stores the token and says it was not verified.
func TestSetupWizardGitHubUnreachableContinueStoresUnvalidated(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	journalPastPreflight(t, stateDir)
	forceGitHubProbe(t, func(string) justcode.CredentialProbe {
		return justcode.CredentialProbe{Unreachable: true, Detail: "dial tcp: connection refused"}
	})
	// key, model, add, unreachable token, C (continue), name, email, apply.
	input := "the-key\n\na\ntok-offline\nc\nTest User\ntest@example.test\ny\n"
	out := captureStdout(t, func() {
		code, err := setupWizardRun(true, bufio.NewReader(strings.NewReader(input)),
			func(context.Context, string) error { return nil },
			func(context.Context) error { return nil },
			func(string) string { return "" })
		if code != 0 || err != nil {
			t.Errorf("wizard run: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(out, "Jeton enregistré sans vérification.") {
		t.Errorf("the unverified-storage notice is missing; output:\n%s", out)
	}
	if stored, err := storedGitHubToken(t); err != nil || stored != "tok-offline" {
		t.Errorf("the token must be stored on an explicit continue: stored %q, %v", stored, err)
	}
}

func TestSetupPlatformStageRejectsACorrectedEndpointSharingAHost(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceNonTTY(t)
	previousPresent := setupCredentialPresentFn
	setupCredentialPresentFn = func(context.Context, justcode.CredentialKind) bool { return false }
	t.Cleanup(func() { setupCredentialPresentFn = previousPresent })
	fs := justcode.DefaultFS
	j := justcode.SetupJournal{
		SchemaVersion: 1, Stage: justcode.StageCredential,
		Platforms: []justcode.SetupPlatform{
			{Kind: "mistral"},
			{Kind: "openai-compatible", Name: "Other", BaseURL: "https://other.example.com/v1"},
		},
	}
	store := &fakeSetupStore{}
	w := justcode.SetupWizard{
		FS: fs, StateDir: stateDir,
		Store: func(context.Context) (justcode.SetupStore, error) { return store, nil },
		ValidateProvider: func(_ context.Context, provider justcode.InferenceProvider, _ string) justcode.CredentialProbe {
			if provider.Kind == justcode.ProviderOpenAICompatible && provider.BaseURL == "https://other.example.com/v1" {
				return justcode.CredentialProbe{Incompatible: true, Detail: "not a models listing"}
			}
			return justcode.CredentialProbe{}
		},
	}
	input := "mistral-key\nk1\nOther\nhttps://api.mistral.ai/v1\nk2\n"
	_, err := setupPlatformStage(context.Background(), w, bufio.NewReader(strings.NewReader(input)), true, &j, fs, stateDir)
	if err == nil || !strings.Contains(err.Error(), "share the host") {
		t.Fatalf("a corrected endpoint sharing a host must be refused: %v", err)
	}
}
