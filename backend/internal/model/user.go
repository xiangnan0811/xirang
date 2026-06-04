package model

import (
	"time"

	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

type User struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	Username      string    `gorm:"size:64;uniqueIndex;not null" json:"username"`
	PasswordHash  string    `gorm:"size:255;not null" json:"-"`
	Role          string    `gorm:"size:32;not null;index" json:"role"`
	TOTPSecret    string    `gorm:"size:255" json:"-"`
	TOTPEnabled   bool      `json:"totp_enabled"`
	RecoveryCodes string    `gorm:"type:text" json:"-"`
	TokenVersion  uint      `gorm:"not null;default:0" json:"-"`
	Onboarded     bool      `gorm:"not null;default:false" json:"onboarded"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// User TOTP 敏感字段加解密 hooks

func (u *User) BeforeSave(_ *gorm.DB) error {
	if u.TOTPSecret != "" {
		encrypted, err := secure.EncryptIfNeeded(u.TOTPSecret)
		if err != nil {
			return err
		}
		u.TOTPSecret = encrypted
	}
	if u.RecoveryCodes != "" {
		encrypted, err := secure.EncryptIfNeeded(u.RecoveryCodes)
		if err != nil {
			return err
		}
		u.RecoveryCodes = encrypted
	}
	return nil
}

func (u *User) AfterFind(_ *gorm.DB) error {
	if u.TOTPSecret != "" {
		decrypted, err := secure.DecryptIfNeeded(u.TOTPSecret)
		if err != nil {
			return err
		}
		u.TOTPSecret = decrypted
	}
	if u.RecoveryCodes != "" {
		decrypted, err := secure.DecryptIfNeeded(u.RecoveryCodes)
		if err != nil {
			return err
		}
		u.RecoveryCodes = decrypted
	}
	return nil
}

type SSHKey struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:128;not null;uniqueIndex" json:"name"`
	// PrivateKey 永远不通过 JSON 序列化暴露——所有 handler 都通过 sshKeyResponseItem
	// + toSSHKeyResponse() 脱敏，此处 json:"-" 是深度防御，防未来误写 c.JSON(model.SSHKey{...})
	Username        string     `gorm:"size:128;not null" json:"username"`
	KeyType         string     `gorm:"size:32;not null;default:auto" json:"key_type"`
	PrivateKey      string     `gorm:"type:text;not null" json:"-"`
	Fingerprint     string     `gorm:"size:255;not null" json:"fingerprint"`
	Disabled        bool       `gorm:"not null;default:false" json:"disabled"`
	ExpiresAt       *time.Time `gorm:"index" json:"expires_at"`
	AllowedPurposes string     `gorm:"type:text;not null;default:''" json:"allowed_purposes"`
	AllowedNodeIDs  string     `gorm:"type:text;not null;default:''" json:"allowed_node_ids"`
	AllowedNodeTags string     `gorm:"type:text;not null;default:''" json:"allowed_node_tags"`
	LastUsedAt      *time.Time `json:"last_used_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (s *SSHKey) BeforeSave(_ *gorm.DB) error {
	if s.PrivateKey == "" {
		return nil
	}
	encrypted, err := secure.EncryptIfNeeded(s.PrivateKey)
	if err != nil {
		return err
	}
	s.PrivateKey = encrypted
	return nil
}

func (s *SSHKey) AfterFind(_ *gorm.DB) error {
	if s.PrivateKey == "" {
		return nil
	}
	decrypted, err := secure.DecryptIfNeeded(s.PrivateKey)
	if err != nil {
		return err
	}
	s.PrivateKey = decrypted
	return nil
}

// LoginFailure 登录失败记录，持久化存储以防重启绕过锁定。
type LoginFailure struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	Username    string     `gorm:"size:64;not null;uniqueIndex:idx_login_failures_user_ip" json:"username"`
	ClientIP    string     `gorm:"size:45;not null;uniqueIndex:idx_login_failures_user_ip" json:"client_ip"`
	FailCount   int        `gorm:"not null;default:0" json:"fail_count"`
	LockedUntil *time.Time `json:"locked_until"`
	UpdatedAt   time.Time  `json:"updated_at"`
}
