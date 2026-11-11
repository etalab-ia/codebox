package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/etalab-ia/codebox/internal/codebox"
)

// TestParseOptionalBindingKindRejectsAlbert pins the Codex P2 on PR #87:
// `bindings revoke albert` must not strip the mandatory Albert proxy
// registration, so the CLI validates the kind as an optional binding before
// touching approvals or runtime state.
func TestParseOptionalBindingKindRejectsAlbert(t *testing.T) {
	if _, err := codebox.ParseOptionalBindingKind("albert"); err == nil {
		t.Fatal("the required albert binding must be rejected")
	}
	if _, err := codebox.ParseOptionalBindingKind("gitlab"); err == nil {
		t.Fatal("an unknown kind must be rejected")
	}
	for _, ok := range []string{"github", "context7"} {
		if kind, err := codebox.ParseOptionalBindingKind(ok); err != nil || string(kind) != ok {
			t.Fatalf("optional kind %q: %q, %v", ok, kind, err)
		}
	}
}

// TestBindingsCmdRejectsRequiredKind covers the command surface itself, so a
// future refactor that bypasses the validation is caught here.
func TestBindingsCmdRejectsRequiredKind(t *testing.T) {
	for _, sub := range []string{"approve", "revoke"} {
		if code, err := bindingsCmd([]string{sub, "albert"}, "cb-test"); code != 2 || err == nil {
			t.Fatalf("bindings %s albert: code %d, err %v; want 2, error", sub, code, err)
		}
	}
}

func TestProjectGitHubRemoteRequiresHostLocalApproval(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://x-access-token:secret@github.com/owner/repo.git"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	project, err := codebox.DiscoverProject(root)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	remote, err := projectGitHubRemote(root, stateDir, project.InstanceName())
	if err != nil || remote.URL != "" {
		t.Fatalf("unapproved repository remote activated GitHub: remote=%#v err=%v", remote, err)
	}
	approvalPath := codebox.BindingApprovalsPath(stateDir, project.InstanceName())
	if err := codebox.ApproveBinding(codebox.DefaultFS, approvalPath, codebox.CredentialGithub); err != nil {
		t.Fatal(err)
	}
	remote, err = projectGitHubRemote(root, stateDir, project.InstanceName())
	if err != nil {
		t.Fatal(err)
	}
	if remote.URL != "https://github.com/owner/repo.git" || remote.Repo != "owner/repo" {
		t.Fatalf("approved origin = %#v", remote)
	}
}

// TestResolveCredentialRefReadsTheProjectRoot pins the Codex P1 on PR #87:
// invoked from a subdirectory of a worktree, the manifest must be read from
// the discovered project root, not from cwd, or the project's credentialRef
// is silently ignored and the user-level credential is injected instead.
func TestResolveCredentialRefReadsTheProjectRoot(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "pkg", "deep")
	if err := os.MkdirAll(codebox.ProjectStateDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"schemaVersion":1,"credentialRef":"project-key"}` + "\n")
	if err := os.WriteFile(codebox.ProjectManifestPath(root), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	// From the project root the manifest is found.
	if got := resolveCredentialRef(root); got != "project-key" {
		t.Fatalf("root resolution = %q", got)
	}
	// The env override still wins.
	t.Setenv("CODEBOX_CREDENTIAL_REF", "env-key")
	if got := resolveCredentialRef(root); got != "env-key" {
		t.Fatalf("env override = %q", got)
	}
}
