package justcode

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

// sealedFakeMSB returns a fake client whose instance passes
// requireSealedWorkspace: it exists with guest-owned /workspace storage.
func sealedFakeMSB() *fakeMSBClient {
	return &fakeMSBClient{exists: true, owned: boolPtr(true)}
}

// applyTestRuntime binds a sealed fake client to a runtime with a test
// state directory, so record stores land in t.TempDir().
func applyTestRuntime(t *testing.T, client *fakeMSBClient) (*MicrosandboxRuntime, string) {
	t.Helper()
	stateDir := t.TempDir()
	m := NewMicrosandboxRuntimeForInstance(Config{}, "test-instance")
	m.Client = client
	m.StateDir = stateDir
	return m, stateDir
}

func TestParseNumstatZ(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want []guestNumstat
	}{
		{
			name: "one text change",
			out:  "12\t3\tsrc/app.js",
			want: []guestNumstat{{rel: "src/app.js", additions: 12, deletions: 3}},
		},
		{
			name: "two changes separated by NUL",
			out:  "84\t0\tsrc/snake.js\x002\t0\tREADME.md",
			want: []guestNumstat{
				{rel: "src/snake.js", additions: 84},
				{rel: "README.md", additions: 2},
			},
		},
		{
			name: "binary file reports dash counts",
			out:  "-\t-\tlogo.png",
			want: []guestNumstat{{rel: "logo.png", binary: true}},
		},
		{
			name: "path containing a tab splits on the first two only",
			out:  "1\t1\tweird\tname.txt",
			want: []guestNumstat{{rel: "weird\tname.txt", additions: 1, deletions: 1}},
		},
		{name: "empty output", out: "", want: nil},
		{name: "trailing NUL", out: "1\t0\ta.txt\x00", want: []guestNumstat{{rel: "a.txt", additions: 1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseNumstatZ(tc.out)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseNumstatZ(%q) = %+v, want %+v", tc.out, got, tc.want)
			}
		})
	}
}

// applyNumstatTwo is the `git diff --numstat -z` payload for two text
// changes: src/app.js modified (12 added, 3 deleted) and a new README.md
// (84 added), in that order.
const applyNumstatTwo = "12\t3\tsrc/app.js\x0084\t0\tREADME.md\x00"

// applyDiffTwoSections is the `git diff` payload matching applyNumstatTwo:
// one modified file then one new file.
const applyDiffTwoSections = "diff --git a/src/app.js b/src/app.js\n" +
	"index 1111111..2222222 100644\n" +
	"--- a/src/app.js\n" +
	"+++ b/src/app.js\n" +
	"@@ -1,2 +1,3 @@\n" +
	" line\n" +
	"+new\n" +
	" tail\n" +
	"diff --git a/README.md b/README.md\n" +
	"new file mode 100644\n" +
	"index 0000000..3333333\n" +
	"--- /dev/null\n" +
	"+++ b/README.md\n" +
	"@@ -0,0 +1,2 @@\n" +
	"+one\n" +
	"+two\n"

// applyFirstSection is the src/app.js section alone, as splitDiffSections
// must cut it out of applyDiffTwoSections.
const applyFirstSection = "diff --git a/src/app.js b/src/app.js\n" +
	"index 1111111..2222222 100644\n" +
	"--- a/src/app.js\n" +
	"+++ b/src/app.js\n" +
	"@@ -1,2 +1,3 @@\n" +
	" line\n" +
	"+new\n" +
	" tail\n"

// applySecondSection is the README.md section alone.
const applySecondSection = "diff --git a/README.md b/README.md\n" +
	"new file mode 100644\n" +
	"index 0000000..3333333\n" +
	"--- /dev/null\n" +
	"+++ b/README.md\n" +
	"@@ -0,0 +1,2 @@\n" +
	"+one\n" +
	"+two\n"

func TestSplitDiffSections(t *testing.T) {
	if got := splitDiffSections(""); got != nil {
		t.Fatalf("empty diff = %v, want nil", got)
	}
	if got := splitDiffSections("not a diff"); got != nil {
		t.Fatalf("diff without a section header = %v, want nil", got)
	}
	one := splitDiffSections(applyFirstSection)
	if len(one) != 1 || one[0] != applyFirstSection {
		t.Fatalf("single section = %q", one)
	}
	got := splitDiffSections(applyDiffTwoSections)
	if len(got) != 2 {
		t.Fatalf("two sections = %d, want 2", len(got))
	}
	if got[0] != applyFirstSection {
		t.Fatalf("first section = %q, want %q", got[0], applyFirstSection)
	}
	if got[1] != applySecondSection {
		t.Fatalf("second section = %q, want %q", got[1], applySecondSection)
	}
}

