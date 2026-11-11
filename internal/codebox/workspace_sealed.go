package codebox

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
const msbTransferArchivePath = "/tmp/codebox-workspace-transfer.tar.gz"

// TransferOptInPath is the host-side record of per-file re-inclusions for an
// instance. It lives in host state, never in the project.
func TransferOptInPath(stateDir, instance string) string {
	return filepath.Join(InstanceStateDir(stateDir, instance), "transfer-optin.json")
}

// SyncOptions tunes a workspace provisioning or refresh.
type SyncOptions struct {
	// Force allows a refresh over guest-local uncommitted work, which is
	// otherwise refused so nothing is overwritten silently. A refreshed
	// comparison base keeps outstanding guest work recoverable.
	Force bool
	// preserveRecoveryBase is set when a forced refresh must keep outstanding
	// guest work distinguishable from the incoming host snapshot.
	preserveRecoveryBase bool
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
	base := m.guestDiffBase()
	if base == "HEAD" {
		return fmt.Errorf("the guest workspace predates recovery-base tracking, so a refresh could hide committed work; push or recover that work and recreate the guest before refreshing")
	}
	preserveBase := false
	dirty, err := m.guestWorkingTreeDirty(ctx)
	if err != nil {
		return err
	}
	if dirty != "" {
		if !opts.Force {
			return fmt.Errorf("the guest workspace has uncommitted changes, so a refresh could overwrite them; "+
				"export them first ('codebox workspace export') or pass --force to refresh anyway:\n%s", dirty)
		}
		preserveBase = true
	}
	changed, err := m.guestChangesSinceBase(ctx, base)
	if err != nil {
		return err
	}
	if changed {
		if !opts.Force {
			return fmt.Errorf("the guest workspace has committed changes since the last host transfer; " +
				"export or apply them first ('codebox workspace export' or 'codebox workspace apply'), push them, or pass --force to refresh anyway")
		}
		preserveBase = true
	}
	opts.preserveRecoveryBase = preserveBase
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
	// review stays available through 'codebox workspace status'.
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
			"inspect 'codebox workspace status' and resolve the unmatched findings before retrying",
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
				"re-include what the project needs with 'codebox workspace allow <path>' (see 'codebox workspace status' for the reasons)",
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
	// consumes the payload built from the filtered set above. A base-preserving
	// refresh first backs up the guest's work and then either restores it over
	// the new snapshot or proves the snapshot already contains it.
	if opts.preserveRecoveryBase {
		base := m.guestDiffBase()
		if !isCommitSHA(base) {
			return fmt.Errorf("the guest recovery base changed before the refresh; push or recover guest work and recreate the guest before refreshing")
		}
		stdout, stderr, code, err := m.Client.ExecCapture(ctx, m.InstanceName(), guestRecoveryRefreshScript(
			msbGuestWorkspace, msbTransferArchivePath, base, m.gitIdentityName(), m.gitIdentityEmail()))
		if err != nil {
			return err
		}
		if code != 0 {
			return fmt.Errorf("refreshing the guest workspace while preserving its changes failed: %s", strings.TrimSpace(stderr))
		}
		refreshedBase := ""
		for _, line := range strings.Split(stdout, "\n") {
			if strings.HasPrefix(line, guestRecoveryBasePrefix) {
				refreshedBase = strings.TrimSpace(strings.TrimPrefix(line, guestRecoveryBasePrefix))
			}
		}
		if !isCommitSHA(refreshedBase) {
			return fmt.Errorf("the guest refresh did not return a valid recovery comparison base")
		}
		// Store the comparison snapshot before repository initialization: even
		// when the GitHub publication guard refuses a snapshot commit, the host
		// transfer has happened and later guest edits need this updated base.
		if err := m.storeRecoveryBase(refreshedBase); err != nil {
			return err
		}
	} else {
		script := fmt.Sprintf("mkdir -p %s && tar -xzf %s -C %s && rm -f %s",
			shellQuote(msbGuestWorkspace), shellQuote(msbTransferArchivePath), shellQuote(msbGuestWorkspace), shellQuote(msbTransferArchivePath))
		if err := m.guestShell(ctx, script); err != nil {
			return fmt.Errorf("extracting the transfer payload inside the guest failed: %w", err)
		}
	}
	if err := m.ensureGuestRepository(ctx); err != nil {
		if marker, markerErr := m.guestOutput(ctx, "git config --local --get codebox.github-workflow-origin"); markerErr == nil && strings.TrimSpace(marker) != "" {
			return fmt.Errorf("GitHub : la copie hôte a été transférée dans la machine isolée mais pas commitée sur la branche publiable. Relisez et récupérez ces changements avec 'codebox workspace apply' : %w", err)
		}
		return err
	}
	// Clean first transfers record the repository HEAD. A forced refresh has
	// already stored its comparison snapshot above, whether or not outstanding
	// guest work remains (#313).
	if !opts.preserveRecoveryBase {
		if err := m.recordRecoveryBase(ctx); err != nil {
			return err
		}
	}
	opts.print(fmt.Sprintf("Guest workspace ready: %d file(s) in %s.\n", len(included), msbGuestWorkspace))
	return nil
}

