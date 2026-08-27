package handlers

import (
	"context"
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
	"xirang/backend/internal/settings"

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
	handler    *AuthHandler
	router     *gin.Engine
	adminUser  model.User
	adminToken string
	adminPass  string
}

func openAuthHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "dGVzdC1rZXktZGF0YS1lbmNyeXB0aW9uLWtleS0zMmIh")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", handlerTestDBName(t))
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
		handler:    authHandler,
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

func TestLogoutRevokesJWTBeforeContentSessionAndIgnoresSafeReconcileFailure(t *testing.T) {
	fx := setupAuthHandlerFixture(t)
	claims, err := fx.jwtManager.ParseToken(fx.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	revoker := &contentSessionRevokerFake{
		jwt: fx.jwtManager, expectedJTI: claims.ID,
		err: fmt.Errorf("FAKE_REVOKER_ERROR_WITH_ID_FOR_TEST_ONLY:%s", claims.ID),
	}
	fx.handler.WithContentSessionRevoker(revoker)

	response := jsonRequest(t, fx.router, http.MethodPost, "/auth/logout", fx.adminToken, `{}`)
	if response.Code != http.StatusOK || revoker.calls != 1 || revoker.jti != claims.ID || revoker.reason != "logout" || !revoker.jwtWasRevoked {
		t.Fatalf("status=%d revoker=%+v body=%s", response.Code, revoker, response.Body.String())
	}
	if strings.Contains(response.Body.String(), claims.ID) || strings.Contains(response.Body.String(), "FAKE_REVOKER_ERROR") {
		t.Fatalf("logout response leaked revoker state: %s", response.Body.String())
	}
	if me := jsonRequest(t, fx.router, http.MethodGet, "/me", fx.adminToken, ""); me.Code != http.StatusUnauthorized {
		t.Fatalf("content revoker failure reauthorized JWT: %d %s", me.Code, me.Body.String())
	}
}

type contentSessionRevokerFake struct {
	jwt           *auth.JWTManager
	expectedJTI   string
	calls         int
	jti           string
	reason        string
	jwtWasRevoked bool
	err           error
}

func (fake *contentSessionRevokerFake) RevokeSession(_ context.Context, jti, reason string) error {
	fake.calls++
	fake.jti, fake.reason = jti, reason
	revoked, err := fake.jwt.IsSessionRevoked(fake.expectedJTI)
	fake.jwtWasRevoked = err == nil && revoked
	return fake.err
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

// ---------- Primary captcha fail-closed ----------

func TestLoginPrimaryCaptchaRejectsWhenStoreMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openAuthHandlerTestDB(t)
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatalf("初始化 system_settings 失败: %v", err)
	}

	adminPass := "FAKE_AdminPass2026!_FOR_TEST_ONLY"
	_ = seedUser(t, db, "admin", "admin", adminPass)

	jwtManager := auth.NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	service := auth.NewService(db, jwtManager, nil, auth.LoginSecurityConfig{
		FailLockThreshold: 5,
		FailLockDuration:  time.Minute,
	})
	settingsSvc := settings.NewService(db)
	if err := settingsSvc.Update("login.captcha_enabled", "true"); err != nil {
		t.Fatalf("启用验证码失败: %v", err)
	}
	// Intentionally no CaptchaStore — must fail closed (legacy free-form captcha
	// string must never authenticate).
	authHandler := NewAuthHandler(service, jwtManager, settingsSvc).WithDB(db)

	router := gin.New()
	router.POST("/auth/login", authHandler.Login)

	resp := jsonRequest(t, router, http.MethodPost, "/auth/login", "",
		fmt.Sprintf(`{"username":"admin","password":%q,"captcha":"anything-non-empty"}`, adminPass))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("CaptchaStore 未注入时期望 400，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "验证码") {
		t.Fatalf("期望验证码不可用提示，实际: %s", resp.Body.String())
	}
}

