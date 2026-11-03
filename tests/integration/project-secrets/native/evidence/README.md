# Native Qualification Evidence

Date: 2026-11-03, three runs completed at approximately 14:56, 14:57 and 14:58 UTC.
Platform: macOS 27.0.1 arm64; native Go 1.27.3; SDK/runtime 0.7.13.
Project fixture source: Microsandbox pin commit `3b853c61b95659c99769eda485dc7791cb44ea14`
(the v0.7.13 pin update, including the native kit SDK pin).

The v3 test artifact was taken from that commit's
`tests/integration/project-secrets/native` directory and run on the Mac:

```text
runtime archive SHA256: 5eb53ca1d6beb36caa64d19602284b9ce37872eff340a8db0542ab91b5808c96
run-1 results SHA256: f0ecd2b19c63ee1896b147558d384c48077a56fd76c2083022eea7b8a6ae4664
run-2 results SHA256: f0ecd2b19c63ee1896b147558d384c48077a56fd76c2083022eea7b8a6ae4664
```

Each run used a fresh random VM name, fresh isolated state, synthetic canaries,
temporary CA and independent lab journal. The sanitized transcripts are
[run 1](darwin-arm64-run-1.txt) and [run 2](darwin-arm64-run-2.txt). They are
byte-identical because they contain stable metadata and case names, not random
values, paths, VM names or timestamps. Two separate native invocations passed
all 29 assertions; independent cleanup also passed for each, and isolated
`msb list` confirmed no sandboxes remained. Run 2 used a second verified-free
port pair (19443/16354) because 18443 was held in TIME_WAIT with no listener.
No process was killed or evicted. A third run reproduced the same transcript
and is not recorded separately.

The v0.7.11 evidence had recorded a kit SHA256 for the Cloud-authored artifact.
This run took the kit directly from the pinned commit above, so that line is
replaced by the pin commit itself rather than a re-derived archive digest.


## Qualified Contract

- Dollar-free opaque markers survive raw header and Basic base64 substitution.
- Two bindings can share an allowed destination and substitute distinct values.
- A third binding's permission for another host never authorizes the first two
  there, and vice versa; positive reachability controls distinguish secret
  authorization from network/DNS/TLS failure.
- Forbidden destinations and cross-host redirects fail closed.
- Live value rotation preserves the other bindings; removal stops transmission
  of old/new canary values without claiming guest-environment erasure.
- Re-add with NoRestart is refused; NextStart persists the registration but
  leaves it inactive until boot. After stop/start, all three markers, restored
  A, unchanged B/C, and per-binding host restrictions work.
- Persisted config contains host environment references rather than raw canary
  values; no configured host mounts were present.

## Boundaries

This qualifies the SDK transport primitive, not an end-to-end just-code custom
binding UI or generated dotenv delivery. Host networking was permitted for the
private TLS/DNS lab, and guest Python skipped client certificate verification;
the interception proxy still performed upstream verification using the temporary
CA and required TLS identity for secret substitution. It is not evidence for
guest CA installation, production egress profiles, public services, or other
native architectures. The disposable `python:3.12-alpine` image tag was not
digest-pinned. Header/Basic transport is covered; local token signing and query/
body substitution are not enabled or qualified by these results.

No existing runtime, project checkout, host DNS or host trust store was
modified. Raw private diagnostic logs and CA keys are not checked in.