const guestRecoveryAdvanceMarker = "RECOVERY-BASE-ADVANCE"
const guestRecoveryBasePrefix = "RECOVERY-BASE="
const guestRecoveryBaseRef = "refs/codebox/recovery-base"

// guestRecoveryRefreshScript refreshes a guest without losing the diff from
// its recovery base. The backup commit makes rollback possible even when the
// patch conflicts with the incoming host snapshot. The marker means the host
// already contains all recovered work, so the recovery base can advance.
func guestRecoveryRefreshScript(workspace, archivePath, base, gitName, gitEmail string) string {
	changedPath := "/tmp/codebox-refresh-changed"
	incomingPath := "/tmp/codebox-refresh-incoming"
	conflictMessage := "The refreshed host snapshot conflicts with the guest work. The pre-refresh state was restored."
	applyProgram := "set -e; for file do" +
		" if git diff --quiet \"$BACKUP\" -- \":(literal)$file\"; then :; else" +
		" patch=$(mktemp /tmp/codebox-refresh-file.XXXXXX)" +
		"; git diff --no-renames --binary \"$BASE\" \"$BACKUP\" -- \":(literal)$file\" > \"$patch\"" +
		"; if ! git apply --3way \"$patch\"; then rm -f \"$patch\"" +
		"; printf '%s\\n' " + shellQuote(conflictMessage) + " >&2; exit 1; fi" +
		"; rm -f \"$patch\"; fi; done"
	return "set -eu; cd " + shellQuote(workspace) +
		"; REAL_INDEX=$(git rev-parse --git-path index)" +
		"; SAVED_INDEX=$(mktemp /tmp/codebox-original-index.XXXXXX)" +
		"; cp \"$REAL_INDEX\" \"$SAVED_INDEX\"" +
		// The restored index still describes the pre-refresh worktree, but the
		// tar extraction has already replaced files in place with whole-second
		// mtimes. With a current index mtime, git trusts that stale stat data
		// and skips re-reading changed files; backdating the index marks every
		// restored entry racily clean, forcing content re-reads.
		"; restore_index() { if [ -n \"$SAVED_INDEX\" ]; then cp \"$SAVED_INDEX\" \"$REAL_INDEX\"; git update-index -q --really-refresh || true; touch -t 200001010000 \"$REAL_INDEX\"; rm -f \"$SAVED_INDEX\"; SAVED_INDEX=; fi; }" +
		"; trap restore_index EXIT HUP INT TERM" +
		"; ORIGINAL_HEAD=$(git rev-parse HEAD)" +
		"; git add -A" +
		"; TREE=$(git write-tree)" +
		"; BACKUP=$(git -c user.name=" + shellQuote(gitName) + " -c user.email=" + shellQuote(gitEmail) +
		" commit-tree \"$TREE\" -p HEAD -m " + shellQuote("codebox: pre-refresh recovery backup") + ")" +
		"; rollback() { git reset --hard \"$BACKUP\" && git clean -fd && git reset --mixed \"$ORIGINAL_HEAD\"; }" +
		// Capture guest paths before extraction can overwrite any of them.
		"; git diff --name-only --diff-filter=ADMT --no-renames -z " + shellQuote(base) + " > " + shellQuote(changedPath) +
		"; rm -rf " + shellQuote(incomingPath) + "; mkdir " + shellQuote(incomingPath) +
		"; if ! tar -xzf " + shellQuote(archivePath) + " -C " + shellQuote(incomingPath) +
		"; then rollback; printf '%s\\n' 'The refreshed host snapshot could not be read. The pre-refresh state was restored.' >&2; exit 1; fi" +
		"; if ! mkdir -p " + shellQuote(workspace) + " || ! tar -xzf " + shellQuote(archivePath) + " -C " + shellQuote(workspace) +
		"; then rollback; printf '%s\\n' 'The refreshed host snapshot could not be extracted. The pre-refresh state was restored.' >&2; exit 1; fi" +
		"; rm -f " + shellQuote(archivePath) +
		// Additive extraction leaves guest content in place when the archive
		// omits a path. Restore omitted paths to their base state, or remove
		// additions, so leftover guest content cannot prove its own recovery.
		"; if ! INCOMING=" + shellQuote(incomingPath) + " BASE=" + shellQuote(base) + " xargs -0 -n 50 sh -c 'set -e; for file do if [ ! -e \"$INCOMING/$file\" ] && [ ! -L \"$INCOMING/$file\" ]; then if git cat-file -e \"$BASE:$file\" 2>/dev/null; then git restore --source=\"$BASE\" --worktree -- \":(literal)$file\"; else rm -f -- \"$file\"; fi; fi; done' sh < " + shellQuote(changedPath) +
		"; then rollback; rm -rf " + shellQuote(incomingPath) + "; printf '%s\\n' 'The guest changes could not be compared with the refreshed host snapshot. The pre-refresh state was restored.' >&2; exit 1; fi" +
		// The comparison base describes the refreshed host tree before guest
		// work is reapplied. Later edits to refreshed files therefore remain
		// visible while outstanding guest work stays distinguishable.
		"; git add -A" +
		"; HOST_TREE=$(git write-tree)" +
		"; REFRESH_BASE=$(git -c user.name=" + shellQuote(gitName) + " -c user.email=" + shellQuote(gitEmail) +
		" commit-tree \"$HOST_TREE\" -p " + shellQuote(base) + " -m " + shellQuote("codebox: refreshed recovery comparison snapshot") + ")" +
		"; git add -A -N" +
		// Apply each file independently: a host may already contain some, but
		// not all, of the guest changes. Skip recovered content, then use a
		// three-way apply so independent host edits survive alongside guest work.
		"; if ! BASE=" + shellQuote(base) + " BACKUP=\"$BACKUP\" xargs -0 -n 50 sh -c " + shellQuote(applyProgram) + " sh < " + shellQuote(changedPath) +
		"; then rollback; rm -rf " + shellQuote(incomingPath) + "; exit 1; fi" +
		"; rm -f " + shellQuote(changedPath) + " && rm -rf " + shellQuote(incomingPath) +
		"; git update-ref " + shellQuote(guestRecoveryBaseRef) + " \"$REFRESH_BASE\"" +
		"; if git add -A -N && git diff --quiet \"$REFRESH_BASE\" --; then printf '%s\\n' " + guestRecoveryAdvanceMarker + "; fi" +
		"; printf '%s%s\\n' " + shellQuote(guestRecoveryBasePrefix) + " \"$REFRESH_BASE\""
}

