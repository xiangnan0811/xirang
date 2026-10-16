// Command recover-account resets selected credentials of an existing account.
// It never migrates, seeds, creates accounts, or signs login tokens.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"xirang/backend/internal/bootstrap"
	"xirang/backend/internal/config"
	"xirang/backend/internal/database"

	"gorm.io/gorm"
)

const usage = `Usage:
  xirang-recover-account -username <existing-user> -reason <reason> -offline-confirmed [-reset-password] [-reset-totp]
  xirang-recover-account -status <operation-id>
  xirang-recover-account -h

Flags:
  -username          Existing account username (reset mode only).
  -reason            Operator reason, trimmed to 8-512 bytes; no control characters.
  -reset-password    Read a new password twice, without echo, from /dev/tty.
  -reset-totp        Remove the old factor and require browser re-enrollment.
  -offline-confirmed Confirm that all Core and worker instances are stopped.
  -status            Observe a reset audit by its 32-character lowercase hex ID.
  -h                 Show help without loading configuration or opening a database.

Select at least one reset. Stop every Core/worker and preserve the database and
matching encryption keys before proceeding. This tool does not detect shutdown.
Confirmation is read without echo from /dev/tty and must match the locally
provisioned XIRANG_BREAK_GLASS_CONFIRMATION. No confirmation/password argv,
password environment variable, password file, or password stdin is accepted.
Password reset requires terminal stdin and a supported controlling terminal.
Status is read-only and cannot be combined with any reset-mode flag.
`

type options struct {
	username      string
	reason        string
	status        string
	resetPassword bool
	resetTOTP     bool
	offline       bool
}

type targetIdentity struct {
	ID       uint
	Username string
	Role     string
}

// Dependencies keep command IO and the mutation boundary independently testable
// without a controlling terminal or a live production database.
type dependencies struct {
	loadConfig func() (config.Config, error)
	open       func(config.Config) (*gorm.DB, error)
	verify     func(*gorm.DB, string) error
	target     func(context.Context, *gorm.DB, string) (targetIdentity, error)
	operation  func() (string, error)
	secrets    func(bool, *os.File) (string, string, error)
	recover    func(context.Context, *gorm.DB, bootstrap.AccountRecoveryRequest) error
	status     func(context.Context, *gorm.DB, string) (bool, error)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, dependencies{
		loadConfig: config.Load,
		open:       database.OpenQuiet,
		verify:     database.VerifyCurrentSchema,
		target:     readTarget,
		operation:  bootstrap.GenerateAccountRecoveryOperationID,
		secrets:    readTerminalSecrets,
		recover:    bootstrap.RecoverAccount,
		status:     bootstrap.ReadAccountRecoveryStatus,
	}))
}

func parseOptions(args []string) (options, error) {
	var opts options
	fs := flag.NewFlagSet("xirang-recover-account", flag.ContinueOnError)
	// Flag errors can contain supplied values, including accidentally supplied
	// secrets. Only our fixed usage/error text may reach terminal output.
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.username, "username", "", "")
	fs.StringVar(&opts.reason, "reason", "", "")
	fs.StringVar(&opts.status, "status", "", "")
	fs.BoolVar(&opts.resetPassword, "reset-password", false, "")
	fs.BoolVar(&opts.resetTOTP, "reset-totp", false, "")
	fs.BoolVar(&opts.offline, "offline-confirmed", false, "")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() != 0 {
		return opts, errors.New("invalid arguments")
	}
	statusMode := false
	resetFlag := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "status" {
			statusMode = true
		} else {
			resetFlag = true
		}
	})
	if statusMode {
		if resetFlag || !validOperationID(opts.status) {
			return opts, errors.New("invalid status mode")
		}
		return opts, nil
	}
	opts.username = strings.TrimSpace(opts.username)
	opts.reason = strings.TrimSpace(opts.reason)
	if opts.username == "" || len(opts.reason) < 8 || len(opts.reason) > 512 ||
		strings.ContainsFunc(opts.reason, unicode.IsControl) ||
		!opts.offline || (!opts.resetPassword && !opts.resetTOTP) {
		return opts, errors.New("invalid reset mode")
	}
	return opts, nil
}

func validOperationID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// admitDatabase checks the filesystem before opening SQLite and then supplies
// mode=rw, so removal between admission and open cannot create a fresh database.
// Arbitrary URI/query DSNs are deliberately not interpreted by the offline tool.
func admitDatabase(cfg config.Config) (config.Config, error) {
	switch cfg.DBType {
	case "postgres":
		return cfg, nil
	case "sqlite":
		path := cfg.SQLitePath
		if path == "" || path == ":memory:" || strings.HasPrefix(path, "file:") ||
			strings.ContainsAny(path, "?#\x00") {
			return cfg, errors.New("unsafe SQLite path")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return cfg, errors.New("SQLite database must already be a regular non-symlink file")
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return cfg, errors.New("unsafe SQLite path")
		}
		uri := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}
		cfg.SQLitePath = uri.String() + "?mode=rw"
		return cfg, nil
	default:
		return cfg, errors.New("unsupported database")
	}
}

func readTarget(ctx context.Context, db *gorm.DB, username string) (targetIdentity, error) {
	var target targetIdentity
	// A raw projection avoids User's decryption hooks during display. Recovery
	// rechecks the identity and encrypted material inside its own write boundary.
	err := db.WithContext(ctx).Table("users").Select("id", "username", "role").
		Where("username = ?", username).Take(&target).Error
	if err != nil {
		return target, err
	}
	if target.ID == 0 || (target.Role != "admin" && target.Role != "operator" && target.Role != "viewer") {
		return target, errors.New("invalid target identity")
	}
	return target, nil
}

