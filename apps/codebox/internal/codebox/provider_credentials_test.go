package codebox

import (
	"context"
	"errors"
	"testing"
)

func TestProviderCredentialRefsBindDestination(t *testing.T) {
	providers, err := ResolveProviders([]ProviderSettings{
		{Kind: ProviderAlbert, CredentialRef: "legacy-albert"},
		{Kind: ProviderMistral, CredentialRef: "mistral:api.mistral.ai"},
		{Kind: ProviderOpenAICompatible, Name: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1", CredentialRef: "openai-compatible:openrouter.ai"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range providers {
		if provider.CredentialRef == "" {
			t.Fatalf("provider %s lost its credentialRef", provider.Kind)
		}
		if provider.Kind != ProviderAlbert && provider.CredentialRef != ProviderCredentialRef(provider) {
			t.Fatalf("provider %s reference %q does not match its host-bound default", provider.Kind, provider.CredentialRef)
		}
		if err := ValidateProviderCredentialRef(provider, provider.CredentialRef); err != nil {
			t.Fatalf("valid reference rejected for %s: %v", provider.Kind, err)
		}
	}
	mistral := providers[1]
	for _, ref := range []string{"albert", "mistral:evil.example", "openai-compatible:openrouter.ai"} {
		if err := ValidateProviderCredentialRef(mistral, ref); err == nil {
			t.Fatalf("Mistral accepted wrong-provider or wrong-host reference %q", ref)
		}
	}
}

func TestProviderCredentialRefRoundTripsBoundStoreEntry(t *testing.T) {
	provider, err := ResolveProvider(ProviderSettings{Kind: ProviderMistral})
	if err != nil {
		t.Fatal(err)
	}
	ref := ProviderCredentialRef(provider)
	record := formatBoundStoreEntry(boundStoreEntry{Entry: ref, Store: "native", Binding: provider.CredentialKind})
	parsed, form := parseBoundStoreEntry(record)
	if form != boundRecordFull || parsed.Entry != ref || parsed.Store != "native" || parsed.Binding != provider.CredentialKind {
		t.Fatalf("bound credential record did not round-trip %q: %+v, form=%v", record, parsed, form)
	}
}

func TestResolveProviderKeysUsesExplicitHostBoundReferences(t *testing.T) {
	providers, err := ResolveProviders([]ProviderSettings{
		{Kind: ProviderMistral, CredentialRef: "mistral:api.mistral.ai"},
		{Kind: ProviderOpenAICompatible, Name: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1", CredentialRef: "openai-compatible:openrouter.ai"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MISTRAL_API_KEY", "environment-mistral")
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "environment-openai")
	var reads []CredentialKind
	read := func(_ context.Context, kind CredentialKind, _ string) (string, string, error) {
		reads = append(reads, kind)
		return "stored-" + string(kind), "fake", nil
	}
	got, err := ResolveProviderKeysWith(read, context.Background(), Config{Providers: providers}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got[MistralAPIKeyEnv] != "stored-mistral:api.mistral.ai" || got[OpenAICompatibleAPIKeyEnv] != "stored-openai-compatible:openrouter.ai" {
		t.Fatalf("explicit references did not win: %#v", got)
	}
	if len(reads) != 2 || reads[0] != CredentialKind("mistral:api.mistral.ai") || reads[1] != CredentialKind("openai-compatible:openrouter.ai") {
		t.Fatalf("unexpected store reads: %v", reads)
	}
}

func TestResolveProviderKeysRejectsWrongHostBeforeRead(t *testing.T) {
	provider, err := ResolveProvider(ProviderSettings{Kind: ProviderMistral})
	if err != nil {
		t.Fatal(err)
	}
	provider.CredentialRef = "mistral:evil.example"
	providers := []InferenceProvider{provider}
	reads := 0
	read := func(_ context.Context, kind CredentialKind, _ string) (string, string, error) {
		reads++
		return "must-not-be-read", "fake", nil
	}
	if _, err := ResolveProviderKeysWith(read, context.Background(), Config{Providers: providers}, ""); err == nil {
		t.Fatal("wrong-host reference unexpectedly resolved")
	}
	if reads != 0 {
		t.Fatalf("wrong-host reference reached the credential store %d times", reads)
	}
}

func TestResolveProviderKeysKeepsLegacyFallback(t *testing.T) {
	providers, err := ResolveProviders([]ProviderSettings{{Kind: ProviderMistral}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MISTRAL_API_KEY", "from-environment")
	read := func(_ context.Context, kind CredentialKind, _ string) (string, string, error) {
		return "fixed-entry", "fake", nil
	}
	got, err := ResolveProviderKeysWith(read, context.Background(), Config{Providers: providers}, "")
	if err != nil || got[MistralAPIKeyEnv] != "from-environment" {
		t.Fatalf("legacy environment precedence changed: %#v, %v", got, err)
	}
	t.Setenv("MISTRAL_API_KEY", "")
	got, err = ResolveProviderKeysWith(read, context.Background(), Config{Providers: providers}, "")
	if err != nil || got[MistralAPIKeyEnv] != "fixed-entry" {
		t.Fatalf("legacy fixed entry fallback changed: %#v, %v", got, err)
	}
}

func TestAlbertCredentialRefEnvironmentOverrideWins(t *testing.T) {
	providers, err := ResolveProviders([]ProviderSettings{{Kind: ProviderAlbert, CredentialRef: "project-albert"}})
	if err != nil {
		t.Fatal(err)
	}
	read := func(_ context.Context, kind CredentialKind, _ string) (string, string, error) {
		if kind != CredentialKind("env-albert") && kind != CredentialKind("project-albert") {
			return "", "", errors.New("unexpected reference")
		}
		return string(kind), "fake", nil
	}
	t.Setenv("CODEBOX_CREDENTIAL_REF", "")
	got, err := ResolveProviderKeysWith(read, context.Background(), Config{Providers: providers}, "")
	if err != nil || got["ALBERT_API_KEY"] != "project-albert" {
		t.Fatalf("project provider reference was not applied: %#v, %v", got, err)
	}
	t.Setenv("CODEBOX_CREDENTIAL_REF", "env-albert")
	got, err = ResolveProviderKeysWith(read, context.Background(), Config{Providers: providers, CredentialRef: "env-albert"}, "")
	if err != nil || got["ALBERT_API_KEY"] != "env-albert" {
		t.Fatalf("environment reference precedence changed: %#v, %v", got, err)
	}
}

func TestValidateProjectProvidersGuardsLegacyOpenAICompatibleEndpoint(t *testing.T) {
	attacker := []ProviderSettings{
		{Kind: ProviderOpenAICompatible, Name: "Evil", BaseURL: "https://attacker.example/v1"},
	}
	scalewayLegacy := []ProviderSettings{
		{Kind: ProviderOpenAICompatible, Name: "Scaleway", BaseURL: "https://api.scaleway.ai/v1"},
	}
	scalewayBound := []ProviderSettings{
		{Kind: ProviderOpenAICompatible, Name: "Scaleway", BaseURL: "https://api.scaleway.ai/v1", CredentialRef: "openai-compatible:api.scaleway.ai"},
	}

	// Unapproved project endpoint with omitted credentialRef must fail when host
	// has no OpenAI-compatible provider or a different host.
	if err := ValidateProjectProviders(attacker, nil); err == nil {
		t.Fatal("project OpenAI-compatible endpoint without credentialRef was accepted with empty user settings")
	}
	if err := ValidateProjectProviders(attacker, scalewayLegacy); err == nil {
		t.Fatal("project OpenAI-compatible endpoint without credentialRef was accepted for a different host")
	}
	// Host configured with an explicit host-bound reference is not on the legacy
	// fallback path, so omitting credentialRef in the project must still fail.
	if err := ValidateProjectProviders(scalewayLegacy, scalewayBound); err == nil {
		t.Fatal("project OpenAI-compatible endpoint without credentialRef was accepted when host settings use an explicit host-bound reference")
	}

	// Matching canonical host configured on the host's own legacy path is allowed.
	canonicalProject := []ProviderSettings{
		{Kind: ProviderOpenAICompatible, Name: "Scaleway", BaseURL: "https://API.SCALEWAY.AI:443/v1/"},
	}
	if err := ValidateProjectProviders(canonicalProject, scalewayLegacy); err != nil {
		t.Fatalf("host-approved legacy OpenAI-compatible endpoint was rejected: %v", err)
	}

	// Explicit matching host-bound credentialRef is allowed even when absent from
	// user settings (resolution only reads that exact host-bound store entry).
	openRouterBound := []ProviderSettings{
		{Kind: ProviderOpenAICompatible, Name: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1", CredentialRef: "openai-compatible:openrouter.ai"},
	}
	if err := ValidateProjectProviders(openRouterBound, nil); err != nil {
		t.Fatalf("explicit host-bound project provider was rejected: %v", err)
	}

	// Fixed-endpoint providers (Albert, Mistral) remain allowed without credentialRef.
	if err := ValidateProjectProviders([]ProviderSettings{{Kind: ProviderAlbert}, {Kind: ProviderMistral}}, nil); err != nil {
		t.Fatalf("fixed-endpoint project providers without credentialRef were rejected: %v", err)
	}
}
