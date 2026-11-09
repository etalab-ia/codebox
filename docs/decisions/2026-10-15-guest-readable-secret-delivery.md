# D-004: Guest-readable secret delivery contract

Date: 15 October 2026
Status: transport qualified on macOS arm64; implementation pending
Tracking: #145; phase 4 #151

> Pin note: the dependency list below records the versions in force when the
> contract was measured (Node `dotenv` 16.4.7, `dotenv-expand` 12.0.2,
> `python-dotenv` 1.2.2). The fixtures now pin `dotenv` 18.0.9,
> `dotenv-expand` 13.0.0 and `python-dotenv` 1.2.4. The behaviour assertions
> that established this contract — quoted apostrophes, raw multiline values,
> carriage returns, `$` and `#` — run on the current pins in the
> `fixture-contracts` CI job and pass unchanged. The end-to-end guest transcript
> has since been re-qualified on the current pins: the 2026-11-09 macOS arm64
> run (`darwin-arm64-run-3` / `-4`, with a negative control) exercises
> `dotenv` 18.0.9, `dotenv-expand` 13.0.0 and `python-dotenv` 1.2.4, while the
> 2026-10-15 pair remains the record for the versions named below (see
> `tests/integration/project-secrets/native-phase3/evidence/README.md`).

## Context

Phases 1–3 cover secrets the network proxy can substitute: the guest holds an
opaque placeholder and the real value never enters the VM. Some values cannot
work that way. A database password, a signing key or a message-broker
credential is consumed *locally* by the application — there is no outbound
request for the proxy to rewrite — so the plaintext value has to exist inside
the guest.

That changes the security posture, and the change is the point of the phase:
the agent runs in that same guest, so anything delivered this way is readable
by the agent and by every process it can start. The plan therefore requires a
separate, explicit approval with a persistent warning, and forbids a silent
fallback from a proxy-protected secret to a guest-readable one.

Before building anything, the delivery channel was qualified on the pinned
runtime (`tests/integration/project-secrets/native-phase4/`, macOS arm64,
SDK/runtime 0.7.6). The question was not "how do we write a file in the guest"
but "which transport leaks, and where".

## Decision

**Transport.** Values reach the guest through the SDK filesystem write
(`FS().Write`) or an stdin stream. Neither appears in process argv, in the
guest or host environment, in guest `/var/log`, in the persisted runtime log,
in the persisted runtime configuration, or in an intermediate host file. The
channels that do leak are excluded by construction, and the probe demonstrates
them as controls: a value in a shell command is visible in the process table,
and a value supplied inline in the runtime's secret configuration is persisted
in the runtime database.

That guarantee covers the **delivery**. It does not cover every later use of
the value, and one such use is a real leak: the runtime persists the stdout of
guest executions in its own log (`logs/exec.log` under `MSB_HOME`), so a
command that *prints* a guest-readable value — a plain `cat .env` — copies it
into a persisted host log. The property is therefore "delivery writes no log",
not "the value can never reach a log", and just-code itself must not read a
managed file back through guest stdout. Comparing content without exposing it
means comparing a digest the guest computes, not reading the file.

That last sentence was written here before it was true in the code: the
inspection path still used `cat` to read a managed file back, so a reconciled
start or a rotation — anything that compares an already-delivered file — copied
the real value into `logs/exec.log`, on the host, in plaintext, and that log
survives the sandbox. The comparison now happens inside the guest: the digest
of the content this run wants is compared against `sha256sum` of the file by
the guest's own shell, and the guest prints only `managed=yes|no` and a token
when the digests match. Nothing prints content, and the value never becomes an
argument either, so it cannot reach the guest process table.

The expected digest travels on the command's standard input, not in argv, and
that is not a detail: the same log records exec argv, so a digest of the
secret written as an argument would land in the host log this section is
about. A digest is not the secret, but for a value with little entropy it is
enough to test guesses offline, and keeping it off argv costs nothing.

