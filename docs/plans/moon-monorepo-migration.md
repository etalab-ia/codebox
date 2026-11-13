# codebox: Moon v2 and `go.work` Monorepo Migration Plan

Date: 2026-11-13 | Tracking issue: [#376](https://github.com/etalab-ia/codebox/issues/376) | Baseline: `main` at `300311e8` (`codebox-v0.17.1`)

## Summary

Migrate `etalab-ia/codebox` from a single root Go module to a **Moon v2 + Go workspace (`go.work`) multi-module monorepo** ahead of adding deployable backend services (starting with `secrets-api`) and shared Go libraries (`packages/*`).

The migration runs as a three-PR stack so each review boundary stays small and independently verifiable:

1. **PR 1 (`monorepo/moon-scaffold`):** Toolchain pins (`.prototools`, `.protolock`), Moon v2 workspace and inherited Go task configuration (`.moon/*`), CI workspace validation, and this migration plan. Zero Go file moves.
2. **PR 2 (`monorepo/move-codebox`):** Relocate the CLI module, embedded assets, and CLI test/qualification suites into `apps/codebox/` with a root `go.work`, keeping public installer URLs and all native CGO/fixture gates intact.
3. **PR 3 (`monorepo/release-please-split`):** Cut `release-please` over from root `"."` to package-scoped `"apps/codebox"` while preserving version `0.17.1` and the `codebox-vX.Y.Z` tag contract.

## Target Repository Layout

```text
codebox/
├── .moon/
│   ├── workspace.yml          # Moon v2; globs ['apps/*', 'packages/*']; telemetry: false
│   ├── toolchains.yml         # Moon v2; go: { workspaces: true } (inherits version from .prototools)
│   └── tasks/
│       └── go.yml             # Inherited Go tasks (fmt-check, vet, test) with transitive input hashing
├── .prototools                # Exact pins: go = "1.26.10", moon = "2.6.5"
├── .protolock                 # Multi-platform SHA-256 checksums for Go and Moon
├── go.work                    # go 1.26.0 / toolchain go1.26.10; use ( ./apps/codebox )
├── apps/
│   └── codebox/               # CLI module and Moon project (id: codebox)
│       ├── moon.yml           # layer: application, language: go, stack: systems
│       ├── go.mod             # module github.com/etalab-ia/codebox/apps/codebox
│       ├── go.sum
│       ├── CHANGELOG.md
│       ├── .env.example
│       ├── assets/            # Moved with assets.go (go:embed forbids '..' paths)
│       ├── cmd/codebox/
│       ├── internal/codebox/  # Strictly private to apps/codebox
│       └── tests/             # CLI installer, OpenCode, credentials, project-secrets & qualification suites
├── packages/                  # Created only when shared libraries have real consumers (e.g. secrets, scaleway)
├── docs/                      # Repository documentation, ADRs, and implementation plans
└── scripts/                   # Public install.sh / install.ps1 (kept at root for raw GitHub URL stability)
```

### Testing Tiers

- **Unit tests:** idiomatic Go `*_test.go` files colocated inside each `apps/<name>/` and `packages/<name>/` module.
- **Project integration and qualification suites:** `apps/<name>/tests/`, owned and input-tracked by that project's `moon.yml`. Relocating the existing 223 files under `tests/` to `apps/codebox/tests/` keeps `tests/integration/opencode/configcompose/main.go` inside the `apps/codebox` module root (so Go's `internal/` visibility rule continues to permit importing `apps/codebox/internal/codebox`) and preserves relative geometry from `internal/codebox/project_secret_loader_contract_test.go` (`../../tests/...`) and `tests/integration/opencode/run.sh` (`$HERE/../../..`).
- **Cross-service end-to-end suites:** no top-level test directory is created during the initial migration. Once `secrets-api` exists and tests exercise the `codebox` CLI against a running `secrets-api` process, a top-level `e2e/` directory is added and registered in `.moon/workspace.yml` as a `layer: automation` project with `dependsOn: [codebox, secrets-api]`.

## Source-Verified Migration Invariants

