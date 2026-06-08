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
	"github.com/pquerna/otp/totp"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ---------- fixture ----------

type authHandlerTestFixture struct {
	db         *gorm.DB
	service    *auth.Service
	jwtManager *auth.JWTManager
	router     *gin.Engine
	adminUser  model.User
	adminToken string
	adminPass  string
}

func openAuthHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "dGVzdC1rZXktZGF0YS1lbmNyeXB0aW9uLWtleS0zMmIh")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.LoginFailure{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	return db
}

func seedUser(t *testing.T, db *gorm.DB, username, role, password string) model.User {
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

func setupAuthHandlerFixture(t *testing.T) authHandlerTestFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := openAuthHandlerTestDB(t)

	adminPass := "FAKE_AdminPass2026!_FOR_TEST_ONLY"
	adminUser := seedUser(t, db, "admin", "admin", adminPass)

	jwtManager := auth.NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	service := auth.NewService(db, jwtManager, nil, auth.LoginSecurityConfig{
		FailLockThreshold: 5,
		FailLockDuration:  time.Minute,
	})
	authHandler := NewAuthHandler(service, jwtManager, nil).WithDB(db)

	router := gin.New()

	// 公开路由（无需认证）
	router.POST("/auth/login", authHandler.Login)
	router.POST("/auth/2fa/login", authHandler.TOTPLogin)

	// 认证路由
	secured := router.Group("")
	secured.Use(middleware.AuthMiddleware(jwtManager, db))
	secured.POST("/auth/logout", authHandler.Logout)
	secured.POST("/auth/change-password", authHandler.ChangePassword)
	secured.POST("/auth/2fa/setup", authHandler.TOTPSetup)
	secured.POST("/auth/2fa/verify", authHandler.TOTPVerify)
	secured.POST("/auth/2fa/disable", authHandler.TOTPDisable)
	secured.GET("/me", authHandler.Me)

	adminToken, err := jwtManager.GenerateToken(adminUser)
	if err != nil {
		t.Fatalf("生成 admin token 失败: %v", err)
	}

	return authHandlerTestFixture{
		db:         db,
		service:    service,
		jwtManager: jwtManager,
		router:     router,
		adminUser:  adminUser,
		adminToken: adminToken,
		adminPass:  adminPass,
	}
}

