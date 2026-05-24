package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
)

func assertTaskRunResponseDoesNotLeak(t *testing.T, body string, forbidden []string) {
	t.Helper()
	for _, fragment := range forbidden {
		if strings.Contains(body, fragment) {
			t.Fatalf("响应泄漏原始遗留证据片段 %q: %s", fragment, body)
		}
	}
}

func TestTaskRunHandlerListByTaskSanitizesLegacyLastErrorWithoutMutatingRows(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{Name: "node-legacy-run-list", Host: "10.0.2.1", Username: "root", AuthType: "key", BackupDir: "node-legacy-run-list"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	taskEntity := model.Task{Name: "task-legacy-run-list", NodeID: node.ID, ExecutorType: "rsync", Status: "failed"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	rawLastError := `执行命令: rsync /srv/private/source root@db.internal.example:/backup/tenant-a; output: token=FAKE_TASK_RUN_LIST_TOKEN_FOR_TEST_ONLY failed against https://backup.internal.example/api?token=FAKE_QUERY_TOKEN_FOR_TEST_ONLY`
	run := model.TaskRun{TaskID: taskEntity.ID, Status: "failed", TriggerType: "manual", LastError: rawLastError}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("创建执行记录失败: %v", err)
	}

	handler := NewTaskRunHandler(db)
	router := gin.New()
	router.GET("/tasks/:id/runs", handler.ListByTask)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/tasks/%d/runs", taskEntity.ID), nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，body=%s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	assertTaskRunResponseDoesNotLeak(t, body, []string{"rsync", "/srv/private/source", "db.internal.example", "/backup/tenant-a", "FAKE_TASK_RUN_LIST_TOKEN_FOR_TEST_ONLY", "backup.internal.example", "FAKE_QUERY_TOKEN_FOR_TEST_ONLY"})
	if !strings.Contains(body, "[命令已隐藏]") {
		t.Fatalf("期望响应包含命令脱敏占位，实际: %s", body)
	}

	var stored model.TaskRun
	if err := db.First(&stored, run.ID).Error; err != nil {
		t.Fatalf("重新读取执行记录失败: %v", err)
	}
	if stored.LastError != rawLastError {
		t.Fatalf("读边界脱敏不应改写 DB last_error，实际: %q", stored.LastError)
	}
}

func TestTaskRunHandlerGetSanitizesLegacyLastErrorAndDrillErrorsWithoutMutatingRows(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.TaskRun{}, &model.RestoreDrillEvidence{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{Name: "node-legacy-run-detail", Host: "10.0.2.2", Username: "root", AuthType: "key", BackupDir: "node-legacy-run-detail"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	policyID := uint(101)
	taskEntity := model.Task{Name: "task-legacy-run-detail", NodeID: node.ID, PolicyID: &policyID, ExecutorType: "rsync", Status: "failed"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	rawLastError := `执行命令: curl https://detail.internal.example/hook?token=FAKE_DETAIL_QUERY_TOKEN_FOR_TEST_ONLY && cat /srv/private/detail.sql; stderr: bearer=FAKE_TASK_RUN_DETAIL_TOKEN_FOR_TEST_ONLY from root@detail.internal.example:/backup/detail`
	run := model.TaskRun{TaskID: taskEntity.ID, Status: "failed", TriggerType: "drill", LastError: rawLastError}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("创建执行记录失败: %v", err)
	}
	rawRestoreError := `restore failed output: cp /srv/private/restore.sql root@restore.internal.example:/tmp/restore token=FAKE_RESTORE_TOKEN_FOR_TEST_ONLY`
	rawVerifyError := `verify failed stdout: curl https://verify.internal.example/api?token=FAKE_VERIFY_QUERY_FOR_TEST_ONLY`
	rawPostVerifyError := `post verify failed stderr: psql postgres://user:FAKE_DB_PASSWORD_FOR_TEST_ONLY@db.internal.example:5432/app token=FAKE_POST_VERIFY_TOKEN_FOR_TEST_ONLY`
	rawCleanupError := `cleanup failed output: rm -rf /srv/private/cleanup detail.internal.example secret=FAKE_CLEANUP_SECRET_FOR_TEST_ONLY`
	evidence := model.RestoreDrillEvidence{
		PolicyID:           policyID,
		TaskID:             taskEntity.ID,
		TaskRunID:          run.ID,
		SnapshotRef:        "task_run:legacy",
		SandboxNodeID:      node.ID,
		SandboxNodeName:    node.Name,
		SandboxPath:        "/tmp/xirang-drill",
		Status:             "failed",
		FailedStep:         "verify",
		RestoreStatus:      "failed",
		RestoreError:       rawRestoreError,
		VerifyStatus:       "failed",
		VerifyError:        rawVerifyError,
		PostVerifyStatus:   "failed",
		PostVerifyError:    rawPostVerifyError,
		CleanupStatus:      "failed",
		CleanupError:       rawCleanupError,
		DurationMs:         12,
		ConfidenceEligible: false,
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

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/task-runs/%d", run.ID), nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，body=%s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	assertTaskRunResponseDoesNotLeak(t, body, []string{
		"curl", "cat", "cp", "rm -rf", "/srv/private/detail.sql", "/srv/private/restore.sql", "/srv/private/cleanup", "detail.internal.example", "restore.internal.example", "verify.internal.example", "db.internal.example",
		"FAKE_DETAIL_QUERY_TOKEN_FOR_TEST_ONLY", "FAKE_TASK_RUN_DETAIL_TOKEN_FOR_TEST_ONLY", "FAKE_RESTORE_TOKEN_FOR_TEST_ONLY", "FAKE_VERIFY_QUERY_FOR_TEST_ONLY", "FAKE_DB_PASSWORD_FOR_TEST_ONLY", "FAKE_POST_VERIFY_TOKEN_FOR_TEST_ONLY", "FAKE_CLEANUP_SECRET_FOR_TEST_ONLY",
	})
	for _, expected := range []string{"[命令已隐藏]", "[输出已隐藏]"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("期望响应包含脱敏占位 %q，实际: %s", expected, body)
		}
	}

	var payload struct {
		Data struct {
			ID            uint   `json:"id"`
			Status        string `json:"status"`
			LastError     string `json:"last_error"`
			DrillEvidence *struct {
				TaskRunID        uint   `json:"task_run_id"`
				SnapshotRef      string `json:"snapshot_ref"`
				Status           string `json:"status"`
				FailedStep       string `json:"failed_step"`
				RestoreStatus    string `json:"restore_status"`
				RestoreError     string `json:"restore_error"`
				VerifyStatus     string `json:"verify_status"`
				VerifyError      string `json:"verify_error"`
				PostVerifyStatus string `json:"post_verify_status"`
				PostVerifyError  string `json:"post_verify_error"`
				CleanupStatus    string `json:"cleanup_status"`
				CleanupError     string `json:"cleanup_error"`
			} `json:"drill_evidence"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if payload.Data.ID != run.ID || payload.Data.Status != "failed" || payload.Data.DrillEvidence == nil {
		t.Fatalf("响应结构字段不符合预期: %+v", payload.Data)
	}
	if payload.Data.DrillEvidence.TaskRunID != run.ID || payload.Data.DrillEvidence.SnapshotRef != "task_run:legacy" || payload.Data.DrillEvidence.FailedStep != "verify" {
		t.Fatalf("drill_evidence 结构字段不应被脱敏改写: %+v", payload.Data.DrillEvidence)
	}
	if payload.Data.LastError == rawLastError || payload.Data.DrillEvidence.RestoreError == rawRestoreError || payload.Data.DrillEvidence.VerifyError == rawVerifyError || payload.Data.DrillEvidence.PostVerifyError == rawPostVerifyError || payload.Data.DrillEvidence.CleanupError == rawCleanupError {
		t.Fatalf("响应中的错误证据仍为原始值: %+v", payload.Data)
	}

	var storedRun model.TaskRun
	if err := db.First(&storedRun, run.ID).Error; err != nil {
		t.Fatalf("重新读取执行记录失败: %v", err)
	}
	if storedRun.LastError != rawLastError {
		t.Fatalf("读边界脱敏不应改写 DB task_runs.last_error，实际: %q", storedRun.LastError)
	}
	var storedEvidence model.RestoreDrillEvidence
	if err := db.First(&storedEvidence, evidence.ID).Error; err != nil {
		t.Fatalf("重新读取演练证据失败: %v", err)
	}
	if storedEvidence.RestoreError != rawRestoreError || storedEvidence.VerifyError != rawVerifyError || storedEvidence.PostVerifyError != rawPostVerifyError || storedEvidence.CleanupError != rawCleanupError {
		t.Fatalf("读边界脱敏不应改写 DB drill error fields，实际: %+v", storedEvidence)
	}
}

func TestTaskRunHandlerLogsSanitizesLegacyMessagesWithoutChangingPagination(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.TaskRun{}, &model.TaskLog{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{Name: "node-legacy-run-logs", Host: "10.0.2.3", Username: "root", AuthType: "key", BackupDir: "node-legacy-run-logs"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	taskEntity := model.Task{Name: "task-legacy-run-logs", NodeID: node.ID, ExecutorType: "rsync", Status: "failed"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	run := model.TaskRun{TaskID: taskEntity.ID, Status: "failed", TriggerType: "manual"}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("创建执行记录失败: %v", err)
	}

	runID := run.ID
	rawInfoMessage := `info output: ls /srv/private/info on info.internal.example token=FAKE_INFO_LOG_TOKEN_FOR_TEST_ONLY`
	rawOldErrorMessage := `执行命令: rclone sync /srv/private/old remote:backup/old --password FAKE_OLD_LOG_PASSWORD_FOR_TEST_ONLY`
	rawNewestErrorMessage := `stderr: curl https://logs.internal.example/hook?token=FAKE_LOG_QUERY_TOKEN_FOR_TEST_ONLY /srv/private/new root@logs.internal.example:/backup/new`
	infoLog := model.TaskLog{TaskID: taskEntity.ID, TaskRunID: &runID, Level: "info", Message: rawInfoMessage}
	oldErrorLog := model.TaskLog{TaskID: taskEntity.ID, TaskRunID: &runID, Level: "error", Message: rawOldErrorMessage}
	newestErrorLog := model.TaskLog{TaskID: taskEntity.ID, TaskRunID: &runID, Level: "error", Message: rawNewestErrorMessage}
	for _, logEntry := range []*model.TaskLog{&infoLog, &oldErrorLog, &newestErrorLog} {
		if err := db.Create(logEntry).Error; err != nil {
			t.Fatalf("创建执行日志失败: %v", err)
		}
	}

	handler := NewTaskRunHandler(db)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("role", "admin")
		c.Next()
	})
	router.GET("/task-runs/:id/logs", handler.Logs)

	url := fmt.Sprintf("/task-runs/%d/logs?level=error&before_id=%d&limit=1", run.ID, newestErrorLog.ID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，body=%s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	assertTaskRunResponseDoesNotLeak(t, body, []string{"rclone", "/srv/private/old", "remote:backup/old", "FAKE_OLD_LOG_PASSWORD_FOR_TEST_ONLY", "FAKE_INFO_LOG_TOKEN_FOR_TEST_ONLY", "logs.internal.example", "FAKE_LOG_QUERY_TOKEN_FOR_TEST_ONLY"})
	if !strings.Contains(body, "[命令已隐藏]") {
		t.Fatalf("期望日志响应包含命令脱敏占位，实际: %s", body)
	}

	var payload struct {
		Data []model.TaskLog `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(payload.Data) != 1 {
		t.Fatalf("期望按 level/before_id/limit 返回 1 条日志，实际: %+v", payload.Data)
	}
	got := payload.Data[0]
	if got.ID != oldErrorLog.ID || got.TaskID != taskEntity.ID || got.TaskRunID == nil || *got.TaskRunID != run.ID || got.Level != "error" || got.CreatedAt.IsZero() {
		t.Fatalf("日志分页/过滤/结构字段不符合预期: %+v", got)
	}
	if got.Message == rawOldErrorMessage {
		t.Fatalf("响应日志 message 仍为原始值")
	}

	var storedLogs []model.TaskLog
	if err := db.Order("id asc").Find(&storedLogs).Error; err != nil {
		t.Fatalf("重新读取日志失败: %v", err)
	}
	if len(storedLogs) != 3 || storedLogs[0].Message != rawInfoMessage || storedLogs[1].Message != rawOldErrorMessage || storedLogs[2].Message != rawNewestErrorMessage {
		t.Fatalf("读边界脱敏不应改写 DB task_logs.message，实际: %+v", storedLogs)
	}
}

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
