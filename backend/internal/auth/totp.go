package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

const recoveryCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
const recoveryCodeLength = 8
const recoveryCodeCount = 8

// GenerateTOTPSecret 生成新的 TOTP 密钥，返回 OTP key 对象。
func GenerateTOTPSecret(issuer, account string) (*otp.Key, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: account,
	})
	if err != nil {
		return nil, fmt.Errorf("生成 TOTP 密钥失败: %w", err)
	}
	return key, nil
}

// ValidateTOTP 验证用户提交的 TOTP 验证码。
func ValidateTOTP(secret, code string) bool {
	return totp.Validate(code, secret)
}

// GenerateRecoveryCodes 生成 8 个随机恢复码，每个 8 位大写字母数字。
// 返回明文恢复码，仅在生成后向用户展示一次。
func GenerateRecoveryCodes() ([]string, error) {
	codes := make([]string, recoveryCodeCount)
	alphabetLen := big.NewInt(int64(len(recoveryCodeAlphabet)))
	for i := range codes {
		buf := make([]byte, recoveryCodeLength)
		for j := range buf {
			n, err := rand.Int(rand.Reader, alphabetLen)
			if err != nil {
				return nil, fmt.Errorf("生成恢复码失败: %w", err)
			}
			buf[j] = recoveryCodeAlphabet[n.Int64()]
		}
		codes[i] = string(buf)
	}
	return codes, nil
}

// HashRecoveryCodes 使用 bcrypt 哈希恢复码，用于安全存储。
// 返回与 codes 顺序一致的 bcrypt 哈希字符串列表。
func HashRecoveryCodes(codes []string) ([]string, error) {
	hashed := make([]string, len(codes))
	for i, code := range codes {
		hash, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("哈希恢复码失败: %w", err)
		}
		hashed[i] = string(hash)
	}
	return hashed, nil
}

// ValidateAndConsumeRecoveryCode 验证恢复码并消费（删除）已使用的码。
// storedJSON 是 JSON 数组，包含 bcrypt 哈希（新格式，以 "$2" 开头）或旧版明文恢复码。
// 返回剩余码列表（保持原有存储格式不变）及验证是否成功。
func ValidateAndConsumeRecoveryCode(storedJSON, code string) ([]string, bool) {
	var stored []string
	if err := json.Unmarshal([]byte(storedJSON), &stored); err != nil {
		return nil, false
	}
	if len(stored) == 0 {
		return nil, false
	}

	remaining := make([]string, 0, len(stored))
	found := false
	for _, s := range stored {
		var matched bool
		if strings.HasPrefix(s, "$2") {
			// bcrypt 哈希格式（新版存储方式）
			if bcrypt.CompareHashAndPassword([]byte(s), []byte(code)) == nil {
				matched = true
			}
		} else {
			// 旧版明文存储方式，保持兼容但打印警告
			if subtle.ConstantTimeCompare([]byte(s), []byte(code)) == 1 {
				matched = true
				log.Println("WARNING: 检测到旧版明文恢复码，请重新生成恢复码")
			}
		}
		if matched {
			found = true
			continue
		}
		remaining = append(remaining, s)
	}

	if !found {
		return nil, false
	}
	return remaining, true
}
