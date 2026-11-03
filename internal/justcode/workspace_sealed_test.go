package justcode

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// testRecoveryBaseSHA is the canned `git rev-parse HEAD` answer for the
// recovery-base recording that follows every host transfer (#313).
const testRecoveryBaseSHA = "0123456789abcdef0123456789abcdef01234567"

// TestProvisionGuestWorkspaceWritesFilteredPayload pins the provisioning
// path end to end at the client boundary: the guest receives one archive,
// built from the filtered set, and the .env in the checkout is not in it.
func TestProvisionGuestWorkspaceWritesFilteredPayload(t *testing.T) {
	client := &fakeMSBClient{exists: true,
		// The guest has no repository yet: provisioning must run.
		execCaptureResults: []fakeMSBExecCaptureResult{
			{stdout: "no"},                // guestWorkspaceProvisioned
			{stdout: testRecoveryBaseSHA}, // recordRecoveryBase
		},
	}
	m := newTestMicrosandbox(t, client)
	writeFile(t, m.cfg.WorkspaceDir, "main.go", "package main\n")
	writeFile(t, m.cfg.WorkspaceDir, ".env", "CANARY=must-not-cross\n")
	gitInitForTransfer(t, m.cfg.WorkspaceDir)

	if err := m.ProvisionGuestWorkspace(context.Background(), SyncOptions{Print: func(string) {}}); err != nil {
		t.Fatalf("ProvisionGuestWorkspace: %v", err)
	}
	if len(client.written) != 1 {
		t.Fatalf("the transfer must write exactly one payload, got %d", len(client.written))
	}
	payload := client.written[0].data
	if client.written[0].guestPath != msbTransferArchivePath {
		t.Fatalf("payload written to %q", client.written[0].guestPath)
	}
	if strings.Contains(string(payload), "CANARY=must-not-cross") {
		t.Fatal("the canary value crossed into the guest payload")
	}
	names := archiveNames(t, payload)
	if !names["main.go"] || names[".env"] {
		t.Fatalf("payload contents = %v", names)
	}
	// Extraction and repository creation both happen in the guest.
	if !hasCall(client, "exec "+msbSandbox) {
		t.Fatalf("extraction did not run in the guest: %v", client.calls)
	}
}

// TestProvisionGuestWorkspaceIsIdempotent pins resumability: a guest that
// already carries its repository is not re-provisioned, so a plain start
// never overwrites guest work.
func TestProvisionGuestWorkspaceIsIdempotent(t *testing.T) {
	client := &fakeMSBClient{exists: true, execCaptureDefault: &fakeMSBExecCaptureResult{stdout: "yes"}}
	m := newTestMicrosandbox(t, client)
	writeFile(t, m.cfg.WorkspaceDir, "main.go", "package main\n")
	gitInitForTransfer(t, m.cfg.WorkspaceDir)

	if err := m.ProvisionGuestWorkspace(context.Background(), SyncOptions{Print: func(string) {}}); err != nil {
		t.Fatal(err)
	}
	if len(client.written) != 0 {
		t.Fatalf("an already provisioned guest must not be re-transferred: %v", client.written)
	}
}

// TestSyncRefusesOverGuestChanges pins the no-silent-overwrite rule: a
// refresh stops when the guest holds uncommitted work, names it, and only
// proceeds with Force.
func TestSyncRefusesOverGuestChanges(t *testing.T) {
	client := &fakeMSBClient{exists: true,
		execCaptureResults: []fakeMSBExecCaptureResult{
			{stdout: "yes"},                          // guestWorkspaceProvisioned
			{stdout: " M main.go\n?? scratch.txt\n"}, // guestWorkingTreeDirty
		},
	}
	m := newTestMicrosandbox(t, client)
	writeFile(t, m.cfg.WorkspaceDir, "main.go", "package main\n")
	gitInitForTransfer(t, m.cfg.WorkspaceDir)

	err := m.SyncGuestWorkspace(context.Background(), SyncOptions{Print: func(string) {}})
	if err == nil {
		t.Fatal("a refresh over guest-local changes must be refused")
	}
	for _, want := range []string{"scratch.txt", "workspace export", "--force"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal must mention %q: %v", want, err)
		}
	}
	if len(client.written) != 0 {
		t.Fatal("a refused refresh must not write anything")
	}

	// With Force, the refresh proceeds and the payload is written.
	client.execCaptureResults = nil
	client.execCaptureDefault = &fakeMSBExecCaptureResult{stdout: testRecoveryBaseSHA}
	if err := m.SyncGuestWorkspace(context.Background(), SyncOptions{Force: true, Print: func(string) {}}); err != nil {
		t.Fatalf("forced refresh: %v", err)
	}
	if len(client.written) != 1 {
		t.Fatalf("the forced refresh must write one payload, got %d", len(client.written))
	}
}

