package codebox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Workspace secret scanning.
//
// Before P22 the workspace was bind-mounted into the sandbox, and this scan
// was the (attempted) security boundary. It could not be one: a secret added
// after startup, in an undetected format, or written by the agent itself
// stays readable. The sealed workspace removes the mount, so this scan is no
// longer a mount gate — it is host-side hygiene advice, and its detection
// logic is reused at the actual boundary by the P22 transfer filter
// (transfer.go), where a refusal is enforceable.

// dotenvSafeSuffixes are .env file suffixes that never carry real values.
var dotenvSafeSuffixes = map[string]bool{
	".example": true,
	".sample":  true,
}

// ScanResult reports what the workspace scan found.
type ScanResult struct {
	// DotenvFiles are paths of files named .env or .env.* (excluding safe
	// suffixes) found under the workspace, relative to the workspace root.
	DotenvFiles []string
	// GitleaksFindings are the redacted findings, one entry per detection,
	// already formatted for display. Empty when gitleaks found nothing.
	GitleaksFindings []string
	// GitleaksMissing records that the bundled scanner could not be prepared,
	// so the secret scan could not run. The scanner is provisioned by
	// codebox itself (issue #177): a missing host installation is no longer
	// a state the user is told about.
	GitleaksMissing bool
	// GitleaksDetail explains a missing scanner for diagnostics. Empty when the
	// scan ran.
	GitleaksDetail string
}

// Blocking reports whether the scan found a reason to refuse startup.
func (r ScanResult) Blocking() bool {
	return len(r.DotenvFiles) > 0 || len(r.GitleaksFindings) > 0
}

// ScanWorkspace walks the workspace for dotenv files and runs a redacted
// secret scan over it with the scanner codebox provisions itself (issue
// #177). Findings are data, not errors; the returned error is reserved for
// failures to inspect the workspace at all.
func ScanWorkspace(ctx context.Context, dir string) (ScanResult, error) {
	res := ScanResult{}

	root, err := filepath.Abs(dir)
	if err != nil {
		return res, err
	}
	// A symlinked workspace root is common (WORKSPACE_DIR pointing at a link
	// to the real project). WalkDir does not follow it, so resolve the root
	// itself before walking; symlinks inside the workspace stay un-followed.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if _, err := os.Stat(root); err != nil {
		return res, err
	}

	// filepath.WalkDir uses Lstat, so symlinks are reported as themselves and
	// never traversed: a symlink named .env is still flagged (it is a .env
	// entry in the workspace), while a symlink to a directory outside the
	// workspace is not descended into. Gitleaks matches this by default
	// (--follow-symlinks is off).
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// The workspace root's own name is the user's choice, not a
			// dotenv file.
			if path == root {
				return nil
			}
			if isDotenvName(d.Name()) {
				fmt.Fprintf(os.Stderr, "Warning: skipping dotenv-named directory %s\n", relTo(root, path))
			}
			return nil
		}
		if isDotenvName(d.Name()) {
			res.DotenvFiles = append(res.DotenvFiles, relTo(root, path))
		}
		return nil
	})
	if err != nil {
		return res, fmt.Errorf("scanning workspace %s: %w", root, err)
	}
	sort.Strings(res.DotenvFiles)

	binary, err := gitleaksExecutable(ctx)
	if err != nil {
		res.GitleaksMissing = true
		res.GitleaksDetail = err.Error()
		return res, nil
	}
	findings, err := runGitleaks(ctx, binary, root)
	if err != nil {
		return res, err
	}
	res.GitleaksFindings = findings
	return res, nil
}

// isDotenvName reports whether name is a dotenv file that can carry real
// values: `.env`, `.env.<something>`, or `<prefix>.env` (docker.env,
// prod.env, …), excluding the safe example/sample suffixes.
//
// The comparison is case-insensitive because the filesystems codebox runs
// on are mostly case-insensitive (macOS, Windows): `.ENV` IS `.env` there, so
// matching only the lowercase spelling would exclude nothing while the file
// still crossed into the guest.
func isDotenvName(name string) bool {
	lower := strings.ToLower(name)
	if dotenvSafeSuffixes[filepath.Ext(lower)] {
		// .env.example / .env.sample: a document, not a value.
		return false
	}
	if lower == ".env" {
		return true
	}
	if strings.HasPrefix(lower, ".env.") {
		return true
	}
	return strings.HasSuffix(lower, ".env")
}

