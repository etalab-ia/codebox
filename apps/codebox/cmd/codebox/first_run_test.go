package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
)

func stubCombinedRunEnvironment(t *testing.T, completed bool) (stateDir string, forms *[]string, initOpts *initOptions) {
	t.Helper()
	stateDir, _ = setupTestEnv(t)
	rootCompleted := setupCompletedFn
	setupCompletedFn = func() (bool, error) { return completed, nil }
	preflight := setupPreflightFn
	setupPreflightFn = func(context.Context) codebox.SetupPreflight {
		return codebox.SetupPreflight{Platform: "test/arch", VirtualizationOK: true}
	}
	credentialPresent := setupCredentialPresentFn
	setupCredentialPresentFn = func(context.Context, codebox.CredentialKind) bool { return true }
	identity := hostGitIdentityFn
	hostGitIdentityFn = func() (string, string) { return "Test User", "test@example.test" }
	wizard := wizardHuhFn
	wizardHuhFn = func() bool { return true }
	run := huhRun
	forms = &[]string{}
	huhRun = func(form *huh.Form) error {
		form.Init()
		_, _ = form.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		*forms = append(*forms, ansi.Strip(form.View()))
		return nil
	}
	initRun := combinedFirstRunInitFn
	initOpts = &initOptions{}
	combinedFirstRunInitFn = func(opts initOptions, _ *bufio.Reader, _ bool) (int, error) {
		*initOpts = opts
		return 0, nil
	}
	t.Cleanup(func() {
		setupCompletedFn = rootCompleted
		setupPreflightFn = preflight
		setupCredentialPresentFn = credentialPresent
		hostGitIdentityFn = identity
		wizardHuhFn = wizard
		huhRun = run
		combinedFirstRunInitFn = initRun
	})
	return stateDir, forms, initOpts
}

