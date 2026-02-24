package auth

import (
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func CheckPassword(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

func ValidatePasswordStrength(password string) error {
	trimmed := strings.TrimSpace(password)
	if len(trimmed) < 12 {
		return fmt.Errorf("密码长度至少 12 位")
	}

	var hasUpper, hasLower, hasDigit, hasSpecial bool
	for _, ch := range trimmed {
		switch {
		case unicode.IsUpper(ch):
			hasUpper = true
		case unicode.IsLower(ch):
			hasLower = true
		case unicode.IsDigit(ch):
			hasDigit = true
		case unicode.IsPunct(ch) || unicode.IsSymbol(ch):
			hasSpecial = true
		}
	}

	if !hasUpper || !hasLower || !hasDigit || !hasSpecial {
		return fmt.Errorf("密码必须同时包含大写字母、小写字母、数字和特殊字符")
	}
	return nil
}
