package model

import "time"

const (
	AccountRecoveryEventReset                 = "reset"
	AccountRecoveryEventReenrollmentCompleted = "reenrollment_completed"
)

// AccountRecoveryAudit retains offline recovery evidence independently of the
// target user's lifetime. It must never contain credentials or enrollment data.
type AccountRecoveryAudit struct {
	ID                uint      `gorm:"primaryKey" json:"id"`
	OperationID       string    `gorm:"size:32;not null;uniqueIndex:idx_account_recovery_audits_operation_id" json:"operation_id"`
	TargetUserID      uint      `gorm:"not null;index" json:"target_user_id"`
	TargetUsername    string    `gorm:"size:64;not null" json:"target_username"`
	Event             string    `gorm:"size:32;not null" json:"event"`
	ResetPassword     bool      `gorm:"not null" json:"reset_password"`
	ResetTOTP         bool      `gorm:"not null" json:"reset_totp"`
	Reason            string    `gorm:"type:text;not null" json:"reason"`
	OldTokenVersion   uint      `gorm:"not null" json:"old_token_version"`
	NewTokenVersion   uint      `gorm:"not null" json:"new_token_version"`
	RevokedGrantCount int64     `gorm:"not null" json:"revoked_grant_count"`
	CreatedAt         time.Time `gorm:"not null;index" json:"created_at"`
}
