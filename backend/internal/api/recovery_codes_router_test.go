package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"github.com/gin-gonic/gin"
	"github.com/mattn/go-sqlite3"
	"github.com/pquerna/otp/totp"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const recoveryCodesCommitFailureDriverName = "xirang_api_recovery_commit_failure_test"

var (
	recoveryCodesCommitFailureDriverOnce  sync.Once
	recoveryCodesCommitFailureDriverState atomic.Pointer[recoveryCodesCommitFailureState]
)

type recoveryCodesCommitFailureState struct {
	failCommit atomic.Bool
}

type recoveryCodesCommitFailureDriver struct{}

func (recoveryCodesCommitFailureDriver) Open(name string) (driver.Conn, error) {
	conn, err := (&sqlite3.SQLiteDriver{}).Open(name)
	if err != nil {
		return nil, err
	}
	return &recoveryCodesCommitFailureConn{
		Conn:  conn,
		state: recoveryCodesCommitFailureDriverState.Load(),
	}, nil
}

type recoveryCodesCommitFailureConn struct {
	driver.Conn
	state *recoveryCodesCommitFailureState
}

func (conn *recoveryCodesCommitFailureConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	beginner, ok := conn.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, driver.ErrSkip
	}
	tx, err := beginner.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &recoveryCodesCommitFailureTx{Tx: tx, state: conn.state}, nil
}

type recoveryCodesCommitFailureTx struct {
	driver.Tx
	state *recoveryCodesCommitFailureState
}

func (tx *recoveryCodesCommitFailureTx) Commit() error {
	if err := tx.Tx.Commit(); err != nil {
		return err
	}
	if tx.state != nil && tx.state.failCommit.CompareAndSwap(true, false) {
		return fmt.Errorf("FAKE_RECOVERY_COMMIT_FAILURE_FOR_TEST_ONLY")
	}
	return nil
}

type recoveryCodesRouterUser struct {
	user   model.User
	token  string
	secret string
}

type recoveryCodesRouterFixture struct {
	db      *gorm.DB
	manager *auth.JWTManager
	router  *gin.Engine
	users   map[string]recoveryCodesRouterUser
}

func newRecoveryCodesRouterFixture(t *testing.T, loginLimit int) recoveryCodesRouterFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RECOVERY_ROUTER_DATA_KEY_32_BYTES_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	gin.SetMode(gin.TestMode)

	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared&_loc=UTC"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open recovery route database: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return newRecoveryCodesRouterFixtureWithDB(t, db, loginLimit)
}

func newRecoveryCodesRouterFixtureWithDB(t *testing.T, db *gorm.DB, loginLimit int) recoveryCodesRouterFixture {
	t.Helper()
	if err := db.AutoMigrate(
		&model.User{},
		&model.PendingAuthToken{},
		&model.TokenRevocation{},
		&model.AuditLog{},
	); err != nil {
		t.Fatalf("migrate recovery route database: %v", err)
	}

	manager := auth.NewJWTManager("FAKE_RECOVERY_ROUTER_JWT_KEY_FOR_TEST_ONLY", time.Hour)
	manager.SetDB(db)
	service := auth.NewService(db, manager, nil, auth.LoginSecurityConfig{
		FailLockThreshold: 5,
		FailLockDuration:  time.Minute,
	})
	fixture := recoveryCodesRouterFixture{
		db:      db,
		manager: manager,
		users:   make(map[string]recoveryCodesRouterUser),
	}
	for _, role := range []string{"admin", "operator", "viewer"} {
		fixture.addUser(t, role, true)
	}
	fixture.router = NewRouter(Dependencies{
		DB:              db,
		AuthService:     service,
		JWTManager:      manager,
		LoginRateLimit:  loginLimit,
		LoginRateWindow: time.Minute,
	})
	return fixture
}

