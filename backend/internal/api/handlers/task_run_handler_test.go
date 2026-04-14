package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
)

func TestTaskRunHandlerDeniesOperatorWhenRunTaskWasDeleted(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.Task{}, &model.TaskRun{}, &model.TaskLog{}, &model.NodeOwner{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	user := model.User{Username: "operator-1", PasswordHash: "hash", Role: "operator"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	node := model.Node{Name: "node-a", Host: "10.0.0.1", Username: "root", AuthType: "key", BackupDir: "node-a"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: node.ID, UserID: user.ID}).Error; err != nil {
		t.Fatalf("创建 ownership 失败: %v", err)
	}

	taskEntity := model.Task{Name: "task-a", NodeID: node.ID, ExecutorType: "local", Status: "success"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	run := model.TaskRun{TaskID: taskEntity.ID, Status: "success", TriggerType: "manual"}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("创建执行记录失败: %v", err)
	}

	logEntry := model.TaskLog{TaskID: taskEntity.ID, TaskRunID: &run.ID, Level: "info", Message: "done"}
	if err := db.Create(&logEntry).Error; err != nil {
		t.Fatalf("创建执行日志失败: %v", err)
	}

	if err := db.Delete(&model.Task{}, taskEntity.ID).Error; err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}

	handler := NewTaskRunHandler(db)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("role", "operator")
		c.Set("userID", user.ID)
		c.Next()
	})
	router.GET("/task-runs/:id", handler.Get)
	router.GET("/task-runs/:id/logs", handler.Logs)

	t.Run("get", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/task-runs/1", nil)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusForbidden {
			t.Fatalf("期望状态码 403，实际: %d，body=%s", resp.Code, resp.Body.String())
		}
	})

	t.Run("logs", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/task-runs/1/logs", nil)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusForbidden {
			t.Fatalf("期望状态码 403，实际: %d，body=%s", resp.Code, resp.Body.String())
		}
	})
}

func TestTaskRunHandlerAllowsAdminToReadOrphanedRun(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.TaskRun{}, &model.TaskLog{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{Name: "node-admin", Host: "10.0.0.2", Username: "root", AuthType: "key", BackupDir: "node-admin"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	taskEntity := model.Task{Name: "task-admin", NodeID: node.ID, ExecutorType: "local", Status: "success"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	run := model.TaskRun{TaskID: taskEntity.ID, Status: "success", TriggerType: "manual"}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("创建执行记录失败: %v", err)
	}
	logEntry := model.TaskLog{TaskID: taskEntity.ID, TaskRunID: &run.ID, Level: "info", Message: "done"}
	if err := db.Create(&logEntry).Error; err != nil {
		t.Fatalf("创建执行日志失败: %v", err)
	}

	if err := db.Delete(&model.Task{}, taskEntity.ID).Error; err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}

	handler := NewTaskRunHandler(db)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("role", "admin")
		c.Next()
	})
	router.GET("/task-runs/:id", handler.Get)
	router.GET("/task-runs/:id/logs", handler.Logs)

	t.Run("get", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/task-runs/1", nil)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("期望状态码 200，实际: %d，body=%s", resp.Code, resp.Body.String())
		}

		var envelope struct {
			Data model.TaskRun `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		if envelope.Data.ID != run.ID {
			t.Fatalf("返回执行记录不符合预期，实际: %+v", envelope.Data)
		}
	})

	t.Run("logs", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/task-runs/1/logs", nil)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("期望状态码 200，实际: %d，body=%s", resp.Code, resp.Body.String())
		}
	})
}

func TestTaskRunHandlerDeniesViewerWhenRunTaskWasDeleted(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.TaskRun{}, &model.TaskLog{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{Name: "node-viewer", Host: "10.0.0.3", Username: "root", AuthType: "key", BackupDir: "node-viewer"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	taskEntity := model.Task{Name: "task-viewer", NodeID: node.ID, ExecutorType: "local", Status: "success"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	run := model.TaskRun{TaskID: taskEntity.ID, Status: "success", TriggerType: "manual"}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("创建执行记录失败: %v", err)
	}

	logEntry := model.TaskLog{TaskID: taskEntity.ID, TaskRunID: &run.ID, Level: "info", Message: "done"}
	if err := db.Create(&logEntry).Error; err != nil {
		t.Fatalf("创建执行日志失败: %v", err)
	}

	if err := db.Delete(&model.Task{}, taskEntity.ID).Error; err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}

	handler := NewTaskRunHandler(db)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("role", "viewer")
		c.Next()
	})
	router.GET("/task-runs/:id", handler.Get)
	router.GET("/task-runs/:id/logs", handler.Logs)

	t.Run("get", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/task-runs/1", nil)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusForbidden {
			t.Fatalf("期望状态码 403，实际: %d，body=%s", resp.Code, resp.Body.String())
		}
	})

	t.Run("logs", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/task-runs/1/logs", nil)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusForbidden {
			t.Fatalf("期望状态码 403，实际: %d，body=%s", resp.Code, resp.Body.String())
		}
	})
}
