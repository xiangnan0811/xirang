package bootstrap

import (
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// SeedPolicyTemplates 初始化内置策略模板（仅在无模板时执行）。
func SeedPolicyTemplates(db *gorm.DB) {
	var count int64
	db.Model(&model.Policy{}).Where("is_template = ?", true).Count(&count)
	if count > 0 {
		return
	}

	templates := []model.Policy{
		{
			Name:          "网站全站备份",
			Description:   "备份 /var/www 下的网站文件，排除日志和临时文件",
			SourcePath:    "/var/www",
			TargetPath:    "/backup/www",
			CronSpec:      "0 2 * * *",
			ExcludeRules:  "*.log\nnode_modules\n.git\n*.tmp",
			RetentionDays: 30,
			MaxConcurrent: 1,
			Enabled:       false,
			IsTemplate:    true,
		},
		{
			Name:          "Docker 数据卷备份",
			Description:   "备份 Docker volumes 数据",
			SourcePath:    "/var/lib/docker/volumes",
			TargetPath:    "/backup/docker-volumes",
			CronSpec:      "0 3 * * *",
			ExcludeRules:  "",
			RetentionDays: 14,
			MaxConcurrent: 1,
			Enabled:       false,
			IsTemplate:    true,
		},
		{
			Name:          "系统配置备份",
			Description:   "每周备份 /etc 系统配置文件",
			SourcePath:    "/etc",
			TargetPath:    "/backup/etc",
			CronSpec:      "0 4 * * 0",
			ExcludeRules:  "*.swp\n*.bak",
			RetentionDays: 90,
			MaxConcurrent: 1,
			Enabled:       false,
			IsTemplate:    true,
		},
		{
			Name:          "用户数据备份",
			Description:   "备份 /home 用户数据，排除缓存和垃圾箱",
			SourcePath:    "/home",
			TargetPath:    "/backup/home",
			CronSpec:      "0 2 * * *",
			ExcludeRules:  "*.cache\n.local/share/Trash\n.thumbnails\n.npm\n.yarn",
			RetentionDays: 30,
			MaxConcurrent: 1,
			Enabled:       false,
			IsTemplate:    true,
		},
	}

	for _, tmpl := range templates {
		if err := db.Create(&tmpl).Error; err != nil {
			logger.Module("bootstrap").Warn().Str("template", tmpl.Name).Err(err).Msg("创建策略模板失败")
		}
	}
	logger.Module("bootstrap").Info().Int("count", len(templates)).Msg("策略模板初始化完成")
}
