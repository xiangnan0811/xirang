# Backend Legacy log.Printf Cleanup Implementation Plan

## Checklist

1. Load backend and shared Trellis specs before editing.
2. Write a failing static regression test that scans:
   - `internal/api/handlers/node_handler.go`
   - `internal/policy/sync.go`
   and rejects `log.Printf` / `log.Println`.
3. Run the focused static test to confirm it fails against current code.
4. Convert scoped runtime logs:
   - remove `log` imports from both files;
   - add `backend/internal/logger`;
   - replace each legacy log with `logger.Module(...).Warn()/Debug().<fields>.Err(err).Msg(...)`.
5. Run `gofmt` on edited Go files.
6. Run focused tests:
   - `cd backend && go test ./internal/...` for affected packages when feasible;
   - at minimum, `go test ./internal/api/handlers ./internal/policy ./internal`.
7. Run full backend verification:
   - `cd backend && go test ./...`
   - `cd backend && go build ./...`
   - `git diff --check`
8. Review whether the backend logging spec needs an update; commit, archive,
   and journal the child task.

## Validation Commands

```bash
cd backend && go test ./internal
cd backend && go test ./internal/api/handlers ./internal/policy
cd backend && go test ./...
cd backend && go build ./...
rg -n 'log\\.(Printf|Println)' backend/internal/api/handlers/node_handler.go backend/internal/policy/sync.go
git diff --check
python3 ./.trellis/scripts/task.py validate .trellis/tasks/07-04-backend-log-printf-cleanup
```

## Risk Notes

- Static tests should scan only the scoped files so documented early-startup
  exceptions remain untouched.
- Do not change best-effort scheduler or alerting behavior while replacing log
  statements.
