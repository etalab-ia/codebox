package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
)

func TestParseAndValidateInitLocalServerOptions(t *testing.T) {
	opts, err := parseInitArgs([]string{"--local-server", "11434", "--small-model", "local/qwen2.5:3b", "--yes"})
	if err != nil {
		t.Fatalf("parseInitArgs: %v", err)
	}
	if opts.LocalServer != "11434" || !opts.Set["local-server"] || opts.SmallModel != "local/qwen2.5:3b" {
		t.Fatalf("parsed opts = %+v", opts)
	}
	if err := validateInitOptions(opts); err != nil {
		t.Fatalf("validateInitOptions valid local-server: %v", err)
	}

	// Refuse --local-server on Tart and agent-vm.
	for _, rt := range []string{"tart", "agent-vm"} {
		badRT, err := parseInitArgs([]string{"--runtime", rt, "--local-server", "11434"})
		if err != nil {
			t.Fatal(err)
		}
		if err := validateInitOptions(badRT); err == nil || !strings.Contains(err.Error(), "Microsandbox") {
			t.Fatalf("--runtime %s with --local-server = %v, want Microsandbox error", rt, err)
		}
	}

	// Refuse remote URL for --local-server.
	badURL, err := parseInitArgs([]string{"--local-server", "https://example.com/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateInitOptions(badURL); err == nil {
		t.Fatal("remote URL for --local-server must be rejected")
	}

	// Refuse local model as main --model.
	badModel, err := parseInitArgs([]string{"--model", "local/qwen2.5:3b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateInitOptions(badModel); err == nil || !strings.Contains(err.Error(), "small-model") {
		t.Fatalf("--model local/... = %v, want small-model-only error", err)
	}
}

func TestResolveInitLocalServerAutoAndPort(t *testing.T) {
	origDetect := detectLocalModelServersFn
	t.Cleanup(func() { detectLocalModelServersFn = origDetect })

	// No running local server -> clear one-line French error.
	detectLocalModelServersFn = func(context.Context, *http.Client) []codebox.LocalServerCandidate {
		return nil
	}
	if _, err := resolveInitLocalServer(context.Background(), "auto"); err == nil || !strings.Contains(err.Error(), "aucun serveur local détecté") {
		t.Fatalf("auto with no server = %v, want one-line French error", err)
	}

	// Running Ollama detected -> returns canonical ProviderLocal.
	detectLocalModelServersFn = func(context.Context, *http.Client) []codebox.LocalServerCandidate {
		return []codebox.LocalServerCandidate{{
			Name:    "Ollama",
			Port:    11434,
			BaseURL: "http://localhost:11434/v1",
			Models:  []string{"qwen2.5:3b"},
		}}
	}
	ps, err := resolveInitLocalServer(context.Background(), "auto")
	if err != nil {
		t.Fatalf("auto with Ollama: %v", err)
	}
	if ps.Kind != codebox.ProviderLocal || ps.Name != "Ollama" || ps.BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("auto resolved = %+v", ps)
	}

	// Bare port 1234 maps to LM Studio canonical URL.
	lm, err := resolveInitLocalServer(context.Background(), "1234")
	if err != nil {
		t.Fatalf("port 1234: %v", err)
	}
	if lm.Name != "LM Studio" || lm.BaseURL != "http://localhost:1234/v1" {
		t.Fatalf("port 1234 resolved = %+v", lm)
	}
}

func TestInitYesWithLocalServerPersistsProjectAndHostApproval(t *testing.T) {
	setupTestEnv(t)
	root := t.TempDir()
	code, err := initCmd([]string{
		"--root", root,
		"--local-server", "11434",
		"--small-model", "local/qwen2.5:3b",
		"--yes",
	})
	if code != 0 || err != nil {
		t.Fatalf("init --local-server: code=%d err=%v", code, err)
	}
	manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root))
	if err != nil {
		t.Fatalf("ReadProjectManifest: %v", err)
	}
	if manifest.SmallModel != "local/qwen2.5:3b" {
		t.Fatalf("manifest.SmallModel = %q, want local/qwen2.5:3b", manifest.SmallModel)
	}
	settingsPath, err := codebox.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	us, err := codebox.ReadUserSettings(codebox.DefaultFS, settingsPath)
	if err != nil {
		t.Fatalf("ReadUserSettings: %v", err)
	}
	if err := codebox.ValidateProjectProviders(manifest.Providers, us.Providers); err != nil {
		t.Fatalf("project local provider must be approved in host user settings: %v", err)
	}
}
