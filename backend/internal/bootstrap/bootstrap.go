package bootstrap

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func generateRandomPassword() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("生成随机密码失败: " + err.Error())
	}
	return hex.EncodeToString(b)
}

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
	adminPassword := strings.TrimSpace(os.Getenv("ADMIN_INITIAL_PASSWORD"))

	seed := []struct {
		Username string
		Role     string
	}{
		{Username: "admin", Role: "admin"},
		{Username: "operator", Role: "operator"},
		{Username: "viewer", Role: "viewer"},
	}

	for _, item := range seed {
		var count int64
		if err := db.Model(&model.User{}).Where("username = ?", item.Username).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			continue
		}

		password := generateRandomPassword()
		if item.Username == "admin" && adminPassword != "" {
			password = adminPassword
		}

		hash, err := auth.HashPassword(password)
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

		if item.Username == "admin" && adminPassword == "" {
			log.Printf("初始管理员密码: %s", password)
			log.Printf("警告: 请尽快修改管理员默认密码")
		}
		if item.Username != "admin" {
			log.Printf("初始用户 %s 密码: %s", item.Username, password)
		}
	}
	return nil
}
