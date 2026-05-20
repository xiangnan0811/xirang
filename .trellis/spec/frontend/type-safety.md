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
- Numeric summary counts, scores, IDs, and evidence IDs must use finite-number
  fallback helpers so invalid wire values never enter React state as `NaN`.

### 4. Tests Required

- API mapper tests must cover `at_risk` -> `atRisk`, `next_steps` ->
  `nextSteps`, `observed_at` -> `observedAt`, `task_run_id` -> `taskRunId`, and
  `last_backup_at` -> `lastBackupAt`.
- API mapper tests must cover invalid numeric summary/evidence fields and assert
  safe fallbacks rather than `NaN`.
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

## Scenario: Credential Access Grant Mapping

### 1. Scope / Trigger

- Trigger: adding or changing frontend access to credential access grant request/response DTOs.
- Applies to `web/src/lib/api/credential-access-grants-api.ts`, `web/src/types/domain.ts`, API client exports, and UI that renders grant status, expiry, node binding, or denial details.

### 2. Signatures

- Raw API endpoint: `POST /credential-access-grants/terminal` through `request<T>()`.
- Raw request fields: `node_id`, `reason`, and optional `ttl_seconds`; step-up proof is sent through the existing request option/header, not in the JSON body.
- Raw response fields include `id`, `requester_user_id`, `requester_username`, `requester_role`, `action`, `purpose`, `node_id`, `reason`, `status`, `requested_ttl_seconds`, `expires_at`, optional `approver_user_id`, optional `approver_username`, `created_at`, and `updated_at`.
- Domain type: `CredentialAccessGrant` with camelCase fields such as `requesterUserId`, `nodeId`, `requestedTtlSeconds`, `expiresAt`, and `createdAt`.

### 3. Contracts

- Keep raw snake_case grant types private to the API module; components consume only camelCase domain fields.
- Normalize all numeric IDs/TTLs with finite-number fallbacks so invalid wire values never enter React state as `NaN`.
- Preserve grant status/action/purpose strings from the backend as constrained unions where practical; unknown status should degrade to a safe non-authorizing value such as `expired` or `denied`.
- Treat `reason` from the API as already sanitized backend text, but keep it bounded in mappers/UI and never enrich it with hostnames, usernames-plus-hosts, endpoints, credentials, command text, or terminal output.
- Grant-required error detection must use machine-readable error codes such as `CREDENTIAL_GRANT_REQUIRED`; do not infer grant state from localized message text.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Response has missing or invalid numeric fields | Map to finite safe fallbacks, never `NaN`. |
| Optional approver fields are null/missing | Map to `undefined` or `null` consistently; do not invent an approver. |
| Unknown `status` value | Degrade to a safe non-active status and keep UI non-authorizing. |
| Raw `expires_at` / `created_at` is missing or malformed | Keep the raw string if displayable or fallback to an empty string; do not store `Invalid Date`. |
| API throws `CREDENTIAL_GRANT_REQUIRED` or related denial code | Keep it as an API/control-flow error, not as auth session expiry. |

### 5. Good/Base/Bad Cases

- Good: mapper receives `requested_ttl_seconds` and exposes `requestedTtlSeconds`; terminal UI uses typed API method and renders sanitized backend message.
- Base: missing optional approver maps to no approver display while the grant remains usable according to backend status/expiry.
- Bad: passing raw `node_id`/`expires_at` objects into components, using localized message text to detect grant-required, or storing grant DTOs in browser storage.

### 6. Tests Required

- API mapper tests must cover snake_case to camelCase mapping for all grant fields.
- API mapper tests must cover invalid numeric fields, nullable approver fields, malformed time fields, and unknown status fallback.
- Core error tests or component tests must cover machine-readable grant-required detection separately from login/session expiry handling.
- UI tests must prove grant DTOs and reason drafts are not persisted to browser storage.

### 7. Wrong vs Correct

Wrong:

```ts
const grant = await request<CredentialAccessGrant>("/credential-access-grants/terminal", { token });
if (error.message.includes("grant")) openGrantDialog();
```

Correct:

```ts
const raw = await request<CredentialAccessGrantResponse>("/credential-access-grants/terminal", { token });
return mapCredentialAccessGrant(raw);
```

---

## Scenario: SSH Key Scope Mapping

### 1. Scope / Trigger

- Trigger: adding or changing frontend access to managed SSH key least-privilege metadata.
- Applies to `web/src/lib/api/ssh-keys-api.ts`, shared `SSHKeyRecord` / `NewSSHKeyInput` types in `web/src/types/domain.ts`, SSH key create/edit/batch-import dialogs, SSH key grid/table scope badges, demo mock data, and tests.

### 2. Signatures

- Raw SSH key response fields include `disabled`, `expires_at`, `allowed_purposes`, `allowed_node_ids`, `allowed_node_tags`, and response-only `broad_scope`.
- Domain fields are `disabled`, `expiresAt`, `allowedPurposes`, `allowedNodeIds`, `allowedNodeTags`, and `broadScope`.
- Request payload fields sent to the backend are `disabled`, `expires_at`, `allowed_purposes`, `allowed_node_ids`, and `allowed_node_tags`.

### 3. Contracts

- Keep raw snake_case SSH key response/request types private to `ssh-keys-api.ts`; components must consume only camelCase domain fields.
- Map `expires_at` to a `datetime-local` compatible string for editor state when possible. Invalid but displayable values may be sliced safely; invalid create/update values should serialize to `null` rather than producing `Invalid Date` or `NaN`-like state.
- Normalize numeric key/test IDs with finite-number fallbacks so invalid values do not enter React state as `NaN`.
- Preserve unknown/invalid `key_type` as safe domain fallback `auto` through `parseSSHKeyType`.
- Empty `allowedPurposes`, `allowedNodeIds`, or `allowedNodeTags` means broad compatibility. Components may show warning badges, but must not invent client-side enforcement rules beyond backend-provided `broadScope` and metadata.
- SSH key create, update, and batch import must send scope metadata alongside key identity/material fields. Batch import examples must use obviously fake key material.
- Components render scope badges/fields as advisory UI only. Do not show hostnames, usernames-plus-hosts, private keys, passwords, credential audit metadata, or remediation actions that mutate keys/nodes from a risk card.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| `expires_at` is missing/null | Map to `expiresAt: undefined` or empty editor value. |
| `expires_at` is invalid | Keep a safe display fallback; do not store `Invalid Date` or `NaN`. |
| Raw `id` or test-result numeric fields are invalid | Fall back to `0` or another safe finite number. |
| Raw `key_type` is unknown | Map to `auto`. |
| `broad_scope` is missing | Map to `false` unless the backend explicitly reports broad scope. |
| API request fails | Keep dialogs/pages usable, show the existing error path, and do not mark scope changes as saved. |

### 5. Tests Required

- API mapper tests must cover snake_case to camelCase mapping for all scope fields and private-key exclusion from records.
- API mapper tests must cover invalid numeric IDs, unknown key types, nullable/invalid expiry, and create/update request serialization to snake_case.
- UI tests should cover create/edit scope fields and scope badges in table/grid when those surfaces change.
- Batch import tests or snapshots should include scope metadata and fake private-key examples.
- `npm run check` must keep `SSHKeyScopeFields`, `SSHKeyRecord`, and `NewSSHKeyInput` users in sync.

### 6. Wrong vs Correct

Wrong:

```ts
const rows = await request<SSHKeyRecord[]>("/ssh-keys", { token });
setKeys(rows); // raw snake_case data can leak into components
```

Correct:

```ts
const rows = await request<SSHKeyResponse[]>("/ssh-keys", { token });
return rows.map(mapSSHKey);
```

---

## Scenario: Settings Security Risk Summary Mapping

### 1. Scope / Trigger