// recoveryBasePath is the host path of the recorded recovery base for one
// instance (#313). Keeping it on the host, not as a guest git ref, puts it
// out of the agent's reach: the base cannot move with the agent's commits.
func recoveryBasePath(stateDir, instance string) string {
	return filepath.Join(InstanceStateDir(stateDir, instance), "guest-recovery-base")
}

// isCommitSHA reports whether s looks like a full git commit id. The base is
// interpolated into a guest shell command, so anything else (a flag, a ref
// with shell metacharacters) is rejected and the legacy path is used.
func isCommitSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// recordRecoveryBase stores the guest's current HEAD as the recovery base
// after a host-initiated snapshot commit.
func (m *MicrosandboxRuntime) recordRecoveryBase(ctx context.Context) error {
	head, err := m.guestOutput(ctx, "git rev-parse HEAD")
	if err != nil {
		return fmt.Errorf("recording the guest recovery base: %w", err)
	}
	sha := strings.TrimSpace(head)
	if !isCommitSHA(sha) {
		return fmt.Errorf("recording the guest recovery base: unexpected HEAD %q", sha)
	}
	return m.storeRecoveryBase(sha)
}

// storeRecoveryBase persists a validated guest commit as the comparison base.
func (m *MicrosandboxRuntime) storeRecoveryBase(sha string) error {
	if !isCommitSHA(sha) {
		return fmt.Errorf("recording the guest recovery base: unexpected commit %q", sha)
	}
	path := recoveryBasePath(m.stateDirOrDefault(), m.InstanceName())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(sha+"\n"), 0o600)
}

