package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/etalab-ia/just-code/internal/justcode"
)

func TestParseArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		action  string
		runtime string
		version bool
	}{
		{"bare attaches", nil, "attach", "", false},
		{"leading runtime flag attaches", []string{"--tart"}, "attach", "--tart", false},
		{"agent-vm leading runtime flag", []string{"--agent-vm"}, "attach", "--agent-vm", false},
		{"runtime flag then command", []string{"--microsandbox", "start"}, "start", "--microsandbox", false},
		{"agent-vm command then runtime flag", []string{"start", "--agent-vm"}, "start", "--agent-vm", false},
		{"command then runtime flag", []string{"start", "--microsandbox"}, "start", "--microsandbox", false},
		{"explicit start", []string{"start", "--microsandbox"}, "start", "--microsandbox", false},
		{"stop", []string{"stop"}, "stop", "", false},
		{"check", []string{"check"}, "check", "", false},
		{"logs", []string{"logs", "--tart"}, "logs", "--tart", false},
		{"help", []string{"help"}, "help", "", false},
		{"help flag", []string{"--help"}, "help", "", false},
		{"version subcommand", []string{"version"}, "version", "", false},
		{"version long flag", []string{"--version"}, "attach", "", true},
		{"version short flag", []string{"-V"}, "attach", "", true},
		{"version lowercase alias", []string{"-v"}, "attach", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseArgs(c.args)
			if err != nil {
				t.Fatalf("parseArgs(%v): %v", c.args, err)
			}
			if got.action != c.action {
				t.Errorf("action = %q, want %q", got.action, c.action)
			}
			if got.runtime != c.runtime {
				t.Errorf("runtime = %q, want %q", got.runtime, c.runtime)
			}
			if got.version != c.version {
				t.Errorf("version = %v, want %v", got.version, c.version)
			}
		})
	}
}

func TestResolveEnabledAlbertModelsKeepsActiveModelAvailable(t *testing.T) {
	setupTestEnv(t)
	path, err := justcode.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	choices := justcode.CuratedAlbertModels()
	if err := justcode.WriteUserSettings(justcode.DefaultFS, path, justcode.UserSettings{
		SchemaVersion: 1,
		DefaultModel:  choices[1].Ref(),
		Models:        []string{choices[1].ID},
	}); err != nil {
		t.Fatal(err)
	}
	got := resolveEnabledAlbertModels(choices[0].Ref())
	var ids []string
	for _, model := range got {
		ids = append(ids, model.ID)
	}
	if len(ids) != 2 || ids[0] != choices[1].OpenCodeID || ids[1] != choices[0].OpenCodeID {
		t.Fatalf("configured model keys = %v; selected and active default models must both be available", ids)
	}
}

func TestResolveEnabledAlbertModelsMapsLegacyFullID(t *testing.T) {
	setupTestEnv(t)
	path, err := justcode.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	deepseek := justcode.CuratedAlbertModels()[1]
	if err := justcode.WriteUserSettings(justcode.DefaultFS, path, justcode.UserSettings{
		SchemaVersion: 1,
		DefaultModel:  "albert/" + deepseek.ID,
		Models:        []string{deepseek.ID},
	}); err != nil {
		t.Fatal(err)
	}
	got := resolveEnabledAlbertModels("albert/" + deepseek.ID)
	if len(got) != 1 || got[0].ID != deepseek.OpenCodeID || got[0].Output != 65536 || got[0].ModelID != deepseek.ID {
		t.Fatalf("legacy DeepSeek model config = %+v", got)
	}
}

func TestResolveModelSelectionCanonicalizesLegacyAlbertReference(t *testing.T) {
	choice := justcode.CuratedAlbertModels()[1]
	t.Setenv("JUST_CODE_MODEL", "albert/"+choice.ID)
	if got := resolveModelSelection(t.TempDir()); got != choice.Ref() {
		t.Fatalf("legacy model reference = %q, want canonical %q", got, choice.Ref())
	}
}

