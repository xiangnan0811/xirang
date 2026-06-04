package model

import (
	"time"
)

// ReportConfig SLA 报告配置
type ReportConfig struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	Name           string    `gorm:"size:128;not null;uniqueIndex" json:"name"`
	ScopeType      string    `gorm:"size:32;not null;default:all" json:"scope_type"` // all | tag | node_ids
	ScopeValue     string    `gorm:"type:text;not null;default:''" json:"scope_value"`
	Period         string    `gorm:"size:32;not null;default:weekly" json:"period"` // weekly | monthly
	Cron           string    `gorm:"size:128;not null" json:"cron"`
	IntegrationIDs string    `gorm:"type:text;not null;default:'[]'" json:"integration_ids"` // JSON array
	Enabled        bool      `gorm:"not null;default:true" json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Report 已生成的 SLA 报告
type Report struct {
	ID               uint          `gorm:"primaryKey" json:"id"`
	ConfigID         uint          `gorm:"not null;index" json:"config_id"`
	Config           *ReportConfig `gorm:"foreignKey:ConfigID" json:"config"`
	PeriodStart      time.Time     `gorm:"not null;index" json:"period_start"`
	PeriodEnd        time.Time     `gorm:"not null" json:"period_end"`
	TotalRuns        int           `gorm:"not null;default:0" json:"total_runs"`
	SuccessRuns      int           `gorm:"not null;default:0" json:"success_runs"`
	FailedRuns       int           `gorm:"not null;default:0" json:"failed_runs"`
	SuccessRate      float64       `gorm:"not null;default:0" json:"success_rate"`
	AvgDurationMs    int64         `gorm:"not null;default:0" json:"avg_duration_ms"`
	ActualRPOMinutes *int          `json:"actual_rpo_minutes"`
	ActualRTOMinutes *int          `json:"actual_rto_minutes"`
	RPOCompliant     *bool         `json:"rpo_compliant"`
	RTOCompliant     *bool         `json:"rto_compliant"`
	TopFailures      string        `gorm:"type:text;not null;default:'[]'" json:"top_failures"` // JSON
	DiskTrend        string        `gorm:"type:text;not null;default:'[]'" json:"disk_trend"`   // JSON
	GeneratedAt      time.Time     `gorm:"not null" json:"generated_at"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}
