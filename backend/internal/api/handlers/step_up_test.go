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
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/pquerna/otp/totp"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const (
	stepUpTestJWTSecret = "step-up-test-signing-marker"
	stepUpTestSessionID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func openStepUpHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", handlerTestDBName(t))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开 step-up 测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{},
		&model.CredentialAuditEvent{},
		&model.CredentialAccessGrant{},
		&model.AuditLog{},
		&model.Node{},
		&model.NodeOwner{},
		&model.Policy{},
		&model.Task{},
		&model.TaskRun{},
		&model.SSHKey{},
		&model.SystemSetting{},
	); err != nil {
		t.Fatalf("初始化 step-up 测试表失败: %v", err)
	}
	return db
}

func seedStepUpUser(t *testing.T, db *gorm.DB, username, role string) model.User {
	t.Helper()
	key, err := auth.GenerateTOTPSecret("Xirang Test", username)
	if err != nil {
		t.Fatalf("生成 TOTP secret 失败: %v", err)
	}
	secret := key.Secret()
	user := model.User{
		Username:     username,
		Role:         role,
		PasswordHash: "hash-redacted",
		TOTPEnabled:  true,
		TOTPSecret:   secret,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("创建 step-up 测试用户失败: %v", err)
	}
	user.TOTPSecret = secret
	return user
}

func currentStepUpCode(t *testing.T, user model.User) string {
	t.Helper()
	code, err := totp.GenerateCode(user.TOTPSecret, time.Now())
	if err != nil {
		t.Fatalf("生成 TOTP code 失败: %v", err)
	}
	return code
}

func signExpiredStepUpProofForTest(t *testing.T, user model.User) string {
	return signExpiredStepUpProofForActionForTest(t, user, auth.StepUpActionTaskManualTrigger)
}

func signExpiredStepUpProofForActionForTest(t *testing.T, user model.User, action auth.StepUpAction) string {
	t.Helper()
	now := time.Now()
	claims := auth.Claims{
		UserID:       user.ID,
		Username:     user.Username,
		Role:         user.Role,
		Purpose:      auth.PurposeStepUp,
		StepUpAction: action,
		SessionID:    stepUpTestSessionID,
		TokenVersion: user.TokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        fmt.Sprintf("expired-step-up-%d", user.ID),
			IssuedAt:  jwt.NewNumericDate(now.Add(-10 * time.Minute)),
			ExpiresAt: jwt.NewNumericDate(now.Add(-5 * time.Minute)),
			Subject:   fmt.Sprintf("%d", user.ID),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(stepUpTestJWTSecret))
	if err != nil {
		t.Fatalf("签发过期 step-up proof 失败: %v", err)
	}
	return signed
}

