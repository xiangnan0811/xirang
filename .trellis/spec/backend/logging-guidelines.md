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
- HTTP access logs include `method`, `path`, `status`, `latency_ms`,
  `client_ip`, optional `request_id`, and optional `user_id`.
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
- Do not downgrade unexpected server failures to silent catches. Either return
  the error to the caller or log it with enough structured fields to debug.
