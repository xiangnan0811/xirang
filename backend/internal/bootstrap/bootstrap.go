package bootstrap

import (
	"fmt"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&model.User{},
		&model.SSHKey{},
		&model.Node{},
		&model.Policy{},
		&model.Integration{},
		&model.Alert{},
		&model.AlertDelivery{},
		&model.Task{},
		&model.TaskLog{},
		&model.AuditLog{},
	)
}

func SeedUsers(db *gorm.DB) error {
	seed := []struct {
		Username string
		Password string
		Role     string
	}{
		{Username: "admin", Password: "REDACTED", Role: "admin"},
		{Username: "operator", Password: "REDACTED", Role: "operator"},
		{Username: "viewer", Password: "REDACTED", Role: "viewer"},
	}

	for _, item := range seed {
		var count int64
		if err := db.Model(&model.User{}).Where("username = ?", item.Username).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		hash, err := auth.HashPassword(item.Password)
		if err != nil {
			return fmt.Errorf("生成用户密码哈希失败: %w", err)
		}
		user := model.User{
			Username:     item.Username,
			PasswordHash: hash,
			Role:         item.Role,
		}
		if err := db.Create(&user).Error; err != nil {
			return err
		}
	}
	return nil
}