func newRecoveryCodesCommitFailureRouterFixture(t *testing.T, loginLimit int) (recoveryCodesRouterFixture, *recoveryCodesCommitFailureState) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RECOVERY_ROUTER_DATA_KEY_32_BYTES_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	gin.SetMode(gin.TestMode)

	recoveryCodesCommitFailureDriverOnce.Do(func() {
		sql.Register(recoveryCodesCommitFailureDriverName, recoveryCodesCommitFailureDriver{})
	})
	state := &recoveryCodesCommitFailureState{}
	recoveryCodesCommitFailureDriverState.Store(state)
	t.Cleanup(func() {
		recoveryCodesCommitFailureDriverState.Store(nil)
	})

	dsn := "file:" + filepath.Join(t.TempDir(), "recovery-router-commit.sqlite") + "?_loc=UTC"
	db, err := gorm.Open(sqlite.New(sqlite.Config{
		DriverName: recoveryCodesCommitFailureDriverName,
		DSN:        dsn,
	}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open commit-failure recovery route database: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	} else {
		t.Fatalf("get commit-failure recovery route database: %v", err)
	}

	fixture := newRecoveryCodesRouterFixtureWithDB(t, db, loginLimit)
	state.failCommit.Store(true)
	return fixture, state
}

func (f *recoveryCodesRouterFixture) addUser(t *testing.T, role string, totpEnabled bool) recoveryCodesRouterUser {
	t.Helper()
	key, err := auth.GenerateTOTPSecret("test", "recovery-router-"+role)
	if err != nil {
		t.Fatalf("generate %s TOTP secret: %v", role, err)
	}
	user := model.User{
		Username:     "recovery-router-" + role,
		PasswordHash: "FAKE_RECOVERY_ROUTER_PASSWORD_HASH_FOR_TEST_ONLY",
		Role:         role,
		TokenVersion: 1,
		TOTPSecret:   key.Secret(),
		TOTPEnabled:  totpEnabled,
		RecoveryCodes: `[
			"FAKE_RECOVERY_ROUTER_OLD_CODE_FOR_TEST_ONLY"
		]`,
	}
	if err := f.db.Create(&user).Error; err != nil {
		t.Fatalf("create %s user: %v", role, err)
	}
	var stored model.User
	if err := f.db.First(&stored, user.ID).Error; err != nil {
		t.Fatalf("reload %s user: %v", role, err)
	}
	token, err := f.manager.GenerateToken(stored)
	if err != nil {
		t.Fatalf("generate %s token: %v", role, err)
	}
	entry := recoveryCodesRouterUser{user: stored, token: token, secret: key.Secret()}
	f.users[role] = entry
	return entry
}

