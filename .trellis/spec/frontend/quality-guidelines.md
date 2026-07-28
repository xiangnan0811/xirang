# Quality Guidelines

> Code quality standards for frontend development.

---

## Overview

Frontend work should match the existing React 18 + TypeScript + Vite +
Tailwind-style utility approach. The standard gate is `cd web && npm run check`,
which runs typecheck, lint, tests with coverage, and build.

This is an operations console, so correctness, clear state, accessible controls,
and predictable repeated workflows matter more than decorative UI.

---

## Forbidden Patterns

- Direct `fetch` calls in components for normal API requests. Use typed API
  wrappers under `web/src/lib/api/`.
- New ad hoc UI primitives when an equivalent exists in `web/src/components/ui/`.
- Raw backend snake_case payloads in React components or contexts.
- Unlabeled icon-only buttons, inaccessible dialogs, or controls without
  keyboard behavior.
- Negative or viewport-scaled text hacks that can make dashboard/control text
  overflow. Keep labels compact and layout-constrained.
- New external dependencies without an explicit need and review.

---

## Required Patterns

- Run `npm run check` before merging frontend behavior changes.
- Preserve the API envelope contract handled by `web/src/lib/api/core.ts`.
- Add or update tests for behavior changes in pages, hooks, API mappers, and UI
  primitives. The repo uses Vitest and Testing Library.
- Use existing i18n helpers for user-facing strings when editing localized UI.
- Route explicit language changes through `setLanguage()` from `web/src/i18n`
  instead of calling `i18n.changeLanguage()` directly. The helper preserves
  localStorage, lazy locale loading, and `<html lang>` synchronization.
- Keep route pages, dialogs, and tables responsive across desktop and mobile.
- Use shared status, date, chart, and theme utilities instead of duplicating
  formatting logic.
- Keep locale resources out of the startup bundle. `web/src/i18n/index.ts`
  loads the detected language before first render and lazy-loads the alternate
  language on demand; do not reintroduce static imports of
  `web/src/i18n/locales/*` into startup modules.

---

## Testing Requirements

- Page behavior tests belong beside the page, for example
  `overview-page.test.tsx`, `nodes-page.test.tsx`, and
  `settings-page.test.tsx`.
- Utility and API mapper tests belong beside the module, for example
  `overview-api.test.ts`, `tasks-api.test.ts`, and `date-utils.test.ts`.
- UI primitive tests live under `web/src/components/ui/__tests__/` or beside the
  primitive when appropriate.
- For accessibility-sensitive UI, test roles, labels, disabled states, dialogs,
  keyboard-visible states, and empty/error variants.
- For async pages, cover loading, success, empty, error, and stale/refresh
  behavior when the code path is user-facing.
- A11y smoke is part of the gate. Before merging frontend behavior changes:
  - `npm run test` must include the existing `vitest-axe` smoke tests and they
    must pass (see `web/src/components/ui/__tests__/dialog.a11y.test.tsx` and
    the page-level smokes under `web/src/pages/**/__tests__/*.a11y.test.tsx`).
  - `npm run lint` must report **0 `jsx-a11y` errors**. Warnings on debt rules
    (see `eslint.config.js`) are tolerated; new errors are not.
  - When you add a new top-level page or a non-trivial dialog, add a matching
    `*.a11y.test.tsx` using the `runAxe` helper. Template lives in
    `.trellis/spec/frontend/a11y-guidelines.md`.

## Scenario: CI-Equivalent Dependency Audit

### 1. Scope / Trigger

- Trigger: changing `web/package-lock.json`, upgrading frontend dependencies,
  investigating a Docker/CI audit discrepancy, or preparing a frontend change
  for merge.
- Applies to local npm commands, the Node 20 CI job, and Docker's Node 20
  web-builder stage.

### 2. Signatures

- Local audit command:
  `env -u NODE_ENV npm --prefix web audit --audit-level=moderate`.
- Clean-install validation: `env -u NODE_ENV npm --prefix web ci` followed by
  the audit above.
- CI uses Node 20 and runs `npm ci` then `npm audit --audit-level=moderate`.

### 3. Contracts

- Audits must include `devDependencies`; Vite, Vitest, ESLint, and build-time
  tooling are part of the CI and Docker attack surface even when the runtime
  image does not retain `node_modules`.
- A developer shell may export `NODE_ENV=production`, which makes npm omit
  development dependencies and can falsely report a clean audit. Unset it for
  local quality evidence.
- Prefer non-breaking lockfile updates within existing semver ranges. Do not
  use `--force` or introduce a major dependency change solely to silence audit
  output without separate review.
- Compare dependency updates as structured lockfile package records, not only
  as a text diff. A different npm writer may remove unchanged optional-platform
  metadata such as `cpu`, `os`, `libc`, or `optional`; reject or restore that
  unrelated churn before delivery.
- Compare unique advisory identifiers and their resolved dependency paths.
  npm may propagate one advisory through several parent packages, so the
  vulnerable-package summary count can increase even when the GHSA set shrinks.
  Record both the actual summary/exit code and the unique GHSA set; never infer
  a clean audit from either count alone.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| `NODE_ENV=production` is inherited locally | Do not use that output as audit evidence; rerun with `env -u NODE_ENV`. |
