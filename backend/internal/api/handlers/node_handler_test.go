package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"
	nodePkg "xirang/backend/internal/node"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openNodeHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}

func TestNodeExecDisabled(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.SSHKey{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{
		Name:      "node-exec-empty",
		Host:      "127.0.0.1",
		Port:      22,
		Username:  "root",
		AuthType:  "password",
		Password:  "secret",
		BackupDir: "node-exec-empty",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	r := gin.New()
	handler := NewNodeHandler(db, nil, nodePkg.NewNodeService(db))
	r.POST("/nodes/:id/exec", handler.Exec)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/nodes/%d/exec", node.ID), strings.NewReader(`{"command":"hostname"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("期望状态码 403，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	var envelope Response
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应信封失败: %v", err)
	}
	if envelope.Code != http.StatusForbidden || envelope.Message != "节点远程执行能力已禁用" {
		t.Fatalf("期望标准禁用响应信封，实际: %+v", envelope)
	}
	data, ok := envelope.Data.(map[string]interface{})
	if !ok || data["error_code"] != nodeExecDisabledCode {
		t.Fatalf("期望返回禁用错误码，实际: %+v", envelope.Data)
	}
}

func TestNodeBatchDeleteRejectsEmptyIDs(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.Alert{}, &model.PolicyNode{}, &model.NodeOwner{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	r := gin.New()
	handler := NewNodeHandler(db, nil, nodePkg.NewNodeService(db))
	r.POST("/nodes/batch-delete", handler.BatchDelete)

	req := httptest.NewRequest(http.MethodPost, "/nodes/batch-delete", strings.NewReader(`{"ids":[]}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("期望状态码 400，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "ids 不能为空") {
		t.Fatalf("期望返回 ids 不能为空，实际: %s", resp.Body.String())
	}
}

func TestNodeBatchDeleteSuccess(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.Alert{}, &model.PolicyNode{}, &model.NodeOwner{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	nodeA := model.Node{Name: "node-a", Host: "10.0.0.11", Port: 22, Username: "root", AuthType: "password", Password: "secret", BackupDir: "node-a"}
	nodeB := model.Node{Name: "node-b", Host: "10.0.0.12", Port: 22, Username: "root", AuthType: "password", Password: "secret", BackupDir: "node-b"}
	if err := db.Create(&nodeA).Error; err != nil {
		t.Fatalf("创建节点 A 失败: %v", err)
	}
	if err := db.Create(&nodeB).Error; err != nil {
		t.Fatalf("创建节点 B 失败: %v", err)
	}

	task := model.Task{Name: "task-a", NodeID: nodeA.ID, Status: "failed", ExecutorType: "local"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	alert := model.Alert{
		NodeID:      nodeA.ID,
		NodeName:    nodeA.Name,
		Severity:    "warning",
		Status:      "open",
		ErrorCode:   "XR-TEST-001",
		Message:     "test",
		Retryable:   true,
		TriggeredAt: time.Now(),
	}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}

	r := gin.New()
	handler := NewNodeHandler(db, nil, nodePkg.NewNodeService(db))
	r.POST("/nodes/batch-delete", func(c *gin.Context) {
		c.Set("role", "admin")
		c.Next()
	}, handler.BatchDelete)

	payload := fmt.Sprintf(`{"ids":[%d,%d,999,%d]}`, nodeA.ID, nodeB.ID, nodeA.ID)
	req := httptest.NewRequest(http.MethodPost, "/nodes/batch-delete", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"deleted":2`) {
		t.Fatalf("期望删除 2 个节点，实际: %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"not_found_ids":[999]`) {
		t.Fatalf("期望 not_found_ids 包含 999，实际: %s", resp.Body.String())
	}

	var nodeCount int64
	if err := db.Model(&model.Node{}).Count(&nodeCount).Error; err != nil {
		t.Fatalf("统计节点失败: %v", err)
	}
	if nodeCount != 0 {
		t.Fatalf("期望节点全部删除，剩余: %d", nodeCount)
	}

	var taskCount int64
	if err := db.Model(&model.Task{}).Count(&taskCount).Error; err != nil {
		t.Fatalf("统计任务失败: %v", err)
	}
	if taskCount != 0 {
		t.Fatalf("期望关联任务被删除，剩余: %d", taskCount)
	}

	var alertCount int64
	if err := db.Model(&model.Alert{}).Count(&alertCount).Error; err != nil {
		t.Fatalf("统计告警失败: %v", err)
	}
	if alertCount != 0 {
		t.Fatalf("期望关联告警被删除，剩余: %d", alertCount)
	}
}

func TestNodeMigrateReturnsInternalErrorWhenPolicyLookupFails(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}); err != nil {
		t.Fatalf("初始化节点表失败: %v", err)
	}

	source := model.Node{Name: "source-node", Host: "10.0.0.21", Port: 22, Username: "root", AuthType: "password", Password: "FAKE_NODE_PASSWORD_FOR_TEST_ONLY", BackupDir: "/backup/source"}
	target := model.Node{Name: "target-node", Host: "10.0.0.22", Port: 22, Username: "root", AuthType: "password", Password: "FAKE_NODE_PASSWORD_FOR_TEST_ONLY", BackupDir: "/backup/target"}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("创建源节点失败: %v", err)
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("创建目标节点失败: %v", err)
	}

	r := gin.New()
	handler := NewNodeHandler(db, nil, nodePkg.NewNodeService(db))
	r.POST("/nodes/:id/migrate", func(c *gin.Context) {
		c.Set("role", "admin")
		c.Next()
	}, handler.Migrate)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/nodes/%d/migrate", source.ID), strings.NewReader(fmt.Sprintf(`{"targetNodeId":%d}`, target.ID)))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("期望策略查询失败返回 500，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	var envelope Response
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if envelope.Code != http.StatusInternalServerError {
		t.Fatalf("期望标准错误信封，实际: %+v", envelope)
	}
}

func TestNodeMigratePreflightReturnsInternalErrorWhenPolicyLookupFails(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}); err != nil {
		t.Fatalf("初始化节点表失败: %v", err)
	}

	source := model.Node{Name: "source-node", Host: "10.0.0.21", Port: 22, Username: "root", AuthType: "password", Password: "FAKE_NODE_PASSWORD_FOR_TEST_ONLY", BackupDir: "/backup/source"}
	target := model.Node{Name: "target-node", Host: "10.0.0.22", Port: 22, Username: "root", AuthType: "password", Password: "FAKE_NODE_PASSWORD_FOR_TEST_ONLY", BackupDir: "/backup/target"}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("创建源节点失败: %v", err)
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("创建目标节点失败: %v", err)
	}

	r := gin.New()
	handler := NewNodeHandler(db, nil, nodePkg.NewNodeService(db))
	r.POST("/nodes/:id/migrate-preflight", func(c *gin.Context) {
		c.Set("role", "admin")
		c.Next()
	}, handler.MigratePreflight)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/nodes/%d/migrate-preflight", source.ID), strings.NewReader(fmt.Sprintf(`{"targetNodeId":%d}`, target.ID)))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("预检策略查询失败期望返回 500，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	var envelope Response
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if envelope.Code != http.StatusInternalServerError {
		t.Fatalf("期望标准错误信封，实际: %+v", envelope)
	}
}

