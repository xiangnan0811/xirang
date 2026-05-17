package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.TaskRun{}, &model.TaskLog{}, &model.RestoreDrillEvidence{}); err != nil {
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

func TestTaskRunHandlerGetIncludesDrillEvidence(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.TaskRun{}, &model.RestoreDrillEvidence{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{Name: "node-drill-detail", Host: "10.0.0.4", Username: "root", AuthType: "key", BackupDir: "node-drill-detail"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	policyID := uint(99)
	taskEntity := model.Task{Name: "task-drill-detail", NodeID: node.ID, PolicyID: &policyID, ExecutorType: "rsync", Status: "success"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	run := model.TaskRun{TaskID: taskEntity.ID, Status: "failed", TriggerType: "drill", DurationMs: 1234}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("创建执行记录失败: %v", err)
	}
	sourceRunID := uint(41)
	postVerifyFinishedAt := time.Now().UTC()
	evidence := model.RestoreDrillEvidence{
		PolicyID:             policyID,
		TaskID:               taskEntity.ID,
		TaskRunID:            run.ID,
		SourceTaskRunID:      &sourceRunID,
		SnapshotRef:          "task_run:41",
		SandboxNodeID:        node.ID,
		SandboxNodeName:      node.Name,
		SandboxPath:          "/tmp/drill-detail",
		Status:               "failed",
		FailedStep:           "post_verify",
		ConfidenceEligible:   false,
		DurationMs:           1234,
		RestoreStatus:        "success",
		VerifyStatus:         "success",
		PostVerifyStatus:     "failed",
		PostVerifyFinishedAt: &postVerifyFinishedAt,
		PostVerifyError:      "post verify token=***",
		CleanupStatus:        "failed",
		CleanupError:         "cleanup failed",
	}
	if err := db.Create(&evidence).Error; err != nil {
		t.Fatalf("创建演练证据失败: %v", err)
	}

	handler := NewTaskRunHandler(db)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("role", "admin")
		c.Next()
	})
	router.GET("/task-runs/:id", handler.Get)

	req := httptest.NewRequest(http.MethodGet, "/task-runs/1", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，body=%s", resp.Code, resp.Body.String())
	}

	var envelope struct {
		Data struct {
			ID            uint `json:"id"`
			DrillEvidence *struct {
				TaskRunID            uint   `json:"task_run_id"`
				SourceTaskRunID      *uint  `json:"source_task_run_id"`
				SnapshotRef          string `json:"snapshot_ref"`
				Status               string `json:"status"`
				FailedStep           string `json:"failed_step"`
				ConfidenceEligible   bool   `json:"confidence_eligible"`
				PostVerifyStatus     string `json:"post_verify_status"`
				PostVerifyFinishedAt string `json:"post_verify_finished_at"`
				PostVerifyError      string `json:"post_verify_error"`
				CleanupStatus        string `json:"cleanup_status"`
				CleanupError         string `json:"cleanup_error"`
			} `json:"drill_evidence"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if envelope.Data.ID != run.ID {
		t.Fatalf("返回执行记录不符合预期，实际: %+v", envelope.Data)
	}
	if envelope.Data.DrillEvidence == nil {
		t.Fatalf("期望返回 drill_evidence")
	}
	if envelope.Data.DrillEvidence.TaskRunID != run.ID || envelope.Data.DrillEvidence.Status != "failed" {
		t.Fatalf("drill_evidence 基本字段不符合预期: %+v", envelope.Data.DrillEvidence)
	}
	if envelope.Data.DrillEvidence.SourceTaskRunID == nil || *envelope.Data.DrillEvidence.SourceTaskRunID != sourceRunID {
		t.Fatalf("drill_evidence 来源执行记录不符合预期: %+v", envelope.Data.DrillEvidence)
	}
	if envelope.Data.DrillEvidence.SnapshotRef != "task_run:41" {
		t.Fatalf("drill_evidence 快照引用不符合预期: %+v", envelope.Data.DrillEvidence)
	}
	if envelope.Data.DrillEvidence.FailedStep != "post_verify" || envelope.Data.DrillEvidence.PostVerifyStatus != "failed" {
		t.Fatalf("drill_evidence post_verify 阶段字段不符合预期: %+v", envelope.Data.DrillEvidence)
	}
	if envelope.Data.DrillEvidence.PostVerifyFinishedAt == "" || envelope.Data.DrillEvidence.PostVerifyError != "post verify token=***" {
		t.Fatalf("drill_evidence post_verify 详情不符合预期: %+v", envelope.Data.DrillEvidence)
	}
	if envelope.Data.DrillEvidence.CleanupStatus != "failed" || envelope.Data.DrillEvidence.CleanupError != "cleanup failed" {
		t.Fatalf("drill_evidence cleanup 阶段字段不符合预期: %+v", envelope.Data.DrillEvidence)
	}
	if envelope.Data.DrillEvidence.ConfidenceEligible {
		t.Fatalf("失败演练不应作为正向可信证据")
	}
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
