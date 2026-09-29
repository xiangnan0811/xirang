package model

import (
	"time"

	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

// Sanitized 返回去除敏感字段（密码、私钥）的节点副本，用于 API 响应。
func (n Node) Sanitized() Node {
	safe := n
	safe.Password = ""
	safe.PrivateKey = ""
	if safe.SSHKey != nil {
		keyCopy := *safe.SSHKey
		keyCopy.PrivateKey = ""
		safe.SSHKey = &keyCopy
	}
	return safe
}

type Node struct {
	ID                 uint       `gorm:"primaryKey" json:"id"`
	Name               string     `gorm:"size:128;not null;uniqueIndex" json:"name"`
	Host               string     `gorm:"size:255;not null" json:"host"`
	Port               int        `gorm:"not null;default:22" json:"port"`
	Username           string     `gorm:"size:128;not null" json:"username"`
	AuthType           string     `gorm:"size:32;not null;default:key" json:"auth_type"`
	Password           string     `gorm:"size:255" json:"password,omitempty"`
	PrivateKey         string     `gorm:"type:text" json:"private_key,omitempty"`
	SSHKeyID           *uint      `gorm:"index" json:"ssh_key_id"`
	SSHKey             *SSHKey    `json:"ssh_key,omitempty"`
	Tags               string     `gorm:"size:512" json:"tags"`
	Status             string     `gorm:"size:32;not null;default:offline" json:"status"` // Most recent manual connection-test result, not live availability.
	BasePath           string     `gorm:"size:255" json:"base_path"`
	BackupDir          string     `gorm:"size:128;not null;uniqueIndex" json:"backup_dir"`
	UseSudo            bool       `gorm:"not null;default:false" json:"use_sudo"`
	ConnectionLatency  int        `gorm:"not null;default:0" json:"connection_latency_ms"` // Most recent successful manual connection-test latency.
	LastSeenAt         *time.Time `json:"last_seen_at"`                                    // Most recent successful manual connection test.
	LastBackupAt       *time.Time `json:"last_backup_at"`
	MaintenanceStart   *time.Time `json:"maintenance_start,omitempty"`
	MaintenanceEnd     *time.Time `json:"maintenance_end,omitempty"`
	ExpiryDate         *time.Time `gorm:"" json:"expiry_date,omitempty"`
	Archived           bool       `gorm:"not null;default:false" json:"archived"`
	EscalationPolicyID *uint      `gorm:"index" json:"escalation_policy_id"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

func (n *Node) BeforeSave(_ *gorm.DB) error {
	if n.Password != "" {
		encrypted, err := secure.EncryptIfNeeded(n.Password)
		if err != nil {
			return err
		}
		n.Password = encrypted
	}
	if n.PrivateKey != "" {
		encrypted, err := secure.EncryptIfNeeded(n.PrivateKey)
		if err != nil {
			return err
		}
		n.PrivateKey = encrypted
	}
	return nil
}

func (n *Node) AfterFind(_ *gorm.DB) error {
	if n.Password != "" {
		decrypted, err := secure.DecryptIfNeeded(n.Password)
		if err != nil {
			return err
		}
		n.Password = decrypted
	}
	if n.PrivateKey != "" {
		decrypted, err := secure.DecryptIfNeeded(n.PrivateKey)
		if err != nil {
			return err
		}
		n.PrivateKey = decrypted
	}
	return nil
}

// NodeOwner 节点 ownership 关联表（operator 只能访问自己负责的节点）
type NodeOwner struct {
	NodeID    uint      `gorm:"primaryKey" json:"node_id"`
	UserID    uint      `gorm:"primaryKey" json:"user_id"`
	User      User      `gorm:"foreignKey:UserID" json:"user"`
	CreatedAt time.Time `json:"created_at"`
}
