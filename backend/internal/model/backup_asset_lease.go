package model

import "time"

type RecoveryPointLease struct {
	ID               string     `gorm:"primaryKey;size:32" json:"id"`
	RecoveryPointID  string     `gorm:"size:32;not null" json:"recovery_point_id"`
	HolderType       string     `gorm:"size:32;not null" json:"holder_type"`
	OwnerID          string     `gorm:"size:64;not null" json:"owner_id"`
	AttemptID        string     `gorm:"size:32;not null" json:"-"`
	FenceToken       string     `gorm:"size:64;not null" json:"-"`
	Status           string     `gorm:"size:16;not null" json:"status"`
	LeaseExpiresAt   time.Time  `gorm:"not null" json:"lease_expires_at"`
	AbsoluteDeadline time.Time  `gorm:"not null" json:"absolute_deadline"`
	LastHeartbeatAt  time.Time  `gorm:"not null" json:"last_heartbeat_at"`
	ReleasedAt       *time.Time `json:"released_at,omitempty"`
	CreatedAt        time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt        time.Time  `gorm:"not null" json:"updated_at"`
}

func (RecoveryPointLease) TableName() string { return "recovery_point_leases" }