func recoveryCodesRequest(t *testing.T, router http.Handler, method, path, token, body, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = remoteAddr
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func recoveryCodesErrorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Data struct {
			ErrorCode string `json:"error_code"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode recovery response: %v; body=%s", err, response.Body.String())
	}
	return envelope.Data.ErrorCode
}

func TestRecoveryCodesRemainingAndRegenerateRecoveryCodesRoutesAreRegistered(t *testing.T) {
	routes := NewRouter(Dependencies{}).Routes()
	if !hasRoute(routes, http.MethodGet, "/api/v1/auth/2fa/recovery-codes") {
		t.Fatal("GET recovery-code inventory route is missing")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/auth/2fa/recovery-codes/regenerate") {
		t.Fatal("POST recovery-code regeneration route is missing")
	}
}

func TestRecoveryCodesRemainingAndRegenerateRecoveryCodesRouterSelfOnlyRolesAndBusinessErrors(t *testing.T) {
	fixture := newRecoveryCodesRouterFixture(t, 20)
	other := fixture.users["viewer"]

	for index, role := range []string{"admin", "operator", "viewer"} {
		entry := fixture.users[role]
		response := recoveryCodesRequest(t, fixture.router, http.MethodGet,
			"/api/v1/auth/2fa/recovery-codes", entry.token, "", fmt.Sprintf("192.0.2.%d:4321", index+1))
		if response.Code != http.StatusOK {
			t.Fatalf("%s GET status=%d body=%s", role, response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s GET cache-control=%q", role, response.Header().Get("Cache-Control"))
		}
		var envelope struct {
			Data struct {
				Remaining int `json:"remaining"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("%s GET response: %v", role, err)
		}
		if envelope.Data.Remaining != 1 {
			t.Fatalf("%s remaining=%d, want 1", role, envelope.Data.Remaining)
		}
	}

	admin := fixture.users["admin"]
	beforeAdmin := rawRecoveryCodesRouterValue(t, fixture.db, admin.user.ID)
	beforeOther := rawRecoveryCodesRouterValue(t, fixture.db, other.user.ID)
	validCode, err := totp.GenerateCode(admin.secret, time.Now())
	if err != nil {
		t.Fatalf("generate valid TOTP code: %v", err)
	}
	response := recoveryCodesRequest(t, fixture.router, http.MethodPost,
		"/api/v1/auth/2fa/recovery-codes/regenerate", admin.token,
		fmt.Sprintf(`{"code":%q,"user_id":%d}`, validCode, other.user.ID), "198.51.100.10:4321")
	if response.Code != http.StatusOK {
		t.Fatalf("regeneration status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("regeneration cache-control=%q", response.Header().Get("Cache-Control"))
	}
	var regeneration struct {
		Data struct {
			RecoveryCodes []string `json:"recovery_codes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &regeneration); err != nil {
		t.Fatalf("decode regeneration response: %v", err)
	}
	if len(regeneration.Data.RecoveryCodes) != 8 {
		t.Fatalf("regenerated code count=%d, want 8", len(regeneration.Data.RecoveryCodes))
	}
	if after := rawRecoveryCodesRouterValue(t, fixture.db, admin.user.ID); after == beforeAdmin {
		t.Fatal("self regeneration did not change target inventory")
	}
	if after := rawRecoveryCodesRouterValue(t, fixture.db, other.user.ID); after != beforeOther {
		t.Fatal("unrelated user_id changed another user's inventory")
	}

	wrongCode := "000000"
	if wrongCode == validCode {
		wrongCode = "999999"
	}
	beforeWrongCode := rawRecoveryCodesRouterValue(t, fixture.db, admin.user.ID)
	response = recoveryCodesRequest(t, fixture.router, http.MethodPost,
		"/api/v1/auth/2fa/recovery-codes/regenerate", admin.token,
		fmt.Sprintf(`{"code":%q}`, wrongCode), "198.51.100.11:4321")
	if response.Code != http.StatusForbidden || recoveryCodesErrorCode(t, response) != "RECOVERY_CODE_TOTP_INVALID" {
		t.Fatalf("wrong TOTP status=%d error_code=%q body=%s", response.Code, recoveryCodesErrorCode(t, response), response.Body.String())
	}
	if raw := rawRecoveryCodesRouterValue(t, fixture.db, admin.user.ID); raw != beforeWrongCode {
		t.Fatal("wrong TOTP changed recovery inventory")
	}

	response = recoveryCodesRequest(t, fixture.router, http.MethodPost,
		"/api/v1/auth/2fa/recovery-codes/regenerate", admin.token, `{}`, "198.51.100.12:4321")
	if response.Code != http.StatusBadRequest || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("missing TOTP status=%d cache-control=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}

	if err := fixture.db.Model(&model.User{}).Where("id = ?", other.user.ID).Update("totp_enabled", false).Error; err != nil {
		t.Fatalf("disable viewer TOTP for route test: %v", err)
	}
	response = recoveryCodesRequest(t, fixture.router, http.MethodGet,
		"/api/v1/auth/2fa/recovery-codes", other.token, "", "198.51.100.13:4321")
	if response.Code != http.StatusConflict || recoveryCodesErrorCode(t, response) != "RECOVERY_CODE_TOTP_DISABLED" {
		t.Fatalf("disabled TOTP status=%d error_code=%q body=%s", response.Code, recoveryCodesErrorCode(t, response), response.Body.String())
	}

	if err := fixture.db.Table("users").Where("id = ?", admin.user.ID).Update("recovery_codes", "[").Error; err != nil {
		t.Fatalf("corrupt recovery inventory for route test: %v", err)
	}
	response = recoveryCodesRequest(t, fixture.router, http.MethodGet,
		"/api/v1/auth/2fa/recovery-codes", admin.token, "", "198.51.100.14:4321")
	if response.Code != http.StatusInternalServerError || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("corrupt inventory status=%d cache-control=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	if strings.Contains(response.Body.String(), "recovery_codes") || strings.Contains(response.Body.String(), "[") {
		t.Fatalf("generic recovery error leaked storage detail: %s", response.Body.String())
	}
}

func TestRegenerateRecoveryCodesRouterAllowsAllPrimaryRoles(t *testing.T) {
	fixture := newRecoveryCodesRouterFixture(t, 100)
	for index, role := range []string{"admin", "operator", "viewer"} {
		entry := fixture.users[role]
		before := rawRecoveryCodesRouterValue(t, fixture.db, entry.user.ID)
		code, err := totp.GenerateCode(entry.secret, time.Now())
		if err != nil {
			t.Fatalf("%s generate TOTP code: %v", role, err)
		}
		response := recoveryCodesRequest(t, fixture.router, http.MethodPost,
			"/api/v1/auth/2fa/recovery-codes/regenerate", entry.token,
			fmt.Sprintf(`{"code":%q}`, code), fmt.Sprintf("198.51.100.%d:4321", index+20))
		if response.Code != http.StatusOK {
			t.Fatalf("%s POST status=%d body=%s", role, response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s POST cache-control=%q", role, response.Header().Get("Cache-Control"))
		}
		var envelope struct {
			Data struct {
				RecoveryCodes []string `json:"recovery_codes"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("%s decode regeneration response: %v", role, err)
		}
		if len(envelope.Data.RecoveryCodes) != 8 {
			t.Fatalf("%s regenerated code count=%d, want 8", role, len(envelope.Data.RecoveryCodes))
		}
		if after := rawRecoveryCodesRouterValue(t, fixture.db, entry.user.ID); after == before {
			t.Fatalf("%s regeneration did not replace its own inventory", role)
		}
	}
}