func TestCombinedFirstRunFreshMachineUsesSixScreenContract(t *testing.T) {
	root := initTestProject(t)
	stateDir, forms, initOpts := stubCombinedRunEnvironment(t, false)
	choose := firstRunChoosePlatformsFn
	providerTitle := ""
	firstRunChoosePlatformsFn = func(current []codebox.SetupPlatform, _ *bufio.Reader, title string) ([]codebox.SetupPlatform, error) {
		providerTitle = title
		return current, nil
	}
	t.Cleanup(func() { firstRunChoosePlatformsFn = choose })
	code, err := combinedFirstRun(initOptions{Root: root, Set: map[string]bool{"root": true}}, bufio.NewReader(strings.NewReader("")))
	if err != nil || code != 0 {
		t.Fatalf("combined first run = %d, %v", code, err)
	}
	if providerTitle != "[2/6] Fournisseur et clé" {
		t.Fatalf("provider title = %q", providerTitle)
	}
	if len(*forms) != 1 || !strings.Contains((*forms)[0], "[1/6] Bienvenue dans codebox") {
		t.Fatalf("welcome forms = %v", *forms)
	}
	if !initOpts.Combined || !initOpts.Set["providers"] || len(initOpts.Providers) != 1 || initOpts.Providers[0].Kind != codebox.ProviderAlbert {
		t.Fatalf("init options = %+v", *initOpts)
	}
	if _, err := os.Stat(codebox.SetupJournalPath(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("completed combined run must remove the setup journal: %v", err)
	}
	settingsPath, err := codebox.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	us, err := codebox.ReadUserSettings(codebox.DefaultFS, settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(us.Providers) != 1 || us.Providers[0].Kind != codebox.ProviderAlbert || us.GitName != "Test User" {
		t.Fatalf("machine settings = %+v", us)
	}
	complete, err := codebox.HasCompletedSetup(codebox.DefaultFS, stateDir, settingsPath)
	if err != nil || !complete {
		t.Fatalf("combined run must leave a completed machine setup: complete=%v err=%v", complete, err)
	}
}

func TestCombinedFirstRunProjectChoicePreservesOtherMachineProviders(t *testing.T) {
	root := initTestProject(t)
	_, _, initOpts := stubCombinedRunEnvironment(t, true)
	settingsPath, err := codebox.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	original := codebox.UserSettings{
		SchemaVersion: 2,
		DefaultModel:  "mistral/mistral-large-latest",
		Models:        []string{"mistral/mistral-large-latest"},
		Providers: []codebox.ProviderSettings{
			{Kind: codebox.ProviderAlbert},
			{Kind: codebox.ProviderMistral, CredentialRef: "mistral:api.mistral.ai"},
		},
	}
	if err := codebox.WriteUserSettings(codebox.DefaultFS, settingsPath, original); err != nil {
		t.Fatal(err)
	}
	choose := firstRunChoosePlatformsFn
	firstRunChoosePlatformsFn = func(current []codebox.SetupPlatform, _ *bufio.Reader, _ string) ([]codebox.SetupPlatform, error) {
		for _, platform := range current {
			if platform.Kind == string(codebox.ProviderAlbert) {
				return []codebox.SetupPlatform{platform}, nil
			}
		}
		t.Fatal("Albert provider missing from defaults")
		return nil, nil
	}
	t.Cleanup(func() { firstRunChoosePlatformsFn = choose })
	code, err := combinedFirstRun(initOptions{Root: root, Set: map[string]bool{"root": true}}, bufio.NewReader(strings.NewReader("")))
	if err != nil || code != 0 {
		t.Fatalf("combined returning run = %d, %v", code, err)
	}
	if len(initOpts.Providers) != 1 || initOpts.Providers[0].Kind != codebox.ProviderAlbert {
		t.Fatalf("project providers = %+v", initOpts.Providers)
	}
	us, err := codebox.ReadUserSettings(codebox.DefaultFS, settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(us.Providers) != 2 || us.DefaultModel != original.DefaultModel || len(us.Models) != 1 {
		t.Fatalf("project choice overwrote machine defaults: %+v", us)
	}
}

func TestCombinedFirstRunMistralResolvesUsableProjectModel(t *testing.T) {
	root := initTestProject(t)
	stateDir, _, initOpts := stubCombinedRunEnvironment(t, false)
	j, err := codebox.ReadSetupJournal(codebox.DefaultFS, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	// A host-bound reference is enough for the existing stage to reuse a saved
	// key. No credential value or real host keychain is involved in this test.
	j.CredentialKinds = []string{"mistral:api.mistral.ai"}
	if err := codebox.WriteSetupJournal(codebox.DefaultFS, stateDir, j); err != nil {
		t.Fatal(err)
	}
	catalogue := setupProviderCatalogueFn
	setupProviderCatalogueFn = func(provider codebox.InferenceProvider) ([]codebox.CatalogueModel, error) {
		if provider.Kind != codebox.ProviderMistral {
			t.Fatalf("unexpected catalogue provider: %s", provider.Kind)
		}
		return []codebox.CatalogueModel{{ID: "mistral-small-latest"}, {ID: "mistral-large-latest"}}, nil
	}
	t.Cleanup(func() { setupProviderCatalogueFn = catalogue })
	choose := firstRunChoosePlatformsFn
	firstRunChoosePlatformsFn = func([]codebox.SetupPlatform, *bufio.Reader, string) ([]codebox.SetupPlatform, error) {
		return []codebox.SetupPlatform{{Kind: string(codebox.ProviderMistral), CredentialRef: "mistral:api.mistral.ai"}}, nil
	}
	t.Cleanup(func() { firstRunChoosePlatformsFn = choose })
	code, err := combinedFirstRun(initOptions{Root: root, Set: map[string]bool{"root": true}}, bufio.NewReader(strings.NewReader("")))
	if err != nil || code != 0 {
		t.Fatalf("Mistral-only first run = %d, %v", code, err)
	}
	if initOpts.DefaultModel != "mistral/mistral-large-latest" {
		t.Fatalf("project default model = %q", initOpts.DefaultModel)
	}
	providers, err := codebox.ResolveProviders(initOpts.Providers)
	if err != nil {
		t.Fatal(err)
	}
	overlays := resolveProviderOverlays(providers, initOpts.DefaultModel)
	if len(overlays) != 1 || overlays[0].Kind != string(codebox.ProviderMistral) || len(overlays[0].Models) != 1 || overlays[0].Models[0].ID != "mistral-large-latest" {
		t.Fatalf("launch provider overlays = %+v", overlays)
	}
	effective := (codebox.ManagedOverlay{Providers: overlays}).EffectiveOverlay()
	if effective.Model != "mistral/mistral-large-latest" {
		t.Fatalf("effective launch model = %q", effective.Model)
	}
	settingsPath, err := codebox.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	us, err := codebox.ReadUserSettings(codebox.DefaultFS, settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if us.DefaultModel != "" || len(us.Models) != 0 {
		t.Fatalf("project default leaked into machine model defaults: %+v", us)
	}
}

func TestFirstRunDefaultModelUsesProviderCatalogue(t *testing.T) {
	setupTestEnv(t)
	catalogue := setupProviderCatalogueFn
	t.Cleanup(func() { setupProviderCatalogueFn = catalogue })
	for _, tc := range []struct {
		name     string
		kind     codebox.ProviderKind
		baseURL  string
		models   []codebox.CatalogueModel
		expected string
	}{
		{name: "Mistral preferred default", kind: codebox.ProviderMistral, models: []codebox.CatalogueModel{{ID: "mistral-small-latest"}, {ID: "mistral-large-latest"}}, expected: "mistral/mistral-large-latest"},
		{name: "OpenAI-compatible first catalogue model", kind: codebox.ProviderOpenAICompatible, baseURL: "https://openrouter.ai/api/v1", models: []codebox.CatalogueModel{{ID: "example/chat-model"}}, expected: "openai-compatible/example/chat-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupProviderCatalogueFn = func(codebox.InferenceProvider) ([]codebox.CatalogueModel, error) { return tc.models, nil }
			provider, err := codebox.ResolveProvider(codebox.ProviderSettings{Kind: tc.kind, Name: "Test", BaseURL: tc.baseURL})
			if err != nil {
				t.Fatal(err)
			}
			got, err := firstRunDefaultModel(t.TempDir(), []codebox.InferenceProvider{provider})
			if err != nil || got != tc.expected {
				t.Fatalf("default model = %q, %v; want %q", got, err, tc.expected)
			}
		})
	}
}

func TestSeedInitAnswersReconcilesModelWithSelectedProvider(t *testing.T) {
	setupTestEnv(t)
	for _, tc := range []struct {
		name      string
		inherited string
		expected  string
	}{
		{name: "incompatible Albert model replaced", inherited: "albert/old-model", expected: "mistral/mistral-large-latest"},
		{name: "compatible Mistral model preserved", inherited: "mistral/mistral-small-latest", expected: "mistral/mistral-small-latest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := initTestProject(t)
			if err := codebox.WriteProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root), codebox.ProjectManifest{SchemaVersion: 8, Model: tc.inherited}); err != nil {
				t.Fatal(err)
			}
			opts := initOptions{
				Root: root, DefaultModel: "mistral/mistral-large-latest",
				Providers: []codebox.ProviderSettings{{Kind: codebox.ProviderMistral}},
				Set:       map[string]bool{"root": true, "providers": true},
			}
			answers := seedInitAnswersFromManifest(opts, codebox.InitAnswers{Root: root})
			if answers.Model != tc.expected {
				t.Fatalf("model = %q, want %q", answers.Model, tc.expected)
			}
		})
	}
}

func TestInitRunPiUsesCompatibleFallbackForAutomaticDefault(t *testing.T) {
	setupTestEnv(t)
	root := initTestProject(t)
	stubInitSkillResolver(t)
	stubCatalogueCheck(t, func(string, string) (string, error) {
		t.Fatal("an automatic Pi-incompatible default must be replaced before catalogue validation")
		return "", nil
	})
	opts := initOptions{
		Root: root, Harness: string(codebox.HarnessPi), DefaultModel: "albert/qwen3-coder",
		Providers: []codebox.ProviderSettings{{Kind: codebox.ProviderAlbert}},
		Set: map[string]bool{
			"root": true, "harness": true, "runtime": true, "skills": true, "skills-storage": true, "mcps": true, "github": true,
		},
	}
	code, err := initRun(opts, bufio.NewReader(strings.NewReader("")), true)
	if err != nil || code != 0 {
		t.Fatalf("Pi initialization with automatic Albert default = %d, %v", code, err)
	}
	manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Model != "albert/deepseek-v4-flash" {
		t.Fatalf("Pi fallback model = %q, want albert/deepseek-v4-flash", manifest.Model)
	}
}

func TestCombinedFirstRunExplicitModelSkipsCatalogue(t *testing.T) {
	root := initTestProject(t)
	stateDir, _, initOpts := stubCombinedRunEnvironment(t, false)
	j, err := codebox.ReadSetupJournal(codebox.DefaultFS, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	j.CredentialKinds = []string{"mistral:api.mistral.ai"}
	if err := codebox.WriteSetupJournal(codebox.DefaultFS, stateDir, j); err != nil {
		t.Fatal(err)
	}
	catalogue := setupProviderCatalogueFn
	setupProviderCatalogueFn = func(codebox.InferenceProvider) ([]codebox.CatalogueModel, error) {
		t.Fatal("an explicit model must not require a catalogue request")
		return nil, errors.New("catalogue unavailable")
	}
	t.Cleanup(func() { setupProviderCatalogueFn = catalogue })
	choose := firstRunChoosePlatformsFn
	firstRunChoosePlatformsFn = func([]codebox.SetupPlatform, *bufio.Reader, string) ([]codebox.SetupPlatform, error) {
		return []codebox.SetupPlatform{{Kind: string(codebox.ProviderMistral), CredentialRef: "mistral:api.mistral.ai"}}, nil
	}
	t.Cleanup(func() { firstRunChoosePlatformsFn = choose })
	code, err := combinedFirstRun(initOptions{
		Root: root, Model: "mistral/custom-model",
		Providers: []codebox.ProviderSettings{{Kind: codebox.ProviderMistral}},
		Set:       map[string]bool{"root": true, "model": true, "providers": true},
	}, bufio.NewReader(strings.NewReader("")))
	if err != nil || code != 0 {
		t.Fatalf("explicit-model first run = %d, %v", code, err)
	}
	if initOpts.Model != "mistral/custom-model" || initOpts.DefaultModel != "" {
		t.Fatalf("explicit model answers = %+v", *initOpts)
	}
}

func TestCombinedFirstRunAdvancedRecoversFromCatalogueOutage(t *testing.T) {
	root := initTestProject(t)
	stateDir, _, initOpts := stubCombinedRunEnvironment(t, false)
	j, err := codebox.ReadSetupJournal(codebox.DefaultFS, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	j.CredentialKinds = []string{"mistral:api.mistral.ai"}
	if err := codebox.WriteSetupJournal(codebox.DefaultFS, stateDir, j); err != nil {
		t.Fatal(err)
	}
	catalogue := setupProviderCatalogueFn
	setupProviderCatalogueFn = func(codebox.InferenceProvider) ([]codebox.CatalogueModel, error) {
		return nil, errors.New("catalogue unavailable")
	}
	t.Cleanup(func() { setupProviderCatalogueFn = catalogue })
	choose := firstRunChoosePlatformsFn
	firstRunChoosePlatformsFn = func([]codebox.SetupPlatform, *bufio.Reader, string) ([]codebox.SetupPlatform, error) {
		return []codebox.SetupPlatform{{Kind: string(codebox.ProviderMistral), CredentialRef: "mistral:api.mistral.ai"}}, nil
	}
	t.Cleanup(func() { firstRunChoosePlatformsFn = choose })
	code, err := combinedFirstRun(initOptions{
		Root: root, Advanced: true,
		Providers: []codebox.ProviderSettings{{Kind: codebox.ProviderMistral}},
		Set:       map[string]bool{"root": true, "providers": true},
	}, bufio.NewReader(strings.NewReader("")))
	if err != nil || code != 0 {
		t.Fatalf("advanced catalogue-outage recovery = %d, %v", code, err)
	}
	if !initOpts.Advanced || initOpts.DefaultModel != "" {
		t.Fatalf("advanced recovery must open with a manual model answer: %+v", *initOpts)
	}
}

func TestCatalogueWarningDoesNotValidateMistralAgainstAlbert(t *testing.T) {
	original := catalogueModelAPIKeyFn
	catalogueModelAPIKeyFn = func(string) (string, error) {
		t.Fatal("a Mistral model must not trigger an Albert credential lookup")
		return "", nil
	}
	t.Cleanup(func() { catalogueModelAPIKeyFn = original })
	warning, err := catalogueModelWarningForRoot(t.TempDir(), "mistral/mistral-large-latest")
	if err != nil || warning != "" {
		t.Fatalf("Mistral validation = %q, %v", warning, err)
	}
}

func TestCombinedFirstRunInterruptedProjectKeepsRecoveryJournal(t *testing.T) {
	root := initTestProject(t)
	stateDir, _, _ := stubCombinedRunEnvironment(t, false)
	choose := firstRunChoosePlatformsFn
	firstRunChoosePlatformsFn = func(current []codebox.SetupPlatform, _ *bufio.Reader, _ string) ([]codebox.SetupPlatform, error) {
		return current, nil
	}
	t.Cleanup(func() { firstRunChoosePlatformsFn = choose })
	interrupted := errors.New("interrupted")
	combinedFirstRunInitFn = func(initOptions, *bufio.Reader, bool) (int, error) { return 1, interrupted }
	code, err := combinedFirstRun(initOptions{Root: root, Set: map[string]bool{"root": true}}, bufio.NewReader(strings.NewReader("")))
	if code != 1 || !errors.Is(err, interrupted) {
		t.Fatalf("interrupted run = %d, %v", code, err)
	}
	j, readErr := codebox.ReadSetupJournal(codebox.DefaultFS, stateDir)
	if readErr != nil {
		t.Fatalf("recovery journal missing: %v", readErr)
	}
	if len(j.Platforms) != 1 || j.Platforms[0].Kind != string(codebox.ProviderAlbert) {
		t.Fatalf("journal platforms = %+v", j.Platforms)
	}
}

func TestCombinedFirstRunRejectsScriptedAnswersWithoutFlags(t *testing.T) {
	setupTestEnv(t)
	wizard := wizardHuhFn
	wizardHuhFn = func() bool { return false }
	t.Cleanup(func() { wizardHuhFn = wizard })
	code, err := combinedFirstRun(initOptions{Set: map[string]bool{}}, bufio.NewReader(strings.NewReader("\n")))
	if code != 1 || err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("scripted first run = %d, %v", code, err)
	}
}

func TestCombinedHuhDefaultsAreEmptyAndNumbered(t *testing.T) {
	root := initTestProject(t)
	catalogue := projectSkillCatalogueFn
	projectSkillCatalogueFn = func(context.Context) ([]codebox.ProjectSkill, error) {
		return initSkillFixtureCatalogue(), nil
	}
	run := huhRun
	probe := githubInitProbeFn
	githubInitProbeFn = func(string) codebox.GitHubProjectStatus { return codebox.GitHubProjectStatus{HasOrigin: false} }
	var views []string
	huhRun = func(form *huh.Form) error {
		form.Init()
		_, _ = form.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		views = append(views, ansi.Strip(form.View()))
		return nil
	}
	t.Cleanup(func() {
		projectSkillCatalogueFn = catalogue
		githubInitProbeFn = probe
		huhRun = run
	})
	opts := initOptions{Combined: true, Set: map[string]bool{"root": true}, hostCPUs: 8, hostMemoryMB: 16384, maxCPUs: 8, maxMemoryMB: 8192}
	got, err := askInitQuestionsHuh(opts, codebox.InitAnswers{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Skills) != 0 || len(got.MCPConnectors) != 0 || got.GitHubWorkflow {
		t.Fatalf("combined defaults must be empty with GitHub skipped: %+v", got)
	}
	if len(views) != 4 {
		t.Fatalf("combined project forms = %d, want harness, runtime, skills and MCP", len(views))
	}
	for _, tc := range []struct {
		got  string
		want string
	}{
		{got: initHarnessStepTitle(opts), want: "[3/6] Agent (harnais)"},
		{got: initVMStepTitle(opts), want: "[4/6] Machine isolée"},
		{got: initSkillsStepTitle(opts), want: "[5/6] "},
		{got: initMCPStepTitle(opts), want: "[6/6] MCP (facultatif)"},
	} {
		if tc.got != tc.want {
			t.Errorf("combined title = %q, want %q", tc.got, tc.want)
		}
	}
	for _, view := range views {
		if strings.Contains(view, "Où enregistrer les skills ?") || strings.Contains(view, "Aucune skill sélectionnée") || strings.Contains(view, "Aucun MCP sélectionné") || strings.Contains(view, "GitHub") {
			t.Fatalf("combined flow contains advanced or confirmation screen:\n%s", view)
		}
	}
}

func TestFirstRunProviderDefaultsValidatesManifestEndpointBeforeCredentialProbe(t *testing.T) {
	setupTestEnv(t)
	root := initTestProject(t)
	manifest := codebox.ProjectManifest{
		SchemaVersion: 8,
		Providers: []codebox.ProviderSettings{{
			Kind: codebox.ProviderOpenAICompatible, Name: "Attacker", BaseURL: "https://attacker.example/v1",
		}},
	}
	if err := codebox.WriteProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root), manifest); err != nil {
		t.Fatal(err)
	}
	present := setupCredentialPresentFn
	probeCalls := 0
	setupCredentialPresentFn = func(context.Context, codebox.CredentialKind) bool {
		probeCalls++
		return true
	}
	t.Cleanup(func() { setupCredentialPresentFn = present })
	j := codebox.SetupJournal{Stage: codebox.StageCredential}
	if _, err := firstRunProviderDefaults(context.Background(), root, &j); err == nil || !strings.Contains(err.Error(), "attacker.example") {
		t.Fatalf("unapproved manifest endpoint was not rejected: %v", err)
	}
	if probeCalls != 0 {
		t.Fatalf("credential presence was probed before endpoint approval: %d", probeCalls)
	}

	manifest.Providers[0].CredentialRef = "openai-compatible:attacker.example"
	if err := codebox.WriteProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root), manifest); err != nil {
		t.Fatal(err)
	}
	platforms, err := firstRunProviderDefaults(context.Background(), root, &j)
	if err != nil {
		t.Fatalf("explicit host-bound provider was not accepted: %v", err)
	}
	if len(platforms) != 1 || platforms[0].CredentialRef != "openai-compatible:attacker.example" || probeCalls != 1 {
		t.Fatalf("host-bound defaults = %+v, probes = %d", platforms, probeCalls)
	}
}

