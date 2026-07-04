# Implementation Plan: Frontend Numeric Helper Consolidation

## Checklist

1. Add failing helper tests in `web/src/lib/api/number-utils.test.ts` for:
   - finite number fallback;
   - positive optional number;
   - nullable finite number.
2. Run the focused helper test and confirm it fails because the module does not
   exist yet.
3. Create `web/src/lib/api/number-utils.ts` with the minimal helper exports.
4. Replace matching local helpers in:
   - `nodes-api.ts`
   - `settings-api.ts`
   - `credential-access-grants-api.ts`
   - `credential-audit-api.ts`
   - `ssh-keys-api.ts`
   - `task-runs-api.ts`
   - `overview-api.ts`
   - `policies-api.ts`
5. Keep form-level `toBoundedInt`, `clampNumberInput`, and parser helpers with
   different semantics unchanged.
6. Run focused tests:
   ```bash
   cd web && npm run test -- --run src/lib/api/number-utils.test.ts src/lib/api/nodes-api.test.ts src/lib/api/settings-api.test.ts src/lib/api/credential-access-grants-api.test.ts src/lib/api/credential-audit-api.test.ts src/lib/api/ssh-keys-api.test.ts src/lib/api/drill-evidence-api.test.ts
   ```
7. Run frontend typecheck:
   ```bash
   cd web && npm run typecheck
   ```
8. Run broader frontend tests when feasible:
   ```bash
   cd web && npm run test -- --run
   ```
9. Inspect `git diff` for accidental component, UI, request body, or domain type
   changes.

## Validation Commands

```bash
cd web && npm run test -- --run src/lib/api/number-utils.test.ts src/lib/api/nodes-api.test.ts src/lib/api/settings-api.test.ts src/lib/api/credential-access-grants-api.test.ts src/lib/api/credential-audit-api.test.ts src/lib/api/ssh-keys-api.test.ts src/lib/api/drill-evidence-api.test.ts
cd web && npm run typecheck
cd web && npm run test -- --run
```

## Risk Points

- `finiteNumber` helper semantics must preserve existing `Number(value)`
  behavior. Do not change null handling for non-zero fallbacks unless a test
  explicitly owns that change.
- Do not consolidate bounded form helpers into API mapper helpers; they have
  min/max and rounding rules.
- Avoid importing from `core.ts` to keep the helper free of API request cycles.

## Rollback Point

After helper tests are added but before implementation, rollback by deleting the
new test file. After implementation, rollback is limited to the new helper/test
and imports in the targeted API modules.