- Trigger: adding or changing frontend access to Settings security-risk summary data.
- Applies to `web/src/lib/api/settings-api.ts`, Settings system-tab UI, locale strings for risk summaries, and tests that render the advisory cards.

### 2. Signatures

- Raw API endpoint: `GET /settings/security-risk-summary` through `request<T>()`.
- Raw response fields: `generated_at`, `summary.total_risks`, `summary.categories`, and `items`.
- Raw item fields: `code`, `severity`, `title`, `description`, `count`, and `examples`.
- Domain types: `SecurityRiskSummary`, `SecurityRiskItem`, `SecurityRiskCode`, and `SecurityRiskSeverity`.
- API mapper signature: `mapSecurityRiskSummary(raw) -> SecurityRiskSummary`.

### 3. Contracts

- Keep raw snake_case response types private to `settings-api.ts`.
- Map `generated_at` to `generatedAt`, `total_risks` to `totalRisks`, and preserve item arrays as camelCase domain data before components render them.
- Supported codes are `root_ssh_users`, `reused_ssh_keys`, `sudo_enabled_nodes`, `broad_scope_ssh_keys`, `disabled_ssh_keys_in_use`, `expired_ssh_keys_in_use`, `stale_ssh_keys`, `recent_credential_operations`, and `weak_security_defaults`; unknown codes must degrade to a safe known code rather than entering component state as arbitrary strings.
- Supported severities are `info`, `warning`, and `critical`; unknown severities must degrade to `warning`, never to a success state.
- Normalize numeric fields with finite-number fallbacks so invalid counts never become `NaN`.
- `examples` must be an array of strings after mapping; invalid or missing examples become `[]`.
- Components render backend-provided advisory text/examples as-is after mapping. Do not enrich them with hostnames, usernames, credentials, executor configs, raw endpoints, credential-audit metadata, remediation links, or one-click actions.
- P1 SSH-key and credential-operation risk codes are advisory only; Settings UI may show counts/examples but must not add mutation links/buttons that disable, delete, rotate, or re-scope keys from the risk summary.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Response is null/undefined | Map to empty generatedAt, zero summary counts, and empty item list. |
| `items` is absent or not an array | Map to `items: []`. |
| `summary.total_risks`, `summary.categories`, or item `count` is invalid | Map to `0`, not `NaN`. |
| Unknown `severity` | Map to `warning`. |
| Unknown `code` | Map to a safe known code such as `weak_security_defaults`. |
| P1 SSH-key/credential-operation codes are present | Preserve the supported code values through the mapper. |
| `examples` includes non-string values | Convert displayable entries to strings and drop empty values. |
| API request fails | Settings system tab should keep the rest of settings usable and avoid rendering stale risk details as confirmed current data. |

### 5. Good/Base/Bad Cases

- Good: mapper receives `generated_at`, `total_risks`, and `root_ssh_users`; Settings renders an advisory card with count, title, description, and sanitized examples.
- Base: missing `items` maps to an empty list; Settings shows the rest of the system settings normally.
- Bad: passing raw snake_case data into the component, rendering a link/button that mutates nodes/SSH keys, or appending extra host/credential details on the client side.

### 6. Tests Required

- API mapper tests must cover snake_case to camelCase mapping and invalid numeric fallback.
- API mapper tests must cover unknown code/severity fallback, invalid examples handling, and the P1 SSH-key/credential-operation codes.
- Settings UI tests must verify advisory risk cards render counts/examples and do not expose remediation links/actions.
- `npm run check` must keep risk code/severity unions and component props valid.

### 7. Wrong vs Correct

Wrong:

```ts
const risks = await request<SecurityRiskSummary>("/settings/security-risk-summary", { token });
const count = risks.summary.total_risks;
```

Correct:

```ts
const raw = await request<SecurityRiskSummaryRaw>("/settings/security-risk-summary", { token });
return mapSecurityRiskSummary(raw);
```

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
| Numeric fields arrive as strings, missing values, or invalid text | Normalize with finite-number checks and safe defaults at the mapper boundary; do not allow `NaN` into state. |
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