func TestFirstRunProviderDefaultsPreferExistingProject(t *testing.T) {
	setupTestEnv(t)
	root := initTestProject(t)
	if err := codebox.WriteProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root), codebox.ProjectManifest{
		SchemaVersion: 8,
		Providers:     []codebox.ProviderSettings{{Kind: codebox.ProviderMistral, CredentialRef: "mistral:api.mistral.ai"}},
	}); err != nil {
		t.Fatal(err)
	}
	settingsPath, err := codebox.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := codebox.WriteUserSettings(codebox.DefaultFS, settingsPath, codebox.UserSettings{
		SchemaVersion: 2,
		Providers:     []codebox.ProviderSettings{{Kind: codebox.ProviderAlbert}},
	}); err != nil {
		t.Fatal(err)
	}
	present := setupCredentialPresentFn
	setupCredentialPresentFn = func(context.Context, codebox.CredentialKind) bool { return true }
	t.Cleanup(func() { setupCredentialPresentFn = present })
	j := codebox.SetupJournal{Stage: codebox.StageCredential}
	platforms, err := firstRunProviderDefaults(context.Background(), root, &j)
	if err != nil {
		t.Fatal(err)
	}
	if len(platforms) != 1 || platforms[0].Kind != string(codebox.ProviderMistral) || platforms[0].CredentialRef != "mistral:api.mistral.ai" {
		t.Fatalf("existing project provider defaults = %+v", platforms)
	}
}

