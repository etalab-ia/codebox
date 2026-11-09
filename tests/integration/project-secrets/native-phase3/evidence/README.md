# Phase 3 Native Qualification Evidence

Date: 2026-10-15, two runs (roughly 10:55 and 10:56 local time).
Platform: macOS arm64 (Apple Silicon), macOS 27.0.1, Go 1.27.1.
Project revision: `f483a8003d7745dfe3cdea7363d5f366af51eebc` — the merge commit
of PR #184 on `main`, i.e. the code that actually ships.

The kit verifies its own vendoring against GitHub before it runs, so the
transcripts below were produced by driving those files, not a reimplementation:

```text
VENDORED-OK project_secret_guest_files.go == just-code@f483a8003d77 (package clause renamed only)
VENDORED-OK project_secret_bindings.go    == just-code@f483a8003d77 (package clause renamed only)
```

| Artifact | Value |
|---|---|
| runtime archive (darwin/aarch64) | `a4f1722fee6460d4e2813e62b60dae4c092021d8120b7aaa9c2f4b5679d97d89` |
| SDK / runtime version | 0.7.6 |
| run 1 results SHA256 | `f6affc8a0219d4450120ca7012c25b6000ab9089bddfea5af8c3788fe75e3440` |
| run 2 results SHA256 | `f6affc8a0219d4450120ca7012c25b6000ab9089bddfea5af8c3788fe75e3440` |
| kit revision | `p3v2` (from the probe's own META line) |

Both runs report the same two META lines and the same 19 `PASS` cases, so the
two files are byte-identical: the transcript contains case names and fixed
metadata, not random canaries, paths, VM names or timestamps. The second META
line is the probe's own, and it is what binds the transcript to what ran — it
names the kit revision (`p3v2`), the source commit and the Go toolchain. A run
that did not produce it fails (`FAIL probe-meta-line-missing`) rather than
recording evidence that cannot be tied to a revision. Each run used a
fresh random VM name, an isolated `MSB_HOME` under a short `/tmp/jcp3-*` root,
its own temporary CA and its own lab journal, and cleaned up its own VM
(`cleanup-own-vm`).

## What This Proves

The end-to-end path a user gets: an approved imported value is rendered by the
real `BuildGuestSecretFiles` into one managed dotenv file per component,
written atomically into the guest workspace, and read by ordinary
applications. The Node and Python fixtures are installed inside the throwaway
guest and configured so their loader can only have got the marker from the
generated file (`processEnv: {}` for Node). Each then calls the lab over HTTPS
carrying that value, and the lab recorded the substituted real canary —
meaning the proxy, not the application, held the value.

Also demonstrated: two components defining the same variable name produce
distinct markers and distinct values without mixing; an unauthorized host is
refused with no lab record; the persisted configuration holds host environment
references and no raw value; and no host mount is configured.

## Boundaries

This qualifies the SDK/proxy contract and the generated-file delivery on macOS
arm64 with a private TLS/DNS lab. It is not the qualification of a user's real
service. Not covered: guest CA installation (the fixtures disable client
verification because the proxy — not the app — verifies upstream), public
network profiles, other architectures, values an application must consume
locally (phase 4), and clients that sign or transform the marker.

The lab binds all interfaces for the duration of a run because the guest
reaches it at the host LAN address; see the README's LAN-exposure note. Raw
private diagnostics and the temporary CA keys are not checked in — only the
sanitized `results.txt` transcripts.

## Pin currency (2026-11-09)

The transcripts themselves record no package versions, only
`sdk=0.7.6 runtime=0.7.6 go=<version>` and step names, so the qualified loader
set comes from the guest manifests at the commit the kit was run from. The
`source=` field is the kit's vendoring pin (`f483a80`), not the run revision: it
names the same commit in every transcript, so it cannot separate two sets (issue
#349). Which transcript covers which set:

| Transcript | Run | Guest loader set covered |
|---|---|---|
| `darwin-arm64-run-1.txt`, `darwin-arm64-run-2.txt` | 2026-10-15, go1.27.1 | Node `dotenv` 16.4.7 + `dotenv-expand` 12.0.2, `python-dotenv` 1.2.2 (manifests at `973ba24`) |
| `darwin-arm64-run-3.txt`, `darwin-arm64-run-4.txt` | 2026-11-09, go1.27.3 | Node `dotenv` 18.0.9 + `dotenv-expand` 13.0.0, `python-dotenv` 1.2.4 (manifests at `d1f6020`) |
| `darwin-arm64-run-negative-control.txt` | 2026-11-09, go1.27.3 | as `-run-3`, with the guest `dotenv` pin replaced by a version that does not exist |

The pair `-run-3` / `-run-4` re-runs the kit on the current pins, so the native
qualification now covers what the fixtures carry: all 19 cases pass, twice in a
row, and the two transcripts are byte-identical (issue #348; macOS 27.2 on
Apple Silicon, Go 1.27.3). The 2026-10-15 pair still stands for the set named in
its row, which is no longer the current one.

The negative control is what keeps the pair meaningful. With the guest manifest
pinned to `dotenv@99.99.99`, the run fails
`node-runtime-and-loaders-installed` — its diagnostic records `npm error
notarget No matching version found for dotenv@99.99.99` — while every earlier
case still passes and the VM is still cleaned up. A kit that passed that
mutation would have qualified nothing.

The host-side loader suites re-measure the same encoding and substitution
properties on every pin change in `fixture-contracts`; these transcripts carry
the end-to-end claim. Treat `main_test.go`'s `TestGuestFixtureDependencyPins` as
the authority on which pins the kit currently carries.
