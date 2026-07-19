package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	return gin.New()
}

// setupAuthHandler 创建带 AuthMiddleware 的路由并注册一个简单的 handler 用于验证
func setupAuthHandler(jwtManager *auth.JWTManager, db *gorm.DB) *gin.Engine {
	r := setupTestRouter()
	r.Use(AuthMiddleware(jwtManager, db))
	r.GET("/test", func(c *gin.Context) {
		userID, _ := c.Get(CtxUserID)
		username, _ := c.Get(CtxUsername)
		role, _ := c.Get(CtxRole)
		_, bindingOK := CurrentSessionBinding(c)
		_, rawTokenPresent := c.Get(CtxToken)
		c.JSON(http.StatusOK, gin.H{
			"userID":          userID,
			"username":        username,
			"role":            role,
			"bindingOK":       bindingOK,
			"rawTokenPresent": rawTokenPresent,
		})
	})
	return r
}

// newTestJWTManager 创建用于测试的 JWTManager
func newTestJWTManager() *auth.JWTManager {
	return auth.NewJWTManager("test-secret-at-least-16-chars", 1*time.Hour)
}

// generateTestToken 生成一个有效的测试 token
func generateTestToken(m *auth.JWTManager, user model.User) string {
	token, err := m.GenerateToken(user)
	if err != nil {
		panic(err)
	}
	return token
}

func authPerformRequest(r *gin.Engine, token string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/test", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	r.ServeHTTP(w, req)
	return w
}

// =============================================================================
// AuthMiddleware 认证测试
// =============================================================================

func TestAuthMiddlewareSessionBindingValidToken(t *testing.T) {
	m := newTestJWTManager()
	r := setupAuthHandler(m, nil)
	user := model.User{ID: 1, Username: "admin", Role: "admin", TokenVersion: 1}
	token := generateTestToken(m, user)

	w := authPerformRequest(r, token)

	if w.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际 %d，body: %s", w.Code, w.Body.String())
	}

	var body map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if uid, ok := body["userID"].(float64); !ok || uint(uid) != user.ID {
		t.Errorf("期望 userID=%d，实际 %v", user.ID, body["userID"])
	}
	if body["username"] != user.Username {
		t.Errorf("期望 username=%s，实际 %v", user.Username, body["username"])
	}
	if body["role"] != user.Role {
		t.Errorf("期望 role=%s，实际 %v", user.Role, body["role"])
	}
	if body["bindingOK"] != true {
		t.Fatalf("safe session binding missing: %v", body)
	}
	if body["rawTokenPresent"] != false {
		t.Fatalf("raw JWT remained in Gin context: %v", body)
	}
}

func TestAuthMiddlewareSessionBindingIsPrivateAndExact(t *testing.T) {
	m := newTestJWTManager()
	user := model.User{ID: 8, Username: "operator", Role: "operator", TokenVersion: 4}
	token := generateTestToken(m, user)
	claims, err := m.ParseToken(token)
	if err != nil {
		t.Fatalf("parse test token: %v", err)
	}

	var got SessionBinding
	var ok bool
	r := setupTestRouter()
	r.Use(AuthMiddleware(m, nil))
	r.GET("/test", func(c *gin.Context) {
		got, ok = CurrentSessionBinding(c)
		c.Status(http.StatusNoContent)
	})
	w := authPerformRequest(r, token)
	if w.Code != http.StatusNoContent || !ok {
		t.Fatalf("status=%d bindingOK=%v", w.Code, ok)
	}
	if got.JTI != claims.ID || got.UserID != user.ID || got.Role != user.Role || got.TokenVersion != user.TokenVersion || !got.ExpiresAt.Equal(claims.ExpiresAt.UTC()) {
		t.Fatalf("binding=%+v claims=%+v", got, claims)
	}
	payload, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal session binding: %v", err)
	}
	if string(payload) != "{}" {
		t.Fatalf("session binding exposed JSON: %s", payload)
	}
}

func TestAuthMiddleware_MissingToken(t *testing.T) {
	m := newTestJWTManager()
	r := setupAuthHandler(m, nil)

	w := authPerformRequest(r, "") // 无 Authorization header

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("期望状态码 401，实际 %d", w.Code)
	}
}

func TestAuthMiddleware_InvalidFormat(t *testing.T) {
	m := newTestJWTManager()
	r := setupAuthHandler(m, nil)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "NoBearerPrefix token123")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("期望状态码 401，实际 %d", w.Code)
	}
}

