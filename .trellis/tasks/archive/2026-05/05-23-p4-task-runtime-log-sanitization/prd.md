# P4 task runtime log sanitization

## Goal

Reduce least-privilege blast radius for task execution evidence by preventing raw hook commands, remote command output, paths, endpoints, hostnames, and token-like values from being stored in `TaskLog.Message`, `TaskRun.LastError`, `Task.LastError`, or pushed through task log WebSocket events.

## Requirements

* Add a local backend sanitization boundary for task runtime log messages before they are persisted or published.
* Sanitize task failure strings before writing `TaskRun.last_error` and `Task.last_error` for the targeted task runner paths.
* Stop logging raw pre-hook/post-hook command text; keep lifecycle messages that preserve operator usability.
* Stop including raw hook failure output in persisted/user-visible task errors.
* Sanitize restore runner messages that include source/target path details before they reach task logs.
* Preserve existing database schema, API field names, WebSocket event shape, scheduling behavior, task status transitions, alert raising, and executor behavior.
* Reuse existing sanitizer utilities where possible; keep the slice local-only and avoid external secret managers or broader policy rewrites.

## Acceptance Criteria

* [ ] `TaskLog.Message` does not contain raw hook command text, hook output, endpoint/query token values, hostnames, or source/target path strings in covered runner paths.
* [ ] WebSocket log events publish the same sanitized message that is persisted.
* [ ] `TaskRun.LastError` and `Task.LastError` do not contain raw hook output or sensitive URL/host/path strings in covered failure paths.
* [ ] Pre-hook/post-hook lifecycle logs remain useful without echoing the command.
* [ ] Existing API response schemas and status transitions are unchanged.
* [ ] Targeted backend tests cover log sanitization, hook failure sanitization, and restore path message sanitization.
* [ ] Backend package/full verification passes before commit.

## Definition of Done

* Tests added/updated for backend task log and LastError sanitization.
* Targeted backend tests pass.
* Full backend tests/build/lint pass or any unrelated flake is rerun and documented.
* Trellis task is archived and journaled before PR.
* PR, CI, merge, release, Docker publish, and local main sync are completed.

## Technical Approach

Introduce a task-package local sanitizer layer around task runtime evidence:

* Apply sanitization in `Manager.emitLog` or the immediate log write boundary so both database persistence and WebSocket publication share one sanitized message.
* Add small helpers for task runtime messages and task LastError values that build on `util.SanitizeMessage` while additionally hiding path-heavy task evidence.
* Replace raw pre/post hook lifecycle messages with generic lifecycle messages.
* Replace hook failure error construction/logging with sanitized error text and no raw command output.
* Keep executor invocation and hook execution inputs unchanged; only alter persisted/published evidence.

## Decision (ADR-lite)

**Context**: P4 has already centralized several credential/provider seams, but task logs and LastError fields remain a direct UI/API/WebSocket storage surface for command text/output, paths, hostnames, and endpoint-shaped errors.

**Decision**: Add a local task runtime evidence sanitization boundary and adjust hook/restore runner messages, without changing schemas or disabling task execution features.

**Consequences**: Operators keep task lifecycle visibility but lose raw command/output/path detail in persisted logs. Deeper command executor stdout/stderr policy can remain a follow-up slice if needed.

## Out of Scope

* External Vault/KMS/SSH CA/session recording/command approval.
* Database schema or API contract changes.
* Frontend redesign or new UI state fields.
* Changing actual executor commands, backup/restore transport, hook storage, or policy templates.
* Full command executor stdout/stderr redesign beyond the shared sanitization boundary.
* Retrofitting or rewriting historical task log records.

## Technical Notes

* Existing task log persistence and WebSocket fanout share `backend/internal/task/log_writer.go`.
* Hook runner currently includes raw remote output in returned errors from `backend/internal/task/hook.go`.
* `backend/internal/task/runner.go` currently logs raw pre/post hook command text and restore source/target paths.
* API handlers return `TaskLog`, `TaskRun`, and `Task` fields directly, so storage-time sanitization is the safest minimal boundary.
* Relevant specs: `.trellis/spec/backend/logging-guidelines.md`, `.trellis/spec/backend/error-handling.md`.
