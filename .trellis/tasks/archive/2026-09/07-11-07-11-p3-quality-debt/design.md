# P3 bounded quality-debt disposition — 2026-09-17

## Evidence and decisions

Baseline: main `491459f5c52a46db549346d3b9f1227ecf9a4b63` after #540. User authorized reevaluation and necessary narrow repairs in this existing task.

- Auth/RBAC/ownership middleware still returns `{error}` while the frontend extracts envelope messages. Reuse a middleware-local response helper/type (handlers cannot be imported without a dependency cycle). Include null data for ordinary errors, preserve safe messages/status/abort and existing 429 Retry-After body/header consistency. Align audit missing-context errors too. Do not normalize metrics authentication, content, streams, CORS or upgrade protocols indiscriminately.
- Panel editor stores the second RAF on its React setter through unknown casts and fails to cancel ID zero. Use effect-local nullable frame ownership. Preserve two-frame readiness; no new generic hook or dashboard redesign.
- Hub Publish emits one standard-log warning per drop, amplifying overload. Count every drop but emit the first warning and at most one per 30-second interval per Hub. Log aggregate dropped count without event payload; keep Publish non-blocking. Make touched handshake diagnostics respect debug level and avoid raw client-controlled data. Prefer small concurrency-safe state with deterministic time-based tests, not a new logging framework or config.

## Scope dispositions

Earlier CAPTCHA/SW/i18n/CSP/bootstrap work remains delivered. Existing 429 format is reused. Terminal/reporting/SSH/migrator/auth legacy logging is not a mechanical zero-Printf target: preserve diagnostics rather than broadly rewrite modules for this bounded flood fix. Config logging deliberately precedes logger initialization. Generic WebSocket protocol changes and KDF salt migration remain outside current scope as previously decided; neither is claimed implemented.

## Verification

Cover API 400/401/403/500/503/429 envelopes and abort semantics, safe messages, Retry-After and frontend extraction; retain metrics/content/upgrade regressions. Cover RAF cancellation before either frame, zero IDs, close/reopen/unmount and two-frame readiness. Cover concurrent saturated Publish, exact drop counts, warning rate bounds and interval reset without sleeps or payload disclosure. Preserve existing authorization/ownership/redaction and connection bounds.
