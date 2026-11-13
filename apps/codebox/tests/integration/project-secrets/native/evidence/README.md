# Native Qualification Evidence

Date: 2026-11-10, two independent runs completed at approximately 14:23 and 14:24 UTC.
Platform: macOS 27.2 (build 26B123) arm64; native Go 1.27.3; SDK/runtime 0.7.14.
Project fixture source: Microsandbox pin commit `684ea414de08a30e2456d3aab24b453b2eec1641`
(the v0.7.14 pin update, including the native kit SDK pin).

The v3 test artifact was taken from that commit's
`tests/integration/project-secrets/native` directory and run on the Mac:

```text
runtime archive SHA256: 9cc2bd2874cc1aa98cb139429a9482830b1e64b48255aa9f40a25b8979a23a9f
run-1 results SHA256: f334a086b7344549edf3afb75bd75ca390d36a99305defd7d248197c54ff7266
run-2 results SHA256: f334a086b7344549edf3afb75bd75ca390d36a99305defd7d248197c54ff7266
```

Each run used a fresh random VM name, fresh isolated state, synthetic canaries,
temporary CA and independent lab journal. The sanitized transcripts are
[run 1](darwin-arm64-run-1.txt) and [run 2](darwin-arm64-run-2.txt). They are
byte-identical because they contain stable metadata and case names, not random
values, paths, VM names or timestamps. Two separate native invocations passed
all 29 assertions; independent cleanup also passed for each, and isolated
`msb list` confirmed no sandboxes remained. Both runs used the same port pair
(18443 TCP, 15354 UDP), verified free beforehand via `lsof` and a bind probe;
no port substitution was needed and no process was killed or evicted. The host
LAN address was `192.168.210.116` on `en0`. Only two runs were made.

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
