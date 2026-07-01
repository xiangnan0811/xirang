# Xirang Console Design System

> This document records the **existing** frontend design system for the Xirang (息壤) operations console. It is a contract, not a proposal: every token, primitive, and rule below is already implemented in `web/src/index.css`, `web/tailwind.config.ts`, and the shared UI primitives under `web/src/components/ui/`. New work must conform to this system; do not invent new tokens or override the system ad hoc.

## 1. Atmosphere & Intent

Xirang is an **operations console** — dense, factual, calm. The visual language prioritizes legibility and trust over decoration:

- **Light-first, dark-mode native.** Both themes ship as first-class via `hsl(var(--token))` CSS variables defined in `web/src/index.css` (`:root` for light, `.dark` for dark). Tailwind maps them in `web/tailwind.config.ts` (`colors.* = "hsl(var(--*))"`), so utility classes like `bg-background`, `text-foreground`, `border-border` resolve to the active theme automatically.
- **Restrained shadows.** Shadows are neutral and non-decorative (`--shadow-sm` through `--shadow-xl`). Dark mode uses pure-black tints; light mode uses slate-tinted tints. Never introduce colored or glow shadows outside the terminal namespace.
- **Subtle atmosphere.** A faint dot-grid (`app-shell-bg::before`) and a login ambient gradient (`bg-login-ambient`) are the only decorative backgrounds. Pages themselves stay on `bg-background` / `bg-card`.
- **One accent.** `--primary` (blue) is the single brand accent, aliased as `--accent-brand`. Status colors (`success`/`warning`/`destructive`/`info`) are semantic and reserved for state, never used as decoration.

## 2. Token Families

All tokens are HSL channel triplets (e.g. `217 84% 43%`) consumed via `hsl(var(--token))`. Source of truth: `web/src/index.css`.

### 2.1 Color (semantic)

| Token | Light | Dark | Usage |
|---|---|---|---|
| `--background` / `--foreground` | `220 24% 97%` / `222 34% 13%` | `222 28% 8%` / `213 31% 91%` | Page canvas + base text |
| `--card` / `--card-foreground` | `0 0% 100%` / `222 34% 13%` | `222 26% 11%` / `213 31% 91%` | Cards, surfaces |
| `--popover` / `--popover-foreground` | `0 0% 100%` / `222 34% 13%` | `222 26% 11%` / `213 31% 91%` | Menus, tooltips, popovers |
| `--primary` / `--primary-foreground` | `217 84% 43%` / `210 40% 98%` | `217 91% 64%` / `222 47% 9%` | Primary actions, links, focus ring |
| `--secondary` / `--secondary-foreground` | `220 17% 93%` / `222 34% 13%` | `222 20% 17%` / `213 31% 91%` | Secondary surfaces, muted blocks |
| `--muted` / `--muted-foreground` | `220 17% 93%` / `220 10% 42%` | `222 20% 17%` / `217 15% 66%` | Muted backgrounds, captions |
| `--accent` / `--accent-foreground` | `213 32% 91%` / `222 34% 13%` | `217 24% 19%` / `213 31% 91%` | Hover/active accents |
| `--accent-brand` | `217 84% 43%` | `217 91% 64%` | Brand accent (login ambient, marks) |
| `--destructive` / `--destructive-foreground` | `0 72% 43%` / `210 40% 98%` | `0 74% 62%` / `222 47% 9%` | Errors, down status, delete |
| `--success` / `--success-foreground` | `156 66% 32%` / `150 45% 96%` | `158 64% 45%` / `222 47% 9%` | Up/healthy status, positive |
| `--warning` / `--warning-foreground` | `36 88% 42%` / `42 45% 97%` | `38 92% 58%` / `222 47% 9%` | Warning, at-risk |
| `--info` / `--info-foreground` | `199 87% 39%` / `200 52% 97%` | `199 90% 56%` / `222 47% 9%` | Informational badges |
| `--border` / `--input` | `220 16% 86%` | `220 18% 23%` | Borders, inputs (shared value) |
| `--ring` | `217 84% 43%` | `217 91% 64%` | Focus ring |

**Chart series** (`--chart-1/2/3`, `--chart-ingress`, `--chart-egress`) are semantic and distinct from status text. **Navigation** tokens (`--nav-active`, `--nav-active-foreground`, `--shadow-panel`, `--shadow-panel-hover`, `--shadow-mobile-sheet`) alias accent/foreground and are consumed by mobile navigation and the SSH key sheet.

### 2.2 Typography

Font stack (`tailwind.config.ts` → `fontFamily`):
- **sans:** `"Inter Variable", "Inter", "PingFang SC", "Microsoft YaHei", system-ui, sans-serif` (loaded via `@fontsource-variable/inter` in `index.css`)
- **mono:** `"JetBrains Mono", monospace`

