# D-003: Project dotenv secret delivery contract

Date: 7 October 2026
Status: transport and loader qualified on macOS arm64; phases 2 and 3 implemented
Tracking: #145; phase 0 #148; phase 1 #147; phase 2 #149; phase 3 #150

## Context

Project `.env` files are intentionally excluded from the sealed workspace
transfer. Microsandbox secrets are a separate mechanism: the guest receives a
placeholder and the network proxy substitutes a host-held value only for
allowed destinations. This cannot satisfy application-local consumers such as
database drivers or signing libraries.

The pinned Go SDK is v0.7.6. Its `SecretEnvOptions` and
`SecretModifySpec` both expose a custom `Placeholder`; the latter also
supports host environment references. Therefore a guest dotenv file may
contain a unique placeholder while the host value stays out of the transferred
workspace and persisted SDK configuration.

## Decision

- Treat import, approval, and delivery as separate operations.
- Identify an imported value by project, profile, source file, and key; the
  key name alone is not a credential-store identity.
- For proxy-protected values, use a stable unique guest variable and
  placeholder per imported identity. A generated guest dotenv target contains
  the placeholder, never the stored value.
- Resolve the stored value only around the SDK call through a temporary host
  environment reference. Preserve the existing inert bootstrap value on the
  create-only SDK path.
- Keep network destinations explicit and host-local. Do not infer them from
  variable names or dotenv contents.
- Keep guest-readable values out of this first path. They need an independent
  delivery design and an explicit warning that the agent can read them.
- Continue to exclude source `.env` files from transfer and never mutate them.

## Qualification boundary

The pinned SDK supports custom placeholders on create/modify. Real guest-loader
tests and two native 0.7.6 canary runs now qualify dollar-free markers, multiple
secrets on a shared destination, independent host authorization, raw/Basic
headers, rotation, removal and next-boot re-add. Phase 2 may replace the old
disjoint-host restriction with per-binding destination approval and distinct
markers, but no production registry policy changes in this qualification PR.
Do not enable body/query substitution. Clients that sign or otherwise transform
tokens locally remain unsupported without separate evidence. This is macOS
arm64 SDK/lab evidence, not the Phase 2/3 application acceptance matrix.

## Consequences

M1 can represent project-scoped bindings without colliding on common guest
variable names. Import/storage is merged in #159; custom binding approvals,
runtime integration and generated guest targets remain to be implemented.
The first delivery slice must fail closed for missing approvals,
unavailable stores, unqualified clients, and unsafe target paths; it must not
fall back to plaintext guest delivery.

## Phase 1 Storage Proposal

The existing project identity is a canonical checkout root, not the optional
`Project` manifest label. The storage foundation uses the full SHA-256 digest
of that root as a host-local namespace, independently of the short VM-name
suffix. Symlink aliases resolve together; separate checkouts, worktrees, moved
roots, and machines do not implicitly share entries. Explicit migration or
sharing is outside this initial implementation.

Within that namespace, an explicit profile, normalized relative component file,
and variable name identify a secret. The index is local to the host and carries
only opaque credential references. There is no global-name or cross-profile
fallback. The manifest is unchanged by this foundation.

Each authorized component replacement prepares new store entries under opaque
references, recording a metadata-only journal first. Atomic index publication
selects the live generation; recovery removes either the uncommitted entries or
the retired generation, never entries referenced by the live index. Cleanup
failure remains visible and recoverable. Operations serialize through the
existing host-local project lock primitive.

Values are encoded inside the credential store to preserve literal empty,
multiline, and whitespace-containing values across native backend conventions.
This is encoding, not encryption: the native store provides the protection;
the explicitly consented file fallback is still plaintext-equivalent. Store
diagnostic text is suppressed because even an encoded value is sensitive.

Phase 1 adds a preview-first `secrets import` command with bounded project
discovery, explicit file selection, a huh multi-select on terminals, and a
non-interactive path requiring `--profile` and `--file`. Apply is separately
confirmed (and requires `--yes` without a terminal). Parsing uses the
maintained `godotenv` parser with dollar references masked so interpolation is
not evaluated; parser errors are redacted. Repeated keys are last-wins and
identified in the preview. Native-store portability limits each value to
951 UTF-8 bytes (the base64 envelope becomes a UTF-16 Windows credential blob
capped at 2,560 bytes); a dotenv file is capped at 1 MiB. These constraints are surfaced
before storage, never by truncation.

