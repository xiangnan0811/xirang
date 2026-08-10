# Design: Trellis 0.6.14 native/auto upgrade

## Boundaries

The change is limited to project-managed Trellis templates, Codex integration,
the journal merge rule, and this independent maintenance task. The installed
CLI is already `0.6.14`. The backup-assets task tree and unrelated dirty files
are read-only boundaries.

## Upgrade Strategy

1. Work on `codex/trellis-0.6.14-upgrade`, created from synchronized `main`.
2. Snapshot tracked status, task state, customization, and protected file hashes.
3. Run `trellis update --create-new` so unmodified templates update normally and
   possible customization conflicts become reviewable `.new` sidecars.
4. Compare each sidecar against the current file, the 0.6.14 contract, and the
   successful Houfeng upgrade. Adopt generated templates where no Xirang
   customization exists; merge real local values deliberately.
5. Keep the bundled native workflow and explicitly set
   `codex.dispatch_mode: auto`. Channel remains available but is not the default
   orchestrator.

## Preserved Customization

- Session journal commit message and 2,000-line rotation limit.
- Channel guard values of five minutes and six live workers.
- All task, spec, workspace, developer identity, and AGENTS data.
- Xirang's branch, PR, CI, release, and post-merge workflow requirements.
- Any current local hook semantics that remain compatible with 0.6.14.

## Codex Integration Contract

`UserPromptSubmit` invokes `inject-workflow-state.py`. `SubagentStart` matches
only `trellis-implement`, `trellis-check`, and `trellis-research`, then invokes
`inject-subagent-context.py`. Both commands resolve the root checkout from:

```sh
git rev-parse --path-format=absolute --git-common-dir
```

Project config pins `agents.max_depth = 1`. This permits native bounded
sub-agent dispatch without reopening recursive Trellis delegation.

## Journal Merge Rule

Append-only `.trellis/workspace/*/journal-*.md` files use Git's union merge
driver so parallel sessions can merge independent entries. Regenerated
workspace indexes do not receive this rule because conflicts there should be
resolved explicitly.

## Compatibility And Safety

- Never use `trellis update --force`.
- Treat `.trellis/.template-hashes.json` as updater-owned metadata, not a place
  for hand-authored behavior.
- Resolve every `.new` file individually and leave none behind.
- Never stage `go.mod` or `recovery/`.
- Stop if the update changes task/spec/workspace contents unexpectedly or if
  protected hashes differ.

## Rollback

Before commit, rollback is file-specific and limited to the paths introduced or
changed by this task. Do not use `git reset --hard`, `git clean`, or broad
checkout commands. After merge, rollback must be a normal follow-up PR that
restores the previous managed configuration while preserving user data.