// TestSyncRefusesEmptyTransferSet pins the failure mode where the filter
// would hand the guest an empty workspace: that is a configuration mistake
// worth stopping for, not a silent empty project.
func TestSyncRefusesEmptyTransferSet(t *testing.T) {
	client := &fakeMSBClient{exists: true, execCaptureDefault: &fakeMSBExecCaptureResult{stdout: ""}}
	m := newTestMicrosandbox(t, client)
	// Only a dotenv file: everything is filtered out.
	writeFile(t, m.cfg.WorkspaceDir, ".env", "X=1\n")
	gitInitForTransfer(t, m.cfg.WorkspaceDir)

	err := m.SyncGuestWorkspace(context.Background(), SyncOptions{Print: func(string) {}})
	if err == nil {
		t.Fatal("an empty transfer set must be refused rather than provision an empty workspace")
	}
	if !strings.Contains(err.Error(), "excluded all") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestEnsureGuestRepositoryDoesNotAutoCommitOnPublishableBranch pins the
// publication boundary after initialization: a host refresh may transfer files
// into the guest for recovery, but must not silently append them to the branch
// whose next push opens the agent's PR. The agent's own commits remain intact.
func TestEnsureGuestRepositoryDoesNotAutoCommitOnPublishableBranch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guest repository bootstrap uses POSIX shell commands")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "origin.git")
	if err := os.Mkdir(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, remote, "init", "-q", "--bare")
	guest := filepath.Join(root, "guest")
	if err := os.Mkdir(guest, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitForTransfer(t, guest)
	base := strings.TrimSpace(gitTestOutput(t, guest, "rev-parse", "HEAD"))
	writeFile(t, guest, "agent.txt", "reviewed agent work\n")
	runGitTest(t, guest, "add", "agent.txt")
	runGitTest(t, guest, "commit", "-qm", "reviewed agent work")
	agentCommit := strings.TrimSpace(gitTestOutput(t, guest, "rev-parse", "HEAD"))
	runGitTest(t, guest, "branch", "-M", "just-code/test")
	for _, marker := range []string{"origin", "branch", "snapshot-policy"} {
		runGitTest(t, guest, "config", "--local", "just-code.github-workflow-"+marker, "present")
	}
	runGitTest(t, guest, "remote", "add", "origin", remote)
	writeFile(t, guest, "local.txt", "unapproved host refresh\n")
	before := strings.TrimSpace(gitTestOutput(t, guest, "status", "--porcelain"))

	out, err := runShellTest(guestRepositoryBootstrapScript(guest, "Guest", "guest@example.test"), guest)
	if err != nil && !strings.Contains(string(out), "publication automatique refusée") {
		t.Fatalf("execute guest repository bootstrap: %v: %s", err, out)
	}
	if err == nil || !strings.Contains(string(out), "publication automatique refusée") {
		t.Fatalf("initialized GitHub workflow must block the automatic host-snapshot commit: %v: %s", err, out)
	}
	if got := strings.TrimSpace(gitTestOutput(t, guest, "rev-parse", "HEAD")); got != agentCommit {
		t.Fatalf("host refresh appended a commit to the publishable branch: %s", got)
	}
	if got := strings.TrimSpace(gitTestOutput(t, guest, "status", "--porcelain")); got != before {
		t.Fatalf("refusal staged or changed the refreshed files: %q -> %q", before, got)
	}
	if got := strings.TrimSpace(gitTestOutput(t, guest, "rev-parse", "HEAD^")); got != base {
		t.Fatalf("the agent's existing commit history was rewritten: %s", got)
	}
	if got := gitTestOutput(t, remote, "for-each-ref"); got != "" {
		t.Fatalf("the refusal published guest history: %s", got)
	}

	// Positive control through the same plumbing: without a GitHub workflow
	// marker, initial repository provisioning still creates its baseline commit.
	for _, marker := range []string{"origin", "branch", "snapshot-policy"} {
		runGitTest(t, guest, "config", "--local", "--unset", "just-code.github-workflow-"+marker)
	}
	out, err = runShellTest(guestRepositoryBootstrapScript(guest, "Guest", "guest@example.test"), guest)
	if err != nil {
		t.Fatalf("non-GitHub guest bootstrap: %v: %s", err, out)
	}
	if got := strings.TrimSpace(gitTestOutput(t, guest, "rev-parse", "HEAD^")); got != agentCommit {
		t.Fatalf("positive control did not execute the repository bootstrap: %s", got)
	}
	if got := gitTestOutput(t, remote, "for-each-ref"); got != "" {
		t.Fatalf("the test must not publish guest history: %s", got)
	}
}

// TestProvisionEmptySourceCreatesEmptyRepository pins the other side of that
// distinction: a source directory with no files at all is a legitimately
// empty project (the default './workspace' on a fresh install), so it gets a
// repository rather than an error.
func TestProvisionEmptySourceCreatesEmptyRepository(t *testing.T) {
	client := &fakeMSBClient{exists: true,
		execCaptureResults: []fakeMSBExecCaptureResult{{stdout: "no"}, {stdout: testRecoveryBaseSHA}},
	}
	m := newTestMicrosandbox(t, client)
	gitInitForTransfer(t, m.cfg.WorkspaceDir)
	// gitInitForTransfer leaves a .git directory only: no working-tree file.
	var out strings.Builder
	if err := m.ProvisionGuestWorkspace(context.Background(), SyncOptions{Print: func(s string) { out.WriteString(s) }}); err != nil {
		t.Fatalf("an empty source must still provision: %v", err)
	}
	if !strings.Contains(out.String(), "empty repository") {
		t.Fatalf("the empty case must be stated plainly: %q", out.String())
	}
	if !hasCall(client, "exec "+msbSandbox) {
		t.Fatalf("the guest repository must still be created: %v", client.calls)
	}
}

// TestExportGuestChangesWritesPatchForReview pins the delivery path: the
// guest's diff is pulled back to host state (not into the project), so it can
// be reviewed before anything is applied.
func TestExportGuestChangesWritesPatchForReview(t *testing.T) {
	patch := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n"
	client := &fakeMSBClient{exists: true, execCaptureDefault: &fakeMSBExecCaptureResult{stdout: patch}}
	m := newTestMicrosandbox(t, client)
	out := filepath.Join(t.TempDir(), "guest.patch")

	path, err := m.ExportGuestChanges(context.Background(), out)
	if err != nil {
		t.Fatalf("ExportGuestChanges: %v", err)
	}
	if path != out {
		t.Fatalf("patch path = %q, want %q", path, out)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != patch {
		t.Fatalf("patch content = %q", data)
	}
}

// TestExportGuestChangesEmptyWhenClean pins the quiet case: no changes means
// no patch file, not an empty one a user might mistake for work.
func TestExportGuestChangesEmptyWhenClean(t *testing.T) {
	client := &fakeMSBClient{exists: true, execCaptureDefault: &fakeMSBExecCaptureResult{stdout: "  \n"}}
	m := newTestMicrosandbox(t, client)
	path, err := m.ExportGuestChanges(context.Background(), filepath.Join(t.TempDir(), "none.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Fatalf("a clean guest must produce no patch, got %q", path)
	}
}

// TestGuestRepositoryUsesConfiguredIdentity pins the P11 identity reaching
// the guest repository through the command the provisioning path runs, rather
// than the guest script's hardcoded default.
func TestGuestRepositoryUsesConfiguredIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	path, err := UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteUserSettings(DefaultFS, path, UserSettings{
		SchemaVersion: 1, GitName: "Luis Arias", GitEmail: "luis@example.gouv.fr",
	}); err != nil {
		t.Fatal(err)
	}
	client := &fakeMSBClient{exists: true, execCaptureDefault: &fakeMSBExecCaptureResult{stdout: ""}}
	m := newTestMicrosandbox(t, client)
	if err := m.ensureGuestRepository(context.Background()); err != nil {
		t.Fatalf("ensureGuestRepository: %v", err)
	}
	joined := strings.Join(client.calls, "\n")
	if !strings.Contains(joined, "Luis Arias") || !strings.Contains(joined, "luis@example.gouv.fr") {
		t.Fatalf("the guest commit must use the configured identity: %v", client.calls)
	}
}

// TestWorkspaceOpsRefuseLegacyBindMount pins the guard on the operations that
// write into the guest workspace. Against a pre-P22 instance, /workspace IS
// the host checkout, so an unguarded sync would extract over the user's files
// and an export would stage them in their repository. Both must refuse.
func TestWorkspaceOpsRefuseLegacyBindMount(t *testing.T) {
	ops := map[string]func(*MicrosandboxRuntime) error{
		"provision": func(m *MicrosandboxRuntime) error {
			return m.ProvisionGuestWorkspace(context.Background(), SyncOptions{Print: func(string) {}})
		},
		"sync": func(m *MicrosandboxRuntime) error {
			return m.SyncGuestWorkspace(context.Background(), SyncOptions{Force: true, Print: func(string) {}})
		},
		"export": func(m *MicrosandboxRuntime) error {
			_, err := m.ExportGuestChanges(context.Background(), "")
			return err
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			client := &fakeMSBClient{exists: true, mount: "/Users/someone/project"}
			m := newTestMicrosandbox(t, client)
			writeFile(t, m.cfg.WorkspaceDir, "main.go", "package main\n")
			err := op(m)
			if err == nil {
				t.Fatal("the operation must refuse a host-mounted workspace")
			}
			if !strings.Contains(err.Error(), "mounted from the host") {
				t.Fatalf("the refusal must name the cause: %v", err)
			}
			if len(client.written) != 0 {
				t.Fatal("nothing may be written into a host-mounted workspace")
			}
		})
	}
}

// TestWorkspaceOpsRefuseUnprovenProvenance pins the positive side of the
// check: a workspace the runtime does not recognize as guest-owned storage is
// refused even when no bind source is visible, so an unrecognized mount shape
// cannot be read as sealed.
func TestWorkspaceOpsRefuseUnprovenProvenance(t *testing.T) {
	notOwned := false
	client := &fakeMSBClient{exists: true, owned: &notOwned}
	m := newTestMicrosandbox(t, client)
	writeFile(t, m.cfg.WorkspaceDir, "main.go", "package main\n")
	err := m.SyncGuestWorkspace(context.Background(), SyncOptions{Force: true, Print: func(string) {}})
	if err == nil {
		t.Fatal("an unproven workspace must be refused")
	}
	if !strings.Contains(err.Error(), "not guest-owned storage") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestWorkspaceOpsRefuseMissingInstance keeps the failure actionable: there is
// nothing to transfer into before the instance exists.
func TestWorkspaceOpsRefuseMissingInstance(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{exists: false})
	err := m.SyncGuestWorkspace(context.Background(), SyncOptions{Print: func(string) {}})
	if err == nil || !strings.Contains(err.Error(), "does not exist yet") {
		t.Fatalf("expected an actionable not-found error, got %v", err)
	}
}

// TestSyncHonoursRecordedOptInWithoutTheFlag pins that a recorded per-file
// decision reaches the transfer even when the caller passes no opt-in set:
// otherwise the initial provisioning would silently ignore 'workspace allow'.
func TestSyncHonoursRecordedOptInWithoutTheFlag(t *testing.T) {
	client := &fakeMSBClient{exists: true, execCaptureDefault: &fakeMSBExecCaptureResult{stdout: testRecoveryBaseSHA}}
	m := newTestMicrosandbox(t, client)
	writeFile(t, m.cfg.WorkspaceDir, ".env", "SECRET=allowed-by-decision\n")
	gitInitForTransfer(t, m.cfg.WorkspaceDir)

	store := TransferOptInStore{Path: TransferOptInPath(m.StateDir, m.InstanceName()), FS: DefaultFS}
	if err := store.Allow(".env", "dotenv file (default-deny)"); err != nil {
		t.Fatal(err)
	}
	// No OptIn in the options: the recorded decision must still apply.
	if err := m.SyncGuestWorkspace(context.Background(), SyncOptions{Force: true, Print: func(string) {}}); err != nil {
		t.Fatalf("SyncGuestWorkspace: %v", err)
	}
	if len(client.written) != 1 {
		t.Fatalf("expected one payload, got %d", len(client.written))
	}
	content := archiveContent(t, client.written[0].data)
	body, ok := content[".env"]
	if !ok {
		t.Fatalf("the recorded re-inclusion must reach the transfer: %v", content)
	}
	if !strings.Contains(body, "SECRET=allowed-by-decision") {
		t.Fatalf(".env content = %q", body)
	}
}

// writeRecoveryBase records a canned recovery base for an instance, the way
// recordRecoveryBase does after a host transfer (#313).
func writeRecoveryBase(t *testing.T, stateDir, instance, sha string) {
	t.Helper()
	path := recoveryBasePath(stateDir, instance)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(sha+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestProvisionGuestWorkspaceRecordsRecoveryBase: the base recorded after a
// host transfer is the guest's HEAD at that moment, so later agent commits
// cannot move it (#313).
func TestProvisionGuestWorkspaceRecordsRecoveryBase(t *testing.T) {
	client := &fakeMSBClient{exists: true,
		execCaptureResults: []fakeMSBExecCaptureResult{
			{stdout: "no"},                // guestWorkspaceProvisioned
			{stdout: testRecoveryBaseSHA}, // recordRecoveryBase
		},
	}
	m := newTestMicrosandbox(t, client)
	writeFile(t, m.cfg.WorkspaceDir, "main.go", "package main\n")
	gitInitForTransfer(t, m.cfg.WorkspaceDir)
	if err := m.ProvisionGuestWorkspace(context.Background(), SyncOptions{Print: func(string) {}}); err != nil {
		t.Fatalf("ProvisionGuestWorkspace: %v", err)
	}
	data, err := os.ReadFile(recoveryBasePath(m.StateDir, m.InstanceName()))
	if err != nil {
		t.Fatalf("the recovery base must be recorded on the host: %v", err)
	}
	if strings.TrimSpace(string(data)) != testRecoveryBaseSHA {
		t.Fatalf("recovery base = %q, want %q", data, testRecoveryBaseSHA)
	}
	if got := m.guestDiffBase(); got != testRecoveryBaseSHA {
		t.Fatalf("guestDiffBase = %q, want the recorded base", got)
	}
}

// TestProvisionGuestWorkspaceWithoutBaseFallsBackToHEAD: an environment with
// no recorded base keeps the legacy HEAD diff, never a guessed base (#313).
func TestProvisionGuestWorkspaceWithoutBaseFallsBackToHEAD(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{exists: true})
	if got := m.guestDiffBase(); got != "HEAD" {
		t.Fatalf("guestDiffBase without a record = %q, want HEAD", got)
	}
}

// TestExportGuestChangesUsesRecordedRecoveryBase: the export diff runs
// against the recorded base, with binary content enabled (#313).
func TestExportGuestChangesUsesRecordedRecoveryBase(t *testing.T) {
	patch := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n"
	client := &fakeMSBClient{exists: true, execCaptureDefault: &fakeMSBExecCaptureResult{stdout: patch}}
	m := newTestMicrosandbox(t, client)
	writeRecoveryBase(t, m.StateDir, m.InstanceName(), testRecoveryBaseSHA)
	out := filepath.Join(t.TempDir(), "guest.patch")
	if _, err := m.ExportGuestChanges(context.Background(), out); err != nil {
		t.Fatalf("ExportGuestChanges: %v", err)
	}
	joined := strings.Join(client.calls, "\n")
	if !strings.Contains(joined, "git diff "+testRecoveryBaseSHA+" --binary") {
		t.Fatalf("the export must diff against the recovery base with --binary: %v", client.calls)
	}
	if strings.Contains(joined, "git diff HEAD") {
		t.Fatalf("a recorded base must not fall back to HEAD: %v", client.calls)
	}
}

// TestGuestRecoveryWarning pins the legacy-environment limitation (#313): a
// provisioned guest without a base warns; a based or unprovisioned guest is
// silent.
func TestGuestRecoveryWarning(t *testing.T) {
	t.Run("legacy provisioned guest warns", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{{stdout: "yes"}}
		m, _ := applyTestRuntime(t, client)
		warning, err := m.GuestRecoveryWarning(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(warning, "commités") || !strings.Contains(warning, "Sauvegardez") {
			t.Fatalf("the legacy limitation must be explicit: %q", warning)
		}
	})
	t.Run("recorded base is silent", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{{stdout: "yes"}}
		m, stateDir := applyTestRuntime(t, client)
		writeRecoveryBase(t, stateDir, m.InstanceName(), testRecoveryBaseSHA)
		warning, err := m.GuestRecoveryWarning(context.Background())
		if err != nil || warning != "" {
			t.Fatalf("warning = %q, err = %v; want silence", warning, err)
		}
	})
	t.Run("unprovisioned guest is silent", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{{stdout: "no"}}
		m, _ := applyTestRuntime(t, client)
		warning, err := m.GuestRecoveryWarning(context.Background())
		if err != nil || warning != "" {
			t.Fatalf("warning = %q, err = %v; want silence", warning, err)
		}
	})
}

