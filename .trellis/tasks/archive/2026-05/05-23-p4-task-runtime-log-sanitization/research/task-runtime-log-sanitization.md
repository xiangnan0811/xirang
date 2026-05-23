# Task runtime log sanitization research

## Completed P4 context

The recent P4 hardening slices already covered local credential/provider seams for SSH credentials, executor SSH access, restic repository password access, app profile credentials, and notification integration response/error sanitization. Those changes did not centralize task runtime evidence such as `TaskLog.Message`, `TaskRun.LastError`, `Task.LastError`, or WebSocket log messages.

## Exposure paths found

* `backend/internal/task/log_writer.go`
  * `Manager.emitLog` accepts arbitrary task log messages.
  * `persistLogBatch` writes `model.TaskLog.Message` to the database.
  * `publishLogEvent` sends the same message to WebSocket clients.
* `backend/internal/task/runner.go`
  * Pre/post hook lifecycle logs currently concatenate `Policy.PreHook` and `Policy.PostHook` into user-visible logs.
  * Hook failure logs and LastError values use `hookErr.Error()`.
  * Restore startup logs include raw source and target paths.
  * Restore failure paths write formatted error strings into `TaskRun.last_error`, emit logs, and raise alerts.
* `backend/internal/task/hook.go`
  * `runSSHHook` returns hook failure errors that include remote command output.
* `backend/internal/api/handlers/task_run_handler.go`
  * Task run detail and log endpoints return stored `TaskRun` and `TaskLog` fields directly.

## Recommended slice

Add a local task runtime evidence sanitizer that runs before task logs and targeted LastError strings are stored or published. Replace raw hook command lifecycle messages with generic messages and stop including raw hook output in hook errors. Keep executor inputs, policy storage, API schema, and WebSocket schema unchanged.

## Deferred follow-ups

* Full command executor stdout/stderr semantics may need a separate product decision because raw command output display may be an existing operator feature.
* restic/rclone/verifier output may need deeper executor-level summarization after the shared task log boundary is in place.
* Historical task log records are not rewritten in this slice.
