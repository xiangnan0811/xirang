# Design: Frontend Numeric Helper Consolidation

## Boundary

This child changes only frontend API mapper utilities and the API modules that
normalize backend numeric payloads. It does not touch components, form input
behavior, backend APIs, domain type definitions, or generated data.

## Current Behavior

Several API modules locally parse wire values with `Number(...)` and guard with
`Number.isFinite(...)`. These copies mostly implement the same three contracts:
finite number fallback, positive optional number, and nullable finite number.
Because each module owns its own copy, new mapper fixes can drift and tests must
implicitly cover identical logic many times.

## Target Behavior

Create `web/src/lib/api/number-utils.ts` with small named helpers:

- `finiteNumber(value: unknown, fallback = 0): number`
- `positiveNumberOrUndefined(value: unknown): number | undefined`
- `nullableFiniteNumber(value: unknown): number | null`

The helper module must have no imports so it can be used by any API wrapper
without creating cycles. API modules import only the helpers they need.

## Compatibility

The helper behavior intentionally matches existing mapper semantics:

- `finiteNumber` parses with `Number(value)` and returns the fallback only when
  the parsed value is non-finite.
- `positiveNumberOrUndefined` returns a value only when the parsed number is
  finite and greater than zero.
- `nullableFiniteNumber` treats `null`, `undefined`, and `""` as null and also
  returns null for invalid parsed values.

Existing local helper names may remain at call sites only when they represent
different behavior. Form helpers with bounds, rounding, min/max constraints, or
input-specific behavior stay local and out of scope.

## Tests

Add focused tests for the shared helper module first. Then update the affected
API modules to import the helper and rely on existing mapper tests for behavior
regression. Run focused tests for:

- `number-utils.test.ts`
- `nodes-api.test.ts`
- `settings-api.test.ts`
- `credential-access-grants-api.test.ts`
- `credential-audit-api.test.ts`
- `ssh-keys-api.test.ts`
- existing policy/task-run drill numeric fallback coverage

## Rollback

Rollback is local: restore the removed helper functions in each API module and
delete `number-utils.ts` plus its tests. No data migration or backend change is
involved.

