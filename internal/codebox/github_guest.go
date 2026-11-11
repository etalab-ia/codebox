package codebox

import (
	"context"
	"fmt"
	"strings"
)

func hasResolvedGitHubBinding(bindings []resolvedBinding) bool {
	for _, binding := range bindings {
		if binding.Kind == CredentialGithub {
			return true
		}
	}
	return false
}

func (m *MicrosandboxRuntime) validateAppliedGitHubOrigin(bindings []resolvedBinding, applied *InstanceState) error {
	if !hasResolvedGitHubBinding(bindings) {
		return nil
	}
	remote, err := ParseGitHubRemote(m.cfg.GitHubRemote.URL)
	if err != nil || remote != m.cfg.GitHubRemote {
		return fmt.Errorf("the approved GitHub workflow requires a supported GitHub.com origin; no runtime changes were made")
	}
	if applied != nil && applied.GitHubOrigin != "" && applied.GitHubOrigin != remote.URL {
		return fmt.Errorf("the approved project origin differs from the guest GitHub origin; review and export guest changes before recreating it. No runtime changes were made")
	}
	return nil
}

func (m *MicrosandboxRuntime) validateStoredGitHubOrigin(bindings []resolvedBinding) error {
	if !hasResolvedGitHubBinding(bindings) {
		return nil
	}
	applied, err := ReadInstanceState(DefaultFS, instanceStatePath(m.stateDirOrDefault(), m.InstanceName()))
	if err != nil {
		return err
	}
	return m.validateAppliedGitHubOrigin(bindings, applied)
}

// validateExistingGitHubPublicationPolicy refuses an already initialized
// guest whose snapshot could have included unapproved host content. It reads
// workflow markers, never a credential, and performs no writes. The healthy
// no-op path calls it before guest mutations even when token resolution fails:
// publishable branch bypass the migration gate on a healthy no-op launch.
func (m *MicrosandboxRuntime) validateExistingGitHubPublicationPolicy(ctx context.Context) error {
	if m.cfg.GitHubRemote.URL == "" {
		return nil
	}
	sandbox, exists, err := m.Client.Lookup(ctx, m.InstanceName())
	if err != nil || !exists || sandbox.Status != "running" {
		return err
	}
	marker, stderr, code, err := m.Client.ExecCapture(ctx, m.InstanceName(),
		"cd "+shellQuote(msbGuestWorkspace)+" && git config --local --get codebox.github-workflow-origin")
	if err != nil {
		return fmt.Errorf("cannot read the guest GitHub workflow marker: %w", err)
	}
	if code == 1 || (code == 128 && strings.Contains(stderr, "can only be used inside a git repository")) {
		// No repository yet, or no workflow marker: normal provisioning cases.
		// Other failures cannot establish that the marker is absent.
		return nil
	}
	if code != 0 || strings.TrimSpace(marker) == "" {
		return fmt.Errorf("cannot read the guest GitHub workflow marker (exit %d): %s", code, strings.TrimSpace(stderr))
	}
	if strings.TrimSpace(marker) != m.cfg.GitHubRemote.URL {
		return fmt.Errorf("the guest GitHub workflow is initialized for a different origin; export guest changes and review the remote change before recreating the guest")
	}
	origin, err := m.guestOutput(ctx, "git remote get-url origin")
	if err != nil {
		return fmt.Errorf("the guest GitHub workflow marker exists but its origin cannot be read: %w", err)
	}
	parsed, err := ParseGitHubRemote(strings.TrimSpace(origin))
	if err != nil || parsed.URL != m.cfg.GitHubRemote.URL {
		return fmt.Errorf("the guest GitHub origin differs from the approved host project origin; refusing to rewrite it")
	}
	policy, err := m.guestOutput(ctx, "git config --local --get codebox.github-workflow-snapshot-policy")
	if err != nil || strings.TrimSpace(policy) != "remote-only-v1" {
		return fmt.Errorf("GitHub : cet environnement a été préparé avec une ancienne règle qui pouvait inclure des fichiers locaux dans les commits publiables. Aucun historique n’a été réécrit. Retirez l’accès GitHub avec 'codebox bindings revoke github' pour reprendre sans publication, puis récupérez les modifications non commitées avec 'codebox workspace apply'. Avant toute recréation, sauvegardez aussi les commits de l’invité : workspace apply ne les récupère pas")
	}
	return nil
}

