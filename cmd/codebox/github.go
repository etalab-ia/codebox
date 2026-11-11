package main

import (
	"context"

	"github.com/etalab-ia/codebox/internal/codebox"
)

// projectGitHubRemote returns no remote unless the project has the host-local
// P09 GitHub approval. A public origin by itself must not activate the token.
func projectGitHubRemote(projectRoot, stateDir, instance string) (codebox.GitHubRemote, error) {
	approvals, err := codebox.ReadBindingApprovals(
		codebox.DefaultFS,
		codebox.BindingApprovalsPath(stateDir, instance),
	)
	if err != nil || !approvals.Approves(codebox.CredentialGithub) {
		return codebox.GitHubRemote{}, nil
	}
	return codebox.GitHubOriginForProject(context.Background(), projectRoot)
}
