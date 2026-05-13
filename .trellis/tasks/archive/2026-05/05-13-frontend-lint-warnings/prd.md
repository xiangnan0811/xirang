# Clean Up Frontend Lint Warnings

## Problem

The frontend lint gate currently passes but reports 47 warnings. The warnings are not cosmetic noise: they mark React Fast Refresh boundary drift, four accessibility-sensitive `autoFocus` usages, and one React Compiler compatibility warning around TanStack Virtual.

Leaving the warnings in place makes real regressions harder to see and keeps the frontend quality baseline ambiguous after the dashboard polish work.

## Goals

- Reduce `cd web && npm run lint` to 0 warnings without disabling rule classes globally.
- Preserve current routing, provider behavior, keyboard workflows, and UI output.
- Split non-component exports out of React component modules where Fast Refresh requires a component-only boundary.
- Replace `autoFocus` props with explicit focus handling that does not trigger `jsx-a11y/no-autofocus`.
- Keep the TanStack Virtual compatibility warning explicit and justified if it cannot be removed without replacing the library.
- Keep changes narrow to frontend source, lint config comments, and task metadata.

## Non-Goals

- Redesign frontend visuals or change dashboard information architecture.
- Change backend API contracts or auth/RBAC behavior.
- Introduce new state, routing, or UI dependencies.
- Relax lint rules to hide current warnings.

## Scope

- `web/src/components/**`
- `web/src/components/ui/**`
- `web/src/context/**`
- `web/src/pages/**`
- `web/src/router.tsx` and any route helper modules required for Fast Refresh boundaries
- `web/eslint.config.js` only if comments or debt-rule severity need to match the new baseline

## Acceptance Criteria

- `cd web && npm run lint` reports 0 warnings.
- `cd web && npm run check` passes.
- TypeScript imports remain explicit and type-only imports use `import type`.
- Provider modules export components only; hooks/constants move to non-component modules or hook modules.
- Dialog, command palette, and login focus behavior remains keyboard-friendly.
- Any retained rule suppression is local, commented with a concrete reason, and limited to the incompatible third-party library call.
- Changes are merged through a PR after CI passes, then post-merge automation is checked and recorded.
