package logger

import (
	"os"
	"time"

	"github.com/rs/zerolog"
)

// Log 是全局结构化日志实例
var Log zerolog.Logger

// Init 初始化全局日志，level 可选: debug, info, warn, error
func Init(level string) {
	lvl, err := zerolog.ParseLevel(level)
	if err != nil {
		lvl = zerolog.InfoLevel
	}

	Log = zerolog.New(os.Stdout).
		Level(lvl).
		With().
		Timestamp().
		Logger()

	// 使用 RFC3339 时间格式
	zerolog.TimeFieldFormat = time.RFC3339
}

// Module 返回带有 module 字段的子 logger
func Module(name string) *zerolog.Logger {
	l := Log.With().Str("module", name).Logger()
	return &l
}
