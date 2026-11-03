package justcode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolveProvidersDefaultsToAlbert(t *testing.T) {
	providers, err := ResolveProviders(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 || providers[0].Kind != ProviderAlbert {
		t.Fatalf("empty settings must mean Albert API, got %+v", providers)
	}
	if providers[0].Host != msbAllowHost || providers[0].CatalogueURL != albertCatalogueURL {
		t.Fatalf("Albert endpoint drifted: %+v", providers[0])
	}
}

func TestResolveProvidersBuildsEachPlatform(t *testing.T) {
	providers, err := ResolveProviders([]ProviderSettings{
		{Kind: ProviderAlbert},
		{Kind: ProviderMistral},
		{Kind: ProviderOpenAICompatible, Name: "Scaleway", BaseURL: "https://api.scaleway.ai/v1/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 3 {
		t.Fatalf("providers = %+v", providers)
	}
	if providers[1].Host != "api.mistral.ai" || providers[1].APIKeyEnv != MistralAPIKeyEnv {
		t.Fatalf("Mistral platform = %+v", providers[1])
	}
	if providers[2].BaseURL != "https://api.scaleway.ai/v1" || providers[2].Host != "api.scaleway.ai" || providers[2].APIKeyEnv != OpenAICompatibleAPIKeyEnv {
		t.Fatalf("OpenAI-compatible platform = %+v", providers[2])
	}
	if providers[2].CatalogueURL != "https://api.scaleway.ai/v1/models" {
		t.Fatalf("catalogue URL = %q", providers[2].CatalogueURL)
	}
}

func TestResolveProviderRejectsUnsafeBaseURLs(t *testing.T) {
	cases := []string{
		"http://api.example.com/v1",
		"https://user:pass@api.example.com/v1",
		"https://api.example.com/v1?x=1",
		"https://api.example.com:8443/v1",
		"https://localhost/v1",
		"https://127.0.0.1/v1",
		"https://api.example.com/v1#frag",
	}
	for _, base := range cases {
		if _, err := ResolveProvider(ProviderSettings{Kind: ProviderOpenAICompatible, Name: "X", BaseURL: base}); err == nil {
			t.Fatalf("%q must be refused: the proxy cannot enforce that destination", base)
		}
	}
}

func TestResolveProviderKeepsFixedEndpoints(t *testing.T) {
	if _, err := ResolveProvider(ProviderSettings{Kind: ProviderAlbert, BaseURL: "https://evil.example/v1"}); err == nil {
		t.Fatal("a custom Albert base URL must be refused")
	}
	if _, err := ResolveProvider(ProviderSettings{Kind: ProviderMistral, BaseURL: "https://evil.example/v1"}); err == nil {
		t.Fatal("a custom Mistral base URL must be refused")
	}
}

func TestProviderModelIDsKeepsSlashContainingAlbertIDs(t *testing.T) {
	refs := []string{"openai/gpt-oss-120b", "albert/qwen3-coder", "mistral/mistral-large-latest", "openai-compatible/llama-3.3-70b"}
	got := ProviderModelIDs(refs, ProviderAlbert)
	if !reflect.DeepEqual(got, []string{"openai/gpt-oss-120b", "qwen3-coder"}) {
		t.Fatalf("Albert IDs = %v", got)
	}
	if got := ProviderModelIDs(refs, ProviderMistral); !reflect.DeepEqual(got, []string{"mistral-large-latest"}) {
		t.Fatalf("Mistral IDs = %v", got)
	}
	if got := ProviderModelIDs(refs, ProviderOpenAICompatible); !reflect.DeepEqual(got, []string{"llama-3.3-70b"}) {
		t.Fatalf("OpenAI-compatible IDs = %v", got)
	}
}

func TestValidateProviderKeyDistinguishesOutcomes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" && r.Header.Get("Authorization") == "Bearer good" {
			_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		if r.Header.Get("Authorization") == "Bearer html" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<html>captive portal</html>`))
			return
		}
		if r.Header.Get("Authorization") == "Bearer bad" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	provider := testProviderAt(server.URL)
	if probe := ValidateProviderKey(context.Background(), nil, provider, "good"); probe.Rejected || probe.Unreachable || probe.Incompatible {
		t.Fatalf("valid key probe = %+v", probe)
	}
	if probe := ValidateProviderKey(context.Background(), nil, provider, "bad"); !probe.Rejected {
		t.Fatalf("401 must be a rejection, got %+v", probe)
	}
	if probe := ValidateProviderKey(context.Background(), nil, provider, "html"); !probe.Incompatible {
		t.Fatalf("a non-catalogue 200 must be incompatible, got %+v", probe)
	}
	if probe := ValidateProviderKey(context.Background(), nil, provider, "boom"); !probe.Unreachable || probe.Rejected {
		t.Fatalf("500 must be unreachable, got %+v", probe)
	}
}

// TestValidateProviderKeyClassifiesHTTPFailures pins the #309 taxonomy: an
// absent models route or a redirect is an URL COMPATIBILITY problem, while
// 429/5xx and transport failures stay temporary unavailability, and 401/403
// stays a key rejection.
func TestValidateProviderKeyClassifiesHTTPFailures(t *testing.T) {
	cases := []struct {
		name           string
		status         int
		body           string
		wantRejected   bool
		wantUnreach    bool
		wantIncompat   bool
		wantDetailPart string
	}{
		{name: "404 is incompatible, not a connection problem", status: 404, wantIncompat: true, wantDetailPart: "404"},
		{name: "401 is a rejection", status: 401, wantRejected: true, wantDetailPart: "401"},
		{name: "403 is a rejection", status: 403, wantRejected: true, wantDetailPart: "403"},
		{name: "429 is temporary", status: 429, wantUnreach: true, wantDetailPart: "429"},
		{name: "500 is temporary", status: 500, wantUnreach: true, wantDetailPart: "500"},
		{name: "502 is temporary", status: 502, wantUnreach: true, wantDetailPart: "502"},
		{name: "200 without a data array is incompatible", status: 200, body: `{"models":[]}`, wantIncompat: true, wantDetailPart: "data array"},
		{name: "200 with invalid JSON is incompatible", status: 200, body: `<html>portal</html>`, wantIncompat: true, wantDetailPart: "not a models listing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			probe := ValidateProviderKey(context.Background(), nil, testProviderAt(server.URL), "key")
			if probe.Rejected != tc.wantRejected || probe.Unreachable != tc.wantUnreach || probe.Incompatible != tc.wantIncompat {
				t.Fatalf("probe = %+v, want rejected=%v unreachable=%v incompatible=%v", probe, tc.wantRejected, tc.wantUnreach, tc.wantIncompat)
			}
			if !strings.Contains(probe.Detail, tc.wantDetailPart) {
				t.Fatalf("detail = %q, want it to mention %q", probe.Detail, tc.wantDetailPart)
			}
		})
	}
}

// TestValidateProviderKeyRedirectIsIncompatibleAndNeverReplayed: a redirect
// means a wrong base URL (incompatible), and the credential never reaches
// the redirect target.
func TestValidateProviderKeyRedirectIsIncompatibleAndNeverReplayed(t *testing.T) {
	var reached int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reached, 1)
		_, _ = w.Write([]byte(`{"data":[{"id":"leaked"}]}`))
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	probe := ValidateProviderKey(context.Background(), nil, testProviderAt(redirect.URL), "secret")
	if !probe.Incompatible {
		t.Fatalf("a redirect must be an URL compatibility problem, got %+v", probe)
	}
	if atomic.LoadInt32(&reached) != 0 {
		t.Fatal("the redirect target received the credential")
	}
}

// TestValidateProviderKeyTransportFailuresStayUnreachable: DNS, TLS and
// timeout are temporary unavailability, never an URL compatibility verdict.
func TestValidateProviderKeyTransportFailuresStayUnreachable(t *testing.T) {
	t.Run("DNS failure", func(t *testing.T) {
		probe := ValidateProviderKey(context.Background(), nil, testProviderAt("http://nonexistent.invalid"), "key")
		if !probe.Unreachable || probe.Incompatible {
			t.Fatalf("a DNS failure must be unreachable, got %+v", probe)
		}
	})
	t.Run("TLS failure", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"data":[]}`))
		}))
		defer server.Close()
		// The default client does not trust the test CA: a TLS error.
		probe := ValidateProviderKey(context.Background(), nil, testProviderAt(server.URL), "key")
		if !probe.Unreachable || probe.Incompatible {
			t.Fatalf("a TLS failure must be unreachable, got %+v", probe)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(250 * time.Millisecond)
			_, _ = w.Write([]byte(`{"data":[]}`))
		}))
		defer server.Close()
		client := &http.Client{Timeout: 20 * time.Millisecond}
		probe := ValidateProviderKey(context.Background(), client, testProviderAt(server.URL), "key")
		if !probe.Unreachable || probe.Incompatible {
			t.Fatalf("a timeout must be unreachable, got %+v", probe)
		}
		if !strings.Contains(probe.Detail, "timeout") {
			t.Fatalf("the timeout must be identifiable in the detail: %q", probe.Detail)
		}
	})
}

