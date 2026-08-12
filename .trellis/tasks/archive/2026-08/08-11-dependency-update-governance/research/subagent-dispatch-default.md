# Research: Persistent Codex Sub-Agent Dispatch

- Query: Where should this project persist the maintainer preference to use Subagent-Driven execution for future Codex work?
- Scope: internal
- Date: 2026-08-11

## Findings

- `.trellis/config.yaml:111-130` is the project-level Codex dispatch source of truth.
- The current `codex.dispatch_mode: auto` already dispatches `trellis-implement`, `trellis-check`, and `trellis-research` agents.
- `sub-agent` is documented locally as a backwards-compatible alias for `auto`; using it makes the maintainer's preference explicit without changing runtime semantics.
- `.codex/agents/trellis-*.toml` define roles, write boundaries, context loading, and recursion guards. They do not need changes for a dispatch preference.
- `.codex/hooks.json` already registers `SubagentStart` context injection for all three Trellis roles.
- `.trellis/.template-hashes.json` shows `.trellis/config.yaml` is Trellis-managed, while the current file hash differs from the recorded template hash. Any edit must preserve all existing project modifications and change only the dispatch value.

## Decision

- Set `.trellis/config.yaml` to `codex.dispatch_mode: sub-agent`.
- Record the durable default in `.trellis/spec/guides/branch-workflow-guidelines.md`.
- Do not modify project agent definitions, hooks, global Codex configuration, or Trellis upstream templates.

## Caveats / Not Found

- This is a project-level preference for Xirang, not a global preference for every repository on the machine.
- A user may still explicitly request inline execution for an individual task.
