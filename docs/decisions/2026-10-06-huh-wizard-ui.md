# 2026-10-06: huh-based wizard UI

## Context

Every interactive prompt in the CLI (the `init` wizard, the global `setup`
wizard, `auth add`, and the launch-path "Configure it now?" offer) rendered
as sequential line prompts. On a terminal this is a long, busy wall of
questions (issue #133): the user answers one question per line, with no
grouping, no selection UI for the skills catalogue, and no way to revise an
earlier answer except restarting.

## Decision

Interactive terminals run the wizards as `charmbracelet/huh` forms:

- One form per wizard, grouped by concern (placement, runtime, model and
  resources, credentials, skills, MCPs and storage). Groups whose answers
  the command line already supplied are hidden (`WithHideFunc` on
  `opts.Set`), preserving the "flags answer questions" contract.
- The skills catalogue and MCP list render as filterable `MultiSelect`
  fields; the review step renders as a huh `Select` with apply, edit
  (reopens the form with the current answers as defaults), and cancel.
- Secrets (`auth add`, the setup wizard's Albert and GitHub prompts) render
  as huh `Input` fields in `EchoModePassword`.

The line-based prompts remain the non-interactive contract, unchanged: a
piped stdin, a script, or a test drives the same `promptLine`/`promptSecret`
flow with the same semantics. The routing seam is `wizardHuhFn` (both stdin
and stdout must be terminals); tests stub it to false, the same pattern as
`stdinIsTTYFn`.

## Consequences

- `huh` v2 becomes a direct dependency (bubbletea, lipgloss, and their
  transitive requirements follow).
- A terminal with stdin piped but stdout attached keeps the line path, so
  `codebox init < answers.txt` in a real terminal still works.
- The non-TTY contract is untouched: with no terminal, `init` still fails
  naming the missing flags rather than waiting.

## 2026-11-23 update — combined first run (#461)

The combined first run supersedes the line-prompt contract for the interactive
setup/init journey:

- Bare `codebox` and interactive `codebox init` use one six-screen Huh flow:
  welcome, provider/key, agent, runtime, skills and MCP. GitHub is asked only
  when a repository remote exists; advanced settings and the final review do
  not add default screens.
- Provider connections and credential values stay machine-scoped. Each project
  records its selected providers and host-bound credential references; choosing
  a subset for one project does not remove other machine connections.
- A fresh machine and an existing machine use the same project flow. Interrupted
  credential stages retain the existing setup journal; successful project apply
  marks machine setup complete without downloading a runtime.
- `codebox setup` manages machine connections, credentials and global defaults.
  Runtime download happens at project launch, not as a setup stage.
- The combined default has no preselected skill or MCP and no empty-selection
  confirmation. Skill storage, model selection, isolation and resources live in
  `codebox init --advanced`.
- Interactive screens have one Huh implementation. Piped answer files are no
  longer a second interactive renderer: scripts and CI supply flags and `--yes`;
  `init` without a terminal refuses to write without that explicit apply flag.
