package codebox

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// OpenCodeVersion is the managed v2 CLI release (npm package @opencode/cli).
const OpenCodeVersion = "2.2.5"

// This file implements the P10 OpenCode configuration composer. Per
// docs/decisions/2026-09-23-opencode-configuration-contract.md (D-001):
//
//   - OPENCODE_CONFIG_CONTENT is OpenCode's official final-merge mechanism:
//     the inline content deep-merges on top of the user global and project
//     configs, so the managed overlay must carry ONLY managed fields.
//   - OpenCode silently ignores unknown top-level keys, so codebox must
//     detect managed-field conflicts itself, before overriding user settings.
//   - User JSONC files are never rewritten; the managed layer lives in the
//     inline overlay exclusively.

// ManagedOverlay is the set of OpenCode configuration fields codebox
// manages. Only these fields may appear in the overlay: every field placed
// here overrides the same field from the project and user configs (final
// merge wins), so an unconsidered addition silently disables user settings.
// Selected curated MCP entries are added separately by the catalogue.
type ManagedOverlay struct {
	// Model is the effective model ("provider/model-id"). Empty keeps the
	// embedded default.
	Model string
	// SmallModel is the model for background tasks. Defaults to Model.
	SmallModel string
	// Models replaces the managed Albert provider model map when non-empty.
	Models []AlbertModelConfig
	// Providers describes the managed non-Albert inference platforms. Albert
	// stays in Models so settings and overlays written before #180 keep their
	// meaning; when both are empty the embedded Albert configuration is used
	// unchanged.
	Providers []ProviderOverlay
	// AlbertEnabled records that Albert API is one of the connected
	// platforms. When another provider is present and this is false, the
	// embedded Albert block is removed: advertising models whose key is not
	// bound in the guest would produce broken choices.
	AlbertEnabled bool
	// MCPConnectors selects managed remote and guest-local entries. User-owned
	// entries with other names remain untouched.
	MCPConnectors []string
}

// ProviderModelConfig is one model exposed by a managed non-Albert provider.
// Unlike an Albert choice it comes from the platform's raw catalogue, so it
// carries no curated display description.
type ProviderModelConfig struct {
	ID      string `json:"id"`
	ModelID string `json:"modelID,omitempty"`
	Name    string `json:"name"`
	Context int    `json:"context,omitempty"`
	Output  int    `json:"output,omitempty"`
}

// ProviderOverlay is a fully described managed provider: the OpenCode
// provider key, its endpoint and the environment variable holding the key.
// The credential value itself never enters the overlay.
type ProviderOverlay struct {
	Kind      string                `json:"kind"`
	Name      string                `json:"name"`
	BaseURL   string                `json:"baseURL"`
	APIKeyEnv string                `json:"apiKeyEnv"`
	Models    []ProviderModelConfig `json:"models"`
}

// AlbertModelConfig is one curated model exposed by the managed Albert provider.
type AlbertModelConfig struct {
	ID          string         `json:"id"`
	ModelID     string         `json:"modelID,omitempty"`
	Name        string         `json:"name"`
	Context     int            `json:"context,omitempty"`
	Output      int            `json:"output,omitempty"`
	RequestBody map[string]any `json:"requestBody,omitempty"`
	Vision      bool           `json:"vision,omitempty"`
}

// opencodeManagedField describes one top-level field the overlay may set.
type opencodeManagedField struct {
	name  string
	value func(o ManagedOverlay) string
}

// managedOverlayFields lists the scalar managed fields in a stable order.
// Curated MCP entries are checked separately against the connector catalogue.
var managedOverlayFields = []opencodeManagedField{
	{"model", func(o ManagedOverlay) string { return o.Model }},
	{"small_model", func(o ManagedOverlay) string { return o.SmallModel }},
}

// EffectiveOverlay returns the overlay with defaults applied: the embedded
// model when none is chosen, and SmallModel following Model.
func (o ManagedOverlay) EffectiveOverlay() ManagedOverlay {
	if o.Model == "" {
		o.Model = o.firstProviderModel()
	}
	if o.SmallModel == "" {
		o.SmallModel = o.Model
	}
	return o
}

// firstProviderModel is the fallback model when nothing is selected: the
// embedded Albert default while Albert is the managed provider, otherwise the
// first model of the first configured platform.
func (o ManagedOverlay) firstProviderModel() string {
	if len(o.Models) > 0 || len(o.Providers) == 0 {
		return defaultOpencodeModel
	}
	for _, p := range o.Providers {
		for _, m := range p.Models {
			return p.Kind + "/" + m.ID
		}
	}
	return defaultOpencodeModel
}

// defaultOpencodeModel mirrors assets/opencode-config.json's "model" field.
// The embedded asset stays the baseline; this constant is what the composer
// writes when the user selected nothing. DeepSeek V4 Flash is the tool-call
// reliable default; qwen3-coder stays available as an explicit choice (#347).
const defaultOpencodeModel = "albert/deepseek-v4-flash"

