package codebox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInitForTransfer makes dir a Git worktree with one commit, so the
// transfer source set comes from git's own view (tracked + untracked +
// ignored) rather than a plain walk.
func gitInitForTransfer(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, args := range [][]string{
		{"init", "-q"},
		// Pin the line-ending policy: byte-level assertions on
		// recovered content must not depend on the runner's global
		// autocrlf (GitHub's Windows images default to true, which
		// makes git apply smudge LF to CRLF).
		{"config", "core.autocrlf", "false"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "T"},
		{"add", "-A"},
		{"commit", "-q", "-m", "init", "--allow-empty"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestTransferExcludesDotenvByDefault is the core canary property: a .env in
// the host checkout never enters the transfer set, so it can never reach the
// guest.
func TestTransferExcludesDotenvByDefault(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\n")
	writeFile(t, dir, ".env", "ALBERT_API_KEY=canary-value\n")
	writeFile(t, dir, ".env.local", "SECRET=canary2\n")
	// The safe templates are documentation, not secrets.
	writeFile(t, dir, ".env.example", "ALBERT_API_KEY=\n")
	gitInitForTransfer(t, dir)

	man, err := ResolveTransferSet(context.Background(), dir, TransferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	included := map[string]bool{}
	for _, e := range man.Included() {
		included[e.Rel] = true
	}
	if !included["main.go"] {
		t.Fatalf("a source file must cross: %v", man.Included())
	}
	if included[".env"] || included[".env.local"] {
		t.Fatalf("dotenv files must not cross: %v", man.Included())
	}
	if !included[".env.example"] {
		t.Fatalf(".env.example carries no value and must cross: %v", man.Included())
	}
	// The exclusion is reported with its reason, not silently dropped.
	var reason string
	for _, e := range man.Excluded() {
		if e.Rel == ".env" {
			reason = e.Reason
		}
	}
	if !strings.Contains(reason, "dotenv") {
		t.Fatalf(".env exclusion reason = %q", reason)
	}
}

// TestTransferCanaryAfterSync pins the refresh case the plan calls out: a
// secret added to the checkout AFTER the first sync must not cross on the
// next resolution either.
func TestTransferCanaryAfterSync(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.py", "print('hi')\n")
	gitInitForTransfer(t, dir)

	before, err := ResolveTransferSet(context.Background(), dir, TransferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Excluded()) != 0 {
		t.Fatalf("precondition: nothing excluded yet, got %v", before.Excluded())
	}

	// Planted after the initial sync, untracked.
	writeFile(t, dir, ".env", "CANARY=leaked-if-transferred\n")

	after, err := ResolveTransferSet(context.Background(), dir, TransferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range after.Included() {
		if e.Rel == ".env" {
			t.Fatal("a secret planted after the first sync crossed on the next resolution")
		}
	}
}

// TestTransferExcludesIgnoredAndSymlinks pins the two non-obvious classes:
// git-ignored files (where local .env files live) and symlinks (which can
// resolve outside the resolved set). Both are reported, not skipped.
func TestTransferExcludesIgnoredAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".gitignore", "node_modules/\n*.log\n")
	writeFile(t, dir, "keep.txt", "keep\n")
	writeFile(t, dir, "debug.log", "log\n")
	writeFile(t, dir, "node_modules/pkg/index.js", "module\n")
	gitInitForTransfer(t, dir)
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	man, err := ResolveTransferSet(context.Background(), dir, TransferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]TransferEntry{}
	for _, e := range man.Entries {
		state[e.Rel] = e
	}
	if !state["keep.txt"].Included {
		t.Fatal("a tracked file must cross")
	}
	if state["debug.log"].Included {
		t.Fatal("an ignored file must not cross")
	}
	if !strings.Contains(state["debug.log"].Reason, "git-ignored") {
		t.Fatalf("ignored reason = %q", state["debug.log"].Reason)
	}
	if state["link"].Included {
		t.Fatal("a symlink must not cross")
	}
}

// TestTransferOptInIsPerFileAndRecorded pins the "no blanket include" rule:
// an explicit decision re-includes exactly one path, and the decision is
// persisted so it survives the next resolution.
func TestTransferOptInIsPerFileAndRecorded(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.go", "package main\n")
	writeFile(t, dir, ".env", "X=1\n")
	writeFile(t, dir, ".env.production", "Y=2\n")
	gitInitForTransfer(t, dir)

	store := TransferOptInStore{Path: filepath.Join(t.TempDir(), "transfer-optin.json"), FS: DefaultFS}
	if err := store.Allow(".env", "dotenv file (default-deny)"); err != nil {
		t.Fatal(err)
	}
	records, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := records[".env"]; !ok {
		t.Fatalf("the decision was not recorded: %v", records)
	}

	man, err := ResolveTransferSet(context.Background(), dir, TransferOptions{OptIn: map[string]bool{".env": true}})
	if err != nil {
		t.Fatal(err)
	}
	included := map[string]TransferEntry{}
	for _, e := range man.Included() {
		included[e.Rel] = e
	}
	if _, ok := included[".env"]; !ok {
		t.Fatal("the re-included file must cross")
	}
	if !included[".env"].OptIn {
		t.Fatal("the entry must be flagged as an explicit decision")
	}
	// The other dotenv file is untouched: decisions are per file.
	if _, ok := included[".env.production"]; ok {
		t.Fatal("a per-file decision must not re-include other excluded files")
	}
}

// TestTransferArchiveContainsOnlyIncludedFiles pins the structural guarantee:
// the payload is built from the resolved set, so a refused path cannot appear
// in it even if it exists on disk.
func TestTransferArchiveContainsOnlyIncludedFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "src/main.go", "package main\n")
	writeFile(t, dir, "src/nested/deep.txt", "deep\n")
	writeFile(t, dir, ".env", "CANARY=do-not-transfer\n")
	gitInitForTransfer(t, dir)

	man, err := ResolveTransferSet(context.Background(), dir, TransferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := BuildTransferArchive(man)
	if err != nil {
		t.Fatal(err)
	}
	content := archiveContent(t, payload)
	for _, want := range []string{"src/main.go", "src/nested/deep.txt"} {
		if _, ok := content[want]; !ok {
			t.Fatalf("archive is missing %s: %v", want, content)
		}
	}
	if _, ok := content[".env"]; ok {
		t.Fatal("the archive must not carry a file the filter refused")
	}
	// Decompress and look for the canary value itself: a gzip stream never
	// holds the plaintext, so a raw-bytes check would pass either way.
	for name, body := range content {
		if strings.Contains(body, "CANARY=do-not-transfer") {
			t.Fatalf("the canary value crossed in %s", name)
		}
	}
}

// TestTransferArchiveIsDeterministic pins reproducibility: the same input
// produces the same bytes, so a refresh is comparable and tests are stable.
func TestTransferArchiveIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "a\n")
	writeFile(t, dir, "b/c.txt", "c\n")
	gitInitForTransfer(t, dir)
	man, err := ResolveTransferSet(context.Background(), dir, TransferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := BuildTransferArchive(man)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildTransferArchive(man)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("two builds of the same manifest differ")
	}
}

