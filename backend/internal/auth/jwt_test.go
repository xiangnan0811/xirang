package auth

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const testStepUpSessionID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestJWTManagerRejectsNonCanonicalBase64URLSignature(t *testing.T) {
	manager := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	token, err := manager.GenerateToken(model.User{ID: 7, Username: "alice", Role: "admin"})
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(parts[2]) == 0 {
		t.Fatalf("生成的 token 格式无效: segments=%d", len(parts))
	}
	const base64URLAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	lastIndex := strings.IndexByte(base64URLAlphabet, parts[2][len(parts[2])-1])
	if lastIndex < 0 || lastIndex%4 != 0 {
		t.Fatalf("签名末字符不是规范的 32-byte Base64URL 结尾: %q", parts[2][len(parts[2])-1])
	}
	nonCanonicalLast := base64URLAlphabet[lastIndex+1]
	nonCanonicalSignature := parts[2][:len(parts[2])-1] + string(nonCanonicalLast)
	canonicalBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("解码规范签名失败: %v", err)
	}
	nonCanonicalBytes, err := base64.RawURLEncoding.DecodeString(nonCanonicalSignature)
	if err != nil {
		t.Fatalf("构造的非规范签名未被宽松解码器接受: %v", err)
	}
	if string(nonCanonicalBytes) != string(canonicalBytes) {
		t.Fatal("构造的非规范签名没有保持相同签名字节")
	}

	parts[2] = nonCanonicalSignature
	if _, err := manager.ParseToken(strings.Join(parts, ".")); err == nil {
		t.Fatal("非规范 Base64URL 签名被接受")
	}
}

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

	proof, expiresAt, err := manager.GenerateStepUpToken(user, StepUpActionTaskManualTrigger, testStepUpSessionID)
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
	if claims.StepUpAction != StepUpActionTaskManualTrigger {
		t.Fatalf("期望 step_up_action=%q，实际 %q", StepUpActionTaskManualTrigger, claims.StepUpAction)
	}
	if claims.UserID != user.ID || claims.Username != user.Username || claims.Role != user.Role || claims.TokenVersion != user.TokenVersion {
		t.Fatalf("step-up claims 未包含当前用户身份、角色和 token version: %+v", claims)
	}
	if claims.SessionID != testStepUpSessionID {
		t.Fatalf("step-up claims session binding = %q, want %q", claims.SessionID, testStepUpSessionID)
	}
	if claims.ID == "" {
		t.Fatalf("step-up proof 应包含 jti")
	}
	if claims.IssuedAt == nil || claims.ExpiresAt == nil {
		t.Fatalf("step-up proof 应包含 iat/exp")
	}
	if !claims.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("返回的 expiresAt 应与 claims exp 一致，claims=%s returned=%s", claims.ExpiresAt.Time, expiresAt)
	}
	ttl := claims.ExpiresAt.Sub(claims.IssuedAt.Time)
	if ttl != StepUpProofTTL {
		t.Fatalf("step-up proof TTL 应为 %s，实际 %s", StepUpProofTTL, ttl)
	}
}

func TestJWTManagerGenerateStepUpTokenUsesExactActionTTLPolicy(t *testing.T) {
	manager := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	user := model.User{ID: 9, Username: "alice", Role: "admin", TokenVersion: 3}

	for _, action := range AllStepUpActions() {
		t.Run(string(action), func(t *testing.T) {
			wantTTL := StepUpProofTTL
			if action == StepUpActionAssetSecretReveal {
				wantTTL = 45 * time.Minute
			}
			if got := StepUpProofTTLForAction(action); got != wantTTL {
				t.Fatalf("StepUpProofTTLForAction(%q) = %s, want %s", action, got, wantTTL)
			}

			proof, returnedExpiry, err := manager.GenerateStepUpToken(user, action, testStepUpSessionID)
			if err != nil {
				t.Fatalf("GenerateStepUpToken(%q): %v", action, err)
			}
			first, err := manager.ParseToken(proof)
			if err != nil {
				t.Fatalf("ParseToken(%q): %v", action, err)
			}
			second, err := manager.ParseToken(proof)
			if err != nil {
				t.Fatalf("second ParseToken(%q): %v", action, err)
			}
			if first.IssuedAt == nil || first.ExpiresAt == nil || second.ExpiresAt == nil {
				t.Fatalf("proof for %q is missing iat/exp", action)
			}
			if got := first.ExpiresAt.Sub(first.IssuedAt.Time); got != wantTTL {
				t.Fatalf("proof TTL for %q = %s, want %s", action, got, wantTTL)
			}
			if !first.ExpiresAt.Equal(returnedExpiry) {
				t.Fatalf("returned expiry for %q = %s, claim exp = %s", action, returnedExpiry, first.ExpiresAt.Time)
			}
			if !second.ExpiresAt.Equal(first.ExpiresAt.Time) {
				t.Fatalf("proof expiry slid on reuse for %q: first=%s second=%s", action, first.ExpiresAt.Time, second.ExpiresAt.Time)
			}
		})
	}

	if got := StepUpProofTTLForAction(StepUpAction("future.action")); got != 0 {
		t.Fatalf("unknown action TTL = %s, want zero", got)
	}
}

func TestJWTManagerGenerateStepUpTokenRejectsMissingOrInvalidSessionBinding(t *testing.T) {
	manager := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	user := model.User{ID: 9, Username: "alice", Role: "admin", TokenVersion: 3}

	for _, sessionID := range []string{"", "not-a-session", strings.Repeat("A", 32)} {
		if _, _, err := manager.GenerateStepUpToken(user, StepUpActionAssetSecretReveal, sessionID); err == nil {
			t.Fatalf("GenerateStepUpToken accepted invalid session binding %q", sessionID)
		}
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
	claims, err := manager.parseToken(token, false)
	if err != nil {
		t.Fatalf("parse revoked token claims: %v", err)
	}
	for _, candidate := range []*JWTManager{manager, reloaded} {
		revoked, revokeErr := candidate.IsSessionRevoked(claims.ID)
		if revokeErr != nil || !revoked {
			t.Fatalf("IsSessionRevoked(%q) = %v, %v", claims.ID, revoked, revokeErr)
		}
	}
}

func TestJWTManagerSessionRevokedFailsClosedForInvalidJTI(t *testing.T) {
	manager := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	for _, value := range []string{"", "ABC", strings.Repeat("A", 32), strings.Repeat("g", 32)} {
		if revoked, err := manager.IsSessionRevoked(value); err == nil || !revoked {
			t.Fatalf("IsSessionRevoked(%q) = %v, %v; want revoked error", value, revoked, err)
		}
	}
}

func TestJWTManagerRevokeSessionByJTI(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(&model.TokenRevocation{}); err != nil {
		t.Fatalf("migrate token revocations: %v", err)
	}

	manager := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	manager.SetDB(db)
	jti := strings.Repeat("a", 32)
	expiresAt := time.Now().UTC().Add(time.Hour)
	if err := manager.RevokeSession(jti, 7, expiresAt); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	if revoked, err := manager.IsSessionRevoked(jti); err != nil || !revoked {
		t.Fatalf("IsSessionRevoked(%q) = %v, %v", jti, revoked, err)
	}
	var row model.TokenRevocation
	if err := db.Where("token_hash = ?", "jti:"+jti).First(&row).Error; err != nil {
		t.Fatalf("load persisted session revocation: %v", err)
	}
	if row.UserID != 7 || !row.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("persisted revocation = %+v, want user=7 expiry=%s", row, expiresAt)
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
