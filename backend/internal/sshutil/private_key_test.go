package sshutil

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"xirang/backend/internal/secure"
)

func buildRSAPrivateKeyForTest(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("生成测试私钥失败: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if len(pemBytes) == 0 {
		t.Fatalf("编码测试私钥失败")
	}
	return string(pemBytes)
}

func TestNormalizePrivateKeyMaterialExtractsKeyBlockFromMixedText(t *testing.T) {
	key := buildRSAPrivateKeyForTest(t)
	mixed := "日志前缀 INFO manual    " + key + "\n后缀垃圾文本"

	normalized := NormalizePrivateKeyMaterial(mixed)
	if !strings.HasPrefix(normalized, "-----BEGIN RSA PRIVATE KEY-----") {
		t.Fatalf("期望提取私钥块开头，实际: %s", normalized[:min(32, len(normalized))])
	}
	if !strings.Contains(normalized, "-----END RSA PRIVATE KEY-----") {
		t.Fatalf("期望保留私钥结束标记")
	}
	if strings.Contains(normalized, "日志前缀") || strings.Contains(normalized, "后缀垃圾") {
		t.Fatalf("期望剔除私钥块外文本，实际: %s", normalized)
	}
}

func TestValidateAndPreparePrivateKeyAcceptsEncryptedCiphertextAfterDecrypt(t *testing.T) {
	key := buildRSAPrivateKeyForTest(t)
	ciphertext, err := secure.EncryptIfNeeded(key)
	if err != nil {
		t.Fatalf("加密测试私钥失败: %v", err)
	}

	prepared, detectedType, err := ValidateAndPreparePrivateKey(ciphertext, SSHKeyTypeRSA)
	if err != nil {
		t.Fatalf("期望自动解密后校验通过，实际失败: %v", err)
	}
	if detectedType != SSHKeyTypeRSA {
		t.Fatalf("期望识别为 rsa，实际: %s", detectedType)
	}
	if !strings.HasPrefix(prepared, "-----BEGIN RSA PRIVATE KEY-----") {
		t.Fatalf("期望输出 PEM RSA 私钥，实际: %s", prepared[:min(32, len(prepared))])
	}
}

func TestValidateAndPreparePrivateKeyRejectsInvalidEncryptedCiphertext(t *testing.T) {
	_, _, err := ValidateAndPreparePrivateKey("enc:v1:abc123", SSHKeyTypeAuto)
	if err == nil {
		t.Fatalf("期望无效 enc:v1 密文被拒绝")
	}
	if !strings.Contains(err.Error(), "DATA_ENCRYPTION_KEY") {
		t.Fatalf("期望返回解密失败提示，实际: %v", err)
	}
}

func TestValidateAndPreparePrivateKeyAcceptsEscapedOpenSSHLikeInput(t *testing.T) {
	key := buildRSAPrivateKeyForTest(t)
	escaped := strings.ReplaceAll(key, "\n", "\\n")

	prepared, detectedType, err := ValidateAndPreparePrivateKey(escaped, SSHKeyTypeRSA)
	if err != nil {
		t.Fatalf("期望校验通过，实际失败: %v", err)
	}
	if detectedType != SSHKeyTypeRSA {
		t.Fatalf("期望识别为 rsa，实际: %s", detectedType)
	}
	if !strings.HasPrefix(prepared, "-----BEGIN RSA PRIVATE KEY-----") {
		t.Fatalf("期望输出 PEM RSA 私钥，实际: %s", prepared[:min(32, len(prepared))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
