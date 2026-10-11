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
	"github.com/pquerna/otp/totp"
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
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", handlerTestDBName(t))
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
	if err := db.AutoMigrate(&model.User{}, &model.PendingAuthToken{}, &model.TokenRevocation{}); err != nil {
		t.Fatalf("初始化用户表失败: %v", err)
	}

	adminPassword := "FAKE_AdminPass2026!_FOR_TEST_ONLY"
	operatorPassword := "FAKE_OperatorPass2026!_FOR_TEST_ONLY"
	adminUser := seedAuthUser(t, db, "admin", "admin", adminPassword)
	operatorUser := seedAuthUser(t, db, "operator", "operator", operatorPassword)

	jwtManager := auth.NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	jwtManager.SetDB(db)
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
	hashedRecoveryCodes, err := auth.HashRecoveryCodes(recoveryCodes)
	if err != nil {
		t.Fatalf("哈希恢复码失败: %v", err)
	}
	recoveryJSON, err := json.Marshal(hashedRecoveryCodes)
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
	if len(remaining) != 1 {
		t.Fatalf("期望保留 1 个未使用恢复码，实际: %d", len(remaining))
	}
	// 剩余恢复码应为 bcrypt 哈希格式
	if !strings.HasPrefix(remaining[0], "$2") {
		t.Fatalf("剩余恢复码应为 bcrypt 哈希格式，实际: %s", remaining[0])
	}
}

func TestAuthHandlerTOTPLoginRejectsWhenRecoveryCodeCannotBeSaved(t *testing.T) {
	db := openAuthUserHandlerTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.PendingAuthToken{}); err != nil {
		t.Fatalf("初始化用户表失败: %v", err)
	}
	user := seedAuthUser(t, db, "admin", "admin", "FAKE_AdminPass2026!_FOR_TEST_ONLY")
	recoveryCodes := []string{"FAKE_RECOVERY_CODE_FOR_TEST_ONLY"}
	hashedRecoveryCodes, err := auth.HashRecoveryCodes(recoveryCodes)
	if err != nil {
		t.Fatalf("哈希恢复码失败: %v", err)
	}
	recoveryJSON, err := json.Marshal(hashedRecoveryCodes)
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
	submittedCode := "FaKe_ReCoVeRy_CoDe_FoR_TeSt_OnLy"
	beforeRecoveryCodes := authUserRecoveryCodes(t, db, user.ID)

	const callbackName = "xirang:test_recovery_code_save_error"
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if err := tx.AddError(errors.New("forced recovery code save failure")); err != nil {
			tx.Logger.Error(tx.Statement.Context, "failed to add forced recovery code save failure: %v", err)
		}
	}); err != nil {
		t.Fatalf("注册保存失败回调失败: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Update().Remove(callbackName)
	})

	resp := performJSONRequest(
		t,
		router,
		http.MethodPost,
		"/auth/2fa/login",
		"",
		authUserTOTPLoginRequestBody(t, loginToken, submittedCode),
	)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("恢复码保存失败时应返回 500 且不签发 token，实际: %d", resp.Code)
	}
	var failedPayload struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &failedPayload); err != nil {
		t.Fatalf("解析失败响应: %v", err)
	}
	if failedPayload.Data.Token != "" {
		t.Fatal("恢复码保存失败不得签发 JWT")
	}
	assertAuthUserRecoveryCodesEqual(t, db, user.ID, beforeRecoveryCodes)
	assertAuthUserPendingUnconsumed(t, db, jwtManager, loginToken)

	if err := db.Callback().Update().Remove(callbackName); err != nil {
		t.Fatalf("移除保存失败回调失败: %v", err)
	}
	resp = performJSONRequest(
		t,
		router,
		http.MethodPost,
		"/auth/2fa/login",
		"",
		authUserTOTPLoginRequestBody(t, loginToken, submittedCode),
	)
	if resp.Code != http.StatusOK {
		t.Fatalf("移除保存失败后应使用同一 pending 和恢复码成功，实际: %d", resp.Code)
	}
	assertAuthUserRecoveryCodesEqual(t, db, user.ID, []string{})
	assertAuthUserPendingConsumed(t, db, jwtManager, loginToken)
}

