package justcode

import (
	"encoding/json"
	"strings"
	"testing"
)

func albertOverlay() ManagedOverlay {
	return ManagedOverlay{
		Model:         "albert/deepseek-v4-flash",
		AlbertEnabled: true,
		Models: []AlbertModelConfig{
			{ID: "deepseek-v4-flash", ModelID: "deepseek-ai/DeepSeek-V4-Flash-0731", Name: "DeepSeek V4 Flash", Context: 131072},
			{ID: "gemma-4", ModelID: "google/gemma-4-31B-it", Name: "Gemma 4", Context: 262144, Vision: true},
		},
	}
}

// Pi sends the model id it is given. The curated alias ("deepseek-v4-flash") is
// an OpenCode display id, so the API id has to reach Pi or every request fails.
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
	if _, ok := ids["deepseek-ai/DeepSeek-V4-Flash-0731"]; !ok {
		t.Fatalf("the API model id is missing from %v", ids)
	}
	if _, ok := ids["deepseek-v4-flash"]; ok {
		t.Fatal("the curated display alias was sent to the API")
	}
	if m := ids["google/gemma-4-31B-it"]; len(m.Input) != 2 || m.Input[0] != "text" || m.Input[1] != "image" {
		t.Fatalf("vision model inputs = %v, want text and image", m.Input)
	}
	if m := ids["deepseek-ai/DeepSeek-V4-Flash-0731"]; m.ContextWindow != 131072 || m.Name != "DeepSeek V4 Flash" {
		t.Fatalf("deepseek entry = %+v", m)
	}
}

