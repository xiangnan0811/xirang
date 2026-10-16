package api

import (
	"bytes"
	"context"
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
	"xirang/backend/internal/model"
	"xirang/backend/internal/ws"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/pquerna/otp/totp"
	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

const accountRecoveryRouterPath = "/api/v1/auth/recovery/enrollment/verify"
const accountRecoveryRouterPassword = "FAKE_RecoveryRouterPassword2026!_FOR_TEST_ONLY"

type accountRecoveryRouterEnrollment struct {
	Token      string `json:"recovery_token"`
	Enrollment struct {
		Secret string `json:"secret"`
	} `json:"enrollment"`
}

func newAccountRecoveryRouterFixture(t *testing.T, limit int) (recoveryCodesRouterFixture, *ws.Hub) {
	t.Helper()
	f := newRecoveryCodesRouterFixture(t, limit)
	if err := f.db.AutoMigrate(&model.LoginFailure{}, &model.AccountRecoveryAudit{}); err != nil {
		t.Fatal("migrate account recovery fixtures")
	}
	hash, err := auth.HashPassword(accountRecoveryRouterPassword)
	if err != nil {
		t.Fatal("hash synthetic password")
	}
	if err := f.db.Model(&model.User{}).Where("id = ?", f.users["admin"].user.ID).UpdateColumn("password_hash", hash).Error; err != nil {
		t.Fatal("set synthetic password")
	}
	service := auth.NewService(f.db, f.manager, nil, auth.LoginSecurityConfig{FailLockThreshold: 5, FailLockDuration: time.Minute})
	hub := ws.NewHub(f.db, nil, true)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hub.Run(ctx)
	f.router = NewRouter(Dependencies{AppContext: ctx, DB: f.db, JWTManager: f.manager, AuthService: service, Hub: hub, LoginRateLimit: limit, LoginRateWindow: time.Minute})
	return f, hub
}

func accountRecoveryRouterBegin(t *testing.T, f recoveryCodesRouterFixture) accountRecoveryRouterEnrollment {
	t.Helper()
	if err := f.db.Model(&model.User{}).Where("id = ?", f.users["admin"].user.ID).Updates(map[string]any{
		"totp_reenrollment_required": true, "totp_enabled": false, "totp_secret": "", "recovery_codes": "",
	}).Error; err != nil {
		t.Fatal("set recovery state")
	}
	response := recoveryCodesRequest(t, f.router, http.MethodPost, "/api/v1/auth/login", "", fmt.Sprintf(`{"username":%q,"password":%q}`, f.users["admin"].user.Username, accountRecoveryRouterPassword), "192.0.2.20:1234")
	accountRecoveryRouterAssert(t, response, http.StatusOK, "")
	var envelope struct {
		Data accountRecoveryRouterEnrollment `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Data.Token == "" || envelope.Data.Enrollment.Secret == "" {
		t.Fatal("missing restricted login capability")
	}
	return envelope.Data
}

func accountRecoveryRouterAssert(t *testing.T, response *httptest.ResponseRecorder, status int, machineCode string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("response status=%d want=%d", response.Code, status)
	}
	if response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("missing no-store on router response")
	}
	if len(response.Header().Values("Set-Cookie")) != 0 {
		t.Fatal("recovery endpoint set cookie")
	}
	if machineCode != "" && recoveryCodesErrorCode(t, response) != machineCode {
		t.Fatal("unexpected recovery machine code")
	}
}

func TestAccountRecoveryRouterPurposeIsolation(t *testing.T) {
	f, hub := newAccountRecoveryRouterFixture(t, 100)
	admin := f.users["admin"]
	pending, err := f.manager.Generate2FAPendingToken(admin.user)
	if err != nil {
		t.Fatal("generate pending capability")
	}
	stepup, _, err := f.manager.GenerateStepUpToken(admin.user, auth.StepUpActionTaskManualTrigger)
	if err != nil {
		t.Fatal("generate step-up capability")
	}
	enrollment := accountRecoveryRouterBegin(t, f)
	for name, token := range map[string]string{"primary": admin.token, "pending": pending, "step-up": stepup, "missing": ""} {
		t.Run(name, func(t *testing.T) {
			response := recoveryCodesRequest(t, f.router, http.MethodPost, accountRecoveryRouterPath, token, `{"code":"123456"}`, "192.0.2.30:1234")
			accountRecoveryRouterAssert(t, response, http.StatusUnauthorized, "RECOVERY_ENROLLMENT_INVALID")
		})
	}
	for _, endpoint := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/me"}, {http.MethodPost, "/api/v1/auth/2fa/setup"},
		{http.MethodPost, "/api/v1/auth/2fa/verify"}, {http.MethodPost, "/api/v1/auth/step-up"},
		{http.MethodPost, "/api/v1/credential-access-grants/terminal"},
	} {
		for name, token := range map[string]string{"recovery": enrollment.Token, "pending": pending, "step-up": stepup} {
			t.Run(endpoint.path+"/"+name, func(t *testing.T) {
				response := recoveryCodesRequest(t, f.router, endpoint.method, endpoint.path, token, `{"code":"123456"}`, "192.0.2.31:1234")
				if response.Code != http.StatusUnauthorized {
					t.Fatalf("restricted bearer reached secured route, status=%d", response.Code)
				}
			})
		}
	}
	// Exercise the production websocket handshake, not a missing-Hub 503.
	server := httptest.NewServer(f.router)
	t.Cleanup(server.Close)
	for _, token := range []string{enrollment.Token, pending, stepup} {
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/ws/logs", nil)
		if err != nil {
			t.Fatal("open websocket handshake")
		}
		if err := conn.WriteJSON(map[string]string{"type": "auth", "token": token}); err != nil {
			_ = conn.Close()
			t.Fatal("write restricted websocket auth")
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _, readErr := conn.ReadMessage()
		_ = conn.Close()
		if readErr == nil {
			t.Fatal("restricted token authenticated to websocket")
		}
		if hub.ClientCount() != 0 {
			t.Fatal("restricted token registered websocket client")
		}
	}
	wrong := "000000"
	if auth.ValidateTOTP(enrollment.Enrollment.Secret, wrong) {
		wrong = "000001"
	}
	response := recoveryCodesRequest(t, f.router, http.MethodPost, accountRecoveryRouterPath, enrollment.Token, fmt.Sprintf(`{"code":%q}`, wrong), "192.0.2.32:1234")
	accountRecoveryRouterAssert(t, response, http.StatusForbidden, "TOTP_CODE_INVALID")
	code, err := totp.GenerateCode(enrollment.Enrollment.Secret, time.Now())
	if err != nil {
		t.Fatal("generate new-factor code")
	}
	response = recoveryCodesRequest(t, f.router, http.MethodPost, accountRecoveryRouterPath, enrollment.Token, fmt.Sprintf(`{"code":%q}`, code), "192.0.2.33:1234")
	accountRecoveryRouterAssert(t, response, http.StatusOK, "")
	// Completion still does not elevate the original capability.
	response = recoveryCodesRequest(t, f.router, http.MethodGet, "/api/v1/me", enrollment.Token, "", "192.0.2.34:1234")
	if response.Code != http.StatusUnauthorized {
		t.Fatal("completed recovery bearer became a session")
	}
}

func TestAccountRecoveryRouterExpiredAndForeignCapabilities(t *testing.T) {
	f, _ := newAccountRecoveryRouterFixture(t, 100)
	enrollment := accountRecoveryRouterBegin(t, f)
	original, err := f.manager.ParseToken(enrollment.Token)
	if err != nil {
		t.Fatal("parse synthetic capability")
	}
	for name, change := range map[string]func(*auth.Claims){
		"expired": func(c *auth.Claims) {
			c.IssuedAt = jwt.NewNumericDate(time.Now().Add(-20 * time.Minute))
			c.ExpiresAt = jwt.NewNumericDate(c.IssuedAt.Add(auth.TOTPEnrollmentTTL))
		},
		"foreign-user": func(c *auth.Claims) {
			c.UserID = f.users["viewer"].user.ID
			c.Subject = fmt.Sprint(c.UserID)
			c.Role = "viewer"
		},
		"foreign-enrollment": func(c *auth.Claims) { c.EnrollmentID = strings.Repeat("a", 32) },
	} {
		t.Run(name, func(t *testing.T) {
			claims := *original
			change(&claims)
			token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("FAKE_RECOVERY_ROUTER_JWT_KEY_FOR_TEST_ONLY"))
			if err != nil {
				t.Fatal("sign synthetic capability")
			}
			response := recoveryCodesRequest(t, f.router, http.MethodPost, accountRecoveryRouterPath, token, `{"code":"123456"}`, "192.0.2.40:1234")
			accountRecoveryRouterAssert(t, response, http.StatusUnauthorized, "RECOVERY_ENROLLMENT_INVALID")
		})
	}
}

func TestAccountRecoveryRouterNoStoreBeforeLimiterAndCORS(t *testing.T) {
	f, _ := newAccountRecoveryRouterFixture(t, 1)
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/2fa/login", accountRecoveryRouterPath} {
		_ = recoveryCodesRequest(t, f.router, http.MethodPost, path, "", `{}`, "192.0.2.50:1234")
		response := recoveryCodesRequest(t, f.router, http.MethodPost, path, "", `{}`, "192.0.2.50:1234")
		accountRecoveryRouterAssert(t, response, http.StatusTooManyRequests, "")
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		request.Header.Set("Origin", "https://untrusted.invalid")
		response = httptest.NewRecorder()
		f.router.ServeHTTP(response, request)
		accountRecoveryRouterAssert(t, response, http.StatusForbidden, "")
	}
}

func TestAccountRecoveryRouterStreamedBodyLimitAndSafeInternalFailure(t *testing.T) {
	f, _ := newAccountRecoveryRouterFixture(t, 100)
	var logs bytes.Buffer
	previous := appLogger.Log
	appLogger.Log = zerolog.New(&logs)
	t.Cleanup(func() { appLogger.Log = previous })
	enrollment := accountRecoveryRouterBegin(t, f)
	request := httptest.NewRequest(http.MethodPost, accountRecoveryRouterPath, strings.NewReader(`{"code":"123456"}`+strings.Repeat(" ", 1024)))
	request.ContentLength = -1
	request.Header.Set("Authorization", "Bearer "+enrollment.Token)
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	accountRecoveryRouterAssert(t, response, http.StatusBadRequest, "")
	callback := "test:account_recovery_router_safe_error"
	if err := f.db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			_ = tx.AddError(errors.New("FAKE_PRIVATE_ROUTER_DRIVER_ERROR"))
		}
	}); err != nil {
		t.Fatal("register safe internal failure")
	}
	t.Cleanup(func() { _ = f.db.Callback().Update().Remove(callback) })
	code, err := totp.GenerateCode(enrollment.Enrollment.Secret, time.Now())
	if err != nil {
		t.Fatal("generate new-factor code")
	}
	response = recoveryCodesRequest(t, f.router, http.MethodPost, accountRecoveryRouterPath, enrollment.Token, fmt.Sprintf(`{"code":%q}`, code), "192.0.2.60:1234")
	accountRecoveryRouterAssert(t, response, http.StatusInternalServerError, "")
	response = recoveryCodesRequest(t, f.router, http.MethodPost, "/api/v1/auth/login", "", fmt.Sprintf(`{"username":%q,"password":%q}`, f.users["admin"].user.Username, accountRecoveryRouterPassword), "192.0.2.61:1234")
	accountRecoveryRouterAssert(t, response, http.StatusInternalServerError, "")
	if strings.Contains(response.Body.String(), "FAKE_PRIVATE_ROUTER_DRIVER_ERROR") {
		t.Fatal("internal error leaked")
	}
	for _, sensitive := range []string{enrollment.Token, enrollment.Enrollment.Secret, code, accountRecoveryRouterPassword, "FAKE_PRIVATE_ROUTER_DRIVER_ERROR"} {
		if strings.Contains(logs.String(), sensitive) {
			t.Fatal("secret or driver details escaped into production router logs")
		}
	}
}

func TestAccountRecoveryRouterUnknownCommitDoesNotRedisplayCodes(t *testing.T) {
	f, state := newRecoveryCodesCommitFailureRouterFixture(t, 100)
	state.failCommit.Store(false)
	if err := f.db.AutoMigrate(&model.LoginFailure{}, &model.AccountRecoveryAudit{}); err != nil {
		t.Fatal("migrate recovery fixture")
	}
	hash, err := auth.HashPassword(accountRecoveryRouterPassword)
	if err != nil {
		t.Fatal("hash synthetic password")
	}
	if err := f.db.Model(&model.User{}).Where("id = ?", f.users["admin"].user.ID).UpdateColumn("password_hash", hash).Error; err != nil {
		t.Fatal("set password")
	}
	enrollment := accountRecoveryRouterBegin(t, f)
	code, err := totp.GenerateCode(enrollment.Enrollment.Secret, time.Now())
	if err != nil {
		t.Fatal("generate new-factor code")
	}
	body := fmt.Sprintf(`{"code":%q}`, code)
	state.failCommit.Store(true)
	response := recoveryCodesRequest(t, f.router, http.MethodPost, accountRecoveryRouterPath, enrollment.Token, body, "192.0.2.70:1234")
	accountRecoveryRouterAssert(t, response, http.StatusInternalServerError, "")
	if strings.Contains(response.Body.String(), "recovery_codes") {
		t.Fatal("unknown commit published recovery codes")
	}
	response = recoveryCodesRequest(t, f.router, http.MethodPost, accountRecoveryRouterPath, enrollment.Token, body, "192.0.2.71:1234")
	accountRecoveryRouterAssert(t, response, http.StatusUnauthorized, "RECOVERY_ENROLLMENT_INVALID")
	var row model.User
	if err := f.db.First(&row, f.users["admin"].user.ID).Error; err != nil || !row.TOTPEnabled || row.TOTPReenrollmentRequired {
		t.Fatal("unknown commit fixture did not commit")
	}
}