func TestResolveSmallModelSelectionAndComposedOpenCodeConfig(t *testing.T) {
	setupTestEnv(t)
	root := t.TempDir()
	if err := justcode.WriteProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(root), justcode.ProjectManifest{
		Model: "albert/qwen3-coder", SmallModel: "ministral-3-8b-instruct-2512",
	}); err != nil {
		t.Fatal(err)
	}
	if got := resolveSmallModelSelection(root); got != justcode.MinistralSmallModelRef {
		t.Fatalf("small-model reference = %q", got)
	}
	models := resolveEnabledAlbertModels(resolveModelSelection(root), justcode.MinistralSmallModelRef)
	var minimistral *justcode.AlbertModelConfig
	for i := range models {
		if models[i].ID == "ministral-3-8b" {
			minimistral = &models[i]
		}
	}
	if minimistral == nil || minimistral.ModelID != "ministral-3-8b-instruct-2512" {
		t.Fatalf("Ministral must be available even without global selection: %+v", models)
	}
	content, err := justcode.ComposeConfigContent(justcode.ManagedOverlay{
		Model: resolveModelSelection(root), SmallModel: resolveSmallModelSelection(root), Models: models,
	})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(content), &config); err != nil {
		t.Fatal(err)
	}
	if config["small_model"] != justcode.MinistralSmallModelRef {
		t.Fatalf("small_model = %v", config["small_model"])
	}
	if !strings.Contains(content, `"ministral-3-8b"`) {
		t.Fatalf("Ministral provider definition missing from launch config: %s", content)
	}
}

