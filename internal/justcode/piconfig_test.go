package justcode

import (
	"encoding/json"
	"strings"
	"testing"
)

func albertOverlay() ManagedOverlay {
	return ManagedOverlay{
		Model:         "albert/qwen3-coder",
		AlbertEnabled: true,
		Models: []AlbertModelConfig{
			{ID: "qwen3-coder", ModelID: "Qwen/Qwen3-Coder-30B-A3B-Instruct", Name: "Qwen3 Coder", Context: 262144},
			{ID: "gemma-4", ModelID: "google/gemma-4-31B-it", Name: "Gemma 4", Context: 262144, Vision: true},
		},
	}
}

// Pi sends the model id it is given. The curated alias ("qwen3-coder") is an
// OpenCode display id, so the API id has to reach Pi or every request fails.
func TestComposePiModelsConfigUsesAPIIDs(t *testing.T) {
	got, err := ComposePiModelsConfig(albertOverlay())
	if err != nil {
		t.Fatal(err)
	}
	var doc piModelsFile
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("composed document is not valid JSON: %v\n%s", err, got)
	}
	albert, ok := doc.Providers["albert"]
	if !ok {
		t.Fatalf("no albert provider in %s", got)
	}
	if albert.BaseURL != albertBaseURL {
		t.Fatalf("baseUrl = %q, want %q", albert.BaseURL, albertBaseURL)
	}
	// The key is named, never written: the guest holds the value (or, under the
	// Microsandbox proxy, only its placeholder).
	if albert.APIKey != msbAPISecretEnv {
		t.Fatalf("apiKey = %q, want the environment variable name %q", albert.APIKey, msbAPISecretEnv)
	}
	ids := map[string]piModel{}
	for _, m := range albert.Models {
		ids[m.ID] = m
	}
	if _, ok := ids["Qwen/Qwen3-Coder-30B-A3B-Instruct"]; !ok {
		t.Fatalf("the API model id is missing from %v", ids)
	}
	if _, ok := ids["qwen3-coder"]; ok {
		t.Fatal("the curated display alias was sent to the API")
	}
	if m := ids["google/gemma-4-31B-it"]; len(m.Input) != 2 || m.Input[0] != "text" || m.Input[1] != "image" {
		t.Fatalf("vision model inputs = %v, want text and image", m.Input)
	}
	if m := ids["Qwen/Qwen3-Coder-30B-A3B-Instruct"]; m.ContextWindow != 262144 || m.Name != "Qwen3 Coder" {
		t.Fatalf("qwen entry = %+v", m)
	}
}

func TestComposePiModelsConfigNonAlbertProviders(t *testing.T) {
	overlay := ManagedOverlay{
		Model: "mistral/mistral-medium-3",
		Providers: []ProviderOverlay{{
			Kind:      "mistral",
			Name:      "Mistral AI",
			BaseURL:   "https://api.mistral.ai/v1",
			APIKeyEnv: MistralAPIKeyEnv,
			Models:    []ProviderModelConfig{{ID: "mistral-medium-3", ModelID: "mistral-medium-3-5", Name: "Mistral Medium", Context: 128000, Output: 32768}},
		}},
	}
	got, err := ComposePiModelsConfig(overlay)
	if err != nil {
		t.Fatal(err)
	}
	var doc piModelsFile
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatal(err)
	}
	mistral, ok := doc.Providers["mistral"]
	if !ok {
		t.Fatalf("no mistral provider in %s", got)
	}
	if mistral.APIKey != MistralAPIKeyEnv || mistral.API != piAPI {
		t.Fatalf("mistral provider = %+v", mistral)
	}
	if len(mistral.Models) != 1 || mistral.Models[0].ID != "mistral-medium-3-5" || mistral.Models[0].MaxTokens != 32768 {
		t.Fatalf("mistral models = %+v", mistral.Models)
	}
}

// The document is compared and diffed, so the same overlay must always produce
// the same bytes whatever order the catalogue returned its models in.
func TestComposePiModelsConfigIsDeterministic(t *testing.T) {
	first, err := ComposePiModelsConfig(albertOverlay())
	if err != nil {
		t.Fatal(err)
	}
	shuffled := albertOverlay()
	shuffled.Models[0], shuffled.Models[1] = shuffled.Models[1], shuffled.Models[0]
	second, err := ComposePiModelsConfig(shuffled)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("model order changed the document:\n%s\n%s", first, second)
	}
}

