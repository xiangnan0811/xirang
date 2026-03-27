package config

const BackupRoot = "/backup"

// DisplayTimeFormat 统一的时间显示格式（用于 CSV 导出等本地消费场景）
const DisplayTimeFormat = "2006-01-02 15:04:05"

// DisplayTimeFormatTZ 带时区偏移的显示格式（用于跨时区接收的通知、告警等场景）
const DisplayTimeFormatTZ = "2006-01-02 15:04:05 -07:00"
