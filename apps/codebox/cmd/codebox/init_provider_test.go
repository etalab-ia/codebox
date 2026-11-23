package main

import (
	"bufio"
	"context"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
)

func TestInitRuntimeOptionsFilteredByOS(t *testing.T) {
	cases := []struct {
		goos        string
		wantValues  []string
		wantLabels  []string
		forbidValue []string
	}{
		{
			goos:       "darwin",
			wantValues: []string{"microsandbox", "tart", "agent-vm"},
			wantLabels: []string{"Microsandbox (recommandé)", "Tart (expérimental)", "agent-vm (expérimental)"},
		},
		{
			goos:        "linux",
			wantValues:  []string{"microsandbox", "agent-vm"},
			wantLabels:  []string{"Microsandbox (recommandé)", "agent-vm (expérimental)"},
			forbidValue: []string{"tart"},
		},
		{
			goos:        "windows",
			wantValues:  []string{"microsandbox"},
			wantLabels:  []string{"Microsandbox (recommandé)"},
			forbidValue: []string{"tart", "agent-vm"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			opts := initRuntimeOptionsForOS(tc.goos)
			if len(opts) != len(tc.wantValues) {
				t.Fatalf("got %d options, want %d", len(opts), len(tc.wantValues))
			}
			for i, wantVal := range tc.wantValues {
				if opts[i].Value != wantVal {
					t.Errorf("option[%d].Value = %q, want %q", i, opts[i].Value, wantVal)
				}
				if !strings.Contains(opts[i].Key, tc.wantLabels[i]) {
					t.Errorf("option[%d].Key = %q, want substring %q", i, opts[i].Key, tc.wantLabels[i])
				}
			}
			for _, forbidden := range tc.forbidValue {
				for _, opt := range opts {
					if opt.Value == forbidden {
						t.Errorf("OS %s must not offer runtime %q", tc.goos, forbidden)
					}
				}
			}
		})
	}
}

