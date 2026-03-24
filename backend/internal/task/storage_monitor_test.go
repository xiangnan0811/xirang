package task

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// openStorageMonitorTestDB 扩展 openManagerTestDB，额外迁移 alerting 依赖的表。
func openStorageMonitorTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openManagerTestDB(t)
	if err := db.AutoMigrate(&model.Integration{}, &model.AlertDelivery{}); err != nil {
		t.Fatalf("迁移 alerting 相关表失败: %v", err)
	}
	return db
}

func TestCheckLocalStorageSpace_NoLocalPolicies(t *testing.T) {
	db := openStorageMonitorTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

	// 远程路径（含 ":"），应被跳过
	policy := model.Policy{
		Name:       "remote-policy",
		SourcePath: "/data/src",
		TargetPath: "remote:bucket/backup",
		CronSpec:   "@daily",
		Enabled:    true,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	m.checkLocalStorageSpace()

	var count int64
	db.Model(&model.Alert{}).Count(&count)
	if count != 0 {
		t.Fatalf("远程路径不应产生告警，实际告警数: %d", count)
	}
}

func TestCheckLocalStorageSpace_ValidLocalPath(t *testing.T) {
	db := openStorageMonitorTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

	tmpDir := t.TempDir()

	policy := model.Policy{
		Name:       "local-policy",
		SourcePath: "/data/src",
		TargetPath: tmpDir,
		CronSpec:   "@daily",
		Enabled:    true,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(tmpDir, &stat); err != nil {
		t.Fatalf("获取测试目录磁盘信息失败: %v", err)
	}
	freeGB := float64(stat.Bavail*uint64(stat.Bsize)) / (1024 * 1024 * 1024)
	triggerThreshold := int(freeGB) + 1

	// 通过提升最小剩余空间阈值来稳定触发告警，避免依赖宿主机实际使用率。
	os.Setenv("BACKUP_STORAGE_MIN_FREE_GB", strconv.Itoa(triggerThreshold))
	defer os.Unsetenv("BACKUP_STORAGE_MIN_FREE_GB")
	os.Setenv("BACKUP_STORAGE_MAX_USAGE_PCT", "100")
	defer os.Unsetenv("BACKUP_STORAGE_MAX_USAGE_PCT")
	os.Setenv("ALERT_DEDUP_WINDOW", "0")
	defer os.Unsetenv("ALERT_DEDUP_WINDOW")

	m.checkLocalStorageSpace()

	var alerts []model.Alert
	db.Find(&alerts)
	if len(alerts) != 1 {
		t.Fatalf("期望产生 1 条告警，实际: %d", len(alerts))
	}
	if !strings.HasPrefix(alerts[0].ErrorCode, "XR-STORAGE-LOW:") {
		t.Fatalf("告警错误码期望 XR-STORAGE-LOW，实际: %s", alerts[0].ErrorCode)
	}
}

func TestCheckLocalStorageSpace_HighThresholdNoAlert(t *testing.T) {
	db := openStorageMonitorTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

	tmpDir := t.TempDir()

	policy := model.Policy{
		Name:       "local-policy-high",
		SourcePath: "/data/src",
		TargetPath: tmpDir,
		CronSpec:   "@daily",
		Enabled:    true,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	// minFreeGB=0 不触发空间不足；maxUsagePct=100 永远不触发
	os.Setenv("BACKUP_STORAGE_MIN_FREE_GB", "0")
	defer os.Unsetenv("BACKUP_STORAGE_MIN_FREE_GB")
	os.Setenv("BACKUP_STORAGE_MAX_USAGE_PCT", "100")
	defer os.Unsetenv("BACKUP_STORAGE_MAX_USAGE_PCT")
	os.Setenv("ALERT_DEDUP_WINDOW", "0")
	defer os.Unsetenv("ALERT_DEDUP_WINDOW")

	m.checkLocalStorageSpace()

	var count int64
	db.Model(&model.Alert{}).Count(&count)
	if count != 0 {
		t.Fatalf("高阈值不应产生告警，实际告警数: %d", count)
	}
}

func TestCheckLocalStorageSpace_NonexistentPath(t *testing.T) {
	db := openStorageMonitorTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

	policy := model.Policy{
		Name:       "bad-path-policy",
		SourcePath: "/data/src",
		TargetPath: "/nonexistent/path/xirang-test-12345",
		CronSpec:   "@daily",
		Enabled:    true,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	// 不应 panic，不应产生告警
	m.checkLocalStorageSpace()

	var count int64
	db.Model(&model.Alert{}).Count(&count)
	if count != 0 {
		t.Fatalf("不存在的路径不应产生告警，实际告警数: %d", count)
	}
}

func TestCheckLocalStorageSpace_DisabledPolicySkipped(t *testing.T) {
	db := openStorageMonitorTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

	tmpDir := t.TempDir()

	policy := model.Policy{
		Name:       "disabled-policy",
		SourcePath: "/data/src",
		TargetPath: tmpDir,
		CronSpec:   "@daily",
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	// GORM default:true 会忽略 Enabled=false 的零值，需显式更新
	if err := db.Model(&policy).Update("enabled", false).Error; err != nil {
		t.Fatalf("禁用策略失败: %v", err)
	}

	// 低阈值（1%）本应触发告警，但因策略已禁用所以不会
	os.Setenv("BACKUP_STORAGE_MIN_FREE_GB", "0")
	defer os.Unsetenv("BACKUP_STORAGE_MIN_FREE_GB")
	os.Setenv("BACKUP_STORAGE_MAX_USAGE_PCT", "1")
	defer os.Unsetenv("BACKUP_STORAGE_MAX_USAGE_PCT")
	os.Setenv("ALERT_DEDUP_WINDOW", "0")
	defer os.Unsetenv("ALERT_DEDUP_WINDOW")

	var beforeCount int64
	db.Model(&model.Alert{}).Count(&beforeCount)

	m.checkLocalStorageSpace()

	var afterCount int64
	db.Model(&model.Alert{}).Count(&afterCount)
	if afterCount != beforeCount {
		t.Fatalf("禁用策略不应产生新告警，调用前: %d，调用后: %d", beforeCount, afterCount)
	}
}
