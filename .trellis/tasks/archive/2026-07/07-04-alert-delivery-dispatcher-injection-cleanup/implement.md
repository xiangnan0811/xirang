# Alert Delivery Dispatcher Injection Cleanup Implementation Plan

## Checklist

1. Load backend and shared Trellis specs before editing.
2. Write a failing static regression test that scans:
   - `internal/integration/service.go`
   - `internal/api/handlers/alert_handler.go`
   and rejects direct package-level delivery shim calls.
3. Run the focused static test to confirm it fails against current code.
4. Implement dispatcher injection:
   - add optional dispatcher field and `WithAlertDispatcher` to
     `IntegrationService`;
   - add optional dispatcher field and `WithAlertDispatcher` to `AlertHandler`;
   - use dispatcher methods for `SendProbe` / `SendAlert`;
   - wire `dep.AlertDispatcher` in `api.NewRouter`.
5. Run `gofmt` on edited Go files.
6. Run focused tests:
   - `cd backend && go test ./internal`
   - `cd backend && go test ./internal/integration ./internal/api/handlers ./internal/api`
7. Run full backend verification:
   - `cd backend && go test ./...`
   - `cd backend && go build ./...`
   - source search for direct shim calls in scoped files
   - `git diff --check`
8. Review spec updates, commit, archive, and journal.

## Validation Commands

```bash
cd backend && go test ./internal
cd backend && go test ./internal/integration ./internal/api/handlers ./internal/api
cd backend && go test ./...
cd backend && go build ./...
rg -n 'alerting\\.Send(Alert|Probe)\\(' backend/internal/integration/service.go backend/internal/api/handlers/alert_handler.go
git diff --check
python3 ./.trellis/scripts/task.py validate .trellis/tasks/07-04-alert-delivery-dispatcher-injection-cleanup
```

## Risk Notes

- Keep fallback constructors for existing tests and call sites.
- Do not widen this task into reporting delivery or alert raise shim removal.
- Sanitized error behavior must remain unchanged.
