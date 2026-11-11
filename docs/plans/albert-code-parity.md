# codebox: plan d'implémentation PR par PR (parité Albert Code)

Brouillon de revue | 22 septembre 2026

## Résumé

Atteindre la parité Albert Code via **22 PR revoyables**, organisées en preuves de compatibilité, fondations, un jalon de lancement par défaut utilisable, puis la configuration projet complète. Conserver Go et les adaptateurs existants. Ne pas construire un nouveau cadre d'orchestration.

Ce document transforme l'[analyse architecturale acceptée](https://gist.github.com/kaaloo/daad9511fdd645712d617e8632e9aebc) en backlog de livraison proposé. Les identifiants **P01-P22 sont des identifiants de planification, pas des numéros de PR GitHub existants**. Aucune PR d'implémentation ni nouvelle issue n'a été ouverte pour ce plan.

Référentiel source revérifié le 22 septembre : codebox main `6d231654633aa409ca373f89b1073f68e92ca619` (v0.4.2), Go 1.22, SDK Microsandbox 0.7.2. Main est inchangé depuis l'analyse. L'[issue #29](https://github.com/etalab-ia/codebox/issues/29) est la seule issue ouverte ; il n'y a pas de PR ouverte. La comparaison Albert Code reste fondée sur `46569c7`.

**Approbation demandée :** le périmètre, la séquence, la politique de compatibilité et les critères d'acceptation ci-dessous. L'approbation de ce document n'est pas une permission d'affaiblir la protection des identifiants si une expérience technique échoue.

### Révision du 22 septembre 2026 : modèle d'espace de travail scellé

Le plan initial conservait le montage inscriptible du checkout hôte par défaut, avec le clone invité comme alternative rejetée. Cette décision est **inversée** : codebox adopte le **modèle scellé** — le checkout hôte n'est plus monté dans l'invité ; l'invité possède son propre clone et les changements reviennent par branche/PR ou par diff revu. Le montage partagé n'est **pas** conservé comme option d'échappement.

Motif : un scan au démarrage ne peut pas fermer la fuite d'identifiants dans un montage inscriptible. `ScanWorkspace` refuse déjà les fichiers `.env` et les détections gitleaks, mais tout secret ajouté après le démarrage, dans un format non détecté, ou écrit par l'agent lui-même reste lisible. Une surveillance continue réduirait la fenêtre sans jamais la fermer. La seule frontière réelle est l'absence de montage. Le scan ne disparaît pas pour autant : il est déplacé à la frontière de transfert hôte→invité, où il devient un filtre refusant par défaut (voir P22).

Conséquences acceptées, détaillées dans P22 : l'éditeur live de l'hôte disparaît du parcours par défaut, un chemin de retour des changements devient obligatoire, et l'estimation est réévaluée à la hausse.

## 1. Décisions de travail

Luis a accepté l'analyse et ses recommandations. Utiliser ce qui suit comme hypothèses de planification plutôt que de les rouvrir dans chaque PR :

| Topic | Implementation baseline |
|---|---|
| Normal launch | `codebox` uses Microsandbox and full isolation without flags or environment variables. Explicit supported overrides remain. |
| Workspace boundary | Sealed model: the host checkout is not mounted into the guest. The guest owns its clone and returns changes through a branch/PR or a reviewed diff. No writable host-workspace mount, and no shared-mount escape hatch. No home, host Docker socket, SSH agent or browser-profile mount either. |
| Project configuration | Secret-free manifest and lockfile intended for version control; explicit local-only storage option. Setup writes files but never stages or commits them. |
| Credentials | Native credential store when usable; an explicitly accepted protected-file fallback for headless systems. No silent storage downgrade. |
| GitHub | Provision `gh` in the guest when GitHub is enabled. Credential access is separately approved per project. No host GitHub CLI requirement for normal use. |
| Instances | Distinct persistent project/worktree environments; one active managed environment initially. No automatic destruction when switching projects. |
| Browser guest | Select Alpine or a Debian-based guest from measured compatibility, not assumption. |
| Updates | Explicit preview/apply; no unpinned package or skill refresh on ordinary launch. |
| Runtime coverage | Microsandbox gets the protected default path. Tart and agent-vm remain explicit alternatives with honest capability and credential-exposure reporting. |

Three clarifications sharpen the previous discussion:

- **Tool installation is not authorization.** Absence of `gh` does not block HTTP calls, public GitHub access, or installation of another client. The managed grant controls whether codebox supplies credentials. Network policy and PAT permissions determine what authorized requests can do.
- **A native keyring is not a universal same-user process barrier.** Encryption, unlock behavior and application access controls vary. Probe availability and lock state; do not assume every desktop has a usable Secret Service or that only codebox can retrieve an item. Audit the runtime's own persistence too.
- **A lockfile improves reproducibility but does not make environments identical.** Host architecture, remote APIs/models and mutable OS package repositories remain variables. Record exact artifacts and verify integrity where supported; distinguish pinned from externally controlled components.

## 2. Règles de livraison et de revue

1. Extend issue #29 with the approved configuration contract, then create the grouped tracking issues in section 6 before implementation. Do not file 21 empty issues merely to mirror the PR count.
2. Each PR includes its own tests, documentation and upgrade notes. P21 is final integration qualification, not the first time features get tested.
3. Keep intermediate main usable. Foundations remain internal until their command path is complete; no placeholder commands or half-working wizard options. Incomplete credential transports are unavailable, never silently substituted with raw guest tokens.
4. Merge independent PRs to main where practical. Dependent PRs name their prerequisites and rebase after those merge. Avoid maintaining a 21-branch stack.
5. Do not merge a release-please release PR while a public behavior change is incomplete. Tag only at qualified milestones. P12 is the first possible new-default release; P21 qualifies complete parity.
6. Every PR must pass the repository's existing checks: formatting, `go vet ./...`, `go test -race ./...` on the current check runner, plus native test/build jobs. These commands are verified in `.github/workflows/ci.yml`. SDK CGO/FFI support means cross-compilation is not a substitute for native builds.
7. Preserve gitleaks and dependency review. The Node-only `cvs-lite-cli` hook is not an applicable Go validation gate. New Go/TUI/keyring dependencies need a license, maintenance, platform and Go-version check before adoption.
8. Real integration results state OS, architecture, CLI/SDK/OpenCode versions and what actually ran. A missing virtualization runner is a blocked test, not a pass. No production credentials or sensitive raw traces in test artifacts.

## 3. Séquence d'ensemble

Toutes les dépendances ci-dessous sont **directes** ; leurs dépendances transitives s'appliquent automatiquement. La numérotation donne un ordre de lecture commode, pas une obligation d'exécuter séquentiellement des travaux indépendants.

| PR | Proposed French PR title | Depends on | Result |
|---|---|---|---|
| P01 | `test: caractériser la configuration effective d’OpenCode` | None | Proven merge/discovery contract |
| P02 | `test: qualifier le transport et le stockage des secrets Microsandbox` | None | Credential transport decision |
| P03 | `test: qualifier les capacités invitées et le terminal interactif` | None | Guest/browser/PTY evidence |
| P04 | `feat(config): séparer les réglages globaux et projet` | None | Typed resolution, schemas, provenance |
| P05 | `refactor(msb): identifier les environnements par projet` | P04 | Named Microsandbox instances |
| P06 | `refactor(runtime): cibler les opérations sur une instance` | P05 | Coherent lifecycle across adapters |
| P07 | `feat(runtime): planifier et réconcilier les changements` | P06 | Safe, resumable application of changes |
| P22 | `feat(workspace): clone invité et retour des changements revus` | P03, P05, P07 | Sealed guest workspace, no host mount |
| P08 | `feat(auth): stocker les identifiants hors des projets` | P02, P04 | Native stores + explicit fallback |
| P09 | `feat(msb): gérer les liaisons de secrets et leur révocation` | P07, P08 | Destination-bound credential lifecycle |
| P10 | `feat(config): composer et approuver la configuration OpenCode` | P01, P07, P09 | Effective config, trust, provider/model handling |
| P11 | `feat(setup): accompagner la configuration globale` | P03, P08, P10 | Usable global wizard |
| P12 | `feat: lancer Microsandbox en isolation complète par défaut` | P06, P10, P11, P22 | Minimal zero-flag vertical slice |
| P13 | `feat(github): provisionner gh et activer les accès approuvés` | P09, P12 | Working guest GitHub workflow + PR-based delivery |
| P14 | `feat(projet): gérer les skills et les instructions versionnées` | P10, P12 | Pinned skills and managed rules |
| P15 | `feat(mcp): configurer data.gouv et Context7` | P09, P10, P12 | Remote MCP setup |
| P16 | `feat(invité): provisionner les MCP navigateur` | P03, P07, P10, P12 | Working Playwright and DevTools |
| P17 | `feat(init): unifier l’assistant de configuration projet` | P13, P14, P15, P16 | Complete project wizard |
| P18 | `feat(update): mettre à jour les dépendances sur validation` | P17 | Reviewable project updates |
| P19 | `feat(migration): importer les projets Albert Code` | P17 | Explicit, non-destructive importer |
| P20 | `feat(install): accompagner l’installation sur chaque plateforme` | P12 | Verified installation wrappers |
| P21 | `test: qualifier la parité et les parcours de migration` | P18, P19, P20 | Release evidence and support matrix |

P22 was added in the 22 September revision (sealed workspace); its identifier preserves the original P01-P21 reading order, and the table rows are in planned dependency order.

**Parallel opportunities:** P01-P04; P05-P07 alongside P08; P13-P16 after P12; P18/P19/P20 once their prerequisites are available. Do not parallelize edits to `main.go` without coordinating ownership.

### Jalons

- **M0, evidence:** P01-P03. Close the consequential unknowns before promising GitHub protection or browser support. P04 can progress independently.
- **M1, usable default:** P04-P12 and P22. Fresh install -> global setup -> minimal project init -> sealed guest clone -> guest TUI -> retained state. This is not yet advertised as Albert Code parity.
- **M2, project parity:** P13-P17. GitHub (including PR-based change delivery), selected skills, managed rules and all four curated MCPs work through a coherent wizard.
- **M3, qualified release:** P18-P21. Explicit updates, import, installation and real-host qualification are complete.

## 4. Contrats détaillés des PR

### P01. Characterize OpenCode configuration and discovery

**Purpose:** settle how generated provider/MCP configuration coexists with repository JSON/JSONC, global settings, skills and AGENTS instructions before writing the composer.

**Scope:** introduce a pinned OpenCode integration fixture and a concise decision record. Test project config, explicit config location and inline configuration precedence; determine how a guest-local overlay can coexist with project-relative paths. Verify the actual supported locations for skills and instruction discovery. Choose a maintained parser only if an import path needs JSONC parsing.

**Likely files:** proposed `tests/integration/opencode/`, a focused decision document under `docs/`; `assets/opencode-config.json` remains unchanged in this PR.

**Acceptance:** the real pinned OpenCode process exposes or exercises the effective model, provider, MCP and skill selections; unrelated user settings survive; conflicting managed fields are detectable; JSONC input is not rewritten. Determine whether plugin/config loading can execute code and include that in the trust contract. No invented OpenCode inspection command: verify its actual CLI/API during the spike.

**Boundary/gate:** test assets and conclusions only. If overlays cannot compose safely, revise P10's design before implementation. A fixture that checks only a generated JSON object is insufficient.

### P02. Qualify credential storage and transport

**Purpose:** establish what Microsandbox 0.7.2 actually protects, persists and permits updating.

**Scope:** opt-in tests with canary credentials for Albert Bearer auth, GitHub `gh` requests, Git HTTPS Basic auth and Context7's actual authentication mechanism. Inspect host state, guest environment/files, command arguments and logs without publishing values. Test allowed destinations, redirects, rotation, addition and removal of bindings on running/stopped guests. Check keyring library availability and locked/unavailable/error distinctions on the supported OS families.

**Likely files:** proposed `tests/integration/credentials/`, SDK contract fixtures near `microsandbox_sdk.go`, focused decision record. Retain current single-key production behavior until P09.

**Acceptance:** positive real calls plus negative destination/redirect cases; demonstrate whether raw credentials persist on the host; record restart versus recreation requirements and any network substitution limits. Validate Git write rights with a dedicated disposable repository, never the production repository. A failed test setup is reported separately from a transport failure.

**Boundary/gate:** if Basic auth or Context7 cannot be protected, stop that dependent feature and return a design choice. A broker is a separate conditional PR with its own threat model and estimate. Guest-readable token fallback is not pre-approved. Keyring storage must not be marketed as eliminating runtime plaintext persistence if the runtime still writes it.

### P03. Qualify guest capabilities and PTY behavior

**Purpose:** choose a practical guest foundation and verify the full-mode user experience.

**Scope:** compare the current Alpine guest with a pinned Debian-based candidate for OpenCode, Node/npm, `gh`, Chromium, Playwright MCP and Chrome DevTools MCP. Record image size, cold preparation time, disk/RAM needs and both guest architectures. Test terminal resize, Ctrl+C, clean exit, reconnect and SDK handle ownership. Measure guest-side clone behavior that P22 depends on: clone from host path versus remote origin, submodule and linked-worktree handling inside a guest-owned clone, and clone disk/time cost for realistic repository sizes.

**Likely files:** proposed integration fixtures and guest candidate assets under test-only directories; no production base-image switch yet.

**Acceptance:** actual browser navigation, screenshot and DOM/console operation; guest TUI runs without host Node/OpenCode; stopping an attach handle has the intended VM lifecycle. Identify unsupported host/guest combinations explicitly. Record measured guest-clone behavior (host-path vs remote clone, submodules, linked worktrees, cost) as P22's design input; the sealed model has no host mount, so unsupported repository layouts must be refused with a clear error rather than worked around.

**Boundary/gate:** browser measurements decide P16, not the M1 default switch. PTY and guest-clone evidence gates P22 and therefore M1. A new maintained image pipeline, if needed, is explicit scope for P16 and may be split into its own prerequisite PR.

### P04. Typed configuration, schemas and provenance

**Purpose:** resolve #29's underlying design without immediately changing all existing launch behavior.

**Scope:** global user config, project manifest, initial lock schema, platform directories and pure field-by-field resolution. Retain a compatibility adapter around existing `Config` callers. Define explicit flags > namespaced environment > project > user > built-ins for non-secret settings; track whether a value was unset or explicitly empty. Credential lookup remains separate. Provide redacted configuration explanation and a bounded parser for explicit legacy `.env` import.

**Likely files:** `config.go`, `config_test.go`, `paths.go`, `cmd/codebox/main.go`; proposed schema/resolver files and versioned JSON schemas. Logical credential requirements map to local credentials through host-local bindings; no machine-specific keyring IDs in shared files.

**Acceptance:** table-driven precedence tests, explicit-empty behavior, malformed/unknown schema versions, platform directory tests and secret redaction. Reading configuration has no process-environment mutation. Secret literals and arbitrary env maps are not supported by the managed schema; reject unsupported fields. No config migration executes shell files.

**Boundary/compatibility:** new resolver is initially internal; legacy launch defaults stay until P12. Define and document the future deprecation window, but do not silently read an unrelated project `.env` in the new path. This PR contributes to #29; it does not close it.

### P05. Project discovery and Microsandbox instance identity

**Purpose:** stop reusing one sandbox for unrelated checkouts.

**Scope:** `ProjectContext` and `InstanceRef`, canonical root discovery, deterministic readable name plus path-derived suffix, host-local registry and per-project operation lock. Explicit directory override wins; otherwise use Git worktree root, or the current directory for non-Git projects. Do not scan/mount the home or filesystem root implicitly. Pass instance names into create, lookup, exec, logs, shell and PTY methods instead of the global constant.

**Likely files:** `microsandbox.go`, `microsandbox_sdk.go`, `name.go`, `paths.go`, tests; proposed `project.go` and registry helpers.

**Acceptance:** two same-named repositories and two worktrees never collide; symlink aliases resolve consistently; Windows casing/path rules are tested on Windows. Concurrent setup calls cannot create duplicate instances. Stale or unknown guest clones fail closed before attach (mount staleness checks are replaced by clone provenance checks in P22). Unsupported external Git metadata gets a precise preflight error. Discovery classifies each root as Git or non-Git and records it in the project context, because the sealed workspace (P22) has no clone source for a non-Git root and must either snapshot it or reject it.

**Boundary/compatibility:** existing singleton instances are discovered as legacy, not renamed/deleted automatically. Metadata absence is not permission to adopt an arbitrary VM. Record ownership only after validating the real guest workspace provenance and runtime state.

### P06. Project-aware lifecycle across runtimes

**Purpose:** replace runtime-type-as-instance assumptions without abandoning explicit runtime overrides.

**Scope:** project-bound backend construction and registry-based discovery; parameterize Tart/agent-vm names, staged files and log paths too. Separate runtime-global template/install operations from project instances. Make `stop`, `check`, `logs`, `shell` and destructive cleanup target a project; add an explicit all-instance stop. Enforce the initial one-active-instance policy by prompting to stop, not destroy, a conflicting environment. Correct platform capability checks.

**Likely files:** `runtime.go`, `dispatcher.go`, `tart.go`, `agentvm.go`, `paths.go`, CLI parser and matching tests.

**Acceptance:** stopping project A leaves project B intact; missing project context never silently becomes stop-all; unsupported runtimes fail before installation. Legacy instances remain discoverable for explicit lifecycle operations. Switching runtimes preserves the other instance's disk. Resource/template settings cannot accidentally rename or remove unrelated VMs.

**Boundary/compatibility:** document the changed `stop` scope and expose the explicit replacement in the same PR. Keep legacy destructive `restart` blocked behind a warning/confirmation until P07 replaces its semantics. No default-runtime switch yet.

### P07. Reconciliation and non-destructive restart

**Purpose:** apply setup changes to existing instances, including running ones, rather than returning early or recreating everything.

**Scope:** a small typed plan with operations for no-op, config write, guest process restart, VM restart and recreation. Persist desired/applied configuration revisions and capability completion markers without credential values. Hash only non-secret config; use opaque credential generations, not hashes of credentials. Implement resumable, atomic writes and a per-project mutation lock. Make restart non-destructive; expose explicit recreation with a named loss summary.

**Likely files:** `microsandbox.go`, adapter lifecycle methods, `assets/guest-prep.sh`, `cmd/codebox/main.go`; proposed plan/reconcile helpers.

**Acceptance:** identical desired state performs no writes; interruption resumes at the unfinished operation; config failure leaves the prior configuration usable; resource/mount/isolation changes are classified correctly. Unknown persisted state does not trigger deletion. Existing readiness/recovery and SDK detach/close regressions remain covered.

**Boundary/compatibility:** old `restart` scripts receive prominent release/migration documentation; tests prove no path invokes `Clean` as ordinary restart. Do not promise transactional rollback of OS package installation: journal completed effects and clearly mark partial repair requirements.

### P22. Sealed guest workspace and reviewed change delivery

**Purpose:** replace the writable host-checkout bind mount with a guest-owned clone, so no host file — including developer `.env` files and other local secrets — is readable by the agent process. Added in the 22 September revision; the shared-mount model is removed, not kept as an option.

**Scope:** guest-side clone of the project's canonical repository (host checkout used as the clone source when reachable, remote origin otherwise), kept under guest-local state so it survives stop/start. For a non-Git project root (which P05 still admits), there is no clone source: transfer an explicit host snapshot/archive into the guest instead, or reject the project at discovery with a clear message. Whichever path is chosen must be decided in this PR and reflected in P05's discovery contract — the sealed model must not promise non-Git support it cannot deliver. Sync host working-tree changes into the guest explicitly (initial clone/snapshot plus refresh on user action), and deliver guest changes back through a reviewed path: push to a branch/PR when the GitHub grant is enabled, or export a diff/patch to the host for review when it is not. Replace `WorkspaceMount` staleness checks with clone provenance checks. Retire the startup secret scan's role as a *mount* security boundary — it may remain as host-side hygiene advice — but the host→guest transfer path gains its own mandatory secret filter (see Acceptance).

**Likely files:** `microsandbox.go`, `microsandbox_sdk.go` (mount removal, clone lifecycle), `workspace.go` (gate repurposing), P05 project registry (clone location metadata), guest bootstrap assets; new workspace-sync and change-delivery helpers.

**Acceptance:** no bind mount of the host checkout exists in any supported path; prove it from the real persisted sandbox configuration, not only from codebox's own spec. A secret planted in the host checkout (a `.env` with a canary value) is unreadable from inside the guest — file access, `cat`, and agent-tool access all fail.

*Host→guest transfer is filtered, default-deny.* The existing `ScanWorkspace` logic does not disappear; it moves to the transfer boundary, where it can actually enforce. Every sync — initial clone/snapshot and each refresh — resolves a concrete, reviewable file set before any byte crosses into the guest. Dotenv-named files (beyond the safe `.example`/`.sample` suffixes) and gitleaks-detected secrets are **excluded by default**, not merely warned about. The user sees the filtered set and any exclusions, and may opt a specific file back in only through an explicit, per-file, recorded decision — never a blanket "include everything". A secret added to the host checkout *after* the last sync must not appear in the guest on the next refresh; prove this with a canary planted post-initial-sync. The filter applies to untracked and ignored files too, since those are exactly where local `.env` files live and a clone-only transfer would miss them.

*Change delivery and lifecycle.* Host edits reach the guest only through the explicit filtered sync; guest edits reach the host only through branch/PR or reviewed diff export. Uncommitted host changes are handled explicitly (included via the filtered patch path, or refused with a clear message — decide in this PR, do not silently drop them, and never bypass the filter to include them). Clone/snapshot refresh is idempotent and preserves guest-local untracked work or reports it before overwriting. Disk usage of the guest clone is accounted for in resource sizing.

*Non-Git projects.* The chosen behavior — snapshot transfer or discovery-time rejection — is implemented and tested end to end through P12's default launch. A non-Git root must never reach a state where the guest has no workspace and the error is opaque.

**Boundary/compatibility:** existing instances with bind mounts cannot be converted in place; they are legacy instances requiring explicit recreation under the sealed model (see Legacy transition). The delivery path is a workflow change users will notice — README, init review screen and release notes must state it plainly. Do not weaken the model by mounting a subdirectory "just for the .env case" or similar exceptions; the boundary is all-or-nothing per project. Equally, do not weaken it by treating the transfer filter as advisory: a sync that copies a dotenv file into the guest because the user pressed enter on a warning is the original leak with extra steps.

### P08. Host credential storage

**Purpose:** provide global credentials without putting them in project files or shell profiles.

**Scope:** minimal credential-store interface; macOS Keychain, Windows Credential Manager and Linux Secret Service adapters; explicit owner-only file fallback. Add complete `auth add`, `auth status` and `auth remove` operations with masked interactive input or a documented noninteractive stdin path. Display storage type and verification state, not secret values. Handle unavailable, locked, denied and corrupt stores separately.

**Likely files:** proposed `credentials*.go`, OS-specific adapters/tests, `paths.go`, CLI auth parsing; `go.mod`/`go.sum` only for reviewed dependencies.

**Acceptance:** real store put/get/remove on each supported OS family; headless Linux fallback only after explicit consent; restrictive directory/file permissions or ACLs; atomic replacement and symlink defenses. Never silently create a new plaintext store after keyring unlock fails. No secret in argv, logs, preview or exported project config.

**Boundary/compatibility:** host credential storage does not itself grant guest access. For deletion of a currently bound credential, coordinate with P09: until revocation can be enforced, refuse with an actionable message rather than claim access was revoked. Existing environment credentials remain supported as explicit session inputs.

### P09. Multiple secret bindings and revocation

**Purpose:** generalize the Albert-specific SDK path while preserving its protection boundary.

**Scope:** replace `APIKey`-only SDK arguments with resolved named bindings, approved hosts, transport metadata and placeholders. Resolve values immediately before the runtime operation. Reconcile addition/removal/rotation using P02's proven lifecycle. Host-local project approval authorizes each optional binding; global storage alone does not. Removal updates running instances before reporting success.

**Likely files:** `microsandbox.go`, `microsandbox_sdk.go`, credentials integration, capability reporting for other adapters and tests built from real SDK JSON.

**Acceptance:** Albert regression passes; multiple bindings cannot cross destinations; no raw values in guest environment/config; running and stopped rotations/removals have real negative tests. A removed key is not left working through stale persisted proxy state. Errors do not print SDK objects containing secrets.

**Boundary/compatibility:** optional GitHub/Context7 bindings are exposed only when their P02 transport gate passes. Tart/agent-vm report guest-readable credentials and require explicit acknowledgement of that weaker behavior; they do not inherit a Microsandbox protection claim. No broker or raw-token workaround hidden in this PR.

### P10. OpenCode composition, local trust and models

**Purpose:** make one effective configuration path govern provider, model and later MCP additions.

**Scope:** implement P01's tested guest-local composition strategy; preserve repository JSON/JSONC and unrelated user settings. Remove stale creation-time inline configuration as the authoritative source. Add host-local approval of execution-relevant project inputs before OpenCode loads them: managed MCP commands, endpoint destinations, plugins and relevant project config. Validate Albert credentials/catalogue through approved endpoints; cache last-known-good models and request explicit selection for stale IDs.

**Likely files:** `assets/opencode-config.json`, `microsandbox.go`, `guest.go`, `agentvm.go`, `tart.go`; proposed OpenCode builder, trust, provider and catalogue helpers.

**Acceptance:** effective real OpenCode config matches approved intent after create and restart; field conflicts show a clear diff rather than an invisible override. A pulled MCP/plugin or endpoint change cannot use credentials before approval. Trust decisions are tied to the canonical project and approved content; recheck before execution to avoid applying changed files. Network errors do not erase the working model; catalogue IDs do not fabricate token limits.

**Boundary/compatibility:** local trust records never travel in the repository. They govern codebox-managed activation, not every action a fully privileged guest process could take. Existing project configuration needs an initial review/import path; do not treat a cloned repository as trusted merely because it contains a lockfile.

### P11. Global setup wizard

**Purpose:** make machine and credential preparation understandable and resumable.

**Scope:** implement `setup`: platform/virtualization/disk preflight -> Albert endpoint and masked credential -> optional GitHub credential and editable identity -> review/apply. Use the real domain operations from preceding PRs. Separate read-only diagnosis from installation. Prototype then select a maintained terminal renderer; retain a plain terminal and noninteractive path. Prepare the minimal guest capability, not browser tools by default.

**Likely files:** `cmd/codebox/main.go`, new command/rendering files; setup operations in `internal/codebox`; runtime installer hooks and docs.

**Acceptance:** clean host with no Node/OpenCode reaches a configured global state; unavailable Secret Service produces a clear choice; no credential prompt precedes a fatal virtualization blocker. Back/cancel/retry, narrow terminals, paste, no-color and non-TTY paths work. Interrupted installation resumes; failed credential validation distinguishes rejection from unavailable network. Skip GitHub without penalty.

**Boundary/compatibility:** `setup` may store a GitHub credential but does not activate it for all projects. It does not say the project GitHub workflow is ready before P13. No inference call just to display a green status badge.

### P12. Zero-flag defaults and minimal project init

**Purpose:** deliver the first end-to-end useful milestone, not merely change two constants.

**Scope:** set built-in runtime to Microsandbox and isolation to full. Bare interactive launch offers missing global setup and a minimal `init`: project root, sharing mode, model, resources, Albert grant and review. Write the minimal manifest/lock data, prepare the sealed guest workspace (P22's clone), and launch the guest TUI. Existing environment/CLI overrides remain explicit; remove ambient `.env` discovery from the new path and offer bounded legacy import. No host OpenCode check in full mode.

**Likely files:** CLI dispatch, `runtime.go`, `isolation.go`, resolver defaults, port mapping and launch policy; README and migration docs.

**Acceptance:** clean-machine zero-flag walkthrough; second launch needs no setup questions and retains sessions; non-TTY launch fails with actionable missing-input details instead of hanging. Full mode opens no OpenCode server port and the guest holds no bind mount of the host checkout (verified from the persisted sandbox configuration). Existing singleton/backend installations offer continued explicit legacy use or approved migration, never automatic recreation.

**Boundary/compatibility:** the sealed workspace gate applies to every new instance from P12 onward: launching without P22's clone path is not a supported intermediate state. Existing backend credentials explicitly set to empty retain their documented meaning, with a warning where appropriate; never silently expose a listener off loopback. P12 closes #29 only after its full acceptance criteria and migration documentation are satisfied. M1 is a default-launch release, not full parity.

### P13. Guest GitHub workflow

**Purpose:** make `gh`, git push and PR operations actually available when selected.

**Scope:** provision `gh` as a versioned guest capability; set user-selected Git identity; configure the tested `gh` and HTTPS git authentication path. Add project selection and approval through the existing minimal init flow. Distinguish stored token, valid account, repository access, write/PR permissions and organization approval. Use repository-scoped fine-grained PAT guidance. In the sealed model this PR also completes the primary change-delivery path: branch push and PR creation from the guest clone are how agent work returns to the host.

**Likely files:** guest provisioning, GitHub helper/configuration, credential bindings, project schema and init options.

**Acceptance:** disposable private repository clone/fetch/push and draft PR from inside the guest; missing/expired/under-scoped token errors are distinct. Revoke the grant and prove subsequent authenticated requests fail. Never install host `gh` for this path. Guest key bytes remain hidden under the selected protected transport.

**Boundary/compatibility:** no host SSH-agent or signing-key forwarding. Disabled means no managed GitHub credential, not inability to access GitHub anonymously. If the transport spike requires an unapproved broker, this PR stays blocked rather than shipping a misleading partial claim.

### P14. Skills, managed instructions and lock data

**Purpose:** provide reproducible, reviewable project skills and preserve handwritten instructions.

**Scope:** catalogue selected skills from `etalab-ia/skills`; record source/revision/content integrity in lock data; fetch selected artifacts into a host cache and install guest-locally using P01's verified discovery path. Preserve user skills and manage only owned entries. Add/update a bounded AGENTS managed zone with a before/after diff. Expose selections through init's existing renderer, not an unrelated command family.

**Likely files:** proposed catalogue/skills/rules helpers, project schemas/lock writer, guest configuration, tests and a managed instruction template.

**Acceptance:** selection, deselection, cached offline launch, corrupt/missing archive, archive traversal/symlink escape and duplicate skill-name tests. Repeated init has no diff; malformed/ambiguous AGENTS markers stop safely; unmanaged text survives byte-for-byte. Real OpenCode discovers only the intended managed additions. No automatic latest revision on launch.

**Boundary/compatibility:** do not invoke an installer that uploads or executes arbitrary host-side code. Skills are reviewed instruction/code artifacts, not a sandbox security policy. Local-only mode stores selections outside the checkout and explicitly handles whether AGENTS content is written; it must not claim to be local-only while silently changing a tracked file.

### P15. Remote MCPs: data.gouv and Context7

**Purpose:** ship the simpler MCP integrations before browser provisioning complexity.

**Scope:** a small curated connector catalogue containing verified transport, endpoint, package/version if needed, configuration schema, credential requirement and health action. Add selected data.gouv and Context7 connectors to the approved overlay. Obtain a Context7 credential only when required by its verified integration contract. Display external data destinations and separate configured/verified/offline states.

**Likely files:** proposed MCP catalogue/configuration/health helpers, project schema, init selection and integration tests.

**Acceptance:** actual tool calls through OpenCode from the guest for both connectors; endpoint/schema drift, unavailable network and bad credentials reported distinctly. Deselection removes generated config and managed credential authorization. Selection does not install unrelated browser tooling. No `latest` package references in generated commands.

**Boundary/compatibility:** verify upstream URLs/auth mechanisms during implementation rather than infer them from names. Remote service availability/version remains external even when the local connector is pinned. Existing custom MCP config is preserved but requires local trust when execution-relevant inputs change.

**Deferred manual guest test (run at the end of this PR run):** the live OpenCode tool-call check is not yet complete. Host-side MCP `initialize` handshakes succeeded for both public endpoints, but this does not prove guest networking, OpenCode tool discovery, or tool execution. On a Mac with Microsandbox and pinned OpenCode available:

1. Provide a credential-safe inference route from the disposable guest to Gemini 3.8 Flash. Keep provider credentials on the host or behind a host-side proxy; never copy them into the guest. If no safe route is available, leave the test blocked rather than weakening credential isolation.
2. Create a disposable guest and select only `data-gouv` and `context7`. Do not use or modify the existing user sandbox. Use Context7 anonymously; no API key is required by the verified contract.
3. From inside that guest, inspect OpenCode's MCP status and record the tools returned for both server names. Confirm the configured endpoints match the curated catalogue.
4. Through OpenCode in the guest, ask Gemini to use one read-only tool from each server. Choose the tool after inspecting `tools/list`; record the tool name, sanitized arguments, and result. Do not invoke tools that mutate or publish remote data.
5. Confirm both calls completed through the guest's OpenCode process, then remove only the disposable guest and test files. Record the OpenCode/MSB versions, outcome, and any endpoint or auth error. Do not count the host-side `codebox mcp status` handshake as a substitute.

### P16. Browser capability and browser MCPs

**Purpose:** ship working Playwright and Chrome DevTools, not just configuration entries.

**Scope:** apply P03's chosen guest approach; version browser, fonts/libraries and connector packages, with architecture-specific artifacts. Add browser capability planning and host-aware resource recommendations. If a maintained image is necessary, introduce digest-pinned image publication as a clearly separate prerequisite change within this workstream, with integrity/provenance and update ownership. Keep browser install optional.

**Likely files:** guest assets/provisioning, SDK image/resource options, browser MCP catalogue, integration tests; optional image build workflow and release metadata.

**Acceptance:** Playwright navigation and screenshot; Chrome DevTools DOM/console interaction; local project dev-server access from the guest browser; both supported guest architectures qualified. No host profile, host browser or exposed remote-debugging listener. Test constrained memory/disk, interrupted install and repeat setup. Record measured resource defaults rather than guessing a larger RAM number.

**Boundary/compatibility:** do not disable browser security flags merely to make a test pass without reviewing the isolation implications. Base-image/root-disk changes may require explicit recreation; they cannot silently destroy an M1 guest. Broader Docker/toolchain parity is not included.

### P17. Complete project wizard and consistent DX

**Purpose:** unify the accumulated capabilities into the intended installation/setup experience.

**Scope:** searchable skill selection, MCP options, GitHub grant, model/resources, versioned/local-only choice and a real combined preview. Back navigation retains non-secret choices; secret input has a separate lifetime. Explain downloads, modified files, credential destinations and process/VM restart consequences. Preserve independent user config. Bare launch invokes only missing setup steps; a configured user reaches the TUI directly.

**Likely files:** command/rendering layer, shared setup plan, UX fixtures and user docs. No second implementation of provisioning inside the TUI.

**Acceptance:** recorded walkthroughs on narrow/wide terminals and Windows terminal; cancel at every step, keyboard-only selection, plaintext fallback, redirected streams and scripted mode. Versioned mode writes but never commits; local-only mode keeps managed selections in host state. Noninteractive approvals name the exact plan/grants; blanket yes cannot authorize arbitrary endpoints or destruction. Warm launch performs no dependency update and does not repeat choices.

**Boundary/compatibility:** agree performance budgets from measured M1/early wizard baselines: local resolution, warm TUI entry and cold download are separate metrics. Avoid presenting a percentage for unmeasured package work.

### P18. Controlled updates

**Purpose:** keep project environments current without changing them unexpectedly during launch.

**Scope:** `update` compares versioned skill artifacts and their generated managed rules with the exact current catalogue revision, builds a reviewable plan and applies approved changes. Catalogue refresh failure retains the known-good environment. The current runtime image is digest-pinned in the CLI and changes through a CLI release; curated remote MCP definitions are also CLI-owned, while the remote services expose no immutable version to compare. Therefore project `update` does not mutate/recreate the runtime or remote MCPs: those upgrades remain explicit CLI releases, and no floating reference is resolved during launch. This makes the CLI/runtime versus project-dependency boundary explicit rather than inventing lock revisions for external services that do not publish them.

**Likely files:** update command, catalogue resolvers, lock writer, managed rules and reconcile integration.

**Acceptance:** no-op update, selective update, network loss, integrity mismatch and crash between manifest/lock writes. Stage and recover paired file updates through a journal; no mismatched manifest/lock accepted as ready. Changed commands/destinations require approval. Config/lock rollback is tested; VM/package rollback limitations and guest-state loss are reported honestly.

**Boundary/compatibility:** no automatic guest-image recreation, no floating revisions on boot, and no claim that rolling back a lockfile restores guest disk contents. Retain previous artifacts only within a documented cache/space policy.

### P19. Albert Code importer

**Purpose:** bring existing users across without deleting or executing their setup.

**Scope:** explicitly detect `.albert-code/skills.txt`, OpenCode config and known managed AGENTS zones. Parse recognized intent into a proposed manifest/lockfile and host credential references. Preserve unknown custom settings for review. Offer explicit credential import only from supported safe inputs; never source a shell profile/runtime script. Keep the legacy `.env`/singleton migration introduced for M1 covered here too.

**Likely files:** importer helpers/fixtures, init import flow, migration docs; no change to the albert-code repository.

**Acceptance:** realistic fixtures with JSONC, custom MCPs, partial setup, malformed markers and tracked/untracked config. Import twice is idempotent. Preview contains no secret bytes; originals remain untouched until the user approves any separate cleanup. Git exclusion edits, if needed, preserve unrelated lines and use Git-resolved paths. Because the sealed model has no host mount, importing an Albert Code project creates a guest clone rather than adopting its workspace, and the clone/sync goes through P22's default-deny transfer filter; the importer must state that guest sessions/files from the old setup do not transfer, and a credential file left in the host checkout is reported as host hygiene advice (it is filtered out of the guest, not merely ignored).

**Boundary/compatibility:** guest sessions, untracked guest files and installed tools are not declared migrated without a proven export/import path. Never execute `.agent-vm.runtime.sh` to discover configuration.

### P20. Verified installers

**Purpose:** reduce installation friction without bypassing existing release integrity checks.

**Scope:** small POSIX-shell and PowerShell wrappers around published artifacts; detect supported target, resolve one release, obtain binary/checksums from that release, verify before installation, install to a user-owned directory and invoke setup. Retain prior binary for rollback. Offer PATH guidance; prompt before profile changes. Reject unsupported targets such as macOS Intel while the SDK lacks its FFI build.

**Likely files:** proposed installer scripts, README, release workflow only where artifact/attestation needs require it; installer tests using representative release metadata.

**Acceptance:** real installation and upgrade on advertised targets, checksum mismatch, partial download, insufficient permissions, spaces in paths and interrupted replacement. No sudo for the user CLI installation. Version skew cannot mix one release's binary with another release's checksum. Preserve the runtime's existing pinned artifact verification.

**Boundary/compatibility:** distinguish checksum integrity from authenticated publisher provenance. Use existing release attestations where verified; otherwise document the trust boundary. Homebrew packaging/notarization are separate follow-ups, not prerequisites hidden in this PR.

### P21. Final diagnostics and release qualification

**Purpose:** prove the assembled experience and publish the actual support matrix.

**Scope:** integrate existing feature diagnostics into read-only `doctor` and project `check`; report host capability, VM state, toolchain, provider verification, managed skills and MCP readiness separately. Add whole-flow integration fixtures, redacted support output and migration/release documentation. Validate empty-machine, returning-user, cloned-team-project and imported-project journeys.

**Likely files:** diagnostic orchestration, end-to-end test harness, docs and hosted CI for deterministic host-side integration tests. Native microVM qualification is recorded through the beta protocol in [`docs/qualification-beta.md`](../qualification-beta.md), not inferred from hosted compilation. Do not provision a permanent runner fleet as a prerequisite; a controlled self-hosted runner may be considered later if beta evidence shows the manual process is inadequate. Do not grant repository secrets to untrusted fork jobs.

**Acceptance:** M1/M2 flows plus update/import on macOS arm64, Linux amd64/arm64 and Windows amd64/arm64 as advertised. Hosted native compile/tests alone do not count as microVM qualification. Record missing evidence explicitly and block unsupported claims. Retry/offline/cancel/revoke tests pass; secrets are absent from diagnostics; doctor does not install or mutate. Confirm explicit Tart/agent-vm paths still work with documented limitations. Collect real-host microVM evidence using the beta protocol; no permanent cloud hypervisor fleet is required to begin qualification.

**Boundary/release:** only after acceptance, merge the appropriate release-please release PR through the existing process. Publish changes to stop/restart scope, runtime/isolation/workspace defaults, configuration lookup and credential storage prominently. Do not label the release complete parity if a credential transport or curated MCP remains unqualified.

## 5. Contrat transversal de tests et de migration

### Checks on every relevant PR

| Layer | Evidence required |
|---|---|
| Resolver/config | Source precedence, explicit empty values, schema compatibility, no global env mutation, redaction |
| Filesystem | Atomic writes, process interruption, symlink/path traversal, ownership and permissions, no unrelated edits |
| SDK | Fixtures captured from real serialized state; positive and negative persisted-state cases; real transport smoke test when affected |
| Lifecycle | No-op, apply failure, retry, current-versus-desired state, project isolation, no implicit deletion |
| Credentials | Store unavailable/locked, explicit fallback, destination mismatch, redirect, rotation and removal |
| Sealed workspace | No host bind mount in the persisted sandbox config; planted host secret unreadable from the guest; host→guest sync default-deny filtered (dotenv and gitleaks hits excluded, per-file opt-in only); post-sync secret planting stays out of the guest on refresh; non-Git roots reach the decided snapshot-or-reject behavior; sync and change-delivery paths exercised both directions |
| User config | Existing JSONC/MCPs/skills/instructions preserved; effective OpenCode state checked |
| CLI | No prompt on non-TTY, help/version usable without credentials, targeted repair messages, no raw secret output |
| Platform | Native builds and tests; real keyring/PTY/microVM evidence wherever those behaviors change |

**Stop conditions:** a security boundary cannot be reproduced; a host bind mount survives in any supported path; a transfer path can carry a dotenv or gitleaks-detected secret into the guest without an explicit per-file opt-in; a destructive migration lacks consent; an existing project loses unmanaged content; or a claimed supported platform has no required integration evidence. Do not relabel these as documentation-only limitations to ship the feature.

### Legacy transition

- Keep legacy exported environment settings for a documented deprecation interval; use provenance/warnings when they override new defaults. Choose the exact retirement release during P04, not in an unreviewed cleanup commit.
- New configuration never implicitly sources generic project/executable-adjacent `.env`; offer explicit import and leave originals intact.
- Existing singleton guests are legacy instances. They carry a bind-mounted workspace, which the sealed model forbids: they are never adopted in place. Migration means explicit recreation under P22's clone model, with user approval and a clear statement that guest sessions and untracked guest files do not transfer. Adoption of any instance requires matching the real guest workspace provenance/isolation and compatible provisioning.
- `stop` changes to project scope, with explicit all-instance operation. `restart` changes to non-destructive stop/start; recreation/cleanup names the instance and loss. Include these changes in M1 release notes.
- New defaults apply to new/unconfigured intent, not as permission to overwrite explicit runtime or isolation choices. An existing backend guest is not silently transformed into full mode.
- Schema readers fail clearly on newer unsupported schemas. Old binaries are not promised forward compatibility with newly configured guests; binary rollback and guest/config rollback are different procedures.

Le document de revue d'architecture détaillé associé est disponible dans [albert-code-parity-architecture.md](./albert-code-parity-architecture.md).

## 6. Organisation des issues et de la revue

After plan approval, establish this issue structure. These are proposed titles, not claims that issues exist.

| Tracker | Proposed scope | PRs |
|---|---|---|
| Existing #29 | Configuration contract, global/project setup baseline, default launch and legacy config migration | P04, P11, P12; references P08/P10 |
| `Qualifier les contrats OpenCode, secrets et environnement invité` | Compatibility evidence with three checklists | P01-P03 |
| `Gérer les instances et leur cycle de vie par projet` | Identity, adapter scope, reconciliation and the sealed guest workspace | P05-P07, P22 |
| `Protéger et révoquer les identifiants des projets` | Credential storage, bindings, trust and GitHub transport | P08-P10, P13 |
| `Configurer les skills, instructions et MCP par projet` | Project capabilities and unified wizard | P14-P17 |
| `Mettre à jour et importer les environnements existants` | Dependency updates and Albert Code import | P18-P19 |
| `Qualifier l’installation et la parité multiplateforme` | Installers, diagnostics and end-to-end release gates | P20-P21 |

Use French issue/PR prose in this repository. Each PR names its planning identifier and tracker, states prerequisites, documents tests actually run and distinguishes source-derived expectations from real observations. Close a tracker only when all of its acceptance criteria are met; use GitHub's English closing keywords (`Closes #N`) in otherwise French bodies.

For the plan's review, the natural repository destination is `docs/plans/albert-code-parity.md` in a **documentation-only draft PR** linked to #29 and the gist. This downloadable draft does not itself create that PR, commit to the repository, or modify the gist. Implementation begins only after review.

## 7. Maîtrise du périmètre et estimation

The earlier 5-8 engineer-week range is an initial estimate, not a commitment. The sealed workspace decision (P22) removes the shared-mount model from scope but adds clone lifecycle, bidirectional sync and change-delivery work that the previous range did not price in; expect the upper bound to move up. Re-estimate after P01-P03 using actual credential, browser, platform and guest-clone results. A protected GitHub broker, maintained guest-image pipeline or worktree metadata support can materially change it. Do not absorb those as incidental wizard changes.

The 22 PRs are review boundaries, not equal-sized units. Split P16 if it needs a new image supply chain; split P22 into clone lifecycle versus change delivery if the review surface grows; split native-store adapters from their common interface if P08 becomes difficult to review. Conversely, combine test-only spike artifacts only if their independent conclusions remain clear.

**Deferred deliberately:** any writable host-checkout mount (removed from scope, not deferred — the sealed model replaces it), simultaneous active projects, host signing-key forwarding, arbitrary secret-manager plugins, broad Docker/other-agent installations, automatic background updates, a runtime/plugin framework, and package-manager distribution beyond the verified installer path.

**Premier lot d'implémentation après approbation :** mettre à jour #29 et créer le suivi de compatibilité ; exécuter P01-P03 tout en implémentant la fondation de configuration pure P04. Examiner leurs preuves avant de câbler des identifiants supplémentaires ou de choisir l'image navigateur de production.