// The curated defaults fill in for a Pi project that selected nothing, and the
// unsupported ones are dropped from that set (#347).
func TestPiDefaultModelIsCuratedAndSupported(t *testing.T) {
	if got := piDefaultModelRef(); got != "albert/deepseek-v4-flash" {
		t.Fatalf("pi default model = %q, want albert/deepseek-v4-flash", got)
	}
	if reason := PiUnsupportedModelReason(piDefaultModelRef()); reason != "" {
		t.Fatalf("the pi default model is refused by its own rule: %s", reason)
	}
	got, err := ComposePiModelsConfig(ManagedOverlay{AlbertEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "Qwen/Qwen3-Coder-30B-A3B-Instruct") {
		t.Fatalf("the dropped model still reaches the guest:\n%s", got)
	}
	if !strings.Contains(got, "deepseek-ai/DeepSeek-V4-Flash-0731") {
		t.Fatalf("the curated defaults are missing from:\n%s", got)
	}
	// The alias form the overlay uses resolves to the same decision as the API
	// id the catalogue answers with.
	if PiUnsupportedModelReason("albert/qwen3-coder") == "" || PiUnsupportedModelReason("Qwen/Qwen3-Coder-30B-A3B-Instruct") == "" {
		t.Fatal("qwen3-coder must be recognised by alias and by model id")
	}
}

// An explicit selection Pi cannot run is refused, not silently replaced: the
// project asked for that model, and a launch on another one would hide it
// (#347).
func TestComposePiModelsConfigRefusesUnsupportedSelection(t *testing.T) {
	overlay := albertOverlay()
	overlay.Model = "albert/qwen3-coder"
	overlay.Models = append(overlay.Models, AlbertModelConfig{ID: "qwen3-coder", ModelID: "Qwen/Qwen3-Coder-30B-A3B-Instruct", Name: "Qwen3 Coder"})
	_, err := ComposePiModelsConfig(overlay)
	if err == nil {
		t.Fatal("a model Pi cannot run was accepted")
	}
	if !strings.Contains(err.Error(), "écarté pour Pi") || !strings.Contains(err.Error(), "Qwen3 Coder 30B") {
		t.Fatalf("refusal = %v, want it to name the model and the harness", err)
	}
	// The overlay itself is left alone: the refusal changes nothing on disk.
	if overlay.Model != "albert/qwen3-coder" {
		t.Fatalf("composing rewrote the selection to %q", overlay.Model)
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
	// The overlay selects the curated alias; Pi has to be handed the API id of
	// that same model, or the request fails on an id the API does not know.
	if got := PiModelRef(overlay); got != "albert/deepseek-ai/DeepSeek-V4-Flash-0731" {
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
	// An overlay that names no model at all resolves through the effective
	// default, and for Pi that default is the curated model it can run.
	if got := PiModelRef(ManagedOverlay{AlbertEnabled: true}); got != "albert/deepseek-ai/DeepSeek-V4-Flash-0731" {
		t.Fatalf("default overlay produced %q", got)
	}
}

// The models a project renders decide its fallback: a project connected to
// another platform must not be handed an Albert reference its own models.json
// does not contain (#347 review).
func TestPiFallbackFollowsTheRenderedModels(t *testing.T) {
	mistralOnly := ManagedOverlay{
		Providers: []ProviderOverlay{{
			Kind:      "mistral",
			Name:      "Mistral AI",
			BaseURL:   "https://api.mistral.ai/v1",
			APIKeyEnv: MistralAPIKeyEnv,
			Models:    []ProviderModelConfig{{ID: "mistral-medium-3", ModelID: "mistral-medium-3-5", Name: "Mistral Medium"}},
		}},
	}
	if got := piFallbackModelRef(mistralOnly); got != "" {
		t.Fatalf("a project with no Albert models fell back to %q", got)
	}
	got, err := ComposePiModelsConfig(mistralOnly)
	if err != nil {
		t.Fatal(err)
	}
	var doc piModelsFile
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Providers["albert"]; ok {
		t.Fatalf("an Albert provider was written for a Mistral-only project:\n%s", got)
	}
	if _, ok := doc.Providers["mistral"]; !ok {
		t.Fatalf("no mistral provider in:\n%s", got)
	}

	// Albert connected, but the project listed a model set that holds none of
	// the curated defaults: the fallback is one of the models it did list, and
	// PiModelRef still resolves its alias to the id the API knows.
	gemmaOnly := ManagedOverlay{
		AlbertEnabled: true,
		Models:        []AlbertModelConfig{{ID: "gemma-4", ModelID: "google/gemma-4-31B-it", Name: "Gemma 4"}},
	}
	if got := piFallbackModelRef(gemmaOnly); got != "albert/gemma-4" {
		t.Fatalf("explicit list fallback = %q", got)
	}
	if got := PiModelRef(gemmaOnly); got != "albert/google/gemma-4-31B-it" {
		t.Fatalf("PiModelRef() = %q", got)
	}

	// A curated default is preferred over that first-entry rule, so the default
	// path keeps starting on the model the init recap showed.
	if got := piFallbackModelRef(albertOverlay()); got != "albert/deepseek-v4-flash" {
		t.Fatalf("curated default was not preferred: %q", got)
	}
}

// A selection that belongs to a platform this run does not configure is refused
// with the platforms that are configured: Pi resolves the model on the command
// line alone, so the alternative is a session that fails on its first request
// (#347 review).
func TestPiRefusesAModelFromAnUnconfiguredPlatform(t *testing.T) {
	overlay := ManagedOverlay{
		Model: "albert/deepseek-v4-flash",
		Providers: []ProviderOverlay{{
			Kind:      "mistral",
			BaseURL:   "https://api.mistral.ai/v1",
			APIKeyEnv: MistralAPIKeyEnv,
			Models:    []ProviderModelConfig{{ID: "mistral-medium-3", ModelID: "mistral-medium-3-5"}},
		}},
	}
	_, err := ComposePiModelsConfig(overlay)
	if err == nil {
		t.Fatal("a model from an unconfigured platform was accepted")
	}
	for _, want := range []string{"albert/deepseek-v4-flash", "mistral"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal = %v, want it to name %q", err, want)
		}
	}
	// The same selection is fine once Albert is one of the connected platforms:
	// a known platform is not a promise that the model exists in a catalogue
	// fetched live.
	overlay.Models = albertOverlay().Models
	overlay.AlbertEnabled = true
	if _, err := ComposePiModelsConfig(overlay); err != nil {
		t.Fatalf("Albert is configured but its model was refused: %v", err)
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
	if !strings.Contains(script, "npm install --global --prefix") {
		t.Fatalf("install script = %s", script)
	}
	// The install runs as the guest user, and the managed agent-vm template
	// leaves npm's global prefix root-owned: a plain global install fails there
	// with EACCES and Pi never launches. The prefix must stay user-owned.
	if !strings.Contains(script, `--prefix "$HOME/.local"`) {
		t.Fatalf("install script does not use a user-owned prefix: %s", script)
	}
	if strings.Contains(script, "npm install -g ") {
		t.Fatalf("install script still installs into the root-owned global prefix: %s", script)
	}
	// The fragment is run through `sh -c`, so a failure has to be an exit code.
	if !strings.Contains(script, "|| exit 1") {
		t.Fatalf("install script swallows a failed install: %s", script)
	}
}

// The install lands in a user-owned prefix, so the launch has to put that
// prefix on the guest PATH: nothing else does, and `exec pi` would otherwise
// not find the binary it just installed.
func TestPiAgentLaunchAddsTheInstallPrefixToPath(t *testing.T) {
	launch := piAgentLaunch("/tmp/just-code-agent.env", "/workspace", nil)
	if !strings.Contains(launch, "PATH="+piGuestBinDir+":$PATH") {
		t.Fatalf("launch does not search the install prefix: %s", launch)
	}
	if !strings.Contains(launch, "exec pi") {
		t.Fatalf("launch does not exec pi: %s", launch)
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
