# P4 next security hardening

## Goal

Implement the next executable local-only P4 hardening slice by adding a node-log content/path sanitization boundary. Node journal/file log entries currently persist remote log paths and raw line content, then return them through the node-log and alert-context APIs. This slice reduces stored and UI-visible runtime evidence while preserving API field names, pagination, filters, and deployment behavior.

## Requirements

- Sanitize node-log `path` and `message` before insertion into `node_logs`.
- Preserve `node_id`, `source`, `timestamp`, `priority`, pagination, time filters, node ownership behavior, and retention behavior.
- Add response-time sanitization for node-log API responses so legacy rows or test fixtures cannot leak raw path/message evidence.
- Keep API schema stable: `path` and `message` remain strings with masked placeholder values.
- Sanitize node log config validation errors so rejected path text is not echoed to clients.
- Keep node log config get/patch success behavior unchanged for configured whitelist paths; this is intentional configuration data, not collected runtime evidence.
- Use local code-only hardening; no external Vault/KMS/SSH CA/session recording/command approval.
- Do not store or return raw secrets, command output/text, file contents, endpoint/proxy values, hostnames, target/include path contents, or host-sensitive strings in logs/audit/docs/UI storage.

## Acceptance Criteria

- [ ] Journal parser masks `_SYSTEMD_UNIT`/path-like values and `MESSAGE` before returning entries for persistence.
- [ ] File parser masks configured file path and line content before returning entries for persistence.
- [ ] Worker-inserted rows contain masked `path` and `message` values.
- [ ] `/api/v1/node-logs` masks `path` and `message` in returned rows even if the DB contains raw legacy values.
- [ ] `/api/v1/alerts/:id/logs` masks `path` and `message` in returned rows even if the DB contains raw legacy values.
- [ ] Log config validation errors do not echo rejected path text.
- [ ] Existing node-log query, ownership, pagination, settings, and config tests still pass.
- [ ] Focused backend tests and relevant package tests pass from the backend module.

## Technical Approach

Add a package-local sanitizer under `backend/internal/nodelogs` mirroring the prior task-runtime hardening pattern: call `util.SanitizeMessage`, mask output markers, URLs/endpoints, remote/named/absolute/Windows paths, IPs/hostnames, and host-sensitive fragments. Apply it in parser constructors before persistence. Add handler-local response mapping in `node_logs_handler.go` to sanitize returned `model.NodeLog` rows before JSON serialization. Keep query predicates operating on stored sanitized data for new rows; response sanitization is a legacy-safety fallback.

## Decision (ADR-lite)

**Context**: Node logs are runtime evidence collected from remote hosts. Existing code stores and returns raw path/message fields directly.

**Decision**: Sanitize at ingestion before persistence, with response-time sanitization as defense-in-depth for pre-existing rows.

**Consequences**: Keyword/path search for newly ingested rows operates on masked content rather than raw remote evidence. This matches the security objective and the previous task-runtime log boundary. API shape remains stable, but users see placeholders instead of sensitive runtime evidence.

## Out of Scope

- Historical DB migration/backfill for existing node_logs rows.
- File browser preview/content behavior.
- Docker volume response masking.
- Node Doctor or migration preflight diagnostic sanitizer.
- External secrets infrastructure, session recording, command approvals, or deployment changes.
- Frontend redesign.

## Research References

- [`research/next-p4-slice.md`](research/next-p4-slice.md) — ranks node-log content/path sanitization as the strongest next local-only P4 slice.

## Technical Notes

- Main files: `backend/internal/nodelogs/parser.go`, `backend/internal/nodelogs/worker.go`, `backend/internal/nodelogs/types.go`, `backend/internal/api/handlers/node_logs_handler.go`, `backend/internal/api/handlers/node_log_config_handler.go`.
- Related specs: `.trellis/spec/backend/logging-guidelines.md`, `.trellis/spec/backend/error-handling.md`, `.trellis/spec/backend/quality-guidelines.md`.
- Prior pattern: task-runtime sanitizers under `backend/internal/task`, `backend/internal/task/executor`, and `backend/internal/task/verifier`.
