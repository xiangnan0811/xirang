package bootstrap

import (
	"fmt"
	"os"
	"strings"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func AutoMigrate(db *gorm.DB, dbType string) error {
	return database.RunMigrations(db, dbType)
}

func SeedUsers(db *gorm.DB) error {
	var count int64
	if err := db.Model(&model.User{}).Where("username = ?", "admin").Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	adminPassword := strings.TrimSpace(os.Getenv("ADMIN_INITIAL_PASSWORD"))
	if adminPassword == "" {
		return fmt.Errorf("ADMIN_INITIAL_PASSWORD 不能为空")
	}
	if err := auth.ValidatePasswordStrength(adminPassword); err != nil {
		return fmt.Errorf("ADMIN_INITIAL_PASSWORD 强度不足: %w", err)
	}

	hash, err := auth.HashPassword(adminPassword)
	if err != nil {
		return fmt.Errorf("生成用户密码哈希失败: %w", err)
	}
	user := model.User{
		Username:     "admin",
		PasswordHash: hash,
		Role:         "admin",
	}
	if err := db.Create(&user).Error; err != nil {
		return err
	}
	return nil
}
