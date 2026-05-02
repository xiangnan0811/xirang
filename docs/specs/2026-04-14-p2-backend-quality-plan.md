# P2: Backend Quality — Implementation Plan

> **For agentic workers:** Use the current repo-approved task execution workflow to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Standardize all API responses to a `{code, message, data}` envelope format, consolidate response helpers, fix silent errors, deduplicate sanitizeNode, and update the frontend API client.

**Architecture:** New `response.go` with typed helpers replaces all 465 inline `c.JSON()` calls across 32 handler files. Frontend `core.ts` auto-unwraps the new envelope so page components need minimal changes. One PR.

**Tech Stack:** Go 1.26 (Gin), React 18 + TypeScript (Vite)

---

## Replacement Rules

These rules apply to ALL handler migration tasks. Each `c.JSON(...)` call maps to one helper:

| Old Pattern | New Helper |
|-------------|------------|
| `c.JSON(http.StatusOK, gin.H{"data": X})` | `respondOK(c, X)` |
| `c.JSON(http.StatusOK, X)` (returning struct/map directly) | `respondOK(c, X)` |
| `c.JSON(http.StatusCreated, gin.H{"data": X})` | `respondCreated(c, X)` |
| `c.JSON(http.StatusOK, gin.H{"message": "..."})` | `respondMessage(c, "...")` |
| `paginatedResponse(c, data, total, pg)` | `respondPaginated(c, data, total, pg)` |
| `c.JSON(http.StatusBadRequest, gin.H{"error": "..."})` | `respondBadRequest(c, "...")` |
| `c.JSON(http.StatusUnauthorized, gin.H{"error": "..."})` | `respondUnauthorized(c, "...")` |
| `c.JSON(http.StatusForbidden, gin.H{"error": "..."})` | `respondForbidden(c, "...")` |
| `c.JSON(http.StatusNotFound, gin.H{"error": "..."})` | `respondNotFound(c, "...")` |
| `c.JSON(http.StatusConflict, gin.H{"error": "..."})` | `respondConflict(c, "...")` |
| `respondInternalError(c, err)` (old helper) | `respondInternalError(c, err)` (new helper — same name, new format) |
| `c.JSON(http.StatusInternalServerError, gin.H{"error": "..."})` | `respondInternalError(c, fmt.Errorf("..."))` |

**Special cases:**
- `c.JSON(200, gin.H{"data": X, "extra_field": Y})` — use `respondOK(c, gin.H{"extra_field": Y, ...X})` or create a response struct
- Login/2FA endpoints returning tokens — use `respondOK(c, gin.H{"token": ..., ...})`
- Handlers with `c.String()` or non-JSON responses — leave unchanged (rare)

---

### Task 1: Create response.go with helpers and tests

**Files:**
- Create: `backend/internal/api/handlers/response.go`
- Create: `backend/internal/api/handlers/response_test.go`

- [ ] **Step 1: Write tests for response helpers**

```go
// backend/internal/api/handlers/response_test.go
package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func setupTestRouter(handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/test", handler)
	return r
}

func TestRespondOK(t *testing.T) {
	r := setupTestRouter(func(c *gin.Context) {
		respondOK(c, gin.H{"id": 1, "name": "test"})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/test", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际 %d", w.Code)
	}
	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if resp.Code != 0 {
		t.Fatalf("期望 code=0，实际 %d", resp.Code)
	}
	if resp.Message != "ok" {
		t.Fatalf("期望 message=ok，实际 %s", resp.Message)
	}
}

func TestRespondMessage(t *testing.T) {
	r := setupTestRouter(func(c *gin.Context) {
		respondMessage(c, "删除成功")
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/test", nil))

	var resp Response
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Code != 0 || resp.Message != "删除成功" || resp.Data != nil {
		t.Fatalf("响应不符合预期: %+v", resp)
	}
}

func TestRespondBadRequest(t *testing.T) {
	r := setupTestRouter(func(c *gin.Context) {
		respondBadRequest(c, "参数不合法")
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/test", nil))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望状态码 400，实际 %d", w.Code)
	}
	var resp Response
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Code != 400 || resp.Message != "参数不合法" {
		t.Fatalf("响应不符合预期: %+v", resp)
	}
}

func TestRespondInternalError(t *testing.T) {
	r := setupTestRouter(func(c *gin.Context) {
		respondInternalError(c, fmt.Errorf("db connection failed"))
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/test", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("期望状态码 500，实际 %d", w.Code)
	}
	var resp Response
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Code != 500 {
		t.Fatalf("期望 code=500，实际 %d", resp.Code)
	}
	// 内部错误消息不应暴露给客户端
	if resp.Message != "服务器内部错误" {
		t.Fatalf("不应暴露内部错误: %s", resp.Message)
	}
}

func TestRespondPaginated(t *testing.T) {
	r := setupTestRouter(func(c *gin.Context) {
		respondPaginated(c, []string{"a", "b"}, 10, 1, 20)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/test", nil))

	var resp PaginatedResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Code != 0 || resp.Total != 10 || resp.Page != 1 || resp.PageSize != 20 {
		t.Fatalf("分页响应不符合预期: %+v", resp)
	}
}

func TestRespondCreated(t *testing.T) {
	r := setupTestRouter(func(c *gin.Context) {
		respondCreated(c, gin.H{"id": 42})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/test", nil))

	if w.Code != http.StatusCreated {
		t.Fatalf("期望状态码 201，实际 %d", w.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/api/handlers/... -v -run "TestRespond"`
