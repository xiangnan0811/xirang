package handlers

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
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
)

func TestBatchCreateRejectsUnownedNodeForOperator(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.NodeOwner{}, &model.Task{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	operator := model.User{Username: "operator", Role: "operator", PasswordHash: "hashed"}
	if err := db.Create(&operator).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	ownedNode := model.Node{Name: "node-owned", Host: "10.0.0.1", Username: "root", AuthType: "key", BackupDir: "node-owned"}
	unownedNode := model.Node{Name: "node-unowned", Host: "10.0.0.2", Username: "root", AuthType: "key", BackupDir: "node-unowned"}
	if err := db.Create(&ownedNode).Error; err != nil {
		t.Fatalf("创建 owned 节点失败: %v", err)
	}
	if err := db.Create(&unownedNode).Error; err != nil {
		t.Fatalf("创建 unowned 节点失败: %v", err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: ownedNode.ID, UserID: operator.ID}).Error; err != nil {
		t.Fatalf("创建 ownership 失败: %v", err)
	}

	handler := NewBatchHandler(db, &mockTaskRunner{})
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "operator")
		c.Set(middleware.CtxUserID, operator.ID)
		c.Next()
	})
	r.POST("/batch-commands", handler.Create)

	body := fmt.Sprintf(`{"node_ids":[%d,%d],"command":"echo hello","name":"batch-demo"}`, ownedNode.ID, unownedNode.ID)
	req := httptest.NewRequest(http.MethodPost, "/batch-commands", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("operator 批量命令包含未授权节点时期望状态码 403，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var count int64
	if err := db.Model(&model.Task{}).Count(&count).Error; err != nil {
		t.Fatalf("统计任务失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("未授权节点不应创建批量任务，实际数量: %d", count)
	}
}

func TestBatchCreateMissingGrantDoesNotDecryptInlineCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "batch-create-grant-admin", "admin")
	token := generatePrimaryToken(t, manager, admin)
	proof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionBatchCommandCreate)
	node := model.Node{
		Name:      "batch-create-gated-node",
		Host:      "redacted",
		Port:      22,
		Username:  "root",
		AuthType:  "password",
		Password:  encryptBatchHandlerTestCiphertext(t, "ENCRYPTED_INLINE_FIXTURE_VALUE"),
		BackupDir: "batch-create-gated-node",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	handler := NewBatchHandler(db, &mockTaskRunner{}).WithJWTManager(manager)
	r := gin.New()
	r.Use(middleware.AuthMiddleware(manager, db))
	r.POST("/batch-commands", handler.Create)

	body := fmt.Sprintf(`{"node_ids":[%d],"command":"echo ok","name":"batch-gated"}`, node.ID)
	resp := performStepUpRequest(t, r, http.MethodPost, "/batch-commands", token, proof, body)
	assertCredentialGrantRequiredEnvelope(t, resp, "required")

	var count int64
	if err := db.Model(&model.Task{}).Count(&count).Error; err != nil {
		t.Fatalf("统计任务失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("缺少 batch command grant 时不应创建任务，实际数量: %d", count)
	}
}

func TestBatchCreateRequiresAllNodeGrantsBeforeCreatingTasks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openStepUpHandlerTestDB(t)
	manager := auth.NewJWTManager(stepUpTestJWTSecret, time.Hour)
	admin := seedStepUpUser(t, db, "batch-create-all-grants-admin", "admin")
	token := generatePrimaryToken(t, manager, admin)
	proof := generateStepUpProofForAction(t, manager, admin, auth.StepUpActionBatchCommandCreate)
	nodeA := model.Node{Name: "batch-grant-node-a", Host: "redacted-a", Port: 22, Username: "root", AuthType: "key", BackupDir: "batch-grant-node-a"}
	nodeB := model.Node{Name: "batch-grant-node-b", Host: "redacted-b", Port: 22, Username: "root", AuthType: "key", BackupDir: "batch-grant-node-b"}
	if err := db.Create(&nodeA).Error; err != nil {
		t.Fatalf("创建 nodeA 失败: %v", err)
	}
	if err := db.Create(&nodeB).Error; err != nil {
		t.Fatalf("创建 nodeB 失败: %v", err)
	}

	createTaskRestoreGrantFixture(t, db, admin, CredentialGrantActionBatchCommand, sshutil.PurposeBatchCommand, CredentialGrantStatusActive, nil, credentialaudit.PtrUint(nodeA.ID), nil, "admin")
	handler := NewBatchHandler(db, &mockTaskRunner{}).WithJWTManager(manager)
	r := gin.New()
	r.Use(middleware.AuthMiddleware(manager, db))
	r.POST("/batch-commands", handler.Create)

	body := fmt.Sprintf(`{"node_ids":[%d,%d],"command":"echo ok","name":"batch-all-or-nothing"}`, nodeA.ID, nodeB.ID)
	missingResp := performStepUpRequest(t, r, http.MethodPost, "/batch-commands", token, proof, body)
	assertCredentialGrantRequiredEnvelope(t, missingResp, "required")
	var count int64
	if err := db.Model(&model.Task{}).Count(&count).Error; err != nil {
		t.Fatalf("统计任务失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("缺少部分 batch command grant 时不应部分创建任务，实际数量: %d", count)
	}

	createTaskRestoreGrantFixture(t, db, admin, CredentialGrantActionBatchCommand, sshutil.PurposeBatchCommand, CredentialGrantStatusActive, nil, credentialaudit.PtrUint(nodeB.ID), nil, "admin")
	grantedResp := performStepUpRequest(t, r, http.MethodPost, "/batch-commands", token, proof, body)
	if grantedResp.Code != http.StatusOK {
		t.Fatalf("全部 batch command grant 存在时应创建批量任务，实际: %d，响应: %s", grantedResp.Code, grantedResp.Body.String())
	}
	if err := db.Model(&model.Task{}).Count(&count).Error; err != nil {
		t.Fatalf("统计任务失败: %v", err)
	}
	if count != 2 {
		t.Fatalf("全部授权后应创建两个任务，实际数量: %d", count)
	}
}

func encryptBatchHandlerTestCiphertext(t *testing.T, plain string) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("生成测试加密 key 失败: %v", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("创建测试 cipher 失败: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("创建测试 GCM 失败: %v", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("生成测试 nonce 失败: %v", err)
	}
	packed := append(nonce, gcm.Seal(nil, nonce, []byte(plain), nil)...)
	return "enc:v2:" + base64.StdEncoding.EncodeToString(packed)
}

func TestBatchGetRedactsExecutorConfig(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{Name: "node-batch-redact", Host: "10.0.0.3", Username: "root", AuthType: "key", BackupDir: "node-batch-redact"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	if err := db.Create(&model.Task{
		Name:           "batch-redact-task",
		NodeID:         node.ID,
		ExecutorType:   "restic",
		RsyncSource:    "/data/src",
		RsyncTarget:    "/backup/repo",
		ExecutorConfig: `{"repository_password":"FAKE_BATCH_RESTIC_PASSWORD_FOR_TEST_ONLY"}`,
		Status:         "pending",
		BatchID:        "batch-redact",
		Source:         "batch",
	}).Error; err != nil {
		t.Fatalf("创建批量任务失败: %v", err)
	}

	handler := NewBatchHandler(db, &mockTaskRunner{})
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "admin")
		c.Next()
	})
	r.GET("/batch-commands/:batch_id", handler.Get)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/batch-commands/batch-redact", nil)
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("batch get status=%d body=%s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	if strings.Contains(body, "executor_config") || strings.Contains(body, "FAKE_BATCH_RESTIC_PASSWORD_FOR_TEST_ONLY") {
		t.Fatalf("批次详情不应暴露 executor_config 或密码，实际: %s", body)
	}
}

