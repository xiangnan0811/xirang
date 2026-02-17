package ws

import (
	"fmt"
	"strings"
	"testing"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLoadBackfillEventsBySinceIDAndTaskID(t *testing.T) {
	db := openHubTestDB(t)
	if err := db.AutoMigrate(&model.TaskLog{}); err != nil {
		t.Fatalf("初始化任务日志表失败: %v", err)
	}

	logs := []model.TaskLog{
		{TaskID: 1, Level: "info", Message: "task1-log1"},
		{TaskID: 1, Level: "warn", Message: "task1-log2"},
		{TaskID: 2, Level: "error", Message: "task2-log1"},
	}
	for _, one := range logs {
		if err := db.Create(&one).Error; err != nil {
			t.Fatalf("创建任务日志失败: %v", err)
		}
	}

	hub := NewHub(db, nil)

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

	taskID := uint(1)
	events, err = hub.loadBackfillEvents(0, &taskID)
	if err != nil {
		t.Fatalf("按 task_id 加载补日志失败: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("task_id=1 期望返回2条，实际: %d", len(events))
	}
	if events[0].TaskID != 1 || events[1].TaskID != 1 {
		t.Fatalf("task_id 过滤不符合预期")
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
