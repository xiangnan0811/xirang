package bootstrap

import (
	"fmt"
	"strings"
	"testing"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openBootstrapTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}

func TestSeedUsersRequiresAdminInitialPassword(t *testing.T) {
	db := openBootstrapTestDB(t)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("初始化用户表失败: %v", err)
	}

	t.Setenv("ADMIN_INITIAL_PASSWORD", "")
	if err := SeedUsers(db); err == nil {
		t.Fatalf("期望缺少 ADMIN_INITIAL_PASSWORD 时返回错误")
	}
}

func TestSeedUsersCreatesAdminOnly(t *testing.T) {
	db := openBootstrapTestDB(t)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("初始化用户表失败: %v", err)
	}

	t.Setenv("ADMIN_INITIAL_PASSWORD", "StrongAdmin#2026")
	if err := SeedUsers(db); err != nil {
		t.Fatalf("初始化用户失败: %v", err)
	}

	var users []model.User
	if err := db.Order("id asc").Find(&users).Error; err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("期望仅创建 admin 用户，实际数量: %d", len(users))
	}
	if users[0].Username != "admin" || users[0].Role != "admin" {
		t.Fatalf("期望仅存在 admin/admin 用户，实际: %+v", users[0])
	}
	if strings.TrimSpace(users[0].PasswordHash) == "" {
		t.Fatalf("期望 admin 密码哈希不为空")
	}

	if err := SeedUsers(db); err != nil {
		t.Fatalf("重复执行 SeedUsers 不应报错，实际: %v", err)
	}
	var count int64
	if err := db.Model(&model.User{}).Count(&count).Error; err != nil {
		t.Fatalf("统计用户失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("重复执行后用户数量应保持 1，实际: %d", count)
	}
}

func TestSeedUsersAllowsMissingPasswordWhenAdminAlreadyExists(t *testing.T) {
	db := openBootstrapTestDB(t)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("初始化用户表失败: %v", err)
	}

	t.Setenv("ADMIN_INITIAL_PASSWORD", "StrongAdmin#2026")
	if err := SeedUsers(db); err != nil {
		t.Fatalf("首次初始化用户失败: %v", err)
	}

	t.Setenv("ADMIN_INITIAL_PASSWORD", "")
	if err := SeedUsers(db); err != nil {
		t.Fatalf("admin 已存在时不应强制要求 ADMIN_INITIAL_PASSWORD，实际: %v", err)
	}
}

func TestAutoMigrateIncludesTaskTrafficSample(t *testing.T) {
	db := openBootstrapTestDB(t)

	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate 失败: %v", err)
	}

	if !db.Migrator().HasTable(&model.TaskTrafficSample{}) {
		t.Fatalf("期望 AutoMigrate 创建 task_traffic_samples 表")
	}
}