func TestMigrationPreflightAuditDoesNotCopyDiagnosticHostOrPath(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	source := model.Node{Name: "source-audit-node", Host: "10.91.0.1", Port: 22, Username: "root", AuthType: "password", Password: "FAKE_SOURCE_PASSWORD_FOR_TEST_ONLY", BackupDir: "source-audit-node", DiskUsedGB: 12}
	target := model.Node{Name: "target-audit-node", Host: "10.91.0.2", Port: 22, Username: "root", AuthType: "password", Password: "", BackupDir: "target-audit-node"}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("创建源节点失败: %v", err)
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("创建目标节点失败: %v", err)
	}
	policy := model.Policy{Name: "preflight-policy", SourcePath: "/very/sensitive/source/path", TargetPath: "/backup/target", Enabled: true}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: policy.ID, NodeID: source.ID}).Error; err != nil {
		t.Fatalf("创建策略节点关联失败: %v", err)
	}
	if err := db.Create(&model.Task{Name: "preflight-task", NodeID: source.ID, PolicyID: &policy.ID, Source: "policy", ExecutorType: "rsync", RsyncSource: "/very/sensitive/source/path", RsyncTarget: "/tmp/not-used", Status: "pending"}).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	r := gin.New()
	handler := NewNodeHandler(db, nil, nodePkg.NewNodeService(db))
	r.POST("/nodes/:id/migrate-preflight", func(c *gin.Context) {
		c.Set("userID", uint(101))
		c.Set("username", "alice")
		c.Set("role", "admin")
		handler.MigratePreflight(c)
	})

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/nodes/%d/migrate-preflight", source.ID), strings.NewReader(fmt.Sprintf(`{"targetNodeId":%d}`, target.ID)))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("预检认证失败仍应返回结构化 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var event model.CredentialAuditEvent
	if err := db.Where("action = ?", "node_migration.preflight").First(&event).Error; err != nil {
		t.Fatalf("应写入 node_migration.preflight 审计事件: %v", err)
	}
	if event.Purpose != sshutil.PurposeNodeMigration || event.Outcome != credentialaudit.OutcomeBlocked || event.NodeID == nil || *event.NodeID != target.ID {
		t.Fatalf("migration preflight audit event 不符合预期: %+v", event)
	}
	for _, forbidden := range []string{"10.91.0.", "target-audit-node", "source-audit-node", "/very/sensitive/source/path", "FAKE_", "SSH 连接目标节点失败"} {
		if strings.Contains(event.Metadata, forbidden) || strings.Contains(event.ErrorMessage, forbidden) {
			t.Fatalf("preflight audit 不应复制诊断主机/路径/证据 %q: metadata=%s error=%s", forbidden, event.Metadata, event.ErrorMessage)
		}
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
		t.Fatalf("metadata json: %v", err)
	}
	if metadata["source_node_id"] == nil || metadata["target_node_id"] == nil || metadata["policy_count"] == nil || metadata["check_count"] == nil || metadata["failure_count"] == nil || metadata["stage"] != "complete" {
		t.Fatalf("preflight audit metadata 缺少安全字段: %#v", metadata)
	}
}

