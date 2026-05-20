package handlers

import (
	"bytes"
	"encoding/json"
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

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/pquerna/otp/totp"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const stepUpTestJWTSecret = "FAKE_JWT_SECRET_FOR_TEST_ONLY"

func openStepUpHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开 step-up 测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{},
		&model.CredentialAuditEvent{},
		&model.AuditLog{},
		&model.Node{},
		&model.NodeOwner{},
		&model.Task{},
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
		PasswordHash: "FAKE_HASH_FOR_TEST_ONLY",
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
	t.Helper()
	now := time.Now()
	claims := auth.Claims{
		UserID:       user.ID,
		Username:     user.Username,
		Role:         user.Role,
		Purpose:      auth.PurposeStepUp,
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
	proof, _, err := manager.GenerateStepUpToken(user)
	if err != nil {
		t.Fatalf("生成 step-up proof 失败: %v", err)
	}
	return proof
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
	if payload.Code != http.StatusForbidden || payload.Data.ErrorCode != stepUpRequiredCode || payload.Data.ProofTTLSeconds != stepUpProofTTLSeconds {
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

	resp := performStepUpRequest(t, router, http.MethodPost, "/auth/step-up", primaryToken, "", fmt.Sprintf(`{"code":%q}`, currentStepUpCode(t, user)))
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
	if claims.Purpose != auth.PurposeStepUp || claims.UserID != user.ID || claims.TokenVersion != user.TokenVersion {
		t.Fatalf("返回 proof claims 不符合预期: %+v", claims)
	}

	events := loadCredentialAuditEvents(t, db, "auth.step_up")
	if len(events) != 1 || events[0].Outcome != credentialaudit.OutcomeSuccess || events[0].UserID != user.ID {
		t.Fatalf("step-up 成功审计事件不符合预期: %+v", events)
	}
	metadata := assertNoForbiddenAuditMetadata(t, events[0].Metadata)
	if metadata["stage"] != "step_up" || metadata["proof"] != "issued" || metadata["operation"] != "step_up" {
		t.Fatalf("step-up 成功审计 metadata 不符合预期: %#v", metadata)
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

	disabledResp := performStepUpRequest(t, router, http.MethodPost, "/auth/step-up", primaryToken, "", fmt.Sprintf(`{"code":%q}`, validCode))
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
	invalidResp := performStepUpRequest(t, router, http.MethodPost, "/auth/step-up", enabledToken, "", fmt.Sprintf(`{"code":%q}`, invalidCode))
	if invalidResp.Code != http.StatusForbidden {
		t.Fatalf("invalid TOTP step-up 期望 403，实际: %d，响应: %s", invalidResp.Code, invalidResp.Body.String())
	}

	events := loadCredentialAuditEvents(t, db, "auth.step_up")
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
		router.GET("/protected", RequireStepUp(db, manager, "task.manual_trigger", "task_command", "task_run"), func(c *gin.Context) {
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
	router.POST("/tasks/:id/trigger", middleware.RBAC("tasks:trigger"), middleware.OwnershipTaskCheck(db), RequireStepUp(db, manager, "task.manual_trigger", "task_command", "task_run"), handler.Trigger)

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
	adminProof := generateStepUpProof(t, manager, admin)

	handler := NewConfigHandler(db, nil)
	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.GET("/config/export", middleware.RequireRole("admin"), RequireStepUpIf(db, manager, "config.export", "config_export", "settings_export_sensitive", func(c *gin.Context) bool {
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

func TestTerminalWebSocketRequiresStepUpProofInAuthMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "terminal-admin", "admin")
	adminToken := generatePrimaryToken(t, manager, admin)
	adminProof := generateStepUpProof(t, manager, admin)

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
		defer conn.Close()
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
	validProofErr := dialAndAuth(t, wsURL, adminProof)
	if validProofErr.Code != websocket.CloseInvalidFramePayloadData || !strings.Contains(validProofErr.Text, "缺少 node_id") {
		t.Fatalf("有效 step-up proof 应通过 step-up gate 后再失败于 node_id 校验，实际: %+v", validProofErr)
	}

	events := loadCredentialAuditEvents(t, db, "terminal.open")
	if len(events) != 3 {
		t.Fatalf("终端 step-up 应写入 3 条凭据审计事件，实际: %+v", events)
	}
	if events[0].Outcome != credentialaudit.OutcomeBlocked || events[1].Outcome != credentialaudit.OutcomeBlocked || events[2].Outcome != credentialaudit.OutcomeSuccess {
		t.Fatalf("终端 step-up 审计 outcome 不符合预期: %+v", events)
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

	handler := NewSnapshotHandler(db)
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
