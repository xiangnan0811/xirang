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
	if err := db.AutoMigrate(&model.User{}, &model.LoginFailure{}); err != nil {
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

	if _, err := service.Login("admin", "wrong-1", "127.0.0.1"); err == nil {
		t.Fatalf("首次错误密码应返回失败")
	}
	if _, err := service.Login("admin", "wrong-2", "127.0.0.1"); err == nil {
		t.Fatalf("第二次错误密码应返回失败")
	}

	if _, err := service.Login("admin", "correct-password", "127.0.0.1"); err == nil {
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

	if _, err := service.Login("admin", "correct-password", "127.0.0.2"); err != nil {
		t.Fatalf("不同 IP 不应受锁定影响，实际错误: %v", err)
	}
}

func TestLoginLockExpiresAfterDuration(t *testing.T) {
	db := openAuthServiceTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.LoginFailure{}); err != nil {
		t.Fatalf("初始化用户表失败: %v", err)
	}

	passwordHash, _ := HashPassword("Correct1!")
	user := model.User{Username: "locktest", PasswordHash: passwordHash, Role: "admin"}
	db.Create(&user)

	service := NewService(db, NewJWTManager("test-secret", time.Hour), LoginSecurityConfig{
		FailLockThreshold: 2,
		FailLockDuration:  2 * time.Second, // 需大于 bcrypt 执行耗时
	})

	// 触发锁定
	service.Login("locktest", "wrong1", "10.0.0.1")
	service.Login("locktest", "wrong2", "10.0.0.1")

	// 锁定中
	_, err := service.Login("locktest", "Correct1!", "10.0.0.1")
	if _, ok := IsLoginLocked(err); !ok {
		t.Fatalf("应处于锁定状态")
	}

	// 等待锁定过期
	time.Sleep(2100 * time.Millisecond)

	// 锁定应过期
	_, err = service.Login("locktest", "Correct1!", "10.0.0.1")
	if err != nil {
		t.Fatalf("锁定过期后应能正常登录，实际错误: %v", err)
	}
}

func TestChangePasswordRejectsWrongCurrent(t *testing.T) {
	db := openAuthServiceTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.LoginFailure{}); err != nil {
		t.Fatalf("初始化用户表失败: %v", err)
	}

	passwordHash, _ := HashPassword("OldPassword1!")
	user := model.User{Username: "chgpwd", PasswordHash: passwordHash, Role: "admin"}
	db.Create(&user)

	service := NewService(db, NewJWTManager("test-secret", time.Hour), LoginSecurityConfig{
		FailLockThreshold: 10,
		FailLockDuration:  time.Minute,
	})

	err := service.ChangePassword(user.ID, "WrongOldPass1!", "NewPassword1!")
	if err == nil {
		t.Fatalf("旧密码错误时应返回错误")
	}
	if !strings.Contains(err.Error(), "当前密码错误") {
		t.Fatalf("错误信息应包含'当前密码错误'，实际: %v", err)
	}

	// 正确旧密码应成功
	err = service.ChangePassword(user.ID, "OldPassword1!", "NewPassword1!")
	if err != nil {
		t.Fatalf("正确旧密码应能修改成功，实际错误: %v", err)
	}
}

func TestCreateUserRejectsDuplicate(t *testing.T) {
	db := openAuthServiceTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.LoginFailure{}); err != nil {
		t.Fatalf("初始化用户表失败: %v", err)
	}

	service := NewService(db, NewJWTManager("test-secret", time.Hour), LoginSecurityConfig{
		FailLockThreshold: 10,
		FailLockDuration:  time.Minute,
	})

	_, err := service.CreateUser("dupuser", "StrongPwd1!xxz", "admin")
	if err != nil {
		t.Fatalf("首次创建用户应成功: %v", err)
	}

	_, err = service.CreateUser("dupuser", "StrongPwd2!xxz", "admin")
	if err == nil {
		t.Fatalf("重复用户名应返回错误")
	}
	if !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("错误信息应包含'已存在'，实际: %v", err)
	}
}

func TestCreateUserRejectsInvalidRole(t *testing.T) {
	db := openAuthServiceTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.LoginFailure{}); err != nil {
		t.Fatalf("初始化用户表失败: %v", err)
	}

	service := NewService(db, NewJWTManager("test-secret", time.Hour), LoginSecurityConfig{
		FailLockThreshold: 10,
		FailLockDuration:  time.Minute,
	})

	_, err := service.CreateUser("roletest", "StrongPwd1!xxz", "superadmin")
	if err == nil {
		t.Fatalf("无效角色应返回错误")
	}

	// 合法角色
	for _, role := range []string{"admin", "operator", "viewer"} {
		_, err := service.CreateUser(fmt.Sprintf("user-%s", role), "StrongPwd1!xxz", role)
		if err != nil {
			t.Fatalf("角色 %s 应合法，实际错误: %v", role, err)
		}
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
