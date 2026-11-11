package codebox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The host->guest transfer boundary (P22). The sealed workspace has no bind
// mount, so project content reaches the guest only through this file: it
// resolves a concrete, reviewable file set from the host checkout, applies a
// default-deny filter, and only then builds the archive that is written into
// the guest. There is deliberately no path that copies a directory tree
// wholesale — a wholesale copy could not enforce the filter, and a filter
// that can be bypassed is the original leak with extra steps.
//
// The excluded classes, all by default:
//   - VCS metadata (.git): the guest repo is created in the guest.
//   - symlinks: a link can resolve outside the resolved set.
//   - dotenv-named files beyond the safe .example/.sample suffixes.
//   - files gitleaks flags.
//   - git-ignored files: that is where local .env files and derived state
//     live. They are still *evaluated* so a secret in one is reported by
//     path rather than silently skipped.
//   - files above the per-file cap.
//
// A user may re-include a specific excluded file, per file, through a
// recorded decision (TransferOptInStore). There is no "include everything".

// DefaultTransferMaxFileBytes bounds one transferred file. The transfer
// writes through the sandbox filesystem API, so a single enormous file would
// hold the whole payload in memory.
const DefaultTransferMaxFileBytes int64 = 64 << 20

// TransferOptions tunes one resolution.
type TransferOptions struct {
	// OptIn lists relative paths the user explicitly re-included after the
	// filter excluded them. Each is a recorded per-file decision.
	OptIn map[string]bool
	// MaxFileBytes bounds a single transferred file; zero uses
	// DefaultTransferMaxFileBytes.
	MaxFileBytes int64
}

// TransferExclusion is the structured class of a refusal. Reason is
// review-screen prose — it can be reworded, translated, or carry a detail
// level one surface prefers over another — so a caller that needs to count
// or branch (the first-launch checklist counts dotenv files apart from
// sensitive ones, issue #172) must not match sentences. The category is the
// stable contract underneath the wording.
type TransferExclusion string

const (
	// TransferExclusionNone marks an entry that crossed into the guest,
	// whether the default policy allowed it or an explicit per-file decision
	// re-included it after a rule had refused it. It is the zero value.
	TransferExclusionNone TransferExclusion = ""
	// TransferExclusionSymlink: a link can resolve outside the resolved set.
	TransferExclusionSymlink TransferExclusion = "symlink"
	// TransferExclusionIrregular: a socket, device or FIFO — reading one can
	// block forever, and none carries project content.
	TransferExclusionIrregular TransferExclusion = "irregular"
	// TransferExclusionGitMetadata: .git internals; the guest repository is
	// created inside the guest.
	TransferExclusionGitMetadata TransferExclusion = "git-metadata"
	// TransferExclusionSubmodule: a gitlink; the submodule's own repository
	// is not transferred.
	TransferExclusionSubmodule TransferExclusion = "submodule"
	// TransferExclusionDotenv: a dotenv-named file beyond the safe
	// .example/.sample suffixes.
	TransferExclusionDotenv TransferExclusion = "dotenv"
	// TransferExclusionSecret: the secret detector flagged the file.
	TransferExclusionSecret TransferExclusion = "secret"
	// TransferExclusionGitIgnored: git-ignored local or derived state.
	TransferExclusionGitIgnored TransferExclusion = "git-ignored"
	// TransferExclusionSizeCap: above the per-file transfer cap.
	TransferExclusionSizeCap TransferExclusion = "size-cap"
)

// TransferEntry is one candidate path in the manifest.
type TransferEntry struct {
	// Rel is the slash-separated path relative to the root.
	Rel string
	// Size is the file size in bytes, from Lstat.
	Size int64
	// Included reports whether the file crosses into the guest.
	Included bool
	// OptIn reports that inclusion came from an explicit per-file decision
	// rather than the default policy.
	OptIn bool
	// Overridable reports whether a per-file decision may re-include this
	// path. Structural exclusions (a symlink, Git metadata, a file above the
	// transfer cap) are not overridable: re-including them would break the
	// property the rule protects, not merely override a judgement call.
	Overridable bool
	// Reason explains the decision, for the review screen.
	Reason string
	// Exclusion is the structured class of the refusal (see
	// TransferExclusion). It is TransferExclusionNone exactly when the file
	// crossed into the guest — including a path an explicit per-file
	// decision re-included after a rule refused it, which counts as crossed —
	// so counting exclusions never has to agree with the Reason prose.
	Exclusion TransferExclusion
	// Mode is the file's permission bits, applied on extraction.
	Mode fs.FileMode
}

