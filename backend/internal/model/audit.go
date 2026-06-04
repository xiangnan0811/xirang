package model

import (
	"time"
)

type AuditLog struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	UserID     uint      `gorm:"index" json:"user_id"`
	Username   string    `gorm:"size:64;index" json:"username"`
	Role       string    `gorm:"size:32;index" json:"role"`
	Method     string    `gorm:"size:16;index" json:"method"`
	Path       string    `gorm:"size:255;index" json:"path"`
	StatusCode int       `gorm:"index" json:"status_code"`
	ClientIP   string    `gorm:"size:64" json:"client_ip"`
	UserAgent  string    `gorm:"size:255" json:"user_agent"`
	PrevHash   string    `gorm:"size:64;index" json:"prev_hash,omitempty"`
	EntryHash  string    `gorm:"size:64;index" json:"entry_hash,omitempty"`
	CreatedAt  time.Time `gorm:"index" json:"created_at"`
}

// CredentialAuditEvent stores domain-specific evidence that a credential was used
// or attempted for a high-risk operation. It must never contain raw secrets,
// terminal streams, command output, or executor config.
type CredentialAuditEvent struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	UserID           uint      `gorm:"index" json:"user_id"`
	Username         string    `gorm:"size:64;not null;index" json:"username"`
	Role             string    `gorm:"size:32;index" json:"role"`
	Action           string    `gorm:"size:64;not null;index" json:"action"`
	Purpose          string    `gorm:"size:64;not null;index" json:"purpose"`
	CredentialKind   string    `gorm:"size:32;not null;index" json:"credential_kind"`
	CredentialSource string    `gorm:"size:64;not null" json:"credential_source"`
	SSHKeyID         *uint     `gorm:"index" json:"ssh_key_id,omitempty"`
	NodeID           *uint     `gorm:"index" json:"node_id,omitempty"`
	TaskID           *uint     `gorm:"index" json:"task_id,omitempty"`
	TaskRunID        *uint     `gorm:"index" json:"task_run_id,omitempty"`
	PolicyID         *uint     `gorm:"index" json:"policy_id,omitempty"`
	Outcome          string    `gorm:"size:16;not null;index" json:"outcome"`
	ErrorMessage     string    `gorm:"type:text;not null;default:''" json:"error_message,omitempty"`
	Metadata         string    `gorm:"type:text;not null;default:'{}'" json:"metadata,omitempty"`
	ClientIP         string    `gorm:"size:64;not null;default:''" json:"client_ip"`
	UserAgent        string    `gorm:"size:255;not null;default:''" json:"user_agent"`
	CreatedAt        time.Time `gorm:"not null;index" json:"created_at"`
}

// CredentialAccessGrant stores a short-lived, operation-bound JIT grant for
// high-risk credential-use boundaries. It must only contain safe actor/resource
// identifiers, bounded sanitized reason text, lifecycle state, and timestamps.
type CredentialAccessGrant struct {
	ID                  uint       `gorm:"primaryKey" json:"id"`
	RequesterUserID     uint       `gorm:"not null;index" json:"requester_user_id"`
	RequesterUsername   string     `gorm:"size:64;not null;default:''" json:"requester_username"`
	RequesterRole       string     `gorm:"size:32;not null;default:''" json:"requester_role"`
	Action              string     `gorm:"size:64;not null;index:idx_credential_access_grants_operation" json:"action"`
	Purpose             string     `gorm:"size:64;not null;index:idx_credential_access_grants_operation" json:"purpose"`
	NodeID              *uint      `gorm:"index;index:idx_credential_access_grants_operation" json:"node_id,omitempty"`
	TaskID              *uint      `gorm:"index" json:"task_id,omitempty"`
	PolicyID            *uint      `gorm:"index" json:"policy_id,omitempty"`
	Reason              string     `gorm:"type:text;not null;default:''" json:"reason"`
	Status              string     `gorm:"size:16;not null;index" json:"status"`
	RequestedTTLSeconds int        `gorm:"not null;default:0" json:"requested_ttl_seconds"`
	RequestedAt         time.Time  `gorm:"not null;index" json:"requested_at"`
	ApprovedAt          *time.Time `json:"approved_at,omitempty"`
	ApproverUserID      *uint      `gorm:"index" json:"approver_user_id,omitempty"`
	ApproverUsername    string     `gorm:"size:64;not null;default:''" json:"approver_username"`
	ExpiresAt           time.Time  `gorm:"not null;index" json:"expires_at"`
	RevokedAt           *time.Time `json:"revoked_at,omitempty"`
	RevokedByUserID     *uint      `gorm:"index" json:"revoked_by_user_id,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}
