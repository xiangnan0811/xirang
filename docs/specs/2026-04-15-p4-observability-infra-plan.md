# P4: Observability & Infrastructure — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Prometheus full-stack metrics, missing DB performance indexes, Makefile DX targets, and OpenAPI/Swagger documentation to complete the Xirang improvement plan.

**Architecture:** Four independent areas in one PR. Prometheus metrics via middleware + GORM plugin + business counters. DB indexes via migration 000032. Makefile adds lint/clean/coverage/check/swag-init. OpenAPI via swaggo annotations on all handlers + Swagger UI at /swagger/.

**Tech Stack:** Go 1.26 (Gin, GORM, prometheus/client_golang, swaggo/swag), SQLite/PostgreSQL migrations

---

### Task 1: Prometheus HTTP metrics middleware + /metrics endpoint

**Files:**
- Create: `backend/internal/middleware/metrics.go`
- Modify: `backend/internal/api/router.go` (add middleware + /metrics endpoint)
- Modify: `backend/go.mod` (add prometheus dependency)

- [ ] **Step 1: Add prometheus dependency**

Run: `cd backend && go get github.com/prometheus/client_golang/prometheus github.com/prometheus/client_golang/prometheus/promhttp`

- [ ] **Step 2: Create metrics middleware**

Create `backend/internal/middleware/metrics.go`:

```go
package middleware

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total number of HTTP requests",
	}, []string{"method", "path", "status"})

	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request duration in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})

	httpResponseSize = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_response_size_bytes",
		Help:    "HTTP response size in bytes",
		Buckets: prometheus.ExponentialBuckets(100, 10, 7), // 100B to 100MB
	}, []string{"method", "path"})

	httpRequestsInFlight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "http_requests_in_flight",
		Help: "Number of HTTP requests currently being processed",
	})
)

// PrometheusMetrics returns a Gin middleware that records HTTP metrics.
func PrometheusMetrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.FullPath()
		if path == "" {
			path = "unknown"
		}
		method := c.Request.Method

		httpRequestsInFlight.Inc()
		start := time.Now()

		c.Next()

		httpRequestsInFlight.Dec()
		status := strconv.Itoa(c.Writer.Status())
		duration := time.Since(start).Seconds()
		size := float64(c.Writer.Size())

		httpRequestsTotal.WithLabelValues(method, path, status).Inc()
		httpRequestDuration.WithLabelValues(method, path).Observe(duration)
		if size > 0 {
			httpResponseSize.WithLabelValues(method, path).Observe(size)
		}
	}
}
```

- [ ] **Step 3: Add middleware and /metrics endpoint to router**

In `backend/internal/api/router.go`, add:

1. Import `"github.com/prometheus/client_golang/prometheus/promhttp"` at the top
2. After the `router.Use(gin.Recovery(), middleware.RequestID(), middleware.StructuredLogger())` line, add:
```go
router.Use(middleware.PrometheusMetrics())
```
3. Before the `/healthz` endpoint (around line 245), add:
```go
router.GET("/metrics", gin.WrapH(promhttp.Handler()))
```

- [ ] **Step 4: Verify**

Run: `cd backend && go build ./... && go test ./... -count=1`
Expected: Clean build, all tests pass.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/middleware/metrics.go backend/internal/api/router.go backend/go.mod backend/go.sum
git commit -m "$(cat <<'EOF'
feat(backend): add Prometheus HTTP metrics middleware

Expose /metrics endpoint with http_requests_total, 
http_request_duration_seconds, http_response_size_bytes,
http_requests_in_flight. Go runtime metrics included by default.
EOF
)"
```

---

### Task 2: GORM metrics plugin + business metrics

**Files:**
- Create: `backend/internal/database/metrics.go`
- Modify: `backend/internal/database/database.go` (register plugin)
- Modify: `backend/internal/task/runner.go` (business metrics)
- Modify: `backend/internal/alerting/dispatcher.go` (alert counter)

- [ ] **Step 1: Create GORM metrics plugin**

Create `backend/internal/database/metrics.go`:

```go
package database

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"gorm.io/gorm"
)

var dbQueryDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "db_query_duration_seconds",
	Help:    "Database query duration in seconds",
	Buckets: prometheus.DefBuckets,
}, []string{"operation"})

