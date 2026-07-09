# Implementation Plan

## Checklist

- [x] Move work from `main` to a dedicated branch:
      `fix/frontend-review-polish`.
- [x] Review existing frontend specs and page/component conventions.
- [x] Inspect the current diff and identify behavior risks.
- [x] Add regression tests for:
      node detail tabs, Card hover-lift scope, and node migration Stepper label.
- [x] Fix node detail tabs:
      scrollable narrow layout, ARIA wiring, and keyboard navigation.
- [x] Fix Card primitive:
      remove default `card-lift`; keep opt-in call-site support.
- [x] Fix node migration Stepper:
      pass localized aria label and add zh/en locale strings.
- [x] Run focused regression tests:
      `npm run test -- --run src/pages/nodes-detail-page.test.tsx src/components/ui/__tests__/card.test.tsx src/components/node-migrate-wizard.test.tsx`.
- [x] Run full frontend gate:
      `cd web && npm run check`.
- [x] Run whitespace check:
      `git diff --check HEAD`.
- [x] Browser-check mobile node detail tab behavior.
- [x] Re-review the full branch diff after Trellis task creation.
- [x] Fix additional issues found in the second review:
      mounted tab panels for all `aria-controls`, remove shared PageHero
      decorative layers, remove app-shell ambient glow / login ambient motion,
      keep Framer Motion under the in-app power-saving boundary, and avoid
      `card-lift` transition overrides; hide a decorative trend-chart expand
      icon from assistive tech.
- [x] Re-run required checks after additional fixes.
- [x] Update frontend component spec with the manual-tab and motion/power-mode
      conventions learned during review.

## Validation Commands

```bash
git diff --check HEAD
cd web && npm run check
```

Focused regression command:

```bash
cd web && npm run test -- --run \
  src/pages/nodes-detail-page.test.tsx \
  src/components/ui/__tests__/card.test.tsx \
  src/components/node-migrate-wizard.test.tsx
```

Browser smoke:

- Start dev server with demo mode enabled.
- Open `/app/nodes/1` at mobile width.
- Confirm page-level horizontal overflow is false.
- Confirm tablist has `overflow-x: auto`, `overflow-y: hidden`, and
  `scrollWidth > clientWidth`.
- Confirm `ArrowRight` changes selection and URL query from Overview to Metrics.

## Final Verification Results

- `git diff --check HEAD` passed.
- `cd web && npm run check` passed: typecheck, lint, 119 test files / 510
  tests, and production build all completed successfully.
- Browser smoke on `http://127.0.0.1:5175/app/nodes/1` at `390x844` passed:
  no page-level horizontal overflow, tab row scrolls horizontally, all tab
  `aria-controls` targets exist, and `ArrowRight` selected Metrics with
  `?tab=metrics`.

## Review Focus

- Check visual changes do not turn shared primitives into overly animated
  global behavior.
- Check manual tab semantics match frontend a11y spec.
- Check locale keys exist in both zh and en.
- Check tests assert behavior rather than implementation-only details where
  practical.