Expected: Compilation error — `respondOK` etc. not defined yet.

- [ ] **Step 3: Implement response.go**

```go
// backend/internal/api/handlers/response.go
package handlers

import (
	"net/http"

	"xirang/backend/internal/logger"

	"github.com/gin-gonic/gin"
)

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

// --- Success helpers ---

func respondOK(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Response{Code: 0, Message: "ok", Data: data})
}

func respondCreated(c *gin.Context, data interface{}) {
	c.JSON(http.StatusCreated, Response{Code: 0, Message: "ok", Data: data})
}

func respondMessage(c *gin.Context, msg string) {
	c.JSON(http.StatusOK, Response{Code: 0, Message: msg, Data: nil})
}

func respondPaginated(c *gin.Context, data interface{}, total int64, page, pageSize int) {
	c.JSON(http.StatusOK, PaginatedResponse{
		Code:     0,
		Message:  "ok",
		Data:     data,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	})
}

// --- Error helpers ---

func respondBadRequest(c *gin.Context, msg string) {
	c.JSON(http.StatusBadRequest, Response{Code: http.StatusBadRequest, Message: msg, Data: nil})
}

func respondUnauthorized(c *gin.Context, msg string) {
	c.JSON(http.StatusUnauthorized, Response{Code: http.StatusUnauthorized, Message: msg, Data: nil})
}

func respondForbidden(c *gin.Context, msg string) {
	c.JSON(http.StatusForbidden, Response{Code: http.StatusForbidden, Message: msg, Data: nil})
}

func respondNotFound(c *gin.Context, msg string) {
	c.JSON(http.StatusNotFound, Response{Code: http.StatusNotFound, Message: msg, Data: nil})
}

func respondConflict(c *gin.Context, msg string) {
	c.JSON(http.StatusConflict, Response{Code: http.StatusConflict, Message: msg, Data: nil})
}

func respondInternalError(c *gin.Context, err error) {
	if err != nil {
		logger.Module("api").Error().Err(err).Str("path", c.FullPath()).Msg("服务器内部错误")
	}
	c.JSON(http.StatusInternalServerError, Response{
		Code:    http.StatusInternalServerError,
		Message: "服务器内部错误",
		Data:    nil,
	})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/api/handlers/... -v -run "TestRespond"`
Expected: All 6 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/api/handlers/response.go backend/internal/api/handlers/response_test.go
git commit -m "$(cat <<'EOF'
feat(backend): add unified API response helpers