func TestOptionalStepUpAcceptsOnlyExactAssetSecretRevealAndFailsInfrastructureClosed(t *testing.T) {
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	user := seedStepUpUser(t, db, "optional-secret-reveal", "admin")
	other := seedStepUpUser(t, db, "optional-secret-other", "admin")
	exactProof := generateStepUpProofForAction(t, manager, user, auth.StepUpActionAssetSecretReveal)

	claims, err := VerifyOptionalStepUpProof(db, manager, "", user.ID, user.Role, auth.StepUpActionAssetSecretReveal, stepUpTestSessionID)
	if err != nil || claims != nil {
		t.Fatalf("absent optional proof should remain absent: claims=%+v err=%v", claims, err)
	}
	invalidProofs := map[string]string{
		"malformed":     "malformed-proof",
		"expired":       signExpiredStepUpProofForActionForTest(t, user, auth.StepUpActionAssetSecretReveal),
		"wrong user":    generateStepUpProofForAction(t, manager, other, auth.StepUpActionAssetSecretReveal),
		"wrong role":    generateStepUpProofForAction(t, manager, model.User{ID: user.ID, Username: user.Username, Role: "operator", TokenVersion: user.TokenVersion, TOTPEnabled: true}, auth.StepUpActionAssetSecretReveal),
		"token version": generateStepUpProofForAction(t, manager, model.User{ID: user.ID, Username: user.Username, Role: user.Role, TokenVersion: user.TokenVersion + 1, TOTPEnabled: true}, auth.StepUpActionAssetSecretReveal),
	}
	for name, proof := range invalidProofs {
		t.Run(name, func(t *testing.T) {
			claims, err := VerifyOptionalStepUpProof(db, manager, proof, user.ID, user.Role, auth.StepUpActionAssetSecretReveal, stepUpTestSessionID)
			if !errors.Is(err, ErrStepUpProofInvalid) || claims != nil {
				t.Fatalf("attached invalid optional proof was not rejected: claims=%+v err=%v", claims, err)
			}
		})
	}

	missingUser := model.User{ID: 999999, Username: "missing", Role: "admin", TokenVersion: 1, TOTPEnabled: true}
	missingProof := generateStepUpProofForAction(t, manager, missingUser, auth.StepUpActionAssetSecretReveal)
	if _, err := validateStepUpProof(db, manager, missingProof, missingUser.ID, missingUser.Role, auth.StepUpActionAssetSecretReveal, stepUpTestSessionID); !errors.Is(err, ErrStepUpProofInvalid) {
		t.Fatalf("missing proof user got %v, want invalid proof", err)
	}

	if err := db.Model(&model.User{}).Where("id = ?", user.ID).Update("totp_enabled", false).Error; err != nil {
		t.Fatalf("disable TOTP: %v", err)
	}
	claims, err = VerifyOptionalStepUpProof(db, manager, exactProof, user.ID, user.Role, auth.StepUpActionAssetSecretReveal, stepUpTestSessionID)
	if !errors.Is(err, ErrStepUpProofInvalid) || claims != nil {
		t.Fatalf("disabled TOTP proof was not rejected: claims=%+v err=%v", claims, err)
	}
	if err := db.Model(&model.User{}).Where("id = ?", user.ID).Update("totp_enabled", true).Error; err != nil {
		t.Fatalf("restore TOTP: %v", err)
	}

	for _, action := range auth.AllStepUpActions() {
		if action == auth.StepUpActionAssetSecretReveal {
			continue
		}
		proof := generateStepUpProofForAction(t, manager, user, action)
		claims, err := VerifyOptionalStepUpProof(db, manager, proof, user.ID, user.Role, auth.StepUpActionAssetSecretReveal, stepUpTestSessionID)
		if !errors.Is(err, ErrStepUpProofInvalid) || claims != nil {
			t.Fatalf("proof for %q was not rejected as secret reveal: claims=%+v err=%v", action, claims, err)
		}
	}

	claims, err = VerifyOptionalStepUpProof(db, manager, exactProof, user.ID, user.Role, auth.StepUpActionAssetSecretReveal, stepUpTestSessionID)
	if err != nil || claims == nil || claims.ID == "" || claims.ExpiresAt == nil || claims.StepUpAction != auth.StepUpActionAssetSecretReveal {
		t.Fatalf("exact secret-reveal proof rejected: claims=%+v err=%v", claims, err)
	}
	if _, err := VerifyOptionalStepUpProof(nil, manager, exactProof, user.ID, user.Role, auth.StepUpActionAssetSecretReveal, stepUpTestSessionID); !errors.Is(err, ErrStepUpVerifierUnavailable) {
		t.Fatalf("nil DB got %v, want verifier unavailable", err)
	}
	if _, err := VerifyOptionalStepUpProof(db, nil, exactProof, user.ID, user.Role, auth.StepUpActionAssetSecretReveal, stepUpTestSessionID); !errors.Is(err, ErrStepUpVerifierUnavailable) {
		t.Fatalf("nil JWT manager got %v, want verifier unavailable", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("open SQL DB: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close SQL DB: %v", err)
	}
	if _, err := VerifyOptionalStepUpProof(db, manager, exactProof, user.ID, user.Role, auth.StepUpActionAssetSecretReveal, stepUpTestSessionID); !errors.Is(err, ErrStepUpVerifierUnavailable) {
		t.Fatalf("DB infrastructure failure got %v, want verifier unavailable", err)
	}
}

func TestStepUpProofValidationBindsSessionAndExactIssuedLifetime(t *testing.T) {
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	user := seedStepUpUser(t, db, "session-bound-secret-reveal", "admin")
	proof := generateStepUpProofForAction(
		t,
		manager,
		user,
		auth.StepUpActionAssetSecretReveal,
		stepUpTestSessionID,
	)

	first, err := validateStepUpProof(
		db, manager, proof, user.ID, user.Role, auth.StepUpActionAssetSecretReveal, stepUpTestSessionID,
	)
	if err != nil {
		t.Fatalf("valid session-bound proof rejected: %v", err)
	}
	second, err := validateStepUpProof(
		db, manager, proof, user.ID, user.Role, auth.StepUpActionAssetSecretReveal, stepUpTestSessionID,
	)
	if err != nil || first.IssuedAt == nil || first.ExpiresAt == nil || second.ExpiresAt == nil ||
		!second.ExpiresAt.Equal(first.ExpiresAt.Time) {
		t.Fatalf("proof reuse changed fixed expiry: first=%+v second=%+v err=%v", first, second, err)
	}
	if _, err := validateStepUpProof(
		db, manager, proof, user.ID, user.Role, auth.StepUpActionAssetSecretReveal, "cccccccccccccccccccccccccccccccc",
	); !errors.Is(err, ErrStepUpProofInvalid) {
		t.Fatalf("wrong login session got %v, want invalid proof", err)
	}
	if _, err := validateStepUpProof(
		db, manager, proof, user.ID, "", auth.StepUpActionAssetSecretReveal, stepUpTestSessionID,
	); !errors.Is(err, ErrStepUpProofInvalid) {
		t.Fatalf("missing caller role got %v, want invalid proof", err)
	}

	tampered := proof[:len(proof)-1] + "x"
	if tampered == proof {
		tampered = proof[:len(proof)-1] + "y"
	}
	if _, err := validateStepUpProof(
		db, manager, tampered, user.ID, user.Role, auth.StepUpActionAssetSecretReveal, stepUpTestSessionID,
	); !errors.Is(err, ErrStepUpProofInvalid) {
		t.Fatalf("tampered proof got %v, want invalid proof", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	baseClaims := auth.Claims{
		UserID:       user.ID,
		Username:     user.Username,
		Role:         user.Role,
		Purpose:      auth.PurposeStepUp,
		StepUpAction: auth.StepUpActionAssetSecretReveal,
		SessionID:    stepUpTestSessionID,
		TokenVersion: user.TokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        "dddddddddddddddddddddddddddddddd",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(45 * time.Minute)),
			Subject:   fmt.Sprintf("%d", user.ID),
		},
	}
	invalidClaims := map[string]auth.Claims{
		"missing proof jti": func() auth.Claims {
			claims := baseClaims
			claims.ID = ""
			return claims
		}(),
		"missing issued-at": func() auth.Claims {
			claims := baseClaims
			claims.IssuedAt = nil
			return claims
		}(),
		"future issued-at": func() auth.Claims {
			claims := baseClaims
			claims.IssuedAt = jwt.NewNumericDate(now.Add(time.Minute))
			claims.ExpiresAt = jwt.NewNumericDate(now.Add(46 * time.Minute))
			return claims
		}(),
		"wrong lifetime": func() auth.Claims {
			claims := baseClaims
			claims.ExpiresAt = jwt.NewNumericDate(now.Add(5 * time.Minute))
			return claims
		}(),
		"wrong subject": func() auth.Claims {
			claims := baseClaims
			claims.Subject = "999"
			return claims
		}(),
		"future action": func() auth.Claims {
			claims := baseClaims
			claims.StepUpAction = auth.StepUpAction("future.action")
			return claims
		}(),
	}
	for name, claims := range invalidClaims {
		t.Run(name, func(t *testing.T) {
			signed := signStepUpClaimsForTest(t, claims)
			if _, err := validateStepUpProof(
				db, manager, signed, user.ID, user.Role, auth.StepUpActionAssetSecretReveal, stepUpTestSessionID,
			); !errors.Is(err, ErrStepUpProofInvalid) {
				t.Fatalf("invalid proof got %v, want invalid proof", err)
			}
		})
	}

	if err := manager.RevokeSession(stepUpTestSessionID, user.ID, now.Add(time.Hour)); err != nil {
		t.Fatalf("revoke primary session: %v", err)
	}
	if _, err := validateStepUpProof(
		db, manager, proof, user.ID, user.Role, auth.StepUpActionAssetSecretReveal, stepUpTestSessionID,
	); !errors.Is(err, ErrStepUpProofInvalid) {
		t.Fatalf("revoked login session got %v, want invalid proof", err)
	}
}

func TestStepUpRequiredEnvelopeReportsExactActionPolicyTTL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	user := seedStepUpUser(t, db, "step-up-required-ttl", "admin")
	primaryToken := generatePrimaryToken(t, manager, user)

	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.GET("/secret", RequireStepUp(
		db,
		manager,
		auth.StepUpActionAssetSecretReveal,
		"asset_secret_reveal",
		"content_issue",
	), func(c *gin.Context) {
		respondOK(c, gin.H{"ok": true})
	})

	resp := performStepUpRequest(t, router, http.MethodGet, "/secret", primaryToken, "", "")
	assertStepUpRequiredEnvelopeForAction(t, resp, auth.StepUpActionAssetSecretReveal)
	if got := stepUpProofTTLSecondsForAction(auth.StepUpAction("future.action")); got != 0 {
		t.Fatalf("unknown action response/audit TTL = %d, want zero", got)
	}
}

