package sshutil

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"

	"xirang/backend/internal/secure"

	"golang.org/x/crypto/ssh"
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
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "xirang-test-encryption-key-for-sshutil")
	secure.ResetForTesting()
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
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "")
	secure.ResetForTesting()
	_, _, err := ValidateAndPreparePrivateKey("enc:v1:abc123", SSHKeyTypeAuto)
	if err == nil {
		t.Fatalf("期望无效 enc:v1 密文被拒绝")
	}
	if !strings.Contains(err.Error(), "DATA_ENCRYPTION_KEY") && !strings.Contains(err.Error(), "解密") {
		t.Fatalf("期望返回解密相关失败提示，实际: %v", err)
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

func buildED25519OpenSSHPrivateKeyForTest(t *testing.T) string {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成 ED25519 测试私钥失败: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatalf("编码 ED25519 OpenSSH 测试私钥失败: %v", err)
	}
	return string(pem.EncodeToMemory(block))
}

func TestValidateAndPreparePrivateKeyDetectsOpenSSHED25519AndRejectsMismatch(t *testing.T) {
	key := buildED25519OpenSSHPrivateKeyForTest(t)

	prepared, detectedType, err := ValidateAndPreparePrivateKey(key, SSHKeyTypeAuto)
	if err != nil {
		t.Fatalf("OpenSSH ED25519 私钥自动识别失败: %v", err)
	}
	if detectedType != SSHKeyTypeED25519 {
		t.Fatalf("OpenSSH ED25519 私钥识别为 %q，期望 %q", detectedType, SSHKeyTypeED25519)
	}
	original, err := ssh.ParsePrivateKey([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	validated, err := ssh.ParsePrivateKey([]byte(prepared))
	if err != nil {
		t.Fatal(err)
	}
	if ssh.FingerprintSHA256(original.PublicKey()) != ssh.FingerprintSHA256(validated.PublicKey()) {
		t.Fatal("validation changed the candidate key identity")
	}

	if _, _, err := ValidateAndPreparePrivateKey(key, SSHKeyTypeRSA); err == nil {
		t.Fatal("OpenSSH ED25519 私钥使用 rsa 类型应被拒绝")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestSamePrivateKeyMaterialAcrossContainersAndNormalization(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, ed25519Key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]any{"rsa": rsaKey, "ecdsa": ecdsaKey, "ed25519": ed25519Key} {
		t.Run(name, func(t *testing.T) {
			der, err := x509.MarshalPKCS8PrivateKey(key)
			if err != nil {
				t.Fatal(err)
			}
			pkcs8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
			block, err := ssh.MarshalPrivateKey(key, "first comment")
			if err != nil {
				t.Fatal(err)
			}
			openSSH := string(pem.EncodeToMemory(block))
			otherBlock, err := ssh.MarshalPrivateKey(key, "different comment")
			if err != nil {
				t.Fatal(err)
			}
			variants := []string{
				pkcs8, openSSH, string(pem.EncodeToMemory(otherBlock)),
				strings.ReplaceAll(openSSH, "\n", "\r\n"),
				strings.ReplaceAll(openSSH, "\n", "\\n"),
				" \n" + pkcs8 + "\t",
			}
			switch typed := key.(type) {
			case *rsa.PrivateKey:
				variants = append(variants, string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(typed)})))
			case *ecdsa.PrivateKey:
				ecDER, err := x509.MarshalECPrivateKey(typed)
				if err != nil {
					t.Fatal(err)
				}
				variants = append(variants, string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ecDER})))
			}
			for index, variant := range variants {
				if !SamePrivateKeyMaterial(pkcs8, variant) || !SamePrivateKeyMaterial(variant, pkcs8) || !SamePrivateKeyMaterial(openSSH, variant) {
					t.Fatalf("same private material rejected for container/normalization variant %d", index)
				}
			}
			if name == "ed25519" {
				pemKey, err := ssh.ParseRawPrivateKey([]byte(pkcs8))
				if err != nil {
					t.Fatal(err)
				}
				openSSHKey, err := ssh.ParseRawPrivateKey([]byte(openSSH))
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := pemKey.(ed25519.PrivateKey); !ok {
					t.Fatal("PKCS8 fixture must exercise ED25519 value")
				}
				if _, ok := openSSHKey.(*ed25519.PrivateKey); !ok {
					t.Fatal("OpenSSH fixture must exercise ED25519 pointer")
				}
			}
		})
	}
}

func TestSamePrivateKeyMaterialRejectsDifferentOrInvalidMaterial(t *testing.T) {
	first, second := buildRSAPrivateKeyForTest(t), buildRSAPrivateKeyForTest(t)
	ed25519Key := buildED25519OpenSSHPrivateKeyForTest(t)
	for _, pair := range [][2]string{
		{first, second}, {first, ed25519Key}, {"", first}, {first, " \t\n"},
		{"broken", first}, {first, "broken"}, {"", ""}, {"broken", "broken"},
		{"enc:v1:invalid", first},
	} {
		if SamePrivateKeyMaterial(pair[0], pair[1]) {
			t.Fatal("different, empty, encrypted or invalid material must not compare equal")
		}
	}
}

func TestSamePrivateKeyMaterialComparesPrivateComponentsNotPublicFingerprint(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	original := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	swapped := &rsa.PrivateKey{
		PublicKey: key.PublicKey, D: new(big.Int).Set(key.D),
		Primes: []*big.Int{new(big.Int).Set(key.Primes[1]), new(big.Int).Set(key.Primes[0])},
	}
	reordered := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(swapped)}))
	left, err := ssh.ParsePrivateKey([]byte(original))
	if err != nil {
		t.Fatal(err)
	}
	right, err := ssh.ParsePrivateKey([]byte(reordered))
	if err != nil {
		t.Fatal(err)
	}
	if ssh.FingerprintSHA256(left.PublicKey()) != ssh.FingerprintSHA256(right.PublicKey()) {
		t.Fatal("fixture must retain the public key")
	}
	if key.Equal(swapped) || SamePrivateKeyMaterial(original, reordered) {
		t.Fatal("private comparison must follow standard-library equality, including RSA prime order")
	}
}

func TestSamePrivateKeyMaterialRejectsUnsupportedDSA(t *testing.T) {
	// A parse-only toy DSA group exercises unsupported-key rejection without
	// generating or using deprecated DSA signing keys: 2^3 mod 23 = 8.
	der, err := asn1.Marshal(struct {
		Version int
		P, Q, G *big.Int
		Y, X    *big.Int
	}{0, big.NewInt(23), big.NewInt(11), big.NewInt(2), big.NewInt(8), big.NewInt(3)})
	if err != nil {
		t.Fatal(err)
	}
	material := string(pem.EncodeToMemory(&pem.Block{Type: "DSA PRIVATE KEY", Bytes: der}))
	if _, err := ssh.ParseRawPrivateKey([]byte(material)); err != nil {
		t.Fatal("DSA fixture must be parseable")
	}
	if SamePrivateKeyMaterial(material, material) {
		t.Fatal("parseable but unsupported DSA must not compare equal")
	}
}
