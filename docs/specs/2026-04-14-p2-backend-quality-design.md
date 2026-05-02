# P2: Backend Quality — API Response Standardization & Code Cleanup

> Historical note: This dated design snapshot documents the plan at the time it was written. Treat it as implementation history, not current operating documentation; verify commands, paths, and workflow behavior against the current repo before acting.

Phase 2 of the Xirang improvement plan. Focuses on unified API response format, error handling standardization, and code deduplication.

## Scope

One PR covering:

1. **Unified response envelope** — all API responses use `{code, message, data}` format
2. **Centralized response helpers** — typed helper functions for all HTTP status codes
3. **Handler refactoring** — all ~45 handlers migrated to use new helpers
4. **Silent error fixes** — config_handler.go Export and similar unhandled DB errors
5. **sanitizeNode consolidation** — move to model method, eliminate duplication
6. **Frontend API client update** — core.ts parses new envelope, transparent to page components

## Response Envelope

Every API response follows this structure:

### Success (HTTP 2xx)

```json
{"code": 0, "message": "ok", "data": {"id": 1, "name": "node-1"}}
```

### Success with message only (confirmations)

```json
{"code": 0, "message": "删除成功", "data": null}
```

### Paginated success

```json
{"code": 0, "message": "ok", "data": [...], "total": 100, "page": 1, "page_size": 20}
```

### Error (HTTP 4xx/5xx)

```json
{"code": 400, "message": "请求参数不合法", "data": null}
{"code": 401, "message": "未授权", "data": null}
{"code": 500, "message": "服务器内部错误", "data": null}
```

### Rules

- `code`: 0 for success, HTTP status code for errors
- `message`: human-readable description (Chinese)
- `data`: response payload or null
- Pagination adds `total`, `page`, `page_size` as top-level fields
- Internal error details are logged server-side, never exposed to client

## Backend: Response Helpers

### New file: `backend/internal/api/handlers/response.go`

```go
// Response is the unified API response envelope.
type Response struct {
    Code    int         `json:"code"`
    Message string      `json:"message"`
    Data    interface{} `json:"data"`
}

// PaginatedResponse extends Response with pagination metadata.
type PaginatedResponse struct {
    Code     int         `json:"code"`
    Message  string      `json:"message"`
    Data     interface{} `json:"data"`
    Total    int64       `json:"total"`
    Page     int         `json:"page"`
    PageSize int         `json:"page_size"`
}
```

### Success helpers

| Function | HTTP Status | Use case |
|----------|-------------|----------|
| `respondOK(c, data)` | 200 | Return data |
| `respondCreated(c, data)` | 201 | After creating a resource |
| `respondMessage(c, msg)` | 200 | Action confirmations (delete, update, logout) |
| `respondPaginated(c, data, total, page, pageSize)` | 200 | List endpoints with pagination |

### Error helpers

| Function | HTTP Status | Use case |
|----------|-------------|----------|
| `respondBadRequest(c, msg)` | 400 | Validation errors, bad input |
| `respondUnauthorized(c, msg)` | 401 | Auth failures |
| `respondForbidden(c, msg)` | 403 | Permission denied |
| `respondNotFound(c, msg)` | 404 | Resource not found |
| `respondConflict(c, msg)` | 409 | Duplicate resource |
| `respondInternalError(c, err)` | 500 | Server errors (logs `err` internally) |

Design decisions:
- Error helpers take `string` (not `error`) except `respondInternalError` — forces callers to provide user-facing messages
- `respondInternalError` takes `error`, logs it with request path context, returns generic message to client
- Delete old `response_helpers.go` — its single function is replaced

## Backend: Handler Migration

All handlers in `backend/internal/api/handlers/` will be migrated:

**Before:**
```go
c.JSON(http.StatusBadRequest, gin.H{"error": "参数不合法"})
// or
c.JSON(http.StatusOK, gin.H{"message": "删除成功"})
// or
c.JSON(http.StatusOK, gin.H{"data": nodes, "total": total, "page": page, "page_size": pageSize})
```

**After:**
```go
respondBadRequest(c, "参数不合法")
// or
respondMessage(c, "删除成功")
// or
respondPaginated(c, nodes, total, page, pageSize)
```

### Migration scope

~45 handler files, estimated ~200 `c.JSON(...)` call sites to replace. Grouped by handler for manageable review:
- auth_handler.go, user_handler.go
- node_handler.go (largest, ~30 call sites)
- task_handler.go, policy_handler.go
- alert_handler.go, integration_handler.go
- All remaining handlers

### Silent error fixes

During migration, fix these known silent error swallowing patterns:
- `config_handler.go` Export function: 4 DB queries with unchecked errors → add error checks with `respondInternalError`
- Any other handler where `db.Find()` or `db.First()` errors are ignored

## Backend: sanitizeNode Consolidation

**Current state:** `sanitizeNode()` is a standalone function in `node_handler.go:68-78`, called from 4 locations across 3 handler files.

**Fix:** Move to model as a method:

```go
// backend/internal/model/node.go
func (n Node) Sanitized() Node {
    copy := n
    copy.Password = ""
    copy.PrivateKey = ""
    if copy.SSHKey != nil {
        keyCopy := *copy.SSHKey
        keyCopy.PrivateKey = ""
        copy.SSHKey = &keyCopy
    }
    return copy
}
```

Update all 4 call sites to use `node.Sanitized()` instead of `sanitizeNode(node)`. Delete the old function from `node_handler.go`.

## Frontend: API Client Update

Changes contained in `web/src/lib/api/core.ts`:

### Type updates

```ts
export type Envelope<T> = {
  code: number;
  message: string;
  data: T;
};

export type PaginatedEnvelope<T> = {
  code: number;
  message: string;
  data: T;
  total: number;
  page: number;
  page_size: number;
};
```

### request() function update

After JSON parsing, check the envelope:
- If `code === 0`: return `data` as `T` (auto-unwrap)
- If `code !== 0`: throw `ApiError(code, message)`
- All endpoints return JSON with the envelope — no 204 No Content responses

This makes `unwrapData()` effectively a no-op for the new format. Keep it temporarily for backward compat during migration, mark as deprecated.

### Caller impact

- `unwrapPaginated()` still needed — reads `total/page/page_size` from the envelope
- Page components and hooks: **minimal changes** — the API client layer absorbs the format change
- Any caller doing `const result = await request<Envelope<T>>(...)` then accessing `result.data` will need updating to just `await request<T>(...)` since `request()` now auto-unwraps

## Out of Scope

- Test coverage expansion (deferred to P2.5 or later)
- Frontend component refactoring (P3)
- Prometheus metrics, DB indexes (P4)
- WebSocket protocol changes (WS messages keep their existing format)

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Handler migration misses a c.JSON call | Medium | Low | grep verification after migration |
| Frontend breaks on new format | Medium | High | Update core.ts first, test each page |
| Pagination format change breaks pages | Low | High | PaginatedEnvelope keeps same field names |
| sanitizeNode callers missed | Low | Low | grep for old function name |

## Implementation Order

1. Create `response.go` with helpers + add tests
2. Move `sanitizeNode` to `model/node.go` as `Sanitized()` method
3. Migrate handlers in batches (auth → node → task → policy → alert → rest)
4. Fix silent error patterns during migration
5. Delete old `response_helpers.go` and `paginatedResponse()` from `helpers.go`
6. Update frontend `core.ts` types and `request()` function
7. Update frontend callers that need adjustment
8. Full verification
