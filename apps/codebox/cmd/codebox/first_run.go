package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/huh/v2"
	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
)

// combinedFirstRunInitFn is the project plan/apply seam. Tests replace it to
// verify orchestration without touching a project or the host credential store.
var combinedFirstRunInitFn = initRun
var combinedFirstRunFn = combinedFirstRun

var firstRunChoosePlatformsFn = chooseSetupPlatformsWithTitle

// combinedFirstRun runs the approved six-screen flow: welcome, provider/key,
// agent, runtime, skills and MCP. The last four screens belong to initRun;
// GitHub and advanced settings are conditional, not additional default steps.
func combinedFirstRun(opts initOptions, in *bufio.Reader) (int, error) {
	if !wizardHuhFn() {
		return 1, fmt.Errorf("le premier parcours interactif utilise un terminal ; en script, fournissez les options nécessaires et --yes, sans réponses sur stdin")
	}
	if err := firstRunWelcome(opts.Root); err != nil {
		return 1, err
	}

	ctx := context.Background()
	fs := codebox.DefaultFS
	stateDir := codebox.DefaultStateDir()
	j, err := codebox.ReadSetupJournal(fs, stateDir)
	if err != nil {
		return 1, err
	}
	completed, err := setupCompletedFn()
	if err != nil {
		return 1, err
	}
	if !completed && j.Stage == codebox.StagePreflight && len(j.CredentialKinds) == 0 && j.GitName == "" {
		res := setupPreflightFn(ctx)
		j.Preflight = res
		if res.Fatal {
			fmt.Println("Ce poste ne peut pas exécuter la machine isolée de codebox :")
			printPreflight(res)
			_ = codebox.WriteSetupJournal(fs, stateDir, j)
			return 1, fmt.Errorf("installation interrompue : corrigez les points ci-dessus, puis relancez 'codebox'")
		}
		j.Stage = codebox.StageCredential
		if err := codebox.WriteSetupJournal(fs, stateDir, j); err != nil {
			return 1, err
		}
	}

	w := newSetupWizard(fs, stateDir, j.StoreKind == "file", nil, nil)
	providersCollected := !opts.Set["providers"]
	if providersCollected {
		current, err := firstRunProviderDefaults(ctx, opts.Root, &j)
		if err != nil {
			return 1, err
		}
		chosen, err := firstRunChoosePlatformsFn(current, in, "[2/6] Fournisseur et clé")
		if err != nil {
			return 1, err
		}
		providers, err := setupSelectedPlatformStage(ctx, w, in, j.StoreKind == "file", &j, fs, stateDir, chosen, firstRunProviderKeyScreenHeader)
		if err != nil {
			return 1, err
		}
		opts.Providers = providerSettingsOf(providers)
		opts.Set["providers"] = true

		machineProviders := mergeFirstRunMachineProviders(providers)
		resolvedMachineProviders, err := codebox.ResolveProviders(machineProviders)
		if err != nil {
			return 1, err
		}
		statsEnabled, statsAsked := codebox.ReadStatsConsent(fs, userSettingsPathOrEmpty())
		if j.StatsConsentAsked {
			statsEnabled, statsAsked = j.StatsEnabled, true
		}
		if err := w.SaveSettings("", nil, false, resolvedMachineProviders, statsEnabled, statsAsked); err != nil {
			return 1, err
		}
		if err := firstRunSaveIdentity(w, &j, fs, stateDir); err != nil {
			return 1, err
		}
	}

	opts.Combined = true
	code, err := combinedFirstRunInitFn(opts, in, true)
	if err != nil || code != 0 {
		return code, err
	}
	if providersCollected {
		if err := w.CompleteSetup(); err != nil {
			return 1, fmt.Errorf("le projet est configuré, mais l'état final du poste n'a pas pu être écrit : %w", err)
		}
	}
	return 0, nil
}

func firstRunWelcome(root string) error {
	if root == "" {
		root, _ = os.Getwd()
	}
	project := filepath.Base(filepath.Clean(root))
	if project == "." || project == string(filepath.Separator) || project == "" {
		project = root
	}
	displayRoot := root
	if home, _ := os.UserHomeDir(); home != "" {
		if rel, err := filepath.Rel(home, root); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			displayRoot = filepath.Join("~", rel)
		}
	}
	return huhRun(huh.NewForm(huh.NewGroup(
		huh.NewNote().
			Title("[1/6] Bienvenue dans codebox").
			Description(fmt.Sprintf("Projet : %s\nDossier : %s\n\nSix écrans courts configurent le poste si nécessaire, puis ce projet. Les réglages techniques restent disponibles avec 'codebox init --advanced'.\n\nAppuyez sur Entrée pour commencer, ou sur Ctrl+C pour quitter.", project, displayRoot)),
	)))
}

