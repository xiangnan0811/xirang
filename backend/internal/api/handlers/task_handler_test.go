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

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	taskPkg "xirang/backend/internal/task"
	"xirang/backend/internal/task/scheduler"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type mockTaskRunner struct {
	triggerManualRunID uint
	syncErrs           []error
	syncCalls          []model.Task
	removeCalls        []uint
}

func (m *mockTaskRunner) TriggerManual(taskID uint) (uint, error) {
	if m.triggerManualRunID != 0 {
		return m.triggerManualRunID, nil
	}
	return 0, nil
}

func (m *mockTaskRunner) TriggerRestore(taskID uint, targetPath string) (uint, error) {
	return 0, nil
}

func (m *mockTaskRunner) SyncSchedule(task model.Task) error {
	m.syncCalls = append(m.syncCalls, task)
	callIndex := len(m.syncCalls) - 1
	if callIndex < len(m.syncErrs) && m.syncErrs[callIndex] != nil {
		return m.syncErrs[callIndex]
	}
	return nil
}

func (m *mockTaskRunner) RemoveSchedule(taskID uint) {
	m.removeCalls = append(m.removeCalls, taskID)
}

func (m *mockTaskRunner) Cancel(taskID uint) error {
	return nil
}

func (m *mockTaskRunner) Pause(taskID uint, cancelRunning bool) error {
	return nil
}

func (m *mockTaskRunner) Resume(taskID uint) error {
	return nil
}

func (m *mockTaskRunner) SetSkipNext(taskID uint) error {
	return nil
}

