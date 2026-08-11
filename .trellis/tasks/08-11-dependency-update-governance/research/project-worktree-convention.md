# Research: Project-Local Worktree Convention

- Query: Where should Subagent-Driven implementation worktrees be created for Xirang?
- Scope: internal
- Date: 2026-08-11

## Findings

- The maintainer explicitly selected the repository-local `.worktrees/` directory because it is easier to manage with the project.
- `.gitignore:36-37` already documents and ignores `.worktrees/`; no `.gitignore` modification is required.
- `git check-ignore -v .worktrees/` resolves to `.gitignore:37:.worktrees/`, satisfying the local worktree safety gate.
- Trellis has no documented project config key for a default worktree directory. The durable project convention therefore belongs in `.trellis/spec/guides/branch-workflow-guidelines.md` rather than an invented config field.

## Decision

- Use `.worktrees/<task-slug>` for future isolated project worktrees.
- Use `.worktrees/dependency-update-governance` for this task.
- Revalidate the ignore rule before every worktree creation; preserve `.worktrees/` in `.gitignore`.

## Caveats / Not Found

- The worktree directory choice is separate from `codex.dispatch_mode`; one configures filesystem isolation, the other configures Trellis agent dispatch.
