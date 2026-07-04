# 前端数值 helper 合并

## Goal

Reduce duplicate numeric fallback logic in frontend API mappers by moving shared
finite-number normalization helpers into one small API-boundary utility, while
preserving all existing mapped domain values and request behavior.

## Confirmed Facts

- Frontend type-safety specs require backend payload normalization at the API
  boundary and safe numeric defaults instead of `NaN` in React state.
- Several API modules define local copies of the same helper shape:
  `finiteNumber(value, fallback = 0)`, positive optional number parsing, and
  nullable finite-number parsing.
- Existing mapper tests already cover safe numeric fallbacks in settings,
  credential access grants, credential audit, SSH keys, node Doctor, policy
  latest drill, and task-run drill evidence paths.
- Form-level numeric parsing helpers such as `toBoundedInt` and
  `clampNumberInput` have different min/max/rounding semantics and should not
  be merged with wire DTO fallback helpers in this child.

## Requirements

- Scope is limited to frontend API-boundary numeric fallback helpers and tests.
- Add one dependency-free helper module under `web/src/lib/api/` for:
  - finite number with fallback;
  - positive finite number as optional `number | undefined`;
  - nullable finite number as `number | null`.
- Replace duplicated local API mapper helpers where semantics match, including
  the modules found in this audit:
  - `nodes-api.ts`
  - `settings-api.ts`
  - `credential-access-grants-api.ts`
  - `credential-audit-api.ts`
  - `ssh-keys-api.ts`
  - `task-runs-api.ts`
  - `overview-api.ts`
  - `policies-api.ts`
- Preserve existing wire-to-domain behavior, including:
  - invalid numeric values fall back to finite defaults;
  - optional positive IDs become `undefined` when missing, invalid, zero, or
    negative;
  - nullable numeric fields keep `null` for missing, empty, or invalid values
    where that is the current contract.
- Do not change React component state, form input clamping, API URLs, request
  bodies, domain type names, i18n strings, or backend contracts.
- Keep helper tests focused and keep existing mapper tests passing.

## Acceptance Criteria

- [ ] A shared API numeric utility exists and is covered by unit tests.
- [ ] Local duplicate `finiteNumber` / positive optional number /
      nullable finite number helpers are removed from the targeted API modules
      where semantics match.
- [ ] Existing mapper tests still prove snake_case to camelCase mapping and safe
      numeric fallbacks.
- [ ] No API response shape, request shape, or component-visible domain type
      changes are introduced.
- [ ] Focused frontend tests pass for affected API modules.
- [ ] `cd web && npm run typecheck` passes.
- [ ] `cd web && npm run test -- --run` passes or any environment-specific
      blocker is recorded.

## Out of Scope

- Consolidating form input helpers such as `toBoundedInt`,
  `clampNumberInput`, port parsing, or slider/input behavior.
- Refactoring date/time formatting helpers.
- Reworking API mapper tests beyond numeric fallback coverage needed for this
  consolidation.
- Moving API helper exports out of `web/src/lib/api/`.
