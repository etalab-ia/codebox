# Native credential store qualification evidence

This directory holds the retained transcripts that qualify the native
credential store on each platform. It is the evidence half of the store
matrix in `docs/plateformes.md`; a platform is qualified there only when a
file here backs it.

## Credential transport matrix

Issue #205's P02 network matrix was run on a Scaleway Mac mini M4
(macOS 26.7.1, arm64) against both the bundled runtime and the current
upstream candidate:

- `darwin-arm64-msb-0.7.6.txt`: 16/16 assertions on the codebox runtime.
- `darwin-arm64-msb-0.7.8.txt`: 16/16 assertions on the newer upstream
  runtime, tested separately without changing the app's bundled version.

Both transcripts include T6 checks for the literal query placeholder and
substituted header. They contain no real credentials; the test uses a
synthetic canary. The detailed interpretation and version decision are in
`docs/decisions/2026-09-24-microsandbox-credential-transport.md`.

The v0.7.8 transcript predates the version-override option. It was captured
from a temporary test copy whose `pin.json` set `msb_cli_version` to `0.7.8`;
the Go SDK pin remained at v0.7.6. The runner now supports
`MSB_CLI_VERSION_OVERRIDE=0.7.8` with `MSB_BIN` pointing to the separately
installed v0.7.8 CLI, so the candidate check can be repeated without editing
`pin.json`. This checks CLI/runtime behavior only, not the v0.7.8 Go SDK.

## What is retained here

Three platform stores are qualified by transcripts in this directory, all
produced on 2026-10-16 against the qualified revision:

- macOS arm64, Keychain: `darwin-arm64-run-1.txt`, `darwin-arm64-run-2.txt`
  (dedicated keychain, unlocked for the run — a headless host cannot unlock
  the `login` keychain),
- Linux amd64, Secret Service: `linux-amd64-run-1.txt`,
  `linux-amd64-run-2.txt`,
- Linux arm64, Secret Service: `linux-arm64-run-1.txt`,
  `linux-arm64-run-2.txt` (real KVM host, Asahi Fedora 42).

Each pair shows `put` / `get` / `remove`, rotation, and removal of an
absent element, executed twice in a row. The `*-nosession.txt` transcripts
are the counter-experiments: on a headless Linux host with a locked
collection the round-trip test does not skip, it **fails**, because the
availability probe is read-only and reports the store "reachable" while
writes fail. Only a passing run on a genuinely usable store qualifies a
row. Windows Credential Manager has now been exercised on Windows Server 2025,
but is not qualified: see the mixed 2026-10-28 results below and #293.

## Windows Credential Manager, 2026-10-28

The source revision is `256e4233845aae324e924e7be6c73a3ef11c663b`, built
natively with Go 1.26.10 on a dedicated Elastic Metal Windows Server 2025
host. All three transcripts use only the repository's synthetic markers:

- `windows-amd64-ssh-skip.txt`: the probe is refused in a non-interactive SSH
  session, so both tests skip. The zero package exit is not qualification.
- `windows-amd64-interactive-run-1.txt`: the first interactive run reaches the
  real store but `Get` reports not-found after an accepted `Put`.
- `windows-amd64-interactive-run-2.txt`: a separate interactive run passes
  round-trip, rotation, and absent-removal checks.

The passing run demonstrates that the real service can be exercised; it does
not erase the failed run. The intermittent failure needs investigation before
Windows is marked qualified in `docs/plateformes.md` (#293).

An unretained run is indistinguishable from a run that never happened, which
is the same failure the phase-3 native kit guards against by binding every
transcript to a source revision through its `META` line.

## Producing a transcript

On a machine where the platform's store service actually answers:

```sh
go test ./internal/codebox/ -run TestRealStore -v -count=1 2>&1 | tee tests/integration/credentials/evidence/$(go env GOOS)-$(go env GOARCH)-run-1.txt
```

The command is the same on each platform — macOS needs `security`, Linux
needs a session `secret-tool`, and Windows needs PowerShell, which is the
executable the Windows adapter drives Credential Manager through. Under
PowerShell the `tee` pipeline above is not available; capture with
`... | Tee-Object -FilePath <name>` instead, or run it from Git Bash.

The tests self-skip when the store is unusable (no session keychain, no
D-Bus), so a transcript full of `--- SKIP` is not evidence of anything: it
records that the store was not exercised. A qualifying transcript contains
`--- PASS` for `TestRealStorePutGetRemoveRoundTrip` and
`TestRealStoreRemoveAbsentIsNotFound`, with real counts (`ok` with a
non-zero `N` of tests run), not skips.

## Binding a transcript to what ran

Each retained transcript must let a reader answer "what produced this?".
Record alongside it:

- the project revision (`git rev-parse HEAD` of the code that was tested),
- the platform and store (e.g. macOS arm64, Keychain, `security` from macOS
  27.0.1),
- the Go toolchain (`go version`),
- the date, and
- for a second run, the fact that it was a separate run rather than a copy of
  the first.

Two runs that are byte-identical must say so and explain why (the store
transcript contains fixed case names and a synthetic marker value, never a
real credential, so identical bytes are expected rather than suspicious).