New response.go with {code, message, data} envelope format.
Helpers: respondOK, respondCreated, respondMessage, respondPaginated,
respondBadRequest, respondUnauthorized, respondForbidden, respondNotFound,
respondConflict, respondInternalError.
EOF
)"
```

---

### Task 2: Move sanitizeNode to model.Node.Sanitized()

**Files:**
- Modify: `backend/internal/model/node.go`
- Modify: `backend/internal/api/handlers/node_handler.go` (delete sanitizeNode, update 4 call sites)
- Modify: `backend/internal/api/handlers/task_handler.go` (update 2 call sites)
- Modify: `backend/internal/api/handlers/batch_handler.go` (update 1 call site)

- [ ] **Step 1: Add Sanitized() method to model.Node**

Add to `backend/internal/model/node.go`:

```go
// Sanitized 返回去除敏感字段（密码、私钥）的节点副本，用于 API 响应。
func (n Node) Sanitized() Node {
	safe := n
	safe.Password = ""
	safe.PrivateKey = ""
	if safe.SSHKey != nil {
		keyCopy := *safe.SSHKey
		keyCopy.PrivateKey = ""
		safe.SSHKey = &keyCopy
	}
	return safe
}
```

- [ ] **Step 2: Update all call sites**

Replace in `node_handler.go`:
- `sanitizeNode(node)` → `node.Sanitized()` (lines 101, 117, 253, 380)

Replace in `task_handler.go`:
- `sanitizeNode(tasks[i].Node)` → `tasks[i].Node = tasks[i].Node.Sanitized()` (lines 115, 159)

Replace in `batch_handler.go`:
- `sanitizeNode(tasks[i].Node)` → `tasks[i].Node = tasks[i].Node.Sanitized()` (line 186)

- [ ] **Step 3: Delete old sanitizeNode function from node_handler.go** (lines 68-78)

- [ ] **Step 4: Verify**

Run: `cd backend && go build ./... && go test ./... -count=1`
Expected: Clean build, all tests pass.

Run: `grep -rn "sanitizeNode" backend/` — should return 0 matches.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/model/node.go backend/internal/api/handlers/node_handler.go backend/internal/api/handlers/task_handler.go backend/internal/api/handlers/batch_handler.go
git commit -m "$(cat <<'EOF'
refactor(backend): move sanitizeNode to model.Node.Sanitized()

Consolidate credential sanitization into a model method.
Eliminates duplication across 3 handler files (7 call sites).
EOF
)"
```

---

### Task 3: Migrate auth + user + captcha handlers

**Files:**
- Modify: `backend/internal/api/handlers/auth_handler.go` (~52 c.JSON calls)
- Modify: `backend/internal/api/handlers/user_handler.go` (~10 c.JSON calls)
- Modify: `backend/internal/api/handlers/captcha_handler.go` (~3 c.JSON calls)

- [ ] **Step 1: Migrate auth_handler.go**

Apply the replacement rules from the top of this plan to every `c.JSON(...)` call in `auth_handler.go`. Key patterns in this file:
- Login success returns token/user data → `respondOK(c, gin.H{"token": ..., "user": ...})`
- 2FA responses → `respondOK(c, gin.H{"requires_2fa": true, "login_token": ...})`
- `{"message": "..."}` confirmations (onboard, password change, logout) → `respondMessage(c, "...")`
- `{"error": "..."}` validation errors → `respondBadRequest(c, "...")`
- `respondInternalError(c, err)` — already uses this name, but ensure it now uses the new format

Also remove `"net/http"` import if no longer directly used (helpers handle status codes), and remove `"log"` import if `log.Printf` calls are replaced by the new `respondInternalError` which logs internally.

- [ ] **Step 2: Migrate user_handler.go**

Same pattern. Key call sites:
- User CRUD operations → `respondOK`, `respondCreated`, `respondMessage`
- `{"message": "删除成功"}` → `respondMessage(c, "删除成功")`

- [ ] **Step 3: Migrate captcha_handler.go**

Small file, ~3 calls.

- [ ] **Step 4: Verify**

Run: `cd backend && go build ./... && go test ./internal/api/handlers/... -count=1`
Expected: Clean build, all tests pass.

Run: `grep -n "gin.H{" backend/internal/api/handlers/auth_handler.go backend/internal/api/handlers/user_handler.go backend/internal/api/handlers/captcha_handler.go` — should show zero `c.JSON` + `gin.H` patterns (all replaced by helpers). Note: `gin.H` may still appear as data payloads inside `respondOK` calls, which is fine.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/api/handlers/auth_handler.go backend/internal/api/handlers/user_handler.go backend/internal/api/handlers/captcha_handler.go
git commit -m "$(cat <<'EOF'
refactor(backend): migrate auth/user/captcha handlers to response helpers

