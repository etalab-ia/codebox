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

// piUnsupportedAlbertModels names the curated Albert models a Pi project must
// not be given (#347, decided 2026-11-09). Qwen3 Coder 30B is unreliable in
// streaming on Albert — 0/6, 1/3 and 3/5 across series, 6/6 non-streaming, and
// tool_choice "auto" does not fix it — and OpenCode fails on it identically,
// so the model is at fault, not the harness. OpenCode keeps offering it for
// the projects that already rely on it; Pi drops it from the set it is handed.
// The reason is part of the entry because both the refusal message and the
// documentation quote it.
var piUnsupportedAlbertModels = map[string]string{
	"qwen3-coder": "Qwen3 Coder 30B est instable en streaming sur Albert",
}

// PiUnsupportedModelReason reports why a model an overlay selected cannot run
// under Pi, or "" when nothing rules it out. A selection may name the curated
// alias ("albert/qwen3-coder") or the API model id, so both are recognised:
// the overlay carries aliases and the catalogue answers with ids.
func PiUnsupportedModelReason(selection string) string {
	name := strings.TrimPrefix(strings.TrimSpace(selection), "albert/")
	if name == "" {
		return ""
	}
	if reason, ok := piUnsupportedAlbertModels[name]; ok {
		return reason
	}
	for _, choice := range CuratedAlbertModels() {
		config := choice.AlbertModelConfig()
		if name != config.ModelID {
			continue
		}
		if reason, ok := piUnsupportedAlbertModels[config.ID]; ok {
			return reason
		}
	}
	return ""
}

// piAlbertRendered reports whether this overlay gets an Albert provider in
// models.json: the project asked for Albert models, or Albert is one of the
// connected platforms. Both the composition and the fallback read the same
// condition, so a model can never be chosen from a provider the document
// leaves out.
func piAlbertRendered(o ManagedOverlay) bool {
	return len(o.Models) > 0 || o.AlbertEnabled
}

// piDefaultModelRef is the Albert model an init wizard names for a Pi project
// that selected none, and the one it writes to the manifest. OpenCode falls
// back to the embedded asset's model; Pi has no embedded asset, so the first
// curated default Pi can actually run is taken instead. Reading the curated
// list rather than naming a model here keeps one source of truth: a default
// dropped from that list cannot leave this function pointing at a model Pi
// refuses.
func piDefaultModelRef() string {
	// The default project declares no model list, so what Pi renders for it is
	// exactly the curated defaults: resolving through the same function the
	// launch uses keeps the recap, the manifest and the launched configuration
	// on one answer.
	return piFallbackModelRef(ManagedOverlay{AlbertEnabled: true})
}

// piFallbackModelRef is the model a Pi project starts on when it selected none,
// taken from the models this overlay actually renders (#347 review). Naming a
// curated default unconditionally would hand a launch a model its own
// models.json does not contain: a project connected to Mistral only would be
// given an Albert reference, and a project whose Albert list excludes every
// curated default would be given one of the excluded ones.
//
// The curated order decides first, so the default path keeps starting on the
// model the init recap showed. A list that names no curated default falls back
// to the first rendered alias in sorted order — the alias being the form the
// configuration carries and the form PiModelRef resolves to an API id. It is a
// stable choice rather than the one models.json happens to list first, whose
// entries are sorted on the API id instead. Nothing rendered means no fallback:
// the caller keeps whatever EffectiveOverlay resolved, and the composition
// reports the gap.
//
// The reference is returned in its alias form ("albert/gemma-4"), the form the
// overlay carries and the form PiModelRef resolves to an API id — the same
// shape the curated defaults come in, so callers need no second rule.
func piFallbackModelRef(o ManagedOverlay) string {
	if !piAlbertRendered(o) {
		return ""
	}
	rendered := piAlbertModels(o)
	// piAlbertModels drops the models Pi must not run, from either source, so
	// an alias present here is runnable.
	aliases := make([]string, 0, len(rendered))
	present := make(map[string]bool, len(rendered))
	for _, m := range rendered {
		alias := m.ID
		if alias == "" {
			alias = m.ModelID
		}
		if alias == "" || present[alias] {
			continue
		}
		present[alias] = true
		aliases = append(aliases, alias)
	}
	for _, choice := range CuratedAlbertModels() {
		// An AlbertModelConfig carries the curated alias in ID and the API model
		// id in ModelID (AlbertModelChoice.ID is the API id), so the alias is
		// what a curated choice has to be matched on.
		if choice.Default && present[choice.OpenCodeID] {
			return choice.Ref()
		}
	}
	if len(aliases) == 0 {
		return ""
	}
	sort.Strings(aliases)
	return "albert/" + aliases[0]
}

