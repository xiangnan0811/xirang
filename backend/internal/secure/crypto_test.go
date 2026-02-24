package secure

import (
	"strings"
	"sync"
	"testing"
)

func resetCryptoKeyState() {
	loadOnce = sync.Once{}
	keyBytes = nil
	keyErr = nil
}

func TestEncryptStringAllowsDevDefaultKey(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("GIN_MODE", "")
	t.Setenv("DATA_ENCRYPTION_KEY", "")
	resetCryptoKeyState()

	encrypted, err := EncryptString("hello")
	if err != nil {
		t.Fatalf("开发环境应允许默认密钥，实际错误: %v", err)
	}
	if !IsEncrypted(encrypted) {
		t.Fatalf("期望返回加密字符串")
	}

	plain, err := DecryptString(encrypted)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if plain != "hello" {
		t.Fatalf("解密内容不匹配，期望 hello，实际: %s", plain)
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