Replace all c.JSON() calls with unified {code, message, data} helpers.
EOF
)"
```

---

### Task 4: Migrate node_handler.go

**Files:**
- Modify: `backend/internal/api/handlers/node_handler.go` (~65 c.JSON calls — largest handler)

- [ ] **Step 1: Migrate all c.JSON calls in node_handler.go**

This is the largest file. Apply replacement rules systematically:
- All `c.JSON(http.StatusOK, gin.H{"data": ...})` → `respondOK(c, ...)`
- All `c.JSON(http.StatusCreated, gin.H{"data": ...})` → `respondCreated(c, ...)`
- All `c.JSON(http.StatusBadRequest, gin.H{"error": "..."})` → `respondBadRequest(c, "...")`
- All `c.JSON(http.StatusForbidden, gin.H{"error": "..."})` → `respondForbidden(c, "...")`
- All `c.JSON(http.StatusNotFound, gin.H{"error": "..."})` → `respondNotFound(c, "...")`
- All `respondInternalError(c, err)` — keep as-is (already uses new helper from response.go)
- Replace `log.Printf(...)` + `c.JSON(500, ...)` combos with just `respondInternalError(c, err)`

Also update `sanitizeNode` call sites to use `node.Sanitized()` if any were missed in Task 2.

- [ ] **Step 2: Verify**

Run: `cd backend && go build ./... && go test ./internal/api/handlers/... -count=1`
Expected: Clean build, all tests pass.

- [ ] **Step 3: Commit**

```bash
git add backend/internal/api/handlers/node_handler.go
git commit -m "$(cat <<'EOF'
refactor(backend): migrate node_handler to response helpers

Replace ~65 c.JSON() calls with unified response helpers.
Largest single handler migration.
EOF
)"
```

---

### Task 5: Migrate task + task_run + batch handlers

**Files:**
- Modify: `backend/internal/api/handlers/task_handler.go` (~32 c.JSON calls)
- Modify: `backend/internal/api/handlers/task_run_handler.go` (~11 c.JSON calls)
- Modify: `backend/internal/api/handlers/batch_handler.go` (~17 c.JSON calls)

- [ ] **Step 1: Migrate task_handler.go**

Apply replacement rules. Key patterns:
- `paginatedResponse(c, tasks, total, pg)` → `respondPaginated(c, tasks, total, pg.Page, pg.PageSize)`
- Note: the old `paginatedResponse` takes `paginationParams` struct, the new `respondPaginated` takes explicit `page, pageSize int`. Extract fields from `pg`.

- [ ] **Step 2: Migrate task_run_handler.go**

Same patterns.

- [ ] **Step 3: Migrate batch_handler.go**

Same patterns.

- [ ] **Step 4: Verify**

Run: `cd backend && go build ./... && go test ./internal/api/handlers/... -count=1`

- [ ] **Step 5: Commit**

```bash
git add backend/internal/api/handlers/task_handler.go backend/internal/api/handlers/task_run_handler.go backend/internal/api/handlers/batch_handler.go
git commit -m "$(cat <<'EOF'
refactor(backend): migrate task/task_run/batch handlers to response helpers
EOF
)"
```

---

### Task 6: Migrate policy + snapshot + snapshot_diff handlers

**Files:**
- Modify: `backend/internal/api/handlers/policy_handler.go` (~31 c.JSON calls)
- Modify: `backend/internal/api/handlers/snapshot_handler.go` (~17 c.JSON calls)
- Modify: `backend/internal/api/handlers/snapshot_diff_handler.go` (~9 c.JSON calls)

- [ ] **Step 1: Migrate all three files** using the replacement rules.

- [ ] **Step 2: Verify**

Run: `cd backend && go build ./... && go test ./internal/api/handlers/... -count=1`

- [ ] **Step 3: Commit**

```bash
git add backend/internal/api/handlers/policy_handler.go backend/internal/api/handlers/snapshot_handler.go backend/internal/api/handlers/snapshot_diff_handler.go
git commit -m "$(cat <<'EOF'
refactor(backend): migrate policy/snapshot handlers to response helpers
EOF
)"
```

---

### Task 7: Migrate alert + integration handlers

**Files:**
- Modify: `backend/internal/api/handlers/alert_handler.go` (~25 c.JSON calls)
- Modify: `backend/internal/api/handlers/integration_handler.go` (~42 c.JSON calls)

- [ ] **Step 1: Migrate both files** using the replacement rules.

- [ ] **Step 2: Verify**

Run: `cd backend && go build ./... && go test ./internal/api/handlers/... -count=1`

- [ ] **Step 3: Commit**

```bash
git add backend/internal/api/handlers/alert_handler.go backend/internal/api/handlers/integration_handler.go
git commit -m "$(cat <<'EOF'
refactor(backend): migrate alert/integration handlers to response helpers
EOF
)"
```

---

### Task 8: Migrate all remaining handlers

**Files** (16 handlers):
- `settings_handler.go` (~8), `config_handler.go` (~8), `report_handler.go` (~29)
- `overview_handler.go` (~2), `overview_storage_handler.go` (~2), `overview_traffic_handler.go` (~2), `overview_backup_health_handler.go` (~1)
- `ssh_key_handler.go` (~29), `docker_handler.go` (~4), `file_handler.go` (~20)
- `node_migrate_handler.go` (~9), `node_migrate_preflight_handler.go` (~8)
- `storage_guide_handler.go` (~7), `system_handler.go` (~9)
- `version_handler.go` (~7), `hook_templates_handler.go` (~1)
- `terminal_handler.go` (~1), `ws_handler.go` (~1), `audit_handler.go` (already done if applicable)

- [ ] **Step 1: Migrate all remaining handler files** using the replacement rules.

**config_handler.go special fix**: In the Export function (around line 65), add error checking for the 4 DB queries that currently silently ignore errors:

Before:
```go
h.db.Find(&nodes)
h.db.Find(&sshKeys)
h.db.Preload("Nodes").Find(&policies)
h.db.Preload("Node").Preload("Policy").Find(&tasks)
```

After:
```go
if err := h.db.Find(&nodes).Error; err != nil {
    respondInternalError(c, err)
    return
}
if err := h.db.Find(&sshKeys).Error; err != nil {
    respondInternalError(c, err)
    return
}
if err := h.db.Preload("Nodes").Find(&policies).Error; err != nil {
    respondInternalError(c, err)
    return
}
if err := h.db.Preload("Node").Preload("Policy").Find(&tasks).Error; err != nil {
    respondInternalError(c, err)
    return
}
```

- [ ] **Step 2: Verify**

Run: `cd backend && go build ./... && go test ./... -count=1`

- [ ] **Step 3: Commit**

```bash
git add -u backend/internal/api/handlers/
git commit -m "$(cat <<'EOF'
refactor(backend): migrate remaining handlers to response helpers

