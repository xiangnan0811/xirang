# fix: archived P2 Trellis context paths

## Goal

Restore validation for the archived P2 credential hardening Trellis task by updating stale context file paths that still point at the pre-archive active task directory.

## Requirements

- Update the archived P2 task `implement.jsonl` and `check.jsonl` entries that reference `.trellis/tasks/05-20-security-p2-credential-hardening/research/*`.
- Point those entries at `.trellis/tasks/archive/2026-05/05-20-security-p2-credential-hardening/research/*`.
- Keep the change limited to Trellis archival metadata; do not modify runtime code.

## Acceptance Criteria

- [ ] `python3 ./.trellis/scripts/task.py validate .trellis/tasks/archive/2026-05/05-20-security-p2-credential-hardening` passes.
- [ ] `git diff --check` passes.
- [ ] The current Trellis task validates before commit.

## Definition of Done

- The archived P2 task context validates successfully.
- The fix is committed on a non-main branch.
- No runtime files are changed.

## Out of Scope

- Changing P2 runtime security behavior.
- Changing Trellis archive mechanics globally.
- Rewriting historical P2 planning or research content.

## Technical Notes

- Reported failing lines: archived P2 `implement.jsonl:9-10` and `check.jsonl:9-10`.
- Root cause: context files preserved active-task-relative paths after archive moved the research files.
