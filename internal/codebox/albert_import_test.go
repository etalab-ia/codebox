package codebox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverAlbertCodeImportRecognizesSafeLegacyIntent(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	writeImportFixtureFile(t, root, ".albert-code/skills.txt", "rgaa\nsecurity\nrgaa\n../escape\n")
	config := `{
  // Albert defaults; JSONC comments and trailing commas are legal.
  "$schema": "https://opencode.ai/config.json",
  "provider": {"albert": {
    "npm": "@ai-sdk/openai-compatible",
    "name": "Albert API",
    "options": {"baseURL": "https://albert.api.etalab.gouv.fr/v1", "apiKey": "{env:ALBERT_API_KEY}"},
    "models": {"deepseek-v4-flash": {"name": "DeepSeek V4 Flash"}},
  }},
  "model": "albert/deepseek-v4-flash",
  "mcp": {
    "data-gouv": {"type": "remote", "url": "https://mcp.data.gouv.fr/mcp", "enabled": true},
    "context7": {"type": "remote", "url": "https://mcp.context7.com/mcp", "enabled": true, "headers": {"Authorization": "Bearer {env:CONTEXT7_API_KEY}"}},
    "playwright": {"type": "local", "command": ["npx", "-y", "@playwright/mcp@latest"], "enabled": true},
    "private-tool": {"type": "local", "command": ["sh", "-c", "echo secret-in-command"], "enabled": true},
  },
  "permission": {"bash": {".*": "allow"}},
}`
	writeImportFixtureFile(t, root, "opencode.jsonc", config)
	agents := "custom instructions\n" + albertAgentsStart + "\nlegacy managed rules\n" + albertAgentsEnd + "\n"
	writeImportFixtureFile(t, root, "AGENTS.md", agents)
	dotenv := "ALBERT_API_KEY=secret-value-must-not-appear\nOTHER_TOKEN=other-secret-must-not-appear\nCONTEXT7_API_KEY=context-secret-must-not-appear\nRUNTIME=tart\n"
	writeImportFixtureFile(t, root, ".env", dotenv)
	writeImportFixtureFile(t, root, ".agent-vm.runtime.sh", "export ALBERT_API_KEY=runtime-secret-must-not-appear\n")

	plan, err := DiscoverAlbertCodeImport(root)
	if err != nil {
		t.Fatal(err)
	}
	plan.ResolveSkills([]ProjectSkill{{ID: "official/rgaa", Name: "rgaa"}})
	if !equalImportStrings(plan.Skills, []string{"official/rgaa"}) {
		t.Fatalf("resolved skills = %v", plan.Skills)
	}
	if !equalImportStrings(plan.MCPConnectors, []string{"data-gouv"}) {
		t.Fatalf("MCP connectors = %v", plan.MCPConnectors)
	}
	if plan.Model != "albert/deepseek-v4-flash" || !plan.CredentialAvailable || !plan.ManagedAgentsPresent || !plan.RuntimeScriptPresent {
		t.Fatalf("recognized intent = %+v", plan)
	}
	if len(plan.ReviewItems) < 3 {
		t.Fatalf("custom/unknown settings were not surfaced for review: %v", plan.ReviewItems)
	}
	preview := plan.Format()
	for _, secret := range []string{"secret-value-must-not-appear", "other-secret-must-not-appear", "context-secret-must-not-appear", "runtime-secret-must-not-appear", "secret-in-command"} {
		if strings.Contains(preview, secret) {
			t.Fatalf("preview leaked %q: %s", secret, preview)
		}
	}
	if !strings.Contains(preview, "--import-albert-key") || !strings.Contains(preview, "do not transfer") || !strings.Contains(preview, "Host hygiene: .env") {
		t.Fatalf("preview does not explain credential and workspace boundaries: %s", preview)
	}
	for _, name := range []string{"ALBERT_API_KEY", "OTHER_TOKEN", "CONTEXT7_API_KEY", "RUNTIME", "no longer offered"} {
		if !strings.Contains(preview, name) {
			t.Fatalf("preview omitted safe legacy environment review item %q: %s", name, preview)
		}
	}
	if got := string(readFileForTest(t, filepath.Join(root, "AGENTS.md"))); got != agents {
		t.Fatal("discovery modified AGENTS.md")
	}
	if got := string(readFileForTest(t, filepath.Join(root, ".env"))); got != dotenv {
		t.Fatal("discovery modified .env")
	}
}

