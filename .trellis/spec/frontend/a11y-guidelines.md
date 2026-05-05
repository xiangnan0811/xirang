# A11y Guidelines

> Accessibility (a11y) baseline for the Xirang React console.

---

## Overview

Xirang is an internal operations tool where keyboard usage, screen-reader
clarity, and color contrast directly affect on-call efficiency. This document
captures the minimum a11y rules every contributor must follow. They are
enforced by `eslint-plugin-jsx-a11y` (lint) and `vitest-axe` (unit/component
tests). Violations should not ship to `main`.

The current target is **WCAG 2.1 AA** for desktop. Mobile-specific a11y rules
are out of scope for this revision.

---

## Required Rules

1. **Decorative `lucide-react` icons must declare `aria-hidden`.** Icons that
   only repeat the visible label (e.g. `<Save />` next to "保存") are
   decorative; without `aria-hidden` screen readers announce them twice.
2. **Icon-only buttons must have an accessible name.** Use either an
   `aria-label`, an `aria-labelledby`, or a visually hidden `<span className="sr-only">…</span>`
   inside the button. Do not rely on tooltips alone.
3. **Form inputs must be labeled.** Pair every `<input>` / `<textarea>` /
   `<select>` with a `<label htmlFor>`, an `aria-label`, or an
   `aria-labelledby`. Wrapping a label without `htmlFor` is acceptable only
   when the input is a direct child of the label.
4. **Radix `Dialog` must contain a `<DialogTitle>`.** Use a visually hidden
   title (`<span className="sr-only">`) when the dialog has no visible header.
   Without a title Radix logs a console warning and screen readers cannot
   announce the dialog purpose.
5. **`<html lang>` must follow the active i18n language.** Update
   `document.documentElement.lang` whenever `i18n.changeLanguage` runs (zh →
   `zh-CN`, en → `en`). This satisfies WCAG 3.1.1 / 3.1.2.
6. **Color contrast must meet WCAG AA.** Body text ≥ 4.5:1, large text
   (18pt / 14pt bold) ≥ 3.0:1. Verify with axe `color-contrast` rule before
   shipping new color tokens or muted text variants.
7. **Do not remove the `focus-visible` ring.** All interactive primitives
   under `web/src/components/ui/` already render a visible focus ring; do not
   override it with `focus:outline-none` in business code.
8. **`tablist` / `tab` / `tabpanel` roles must be used together.** When you
   wire roles manually (rather than via Radix Tabs), include
   `aria-controls`, `aria-selected`, and `tabIndex` so keyboard users can
   move with arrow keys.

---

## Tooling

| Tool | Where | Purpose |
|---|---|---|
| `eslint-plugin-jsx-a11y` | `web/eslint.config.js` | Static check. `aria-role`, `no-redundant-roles`, `anchor-is-valid`, plus all default-error rules from `jsx-a11y/recommended` are `error`. Five rules with remaining debt stay `warn` (see config comments). |
| `vitest-axe` | `web/vitest.setup.ts` | Runtime axe-core check via `expect(results).toHaveNoViolations()` |
| `axe-core` | transitive | The actual rule engine |
| `runAxe` helper | `web/src/test/a11y-helpers.ts` | Wraps `axe()` with `color-contrast` disabled (jsdom limitation, see below) |

### Test template

Use the shared `runAxe` helper instead of calling `axe()` directly. It centralises
the color-contrast exemption and keeps individual tests free of duplicated rules
config.

```tsx
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { runAxe } from "@/test/a11y-helpers";

describe("MyComponent a11y", () => {
  it("smoke: 默认渲染无 axe 违规", async () => {
    const { container } = render(<MyComponent />);
    const results = await runAxe(container);
    expect(results).toHaveNoViolations();
  });
});
```

For Radix `Dialog` / `DropdownMenu` / `Tooltip` etc. that render via portal to
`document.body`, scan the body instead of the render container so portal content
is included:

```tsx
const results = await runAxe(document.body);
```

When the page under test depends on context providers, follow the existing
PR-C page tests (`web/src/pages/__tests__/*-page.a11y.test.tsx`) — mock each
context with `vi.mock(...)` and seed minimal data via a `buildContext()` helper
inside the test file. Avoid pulling real API or WebSocket modules; a smoke test
only needs the first paint to be axe-clean.

