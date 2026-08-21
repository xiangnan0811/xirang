package handlers

import (
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
		Host:      "redacted",
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

func seedCredentialGrantTask(t *testing.T, db *gorm.DB, executorType string) model.Task {
	t.Helper()
	node := seedCredentialGrantNode(t, db)
	taskEntity := model.Task{
		Name:         fmt.Sprintf("grant-task-%d", time.Now().UnixNano()),
		NodeID:       node.ID,
		ExecutorType: executorType,
		Status:       "pending",
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建授权测试任务失败: %v", err)
	}
	return taskEntity
}

func seedSuccessfulCredentialGrantTaskRun(t *testing.T, db *gorm.DB, taskID uint) {
	t.Helper()
	run := model.TaskRun{TaskID: taskID, TriggerType: "manual", Status: "success"}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("创建授权测试成功执行记录失败: %v", err)
	}
}

func newCredentialGrantTestRouter(db *gorm.DB, manager *auth.JWTManager) *gin.Engine {
	handler := NewCredentialAccessGrantHandler(db, manager)
	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.GET("/credential-access-grants", middleware.RequireRole("admin"), handler.List)
	router.POST("/credential-access-grants/terminal", middleware.RequireRole("admin"), handler.RequestTerminalGrant)
	router.POST("/credential-access-grants/config-import", middleware.RequireRole("admin"), handler.RequestConfigImportGrant)
	router.POST("/credential-access-grants/config-export", middleware.RequireRole("admin"), handler.RequestConfigExportGrant)
	router.POST("/credential-access-grants/snapshot-restore", middleware.RequireRole("admin"), handler.RequestSnapshotRestoreGrant)
	router.POST("/credential-access-grants/task-restore", middleware.RequireRole("admin"), handler.RequestTaskRestoreGrant)
	router.POST("/credential-access-grants/task-manual-trigger", middleware.RBAC("tasks:trigger"), handler.RequestTaskManualTriggerGrant)
	router.POST("/credential-access-grants/task-batch-trigger", middleware.RBAC("tasks:write"), handler.RequestTaskBatchTriggerGrant)
	router.POST("/credential-access-grants/batch-command", middleware.RBAC("tasks:write"), handler.RequestBatchCommandGrant)
	return router
}

