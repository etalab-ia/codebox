package justcode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// GuestChange is one file's slice of the agent's uncommitted work in the
// isolated machine: enough to list it on a screen, and the exact patch bytes
// to apply it on its own (issue #173).
type GuestChange struct {
	// Rel is the file's slash-separated path relative to the project root.
	Rel string
	// Status is 'M' (modified), 'A' (added) or 'D' (deleted).
	Status byte
	// Additions and Deletions are the numstat line counts. A binary file has
	// no line counts; Binary reports that case instead.
	Additions int
	Deletions int
	// Binary marks a GIT binary patch.
	Binary bool
	// Patch is this file's own patch, appliable on its own with git apply.
	Patch []byte
	// Synthetic marks a change that carries no patch: the guest reverted
	// (or deleted) a file recovered before, so the diff against its HEAD
	// shows nothing while the host still holds the recovered content. It
	// comes back through the direct path only, never git apply.
	Synthetic bool
}

// GuestChangeDigest fingerprints one change (path + patch bytes) so a later
// run can tell whether this exact change was already applied to the host
// folder. The path is part of the digest so identical content under a new
// name still counts as new work.
func GuestChangeDigest(c GuestChange) string {
	h := sha256.New()
	h.Write([]byte(c.Rel))
	h.Write([]byte{0})
	h.Write(c.Patch)
	return hex.EncodeToString(h.Sum(nil))
}

// CollectGuestChanges reads the agent's work in the isolated machine as one
// change per file, whether it is committed in the guest or still uncommitted
// (#313). It is the read half of `workspace apply` (issue #173) and of the
// exit screen's recovery block (issue #174).
//
// The paths and line counts come from `git diff --numstat -z`, which is
// unambiguous even for paths with spaces or quotes; the per-file patches come
// from the same diff without --numstat, split on its `diff --git` boundaries.
// The two reads are paired by position and verified to agree in count, so a
// guest that changed between the two reads fails closed instead of pairing
// the wrong patch with the wrong path.
func (m *MicrosandboxRuntime) CollectGuestChanges(ctx context.Context) ([]GuestChange, error) {
	// `git add -A -N` below mutates the repository it runs in; on a
	// bind-mounted instance that repository is the user's own checkout.
	if err := m.requireSealedWorkspace(ctx); err != nil {
		return nil, err
	}
	// A guest that was never provisioned holds no work to recover. Reading
	// its missing repository as an error would present an empty exit screen
	// as a failure instead of the normal "no changes" case.
	provisioned, err := m.guestWorkspaceProvisioned(ctx)
	if err != nil {
		return nil, err
	}
	if !provisioned {
		return nil, nil
	}
	// -N (intent-to-add) makes new files visible in the diff; --no-renames
	// keeps one section per path so numstat and diff pair one-to-one. The
	// diff runs against the recovery base, not HEAD, so changes the agent
	// already committed in the guest are collected too (#313).
	base := " && git add -A -N && git diff " + m.guestDiffBase() + " --binary --no-renames"
	numstatOut, stderr, code, err := m.Client.ExecCapture(ctx, m.InstanceName(),
		"cd "+shellQuote(msbGuestWorkspace)+base+" --numstat -z")
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("the guest could not list its changes (exit %d): %s", code, strings.TrimSpace(stderr))
	}
	stats := parseNumstatZ(numstatOut)
	diffOut, stderr, code, err := m.Client.ExecCapture(ctx, m.InstanceName(),
		"cd "+shellQuote(msbGuestWorkspace)+base)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("the guest could not produce a diff (exit %d): %s", code, strings.TrimSpace(stderr))
	}
	if len(stats) == 0 && strings.TrimSpace(diffOut) == "" {
		// The diff is empty, but a guest that reverted a file recovered
		// before is invisible to it: the content matches HEAD again.
		// Synthesize those reversals so both recovery screens still
		// offer them (PR #280 review).
		return m.synthesizeReversalChanges(ctx, stats)
	}
	sections := splitDiffSections(diffOut)
	if len(sections) != len(stats) {
		return nil, fmt.Errorf("the guest workspace changed while it was being read (%d files listed, %d in the patch); run the command again", len(stats), len(sections))
	}
	changes := make([]GuestChange, 0, len(sections))
	for i, section := range sections {
		c := GuestChange{
			Rel:       stats[i].rel,
			Additions: stats[i].additions,
			Deletions: stats[i].deletions,
			Binary:    stats[i].binary,
			Patch:     []byte(section),
		}
		c.Status = guestSectionStatus(section)
		changes = append(changes, c)
	}
	reversals, err := m.synthesizeReversalChanges(ctx, stats)
	if err != nil {
		return nil, err
	}
	return append(changes, reversals...), nil
}

