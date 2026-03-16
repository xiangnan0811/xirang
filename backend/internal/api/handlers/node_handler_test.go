package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openNodeHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
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
		Name:     "node-exec-empty",
		Host:     "127.0.0.1",
		Port:     22,
		Username: "root",
		AuthType: "password",
		Password: "secret",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	r := gin.New()
	handler := NewNodeHandler(db)
	r.POST("/nodes/:id/exec", handler.Exec)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/nodes/%d/exec", node.ID), strings.NewReader(`{"command":"hostname"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("期望状态码 403，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "XR-SEC-EXEC-DISABLED") {
		t.Fatalf("期望返回禁用错误码，实际: %s", resp.Body.String())
	}
}

func TestNodeBatchDeleteRejectsEmptyIDs(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.Alert{}, &model.PolicyNode{}, &model.NodeOwner{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	r := gin.New()
	handler := NewNodeHandler(db)
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

	nodeA := model.Node{Name: "node-a", Host: "10.0.0.11", Port: 22, Username: "root", AuthType: "password", Password: "secret"}
	nodeB := model.Node{Name: "node-b", Host: "10.0.0.12", Port: 22, Username: "root", AuthType: "password", Password: "secret"}
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
	handler := NewNodeHandler(db)
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

func TestParseDiskProbeAcceptsFullUsage(t *testing.T) {
	used, total, ok := sshutil.ParseDiskProbe("100G 100G")
	if !ok {
		t.Fatalf("期望 100%% 磁盘占用可被解析")
	}
	if used != 100 || total != 100 {
		t.Fatalf("解析结果不符合预期，used=%d total=%d", used, total)
	}
}