func TestCredentialAccessGrantListFiltersPaginationSortsAndSanitizes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "grant-list-admin", "admin")
	token := generatePrimaryToken(t, manager, admin)
	router := newCredentialGrantTestRouter(db, manager)

	nodeID := uint(101)
	taskID := uint(202)
	policyID := uint(303)
	base := time.Date(2026, 5, 22, 8, 0, 0, 0, time.UTC)
	rows := []model.CredentialAccessGrant{
		{
			RequesterUserID:     admin.ID,
			RequesterUsername:   "grant-list-admin",
			RequesterRole:       "admin",
			Action:              CredentialGrantActionTerminalOpen,
			Purpose:             sshutil.PurposeTerminal,
			NodeID:              &nodeID,
			Reason:              "token: hidden host: internal",
			Status:              CredentialGrantStatusActive,
			RequestedTTLSeconds: 600,
			RequestedAt:         base.Add(-1 * time.Hour),
			ExpiresAt:           base.Add(10 * time.Minute),
			CreatedAt:           base,
			UpdatedAt:           base,
		},
		{
			RequesterUserID:     admin.ID,
			RequesterUsername:   "grant-list-admin",
			RequesterRole:       "admin",
			Action:              CredentialGrantActionTaskRestore,
			Purpose:             sshutil.PurposeTaskRestore,
			TaskID:              &taskID,
			PolicyID:            &policyID,
			Reason:              "例行恢复",
			Status:              CredentialGrantStatusRevoked,
			RequestedTTLSeconds: 300,
			RequestedAt:         base.Add(1 * time.Hour),
			ExpiresAt:           base.Add(2 * time.Hour),
			CreatedAt:           base.Add(1 * time.Hour),
			UpdatedAt:           base.Add(90 * time.Minute),
		},
		{
			RequesterUserID:     admin.ID,
			RequesterUsername:   "other-admin",
			RequesterRole:       "admin",
			Action:              CredentialGrantActionConfigImport,
			Purpose:             CredentialGrantPurposeConfigImport,
			Reason:              "例行导入",
			Status:              CredentialGrantStatusDenied,
			RequestedTTLSeconds: 300,
			RequestedAt:         base.Add(2 * time.Hour),
			ExpiresAt:           base.Add(3 * time.Hour),
			CreatedAt:           base.Add(2 * time.Hour),
			UpdatedAt:           base.Add(2 * time.Hour),
		},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("创建 grant 列表 fixture 失败: %v", err)
	}

	resp := performStepUpRequest(t, router, http.MethodGet, "/credential-access-grants?status=revoked&action=task.restore_trigger&purpose=task_restore&requester_user_id="+fmt.Sprint(admin.ID)+"&requester_username=grant-list-admin&requester_role=admin&task_id=202&policy_id=303&from=2026-05-22T08:30:00Z&to=2026-05-22T09:30:00Z&page=1&page_size=1&sort_by=created_at&sort_order=asc", token, "", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("grant 列表过滤请求期望 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var payload struct {
		Total    int `json:"total"`
		Page     int `json:"page"`
		PageSize int `json:"page_size"`
		Data     []struct {
			ID                  uint   `json:"id"`
			RequesterUserID     uint   `json:"requester_user_id"`
			RequesterUsername   string `json:"requester_username"`
			RequesterRole       string `json:"requester_role"`
			Action              string `json:"action"`
			Purpose             string `json:"purpose"`
			TaskID              uint   `json:"task_id"`
			PolicyID            uint   `json:"policy_id"`
			Reason              string `json:"reason"`
			Status              string `json:"status"`
			RequestedTTLSeconds int    `json:"requested_ttl_seconds"`
			CreatedAt           string `json:"created_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析 grant 列表响应失败: %v", err)
	}
	if payload.Total != 1 || payload.Page != 1 || payload.PageSize != 1 || len(payload.Data) != 1 {
		t.Fatalf("grant 列表分页统计不符合预期: %+v", payload)
	}
	row := payload.Data[0]
	if row.Action != CredentialGrantActionTaskRestore || row.Purpose != sshutil.PurposeTaskRestore || row.Status != CredentialGrantStatusRevoked || row.TaskID != taskID || row.PolicyID != policyID {
		t.Fatalf("grant 列表过滤结果不符合预期: %+v", row)
	}
	if row.RequesterUserID != admin.ID || row.RequesterUsername != "grant-list-admin" || row.RequesterRole != "admin" || row.RequestedTTLSeconds != 300 || row.Reason != "例行恢复" {
		t.Fatalf("grant 列表 DTO 字段不符合预期: %+v", row)
	}

	unsafeResp := performStepUpRequest(t, router, http.MethodGet, "/credential-access-grants?node_id=101", token, "", "")
	if unsafeResp.Code != http.StatusOK {
		t.Fatalf("grant 列表旧数据清洗请求期望 200，实际: %d，响应: %s", unsafeResp.Code, unsafeResp.Body.String())
	}
	body := unsafeResp.Body.String()
	for _, forbidden := range []string{"hidden", "internal", "token:", "host:"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("grant 列表不应回显旧数据敏感片段 %q，响应: %s", forbidden, body)
		}
	}

	defaultResp := performStepUpRequest(t, router, http.MethodGet, "/credential-access-grants?sort_by=not_allowed&page_size=1", token, "", "")
	if defaultResp.Code != http.StatusOK {
		t.Fatalf("grant 列表默认排序请求期望 200，实际: %d，响应: %s", defaultResp.Code, defaultResp.Body.String())
	}
	var defaultPayload struct {
		Data []struct {
			ID     uint   `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(defaultResp.Body.Bytes(), &defaultPayload); err != nil {
		t.Fatalf("解析 grant 默认排序响应失败: %v", err)
	}
	if len(defaultPayload.Data) != 1 || defaultPayload.Data[0].Status != CredentialGrantStatusDenied {
		t.Fatalf("不安全 sort_by 应回退 created_at desc 并返回最近授权，实际: %+v", defaultPayload.Data)
	}
}

func TestCredentialAccessGrantRequestCreatesActiveSelfGrantWithSafeAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "grant-admin", "admin")
	token := generatePrimaryToken(t, manager, admin)
	proof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionTerminalOpen)
	node := seedCredentialGrantNode(t, db)
	router := newCredentialGrantTestRouter(db, manager)

	oversizedReason := strings.Repeat("安全说明", 90)
	unsafeReasonResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/terminal", token, proof,
		fmt.Sprintf(`{"node_id":%d,"reason":%q,"requested_ttl_seconds":600}`, node.ID, oversizedReason))
	if unsafeReasonResp.Code != http.StatusBadRequest || strings.Contains(unsafeReasonResp.Body.String(), oversizedReason) {
		t.Fatalf("过长授权原因应被拒绝且不回显原文，实际: %d，响应: %s", unsafeReasonResp.Code, unsafeReasonResp.Body.String())
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
	if len(events) != 4 {
		t.Fatalf("grant 请求应写入安全拒绝 step-up 及成功 step-up/request/activate 审计事件，实际: %+v", events)
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

func TestConfigImportCredentialGrantRequestCreatesSystemScopedGrantWithSafeAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "grant-config-admin", "admin")
	token := generatePrimaryToken(t, manager, admin)
	proof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionConfigImport)
	router := newCredentialGrantTestRouter(db, manager)

	oversizedReason := strings.Repeat("配置恢复", 90)
	unsafeReasonResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/config-import", token, proof,
		fmt.Sprintf(`{"reason":%q,"requested_ttl_seconds":600}`, oversizedReason))
	if unsafeReasonResp.Code != http.StatusBadRequest || strings.Contains(unsafeReasonResp.Body.String(), oversizedReason) {
		t.Fatalf("config import 过长授权原因应被拒绝且不回显原文，实际: %d，响应: %s", unsafeReasonResp.Code, unsafeReasonResp.Body.String())
	}

	resp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/config-import", token, proof,
		`{"reason":"例行配置恢复","requested_ttl_seconds":600}`)
	if resp.Code != http.StatusCreated {
		t.Fatalf("申请配置导入授权期望 201，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var payload struct {
		Data struct {
			ID                  uint   `json:"id"`
			RequesterUserID     uint   `json:"requester_user_id"`
			Action              string `json:"action"`
			Purpose             string `json:"purpose"`
			NodeID              uint   `json:"node_id"`
			Reason              string `json:"reason"`
			Status              string `json:"status"`
			RequestedTTLSeconds int    `json:"requested_ttl_seconds"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析配置导入授权响应失败: %v", err)
	}
	if payload.Data.ID == 0 || payload.Data.RequesterUserID != admin.ID || payload.Data.NodeID != 0 || payload.Data.Status != CredentialGrantStatusActive {
		t.Fatalf("配置导入授权 DTO 基本字段不符合预期: %+v", payload.Data)
	}
	if payload.Data.Action != CredentialGrantActionConfigImport || payload.Data.Purpose != CredentialGrantPurposeConfigImport || payload.Data.RequestedTTLSeconds != 600 {
		t.Fatalf("配置导入授权 DTO 操作/用途/TTL 不符合预期: %+v", payload.Data)
	}
	if payload.Data.Reason != "例行配置恢复" {
		t.Fatalf("配置导入授权原因应保存安全文本，实际: %q", payload.Data.Reason)
	}

	var grant model.CredentialAccessGrant
	if err := db.First(&grant, payload.Data.ID).Error; err != nil {
		t.Fatalf("读取配置导入授权记录失败: %v", err)
	}
	if grant.NodeID != nil || grant.TaskID != nil || grant.PolicyID != nil || grant.ApproverUserID == nil || *grant.ApproverUserID != admin.ID {
		t.Fatalf("配置导入 grant 应为系统作用域自批准记录: %+v", grant)
	}

	events := loadCredentialAuditEvents(t, db, CredentialGrantActionConfigImport)
	if len(events) != 4 {
		t.Fatalf("config import grant 请求应写入安全拒绝 step-up 及成功 step-up/request/activate 审计事件，实际: %+v", events)
	}
	for _, event := range events {
		metadata := assertNoForbiddenAuditMetadata(t, event.Metadata)
		if event.Outcome != credentialaudit.OutcomeSuccess || event.UserID != admin.ID {
			t.Fatalf("config import grant 审计 actor/outcome 不符合预期: %+v", event)
		}
		if stage, ok := metadata["stage"].(string); !ok || stage == "" {
			t.Fatalf("config import grant 审计 metadata 缺少安全 stage: %#v", metadata)
		}
	}
}

func TestConfigExportCredentialGrantRequestCreatesSystemScopedGrantWithValidationAndSafeAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "grant-config-export-admin", "admin")
	operator := seedStepUpUser(t, db, "grant-config-export-operator", "operator")
	adminToken := generatePrimaryToken(t, manager, admin)
	operatorToken := generatePrimaryToken(t, manager, operator)
	proof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionConfigExport)
	router := newCredentialGrantTestRouter(db, manager)

	operatorResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/config-export", operatorToken, generateStepUpProofForAction(t, manager, operator, auth.StepUpActionConfigExport),
		`{"reason":"例行导出","requested_ttl_seconds":600}`)
	if operatorResp.Code != http.StatusForbidden {
		t.Fatalf("operator 不应申请配置导出授权，实际: %d，响应: %s", operatorResp.Code, operatorResp.Body.String())
	}

	missingProofResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/config-export", adminToken, "",
		`{"reason":"例行导出","requested_ttl_seconds":600}`)
	assertStepUpRequiredEnvelope(t, missingProofResp)

	emptyReasonResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/config-export", adminToken, proof,
		`{"reason":"   ","requested_ttl_seconds":600}`)
	if emptyReasonResp.Code != http.StatusBadRequest || !strings.Contains(emptyReasonResp.Body.String(), "授权原因不能为空") {
		t.Fatalf("空配置导出授权原因应返回 400，实际: %d，响应: %s", emptyReasonResp.Code, emptyReasonResp.Body.String())
	}

	shortTTLResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/config-export", adminToken, proof,
		`{"reason":"例行导出","requested_ttl_seconds":30}`)
	if shortTTLResp.Code != http.StatusBadRequest || !strings.Contains(shortTTLResp.Body.String(), "授权时长不能少于") {
		t.Fatalf("配置导出授权过短 TTL 应返回 400，实际: %d，响应: %s", shortTTLResp.Code, shortTTLResp.Body.String())
	}

	resp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/config-export", adminToken, proof,
		`{"reason":"例行导出","requested_ttl_seconds":600}`)
	if resp.Code != http.StatusCreated {
		t.Fatalf("申请配置导出授权期望 201，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var payload struct {
		Data struct {
			ID                  uint   `json:"id"`
			RequesterUserID     uint   `json:"requester_user_id"`
			Action              string `json:"action"`
			Purpose             string `json:"purpose"`
			NodeID              uint   `json:"node_id"`
			TaskID              uint   `json:"task_id"`
			PolicyID            uint   `json:"policy_id"`
			Reason              string `json:"reason"`
			Status              string `json:"status"`
			RequestedTTLSeconds int    `json:"requested_ttl_seconds"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析配置导出授权响应失败: %v", err)
	}
	if payload.Data.ID == 0 || payload.Data.RequesterUserID != admin.ID || payload.Data.NodeID != 0 || payload.Data.TaskID != 0 || payload.Data.PolicyID != 0 || payload.Data.Status != CredentialGrantStatusActive {
		t.Fatalf("配置导出授权 DTO 基本字段不符合预期: %+v", payload.Data)
	}
	if payload.Data.Action != CredentialGrantActionConfigExport || payload.Data.Purpose != CredentialGrantPurposeConfigExport || payload.Data.RequestedTTLSeconds != 600 {
		t.Fatalf("配置导出授权 DTO 操作/用途/TTL 不符合预期: %+v", payload.Data)
	}
	if payload.Data.Reason != "例行导出" {
		t.Fatalf("配置导出授权原因应保存安全文本，实际: %q", payload.Data.Reason)
	}

	var grant model.CredentialAccessGrant
	if err := db.First(&grant, payload.Data.ID).Error; err != nil {
		t.Fatalf("读取配置导出授权记录失败: %v", err)
	}
	if grant.NodeID != nil || grant.TaskID != nil || grant.PolicyID != nil || grant.ApproverUserID == nil || *grant.ApproverUserID != admin.ID {
		t.Fatalf("配置导出 grant 应为系统作用域自批准记录: %+v", grant)
	}

	events := loadCredentialAuditEvents(t, db, CredentialGrantActionConfigExport)
	if len(events) == 0 {
		t.Fatalf("配置导出 grant 请求应写入审计事件")
	}
	for _, event := range events {
		metadata := assertNoForbiddenAuditMetadata(t, event.Metadata)
		if stage, ok := metadata["stage"].(string); !ok || stage == "" {
			t.Fatalf("配置导出 grant 审计 metadata 缺少安全 stage: %#v", metadata)
		}
	}
}

func TestSnapshotRestoreCredentialGrantRequestCreatesTaskScopedGrantWithValidationAndSafeAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "grant-snapshot-admin", "admin")
	operator := seedStepUpUser(t, db, "grant-snapshot-operator", "operator")
	adminToken := generatePrimaryToken(t, manager, admin)
	operatorToken := generatePrimaryToken(t, manager, operator)
	proof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionSnapshotRestore)
	taskEntity := seedCredentialGrantTask(t, db, "restic")
	nonResticTask := seedCredentialGrantTask(t, db, "rsync")
	router := newCredentialGrantTestRouter(db, manager)

	operatorResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/snapshot-restore", operatorToken, generateStepUpProofForAction(t, manager, operator, auth.StepUpActionSnapshotRestore),
		fmt.Sprintf(`{"task_id":%d,"reason":"例行恢复","requested_ttl_seconds":600}`, taskEntity.ID))
	if operatorResp.Code != http.StatusForbidden {
		t.Fatalf("operator 不应申请快照恢复授权，实际: %d，响应: %s", operatorResp.Code, operatorResp.Body.String())
	}

	missingProofResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/snapshot-restore", adminToken, "",
		fmt.Sprintf(`{"task_id":%d,"reason":"例行恢复","requested_ttl_seconds":600}`, taskEntity.ID))
	assertStepUpRequiredEnvelope(t, missingProofResp)

	emptyReasonResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/snapshot-restore", adminToken, proof,
		fmt.Sprintf(`{"task_id":%d,"reason":"   ","requested_ttl_seconds":600}`, taskEntity.ID))
	if emptyReasonResp.Code != http.StatusBadRequest || !strings.Contains(emptyReasonResp.Body.String(), "授权原因不能为空") {
		t.Fatalf("空快照恢复授权原因应返回 400，实际: %d，响应: %s", emptyReasonResp.Code, emptyReasonResp.Body.String())
	}

	shortTTLResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/snapshot-restore", adminToken, proof,
		fmt.Sprintf(`{"task_id":%d,"reason":"例行恢复","requested_ttl_seconds":30}`, taskEntity.ID))
	if shortTTLResp.Code != http.StatusBadRequest || !strings.Contains(shortTTLResp.Body.String(), "授权时长不能少于") {
		t.Fatalf("快照恢复授权过短 TTL 应返回 400，实际: %d，响应: %s", shortTTLResp.Code, shortTTLResp.Body.String())
	}

	missingTaskResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/snapshot-restore", adminToken, proof,
		`{"task_id":999999,"reason":"例行恢复","requested_ttl_seconds":600}`)
	if missingTaskResp.Code != http.StatusNotFound {
		t.Fatalf("不存在任务应返回 404，实际: %d，响应: %s", missingTaskResp.Code, missingTaskResp.Body.String())
	}

	nonResticResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/snapshot-restore", adminToken, proof,
		fmt.Sprintf(`{"task_id":%d,"reason":"例行恢复","requested_ttl_seconds":600}`, nonResticTask.ID))
	if nonResticResp.Code != http.StatusBadRequest || !strings.Contains(nonResticResp.Body.String(), "仅 restic") {
		t.Fatalf("非 restic 任务不应创建快照恢复授权，实际: %d，响应: %s", nonResticResp.Code, nonResticResp.Body.String())
	}

	resp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/snapshot-restore", adminToken, proof,
		fmt.Sprintf(`{"task_id":%d,"reason":"例行恢复","requested_ttl_seconds":600}`, taskEntity.ID))
	if resp.Code != http.StatusCreated {
		t.Fatalf("申请快照恢复授权期望 201，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var payload struct {
		Data struct {
			ID                  uint   `json:"id"`
			RequesterUserID     uint   `json:"requester_user_id"`
			Action              string `json:"action"`
			Purpose             string `json:"purpose"`
			NodeID              uint   `json:"node_id"`
			TaskID              uint   `json:"task_id"`
			PolicyID            uint   `json:"policy_id"`
			Reason              string `json:"reason"`
			Status              string `json:"status"`
			RequestedTTLSeconds int    `json:"requested_ttl_seconds"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析快照恢复授权响应失败: %v", err)
	}
	if payload.Data.ID == 0 || payload.Data.RequesterUserID != admin.ID || payload.Data.NodeID != 0 || payload.Data.TaskID != taskEntity.ID || payload.Data.PolicyID != 0 || payload.Data.Status != CredentialGrantStatusActive {
		t.Fatalf("快照恢复授权 DTO 基本字段不符合预期: %+v", payload.Data)
	}
	if payload.Data.Action != CredentialGrantActionSnapshotRestore || payload.Data.Purpose != sshutil.PurposeSnapshot || payload.Data.RequestedTTLSeconds != 600 {
		t.Fatalf("快照恢复授权 DTO 操作/用途/TTL 不符合预期: %+v", payload.Data)
	}
	if payload.Data.Reason != "例行恢复" {
		t.Fatalf("快照恢复授权原因应保存安全文本，实际: %q", payload.Data.Reason)
	}

	var grant model.CredentialAccessGrant
	if err := db.First(&grant, payload.Data.ID).Error; err != nil {
		t.Fatalf("读取快照恢复授权记录失败: %v", err)
	}
	if grant.NodeID != nil || grant.TaskID == nil || *grant.TaskID != taskEntity.ID || grant.PolicyID != nil || grant.ApproverUserID == nil || *grant.ApproverUserID != admin.ID {
		t.Fatalf("快照恢复 grant 应为任务作用域自批准记录: %+v", grant)
	}

	events := loadCredentialAuditEvents(t, db, CredentialGrantActionSnapshotRestore)
	if len(events) == 0 {
		t.Fatalf("快照恢复 grant 请求应写入审计事件")
	}
	for _, event := range events {
		metadata := assertNoForbiddenAuditMetadata(t, event.Metadata)
		if stage, ok := metadata["stage"].(string); !ok || stage == "" {
			t.Fatalf("快照恢复 grant 审计 metadata 缺少安全 stage: %#v", metadata)
		}
	}
}

func TestTaskRestoreCredentialGrantRequestCreatesTaskScopedGrantWithValidationAndSafeAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "grant-task-restore-admin", "admin")
	operator := seedStepUpUser(t, db, "grant-task-restore-operator", "operator")
	adminToken := generatePrimaryToken(t, manager, admin)
	operatorToken := generatePrimaryToken(t, manager, operator)
	proof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionTaskRestoreTrigger)
	taskEntity := seedCredentialGrantTask(t, db, "rsync")
	seedSuccessfulCredentialGrantTaskRun(t, db, taskEntity.ID)
	unsupportedTask := seedCredentialGrantTask(t, db, "command")
	seedSuccessfulCredentialGrantTaskRun(t, db, unsupportedTask.ID)
	noSuccessTask := seedCredentialGrantTask(t, db, "restic")
	router := newCredentialGrantTestRouter(db, manager)

	operatorResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/task-restore", operatorToken, generateStepUpProofForAction(t, manager, operator, auth.StepUpActionTaskRestoreTrigger),
		fmt.Sprintf(`{"task_id":%d,"reason":"例行恢复","requested_ttl_seconds":600}`, taskEntity.ID))
	if operatorResp.Code != http.StatusForbidden {
		t.Fatalf("operator 不应申请任务恢复授权，实际: %d，响应: %s", operatorResp.Code, operatorResp.Body.String())
	}

	missingProofResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/task-restore", adminToken, "",
		fmt.Sprintf(`{"task_id":%d,"reason":"例行恢复","requested_ttl_seconds":600}`, taskEntity.ID))
	assertStepUpRequiredEnvelope(t, missingProofResp)

	emptyReasonResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/task-restore", adminToken, proof,
		fmt.Sprintf(`{"task_id":%d,"reason":"   ","requested_ttl_seconds":600}`, taskEntity.ID))
	if emptyReasonResp.Code != http.StatusBadRequest || !strings.Contains(emptyReasonResp.Body.String(), "授权原因不能为空") {
		t.Fatalf("空任务恢复授权原因应返回 400，实际: %d，响应: %s", emptyReasonResp.Code, emptyReasonResp.Body.String())
	}

	shortTTLResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/task-restore", adminToken, proof,
		fmt.Sprintf(`{"task_id":%d,"reason":"例行恢复","requested_ttl_seconds":30}`, taskEntity.ID))
	if shortTTLResp.Code != http.StatusBadRequest || !strings.Contains(shortTTLResp.Body.String(), "授权时长不能少于") {
		t.Fatalf("任务恢复授权过短 TTL 应返回 400，实际: %d，响应: %s", shortTTLResp.Code, shortTTLResp.Body.String())
	}

	missingTaskResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/task-restore", adminToken, proof,
		`{"task_id":999999,"reason":"例行恢复","requested_ttl_seconds":600}`)
	if missingTaskResp.Code != http.StatusNotFound {
		t.Fatalf("不存在任务应返回 404，实际: %d，响应: %s", missingTaskResp.Code, missingTaskResp.Body.String())
	}

	unsupportedResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/task-restore", adminToken, proof,
		fmt.Sprintf(`{"task_id":%d,"reason":"例行恢复","requested_ttl_seconds":600}`, unsupportedTask.ID))
	if unsupportedResp.Code != http.StatusBadRequest || !strings.Contains(unsupportedResp.Body.String(), "不支持备份恢复") {
		t.Fatalf("不支持恢复的任务不应创建任务恢复授权，实际: %d，响应: %s", unsupportedResp.Code, unsupportedResp.Body.String())
	}

	noSuccessResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/task-restore", adminToken, proof,
		fmt.Sprintf(`{"task_id":%d,"reason":"例行恢复","requested_ttl_seconds":600}`, noSuccessTask.ID))
	if noSuccessResp.Code != http.StatusBadRequest || !strings.Contains(noSuccessResp.Body.String(), "没有成功的执行记录") {
		t.Fatalf("没有成功执行记录的任务不应创建任务恢复授权，实际: %d，响应: %s", noSuccessResp.Code, noSuccessResp.Body.String())
	}

	resp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/task-restore", adminToken, proof,
		fmt.Sprintf(`{"task_id":%d,"reason":"例行恢复","requested_ttl_seconds":600}`, taskEntity.ID))
	if resp.Code != http.StatusCreated {
		t.Fatalf("申请任务恢复授权期望 201，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var payload struct {
		Data struct {
			ID                  uint   `json:"id"`
			RequesterUserID     uint   `json:"requester_user_id"`
			Action              string `json:"action"`
			Purpose             string `json:"purpose"`
			NodeID              uint   `json:"node_id"`
			TaskID              uint   `json:"task_id"`
			PolicyID            uint   `json:"policy_id"`
			Reason              string `json:"reason"`
			Status              string `json:"status"`
			RequestedTTLSeconds int    `json:"requested_ttl_seconds"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析任务恢复授权响应失败: %v", err)
	}
	if payload.Data.ID == 0 || payload.Data.RequesterUserID != admin.ID || payload.Data.NodeID != 0 || payload.Data.TaskID != taskEntity.ID || payload.Data.PolicyID != 0 || payload.Data.Status != CredentialGrantStatusActive {
		t.Fatalf("任务恢复授权 DTO 基本字段不符合预期: %+v", payload.Data)
	}
	if payload.Data.Action != CredentialGrantActionTaskRestore || payload.Data.Purpose != sshutil.PurposeTaskRestore || payload.Data.RequestedTTLSeconds != 600 {
		t.Fatalf("任务恢复授权 DTO 操作/用途/TTL 不符合预期: %+v", payload.Data)
	}
	if payload.Data.Reason != "例行恢复" {
		t.Fatalf("任务恢复授权原因应保存安全文本，实际: %q", payload.Data.Reason)
	}

	var grant model.CredentialAccessGrant
	if err := db.First(&grant, payload.Data.ID).Error; err != nil {
		t.Fatalf("读取任务恢复授权记录失败: %v", err)
	}
	if grant.NodeID != nil || grant.TaskID == nil || *grant.TaskID != taskEntity.ID || grant.PolicyID != nil || grant.ApproverUserID == nil || *grant.ApproverUserID != admin.ID {
		t.Fatalf("任务恢复 grant 应为任务作用域自批准记录: %+v", grant)
	}

	events := loadCredentialAuditEvents(t, db, CredentialGrantActionTaskRestore)
	if len(events) == 0 {
		t.Fatalf("任务恢复 grant 请求应写入审计事件")
	}
	for _, event := range events {
		metadata := assertNoForbiddenAuditMetadata(t, event.Metadata)
		if stage, ok := metadata["stage"].(string); !ok || stage == "" {
			t.Fatalf("任务恢复 grant 审计 metadata 缺少安全 stage: %#v", metadata)
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

	operatorResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/terminal", operatorToken, generateStepUpProofForAction(t, manager, operator, auth.StepUpActionTerminalOpen), body)
	if operatorResp.Code != http.StatusForbidden {
		t.Fatalf("operator 不应申请终端授权，实际: %d，响应: %s", operatorResp.Code, operatorResp.Body.String())
	}

	missingProofResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/terminal", adminToken, "", body)
	assertStepUpRequiredEnvelope(t, missingProofResp)

	emptyReasonResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/terminal", adminToken, generateStepUpProofForAction(t, manager, admin, auth.StepUpActionTerminalOpen), fmt.Sprintf(`{"node_id":%d,"reason":"   ","requested_ttl_seconds":600}`, node.ID))
	if emptyReasonResp.Code != http.StatusBadRequest || !strings.Contains(emptyReasonResp.Body.String(), "授权原因不能为空") {
		t.Fatalf("空原因应返回 400，实际: %d，响应: %s", emptyReasonResp.Code, emptyReasonResp.Body.String())
	}

	longReason := strings.Repeat("测", credentialGrantMaxReasonLen+1)
	longReasonResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/terminal", adminToken, generateStepUpProofForAction(t, manager, admin, auth.StepUpActionTerminalOpen), fmt.Sprintf(`{"node_id":%d,"reason":%q,"requested_ttl_seconds":600}`, node.ID, longReason))
	if longReasonResp.Code != http.StatusBadRequest || !strings.Contains(longReasonResp.Body.String(), "授权原因不能超过") {
		t.Fatalf("超长原因应返回 400，实际: %d，响应: %s", longReasonResp.Code, longReasonResp.Body.String())
	}

	shortTTLResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/terminal", adminToken, generateStepUpProofForAction(t, manager, admin, auth.StepUpActionTerminalOpen), fmt.Sprintf(`{"node_id":%d,"reason":"维护","requested_ttl_seconds":30}`, node.ID))
	if shortTTLResp.Code != http.StatusBadRequest || !strings.Contains(shortTTLResp.Body.String(), "授权时长不能少于") {
		t.Fatalf("过短 TTL 应返回 400，实际: %d，响应: %s", shortTTLResp.Code, shortTTLResp.Body.String())
	}

	longTTLResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/terminal", adminToken, generateStepUpProofForAction(t, manager, admin, auth.StepUpActionTerminalOpen), fmt.Sprintf(`{"node_id":%d,"reason":"维护","requested_ttl_seconds":3600}`, node.ID))
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

	found, err := findActiveCredentialGrant(context.Background(), db, claims, credentialGrantMatch{Action: CredentialGrantActionTerminalOpen, Purpose: sshutil.PurposeTerminal, NodeID: credentialaudit.PtrUint(node.ID)})
	if err != nil || found == nil || found.ID != valid.ID {
		t.Fatalf("有效 active grant 应授权且不被历史 revoked grant 阻断，grant=%+v err=%v", found, err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, otherClaims, credentialGrantMatch{Action: CredentialGrantActionTerminalOpen, Purpose: sshutil.PurposeTerminal, NodeID: credentialaudit.PtrUint(otherNode.ID)}); !errors.Is(err, ErrCredentialGrantRequired) {
		t.Fatalf("wrong user/resource 应拒绝，实际: %v", err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, claims, credentialGrantMatch{Action: "ssh_key.export", Purpose: sshutil.PurposeTerminal, NodeID: credentialaudit.PtrUint(node.ID)}); !errors.Is(err, ErrCredentialGrantRequired) {
		t.Fatalf("wrong operation 应拒绝，实际: %v", err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, claims, credentialGrantMatch{Action: CredentialGrantActionTerminalOpen, Purpose: "task_command", NodeID: credentialaudit.PtrUint(node.ID)}); !errors.Is(err, ErrCredentialGrantRequired) {
		t.Fatalf("wrong purpose 应拒绝，实际: %v", err)
	}
	if err := db.Model(&model.User{}).Where("id = ?", user.ID).Update("role", "operator").Error; err != nil {
		t.Fatalf("更新用户角色失败: %v", err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, claims, credentialGrantMatch{Action: CredentialGrantActionTerminalOpen, Purpose: sshutil.PurposeTerminal, NodeID: credentialaudit.PtrUint(node.ID)}); !errors.Is(err, ErrCredentialGrantInvalid) {
		t.Fatalf("角色变化后的历史 grant 应拒绝，实际: %v", err)
	}
}

func TestFindActiveConfigImportCredentialGrantMatchesSystemScopeAndRejectsWrongTuple(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	user := seedStepUpUser(t, db, "grant-config-match-admin", "admin")
	other := seedStepUpUser(t, db, "grant-config-match-other", "admin")
	node := seedCredentialGrantNode(t, db)
	now := time.Now().UTC()
	claims := &auth.Claims{UserID: user.ID, Username: user.Username, Role: user.Role}
	otherClaims := &auth.Claims{UserID: other.ID, Username: other.Username, Role: other.Role}

	mkGrant := func(userID uint, action string, purpose string, nodeID *uint, status string, expiresAt time.Time) model.CredentialAccessGrant {
		grant := model.CredentialAccessGrant{
			RequesterUserID:     userID,
			RequesterUsername:   "grant-user",
			RequesterRole:       "admin",
			Action:              action,
			Purpose:             purpose,
			NodeID:              nodeID,
			Reason:              "维护",
			Status:              status,
			RequestedTTLSeconds: 600,
			RequestedAt:         now,
			ExpiresAt:           expiresAt,
		}
		if err := db.Create(&grant).Error; err != nil {
			t.Fatalf("创建 config grant fixture 失败: %v", err)
		}
		return grant
	}

	valid := mkGrant(user.ID, CredentialGrantActionConfigImport, CredentialGrantPurposeConfigImport, nil, CredentialGrantStatusActive, now.Add(10*time.Minute))
	mkGrant(user.ID, CredentialGrantActionTerminalOpen, sshutil.PurposeTerminal, credentialaudit.PtrUint(node.ID), CredentialGrantStatusActive, now.Add(10*time.Minute))
	mkGrant(user.ID, CredentialGrantActionConfigImport, "settings_export", nil, CredentialGrantStatusDenied, now.Add(10*time.Minute))
	mkGrant(other.ID, CredentialGrantActionConfigImport, CredentialGrantPurposeConfigImport, nil, CredentialGrantStatusRevoked, now.Add(10*time.Minute))

	found, err := findActiveCredentialGrant(context.Background(), db, claims, credentialGrantMatch{Action: CredentialGrantActionConfigImport, Purpose: CredentialGrantPurposeConfigImport})
	if err != nil || found == nil || found.ID != valid.ID {
		t.Fatalf("有效 config import system-scoped grant 应授权，grant=%+v err=%v", found, err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, otherClaims, credentialGrantMatch{Action: CredentialGrantActionConfigImport, Purpose: CredentialGrantPurposeConfigImport}); !errors.Is(err, ErrCredentialGrantRevoked) {
		t.Fatalf("wrong user 的 revoked config import grant 应拒绝，实际: %v", err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, claims, credentialGrantMatch{Action: CredentialGrantActionTerminalOpen, Purpose: CredentialGrantPurposeConfigImport}); !errors.Is(err, ErrCredentialGrantRequired) {
		t.Fatalf("wrong action 的 config import grant 应拒绝，实际: %v", err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, claims, credentialGrantMatch{Action: CredentialGrantActionConfigImport, Purpose: "settings_export"}); !errors.Is(err, ErrCredentialGrantDenied) {
		t.Fatalf("wrong purpose 的 denied config import grant 应拒绝，实际: %v", err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, claims, credentialGrantMatch{Action: CredentialGrantActionConfigImport, Purpose: CredentialGrantPurposeConfigImport, NodeID: credentialaudit.PtrUint(node.ID)}); !errors.Is(err, ErrCredentialGrantRequired) {
		t.Fatalf("system-scoped grant 不应授权 node-scoped tuple，实际: %v", err)
	}
}

func TestFindActiveSnapshotRestoreCredentialGrantMatchesTaskScopeAndRejectsWrongTuple(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	user := seedStepUpUser(t, db, "grant-snapshot-match-admin", "admin")
	other := seedStepUpUser(t, db, "grant-snapshot-match-other", "admin")
	taskEntity := seedCredentialGrantTask(t, db, "restic")
	otherTask := seedCredentialGrantTask(t, db, "restic")
	node := seedCredentialGrantNode(t, db)
	now := time.Now().UTC()
	claims := &auth.Claims{UserID: user.ID, Username: user.Username, Role: user.Role}
	otherClaims := &auth.Claims{UserID: other.ID, Username: other.Username, Role: other.Role}

	mkGrant := func(userID uint, action string, purpose string, taskID *uint, nodeID *uint, status string, role string) model.CredentialAccessGrant {
		if role == "" {
			role = "admin"
		}
		grant := model.CredentialAccessGrant{
			RequesterUserID:     userID,
			RequesterUsername:   "grant-user",
			RequesterRole:       role,
			Action:              action,
			Purpose:             purpose,
			NodeID:              nodeID,
			TaskID:              taskID,
			Reason:              "维护",
			Status:              status,
			RequestedTTLSeconds: 600,
			RequestedAt:         now,
			ExpiresAt:           now.Add(10 * time.Minute),
		}
		if err := db.Create(&grant).Error; err != nil {
			t.Fatalf("创建 snapshot grant fixture 失败: %v", err)
		}
		return grant
	}

	valid := mkGrant(user.ID, CredentialGrantActionSnapshotRestore, sshutil.PurposeSnapshot, credentialaudit.PtrUint(taskEntity.ID), nil, CredentialGrantStatusActive, "admin")
	mkGrant(user.ID, CredentialGrantActionSnapshotRestore, sshutil.PurposeSnapshot, credentialaudit.PtrUint(otherTask.ID), nil, CredentialGrantStatusActive, "admin")
	mkGrant(other.ID, CredentialGrantActionSnapshotRestore, sshutil.PurposeSnapshot, credentialaudit.PtrUint(taskEntity.ID), nil, CredentialGrantStatusRevoked, "admin")
	mkGrant(user.ID, CredentialGrantActionTerminalOpen, sshutil.PurposeTerminal, nil, credentialaudit.PtrUint(node.ID), CredentialGrantStatusActive, "admin")
	mkGrant(user.ID, CredentialGrantActionConfigImport, CredentialGrantPurposeConfigImport, nil, nil, CredentialGrantStatusActive, "admin")
	mkGrant(user.ID, CredentialGrantActionSnapshotRestore, "snapshot_diff", credentialaudit.PtrUint(taskEntity.ID), nil, CredentialGrantStatusDenied, "admin")
	mkGrant(user.ID, CredentialGrantActionSnapshotRestore, sshutil.PurposeSnapshot, credentialaudit.PtrUint(taskEntity.ID), nil, CredentialGrantStatusActive, "operator")

	found, err := findActiveCredentialGrant(context.Background(), db, claims, credentialGrantMatch{Action: CredentialGrantActionSnapshotRestore, Purpose: sshutil.PurposeSnapshot, TaskID: credentialaudit.PtrUint(taskEntity.ID)})
	if err != nil || found == nil || found.ID != valid.ID {
		t.Fatalf("有效快照恢复 task-scoped grant 应授权，grant=%+v err=%v", found, err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, claims, credentialGrantMatch{Action: CredentialGrantActionSnapshotRestore, Purpose: sshutil.PurposeSnapshot, TaskID: credentialaudit.PtrUint(otherTask.ID + 1000)}); !errors.Is(err, ErrCredentialGrantRequired) {
		t.Fatalf("wrong task 应拒绝，实际: %v", err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, otherClaims, credentialGrantMatch{Action: CredentialGrantActionSnapshotRestore, Purpose: sshutil.PurposeSnapshot, TaskID: credentialaudit.PtrUint(taskEntity.ID)}); !errors.Is(err, ErrCredentialGrantRevoked) {
		t.Fatalf("wrong user 的 revoked 快照恢复 grant 应拒绝，实际: %v", err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, claims, credentialGrantMatch{Action: CredentialGrantActionTerminalOpen, Purpose: sshutil.PurposeSnapshot, TaskID: credentialaudit.PtrUint(taskEntity.ID)}); !errors.Is(err, ErrCredentialGrantRequired) {
		t.Fatalf("wrong action 应拒绝，实际: %v", err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, claims, credentialGrantMatch{Action: CredentialGrantActionSnapshotRestore, Purpose: "snapshot_diff", TaskID: credentialaudit.PtrUint(taskEntity.ID)}); !errors.Is(err, ErrCredentialGrantDenied) {
		t.Fatalf("wrong purpose 的 denied 快照恢复 grant 应拒绝，实际: %v", err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, claims, credentialGrantMatch{Action: CredentialGrantActionSnapshotRestore, Purpose: sshutil.PurposeSnapshot}); !errors.Is(err, ErrCredentialGrantRequired) {
		t.Fatalf("task-scoped grant 不应授权 system-scoped tuple，实际: %v", err)
	}
}

func TestOperatorCanRequestOwnedManualTriggerGrant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	operator := seedStepUpUser(t, db, "grant-manual-operator", "operator")
	token := generatePrimaryToken(t, manager, operator)
	proof := generateStepUpProofForAction(t, manager, operator, auth.StepUpActionTaskManualTrigger)
	node := seedCredentialGrantNode(t, db)
	taskEntity := model.Task{Name: "owned-manual", NodeID: node.ID, ExecutorType: "command", Command: "echo ok", Status: "pending"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: node.ID, UserID: operator.ID}).Error; err != nil {
		t.Fatalf("创建 ownership 失败: %v", err)
	}
	router := newCredentialGrantTestRouter(db, manager)

	resp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/task-manual-trigger", token, proof, fmt.Sprintf(`{"task_id":%d,"reason":"例行触发","requested_ttl_seconds":600}`, taskEntity.ID))
	if resp.Code != http.StatusCreated {
		t.Fatalf("operator owned manual grant 期望 201，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Data credentialGrantDTO `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析 grant 响应失败: %v", err)
	}
	if payload.Data.Action != CredentialGrantActionTaskManualTrigger || payload.Data.Purpose != sshutil.PurposeTaskCommand || payload.Data.TaskID == nil || *payload.Data.TaskID != taskEntity.ID || payload.Data.RequesterRole != "operator" {
		t.Fatalf("operator manual grant DTO 不符合预期: %+v", payload.Data)
	}
	if strings.Contains(resp.Body.String(), "echo ok") {
		t.Fatalf("manual grant 响应不应包含命令内容: %s", resp.Body.String())
	}
}

func TestOperatorCannotRequestUnownedManualTriggerGrant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	operator := seedStepUpUser(t, db, "grant-manual-unowned-operator", "operator")
	token := generatePrimaryToken(t, manager, operator)
	proof := generateStepUpProofForAction(t, manager, operator, auth.StepUpActionTaskManualTrigger)
	taskEntity := seedCredentialGrantTask(t, db, "command")
	router := newCredentialGrantTestRouter(db, manager)

	resp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/task-manual-trigger", token, proof, fmt.Sprintf(`{"task_id":%d,"reason":"例行触发","requested_ttl_seconds":600}`, taskEntity.ID))
	if resp.Code != http.StatusForbidden {
		t.Fatalf("operator unowned manual grant 期望 403，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	var count int64
	if err := db.Model(&model.CredentialAccessGrant{}).Where("action = ?", CredentialGrantActionTaskManualTrigger).Count(&count).Error; err != nil {
		t.Fatalf("统计 grant 失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("未授权任务不应创建 grant，实际: %d", count)
	}
}

func TestBatchGrantRequestsCreateRowsPerResource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	operator := seedStepUpUser(t, db, "grant-batch-operator", "operator")
	token := generatePrimaryToken(t, manager, operator)
	taskProof := generateStepUpProofForAction(t, manager, operator, auth.StepUpActionTaskBatchTrigger)
	commandProof := generateStepUpProofForAction(t, manager, operator, auth.StepUpActionBatchCommandCreate)
	nodeA := seedCredentialGrantNode(t, db)
	nodeB := seedCredentialGrantNode(t, db)
	for _, node := range []model.Node{nodeA, nodeB} {
		if err := db.Create(&model.NodeOwner{NodeID: node.ID, UserID: operator.ID}).Error; err != nil {
			t.Fatalf("创建 ownership 失败: %v", err)
		}
	}
	taskA := model.Task{Name: "batch-a", NodeID: nodeA.ID, ExecutorType: "rsync", Status: "pending"}
	taskB := model.Task{Name: "batch-b", NodeID: nodeB.ID, ExecutorType: "rsync", Status: "pending"}
	if err := db.Create(&taskA).Error; err != nil {
		t.Fatalf("创建 taskA 失败: %v", err)
	}
	if err := db.Create(&taskB).Error; err != nil {
		t.Fatalf("创建 taskB 失败: %v", err)
	}
	router := newCredentialGrantTestRouter(db, manager)

	batchTaskResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/task-batch-trigger", token, taskProof, fmt.Sprintf(`{"task_ids":[%d,%d,%d],"reason":"批量触发","requested_ttl_seconds":600}`, taskA.ID, taskB.ID, taskA.ID))
	if batchTaskResp.Code != http.StatusCreated {
		t.Fatalf("batch task grant 期望 201，实际: %d，响应: %s", batchTaskResp.Code, batchTaskResp.Body.String())
	}
	var taskPayload struct {
		Data []credentialGrantDTO `json:"data"`
	}
	if err := json.Unmarshal(batchTaskResp.Body.Bytes(), &taskPayload); err != nil {
		t.Fatalf("解析 batch task grant 失败: %v", err)
	}
	if len(taskPayload.Data) != 2 {
		t.Fatalf("batch task grant 应按去重任务创建 2 行，实际: %+v", taskPayload.Data)
	}

	batchCommandResp := performStepUpRequest(t, router, http.MethodPost, "/credential-access-grants/batch-command", token, commandProof, fmt.Sprintf(`{"node_ids":[%d,%d,%d],"reason":"批量操作","requested_ttl_seconds":600}`, nodeA.ID, nodeB.ID, nodeA.ID))
	if batchCommandResp.Code != http.StatusCreated {
		t.Fatalf("batch command grant 期望 201，实际: %d，响应: %s", batchCommandResp.Code, batchCommandResp.Body.String())
	}
	var nodePayload struct {
		Data []credentialGrantDTO `json:"data"`
	}
	if err := json.Unmarshal(batchCommandResp.Body.Bytes(), &nodePayload); err != nil {
		t.Fatalf("解析 batch command grant 失败: %v", err)
	}
	if len(nodePayload.Data) != 2 {
		t.Fatalf("batch command grant 应按去重节点创建 2 行，实际: %+v", nodePayload.Data)
	}
	if strings.Contains(batchCommandResp.Body.String(), "rm -rf") || strings.Contains(batchCommandResp.Body.String(), "echo ok") {
		t.Fatalf("batch command grant 响应不应包含命令文本: %s", batchCommandResp.Body.String())
	}
}

func TestFindActiveGrantAllowsOperatorOnlyForOwnedResourceOperations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	operator := seedStepUpUser(t, db, "grant-find-operator", "operator")
	taskEntity := seedCredentialGrantTask(t, db, "command")
	now := time.Now().UTC()
	manualGrant := createTaskRestoreGrantFixture(t, db, operator, CredentialGrantActionTaskManualTrigger, sshutil.PurposeTaskCommand, CredentialGrantStatusActive, credentialaudit.PtrUint(taskEntity.ID), nil, nil, "operator")
	createTaskRestoreGrantFixture(t, db, operator, CredentialGrantActionTaskRestore, sshutil.PurposeTaskRestore, CredentialGrantStatusActive, credentialaudit.PtrUint(taskEntity.ID), nil, nil, "operator")
	claims := &auth.Claims{UserID: operator.ID, Username: operator.Username, Role: operator.Role}

	found, err := findActiveCredentialGrant(context.Background(), db, claims, credentialGrantMatch{Action: CredentialGrantActionTaskManualTrigger, Purpose: sshutil.PurposeTaskCommand, TaskID: credentialaudit.PtrUint(taskEntity.ID)})
	if err != nil || found == nil || found.ID != manualGrant.ID || found.ExpiresAt.Before(now) {
		t.Fatalf("operator manual grant 应授权，grant=%+v err=%v", found, err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, claims, credentialGrantMatch{Action: CredentialGrantActionTaskRestore, Purpose: sshutil.PurposeTaskRestore, TaskID: credentialaudit.PtrUint(taskEntity.ID)}); !errors.Is(err, ErrCredentialGrantInvalid) {
		t.Fatalf("operator 不应授权旧 admin-only task restore，实际: %v", err)
	}
}

func TestManualTriggerRouteRequiresGrantBeforeHandlerExecution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	operator := seedStepUpUser(t, db, "manual-route-operator", "operator")
	token := generatePrimaryToken(t, manager, operator)
	proof := generateStepUpProofForAction(t, manager, operator, auth.StepUpActionTaskManualTrigger)
	taskEntity := seedCredentialGrantTask(t, db, "command")
	calls := 0
	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.POST("/tasks/:id/trigger", RequireStepUp(db, manager, auth.StepUpActionTaskManualTrigger, sshutil.PurposeTaskCommand, "task_run"), RequireTaskManualTriggerCredentialGrant(db), func(c *gin.Context) {
		calls++
		c.Status(http.StatusNoContent)
	})

	missingGrantResp := performStepUpRequest(t, router, http.MethodPost, fmt.Sprintf("/tasks/%d/trigger", taskEntity.ID), token, proof, "")
	assertCredentialGrantRequiredEnvelope(t, missingGrantResp, "required")
	if calls != 0 {
		t.Fatalf("缺少 manual trigger grant 时不应进入 handler")
	}
	grant := createTaskRestoreGrantFixture(t, db, operator, CredentialGrantActionTaskManualTrigger, sshutil.PurposeTaskCommand, CredentialGrantStatusActive, credentialaudit.PtrUint(taskEntity.ID), nil, nil, "operator")
	grantedResp := performStepUpRequest(t, router, http.MethodPost, fmt.Sprintf("/tasks/%d/trigger", taskEntity.ID), token, proof, "")
	if grantedResp.Code != http.StatusNoContent || calls != 1 {
		t.Fatalf("有效 manual trigger grant 应进入 handler，status=%d calls=%d body=%s", grantedResp.Code, calls, grantedResp.Body.String())
	}
	if grant.ID == 0 {
		t.Fatalf("grant fixture 未创建")
	}
}

func TestBatchGrantEnforcementIsAllOrNothing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	operator := seedStepUpUser(t, db, "batch-enforce-operator", "operator")
	token := generatePrimaryToken(t, manager, operator)
	proof := generateStepUpProofForAction(t, manager, operator, auth.StepUpActionTaskBatchTrigger)
	taskA := seedCredentialGrantTask(t, db, "rsync")
	taskB := seedCredentialGrantTask(t, db, "rsync")
	called := false
	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.POST("/tasks/batch-trigger", func(c *gin.Context) {
		if !EnforceStepUp(c, db, manager, auth.StepUpActionTaskBatchTrigger, sshutil.PurposeTaskCommand, "task_bulk_run") {
			return
		}
		if !EnforceTaskBatchTriggerCredentialGrants(c, db, []uint{taskA.ID, taskB.ID}) {
			return
		}
		called = true
		c.Status(http.StatusNoContent)
	})

	createTaskRestoreGrantFixture(t, db, operator, CredentialGrantActionTaskBatchTrigger, sshutil.PurposeTaskCommand, CredentialGrantStatusActive, credentialaudit.PtrUint(taskA.ID), nil, nil, "operator")
	missingResp := performStepUpRequest(t, router, http.MethodPost, "/tasks/batch-trigger", token, proof, `{"task_ids":[]}`)
	assertCredentialGrantRequiredEnvelope(t, missingResp, "required")
	if called {
		t.Fatalf("缺少一个 batch trigger grant 时不应执行")
	}
	createTaskRestoreGrantFixture(t, db, operator, CredentialGrantActionTaskBatchTrigger, sshutil.PurposeTaskCommand, CredentialGrantStatusActive, credentialaudit.PtrUint(taskB.ID), nil, nil, "operator")
	grantedResp := performStepUpRequest(t, router, http.MethodPost, "/tasks/batch-trigger", token, proof, `{"task_ids":[]}`)
	if grantedResp.Code != http.StatusNoContent || !called {
		t.Fatalf("全部 batch trigger grant 存在时应执行，status=%d body=%s", grantedResp.Code, grantedResp.Body.String())
	}
}

func TestFindActiveTaskRestoreCredentialGrantMatchesTaskScopeAndRejectsWrongTuple(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	user := seedStepUpUser(t, db, "grant-task-restore-match-admin", "admin")
	other := seedStepUpUser(t, db, "grant-task-restore-match-other", "admin")
	taskEntity := seedCredentialGrantTask(t, db, "rsync")
	otherTask := seedCredentialGrantTask(t, db, "restic")
	node := seedCredentialGrantNode(t, db)
	policy := model.Policy{Name: "grant-policy"}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建授权测试策略失败: %v", err)
	}
	now := time.Now().UTC()
	claims := &auth.Claims{UserID: user.ID, Username: user.Username, Role: user.Role}
	otherClaims := &auth.Claims{UserID: other.ID, Username: other.Username, Role: other.Role}

	valid := createTaskRestoreGrantFixture(t, db, user, CredentialGrantActionTaskRestore, sshutil.PurposeTaskRestore, CredentialGrantStatusActive, credentialaudit.PtrUint(taskEntity.ID), nil, nil, "admin")
	createTaskRestoreGrantFixture(t, db, user, CredentialGrantActionTaskRestore, sshutil.PurposeTaskRestore, CredentialGrantStatusActive, credentialaudit.PtrUint(otherTask.ID), nil, nil, "admin")
	createTaskRestoreGrantFixture(t, db, other, CredentialGrantActionTaskRestore, sshutil.PurposeTaskRestore, CredentialGrantStatusRevoked, credentialaudit.PtrUint(taskEntity.ID), nil, nil, "admin")
	createTaskRestoreGrantFixture(t, db, user, CredentialGrantActionTerminalOpen, sshutil.PurposeTerminal, CredentialGrantStatusActive, nil, credentialaudit.PtrUint(node.ID), nil, "admin")
	createTaskRestoreGrantFixture(t, db, user, CredentialGrantActionConfigImport, CredentialGrantPurposeConfigImport, CredentialGrantStatusActive, nil, nil, nil, "admin")
	createTaskRestoreGrantFixture(t, db, user, CredentialGrantActionTaskRestore, sshutil.PurposeTaskBackup, CredentialGrantStatusDenied, credentialaudit.PtrUint(taskEntity.ID), nil, nil, "admin")
	createTaskRestoreGrantFixture(t, db, user, CredentialGrantActionTaskRestore, sshutil.PurposeTaskRestore, CredentialGrantStatusActive, credentialaudit.PtrUint(taskEntity.ID), credentialaudit.PtrUint(node.ID), nil, "admin")
	createTaskRestoreGrantFixture(t, db, user, CredentialGrantActionTaskRestore, sshutil.PurposeTaskRestore, CredentialGrantStatusActive, credentialaudit.PtrUint(taskEntity.ID), nil, credentialaudit.PtrUint(policy.ID), "admin")
	createTaskRestoreGrantFixture(t, db, user, CredentialGrantActionTaskRestore, sshutil.PurposeTaskRestore, CredentialGrantStatusActive, credentialaudit.PtrUint(taskEntity.ID), nil, nil, "operator")

	found, err := findActiveCredentialGrant(context.Background(), db, claims, credentialGrantMatch{Action: CredentialGrantActionTaskRestore, Purpose: sshutil.PurposeTaskRestore, TaskID: credentialaudit.PtrUint(taskEntity.ID)})
	if err != nil || found == nil || found.ID != valid.ID || found.ExpiresAt.Before(now) {
		t.Fatalf("有效任务恢复 task-scoped grant 应授权，grant=%+v err=%v", found, err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, claims, credentialGrantMatch{Action: CredentialGrantActionTaskRestore, Purpose: sshutil.PurposeTaskRestore, TaskID: credentialaudit.PtrUint(otherTask.ID + 1000)}); !errors.Is(err, ErrCredentialGrantRequired) {
		t.Fatalf("wrong task 应拒绝，实际: %v", err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, otherClaims, credentialGrantMatch{Action: CredentialGrantActionTaskRestore, Purpose: sshutil.PurposeTaskRestore, TaskID: credentialaudit.PtrUint(taskEntity.ID)}); !errors.Is(err, ErrCredentialGrantRevoked) {
		t.Fatalf("wrong user 的 revoked 任务恢复 grant 应拒绝，实际: %v", err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, claims, credentialGrantMatch{Action: CredentialGrantActionTerminalOpen, Purpose: sshutil.PurposeTaskRestore, TaskID: credentialaudit.PtrUint(taskEntity.ID)}); !errors.Is(err, ErrCredentialGrantRequired) {
		t.Fatalf("wrong action 应拒绝，实际: %v", err)
	}
	if _, err := findActiveCredentialGrant(context.Background(), db, claims, credentialGrantMatch{Action: CredentialGrantActionTaskRestore, Purpose: sshutil.PurposeTaskBackup, TaskID: credentialaudit.PtrUint(taskEntity.ID)}); !errors.Is(err, ErrCredentialGrantDenied) {
		t.Fatalf("wrong purpose 的 denied 任务恢复 grant 应拒绝，实际: %v", err)
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

	_, err := findActiveCredentialGrant(context.Background(), db, claims, credentialGrantMatch{Action: CredentialGrantActionTerminalOpen, Purpose: sshutil.PurposeTerminal, NodeID: credentialaudit.PtrUint(node.ID)})
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

func TestSnapshotRestoreRouteRequiresGrantBeforeHandlerExecution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "snapshot-route-before-admin", "admin")
	token := generatePrimaryToken(t, manager, admin)
	proof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionSnapshotRestore)
	taskEntity := seedCredentialGrantTask(t, db, "restic")
	called := false
	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.POST("/tasks/:id/snapshots/:sid/restore", middleware.RequireRole("admin"), RequireStepUp(db, manager, auth.StepUpActionSnapshotRestore, sshutil.PurposeSnapshot, "snapshot_restore"), RequireSnapshotRestoreCredentialGrant(db), func(c *gin.Context) {
		called = true
		c.Status(http.StatusNoContent)
	})

	missingProofResp := performStepUpRequest(t, router, http.MethodPost, fmt.Sprintf("/tasks/%d/snapshots/abcdef123456/restore", taskEntity.ID), token, "", "")
	assertStepUpRequiredEnvelope(t, missingProofResp)
	if called {
		t.Fatalf("缺少 step-up proof 时不应进入快照恢复 handler")
	}

	missingGrantResp := performStepUpRequest(t, router, http.MethodPost, fmt.Sprintf("/tasks/%d/snapshots/abcdef123456/restore", taskEntity.ID), token, proof, `{"includes":["/safe"],"targetPath":"/tmp/xirang-restore"}`)
	assertCredentialGrantRequiredEnvelope(t, missingGrantResp, "required")
	if called {
		t.Fatalf("缺少快照恢复授权时不应进入恢复 handler")
	}

	events := loadCredentialAuditEvents(t, db, CredentialGrantActionSnapshotRestore)
	if len(events) != 3 {
		t.Fatalf("快照恢复拒绝应写 step-up 和 blocked grant 审计事件，实际: %+v", events)
	}
	for _, event := range events {
		metadata := assertNoForbiddenAuditMetadata(t, event.Metadata)
		if event.CredentialKind == credentialGrantKind && (metadata["stage"] != "grant_check" || metadata["status"] != "required" || metadata["task_id"] != float64(taskEntity.ID)) {
			t.Fatalf("blocked audit metadata 不符合预期: %#v", metadata)
		}
	}
}

func TestTaskRestoreRouteRequiresGrantBeforeHandlerExecution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "task-restore-route-before-admin", "admin")
	token := generatePrimaryToken(t, manager, admin)
	proof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionTaskRestoreTrigger)
	taskEntity := seedCredentialGrantTask(t, db, "rsync")
	called := false
	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.POST("/tasks/:id/restore", middleware.RequireRole("admin"), RequireStepUp(db, manager, auth.StepUpActionTaskRestoreTrigger, sshutil.PurposeTaskRestore, "task_restore"), RequireTaskRestoreCredentialGrant(db), func(c *gin.Context) {
		called = true
		c.Status(http.StatusNoContent)
	})

	missingProofResp := performStepUpRequest(t, router, http.MethodPost, fmt.Sprintf("/tasks/%d/restore", taskEntity.ID), token, "", "")
	assertStepUpRequiredEnvelope(t, missingProofResp)
	if called {
		t.Fatalf("缺少 step-up proof 时不应进入任务恢复 handler")
	}

	missingGrantResp := performStepUpRequest(t, router, http.MethodPost, fmt.Sprintf("/tasks/%d/restore", taskEntity.ID), token, proof, `{"target_path":"/tmp/xirang-restore"}`)
	assertCredentialGrantRequiredEnvelope(t, missingGrantResp, "required")
	if called {
		t.Fatalf("缺少任务恢复授权时不应进入恢复 handler")
	}

	events := loadCredentialAuditEvents(t, db, CredentialGrantActionTaskRestore)
	if len(events) != 3 {
		t.Fatalf("任务恢复拒绝应写 step-up 和 blocked grant 审计事件，实际: %+v", events)
	}
	for _, event := range events {
		metadata := assertNoForbiddenAuditMetadata(t, event.Metadata)
		if event.CredentialKind == credentialGrantKind && (metadata["stage"] != "grant_check" || metadata["status"] != "required" || metadata["task_id"] != float64(taskEntity.ID)) {
			t.Fatalf("blocked audit metadata 不符合预期: %#v", metadata)
		}
	}
}

func TestSnapshotRestoreRouteUsesActiveTaskGrantAndAuditIsSafe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "snapshot-route-valid-admin", "admin")
	token := generatePrimaryToken(t, manager, admin)
	proof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionSnapshotRestore)
	taskEntity := seedCredentialGrantTask(t, db, "restic")
	called := false
	now := time.Now().UTC()
	grant := model.CredentialAccessGrant{
		RequesterUserID:     admin.ID,
		RequesterUsername:   admin.Username,
		RequesterRole:       admin.Role,
		Action:              CredentialGrantActionSnapshotRestore,
		Purpose:             sshutil.PurposeSnapshot,
		TaskID:              credentialaudit.PtrUint(taskEntity.ID),
		Reason:              "例行恢复",
		Status:              CredentialGrantStatusActive,
		RequestedTTLSeconds: 600,
		RequestedAt:         now,
		ExpiresAt:           now.Add(10 * time.Minute),
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatalf("创建快照恢复 grant 失败: %v", err)
	}

	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.POST("/tasks/:id/snapshots/:sid/restore", middleware.RequireRole("admin"), RequireStepUp(db, manager, auth.StepUpActionSnapshotRestore, sshutil.PurposeSnapshot, "snapshot_restore"), RequireSnapshotRestoreCredentialGrant(db), func(c *gin.Context) {
		called = true
		respondMessage(c, "恢复成功")
	})

	resp := performStepUpRequest(t, router, http.MethodPost, fmt.Sprintf("/tasks/%d/snapshots/abcdef123456/restore", taskEntity.ID), token, proof, `{"includes":["/safe"],"targetPath":"/tmp/xirang-restore"}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("带有效快照恢复 grant 应放行，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	if !called {
		t.Fatalf("有效快照恢复 grant 应进入 handler")
	}

	events := loadCredentialAuditEvents(t, db, CredentialGrantActionSnapshotRestore)
	if len(events) != 2 {
		t.Fatalf("快照恢复成功放行应写 step-up/grant use 审计事件，实际: %+v", events)
	}
	if events[0].Outcome != credentialaudit.OutcomeSuccess || events[1].Outcome != credentialaudit.OutcomeSuccess || events[1].TaskID == nil || *events[1].TaskID != taskEntity.ID {
		t.Fatalf("快照恢复 grant use 审计事件不符合预期: %+v", events)
	}
	for _, event := range events {
		metadata := assertNoForbiddenAuditMetadata(t, event.Metadata)
		if event.CredentialKind == credentialGrantKind && (metadata["stage"] != "use" || metadata["status"] != "active" || metadata["task_id"].(float64) != float64(taskEntity.ID)) {
			t.Fatalf("快照恢复 grant use 审计 metadata 不符合预期: %#v", metadata)
		}
	}
}

func TestTaskRestoreRouteUsesActiveTaskGrantAndAuditIsSafe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "task-restore-route-valid-admin", "admin")
	token := generatePrimaryToken(t, manager, admin)
	proof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionTaskRestoreTrigger)
	taskEntity := seedCredentialGrantTask(t, db, "rsync")
	grant := createTaskRestoreGrantFixture(t, db, admin, CredentialGrantActionTaskRestore, sshutil.PurposeTaskRestore, CredentialGrantStatusActive, credentialaudit.PtrUint(taskEntity.ID), nil, nil, "admin")

	router := newTaskRestoreGrantEnforcementRouter(db, manager)
	resp := performStepUpRequest(t, router, http.MethodPost, fmt.Sprintf("/tasks/%d/restore", taskEntity.ID), token, proof, `{"target_path":"/tmp/xirang-restore"}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("带有效任务恢复 grant 应放行，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	events := loadCredentialAuditEvents(t, db, CredentialGrantActionTaskRestore)
	if len(events) != 2 {
		t.Fatalf("任务恢复成功放行应写 step-up/grant use 审计事件，实际: %+v", events)
	}
	if events[0].Outcome != credentialaudit.OutcomeSuccess || events[1].Outcome != credentialaudit.OutcomeSuccess || events[1].TaskID == nil || *events[1].TaskID != taskEntity.ID {
		t.Fatalf("任务恢复 grant use 审计事件不符合预期: %+v", events)
	}
	for _, event := range events {
		metadata := assertNoForbiddenAuditMetadata(t, event.Metadata)
		if event.CredentialKind == credentialGrantKind {
			if metadata["stage"] != "use" || metadata["status"] != "active" || metadata["grant_id"].(float64) != float64(grant.ID) || metadata["task_id"].(float64) != float64(taskEntity.ID) {
				t.Fatalf("任务恢复 grant use 审计 metadata 不符合预期: %#v", metadata)
			}
		}
	}
}

func TestTaskRestoreRouteRejectsInactiveWrongTupleAndOtherOperationGrants(t *testing.T) {
	gin.SetMode(gin.TestMode)
	testCases := []struct {
		name       string
		wantStatus string
		seedGrant  func(t *testing.T, db *gorm.DB, admin model.User, taskID uint)
		assertDB   func(t *testing.T, db *gorm.DB)
	}{
		{
			name:       "expired",
			wantStatus: CredentialGrantStatusExpired,
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User, taskID uint) {
				now := time.Now().UTC()
				grant := model.CredentialAccessGrant{RequesterUserID: admin.ID, RequesterUsername: admin.Username, RequesterRole: admin.Role, Action: CredentialGrantActionTaskRestore, Purpose: sshutil.PurposeTaskRestore, TaskID: credentialaudit.PtrUint(taskID), Reason: "例行恢复", Status: CredentialGrantStatusActive, RequestedTTLSeconds: 60, RequestedAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(-time.Minute)}
				if err := db.Create(&grant).Error; err != nil {
					t.Fatalf("创建过期任务恢复 grant 失败: %v", err)
				}
			},
			assertDB: func(t *testing.T, db *gorm.DB) {
				var grant model.CredentialAccessGrant
				if err := db.Where("action = ? AND purpose = ?", CredentialGrantActionTaskRestore, sshutil.PurposeTaskRestore).First(&grant).Error; err != nil {
					t.Fatalf("读取过期任务恢复 grant 失败: %v", err)
				}
				if grant.Status != CredentialGrantStatusExpired {
					t.Fatalf("过期 active 任务恢复 grant 应标记 expired，实际: %s", grant.Status)
				}
			},
		},
		{
			name:       "revoked",
			wantStatus: CredentialGrantStatusRevoked,
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User, taskID uint) {
				createTaskRestoreGrantFixture(t, db, admin, CredentialGrantActionTaskRestore, sshutil.PurposeTaskRestore, CredentialGrantStatusRevoked, credentialaudit.PtrUint(taskID), nil, nil, "admin")
			},
		},
		{
			name:       "denied",
			wantStatus: CredentialGrantStatusDenied,
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User, taskID uint) {
				createTaskRestoreGrantFixture(t, db, admin, CredentialGrantActionTaskRestore, sshutil.PurposeTaskRestore, CredentialGrantStatusDenied, credentialaudit.PtrUint(taskID), nil, nil, "admin")
			},
		},
		{
			name:       "wrong-user",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, _ model.User, taskID uint) {
				other := seedStepUpUser(t, db, "task-restore-other-user", "admin")
				createTaskRestoreGrantFixture(t, db, other, CredentialGrantActionTaskRestore, sshutil.PurposeTaskRestore, CredentialGrantStatusActive, credentialaudit.PtrUint(taskID), nil, nil, "admin")
			},
		},
		{
			name:       "wrong-role",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User, taskID uint) {
				createTaskRestoreGrantFixture(t, db, admin, CredentialGrantActionTaskRestore, sshutil.PurposeTaskRestore, CredentialGrantStatusActive, credentialaudit.PtrUint(taskID), nil, nil, "operator")
			},
		},
		{
			name:       "wrong-task",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User, taskID uint) {
				createTaskRestoreGrantFixture(t, db, admin, CredentialGrantActionTaskRestore, sshutil.PurposeTaskRestore, CredentialGrantStatusActive, credentialaudit.PtrUint(taskID+1000), nil, nil, "admin")
			},
		},
		{
			name:       "wrong-action-terminal",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User, _ uint) {
				node := seedCredentialGrantNode(t, db)
				createTaskRestoreGrantFixture(t, db, admin, CredentialGrantActionTerminalOpen, sshutil.PurposeTerminal, CredentialGrantStatusActive, nil, credentialaudit.PtrUint(node.ID), nil, "admin")
			},
		},
		{
			name:       "wrong-action-config",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User, _ uint) {
				createTaskRestoreGrantFixture(t, db, admin, CredentialGrantActionConfigImport, CredentialGrantPurposeConfigImport, CredentialGrantStatusActive, nil, nil, nil, "admin")
			},
		},
		{
			name:       "wrong-purpose",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User, taskID uint) {
				createTaskRestoreGrantFixture(t, db, admin, CredentialGrantActionTaskRestore, sshutil.PurposeTaskBackup, CredentialGrantStatusActive, credentialaudit.PtrUint(taskID), nil, nil, "admin")
			},
		},
		{
			name:       "wrong-node-scope",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User, taskID uint) {
				node := seedCredentialGrantNode(t, db)
				createTaskRestoreGrantFixture(t, db, admin, CredentialGrantActionTaskRestore, sshutil.PurposeTaskRestore, CredentialGrantStatusActive, credentialaudit.PtrUint(taskID), credentialaudit.PtrUint(node.ID), nil, "admin")
			},
		},
		{
			name:       "wrong-policy-scope",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User, taskID uint) {
				policy := model.Policy{Name: "task-restore-deny-policy"}
				if err := db.Create(&policy).Error; err != nil {
					t.Fatalf("创建任务恢复拒绝测试策略失败: %v", err)
				}
				createTaskRestoreGrantFixture(t, db, admin, CredentialGrantActionTaskRestore, sshutil.PurposeTaskRestore, CredentialGrantStatusActive, credentialaudit.PtrUint(taskID), nil, credentialaudit.PtrUint(policy.ID), "admin")
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			db := openStepUpHandlerTestDB(t)
			manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
			admin := seedStepUpUser(t, db, "task-restore-deny-admin", "admin")
			token := generatePrimaryToken(t, manager, admin)
			proof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionTaskRestoreTrigger)
			taskEntity := seedCredentialGrantTask(t, db, "rsync")
			tc.seedGrant(t, db, admin, taskEntity.ID)

			router := newTaskRestoreGrantEnforcementRouter(db, manager)
			resp := performStepUpRequest(t, router, http.MethodPost, fmt.Sprintf("/tasks/%d/restore", taskEntity.ID), token, proof, `{"target_path":"/tmp/xirang-restore"}`)
			assertCredentialGrantRequiredEnvelope(t, resp, tc.wantStatus)
			if tc.assertDB != nil {
				tc.assertDB(t, db)
			}

			events := loadCredentialAuditEvents(t, db, CredentialGrantActionTaskRestore)
			if len(events) == 0 {
				t.Fatalf("任务恢复 grant 拒绝应写审计事件")
			}
			for _, event := range events {
				assertNoForbiddenAuditMetadata(t, event.Metadata)
			}
		})
	}
}

