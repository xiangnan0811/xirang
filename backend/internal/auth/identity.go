package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/dbtx"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// TOTPEnrollmentTTL bounds the time an unverified setup secret remains
	// usable. Expired enrollments are cleared on the next setup/verify attempt.
	TOTPEnrollmentTTL        = 10 * time.Minute
	identityMutationAttempts = 4
)

var (
	ErrLastAdmin              = errors.New("至少保留一名管理员")
	ErrSecurityConflict       = errors.New("身份安全状态已变更，请重试")
	ErrTOTPAlreadyEnabled     = errors.New("两步验证已启用")
	ErrTOTPEnrollmentRequired = errors.New("请先调用 setup 接口生成密钥")
	ErrTOTPEnrollmentExpired  = errors.New("两步验证设置已过期，请重新生成密钥")
	ErrTOTPEnrollmentConflict = errors.New("两步验证设置已被替换，请使用最新设置")
	ErrTOTPCodeInvalid        = errors.New("验证码错误")
	ErrPendingLoginInvalid    = errors.New("登录令牌无效或已过期")
	ErrRecoveryCodeInvalid    = errors.New("验证码错误")

	errRecoveryCASConflict = errors.New("恢复码状态已变更")
)

type TOTPSetupResult struct {
	Secret       string
	QRURL        string
	Issuer       string
	EnrollmentID string
	ExpiresAt    time.Time
}

type TOTPVerifyResult struct {
	RecoveryCodes []string
}

type TOTPLoginResult struct {
	Token string
	User  model.User
}

type storedIdentityUser struct {
	ID                      uint         `gorm:"column:id"`
	Username                string       `gorm:"column:username"`
	PasswordHash            string       `gorm:"column:password_hash"`
	Role                    string       `gorm:"column:role"`
	TOTPSecret              string       `gorm:"column:totp_secret"`
	TOTPEnabled             bool         `gorm:"column:totp_enabled"`
	RecoveryCodes           string       `gorm:"column:recovery_codes"`
	TokenVersion            uint         `gorm:"column:token_version"`
	TOTPEnrollmentID        string       `gorm:"column:totp_enrollment_id"`
	TOTPEnrollmentExpiresAt sql.NullTime `gorm:"column:totp_enrollment_expires_at"`
}

func (row storedIdentityUser) user() (model.User, error) {
	secret, err := secure.DecryptIfNeeded(row.TOTPSecret)
	if err != nil {
		return model.User{}, fmt.Errorf("解密 TOTP 密钥失败: %w", err)
	}
	recoveryCodes, err := secure.DecryptIfNeeded(row.RecoveryCodes)
	if err != nil {
		return model.User{}, fmt.Errorf("解密恢复码失败: %w", err)
	}
	user := model.User{
		ID:               row.ID,
		Username:         row.Username,
		PasswordHash:     row.PasswordHash,
		Role:             row.Role,
		TOTPSecret:       secret,
		TOTPEnabled:      row.TOTPEnabled,
		RecoveryCodes:    recoveryCodes,
		TokenVersion:     row.TokenVersion,
		TOTPEnrollmentID: row.TOTPEnrollmentID,
	}
	if row.TOTPEnrollmentExpiresAt.Valid {
		expiresAt := row.TOTPEnrollmentExpiresAt.Time
		user.TOTPEnrollmentExpiresAt = &expiresAt
	}
	return user, nil
}

func loadStoredIdentityUser(tx *gorm.DB, userID uint, lock bool) (storedIdentityUser, error) {
	query := tx.Table("users")
	if lock && tx.Name() != "sqlite" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row storedIdentityUser
	result := query.Select(
		"id", "username", "password_hash", "role", "totp_secret", "totp_enabled",
		"recovery_codes", "token_version", "totp_enrollment_id", "totp_enrollment_expires_at",
	).Where("id = ?", userID).Take(&row)
	if result.Error != nil {
		return storedIdentityUser{}, result.Error
	}
	return row, nil
}

func identityNow(s *Service) time.Time {
	if s != nil && s.nowFunc != nil {
		return s.nowFunc().UTC()
	}
	return time.Now().UTC()
}

