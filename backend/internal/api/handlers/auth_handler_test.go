package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
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

type authHandlerWriteFailureResponseWriter struct {
	gin.ResponseWriter
	writeAttempts int
}

func (w *authHandlerWriteFailureResponseWriter) Write(data []byte) (int, error) {
	w.WriteHeaderNow()
	w.writeAttempts++
	return 0, fmt.Errorf("FAKE_RESPONSE_WRITE_FAILURE_FOR_TEST_ONLY")
}

func (w *authHandlerWriteFailureResponseWriter) WriteString(data string) (int, error) {
	w.WriteHeaderNow()
	w.writeAttempts++
	return 0, fmt.Errorf("FAKE_RESPONSE_WRITE_FAILURE_FOR_TEST_ONLY")
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
	if err := db.AutoMigrate(&model.User{}, &model.LoginFailure{}, &model.PendingAuthToken{}, &model.TokenRevocation{}); err != nil {
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
	return setupAuthHandlerFixtureWithDB(t, openAuthHandlerTestDB(t))
}

func setupAuthHandlerPostgresFixture(t *testing.T) authHandlerTestFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "dGVzdC1rZXktZGF0YS1lbmNyeXB0aW9uLWtleS0zMmIh")
	t.Cleanup(secure.ResetForTesting)
	secure.ResetForTesting()
	db, _ := openConfigHandlerTestDBPairForEngine(t, "postgres")
	if err := db.AutoMigrate(&model.User{}, &model.LoginFailure{}, &model.PendingAuthToken{}, &model.TokenRevocation{}); err != nil {
		t.Fatalf("初始化 PostgreSQL 测试数据表失败: %v", err)
	}
	return setupAuthHandlerFixtureWithDB(t, db)
}

func setupAuthHandlerFixtureWithDB(t *testing.T, db *gorm.DB) authHandlerTestFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)

	adminPass := "FAKE_AdminPass2026!_FOR_TEST_ONLY"
	adminUser := seedUser(t, db, "admin", "admin", adminPass)

	jwtManager := auth.NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	jwtManager.SetDB(db)
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
	logout := router.Group("")
	logout.Use(middleware.LogoutMiddleware(jwtManager))
	logout.POST("/auth/logout", authHandler.Logout)

	secured := router.Group("")
	secured.Use(middleware.AuthMiddleware(jwtManager, db))
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

func TestLogoutIsIdempotentForAlreadyRevokedSession(t *testing.T) {
	fx := setupAuthHandlerFixture(t)
	for attempt := 1; attempt <= 2; attempt++ {
		response := jsonRequest(t, fx.router, http.MethodPost, "/auth/logout", fx.adminToken, `{}`)
		if response.Code != http.StatusOK {
			t.Fatalf("logout attempt %d status=%d body=%s", attempt, response.Code, response.Body.String())
		}
	}
}

func TestLogoutIgnoresArbitraryBodyTargetAndRevokesOnlyBearerSession(t *testing.T) {
	fx := setupAuthHandlerFixture(t)
	other := seedUser(t, fx.db, "other-admin", "admin", "FAKE_OtherPass2026!_FOR_TEST_ONLY")
	otherToken, err := fx.jwtManager.GenerateToken(other)
	if err != nil {
		t.Fatalf("generate other session token: %v", err)
	}

	response := jsonRequest(t, fx.router, http.MethodPost, "/auth/logout", fx.adminToken,
		fmt.Sprintf(`{"token":%q,"jti":%q,"user_id":%d}`, otherToken, strings.Repeat("f", 32), other.ID))
	if response.Code != http.StatusOK {
		t.Fatalf("logout with arbitrary body target status=%d body=%s", response.Code, response.Body.String())
	}
	if otherResponse := jsonRequest(t, fx.router, http.MethodGet, "/me", otherToken, ""); otherResponse.Code != http.StatusOK {
		t.Fatalf("body target unexpectedly revoked another session: %d %s", otherResponse.Code, otherResponse.Body.String())
	}
	if ownResponse := jsonRequest(t, fx.router, http.MethodGet, "/me", fx.adminToken, ""); ownResponse.Code != http.StatusUnauthorized {
		t.Fatalf("bearer session was not revoked: %d %s", ownResponse.Code, ownResponse.Body.String())
	}
}