func TestRegenerateRecoveryCodesRouterRejectsInvalidSessionKindsWithNoStoreAndNoMutation(t *testing.T) {
	fixture := newRecoveryCodesRouterFixture(t, 100)
	admin := fixture.users["admin"]
	cases := []struct {
		name   string
		token  string
		target recoveryCodesRouterUser
	}{
		{name: "missing", target: admin},
	}

	pending, err := fixture.manager.Generate2FAPendingToken(admin.user)
	if err != nil {
		t.Fatalf("generate pending token: %v", err)
	}
	cases = append(cases, struct {
		name   string
		token  string
		target recoveryCodesRouterUser
	}{name: "pending", token: pending, target: admin})

	stepUp, _, err := fixture.manager.GenerateStepUpToken(admin.user, auth.StepUpActionTaskManualTrigger)
	if err != nil {
		t.Fatalf("generate step-up token: %v", err)
	}
	cases = append(cases, struct {
		name   string
		token  string
		target recoveryCodesRouterUser
	}{name: "step-up", token: stepUp, target: admin})

	unknown := fixture.addUser(t, "unknown", true)
	cases = append(cases, struct {
		name   string
		token  string
		target recoveryCodesRouterUser
	}{name: "unknown role", token: unknown.token, target: unknown})

	stale := fixture.users["operator"]
	cases = append(cases, struct {
		name   string
		token  string
		target recoveryCodesRouterUser
	}{name: "stale token version", token: stale.token, target: stale})
	if err := fixture.db.Model(&model.User{}).Where("id = ?", stale.user.ID).Update("token_version", gorm.Expr("token_version + ?", 1)).Error; err != nil {
		t.Fatalf("stale token update: %v", err)
	}

	revoked := fixture.users["viewer"]
	claims, err := fixture.manager.ParseToken(revoked.token)
	if err != nil {
		t.Fatalf("parse token to revoke: %v", err)
	}
	if err := fixture.manager.RevokeSession(claims.ID, claims.UserID, claims.ExpiresAt.Time); err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	cases = append(cases, struct {
		name   string
		token  string
		target recoveryCodesRouterUser
	}{name: "revoked", token: revoked.token, target: revoked})

	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := rawRecoveryCodesRouterValue(t, fixture.db, tc.target.user.ID)
			code, err := totp.GenerateCode(tc.target.secret, time.Now())
			if err != nil {
				t.Fatalf("generate denial TOTP code: %v", err)
			}
			response := recoveryCodesRequest(t, fixture.router, http.MethodPost,
				"/api/v1/auth/2fa/recovery-codes/regenerate", tc.token,
				fmt.Sprintf(`{"code":%q}`, code), fmt.Sprintf("203.0.113.%d:4321", index+20))
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("cache-control=%q", response.Header().Get("Cache-Control"))
			}
			if strings.Contains(response.Body.String(), "RECOVERY_CODE_") {
				t.Fatalf("session denial exposed a recovery business code: %s", response.Body.String())
			}
			if after := rawRecoveryCodesRouterValue(t, fixture.db, tc.target.user.ID); after != before {
				t.Fatal("invalid session changed recovery inventory")
			}
		})
	}
}

