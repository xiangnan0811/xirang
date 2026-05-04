package logger

import (
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// Log 是全局结构化日志实例
var Log zerolog.Logger

// logFile 持有 LOG_FILE 打开的文件句柄；进程生命周期内一直打开，
// 由操作系统在退出时回收。导出为变量以便测试时可以重置。
var logFile *os.File

// Init 初始化全局日志，level 可选: debug, info, warn, error
//
// 输出目的地：
//   - 默认仅 stdout（容器场景由 docker daemon / journald 收集）
//   - 设置 LOG_FILE 环境变量后，同时写文件与 stdout（io.MultiWriter）；
//     文件以追加模式打开，权限 0o644。文件打开失败不阻塞启动，回退到 stdout-only
//     并在 logger 就绪后打一条 error 日志。
//
// 不内置文件轮转，依赖外部工具（logrotate、docker json-file driver 等）。
func Init(level string) {
	lvl, err := zerolog.ParseLevel(level)
	// ParseLevel("") 返回 (NoLevel, nil) — NoLevel(6) 高于 FatalLevel(4)，
	// 会把所有正常日志（包括 Fatal）过滤掉，导致进程静默退出。
	if err != nil || lvl == zerolog.NoLevel {
		lvl = zerolog.InfoLevel
	}

	var output io.Writer = os.Stdout
	var fileOpenErr error
	logFilePath := strings.TrimSpace(os.Getenv("LOG_FILE"))
	if logFilePath != "" {
		f, openErr := os.OpenFile(logFilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if openErr != nil {
			fileOpenErr = openErr
		} else {
			logFile = f
			output = io.MultiWriter(os.Stdout, f)
		}
	}

	Log = zerolog.New(output).
		Level(lvl).
		With().
		Timestamp().
		Logger()

	// 结构化日志保留 RFC3339（含时区），便于机器消费和跨时区排障
	zerolog.TimeFieldFormat = time.RFC3339

	if fileOpenErr != nil {
		Log.Error().
			Err(fileOpenErr).
			Str("log_file", logFilePath).
			Msg("LOG_FILE 打开失败，回退到 stdout-only 输出")
	}
}

// Module 返回带有 module 字段的子 logger
func Module(name string) *zerolog.Logger {
	l := Log.With().Str("module", name).Logger()
	return &l
}
