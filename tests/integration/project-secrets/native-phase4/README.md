# codebox Phase 4 — native Microsandbox channel probe (TEST ARTIFACT ONLY)

This kit is the **evidence-gathering step** for codebox issue #151 (Phase 4:
"deliver a guest-readable value with separate, explicit consent"). It does not
touch the codebox codebase and implements no product behavior. It answers
one question on a real native macOS arm64 machine, with the pinned official
Microsandbox 0.7.6 runtime and Go SDK:

> Does a channel exist that hands a running guest a plaintext value the guest
> must consume **locally** (a DB password, a signing key) while satisfying
> ALL of the five criteria below?

The five criteria (from the issue):

1. **argv** — the value never appears in host or guest command lines
   (`ps`, `/proc/*/cmdline`).
2. **logs** — the value never appears in any host log, guest log, the
   persisted Microsandbox runtime log, or any runtime journal.
3. **persisted runtime config** — the value never lands in the persisted
   runtime state (`msb.db`, saved sandbox config, `msb inspect` output).
4. **host plaintext files** — the value never exists in a plaintext file on
   the HOST (temp files, run-dir leftovers, guest disk images).
5. **guest readability** — exactly who inside the guest can read the value,
   from where, and for how long, is measured and stated, not assumed. (The
   agent being able to read it is the point; the question is who else can,
   and for how long.)

A channel that fails any criterion is **disqualified**, and if no channel
qualifies the honest outcome is "Phase 4 blocked until the runtime grows a
suitable channel" — that is a valid result of this probe, and the transcript
is built to show it plainly.

## Channels under test

Each channel gets its own synthetic random 48-hex canary, generated inside
the probe; the runner never sees any of them. Diagnostics report leak
**locations** (paths, pids, classes) — never values.

| ID | Channel | Mechanism |
|----|---------|-----------|
| A  | disk `FS().Write` | SDK file write to `/workspace/jcp4/local-secret` (disk-backed upper) |
| B  | tmpfs `FS().Write` | SDK file write to `/jcram/tmpfs-secret` — an 8 MiB RAM-backed `tmpfs` mount (`noexec,nosuid,nodev`) |
| E1 | stdin → tmpfs file | `ExecStream` with `WithExecStdinPipe`, guest `cat` writes `/jcram/stdin-secret` |
| E2 | stdin → memory | `ExecStream` with `WithExecStdinPipe`, a Python reader keeps the value in process memory and prints only its SHA256 |
| C  | **negative control: argv** | value embedded in a `Shell`-style command line (`sleep 300 # jcp4argv-<canary>`) |
| D  | **negative control: persisted inline** | inline `Secret.Env` value at create time — persisted runtime config by design |
| F  | **negative control: per-exec env** | value in `WithExecEnv` for one `sleep 300` |

The controls exist to prove the scans can actually see a leak: if C is not
found in guest cmdline, or F in guest environ, or D in the persisted config,
the "clean" results for the real channels mean nothing, and the run fails.

## What each PASS asserts

Delivery and readability (criterion 5):

- `deliver-disk-write`, `deliver-tmpfs-write`, `deliver-stdin-file`,
  `deliver-stdin-memory` — the value arrived AND a guest process read it
  back (verified by an in-guest SHA256, so the value itself never crosses
  exec stdout) AND an `FS().Read` byte-compare matched.
- `chmod-0600-achievable` — after `chmod 0600`, uid 65534 can no longer read
  the delivered files while root still can. A `DIAGNOSTIC delivery-modes`
  record states the as-delivered modes (the SDK's `FS().Write` takes no
  mode), and `DIAGNOSTIC prechmod-nobody-read` states whether a non-root
  guest process could read them before the chmod — that is the honest
  criterion-5 answer about the default posture. `DIAGNOSTIC rewrite-mode`
  states whether rewriting a 0600 file via `FS().Write` preserves the mode
  (a rotation-design fact).

Criterion 1 (argv):

- `argv-scan-finds-control` / `environ-scan-finds-control` — the in-guest
  scanner demonstrably catches the C/F controls in `/proc/*/cmdline` /
  `/proc/*/environ`. (The search values reach the scanner through a 0600
  file on the guest tmpfs, never through its own argv or environment, so it
  cannot match itself.)
- `argv-clean-for-delivered` / `environ-clean-for-delivered` — no delivered
  canary (A, B, E1, E2; plus D and the cross-form of F/C) appears in any
  guest cmdline/environ.
- `host-argv-clean` — no canary appears in host `ps -axo args`.

Criterion 2 (logs):

- `runtime-log-clean-for-delivered` — the persisted runtime log (all four
  SDK sources: stdout, stderr, output, system) contains none of A, B, D,
  E1, E2, F. Whether it captures the argv control C is reported separately
  as a measurement (`DIAGNOSTIC runtime-log-captures-argv`) — that tells you
  the argv channel ALSO fails criterion 2, not just criterion 1.
- `guest-varlog-clean` — no canary in any guest `/var/log` file.
- `host-oslog-clean` — no canary in the macOS unified log under the probe
  process (the FFI runtime logs as this process).