func authUserTOTPLoginRequestBody(t *testing.T, loginToken, code string) string {
	t.Helper()
	payload := struct {
		LoginToken string `json:"login_token"`
		TOTPCode   string `json:"totp_code"`
	}{
		LoginToken: loginToken,
		TOTPCode:   code,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("序列化 2FA 登录请求失败: %v", err)
	}
	return string(body)
}

func setupAuthUserRecoveryCodeLogin(t *testing.T, plaintext bool) (authUserTestFixture, string) {
	t.Helper()
	fx := setupAuthUserFixture(t)
	key, err := auth.GenerateTOTPSecret("FAKE_XIRANG_ISSUER_FOR_TEST_ONLY", "admin")
	if err != nil {
		t.Fatalf("生成 TOTP secret 失败: %v", err)
	}
	recoveryCodes := []string{"ABCD2345", "EFGH6789"}
	storedCodes := recoveryCodes
	if !plaintext {
		storedCodes, err = auth.HashRecoveryCodes(recoveryCodes)
		if err != nil {
			t.Fatalf("哈希恢复码失败: %v", err)
		}
	}
	recoveryJSON, err := json.Marshal(storedCodes)
	if err != nil {
		t.Fatalf("序列化恢复码失败: %v", err)
	}
	fx.adminUser.TOTPEnabled = true
	fx.adminUser.TOTPSecret = key.Secret()
	fx.adminUser.RecoveryCodes = string(recoveryJSON)
	if err := fx.db.Save(&fx.adminUser).Error; err != nil {
		t.Fatalf("保存 2FA 用户失败: %v", err)
	}
	authHandler := NewAuthHandler(fx.service, fx.jwtManager, nil).WithDB(fx.db)
	fx.router.POST("/auth/2fa/login", authHandler.TOTPLogin)
	return fx, key.Secret()
}

func authUserPendingRow(t *testing.T, db *gorm.DB, jwtManager *auth.JWTManager, loginToken string) model.PendingAuthToken {
	t.Helper()
	claims, err := jwtManager.ParseToken(loginToken)
	if err != nil {
		t.Fatalf("解析 2FA 中间 token 失败: %v", err)
	}
	var pending model.PendingAuthToken
	if err := db.Where("jti = ?", claims.ID).First(&pending).Error; err != nil {
		t.Fatalf("读取 2FA 中间 token 持久化行失败: %v", err)
	}
	return pending
}

func generateAuthUserPendingToken(t *testing.T, fx authUserTestFixture) string {
	t.Helper()
	var user model.User
	if err := fx.db.First(&user, fx.adminUser.ID).Error; err != nil {
		t.Fatalf("重新加载 2FA 用户失败: %v", err)
	}
	loginToken, err := fx.jwtManager.Generate2FAPendingToken(user)
	if err != nil {
		t.Fatalf("生成 2FA 中间 token 失败: %v", err)
	}
	return loginToken
}

func authUserRecoveryCodes(t *testing.T, db *gorm.DB, userID uint) []string {
	t.Helper()
	var user model.User
	if err := db.First(&user, userID).Error; err != nil {
		t.Fatalf("重新加载恢复码用户失败: %v", err)
	}
	if strings.TrimSpace(user.RecoveryCodes) == "" {
		return []string{}
	}
	var recoveryCodes []string
	if err := json.Unmarshal([]byte(user.RecoveryCodes), &recoveryCodes); err != nil {
		t.Fatalf("解析持久化恢复码失败: %v", err)
	}
	return recoveryCodes
}

func assertAuthUserRecoveryCodesEqual(t *testing.T, db *gorm.DB, userID uint, want []string) {
	t.Helper()
	got := authUserRecoveryCodes(t, db, userID)
	if len(got) != len(want) {
		t.Fatalf("恢复码库存数量不符，实际 %d，期望 %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("恢复码库存内容不符，索引 %d 不应变化", i)
		}
	}
}

func assertAuthUserPendingUnconsumed(t *testing.T, db *gorm.DB, jwtManager *auth.JWTManager, loginToken string) {
	t.Helper()
	if pending := authUserPendingRow(t, db, jwtManager, loginToken); pending.ConsumedAt != nil {
		t.Fatal("失败的恢复码登录不应消费 pending token")
	}
}

func assertAuthUserPendingConsumed(t *testing.T, db *gorm.DB, jwtManager *auth.JWTManager, loginToken string) {
	t.Helper()
	if pending := authUserPendingRow(t, db, jwtManager, loginToken); pending.ConsumedAt == nil {
		t.Fatal("成功的 2FA 登录应消费 pending token")
	}
}