---

## Scenario: Health Incident Timeline Mapping

### 1. Scope / Trigger

- Trigger: adding or changing frontend access to health incident timeline groups, signals, resources, source types, or next-action links.
- Applies to `web/src/lib/api/overview-api.ts`, shared domain types in `web/src/types/domain.ts`, shared overview data context, and Overview timeline UI components.

### 2. Signatures

- Raw API endpoint: `GET /overview/health-incident-timeline?window_hours=<n>` through `request<T>()`.
- Raw top-level fields: `generated_at`, `window_hours`, `summary`, and `groups`.
- Raw group fields: `last_seen_at`, `event_count`, `likely_cause`, `source_types`, `next_actions`, and `signals`.
- Raw resource fields include `node_id`, `node_name`, `policy_id`, and `policy_name`.
- Raw signal fields include `occurred_at`, `alert_id`, `delivery_id`, `task_id`, `task_run_id`, `node_id`, and `policy_id`.
- Domain types: `HealthIncidentTimelineData`, `HealthIncidentGroup`, `HealthIncidentResource`, `HealthIncidentSignal`, and `HealthIncidentAction`.

### 3. Contracts

- Keep raw snake_case timeline response types private to the API module.
- Map all response fields to camelCase before shared context or components render them.
- Use `Array.isArray` for `groups`, `source_types`, `next_actions`, and `signals`; default missing arrays to `[]`.
- Normalize numeric IDs and counts with finite-number checks; invalid or non-positive optional IDs should become `undefined`, while counts should fall back to `0`.
- Preserve supported severity/source/resource unions; unknown severity values must degrade to `warning`, unknown source types to `alert`, and unknown resource types to `platform`.
- Filter out next actions with empty `href` before rendering link buttons.
- Components must render backend-provided sanitized `likelyCause` and signal messages as-is; do not enrich them with node credentials, executor config, raw logs, or alert delivery secrets.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| `groups` is absent or not an array | Map to `groups: []`; Overview should render the empty/fallback state. |
| `source_types`, `next_actions`, or `signals` is absent or invalid | Map the field to an empty array. |
| Unknown `severity` | Map to `warning`, never to `info` or a success state. |
| Unknown `resource.type` | Map to `platform` and use a safe platform name fallback. |
| Numeric IDs, `window_hours`, summary counts, or `event_count` arrive as strings, missing values, zero, or invalid text | Map valid positive IDs to numbers; invalid optional IDs become `undefined`, and count/window fields fall back to `0` rather than `NaN`. |
| API request fails | Keep the Overview page usable, show the timeline error state, and do not render stale previous incident groups. |

### 5. Good/Base/Bad Cases

- Good: `overview-api.ts` receives `last_seen_at` and `next_actions`, exposes `lastSeenAt` and `nextActions`, then Overview renders a severity badge, resource label, likely cause, event count, source chips, and primary action link.
- Base: empty or missing `groups` maps to an empty list while summary counts remain zero, and the Overview panel renders the healthy/empty state.
- Bad: passing raw snake_case group objects into `OverviewPage`, using `Number("bad")` as `NaN` in state, or rendering action links whose `href` is blank.

### 6. Tests Required

- API mapper tests must cover `last_seen_at` -> `lastSeenAt`, `event_count` -> `eventCount`, `source_types` -> `sourceTypes`, `next_actions` -> `nextActions`, `occurred_at` -> `occurredAt`, and `task_run_id` -> `taskRunId`.
- API mapper tests must cover unknown severity/source/resource fallback and invalid numeric IDs.
- UI tests must verify the Overview timeline renders a non-empty incident group with severity, resource, likely cause, event count, and a next-action link.
- UI tests should cover loading, empty, and error states when the shared context behavior changes.
- `npm run check` must keep the timeline status/source/resource unions and component props valid.

### 7. Wrong vs Correct