func TestProviderFlagSelectsMachineConnectionForProject(t *testing.T) {
	setupTestEnv(t)
	settingsPath, err := codebox.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := codebox.WriteUserSettings(codebox.DefaultFS, settingsPath, codebox.UserSettings{
		SchemaVersion: 2,
		Providers: []codebox.ProviderSettings{
			{Kind: codebox.ProviderAlbert},
			{Kind: codebox.ProviderMistral, CredentialRef: "mistral:api.mistral.ai"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	opts, err := parseInitArgs([]string{"--root", t.TempDir(), "--provider", "mistral", "--yes"})
	if err != nil {
		t.Fatal(err)
	}
	answers, err := resolveInitProviders(opts, codebox.InitAnswers{Root: opts.Root, Providers: append([]codebox.ProviderSettings(nil), opts.Providers...)})
	if err != nil {
		t.Fatal(err)
	}
	if len(answers.Providers) != 1 || answers.Providers[0].Kind != codebox.ProviderMistral || answers.Providers[0].CredentialRef != "mistral:api.mistral.ai" {
		t.Fatalf("providers = %+v", answers.Providers)
	}
	if _, err := parseInitArgs([]string{"--provider", "local"}); err == nil {
		t.Fatal("a local main-model provider must be rejected by --provider")
	}
}

func TestInitCmdNonTTYRequiresExplicitApply(t *testing.T) {
	setupTestEnv(t)
	tty := initTTYFn
	initTTYFn = func() bool { return false }
	t.Cleanup(func() { initTTYFn = tty })
	root := initTestProject(t)
	code, err := initCmd([]string{"--root", root})
	if code != 1 || err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("non-TTY init = %d, %v", code, err)
	}
	if _, statErr := os.Stat(codebox.ProjectManifestPath(root)); !os.IsNotExist(statErr) {
		t.Fatalf("non-TTY init without --yes must not write: %v", statErr)
	}
}

func TestAdvancedSettingsOwnSkillStorage(t *testing.T) {
	originalRun := runInitAdvancedForm
	t.Cleanup(func() { runInitAdvancedForm = originalRun })
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
			field.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			field.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		case 3:
			return true, nil
		default:
			t.Fatal("advanced settings reopened after skill storage was selected")
		}
		return false, nil
	}
	answers, err := askInitAdvancedHuh(initOptions{hostCPUs: 8, hostMemoryMB: 16384, maxCPUs: 8, maxMemoryMB: 16384}, codebox.InitAnswers{Root: "/tmp/example"})
	if err != nil {
		t.Fatal(err)
	}
	if !answers.SkillsLocalOnly || !answers.SkillsLocalOnlySet {
		t.Fatalf("skill storage was not applied from advanced settings: %+v", answers)
	}
}

func TestInitRuntimeOptionsFilterUnsupportedPlatforms(t *testing.T) {
	for _, tc := range []struct {
		goos       string
		wantTart   bool
		wantAgent  bool
		wantLength int
	}{
		{goos: "darwin", wantTart: true, wantAgent: true, wantLength: 3},
		{goos: "linux", wantAgent: true, wantLength: 2},
		{goos: "windows", wantLength: 1},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			options := initRuntimeOptionsForOS(tc.goos)
			if len(options) != tc.wantLength {
				t.Fatalf("options = %+v", options)
			}
			var tart, agent bool
			for _, option := range options {
				tart = tart || option.Value == string(codebox.RuntimeTart)
				agent = agent || option.Value == string(codebox.RuntimeAgentVM)
				if option.Value != string(codebox.RuntimeMicrosandbox) && !strings.Contains(option.Key, "expérimental") {
					t.Errorf("non-default runtime must be experimental: %+v", option)
				}
			}
			if tart != tc.wantTart || agent != tc.wantAgent {
				t.Fatalf("tart=%v agent=%v, want %v/%v", tart, agent, tc.wantTart, tc.wantAgent)
			}
		})
	}
}