// configureGitHubWorkspace provisions the guest-only GitHub workflow. It runs
// only when the P09 host-local approval resolved a stored GitHub credential.
// The secret itself remains a proxy placeholder in the guest environment.
func (m *MicrosandboxRuntime) configureGitHubWorkspace(ctx context.Context, bindings []resolvedBinding) error {
	if !hasResolvedGitHubBinding(bindings) {
		return nil
	}
	if m.cfg.GitHubRemote.URL == "" || m.cfg.GitHubRemote.Repo == "" {
		return fmt.Errorf("the GitHub binding is approved but the project has no supported github.com origin; configure origin or revoke the GitHub binding")
	}
	if err := m.guestShell(ctx, githubCLIInstallScript()); err != nil {
		return fmt.Errorf("installing the pinned GitHub CLI in the guest failed: %w", err)
	}

	marker, markerErr := m.guestOutput(ctx, "git config --local --get codebox.github-workflow-origin")
	if markerErr == nil {
		if strings.TrimSpace(marker) != m.cfg.GitHubRemote.URL {
			return fmt.Errorf("the guest GitHub workflow is initialized for a different origin; export guest changes and review the remote change before recreating the guest")
		}
		origin, err := m.guestOutput(ctx, "git remote get-url origin")
		if err != nil {
			return fmt.Errorf("the guest GitHub workflow marker exists but its origin cannot be read: %w", err)
		}
		parsed, err := ParseGitHubRemote(strings.TrimSpace(origin))
		if err != nil || parsed.URL != m.cfg.GitHubRemote.URL {
			return fmt.Errorf("the guest GitHub origin differs from the approved host project origin; refusing to rewrite it")
		}
		policy, err := m.guestOutput(ctx, "git config --local --get codebox.github-workflow-snapshot-policy")
		if err != nil || strings.TrimSpace(policy) != "remote-only-v1" {
			return fmt.Errorf("GitHub : cet environnement a été préparé avec une ancienne règle qui pouvait inclure des fichiers locaux dans les commits publiables. Aucun historique n’a été réécrit. Retirez l’accès GitHub avec 'codebox bindings revoke github' pour reprendre sans publication, puis récupérez les modifications non commitées avec 'codebox workspace apply'. Avant toute recréation, sauvegardez aussi les commits de l’invité : workspace apply ne les récupère pas")
		}
		return nil
	}

	if origin, err := m.guestOutput(ctx, "git remote get-url origin"); err == nil && strings.TrimSpace(origin) != "" {
		parsed, parseErr := ParseGitHubRemote(strings.TrimSpace(origin))
		if parseErr != nil || parsed.URL != m.cfg.GitHubRemote.URL {
			return fmt.Errorf("the guest already has a different origin remote; review it before enabling the GitHub workflow")
		}
		return fmt.Errorf("the guest has an origin remote but no codebox GitHub workflow marker; refusing to reset its branch history")
	}
	remotes, err := m.guestOutput(ctx, "git remote")
	if err != nil {
		return fmt.Errorf("cannot inspect guest Git remotes: %w", err)
	}
	if strings.TrimSpace(remotes) != "" {
		return fmt.Errorf("the guest has a remote without an origin; review its Git configuration before enabling the GitHub workflow")
	}

	// Validate the stored token and distinct permission layers before changing
	// the guest repository. gh honors the proxy-provided GITHUB_TOKEN value.
	if _, err := m.guestOutput(ctx, "GH_PROMPT_DISABLED=1 gh auth status --hostname github.com"); err != nil {
		return fmt.Errorf("the GitHub credential was not accepted by github.com; it may be expired, revoked, or awaiting organization approval")
	}
	if _, err := m.guestOutput(ctx, "GH_PROMPT_DISABLED=1 gh auth setup-git --hostname github.com"); err != nil {
		return fmt.Errorf("gh could not configure the guest Git HTTPS credential helper")
	}
	branchOutput, err := m.guestOutput(ctx, "GH_PROMPT_DISABLED=1 gh repo view "+shellQuote(m.cfg.GitHubRemote.Repo)+" --json defaultBranchRef --jq .defaultBranchRef.name")
	if err != nil {
		return fmt.Errorf("the GitHub account is authenticated but cannot read %s; check repository access and organization approval", m.cfg.GitHubRemote.Repo)
	}
	baseBranch := strings.TrimSpace(branchOutput)
	if baseBranch == "" || baseBranch == "null" {
		return fmt.Errorf("GitHub repository %s has no default branch", m.cfg.GitHubRemote.Repo)
	}
	if _, err := m.guestOutput(ctx, "git check-ref-format --branch "+shellQuote(baseBranch)); err != nil {
		return fmt.Errorf("GitHub returned an invalid default branch for %s", m.cfg.GitHubRemote.Repo)
	}
	pushPermission, err := m.guestOutput(ctx, "GH_PROMPT_DISABLED=1 gh api "+shellQuote("repos/"+m.cfg.GitHubRemote.Repo)+" --jq '.permissions.push // false'")
	if err != nil {
		return fmt.Errorf("the GitHub account can read %s but its write permission could not be verified; check token scope and organization approval", m.cfg.GitHubRemote.Repo)
	}
	if strings.TrimSpace(pushPermission) != "true" {
		return fmt.Errorf("the authenticated GitHub account has no push role on %s; grant repository write access. A fine-grained token must also have Contents: read/write and Pull requests: read/write", m.cfg.GitHubRemote.Repo)
	}
	branch, err := GitHubBranchName(m.InstanceName())
	if err != nil {
		return err
	}
	script := githubSnapshotBranchScript(msbGuestWorkspace, m.cfg.GitHubRemote, baseBranch, branch)
	if err := m.guestShell(ctx, script); err != nil {
		return fmt.Errorf("préparation de la branche GitHub de %s interrompue sans publication : %w", m.cfg.GitHubRemote.Repo, err)
	}
	// GitHub initialization replaces the filtered snapshot with the remote
	// default-branch tree. Recovery starts from that actual initial tree, so
	// remote-only files are never reported as agent additions (#313).
	if err := m.recordRecoveryBase(ctx); err != nil {
		return err
	}
	fmt.Printf("GitHub guest workflow ready for %s on branch %s. Original guest history is retained on %s-snapshot-backup. Push reviewed guest commits with 'git push -u origin HEAD', then open a draft PR with 'gh pr create --draft'.\n", m.cfg.GitHubRemote.Repo, branch, branch)
	return nil
}

// guestOutput executes a command in the guest and returns stdout only. Stderr
// is deliberately omitted from errors: an external CLI can include request
// details there, and a GitHub credential must never reach host logs.
func (m *MicrosandboxRuntime) guestOutput(ctx context.Context, command string) (string, error) {
	stdout, _, code, err := m.Client.ExecCapture(ctx, m.InstanceName(), "cd "+shellQuote(msbGuestWorkspace)+" && "+command)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("guest command exited %d", code)
	}
	return stdout, nil
}