func TestModelsListAcceptsCuratedSmallModelAPIReference(t *testing.T) {
	setupTestEnv(t)
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("ALBERT_API_KEY", "test-albert-key")
	if err := justcode.WriteProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(root), justcode.ProjectManifest{
		SmallModel: "ministral-3-8b-instruct-2512",
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"ministral-3-8b-instruct-2512","type":"image-text-to-text","max_context_length":262144,"aliases":["openweight-small"]}]}`))
	}))
	defer server.Close()
	originalFetch := catalogueModelFetchFn
	catalogueModelFetchFn = func(ctx context.Context, client *http.Client, apiKey, _ string) (justcode.Catalogue, error) {
		return justcode.FetchCatalogue(ctx, client, apiKey, server.URL)
	}
	t.Cleanup(func() { catalogueModelFetchFn = originalFetch })
	var err error
	stderr := captureStderr(t, func() {
		err = modelsListProvider(context.Background(), justcode.InferenceProvider{Kind: justcode.ProviderAlbert, Name: "Albert API"}, justcode.UserSettings{}, "")
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "small model") || strings.Contains(stderr, "may have been removed") {
		t.Fatalf("valid small model reported as removed: %s", stderr)
	}
}

func TestResolveSmallModelDefaultsToMainModel(t *testing.T) {
	root := t.TempDir()
	if got := resolveSmallModelSelection(root); got != "" {
		t.Fatalf("no explicit small model expected, got %q", got)
	}
	content, err := justcode.ComposeConfigContent(justcode.ManagedOverlay{Model: "albert/gpt-oss-120b", SmallModel: resolveSmallModelSelection(root)})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(content), &config); err != nil {
		t.Fatal(err)
	}
	if config["small_model"] != config["model"] {
		t.Fatalf("small model must inherit main-model changes: %v", config)
	}
}

func TestParseGuestAlbertModels(t *testing.T) {
	choice := justcode.CuratedAlbertModels()[1]
	want := choice.AlbertModelConfig()
	data, err := json.Marshal([]justcode.AlbertModelConfig{want})
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseGuestAlbertModels(string(data))
	if err != nil || len(got) != 1 {
		t.Fatalf("parse guest model configs: models=%v err=%v", got, err)
	}
	if got[0].ID != want.ID || got[0].ModelID != want.ModelID || got[0].Output != want.Output || got[0].RequestBody["tool_choice"] != "auto" {
		t.Fatalf("guest model config = %+v, want %+v", got[0], want)
	}
	if parsed, err := parseGuestAlbertModels("null"); err != nil || len(parsed) != 0 {
		t.Fatalf("empty guest model config = %v, err=%v", parsed, err)
	}
	if _, err := parseGuestAlbertModels(`[{"id":""}]`); err == nil {
		t.Fatal("empty model ID should be rejected")
	}
}

func TestParsePortsArgs(t *testing.T) {
	parsed, err := parseArgs([]string{"ports", "add", "8080:80"})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.action != "ports" || strings.Join(parsed.portsArgs, " ") != "add 8080:80" {
		t.Fatalf("parsed ports command = action %q args %v", parsed.action, parsed.portsArgs)
	}
}

func TestParseMCPArgsAroundGlobalRuntimeFlags(t *testing.T) {
	for _, args := range [][]string{
		{"mcp", "status"},
		{"--microsandbox", "mcp", "status"},
		{"mcp", "status", "--microsandbox"},
	} {
		parsed, err := parseArgs(args)
		if err != nil {
			t.Fatalf("parseArgs(%v): %v", args, err)
		}
		if parsed.action != "mcp" || strings.Join(parsed.mcpArgs, " ") != "status" {
			t.Errorf("parseArgs(%v) = action %q, mcpArgs %v", args, parsed.action, parsed.mcpArgs)
		}
	}
}

func TestParseUpdateArgs(t *testing.T) {
	parsed, err := parseArgs([]string{"update", "--skill", "official/rgaa", "--yes"})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.action != "update" || strings.Join(parsed.updateArgs, " ") != "--skill official/rgaa --yes" {
		t.Fatalf("parsed = action %q, update args %v", parsed.action, parsed.updateArgs)
	}
}

func TestUpdateCommandSkipsStartupRecovery(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"--recover"}, true},
		{[]string{"--help"}, true},
		{[]string{"-h"}, true},
		{[]string{"--skill", "official/rgaa"}, false},
	} {
		if got := updateCommandSkipsStartupRecovery(tc.args); got != tc.want {
			t.Errorf("updateCommandSkipsStartupRecovery(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

func TestUpdateRecoveryFlagValidationAndPreview(t *testing.T) {
	if code, err := updateCmd([]string{"--rollback"}, t.TempDir()); code != 2 || err == nil {
		t.Fatalf("--rollback without --recover = (%d, %v); want usage error", code, err)
	}
	if code, err := updateCmd([]string{"--recover", "--rollback"}, t.TempDir()); code != 0 || err != nil {
		t.Fatalf("non-approved rollback preview = (%d, %v); want read-only preview", code, err)
	}
}

func TestParseGuestMCPIDs(t *testing.T) {
	ids, err := parseGuestMCPIDs("data-gouv,context7")
	if err != nil || strings.Join(ids, ",") != "context7,data-gouv" {
		t.Fatalf("guest MCP IDs = %v, %v", ids, err)
	}
	if ids, err := parseGuestMCPIDs(" "); err != nil || len(ids) != 0 {
		t.Fatalf("empty guest MCP IDs = %v, %v", ids, err)
	}
	if ids, err := parseGuestMCPIDs("context7,unknown"); err == nil || len(ids) != 0 {
		t.Fatalf("invalid guest MCP IDs = %v, %v; want an error", ids, err)
	}
}

func TestResolveMCPSelection(t *testing.T) {
	root := t.TempDir()
	if ids, err := resolveMCPSelection(root); err != nil || len(ids) != 0 {
		t.Fatalf("missing manifest selection = %v, %v", ids, err)
	}
	path := justcode.ProjectManifestPath(root)
	if err := justcode.WriteProjectManifest(justcode.DefaultFS, path, justcode.ProjectManifest{MCPConnectors: []string{"context7", "data-gouv"}}); err != nil {
		t.Fatal(err)
	}
	if ids, err := resolveMCPSelection(root); err != nil || strings.Join(ids, ",") != "context7,data-gouv" {
		t.Fatalf("valid manifest selection = %v, %v", ids, err)
	}
	if err := justcode.WriteProjectManifest(justcode.DefaultFS, path, justcode.ProjectManifest{MCPConnectors: []string{"browser"}}); err != nil {
		t.Fatal(err)
	}
	if ids, err := resolveMCPSelection(root); err == nil || len(ids) != 0 || !strings.Contains(err.Error(), "unknown managed MCP") {
		t.Fatalf("invalid manifest selection = %v, %v; want a hard validation error", ids, err)
	}
}

func TestOpenCodeAttachArgsResumesLastSession(t *testing.T) {
	resumeArgs, warn := justcode.OpenCodeResumeArgs(true, nil, "--continue continue the last session", nil)
	if warn {
		t.Fatal("supported CLI with a prior session must not warn")
	}
	got := strings.Join(openCodeAttachArgs("http://127.0.0.1:4096", resumeArgs), " ")
	want := "--server http://127.0.0.1:4096 --continue"
	if got != want {
		t.Fatalf("OpenCode attach args = %q, want %q", got, want)
	}
}

func TestOpenCodeAttachArgsFallsBackWhenContinueIsUnsupported(t *testing.T) {
	resumeArgs, warn := justcode.OpenCodeResumeArgs(true, nil, "Options: --session session id", nil)
	if !warn {
		t.Fatal("unsupported CLI with a prior session must warn")
	}
	got := strings.Join(openCodeAttachArgs("http://127.0.0.1:4096", resumeArgs), " ")
	want := "--server http://127.0.0.1:4096"
	if got != want {
		t.Fatalf("OpenCode attach args = %q, want the compatible fresh-session command %q", got, want)
	}
}

func TestOpenCodeAttachArgsStartsNewWithoutPriorSession(t *testing.T) {
	resumeArgs, warn := justcode.OpenCodeResumeArgs(false, nil, "--continue", nil)
	if warn || len(resumeArgs) != 0 {
		t.Fatalf("first launch resume decision = (%v, %t), want no flag and no warning", resumeArgs, warn)
	}
	got := strings.Join(openCodeAttachArgs("http://127.0.0.1:4096", resumeArgs), " ")
	want := "--server http://127.0.0.1:4096"
	if got != want {
		t.Fatalf("OpenCode attach args = %q, want %q", got, want)
	}
}

// TestParseArgsRejectsCode covers the deliberate break with the old CLI: the
// `code` command is gone and the error explains the replacement.
func TestParseArgsRejectsCode(t *testing.T) {
	_, err := parseArgs([]string{"code"})
	if err == nil {
		t.Fatal("expected an error for the removed `code` command")
	}
	if !strings.Contains(err.Error(), "'code' command was removed") {
		t.Fatalf("error = %q, want the migration message", err)
	}
	if !strings.Contains(err.Error(), "Microsandbox/full by default") {
		t.Fatalf("error = %q, want the default guest TUI behavior", err)
	}
}

func TestParseArgsRejectsUnknown(t *testing.T) {
	for _, args := range [][]string{{"foo"}, {"--podman"}, {"start", "stop"}, {"--docker", "--tart"}} {
		if _, err := parseArgs(args); err == nil {
			t.Errorf("parseArgs(%v): expected an error", args)
		}
	}
}

// TestParseArgsRejectsDocker covers the removal of the Docker runtime: --docker
// is no longer a known flag and must be reported as such.
func TestParseArgsRejectsDocker(t *testing.T) {
	_, err := parseArgs([]string{"--docker"})
	if err == nil || !strings.Contains(err.Error(), "Unknown argument: --docker") {
		t.Fatalf("error = %v, want an unknown-argument error for --docker", err)
	}
}

func TestParseArgsConflictingRuntimes(t *testing.T) {
	for _, args := range [][]string{{"--microsandbox", "--tart"}, {"--tart", "--agent-vm"}} {
		_, err := parseArgs(args)
		if err == nil || !strings.Contains(err.Error(), "Select exactly one runtime") {
			t.Fatalf("parseArgs(%v) = %v, want the runtime conflict message", args, err)
		}
	}
}

// TestRunNeedsNoRuntimeForLocalActions guards a regression where the dispatch
// resolved a runtime before handling actions that do not need one: `just-code
// version` used to fail with "select --microsandbox or --tart, ...".
func TestRunNeedsNoRuntimeForLocalActions(t *testing.T) {
	t.Setenv("RUNTIME", "")
	for _, args := range [][]string{{"version"}, {"help"}, {"-V"}} {
		if _, err := run(args); err != nil {
			t.Errorf("run(%v) = %v, want no error", args, err)
		}
	}
}

