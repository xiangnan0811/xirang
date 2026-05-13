# Frontend Workbench UX Wave 3

## Goal

Continue the console UI refinement by bringing the remaining high-value admin surfaces into the shared workbench grammar. This wave focuses on Settings and Application Credentials, with Backups limited to small consistency checks because it already has the shared page header.

## What I Already Know

- Wave 1 and Wave 2 are merged into `main` and released through `v0.30.0`.
- The user agreed to continue from the prior next-step recommendation.
- Current `main` is clean and synchronized with `origin/main`.
- Work is on branch `feat/frontend-workbench-ux-wave3`.
- The shared workbench primitives already exist: `PageHero`, `DataSurface`, `DataSurfaceHeader`, `DataSurfaceToolbar`, `DataSurfaceContent`, `DataSurfaceFooter`, and `StatCardsSection`.
- `web/src/pages/settings-page.tsx` still uses a raw `h1` and border-only tab bar instead of `PageHero` + workbench tab surface.
- `web/src/pages/credentials-page.tsx` still uses an ad hoc header and `Card/CardContent` table wrapper.
- `web/src/pages/users-page.tsx` exists but is not routed; user management is currently reachable through `SettingsPage` via `settings-page.users.tsx`.
- `web/src/pages/backups-page.tsx` already uses `PageHero`; this wave should avoid broad rewrites of backup health/storage panels.

## Research Notes

- See `research/wave3-page-audit.md` for the page inventory and scope decision.
- Settings already has URL-backed `tab` state, arrow-key navigation, and tests for tab semantics; the work should preserve this behavior.
- Credentials has no focused page test today; add coverage for the new workbench surface and the empty/list states.

## Assumptions

- This is a frontend-only UX refinement.
- No backend API, RBAC, permission, release, or Docker workflow changes are required.
- Existing reachable route behavior should be preserved.
- The visual direction remains a restrained operations workbench: compact, scannable, dense enough for repeated use, and not marketing-style.

## Requirements

### R1. Settings workbench shell

- Replace the raw Settings title with `PageHero`.
- Add compact metadata that reflects visible tab count and admin/non-admin scope.
- Convert the tab row to a workbench-style segmented/tab control consistent with Logs and Reports.
- Preserve URL-backed `tab` behavior.
- Preserve admin-only tabs and non-admin fallback to Personal/Account.
- Preserve `role="tablist"`, `role="tab"`, `role="tabpanel"`, `aria-controls`, `aria-selected`, `tabIndex`, and arrow/Home/End keyboard behavior.
- Avoid changing the internals of each Settings tab unless a small a11y/icon fix is directly needed.

### R2. Application Credentials inventory

- Replace the ad hoc header with `PageHero`.
- Convert the table card wrapper to `DataSurface` with header/content hierarchy.
- Add compact metadata for total credentials, password-configured credentials, referenced credentials, and unused credentials when derivable from loaded data.
- Preserve create/edit/delete behavior and the existing credential editor dialog.
- Preserve empty and loading states.
- Add decorative `aria-hidden` to labeled button icons where applicable.
- Keep the table dense and readable on desktop; avoid card-in-card page sections.

### R3. Backups consistency check

- Keep the existing `PageHero` and backup health/storage panels.
- Make only small consistency/a11y fixes if discovered.
- Do not redesign backup health charts or storage cards in this wave.

### R4. Tests and verification

- Update Settings page tests for the new workbench header/metadata while preserving existing tab behavior tests.
- Add focused Credentials page tests for the PageHero/DataSurface shell, empty state, list state, and action semantics.
- Run focused tests for changed pages.
- Run `cd web && npm run lint`, `cd web && npm run typecheck`, and `cd web && TMPDIR=/Users/weibo/Code/xirang/.tmp/tmp npm run check` before PR.
- Perform browser-level visual verification for Settings and Credentials if the local app can be served.

## Acceptance Criteria

- [x] Settings uses the shared workbench page shell and preserves tab URL/keyboard/a11y behavior.
- [x] Credentials uses `PageHero` + `DataSurface` and preserves create/edit/delete behavior.
- [x] Credentials exposes useful compact metadata derived from loaded credentials.
- [x] Backups is either left unchanged with rationale or receives only small consistency fixes.
- [x] Focused tests for changed pages pass.
- [x] Full frontend check passes before PR.
- [ ] Work is delivered through PR, monitored until CI passes, merged, and post-merge automation is monitored.

## Verification Evidence

- Focused page tests passed: `cd web && TMPDIR=/Users/weibo/Code/xirang/.tmp/tmp npm run test -- --run settings-page credentials-page backups-page` (5 files, 25 tests).
- Frontend lint passed: `cd web && npm run lint`.
- Frontend type-check passed: `cd web && npm run typecheck`.
- Full frontend gate passed: `cd web && TMPDIR=/Users/weibo/Code/xirang/.tmp/tmp npm run check` (85 test files, 351 tests, production build). Vite still reports the existing non-failing chunk-size warning for the main bundle.
- Whitespace check passed: `git diff --check`.
- Browser verification passed in headless Chrome against local Vite dev server with mocked `/api/v1` data:
  - Settings rendered the `PageHero`, `settings.tabListLabel`, admin metadata, selected Personal tab, and no horizontal document overflow.
  - Credentials rendered the `PageHero`, `DataSurface`, four metadata badges, sample rows, and no horizontal document overflow.
  - Screenshots were written to `/tmp/xirang-wave3-screens-final/settings.png` and `/tmp/xirang-wave3-screens-final/credentials.png`.
  - Existing React Router v7 future-flag development warnings were observed and ignored as unrelated framework noise.

## Out Of Scope

- Backend/API/RBAC changes.
- Full redesign of every remaining route.
- Refactoring the unreachable standalone `UsersPage` unless it becomes routed.
- Large backup health/storage panel redesign.
- New dependencies or a new design-system layer.
- Changing public release or Docker workflow contracts.

## Implementation Plan

### Phase A. Settings shell

- Add `PageHero`, badges, and workbench tab styling.
- Preserve the current URL and keyboard behavior.
- Update Settings tests.

### Phase B. Credentials inventory

- Add `PageHero`, metadata badges, and a `DataSurface`.
- Preserve table, empty/loading states, and dialogs.
- Add focused tests.

### Phase C. Verification and delivery

- Run focused and full frontend checks.
- Browser-verify the changed pages.
- Commit, archive Trellis task, create PR, monitor CI, merge, and monitor Release Please/release/Docker automation.