func TestSnapshotRestoreRouteRejectsInactiveWrongTupleAndOtherOperationGrants(t *testing.T) {
	gin.SetMode(gin.TestMode)
	testCases := []struct {
		name       string
		wantStatus string
		seedGrant  func(t *testing.T, db *gorm.DB, admin model.User, taskID uint)
		assertDB   func(t *testing.T, db *gorm.DB)
	}{
		{
			name:       "expired",
			wantStatus: CredentialGrantStatusExpired,
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User, taskID uint) {
				now := time.Now().UTC()
				grant := model.CredentialAccessGrant{RequesterUserID: admin.ID, RequesterUsername: admin.Username, RequesterRole: admin.Role, Action: CredentialGrantActionSnapshotRestore, Purpose: sshutil.PurposeSnapshot, TaskID: credentialaudit.PtrUint(taskID), Reason: "例行恢复", Status: CredentialGrantStatusActive, RequestedTTLSeconds: 60, RequestedAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(-time.Minute)}
				if err := db.Create(&grant).Error; err != nil {
					t.Fatalf("创建过期快照恢复 grant 失败: %v", err)
				}
			},
			assertDB: func(t *testing.T, db *gorm.DB) {
				var grant model.CredentialAccessGrant
				if err := db.Where("action = ? AND purpose = ?", CredentialGrantActionSnapshotRestore, sshutil.PurposeSnapshot).First(&grant).Error; err != nil {
					t.Fatalf("读取过期快照恢复 grant 失败: %v", err)
				}
				if grant.Status != CredentialGrantStatusExpired {
					t.Fatalf("过期 active 快照恢复 grant 应标记 expired，实际: %s", grant.Status)
				}
			},
		},
		{
			name:       "revoked",
			wantStatus: CredentialGrantStatusRevoked,
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User, taskID uint) {
				createSnapshotRestoreGrantFixture(t, db, admin, CredentialGrantActionSnapshotRestore, sshutil.PurposeSnapshot, CredentialGrantStatusRevoked, credentialaudit.PtrUint(taskID), "admin")
			},
		},
		{
			name:       "denied",
			wantStatus: CredentialGrantStatusDenied,
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User, taskID uint) {
				createSnapshotRestoreGrantFixture(t, db, admin, CredentialGrantActionSnapshotRestore, sshutil.PurposeSnapshot, CredentialGrantStatusDenied, credentialaudit.PtrUint(taskID), "admin")
			},
		},
		{
			name:       "wrong-user",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, _ model.User, taskID uint) {
				other := seedStepUpUser(t, db, "snapshot-other-user", "admin")
				createSnapshotRestoreGrantFixture(t, db, other, CredentialGrantActionSnapshotRestore, sshutil.PurposeSnapshot, CredentialGrantStatusActive, credentialaudit.PtrUint(taskID), "admin")
			},
		},
		{
			name:       "wrong-role",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User, taskID uint) {
				createSnapshotRestoreGrantFixture(t, db, admin, CredentialGrantActionSnapshotRestore, sshutil.PurposeSnapshot, CredentialGrantStatusActive, credentialaudit.PtrUint(taskID), "operator")
			},
		},
		{
			name:       "wrong-task",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User, taskID uint) {
				createSnapshotRestoreGrantFixture(t, db, admin, CredentialGrantActionSnapshotRestore, sshutil.PurposeSnapshot, CredentialGrantStatusActive, credentialaudit.PtrUint(taskID+1000), "admin")
			},
		},
		{
			name:       "wrong-action-terminal",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User, _ uint) {
				node := seedCredentialGrantNode(t, db)
				grant := model.CredentialAccessGrant{RequesterUserID: admin.ID, RequesterUsername: admin.Username, RequesterRole: admin.Role, Action: CredentialGrantActionTerminalOpen, Purpose: sshutil.PurposeTerminal, NodeID: credentialaudit.PtrUint(node.ID), Reason: "维护", Status: CredentialGrantStatusActive, RequestedTTLSeconds: 600, RequestedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(10 * time.Minute)}
				if err := db.Create(&grant).Error; err != nil {
					t.Fatalf("创建 terminal grant fixture 失败: %v", err)
				}
			},
		},
		{
			name:       "wrong-action-config",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User, _ uint) {
				createConfigImportGrantFixture(t, db, admin, CredentialGrantActionConfigImport, CredentialGrantPurposeConfigImport, CredentialGrantStatusActive)
			},
		},
		{
			name:       "wrong-purpose",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User, taskID uint) {
				createSnapshotRestoreGrantFixture(t, db, admin, CredentialGrantActionSnapshotRestore, "snapshot_diff", CredentialGrantStatusActive, credentialaudit.PtrUint(taskID), "admin")
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			db := openStepUpHandlerTestDB(t)
			manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
			admin := seedStepUpUser(t, db, "snapshot-deny-admin", "admin")
			token := generatePrimaryToken(t, manager, admin)
			proof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionSnapshotRestore)
			taskEntity := seedCredentialGrantTask(t, db, "restic")
			tc.seedGrant(t, db, admin, taskEntity.ID)

			router := newSnapshotRestoreGrantEnforcementRouter(db, manager)
			resp := performStepUpRequest(t, router, http.MethodPost, fmt.Sprintf("/tasks/%d/snapshots/abcdef123456/restore", taskEntity.ID), token, proof, `{"includes":["/safe"],"targetPath":"/tmp/xirang-restore"}`)
			assertCredentialGrantRequiredEnvelope(t, resp, tc.wantStatus)
			if tc.assertDB != nil {
				tc.assertDB(t, db)
			}

			events := loadCredentialAuditEvents(t, db, CredentialGrantActionSnapshotRestore)
			if len(events) == 0 {
				t.Fatalf("快照恢复 grant 拒绝应写审计事件")
			}
			for _, event := range events {
				assertNoForbiddenAuditMetadata(t, event.Metadata)
			}
		})
	}
}