| Node 20 `npm ci` then audit finds moderate/high vulnerabilities | Update the lockfile through compatible versions and rerun Node 20 audit. |
| Audit fix requires a major or forced change | Stop and review the dependency upgrade separately. |
| Local audit is clean but Docker/CI audit is not | Compare Node version, `NODE_ENV`, and a clean lockfile install before claiming success. |
| Targeted npm update rewrites unrelated optional-platform metadata | Reject the expanded diff or restore the unchanged metadata, then prove the final changed-record set exactly. |
| Package-record count grows while unique GHSA count falls | Inspect `via`, `nodes`, `npm ls`, and `npm explain`; report the propagated records and GHSA set separately. |

### 5. Good/Base/Bad Cases

- Good: a lockfile-only update moves Vite/esbuild and transitive packages to
  fixed versions within declared ranges; Node 20 `npm ci`, audit, and
  `npm run check` all pass.
- Base: an application with no dev dependency changes still runs the audit with
  `NODE_ENV` unset before merge.
- Bad: recording `npm audit` as clean from a shell where production mode hid
  the vulnerable development tree.

### 6. Tests Required

- Run the local audit command with `NODE_ENV` unset.
- Run `npm run check` after any lockfile update.
- When Docker/CI uses a different Node version, verify a clean Node 20 install
  and audit before merging.
- Structurally compare old and new `packages` entries and assert that only the
  intended dependency records changed; keep `package.json` byte-identical for
  a lockfile-only remediation.

### 7. Wrong vs Correct

Wrong:

```bash
npm audit --audit-level=moderate  # NODE_ENV=production in the shell
```

Correct:

```bash
env -u NODE_ENV npm --prefix web ci
env -u NODE_ENV npm --prefix web audit --audit-level=moderate
```

## Scenario: Time-Stable Expiry Fixtures

### 1. Scope / Trigger

- Trigger: testing UI or API behavior that compares `expiresAt`, `expires_at`,
  TTLs, leases, setup tokens, preflights, or grants with `Date.now()`.
- Applies to Vitest fixtures and Testing Library flows whose enabled/disabled
  state depends on whether a timestamp is still valid.

### 2. Signatures

- Production expiry check example:
  `Date.parse(preflight.expiresAt) <= Date.now()`.
- Valid test fixture helper:
  `const futureExpiry = () => new Date(Date.now() + 60 * 60 * 1000).toISOString()`.
- Expired test fixture helper:
  `const pastExpiry = () => new Date(Date.now() - 60 * 1000).toISOString()`.

### 3. Contracts

- A fixture representing a currently valid token must be derived from the test
  clock with a margin comfortably longer than the test runtime, or the test must
  freeze time explicitly.
- A fixture representing expiry must derive a past timestamp from the same clock
  and assert the fail-closed UI state directly.
- Do not use a near-future calendar date as a permanent valid fixture. It turns
  a deterministic state test into a date-dependent failure.
- When fake timers would interfere with `userEvent`, prefer relative real-clock
  fixtures; otherwise configure `userEvent` with the fake-timer advancement
  hook and restore timers after each test.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Valid fixture timestamp is already before `Date.now()` | Test fixture bug; replace it with a relative future value before evaluating product behavior. |
| Expired fixture is before `Date.now()` | Activation or mutation control remains disabled and the expiry path is asserted. |
| Test runs slowly under full-suite load | Future margin remains valid; enabled-state assertion does not depend on wall-clock date. |
| Fake clock is used without restoring timers | Invalid test isolation; restore real timers in cleanup. |

### 5. Good/Base/Bad Cases

- Good: a preflight fixture expires one hour after the current test clock, and
  the activation test remains valid in any year.
- Base: a display-only timestamp has no expiry-dependent assertion and may use a
  fixed historical value.
- Bad: using `2026-07-17T10:00:00Z` as a valid preflight fixture causes the same
  activation test to fail after that instant.

### 6. Tests Required

- Valid-token flows must assert the protected action becomes enabled before
  clicking it.
- Expired-token flows must assert the same action remains disabled and no API
  mutation occurs.
- Re-run the focused test and the full `npm run check` gate after changing a
  time-sensitive fixture.

### 7. Wrong vs Correct

Wrong:

```ts
const preflight = { expiresAt: "2026-07-17T10:00:00Z" };
```

Correct:

```ts
const futureExpiry = () => new Date(Date.now() + 60 * 60 * 1000).toISOString();
const preflight = { expiresAt: futureExpiry() };
```

---

## Code Review Checklist

- Does the change use typed API wrappers and mapped domain data?
- Are loading, empty, error, and permission states handled explicitly?
- Are interactive controls accessible and keyboard friendly?
- Did the change reuse existing UI primitives, hooks, and utilities?
- Are tests updated for changed behavior?
- Does `npm run check` pass or is any skipped gate clearly justified?
- If the change touches API/domain contracts, are backend docs and frontend
  domain types synchronized?
