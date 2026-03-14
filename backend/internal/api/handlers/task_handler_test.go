package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type mockTaskRunner struct {
	syncErrs    []error
	syncCalls   []model.Task
	removeCalls []uint
}

func (m *mockTaskRunner) TriggerManual(taskID uint) (uint, error) {
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

func TestTaskListFilterPaginationSort(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node1 := model.Node{Name: "node-a", Host: "10.0.0.1", Username: "root", AuthType: "key"}
	node2 := model.Node{Name: "node-b", Host: "10.0.0.2", Username: "root", AuthType: "key"}
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

	req = httptest.NewRequest(http.MethodGet, "/tasks?sort=-id&limit=2&offset=1", nil)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("分页请求期望状态码 200，实际: %d", resp.Code)
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析分页响应失败: %v", err)
	}
	if len(result.Data) != 2 {
		t.Fatalf("分页结果数量错误，期望 2，实际 %d", len(result.Data))
	}
	if result.Data[0].ID != task2.ID || result.Data[1].ID != task1.ID {
		t.Fatalf("排序或偏移不符合预期，实际 id 顺序: %d, %d", result.Data[0].ID, result.Data[1].ID)
	}
}

func TestTaskListDefaultsToLatestCreatedTasksFirst(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{Name: "node-recent", Host: "10.0.0.9", Username: "root", AuthType: "key"}
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

func TestTaskLogsFilterLevelLimitBeforeID(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.TaskLog{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{Name: "node-log", Host: "10.0.1.1", Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	taskEntity := model.Task{Name: "task-log", NodeID: node.ID, ExecutorType: "local", Status: "running"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	log1 := model.TaskLog{TaskID: taskEntity.ID, Level: "info", Message: "info message"}
	log2 := model.TaskLog{TaskID: taskEntity.ID, Level: "error", Message: "error message 1"}
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
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}

func TestValidateTaskRequestRejectsInvalidCron(t *testing.T) {
	req := taskRequest{
		Name:         "task-a",
		NodeID:       1,
		ExecutorType: "rsync",
		RsyncSource:  "/data/src",
		RsyncTarget:  "/backup/dst",
		CronSpec:     "invalid cron",
	}
	if err := validateTaskRequest(req); err == nil {
		t.Fatalf("期望非法 cron 返回错误")
	}
}

func TestValidateTaskRequestRejectsRsyncWithoutPath(t *testing.T) {
	req := taskRequest{
		Name:         "task-rsync",
		NodeID:       1,
		ExecutorType: "rsync",
		RsyncSource:  "/data/src",
		RsyncTarget:  "",
	}
	if err := validateTaskRequest(req); err == nil {
		t.Fatalf("期望 rsync 缺少目标路径时返回错误")
	}
}

func TestValidateTaskRequestChecksWhitelist(t *testing.T) {
	t.Setenv("RSYNC_ALLOWED_SOURCE_PREFIXES", "/data")
	t.Setenv("RSYNC_ALLOWED_TARGET_PREFIXES", "/backup")

	req := taskRequest{
		Name:         "task-rsync",
		NodeID:       1,
		ExecutorType: "rsync",
		RsyncSource:  "/etc/passwd",
		RsyncTarget:  "/backup/node-a",
	}
	if err := validateTaskRequest(req); err == nil {
		t.Fatalf("期望 source 路径不在白名单时返回错误")
	}

	req.RsyncSource = "/data/node-a"
	if err := validateTaskRequest(req); err != nil {
		t.Fatalf("期望白名单内路径通过校验，实际错误: %v", err)
	}
}

func TestValidateTaskRequestRejectsNonRsyncExecutor(t *testing.T) {
	req := taskRequest{
		Name:         "task-local",
		NodeID:       1,
		ExecutorType: "local",
	}
	if err := validateTaskRequest(req); err == nil {
		t.Fatalf("期望 local 执行器被拒绝")
	}
}

func TestValidateTaskRequestRejectsCommandWithEmptyContent(t *testing.T) {
	// command 类型任务必须填写命令内容
	req := taskRequest{
		Name:         "task-cmd",
		NodeID:       1,
		ExecutorType: "command",
		Command:      "   ", // 全空白，应被拒绝
	}
	if err := validateTaskRequest(req); err == nil {
		t.Fatalf("期望 command 内容为空时被拒绝")
	}
}

func TestInferTaskExecutorDefaultsToRsync(t *testing.T) {
	req := &taskRequest{}
	inferTaskExecutor(req, "local")
	if req.ExecutorType != "rsync" {
		t.Fatalf("期望默认推断 rsync，实际: %s", req.ExecutorType)
	}
}

func TestInferTaskExecutorKeepsExplicitValue(t *testing.T) {
	req := &taskRequest{ExecutorType: "local"}
	inferTaskExecutor(req, "rsync")
	if req.ExecutorType != "local" {
		t.Fatalf("期望保留显式 executor_type 供校验拒绝，实际: %s", req.ExecutorType)
	}
}

func TestTaskCreateRejectsLocalExecutorFromRequest(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	node := model.Node{Name: "node-a", Host: "10.0.0.1", Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	handler := NewTaskHandler(db, nil)
	r := gin.New()
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
	r := gin.New()
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

func TestTaskCreateReturnsInternalErrorWhenTaskRefValidationQueryFails(t *testing.T) {
	db := openTaskHandlerTestDB(t)
	// 不执行 AutoMigrate，触发引用校验查询失败，验证返回 500 而非 400。

	handler := NewTaskHandler(db, nil)
	r := gin.New()
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

	node := model.Node{Name: "node-a", Host: "10.0.0.1", Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	runner := &mockTaskRunner{
		syncErrs: []error{errors.New("sync failed")},
	}
	handler := NewTaskHandler(db, runner)
	r := gin.New()
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

	node := model.Node{Name: "node-a", Host: "10.0.0.1", Username: "root", AuthType: "key"}
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
	r := gin.New()
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

	node := model.Node{Name: "node-a", Host: "10.0.0.1", Username: "root", AuthType: "key"}
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
	r := gin.New()
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

	node := model.Node{Name: "node-a", Host: "10.0.0.1", Username: "root", AuthType: "key"}
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
	r := gin.New()
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