### Decorative vs semantic icons

`lucide-react` icons render as inline SVG. By default screen readers see them
as a graphic with the icon name as accessible name, which is almost always
noise next to a visible label.

- **Decorative icon** (icon next to a visible text label, or inside an
  already-labeled button): add `aria-hidden`. Examples:
  - `<Button>` with `<Save />` and the text "保存" — the icon is decorative.
  - Status pill with both `<CircleAlert />` and the text "失败".
  - Icon used purely as a visual bullet inside a list item.
- **Semantic icon** (icon-only button or icon used to convey meaning the text
  does not): the surrounding interactive element must have an accessible name
  via `aria-label`, `aria-labelledby`, or a `<span className="sr-only">` child.
  Do not add `aria-hidden` to the icon itself in this case.

Heuristic: if removing the icon still leaves the same information for a
screen-reader user, the icon is decorative — hide it.

### i18n + `<html lang>` sync

Whenever the user changes UI language, `document.documentElement.lang` must
follow. The current implementation lives in `web/src/i18n/index.ts` and looks
like:

```ts
import i18n from "i18next";

// Map i18next internal codes to BCP 47 values for the `<html lang>` attribute.
function mapLangToHtml(lng: string): string {
  if (lng?.startsWith("zh")) return "zh-CN";
  return "en";
}

function syncDocumentLang(lng: string) {
  if (typeof document === "undefined") return;
  document.documentElement.lang = mapLangToHtml(lng);
}

// Sync once on init, then every time the user switches language.
syncDocumentLang(i18n.language);
i18n.on("languageChanged", syncDocumentLang);
```

Mirror this pattern in any new locale entry-point. Without it, screen readers
keep announcing the page in the wrong language and WCAG 3.1.1 / 3.1.2 fail.

---

## Known exemptions

The following gaps are intentional and do not need a fix in component code.
Document the reason if you add a new exemption.

| Surface | Reason | Mitigation |
|---|---|---|
| `react-grid-layout` drag-and-drop | Upstream community issue — keyboard parity for grid drag is not feasible without forking. | We expose explicit "move up / down" buttons (see `panel-editor-dialog.tsx`) so keyboard users can reorder panels without dragging. |
| `xterm.js` terminal pane | Terminal emulators render content into a canvas; SR support is not part of WCAG conformance for terminal apps. | We label the wrapper element and expose copy/paste shortcuts; we do not attempt to make the terminal buffer screen-reader friendly. |
| `axe-core` `color-contrast` rule under jsdom | jsdom does not implement `HTMLCanvasElement.prototype.getContext`, so axe cannot compute contrast ratios. Running it produces stderr noise and unreliable results. | `runAxe` disables the rule. Validate contrast manually in the browser via the axe DevTools extension before shipping color tokens or muted text styles. |

---

## Common Pitfalls

- Adding a `lucide-react` icon to a button that already has visible text
  without `aria-hidden`. Decorative icons inside labeled buttons must be
  hidden from assistive tech.
- Using `<div onClick>` for clickable surfaces. Use `<button>` (or add
  `role="button"`, `tabIndex={0}`, and a key handler if a `<button>` is not
  feasible — but prefer the native element).
- Mounting Radix `Dialog` without `DialogTitle`. Even a sr-only title
  satisfies the requirement.
- Hard-coding `<html lang="zh-CN">` and forgetting to update it on language
  change. The i18n bootstrap and the `languageChanged` listener must both
  sync `document.documentElement.lang`.
- Adding muted text (`text-muted-foreground`, `text-foreground/60`, etc.)
  without checking contrast. Run a quick axe scan locally if uncertain.

---

## Out of Scope (for now)

- E2E a11y testing with Playwright + `@axe-core/playwright`
- Mobile / touch-specific a11y (separate audit)
- `react-grid-layout` drag-and-drop keyboard parity (long-standing community
  issue; we provide explicit "move up / down" buttons as a fallback)
- Screen-reader UX of the embedded `xterm.js` terminal (terminal emulators
  are commonly exempted from WCAG)

---

**Language**: All documentation should be written in **English**.
