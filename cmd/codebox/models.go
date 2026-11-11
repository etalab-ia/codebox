package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/etalab-ia/codebox/internal/codebox"
)

// modelsCmd implements `codebox models` (P10): the validated Albert model
// catalogue. A network failure falls back to the last-known-good cache (a
// stale list beats no list); the model selection is validated against
// whichever list won, and stale selections are called out rather than
// silently kept.
func modelsCmd(args []string) (int, error) {
	if len(args) == 0 {
		// Default view: list the usable models and validate the current
		// selection against the catalogue.
		return modelsListCmd()
	}
	switch args[0] {
	case "list":
		return modelsListCmd()
	default:
		return 2, fmt.Errorf("Unknown models command: %s (expected list)", args[0])
	}
}

func modelsListCmd() (int, error) {
	ctx := context.Background()
	settings, ok := currentUserSettings()
	if !ok {
		settings = codebox.UserSettings{SchemaVersion: 1}
	}
	providers, err := codebox.ResolveProviders(settings.Providers)
	if err != nil {
		return 1, err
	}
	selection := resolveModelSelection(".")
	for _, provider := range providers {
		if err := modelsListProvider(ctx, provider, settings, selection); err != nil {
			return 1, err
		}
	}
	return 0, nil
}

// modelsListProvider lists one platform's usable models and marks the checked
// ones. Albert keeps the last-known-good cache; the other platforms are read
// live because their catalogues are not curated.
func modelsListProvider(ctx context.Context, provider codebox.InferenceProvider, settings codebox.UserSettings, selection string) error {
	var models []codebox.CatalogueModel
	source := "live"
	if provider.Kind == codebox.ProviderAlbert {
		apiKey, err := resolveAlbertKeyForCatalogue()
		if err != nil {
			return err
		}
		cache := codebox.CatalogueCache{
			FS:  codebox.DefaultFS,
			Dir: codebox.DefaultStateDir(),
			Fetch: func(ctx context.Context) (codebox.Catalogue, error) {
				return catalogueModelFetchFn(ctx, nil, apiKey, codebox.AlbertCatalogueURL())
			},
		}
		catalogue, fresh, loadErr := cache.Load(ctx)
		if loadErr != nil {
			return fmt.Errorf("cannot load the Albert model catalogue and no last-known-good cache exists: %w", loadErr)
		}
		models = catalogue.TextGenerationModels()
		if !fresh {
			source = "last-known-good"
			fmt.Fprintln(os.Stderr, "Warning: the Albert catalogue could not be fetched; showing the last-known-good list.")
		}
		if selection != "" {
			kind, _ := codebox.ProviderModelID(selection)
			if kind == codebox.ProviderAlbert {
				if _, ok := catalogue.Find(selection); !ok && len(models) > 0 {
					fmt.Fprintf(os.Stderr, "Warning: the selected model %q is not in the Albert catalogue; it may have been removed.\n", selection)
				}
			}
		}
		if small := resolveSmallModelSelection("."); small != "" {
			kind, _ := codebox.ProviderModelID(small)
			if kind == codebox.ProviderAlbert {
				if _, ok := catalogue.Find(canonicalAlbertModelID(small)); !ok && len(models) > 0 {
					fmt.Fprintf(os.Stderr, "Warning: the selected small model %q is not in the Albert catalogue; it may have been removed.\n", small)
				}
			}
		}
	} else {
		apiKey, keyErr := resolveProviderKeyForCatalogue(provider)
		if keyErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: %s: %v\n", provider.Name, keyErr)
			models = providerFallbackModels(settings.Models, provider)
			source = "selection"
		} else {
			fetched, fetchErr := codebox.FetchProviderModels(ctx, nil, provider, apiKey)
			if fetchErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: the %s catalogue could not be fetched; showing the current selection.\n", provider.Name)
				models = providerFallbackModels(settings.Models, provider)
				source = "selection"
			} else {
				models = fetched
			}
		}
	}
	fmt.Printf("%s models (%s):\n", provider.Name, source)
	if len(models) == 0 {
		fmt.Println("  (none)")
		return nil
	}
	for _, model := range models {
		marker := "  "
		checked := false
		if provider.Kind == codebox.ProviderAlbert {
			checked = selection == "albert/"+model.ID || matchesAlias(selection, model.Alias)
			for _, id := range codebox.ProviderModelIDs(settings.Models, provider.Kind) {
				if id == model.ID {
					checked = true
				}
			}
		} else {
			checked = containsString(codebox.ProviderModelIDs(settings.Models, provider.Kind), model.ID)
		}
		if checked {
			marker = "->"
		}
		fmt.Printf("%s %-36s context: %d\n", marker, model.ID, model.MaxContextLength)
	}
	fmt.Println()
	return nil
}

func providerFallbackModels(refs []string, provider codebox.InferenceProvider) []codebox.CatalogueModel {
	var out []codebox.CatalogueModel
	for _, id := range codebox.ProviderModelIDs(refs, provider.Kind) {
		out = append(out, codebox.CatalogueModel{ID: id})
	}
	return out
}

// resolveProviderKeyForCatalogue obtains a key for a platform's catalogue
// request without exposing it.
func resolveProviderKeyForCatalogue(provider codebox.InferenceProvider) (string, error) {
	if provider.Kind == codebox.ProviderAlbert {
		return resolveAlbertKeyForCatalogue()
	}
	return codebox.ResolveProviderKey(context.Background(), codebox.Config{APIKey: os.Getenv("ALBERT_API_KEY")}, provider)
}

func matchesAlias(selection, alias string) bool {
	if alias == "" {
		return false
	}
	id := strings.TrimPrefix(selection, "albert/")
	for _, a := range strings.Split(alias, ",") {
		if a == id {
			return true
		}
	}
	return false
}

func sourceLabel(fresh bool) string {
	if fresh {
		return "live"
	}
	return "last-known-good"
}

// resolveAlbertKeyForCatalogue obtains a key for the catalogue request via
// the normal resolution chain (credentialRef > legacy env > store), without
// exposing it.
func resolveAlbertKeyForCatalogue() (string, error) {
	return resolveAlbertKeyForCatalogueAt(resolveProjectRootForAux())
}

// resolveAlbertKeyForCatalogueAt resolves the key for a specific project root,
// so a command configuring one directory does not read another's
// credentialRef (the reference lives in the project manifest).
func resolveAlbertKeyForCatalogueAt(projectRoot string) (string, error) {
	return resolveAlbertKeyForCredentialRef(resolveCredentialRef(projectRoot))
}

func resolveAlbertKeyForDefaultCatalogue() (string, error) {
	return resolveAlbertKeyForCredentialRef("")
}

func resolveAlbertKeyForCredentialRef(credentialRef string) (string, error) {
	cfg := codebox.Config{CredentialRef: credentialRef, APIKey: os.Getenv("ALBERT_API_KEY")}
	v, _, _, _, err := codebox.ResolveAlbert(ctxForCatalogue(), cfg, "")
	if err != nil {
		return "", fmt.Errorf("an Albert credential is needed to fetch the model catalogue: %w", err)
	}
	return v, nil
}

func ctxForCatalogue() context.Context { return context.Background() }

// resolveProjectRootForAux discovers the project root for commands that run
// before the main flow's discovery. Best-effort: "." when discovery fails.
func resolveProjectRootForAux() string {
	if pc, err := codebox.DiscoverProject("."); err == nil {
		return pc.Root
	}
	return "."
}
