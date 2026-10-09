package model

import "time"

// CronBackupHealth stores the durable alert cursor for one cron backup state
// source. The source key is the canonical hash of the state directory and
// engine, while SourceID and HighestRevision fence state replacement and
// rollback.
type CronBackupHealth struct {
	SourceKey       string    `gorm:"column:source_key;primaryKey" json:"-"`
	SourceID        string    `gorm:"column:source_id" json:"-"`
	HighestRevision int64     `gorm:"column:highest_revision" json:"-"`
	EnrolledAt      time.Time `gorm:"column:enrolled_at" json:"-"`
	FaultActive     bool      `gorm:"column:fault_active" json:"-"`
	AlertID         *uint     `gorm:"column:alert_id" json:"-"`
	UpdatedAt       time.Time `gorm:"column:updated_at" json:"-"`
}

// TableName returns the versioned persistence table name.
func (CronBackupHealth) TableName() string { return "cron_backup_health" }

// CronBackupHealthUsage is a permanent, single-row proof that the cron backup
// health schema has been used. It remains after health rows are removed so a
// destructive downgrade cannot erase historical evidence.
type CronBackupHealthUsage struct {
	ID int `gorm:"column:id;primaryKey" json:"-"`
}

// TableName returns the versioned persistence table name.
func (CronBackupHealthUsage) TableName() string { return "cron_backup_health_usage" }
