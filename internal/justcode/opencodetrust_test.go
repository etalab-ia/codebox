package justcode

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
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