Wrong:

```ts
const firstAction = raw.groups?.[0]?.next_actions?.[0]?.href;
setIncidentGroups(raw.groups as HealthIncidentGroup[]);
```

Correct:

```ts
const timeline = mapHealthIncidentTimeline(raw);
setIncidentGroups(timeline.groups);
```

---

## Scenario: Demo Mode Trusted Ops Story

### 1. Scope / Trigger

- Trigger: adding or changing frontend demo mode behavior, mock data, no-token
  console access, trusted-ops walkthroughs, or demo-only write/read flows.
- Applies to `VITE_ENABLE_DEMO_MODE`, `web/src/data/mock.ts`, auth/routing
  guards, shared console data hooks, demo operation helpers, and public docs that
  describe demo behavior.

### 2. Signatures

- Env key: `VITE_ENABLE_DEMO_MODE=true` enables the no-token frontend demo path.
- Demo data source: `loadMocks() = import("@/data/mock")` should remain lazily
  loaded from demo/no-token branches.
- Auth/routing boundary: `/app/*` may be entered without a token only when demo
  mode is explicitly enabled.
- User-facing docs/examples: `web/.env.example` and `docs/env-vars.md` must
  describe demo mode as mock-only.

### 3. Contracts

- Demo mode must be opt-in through `VITE_ENABLE_DEMO_MODE=true`; authenticated
  users should continue using normal API-backed data paths.
- No-token demo paths must never call write APIs, connect to real servers, use
  real SSH keys, or touch backup storage. Use local mock state and demo helper
  functions instead.
- Login, app-shell, and any demo entry copy must clearly say the console uses
  mock data only and is not connected to real infrastructure.
- Mock data should include at least one successful trusted-ops path and one
  explainable failure path covering backup confidence, restore drill evidence,
  SSH diagnostics/Doctor evidence, health incident timeline, and task/log
  evidence when those surfaces are part of the demo story.
- Public docs must not claim hosted demo infrastructure, telemetry collection,
  production maturity, or user scale that is not backed by current repository
  state.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| `VITE_ENABLE_DEMO_MODE` is absent or not `true` and there is no token | Protected routes redirect to login; no mock console access. |
| `VITE_ENABLE_DEMO_MODE=true` and there is no token | Protected routes allow `/app/*`; data comes from lazy-loaded mocks. |
| A demo write/read action has no token | Use local mock behavior or a safe empty fallback; do not call the backend. |
| Demo copy or docs mention real infrastructure | Revise to mock-only wording before merging. |
| Demo mock stories include only healthy states | Add an explainable failure path with evidence and next-step guidance. |

### 5. Good/Base/Bad Cases

- Good: login exposes a demo entry only when demo mode is enabled, `/app/*`
  renders mock-only banners, and mock data shows a successful backup/drill path
  plus an SSH/incident failure path with evidence.
- Base: no-token demo mode is disabled; users without tokens remain on login and
  the production bundle does not eagerly import mock data.
- Bad: bypassing `ProtectedRoute` for every environment, silently showing
  production-looking mock data without a banner, or calling real APIs from a
  no-token demo action.

### 6. Tests Required

- Route/auth tests must cover no-token access denied by default and allowed only
  when `VITE_ENABLE_DEMO_MODE=true`.
- Login or shell tests must assert mock-only demo copy is visible when demo mode
  is enabled and that entering demo mode does not submit login credentials.
- Hook or page tests must assert demo mock data includes both a successful
  trusted path and an explainable failure path with evidence/next actions.
- `npm run check`, `git diff --check`, and doc freshness checks must pass when
  demo docs or mock code change.

### 7. Wrong vs Correct

Wrong:

```ts
const demoMode = true;
return <Navigate to="/app" replace />;
```

Correct:

```ts
const demoModeEnabled = import.meta.env.VITE_ENABLE_DEMO_MODE === "true";
if (!token && !demoModeEnabled) return <Navigate to="/login" replace />;
```
