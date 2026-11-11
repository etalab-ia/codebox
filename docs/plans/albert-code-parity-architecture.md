# codebox: Albert Code parity implementation plan

**Decision draft | 22 September 2026**

## Recommendation

Keep the Go CLI and the existing runtime adapters. Evolve codebox from a runtime launcher into a **project environment manager**, with two explicit setup scopes:

- **Global setup:** managed runtime installation, credentials, identity, trusted endpoints and machine defaults.
- **Project setup:** selected skills, MCP servers, agent instructions, model, toolchain requirements and permission grants.

The normal path becomes `codebox` from a project directory. Microsandbox and full isolation are built-in defaults, not wizard questions, required flags or required environment variables. Existing runtime flags and `--isolation backend` remain explicit escape hatches. No automatic fallback to a weaker isolation boundary when Microsandbox is unavailable.

This is a staged extension, not a rewrite. The necessary refactors concern configuration resolution, project identity, credential transport, OpenCode configuration and lifecycle reconciliation. A polished wizard should be the front end to those tested operations, not the place where they are implemented.

## Evidence and scope

Source snapshots reviewed:

- [codebox `6d23165`](https://github.com/etalab-ia/codebox/tree/6d231654633aa409ca373f89b1073f68e92ca619), current main when inspected.
- [albert-code `46569c7`](https://github.com/etalab-ia/albert-code/tree/46569c70653ee8e987ee4e4b57d3d9153c505700), current main when inspected.
- [codebox issue #29](https://github.com/etalab-ia/codebox/issues/29), the existing configuration-model issue. Use it as the configuration workstream, not as an unrelated duplicate.

This is a source-based assessment. No runtime, PTY, browser or secret-proxy tests were executed for this report. Proposed commands and file formats below are designs, not currently implemented CLI features. The backlog is not yet published as GitHub issues.

### What “full isolation” means here

The existing `full` mode means **the entire OpenCode process, including its TUI, runs in the guest**. In codebox as it stands today, it does not mean an air gap or an immutable host workspace: the checkout is writable through a bind mount and public-network access is configured. The Albert credential is substituted at the Microsandbox network boundary; other runtimes expose it inside the guest.

> **Revised 22 September 2026 (sealed model).** Luis decided to **remove the writable host-checkout mount entirely** and adopt a sealed guest workspace: the guest owns its own clone and returns changes through a branch/PR or a reviewed diff. The shared-mount model is no longer the default and is not kept as an escape hatch. Rationale: a startup secret scan cannot close the leakage window in a writable mount — a secret added after startup, in an undetected format, or written by the agent itself stays readable. The absence of a mount is the only real boundary. The consequences (loss of live host editing as the default path, a mandatory change-delivery flow, upward re-estimation) are detailed in P22 of the PR-by-PR plan. The passages below that still describe the old shared-mount default are retained as the historical analysis and are superseded by this note and by the plan.
>
> **Addendum 23 September 2026.** The startup secret scan is not dropped with the mount; it moves to the host→guest transfer boundary, where it becomes a default-deny filter (dotenv-named files and gitleaks-detected secrets are excluded, with explicit per-file opt-in only). This closes the residual path where a sync could copy a newly added `.env` into the agent-readable guest. Non-Git project roots, which have no clone source, get an explicit snapshot transfer or a discovery-time rejection. Both refinements are specified in P22 of the PR-by-PR plan.

The plan no longer retains the writable-mount workspace model: no home-directory mount, no host checkout mount, no SSH-agent forwarding, no host browser profile, no host Docker socket, and no host MCP processes. The guest clone is the only project filesystem the agent touches.

**Decision settled 22 September 2026:** "full isolation" now means *no writable host filesystem shared with the agent*. The bind mount is replaced with a guest-owned clone and branch/PR or reviewed-diff delivery (P22 of the PR-by-PR plan). This is a different workspace architecture and is settled before implementing project identity.

## 1. Parity inventory

| Capability | Albert Code today | codebox today | Target |
|---|---|---|---|
| First-run installation | Host bootstrap, interactive credentials, VM preparation | Release binary, managed verified Microsandbox runtime download | Verified installer plus resumable global setup; retain download verification |
| Default launch | Full guest execution through agent-vm | Explicit runtime on macOS/Linux; backend isolation | Microsandbox + full, zero flags |
| Global credentials | Albert and optional GitHub setup; shell/runtime-file persistence | Albert from environment or generic `.env` | OS credential store; named credential references; optional explicit fallback |
| GitHub workflow | PAT validation, git identity, guest `gh`, git auth | Git installed; fixed agent identity; no managed GitHub setup | Optional project grant for HTTPS git and GitHub API/PR operations |
| Project discovery | Setup in current project | Defaults to `./workspace` | Current project/worktree root, with explicit directory override |
| Skills | Per-project selection, guest cache and reconciliation | No managed selection or installation | Searchable selection, pinned revision, guest-local installation |
| MCP | data.gouv, Context7, Playwright, Chrome DevTools | No managed connector setup | Same curated choices, opt-in, real guest verification |
| Agent instructions | Managed zone in `AGENTS.md`, user text preserved | No scaffold/update mechanism | Reviewable managed policy with preservation and conflict detection |
| Existing OpenCode config | Provider merge and model repair | Fixed embedded inline provider config | Preserve project settings; explicit managed-key ownership |
| Models/endpoints | Custom base URL, catalogue checks and stale-ID repair | Fixed Albert endpoint and model template | Trusted endpoint profiles, model discovery and controlled updates |
| Guest tools | Debian base with Node, git, gh, Chromium, Docker and other tools | Smaller Alpine preparation: build tools, Node, Python, git | Curated base + browser capability; broader tools evaluated separately |
| Updates | Project rules/config/runtime refresh; skills pull at boot | Binary/runtime updates, no project reconciliation | Explicit project update preview and pinned dependency refresh |
| Resources | Host-aware CPU/RAM defaults; configurable disk | Microsandbox hardcoded 2 CPU / 4 GiB / 8 GiB | Resource plan, disk checks, configurable per project |
| Diagnostics | Setup checks and troubleshooting | Runtime doctor; full-mode check reports VM state | Runtime, guest, provider, skills and MCP readiness separately |

Parity should reproduce useful capabilities, not historical implementation choices. Do not copy secrets into shell startup files, install every skill when the manifest is absent, update remote code on every launch, or treat prompt instructions as security enforcement.

Albert Code itself has documentation drift: its README describes newly selected MCPs as disabled, while `scaffold_opencode_json` generates `enabled:true`. The target contract should be unambiguous: selected and approved connectors are enabled; unselected connectors do not execute.

## 2. Required architectural changes

### A. Replace ambient configuration with explicit resolution

**Evidence:** [`config.go`](https://github.com/etalab-ia/codebox/blob/6d231654633aa409ca373f89b1073f68e92ca619/internal/codebox/config.go#L97) reads `.env` from the working directory and executable directory, mutates process environment and mixes secrets, runtime settings and project paths. `main.go` separately reads `RUNTIME` from the environment. The workspace default is `./workspace`.

**Impact:** introduce a pure resolver that takes explicit sources and returns a typed configuration plus field provenance. Keep a compatibility adapter for legacy environment settings. Do not have adapters discover configuration independently.

Proposed non-secret precedence: explicit CLI flags > deliberate namespaced environment overrides > project settings > user defaults > built-ins. Legacy `RUNTIME`, `ISOLATION`, `WORKSPACE_DIR` and credential environment variables remain during migration, with a warning when they change the normal defaults. A redacted `config explain` command names the winning source.

Credentials use a separate lookup path: explicit invocation credential input or recognized environment override > project-selected credential reference > global default reference. No credential literal in a project manifest. Never accept secrets as command-line flag values; use masked entry, stdin or a credential store.

Stop loading arbitrary application `.env` files in the new model. Provide explicit legacy import, with preview. Never execute a shell startup file to import values.

### B. Introduce project-scoped sandbox identity

**Evidence:** [`microsandbox.go`](https://github.com/etalab-ia/codebox/blob/6d231654633aa409ca373f89b1073f68e92ca619/internal/codebox/microsandbox.go) uses a fixed sandbox name. SDK creation, shell, logs and attach also hardcode that identity. [`dispatcher.go`](https://github.com/etalab-ia/codebox/blob/6d231654633aa409ca373f89b1073f68e92ca619/internal/codebox/dispatcher.go#L14) maps runtime types to single backends and assumes only one active runtime. A workspace mismatch currently warns and continues.

**Impact:** add `ProjectContext` and `InstanceRef`; pass the instance through all adapter operations, including logs and PTY attach. Identify a project from its canonical worktree root, not just repository name or remote URL. Generate a readable name with a short path-derived suffix and store metadata outside the workspace. Two worktrees must not share one guest accidentally.

Fail closed on mount mismatch. Detect symlink aliases and moved checkouts. A moved project gets an explicit rebind/migration choice, never an implicit deletion. Do not mount an entire parent directory merely to make Git worktrees function: their `.git` pointer can refer outside the mounted tree. Test this case; either provide a narrow safe metadata strategy or explicitly reject unsupported layouts until available.

Make normal `stop`, `check`, `logs`, `shell` and `clean` project-scoped; add explicit all-instance operations. This changes today's global `stop` contract and needs migration messaging. Initially permit several persistent project environments with one active environment at a time. Concurrent environments can follow once port allocation is reliable.

### C. Separate preparation, configuration and running

**Evidence:** [`microsandbox.go:Start`](https://github.com/etalab-ia/codebox/blob/6d231654633aa409ca373f89b1073f68e92ca619/internal/codebox/microsandbox.go#L207) combines validation, workspace scanning, installation, creation, boot, readiness and recovery. Existing running guests return before configuration refresh. `nextStartEnv` refreshes HTTP credentials but deliberately retains creation-time OpenCode configuration. The bootstrap script is immutable and isolation is inferred from its text.

**Impact:** introduce a small reconciliation plan, not a general workflow framework:

`discover -> resolve -> preflight -> plan changes -> approve -> ensure guest -> apply project config -> verify -> run TUI`

Classify changes as no-op, live configuration update, guest-process restart, VM stop/start, or destructive recreation. Record configuration/provisioning versions and hashes without secret values. Keep the existing proven recovery logic, but version readiness markers by installed capability rather than using one permanent `toolchain-ready` flag.

Do not recreate a VM to toggle a skill. Conversely, do not report an updated credential as effective while an old running guest still uses the previous proxy configuration. Probe SDK behavior for adding/removing secrets as well as rotating existing secrets; they may have different lifecycle requirements.

Separate ordinary restart from recreation in the new CLI. Today's `restart` destroys guest state; migrate it explicitly rather than silently changing its meaning. Named confirmation must describe lost sessions, installed tools and guest-only files. Capture/export supported session data before destructive migration; never promise preservation without a tested export path.

### D. Generalize secret transport without weakening it

**Evidence:** `Config.APIKey`, `msbSandboxSpec.APIKey`, `ModifyNextStart(..., apiKey)` and [`msbNextStartOptions`](https://github.com/etalab-ia/codebox/blob/6d231654633aa409ca373f89b1073f68e92ca619/internal/codebox/microsandbox_sdk.go#L169) are Albert-specific, with a single fixed allowed hostname.

**Impact:** represent named credential bindings with a credential reference, guest placeholder, approved destination and transport requirements. A credential-store interface needs only get/put/delete/metadata operations; runtime adapters receive the minimum resolved bindings.

Use platform credential storage where available: macOS Keychain, Windows Credential Manager and Linux Secret Service, subject to implementation verification. Headless Linux must work without assuming a graphical keyring: offer a clearly labelled owner-only file outside every workspace, with 0700 directory/0600 file and Windows ACL equivalents where applicable. Never silently fall back to plaintext. Inspect the Microsandbox host-side persistence too: putting a key in a keychain does not guarantee the runtime avoids storing another copy.

Global storage does not grant every project access. New projects receive Albert access only for the trusted endpoint; GitHub and Context7 require explicit selection/grants. Changes to endpoint, MCP command or credential destination require fresh approval recorded in host state, not in repository-controlled files.

**GitHub is the critical spike:** prove `gh` API calls and HTTPS git push using placeholders. Git can encode credentials into HTTP Basic auth, so working Bearer-header substitution does not prove git push support. Test redirects, allowed-host boundaries and token rotation. If protected transport cannot support git, choose an explicit design before shipping: a narrowly scoped host credential broker, or a disclosed weaker guest-readable token mode. Never silently expose the PAT to make parity appear complete.

Use dedicated, short-lived, repository-scoped fine-grained PATs. Account validation does not prove repository write/PR rights or organization approval; display those as separate statuses. Destination filtering hides the credential bytes, but does not prevent a malicious agent from abusing authorized GitHub operations. Token scope and human review remain necessary.

### E. Make OpenCode configuration composable

**Evidence:** [`assets/opencode-config.json`](https://github.com/etalab-ia/codebox/blob/6d231654633aa409ca373f89b1073f68e92ca619/assets/opencode-config.json) is embedded and sent through `OPENCODE_CONFIG_CONTENT` at guest creation. It fixes the provider, model and permission policy. Adding an `opencode.json` wizard without addressing that second source risks conflicts and invisible overrides.

**Impact:** one configuration builder owns generated provider/MCP settings. Keep user OpenCode configuration authoritative for unrelated fields. Before choosing file layout, test the pinned OpenCode version's actual merge precedence for project JSON/JSONC, inline content, global files and explicit config paths. Assert the effective result, not merely the generated JSON.

Recommended ownership: `.codebox/project.json` declares project intent; a generated guest-local OpenCode overlay supplies managed fields. Do not overwrite the user's `opencode.json`. Reject conflicting managed fields with a precise diff and a choice. Keep generated keys out of a stale persisted environment value. Preserve user JSONC comments by avoiding rewrites; any optional import requires a real JSONC parser.

Model catalogue checks validate IDs, not context/output capabilities unless the API actually reports them. Cache last-known-good results, distinguish invalid keys from network failures, and never erase a working model because the catalogue is temporarily unavailable. Custom endpoints require explicit host-side trust before sending any credential; validate TLS and redirect handling.

### F. Treat guest capabilities as real dependencies

**Evidence:** [`guest-prep.sh`](https://github.com/etalab-ia/codebox/blob/6d231654633aa409ca373f89b1073f68e92ca619/assets/guest-prep.sh) uses Alpine `apk`, has no explicit `gh` or Chromium provisioning, and reapplies a fixed Git identity. Microsandbox uses 2 CPU, 4096 MiB RAM and an 8 GiB root disk in [`msbCreateOptions`](https://github.com/etalab-ia/codebox/blob/6d231654633aa409ca373f89b1073f68e92ca619/internal/codebox/microsandbox_sdk.go#L113).

**Impact:** define a minimal base capability and an optional browser capability. Provision `gh` when GitHub is enabled and set the selected Git identity without overwriting it every launch. Use a workspace-specific Git safe-directory entry rather than `safe.directory '*'`. Do not forward a host signing key; verified signing is a separate opt-in design, not PAT parity.

Browser MCPs require executable compatibility, browser libraries, fonts and process/security configuration. Do not assume `npx @playwright/mcp` makes Alpine a supported browser environment. Spike Alpine + system Chromium against a pinned Debian-based OCI guest. Prefer Debian if it materially improves reliable browser/tooling parity; retain Microsandbox as the hypervisor either way. Publish maintained, digest-pinned images only after confirming the chosen images boot and run on the target architectures.

Albert Code's broader Docker/other-agent installations are not automatically required for OpenCode parity. List them as optional development capabilities. If Docker support is desired, validate guest-local daemon support; never substitute a host Docker socket.

### G. Remove backend-first operational assumptions

**Evidence:** `msbPortMappings` publishes 4096 and 3000-3010 even in full mode. Full-mode `check` reports VM state, not provider/MCP readiness. The default HTTP password is static. The backend attach path checks for host OpenCode after starting the guest, despite a comment claiming otherwise.

**Impact:** full mode should not publish 4096 or configure unused server auth. Make preview ports explicit and loopback-bound, with collision checks and displayed URLs; verify the SDK's actual bind behavior. Backend mode gets a generated per-instance password and its own host OpenCode preflight before boot. Runtime capability checks should reject unsupported platform/runtime combinations early, including Tart on Linux.

`doctor` should diagnose without secretly installing; installation/repair belongs to setup or an explicit repair action. Report VM state, guest readiness, credential verification and MCP health independently. No inference request on every launch merely to paint a green status line.

## 3. Configuration and persistence contract

| Data | Proposed home | Ownership |
|---|---|---|
| User defaults and endpoint profiles | Platform user config directory / `codebox/config.json` | User; contains references, not secrets |
| Credential values | OS credential store, or explicitly accepted protected fallback | Host only; never workspace-mounted |
| Project intent | `.codebox/project.json` | Reviewable, shareable, no absolute host paths or secrets |
| Pinned skills/MCP/image versions | `.codebox/lock.json` | Shareable lock data |
| Project rules | Managed zone in `AGENTS.md` | Reviewable; preserve everything outside the zone |
| Trust grants, canonical paths, instance IDs | Platform state directory / `codebox/projects/<id>/` | Host-local; not repository-controlled |
| Effective OpenCode config and installed skills | Guest-local managed directory | Generated from approved intent |
| Downloaded packages/catalogues | Platform cache directory | Disposable; integrity verified |

Use platform directory helpers rather than hardcoding Linux paths on Windows/macOS. JSON keeps the initial Go implementation small; schemas provide editor validation and explicit version migrations. Strictly validate managed manifests and preserve unknown OpenCode fields.

Recommend versioning project intent and lock data, unlike Albert Code's local-only selections: teammates should be able to reproduce the environment. Offer a local-only setup that stores intent in host state instead. Do not add broad `.gitignore` entries or hide an existing tracked config. Locate Git exclusion paths through Git itself for worktrees; preserve all unmanaged content.

Project trust is local and keyed to the approved execution-relevant config. Changes from a pull must not execute new MCP commands or redirect credentials automatically. A noninteractive launch fails with actionable trust/setup instructions instead of implicitly approving repository code.

## 4. Installation and wizard DX

### Proposed command surface

```text
codebox                         First-use setup when needed, then guest OpenCode
codebox setup                   Global machine and credential setup
codebox init                    Project setup / edit selection
codebox auth add|status|remove   Credential management, redacted output
codebox update                  Preview and apply project dependency/policy updates
codebox config explain          Effective settings with source provenance
codebox doctor                  Read-only diagnostics
codebox stop                    Stop this project's guest
codebox stop --all              Explicitly stop all managed guests
codebox recreate                Explicit destructive rebuild
```

Keep `--microsandbox`, `--tart`, `--agent-vm`, and `--isolation backend|full`. Add deliberate workspace and resource flags; avoid a proliferation of feature-selection flags before there is a demonstrated automation need. Wizard commands support `--dry-run`, `--non-interactive` and structured output. Noninteractive consent must name grants; generic `--yes` must not approve arbitrary endpoints or destructive migration.

### Binary installation

Provide a short documented path on macOS/Linux and PowerShell on Windows: detect architecture, resolve a single release version, download the binary and checksum from that same release, verify, install to a user-writable bin directory, then invoke setup. Never require sudo merely to install the CLI. Explain PATH changes and ask before modifying a shell profile. Detect existing installs and preserve a rollback binary.

Retain the current pinned Microsandbox artifact verification. Checksums provide integrity, not independent publisher authentication; add attestation verification where supported. Notarization/package-manager distribution can be a later distribution workstream. No unverified one-line installer promise in the interim.

### Global setup: four short stages

1. **Machine:** OS/architecture, virtualization, disk and memory checks. Show what will be installed and approximate download size. On Windows, explain WHP/reboot prerequisites. No secret prompts before a fatal platform blocker.
2. **Albert:** masked key field, trusted endpoint shown, bounded validation. Distinguish rejected credentials, timeout, unavailable catalogue and offline/unverified state. Offer explicit save-for-later without pretending verification succeeded.
3. **GitHub, optional:** skip is easy. Show dedicated fine-grained PAT guidance, masked entry, account validation and editable Git identity. Store globally but state that projects must opt into its use.
4. **Review and apply:** credential destinations/storage backend, runtime version, disk footprint and host changes. Apply with resumable stage progress. No host Node/OpenCode dependency in the default full mode.

### Project setup: a compact selection screen

```text
codebox / assistant-rh

Environment   Microsandbox / full isolation
Workspace     ~/Code/etalab-ia/assistant-rh
Model         Albert / deepseek-v4-flash

Skills        Search...  [selected items with one-line descriptions]
Tools         [ ] data.gouv  [ ] Context7  [ ] Browser testing
GitHub        Disabled    [Enable for this project]

Changes       Create project.json and lock.json
              Add managed rules to AGENTS.md
              Install browser dependencies only if selected

              Back       Review changes       Apply
```

The mockup is illustrative; labels/models reflect available catalogue entries at setup time. Project detection can recommend relevant skills, but nothing is silently installed. The browser choice expands to Playwright/Chrome DevTools selection and prerequisites. Context7 asks for a credential only when selected; reuse an existing stored credential without re-entry. Explain external data destinations beside remote MCP choices.

Review shows a real file diff, downloads, credential grants and whether a running guest must restart. Existing `AGENTS.md`, OpenCode settings and personal skills are preserved. Skill deselection removes only managed entries. Re-running setup is idempotent and shows current choices.

### Interaction quality

- Use a maintained Go terminal component library after a small accessibility/Windows prototype; Bubble Tea/Huh are candidates, not yet a dependency decision. Keep operations independent of the renderer.
- Keyboard-first navigation, searchable multi-select, back navigation, masked secrets, paste-safe inputs and no token echoed into a yes/no question.
- Narrow-terminal layout; plain-text/no-color fallback; non-TTY operation must never hang on a prompt. French user-facing copy suits the target audience.
- Named stages and real download progress; show preparation logs on demand. Do not invent a percentage for unmeasured package installation.
- Ctrl+C restores the terminal, records completed non-secret steps and allows retry. Never save entered secrets in wizard transcripts or recovery journals.
- End with an honest summary: ready, configured but unverified, or blocked with a specific repair action. Do not leave the user to discover missing setup inside an unconfigured OpenCode session.
- Bare launch offers missing global/project setup only interactively. Returning users reach the TUI without repeated questions. Choose a persistent project stop policy once instead of asking on every exit.

## 5. Delivery sequence and code impact

Each phase should be a small set of reviewable PRs, with French GitHub issues and explicit acceptance tests. Extend #29 for the configuration contract; create linked workstream issues before implementation. Do not package the whole migration into one PR.

| Phase | Work and primary code impact | Exit gate |
|---|---|---|
| 0. Contract and compatibility spikes | Verify OpenCode merge precedence; PAT Bearer + git Basic auth substitution; Context7 auth; browser MCP on Alpine vs Debian; PTY lifecycle; define workspace boundary | Written decisions and real integration evidence; no unresolved security claim hidden behind a mock |
| 1. Defaults and resolver (#29) | `config.go`, `isolation.go`, `runtime.go`, `paths.go`, `main.go`; new typed manifest/resolver with provenance; legacy import | New install launches Microsandbox/full without flags; CLI overrides win; legacy instance is never silently destroyed |
| 2. Project instances and lifecycle | `dispatcher.go`, `runtime.go`, all adapters and SDK attach/log/shell methods; project registry and metadata | Two projects preserve distinct guests; wrong mount refuses; project-scoped lifecycle; destructive action is explicit |
| 3. Credentials and global setup | Credential store/bindings; replace single-key SDK signatures; GitHub identity and guest `gh`; setup domain operations | Fresh-machine global setup works; guest sees placeholders; rotation/removal really takes effect; PAT repository capability separately reported |
| 4. Project setup and configuration | Config builder, trust store, skills catalogue/lock/install, managed AGENTS zone, endpoint/model validation | Existing project config preserved; trusted selections effective in real OpenCode; repeated init produces no changes |
| 5. MCP and guest capabilities | Versioned guest provisioning or image pipeline; browser capability; MCP auth/health; resource settings | Each curated MCP works inside the guest; browser renders a real page; no host browser/profile dependency |
| 6. Updates, migration and DX completion | Reconciliation/update preview, legacy Albert import, installer UX, terminal renderer, diagnostics and docs | Interrupted setup resumes; updates preserve custom content; offline path works; fresh-user walkthrough validated on supported hosts |

Build a thin wizard prototype alongside phase 1, but keep decorative polish out of the critical path until the operations are correct. Deliver a usable minimal vertical slice after phase 3; do not advertise full parity until phase 5 acceptance passes.

### Suggested internal boundaries

Keep the current `internal/codebox` package initially and add focused files/modules around `ProjectContext`, configuration resolution, credentials, setup planning, OpenCode configuration, skills and MCP catalogue. Extract packages only when dependencies justify it. Keep `cmd/codebox` responsible for parsing/rendering, not filesystem reconciliation or credential policy.

Replace runtime-type-as-instance assumptions in `Backend` with project-bound instances and explicit capability reporting. Avoid building a plugin framework or a generic orchestration engine. Reuse existing SDK wrappers, integrity checks, workspace gates and boot-recovery tests.

### Effort and critical path

Planning range: **roughly 5-8 engineer-weeks** for one experienced Go engineer, including real-host verification and migration, not a delivery commitment. Configuration/project identity and credential transport dominate. A credential broker or new maintained guest-image pipeline can add 1-3 weeks; a guest-owned clone workspace model requires re-estimation rather than being absorbed as wizard work.

Parallelizable after the contracts settle: installer/terminal UX, skills/rules management, and guest browser validation. Credential transport and configuration precedence should not be deferred while the wizard is built.

## 6. Migration policy

1. Detect legacy `.env`, the existing singleton sandbox and Albert Code project artifacts. Show a redacted import proposal; do not execute their scripts.
2. Import recognized non-secret settings and credentials only with approval. Preserve sources until verification; flag a legacy plaintext file that remains inside the mounted workspace. Do not silently remove application `.env` files to pass the workspace gate.
3. Import selected skills, MCP intent and model settings from `.albert-code/skills.txt` and `opencode.json`. Preserve unknown connectors as custom configuration requiring review. Do not import the entire agent-vm runtime shell script.
4. Existing backend-mode guests do not become full-mode guests by relabelling them. Offer continued explicit backend use or a controlled migration to a new full-mode instance. Explain that the old guest's sessions/tooling do not transfer automatically.
5. Preserve personal skills and unmanaged AGENTS text. Migrate Albert-managed markers only after showing the diff; do not create two competing managed rule blocks.
6. Maintain legacy environment compatibility for a documented deprecation interval. New documentation uses zero-flag defaults and the new setup flow. Remove executable-adjacent `.env` discovery rather than carrying it indefinitely.

## 7. Verification and release gates

### Unit and contract tests

- Configuration precedence, unset vs explicit empty values, malformed input, schema migration and redacted provenance; help/version/stop work without an Albert key.
- Canonical paths, symlink aliases, same-name projects, worktrees, spaces, Unicode and Windows paths; no cross-project state collision.
- Repeat init/update produces no diff; malformed AGENTS markers fail safely; unmanaged text/config/skills survive; interrupted writes recover atomically.
- Reconciliation correctly classifies toggle/model/credential/resource/isolation changes; credential deletion removes authorization instead of merely hiding metadata.
- SDK fixtures derive from real persisted JSON. Test both typed options and actual serialization/FFI behavior where secrets, mounts and persisted environment differ.
- Trust checks for changed endpoints, MCP commands, lock revisions and cloned repositories; no network credential validation before endpoint approval.
- PTY transcript tests: resize, paste, back, cancel, narrow terminal, no-color, non-TTY failure, and exit-code propagation.

### Real integration tests

- macOS Apple Silicon and Linux/KVM for the complete default path; Windows/WHP for installation, credentials, guest launch and terminal behavior on both advertised architectures as capacity permits. Do not infer WHP correctness from Linux unit tests.
- Clean host with no Node or OpenCode installed: setup -> init -> bare launch -> model response -> file edit -> stop -> relaunch with retained project state.
- Albert, Context7 and GitHub keys absent from guest env/config/files and captured logs/argv; authorized calls succeed, unrelated hosts and redirect exfiltration fail. Use dedicated canary credentials, not production tokens.
- GitHub validation, private-repo access, HTTPS push and draft PR in a disposable repository; missing permissions, expired token and org approval failure have distinct outcomes.
- All four MCPs from the guest: data.gouv call, Context7 authenticated call, Playwright navigation/screenshot, Chrome DevTools DOM/console operation. A process that starts is not proof of a working browser connector.
- Credential rotation while running/stopped; add and remove a connector; network loss during provisioning; retry after guest-prep crash; cancellation during download.
- Two projects sequentially without destructive switching; optional concurrency only after port tests; preview exposure limited to intended interfaces.
- Existing project with personal MCPs, JSONC, AGENTS content and skills; confirm actual OpenCode effective config after each update.

Use existing `go test ./...`, race testing where supported and `go vet ./...`, plus platform-specific integration gates. Keep security scans/pre-commit checks. CI simulation is not a replacement for real microVM/browser/PAT evidence.

## 8. Decisions to settle before implementation

1. **Workspace boundary:** retain the current shared checkout for parity, or require guest-owned clones for stronger isolation? ~~Recommendation for this plan: retain it, clearly disclosed; never call it complete host-filesystem isolation.~~ **Settled 22 September 2026 (Luis):** guest-owned clone (the sealed model) becomes the architecture; the shared writable mount is removed rather than kept as an option. See the revision note in "What full isolation means here" and P22 of the PR-by-PR plan.
2. **Project sharing:** version the secret-free manifest and lockfile, or keep selections local like Albert Code? Recommendation: versioned by default, local-only option.
3. **Credential fallback:** permit an explicit protected-file fallback on headless hosts? Recommendation: yes, with clear storage disclosure and no silent downgrade.
4. **Browser guest:** keep Alpine if real MCP tests pass, otherwise adopt a pinned Debian-based image. Decide from the phase-0 test, not familiarity.
5. **Concurrency:** require simultaneous projects in the first parity release? Recommendation: distinct persistent guests first, one active guest initially; dynamic preview ports later.

**Bottom line:** the wizards are worthwhile, but they depend on project identity and deterministic configuration. Build those foundations first, preserve Microsandbox's stronger credential boundary, and make the default path genuinely `codebox`, not a shorter spelling of today's manual environment setup.
