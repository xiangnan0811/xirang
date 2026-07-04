# Alert delivery dispatcher injection cleanup

## Goal

Narrow alerting package-level delivery shims by routing API retry/probe delivery
through explicit `*alerting.Dispatcher` dependencies instead of direct
`alerting.SendAlert` / `alerting.SendProbe` calls.

This child supports the parent slimming/refactor goal by reducing package-global
alerting coupling in active backend paths while preserving delivery behavior and
API response contracts.

## Confirmed Facts

- `api.Dependencies` already carries `AlertDispatcher *alerting.Dispatcher`.
- `NodeHandler` already follows the local pattern of accepting an optional
  alert dispatcher and falling back to a local dispatcher when unset.
- `IntegrationService.TestIntegration` currently calls the package-level
  `alerting.SendProbe`.
- `AlertHandler.RetryDelivery` and `RetryFailedDeliveries` currently call the
  package-level `alerting.SendAlert`.
- Reporting delivery and `DefaultRaiser` remain separate flows and are not part
  of this child.

## Requirements

- Add dispatcher injection to `integration.IntegrationService`.
- Add dispatcher injection to `handlers.AlertHandler`.
- Wire `dep.AlertDispatcher` from `api.NewRouter` into both components.
- Preserve existing constructor compatibility for tests and older call sites by
  falling back to `alerting.NewDispatcher(db, nil, nil)` when no dispatcher is
  injected.
- Replace scoped direct `alerting.SendProbe` / `alerting.SendAlert` calls with
  dispatcher methods.
- Preserve all API response bodies, status codes, delivery record persistence,
  and sanitized error behavior.
- Add a static regression test that fails if the scoped files reintroduce direct
  package-level delivery shim calls.
- Do not remove package-level alerting shim functions in this child; other live
  callers still exist.

## Acceptance Criteria

- [ ] `backend/internal/integration/service.go` does not call
      `alerting.SendProbe` directly.
- [ ] `backend/internal/api/handlers/alert_handler.go` does not call
      `alerting.SendAlert` directly.
- [ ] `api.NewRouter` passes `dep.AlertDispatcher` into the integration service
      and alert handler.
- [ ] Existing alert retry and integration probe behavior remains unchanged.
- [ ] Focused tests and full backend `go test ./...` / `go build ./...` pass,
      or any environment-only blocker is recorded.

## Notes

- This is a dependency-boundary cleanup, not a delivery behavior rewrite.
