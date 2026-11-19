# Contributing to codebox

This guide defines the shared workflow for human contributors and coding agents. The deeper engineering practices live in [docs/engineering-playbook.md](docs/engineering-playbook.md); toolchain and architecture details live in [docs/development.md](docs/development.md).

## 1. Frame the change

Before implementation:

- Find or create a GitHub issue describing the problem, scope, non-goals, acceptance criteria, and relevant execution constraints.
- Search existing issues, branches, PRs, and review activity. Resume or adopt existing work instead of creating a duplicate.
- Identify the evidence each acceptance criterion needs: a test, CI job, screenshot, native qualification, or human decision.
- Stop and clarify unresolved product choices, unsafe validation paths, or ownership conflicts.

GitHub issues, PR descriptions, and review comments are written in English unless the maintainers explicitly choose otherwise.

## 2. Create an isolated workspace

Never commit directly on `main`. Create a feature branch or isolated worktree and check the repository state before editing.

Install the hooks in every fresh clone or worktree:

```bash
pre-commit install --install-hooks
```

Do not bypass hooks without an explicit maintainer-approved exception. If a prerequisite is missing, report it rather than silently skipping the control.

## 3. Implement narrowly

- Read the surrounding code and tests first. Follow established module boundaries and local patterns.
- Make the smallest cohesive change that satisfies the acceptance criteria.
- Preserve concurrent and user-authored work. Do not revert unrelated changes.
- Add regression and edge-case coverage proportionate to the blast radius.
- Update user or developer documentation when behavior, commands, configuration, or operational requirements change.
- Use an ADR under `docs/decisions/` only for a durable architectural or trust-boundary decision. Do not generate a document tree for every feature.

For credentials, workspace transfer, ACLs, authentication, redirects, CI permissions, or deployment changes, read the security section of the engineering playbook and the relevant design documents before editing.

## 4. Validate honestly

Run the checks that cover the changed surface. The common Go checks are:

```bash
moon run :fmt-check :vet
go -C apps/codebox test -race ./...
GOWORK=off go -C packages/secrets test -race ./...
GOWORK=off go -C apps/codebox-server test -race ./...
```

The full CI surface also includes installer suites, host integration, separate fixture modules, server smoke tests, deployment reconciliation, and native platform builds. Consult `.github/workflows/ci.yml` and `docs/development.md` rather than assuming one command covers everything.

The repository controls are complementary:

| Control | Local role | CI role |
|---|---|---|
| Gitleaks | Scan staged changes at pre-commit | Scan full repository history on PRs and `main` |
| Go formatting, vet, and tests | Fast feedback on affected modules | Repeat checks and exercise the complete workflow graph |
| OSV-Scanner | Optional local triage | Block newly introduced PR vulnerabilities; report the full scheduled/default-branch inventory |
| Dependabot | Not applicable | Propose grouped updates with a seven-day cooldown, subject to documented exceptions |
| Native qualification | Run only on a suitably provisioned host | Ordinary hosted CI does not prove microVM startup |

Distinguish in the PR between local checks actually run, CI checks still pending, and native or human acceptance evidence. Never claim an unrun, skipped, stale, or environment-blocked check as passed.

## 5. Commit and push

- Use Conventional Commits: `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `ci:`, `build:`, or `chore:`.
- Configure the required repository-local signing identity. Every commit in the PR must be signed and GitHub-verified, not only the head commit.
- Stage explicit paths. Review `git status` and the staged diff for secrets, generated artifacts, and unrelated edits.
- Keep WIP checkpoints off release-please PR titles when the repository workflow requires final conventional subjects.

Committing and pushing a feature branch does not authorize merging it.

## 6. Open the pull request

Open a ready PR when all of these hold:

1. The issue, scope, acceptance criteria, and target branch are clear.
2. Implementation and documentation are complete for the stated scope.
3. Applicable local checks ran successfully, or an incomplete PR is explicitly marked draft.
4. Tests are meaningful, hermetic where required, and cover the changed behavior.
5. The full diff has been self-reviewed and contains no secrets or unrelated artifacts.
6. The PR body lists actual validation, residual risks, and remaining acceptance evidence.

Use `Closes #N` only when the PR fully satisfies the issue acceptance criteria. Otherwise use `Refs #N` and name the remaining work. Put the English closing keyword in the PR body, not only in a commit message.

For a substantial PR body, lead with two to five sentences explaining the purpose, previous problem, and visible effect. Put exhaustive agent-generated detail in one `<details><summary>...</summary></details>` block. Keep `Closes #N` or `Refs #N`, risks, and failing evidence visible.

## 7. Drive review and CI

- Inspect issue comments, review summaries, inline threads, and checks after every new head.
- Obtain the required automated review for the exact current head. A running review, an old approval, or green CI alone is not clearance.
- Reproduce each finding against source or a discriminating test. Fix valid defects and answer invalid findings with counter-evidence.
- Bring design choices and public-contract tradeoffs to a maintainer instead of changing them merely to satisfy an automated reviewer.
- Read failing logs before editing. Classify infrastructure, base, test, platform, and implementation failures accurately.
- Treat pending or unavailable status as unknown. Re-review substantial new heads and re-run the relevant checks.
- Do not manufacture green by weakening assertions, deleting tests, bypassing hooks, or hiding unrelated failures.

Agents must not merge or close PRs without explicit maintainer instruction. A green, reviewed PR is a handoff, not permission to merge.

## 8. Use stacked PRs for dependent work

Use GitHub native stacked PRs and the `gh stack` CLI when a change splits into genuinely dependent layers. Do not stack unrelated work merely because it was produced in one session.

Install the CLI extension and, for agents that support skills, the public upstream skill:

```bash
gh extension install github/gh-stack
gh skill install github/gh-stack
# Alternative skill installer:
npx skills add github/gh-stack
```

Follow the [stacked-PR section](docs/engineering-playbook.md#6-stacked-pull-requests-with-gh-stack) for planning, per-layer review, ownership, synchronization, and handoff. Key constraints:

- Native stacks are same-repository only; cross-fork stacks are unsupported.
- Every layer is judged against the stack base. A red or unreviewed lower layer blocks the layers above it.
- Wait for a clean review on the current layer before starting the next.
- Fix a concern in its owning layer, then propagate with `gh stack rebase --upstack`.
- Each PR body describes its own delta and dependency position.
- Stack merges are explicit maintainer actions.

## 9. Maintain the playbook

Changes to `AGENTS.md`, `CLAUDE.md`, this guide, or the engineering playbook affect both humans and agents and require the same review discipline as code.

Add a rule when repeated evidence shows a reusable failure or expectation. Write the invariant and its verification, link detailed evidence when useful, and remove obsolete or contradictory guidance. Do not turn these files into an append-only incident log.