func TestDiscoverAlbertCodeImportRecoversOpenRouterPlatform(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	config := `{
  "provider": {"openrouter": {
    "npm": "@ai-sdk/openai-compatible",
    "name": "OpenRouter",
    "options": {"baseURL": "https://openrouter.ai/api/v1", "apiKey": "{env:OPENROUTER_API_KEY}"},
    "models": {"z-ai/glm-4.6": {"name": "GLM 4.6"}},
  }},
  "model": "openrouter/z-ai/glm-4.6",
}`
	writeImportFixtureFile(t, root, "opencode.json", config)
	plan, err := DiscoverAlbertCodeImport(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Platforms) != 1 {
		t.Fatalf("platforms = %+v", plan.Platforms)
	}
	platform := plan.Platforms[0]
	if platform.Kind != ProviderOpenAICompatible || platform.Name != "OpenRouter" || platform.BaseURL != "https://openrouter.ai/api/v1" {
		t.Fatalf("imported platform = %+v", platform)
	}
	if plan.Model != "openai-compatible/z-ai/glm-4.6" {
		t.Fatalf("imported model = %q", plan.Model)
	}
	if !equalImportStrings(plan.ProviderModels, []string{"openai-compatible/z-ai/glm-4.6"}) {
		t.Fatalf("imported models = %v", plan.ProviderModels)
	}
}

func TestDiscoverAlbertCodeImportRejectsAnEndpointTheResolverRefuses(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	config := `{
  "provider": {"openrouter": {
    "npm": "@ai-sdk/openai-compatible",
    "name": "OpenRouter",
    "options": {"baseURL": "https://api.example.com:8443/v1", "apiKey": "{env:OPENROUTER_API_KEY}"},
    "models": {"z-ai/glm-4.6": {}}
  }},
  "model": "openrouter/z-ai/glm-4.6"
}`
	writeImportFixtureFile(t, root, "opencode.json", config)
	plan, err := DiscoverAlbertCodeImport(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Platforms) != 0 {
		t.Fatalf("an endpoint the resolver rejects must not be imported: %+v", plan.Platforms)
	}
	if !strings.Contains(strings.Join(plan.ReviewItems, "\n"), "cannot be imported") {
		t.Fatalf("the refusal must be explained: %v", plan.ReviewItems)
	}
}

func TestDiscoverAlbertCodeImportReportsMultipleOpenCodeConfigs(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	writeImportFixtureFile(t, root, "opencode.json", `{"model":"albert/model"}`)
	writeImportFixtureFile(t, root, "opencode.jsonc", `{"model":"albert/other"}`)
	plan, err := DiscoverAlbertCodeImport(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan.ReviewItems, "\n"), "multiple OpenCode config files") {
		t.Fatalf("multiple config files were not surfaced: %+v", plan)
	}
	if !equalImportStrings(plan.SourceFiles, []string{"opencode.json"}) {
		t.Fatalf("discovery read more than the first config: %v", plan.SourceFiles)
	}
}

func TestDiscoverAlbertCodeImportRejectsSymlinkedLegacyDirectories(t *testing.T) {
	for _, name := range []string{".opencode", ".albert-code"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			gitInit(t, root)
			outside := t.TempDir()
			if err := os.Symlink(outside, filepath.Join(root, name)); err != nil {
				t.Skipf("symlink creation unavailable: %v", err)
			}
			if _, err := DiscoverAlbertCodeImport(root); err == nil || !strings.Contains(err.Error(), "regular directory") {
				t.Fatalf("symlinked %s directory was accepted: %v", name, err)
			}
		})
	}
}

func TestDiscoverAlbertCodeImportRequiresGitWorktree(t *testing.T) {
	_, err := DiscoverAlbertCodeImport(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "Git worktree") {
		t.Fatalf("non-Git project root was accepted: %v", err)
	}
}

