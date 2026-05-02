# Document branch-required workflow

## Goal

Codify that new feature development, bug fixes, and other repository changes must happen on a dedicated work branch, not directly on `main`.

## What I already know

- The user wants a repository norm that forbids direct commits on `main`.
- Any new feature development and problem fix should be completed on a new branch.
- The repository already uses GitHub PRs, required CI checks, and squash merge into `main`.
- The current change is being made on `chore/require-work-branches`, not directly on `main`.

## Assumptions

- The rule should apply to AI assistants and human contributors.
- Documentation-only, Trellis task, and process changes should also use a work branch because they still modify repository state.
- Read-only inspection and syncing `main` with `origin/main` are allowed on `main`.

## Requirements

- Add a clear branch-required rule to AI-facing project instructions.
- Add the same rule to contributor-facing documentation.
- Preserve Trellis-managed blocks rather than editing inside generated sections when a stable non-managed location exists.
- Add a Trellis spec guide so future Trellis-backed work can load the rule as project convention.

## Acceptance Criteria

- [x] `AGENTS.md` tells agents not to commit directly on `main`.
- [x] `CONTRIBUTING.md` tells contributors to branch before making changes.
- [x] `.trellis/spec/guides/` includes a branch workflow guide and the guide index links to it.
- [x] The task context references the new guide for implement/check.
- [x] Repository status is clean after commit.

## Definition of Done

- Documentation updated.
- Trellis task validated.
- Diff whitespace check passes.
- Changes committed on the work branch.

## Out of Scope

- Changing GitHub branch protection settings.
- Adding local Git hooks.
- Creating or merging a PR for this change unless requested separately.

## Technical Notes

- `AGENTS.md` contains a Trellis-managed block; edits should go outside that block.
- `CONTRIBUTING.md` already instructs contributors to create a feature branch, but it does not explicitly prohibit direct work on `main`.
- Trellis local customization guidance recommends putting project conventions in `.trellis/spec/`.