// TestApplyIsolationPrecedence pins the flag-over-environment contract at the
// entry point: an explicit --isolation value wins even when ISOLATION in .env
// is invalid, so a typo is always recoverable from the command line. The
// resolved value is applied to the returned config, which is what the backends
// are constructed from.
func TestApplyIsolationPrecedence(t *testing.T) {
	brokenEnv := justcode.Config{
		Isolation:    justcode.IsolationBackend,
		IsolationErr: fmt.Errorf("ISOLATION must be backend or full, got %q", "sometimes"),
	}

	// No flag, broken env: the error surfaces and the config is untouched.
	got, err := applyIsolation(brokenEnv, "", "")
	if err == nil {
		t.Fatal("an invalid ISOLATION must be surfaced when no flag is given")
	}
	if got.Isolation != justcode.IsolationBackend {
		t.Errorf("failed resolution must not change the level, got %q", got.Isolation)
	}

	// Explicit flag, broken env: the flag wins and the level is applied.
	got, err = applyIsolation(brokenEnv, "full", "")
	if err != nil {
		t.Fatalf("an explicit flag must override an invalid ISOLATION: %v", err)
	}
	if got.Isolation != justcode.IsolationFull {
		t.Errorf("Isolation = %q, want full (the resolved flag value)", got.Isolation)
	}
	if got.IsolationErr != nil {
		t.Errorf("IsolationErr must be cleared once the flag resolves the level: %v", got.IsolationErr)
	}

	// Explicit flag, valid env: the flag still wins.
	got, err = applyIsolation(justcode.Config{Isolation: justcode.IsolationBackend}, "full", "")
	if err != nil || got.Isolation != justcode.IsolationFull {
		t.Errorf("applyIsolation(valid env, full) = (%q, %v)", got.Isolation, err)
	}
}