// TransferManifest is the reviewable outcome of one resolution.
type TransferManifest struct {
	// Root is the absolute, symlink-resolved host root.
	Root string
	// Entries covers every candidate, included or not, sorted by path.
	Entries []TransferEntry
	// GitManaged reports whether candidates came from git's own view of the
	// working tree (tracked + untracked + ignored, minus .git internals).
	GitManaged bool
	// GitleaksMissing records that the bundled scanner could not be prepared,
	// so the detection half of the filter did not run. The dotenv half still
	// did. The scanner is provisioned by codebox itself (issue #177); a
	// missing host installation is not a state the user is told about.
	GitleaksMissing bool
	// GitleaksDetail carries the technical cause when the scanner could not
	// be prepared. It is deliberately NOT rendered in Summary(): the cause can
	// name the release URL or an archive, and the user-facing text must stay
	// tool-agnostic (issue #177). It exists for diagnostics.
	GitleaksDetail string
	// Warnings are non-fatal resolution notes (an unreadable path, a
	// skipped directory).
	Warnings []string
	// UnmappedFindings lists gitleaks findings whose reported path could not
	// be attributed to a candidate. They cannot be matched to a file, so a
	// transfer must refuse rather than proceed without knowing what was
	// flagged — the enforcement lives in workspace_sealed.go, where the
	// transfer is refused while this list is non-empty.
	UnmappedFindings []string
}

// Included returns the entries that cross into the guest.
func (m TransferManifest) Included() []TransferEntry {
	var out []TransferEntry
	for _, e := range m.Entries {
		if e.Included {
			out = append(out, e)
		}
	}
	return out
}

// CandidateCount is how many paths the resolution considered, included or
// not. It distinguishes "the source directory is empty" from "every candidate
// was filtered out", which callers must report differently: the first is a
// legitimately empty project, the second is a filter decision the user has to
// see.
func (m TransferManifest) CandidateCount() int { return len(m.Entries) }

// Excluded returns the entries the filter refused.
func (m TransferManifest) Excluded() []TransferEntry {
	var out []TransferEntry
	for _, e := range m.Entries {
		if !e.Included {
			out = append(out, e)
		}
	}
	return out
}

// ExclusionCount counts the candidates one refusal class refused. The launch
// checklist (issue #172) uses it to report how many dotenv files and how
// many sensitive files stayed behind, from structured categories rather
// than by matching the reason wording. TransferExclusionNone always counts
// zero: it means the file crossed.
func (m TransferManifest) ExclusionCount(c TransferExclusion) int {
	if c == TransferExclusionNone {
		return 0
	}
	n := 0
	for _, e := range m.Entries {
		if !e.Included && e.Exclusion == c {
			n++
		}
	}
	return n
}

// TotalBytes is the transfer size of the included set.
func (m TransferManifest) TotalBytes() int64 {
	var total int64
	for _, e := range m.Included() {
		total += e.Size
	}
	return total
}

// Summary renders the manifest for the review screen: what crosses, and what
// the filter refused with the reason.
func (m TransferManifest) Summary() string {
	var b strings.Builder
	included := m.Included()
	excluded := m.Excluded()
	fmt.Fprintf(&b, "Transfer into the sealed guest: %d file(s), %s\n", len(included), FormatByteSize(m.TotalBytes()))
	if m.GitManaged {
		b.WriteString("Source set: the Git working tree (tracked, untracked, and ignored files; .git metadata is not transferred).\n")
	} else {
		b.WriteString("Source set: the directory tree (no Git repository at this root).\n")
	}
	if m.GitleaksMissing {
		b.WriteString("Warning: secret detection did not run; the dotenv name filter still applied.\n")
	}
	if len(m.UnmappedFindings) > 0 {
		b.WriteString("Warning: these secret-detection findings could not be attributed to a file; the transfer is refused while they are unresolved:\n")
		for _, f := range m.UnmappedFindings {
			fmt.Fprintf(&b, "  %s\n", f)
		}
	}
	for _, w := range m.Warnings {
		fmt.Fprintf(&b, "Note: %s\n", w)
	}
	if len(excluded) > 0 {
		b.WriteString("\nExcluded by the default-deny filter:\n")
		for _, e := range excluded {
			fmt.Fprintf(&b, "  %s (%s)\n", e.Rel, e.Reason)
		}
	}
	return b.String()
}

