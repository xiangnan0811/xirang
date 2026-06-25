# API Handlers

> Quick reference for `backend/internal/api/handlers/` — the REST handler layer.
> Full conventions: [Backend directory structure](../../../../.trellis/spec/backend/directory-structure.md) · [Error handling](../../../../.trellis/spec/backend/error-handling.md) · [Quality](../../../../.trellis/spec/backend/quality-guidelines.md)

---

## What Lives Here

104 Go files implementing thin REST handlers for the Gin router. Each handler file is resource-oriented (`node_handler.go`, `settings_handler.go`, `alert_handler.go`), with tests colocated as `*_test.go`. RBAC integration tests live one level up in `backend/internal/api/` (e.g. `app_credential_rbac_test.go`).

---

## Handler Anatomy

Handlers are **orchestration layers only** — parse request, bind JSON, call a domain service or GORM query, then return through `response.go` helpers. Business logic belongs in domain packages (`dashboards.Service`, `settings.Service`, `task.Manager`), not here.

Reference example: `dashboard_handler.go`

```go
// Struct holds dependencies (service or *gorm.DB)
type DashboardHandler struct {
    svc *dashboards.Service
}

// Constructor wires dependencies
func NewDashboardHandler(db *gorm.DB) *DashboardHandler {
    return &DashboardHandler{svc: dashboards.NewService(db)}
}

// Method = one endpoint. Pattern: parse → bind → call → respond
func (h *DashboardHandler) Get(c *gin.Context) {
    uid := middleware.CurrentUserID(c)          // auth context
    id, ok := parseID(c, "id")                  // path param
    if !ok { return }
    d, err := h.svc.Get(c.Request.Context(), uid, id)
    if err != nil {
        if errors.Is(err, dashboards.ErrNotFound) {
            respondNotFound(c, "看板不存在")     // sentinel → 404
            return
        }
        respondInternalError(c, err)            // unknown → 500
        return
    }
    respondOK(c, d)                             // success → 200
}
```

---

## Response Helpers (`response.go`)

**Never use `c.JSON` directly.** All responses go through these helpers:

| Helper | HTTP Status | When |
|--------|-------------|------|
| `respondOK` | 200 | Successful GET/PUT/PATCH |
| `respondCreated` | 201 | Resource created |
| `respondAccepted` | 202 | Async task started |
| `respondMessage` | 200 | Simple success message |
| `respondPaginated` | 200 | List with `total`, `page`, `pageSize` |
| `respondBadRequest` | 400 | Validation / bind error |
| `respondUnauthorized` | 401 | Not authenticated |
| `respondForbidden` | 403 | Missing permission |
| `respondNotFound` | 404 | Resource missing |
| `respondConflict` | 409 | Duplicate / state conflict |
| `respondBadGateway` | 502 | Upstream / SSH failure |
| `respondServiceUnavailable` | 503 | Dependency down |
| `respondNotImplemented` | 501 | Stub endpoint |
| `respondInternalError` | 500 | Unexpected error (logs raw err) |

Also: `parseID(c, "id")` for path param ID parsing, `mapServiceErr(c, err)` for mapping domain sentinel errors to HTTP responses.

---

## Route Wiring

Routes are registered in `backend/internal/api/router.go`, not in handler files. The standard pattern:

1. Construct handler with dependencies in `NewRouter()`
2. Public routes (login, captcha, version) go directly on `v1`
3. Protected routes go on `secured := v1.Group("")` which applies `AuthMiddleware`, `AuditLogger`, `APIRateLimit`, `MaxBodySize`
4. Each route gets `middleware.RBAC("permission:action")` for authorization

```go
secured.GET("/dashboards", middleware.ETag(), dashboardHandler.List)
secured.POST("/dashboards", middleware.RBAC("dashboards:manage"), dashboardHandler.Create)
secured.GET("/dashboards/:id", dashboardHandler.Get)
```

---

## Request Payloads

Define request structs locally in the handler file, with `json` tags using snake_case to match API conventions:

```go
type dashboardPayload struct {
    Name        string `json:"name"`
    Description string `json:"description"`
}
```

Bind with `c.ShouldBindJSON(&req)` — on error, call `respondBadRequest(c, err.Error())` and return.

---

## Testing

- Colocated: `node_handler_test.go` next to `node_handler.go`
- RBAC integration tests: `backend/internal/api/*_rbac_test.go` (package-level)
- Test fixtures use `FAKE_*_FOR_TEST_ONLY` naming to avoid secret scanner false positives
- Use `httptest` + Gin test mode; assert via `response.go` envelope structure

---

## Forbidden Here

- Ad hoc `c.JSON` responses — use `response.go` helpers
- Business logic growing beyond parse/bind/call/respond — extract to a domain package
- Direct DB queries for cross-domain workflows — use a service
- Returning unsanitized sensitive fields — use `model.Sanitized()`
- Bypassing `AuthMiddleware` / `RBAC` / ownership checks on any route
- Raw `err.Error()` for 500 responses — `respondInternalError` handles logging