// TestParseArgsStopAll pins the --all flag: it parses on the stop command and
// is rejected as a duplicate.
func TestParseArgsStopAll(t *testing.T) {
	p, err := parseArgs([]string{"stop", "--all"})
	if err != nil {
		t.Fatalf("parseArgs(stop --all): %v", err)
	}
	if p.action != "stop" || !p.stopAll {
		t.Fatalf("parseArgs(stop --all) = %+v, want stop with stopAll", p)
	}
	if _, err := parseArgs([]string{"stop", "--all", "--all"}); err == nil {
		t.Fatal("duplicate --all must be rejected")
	}
}

// TestParseArgsAllAcceptedAnywhere pins that --all, like the runtime flags,
// may appear in any position.
func TestParseArgsAllAcceptedAnywhere(t *testing.T) {
	p, err := parseArgs([]string{"--all", "stop"})
	if err != nil {
		t.Fatalf("parseArgs(--all stop): %v", err)
	}
	if p.action != "stop" || !p.stopAll {
		t.Fatalf("parseArgs(--all stop) = %+v", p)
	}
}

// TestWorkspaceSourceDefaultsToProjectRoot pins the P12 sealed-source rule: a
// zero-flag launch transfers the project the user is in, while an explicit
// WORKSPACE_DIR is never overridden.
func TestWorkspaceSourceDefaultsToProjectRoot(t *testing.T) {
	root := t.TempDir()
	pc := justcode.ProjectContext{Root: root, Name: "proj"}

	got := withProjectRootAsWorkspaceSource(justcode.Config{WorkspaceDir: "/configured"}, pc, nil)
	if got.WorkspaceDir != root {
		t.Fatalf("a zero-flag launch must use the project root, got %q", got.WorkspaceDir)
	}

	explicit := justcode.Config{WorkspaceDir: "/configured", WorkspaceDirSet: true}
	if got := withProjectRootAsWorkspaceSource(explicit, pc, nil); got.WorkspaceDir != "/configured" {
		t.Fatalf("an explicit source must be honoured, got %q", got.WorkspaceDir)
	}

	// Discovery failure leaves the value alone rather than guessing. This is
	// the case with NO explicit configuration, which is the one that would
	// otherwise be replaced by an empty root.
	unset := justcode.Config{WorkspaceDir: "/default-ish"}
	if got := withProjectRootAsWorkspaceSource(unset, pc, errDiscovery); got.WorkspaceDir != "/default-ish" {
		t.Fatalf("a discovery failure must not change the source, got %q", got.WorkspaceDir)
	}
	if got := withProjectRootAsWorkspaceSource(explicit, pc, errDiscovery); got.WorkspaceDir != "/configured" {
		t.Fatalf("a discovery failure must not change an explicit source, got %q", got.WorkspaceDir)
	}
}

