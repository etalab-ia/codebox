package justcode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// This file composes Pi's managed configuration from the same overlay the
// OpenCode composer uses. The overlay is harness-independent on purpose: it
// describes the inference platforms, the model selection and the project's
// execution inputs, and each harness renders it into its own format. That
// keeps one source of truth for "what the project asked for" and lets a
// harness be added without duplicating the resolution rules (#176).
//
// Pi has no equivalent of OpenCode's final-merge overlay: it reads
// ~/.pi/agent/models.json and ~/.pi/agent/settings.json as plain files, with a
// project-local .pi/settings.json overriding the global one. just-code writes
// the global files inside the guest only; a project's own .pi/ files remain
// the project's business, exactly like a project opencode.jsonc.

// piModelsFile is the models.json document: custom providers and their models.
type piModelsFile struct {
	Providers map[string]piProvider `json:"providers"`
}

// piProvider is one provider entry. Pi resolves apiKey as a literal, an
// environment variable name, or a "!command"; just-code always names the
// environment variable that the guest secret delivery already populates.
type piProvider struct {
	BaseURL string    `json:"baseUrl"`
	API     string    `json:"api"`
	APIKey  string    `json:"apiKey"`
	Models  []piModel `json:"models"`
}

// piModel is one model entry. id is what Pi sends to the API, so an Albert
// entry carries the API model id and not the curated display alias.
type piModel struct {
	ID            string   `json:"id"`
	Name          string   `json:"name,omitempty"`
	Reasoning     bool     `json:"reasoning,omitempty"`
	Input         []string `json:"input,omitempty"`
	ContextWindow int      `json:"contextWindow,omitempty"`
	MaxTokens     int      `json:"maxTokens,omitempty"`
}

// piSettingsFile is the subset of settings.json just-code manages. Pi keeps
// user settings in the same file, so the guest copy is written once at
// preparation time and never rewritten under a running session.
type piSettingsFile struct {
	// EnableInstallTelemetry is disabled: a managed guest is installed by the
	// runtime, not by a user opting in, so the anonymous install ping has no
	// consent behind it. Pi's update check is disabled separately through
	// PI_SKIP_VERSION_CHECK (see PiSkipVersionCheckEnv).
	EnableInstallTelemetry bool `json:"enableInstallTelemetry"`
}

// piAPI is the only API type just-code's managed platforms speak: Albert,
// Mistral AI and the OpenAI-compatible platform are all Chat Completions.
const piAPI = "openai-completions"

// ComposePiModelsConfig renders the managed overlay as Pi's models.json. The
// document is deterministic (sorted providers, sorted models) because it is
// compared and diffed: the same overlay must always produce the same bytes.
func ComposePiModelsConfig(o ManagedOverlay) (string, error) {
	o = o.EffectiveOverlay()
	providers := map[string]piProvider{}
	if len(o.Models) > 0 || o.AlbertEnabled {
		models := make([]piModel, 0, len(o.Models))
		for _, m := range piAlbertModels(o) {
			if entry, ok := piModelFromAlbert(m); ok {
				models = append(models, entry)
			}
		}
		if len(models) > 0 {
			sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
			providers[string(ProviderAlbert)] = piProvider{
				BaseURL: albertBaseURL,
				API:     piAPI,
				// The guest secret delivery populates this variable; with the
				// Microsandbox proxy the guest only ever holds the placeholder.
				APIKey: msbAPISecretEnv,
				Models: models,
			}
		}
	}
	for _, provider := range o.Providers {
		if provider.Kind == "" || provider.APIKeyEnv == "" || provider.BaseURL == "" {
			continue
		}
		models := make([]piModel, 0, len(provider.Models))
		for _, m := range provider.Models {
			id := m.ModelID
			if id == "" {
				id = m.ID
			}
			if id == "" {
				continue
			}
			name := m.Name
			if name == "" {
				name = id
			}
			models = append(models, piModel{
				ID:            id,
				Name:          name,
				Input:         []string{"text"},
				ContextWindow: m.Context,
				MaxTokens:     m.Output,
			})
		}
		if len(models) == 0 {
			continue
		}
		sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
		providers[provider.Kind] = piProvider{
			BaseURL: provider.BaseURL,
			API:     piAPI,
			APIKey:  provider.APIKeyEnv,
			Models:  models,
		}
	}
	if len(providers) == 0 {
		return "", fmt.Errorf("no inference platform to configure for Pi")
	}
	document := piModelsFile{Providers: providers}
	// Marshal with sorted keys: map keys are emitted in sorted order by
	// encoding/json, and the model slices are sorted above, so the output is
	// stable without a custom marshaller.
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data) + "\n", nil
}