func signStepUpClaimsForTest(t *testing.T, claims auth.Claims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(stepUpTestJWTSecret))
	if err != nil {
		t.Fatalf("sign step-up claims: %v", err)
	}
	return signed
}

func generatePrimaryToken(t *testing.T, manager *auth.JWTManager, user model.User) string {
	t.Helper()
	token, err := manager.GenerateToken(user)
	if err != nil {
		t.Fatalf("生成主认证 token 失败: %v", err)
	}
	return token
}

func generateStepUpProof(t *testing.T, manager *auth.JWTManager, user model.User) string {
	t.Helper()
	return generateStepUpProofForAction(t, manager, user, auth.StepUpActionTaskManualTrigger)
}

func generateStepUpProofForAction(t *testing.T, manager *auth.JWTManager, user model.User, action auth.StepUpAction, sessionIDs ...string) string {
	t.Helper()
	sessionID := stepUpTestSessionID
	if len(sessionIDs) == 1 {
		sessionID = sessionIDs[0]
	}
	proof, _, err := manager.GenerateStepUpToken(user, action, sessionID)
	if err != nil {
		t.Fatalf("生成 step-up proof 失败: %v", err)
	}
	return proof
}

func TestStepUpRequestRequiresKnownAction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	user := seedStepUpUser(t, db, "step-up-action-request", "admin")
	primaryToken := generatePrimaryToken(t, manager, user)
	handler := NewAuthHandler(nil, manager, nil).WithDB(db)
	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.POST("/auth/step-up", handler.StepUp)
	code := currentStepUpCode(t, user)

	for _, body := range []string{
		fmt.Sprintf(`{"code":%q}`, code),
		fmt.Sprintf(`{"code":%q,"step_up_action":"future.action"}`, code),
	} {
		resp := performStepUpRequest(t, router, http.MethodPost, "/auth/step-up", primaryToken, "", body)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("unknown/missing step_up_action should return 400, got %d: %s", resp.Code, resp.Body.String())
		}
	}
}

