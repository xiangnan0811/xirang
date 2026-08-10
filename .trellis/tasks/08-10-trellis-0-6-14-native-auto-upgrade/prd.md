# Trellis 0.6.14 native/auto upgrade

## Goal

Upgrade Xirang's project-managed Trellis files from `0.6.0-beta.22` to the
installed stable CLI version `0.6.14`, adopt the bundled native workflow with
Codex auto dispatch, and preserve all project customization and task memory.

## Requirements

- Keep the bundled `native` workflow; do not switch to a channel-driven workflow.
- Set `codex.dispatch_mode: auto` explicitly. `sub-agent` compatibility remains
  acceptable, but `auto` is the selected forward-looking mode.
- Keep `trellis channel` as an optional capability and retain worker guard values
  `idle_timeout: 5m` and `max_live_workers: 6`.
- Preserve `session_commit_message: "chore: record journal"` and
  `max_journal_lines: 2000`.
- Preserve `.trellis/tasks/`, `.trellis/spec/`, `.trellis/workspace/`,
  `.trellis/.developer`, AGENTS instructions, and all Xirang-specific workflow,
  lifecycle, hook, and platform customization.
- Use `trellis update --create-new`; never use `--force`, and explicitly review
  every generated `.new` sidecar before accepting or merging it.
- Register Codex `UserPromptSubmit` workflow injection and a `SubagentStart`
  context hook limited to `trellis-research`, `trellis-implement`, and
  `trellis-check`.
- Resolve Codex hook scripts through the absolute Git common directory so the
  project integration is safe from registered worktrees.
- Pin project-scoped Codex agent recursion to depth 1.
- Add the union merge rule only for append-only Trellis developer journals;
  do not apply it to regenerated workspace indexes.
- Keep the existing backup recovery task tree unchanged and do not begin Task 8.
- Preserve the pre-existing untracked `go.mod` and
  `recovery/testdata/rsync_local_to_remote.json` byte-for-byte, and never stage
  or commit them.
- Complete this maintenance change through commit, PR, required CI, squash
  merge, post-merge automation review, local `main` synchronization, and topic
  branch cleanup before Task 8 begins.

## Acceptance Criteria

- [x] `trellis --version` and `.trellis/.version` both report `0.6.14`.
- [x] The bundled workflow is native and `.trellis/config.yaml` explicitly sets
  `codex.dispatch_mode: auto` while retaining Xirang's journal and channel values.
- [x] The native Trellis research/implement/check definitions exist and the
  `SubagentStart` hook injects the active task context for each role.
- [x] Both Codex hooks resolve scripts through the absolute Git common directory,
  and `.codex/config.toml` pins `[agents] max_depth = 1`.
- [x] No unresolved `.new` sidecars remain and a final
  `trellis update --dry-run` reports only the four intentional local
  customizations (`.trellis/config.yaml`, `.codex/hooks.json`, and the two
  whitespace-clean `command-reference.md` copies), with no pending version or
  template upgrade.
- [x] Trellis scripts and hooks compile; configuration parses; task list/current/
  validate checks pass; hook simulations return valid context; `git diff --check`
  passes.
- [x] Task 7 and the wider backup-assets task tree retain their prior state;
  Task 8 has not started.
- [x] Protected untracked file hashes remain
  `b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd`
  and `2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892`.
- [ ] The maintenance PR is merged with required CI green, post-merge automation
  is understood, local `main` matches `origin/main`, and the work branch is
  removed locally.

## Out of Scope

- Modifying the globally installed Trellis package or Trellis upstream source.
- Changing application backend/frontend behavior.
- Changing Task 7 requirements, implementation, status, or archive state.
- Starting or implementing Task 8.
- Cleaning unrelated untracked files or historical workspace data.