// synthesizeReversalChanges returns one change per previously recovered
// file the guest has since reverted or deleted, so the recovery screens
// still offer it even though the diff against the guest's HEAD shows
// nothing (PR #280 review). stats is the diff's numstat entries, used to
// skip the paths the diff already reports.
func (m *MicrosandboxRuntime) synthesizeReversalChanges(ctx context.Context, stats []guestNumstat) ([]GuestChange, error) {
	store := ApplyRecordStore{Path: ApplyRecordPath(m.stateDirOrDefault(), m.InstanceName())}
	baselines, err := store.LoadRecoveredBaselines()
	if err != nil {
		return nil, err
	}
	inDiff := make(map[string]bool, len(stats))
	for _, s := range stats {
		inDiff[s.rel] = true
	}
	// Map iteration order is random; sort for a stable screen.
	var candidates []string
	for rel, base := range baselines {
		// Without a recorded guest baseline (schema v1 and v2 records)
		// a reversal cannot be told from a file still carrying the
		// recovered content; skip rather than guess.
		if base.Guest == "" || inDiff[rel] {
			continue
		}
		candidates = append(candidates, rel)
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sort.Strings(candidates)
	digests, err := m.guestContentDigests(ctx, candidates)
	if err != nil {
		return nil, err
	}
	var out []GuestChange
	for _, rel := range candidates {
		if digests[rel] == baselines[rel].Guest {
			continue
		}
		status := byte('M')
		if digests[rel] == "deleted" {
			status = 'D'
		}
		out = append(out, GuestChange{Rel: rel, Status: status, Synthetic: true})
	}
	return out, nil
}

// guestContentDigests returns the sha256 of each file's current content in
// the isolated machine, one exec for the whole batch ("deleted" for a path
// that no longer exists). The output lines pair with rels by position, and
// a count mismatch fails closed: the guest changed while it was read.
func (m *MicrosandboxRuntime) guestContentDigests(ctx context.Context, rels []string) (map[string]string, error) {
	out := make(map[string]string, len(rels))
	if len(rels) == 0 {
		return out, nil
	}
	var b strings.Builder
	b.WriteString("cd " + shellQuote(msbGuestWorkspace) + " && for f in")
	for _, rel := range rels {
		b.WriteString(" " + shellQuote(rel))
	}
	b.WriteString("; do if [ -f \"$f\" ]; then sha256sum -- \"$f\" | cut -d' ' -f1; else echo deleted; fi; done")
	stdout, stderr, code, err := m.Client.ExecCapture(ctx, m.InstanceName(), b.String())
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("the guest could not read the recovered files (exit %d): %s", code, strings.TrimSpace(stderr))
	}
	var lines []string
	if trimmed := strings.TrimSpace(stdout); trimmed != "" {
		lines = strings.Split(trimmed, "\n")
	}
	if len(lines) != len(rels) {
		return nil, fmt.Errorf("the guest workspace changed while it was being read (%d files asked, %d answered); run the command again", len(rels), len(lines))
	}
	for i, rel := range rels {
		digest := strings.TrimSpace(lines[i])
		if digest == "" {
			return nil, fmt.Errorf("the guest returned no digest for %s", rel)
		}
		out[rel] = digest
	}
	return out, nil
}

// guestNumstat is one --numstat -z entry.
type guestNumstat struct {
	rel       string
	additions int
	deletions int
	binary    bool
}

// parseNumstatZ parses `git diff --numstat -z` output. Entries are
// `added\tdeleted\tpath` separated by NUL; a binary file reports `-` counts.
// A path may itself contain tabs, so only the first two tabs split fields.
func parseNumstatZ(out string) []guestNumstat {
	var stats []guestNumstat
	for _, entry := range strings.Split(out, "\x00") {
		if entry == "" {
			continue
		}
		tab1 := strings.IndexByte(entry, '\t')
		if tab1 < 0 {
			continue
		}
		tab2 := strings.IndexByte(entry[tab1+1:], '\t')
		if tab2 < 0 {
			continue
		}
		addField := entry[:tab1]
		delField := entry[tab1+1 : tab1+1+tab2]
		s := guestNumstat{rel: entry[tab1+1+tab2+1:]}
		if addField == "-" || delField == "-" {
			s.binary = true
		} else {
			s.additions, _ = strconv.Atoi(addField)
			s.deletions, _ = strconv.Atoi(delField)
		}
		stats = append(stats, s)
	}
	return stats
}

// splitDiffSections splits a `git diff` payload into one section per file,
// each starting at its `diff --git` line. It returns nil when the payload is
// empty or does not start with a section header.
func splitDiffSections(diff string) []string {
	if !strings.HasPrefix(diff, "diff --git ") {
		return nil
	}
	var sections []string
	for {
		next := strings.Index(diff[1:], "\ndiff --git ")
		if next < 0 {
			sections = append(sections, diff)
			return sections
		}
		cut := next + 1
		// The newline before the next header terminates the previous
		// section's last line, so it stays with that section.
		sections = append(sections, diff[:cut+1])
		diff = diff[cut+1:]
	}
}

// guestSectionStatus classifies a diff section as added, deleted or modified
// from its header lines, before the first hunk.
func guestSectionStatus(section string) byte {
	for _, line := range strings.Split(section, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			return 'M'
		case strings.HasPrefix(line, "new file mode "):
			return 'A'
		case strings.HasPrefix(line, "deleted file mode "):
			return 'D'
		case strings.HasPrefix(line, "Binary files "):
			return 'M'
		case strings.HasPrefix(line, "GIT binary patch"):
			return 'M'
		}
	}
	return 'M'
}

