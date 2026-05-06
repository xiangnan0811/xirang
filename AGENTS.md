<!-- TRELLIS:START -->
# Trellis Instructions

These instructions are for AI assistants working in this project.

This project is managed by Trellis. The working knowledge you need lives under `.trellis/`:

- `.trellis/workflow.md` — development phases, when to create tasks, skill routing
- `.trellis/spec/` — package- and layer-scoped coding guidelines (read before writing code in a given layer)
- `.trellis/workspace/` — per-developer journals and session traces
- `.trellis/tasks/` — active and archived tasks (PRDs, research, jsonl context)

If a Trellis command is available on your platform (e.g. `/trellis:finish-work`, `/trellis:continue`), prefer it over manual steps. Not every platform exposes every command.

If you're using Codex or another agent-capable tool, additional project-scoped helpers may live in:
- `.agents/skills/` — reusable Trellis skills
- `.codex/agents/` — optional custom subagents

## Subagents

- ALWAYS wait for all subagents to complete before yielding.
- Spawn subagents automatically when:
  - Parallelizable work (e.g., install + verify, npm test + typecheck, multiple tasks from plan)
  - Long-running or blocking tasks where a worker can run independently.
  - Isolation for risky changes or checks

Managed by Trellis. Edits outside this block are preserved; edits inside may be overwritten by a future `trellis update`.

<!-- TRELLIS:END -->

## Repository Workflow

- Do not commit directly on `main`. Treat `main` as an integration branch that should track `origin/main`.
- Before any file-changing work, create or switch to a dedicated work branch from an up-to-date `main`. This applies to feature work, bug fixes, docs/config changes, Trellis task/spec updates, and process changes.
- Allowed on `main`: read-only inspection, fetch/pull synchronization, branch creation, and post-merge sync. If `main` has local-only commits, stop and resolve the branch state before starting new work.
- Complete changes through a pull request with CI checks. After squash merge, sync local `main` to `origin/main` before starting the next branch.
- After creating a pull request, the responsible agent or maintainer must monitor all required CI jobs, fix failures on the same work branch, push the fix, and keep monitoring until the required checks pass or a real external blocker is recorded. Do not merge while required checks are failing, pending, or missing.
- After a PR merges, monitor post-merge automation before declaring the task complete: `Release Please`, any auto release, `Publish Docker Images`, and `Sync Docker Hub Description` when README/release docs are involved. If the merge does not trigger a formal release, explicitly record that no GitHub Release or Docker Hub publish was expected.
- Keep the release contract accurate in process docs and PRs: GitHub Release is the public version source of truth, Docker Hub is the only official public image source, and public releases use stable semver tags only.
