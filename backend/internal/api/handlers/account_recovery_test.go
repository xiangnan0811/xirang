package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	appLogger "xirang/backend/internal/logger"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
	"github.com/pquerna/otp/totp"
	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

const accountRecoveryVerifyPath = "/auth/recovery/enrollment/verify"

type accountRecoveryLoginData struct {
	RequiresReenrollment bool   `json:"requires_reenrollment"`
	RecoveryToken        string `json:"recovery_token"`
	Enrollment           struct {
		Secret       string    `json:"secret"`
		QRURL        string    `json:"qr_url"`
		Issuer       string    `json:"issuer"`
		EnrollmentID string    `json:"enrollment_id"`
		ExpiresAt    time.Time `json:"expires_at"`
	} `json:"enrollment"`
}

func newAccountRecoveryHandlerFixture(t *testing.T, engine string) authHandlerTestFixture {
	t.Helper()
	var f authHandlerTestFixture
	if engine == "postgres" {
		f = setupAuthHandlerPostgresFixture(t)
	} else {
		secure.ResetForTesting()
		t.Cleanup(secure.ResetForTesting)
		f = setupAuthHandlerFixture(t)
	}
	if err := f.db.AutoMigrate(&model.AccountRecoveryAudit{}); err != nil {
		t.Fatal("migrate account recovery audit")
	}
	f.router.POST(accountRecoveryVerifyPath, middleware.MaxBodySize(1<<10), func(c *gin.Context) {
		if _, ok := middleware.CurrentSessionBinding(c); ok {
			t.Fatal("recovery endpoint received an ordinary session binding")
		}
		f.handler.RecoveryEnrollmentVerify(c)
		if _, ok := middleware.CurrentSessionBinding(c); ok {
			t.Fatal("recovery endpoint installed an ordinary session binding")
		}
	})
	if err := f.db.Model(&model.User{}).Where("id = ?", f.adminUser.ID).Updates(map[string]any{
		"totp_reenrollment_required": true, "totp_enabled": false,
	}).Error; err != nil {
		t.Fatal("mark recovery required")
	}
	return f
}

func accountRecoveryLogin(t *testing.T, f authHandlerTestFixture) accountRecoveryLoginData {
	t.Helper()
	response := jsonRequest(t, f.router, http.MethodPost, "/auth/login", "",
		fmt.Sprintf(`{"username":"admin","password":%q}`, f.adminPass))
	accountRecoveryAssertResponse(t, response, http.StatusOK, "")
	var envelope struct {
		Data accountRecoveryLoginData `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal("decode recovery login")
	}
	data := envelope.Data
	if !data.RequiresReenrollment || data.RecoveryToken == "" || data.Enrollment.Secret == "" ||
		data.Enrollment.EnrollmentID == "" || !strings.HasPrefix(data.Enrollment.QRURL, "otpauth://totp/") ||
		data.Enrollment.Issuer != "息壤 XiRang" || !data.Enrollment.ExpiresAt.After(time.Now()) {
		t.Fatal("incomplete recovery login contract")
	}
	var raw struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &raw); err != nil || len(raw.Data) != 3 {
		t.Fatal("recovery login must contain only the three recovery fields")
	}
	for _, forbidden := range []string{"token", "user", "login_token", "requires_2fa"} {
		if _, exists := raw.Data[forbidden]; exists {
			t.Fatal("recovery login contains session fields")
		}
	}
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(raw.Data["enrollment"], &nested); err != nil || len(nested) != 5 {
		t.Fatal("unexpected enrollment fields")
	}
	return data
}

func accountRecoveryAssertResponse(t *testing.T, response *httptest.ResponseRecorder, status int, errorCode string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d, want=%d", response.Code, status)
	}
	if response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("missing private no-store")
	}
	if len(response.Header().Values("Set-Cookie")) != 0 {
		t.Fatal("recovery response sets a cookie")
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			ErrorCode string `json:"error_code"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Code != status || envelope.Data.ErrorCode != errorCode {
		t.Fatal("unexpected response envelope")
	}
}

func accountRecoveryCurrentCode(t *testing.T, secret string) string {
	t.Helper()
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal("generate synthetic TOTP")
	}
	return code
}