Project-specific size tokens (sit alongside Tailwind's built-in scale; promoted from recurring `text-[Npx]` values, audit 2026-04-25):

| Token | Size / Line | Usage |
|---|---|---|
| `text-micro` | 10px / 14px | Micro labels: badges, source tags, tiny chips |
| `text-mini` | 11px / 16px | Small meta: stat captions, table headers, footnotes |
| `text-nav` | 13px / 18px | Sidebar nav items |
| `text-stat` | 28px / 32px | Hero stat numbers on overview cards |

For one-off sizes that don't fit, prefer `text-[Npx]` over forcing a new token name.

### 2.3 Spacing, Radius, Shadow

Radius (`--radius*` in `index.css`, mapped in `tailwind.config.ts` → `borderRadius`):
- `--radius`: `0.5rem` (8px) — default; `--radius-sm`: `0.375rem` (6px); `--radius-lg`/`--radius-xl`: `0.5rem` (8px)
- `rounded-xs`: `0.25rem` (4px) — small status dots, micro icon containers
- `html[data-density="compact"]` shrinks `--radius` to `0.3rem`.

Shadows (`--shadow-sm` / `--shadow-md` / `--shadow-lg` / `--shadow-xl`): neutral, non-decorative. Dark mode uses pure-black tints.

Spacing follows Tailwind's default scale (4px base via `gap-*`, `p-*`, `m-*`). No custom spacing tokens exist; do not invent arbitrary pixel values (e.g. `margin: 13px`).

### 2.4 Motion

Durations (`--duration-fast`: 100ms, `--duration-normal`: 150ms, `--duration-slow`: 200ms) with eases (`--ease-enter`, `--ease-exit`, `--ease-in-out`). Animations: `animate-fade-in`, `animate-slide-up`, `animate-slide-down`, `animate-animate-in`, `animate-popover-in`, `animate-popover-out`, `animate-float`, `animate-spin`.

**GPU-composited only** — animate `transform`, `opacity`, `filter`; never animate layout properties.

#### Reduced motion

`@media (prefers-reduced-motion: reduce)` collapses all animation/transition durations to `0.01ms` **except** `.animate-spin` (loading spinners are an essential affordance that work is in flight, not decoration — they keep spinning at 1s).

#### Power-save mode

`html[data-power="save"]` clamps animation duration to `0.01ms`, iteration count to 1, transition to 80ms, and disables the status pulse (`pulse-online`/`pulse-warning`/`pulse-offline`) and `app-shell-bg` opacity.

#### Density

`html[data-density="compact"]` shrinks `--radius` to `0.3rem`, tightens `.filter-panel` padding, reduces `[data-density-gap="compact"]` gaps to `0.4rem`, and trims table cell padding.

### 2.5 Terminal namespace (intentional exception)

The terminal surface (`web-terminal`, log streams) keeps its own aesthetic via `--terminal-*` tokens and `.terminal-*` utility classes in `index.css`. This is an **intentional, scoped exception** — terminal backgrounds, glows, chips, and panels use terminal-specific tokens, not the semantic status tokens. Do not propagate terminal tokens outside terminal components, and do not replace terminal hex/gradient values with semantic tokens.

### 2.6 Status pulse

`.pulse-online` / `.pulse-warning` / `.pulse-offline` apply a 2.4s breathing-opacity animation keyed to `--success` / `--warning` / `--destructive` respectively. Disabled under reduced-motion and power-save.

## 3. Status Token Rules

Status semantics must use the semantic tokens, **not** fixed Tailwind palette classes:

| State | Token class | Forbidden |
|---|---|---|
| Up / healthy / success | `bg-success`, `text-success`, `border-success/30`, `bg-success/5` | `bg-emerald-500`, `text-emerald-500`, `border-emerald-500/30`, `bg-emerald-500/5` |
| Down / error / destructive | `bg-destructive`, `text-destructive`, `border-destructive/30`, `bg-destructive/5` | `bg-red-500`, `text-red-500`, `border-red-500/30`, `bg-red-500/5` |
| Warning | `bg-warning`, `text-warning` | `bg-amber-500`, `text-amber-500` |
| Info | `bg-info`, `text-info` | `bg-sky-500`, `text-sky-500` |

**Exceptions where raw palette/hex is allowed:**
- **Chart series colors** in `panel-renderer.tsx` (`SERIES_COLORS` hex array) — Recharts needs concrete hex; these are chart-only, not status.
- **Terminal namespace** — terminal tokens/colors are scoped to terminal components.
- **`muted-foreground/30`** for "unknown/neutral" status dots is a semantic use, not a raw palette class.

When a new status surface is added, extend the semantic token usage; do not reach for `emerald-500`/`red-500`/`amber-500`.

## 4. Component Primitives

Shared primitives live in `web/src/components/ui/` (shadcn/ui + Radix). Pages **must** import from here; never create ad hoc primitives in `web/src/pages/`.

Key primitives:
- **`PageHero`** — page-level header with a single `h1` title, optional subtitle, meta, and actions. Used by most `/app/*` pages. A page without a `PageHero` must still provide exactly one `h1` by other means.
- **`StatCardsSection`** — responsive grid of stat cards with `tone` (`info`/`success`/`warning`/`destructive`/`primary`).
- **`DataSurface`** + `DataSurfaceHeader` / `DataSurfaceContent` / `DataSurfaceToolbar` / `DataSurfaceFooter` — the standard "panel" container for tables and lists. `DataSurfaceHeader` defaults to `h2` (configurable via `headingLevel`).
- **`Card`** / `CardContent` / `CardHeader` / `CardTitle` — shadcn card primitives.
- **`Badge`** — with `tone` (`neutral`/`success`/`warning`/`destructive`/`info`/`primary`).
- **`Button`** — variants: `default`, `outline`, `ghost`, `secondary`; sizes: `sm`, `icon`, etc.
- **`FormDialog`** — wraps Radix `Dialog` with `DialogTitle` + optional `DialogDescription` + body + footer. **Always pass a `description` prop** so Radix `DialogContent` does not warn about a missing accessible description.
- **`Dialog`** family — `Dialog`, `DialogContent`, `DialogHeader`, `DialogTitle`, `DialogDescription`, `DialogCloseButton`, `DialogBody`, `DialogFooter`.
- **`Input`**, **`Select`**, **`Switch`**, **`Pagination`**, **`EmptyState`**, **`InlineAlert`**, **`LoadingState`**, **`Skeleton`**, **`TagChips`**, **`Toast`** (sonner).

## 5. Page Composition

Most `/app/*` pages follow this shape:

```tsx
<div className="animate-fade-in space-y-5">
  <PageHero title={t("...")} subtitle={t("...")} meta={...} actions={...} />
  <StatCardsSection items={[...]} />
  <DataSurface>
    <DataSurfaceHeader title={...} description={...} actions={...} />
    <DataSurfaceContent>...</DataSurfaceContent>
  </DataSurface>
</div>
```

- **One `h1` per page.** `PageHero` renders the `h1`; a page using `PageHero` must not add another `h1`. Section headings inside surfaces use `h2`/`h3` via `DataSurfaceHeader` (default `h2`) or explicit heading elements.
- **`animate-fade-in`** on the page root; staggered `animate-slide-up [animation-delay:Nms]` on major sections.
- **Filters** use the `.filter-panel` + `.sticky-filter` utilities (sticky on `md+`).
- **Tables** use `border-b border-border` rows with `hover:bg-muted/30` and `text-mini uppercase tracking-wide text-muted-foreground` headers.
- **Empty/loading/error** states use `EmptyState`, `LoadingState`, `InlineAlert` — never ad hoc divs.

Small pages (e.g. `more-page.tsx`, `login-page.tsx`) may stay single-file and skip `PageHero` only if they provide their own single `h1`.

## 6. Do / Don't

### Do
- Reference tokens via Tailwind utility classes (`bg-background`, `text-foreground`, `border-border`, `bg-success`, `text-destructive`) — they resolve to `hsl(var(--token))` automatically.
- Use `import type` for type-only imports.
- Map `snake_case` → `camelCase` at the API boundary; components receive typed `camelCase` props.
- Use `setLanguage()` helper for language switching; never call `i18n.changeLanguage()` directly.
- Pass a `description` to every `FormDialog` / `Dialog` that has a title, to satisfy Radix's accessible description requirement.
- Provide exactly one `h1` per page (usually via `PageHero`).
- Keep animations GPU-composited (`transform`/`opacity`/`filter`).

### Don't
- **Don't** hardcode hex/rgb colors (`#3b82f6`, `rgb(59,130,246)`) or raw Tailwind palette classes (`emerald-500`, `red-500`, `amber-500`, `sky-500`) for status semantics — use the semantic tokens. (Chart series hex in `panel-renderer.tsx` and terminal tokens are the documented exceptions.)
- **Don't** invent arbitrary spacing (`margin: 13px`, `padding: 7px`) — use the Tailwind scale.
- **Don't** invent ad hoc font sizes — use the scale tokens (`text-micro`/`text-mini`/`text-nav`/`text-stat`) or Tailwind's built-in scale, or `text-[Npx]` for genuine one-offs.
- **Don't** create ad hoc UI primitives in `pages/` — import from `components/ui/`.
- **Don't** use `any`, `as any`, `@ts-ignore`, `@ts-expect-error`, or broad unsafe casts.
- **Don't** suppress Radix/Recharts/browser warnings via console filters or test spies that hide the root cause — fix the root cause (e.g. add a `description`, give chart containers a non-zero minimum size).
- **Don't** animate layout properties (width, height, top, left, padding, margin).
- **Don't** propagate terminal tokens outside the terminal namespace.
- **Don't** add a second `h1` to a page that already has `PageHero`.