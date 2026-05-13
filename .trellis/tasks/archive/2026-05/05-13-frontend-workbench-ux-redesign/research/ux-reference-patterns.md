# UX Reference Patterns For Xirang Workbench Redesign

## Purpose

This note converts the read-only frontend audit and external product references
into implementation constraints for Xirang. It is not a visual cloning brief.
The goal is to borrow durable operations-console patterns and adapt them to the
existing React/Tailwind/Radix codebase.

## Local Diagnosis Summary

- The console currently reads as a soft card-based SaaS template more than a
  dense operations tool.
- The problem is systemic across tokens, card primitives, stat cards, page
  headers, navigation, tables, filters, and row actions.
- The first repair should focus on shared workbench grammar and high-traffic
  pages rather than page-by-page decorative restyling.

## External References

### Grafana

Official docs:

- https://grafana.com/docs/grafana/latest/panels-visualizations/
- https://grafana.com/docs/grafana/latest/visualizations/panels-visualizations/visualizations/
- https://grafana.com/docs/grafana/latest/panels-visualizations/panel-inspector/

Useful patterns:

- Treat panels as inspectable operational instruments, not decorative cards.
- Let chart/table panels expose controls for investigation near the data.
- Use clear panel boundaries and compact panel headers instead of oversized
  section decoration.

Implication for Xirang:

- Overview traffic and node matrix should be secondary operational instruments
  after health/abnormality triage, with compact controls and inspectable state.

### Proxmox VE

Official docs:

- https://pve.proxmox.com/pve-docs/
- https://pve.proxmox.com/pve-docs-8/pve-admin-guide.html

Useful patterns:

- A management console can be visually plain and still effective when resource
  hierarchy, object selection, and action placement are clear.
- Object navigation and status/state summaries matter more than large KPI
  blocks.

Implication for Xirang:

- Nodes and detail-entry paths should emphasize fleet hierarchy, current
  health, and available operations. Do not bury object identity under repeated
  metric cards.

### Portainer

Official docs:

- https://docs.portainer.io/2.27/user/docker/dashboard
- https://docs.portainer.io/user/docker/containers/view
- https://docs.portainer.io/user/docker/containers/inspect

Useful patterns:

- Inventory pages work best when filters, table/list content, inspect actions,
  and destructive actions share one predictable grammar.
- Inspect/drill-down affordances should be explicit and stable.

Implication for Xirang:

- Nodes, SSH Keys, and Tasks should share a data-surface pattern: compact
  header, toolbar/filter row, dense rows/cards, stable pagination/selection, and
  grouped row actions.

### Sentry

Official docs:

- https://docs.sentry.io/product/issues/issue-details/
- https://docs.sentry.io/product/issues/states-triage/

Useful patterns:

- Triage screens should lead with what needs attention, how severe it is, and
  what changed recently.
- State transitions should be visible and actions should match the workflow.

Implication for Xirang:

- Overview and Tasks should prioritize failed/running/paused work and recent
  operational exceptions before generic aggregate success metrics.

### Vercel Web Interface Guidelines

Latest source checked on 2026-05-13:

- https://raw.githubusercontent.com/vercel-labs/web-interface-guidelines/main/command.md

High-value checkpoints for this task:

- Use native link semantics for navigation.
- Use native button semantics for commands.
- Keep focus states and accessible names.
- Avoid inaccessible icon-only controls.
- Keep numeric content, long text, and dynamic labels within stable layout
  constraints.
- Prefer URL-backed state for navigable/filterable workflows when applicable.

Implication for Xirang:

- Route-changing buttons in overview/task surfaces should become links where
  feasible.
- Row action icon buttons must retain accessible labels and tooltips where the
  icon is not universally obvious.
- Responsive grid/table layouts need fixed constraints and column priority
  instead of relying on overflow alone.

## Design Direction

Use a quiet, utilitarian operations-console style:

- neutral base surfaces,
- stronger semantic status colors,
- compact typography,
- stable tabular numbers,
- clear toolbars and filter rows,
- dense tables/cards with predictable row actions,
- restrained shadows,
- no decorative hero or marketing layout.

## First-PR Scope Recommendation

The best first slice is "workbench MVP":

- shared visual tokens and core primitives,
- app shell/navigation refinement,
- Overview,
- Nodes,
- Tasks,
- SSH Keys,
- focused tests and screenshot verification.

This slice is broad enough to change the product feel, but small enough to keep
review, test, and rollback realistic.