// TestTransferNonGitRootUsesSnapshot pins the explicit non-Git decision: the
// root is snapshotted through the same filter rather than rejected, so a
// non-Git project still reaches a usable guest workspace.
func TestTransferNonGitRootUsesSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "notes.md", "hello\n")
	writeFile(t, dir, ".env", "SECRET=canary\n")

	man, err := ResolveTransferSet(context.Background(), dir, TransferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if man.GitManaged {
		t.Fatal("a directory without a repository must not be reported as Git-managed")
	}
	included := map[string]bool{}
	for _, e := range man.Included() {
		included[e.Rel] = true
	}
	if !included["notes.md"] {
		t.Fatalf("the snapshot must carry the project files: %v", man.Included())
	}
	if included[".env"] {
		t.Fatal("the filter applies to a non-Git root too")
	}
}

// TestTransferRejectsPathsOutsideTheProject pins the guard on recorded
// re-inclusions: a decision can only name a file inside the project.
func TestTransferRejectsPathsOutsideTheProject(t *testing.T) {
	for _, in := range []string{"", ".", "..", "../secret", "/etc/passwd", "a/../../b"} {
		if got := NormalizeTransferPath(in); got != "" {
			t.Fatalf("NormalizeTransferPath(%q) = %q, want empty", in, got)
		}
	}
	if got := NormalizeTransferPath("./src/main.go"); got != "src/main.go" {
		t.Fatalf("NormalizeTransferPath = %q", got)
	}
}