func TestStepUpProofRejectsMissingLegacyGenericAction(t *testing.T) {
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	user := seedStepUpUser(t, db, "legacy-generic-proof", "admin")
	now := time.Now()
	claims := auth.Claims{
		UserID:       user.ID,
		Username:     user.Username,
		Role:         user.Role,
		Purpose:      auth.PurposeStepUp,
		TokenVersion: user.TokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        "legacy-generic-proof-marker",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
			Subject:   fmt.Sprintf("%d", user.ID),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	proof, err := token.SignedString([]byte(stepUpTestJWTSecret))
	if err != nil {
		t.Fatalf("sign legacy generic proof: %v", err)
	}
	if _, err := validateStepUpProof(db, manager, proof, user.ID, user.Role, auth.StepUpActionTerminalOpen); err == nil {
		t.Fatal("legacy generic step-up proof unexpectedly accepted")
	}
}

func TestStepUpProofPairwiseCrossPurposeRejection(t *testing.T) {
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	user := seedStepUpUser(t, db, "pairwise-step-up", "admin")
	actions := auth.AllStepUpActions()
	accepted := 0
	rejected := 0
	for _, issuedAction := range actions {
		proof := generateStepUpProofForAction(t, manager, user, issuedAction)
		for _, expectedAction := range actions {
			_, err := validateStepUpProof(db, manager, proof, user.ID, user.Role, expectedAction, stepUpTestSessionID)
			if issuedAction == expectedAction {
				if err != nil {
					t.Fatalf("matching action %q rejected: %v", issuedAction, err)
				}
				accepted++
				continue
			}
			if err == nil {
				t.Fatalf("proof for %q accepted as %q", issuedAction, expectedAction)
			}
			rejected++
		}
	}
	const reviewedActionCount = 18
	if len(actions) != reviewedActionCount {
		t.Fatalf("step-up registry has %d actions, want reviewed count %d", len(actions), reviewedActionCount)
	}
	wantAccepted := len(actions)
	wantRejected := len(actions) * (len(actions) - 1)
	if accepted != wantAccepted || rejected != wantRejected {
		t.Fatalf("pairwise matrix accepted=%d rejected=%d, want %d/%d", accepted, rejected, wantAccepted, wantRejected)
	}
}

func performStepUpRequest(t *testing.T, router *gin.Engine, method, path, token, proof, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if proof != "" {
		req.Header.Set(StepUpHeaderName, proof)
	}
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func assertStepUpRequiredEnvelope(t *testing.T, resp *httptest.ResponseRecorder) {
	assertStepUpRequiredEnvelopeForAction(t, resp, auth.StepUpActionTaskManualTrigger)
}

func assertStepUpRequiredEnvelopeForAction(t *testing.T, resp *httptest.ResponseRecorder, action auth.StepUpAction) {
	t.Helper()
	if resp.Code != http.StatusForbidden {
		t.Fatalf("期望 step-up required 返回 403，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Code int `json:"code"`
		Data struct {
			ErrorCode       string `json:"error_code"`
			ProofTTLSeconds int    `json:"proof_ttl_seconds"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析 step-up required 响应失败: %v", err)
	}
	wantTTL := int(auth.StepUpProofTTLForAction(action).Seconds())
	if payload.Code != http.StatusForbidden || payload.Data.ErrorCode != stepUpRequiredCode || payload.Data.ProofTTLSeconds != wantTTL {
		t.Fatalf("step-up required 响应缺少机器可读字段: %+v", payload)
	}
}

func loadCredentialAuditEvents(t *testing.T, db *gorm.DB, action string) []model.CredentialAuditEvent {
	t.Helper()
	var events []model.CredentialAuditEvent
	if err := db.Where("action = ?", action).Order("id asc").Find(&events).Error; err != nil {
		t.Fatalf("读取凭据审计事件失败: %v", err)
	}
	return events
}

func assertNoForbiddenAuditMetadata(t *testing.T, metadata string) map[string]any {
	t.Helper()
	lower := strings.ToLower(metadata)
	for _, marker := range []string{"token", "credential", "config", "command", "content", "payload", "output", "stream", "private", "password", "secret"} {
		if strings.Contains(lower, marker) {
			t.Fatalf("凭据审计 metadata 不应包含禁用标记 %q: %s", marker, metadata)
		}
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(metadata), &parsed); err != nil {
		t.Fatalf("解析凭据审计 metadata 失败: %v", err)
	}
	return parsed
}

func TestAuthHandlerStepUpIssuesProofForEnabledTOTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	user := seedStepUpUser(t, db, "step-up-admin", "admin")
	primaryToken := generatePrimaryToken(t, manager, user)

	handler := NewAuthHandler(nil, manager, nil).WithDB(db)
	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.POST("/auth/step-up", handler.StepUp)

	resp := performStepUpRequest(t, router, http.MethodPost, "/auth/step-up", primaryToken, "", fmt.Sprintf(`{"code":%q,"step_up_action":%q}`, currentStepUpCode(t, user), auth.StepUpActionTaskManualTrigger))
	if resp.Code != http.StatusOK {
		t.Fatalf("step-up 成功期望 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Data struct {
			Proof           string `json:"proof"`
			ExpiresAt       string `json:"expires_at"`
			ProofTTLSeconds int    `json:"proof_ttl_seconds"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析 step-up 响应失败: %v", err)
	}
	if payload.Data.Proof == "" || payload.Data.ProofTTLSeconds != stepUpProofTTLSeconds || payload.Data.ExpiresAt == "" {
		t.Fatalf("step-up 响应缺少 proof/expiry/ttl: %+v", payload.Data)
	}
	claims, err := manager.ParseToken(payload.Data.Proof)
	if err != nil {
		t.Fatalf("解析返回 proof 失败: %v", err)
	}
	if claims.Purpose != auth.PurposeStepUp || claims.StepUpAction != auth.StepUpActionTaskManualTrigger || claims.UserID != user.ID || claims.TokenVersion != user.TokenVersion {
		t.Fatalf("返回 proof claims 不符合预期: %+v", claims)
	}

	events := loadCredentialAuditEvents(t, db, string(auth.StepUpActionTaskManualTrigger))
	if len(events) != 1 || events[0].Outcome != credentialaudit.OutcomeSuccess || events[0].UserID != user.ID {
		t.Fatalf("step-up 成功审计事件不符合预期: %+v", events)
	}
	metadata := assertNoForbiddenAuditMetadata(t, events[0].Metadata)
	if metadata["stage"] != "step_up" || metadata["proof"] != "issued" || metadata["operation"] != "step_up" {
		t.Fatalf("step-up 成功审计 metadata 不符合预期: %#v", metadata)
	}
}

func TestAuthHandlerStepUpReportsExactActionPolicyTTL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	user := seedStepUpUser(t, db, "step-up-action-ttl", "admin")
	primaryToken := generatePrimaryToken(t, manager, user)
	handler := NewAuthHandler(nil, manager, nil).WithDB(db)
	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.POST("/auth/step-up", handler.StepUp)

	for _, tc := range []struct {
		action  auth.StepUpAction
		seconds int
	}{
		{action: auth.StepUpActionTaskManualTrigger, seconds: 300},
		{action: auth.StepUpActionAssetSecretReveal, seconds: 45 * 60},
	} {
		t.Run(string(tc.action), func(t *testing.T) {
			resp := performStepUpRequest(
				t,
				router,
				http.MethodPost,
				"/auth/step-up",
				primaryToken,
				"",
				fmt.Sprintf(`{"code":%q,"step_up_action":%q}`, currentStepUpCode(t, user), tc.action),
			)
			if resp.Code != http.StatusOK {
				t.Fatalf("step-up status=%d body=%s", resp.Code, resp.Body.String())
			}
			var payload struct {
				Data stepUpResponse `json:"data"`
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode step-up response: %v", err)
			}
			if payload.Data.ProofTTLSeconds != tc.seconds {
				t.Fatalf("proof_ttl_seconds for %q = %d, want %d", tc.action, payload.Data.ProofTTLSeconds, tc.seconds)
			}
			claims, err := manager.ParseToken(payload.Data.Proof)
			if err != nil || claims.IssuedAt == nil || claims.ExpiresAt == nil {
				t.Fatalf("parse issued proof for %q: claims=%+v err=%v", tc.action, claims, err)
			}
			if got := int(claims.ExpiresAt.Sub(claims.IssuedAt.Time).Seconds()); got != tc.seconds {
				t.Fatalf("claim TTL for %q = %d, want %d", tc.action, got, tc.seconds)
			}

			events := loadCredentialAuditEvents(t, db, string(tc.action))
			if len(events) != 1 {
				t.Fatalf("audit events for %q = %d, want 1", tc.action, len(events))
			}
			metadata := assertNoForbiddenAuditMetadata(t, events[0].Metadata)
			if metadata["ttl_seconds"] != float64(tc.seconds) {
				t.Fatalf("audit TTL for %q = %#v, want %d", tc.action, metadata["ttl_seconds"], tc.seconds)
			}
		})
	}
}

func TestAuthHandlerStepUpRejectsDisabledOrInvalidTOTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	user := seedStepUpUser(t, db, "step-up-disabled", "admin")
	validCode := currentStepUpCode(t, user)
	if err := db.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]any{"totp_enabled": false, "totp_secret": ""}).Error; err != nil {
		t.Fatalf("禁用测试用户 TOTP 失败: %v", err)
	}
	user.TOTPEnabled = false
	user.TOTPSecret = ""
	primaryToken := generatePrimaryToken(t, manager, user)

	handler := NewAuthHandler(nil, manager, nil).WithDB(db)
	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.POST("/auth/step-up", handler.StepUp)

	disabledResp := performStepUpRequest(t, router, http.MethodPost, "/auth/step-up", primaryToken, "", fmt.Sprintf(`{"code":%q,"step_up_action":%q}`, validCode, auth.StepUpActionTaskManualTrigger))
	if disabledResp.Code != http.StatusForbidden {
		t.Fatalf("TOTP disabled step-up 期望 403，实际: %d，响应: %s", disabledResp.Code, disabledResp.Body.String())
	}

	enabledUser := seedStepUpUser(t, db, "step-up-invalid", "admin")
	enabledToken := generatePrimaryToken(t, manager, enabledUser)
	invalidCode := currentStepUpCode(t, enabledUser)
	invalidCode = invalidCode[:len(invalidCode)-1] + "9"
	if invalidCode == currentStepUpCode(t, enabledUser) {
		invalidCode = invalidCode[:len(invalidCode)-1] + "8"
	}
	invalidResp := performStepUpRequest(t, router, http.MethodPost, "/auth/step-up", enabledToken, "", fmt.Sprintf(`{"code":%q,"step_up_action":%q}`, invalidCode, auth.StepUpActionTaskManualTrigger))
	if invalidResp.Code != http.StatusForbidden {
		t.Fatalf("invalid TOTP step-up 期望 403，实际: %d，响应: %s", invalidResp.Code, invalidResp.Body.String())
	}

	events := loadCredentialAuditEvents(t, db, string(auth.StepUpActionTaskManualTrigger))
	if len(events) != 2 || events[0].Outcome != credentialaudit.OutcomeBlocked || events[1].Outcome != credentialaudit.OutcomeFailure {
		t.Fatalf("step-up 失败/blocked 审计事件不符合预期: %+v", events)
	}
	for _, event := range events {
		assertNoForbiddenAuditMetadata(t, event.Metadata)
	}
}