// IsProjectDotenvName reports whether a filename is in the dotenv family
// that may hold local values. It is shared by transfer filtering and the
// explicit secret-import discovery UI.
func IsProjectDotenvName(name string) bool { return isDotenvName(name) }

// gitleaksFinding is the subset of a gitleaks JSON report that the gate shows.
type gitleaksFinding struct {
	RuleID string `json:"RuleID"`
	File   string `json:"File"`
	Line   int    `json:"StartLine"`
	Match  string `json:"Match"` // redacted by --redact
}

// runGitleaksFindings runs `gitleaks detect --no-git` over the working tree
// (the files that can cross into the guest, not git history) with findings
// redacted. Gitleaks exits 1 on leaks and 0 when clean; both are normal
// outcomes here.
func runGitleaksFindings(ctx context.Context, binary, dir string) ([]gitleaksFinding, error) {
	cmd := exec.CommandContext(ctx, binary, "detect",
		"--no-banner", "--no-git", "--redact",
		"--report-format", "json", "--report-path", "-",
		"--source", dir,
	)
	out, err := cmd.Output()
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() != 1 {
		return nil, fmt.Errorf("secret scanner exited %d on %s: %s", ee.ExitCode(), dir, ee.Stderr)
	}
	if err != nil && !isExitError(err) {
		return nil, fmt.Errorf("running the secret scanner on %s: %w", dir, err)
	}
	var findings []gitleaksFinding
	if err := json.Unmarshal(out, &findings); err != nil {
		return nil, fmt.Errorf("parsing the secret scanner report for %s: %w", dir, err)
	}
	return findings, nil
}

// runGitleaks formats the findings for display.
func runGitleaks(ctx context.Context, binary, dir string) ([]string, error) {
	findings, err := runGitleaksFindings(ctx, binary, dir)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, f := range findings {
		lines = append(lines, fmt.Sprintf("%s:%d: %s (%s)", f.File, f.Line, f.Match, f.RuleID))
	}
	return lines, nil
}

func isExitError(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee)
}

func relTo(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	// Normalize to forward slashes so reported paths are identical on every
	// platform (Windows filepath.Rel yields backslash separators).
	return filepath.ToSlash(rel)
}

// CheckWorkspaceGate is the startup gate for the runtimes that still MOUNT the
// host checkout: Tart and agent-vm. There the scan is a real boundary — the
// guest reads whatever is in the mounted directory — so a finding refuses the
// start. When the bundled scanner could not be prepared it warns that the scan
// did not run, without asking the user to install anything.
//
// Microsandbox does NOT call this: its workspace is sealed (P22), the checkout
// is not mounted, and the enforceable boundary is the transfer filter
// (ResolveTransferSet). It uses warnWorkspaceHygiene instead.
func CheckWorkspaceGate(ctx context.Context, dir string) error {
	res, err := ScanWorkspace(ctx, dir)
	if err != nil {
		return err
	}
	if res.GitleaksMissing {
		fmt.Fprintln(os.Stderr, workspaceScanUnavailableWarning())
	}
	if !res.Blocking() {
		return nil
	}
	var b strings.Builder
	b.WriteString("refusing to start: the workspace is mounted into the sandbox and contains secrets.\n")
	for _, f := range res.DotenvFiles {
		fmt.Fprintf(&b, "  dotenv file: %s\n", f)
	}
	for _, f := range res.GitleaksFindings {
		fmt.Fprintf(&b, "  secret: %s\n", f)
	}
	b.WriteString("\nMove real secrets out of the workspace (the agent can read everything mounted there),\n" +
		"or replace the files with non-secret .env.example templates, then run 'codebox start' again.")
	return fmt.Errorf("%s", b.String())
}

// workspaceScanUnavailableWarning is the user-facing notice when the bundled
// scanner could not be prepared. It names no tool and no install step: the
// scanner is part of codebox, so there is nothing for the user to install
// (issue #177). The technical cause is NOT appended: it can carry the release
// URL or an archive name, and this sentence is the one place the criterion
// says must stay tool-agnostic. The cause is kept on ScanResult for
// diagnostics.
func workspaceScanUnavailableWarning() string {
	return "Warning: the workspace secret scan could not run."
}
