# Branch Workflow Guidelines

> **Purpose**: Keep `main` clean and make every repository change reviewable through a work branch and PR.

---

## Required Rule

Do not commit directly on `main`.

Any file-changing work must happen on a dedicated branch. This includes:

- Feature development
- Bug fixes
- Documentation updates
- Configuration or CI changes
- Trellis task, spec, workflow, or workspace changes
- Repository process and governance updates

`main` is an integration branch. It should track `origin/main` and receive changes through merged pull requests.

---

## Before Starting Work

If a request will change files, do this before editing:

```bash
git fetch origin --prune
git switch main
git pull --ff-only
git switch -c <type>/<short-description>
```

Use a branch name that describes the work, for example:

```bash
git switch -c feat/node-health-summary
git switch -c fix/policy-update-warning
git switch -c docs/branch-workflow
git switch -c chore/trellis-guidelines
```

If `main` has local-only commits, do not continue work on `main`. First resolve whether those commits should become a branch, be merged through a PR, or be discarded with explicit maintainer approval.

---

## What Is Allowed On `main`

These actions are acceptable on `main` because they do not create new project changes:

- Read-only inspection
- `git fetch`
- Fast-forward sync from `origin/main`
- Creating a new branch
- Post-merge sync after a PR lands

Do not edit, stage, or commit project files on `main`.

---

## PR And Merge Flow

1. Make changes on the work branch.
2. Run the relevant local checks for the changed area.
3. Push the branch and open a PR targeting `main`.
4. Monitor CI and fix failures on the same work branch.
5. Merge only after required checks pass.
6. After merge, monitor post-merge automation before declaring the task done:
   `Release Please`, any auto release, `Publish Docker Images`, and release-doc
   workflows such as `Sync Docker Hub Description` when relevant. If the merge
   is not expected to create a formal GitHub Release or Docker Hub publish,
   record that explicitly in the task/PR handoff.
   When changing Docker publish workflows, verify each platform build and scan
   path explicitly; do not assume a multi-arch manifest step proves the
   per-platform scanner selected the intended platform.
7. After post-merge automation is understood, sync local `main` to
   `origin/main` before starting new work.

The repository normally uses squash merge, so local topic branches may not share ancestry with the final `main` commit after merge. Start the next task from the updated `main`, not from the old topic branch.

## Dependency Automation Maintenance

- Keep routine dependency version updates monthly and grouped by ecosystem; do not return to one weekly PR per dependency without an explicit maintainer decision.
- Treat ordinary major-version upgrades as dedicated tasks with compatibility research, full validation, and upstream release-note review.
- Keep Dependabot vulnerability alerts and security fixes independent from the routine version-update schedule and groups.
- Before replacing old bot PRs, capture exact PR numbers and head branches. Close only that allowlist; never use a dynamic close-all query that could include new security PRs.
- Run pull-request CI through `pull_request`, and limit push-triggered CI to `main` so a PR commit does not run the same workflow twice.
- For Codex Trellis work, default to project-configured sub-agent dispatch for research, implementation, and checks. Use inline only when the user explicitly requests it for the current task.
- Use the repository-local ignored `.worktrees/<task-slug>` path for isolated implementation worktrees; preserve `.worktrees/` in `.gitignore` and revalidate it before creation.

## Workflow Action Runtime Maintenance

When editing `.github/workflows/*.yml` action pins:

- Keep third-party and GitHub-owned actions pinned by full commit SHA, with a nearby release tag comment for auditability.
- Resolve the target tag to the exact SHA with `git ls-remote` before editing the workflow.
- For JavaScript actions, inspect the target tag's `action.yml` / `action.yaml` and confirm `runs.using` matches the currently supported GitHub Actions runtime. Do not treat runtime opt-out environment variables as a durable fix.
- If the pinned action repository is archived or deprecated, migrate to the maintained upstream before bumping the old repository's latest tag.