func TestStepUpMiddlewareValidatesMissingInvalidExpiredWrongUserAndTokenVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	user := seedStepUpUser(t, db, "step-up-operator", "operator")
	otherUser := seedStepUpUser(t, db, "step-up-other", "operator")
	primaryToken := generatePrimaryToken(t, manager, user)
	validProof := generateStepUpProof(t, manager, user)
	wrongUserProof := generateStepUpProof(t, manager, otherUser)
	expiredProof := signExpiredStepUpProofForTest(t, user)

	newRouter := func() *gin.Engine {
		router := gin.New()
		router.Use(middleware.AuthMiddleware(manager, db))
		router.GET("/protected", RequireStepUp(db, manager, auth.StepUpActionTaskManualTrigger, "task_command", "task_run"), func(c *gin.Context) {
			respondOK(c, gin.H{"ok": true})
		})
		return router
	}

	cases := []struct {
		name  string
		proof string
	}{
		{name: "missing"},
		{name: "invalid", proof: "malformed-proof"},
		{name: "expired", proof: expiredProof},
		{name: "wrong-user", proof: wrongUserProof},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := performStepUpRequest(t, newRouter(), http.MethodGet, "/protected", primaryToken, tc.proof, "")
			assertStepUpRequiredEnvelope(t, resp)
		})
	}

	staleProof := generateStepUpProof(t, manager, user)
	if err := db.Model(&model.User{}).Where("id = ?", user.ID).Update("token_version", gorm.Expr("token_version + 1")).Error; err != nil {
		t.Fatalf("更新 token_version 失败: %v", err)
	}
	resp := performStepUpRequest(t, newRouter(), http.MethodGet, "/protected", primaryToken, staleProof, "")
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("主 token version 过期时应先返回 401，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	if err := db.Model(&model.User{}).Where("id = ?", user.ID).Update("token_version", 0).Error; err != nil {
		t.Fatalf("恢复 token_version 失败: %v", err)
	}
	if err := db.Model(&model.User{}).Where("id = ?", user.ID).Update("token_version", 1).Error; err != nil {
		t.Fatalf("仅使 step-up proof 过期失败: %v", err)
	}
	freshPrimary := generatePrimaryToken(t, manager, model.User{ID: user.ID, Username: user.Username, Role: user.Role, TokenVersion: 1})
	staleProofResp := performStepUpRequest(t, newRouter(), http.MethodGet, "/protected", freshPrimary, validProof, "")
	assertStepUpRequiredEnvelope(t, staleProofResp)

	freshProof := generateStepUpProof(t, manager, model.User{ID: user.ID, Username: user.Username, Role: user.Role, TokenVersion: 1, TOTPEnabled: true})
	okResp := performStepUpRequest(t, newRouter(), http.MethodGet, "/protected", freshPrimary, freshProof, "")
	if okResp.Code != http.StatusOK {
		t.Fatalf("有效 step-up proof 应放行，实际: %d，响应: %s", okResp.Code, okResp.Body.String())
	}
}

