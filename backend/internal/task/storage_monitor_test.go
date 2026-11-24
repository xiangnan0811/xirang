package task

import (
	"strconv"
	"strings"
	"syscall"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

// openStorageMonitorTestDB 扩展 openManagerTestDB，额外迁移 alerting 依赖的表。
func openStorageMonitorTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openManagerTestDB(t)
	if err := db.AutoMigrate(&model.Integration{}, &model.AlertDelivery{}, &model.SystemSetting{}); err != nil {
		t.Fatalf("迁移 alerting/settings 相关表失败: %v", err)
	}
	return db
}

func TestCheckLocalStorageSpace_NoLocalPolicies(t *testing.T) {
	db := openStorageMonitorTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, m)

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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, m)

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
	t.Setenv("BACKUP_STORAGE_MIN_FREE_GB", strconv.Itoa(triggerThreshold))
	t.Setenv("BACKUP_STORAGE_MAX_USAGE_PCT", "100")
	t.Setenv("ALERT_DEDUP_WINDOW", "0")

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

func TestCheckLocalStorageSpace_SettingsOverrideActivatesAndResolves(t *testing.T) {
	db := openStorageMonitorTestDB(t)
	settingsSvc := settings.NewService(db)
	tmpDir := t.TempDir()

	policy := model.Policy{
		Name:       "settings-local-policy",
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

	t.Setenv("BACKUP_STORAGE_MIN_FREE_GB", "0")
	t.Setenv("BACKUP_STORAGE_MAX_USAGE_PCT", "100")
	t.Setenv("ALERT_DEDUP_WINDOW", "0")
	if err := settingsSvc.Update("storage.min_free_gb", strconv.Itoa(triggerThreshold)); err != nil {
		t.Fatalf("设置最小剩余空间覆盖值失败: %v", err)
	}

	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, settingsSvc, nil, 8, 90)
	shutdownManagerOnCleanup(t, m)

	m.checkLocalStorageSpace()

	errorCode := "XR-STORAGE-LOW:" + sanitizeTaskLogMessage(tmpDir)
	var alert model.Alert
	if err := db.Where("error_code = ?", errorCode).First(&alert).Error; err != nil {
		t.Fatalf("设置覆盖值未产生存储空间告警: %v", err)
	}
	if alert.Status != "open" {
		t.Fatalf("存储空间告警状态=%q，期望 open", alert.Status)
	}

	if err := settingsSvc.Update("storage.min_free_gb", "0"); err != nil {
		t.Fatalf("清除告警阈值覆盖值失败: %v", err)
	}
	m.checkLocalStorageSpace()

	if err := db.Where("error_code = ?", errorCode).First(&alert).Error; err != nil {
		t.Fatalf("查询存储空间告警失败: %v", err)
	}
	if alert.Status != "resolved" {
		t.Fatalf("恢复空间阈值后告警状态=%q，期望 resolved", alert.Status)
	}
}

func TestCheckLocalStorageSpace_HighThresholdNoAlert(t *testing.T) {
	db := openStorageMonitorTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, m)

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
	t.Setenv("BACKUP_STORAGE_MIN_FREE_GB", "0")
	t.Setenv("BACKUP_STORAGE_MAX_USAGE_PCT", "100")
	t.Setenv("ALERT_DEDUP_WINDOW", "0")

	m.checkLocalStorageSpace()

	var count int64
	db.Model(&model.Alert{}).Count(&count)
	if count != 0 {
		t.Fatalf("高阈值不应产生告警，实际告警数: %d", count)
	}
}

func TestCheckLocalStorageSpace_NonexistentPath(t *testing.T) {
	db := openStorageMonitorTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, m)

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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, m)

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
	t.Setenv("BACKUP_STORAGE_MIN_FREE_GB", "0")
	t.Setenv("BACKUP_STORAGE_MAX_USAGE_PCT", "1")
	t.Setenv("ALERT_DEDUP_WINDOW", "0")

	var beforeCount int64
	db.Model(&model.Alert{}).Count(&beforeCount)

	m.checkLocalStorageSpace()

	var afterCount int64
	db.Model(&model.Alert{}).Count(&afterCount)
	if afterCount != beforeCount {
		t.Fatalf("禁用策略不应产生新告警，调用前: %d，调用后: %d", beforeCount, afterCount)
	}
}