func TestLogoutAfterAccountDeletionStillRevokesSignedSession(t *testing.T) {
	fx := setupAuthHandlerFixture(t)
	claims, err := fx.jwtManager.ParseToken(fx.adminToken)
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	if err := fx.db.Delete(&model.User{}, fx.adminUser.ID).Error; err != nil {
		t.Fatalf("delete account: %v", err)
	}

	response := jsonRequest(t, fx.router, http.MethodPost, "/auth/logout", fx.adminToken, `{}`)
	if response.Code != http.StatusOK {
		t.Fatalf("logout after account deletion status=%d body=%s", response.Code, response.Body.String())
	}
	revoked, err := fx.jwtManager.IsSessionRevoked(claims.ID)
	if err != nil || !revoked {
		t.Fatalf("deleted-account session revocation=%v err=%v", revoked, err)
	}
}

func TestLogoutRejectsMissingSessionBinding(t *testing.T) {
	fx := setupAuthHandlerFixture(t)
	router := gin.New()
	router.POST("/auth/logout", fx.handler.Logout)
	response := jsonRequest(t, router, http.MethodPost, "/auth/logout", "", `{}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("missing session binding status=%d body=%s", response.Code, response.Body.String())
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
			Secret       string    `json:"secret"`
			QrURL        string    `json:"qr_url"`
			Issuer       string    `json:"issuer"`
			EnrollmentID string    `json:"enrollment_id"`
			ExpiresAt    time.Time `json:"expires_at"`
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
	if r.Data.EnrollmentID == "" {
		t.Fatalf("期望返回 enrollment_id")
	}
	if r.Data.ExpiresAt.IsZero() || !r.Data.ExpiresAt.After(time.Now()) {
		t.Fatalf("期望返回未来 expires_at")
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
	setupResp := jsonRequest(t, fx.router, http.MethodPost, "/auth/2fa/setup", fx.adminToken, "")
	if setupResp.Code != http.StatusOK {
		t.Fatalf("setup 失败: %d %s", setupResp.Code, setupResp.Body.String())
	}
	var setup struct {
		Data struct {
			EnrollmentID string `json:"enrollment_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(setupResp.Body.Bytes(), &setup); err != nil {
		t.Fatalf("解析 setup 响应失败: %v", err)
	}
	if setup.Data.EnrollmentID == "" {
		t.Fatalf("setup 未返回 enrollment_id")
	}

	var user model.User
	if err := fx.db.First(&user, fx.adminUser.ID).Error; err != nil {
		t.Fatalf("重新加载用户失败: %v", err)
	}
	code, err := totp.GenerateCode(user.TOTPSecret, time.Now())
	if err != nil {
		t.Fatalf("生成 TOTP 验证码失败: %v", err)
	}
	verifyResp := jsonRequest(t, fx.router, http.MethodPost, "/auth/2fa/verify", fx.adminToken, fmt.Sprintf(`{"code":%q,"enrollment_id":%q}`, code, setup.Data.EnrollmentID))
	if verifyResp.Code != http.StatusOK {
		t.Fatalf("启用 TOTP 失败: %d %s", verifyResp.Code, verifyResp.Body.String())
	}
	if err := fx.db.First(&user, fx.adminUser.ID).Error; err != nil {
		t.Fatalf("重新加载启用后用户失败: %v", err)
	}
	activeSecret := user.TOTPSecret
	freshToken, err := fx.jwtManager.GenerateToken(user)
	if err != nil {
		t.Fatalf("生成刷新后的 admin token 失败: %v", err)
	}

	resp := jsonRequest(t, fx.router, http.MethodPost, "/auth/2fa/setup", freshToken, "")
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
	setupResp := jsonRequest(t, fx.router, http.MethodPost, "/auth/2fa/setup", fx.adminToken, "")
	if setupResp.Code != http.StatusOK {
		t.Fatalf("setup 失败: %d %s", setupResp.Code, setupResp.Body.String())
	}
	var setup struct {
		Data struct {
			EnrollmentID string `json:"enrollment_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(setupResp.Body.Bytes(), &setup); err != nil {
		t.Fatalf("解析 setup 响应失败: %v", err)
	}
	if setup.Data.EnrollmentID == "" {
		t.Fatalf("setup 未返回 enrollment_id")
	}

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
	body := fmt.Sprintf(`{"code":%q,"enrollment_id":%q}`, code, setup.Data.EnrollmentID)
	resp := jsonRequest(t, fx.router, http.MethodPost, "/auth/2fa/verify", fx.adminToken, body)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	// 验证返回 replacement token、safe user 和 recovery_codes。
	var r struct {
		Data struct {
			Token string `json:"token"`
			User  struct {
				ID          uint   `json:"id"`
				Username    string `json:"username"`
				Role        string `json:"role"`
				TOTPEnabled bool   `json:"totp_enabled"`
			} `json:"user"`
			RecoveryCodes []string `json:"recovery_codes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &r); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if r.Data.Token == "" {
		t.Fatalf("期望返回 replacement token")
	}
	if r.Data.User.ID != fx.adminUser.ID || r.Data.User.Username != "admin" ||
		r.Data.User.Role != "admin" || !r.Data.User.TOTPEnabled {
		t.Fatalf("激活响应用户不安全或不完整: %+v", r.Data.User)
	}
	if len(r.Data.RecoveryCodes) == 0 {
		t.Fatalf("期望返回 recovery_codes")
	}
	cacheControl := resp.Header().Get("Cache-Control")
	if !strings.Contains(cacheControl, "private") || !strings.Contains(cacheControl, "no-store") {
		t.Fatalf("TOTP verify response must not be cached: %q", cacheControl)
	}

	oldClaims, err := fx.jwtManager.ParseToken(fx.adminToken)
	if err != nil {
		t.Fatalf("解析旧 token 失败: %v", err)
	}
	newClaims, err := fx.jwtManager.ParseToken(r.Data.Token)
	if err != nil {
		t.Fatalf("解析 replacement token 失败: %v", err)
	}
	if oldClaims.ID != newClaims.ID || oldClaims.ExpiresAt == nil || newClaims.ExpiresAt == nil ||
		!oldClaims.ExpiresAt.Equal(newClaims.ExpiresAt.Time) {
		t.Fatalf("replacement token must preserve session JTI/expiry: old=%+v new=%+v", oldClaims, newClaims)
	}
	if newClaims.TokenVersion != oldClaims.TokenVersion+1 {
		t.Fatalf("replacement token version=%d, want %d", newClaims.TokenVersion, oldClaims.TokenVersion+1)
	}
	if newClaims.Purpose != "" || newClaims.SessionID != "" || newClaims.TOTPBinding != "" ||
		newClaims.StepUpAction != "" {
		t.Fatalf("replacement token must be an ordinary primary token: %+v", newClaims)
	}

	// token_version invalidates the old bearer while the same session JTI keeps
	// the replacement token valid.
	if oldResp := jsonRequest(t, fx.router, http.MethodGet, "/me", fx.adminToken, ""); oldResp.Code != http.StatusUnauthorized {
		t.Fatalf("old token should be invalid after activation: %d %s", oldResp.Code, oldResp.Body.String())
	}
	if newResp := jsonRequest(t, fx.router, http.MethodGet, "/me", r.Data.Token, ""); newResp.Code != http.StatusOK {
		t.Fatalf("replacement token should authenticate: %d %s", newResp.Code, newResp.Body.String())
	}
	logoutResp := jsonRequest(t, fx.router, http.MethodPost, "/auth/logout", r.Data.Token, `{}`)
	if logoutResp.Code != http.StatusOK {
		t.Fatalf("logout after committed TOTP activation failed: %d %s", logoutResp.Code, logoutResp.Body.String())
	}
	if revokedResp := jsonRequest(t, fx.router, http.MethodGet, "/me", r.Data.Token, ""); revokedResp.Code != http.StatusUnauthorized {
		t.Fatalf("logout must revoke the replacement token sharing the activation JTI: %d %s", revokedResp.Code, revokedResp.Body.String())
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

func TestTOTPActivationLogoutBeforeCommitHTTPPostgres(t *testing.T) {
	fx := setupAuthHandlerPostgresFixture(t)
	serveJSON := func(router *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if strings.TrimSpace(token) != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		return resp
	}

	setupResp := serveJSON(fx.router, http.MethodPost, "/auth/2fa/setup", fx.adminToken, "")
	if setupResp.Code != http.StatusOK {
		t.Fatalf("setup failed: %d %s", setupResp.Code, setupResp.Body.String())
	}
	var setup struct {
		Data struct {
			EnrollmentID string `json:"enrollment_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(setupResp.Body.Bytes(), &setup); err != nil {
		t.Fatalf("decode setup response: %v", err)
	}
	if setup.Data.EnrollmentID == "" {
		t.Fatal("setup did not return enrollment_id")
	}
	var pending model.User
	if err := fx.db.First(&pending, fx.adminUser.ID).Error; err != nil {
		t.Fatalf("load pending user: %v", err)
	}
	code, err := totp.GenerateCode(pending.TOTPSecret, time.Now())
	if err != nil {
		t.Fatalf("generate TOTP code: %v", err)
	}
	oldClaims, err := fx.jwtManager.ParseToken(fx.adminToken)
	if err != nil || oldClaims.ExpiresAt == nil {
		t.Fatalf("parse old token: claims=%+v err=%v", oldClaims, err)
	}

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseBarrier := func() {
		releaseOnce.Do(func() { close(release) })
	}
	const callbackName = "xirang:test_auth_totp_http_activation_barrier"
	if err := fx.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != "users" {
			return
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}); err != nil {
		t.Fatalf("register activation barrier: %v", err)
	}
	t.Cleanup(func() {
		releaseBarrier()
		if err := fx.db.Callback().Update().Remove(callbackName); err != nil {
			t.Errorf("remove activation barrier: %v", err)
		}
	})

	activationDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		activationDone <- serveJSON(fx.router, http.MethodPost, "/auth/2fa/verify", fx.adminToken,
			fmt.Sprintf(`{"code":%q,"enrollment_id":%q}`, code, setup.Data.EnrollmentID))
	}()
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-waitCtx.Done():
		releaseBarrier()
		t.Fatalf("activation did not reach the pre-commit barrier: %v", waitCtx.Err())
	}

	logoutDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		logoutDone <- serveJSON(fx.router, http.MethodPost, "/auth/logout", fx.adminToken, `{}`)
	}()
	var logoutResp *httptest.ResponseRecorder
	select {
	case logoutResp = <-logoutDone:
	case <-waitCtx.Done():
		releaseBarrier()
		t.Fatalf("logout did not complete before activation release: %v", waitCtx.Err())
	}
	if logoutResp.Code != http.StatusOK {
		releaseBarrier()
		t.Fatalf("logout before activation commit failed: %d %s", logoutResp.Code, logoutResp.Body.String())
	}
	releaseBarrier()

	var activationResp *httptest.ResponseRecorder
	select {
	case activationResp = <-activationDone:
	case <-waitCtx.Done():
		t.Fatalf("activation did not finish after releasing the barrier: %v", waitCtx.Err())
	}
	if activationResp.Code != http.StatusOK {
		t.Fatalf("activation after logout commit failed: %d %s", activationResp.Code, activationResp.Body.String())
	}
	var verify struct {
		Data struct {
			Token         string   `json:"token"`
			RecoveryCodes []string `json:"recovery_codes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(activationResp.Body.Bytes(), &verify); err != nil {
		t.Fatalf("decode activation response: %v", err)
	}
	if verify.Data.Token == "" || len(verify.Data.RecoveryCodes) == 0 {
		t.Fatalf("activation response missing replacement token or recovery codes: %+v", verify.Data)
	}

	replacementManager := auth.NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	replacementClaims, err := replacementManager.ParseToken(verify.Data.Token)
	if err != nil || replacementClaims.ExpiresAt == nil ||
		replacementClaims.ID != oldClaims.ID ||
		!replacementClaims.ExpiresAt.Equal(oldClaims.ExpiresAt.Time) ||
		replacementClaims.TokenVersion != oldClaims.TokenVersion+1 {
		t.Fatalf("replacement claims changed across HTTP ordering: old=%+v replacement=%+v err=%v", oldClaims, replacementClaims, err)
	}
	if _, err := fx.jwtManager.ParseToken(verify.Data.Token); err == nil {
		t.Fatal("HTTP activation returned a replacement that resurrected the logged-out JTI")
	}
	if oldResp := serveJSON(fx.router, http.MethodGet, "/me", fx.adminToken, ""); oldResp.Code != http.StatusUnauthorized {
		t.Fatalf("old token should be invalid after activation/logout: %d %s", oldResp.Code, oldResp.Body.String())
	}
	if replacementResp := serveJSON(fx.router, http.MethodGet, "/me", verify.Data.Token, ""); replacementResp.Code != http.StatusUnauthorized {
		t.Fatalf("replacement token should remain revoked after activation: %d %s", replacementResp.Code, replacementResp.Body.String())
	}

	var committed model.User
	if err := fx.db.First(&committed, fx.adminUser.ID).Error; err != nil {
		t.Fatalf("load committed user: %v", err)
	}
	if !committed.TOTPEnabled || committed.TokenVersion != oldClaims.TokenVersion+1 {
		t.Fatalf("activation did not commit exactly once: user=%+v", committed)
	}
	var revocation model.TokenRevocation
	if err := fx.db.Where("token_hash = ?", "jti:"+oldClaims.ID).First(&revocation).Error; err != nil {
		t.Fatalf("load durable logout revocation: %v", err)
	}
	if !revocation.ExpiresAt.Equal(oldClaims.ExpiresAt.Time) {
		t.Fatalf("HTTP logout extended the session expiry: got=%s want=%s", revocation.ExpiresAt, oldClaims.ExpiresAt.Time)
	}
}

func TestTOTPVerifyCommitsBeforeResponseWriterFailure(t *testing.T) {
	fx := setupAuthHandlerFixture(t)

	setupResp := jsonRequest(t, fx.router, http.MethodPost, "/auth/2fa/setup", fx.adminToken, "")
	if setupResp.Code != http.StatusOK {
		t.Fatalf("setup 失败: %d %s", setupResp.Code, setupResp.Body.String())
	}
	var setup struct {
		Data struct {
			EnrollmentID string `json:"enrollment_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(setupResp.Body.Bytes(), &setup); err != nil {
		t.Fatalf("解析 setup 响应失败: %v", err)
	}
	if setup.Data.EnrollmentID == "" {
		t.Fatalf("setup 未返回 enrollment_id")
	}

	var pending model.User
	if err := fx.db.First(&pending, fx.adminUser.ID).Error; err != nil {
		t.Fatalf("重新加载 pending 用户失败: %v", err)
	}
	code, err := totp.GenerateCode(pending.TOTPSecret, time.Now())
	if err != nil {
		t.Fatalf("生成 TOTP 验证码失败: %v", err)
	}
	oldClaims, err := fx.jwtManager.ParseToken(fx.adminToken)
	if err != nil {
		t.Fatalf("解析旧 token 失败: %v", err)
	}

	failureWriter := &authHandlerWriteFailureResponseWriter{}
	failureRouter := gin.New()
	secured := failureRouter.Group("")
	secured.Use(middleware.AuthMiddleware(fx.jwtManager, fx.db))
	secured.POST("/auth/2fa/verify", func(c *gin.Context) {
		failureWriter.ResponseWriter = c.Writer
		c.Writer = failureWriter
		fx.handler.TOTPVerify(c)
	})

	response := jsonRequest(t, failureRouter, http.MethodPost, "/auth/2fa/verify", fx.adminToken,
		fmt.Sprintf(`{"code":%q,"enrollment_id":%q}`, code, setup.Data.EnrollmentID))
	if failureWriter.writeAttempts == 0 {
		t.Fatal("TOTP verify did not reach the failing response writer")
	}
	if failureWriter.writeAttempts != 1 {
		t.Fatalf("TOTP verify attempted to write/retry the response %d times", failureWriter.writeAttempts)
	}
	if body := response.Body.String(); body != "" {
		t.Fatalf("caller unexpectedly received an activation response after writer failure: %s", body)
	}

	var committed model.User
	if err := fx.db.First(&committed, fx.adminUser.ID).Error; err != nil {
		t.Fatalf("重新加载已提交用户失败: %v", err)
	}
	if !committed.TOTPEnabled || committed.TokenVersion != oldClaims.TokenVersion+1 ||
		committed.TOTPEnrollmentID != "" || committed.TOTPEnrollmentExpiresAt != nil ||
		committed.RecoveryCodes == "" {
		t.Fatalf("activation was not committed exactly once before response failure: user=%+v", committed)
	}
	if oldResp := jsonRequest(t, fx.router, http.MethodGet, "/me", fx.adminToken, ""); oldResp.Code != http.StatusUnauthorized {
		t.Fatalf("old token should be invalid after committed activation: %d %s", oldResp.Code, oldResp.Body.String())
	}
	// Do not retry the request: the response is unusable while the activation
	// state, including recovery codes, is already durable.
}

func TestVerifyTOTPWrongCode(t *testing.T) {
	fx := setupAuthHandlerFixture(t)

	// 先 setup
	setupResp := jsonRequest(t, fx.router, http.MethodPost, "/auth/2fa/setup", fx.adminToken, "")
	if setupResp.Code != http.StatusOK {
		t.Fatalf("setup 失败: %d %s", setupResp.Code, setupResp.Body.String())
	}
	var setup struct {
		Data struct {
			EnrollmentID string `json:"enrollment_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(setupResp.Body.Bytes(), &setup); err != nil {
		t.Fatalf("解析 setup 响应失败: %v", err)
	}
	if setup.Data.EnrollmentID == "" {
		t.Fatalf("setup 未返回 enrollment_id")
	}

	// 用错误验证码 verify
	resp := jsonRequest(t, fx.router, http.MethodPost, "/auth/2fa/verify", fx.adminToken,
		fmt.Sprintf(`{"code":"000000","enrollment_id":%q}`, setup.Data.EnrollmentID))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("期望状态码 400，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	var envelope struct {
		Data struct {
			ErrorCode string `json:"error_code"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if envelope.Data.ErrorCode != "TOTP_CODE_INVALID" {
		t.Fatalf("错误验证码 error_code=%q", envelope.Data.ErrorCode)
	}
	if !strings.Contains(resp.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("错误响应必须 no-store")
	}

	var user model.User
	if err := fx.db.First(&user, fx.adminUser.ID).Error; err != nil {
		t.Fatalf("重新加载用户失败: %v", err)
	}
	if user.TOTPEnabled {
		t.Fatalf("错误验证码不应启用 TOTP")
	}
}

func TestVerifyTOTPNoSetup(t *testing.T) {
	fx := setupAuthHandlerFixture(t)

	// 不调用 setup，直接 verify；缺少 enrollment_id 由服务错误码处理。
	resp := jsonRequest(t, fx.router, http.MethodPost, "/auth/2fa/verify", fx.adminToken,
		`{"code":"123456"}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("期望状态码 400，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	var envelope struct {
		Data struct {
			ErrorCode string `json:"error_code"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if envelope.Data.ErrorCode != "TOTP_ENROLLMENT_REQUIRED" {
		t.Fatalf("缺少 enrollment_id error_code=%q", envelope.Data.ErrorCode)
	}
}

func TestVerifyTOTPRequiresSessionBinding(t *testing.T) {
	fx := setupAuthHandlerFixture(t)
	router := gin.New()
	router.POST("/auth/2fa/verify", fx.handler.TOTPVerify)

	resp := jsonRequest(t, router, http.MethodPost, "/auth/2fa/verify", "", `{}`)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("缺少 session binding 应返回 401，实际: %d", resp.Code)
	}
	if !strings.Contains(resp.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("缺少 session binding 的响应必须 no-store")
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