Complete handler migration (16 files). Fix silent DB error
swallowing in config_handler.go Export function.
EOF
)"
```

---

### Task 9: Delete old helpers and cleanup

**Files:**
- Delete: `backend/internal/api/handlers/response_helpers.go`
- Modify: `backend/internal/api/handlers/helpers.go` (remove `paginatedResponse` function)

- [ ] **Step 1: Delete response_helpers.go**

This file only contained the old `respondInternalError` function which is now replaced by `response.go`.

- [ ] **Step 2: Remove paginatedResponse from helpers.go**

Delete the `paginatedResponse` function (lines 161-168 in helpers.go). It's replaced by `respondPaginated` in `response.go`.

- [ ] **Step 3: Verify no remaining references**

Run: `grep -rn "paginatedResponse\b" backend/internal/api/handlers/` — should return 0 matches.
Run: `grep -rn "response_helpers" backend/` — should return 0 matches.

- [ ] **Step 4: Build and test**

Run: `cd backend && go build ./... && go test ./... -count=1`

- [ ] **Step 5: Verify all responses use new format**

Run: `grep -n 'c\.JSON(' backend/internal/api/handlers/*.go` — should only appear inside `response.go` (the helpers themselves call c.JSON). If any `c.JSON` calls remain in other handler files, fix them.

- [ ] **Step 6: Commit**

```bash
git add -u backend/internal/api/handlers/
git commit -m "$(cat <<'EOF'
refactor(backend): remove old response helpers

Delete response_helpers.go and paginatedResponse from helpers.go.
All handlers now use unified response.go helpers exclusively.
EOF
)"
```

---

### Task 10: Update frontend core.ts

**Files:**
- Modify: `web/src/lib/api/core.ts`

- [ ] **Step 1: Update Envelope and PaginatedEnvelope types**

Replace the existing types:

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

- [ ] **Step 2: Update request() to auto-unwrap the envelope**

In the `request<T>()` function, after the JSON parsing and before the return, replace the success handling (the section after `if (!response.ok)`) with:

```ts
  // Auto-unwrap unified {code, message, data} envelope
  if (payload && typeof payload === "object" && "code" in (payload as Record<string, unknown>)) {
    const envelope = payload as { code: number; message: string; data: unknown };
    if (envelope.code !== 0) {
      throw new ApiError(envelope.code, envelope.message, payload);
    }
    // For paginated responses, return the full envelope (unwrapPaginated needs total/page/page_size)
    if ("total" in (payload as Record<string, unknown>)) {
      return payload as T;
    }
    return envelope.data as T;
  }

  return payload as T;
```

- [ ] **Step 3: Update unwrapPaginated to handle new format**

```ts
export function unwrapPaginated<T>(payload: PaginatedEnvelope<T[]>): {
  items: T[];
  total: number;
  page: number;
  pageSize: number;
} {
  return {
    items: payload.data ?? ([] as T[]),
    total: Number(payload.total ?? 0),
    page: Number(payload.page ?? 1),
    pageSize: Number(payload.page_size ?? 20),
  };
}
```

This is unchanged since field names (`data`, `total`, `page`, `page_size`) are the same in the new format.

- [ ] **Step 4: Deprecate unwrapData**

Update `unwrapData` to be a simple pass-through since `request()` now auto-unwraps:

```ts
/** @deprecated request() now auto-unwraps the envelope. This function is a pass-through. */
export function unwrapData<T>(payload: Envelope<T> | T): T {
  if (payload && typeof payload === "object" && "data" in (payload as Record<string, unknown>)) {
    return ((payload as Envelope<T>).data ?? null) as T;
  }
  return payload as T;
}
```

- [ ] **Step 5: Verify frontend**

Run: `cd web && npm run check`
Expected: typecheck + lint + tests + build all pass.

- [ ] **Step 6: Commit**

```bash
git add web/src/lib/api/core.ts
git commit -m "$(cat <<'EOF'
feat(web): update API client for unified {code, message, data} envelope

