package justcode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Sealed-workspace provisioning, refresh, and change delivery (P22).
//
// The guest owns its working tree. Content crosses host->guest only through
// ResolveTransferSet + BuildTransferArchive (transfer.go), written with the
// sandbox filesystem API; changes come back only as a reviewed diff. Nothing
// here mounts the host checkout, and nothing copies a host directory
// wholesale.

// msbTransferArchivePath is where the transfer payload is staged inside the
// guest before extraction. It is removed after a successful extraction.
const msbTransferArchivePath = "/tmp/just-code-workspace-transfer.tar.gz"

// TransferOptInPath is the host-side record of per-file re-inclusions for an
// instance. It lives in host state, never in the project.
func TransferOptInPath(stateDir, instance string) string {
	return filepath.Join(InstanceStateDir(stateDir, instance), "transfer-optin.json")
}

// SyncOptions tunes a workspace provisioning or refresh.
type SyncOptions struct {
	// Force allows a refresh over guest-local uncommitted work, which is
	// otherwise refused so nothing is overwritten silently.
	Force bool
	// OptIn carries the recorded per-file re-inclusions (see
	// TransferOptInStore).
	OptIn map[string]bool
	// Print receives the review screen. Nil means stdout.
	Print func(string)
}

func (o SyncOptions) print(msg string) {
	if o.Print != nil {
		o.Print(msg)
		return
	}
	fmt.Print(msg)
}

// WorkspaceManifest resolves the transfer set for this project without
// sending anything, so a caller can show the user what would cross.
func (m *MicrosandboxRuntime) WorkspaceManifest(ctx context.Context, opts SyncOptions) (TransferManifest, error) {
	return ResolveTransferSet(ctx, m.cfg.WorkspaceDir, TransferOptions{OptIn: opts.OptIn})
}

// ProvisionGuestWorkspace performs the initial filtered transfer into a
// freshly created sandbox and creates the guest repository. It is idempotent:
// a guest that already carries a repository is left alone (use
// SyncGuestWorkspace to refresh).
func (m *MicrosandboxRuntime) ProvisionGuestWorkspace(ctx context.Context, opts SyncOptions) error {
	// Never transfer into a workspace whose isolation is unproven: with a
	// pre-P22 bind mount, /workspace is the host checkout and this call would
	// extract over the user's files and commit them.
	if err := m.requireSealedWorkspace(ctx); err != nil {
		return err
	}
	// The initial transfer honours the same recorded per-file decisions as a
	// refresh: a user who ran 'workspace allow' before the first start must
	// not have to run a second command to see it applied.
	opts.OptIn = m.mergeRecordedOptIn(opts.OptIn)
	provisioned, err := m.guestWorkspaceProvisioned(ctx)
	if err != nil {
		return err
	}
	if provisioned {
		return nil
	}
	return m.transferIntoGuest(ctx, opts, "Provisioning the sealed guest workspace")
}

// SyncGuestWorkspace refreshes the guest working tree from the host checkout.
//
// Guest-local uncommitted work is not overwritten silently: the refresh
// refuses unless opts.Force is set, and reports what it found. (Extraction is
// additive by nature — it does not delete files the guest added — but a file
// the guest edited and the host also changed would be clobbered, which is the
// case worth stopping for.)
func (m *MicrosandboxRuntime) SyncGuestWorkspace(ctx context.Context, opts SyncOptions) error {
	if err := m.requireSealedWorkspace(ctx); err != nil {
		return err
	}
	opts.OptIn = m.mergeRecordedOptIn(opts.OptIn)
	provisioned, err := m.guestWorkspaceProvisioned(ctx)
	if err != nil {
		return err
	}
	if !provisioned {
		// Nothing to refresh: a guest without a usable repository has no work
		// to preserve, so the first transfer is what it needs. (A guest with
		// a repository probes as provisioned, which is where the dirty check
		// below applies.)
		return m.transferIntoGuest(ctx, opts, "Provisioning the sealed guest workspace")
	}
	if !opts.Force {
		dirty, err := m.guestWorkingTreeDirty(ctx)
		if err != nil {
			return err
		}
		if dirty != "" {
			return fmt.Errorf("the guest workspace has uncommitted changes, so a refresh could overwrite them; "+
				"export them first ('just-code workspace export') or pass --force to refresh anyway:\n%s", dirty)
		}
	}
	return m.transferIntoGuest(ctx, opts, "Refreshing the sealed guest workspace")
}

// mergeRecordedOptIn folds the persisted per-file re-inclusions into the
// caller's set. The store lives in host state, keyed by instance, so it
// follows the project rather than the invocation.
func (m *MicrosandboxRuntime) mergeRecordedOptIn(given map[string]bool) map[string]bool {
	out := map[string]bool{}
	for rel, allowed := range given {
		out[rel] = allowed
	}
	store := TransferOptInStore{Path: TransferOptInPath(m.stateDirOrDefault(), m.InstanceName()), FS: DefaultFS}
	records, err := store.Load()
	if err != nil {
		// An unreadable store must not silently drop the rules it may hold:
		// fail closed by refusing to transfer at all is wrong here (the rules
		// only ever ADD files), so the caller is told and the default-deny
		// policy still applies to everything else.
		fmt.Fprintf(os.Stderr, "Warning: cannot read the recorded transfer re-inclusions for %s (%v); only the default-deny filter applies\n", m.InstanceName(), err)
		return out
	}
	for rel := range records {
		out[rel] = true
	}
	return out
}

