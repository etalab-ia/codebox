# Engineering playbook

This playbook records the engineering practices expected in codebox. It applies to humans and coding agents. [AGENTS.md](../AGENTS.md) is the concise agent entry point; [CONTRIBUTING.md](../CONTRIBUTING.md) sequences the contribution workflow; [development.md](development.md) remains the detailed toolchain and operations reference.

Instructions guide judgment. Hooks, CI, rulesets, signatures, and reviewed deployment controls provide enforcement. Do not describe a prose rule as an enforced control.

## 1. Frame and design the change

Start with observable behavior, not an implementation guess:

- Define the problem, scope, non-goals, acceptance criteria, and validation path in an issue or approved task.
- Inspect the relevant source, tests, docs, branches, and PRs. Establish what already exists before proposing a new abstraction.
- Choose the simplest design that satisfies the requirement and matches existing module boundaries.
- Keep unrelated refactors, formatting, dependency updates, and cleanup out of the change.
- Present viable alternatives when a public contract, user-visible behavior, security boundary, or material cost is involved. Do not hide a design decision inside implementation.
- Name the evidence that could disprove the current diagnosis. A command that failed before reaching the behavior under test is not confirmation of that behavior.

Do not generate per-feature `spec.md`, `plan.md`, `tasks.md`, and checklist scaffolding. A clear issue, acceptance criteria, the PR, and durable decisions are sufficient. Use one maintained capability specification only when a long-lived contract genuinely requires it.

## 2. Write tests that prove behavior

### Regression tests must be able to fail

- Reproduce the defect before fixing it. Show that the new test fails on the unfixed code for the stated reason.
- After a fix, perform a focused mutation or temporarily restore the defective condition when practical. Confirm the named test detects it.
- Test the intended path, not a nearby path that happens to produce the same output.
- When replacing or deleting a test with an old flow, identify the invariant it protected and establish equivalent coverage in the replacement flow.
- Do not weaken an assertion merely because the expected behavior changed. Derive new expectations from the merged contract and update all affected user-facing strings or input sequences systematically.

### Assert outcomes, not only interactions

Mock calls prove that code took a path. They do not prove that the path achieved the intended result.

- Assert real state, stored records, emitted output, external side effects, or decoded artifact contents alongside mock expectations where appropriate.
- Pair a negative assertion with a positive assertion through the same plumbing so a broken decoder, filter, or serializer cannot make the negative case vacuous.
- For caches, streams, stores, ACLs, revocations, and publication paths, inspect the resulting state rather than only checking that a method was invoked.
- Synchronize asynchronous tests on the state being asserted. A banner, log line, or arbitrary sleep does not prove the requested operation completed.

### Make tests hermetic and runtime-faithful

- Inject network, credentials, environment, clock, terminal, process, and filesystem dependencies that would otherwise inherit host state.
- When adding an injectable dependency, find every construction site in existing tests and seal it with a fake. A green suite can otherwise hide a new real host dependency.
- Build fakes from actual production responses, including omitted fields, defaults, error behavior, and platform variants. Do not give a fake richer data than the real dependency supplies.
- Keep a narrow boundary or integration check that validates the adapter against the real contract.
- Run relevant tests with both plausible and empty environment configuration when credentials or discovery affect control flow.
- A test that passes only because the host lacks a credential, binary, service, or platform capability is not portable.

### Make timing, load, and platform claims meaningful

- Derive the achievable request rate and observation window before testing a limiter, TTL, timeout, or periodic mechanism. A test that cannot reach the limit proves nothing about it.
- Use the mechanism's actual period and state transitions rather than wall-clock thresholds alone.
- Assert the part of an error or path that codebox owns. Operating systems differ in error wording, symlink resolution, path representation, and available runtimes.
- Skip a platform-gated runtime explicitly where the platform refuses it; do not turn an unsupported platform into a false failure or a false pass.
- For portable shell code, avoid GNU-only or BSD-only behavior. Scope per-invocation environment changes to shell functions in a subshell instead of relying on `VAR=value function_name`, whose persistence differs between macOS `/bin/sh` and Linux dash.
- Process-reentry tests need an early helper dispatch before `m.Run()` and must never recursively re-execute the ordinary test path. Stub stats flushing through `StatsFlushSpawn` in tests that reach it.

### Cover every module and evidence class

