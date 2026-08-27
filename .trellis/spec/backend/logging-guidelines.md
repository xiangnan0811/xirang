# Logging Guidelines

> How logging is done in this project.

---

## Overview

The primary logging library is `zerolog`, initialized in
`backend/internal/logger/logger.go`. New code should prefer
`logger.Module("<module>")` so every structured event includes a `module`
field. HTTP access logging is handled by
`backend/internal/middleware/structured_logger.go`.

There are still legacy `log.Printf` call sites in older packages. Do not copy
that pattern into new code unless the surrounding file is already using it and
the change is intentionally minimal.

---

## Log Levels

- `Debug`: high-volume diagnostics that are disabled by default, such as EWMA
  anomaly details.
- `Info`: lifecycle events and successful background maintenance, for example
  server startup, bootstrap seeding, retention completion, and aggregate cleanup
  summaries.
- `Warn`: recoverable failures, skipped work, degraded behavior, retryable
  dispatch errors, queue saturation, and validation rejections worth observing.
- `Error`: unexpected failures that prevent a requested operation or background
  job from completing.
- `Fatal`: startup failures that mean the process cannot safely run, as in
  `backend/cmd/server/main.go`.

---

## Structured Logging

- Use `logger.Module("name").Level()` and attach stable fields with typed
  zerolog helpers (`Uint`, `Int`, `Str`, `Err`, `Time`, etc.).
- Ordinary HTTP access logs include `method`, `path`, `status`, `latency_ms`,
  `client_ip`, optional `request_id`, and optional `user_id`. Content-shaped
  requests are the deliberate privacy exception below.
- Include identifiers that let maintainers connect logs to data:
  `task_id`, `task_run_id`, `node_id`, `alert_id`, `integration_id`,
  `policy_id`, and `worker` are established examples.
- Use `Err(err)` rather than formatting errors into strings when using zerolog.
- Keep log messages short and action-specific. Current backend log messages are
  mostly Simplified Chinese; English module names and field names are normal.

---

## What to Log

- Startup and shutdown milestones in `cmd/server/main.go`.
- Background worker failures and recoverable skips in task, alerting,
  nodelogs, metrics, SLO, anomaly, and escalation packages.
- Security-relevant warnings such as disabled SSH host key checking or rejected
  path validation.
- Internal server errors through `respondInternalError`, which adds route path
  context.
- Queue overflow/fallback paths, for example task log or sample writer fallback
  behavior.
- External delivery failures and retry outcomes, with channel IDs but without
  secrets.

---

## What NOT to Log

- Do not log passwords, private keys, TOTP secrets, JWTs, recovery codes,
  `DATA_ENCRYPTION_KEY`, SMTP passwords, webhook secrets, bearer tokens, or raw
  notification endpoints.
- Do not log decrypted values returned by model hooks. If a value came from
  `secure.DecryptIfNeeded`, treat it as sensitive.
- Do not log full command output when it may contain credentials. When output is
  needed for diagnosis, keep it scoped to existing patterns and prefer task log
  storage over global process logs.
- Do not log decrypted system setting values, exported config payloads, SFTP file
  contents, Docker command output or volume names, node Doctor evidence, migration
  preflight command output, executor config, or credential audit metadata that may
  contain raw remote evidence.
- Do not downgrade unexpected server failures to silent catches. Either return
  the error to the caller or log it with enough structured fields to debug.

---

## Scenario: Secret-Safe Asset Content Logging

### 1. Scope / Trigger

- Trigger: adding a cookie-authenticated or opaque-ID content route, changing
  HTTP access logging/recovery, or adding content audit/metrics fields.
- Applies to `StructuredLogger`, `ContentSafeRecovery`, backup-content Broker
  logs/audit/metrics, and the dedicated Nginx content log format.

### 2. Signatures

- Safe route label:
  `middleware.BackupContentSafeRoute = "/api/v1/asset-content/:deliveryId"`.
- Path classifier: `middleware.IsBackupContentShapedPath(path string) bool`.
- Recovery wrapper: `middleware.ContentSafeRecovery() gin.HandlerFunc`.
- Nginx format: `xirang_asset_content` containing only request ID, status,
  body bytes, request time, and upstream timing variables.
- Audit actions: `preview_ticket`, `preview_read`,
  `asset_download_ticket`, and `asset_download`.
- Detached Content cleanup ceilings: `ticketFailureAuditTimeout = 5 * time.Second`
  and `gatewayCleanupTimeout = 5 * time.Second`.

### 3. Contracts

- Every exact or malformed asset-content-shaped path is logged under the
  constant safe route label. Never log the raw delivery ID, URL path,
  `RequestURI`, query, Cookie/Authorization header, session JTI, cookie secret,
  Catalog path/name, Provider locator, or content.
- For every method and content-shaped path, `StructuredLogger` must omit
  `client_ip`, `user_id`, XFF, X-Real-IP, and all other raw request identity
  evidence. Retain only the safe route label, method, status, latency, and
  optional request ID. Ordinary-route identity logging remains unchanged.
