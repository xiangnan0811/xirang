# Wave 2 Page Audit

## Purpose

Identify second-wave frontend pages that still diverge from the workbench grammar added by `05-13-frontend-workbench-ux-redesign`.

## Findings

- Wave 1 is present on `main` via `6eac43b feat(web): refine console workbench UX` and added `PageHero`, `DataSurface`, and responsive `StatCardsSection` primitives.
- `web/src/pages/logs/logs-page.tsx` still starts with a raw tab bar and wraps task logs in `Card/CardContent`; it should use a compact page header and a single data tool surface while preserving URL-backed filters.
- `web/src/pages/notifications-page.tsx` uses a large five-metric `StatCardsSection`, `DeliveryStatsCard`, and `AlertCenter`; the page should become an alert triage workbench with compact metadata and a data surface.
- `web/src/pages/notifications/alert-center.tsx` wraps filters/list/pagination in `Card/CardContent`; replace this with `DataSurface` sections.
- `web/src/pages/service-monitors-page.tsx` uses an ad hoc header and raw table card; it is a good fit for `PageHero` + `DataSurface`.
- `web/src/pages/policies-page.tsx` wraps controls in an outer `Card/CardContent` and has an inner desktop table card; convert to a single workbench data surface and remove obvious card nesting.
- `web/src/pages/reports-page.tsx` uses ad hoc tabs/header/cards; align header and tab grammar, but avoid a full report-card redesign.
- `web/src/pages/backups-page.tsx` already uses `PageHero`; leave out of primary scope.

## Recommended PR Scope

Primary target list:

1. Logs
2. Notifications / Alert Center
3. Service Monitors
4. Policies
5. Reports

Defer broad settings/users/credentials/dashboard detail pages to later waves unless a small shared fix naturally applies.
