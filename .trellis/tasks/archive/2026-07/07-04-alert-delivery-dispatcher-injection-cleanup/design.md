# Alert Delivery Dispatcher Injection Cleanup Design

## Scope

This task owns only API/integration delivery paths:

- `backend/internal/integration/service.go`
- `backend/internal/api/handlers/alert_handler.go`
- `backend/internal/api/router.go`

It does not change reporting delivery, alert raising, alert grouping, silence
matching, `DefaultRaiser`, package-level alerting shim function definitions, or
alert delivery worker behavior.

## Dependency Shape

`IntegrationService` and `AlertHandler` will both accept an optional
`*alerting.Dispatcher`:

```go
func (s *IntegrationService) WithAlertDispatcher(d *alerting.Dispatcher) *IntegrationService
func (h *AlertHandler) WithAlertDispatcher(d *alerting.Dispatcher) *AlertHandler
```

Each component gets a small unexported helper:

```go
func (s *IntegrationService) getAlertDispatcher() *alerting.Dispatcher
func (h *AlertHandler) getAlertDispatcher() *alerting.Dispatcher
```

The helper returns the injected dispatcher when present and otherwise creates
`alerting.NewDispatcher(db, nil, nil)`. This preserves existing test and
constructor behavior while avoiding package-level delivery shims in the scoped
runtime paths.

`api.NewRouter` already receives `dep.AlertDispatcher`, so router wiring becomes:

```go
integrationSvc := integration.NewIntegrationService(dep.DB).WithAlertDispatcher(dep.AlertDispatcher)
alertHandler := handlers.NewAlertHandler(dep.DB).WithAlertDispatcher(dep.AlertDispatcher)
```

## Behavior Preservation

`IntegrationService.TestIntegration` should continue to:

- load the integration by ID;
- time the probe;
- return success/failure result structs;
- sanitize failed probe errors with `util.SanitizeError`.

`AlertHandler.RetryDelivery` and `RetryFailedDeliveries` should continue to:

- authorize the alert;
- load integrations;
- send retry attempts;
- persist `AlertDelivery` rows;
- sanitize delivery errors with `util.SanitizeDeliveryError`;
- return the same response envelope payloads.

## Tests

Add a static test for the scoped files to reject:

- `alerting.SendProbe(`
- `alerting.SendAlert(`

Existing handler/integration tests should cover response behavior. Focused
tests plus full backend test/build gates cover compile and behavior drift.

## Rollback

Rollback is a single backend commit revert. No schema, API, migration, or config
changes are involved.
