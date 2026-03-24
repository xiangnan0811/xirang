package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

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
