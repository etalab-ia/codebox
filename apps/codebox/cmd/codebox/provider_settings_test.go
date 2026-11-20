package main

import (
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
		CredentialRef: "openai-compatible@openrouter.ai",
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
