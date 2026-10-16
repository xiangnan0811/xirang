package bootstrap

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"os"
	"strings"
	"time"
	"unicode"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/database"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// These errors deliberately contain no database, configuration or credential
// details. Only the explicitly pre-write errors below establish rejection.
var (
	ErrAccountRecoveryRequest            = errors.New("account recovery request is invalid")
	ErrAccountRecoveryOperationID        = errors.New("account recovery operation ID is invalid")
	ErrAccountRecoveryConfirmation       = errors.New("account recovery confirmation is invalid")
	ErrAccountRecoveryReason             = errors.New("account recovery reason is invalid")
	ErrAccountRecoveryPassword           = errors.New("account recovery password is invalid")
	ErrAccountRecoverySchema             = errors.New("account recovery requires a complete current clean schema")
	ErrAccountRecoveryTarget             = errors.New("account recovery target is unavailable or invalid")
	ErrAccountRecoveryKey                = errors.New("account recovery target credentials cannot be decrypted")
	ErrAccountRecoveryDuplicateOperation = errors.New("account recovery operation ID has already been used")
	ErrAccountRecoveryUnknown            = errors.New("account recovery outcome is unknown; observe the operation ID before any new operation")
	ErrAccountRecoveryStatus             = errors.New("account recovery status is unavailable")
	ErrAccountRecoveryRandom             = errors.New("account recovery operation ID generation failed")
)

type AccountRecoveryRequest struct {
	OperationID   string
	Username      string
	Reason        string
	Confirmation  string
	ResetPassword bool
	NewPassword   string
	ResetTOTP     bool
}

// GenerateAccountRecoveryOperationID returns an independent safe observation ID,
// not an authorization credential or an idempotent replay key.
func GenerateAccountRecoveryOperationID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", ErrAccountRecoveryRandom
	}
	return hex.EncodeToString(bytes[:]), nil
}

func validAccountRecoveryOperationID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, ch := range id {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

// Nullable fields preserve the exact stored ciphertext/null baseline. Model
// hooks must not decrypt or re-encrypt the values used by the compare-and-swap.
type accountRecoveryIdentity struct {
	ID                       uint
	Username                 string
	PasswordHash             string
	Role                     string
	TOTPSecret               sql.NullString
	TOTPEnabled              bool
	TOTPReenrollmentRequired bool
	RecoveryCodes            sql.NullString
	TokenVersion             uint
	TOTPEnrollmentID         sql.NullString
	TOTPEnrollmentExpiresAt  sql.NullTime
}

func accountRecoveryDB(ctx context.Context, db *gorm.DB) (*gorm.DB, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, breakGlassTimeout)
	// Silence the scoped SQL logger before any read or write involving hashes,
	// ciphertext or the confirmation. Never mutate the process-global logger.
	return db.Session(&gorm.Session{Logger: logger.Discard}).WithContext(ctx), cancel
}

