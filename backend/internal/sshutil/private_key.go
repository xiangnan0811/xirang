package sshutil

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"regexp"
	"strings"

	"xirang/backend/internal/secure"

	"golang.org/x/crypto/ssh"
)

const (
	SSHKeyTypeAuto    = "auto"
	SSHKeyTypeRSA     = "rsa"
	SSHKeyTypeED25519 = "ed25519"
	SSHKeyTypeECDSA   = "ecdsa"
)

var privateKeyBlockRegex = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]+ PRIVATE KEY-----.*?-----END [A-Z0-9 ]+ PRIVATE KEY-----`)

func NormalizePrivateKeyMaterial(raw string) string {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return ""
	}

	if (strings.HasPrefix(normalized, "\"") && strings.HasSuffix(normalized, "\"")) ||
		(strings.HasPrefix(normalized, "'") && strings.HasSuffix(normalized, "'")) {
		normalized = strings.TrimSpace(normalized[1 : len(normalized)-1])
	}

	normalized = strings.ReplaceAll(normalized, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	normalized = strings.ReplaceAll(normalized, "\\r\\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\\r", "\n")

	if matched := privateKeyBlockRegex.FindString(normalized); strings.TrimSpace(matched) != "" {
		normalized = matched
	}

	lines := strings.Split(normalized, "\n")
	cleanLines := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if trimmed == "" {
			continue
		}
		cleanLines = append(cleanLines, trimmed)
	}

	normalized = strings.Join(cleanLines, "\n")
	normalized = strings.TrimSpace(normalized)
	if normalized != "" && !strings.HasSuffix(normalized, "\n") {
		normalized += "\n"
	}
	return normalized
}

func NormalizeKeyType(input string) string {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case SSHKeyTypeRSA:
		return SSHKeyTypeRSA
	case SSHKeyTypeED25519:
		return SSHKeyTypeED25519
	case SSHKeyTypeECDSA:
		return SSHKeyTypeECDSA
	default:
		return SSHKeyTypeAuto
	}
}

func keyTypeLabel(keyType string) string {
	switch NormalizeKeyType(keyType) {
	case SSHKeyTypeRSA:
		return "RSA"
	case SSHKeyTypeED25519:
		return "ED25519"
	case SSHKeyTypeECDSA:
		return "ECDSA"
	default:
		return "AUTO"
	}
}

func detectPrivateKeyType(normalizedKey string) (string, error) {
	rawKey, err := ssh.ParseRawPrivateKey([]byte(normalizedKey))
	if err != nil {
		return "", err
	}

	switch rawKey.(type) {
	case *rsa.PrivateKey:
		return SSHKeyTypeRSA, nil
	case ed25519.PrivateKey:
		return SSHKeyTypeED25519, nil
	case *ecdsa.PrivateKey:
		return SSHKeyTypeECDSA, nil
	default:
		return SSHKeyTypeAuto, nil
	}
}

func KeyTypeMatches(selectedKeyType, detectedKeyType string) bool {
	selected := NormalizeKeyType(selectedKeyType)
	detected := NormalizeKeyType(detectedKeyType)
	if selected == SSHKeyTypeAuto || detected == SSHKeyTypeAuto {
		return true
	}
	return selected == detected
}

func normalizePrivateKeyParseError(err error) error {
	if err == nil {
		return fmt.Errorf("私钥格式无效，请粘贴 OpenSSH 私钥内容（含 BEGIN/END）")
	}

	raw := strings.TrimSpace(err.Error())
	lower := strings.ToLower(raw)

	switch {
	case strings.Contains(lower, "passphrase") || strings.Contains(lower, "encrypted"):
		return fmt.Errorf("私钥已加密且需要口令，暂不支持，请提供无口令私钥")
	case strings.Contains(lower, "no key found"):
		return fmt.Errorf("私钥格式无效，请粘贴完整 OpenSSH 私钥内容（含 BEGIN/END）")
	default:
		return fmt.Errorf("私钥格式无效，请粘贴 OpenSSH 私钥内容（含 BEGIN/END），解析详情: %s", raw)
	}
}

func convertPrivateKeyToPEM(normalizedKey string) (string, bool) {
	rawKey, err := ssh.ParseRawPrivateKey([]byte(normalizedKey))
	if err != nil {
		return "", false
	}

	encode := func(blockType string, content []byte) (string, bool) {
		encoded := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: content})
		if len(encoded) == 0 {
			return "", false
		}
		return string(encoded), true
	}

	switch key := rawKey.(type) {
	case *rsa.PrivateKey:
		return encode("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key))
	case *ecdsa.PrivateKey:
		der, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			return "", false
		}
		return encode("EC PRIVATE KEY", der)
	case ed25519.PrivateKey:
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return "", false
		}
		return encode("PRIVATE KEY", der)
	default:
		der, err := x509.MarshalPKCS8PrivateKey(rawKey)
		if err != nil {
			return "", false
		}
		return encode("PRIVATE KEY", der)
	}
}

func ValidateAndPreparePrivateKey(rawKey string, selectedKeyType string) (string, string, error) {
	rawTrimmed := strings.TrimSpace(rawKey)
	if strings.HasPrefix(rawTrimmed, "enc:v1:") {
		decrypted, err := secure.DecryptIfNeeded(rawTrimmed)
		if err != nil {
			return "", SSHKeyTypeAuto, fmt.Errorf("检测到加密密文 enc:v1，但解密失败，请检查 DATA_ENCRYPTION_KEY 是否与入库时一致")
		}
		rawKey = decrypted
	}

	normalized := NormalizePrivateKeyMaterial(rawKey)
	if normalized == "" {
		return "", SSHKeyTypeAuto, fmt.Errorf("私钥不能为空")
	}

	if _, err := ssh.ParsePrivateKey([]byte(normalized)); err != nil {
		return "", SSHKeyTypeAuto, normalizePrivateKeyParseError(err)
	}

	detectedType, err := detectPrivateKeyType(normalized)
	if err != nil {
		return "", SSHKeyTypeAuto, normalizePrivateKeyParseError(err)
	}

	normalizedSelectedType := NormalizeKeyType(selectedKeyType)
	if !KeyTypeMatches(normalizedSelectedType, detectedType) {
		return "", SSHKeyTypeAuto, fmt.Errorf("密钥类型不匹配：你选择的是 %s，但解析结果是 %s", keyTypeLabel(normalizedSelectedType), keyTypeLabel(detectedType))
	}

	prepared := normalized
	if pemKey, ok := convertPrivateKeyToPEM(normalized); ok {
		prepared = pemKey
	}

	storedType := detectedType
	if normalizedSelectedType != SSHKeyTypeAuto {
		storedType = normalizedSelectedType
	}
	return prepared, storedType, nil
}