// Nothing to configure is a configuration problem the caller must see, not an
// empty models.json that leaves Pi without any provider.
func TestComposePiModelsConfigRequiresAPlatform(t *testing.T) {
	if _, err := ComposePiModelsConfig(ManagedOverlay{}); err == nil {
		t.Fatal("an overlay with no inference platform produced a document")
	}
}

func TestPiModelRefResolvesTheAPIID(t *testing.T) {
	overlay := albertOverlay()
	if got := PiModelRef(overlay); got != "albert/Qwen/Qwen3-Coder-30B-A3B-Instruct" {
		t.Fatalf("PiModelRef() = %q", got)
	}
	// A model the current catalogue did not list is passed through: the
	// selection may name something the catalogue fetch could not see.
	unknown := ManagedOverlay{Model: "albert/not-in-catalogue"}
	if got := PiModelRef(unknown); got != "albert/not-in-catalogue" {
		t.Fatalf("unknown alias = %q", got)
	}
	provider := ManagedOverlay{
		Model: "openai-compatible/alias-model",
		Providers: []ProviderOverlay{{
			Kind:   "openai-compatible",
			Models: []ProviderModelConfig{{ID: "alias-model", ModelID: "vendor/real-model"}},
		}},
	}
	if got := PiModelRef(provider); got != "openai-compatible/vendor/real-model" {
		t.Fatalf("provider model ref = %q", got)
	}
	// An overlay that names no model at all still resolves: the effective
	// overlay applies the default, and the default is a curated Albert model.
	if got := PiModelRef(ManagedOverlay{}); got != "albert/Qwen/Qwen3-Coder-30B-A3B-Instruct" {
		t.Fatalf("default overlay produced %q", got)
	}
}

func TestComposePiSettingsDisablesInstallTelemetry(t *testing.T) {
	got, err := ComposePiSettingsConfig()
	if err != nil {
		t.Fatal(err)
	}
	var settings piSettingsFile
	if err := json.Unmarshal([]byte(got), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.EnableInstallTelemetry {
		t.Fatal("install telemetry is still enabled in a managed guest")
	}
}

// The configuration travels the guest as shell text. A quote inside a model
// name must not be able to end the argument and turn the rest into commands.
func TestPiGuestConfigScriptQuotesTheJSON(t *testing.T) {
	overlay := albertOverlay()
	overlay.Models[0].Name = "Qwen's coder"
	script, err := piGuestConfigScript(overlay)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, `"$HOME/.pi/agent/models.json"`) || !strings.Contains(script, `"$HOME/.pi/agent/settings.json"`) {
		t.Fatalf("script does not write both managed files:\n%s", script)
	}
	// shellQuote turns ' into '\''; the unescaped form must not survive.
	if strings.Contains(script, `Qwen's coder`) {
		t.Fatalf("an unescaped quote reached the shell text:\n%s", script)
	}
	if !strings.Contains(script, `Qwen'\''s coder`) {
		t.Fatalf("the quote was not escaped as expected:\n%s", script)
	}
}

func TestPiGuestInstallScriptPinsTheRelease(t *testing.T) {
	script := piGuestInstallScript()
	// The package and the version are quoted separately by shellQuote, so the
	// check is on the two values, not on their concatenation.
	if !strings.Contains(script, PiPackage) || !strings.Contains(script, PiVersion) {
		t.Fatalf("install script does not pin the release: %s", script)
	}
	if !strings.Contains(script, "npm install -g") {
		t.Fatalf("install script = %s", script)
	}
}

func TestPiResumeArgs(t *testing.T) {
	if got := piResumeArgs(false); len(got) != 0 {
		t.Fatalf("a guest without sessions must start fresh, got %v", got)
	}
	if got := piResumeArgs(true); len(got) != 1 || got[0] != "--continue" {
		t.Fatalf("resume args = %v", got)
	}
}
