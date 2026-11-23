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
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
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
	if err := codebox.WriteUserSettings(codebox.DefaultFS, settingsPath, codebox.UserSettings{
		SchemaVersion: 1,
		Providers:     []codebox.ProviderSettings{{Kind: codebox.ProviderMistral, CredentialRef: "provider-mistral-api.mistral.ai"}},
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
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
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
