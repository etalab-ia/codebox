package codebox

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSplitGitHubSlug(t *testing.T) {
	for _, tt := range []struct {
		slug       string
		wantOwner  string
		wantRepo   string
		wantPieces bool
	}{
		{slug: "owner/repo", wantOwner: "owner", wantRepo: "repo", wantPieces: true},
		{slug: "a/b/c", wantOwner: "a", wantRepo: "b/c", wantPieces: true},
		{slug: "owner", wantPieces: false},
		{slug: "", wantPieces: false},
	} {
		owner, repo := splitGitHubSlug(tt.slug)
		if tt.wantPieces {
			if owner != tt.wantOwner || repo != tt.wantRepo {
				t.Fatalf("splitGitHubSlug(%q) = %q, %q; want %q, %q", tt.slug, owner, repo, tt.wantOwner, tt.wantRepo)
			}
		} else if owner != "" || repo != "" {
			t.Fatalf("splitGitHubSlug(%q) = %q, %q; want empty", tt.slug, owner, repo)
		}
	}
}

func TestProbeGitHubProjectAt(t *testing.T) {
	for _, tt := range []struct {
		name       string
		userStatus int
		userBody   string
		repoStatus int
		repoBody   string
		// exhaustedRateLimit sends the header GitHub sets when the window is
		// spent, which must be read as "unknown", not as insufficient rights.
		exhaustedRateLimit bool
		// pullsStatus is the answer from the pull-request listing, which needs
		// the Pull requests permission; zero means "OK, empty list".
		pullsStatus int
		// pullsThrottled marks that answer as a spent rate limit rather than a
		// permission refusal.
		pullsThrottled bool
		wantLogin      string
		wantRights     GitHubRepoRights
	}{
		{
			name:       "read and push is OK",
			userStatus: http.StatusOK, userBody: `{"login":"kai"}`,
			repoStatus: http.StatusOK, repoBody: `{"permissions":{"push":true}}`,
			wantLogin: "kai", wantRights: GitHubRepoRightsOK,
		},
		{
			// A fine-grained token can report push access (a repository role)
			// and still lack the Pull requests permission, in which case the
			// draft PR the wizard promises would be rejected. Both must hold.
			name:       "push without pull-request access is insufficient",
			userStatus: http.StatusOK, userBody: `{"login":"kai"}`,
			repoStatus: http.StatusOK, repoBody: `{"permissions":{"push":true}}`,
			pullsStatus: http.StatusForbidden,
			wantLogin:   "kai", wantRights: GitHubRepoRightsInsufficient,
		},
		{
			// A throttle on the pull-request check must not be reported as a
			// rights problem: the token may be fine and the remedy is to retry.
			name:       "throttled pull-request check is unknown, not insufficient",
			userStatus: http.StatusOK, userBody: `{"login":"kai"}`,
			repoStatus: http.StatusOK, repoBody: `{"permissions":{"push":true}}`,
			pullsStatus: http.StatusForbidden, pullsThrottled: true,
			wantLogin: "kai", wantRights: GitHubRepoRightsUnknown,
		},
		{
			name:       "read without push is insufficient",
			userStatus: http.StatusOK, userBody: `{"login":"kai"}`,
			repoStatus: http.StatusOK, repoBody: `{"permissions":{"push":false}}`,
			wantLogin: "kai", wantRights: GitHubRepoRightsInsufficient,
		},
		{
			name:       "404 is no access",
			userStatus: http.StatusOK, userBody: `{"login":"kai"}`,
			repoStatus: http.StatusNotFound, repoBody: `{"message":"Not Found"}`,
			wantLogin: "kai", wantRights: GitHubRepoRightsNoAccess,
		},
		{
			name:       "401 is invalid",
			userStatus: http.StatusOK, userBody: `{"login":"kai"}`,
			repoStatus: http.StatusUnauthorized, repoBody: `{"message":"Bad credentials"}`,
			wantLogin: "kai", wantRights: GitHubRepoRightsInvalid,
		},
		{
			name:       "403 with organization hint is org pending",
			userStatus: http.StatusOK, userBody: `{"login":"kai"}`,
			repoStatus: http.StatusForbidden, repoBody: `{"message":"the organization has not approved access"}`,
			wantLogin: "kai", wantRights: GitHubRepoRightsOrgPending,
		},
		{
			name:       "403 without organization hint is insufficient",
			userStatus: http.StatusOK, userBody: `{"login":"kai"}`,
			repoStatus: http.StatusForbidden, repoBody: `{"message":"Resource not accessible by personal access token"}`,
			wantLogin: "kai", wantRights: GitHubRepoRightsInsufficient,
		},
		{
			name:       "403 rate limit is unknown, not insufficient rights",
			userStatus: http.StatusOK, userBody: `{"login":"kai"}`,
			repoStatus: http.StatusForbidden, repoBody: `{"message":"API rate limit exceeded for user ID 1."}`,
			wantLogin: "kai", wantRights: GitHubRepoRightsUnknown,
		},
		{
			name:       "403 with exhausted rate limit header is unknown",
			userStatus: http.StatusOK, userBody: `{"login":"kai"}`,
			repoStatus: http.StatusForbidden, repoBody: `{"message":"Forbidden"}`,
			exhaustedRateLimit: true,
			wantLogin:          "kai", wantRights: GitHubRepoRightsUnknown,
		},
		{
			name:       "403 saml enforcement is its own cause",
			userStatus: http.StatusOK, userBody: `{"login":"kai"}`,
			repoStatus: http.StatusForbidden, repoBody: `{"message":"Resource protected by organization SAML enforcement. You must grant your token access to this organization."}`,
			wantLogin: "kai", wantRights: GitHubRepoRightsSSO,
		},
		{
			name:       "user endpoint failure still classifies from the repo",
			userStatus: http.StatusInternalServerError, userBody: ``,
			repoStatus: http.StatusOK, repoBody: `{"permissions":{"push":true}}`,
			wantLogin: "", wantRights: GitHubRepoRightsOK,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.pullsStatus == 0 {
				tt.pullsStatus = http.StatusOK
			}
			mux := http.NewServeMux()
			mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.userStatus)
				w.Write([]byte(tt.userBody))
			})
			mux.HandleFunc("/repos/owner/repo/pulls", func(w http.ResponseWriter, r *http.Request) {
				if tt.pullsThrottled {
					w.Header().Set("X-RateLimit-Remaining", "0")
				}
				w.WriteHeader(tt.pullsStatus)
				w.Write([]byte(`[]`))
			})
			mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
				if tt.exhaustedRateLimit {
					w.Header().Set("X-RateLimit-Remaining", "0")
				}
				w.WriteHeader(tt.repoStatus)
				w.Write([]byte(tt.repoBody))
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()

			got := probeGitHubProjectAt(context.Background(), srv.Client(), "tok", "owner", "repo", srv.URL+"/user", srv.URL+"/repos/owner/repo", srv.URL+"/repos/owner/repo/pulls")
			if got.login != tt.wantLogin {
				t.Fatalf("login = %q, want %q", got.login, tt.wantLogin)
			}
			if got.rights != tt.wantRights {
				t.Fatalf("rights = %v, want %v", got.rights, tt.wantRights)
			}
		})
	}
}