// guestDiffBase returns the commit the guest diffs are taken against: the
// recorded recovery base, or "HEAD" for an environment created before this
// tracking existed (#313). The legacy fallback keeps old behavior, and
// GuestRecoveryWarning makes its limit explicit instead of guessing a base.
func (m *MicrosandboxRuntime) guestDiffBase() string {
	data, err := os.ReadFile(recoveryBasePath(m.stateDirOrDefault(), m.InstanceName()))
	if err != nil {
		return "HEAD"
	}
	if sha := strings.TrimSpace(string(data)); isCommitSHA(sha) {
		return sha
	}
	return "HEAD"
}

// GuestRecoveryWarning returns the limitation to show for an environment
// created before recovery-base tracking (#313): committed guest work is
// invisible to the legacy HEAD diff, so "no changes" must not read as "all
// work recovered". It returns "" for a guest with a recorded base and for a
// guest that was never provisioned (which holds no work at all).
func (m *MicrosandboxRuntime) GuestRecoveryWarning(ctx context.Context) (string, error) {
	provisioned, err := m.guestWorkspaceProvisioned(ctx)
	if err != nil || !provisioned {
		return "", err
	}
	if m.guestDiffBase() != "HEAD" {
		return "", nil
	}
	return "Cet environnement a été créé avant le suivi des changements commités : les changements déjà commités dans la machine isolée ne sont PAS inclus dans l'export ni dans la récupération. Sauvegardez ces commits (push ou récupération manuelle) avant de recréer la machine.", nil
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
		"; if git config --local --get codebox.github-workflow-origin >/dev/null 2>&1 || git config --local --get codebox.github-workflow-branch >/dev/null 2>&1 || git config --local --get codebox.github-workflow-snapshot-policy >/dev/null 2>&1; then " +
		"echo 'GitHub : publication automatique refusée. La copie hôte a été transférée dans la machine isolée mais pas commitée sur la branche publiable. Relisez et récupérez ces changements avec codebox workspace apply.' >&2; exit 1; fi" +
		"; if ! git rev-parse --git-dir >/dev/null 2>&1; then git init -q; git symbolic-ref HEAD refs/heads/main; fi" +
		"; git add -A" +
		"; git -c user.name=" + shellQuote(gitName) + " -c user.email=" + shellQuote(gitEmail) +
		" commit -q -m " + shellQuote("codebox: initial guest snapshot from the host working tree") + " --allow-empty"
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

// guestChangesSinceBase reports whether tracked guest content differs from the
// last host snapshot, including committed work that a HEAD-only dirty check
// would miss. Untracked files are covered separately by guestWorkingTreeDirty.
func (m *MicrosandboxRuntime) guestChangesSinceBase(ctx context.Context, base string) (bool, error) {
	_, stderr, code, err := m.Client.ExecCapture(ctx, m.InstanceName(),
		"cd "+shellQuote(msbGuestWorkspace)+" && git diff --quiet "+base+" --")
	if err != nil {
		return false, err
	}
	switch code {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, fmt.Errorf("cannot compare the guest workspace with its recovery base: %s", strings.TrimSpace(stderr))
	}
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
	// the diff; without it, new files would be invisible in the export. The
	// diff runs against the recovery base, not HEAD, so work the agent
	// already committed is exported too (#313); --binary carries binary
	// content.
	cmd := "cd " + shellQuote(msbGuestWorkspace) + " && git add -A -N && git diff " + m.guestDiffBase() + " --binary"
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