func TestSnapshotBrowseRoutesDoNotRequireSnapshotRestoreGrant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "snapshot-browse-admin", "admin")
	token := generatePrimaryToken(t, manager, admin)
	taskEntity := seedCredentialGrantTask(t, db, "rsync")
	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	handler := NewSnapshotHandler(db, nil, nil)
	router.GET("/tasks/:id/snapshots", middleware.RBAC("tasks:read"), handler.ListSnapshots)

	resp := performStepUpRequest(t, router, http.MethodGet, fmt.Sprintf("/tasks/%d/snapshots", taskEntity.ID), token, "", "")
	if resp.Code != http.StatusGone || strings.Contains(resp.Body.String(), credentialGrantRequiredCode) {
		t.Fatalf("快照浏览应退役为 410 且不要求快照恢复 grant，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var grantAuditCount int64
	if err := db.Model(&model.CredentialAuditEvent{}).Where("credential_kind = ? AND action = ?", credentialGrantKind, CredentialGrantActionSnapshotRestore).Count(&grantAuditCount).Error; err != nil {
		t.Fatalf("统计快照恢复 grant 审计失败: %v", err)
	}
	if grantAuditCount != 0 {
		t.Fatalf("快照浏览不应写快照恢复 grant 使用/阻断审计，实际数量: %d", grantAuditCount)
	}
}

func TestConfigImportRouteRequiresStepUpAndCredentialGrantBeforeMutation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "config-import-route-admin", "admin")
	token := generatePrimaryToken(t, manager, admin)
	proof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionConfigImport)
	router := newConfigImportGrantEnforcementRouter(db, manager)
	body := configImportSSHKeyBody("step-up-entry")

	missingProofResp := performStepUpRequest(t, router, http.MethodPost, "/config/import", token, "", body)
	assertStepUpRequiredEnvelope(t, missingProofResp)
	assertNoImportedSSHKey(t, db, "step-up-entry")

	missingGrantResp := performStepUpRequest(t, router, http.MethodPost, "/config/import", token, proof, body)
	assertCredentialGrantRequiredEnvelope(t, missingGrantResp, "required")
	assertNoImportedSSHKey(t, db, "step-up-entry")

	events := loadCredentialAuditEvents(t, db, CredentialGrantActionConfigImport)
	if len(events) != 3 {
		t.Fatalf("缺少 step-up/grant blocked 审计事件，实际: %+v", events)
	}
	for _, event := range events {
		assertNoForbiddenAuditMetadata(t, event.Metadata)
	}
}

