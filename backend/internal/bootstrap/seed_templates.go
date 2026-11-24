package bootstrap

import (
	"fmt"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func defaultSeedPolicyTemplate(tmpl model.Policy) model.Policy {
	// Materialize schema defaults so CreatePolicyWithExplicitValues restores
	// Enabled=false and IsTemplate=true without zeroing default-populated columns.
	if tmpl.RetentionMode == "" {
		tmpl.RetentionMode = "simple"
	}
	if tmpl.HookTimeoutSeconds == 0 {
		tmpl.HookTimeoutSeconds = 300
	}
	if tmpl.MaxRetries == 0 {
		tmpl.MaxRetries = 2
	}
	if tmpl.RetryBaseSeconds == 0 {
		tmpl.RetryBaseSeconds = 30
	}
	if tmpl.DrillRestorePath == "" {
		tmpl.DrillRestorePath = "/tmp/xirang-drill"
	}
	tmpl.VerifyEnabled = true
	tmpl.DrillAutoCleanup = true
	tmpl.Enabled = false
	tmpl.IsTemplate = true
	return tmpl
}

func builtinPolicyTemplates() []model.Policy {
	return []model.Policy{
		defaultSeedPolicyTemplate(model.Policy{
			Name:          "网站全站备份",
			Description:   "备份 /var/www 下的网站文件，排除日志和临时文件",
			SourcePath:    "/var/www",
			TargetPath:    "/backup/www",
			CronSpec:      "0 2 * * *",
			ExcludeRules:  "*.log\nnode_modules\n.git\n*.tmp",
			RetentionDays: 30,
			MaxConcurrent: 1,
		}),
		defaultSeedPolicyTemplate(model.Policy{
			Name:          "Docker 数据卷备份",
			Description:   "备份 Docker volumes 数据",
			SourcePath:    "/var/lib/docker/volumes",
			TargetPath:    "/backup/docker-volumes",
			CronSpec:      "0 3 * * *",
			ExcludeRules:  "",
			RetentionDays: 14,
			MaxConcurrent: 1,
		}),
		defaultSeedPolicyTemplate(model.Policy{
			Name:          "系统配置备份",
			Description:   "每周备份 /etc 系统配置文件",
			SourcePath:    "/etc",
			TargetPath:    "/backup/etc",
			CronSpec:      "0 4 * * 0",
			ExcludeRules:  "*.swp\n*.bak",
			RetentionDays: 90,
			MaxConcurrent: 1,
		}),
		defaultSeedPolicyTemplate(model.Policy{
			Name:          "用户数据备份",
			Description:   "备份 /home 用户数据，排除缓存和垃圾箱",
			SourcePath:    "/home",
			TargetPath:    "/backup/home",
			CronSpec:      "0 2 * * *",
			ExcludeRules:  "*.cache\n.local/share/Trash\n.thumbnails\n.npm\n.yarn",
			RetentionDays: 30,
			MaxConcurrent: 1,
		}),
	}
}

// SeedPolicyTemplates 在无策略模板时逐条初始化默认禁用的内置策略模板。
// 启动过程不猜测并改写既有模板的 enabled 状态，避免覆盖用户显式启用或配置导入的模板；
// 总览统计、健康/存储检查与后台清理均按 is_template=false 隔离模板策略。
func SeedPolicyTemplates(db *gorm.DB) {
	if db == nil {
		return
	}
	log := logger.Module("bootstrap")

	var count int64
	if err := db.Model(&model.Policy{}).Where("is_template = ?", true).Count(&count).Error; err != nil {
		log.Warn().Err(fmt.Errorf("统计策略模板失败: %w", err)).Msg("策略模板初始化失败")
		return
	}
	if count > 0 {
		return
	}

	templates := builtinPolicyTemplates()
	// 逐条插入内置模板，避免单个同名普通策略冲突导致整批模板初始化失败。
	createdCount := 0
	for i := range templates {
		if err := db.Transaction(func(tx *gorm.DB) error {
			return model.CreatePolicyWithExplicitValues(tx, &templates[i], model.PolicyCreateExplicitColumns()...)
		}); err != nil {
			log.Warn().Err(err).Str("template", templates[i].Name).Msg("创建策略模板失败，继续初始化其余模板")
			continue
		}
		createdCount++
	}
	if createdCount > 0 {
		log.Info().Int("count", createdCount).Msg("策略模板初始化完成")
	}
}
