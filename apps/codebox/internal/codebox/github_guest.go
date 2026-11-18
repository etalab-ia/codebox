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
		return fmt.Errorf("le flux de travail GitHub approuvé nécessite une origine GitHub.com prise en charge ; aucune modification du runtime n'a été effectuée")
	}
	if applied != nil && applied.GitHubOrigin != "" && applied.GitHubOrigin != remote.URL {
		return fmt.Errorf("l'origine approuvée du projet diffère de l'origine GitHub de la machine isolée ; examinez et exportez les modifications de la machine isolée avant de la recréer. Aucune modification du runtime n'a été effectuée")
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
		return fmt.Errorf("impossible de lire le marqueur du flux de travail GitHub de la machine isolée : %w", err)
	}
	if code == 1 || (code == 128 && strings.Contains(stderr, "can only be used inside a git repository")) {
		// No repository yet, or no workflow marker: normal provisioning cases.
		// Other failures cannot establish that the marker is absent.
		return nil
	}
	if code != 0 || strings.TrimSpace(marker) == "" {
		return fmt.Errorf("impossible de lire le marqueur du flux de travail GitHub de la machine isolée (code %d) : %s", code, strings.TrimSpace(stderr))
	}
	if strings.TrimSpace(marker) != m.cfg.GitHubRemote.URL {
		return fmt.Errorf("le flux de travail GitHub de la machine isolée est initialisé pour une autre origine ; exportez les modifications de la machine isolée et examinez le changement de dépôt distant avant de recréer la machine isolée")
	}
	origin, err := m.guestOutput(ctx, "git remote get-url origin")
	if err != nil {
		return fmt.Errorf("le marqueur du flux de travail GitHub de la machine isolée existe mais son origine ne peut pas être lue : %w", err)
	}
	parsed, err := ParseGitHubRemote(strings.TrimSpace(origin))
	if err != nil || parsed.URL != m.cfg.GitHubRemote.URL {
		return fmt.Errorf("l'origine GitHub de la machine isolée diffère de l'origine approuvée du projet sur le poste ; refus de la réécrire")
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
		return fmt.Errorf("l'association GitHub est approuvée mais le projet n'a pas d'origine github.com prise en charge ; configurez l'origine ou révoquez l'association GitHub")
	}
	if err := m.guestShell(ctx, githubCLIInstallScript()); err != nil {
		return fmt.Errorf("l'installation du CLI GitHub épinglé dans la machine isolée a échoué : %w", err)
	}

	marker, markerErr := m.guestOutput(ctx, "git config --local --get codebox.github-workflow-origin")
	if markerErr == nil {
		if strings.TrimSpace(marker) != m.cfg.GitHubRemote.URL {
			return fmt.Errorf("le flux de travail GitHub de la machine isolée est initialisé pour une autre origine ; exportez les modifications de la machine isolée et examinez le changement de dépôt distant avant de recréer la machine isolée")
		}
		origin, err := m.guestOutput(ctx, "git remote get-url origin")
		if err != nil {
			return fmt.Errorf("le marqueur du flux de travail GitHub de la machine isolée existe mais son origine ne peut pas être lue : %w", err)
		}
		parsed, err := ParseGitHubRemote(strings.TrimSpace(origin))
		if err != nil || parsed.URL != m.cfg.GitHubRemote.URL {
			return fmt.Errorf("l'origine GitHub de la machine isolée diffère de l'origine approuvée du projet sur le poste ; refus de la réécrire")
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
			return fmt.Errorf("la machine isolée a déjà un dépôt distant d'origine différent ; examinez-le avant d'activer le flux de travail GitHub")
		}
		return fmt.Errorf("la machine isolée a un dépôt distant d'origine mais aucun marqueur de flux de travail GitHub codebox ; refus de réinitialiser son historique de branches")
	}
	remotes, err := m.guestOutput(ctx, "git remote")
	if err != nil {
		return fmt.Errorf("impossible d'inspecter les dépôts distants Git de la machine isolée : %w", err)
	}
	if strings.TrimSpace(remotes) != "" {
		return fmt.Errorf("la machine isolée a un dépôt distant sans origine ; examinez sa configuration Git avant d'activer le flux de travail GitHub")
	}

	// Validate the stored token and distinct permission layers before changing
	// the guest repository. gh honors the proxy-provided GITHUB_TOKEN value.
	if _, err := m.guestOutput(ctx, "GH_PROMPT_DISABLED=1 gh auth status --hostname github.com"); err != nil {
		return fmt.Errorf("l'identifiant GitHub n'a pas été accepté par github.com ; il est peut-être expiré, révoqué ou en attente d'approbation d'organisation")
	}
	if _, err := m.guestOutput(ctx, "GH_PROMPT_DISABLED=1 gh auth setup-git --hostname github.com"); err != nil {
		return fmt.Errorf("gh n'a pas pu configurer l'assistant d'identifiants Git HTTPS de la machine isolée")
	}
	branchOutput, err := m.guestOutput(ctx, "GH_PROMPT_DISABLED=1 gh repo view "+shellQuote(m.cfg.GitHubRemote.Repo)+" --json defaultBranchRef --jq .defaultBranchRef.name")
	if err != nil {
		return fmt.Errorf("le compte GitHub est authentifié mais ne peut pas lire %s ; vérifiez l'accès au dépôt et l'approbation de l'organisation", m.cfg.GitHubRemote.Repo)
	}
	baseBranch := strings.TrimSpace(branchOutput)
	if baseBranch == "" || baseBranch == "null" {
		return fmt.Errorf("le dépôt GitHub %s n'a pas de branche par défaut", m.cfg.GitHubRemote.Repo)
	}
	if _, err := m.guestOutput(ctx, "git check-ref-format --branch "+shellQuote(baseBranch)); err != nil {
		return fmt.Errorf("GitHub a renvoyé une branche par défaut invalide pour %s", m.cfg.GitHubRemote.Repo)
	}
	pushPermission, err := m.guestOutput(ctx, "GH_PROMPT_DISABLED=1 gh api "+shellQuote("repos/"+m.cfg.GitHubRemote.Repo)+" --jq '.permissions.push // false'")
	if err != nil {
		return fmt.Errorf("le compte GitHub peut lire %s mais son droit d'écriture n'a pas pu être vérifié ; vérifiez la portée du jeton et l'approbation de l'organisation", m.cfg.GitHubRemote.Repo)
	}
	if strings.TrimSpace(pushPermission) != "true" {
		return fmt.Errorf("le compte GitHub authentifié n'a pas de rôle de poussée sur %s ; accordez un accès en écriture au dépôt. Un jeton à granularité fine doit aussi avoir Contents: read/write et Pull requests: read/write", m.cfg.GitHubRemote.Repo)
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
	fmt.Printf("Flux de travail GitHub de la machine isolée prêt pour %s sur la branche %s. L'historique d'origine de la machine isolée est conservé sur %s-snapshot-backup. Poussez les commits relus de la machine isolée avec 'git push -u origin HEAD', puis ouvrez une PR en brouillon avec 'gh pr create --draft'.\n", m.cfg.GitHubRemote.Repo, branch, branch)
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
		return "", fmt.Errorf("la commande dans la machine isolée s'est terminée avec le code %d", code)
	}
	return stdout, nil
}
