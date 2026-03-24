package handlers

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openRealtimeAuthTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}

func TestAuthorizeRealtimeTokenRejectsStaleTokenVersion(t *testing.T) {
	db := openRealtimeAuthTestDB(t)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("初始化用户表失败: %v", err)
	}

	user := model.User{Username: "operator", Role: "operator", PasswordHash: "hashed", TokenVersion: 0}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	jwtManager := auth.NewJWTManager("test-secret", time.Hour)
	token, err := jwtManager.GenerateToken(user)
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}

	if err := db.Model(&model.User{}).Where("id = ?", user.ID).Update("token_version", 1).Error; err != nil {
		t.Fatalf("更新 token_version 失败: %v", err)
	}

	if _, err := authorizeRealtimeToken(token, jwtManager, db, realtimeAuthRequirements{Permission: "tasks:read"}); err == nil {
		t.Fatalf("过期 token_version 应被拒绝")
	}
}

func TestAuthorizeRealtimeTokenRejectsRoleMismatch(t *testing.T) {
	db := openRealtimeAuthTestDB(t)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("初始化用户表失败: %v", err)
	}

	user := model.User{Username: "operator", Role: "operator", PasswordHash: "hashed"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	jwtManager := auth.NewJWTManager("test-secret", time.Hour)
	token, err := jwtManager.GenerateToken(user)
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}

	if _, err := authorizeRealtimeToken(token, jwtManager, db, realtimeAuthRequirements{Role: "admin"}); err == nil {
		t.Fatalf("非 admin token 应被拒绝")
	} else if !strings.Contains(err.Error(), "权限不足") {
		t.Fatalf("期望返回权限不足，实际: %v", err)
	}
}