var errDiscovery = errors.New("not a project")

// TestProjectManifestSizesTheGuest pins the precedence for guest sizing: an
// exported variable beats the project manifest, which beats the built-in
// default. Without this, the resource answer the project setup records would
// be displayed by 'config explain' and then ignored at launch.
func TestProjectManifestSizesTheGuest(t *testing.T) {
	root := t.TempDir()
	pm := justcode.ProjectManifest{SchemaVersion: 1, Project: "p", CPUs: 6, MemoryMB: 12288}
	if err := justcode.WriteProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(root), pm); err != nil {
		t.Fatal(err)
	}
	base := justcode.Config{CPUs: justcode.DefaultSandboxCPUs, MemoryMB: justcode.DefaultSandboxMemoryMB}

	got, err := applyProjectSandboxResources(base, root)
	if err != nil {
		t.Fatalf("applyProjectSandboxResources: %v", err)
	}
	if got.CPUs != 6 || got.MemoryMB != 12288 {
		t.Fatalf("manifest sizing not applied: %d cpus / %d MB", got.CPUs, got.MemoryMB)
	}

	// A value the SDK cannot carry is an error, not a silent wrap. (Checked
	// before any environment variable is set: an exported value would mask
	// the manifest, which is the next case.)
	pm.CPUs = justcode.MaxSandboxCPUs + 1
	if err := justcode.WriteProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(root), pm); err != nil {
		t.Fatal(err)
	}
	if _, err := applyProjectSandboxResources(base, root); err == nil {
		t.Fatal("an out-of-range manifest CPU count must be reported")
	}

	// An exported variable wins over the manifest, per variable.
	t.Setenv("JUST_CODE_CPUS", "2")
	got, err = applyProjectSandboxResources(base, root)
	if err != nil {
		t.Fatal(err)
	}
	if got.CPUs != justcode.DefaultSandboxCPUs {
		t.Fatalf("an exported CPU count must win over the manifest, got %d", got.CPUs)
	}
	if got.MemoryMB != 12288 {
		t.Fatalf("the unset variable must still let the manifest through, got %d", got.MemoryMB)
	}

	// No manifest is the normal zero-flag state, not an error.
	empty, err := applyProjectSandboxResources(base, t.TempDir())
	if err != nil || empty.CPUs != base.CPUs {
		t.Fatalf("a project without a manifest must keep the defaults: %d cpus, err %v", empty.CPUs, err)
	}
}