func TestParseGitHubRemote(t *testing.T) {
	for _, tt := range []struct {
		name     string
		input    string
		wantURL  string
		wantRepo string
		wantErr  bool
	}{
		{name: "https", input: "https://github.com/owner/repo", wantURL: "https://github.com/owner/repo.git", wantRepo: "owner/repo"},
		{name: "https trailing slash", input: "https://github.com/owner/repo.git/", wantURL: "https://github.com/owner/repo.git", wantRepo: "owner/repo"},
		{name: "https strips embedded credentials", input: "https://x-access-token:secret@github.com/owner/repo.git", wantURL: "https://github.com/owner/repo.git", wantRepo: "owner/repo"},
		{name: "ssh", input: "ssh://git@github.com/owner/repo.git", wantURL: "https://github.com/owner/repo.git", wantRepo: "owner/repo"},
		{name: "scp", input: "git@github.com:owner/repo.git", wantURL: "https://github.com/owner/repo.git", wantRepo: "owner/repo"},
		{name: "http is rejected", input: "http://github.com/owner/repo.git", wantErr: true},
		{name: "enterprise host is rejected", input: "https://github.example.net/owner/repo.git", wantErr: true},
		{name: "nested path is rejected", input: "https://github.com/owner/group/repo.git", wantErr: true},
		{name: "query is rejected", input: "https://github.com/owner/repo.git?token=secret", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseGitHubRemote(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseGitHubRemote(%q) error = %v, wantErr %t", tt.input, err, tt.wantErr)
			}
			if err == nil && (got.URL != tt.wantURL || got.Repo != tt.wantRepo) {
				t.Fatalf("ParseGitHubRemote(%q) = %#v, want URL %q and repo %q", tt.input, got, tt.wantURL, tt.wantRepo)
			}
			if err == nil && got.URL == tt.input && tt.name == "https strips embedded credentials" {
				t.Fatal("remote credentials must not be returned")
			}
		})
	}
}

func TestGitHubOriginForProjectReadsAndSanitizesOrigin(t *testing.T) {
	root := t.TempDir()
	runGitTest(t, root, "init", "-q")
	runGitTest(t, root, "remote", "add", "origin", "https://x-access-token:secret@github.com/owner/repo.git")

	got, err := GitHubOriginForProject(context.Background(), root)
	if err != nil {
		t.Fatalf("GitHubOriginForProject: %v", err)
	}
	if got.URL != "https://github.com/owner/repo.git" || got.Repo != "owner/repo" {
		t.Fatalf("origin = %#v", got)
	}
}

func TestGitHubOriginForProjectRejectsAmbiguousOrigins(t *testing.T) {
	root := t.TempDir()
	runGitTest(t, root, "init", "-q")
	runGitTest(t, root, "remote", "add", "origin", "https://github.com/owner/repo.git")
	runGitTest(t, root, "remote", "set-url", "--add", "origin", "https://github.com/owner/other.git")
	if _, err := GitHubOriginForProject(context.Background(), root); err == nil {
		t.Fatal("multiple origin URLs must be rejected")
	}
}

func TestGitHubCLIInstallScriptIsVersionAndChecksumPinned(t *testing.T) {
	script := githubCLIInstallScript()
	for _, want := range []string{
		"version=" + githubCLIVersion,
		githubCLIAMD64SHA,
		githubCLIARM64SHA,
		"sha256sum -c -",
		"https://github.com/cli/cli/releases/download/v${version}/${asset}",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("pinned installer is missing %q", want)
		}
	}
	if strings.Contains(script, "sudo ") || strings.Contains(script, "GITHUB_TOKEN") {
		t.Fatal("GitHub CLI installer must not use host privilege escalation or credentials")
	}
}