// ResolveTransferSet resolves the host file set for one transfer. It reads
// the host checkout only; nothing crosses into the guest here.
func ResolveTransferSet(ctx context.Context, root string, opts TransferOptions) (TransferManifest, error) {
	man := TransferManifest{Root: root}
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = DefaultTransferMaxFileBytes
	}

	abs, err := filepath.Abs(root)
	if err != nil {
		return man, err
	}
	// A symlinked root is common; resolve it so candidate paths are stable.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if _, err := os.Stat(abs); err != nil {
		return man, err
	}
	man.Root = abs

	// Secret detection runs over the whole working tree; findings are then
	// mapped onto candidates so a flagged file is refused by path.
	findingsByPath := map[string][]gitleaksFinding{}
	probe, err := probeGitleaks(ctx, abs)
	if err != nil {
		return man, err
	}
	man.GitleaksMissing = probe.missing
	man.GitleaksDetail = probe.detail
	for _, f := range probe.findings {
		rel := findingRelPath(abs, f.File)
		if rel == "" {
			man.UnmappedFindings = append(man.UnmappedFindings, fmt.Sprintf("%s:%d (%s)", f.File, f.Line, f.RuleID))
			continue
		}
		findingsByPath[rel] = append(findingsByPath[rel], f)
	}

	type candidate struct {
		rel     string
		ignored bool
	}
	var candidates []candidate
	if gitAvailable() && gitWorktreeRootExists(abs) {
		man.GitManaged = true
		tracked, err := gitListFiles(ctx, abs, "--cached", "--others", "--exclude-standard")
		if err != nil {
			return man, err
		}
		ignored, err := gitListFiles(ctx, abs, "--others", "--ignored", "--exclude-standard")
		if err != nil {
			return man, err
		}
		for _, rel := range tracked {
			candidates = append(candidates, candidate{rel: rel})
		}
		for _, rel := range ignored {
			candidates = append(candidates, candidate{rel: rel, ignored: true})
		}
	} else {
		// No Git view to enumerate: walk the tree, skipping .git internals.
		err := filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				// An unreadable path is a resolution note, not a failure:
				// the file simply does not cross.
				man.Warnings = append(man.Warnings, fmt.Sprintf("skipped unreadable path %s: %v", relTo(abs, path), err))
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if path == abs {
				return nil
			}
			if d.IsDir() {
				if d.Name() == ".git" {
					return fs.SkipDir
				}
				return nil
			}
			candidates = append(candidates, candidate{rel: relTo(abs, path)})
			return nil
		})
		if err != nil {
			return man, err
		}
	}

	seen := map[string]bool{}
	for _, c := range candidates {
		if c.rel == "" || seen[c.rel] {
			continue
		}
		seen[c.rel] = true
		entry, err := classifyTransferCandidate(abs, c.rel, c.ignored, man.GitManaged, findingsByPath[c.rel], opts)
		if err != nil {
			man.Warnings = append(man.Warnings, fmt.Sprintf("skipped %s: %v", c.rel, err))
			continue
		}
		man.Entries = append(man.Entries, entry)
	}
	sort.Slice(man.Entries, func(i, j int) bool { return man.Entries[i].Rel < man.Entries[j].Rel })
	return man, nil
}