func TestRegenerateRecoveryCodesRouterDisabledReturnsMachineCodeWithoutMutation(t *testing.T) {
	fixture := newRecoveryCodesRouterFixture(t, 100)
	entry := fixture.users["operator"]
	before := rawRecoveryCodesRouterValue(t, fixture.db, entry.user.ID)
	if err := fixture.db.Model(&model.User{}).Where("id = ?", entry.user.ID).Update("totp_enabled", false).Error; err != nil {
		t.Fatalf("disable operator TOTP: %v", err)
	}
	code, err := totp.GenerateCode(entry.secret, time.Now())
	if err != nil {
		t.Fatalf("generate disabled-account TOTP code: %v", err)
	}
	response := recoveryCodesRequest(t, fixture.router, http.MethodPost,
		"/api/v1/auth/2fa/recovery-codes/regenerate", entry.token,
		fmt.Sprintf(`{"code":%q}`, code), "198.51.100.80:4321")
	if response.Code != http.StatusConflict || recoveryCodesErrorCode(t, response) != "RECOVERY_CODE_TOTP_DISABLED" {
		t.Fatalf("disabled TOTP status=%d error_code=%q body=%s", response.Code, recoveryCodesErrorCode(t, response), response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("disabled TOTP cache-control=%q", response.Header().Get("Cache-Control"))
	}
	if after := rawRecoveryCodesRouterValue(t, fixture.db, entry.user.ID); after != before {
		t.Fatal("disabled TOTP changed recovery inventory")
	}
}

func TestRegenerateRecoveryCodesRouterBaselineConflictReturnsMachineCode(t *testing.T) {
	fixture := newRecoveryCodesRouterFixture(t, 100)
	entry := fixture.users["admin"]
	replacement := `["FAKE_RECOVERY_ROUTER_BASELINE_CONFLICT_CODE_FOR_TEST_ONLY"]`
	injected := false
	callbackName := fmt.Sprintf("test:recovery-router-baseline-conflict-%d", time.Now().UnixNano())
	if err := fixture.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if injected || tx.Statement == nil || tx.Statement.Table != "users" {
			return
		}
		selected := strings.ToLower(strings.Join(tx.Statement.Selects, ","))
		query := strings.ToLower(tx.Statement.SQL.String())
		if (!strings.Contains(selected, "totp_secret") || !strings.Contains(selected, "recovery_codes")) &&
			(!strings.Contains(query, "totp_secret") || !strings.Contains(query, "recovery_codes")) {
			return
		}
		injected = true
		if err := fixture.db.Table("users").Where("id = ?", entry.user.ID).Update("recovery_codes", replacement).Error; err != nil {
			_ = tx.AddError(err)
		}
	}); err != nil {
		t.Fatalf("register baseline conflict callback: %v", err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove baseline conflict callback: %v", err)
		}
	})

	code, err := totp.GenerateCode(entry.secret, time.Now())
	if err != nil {
		t.Fatalf("generate baseline conflict TOTP code: %v", err)
	}
	response := recoveryCodesRequest(t, fixture.router, http.MethodPost,
		"/api/v1/auth/2fa/recovery-codes/regenerate", entry.token,
		fmt.Sprintf(`{"code":%q}`, code), "198.51.100.81:4321")
	if response.Code != http.StatusConflict || recoveryCodesErrorCode(t, response) != "RECOVERY_CODE_CONFLICT" {
		t.Fatalf("baseline conflict status=%d error_code=%q body=%s", response.Code, recoveryCodesErrorCode(t, response), response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("baseline conflict cache-control=%q", response.Header().Get("Cache-Control"))
	}
	if !injected {
		t.Fatal("baseline conflict callback did not run")
	}
	if after := rawRecoveryCodesRouterValue(t, fixture.db, entry.user.ID); after != replacement {
		t.Fatalf("baseline conflict altered external inventory: %q", after)
	}
}

