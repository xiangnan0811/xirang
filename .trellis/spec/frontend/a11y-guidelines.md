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
| `eslint-plugin-jsx-a11y` | `web/eslint.config.js` | Static check (warn during PR-A; promoted to error in PR-D) |
| `vitest-axe` | `web/vitest.setup.ts` | Runtime axe-core check via `expect(results).toHaveNoViolations()` |
| `axe-core` | transitive | The actual rule engine |

### Test template

```tsx
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { axe } from "vitest-axe";

describe("MyComponent a11y", () => {
  it("smoke: 默认渲染无 axe 违规", async () => {
    render(<MyComponent />);
    // Radix portals render to document.body; scan the whole body to catch
    // dialog/menu/tooltip portal content.
    const results = await axe(document.body);
    expect(results).toHaveNoViolations();
  });
});
```

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