func TestFetchProviderModelsReturnsRawSortedIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"b-model"},{"id":"a-model"},{"id":"b-model"}]}`))
	}))
	defer server.Close()
	provider := testProviderAt(server.URL)
	models, err := FetchProviderModels(context.Background(), nil, provider, "key")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "a-model" || models[1].ID != "b-model" {
		t.Fatalf("models = %+v", models)
	}
}

func TestFetchCatalogueNeverFollowsRedirects(t *testing.T) {
	var reached int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reached, 1)
		_, _ = w.Write([]byte(`{"data":[{"id":"leaked"}]}`))
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	if _, err := FetchCatalogue(context.Background(), nil, "secret", redirect.URL); err == nil {
		t.Fatal("a redirect must not be followed: the bearer key would be replayed")
	}
	if atomic.LoadInt32(&reached) != 0 {
		t.Fatal("the redirect target received the credential")
	}
}

// testProviderAt builds a platform pointing at an httptest server, which is
// HTTP; ResolveProvider intentionally requires HTTPS for production URLs.
func testProviderAt(base string) InferenceProvider {
	return InferenceProvider{
		Kind: ProviderOpenAICompatible, Name: "Test", BaseURL: base,
		CatalogueURL: base + "/models", CredentialKind: CredentialOpenAICompatible,
		APIKeyEnv: OpenAICompatibleAPIKeyEnv, Host: "api.example.com",
	}
}

func TestComposeConfigContentCarriesEveryConfiguredProvider(t *testing.T) {
	content, err := ComposeConfigContent(ManagedOverlay{
		Model:         "mistral/mistral-large-latest",
		AlbertEnabled: true,
		Models:        []AlbertModelConfig{CuratedAlbertModels()[0].AlbertModelConfig()},
		Providers: []ProviderOverlay{
			{Kind: "mistral", Name: "Mistral AI", BaseURL: "https://api.mistral.ai/v1", APIKeyEnv: MistralAPIKeyEnv,
				Models: []ProviderModelConfig{{ID: "mistral-large-latest", Name: "mistral-large-latest"}}},
			{Kind: "openai-compatible", Name: "Scaleway", BaseURL: "https://api.scaleway.ai/v1", APIKeyEnv: OpenAICompatibleAPIKeyEnv,
				Models: []ProviderModelConfig{{ID: "llama-3.3-70b", Name: "llama-3.3-70b"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(content), &config); err != nil {
		t.Fatal(err)
	}
	providers, _ := config["providers"].(map[string]any)
	for _, kind := range []string{"albert", "mistral", "openai-compatible"} {
		if _, ok := providers[kind]; !ok {
			t.Fatalf("provider %s missing from composed config: %s", kind, content)
		}
	}
	mistral, _ := providers["mistral"].(map[string]any)
	options, _ := mistral["settings"].(map[string]any)
	if options["baseURL"] != "https://api.mistral.ai/v1" || options["apiKey"] != "{env:MISTRAL_API_KEY}" {
		t.Fatalf("Mistral provider options = %+v", options)
	}
	if !strings.Contains(content, `"model":"mistral/mistral-large-latest"`) {
		t.Fatalf("composed config lost the default model: %s", content)
	}
}

func TestComposeConfigContentDropsUnconnectedAlbert(t *testing.T) {
	content, err := ComposeConfigContent(ManagedOverlay{
		Model: "openai-compatible/m1",
		Providers: []ProviderOverlay{
			{Kind: "openai-compatible", Name: "X", BaseURL: "https://api.example.com/v1", APIKeyEnv: OpenAICompatibleAPIKeyEnv,
				Models: []ProviderModelConfig{{ID: "m1", Name: "m1"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(content), &config); err != nil {
		t.Fatal(err)
	}
	providers, _ := config["providers"].(map[string]any)
	if _, ok := providers["albert"]; ok {
		t.Fatalf("an unconnected Albert block must be removed: %s", content)
	}
}

func TestSetupJournalSetPlatformsKeepsVerification(t *testing.T) {
	verified := true
	j := SetupJournal{Platforms: []SetupPlatform{{Kind: "albert", Name: "Albert API (État)", BaseURL: albertBaseURL, Verified: &verified}}}
	providers, err := ResolveProviders(j.PlatformSettings())
	if err != nil {
		t.Fatal(err)
	}
	j.SetPlatforms(providers)
	entry, ok := j.Platform(ProviderAlbert)
	if !ok || entry.Verified == nil || !*entry.Verified {
		t.Fatalf("verification lost on an unchanged platform: %+v", j.Platforms)
	}
}
