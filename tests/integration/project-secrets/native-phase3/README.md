# just-code Phase 3 native qualification kit

Qualifies the **project-secrets Phase 3 end-to-end path** of
[`etalab-ia/just-code`](https://github.com/etalab-ia/just-code) at commit
`f483a8003d7745dfe3cdea7363d5f366af51eebc` (branch
`letta/project-secret-guest-files-46ccf2e8`) on **native macOS arm64** with
the pinned **Microsandbox 0.7.6** runtime and Go SDK.

Everything this kit uses is synthetic: two random canaries per run, a throwaway
VM named `jc-p3-<random>`, a one-day temporary CA that is never installed into
any trust store, and an own-port DNS/TLS lab. It never touches
`~/.microsandbox`, an installed `msb`, the host trust store, host DNS, or host
interfaces, and it refuses to start if its ports are occupied (it never kills
another owner).

## What a passing run proves

1. **Same variable name, two components.** The REAL `BuildGuestSecretFiles`
   renders two deliveries that both define `API_KEY` — one in component
   `.env`, one in `apps/api/.env` — each with its own derived dollar-free
   marker (`ProjectSecretPlaceholder`) and its own guest variable
   (`JC_API_KEY` / `JC_APPS_API_API_KEY` from `ProjectSecretGuestEnv`).
2. **One VM, faithful registration.** ONE Microsandbox 0.7.6 VM boots with
   inert sentinels plus the custom markers and per-binding destination lists,
   then rotates to host env references under `ModificationPolicyNoRestart` —
   exactly the pinned runtime's create-then-rotate sequence.
3. **Atomic publish.** The REAL `syncGuestSecretFiles` pipeline
   (`readGuestSecretFiles` → `PlanGuestSecretPublish` →
   `publishGuestSecretFiles`) writes both files into the guest at their
   `GuestPath` (`/workspace/.env`, `/workspace/apps/api/.env`) via temp file +
   `mv`, over the same SDK calls just-code's `sdkMSBClient` makes
   (`Shell` / `FS().Write`). The probe asserts byte-exact content, marker-only
   values, no canary on disk, and no leftover temp files.
4. **Real guest apps, real loaders, real substitution.** Inside the guest,
   a Node app using **dotenv@16.6.1 + dotenv-expand@13.0.0** and a Python app
   using **python-dotenv==1.2.4** each read their dotenv file and send the
   value as an HTTPS header to the lab. The probe asserts each app parsed the
   FILE's marker (not its environment), and that the lab received the
   substituted REAL canary for BOTH components — while the guest files still
   hold only markers.
5. **No bleed.** The two components' markers, canaries and delivered header
   values are all distinct and correctly paired; a request to a destination no
   binding approved (`evil.test`) fails closed with no lab record.
6. **Unit-level safety (cloud-runnable, `go test`).**
   - A target file just-code did not write is refused by
     `PlanGuestSecretPublish` (conflict, no write) and
     `publishGuestSecretFiles` refuses to apply the plan — the foreign file
     survives.
   - Withdrawing an approval removes the managed file (guest `rm`), while a
     file the user replaced with their own content is never removed.
   - Plus: marker derivation shape/determinism, unsafe component rejection,
     managed-header detection, and the approval
     persist/approve/revoke round-trip through the REAL
     `Write/ReadProjectSecretBindings`.

## Vendoring — what "real code" means here

Importing `internal/justcode` directly is not possible from a standalone kit
(Go's `internal/` rule), and the Phase 3 functions are unexported. So the kit
carries the two source files **verbatim**:

- `jcvendor/project_secret_guest_files.go`
- `jcvendor/project_secret_bindings.go`

Each is byte-identical to `internal/justcode/<name>` at the pinned commit
except the first line (`package justcode` → `package jcvendor`).
`verify_vendored.py` re-fetches both upstream files from GitHub at the pinned
commit and **aborts the run on any drift**; it is a hard preflight in
`run.py`.

The vendored files reference symbols from elsewhere in just-code. Those are
supplied by `jcvendor/support.go`, whose header documents every entry. In
short:

- **EXACT COPY** (comments included, from the pinned commit; exercised by
  tests): `shellQuote`, `isDNSName`, `validSecret{Identifier,Profile,Component}`,
  `isDotenvName`/`IsProjectDotenvName`/`dotenvSafeSuffixes`, the `FS`
  interface + `realFS` + `DefaultFS` + `atomicWrite`, `msbSecretBindings`
  with its constants and `msbSecretBinding`/`ProjectBindingRef`,
  `ProjectSecretEntry.identity`, `projectSecretIndex`, `msbGuestWorkspace`,
  and the `guestShell` body. The `GuestClient` interface's three method
  signatures are exact copies of the corresponding `msbClient` entries.
- **KIT STUB** (never executed by any assertion; each fails loudly if called):
  credential-store resolution — `DefaultCredentialStore` (returns nil, which
  the vendored callers turn into a loud failure via `NewFileCredentialStore`),
  `NewFileCredentialStore`, `NewProjectSecretStore`,
  `ProjectSecretStore.readIndex`. Phase 3 consumes *deliveries*; the kit
  constructs them directly with the vendored `ProjectSecretPlaceholder`, the
  same derivation the real `Deliveries` performs.
- **Exported wrappers** (`SyncGuestSecretFilesForTest`,
  `IsManagedGuestSecretFileForTest`, `GuestWorkspaceDotenvPathForTest`,
  `NewGuestRuntimeForTest`) forward verbatim so the package-main probe can
  drive the unexported pipeline.

## Layout

```
README.md               this file
run.py                  native runner: preflight, runtime, CA, lab, probe, report, cleanup
verify_vendored.py      hard preflight: vendored files == pinned commit (minus package line)
runtime-download.py     pinned official darwin/arm64 0.7.6 runtime, SHA256-verified
lab.py                  own-port DNS/TLS lab; persists SHA256 digests only
test_lab.py             lab DNS unit tests (cloud-runnable)
main.go                 native probe (VM lifecycle, publish, guest apps, assertions)
main_test.go            probe unit tests (cloud-runnable)
jcvendor/               vendored just-code sources + support.go + unit tests
guest/node-app/         Node fixture app + pinned package.json/package-lock.json
guest/python-app/       Python fixture app + pinned requirements.txt
go.mod / go.sum         module pinned to microsandbox SDK v0.7.6
```

## Preflight (on the Mac)

- macOS on Apple Silicon (the kit refuses anything else), with network access.
- `go` (1.25+), `curl`, `openssl`, `python3` (3.11+) on PATH. **No Node, npm
  or pip needed on the host**: the guest installs its own throwaway packages
  (`apk add nodejs npm`, `npm ci`, `pip install`) from public registries
  through the lab's DNS relay.
  - If `go` is managed by `proto` and the shell reports
    `proto::detect::failed`, there is no version pinned for that directory:
    either `proto pin go <version>` inside the kit, or put the installed
    toolchain on PATH directly (e.g.
    `export PATH="$HOME/.proto/tools/go/1.27.1/bin:$PATH"`). This is a host
    quirk, not a kit failure.
- The machine's **LAN IPv4** (e.g. from `ipconfig getifaddr en0`).
- Ports `19443/tcp` and `15356/udp` free (defaults; override with `--tls`/`--dns`).
- The guest pulls the `python:3.12-alpine` image from Docker Hub on first run.

## Run

```sh
cd just-code-native-phase3-kit
go test ./...                       # unit-level qualification (runs anywhere)
python3 test_lab.py                 # lab DNS unit tests (runs anywhere)
python3 verify_vendored.py          # vendored sources match the pinned commit
python3 run.py --ip <LAN IPv4>      # e.g. --ip 192.168.1.42
```

The run needs the fixture directories to exist inside the guest; the probe
creates them itself (`mkdir -p`) before writing, because the sandbox write API
does not create parents. A first run that fails at `guest-fixtures-pushed` with
a bare "No such file or directory" means that step was skipped — do not
"fix" it by weakening the assertion.

Optional flags: `--tls <port>`, `--dns <port>`,
`--dns-upstream <resolver IPv4>` (default: the host's first configured
nameserver, else `1.1.1.1`).

`run.py` exits 0 only if every case passes; the sanitized report is written
to `results.txt` (one `PASS`/`FAIL <name>` per case plus a `META` line with
the kit revision, source commit, SDK/runtime versions, runtime archive
SHA256 and Go version). Case names:

```
create-inert-sentinels          rotate-to-envrefs            exact-guest-env-markers
published-managed-paths         root-file-exact-marker-only  api-file-exact-marker-only
no-temp-file-left               guest-fixtures-pushed        node-runtime-and-loaders-installed
python-loader-installed         node-parses-file-marker      python-parses-file-marker
node-header-substituted         python-header-substituted    components-do-not-bleed
evil-denied-failclosed          persisted-envrefs-no-raw-canary
no-host-mounts                  cleanup-own-vm
```

## LAN exposure (read before running)

During the run (≤ 15 minutes, hard deadline), the lab listens on
`0.0.0.0:<tls>` and `0.0.0.0:<dns>` so the VM can reach it via the host's LAN
IP. That makes both ports reachable from other machines on the same network
segment. The DNS side answers only the two `.test` names (everything else
`.test` is REFUSED; public names are relayed to the upstream resolver), and
the TLS side serves empty 200 responses while recording SHA256 digests of
header values only. Run on a trusted network. All listeners close when the
run ends, on failure, and on Ctrl-C.

The guest's DNS is pointed exclusively at the lab; public lookups (for the
guest's own `apk`/`npm`/`pip` installs) are relayed verbatim to the upstream
resolver. Host DNS is never read beyond `/etc/resolv.conf` for the relay
default, and never modified.

## Evidence and privacy

### Recorded native run (2026-10-15)

`python3 run.py --ip <the host's LAN IPv4>` was run twice on macOS arm64
(Apple Silicon, macOS 27.0.1, Go 1.27.1) against the landed merge commit
`f483a8003d7745dfe3cdea7363d5f366af51eebc`. **All 19 assertions passed
both times**, with the byte-identical sanitized transcript in
[`evidence/`](evidence/README.md):

```
PASS create-inert-sentinels              PASS node-parses-file-marker
PASS rotate-to-envrefs                   PASS python-parses-file-marker
PASS exact-guest-env-markers             PASS node-header-substituted
PASS published-managed-paths             PASS python-header-substituted
PASS root-file-exact-marker-only         PASS components-do-not-bleed
PASS api-file-exact-marker-only          PASS evil-denied-failclosed
PASS no-temp-file-left                   PASS persisted-envrefs-no-raw-canary
PASS guest-fixtures-pushed               PASS no-host-mounts
PASS node-runtime-and-loaders-installed  PASS cleanup-own-vm
PASS python-loader-installed
```

Both transcripts carry the probe's own META line naming the kit revision and
the source commit, so a recorded run is always tied to a revision of the
harness:

```
META kit=p3v2 source=f483a8003d7745dfe3cdea7363d5f366af51eebc sdk=0.7.6 runtime=0.7.6 \
     archive_sha256=a4f1722fee6460d4e2813e62b60dae4c092021d8120b7aaa9c2f4b5679d97d89 go=go1.27.1
```

The two cases that carry the phase-3 claim are `node-header-substituted` and
`python-header-substituted`: a real Node application and a real Python
application each read their own component's generated file, took the marker
from it, and issued the HTTPS request — and the lab recorded the substituted
canary. `components-do-not-bleed` proves two components can define the same
variable name without mixing.

- Only synthetic random canaries are used (two per run). They live in the
  runner process environment and the throwaway VM, never in files under the
  kit, and are scrubbed from all probe diagnostics (including base64 forms
  and authorization-shaped strings).
- The lab persists **SHA256 digests** of header values only, inside the run
  directory, which is deleted on cleanup. `results.txt` contains case names,
  `DIAGNOSTIC` records (scrubbed) and the `META` line — no canaries.
- `~/.microsandbox` is fingerprinted recursively (names, sizes, mtimes) before
  and after; any change fails the run (`FAIL host-msb-state-touched`).
- The transcript must contain the probe's own `META` line, which names the kit
  revision and the source commit; a run whose META line is missing or filtered
  out fails (`FAIL probe-meta-line-missing`) instead of producing evidence that
  cannot be tied to what ran.

## Cleanup

Everything is bounded and self-cleaning: the runner has a 15-minute wall
clock, the probe a 14-minute context, the VM a 12-minute `MaxDuration`, and
each guest call its own timeout. On completion, failure, or SIGINT/SIGTERM
the runner stops the lab, removes only its own `jc-p3-*` VM (identified by a
name file in the run directory; `--cleanup` refuses any other name), and
deletes the run directory (`/tmp/jcp3-*`). If the machine died mid-run, a
later run's leftover VM can be removed with
`MSB_HOME=<run-dir>/msb-home MSB_PATH=<run-dir>/runtime/msb <run-dir>/probe -cleanup -dir <run-dir>`.

## Honest limits

- **macOS arm64 only.** The pinned runtime archive is darwin/aarch64; there
  is no Linux or Intel qualification here.
- **Guest apps are fixtures.** They are minimal real apps (one file, pinned
  loaders, one request) that prove the marker→value path; they are not
  just-code's own guest tooling. The dotenv files, loaders and network path
  are real; the "app" is a stand-in for whatever the project runs.
- **Not covered:** installing the temporary CA into the *guest* trust store
  (the fixtures disable client verification, like the Phase 0 guest, because
  the proxy — not the app — verifies upstream), and profiles other than the
  single `development` profile with per-binding lab-only destination lists
  (e.g. public-network or multi-profile approval matrices).
- The DNS relay exists only so the disposable guest can fetch its throwaway
  packages; it is not a correctness target of the qualification.
- Unit tests and cloud checks run anywhere; **substitution itself is proxy
  behavior and is only qualified by the native macOS run.**
