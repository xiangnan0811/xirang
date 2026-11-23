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

// SeedPolicyTemplates 修复历史错误启用的模板并在无模板时初始化内置策略模板。
func SeedPolicyTemplates(db *gorm.DB) {
	if db == nil {
		return
	}
	log := logger.Module("bootstrap")

	err := db.Transaction(func(tx *gorm.DB) error {
		// 模板策略不参与调度或任务生成；早期因 GORM default:true 误写为启用的模板需幂等修复为禁用。
		repaired := tx.Model(&model.Policy{}).
			Where("is_template = ? AND enabled = ?", true, true).
			Update("enabled", false)
		if repaired.Error != nil {
			return fmt.Errorf("修复策略模板启用状态失败: %w", repaired.Error)
		}
		if repaired.RowsAffected > 0 {
			log.Info().Int64("count", repaired.RowsAffected).Msg("已修复错误启用的策略模板")
		}

		var count int64
		if err := tx.Model(&model.Policy{}).Where("is_template = ?", true).Count(&count).Error; err != nil {
			return fmt.Errorf("统计策略模板失败: %w", err)
		}
		if count > 0 {
			return nil
		}

		templates := []model.Policy{
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

		for i := range templates {
			if err := model.CreatePolicyWithExplicitValues(tx, &templates[i], model.PolicyCreateExplicitColumns()...); err != nil {
				return fmt.Errorf("创建策略模板 %q 失败: %w", templates[i].Name, err)
			}
		}
		log.Info().Int("count", len(templates)).Msg("策略模板初始化完成")
		return nil
	})
	if err != nil {
		log.Warn().Err(err).Msg("策略模板初始化失败")
	}
}
