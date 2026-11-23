# 2026-10-06: huh-based wizard UI

## Context

Every interactive prompt in the CLI (the `init` wizard, the global `setup`
wizard, `auth add`, and the launch-path "Configure it now?" offer) rendered
as sequential line prompts. On a terminal this is a long, busy wall of
questions (issue #133): the user answers one question per line, with no
grouping, no selection UI for the skills catalogue, and no way to revise an
earlier answer except restarting.

## Decision (updated 2026-11-23, #445 / #461)

Interactive terminals run the wizards through one shared `charmbracelet/huh`
implementation (`codeboxTheme` + `frenchKeyMap` in `huhRun`):

- Bare `codebox` in an unconfigured project runs one combined six-screen flow
  (welcome, provider/key with host-bound saved-key reuse, agent, OS-filtered
  runtime, skills, and MCP) with no default skill or MCP preselected and no
  extra review screen. GitHub is asked once and only when the project has a
  GitHub remote on a supporting runtime; runtime download happens at launch.
- Command split: `codebox` for combined first run then launch, `codebox setup`
  for machine connections and global defaults, `codebox init` for project
  configuration, and `codebox init --advanced` for model, resources, isolation,
  and skill storage.
- Groups whose answers the command line already supplied are omitted
  structurally, preserving the "flags answer questions" contract.
- Secrets (`auth add`, setup, and the provider/key step) render as huh `Input`
  fields in `EchoModePassword`.

Scripted and CI automation uses explicit CLI flags (`codebox init --yes
--root ... --harness ... --runtime ... --skills ... --mcp ...`) rather than
piping answers into line prompts. Legacy line-based helpers remain only for
existing non-TTY/scripted compatibility tests while `wizardHuhFn` (both stdin
and stdout must be terminals) gates the interactive huh UI.

## Consequences

- `huh` v2 is the single interactive UI stack (bubbletea, lipgloss, and their
  transitive requirements follow).
- New interactive screens and UX changes are implemented once in the shared huh
  surface rather than duplicated across two interactive renderers.
- The non-TTY contract is explicit and deterministic: with no terminal, `init`
  never waits and fails naming the missing flags (or `--yes`).
