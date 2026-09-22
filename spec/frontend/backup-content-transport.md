# Backup Content Transport

## Scenario: Admin Setting And Closed Error Guidance

### 1. Scope / Trigger

- Trigger: changing the private-network content transport Admin control, the
  delivery-ticket 503 product, or error UI for preview/download/export/archive/
  recovery content.
- Applies to the narrow settings mapper/API, Backups Overview panel, shared
  backup-assets error mapper, role-aware guidance, and every delivery surface.

### 2. Signatures

- Setting key:
  `backup_assets.content_allow_insecure_private_network`.
- Environment definition identity:
  `BACKUP_ASSETS_CONTENT_ALLOW_INSECURE_PRIVATE_NETWORK`.
- Typed state:
  `{ enabled: boolean; source: "db" | "env" | "default" }`.
- API: `GET /settings` and `PUT /settings` through the central `request()`
  wrapper; PUT sends only the exact setting key with string `"true"|"false"`.
- Closed ticket error:
  `BackupAssetsUIErrorCode = "secure_transport_required"`, non-retryable and
  with action `"none"`.

### 3. Contracts

- The mapper accepts the setting only when its definition has the exact key,
  env var, bool type, `backup_assets` category, and code default `false`, and the
  resolved value/source is closed. Invalid or future shapes throw; do not cast.
- Mount the control only for an authenticated Admin in Backups Overview at
  `#backup-assets-content-transport`. Enabling requires explicit risk
  confirmation. Disable/save is single-flight; a failed update keeps the last
  server-confirmed state and exposes a safe error. Loading, saved, warning, and
  focus states remain accessible.
- Recognize `secure_transport_required` only for HTTP 503 in the
  `content_ticket` context when `data.reason` contains exactly `code` and an
  empty `params` object. Other 503s and malformed/future reason products retain
  generic unavailable behavior.
- Admin guidance links to the exact Overview hash. Operator guidance says to use
  HTTPS or contact an Admin and offers no settings action. Viewer/unknown roles
  receive only generic safe guidance.
- Apply the same closed error/guidance to native preview and original download,
  Export artifact, archive member, and Recovery result. Never render or persist
  a token, proof, ticket, delivery URL, Provider locator, backup path/name, or
  content while explaining transport policy.

### 4. Validation & Error Matrix

| Input/state | UI result |
|---|---|
| Exact setting definition/value/source | Render canonical enabled state and source. |
| Invalid definition, value, or source | Mapper throws; panel shows safe load error. |
| Admin enables | Confirm risk, send one PUT, then show saved DB state. |
| PUT fails or is duplicated while pending | Keep prior state, show safe error, issue no duplicate PUT. |
| Exact content-ticket 503 reason | Non-retryable transport error plus role-aware guidance. |
| Generic 503 or reason with extra/non-empty params | Preserve generic unavailable behavior. |
| Operator/Viewer reaches a delivery error | No Admin settings mutation or privileged detail. |

### 5. Good/Base/Bad Cases

- Good: an Admin follows the delivery error link, focus lands on the Overview
  panel, confirms the plaintext-LAN warning, and one typed PUT saves the setting.
- Base: the setting remains false and all ordinary 503 products keep the
  existing generic retry/unavailable behavior.
- Bad: map every 503 to the transport action, place the key in the generic
  settings category order, use direct `fetch`, optimistically flip state before
  server success, or expose the failed asset in the guidance.

### 6. Tests Required

- Mapper tests cover exact definition identity, bool/source unions, invalid
  shapes, and exact PUT body through the central request wrapper.
- Panel tests cover Admin-only mount, load/source display, enable confirmation,
  disable, single-flight, failed-save rollback, saved live region, warning, and
  hash focus.
- Error tests mutation-test status, context, code, exact reason keys, and empty
  params. Surface tests cover Admin, Operator, Viewer/unknown, canary
  non-leakage, preview/original, Export, archive, and Recovery.
- Run `env -u NODE_ENV npm run check` and source scans forbidding direct fetch,
  storage/history/router persistence, `any`, and `unknown as T` bypasses.

### 7. Wrong vs Correct

Wrong:

```ts
if (error.status === 503) {
  return { code: "secure_transport_required", action: "open_settings" };
}
```

Correct:

```ts
if (error.status === 503 && context === "content_ticket" && isExactTransportReason(error.detail)) {
  return uiError("secure_transport_required", false, "none");
}
```
