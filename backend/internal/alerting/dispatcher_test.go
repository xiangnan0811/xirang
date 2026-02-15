package alerting

import (
	"fmt"
	"strings"
	"testing"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRaiseTaskFailureDedupWindow(t *testing.T) {
	t.Setenv("ALERT_DEDUP_WINDOW", "15m")

	db := openAlertingTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.AlertDelivery{}, &model.Integration{}); err != nil {
		t.Fatalf("初始化告警表失败: %v", err)
	}

	task := model.Task{
		ID:     1,
		NodeID: 2,
		Node: model.Node{
			Name: "node-a",
		},
	}

	if err := RaiseTaskFailure(db, task, "执行失败"); err != nil {
		t.Fatalf("首次创建告警失败: %v", err)
	}
	if err := RaiseTaskFailure(db, task, "执行失败-重复"); err != nil {
		t.Fatalf("重复创建告警失败: %v", err)
	}

	var count int64
	if err := db.Model(&model.Alert{}).Count(&count).Error; err != nil {
		t.Fatalf("统计告警数量失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("去重窗口内应仅保留1条告警，实际: %d", count)
	}
}

func openAlertingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}
