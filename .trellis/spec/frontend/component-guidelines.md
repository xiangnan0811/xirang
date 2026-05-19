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

### Convention: Radix `asChild` Single-Child Composition

**What**: Components backed by Radix `Slot` or Radix primitives with `asChild`
must pass exactly one React element child to the slot. Shared primitives such
as `Button` should branch the slotted path so conditional loader/icons are not
rendered as sibling children beside the consumer's element.

**Why**: Radix validates slot composition with `React.Children.only`. Passing
multiple children, including `null` from a conditional expression plus the real
child, crashes the page with `React.Children.only expected to receive a single
React element child`.

**Wrong**:

```tsx
const Comp = asChild ? Slot : "button";

return (
  <Comp>
    {loading ? <Loader2 /> : null}
    {children}
  </Comp>
);
```

**Correct**:

```tsx
if (asChild) {
  return <Slot>{children}</Slot>;
}

return (
  <button disabled={loading}>
    {loading ? <Loader2 aria-hidden /> : null}
    {children}
  </button>
);
```

**Tests**: When changing a primitive that supports `asChild`, add a render test
using a link-like child so a `React.Children.only` regression fails in unit
tests.

### Convention: Permission-aware navigation registry

**What**: Use `getVisibleNavItems(role)` as the canonical source for
role-filtered application navigation. Components that render navigation-like
entry points, including sidebars, mobile drawers, and command palettes, should
consume this helper instead of iterating over `navItems` directly.

**Why**: The backend rejects protected feature routes through RBAC. If one
frontend entry point bypasses the canonical filter, non-admin users can still
navigate into a page that immediately fails API calls with 403.

**Example**:

```tsx
const { role } = useAuth();
const visibleNavItems = useMemo(() => getVisibleNavItems(role), [role]);

return visibleNavItems.map((item) => (
  <NavLink key={item.path} to={item.path}>
    {t(item.titleKey)}
  </NavLink>
));
```

**Tests**: When a navigation item becomes role-restricted, update the registry
test and at least one alternate navigation surface test, such as the command
palette, to assert the item is hidden for non-authorized roles.

### Convention: Fast Refresh export boundaries

**What**: `.tsx` files that export React components should not also export
plain constants, variant helpers, third-party APIs, or consumer hooks. Put
those shared values in sibling `.ts` modules and import them from component
files and call sites.

**Why**: Vite Fast Refresh can only preserve component state reliably when a
module's public exports are component-shaped. Mixed exports trigger
`react-refresh/only-export-components` warnings and make future lint output
harder to trust.

**Examples**:

- Keep `Button` in `button.tsx`; put `buttonVariants` in
  `button.variants.ts`.
- Keep `Toaster` in `toast.tsx`; put the `toast` API re-export in
  `toast-sonner.ts`.
- Keep route object creation in `router.tsx`; put lazy page component exports
  in a component-only router page module.

**Tests**: After splitting a shared export, update imports and mocks to the new
module path, then run `cd web && npm run lint` to confirm the Fast Refresh
warning is gone rather than hidden.

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

### Convention: SSH Key least-privilege scope UI

**What**: SSH key inventory and edit surfaces should render least-privilege scope
metadata as compact, explicit form fields and badges. The canonical domain fields
are `disabled`, `expiresAt`, `allowedPurposes`, `allowedNodeIds`,
`allowedNodeTags`, and response-derived `broadScope`.

**Why**: Scope metadata is security-sensitive and advisory-to-enforcement: the
backend enforces disabled/expiry/purpose/node/tag restrictions, while the
frontend helps admins see broad or restricted keys without inventing separate
client-side authorization rules.

**Contracts**:

- SSH key create/edit/batch-import UIs may expose disabled, expiry, allowed
  purposes, allowed node IDs, and allowed node tags; empty scope inputs must be
  described as compatibility defaults that allow all for that dimension.
- Inventory table/grid badges should distinguish disabled, expiring/expiry-set,
  broad-scope, and restricted keys with text plus badge tone. Do not rely on
  color alone.
- Scope controls must be labeled (`label htmlFor`, `aria-label`, or equivalent),
  and decorative icons in scope-related buttons/badges must use `aria-hidden`.
- The UI must not enrich scope cards with node hostnames, usernames plus hosts,
  private keys, passwords, credential audit metadata, raw endpoints, or one-click
  remediation actions. Mutations still go through the normal SSH key edit flow.
- Components should consume mapped camelCase fields from `SSHKeyRecord`; do not
  read raw `allowed_purposes`, `allowed_node_ids`, `allowed_node_tags`,
  `expires_at`, or `broad_scope` directly.

**Tests**: When changing the SSH key scope UI, cover at least one form path and
one inventory badge path, including broad-scope and disabled states, plus the
existing a11y label/icon expectations.

### Convention: Workbench Page Shells

**What**: Top-level console routes should use `PageHero` for the page title,
description, compact metadata, and primary actions. Inventory or operational
tool areas should use `DataSurface` plus `DataSurfaceHeader` /
`DataSurfaceContent` instead of wrapping whole page sections in generic cards.
Use cards for repeated items, dialogs, compact widgets, and genuinely framed
content, not for a page-section shell around another card-like surface.

**Why**: The console is an operations workbench. Consistent page shells make
routes easier to scan, keep primary actions predictable, and avoid stacked
card-in-card layouts that make internal tools feel busy and less polished.

**Example**:

```tsx
return (
  <div className="animate-fade-in space-y-5">
    <PageHero
      title={t("credentials.pageTitle")}
      subtitle={t("credentials.pageDesc")}
      meta={<Badge tone="info">{t("credentials.totalMeta", { count })}</Badge>}
      actions={<Button onClick={openCreateDialog}>{t("credentials.createBtn")}</Button>}
    />

    <DataSurface>
      <DataSurfaceHeader
        title={t("credentials.surfaceTitle")}
        description={t("credentials.surfaceDesc")}
      />
      <DataSurfaceContent className="p-0">
        <CredentialTable rows={credentials} />
      </DataSurfaceContent>
    </DataSurface>
  </div>
);
```

**URL-backed controls**: When a page stores tabs, filters, or view state in the
URL, clone the existing `URLSearchParams` and update only the target key so
unrelated query parameters survive user interaction.

```tsx
const next = new URLSearchParams(searchParams);
next.set("tab", tab);
setSearchParams(next, { replace: true });
```

**Tests**: Page-shell changes should assert the routed heading, primary action,
metadata that affects scanability, `DataSurface` title where present, and URL
state preservation for tabs or filters. Manual tab controls must also preserve
`role="tablist"`, `role="tab"`, `aria-selected`, `aria-controls`, and keyboard
navigation tests.

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
