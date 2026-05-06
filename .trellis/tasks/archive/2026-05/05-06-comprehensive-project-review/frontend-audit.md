# Frontend Audit

## Scope

- Slice: React/Vite frontend only.
- Write scope used: `web/**` and this task-local audit note.
- Explicitly not changed: `backend/**`, root docs, workflows, deploy files, and scripts.

## Fixed Findings

### Medium: self-backup API result lost the user-facing filename

- Evidence: the backend backup response includes `filename`, `path`, `size`, and `sha256`, but the frontend `BackupResult` type omitted `filename` and the success toast surfaced only the local path/size wording.
- Fix: added `filename` to the frontend API contract and updated localized success messages to show filename and size.
- Tests: covered in `web/src/lib/api/system-api.test.ts`.

### Medium: command palette route index drifted from the navigation registry

- Evidence: the command palette had a hard-coded route list that omitted reachable app sections such as dashboards, credentials, automation rules, and service monitors.
- Fix: changed the command palette to use the canonical `navItems` registry and added an accessible dialog description.
- Tests: added `web/src/components/ui/command-palette.test.tsx`.

### Medium: dashboard cards used click handlers instead of link semantics

- Evidence: dashboard cards were rendered as clickable non-interactive containers, which blocked normal link behavior and produced accessibility lint warnings.
- Fix: moved navigation onto real `Link` elements, preserved card actions, associated create-dialog labels with inputs, and kept time formatting consistent with the shared formatter.
- Tests: existing dashboard page tests were included in targeted verification.

### Medium: node grid cards exposed non-interactive keyboard focus

- Evidence: desktop node cards used `tabIndex`, click handlers, and key handlers on a non-interactive container.
- Fix: removed card-level interactivity and let the existing details link handle focus/navigation; focusing the link now marks the node as selected for preview state.
- Tests: updated `web/src/pages/nodes-page.test.tsx`.

### Medium: service monitor form accepted invalid frontend-side input states

- Evidence: required fields were validated only through toasts, HTTP/TCP targets were not checked before API submission, numeric fields could fall outside practical API ranges, and table row actions used generic accessible names.
- Fix: added inline field validation, target-shape validation, numeric clamping for interval/timeout/status code fields, table header scopes, monitor-specific action labels, and labeled dynamic header inputs.
- Tests: added `web/src/pages/service-monitors-page.test.tsx`.

### Low: backups page primary action was a no-op button

- Evidence: the backups page rendered a "new backup" button with no action, creating a dead control in a user-facing page.
- Fix: changed the control to a link to the task configuration page and renamed the label to "Configure backup task" in English and Chinese.
- Tests: covered by typecheck and full frontend checks.

### Low: status page polling did not guard aborted updates

- Evidence: status polling could still process an aborted request and some status icons were exposed as unnamed content.
- Fix: added abort-error handling, avoided state updates after abort, localized the manual check loading label, and marked decorative icons hidden from assistive tech.
- Tests: covered by typecheck and full frontend checks.

### Low: accessibility and lint debt in shared frontend surfaces

- Evidence: task pause radio labels, SSH key rotation labels, and the tree component hook dependency emitted frontend lint warnings; several stale eslint-disable comments were unused.
- Fix: associated labels with stable ids, added screen-reader labels where visible labels were nested controls, stabilized the tree fallback expanded set, and removed stale disable comments.
- Tests: covered by `npm run lint`, targeted tests, and `npm run check`.

## Deferred Findings

### Low: existing Fast Refresh lint warnings in shared exports

- Status: deferred.
- Evidence: `npm run check` still reports `react-refresh/only-export-components` warnings in UI primitives, contexts, router exports, and the node metrics chart.
- Reason: fixing this cleanly requires broader module splitting across shared exports and is not necessary for production correctness; lint exits successfully.

### Low: existing autofocus warnings need product-level focus policy

- Status: deferred.
- Evidence: warnings remain in login, TOTP dialogs, and command palette autofocus behavior.
- Reason: the current behavior appears intentional for keyboard-first flows; changing it should be decided consistently across auth and command surfaces.

### Low: logs viewer TanStack Virtual warning remains

- Status: deferred.
- Evidence: `npm run check` still reports the React compiler incompatible-library warning for TanStack Virtual usage in the logs viewer.
- Reason: this is library-specific and changing the virtualizer path is higher-risk than the audit slice warrants.

### Low: build bundle-size warning remains

- Status: deferred.
- Evidence: Vite reports chunks over the configured warning limit, including the main app chunk and chart/vendor chunks.
- Reason: meaningful improvement likely needs a chunking strategy and possibly Vite/root build configuration work outside this focused frontend correctness pass.

### Low: Vitest localstorage-file warning remains

- Status: deferred.
- Evidence: test runs emit ``--localstorage-file` was provided without a valid path``.
- Reason: tests pass with `TMPDIR=/tmp`; fixing the warning appears to be tooling/config cleanup rather than an app correctness issue.

## Verification

- `cd web && npm run typecheck`: passed.
- `cd web && TMPDIR=/tmp npm run test -- command-palette service-monitors-page nodes-page dashboards-page system-api`: passed, 7 files and 34 tests.
- `cd web && TMPDIR=/tmp npm run check`: passed. This covered typecheck, lint, full Vitest coverage, and production build. Full test suite passed with 80 files and 325 tests.
- `cd web && npm run lint`: passed with warning-only residual debt listed above.
- `git diff --check -- web .trellis/tasks/05-06-comprehensive-project-review`: passed.