// transferIntoGuest resolves, filters, archives, and writes the transfer set.
func (m *MicrosandboxRuntime) transferIntoGuest(ctx context.Context, opts SyncOptions, headline string) error {
	manifest, err := m.WorkspaceManifest(ctx, opts)
	if err != nil {
		return err
	}
	// During a first launch (issue #172) the English review block is replaced
	// by the French checklist, which reports this manifest's counts instead;
	// printing both would say the same thing twice, in two languages. The
	// review stays available through 'just-code workspace status'.
	if m.Launch != nil && m.Launch.Terminal {
		m.Launch.RecordTransfer(manifest)
	} else {
		opts.print(manifest.Summary())
	}
	// Fail closed on an unattributable finding: a flagged file that cannot be
	// named cannot be excluded, and proceeding would mean guessing that it is
	// harmless.
	if len(manifest.UnmappedFindings) > 0 {
		return fmt.Errorf("the secret detector reported %d finding(s) whose paths could not be matched to a file in %s, so the filter cannot tell which candidate to exclude: "+
			"inspect 'just-code workspace status' and resolve the unmatched findings before retrying",
			len(manifest.UnmappedFindings), manifest.Root)
	}
	included := manifest.Included()
	if len(included) == 0 {
		if manifest.CandidateCount() == 0 {
			// Nothing to send: the source directory holds no files at all.
			// That is an empty project, not a filter decision, so the guest
			// still gets a repository it can work in.
			opts.print(fmt.Sprintf("The transfer source %s holds no files; the guest workspace will be an empty repository.\n", manifest.Root))
		} else {
			return fmt.Errorf("the transfer filter excluded all %d candidate file(s) in %s, so the guest would start with an empty workspace; "+
				"re-include what the project needs with 'just-code workspace allow <path>' (see 'just-code workspace status' for the reasons)",
				manifest.CandidateCount(), manifest.Root)
		}
	}
	payload, err := BuildTransferArchive(manifest)
	if err != nil {
		return err
	}
	opts.print(fmt.Sprintf("%s: writing %s to the guest...\n", headline, FormatByteSize(int64(len(payload)))))
	if err := m.Client.WriteFile(ctx, m.InstanceName(), msbTransferArchivePath, payload); err != nil {
		return fmt.Errorf("writing the transfer payload into the guest failed: %w", err)
	}
	// Extraction is the only thing that creates content in /workspace, and it
	// consumes the payload built from the filtered set above.
	script := fmt.Sprintf("mkdir -p %s && tar -xzf %s -C %s && rm -f %s",
		shellQuote(msbGuestWorkspace), shellQuote(msbTransferArchivePath), shellQuote(msbGuestWorkspace), shellQuote(msbTransferArchivePath))
	if err := m.guestShell(ctx, script); err != nil {
		return fmt.Errorf("extracting the transfer payload inside the guest failed: %w", err)
	}
	if err := m.ensureGuestRepository(ctx); err != nil {
		if marker, markerErr := m.guestOutput(ctx, "git config --local --get just-code.github-workflow-origin"); markerErr == nil && strings.TrimSpace(marker) != "" {
			return fmt.Errorf("GitHub : la copie hôte a été transférée dans la machine isolée mais pas commitée sur la branche publiable. Relisez et récupérez ces changements avec 'just-code workspace apply' : %w", err)
		}
		return err
	}
	opts.print(fmt.Sprintf("Guest workspace ready: %d file(s) in %s.\n", len(included), msbGuestWorkspace))
	return nil
}

// ensureGuestRepository makes the initial guest workspace a Git repository
// with one commit, so guest changes can be delivered as a reviewed diff,
// including for a project root that is not a Git repository on the host.
//
// The host's history is deliberately NOT transferred. A git bundle would
// carry committed content the transfer filter cannot inspect, and the filter
// is the boundary; a snapshot that is filtered file by file is the property
// worth keeping. The host origin URL is recorded as a remote for later
// branch/PR delivery only after the project approves the GitHub binding (P13).
// A host refresh must never append that snapshot to an already publishable
// branch: the agent's own reviewed commits must remain the only additions.
func guestRepositoryBootstrapScript(workspace, gitName, gitEmail string) string {
	return "set -eu; cd " + shellQuote(workspace) +
		"; if git config --local --get just-code.github-workflow-origin >/dev/null 2>&1 || git config --local --get just-code.github-workflow-branch >/dev/null 2>&1 || git config --local --get just-code.github-workflow-snapshot-policy >/dev/null 2>&1; then " +
		"echo 'GitHub : publication automatique refusée. La copie hôte a été transférée dans la machine isolée mais pas commitée sur la branche publiable. Relisez et récupérez ces changements avec just-code workspace apply.' >&2; exit 1; fi" +
		"; git rev-parse --git-dir >/dev/null 2>&1 || git init -q -b main" +
		"; git add -A" +
		"; git -c user.name=" + shellQuote(gitName) + " -c user.email=" + shellQuote(gitEmail) +
		" commit -q -m " + shellQuote("just-code: initial guest snapshot from the host working tree") + " --allow-empty"
}

