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

---

## Scenario: SSH Fleet Doctor Mapping

### 1. Scope / Trigger

- Trigger: adding or changing frontend access to node Doctor diagnostics.
- Applies to `web/src/lib/api/nodes-api.ts`, shared domain types in
  `web/src/types/domain.ts`, and node-context UI that renders Doctor results.

### 2. Signatures

- Raw API endpoint: `POST /nodes/:id/doctor` through `request<T>()`.
- Raw response fields include `node_id`, `node_name`, `generated_at`, and
  `checks`.
- Raw check fields include `check`, `status`, `evidence`, and `suggestion`.
- Domain types: `NodeDoctorResult`, `NodeDoctorCheckResult`, and
  `NodeDoctorCheckStatus = "pass" | "warn" | "fail" | "skip"`.

### 3. Contracts

- Keep raw snake_case Doctor response types private to the API module.
- Map `node_id` to `nodeId`, `node_name` to `nodeName`, and `generated_at` to
  `generatedAt` before components render data.
- Use `Array.isArray` for `checks`; default missing checks to `[]`.
- Preserve backend status values; unknown status values should degrade to a safe
  non-success status such as `warn`.
- Components render sanitized `evidence` and `suggestion` from the API as-is;
  do not enrich them with connection secrets or raw node credentials.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| `checks` is absent or not an array | Map to `checks: []`; the dialog should render the empty/fallback state. |
| `status` is not one of `pass/warn/fail/skip` | Map to `warn`, never to `pass`. |
| `node_id` is missing, non-numeric, or non-finite | Map to `0` or another safe non-NaN fallback. |
| `evidence` or `suggestion` is missing | Map to `""` and let the component render existing fallback labels. |
| API request fails | Keep the selected node dialog context, show an error alert/toast, and do not render stale previous results. |

### 5. Good/Base/Bad Cases

- Good: `runNodeDoctor()` receives snake_case payload, maps it to `NodeDoctorResult`, and the dialog renders backend-provided sanitized evidence/suggestions.
- Base: missing `checks` maps to an empty array while summary counts remain zero.
- Bad: passing raw `node_id`/`generated_at` objects into components, coercing unknown statuses to `pass`, or allowing `Number("bad")` to become `NaN` in state.

### 6. Tests Required

- API mapper tests must cover snake_case to camelCase mapping and status normalization.
- API mapper tests must cover invalid numeric IDs and missing check arrays.
- UI tests must verify a node-context Doctor entry can run diagnostics and show check evidence/suggestions.
- `npm run check` must keep the Doctor status union and component props valid.

### 7. Wrong vs Correct

Wrong:

```ts
const result = await request<NodeDoctorResult>(`/nodes/${nodeId}/doctor`, { method: "POST", token });
const nodeId = Number(result.node_id);
```

Correct:

```ts
const row = await request<NodeDoctorResponse>(`/nodes/${nodeId}/doctor`, { method: "POST", token });
return mapNodeDoctorResult(row);
```

---

## Scenario: Backup Confidence Mapping

### 1. Scope / Trigger

- Trigger: adding or changing frontend access to backup confidence items,
  reasons, evidence, or next-step recommendations.
- Applies to API modules under `web/src/lib/api/`, shared domain types in
  `web/src/types/domain.ts`, and components that render backup confidence.

### 2. Signatures

- Raw API endpoint: `GET /overview/backup-confidence` through `request<T>()`.
- Raw summary fields include `at_risk`; domain summary field is `atRisk`.
- Raw item fields include `policy_id`, `policy_name`, `node_id`, `node_name`,
  `next_steps`, and `targets[].last_backup_at`.
- Raw evidence fields include `observed_at`, `task_id`, `task_run_id`, and
  `alert_id`.
- Domain status union: `"healthy" | "warning" | "at_risk" | "insufficient"`.

### 3. Contracts

- Keep raw snake_case confidence response types private to the API module.
- Map all confidence response fields to camelCase before components render them.
- Use `Array.isArray` for `items`, `reasons`, `evidence`, `next_steps`, and
  `targets`; default missing arrays to `[]`.
- Preserve `at_risk` as the status value because it is the backend/API contract;
  only the object key changes to `summary.atRisk`.
- Components must render backend-provided sanitized evidence/reason text as-is;
  do not parse or enrich it with node connection details or secret-bearing data.

