# Bundle Budget Analysis

## Current Build Evidence

The current build passes the budget gate but has almost no remaining main JS
headroom:

```text
[bundle-budget] 主 JS: index-Bpkl_LTZ.js | 当前 557.94 KiB | 预算 558.00 KiB | OK
[bundle-budget] 主 CSS: index-DchDg4im.css | 当前 67.81 KiB | 预算 70.00 KiB | OK
```

The route graph is already lazy-loaded through `web/src/router-pages.tsx`.
Vite already extracts `recharts` and `framer-motion` in
`web/vite.config.ts`, and the built assets confirm those chunks are separate.

The largest easy startup-path issue is i18n: `web/src/i18n/index.ts` imports
both locale files synchronously:

```text
104461 web/src/i18n/locales/zh.ts
104576 web/src/i18n/locales/en.ts
```

That means the preferred language and the inactive language are both reachable
from the startup bundle even though users only need one locale at boot.

## Options Considered

1. Raise the budget again.
   * Lowest engineering cost.
   * Bad governance outcome: this hides the fact that normal UI work has no
     headroom left.

2. Split more page chunks manually.
   * Existing page chunks are already lazy.
   * This is unlikely to move the startup bundle much unless shared dependencies
     are also split, which is higher risk.

3. Lazy-load locale resources.
   * Directly addresses a confirmed startup dependency.
   * Keeps current i18next/react-i18next stack.
   * Does not require new dependencies.
   * Preserves preferred-language first paint by awaiting the initial locale
     before React renders.

## Chosen Approach

Use dynamic imports for locale resources:

* load the detected preferred language before rendering the React app;
* register a small local i18next backend so `changeLanguage` can load the
  alternate language later;
* route all explicit language changes through `setLanguage`;
* lower the main JS bundle budget after build evidence confirms the reduction.

This keeps the user-facing behavior stable while turning locale files into
on-demand chunks.

## Final Build Evidence

After the implementation, the production build emits the locale resources as
separate lazy chunks:

```text
dist/assets/zh-FDP6hTS4.js  85.52 kB
dist/assets/en-CtwYQqXw.js  85.69 kB
```

The startup main JS dropped well below the tightened budget:

```text
[bundle-budget] 主 JS: index-CbDLS83J.js | 当前 391.20 KiB | 预算 500.00 KiB | OK
[bundle-budget] 主 CSS: index-DchDg4im.css | 当前 67.81 KiB | 预算 70.00 KiB | OK
```