The ownership header is compared by a digest over its exact bytes — the header
line and its newline — and that choice came from three rounds of review, each
finding a way to forge a comparison that normalised something. A shell variable
drops NUL bytes (bash and dash alike), so `# just-code mana\0ged: do not edit`
came back as the marker. `tr -d '\r'` stripped an interior carriage return, so
`# just-code mana\rged: do not edit` did too. And `grep -qxF` matches a marker
followed by a NUL, because GNU grep and the guest's busybox both treat such a
line as binary and match on the readable prefix — busybox does it even with
`-a`, which is what the obvious advice suggests and what was measured not to
work in the shipping image. A digest over a fixed byte count cannot match a
line differing by one byte anywhere, and needs only `head -c` and `sha256sum`,
both already present in the guest. In each case the failure was that a foreign
file read as just-code's own, which lets a synchronization overwrite it.

One limit is deliberate and stated rather than papered over: this proves the
file's content is the one just-code wanted, not who wrote it. If a sandbox is
rebuilt and a file at the same path happens to hold the same bytes, the plan
reads it as unchanged. The forgeable case is narrow — rebuilding a sandbox and
writing the exact expected bytes there is strictly more work than writing the
original file would have been, so a caller able to do it is already inside the
guest — but it is a limit, not a proof. Telling the two apart needs a secret
this sandbox carries, which the guest environment cannot hold without putting
it where any guest process can read it; that is a channel decision for a later
phase, not a line of code here.

**Destination.** A guest-readable value is delivered to the path the
application already reads — by default the component's own file in the guest
workspace (`/workspace/.env`, `/workspace/apps/api/.env`), matching where the
manager's generated dotenv files already go. Writing a file into the workspace
is the ordinary case this feature exists to serve, and it is explicitly not a
violation of anything: the guest's disk is a host-side image, so *any* file the
guest writes lives in a host file. What the contract forbids is an
*intermediate* host file used to stage the value on its way in.

**Permissions.** Files are created `0600`. The runtime writes `0644` by
default, so the mode is set explicitly and verified, and the value is delivered
only after the file exists with the restrictive mode.

**Encoding.** A value is written as a single-quoted dotenv literal whenever
that form carries it, and unquoted whenever quoting cannot carry it and the raw
form safely can. This is not cosmetic: dotenv has no escaping that every loader
agrees on, and the failure is not confined to the variable. Measured against the
loaders just-code pins (Node `dotenv` 16.4.7 with and without `dotenv-expand`
12.0.2, `python-dotenv` 1.2.2, `godotenv` 1.5.1):

- **Single quotes** carry newlines, whitespace, `#`, double quotes, the
  backtick, an interior backslash and the empty value exactly, in all four
  configurations, so they are the default form. They do *not* carry an
  apostrophe: a single quote cannot be escaped inside a quoted value, so
  `godotenv` refuses the whole file and `python-dotenv` misparses it.
- A **raw** value containing a newline makes `godotenv` refuse the *whole file*,
  so one PEM-shaped secret would take down every other secret in that `.env`.
  A raw value is also trimmed of leading and trailing whitespace, cut short at
  a `#` that follows a space, and expanded on `$`. Raw is therefore restricted
  to a measured set (see below), never used as a general encoding.
