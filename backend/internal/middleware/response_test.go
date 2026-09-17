package middleware

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestOrdinaryMiddlewareErrorEnvelope(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	manager := auth.NewJWTManager("test-secret-at-least-16-chars", time.Hour)
	token := generateTestToken(manager, model.User{ID: 1, Role: "admin"})
	cases := []struct {
		name             string
		handler          gin.HandlerFunc
		role, id, header string
		status           int
		message          string
	}{
		{"auth missing", AuthMiddleware(manager, nil), "", "1", "", 401, "缺少 Authorization 头"},
		{"auth unavailable", AuthMiddleware(manager, nil), "", "1", "Bearer " + token, 503, "认证服务不可用"},
		{"rbac", RBAC("nodes:delete"), "viewer", "1", "", 403, "权限不足"},
		{"node bad id", OwnershipNodeCheck(db), "operator", "bad", "", 400, "无效的节点 ID"},
		{"task bad id", OwnershipTaskCheck(db), "operator", "bad", "", 400, "无效的任务 ID"},
		{"node database", OwnershipNodeCheck(db), "operator", "1", "", 500, "服务器内部错误"},
		{"task database", OwnershipTaskCheck(db), "operator", "1", "", 500, "服务器内部错误"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := setupTestRouter()
			called := false
			r.GET("/:id", func(c *gin.Context) { c.Set(CtxRole, tc.role) }, tc.handler, func(c *gin.Context) { called = true })
			w := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/"+tc.id, nil)
			req.Header.Set("Authorization", tc.header)
			r.ServeHTTP(w, req)
			if called || w.Code != tc.status {
				t.Fatalf("called=%v status=%d", called, w.Code)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			var code int
			var message string
			_ = json.Unmarshal(body["code"], &code)
			_ = json.Unmarshal(body["message"], &message)
			if len(body) != 3 || code != tc.status || message != tc.message || string(body["data"]) != "null" {
				t.Fatalf("unexpected envelope: %s", w.Body.String())
			}
		})
	}
}

func TestAuditMissingContextEnvelope(t *testing.T) {
	r := setupTestRouter()
	r.POST("/test", AuditLogger(&gorm.DB{}), func(c *gin.Context) {})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/test", nil))
	if w.Code != 500 || w.Body.String() != `{"code":500,"message":"audit log missing user context","data":null}` {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
}