request() now auto-unwraps the envelope: returns data on code=0,
throws ApiError on code!=0. unwrapData deprecated (pass-through).
unwrapPaginated unchanged (same field names in new format).
EOF
)"
```

---

### Task 11: Update frontend API callers

**Files:**
- Modify: All files in `web/src/lib/api/` that import `Envelope` or call `unwrapData`

The key change: callers that do `request<Envelope<T>>(...)` then `unwrapData(result)` should now just do `request<T>(...)` since `request()` auto-unwraps.

- [ ] **Step 1: Update API modules**

For each file that imports `unwrapData` from `core`:

Pattern replacement:
```ts
// Before:
const payload = await request<Envelope<Node[]>>(url, opts);
const rows = unwrapData(payload) ?? [];

// After:
const rows = (await request<Node[]>(url, opts)) ?? [];
```

For `.then(unwrapData)` chains:
```ts
// Before:
request<Envelope<Report>>(url, opts).then(unwrapData)

// After:
request<Report>(url, opts)
```

Files to update (~12 files):
- `nodes-api.ts`, `integrations-api.ts`, `audit-api.ts`, `files-api.ts`
- `ssh-keys-api.ts`, `policies-api.ts`, `storage-guide-api.ts`, `reports-api.ts`
- `task-runs-api.ts`, `snapshot-diff-api.ts`, `alerts-api.ts`
- Any other file importing `unwrapData` or `Envelope`

For `unwrapPaginated` callers — these still need the full envelope, so they should use `request<PaginatedEnvelope<T[]>>(...)` since pagination metadata is at the envelope level.

- [ ] **Step 2: Verify**

Run: `cd web && npm run check`
Expected: typecheck + lint + tests + build all pass.

- [ ] **Step 3: Commit**

```bash
git add -u web/src/lib/api/
git commit -m "$(cat <<'EOF'
refactor(web): simplify API callers with auto-unwrapped responses

Remove unwrapData calls — request() now returns data directly.
Update type parameters from Envelope<T> to T.
EOF
)"
```

---

### Task 12: Final verification

- [ ] **Step 1: Run full backend test suite**

Run: `cd backend && go test ./... -count=1`
Expected: All tests pass.

- [ ] **Step 2: Run backend lint**

Run: `cd backend && golangci-lint run ./...`
Expected: 0 issues.

- [ ] **Step 3: Run full frontend check**

Run: `cd web && npm run check`
Expected: typecheck + lint + tests + build all pass.

- [ ] **Step 4: Verify no old-format responses remain**

Run: `grep -rn 'c\.JSON(' backend/internal/api/handlers/*.go | grep -v response.go`
Expected: Zero matches (all c.JSON calls are inside response.go helpers only).

Run: `grep -rn 'gin\.H{"error"' backend/internal/api/handlers/`
Expected: Zero matches.

Run: `grep -rn 'gin\.H{"message"' backend/internal/api/handlers/`
Expected: Zero matches (messages now go through respondMessage).

- [ ] **Step 5: Verify sanitizeNode is gone**

Run: `grep -rn "sanitizeNode" backend/`
Expected: Zero matches.

- [ ] **Step 6: Review git log**

Run: `git log --oneline` — verify commits are clean and well-organized.
