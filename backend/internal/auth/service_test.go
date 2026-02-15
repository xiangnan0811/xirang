package auth

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLoginLocksByUsernameAndIPAfterThreshold(t *testing.T) {
	db := openAuthServiceTestDB(t)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("初始化用户表失败: %v", err)
	}

	passwordHash, err := HashPassword("correct-password")
	if err != nil {
		t.Fatalf("生成密码哈希失败: %v", err)
	}
	user := model.User{Username: "admin", PasswordHash: passwordHash, Role: "admin"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	service := NewService(db, NewJWTManager("test-secret", time.Hour), LoginSecurityConfig{
		FailLockThreshold: 2,
		FailLockDuration:  time.Minute,
	})

	if _, _, err := service.Login("admin", "wrong-1", "127.0.0.1"); err == nil {
		t.Fatalf("首次错误密码应返回失败")
	}
	if _, _, err := service.Login("admin", "wrong-2", "127.0.0.1"); err == nil {
		t.Fatalf("第二次错误密码应返回失败")
	}

	if _, _, err := service.Login("admin", "correct-password", "127.0.0.1"); err == nil {
		t.Fatalf("达到阈值后应被锁定")
	} else {
		lockedErr, ok := IsLoginLocked(err)
		if !ok {
			t.Fatalf("期望返回登录锁定错误，实际: %v", err)
		}
		if lockedErr.RetryAfterSeconds(time.Now()) <= 0 {
			t.Fatalf("锁定错误应返回正数重试秒数")
		}
	}

	if _, _, err := service.Login("admin", "correct-password", "127.0.0.2"); err != nil {
		t.Fatalf("不同 IP 不应受锁定影响，实际错误: %v", err)
	}
}

func openAuthServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}
