# P4: Observability & Infrastructure

Final phase of the Xirang improvement plan. Adds Prometheus metrics, performance indexes, Makefile DX, and OpenAPI documentation.

## Scope

One PR with 4 independent areas:

1. **Prometheus metrics** — full observability stack (HTTP, DB, business, Go runtime)
2. **Database indexes** — missing performance indexes for high-frequency queries
3. **Makefile DX** — lint, clean, coverage, check targets
4. **OpenAPI/Swagger** — swaggo annotations + Swagger UI

## Part 1: Prometheus Metrics

### Dependencies

Add to `go.mod`:
- `github.com/prometheus/client_golang`

### HTTP Metrics Middleware

New file: `backend/internal/middleware/metrics.go`

Metrics collected:
- `http_requests_total{method, path, status}` — counter
- `http_request_duration_seconds{method, path}` — histogram
- `http_response_size_bytes{method, path}` — histogram
- `http_requests_in_flight` — gauge

The middleware wraps all routes in the `secured` and `public` groups. The `path` label uses `c.FullPath()` (route pattern, not actual URL) to prevent label cardinality explosion.

### GORM Metrics Plugin

New file: `backend/internal/database/metrics.go`

Register a GORM callback plugin that tracks:
- `db_query_duration_seconds{operation}` — histogram (operation = create/query/update/delete/raw)

### Business Metrics

Registered in their respective packages:

- `xirang_tasks_active` gauge — set by task manager when tasks start/stop
- `xirang_backup_last_success_timestamp{task_name}` gauge — set on successful backup completion
- `xirang_alerts_total{severity}` counter — incremented when alerts are raised

### Go Runtime Metrics

`promhttp.Handler()` automatically exposes:
- Goroutine count, GC stats, memory allocation, CPU usage
- No additional code needed — included by default

### Endpoint

`GET /metrics` — registered on the main router OUTSIDE `/api/v1`:
```go
router.GET("/metrics", gin.WrapH(promhttp.Handler()))
```

No authentication required (intended for internal Prometheus scraping). Can be restricted by network policy.

## Part 2: Database Indexes

### New migration: `000032_performance_indexes`

**SQLite up:**
```sql
CREATE INDEX IF NOT EXISTS idx_task_runs_node_id ON task_runs(node_id);
CREATE INDEX IF NOT EXISTS idx_task_runs_node_created ON task_runs(node_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_nodes_created_at ON nodes(created_at DESC);
```

**SQLite down:**
```sql
DROP INDEX IF EXISTS idx_task_runs_node_id;
DROP INDEX IF EXISTS idx_task_runs_node_created;
DROP INDEX IF EXISTS idx_nodes_created_at;
```

**PostgreSQL**: Same SQL (both dialects support this syntax).

### Rationale

- `task_runs.node_id` — used in task run list queries filtered by node, currently does full table scan
- `task_runs(node_id, created_at DESC)` — composite for "recent runs for this node" queries
- `nodes.created_at DESC` — used in node list sorting

All other high-frequency columns are already indexed (46 existing indexes verified).

## Part 3: Makefile DX

Add these targets to the existing Makefile:

```makefile
## Quality & Testing
lint: lint-backend lint-frontend          ## Run all linters
lint-backend:                             ## Run golangci-lint
    cd backend && golangci-lint run ./...
lint-frontend:                            ## Run ESLint
    cd web && npm run lint

coverage: coverage-backend coverage-frontend  ## Generate coverage reports
coverage-backend:
    cd backend && go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out
coverage-frontend:
    cd web && npx vitest run --coverage

check: lint test build                    ## Full pre-commit gate
test: backend-test web-test               ## Run all tests
build: backend-build web-build            ## Build all

## Cleanup
clean:                                    ## Remove build artifacts
    rm -rf backend/xirang backend/coverage.out web/dist web/coverage

## OpenAPI
swag-init:                                ## Regenerate OpenAPI spec
    cd backend && swag init -g cmd/server/main.go -o internal/api/docs --parseDependency
```

## Part 4: OpenAPI with swaggo/swag

### Dependencies

Add to `go.mod`:
- `github.com/swaggo/swag`
- `github.com/swaggo/gin-swagger`
- `github.com/swaggo/files`

### General API Info

Add to `backend/cmd/server/main.go`:
```go
// @title           Xirang API
// @version         1.0
// @description     Server operations management platform API
// @host            localhost:8080
// @BasePath        /api/v1
// @securityDefinitions.apikey Bearer
// @in header
// @name Authorization
// @description JWT Bearer token
```

### Handler Annotations

Add swagger annotations to each handler. Example pattern:
```go
// List godoc
// @Summary      List all nodes
// @Description  Returns all server nodes with optional filtering
// @Tags         nodes
// @Security     Bearer
// @Produce      json
// @Param        page       query  int     false  "Page number"
// @Param        page_size  query  int     false  "Page size"
// @Success      200  {object}  handlers.PaginatedResponse{data=[]model.Node}
// @Failure      401  {object}  handlers.Response
// @Failure      500  {object}  handlers.Response
// @Router       /nodes [get]
func (h *NodeHandler) List(c *gin.Context) {
```

### Swagger UI Route

In `router.go`:
```go
import (
    _ "xirang/backend/internal/api/docs" // swagger generated docs
    ginSwagger "github.com/swaggo/gin-swagger"
    swaggerFiles "github.com/swaggo/files"
)

router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
```

### Annotation Scope

Annotate all handler route groups:
- auth (login, 2FA, captcha, logout, password change)
- users (CRUD)
- nodes (CRUD + test connection, backup, metrics, owners)
- policies (CRUD + toggle, schedule)
- tasks (CRUD + trigger, cancel, retry, pause, logs)
- task_runs (list, get, logs)
- alerts (list, get, ack, resolve, retry, deliveries)
- integrations (CRUD + toggle, test)
- ssh_keys (CRUD + rotation, batch import)
- settings, system, reports, config, overview endpoints

### Generated Files

`swag init` generates `backend/internal/api/docs/` with:
- `docs.go` — Go source with embedded spec
- `swagger.json` — OpenAPI 3.0 spec
- `swagger.yaml` — YAML version

Add `docs.go` to git (it's the embed source). Add `swagger.json` and `swagger.yaml` to `.gitignore` (regeneratable).

## Implementation Order

1. Prometheus metrics (middleware + GORM plugin + business metrics + /metrics endpoint)
2. Database indexes (migration 000032)
3. Makefile DX targets
4. OpenAPI annotations + Swagger UI
5. Final verification

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Prometheus adds latency | Low | Low | Histogram buckets are cheap; middleware adds <1ms |
| Index migration on large tables | Low | Medium | IF NOT EXISTS is safe; SQLite locks briefly |
| swag annotations verbose | Medium | Low | Mechanical work, follow the pattern |
| Swagger UI security | Low | Medium | No auth on /swagger — restrict via network or add basic auth |

## Out of Scope

- Grafana dashboards (ops team can build from /metrics)
- Distributed tracing / OpenTelemetry (future enhancement)
- Log aggregation (zerolog already in place)
