//go:build !windows

package codebox

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A FIFO named like a config must stay in discovery (so the trust flow can
// block on it) while the read refuses it instead of hanging on the open.
func TestParseProjectOpenCodeConfigRefusesFIFO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("cannot create FIFO: %v", err)
	}
	if got := FindProjectOpenCodeConfig(dir); got != path {
		t.Fatalf("discovery must keep reporting the FIFO, got %q", got)
	}
	if _, err := ParseProjectOpenCodeConfig(dir, path); err == nil {
		t.Fatal("parse must refuse a FIFO instead of hanging on it")
	}
	if _, err := DiscoverProjectExecutionInputs(dir); err == nil {
		t.Fatal("input discovery must fail closed on a FIFO config")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("the FIFO must be left in place, not consumed: %v", err)
	}
}

// A symlinked plugin directory must fail discovery closed: OpenCode follows
// the symlink at runtime and executes whatever .js/.ts files the link target
// holds, so silently skipping it would under-report execution inputs.
func TestDiscoverProjectExecutionInputsRefusesSymlinkedPluginDir(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "hostile.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".opencode", "plugin")); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverProjectExecutionInputs(root); err == nil {
		t.Fatal("discovery must fail closed on a symlinked plugin directory")
	}
}

// A config reached through a symlinked parent directory must stay in
// discovery but be refused by the read: Lstat resolves intermediate
// components, so without the confined walk the read would follow the link
// outside the checkout.
func TestParseProjectOpenCodeConfigRefusesSymlinkedParentDir(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "opencode.json"), []byte(`{"plugin": ["./p.ts"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".opencode")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".opencode", "opencode.json")
	if got := FindProjectOpenCodeConfig(root); got != path {
		t.Fatalf("discovery must keep reporting the config behind the symlinked parent, got %q", got)
	}
	if _, err := ParseProjectOpenCodeConfig(root, path); err == nil {
		t.Fatal("parse must refuse a config behind a symlinked parent directory")
	}
	if _, err := DiscoverProjectExecutionInputs(root); err == nil {
		t.Fatal("input discovery must fail closed on a symlinked parent directory")
	}
}
