# Component Guidelines

> How components are built in this project.

---

## Overview

Components use React 18 functional components, TypeScript props, Tailwind-style
utility classes, and small shared primitives from `web/src/components/ui/`.
The UI uses a restrained admin-console style: dense, scannable panels, clear
tables, accessible dialogs, and reusable form controls.

Prefer composition with existing primitives over one-off markup. Check
`web/src/components/ui/` before adding new buttons, cards, dialogs, inputs,
switches, badges, skeletons, pagination, or empty states.

---

## Component Structure

- Keep components focused on one responsibility. Large pages are split into
  fragments such as `nodes-page.grid.tsx`, `tasks-page.table.tsx`, and
  `settings-page.users.tsx`.
- Define local helper types near the component when they are not shared. Use
  `web/src/types/domain.ts` only for cross-module domain shapes.
- Keep data normalization out of components when it belongs in API mappers.
  Example: `web/src/lib/api/overview-api.ts` converts backend traffic payloads
  before `overview-page.traffic.tsx` renders them.
- Use stable UI primitives instead of recreating styles. Examples:
  `Button`, `Card`, `Dialog`, `Input`, `Select`, `Switch`, `Badge`, and
  `PageHero`.
- Use `lucide-react` icons for recognizable actions instead of hand-written SVG
  icons.

---

## Props Conventions

- Prefer explicit object props with named fields. Avoid boolean prop clusters
  when a variant or small union type communicates intent better.
- Use domain types from `web/src/types/domain.ts` for persisted/API-backed data.
  Keep API raw response types private to API wrapper files.
- Event handlers should be named by action, for example `onSave`, `onClose`,
  `onConfirm`, `onRefresh`, or `onSelectionChange`.
- If a component renders user-visible async states, include explicit loading,
  empty, and error states instead of relying on a parent to hide it.

---

## Styling Patterns

- Styling is primarily utility-class based. Use `cn()` from
  `web/src/lib/utils.ts` when composing conditional classes.
- Design tokens and shared utility variants should live in the `ui/` component
  layer. Reuse `buttonVariants`, card primitives, badges, and mono chips where
  possible.
- Keep admin-tool surfaces compact. Avoid marketing-style hero composition for
  internal pages; existing pages use cards, tables, filters, segmented toggles,
  and small status summaries.
- For charts, reuse chart helpers such as `web/src/lib/chart-theme.ts` and
  existing Recharts patterns in node metrics, overview traffic, and dashboard
  panels.
- Keep responsive layouts explicit with grid/flex constraints. Do not rely on
  text overflow or dynamic content to size fixed controls.

---

## Accessibility

- Use existing accessible primitives backed by Radix UI where available:
  dialogs, dropdown menus, select, switches, checkboxes, and alert dialogs.
- Icon-only buttons need accessible labels. Existing UI primitives support
  semantic button behavior; keep labels/tooltips where actions are not obvious.
- Preserve keyboard workflows in dialogs, tables, pagination, mobile navigation,
  and command palette interactions.
- Error, warning, and status messages should use the established alert/inline
  alert patterns so assistive technologies can discover them.
- Tests should cover accessibility-sensitive UI states when behavior is not
  obvious. Examples already exist for protected routes, mobile navigation,
  dialogs, empty states, and inline alerts.

---

## Common Mistakes

- Do not create new card/button/input variants before searching
  `web/src/components/ui/`.
- Do not put API snake_case payloads directly into components. Map them in
  `web/src/lib/api/*`.
- Do not hide critical state behind color alone; include text, badges, or icons.
- Do not introduce new dependencies for UI primitives already covered by Radix,
  lucide, Recharts, or local components.
- Do not add in-app explanatory text about implementation details, shortcuts, or
  visual design unless the product itself needs that copy.