func TestAccountRecoveryHTTPCompletionAndNormalLogin(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			f := newAccountRecoveryHandlerFixture(t, engine)
			login := accountRecoveryLogin(t, f)
			// Recovery capability is not installed as an ordinary session.
			if response := jsonRequest(t, f.router, http.MethodGet, "/me", login.RecoveryToken, ""); response.Code != http.StatusUnauthorized {
				t.Fatal("recovery bearer reached /me")
			}
			body := fmt.Sprintf(`{"code":%q}`, accountRecoveryCurrentCode(t, login.Enrollment.Secret))
			response := jsonRequest(t, f.router, http.MethodPost, accountRecoveryVerifyPath, login.RecoveryToken, body)
			accountRecoveryAssertResponse(t, response, http.StatusOK, "")
			var completed struct {
				Data map[string]json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &completed); err != nil || len(completed.Data) != 1 {
				t.Fatal("completion contains unexpected fields")
			}
			var codes []string
			if err := json.Unmarshal(completed.Data["recovery_codes"], &codes); err != nil || len(codes) != 8 {
				t.Fatal("completion does not contain eight recovery codes")
			}
			seen := map[string]bool{}
			for _, code := range codes {
				if code == "" || seen[code] {
					t.Fatal("invalid recovery code inventory")
				}
				seen[code] = true
			}
			replay := jsonRequest(t, f.router, http.MethodPost, accountRecoveryVerifyPath, login.RecoveryToken, body)
			accountRecoveryAssertResponse(t, replay, http.StatusUnauthorized, "RECOVERY_ENROLLMENT_INVALID")
			password := jsonRequest(t, f.router, http.MethodPost, "/auth/login", "", fmt.Sprintf(`{"username":"admin","password":%q}`, f.adminPass))
			var pending struct {
				Data struct {
					Requires2FA bool   `json:"requires_2fa"`
					LoginToken  string `json:"login_token"`
				} `json:"data"`
			}
			if err := json.Unmarshal(password.Body.Bytes(), &pending); err != nil || !pending.Data.Requires2FA || pending.Data.LoginToken == "" {
				t.Fatal("completion did not require a fresh normal two-factor login")
			}
			ordinary := jsonRequest(t, f.router, http.MethodPost, "/auth/2fa/login", "", fmt.Sprintf(`{"login_token":%q,"totp_code":%q}`, pending.Data.LoginToken, accountRecoveryCurrentCode(t, login.Enrollment.Secret)))
			var session struct {
				Data struct {
					Token string `json:"token"`
				} `json:"data"`
			}
			if err := json.Unmarshal(ordinary.Body.Bytes(), &session); err != nil || ordinary.Code != http.StatusOK || session.Data.Token == "" {
				t.Fatal("fresh new-factor login failed")
			}
			if response := jsonRequest(t, f.router, http.MethodGet, "/me", session.Data.Token, ""); response.Code != http.StatusOK {
				t.Fatal("normal login session cannot access /me")
			}
		})
	}
}

func TestAccountRecoveryHTTPStrictJSONAndStreamingLimit(t *testing.T) {
	f := newAccountRecoveryHandlerFixture(t, "sqlite")
	login := accountRecoveryLogin(t, f)
	for name, body := range map[string]string{
		"empty": "", "null": "null", "array": "[]", "missing": "{}", "null-code": `{"code":null}`,
		"number": `{"code":123456}`, "unknown": `{"code":"123456","extra":true}`,
		"duplicate": `{"code":"123456","code":"123456"}`, "case": `{"Code":"123456"}`,
		"multiple": `{"code":"123456"}{"code":"123456"}`, "trailing-null": `{"code":"123456"} null`,
		"short": `{"code":"12345"}`, "long": `{"code":"1234567"}`, "space": `{"code":" 12345"}`,
		"unicode": `{"code":"１２３４５６"}`, "oversized": `{"code":"123456"}` + strings.Repeat(" ", 1024),
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, accountRecoveryVerifyPath, strings.NewReader(body))
			request.ContentLength = -1 // Real streamed/chunked bodies have no trusted length.
			request.Header.Set("Authorization", "Bearer "+login.RecoveryToken)
			response := httptest.NewRecorder()
			f.router.ServeHTTP(response, request)
			accountRecoveryAssertResponse(t, response, http.StatusBadRequest, "")
		})
	}
	// Exactly 1024 bytes, including trailing whitespace, must still parse.
	code := accountRecoveryCurrentCode(t, login.Enrollment.Secret)
	wrong := "000000"
	if auth.ValidateTOTP(login.Enrollment.Secret, wrong) {
		wrong = "000001"
	}
	body := fmt.Sprintf(`{"code":%q}`, wrong)
	body += strings.Repeat(" ", 1024-len(body))
	response := jsonRequest(t, f.router, http.MethodPost, accountRecoveryVerifyPath, login.RecoveryToken, body)
	accountRecoveryAssertResponse(t, response, http.StatusForbidden, "TOTP_CODE_INVALID")
	response = jsonRequest(t, f.router, http.MethodPost, accountRecoveryVerifyPath, login.RecoveryToken, fmt.Sprintf(`{"code":%q}`, code))
	accountRecoveryAssertResponse(t, response, http.StatusOK, "")
}

