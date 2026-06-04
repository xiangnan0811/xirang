package model

import (
	"encoding/json"
	"time"

	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

// AppCredential 独立凭据资源，用于 Policy 引用数据库连接信息。
// Config 字段存储完整 JSON（含 password），通过 GORM hooks 加解密。
// API 响应不返回原始 Config，改为返回 SanitizedConfig() 的结果。
type AppCredential struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:128;not null;uniqueIndex" json:"name"`
	Type        string    `gorm:"size:32;not null" json:"type"`
	Description string    `gorm:"size:255" json:"description"`
	Config      string    `gorm:"type:text;not null;default:'{}'" json:"-"`
	HasPassword bool      `gorm:"-" json:"has_password"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (a *AppCredential) BeforeSave(_ *gorm.DB) error {
	if a.Config != "" && !secure.IsEncrypted(a.Config) {
		encrypted, err := secure.EncryptIfNeeded(a.Config)
		if err != nil {
			return err
		}
		a.Config = encrypted
	}
	return nil
}

func (a *AppCredential) AfterFind(_ *gorm.DB) error {
	if a.Config != "" {
		decrypted, err := secure.DecryptIfNeeded(a.Config)
		if err != nil {
			return err
		}
		a.Config = decrypted
	}
	return nil
}

// SanitizedConfig 返回去除 password 的配置 JSON map，用于 API 响应。
func (a *AppCredential) SanitizedConfig() map[string]interface{} {
	raw := map[string]interface{}{}
	if err := json.Unmarshal([]byte(a.Config), &raw); err != nil {
		return map[string]interface{}{}
	}
	delete(raw, "password")
	return raw
}

type Integration struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	Type            string    `gorm:"size:32;not null" json:"type"`
	Name            string    `gorm:"size:128;not null;uniqueIndex" json:"name"`
	Endpoint        string    `gorm:"type:text;not null" json:"endpoint"`
	Secret          string    `gorm:"size:512" json:"-"`
	HasSecret       bool      `gorm:"-" json:"has_secret"`
	Enabled         bool      `gorm:"not null;default:true" json:"enabled"`
	FailThreshold   int       `gorm:"not null;default:1" json:"fail_threshold"`
	CooldownMinutes int       `gorm:"not null;default:5" json:"cooldown_minutes"`
	ProxyURL        string    `gorm:"size:512;not null;default:''" json:"proxy_url"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (i *Integration) BeforeSave(_ *gorm.DB) error {
	if i.Endpoint != "" {
		encrypted, err := secure.EncryptIfNeeded(i.Endpoint)
		if err != nil {
			return err
		}
		i.Endpoint = encrypted
	}
	if i.Secret != "" {
		encrypted, err := secure.EncryptIfNeeded(i.Secret)
		if err != nil {
			return err
		}
		i.Secret = encrypted
	}
	if i.ProxyURL != "" {
		encrypted, err := secure.EncryptIfNeeded(i.ProxyURL)
		if err != nil {
			return err
		}
		i.ProxyURL = encrypted
	}
	return nil
}

func (i *Integration) AfterFind(_ *gorm.DB) error {
	if i.Endpoint != "" {
		decrypted, err := secure.DecryptIfNeeded(i.Endpoint)
		if err != nil {
			return err
		}
		i.Endpoint = decrypted
	}
	if i.Secret != "" {
		decrypted, err := secure.DecryptIfNeeded(i.Secret)
		if err != nil {
			return err
		}
		i.Secret = decrypted
	}
	if i.ProxyURL != "" {
		decrypted, err := secure.DecryptIfNeeded(i.ProxyURL)
		if err != nil {
			return err
		}
		i.ProxyURL = decrypted
	}
	i.HasSecret = i.Secret != ""
	return nil
}
