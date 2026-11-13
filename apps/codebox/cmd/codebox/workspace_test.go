package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
)

// captureStdout captures what fn writes to stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	fn()
	os.Stdout = old
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// captureStderr captures what fn writes to stderr, for the warnings that only
// exist as text.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	fn()
	os.Stderr = old
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// TestParseArgsWorkspaceCapturesSubcommand pins the dispatch contract: the
// words after `workspace` belong to the command.
func TestParseArgsWorkspaceCapturesSubcommand(t *testing.T) {
	p, err := parseArgs([]string{"workspace", "sync", "--force"})
	if err != nil {
		t.Fatal(err)
	}
	if p.action != "workspace" {
		t.Fatalf("action = %q", p.action)
	}
	if len(p.workspaceArgs) != 2 || p.workspaceArgs[0] != "sync" || p.workspaceArgs[1] != "--force" {
		t.Fatalf("workspaceArgs = %v", p.workspaceArgs)
	}
}

// TestWorkspaceCmdRejectsUnknownSubcommand keeps a typo from silently doing
// nothing.
func TestWorkspaceCmdRejectsUnknownSubcommand(t *testing.T) {
	code, err := workspaceCmd([]string{"frobnicate"}, codebox.Config{}, "cb-x")
	if code != 2 || err == nil {
		t.Fatalf("unknown subcommand must be a usage error: code=%d err=%v", code, err)
	}
}

// TestWorkspaceAllowRejectsPathOutsideTheProject pins the guard on recorded
// re-inclusions: a decision can only name a file inside the project.
func TestWorkspaceAllowRejectsPathOutsideTheProject(t *testing.T) {
	store := codebox.TransferOptInStore{Path: filepath.Join(t.TempDir(), "optin.json"), FS: codebox.DefaultFS}
	code, err := workspaceAllowCmd(store, t.TempDir(), "../outside")
	if code != 2 || err == nil {
		t.Fatalf("an escaping path must be rejected: code=%d err=%v", code, err)
	}
}

// TestWorkspaceStatusReportsFilteredSet pins the read-only review screen: it
// shows what crosses and what the filter refuses, without writing anything.
func TestWorkspaceStatusReportsFilteredSet(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=canary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("git"); err == nil {
		for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@e.com"}, {"config", "user.name", "T"}, {"add", "-A"}, {"commit", "-q", "-m", "i", "--allow-empty"}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v (%s)", args, err, out)
			}
		}
	}

	out := captureStdout(t, func() {
		if code, err := workspaceStatusCmd(dir, "cb-x", nil); code != 0 || err != nil {
			t.Fatalf("workspaceStatusCmd: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(out, "src/main.go") || !strings.Contains(out, ".env") {
		t.Fatalf("status must report both the crossing file and the exclusion: %q", out)
	}
	if !strings.Contains(out, "dotenv") {
		t.Fatalf("the exclusion reason must be shown: %q", out)
	}
	if !strings.Contains(out, "not mounted") {
		t.Fatalf("the status must state that the checkout is not mounted: %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "transfer-optin.json")); err == nil {
		t.Fatal("a read-only status must not write state")
	}
}

// TestWorkspaceStatusUsesConfiguredWorkspaceDir pins the transfer source of
// the workspace commands (PR #280 review): status reviews the configured
// workspace dir — the same source provisioning copies from — never a
// directory resolved independently of the configuration.
func TestWorkspaceStatusUsesConfiguredWorkspaceDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "from-config.txt"), []byte("config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if code, err := workspaceCmd([]string{"status"}, codebox.Config{WorkspaceDir: dir}, "cb-x"); code != 0 || err != nil {
			t.Fatalf("workspaceCmd status: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(out, "from-config.txt") {
		t.Fatalf("status must list the configured workspace dir's files, got:\n%s", out)
	}
}

// TestWorkspaceAllowRefusesStructuralExclusion pins the CLI half of the
// overridable rule: recording a decision for a symlink would create a rule
// that never applies, which reads as "allowed" while nothing crosses.
func TestWorkspaceAllowRefusesStructuralExclusion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	store := codebox.TransferOptInStore{Path: filepath.Join(t.TempDir(), "optin.json"), FS: codebox.DefaultFS}
	code, err := workspaceAllowCmd(store, dir, "link")
	if code == 0 || err == nil {
		t.Fatalf("a structural exclusion must be refused: code=%d err=%v", code, err)
	}
	if !strings.Contains(err.Error(), "structural") {
		t.Fatalf("the refusal must say why: %v", err)
	}
	records, lerr := store.Load()
	if lerr != nil {
		t.Fatal(lerr)
	}
	if _, ok := records["link"]; ok {
		t.Fatal("no rule may be recorded for a path that can never cross")
	}
}

// TestWorkspaceSyncRejectsUnknownOption keeps a typo from silently running an
// unforced sync.
func TestWorkspaceSyncRejectsUnknownOption(t *testing.T) {
	for _, args := range [][]string{{"sync", "--forc"}, {"export", "--bogus"}, {"export", "--out"}} {
		code, err := workspaceCmd(args, codebox.Config{}, "cb-x")
		if code != 2 || err == nil {
			t.Fatalf("%v must be a usage error: code=%d err=%v", args, code, err)
		}
	}
}

// TestWorkspaceAllowDenyTakeExactlyOnePath pins the argument strictness: an
// extra word must not be silently ignored, or a mistyped path reads as "the
// decision was recorded" while nothing was.
func TestWorkspaceAllowDenyTakeExactlyOnePath(t *testing.T) {
	for _, args := range [][]string{{"allow"}, {"allow", "a", "b"}, {"deny"}, {"deny", "a", "b"}} {
		code, err := workspaceCmd(args, codebox.Config{}, "cb-x")
		if code != 2 || err == nil {
			t.Fatalf("%v must be a usage error: code=%d err=%v", args, code, err)
		}
	}
}

// TestWorkspaceRecoverRejectsOptions keeps a stray word from being silently
// ignored: recovery has no flags, so any trailing token is a usage error.
func TestWorkspaceRecoverRejectsOptions(t *testing.T) {
	for _, args := range [][]string{{"recover", "--all"}, {"recover", "extra"}} {
		code, err := workspaceCmd(args, codebox.Config{}, "cb-x")
		if code != 2 || err == nil {
			t.Fatalf("%v must be a usage error: code=%d err=%v", args, code, err)
		}
	}
}