### 4. Tests Required

- API mapper tests must cover `at_risk` -> `atRisk`, `next_steps` ->
  `nextSteps`, `observed_at` -> `observedAt`, `task_run_id` -> `taskRunId`, and
  `last_backup_at` -> `lastBackupAt`.
- UI tests must cover a non-healthy confidence item and assert the Backups page
  exposes a clear confidence entry.
- `npm run check` must keep the confidence status union and mapper types valid.

### 5. Wrong vs Correct

Wrong:

```ts
const status = raw.status === "healthy" ? "healthy" : "warning";
const firstStep = raw.next_steps?.[0]?.label;
```

Correct:

```ts
const item = mapBackupConfidenceItem(raw);
const firstStep = item.nextSteps[0]?.label;
```

---

## Scenario: Drill Evidence Mapping

### 1. Scope / Trigger

- Trigger: adding or changing frontend access to policy `latest_drill`, task-run `drill_evidence`, restore drill phase fields, or drill trigger types.
- Applies to API modules under `web/src/lib/api/`, shared domain types in `web/src/types/domain.ts`, and components that render policy drill summaries or task-run drill evidence.

### 2. Signatures

- Raw policy API field: `latest_drill`.
- Raw task-run detail API field: `drill_evidence`.
- Domain types: `PolicyLatestDrillSummary`, `RestoreDrillEvidence`, and `TaskRunTriggerType` including `"drill"`.
- API mapper signatures: policy mappers return `latestDrill`; task-run mappers return `drillEvidence`.

### 3. Contracts

- Keep raw snake_case response types private to the API module that receives them.
- Map `latest_drill` to `PolicyLatestDrillSummary | null` before policy components render it.
- Map `drill_evidence` to `RestoreDrillEvidence | null` before task-run components render it.
- `trigger_type="drill"` must remain distinct; do not normalize unknown or drill trigger types to `manual`.
- Components read only camelCase fields such as `latestDrill`, `drillEvidence`, `failedStep`, `confidenceEligible`, `postVerifyFinishedAt`, and `cleanupError`.
- Task-run history rows may omit `drillEvidence`; detail UI that renders evidence must fetch `GET /api/v1/task-runs/:id` or otherwise load the full detail payload before showing evidence-specific sections.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| `latest_drill` is absent or null | Map to `latestDrill: null` or `undefined`; policy cards should show no latest-drill proof rather than crash. |
| `drill_evidence` is absent or null | Map to `drillEvidence: null`; task-run detail should render normal run details without evidence sections. |
| Numeric fields arrive as strings or missing values | Normalize with `Number(...)` and safe defaults at the mapper boundary. |
| Time fields are missing | Keep the corresponding camelCase time field undefined and let the component render a fallback. |
| `trigger_type` is `drill` | Preserve `"drill"` for labels, badges, and filtering. |
| Evidence error text is present | Render sanitized text from the API; do not parse or enrich it with sensitive raw data. |

### 5. Good/Base/Bad Cases

- Good: `task-runs-api.ts` receives `post_verify_finished_at` and exposes `postVerifyFinishedAt`, then `TaskRunDetail` renders it from `run.drillEvidence`.
- Base: a task-run list row lacks `drill_evidence`; the detail dialog fetches the task-run detail endpoint and then renders evidence if present.
- Bad: a component reads `row.drill_evidence.post_verify_error` directly or treats `trigger_type="drill"` as `manual`.

### 6. Tests Required

- API mapper tests must cover `latest_drill` -> `latestDrill` and `drill_evidence` -> `drillEvidence`.
- API mapper tests must include phase fields such as `post_verify_finished_at`, `post_verify_error`, `cleanup_status`, and `confidence_eligible`.
- Type tests or `npm run check` must ensure `"drill"` is part of `TaskRunTriggerType`.
- Component tests for task-run detail must cover the detail fetch path, not only already-expanded list data.
- i18n tests/checks must keep drill labels and failed-step labels in sync across supported locales.

### 7. Wrong vs Correct

Wrong:

```ts
const failedStep = raw.drill_evidence?.failed_step;
const triggerType = raw.trigger_type === "cron" ? "cron" : "manual";
```

Correct:

```ts
const run = mapTaskRun(raw);
const failedStep = run.drillEvidence?.failedStep;
const triggerType = run.triggerType;
```