// piAlbertModels resolves the Albert model set a Pi configuration must
// describe. The overlay may legitimately carry none — the CLI passes an empty
// slice when nothing beyond the embedded defaults is selected, which for
// OpenCode means "use the embedded asset". Pi has no embedded asset, so the
// curated defaults fill in; without them the guest would start with no
// provider at all on the default path.
func piAlbertModels(o ManagedOverlay) []AlbertModelConfig {
	if len(o.Models) > 0 {
		return o.Models
	}
	var models []AlbertModelConfig
	for _, choice := range CuratedAlbertModels() {
		if choice.Default {
			models = append(models, choice.AlbertModelConfig())
		}
	}
	return models
}

// piModelFromAlbert maps a curated Albert model to a Pi entry. The curated ID
// is the display alias ("qwen3-coder"); Pi must send the API id, which the
// curated entry carries as ModelID.
func piModelFromAlbert(m AlbertModelConfig) (piModel, bool) {
	id := m.ModelID
	if id == "" {
		id = m.ID
	}
	if id == "" {
		return piModel{}, false
	}
	name := m.Name
	if name == "" {
		name = m.ID
	}
	inputs := []string{"text"}
	if m.Vision {
		inputs = append(inputs, "image")
	}
	return piModel{
		ID:            id,
		Name:          name,
		Input:         inputs,
		ContextWindow: m.Context,
		MaxTokens:     m.Output,
	}, true
}

// ComposePiSettingsConfig renders the managed Pi settings.json.
func ComposePiSettingsConfig() (string, error) {
	data, err := json.MarshalIndent(piSettingsFile{EnableInstallTelemetry: false}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data) + "\n", nil
}

// PiModelRef resolves the overlay's model selection to the "provider/id" form
// Pi expects on --model. The overlay carries curated aliases ("albert/qwen3-coder");
// Pi has to be given the API id, so the curated entry is looked up. An
// unknown alias is passed through unchanged rather than silently replaced: the
// catalogue is fetched live, and a project may legitimately name a model the
// current catalogue did not list.
func PiModelRef(o ManagedOverlay) string {
	o = o.EffectiveOverlay()
	selection := strings.TrimSpace(o.Model)
	if selection == "" {
		return ""
	}
	kind, alias, ok := strings.Cut(selection, "/")
	if !ok {
		return selection
	}
	if ProviderKind(kind) == ProviderAlbert {
		for _, m := range piAlbertModels(o) {
			if m.ID == alias {
				if m.ModelID != "" {
					return kind + "/" + m.ModelID
				}
				return selection
			}
		}
		return selection
	}
	for _, provider := range o.Providers {
		if provider.Kind != kind {
			continue
		}
		for _, m := range provider.Models {
			if m.ID == alias && m.ModelID != "" {
				return kind + "/" + m.ModelID
			}
		}
	}
	return selection
}

// PiConfigFingerprint digests the managed Pi configuration so a guest can
// report which configuration it actually has on disk. The prep writes it to a
// marker next to the files, and the readiness probe compares the marker to the
// fingerprint the host is currently sending: matching means the guest holds the
// configuration this launch wants, and a mismatch means preparation still has
// to run, even when an older marker and the installed binary already exist.
func PiConfigFingerprint(models, settings string) string {
	sum := sha256.Sum256([]byte(models + "\x00" + settings))
	return hex.EncodeToString(sum[:])
}