func run(args []string, stdin *os.File, stdout, stderr io.Writer, deps dependencies) int {
	opts, err := parseOptions(args)
	if errors.Is(err, flag.ErrHelp) {
		_, _ = io.WriteString(stdout, usage)
		return 0
	}
	if err != nil {
		_, _ = io.WriteString(stderr, "Invalid arguments. Use xirang-recover-account -h for usage.\n")
		return 2
	}
	cfg, err := deps.loadConfig()
	if err != nil {
		_, _ = io.WriteString(stderr, "Configuration rejected; check the existing deployment configuration.\n")
		return 1
	}
	cfg, err = admitDatabase(cfg)
	if err != nil {
		_, _ = io.WriteString(stderr, "Database admission rejected; SQLite requires an existing regular non-symlink file path without URI/query options.\n")
		return 1
	}
	db, err := deps.open(cfg)
	if err != nil {
		_, _ = io.WriteString(stderr, "Database connection failed; no recovery was attempted.\n")
		return 1
	}
	if db != nil {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			defer func() { _ = sqlDB.Close() }()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := deps.verify(db.WithContext(ctx), cfg.DBType); err != nil {
		_, _ = io.WriteString(stderr, "Database schema rejected; a clean complete current schema is required. No migrations were run.\n")
		return 1
	}
	if opts.status != "" {
		committed, err := deps.status(ctx, db, opts.status)
		if err != nil {
			_, _ = io.WriteString(stderr, "Recovery status could not be observed.\n")
			return 1
		}
		state := "not-found"
		if committed {
			state = "committed"
		}
		if _, err := fmt.Fprintf(stdout, "operation_id=%s status=%s\n", opts.status, state); err != nil {
			return 1
		}
		if !committed {
			_, _ = io.WriteString(stdout, "No commit is currently observed; this does not prove non-commit while the original process or transaction is still running.\n")
		}
		return 0
	}
	target, err := deps.target(ctx, db, opts.username)
	if err != nil {
		_, _ = io.WriteString(stderr, "Target account is unavailable or invalid; no recovery was attempted.\n")
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "Target: id=%d username=%q role=%q\nreset_password=%t reset_totp=%t\nAll Core/worker instances must already be stopped; shutdown is not detected by this tool.\n", target.ID, target.Username, target.Role, opts.resetPassword, opts.resetTOTP); err != nil {
		return 1
	}
	if opts.resetTOTP && !opts.resetPassword {
		if _, err := io.WriteString(stdout, "Factor-only reset: anyone who knows the current password can re-enroll. If password compromise is suspected, also reset the password.\n"); err != nil {
			return 1
		}
	}
	confirmation, password, err := deps.secrets(opts.resetPassword, stdin)
	if err != nil {
		_, _ = io.WriteString(stderr, "Terminal input rejected; use a supported controlling /dev/tty, terminal stdin for password reset, and identical new password entries. No recovery was attempted.\n")
		return 1
	}
	operationID, err := deps.operation()
	if err != nil || !validOperationID(operationID) {
		_, _ = io.WriteString(stderr, "Could not generate a recovery operation ID; no recovery was attempted.\n")
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "operation_id=%s\n", operationID); err != nil {
		_, _ = io.WriteString(stderr, "Could not display the operation ID; no recovery was attempted.\n")
		return 1
	}
	// Human input is not charged against the single bounded mutation attempt.
	mutationCtx, mutationCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer mutationCancel()
	err = deps.recover(mutationCtx, db, bootstrap.AccountRecoveryRequest{
		OperationID: operationID, Username: opts.username, Reason: opts.reason,
		Confirmation: confirmation, ResetPassword: opts.resetPassword,
		NewPassword: password, ResetTOTP: opts.resetTOTP,
	})
	if err != nil {
		return reportRecoveryError(stderr, operationID, err)
	}
	if _, err := fmt.Fprintf(stdout, "Recovery committed: operation_id=%s reset_password=%t reset_totp=%t\n", operationID, opts.resetPassword, opts.resetTOTP); err != nil {
		_, _ = fmt.Fprintf(stderr, "Recovery committed: operation_id=%s. Observe with: xirang-recover-account -status %s\n", operationID, operationID)
		return 1
	}
	return 0
}

func reportRecoveryError(out io.Writer, operationID string, err error) int {
	// Only explicitly guaranteed pre-write errors are classified as rejection.
	// No underlying error text is rendered, including unexpected wrapped errors.
	safe := []error{
		bootstrap.ErrAccountRecoveryRequest, bootstrap.ErrAccountRecoveryOperationID,
		bootstrap.ErrAccountRecoveryConfirmation, bootstrap.ErrAccountRecoveryReason,
		bootstrap.ErrAccountRecoveryPassword, bootstrap.ErrAccountRecoverySchema,
		bootstrap.ErrAccountRecoveryTarget, bootstrap.ErrAccountRecoveryKey,
		bootstrap.ErrAccountRecoveryDuplicateOperation,
	}
	for _, rejection := range safe {
		if errors.Is(err, rejection) {
			_, _ = io.WriteString(out, "Recovery rejected before credential writes; check mode, confirmation, password policy, target, key, schema, and operation ID.\n")
			return 1
		}
	}
	_, _ = fmt.Fprintf(out, "Recovery result UNKNOWN: operation_id=%s. Do not repeat the reset automatically.\nObserve with: xirang-recover-account -status %s\nA not-found observation does not prove non-commit while the original process or transaction is still running.\n", operationID, operationID)
	return 1
}
