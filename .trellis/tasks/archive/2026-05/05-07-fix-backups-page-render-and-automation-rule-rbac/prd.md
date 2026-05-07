# Fix Backups Page Render And Automation Rule RBAC

## Goal

Fix the backups page regression reported after login: the page must render
without the `React.Children.only expected to receive a single React element
child` crash, and the `/api/v1/automation-rules` endpoint must not return 403
for the role that is supposed to use the automation-rules feature.

## What I Already Know

- User observed the backups page rendering error in the browser error boundary.
- Console shows `GET /api/v1/automation-rules 403 (Forbidden)`.
- Console also shows a Recharts chunk error:
  `React.Children.only expected to receive a single React element child`.
- `/automation-rules` routes use `middleware.RBAC("automation:read")` and
  `middleware.RBAC("automation:write")`.
- `backend/internal/middleware/rbac.go` does not currently grant
  `automation:*` permissions to any role.
- The backups page uses Recharts panels and a `Button asChild` link.

## Assumptions

- Automation rules are an operational automation feature that can pause
  policies, disable policies, trigger tasks, and send notifications; treat
  management as admin-only and hide non-admin navigation entry points.
- The React child error is caused by a component that enforces a single child,
  most likely a Radix `asChild`/`Slot` usage or a Recharts child slot.

## Requirements

- Add missing backend RBAC permission coverage for automation-rule routes.
- Add full-router authorization tests for automation-rule routes through
  `AuthMiddleware` and `RBAC`, not handler-only tests.
- Fix the backups page render crash without changing the core data displayed by
  backup-health or storage panels.
- Add or update frontend tests that reproduce the relevant page render path.
- Preserve role-filtered navigation consistency if the backend authorization
  decision changes visible feature access.

## Acceptance Criteria

- [x] The backups page renders in tests without throwing
  `React.Children.only expected to receive a single React element child`.
- [x] Admin receives a handler response for
  `GET /api/v1/automation-rules`.
- [x] Operator and viewer receive 403 for automation-rule routes.
- [x] Focused backend and frontend tests pass.
- [x] Full relevant quality gates pass before commit.

## Out Of Scope

- Redesigning the backups page UI.
- Changing automation rule event/action semantics.
- Changing public release or deployment workflows.

## Technical Notes

- Relevant backend files:
  `backend/internal/api/router.go`,
  `backend/internal/middleware/rbac.go`,
  `backend/internal/api/handlers/automation_rule_handler_test.go`.
- Relevant frontend files:
  `web/src/pages/backups-page.tsx`,
  `web/src/components/backup-health-panel.tsx`,
  `web/src/components/storage-usage-panel.tsx`,
  `web/src/components/layout/navigation.ts`,
  `web/src/components/ui/command-palette.tsx`.
- Specs injected for implementation and check are in `implement.jsonl` and
  `check.jsonl`.
- `scripts/check-doc-freshness.sh` checks committed diffs by default; with the
  current uncommitted changed-file list injected through
  `DOC_FRESHNESS_CHANGED_FILES`, the check passes.