func TestRegenerateRecoveryCodesRouterCASConflictReturnsMachineCode(t *testing.T) {
	fixture := newRecoveryCodesRouterFixture(t, 100)
	entry := fixture.users["admin"]
	before := rawRecoveryCodesRouterValue(t, fixture.db, entry.user.ID)
	injected := false
	callbackName := fmt.Sprintf("test:recovery-router-cas-conflict-%d", time.Now().UnixNano())
	if err := fixture.db.Callback().Update().After("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if injected || tx.Statement == nil || tx.Statement.Table != "users" {
			return
		}
		injected = true
		tx.RowsAffected = 0
	}); err != nil {
		t.Fatalf("register CAS conflict callback: %v", err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Update().Remove(callbackName); err != nil {
			t.Errorf("remove CAS conflict callback: %v", err)
		}
	})

	code, err := totp.GenerateCode(entry.secret, time.Now())
	if err != nil {
		t.Fatalf("generate CAS conflict TOTP code: %v", err)
	}
	response := recoveryCodesRequest(t, fixture.router, http.MethodPost,
		"/api/v1/auth/2fa/recovery-codes/regenerate", entry.token,
		fmt.Sprintf(`{"code":%q}`, code), "198.51.100.82:4321")
	if response.Code != http.StatusConflict || recoveryCodesErrorCode(t, response) != "RECOVERY_CODE_CONFLICT" {
		t.Fatalf("CAS conflict status=%d error_code=%q body=%s", response.Code, recoveryCodesErrorCode(t, response), response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("CAS conflict cache-control=%q", response.Header().Get("Cache-Control"))
	}
	if !injected {
		t.Fatal("CAS conflict callback did not run")
	}
	if after := rawRecoveryCodesRouterValue(t, fixture.db, entry.user.ID); after != before {
		t.Fatal("CAS conflict changed recovery inventory")
	}
}