`go test ./...` stops at module boundaries. The project-secret fixtures and other nested modules need their dedicated `fixture-contracts` job and documented local commands. The server, shared secrets package, installers, host integration, deployment reconciliation, and native platform jobs are separate evidence surfaces.

Ordinary hosted CI does not start a microVM on a native hypervisor. Use [qualification-beta.md](qualification-beta.md) and the affected integration README for runtime, credential-transport, workspace, and platform qualification. Record unsupported, blocked, and unrun combinations explicitly.

Honor any execution constraint attached to the checkout. CI-only validation changes where tests run, not whether they must exist and pass.

## 3. Use the repository quality controls

The current controls have distinct scopes:

| Control | What it proves | What it does not prove alone |
|---|---|---|
| `.pre-commit-config.yaml` Gitleaks hook | Staged changes do not match the configured secret detectors | Full-history cleanliness when the hook was not installed or run |
| `.github/workflows/gitleaks.yml` | Full repository history passes the reviewed CI scan | That a previously exposed credential is still safe; rotation is separate |
| `.github/workflows/ci.yml` | The executed formatting, vet, test, integration, installer, fixture, smoke, and native build jobs pass on their runners | Native hypervisor behavior or jobs that did not run |
| `.github/workflows/osv-scanner.yml` PR scan | The PR introduces no vulnerability detected by the differential scan | That the full existing dependency inventory is vulnerability-free |
| OSV scheduled/default-branch scan | The full detected inventory is reported to Code Scanning | A blocking verdict on every pre-existing finding |
| `.github/dependabot.yml` | Covered ecosystems receive grouped update proposals with the configured cooldown | That updates are applied, security fixes are never suppressed, or scanner coverage replaces review |
| Main-branch ruleset | Required signatures, required checks, non-fast-forward/deletion protection, PR workflow, and resolved review threads | Human design acceptance or merge authorization |
| `release-please-config.json` and its workflow | Conventional release metadata and coordinated artifacts are produced by the configured pipeline | That an unpublished or incomplete release is safe to consume before publication checks finish |

Activate hooks in every clone and worktree. Confirm required status checks and signatures from GitHub rather than inferring them from config files or a green local run. The active ruleset is authoritative for which checks block merging.

A conflicted or otherwise ineligible PR can have no GitHub Actions run for its head while third-party checks remain green. Inspect mergeability and actual Actions runs for the exact head, not only an aggregated check list.

## 4. Preserve security boundaries

### Secrets and credential delivery

- Keep real credentials out of source, fixtures, logs, arguments, issue bodies, screenshots, and evidence archives. Use revocable synthetic values in tests.
- Rotate exposed credentials. `.gitignore`, redaction, and a later deletion do not undo exposure.
- Preserve the distinction between host-only credentials, guest-readable project secrets, proxy placeholders, approved destinations, and runtime-specific transport.
- Before recovering or republishing a credential, validate the persisted destination policy and host allowlist. A shared environment-variable name or current provider selection does not authorize delivery to another endpoint.
- Reconcile revocations on existing instances before a fail-closed creation or launch gate can exit. A revoked binding must not remain active in a running guest merely because startup is blocked.

### Executable trust and project configuration

- Treat repository-controlled plugins, MCP commands, harness extensions, imported configuration, and resolved executable files as code-execution surfaces.
- Whenever a runtime, harness, or configuration schema adds an executable surface, extend trust discovery in the same change. Cover the executable class, including nested declarations, local and remote forms, symlinks, and imported files.
- Approval must bind the resolved executable target's current content, not only the manifest that names it. Changing the target requires a new approval.
- Prompt-only instruction files are not executable merely because an agent reads them. Keep the trust distinction explicit and test it.

### Authentication, tokens, and network policy

- Use `crypto/rand` for security-sensitive tokens and strict validation at encoding and parsing boundaries.
- Bind OAuth/OIDC state and login tickets to the attempt. Reject plaintext and cross-origin redirects unless the reviewed contract explicitly requires them.
- Bind UserInfo authorization claims to the authenticated ID-token subject by matching `sub`. Apply token-type-specific validation rules; signed UserInfo and ID tokens do not have identical expiry requirements.
- Serialize critical read-modify-write sequences when the backing store cannot provide atomic compare-and-swap. Test concurrent revocation and revision behavior.