1. **Historical qualification kits remain excluded from `go.work`.**
   The three nested Go modules under `apps/codebox/tests/integration/project-secrets/{native,native-phase3,native-phase4}` intentionally pin distinct SDK/Go baselines (`native-phase3` and `native-phase4` pin `go 1.25.0` and `github.com/superradcompany/microsandbox/sdk/go v0.7.6` as historical qualification kits, while `apps/codebox` and `native` pin `v0.7.14`). Including them in `go.work` would unify Minimal Version Selection across the workspace and invalidate those pinned contracts. Because each kit carries its own `go.mod`, `go test ./...` inside `apps/codebox` skips them automatically, and the `fixture-contracts` CI job continues to execute them explicitly.

2. **Public installer URLs remain at the repository root.**
   `README.md` and `docs/installation.md` instruct users to run `https://raw.githubusercontent.com/etalab-ia/codebox/main/scripts/install.sh` and `scripts/install.ps1`. Keeping `scripts/` at the root preserves those URLs; `apps/codebox/tests/installers/test-posix.sh` and `test-windows.ps1` update their `repo_root` lookup from `../..` to `../../../..`.

3. **Linker `-X` symbol paths move with the module path.**
   `.github/workflows/release-please.yml` stamps `StatsEndpointDefault` via `-ldflags "-X ..."`. Go's linker does not error when an `-X` package path fails to match a compiled symbol; it silently leaves the variable unset. PR 3 updates the symbol target to `github.com/etalab-ia/codebox/apps/codebox/internal/codebox.StatsEndpointDefault` alongside the package cutover.

4. **Dual `go.work` and `replace` compatibility for shared packages.**
   When `packages/*` modules are introduced alongside `apps/secrets-api`, `go.work` resolves local modules during workspace builds while consumer `apps/*/go.mod` files also carry relative `replace` directives (for example `replace github.com/etalab-ia/codebox/packages/secrets => ../../packages/secrets`). Standalone module consumers (`GOWORK=off`, OSV-Scanner, Dependabot) continue to resolve local packages without relying solely on `go.work`.

5. **Transitive Moon task invalidation and Windows shell safety.**
   In Moon v2, project `dependsOn` edges alone do not add upstream source files to task hashes, and task dependencies without `outputs` default to `cacheStrategy: "ignored"`. `.moon/tasks/go.yml` therefore pairs `project: "^"` (`group: "sources"`) with `deps: [{ target: "^:vet", cacheStrategy: "hash" }]` (and `"^:test"`), propagating changes across direct and transitive Go libraries. POSIX shell checks (`fmt-check`) restrict `options.os` to `["linux", "macos"]` so Moon's default PowerShell shell on Windows is never handed POSIX syntax, and `moonrepo/setup-toolchain` runs on Linux while Windows/macOS native CGO jobs retain `actions/setup-go` (Moon v2.6.5 publishes no native `windows-arm64` archive).

6. **Security and secret-hygiene boundaries.**
   Moon is strictly a task graph and build orchestrator, never a secret store: `telemetry: false`, no remote cache, no generated VCS hooks overwriting Gitleaks pre-commit, no implicit `envFile` loading, and no credential environment variables in task `inputs` or cached outputs. Existing Gitleaks, OSV-Scanner, 7-day Dependabot cooldowns, signed commits, and native platform gates remain mandatory throughout the migration.

## Stacked PR Breakdown

### PR 1: Toolchain, Moon v2 Scaffold, and Migration Plan (`monorepo/moon-scaffold`)

- Add `docs/plans/moon-monorepo-migration.md`.
- Add `.prototools` (`go = "1.26.10"`, `moon = "2.6.5"`, `telemetry = false`, `detect-strategy = "prefer-prototools"`, `unstable-lockfile = true`) and multi-platform `.protolock` covering `linux/{x64,arm64}`, `macos/arm64`, and `windows/{x64,arm64}`.
- Add `.moon/workspace.yml`, `.moon/toolchains.yml` (`go: { workspaces: true }`), and `.moon/tasks/go.yml` against `/schemas/v2/...` schemas.
- Ignore `/.moon/cache/` in `.gitignore`.
- Add a Moon/proto validation step to the `check` job in `.github/workflows/ci.yml` (`moonrepo/setup-toolchain@261c62cb5b0f580c7be7c8cd0f023a2e96756095`, `proto install --immutable-lockfile` pin alignment check against `go.mod`, `moon sync config-schemas`, `moon query projects`, `moon query tasks`).