- Content-local recovery catches panics before Gin's outer recovery can dump a
  request. It logs only module, fixed category, and optional request ID; panic
  values and request metadata are excluded.
- Content diagnostics may use a per-process keyed delivery fingerprint and
  closed action/outcome/reason/renderer/provider classes. Raw public/internal
  IDs and unbounded MIME/error strings are not log or metric labels.
- Audit correlates with the internal grant ID and a keyed asset fingerprint,
  not the public delivery ID. It records bounded counters and closed failure
  codes; Range requests are aggregated instead of producing one sensitive
  event per seek.
- Nginx content access logging is independently redacted, and content
  locations disable unformattable error logs. Application redaction is not a
  substitute for gateway redaction.
- Client cancellation must reach and join the Provider reader first. The
  subsequent cache revoke, reservation finalization, and aggregate read-audit
  update share one `context.WithTimeout(context.WithoutCancel(requestCtx), 5s)`
  budget. Never use an unbounded detached context; if the bounded cleanup cannot
  prove final state, startup reconciliation charges the full reservation and
  completes the pending aggregate audit.
- Ticket failure after provisional lease acquisition must release that lease
  with the same bounded detached pattern. A caller cancellation or ticket
  deadline must not skip release, while `context.Background()` must not make
  the handler wait without a ceiling. Release failure joins a safe unavailable
  error and leaves the fence for bounded reconciliation.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Content URL includes a valid or malformed delivery suffix | Application path field is the constant safe label. |
| Panic value or request carries ID/cookie/query | Recovery emits none of those values and returns safe content headers/status. |
| A metric label receives an arbitrary ID/MIME/reason | Map to a closed class or `unknown`; never publish high-cardinality private data. |
| Audit write needs asset correlation | Persist internal grant ID/keyed fingerprint only, never public route or cookie/session material. |
| Nginx format contains `$request`, `$request_uri`, `$uri`, `$args`, or cookie variables | Deployment check fails. |
| Required security cleanup fails | Return/record a safe closed error and reconciliation metric; do not silently swallow it or log secrets. |
| Client cancels while DB finalization or audit is blocked | Reader closes immediately; detached cleanup expires within five seconds and leaves conservative reconciliation state. |
| Ticket fails after acquiring its provisional lease | Release uses a detached five-second deadline; failure is surfaced safely and reconciliation retains authority. |

### 5. Good/Base/Bad Cases

- Good: `/api/v1/asset-content/<opaque-id>?x=1` is rejected while both Nginx
  and application logs contain only the safe route/category and request ID.
- Base: a successful Range read contributes bounded aggregate counters and a
  low-cardinality outcome metric.
- Base: a canceled read detaches finalization from the canceled request but
  gives revoke, accounting, and audit one shared finite cleanup deadline.
- Base: a failed ticket detaches provisional lease release from the request's
  cancellation but caps it at five seconds.
- Bad: `event.Str("path", c.Request.URL.Path)`, `event.Err(providerErr)`, or a
  generic recovery dump on a cookie-authenticated content request; also bad is
  `budget.Finalize(context.WithoutCancel(ctx), ...)` with no deadline.

### 6. Tests Required

- Capture structured logs for exact, malformed, trailing-slash, query, and
  panic requests; assert constant route/category and absence of delivery ID,
  Cookie, Authorization, query, panic value, path/name, and locator.
- Audit tests must cover ticket/read success, blocked, failure, idempotent
  retry, aggregate Range counters, and backlog behavior without sensitive
  fields.
- Metrics tests must enumerate accepted labels and prove unknown/high-cardinality
  values collapse to closed `unknown` labels.
- Render and mutation-test the Nginx log format and content-local error logging.
- Capture the contexts received by budget finalization and read-audit after a
  canceled blocking stream. Assert cancellation is detached, both contexts
  have the same future deadline, and that deadline is no more than five seconds
  after cleanup begins.
- Force source-open failure after provisional lease acquisition and assert the
  lease controller receives a non-canceled context with a deadline no more than
  five seconds away.

### 7. Wrong vs Correct

Wrong:

```go
logger.Module("http").Error().
    Str("path", c.Request.URL.Path).
    Str("cookie", c.GetHeader("Cookie")).
    Interface("panic", recovered).
    Msg("content request failed")
```

Correct:

```go
path := c.Request.URL.Path
if middleware.IsBackupContentShapedPath(path) {
    path = middleware.BackupContentSafeRoute
}
logger.Module("http_content").Error().
    Str("category", "content_panic").
    Str("request_id", requestID).
    Msg("content request panic recovered")
```

Wrong detached cleanup:

```go
_ = budget.Finalize(context.WithoutCancel(ctx), intent)
_ = readAudit.RecordRead(context.WithoutCancel(ctx), summary)
```

Correct detached cleanup:

```go
cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
defer cancel()
_ = budget.Finalize(cleanupCtx, intent)
_ = readAudit.RecordRead(cleanupCtx, summary)
```

Use the same pattern for ticket failure cleanup; do not call
`lease.Release(context.Background())` from a request path.