// patchContentSize estimates how much content a patch would write, for the
// per-file transfer cap: the added lines of a text patch, or the size stated
// by a binary patch's literal/delta header.
func patchContentSize(section string) int64 {
	if strings.Contains(section, "\nGIT binary patch\n") {
		for _, line := range strings.Split(section, "\n") {
			if n, ok := patchBinarySize(line, "literal "); ok {
				return n
			}
			if n, ok := patchBinarySize(line, "delta "); ok {
				return n
			}
		}
		return 0
	}
	var total int64
	inHunk := false
	for _, line := range strings.Split(section, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case inHunk && strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			total += int64(len(line)) // +1 for the newline the diff omits
		}
	}
	return total
}

func patchBinarySize(line, prefix string) (int64, bool) {
	if !strings.HasPrefix(line, prefix) {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(line[len(prefix):]), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// GuestBranchStatus is the agent's branch state in the isolated machine, for
// the exit screen (issues #174, #306). The zero value means "nothing to
// report": no work branch was created in the guest.
type GuestBranchStatus struct {
	// Branch is the selected work branch, "" when the guest has none.
	Branch string
	// Pushed reports that refs/remotes/origin/<Branch> exists in the guest.
	Pushed bool
	// PRURL is the open pull request found for Branch; empty when there is
	// none or when the probe could not answer.
	PRURL string
	// PRDraft selects the draft wording; meaningful only when PRURL is set.
	PRDraft bool
	// PRKnown reports that the pull-request probe answered. False means the
	// PR state is unknown (gh unusable, network down), never that no PR
	// exists.
	PRKnown bool
}

// guestLocalBranch is one refs/heads entry with its committer date, used to
// pick the most recently worked-on branch when the marker is stale.
type guestLocalBranch struct {
	name          string
	committerUnix int64
}

// selectGuestWorkBranch picks the branch the exit screen reports (#306). The
// workflow marker wins when its branch was actually pushed; otherwise the
// most recently committed pushed branch is used, so a branch the agent
// created under another name is still found. Without any push, the marker
// branch (or the latest local branch) is reported as not pushed. The default
// branch and the workflow's snapshot-backup branch are never candidates.
func selectGuestWorkBranch(marker string, locals []guestLocalBranch, remotes map[string]bool, defaultBranch string) (string, bool) {
	var candidates []guestLocalBranch
	for _, local := range locals {
		if local.name == "" || local.name == defaultBranch || local.name == marker+"-snapshot-backup" {
			continue
		}
		candidates = append(candidates, local)
	}
	latest := func(branches []guestLocalBranch) string {
		best := ""
		var bestUnix int64 = -1
		for _, b := range branches {
			if b.committerUnix > bestUnix || (b.committerUnix == bestUnix && b.name < best) {
				best, bestUnix = b.name, b.committerUnix
			}
		}
		return best
	}
	var pushed []guestLocalBranch
	for _, b := range candidates {
		if remotes["origin/"+b.name] {
			pushed = append(pushed, b)
		}
	}
	if len(pushed) > 0 {
		for _, b := range pushed {
			if b.name == marker {
				return marker, true
			}
		}
		return latest(pushed), true
	}
	for _, b := range candidates {
		if b.name == marker {
			return marker, false
		}
	}
	return latest(candidates), false
}

// parseGuestLocalBranches parses `git for-each-ref` output whose format is
// '%(refname:short)%00%(committerdate:unix)'. A malformed line is skipped:
// one odd ref must not hide the others.
func parseGuestLocalBranches(out string) []guestLocalBranch {
	var locals []guestLocalBranch
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, date, ok := strings.Cut(line, "\x00")
		if !ok {
			continue
		}
		unix, err := strconv.ParseInt(strings.TrimSpace(date), 10, 64)
		if err != nil {
			unix = 0
		}
		locals = append(locals, guestLocalBranch{name: strings.TrimSpace(name), committerUnix: unix})
	}
	return locals
}

// GuestBranchStatus discovers the agent's work branch in the guest and
// whether it was pushed, with its open pull request, for the exit screen
// (issue #306). Discovery no longer relies on the workflow marker alone: the
// agent may have created and pushed a branch under another name. A branch
// that was never created or never pushed is reported as such, not as an
// error; a git read failure is an error and must not read as "no push".
func (m *MicrosandboxRuntime) GuestBranchStatus(ctx context.Context) (GuestBranchStatus, error) {
	prefix := "cd " + shellQuote(msbGuestWorkspace) + " && "
	stdout, _, code, err := m.Client.ExecCapture(ctx, m.InstanceName(),
		prefix+"git config --get just-code.github-workflow-branch")
	if err != nil {
		return GuestBranchStatus{}, err
	}
	marker := ""
	if code == 0 {
		marker = strings.TrimSpace(stdout)
	}
	localsOut, stderr, code, err := m.Client.ExecCapture(ctx, m.InstanceName(),
		prefix+"git for-each-ref --format="+shellQuote("%(refname:short)%00%(committerdate:unix)")+" refs/heads")
	if err != nil {
		return GuestBranchStatus{}, err
	}
	if code != 0 {
		return GuestBranchStatus{}, fmt.Errorf("git for-each-ref refs/heads: %s", strings.TrimSpace(stderr))
	}
	remotesOut, stderr, code, err := m.Client.ExecCapture(ctx, m.InstanceName(),
		prefix+"git for-each-ref --format="+shellQuote("%(refname:short)")+" refs/remotes/origin")
	if err != nil {
		return GuestBranchStatus{}, err
	}
	if code != 0 {
		return GuestBranchStatus{}, fmt.Errorf("git for-each-ref refs/remotes/origin: %s", strings.TrimSpace(stderr))
	}
	remotes := map[string]bool{}
	for _, line := range strings.Split(remotesOut, "\n") {
		if ref := strings.TrimSpace(line); ref != "" {
			remotes[ref] = true
		}
	}
	// The remote HEAD names the default branch, which is not agent work.
	// Best-effort: an unborn or detached HEAD just means no exclusion.
	defaultOut, _, code, err := m.Client.ExecCapture(ctx, m.InstanceName(),
		prefix+"git symbolic-ref --quiet --short refs/remotes/origin/HEAD")
	if err != nil {
		return GuestBranchStatus{}, err
	}
	defaultBranch := ""
	if code == 0 {
		defaultBranch = strings.TrimPrefix(strings.TrimSpace(defaultOut), "origin/")
	}
	branch, pushed := selectGuestWorkBranch(marker, parseGuestLocalBranches(localsOut), remotes, defaultBranch)
	if branch == "" {
		return GuestBranchStatus{PRKnown: true}, nil
	}
	status := GuestBranchStatus{Branch: branch, Pushed: pushed}
	if !pushed {
		return status, nil
	}
	// The pull request probe is best-effort: a guest without a usable `gh`
	// still pushed its branch. Its failure means "PR state unknown", not
	// "no PR" and not a fatal read error (network may simply be down).
	out, _, code, err := m.Client.ExecCapture(ctx, m.InstanceName(),
		prefix+"gh pr list --head "+shellQuote(branch)+" --json url,isDraft --limit 1")
	if err != nil || code != 0 {
		return status, nil
	}
	var prs []struct {
		URL     string `json:"url"`
		IsDraft bool   `json:"isDraft"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &prs) != nil {
		return status, nil
	}
	status.PRKnown = true
	if len(prs) > 0 {
		status.PRURL = prs[0].URL
		status.PRDraft = prs[0].IsDraft
	}
	return status, nil
}

// ApplyRecordPath is the host path of the applied-changes record for one
// instance: which exact guest changes were already recovered into the host
// folder.
func ApplyRecordPath(stateDir, instance string) string {
	return filepath.Join(InstanceStateDir(stateDir, instance), "applied-guest-changes.json")
}

// ApplyRecordStore records which guest changes were already applied to the
// host folder, so the exit screen does not re-offer recovered work (issue
// #174) and a second apply does not report an already-applied file as a
// conflict (issue #173).
type ApplyRecordStore struct {
	Path string
	FS   FS
}

type appliedGuestChangeFile struct {
	SchemaVersion int               `json:"schemaVersion"`
	Digests       map[string]string `json:"digests"`
	// RecoveredContent records, per path, the digest of the host file's
	// content right after the change was applied ("deleted" when the apply
	// removed the file). It is the baseline a later run compares the host
	// against to tell a folder still at its last recovered state from one
	// the user edited since.
	RecoveredContent map[string]string `json:"recoveredContent,omitempty"`
	// RecoveredGuestContent records, per path, the digest of the guest
	// file's content at the moment the change was applied ("deleted" when
	// the guest no longer had it). Compared against the guest's current
	// content, it tells a re-delivered identical change from a new edit,
	// and a reverted recovered file from one still carrying the
	// recovered content.
	RecoveredGuestContent map[string]string `json:"recoveredGuestContent,omitempty"`
}

const appliedGuestChangeSchemaVersion = 3

// Load returns the recorded digests keyed by relative path. A missing record
// is an empty history, not an error.
func (s ApplyRecordStore) Load() (map[string]string, error) {
	out := map[string]string{}
	fsys := s.FS
	if fsys == nil {
		fsys = DefaultFS
	}
	data, err := fsys.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	var raw appliedGuestChangeFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("applied-changes record %s: %w", s.Path, err)
	}
	for rel, digest := range raw.Digests {
		if rel != "" && digest != "" {
			out[rel] = digest
		}
	}
	return out, nil
}

// RecoveredBaseline is the recorded state of one path after its last
// recovery: the host content the apply left behind, and the guest content
// it came from. Either digest is empty when the record predates it.
type RecoveredBaseline struct {
	Host  string
	Guest string
}

// LoadRecoveredBaselines returns the per-path baselines recorded at the
// last successful apply. A record written before a field existed (schema
// v1, v2) carries the older half only: those files fall back to the
// conservative paths until they are applied once more.
func (s ApplyRecordStore) LoadRecoveredBaselines() (map[string]RecoveredBaseline, error) {
	out := map[string]RecoveredBaseline{}
	fsys := s.FS
	if fsys == nil {
		fsys = DefaultFS
	}
	data, err := fsys.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	var raw appliedGuestChangeFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("applied-changes record %s: %w", s.Path, err)
	}
	for rel, digest := range raw.RecoveredContent {
		if rel != "" && digest != "" {
			out[rel] = RecoveredBaseline{Host: digest}
		}
	}
	for rel, digest := range raw.RecoveredGuestContent {
		if rel == "" || digest == "" {
			continue
		}
		base := out[rel]
		base.Guest = digest
		out[rel] = base
	}
	return out, nil
}

// Record marks one guest change as applied: the digest of its patch, for
// the already-recovered classification, and the digests of the host
// content the apply left behind and of the guest content it came from, as
// the baselines for the next recovery. An empty digest leaves the
// corresponding baseline unset.
func (s ApplyRecordStore) Record(rel, patchDigest, hostDigest, guestDigest string) error {
	return s.mutate(func(file *appliedGuestChangeFile) {
		if file.Digests == nil {
			file.Digests = map[string]string{}
		}
		file.Digests[rel] = patchDigest
		if hostDigest == "" {
			return
		}
		if file.RecoveredContent == nil {
			file.RecoveredContent = map[string]string{}
		}
		file.RecoveredContent[rel] = hostDigest
		if guestDigest == "" {
			return
		}
		if file.RecoveredGuestContent == nil {
			file.RecoveredGuestContent = map[string]string{}
		}
		file.RecoveredGuestContent[rel] = guestDigest
	})
}

func (s ApplyRecordStore) mutate(change func(*appliedGuestChangeFile)) error {
	fsys := s.FS
	if fsys == nil {
		fsys = DefaultFS
	}
	file := appliedGuestChangeFile{}
	data, err := fsys.ReadFile(s.Path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	} else if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("applied-changes record %s: %w", s.Path, err)
	}
	change(&file)
	file.SchemaVersion = appliedGuestChangeSchemaVersion
	if err := fsys.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	data, err = json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(fsys, s.Path, append(data, '\n'), 0o600)
}

// ApplyReview is the classified outcome for the apply screen (issue #173):
// every guest change, which are already recovered, and which the filter
// refuses to bring back.
type ApplyReview struct {
	// Changes is every guest change, in diff order.
	Changes []GuestChange
	// Recovered lists paths whose exact change was already applied to the
	// host folder by a previous run.
	Recovered map[string]bool
	// Refused maps a path to the reason it must not be applied.
	Refused map[string]string
}

// Applicable lists the changes the user may apply: not already recovered and
// not refused.
func (r ApplyReview) Applicable() []GuestChange {
	var out []GuestChange
	for _, c := range r.Changes {
		if r.Recovered[c.Rel] || r.Refused[c.Rel] != "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// PrepareApplyReview collects the guest's changes and classifies each one
// against the applied-changes record and the transfer filter.
func PrepareApplyReview(ctx context.Context, m *MicrosandboxRuntime, root string, optIn map[string]bool) (ApplyReview, error) {
	changes, err := m.CollectGuestChanges(ctx)
	if err != nil {
		return ApplyReview{}, err
	}
	store := ApplyRecordStore{Path: ApplyRecordPath(m.stateDirOrDefault(), m.InstanceName()), FS: DefaultFS}
	records, err := store.Load()
	if err != nil {
		return ApplyReview{}, err
	}
	man, err := ResolveTransferSet(ctx, root, TransferOptions{OptIn: optIn})
	if err != nil {
		return ApplyReview{}, err
	}
	policy := newApplyPolicy(root, man, optIn)
	review := ApplyReview{
		Changes:   changes,
		Recovered: map[string]bool{},
		Refused:   map[string]string{},
	}
	for _, c := range changes {
		if records[c.Rel] == GuestChangeDigest(c) {
			review.Recovered[c.Rel] = true
			continue
		}
		if reason := policy.check(ctx, c); reason != "" {
			review.Refused[c.Rel] = reason
		}
	}
	return review, nil
}

// UnrecoveredGuestChanges lists the guest's changes not yet applied to the
// host folder, for the exit screen's recovery block (issue #174). Refused
// changes still count as unrecovered: the file was modified and the change
// did not come back, whatever the reason.
func UnrecoveredGuestChanges(ctx context.Context, m *MicrosandboxRuntime) ([]GuestChange, error) {
	changes, err := m.CollectGuestChanges(ctx)
	if err != nil {
		return nil, err
	}
	store := ApplyRecordStore{Path: ApplyRecordPath(m.stateDirOrDefault(), m.InstanceName()), FS: DefaultFS}
	records, err := store.Load()
	if err != nil {
		return nil, err
	}
	var out []GuestChange
	for _, c := range changes {
		if records[c.Rel] == GuestChangeDigest(c) {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// applyPolicy is the per-file decision context for bringing guest changes
// back: the host manifest (what crossed into the guest), the recorded
// per-file re-inclusions, and the host root for the git-ignored probe.
type applyPolicy struct {
	root       string
	gitManaged bool
	entries    map[string]TransferEntry
	optIn      map[string]bool
}

func newApplyPolicy(root string, man TransferManifest, optIn map[string]bool) applyPolicy {
	entries := make(map[string]TransferEntry, len(man.Entries))
	for _, e := range man.Entries {
		entries[e.Rel] = e
	}
	return applyPolicy{
		root:       root,
		gitManaged: man.GitManaged,
		entries:    entries,
		optIn:      optIn,
	}
}

// check returns the refusal reason for one change, or "" when it may be
// applied to the host folder. The rules mirror the host-to-guest transfer
// filter for the reverse direction: a path the copy would not carry — Git
// metadata, a dotenv file, a secret the filter flagged, git-ignored state —
// must not cross back either.
func (p applyPolicy) check(ctx context.Context, c GuestChange) string {
	// Structural first: an explicit per-file decision cannot override it,
	// the same way the forward filter refuses to record one.
	if path.Base(c.Rel) == ".git" || strings.HasPrefix(c.Rel, ".git/") {
		return "Git metadata never crosses the isolation boundary"
	}
	// A recorded re-inclusion is the user's explicit call that this path may
	// cross the boundary; it holds in both directions.
	if p.optIn[c.Rel] {
		return ""
	}
	if isDotenvName(path.Base(c.Rel)) {
		return "dotenv file (default-deny)"
	}
	if entry, ok := p.entries[c.Rel]; ok {
		if entry.Included {
			return ""
		}
		if entry.Reason != "" {
			return entry.Reason
		}
		return string(entry.Exclusion)
	}
	// The agent created this path after the copy: it has no manifest entry.
	// Host git-ignored state must not come back either — the filter refuses
	// to copy it in, so writing it out would create files the user's own
	// gitignore calls derived state.
	if p.gitIgnored(ctx, c.Rel) {
		return "git-ignored (local or derived state)"
	}
	if patchContentSize(string(c.Patch)) > DefaultTransferMaxFileBytes {
		return "above the per-file transfer cap"
	}
	return ""
}

// gitIgnored reports whether the host's git would ignore this path. It is
// best-effort: without a git-managed host or a git binary, the answer is
// "not ignored", because the probe is a refinement, not a gate.
func (p applyPolicy) gitIgnored(ctx context.Context, rel string) bool {
	if !p.gitManaged {
		return false
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return false
	}
	cmd := exec.CommandContext(ctx, git, "-C", p.root, "check-ignore", "-q", "--", rel)
	return cmd.Run() == nil // exit 0 = ignored; anything else = not known to be ignored
}

// ApplyGuestPatches writes each change's patch into the host folder with
// git apply. A patch that no longer applies — because the host file moved on
// since the copy — is skipped, never forced (issue #173: a conflict must not
// overwrite a host-modified file).
func ApplyGuestPatches(ctx context.Context, root string, files []GuestChange) (applied, conflicted []GuestChange, err error) {
	if len(files) == 0 {
		return nil, nil, nil
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, nil, fmt.Errorf("the 'git' CLI is required to apply changes to your folder but was not found on PATH")
	}
	dir, err := os.MkdirTemp("", "just-code-apply-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	// When the host folder sits inside a git checkout (the guest sandbox
	// is a subdirectory of the user's repo), git apply would resolve
	// paths against the outer repo's root and silently ignore patch
	// paths outside the subdirectory — exit 0, file untouched. The
	// ceiling stops the discovery walk at the folder itself (PR #280
	// review).
	env := os.Environ()
	if ceiling, ok := gitCeilingDirectory(root); ok {
		env = append(env, "GIT_CEILING_DIRECTORIES="+ceiling)
	}
	for i, c := range files {
		patchPath := filepath.Join(dir, fmt.Sprintf("change-%d.patch", i))
		if err := os.WriteFile(patchPath, c.Patch, 0o600); err != nil {
			return nil, nil, err
		}
		check := exec.CommandContext(ctx, git, "apply", "--check", patchPath)
		check.Dir = root
		check.Env = env
		if err := check.Run(); err != nil {
			conflicted = append(conflicted, c)
			continue
		}
		apply := exec.CommandContext(ctx, git, "apply", patchPath)
		apply.Dir = root
		apply.Env = env
		if err := apply.Run(); err != nil {
			// The check passed but the apply failed: report the file as
			// unapplied rather than claiming success for a half-written one.
			conflicted = append(conflicted, c)
			continue
		}
		applied = append(applied, c)
	}
	return applied, conflicted, nil
}

// gitCeilingDirectory returns the directory at which git should stop
// looking for a repository root above root: root's own parent. An empty
// answer means no ceiling is needed (root could not be resolved, or is
// the filesystem root, whose parent is itself).
func gitCeilingDirectory(root string) (string, bool) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	if abs == string(filepath.Separator) {
		return "", false
	}
	return filepath.Dir(abs), true
}

// ReadGuestFile returns one file's current content and permission bits from
// the isolated machine. The content crosses as base64 so any bytes — text,
// UTF-8 or binary — survive the exec channel unchanged.
func (m *MicrosandboxRuntime) ReadGuestFile(ctx context.Context, rel string) ([]byte, os.FileMode, error) {
	cmd := "cd " + shellQuote(msbGuestWorkspace) + " && stat -c '%a %s' -- " + shellQuote(rel) + " && base64 -w0 -- " + shellQuote(rel)
	stdout, stderr, code, err := m.Client.ExecCapture(ctx, m.InstanceName(), cmd)
	if err != nil {
		return nil, 0, err
	}
	if code != 0 {
		return nil, 0, fmt.Errorf("the guest could not read %s (exit %d): %s", rel, code, strings.TrimSpace(stderr))
	}
	nl := strings.IndexByte(stdout, '\n')
	if nl < 0 {
		return nil, 0, fmt.Errorf("the guest returned no file header for %s", rel)
	}
	fields := strings.Fields(stdout[:nl])
	if len(fields) != 2 {
		return nil, 0, fmt.Errorf("the guest returned an unreadable file header for %s: %q", rel, stdout[:nl])
	}
	mode64, err := strconv.ParseUint(fields[0], 8, 32)
	if err != nil {
		return nil, 0, fmt.Errorf("the guest returned an unreadable permission for %s: %q", rel, fields[0])
	}
	content, err := base64.StdEncoding.DecodeString(strings.TrimSpace(stdout[nl+1:]))
	if err != nil {
		return nil, 0, fmt.Errorf("the guest returned unreadable content for %s: %v", rel, err)
	}
	return content, os.FileMode(mode64).Perm(), nil
}

// contentDigest fingerprints file content for the recovered-baseline record.
func contentDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// patchChangesMode reports whether a change's patch carries a permission
// change: git renders it as an "old mode" line, which a content digest
// cannot see. The no-op re-record path uses it to land the guest's mode
// instead of silently dropping it (PR #280 review).
func patchChangesMode(c GuestChange) bool {
	return bytes.Contains(c.Patch, []byte("\nold mode "))
}

// hostContentDigest fingerprints the host file's current content: its sha256,
// or "deleted" when it is absent.
func hostContentDigest(root, rel string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		if os.IsNotExist(err) {
			return "deleted", nil
		}
		return "", err
	}
	return contentDigest(data), nil
}

// safeGuestRel keeps a guest-reported path inside the host root. The diff
// machinery only produces repository-relative paths, but the direct-apply
// path writes with os.WriteFile, which does not enforce that itself.
func safeGuestRel(rel string) bool {
	if rel == "" || path.Clean(rel) != rel || strings.HasPrefix(rel, "/") {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return false
	}
	return path.Base(rel) != ".git" && !strings.HasPrefix(rel, ".git/")
}

// removeHostFile deletes one recovered file from the host folder. A file
// that is already absent counts as removed: the guest's deletion and the
// host's state agree.
func removeHostFile(root, rel string) error {
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// writeRecoveredFile writes one file's guest content and mode to the host
// folder and records both baselines. The caller has already read the
// content from the guest and checked it against the transfer cap.
func writeRecoveredFile(store ApplyRecordStore, root string, c GuestChange, content []byte, mode os.FileMode) error {
	hostPath := filepath.Join(root, filepath.FromSlash(c.Rel))
	if err := os.MkdirAll(filepath.Dir(hostPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(hostPath, content, mode); err != nil {
		return err
	}
	if err := os.Chmod(hostPath, mode); err != nil {
		return err
	}
	digest := contentDigest(content)
	return store.Record(c.Rel, GuestChangeDigest(c), digest, digest)
}

// ApplySelectedChanges applies the user's selection from the apply screen.
//
// The recovered baseline recorded at the last apply decides each file's
// path (PR #280 review):
//
//   - A host file still at its recorded baseline may be brought to the
//     guest's current content directly — the guest's cumulative patch is
//     based on its HEAD, which no longer matches a host that recovered
//     once, so applying it would report a false conflict.
//   - A re-delivered change whose guest content also matches its recorded
//     baseline is a no-op: re-recording it preserves any host edit merged
//     in by the first apply instead of silently deleting it. A re-delivery
//     that carries a permission change lands the guest's mode without
//     touching the content.
//   - A host file the user edited since is never overwritten directly: it
//     goes through git apply, which either merges the new work or reports
//     a visible conflict.
//   - A synthetic change (the guest reverted a recovered file; no patch)
//     is applied directly or reported as a conflict — git apply has
//     nothing to merge.
//
// Every applied file records its new host and guest baselines so the next
// run classifies honestly.
func ApplySelectedChanges(ctx context.Context, m *MicrosandboxRuntime, root string, selected []GuestChange) (applied, conflicted []GuestChange, err error) {
	store := ApplyRecordStore{Path: ApplyRecordPath(m.stateDirOrDefault(), m.InstanceName())}
	baselines, err := store.LoadRecoveredBaselines()
	if err != nil {
		return nil, nil, err
	}
	var patches []GuestChange
	for _, c := range selected {
		base, recorded := baselines[c.Rel]
		hostNow, err := hostContentDigest(root, c.Rel)
		if err != nil {
			return nil, nil, err
		}
		atBaseline := recorded && hostNow == base.Host && safeGuestRel(c.Rel)
		if c.Status == 'D' && hostNow == "deleted" {
			// The guest deleted the file and the host does not have
			// it either: nothing to transfer, and recording keeps
			// the classification honest on the next run.
			if err := store.Record(c.Rel, GuestChangeDigest(c), "deleted", "deleted"); err != nil {
				return nil, nil, err
			}
			applied = append(applied, c)
			continue
		}
		if c.Synthetic {
			// The guest reverted a recovered file: there is no
			// patch to apply, so only a host still at its baseline
			// — and one whose last recovery left it identical to
			// the guest, with no host edit merged in — can take
			// the direct path. Anything else is a conflict the
			// user must see, never a silent skip (PR #280 review).
			if !atBaseline || base.Host != base.Guest {
				conflicted = append(conflicted, c)
				continue
			}
			if c.Status == 'D' {
				// The guest deleted the reverted file: the
				// host loses its recovered copy, nothing to
				// read from the guest.
				if err := removeHostFile(root, c.Rel); err != nil {
					return nil, nil, err
				}
				if err := store.Record(c.Rel, GuestChangeDigest(c), "deleted", "deleted"); err != nil {
					return nil, nil, err
				}
				applied = append(applied, c)
				continue
			}
			content, mode, err := m.ReadGuestFile(ctx, c.Rel)
			if err != nil {
				return nil, nil, err
			}
			if int64(len(content)) > DefaultTransferMaxFileBytes {
				// No patch exists to fall back to: report the
				// reversal as a conflict rather than dropping
				// it silently.
				conflicted = append(conflicted, c)
				continue
			}
			if err := writeRecoveredFile(store, root, c, content, mode); err != nil {
				return nil, nil, err
			}
			applied = append(applied, c)
			continue
		}
		if !atBaseline {
			patches = append(patches, c)
			continue
		}
		if c.Status == 'D' {
			// The guest deleted the file while the host still
			// sits at its recorded baseline: remove it directly.
			// There is nothing to read from the guest — the
			// file is gone there.
			if err := removeHostFile(root, c.Rel); err != nil {
				return nil, nil, err
			}
			if err := store.Record(c.Rel, GuestChangeDigest(c), "deleted", "deleted"); err != nil {
				return nil, nil, err
			}
			applied = append(applied, c)
			continue
		}
		// The host is still at the last recovery's baseline. Compare
		// the guest's current content against its recorded half to
		// tell a re-delivered identical change (no-op) from new work.
		guestContent, mode, err := m.ReadGuestFile(ctx, c.Rel)
		if err != nil {
			return nil, nil, err
		}
		guestNow := contentDigest(guestContent)
		if base.Guest != "" && guestNow == base.Guest {
			// The guest re-delivered exactly what the last
			// recovery already brought over. Re-record without
			// touching the host, so an edit the patch path merged
			// into the host file survives (PR #280 review).
			if patchChangesMode(c) {
				// A content-identical re-delivery can still
				// carry a permission change, which git
				// renders as old/new mode lines and a
				// content digest cannot see: land the
				// guest's current mode, never its
				// content, so merged host edits survive
				// while the executable bit crosses
				// (PR #280 review).
				if err := os.Chmod(filepath.Join(root, filepath.FromSlash(c.Rel)), mode); err != nil {
					return nil, nil, err
				}
			}
			if err := store.Record(c.Rel, GuestChangeDigest(c), hostNow, base.Guest); err != nil {
				return nil, nil, err
			}
			applied = append(applied, c)
			continue
		}
		if base.Guest != "" && base.Host == base.Guest {
			// The last recovery copied this file verbatim: no
			// host edit can be lost by bringing the folder to
			// the guest's current state.
			if int64(len(guestContent)) > DefaultTransferMaxFileBytes {
				// Above the transfer cap: fall back to
				// the patch, which preserves review.
				patches = append(patches, c)
				continue
			}
			if err := writeRecoveredFile(store, root, c, guestContent, mode); err != nil {
				return nil, nil, err
			}
			applied = append(applied, c)
			continue
		}
		// The host is at its baseline but the last apply went through
		// the patch path, so the host may hold edits merged from an
		// older patch. Only git apply can merge the new work without
		// losing them.
		patches = append(patches, c)
	}
	patchApplied, patchConflicted, err := ApplyGuestPatches(ctx, root, patches)
	if err != nil {
		return nil, nil, err
	}
	if len(patchApplied) > 0 {
		rels := make([]string, len(patchApplied))
		for i, c := range patchApplied {
			rels[i] = c.Rel
		}
		guestDigests, digestErr := m.guestContentDigests(ctx, rels)
		for _, c := range patchApplied {
			hostDigest, err := hostContentDigest(root, c.Rel)
			if err != nil {
				return nil, nil, err
			}
			guestDigest := ""
			if digestErr == nil {
				guestDigest = guestDigests[c.Rel]
			}
			// The files are already on the host: a failure to
			// refine the record must not turn an applied change
			// into an error. Record the host half only and let
			// the next run re-derive the guest half.
			if err := store.Record(c.Rel, GuestChangeDigest(c), hostDigest, guestDigest); err != nil {
				return nil, nil, err
			}
			applied = append(applied, c)
		}
	}
	conflicted = append(conflicted, patchConflicted...)
	return applied, conflicted, nil
}

// PreserveUnappliedPatches writes the patches of changes that could not be
// applied to the host state, so the user can review and apply them by hand.
// The file is overwritten on each run: it mirrors the current unapplied set,
// not an append-only history.
func PreserveUnappliedPatches(stateDir, instance string, files []GuestChange) (string, error) {
	if len(files) == 0 {
		return "", nil
	}
	var buf bytes.Buffer
	for _, c := range files {
		buf.Write(c.Patch)
		if !bytes.HasSuffix(c.Patch, []byte("\n")) {
			buf.WriteByte('\n')
		}
	}
	outPath := filepath.Join(InstanceStateDir(stateDir, instance), "unapplied-guest-changes.patch")
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(outPath, buf.Bytes(), 0o600); err != nil {
		return "", err
	}
	return outPath, nil
}

// SortGuestChanges orders changes by path for stable screen output.
func SortGuestChanges(changes []GuestChange) {
	sort.Slice(changes, func(i, j int) bool { return changes[i].Rel < changes[j].Rel })
}