- The recursive host scan (below) classifies every hit; any canary in a
  `log`-class file outside its allowance fails `host-files-clean-for-delivered`.

Criterion 3 (persisted runtime config):

- `persisted-config-holds-control` — the inline D value IS in the saved
  sandbox config (control: the scan can see persisted leaks).
- `persisted-config-clean-for-delivered` — none of A, B, C, E1, E2, F is in it.
- `host-persisted-store-shows-control` — the host byte scan finds D in a
  `store`-class file, confirming on disk what the API showed.

Criterion 4 (host plaintext):

- `host-files-clean-for-delivered` — recursive byte scan of the whole run
  directory (which contains `MSB_HOME`, the runtime, and the guest disk
  image): every canary appears only in its allowed classes —
  A: `disk-image`, `snapshot`; B/E1/E2: *none, anywhere*; C/F: `log` at most;
  D: `store` (+ the disk image the runtime writes it into).
- `tmpfs-never-on-host` — across scans before AND after snapshot creation,
  B/E1/E2 have zero host hits of any class.
- `host-files-clean-after-teardown` — after VM and snapshot removal,
  B/E1/E2 are still nowhere and C/F have no disallowed hits.
- `disk-value-removed-with-vm` — once the VM is removed, A's value is gone
  from the host too (the runtime deletes the disk image with the sandbox).