func TestEnforceHostResourceLimits(t *testing.T) {
	for _, key := range []string{"JUST_CODE_CPUS", "JUST_CODE_MEMORY_MB"} {
		value, present := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if present {
				_ = os.Setenv(key, value)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
	originalHostResources := detectHostResourcesFn
	hostResources := justcode.HostResources{CPUs: 1, MemoryMB: 6000}
	detectHostResourcesFn = func() (justcode.HostResources, error) { return hostResources, nil }
	t.Cleanup(func() { detectHostResourcesFn = originalHostResources })

	defaults := justcode.Config{CPUs: justcode.DefaultSandboxCPUs, MemoryMB: justcode.DefaultSandboxMemoryMB}
	got, err := enforceHostResourceLimits(defaults, t.TempDir())
	if err != nil {
		t.Fatalf("default sizing should fit after selecting the only CPU: %v", err)
	}
	if got.CPUs != 1 || got.MemoryMB != 4096 {
		t.Fatalf("single-choice/default sizing = %d CPUs / %d MiB, want 1 / 4096", got.CPUs, got.MemoryMB)
	}
	hostResources.MemoryMB = 4096
	got, err = enforceHostResourceLimits(defaults, t.TempDir())
	if err != nil || got.CPUs != 1 || got.MemoryMB != 3072 {
		t.Fatalf("low-memory defaults = %d CPUs / %d MiB, err=%v; want 1 / 3072", got.CPUs, got.MemoryMB, err)
	}
	hostResources.MemoryMB = 6000
	implicitRoot := t.TempDir()
	if err := justcode.WriteProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(implicitRoot), justcode.ProjectManifest{SchemaVersion: 1, Project: "implicit-defaults"}); err != nil {
		t.Fatal(err)
	}
	hostResources = justcode.HostResources{CPUs: 2, MemoryMB: 4096}
	got, err = enforceHostResourceLimits(defaults, implicitRoot)
	if err != nil || got.CPUs != 1 || got.MemoryMB != 3072 {
		t.Fatalf("small-host implicit defaults = %d CPUs / %d MiB, err=%v; want 1 / 3072", got.CPUs, got.MemoryMB, err)
	}
	hostResources = justcode.HostResources{CPUs: 8, MemoryMB: 16384}
	got, err = enforceHostResourceLimits(defaults, implicitRoot)
	if err != nil || got.CPUs != 2 || got.MemoryMB != 4096 {
		t.Fatalf("larger-host implicit defaults = %d CPUs / %d MiB, err=%v; want 2 / 4096", got.CPUs, got.MemoryMB, err)
	}
	implicitManifest, err := justcode.ReadProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(implicitRoot))
	if err != nil || implicitManifest.CPUs != 0 || implicitManifest.MemoryMB != 0 {
		t.Fatalf("host-adapted defaults were persisted: %+v, err=%v", implicitManifest, err)
	}
	hostResources = justcode.HostResources{CPUs: 1, MemoryMB: 6000}

	root := t.TempDir()
	manifest := justcode.ProjectManifest{SchemaVersion: 1, Project: "p", CPUs: 2, MemoryMB: 4096}
	if err := justcode.WriteProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(root), manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := enforceHostResourceLimits(defaults, root); err == nil || !strings.Contains(err.Error(), "exceeds detected host capacity") {
		t.Fatalf("over-cap manifest should be rejected, got %v", err)
	}
	t.Run("over-cap CPU environment value", func(t *testing.T) {
		t.Setenv("JUST_CODE_CPUS", "5")
		if _, err := enforceHostResourceLimits(justcode.Config{CPUs: 5, MemoryMB: 4096}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "exceeds detected host capacity") {
			t.Fatalf("over-cap CPU environment value should be rejected, got %v", err)
		}
	})
	t.Run("over-cap memory environment value", func(t *testing.T) {
		t.Setenv("JUST_CODE_MEMORY_MB", "10G")
		if _, err := enforceHostResourceLimits(justcode.Config{CPUs: 1, MemoryMB: 10240}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "exceeds detected host capacity") {
			t.Fatalf("over-cap memory environment value should be rejected, got %v", err)
		}
	})
}