func TestGuestSectionStatus(t *testing.T) {
	cases := []struct {
		name    string
		section string
		want    byte
	}{
		{"modified file", applyFirstSection, 'M'},
		{"new file", applySecondSection, 'A'},
		{
			"deleted file",
			"diff --git a/gone.txt b/gone.txt\ndeleted file mode 100644\nindex 111..000\n--- a/gone.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-bye\n",
			'D',
		},
		{
			"binary patch",
			"diff --git a/logo.png b/logo.png\nindex 111..222 100644\nGIT binary patch\nliteral 4\ndata 4\n",
			'M',
		},
		{
			"header only falls back to modified",
			"diff --git a/x.txt b/x.txt\n",
			'M',
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := guestSectionStatus(tc.section); got != tc.want {
				t.Fatalf("guestSectionStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPatchContentSize(t *testing.T) {
	cases := []struct {
		name    string
		section string
		want    int64
	}{
		{"empty", "", 0},
		{"one added line", applyFirstSection, 4},
		{"two added lines", applySecondSection, 8},
		{
			"binary literal size",
			"diff --git a/b.bin b/b.bin\nindex 111..222 100644\nGIT binary patch\nliteral 4096\ndata 8\n",
			4096,
		},
		{
			"binary delta size",
			"diff --git a/b.bin b/b.bin\nindex 111..222 100644\nGIT binary patch\ndelta 2048\ndata 8\n",
			2048,
		},
		{"no hunk", "diff --git a/x.txt b/x.txt\nindex 111..222 100644\n", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := patchContentSize(tc.section); got != tc.want {
				t.Fatalf("patchContentSize = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestGuestChangeDigest(t *testing.T) {
	base := GuestChange{Rel: "src/app.js", Patch: []byte(applyFirstSection)}
	same := GuestChange{Rel: "src/app.js", Patch: []byte(applyFirstSection)}
	if GuestChangeDigest(base) != GuestChangeDigest(same) {
		t.Fatal("digest of the same change differs between calls")
	}
	if GuestChangeDigest(base) == GuestChangeDigest(GuestChange{Rel: "other.js", Patch: base.Patch}) {
		t.Fatal("digest ignores the path")
	}
	if GuestChangeDigest(base) == GuestChangeDigest(GuestChange{Rel: base.Rel, Patch: []byte(applySecondSection)}) {
		t.Fatal("digest ignores the patch bytes")
	}
}

func TestApplyRecordStoreRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "applied-guest-changes.json")
	store := ApplyRecordStore{Path: path}

	if got, err := store.Load(); err != nil || len(got) != 0 {
		t.Fatalf("Load on a missing record = (%v, %v), want empty and no error", got, err)
	}
	if err := store.Record("src/app.js", "digest-one", "host-one", "guest-one"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := store.Record("README.md", "digest-two", "host-two", ""); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]string{"src/app.js": "digest-one", "README.md": "digest-two"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load = %v, want %v", got, want)
	}
	baselines, err := store.LoadRecoveredBaselines()
	if err != nil {
		t.Fatalf("LoadRecoveredBaselines: %v", err)
	}
	wantBaselines := map[string]RecoveredBaseline{
		"src/app.js": {Host: "host-one", Guest: "guest-one"},
		"README.md":  {Host: "host-two"}, // empty guest digest leaves the half unset
	}
	if !reflect.DeepEqual(baselines, wantBaselines) {
		t.Fatalf("LoadRecoveredBaselines = %v, want %v", baselines, wantBaselines)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no Unix permission bits (Go reports 0666 there).
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("record mode = %v, want 0600", info.Mode().Perm())
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "applied-changes record") {
		t.Fatalf("Load on corrupt JSON = %v, want an applied-changes record error", err)
	}
}

// TestApplyRecordStoreLoadsOldSchemasWithoutBaselines pins the back-compat
// halves of the v3 record: a file written before the recovered-content
// fields existed (v1), or with the host half only (v2), still classifies its
// patches, and its files fall back to the conservative paths until they
// are applied once more.
func TestApplyRecordStoreLoadsOldSchemasWithoutBaselines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "applied-guest-changes.json")
	v1 := `{"schemaVersion":1,"digests":{"src/app.js":"digest-one"}}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	store := ApplyRecordStore{Path: path}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := map[string]string{"src/app.js": "digest-one"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Load = %v, want %v", got, want)
	}
	baselines, err := store.LoadRecoveredBaselines()
	if err != nil {
		t.Fatalf("LoadRecoveredBaselines: %v", err)
	}
	if len(baselines) != 0 {
		t.Fatalf("LoadRecoveredBaselines = %v, want empty for a v1 record", baselines)
	}
	// Recording on top of a v1 file upgrades it without losing the old
	// patch classification.
	if err := store.Record("README.md", "digest-two", "host-two", "guest-two"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, err = store.Load()
	if err != nil {
		t.Fatalf("Load after upgrade: %v", err)
	}
	want := map[string]string{"src/app.js": "digest-one", "README.md": "digest-two"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load after upgrade = %v, want %v", got, want)
	}
	baselines, err = store.LoadRecoveredBaselines()
	if err != nil {
		t.Fatalf("LoadRecoveredBaselines after upgrade: %v", err)
	}
	if want := map[string]RecoveredBaseline{"README.md": {Host: "host-two", Guest: "guest-two"}}; !reflect.DeepEqual(baselines, want) {
		t.Fatalf("LoadRecoveredBaselines after upgrade = %v, want %v", baselines, want)
	}

	// A v2 file carries the host half of the baseline only: its guest
	// half stays unset until the file is recovered once more, which
	// keeps reversal synthesis and re-delivery detection off files
	// whose recorded state is incomplete.
	v2 := `{"schemaVersion":2,"digests":{"legacy.js":"digest-old"},"recoveredContent":{"legacy.js":"host-old"}}`
	if err := os.WriteFile(path, []byte(v2), 0o600); err != nil {
		t.Fatal(err)
	}
	baselines, err = store.LoadRecoveredBaselines()
	if err != nil {
		t.Fatalf("LoadRecoveredBaselines on v2: %v", err)
	}
	if want := map[string]RecoveredBaseline{"legacy.js": {Host: "host-old"}}; !reflect.DeepEqual(baselines, want) {
		t.Fatalf("LoadRecoveredBaselines on v2 = %v, want %v", baselines, want)
	}
}

func TestGuestBranchStatus(t *testing.T) {
	t.Run("never created", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{
			{code: 1},                  // git config --get: no marker
			{stdout: "main\x001000\n"}, // local branches
			{stdout: "origin/main\x001000\norigin/HEAD\x001000\n"}, // remote refs
			{stdout: "origin/main\n"},                              // remote HEAD
		}
		m, _ := applyTestRuntime(t, client)
		status, err := m.GuestBranchStatus(context.Background())
		if err != nil || status.Branch != "" || status.Pushed {
			t.Fatalf("GuestBranchStatus = (%+v, %v), want nothing to report", status, err)
		}
	})
	t.Run("never created without marker or origin", func(t *testing.T) {
		for _, baseline := range []string{"main", "master"} {
			t.Run(baseline, func(t *testing.T) {
				client := sealedFakeMSB()
				client.execCaptureResults = []fakeMSBExecCaptureResult{
					{code: 1},
					{stdout: baseline + "\x001000\n"},
					{stdout: ""},
					{code: 1},
				}
				m, _ := applyTestRuntime(t, client)
				status, err := m.GuestBranchStatus(context.Background())
				if err != nil || status.Branch != "" || status.Pushed {
					t.Fatalf("GuestBranchStatus = (%+v, %v), want the bootstrap baseline ignored", status, err)
				}
			})
		}
	})
	t.Run("created but never pushed", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{
			{stdout: "letta/fix\n"},
			{stdout: "letta/fix\x001000\n"},
			{stdout: "origin/main\x001000\n"},
			{stdout: "origin/main\n"},
		}
		m, _ := applyTestRuntime(t, client)
		status, err := m.GuestBranchStatus(context.Background())
		if err != nil || status.Pushed || status.Branch != "letta/fix" || status.PRURL != "" {
			t.Fatalf("GuestBranchStatus = (%+v, %v), want the branch, not pushed", status, err)
		}
	})
	t.Run("pushed with a draft pull request", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{
			{stdout: "letta/fix\n"},
			{stdout: "letta/fix\x001000\n"},
			{stdout: "origin/main\x001000\norigin/letta/fix\x001000\n"},
			{stdout: "origin/main\n"},
			{stdout: `[{"url":"https://github.com/owner/repo/pull/9","isDraft":true}]`},
		}
		m, _ := applyTestRuntime(t, client)
		status, err := m.GuestBranchStatus(context.Background())
		if err != nil || !status.Pushed || status.Branch != "letta/fix" ||
			status.PRURL != "https://github.com/owner/repo/pull/9" || !status.PRDraft || !status.PRKnown {
			t.Fatalf("GuestBranchStatus = (%+v, %v), want pushed with the draft pull request", status, err)
		}
	})
	t.Run("pushed without a usable gh means PR unknown", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{
			{stdout: "letta/fix\n"},
			{stdout: "letta/fix\x001000\n"},
			{stdout: "origin/main\x001000\norigin/letta/fix\x001000\n"},
			{stdout: "origin/main\n"},
			{code: 1}, // gh pr list fails; the branch still counts as pushed
		}
		m, _ := applyTestRuntime(t, client)
		status, err := m.GuestBranchStatus(context.Background())
		if err != nil || !status.Pushed || status.Branch != "letta/fix" || status.PRURL != "" || status.PRKnown {
			t.Fatalf("GuestBranchStatus = (%+v, %v), want pushed with an unknown PR state", status, err)
		}
	})
	t.Run("pushed without a pull request", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{
			{stdout: "letta/fix\n"},
			{stdout: "letta/fix\x001000\n"},
			{stdout: "origin/main\x001000\norigin/letta/fix\x001000\n"},
			{stdout: "origin/main\n"},
			{stdout: `[]`},
		}
		m, _ := applyTestRuntime(t, client)
		status, err := m.GuestBranchStatus(context.Background())
		if err != nil || !status.Pushed || status.PRURL != "" || !status.PRKnown {
			t.Fatalf("GuestBranchStatus = (%+v, %v), want pushed with no PR, known state", status, err)
		}
	})
	// The QA case of #306: the agent created and pushed its own branch
	// instead of the generated marker branch. The exit screen must find the
	// branch that was ACTUALLY pushed, with its non-draft PR.
	t.Run("pushed branch differs from the marker", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{
			{stdout: "just-code/generated-123\n"},
			{stdout: "just-code/generated-123\x001000\nmy-fix\x002000\n"},
			{stdout: "origin/main\x001000\norigin/my-fix\x002000\n"},
			{stdout: "origin/main\n"},
			{stdout: `[{"url":"https://github.com/owner/repo/pull/12","isDraft":false}]`},
		}
		m, _ := applyTestRuntime(t, client)
		status, err := m.GuestBranchStatus(context.Background())
		if err != nil || !status.Pushed || status.Branch != "my-fix" ||
			status.PRURL != "https://github.com/owner/repo/pull/12" || status.PRDraft || !status.PRKnown {
			t.Fatalf("GuestBranchStatus = (%+v, %v), want the actually-pushed branch and its PR", status, err)
		}
	})
	t.Run("newer pushed branch wins over a pushed marker", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{
			{stdout: "just-code/generated-123\n"},
			{stdout: "just-code/generated-123\x001000\nmy-fix\x002000\n"},
			{stdout: "origin/main\x001000\norigin/just-code/generated-123\x001000\norigin/my-fix\x002000\n"},
			{stdout: "origin/main\n"},
			{stdout: `[{"url":"https://github.com/owner/repo/pull/13","isDraft":false}]`},
		}
		m, _ := applyTestRuntime(t, client)
		status, err := m.GuestBranchStatus(context.Background())
		if err != nil || !status.Pushed || status.Branch != "my-fix" ||
			status.PRURL != "https://github.com/owner/repo/pull/13" || status.PRDraft || !status.PRKnown {
			t.Fatalf("GuestBranchStatus = (%+v, %v), want the newer pushed branch and its PR", status, err)
		}
		if len(client.calls) < 3 || !strings.Contains(client.calls[2], "%(committerdate:unix)") {
			t.Fatalf("remote discovery must request commit dates, calls: %v", client.calls)
		}
	})
	t.Run("remote-only pushed branch is discovered", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{
			{stdout: "just-code/generated-123\n"},
			{stdout: "just-code/generated-123\x001000\n"},
			{stdout: "origin/main\x001000\norigin/my-fix\x002000\n"},
			{stdout: "origin/main\n"},
			{stdout: `[{"url":"https://github.com/owner/repo/pull/14","isDraft":false}]`},
		}
		m, _ := applyTestRuntime(t, client)
		status, err := m.GuestBranchStatus(context.Background())
		if err != nil || !status.Pushed || status.Branch != "my-fix" ||
			status.PRURL != "https://github.com/owner/repo/pull/14" || status.PRDraft || !status.PRKnown {
			t.Fatalf("GuestBranchStatus = (%+v, %v), want the remote-only pushed branch and its PR", status, err)
		}
	})
	t.Run("latest pushed branch wins without a marker", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{
			{code: 1}, // no marker
			{stdout: "main\x001000\nfeature-a\x001000\nfeature-b\x002000\n"},
			{stdout: "origin/main\x001000\norigin/feature-a\x001000\norigin/feature-b\x002000\n"},
			{stdout: "origin/main\n"},
			{stdout: `[]`},
		}
		m, _ := applyTestRuntime(t, client)
		status, err := m.GuestBranchStatus(context.Background())
		if err != nil || status.Branch != "feature-b" || !status.Pushed {
			t.Fatalf("GuestBranchStatus = (%+v, %v), want the most recently committed branch", status, err)
		}
	})
	t.Run("the snapshot backup is never a candidate", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{
			{stdout: "letta/fix\n"},
			{stdout: "letta/fix\x001000\nletta/fix-snapshot-backup\x002000\n"},
			{stdout: "origin/main\x001000\norigin/letta/fix-snapshot-backup\x002000\n"},
			{stdout: "origin/main\n"},
			{stdout: `[]`},
		}
		m, _ := applyTestRuntime(t, client)
		status, err := m.GuestBranchStatus(context.Background())
		// Only the backup was pushed; it is a workflow artifact, not agent
		// work, so the marker branch is reported as not pushed.
		if err != nil || status.Branch != "letta/fix" || status.Pushed {
			t.Fatalf("GuestBranchStatus = (%+v, %v), want the marker, not pushed", status, err)
		}
	})
	t.Run("a git read failure is an error, not an absence", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{
			{stdout: "letta/fix\n"},
			{code: 1, stderr: "fatal: not a git repository"},
		}
		m, _ := applyTestRuntime(t, client)
		status, err := m.GuestBranchStatus(context.Background())
		if err == nil {
			t.Fatalf("a failed git read must surface an error, got %+v", status)
		}
	})
	t.Run("a transport failure is an error, not an absence", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{
			{err: errors.New("sandbox unreachable")},
		}
		m, _ := applyTestRuntime(t, client)
		if _, err := m.GuestBranchStatus(context.Background()); err == nil {
			t.Fatal("a transport failure must surface an error")
		}
	})
}

// TestCollectGuestChangesUsesRecordedRecoveryBase: with a recorded base, the
// numstat and diff reads run against it, not HEAD, so committed guest work is
// collected (#313); without one, the legacy HEAD command is kept.
func TestCollectGuestChangesUsesRecordedRecoveryBase(t *testing.T) {
	newClient := func() *fakeMSBClient {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{
			{stdout: "yes"},           // provisioned probe
			{stdout: applyNumstatTwo}, // numstat
			{stdout: applyDiffTwoSections},
		}
		return client
	}
	t.Run("recorded base", func(t *testing.T) {
		client := newClient()
		m, stateDir := applyTestRuntime(t, client)
		writeRecoveryBase(t, stateDir, m.InstanceName(), testRecoveryBaseSHA)
		changes, err := m.CollectGuestChanges(context.Background())
		if err != nil || len(changes) != 2 {
			t.Fatalf("CollectGuestChanges = (%d changes, %v), want 2", len(changes), err)
		}
		joined := strings.Join(client.calls, "\n")
		if !strings.Contains(joined, "git diff "+testRecoveryBaseSHA+" --binary --no-renames") {
			t.Fatalf("the diff must run against the recovery base: %v", client.calls)
		}
		if strings.Contains(joined, "git diff HEAD") {
			t.Fatalf("a recorded base must not fall back to HEAD: %v", client.calls)
		}
	})
	t.Run("legacy HEAD fallback", func(t *testing.T) {
		client := newClient()
		m, _ := applyTestRuntime(t, client)
		if _, err := m.CollectGuestChanges(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(client.calls, "\n"), "git diff HEAD --binary --no-renames") {
			t.Fatalf("a guest without a base must keep the legacy HEAD diff: %v", client.calls)
		}
	})
}

func TestCollectGuestChangesPairsNumstatWithDiff(t *testing.T) {
	client := sealedFakeMSB()
	client.execCaptureResults = []fakeMSBExecCaptureResult{
		{stdout: "yes"},           // provisioned probe
		{stdout: applyNumstatTwo}, // numstat
		{stdout: applyDiffTwoSections},
	}
	m, _ := applyTestRuntime(t, client)
	changes, err := m.CollectGuestChanges(context.Background())
	if err != nil {
		t.Fatalf("CollectGuestChanges: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("got %d changes, want 2: %+v", len(changes), changes)
	}
	appJS, readme := changes[0], changes[1]
	if appJS.Rel != "src/app.js" || appJS.Status != 'M' || appJS.Additions != 12 || appJS.Deletions != 3 || appJS.Binary {
		t.Fatalf("src/app.js change = %+v", appJS)
	}
	if readme.Rel != "README.md" || readme.Status != 'A' || readme.Additions != 84 || readme.Deletions != 0 {
		t.Fatalf("README.md change = %+v", readme)
	}
	if string(appJS.Patch) != applyFirstSection {
		t.Fatalf("src/app.js patch = %q, want %q", appJS.Patch, applyFirstSection)
	}
	if string(readme.Patch) != applySecondSection {
		t.Fatalf("README.md patch = %q, want %q", readme.Patch, applySecondSection)
	}
	if n := countCalls(client, "execcapture"); n != 3 {
		t.Fatalf("guest ran %d captured commands, want the probe, numstat and diff", n)
	}
}

func TestCollectGuestChangesUnprovisionedGuestHasNoWork(t *testing.T) {
	client := sealedFakeMSB()
	client.execCaptureResults = []fakeMSBExecCaptureResult{{stdout: "no"}}
	m, _ := applyTestRuntime(t, client)
	changes, err := m.CollectGuestChanges(context.Background())
	if err != nil {
		t.Fatalf("CollectGuestChanges: %v", err)
	}
	if changes != nil {
		t.Fatalf("got %+v, want nil: an unprovisioned guest holds no work", changes)
	}
	if n := countCalls(client, "execcapture"); n != 1 {
		t.Fatalf("guest ran %d captured commands, want exactly the provisioned probe", n)
	}
}

func TestCollectGuestChangesFailsClosedOnCountMismatch(t *testing.T) {
	client := sealedFakeMSB()
	client.execCaptureResults = []fakeMSBExecCaptureResult{
		{stdout: "yes"},
		{stdout: applyNumstatTwo},   // two files listed
		{stdout: applyFirstSection}, // but a one-file patch
	}
	m, _ := applyTestRuntime(t, client)
	_, err := m.CollectGuestChanges(context.Background())
	if err == nil || !strings.Contains(err.Error(), "changed while it was being read") {
		t.Fatalf("error = %v, want the fail-closed mismatch message", err)
	}
}

func TestCollectGuestChangesReportsGuestFailures(t *testing.T) {
	t.Run("numstat fails", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{
			{stdout: "yes"},
			{code: 128, stderr: "fatal: not a git repository"},
		}
		m, _ := applyTestRuntime(t, client)
		_, err := m.CollectGuestChanges(context.Background())
		if err == nil || !strings.Contains(err.Error(), "could not list its changes") {
			t.Fatalf("error = %v, want the numstat failure", err)
		}
	})
	t.Run("diff fails", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{
			{stdout: "yes"},
			{stdout: ""}, // numstat: clean tree
			{code: 128, stderr: "fatal: bad object HEAD"},
		}
		m, _ := applyTestRuntime(t, client)
		_, err := m.CollectGuestChanges(context.Background())
		if err == nil || !strings.Contains(err.Error(), "could not produce a diff") {
			t.Fatalf("error = %v, want the diff failure", err)
		}
	})
}

func TestCollectGuestChangesRefusesUnsealedWorkspace(t *testing.T) {
	cases := []struct {
		name   string
		client *fakeMSBClient
		want   string
	}{
		{
			"missing instance",
			&fakeMSBClient{exists: false},
			"does not exist yet",
		},
		{
			"bind-mounted workspace",
			&fakeMSBClient{exists: true, mount: "/home/user/proj"},
			"mounted from the host",
		},
		{
			"unowned storage",
			&fakeMSBClient{exists: true, owned: boolPtr(false)},
			"guest-owned storage",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := applyTestRuntime(t, tc.client)
			_, err := m.CollectGuestChanges(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
			if n := countCalls(tc.client, "execcapture"); n != 0 {
				t.Fatalf("an unsealed guest ran %d captured commands, want none", n)
			}
		})
	}
}

func TestApplyPolicyCheck(t *testing.T) {
	man := TransferManifest{
		Entries: []TransferEntry{
			{Rel: "src/app.js", Included: true},
			{Rel: "big.bin", Included: false, Reason: "above the per-file transfer cap", Exclusion: TransferExclusionSizeCap},
			{Rel: "link", Included: false, Exclusion: TransferExclusionSymlink},
		},
	}
	optIn := map[string]bool{".env.local": true, ".git/config": true}
	p := newApplyPolicy(t.TempDir(), man, optIn)
	ctx := context.Background()
	cases := []struct {
		name string
		rel  string
		want string
	}{
		{"git metadata is structural", ".git/config", "Git metadata never crosses the isolation boundary"},
		{"git directory itself", ".git", "Git metadata never crosses the isolation boundary"},
		{"opt-in re-includes a dotenv file", ".env.local", ""},
		{"dotenv is default-deny", ".env", "dotenv file (default-deny)"},
		{"manifest entry that crossed", "src/app.js", ""},
		{"manifest entry refused with a reason", "big.bin", "above the per-file transfer cap"},
		{"manifest entry refused by class", "link", "symlink"},
		{"unknown path is allowed", "src/new-file.ts", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.check(ctx, GuestChange{Rel: tc.rel}); got != tc.want {
				t.Fatalf("check(%q) = %q, want %q", tc.rel, got, tc.want)
			}
		})
	}
}

func TestApplyPolicyCheckRefusesGitIgnoredAgentPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitInitForTransfer(t, root)
	writeFile(t, root, ".gitignore", "node_modules/\n")
	runGitTest(t, root, "add", ".gitignore")
	runGitTest(t, root, "commit", "-q", "-m", "gitignore")
	p := newApplyPolicy(root, TransferManifest{GitManaged: true}, nil)
	ctx := context.Background()

	if got := p.check(ctx, GuestChange{Rel: "node_modules/derived.js"}); got != "git-ignored (local or derived state)" {
		t.Fatalf("ignored path: check = %q, want the git-ignored refusal", got)
	}
	if got := p.check(ctx, GuestChange{Rel: "src/new-file.ts"}); got != "" {
		t.Fatalf("tracked path: check = %q, want allowed", got)
	}
}

func TestApplyPolicyCheckGitIgnoredProbeIsBestEffort(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// A git-managed manifest against a root that is not a repository: the
	// probe cannot run, and the refusal must not turn into a blanket block.
	p := newApplyPolicy(t.TempDir(), TransferManifest{GitManaged: true}, nil)
	if got := p.check(context.Background(), GuestChange{Rel: "node_modules/derived.js"}); got != "" {
		t.Fatalf("check = %q, want allowed: the probe is a refinement, not a gate", got)
	}
}

func TestApplyPolicyCheckRefusesAboveTheCap(t *testing.T) {
	p := newApplyPolicy(t.TempDir(), TransferManifest{}, nil)
	ctx := context.Background()
	header := "diff --git a/big.bin b/big.bin\nindex 111..222 100644\nGIT binary patch\nliteral "
	over := header + strconv.FormatInt(DefaultTransferMaxFileBytes+1, 10) + "\ndata 4\n"
	if got := p.check(ctx, GuestChange{Rel: "big.bin", Patch: []byte(over)}); got != "above the per-file transfer cap" {
		t.Fatalf("check over the cap = %q, want the cap refusal", got)
	}
	at := header + strconv.FormatInt(DefaultTransferMaxFileBytes, 10) + "\ndata 4\n"
	if got := p.check(ctx, GuestChange{Rel: "big.bin", Patch: []byte(at)}); got != "" {
		t.Fatalf("check exactly at the cap = %q, want allowed", got)
	}
}

func TestApplyGuestPatchesAppliesAndConflicts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitInitForTransfer(t, root)
	writeFile(t, root, "a.txt", "old\n")
	writeFile(t, root, "keep.txt", "stay\n")
	runGitTest(t, root, "add", "-A")
	runGitTest(t, root, "commit", "-q", "-m", "base")

	ctx := context.Background()
	if applied, conflicted, err := ApplyGuestPatches(ctx, root, nil); err != nil || applied != nil || conflicted != nil {
		t.Fatalf("empty set = (%v, %v, %v), want nothing and no error", applied, conflicted, err)
	}

	good := GuestChange{Rel: "a.txt", Patch: []byte("diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+new\n")}
	bad := GuestChange{Rel: "keep.txt", Patch: []byte("diff --git a/keep.txt b/keep.txt\n--- a/keep.txt\n+++ b/keep.txt\n@@ -1 +1 @@\n-not-the-content\n+new\n")}
	applied, conflicted, err := ApplyGuestPatches(ctx, root, []GuestChange{good, bad})
	if err != nil {
		t.Fatalf("ApplyGuestPatches: %v", err)
	}
	if len(applied) != 1 || applied[0].Rel != "a.txt" {
		t.Fatalf("applied = %+v, want exactly a.txt", applied)
	}
	if len(conflicted) != 1 || conflicted[0].Rel != "keep.txt" {
		t.Fatalf("conflicted = %+v, want exactly keep.txt", conflicted)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(got) != "new\n" {
		t.Fatalf("a.txt = %q, want the patch applied", got)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "keep.txt")); string(got) != "stay\n" {
		t.Fatalf("keep.txt = %q, want the host content untouched", got)
	}
}

func TestApplyGuestPatchesConflictDoesNotOverwriteHostWork(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitInitForTransfer(t, root)
	writeFile(t, root, "a.txt", "old\n")
	runGitTest(t, root, "add", "-A")
	runGitTest(t, root, "commit", "-q", "-m", "base")
	// The host file moves on after the copy, without committing.
	writeFile(t, root, "a.txt", "host\n")

	guest := GuestChange{Rel: "a.txt", Patch: []byte("diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+guest\n")}
	applied, conflicted, err := ApplyGuestPatches(context.Background(), root, []GuestChange{guest})
	if err != nil {
		t.Fatalf("ApplyGuestPatches: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("applied = %+v, want none", applied)
	}
	if len(conflicted) != 1 || conflicted[0].Rel != "a.txt" {
		t.Fatalf("conflicted = %+v, want exactly a.txt", conflicted)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(got) != "host\n" {
		t.Fatalf("a.txt = %q, want the host work preserved", got)
	}
}

// TestApplySelectedChangesReAppliesRecoveredFile pins the recovery of a
// file the agent edited again after a first recovery (PR #280 review): the
// guest's second patch is cumulative from its HEAD, so it cannot apply on a
// host that already recovered once. The host still sitting at the recorded
// recovered baseline, the file is brought to the guest's current content
// directly, without a false conflict, and the next review classifies it as
// already recovered.
func TestApplySelectedChangesReAppliesRecoveredFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitInitForTransfer(t, root)
	writeFile(t, root, "src/app.js", "old\n")
	runGitTest(t, root, "add", "-A")
	runGitTest(t, root, "commit", "-q", "-m", "base")
	ctx := context.Background()

	first := GuestChange{Rel: "src/app.js", Status: 'M', Patch: []byte("diff --git a/src/app.js b/src/app.js\n--- a/src/app.js\n+++ b/src/app.js\n@@ -1 +1 @@\n-old\n+new\n")}
	client := sealedFakeMSB()
	m, _ := applyTestRuntime(t, client)
	// The first apply goes through git apply; the batch digest exec
	// then records the guest's current content ("new\n") as the guest
	// half of the baseline.
	client.execCaptureResults = []fakeMSBExecCaptureResult{{stdout: contentDigest([]byte("new\n"))}}
	applied, conflicted, err := ApplySelectedChanges(ctx, m, root, []GuestChange{first})
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if len(applied) != 1 || len(conflicted) != 0 {
		t.Fatalf("first apply = (%+v, %+v), want the one file applied", applied, conflicted)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "src", "app.js")); string(got) != "new\n" {
		t.Fatalf("src/app.js = %q after the first apply, want the patch applied", got)
	}

	// The agent edits the file again in the guest. The host sits at its
	// recorded baseline and the last recovery copied the file verbatim
	// (host half equals guest half), so the direct path brings the whole
	// file over: the base64 read is 644, 6 bytes, "newer\n".
	second := GuestChange{Rel: "src/app.js", Status: 'M', Patch: []byte("diff --git a/src/app.js b/src/app.js\n--- a/src/app.js\n+++ b/src/app.js\n@@ -1 +1 @@\n-old\n+newer\n")}
	client.execCaptureResults = []fakeMSBExecCaptureResult{{stdout: "644 6\nbmV3ZXIK"}}
	applied, conflicted, err = ApplySelectedChanges(ctx, m, root, []GuestChange{second})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if len(applied) != 1 || len(conflicted) != 0 {
		t.Fatalf("second apply = (%+v, %+v), want the recovered file re-applied without conflict", applied, conflicted)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "src", "app.js")); string(got) != "newer\n" {
		t.Fatalf("src/app.js = %q after the second apply, want the guest's current content", got)
	}

	// The next review must classify the re-delivered file as recovered,
	// not offer it again. The diff still reports the file, so reversal
	// synthesis has nothing to add and consumes no batch read.
	client.execCaptureResults = []fakeMSBExecCaptureResult{
		{stdout: "yes"},
		{stdout: "1\t1\tsrc/app.js\x00"},
		{stdout: string(second.Patch)},
	}
	unrecovered, err := UnrecoveredGuestChanges(ctx, m)
	if err != nil {
		t.Fatalf("UnrecoveredGuestChanges: %v", err)
	}
	if len(unrecovered) != 0 {
		t.Fatalf("unrecovered = %+v, want the re-applied file classified as recovered", unrecovered)
	}
}

// TestApplySelectedChangesConflictsWhenHostEditedAfterRecovery pins the
// safety half of the direct path: once the user edits the recovered file on
// the host, the host no longer sits at the recorded baseline, so the file
// goes back through git apply — which refuses to overwrite the user's work
// and reports the conflict instead.
func TestApplySelectedChangesConflictsWhenHostEditedAfterRecovery(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitInitForTransfer(t, root)
	writeFile(t, root, "src/app.js", "old\n")
	runGitTest(t, root, "add", "-A")
	runGitTest(t, root, "commit", "-q", "-m", "base")
	ctx := context.Background()

	first := GuestChange{Rel: "src/app.js", Status: 'M', Patch: []byte("diff --git a/src/app.js b/src/app.js\n--- a/src/app.js\n+++ b/src/app.js\n@@ -1 +1 @@\n-old\n+new\n")}
	client := sealedFakeMSB()
	m, _ := applyTestRuntime(t, client)
	client.execCaptureResults = []fakeMSBExecCaptureResult{{stdout: contentDigest([]byte("new\n"))}}
	if _, _, err := ApplySelectedChanges(ctx, m, root, []GuestChange{first}); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	// The user edits the recovered file after the first apply.
	writeFile(t, root, "src/app.js", "host-edited\n")

	second := GuestChange{Rel: "src/app.js", Status: 'M', Patch: []byte("diff --git a/src/app.js b/src/app.js\n--- a/src/app.js\n+++ b/src/app.js\n@@ -1 +1 @@\n-old\n+newer\n")}
	applied, conflicted, err := ApplySelectedChanges(ctx, m, root, []GuestChange{second})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("applied = %+v, want none: the host moved on since the recovery", applied)
	}
	if len(conflicted) != 1 || conflicted[0].Rel != "src/app.js" {
		t.Fatalf("conflicted = %+v, want exactly src/app.js", conflicted)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "src", "app.js")); string(got) != "host-edited\n" {
		t.Fatalf("src/app.js = %q, want the host work preserved", got)
	}
}

// TestApplySelectedChangesAppliesDeletionAfterRecovery covers the deletion
// half of the direct path: a file the agent deletes after a first recovery
// is removed on the host when the host still sits at the recorded baseline,
// and the record notes the deletion so a later review classifies honestly.
func TestApplySelectedChangesAppliesDeletionAfterRecovery(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitInitForTransfer(t, root)
	writeFile(t, root, "src/app.js", "old\n")
	runGitTest(t, root, "add", "-A")
	runGitTest(t, root, "commit", "-q", "-m", "base")
	ctx := context.Background()

	first := GuestChange{Rel: "src/app.js", Status: 'M', Patch: []byte("diff --git a/src/app.js b/src/app.js\n--- a/src/app.js\n+++ b/src/app.js\n@@ -1 +1 @@\n-old\n+new\n")}
	client := sealedFakeMSB()
	m, stateDir := applyTestRuntime(t, client)
	client.execCaptureResults = []fakeMSBExecCaptureResult{{stdout: contentDigest([]byte("new\n"))}}
	if _, _, err := ApplySelectedChanges(ctx, m, root, []GuestChange{first}); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	deleted := GuestChange{Rel: "src/app.js", Status: 'D', Patch: []byte("diff --git a/src/app.js b/src/app.js\n--- a/src/app.js\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n")}
	applied, conflicted, err := ApplySelectedChanges(ctx, m, root, []GuestChange{deleted})
	if err != nil {
		t.Fatalf("deletion apply: %v", err)
	}
	if len(applied) != 1 || len(conflicted) != 0 {
		t.Fatalf("deletion apply = (%+v, %+v), want the deletion applied", applied, conflicted)
	}
	if _, err := os.Stat(filepath.Join(root, "src", "app.js")); !os.IsNotExist(err) {
		t.Fatalf("src/app.js must be gone from the host, stat err = %v", err)
	}
	store := ApplyRecordStore{Path: ApplyRecordPath(stateDir, m.InstanceName())}
	baselines, err := store.LoadRecoveredBaselines()
	if err != nil {
		t.Fatal(err)
	}
	if base := baselines["src/app.js"]; base.Host != "deleted" || base.Guest != "deleted" {
		t.Fatalf("recovered baseline = %+v, want the deleted sentinel on both halves", base)
	}
}

// TestApplySelectedChangesRefusesUnsafeRecoveredPath pins the containment
// guard of the direct path: a guest-reported path outside the project never
// gets the direct write, even when a record claims a recovered baseline for
// it.
func TestApplySelectedChangesRefusesUnsafeRecoveredPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitInitForTransfer(t, root)
	ctx := context.Background()
	client := sealedFakeMSB()
	m, stateDir := applyTestRuntime(t, client)
	store := ApplyRecordStore{Path: ApplyRecordPath(stateDir, m.InstanceName())}
	// The seeded baseline matches the host state (the file is absent, so
	// "deleted"): without the path guard the direct write would fire and
	// land outside the host root, which is exactly what must never happen.
	if err := store.Record("../escape.txt", "patch-digest", "deleted", "deleted"); err != nil {
		t.Fatal(err)
	}
	client.execCaptureResults = []fakeMSBExecCaptureResult{{stdout: "644 6\nbmV3ZXIK"}}

	escape := GuestChange{Rel: "../escape.txt", Status: 'M', Patch: []byte("diff --git a/escape.txt b/escape.txt\n--- a/escape.txt\n+++ b/escape.txt\n@@ -1 +1 @@\n-old\n+new\n")}
	applied, conflicted, err := ApplySelectedChanges(ctx, m, root, []GuestChange{escape})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("applied = %+v, want none", applied)
	}
	if len(conflicted) != 1 || conflicted[0].Rel != "../escape.txt" {
		t.Fatalf("conflicted = %+v, want the escaping path reported, not applied", conflicted)
	}
	if _, err := os.Stat(filepath.Join(root, "..", "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("nothing may be written outside the host root, stat err = %v", err)
	}
}

// TestApplySelectedChangesPreservesHostEditsMergedByPatch is the F1 guard
// of PR #280's review: a host edit that the first patch apply merged into
// the file must survive the second recovery. The host sits at its recorded
// baseline but the baseline's halves differ (the host holds an edit the
// guest does not), so the direct copy must not fire — the new guest work
// goes through git apply, which reports the conflict instead of silently
// deleting the host edit.
func TestApplySelectedChangesPreservesHostEditsMergedByPatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitInitForTransfer(t, root)
	writeFile(t, root, "src/app.js", "line1\nold\nline3\n")
	runGitTest(t, root, "add", "-A")
	runGitTest(t, root, "commit", "-q", "-m", "base")
	// The user edits the host file outside the patch's hunk before any
	// recovery happened.
	writeFile(t, root, "src/app.js", "line1\nold\nline3\nhost-line\n")
	ctx := context.Background()

	first := GuestChange{Rel: "src/app.js", Status: 'M', Patch: []byte("diff --git a/src/app.js b/src/app.js\n--- a/src/app.js\n+++ b/src/app.js\n@@ -1,3 +1,3 @@\n line1\n-old\n+new\n line3\n")}
	client := sealedFakeMSB()
	m, _ := applyTestRuntime(t, client)
	// The guest's current content after its own edit: the patch applied
	// to the guest's HEAD, without the host-only line.
	guestAfterFirst := []byte("line1\nnew\nline3\n")
	client.execCaptureResults = []fakeMSBExecCaptureResult{{stdout: contentDigest(guestAfterFirst)}}
	applied, conflicted, err := ApplySelectedChanges(ctx, m, root, []GuestChange{first})
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if len(applied) != 1 || len(conflicted) != 0 {
		t.Fatalf("first apply = (%+v, %+v), want the patch applied", applied, conflicted)
	}
	wantMerged := "line1\nnew\nline3\nhost-line\n"
	if got, _ := os.ReadFile(filepath.Join(root, "src", "app.js")); string(got) != wantMerged {
		t.Fatalf("src/app.js = %q after the first apply, want the patch merged with the host edit kept", got)
	}

	// The agent edits the guest file again: the cumulative patch still
	// expects "old" at that line, which the host no longer has.
	second := GuestChange{Rel: "src/app.js", Status: 'M', Patch: []byte("diff --git a/src/app.js b/src/app.js\n--- a/src/app.js\n+++ b/src/app.js\n@@ -1,3 +1,3 @@\n line1\n-old\n+newer\n line3\n")}
	guestAfterSecond := []byte("line1\nnewer\nline3\n")
	client.execCaptureResults = []fakeMSBExecCaptureResult{{
		stdout: "644 " + strconv.Itoa(len(guestAfterSecond)) + "\n" + base64.StdEncoding.EncodeToString(guestAfterSecond),
	}}
	applied, conflicted, err = ApplySelectedChanges(ctx, m, root, []GuestChange{second})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("applied = %+v, want none: the host edit merged by the first patch must block the direct copy", applied)
	}
	if len(conflicted) != 1 || conflicted[0].Rel != "src/app.js" {
		t.Fatalf("conflicted = %+v, want exactly src/app.js reported as a conflict", conflicted)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "src", "app.js")); string(got) != wantMerged {
		t.Fatalf("src/app.js = %q after the second apply, want the host edit preserved", got)
	}
}

// TestApplySelectedChangesSyntheticReversalPreservesMergedHostEdits is the
// P1 guard of PR #280's round-3 review: when the first recovery merges a
// host-only edit (patch path), the recorded baselines differ, and a later
// guest reversal is synthetic — no patch exists to preserve the host edit.
// The direct copy would silently drop it, so the reversal must be reported
// as a conflict instead.
func TestApplySelectedChangesSyntheticReversalPreservesMergedHostEdits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitInitForTransfer(t, root)
	writeFile(t, root, "src/app.js", "line1\nold\nline3\n")
	runGitTest(t, root, "add", "-A")
	runGitTest(t, root, "commit", "-q", "-m", "base")
	// The user edits the host file outside the patch's hunk before any
	// recovery happened.
	writeFile(t, root, "src/app.js", "line1\nold\nline3\nhost-line\n")
	ctx := context.Background()

	first := GuestChange{Rel: "src/app.js", Status: 'M', Patch: []byte("diff --git a/src/app.js b/src/app.js\n--- a/src/app.js\n+++ b/src/app.js\n@@ -1,3 +1,3 @@\n line1\n-old\n+new\n line3\n")}
	client := sealedFakeMSB()
	m, _ := applyTestRuntime(t, client)
	// The guest's current content after its own edit: the patch applied
	// to the guest's HEAD, without the host-only line.
	guestAfterFirst := []byte("line1\nnew\nline3\n")
	client.execCaptureResults = []fakeMSBExecCaptureResult{{stdout: contentDigest(guestAfterFirst)}}
	applied, _, err := ApplySelectedChanges(ctx, m, root, []GuestChange{first})
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if len(applied) != 1 {
		t.Fatalf("first apply = %+v, want the patch applied", applied)
	}
	wantMerged := "line1\nnew\nline3\nhost-line\n"

	// The guest reverts the file to HEAD: the diff is empty, the
	// synthesis offers the reversal.
	head := []byte("line1\nold\nline3\n")
	client.execCaptureResults = []fakeMSBExecCaptureResult{
		{stdout: "yes"},
		{stdout: ""},
		{stdout: ""},
		{stdout: contentDigest(head)},
	}
	changes, err := m.CollectGuestChanges(ctx)
	if err != nil {
		t.Fatalf("CollectGuestChanges: %v", err)
	}
	if len(changes) != 1 || !changes[0].Synthetic || changes[0].Rel != "src/app.js" {
		t.Fatalf("changes = %+v, want the synthesized reversal", changes)
	}

	// The reversal must not overwrite the merged host edit: the
	// baselines differ, so it is a conflict. The fake serves no
	// ReadGuestFile result — reaching for the guest content would
	// already be the bug.
	applied, conflicted, err := ApplySelectedChanges(ctx, m, root, changes)
	if err != nil {
		t.Fatalf("reversal apply: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("applied = %+v, want none: the reversal must not copy over the merged host edit", applied)
	}
	if len(conflicted) != 1 || conflicted[0].Rel != "src/app.js" {
		t.Fatalf("conflicted = %+v, want exactly src/app.js reported as a conflict", conflicted)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "src", "app.js")); string(got) != wantMerged {
		t.Fatalf("src/app.js = %q, want the merged host edit preserved", got)
	}
}

// TestApplySelectedChangesRedeliveryLandsModeChange is the P2 guard of PR
// #280's round-3 review: a guest change that only toggles the executable
// bit has identical content, so the no-op re-record path used to swallow
// it — the host permissions never changed while the change was recorded as
// recovered and hidden from later runs. The re-delivery must land the
// guest's mode.
func TestApplySelectedChangesRedeliveryLandsModeChange(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitInitForTransfer(t, root)
	writeFile(t, root, "src/app.js", "new\n")
	runGitTest(t, root, "add", "-A")
	runGitTest(t, root, "commit", "-q", "-m", "base")
	ctx := context.Background()

	client := sealedFakeMSB()
	m, stateDir := applyTestRuntime(t, client)
	store := ApplyRecordStore{Path: ApplyRecordPath(stateDir, m.InstanceName())}
	// The last recovery copied "new\n" verbatim to both sides.
	recovered := []byte("new\n")
	if err := store.Record("src/app.js", "patch-digest", contentDigest(recovered), contentDigest(recovered)); err != nil {
		t.Fatal(err)
	}

	// The guest only toggles the executable bit: the content digests
	// match, the patch carries old/new mode lines.
	modeOnly := GuestChange{Rel: "src/app.js", Status: 'M', Patch: []byte("diff --git a/src/app.js b/src/app.js\nold mode 100644\nnew mode 100755\n")}
	client.execCaptureResults = []fakeMSBExecCaptureResult{{
		stdout: "755 " + strconv.Itoa(len(recovered)) + "\n" + base64.StdEncoding.EncodeToString(recovered),
	}}
	applied, conflicted, err := ApplySelectedChanges(ctx, m, root, []GuestChange{modeOnly})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(applied) != 1 || len(conflicted) != 0 {
		t.Fatalf("apply = (%+v, %+v), want the mode change applied", applied, conflicted)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "src", "app.js")); string(got) != string(recovered) {
		t.Fatalf("src/app.js = %q, want the content untouched", got)
	}
	// Windows has no Unix permission bits (Go reports 0666 there), so
	// the landed mode is only asserted where the bit exists.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(root, "src", "app.js"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("src/app.js mode = %v, want 0755", info.Mode().Perm())
		}
	}
}

// TestApplyGuestPatchesSubdirectoryOfCheckoutApplies is the F2 guard of
// PR #280's review: when the host folder is a subdirectory of a git
// checkout, git apply resolves the patch paths against the outer repo's
// root and silently skips them (exit 0, file unchanged) — the change is
// then recorded as recovered while nothing crossed. The ceiling on the
// folder's parent stops the repo discovery, so the patch applies to the
// folder itself.
func TestApplyGuestPatchesSubdirectoryOfCheckoutApplies(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	outer := t.TempDir()
	gitInitForTransfer(t, outer)
	writeFile(t, outer, "w/app.js", "old\n")
	runGitTest(t, outer, "add", "-A")
	runGitTest(t, outer, "commit", "-q", "-m", "base")
	root := filepath.Join(outer, "w")

	patch := []byte("diff --git a/app.js b/app.js\n--- a/app.js\n+++ b/app.js\n@@ -1 +1 @@\n-old\n+new\n")
	applied, conflicted, err := ApplyGuestPatches(context.Background(), root, []GuestChange{{Rel: "app.js", Status: 'M', Patch: patch}})
	if err != nil {
		t.Fatalf("ApplyGuestPatches: %v", err)
	}
	if len(applied) != 1 || len(conflicted) != 0 {
		t.Fatalf("ApplyGuestPatches = (%+v, %+v), want the one file applied", applied, conflicted)
	}
	// A Windows runner with core.autocrlf smudges LF to CRLF when git
	// apply writes the file, so compare through a CRLF-insensitive
	// reading: the smudged content and the raw LF content are both the
	// patch applied, and the old content fails either way.
	got, err := os.ReadFile(filepath.Join(root, "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(got), "\r\n", "\n") != "new\n" {
		t.Fatalf("app.js = %q, want the patch applied to the subdirectory: without the ceiling git reports the change as applied while leaving the file untouched", got)
	}
}

// TestCollectGuestChangesSynthesizesReversals covers the F3 guard of PR
// #280's review: a guest that reverted (or deleted) a file recovered before
// is invisible to the diff against its HEAD, so the recovery screens would
// show nothing. The synthesis compares the guest's current content against
// the recorded guest baseline and offers the reversal as a synthetic
// change.
func TestCollectGuestChangesSynthesizesReversals(t *testing.T) {
	head := []byte("old\n")
	recovered := []byte("new\n")

	t.Run("reverted file comes back as a synthetic change", func(t *testing.T) {
		client := sealedFakeMSB()
		m, stateDir := applyTestRuntime(t, client)
		store := ApplyRecordStore{Path: ApplyRecordPath(stateDir, m.InstanceName())}
		if err := store.Record("src/app.js", "patch-digest", contentDigest(recovered), contentDigest(recovered)); err != nil {
			t.Fatal(err)
		}
		client.execCaptureResults = []fakeMSBExecCaptureResult{
			{stdout: "yes"}, // provisioned
			{stdout: ""},    // numstat: the diff is empty
			{stdout: ""},    // diff
			{stdout: contentDigest(head)},
		}
		changes, err := m.CollectGuestChanges(context.Background())
		if err != nil {
			t.Fatalf("CollectGuestChanges: %v", err)
		}
		if len(changes) != 1 {
			t.Fatalf("changes = %+v, want the reverted file synthesized", changes)
		}
		c := changes[0]
		if c.Rel != "src/app.js" || c.Status != 'M' || !c.Synthetic {
			t.Fatalf("synthesized change = %+v, want a synthetic modification of src/app.js", c)
		}
	})

	t.Run("deleted file comes back as a synthetic deletion", func(t *testing.T) {
		client := sealedFakeMSB()
		m, stateDir := applyTestRuntime(t, client)
		store := ApplyRecordStore{Path: ApplyRecordPath(stateDir, m.InstanceName())}
		if err := store.Record("src/app.js", "patch-digest", contentDigest(recovered), contentDigest(recovered)); err != nil {
			t.Fatal(err)
		}
		client.execCaptureResults = []fakeMSBExecCaptureResult{
			{stdout: "yes"},
			{stdout: ""},
			{stdout: ""},
			{stdout: "deleted"},
		}
		changes, err := m.CollectGuestChanges(context.Background())
		if err != nil {
			t.Fatalf("CollectGuestChanges: %v", err)
		}
		if len(changes) != 1 || changes[0].Rel != "src/app.js" || changes[0].Status != 'D' || !changes[0].Synthetic {
			t.Fatalf("changes = %+v, want a synthetic deletion of src/app.js", changes)
		}
	})

	t.Run("file still at its recovered content stays quiet", func(t *testing.T) {
		client := sealedFakeMSB()
		m, stateDir := applyTestRuntime(t, client)
		store := ApplyRecordStore{Path: ApplyRecordPath(stateDir, m.InstanceName())}
		if err := store.Record("src/app.js", "patch-digest", contentDigest(recovered), contentDigest(recovered)); err != nil {
			t.Fatal(err)
		}
		client.execCaptureResults = []fakeMSBExecCaptureResult{
			{stdout: "yes"},
			{stdout: ""},
			{stdout: ""},
			{stdout: contentDigest(recovered)},
		}
		changes, err := m.CollectGuestChanges(context.Background())
		if err != nil {
			t.Fatalf("CollectGuestChanges: %v", err)
		}
		if len(changes) != 0 {
			t.Fatalf("changes = %+v, want none: the guest content matches the recorded baseline", changes)
		}
	})

	t.Run("file reported by the diff is not re-synthesized", func(t *testing.T) {
		client := sealedFakeMSB()
		m, stateDir := applyTestRuntime(t, client)
		store := ApplyRecordStore{Path: ApplyRecordPath(stateDir, m.InstanceName())}
		if err := store.Record("src/app.js", "patch-digest", contentDigest(recovered), contentDigest(recovered)); err != nil {
			t.Fatal(err)
		}
		client.execCaptureResults = []fakeMSBExecCaptureResult{
			{stdout: "yes"},
			{stdout: "1\t1\tsrc/app.js\x00"},
			{stdout: applyFirstSection},
		}
		changes, err := m.CollectGuestChanges(context.Background())
		if err != nil {
			t.Fatalf("CollectGuestChanges: %v", err)
		}
		if len(changes) != 1 || changes[0].Synthetic {
			t.Fatalf("changes = %+v, want only the diff's own change", changes)
		}
		if got := countCalls(client, "execcapture"); got != 3 {
			t.Fatalf("exec captures = %d, want 3: no batch digest read for a path the diff already reports", got)
		}
	})

	t.Run("record without a guest baseline is skipped", func(t *testing.T) {
		client := sealedFakeMSB()
		m, stateDir := applyTestRuntime(t, client)
		store := ApplyRecordStore{Path: ApplyRecordPath(stateDir, m.InstanceName())}
		// A v2-era record: host half only. A reversal cannot be told
		// from a file still carrying the recovered content.
		if err := store.Record("src/app.js", "patch-digest", contentDigest(recovered), ""); err != nil {
			t.Fatal(err)
		}
		client.execCaptureResults = []fakeMSBExecCaptureResult{
			{stdout: "yes"},
			{stdout: ""},
			{stdout: ""},
		}
		changes, err := m.CollectGuestChanges(context.Background())
		if err != nil {
			t.Fatalf("CollectGuestChanges: %v", err)
		}
		if len(changes) != 0 {
			t.Fatalf("changes = %+v, want none: an incomplete baseline must not guess", changes)
		}
		if got := countCalls(client, "execcapture"); got != 3 {
			t.Fatalf("exec captures = %d, want 3: no batch digest read without a guest baseline", got)
		}
	})
}

// TestApplySelectedChangesReversalLoopThenQuiet runs the F3 end-to-end
// shape: the synthesis offers a reverted file, the apply brings the host
// back to the guest's reverted content, and the next collection is quiet —
// the reversal is recovered, not an endless loop.
func TestApplySelectedChangesReversalLoopThenQuiet(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitInitForTransfer(t, root)
	writeFile(t, root, "src/app.js", "old\n")
	runGitTest(t, root, "add", "-A")
	runGitTest(t, root, "commit", "-q", "-m", "base")
	ctx := context.Background()

	recovered := []byte("new\n")
	head := []byte("old\n")
	client := sealedFakeMSB()
	m, stateDir := applyTestRuntime(t, client)
	store := ApplyRecordStore{Path: ApplyRecordPath(stateDir, m.InstanceName())}
	// The last recovery left "new\n" on both sides; the host file
	// matches so the baseline holds.
	writeFile(t, root, "src/app.js", string(recovered))
	if err := store.Record("src/app.js", "patch-digest", contentDigest(recovered), contentDigest(recovered)); err != nil {
		t.Fatal(err)
	}

	// The guest reverted: the diff is empty, the synthesis offers the
	// reversal.
	client.execCaptureResults = []fakeMSBExecCaptureResult{
		{stdout: "yes"},
		{stdout: ""},
		{stdout: ""},
		{stdout: contentDigest(head)},
	}
	changes, err := m.CollectGuestChanges(ctx)
	if err != nil {
		t.Fatalf("CollectGuestChanges: %v", err)
	}
	if len(changes) != 1 || !changes[0].Synthetic || changes[0].Rel != "src/app.js" {
		t.Fatalf("changes = %+v, want the synthesized reversal", changes)
	}

	// Applying it brings the host back to the guest's reverted content.
	client.execCaptureResults = []fakeMSBExecCaptureResult{{
		stdout: "644 " + strconv.Itoa(len(head)) + "\n" + base64.StdEncoding.EncodeToString(head),
	}}
	applied, conflicted, err := ApplySelectedChanges(ctx, m, root, changes)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(applied) != 1 || len(conflicted) != 0 {
		t.Fatalf("apply = (%+v, %+v), want the reversal applied", applied, conflicted)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "src", "app.js")); string(got) != string(head) {
		t.Fatalf("src/app.js = %q, want the reverted content brought back", got)
	}

	// The next collection is quiet: the guest matches the recorded
	// baseline again.
	client.execCaptureResults = []fakeMSBExecCaptureResult{
		{stdout: "yes"},
		{stdout: ""},
		{stdout: ""},
		{stdout: contentDigest(head)},
	}
	after, err := m.CollectGuestChanges(ctx)
	if err != nil {
		t.Fatalf("CollectGuestChanges after the reversal: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("changes = %+v, want none after the reversal was recovered", after)
	}
}

func TestReadGuestFile(t *testing.T) {
	ctx := context.Background()
	t.Run("returns content and mode", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{{stdout: "644 6\nbmV3ZXIK"}}
		m, _ := applyTestRuntime(t, client)
		content, mode, err := m.ReadGuestFile(ctx, "src/app.js")
		if err != nil {
			t.Fatalf("ReadGuestFile: %v", err)
		}
		if string(content) != "newer\n" {
			t.Fatalf("content = %q, want the base64-decoded bytes", content)
		}
		if mode.Perm() != 0o644 {
			t.Fatalf("mode = %v, want 0644", mode.Perm())
		}
	})
	t.Run("guest read failure is an error", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{{code: 1, stderr: "no such file"}}
		m, _ := applyTestRuntime(t, client)
		_, _, err := m.ReadGuestFile(ctx, "gone.txt")
		if err == nil || !strings.Contains(err.Error(), "could not read gone.txt") {
			t.Fatalf("err = %v, want a guest read failure naming the file", err)
		}
	})
	t.Run("missing header is an error", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{{stdout: "bmV3ZXIK"}}
		m, _ := applyTestRuntime(t, client)
		_, _, err := m.ReadGuestFile(ctx, "src/app.js")
		if err == nil || !strings.Contains(err.Error(), "no file header") {
			t.Fatalf("err = %v, want a missing-header error", err)
		}
	})
	t.Run("unreadable permission is an error", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{{stdout: "nine 6\nbmV3ZXIK"}}
		m, _ := applyTestRuntime(t, client)
		_, _, err := m.ReadGuestFile(ctx, "src/app.js")
		if err == nil || !strings.Contains(err.Error(), "unreadable permission") {
			t.Fatalf("err = %v, want an unreadable-permission error", err)
		}
	})
	t.Run("unreadable content is an error", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{{stdout: "644 6\n!!not-base64!!"}}
		m, _ := applyTestRuntime(t, client)
		_, _, err := m.ReadGuestFile(ctx, "src/app.js")
		if err == nil || !strings.Contains(err.Error(), "unreadable content") {
			t.Fatalf("err = %v, want an unreadable-content error", err)
		}
	})
}

func TestGuestContentDigests(t *testing.T) {
	ctx := context.Background()
	t.Run("pairs digest lines with rels by position", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{{stdout: "aaa\nbbb\n"}}
		m, _ := applyTestRuntime(t, client)
		got, err := m.guestContentDigests(ctx, []string{"b.txt", "a.txt"})
		if err != nil {
			t.Fatalf("guestContentDigests: %v", err)
		}
		want := map[string]string{"b.txt": "aaa", "a.txt": "bbb"}
		if len(got) != 2 || got["b.txt"] != want["b.txt"] || got["a.txt"] != want["a.txt"] {
			t.Fatalf("digests = %v, want %v", got, want)
		}
	})
	t.Run("reports deleted files as deleted", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{{stdout: "deleted"}}
		m, _ := applyTestRuntime(t, client)
		got, err := m.guestContentDigests(ctx, []string{"gone.txt"})
		if err != nil {
			t.Fatalf("guestContentDigests: %v", err)
		}
		if len(got) != 1 || got["gone.txt"] != "deleted" {
			t.Fatalf("digests = %v, want gone.txt reported as deleted", got)
		}
	})
	t.Run("count mismatch fails closed", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{{stdout: "only-one"}}
		m, _ := applyTestRuntime(t, client)
		_, err := m.guestContentDigests(ctx, []string{"a.txt", "b.txt"})
		if err == nil || !strings.Contains(err.Error(), "files asked") {
			t.Fatalf("err = %v, want a digest-count mismatch error", err)
		}
	})
	t.Run("empty digest fails closed", func(t *testing.T) {
		client := sealedFakeMSB()
		client.execCaptureResults = []fakeMSBExecCaptureResult{{stdout: ""}}
		m, _ := applyTestRuntime(t, client)
		_, err := m.guestContentDigests(ctx, []string{"a.txt"})
		if err == nil || !strings.Contains(err.Error(), "files asked") {
			t.Fatalf("err = %v, want a digest-count mismatch error", err)
		}
	})
}

func TestSafeGuestRel(t *testing.T) {
	cases := []struct {
		rel  string
		want bool
	}{
		{"src/app.js", true},
		{"README.md", true},
		{"", false},
		{"./src/app.js", false},
		{"src/../app.js", false},
		{"..", false},
		{"../escape.txt", false},
		{"/etc/passwd", false},
		{".git/config", false},
		{"src/.git", false},
	}
	for _, tc := range cases {
		if got := safeGuestRel(tc.rel); got != tc.want {
			t.Errorf("safeGuestRel(%q) = %v, want %v", tc.rel, got, tc.want)
		}
	}
}

func TestPreserveUnappliedPatches(t *testing.T) {
	stateDir := t.TempDir()
	if path, err := PreserveUnappliedPatches(stateDir, "inst", nil); err != nil || path != "" {
		t.Fatalf("empty set = (%q, %v), want no file and no error", path, err)
	}
	first := GuestChange{Rel: "a.txt", Patch: []byte("diff --git a/a.txt b/a.txt\n@@ -1 +1 @@\n-old\n+new")}
	second := GuestChange{Rel: "b.txt", Patch: []byte("diff --git a/b.txt b/b.txt\n@@ -1 +1 @@\n-x\n+y\n")}
	path, err := PreserveUnappliedPatches(stateDir, "inst", []GuestChange{first, second})
	if err != nil {
		t.Fatalf("PreserveUnappliedPatches: %v", err)
	}
	if path != filepath.Join(InstanceStateDir(stateDir, "inst"), "unapplied-guest-changes.patch") {
		t.Fatalf("path = %q, want the instance state file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The first patch lacks its trailing newline and gains one; the second
	// already ends with one and must not gain a second.
	want := string(first.Patch) + "\n" + string(second.Patch)
	if string(data) != want {
		t.Fatalf("patch file = %q, want %q", data, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no Unix permission bits (Go reports 0666 there).
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("patch file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestSortGuestChanges(t *testing.T) {
	changes := []GuestChange{{Rel: "b.txt"}, {Rel: "a.txt"}, {Rel: "c.txt"}}
	SortGuestChanges(changes)
	if changes[0].Rel != "a.txt" || changes[1].Rel != "b.txt" || changes[2].Rel != "c.txt" {
		t.Fatalf("sorted = %v", changes)
	}
}

func TestUnrecoveredGuestChangesSkipsRecorded(t *testing.T) {
	client := sealedFakeMSB()
	client.execCaptureResults = []fakeMSBExecCaptureResult{
		{stdout: "yes"},
		{stdout: applyNumstatTwo},
		{stdout: applyDiffTwoSections},
	}
	m, stateDir := applyTestRuntime(t, client)
	recorded := GuestChange{Rel: "src/app.js", Patch: []byte(applyFirstSection)}
	store := ApplyRecordStore{Path: ApplyRecordPath(stateDir, m.InstanceName())}
	// No baseline halves: reversal synthesis must stay off a record
	// whose guest half was never written.
	if err := store.Record(recorded.Rel, GuestChangeDigest(recorded), "", ""); err != nil {
		t.Fatalf("Record: %v", err)
	}

	out, err := UnrecoveredGuestChanges(context.Background(), m)
	if err != nil {
		t.Fatalf("UnrecoveredGuestChanges: %v", err)
	}
	if len(out) != 1 || out[0].Rel != "README.md" {
		t.Fatalf("unrecovered = %+v, want exactly README.md", out)
	}
}

func TestPrepareApplyReviewMarksRecoveredAndRefused(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "src/app.js", "old\n")
	writeFile(t, root, ".env", "")
	writeFile(t, root, "README.md", "base\n")
	client := sealedFakeMSB()
	envSection := "diff --git a/.env b/.env\nindex 111..222 100644\n--- a/.env\n+++ b/.env\n@@ -1 +0,0 @@\n-old\n"
	readmeSection := "diff --git a/README.md b/README.md\nindex 111..222 100644\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-old\n+new\n"
	client.execCaptureResults = []fakeMSBExecCaptureResult{
		{stdout: "yes"},
		{stdout: "0\t1\t.env\x001\t1\tREADME.md\x001\t1\tsrc/app.js\x00"},
		{stdout: envSection + readmeSection + applyFirstSection},
	}
	m, stateDir := applyTestRuntime(t, client)
	recorded := GuestChange{Rel: "src/app.js", Patch: []byte(applyFirstSection)}
	store := ApplyRecordStore{Path: ApplyRecordPath(stateDir, m.InstanceName())}
	if err := store.Record(recorded.Rel, GuestChangeDigest(recorded), "", ""); err != nil {
		t.Fatalf("Record: %v", err)
	}

	review, err := PrepareApplyReview(context.Background(), m, root, nil)
	if err != nil {
		t.Fatalf("PrepareApplyReview: %v", err)
	}
	if len(review.Changes) != 3 {
		t.Fatalf("changes = %+v, want the three guest files", review.Changes)
	}
	if !review.Recovered["src/app.js"] {
		t.Fatalf("recovered = %v, want src/app.js", review.Recovered)
	}
	if got := review.Refused[".env"]; got != "dotenv file (default-deny)" {
		t.Fatalf(".env refusal = %q, want the dotenv default-deny", got)
	}
	applicable := review.Applicable()
	if len(applicable) != 1 || applicable[0].Rel != "README.md" {
		t.Fatalf("applicable = %+v, want exactly README.md", applicable)
	}
}
