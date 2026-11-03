# Phase0 native secret qualification v3 (TEST ARTIFACT ONLY)

v3 adds an independently authorized third binding and removed-binding re-add/next-boot lifecycle gates. The parent reported all v1 native assertions PASS with `TMPDIR=/tmp`, and all v2 native assertions PASS with the built-in short root. No v3 native pass is claimed here. The established v1 fixture defect was the long default macOS temp path: runtime Unix socket derivation exceeded its <104-byte limit. The runner explicitly creates `/tmp/jcp0-*` with mode 0700 and a conservative MSB_HOME length preflight, ignoring host TMPDIR. Results metadata includes `kit=v3` alongside pinned source, SDK, runtime, and archive hash. v2 diagnostics and isolation remain intact; no earlier assertion has been removed or weakened.

Pinned just-code source: `35f5d312aed0a3ea2a972e7862a17987650b9c69` (Microsandbox v0.7.11 pin update).
Go SDK: `github.com/superradcompany/microsandbox/sdk/go v0.7.13`.
Runtime source is the pinned commit's `internal/justcode/msb_runtime_install.go`, darwin/arm64:

- URL: https://github.com/superradcompany/microsandbox/releases/download/v0.7.13/microsandbox-darwin-aarch64.tar.gz
- SHA256: `5eb53ca1d6beb36caa64d19602284b9ce37872eff340a8db0542ab91b5808c96`

Nothing here changes just-code, upgrades the user's msb 0.7.0, installs global tools, imports credentials, changes host DNS, or installs a CA into host trust. Do not run against a user MSB_HOME. No native pass is claimed from Cloud.

## Preflight / dependencies

Use native macOS arm64 with working Hypervisor.framework, network to GitHub/Go module proxy/Docker Hub, Go 1.25+, Python3, curl, and OpenSSL (LibreSSL config-based certificate generation is supported). Do not install anything globally for this kit. Existing tools are sufficient. At least 2 CPUs/768 MiB for this one VM. Docker image `python:3.12-alpine` is intentionally disposable and uncredentialed, but its tag is not digest-pinned.

Extract in a standalone temporary directory, NOT the just-code repository. Check:

```sh
uname -sm
go version
python3 --version
curl --version
openssl version
```

Host LAN IP is required explicitly and must be assigned to this Mac and reachable by guest networking. TLS TCP defaults to 18443, DNS UDP defaults to 15354. Both are nonprivileged. Occupied ports are rejected; no process is killed or evicted. If occupied, select different unoccupied ports with the arguments below. No `sudo`, host interface modification, host resolver changes, or host trust commands.

**The lab listens on all interfaces for the duration of the run.** The mock TLS
and DNS services bind `0.0.0.0` on the two chosen ports, because the guest VM
must reach them at the host LAN address; they are therefore reachable by
anything else on the same network while the run lasts. They serve only fixed
empty HTTP responses and two `.test` names with synthetic addresses, and expose
no files, credentials or host state — but treat the ports as LAN-visible for
the length of a run, and prefer a trusted network.

## Run

From the extracted kit directory:

```sh
go test ./...
python3 run.py --ip "$(ipconfig getifaddr en0)" --tls 18443 --dns 15354
```

`run.py` creates and prints `RUN_DIR=/tmp/jcp0-...`, downloads only the official pinned tarball, verifies SHA256 before extracting two regular runtime files, checks `msb --version`, creates a one-day temporary CA/server cert, binds the lab's ports without reuse, then invokes native `go run .`. A prebuilt copy is retained only for independent emergency cleanup. It sets all of:

```text
MSB_HOME=$RUN_DIR/msb-home
MSB_PATH=$RUN_DIR/runtime/msb
MSB_LIBKRUNFW_PATH=$RUN_DIR/runtime/libkrunfw.5.dylib
```

User `~/.microsandbox` is never used. The VM name is cryptographically unique, written to `RUN_DIR/vm-name` before create, and cleanup targets only that name in that isolated home. Every SDK exec has a 20-second timeout; Python HTTPS calls have a 12-second timeout. Native probe has an 8-minute context deadline and the runner imposes a 9-minute wall deadline; VM runtime is capped at 6 minutes. Setup/download/build also have independent timeouts. Cleanup runs on normal success, failure, SIGINT/SIGTERM, and runner timeout. SIGKILL/power loss require the manual cleanup below.

## Qualification assertions

Two distinct guest variables `JC_A`, `JC_B` share the same sole authorized host `p02lab.test`, with custom opaque dollar-free markers `__JUST_CODE_SECRET_a01__`, `__JUST_CODE_SECRET_b02__`. A third variable `JC_C`, with marker `__JUST_CODE_SECRET_c03__`, authorizes ONLY `evil.test` (which is evil/disallowed for A/B, not for C). Create registers just-code's inert `$MSB_BOOTSTRAP_UNSET` sentinel, then all three are rotated via `SecretModifySpec.Env` + `ModificationPolicyNoRestart` before requests. Only synthetic random 192-bit canaries are used; no keychain, password manager, dotenv, or real credential is read.

