package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
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
		if err := validateInitOptions(badRT); err == nil || !strings.Contains(strings.ToLower(err.Error()), "microsandbox") {
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

	// Refuse --local-server none combined with a local --small-model.
	conflict, err := parseInitArgs([]string{"--local-server", "none", "--small-model", "local/qwen2.5:3b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateInitOptions(conflict); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("--local-server none + local small model = %v, want incompatibility error", err)
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

func TestInitReplaceExplicitLocalServerReplacesUnapprovedInheritedPort(t *testing.T) {
	setupTestEnv(t)
	root := t.TempDir()
	// Pre-populate a cloned manifest with an unapproved local port (2375).
	if err := codebox.WriteProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root), codebox.ProjectManifest{
		SchemaVersion: 8,
		Project:       "cloned",
		Providers: []codebox.ProviderSettings{
			{Kind: codebox.ProviderAlbert},
			{Kind: codebox.ProviderLocal, Name: "Inherited", BaseURL: "http://localhost:2375/v1"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Explicit --replace --local-server 11434 must succeed and replace port 2375.
	code, err := initCmd([]string{
		"--root", root,
		"--replace",
		"--local-server", "11434",
		"--small-model", "local/qwen2.5:3b",
		"--yes",
	})
	if code != 0 || err != nil {
		t.Fatalf("init --replace --local-server 11434: code=%d err=%v", code, err)
	}
	manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root))
	if err != nil {
		t.Fatal(err)
	}
	found11434 := false
	for _, p := range manifest.Providers {
		if p.Kind == codebox.ProviderLocal {
			if p.BaseURL != "http://localhost:11434/v1" {
				t.Fatalf("expected inherited port 2375 replaced with 11434, got %+v", p)
			}
			found11434 = true
		}
	}
	if !found11434 {
		t.Fatalf("missing local provider in updated manifest: %+v", manifest.Providers)
	}
}

func TestInitLocalServerNoneRemovesProjectAndHostApproval(t *testing.T) {
	setupTestEnv(t)
	root := t.TempDir()
	if code, err := initCmd([]string{
		"--root", root,
		"--local-server", "11434",
		"--small-model", "local/qwen2.5:3b",
		"--yes",
	}); code != 0 || err != nil {
		t.Fatalf("setup init --local-server: code=%d err=%v", code, err)
	}
	if code, err := initCmd([]string{
		"--root", root,
		"--replace",
		"--local-server", "none",
		"--yes",
	}); code != 0 || err != nil {
		t.Fatalf("init --replace --local-server none: code=%d err=%v", code, err)
	}
	manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SmallModel != "" {
		t.Fatalf("local small model was not cleared: %q", manifest.SmallModel)
	}
	for _, p := range manifest.Providers {
		if p.Kind == codebox.ProviderLocal {
			t.Fatalf("ProviderLocal remained in project.json after --local-server none: %+v", manifest.Providers)
		}
	}
	settingsPath, err := codebox.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	us, err := codebox.ReadUserSettings(codebox.DefaultFS, settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range us.Providers {
		if p.Kind == codebox.ProviderLocal {
			t.Fatalf("ProviderLocal remained in host settings.json after --local-server none: %+v", us.Providers)
		}
	}
}

func TestInitSmallModelDefaultPrunesOrphanedLocalProvider(t *testing.T) {
	setupTestEnv(t)
	root := t.TempDir()
	if code, err := initCmd([]string{
		"--root", root,
		"--local-server", "11434",
		"--small-model", "local/qwen2.5:3b",
		"--yes",
	}); code != 0 || err != nil {
		t.Fatalf("setup init --local-server: code=%d err=%v", code, err)
	}
	if code, err := initCmd([]string{
		"--root", root,
		"--replace",
		"--small-model", "default",
		"--yes",
	}); code != 0 || err != nil {
		t.Fatalf("init --replace --small-model default: code=%d err=%v", code, err)
	}
	manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SmallModel != "" {
		t.Fatalf("manifest.SmallModel = %q, want empty", manifest.SmallModel)
	}
	for _, p := range manifest.Providers {
		if p.Kind == codebox.ProviderLocal {
			t.Fatalf("orphaned ProviderLocal remained in project.json after --small-model default: %+v", manifest.Providers)
		}
	}
}

func TestWarnIfLocalModelServerUnreachableNonBlocking(t *testing.T) {
	origProbe := probeLocalModelServerFn
	t.Cleanup(func() { probeLocalModelServerFn = origProbe })
	localProv, err := codebox.ResolveProvider(codebox.ProviderSettings{
		Kind:    codebox.ProviderLocal,
		Name:    "Ollama",
		BaseURL: "http://localhost:11434/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	launch := &codebox.LaunchUX{Terminal: true}
	probeLocalModelServerFn = func(context.Context, string) ([]string, error) {
		return nil, errors.New("connection refused")
	}
	warnIfLocalModelServerUnreachable([]codebox.InferenceProvider{localProv}, launch)
	if warnings := launch.Warnings(); len(warnings) != 1 || !strings.Contains(warnings[0], "Ollama") || !strings.Contains(warnings[0], "--local-server aucun") {
		t.Fatalf("unreachable local server warnings = %v, want actionable French warning", warnings)
	}

	launchOK := &codebox.LaunchUX{Terminal: true}
	probeLocalModelServerFn = func(context.Context, string) ([]string, error) {
		return []string{"qwen2.5:3b"}, nil
	}
	warnIfLocalModelServerUnreachable([]codebox.InferenceProvider{localProv}, launchOK)
	if warnings := launchOK.Warnings(); len(warnings) != 0 {
		t.Fatalf("reachable local server emitted unexpected warnings: %v", warnings)
	}

	var doctorOut strings.Builder
	reportLocalModelServerDoctor(&doctorOut, []codebox.InferenceProvider{localProv})
	if !strings.Contains(doctorOut.String(), "joignable (1 modèle(s) ; port hôte 11434)") {
		t.Fatalf("doctor reachable output = %q", doctorOut.String())
	}

	doctorOut.Reset()
	probeLocalModelServerFn = func(context.Context, string) ([]string, error) {
		return nil, errors.New("connection refused")
	}
	reportLocalModelServerDoctor(&doctorOut, []codebox.InferenceProvider{localProv})
	if !strings.Contains(doctorOut.String(), "Ollama") || !strings.Contains(doctorOut.String(), "--local-server aucun") {
		t.Fatalf("doctor unreachable output = %q", doctorOut.String())
	}
}

func TestInitAdvancedConfiguresAndRemovesLocalModelServer(t *testing.T) {
	origRun, origInstall, origDetect, origProbe := runInitAdvancedForm, runtimeInstallHintFn, detectLocalModelServersFn, probeLocalModelServerFn
	t.Cleanup(func() {
		runInitAdvancedForm, runtimeInstallHintFn, detectLocalModelServersFn, probeLocalModelServerFn = origRun, origInstall, origDetect, origProbe
	})
	runtimeInstallHintFn = func(codebox.Runtime) (bool, string) { return true, "" }
	detectLocalModelServersFn = func(context.Context, *http.Client) []codebox.LocalServerCandidate {
		return []codebox.LocalServerCandidate{{
			Name:    "Ollama",
			Port:    11434,
			BaseURL: "http://localhost:11434/v1",
			Models:  []string{"qwen2.5:3b"},
		}}
	}
	probeLocalModelServerFn = func(context.Context, string) ([]string, error) {
		return []string{"qwen2.5:3b"}, nil
	}

	// Step 1: Select detected Ollama in "Serveur de modèles local", then pick local/qwen2.5:3b in "Petit modèle".
	calls := 0
	runInitAdvancedForm = func(form *huh.Form) (bool, error) {
		calls++
		field := form.GetFocusedField()
		field.Focus()
		switch calls {
		case 1:
			// Option 6 is "Serveur de modèles local".
			for i := 0; i < 6; i++ {
				field.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			}
			field.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		case 2:
			// Option 0 is "Aucun", Option 1 is detected Ollama.
			field.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			field.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		case 3:
			// Choice resets to "local-server" (index 6); move up 2 to "small-model" (index 4).
			for i := 0; i < 2; i++ {
				field.Update(tea.KeyPressMsg{Code: tea.KeyUp})
			}
			field.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		case 4:
			// Option 0: default, Option 1: Ministral, Option 2: qwen2.5:3b (Ollama).
			for i := 0; i < 2; i++ {
				field.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			}
			field.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		case 5:
			return true, nil
		default:
			t.Fatalf("unexpected form call %d", calls)
		}
		return false, nil
	}
	opts := initOptions{hostCPUs: 8, hostMemoryMB: 16384, maxCPUs: 8, maxMemoryMB: 16384}
	afterAdd, err := askInitAdvancedHuh(opts, codebox.InitAnswers{
		Root:      "/tmp/example",
		Providers: []codebox.ProviderSettings{{Kind: codebox.ProviderAlbert}},
	})
	if err != nil {
		t.Fatalf("askInitAdvancedHuh add local server: %v", err)
	}
	if !afterAdd.LocalServerApproved || afterAdd.SmallModel != "local/qwen2.5:3b" {
		t.Fatalf("expected approved local server + local/qwen2.5:3b, got %+v", afterAdd)
	}
	if _, ok := localProviderSetting(afterAdd.Providers); !ok {
		t.Fatalf("ProviderLocal missing after advanced add: %+v", afterAdd.Providers)
	}

	// Step 2: Re-open and disable "Serveur de modèles local" -> clears ProviderLocal and local SmallModel.
	calls = 0
	runInitAdvancedForm = func(form *huh.Form) (bool, error) {
		calls++
		field := form.GetFocusedField()
		field.Focus()
		switch calls {
		case 1:
			for i := 0; i < 6; i++ {
				field.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			}
			field.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		case 2:
			// Current selection is Ollama (index 1); move up 1 to "Aucun" (index 0).
			field.Update(tea.KeyPressMsg{Code: tea.KeyUp})
			field.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		case 3:
			return true, nil
		default:
			t.Fatalf("unexpected disable form call %d", calls)
		}
		return false, nil
	}
	afterRemove, err := askInitAdvancedHuh(opts, afterAdd)
	if err != nil {
		t.Fatalf("askInitAdvancedHuh remove local server: %v", err)
	}
	if !afterRemove.LocalServerCleared || afterRemove.SmallModel != "" {
		t.Fatalf("expected cleared local server and small model, got %+v", afterRemove)
	}
	if _, ok := localProviderSetting(afterRemove.Providers); ok {
		t.Fatalf("ProviderLocal remained after advanced disable: %+v", afterRemove.Providers)
	}
}
