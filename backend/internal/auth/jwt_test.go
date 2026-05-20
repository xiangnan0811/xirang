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

func TestJWTManagerGenerateStepUpTokenIncludesDedicatedPurposeTTLAndVersion(t *testing.T) {
	manager := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	user := model.User{ID: 9, Username: "alice", Role: "admin", TokenVersion: 3}

	proof, expiresAt, err := manager.GenerateStepUpToken(user)
	if err != nil {
		t.Fatalf("生成 step-up proof 失败: %v", err)
	}
	if proof == "" {
		t.Fatalf("step-up proof 不应为空")
	}

	claims, err := manager.ParseToken(proof)
	if err != nil {
		t.Fatalf("解析 step-up proof 失败: %v", err)
	}
	if claims.Purpose != PurposeStepUp {
		t.Fatalf("期望 purpose=%q，实际 %q", PurposeStepUp, claims.Purpose)
	}
	if claims.UserID != user.ID || claims.Username != user.Username || claims.Role != user.Role || claims.TokenVersion != user.TokenVersion {
		t.Fatalf("step-up claims 未包含当前用户身份、角色和 token version: %+v", claims)
	}
	if claims.ID == "" {
		t.Fatalf("step-up proof 应包含 jti")
	}
	if claims.IssuedAt == nil || claims.ExpiresAt == nil {
		t.Fatalf("step-up proof 应包含 iat/exp")
	}
	if !claims.ExpiresAt.Time.Equal(expiresAt.Truncate(time.Second)) {
		t.Fatalf("返回的 expiresAt 应与 claims exp 一致，claims=%s returned=%s", claims.ExpiresAt.Time, expiresAt)
	}
	ttl := claims.ExpiresAt.Time.Sub(claims.IssuedAt.Time)
	if ttl != StepUpProofTTL {
		t.Fatalf("step-up proof TTL 应为 %s，实际 %s", StepUpProofTTL, ttl)
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