func TestConfigImportRouteUsesActiveSystemGrantAndAuditsSafely(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "config-import-valid-admin", "admin")
	token := generatePrimaryToken(t, manager, admin)
	proof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionConfigImport)
	now := time.Now().UTC()
	grant := model.CredentialAccessGrant{
		RequesterUserID:     admin.ID,
		RequesterUsername:   admin.Username,
		RequesterRole:       admin.Role,
		Action:              CredentialGrantActionConfigImport,
		Purpose:             CredentialGrantPurposeConfigImport,
		Reason:              "例行恢复",
		Status:              CredentialGrantStatusActive,
		RequestedTTLSeconds: 600,
		RequestedAt:         now,
		ExpiresAt:           now.Add(10 * time.Minute),
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatalf("创建配置导入 grant 失败: %v", err)
	}

	router := newConfigImportGrantEnforcementRouter(db, manager)
	resp := performStepUpRequest(t, router, http.MethodPost, "/config/import", token, proof, configImportSSHKeyBody("valid-entry"))
	if resp.Code != http.StatusOK {
		t.Fatalf("带有效配置导入 grant 应成功，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	assertImportedSSHKey(t, db, "valid-entry")

	events := loadCredentialAuditEvents(t, db, CredentialGrantActionConfigImport)
	if len(events) != 3 {
		t.Fatalf("配置导入 step-up/grant use/import 应写 3 条审计事件，实际: %+v", events)
	}
	for _, event := range events {
		if event.Outcome != credentialaudit.OutcomeSuccess || event.UserID != admin.ID {
			t.Fatalf("配置导入授权使用审计事件不符合预期: %+v", event)
		}
		metadata := assertNoForbiddenAuditMetadata(t, event.Metadata)
		if event.CredentialKind == "system_import" {
			if metadata["stage"] != "success" || metadata["key_count"].(float64) != 1 {
				t.Fatalf("配置导入成功审计 metadata 不符合预期: %#v", metadata)
			}
		}
	}
}

func TestConfigExportRouteRequiresGrantBeforeHandlerExecution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "config-export-before-admin", "admin")
	token := generatePrimaryToken(t, manager, admin)
	proof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionConfigExport)
	called := false
	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.GET("/config/export", middleware.RequireRole("admin"), RequireStepUpIf(db, manager, auth.StepUpActionConfigExport, CredentialGrantPurposeConfigExport, "settings_export_sensitive", func(c *gin.Context) bool {
		return c.Query("include_secrets") == "true"
	}), RequireConfigExportCredentialGrantIf(db, func(c *gin.Context) bool {
		return c.Query("include_secrets") == "true"
	}), func(c *gin.Context) {
		called = true
		c.Status(http.StatusNoContent)
	})

	resp := performStepUpRequest(t, router, http.MethodGet, "/config/export?include_secrets=true", token, proof, "")
	assertCredentialGrantRequiredEnvelope(t, resp, "required")
	if called {
		t.Fatalf("缺少配置导出授权时不应进入导出 handler")
	}

	events := loadCredentialAuditEvents(t, db, CredentialGrantActionConfigExport)
	if len(events) != 2 {
		t.Fatalf("敏感导出拒绝应仅写 step-up 和 blocked grant 审计事件，实际: %+v", events)
	}
	if events[0].Outcome != credentialaudit.OutcomeSuccess || events[1].Outcome != credentialaudit.OutcomeBlocked {
		t.Fatalf("敏感导出拒绝审计 outcome 不符合预期: %+v", events)
	}
	for _, event := range events {
		assertNoForbiddenAuditMetadata(t, event.Metadata)
	}
}

