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
6. After merge, sync local `main` to `origin/main` before starting new work.

The repository normally uses squash merge, so local topic branches may not share ancestry with the final `main` commit after merge. Start the next task from the updated `main`, not from the old topic branch.
