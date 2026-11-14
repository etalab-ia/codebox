package codebox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIsDotenvName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{".env", true},
		{".env.local", true},
		{".env.production", true},
		// P22 widened the family: `<prefix>.env` (docker.env, prod.env) is a
		// real dotenv file, and the earlier name-only rule let it cross.
		{"app.env", true},
		{"docker.env", true},
		// Case-insensitive: macOS and Windows filesystems are, so `.ENV` IS
		// `.env` there and a case-sensitive rule would exclude nothing.
		{".ENV", true},
		{".Env.Staging", true},
		{".env.example", false},
		{".env.sample", false},
		{".ENV.EXAMPLE", false},
		{"env", false},
		{".envrc", false},
		{"environment.md", false},
		{"README.md", false},
	}
	for _, c := range cases {
		if got := isDotenvName(c.name); got != c.want {
			t.Errorf("isDotenvName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestScanWorkspaceDotenvFiles(t *testing.T) {
	dir := t.TempDir()
	dotenv := map[string]bool{
		".env":              true,
		".env.local":        true,
		"sub/.env":          true,
		"sub/deep/.env.aws": true,
		".env.example":      false,
		"sub/.env.sample":   false,
		"package.json":      false,
		"src/main.go":       false,
		"node_modules/.env": true, // scanned: it is readable in the workspace
	}
	for name := range dotenv {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("X=1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := ScanWorkspace(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".env", ".env.local", "node_modules/.env", "sub/.env", "sub/deep/.env.aws"}
	if len(res.DotenvFiles) != len(want) {
		t.Fatalf("DotenvFiles = %v, want %v", res.DotenvFiles, want)
	}
	for i := range want {
		if res.DotenvFiles[i] != want[i] {
			t.Errorf("DotenvFiles[%d] = %q, want %q", i, res.DotenvFiles[i], want[i])
		}
	}
	if !res.Blocking() {
		t.Error("Blocking() must be true: dotenv files were found")
	}
}

func TestScanWorkspaceSymlinks(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	// A symlink named .env is itself a dotenv entry in the workspace.
	if err := os.Symlink(filepath.Join(outside, "real.env"), filepath.Join(dir, ".env")); err != nil {
		t.Skip("symlinks unavailable on this platform")
	}
	// A symlink to a directory outside the workspace must not be traversed.
	if err := os.Symlink(outside, filepath.Join(dir, "outside")); err != nil {
		t.Skip("symlinks unavailable on this platform")
	}
	if err := os.WriteFile(filepath.Join(outside, ".env"), []byte("X=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ScanWorkspace(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DotenvFiles) != 1 || res.DotenvFiles[0] != ".env" {
		t.Fatalf("DotenvFiles = %v, want [.env] (symlink flagged, target dir not traversed)", res.DotenvFiles)
	}
}

func TestScanWorkspaceClean(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte("X=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ScanWorkspace(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Blocking() {
		t.Errorf("clean workspace blocks: DotenvFiles=%v GitleaksFindings=%v", res.DotenvFiles, res.GitleaksFindings)
	}
}

func TestScanWorkspaceSymlinkedRoot(t *testing.T) {
	// WORKSPACE_DIR may itself be a symlink to the real project; the walk must
	// resolve the root (while still not following symlinks inside it).
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, ".env"), []byte("X=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable on this platform")
	}
	res, err := ScanWorkspace(context.Background(), link)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DotenvFiles) != 1 || res.DotenvFiles[0] != ".env" {
		t.Fatalf("DotenvFiles = %v, want [.env] resolved through the symlinked root", res.DotenvFiles)
	}
}

func TestScanWorkspaceMissingDir(t *testing.T) {
	_, err := ScanWorkspace(context.Background(), filepath.Join(t.TempDir(), "absent"))
	if err == nil {
		t.Fatal("scanning a missing directory must return an error")
	}
}

func TestCheckWorkspaceGateBlocksOnDotenv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("ALBERT_API_KEY=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := CheckWorkspaceGate(context.Background(), dir)
	if err == nil {
		t.Fatal("gate must block a workspace containing .env")
	}
	msg := err.Error()
	for _, want := range []string{"refusing to start", ".env", "Move real secrets"} {
		if !contains(msg, want) {
			t.Errorf("gate error missing %q:\n%s", want, msg)
		}
	}
}

func TestCheckWorkspaceGateClean(t *testing.T) {
	dir := t.TempDir()
	if err := CheckWorkspaceGate(context.Background(), dir); err != nil {
		t.Fatalf("clean workspace must not block: %v", err)
	}
}

// TestScanWorkspaceUsesBundledScanner exercises the scan through the injected
// bundled executable, with no scanner on PATH: after issue #177 the host has
// nothing to provide, so a test that required a host binary would test a state
// the product no longer has.
func TestScanWorkspaceUsesBundledScanner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stub is a POSIX shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.txt"),
		[]byte("password: \"hunter2correcthorsebatterystaple12345\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(t.TempDir(), "scanner")
	report := fmt.Sprintf(`[{"RuleID":"generic-api-key","File":%q,"StartLine":1,"Match":"REDACTED"}]`,
		filepath.Join(dir, "config.txt"))
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s' '"+report+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(stub))
	orig := gitleaksExecutable
	gitleaksExecutable = func(context.Context) (string, error) { return stub, nil }
	t.Cleanup(func() { gitleaksExecutable = orig })

	res, err := ScanWorkspace(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.GitleaksMissing {
		t.Fatal("a resolvable bundled scanner must not be reported missing")
	}
	if len(res.GitleaksFindings) == 0 {
		t.Fatal("the scanner reported no secret in a workspace containing one")
	}
	if !res.Blocking() {
		t.Error("findings must block")
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
