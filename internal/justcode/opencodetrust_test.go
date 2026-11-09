package justcode

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func trustProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// A project with an auto-discovered plugin and a config declaring an MCP
	// command.
	plugin := filepath.Join(dir, ".opencode", "plugin", "hook.js")
	if err := os.MkdirAll(filepath.Dir(plugin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plugin, []byte("// v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(cfg, []byte(`{"mcp":{"docs":{"command":"npx"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestTrustLifecycle(t *testing.T) {
	root := trustProject(t)
	fs, stateDir := DefaultFS, t.TempDir()

	// Before approval: both inputs are unapproved.
	unapproved, err := DiscoverUnapprovedInputs(fs, stateDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(unapproved) != 2 {
		t.Fatalf("both inputs must be unapproved: %+v", unapproved)
	}

	// Approval records the current content.
	if err := ApproveExecutionInputs(fs, stateDir, root); err != nil {
		t.Fatal(err)
	}
	unapproved, err = DiscoverUnapprovedInputs(fs, stateDir, root)
	if err != nil || len(unapproved) != 0 {
		t.Fatalf("after approval nothing may remain: %+v, %v", unapproved, err)
	}

	// A changed file is unapproved again: approval is tied to content.
	plugin := filepath.Join(root, ".opencode", "plugin", "hook.js")
	if err := os.WriteFile(plugin, []byte("// v2 malicious"), 0o644); err != nil {
		t.Fatal(err)
	}
	unapproved, err = DiscoverUnapprovedInputs(fs, stateDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(unapproved) != 1 || unapproved[0].Reason != "changed since approval" {
		t.Fatalf("a changed plugin must be unapproved as changed: %+v", unapproved)
	}

	// A new auto-discovered plugin appears: unapproved.
	if err := os.WriteFile(filepath.Join(root, ".opencode", "plugin", "second.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	unapproved, err = DiscoverUnapprovedInputs(fs, stateDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(unapproved) != 2 {
		t.Fatalf("changed + new plugin must both be unapproved: %+v", unapproved)
	}
}

func TestV2ExecutionDeclarationsRequireFreshTrust(t *testing.T) {
	for name, config := range map[string]string{
		"plugin string":      `{"plugins":["./declared-plugin"]}`,
		"plugin object":      `{"plugins":[{"package":"./declared-plugin","options":{"enabled":true}}]}`,
		"native MCP command": `{"mcp":{"servers":{"local":{"type":"local","command":["node","server.js"]}}}}`,
		"legacy MCP array":   `{"mcp":{"local":{"type":"local","command":["node","server.js"]}}}`,
		"native remote MCP":  `{"mcp":{"servers":{"docs":{"type":"remote","url":"https://docs.example/mcp"}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			root, state := t.TempDir(), t.TempDir()
			path := filepath.Join(root, "opencode.json")
			if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			if got, err := DiscoverUnapprovedInputs(DefaultFS, state, root); err != nil || len(got) != 1 {
				t.Fatalf("v2 execution declaration bypassed approval: %+v, %v", got, err)
			}
			if err := ApproveExecutionInputs(DefaultFS, state, root); err != nil {
				t.Fatal(err)
			}
			if got, err := DiscoverUnapprovedInputs(DefaultFS, state, root); err != nil || len(got) != 0 {
				t.Fatalf("approved declaration not recognized: %+v, %v", got, err)
			}
			if err := os.WriteFile(path, []byte(config+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if got, err := DiscoverUnapprovedInputs(DefaultFS, state, root); err != nil || len(got) != 1 || got[0].Reason != "changed since approval" {
				t.Fatalf("changed v2 declaration kept prior approval: %+v, %v", got, err)
			}
		})
	}
}

func TestDeclaredPluginCodeChangeRequiresFreshTrust(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	config := filepath.Join(root, "opencode.json")
	if err := os.WriteFile(config, []byte(`{"plugins":["./.opencode/declared"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(root, ".opencode", "declared", "lib", "hook.js")
	if err := os.MkdirAll(filepath.Dir(plugin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plugin, []byte("export const approved = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ApproveExecutionInputs(DefaultFS, state, root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plugin, []byte("export const approved = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := DiscoverUnapprovedInputs(DefaultFS, state, root)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(".opencode", "declared", "lib", "hook.js")
	if len(got) != 1 || got[0].Path != wantPath || got[0].Reason != "changed since approval" {
		t.Fatalf("changed declared plugin code kept prior approval: %+v", got)
	}
}

func TestNestedConfigDeclaredPluginCodeChangeRequiresFreshTrust(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	config := filepath.Join(root, ".opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(config), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(`{"plugins":["./declared"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(root, ".opencode", "declared", "index.js")
	if err := os.MkdirAll(filepath.Dir(plugin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plugin, []byte("export const approved = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ApproveExecutionInputs(DefaultFS, state, root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plugin, []byte("export const approved = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := DiscoverUnapprovedInputs(DefaultFS, state, root)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(".opencode", "declared", "index.js")
	if len(got) != 1 || got[0].Path != wantPath || got[0].Reason != "changed since approval" {
		t.Fatalf("nested config plugin code kept prior approval: %+v", got)
	}
}

func TestFileURLDeclaredPluginCodeChangeRequiresFreshTrust(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	pluginDir := filepath.Join(root, ".opencode", "declared")
	plugin := filepath.Join(pluginDir, "index.js")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plugin, []byte("export const approved = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pluginURLPath := filepath.ToSlash(pluginDir)
	if runtime.GOOS == "windows" {
		pluginURLPath = "/" + pluginURLPath
	}
	reference := (&url.URL{Scheme: "file", Path: pluginURLPath}).String()
	config := []byte(`{"plugins":[` + strconv.Quote(reference) + `]}`)
	if err := os.WriteFile(filepath.Join(root, "opencode.json"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ApproveExecutionInputs(DefaultFS, state, root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plugin, []byte("export const approved = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := DiscoverUnapprovedInputs(DefaultFS, state, root)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(".opencode", "declared", "index.js")
	if len(got) != 1 || got[0].Path != wantPath || got[0].Reason != "changed since approval" {
		t.Fatalf("file URL plugin code kept prior approval: %+v", got)
	}
}

func TestDeclaredPluginFileURLOutsideProjectRejected(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	outsidePath := filepath.ToSlash(outside)
	if runtime.GOOS == "windows" {
		outsidePath = "/" + outsidePath
	}
	reference := (&url.URL{Scheme: "file", Path: outsidePath}).String()
	config := []byte(`{"plugins":[` + strconv.Quote(reference) + `]}`)
	if err := os.WriteFile(filepath.Join(root, "opencode.json"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverProjectExecutionInputs(root); err == nil {
		t.Fatal("declared plugin outside the project must not bypass confinement")
	}
}

func TestTrustRecordNeverInRepository(t *testing.T) {
	root := trustProject(t)
	fs, stateDir := DefaultFS, t.TempDir()
	if err := ApproveExecutionInputs(fs, stateDir, root); err != nil {
		t.Fatal(err)
	}
	// The record exists in host state...
	path := OpenCodeTrustPath(stateDir, root)
	if _, err := fs.ReadFile(path); err != nil {
		t.Fatalf("the record must exist in host state: %v", err)
	}
	// ...and nowhere under the project root.
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && filepath.Base(p) == "opencode-trust.json" {
			t.Fatalf("trust record leaked into the repository: %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRemoteMCPEndpointChangeRequiresFreshTrust(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "opencode.json")
	if err := os.WriteFile(cfg, []byte(`{"mcp":{"docs":{"type":"remote","url":"https://docs.example/mcp"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	if got, err := DiscoverUnapprovedInputs(DefaultFS, stateDir, root); err != nil || len(got) != 1 {
		t.Fatalf("remote endpoint config should require approval: %+v, %v", got, err)
	}
	if err := ApproveExecutionInputs(DefaultFS, stateDir, root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(`{"mcp":{"docs":{"type":"remote","url":"https://changed.example/mcp"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := DiscoverUnapprovedInputs(DefaultFS, stateDir, root)
	if err != nil || len(got) != 1 || got[0].Reason != "changed since approval" {
		t.Fatalf("changed endpoint should require fresh approval: %+v, %v", got, err)
	}
}

func TestTrustRecordRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, ".opencode", "plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(pluginDir, "link.js")); err != nil {
		t.Skip("symlinks unavailable on this platform")
	}
	fs, stateDir := DefaultFS, t.TempDir()
	err := ApproveExecutionInputs(fs, stateDir, root)
	if err == nil {
		t.Fatal("a symlinked plugin must not be approvable: the path could be repointed after approval")
	}
}

func TestHashFileContentStreamsLargeFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big-input.js")
	// ~3 MiB forces the streaming hash through multiple io.Copy chunks; the
	// digest must match a whole-buffer sha256 of the same content.
	content := bytes.Repeat([]byte("just-code"), (3<<20)/len("just-code")+1)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := hashFileContent(dir, "big-input.js")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	if want := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("streaming hash mismatch: got %s want %s", got, want)
	}
}

// TestApproveDropsStaleEntries pins the re-approve semantics: an approval for
// a file that no longer exists must not survive to re-approve a re-created
// file of the same name with different content.
func TestApproveDropsStaleEntries(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, ".opencode", "plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(pluginDir, "hook.js")
	if err := os.WriteFile(plugin, []byte("// v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, stateDir := DefaultFS, t.TempDir()
	if err := ApproveExecutionInputs(fs, stateDir, root); err != nil {
		t.Fatal(err)
	}
	// Delete the plugin; re-approve (a no-inputs approve) must drop the
	// stale entry rather than keep it.
	if err := os.Remove(plugin); err != nil {
		t.Fatal(err)
	}
	if err := ApproveExecutionInputs(fs, stateDir, root); err != nil {
		t.Fatal(err)
	}
	// Re-create the same name with different content: unapproved.
	if err := os.WriteFile(plugin, []byte("// v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	unapproved, err := DiscoverUnapprovedInputs(fs, stateDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(unapproved) != 1 || unapproved[0].Reason != "unapproved" {
		t.Fatalf("a re-created file must need a fresh approval: %+v", unapproved)
	}
}

// piTrustProject builds a checkout whose Pi surface executes code at launch: an
// auto-discovered extension file, a directory extension with its entry point
// and a helper module, and the project-local settings Pi prefers over the
// managed global ones.
func piTrustProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, rel := range []string{
		filepath.Join(".pi", "settings.json"),
		filepath.Join(".pi", "extensions", "hook.ts"),
		filepath.Join(".pi", "extensions", "tool", "index.ts"),
		filepath.Join(".pi", "extensions", "tool", "helper.ts"),
	} {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "// v1"
		if rel == filepath.Join(".pi", "settings.json") {
			// Pi parses its settings as JSON, so the fixture has to be a JSON
			// document; the gate refuses a settings file it cannot parse
			// rather than assume it declares nothing.
			body = `{"extensions":[]}`
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestPiProjectInputsRequireTrust is the reproduction of the #345 review
// finding that Pi project inputs bypassed the trust gate. Pi auto-discovers
// .pi/extensions/*.ts and .pi/extensions/*/index.ts and reads the project-local
// .pi/settings.json — which can declare further extensions and packages — then
// runs all of it with the guest's permissions before the user sends a prompt.
// None of those files is VCS metadata, dotenv-named, git-ignored or gitleaks
// flagged, so the workspace transfer filter lets them cross into the guest: the
// host-local approval gate has to cover them exactly like the OpenCode plugin
// surface.
func TestPiProjectInputsRequireTrust(t *testing.T) {
	root := piTrustProject(t)
	fs, stateDir := DefaultFS, t.TempDir()

	unapproved, err := DiscoverUnapprovedInputs(fs, stateDir, root)
	if err != nil {
		t.Fatal(err)
	}
	unapprovedSet := map[string]bool{}
	for _, u := range unapproved {
		unapprovedSet[u.Path] = true
	}
	want := []string{
		filepath.Join(".pi", "settings.json"),
		filepath.Join(".pi", "extensions", "hook.ts"),
		filepath.Join(".pi", "extensions", "tool", "index.ts"),
		filepath.Join(".pi", "extensions", "tool", "helper.ts"),
	}
	for _, rel := range want {
		if !unapprovedSet[rel] {
			t.Fatalf("%s runs at Pi launch and must need approval: %+v", rel, unapproved)
		}
	}
	if len(unapproved) != len(want) {
		t.Fatalf("unexpected extra inputs: %+v", unapproved)
	}

	// Approval records the current content of every Pi input, and the launch
	// gate stops firing.
	if err := ApproveExecutionInputs(fs, stateDir, root); err != nil {
		t.Fatal(err)
	}
	unapproved, err = DiscoverUnapprovedInputs(fs, stateDir, root)
	if err != nil || len(unapproved) != 0 {
		t.Fatalf("after approval nothing may remain: %+v, %v", unapproved, err)
	}

	// A repository can change an extension after approval; approval is tied to
	// content, so the changed file is a new decision.
	hook := filepath.Join(root, ".pi", "extensions", "hook.ts")
	if err := os.WriteFile(hook, []byte("// v2 exfiltrates secrets"), 0o644); err != nil {
		t.Fatal(err)
	}
	unapproved, err = DiscoverUnapprovedInputs(fs, stateDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(unapproved) != 1 || unapproved[0].Path != filepath.Join(".pi", "extensions", "hook.ts") ||
		unapproved[0].Reason != "changed since approval" {
		t.Fatalf("a changed extension must need fresh approval: %+v", unapproved)
	}
}

// TestPiDeclaredExtensionContentRequiresFreshApproval is the reproduction of the
// #345 review finding on the declaring settings file: Pi resolves the local
// paths an "extensions" entry names, so approving the settings file pins the
// declaration but not the code it points at. A change pulled into the declared
// file must be a new decision.
func TestPiDeclaredExtensionContentRequiresFreshApproval(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".pi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".pi", "settings.json"),
		[]byte(`{"extensions":["../hooks/startup.ts"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	declared := filepath.Join(root, "hooks", "startup.ts")
	if err := os.WriteFile(declared, []byte("// v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, stateDir := DefaultFS, t.TempDir()
	unapproved, err := DiscoverUnapprovedInputs(fs, stateDir, root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		filepath.Join(".pi", "settings.json"): true,
		filepath.Join("hooks", "startup.ts"):  true,
	}
	for _, u := range unapproved {
		delete(want, u.Path)
	}
	if len(want) != 0 || len(unapproved) != 2 {
		t.Fatalf("a declared extension runs at launch and must need approval: %+v", unapproved)
	}

	if err := ApproveExecutionInputs(fs, stateDir, root); err != nil {
		t.Fatal(err)
	}
	// The declaration in the settings file is untouched while the code it names
	// is replaced: the settings hash alone would let this through unreviewed.
	if err := os.WriteFile(declared, []byte("// v2 exfiltrates secrets"), 0o644); err != nil {
		t.Fatal(err)
	}
	unapproved, err = DiscoverUnapprovedInputs(fs, stateDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(unapproved) != 1 || unapproved[0].Path != filepath.Join("hooks", "startup.ts") ||
		unapproved[0].Reason != "changed since approval" {
		t.Fatalf("a changed declared extension must need fresh approval: %+v", unapproved)
	}
}

// TestPiProjectSettingsAloneRequireTrust covers the other half of the same
// surface: the project-local settings file is itself an execution input because
// its schema can declare extensions and packages, so a project carrying nothing
// but .pi/settings.json still needs approval before Pi reads it.
func TestPiProjectSettingsAloneRequireTrust(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, ".pi", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"packages":["npm:some-tool"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	inputs, err := DiscoverProjectExecutionInputs(root)
	if err != nil {
		t.Fatal(err)
	}
	if inputs.PiProjectConfig != settings {
		t.Fatalf("project-local Pi settings must be an input: %q", inputs.PiProjectConfig)
	}
	unapproved, err := DiscoverUnapprovedInputs(DefaultFS, t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(unapproved) != 1 || unapproved[0].Path != filepath.Join(".pi", "settings.json") {
		t.Fatalf("declared Pi packages must need approval: %+v", unapproved)
	}
}

// TestPiProjectInputsRefuseSymlinks keeps the surface fail-closed: a symlinked
// settings file or extension must be refused rather than skipped, so content
// outside the approval boundary cannot hide behind a link.
func TestPiProjectInputsRefuseSymlinks(t *testing.T) {
	outside := t.TempDir()
	target := filepath.Join(outside, "hook.ts")
	if err := os.WriteFile(target, []byte("// outside the checkout"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		filepath.Join(".pi", "settings.json"),
		filepath.Join(".pi", "extensions", "hook.ts"),
	} {
		root := t.TempDir()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := DiscoverProjectExecutionInputs(root); err == nil {
			t.Fatalf("%s is a symlink and must be refused, not followed", rel)
		}
	}
}