// classifyTransferCandidate applies the default-deny policy to one path. The
// order of the checks decides the reported reason and exclusion category: a
// dotenv file that is also git-ignored is reported as a dotenv exclusion,
// because that is the security-relevant fact.
func classifyTransferCandidate(root, rel string, ignored, gitManaged bool, findings []gitleaksFinding, opts TransferOptions) (TransferEntry, error) {
	entry := TransferEntry{Rel: rel}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(abs)
	if err != nil {
		return entry, err
	}
	if info.IsDir() {
		// In git mode the only directory git reports is a gitlink: a
		// submodule, whose content is a repository of its own. Name it
		// rather than emitting an opaque skip.
		if gitManaged {
			entry.Exclusion = TransferExclusionSubmodule
			entry.Reason = "submodule (gitlink); its own repository is not transferred"
			return entry, nil
		}
		return entry, fmt.Errorf("directory entry")
	}
	entry.Size = info.Size()
	entry.Mode = info.Mode().Perm()

	optIn := opts.OptIn[rel]

	// exclude records a refusal. overridable marks a judgement call the user
	// may revisit; a structural refusal (symlink, Git metadata, size cap)
	// cannot be overridden, because what it protects is not a preference.
	// A refusal carries both the category (for counting) and the reason (for
	// the review screen); a re-inclusion carries neither, because the file
	// crossed.
	exclude := func(category TransferExclusion, reason string, overridable bool) (TransferEntry, error) {
		entry.Overridable = overridable
		if optIn && overridable {
			entry.Included = true
			entry.OptIn = true
			entry.Reason = "re-included by an explicit per-file decision; filter rule was: " + reason
			return entry, nil
		}
		entry.Exclusion = category
		entry.Reason = reason
		return entry, nil
	}

	name := filepath.Base(rel)
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return exclude(TransferExclusionSymlink, "symlink; not transferred into the sealed guest", false)
	case !info.Mode().IsRegular():
		// A FIFO, socket or device node: reading it can block forever, and
		// none of them carries project content worth transferring.
		return exclude(TransferExclusionIrregular, "not a regular file (socket, device or FIFO); not transferred", false)
	case name == ".git" || strings.HasPrefix(rel, ".git/"):
		return exclude(TransferExclusionGitMetadata, "Git metadata; the guest repository is created inside the guest", false)
	case isDotenvName(name):
		return exclude(TransferExclusionDotenv, "dotenv file (default-deny)", true)
	case len(findings) > 0:
		// The rule identifier stays (it says what was found); the tool that
		// reported it does not (the user decides about secrets, not about a
		// scanner, issue #172).
		return exclude(TransferExclusionSecret, fmt.Sprintf("secret detected: %s", findings[0].RuleID), true)
	case ignored:
		return exclude(TransferExclusionGitIgnored, "git-ignored (local or derived state); re-include explicitly if the guest needs it", true)
	case info.Size() > opts.MaxFileBytes:
		return exclude(TransferExclusionSizeCap, fmt.Sprintf("above the per-file transfer cap (%s)", FormatByteSize(opts.MaxFileBytes)), false)
	}
	entry.Included = true
	entry.Reason = "included"
	return entry, nil
}

// gitListFiles runs `git ls-files -z` with the given selector flags and
// returns slash-separated paths relative to the root.
func gitListFiles(ctx context.Context, root string, flags ...string) ([]string, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	args := append([]string{"-C", root, "ls-files", "-z"}, flags...)
	cmd := exec.CommandContext(ctx, git, args...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files in %s: %w", root, err)
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" {
			continue
		}
		paths = append(paths, filepath.ToSlash(p))
	}
	return paths, nil
}

func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// gitleaksProbe is one secret-detection run: either findings, or the fact that
// the bundled scanner could not be prepared (in which case the dotenv name
// filter still applies and the caller warns).
type gitleaksProbe struct {
	findings []gitleaksFinding
	missing  bool
	// detail explains a missing scanner for the review screen; empty when the
	// scan ran or when a test supplied the state directly.
	detail string
}

// probeGitleaks is the detection seam. It is a variable so tests can supply
// findings directly: driving it through a stub executable on PATH is not
// portable (a shell script is not executable on Windows, and a resolved path
// differs from an unresolved one on macOS), and the mapping logic under test
// is the code that consumes the findings, not the tool invocation.
var probeGitleaks = func(ctx context.Context, dir string) (gitleaksProbe, error) {
	binary, err := gitleaksExecutable(ctx)
	if err != nil {
		return gitleaksProbe{missing: true, detail: err.Error()}, nil
	}
	findings, err := runGitleaksFindings(ctx, binary, dir)
	if err != nil {
		return gitleaksProbe{}, err
	}
	return gitleaksProbe{findings: findings}, nil
}

