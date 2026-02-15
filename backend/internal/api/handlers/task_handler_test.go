package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

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
		ExecutorType: "local",
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
