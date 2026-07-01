# 前端审查整改第二阶段设计

## Scope

This task implements one bounded UI remediation from the original frontend audit: reduce the desktop node table action-cluster density while preserving every existing action and accessibility contract.

The task intentionally avoids broad redesign, React tooling installation, backend work, and performance architecture changes.

## Current State

- `web/src/pages/nodes-page.table.tsx` renders the desktop node table.
- Each table row currently renders many inline actions in one cluster: test connection, Fleet Doctor, logs link, Web Terminal, file browser, edit, migrate, delete, and manual backup.
- `web/src/components/ui/dropdown-menu.tsx` already exposes Radix-backed `DropdownMenu`, `DropdownMenuTrigger`, `DropdownMenuContent`, `DropdownMenuItem`, `DropdownMenuSeparator`, and `DropdownMenuLabel` primitives.
- Existing patterns such as `web/src/components/ssh-key-actions-menu.tsx` and `web/src/pages/notifications/alert-bulk-actions.tsx` already use dropdown menus for secondary actions.
- `web/src/pages/nodes-page.test.tsx` already covers logs link semantics, Fleet Doctor, mobile More menu, test-connection failure, view switching, and filter reset.

## Design Decisions

### Primary actions remain inline

Keep the frequent and scan-critical operations directly visible:

- Test connection.
- View logs as a real `<Link>` inside the existing `Button asChild` composition.
- Manual backup.
- More actions trigger.

### Secondary actions move into row overflow

Move the remaining actions into a node-specific dropdown menu:

- Fleet Doctor.
- Web Terminal when `isAdmin` is true.
- File Browser when `canBrowseNodeFiles` is true.
- Edit.
- Migrate when `onMigrate` exists.
- Delete after a separator with destructive styling.

### Accessibility

- The overflow trigger must be an icon button with a node-specific accessible label, e.g. `t("nodes.moreActionsAriaLabel", { name: node.name })`.
- Menu item icons are decorative and must use `aria-hidden`.
- Delete must use existing destructive token styles and remain visually separated.
- The logs action must remain a link, not a button that imperatively navigates.
- Existing icon-only primary buttons must keep accessible labels.

### i18n

Add only the labels needed for the new overflow trigger if no existing key fits.

Recommended keys:

- `nodes.moreActions`: visible/title label for the trigger if needed.
- `nodes.moreActionsAriaLabel`: node-specific accessible label.

Use Chinese and English locale files together.

### Testing strategy

Tests live in `web/src/pages/nodes-page.test.tsx` because this is route-level behavior, not a new primitive.

Add tests before production changes:

- RED test for desktop action grouping.
- Regression test for overflow action reachability.

The tests should use Testing Library role queries rather than implementation selectors where possible.

### Deferred items

- React Scan / react-grab / react-doctor remain deferred because package/tool identity or dependency compatibility is not resolved.
- `useConsoleData` render-fan-out optimization remains deferred until render observability is available.
- Generic coverage improvement remains deferred.
- Existing non-blocking test warnings remain deferred unless they are reproduced inside the changed nodes tests.

## Rollback

This task touches the nodes table and node page tests only, plus locale files if new labels are needed. Revert those files to roll back the feature without affecting first-stage remediation commits.
