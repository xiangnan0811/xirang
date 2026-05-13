# Wave 3 Page Audit

## Purpose

Identify the next high-value frontend pages that still diverge from the shared workbench grammar after Wave 1 and Wave 2.

## Findings

- `web/src/pages/settings-page.tsx` is reachable through `/app/settings` and still uses a raw `h1` plus border-only tab list. It already has URL-backed tabs, roving keyboard behavior, and tests; it is a strong Wave 3 target for shell alignment without changing tab internals.
- `web/src/pages/credentials-page.tsx` is reachable through `/app/credentials` for admins and still uses an ad hoc header plus `Card/CardContent` table wrapper. It has no focused page test today. It is a strong Wave 3 target for inventory-surface alignment.
- `web/src/pages/users-page.tsx` is not currently routed. User management is implemented again inside `web/src/pages/settings-page.users.tsx`; broad cleanup of the duplicate standalone page would be a separate route/dead-code decision.
- `web/src/pages/backups-page.tsx` already uses `PageHero` and has a regression test for `Button asChild` single-child safety. Its child panels still use cards, but they are analytical panels rather than a simple inventory surface. Avoid broad redesign in this wave.
- `web/src/pages/automation-rules-page.tsx` and `web/src/pages/audit-page.tsx` also still use older card shells, but the current user-approved next step named Settings / Users / Credentials / Backups first. Defer automation/audit to a later wave unless a tiny shared fix is necessary.

## Recommended Scope

Primary targets:

1. Settings page shell and tab workbench styling.
2. Application Credentials page header/surface/test coverage.

Secondary:

1. Backups page small a11y/consistency checks only.

Deferred:

1. Standalone `UsersPage` route/dead-code cleanup.
2. Automation Rules and Audit page workbench conversion.
3. Broad Backup health/storage panel redesign.
