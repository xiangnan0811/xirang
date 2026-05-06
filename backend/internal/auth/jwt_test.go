package auth

import (
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestJWTManagerRevokeToken(t *testing.T) {
	manager := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	user := model.User{ID: 7, Username: "alice", Role: "admin"}

	token, err := manager.GenerateToken(user)
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}

	claims, err := manager.ParseToken(token)
	if err != nil {
		t.Fatalf("解析 token 失败: %v", err)
	}
	if claims.ID == "" {
		t.Fatalf("期望生成 token 包含 jti")
	}

	if err := manager.RevokeToken(token); err != nil {
		t.Fatalf("注销 token 失败: %v", err)
	}

	if _, err := manager.ParseToken(token); err == nil {
		t.Fatalf("期望已注销 token 被拒绝")
	} else if !strings.Contains(err.Error(), "已注销") {
		t.Fatalf("期望返回注销错误，实际: %v", err)
	}
}

func TestJWTManagerRevokeTokenPersistsAndReloads(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.TokenRevocation{}); err != nil {
		t.Fatalf("初始化撤销表失败: %v", err)
	}

	manager := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	manager.SetDB(db)
	user := model.User{ID: 7, Username: "alice", Role: "admin"}
	token, err := manager.GenerateToken(user)
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}

	if err := manager.RevokeToken(token); err != nil {
		t.Fatalf("注销 token 失败: %v", err)
	}

	reloaded := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	reloaded.SetDB(db)
	if _, err := reloaded.ParseToken(token); err == nil {
		t.Fatalf("期望重新加载后的管理器拒绝已持久化注销 token")
	}
}

func TestJWTManagerRevokeTokenReturnsPersistenceError(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}

	manager := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	manager.SetDB(db)
	user := model.User{ID: 7, Username: "alice", Role: "admin"}
	token, err := manager.GenerateToken(user)
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}

	if err := manager.RevokeToken(token); err == nil {
		t.Fatalf("撤销表缺失时应返回持久化错误")
	}
}