// ComposeConfigContent builds the full OPENCODE_CONFIG_CONTENT from the
// embedded base and the managed overlay by deep-merging them host-side: the
// base's provider block and permissions survive, and the overlay's managed
// fields override their base counterparts. This keeps one document (no
// reliance on OpenCode parsing multiple concatenated documents) while the
// project and user configs still merge underneath at load time.
func ComposeConfigContent(o ManagedOverlay) (string, error) {
	var base map[string]any
	if err := json.Unmarshal([]byte(opencodeConfigContent), &base); err != nil {
		return "", fmt.Errorf("parse embedded OpenCode config: %w", err)
	}
	overlayJSON, err := renderOverlayJSON(o.EffectiveOverlay())
	if err != nil {
		return "", err
	}
	var overlay map[string]any
	if err := json.Unmarshal([]byte(overlayJSON), &overlay); err != nil {
		return "", fmt.Errorf("parse rendered overlay: %w", err)
	}
	// The embedded base always carries an Albert provider. When this run has
	// no Albert models, the platform is not connected: leaving the base entry
	// would advertise models whose key is not bound in the guest.
	if !o.AlbertEnabled && len(o.Providers) > 0 {
		if provider, ok := base["providers"].(map[string]any); ok {
			delete(provider, "albert")
		}
	}
	if len(o.Models) > 0 {
		provider, _ := base["providers"].(map[string]any)
		albert, _ := provider["albert"].(map[string]any)
		if albert != nil {
			albert["models"] = map[string]any{}
		}
	}
	merged := deepMergeMaps(base, overlay)
	out, err := json.Marshal(merged)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// deepMergeMaps merges src over dst recursively, map values merging and all
// other values replacing. Neither input is mutated.
func deepMergeMaps(dst, src map[string]any) map[string]any {
	out := make(map[string]any, len(dst)+len(src))
	for k, v := range dst {
		out[k] = v
	}
	for k, v := range src {
		if srcMap, ok := v.(map[string]any); ok {
			if dstMap, ok := out[k].(map[string]any); ok {
				out[k] = deepMergeMaps(dstMap, srcMap)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// completeModelLimit returns an OpenCode limit only when both values are
// known. OpenCode rejects a partial limit object; unknown metadata therefore
// omits the whole object and leaves OpenCode's defaults in effect.
func completeModelLimit(context, output int) map[string]any {
	if context <= 0 || output <= 0 {
		return nil
	}
	return map[string]any{"context": context, "output": output}
}

// renderOverlayJSON renders the managed overlay as the JSON injected into
// OPENCODE_CONFIG_CONTENT. It carries managed fields ONLY — the provider
// block, permissions and schema come from the embedded asset, which remains
// the base configuration; this overlay merges on top of it.
func renderOverlayJSON(o ManagedOverlay) (string, error) {
	fields := map[string]any{}
	for _, f := range managedOverlayFields {
		if v := f.value(o); v != "" {
			fields[f.name] = v
		}
	}
	if len(o.Models) > 0 {
		models := make(map[string]any, len(o.Models))
		for _, model := range o.Models {
			definition := map[string]any{"name": model.Name}
			if model.ModelID != "" {
				definition["modelID"] = model.ModelID
			}
			inputs := []string{"text"}
			if model.Vision {
				inputs = append(inputs, "image")
			}
			definition["capabilities"] = map[string]any{
				"tools":  true,
				"input":  inputs,
				"output": []string{"text"},
			}
			if limit := completeModelLimit(model.Context, model.Output); limit != nil {
				definition["limit"] = limit
			}
			if len(model.RequestBody) > 0 {
				definition["body"] = model.RequestBody
			}
			models[model.ID] = definition
		}
		fields["providers"] = map[string]any{"albert": map[string]any{"models": models}}
	}
	if len(o.Providers) > 0 {
		providers, _ := fields["providers"].(map[string]any)
		if providers == nil {
			providers = map[string]any{}
		}
		for _, provider := range o.Providers {
			if provider.Kind == "" || len(provider.Models) == 0 {
				continue
			}
			models := make(map[string]any, len(provider.Models))
			for _, model := range provider.Models {
				if model.ID == "" {
					continue
				}
				name := model.Name
				if name == "" {
					name = model.ID
				}
				definition := map[string]any{
					"name": name,
					"capabilities": map[string]any{
						"tools":  true,
						"input":  []string{"text"},
						"output": []string{"text"},
					},
				}
				if model.ModelID != "" {
					definition["modelID"] = model.ModelID
				}
				if limit := completeModelLimit(model.Context, model.Output); limit != nil {
					definition["limit"] = limit
				}
				models[model.ID] = definition
			}
			if len(models) == 0 {
				continue
			}
			providers[provider.Kind] = map[string]any{
				"package": "@ai-sdk/openai-compatible",
				"name":    provider.Name,
				"settings": map[string]any{
					"baseURL": provider.BaseURL,
					"apiKey":  "{env:" + provider.APIKeyEnv + "}",
				},
				"models": models,
			}
		}
		if len(providers) > 0 {
			fields["providers"] = providers
		}
	}
	if len(o.MCPConnectors) > 0 {
		mcp, err := managedMCPOverlay(o.MCPConnectors)
		if err != nil {
			return "", err
		}
		fields["mcp"] = mcp
	}
	// Stable key order: the content travels through env files sourced by
	// shells and is diffed in tests; a canonical form avoids noise.
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(",")
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return "", err
		}
		vb, err := json.Marshal(fields[k])
		if err != nil {
			return "", err
		}
		b.Write(kb)
		b.WriteString(":")
		b.Write(vb)
	}
	b.WriteString("}")
	return b.String(), nil
}

// ConfigConflict is one managed field set differently by the project config.
// The project value is the one OpenCode would otherwise use; the overlay
// wins by design (final merge), so the conflict is surfaced, not silently
// applied.
type ConfigConflict struct {
	Field        string
	ProjectValue string
	ManagedValue string
}

// DetectOverlayConflicts compares the managed overlay against a parsed
// project config (any JSON object; JSONC is pre-stripped by the caller). A
// conflict exists when the project sets a managed field to a different value
// than the overlay: the final-merge semantics mean the overlay would
// silently override the project. Unknown top-level keys are ignored —
// OpenCode ignores them too, and they are the user's to manage.
func DetectOverlayConflicts(project map[string]any, overlay ManagedOverlay) []ConfigConflict {
	overlay = overlay.EffectiveOverlay()
	var conflicts []ConfigConflict
	for _, f := range managedOverlayFields {
		managed := f.value(overlay)
		if managed == "" {
			continue
		}
		raw, present := project[f.name]
		if !present {
			continue
		}
		projectValue, ok := raw.(string)
		if !ok {
			// A non-string value in a string field is a project misshape the
			// overlay would replace; report it with its raw JSON.
			rawJSON, _ := json.Marshal(raw)
			projectValue = string(rawJSON)
		}
		if projectValue != managed {
			conflicts = append(conflicts, ConfigConflict{
				Field:        f.name,
				ProjectValue: projectValue,
				ManagedValue: managed,
			})
		}
	}
	managedMCP, err := managedMCPOverlay(overlay.MCPConnectors)
	if err == nil {
		projectMCP, _ := project["mcp"].(map[string]any)
		if native, ok := projectMCP["servers"].(map[string]any); ok {
			projectMCP = deepMergeMaps(projectMCP, native)
		}
		for name, managed := range managedMCP {
			if projectValue, exists := projectMCP[name]; exists && canonicalMCPEntry(projectValue) != canonicalMCPEntry(managed) {
				conflicts = append(conflicts, ConfigConflict{Field: "mcp." + name, ProjectValue: canonicalMCPEntry(projectValue), ManagedValue: canonicalMCPEntry(managed)})
			}
		}
	}
	return conflicts
}

// FormatConflicts renders the conflict list as user-facing lines, one per
// field, with both values named. The overlay still wins (final merge), so
// the message states that rather than implying the project was preserved.
func FormatConflicts(conflicts []ConfigConflict) string {
	if len(conflicts) == 0 {
		return ""
	}
	sorted := append([]ConfigConflict(nil), conflicts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Field < sorted[j].Field })
	var lines []string
	for _, c := range sorted {
		lines = append(lines, fmt.Sprintf("  %s: project=%s, codebox=%s (the codebox value wins; set the project field to match or change the managed selection)", c.Field, c.ProjectValue, c.ManagedValue))
	}
	return strings.Join(lines, "\n")
}

// OpenCodeDisableUpdateEnv turns off OpenCode's update check in a process.
//
// The managed guest CLI and host connection client must remain on the tested
// release. In v2.2.5 packages/cli/src/services/updater.ts checks this variable
// before automatic update discovery; a legacy inline `autoupdate` field is not
// the process-level control. Carry it in both environments and sourced secret
// files, without rewriting the user's global config. One-shot --help/--version
// probes do not start the updater. A bare codebox shell does not source the
// managed environment; deliberate manual upgrades remain the user's choice.
const OpenCodeDisableUpdateEnv = "OPENCODE_DISABLE_AUTOUPDATE"

// OpenCodeDisableUpdateAssignment is the KEY=VALUE form for exec environments
// and sourced secrets files.
const OpenCodeDisableUpdateAssignment = OpenCodeDisableUpdateEnv + "=1"