Lifecycle (criterion 5's "how long"):

- `removal-reported-gone`, `removal-survives-restart` — `FS().Remove` makes
  the disk value vanish and it does not come back across a VM restart.
- `tmpfs-cleared-on-restart` — a restart wipes the tmpfs content;
  `tmpfs-mount-persists-across-restart` — but the mount itself comes back.
- `snapshot-created`, `snapshot-restored` — the snapshot machinery worked
  (if it cannot run, whether a snapshot captures a delivered value is
  unanswered and the run fails rather than implying safety). The value is
  re-delivered before the snapshot on purpose: this measures what a snapshot
  does to a value that is live when it is taken, not whether a deleted value
  comes back. It does not.
- `snapshot-excludes-tmpfs` — the tmpfs value is NOT present in a VM restored
  from a snapshot taken while it was delivered: a RAM-backed delivery is not
  captured by a snapshot.
- `DIAGNOSTIC snapshot-captures-delivered-value` — whether a value delivered
  to the disk IS present in a VM restored from a snapshot taken while it was
  live. Either way it is reported plainly: `true` means a snapshot holds a
  copy of the value that no later in-guest deletion can revoke, which is
  ordinary snapshot behaviour and a lifecycle rule for the delivery contract
  (revoking a secret does not reach snapshots already taken); `false` means
  the pinned runtime did not flush the write into the snapshot (the SDK's own
  tests flag stop-flush as not yet guaranteed) — also
  reported, with `DIAGNOSTIC snapshot-captures-disk-value` stating whether
  the value's bytes are in the host-side snapshot files.

Ownership and hygiene (same contract as the Phase 3 kit):

- `create-vm`, `restart-succeeded`, `guest-fixtures-pushed`,
  `guest-dirs-no-leftovers`, `guest-tmpfs-no-leftovers`,
  `tmpfs-mount-present`, `cleanup-own-vm`.
- Runner-side: `host-msb-state-touched` (the tester's real `~/.microsandbox`
  is fingerprinted before and after), `runner-deadline`,
  `unfiltered-sensitive-token`, `probe-meta-line-missing`, and
  `results-sensitive-token` — the last rejects any 40+ hex run in
  `results.txt` other than the two pinned constants (the source commit and
  the runtime archive digest), so a canary cannot reach the recorded
  evidence.

## How to run (on the native Mac)

```sh
tar -xzf codebox-native-phase4-channel-probe.tar.gz
cd codebox-native-phase4-channel-probe
python3 run.py
```

No flags, no ports, no lab, no `--ip`: every assertion is local to the
host/guest pair and the guest tooling is Python-stdlib-only. Requires `go`
and `curl` on PATH. Budget: up to ~25 minutes wall (probe ctx 20 min, VM
max duration 16 min). Exit 0 and `results.txt` full of PASS = every
assertion held. Any FAIL line names the broken property; DIAGNOSTIC lines
give locations and measurements.

The probe never touches `~/.microsandbox`, an installed `msb`, the trust
store, DNS, or interfaces; it uses only synthetic canaries; it creates at
most two VMs (`cb-p4-*` main, `cb-p4-r-*` restored) and one snapshot
(`cbp4snap-*`), all removed on success, failure, and signals (emergency:
`probe -cleanup -dir <run-dir>`; the run directory itself is deleted by the
runner).

## What a passing run proves — and what it cannot

A passing run proves, for the pinned runtime (0.7.6, archive SHA256 pinned
in `runtime-download.py`) on that machine:

- Channels B and E1 deliver a value a guest process can read, with the value
  in **no** argv, environ, guest `/var/log`, persisted runtime log, persisted
  runtime config, macOS unified log, or host file of any class — before,
  during, and after snapshot/teardown — and a restart both wipes it and
  keeps the delivery point available. That is criteria 1–4 satisfied, with
  criterion 5 answered by measurement: delivered mode (see diagnostics),
  0600 achievable, non-root excluded after chmod, root (the agent's own
  processes) able to read — which is the Phase 4 intent, and the reason the
  agent warning matters.
- Channel E2 additionally shows a host→guest value transfer whose only guest
  landing zone is process memory.
- Channel A (disk) delivers the same readability, and its value additionally
  lives inside the host-side guest disk image while the VM exists
  (`disk-value-on-host`). This is **not** a transport leak: the guest's disk
  *is* a host file, so any file a guest writes necessarily appears there. It
  matters for two reasons only, and both are policy for the phase-4 contract
  rather than a defect:
  1. criterion 4 forbids an **intermediate** host file used to stage the
     value on its way in. The disk image is the guest's own storage, not a
     staging file, so a strict reading of "no host file at all" would also
     forbid the ordinary case of an application reading a file in its own
     workspace — which is the point of the feature.
  2. any snapshot taken while the file exists captures it, and deleting the
     file afterwards does not remove it from snapshots already taken
     (`snapshot-captures-delivered-value`). This is normal snapshot
     behaviour and belongs in the contract as a documented lifecycle rule,
     not as a reason to avoid the workspace.

  Channel B (tmpfs) is the alternative when a value should not be captured:
  it is RAM-backed, absent from the host image and from snapshots, and cleared
  on restart (so it is re-delivered at every boot).

A passing run does **not** prove (state these honestly in the issue):

1. **Host process memory.** The value necessarily transits the probe/runtime
   process memory over the FFI boundary; memory disclosure (swap, core
   dumps, hibernation) is out of scope for criteria 1–5 as written.
2. **macOS unified-log redaction.** `log show` may replace values with
   `<private>`; `host-oslog-clean` is therefore weaker than the other log
   checks. The persisted runtime log and file scans carry the load for
   criterion 2.
3. **Debug log levels.** The runtime runs at its default log level; a
   deployment that raises runtime verbosity is a different, unqualified
   configuration.
4. **Races.** `/proc` and the log/file scans are point-in-time; a value
   flushed to a host log after the last scan would not be seen. The scan
   ordering (delivery → scans → restart → snapshot → teardown scans) is
   designed to bracket every lifecycle transition, but it is sampling, not
   a proof of absence at every instant.
5. **Other runtimes/versions.** Results bind to the pinned archive SHA256,
   SDK 0.7.6, and the `python:3.12-alpine` guest image, on darwin/arm64.
6. **Concurrent revocation.** If a snapshot exists, deleting the value in
   the guest cannot revoke the copy inside the snapshot (measured by
   `snapshot-captures-delivered-value`); for channel B the snapshot simply does not
   contain it (`snapshot-excludes-tmpfs`).

## Kit contents

- `run.py` — native runner (preflight, runtime download, build, deadline,
  results filtering, cleanup).
- `runtime-download.py` — pinned official 0.7.6 darwin/arm64 archive,
  SHA256-verified before extraction.
- `main.go` + `main_test.go` — the probe and its unit tests
  (`go test ./...` runs anywhere; the probe itself requires darwin/arm64).
- `guest/scanner.py`, `guest/reader.py` — stdlib-only in-guest tooling,
  pushed via `FS().Write` at run time.
- `go.mod` / `go.sum` — SDK pinned at v0.7.6.

## Known failure modes to check before trusting a run

The 2026-10-15 qualification session drove a *different* phase-4 kit revision
— an end-to-end delivery kit, not this channel probe. That revision was not
retained, so neither were its transcripts. Four of its failure modes were
recorded at the time and are repeated here because a re-run is planned; each
one makes a run look like a product failure when it is a harness failure:

- **The guest fixtures directory was not created**, so the pushed guest
  tooling was missing and every case that depends on it failed. This is the
  same defect class the phase-3 kit hit. Note for whoever re-authors it: the
  SDK's `FS().Mkdir` does create missing parents (`fs.go`: "creates a
  directory (and any missing parents)"), so the cause is not a missing
  `-p`; find it rather than assuming.
- **The host disk classifier did not recognise a `.upper.ext4` file.** This
  probe's own `classify` matches `upper` and so handles that name; a
  re-authored kit must carry the same coverage, or a real disk-image hit
  reads as a clean scan.
- **A rotation step was rejected by the proxy with `requires restart`**, so
  the rotation case could not be exercised in that shape.
- **One assertion depended on a fix that was still unmerged** (the dotenv
  backslash encoding, PR #188). Assert against merged code, or the run
  measures a branch rather than the product.

Two habits from the phase-3 session apply: run the kit from a repository
checkout rather than bouncing it through sandboxes, because harness defects
cascade silently and the kit's own cleanup cannot recover from wrong runtime
state; and treat a transcript that is not bound to a source revision as no
evidence at all.