func TestPurposeScopedTokensCannotBePrimaryAuthTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	user := seedStepUpUser(t, db, "purpose-admin", "admin")
	stepUpProof := generateStepUpProof(t, manager, user)
	pending2FA, err := manager.Generate2FAPendingToken(user)
	if err != nil {
		t.Fatalf("生成 2FA pending token 失败: %v", err)
	}

	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.GET("/me", func(c *gin.Context) { respondOK(c, gin.H{"ok": true}) })
	for _, scopedToken := range []string{stepUpProof, pending2FA} {
		resp := performStepUpRequest(t, router, http.MethodGet, "/me", scopedToken, "", "")
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("purpose-scoped token 不应作为 REST 主认证通过，实际: %d，响应: %s", resp.Code, resp.Body.String())
		}
		if _, err := authorizeRealtimeToken(scopedToken, manager, db, realtimeAuthRequirements{Role: "admin"}); err == nil {
			t.Fatalf("purpose-scoped token 不应作为 WebSocket 主认证通过")
		}
	}
}

func TestStepUpPreservesRBACAndOwnershipDenials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	operator := seedStepUpUser(t, db, "ownership-operator", "operator")
	operatorToken := generatePrimaryToken(t, manager, operator)
	node := model.Node{Name: "step-up-owned-node", Host: "10.0.20.1", Username: "root", AuthType: "key", BackupDir: "step-up-owned-node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	taskEntity := model.Task{Name: "step-up-task", NodeID: node.ID, ExecutorType: "rsync", RsyncSource: "/data", RsyncTarget: "/backup", Status: "pending"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	runner := &mockTaskRunner{triggerManualRunID: 77}
	handler := NewTaskHandler(db, runner).WithJWTManager(manager)
	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.POST("/tasks/:id/trigger", middleware.RBAC("tasks:trigger"), middleware.OwnershipTaskCheck(db), RequireStepUp(db, manager, auth.StepUpActionTaskManualTrigger, "task_command", "task_run"), handler.Trigger)

	resp := performStepUpRequest(t, router, http.MethodPost, fmt.Sprintf("/tasks/%d/trigger", taskEntity.ID), operatorToken, "", "")
	if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), "无权访问该任务") {
		t.Fatalf("ownership 拒绝应先于 step-up，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	if events := loadCredentialAuditEvents(t, db, "task.manual_trigger"); len(events) != 0 {
		t.Fatalf("ownership 拒绝不应写入 step-up/task trigger 审计，实际: %+v", events)
	}
}

