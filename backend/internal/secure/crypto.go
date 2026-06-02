package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/util"
)

const (
	encryptedPrefixV1 = "enc:v1:"
	encryptedPrefixV2 = "enc:v2:"
)

// argon2id 参数（OWASP 推荐的最低配置）
var kdfSalt = []byte("xirang-argon2id-kdf-v2")

const (
	argon2Time    = 1
	argon2Memory  = 64 * 1024 // 64 MB
	argon2Threads = 4
	argon2KeyLen  = 32
)

var (
	loadOnce   sync.Once
	primaryKey []byte // v2 key (argon2id derived or raw base64)
	legacyKey  []byte // v1 key (sha256 derived or raw base64)
	keyErr     error
)

func loadKey() {
	raw := strings.TrimSpace(os.Getenv("DATA_ENCRYPTION_KEY"))
	if raw == "" {
		if !util.IsDevelopmentEnv() {
			keyErr = fmt.Errorf("必须设置 DATA_ENCRYPTION_KEY（仅 APP_ENV=development 可省略）")
			return
		}
		// 开发环境下未设置 DATA_ENCRYPTION_KEY：生成随机密钥并 WARNING 提示。
		// 使用此密钥加密的数据在重启后无法解密，因此开发环境建议显式设置。
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			keyErr = fmt.Errorf("生成临时加密密钥失败: %w", err)
			return
		}
		generated := base64.StdEncoding.EncodeToString(buf)
		raw = generated
		logger.Log.Warn().
			Str("hint", "set DATA_ENCRYPTION_KEY to a stable value for data persistence across restarts").
			Str("generated_key", generated).
			Msg("开发环境未设置 DATA_ENCRYPTION_KEY，已生成临时密钥，重启后数据将不可解密")
	}

	primary, legacy, err := deriveKeyPair(raw)
	if err != nil {
		keyErr = err
		return
	}
	primaryKey = primary
	legacyKey = legacy

	legacyRaw := strings.TrimSpace(os.Getenv("DATA_ENCRYPTION_LEGACY_KEY"))
	if legacyRaw != "" {
		legacy, err := deriveLegacyKey(legacyRaw)
		if err != nil {
			keyErr = err
			return
		}
		legacyKey = legacy
	}
}

func deriveKeyPair(raw string) ([]byte, []byte, error) {
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil && len(decoded) >= 32 {
		key := append([]byte(nil), decoded[:32]...)
		return key, key, nil
	}

	primary := argon2.IDKey([]byte(raw), kdfSalt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	legacy, err := deriveLegacyKey(raw)
	if err != nil {
		return nil, nil, err
	}
	return primary, legacy, nil
}

func deriveLegacyKey(raw string) ([]byte, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("DATA_ENCRYPTION_LEGACY_KEY 不能为空")
	}
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil && len(decoded) >= 32 {
		return append([]byte(nil), decoded[:32]...), nil
	}
	sum := sha256.Sum256([]byte(raw))
	return append([]byte(nil), sum[:]...), nil
}

func getPrimaryKey() ([]byte, error) {
	loadOnce.Do(loadKey)
	if keyErr != nil {
		return nil, keyErr
	}
	if len(primaryKey) != 32 {
		return nil, errors.New("无效的数据加密密钥")
	}
	return primaryKey, nil
}

func getLegacyKey() ([]byte, error) {
	loadOnce.Do(loadKey)
	if keyErr != nil {
		return nil, keyErr
	}
	if len(legacyKey) != 32 {
		return nil, errors.New("无效的数据加密密钥")
	}
	return legacyKey, nil
}

func IsEncrypted(raw string) bool {
	return strings.HasPrefix(raw, encryptedPrefixV1) || strings.HasPrefix(raw, encryptedPrefixV2)
}

// IsV1Encrypted 判断是否为 v1 加密格式（SHA-256 KDF）。
func IsV1Encrypted(raw string) bool {
	return strings.HasPrefix(raw, encryptedPrefixV1)
}

func EncryptIfNeeded(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return raw, nil
	}
	if IsEncrypted(raw) {
		return raw, nil
	}
	return EncryptString(raw)
}

func DecryptIfNeeded(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return raw, nil
	}
	if !IsEncrypted(raw) {
		return raw, nil
	}
	return DecryptString(raw)
}

// EncryptString 使用 v2（argon2id）密钥加密。
func EncryptString(raw string) (string, error) {
	key, err := getPrimaryKey()
	if err != nil {
		return "", err
	}
	return encryptWithKey(raw, encryptedPrefixV2, key)
}

// DecryptString 自动检测 v1/v2 前缀并使用对应密钥解密。
func DecryptString(raw string) (string, error) {
	if strings.HasPrefix(raw, encryptedPrefixV2) {
		key, err := getPrimaryKey()
		if err != nil {
			return "", err
		}
		return decryptWithKey(raw, encryptedPrefixV2, key)
	}
	if strings.HasPrefix(raw, encryptedPrefixV1) {
		key, err := getLegacyKey()
		if err != nil {
			return "", err
		}
		return decryptWithKey(raw, encryptedPrefixV1, key)
	}
	return raw, nil
}

// ReEncryptV1Value 将 v1 加密值重新加密为 v2。返回 (新值, 是否变更, 错误)。
func ReEncryptV1Value(raw string) (string, bool, error) {
	if !IsV1Encrypted(raw) {
		return raw, false, nil
	}
	plain, err := DecryptString(raw)
	if err != nil {
		return "", false, err
	}
	encrypted, err := EncryptString(plain)
	if err != nil {
		return "", false, err
	}
	return encrypted, true, nil
}

func encryptWithKey(raw, prefix string, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	cipherText := gcm.Seal(nil, nonce, []byte(raw), nil)
	packed := append(nonce, cipherText...)
	return prefix + base64.StdEncoding.EncodeToString(packed), nil
}

// ResetForTesting 重置密钥状态，仅供测试使用。
func ResetForTesting() {
	loadOnce = sync.Once{}
	primaryKey = nil
	legacyKey = nil
	keyErr = nil
}

func decryptWithKey(raw, prefix string, key []byte) (string, error) {
	encoded := strings.TrimPrefix(raw, prefix)
	packed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("解密数据格式错误")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(packed) < nonceSize {
		return "", fmt.Errorf("解密数据长度不足")
	}
	nonce := packed[:nonceSize]
	cipherText := packed[nonceSize:]

	plain, err := gcm.Open(nil, nonce, cipherText, nil)
	if err != nil {
		return "", fmt.Errorf("解密失败")
	}
	return string(plain), nil
}
