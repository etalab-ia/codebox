package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"charm.land/huh/v2"
	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
)

// initHostGOOSFn returns the host operating system used to filter runtime
// choices in the wizard (#445, #461). Tests stub it to exercise macOS, Linux,
// and Windows option lists on any runner.
var initHostGOOSFn = func() string { return runtime.GOOS }

// initProviderCredentialPresentFn reports whether the machine credential store
// holds a usable secret for the given provider and reference.
var initProviderCredentialPresentFn = func(ctx context.Context, provider codebox.InferenceProvider, ref string) bool {
	if err := codebox.ValidateProviderCredentialRef(provider, ref); err != nil {
		return false
	}
	if provider.Kind == codebox.ProviderAlbert {
		_, err := resolveAlbertKeyForCredentialRef(ref)
		return err == nil
	}
	if strings.TrimSpace(ref) == "" {
		ref = codebox.ProviderCredentialRef(provider)
	}
	_, err := codebox.ReadStoredCredential(ctx, codebox.CredentialKind(ref))
	return err == nil
}

// initProviderValidateFn probes an entered API key against the chosen provider
// endpoint. Tests stub it so no network traffic occurs.
var initProviderValidateFn = func(ctx context.Context, provider codebox.InferenceProvider, key string) codebox.CredentialProbe {
	return codebox.ValidateProviderKey(ctx, nil, provider, key)
}

// initProviderStoreFn writes a validated API key into the machine credential
// store under its host-bound reference and returns the stored reference.
var initProviderStoreFn = func(ctx context.Context, provider codebox.InferenceProvider, key string) (string, error) {
	store, err := authStore(false)
	if err != nil {
		return "", err
	}
	ref := strings.TrimSpace(provider.CredentialRef)
	if provider.Kind == codebox.ProviderAlbert {
		kind := codebox.CredentialAlbert
		if ref != "" {
			kind = codebox.CredentialKind(ref)
		}
		if err := store.Put(ctx, kind, key); err != nil {
			return "", err
		}
		codebox.BumpCredentialGeneration(kind)
		_ = codebox.RecordCredentialStore(codebox.DefaultStateDir(), kind, store.Kind())
		return ref, nil
	}
	if ref == "" {
		ref = codebox.ProviderCredentialRef(provider)
	}
	if err := codebox.ValidateProviderCredentialRef(provider, ref); err != nil {
		return "", err
	}
	kind := codebox.CredentialKind(ref)
	if err := store.Put(ctx, kind, key); err != nil {
		return "", err
	}
	codebox.BumpCredentialGeneration(kind)
	_ = codebox.RecordCredentialStore(codebox.DefaultStateDir(), kind, store.Kind())
	return ref, nil
}

// initProviderStepFn is the interactive provider/key screen in askInitQuestionsHuh.
// Tests that focus on downstream steps may stub it; end-to-end tests exercise
// the real askInitProviderStepHuh.
var initProviderStepFn = askInitProviderStepHuh

// initRuntimeOptionsForOS returns the isolated-machine options supported on
// goos (#445, #461): Tart is macOS-only, agent-vm is Unix-only, and both
// non-default runtimes are marked experimental.
func initRuntimeOptionsForOS(goos string) []huh.Option[string] {
	options := []huh.Option[string]{
		huh.NewOption("Microsandbox (recommandé)", string(codebox.RuntimeMicrosandbox)),
	}
	switch goos {
	case "darwin":
		options = append(options,
			huh.NewOption("Tart (expérimental) : VM macOS complète ; dossier partagé", string(codebox.RuntimeTart)),
			huh.NewOption("agent-vm (expérimental) : VM Lima ; dossier partagé", string(codebox.RuntimeAgentVM)),
		)
	case "linux":
		options = append(options,
			huh.NewOption("agent-vm (expérimental) : VM Lima ; dossier partagé", string(codebox.RuntimeAgentVM)),
		)
	}
	return options
}