Checks: exact guest markers (no raw canary), both header substitutions to the shared host, Basic base64 substitution, direct evil.test refusal, allowed-to-evil redirect refusal with proof the allowed first leg reached the lab, one-binding live rotation preserving the other, persisted ConfigJSON containing all three host env refs and no old/new raw canary, Config.Volumes empty (no configured host mounts), removal preventing old/new secret transmission, remaining binding still working, and own-VM cleanup.

Additional v3 security gates: C->evil.test must positively reach verified upstream TLS and substitute C; C->p02lab.test must be blocked even though A/B authorize that host; A/B->evil.test must remain blocked even though C positively reaches and authorizes it. This catches an any-binding-authorizes-host confused deputy. Existing evil/redirect tests now also run with C registered.

Additional v3 lifecycle gates: after removing A, actual NoRestart re-add must fail specifically with `JC_A` and `requires restart` in the SDK error (matching the pinned Rust planner), and must not reactivate A. NextStart registration must persist successfully without becoming live. The probe calls `Sandbox.StopWithTimeout`, releases its old handle with `Detach`, then gets the same owned VM and calls `SandboxHandle.StartDetached` (returns a connected Sandbox). It verifies three exact guest markers, rotated A plus unchanged B header substitution, unchanged C substitution, both cross-destination denials, and persisted references/no raw canary/no configured mounts after boot. A different error, failed start, or lost binding is a FAIL, not a workaround. Expected blocked requests and NoRestart refusal produce private diagnostics even on a successful suite.

DNS is configured explicitly per VM to LAN-IP:DNS-port. TLS interception is configured explicitly for the selected TLS port, with the temporary CA as upstream trust. Host CA shipping is disabled. DNS rebind protection is disabled only on this isolated lab VM to permit the RFC1918 LAN IP. Guest Python disables client certificate verification to avoid depending on Python CA store integration; the proxy retains its default upstream verification and secret TLS identity requirement, using the temporary CA. This is a network-secret substitution qualification, not a qualification of guest OS CA installation or public-network profiles.

The lab persists only SHA256 digests of received header values, plus fixed probe path/host. No raw request headers are echoed or persisted. Removal permits either blocked transport or an unchanged marker reaching the allowed endpoint, but FAILs any old/new canary transmission. Live removal does not promise environment removal until restart (matching just-code semantics).

## Evidence / cleanup

Only `results.txt` (per-case PASS/FAIL plus source/SDK/runtime/hash metadata) is shareable. An orchestration error is NOT a native pass. Keep the printed RUN_DIR to inspect failures. `native-private.log` and `native-stdout-private.log` can contain native library diagnostics; treat them as private, synthetic-sensitive test data, never attach blindly. The runner filters stdout into `results.txt`, allowing only the probe's PASS/FAIL case-name format and constrained metadata format. The probe emits JSON `DIAGNOSTIC` records to private stderr for SDK stage errors, including create, modification, exec/guest failures, config decode, and cleanup. Records retain SDK error kind name, numeric `kind_code` (the SDK exposes no separate error-code field), and scrubbed message. Raw/encoded synthetic canaries and Authorization/Basic/Bearer strings are redacted by the probe. Native-generated diagnostics outside this helper still require private treatment. Read useful create failures with `grep '^DIAGNOSTIC ' "$RUN_DIR/native-private.log"`. Lab binding failures are in `lab.log`. CA keys and runtime/state remain in private temp storage until explicit deletion, not host trust.

Manual cleanup, from kit directory, substitute the exact printed absolute RUN_DIR:

```sh
RUN_DIR='/tmp/jcp0-REPLACE'
MSB_HOME="$RUN_DIR/msb-home" MSB_PATH="$RUN_DIR/runtime/msb" \
MSB_LIBKRUNFW_PATH="$RUN_DIR/runtime/libkrunfw.5.dylib" \
go run . --dir "$RUN_DIR" --cleanup
```

Cleanup is idempotent for a missing VM and checks the `jc-p0-` ownership prefix. After PASS cleanup and copying only sanitized results, remove only this exact private directory:

```sh
case "$RUN_DIR" in /tmp/jcp0-*) test -f "$RUN_DIR/vm-name" && rm -rf -- "$RUN_DIR" ;; *) exit 1 ;; esac
```

Do not use `msb` from PATH, kill unrelated processes, enumerate/delete user VMs, or delete state before VM cleanup succeeds. The runner stops only the lab/probe process groups it started. If an uncatchable interruption leaves the lab alive, stop only the PID belonging to this RUN_DIR (inspect its `lab.py --dir` command); do not kill by port or global process name.

## Qualification Evidence

The native v3 probe passed twice on macOS arm64 on 2026-10-27: 29 probe
assertions per run plus successful independent cleanup. Sanitized transcripts,
artifact/runtime checksums and explicit scope limitations are in
[evidence/README.md](evidence/README.md). Repository integration makes the lab
IP explicit; the verified calls already supplied it explicitly. Core probe
logic is unchanged from the verified v3 artifact.

## Cloud validation limits

Cloud validation covers the four native-probe unit tests (including diagnostic
redaction and C destination independence), vet, Python syntax and the three DNS
tests. It does not boot a VM. A Darwin cross-build on Linux cannot run this
CGO-dependent SDK without a Darwin toolchain. Native execution evidence is
recorded separately above. Any future native failure must be reported, not
worked around by weakening assertions. This test harness does not enable any
new production binding policy.
