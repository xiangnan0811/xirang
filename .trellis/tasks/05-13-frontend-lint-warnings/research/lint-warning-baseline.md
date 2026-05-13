# Frontend Lint Warning Baseline

Date: 2026-05-13

Command:

```bash
cd web && npx eslint . --format json
```

## Current Warning Counts

- Total: 47 warnings
- `react-refresh/only-export-components`: 42
- `jsx-a11y/no-autofocus`: 4
- `react-hooks/incompatible-library`: 1

## Warning Surfaces

Fast Refresh boundary drift:

- Component files exporting reusable constants or helpers:
  - `web/src/components/node-metrics-chart.tsx`
  - `web/src/components/ui/badge.tsx`
  - `web/src/components/ui/button.tsx`
  - `web/src/components/ui/motion.tsx`
  - `web/src/components/ui/toast.tsx`
- Context provider files exporting both providers and consumer hooks:
  - `web/src/context/alerts-context.tsx`
  - `web/src/context/auth-context.tsx`
  - `web/src/context/command-palette-context.tsx`
  - `web/src/context/integrations-context.tsx`
  - `web/src/context/nodes-context.tsx`
  - `web/src/context/policies-context.tsx`
  - `web/src/context/shared-context.tsx`
  - `web/src/context/ssh-keys-context.tsx`
  - `web/src/context/tasks-context.tsx`
  - `web/src/context/theme-context.tsx`
- Router file declaring lazy page components and exporting a route object:
  - `web/src/router.tsx`

Accessibility warning surfaces:

- `web/src/components/totp-disable-dialog.tsx`
- `web/src/components/totp-setup-dialog.tsx`
- `web/src/components/ui/command-palette.tsx`
- `web/src/pages/login-page.tsx`

React Compiler compatibility warning:

- `web/src/pages/logs/logs-viewer.tsx` uses TanStack Virtual's `useVirtualizer()`, whose returned functions are intentionally not compiler-memoizable.

## Implementation Strategy

- Split shared constants and variant helpers into `.ts` modules imported by the component modules and callers.
- Split context consumer hooks into hook/core modules so provider `.tsx` files export only React components.
- Move lazy route component declarations into a component-only helper module while keeping `router.tsx` responsible for exporting the router object.
- Replace `autoFocus` props with explicit refs/effects tied to dialog or state transitions.
- Keep a narrow local eslint suppression for the TanStack Virtual hook call only if the component's behavior is otherwise unchanged.

## Verification Strategy

- Run `cd web && npm run lint` and require 0 warnings.
- Run `cd web && npm run typecheck`.
- Run `cd web && npm run test -- --run`.
- Run `cd web && npm run build`.
- Run `cd web && npm run check` for final frontend parity.

## Implementation Result

- Fast Refresh warnings were removed by splitting:
  - component constants/variants into `.ts` modules,
  - context provider/component exports from `*-context.hooks.ts` and
    `*-context.shared.ts`,
  - router lazy page components into `web/src/router-pages.tsx`,
  - the Sonner `toast` API into `web/src/components/ui/toast-sonner.ts`.
- `autoFocus` props were removed from the login 2FA step, TOTP dialogs, and
  command palette. Each surface now focuses the expected input through a ref
  when the dialog/state becomes active.
- The TanStack Virtual compiler warning is handled with a local
  `eslint-disable-next-line react-hooks/incompatible-library` and an adjacent
  comment explaining that the virtualizer's imperative helpers remain local to
  `LogsViewer`.
- `jsx-a11y/no-autofocus` is now an error because the current baseline is zero
  violations.

Verification completed:

- `cd web && npm run lint` -> 0 messages
- `cd web && npm run typecheck` -> pass
- `cd web && TMPDIR=/Users/weibo/Code/xirang/.tmp/tmp npm run test -- --run` -> 83 test files / 335 tests passed
- `cd web && npm run build` -> pass, with existing chunk-size warning
- `cd web && TMPDIR=/Users/weibo/Code/xirang/.tmp/tmp npm run check` -> pass