func TestAccountRecoveryHTTPBearerAndStaleCapabilities(t *testing.T) {
	f := newAccountRecoveryHandlerFixture(t, "sqlite")
	first := accountRecoveryLogin(t, f)
	second := accountRecoveryLogin(t, f)
	body := fmt.Sprintf(`{"code":%q}`, accountRecoveryCurrentCode(t, second.Enrollment.Secret))
	for _, header := range []string{"", "Basic abc", "bearer " + second.RecoveryToken, "Bearer ", "Bearer  " + second.RecoveryToken, "Bearer " + second.RecoveryToken + ",abc", "Bearer " + f.adminToken} {
		request := httptest.NewRequest(http.MethodPost, accountRecoveryVerifyPath, strings.NewReader(body))
		request.Header.Set("Authorization", header)
		response := httptest.NewRecorder()
		f.router.ServeHTTP(response, request)
		accountRecoveryAssertResponse(t, response, http.StatusUnauthorized, "RECOVERY_ENROLLMENT_INVALID")
	}
	request := httptest.NewRequest(http.MethodPost, accountRecoveryVerifyPath, strings.NewReader(body))
	request.Header.Add("Authorization", "Bearer "+second.RecoveryToken)
	request.Header.Add("Authorization", "Bearer "+second.RecoveryToken)
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	accountRecoveryAssertResponse(t, response, http.StatusUnauthorized, "RECOVERY_ENROLLMENT_INVALID")
	response = jsonRequest(t, f.router, http.MethodPost, accountRecoveryVerifyPath, first.RecoveryToken, body)
	accountRecoveryAssertResponse(t, response, http.StatusUnauthorized, "RECOVERY_ENROLLMENT_INVALID")
	if err := f.db.Model(&model.User{}).Where("id = ?", f.adminUser.ID).UpdateColumn("token_version", gorm.Expr("token_version + 1")).Error; err != nil {
		t.Fatal("change token version")
	}
	response = jsonRequest(t, f.router, http.MethodPost, accountRecoveryVerifyPath, second.RecoveryToken, body)
	accountRecoveryAssertResponse(t, response, http.StatusUnauthorized, "RECOVERY_ENROLLMENT_INVALID")
}

func TestAccountRecoveryHTTPRejectedLoginDoesNotGenerateEnrollment(t *testing.T) {
	f := newAccountRecoveryHandlerFixture(t, "sqlite")
	assertEmpty := func() {
		t.Helper()
		var row struct {
			TOTPSecret       string
			TOTPEnrollmentID string
		}
		if err := f.db.Table("users").Select("totp_secret", "totp_enrollment_id").Where("id = ?", f.adminUser.ID).Take(&row).Error; err != nil {
			t.Fatal("load enrollment")
		}
		if row.TOTPSecret != "" || row.TOTPEnrollmentID != "" {
			t.Fatal("rejected login generated a secret")
		}
	}
	if err := f.db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal("migrate settings")
	}
	f.handler.settingsSvc = settings.NewService(f.db)
	for _, key := range []string{"login.captcha_enabled", "login.second_captcha_enabled"} {
		if err := f.handler.settingsSvc.Update(key, "true"); err != nil {
			t.Fatal("enable captcha")
		}
		response := jsonRequest(t, f.router, http.MethodPost, "/auth/login", "", fmt.Sprintf(`{"username":"admin","password":%q}`, f.adminPass))
		accountRecoveryAssertResponse(t, response, http.StatusBadRequest, "")
		assertEmpty()
		if err := f.handler.settingsSvc.Update(key, "false"); err != nil {
			t.Fatal("disable captcha")
		}
	}
	for range 5 {
		response := jsonRequest(t, f.router, http.MethodPost, "/auth/login", "", `{"username":"admin","password":"FAKE_WRONG_PASSWORD"}`)
		accountRecoveryAssertResponse(t, response, http.StatusUnauthorized, "")
		assertEmpty()
	}
	response := jsonRequest(t, f.router, http.MethodPost, "/auth/login", "", fmt.Sprintf(`{"username":"admin","password":%q}`, f.adminPass))
	accountRecoveryAssertResponse(t, response, http.StatusLocked, "")
	assertEmpty()
}