func TestEligibleSavedProviderEnforcesProviderAndHostBinding(t *testing.T) {
	origPresent := initProviderCredentialPresentFn
	t.Cleanup(func() { initProviderCredentialPresentFn = origPresent })

	openRouter, err := codebox.ResolveProvider(codebox.ProviderSettings{
		Kind:    codebox.ProviderOpenAICompatible,
		Name:    "OpenRouter",
		BaseURL: "https://openrouter.ai/api/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	otherHost, err := codebox.ResolveProvider(codebox.ProviderSettings{
		Kind:    codebox.ProviderOpenAICompatible,
		Name:    "Other",
		BaseURL: "https://api.other.example/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	mistral, err := codebox.ResolveProvider(codebox.ProviderSettings{Kind: codebox.ProviderMistral})
	if err != nil {
		t.Fatal(err)
	}

	presentRefs := map[string]bool{
		"albert": true,
		codebox.ProviderCredentialRef(openRouter): true,
	}
	initProviderCredentialPresentFn = func(_ context.Context, p codebox.InferenceProvider, ref string) bool {
		if p.Kind == codebox.ProviderAlbert && ref == "" {
			return presentRefs["albert"]
		}
		return presentRefs[ref]
	}

	saved := []codebox.ProviderSettings{
		{Kind: codebox.ProviderAlbert, Name: "Albert API"},
		{
			Kind:          codebox.ProviderOpenAICompatible,
			Name:          "OpenRouter",
			BaseURL:       "https://openrouter.ai/api/v1",
			CredentialRef: codebox.ProviderCredentialRef(openRouter),
		},
	}

	// Exact provider + host match succeeds.
	got, ok := eligibleSavedProvider(context.Background(), openRouter, saved)
	if !ok || got.CredentialRef != codebox.ProviderCredentialRef(openRouter) {
		t.Fatalf("expected OpenRouter saved key to be eligible, got ok=%v setting=%+v", ok, got)
	}

	// Different host must NEVER be offered OpenRouter's or Albert's key.
	if got, ok := eligibleSavedProvider(context.Background(), otherHost, saved); ok {
		t.Fatalf("wrong-host provider must not reuse saved key, got %+v", got)
	}

	// Different provider kind must NEVER be offered Albert's key.
	if got, ok := eligibleSavedProvider(context.Background(), mistral, saved); ok {
		t.Fatalf("Mistral must not reuse Albert's saved key, got %+v", got)
	}

	// Missing secret in machine store must not be offered.
	presentRefs[codebox.ProviderCredentialRef(openRouter)] = false
	if got, ok := eligibleSavedProvider(context.Background(), openRouter, saved); ok {
		t.Fatalf("missing credential in store must not be offered, got %+v", got)
	}
}

func TestInitSetupNeededRecognizesCompletedNonAlbertSetup(t *testing.T) {
	setupTestEnv(t)
	t.Setenv("ALBERT_API_KEY", "")

	origCheck := initAlbertCredentialCheckFn
	initAlbertCredentialCheckFn = func() error { return codebox.ErrCredentialNotFound }
	t.Cleanup(func() { initAlbertCredentialCheckFn = origCheck })

	// Before machine setup completes, setup is needed.
	needed, err := initSetupNeeded()
	if err != nil || !needed {
		t.Fatalf("fresh machine: initSetupNeeded = %v, %v; want true, nil", needed, err)
	}

	// Completing setup with Mistral (no Albert key) satisfies initSetupNeeded.
	settingsPath, err := codebox.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	mistral, err := codebox.ResolveProvider(codebox.ProviderSettings{Kind: codebox.ProviderMistral})
	if err != nil {
		t.Fatal(err)
	}
	if err := codebox.WriteUserSettings(codebox.DefaultFS, settingsPath, codebox.UserSettings{
		SchemaVersion: 1,
		Providers:     []codebox.ProviderSettings{{Kind: codebox.ProviderMistral, CredentialRef: codebox.ProviderCredentialRef(mistral)}},
	}); err != nil {
		t.Fatal(err)
	}
	needed, err = initSetupNeeded()
	if err != nil || needed {
		t.Fatalf("completed Mistral setup: initSetupNeeded = %v, %v; want false, nil", needed, err)
	}
}

func TestCombinedFirstRunFreshMachineAndProject(t *testing.T) {
	clearLaunchChoiceEnv(t)
	setupTestEnv(t)
	stubCatalogueCheck(t, func(string, string) (string, error) { return "", nil })
	stubProjectSkills(t)

	root := initTestProject(t)

	origHuh := wizardHuhFn
	origRun := huhRun
	origPresent := initProviderCredentialPresentFn
	origValidate := initProviderValidateFn
	origStore := initProviderStoreFn
	origProbe := githubInitProbeFn
	origHostGit := hostGitIdentityFn
	t.Cleanup(func() {
		wizardHuhFn = origHuh
		huhRun = origRun
		initProviderCredentialPresentFn = origPresent
		initProviderValidateFn = origValidate
		initProviderStoreFn = origStore
		githubInitProbeFn = origProbe
		hostGitIdentityFn = origHostGit
	})

	wizardHuhFn = func() bool { return true }
	hostGitIdentityFn = func() (string, string) { return "Test User", "test@example.gouv.fr" }
	initProviderCredentialPresentFn = func(context.Context, codebox.InferenceProvider, string) bool { return false }
	initProviderValidateFn = func(_ context.Context, _ codebox.InferenceProvider, key string) codebox.CredentialProbe {
		if key == "bad-key" {
			return codebox.CredentialProbe{Rejected: true, Detail: "401 Unauthorized"}
		}
		return codebox.CredentialProbe{}
	}
	var storedSecret string
	initProviderStoreFn = func(_ context.Context, _ codebox.InferenceProvider, key string) (string, error) {
		storedSecret = key
		return "", nil
	}
	githubInitProbeFn = func(string) codebox.GitHubProjectStatus {
		return codebox.GitHubProjectStatus{HasOrigin: false}
	}

	typeInput := func(form *huh.Form, text string) string {
		form.Init()
		view := form.View()
		for _, r := range text {
			_, _ = form.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		_, _ = form.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		return view
	}

	formIndex := 0
	var keyDescriptions []string
	huhRun = func(form *huh.Form) error {
		formIndex++
		if formIndex == 2 {
			// First key attempt: rejected.
			keyDescriptions = append(keyDescriptions, typeInput(form, "bad-key"))
			return nil
		}
		if formIndex == 3 {
			// Second key attempt: accepted; must show inline error from first attempt.
			keyDescriptions = append(keyDescriptions, typeInput(form, "good-secret-key"))
			return nil
		}
		// Other screens (provider select, agent, runtime, skills, MCP): accept defaults.
		return nil
	}

	// One newline for the welcome screen (Screen 1).
	in := bufio.NewReader(strings.NewReader("\n"))
	var configured bool
	var err error
	_ = captureStdout(t, func() {
		configured, _, err = offerProjectInitWithReader(root, parsedArgs{action: "attach"}, in, true)
	})
	if err != nil || !configured {
		t.Fatalf("combined first run failed: configured=%v err=%v", configured, err)
	}
	if storedSecret != "good-secret-key" {
		t.Fatalf("stored secret = %q, want good-secret-key", storedSecret)
	}
	if len(keyDescriptions) < 2 || !strings.Contains(keyDescriptions[1], "401 Unauthorized") {
		t.Fatalf("rejected key must display inline error on retry; views=%v", keyDescriptions)
	}

	// Machine setup must now be marked complete.
	completed, err := setupCompletedFn()
	if err != nil || !completed {
		t.Fatalf("machine setup must be marked complete after combined first run: completed=%v err=%v", completed, err)
	}

	// Project manifest must be written with zero skills, zero MCPs, and no leaked secret.
	manifestBytes, err := os.ReadFile(codebox.ProjectManifestPath(root))
	if err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if strings.Contains(string(manifestBytes), "good-secret-key") {
		t.Fatal("project manifest leaked the API key value")
	}
	pm, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root))
	if err != nil {
		t.Fatalf("ReadProjectManifest: %v", err)
	}
	if len(pm.Skills) != 0 {
		t.Fatalf("fresh init must not preselect skills, got %v", pm.Skills)
	}
	if len(pm.MCPConnectors) != 0 {
		t.Fatalf("fresh init must not preselect MCPs, got %v", pm.MCPConnectors)
	}
	if len(pm.Providers) != 1 || pm.Providers[0].Kind != codebox.ProviderAlbert {
		t.Fatalf("project providers = %+v, want Albert", pm.Providers)
	}
}

func TestPersistMachineProviderSettingKeepsProvidersUniqueByKind(t *testing.T) {
	setupTestEnv(t)
	first, err := codebox.ResolveProvider(codebox.ProviderSettings{
		Kind:    codebox.ProviderOpenAICompatible,
		Name:    "Gateway A",
		BaseURL: "https://a.example/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := codebox.ResolveProvider(codebox.ProviderSettings{
		Kind:    codebox.ProviderOpenAICompatible,
		Name:    "Gateway B",
		BaseURL: "https://b.example/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := persistMachineProviderSetting(codebox.ProviderSettings{
		Kind: first.Kind, Name: first.Name, BaseURL: first.BaseURL, CredentialRef: codebox.ProviderCredentialRef(first),
	}); err != nil {
		t.Fatal(err)
	}
	if err := persistMachineProviderSetting(codebox.ProviderSettings{
		Kind: second.Kind, Name: second.Name, BaseURL: second.BaseURL, CredentialRef: codebox.ProviderCredentialRef(second),
	}); err != nil {
		t.Fatal(err)
	}
	settingsPath, err := codebox.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	us, err := codebox.ReadUserSettings(codebox.DefaultFS, settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codebox.ResolveProviders(us.Providers); err != nil {
		t.Fatalf("persisted machine providers must remain unique by kind: %v (providers=%+v)", err, us.Providers)
	}
	if len(us.Providers) != 1 || us.Providers[0].BaseURL != "https://b.example/v1" {
		t.Fatalf("machine providers = %+v, want single updated openai-compatible entry", us.Providers)
	}
}

func TestEligibleSavedProviderPreservesChosenEndpointPath(t *testing.T) {
	origPresent := initProviderCredentialPresentFn
	t.Cleanup(func() { initProviderCredentialPresentFn = origPresent })

	teamA, err := codebox.ResolveProvider(codebox.ProviderSettings{
		Kind:    codebox.ProviderOpenAICompatible,
		Name:    "Team A",
		BaseURL: "https://gateway.example/team-a/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	teamB, err := codebox.ResolveProvider(codebox.ProviderSettings{
		Kind:    codebox.ProviderOpenAICompatible,
		Name:    "Team B",
		BaseURL: "https://gateway.example/team-b/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := codebox.ProviderCredentialRef(teamA)
	initProviderCredentialPresentFn = func(_ context.Context, _ codebox.InferenceProvider, gotRef string) bool {
		return gotRef == ref
	}
	saved := []codebox.ProviderSettings{{
		Kind: teamA.Kind, Name: teamA.Name, BaseURL: teamA.BaseURL, CredentialRef: ref,
	}}
	got, ok := eligibleSavedProvider(context.Background(), teamB, saved)
	if !ok {
		t.Fatal("expected same-host saved key to be eligible")
	}
	if got.CredentialRef != ref {
		t.Fatalf("credentialRef = %q, want %q", got.CredentialRef, ref)
	}
	if got.Name != "Team B" || got.BaseURL != "https://gateway.example/team-b/v1" {
		t.Fatalf("saved-key reuse overwrote chosen endpoint: got %+v", got)
	}
}

func TestAskInitProviderStepReopensEndpointOnIncompatibleProbe(t *testing.T) {
	setupTestEnv(t)
	origRun := huhRun
	origPresent := initProviderCredentialPresentFn
	origValidate := initProviderValidateFn
	origStore := initProviderStoreFn
	origModels := initProviderModelsFn
	t.Cleanup(func() {
		huhRun = origRun
		initProviderCredentialPresentFn = origPresent
		initProviderValidateFn = origValidate
		initProviderStoreFn = origStore
		initProviderModelsFn = origModels
	})
	initProviderModelsFn = func(context.Context, codebox.InferenceProvider, string) ([]codebox.CatalogueModel, error) {
		return []codebox.CatalogueModel{{ID: "custom-model"}}, nil
	}

	initProviderCredentialPresentFn = func(context.Context, codebox.InferenceProvider, string) bool { return false }
	initProviderValidateFn = func(_ context.Context, p codebox.InferenceProvider, _ string) codebox.CredentialProbe {
		if strings.Contains(p.BaseURL, "/wrong") {
			return codebox.CredentialProbe{Incompatible: true, Detail: "HTTP 404"}
		}
		return codebox.CredentialProbe{}
	}
	storedCount := 0
	initProviderStoreFn = func(_ context.Context, p codebox.InferenceProvider, _ string) (string, error) {
		storedCount++
		return codebox.ProviderCredentialRef(p), nil
	}

	typeInput := func(form *huh.Form, text string) {
		form.Init()
		for _, r := range text {
			_, _ = form.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		_, _ = form.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	}

	step := 0
	var reopenedView string
	huhRun = func(form *huh.Form) error {
		step++
		switch step {
		case 1:
			// Provider select: move down twice to OpenAI-compatible.
			form.Init()
			_, _ = form.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			_, _ = form.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			_, _ = form.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		case 2:
			// First endpoint attempt: incompatible URL.
			form.Init()
			for _, r := range "Custom" {
				_, _ = form.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
			}
			form.NextField()
			for _, r := range "https://api.example/wrong" {
				_, _ = form.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
			}
			_, _ = form.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		case 3:
			// Key prompt for incompatible endpoint.
			typeInput(form, "secret-key")
		case 4:
			// Reopened endpoint form must show the incompatible error; fix URL.
			form.Init()
			_, _ = form.Update(tea.WindowSizeMsg{Width: 160, Height: 24})
			reopenedView = form.View()
			// Keep name ("Custom"), move to URL field, replace with valid URL.
			form.NextField()
			for i := 0; i < 40; i++ {
				_, _ = form.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
			}
			for _, r := range "https://api.example/v1" {
				_, _ = form.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
			}
			_, _ = form.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		case 5:
			// Key prompt for corrected endpoint.
			typeInput(form, "secret-key")
		}
		return nil
	}

	got, err := askInitProviderStepHuh(initOptions{}, codebox.InitAnswers{})
	if err != nil {
		t.Fatalf("askInitProviderStepHuh: %v", err)
	}
	if !strings.Contains(reopenedView, "HTTP 404") {
		t.Fatalf("reopened endpoint prompt must explain incompatible failure; view=\n%s", reopenedView)
	}
	if storedCount != 1 {
		t.Fatalf("incompatible endpoint must not be stored; storedCount = %d, want 1", storedCount)
	}
	if len(got.Providers) != 1 || got.Providers[0].BaseURL != "https://api.example/v1" {
		t.Fatalf("providers = %+v, want corrected endpoint", got.Providers)
	}
}

func TestInitWizardPlanLeavesPiModelEmptyForNonAlbertProvider(t *testing.T) {
	root := initTestProject(t)
	mistral, err := codebox.ResolveProvider(codebox.ProviderSettings{Kind: codebox.ProviderMistral})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (codebox.InitWizard{}).Plan(codebox.InitAnswers{
		Root:    root,
		Harness: string(codebox.HarnessPi),
		Providers: []codebox.ProviderSettings{{
			Kind:          mistral.Kind,
			Name:          mistral.Name,
			BaseURL:       mistral.BaseURL,
			CredentialRef: codebox.ProviderCredentialRef(mistral),
		}},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Answers.Model != "" {
		t.Fatalf("Pi + Mistral-only project must not be assigned an Albert model ref, got %q", plan.Answers.Model)
	}
}

func TestPersistMachineProviderSettingDeduplicatesSharedHost(t *testing.T) {
	setupTestEnv(t)
	mistral, err := codebox.ResolveProvider(codebox.ProviderSettings{Kind: codebox.ProviderMistral})
	if err != nil {
		t.Fatal(err)
	}
	if err := persistMachineProviderSetting(codebox.ProviderSettings{
		Kind: mistral.Kind, Name: mistral.Name, BaseURL: mistral.BaseURL, CredentialRef: codebox.ProviderCredentialRef(mistral),
	}); err != nil {
		t.Fatal(err)
	}
	// Connect an OpenAI-compatible provider pointing at Mistral's host.
	compat, err := codebox.ResolveProvider(codebox.ProviderSettings{
		Kind:    codebox.ProviderOpenAICompatible,
		Name:    "Mistral Compat",
		BaseURL: "https://api.mistral.ai/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := persistMachineProviderSetting(codebox.ProviderSettings{
		Kind: compat.Kind, Name: compat.Name, BaseURL: compat.BaseURL, CredentialRef: codebox.ProviderCredentialRef(compat),
	}); err != nil {
		t.Fatal(err)
	}
	settingsPath, err := codebox.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	us, err := codebox.ReadUserSettings(codebox.DefaultFS, settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codebox.ResolveProviders(us.Providers); err != nil {
		t.Fatalf("saved machine providers must not contain duplicate hosts: %v (providers=%+v)", err, us.Providers)
	}
	if len(us.Providers) != 1 || us.Providers[0].Kind != codebox.ProviderOpenAICompatible {
		t.Fatalf("machine providers = %+v, want single non-colliding entry", us.Providers)
	}
}

func TestMergeProjectRemoteProviderPreservesModelRequiredProviders(t *testing.T) {
	mistral, err := codebox.ResolveProvider(codebox.ProviderSettings{Kind: codebox.ProviderMistral})
	if err != nil {
		t.Fatal(err)
	}
	answers := codebox.InitAnswers{
		Model:      "albert/Qwen3-Coder-30B-A3B-Instruct",
		SmallModel: "mistral/mistral-small-latest",
		Providers: []codebox.ProviderSettings{
			{Kind: codebox.ProviderAlbert, Name: "Albert API"},
			{Kind: mistral.Kind, Name: mistral.Name, BaseURL: mistral.BaseURL, CredentialRef: codebox.ProviderCredentialRef(mistral)},
		},
	}
	merged := mergeProjectRemoteProvider(answers, codebox.ProviderSettings{Kind: codebox.ProviderAlbert, Name: "Albert API"})
	if len(merged) != 2 {
		t.Fatalf("expected Albert + Mistral (needed by smallModel), got %+v", merged)
	}
	if merged[0].Kind != codebox.ProviderAlbert || merged[1].Kind != codebox.ProviderMistral {
		t.Fatalf("merged providers = %+v, want Albert and Mistral", merged)
	}
}

func TestReconcileInitProviderPopulatesModelsForNewNonAlbertPlatform(t *testing.T) {
	setupTestEnv(t)
	origModels := initProviderModelsFn
	t.Cleanup(func() { initProviderModelsFn = origModels })
	initProviderModelsFn = func(_ context.Context, _ codebox.InferenceProvider, _ string) ([]codebox.CatalogueModel, error) {
		return []codebox.CatalogueModel{
			{ID: "mistral-large-latest"},
			{ID: "mistral-small-latest"},
		}, nil
	}

	mistral, err := codebox.ResolveProvider(codebox.ProviderSettings{Kind: codebox.ProviderMistral})
	if err != nil {
		t.Fatal(err)
	}
	setting := codebox.ProviderSettings{
		Kind:          mistral.Kind,
		Name:          mistral.Name,
		BaseURL:       mistral.BaseURL,
		CredentialRef: codebox.ProviderCredentialRef(mistral),
	}
	if err := persistMachineProviderSetting(setting); err != nil {
		t.Fatal(err)
	}
	got := reconcileInitProviderAnswer(context.Background(), initOptions{}, codebox.InitAnswers{}, mistral, setting, "secret-key", true)
	if got.Model != "mistral/mistral-large-latest" {
		t.Fatalf("project model = %q, want mistral/mistral-large-latest", got.Model)
	}
	settingsPath, err := codebox.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	us, err := codebox.ReadUserSettings(codebox.DefaultFS, settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if us.DefaultModel != "mistral/mistral-large-latest" {
		t.Fatalf("UserSettings.DefaultModel = %q, want mistral/mistral-large-latest", us.DefaultModel)
	}
	overlays := resolveProviderOverlays([]codebox.InferenceProvider{mistral}, got.Model)
	if len(overlays) != 1 || len(overlays[0].Models) != 2 {
		t.Fatalf("expected Mistral overlay with 2 models, got %+v", overlays)
	}
}

func TestReconcileInitProviderClearsStaleLegacyAlbertCredentialRef(t *testing.T) {
	albert, err := codebox.ResolveProvider(codebox.ProviderSettings{Kind: codebox.ProviderAlbert})
	if err != nil {
		t.Fatal(err)
	}
	// Project inherited a legacy custom credentialRef, then stored a new key
	// under the default Albert entry (CredentialRef == "").
	inherited := codebox.InitAnswers{
		CredentialRef: "custom-old-albert",
		Providers:     []codebox.ProviderSettings{{Kind: codebox.ProviderAlbert, CredentialRef: "custom-old-albert"}},
	}
	replaced := codebox.ProviderSettings{Kind: albert.Kind, Name: albert.Name, BaseURL: albert.BaseURL, CredentialRef: ""}
	got := reconcileInitProviderAnswer(context.Background(), initOptions{}, inherited, albert, replaced, "new-key", true)
	if got.CredentialRef != "" {
		t.Fatalf("legacy CredentialRef = %q, want empty so launch uses the newly stored default Albert key", got.CredentialRef)
	}
}

func TestDiscoverNonAlbertModelRefsSkipsNonChatModels(t *testing.T) {
	setupTestEnv(t)
	origModels := initProviderModelsFn
	t.Cleanup(func() { initProviderModelsFn = origModels })
	initProviderModelsFn = func(_ context.Context, _ codebox.InferenceProvider, _ string) ([]codebox.CatalogueModel, error) {
		return []codebox.CatalogueModel{
			{ID: "bge-embedding-v1", Type: "text-embeddings-inference"},
			{ID: "bge-reranker-large"},
			{ID: "whisper-large-v3"},
			{ID: "qwen-coder-32b", Type: "text-generation"},
		}, nil
	}
	compat, err := codebox.ResolveProvider(codebox.ProviderSettings{
		Kind:    codebox.ProviderOpenAICompatible,
		Name:    "Custom",
		BaseURL: "https://api.example/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	refs := discoverNonAlbertModelRefs(context.Background(), compat, "secret", true)
	if len(refs) != 1 || refs[0] != "openai-compatible/qwen-coder-32b" {
		t.Fatalf("discovered refs = %v, want only chat-capable qwen-coder-32b", refs)
	}
}

func TestReconcileInitProviderSetsAlbertDefaultWhenGlobalDefaultIsNonAlbert(t *testing.T) {
	setupTestEnv(t)
	settingsPath, err := codebox.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := codebox.WriteUserSettings(codebox.DefaultFS, settingsPath, codebox.UserSettings{
		SchemaVersion: 1,
		DefaultModel:  "mistral/mistral-large-latest",
		Providers:     []codebox.ProviderSettings{{Kind: codebox.ProviderMistral}},
	}); err != nil {
		t.Fatal(err)
	}
	albert, err := codebox.ResolveProvider(codebox.ProviderSettings{Kind: codebox.ProviderAlbert})
	if err != nil {
		t.Fatal(err)
	}
	setting := codebox.ProviderSettings{Kind: albert.Kind, Name: albert.Name, BaseURL: albert.BaseURL}
	got := reconcileInitProviderAnswer(context.Background(), initOptions{}, codebox.InitAnswers{}, albert, setting, "", false)
	if codebox.ModelRefProvider(got.Model) != codebox.ProviderAlbert || got.Model == "" {
		t.Fatalf("Albert project with non-Albert global default must get an Albert model, got %q", got.Model)
	}
}

func TestDiscoverNonAlbertModelRefsFetchesLiveModelsForDifferentEndpointPath(t *testing.T) {
	setupTestEnv(t)
	origModels := initProviderModelsFn
	t.Cleanup(func() { initProviderModelsFn = origModels })

	settingsPath, err := codebox.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := codebox.WriteUserSettings(codebox.DefaultFS, settingsPath, codebox.UserSettings{
		SchemaVersion: 1,
		Models:        []string{"openai-compatible/team-a-only-model"},
		Providers: []codebox.ProviderSettings{{
			Kind:    codebox.ProviderOpenAICompatible,
			Name:    "Team A",
			BaseURL: "https://gateway.example/team-a/v1",
		}},
	}); err != nil {
		t.Fatal(err)
	}

	initProviderModelsFn = func(_ context.Context, p codebox.InferenceProvider, _ string) ([]codebox.CatalogueModel, error) {
		if p.BaseURL == "https://gateway.example/team-b/v1" {
			return []codebox.CatalogueModel{{ID: "team-b-chat-model", Type: "text-generation"}}, nil
		}
		return nil, nil
	}
	teamB, err := codebox.ResolveProvider(codebox.ProviderSettings{
		Kind:    codebox.ProviderOpenAICompatible,
		Name:    "Team B",
		BaseURL: "https://gateway.example/team-b/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	refs := discoverNonAlbertModelRefs(context.Background(), teamB, "", false)
	if len(refs) != 1 || refs[0] != "openai-compatible/team-b-chat-model" {
		t.Fatalf("different endpoint path must fetch its own models instead of reusing team-a cache, got %v", refs)
	}
}

func TestPersistMachineProviderModelsReconcilesStaleDefaultModel(t *testing.T) {
	setupTestEnv(t)
	settingsPath, err := codebox.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := codebox.WriteUserSettings(codebox.DefaultFS, settingsPath, codebox.UserSettings{
		SchemaVersion: 1,
		DefaultModel:  "openai-compatible/team-a-model",
		Models:        []string{"openai-compatible/team-a-model"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := persistMachineProviderModels(codebox.ProviderOpenAICompatible, []string{"openai-compatible/team-b-model"}); err != nil {
		t.Fatal(err)
	}
	us, err := codebox.ReadUserSettings(codebox.DefaultFS, settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if us.DefaultModel != "openai-compatible/team-b-model" {
		t.Fatalf("stale DefaultModel was not updated: got %q, want openai-compatible/team-b-model", us.DefaultModel)
	}
}

func TestReconcileInitProviderReplacesStaleInheritedModelsOnEndpointSwitch(t *testing.T) {
	setupTestEnv(t)
	origModels := initProviderModelsFn
	t.Cleanup(func() { initProviderModelsFn = origModels })
	initProviderModelsFn = func(_ context.Context, _ codebox.InferenceProvider, _ string) ([]codebox.CatalogueModel, error) {
		return []codebox.CatalogueModel{{ID: "team-b-model", Type: "text-generation"}}, nil
	}
	teamB, err := codebox.ResolveProvider(codebox.ProviderSettings{
		Kind:    codebox.ProviderOpenAICompatible,
		Name:    "Team B",
		BaseURL: "https://gateway.example/team-b/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	inherited := codebox.InitAnswers{
		Model:      "openai-compatible/team-a-model",
		SmallModel: "openai-compatible/team-a-small",
		Providers: []codebox.ProviderSettings{{
			Kind:    codebox.ProviderOpenAICompatible,
			Name:    "Team A",
			BaseURL: "https://gateway.example/team-a/v1",
		}},
	}
	setting := codebox.ProviderSettings{
		Kind:          teamB.Kind,
		Name:          teamB.Name,
		BaseURL:       teamB.BaseURL,
		CredentialRef: codebox.ProviderCredentialRef(teamB),
	}
	got := reconcileInitProviderAnswer(context.Background(), initOptions{}, inherited, teamB, setting, "secret", true)
	if got.Model != "openai-compatible/team-b-model" {
		t.Fatalf("inherited Model = %q, want openai-compatible/team-b-model", got.Model)
	}
	if got.SmallModel != "" {
		t.Fatalf("inherited stale SmallModel = %q, want empty", got.SmallModel)
	}
}