func TestConfigExportRouteUsesActiveSystemGrantAndKeepsSafeExportUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "config-export-valid-admin", "admin")
	token := generatePrimaryToken(t, manager, admin)
	proof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionConfigExport)
	now := time.Now().UTC()
	grant := model.CredentialAccessGrant{
		RequesterUserID:     admin.ID,
		RequesterUsername:   admin.Username,
		RequesterRole:       admin.Role,
		Action:              CredentialGrantActionConfigExport,
		Purpose:             CredentialGrantPurposeConfigExport,
		Reason:              "例行导出",
		Status:              CredentialGrantStatusActive,
		RequestedTTLSeconds: 600,
		RequestedAt:         now,
		ExpiresAt:           now.Add(10 * time.Minute),
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatalf("创建配置导出 grant 失败: %v", err)
	}

	router := newConfigExportGrantEnforcementRouter(db, manager)
	safeResp := performStepUpRequest(t, router, http.MethodGet, "/config/export", token, "", "")
	if safeResp.Code != http.StatusOK {
		t.Fatalf("普通配置导出不应要求 step-up 或 grant，实际: %d，响应: %s", safeResp.Code, safeResp.Body.String())
	}
	var grantAuditCount int64
	if err := db.Model(&model.CredentialAuditEvent{}).Where("action = ? AND credential_kind = ?", CredentialGrantActionConfigExport, credentialGrantKind).Count(&grantAuditCount).Error; err != nil {
		t.Fatalf("统计 grant audit 失败: %v", err)
	}
	if grantAuditCount != 0 {
		t.Fatalf("普通配置导出不应写配置导出 grant 使用/阻断审计，实际数量: %d", grantAuditCount)
	}
	if err := db.Where("action = ?", CredentialGrantActionConfigExport).Delete(&model.CredentialAuditEvent{}).Error; err != nil {
		t.Fatalf("清理普通导出审计事件失败: %v", err)
	}

	sensitiveResp := performStepUpRequest(t, router, http.MethodGet, "/config/export?include_secrets=true", token, proof, "")
	if sensitiveResp.Code != http.StatusOK {
		t.Fatalf("带有效配置导出 grant 应允许敏感导出，实际: %d，响应: %s", sensitiveResp.Code, sensitiveResp.Body.String())
	}
	events := loadCredentialAuditEvents(t, db, CredentialGrantActionConfigExport)
	if len(events) != 3 {
		t.Fatalf("敏感配置导出应写 step-up/grant use/export 成功审计事件，实际: %+v", events)
	}
	for _, event := range events {
		metadata := assertNoForbiddenAuditMetadata(t, event.Metadata)
		if event.CredentialKind == credentialGrantKind {
			if event.Outcome != credentialaudit.OutcomeSuccess || metadata["stage"] != "use" || metadata["status"] != "active" {
				t.Fatalf("配置导出 grant use 审计不符合预期: event=%+v metadata=%#v", event, metadata)
			}
		}
		if event.CredentialKind == "config_export" {
			if event.Outcome != credentialaudit.OutcomeSuccess || metadata["stage"] != "success" || metadata["with_sensitive"] != true {
				t.Fatalf("配置导出成功审计不符合预期: event=%+v metadata=%#v", event, metadata)
			}
		}
	}
}