// eligibleSavedProvider returns the chosen provider paired with the first saved
// machine credential reference whose kind, resolved host, and reference match
// chosen AND whose secret is present in the machine store. A key for another
// provider or host is never returned, and the newly chosen endpoint name and
// base URL are preserved (#445, #461).
func eligibleSavedProvider(ctx context.Context, chosen codebox.InferenceProvider, saved []codebox.ProviderSettings) (codebox.ProviderSettings, bool) {
	for _, entry := range saved {
		if entry.Kind != chosen.Kind {
			continue
		}
		resolved, err := codebox.ResolveProvider(entry)
		if err != nil {
			continue
		}
		if !strings.EqualFold(resolved.Host, chosen.Host) {
			continue
		}
		ref := strings.TrimSpace(entry.CredentialRef)
		if err := codebox.ValidateProviderCredentialRef(chosen, ref); err != nil {
			continue
		}
		if !initProviderCredentialPresentFn(ctx, chosen, ref) {
			continue
		}
		return codebox.ProviderSettings{
			Kind:          chosen.Kind,
			Name:          chosen.Name,
			BaseURL:       chosen.BaseURL,
			CredentialRef: ref,
		}, true
	}
	// Legacy machine setup may have stored an Albert key before us.Providers
	// existed; offer it only when the chosen provider is Albert.
	if chosen.Kind == codebox.ProviderAlbert && initProviderCredentialPresentFn(ctx, chosen, "") {
		return codebox.ProviderSettings{
			Kind:    chosen.Kind,
			Name:    chosen.Name,
			BaseURL: chosen.BaseURL,
		}, true
	}
	return codebox.ProviderSettings{}, false
}

// persistMachineProviderSetting records the newly connected provider in
// UserSettings, keeping providers unique by kind and host so ResolveProviders
// never rejects the saved machine settings, populates git identity from host
// git config when absent, and clears any stale setup journal so
// HasCompletedSetup reports true.
func persistMachineProviderSetting(setting codebox.ProviderSettings) error {
	newResolved, err := codebox.ResolveProvider(setting)
	if err != nil {
		return err
	}
	settingsPath, err := codebox.UserSettingsPath()
	if err != nil {
		return err
	}
	us, err := codebox.ReadUserSettings(codebox.DefaultFS, settingsPath)
	if err != nil {
		return err
	}
	us.SchemaVersion = 1
	next := make([]codebox.ProviderSettings, 0, len(us.Providers)+1)
	for _, existing := range us.Providers {
		if existing.Kind == setting.Kind {
			continue
		}
		if resolved, rerr := codebox.ResolveProvider(existing); rerr == nil && strings.EqualFold(resolved.Host, newResolved.Host) {
			continue
		}
		next = append(next, existing)
	}
	next = append(next, setting)
	if _, err := codebox.ResolveProviders(next); err != nil {
		return err
	}
	us.Providers = next
	if strings.TrimSpace(us.GitName) == "" || strings.TrimSpace(us.GitEmail) == "" {
		hostName, hostEmail := hostGitIdentityFn()
		if strings.TrimSpace(us.GitName) == "" && strings.TrimSpace(hostName) != "" {
			us.GitName = strings.TrimSpace(hostName)
		}
		if strings.TrimSpace(us.GitEmail) == "" && strings.TrimSpace(hostEmail) != "" {
			us.GitEmail = strings.TrimSpace(hostEmail)
		}
	}
	if err := codebox.WriteUserSettings(codebox.DefaultFS, settingsPath, us); err != nil {
		return err
	}
	_ = codebox.RemoveSetupJournal(codebox.DefaultFS, codebox.DefaultStateDir())
	return nil
}

// mergeProjectRemoteProvider sets the chosen remote provider on answers while
// preserving any explicit local model server provider (e.g. --local-server)
// and any other existing non-colliding remote providers required by retained
// model or small-model selections.
func mergeProjectRemoteProvider(answers codebox.InitAnswers, chosen codebox.ProviderSettings) []codebox.ProviderSettings {
	chosenHost := ""
	if resolved, err := codebox.ResolveProvider(chosen); err == nil {
		chosenHost = resolved.Host
	}
	neededKinds := map[codebox.ProviderKind]bool{}
	if m := strings.TrimSpace(answers.Model); m != "" {
		neededKinds[codebox.ModelRefProvider(m)] = true
	}
	if sm := strings.TrimSpace(answers.SmallModel); sm != "" {
		neededKinds[codebox.ModelRefProvider(sm)] = true
	}
	seenKinds := map[codebox.ProviderKind]bool{chosen.Kind: true}
	out := []codebox.ProviderSettings{chosen}
	for _, p := range answers.Providers {
		if p.Kind == codebox.ProviderLocal {
			out = append(out, p)
			continue
		}
		if !neededKinds[p.Kind] || seenKinds[p.Kind] {
			continue
		}
		if resolved, err := codebox.ResolveProvider(p); err == nil && chosenHost != "" && strings.EqualFold(resolved.Host, chosenHost) {
			continue
		}
		seenKinds[p.Kind] = true
		out = append(out, p)
	}
	return out
}