// TestRecoveryBaseDiffIncludesCommittedWork verifies the #313 mechanism with
// real git: after agent commits, `git diff HEAD` sees nothing while the diff
// against the recorded base sees every change — addition, modification,
// deletion, binary content, multiple commits — plus uncommitted work, and an
// unchanged tree produces an empty diff.
func TestRecoveryBaseDiffIncludesCommittedWork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recovery-base scenario uses POSIX shell commands")
	}
	dir := t.TempDir()
	script := `
set -eu
git init -q
git config user.name T
git config user.email t@example.test
echo one > a.txt
echo keep > keep.txt
echo gone > gone.txt
git add -A
git commit -q -m base
BASE=$(git rev-parse HEAD)
test -z "$(git add -A -N && git diff "$BASE" --binary)"
echo two >> a.txt
git commit -q -am 'modify a'
echo new > new.txt
git add new.txt
git commit -q -m 'add new'
git rm -q gone.txt
git commit -q -m 'delete gone'
printf '\000\001\002' > bin.dat
git add bin.dat
git commit -q -m 'binary'
echo three >> a.txt
git commit -q -am 'modify a again'
test -z "$(git add -A -N && git diff HEAD --binary)"
DIFF=$(git add -A -N && git diff "$BASE" --binary)
printf '%s' "$DIFF" | grep -q 'a.txt'
printf '%s' "$DIFF" | grep -q 'new.txt'
printf '%s' "$DIFF" | grep -q 'gone.txt'
printf '%s' "$DIFF" | grep -q 'GIT binary patch'
echo dirty >> keep.txt
DIFF2=$(git add -A -N && git diff "$BASE" --binary)
printf '%s' "$DIFF2" | grep -q 'keep.txt'
echo RECOVERY-BASE-OK
`
	out, err := runShellTest(script, dir)
	if err != nil {
		t.Fatalf("recovery-base scenario: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "RECOVERY-BASE-OK") {
		t.Fatalf("scenario did not complete: %s", out)
	}
}
