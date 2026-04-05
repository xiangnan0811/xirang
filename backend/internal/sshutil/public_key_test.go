package sshutil

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// buildED25519PrivateKeyForTest 生成一个 ED25519 测试私钥（OpenSSH 格式 PEM）。
func buildED25519PrivateKeyForTest(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成 ED25519 测试私钥失败: %v", err)
	}

	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("编码 ED25519 测试私钥为 OpenSSH 格式失败: %v", err)
	}

	return string(pem.EncodeToMemory(pemBlock))
}

func TestDerivePublicKey(t *testing.T) {
	privPEM := buildED25519PrivateKeyForTest(t)

	pubKey, err := DerivePublicKey(privPEM)
	if err != nil {
		t.Fatalf("期望成功派生公钥，实际失败: %v", err)
	}
	if pubKey == "" {
		t.Fatal("期望返回非空公钥字符串")
	}
	if !strings.HasPrefix(pubKey, "ssh-ed25519 ") {
		t.Fatalf("期望公钥以 ssh-ed25519 开头，实际: %s", pubKey)
	}
	// 确认没有尾部换行
	if strings.HasSuffix(pubKey, "\n") {
		t.Fatal("期望公钥字符串不含尾部换行")
	}
}

func TestDerivePublicKey_InvalidKey(t *testing.T) {
	_, err := DerivePublicKey("not-a-valid-private-key")
	if err == nil {
		t.Fatal("期望无效私钥返回错误，实际返回 nil")
	}
}

func TestDerivePublicKey_EmptyKey(t *testing.T) {
	pubKey, err := DerivePublicKey("")
	if err != nil {
		t.Fatalf("期望空私钥不返回错误，实际: %v", err)
	}
	if pubKey != "" {
		t.Fatalf("期望空私钥返回空字符串，实际: %s", pubKey)
	}
}

func TestDerivePublicKey_WhitespaceOnly(t *testing.T) {
	pubKey, err := DerivePublicKey("   \n\t  ")
	if err != nil {
		t.Fatalf("期望纯空白私钥不返回错误，实际: %v", err)
	}
	if pubKey != "" {
		t.Fatalf("期望纯空白私钥返回空字符串，实际: %s", pubKey)
	}
}

func TestDerivePublicKey_RSA(t *testing.T) {
	// 复用已有 helper 生成 RSA 私钥，验证 DerivePublicKey 也支持 RSA
	privPEM := buildRSAPrivateKeyForTest(t)

	pubKey, err := DerivePublicKey(privPEM)
	if err != nil {
		t.Fatalf("期望成功派生 RSA 公钥，实际失败: %v", err)
	}
	if !strings.HasPrefix(pubKey, "ssh-rsa ") {
		t.Fatalf("期望公钥以 ssh-rsa 开头，实际: %s", pubKey)
	}
}
