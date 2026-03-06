package ws

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

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

	events, err := hub.loadBackfillEvents(1, nil)
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
	events, err = hub.loadBackfillEvents(0, &taskID)
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

func TestLoadBackfillEventsLeavesStatusEmptyWhenTaskMissing(t *testing.T) {
	db := openHubTestDB(t)
	if err := db.AutoMigrate(&model.Task{}, &model.TaskLog{}); err != nil {
		t.Fatalf("初始化任务/任务日志表失败: %v", err)
	}

	if err := db.Create(&model.TaskLog{TaskID: 999, Level: "info", Message: "orphan-log"}).Error; err != nil {
		t.Fatalf("创建孤立任务日志失败: %v", err)
	}

	hub := NewHub(db, nil, false)
	events, err := hub.loadBackfillEvents(0, nil)
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
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}
