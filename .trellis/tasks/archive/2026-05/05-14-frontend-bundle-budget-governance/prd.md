# Frontend Bundle Budget Governance

## Goal

Reduce the frontend startup bundle pressure introduced by the workbench page
polish work without weakening the product UI. The current build passes the
main JavaScript budget by only about 0.06 KiB, so small future copy or UI
changes can break CI even when the implementation is otherwise sound.

## What I Already Know

* The repository is on branch `chore/frontend-bundle-budget-governance`, created
  from an up-to-date `origin/main`.
* The current bundle budget result is:
  * main JS: `557.94 KiB` / `558.00 KiB`
  * main CSS: `67.81 KiB` / `70.00 KiB`
* Route-level page lazy loading already exists in `web/src/router-pages.tsx`.
* Vite already separates heavy `recharts` and `framer-motion` chunks in
  `web/vite.config.ts`.
* The i18n bootstrap currently imports both full locale files synchronously.
  Each locale source is about 104 KiB before minification:
  * `web/src/i18n/locales/zh.ts`
  * `web/src/i18n/locales/en.ts`

## Assumptions

* The preferred language must still be available before the React app renders so
  users do not see translation keys during first paint.
* The alternate language can be lazy-loaded when the user switches language.
* No new runtime dependency is required; i18next can load resources through a
  small local backend module.

## Requirements

* Keep user-visible language behavior unchanged:
  * detect `xirang.language` from `localStorage`;
  * fall back to browser language;
  * fall back to Chinese resources when needed;
  * keep `<html lang>` synchronized with the active language.
* Split locale resources out of the startup bundle so the main JS has real
  budget headroom.
* Ensure all language-switching call sites use the same helper so persistence,
  resource loading, and `documentElement.lang` behavior stay consistent.
* Keep the existing route-level lazy loading and manual heavy dependency chunks.
* Make the bundle budget gate less brittle by documenting and enforcing a lower
  main JS budget after optimization.
* Add focused tests for language initialization and switching behavior.

## Acceptance Criteria

* [x] `npm run typecheck` passes in `web/`.
* [x] `npm run lint` passes in `web/`.
* [x] Focused i18n/language tests pass.
* [x] `npm run build` passes in `web/`.
* [x] `node scripts/check-bundle-budget.mjs` passes with a lower main JS budget
      than `558 KiB`.
* [x] The build output shows locale resources in separate lazy chunks or
      equivalent non-startup assets.
* [x] Existing language switcher behavior persists the selected language.

## Definition Of Done

* Code and tests are committed on the feature branch.
* Trellis task is archived and journaled after verification.
* Pull request is opened, CI monitored, and merged only when green.
* Post-merge automation is monitored, including Release Please and Docker
  publish if a release is produced.

## Out Of Scope

* Visual redesign of additional pages.
* Replacing i18next/react-i18next.
* Broad dependency pruning unrelated to the startup bundle.
* Backend changes.

## Technical Notes

* Existing bundle budget script: `web/scripts/check-bundle-budget.mjs`.
* Existing i18n bootstrap: `web/src/i18n/index.ts`.
* Existing language switcher: `web/src/components/language-switcher.tsx`.
* Settings page language selector currently calls `i18n.changeLanguage`
  directly and must be moved to the shared helper.

## Research References

* [`research/bundle-budget-analysis.md`](research/bundle-budget-analysis.md) -
  startup bundle evidence and chosen optimization path.
