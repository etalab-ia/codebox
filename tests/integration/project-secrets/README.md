# Project Secret Loader Qualification

Phase 0 of the secrets plan (#145 / #148). These tests exercise real dotenv
loaders against proposed guest-visible placeholders. They contain no secrets
and neither start a microVM nor mock a successful proxy exchange.

## Run

From the repository root:

```sh
npm ci --prefix tests/integration/project-secrets --ignore-scripts
npm test --prefix tests/integration/project-secrets
uv run --no-project --with python-dotenv==1.2.4 python tests/integration/project-secrets/test_python_loaders.py -v
go test ./internal/justcode -run 'TestOpaqueProjectSecretMarkers|TestDollarProjectSecretMarker' -count=1
```

Node dependencies are isolated here, pinned in `package-lock.json`; Python is
isolated by uv. The Go test uses the repository's pinned dependency. No loader
imports host credentials: Node interpolation gets an empty `processEnv`, Python
tests clear the environment temporarily, and Go uses `Unmarshal` only.

## Observed Matrix

| Loader | Version | Opaque marker | Dollar marker |
|---|---|---|---|
| Node dotenv | 16.6.1 | Preserved, unquoted/single/double quoted | Preserved by parse alone |
| Node dotenv-expand | 13.0.0 | Preserved in all tested quote modes | Undefined `$MSB_...` erased, including after quoted parsing |
| Python python-dotenv | 1.2.4 | Preserved in all tested quote/interpolation modes | Bare `$MSB_...` preserved; `${MSB_...}` erased with interpolation |
| Go godotenv | 1.5.1 | Preserved in all tested quote modes | Unquoted undefined `$MSB_...` erased |

First measured 2026-10-14 on Node 16.4.7 / expand 12.0.2 / Python 1.2.2, and
re-measured 2026-10-25 on the pinned set above after the fixture bump: the
behaviour of every row is unchanged. The suites grew since the first run; Node
now reports 46 tests and Python 16, all passing. Two component fixtures use the
same application key with distinct markers.

Two majors were evaluated rather than applied blindly.

`dotenv-expand` 1000.0.0 (Dependabot #258 / #264 / #271 / #299) is **refused**. It imports
`child_process` and executes `$(...)` command substitution found in a `.env`
value in no, single *and* double quote modes, while 12.0.3 and 13.0.0 keep it
verbatim in all three. The guard tests named `dotenv-expand does not evaluate
command substitution` pin that property, so the refusal is enforced, not merely
recorded: `npm test` against 1000.0.0 fails tests 43/44/45, which turns the
`fixture-contracts` CI job red on any Dependabot PR to that version. The major
is additionally ignored in `dependabot.yml` so Dependabot stops proposing it
weekly: an `@dependabot ignore` comment is a no-op on a PR built from a group
rule (see #271), which is why the refusal kept returning. Versions below 1000
remain eligible; 13.0.0 keeps `$(...)` verbatim.

The reach of that hazard into just-code's own delivery is narrow, and the
reason is worth keeping straight. `dotenvLiteral` refuses `$` in *both* the
quoted and the unquoted form, so a guest-readable value containing `$(...)`
cannot be written into a generated guest file at all — the approval refuses it
while the user is still present (`CheckGuestReadableValue`). The other thing
these files carry is the opaque placeholder for secrets the guest may not read,
`__JUST_CODE_SECRET_<16 hex>__`, which contains no dollar sign. The
exposure this pin guards is the other side of the same file format: a
guest-authored project `.env`, read by an application that has adopted an
expanding loader, where an unquoted `$(...)` becomes code execution. That is a
property of the dependency version, so the fixtures qualify it.

`dotenv-expand` 13.0.0, applied by Dependabot #324, passes all 46 Node tests
and carries no OSV advisory. The Python suite is not evidence about it: it
loads `python-dotenv`, so its 16 tests qualify the applied `python-dotenv`
1.2.4 pin only. The native macOS arm64 run predates this upgrade and remains
tied to its recorded loader versions.

`dotenv` 17.4.2 and `dotenv` 18.0.8, as offered by Dependabot #254 / #262,
also pass all 46 Node tests and carry no OSV advisory. They remain deferred
until a native guest qualification re-run covers them; the `dotenv` direct pin
stays at 16.6.1 while `dotenv-expand` 13.0.0 resolves its own 17.4.2
dependency in the lockfile.

## Design Consequence and Remaining Gate

Project bindings should use a stable opaque marker with no dollar references,
for example `__JUST_CODE_SECRET_<opaque-id>__`. Quote selection alone is not a
portable solution. This is a proposal, not a newly enabled runtime binding.

The current Microsandbox runtime/SDK v0.7.13 passed the native canary matrix
twice on macOS arm64. See [the native runner and evidence](native/README.md)
for custom opaque markers (raw and Basic), shared destinations, independent
per-binding authorization, forbidden hosts/redirects, rotation, removal,
re-add/restart and reference persistence. Applications that sign or transform
tokens locally remain unsupported without separate evidence.

The P02 credential-transport laboratory is now pinned to msb 0.7.6 and its
network matrix was requalified 16/16 on macOS (see #205). That result covers
P02 transport only; it does not qualify this project-secrets loader matrix or
these new cases. Do not call Phase 0 complete from loader tests alone. This
loader matrix ran in Cloud; the native matrix ran separately on an explicitly
connected Mac. Neither matrix alone proves the later Phase 2/3 just-code
application integration.