func TestConfigExportStepUpOnlyWhenIncludingSensitiveValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "config-admin", "admin")
	adminToken := generatePrimaryToken(t, manager, admin)
	adminProof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionConfigExport)

	handler := NewConfigHandler(db, nil)
	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.GET("/config/export", middleware.RequireRole("admin"), RequireStepUpIf(db, manager, auth.StepUpActionConfigExport, "config_export", "settings_export_sensitive", func(c *gin.Context) bool {
		return c.Query("include_secrets") == "true"
	}), handler.Export)

	plainResp := performStepUpRequest(t, router, http.MethodGet, "/config/export", adminToken, "", "")
	if plainResp.Code != http.StatusOK {
		t.Fatalf("普通配置导出不应要求 step-up，实际: %d，响应: %s", plainResp.Code, plainResp.Body.String())
	}
	secretResp := performStepUpRequest(t, router, http.MethodGet, "/config/export?include_secrets=true", adminToken, "", "")
	assertStepUpRequiredEnvelope(t, secretResp)
	secretWithProofResp := performStepUpRequest(t, router, http.MethodGet, "/config/export?include_secrets=true", adminToken, adminProof, "")
	if secretWithProofResp.Code != http.StatusOK {
		t.Fatalf("带有效 step-up proof 的敏感配置导出应成功，实际: %d，响应: %s", secretWithProofResp.Code, secretWithProofResp.Body.String())
	}
}

func TestTerminalAcceptsOnlyTerminalOpenProof(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "terminal-admin", "admin")
	adminToken := generatePrimaryToken(t, manager, admin)
	adminProof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionTerminalOpen)
	wrongActionProof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionTaskManualTrigger)
	now := time.Now().UTC()
	grant := model.CredentialAccessGrant{
		RequesterUserID:     admin.ID,
		RequesterUsername:   admin.Username,
		RequesterRole:       admin.Role,
		Action:              CredentialGrantActionTerminalOpen,
		Purpose:             sshutil.PurposeTerminal,
		NodeID:              credentialaudit.PtrUint(2),
		Reason:              "维护",
		Status:              CredentialGrantStatusActive,
		RequestedTTLSeconds: 600,
		RequestedAt:         now,
		ExpiresAt:           now.Add(10 * time.Minute),
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatalf("创建 terminal grant fixture 失败: %v", err)
	}

	handler := NewTerminalHandler(db, manager, func(*http.Request) bool { return true })
	router := gin.New()
	router.GET("/api/v1/ws/terminal", handler.ServeTerminal)
	server := httptest.NewServer(router)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/ws/terminal"

	dialAndAuth := func(t *testing.T, targetURL string, proof string) *websocket.CloseError {
		t.Helper()
		conn, _, err := websocket.DefaultDialer.Dial(targetURL, nil)
		if err != nil {
			t.Fatalf("建立测试 WebSocket 失败: %v", err)
		}
		defer func() { _ = conn.Close() }()
		payload, err := json.Marshal(map[string]string{"type": "auth", "token": adminToken, "step_up_proof": proof})
		if err != nil {
			t.Fatalf("序列化 WebSocket auth payload 失败: %v", err)
		}
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			t.Fatalf("发送 WebSocket auth payload 失败: %v", err)
		}
		_, _, err = conn.ReadMessage()
		closeErr, ok := err.(*websocket.CloseError)
		if !ok {
			t.Fatalf("期望收到 WebSocket close frame，实际错误: %v", err)
		}
		return closeErr
	}

	missingProofErr := dialAndAuth(t, wsURL+"?node_id=1", "")
	if missingProofErr.Code != websocket.ClosePolicyViolation || !strings.Contains(missingProofErr.Text, "需要二次验证") {
		t.Fatalf("缺少 step-up proof 应 policy violation，实际: %+v", missingProofErr)
	}
	invalidProofErr := dialAndAuth(t, wsURL+"?node_id=1", "malformed-proof")
	if invalidProofErr.Code != websocket.ClosePolicyViolation || !strings.Contains(invalidProofErr.Text, "需要二次验证") {
		t.Fatalf("无效 step-up proof 应 policy violation，实际: %+v", invalidProofErr)
	}
	wrongActionErr := dialAndAuth(t, wsURL+"?node_id=1", wrongActionProof)
	if wrongActionErr.Code != websocket.ClosePolicyViolation || !strings.Contains(wrongActionErr.Text, "需要二次验证") {
		t.Fatalf("非 terminal.open proof 应被终端拒绝，实际: %+v", wrongActionErr)
	}
	validProofErr := dialAndAuth(t, wsURL, adminProof)
	if validProofErr.Code != websocket.CloseInvalidFramePayloadData || !strings.Contains(validProofErr.Text, "缺少 node_id") {
		t.Fatalf("有效 step-up proof 应通过 step-up gate 后再失败于 node_id 校验，实际: %+v", validProofErr)
	}
	missingGrantErr := dialAndAuth(t, wsURL+"?node_id=1", adminProof)
	if missingGrantErr.Code != websocket.ClosePolicyViolation || !strings.Contains(missingGrantErr.Text, credentialGrantRequiredCode) {
		t.Fatalf("缺少 JIT grant 应在节点/SSH 凭据解析前返回机器可读 close reason，实际: %+v", missingGrantErr)
	}
	matchingGrantErr := dialAndAuth(t, wsURL+"?node_id=2", adminProof)
	if matchingGrantErr.Code != websocket.CloseInvalidFramePayloadData || !strings.Contains(matchingGrantErr.Text, "节点不存在") {
		t.Fatalf("匹配 JIT grant 应放行到节点查询，实际: %+v", matchingGrantErr)
	}

	events := loadCredentialAuditEvents(t, db, "terminal.open")
	if len(events) != 8 {
		t.Fatalf("终端 step-up/grant 应写入 8 条凭据审计事件，实际: %+v", events)
	}
	if events[0].Outcome != credentialaudit.OutcomeBlocked || events[1].Outcome != credentialaudit.OutcomeBlocked || events[2].Outcome != credentialaudit.OutcomeBlocked || events[3].Outcome != credentialaudit.OutcomeSuccess || events[4].Outcome != credentialaudit.OutcomeSuccess || events[5].Outcome != credentialaudit.OutcomeBlocked || events[6].Outcome != credentialaudit.OutcomeSuccess || events[7].Outcome != credentialaudit.OutcomeSuccess {
		t.Fatalf("终端 step-up/grant 审计 outcome 不符合预期: %+v", events)
	}
	for _, event := range events {
		assertNoForbiddenAuditMetadata(t, event.Metadata)
	}
}

