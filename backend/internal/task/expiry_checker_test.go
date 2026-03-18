package task

import (
	"context"
	"fmt"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// openExpiryTestDB 在 openManagerTestDB 基础上追加 Integration 和 AlertDelivery 表，
// 以满足 raiseAndDispatch 内部查询需要。
func openExpiryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openManagerTestDB(t)
	if err := db.AutoMigrate(&model.Integration{}, &model.AlertDelivery{}); err != nil {
		t.Fatalf("追加迁移 Integration/AlertDelivery 失败: %v", err)
	}
	return db
}

func createExpiryNode(t *testing.T, db *gorm.DB, name string, expiryDate *time.Time, archived bool) model.Node {
	t.Helper()
	node := model.Node{
		Name:       name,
		Host:       "127.0.0.1",
		Port:       22,
		Username:   "root",
		AuthType:   "key",
		ExpiryDate: expiryDate,
		Archived:   archived,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建测试节点失败: %v", err)
	}
	return node
}

func TestCheckNodeExpiry_ExpiredNode(t *testing.T) {
	t.Setenv("ALERT_DEDUP_WINDOW", "0")

	db := openExpiryTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

	expiry := time.Now().Add(-2 * time.Hour)
	node := createExpiryNode(t, db, "node-expired", &expiry, false)

	m.checkNodeExpiry()

	// 断言：节点已被归档
	var updated model.Node
	if err := db.First(&updated, node.ID).Error; err != nil {
		t.Fatalf("查询节点失败: %v", err)
	}
	if !updated.Archived {
		t.Fatalf("期望过期节点被归档（Archived=true），实际 Archived=%v", updated.Archived)
	}
}

func TestCheckNodeExpiry_OneDayWarning(t *testing.T) {
	t.Setenv("ALERT_DEDUP_WINDOW", "0")

	db := openExpiryTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

	expiry := time.Now().Add(12 * time.Hour)
	node := createExpiryNode(t, db, "node-1day", &expiry, false)

	// 创建关联的备份任务（source=policy, executor_type=rsync, status=pending）
	taskEntity := model.Task{
		Name:         "task-emergency-backup",
		NodeID:       node.ID,
		ExecutorType: "rsync",
		Source:       "policy",
		Status:       string(StatusPending),
		RsyncSource:  "/tmp/src",
		RsyncTarget:  "/tmp/dst",
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建测试任务失败: %v", err)
	}

	m.checkNodeExpiry()

	// 等待 TriggerManual 启动的 goroutine 完成
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = m.Shutdown(shutdownCtx)

	// 断言：创建了包含 XR-NODE-EXPIRY 的告警
	var alerts []model.Alert
	if err := db.Where("error_code LIKE ?", "XR-NODE-EXPIRY%").Find(&alerts).Error; err != nil {
		t.Fatalf("查询告警失败: %v", err)
	}
	if len(alerts) == 0 {
		t.Fatalf("期望创建 XR-NODE-EXPIRY 告警，实际未找到")
	}

	found := false
	for _, a := range alerts {
		if a.ErrorCode == fmt.Sprintf("XR-NODE-EXPIRY-%d", node.ID) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("期望找到 error_code=XR-NODE-EXPIRY-%d 的告警", node.ID)
	}
}

func TestCheckNodeExpiry_ThreeDayWarning(t *testing.T) {
	t.Setenv("ALERT_DEDUP_WINDOW", "0")

	db := openExpiryTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

	expiry := time.Now().Add(2 * 24 * time.Hour)
	node := createExpiryNode(t, db, "node-3day", &expiry, false)

	m.checkNodeExpiry()

	// 断言：创建了 XR-NODE-EXPIRY 告警
	var count int64
	if err := db.Model(&model.Alert{}).
		Where("error_code = ?", fmt.Sprintf("XR-NODE-EXPIRY-%d", node.ID)).
		Count(&count).Error; err != nil {
		t.Fatalf("查询告警失败: %v", err)
	}
	if count == 0 {
		t.Fatalf("期望创建 XR-NODE-EXPIRY-%d 告警，实际未找到", node.ID)
	}
}

func TestCheckNodeExpiry_FarFuture(t *testing.T) {
	t.Setenv("ALERT_DEDUP_WINDOW", "0")

	db := openExpiryTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

	expiry := time.Now().Add(30 * 24 * time.Hour)
	node := createExpiryNode(t, db, "node-far", &expiry, false)

	m.checkNodeExpiry()

	// 断言：没有创建告警
	var count int64
	if err := db.Model(&model.Alert{}).
		Where("error_code = ?", fmt.Sprintf("XR-NODE-EXPIRY-%d", node.ID)).
		Count(&count).Error; err != nil {
		t.Fatalf("查询告警失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("期望远期节点不产生告警，实际找到 %d 条", count)
	}

	// 断言：节点未被归档
	var updated model.Node
	if err := db.First(&updated, node.ID).Error; err != nil {
		t.Fatalf("查询节点失败: %v", err)
	}
	if updated.Archived {
		t.Fatalf("期望远期节点未被归档，实际 Archived=true")
	}
}

func TestCheckNodeExpiry_AlreadyArchived(t *testing.T) {
	t.Setenv("ALERT_DEDUP_WINDOW", "0")

	db := openExpiryTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

	expiry := time.Now().Add(-2 * time.Hour)
	_ = createExpiryNode(t, db, "node-already-archived", &expiry, true)

	m.checkNodeExpiry()

	// 断言：没有创建告警（查询已过滤 archived=true 的节点）
	var count int64
	if err := db.Model(&model.Alert{}).Count(&count).Error; err != nil {
		t.Fatalf("查询告警失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("期望已归档节点不产生告警，实际找到 %d 条", count)
	}
}