func TestMigrationPreflightResponseSanitizesDiagnosticFields(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	source := model.Node{Name: "source-response-node", Host: "source-db.example.internal", Port: 22, Username: "root", AuthType: "password", Password: "FAKE_SOURCE_PASSWORD_FOR_TEST_ONLY", BackupDir: "source-response-node", DiskUsedGB: 12}
	target := model.Node{Name: "target-response-node", Host: "10.92.0.2", Port: 22, Username: "root", AuthType: "password", Password: "", BackupDir: "target-response-node"}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("创建源节点失败: %v", err)
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("创建目标节点失败: %v", err)
	}
	policy := model.Policy{Name: "response-policy", SourcePath: "/sensitive/include/source", TargetPath: "/backup/target", Enabled: true}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: policy.ID, NodeID: source.ID}).Error; err != nil {
		t.Fatalf("创建策略节点关联失败: %v", err)
	}
	if err := db.Create(&model.Task{Name: "response-task", NodeID: source.ID, PolicyID: &policy.ID, Source: "policy", ExecutorType: "rsync", RsyncSource: "/sensitive/include/source", RsyncTarget: "/tmp/not-used", Status: "pending"}).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	r := gin.New()
	handler := NewNodeHandler(db, nil, nodePkg.NewNodeService(db))
	r.POST("/nodes/:id/migrate-preflight", func(c *gin.Context) {
		c.Set("userID", uint(101))
		c.Set("username", "alice")
		c.Set("role", "admin")
		handler.MigratePreflight(c)
	})

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/nodes/%d/migrate-preflight", source.ID), strings.NewReader(fmt.Sprintf(`{"targetNodeId":%d}`, target.ID)))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("预检认证失败仍应返回结构化 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var envelope struct {
		Code int                      `json:"code"`
		Data MigratePreflightResponse `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if envelope.Code != http.StatusOK {
		t.Fatalf("响应信封不符合预期: %+v", envelope)
	}
	if envelope.Data.SourceNode.Host != "[HOST_REDACTED]" || envelope.Data.TargetNode.Host != "[HOST_REDACTED]" {
		t.Fatalf("host 字段应保留但脱敏: %+v", envelope.Data)
	}
	if len(envelope.Data.Policies) != 1 || envelope.Data.Policies[0].SourcePath != "[PATH_REDACTED]" {
		t.Fatalf("policy sourcePath 字段应保留但脱敏: %+v", envelope.Data.Policies)
	}
	body := resp.Body.String()
	for _, forbidden := range []string{"source-db.example.internal", "10.92.0.2", "/sensitive/include/source", "FAKE_SOURCE_PASSWORD_FOR_TEST_ONLY", "no supported methods"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("preflight 响应泄漏 %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, "SSH 连接目标节点失败") || !strings.Contains(body, "SSH 认证失败") {
		t.Fatalf("preflight 响应应保留有用的通用失败分类: %s", body)
	}
}

func TestPreflightAuditOutcomeClassifiesSSHFailureAsBlocked(t *testing.T) {
	checks := []PreflightCheckItem{
		{Name: "ssh", Status: "fail", Message: "SSH 连接目标节点失败: FAKE_PASSWORD_FOR_TEST_ONLY"},
		{Name: "disk", Status: "skip"},
	}
	if got := preflightAuditOutcome(checks); got != credentialaudit.OutcomeBlocked {
		t.Fatalf("ssh failure 应归类为 blocked，got %s", got)
	}
}

func TestParseDiskProbeAcceptsFullUsage(t *testing.T) {
	used, total, ok := sshutil.ParseDiskProbe("100G 100G")
	if !ok {
		t.Fatalf("期望 100%% 磁盘占用可被解析")
	}
	if used != 100 || total != 100 {
		t.Fatalf("解析结果不符合预期，used=%d total=%d", used, total)
	}
}
