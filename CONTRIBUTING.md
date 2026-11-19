# Contributing to codebox

This guide defines the shared workflow for human contributors and coding agents. The deeper engineering practices live in [docs/engineering-playbook.md](docs/engineering-playbook.md); toolchain and architecture details live in [docs/development.md](docs/development.md).

## 1. Frame the change

Before implementation:

- Find or create a GitHub issue describing the problem, scope, non-goals, acceptance criteria, and relevant execution constraints.
- Assign yourself to the issue before starting and unassign yourself if you stop. If you cannot self-assign, ask a maintainer to assign it; do not leave an abandoned claim in place.
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

Run the checks that cover the changed surface. The canonical Go module commands are in [docs/development.md](docs/development.md); do not copy them into additional guides.

The full CI surface also includes installer suites, host integration, separate fixture modules, server smoke tests, deployment reconciliation, and native platform builds. Consult `.github/workflows/ci.yml` and `docs/development.md` rather than assuming one command covers everything.

The repository controls are complementary:

| Control | Local role | CI role |
|---|---|---|
| Gitleaks | Scan staged changes at pre-commit | Scan full repository history on PRs and `main` |
| Go formatting, vet, and tests | Fast feedback on affected modules | Repeat checks and exercise the complete workflow graph |
| OSV-Scanner | Optional local triage | Block newly introduced PR vulnerabilities; report the full scheduled/default-branch inventory |
| Dependabot | Not applicable | Propose grouped updates with a seven-day cooldown, subject to documented exceptions |
| Native qualification | Run only on a suitably provisioned host | Ordinary hosted CI does not prove microVM startup |

### Documentation and QA-only changes

For a PR that changes only documentation, comments, or QA notes:

1. Install the repository hooks, review changed links and command references, and run `git diff --check`.
2. Redact QA evidence and stage explicit paths. Run `git diff --cached --check` and `pre-commit run gitleaks`.
3. Local Go builds and tests are not required when no executable, test, or workflow configuration changed. The required PR checks still apply: Gitleaks full history, the OSV PR scan, `check`, `integration-host`, and the applicable native-platform checks. The active `main` ruleset is authoritative.
4. Report local validation and CI separately. For changes to agent instructions, include the exact prompt diff in the PR body.

Distinguish in the PR between local checks actually run, CI checks still pending, and native or human acceptance evidence. Never claim an unrun, skipped, stale, or environment-blocked check as passed.

## 5. Commit and push

- Use Conventional Commits: `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `ci:`, `build:`, or `chore:`.
- Configure the required repository-local signing identity. Every commit in the PR must be signed and GitHub-verified, not only the head commit.
- Stage explicit paths. Review `git status` and the staged diff for secrets, generated artifacts, and unrelated edits.
- Keep WIP checkpoints off release-please PR titles when the repository workflow requires final conventional subjects.

### Configure commit signing

Every commit in the PR must have a signature GitHub verifies against the committer identity. Use your own identity and signing key, with an email address verified on your GitHub account. Never share a private key.

SSH signing requires Git 2.34 or later. If needed, create an Ed25519 key and add the **public** key to GitHub as a signing key:

```bash
ssh-keygen -t ed25519 -C "your-verified-email@example.com"
gh ssh-key add "$HOME/.ssh/id_ed25519.pub" --type signing
```

If the key is already registered for authentication, add it again with the `signing` type. Then configure signing locally for this repository:

```bash
git config --local user.name "Your Name"
git config --local user.email "your-verified-email@example.com"
git config --local gpg.format ssh
git config --local user.signingkey "$HOME/.ssh/id_ed25519.pub"
git config --local commit.gpgsign true
```

See GitHub's [SSH key configuration](https://docs.github.com/en/authentication/managing-commit-signature-verification/telling-git-about-your-signing-key#telling-git-about-your-ssh-key), [key registration](https://docs.github.com/en/authentication/connecting-to-github-with-ssh/adding-a-new-ssh-key-to-your-github-account), and [commit signing](https://docs.github.com/en/authentication/managing-commit-signature-verification/signing-commits) guides. GPG and S/MIME are supported alternatives. After pushing, open the PR's Commits tab and confirm that **every** commit shows `Verified`.

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

Use GitHub native stacked PRs and the `gh stack` CLI when a change splits into genuinely dependent layers. Group by dependency, not provenance. Independent QA findings and other independent work get separate issues and ordinary PRs from `main`, even when one session discovered them.

Install the CLI extension and, for agents that support skills, the public upstream skill:

```bash
gh extension install github/gh-stack
gh skill install github/gh-stack
# Alternative skill installer:
npx skills add github/gh-stack
```

Follow the [stacked-PR section](docs/engineering-playbook.md#6-stacked-pull-requests-with-gh-stack) for planning, per-layer review, ownership, synchronization, and handoff.

### Stack ownership and concurrency

- Give each stack one named mutation owner. Other contributors participate through review or explicit layer handoff.
- Work in an isolated worktree and compare the local branch, HEAD, and intended base with GitHub before editing. Stop on unexpected commits.
- Pause upper-layer edits during stack mutation. The mutation owner fixes the owning layer and propagates with `gh stack rebase --upstack`; everyone fetches the new heads before resuming.
- Native stacks are same-repository only. Fork contributors use ordinary PRs. Wait for a clean review before starting the next layer, and for the lower layer's merge when its contract remains unstable.

The full conflict, synchronization, and recovery procedure lives in the engineering playbook rather than being duplicated here.

## 9. Maintain the playbook

Changes to `AGENTS.md`, `CLAUDE.md`, this guide, or the engineering playbook affect both humans and agents and require the same review discipline as code.

Add a rule when repeated evidence shows a reusable failure or expectation. Write the invariant and its verification, link detailed evidence when useful, and remove obsolete or contradictory guidance. Do not turn these files into an append-only incident log.