func archiveNames(t *testing.T, payload []byte) map[string]bool {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			names[hdr.Name] = true
		}
	}
	return names
}

// withFindings replaces the secret-detection seam for one test, so the
// finding→path mapping is exercised without the real tool (whose presence,
// executable-script support on Windows and path convention we do not control).
func withFindings(t *testing.T, findings []gitleaksFinding) {
	t.Helper()
	orig := probeGitleaks
	probeGitleaks = func(context.Context, string) (gitleaksProbe, error) {
		return gitleaksProbe{findings: findings}, nil
	}
	t.Cleanup(func() { probeGitleaks = orig })
}

// withNoGitleaks makes the probe report the tool as absent.
func withNoGitleaks(t *testing.T) {
	t.Helper()
	orig := probeGitleaks
	probeGitleaks = func(context.Context, string) (gitleaksProbe, error) {
		return gitleaksProbe{missing: true}, nil
	}
	t.Cleanup(func() { probeGitleaks = orig })
}

// TestTransferFindingPathMappingHandlesBothConventions pins the mapping for a
// gitleaks report whose File is relative (current versions) and absolute
// (observed in other builds). Either way the flagged file must be excluded:
// dropping the finding would let it cross.
func TestTransferFindingPathMappingHandlesBothConventions(t *testing.T) {
	for _, tc := range []struct {
		name string
		file func(root string) string
	}{
		{"relative", func(string) string { return "src/config.txt" }},
		{"absolute", func(root string) string { return filepath.Join(root, "src", "config.txt") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "src/config.txt", "password: \"hunter2correcthorsebatterystaple12345\"\n")
			writeFile(t, dir, "src/other.go", "package main\n")
			gitInitForTransfer(t, dir)
			withFindings(t, []gitleaksFinding{{
				RuleID: "generic-api-key", File: tc.file(dir), Line: 1, Match: "REDACTED",
			}})

			man, err := ResolveTransferSet(context.Background(), dir, TransferOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(man.UnmappedFindings) != 0 {
				t.Fatalf("a mappable finding must not be reported as unmapped: %v", man.UnmappedFindings)
			}
			for _, e := range man.Included() {
				if e.Rel == "src/config.txt" {
					t.Fatalf("a flagged file must not cross: %v", man.Included())
				}
			}
			var flagged TransferEntry
			for _, e := range man.Excluded() {
				if e.Rel == "src/config.txt" {
					flagged = e
				}
			}
			if flagged.Exclusion != TransferExclusionSecret {
				t.Fatalf("exclusion category = %q, want %q", flagged.Exclusion, TransferExclusionSecret)
			}
			// The rule identifier says what was found and stays; the
			// scanner's name is an internal dependency and does not.
			if !strings.Contains(flagged.Reason, "generic-api-key") {
				t.Fatalf("the reason must keep the rule identifier: %q", flagged.Reason)
			}
			if strings.Contains(strings.ToLower(flagged.Reason), "gitleaks") {
				t.Fatalf("the reason must not name the scanner: %q", flagged.Reason)
			}
			// An unflagged sibling still crosses: the filter is precise.
			var crossed bool
			for _, e := range man.Included() {
				if e.Rel == "src/other.go" {
					crossed = true
				}
			}
			if !crossed {
				t.Fatalf("an unflagged file must still cross: %v", man.Included())
			}
		})
	}
}