func (m *MicrosandboxRuntime) ensureGuestRepository(ctx context.Context) error {
	// -q and an explicit identity keep the command independent of the guest's
	// global configuration, which the user may have changed. The publication
	// guard precedes any staging or write. Missing or unreadable markers mean
	// the old, non-publishable snapshot behavior.
	return m.guestShell(ctx, guestRepositoryBootstrapScript(msbGuestWorkspace, m.gitIdentityName(), m.gitIdentityEmail()))
}

// gitIdentityName and gitIdentityEmail resolve the guest commit identity from
// the P11 user settings, falling back to the documented defaults.
func (m *MicrosandboxRuntime) gitIdentityName() string {
	name, _ := guestGitIdentity(m.guestGitConfig())
	return name
}

func (m *MicrosandboxRuntime) gitIdentityEmail() string {
	_, email := guestGitIdentity(m.guestGitConfig())
	return email
}

// guestGitConfig reads the host-side identity records onto a guest config.
func (m *MicrosandboxRuntime) guestGitConfig() GuestConfig {
	name, email := hostGitIdentity()
	return GuestConfig{GitName: name, GitEmail: email}
}

// guestWorkspaceProvisioned reports whether the guest already has a workspace
// repository with a commit.
//
// The probe asks for a resolvable HEAD rather than the presence of .git: a
// repository with no commit (an interrupted provision, or a hand-made empty
// repo) cannot produce a diff, so treating it as provisioned would leave
// change delivery broken with no way to notice.
func (m *MicrosandboxRuntime) guestWorkspaceProvisioned(ctx context.Context) (bool, error) {
	stdout, _, code, err := m.Client.ExecCapture(ctx, m.InstanceName(),
		"cd "+shellQuote(msbGuestWorkspace)+" && git rev-parse --verify -q HEAD >/dev/null && echo yes || echo no")
	if err != nil {
		return false, err
	}
	if code != 0 {
		return false, fmt.Errorf("cannot determine whether the guest workspace is provisioned")
	}
	return strings.TrimSpace(stdout) == "yes", nil
}

// guestWorkingTreeDirty returns the guest's porcelain status, or "" when the
// tree is clean. A guest without a repository (or with an unreadable one) is
// reported as an error rather than as clean: reading a failure as "nothing to
// lose" is how a refresh would overwrite work.
func (m *MicrosandboxRuntime) guestWorkingTreeDirty(ctx context.Context) (string, error) {
	stdout, stderr, code, err := m.Client.ExecCapture(ctx, m.InstanceName(),
		"cd "+shellQuote(msbGuestWorkspace)+" && git status --porcelain")
	if err != nil {
		return "", err
	}
	if code != 0 {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = "git status failed in the guest workspace"
		}
		return "", fmt.Errorf("cannot read the state of the guest workspace: %s", detail)
	}
	return strings.TrimSpace(stdout), nil
}

// guestShell runs a command in the guest, surfacing stderr on failure.
func (m *MicrosandboxRuntime) guestShell(ctx context.Context, command string) error {
	code, stderr, err := m.Client.Exec(ctx, m.InstanceName(), command)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("guest command exited %d: %s", code, strings.TrimSpace(stderr))
	}
	return nil
}

// ExportGuestChanges produces a reviewable patch of the guest's work and
// writes it to the host. It is the change-delivery path for a project without
// an enabled GitHub grant; when enabled, the guest can also deliver reviewed
// changes through its GitHub branch and PR workflow.
//
// The patch is written outside the project by default (host state), so
// reviewing it does not itself dirty the checkout.
func (m *MicrosandboxRuntime) ExportGuestChanges(ctx context.Context, outPath string) (string, error) {
	// `git add -A -N` below mutates the repository it runs in; on a
	// bind-mounted instance that repository is the user's own checkout.
	if err := m.requireSealedWorkspace(ctx); err != nil {
		return "", err
	}
	// `git add -A -N` marks untracked files intent-to-add so they appear in
	// the diff; without it, new files would be invisible in the export.
	cmd := "cd " + shellQuote(msbGuestWorkspace) + " && git add -A -N && git diff HEAD"
	stdout, stderr, code, err := m.Client.ExecCapture(ctx, m.InstanceName(), cmd)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("the guest could not produce a diff (exit %d): %s", code, strings.TrimSpace(stderr))
	}
	if strings.TrimSpace(stdout) == "" {
		return "", nil
	}
	if outPath == "" {
		outPath = filepath.Join(InstanceStateDir(DefaultStateDir(), m.InstanceName()), "guest-changes.patch")
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(outPath, []byte(stdout), 0o600); err != nil {
		return "", err
	}
	return outPath, nil
}