func TestSnapshotRestoreWritesSafeCredentialAuditEvidence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	node := model.Node{Name: "snapshot-node", Host: "10.0.30.1", Username: "root", AuthType: "key", BackupDir: "snapshot-node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	taskEntity := model.Task{Name: "snapshot-task", NodeID: node.ID, ExecutorType: "rsync", RsyncSource: "/data", RsyncTarget: "/backup", Status: "pending"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	handler := NewSnapshotHandler(db, nil, nil).WithFeatureLive(func() (bool, error) { return true, nil })
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(10))
		c.Set(middleware.CtxUsername, "snapshot-admin")
		c.Set(middleware.CtxRole, "admin")
		c.Next()
	})
	router.POST("/tasks/:id/snapshots/:sid/restore", handler.Restore)

	body := bytes.NewBufferString(`{"includes":["/restore/include"],"targetPath":"/tmp/xirang-restore-test"}`)
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/tasks/%d/snapshots/abcdef1234567890/restore", taskEntity.ID), body)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("非 restic 快照恢复应返回 400，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	events := loadCredentialAuditEvents(t, db, "snapshot.restore")
	if len(events) != 1 {
		t.Fatalf("快照恢复应写入凭据审计事件，实际: %+v", events)
	}
	event := events[0]
	if event.Outcome != credentialaudit.OutcomeBlocked || event.UserID != 10 || event.TaskID == nil || *event.TaskID != taskEntity.ID || event.NodeID == nil || *event.NodeID != node.ID {
		t.Fatalf("快照恢复凭据审计事件不符合预期: %+v", event)
	}
	metadata := assertNoForbiddenAuditMetadata(t, event.Metadata)
	if metadata["stage"] != "executor" || metadata["include_count"].(float64) != 1 || metadata["target_set"] != true || metadata["snapshot_short"] != "abcdef123456" {
		t.Fatalf("快照恢复凭据审计 metadata 不符合预期: %#v", metadata)
	}
}

func TestStepUpHoldReleaseRejectsRepositoryPurgeProof(t *testing.T) {
	assertRetentionLifecycleStepUpIsolation(t, auth.StepUpActionRetentionHoldRelease, auth.StepUpActionRepositoryPurge, "/recovery-points/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb/holds/cccccccccccccccccccccccccccccccc/release")
}

func TestStepUpRepositoryPurgeRejectsHoldReleaseProof(t *testing.T) {
	assertRetentionLifecycleStepUpIsolation(t, auth.StepUpActionRepositoryPurge, auth.StepUpActionRetentionHoldRelease, "/backup-repositories/dddddddddddddddddddddddddddddddd/purges")
}

func assertRetentionLifecycleStepUpIsolation(t *testing.T, expected, cross auth.StepUpAction, path string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "lifecycle-admin", "admin")
	token := generatePrimaryToken(t, manager, admin)
	crossProof := generateStepUpProofForAction(t, manager, admin, cross)
	validProof := generateStepUpProofForAction(t, manager, admin, expected)

	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	if expected == auth.StepUpActionRetentionHoldRelease {
		router.POST("/recovery-points/:id/holds/:holdId/release", RequireStepUp(db, manager, expected, "retention_hold_release", "hold_release"), func(c *gin.Context) {
			respondOK(c, gin.H{"ok": true})
		})
	} else {
		router.POST("/backup-repositories/:id/purges", RequireStepUp(db, manager, expected, "repository_purge", "repository_purge"), func(c *gin.Context) {
			respondOK(c, gin.H{"ok": true})
		})
	}

	missing := performStepUpRequest(t, router, http.MethodPost, path, token, "", `{"reason":"done"}`)
	assertStepUpRequiredEnvelope(t, missing)
	wrong := performStepUpRequest(t, router, http.MethodPost, path, token, crossProof, `{"reason":"done"}`)
	assertStepUpRequiredEnvelope(t, wrong)
	okResp := performStepUpRequest(t, router, http.MethodPost, path, token, validProof, `{"reason":"done"}`)
	if okResp.Code != http.StatusOK {
		t.Fatalf("matching %s proof should pass, status=%d body=%s", expected, okResp.Code, okResp.Body.String())
	}
}
