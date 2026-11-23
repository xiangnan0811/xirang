package bootstrap

import (
	"testing"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestSeedPolicyTemplatesPersistsDisabledTemplatesWithDefaults(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	db := openBootstrapTestDB(t)
	if err := db.AutoMigrate(&model.Policy{}); err != nil {
		t.Fatalf("初始化策略表失败: %v", err)
	}

	SeedPolicyTemplates(db)

	var templates []model.Policy
	if err := db.Order("id asc").Find(&templates).Error; err != nil {
		t.Fatalf("查询策略模板失败: %v", err)
	}
	if len(templates) != 4 {
		t.Fatalf("期望创建 4 个策略模板，实际: %d", len(templates))
	}
	for _, tmpl := range templates {
		if !tmpl.IsTemplate {
			t.Fatalf("模板 %q is_template 应为 true", tmpl.Name)
		}
		if tmpl.Enabled {
			t.Fatalf("模板 %q enabled 应持久化为 false，实际为 true", tmpl.Name)
		}
		if tmpl.RetentionMode != "simple" || !tmpl.VerifyEnabled || tmpl.HookTimeoutSeconds != 300 ||
			tmpl.MaxRetries != 2 || tmpl.RetryBaseSeconds != 30 ||
			tmpl.DrillRestorePath != "/tmp/xirang-drill" || !tmpl.DrillAutoCleanup {
			t.Fatalf("模板 %q 默认字段未保留: %+v", tmpl.Name, tmpl)
		}
	}

	// 再次执行应保持幂等，不重复创建。
	SeedPolicyTemplates(db)
	var total int64
	if err := db.Model(&model.Policy{}).Count(&total).Error; err != nil {
		t.Fatalf("统计策略总数失败: %v", err)
	}
	if total != 4 {
		t.Fatalf("重复执行后策略数量应保持 4，实际: %d", total)
	}
}

func TestSeedPolicyTemplatesRepairsIncorrectlyEnabledTemplatesOnly(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	db := openBootstrapTestDB(t)
	if err := db.AutoMigrate(&model.Policy{}); err != nil {
		t.Fatalf("初始化策略表失败: %v", err)
	}

	// 复现旧版 db.Create(&tmpl) 因 GORM default:true 导致的坏数据：模板被保存为 enabled=true。
	legacyBrokenTemplate := model.Policy{
		Name:          "网站全站备份",
		SourcePath:    "/var/www",
		TargetPath:    "/backup/www",
		CronSpec:      "0 2 * * *",
		RetentionDays: 30,
		MaxConcurrent: 1,
		Enabled:       false,
		IsTemplate:    true,
	}
	if err := db.Create(&legacyBrokenTemplate).Error; err != nil {
		t.Fatalf("创建旧版模板失败: %v", err)
	}
	if !legacyBrokenTemplate.Enabled {
		t.Fatalf("前置条件验证失败：期望复现 GORM default:true 将 Enabled 覆写为 true")
	}

	// 创建一条普通启用的非模板策略与一条普通禁用的非模板策略，验证修复逻辑不会误伤普通策略。
	activeNormal := model.Policy{
		Name:          "normal-enabled",
		SourcePath:    "/data/a",
		TargetPath:    "/backup/a",
		CronSpec:      "0 * * * *",
		RetentionMode: "simple",
		Enabled:       true,
		IsTemplate:    false,
	}
	disabledNormal := model.Policy{
		Name:               "normal-disabled",
		SourcePath:         "/data/b",
		TargetPath:         "/backup/b",
		CronSpec:           "0 * * * *",
		RetentionMode:      "simple",
		HookTimeoutSeconds: 300,
		MaxRetries:         2,
		RetryBaseSeconds:   30,
		DrillRestorePath:   "/tmp/xirang-drill",
		DrillAutoCleanup:   true,
		VerifyEnabled:      true,
		Enabled:            false,
		IsTemplate:         false,
	}
	if err := db.Create(&activeNormal).Error; err != nil {
		t.Fatalf("创建普通启用策略失败: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return model.CreatePolicyWithExplicitValues(tx, &disabledNormal, model.PolicyCreateExplicitColumns()...)
	}); err != nil {
		t.Fatalf("创建普通禁用策略失败: %v", err)
	}

	SeedPolicyTemplates(db)

	var reloadedBroken model.Policy
	if err := db.First(&reloadedBroken, legacyBrokenTemplate.ID).Error; err != nil {
		t.Fatalf("读取修复后的模板失败: %v", err)
	}
	if reloadedBroken.Enabled || !reloadedBroken.IsTemplate {
		t.Fatalf("历史错误启用的模板应被修复为 enabled=false, is_template=true，实际: %+v", reloadedBroken)
	}

	var reloadedNormal model.Policy
	if err := db.First(&reloadedNormal, activeNormal.ID).Error; err != nil {
		t.Fatalf("读取普通启用策略失败: %v", err)
	}
	if !reloadedNormal.Enabled || reloadedNormal.IsTemplate {
		t.Fatalf("普通启用策略不应被禁用: %+v", reloadedNormal)
	}

	var reloadedDisabled model.Policy
	if err := db.First(&reloadedDisabled, disabledNormal.ID).Error; err != nil {
		t.Fatalf("读取普通禁用策略失败: %v", err)
	}
	if reloadedDisabled.Enabled || reloadedDisabled.IsTemplate {
		t.Fatalf("普通禁用策略状态应保持不变: %+v", reloadedDisabled)
	}

	var templateCount int64
	if err := db.Model(&model.Policy{}).Where("is_template = ?", true).Count(&templateCount).Error; err != nil {
		t.Fatalf("统计模板数量失败: %v", err)
	}
	if templateCount != 1 {
		t.Fatalf("已有模板时不应重复插入内置模板，实际模板数量: %d", templateCount)
	}
}
