package model

import "time"

// BatchCommand retains the requester-scoped idempotency identity even after deletion.
// It intentionally stores a request hash rather than a second copy of command text.
type BatchCommand struct {
	ID             string     `gorm:"size:64;primaryKey" json:"batch_id"`
	RequesterID    uint       `gorm:"not null;uniqueIndex:idx_batch_commands_request_key" json:"-"`
	IdempotencyKey string     `gorm:"size:256;not null;uniqueIndex:idx_batch_commands_request_key" json:"-"`
	RequestHash    string     `gorm:"size:64;not null" json:"-"`
	Retain         bool       `gorm:"not null;default:false" json:"retain"`
	DeletedAt      *time.Time `json:"-"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// BatchCommandDispatch describes initial dispatch, not the result of task execution.
// Keep these receipts after task deletion so a replay cannot create new commands.
type BatchCommandDispatch struct {
	TaskID    uint      `gorm:"primaryKey;autoIncrement:false" json:"task_id"`
	BatchID   string    `gorm:"size:64;not null;index" json:"batch_id"`
	NodeID    uint      `gorm:"not null" json:"node_id"`
	Status    string    `gorm:"size:32;not null" json:"status"`
	RunID     uint      `gorm:"not null;default:0" json:"run_id"`
	LastError string    `gorm:"type:text;not null;default:''" json:"last_error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