func TestConfigExportRouteRejectsInactiveWrongTupleAndOtherOperationGrants(t *testing.T) {
	gin.SetMode(gin.TestMode)
	testCases := []struct {
		name       string
		wantStatus string
		seedGrant  func(t *testing.T, db *gorm.DB, admin model.User)
		assertDB   func(t *testing.T, db *gorm.DB)
	}{
		{
			name:       "expired",
			wantStatus: CredentialGrantStatusExpired,
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User) {
				now := time.Now().UTC()
				grant := model.CredentialAccessGrant{RequesterUserID: admin.ID, RequesterUsername: admin.Username, RequesterRole: admin.Role, Action: CredentialGrantActionConfigExport, Purpose: CredentialGrantPurposeConfigExport, Reason: "例行导出", Status: CredentialGrantStatusActive, RequestedTTLSeconds: 60, RequestedAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(-time.Minute)}
				if err := db.Create(&grant).Error; err != nil {
					t.Fatalf("创建过期配置导出 grant 失败: %v", err)
				}
			},
			assertDB: func(t *testing.T, db *gorm.DB) {
				var grant model.CredentialAccessGrant
				if err := db.Where("action = ? AND purpose = ?", CredentialGrantActionConfigExport, CredentialGrantPurposeConfigExport).First(&grant).Error; err != nil {
					t.Fatalf("读取过期配置导出 grant 失败: %v", err)
				}
				if grant.Status != CredentialGrantStatusExpired {
					t.Fatalf("过期 active 配置导出 grant 应标记 expired，实际: %s", grant.Status)
				}
			},
		},
		{
			name:       "revoked",
			wantStatus: CredentialGrantStatusRevoked,
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User) {
				createConfigExportGrantFixture(t, db, admin, CredentialGrantActionConfigExport, CredentialGrantPurposeConfigExport, CredentialGrantStatusRevoked, nil, "admin")
			},
		},
		{
			name:       "denied",
			wantStatus: CredentialGrantStatusDenied,
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User) {
				createConfigExportGrantFixture(t, db, admin, CredentialGrantActionConfigExport, CredentialGrantPurposeConfigExport, CredentialGrantStatusDenied, nil, "admin")
			},
		},
		{
			name:       "wrong-user",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, _ model.User) {
				other := seedStepUpUser(t, db, "config-export-other-user", "admin")
				createConfigExportGrantFixture(t, db, other, CredentialGrantActionConfigExport, CredentialGrantPurposeConfigExport, CredentialGrantStatusActive, nil, "admin")
			},
		},
		{
			name:       "wrong-role",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User) {
				createConfigExportGrantFixture(t, db, admin, CredentialGrantActionConfigExport, CredentialGrantPurposeConfigExport, CredentialGrantStatusActive, nil, "operator")
			},
		},
		{
			name:       "wrong-action-terminal",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User) {
				node := seedCredentialGrantNode(t, db)
				createConfigExportGrantFixture(t, db, admin, CredentialGrantActionTerminalOpen, sshutil.PurposeTerminal, CredentialGrantStatusActive, credentialaudit.PtrUint(node.ID), "admin")
			},
		},
		{
			name:       "wrong-action-import",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User) {
				createConfigExportGrantFixture(t, db, admin, CredentialGrantActionConfigImport, CredentialGrantPurposeConfigImport, CredentialGrantStatusActive, nil, "admin")
			},
		},
		{
			name:       "wrong-purpose",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User) {
				createConfigExportGrantFixture(t, db, admin, CredentialGrantActionConfigExport, CredentialGrantPurposeConfigImport, CredentialGrantStatusActive, nil, "admin")
			},
		},
		{
			name:       "wrong-resource",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User) {
				nodeID := uint(1)
				createConfigExportGrantFixture(t, db, admin, CredentialGrantActionConfigExport, CredentialGrantPurposeConfigExport, CredentialGrantStatusActive, &nodeID, "admin")
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			db := openStepUpHandlerTestDB(t)
			manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
			admin := seedStepUpUser(t, db, "config-export-deny-admin", "admin")
			token := generatePrimaryToken(t, manager, admin)
			proof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionConfigExport)
			tc.seedGrant(t, db, admin)

			router := newConfigExportGrantEnforcementRouter(db, manager)
			resp := performStepUpRequest(t, router, http.MethodGet, "/config/export?include_secrets=true", token, proof, "")
			assertCredentialGrantRequiredEnvelope(t, resp, tc.wantStatus)
			if tc.assertDB != nil {
				tc.assertDB(t, db)
			}

			events := loadCredentialAuditEvents(t, db, CredentialGrantActionConfigExport)
			if len(events) == 0 {
				t.Fatalf("配置导出 grant 拒绝应写审计事件")
			}
			for _, event := range events {
				assertNoForbiddenAuditMetadata(t, event.Metadata)
			}
		})
	}
}