func withIdentityTransaction(ctx context.Context, db *gorm.DB, body func(*gorm.DB) error) error {
	if db == nil {
		return errors.New("身份数据库未初始化")
	}
	return dbtx.WithSQLiteBusyRetryTx(ctx, db, body)
}

// SerializeIdentityMutationLock takes a database-backed serialization lock
// shared by role mutations and break-glass recovery. Locking every user row
// also covers the zero-admin case, where a role-filtered update would lock no
// rows and allow two recovery processes to promote different accounts.
func SerializeIdentityMutationLock(tx *gorm.DB) error {
	return tx.Model(&model.User{}).
		Where("1 = 1").
		UpdateColumn("updated_at", gorm.Expr("updated_at")).Error
}

func countAdmins(tx *gorm.DB) (int64, error) {
	var count int64
	if err := tx.Model(&model.User{}).Where("role = ?", "admin").Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func deletePendingTokens(tx *gorm.DB, userID uint) error {
	return tx.Where("user_id = ?", userID).Delete(&model.PendingAuthToken{}).Error
}

func (s *Service) SetupTOTP(ctx context.Context, userID uint, account string) (*TOTPSetupResult, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("身份数据库未初始化")
	}
	if userID == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	key, err := GenerateTOTPSecret("息壤 XiRang", account)
	if err != nil {
		return nil, err
	}
	enrollmentID, err := generateTokenID()
	if err != nil {
		return nil, err
	}
	now := identityNow(s)
	expiresAt := now.Add(TOTPEnrollmentTTL)
	// This is a write path, so encrypt explicitly rather than relying on a
	// receiver hook (map updates do not have a populated User receiver).
	encryptedSecret, err := secure.EncryptString(key.Secret())
	if err != nil {
		return nil, fmt.Errorf("加密 TOTP 密钥失败: %w", err)
	}

	err = withIdentityTransaction(ctx, s.db, func(tx *gorm.DB) error {
		row, err := loadStoredIdentityUser(tx, userID, true)
		if err != nil {
			return err
		}
		if row.TOTPEnabled {
			return ErrTOTPAlreadyEnabled
		}
		result := tx.Table("users").Where(
			"id = ? AND token_version = ? AND totp_enabled = ? AND totp_secret = ? AND totp_enrollment_id = ?",
			userID, row.TokenVersion, false, row.TOTPSecret, row.TOTPEnrollmentID,
		).Updates(map[string]any{
			"totp_secret":                encryptedSecret,
			"totp_enrollment_id":         enrollmentID,
			"totp_enrollment_expires_at": expiresAt,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrSecurityConflict
		}
		return deletePendingTokens(tx, userID)
	})
	if err != nil {
		return nil, err
	}
	return &TOTPSetupResult{
		Secret:       key.Secret(),
		QRURL:        key.URL(),
		Issuer:       "息壤 XiRang",
		EnrollmentID: enrollmentID,
		ExpiresAt:    expiresAt,
	}, nil
}

func (s *Service) VerifyTOTP(ctx context.Context, userID uint, code, enrollmentID string) (*TOTPVerifyResult, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("身份数据库未初始化")
	}
	code = strings.TrimSpace(code)
	enrollmentID = strings.TrimSpace(enrollmentID)
	if code == "" {
		return nil, ErrTOTPCodeInvalid
	}
	if enrollmentID == "" {
		return nil, ErrTOTPEnrollmentRequired
	}

	var verified TOTPVerifyResult
	var outcome error
	err := withIdentityTransaction(ctx, s.db, func(tx *gorm.DB) error {
		row, err := loadStoredIdentityUser(tx, userID, true)
		if err != nil {
			return err
		}
		user, err := row.user()
		if err != nil {
			return err
		}
		if user.TOTPEnabled {
			return ErrTOTPAlreadyEnabled
		}
		if user.TOTPEnrollmentID == "" || user.TOTPSecret == "" {
			return ErrTOTPEnrollmentRequired
		}
		if subtle.ConstantTimeCompare([]byte(enrollmentID), []byte(user.TOTPEnrollmentID)) != 1 {
			return ErrTOTPEnrollmentConflict
		}
		if user.TOTPEnrollmentExpiresAt == nil || !user.TOTPEnrollmentExpiresAt.After(identityNow(s)) {
			// Commit cleanup even though this request reports expiration. This
			// prevents abandoned secrets from remaining eligible indefinitely.
			result := tx.Table("users").Where(
				"id = ? AND token_version = ? AND totp_enabled = ? AND totp_secret = ? AND totp_enrollment_id = ?",
				userID, row.TokenVersion, false, row.TOTPSecret, row.TOTPEnrollmentID,
			).Updates(map[string]any{
				"totp_secret":                "",
				"totp_enrollment_id":         "",
				"totp_enrollment_expires_at": nil,
			})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrSecurityConflict
			}
			if err := deletePendingTokens(tx, userID); err != nil {
				return err
			}
			outcome = ErrTOTPEnrollmentExpired
			return nil
		}
		if !ValidateTOTP(user.TOTPSecret, code) {
			return ErrTOTPCodeInvalid
		}

		recoveryCodes, err := GenerateRecoveryCodes()
		if err != nil {
			return err
		}
		hashedRecoveryCodes, err := HashRecoveryCodes(recoveryCodes)
		if err != nil {
			return err
		}
		hashedRecoveryJSON, err := json.Marshal(hashedRecoveryCodes)
		if err != nil {
			return err
		}
		encryptedSecret, err := secure.EncryptString(user.TOTPSecret)
		if err != nil {
			return fmt.Errorf("加密 TOTP 密钥失败: %w", err)
		}
		encryptedRecovery, err := secure.EncryptString(string(hashedRecoveryJSON))
		if err != nil {
			return fmt.Errorf("加密恢复码失败: %w", err)
		}
		result := tx.Table("users").Where(
			"id = ? AND token_version = ? AND totp_enabled = ? AND totp_secret = ? AND totp_enrollment_id = ?",
			userID, row.TokenVersion, false, row.TOTPSecret, row.TOTPEnrollmentID,
		).Updates(map[string]any{
			"totp_secret":                encryptedSecret,
			"totp_enabled":               true,
			"recovery_codes":             encryptedRecovery,
			"totp_enrollment_id":         "",
			"totp_enrollment_expires_at": nil,
			"token_version":              gorm.Expr("token_version + 1"),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrSecurityConflict
		}
		if err := deletePendingTokens(tx, userID); err != nil {
			return err
		}
		verified.RecoveryCodes = recoveryCodes
		return nil
	})
	if err != nil {
		return nil, err
	}
	if outcome != nil {
		return nil, outcome
	}
	return &verified, nil
}

