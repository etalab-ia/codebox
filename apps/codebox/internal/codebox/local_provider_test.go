package codebox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	msb "github.com/superradcompany/microsandbox/sdk/go"
)

func TestResolveLocalProviderValidation(t *testing.T) {
	p, err := ResolveProvider(ProviderSettings{
		Kind:    ProviderLocal,
		Name:    "Ollama",
		BaseURL: "http://127.0.0.1:11434/v1/",
	})
	if err != nil {
		t.Fatalf("ResolveProvider local: %v", err)
	}
	if p.BaseURL != "http://localhost:11434/v1" || p.LocalPort != 11434 || !p.NoKey || p.Host != LocalModelHost || p.APIKeyEnv != "" {
		t.Fatalf("resolved local provider = %+v", p)
	}
	if got := LocalGuestBaseURL(p.LocalPort); got != "http://host.microsandbox.internal:11434/v1" {
		t.Fatalf("LocalGuestBaseURL = %q", got)
	}

	// Default display name when omitted.
	unnamed, err := ResolveProvider(ProviderSettings{Kind: ProviderLocal, BaseURL: "http://localhost:1234/v1"})
	if err != nil || unnamed.Name != "Serveur local" || unnamed.LocalPort != 1234 {
		t.Fatalf("unnamed local provider = %+v, err=%v", unnamed, err)
	}

	// Reject remote hosts, https, missing port, wrong path, credentials, query.
	for _, bad := range []string{
		"",
		"https://localhost:11434/v1",
		"http://example.com:11434/v1",
		"http://localhost/v1",
		"http://localhost:99999/v1",
		"http://localhost:11434",
		"http://user:pass@localhost:11434/v1",
		"http://localhost:11434/v1?x=1",
	} {
		if _, err := ResolveProvider(ProviderSettings{Kind: ProviderLocal, BaseURL: bad}); err == nil {
			t.Errorf("expected error for invalid local baseURL %q", bad)
		}
	}
}

func TestResolveProvidersRequiresRemoteAlongsideLocal(t *testing.T) {
	// Local alone is refused: main agent model must stay on a remote provider.
	if _, err := ResolveProviders([]ProviderSettings{
		{Kind: ProviderLocal, BaseURL: "http://localhost:11434/v1"},
	}); err == nil {
		t.Fatal("local provider alone must be rejected")
	}

	// Local with a credentialRef is refused.
	if _, err := ResolveProviders([]ProviderSettings{
		{Kind: ProviderAlbert},
		{Kind: ProviderLocal, BaseURL: "http://localhost:11434/v1", CredentialRef: "local:key"},
	}); err == nil {
		t.Fatal("local provider with credentialRef must be rejected")
	}

	// Albert + OpenAI-compatible + Local all coexist in one project.
	resolved, err := ResolveProviders([]ProviderSettings{
		{Kind: ProviderAlbert},
		{Kind: ProviderOpenAICompatible, Name: "Scaleway", BaseURL: "https://api.scaleway.ai/v1"},
		{Kind: ProviderLocal, Name: "Ollama", BaseURL: "http://localhost:11434/v1"},
	})
	if err != nil {
		t.Fatalf("ResolveProviders with local: %v", err)
	}
	if len(resolved) != 3 || localModelPortFor(resolved) != 11434 {
		t.Fatalf("resolved = %+v, port = %d", resolved, localModelPortFor(resolved))
	}
	// Secret proxy bindings skip keyless local provider.
	for _, b := range msbSecretBindingsFor(resolved) {
		if b.Kind == "" || b.GuestEnv == "" || b.AllowHosts[0] == LocalModelHost {
			t.Fatalf("local provider must not produce a secret binding: %+v", b)
		}
	}
}