// TestGitHubCLIInstallScriptCannotExitComposedStartScript pins the P16 fix:
// the installer is concatenated into the guest start script (prep + gh +
// devtools + exec opencode serve), so an early `exit 0` when gh is already
// installed would terminate the whole script and leave the backend down on
// every restart. The early exit must be scoped to the installer function.
func TestGitHubCLIInstallScriptCannotExitComposedStartScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guest start script uses POSIX shell commands")
	}
	script := githubCLIInstallScript()
	if !strings.Contains(script, "codebox_install_gh() (") {
		t.Fatal("installer must be wrapped in a function so its early exit cannot terminate the composed start script")
	}
	// Simulate the composed script: everything after the installer must run
	// even when the installer takes its already-installed early exit. The
	// installer checks a fixed path, so point that path at a fake preinstalled
	// gh in the test sandbox instead of relying on the host's /usr/local/bin.
	ghPath := filepath.Join(t.TempDir(), "gh")
	ghScript := "#!/bin/sh\necho \"gh version " + githubCLIVersion + "\"\n"
	if err := os.WriteFile(ghPath, []byte(ghScript), 0o755); err != nil {
		t.Fatal(err)
	}
	composed := strings.Replace(script, "binary=/usr/local/bin/gh", "binary="+ghPath, 1) + "echo reached-tail\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "start")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+composed), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", path)
	cmd.Env = append(os.Environ(), "HOME="+dir)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("composed start script must survive an already-installed gh: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "reached-tail") {
		t.Fatalf("tail after the installer did not run; output: %s", out)
	}
}

func TestGitHubBranchNameIsUniqueAndScoped(t *testing.T) {
	first, err := GitHubBranchName("project name/with spaces")
	if err != nil {
		t.Fatal(err)
	}
	second, err := GitHubBranchName("project name/with spaces")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !strings.HasPrefix(first, "codebox/") || strings.Contains(first, " ") || strings.Contains(first, "..") {
		t.Fatalf("branch names are not unique, scoped and safe: %q, %q", first, second)
	}
}