// RegisterMetricsCallbacks adds Prometheus timing callbacks to GORM operations.
func RegisterMetricsCallbacks(db *gorm.DB) {
	for _, op := range []string{"create", "query", "update", "delete", "raw"} {
		callbackName := "metrics:" + op
		switch op {
		case "create":
			db.Callback().Create().Before("gorm:create").Register(callbackName+":before", setStartTime)
			db.Callback().Create().After("gorm:create").Register(callbackName+":after", recordDuration("create"))
		case "query":
			db.Callback().Query().Before("gorm:query").Register(callbackName+":before", setStartTime)
			db.Callback().Query().After("gorm:query").Register(callbackName+":after", recordDuration("query"))
		case "update":
			db.Callback().Update().Before("gorm:update").Register(callbackName+":before", setStartTime)
			db.Callback().Update().After("gorm:update").Register(callbackName+":after", recordDuration("update"))
		case "delete":
			db.Callback().Delete().Before("gorm:delete").Register(callbackName+":before", setStartTime)
			db.Callback().Delete().After("gorm:delete").Register(callbackName+":after", recordDuration("delete"))
		case "raw":
			db.Callback().Raw().Before("gorm:raw").Register(callbackName+":before", setStartTime)
			db.Callback().Raw().After("gorm:raw").Register(callbackName+":after", recordDuration("raw"))
		}
	}
}

const metricsStartTimeKey = "metrics:start_time"

func setStartTime(db *gorm.DB) {
	db.Set(metricsStartTimeKey, time.Now())
}

func recordDuration(operation string) func(*gorm.DB) {
	return func(db *gorm.DB) {
		if start, ok := db.Get(metricsStartTimeKey); ok {
			if startTime, ok := start.(time.Time); ok {
				dbQueryDuration.WithLabelValues(operation).Observe(time.Since(startTime).Seconds())
			}
		}
	}
}
```

- [ ] **Step 2: Register GORM metrics in database initialization**

In `backend/internal/database/database.go`, after the DB is opened and configured, add:
```go
RegisterMetricsCallbacks(db)
```

Find where `db` is returned (after AutoMigrate or similar setup) and add the call there.

- [ ] **Step 3: Add business metrics to task runner**

In `backend/internal/task/runner.go`, add near the top (imports section):
```go
import "github.com/prometheus/client_golang/prometheus/promauto"

var (
	tasksActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "xirang_tasks_active",
		Help: "Number of currently running tasks",
	})
	backupLastSuccess = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "xirang_backup_last_success_timestamp",
		Help: "Unix timestamp of last successful backup per task",
	}, []string{"task_name"})
)
```

Then in the task execution function:
- Call `tasksActive.Inc()` when a task starts running
- Call `tasksActive.Dec()` when a task finishes (success or failure)
- On successful backup completion, call `backupLastSuccess.WithLabelValues(task.Name).SetToCurrentTime()`

Search for where task status is set to "running" and "success/failed" to find the right insertion points.

- [ ] **Step 4: Add alert counter to dispatcher**

In `backend/internal/alerting/dispatcher.go`, add near the top:
```go
var alertsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "xirang_alerts_total",
	Help: "Total alerts raised by severity",
}, []string{"severity"})
```

In the alert raise functions (`RaiseTaskFailure`, `RaiseVerificationFailure`, etc.), add:
```go
alertsTotal.WithLabelValues(alert.Severity).Inc()
```

Search for where alerts are created/inserted to find the right insertion point.

- [ ] **Step 5: Verify**

Run: `cd backend && go build ./... && go test ./... -count=1`

- [ ] **Step 6: Commit**

```bash
git add backend/internal/database/metrics.go backend/internal/database/database.go backend/internal/task/runner.go backend/internal/alerting/dispatcher.go backend/go.mod backend/go.sum
git commit -m "$(cat <<'EOF'
feat(backend): add GORM query metrics and business metrics

