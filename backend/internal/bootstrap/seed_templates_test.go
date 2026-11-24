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

func TestSeedPolicyTemplatesDoesNotRewriteExistingTemplates(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	db := openBootstrapTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{}); err != nil {
		t.Fatalf("初始化测试表失败: %v", err)
	}

	// 1. 与内置模板名称/源路径/目标路径/Cron 完全一致、未被任何任务或节点引用、但由配置导入或用户显式启用的模板。
	importedEnabledTemplate := model.Policy{
		Name:          "网站全站备份",
		SourcePath:    "/var/www",
		TargetPath:    "/backup/www",
		CronSpec:      "0 2 * * *",
		RetentionDays: 30,
		MaxConcurrent: 1,
		Enabled:       true,
		IsTemplate:    true,
	}
	if err := model.CreatePolicyWithExplicitValues(db, &importedEnabledTemplate, model.PolicyCreateExplicitColumns()...); err != nil {
		t.Fatalf("创建显式启用模板失败: %v", err)
	}

	// 2. 自定义显式启用的模板。
	customEnabledTemplate := model.Policy{
		Name:       "custom-enabled-template",
		SourcePath: "/srv/app",
		TargetPath: "/backup/app",
		CronSpec:   "0 1 * * *",
		Enabled:    true,
		IsTemplate: true,
	}
	if err := model.CreatePolicyWithExplicitValues(db, &customEnabledTemplate, model.PolicyCreateExplicitColumns()...); err != nil {
		t.Fatalf("创建自定义启用模板失败: %v", err)
	}

	// 3. 普通启用的非模板策略与普通禁用的非模板策略。
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

	var gotImported model.Policy
	if err := db.First(&gotImported, importedEnabledTemplate.ID).Error; err != nil {
		t.Fatalf("读取导入模板失败: %v", err)
	}
	if !gotImported.Enabled || !gotImported.IsTemplate {
		t.Fatalf("启动不应覆盖用户或配置导入的启用模板: %+v", gotImported)
	}

	var gotCustom model.Policy
	if err := db.First(&gotCustom, customEnabledTemplate.ID).Error; err != nil {
		t.Fatalf("读取自定义模板失败: %v", err)
	}
	if !gotCustom.Enabled || !gotCustom.IsTemplate {
		t.Fatalf("启动不应覆盖用户自定义启用模板: %+v", gotCustom)
	}

	var gotNormal model.Policy
	if err := db.First(&gotNormal, activeNormal.ID).Error; err != nil {
		t.Fatalf("读取普通启用策略失败: %v", err)
	}
	if !gotNormal.Enabled || gotNormal.IsTemplate {
		t.Fatalf("普通启用策略不应被修改: %+v", gotNormal)
	}

	var gotDisabled model.Policy
	if err := db.First(&gotDisabled, disabledNormal.ID).Error; err != nil {
		t.Fatalf("读取普通禁用策略失败: %v", err)
	}
	if gotDisabled.Enabled || gotDisabled.IsTemplate {
		t.Fatalf("普通禁用策略不应被修改: %+v", gotDisabled)
	}

	var templateCount int64
	if err := db.Model(&model.Policy{}).Where("is_template = ?", true).Count(&templateCount).Error; err != nil {
		t.Fatalf("统计模板数量失败: %v", err)
	}
	if templateCount != 2 {
		t.Fatalf("已有模板时不应重复插入内置模板，实际模板数量: %d", templateCount)
	}
}

func TestSeedPolicyTemplatesContinuesWhenOneBuiltinNameConflicts(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	db := openBootstrapTestDB(t)
	if err := db.AutoMigrate(&model.Policy{}); err != nil {
		t.Fatalf("初始化策略表失败: %v", err)
	}

	// 预先创建一个与内置模板同名的普通策略，验证不会整批回滚其余 3 个内置模板。
	existing := model.Policy{
		Name:       "网站全站备份",
		SourcePath: "/custom/www",
		TargetPath: "/custom/backup",
		CronSpec:   "0 1 * * *",
		Enabled:    true,
		IsTemplate: false,
	}
	if err := model.CreatePolicyWithExplicitValues(db, &existing, model.PolicyCreateExplicitColumns()...); err != nil {
		t.Fatalf("创建冲突普通策略失败: %v", err)
	}

	SeedPolicyTemplates(db)

	var templates []model.Policy
	if err := db.Where("is_template = ?", true).Find(&templates).Error; err != nil {
		t.Fatalf("查询模板失败: %v", err)
	}
	if len(templates) != 3 {
		t.Fatalf("单个名称冲突后其余 3 个内置模板仍应创建，实际: %d", len(templates))
	}
	for _, tmpl := range templates {
		if tmpl.Enabled {
			t.Fatalf("新创建模板 %q 应保持禁用", tmpl.Name)
		}
	}
}
