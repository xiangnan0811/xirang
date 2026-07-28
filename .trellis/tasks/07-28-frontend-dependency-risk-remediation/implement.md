# Frontend dependency risk remediation implementation plan

## Status

- Task: `in_progress`
- Branch: `codex/frontend-dependency-risk-remediation`
- Base: `main@ffa1ebf685af91ee7ebefb1a1535b65f8a870c6c`
- Planning review: `SPEC APPROVED` (2026-07-28)
- Implementation: `implemented_local_gates_passed_pending_review_delivery`
- Expected product manifest: `web/package-lock.json` only

## Step 1: Planning And Preflight

- Independently review `prd.md`, `design.md`, this plan and current-main
  evidence.
- Validate the task and confirm the branch is based on clean, current `main`.
- Record the current state of PRs #379 and #383 without merging either.
- After spec approval, run `task.py start`; do not edit dependencies while the
  task is still planning.

## Step 2: Genuine Audit RED

Use Node 20 with `NODE_ENV` unset and preserve the JSON output:

```bash
env -u NODE_ENV npm --prefix web ci
env -u NODE_ENV npm --prefix web audit --audit-level=moderate --json
npm --prefix web ls brace-expansion postcss nanoid react-router react-router-dom --all
npm --prefix web explain brace-expansion
```

The audit command must exit nonzero and show the baseline advisory set. A
network, registry or omitted-dev-dependency failure is not valid RED evidence.

## Step 3: Bounded Lockfile Refresh

- Update only the named packages inside existing semver ranges with a
  lockfile-only npm operation.
- Verify `web/package.json` is byte-identical and reject unrelated lockfile
  churn.
- Re-run `npm ls`, `npm explain` and registry engine/peer checks for the selected
  versions.
- Record the exact version and integrity deltas.

No force, legacy peer mode, override, fork, audit suppression or major update is
permitted.

## Step 4: Audit GREEN-With-Residuals

Run the strict audit again and compare unique GHSA identifiers. The expected
result is not exit zero: only `GHSA-mh99-v99m-4gvg` and
`GHSA-qwww-vcr4-c8h2` may remain. Any other moderate/high result reopens Step 3
or planning.

Record:

- complete audit JSON and npm summary;
- the two residual dependency paths;
- applicability and revisit triggers from `design.md`;
- an explicit statement that the audit is not clean.

## Step 5: Compatibility Gates

From a clean Node 20 install:

```bash
env -u NODE_ENV npm --prefix web ci
env -u NODE_ENV npm --prefix web run check
env -u NODE_ENV node web/scripts/check-bundle-budget.mjs
env -u NODE_ENV make check
make docker-build
git diff --check
```

The bundle checker may be run from `web/` if its path assumptions require it;
record the exact successful command. Remove any generated backend binary and
confirm the final product diff remains lockfile-only.

## Step 6: Review And Delivery

1. Independent spec review: verify exact advisory removal, residual honesty,
   Node/React compatibility and scope.
2. Independent quality review: inspect lockfile integrity, package graph and
   absence of unrelated churn.
3. Stage the exact task artifacts plus `web/package-lock.json`, commit with a
   conventional title, push and create a PR.
4. Attach strict audit evidence to the PR because CI audit is non-blocking.
5. Monitor every required PR check; fix failures on this branch.
6. Squash merge only after checks and reviews pass, then monitor main CI and
   Release Please.
7. Close PR #383 as superseded after merge; do not modify PR #379.
8. Synchronize local `main`, update the task ledger, archive the task and record
   the journal through a bookkeeping PR if required by the post-merge ordering.

## Stop Conditions

- A required fix needs Router 8, React 19, Node 22, ESLint 10, an override or a
  fork.
- A compatible refresh changes application source or CI policy.
- A newly published advisory expands the residual set without an applicability
  assessment.
- Clean install, frontend/full-project, bundle or Docker gates regress.
