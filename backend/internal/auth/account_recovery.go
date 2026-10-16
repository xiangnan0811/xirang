package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
)

var (
	// ErrRecoveryEnrollmentInvalid deliberately collapses stale, consumed,
	// expired and incorrectly scoped credentials into one safe 401 outcome.
	ErrRecoveryEnrollmentInvalid = errors.New("恢复绑定凭据无效或已过期")
	// ErrRecoveryEnrollmentInternal includes unacknowledged COMMIT outcomes.
	// Callers must not replay the operation or suggest that it rolled back.
	ErrRecoveryEnrollmentInternal = errors.New("恢复绑定操作失败，结果可能未知，请返回登录")
)

type RecoveryEnrollmentResult struct {
	Token string
	Setup TOTPSetupResult
}

// GenerateRecoveryEnrollmentToken signs only a restricted enrollment capability.
// It does not register a pending login or establish a primary session. Its
// lifetime and persisted enrollment share the same exact whole-second bounds.
func (m *JWTManager) GenerateRecoveryEnrollmentToken(user model.User, enrollmentID string, expiresAt time.Time) (string, error) {
	if m == nil {
		return "", ErrRecoveryEnrollmentInternal
	}
	expiresAt = expiresAt.UTC().Truncate(time.Second)
	now := time.Now().UTC()
	issuedAt := expiresAt.Add(-TOTPEnrollmentTTL)
	if user.ID == 0 || !isKnownRecoveryCodeRole(user.Role) || !user.TOTPReenrollmentRequired || user.TOTPEnabled ||
		!lowerHexID(enrollmentID) ||
		!expiresAt.After(now) || issuedAt.After(now) {
		return "", ErrRecoveryEnrollmentInvalid
	}
	jti, err := generateTokenID()
	if err != nil {
		return "", ErrRecoveryEnrollmentInternal
	}
	claims := Claims{
		UserID: user.ID, Username: user.Username, Role: user.Role,
		TokenVersion: user.TokenVersion, Purpose: PurposeRecoveryEnrollment,
		EnrollmentID: enrollmentID,
		RegisteredClaims: jwt.RegisteredClaims{
			ID: jti, Subject: strconv.FormatUint(uint64(user.ID), 10),
			IssuedAt: jwt.NewNumericDate(issuedAt), ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	signed, err := m.signToken(jwt.NewWithClaims(jwt.SigningMethodHS256, claims))
	if err != nil || signed == "" {
		return "", ErrRecoveryEnrollmentInternal
	}
	return signed, nil
}

// beginRecoveryEnrollment must receive the exact snapshot whose password was
// verified by Login, never a freshly reloaded identity. In particular, an old
// password verification cannot be upgraded to a post-reset token version.
func (s *Service) beginRecoveryEnrollment(ctx context.Context, verified model.User) (*RecoveryEnrollmentResult, error) {
	if s == nil || s.db == nil || s.jwt == nil {
		return nil, ErrRecoveryEnrollmentInternal
	}
	var result RecoveryEnrollmentResult
	err := withTOTPActivationTransaction(ctx, recoveryCodeSafeDB(s.db, ctx), func(tx *gorm.DB) error {
		row, err := loadStoredIdentityUser(tx, verified.ID, true)
		if err != nil {
			return err
		}
		if row.PasswordHash != verified.PasswordHash || row.TokenVersion != verified.TokenVersion || row.Role != verified.Role ||
			!isKnownRecoveryCodeRole(row.Role) || !row.TOTPReenrollmentRequired || row.TOTPEnabled {
			return ErrRecoveryEnrollmentInvalid
		}
		// Validate existing ciphertext before replacing an abandoned enrollment.
		user, err := row.user()
		if err != nil {
			return err
		}
		key, err := GenerateTOTPSecret("息壤 XiRang", row.Username)
		if err != nil {
			return err
		}
		enrollmentID, err := generateTokenID()
		if err != nil {
			return err
		}
		expiresAt := identityNow(s).Truncate(time.Second).Add(TOTPEnrollmentTTL)
		encrypted, err := secure.EncryptString(key.Secret())
		if err != nil {
			return err
		}
		token, err := s.jwt.GenerateRecoveryEnrollmentToken(user, enrollmentID, expiresAt)
		if err != nil {
			return err
		}
		write := tx.Table("users").Where(
			"id = ? AND password_hash = ? AND role = ? AND token_version = ? AND totp_reenrollment_required = ? AND totp_enabled = ? AND totp_secret = ? AND totp_enrollment_id = ?",
			row.ID, verified.PasswordHash, verified.Role, verified.TokenVersion, true, false, row.TOTPSecret, row.TOTPEnrollmentID,
		).Updates(map[string]any{
			"totp_secret": encrypted, "totp_enrollment_id": enrollmentID, "totp_enrollment_expires_at": expiresAt,
		})
		if write.Error != nil {
			return write.Error
		}
		if write.RowsAffected != 1 {
			return ErrRecoveryEnrollmentInvalid
		}
		if err := deletePendingTokens(tx, row.ID); err != nil {
			return err
		}
		result = RecoveryEnrollmentResult{Token: token, Setup: TOTPSetupResult{
			Secret: key.Secret(), QRURL: key.URL(), Issuer: "息壤 XiRang", EnrollmentID: enrollmentID, ExpiresAt: expiresAt,
		}}
		return nil
	})
	if err != nil {
		return nil, recoveryEnrollmentError(err)
	}
	return &result, nil
}

func recoveryEnrollmentError(err error) error {
	switch {
	case errors.Is(err, ErrRecoveryEnrollmentInvalid), errors.Is(err, gorm.ErrRecordNotFound):
		return ErrRecoveryEnrollmentInvalid
	case errors.Is(err, ErrTOTPCodeInvalid):
		return ErrTOTPCodeInvalid
	default:
		return ErrRecoveryEnrollmentInternal
	}
}

func (s *Service) recoveryEnrollmentClaims(token string) (*Claims, error) {
	parsed, err := jwt.ParseWithClaims(token, &Claims{}, func(*jwt.Token) (interface{}, error) {
		return s.jwt.secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithStrictDecoding(), jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(), jwt.WithTimeFunc(func() time.Time { return identityNow(s) }))
	if err != nil || !parsed.Valid {
		return nil, ErrRecoveryEnrollmentInvalid
	}
	claims, ok := parsed.Claims.(*Claims)
	if !ok || claims.Purpose != PurposeRecoveryEnrollment || claims.UserID == 0 || !isKnownRecoveryCodeRole(claims.Role) ||
		claims.Subject != strconv.FormatUint(uint64(claims.UserID), 10) || !lowerHexID(claims.ID) || !lowerHexID(claims.EnrollmentID) ||
		claims.IssuedAt == nil || claims.ExpiresAt == nil || claims.NotBefore != nil ||
		claims.SessionID != "" || claims.StepUpAction != "" || claims.TOTPBinding != "" ||
		!claims.ExpiresAt.Equal(claims.IssuedAt.Add(TOTPEnrollmentTTL)) || !claims.ExpiresAt.After(identityNow(s)) {
		return nil, ErrRecoveryEnrollmentInvalid
	}
	return claims, nil
}

// CompleteRecoveryEnrollment publishes recovery codes only after one confirmed
// commit. It intentionally never signs or returns a primary login token.
func (s *Service) CompleteRecoveryEnrollment(ctx context.Context, token, code string) ([]string, error) {
	if s == nil || s.db == nil || s.jwt == nil {
		return nil, ErrRecoveryEnrollmentInternal
	}
	claims, err := s.recoveryEnrollmentClaims(token)
	if err != nil {
		return nil, err
	}
	// Only a fresh six-digit TOTP is accepted, never a recovery code.
	validCode := len(code) == 6
	for _, c := range code {
		if c < '0' || c > '9' {
			validCode = false
		}
	}
	var generated []string
	err = withTOTPActivationTransaction(ctx, recoveryCodeSafeDB(s.db, ctx), func(tx *gorm.DB) error {
		row, err := loadStoredIdentityUser(tx, claims.UserID, true)
		if err != nil {
			return err
		}
		now := identityNow(s)
		if !claims.ExpiresAt.After(now) || claims.IssuedAt.After(now) || row.Role != claims.Role || row.TokenVersion != claims.TokenVersion ||
			!row.TOTPReenrollmentRequired || row.TOTPEnabled || row.TOTPEnrollmentID != claims.EnrollmentID ||
			!row.TOTPEnrollmentExpiresAt.Valid || !row.TOTPEnrollmentExpiresAt.Time.Equal(claims.ExpiresAt.Time) ||
			!row.TOTPEnrollmentExpiresAt.Time.After(now) || row.TOTPSecret == "" {
			return ErrRecoveryEnrollmentInvalid
		}
		user, err := row.user()
		if err != nil {
			return err
		}
		if !validCode || !ValidateTOTP(user.TOTPSecret, code) {
			return ErrTOTPCodeInvalid
		}
		codes, err := GenerateRecoveryCodes()
		if err != nil {
			return err
		}
		hashes, err := HashRecoveryCodes(codes)
		if err != nil {
			return err
		}
		inventory, err := json.Marshal(hashes)
		if err != nil {
			return err
		}
		encrypted, err := secure.EncryptString(string(inventory))
		if err != nil {
			return err
		}
		operationID, err := generateTokenID()
		if err != nil {
			return err
		}
		write := tx.Table("users").Where(
			"id = ? AND role = ? AND token_version = ? AND totp_reenrollment_required = ? AND totp_enabled = ? AND totp_secret = ? AND totp_enrollment_id = ? AND totp_enrollment_expires_at = ?",
			row.ID, claims.Role, claims.TokenVersion, true, false, row.TOTPSecret, claims.EnrollmentID, claims.ExpiresAt.Time.UTC(),
		).Updates(map[string]any{
			"totp_enabled": true, "totp_reenrollment_required": false, "recovery_codes": encrypted,
			"totp_enrollment_id": "", "totp_enrollment_expires_at": nil, "token_version": gorm.Expr("token_version + 1"),
		})
		if write.Error != nil {
			return write.Error
		}
		if write.RowsAffected != 1 {
			return ErrRecoveryEnrollmentInvalid
		}
		if err := deletePendingTokens(tx, row.ID); err != nil {
			return err
		}
		if err := tx.Create(&model.AccountRecoveryAudit{
			OperationID: operationID, TargetUserID: row.ID, TargetUsername: row.Username,
			Event:           model.AccountRecoveryEventReenrollmentCompleted,
			OldTokenVersion: row.TokenVersion, NewTokenVersion: row.TokenVersion + 1, CreatedAt: now,
		}).Error; err != nil {
			return err
		}
		generated = codes
		return nil
	})
	if err != nil {
		return nil, recoveryEnrollmentError(err)
	}
	return generated, nil
}