// TestTransferRefusesUnattributableFinding pins the fail-closed rule: a
// finding whose path cannot be matched to a candidate blocks the transfer
// instead of being dropped.
func TestTransferRefusesUnattributableFinding(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\n")
	gitInitForTransfer(t, dir)
	// A path outside the source root cannot be attributed to any candidate.
	withFindings(t, []gitleaksFinding{{
		RuleID: "generic-api-key", File: "/elsewhere/other/config.txt", Line: 1, Match: "REDACTED",
	}})

	man, err := ResolveTransferSet(context.Background(), dir, TransferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(man.UnmappedFindings) == 0 {
		t.Fatal("an unattributable finding must be recorded")
	}
	client := &fakeMSBClient{exists: true, execCaptureResults: []fakeMSBExecCaptureResult{{stdout: "no"}}}
	m := newTestMicrosandbox(t, client)
	m.cfg.WorkspaceDir = dir
	if err := m.ProvisionGuestWorkspace(context.Background(), SyncOptions{Print: func(string) {}}); err == nil {
		t.Fatal("provisioning must refuse while a finding cannot be attributed")
	}
	if len(client.written) != 0 {
		t.Fatal("nothing may be written when the filter cannot decide")
	}
}

// TestTransferWithoutGitleaksStillFiltersNames pins the honest degraded mode:
// when the detection tool is absent the manifest says so, and the name-based
// filter keeps working rather than being silently disabled.
func TestTransferWithoutGitleaksStillFiltersNames(t *testing.T) {
	withNoGitleaks(t)
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\n")
	writeFile(t, dir, ".env", "SECRET=canary\n")
	gitInitForTransfer(t, dir)

	man, err := ResolveTransferSet(context.Background(), dir, TransferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !man.GitleaksMissing {
		t.Fatal("the manifest must report that detection did not run")
	}
	if !strings.Contains(man.Summary(), "secret detection did not run") {
		t.Fatalf("the summary must warn: %q", man.Summary())
	}
	if strings.Contains(strings.ToLower(man.Summary()), "gitleaks") {
		t.Fatalf("the summary must not name the scanner: %q", man.Summary())
	}
	for _, e := range man.Included() {
		if e.Rel == ".env" {
			t.Fatal("the name filter must still refuse a dotenv file")
		}
	}
}

// TestFindingRelPath pins the attribution rules, including the case where the
// two sides sit on different views of the same tree (macOS /var).
func TestFindingRelPath(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name     string
		reported string
		want     string
	}{
		{"relative path", "src/config.txt", "src/config.txt"},
		{"relative with dot prefix", "./src/config.txt", "src/config.txt"},
		{"absolute under the root", filepath.Join(root, "src", "config.txt"), "src/config.txt"},
		{"absolute outside the root", filepath.Join(string(filepath.Separator)+"elsewhere", "x.txt"), ""},
		{"escapes the root", "../outside.txt", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := findingRelPath(root, tc.reported); got != tc.want {
				t.Fatalf("findingRelPath(%q) = %q, want %q", tc.reported, got, tc.want)
			}
		})
	}
	// Exercise the same branch unconditionally on every platform (a symlinked
	// root and a finding naming the real path), so the macOS-relevant logic is
	// covered on Linux CI too. The reported file must exist: gitleaks only
	// reports real paths, and the canonicalization that reconciles two views
	// of a tree resolves the reported path on disk.
	real := filepath.Join(t.TempDir(), "real")
	// The reported file must exist: gitleaks only reports real paths, and the
	// canonicalization that makes cross-view attribution work relies on that.
	if err := os.MkdirAll(filepath.Join(real, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "src", "config.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got := findingRelPath(link, filepath.Join(real, "src", "config.txt")); got != "src/config.txt" {
		t.Fatalf("symlinked-root attribution = %q, want src/config.txt", got)
	}
	if got := findingRelPath(real, filepath.Join(link, "src", "config.txt")); got != "src/config.txt" {
		t.Fatalf("reverse-view attribution = %q, want src/config.txt", got)
	}
}

// archiveContent decompresses the payload and returns each regular file's
// content. Asserting on decompressed content is the only way to prove a value
// did (or did not) cross: a gzip stream never contains the plaintext.
func archiveContent(t *testing.T, payload []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	out := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out[hdr.Name] = string(data)
	}
	return out
}

