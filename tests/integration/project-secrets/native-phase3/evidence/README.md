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

## Pin currency (2026-10-25)

The transcripts themselves record no package versions, only
`sdk=0.7.6 runtime=0.7.6 go=go1.27.1` and step names, so the qualified loader
set comes from the guest manifests at the recorded commit `973ba24` (the
`source=` field points at `f483a80`, where these paths did not yet exist):
**Node dotenv 16.4.7 + dotenv-expand 12.0.2** and **python-dotenv 1.2.2**. The
manifests under `guest/` have since moved to **16.6.1 / 13.0.0 / 1.2.4**
(issues #246 and #324), and the
guest installs its packages from those manifests at run time. A fresh run
therefore exercises a different loader set than the one recorded here, so this
evidence should not be cited as covering the current pins. The host-side loader
suites re-measure the same encoding and substitution properties on the new set,
but the native qualification itself needs one more macOS arm64 run to be
restated. Treat
`main_test.go`'s `TestGuestFixtureDependencyPins` as the authority on which
pins the kit currently carries, and `git show 973ba24:` on the two guest
manifests as the authority on what this evidence covers.