func TestLocalProviderRefusedOnNonMicrosandboxRuntimes(t *testing.T) {
	resolved, err := ResolveProviders([]ProviderSettings{
		{Kind: ProviderAlbert},
		{Kind: ProviderLocal, BaseURL: "http://localhost:11434/v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ResolveProviderKeysWith(func(context.Context, CredentialKind, string) (string, string, error) {
		return "dummy", "env", nil
	}, context.Background(), Config{Providers: resolved}, "")
	if err == nil || !strings.Contains(err.Error(), "Microsandbox") {
		t.Fatalf("non-Microsandbox ResolveProviderKeysWith = %v, want Microsandbox-only error", err)
	}
}

func TestMSBNetworkConfigAllowsOnlyLocalModelHostAndPort(t *testing.T) {
	// Without local server: no host.microsandbox.internal rule.
	plain := msbNetworkConfig(0)
	for _, r := range plain.Rules {
		if r.Destination == LocalModelHost {
			t.Fatalf("unexpected local model rule without local server: %+v", r)
		}
	}

	// With local server on 11434: allows ONLY host.microsandbox.internal TCP 11434,
	// never opening the whole "host" group beyond DNS.
	withLocal := msbNetworkConfig(11434)
	found := false
	for _, r := range withLocal.Rules {
		if r.Destination == "host" && r.Port != "53" {
			t.Fatalf("must not open broad host group: %+v", r)
		}
		if r.Destination == LocalModelHost {
			if r.Action != msb.PolicyActionAllow || r.Direction != msb.PolicyDirectionEgress || r.Port != "11434" || len(r.Protocols) != 1 || r.Protocols[0] != msb.PolicyProtocolTCP {
				t.Fatalf("unexpected local model egress rule: %+v", r)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("missing narrow egress rule for %s:11434 in %+v", LocalModelHost, withLocal.Rules)
	}
}

func TestComposeConfigContentLocalSmallModelOnly(t *testing.T) {
	// Valid: main model on Albert, small_model on local.
	content, err := ComposeConfigContent(ManagedOverlay{
		AlbertEnabled: true,
		Model:         "albert/deepseek-v4-flash",
		SmallModel:    "local/qwen2.5:3b",
		Providers: []ProviderOverlay{{
			Kind:    string(ProviderLocal),
			Name:    "Ollama",
			BaseURL: LocalGuestBaseURL(11434),
			Models:  []ProviderModelConfig{{ID: "qwen2.5:3b", Name: "qwen2.5:3b"}},
		}},
	})
	if err != nil {
		t.Fatalf("ComposeConfigContent with local small_model: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["small_model"] != "local/qwen2.5:3b" {
		t.Fatalf("small_model = %v", parsed["small_model"])
	}
	providers, _ := parsed["providers"].(map[string]any)
	localProv, _ := providers["local"].(map[string]any)
	settings, _ := localProv["settings"].(map[string]any)
	if settings["baseURL"] != "http://host.microsandbox.internal:11434/v1" {
		t.Fatalf("local baseURL = %v", settings["baseURL"])
	}
	if _, hasKey := settings["apiKey"]; hasKey {
		t.Fatalf("keyless local provider must omit apiKey, got %v", settings["apiKey"])
	}

	// Refused: local model as main agent model.
	if _, err := ComposeConfigContent(ManagedOverlay{
		AlbertEnabled: true,
		Model:         "local/qwen2.5:3b",
	}); err == nil || !strings.Contains(err.Error(), "small_model") {
		t.Fatalf("local main model = %v, want small_model-only error", err)
	}
}

func TestProbeLocalModelServerWithoutAuthorizationHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("local server probe must not send Authorization header, got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen2.5:3b"},{"id":"llama3.2:1b"}]}`))
	}))
	defer srv.Close()

	models, err := ProbeLocalModelServer(context.Background(), srv.Client(), srv.URL+"/v1")
	if err != nil {
		t.Fatalf("ProbeLocalModelServer: %v", err)
	}
	if len(models) != 2 || models[0] != "llama3.2:1b" || models[1] != "qwen2.5:3b" {
		t.Fatalf("models = %v, want sorted [llama3.2:1b qwen2.5:3b]", models)
	}
}

func TestValidateProjectProvidersRequiresHostApprovalForLocalPort(t *testing.T) {
	projectDockerPort := []ProviderSettings{
		{Kind: ProviderAlbert},
		{Kind: ProviderLocal, Name: "Evil", BaseURL: "http://localhost:2375/v1"},
	}
	// Refused when host has no local server configured.
	if err := ValidateProjectProviders(projectDockerPort, nil); err == nil || !strings.Contains(err.Error(), "2375") {
		t.Fatalf("unapproved local port 2375 with no host local = %v, want refusal naming port 2375", err)
	}
	// Refused when host approved a different local port (11434 != 2375).
	hostOllama := []ProviderSettings{
		{Kind: ProviderAlbert},
		{Kind: ProviderLocal, Name: "Ollama", BaseURL: "http://localhost:11434/v1"},
	}
	if err := ValidateProjectProviders(projectDockerPort, hostOllama); err == nil || !strings.Contains(err.Error(), "2375") {
		t.Fatalf("changed local port 2375 against approved 11434 = %v, want refusal", err)
	}
	// Allowed when host approved the exact same canonical port (127.0.0.1:11434 == localhost:11434).
	projectOllama := []ProviderSettings{
		{Kind: ProviderAlbert},
		{Kind: ProviderLocal, Name: "Ollama", BaseURL: "http://127.0.0.1:11434/v1"},
	}
	if err := ValidateProjectProviders(projectOllama, hostOllama); err != nil {
		t.Fatalf("host-approved local port 11434 must succeed: %v", err)
	}
}

func TestResolveProviderWithHonorsKeylessLocalProvider(t *testing.T) {
	localProv, err := ResolveProvider(ProviderSettings{Kind: ProviderLocal, BaseURL: "http://localhost:11434/v1"})
	if err != nil {
		t.Fatal(err)
	}
	storeCalled := false
	read := func(context.Context, CredentialKind, string) (string, string, error) {
		storeCalled = true
		return "", "", ErrCredentialNotFound
	}
	val, _, _, _, err := resolveProviderWith(read, context.Background(), Config{}, localProv, "")
	if err != nil || val != "" {
		t.Fatalf("resolveProviderWith(local) = (%q, %v), want empty value and nil error", val, err)
	}
	if storeCalled {
		t.Fatal("keyless local provider must not touch the credential store")
	}
}

func TestComposeConfigContentSkipsLocalForDefaultMainModel(t *testing.T) {
	// Even when local appears before a remote provider and Model is empty,
	// EffectiveOverlay must never select a local model as the main agent model.
	content, err := ComposeConfigContent(ManagedOverlay{
		Providers: []ProviderOverlay{
			{
				Kind:    string(ProviderLocal),
				Name:    "Ollama",
				BaseURL: LocalGuestBaseURL(11434),
				Models:  []ProviderModelConfig{{ID: "qwen2.5:3b", Name: "qwen2.5:3b"}},
			},
			{
				Kind:      string(ProviderMistral),
				Name:      "Mistral AI",
				BaseURL:   "https://api.mistral.ai/v1",
				APIKeyEnv: MistralAPIKeyEnv,
				Models:    []ProviderModelConfig{{ID: "mistral-large-latest", Name: "mistral-large-latest"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("ComposeConfigContent: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["model"] != "mistral/mistral-large-latest" {
		t.Fatalf("default main model = %v, want mistral/mistral-large-latest (must skip local)", parsed["model"])
	}
}