func (s *Service) DisableTOTP(ctx context.Context, userID uint, password, code string) error {
	if s == nil || s.db == nil {
		return errors.New("身份数据库未初始化")
	}
	// Passwords are opaque values. Unlike the TOTP code, preserve the exact
	// bytes supplied by the caller for bcrypt verification.
	code = strings.TrimSpace(code)
	return withIdentityTransaction(ctx, s.db, func(tx *gorm.DB) error {
		row, err := loadStoredIdentityUser(tx, userID, true)
		if err != nil {
			return err
		}
		user, err := row.user()
		if err != nil {
			return err
		}
		if !user.TOTPEnabled || user.TOTPSecret == "" {
			return ErrTOTPCodeInvalid
		}
		if err := CheckPassword(user.PasswordHash, password); err != nil {
			return fmt.Errorf("密码错误")
		}
		if !ValidateTOTP(user.TOTPSecret, code) {
			return ErrTOTPCodeInvalid
		}
		result := tx.Table("users").Where(
			"id = ? AND token_version = ? AND totp_enabled = ? AND totp_secret = ?",
			userID, row.TokenVersion, true, row.TOTPSecret,
		).Updates(map[string]any{
			"totp_secret":                "",
			"totp_enabled":               false,
			"recovery_codes":             "",
			"totp_enrollment_id":         "",
			"totp_enrollment_expires_at": nil,
			"token_version":              gorm.Expr("token_version + 1"),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrSecurityConflict
		}
		return deletePendingTokens(tx, userID)
	})
}

func (s *Service) Complete2FALogin(ctx context.Context, loginToken, code string) (*TOTPLoginResult, error) {
	if s == nil || s.db == nil || s.jwt == nil {
		return nil, errors.New("身份认证服务未初始化")
	}
	claims, err := s.jwt.ParseToken(loginToken)
	if err != nil || claims == nil || claims.Purpose != Purpose2FAPending || claims.UserID == 0 || !lowerHexID(claims.ID) || claims.TOTPBinding == "" || claims.ExpiresAt == nil {
		return nil, ErrPendingLoginInvalid
	}
	if !claims.ExpiresAt.After(identityNow(s)) {
		return nil, ErrPendingLoginInvalid
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, ErrRecoveryCodeInvalid
	}

	for attempt := 0; attempt < identityMutationAttempts; attempt++ {
		var user model.User
		err = withIdentityTransaction(ctx, s.db, func(tx *gorm.DB) error {
			// Lock the user before the pending row. Password/role/MFA changes
			// follow this same order before deleting pending rows, preventing a
			// cross-connection deadlock during invalidation.
			row, err := loadStoredIdentityUser(tx, claims.UserID, true)
			if err != nil {
				return err
			}
			user, err = row.user()
			if err != nil {
				return err
			}
			if user.TokenVersion != claims.TokenVersion || user.Role != claims.Role || !user.TOTPEnabled || user.TOTPSecret == "" || TOTPBindingDigest(user.TOTPSecret) != claims.TOTPBinding {
				return ErrPendingLoginInvalid
			}

			pendingQuery := tx.Where("jti = ?", claims.ID)
			if tx.Name() != "sqlite" {
				pendingQuery = pendingQuery.Clauses(clause.Locking{Strength: "UPDATE"})
			}
			var pending model.PendingAuthToken
			if err := pendingQuery.Take(&pending).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrPendingLoginInvalid
				}
				return err
			}
			if pending.UserID != claims.UserID || pending.TokenVersion != claims.TokenVersion || pending.TOTPBinding != claims.TOTPBinding || pending.ConsumedAt != nil || !pending.ExpiresAt.After(identityNow(s)) {
				return ErrPendingLoginInvalid
			}

			recoveryUpdate := ""
			if !ValidateTOTP(user.TOTPSecret, code) {
				if user.RecoveryCodes == "" {
					return ErrRecoveryCodeInvalid
				}
				remaining, ok := ValidateAndConsumeRecoveryCode(user.RecoveryCodes, code)
				if !ok {
					return ErrRecoveryCodeInvalid
				}
				remainingJSON, err := json.Marshal(remaining)
				if err != nil {
					return err
				}
				recoveryUpdate, err = secure.EncryptString(string(remainingJSON))
				if err != nil {
					return fmt.Errorf("加密恢复码失败: %w", err)
				}
				// Include the old encrypted value in the update predicate. This
				// prevents a stale decrypted snapshot from restoring another
				// request's consumed code.
				result := tx.Table("users").Where(
					"id = ? AND token_version = ? AND totp_enabled = ? AND totp_secret = ? AND recovery_codes = ?",
					user.ID, row.TokenVersion, true, row.TOTPSecret, row.RecoveryCodes,
				).Update("recovery_codes", recoveryUpdate)
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return errRecoveryCASConflict
				}
			}

			consumedAt := identityNow(s)
			result := tx.Model(&model.PendingAuthToken{}).Where(
				"jti = ? AND user_id = ? AND consumed_at IS NULL AND expires_at > ?",
				claims.ID, claims.UserID, consumedAt,
			).Update("consumed_at", consumedAt)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrPendingLoginInvalid
			}
			return nil
		})
		if errors.Is(err, errRecoveryCASConflict) {
			continue
		}
		if err != nil {
			return nil, err
		}
		token, err := s.jwt.GenerateToken(user)
		if err != nil {
			return nil, err
		}
		return &TOTPLoginResult{Token: token, User: user}, nil
	}
	return nil, ErrSecurityConflict
}