// TestStructuralExclusionsCannotBeOverridden pins that a per-file decision
// only revisits judgement calls. Re-including a symlink would pull content
// from outside the resolved set, and re-including an oversized file would
// drop the memory bound the cap protects, so both stay refused.
func TestStructuralExclusionsCannotBeOverridden(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "keep.txt", "keep\n")
	writeFile(t, dir, "big.bin", strings.Repeat("x", 4096))
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	gitInitForTransfer(t, dir)

	man, err := ResolveTransferSet(context.Background(), dir, TransferOptions{
		OptIn:        map[string]bool{"link": true, "big.bin": true},
		MaxFileBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range man.Included() {
		switch e.Rel {
		case "link":
			t.Fatal("a symlink must not be re-includable")
		case "big.bin":
			t.Fatal("a file above the cap must not be re-includable")
		}
	}
	// And the manifest says so, so the CLI can refuse the decision instead of
	// recording a rule that never applies. The overridable side is checked on
	// a dotenv file, which is a content exclusion the user may genuinely
	// revisit (a .gitignore would be included, not excluded, and so could not
	// exercise this).
	writeFile(t, dir, ".env", "SECRET=1\n")
	man, err = ResolveTransferSet(context.Background(), dir, TransferOptions{
		OptIn:        map[string]bool{"link": true, "big.bin": true, ".env": true},
		MaxFileBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range man.Excluded() {
		if e.Rel == "link" || e.Rel == "big.bin" {
			if e.Overridable {
				t.Fatalf("%s must be reported as not overridable: %+v", e.Rel, e)
			}
		}
	}
	// A content exclusion stays overridable, and the recorded decision is
	// honoured (this is what makes 'workspace allow' meaningful).
	var envIncluded bool
	for _, e := range man.Included() {
		if e.Rel == ".env" {
			envIncluded = e.OptIn
		}
	}
	if !envIncluded {
		t.Fatalf("a dotenv exclusion must remain overridable by an explicit decision: %+v", man.Entries)
	}
}

// TestTransferExclusionCategoryClassifiesEveryRefusal pins the structured
// category (issue #172): each refusal class carries a stable value a caller
// can count, an included entry carries none, and a re-included path counts as
// crossed — so counting never has to match the reason prose.
func TestTransferExclusionCategoryClassifiesEveryRefusal(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "keep.txt", "keep\n")
	writeFile(t, dir, ".gitignore", "*.log\n")
	writeFile(t, dir, "debug.log", "log\n")
	writeFile(t, dir, ".env", "SECRET=1\n")
	writeFile(t, dir, "flagged.txt", "password: \"hunter2correcthorsebatterystaple12345\"\n")
	writeFile(t, dir, "big.bin", strings.Repeat("x", 4096))
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	gitInitForTransfer(t, dir)
	withFindings(t, []gitleaksFinding{{
		RuleID: "generic-api-key", File: "flagged.txt", Line: 1, Match: "REDACTED",
	}})

	man, err := ResolveTransferSet(context.Background(), dir, TransferOptions{MaxFileBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]TransferExclusion{
		"keep.txt":    TransferExclusionNone,
		"debug.log":   TransferExclusionGitIgnored,
		".env":        TransferExclusionDotenv,
		"flagged.txt": TransferExclusionSecret,
		"big.bin":     TransferExclusionSizeCap,
		"link":        TransferExclusionSymlink,
	}
	state := map[string]TransferEntry{}
	for _, e := range man.Entries {
		state[e.Rel] = e
	}
	for rel, wantCat := range want {
		e, ok := state[rel]
		if !ok {
			t.Fatalf("%s is missing from the manifest: %v", rel, man.Entries)
		}
		if e.Exclusion != wantCat {
			t.Fatalf("%s exclusion category = %q, want %q (reason %q)", rel, e.Exclusion, wantCat, e.Reason)
		}
	}
	// The invariant counting relies on: a category is present exactly when
	// the file did not cross.
	for _, e := range man.Entries {
		if (e.Exclusion == TransferExclusionNone) != e.Included {
			t.Fatalf("exclusion %q and included=%t disagree for %s", e.Exclusion, e.Included, e.Rel)
		}
	}

	// An explicit re-inclusion counts as crossed: the overridden rule stays
	// in the reason prose, but the category stays empty, so a count of
	// exclusions cannot double-count a file the user chose to send.
	man, err = ResolveTransferSet(context.Background(), dir, TransferOptions{
		MaxFileBytes: 1024,
		OptIn:        map[string]bool{".env": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range man.Entries {
		if e.Rel != ".env" {
			continue
		}
		if !e.Included || !e.OptIn || e.Exclusion != TransferExclusionNone {
			t.Fatalf("a re-included file must count as crossed: %+v", e)
		}
	}
}

// TestTransferExclusionCountSeparatesDotenvFromSecrets pins the counting
// contract the first-launch checklist (issue #172) relies on: how many
// environment files and how many sensitive files stayed behind comes from
// structured categories, never from matching the reason prose.
func TestTransferExclusionCountSeparatesDotenvFromSecrets(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\n")
	writeFile(t, dir, ".env", "A=1\n")
	writeFile(t, dir, ".env.staging", "B=2\n")
	writeFile(t, dir, "prod.conf", "api_key: \"hunter2correcthorsebatterystaple12345\"\n")
	writeFile(t, dir, "token.txt", "token: \"hunter2correcthorsebatterystaple12345\"\n")
	gitInitForTransfer(t, dir)
	withFindings(t, []gitleaksFinding{
		{RuleID: "generic-api-key", File: "prod.conf", Line: 1, Match: "REDACTED"},
		{RuleID: "generic-api-key", File: "token.txt", Line: 1, Match: "REDACTED"},
	})

	man, err := ResolveTransferSet(context.Background(), dir, TransferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := man.ExclusionCount(TransferExclusionDotenv); got != 2 {
		t.Fatalf("dotenv exclusions = %d, want 2 (excluded: %v)", got, man.Excluded())
	}
	if got := man.ExclusionCount(TransferExclusionSecret); got != 2 {
		t.Fatalf("secret exclusions = %d, want 2 (excluded: %v)", got, man.Excluded())
	}
	if got := man.ExclusionCount(TransferExclusionGitIgnored); got != 0 {
		t.Fatalf("git-ignored exclusions = %d, want 0", got)
	}
	if got := man.ExclusionCount(TransferExclusionNone); got != 0 {
		t.Fatalf("counting the none category = %d, want 0 (it means the file crossed)", got)
	}
}

// TestTransferUserVisibleTextNamesNoScanner pins the acceptance criterion
// from issue #172: nothing the user reads names the detection tool. The tool
// remains an internal dependency of the filter, so its name may survive only
// where the user must type it — the remediation command.
func TestTransferUserVisibleTextNamesNoScanner(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\n")
	writeFile(t, dir, ".env", "SECRET=1\n")
	gitInitForTransfer(t, dir)

	// Degraded mode: the summary warns about what did not run, without
	// naming what is missing.
	withNoGitleaks(t)
	man, err := ResolveTransferSet(context.Background(), dir, TransferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(man.Summary()), "gitleaks") {
		t.Fatalf("the summary must not name the scanner: %q", man.Summary())
	}
	for _, e := range man.Entries {
		if strings.Contains(strings.ToLower(e.Reason), "gitleaks") {
			t.Fatalf("the reason for %s must not name the scanner: %q", e.Rel, e.Reason)
		}
	}

	// Unattributable findings: the summary's warning and the transfer's
	// refusal stay tool-free, except for the command the user may run.
	withFindings(t, []gitleaksFinding{{
		RuleID: "generic-api-key", File: "/elsewhere/other/config.txt", Line: 1, Match: "REDACTED",
	}})
	man, err = ResolveTransferSet(context.Background(), dir, TransferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(man.UnmappedFindings) == 0 {
		t.Fatal("an unattributable finding must be recorded")
	}
	if strings.Contains(strings.ToLower(man.Summary()), "gitleaks") {
		t.Fatalf("the summary must not name the scanner: %q", man.Summary())
	}
	client := &fakeMSBClient{exists: true, execCaptureResults: []fakeMSBExecCaptureResult{{stdout: "no"}}}
	m := newTestMicrosandbox(t, client)
	m.cfg.WorkspaceDir = dir
	err = m.ProvisionGuestWorkspace(context.Background(), SyncOptions{Print: func(string) {}})
	if err == nil {
		t.Fatal("provisioning must refuse while a finding cannot be attributed")
	}
	if strings.Contains(err.Error(), "gitleaks reported") {
		t.Fatalf("the refusal must not name the scanner: %v", err)
	}
	if !strings.Contains(err.Error(), "codebox workspace status") {
		t.Fatalf("the remediation command must keep its name: %v", err)
	}
}