### PR 2: Relocate CLI into `apps/codebox` with `go.work` (`monorepo/move-codebox`)

- Move `go.mod`, `go.sum`, `cmd/`, `internal/`, `assets/`, `tests/`, `.env.example`, and `CHANGELOG.md` into `apps/codebox/` with `git mv`.
- Rename module and internal imports to `github.com/etalab-ia/codebox/apps/codebox`.
- Create root `go.work` (`go 1.26.0`, `toolchain go1.26.10`, `use ./apps/codebox`).
- Add `apps/codebox/moon.yml` (`id: "codebox"`, `language: "go"`, `layer: "application"`, `stack: "systems"`, appending `assets/**/*` and `tests/**/*` to project `sources` and defining the `build` task).
- Update `apps/codebox/tests/installers/test-{posix.sh,windows.ps1}` root path resolution (`../../../..` to reach `/scripts/install.*`).
- Update `.github/workflows/ci.yml`, `.github/workflows/msb-runtime-watch.yml`, `.github/dependabot.yml` (`directory: "/apps/codebox"` preserving the `microsandbox/sdk/go` ignore rule, plus updated `/apps/codebox/tests/...` fixture directories), `.gitignore`, `docs/development.md`, and `docs/installation.md`.
- Verify `moon query projects` discovers `codebox` and run `moon run codebox:fmt-check codebox:vet` plus full `-race`, native CGO Linux/macOS/Windows builds, installer suites, OpenCode host integration, fixture-contracts, and OSV-Scanner.

### PR 3: Multi-Package `release-please` Cutover (`monorepo/release-please-split`)

- Update `.release-please-manifest.json` from `{".": "0.17.1"}` to `{"apps/codebox": "0.17.1"}`.
- Update `release-please-config.json` package key from `"."` to `"apps/codebox"` with `"component": "codebox"` and `"changelog-path": "CHANGELOG.md"` so release tags remain `codebox-vX.Y.Z` and installer scripts require no tag-pattern changes.
- Update `.github/workflows/release-please.yml` to read path-scoped action outputs (`steps.release.outputs['apps/codebox--release_created']` and `steps.release.outputs['apps/codebox--tag_name']`, retaining the `workflow_dispatch` tag path), build `./apps/codebox/cmd/codebox`, run `go -C apps/codebox test ./...`, and stamp `-X github.com/etalab-ia/codebox/apps/codebox/internal/codebox.StatsEndpointDefault`.
- Exercise the release artifact build and checksum hand-off via `workflow_dispatch` against `codebox-v0.17.1`.

## Adding Deployable Services Next (`secrets-api`)

Once PRs 1-3 are merged, introducing `secrets-api` and shared libraries requires no further structural churn:

1. Add shared libraries under `packages/secrets` and `packages/scaleway` (`layer: "library"`) and register them in `go.work`.
2. Add `apps/secrets-api` (`layer: "application"`, `stack: "backend"`, `dependsOn: ["secrets", "scaleway"]`), including `/deploy/secrets-api/**/*` in its task inputs and setting `options.cache: false` on any externally stateful deploy or live qualification task.
3. Add `"apps/secrets-api"` to `release-please-config.json` starting at `0.1.0` (`bump-minor-pre-major: true`, `component: "secrets-api"`, tag series `secrets-api-vX.Y.Z`) with an independent Linux container build/publish job gated on `steps.release.outputs['apps/secrets-api--release_created']`, leaving CLI desktop builds untouched on API-only releases.
4. Unify all three CLI project-secret value-read paths in `apps/codebox/internal/codebox/microsandbox.go` (launch bindings, guest-readable dotenv files, and `Recover` compatibility checks) behind one remote-capable resolution set while keeping host-only destination approvals non-versioned and keeping local OS-store size limits (`951` bytes) separate from Scaleway's `64 KiB` payload limit.
