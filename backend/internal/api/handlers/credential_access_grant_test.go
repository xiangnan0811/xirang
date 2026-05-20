package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func seedCredentialGrantNode(t *testing.T, db *gorm.DB) model.Node {
	t.Helper()
	suffix := time.Now().UnixNano()
	node := model.Node{
		Name:      fmt.Sprintf("grant-node-%d", suffix),
		Host:      "10.0.40.1",
		Port:      22,
		Username:  "root",
		AuthType:  "password",
		BackupDir: fmt.Sprintf("grant-node-%d", suffix),
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建授权测试节点失败: %v", err)
	}
	return node
}

func newCredentialGrantTestRouter(db *gorm.DB, manager *auth.JWTManager) *gin.Engine {
	handler := NewCredentialAccessGrantHandler(db, manager)
	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.POST("/credential-access-grants/terminal", middleware.RequireRole("admin"), handler.RequestTerminalGrant)
	return router
}

func TestCredentialAccessGrantRequestCreatesActiveSelfGrantWithSafeAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "grant-admin", "admin")
	token := generatePrimaryToken(t, manager, admin)
	proof := generateStepUpProof(t, manager, admin)
	node := seedCredentialGrantNode(t, db)
	router := newCredentialGrantTestRouter(db, manager)

	unsafeReasonResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/terminal", token, proof,
		fmt.Sprintf(`{"node_id":%d,"reason":"例行维护 token=FAKE_TOKEN_FOR_TEST_ONLY","requested_ttl_seconds":600}`, node.ID))
	if unsafeReasonResp.Code != http.StatusBadRequest || strings.Contains(unsafeReasonResp.Body.String(), "FAKE_TOKEN_FOR_TEST_ONLY") {
		t.Fatalf("secret-shaped 授权原因应被拒绝且不回显原文，实际: %d，响应: %s", unsafeReasonResp.Code, unsafeReasonResp.Body.String())
	}

	resp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/terminal", token, proof,
		fmt.Sprintf(`{"node_id":%d,"reason":"例行维护","requested_ttl_seconds":600}`, node.ID))
	if resp.Code != http.StatusCreated {
		t.Fatalf("申请终端授权期望 201，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var payload struct {
		Data struct {
			ID                  uint   `json:"id"`
			RequesterUserID     uint   `json:"requester_user_id"`
			RequesterUsername   string `json:"requester_username"`
			RequesterRole       string `json:"requester_role"`
			Action              string `json:"action"`
			Purpose             string `json:"purpose"`
			NodeID              uint   `json:"node_id"`
			Reason              string `json:"reason"`
			Status              string `json:"status"`
			RequestedTTLSeconds int    `json:"requested_ttl_seconds"`
			ExpiresAt           string `json:"expires_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析终端授权响应失败: %v", err)
	}
	if payload.Data.ID == 0 || payload.Data.RequesterUserID != admin.ID || payload.Data.NodeID != node.ID || payload.Data.Status != CredentialGrantStatusActive {
		t.Fatalf("授权 DTO 基本字段不符合预期: %+v", payload.Data)
	}
	if payload.Data.Action != CredentialGrantActionTerminalOpen || payload.Data.Purpose != sshutil.PurposeTerminal || payload.Data.RequestedTTLSeconds != 600 {
		t.Fatalf("授权 DTO 操作/用途/TTL 不符合预期: %+v", payload.Data)
	}
	if payload.Data.Reason != "例行维护" {
		t.Fatalf("授权原因应保存安全文本，实际: %q", payload.Data.Reason)
	}

	var grant model.CredentialAccessGrant
	if err := db.First(&grant, payload.Data.ID).Error; err != nil {
		t.Fatalf("读取授权记录失败: %v", err)
	}
	if grant.ApprovedAt == nil || grant.ApproverUserID == nil || *grant.ApproverUserID != admin.ID || grant.ExpiresAt.Before(time.Now().UTC()) {
		t.Fatalf("授权记录应为自批准短时 active grant: %+v", grant)
	}

	events := loadCredentialAuditEvents(t, db, CredentialGrantActionTerminalOpen)
	if len(events) != 3 {
		t.Fatalf("grant 请求应写入 step-up/request/activate 三条终端审计事件，实际: %+v", events)
	}
	for _, event := range events {
		metadata := assertNoForbiddenAuditMetadata(t, event.Metadata)
		if event.Outcome != credentialaudit.OutcomeSuccess || event.UserID != admin.ID {
			t.Fatalf("grant 审计事件 actor/outcome 不符合预期: %+v", event)
		}
		if stage, ok := metadata["stage"].(string); !ok || stage == "" {
			t.Fatalf("grant 审计 metadata 缺少安全 stage: %#v", metadata)
		}
	}
}

func TestCredentialAccessGrantRequestRequiresAdminAndStepUpAndValidReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "grant-admin-validation", "admin")
	operator := seedStepUpUser(t, db, "grant-operator-validation", "operator")
	adminToken := generatePrimaryToken(t, manager, admin)
	operatorToken := generatePrimaryToken(t, manager, operator)
	node := seedCredentialGrantNode(t, db)
	router := newCredentialGrantTestRouter(db, manager)
	body := fmt.Sprintf(`{"node_id":%d,"reason":"维护","requested_ttl_seconds":600}`, node.ID)

	operatorResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/terminal", operatorToken, generateStepUpProof(t, manager, operator), body)
	if operatorResp.Code != http.StatusForbidden {
		t.Fatalf("operator 不应申请终端授权，实际: %d，响应: %s", operatorResp.Code, operatorResp.Body.String())
	}

	missingProofResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/terminal", adminToken, "", body)
	assertStepUpRequiredEnvelope(t, missingProofResp)

	emptyReasonResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/terminal", adminToken, generateStepUpProof(t, manager, admin), fmt.Sprintf(`{"node_id":%d,"reason":"   ","requested_ttl_seconds":600}`, node.ID))
	if emptyReasonResp.Code != http.StatusBadRequest || !strings.Contains(emptyReasonResp.Body.String(), "授权原因不能为空") {
		t.Fatalf("空原因应返回 400，实际: %d，响应: %s", emptyReasonResp.Code, emptyReasonResp.Body.String())
	}

	longReason := strings.Repeat("测", credentialGrantMaxReasonLen+1)
	longReasonResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/terminal", adminToken, generateStepUpProof(t, manager, admin), fmt.Sprintf(`{"node_id":%d,"reason":%q,"requested_ttl_seconds":600}`, node.ID, longReason))
	if longReasonResp.Code != http.StatusBadRequest || !strings.Contains(longReasonResp.Body.String(), "授权原因不能超过") {
		t.Fatalf("超长原因应返回 400，实际: %d，响应: %s", longReasonResp.Code, longReasonResp.Body.String())
	}

	shortTTLResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/terminal", adminToken, generateStepUpProof(t, manager, admin), fmt.Sprintf(`{"node_id":%d,"reason":"维护","requested_ttl_seconds":30}`, node.ID))
	if shortTTLResp.Code != http.StatusBadRequest || !strings.Contains(shortTTLResp.Body.String(), "授权时长不能少于") {
		t.Fatalf("过短 TTL 应返回 400，实际: %d，响应: %s", shortTTLResp.Code, shortTTLResp.Body.String())
	}

	longTTLResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/terminal", adminToken, generateStepUpProof(t, manager, admin), fmt.Sprintf(`{"node_id":%d,"reason":"维护","requested_ttl_seconds":3600}`, node.ID))
	if longTTLResp.Code != http.StatusBadRequest || !strings.Contains(longTTLResp.Body.String(), "授权时长不能超过") {
		t.Fatalf("过长 TTL 应返回 400，实际: %d，响应: %s", longTTLResp.Code, longTTLResp.Body.String())
	}
}

func TestFindActiveCredentialGrantMatchesUserOperationResourceAndExpiry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	user := seedStepUpUser(t, db, "grant-match-admin", "admin")
	other := seedStepUpUser(t, db, "grant-match-other", "admin")
	node := seedCredentialGrantNode(t, db)
	otherNode := seedCredentialGrantNode(t, db)
	now := time.Now().UTC()
	claims := &auth.Claims{UserID: user.ID, Username: user.Username, Role: user.Role}
	otherClaims := &auth.Claims{UserID: other.ID, Username: other.Username, Role: other.Role}

	mkGrant := func(userID uint, action string, purpose string, nodeID uint, status string, expiresAt time.Time) model.CredentialAccessGrant {
		grant := model.CredentialAccessGrant{
			RequesterUserID:     userID,
			RequesterUsername:   "grant-user",
			RequesterRole:       "admin",
			Action:              action,
			Purpose:             purpose,
			NodeID:              credentialaudit.PtrUint(nodeID),
			Reason:              "维护",
			Status:              status,
			RequestedTTLSeconds: 600,
			RequestedAt:         now,
			ExpiresAt:           expiresAt,
		}
		if err := db.Create(&grant).Error; err != nil {
			t.Fatalf("创建 grant fixture 失败: %v", err)
		}
		return grant
	}

	mkGrant(user.ID, CredentialGrantActionTerminalOpen, sshutil.PurposeTerminal, node.ID, CredentialGrantStatusRevoked, now.Add(20*time.Minute))
	valid := mkGrant(user.ID, CredentialGrantActionTerminalOpen, sshutil.PurposeTerminal, node.ID, CredentialGrantStatusActive, now.Add(10*time.Minute))
	mkGrant(other.ID, CredentialGrantActionTerminalOpen, sshutil.PurposeTerminal, node.ID, CredentialGrantStatusActive, now.Add(10*time.Minute))
	mkGrant(user.ID, CredentialGrantActionTerminalOpen, sshutil.PurposeTerminal, otherNode.ID, CredentialGrantStatusActive, now.Add(10*time.Minute))
	mkGrant(user.ID, "ssh_key.export", "ssh_key_export", node.ID, CredentialGrantStatusActive, now.Add(10*time.Minute))

	found, err := findActiveCredentialGrant(context.Background(), db, claims, CredentialGrantActionTerminalOpen, sshutil.PurposeTerminal, node.ID)
	if err != nil || found == nil || found.ID != valid.ID {
		t.Fatalf("有效 active grant 应授权且不被历史 revoked grant 阻断，grant=%+v err=%v", found, err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, otherClaims, CredentialGrantActionTerminalOpen, sshutil.PurposeTerminal, otherNode.ID); !errors.Is(err, ErrCredentialGrantRequired) {
		t.Fatalf("wrong user/resource 应拒绝，实际: %v", err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, claims, "ssh_key.export", sshutil.PurposeTerminal, node.ID); !errors.Is(err, ErrCredentialGrantRequired) {
		t.Fatalf("wrong operation 应拒绝，实际: %v", err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, claims, CredentialGrantActionTerminalOpen, "task_command", node.ID); !errors.Is(err, ErrCredentialGrantRequired) {
		t.Fatalf("wrong purpose 应拒绝，实际: %v", err)
	}
	if err := db.Model(&model.User{}).Where("id = ?", user.ID).Update("role", "operator").Error; err != nil {
		t.Fatalf("更新用户角色失败: %v", err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, claims, CredentialGrantActionTerminalOpen, sshutil.PurposeTerminal, node.ID); !errors.Is(err, ErrCredentialGrantInvalid) {
		t.Fatalf("角色变化后的历史 grant 应拒绝，实际: %v", err)
	}
}

func TestFindActiveCredentialGrantExpiresAndReportsInactiveStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	user := seedStepUpUser(t, db, "grant-expiry-admin", "admin")
	node := seedCredentialGrantNode(t, db)
	now := time.Now().UTC()
	claims := &auth.Claims{UserID: user.ID, Username: user.Username, Role: user.Role}
	grant := model.CredentialAccessGrant{
		RequesterUserID:     user.ID,
		RequesterUsername:   user.Username,
		RequesterRole:       user.Role,
		Action:              CredentialGrantActionTerminalOpen,
		Purpose:             sshutil.PurposeTerminal,
		NodeID:              credentialaudit.PtrUint(node.ID),
		Reason:              "维护",
		Status:              CredentialGrantStatusActive,
		RequestedTTLSeconds: 60,
		RequestedAt:         now.Add(-2 * time.Minute),
		ExpiresAt:           now.Add(-1 * time.Minute),
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatalf("创建过期 grant 失败: %v", err)
	}

	_, err := findActiveCredentialGrant(context.Background(), db, claims, CredentialGrantActionTerminalOpen, sshutil.PurposeTerminal, node.ID)
	if !errors.Is(err, ErrCredentialGrantExpired) {
		t.Fatalf("过期 active grant 应返回 expired，实际: %v", err)
	}
	var reloaded model.CredentialAccessGrant
	if err := db.First(&reloaded, grant.ID).Error; err != nil {
		t.Fatalf("读取过期 grant 失败: %v", err)
	}
	if reloaded.Status != CredentialGrantStatusExpired {
		t.Fatalf("过期 grant 应被标记 expired，实际: %s", reloaded.Status)
	}
}

func TestTerminalCredentialGrantReasonSanitizationRejectsSecretMarkers(t *testing.T) {
	cases := []string{
		"查看输出 output: FAKE_COMMAND_OUTPUT_FOR_TEST_ONLY",
		"连接 https://example.invalid/hook/FAKE_TOKEN_FOR_TEST_ONLY",
		"目标 host: prod.internal",
		"密码 password=FAKE_PASSWORD_FOR_TEST_ONLY",
	}
	for _, input := range cases {
		if reason, err := sanitizeCredentialGrantReason(input); err == nil {
			t.Fatalf("secret-shaped reason 应被拒绝，input=%q reason=%q", input, reason)
		}
	}
	if reason, err := sanitizeCredentialGrantReason("处理告警并检查服务状态"); err != nil || reason == "" {
		t.Fatalf("安全 reason 应保留，reason=%q err=%v", reason, err)
	}
}

func TestEnforceTerminalCredentialGrantWritesUseAndBlockedAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	user := seedStepUpUser(t, db, "grant-enforce-admin", "admin")
	node := seedCredentialGrantNode(t, db)
	claims := &auth.Claims{UserID: user.ID, Username: user.Username, Role: user.Role}
	router := gin.New()
	router.GET("/enforce", func(c *gin.Context) {
		_, _ = EnforceTerminalCredentialGrantForWebSocket(c, db, claims, node.ID)
		c.Status(http.StatusNoContent)
	})

	resp := performStepUpRequest(t, router, http.MethodGet, "/enforce", "", "", "")
	if resp.Code != http.StatusNoContent {
		t.Fatalf("blocked enforce 测试路由应返回 204，实际: %d", resp.Code)
	}
	events := loadCredentialAuditEvents(t, db, CredentialGrantActionTerminalOpen)
	if len(events) != 1 || events[0].Outcome != credentialaudit.OutcomeBlocked {
		t.Fatalf("缺少 grant 应写 blocked audit，实际: %+v", events)
	}
	metadata := assertNoForbiddenAuditMetadata(t, events[0].Metadata)
	if metadata["stage"] != "grant_check" || metadata["status"] != "required" {
		t.Fatalf("blocked audit metadata 不符合预期: %#v", metadata)
	}

	grant := model.CredentialAccessGrant{
		RequesterUserID:     user.ID,
		RequesterUsername:   user.Username,
		RequesterRole:       user.Role,
		Action:              CredentialGrantActionTerminalOpen,
		Purpose:             sshutil.PurposeTerminal,
		NodeID:              credentialaudit.PtrUint(node.ID),
		Reason:              "维护",
		Status:              CredentialGrantStatusActive,
		RequestedTTLSeconds: 600,
		RequestedAt:         time.Now().UTC(),
		ExpiresAt:           time.Now().UTC().Add(10 * time.Minute),
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatalf("创建 active grant 失败: %v", err)
	}
	resp = performStepUpRequest(t, router, http.MethodGet, "/enforce", "", "", "")
	if resp.Code != http.StatusNoContent {
		t.Fatalf("use enforce 测试路由应返回 204，实际: %d", resp.Code)
	}
	events = loadCredentialAuditEvents(t, db, CredentialGrantActionTerminalOpen)
	if len(events) != 2 || events[1].Outcome != credentialaudit.OutcomeSuccess || events[1].UserID != user.ID {
		t.Fatalf("有效 grant 使用应写 success audit，实际: %+v", events)
	}
	metadata = assertNoForbiddenAuditMetadata(t, events[1].Metadata)
	if metadata["stage"] != "use" || metadata["status"] != "active" {
		t.Fatalf("use audit metadata 不符合预期: %#v", metadata)
	}
}
