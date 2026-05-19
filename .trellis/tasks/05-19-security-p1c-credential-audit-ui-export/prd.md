# P1c Credential Audit Listing, Filters, and Export UI

## Goal

Make credential-use audit events directly reviewable by administrators through backend list/export APIs and frontend UI, beyond the Settings high-level risk summary.

## Requirements

- Add admin-only backend list and export endpoints for credential-use audit events.
- Support practical filters such as action, purpose, outcome, user, node, SSH key, task, task run, policy, and date range.
- Add frontend API mappers and a UI surface for browsing and exporting credential-use events.
- Reuse existing audit/settings UI patterns and maintain accessibility for filters, tables/cards, empty states, and export actions.
- Keep event details secret-safe; do not display raw secrets, terminal streams, command output, file contents, or executor config.

## Acceptance Criteria

- Admin can list, filter, and export credential-use audit records.
- Non-admin roles are denied by backend RBAC and do not see a normal frontend navigation path.
- Frontend mappers safely normalize snake_case fields, unknown action/severity/outcome values, nullable IDs, and metadata.
- Tests cover backend RBAC/filter/export behavior and frontend mapping/rendering/export affordances.
