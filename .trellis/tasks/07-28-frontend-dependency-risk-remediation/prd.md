# Frontend dependency risk remediation

## Goal

Reduce the known frontend dependency vulnerability surface using compatible
updates inside the repository's Node 20, React 18, Vite 7 and ESLint 9
contracts. Preserve honest audit evidence: this task succeeds by removing every
currently remediable moderate/high advisory and explicitly tracking the two
upstream-constrained residuals, not by suppressing them or claiming a clean
audit.

## Confirmed Baseline

- Baseline `main` is `ffa1ebf685af91ee7ebefb1a1535b65f8a870c6c` after
  Child 12 bookkeeping PR #400.
- `env -u NODE_ENV npm --prefix web audit --audit-level=moderate --json`
  reports four vulnerable package records (`1 moderate + 3 high`) spanning
  eight unique GHSAs.
- Compatible current-major updates can remove six of those GHSAs by refreshing
  `brace-expansion`, `postcss`, `nanoid`, `react-router` and
  `react-router-dom` inside existing manifest ranges.
- `GHSA-mh99-v99m-4gvg` remains on the ESLint/jsx-a11y
  `brace-expansion@1.x` path because no compatible fixed version exists.
- `GHSA-qwww-vcr4-c8h2` still matches Router 7.18.1, but applies to unstable
  React Server Components APIs. Xirang is a Vite browser SPA and does not use
  RSC, SSR, router loaders or router actions.
- CI runs the correct Node 20 audit including development dependencies, but the
  step has `continue-on-error: true`; green CI is therefore not clean-audit
  evidence.
- Dependabot PR #383 is a stale partial PostCSS update that overlaps this task.
  PR #379 is an incompatible ESLint 10 attempt and is excluded.

## Requirements

1. Prefer a lockfile-only refresh inside existing semver ranges. Keep
   `web/package.json` unchanged unless evidence proves a direct floor change is
   required for deterministic resolution.
2. Remove the six currently remediable advisories:
   `GHSA-3jxr-9vmj-r5cp`, `GHSA-r28c-9q8g-f849`,
   `GHSA-wrjc-x8rr-h8h6`, `GHSA-h8fp-f39c-q6mh`,
   `GHSA-337j-9hxr-rhxg` and `GHSA-chx6-hx7r-mcp5`.
3. Record each residual advisory with the affected path, applicability,
   responsible owner and an objective revisit trigger.
4. Assess any new moderate/high advisory published or resolved during execution
   instead of silently widening the accepted residual set.
5. Preserve Node 20, React 18, ESLint 9, Vite 7 and the current browser-SPA
   architecture.
6. Preserve CI policy. This task does not add audit suppressions or weaken the
   audit threshold, install mode, test gates or bundle budgets.
7. Supersede Dependabot PR #383 after this task's replacement is merged; do not
   merge or reuse PR #379.

## Acceptance Criteria

- [x] A fresh Node 20 clean install includes development dependencies and the
      strict moderate-level audit no longer reports the six remediable GHSAs.
- [x] No moderate/high finding remains except `GHSA-mh99-v99m-4gvg` on the
      unavoidable `brace-expansion@1.x` path and `GHSA-qwww-vcr4-c8h2` on
      Router 7.x.
- [x] The two residuals have applicability, owner and revisit-trigger evidence;
      delivery text does not claim `npm audit` is clean or passed.
- [x] No `--force`, `--legacy-peer-deps`, overrides, forks, audit omission,
      major dependency migration or CI-policy weakening appears in the diff.
- [x] `web/package-lock.json` is the only expected product file; any
      `web/package.json` change requires a documented planning amendment.
- [x] Node 20 `npm ci`, `npm run check`, bundle-budget validation, the full
      project gate and Docker build pass on the final lockfile.
- [ ] PR #383 is closed as superseded only after the replacement merges; PR
      #379 remains excluded.
- [ ] Required PR CI, post-merge main CI and Release Please complete before the
      task is archived.

## Out Of Scope

- Router 8, React 19, Node 22, ESLint 10 or another major platform migration.
- Application-source refactors, new dependencies, dependency forks or local
  vulnerability patches.
- Treating RSC-only exposure as an application vulnerability when the
  application has no RSC execution path.
- Changing GitHub advisory settings or the repository-wide CI audit policy.

## Approval State

The user has granted the controller standing authority to make routine technical
and workflow decisions. An independent read-only reviewer returned
`SPEC APPROVED` on 2026-07-28. Planning and implementation within this exact
scope are authorized; `task.py start` has run and the task remains
`in_progress` pending review, delivery, CI, merge and post-merge closure.

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.