func TestRuntimeUsesMicrosandbox(t *testing.T) {
	t.Setenv("RUNTIME", "tart")
	if runtimeUsesMicrosandbox("", justcode.RuntimeMicrosandbox) {
		t.Fatal("environment runtime should override the manifest")
	}
	if !runtimeUsesMicrosandbox("microsandbox", justcode.RuntimeTart) {
		t.Fatal("explicit runtime flag should override the environment")
	}
	t.Setenv("RUNTIME", "")
	if !runtimeUsesMicrosandbox("", justcode.RuntimeMicrosandbox) {
		t.Fatal("manifest runtime should be used when the environment is unset")
	}
}

// TestProjectManifestSelectsRuntimeAndIsolation pins that the two choices the
// project setup records are actually applied at launch, with the documented
// precedence: flag > exported variable > project manifest > built-in default.
// Before this, they were displayed by 'config explain' and ignored by the
// runtime.
func TestProjectManifestSelectsRuntimeAndIsolation(t *testing.T) {
	// applyIsolation reads the ambient variable before this test sets its own
	// value; do not let a developer shell's ISOLATION alter the manifest case.
	t.Setenv("ISOLATION", "")
	root := t.TempDir()
	pm := justcode.ProjectManifest{SchemaVersion: 1, Runtime: "tart", Isolation: "backend"}
	if err := justcode.WriteProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(root), pm); err != nil {
		t.Fatal(err)
	}
	runtimeChoice, isolationChoice := projectRuntimeIsolation(root)
	if runtimeChoice != justcode.RuntimeTart || isolationChoice != justcode.IsolationBackend {
		t.Fatalf("manifest choices = %q / %q", runtimeChoice, isolationChoice)
	}

	// A project value beats the built-in default.
	got, err := applyIsolation(justcode.Config{Isolation: justcode.IsolationFull}, "", isolationChoice)
	if err != nil {
		t.Fatalf("applyIsolation: %v", err)
	}
	if got.Isolation != justcode.IsolationBackend {
		t.Fatalf("isolation = %q, want the project's choice", got.Isolation)
	}

	// An exported variable beats the project value.
	t.Setenv("ISOLATION", string(justcode.IsolationFull))
	got, err = applyIsolation(justcode.Config{Isolation: justcode.IsolationFull}, "", isolationChoice)
	if err != nil {
		t.Fatal(err)
	}
	if got.Isolation != justcode.IsolationFull {
		t.Fatalf("isolation = %q, want the exported value to win", got.Isolation)
	}

	// An explicit flag beats both.
	t.Setenv("ISOLATION", string(justcode.IsolationFull))
	got, err = applyIsolation(justcode.Config{Isolation: justcode.IsolationFull}, "backend", justcode.IsolationFull)
	if err != nil {
		t.Fatal(err)
	}
	if got.Isolation != justcode.IsolationBackend {
		t.Fatalf("isolation = %q, want the flag to win", got.Isolation)
	}

	// A hand-edited manifest naming an unknown runtime is reported, not ignored.
	pm.Runtime = "podman"
	if err := justcode.WriteProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(root), pm); err != nil {
		t.Fatal(err)
	}
	runtimeChoice, _ = projectRuntimeIsolation(root)
	if _, err := justcode.ResolveRuntime("", string(runtimeChoice)); err == nil {
		t.Fatal("an unknown runtime in the manifest must be reported at launch")
	}

	// No manifest is the normal zero-flag state.
	runtimeChoice, isolationChoice = projectRuntimeIsolation(t.TempDir())
	if runtimeChoice != "" || isolationChoice != "" {
		t.Fatalf("a project without a manifest must yield no preference, got %q / %q", runtimeChoice, isolationChoice)
	}
}