func TestGitHubSnapshotBranchScriptRefusesChangesAndKeepsRemoteBase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guest branch bootstrap uses POSIX shell commands")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "origin.git")
	cmd := exec.Command("git", "init", "-q", "--bare", remote)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create remote: %v\n%s", err, out)
	}
	runGitTest(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")

	base := filepath.Join(root, "base")
	if err := os.Mkdir(base, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, base, "init", "-q")
	runGitTest(t, base, "symbolic-ref", "HEAD", "refs/heads/main")
	runGitTest(t, base, "config", "user.name", "Base")
	runGitTest(t, base, "config", "user.email", "base@example.test")
	if err := os.WriteFile(filepath.Join(base, "shared.txt"), []byte("remote version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "remote-only.txt"), []byte("preserve me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, base, "add", "-f", "-A")
	runGitTest(t, base, "commit", "-qm", "remote base")
	runGitTest(t, base, "remote", "add", "origin", remote)
	runGitTest(t, base, "push", "-u", "origin", "main")
	baseCommit := strings.TrimSpace(gitTestOutput(t, base, "rev-parse", "HEAD"))

	snapshot := filepath.Join(root, "snapshot")
	if err := os.Mkdir(snapshot, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, snapshot, "init", "-q")
	runGitTest(t, snapshot, "config", "user.name", "Guest")
	runGitTest(t, snapshot, "config", "user.email", "guest@example.test")
	if err := os.WriteFile(filepath.Join(snapshot, "shared.txt"), []byte("filtered host version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshot, "host-only.txt"), []byte("filtered new file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshot, ".gitignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, snapshot, "add", "-A")
	runGitTest(t, snapshot, "commit", "-qm", "P22 filtered snapshot")
	runGitTest(t, snapshot, "commit", "--allow-empty", "-qm", "Prior guest history")

	script := githubSnapshotBranchScript(snapshot, GitHubRemote{URL: remote, Repo: "owner/repo"}, "main", "codebox/test")
	ignored := filepath.Join(snapshot, "ignored", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(ignored), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ignored, []byte("ignored guest work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runShellTest(script, snapshot); err == nil || !strings.Contains(string(out), "ignored files") {
		t.Fatalf("ignored guest files must block setup: err=%v out=%s", err, out)
	}
	if got, err := os.ReadFile(ignored); err != nil || string(got) != "ignored guest work\n" {
		t.Fatalf("ignored guest work was changed: content=%q err=%v", got, err)
	}
	if err := os.RemoveAll(filepath.Dir(ignored)); err != nil {
		t.Fatal(err)
	}
	dirty := filepath.Join(snapshot, "dirty.txt")
	if err := os.WriteFile(dirty, []byte("uncommitted guest work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runShellTest(script, snapshot); err == nil || !strings.Contains(string(out), "worktree is dirty") {
		t.Fatalf("dirty guest worktree must block setup: err=%v out=%s", err, out)
	}
	if got, err := os.ReadFile(dirty); err != nil || string(got) != "uncommitted guest work\n" {
		t.Fatalf("uncommitted guest work was changed: content=%q err=%v", got, err)
	}
	if err := os.Remove(dirty); err != nil {
		t.Fatal(err)
	}
	before := strings.TrimSpace(gitTestOutput(t, snapshot, "rev-parse", "HEAD"))
	if out, err := runShellTest(script, snapshot); err == nil || !strings.Contains(string(out), "publication n’a pas été approuvée") {
		t.Fatalf("unapproved snapshot must fail before publishing the branch: err=%v out=%s", err, out)
	}
	if got := strings.TrimSpace(gitTestOutput(t, snapshot, "rev-parse", "HEAD")); got != before {
		t.Fatal("failed tree preparation changed guest HEAD")
	}
	if got := gitTestOutput(t, snapshot, "status", "--porcelain"); got != "" {
		t.Fatalf("failed tree preparation changed the live index: %s", got)
	}
	if got := gitTestOutput(t, snapshot, "remote"); got != "" {
		t.Fatalf("refusal configured a remote: %s", got)
	}
	if got := gitTestOutput(t, snapshot, "for-each-ref", "--format=%(refname)", "refs/heads/codebox/"); got != "" {
		t.Fatalf("refusal created a publishable branch or backup: %s", got)
	}
	// Resolve the delta explicitly in the fixture. Remote-only files remain
	// preserved by the overlay; missing filtered files are not deletions.
	if err := os.WriteFile(filepath.Join(snapshot, "shared.txt"), []byte("remote version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"host-only.txt", ".gitignore"} {
		if err := os.Remove(filepath.Join(snapshot, path)); err != nil {
			t.Fatal(err)
		}
	}
	runGitTest(t, snapshot, "add", "-A")
	runGitTest(t, snapshot, "commit", "-qm", "reviewed remote-equivalent snapshot")
	before = strings.TrimSpace(gitTestOutput(t, snapshot, "rev-parse", "HEAD"))
	shim := t.TempDir()
	if err := os.WriteFile(filepath.Join(shim, "mv"), []byte("#!/bin/sh\nexit 70\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	interrupted := githubSnapshotBranchScript(snapshot, GitHubRemote{URL: remote}, "main", "codebox/interrupted")
	cmd = exec.Command("/bin/sh", "-c", interrupted)
	cmd.Dir = snapshot
	cmd.Env = append(os.Environ(), "PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(root, "empty-gitconfig"))
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("failed config publication must be surfaced: %s", out)
	}
	if got := gitTestOutput(t, snapshot, "remote"); got != "" {
		t.Fatalf("failed publication exposed a half-configured origin: %s", got)
	}
	if got := gitTestOutput(t, snapshot, "status", "--porcelain"); got != "" {
		t.Fatalf("failed publication left an unresumable dirty workspace: %s", got)
	}
	if got := strings.TrimSpace(gitTestOutput(t, snapshot, "rev-parse", "codebox/interrupted-snapshot-backup")); got != before {
		t.Fatal("interrupted publication lost original guest history")
	}
	preservedBeforeSuccess := strings.TrimSpace(gitTestOutput(t, snapshot, "rev-parse", "HEAD"))
	cmd = exec.Command("/bin/sh", "-c", script)
	cmd.Dir = snapshot
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(root, "empty-gitconfig"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed guest branch: %v\n%s", err, out)
	}

	if got := strings.TrimSpace(gitTestOutput(t, snapshot, "branch", "--show-current")); got != "codebox/test" {
		t.Fatalf("current branch = %q", got)
	}
	if got := strings.TrimSpace(gitTestOutput(t, snapshot, "rev-parse", "HEAD")); got != baseCommit {
		t.Fatalf("branch tip = %s, want remote base %s without a synthetic commit", got, baseCommit)
	}
	for path, want := range map[string]string{
		"shared.txt":      "remote version\n",
		"remote-only.txt": "preserve me\n",
	} {
		if got := gitTestOutput(t, snapshot, "show", "HEAD:"+path); got != want {
			t.Errorf("HEAD:%s = %q, want %q", path, got, want)
		}
	}
	if got := strings.TrimSpace(gitTestOutput(t, snapshot, "remote", "get-url", "origin")); got != remote {
		t.Fatalf("origin = %q, want %q", got, remote)
	}
	if got := strings.TrimSpace(gitTestOutput(t, snapshot, "status", "--porcelain")); got != "" {
		t.Fatalf("worktree status after seeding = %q, want clean", got)
	}
	if got := strings.TrimSpace(gitTestOutput(t, snapshot, "rev-parse", "codebox/test-snapshot-backup")); got != preservedBeforeSuccess {
		t.Fatalf("guest history backup = %s, want %s", got, preservedBeforeSuccess)
	}
	if got := strings.TrimSpace(gitTestOutput(t, snapshot, "config", "--local", "codebox.github-workflow-origin")); got != remote {
		t.Fatalf("published origin marker = %q, want %q", got, remote)
	}
	if got := strings.TrimSpace(gitTestOutput(t, snapshot, "config", "--local", "remote.origin.fetch")); got != "+refs/heads/*:refs/remotes/origin/*" {
		t.Fatalf("published origin fetch refspec = %q", got)
	}
	if got := strings.TrimSpace(gitTestOutput(t, snapshot, "config", "--local", "codebox.github-workflow-snapshot-policy")); got != "remote-only-v1" {
		t.Fatalf("published snapshot policy = %q", got)
	}
	runGitTest(t, snapshot, "push", "-u", "origin", "HEAD")
	if got, want := strings.TrimSpace(gitTestOutput(t, remote, "rev-parse", "refs/heads/codebox/test")), strings.TrimSpace(gitTestOutput(t, snapshot, "rev-parse", "HEAD")); got != want {
		t.Fatalf("remote branch tip = %s, want pushed guest commit %s", got, want)
	}
}

func TestConfigureGitHubWorkspaceRequiresResolvedApproval(t *testing.T) {
	client := &fakeMSBClient{}
	m := NewMicrosandboxRuntimeForInstance(Config{
		GitHubRemote: GitHubRemote{URL: "https://github.com/owner/repo.git", Repo: "owner/repo"},
	}, "opencode-cb-test-12345")
	m.Client = client
	if err := m.configureGitHubWorkspace(context.Background(), nil); err != nil {
		t.Fatalf("disabled GitHub workflow: %v", err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("disabled GitHub workflow touched the guest: %v", client.calls)
	}
}

func TestGitHubSnapshotPublicationGateAfterFilteredTransfer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guest branch bootstrap uses POSIX shell commands")
	}
	for _, tc := range []struct {
		name   string
		refuse bool
	}{
		{"tracked modification", true},
		{"untracked file", true},
		{"init configuration", true},
		{"local unpushed feature commit", true},
		{"host behind default branch", true},
		{"ignored file explicitly included", true},
		{"dotenv excluded", false},
		{"ignored file excluded", false},
		{"local deletion preserved remotely", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			base := filepath.Join(root, "base")
			if err := os.Mkdir(base, 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, base, "shared.txt", "published content\n")
			writeFile(t, base, ".gitignore", "private.txt\n")
			gitInitForTransfer(t, base)
			runGitTest(t, base, "branch", "-M", "main")
			remote := filepath.Join(root, "origin.git")
			runGitTest(t, root, "clone", "-q", "--bare", base, remote)
			host := filepath.Join(root, "host")
			runGitTest(t, root, "clone", "-q", remote, host)
			runGitTest(t, host, "config", "user.name", "Host")
			runGitTest(t, host, "config", "user.email", "host@example.test")
			opts := TransferOptions{}
			switch tc.name {
			case "tracked modification":
				writeFile(t, host, "shared.txt", "not reviewed\n")
			case "untracked file":
				writeFile(t, host, "notes.txt", "not reviewed\n")
			case "init configuration":
				for _, path := range []string{".codebox/project.json", ".codebox/lock.json", "AGENTS.md"} {
					writeFile(t, host, path, "generated by init\n")
				}
			case "local unpushed feature commit":
				runGitTest(t, host, "checkout", "-qb", "feature")
				writeFile(t, host, "shared.txt", "local feature\n")
				runGitTest(t, host, "add", "shared.txt")
				runGitTest(t, host, "commit", "-qm", "local feature, not pushed")
			case "host behind default branch":
				writeFile(t, base, "shared.txt", "newer remote content\n")
				runGitTest(t, base, "add", "shared.txt")
				runGitTest(t, base, "commit", "-qm", "remote update")
				runGitTest(t, base, "push", remote, "main")
			case "ignored file explicitly included":
				writeFile(t, host, "private.txt", "guest-readable, not publishable\n")
				opts.OptIn = map[string]bool{"private.txt": true}
			case "dotenv excluded":
				writeFile(t, host, ".env", "LOCAL_ONLY=canary\n")
			case "ignored file excluded":
				writeFile(t, host, "private.txt", "local only\n")
			case "local deletion preserved remotely":
				if err := os.Remove(filepath.Join(host, "shared.txt")); err != nil {
					t.Fatal(err)
				}
			}
			hostBefore := gitTestOutput(t, host, "status", "--porcelain") + gitTestOutput(t, host, "rev-parse", "HEAD")
			manifest, err := ResolveTransferSet(context.Background(), host, opts)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := BuildTransferArchive(manifest)
			if err != nil {
				t.Fatal(err)
			}
			guest := filepath.Join(root, "guest")
			if err := os.Mkdir(guest, 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("tar", "-xzf", "-", "-C", guest)
			cmd.Stdin = bytes.NewReader(payload)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("extract filtered payload: %v: %s", err, out)
			}
			gitInitForTransfer(t, guest)
			before := gitTestOutput(t, guest, "rev-parse", "HEAD")
			refsBefore := gitTestOutput(t, guest, "show-ref")
			configBefore, err := os.ReadFile(filepath.Join(guest, ".git", "config"))
			if err != nil {
				t.Fatal(err)
			}
			// No stdin/TTY is supplied: a file transfer opt-in must never be
			// interpreted as publication consent in unattended launches.
			out, err := runShellTest(githubSnapshotBranchScript(guest, GitHubRemote{URL: remote}, "main", "codebox/test"), guest)
			if tc.refuse {
				want := "publication n’a pas été approuvée"
				if tc.name == "ignored file explicitly included" {
					// The repository bootstrap deliberately does not force-add
					// ignored files; its existing ignored-work guard runs first.
					want = "ignored files"
				}
				if err == nil || !strings.Contains(string(out), want) {
					t.Fatalf("want publication refusal: err=%v out=%s", err, out)
				}
				if got := gitTestOutput(t, guest, "rev-parse", "HEAD"); got != before {
					t.Fatal("refusal changed guest HEAD")
				}
				if got := gitTestOutput(t, guest, "show-ref"); got != refsBefore {
					t.Fatal("refusal created or changed a guest branch")
				}
				configAfter, err := os.ReadFile(filepath.Join(guest, ".git", "config"))
				if err != nil || !bytes.Equal(configBefore, configAfter) {
					t.Fatalf("refusal changed guest configuration: %v", err)
				}
				if tc.name == "init configuration" {
					for _, path := range []string{".codebox/project.json", ".codebox/lock.json", "AGENTS.md"} {
						if !strings.Contains(string(out), path) {
							t.Errorf("missing refused path %s: %s", path, out)
						}
					}
				}
			} else {
				if err != nil {
					t.Fatalf("remote-equivalent filtered snapshot: %v: %s", err, out)
				}
				if got, want := gitTestOutput(t, guest, "rev-parse", "HEAD"), gitTestOutput(t, remote, "rev-parse", "main"); got != want {
					t.Fatal("clean bootstrap created a synthetic commit")
				}
				for _, path := range []string{".env", "private.txt"} {
					if _, err := os.Stat(filepath.Join(guest, path)); !os.IsNotExist(err) {
						t.Fatalf("excluded %s appeared in guest: %v", path, err)
					}
				}
				if tc.name == "local deletion preserved remotely" {
					if got := gitTestOutput(t, guest, "show", "HEAD:shared.txt"); got != "published content\n" {
						t.Fatalf("remote-only file must retain existing overlay behavior: %q", got)
					}
				}
			}
			if got := gitTestOutput(t, guest, "status", "--porcelain"); got != "" {
				t.Fatalf("bootstrap left a dirty guest: %s", got)
			}
			if got := gitTestOutput(t, host, "status", "--porcelain") + gitTestOutput(t, host, "rev-parse", "HEAD"); got != hostBefore {
				t.Fatal("bootstrap modified the host checkout")
			}
			if got := gitTestOutput(t, remote, "for-each-ref", "refs/heads/codebox/"); got != "" {
				t.Fatal("bootstrap published a branch")
			}
		})
	}
}

func TestReconcileNoOpValidatesExistingGitHubSnapshotPolicy(t *testing.T) {
	isolateHostState(t)
	remote := GitHubRemote{URL: "https://github.com/owner/repo.git", Repo: "owner/repo"}
	for _, tc := range []struct {
		name  string
		valid bool
	}{
		{name: "legacy marker", valid: false},
		{name: "current marker", valid: true},
		{name: "approved credential missing", valid: false},
		{name: "credential store unavailable", valid: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateHostState(t)
			client := &fakeMSBClient{exists: true, status: "running"}
			m := newTestMicrosandbox(t, client)
			m.cfg.Isolation = IsolationFull
			m.cfg.GitHubRemote = remote
			m.Probe = func(context.Context, string, string, string) HealthProbe { return HealthProbe{Healthy: true} }
			m.credentialRead = stubReader("stored")
			if tc.name == "approved credential missing" {
				m.credentialRead = func(_ context.Context, kind CredentialKind, _ string) (string, string, error) {
					if kind == CredentialGithub {
						return "", "", ErrCredentialNotFound
					}
					return "stored", "test", nil
				}
			}
			if tc.name == "credential store unavailable" {
				m.credentialRead = func(_ context.Context, kind CredentialKind, _ string) (string, string, error) {
					if kind == CredentialGithub {
						return "", "", storeErrorf("get", "test", "locked", "")
					}
					return "stored", "test", nil
				}
			}
			if err := ApproveBinding(DefaultFS, bindingApprovalsPath(m.stateDirOrDefault(), m.InstanceName()), CredentialGithub); err != nil {
				t.Fatal(err)
			}
			resolved, resolveErr := m.resolveBindings(context.Background())
			if tc.name == "approved credential missing" {
				if resolveErr != nil || hasResolvedGitHubBinding(resolved) {
					t.Fatalf("fixture must omit the missing approved GitHub credential: %v", resolveErr)
				}
			} else if tc.name == "credential store unavailable" {
				if resolveErr == nil {
					t.Fatal("fixture must exercise an unavailable GitHub credential store")
				}
			} else if resolveErr != nil || !hasResolvedGitHubBinding(resolved) {
				t.Fatalf("fixture must resolve the approved GitHub binding: %v", resolveErr)
			}
			desired := m.desiredState(resolveErr == nil, resolved, nil)
			statePath := instanceStatePath(DefaultStateDir(), m.InstanceName())
			if err := WriteInstanceState(DefaultFS, statePath, desired.toState()); err != nil {
				t.Fatal(err)
			}
			policy := ""
			if tc.valid {
				policy = "remote-only-v1\n"
			}
			client.execCaptureDefault = &fakeMSBExecCaptureResult{stdout: "yes"}
			client.execCaptureResults = []fakeMSBExecCaptureResult{
				{stdout: remote.URL + "\n"},
				{stdout: remote.URL + "\n"},
				{stdout: policy},
			}
			callsBefore := len(client.calls)
			err := m.Reconcile(context.Background())
			if tc.valid && err != nil {
				t.Fatalf("current publication policy must remain a healthy no-op: %v", err)
			}
			if !tc.valid && (err == nil || !strings.Contains(err.Error(), "ancienne règle")) {
				t.Fatalf("healthy no-op launch must reject the legacy publication policy: %v", err)
			}
			foundPolicyProbe := false
			for _, call := range client.calls[callsBefore:] {
				if strings.Contains(call, "codebox.github-workflow-snapshot-policy") {
					foundPolicyProbe = true
				}
				if strings.Contains(call, "git reset") || strings.Contains(call, "git branch ") || strings.Contains(call, "git config --file") {
					t.Fatalf("the no-op policy check rewrote guest history or config: %s", call)
				}
			}
			if !foundPolicyProbe {
				t.Fatalf("healthy no-op launch bypassed the policy check: %v", client.calls)
			}
		})
	}
}

func TestExistingGitHubPublicationPolicyAllowsUninitializedGuestWorkspace(t *testing.T) {
	remote := GitHubRemote{URL: "https://github.com/owner/repo.git", Repo: "owner/repo"}
	client := &fakeMSBClient{exists: true, status: "running", execCaptureResults: []fakeMSBExecCaptureResult{
		{code: 128, stderr: "fatal: --local can only be used inside a git repository"},
	}}
	m := NewMicrosandboxRuntimeForInstance(Config{GitHubRemote: remote}, "opencode-cb-test-12345")
	m.Client = client
	if err := m.validateExistingGitHubPublicationPolicy(context.Background()); err != nil {
		t.Fatalf("a guest workspace not yet initialized as a repository must be provisioned normally: %v", err)
	}
	for _, call := range client.calls {
		if strings.Contains(call, "git reset") || strings.Contains(call, "git branch ") || strings.Contains(call, "git config --file") {
			t.Fatalf("the fresh-workspace policy probe modified the guest: %s", call)
		}
	}
}

func TestConfigureGitHubWorkspaceRefusesLegacySnapshotPolicy(t *testing.T) {
	remote := GitHubRemote{URL: "https://github.com/owner/repo.git", Repo: "owner/repo"}
	for _, policy := range []fakeMSBExecCaptureResult{{code: 1}, {stdout: "unknown-policy\n"}, {err: errors.New("cannot read policy")}} {
		client := &fakeMSBClient{execCaptureResults: []fakeMSBExecCaptureResult{
			{stdout: remote.URL}, {stdout: remote.URL}, policy,
		}}
		m := NewMicrosandboxRuntimeForInstance(Config{GitHubRemote: remote}, "opencode-cb-test-12345")
		m.Client = client
		err := m.configureGitHubWorkspace(context.Background(), []resolvedBinding{{msbSecretBinding: msbSecretBinding{Kind: CredentialGithub}}})
		if err == nil || !strings.Contains(err.Error(), "ancienne règle") || !strings.Contains(err.Error(), "workspace apply ne les récupère pas") {
			t.Fatalf("legacy guest must refuse with honest recovery guidance: %v", err)
		}
		for _, call := range client.calls {
			if strings.Contains(call, "git fetch") || strings.Contains(call, "git reset") || strings.Contains(call, "git branch ") || strings.Contains(call, "git config --file") {
				t.Fatalf("legacy guest history/config was rewritten: %s", call)
			}
		}
	}
}

func TestGitHubOriginMismatchRefusesBeforeRuntimeChanges(t *testing.T) {
	for _, action := range []string{"start", "restart", "reconcile"} {
		t.Run(action, func(t *testing.T) {
			isolateHostState(t)
			client := &fakeMSBClient{exists: true, status: "running"}
			m := newTestMicrosandbox(t, client)
			m.StateDir = DefaultStateDir()
			m.credentialRead = stubReader("stored")
			m.cfg.GitHubRemote = GitHubRemote{URL: "https://github.com/owner/new.git", Repo: "owner/new"}
			if err := ApproveBinding(DefaultFS, BindingApprovalsPath(m.StateDir, m.InstanceName()), CredentialGithub); err != nil {
				t.Fatal(err)
			}
			if err := WriteInstanceState(DefaultFS, instanceStatePath(m.StateDir, m.InstanceName()), InstanceState{
				SchemaVersion: 1, Instance: m.InstanceName(), GitHubOrigin: "https://github.com/owner/old.git",
			}); err != nil {
				t.Fatal(err)
			}
			var err error
			switch action {
			case "start":
				err = m.Start(context.Background())
			case "restart":
				err = m.Restart(context.Background())
			case "reconcile":
				err = m.Reconcile(context.Background())
			}
			if err == nil || !strings.Contains(err.Error(), "origin differs") {
				t.Fatalf("%s origin mismatch = %v", action, err)
			}
			if len(client.calls) != 0 {
				t.Fatalf("%s touched the runtime before refusing: %v", action, client.calls)
			}
		})
	}
}

func TestConfigureGitHubWorkspaceChecksAccessBeforeSeedingBranch(t *testing.T) {
	remote := GitHubRemote{URL: "https://github.com/owner/repo.git", Repo: "owner/repo"}
	client := &fakeMSBClient{execCaptureResults: []fakeMSBExecCaptureResult{
		{code: 1}, // no codebox origin marker
		{code: 1}, // no existing origin remote
		{stdout: ""},
		{stdout: "Logged in as test-user"},
		{stdout: "Git credential helper configured"},
		{stdout: "main\n"},
		{stdout: ""},
		{stdout: "true\n"},
		{stdout: testRecoveryBaseSHA}, // recovery base after remote branch seeding
	}}
	m := NewMicrosandboxRuntimeForInstance(Config{GitHubRemote: remote}, "opencode-cb-test-12345")
	m.StateDir = t.TempDir()
	m.Client = client
	bindings := []resolvedBinding{{msbSecretBinding: msbSecretBinding{Kind: CredentialGithub, GuestEnv: "GITHUB_TOKEN"}, value: "synthetic-test-token"}}
	if err := m.configureGitHubWorkspace(context.Background(), bindings); err != nil {
		t.Fatalf("configureGitHubWorkspace: %v", err)
	}
	if len(client.execCaptureResults) != 0 {
		t.Fatalf("not all expected guest probes ran: %d results remain", len(client.execCaptureResults))
	}
	if got := m.guestDiffBase(); got != testRecoveryBaseSHA {
		t.Fatalf("recovery base after GitHub initialization = %q, want %q", got, testRecoveryBaseSHA)
	}
	calls := strings.Join(client.calls, "\n")
	for _, want := range []string{
		"gh auth status --hostname github.com",
		"gh auth setup-git --hostname github.com",
		"gh repo view 'owner/repo'",
		"repos/owner/repo",
		"git fetch --depth=1 'https://github.com/owner/repo.git' 'refs/heads/main'",
		"git config --file \"$temp_config\" codebox.github-workflow-origin",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("guest commands do not include %q:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "synthetic-test-token") {
		t.Fatal("credential value leaked into guest command arguments")
	}
}

func TestConfigureGitHubWorkspaceRejectsMissingWritePermission(t *testing.T) {
	client := &fakeMSBClient{execCaptureResults: []fakeMSBExecCaptureResult{
		{code: 1}, // no marker
		{code: 1}, // no origin remote
		{},        // no other remotes
		{},        // authenticated
		{},        // credential helper configured
		{stdout: "main\n"},
		{}, // valid branch name
		{stdout: "false\n"},
	}}
	m := NewMicrosandboxRuntimeForInstance(Config{
		GitHubRemote: GitHubRemote{URL: "https://github.com/owner/repo.git", Repo: "owner/repo"},
	}, "opencode-cb-test-12345")
	m.Client = client
	bindings := []resolvedBinding{{msbSecretBinding: msbSecretBinding{Kind: CredentialGithub, GuestEnv: "GITHUB_TOKEN"}}}
	err := m.configureGitHubWorkspace(context.Background(), bindings)
	if err == nil || !strings.Contains(err.Error(), "has no push role") {
		t.Fatalf("missing write permission: %v", err)
	}
	for _, call := range client.calls {
		if strings.Contains(call, "git fetch") || strings.Contains(call, "git reset --hard") {
			t.Fatalf("branch seeding ran despite missing write permission: %s", call)
		}
	}
}

func TestConfigureGitHubWorkspaceRejectsAuthenticationBeforeSeeding(t *testing.T) {
	client := &fakeMSBClient{execCaptureResults: []fakeMSBExecCaptureResult{
		{code: 1}, {code: 1}, {}, {code: 1, stderr: "synthetic-sensitive-cli-diagnostic"},
	}}
	m := NewMicrosandboxRuntimeForInstance(Config{
		GitHubRemote: GitHubRemote{URL: "https://github.com/owner/repo.git", Repo: "owner/repo"},
	}, "opencode-cb-test-12345")
	m.Client = client
	err := m.configureGitHubWorkspace(context.Background(), []resolvedBinding{{msbSecretBinding: msbSecretBinding{Kind: CredentialGithub}}})
	if err == nil || !strings.Contains(err.Error(), "credential was not accepted") {
		t.Fatalf("authentication failure = %v", err)
	}
	if strings.Contains(err.Error(), "synthetic-sensitive-cli-diagnostic") {
		t.Fatal("external CLI diagnostics reached the host error")
	}
	for _, call := range client.calls {
		if strings.Contains(call, "git fetch") || strings.Contains(call, "git reset --hard") {
			t.Fatalf("authentication failure seeded the branch: %s", call)
		}
	}
}

func TestConfigureGitHubWorkspaceDoesNotReseedAnInitializedGuest(t *testing.T) {
	remote := GitHubRemote{URL: "https://github.com/owner/repo.git", Repo: "owner/repo"}
	client := &fakeMSBClient{execCaptureResults: []fakeMSBExecCaptureResult{
		{stdout: remote.URL}, {stdout: remote.URL}, {stdout: "remote-only-v1\n"},
	}}
	m := NewMicrosandboxRuntimeForInstance(Config{GitHubRemote: remote}, "opencode-cb-test-12345")
	m.Client = client
	if err := m.configureGitHubWorkspace(context.Background(), []resolvedBinding{{msbSecretBinding: msbSecretBinding{Kind: CredentialGithub}}}); err != nil {
		t.Fatal(err)
	}
	for _, call := range client.calls {
		if strings.Contains(call, "git fetch") || strings.Contains(call, "git reset --hard") {
			t.Fatalf("initialized guest work was reseeded: %s", call)
		}
	}
}

func gitTestOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(root, "empty-gitconfig"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

func runShellTest(script, dir string) ([]byte, error) {
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(dir, "empty-gitconfig"))
	return cmd.CombinedOutput()
}

func runGitTest(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(root, "empty-gitconfig"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestProbeGitHubForProjectDistinguishesStoreFailure pins that a store that
// cannot be read is NOT reported as "no token stored": the two situations have
// different remedies (unlock the store, versus add a token), so the init screen
// must not guess. A missing credential stays the ordinary case.
func TestProbeGitHubForProjectDistinguishesStoreFailure(t *testing.T) {
	remote := GitHubRemote{URL: "https://github.com/owner/repo.git", Repo: "owner/repo"}
	origin := func(context.Context, string) (GitHubRemote, error) { return remote, nil }

	// Not found: an ordinary "no token yet" project.
	notFound := func(context.Context, CredentialKind) (string, error) { return "", ErrCredentialNotFound }
	st := probeGitHubForProjectWith(context.Background(), "/p", nil, origin, notFound)
	if st.TokenError != nil || st.HasToken {
		t.Fatalf("a missing credential must read as no token, got %+v", st)
	}
	if !st.HasOrigin || st.Remote != remote {
		t.Fatalf("the origin must still be reported, got %+v", st)
	}

	// Locked store: a real problem, not an invitation to add a token.
	locked := func(context.Context, CredentialKind) (string, error) {
		return "", &StoreError{Op: "get", Kind: string(CredentialGithub), State: "locked"}
	}
	st = probeGitHubForProjectWith(context.Background(), "/p", nil, origin, locked)
	if st.TokenError == nil || st.HasToken {
		t.Fatalf("a locked store must surface as a store error, got %+v", st)
	}
}

// TestProbeGitHubForProjectWithoutOrigin skips the credential read entirely:
// a project with no GitHub link is its own situation, and asking for a token
// there would offer a step the origin cannot support.
func TestProbeGitHubForProjectWithoutOrigin(t *testing.T) {
	read := false
	noOrigin := func(context.Context, string) (GitHubRemote, error) {
		return GitHubRemote{}, errors.New("no readable origin remote")
	}
	readFn := func(context.Context, CredentialKind) (string, error) {
		read = true
		return "tok", nil
	}
	st := probeGitHubForProjectWith(context.Background(), "/p", nil, noOrigin, readFn)
	if st.HasOrigin || st.HasToken || st.TokenError != nil {
		t.Fatalf("an unlinked project must be reported as such, got %+v", st)
	}
	if read {
		t.Fatal("the credential store must not be read when the project has no origin")
	}
}
