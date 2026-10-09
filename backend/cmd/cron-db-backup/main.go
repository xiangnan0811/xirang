package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"xirang/backend/internal/cronbackup"
	"xirang/backend/internal/logger"
)

const (
	defaultScriptPath  = "/usr/local/bin/backup-db.sh"
	usageMessage       = "usage: xirang-cron-db-backup init | run [--script PATH] OUTPUT_DIR"
	alreadyRunningExit = 75
)

func main() {
	// Cron logs go only to its process stream. Do not expose LOG_FILE open
	// errors or create another long-lived application log descriptor.
	logger.Log = zerolog.New(os.Stdout).With().Timestamp().Logger()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := execute(ctx, os.Args[1:], os.Stderr)
	stop()
	os.Exit(code)
}

// execute runs one CLI invocation and returns its process exit status. It
// accepts an output writer so callers can verify that failures never expose
// filesystem paths, commands, or child-process error text.
func execute(ctx context.Context, args []string, stderr io.Writer) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if len(args) == 0 {
		writeFixed(stderr, usageMessage)
		return 2
	}

	switch args[0] {
	case "init":
		if len(args) != 1 {
			writeFixed(stderr, usageMessage)
			return 2
		}
		return executeInit(ctx, stderr)
	case "run":
		scriptPath, outputDir, ok := parseRunArgs(args[1:])
		if !ok {
			writeFixed(stderr, usageMessage)
			return 2
		}
		return executeRun(ctx, scriptPath, outputDir, stderr)
	default:
		writeFixed(stderr, usageMessage)
		return 2
	}
}

func executeInit(ctx context.Context, stderr io.Writer) int {
	cfg, err := loadConfig()
	if err != nil {
		return reportFailure(stderr, err)
	}
	if err := cronbackup.Initialize(ctx, cfg, time.Now().UTC()); err != nil {
		return reportFailure(stderr, err)
	}
	return 0
}

func executeRun(ctx context.Context, scriptPath, outputDir string, stderr io.Writer) int {
	cfg, err := loadConfig()
	if err != nil {
		return reportFailure(stderr, err)
	}
	if err := cronbackup.Run(ctx, cfg, scriptPath, outputDir); err != nil {
		return reportFailure(stderr, err)
	}
	return 0
}

func loadConfig() (cronbackup.Config, error) {
	engine := strings.TrimSpace(os.Getenv("DB_TYPE"))
	if engine == "" {
		engine = "sqlite"
	}
	return cronbackup.LoadConfig(engine)
}

func parseRunArgs(args []string) (scriptPath, outputDir string, ok bool) {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	scriptPath = defaultScriptPath
	flags.StringVar(&scriptPath, "script", defaultScriptPath, "backup script path")
	if err := flags.Parse(args); err != nil {
		return "", "", false
	}
	positionals := flags.Args()
	if len(positionals) != 1 {
		return "", "", false
	}
	return scriptPath, positionals[0], true
}

func reportFailure(stderr io.Writer, err error) int {
	writeFixed(stderr, failureCode(err))
	if errors.Is(err, cronbackup.ErrAlreadyRunning) {
		return alreadyRunningExit
	}
	return 1
}

func failureCode(err error) string {
	switch {
	case errors.Is(err, cronbackup.ErrNotConfigured):
		return "not_configured"
	case errors.Is(err, cronbackup.ErrInvalidConfiguration):
		return "invalid_configuration"
	case errors.Is(err, cronbackup.ErrNotInitialized):
		return "not_initialized"
	case errors.Is(err, cronbackup.ErrStateUnavailable):
		return "state_unavailable"
	case errors.Is(err, cronbackup.ErrStateInvalid):
		return "state_invalid"
	case errors.Is(err, cronbackup.ErrAlreadyRunning):
		return "already_running"
	case errors.Is(err, cronbackup.ErrStatePublishFailed):
		return "state_publish_failed"
	case errors.Is(err, cronbackup.ErrClockAnomaly):
		return "clock_anomaly"
	case errors.Is(err, cronbackup.ErrBackupStartFailed):
		return "backup_start_failed"
	case errors.Is(err, cronbackup.ErrBackupFailed):
		return "backup_failed"
	case errors.Is(err, cronbackup.ErrResultInvalid):
		return "result_invalid"
	case errors.Is(err, cronbackup.ErrProcessInterrupted), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "process_interrupted"
	default:
		// Initialization and execution touch only the private state volume after
		// configuration has been validated. Any uncategorized I/O failure is
		// therefore reported as the same safe state-access code rather than
		// leaking a path or syscall detail.
		return "state_unavailable"
	}
}

func writeFixed(stderr io.Writer, message string) {
	_, _ = fmt.Fprintln(stderr, message)
}
