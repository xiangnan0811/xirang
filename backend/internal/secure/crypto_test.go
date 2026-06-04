package secure

import (
	"strings"
	"sync"
	"testing"
)

func resetCryptoKeyState() {
	loadOnce = sync.Once{}
	primaryKey = nil
	legacyKey = nil
	keyErr = nil
}

func TestEncryptStringAllowsDevModeWithGeneratedKey(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("GIN_MODE", "")
	t.Setenv("DATA_ENCRYPTION_KEY", "")
	resetCryptoKeyState()

	result, err := EncryptString("hello")
	if err != nil {
		t.Fatalf("dev mode should generate temp key and work: %v", err)
	}
	if !IsEncrypted(result) {
		t.Fatalf("encrypted result should have prefix, got: %s", result)
	}
}

func TestEncryptStringRejectsMissingKeyWhenEnvUnset(t *testing.T) {
	t.Setenv("APP_ENV", "")
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("GIN_MODE", "")
	t.Setenv("DATA_ENCRYPTION_KEY", "")
	resetCryptoKeyState()

	_, err := EncryptString("hello")
	if err == nil {
		t.Fatalf("未声明开发环境时，缺少密钥应返回错误")
	}
	if !strings.Contains(err.Error(), "DATA_ENCRYPTION_KEY") {
		t.Fatalf("错误信息应包含 DATA_ENCRYPTION_KEY，实际: %v", err)
	}
}

func TestV1CompatDecryption(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "test-string-key")
	resetCryptoKeyState()

	// 用 legacy key (sha256) 手动加密一个 v1 值
	lk, err := getLegacyKey()
	if err != nil {
		t.Fatalf("获取 legacy key 失败: %v", err)
	}
	v1Encrypted, err := encryptWithKey("secret-data", encryptedPrefixV1, lk)
	if err != nil {
		t.Fatalf("v1 加密失败: %v", err)
	}

	// DecryptString 应能自动识别 v1 前缀并解密
	plain, err := DecryptString(v1Encrypted)
	if err != nil {
		t.Fatalf("v1 解密失败: %v", err)
	}
	if plain != "secret-data" {
		t.Fatalf("v1 解密内容不匹配，期望 secret-data，实际: %s", plain)
	}
}

func TestReEncryptV1Value(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "test-string-key")
	resetCryptoKeyState()

	// 用 legacy key 制造一条 v1 数据
	lk, _ := getLegacyKey()
	v1Encrypted, _ := encryptWithKey("migrate-me", encryptedPrefixV1, lk)

	// 重加密
	v2Encrypted, changed, err := ReEncryptV1Value(v1Encrypted)
	if err != nil {
		t.Fatalf("重加密失败: %v", err)
	}
	if !changed {
		t.Fatalf("期望 changed=true")
	}
	if !strings.HasPrefix(v2Encrypted, encryptedPrefixV2) {
		t.Fatalf("期望 v2 前缀，实际: %s", v2Encrypted[:10])
	}

	// 验证 v2 数据可解密
	plain, err := DecryptString(v2Encrypted)
	if err != nil {
		t.Fatalf("v2 解密失败: %v", err)
	}
	if plain != "migrate-me" {
		t.Fatalf("解密内容不匹配，期望 migrate-me，实际: %s", plain)
	}

	// 已经是 v2 的数据不应再变更
	_, changed2, _ := ReEncryptV1Value(v2Encrypted)
	if changed2 {
		t.Fatalf("v2 数据不应被重加密")
	}
}

func TestReEncryptV1ValueUsesLegacyKeyEnv(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "old-test-string-key")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	resetCryptoKeyState()

	oldLegacyKey, err := getLegacyKey()
	if err != nil {
		t.Fatalf("获取旧 legacy key 失败: %v", err)
	}
	v1Encrypted, err := encryptWithKey("secret-data", encryptedPrefixV1, oldLegacyKey)
	if err != nil {
		t.Fatalf("v1 加密失败: %v", err)
	}

	t.Setenv("DATA_ENCRYPTION_KEY", "new-test-string-key")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "old-test-string-key")
	resetCryptoKeyState()

	v2Encrypted, changed, err := ReEncryptV1Value(v1Encrypted)
	if err != nil {
		t.Fatalf("使用 DATA_ENCRYPTION_LEGACY_KEY 重加密失败: %v", err)
	}
	if !changed || !strings.HasPrefix(v2Encrypted, encryptedPrefixV2) {
		t.Fatalf("期望迁移为 v2 密文，changed=%v value=%s", changed, v2Encrypted)
	}
	plain, err := DecryptString(v2Encrypted)
	if err != nil {
		t.Fatalf("解密新 v2 密文失败: %v", err)
	}
	if plain != "secret-data" {
		t.Fatalf("解密内容不匹配，期望 secret-data，实际: %s", plain)
	}
}

func TestBase64KeyUseSameForV1V2(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	// 提供一个 base64 编码的 32 字节密钥
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	resetCryptoKeyState()

	pk, _ := getPrimaryKey()
	lk, _ := getLegacyKey()
	if string(pk) != string(lk) {
		t.Fatalf("base64 密钥模式下 primaryKey 和 legacyKey 应相同")
	}
}