func assertAuthUserTOTPLoginOK(t *testing.T, resp *httptest.ResponseRecorder) {
	t.Helper()
	if resp.Code != http.StatusOK {
		t.Fatalf("2FA 登录期望状态码 200，实际: %d", resp.Code)
	}
	var payload struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析 2FA 登录响应失败: %v", err)
	}
	if strings.TrimSpace(payload.Data.Token) == "" {
		t.Fatal("成功的 2FA 登录应返回 JWT")
	}
}

func assertAuthUserOneRemainingRecoveryCode(t *testing.T, db *gorm.DB, userID uint, plaintext bool) {
	t.Helper()
	remaining := authUserRecoveryCodes(t, db, userID)
	if len(remaining) != 1 {
		t.Fatalf("成功消费后应剩余 1 个恢复码，实际 %d", len(remaining))
	}
	if plaintext {
		if remaining[0] != "EFGH6789" {
			t.Fatal("明文恢复码库存中的未使用值不应变化")
		}
		return
	}
	if !strings.HasPrefix(remaining[0], "$2") {
		t.Fatal("bcrypt 恢复码库存中的未使用值应保持哈希格式")
	}
}

func TestAuthHandlerTOTPLoginRecoveryCodeNormalization(t *testing.T) {
	storages := []struct {
		name      string
		plaintext bool
	}{
		{name: "bcrypt", plaintext: false},
		{name: "historical plaintext", plaintext: true},
	}
	inputs := []struct {
		name string
		code string
	}{
		{name: "uppercase", code: "ABCD2345"},
		{name: "lowercase", code: "abcd2345"},
		{name: "mixed case", code: "AbCd2345"},
		{name: "surrounding whitespace", code: " \tAbCd2345\r\n"},
	}

	for _, storage := range storages {
		storage := storage
		for _, input := range inputs {
			input := input
			t.Run(storage.name+"/"+input.name, func(t *testing.T) {
				fx, _ := setupAuthUserRecoveryCodeLogin(t, storage.plaintext)
				beforeRecoveryCodes := authUserRecoveryCodes(t, fx.db, fx.adminUser.ID)
				loginToken := generateAuthUserPendingToken(t, fx)
				resp := performJSONRequest(
					t,
					fx.router,
					http.MethodPost,
					"/auth/2fa/login",
					"",
					authUserTOTPLoginRequestBody(t, loginToken, input.code),
				)
				assertAuthUserTOTPLoginOK(t, resp)
				assertAuthUserPendingConsumed(t, fx.db, fx.jwtManager, loginToken)
				assertAuthUserOneRemainingRecoveryCode(t, fx.db, fx.adminUser.ID, storage.plaintext)
				assertAuthUserRecoveryCodesEqual(t, fx.db, fx.adminUser.ID, beforeRecoveryCodes[1:])

				freshPending := generateAuthUserPendingToken(t, fx)
				resp = performJSONRequest(
					t,
					fx.router,
					http.MethodPost,
					"/auth/2fa/login",
					"",
					authUserTOTPLoginRequestBody(t, freshPending, input.code),
				)
				if resp.Code != http.StatusUnauthorized {
					t.Fatalf("已消费恢复码配合新 pending 应返回 401，实际: %d", resp.Code)
				}
				assertAuthUserPendingUnconsumed(t, fx.db, fx.jwtManager, freshPending)
				assertAuthUserOneRemainingRecoveryCode(t, fx.db, fx.adminUser.ID, storage.plaintext)
				assertAuthUserRecoveryCodesEqual(t, fx.db, fx.adminUser.ID, beforeRecoveryCodes[1:])
			})
		}
	}

	invalidAttempts := []struct {
		name string
		code string
	}{
		{name: "wrong character", code: "ABCD2346"},
		{name: "internal space", code: "AB CD2345"},
		{name: "fullwidth character", code: "\uFF21BCD2345"},
		{name: "unicode confusable", code: "\u0410BCD2345"},
		{name: "whitespace only", code: " \t\r\n"},
	}
	for _, storage := range storages {
		storage := storage
		t.Run(storage.name+"/invalid attempts preserve state", func(t *testing.T) {
			fx, _ := setupAuthUserRecoveryCodeLogin(t, storage.plaintext)
			loginToken := generateAuthUserPendingToken(t, fx)
			beforeRecoveryCodes := authUserRecoveryCodes(t, fx.db, fx.adminUser.ID)
			for _, attempt := range invalidAttempts {
				attempt := attempt
				t.Run(attempt.name, func(t *testing.T) {
					resp := performJSONRequest(
						t,
						fx.router,
						http.MethodPost,
						"/auth/2fa/login",
						"",
						authUserTOTPLoginRequestBody(t, loginToken, attempt.code),
					)
					if resp.Code != http.StatusUnauthorized {
						t.Fatalf("无效恢复码应返回 401，实际: %d", resp.Code)
					}
					assertAuthUserRecoveryCodesEqual(t, fx.db, fx.adminUser.ID, beforeRecoveryCodes)
					assertAuthUserPendingUnconsumed(t, fx.db, fx.jwtManager, loginToken)
				})
			}

			resp := performJSONRequest(
				t,
				fx.router,
				http.MethodPost,
				"/auth/2fa/login",
				"",
				authUserTOTPLoginRequestBody(t, loginToken, " \tAbCd2345\r\n"),
			)
			assertAuthUserTOTPLoginOK(t, resp)
			assertAuthUserPendingConsumed(t, fx.db, fx.jwtManager, loginToken)
			assertAuthUserOneRemainingRecoveryCode(t, fx.db, fx.adminUser.ID, storage.plaintext)
			assertAuthUserRecoveryCodesEqual(t, fx.db, fx.adminUser.ID, beforeRecoveryCodes[1:])
		})
	}

	t.Run("expired persistent pending", func(t *testing.T) {
		fx, _ := setupAuthUserRecoveryCodeLogin(t, false)
		loginToken := generateAuthUserPendingToken(t, fx)
		claims, err := fx.jwtManager.ParseToken(loginToken)
		if err != nil {
			t.Fatalf("解析 2FA 中间 token 失败: %v", err)
		}
		expiredAt := time.Now().UTC().Add(-time.Minute)
		result := fx.db.Model(&model.PendingAuthToken{}).
			Where("jti = ?", claims.ID).
			Update("expires_at", expiredAt)
		if result.Error != nil {
			t.Fatalf("设置过期 pending token 失败: %v", result.Error)
		}
		if result.RowsAffected != 1 {
			t.Fatalf("设置过期 pending token 应影响 1 行，实际: %d", result.RowsAffected)
		}
		beforeRecoveryCodes := authUserRecoveryCodes(t, fx.db, fx.adminUser.ID)

		resp := performJSONRequest(
			t,
			fx.router,
			http.MethodPost,
			"/auth/2fa/login",
			"",
			authUserTOTPLoginRequestBody(t, loginToken, "ABCD2345"),
		)
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("持久化 pending 过期时应返回 401，实际: %d", resp.Code)
		}
		assertAuthUserRecoveryCodesEqual(t, fx.db, fx.adminUser.ID, beforeRecoveryCodes)
		assertAuthUserPendingUnconsumed(t, fx.db, fx.jwtManager, loginToken)
		if pending := authUserPendingRow(t, fx.db, fx.jwtManager, loginToken); !pending.ExpiresAt.Before(time.Now().UTC()) {
			t.Fatal("pending token 持久化 expires_at 应保持在过去")
		}
	})

	t.Run("real TOTP remains accepted", func(t *testing.T) {
		fx, secret := setupAuthUserRecoveryCodeLogin(t, false)
		loginToken := generateAuthUserPendingToken(t, fx)
		beforeRecoveryCodes := authUserRecoveryCodes(t, fx.db, fx.adminUser.ID)
		code, err := totp.GenerateCode(secret, time.Now())
		if err != nil {
			t.Fatalf("生成 TOTP 验证码失败: %v", err)
		}

		resp := performJSONRequest(
			t,
			fx.router,
			http.MethodPost,
			"/auth/2fa/login",
			"",
			authUserTOTPLoginRequestBody(t, loginToken, code),
		)
		assertAuthUserTOTPLoginOK(t, resp)
		assertAuthUserPendingConsumed(t, fx.db, fx.jwtManager, loginToken)
		assertAuthUserRecoveryCodesEqual(t, fx.db, fx.adminUser.ID, beforeRecoveryCodes)
	})
}