// RecoverAccount is an offline-only, single-attempt transaction. The caller is
// responsible for configuration validation and stopping every application writer.
// It never seeds, migrates, issues tokens, or retries a failed transaction. An
// unknown error does not establish rollback: observe OperationID separately.
func RecoverAccount(ctx context.Context, db *gorm.DB, request AccountRecoveryRequest) error {
	if db == nil || (!request.ResetPassword && !request.ResetTOTP) ||
		(!request.ResetPassword && request.NewPassword != "") {
		return ErrAccountRecoveryRequest
	}
	if !validAccountRecoveryOperationID(request.OperationID) {
		return ErrAccountRecoveryOperationID
	}
	request.Username = strings.TrimSpace(request.Username)
	if request.Username == "" {
		return ErrAccountRecoveryRequest
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if len(request.Reason) < 8 || len(request.Reason) > breakGlassReasonMax || strings.ContainsFunc(request.Reason, unicode.IsControl) {
		return ErrAccountRecoveryReason
	}
	expected := strings.TrimSpace(os.Getenv(breakGlassConfirmationEnv))
	provided := strings.TrimSpace(request.Confirmation)
	if expected == "" || provided == "" || subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) != 1 {
		return ErrAccountRecoveryConfirmation
	}
	passwordHash := ""
	if request.ResetPassword {
		if auth.ValidatePasswordStrength(request.NewPassword) != nil {
			return ErrAccountRecoveryPassword
		}
		var err error
		// Hash the original bytes, including any surrounding whitespace.
		passwordHash, err = auth.HashPassword(request.NewPassword)
		if err != nil {
			return ErrAccountRecoveryPassword
		}
	}
	safeDB, cancel := accountRecoveryDB(ctx, db)
	defer cancel()
	if database.VerifyCurrentSchema(safeDB, safeDB.Name()) != nil {
		return ErrAccountRecoverySchema
	}

	var rejected error
	err := safeDB.Transaction(func(tx *gorm.DB) error {
		if err := auth.SerializeIdentityMutationLock(tx); err != nil {
			return err
		}
		query := tx.Table("users").Select("id", "username", "password_hash", "role", "totp_secret", "totp_enabled", "totp_reenrollment_required", "recovery_codes", "token_version", "totp_enrollment_id", "totp_enrollment_expires_at").Where("username = ?", request.Username)
		if tx.Name() != "sqlite" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var user accountRecoveryIdentity
		if err := query.Take(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				rejected = ErrAccountRecoveryTarget
				return rejected
			}
			return err
		}
		if user.ID == 0 || (user.Role != "admin" && user.Role != "operator" && user.Role != "viewer") || uint64(user.TokenVersion) >= math.MaxInt64 {
			rejected = ErrAccountRecoveryTarget
			return rejected
		}
		// Both encrypted fields must be readable even if this operation clears
		// them. Empty/plaintext fields do not prove a configured key matches DB.
		if _, err := secure.DecryptIfNeeded(user.TOTPSecret.String); err != nil {
			rejected = ErrAccountRecoveryKey
			return rejected
		}
		if _, err := secure.DecryptIfNeeded(user.RecoveryCodes.String); err != nil {
			rejected = ErrAccountRecoveryKey
			return rejected
		}
		var used int64
		if err := tx.Model(&model.AccountRecoveryAudit{}).Where("operation_id = ?", request.OperationID).Count(&used).Error; err != nil {
			return err
		}
		if used != 0 {
			rejected = ErrAccountRecoveryDuplicateOperation
			return rejected
		}

		now := time.Now().UTC()
		updates := map[string]any{
			"token_version":      gorm.Expr("token_version + 1"),
			"totp_enrollment_id": "", "totp_enrollment_expires_at": nil,
			"updated_at": now,
		}
		if request.ResetPassword {
			updates["password_hash"] = passwordHash
		}
		if request.ResetTOTP {
			updates["totp_secret"] = ""
			updates["recovery_codes"] = ""
			updates["totp_enabled"] = false
			updates["totp_reenrollment_required"] = true
		} else if !user.TOTPEnabled {
			// Disabled-factor secrets belong to unfinished enrollment, whereas
			// an enabled factor's active secret must survive password-only reset.
			updates["totp_secret"] = ""
		}
		result := tx.Table("users").Where(map[string]any{
			"id": user.ID, "username": user.Username, "role": user.Role,
			"password_hash": user.PasswordHash, "token_version": user.TokenVersion,
			"totp_enabled": user.TOTPEnabled, "totp_reenrollment_required": user.TOTPReenrollmentRequired,
			"totp_secret": user.TOTPSecret, "recovery_codes": user.RecoveryCodes,
			"totp_enrollment_id": user.TOTPEnrollmentID, "totp_enrollment_expires_at": user.TOTPEnrollmentExpiresAt,
		}).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrAccountRecoveryUnknown
		}
		if err := tx.Where("user_id = ?", user.ID).Delete(&model.PendingAuthToken{}).Error; err != nil {
			return err
		}
		grants := tx.Model(&model.CredentialAccessGrant{}).
			Where("requester_user_id = ? AND (status IS NULL OR status NOT IN ?)", user.ID, []string{"denied", "revoked", "expired"}).
			Updates(map[string]any{"status": "revoked", "revoked_at": now, "revoked_by_user_id": nil, "updated_at": now})
		if grants.Error != nil {
			return grants.Error
		}
		if err := tx.Create(&model.AccountRecoveryAudit{
			OperationID: request.OperationID, TargetUserID: user.ID, TargetUsername: user.Username,
			Event: model.AccountRecoveryEventReset, ResetPassword: request.ResetPassword, ResetTOTP: request.ResetTOTP,
			Reason: request.Reason, OldTokenVersion: user.TokenVersion, NewTokenVersion: user.TokenVersion + 1,
			RevokedGrantCount: grants.RowsAffected, CreatedAt: now,
		}).Error; err != nil {
			return err
		}
		// This describes a local CLI action against the actual target role, not
		// a claim that the target performed it. The dedicated row stores reason;
		// the existing general hash payload does not include that reason.
		return middleware.SaveAuditLogWithHashChain(tx, &model.AuditLog{
			UserID: user.ID, Username: user.Username, Role: user.Role,
			Method: "CLI", Path: "break-glass/account-recover", StatusCode: 200,
			ClientIP: "local", UserAgent: "xirang-recover-account", CreatedAt: now,
		})
	})
	if err == nil {
		return nil
	}
	if rejected != nil && errors.Is(err, rejected) {
		return rejected
	}
	return ErrAccountRecoveryUnknown
}

// ReadAccountRecoveryStatus only observes the dedicated durable audit. False
// means not currently observed, not proof that an in-flight operation rolled
// back. Callers must not use it as permission to automatically replay recovery.
func ReadAccountRecoveryStatus(ctx context.Context, db *gorm.DB, operationID string) (bool, error) {
	if !validAccountRecoveryOperationID(operationID) {
		return false, ErrAccountRecoveryOperationID
	}
	if db == nil {
		return false, ErrAccountRecoveryStatus
	}
	safeDB, cancel := accountRecoveryDB(ctx, db)
	defer cancel()
	if database.VerifyCurrentSchema(safeDB, safeDB.Name()) != nil {
		return false, ErrAccountRecoverySchema
	}
	var count int64
	if err := safeDB.Model(&model.AccountRecoveryAudit{}).Where("operation_id = ?", operationID).Count(&count).Error; err != nil {
		return false, ErrAccountRecoveryStatus
	}
	return count != 0, nil
}
