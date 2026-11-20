package main

import (
	"bufio"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
)

func TestResolveProviderSettingsAtProject(t *testing.T) {
	setupTestEnv(t)
	root := t.TempDir()
	settingsPath, err := codebox.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := codebox.WriteUserSettings(codebox.DefaultFS, settingsPath, codebox.UserSettings{
		Providers: []codebox.ProviderSettings{{Kind: codebox.ProviderMistral}},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := resolveProviderSettingsAt(root)
	if err != nil || len(got) != 1 || got[0].Kind != codebox.ProviderMistral {
		t.Fatalf("missing project manifest must preserve user providers: %#v, %v", got, err)
	}

	projectProvider := codebox.ProviderSettings{
		Kind: codebox.ProviderOpenAICompatible, Name: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1",
		CredentialRef: "openai-compatible:openrouter.ai",
	}
	if err := codebox.WriteProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root), codebox.ProjectManifest{
		Providers: []codebox.ProviderSettings{projectProvider},
	}); err != nil {
		t.Fatal(err)
	}
	got, err = resolveProviderSettingsAt(root)
	if err != nil || len(got) != 1 || got[0] != projectProvider {
		t.Fatalf("project provider did not replace the machine default: %#v, %v", got, err)
	}

	if err := codebox.WriteUserSettings(codebox.DefaultFS, settingsPath, codebox.UserSettings{}); err != nil {
		t.Fatal(err)
	}
	if err := codebox.WriteProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root), codebox.ProjectManifest{CredentialRef: "legacy-albert"}); err != nil {
		t.Fatal(err)
	}
	got, err = resolveProviderSettingsAt(root)
	if err != nil || len(got) != 1 || got[0].Kind != codebox.ProviderAlbert || got[0].CredentialRef != "legacy-albert" {
		t.Fatalf("legacy project reference was not preserved: %#v, %v", got, err)
	}

	projectProvider = codebox.ProviderSettings{Kind: codebox.ProviderAlbert, CredentialRef: "explicit-albert"}
	if err := codebox.WriteProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root), codebox.ProjectManifest{
		CredentialRef: "legacy-albert", Providers: []codebox.ProviderSettings{projectProvider},
	}); err != nil {
		t.Fatal(err)
	}
	got, err = resolveProviderSettingsAt(root)
	if err != nil || got[0].CredentialRef != "explicit-albert" {
		t.Fatalf("legacy reference overrode the explicit provider reference: %#v, %v", got, err)
	}
}

func TestInitPersistsProjectProviderCredentials(t *testing.T) {
	setupTestEnv(t)
	root := initTestProject(t)
	settingsPath, err := codebox.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	provider := codebox.ProviderSettings{
		Kind: codebox.ProviderMistral, CredentialRef: "mistral:api.mistral.ai",
	}
	if err := codebox.WriteUserSettings(codebox.DefaultFS, settingsPath, codebox.UserSettings{
		Providers: []codebox.ProviderSettings{provider},
	}); err != nil {
		t.Fatal(err)
	}
	code, err := initRun(initOptions{Root: root, Yes: true, Set: map[string]bool{"root": true}}, bufio.NewReader(strings.NewReader("")), false)
	if code != 0 || err != nil {
		t.Fatalf("initRun = %d, %v", code, err)
	}
	manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Providers) != 1 || manifest.Providers[0].Kind != provider.Kind || manifest.Providers[0].CredentialRef != provider.CredentialRef {
		t.Fatalf("project provider reference was not persisted: %#v", manifest.Providers)
	}
	raw, err := os.ReadFile(codebox.ProjectManifestPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "mistral:api.mistral.ai") {
		t.Fatal("project manifest is missing the non-secret reference")
	}
	if strings.Contains(string(raw), "credential-value") {
		t.Fatal("project manifest contains a credential value")
	}
}

func TestInitExplicitCredentialRefReplacesProviderReference(t *testing.T) {
	setupTestEnv(t)
	root := initTestProject(t)
	first := initOptions{Root: root, Yes: true, CredentialRef: "old-albert", Set: map[string]bool{"root": true, "credential-ref": true}}
	if code, err := initRun(first, bufio.NewReader(strings.NewReader("")), false); code != 0 || err != nil {
		t.Fatalf("initial initRun = %d, %v", code, err)
	}
	second := initOptions{Root: root, Yes: true, Replace: true, CredentialRef: "new-albert", Set: map[string]bool{"root": true, "credential-ref": true}}
	if code, err := initRun(second, bufio.NewReader(strings.NewReader("")), false); code != 0 || err != nil {
		t.Fatalf("replacement initRun = %d, %v", code, err)
	}
	manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Providers) != 1 || manifest.Providers[0].CredentialRef != "new-albert" {
		t.Fatalf("explicit credential-ref did not replace provider reference: %#v", manifest.Providers)
	}
}

func TestModelsListUsesProjectProviders(t *testing.T) {
	setupTestEnv(t)
	root := t.TempDir()
	t.Chdir(root)
	if err := codebox.WriteProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root), codebox.ProjectManifest{
		Providers: []codebox.ProviderSettings{{Kind: codebox.ProviderMistral, CredentialRef: "mistral:api.mistral.ai"}},
	}); err != nil {
		t.Fatal(err)
	}
	original := modelsListProviderFn
	var got []codebox.ProviderKind
	modelsListProviderFn = func(_ context.Context, provider codebox.InferenceProvider, _ codebox.UserSettings, _ string) error {
		got = append(got, provider.Kind)
		return nil
	}
	t.Cleanup(func() { modelsListProviderFn = original })
	if code, err := modelsListCmd(); code != 0 || err != nil {
		t.Fatalf("modelsListCmd = %d, %v", code, err)
	}
	if len(got) != 1 || got[0] != codebox.ProviderMistral {
		t.Fatalf("models command used machine providers instead of project providers: %v", got)
	}
}