func TestConfigImportRouteRejectsInactiveAndWrongCredentialGrantTuples(t *testing.T) {
	gin.SetMode(gin.TestMode)
	testCases := []struct {
		name       string
		wantStatus string
		seedGrant  func(t *testing.T, db *gorm.DB, admin model.User)
		assertDB   func(t *testing.T, db *gorm.DB)
	}{
		{
			name:       "expired",
			wantStatus: CredentialGrantStatusExpired,
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User) {
				now := time.Now().UTC()
				grant := model.CredentialAccessGrant{RequesterUserID: admin.ID, RequesterUsername: admin.Username, RequesterRole: admin.Role, Action: CredentialGrantActionConfigImport, Purpose: CredentialGrantPurposeConfigImport, Reason: "例行恢复", Status: CredentialGrantStatusActive, RequestedTTLSeconds: 60, RequestedAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(-time.Minute)}
				if err := db.Create(&grant).Error; err != nil {
					t.Fatalf("创建过期 grant 失败: %v", err)
				}
			},
			assertDB: func(t *testing.T, db *gorm.DB) {
				var grant model.CredentialAccessGrant
				if err := db.Where("action = ? AND purpose = ?", CredentialGrantActionConfigImport, CredentialGrantPurposeConfigImport).First(&grant).Error; err != nil {
					t.Fatalf("读取过期 grant 失败: %v", err)
				}
				if grant.Status != CredentialGrantStatusExpired {
					t.Fatalf("过期 active grant 应标记 expired，实际: %s", grant.Status)
				}
			},
		},
		{
			name:       "revoked",
			wantStatus: CredentialGrantStatusRevoked,
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User) {
				createConfigImportGrantFixture(t, db, admin, CredentialGrantActionConfigImport, CredentialGrantPurposeConfigImport, CredentialGrantStatusRevoked)
			},
		},
		{
			name:       "denied",
			wantStatus: CredentialGrantStatusDenied,
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User) {
				createConfigImportGrantFixture(t, db, admin, CredentialGrantActionConfigImport, CredentialGrantPurposeConfigImport, CredentialGrantStatusDenied)
			},
		},
		{
			name:       "wrong-user",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, _ model.User) {
				other := seedStepUpUser(t, db, "config-import-other-user", "admin")
				createConfigImportGrantFixture(t, db, other, CredentialGrantActionConfigImport, CredentialGrantPurposeConfigImport, CredentialGrantStatusActive)
			},
		},
		{
			name:       "wrong-action",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User) {
				createConfigImportGrantFixture(t, db, admin, CredentialGrantActionTerminalOpen, sshutil.PurposeTerminal, CredentialGrantStatusActive)
			},
		},
		{
			name:       "wrong-purpose",
			wantStatus: "required",
			seedGrant: func(t *testing.T, db *gorm.DB, admin model.User) {
				createConfigImportGrantFixture(t, db, admin, CredentialGrantActionConfigImport, "settings_export", CredentialGrantStatusActive)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			db := openStepUpHandlerTestDB(t)
			manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
			admin := seedStepUpUser(t, db, "config-import-deny-admin", "admin")
			token := generatePrimaryToken(t, manager, admin)
			proof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionConfigImport)
			tc.seedGrant(t, db, admin)

			keyName := "deny-entry-" + tc.name
			router := newConfigImportGrantEnforcementRouter(db, manager)
			resp := performStepUpRequest(t, router, http.MethodPost, "/config/import", token, proof, configImportSSHKeyBody(keyName))
			assertCredentialGrantRequiredEnvelope(t, resp, tc.wantStatus)
			assertNoImportedSSHKey(t, db, keyName)
			if tc.assertDB != nil {
				tc.assertDB(t, db)
			}

			events := loadCredentialAuditEvents(t, db, CredentialGrantActionConfigImport)
			if len(events) == 0 {
				t.Fatalf("配置导入 grant 拒绝应写审计事件")
			}
			for _, event := range events {
				assertNoForbiddenAuditMetadata(t, event.Metadata)
			}
		})
	}
}

func newSnapshotRestoreGrantEnforcementRouter(db *gorm.DB, manager *auth.JWTManager) *gin.Engine {
	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.POST("/tasks/:id/snapshots/:sid/restore", middleware.RequireRole("admin"), RequireStepUp(db, manager, auth.StepUpActionSnapshotRestore, sshutil.PurposeSnapshot, "snapshot_restore"), RequireSnapshotRestoreCredentialGrant(db), func(c *gin.Context) {
		respondMessage(c, "恢复成功")
	})
	return router
}

func newTaskRestoreGrantEnforcementRouter(db *gorm.DB, manager *auth.JWTManager) *gin.Engine {
	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.POST("/tasks/:id/restore", middleware.RequireRole("admin"), RequireStepUp(db, manager, auth.StepUpActionTaskRestoreTrigger, sshutil.PurposeTaskRestore, "task_restore"), RequireTaskRestoreCredentialGrant(db), func(c *gin.Context) {
		respondMessage(c, "恢复成功")
	})
	return router
}

func newConfigImportGrantEnforcementRouter(db *gorm.DB, manager *auth.JWTManager) *gin.Engine {
	handler := NewConfigHandler(db, nil)
	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.POST("/config/import", middleware.RequireRole("admin"), RequireStepUp(db, manager, auth.StepUpActionConfigImport, CredentialGrantPurposeConfigImport, "settings_import"), RequireConfigImportCredentialGrant(db), handler.Import)
	return router
}

func newConfigExportGrantEnforcementRouter(db *gorm.DB, manager *auth.JWTManager) *gin.Engine {
	handler := NewConfigHandler(db, nil)
	router := gin.New()
	router.Use(middleware.AuthMiddleware(manager, db))
	router.GET("/config/export", middleware.RequireRole("admin"), RequireStepUpIf(db, manager, auth.StepUpActionConfigExport, CredentialGrantPurposeConfigExport, "settings_export_sensitive", func(c *gin.Context) bool {
		return c.Query("include_secrets") == "true"
	}), RequireConfigExportCredentialGrantIf(db, func(c *gin.Context) bool {
		return c.Query("include_secrets") == "true"
	}), handler.Export)
	return router
}

func createSnapshotRestoreGrantFixture(t *testing.T, db *gorm.DB, user model.User, action, purpose, status string, taskID *uint, requesterRole string) {
	t.Helper()
	if requesterRole == "" {
		requesterRole = user.Role
	}
	now := time.Now().UTC()
	grant := model.CredentialAccessGrant{
		RequesterUserID:     user.ID,
		RequesterUsername:   user.Username,
		RequesterRole:       requesterRole,
		Action:              action,
		Purpose:             purpose,
		TaskID:              taskID,
		Reason:              "例行恢复",
		Status:              status,
		RequestedTTLSeconds: 600,
		RequestedAt:         now,
		ExpiresAt:           now.Add(10 * time.Minute),
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatalf("创建快照恢复 grant fixture 失败: %v", err)
	}
}

func createTaskRestoreGrantFixture(t *testing.T, db *gorm.DB, user model.User, action, purpose, status string, taskID *uint, nodeID *uint, policyID *uint, requesterRole string) model.CredentialAccessGrant {
	t.Helper()
	if requesterRole == "" {
		requesterRole = user.Role
	}
	now := time.Now().UTC()
	grant := model.CredentialAccessGrant{
		RequesterUserID:     user.ID,
		RequesterUsername:   user.Username,
		RequesterRole:       requesterRole,
		Action:              action,
		Purpose:             purpose,
		NodeID:              nodeID,
		TaskID:              taskID,
		PolicyID:            policyID,
		Reason:              "例行恢复",
		Status:              status,
		RequestedTTLSeconds: 600,
		RequestedAt:         now,
		ExpiresAt:           now.Add(10 * time.Minute),
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatalf("创建任务恢复 grant fixture 失败: %v", err)
	}
	return grant
}

func createConfigExportGrantFixture(t *testing.T, db *gorm.DB, user model.User, action, purpose, status string, nodeID *uint, requesterRole string) {
	t.Helper()
	if requesterRole == "" {
		requesterRole = user.Role
	}
	now := time.Now().UTC()
	grant := model.CredentialAccessGrant{
		RequesterUserID:     user.ID,
		RequesterUsername:   user.Username,
		RequesterRole:       requesterRole,
		Action:              action,
		Purpose:             purpose,
		NodeID:              nodeID,
		Reason:              "例行导出",
		Status:              status,
		RequestedTTLSeconds: 600,
		RequestedAt:         now,
		ExpiresAt:           now.Add(10 * time.Minute),
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatalf("创建配置导出 grant fixture 失败: %v", err)
	}
}

func createConfigImportGrantFixture(t *testing.T, db *gorm.DB, user model.User, action, purpose, status string) {
	t.Helper()
	now := time.Now().UTC()
	grant := model.CredentialAccessGrant{
		RequesterUserID:     user.ID,
		RequesterUsername:   user.Username,
		RequesterRole:       user.Role,
		Action:              action,
		Purpose:             purpose,
		Reason:              "例行恢复",
		Status:              status,
		RequestedTTLSeconds: 600,
		RequestedAt:         now,
		ExpiresAt:           now.Add(10 * time.Minute),
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatalf("创建配置导入 grant fixture 失败: %v", err)
	}
}

func configImportSSHKeyBody(keyName string) string {
	return fmt.Sprintf(`{"ssh_keys":[{"name":%q,"username":"fixture-user","key_type":"auto","fingerprint":"fp-redacted"}]}`, keyName)
}

func assertCredentialGrantRequiredEnvelope(t *testing.T, resp *httptest.ResponseRecorder, status string) {
	t.Helper()
	if resp.Code != http.StatusForbidden {
		t.Fatalf("期望 credential grant required 返回 403，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Code int `json:"code"`
		Data struct {
			ErrorCode string `json:"error_code"`
			Status    string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析 credential grant required 响应失败: %v", err)
	}
	if payload.Code != http.StatusForbidden || payload.Data.ErrorCode != credentialGrantRequiredCode || payload.Data.Status != status {
		t.Fatalf("credential grant required 响应缺少机器可读字段: %+v", payload)
	}
}

func assertNoImportedSSHKey(t *testing.T, db *gorm.DB, keyName string) {
	t.Helper()
	var count int64
	if err := db.Model(&model.SSHKey{}).Where("name = ?", keyName).Count(&count).Error; err != nil {
		t.Fatalf("统计 SSH key 失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("未授权配置导入不应创建 SSH key %q，实际数量: %d", keyName, count)
	}
}

func assertImportedSSHKey(t *testing.T, db *gorm.DB, keyName string) {
	t.Helper()
	var count int64
	if err := db.Model(&model.SSHKey{}).Where("name = ?", keyName).Count(&count).Error; err != nil {
		t.Fatalf("统计 SSH key 失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("授权配置导入应创建 SSH key %q，实际数量: %d", keyName, count)
	}
}

func TestTerminalCredentialGrantReasonSanitizationRejectsSecretMarkers(t *testing.T) {
	cases := []string{
		"查看输出 output:",
		"连接 https://redacted.invalid",
		"目标 host:",
		"包含敏感占位 [REDACTED]",
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