// firstRunProviderDefaults seeds a project choice from an interrupted journal,
// then from an existing project manifest, and finally from machine settings.
// Credential values are never read here: only their presence is recorded.
func firstRunProviderDefaults(ctx context.Context, root string, j *codebox.SetupJournal) ([]codebox.SetupPlatform, error) {
	if len(j.Platforms) > 0 {
		return append([]codebox.SetupPlatform(nil), j.Platforms...), nil
	}
	var settings []codebox.ProviderSettings
	if pc, err := codebox.DiscoverProject(root); err == nil {
		if manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(pc.Root)); err == nil {
			settings = append(settings, manifest.Providers...)
		}
	}
	if len(settings) == 0 {
		if us, ok := currentUserSettings(); ok {
			settings = append(settings, us.Providers...)
		}
	}
	providers, err := codebox.ResolveProviders(settings)
	if err != nil {
		return nil, err
	}
	platforms := make([]codebox.SetupPlatform, 0, len(providers))
	for _, provider := range providers {
		platforms = append(platforms, codebox.SetupPlatform{
			Kind: string(provider.Kind), Name: provider.Name, BaseURL: provider.BaseURL, CredentialRef: provider.CredentialRef,
		})
		if setupCredentialPresentFn(ctx, setupProviderStoreKind(provider)) {
			j.CredentialKinds = addCredentialKind(j.CredentialKinds, string(setupProviderStoreKind(provider)))
		}
	}
	return platforms, nil
}

// mergeFirstRunMachineProviders updates the selected provider connections while
// preserving unselected machine connections for other projects. A project
// chooses a subset; it never revokes the other saved provider choices.
func mergeFirstRunMachineProviders(selected []codebox.InferenceProvider) []codebox.ProviderSettings {
	var existing []codebox.ProviderSettings
	if us, ok := currentUserSettings(); ok {
		existing = append(existing, us.Providers...)
	}
	merged := append([]codebox.ProviderSettings(nil), existing...)
	for _, provider := range selected {
		settings := codebox.ProviderSettings{Kind: provider.Kind, Name: provider.Name, BaseURL: provider.BaseURL, CredentialRef: provider.CredentialRef}
		replaced := false
		for i := range merged {
			if merged[i].Kind == provider.Kind {
				merged[i] = settings
				replaced = true
				break
			}
		}
		if !replaced {
			merged = append(merged, settings)
		}
	}
	return merged
}

func firstRunProviderKeyScreenHeader(provider codebox.InferenceProvider, fallback bool) string {
	return strings.Replace(providerKeyScreenHeader(provider, fallback), "[1/3]", "[2/6]", 1)
}

func firstRunSaveIdentity(w codebox.SetupWizard, j *codebox.SetupJournal, fs codebox.FS, stateDir string) error {
	name, email := j.GitName, j.GitEmail
	if us, ok := currentUserSettings(); ok {
		if name == "" {
			name = us.GitName
		}
		if email == "" {
			email = us.GitEmail
		}
	}
	hostName, hostEmail := hostGitIdentityFn()
	if name == "" {
		name = hostName
	}
	if email == "" {
		email = hostEmail
	}
	var fields []huh.Field
	if strings.TrimSpace(name) == "" {
		fields = append(fields, huh.NewInput().Title("Prénom et nom").Value(&name))
	}
	if strings.TrimSpace(email) == "" {
		fields = append(fields, huh.NewInput().Title("Adresse e-mail").Value(&email))
	}
	if err := runSetupIdentityForm(fields); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			recordSetupAbandon(stateDir, "identity")
		}
		return err
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(email) == "" {
		return fmt.Errorf("le nom et l'adresse e-mail sont nécessaires pour signer les commits")
	}
	if err := w.SaveIdentity(strings.TrimSpace(name), strings.TrimSpace(email)); err != nil {
		return err
	}
	j.GitName, j.GitEmail = strings.TrimSpace(name), strings.TrimSpace(email)
	return codebox.WriteSetupJournal(fs, stateDir, *j)
}