func TestBatchGetRejectsUnownedBatchForOperator(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.NodeOwner{}, &model.Task{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	operator := model.User{Username: "operator", Role: "operator", PasswordHash: "hashed"}
	if err := db.Create(&operator).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	unownedNode := model.Node{Name: "node-unowned", Host: "10.0.0.2", Username: "root", AuthType: "key", BackupDir: "node-unowned"}
	if err := db.Create(&unownedNode).Error; err != nil {
		t.Fatalf("创建 unowned 节点失败: %v", err)
	}

	if err := db.Create(&model.Task{
		Name:         "batch-task",
		NodeID:       unownedNode.ID,
		ExecutorType: "command",
		Command:      "echo hello",
		Status:       "pending",
		BatchID:      "batch-denied",
		Source:       "batch",
	}).Error; err != nil {
		t.Fatalf("创建批量任务失败: %v", err)
	}

	handler := NewBatchHandler(db, &mockTaskRunner{})
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "operator")
		c.Set(middleware.CtxUserID, operator.ID)
		c.Next()
	})
	r.GET("/batch-commands/:batch_id", handler.Get)

	req := httptest.NewRequest(http.MethodGet, "/batch-commands/batch-denied", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("operator 查看未授权批次期望状态码 403，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
}

func TestBatchDeleteRejectsUnownedBatchForOperator(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.NodeOwner{}, &model.Task{}, &model.TaskLog{}, &model.TaskRun{}, &model.TaskTrafficSample{}, &model.Alert{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	operator := model.User{Username: "operator", Role: "operator", PasswordHash: "hashed"}
	if err := db.Create(&operator).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	unownedNode := model.Node{Name: "node-unowned", Host: "10.0.0.2", Username: "root", AuthType: "key", BackupDir: "node-unowned"}
	if err := db.Create(&unownedNode).Error; err != nil {
		t.Fatalf("创建 unowned 节点失败: %v", err)
	}

	taskEntity := model.Task{
		Name:         "batch-task",
		NodeID:       unownedNode.ID,
		ExecutorType: "command",
		Command:      "echo hello",
		Status:       "pending",
		BatchID:      "batch-denied",
		Source:       "batch",
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建批量任务失败: %v", err)
	}

	handler := NewBatchHandler(db, &mockTaskRunner{})
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "operator")
		c.Set(middleware.CtxUserID, operator.ID)
		c.Next()
	})
	r.DELETE("/batch-commands/:batch_id", handler.Delete)

	req := httptest.NewRequest(http.MethodDelete, "/batch-commands/batch-denied", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("operator 删除未授权批次期望状态码 403，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var count int64
	if err := db.Model(&model.Task{}).Where("batch_id = ?", "batch-denied").Count(&count).Error; err != nil {
		t.Fatalf("统计批量任务失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("未授权删除不应移除批次任务，实际剩余数量: %d", count)
	}
}

func TestBatchDeleteReturnsInternalErrorWhenCleanupFails(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.NodeOwner{}, &model.Task{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{Name: "node-owned", Host: "10.0.0.1", Username: "root", AuthType: "key", BackupDir: "node-owned"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	taskEntity := model.Task{
		Name:         "batch-task",
		NodeID:       node.ID,
		ExecutorType: "command",
		Command:      "echo hello",
		Status:       "pending",
		BatchID:      "batch-cleanup-fail",
		Source:       "batch",
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建批量任务失败: %v", err)
	}

	handler := NewBatchHandler(db, &mockTaskRunner{})
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "admin")
		c.Next()
	})
	r.DELETE("/batch-commands/:batch_id", handler.Delete)

	req := httptest.NewRequest(http.MethodDelete, "/batch-commands/batch-cleanup-fail", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("关联表缺失时期望 500，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	var count int64
	if err := db.Model(&model.Task{}).Where("batch_id = ?", "batch-cleanup-fail").Count(&count).Error; err != nil {
		t.Fatalf("统计批量任务失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("清理失败时事务应回滚保留任务，实际剩余数量: %d", count)
	}
}
