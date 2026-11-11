//go:build !windows

package codebox

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Non-regular inputs must be refused before the open: a FIFO would hang the
// hash and a device file would stream unbounded data into it.
func TestHashFileContentRefusesNonRegularFiles(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo-input.js")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("cannot create FIFO: %v", err)
	}
	if _, err := hashFileContent(dir, "fifo-input.js"); err == nil {
		t.Fatal("hash must refuse a FIFO")
	}
	if _, err := os.Lstat(fifo); err != nil {
		t.Fatalf("the FIFO must be left in place, not consumed: %v", err)
	}
	if _, err := os.Lstat("/dev/zero"); err != nil {
		t.Skip("no /dev/zero on this system")
	}
	if _, err := hashFileContent("/dev", "zero"); err == nil {
		t.Fatal("hash must refuse a device file instead of hashing an endless stream")
	}
}

// A symlink in an intermediate path component must be refused: the approval
// must not hash (and pin) a file that lives outside the checkout behind a
// symlinked parent directory.
func TestHashFileContentRefusesSymlinkedParentDir(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "hostile.ts"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".opencode", "plugin")); err != nil {
		t.Fatal(err)
	}
	if _, err := hashFileContent(root, ".opencode/plugin/hostile.ts"); err == nil {
		t.Fatal("hash must refuse a file reached through a symlinked parent directory")
	}
}

func TestDeclaredPluginDirectoryRefusesSymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".opencode", "declared")); err != nil {
		t.Fatal(err)
	}
	config := []byte(`{"plugins":["./.opencode/declared"]}`)
	if err := os.WriteFile(filepath.Join(root, "opencode.json"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverProjectExecutionInputs(root); err == nil {
		t.Fatal("declared plugin discovery must refuse a symlinked directory")
	}
}
