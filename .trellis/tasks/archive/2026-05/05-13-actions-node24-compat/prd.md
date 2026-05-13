# Upgrade GitHub Actions for Node 24 compatibility

## Goal

Update the repository's GitHub Actions workflows so JavaScript actions no longer depend on the deprecated Node 20 runtime, while preserving SHA pinning, release safety, and the current PR/post-merge monitoring contract.

## What I Already Know

- GitHub has started the Node 20 deprecation process for GitHub Actions. GitHub-hosted runners support Node 24, will default JavaScript actions to Node 24 beginning June 2, 2026, and only allow the temporary Node 20 opt-out until runner support is removed later in 2026.
- Recent Xirang workflow runs emitted Node 20 deprecation warnings for pinned workflow actions, including `actions/checkout`, Docker actions, artifact actions, and attestation actions.
- Current workflow files intentionally pin actions by commit SHA with a nearby version comment; this supply-chain hardening should be preserved.
- Release and Docker publish workflows are part of Xirang's public release contract. Changes to those workflows must be reflected in maintainer docs and verified after merge.
- The currently used `google-github-actions/release-please-action` repository is archived. Its latest tag still declares `runs.using: node20`; the active replacement is `googleapis/release-please-action`, whose `v5.0.0` declares `runs.using: node24`.

## Requirements

- Upgrade JavaScript actions in `.github/workflows/*.yml` to versions whose `action.yml` / `action.yaml` declares `node24`, and pin each by resolved commit SHA.
- Preserve the existing workflow behavior for CI, release creation, Docker image publishing, Docker Hub description sync, and manual deploy.
- Migrate Release Please from the archived `google-github-actions/release-please-action` repository to `googleapis/release-please-action` at a Node 24-compatible tag.
- Update maintainer release documentation where publish/release/deploy workflow behavior or action maintenance guidance changes.
- Do not add `ACTIONS_ALLOW_USE_UNSECURE_NODE_VERSION` or other temporary Node 20 opt-out variables.
- Keep unrelated frontend warning cleanup out of scope for this CI-focused task.

## Acceptance Criteria

- All changed JavaScript actions are pinned by commit SHA and have accurate version comments.
- `release-please.yml`, `publish-images.yml`, `dockerhub-description.yml`, `deploy.yml`, and `ci.yml` remain syntactically valid and pass local workflow linting.
- `scripts/check-doc-freshness.sh` and its self-test pass.
- Local CI parity checks pass for the repository.
- PR CI passes before merge.
- After merge, post-merge automation is monitored. If the chore-only CI change does not create a formal release, that is recorded explicitly.

## Out of Scope

- Reworking the release workflow topology beyond the action upgrades needed for Node 24 compatibility.
- Changing Docker image tag semantics, `latest` behavior, Trivy severity policy, or release versioning.
- Fixing existing frontend runtime warnings that are unrelated to GitHub Actions runtime deprecation.

## Technical Notes

- Relevant specs/guides:
  - `.trellis/spec/guides/branch-workflow-guidelines.md`
  - `.trellis/spec/guides/documentation-truth-guide.md`
  - `.trellis/spec/guides/code-reuse-thinking-guide.md`
- Research artifact: `.trellis/tasks/05-13-actions-node24-compat/research/actions-node24-compat.md`
- GitHub changelog source for the runtime schedule: <https://github.blog/changelog/2025-09-19-deprecation-of-node-20-on-github-actions-runners/>