### CI, dependencies, and deployment

- Pin actions and reusable workflows to reviewed full commit SHAs. Start with empty permissions and grant only job-scoped needs.
- Keep untrusted code out of `pull_request_target` and credentialed deployment paths. Preserve job timeouts, concurrency, and default-branch restrictions.
- Treat scanner suppressions and dependency exceptions as documented debt with a reason and revisit path.
- Dependabot `ignore` rules also suppress matching security-update PRs. Every ignored vulnerable dependency needs an alternative, documented patch path; OSV reporting alone does not deliver a fix.
- Update the Microsandbox SDK, runtime version, platform digests, tests, and qualification evidence through the coordinated process in `docs/development.md`. Do not land an isolated Dependabot bump.
- Keep deployment and paid hardware qualification explicit. A PR, push, or cron must not silently acquire production credentials or start billable infrastructure.

### Release publication

A CLI release remains draft until every required binary and `SHA256SUMS` is uploaded successfully. Publication is serialized, and only the CLI release claims GitHub's `latest` release marker. A `codebox-server` release must not make installers resolve to the wrong component. Preserve and test these invariants when changing release automation.

## 5. Review the exact current head

For every PR or stack layer:

1. Re-read the remote head, base, mergeability, issue comments, review summaries, inline threads, and actual check runs.
2. Obtain the repository's Codex review or the maintainer-designated substitute for the current head. A running review, an old verdict, or a receipt without a completed result is not clearance.
3. Reproduce findings against source or a discriminating test. Fix valid defects; answer invalid findings with counter-evidence.
4. Escalate findings that would change a public contract, add an abstraction, alter intended user behavior, or contradict an explicit product decision. Do not comply mechanically for a clean verdict.
5. Record the reviewed SHA, validation, remaining risk, and any deferred acceptance evidence.
6. Re-review substantial subsequent changes. A clean verdict on an earlier SHA does not cover a new head.

Do not equate green CI with satisfied security, native qualification, visual acceptance, or product acceptance. Do not merge or close automatically.

## 6. Stacked pull requests with `gh stack`

### Choose the right topology

Use a native stack for dependent, independently reviewable concerns such as contract, service, CLI integration, and deployment. Put foundational work at the bottom and keep that layer small. A multi-PR feature normally has one implementer acting as the stack's mutation owner; other contributors participate through review or explicit layer handoff.

Group by dependency, not provenance. Independent QA findings and other independent work use separate issues and ordinary PRs against the trunk, even when one session discovered them. Do not create a stack of one, stack unrelated work, or represent links between repositories as a native stack. Native stacks require at least two PRs and branches in the same repository; cross-fork stacks are unsupported.

Every layer is evaluated against the stack base for required reviews, checks, CODEOWNERS, and trunk workflows. A red or unreviewed lower layer blocks every layer above it. Merges are atomic and bottom-up, which is why merge authorization remains explicit.

### Install the CLI and agent skill

```bash
gh extension install github/gh-stack
gh skill install github/gh-stack
# Alternative skill installer:
npx skills add github/gh-stack
```

The public upstream skill lives at `github/gh-stack/skills/gh-stack`. Use `gh stack <command> --help` against the installed extension for exact flags. Agents without skill loading follow this section directly.

For non-interactive agent runs, use explicit subcommands and flags rather than allowing a TUI or prompt to block. When multiple remotes exist, configure `remote.pushDefault` or pass `--remote origin` to commands that accept it.

### Stack ownership and concurrency

A stack is a review dependency, not a shared integration branch. `gh stack` manages topology and local operation locks; it does not coordinate another contributor's clone or provide a distributed editing lock.

1. **Name one mutation owner.** Record that person or agent in the issue or top PR. Only the mutation owner runs stack-wide `submit`, `sync`, `rebase`, and pushes. Prefer one implementer for all layers when practical.
2. **Claim the layer before editing.** Other contributors review or accept an explicit layer handoff. The claim identifies the issue, PR, branch, intended predecessor, and current owner. Independent work branches from the trunk instead of joining the current stack.
3. **Verify the workspace and exact head.** Use an isolated worktree. Check the branch, clean status, local HEAD, GitHub PR head, and base before editing. `gh stack view --json` is a catalog, not the authority. Stop when GitHub contains commits the local checkout does not expect.
4. **Serialize stack mutations.** Upper-layer editing pauses during a lower-layer fix, rebase, or synchronization. The mutation owner fetches and inspects every layer first, fixes the owning layer, propagates with the extension, and pushes through its protected workflow. Never concurrently rebase a stack or use a manual force push.
5. **Resynchronize before resuming.** After propagation, every contributor fetches and verifies the new head and base of their layer before making another edit. Refresh affected CI and review evidence.
6. **Keep stacks short-lived.** Obtain a clean review before starting the next layer. When the lower-layer contract is still unstable, wait for that layer to merge instead. Split independent concerns back into ordinary PRs.

