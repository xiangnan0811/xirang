package ws

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLoadBackfillEventsBySinceIDAndTaskID(t *testing.T) {
	db := openHubTestDB(t)
	if err := db.AutoMigrate(&model.Task{}, &model.TaskLog{}); err != nil {
		t.Fatalf("初始化任务/任务日志表失败: %v", err)
	}

	task1 := model.Task{Name: "task-1", NodeID: 1, ExecutorType: "rsync", Status: "success"}
	task2 := model.Task{Name: "task-2", NodeID: 1, ExecutorType: "rsync", Status: "failed"}
	if err := db.Create(&task1).Error; err != nil {
		t.Fatalf("创建任务 1 失败: %v", err)
	}
	if err := db.Create(&task2).Error; err != nil {
		t.Fatalf("创建任务 2 失败: %v", err)
	}

	logs := []model.TaskLog{
		{TaskID: task1.ID, Level: "info", Message: "task1-log1"},
		{TaskID: task1.ID, Level: "warn", Message: "task1-log2"},
		{TaskID: task2.ID, Level: "error", Message: "task2-log1"},
	}
	for _, one := range logs {
		if err := db.Create(&one).Error; err != nil {
			t.Fatalf("创建任务日志失败: %v", err)
		}
	}

	hub := NewHub(db, nil, false)

	events, err := hub.loadBackfillEvents(1, nil, AccessScope{})
	if err != nil {
		t.Fatalf("加载补日志失败: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("since_id=1 期望返回2条，实际: %d", len(events))
	}
	if events[0].LogID != 2 || events[1].LogID != 3 {
		t.Fatalf("补日志游标顺序错误，实际: %d, %d", events[0].LogID, events[1].LogID)
	}
	if events[0].Status != "success" || events[1].Status != "failed" {
		t.Fatalf("补日志状态映射不符合预期，实际: %q, %q", events[0].Status, events[1].Status)
	}

	taskID := task1.ID
	events, err = hub.loadBackfillEvents(0, &taskID, AccessScope{})
	if err != nil {
		t.Fatalf("按 task_id 加载补日志失败: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("task_id=1 期望返回2条，实际: %d", len(events))
	}
	if events[0].TaskID != task1.ID || events[1].TaskID != task1.ID {
		t.Fatalf("task_id 过滤不符合预期")
	}
	if events[0].Status != "success" || events[1].Status != "success" {
		t.Fatalf("按 task_id 加载补日志时应带当前任务状态，实际: %q, %q", events[0].Status, events[1].Status)
	}
}

func TestLoadBackfillEventsSanitizesLegacyMessagesWithoutMutatingRows(t *testing.T) {
	db := openHubTestDB(t)
	if err := db.AutoMigrate(&model.Task{}, &model.TaskLog{}); err != nil {
		t.Fatalf("初始化任务/任务日志表失败: %v", err)
	}

	task := model.Task{Name: "legacy-task", NodeID: 1, ExecutorType: "rsync", Status: "running"}
	otherTask := model.Task{Name: "other-task", NodeID: 1, ExecutorType: "rsync", Status: "failed"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建目标任务失败: %v", err)
	}
	if err := db.Create(&otherTask).Error; err != nil {
		t.Fatalf("创建其他任务失败: %v", err)
	}

	createdAt := time.Date(2026, 5, 24, 10, 30, 0, 123456000, time.UTC)
	cursor := model.TaskLog{TaskID: task.ID, Level: "info", Message: "cursor-log", CreatedAt: createdAt.Add(-2 * time.Minute)}
	if err := db.Create(&cursor).Error; err != nil {
		t.Fatalf("创建游标日志失败: %v", err)
	}

	rawMessage := `proxy=https://proxy.internal.example/tunnel?token=FAKE_PROXY_TOKEN_FOR_TEST_ONLY private=FAKE_PRIVATE_VALUE_FOR_TEST_ONLY path=/srv/private/source host=prod-db.internal.example command: mysqldump -h prod-db.internal.example -pFAKE_PASSWORD_FOR_TEST_ONLY; stdout: root@backup.internal.example:/backup/tenant-a token=FAKE_OUTPUT_TOKEN_FOR_TEST_ONLY`
	legacyLog := model.TaskLog{TaskID: task.ID, Level: "error", Message: rawMessage, CreatedAt: createdAt}
	if err := db.Create(&legacyLog).Error; err != nil {
		t.Fatalf("创建遗留原始日志失败: %v", err)
	}
	otherLog := model.TaskLog{TaskID: otherTask.ID, Level: "error", Message: "other-task-log", CreatedAt: createdAt.Add(time.Second)}
	if err := db.Create(&otherLog).Error; err != nil {
		t.Fatalf("创建其他任务日志失败: %v", err)
	}
	targetSafeLog := model.TaskLog{TaskID: task.ID, Level: "info", Message: "target-safe-log", CreatedAt: createdAt.Add(2 * time.Second)}
	if err := db.Create(&targetSafeLog).Error; err != nil {
		t.Fatalf("创建目标安全日志失败: %v", err)
	}

	hub := NewHub(db, nil, false)
	taskID := task.ID
	events, err := hub.loadBackfillEvents(cursor.ID, &taskID, AccessScope{})
	if err != nil {
		t.Fatalf("加载带过滤的补日志失败: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("期望返回 2 条目标任务补日志，实际: %d", len(events))
	}
	if events[0].LogID != legacyLog.ID || events[1].LogID != targetSafeLog.ID {
		t.Fatalf("补日志顺序/过滤不符合预期，实际 log_id: %d, %d", events[0].LogID, events[1].LogID)
	}
	if events[0].TaskID != task.ID || events[0].Level != "error" || events[0].Status != "running" {
		t.Fatalf("补日志元数据不符合预期: task_id=%d level=%q status=%q", events[0].TaskID, events[0].Level, events[0].Status)
	}
	if !events[0].Timestamp.Equal(createdAt) {
		t.Fatalf("补日志时间戳不符合预期，期望 %s，实际 %s", createdAt, events[0].Timestamp)
	}

	forbidden := []string{
		"proxy.internal.example",
		"FAKE_PROXY_TOKEN_FOR_TEST_ONLY",
		"FAKE_PRIVATE_VALUE_FOR_TEST_ONLY",
		"/srv/private/source",
		"prod-db.internal.example",
		"mysqldump",
		"FAKE_PASSWORD_FOR_TEST_ONLY",
		"root@backup.internal.example",
		"/backup/tenant-a",
		"FAKE_OUTPUT_TOKEN_FOR_TEST_ONLY",
	}
	for _, item := range forbidden {
		if strings.Contains(events[0].Message, item) {
			t.Fatalf("补日志消息泄露敏感片段 %q: %s", item, events[0].Message)
		}
	}
	if !strings.Contains(events[0].Message, "[命令已隐藏]") {
		t.Fatalf("期望补日志消息包含命令隐藏占位符，实际: %q", events[0].Message)
	}

	var stored model.TaskLog
	if err := db.First(&stored, legacyLog.ID).Error; err != nil {
		t.Fatalf("读取遗留日志失败: %v", err)
	}
	if stored.Message != rawMessage {
		t.Fatalf("补日志读取不应修改存储行，期望 %q，实际 %q", rawMessage, stored.Message)
	}
}

func TestLoadBackfillEventsLeavesStatusEmptyWhenTaskMissing(t *testing.T) {
	db := openHubTestDB(t)
	if err := db.AutoMigrate(&model.Task{}, &model.TaskLog{}); err != nil {
		t.Fatalf("初始化任务/任务日志表失败: %v", err)
	}

	if err := db.Create(&model.TaskLog{TaskID: 999, Level: "info", Message: "orphan-log"}).Error; err != nil {
		t.Fatalf("创建孤立任务日志失败: %v", err)
	}

	hub := NewHub(db, nil, false)
	events, err := hub.loadBackfillEvents(0, nil, AccessScope{})
	if err != nil {
		t.Fatalf("加载孤立补日志失败: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("期望返回1条补日志，实际: %d", len(events))
	}
	if events[0].Status != "" {
		t.Fatalf("缺失任务映射时 status 应为空，实际: %q", events[0].Status)
	}
}

func TestLoadBackfillEventsHidesMissingTaskLogsForViewer(t *testing.T) {
	db := openHubTestDB(t)
	if err := db.AutoMigrate(&model.Task{}, &model.TaskLog{}); err != nil {
		t.Fatalf("初始化任务/任务日志表失败: %v", err)
	}

	if err := db.Create(&model.TaskLog{TaskID: 999, Level: "info", Message: "orphan-log"}).Error; err != nil {
		t.Fatalf("创建孤立任务日志失败: %v", err)
	}

	hub := NewHub(db, nil, false)
	events, err := hub.loadBackfillEvents(0, nil, AccessScope{Role: "viewer"})
	if err != nil {
		t.Fatalf("加载 viewer 补日志失败: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("viewer 不应看到孤立补日志，实际: %d", len(events))
	}
}

func TestLoadBackfillEventsAllowsViewerToSeeExistingTaskLogs(t *testing.T) {
	db := openHubTestDB(t)
	if err := db.AutoMigrate(&model.Task{}, &model.TaskLog{}); err != nil {
		t.Fatalf("初始化任务/任务日志表失败: %v", err)
	}

	task := model.Task{Name: "visible-task", NodeID: 1, ExecutorType: "rsync", Status: "success"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := db.Create(&model.TaskLog{TaskID: task.ID, Level: "info", Message: "visible-log"}).Error; err != nil {
		t.Fatalf("创建任务日志失败: %v", err)
	}
	// 孤立日志：不应出现在 viewer 结果中
	if err := db.Create(&model.TaskLog{TaskID: 9999, Level: "info", Message: "orphan-log"}).Error; err != nil {
		t.Fatalf("创建孤立日志失败: %v", err)
	}

	hub := NewHub(db, nil, false)
	events, err := hub.loadBackfillEvents(0, nil, AccessScope{Role: "viewer"})
	if err != nil {
		t.Fatalf("加载 viewer 补日志失败: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("viewer 应看到 1 条存活任务日志，实际: %d", len(events))
	}
	if events[0].TaskID != task.ID {
		t.Fatalf("viewer 日志 task_id 不符，期望 %d，实际 %d", task.ID, events[0].TaskID)
	}
}

func TestLoadBackfillEventsAppliesTaskAccessChecker(t *testing.T) {
	db := openHubTestDB(t)
	if err := db.AutoMigrate(&model.Task{}, &model.TaskLog{}); err != nil {
		t.Fatalf("初始化任务/任务日志表失败: %v", err)
	}

	task1 := model.Task{Name: "task-1", NodeID: 1, ExecutorType: "rsync", Status: "success"}
	task2 := model.Task{Name: "task-2", NodeID: 2, ExecutorType: "rsync", Status: "failed"}
	if err := db.Create(&task1).Error; err != nil {
		t.Fatalf("创建任务 1 失败: %v", err)
	}
	if err := db.Create(&task2).Error; err != nil {
		t.Fatalf("创建任务 2 失败: %v", err)
	}

	for _, one := range []model.TaskLog{
		{TaskID: task1.ID, Level: "info", Message: "task1-log"},
		{TaskID: task2.ID, Level: "warn", Message: "task2-log"},
	} {
		if err := db.Create(&one).Error; err != nil {
			t.Fatalf("创建任务日志失败: %v", err)
		}
	}

	hub := NewHub(db, nil, false)
	events, err := hub.loadBackfillEvents(0, nil, AccessScope{
		Role: "operator",
		AllowedNodeIDs: map[uint]struct{}{
			task1.NodeID: {},
		},
	})
	if err != nil {
		t.Fatalf("带权限检查器加载补日志失败: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("期望仅返回 1 条可访问日志，实际: %d", len(events))
	}
	if events[0].TaskID != task1.ID {
		t.Fatalf("补日志权限过滤失效，实际 task_id=%d", events[0].TaskID)
	}
}

func TestHubCheckOriginRejectsEmptyOriginByDefault(t *testing.T) {
	hub := NewHub(nil, []string{"https://xirang.example.com"}, false)
	upgrader := hub.newUpgrader()

	req := httptest.NewRequest("GET", "http://localhost/ws", nil)
	if upgrader.CheckOrigin(req) {
		t.Fatalf("默认配置下空 Origin 应被拒绝")
	}

	req = httptest.NewRequest("GET", "http://localhost/ws", nil)
	req.Header.Set("Origin", "https://xirang.example.com")
	if !upgrader.CheckOrigin(req) {
		t.Fatalf("匹配白名单 Origin 应允许")
	}
}

func TestLoadBackfillEventsFiltersOperatorScope(t *testing.T) {
	db := openHubTestDB(t)
	if err := db.AutoMigrate(&model.Task{}, &model.TaskLog{}); err != nil {
		t.Fatalf("初始化任务/任务日志表失败: %v", err)
	}

	task1 := model.Task{Name: "task-1", NodeID: 1, ExecutorType: "rsync", Status: "running"}
	task2 := model.Task{Name: "task-2", NodeID: 2, ExecutorType: "rsync", Status: "running"}
	if err := db.Create(&task1).Error; err != nil {
		t.Fatalf("创建任务 1 失败: %v", err)
	}
	if err := db.Create(&task2).Error; err != nil {
		t.Fatalf("创建任务 2 失败: %v", err)
	}
	if err := db.Create(&model.TaskLog{TaskID: task1.ID, Level: "info", Message: "task1"}).Error; err != nil {
		t.Fatalf("创建 task1 日志失败: %v", err)
	}
	if err := db.Create(&model.TaskLog{TaskID: task2.ID, Level: "info", Message: "task2"}).Error; err != nil {
		t.Fatalf("创建 task2 日志失败: %v", err)
	}

	hub := NewHub(db, nil, false)
	events, err := hub.loadBackfillEvents(0, nil, AccessScope{
		Role: "operator",
		AllowedNodeIDs: map[uint]struct{}{
			1: {},
		},
	})
	if err != nil {
		t.Fatalf("加载带 ownership 过滤的补日志失败: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("operator 仅应看到所属节点日志，实际返回 %d 条", len(events))
	}
	if events[0].TaskID != task1.ID {
		t.Fatalf("operator 不应看到未授权任务日志，实际 task_id=%d", events[0].TaskID)
	}
}

func TestClientCanAccessTaskUsesOperatorScope(t *testing.T) {
	db := openHubTestDB(t)
	if err := db.AutoMigrate(&model.Task{}); err != nil {
		t.Fatalf("初始化任务表失败: %v", err)
	}

	task1 := model.Task{Name: "task-1", NodeID: 1, ExecutorType: "rsync", Status: "running"}
	task2 := model.Task{Name: "task-2", NodeID: 2, ExecutorType: "rsync", Status: "running"}
	if err := db.Create(&task1).Error; err != nil {
		t.Fatalf("创建任务 1 失败: %v", err)
	}
	if err := db.Create(&task2).Error; err != nil {
		t.Fatalf("创建任务 2 失败: %v", err)
	}

	hub := NewHub(db, nil, false)
	cl := &client{
		access: AccessScope{
			Role: "operator",
			AllowedNodeIDs: map[uint]struct{}{
				1: {},
			},
		},
		taskAccess: make(map[uint]taskAccessEntry),
	}

	if !hub.clientCanAccessTask(cl, task1.ID) {
		t.Fatalf("operator 应可访问所属节点任务")
	}
	if hub.clientCanAccessTask(cl, task2.ID) {
		t.Fatalf("operator 不应可访问未授权节点任务")
	}
}

func TestClientCanAccessTaskRechecksExpiredCache(t *testing.T) {
	db := openHubTestDB(t)
	if err := db.AutoMigrate(&model.Task{}); err != nil {
		t.Fatalf("初始化任务表失败: %v", err)
	}

	task := model.Task{Name: "task-ttl", NodeID: 1, ExecutorType: "rsync", Status: "running"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	hub := NewHub(db, nil, false)
	cl := &client{
		access: AccessScope{
			Role: "operator",
			AllowedNodeIDs: map[uint]struct{}{
				1: {},
			},
		},
		taskAccess: make(map[uint]taskAccessEntry),
	}

	if !hub.clientCanAccessTask(cl, task.ID) {
		t.Fatalf("初次检查应允许访问")
	}

	if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Update("node_id", 2).Error; err != nil {
		t.Fatalf("更新任务节点失败: %v", err)
	}

	cl.taskAccessMu.Lock()
	entry := cl.taskAccess[task.ID]
	entry.expiresAt = time.Now().Add(-time.Second)
	cl.taskAccess[task.ID] = entry
	cl.taskAccessMu.Unlock()

	if hub.clientCanAccessTask(cl, task.ID) {
		t.Fatalf("缓存过期后应重新鉴权并拒绝访问")
	}
}

func TestHubCheckOriginAllowsEmptyOriginWhenEnabled(t *testing.T) {
	hub := NewHub(nil, []string{"https://xirang.example.com"}, true)
	upgrader := hub.newUpgrader()

	req := httptest.NewRequest("GET", "http://localhost/ws", nil)
	if !upgrader.CheckOrigin(req) {
		t.Fatalf("开启 WS_ALLOW_EMPTY_ORIGIN 后应允许空 Origin")
	}
}

func TestHubCheckOriginAllowsSameHostDifferentPort(t *testing.T) {
	hub := NewHub(nil, nil, false)
	upgrader := hub.newUpgrader()

	req := httptest.NewRequest("GET", "http://192.168.1.20:8080/ws", nil)
	req.Header.Set("Origin", "http://192.168.1.20:5173")
	if !upgrader.CheckOrigin(req) {
		t.Fatalf("同主机跨端口 Origin 应允许")
	}
}

func TestHubCheckOriginRejectsInvalidOrigin(t *testing.T) {
	hub := NewHub(nil, nil, false)
	upgrader := hub.newUpgrader()

	req := httptest.NewRequest("GET", "http://192.168.1.20:8080/ws", nil)
	req.Header.Set("Origin", "null")
	if upgrader.CheckOrigin(req) {
		t.Fatalf("非法 Origin 不应放行")
	}
}

func TestHubCheckOriginRejectsDifferentHost(t *testing.T) {
	hub := NewHub(nil, nil, false)
	upgrader := hub.newUpgrader()

	req := httptest.NewRequest("GET", "http://192.168.1.20:8080/ws", nil)
	req.Header.Set("Origin", "http://evil.com:5173")
	if upgrader.CheckOrigin(req) {
		t.Fatalf("不同主机 Origin 不应放行")
	}
}

func openHubTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}
