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

	"xirang/backend/internal/util"
)

const (
	encryptedPrefix = "enc:v1:"
	defaultDevKey   = "xirang-dev-encryption-key-change-me"
)

var (
	loadOnce sync.Once
	keyBytes []byte
	keyErr   error
)

func loadKey() {
	raw := strings.TrimSpace(os.Getenv("DATA_ENCRYPTION_KEY"))
	if raw == "" {
		if !util.IsDevelopmentEnv() {
			keyErr = fmt.Errorf("必须设置 DATA_ENCRYPTION_KEY（仅 APP_ENV=development 可省略）")
			return
		}
		raw = defaultDevKey
	}

	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil && len(decoded) >= 32 {
		keyBytes = append([]byte(nil), decoded[:32]...)
		return
	}

	sum := sha256.Sum256([]byte(raw))
	keyBytes = append([]byte(nil), sum[:]...)
}

// isDevelopmentEnv 已迁移至 util.IsDevelopmentEnv

func getKey() ([]byte, error) {
	loadOnce.Do(loadKey)
	if keyErr != nil {
		return nil, keyErr
	}
	if len(keyBytes) != 32 {
		return nil, errors.New("无效的数据加密密钥")
	}
	return keyBytes, nil
}

func IsEncrypted(raw string) bool {
	return strings.HasPrefix(raw, encryptedPrefix)
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

func EncryptString(raw string) (string, error) {
	key, err := getKey()
	if err != nil {
		return "", err
	}

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
	return encryptedPrefix + base64.StdEncoding.EncodeToString(packed), nil
}

func DecryptString(raw string) (string, error) {
	if !IsEncrypted(raw) {
		return raw, nil
	}

	key, err := getKey()
	if err != nil {
		return "", err
	}

	encoded := strings.TrimPrefix(raw, encryptedPrefix)
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