// askInitProviderStepHuh renders screen 2 of the six-screen first run / init
// flow: choose the project's inference provider, reuse an eligible host-bound
// saved key when one exists, or enter and validate a new masked key (#445, #461).
func askInitProviderStepHuh(opts initOptions, answers codebox.InitAnswers) (codebox.InitAnswers, error) {
	if opts.Set["credential-ref"] {
		return answers, nil
	}
	ctx := context.Background()

	var savedProviders []codebox.ProviderSettings
	if settingsPath, err := codebox.UserSettingsPath(); err == nil {
		if us, readErr := codebox.ReadUserSettings(codebox.DefaultFS, settingsPath); readErr == nil {
			savedProviders = us.Providers
		}
	}

	kindChoice := string(codebox.ProviderAlbert)
	customName := ""
	customBaseURL := ""
	for _, p := range answers.Providers {
		if p.Kind == codebox.ProviderLocal {
			continue
		}
		kindChoice = string(p.Kind)
		if p.Kind == codebox.ProviderOpenAICompatible {
			customName = p.Name
			customBaseURL = p.BaseURL
		}
		break
	}
	if kindChoice == string(codebox.ProviderAlbert) && len(answers.Providers) == 0 && len(savedProviders) > 0 {
		for _, p := range savedProviders {
			if p.Kind == codebox.ProviderLocal {
				continue
			}
			kindChoice = string(p.Kind)
			if p.Kind == codebox.ProviderOpenAICompatible {
				customName = p.Name
				customBaseURL = p.BaseURL
			}
			break
		}
	}

	title := initProviderStepTitle(opts)
	kindField := huh.NewSelect[string]().
		Title(title).
		Description("Choisissez la plateforme d'inférence pour ce projet. Les clés restent enregistrées sur ce poste, jamais dans le dépôt.").
		Options(
			huh.NewOption("Albert API (recommandé)", string(codebox.ProviderAlbert)),
			huh.NewOption("Mistral AI", string(codebox.ProviderMistral)),
			huh.NewOption("Plateforme compatible OpenAI", string(codebox.ProviderOpenAICompatible)),
		).
		Value(&kindChoice)
	if err := huhRun(huh.NewForm(huh.NewGroup(kindField))); err != nil {
		return answers, err
	}

	var endpointError string
	for {
		setting := codebox.ProviderSettings{Kind: codebox.ProviderKind(kindChoice)}
		if setting.Kind == codebox.ProviderOpenAICompatible {
			urlDesc := "URL de la racine API OpenAI (ex. https://openrouter.ai/api/v1)."
			if endpointError != "" {
				urlDesc = "Erreur : " + endpointError + "\n" + urlDesc
			}
			if err := huhRun(huh.NewForm(huh.NewGroup(
				huh.NewInput().
					Title("Nom de la plateforme").
					Placeholder("OpenRouter").
					Validate(func(s string) error {
						if strings.TrimSpace(s) == "" {
							return fmt.Errorf("indiquez un nom pour cette plateforme")
						}
						return nil
					}).
					Value(&customName),
				huh.NewInput().
					Title("URL de base (API OpenAI)").
					Description(urlDesc).
					Placeholder("https://openrouter.ai/api/v1").
					Validate(func(s string) error {
						_, err := codebox.ResolveProvider(codebox.ProviderSettings{
							Kind:    codebox.ProviderOpenAICompatible,
							Name:    strings.TrimSpace(customName),
							BaseURL: strings.TrimSpace(s),
						})
						return err
					}).
					Value(&customBaseURL),
			))); err != nil {
				return answers, err
			}
			endpointError = ""
			setting.Name = strings.TrimSpace(customName)
			setting.BaseURL = strings.TrimSpace(customBaseURL)
		}

		chosen, err := codebox.ResolveProvider(setting)
		if err != nil {
			return answers, err
		}

		if eligible, ok := eligibleSavedProvider(ctx, chosen, savedProviders); ok {
			useSaved := "saved"
			label := fmt.Sprintf("Utiliser la clé enregistrée (%s — %s)", eligible.Name, chosen.Host)
			err := huhRun(huh.NewForm(huh.NewGroup(
				huh.NewSelect[string]().
					Title(fmt.Sprintf("Clé %s", chosen.Name)).
					Description("Une clé enregistrée sur ce poste correspond à cette plateforme et à cet hôte.").
					Options(
						huh.NewOption(label, "saved"),
						huh.NewOption("Saisir une nouvelle clé", "new"),
					).
					Value(&useSaved),
			)))
			if err != nil {
				return answers, err
			}
			if useSaved == "saved" {
				answers.Providers = mergeProjectRemoteProvider(answers, eligible)
				return answers, nil
			}
		}

		// Prompt for a new masked key; inline errors preserve the chosen provider
		// and endpoint inputs.
		var keyError string
		reopenEndpoint := false
		for {
			var key string
			desc := fmt.Sprintf("Clé pour %s (%s). La saisie est masquée et enregistrée dans le magasin de ce poste.", chosen.Name, chosen.Host)
			if keyError != "" {
				desc = "Erreur : " + keyError + "\n" + desc
			}
			err := huhRun(huh.NewForm(huh.NewGroup(
				huh.NewInput().
					Title(fmt.Sprintf("Clé %s", chosen.Name)).
					Description(desc).
					EchoMode(huh.EchoModePassword).
					Validate(func(s string) error {
						if strings.TrimSpace(s) == "" {
							return fmt.Errorf("saisissez une clé API")
						}
						return nil
					}).
					Value(&key),
			)))
			if err != nil {
				return answers, err
			}
			key = strings.TrimSpace(key)
			if key == "" {
				return answers, nil
			}
			probe := initProviderValidateFn(ctx, chosen, key)
			if probe.Rejected {
				keyError = fmt.Sprintf("clé refusée par %s (%s)", chosen.Name, probe.Detail)
				continue
			}
			if probe.Incompatible {
				detail := fmt.Sprintf("la réponse de %s n'est pas un catalogue de modèles compatible OpenAI (%s)", chosen.Name, probe.Detail)
				if chosen.Kind == codebox.ProviderOpenAICompatible {
					endpointError = detail
					reopenEndpoint = true
					break
				}
				keyError = detail
				continue
			}
			if probe.Unreachable {
				saveAnyway := "save"
				confirmErr := huhRun(huh.NewForm(huh.NewGroup(
					huh.NewSelect[string]().
						Title(fmt.Sprintf("%s ne répond pas", chosen.Name)).
						Description(probe.Detail).
						Options(
							huh.NewOption("Enregistrer la clé quand même", "save"),
							huh.NewOption("Réessayer la saisie", "retry"),
							huh.NewOption("Annuler", "cancel"),
						).
						Value(&saveAnyway),
				)))
				if confirmErr != nil {
					return answers, confirmErr
				}
				if saveAnyway == "retry" {
					keyError = ""
					continue
				}
				if saveAnyway == "cancel" {
					return answers, huh.ErrUserAborted
				}
			}

			ref, storeErr := initProviderStoreFn(ctx, chosen, key)
			if storeErr != nil {
				return answers, fmt.Errorf("impossible d'enregistrer la clé %s : %w", chosen.Name, storeErr)
			}
			persisted := codebox.ProviderSettings{
				Kind:          chosen.Kind,
				Name:          chosen.Name,
				BaseURL:       chosen.BaseURL,
				CredentialRef: ref,
			}
			if err := persistMachineProviderSetting(persisted); err != nil {
				fmt.Fprintf(os.Stderr, "Attention : la configuration globale n'a pas pu être mise à jour : %v\n", err)
			}
			answers.Providers = mergeProjectRemoteProvider(answers, persisted)
			return answers, nil
		}
		if reopenEndpoint {
			continue
		}
		return answers, nil
	}
}

func initProviderStepTitle(opts initOptions) string {
	return initStepPrefix(opts, 1) + "Plateforme d'inférence"
}

// isInitAborted reports whether err is a user cancellation from huh.
func isInitAborted(err error) bool {
	return errors.Is(err, huh.ErrUserAborted)
}
