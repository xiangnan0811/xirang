# Backend Legacy log.Printf Cleanup Design

## Scope

This child converts legacy standard-library logging in two runtime files:

- `backend/internal/api/handlers/node_handler.go`
- `backend/internal/policy/sync.go`

It does not touch early startup, config loading, database migration, bootstrap,
terminal streaming, reporting, or SSH utility logging. Those are separate
runtime contexts and need their own slices if converted later.

## Logging Contracts

Use the existing structured logger:

```go
logger.Module("api").Warn().Uint("node_id", node.ID).Err(err).Msg("...")
logger.Module("policy").Warn().Uint("task_id", task.ID).Err(err).Msg("...")
```

Node handler replacement logs should keep:

- `node_id` for node status save, alert raise, alert resolve, and SSH test
  failure logs.
- `ssh_key_id` for SSH key last-used update failures.
- `Err(err)` for the raw error value without formatting it into a string.

Policy sync replacement logs should keep:

- `task_id` for schedule sync, registration, and resume failures.
- `Err(err)` for scheduler errors.

## Behavior Preservation

Every converted log call currently sits in a best-effort path. The code should
continue after logging, and returned errors/responses must not change.

For node connection tests, conversion must not change:

- node status save attempts;
- alert raise/resolve attempts;
- credential audit writes;
- `respondOK` bodies for failed connection tests;
- SSH dial/session behavior.

For policy sync, conversion must not change:

- task updates/creates/orphaning;
- scheduler best-effort sync/register/resume semantics;
- returned errors from database writes.

## Tests

Add a static Go test under `backend/internal/logging_test.go` or another
appropriate package-level test that reads the scoped files and rejects
`log.Printf` / `log.Println`. This pins the cleanup without trying to assert
zerolog output bytes.

Existing handler/policy tests cover behavior; focused package tests plus full
backend test/build gate cover regressions.

## Rollback

Rollback is a single commit revert. No schema, API, migration, or config
contract changes are involved.