- Values quoting cannot carry are written **unquoted** instead of refused,
  whenever the raw form accepts them. That class is not a fixed set of shapes:
  it is every value whose only quoting obstacle is an **apostrophe** (no
  position saves it, since a single quote cannot be escaped in a quoted value,
  so even `it's` is written raw), a **pair of consecutive backslashes**
  (`python-dotenv` silently collapses them to one when quoted) or a **trailing
  backslash** (it escapes the closing quote, so `python-dotenv` loses the value
  *and the following line* and `godotenv` refuses the whole file — the shape a
  generated file always has, the value followed by another quoted value), or
  any combination of the three. An unquoted value has no escape processing in
  any pinned loader, so all of them round-trip verbatim. This is what makes
  Windows paths, UNC paths and paths such as `C:\Users\O'Brien\` deliverable.
  A value that adds any *other* quoting obstacle — a `$`, a carriage return —
  leaves quoting impossible and is refused if the raw form rejects it too.
- The **raw character set** was derived by measurement, not by guessing. A
  sweep over every assigned code point, placed at the **leading, trailing and
  interior** position of a value in a generated file (the raw value between
  quoted values), through all three pinned loaders, found:
  - **interior**: exactly three failures — LF, CR and the already-refused `#`
    (and `$`, refused for expansion rather than for line handling; and a
    wrapping backtick pair, which plain Node `dotenv` strips but the other two
    keep);
  - **edges**: an edge-trim set that is the union of what each loader strips:
    `unicode.IsSpace(r)` (Go, Python and Node all strip these), Python's four
    information separators `U+001C..U+001F` (Python's `str.isspace`, not Go's),
    and `U+FEFF` (JavaScript's `String.trim`).
  A character from that set is refused at an edge and kept in the interior,
  which is where the loaders differ. **Everything else, including every
  non-ASCII letter, mark, punctuation and symbol, is carried verbatim**: an
  unquoted value undergoes no decoding, so `C:\Répertoire\`, `naïve\` or `秘密\`
  round-trip byte for byte. Refusing non-ASCII would reject most of the world's
  file names over a hazard that exists only inside quotes.
- A **carriage return** is refused by both forms. Quoting normalises CR and
  CRLF to LF in Node `dotenv` and `python-dotenv` — and `python-dotenv` does
  this at the *file* read (universal newlines), not in the parser, so a value
  read through a stream looks preserved while the shipping path alters it —
  while a raw CR ends the value. A PEM secret pasted from a Windows source is
  exactly the case that hits this.
- What remains, and cannot be carried by either form, is **refused** at
  approval time so the user is present, naming the variable and the character.
  A refused secret is a nuisance; a silently altered one fails far from here.
  The refusal is a conjunction, not a list of characters: quoting must be ruled
  out by the value *and* the raw form must be ruled out as well. Quoting is
  ruled out by a carriage return, an apostrophe, a `$`, a backslash pair or a
  trailing backslash; the raw form is ruled out by a `$`, a `#`, a newline, a
  carriage return, an empty value, a leading single or double quote, a wrapping
  backtick pair or a character the loaders trim at an edge. (A leading double
  quote rules out *raw* only; it rules out *nothing* on its own, because the
  quoted form carries it.) So a backslash pair followed by a
  `#` is refused for its raw `#`, a value carrying both a newline and a trailing
  backslash for its raw newline, and a backslash pair followed by a trailing
  space for its raw edge-trimmed character — none of them an apostrophe case,
  which is the point: quoting fails on the backslash shape while raw fails on
  the other character, and only the conjunction refuses.
  - An **apostrophe**, but only where neither form can carry it. Quoting never
    carries one, so an apostrophe always forces the raw form; it is refused only
    when the raw form refuses the value too — the value opens with the quote, or
    it carries another raw obstacle such as a `$`, a newline, a carriage return
    or an edge-trimmed character. A backslash pair or a trailing backslash is
    *not* such an obstacle: it is the reason the raw form exists, so `it's a\\b`
    and `C:\Users\O'Brien\` are delivered raw, not refused, and `it's` on its
    own is delivered raw as well.
  - A **quote in first position**, but only where quoting is already ruled
    out. Written raw, a leading single or double quote opens a quoted value, so
    `godotenv` refuses the whole file and `python-dotenv` misparses it. A
    leading **double** quote is still delivered — the single-quoted form
    carries it (`dotenvLiteral("\"abc")` returns `'"abc'`), so only a value
    that another character has already pushed to the raw form is refused for
    it. A leading **single** quote defeats quoting on its own, since quoting
    can carry no single quote at all, so it is refused unconditionally.
    Interior and trailing quotes are ordinary bytes.
  - A value **wrapped in a matching backtick pair**, but only where quoting is
    already ruled out. The single-quoted form carries it, so `dotenvLiteral`
    returns it wrapped in single quotes unchanged and
    `TestDotenvLiteralDeliversPositionalQuotesAndBackticks` pins that delivery.
    Written raw it is not exact: plain Node `dotenv` strips the wrapping pair
    from an unquoted value while `python-dotenv` and `godotenv` keep it. A lone
    leading backtick or an interior backtick is delivered in either form.
  - A **`$`**: expanded by `dotenv-expand` and `godotenv`, literal for plain
    `dotenv`, and the usual escape `\$` is then left as a literal backslash by
    plain `dotenv` and rejected by `python-dotenv`.
  - A **carriage return**, in both forms, for the reason above.
  - **Invalid UTF-8**, checked once before either form: `python-dotenv` raises
    `UnicodeDecodeError` and loses the whole file, Node substitutes U+FFFD, and
    a quoted invalid byte can leave `godotenv` reading the next line into this
    value. The original bytes cannot be delivered as a string.

The character-level classes were found by reading the loaders; the backslash
shapes came from a native qualification kit and were confirmed by measurement,
after an earlier version of this record claimed single quotes preserved
backslashes "exactly in every loader" on the strength of an interior backslash
alone, then refused both shapes rather than measuring whether an unquoted value
could carry them. An unquoted value was then restricted to printable ASCII,
which measurement had never justified either — Codex flagged that a value such
as `C:\Répertoire\` was refused though every pinned loader carries it, and the
sweep above replaced that guess. The same sweep showed quotes and backticks are
**positional, not forbidden**: an earlier version refused every value holding
one, though interior and trailing quotes round-trip raw in all three loaders.
Among the quote characters, the raw form rejects exactly two positions — a
wrapping backtick pair, and a leading single or double quote — and neither is a
refusal of the value itself when the quoted form can carry it. Those are
positions, not the only raw obstacles: the raw form still refuses an empty
value, `$`, `#`, LF, CR and the edge-trimmed characters listed above. A value
like `a\\b#c` is therefore refused by both forms, and for two different reasons:
quoting cannot carry its backslash pair, and the raw form cannot carry its `#`.
The round-trip is now checked against real loaders rather than asserted:
`godotenv` in `internal/justcode/project_secret_encoding_loaders_test.go`, and
Node and Python in `tests/integration/project-secrets/`.

**Snapshot lifecycle.** A snapshot taken while a guest-readable value is
delivered captures it, and deleting the file afterwards does not remove it from
snapshots already taken. This is ordinary snapshot behaviour, not a defect, and
it is a documented property of the feature rather than a reason to avoid the
workspace: an operator who snapshots a guest holding live credentials has
snapshotted those credentials. Revocation therefore means "removed from the
live guest", never "gone everywhere". A value that must not be captured can be
delivered to a RAM-backed target instead, which snapshots do not contain and a
restart clears — at the cost of re-delivering it at every boot.

**Approval.** Guest-readable delivery is a distinct approval from the proxy
bindings, recorded host-locally beside them, per project/profile/component/
variable. Enabling it prints a persistent warning, repeated at each start, that
the value is readable inside the guest by the agent and anything it runs. There
is no automatic downgrade from a proxy-protected binding to a guest-readable
one, and no implicit delivery: a value is not readable in the guest until it is
explicitly approved for that.

**Lifecycle.** The value is (re-)delivered on every start, so a restarted guest
and a RAM-backed target are always repopulated. Removing the approval removes
the delivered file from the guest. The host-local inventory of what is
currently delivered lives beside the existing binding records, never in the
project, and never in the instance state that reconciliation rewrites.

## Qualification boundary

The probe measures the pinned runtime on macOS arm64 with synthetic canaries; it
does not prove absence in host process memory (the value necessarily transits
the host process over the FFI boundary), and the macOS unified-log check is
weaker than the others because `log show` may redact values as `<private>`.
Absence is measured at the points listed above, before, during and after a
snapshot and after teardown — not continuously.

These measurements cover the runtime's **own** activity, not arbitrary later
use of the value: the probe never runs a command that prints the value, so a
clean result here does not contradict the stdout exposure described under
Transport. The digest-based comparison described under Transport has since
shipped: the inspection runs in the guest, reads the expected digest from the
command's standard input, and prints only a token, so the value never reaches
the runtime's execution log.

Values that an application must consume locally are, by construction, readable
by the agent. That is accepted here with explicit consent, not mitigated.