func TestRegenerateRecoveryCodesRouterCommitFailureIsGenericAmbiguous(t *testing.T) {
	fixture, state := newRecoveryCodesCommitFailureRouterFixture(t, 100)
	entry := fixture.users["admin"]
	before := rawRecoveryCodesRouterValue(t, fixture.db, entry.user.ID)
	code, err := totp.GenerateCode(entry.secret, time.Now())
	if err != nil {
		t.Fatalf("generate commit-failure TOTP code: %v", err)
	}
	response := recoveryCodesRequest(t, fixture.router, http.MethodPost,
		"/api/v1/auth/2fa/recovery-codes/regenerate", entry.token,
		fmt.Sprintf(`{"code":%q}`, code), "198.51.100.83:4321")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("commit failure status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("commit failure cache-control=%q", response.Header().Get("Cache-Control"))
	}
	if code := recoveryCodesErrorCode(t, response); code != "" {
		t.Fatalf("commit failure returned business error_code=%q body=%s", code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "RECOVERY_CODE_") ||
		strings.Contains(response.Body.String(), "FAKE_RECOVERY_COMMIT_FAILURE_FOR_TEST_ONLY") {
		t.Fatalf("commit failure exposed a business or driver detail: %s", response.Body.String())
	}
	if state.failCommit.Load() {
		t.Fatal("commit failure fixture did not inject the commit error")
	}
	if after := rawRecoveryCodesRouterValue(t, fixture.db, entry.user.ID); after == before {
		t.Fatal("commit failure unexpectedly left inventory unchanged; ambiguous result must reflect the committed replacement")
	}
}

func TestRecoveryCodesRemainingRouterRejectsInvalidSessionKindsWithNoStore(t *testing.T) {
	fixture := newRecoveryCodesRouterFixture(t, 20)
	admin := fixture.users["admin"]
	cases := []struct {
		name  string
		token string
	}{
		{name: "missing"},
	}

	pending, err := fixture.manager.Generate2FAPendingToken(admin.user)
	if err != nil {
		t.Fatalf("generate pending token: %v", err)
	}
	cases = append(cases, struct {
		name  string
		token string
	}{name: "pending", token: pending})

	stepUp, _, err := fixture.manager.GenerateStepUpToken(admin.user, auth.StepUpActionTaskManualTrigger)
	if err != nil {
		t.Fatalf("generate step-up token: %v", err)
	}
	cases = append(cases, struct {
		name  string
		token string
	}{name: "step-up", token: stepUp})

	unknown := fixture.addUser(t, "unknown", true)
	cases = append(cases, struct {
		name  string
		token string
	}{name: "unknown role", token: unknown.token})

	stale := fixture.users["operator"]
	cases = append(cases, struct {
		name  string
		token string
	}{name: "stale token version", token: stale.token})
	if err := fixture.db.Model(&model.User{}).Where("id = ?", stale.user.ID).Update("token_version", gorm.Expr("token_version + ?", 1)).Error; err != nil {
		t.Fatalf("stale token update: %v", err)
	}

	revoked := fixture.users["viewer"]
	claims, err := fixture.manager.ParseToken(revoked.token)
	if err != nil {
		t.Fatalf("parse token to revoke: %v", err)
	}
	if err := fixture.manager.RevokeSession(claims.ID, claims.UserID, claims.ExpiresAt.Time); err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	cases = append(cases, struct {
		name  string
		token string
	}{name: "revoked", token: revoked.token})

	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := recoveryCodesRequest(t, fixture.router, http.MethodGet,
				"/api/v1/auth/2fa/recovery-codes", tc.token, "", fmt.Sprintf("203.0.113.%d:4321", index+1))
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("cache-control=%q", response.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestRegenerateRecoveryCodesRouterRateLimitDenialIsNoStore(t *testing.T) {
	fixture := newRecoveryCodesRouterFixture(t, 1)
	admin := fixture.users["admin"]
	for index := range 2 {
		response := recoveryCodesRequest(t, fixture.router, http.MethodPost,
			"/api/v1/auth/2fa/recovery-codes/regenerate", admin.token, `{}`, "192.0.2.200:4321")
		if index == 0 {
			if response.Code != http.StatusBadRequest {
				t.Fatalf("first request status=%d body=%s", response.Code, response.Body.String())
			}
			continue
		}
		if response.Code != http.StatusTooManyRequests {
			t.Fatalf("rate-limited status=%d body=%s", response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Retry-After") == "" {
			t.Fatalf("rate-limited headers=%v", response.Header())
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || len(envelope) != 3 {
			t.Fatalf("rate-limited envelope=%s err=%v", response.Body.String(), err)
		}
	}
}

func rawRecoveryCodesRouterValue(t *testing.T, db *gorm.DB, userID uint) string {
	t.Helper()
	var row struct {
		RecoveryCodes string `gorm:"column:recovery_codes"`
	}
	if err := db.Table("users").Select("recovery_codes").Where("id = ?", userID).Take(&row).Error; err != nil {
		t.Fatalf("read raw recovery inventory: %v", err)
	}
	return row.RecoveryCodes
}