// findingRelPath maps a gitleaks report path onto the manifest's relative
// form. Current gitleaks versions report the path relative to --source, but
// an absolute path has been observed in other builds; both are normalized
// here so the mapping cannot silently miss. A path that cannot be attributed
// returns "" and the caller fails closed: dropping a finding would let the
// flagged file cross.
//
// An absolute path is relativized against both the root as given and its
// symlink-resolved form, because the two sides can sit on different views of
// the same tree (macOS /var -> /private/var): comparing raw strings would
// refuse a transfer gitleaks attributed correctly.
func findingRelPath(root, reported string) string {
	if strings.TrimSpace(reported) == "" {
		return ""
	}
	p := filepath.FromSlash(reported)
	if !filepath.IsAbs(p) {
		return NormalizeTransferPath(filepath.ToSlash(p))
	}
	for _, base := range distinctPaths(root) {
		if rel := relativize(base, p); rel != "" {
			return rel
		}
	}
	// The report may name a resolved path while the root is not (or the
	// reverse): canonicalize the report and retry.
	if canon, err := filepath.EvalSymlinks(p); err == nil {
		for _, base := range distinctPaths(root) {
			if rel := relativize(base, canon); rel != "" {
				return rel
			}
		}
	}
	return ""
}

// distinctPaths returns the path as given plus its symlink-resolved form,
// skipping duplicates.
func distinctPaths(p string) []string {
	out := []string{p}
	if canon, err := filepath.EvalSymlinks(p); err == nil && canon != p {
		out = append(out, canon)
	}
	return out
}

// relativize returns the project-relative form of abs under base, or "" when
// abs is not inside base.
func relativize(base, abs string) string {
	rel, err := filepath.Rel(base, abs)
	if err != nil {
		return ""
	}
	return NormalizeTransferPath(filepath.ToSlash(rel))
}

// FormatByteSize renders a byte count for the review screen. The codebox
// package cannot use the CLI's helper in cmd/codebox, so the sealed-transfer
// messages format here.
func FormatByteSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// NormalizeTransferPath canonicalizes a user-supplied path into the
// manifest's form: relative to the project root, slash-separated, cleaned. A
// path that is empty, absolute, or escapes the root returns "", so a
// re-inclusion decision can never be recorded against a file outside the
// project.
func NormalizeTransferPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))
	cleaned = strings.TrimPrefix(cleaned, "./")
	if cleaned == "" || cleaned == "." || cleaned == ".." ||
		strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") ||
		strings.Contains(cleaned, ":") {
		return ""
	}
	return cleaned
}

// exactTimeFormat picks the tar header format that preserves a modification
// time exactly. A USTAR header has one-second resolution, so Go rounds a
// sub-second mtime to the nearest second when writing one. PAX carries the
// value verbatim. Whole-second times have nothing to lose, so they keep the
// smaller USTAR form, and a zero time (a directory whose mtime could not be
// read) has no value worth preserving at all.
func exactTimeFormat(t time.Time) tar.Format {
	if !t.IsZero() && t.Nanosecond() != 0 {
		return tar.FormatPAX
	}
	return tar.FormatUnknown
}