> **Addendum 22 October 2026.** The import parser now uses
> `go-rotini/dotenv` (v1.0.3) instead of `godotenv`; the no-expansion contract
> and the size caps are unchanged. Two input forms changed with it: a leading
> UTF-8 BOM is now accepted, and backtick-quoted values are now refused (the
> previous parser kept the backticks literally inside the value). Parser
> errors are no longer merely redacted: a refusal names the variable and the
> rule whenever the name can be identified and echoed safely (#208); otherwise
> it names the rule alone or stays generic, and never the value. The
> user-facing dialect is
> documented in `docs/credentials.md`.

The command only stores values under the current canonical-root scope. It
does not create a project manifest entry, approve a binding, or deliver
anything to a guest. The user documentation is in `docs/credentials.md`.

M1 comprises phases 0–3; M2 comprises phases 4–5, not this storage foundation.

## Phase 2 Host-Local Bindings

The delivery decision is split in two, on purpose. The credential value stays
where Phase 1 put it, referenced by an opaque name in the project secret index.
What the guest may see, and toward which hosts the value may be substituted, is
a separate host-local record:

    <stateDir>/project-secrets/<namespace>/bindings.json

It carries the profile, component, variable, derived guest variable and the
approved destination list — and never a value, a credential reference or
anything derived from one. Approving a destination is the security decision
that enables substitution, so it must not travel with a clone; a checkout that
is copied to another machine delivers nothing until it is approved there.

The active profile is explicit and singular. Development and production are
never combined implicitly: a binding recorded for a profile other than the
active one is an error, not something to fall back on. Clearing the profile
while bindings remain is refused, because that state fails every start with an
unexplained error.

Guest variables are derived from the component and variable
(`.env` + `API_KEY` -> `JC_API_KEY`, `apps/api/.env` + `API_KEY` ->
`JC_APPS_API_API_KEY`) so two components that define the same variable stay
distinct. The derived names are validated for uniqueness, and the built-in
binding names (`ALBERT_API_KEY`, `GITHUB_TOKEN`, `CONTEXT7_API_KEY`) are
reserved: two registrations for one variable would shadow a system credential.

The guest sees an opaque dollar-free marker
(`__CODEBOX_SECRET_<digest>__`), not the proxy default `$MSB_<VAR>`. This is
not cosmetic. Real-loader tests show the default form is erased by ordinary
application loaders — Node `dotenv-expand` removes an undefined `$MSB_...`
reference in every quote mode tested, Go `godotenv` removes it unquoted, and
`python-dotenv` removes the braced form — while the opaque marker survives all
of them. The runtime matrix in PR #182 confirms the same marker is substituted
correctly for raw headers and Basic auth, stays distinct across bindings that
share a destination, and is denied toward an unauthorized host.

The two failure modes are deliberately fatal rather than degrading:

- an approved binding whose imported entry is gone, or whose profile is not
  active, blocks the start (the user asked for a delivery codebox cannot
  perform);
- an unreadable or locked credential store blocks it too. Dropping the binding
  would make the omission the desired set, and the runtime's secret
  modification is a patch — so "nothing to say" means "remove it", and a
  transient store failure would unregister a live credential.

That patch semantics is why the host-local inventory
(`<stateDir>/project-secrets/<namespace>/applied.json`) records the guest
variables and managed files the sandbox currently holds. A lifted approval is
only actually unregistered when its variable is named explicitly in
`SecretsRemove`; without that inventory a revoked binding would keep working
through every later refresh.

The inventory is deliberately host-local and *not* part of the instance state.
Two reasons, both found by review rather than by reasoning: the runtime's state
file describes the VM configuration and drives reconciliation, so writing it a
second time from the start path races that plan; and reconciliation journals
the desired state before its operations run, so the applied inventory would be
overwritten exactly when a revocation needs it. The inventory is read at the
moment each apply runs, including the direct start that `restart` performs —
the path the CLI tells a user to take after revoking.

Incompatible entries are not delivered and are reported by name rather than
silently skipped. Values that an application must consume locally — database
passwords, signing keys — remain out of scope: they need the separate,
explicitly consented delivery design of Phase 4.

## Guest Loader Evidence (2026-10-14)

The host importer disables expansion, but that does not control application
loaders in the guest. Real-library tests now reproduce a compatibility hazard:
Node `dotenv-expand` 12.0.2 removes undefined `$MSB_...` references even after
single/double quoted dotenv parsing, and Go `godotenv` 1.5.1 removes an unquoted
reference. Python `python-dotenv` 1.2.2 preserves bare dollar references but
expands the braced form. Plain Node `dotenv` 16.4.7 preserves the reference.

Dollar-free opaque markers survive every quote/interpolation mode exercised by
these tests. Phase 2 should therefore use a stable marker such as
`__CODEBOX_SECRET_<opaque-id>__`, not the default dollar-form placeholder.
The native canary/proxy tests now pass as well; no new production binding policy
is enabled by the test harness. See the reproducible
[loader matrix](../../tests/integration/project-secrets/README.md) and
[native evidence](../../tests/integration/project-secrets/native/evidence/README.md).
