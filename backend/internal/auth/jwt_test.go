package auth

import (
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func TestJWTManagerRevokeToken(t *testing.T) {
	manager := NewJWTManager("test-secret", time.Hour)
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