func TestAuthMiddleware_ExpiredToken(t *testing.T) {
	// 使用极短的 ttl，生成一个马上过期的 token
	m := auth.NewJWTManager("test-secret-at-least-16-chars", 1*time.Nanosecond)
	user := model.User{ID: 1, Username: "admin", Role: "admin", TokenVersion: 1}
	token := generateTestToken(m, user)

	// 等待极短时间确保过期；TTL=1ns 已经过期，但需要给 clock 一个 tick 的窗口。
	// JWT 内部使用 time.Now()，无法注入时钟，2ms 是稳定通过的最小可靠等待。
	time.Sleep(2 * time.Millisecond)

	r := setupAuthHandler(m, nil)
	w := authPerformRequest(r, token)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("期望状态码 401（过期 token），实际 %d", w.Code)
	}
}

func TestAuthMiddleware_InvalidSignature(t *testing.T) {
	m := newTestJWTManager()
	// 用另一个 key 的 manager 生成一个 token，然后用第一个 manager 验证
	otherManager := auth.NewJWTManager("different-secret-at-least-16-chars", 1*time.Hour)
	user := model.User{ID: 1, Username: "admin", Role: "admin", TokenVersion: 1}
	token := generateTestToken(otherManager, user)

	r := setupAuthHandler(m, nil)
	w := authPerformRequest(r, token)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("期望状态码 401（无效签名），实际 %d", w.Code)
	}
}

func TestAuthMiddleware_EmptyPartsToken(t *testing.T) {
	m := newTestJWTManager()
	r := setupAuthHandler(m, nil)

	// Authorization header 只包含 "Bearer" 没有 token
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("期望状态码 401（Bearer 后无 token），实际 %d", w.Code)
	}
}

func TestAuthMiddleware_PurposeMismatch(t *testing.T) {
	m := newTestJWTManager()
	r := setupAuthHandler(m, nil)

	user := model.User{ID: 1, Username: "admin", Role: "admin", TokenVersion: 1}
	token, err := m.Generate2FAPendingToken(user)
	if err != nil {
		t.Fatalf("生成 2FA token 失败: %v", err)
	}

	w := authPerformRequest(r, token)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("期望状态码 401（token 用途不匹配），实际 %d, body: %s", w.Code, w.Body.String())
	}
}

func TestAuthMiddleware_TokenVersionMismatch(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("创建 SQLite 内存数据库失败: %v", err)
	}
	_ = db.AutoMigrate(&model.User{})

	// 插入用户，token_version=5
	db.Create(&model.User{ID: 1, Username: "admin", Role: "admin", TokenVersion: 5})

	m := newTestJWTManager()
	// 生成 token 时 token_version=1（与数据库中的 5 不匹配）
	user := model.User{ID: 1, Username: "admin", Role: "admin", TokenVersion: 1}
	token := generateTestToken(m, user)

	r := setupAuthHandler(m, db)
	w := authPerformRequest(r, token)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("期望状态码 401（token 版本不匹配），实际 %d, body: %s", w.Code, w.Body.String())
	}
}

func TestAuthMiddleware_UserNotFound(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("创建 SQLite 内存数据库失败: %v", err)
	}
	_ = db.AutoMigrate(&model.User{})

	m := newTestJWTManager()
	// 生成 token 指向不存在的用户
	user := model.User{ID: 999, Username: "ghost", Role: "admin", TokenVersion: 1}
	token := generateTestToken(m, user)

	r := setupAuthHandler(m, db)
	w := authPerformRequest(r, token)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("期望状态码 401（用户不存在），实际 %d, body: %s", w.Code, w.Body.String())
	}
}

func TestAuthMiddleware_WithDatabase(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("创建 SQLite 内存数据库失败: %v", err)
	}
	_ = db.AutoMigrate(&model.User{})

	// 插入用户
	db.Create(&model.User{ID: 1, Username: "admin", Role: "admin", TokenVersion: 3})

	m := newTestJWTManager()
	// token_version 匹配数据库
	user := model.User{ID: 1, Username: "admin", Role: "admin", TokenVersion: 3}
	token := generateTestToken(m, user)

	r := setupAuthHandler(m, db)
	w := authPerformRequest(r, token)

	if w.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际 %d, body: %s", w.Code, w.Body.String())
	}

	var body map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["username"] != "admin" {
		t.Errorf("期望 username='admin'，实际 %v", body["username"])
	}
}

// =============================================================================
// CurrentRole 辅助测试
// =============================================================================

func TestCurrentRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(CtxRole, "admin")

	role := CurrentRole(c)
	if role != "admin" {
		t.Errorf("期望 CurrentRole='admin'，实际 %q", role)
	}
}

func TestCurrentRole_NotSet(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	role := CurrentRole(c)
	if role != "" {
		t.Errorf("期望未设置时返回空字符串，实际 %q", role)
	}
}