GORM plugin tracks db_query_duration_seconds by operation.
Business metrics: xirang_tasks_active gauge, 
xirang_backup_last_success_timestamp per task,
xirang_alerts_total by severity.
EOF
)"
```

---

### Task 3: Database performance indexes

**Files:**
- Create: `backend/internal/database/migrations/sqlite/000032_performance_indexes.up.sql`
- Create: `backend/internal/database/migrations/sqlite/000032_performance_indexes.down.sql`
- Create: `backend/internal/database/migrations/postgres/000032_performance_indexes.up.sql`
- Create: `backend/internal/database/migrations/postgres/000032_performance_indexes.down.sql`

- [ ] **Step 1: Create SQLite up migration**

```sql
-- 000032_performance_indexes.up.sql
CREATE INDEX IF NOT EXISTS idx_task_runs_node_id ON task_runs(node_id);
CREATE INDEX IF NOT EXISTS idx_task_runs_node_created ON task_runs(node_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_nodes_created_at ON nodes(created_at DESC);
```

- [ ] **Step 2: Create SQLite down migration**

```sql
-- 000032_performance_indexes.down.sql
DROP INDEX IF EXISTS idx_task_runs_node_id;
DROP INDEX IF EXISTS idx_task_runs_node_created;
DROP INDEX IF EXISTS idx_nodes_created_at;
```

- [ ] **Step 3: Create PostgreSQL migrations** (same SQL)

Copy the same content to the postgres directory.

- [ ] **Step 4: Verify migration applies**

Run: `cd backend && go test ./internal/database/... -count=1`
Expected: Database tests pass (migrations apply cleanly).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/database/migrations/
git commit -m "$(cat <<'EOF'
feat(backend): add performance indexes (migration 000032)

Index task_runs.node_id, task_runs(node_id, created_at DESC),
and nodes.created_at for faster list/sort queries.
EOF
)"
```

---

### Task 4: Makefile DX targets

**Files:**
- Modify: `Makefile`

- [ ] **Step 1: Add new targets to Makefile**

Append these targets after the existing `setup-hooks` target and before the Docker section:

```makefile
# ── Quality & Testing ──
.PHONY: lint lint-backend lint-frontend coverage coverage-backend coverage-frontend check test build clean swag-init

lint: lint-backend lint-frontend ## Run all linters

lint-backend: ## Run golangci-lint
	cd backend && golangci-lint run ./...

lint-frontend: ## Run ESLint
	cd web && npm run lint

coverage: coverage-backend coverage-frontend ## Generate coverage reports

coverage-backend: ## Run backend tests with coverage
	cd backend && go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out

coverage-frontend: ## Run frontend tests with coverage
	cd web && npx vitest run --coverage

check: lint test build ## Full pre-commit quality gate

test: backend-test web-test ## Run all tests

build: backend-build web-build ## Build all

clean: ## Remove build artifacts
	rm -rf backend/xirang-server backend/coverage.out web/dist web/coverage
```

Also update the `.PHONY` line at the top to include the new targets.

- [ ] **Step 2: Verify targets work**

Run: `make lint 2>&1 | tail -5` (should show lint results)
Run: `make clean` (should succeed silently)

- [ ] **Step 3: Commit**

```bash
git add Makefile
git commit -m "$(cat <<'EOF'
chore: add Makefile DX targets

New targets: lint, lint-backend, lint-frontend, coverage,
coverage-backend, coverage-frontend, check, test, build, clean.
EOF
)"
```

---

### Task 5: OpenAPI setup — dependencies, general info, Swagger UI route

**Files:**
- Modify: `backend/go.mod` (add swaggo dependencies)
- Modify: `backend/cmd/server/main.go` (add general API info annotation)
- Modify: `backend/internal/api/router.go` (add swagger UI route)

- [ ] **Step 1: Add swaggo dependencies**

Run: `cd backend && go get github.com/swaggo/swag github.com/swaggo/gin-swagger github.com/swaggo/files`

- [ ] **Step 2: Add general API info to main.go**

Add these comments ABOVE the `func main()` declaration in `backend/cmd/server/main.go`:

```go
// @title           Xirang API
// @version         1.0
// @description     息壤 — 服务器运维管理平台 API
// @host            localhost:8080
// @BasePath        /api/v1
// @securityDefinitions.apikey Bearer
// @in header
// @name Authorization
// @description JWT Bearer token (格式: Bearer <token>)
```

- [ ] **Step 3: Install swag CLI and generate initial docs**

Run:
```bash
cd backend && go install github.com/swaggo/swag/cmd/swag@latest
swag init -g cmd/server/main.go -o internal/api/docs --parseDependency
```

This creates `backend/internal/api/docs/` with `docs.go`, `swagger.json`, `swagger.yaml`.

- [ ] **Step 4: Add swagger.json and swagger.yaml to .gitignore**

Add to root `.gitignore`:
```
backend/internal/api/docs/swagger.json
backend/internal/api/docs/swagger.yaml
```

Keep `docs.go` tracked (it's the Go embed source).

- [ ] **Step 5: Add Swagger UI route to router.go**

In `backend/internal/api/router.go`, add imports:
```go
_ "xirang/backend/internal/api/docs"
ginSwagger "github.com/swaggo/gin-swagger"
swaggerFiles "github.com/swaggo/files"
```

Add route near the `/healthz` and `/metrics` endpoints:
```go
router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
```

- [ ] **Step 6: Verify**

Run: `cd backend && go build ./... && go test ./... -count=1`

- [ ] **Step 7: Commit**

```bash
git add backend/go.mod backend/go.sum backend/cmd/server/main.go backend/internal/api/router.go backend/internal/api/docs/docs.go .gitignore
git commit -m "$(cat <<'EOF'
feat(backend): add OpenAPI/Swagger UI setup

swaggo/swag for annotation-driven spec generation.
Swagger UI served at /swagger/. Initial spec with general
API info only — handler annotations follow in next task.
EOF
)"
```

---

### Task 6: OpenAPI handler annotations — auth, users, nodes

**Files:**
- Modify: `backend/internal/api/handlers/auth_handler.go`
- Modify: `backend/internal/api/handlers/user_handler.go`
- Modify: `backend/internal/api/handlers/node_handler.go`

- [ ] **Step 1: Add swagger annotations to auth_handler.go**

Add annotations above each handler method. Pattern for each:
```go
// Login godoc
// @Summary      User login
// @Description  Authenticate with username and password
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body  body  object{username=string,password=string,captcha_id=string,captcha_answer=string}  true  "Login credentials"
// @Success      200  {object}  handlers.Response{data=object{token=string,user=object}}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /auth/login [post]
```

Add similar annotations for: Login, Verify2FA, GetCaptcha, Logout, ChangePassword, CompleteOnboarding, GetCurrentUser.

- [ ] **Step 2: Add swagger annotations to user_handler.go**

Annotate: ListUsers, CreateUser, UpdateUser, DeleteUser.

- [ ] **Step 3: Add swagger annotations to node_handler.go**

Annotate all methods: List, Get, Create, Update, Delete, DeleteBatch, TestConnection, TriggerBackup, Exec, ListOwners, AddOwner, RemoveOwner, and any other public methods.

- [ ] **Step 4: Regenerate docs**

Run: `cd backend && swag init -g cmd/server/main.go -o internal/api/docs --parseDependency`

- [ ] **Step 5: Verify**

Run: `cd backend && go build ./...`

- [ ] **Step 6: Commit**

```bash
git add -u backend/internal/api/handlers/ backend/internal/api/docs/docs.go
git commit -m "$(cat <<'EOF'
docs(backend): add OpenAPI annotations for auth/users/nodes
EOF
)"
```

---

### Task 7: OpenAPI handler annotations — tasks, policies, alerts, integrations

**Files:**
- Modify: `backend/internal/api/handlers/task_handler.go`
- Modify: `backend/internal/api/handlers/task_run_handler.go`
- Modify: `backend/internal/api/handlers/policy_handler.go`
- Modify: `backend/internal/api/handlers/alert_handler.go`
- Modify: `backend/internal/api/handlers/integration_handler.go`

- [ ] **Step 1: Annotate task_handler.go**

Annotate: List, Get, Create, Update, Delete, Trigger, Cancel, Retry, Pause, Resume, SkipNext, Logs, BatchTrigger, Restore.

- [ ] **Step 2: Annotate task_run_handler.go**

Annotate: ListByTask, Get, Logs.

- [ ] **Step 3: Annotate policy_handler.go**

Annotate: List, Get, Create, Update, Delete, CloneFromTemplate, BatchToggle.

- [ ] **Step 4: Annotate alert_handler.go**

Annotate: List, Get, Acknowledge, Resolve, Retry, ListDeliveries, RetryDelivery, RetryFailedDeliveries, DeliveryStats.

- [ ] **Step 5: Annotate integration_handler.go**

Annotate: List, Get, Create, Update, Delete, Toggle, Test.

- [ ] **Step 6: Regenerate docs**

Run: `cd backend && swag init -g cmd/server/main.go -o internal/api/docs --parseDependency`

- [ ] **Step 7: Verify**

Run: `cd backend && go build ./...`

- [ ] **Step 8: Commit**

```bash
git add -u backend/internal/api/handlers/ backend/internal/api/docs/docs.go
git commit -m "$(cat <<'EOF'
docs(backend): add OpenAPI annotations for tasks/policies/alerts/integrations
EOF
)"
```

---

### Task 8: OpenAPI handler annotations — remaining handlers

**Files to annotate:**
- `ssh_key_handler.go` — List, Get, Create, Update, Delete, BatchImport, Rotate, Export
- `settings_handler.go` — List, Get, Update, Reset
- `config_handler.go` — Export, Import
- `report_handler.go` — ListConfigs, CreateConfig, UpdateConfig, DeleteConfig, Generate, ListReports, GetReport
- `overview_handler.go`, `overview_storage_handler.go`, `overview_traffic_handler.go`, `overview_backup_health_handler.go`
- `system_handler.go` — Backup, Restore, Integrity
- `audit_handler.go` — List
- `batch_handler.go` — Create, Get, Delete
- `version_handler.go`, `docker_handler.go`, `file_handler.go`
- `snapshot_handler.go`, `snapshot_diff_handler.go`
- `storage_guide_handler.go`, `node_migrate_handler.go`, `node_migrate_preflight_handler.go`
- `hook_templates_handler.go`, `captcha_handler.go`

- [ ] **Step 1: Annotate all remaining handlers** following the same pattern
- [ ] **Step 2: Regenerate docs**

Run: `cd backend && swag init -g cmd/server/main.go -o internal/api/docs --parseDependency`

- [ ] **Step 3: Verify**

Run: `cd backend && go build ./... && go test ./... -count=1`

- [ ] **Step 4: Add swag-init target to Makefile**

Append to the Makefile (in the Quality section):
```makefile
swag-init: ## Regenerate OpenAPI spec
	cd backend && swag init -g cmd/server/main.go -o internal/api/docs --parseDependency
```

Update `.PHONY` to include `swag-init`.

- [ ] **Step 5: Commit**

```bash
git add -u backend/internal/api/handlers/ backend/internal/api/docs/docs.go Makefile
git commit -m "$(cat <<'EOF'
docs(backend): complete OpenAPI annotations for all handlers

All API endpoints now have swagger annotations. Swagger UI
available at /swagger/. Added make swag-init target.
EOF
)"
```

---

### Task 9: Final verification

- [ ] **Step 1: Run full backend suite**

Run: `cd backend && go test ./... -count=1`
Expected: All tests pass.

- [ ] **Step 2: Run backend lint**

Run: `cd backend && golangci-lint run ./...`
Expected: 0 issues.

- [ ] **Step 3: Run full frontend check**

Run: `cd web && npm run check`
Expected: All pass.

- [ ] **Step 4: Test Makefile targets**

Run: `make check 2>&1 | tail -10`
Expected: lint + test + build all succeed.

- [ ] **Step 5: Verify /metrics endpoint**

Run: `cd backend && go run ./cmd/server &` (start server briefly)
Run: `curl -s localhost:8080/metrics | head -20`
Expected: Prometheus metrics output with `http_requests_total`, `db_query_duration_seconds`, etc.
Kill the server.

- [ ] **Step 6: Verify /swagger/ endpoint**

Run: `curl -s localhost:8080/swagger/index.html | head -5`
Expected: HTML page (Swagger UI).

- [ ] **Step 7: Review git log**

Run: `git log --oneline` — verify clean commits.
