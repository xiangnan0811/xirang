package auth

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"

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

// ValidateAndConsumeRecoveryCode 验证恢复码并消费（删除）已使用的码。
// 返回剩余的恢复码列表和是否验证成功。
func ValidateAndConsumeRecoveryCode(storedJSON, code string) ([]string, bool) {
	var codes []string
	if err := json.Unmarshal([]byte(storedJSON), &codes); err != nil {
		return nil, false
	}
	remaining := make([]string, 0, len(codes))
	found := false
	for _, c := range codes {
		if c == code {
			found = true
			continue
		}
		remaining = append(remaining, c)
	}
	if !found {
		return nil, false
	}
	return remaining, true
}
