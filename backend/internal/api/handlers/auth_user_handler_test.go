package handlers

import (
	"encoding/json"
	"errors"
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
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
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

	adminPassword := "FAKE_AdminPass2026!_FOR_TEST_ONLY"
	operatorPassword := "FAKE_OperatorPass2026!_FOR_TEST_ONLY"
	adminUser := seedAuthUser(t, db, "admin", "admin", adminPassword)
	operatorUser := seedAuthUser(t, db, "operator", "operator", operatorPassword)

	jwtManager := auth.NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	service := auth.NewService(db, jwtManager, nil, auth.LoginSecurityConfig{
		FailLockThreshold: 5,
		FailLockDuration:  time.Minute,
	})

	authHandler := NewAuthHandler(service, jwtManager, nil)
	userHandler := NewUserHandler(service)

	router := gin.New()
	secured := router.Group("")
	secured.Use(middleware.AuthMiddleware(jwtManager, db))
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
		`{"current_password":"FAKE_AdminPass2026!_FOR_TEST_ONLY","new_password":"FAKE_NewAdminPass2026!_FOR_TEST_ONLY"}`,
	)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	if _, err := fx.service.Login("admin", "FAKE_AdminPass2026!_FOR_TEST_ONLY", "127.0.0.1"); err == nil {
		t.Fatalf("旧密码不应继续可用")
	}
	if _, err := fx.service.Login("admin", "FAKE_NewAdminPass2026!_FOR_TEST_ONLY", "127.0.0.1"); err != nil {
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
		`{"username":"alice","password":"FAKE_AlicePass2026!_FOR_TEST_ONLY","role":"operator"}`,
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
		`{"role":"viewer","password":"FAKE_NewAlicePass2026!_FOR_TEST_ONLY"}`,
	)
	if updateResp.Code != http.StatusOK {
		t.Fatalf("更新接口期望 200，实际: %d，响应: %s", updateResp.Code, updateResp.Body.String())
	}

	if _, err := fx.service.Login("alice", "FAKE_NewAlicePass2026!_FOR_TEST_ONLY", "127.0.0.1"); err != nil {
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

func TestAuthHandlerTOTPLoginConsumesRecoveryCodeBeforeIssuingToken(t *testing.T) {
	fx := setupAuthUserFixture(t)
	authHandler := NewAuthHandler(fx.service, fx.jwtManager, nil).WithDB(fx.db)
	router := gin.New()
	router.POST("/auth/2fa/login", authHandler.TOTPLogin)

	recoveryCodes := []string{
		"FAKE_RECOVERY_CODE_ONE_FOR_TEST_ONLY",
		"FAKE_RECOVERY_CODE_TWO_FOR_TEST_ONLY",
	}
	recoveryJSON, err := json.Marshal(recoveryCodes)
	if err != nil {
		t.Fatalf("序列化恢复码失败: %v", err)
	}
	fx.adminUser.TOTPEnabled = true
	fx.adminUser.TOTPSecret = "FAKE_TOTP_SECRET_FOR_TEST_ONLY"
	fx.adminUser.RecoveryCodes = string(recoveryJSON)
	if err := fx.db.Save(&fx.adminUser).Error; err != nil {
		t.Fatalf("保存 2FA 用户失败: %v", err)
	}
	loginToken, err := fx.jwtManager.Generate2FAPendingToken(fx.adminUser)
	if err != nil {
		t.Fatalf("生成 2FA 中间 token 失败: %v", err)
	}

	resp := performJSONRequest(
		t,
		router,
		http.MethodPost,
		"/auth/2fa/login",
		"",
		fmt.Sprintf(`{"login_token":%q,"totp_code":"FAKE_RECOVERY_CODE_ONE_FOR_TEST_ONLY"}`, loginToken),
	)
	if resp.Code != http.StatusOK {
		t.Fatalf("恢复码登录期望 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var user model.User
	if err := fx.db.First(&user, fx.adminUser.ID).Error; err != nil {
		t.Fatalf("重新加载用户失败: %v", err)
	}
	var remaining []string
	if err := json.Unmarshal([]byte(user.RecoveryCodes), &remaining); err != nil {
		t.Fatalf("解析剩余恢复码失败: %v", err)
	}
	if len(remaining) != 1 || remaining[0] != "FAKE_RECOVERY_CODE_TWO_FOR_TEST_ONLY" {
		t.Fatalf("期望只保留未使用恢复码，实际: %v", remaining)
	}
}

func TestAuthHandlerTOTPLoginRejectsWhenRecoveryCodeCannotBeSaved(t *testing.T) {
	db := openAuthUserHandlerTestDB(t)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("初始化用户表失败: %v", err)
	}
	user := seedAuthUser(t, db, "admin", "admin", "FAKE_AdminPass2026!_FOR_TEST_ONLY")
	recoveryCodes := []string{"FAKE_RECOVERY_CODE_FOR_TEST_ONLY"}
	recoveryJSON, err := json.Marshal(recoveryCodes)
	if err != nil {
		t.Fatalf("序列化恢复码失败: %v", err)
	}
	user.TOTPEnabled = true
	user.TOTPSecret = "FAKE_TOTP_SECRET_FOR_TEST_ONLY"
	user.RecoveryCodes = string(recoveryJSON)
	if err := db.Save(&user).Error; err != nil {
		t.Fatalf("保存 2FA 用户失败: %v", err)
	}

	jwtManager := auth.NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	service := auth.NewService(db, jwtManager, nil, auth.LoginSecurityConfig{
		FailLockThreshold: 5,
		FailLockDuration:  time.Minute,
	})
	authHandler := NewAuthHandler(service, jwtManager, nil).WithDB(db)
	router := gin.New()
	router.POST("/auth/2fa/login", authHandler.TOTPLogin)
	loginToken, err := jwtManager.Generate2FAPendingToken(user)
	if err != nil {
		t.Fatalf("生成 2FA 中间 token 失败: %v", err)
	}

	if err := db.Callback().Update().Before("gorm:update").Register("xirang:test_recovery_code_save_error", func(tx *gorm.DB) {
		if err := tx.AddError(errors.New("forced recovery code save failure")); err != nil {
			tx.Logger.Error(tx.Statement.Context, "failed to add forced recovery code save failure: %v", err)
		}
	}); err != nil {
		t.Fatalf("注册保存失败回调失败: %v", err)
	}

	resp := performJSONRequest(
		t,
		router,
		http.MethodPost,
		"/auth/2fa/login",
		"",
		fmt.Sprintf(`{"login_token":%q,"totp_code":"FAKE_RECOVERY_CODE_FOR_TEST_ONLY"}`, loginToken),
	)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("恢复码保存失败时应返回 500 且不签发 token，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
}