Fork-based contributors use ordinary PRs because native cross-fork stacks are unsupported. Do not adopt an external branch into a stack or rewrite a contributor's branch without an explicit coordinated handoff.

### Plan and publish bottom-up

```text
(main) <- topic/contract <- topic/service <- topic/cli
```

Create the topology before implementing the layers:

```bash
gh stack init topic/contract
# Edit, test, stage explicit paths, and commit the contract layer.
gh stack add topic/service
# Edit, test, stage explicit paths, and commit the service layer.
gh stack submit --auto --open
gh stack view --json
```

`submit --auto` avoids the interactive title editor and creates drafts unless `--open` is supplied. `--open` marks new and existing PRs ready for review. Drafts remain appropriate for incomplete work needing early CI or review.

Verify heads and bases with Git and the GitHub PR API. `gh stack view --json` is a local catalog view and can lag a PR created outside the CLI. Do not create a duplicate PR because the catalog has not observed an existing one. Existing branches or PRs can be adopted bottom-up with `gh stack link`.

### Review and repair by owning layer

- Obtain a clean review on the current layer before starting the next layer.
- Give each layer its own exact-head review, CI evidence, and acceptance record. One verdict does not cover the stack.
- Describe only the layer's delta in its PR body and identify what must merge first.
- Check out the layer that owns a finding. Never commit a lower-layer fix on the top branch.
- After fixing a layer, pause other edits while the mutation owner propagates the change upward:

```bash
gh stack rebase --upstack
gh stack push
```

Re-run affected checks and inspect each new head. A red foundational layer blocks all upper layers regardless of their isolated appearance.

### Synchronize and hand off

The mutation owner uses `gh stack sync` to reconcile with GitHub, rebase the chain, push, and refresh PR state. Other contributors pause stack edits and fetch and verify their new layer heads afterward. Rebase instead of merging the trunk into a layer; fully linear history between branches is a stack requirement.

A sync can report divergence without changing anything. Read the state instead of retrying blindly. After a conflict, follow the extension's recorded recovery path and preserve concurrent work.

Hand off layers in bottom-to-top merge order with PR link, exact head, CI state, review verdict, issue linkage, and remaining acceptance evidence. `gh stack merge` is an atomic merge operation, so only an authorized maintainer decides when to use it. Do not close superseded or partially merged layers automatically.

## 7. Keep documentation useful

- Update documentation when code changes invalidate it. Link canonical sources instead of copying command matrices into several files.
- Keep validation command lists in `docs/development.md` and detailed stack procedures in this playbook. `AGENTS.md` carries only always-needed invariants and links; `CONTRIBUTING.md` carries the contribution workflow.
- Use ADRs for durable architecture and trust decisions, not for transient implementation logs.
- Convert incident lessons into a reusable invariant plus a verification method. Keep the incident narrative in its original issue or PR.
- Review changes to agent instructions like code. Confirm that Claude Code loads `AGENTS.md` through `CLAUDE.md` and that other agents discover the root file.
- Keep `AGENTS.md` short enough to remain effective. Move detail here or into path-specific documentation and prune stale or contradictory rules.

## Final change checklist

Before handing off a PR, confirm:

- The issue and acceptance criteria match the delivered scope.
- The diff contains only the intended concern and no secrets or build artifacts.
- New regression tests can fail for the intended reason and cover the affected modules.
- Hooks and relevant local checks actually ran, or their absence is explicit.
- Required CI, security scans, signatures, and review apply to the exact current head.
- Native, visual, or human acceptance evidence is distinguished from ordinary CI.
- The PR states residual risk, unrun checks, and remaining work honestly.
- Stack layers, when present, have clean lower layers and correct ownership.
- Merge and closure remain explicit maintainer decisions.
