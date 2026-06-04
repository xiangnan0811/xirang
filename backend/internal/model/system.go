package model

import (
	"time"
)

// SystemSetting 系统设置（key-value 存储，DB 覆盖值 → 环境变量 → 代码默认值）
type SystemSetting struct {
	Key       string    `gorm:"primaryKey;size:128" json:"key"`
	Value     string    `gorm:"type:text;not null" json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}
