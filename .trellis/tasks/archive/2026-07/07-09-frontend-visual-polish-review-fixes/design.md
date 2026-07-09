# Design: Frontend visual polish and review fixes

## Boundaries

The task is scoped to the React frontend under `web/src/` plus `.gitignore`.
It does not change backend contracts, API wrappers, routing contracts, or
deployment behavior.

## UI Architecture

- Shared primitives remain in `web/src/components/ui/`.
- Page-level layout changes stay in route pages or layout components.
- Motion is centralized through existing `framer-motion` usage and the new
  small `Reveal` / `Stagger` helpers.
- `Card` stays a quiet structural primitive. Hover lift is a utility class
  applied only by surfaces that are intentionally interactive or repeated-list
  cards.

## Node Detail Tabs

The node detail page uses manual tab controls because the existing page stores
the selected tab in the URL query string. The tab row should:

- render as a horizontal scroll container on narrow viewports;
- keep the page itself from horizontally overflowing;
- use `role="tablist"`, `role="tab"`, `aria-selected`, `aria-controls`,
  `tabIndex`, and a matching `role="tabpanel"`;
- support `ArrowLeft`, `ArrowRight`, `Home`, and `End`;
- preserve the existing `?tab=<id>` URL behavior.

This preserves the current URL-backed state model while closing the responsive
and accessibility gap.

## Stepper Accessibility

The shared `Stepper` still provides a fallback label for isolated rendering, but
product flows should pass localized labels. The node migration wizard passes
`nodes.migrateStepsAriaLabel`, with matching zh/en locale entries.

## Testing Strategy

- Unit/component tests cover the reviewed failures directly:
  - node detail tab scrolling/accessibility/keyboard behavior;
  - Card default-vs-opt-in `card-lift`;
  - node migration Stepper localized label.
- Second-pass review adds guardrails for:
  - `aria-controls` pointing to mounted tab panels for every node detail tab;
  - shared `PageHero` staying free of hidden decorative background layers;
  - global CSS avoiding app-shell ambient glow layers and animated login
    background decoration;
  - Framer Motion respecting the in-app power-saving mode through the root
    motion preference boundary.
- Full frontend gate validates type safety, lint/a11y static rules, tests with
  coverage, and production build.
- Browser smoke checks the mobile viewport behavior that jsdom cannot fully
  model.

## Risks And Rollback

- Visual polish changes are easy to roll back by reverting affected frontend
  files.
- The highest-risk behavior is manual tab keyboard handling; regression tests
  cover URL-backed selection changes and ARIA contracts.
- Browser smoke is required because responsive scroll behavior cannot be fully
  proven by unit tests.