func TestAccountRecoveryHTTPInternalErrorsAndLogsAreSecretSafe(t *testing.T) {
	f := newAccountRecoveryHandlerFixture(t, "sqlite")
	var logs bytes.Buffer
	previous := appLogger.Log
	appLogger.Log = zerolog.New(&logs)
	t.Cleanup(func() { appLogger.Log = previous })
	login := accountRecoveryLogin(t, f)
	code := accountRecoveryCurrentCode(t, login.Enrollment.Secret)
	callback := "test:account_recovery_safe_error"
	if err := f.db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			_ = tx.AddError(errors.New("FAKE_PRIVATE_DRIVER_ERROR " + login.RecoveryToken + " " + login.Enrollment.Secret + " " + code))
		}
	}); err != nil {
		t.Fatal("register failure")
	}
	t.Cleanup(func() { _ = f.db.Callback().Update().Remove(callback) })
	response := jsonRequest(t, f.router, http.MethodPost, accountRecoveryVerifyPath, login.RecoveryToken, fmt.Sprintf(`{"code":%q}`, code))
	accountRecoveryAssertResponse(t, response, http.StatusInternalServerError, "")
	response = jsonRequest(t, f.router, http.MethodPost, "/auth/login", "", fmt.Sprintf(`{"username":"admin","password":%q}`, f.adminPass))
	accountRecoveryAssertResponse(t, response, http.StatusInternalServerError, "")
	for _, sensitive := range []string{login.RecoveryToken, login.Enrollment.Secret, code, f.adminPass, "FAKE_PRIVATE_DRIVER_ERROR"} {
		if strings.Contains(logs.String(), sensitive) || strings.Contains(response.Body.String(), sensitive) {
			t.Fatal("secret escaped into logs or safe internal response")
		}
	}
}

func TestAccountRecoveryHTTPResponseLossCannotRedisplayCodes(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			f := newAccountRecoveryHandlerFixture(t, engine)
			login := accountRecoveryLogin(t, f)
			body := fmt.Sprintf(`{"code":%q}`, accountRecoveryCurrentCode(t, login.Enrollment.Secret))
			var writer *authHandlerWriteFailureResponseWriter
			lost := gin.New()
			lost.Use(func(c *gin.Context) {
				writer = &authHandlerWriteFailureResponseWriter{ResponseWriter: c.Writer}
				c.Writer = writer
				c.Next()
			})
			lost.POST(accountRecoveryVerifyPath, middleware.MaxBodySize(1<<10), f.handler.RecoveryEnrollmentVerify)
			_ = jsonRequest(t, lost, http.MethodPost, accountRecoveryVerifyPath, login.RecoveryToken, body)
			if writer == nil || writer.writeAttempts != 1 {
				t.Fatal("response loss was not exercised")
			}
			var user model.User
			if err := f.db.First(&user, f.adminUser.ID).Error; err != nil || !user.TOTPEnabled || user.TOTPReenrollmentRequired {
				t.Fatal("lost response did not commit enrollment")
			}
			replay := jsonRequest(t, f.router, http.MethodPost, accountRecoveryVerifyPath, login.RecoveryToken, body)
			accountRecoveryAssertResponse(t, replay, http.StatusUnauthorized, "RECOVERY_ENROLLMENT_INVALID")
			if strings.Contains(replay.Body.String(), "recovery_codes") {
				t.Fatal("lost recovery codes were redisplayed")
			}
			password := jsonRequest(t, f.router, http.MethodPost, "/auth/login", "", fmt.Sprintf(`{"username":"admin","password":%q}`, f.adminPass))
			var pending struct {
				Data struct {
					Requires2FA bool   `json:"requires_2fa"`
					LoginToken  string `json:"login_token"`
				} `json:"data"`
			}
			if err := json.Unmarshal(password.Body.Bytes(), &pending); err != nil || !pending.Data.Requires2FA || pending.Data.LoginToken == "" {
				t.Fatal("lost completion response prevented ordinary two-factor login")
			}
			ordinary := jsonRequest(t, f.router, http.MethodPost, "/auth/2fa/login", "", fmt.Sprintf(`{"login_token":%q,"totp_code":%q}`, pending.Data.LoginToken, accountRecoveryCurrentCode(t, login.Enrollment.Secret)))
			if ordinary.Code != http.StatusOK {
				t.Fatal("new factor cannot log in after completion response loss")
			}
		})
	}
}
