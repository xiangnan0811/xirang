# Directory Structure

> How backend code is organized in this project.

---

## Overview

The backend is a Go module under `backend/`. The HTTP entry point is
`backend/cmd/server/main.go`; most application code lives under
`backend/internal/` so packages are private to this service.

Use the current package boundaries before creating new ones. Thin REST
handlers live in `backend/internal/api/handlers/`, route registration lives in
`backend/internal/api/router.go`, persistent models live in
`backend/internal/model/models.go`, and cross-cutting middleware lives in
`backend/internal/middleware/`. Larger domains use their own package with a
small service API, for example `backend/internal/dashboards/service.go`,
`backend/internal/settings/service.go`, and `backend/internal/task/manager.go`.

---

## Directory Layout

```
backend/
├── cmd/server/                 # process bootstrap and worker wiring
├── internal/api/               # Gin router, Swagger docs, REST handlers
├── internal/auth/              # JWT, password, login lock, TOTP
├── internal/middleware/        # auth, RBAC, audit, metrics, request logging
├── internal/model/             # GORM models and model hooks
├── internal/database/          # DB open, GORM logger, migrations
├── internal/settings/          # DB -> env -> default settings registry
├── internal/task/              # scheduler, manager, runners, task state
├── internal/task/executor/     # rsync/restic/rclone/command executors
├── internal/alerting/          # alert dispatch, retry, grouping, silence
├── internal/dashboards/        # dashboard service and metric providers
├── internal/metrics/           # metric ingestion, aggregation, sinks
├── internal/nodelogs/          # remote log collection and retention
├── internal/secure/            # sensitive-field encryption helpers
├── internal/sshutil/           # SSH auth, host keys, probes, SFTP helpers
├── internal/util/              # small shared helpers
└── internal/ws/                # WebSocket hub
```

---

## Module Organization

- Add new REST endpoints in a resource-specific handler file under
  `backend/internal/api/handlers/`, then wire the handler in
  `backend/internal/api/router.go` with the required auth, RBAC, ownership, and
  rate/body-size middleware.
- Keep handlers as orchestration layers: parse params, bind JSON, call a domain
  service or a GORM query, then return through `response.go` helpers. For
  larger workflows, create or extend a domain package instead of growing a
  handler. Existing examples: `dashboards.Service`, `settings.Service`, and
  `task.Manager`.
- Put GORM structs and model hooks in `backend/internal/model/`. Sensitive
  field encryption belongs in model hooks using `backend/internal/secure/`, not
  in handlers.
- Put database-opening behavior and migrations in `backend/internal/database/`.
  Every schema change must include both SQLite and PostgreSQL migrations.
- Put SSH-specific logic in `backend/internal/sshutil/` or
  `backend/internal/task/executor/`; avoid duplicating connection/auth parsing
  in unrelated packages.

---

## Naming Conventions

- Package directories are lowercase, usually one word (`settings`, `alerting`,
  `nodelogs`) unless the existing package already uses a compound.
- Handler files are resource oriented, such as `node_handler.go`,
  `settings_handler.go`, and `snapshot_diff_handler.go`.
- Tests are colocated as `*_test.go`. Use export-only test helpers sparingly and
  prefer package-private tests when possible.
- Migration files use monotonically increasing numeric prefixes and paired
  `up`/`down` files, for example
  `000047_alert_deliveries_drop_error.up.sql`.
- JSON fields use snake_case to match the API and database conventions, while
  Go exported fields stay PascalCase.

---

## Examples

- `backend/internal/api/router.go` shows the standard route layout: public auth
  routes first, then `secured := v1.Group("")` with `AuthMiddleware`,
  `AuditLogger`, API rate limiting, and per-route RBAC.
- `backend/internal/api/handlers/dashboard_handler.go` is the preferred thin
  handler pattern: parse IDs with `parseID`, bind payloads, call
  `dashboards.Service`, and map sentinel errors to response helpers.
- `backend/internal/dashboards/service.go` shows the domain-service pattern for
  validation, ownership scoping, transactions, and sentinel errors.
- `backend/internal/settings/service.go` is the example for registry-style
  configuration: one in-code registry, validation helpers, cache invalidation,
  and DB/env/default resolution.
- `backend/internal/model/models.go` shows current GORM model placement,
  `json`/`gorm` tags, `Sanitized()` response helpers, and sensitive-field
  `BeforeSave`/`AfterFind` hooks.