func TestDiscoverAlbertCodeImportReportsMalformedMarkersAndPartialSetup(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	writeImportFixtureFile(t, root, ".albert-code/skills.txt", "invalid skill/name\n")
	writeImportFixtureFile(t, root, "AGENTS.md", albertAgentsStart+"\nmissing end marker\n")
	plan, err := DiscoverAlbertCodeImport(root)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.MalformedAgentsMarkers || len(plan.ReviewItems) != 2 {
		t.Fatalf("malformed data not reported: %+v", plan)
	}
	if strings.Contains(plan.Format(), "invalid skill/name") {
		t.Fatal("invalid skill content should not be echoed into the preview")
	}

	partial := t.TempDir()
	gitInit(t, partial)
	if err := os.Mkdir(filepath.Join(partial, ".albert-code"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan, err = DiscoverAlbertCodeImport(partial)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "partial setup") {
		t.Fatalf("partial setup warning = %v", plan.Warnings)
	}
}

func TestDiscoverAlbertCodeImportSkipsSymlinkedOptionalFiles(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	writeImportFixtureFile(t, root, "opencode.json", `{"model": broken}`)
	if _, err := DiscoverAlbertCodeImport(root); err == nil || !strings.Contains(err.Error(), "parse opencode.json") {
		t.Fatalf("malformed OpenCode config error = %v", err)
	}

	root = t.TempDir()
	gitInit(t, root)
	outside := filepath.Join(t.TempDir(), "opencode.json")
	if err := os.WriteFile(outside, []byte(`{"model":"albert/x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "opencode.json")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	secretEnv := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(secretEnv, []byte("ALBERT_API_KEY=must-not-read\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretEnv, filepath.Join(root, ".env")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	outsideAgents := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(outsideAgents, []byte("external instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideAgents, filepath.Join(root, "AGENTS.md")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	plan, err := DiscoverAlbertCodeImport(root)
	if err != nil {
		t.Fatalf("preview must warn and skip optional symlinks: %v", err)
	}
	if plan.HasOpenCodeConfig || plan.CredentialAvailable || !strings.Contains(plan.Format(), "AGENTS.md is not a regular file; it was not read") {
		t.Fatalf("symlinked files were followed or not reported: %+v\n%s", plan, plan.Format())
	}
	if strings.Contains(plan.Format(), "must-not-read") || strings.Contains(plan.Format(), "external instructions") {
		t.Fatal("preview exposed content from a symlink target")
	}
}

func TestAlbertCodeImportPreviewEscapesUntrustedConfigKeys(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	if err := os.WriteFile(filepath.Join(root, "opencode.json"), []byte(`{"bad\u001b[31m":"value"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := DiscoverAlbertCodeImport(root)
	if err != nil {
		t.Fatal(err)
	}
	preview := plan.Format()
	if strings.ContainsRune(preview, '\x1b') || !strings.Contains(preview, `\x1b[31m`) {
		t.Fatalf("preview did not safely quote an untrusted config key: %q", preview)
	}
}

func TestAlbertCodeImportPreviewDoesNotRevealRawOpenCodeAPIKey(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	config := `{"provider":{"albert":{"npm":"@ai-sdk/openai-compatible","name":"Albert","options":{"baseURL":"https://albert.api.etalab.gouv.fr/v1","apiKey":"raw-api-key-must-not-appear"},"models":{"model":{}}}},"model":"albert/model"}`
	if err := os.WriteFile(filepath.Join(root, "opencode.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := DiscoverAlbertCodeImport(root)
	if err != nil {
		t.Fatal(err)
	}
	if preview := plan.Format(); strings.Contains(preview, "raw-api-key-must-not-appear") {
		t.Fatalf("preview leaked a raw provider key: %s", preview)
	}
}

func TestResolveAlbertCodeImportSkillsOnlyMapsOfficialNames(t *testing.T) {
	plan := AlbertCodeImport{SkillNames: []string{"rgaa", "private"}}
	plan.ResolveSkills([]ProjectSkill{
		{ID: "official/rgaa", Name: "rgaa"},
		{ID: "experimental/private", Name: "private"},
	})
	if !equalImportStrings(plan.Skills, []string{"official/rgaa"}) || len(plan.ReviewItems) != 1 {
		t.Fatalf("skill mapping = %+v", plan)
	}
}

func TestAlbertCodeImportFormatShowsUnresolvedSkillsWhenCatalogueIsUnavailable(t *testing.T) {
	plan := AlbertCodeImport{
		Root:                      "/tmp/project",
		SkillNames:                []string{"rgaa"},
		HasSkillsFile:             true,
		SkillCatalogueUnavailable: true,
		SourceFiles:               []string{".albert-code/skills.txt"},
	}
	preview := plan.Format()
	if !strings.Contains(preview, "Legacy skills not resolved (catalogue unavailable): rgaa") {
		t.Fatalf("offline preview omitted unresolved skill names: %s", preview)
	}
}

func equalImportStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func writeImportFixtureFile(t *testing.T, root, rel, contents string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverAlbertCodeImportPreservesModelAliases(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	config := `{
  "provider": {"openrouter": {
    "npm": "@ai-sdk/openai-compatible",
    "name": "OpenRouter",
    "options": {"baseURL": "https://openrouter.ai/api/v1", "apiKey": "{env:OPENROUTER_API_KEY}"},
    "models": {"glm": {"name": "GLM", "modelID": "z-ai/glm-4.6"}}
  }},
  "model": "openrouter/glm"
}`
	writeImportFixtureFile(t, root, "opencode.json", config)
	plan, err := DiscoverAlbertCodeImport(root)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ProviderModelIDs["openai-compatible/glm"] != "z-ai/glm-4.6" {
		t.Fatalf("alias mapping lost: %+v", plan.ProviderModelIDs)
	}
}