func TestCheckLocalStorageSpace_TemplatePolicySkipped(t *testing.T) {
	db := openStorageMonitorTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, m)

	tmpDir := t.TempDir()
	policy := model.Policy{
		Name:       "enabled-template-policy",
		SourcePath: "/data/src",
		TargetPath: tmpDir,
		CronSpec:   "@daily",
		Enabled:    true,
		IsTemplate: true,
	}
	if err := model.CreatePolicyWithExplicitValues(db, &policy, "enabled", "is_template"); err != nil {
		t.Fatalf("创建模板策略失败: %v", err)
	}

	t.Setenv("BACKUP_STORAGE_MIN_FREE_GB", "0")
	t.Setenv("BACKUP_STORAGE_MAX_USAGE_PCT", "1")
	t.Setenv("ALERT_DEDUP_WINDOW", "0")

	m.checkLocalStorageSpace()

	var count int64
	if err := db.Model(&model.Alert{}).Count(&count).Error; err != nil {
		t.Fatalf("查询告警失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("模板策略不应触发存储告警，实际告警数: %d", count)
	}
}

func TestCheckLocalStorageSpace_PreservesAmbiguousHistoricalAlertsWhenTemplatesExist(t *testing.T) {
	t.Run("preserves open alert from deleted regular policy even when template shares sanitized code", func(t *testing.T) {
		db := openStorageMonitorTestDB(t)
		m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
		shutdownManagerOnCleanup(t, m)

		// 模拟普通策略触发告警后被删除，仅剩内置模板策略存在。
		templateDir := t.TempDir()
		templatePolicy := model.Policy{
			Name: "template-only-path", SourcePath: "/src/1", TargetPath: templateDir,
			CronSpec: "@daily", Enabled: false, IsTemplate: true,
		}
		if err := model.CreatePolicyWithExplicitValues(db, &templatePolicy, "enabled", "is_template"); err != nil {
			t.Fatalf("创建模板策略失败: %v", err)
		}

		const originalMsg = "已删除普通策略触发的存储空间告警"
		deletedRegularSanitizedAlert := model.Alert{
			Severity:  "warning",
			Status:    "open",
			ErrorCode: "XR-STORAGE-LOW:" + sanitizeTaskLogMessage(templateDir),
			Message:   originalMsg,
		}
		deletedRegularRawAlert := model.Alert{
			Severity:  "warning",
			Status:    "open",
			ErrorCode: "XR-STORAGE-LOW:" + templateDir,
			Message:   originalMsg,
		}
		for _, alert := range []*model.Alert{&deletedRegularSanitizedAlert, &deletedRegularRawAlert} {
			if err := db.Create(alert).Error; err != nil {
				t.Fatalf("创建历史告警失败: %v", err)
			}
		}

		t.Setenv("BACKUP_STORAGE_MIN_FREE_GB", "999999")
		t.Setenv("BACKUP_STORAGE_MAX_USAGE_PCT", "100")
		t.Setenv("ALERT_DEDUP_WINDOW", "0")

		m.checkLocalStorageSpace()

		for _, id := range []uint{deletedRegularSanitizedAlert.ID, deletedRegularRawAlert.ID} {
			var got model.Alert
			if err := db.First(&got, id).Error; err != nil {
				t.Fatalf("读取历史告警失败: %v", err)
			}
			if got.Status != "open" || got.Message != originalMsg {
				t.Fatalf("缺少来源依据时不得猜测关闭历史存储告警或覆盖正文: id=%d status=%s msg=%q", id, got.Status, got.Message)
			}
		}
	})

	t.Run("preserves disabled regular policy alert when template shares sanitized code", func(t *testing.T) {
		db := openStorageMonitorTestDB(t)
		m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
		shutdownManagerOnCleanup(t, m)

		disabledRegularDir := t.TempDir()
		templateDir := t.TempDir()
		disabledRegular := model.Policy{
			Name: "disabled-regular", SourcePath: "/src/reg", TargetPath: disabledRegularDir,
			CronSpec: "@daily", Enabled: false, IsTemplate: false,
		}
		templatePolicy := model.Policy{
			Name: "template-sibling", SourcePath: "/src/tmpl", TargetPath: templateDir,
			CronSpec: "@daily", Enabled: false, IsTemplate: true,
		}
		for _, p := range []*model.Policy{&disabledRegular, &templatePolicy} {
			if err := model.CreatePolicyWithExplicitValues(db, p, "enabled", "is_template"); err != nil {
				t.Fatalf("创建策略失败: %v", err)
			}
		}

		const originalMsg = "普通策略触发的存储空间告警"
		regularAlert := model.Alert{
			Severity:  "warning",
			Status:    "open",
			ErrorCode: "XR-STORAGE-LOW:" + sanitizeTaskLogMessage(disabledRegularDir),
			Message:   originalMsg,
		}
		if err := db.Create(&regularAlert).Error; err != nil {
			t.Fatalf("创建普通策略告警失败: %v", err)
		}

		t.Setenv("BACKUP_STORAGE_MIN_FREE_GB", "999999")
		t.Setenv("BACKUP_STORAGE_MAX_USAGE_PCT", "100")
		t.Setenv("ALERT_DEDUP_WINDOW", "0")

		m.checkLocalStorageSpace()

		var got model.Alert
		if err := db.First(&got, regularAlert.ID).Error; err != nil {
			t.Fatalf("读取告警失败: %v", err)
		}
		if got.Status != "open" || got.Message != originalMsg {
			t.Fatalf("不得误关已禁用普通策略的告警或覆盖正文: status=%s msg=%q", got.Status, got.Message)
		}
	})

	t.Run("resolves shared sanitized code once active regular local policy recovers", func(t *testing.T) {
		db := openStorageMonitorTestDB(t)
		m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
		shutdownManagerOnCleanup(t, m)

		activeDir := t.TempDir()
		templateDir := t.TempDir()
		activePolicy := model.Policy{
			Name: "active-healthy", SourcePath: "/src/a", TargetPath: activeDir,
			CronSpec: "@daily", Enabled: true, IsTemplate: false,
		}
		templatePolicy := model.Policy{
			Name: "template-sibling", SourcePath: "/src/t", TargetPath: templateDir,
			CronSpec: "@daily", Enabled: false, IsTemplate: true,
		}
		for _, p := range []*model.Policy{&activePolicy, &templatePolicy} {
			if err := model.CreatePolicyWithExplicitValues(db, p, "enabled", "is_template"); err != nil {
				t.Fatalf("创建策略失败: %v", err)
			}
		}

		openAlert := model.Alert{
			Severity:  "warning",
			Status:    "open",
			ErrorCode: "XR-STORAGE-LOW:" + sanitizeTaskLogMessage(activeDir),
			Message:   "旧存储空间告警",
		}
		if err := db.Create(&openAlert).Error; err != nil {
			t.Fatalf("创建告警失败: %v", err)
		}

		// 活动策略空间恢复正常后，正常走恢复逻辑解除告警。
		t.Setenv("BACKUP_STORAGE_MIN_FREE_GB", "0")
		t.Setenv("BACKUP_STORAGE_MAX_USAGE_PCT", "100")
		t.Setenv("ALERT_DEDUP_WINDOW", "0")

		m.checkLocalStorageSpace()

		var got model.Alert
		if err := db.First(&got, openAlert.ID).Error; err != nil {
			t.Fatalf("读取告警失败: %v", err)
		}
		if got.Status != "resolved" {
			t.Fatalf("活动策略空间恢复后告警应自动 resolved，实际: %s", got.Status)
		}
	})
}