// piEffectiveOverlay applies Pi's own defaults to the harness-independent
// overlay. The difference from OpenCode is the fallback model. The decision is
// taken on the *raw* selection, so a project that named a model keeps it —
// including one Pi refuses, which ComposePiModelsConfig reports rather than
// rewrites.
//
// A project that named no model is a case the launch no longer reaches empty:
// init writes the fallback it showed in its recap into the manifest, so the
// model the recap promised is the one that launches (#347 review). This branch
// covers the projects that predate that — a manifest with no model — and the
// harness-independent callers that pass a bare overlay.
func piEffectiveOverlay(o ManagedOverlay) ManagedOverlay {
	selected := strings.TrimSpace(o.Model)
	effective := o.EffectiveOverlay()
	if selected == "" {
		if model := piFallbackModelRef(o); model != "" {
			effective.Model = model
		}
	}
	return effective
}

// ComposePiModelsConfig renders the managed overlay as Pi's models.json. The
// document is deterministic (sorted providers, sorted models) because it is
// compared and diffed: the same overlay must always produce the same bytes.
func ComposePiModelsConfig(o ManagedOverlay) (string, error) {
	// A project that named a model Pi cannot run is refused here rather than
	// launched on another one: Pi has no merged configuration to fall back on,
	// so the alternative would be a session silently running a model nobody
	// chose (#347).
	if selected := strings.TrimSpace(o.Model); selected != "" {
		if reason := PiUnsupportedModelReason(selected); reason != "" {
			return "", fmt.Errorf("%s. Ce modèle est écarté pour Pi : choisissez un autre modèle principal, ou OpenCode", reason)
		}
	}
	o = piEffectiveOverlay(o)
	providers := piProviders(o)
	if len(providers) == 0 {
		return "", fmt.Errorf("no inference platform to configure for Pi")
	}
	if err := validatePiModelPlatform(o, providers); err != nil {
		return "", err
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

// validatePiModelPlatform refuses a launch whose selected model belongs to a
// platform this run does not configure. Pi resolves the model on the command
// line and nothing else, so a selection pointing at an unconnected platform
// starts a session that fails on its first request with the provider's own
// error; naming the selection, the platform and the configured ones here keeps
// the failure actionable. A known platform is not a promise the model exists:
// the catalogue is fetched live and a project may legitimately name a model it
// did not list.
func validatePiModelPlatform(o ManagedOverlay, providers map[string]piProvider) error {
	ref := strings.TrimSpace(o.Model)
	if ref == "" {
		return nil
	}
	kind, _, ok := strings.Cut(ref, "/")
	if !ok || kind == "" {
		return nil
	}
	if _, configured := providers[kind]; configured {
		return nil
	}
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return fmt.Errorf("le modèle %q appartient à la plateforme %q, qui n'est pas configurée pour ce projet ; plateformes configurées : %s. Choisissez un modèle de l'une d'elles (just-code init --model …), ou retirez le modèle du manifeste pour prendre celui de la plateforme connectée", ref, kind, strings.Join(names, ", "))
}

// piProviders builds the provider blocks of models.json. A platform appears only
// when it has at least one model to advertise: a provider entry with an empty
// model list would give Pi a choice it cannot honour.
func piProviders(o ManagedOverlay) map[string]piProvider {
	providers := map[string]piProvider{}
	if piAlbertRendered(o) {
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
	return providers
}

// piAlbertModels resolves the Albert model set a Pi configuration must
// describe. The overlay may legitimately carry none — the CLI passes an empty
// slice when nothing beyond the embedded defaults is selected, which for
// OpenCode means "use the embedded asset". Pi has no embedded asset, so the
// curated defaults fill in; without them the guest would start with no
// provider at all on the default path. Entries the harness must not offer are
// dropped from either source, so a selection that reaches a launch and the set
// the guest is given cannot disagree.
func piAlbertModels(o ManagedOverlay) []AlbertModelConfig {
	configured := o.Models
	if len(configured) == 0 {
		for _, choice := range CuratedAlbertModels() {
			if choice.Default {
				configured = append(configured, choice.AlbertModelConfig())
			}
		}
	}
	models := make([]AlbertModelConfig, 0, len(configured))
	for _, m := range configured {
		if PiUnsupportedModelReason(m.ID) != "" || PiUnsupportedModelReason(m.ModelID) != "" {
			continue
		}
		models = append(models, m)
	}
	return models
}

// piModelFromAlbert maps a curated Albert model to a Pi entry. The curated ID
// is the display alias ("deepseek-v4-flash"); Pi must send the API id, which
// the curated entry carries as ModelID.
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
// Pi expects on --model. The overlay carries curated aliases
// ("albert/deepseek-v4-flash"); Pi has to be given the API id, so the curated
// entry is looked up. An unknown alias is passed through unchanged rather than
// silently replaced: the catalogue is fetched live, and a project may
// legitimately name a model the current catalogue did not list.
func PiModelRef(o ManagedOverlay) string {
	o = piEffectiveOverlay(o)
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