func TestLoginPrimaryCaptchaRejectsLegacyFreeFormWhenStorePresent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openAuthHandlerTestDB(t)
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatalf("初始化 system_settings 失败: %v", err)
	}

	adminPass := "FAKE_AdminPass2026!_FOR_TEST_ONLY"
	_ = seedUser(t, db, "admin", "admin", adminPass)

	jwtManager := auth.NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	service := auth.NewService(db, jwtManager, nil, auth.LoginSecurityConfig{
		FailLockThreshold: 5,
		FailLockDuration:  time.Minute,
	})
	settingsSvc := settings.NewService(db)
	if err := settingsSvc.Update("login.captcha_enabled", "true"); err != nil {
		t.Fatalf("启用验证码失败: %v", err)
	}
	store := NewCaptchaStore()
	authHandler := NewAuthHandler(service, jwtManager, settingsSvc).WithDB(db).WithCaptchaStore(store)

	router := gin.New()
	router.POST("/auth/login", authHandler.Login)

	// Free-form captcha without captcha_id/answer must fail.
	resp := jsonRequest(t, router, http.MethodPost, "/auth/login", "",
		fmt.Sprintf(`{"username":"admin","password":%q,"captcha":"12"}`, adminPass))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("legacy captcha 期望 400，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
}

// ---------- Second captcha (login.second_captcha_enabled) ----------

func TestLoginSecondCaptchaRejectsLegacyFreeFormOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openAuthHandlerTestDB(t)
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatalf("初始化 system_settings 失败: %v", err)
	}

	adminPass := "FAKE_AdminPass2026!_FOR_TEST_ONLY"
	adminUser := seedUser(t, db, "admin", "admin", adminPass)
	_ = adminUser

	jwtManager := auth.NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	service := auth.NewService(db, jwtManager, nil, auth.LoginSecurityConfig{
		FailLockThreshold: 5,
		FailLockDuration:  time.Minute,
	})
	settingsSvc := settings.NewService(db)
	if err := settingsSvc.Update("login.second_captcha_enabled", "true"); err != nil {
		t.Fatalf("启用二次验证码失败: %v", err)
	}
	store := NewCaptchaStore()
	authHandler := NewAuthHandler(service, jwtManager, settingsSvc).WithDB(db).WithCaptchaStore(store)

	router := gin.New()
	router.POST("/auth/login", authHandler.Login)

	// legacy free-form second_captcha alone must NOT satisfy the gate
	resp := jsonRequest(t, router, http.MethodPost, "/auth/login", "",
		fmt.Sprintf(`{"username":"admin","password":%q,"second_captcha":"anything"}`, adminPass))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("仅 legacy second_captcha 期望 400，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "二次验证码") {
		t.Fatalf("期望二次验证码错误提示，实际: %s", resp.Body.String())
	}
}

func TestLoginSecondCaptchaRequiresStoreBackedChallenge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openAuthHandlerTestDB(t)
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatalf("初始化 system_settings 失败: %v", err)
	}

	adminPass := "FAKE_AdminPass2026!_FOR_TEST_ONLY"
	_ = seedUser(t, db, "admin", "admin", adminPass)

	jwtManager := auth.NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	service := auth.NewService(db, jwtManager, nil, auth.LoginSecurityConfig{
		FailLockThreshold: 5,
		FailLockDuration:  time.Minute,
	})
	settingsSvc := settings.NewService(db)
	if err := settingsSvc.Update("login.second_captcha_enabled", "true"); err != nil {
		t.Fatalf("启用二次验证码失败: %v", err)
	}
	store := NewCaptchaStore()
	store.Set("second-ok", 9)
	authHandler := NewAuthHandler(service, jwtManager, settingsSvc).WithDB(db).WithCaptchaStore(store)

	router := gin.New()
	router.POST("/auth/login", authHandler.Login)

	// wrong second answer
	resp := jsonRequest(t, router, http.MethodPost, "/auth/login", "",
		fmt.Sprintf(`{"username":"admin","password":%q,"second_captcha_id":"second-ok","second_captcha_answer":"1"}`, adminPass))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("错误二次验证码期望 400，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	// re-seed after consume-on-fail
	store.Set("second-ok", 9)
	resp = jsonRequest(t, router, http.MethodPost, "/auth/login", "",
		fmt.Sprintf(`{"username":"admin","password":%q,"second_captcha_id":"second-ok","second_captcha_answer":"9"}`, adminPass))
	if resp.Code != http.StatusOK {
		t.Fatalf("正确二次验证码期望 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
}