// BuildTransferArchive builds the deterministic tar.gz payload for a manifest.
// Only included entries are added: the archive is built from the resolved
// set, so a path the filter refused cannot appear in the payload.
//
// Modification times are the files' own, not a fixed epoch. Stamping every
// entry with 1970 made the guest's copy look older than every source file, so
// build tools that compare dates (make, bundlers, caches) rebuilt everything
// on each sync and an editor showed the whole tree as untouched since the
// epoch. The payload stays reproducible for a given input, because the same
// files carry the same mtimes: what changed is that the archive now reflects
// the input rather than erasing part of it.
//
// An entry whose mtime carries a sub-second part is written as PAX, because a
// USTAR header stores whole seconds and Go rounds to the nearest one: two files
// created within the same second can then come out with equal timestamps, and a
// tool comparing them has no ordering to act on, exactly for the files a build
// regenerates together. Rounding is monotone, so it can only collapse a tie —
// never invert one — but a tie is enough to make such a tool repeat or skip
// work. Whole-second mtimes stay USTAR, which is smaller, so the exact form is
// paid for only where it can matter. PAX degrades safely: GNU tar restores the
// nanoseconds, and busybox tar ignores them and falls back to the second.
func BuildTransferArchive(man TransferManifest) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	dirsWritten := map[string]bool{}

	for _, e := range man.Included() {
		// Parent directories, so extraction needs no mkdir pass.
		parts := strings.Split(e.Rel, "/")
		for i := 1; i < len(parts); i++ {
			dir := strings.Join(parts[:i], "/")
			if dirsWritten[dir] {
				continue
			}
			dirsWritten[dir] = true
			// A directory's own mtime is the one its children were added or
			// removed against. If it cannot be read, the field stays zero;
			// note that tar renders a zero time as the 1970 epoch rather
			// than omitting it, so this entry does carry that stamp. It is
			// effectively unreachable (an Lstat that fails here fails for
			// the entry itself moments later), and it is not worth failing
			// the transfer over a directory timestamp.
			dirTime := time.Time{}
			if info, err := os.Lstat(filepath.Join(man.Root, filepath.FromSlash(dir))); err == nil {
				dirTime = info.ModTime()
			}
			if err := tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeDir,
				Name:     dir + "/",
				Mode:     0o755,
				ModTime:  dirTime,
				Format:   exactTimeFormat(dirTime),
			}); err != nil {
				return nil, err
			}
		}
		abs := filepath.Join(man.Root, filepath.FromSlash(e.Rel))
		// Re-check at read time. Classification ran earlier, and the path
		// could have been replaced in between: opening a symlink here would
		// pull content from outside the resolved set into the payload, and a
		// file that grew would exceed the size the manifest promised.
		info, err := os.Lstat(abs)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("transfer %s: the path is no longer a regular file (%s)", e.Rel, info.Mode().Type())
		}
		if info.Size() != e.Size {
			return nil, fmt.Errorf("transfer %s: the file changed size since the transfer set was resolved (%d -> %d bytes); re-run the sync", e.Rel, e.Size, info.Size())
		}
		file, err := os.Open(abs)
		if err != nil {
			return nil, err
		}
		// O_NOFOLLOW is unavailable portably; the Lstat above plus the
		// regular-file check narrow the window to a same-size regular-file
		// swap, which the host user would have to perform deliberately.
		data, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		mode := int64(e.Mode.Perm())
		if mode == 0 {
			mode = 0o644
		}
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     e.Rel,
			Mode:     mode,
			Size:     int64(len(data)),
			ModTime:  info.ModTime(),
			Format:   exactTimeFormat(info.ModTime()),
		}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// TransferOptInRecord is one recorded re-inclusion decision.
type TransferOptInRecord struct {
	// Rel is the path relative to the project root.
	Rel string `json:"rel"`
	// Reason is the filter rule the user overrode.
	Reason string `json:"reason,omitempty"`
	// RecordedAt is when the decision was made (RFC3339, UTC).
	RecordedAt string `json:"recordedAt"`
}

// TransferOptInStore persists the per-file re-inclusion decisions so a
// decision survives across syncs instead of being re-asked every time. The
// file is host state, never inside the project.
type TransferOptInStore struct {
	Path string
	FS   FS
}

type transferOptInFile struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Paths         []TransferOptInRecord `json:"paths"`
}

const transferOptInSchemaVersion = 1

// Load returns the recorded decisions keyed by relative path.
func (s TransferOptInStore) Load() (map[string]TransferOptInRecord, error) {
	out := map[string]TransferOptInRecord{}
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
	var raw transferOptInFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("transfer opt-in file %s: %w", s.Path, err)
	}
	for _, r := range raw.Paths {
		if r.Rel != "" {
			out[r.Rel] = r
		}
	}
	return out, nil
}

// Allow records a per-file re-inclusion.
func (s TransferOptInStore) Allow(rel, reason string) error {
	return s.mutate(func(records map[string]TransferOptInRecord) {
		records[rel] = TransferOptInRecord{Rel: rel, Reason: reason, RecordedAt: time.Now().UTC().Format(time.RFC3339)}
	})
}

// Revoke drops a recorded re-inclusion, returning the file to the default
// policy.
func (s TransferOptInStore) Revoke(rel string) error {
	return s.mutate(func(records map[string]TransferOptInRecord) {
		delete(records, rel)
	})
}

func (s TransferOptInStore) mutate(change func(map[string]TransferOptInRecord)) error {
	fsys := s.FS
	if fsys == nil {
		fsys = DefaultFS
	}
	records, err := s.Load()
	if err != nil {
		return err
	}
	change(records)
	file := transferOptInFile{SchemaVersion: transferOptInSchemaVersion}
	for _, r := range records {
		file.Paths = append(file.Paths, r)
	}
	sort.Slice(file.Paths, func(i, j int) bool { return file.Paths[i].Rel < file.Paths[j].Rel })
	if err := fsys.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(fsys, s.Path, append(data, '\n'), 0o600)
}
