# Backend legacy log.Printf cleanup

## Goal

Reduce legacy standard-library `log.Printf` usage in backend runtime code by
converting a focused slice to structured `logger.Module` logging, preserving
the current operational behavior and API contracts.

This child task supports the parent slimming/refactor goal by narrowing an
older logging pattern in active runtime paths without changing request
responses, scheduling behavior, SSH connection behavior, or persistence.

## Confirmed Facts

- Backend specs require `logger.Module` for new structured backend logs.
- Early startup/config/database code still has documented standard-library log
  exceptions because it may run before logger initialization or during migration
  bootstrap.
- Runtime `log.Printf` calls remain in several packages. This child owns a
  focused slice only:
  - `backend/internal/api/handlers/node_handler.go`
  - `backend/internal/policy/sync.go`
- `node_handler.go` logs recoverable node connection-test failures, alert raise
  failures, alert resolve failures, and SSH key last-used update failures.
- `policy/sync.go` logs recoverable schedule sync/registration/resume failures.

## Requirements

- Convert selected `log.Printf` calls in the scoped files to structured
  zerolog events through `backend/internal/logger`.
- Preserve current control flow:
  - node connection tests still update node status, raise/resolve alerts, write
    credential audit entries, and return the same response bodies;
  - policy task sync still continues when scheduler sync/registration/resume
    fails, matching the existing best-effort behavior.
- Include stable fields needed for diagnosis, such as `node_id`, `task_id`, and
  `ssh_key_id`.
- Do not convert early-boot/config/database standard-library logging in this
  child.
- Do not change alerting, scheduler, SSH auth, database migration, or response
  helper contracts.
- Add a regression test that fails if the scoped runtime files reintroduce
  `log.Printf` or `log.Println`.

## Acceptance Criteria

- [ ] `backend/internal/api/handlers/node_handler.go` no longer imports `log`
      and contains no `log.Printf` / `log.Println` calls.
- [ ] `backend/internal/policy/sync.go` no longer imports `log` and contains no
      `log.Printf` / `log.Println` calls.
- [ ] Replacement logs use `logger.Module(...)` with structured IDs and
      `Err(err)` rather than formatted error strings.
- [ ] Behavior is unchanged for connection-test failure paths and policy task
      schedule best-effort failure paths.
- [ ] Focused backend tests and a static regression test pass.
- [ ] `cd backend && go test ./...` and `cd backend && go build ./...` pass, or
      any environment-only blocker is recorded.

## Notes

- This task intentionally reduces a small runtime slice instead of attempting a
  repository-wide logging migration.
