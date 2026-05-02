# Type Safety

> Type safety patterns in this project.

---

## Overview

The frontend is TypeScript-first and built with `tsc -b --noEmit` as part of
`npm run check`. Shared domain types live in `web/src/types/domain.ts`; raw API
payload types are usually local to the API wrapper that maps them.

There is no runtime schema validation library such as Zod. Runtime defensive
normalization is done in API mappers using `Number(...)`, `String(...)`,
`Boolean(...)`, optional chaining, array checks, and fallback defaults.

---

## Type Organization

- Put cross-module product/domain types in `web/src/types/domain.ts`.
- Keep API response/request wire types private to `web/src/lib/api/*.ts` unless
  multiple API modules need them.
- Use explicit return types for exported API methods, context values, and hooks.
- Keep component-local types near the component when they are not reused.
- Use string unions for constrained UI/domain values. Examples:
  `OverviewTrafficWindow`, auth roles `"admin" | "operator" | "viewer"`, and
  status-like domain unions.

---

## Validation

- Normalize backend payloads at the API boundary. Examples:
  `mapOverviewTraffic`, `mapBackupHealth`, and other `map*` helpers in
  `web/src/lib/api/overview-api.ts`.
- Use `Array.isArray` before mapping unknown arrays from responses.
- Convert numeric fields with `Number(...)` and provide safe defaults for
  missing values.
- Validate redirect and route-sensitive strings explicitly. Existing example:
  `normalizeRedirectTarget` in `core.ts`.
- Browser storage reads/writes should be guarded with try/catch and null checks,
  as in `auth-context.tsx`.

---

## Common Patterns

- `request<T>()` in `core.ts` unwraps the backend `{code, message, data}`
  envelope and throws `ApiError` for HTTP/envelope errors.
- `PaginatedEnvelope<T>` plus `unwrapPaginated` is the preferred pattern for
  paginated endpoint clients.
- API modules export `create*Api()` factories returning typed methods rather
  than exposing raw URLs throughout components.
- Use `import type` for type-only imports when importing domain types.
- Prefer discriminated or narrow unions for UI state that has a finite set of
  values.

---

## Forbidden Patterns

- Do not use `any` for API responses, component props, or context values. Use
  local raw types plus mapper functions.
- Do not pass raw snake_case API objects into components.
- Do not silence type errors with broad type assertions unless there is a
  narrow, documented boundary.
- Do not add implicit `unknown as T` casts where a mapper can validate and
  normalize the shape.
- Do not bypass the central request wrapper for normal JSON API calls.
