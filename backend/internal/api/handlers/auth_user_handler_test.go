package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type authUserTestFixture struct {
	db            *gorm.DB
	service       *auth.Service
	jwtManager    *auth.JWTManager
	router        *gin.Engine
	adminUser     model.User
	operatorUser  model.User
	adminToken    string
	operatorToken string
}

func openAuthUserHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}

func seedAuthUser(t *testing.T, db *gorm.DB, username, role, password string) model.User {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("生成密码哈希失败: %v", err)
	}
	user := model.User{Username: username, Role: role, PasswordHash: hash}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	return user
}

func setupAuthUserFixture(t *testing.T) authUserTestFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := openAuthUserHandlerTestDB(t)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("初始化用户表失败: %v", err)
	}

	adminPassword := "Admin#Pass2026"
	operatorPassword := "Operator#Pass2026"
	adminUser := seedAuthUser(t, db, "admin", "admin", adminPassword)
	operatorUser := seedAuthUser(t, db, "operator", "operator", operatorPassword)

	jwtManager := auth.NewJWTManager("test-secret", time.Hour)
	service := auth.NewService(db, jwtManager, auth.LoginSecurityConfig{
		FailLockThreshold: 5,
		FailLockDuration:  time.Minute,
	})

	authHandler := NewAuthHandler(service, jwtManager, false, false)
	userHandler := NewUserHandler(service)

	router := gin.New()
	secured := router.Group("")
	secured.Use(middleware.AuthMiddleware(jwtManager))
	secured.POST("/auth/change-password", authHandler.ChangePassword)
	secured.POST("/auth/logout", authHandler.Logout)
	secured.GET("/users", middleware.RBAC("users:manage"), userHandler.List)
	secured.POST("/users", middleware.RBAC("users:manage"), userHandler.Create)
	secured.PUT("/users/:id", middleware.RBAC("users:manage"), userHandler.Update)
	secured.DELETE("/users/:id", middleware.RBAC("users:manage"), userHandler.Delete)

	adminToken, err := jwtManager.GenerateToken(adminUser)
	if err != nil {
		t.Fatalf("生成 admin token 失败: %v", err)
	}
	operatorToken, err := jwtManager.GenerateToken(operatorUser)
	if err != nil {
		t.Fatalf("生成 operator token 失败: %v", err)
	}

	return authUserTestFixture{
		db:            db,
		service:       service,
		jwtManager:    jwtManager,
		router:        router,
		adminUser:     adminUser,
		operatorUser:  operatorUser,
		adminToken:    adminToken,
		operatorToken: operatorToken,
	}
}

func performJSONRequest(t *testing.T, router *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func TestAuthHandlerChangePasswordSuccess(t *testing.T) {
	fx := setupAuthUserFixture(t)

	resp := performJSONRequest(
		t,
		fx.router,
		http.MethodPost,
		"/auth/change-password",
		fx.adminToken,
		`{"current_password":"Admin#Pass2026","new_password":"Admin#Pass2026!"}`,
	)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	if _, err := fx.service.Login("admin", "Admin#Pass2026", "127.0.0.1"); err == nil {
		t.Fatalf("旧密码不应继续可用")
	}
	if _, err := fx.service.Login("admin", "Admin#Pass2026!", "127.0.0.1"); err != nil {
		t.Fatalf("新密码应可登录，实际错误: %v", err)
	}
}

func TestAuthHandlerLogoutRevokesToken(t *testing.T) {
	fx := setupAuthUserFixture(t)

	logoutResp := performJSONRequest(t, fx.router, http.MethodPost, "/auth/logout", fx.adminToken, `{}`)
	if logoutResp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，响应: %s", logoutResp.Code, logoutResp.Body.String())
	}

	listResp := performJSONRequest(t, fx.router, http.MethodGet, "/users", fx.adminToken, "")
	if listResp.Code != http.StatusUnauthorized {
		t.Fatalf("已注销 token 期望状态码 401，实际: %d，响应: %s", listResp.Code, listResp.Body.String())
	}
}

func TestUserHandlerCRUDAsAdmin(t *testing.T) {
	fx := setupAuthUserFixture(t)

	listResp := performJSONRequest(t, fx.router, http.MethodGet, "/users", fx.adminToken, "")
	if listResp.Code != http.StatusOK {
		t.Fatalf("列表接口期望 200，实际: %d，响应: %s", listResp.Code, listResp.Body.String())
	}

	createResp := performJSONRequest(
		t,
		fx.router,
		http.MethodPost,
		"/users",
		fx.adminToken,
		`{"username":"alice","password":"Alice#Pass2026","role":"operator"}`,
	)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("创建接口期望 201，实际: %d，响应: %s", createResp.Code, createResp.Body.String())
	}

	var createPayload struct {
		Data struct {
			ID       uint   `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createResp.Body.Bytes(), &createPayload); err != nil {
		t.Fatalf("解析创建响应失败: %v", err)
	}
	if createPayload.Data.Username != "alice" || createPayload.Data.Role != "operator" {
		t.Fatalf("创建结果不符合预期: %+v", createPayload.Data)
	}

	updateResp := performJSONRequest(
		t,
		fx.router,
		http.MethodPut,
		fmt.Sprintf("/users/%d", createPayload.Data.ID),
		fx.adminToken,
		`{"role":"viewer","password":"Alice#Pass2026!"}`,
	)
	if updateResp.Code != http.StatusOK {
		t.Fatalf("更新接口期望 200，实际: %d，响应: %s", updateResp.Code, updateResp.Body.String())
	}

	if _, err := fx.service.Login("alice", "Alice#Pass2026!", "127.0.0.1"); err != nil {
		t.Fatalf("更新后的密码应可登录，实际错误: %v", err)
	}

	deleteResp := performJSONRequest(
		t,
		fx.router,
		http.MethodDelete,
		fmt.Sprintf("/users/%d", createPayload.Data.ID),
		fx.adminToken,
		"",
	)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("删除接口期望 200，实际: %d，响应: %s", deleteResp.Code, deleteResp.Body.String())
	}

	var userCount int64
	if err := fx.db.Model(&model.User{}).Where("username = ?", "alice").Count(&userCount).Error; err != nil {
		t.Fatalf("统计用户失败: %v", err)
	}
	if userCount != 0 {
		t.Fatalf("期望用户已删除，剩余数量: %d", userCount)
	}
}

func TestUserHandlerForbiddenForNonAdmin(t *testing.T) {
	fx := setupAuthUserFixture(t)

	resp := performJSONRequest(t, fx.router, http.MethodGet, "/users", fx.operatorToken, "")
	if resp.Code != http.StatusForbidden {
		t.Fatalf("非 admin 访问期望 403，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
}