func jsonRequest(t *testing.T, router *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
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

// ---------- Login ----------

func TestLoginSuccess(t *testing.T) {
	fx := setupAuthHandlerFixture(t)

	resp := jsonRequest(t, fx.router, http.MethodPost, "/auth/login", "",
		fmt.Sprintf(`{"username":"admin","password":%q}`, fx.adminPass))
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var r struct {
		Code int `json:"code"`
		Data struct {
			Token string `json:"token"`
			User  struct {
				ID          uint   `json:"id"`
				Username    string `json:"username"`
				Role        string `json:"role"`
				TOTPEnabled bool   `json:"totp_enabled"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &r); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if r.Data.Token == "" {
		t.Fatalf("期望返回 token，实际为空")
	}
	if r.Data.User.Username != "admin" {
		t.Fatalf("期望用户名 admin，实际: %s", r.Data.User.Username)
	}
	if r.Data.User.Role != "admin" {
		t.Fatalf("期望角色 admin，实际: %s", r.Data.User.Role)
	}
}

func TestLoginMissingCredentials(t *testing.T) {
	fx := setupAuthHandlerFixture(t)

	resp := jsonRequest(t, fx.router, http.MethodPost, "/auth/login", "",
		`{"username":"","password":""}`)

	// gin binding "required" 会返回 400
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("期望状态码 400，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
}

func TestLoginWrongPassword(t *testing.T) {
	fx := setupAuthHandlerFixture(t)

	resp := jsonRequest(t, fx.router, http.MethodPost, "/auth/login", "",
		`{"username":"admin","password":"WrongPass123!"}`)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("期望状态码 401，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
}

// ---------- Logout ----------

func TestLogoutSuccess(t *testing.T) {
	fx := setupAuthHandlerFixture(t)

	logoutResp := jsonRequest(t, fx.router, http.MethodPost, "/auth/logout", fx.adminToken, `{}`)
	if logoutResp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，响应: %s", logoutResp.Code, logoutResp.Body.String())
	}

	// 用已注销的 token 再次访问应返回 401
	meResp := jsonRequest(t, fx.router, http.MethodGet, "/me", fx.adminToken, "")
	if meResp.Code != http.StatusUnauthorized {
		t.Fatalf("已注销 token 期望状态码 401，实际: %d，响应: %s", meResp.Code, meResp.Body.String())
	}
}

// ---------- ChangePassword ----------

func TestChangePasswordSuccess(t *testing.T) {
	fx := setupAuthHandlerFixture(t)

	newPass := "FAKE_NewAdminPass2026!_FOR_TEST_ONLY"
	resp := jsonRequest(t, fx.router, http.MethodPost, "/auth/change-password", fx.adminToken,
		fmt.Sprintf(`{"current_password":%q,"new_password":%q}`, fx.adminPass, newPass))
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	// 旧密码不应再可用
	if _, err := fx.service.Login("admin", fx.adminPass, "127.0.0.1"); err == nil {
		t.Fatalf("旧密码不应继续可用")
	}
	// 新密码应可登录
	if _, err := fx.service.Login("admin", newPass, "127.0.0.1"); err != nil {
		t.Fatalf("新密码应可登录，实际错误: %v", err)
	}
}

func TestChangePasswordWrongCurrent(t *testing.T) {
	fx := setupAuthHandlerFixture(t)

	resp := jsonRequest(t, fx.router, http.MethodPost, "/auth/change-password", fx.adminToken,
		`{"current_password":"WrongCurrentPass!","new_password":"NewPass123!"}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("期望状态码 400，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
}

// ---------- TOTP Setup ----------

func TestSetupTOTPSuccess(t *testing.T) {
	fx := setupAuthHandlerFixture(t)

	resp := jsonRequest(t, fx.router, http.MethodPost, "/auth/2fa/setup", fx.adminToken, "")
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var r struct {
		Data struct {
			Secret string `json:"secret"`
			QrURL  string `json:"qr_url"`
			Issuer string `json:"issuer"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &r); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if r.Data.Secret == "" {
		t.Fatalf("期望返回 secret")
	}
	if r.Data.QrURL == "" {
		t.Fatalf("期望返回 qr_url")
	}
	if r.Data.Issuer == "" {
		t.Fatalf("期望返回 issuer")
	}

	// 验证 DB 中已存储 pending secret
	var user model.User
	if err := fx.db.First(&user, fx.adminUser.ID).Error; err != nil {
		t.Fatalf("重新加载用户失败: %v", err)
	}
	if user.TOTPSecret == "" {
		t.Fatalf("期望 TOTPSecret 已存储")
	}
	if user.TOTPEnabled {
		t.Fatalf("setup 后 TOTPEnabled 仍应为 false")
	}
}

func TestSetupTOTPRejectsAlreadyEnabledUser(t *testing.T) {
	fx := setupAuthHandlerFixture(t)
	_ = jsonRequest(t, fx.router, http.MethodPost, "/auth/2fa/setup", fx.adminToken, "")

	var user model.User
	if err := fx.db.First(&user, fx.adminUser.ID).Error; err != nil {
		t.Fatalf("重新加载用户失败: %v", err)
	}
	code, err := totp.GenerateCode(user.TOTPSecret, time.Now())
	if err != nil {
		t.Fatalf("生成 TOTP 验证码失败: %v", err)
	}
	verifyResp := jsonRequest(t, fx.router, http.MethodPost, "/auth/2fa/verify", fx.adminToken, fmt.Sprintf(`{"code":%q}`, code))
	if verifyResp.Code != http.StatusOK {
		t.Fatalf("启用 TOTP 失败: %d %s", verifyResp.Code, verifyResp.Body.String())
	}
	if err := fx.db.First(&user, fx.adminUser.ID).Error; err != nil {
		t.Fatalf("重新加载启用后用户失败: %v", err)
	}
	activeSecret := user.TOTPSecret

	resp := jsonRequest(t, fx.router, http.MethodPost, "/auth/2fa/setup", fx.adminToken, "")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("已启用用户再次 setup 应返回 400，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	if err := fx.db.First(&user, fx.adminUser.ID).Error; err != nil {
		t.Fatalf("重新加载拒绝后用户失败: %v", err)
	}
	if user.TOTPSecret != activeSecret {
		t.Fatalf("拒绝重复 setup 后不应轮换 active secret")
	}
}

// ---------- TOTP Verify ----------

func TestVerifyTOTPSuccess(t *testing.T) {
	fx := setupAuthHandlerFixture(t)

	// 第一步：setup
	_ = jsonRequest(t, fx.router, http.MethodPost, "/auth/2fa/setup", fx.adminToken, "")

	// 读取 pending secret
	var user model.User
	if err := fx.db.First(&user, fx.adminUser.ID).Error; err != nil {
		t.Fatalf("重新加载用户失败: %v", err)
	}
	if user.TOTPSecret == "" {
		t.Fatalf("setup 后 TOTPSecret 不应为空")
	}

	// 从 secret 生成有效的 TOTP 验证码
	code, err := totp.GenerateCode(user.TOTPSecret, time.Now())
	if err != nil {
		t.Fatalf("生成 TOTP 验证码失败: %v", err)
	}

	// 第二步：verify
	body := fmt.Sprintf(`{"code":%q}`, code)
	resp := jsonRequest(t, fx.router, http.MethodPost, "/auth/2fa/verify", fx.adminToken, body)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	// 验证返回 recovery_codes
	var r struct {
		Data struct {
			RecoveryCodes []string `json:"recovery_codes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &r); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(r.Data.RecoveryCodes) == 0 {
		t.Fatalf("期望返回 recovery_codes")
	}

	// 验证 DB 中 TOTPEnabled 已设置
	if err := fx.db.First(&user, fx.adminUser.ID).Error; err != nil {
		t.Fatalf("重新加载用户失败: %v", err)
	}
	if !user.TOTPEnabled {
		t.Fatalf("verify 后 TOTPEnabled 应为 true")
	}
	if user.RecoveryCodes == "" {
		t.Fatalf("verify 后 RecoveryCodes 不应为空")
	}
}

func TestVerifyTOTPWrongCode(t *testing.T) {
	fx := setupAuthHandlerFixture(t)

	// 先 setup
	_ = jsonRequest(t, fx.router, http.MethodPost, "/auth/2fa/setup", fx.adminToken, "")

	// 用错误验证码 verify
	resp := jsonRequest(t, fx.router, http.MethodPost, "/auth/2fa/verify", fx.adminToken,
		`{"code":"000000"}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("期望状态码 400，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var user model.User
	fx.db.First(&user, fx.adminUser.ID)
	if user.TOTPEnabled {
		t.Fatalf("错误验证码不应启用 TOTP")
	}
}

func TestVerifyTOTPNoSetup(t *testing.T) {
	fx := setupAuthHandlerFixture(t)

	// 不调用 setup，直接 verify
	resp := jsonRequest(t, fx.router, http.MethodPost, "/auth/2fa/verify", fx.adminToken,
		`{"code":"123456"}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("期望状态码 400，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
}