func TestTaskTriggerReturnsAcceptedEnvelope(t *testing.T) {
	runner := &mockTaskRunner{triggerManualRunID: 42}
	handler := NewTaskHandler(openTaskHandlerTestDB(t), runner)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/tasks/:id/trigger", handler.Trigger)

	req := httptest.NewRequest(http.MethodPost, "/tasks/7/trigger", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("期望状态码 202，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Message string `json:"message"`
			RunID   uint   `json:"run_id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if envelope.Code != http.StatusAccepted || envelope.Data.Message != "triggered" || envelope.Data.RunID != 42 {
		t.Fatalf("期望标准 202 信封响应，实际: %+v", envelope)
	}
}

func TestTaskListFilterPaginationSort(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.NodeOwner{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node1 := model.Node{Name: "node-a", Host: "10.0.0.1", Username: "root", AuthType: "key", BackupDir: "node-a"}
	node2 := model.Node{Name: "node-b", Host: "10.0.0.2", Username: "root", AuthType: "key", BackupDir: "node-b"}
	if err := db.Create(&node1).Error; err != nil {
		t.Fatalf("创建 node1 失败: %v", err)
	}
	if err := db.Create(&node2).Error; err != nil {
		t.Fatalf("创建 node2 失败: %v", err)
	}

	policy := model.Policy{Name: "policy-a", SourcePath: "/src", TargetPath: "/dst", CronSpec: "*/5 * * * *"}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	policyID := policy.ID
	task1 := model.Task{Name: "alpha backup", NodeID: node1.ID, PolicyID: &policyID, Command: "echo alpha", ExecutorType: "local", Status: "pending"}
	task2 := model.Task{Name: "beta backup", NodeID: node1.ID, Command: "echo beta", ExecutorType: "local", Status: "running"}
	task3 := model.Task{Name: "gamma sync", NodeID: node2.ID, PolicyID: &policyID, Command: "rsync gamma", ExecutorType: "rsync", Status: "pending"}
	if err := db.Create(&task1).Error; err != nil {
		t.Fatalf("创建 task1 失败: %v", err)
	}
	if err := db.Create(&task2).Error; err != nil {
		t.Fatalf("创建 task2 失败: %v", err)
	}
	if err := db.Create(&task3).Error; err != nil {
		t.Fatalf("创建 task3 失败: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewTaskHandler(db, nil)
	r.GET("/tasks", handler.List)

	url := fmt.Sprintf("/tasks?status=pending&node_id=%d&policy_id=%d&keyword=alpha&limit=1&offset=0&sort=-id", node1.ID, policy.ID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}

	var result struct {
		Data []model.Task `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(result.Data) != 1 || result.Data[0].ID != task1.ID {
		t.Fatalf("筛选结果不符合预期，实际: %+v", result.Data)
	}

	// 使用 page/page_size 分页：第 2 页，每页 2 条，sort=-id → [task1]
	req = httptest.NewRequest(http.MethodGet, "/tasks?sort=-id&page_size=2&page=2", nil)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("分页请求期望状态码 200，实际: %d", resp.Code)
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析分页响应失败: %v", err)
	}
	if len(result.Data) != 1 {
		t.Fatalf("分页结果数量错误，期望 1，实际 %d", len(result.Data))
	}
	if result.Data[0].ID != task1.ID {
		t.Fatalf("排序或偏移不符合预期，实际 id: %d, 期望: %d", result.Data[0].ID, task1.ID)
	}
}

func TestTaskListSanitizesLegacyLastErrorAndPolicyHooksWithoutMutatingRows(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.TaskRun{}, &model.NodeOwner{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{Name: "node-task-list-boundary", Host: "10.0.3.1", Username: "root", AuthType: "key", BackupDir: "node-task-list-boundary", Password: "FAKE_NODE_LIST_PASSWORD_FOR_TEST_ONLY", PrivateKey: "FAKE_NODE_LIST_PRIVATE_KEY_FOR_TEST_ONLY"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	rawPreHook := `mysqldump -h db.internal.example -u app -p'FAKE_POLICY_LIST_PASSWORD_FOR_TEST_ONLY' --all-databases > /tmp/xirang-mysql-backup.sql`
	rawPostHook := `curl https://hooks.internal.example/notify?token=FAKE_POLICY_LIST_HOOK_TOKEN_FOR_TEST_ONLY /tmp/xirang-mysql-backup.sql`
	policy := model.Policy{
		Name:       "policy-task-list-boundary",
		SourcePath: "/data/policy-list-source",
		TargetPath: "/backup/policy-list-target",
		CronSpec:   "*/5 * * * *",
		PreHook:    rawPreHook,
		PostHook:   rawPostHook,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	rawLastError := `执行命令: rsync /srv/private/source root@task-list.internal.example:/backup/private/target; stderr: token=FAKE_TASK_LIST_LAST_ERROR_TOKEN_FOR_TEST_ONLY from https://task-list.internal.example/api?token=FAKE_TASK_LIST_QUERY_TOKEN_FOR_TEST_ONLY`
	policyID := policy.ID
	taskEntity := model.Task{
		Name:         "task-list-boundary",
		NodeID:       node.ID,
		PolicyID:     &policyID,
		ExecutorType: "rsync",
		RsyncSource:  "/data/task-list-source",
		RsyncTarget:  "/backup/task-list-target",
		Status:       "running",
		LastError:    rawLastError,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	runningRun := model.TaskRun{TaskID: taskEntity.ID, Status: "running", TriggerType: "manual", Progress: 67}
	if err := db.Create(&runningRun).Error; err != nil {
		t.Fatalf("创建运行记录失败: %v", err)
	}

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "admin")
		c.Next()
	})
	handler := NewTaskHandler(db, nil)
	router.GET("/tasks", handler.List)

	req := httptest.NewRequest(http.MethodGet, "/tasks?status=running&page_size=10&page=1", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，body=%s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	assertTaskReadResponseDoesNotLeak(t, body, []string{
		"rsync /srv/private/source", "/srv/private/source", "root@task-list.internal.example", "task-list.internal.example", "/backup/private/target", "FAKE_TASK_LIST_LAST_ERROR_TOKEN_FOR_TEST_ONLY", "FAKE_TASK_LIST_QUERY_TOKEN_FOR_TEST_ONLY",
		"mysqldump", "db.internal.example", "FAKE_POLICY_LIST_PASSWORD_FOR_TEST_ONLY", "FAKE_POLICY_LIST_HOOK_TOKEN_FOR_TEST_ONLY", "hooks.internal.example", "/tmp/xirang-mysql-backup.sql",
		"FAKE_NODE_LIST_PASSWORD_FOR_TEST_ONLY", "FAKE_NODE_LIST_PRIVATE_KEY_FOR_TEST_ONLY",
	})
	for _, expected := range []string{"[命令已隐藏]", "\"pre_hook\":\"\"", "\"post_hook\":\"\""} {
		if !strings.Contains(body, expected) {
			t.Fatalf("期望任务列表响应包含脱敏结果 %q，实际: %s", expected, body)
		}
	}

	var payload struct {
		Data  []model.Task `json:"data"`
		Total int64        `json:"total"`
		Page  int          `json:"page"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if payload.Total != 1 || payload.Page != 1 || len(payload.Data) != 1 {
		t.Fatalf("分页响应不符合预期: %+v", payload)
	}
	got := payload.Data[0]
	if got.ID != taskEntity.ID || got.Policy == nil || got.Policy.ID != policy.ID || got.Policy.Name != policy.Name || got.Progress == nil || *got.Progress != runningRun.Progress {
		t.Fatalf("任务列表响应应保留任务结构、策略关联和进度: %+v", got)
	}
	if got.LastError == rawLastError || !strings.Contains(got.LastError, "[命令已隐藏]") {
		t.Fatalf("任务列表 last_error 未按读边界脱敏: %q", got.LastError)
	}
	if got.Policy.PreHook != "" || got.Policy.PostHook != "" {
		t.Fatalf("任务列表嵌套 policy hook 应清空，实际: pre=%q post=%q", got.Policy.PreHook, got.Policy.PostHook)
	}
	if got.Node.Password != "" || got.Node.PrivateKey != "" {
		t.Fatalf("任务列表响应应保留节点脱敏行为，实际: %+v", got.Node)
	}

	var storedTask model.Task
	if err := db.First(&storedTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("重新读取任务失败: %v", err)
	}
	if storedTask.LastError != rawLastError {
		t.Fatalf("读边界脱敏不应改写 DB tasks.last_error，实际: %q", storedTask.LastError)
	}
	var storedPolicy model.Policy
	if err := db.First(&storedPolicy, policy.ID).Error; err != nil {
		t.Fatalf("重新读取策略失败: %v", err)
	}
	if storedPolicy.PreHook != rawPreHook || storedPolicy.PostHook != rawPostHook {
		t.Fatalf("读边界脱敏不应改写 DB policy hooks，实际: pre=%q post=%q", storedPolicy.PreHook, storedPolicy.PostHook)
	}
}

func TestTaskGetSanitizesLegacyLastErrorAndPolicyHooksWithoutMutatingRows(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{Name: "node-task-detail-boundary", Host: "10.0.3.2", Username: "root", AuthType: "key", BackupDir: "node-task-detail-boundary", Password: "FAKE_NODE_DETAIL_PASSWORD_FOR_TEST_ONLY", PrivateKey: "FAKE_NODE_DETAIL_PRIVATE_KEY_FOR_TEST_ONLY"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	rawPreHook := `PGPASSWORD='FAKE_POLICY_DETAIL_PASSWORD_FOR_TEST_ONLY' pg_dumpall -h pg.internal.example > /tmp/xirang-pg-backup.sql`
	rawPostHook := `redis-cli -h redis.internal.example -a 'FAKE_POLICY_DETAIL_REDIS_PASSWORD_FOR_TEST_ONLY' BGSAVE`
	policy := model.Policy{
		Name:       "policy-task-detail-boundary",
		SourcePath: "/data/policy-detail-source",
		TargetPath: "/backup/policy-detail-target",
		CronSpec:   "*/10 * * * *",
		PreHook:    rawPreHook,
		PostHook:   rawPostHook,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	rawLastError := `output: curl https://task-detail.internal.example/hook?token=FAKE_TASK_DETAIL_QUERY_TOKEN_FOR_TEST_ONLY && cat /var/lib/postgresql/private.sql; stderr: bearer=FAKE_TASK_DETAIL_LAST_ERROR_TOKEN_FOR_TEST_ONLY root@task-detail.internal.example:/backup/postgresql`
	policyID := policy.ID
	taskEntity := model.Task{
		Name:         "task-detail-boundary",
		NodeID:       node.ID,
		PolicyID:     &policyID,
		ExecutorType: "restic",
		RsyncSource:  "/data/task-detail-source",
		RsyncTarget:  "/backup/task-detail-target",
		Status:       "running",
		LastError:    rawLastError,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	runningRun := model.TaskRun{TaskID: taskEntity.ID, Status: "running", TriggerType: "manual", Progress: 42}
	if err := db.Create(&runningRun).Error; err != nil {
		t.Fatalf("创建运行记录失败: %v", err)
	}

	router := gin.New()
	handler := NewTaskHandler(db, nil)
	router.GET("/tasks/:id", handler.Get)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/tasks/%d", taskEntity.ID), nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，body=%s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	assertTaskReadResponseDoesNotLeak(t, body, []string{
		"curl https://task-detail.internal.example", "cat /var/lib/postgresql/private.sql", "/var/lib/postgresql/private.sql", "root@task-detail.internal.example", "task-detail.internal.example", "/backup/postgresql", "FAKE_TASK_DETAIL_QUERY_TOKEN_FOR_TEST_ONLY", "FAKE_TASK_DETAIL_LAST_ERROR_TOKEN_FOR_TEST_ONLY",
		"PGPASSWORD", "pg_dumpall", "pg.internal.example", "FAKE_POLICY_DETAIL_PASSWORD_FOR_TEST_ONLY", "redis-cli", "redis.internal.example", "FAKE_POLICY_DETAIL_REDIS_PASSWORD_FOR_TEST_ONLY", "/tmp/xirang-pg-backup.sql",
		"FAKE_NODE_DETAIL_PASSWORD_FOR_TEST_ONLY", "FAKE_NODE_DETAIL_PRIVATE_KEY_FOR_TEST_ONLY",
	})
	for _, expected := range []string{"[输出已隐藏]", "\"pre_hook\":\"\"", "\"post_hook\":\"\""} {
		if !strings.Contains(body, expected) {
			t.Fatalf("期望任务详情响应包含脱敏结果 %q，实际: %s", expected, body)
		}
	}

	var payload struct {
		Data model.Task `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	got := payload.Data
	if got.ID != taskEntity.ID || got.Policy == nil || got.Policy.ID != policy.ID || got.Policy.Name != policy.Name || got.Progress == nil || *got.Progress != runningRun.Progress {
		t.Fatalf("任务详情响应应保留任务结构、策略关联和进度: %+v", got)
	}
	if got.LastError == rawLastError || !strings.Contains(got.LastError, "[输出已隐藏]") {
		t.Fatalf("任务详情 last_error 未按读边界脱敏: %q", got.LastError)
	}
	if got.Policy.PreHook != "" || got.Policy.PostHook != "" {
		t.Fatalf("任务详情嵌套 policy hook 应清空，实际: pre=%q post=%q", got.Policy.PreHook, got.Policy.PostHook)
	}
	if got.Node.Password != "" || got.Node.PrivateKey != "" {
		t.Fatalf("任务详情响应应保留节点脱敏行为，实际: %+v", got.Node)
	}

	var storedTask model.Task
	if err := db.First(&storedTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("重新读取任务失败: %v", err)
	}
	if storedTask.LastError != rawLastError {
		t.Fatalf("读边界脱敏不应改写 DB tasks.last_error，实际: %q", storedTask.LastError)
	}
	var storedPolicy model.Policy
	if err := db.First(&storedPolicy, policy.ID).Error; err != nil {
		t.Fatalf("重新读取策略失败: %v", err)
	}
	if storedPolicy.PreHook != rawPreHook || storedPolicy.PostHook != rawPostHook {
		t.Fatalf("读边界脱敏不应改写 DB policy hooks，实际: pre=%q post=%q", storedPolicy.PreHook, storedPolicy.PostHook)
	}
}

func assertTaskReadResponseDoesNotLeak(t *testing.T, body string, forbidden []string) {
	t.Helper()
	for _, fragment := range forbidden {
		if strings.Contains(body, fragment) {
			t.Fatalf("任务读响应泄漏原始敏感片段 %q: %s", fragment, body)
		}
	}
}

func TestTaskResponsesRedactExecutorConfig(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	node := model.Node{Name: "node-redact", Host: "10.0.0.8", Username: "root", AuthType: "key", BackupDir: "node-redact"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	secretConfig := `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY","append_only":true}`
	taskEntity := model.Task{
		Name:           "task-redact",
		NodeID:         node.ID,
		ExecutorType:   "restic",
		RsyncSource:    "/data/src",
		RsyncTarget:    "/backup/repo",
		ExecutorConfig: secretConfig,
		Status:         "pending",
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	handler := NewTaskHandler(db, nil)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	r.GET("/tasks", handler.List)
	r.GET("/tasks/:id", handler.Get)
	r.POST("/tasks", handler.Create)
	r.PUT("/tasks/:id", handler.Update)

	requests := []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/tasks"},
		{method: http.MethodGet, path: fmt.Sprintf("/tasks/%d", taskEntity.ID)},
		{method: http.MethodPost, path: "/tasks", body: fmt.Sprintf(`{"name":"task-create-redact","node_id":%d,"executor_type":"restic","rsync_source":"/data/src","rsync_target":"/backup/repo","executor_config":%q}`, node.ID, secretConfig)},
		{method: http.MethodPut, path: fmt.Sprintf("/tasks/%d", taskEntity.ID), body: fmt.Sprintf(`{"name":"task-update-redact","node_id":%d,"executor_type":"restic","rsync_source":"/data/src","rsync_target":"/backup/repo","executor_config":%q}`, node.ID, secretConfig)},
	}
	for _, tc := range requests {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)
		if resp.Code < 200 || resp.Code >= 300 {
			t.Fatalf("%s %s 期望成功，实际: %d body=%s", tc.method, tc.path, resp.Code, resp.Body.String())
		}
		body := resp.Body.String()
		if strings.Contains(body, "executor_config") || strings.Contains(body, "FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY") {
			t.Fatalf("%s %s 响应不应暴露 executor_config 或密码，实际: %s", tc.method, tc.path, body)
		}
	}
}

func TestTaskListDefaultsToLatestCreatedTasksFirst(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.NodeOwner{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{Name: "node-recent", Host: "10.0.0.9", Username: "root", AuthType: "key", BackupDir: "node-recent"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	base := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	tasks := []model.Task{
		{Name: "oldest", NodeID: node.ID, ExecutorType: "local", Status: "pending", CreatedAt: base.Add(-2 * time.Hour), UpdatedAt: base.Add(-2 * time.Hour)},
		{Name: "middle", NodeID: node.ID, ExecutorType: "local", Status: "pending", CreatedAt: base.Add(-1 * time.Hour), UpdatedAt: base.Add(-1 * time.Hour)},
		{Name: "latest", NodeID: node.ID, ExecutorType: "local", Status: "pending", CreatedAt: base, UpdatedAt: base},
	}
	for i := range tasks {
		if err := db.Create(&tasks[i]).Error; err != nil {
			t.Fatalf("创建任务失败: %v", err)
		}
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewTaskHandler(db, nil)
	r.GET("/tasks", handler.List)

	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}

	var result struct {
		Data []model.Task `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(result.Data) != len(tasks) {
		t.Fatalf("返回任务数量不符合预期，期望 %d，实际 %d", len(tasks), len(result.Data))
	}

	gotNames := []string{result.Data[0].Name, result.Data[1].Name, result.Data[2].Name}
	wantNames := []string{"latest", "middle", "oldest"}
	for i := range wantNames {
		if gotNames[i] != wantNames[i] {
			t.Fatalf("默认排序不符合预期，实际顺序: %v", gotNames)
		}
	}
}

func TestBatchTriggerNoOpWritesCredentialAuditTelemetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.NodeOwner{}, &model.Task{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	operator := model.User{Username: "batch-operator", Role: "operator", PasswordHash: "hash-redacted"}
	if err := db.Create(&operator).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	node := model.Node{Name: "batch-unowned-node", Host: "redacted", Username: "batch-user", AuthType: "key", BackupDir: "batch-unowned-node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	taskEntity := model.Task{Name: "batch-unowned-task", NodeID: node.ID, ExecutorType: "rsync", RsyncSource: "redacted-source", RsyncTarget: "redacted-target", Status: "pending"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	runner := &mockTaskRunner{triggerManualRunID: 99}
	handler := NewTaskHandler(db, runner)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, operator.ID)
		c.Set(middleware.CtxUsername, operator.Username)
		c.Set(middleware.CtxRole, operator.Role)
		c.Next()
	})
	r.POST("/tasks/batch-trigger", handler.BatchTrigger)

	req := httptest.NewRequest(http.MethodPost, "/tasks/batch-trigger", strings.NewReader(fmt.Sprintf(`{"task_ids":[%d]}`, taskEntity.ID)))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("全量过滤的 batch trigger 仍应返回 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	if len(runner.syncCalls) != 0 || len(runner.removeCalls) != 0 {
		t.Fatalf("batch trigger no-op 不应触发调度副作用，sync=%d remove=%d", len(runner.syncCalls), len(runner.removeCalls))
	}

	var payload struct {
		Data struct {
			Total        int `json:"total"`
			SuccessCount int `json:"success_count"`
			Results      []struct {
				TaskID uint   `json:"task_id"`
				Error  string `json:"error"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析 batch trigger 响应失败: %v", err)
	}
	if payload.Data.Total != 1 || payload.Data.SuccessCount != 0 || len(payload.Data.Results) != 1 || payload.Data.Results[0].TaskID != taskEntity.ID || payload.Data.Results[0].Error == "" {
		t.Fatalf("batch trigger no-op 响应形状不符合预期: %+v", payload.Data)
	}

	events := loadCredentialAuditEvents(t, db, "task.batch_trigger")
	if len(events) != 1 {
		t.Fatalf("batch trigger no-op 应写 1 条凭据审计事件，实际: %+v", events)
	}
	event := events[0]
	if event.UserID != operator.ID || event.Username != operator.Username || event.Role != operator.Role || event.Outcome != credentialaudit.OutcomeBlocked {
		t.Fatalf("batch trigger no-op 审计 actor/outcome 不符合预期: %+v", event)
	}
	metadata := assertNoForbiddenAuditMetadata(t, event.Metadata)
	if metadata["stage"] != "no_op" || metadata["requested_count"].(float64) != 1 || metadata["eligible_count"].(float64) != 0 || metadata["executed_count"].(float64) != 0 || metadata["blocked_count"].(float64) != 1 || metadata["no_op"] != true {
		t.Fatalf("batch trigger no-op 审计 metadata 不符合预期: %#v", metadata)
	}
}

func TestTaskLogsFilterLevelLimitBeforeID(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.TaskLog{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{Name: "node-log", Host: "10.0.1.1", Username: "root", AuthType: "key", BackupDir: "node-log"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	taskEntity := model.Task{Name: "task-log", NodeID: node.ID, ExecutorType: "local", Status: "running"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	log1 := model.TaskLog{TaskID: taskEntity.ID, Level: "info", Message: "info message"}
	rawLegacyErrorLog := `执行命令: rclone sync /srv/private/task-log remote:backup/task-log --password FAKE_TASK_LOG_PASSWORD_FOR_TEST_ONLY`
	log2 := model.TaskLog{TaskID: taskEntity.ID, Level: "error", Message: rawLegacyErrorLog}
	log3 := model.TaskLog{TaskID: taskEntity.ID, Level: "error", Message: "error message 2"}
	if err := db.Create(&log1).Error; err != nil {
		t.Fatalf("创建日志1失败: %v", err)
	}
	if err := db.Create(&log2).Error; err != nil {
		t.Fatalf("创建日志2失败: %v", err)
	}
	if err := db.Create(&log3).Error; err != nil {
		t.Fatalf("创建日志3失败: %v", err)
	}

	r := gin.New()
	handler := NewTaskHandler(db, nil)
	r.GET("/tasks/:id/logs", handler.Logs)

	url := fmt.Sprintf("/tasks/%d/logs?level=error&before_id=%d&limit=10", taskEntity.ID, log3.ID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}

	var result struct {
		Data []model.TaskLog `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(result.Data) != 1 || result.Data[0].ID != log2.ID {
		t.Fatalf("日志过滤结果不符合预期，实际: %+v", result.Data)
	}
	body := resp.Body.String()
	for _, forbidden := range []string{"rclone", "/srv/private/task-log", "remote:backup/task-log", "FAKE_TASK_LOG_PASSWORD_FOR_TEST_ONLY"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("任务日志响应泄漏原始遗留证据片段 %q: %s", forbidden, body)
		}
	}
	if result.Data[0].Message == rawLegacyErrorLog || !strings.Contains(result.Data[0].Message, "[命令已隐藏]") {
		t.Fatalf("期望任务日志 message 在读边界脱敏，实际: %+v", result.Data[0])
	}
	var storedLog model.TaskLog
	if err := db.First(&storedLog, log2.ID).Error; err != nil {
		t.Fatalf("重新读取日志失败: %v", err)
	}
	if storedLog.Message != rawLegacyErrorLog {
		t.Fatalf("读边界脱敏不应改写 DB task_logs.message，实际: %q", storedLog.Message)
	}

	url = fmt.Sprintf("/tasks/%d/logs?level=error&limit=1", taskEntity.ID)
	req = httptest.NewRequest(http.MethodGet, url, nil)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("limit 场景期望状态码 200，实际: %d", resp.Code)
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析 limit 响应失败: %v", err)
	}
	if len(result.Data) != 1 || !strings.EqualFold(result.Data[0].Level, "error") || result.Data[0].ID != log3.ID {
		t.Fatalf("日志 limit 结果不符合预期，实际: %+v", result.Data)
	}
}

func openTaskHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}

func TestValidateTaskRequestRejectsInvalidCron(t *testing.T) {
	req := taskPkg.CreateTaskInput{
		Name:         "task-a",
		NodeID:       1,
		ExecutorType: "rsync",
		RsyncSource:  "/data/src",
		RsyncTarget:  "/backup/dst",
		CronSpec:     "invalid cron",
	}
	if err := taskPkg.ValidateTaskInput(req); err == nil {
		t.Fatalf("期望非法 cron 返回错误")
	}
}

func TestValidateTaskRequestRejectsRsyncWithoutPath(t *testing.T) {
	req := taskPkg.CreateTaskInput{
		Name:         "task-rsync",
		NodeID:       1,
		ExecutorType: "rsync",
		RsyncSource:  "/data/src",
		RsyncTarget:  "",
	}
	if err := taskPkg.ValidateTaskInput(req); err == nil {
		t.Fatalf("期望 rsync 缺少目标路径时返回错误")
	}
}

func TestValidateTaskRequestChecksWhitelist(t *testing.T) {
	t.Setenv("RSYNC_ALLOWED_SOURCE_PREFIXES", "/data")
	t.Setenv("RSYNC_ALLOWED_TARGET_PREFIXES", "/backup")

	req := taskPkg.CreateTaskInput{
		Name:         "task-rsync",
		NodeID:       1,
		ExecutorType: "rsync",
		RsyncSource:  "/etc/passwd",
		RsyncTarget:  "/backup/node-a",
	}
	if err := taskPkg.ValidateTaskInput(req); err == nil {
		t.Fatalf("期望 source 路径不在白名单时返回错误")
	}

	req.RsyncSource = "/data/node-a"
	if err := taskPkg.ValidateTaskInput(req); err != nil {
		t.Fatalf("期望白名单内路径通过校验，实际错误: %v", err)
	}
}

func TestValidateTaskRequestRejectsNonRsyncExecutor(t *testing.T) {
	req := taskPkg.CreateTaskInput{
		Name:         "task-local",
		NodeID:       1,
		ExecutorType: "local",
	}
	if err := taskPkg.ValidateTaskInput(req); err == nil {
		t.Fatalf("期望 local 执行器被拒绝")
	}
}

func TestValidateTaskRequestRejectsCommandWithEmptyContent(t *testing.T) {
	// command 类型任务必须填写命令内容
	req := taskPkg.CreateTaskInput{
		Name:         "task-cmd",
		NodeID:       1,
		ExecutorType: "command",
		Command:      "   ", // 全空白，应被拒绝
	}
	if err := taskPkg.ValidateTaskInput(req); err == nil {
		t.Fatalf("期望 command 内容为空时被拒绝")
	}
}

func TestValidateTaskRequestRejectsCommandSafetyViolations(t *testing.T) {
	req := taskPkg.CreateTaskInput{
		Name:         "task-cmd",
		NodeID:       1,
		ExecutorType: "command",
		Command:      strings.Repeat("a", maxCommandLength+1),
	}
	if err := taskPkg.ValidateTaskInput(req); err == nil || !strings.Contains(err.Error(), "命令长度不能超过") {
		t.Fatalf("期望 command 长度超限被拒绝，实际: %v", err)
	}

	req.Command = "rm -rf /etc"
	if err := taskPkg.ValidateTaskInput(req); err == nil || !strings.Contains(err.Error(), "安全策略拦截") {
		t.Fatalf("期望危险 command 被拒绝，实际: %v", err)
	}
}

func TestInferTaskExecutorDefaultsToRsync(t *testing.T) {
	req := &taskPkg.CreateTaskInput{}
	taskPkg.InferTaskExecutor(req, "")
	if req.ExecutorType != "rsync" {
		t.Fatalf("期望默认推断 rsync，实际: %s", req.ExecutorType)
	}
}

func TestInferTaskExecutorKeepsExplicitValue(t *testing.T) {
	req := &taskPkg.CreateTaskInput{ExecutorType: "local"}
	taskPkg.InferTaskExecutor(req, "rsync")
	if req.ExecutorType != "local" {
		t.Fatalf("期望保留显式 executor_type 供校验拒绝，实际: %s", req.ExecutorType)
	}
}

func withAdminRole(r *gin.Engine) *gin.Engine {
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "admin")
		c.Set(middleware.CtxUserID, uint(1))
		c.Next()
	})
	return r
}

func TestTaskCreateRejectsLocalExecutorFromRequest(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{Name: "node-a", Host: "10.0.0.1", Username: "root", AuthType: "key", BackupDir: "node-a"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	handler := NewTaskHandler(db, nil)
	r := withAdminRole(gin.New())
	r.POST("/tasks", handler.Create)

	body := fmt.Sprintf(`{"name":"task-local","node_id":%d,"executor_type":"local","rsync_source":"/data/src","rsync_target":"/backup/dst","cron_spec":"*/5 * * * *"}`, node.ID)
	req := httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("期望状态码 400，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "不支持的执行器类型") {
		t.Fatalf("期望返回 local 拒绝错误，实际: %s", resp.Body.String())
	}
}

func TestTaskCreateRejectsUnknownNodeReference(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	handler := NewTaskHandler(db, nil)
	r := withAdminRole(gin.New())
	r.POST("/tasks", handler.Create)

	req := httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(`{"name":"task-a","node_id":999,"rsync_source":"/data/src","rsync_target":"/backup/dst"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("期望状态码 400，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "所选节点不存在，请重新选择") {
		t.Fatalf("期望返回节点不存在错误，实际: %s", resp.Body.String())
	}

	var count int64
	if err := db.Model(&model.Task{}).Count(&count).Error; err != nil {
		t.Fatalf("统计任务失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("非法节点引用不应写入任务记录，实际数量: %d", count)
	}
}

func TestTaskCreateRejectsUnownedNodeForOperator(t *testing.T) {
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

	handler := NewTaskHandler(db, nil)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "operator")
		c.Set(middleware.CtxUserID, operator.ID)
		c.Next()
	})
	r.POST("/tasks", handler.Create)

	body := fmt.Sprintf(`{"name":"task-unowned","node_id":%d,"rsync_source":"/data/src","rsync_target":"/backup/dst","cron_spec":"*/5 * * * *"}`, unownedNode.ID)
	req := httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("operator 创建未授权节点任务期望状态码 403，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var count int64
	if err := db.Model(&model.Task{}).Count(&count).Error; err != nil {
		t.Fatalf("统计任务失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("未授权节点不应创建任务，实际数量: %d", count)
	}
}

func TestTaskCreateReturnsInternalErrorWhenTaskRefValidationQueryFails(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	// 不执行 AutoMigrate，触发引用校验查询失败，验证返回 500 而非 400。

	handler := NewTaskHandler(db, nil)
	r := withAdminRole(gin.New())
	r.POST("/tasks", handler.Create)

	req := httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(`{"name":"task-a","node_id":1,"rsync_source":"/data/src","rsync_target":"/backup/dst"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("期望状态码 500，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "服务器内部错误") {
		t.Fatalf("期望返回内部错误提示，实际: %s", resp.Body.String())
	}
}

func TestTaskCreateSyncFailureCompensatesByDeletingTask(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{Name: "node-a", Host: "10.0.0.1", Username: "root", AuthType: "key", BackupDir: "node-a"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	runner := &mockTaskRunner{
		syncErrs: []error{errors.New("sync failed")},
	}
	handler := NewTaskHandler(db, runner)
	r := withAdminRole(gin.New())
	r.POST("/tasks", handler.Create)

	body := fmt.Sprintf(`{"name":"task-a","node_id":%d,"rsync_source":"/data/src","rsync_target":"/backup/dst","cron_spec":"*/5 * * * *"}`, node.ID)
	req := httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("期望状态码 400，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var count int64
	if err := db.Model(&model.Task{}).Count(&count).Error; err != nil {
		t.Fatalf("统计任务失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("期望补偿后不保留任务记录，实际数量: %d", count)
	}
	if len(runner.removeCalls) != 1 {
		t.Fatalf("期望同步失败后调用 RemoveSchedule 一次，实际: %d", len(runner.removeCalls))
	}
	if runner.removeCalls[0] == 0 {
		t.Fatalf("期望 RemoveSchedule 使用有效任务 ID，实际: %d", runner.removeCalls[0])
	}
}

func TestTaskUpdateSyncFailureCompensatesByRestoringTask(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{Name: "node-a", Host: "10.0.0.1", Username: "root", AuthType: "key", BackupDir: "node-a"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	taskEntity := model.Task{
		Name:         "task-old",
		NodeID:       node.ID,
		Command:      "",
		RsyncSource:  "/data/old",
		RsyncTarget:  "/backup/old",
		ExecutorType: "rsync",
		CronSpec:     "*/5 * * * *",
		Status:       "pending",
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	runner := &mockTaskRunner{
		syncErrs: []error{errors.New("sync failed"), nil},
	}
	handler := NewTaskHandler(db, runner)
	r := withAdminRole(gin.New())
	r.PUT("/tasks/:id", handler.Update)

	body := fmt.Sprintf(`{"name":"task-new","node_id":%d,"rsync_source":"/data/new","rsync_target":"/backup/new","cron_spec":"*/10 * * * *"}`, node.ID)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tasks/%d", taskEntity.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("期望状态码 400，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var restored model.Task
	if err := db.First(&restored, taskEntity.ID).Error; err != nil {
		t.Fatalf("读取补偿后任务失败: %v", err)
	}
	if restored.Name != "task-old" || restored.RsyncSource != "/data/old" || restored.RsyncTarget != "/backup/old" || restored.CronSpec != "*/5 * * * *" {
		t.Fatalf("期望更新失败后恢复旧任务，实际: %+v", restored)
	}
	if len(runner.syncCalls) != 2 {
		t.Fatalf("期望调度补偿触发两次同步（新值失败+旧值恢复），实际: %d", len(runner.syncCalls))
	}
	if runner.syncCalls[1].CronSpec != "*/5 * * * *" {
		t.Fatalf("期望第二次同步恢复旧 cron，实际: %s", runner.syncCalls[1].CronSpec)
	}
	if len(runner.removeCalls) != 1 || runner.removeCalls[0] != taskEntity.ID {
		t.Fatalf("期望更新失败时先移除失败调度，实际调用: %+v", runner.removeCalls)
	}
}

func TestTaskUpdateDoesNotInheritCommand(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{Name: "node-a", Host: "10.0.0.1", Username: "root", AuthType: "key", BackupDir: "node-a"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	taskEntity := model.Task{
		Name:         "task-old",
		NodeID:       node.ID,
		Command:      "echo legacy-command",
		RsyncSource:  "/data/src",
		RsyncTarget:  "/backup/dst",
		ExecutorType: "rsync",
		CronSpec:     "*/5 * * * *",
		Status:       "pending",
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	runner := &mockTaskRunner{}
	handler := NewTaskHandler(db, runner)
	r := withAdminRole(gin.New())
	r.PUT("/tasks/:id", handler.Update)

	body := fmt.Sprintf(`{"name":"task-new","node_id":%d,"rsync_source":"/data/src","rsync_target":"/backup/dst","cron_spec":"*/10 * * * *"}`, node.ID)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tasks/%d", taskEntity.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var updated model.Task
	if err := db.First(&updated, taskEntity.ID).Error; err != nil {
		t.Fatalf("查询更新后任务失败: %v", err)
	}
	if updated.Command != "" {
		t.Fatalf("期望更新后 command 被清空，实际: %q", updated.Command)
	}
}

func TestTaskUpdateRejectsUnknownPolicyReference(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{Name: "node-a", Host: "10.0.0.1", Username: "root", AuthType: "key", BackupDir: "node-a"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	taskEntity := model.Task{
		Name:         "task-old",
		NodeID:       node.ID,
		Command:      "",
		RsyncSource:  "/data/src",
		RsyncTarget:  "/backup/dst",
		ExecutorType: "rsync",
		CronSpec:     "*/5 * * * *",
		Status:       "pending",
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	handler := NewTaskHandler(db, nil)
	r := withAdminRole(gin.New())
	r.PUT("/tasks/:id", handler.Update)

	req := httptest.NewRequest(
		http.MethodPut,
		fmt.Sprintf("/tasks/%d", taskEntity.ID),
		strings.NewReader(fmt.Sprintf(`{"name":"task-old","node_id":%d,"policy_id":999,"rsync_source":"/data/src","rsync_target":"/backup/dst","cron_spec":"*/5 * * * *"}`, node.ID)),
	)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("期望状态码 400，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "所选策略不存在，请重新选择") {
		t.Fatalf("期望返回策略不存在错误，实际: %s", resp.Body.String())
	}

	var updated model.Task
	if err := db.First(&updated, taskEntity.ID).Error; err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if updated.PolicyID != nil {
		t.Fatalf("非法策略引用不应写入任务，实际 policy_id=%v", *updated.PolicyID)
	}
}

func TestTaskUpdateRejectsMovingTaskToUnownedNodeForOperator(t *testing.T) {
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

	taskEntity := model.Task{
		Name:         "task-owned",
		NodeID:       ownedNode.ID,
		RsyncSource:  "/data/src",
		RsyncTarget:  "/backup/dst",
		ExecutorType: "rsync",
		CronSpec:     "*/5 * * * *",
		Status:       "pending",
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	handler := NewTaskHandler(db, &mockTaskRunner{})
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "operator")
		c.Set(middleware.CtxUserID, operator.ID)
		c.Next()
	})
	r.PUT("/tasks/:id", middleware.OwnershipTaskCheck(db), handler.Update)

	body := fmt.Sprintf(`{"name":"task-owned","node_id":%d,"rsync_source":"/data/src","rsync_target":"/backup/dst","cron_spec":"*/10 * * * *"}`, unownedNode.ID)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tasks/%d", taskEntity.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("operator 迁移任务到未授权节点期望状态码 403，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var updated model.Task
	if err := db.First(&updated, taskEntity.ID).Error; err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if updated.NodeID != ownedNode.ID {
		t.Fatalf("未授权迁移不应改写 node_id，实际: %d", updated.NodeID)
	}
}

func TestTaskDeleteDoesNotRemoveScheduleWhenDBDeleteFails(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	// 不执行 AutoMigrate，触发删除时数据库错误，验证不会提前移除调度

	runner := &mockTaskRunner{}
	handler := NewTaskHandler(db, runner)
	r := gin.New()
	r.DELETE("/tasks/:id", handler.Delete)

	req := httptest.NewRequest(http.MethodDelete, "/tasks/1", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("期望状态码 500，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	if len(runner.removeCalls) != 0 {
		t.Fatalf("数据库删除失败时不应先移除调度，实际 removeCalls=%+v", runner.removeCalls)
	}
}

// Empty query (no status/node/policy filters) must still scope operator lists to owned nodes.
func TestTaskListEmptyFiltersScopesToOwnedNodesForOperator(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.Task{}, &model.NodeOwner{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	operator := model.User{Username: "list-op", Role: "operator", PasswordHash: "hash"}
	if err := db.Create(&operator).Error; err != nil {
		t.Fatalf("创建 operator 失败: %v", err)
	}
	ownedNode := model.Node{Name: "owned", Host: "10.0.0.1", Username: "root", AuthType: "key", BackupDir: "owned"}
	otherNode := model.Node{Name: "other", Host: "10.0.0.2", Username: "root", AuthType: "key", BackupDir: "other"}
	if err := db.Create(&ownedNode).Error; err != nil {
		t.Fatalf("创建 owned 节点失败: %v", err)
	}
	if err := db.Create(&otherNode).Error; err != nil {
		t.Fatalf("创建 other 节点失败: %v", err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: ownedNode.ID, UserID: operator.ID}).Error; err != nil {
		t.Fatalf("创建 ownership 失败: %v", err)
	}
	ownedTask := model.Task{Name: "owned-task", NodeID: ownedNode.ID, ExecutorType: "rsync", Status: "pending", RsyncSource: "/a", RsyncTarget: "/b"}
	otherTask := model.Task{Name: "other-task", NodeID: otherNode.ID, ExecutorType: "rsync", Status: "running", RsyncSource: "/c", RsyncTarget: "/d"}
	if err := db.Create(&ownedTask).Error; err != nil {
		t.Fatalf("创建 owned 任务失败: %v", err)
	}
	if err := db.Create(&otherTask).Error; err != nil {
		t.Fatalf("创建 other 任务失败: %v", err)
	}

	handler := NewTaskHandler(db, nil)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "operator")
		c.Set(middleware.CtxUserID, operator.ID)
		c.Next()
	})
	r.GET("/tasks", handler.List)

	// No status / node_id / policy_id / keyword — pure empty filter path.
	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var result struct {
		Data []model.Task `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(result.Data) != 1 {
		t.Fatalf("空 filter 下 operator 应只见 owned 任务，实际 %d 条: %+v", len(result.Data), result.Data)
	}
	if result.Data[0].ID != ownedTask.ID {
		t.Fatalf("期望 owned task id=%d，实际 id=%d", ownedTask.ID, result.Data[0].ID)
	}
}

func TestTaskDeleteArchivesAndUnlinks(t *testing.T) {
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(
		&model.User{}, &model.Node{}, &model.NodeOwner{}, &model.Task{}, &model.TaskRun{}, &model.TaskLog{},
		&model.BackupRepository{}, &model.TaskRepositoryLink{}, &model.BackupRetentionPolicy{},
		&model.RecoveryPoint{}, &model.CredentialAuditEvent{},
		&model.WrappedDomainKey{}, &model.BackupAssetAuditCheckpoint{}, &model.BackupAssetAuditEvent{},
	); err != nil {
		t.Fatalf("初始化归档测试表失败: %v", err)
	}

	admin := model.User{Username: "archive-admin", Role: "admin", PasswordHash: "hash"}
	operator := model.User{Username: "archive-operator", Role: "operator", PasswordHash: "hash"}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("创建 admin 失败: %v", err)
	}
	if err := db.Create(&operator).Error; err != nil {
		t.Fatalf("创建 operator 失败: %v", err)
	}
	ownedNode := model.Node{Name: "archive-owned", Host: "10.0.14.1", Username: "root", AuthType: "key", BackupDir: "archive-owned"}
	unownedNode := model.Node{Name: "archive-unowned", Host: "10.0.14.2", Username: "root", AuthType: "key", BackupDir: "archive-unowned"}
	if err := db.Create(&ownedNode).Error; err != nil {
		t.Fatalf("创建 owned 节点失败: %v", err)
	}
	if err := db.Create(&unownedNode).Error; err != nil {
		t.Fatalf("创建 unowned 节点失败: %v", err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: ownedNode.ID, UserID: operator.ID}).Error; err != nil {
		t.Fatalf("创建 ownership 失败: %v", err)
	}

	unownedTask := model.Task{Name: "unowned-archive", NodeID: unownedNode.ID, ExecutorType: "command", Command: "true", Status: "pending", Enabled: true}
	if err := db.Create(&unownedTask).Error; err != nil {
		t.Fatalf("创建未授权任务失败: %v", err)
	}
	taskEntity := model.Task{
		Name: "http-archive-task", NodeID: ownedNode.ID, ExecutorType: "restic", Command: "restic backup /data",
		RsyncSource: "/data/src", RsyncTarget: "/backup/dst", ExecutorConfig: `{"repository_password":"FAKE_HTTP_ARCHIVE_PASSWORD_FOR_TEST_ONLY"}`,
		CronSpec: "*/5 * * * *", Status: "pending", Enabled: true,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	run := model.TaskRun{TaskID: taskEntity.ID, TriggerType: "manual", Status: "success"}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("创建 TaskRun 失败: %v", err)
	}
	if err := db.Create(&model.TaskLog{TaskID: taskEntity.ID, TaskRunID: &run.ID, Level: "info", Message: "http archive history"}).Error; err != nil {
		t.Fatalf("创建 TaskLog 失败: %v", err)
	}
	repositoryID := "dddddddddddddddddddddddddddddddd"
	if err := db.Create(&model.BackupRepository{
		ID: repositoryID, ProviderKind: "restic", DisplayName: "http-archive-repo",
		VersionMode: "native_snapshot", Status: "online", CapabilityRevision: 1, CapabilitiesJSON: `{}`,
		ImmutabilityLevel: "backend_versioned",
	}).Error; err != nil {
		t.Fatalf("创建仓库失败: %v", err)
	}
	now := time.Date(2026, 8, 18, 15, 0, 0, 0, time.UTC)
	legacyLocator := "sftp:http-archive@example.invalid:/protected/legacy"
	link := model.TaskRepositoryLink{
		ID: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", TaskID: &taskEntity.ID, RepositoryID: repositoryID,
		TaskNameSnapshot: "http-archive-task", NodeIDSnapshot: ownedNode.ID, NodeNameSnapshot: ownedNode.Name,
		PublicationMode: "native_snapshot", EncryptedLegacyLocator: legacyLocator, LinkedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&link).Error; err != nil {
		t.Fatalf("创建 TaskRepositoryLink 失败: %v", err)
	}
	captured := now.Add(-time.Hour)
	point := model.RecoveryPoint{
		ID: "ffffffffffffffffffffffffffffffff", RepositoryID: repositoryID, ProducingTaskID: &taskEntity.ID, ProducingTaskRunID: &run.ID,
		ProducingTaskNameSnapshot: "http-archive-task", LineageJSON: `{"version":1}`, EncryptedProviderLocator: `{"snapshot":"keep"}`,
		Semantics: "native_snapshot", State: "committed", CapturedAt: &captured, CommittedAt: &captured, PointRevision: 1,
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: "backend_versioned", PhysicalAvailability: "online", HoldState: "none",
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("创建 RecoveryPoint 失败: %v", err)
	}
	if err := db.Create(&model.CredentialAuditEvent{
		Username: "admin", Action: "task.manual_trigger", Purpose: "task_run", CredentialKind: "ssh_key",
		CredentialSource: "node", TaskID: &taskEntity.ID, Outcome: "success", Metadata: "{}",
	}).Error; err != nil {
		t.Fatalf("创建审计失败: %v", err)
	}
	policyID := "12121212121212121212121212121212"
	if err := db.Create(&model.BackupRetentionPolicy{
		ID: policyID, ScopeKind: "task_link", ScopeID: link.ID, Revision: 1,
		RulesJSON: `{"version":1,"age":{"keep_days":7}}`, Status: "active",
		CreatedBy: admin.ID, UpdatedBy: admin.ID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("创建 Task-link 保留策略失败: %v", err)
	}

	dependent := model.Task{Name: "http-dependent", NodeID: ownedNode.ID, DependsOnTaskID: &taskEntity.ID, ExecutorType: "command", Command: "true", Status: "pending", Enabled: true}
	if err := db.Create(&dependent).Error; err != nil {
		t.Fatalf("创建依赖任务失败: %v", err)
	}

	runner := &mockTaskRunner{}
	handler := NewTaskHandler(db, runner)
	owned := gin.New()
	owned.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "operator")
		c.Set(middleware.CtxUserID, operator.ID)
		c.Next()
	})
	owned.DELETE("/tasks/:id", middleware.OwnershipTaskCheck(db), handler.Delete)
	unownedReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/tasks/%d", unownedTask.ID), nil)
	unownedResp := httptest.NewRecorder()
	owned.ServeHTTP(unownedResp, unownedReq)
	if unownedResp.Code != http.StatusForbidden {
		t.Fatalf("operator 删除未授权任务期望 403，实际: %d，响应: %s", unownedResp.Code, unownedResp.Body.String())
	}

	adminRouter := withAdminRole(gin.New())
	adminRouter.Use(func(c *gin.Context) {
		c.Set(middleware.RequestIDKey, "archive-http-request-1")
		c.Next()
	})
	adminRouter.DELETE("/tasks/:id", middleware.OwnershipTaskCheck(db), handler.Delete)
	conflictReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/tasks/%d", taskEntity.ID), nil)
	conflictResp := httptest.NewRecorder()
	adminRouter.ServeHTTP(conflictResp, conflictReq)
	if conflictResp.Code != http.StatusConflict {
		t.Fatalf("存在活依赖时期望 409，实际: %d，响应: %s", conflictResp.Code, conflictResp.Body.String())
	}
	if len(runner.removeCalls) != 0 {
		t.Fatalf("依赖冲突不应移除调度，实际 removeCalls=%+v", runner.removeCalls)
	}
	if err := db.Delete(&dependent).Error; err != nil {
		t.Fatalf("解除测试依赖失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/tasks/%d", taskEntity.ID), nil)
	resp := httptest.NewRecorder()
	adminRouter.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("归档删除期望 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Archived             bool `json:"archived"`
			Unlinked             bool `json:"unlinked"`
			ScheduleRemoved      bool `json:"schedule_removed"`
			ProviderBytesDeleted bool `json:"provider_bytes_deleted"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析归档响应失败: %v", err)
	}
	if envelope.Code != http.StatusOK || !envelope.Data.Archived || !envelope.Data.Unlinked || !envelope.Data.ScheduleRemoved || envelope.Data.ProviderBytesDeleted {
		t.Fatalf("期望标准归档结果，实际: %+v 原文=%s", envelope, resp.Body.String())
	}
	if len(runner.removeCalls) != 1 || runner.removeCalls[0] != taskEntity.ID {
		t.Fatalf("期望提交后移除调度一次，实际 removeCalls=%+v", runner.removeCalls)
	}

	var archived model.Task
	if err := db.First(&archived, taskEntity.ID).Error; err != nil {
		t.Fatalf("任务行应被归档保留: %v", err)
	}
	if archived.Enabled || archived.ArchivedAt == nil || archived.ExecutorConfig == "" {
		t.Fatalf("任务未按归档合同落库: enabled=%v archived_at=%v executor=%q", archived.Enabled, archived.ArchivedAt, archived.ExecutorConfig)
	}
	var storedLink model.TaskRepositoryLink
	if err := db.First(&storedLink, "id = ?", link.ID).Error; err != nil {
		t.Fatalf("链接行丢失: %v", err)
	}
	if storedLink.TaskID != nil || storedLink.UnlinkedAt == nil || storedLink.EncryptedLegacyLocator != legacyLocator {
		t.Fatalf("链接未按 unlink 合同保留快照: %+v", storedLink)
	}
	if err := db.First(&model.BackupRepository{}, "id = ?", repositoryID).Error; err != nil {
		t.Fatalf("仓库被删除: %v", err)
	}
	if err := db.First(&model.RecoveryPoint{}, "id = ?", point.ID).Error; err != nil {
		t.Fatalf("RecoveryPoint 被删除: %v", err)
	}
	if err := db.First(&model.TaskRun{}, run.ID).Error; err != nil {
		t.Fatalf("TaskRun 被删除: %v", err)
	}
	var auditCount int64
	if err := db.Model(&model.CredentialAuditEvent{}).Where("task_id = ?", taskEntity.ID).Count(&auditCount).Error; err != nil || auditCount != 1 {
		t.Fatalf("审计应保留，count=%d err=%v", auditCount, err)
	}
	var storedPolicy model.BackupRetentionPolicy
	if err := db.First(&storedPolicy, "id = ?", policyID).Error; err != nil {
		t.Fatalf("保留策略应被软删除保留: %v", err)
	}
	if storedPolicy.Status != "deleted" || storedPolicy.DeletedAt == nil || storedPolicy.Revision != 2 {
		t.Fatalf("保留策略未按归档合同软删除: status=%q revision=%d deleted_at=%v", storedPolicy.Status, storedPolicy.Revision, storedPolicy.DeletedAt)
	}
	var deleteAudits []model.BackupAssetAuditEvent
	if err := db.Where("action = ? AND repository_id = ?", "retention_policy_delete", repositoryID).
		Find(&deleteAudits).Error; err != nil || len(deleteAudits) != 1 {
		t.Fatalf("归档应写入 retention_policy_delete 审计，count=%d err=%v", len(deleteAudits), err)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(deleteAudits[0].FieldsJSON), &fields); err != nil {
		t.Fatalf("parse retention_policy_delete fields: %v", err)
	}
	if fields["correlation_id"] != "archive-http-request-1" {
		t.Fatalf("retention_policy_delete correlation=%v fields=%s, want archive-http-request-1", fields["correlation_id"], deleteAudits[0].FieldsJSON)
	}
}

func TestTaskDeleteDoesNotRemoveScheduleWhenArchiveFails(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	node := model.Node{Name: "archive-fail-node", Host: "10.0.14.3", Username: "root", AuthType: "key", BackupDir: "archive-fail"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	taskEntity := model.Task{
		Name: "archive-fail-task", NodeID: node.ID, ExecutorType: "command", Command: "true",
		CronSpec: "*/5 * * * *", Status: "pending", Enabled: true,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := db.Callback().Update().Before("gorm:update").Register("task_archive_fail_"+t.Name(), func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "tasks" {
			_ = tx.AddError(errors.New("forced archive update failure"))
		}
	}); err != nil {
		t.Fatalf("注册失败回调: %v", err)
	}

	runner := &mockTaskRunner{}
	handler := NewTaskHandler(db, runner)
	r := gin.New()
	r.DELETE("/tasks/:id", handler.Delete)
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/tasks/%d", taskEntity.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("归档失败期望 500，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	if len(runner.removeCalls) != 0 {
		t.Fatalf("归档失败时不应移除调度，实际 removeCalls=%+v", runner.removeCalls)
	}
	var stillLive model.Task
	if err := db.First(&stillLive, taskEntity.ID).Error; err != nil {
		t.Fatalf("归档失败后任务应仍存在: %v", err)
	}
	if !stillLive.Enabled || stillLive.ArchivedAt != nil {
		t.Fatalf("归档失败必须保留可调度任务，enabled=%v archived_at=%v", stillLive.Enabled, stillLive.ArchivedAt)
	}
}

func TestTaskResumeRejectsArchivedTask(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.Task{}); err != nil {
		t.Fatalf("migrate archived resume tables: %v", err)
	}
	node := model.Node{Name: "archived-resume-node", Host: "10.0.18.1", Username: "root", AuthType: "key", BackupDir: "archived-resume"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	archivedAt := time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC)
	taskEntity := model.Task{
		Name: "archived-resume-task", NodeID: node.ID, ExecutorType: "command", Command: "true",
		CronSpec: "*/5 * * * *", Status: "pending", Enabled: false, ArchivedAt: &archivedAt,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("create archived task: %v", err)
	}
	if err := db.Model(&taskEntity).Updates(map[string]any{
		"enabled":     false,
		"archived_at": archivedAt,
		"next_run_at": nil,
	}).Error; err != nil {
		t.Fatalf("persist archived disabled task: %v", err)
	}
	cron := scheduler.NewCronScheduler()
	manager := taskPkg.NewManager(db, nil, nil, cron, nil, nil, 8, 90)
	t.Cleanup(func() {
		_ = manager.Shutdown(context.Background())
		cron.Stop()
	})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/tasks/:id/resume", NewTaskHandler(db, manager).Resume)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/tasks/%d/resume", taskEntity.ID), nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("archived resume status=%d body=%s, want 409", response.Code, response.Body.String())
	}

	var reloaded model.Task
	if err := db.First(&reloaded, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload archived resume task: %v", err)
	}
	if reloaded.Enabled || reloaded.ArchivedAt == nil || reloaded.NextRunAt != nil {
		t.Fatalf("archived HTTP resume rescheduled task: enabled=%v archived_at=%v next_run_at=%v", reloaded.Enabled, reloaded.ArchivedAt, reloaded.NextRunAt)
	}
}

func TestTaskRestoreRejectsArchivedTask(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("migrate archived restore tables: %v", err)
	}
	node := model.Node{Name: "archived-restore-node", Host: "10.0.18.2", Username: "root", AuthType: "key", BackupDir: "archived-restore"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	archivedAt := time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC)
	taskEntity := model.Task{
		Name: "archived-restore-task", NodeID: node.ID, ExecutorType: "rsync",
		RsyncSource: "/tmp/src", RsyncTarget: "/tmp/dst", CronSpec: "*/5 * * * *",
		Status: "pending", Enabled: false, ArchivedAt: &archivedAt,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("create archived task: %v", err)
	}
	if err := db.Create(&model.TaskRun{TaskID: taskEntity.ID, TriggerType: "manual", Status: "success"}).Error; err != nil {
		t.Fatalf("create success run: %v", err)
	}
	cron := scheduler.NewCronScheduler()
	manager := taskPkg.NewManager(db, nil, nil, cron, nil, nil, 8, 90)
	t.Cleanup(func() {
		_ = manager.Shutdown(context.Background())
		cron.Stop()
	})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/tasks/:id/restore", NewTaskHandler(db, manager).Restore)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/tasks/%d/restore", taskEntity.ID), nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("archived restore status=%d body=%s, want 409", response.Code, response.Body.String())
	}
	var runs int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND trigger_type = ?", taskEntity.ID, "restore").Count(&runs).Error; err != nil {
		t.Fatalf("count restore runs: %v", err)
	}
	if runs != 0 {
		t.Fatalf("archived restore launched %d run(s)", runs)
	}
}

func TestTaskUpdateRejectsArchivedTask(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.Task{}); err != nil {
		t.Fatalf("migrate archived update tables: %v", err)
	}
	node := model.Node{Name: "archived-update-node", Host: "10.0.18.3", Username: "root", AuthType: "key", BackupDir: "archived-update"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	archivedAt := time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC)
	taskEntity := model.Task{
		Name: "archived-update-task", NodeID: node.ID, ExecutorType: "rsync",
		RsyncSource: "/tmp/src", RsyncTarget: "/tmp/dst", CronSpec: "*/5 * * * *",
		Status: "pending", Enabled: false, ArchivedAt: &archivedAt,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("create archived task: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := withAdminRole(gin.New())
	router.PUT("/tasks/:id", NewTaskHandler(db, nil).Update)
	body := fmt.Sprintf(`{"name":"mutated-archived-task","node_id":%d,"rsync_source":"/tmp/src","rsync_target":"/tmp/dst","cron_spec":"*/10 * * * *"}`, node.ID)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tasks/%d", taskEntity.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusConflict {
		t.Fatalf("archived update status=%d body=%s, want 409", response.Code, response.Body.String())
	}
	var reloaded model.Task
	if err := db.First(&reloaded, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload archived update task: %v", err)
	}
	if reloaded.Name != "archived-update-task" || reloaded.CronSpec != "*/5 * * * *" || reloaded.ArchivedAt == nil {
		t.Fatalf("archived HTTP update mutated task: name=%q cron=%q archived_at=%v", reloaded.Name, reloaded.CronSpec, reloaded.ArchivedAt)
	}
}
