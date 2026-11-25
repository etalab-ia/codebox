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

// firstRunChooseModelsFn runs the per-project model selection inside step [2/6].
// Tests can stub it independently of setupModelPickerRun when needed.
var firstRunChooseModelsFn = func(providers []codebox.InferenceProvider, current []string, currentDefault string) ([]string, string, error) {
	models, defaultModel, _, err := promptSetupModelsWithPrefix(providers, current, currentDefault, "[2/6]", true)
	return models, defaultModel, err
}

// combinedFirstRun runs the approved six-screen flow: welcome, provider/key &
// models, agent, runtime, skills and MCP. The last four screens belong to
// initRun; GitHub and advanced settings are conditional, not additional
// default steps.
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
		current, err := firstRunProviderDefaults(ctx, opts, &j)
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
		if !opts.Set["local-server"] {
			localProviders, err := firstRunLocalProviders(current)
			if err != nil {
				return 1, err
			}
			providers = append(providers, localProviders...)
		}
		j.SetPlatforms(providers)
		if err := codebox.WriteSetupJournal(fs, stateDir, j); err != nil {
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

	providerAnswers, err := resolveInitProviders(opts, codebox.InitAnswers{Root: opts.Root, Providers: append([]codebox.ProviderSettings(nil), opts.Providers...)})
	if err != nil {
		return 1, err
	}
	opts.Providers = providerAnswers.Providers
	defaultProviders, err := codebox.ResolveProviders(opts.Providers)
	if err != nil {
		return 1, err
	}
	if !opts.Set["model"] {
		defaultModel, defaultErr := firstRunDefaultModel(opts.Root, defaultProviders)
		if providersCollected {
			currentModels := firstRunModelDefaults(opts.Root, defaultProviders)
			chosenModels, chosenDefault, pickErr := firstRunChooseModelsFn(defaultProviders, currentModels, defaultModel)
			if pickErr != nil {
				if errors.Is(pickErr, huh.ErrUserAborted) {
					return 1, pickErr
				}
				if !opts.Advanced {
					if defaultErr != nil {
						return 1, defaultErr
					}
					return 1, pickErr
				}
				fmt.Fprintf(os.Stderr, "Attention : %v\n", pickErr)
			} else {
				opts.Models = chosenModels
				opts.Set["models"] = true
				opts.DefaultModel = chosenDefault
			}
		} else if defaultErr != nil {
			if !opts.Advanced {
				return 1, defaultErr
			}
			fmt.Fprintf(os.Stderr, "Attention : %v\n", defaultErr)
		} else {
			opts.DefaultModel = defaultModel
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
func firstRunProviderDefaults(ctx context.Context, opts initOptions, j *codebox.SetupJournal) ([]codebox.SetupPlatform, error) {
	root := opts.Root
	var settings []codebox.ProviderSettings
	if len(j.Platforms) > 0 {
		settings = j.PlatformSettings()
	}
	fromManifest := false
	if len(settings) == 0 {
		if pc, err := codebox.DiscoverProject(root); err == nil {
			if manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(pc.Root)); err == nil && len(manifest.Providers) > 0 {
				settings = append(settings, manifest.Providers...)
				fromManifest = true
			}
		}
	}
	if fromManifest {
		var userProviders []codebox.ProviderSettings
		if us, ok := currentUserSettings(); ok {
			userProviders = append(userProviders, us.Providers...)
		}
		if opts.Set["local-server"] {
			local, err := resolveInitLocalServer(ctx, opts.LocalServer)
			if err != nil {
				return nil, err
			}
			if len(userProviders) == 0 {
				userProviders = codebox.DefaultProviderSettings()
			}
			filtered := make([]codebox.ProviderSettings, 0, len(userProviders)+1)
			for _, provider := range userProviders {
				if provider.Kind != codebox.ProviderLocal {
					filtered = append(filtered, provider)
				}
			}
			userProviders = append(filtered, local)
			filteredSettings := make([]codebox.ProviderSettings, 0, len(settings)+1)
			for _, provider := range settings {
				if provider.Kind != codebox.ProviderLocal {
					filteredSettings = append(filteredSettings, provider)
				}
			}
			settings = append(filteredSettings, local)
		}
		if err := codebox.ValidateProjectProviders(settings, userProviders); err != nil {
			return nil, fmt.Errorf("la plateforme du projet ne peut pas être approuvée avant de réutiliser un identifiant : %w", err)
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
		if provider.NoKey {
			continue
		}
		if setupCredentialPresentFn(ctx, setupProviderStoreKind(provider)) {
			j.CredentialKinds = addCredentialKind(j.CredentialKinds, string(setupProviderStoreKind(provider)))
		}
	}
	return platforms, nil
}

// firstRunModelDefaults seeds the per-project model picker from an existing
// project manifest first, then from compatible machine-level user settings.
// Returning nil triggers fresh provider defaults (all curated Albert models).
func firstRunModelDefaults(root string, providers []codebox.InferenceProvider) []string {
	filter := func(models []string) []string {
		var out []string
		for _, raw := range models {
			kind, id := codebox.ProviderModelID(raw)
			if id == "" {
				continue
			}
			for _, provider := range providers {
				if provider.Kind == kind && provider.Kind != codebox.ProviderLocal {
					out = append(out, raw)
					break
				}
			}
		}
		return out
	}
	if pc, err := codebox.DiscoverProject(root); err == nil {
		if manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(pc.Root)); err == nil && len(manifest.Models) > 0 {
			if compatible := filter(manifest.Models); len(compatible) > 0 {
				return compatible
			}
		}
	}
	if us, ok := currentUserSettings(); ok && len(us.Models) > 0 {
		if compatible := filter(us.Models); len(compatible) > 0 {
			return compatible
		}
	}
	return nil
}

// firstRunDefaultModel resolves the model recorded in the project manifest.
// Existing compatible defaults win, Albert keeps its curated default, and other
// providers use their own catalogue. The machine-wide model settings are not
// changed: this is a project answer, editable later under --advanced.
func firstRunDefaultModel(root string, providers []codebox.InferenceProvider) (string, error) {
	if pc, err := codebox.DiscoverProject(root); err == nil {
		if manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(pc.Root)); err == nil && manifest.Model != "" {
			kind, _ := codebox.ProviderModelID(manifest.Model)
			for _, provider := range providers {
				if provider.Kind == kind {
					return manifest.Model, nil
				}
			}
		}
	}
	if us, ok := currentUserSettings(); ok && us.DefaultModel != "" {
		kind, _ := codebox.ProviderModelID(us.DefaultModel)
		for _, provider := range providers {
			if provider.Kind == kind {
				return us.DefaultModel, nil
			}
		}
	}
	for _, provider := range providers {
		if provider.Kind == codebox.ProviderAlbert {
			if model := defaultSetupModel(); model != "" {
				return model, nil
			}
		}
	}
	for _, provider := range providers {
		if provider.Kind == codebox.ProviderAlbert || provider.Kind == codebox.ProviderLocal {
			continue
		}
		if us, ok := currentUserSettings(); ok {
			if ids := codebox.ProviderModelIDs(us.Models, provider.Kind); len(ids) > 0 {
				return codebox.ProviderModelRef(provider.Kind, ids[0]), nil
			}
		}
		models, err := setupProviderCatalogueFn(provider)
		if err != nil {
			return "", fmt.Errorf("aucun modèle par défaut n'a pu être résolu pour %s : son catalogue est indisponible (%w) ; relancez le parcours ou choisissez un modèle avec 'codebox init --advanced'", provider.Name, err)
		}
		if len(models) == 0 {
			return "", fmt.Errorf("aucun modèle n'est disponible sur %s ; choisissez un modèle avec 'codebox init --advanced'", provider.Name)
		}
		for _, model := range models {
			if provider.Kind == codebox.ProviderMistral && model.ID == "mistral-large-latest" {
				return codebox.ProviderModelRef(provider.Kind, model.ID), nil
			}
		}
		for _, model := range models {
			if model.Type != "text-generation" {
				continue
			}
			return codebox.ProviderModelRef(provider.Kind, model.ID), nil
		}
		return "", fmt.Errorf("aucun modèle de conversation vérifiable n'est disponible sur %s ; indiquez un modèle explicite avec --model ou 'codebox init --advanced'", provider.Name)
	}
	return "", fmt.Errorf("aucun modèle par défaut n'est disponible pour la plateforme choisie ; choisissez un modèle avec 'codebox init --advanced'")
}

// firstRunLocalProviders preserves auxiliary local small-model connections,
// which are intentionally absent from the remote-provider selection screen.
// They are appended after credential collection so no local key is requested.
func firstRunLocalProviders(current []codebox.SetupPlatform) ([]codebox.InferenceProvider, error) {
	var providers []codebox.InferenceProvider
	for _, platform := range current {
		if platform.Kind != string(codebox.ProviderLocal) {
			continue
		}
		provider, err := codebox.ResolveProvider(codebox.ProviderSettings{
			Kind: codebox.ProviderKind(platform.Kind), Name: platform.Name, BaseURL: platform.BaseURL, CredentialRef: platform.CredentialRef,
		})
		if err != nil {
			return nil, err
		}
		providers = append(providers, provider)
	}
	return providers, nil
}

// mergeFirstRunMachineProviders updates the selected provider connections while
// preserving unselected machine connections for other projects. A project
// chooses a subset; it never revokes the other saved provider choices.
func mergeFirstRunMachineProviders(selected []codebox.InferenceProvider) []codebox.ProviderSettings {
	var existing []codebox.ProviderSettings
	if us, ok := currentUserSettings(); ok {
		existing = append(existing, us.Providers...)
	}
	if len(existing) == 0 {
		existing = codebox.DefaultProviderSettings()
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
