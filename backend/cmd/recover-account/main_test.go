package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xirang/backend/internal/bootstrap"
	"xirang/backend/internal/config"
	"xirang/backend/internal/database"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var testOperationID = hex.EncodeToString([]byte("FAKE_X_TEST_ONLY"))

func resetArgs() []string {
	return []string{"-username", "existing-user", "-reason", "local recovery requested", "-offline-confirmed", "-reset-totp"}
}

func TestAccountRecoveryCLIHelpWithoutConfiguration(t *testing.T) {
	for _, help := range []string{"-h", "-help"} {
		var stdout, stderr bytes.Buffer
		code := run([]string{help}, nil, &stdout, &stderr, dependencies{})
		if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "-status") || !strings.Contains(stdout.String(), "-offline-confirmed") {
			t.Fatalf("help failed: exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	}
}

func TestAccountRecoveryCLIArgumentModes(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		valid bool
	}{
		{"empty", nil, false},
		{"factor", resetArgs(), true},
		{"password", []string{"-username", "existing-user", "-reason", "local recovery", "-offline-confirmed", "-reset-password"}, true},
		{"both", append(resetArgs(), "-reset-password"), true},
		{"no reset", []string{"-username", "existing-user", "-reason", "local recovery", "-offline-confirmed"}, false},
		{"no offline acknowledgement", []string{"-username", "existing-user", "-reason", "local recovery", "-reset-totp"}, false},
		{"short reason", []string{"-username", "existing-user", "-reason", "short", "-offline-confirmed", "-reset-totp"}, false},
		{"control reason", []string{"-username", "existing-user", "-reason", "local\nrecovery", "-offline-confirmed", "-reset-totp"}, false},
		{"long reason", []string{"-username", "existing-user", "-reason", strings.Repeat("x", 513), "-offline-confirmed", "-reset-totp"}, false},
		{"status", []string{"-status", testOperationID}, true},
		{"empty status", []string{"-status", ""}, false},
		{"uppercase status", []string{"-status", strings.ToUpper(testOperationID)}, false},
		{"short status", []string{"-status", "123"}, false},
		{"status with username", []string{"-status", testOperationID, "-username", ""}, false},
		{"status with false reset", []string{"-status", testOperationID, "-reset-totp=false"}, false},
		{"status with acknowledgement", []string{"-status", testOperationID, "-offline-confirmed"}, false},
		{"positional", append(resetArgs(), "unexpected"), false},
		{"password argv", append(resetArgs(), "-password", "FAKE_PASSWORD_FOR_TEST_ONLY"), false},
		{"confirmation argv", append(resetArgs(), "-confirmation", "FAKE_CONFIRMATION_FOR_TEST_ONLY"), false},
		{"password file", append(resetArgs(), "-password-file", "FAKE_FILE_FOR_TEST_ONLY"), false},
		{"password stdin", append(resetArgs(), "-password-stdin"), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseOptions(test.args)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%t err=%v", test.valid, err)
			}
			if !test.valid {
				var stdout, stderr bytes.Buffer
				if got := run(test.args, nil, &stdout, &stderr, dependencies{}); got != 2 {
					t.Fatalf("exit=%d", got)
				}
				if stdout.Len() != 0 || strings.Contains(stderr.String(), "FAKE_") {
					t.Fatal("argument error exposed supplied values")
				}
			}
		})
	}
}

func TestAccountRecoveryCLIReasonUsesBytesAndTrims(t *testing.T) {
	opts, err := parseOptions([]string{"-username", " existing-user ", "-reason", "  local recovery  ", "-offline-confirmed", "-reset-totp"})
	if err != nil || opts.reason != "local recovery" || opts.username != "existing-user" {
		t.Fatalf("unexpected normalized options: %#v err=%v", opts, err)
	}
	_, err = parseOptions([]string{"-username", "existing-user", "-reason", strings.Repeat("界", 171), "-offline-confirmed", "-reset-totp"})
	if err == nil {
		t.Fatal("reason limit counted characters instead of bytes")
	}
}

func TestAccountRecoveryCLISQLiteAdmission(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing database.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	admitted, err := admitDatabase(config.Config{DBType: "sqlite", SQLitePath: path})
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(admitted.SQLitePath)
	if err != nil || uri.Scheme != "file" || uri.Query().Get("mode") != "rw" || uri.Path != filepath.ToSlash(path) {
		t.Fatalf("unsafe admitted URI: %q err=%v", admitted.SQLitePath, err)
	}
	for _, rejected := range []string{"", ":memory:", "file:" + path, "file:" + path + "?mode=rwc", path + "?mode=rw", path + "#fragment", dir, filepath.Join(dir, "missing.db")} {
		if _, err := admitDatabase(config.Config{DBType: "sqlite", SQLitePath: rejected}); err == nil {
			t.Fatalf("accepted unsafe SQLite path %q", rejected)
		}
	}
	link := filepath.Join(dir, "linked.db")
	if err := os.Symlink(path, link); err == nil {
		if _, err := admitDatabase(config.Config{DBType: "sqlite", SQLitePath: link}); err == nil {
			t.Fatal("accepted symlink database")
		}
	}
	missing := filepath.Join(dir, "never-create.db")
	var stdout, stderr bytes.Buffer
	deps := dependencies{loadConfig: func() (config.Config, error) {
		return config.Config{DBType: "sqlite", SQLitePath: missing}, nil
	}}
	if got := run(resetArgs(), nil, &stdout, &stderr, deps); got != 1 {
		t.Fatalf("exit=%d", got)
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refusal created a database")
	}
}

func testDependencies(t *testing.T) dependencies {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return dependencies{
		loadConfig: func() (config.Config, error) { return config.Config{DBType: "postgres"}, nil },
		open:       func(config.Config) (*gorm.DB, error) { return db, nil },
		verify:     func(*gorm.DB, string) error { return nil },
		target: func(context.Context, *gorm.DB, string) (targetIdentity, error) {
			return targetIdentity{ID: 17, Username: "existing-user", Role: "operator"}, nil
		},
		operation: func() (string, error) { return testOperationID, nil },
		secrets: func(resetPassword bool, _ *os.File) (string, string, error) {
			if resetPassword {
				return "FAKE_CONFIRMATION_FOR_TEST_ONLY", " FAKE_PASSWORD_FOR_TEST_ONLY ", nil
			}
			return "FAKE_CONFIRMATION_FOR_TEST_ONLY", "", nil
		},
		recover: func(context.Context, *gorm.DB, bootstrap.AccountRecoveryRequest) error { return nil },
		status:  func(context.Context, *gorm.DB, string) (bool, error) { return false, nil },
	}
}

func TestAccountRecoveryCLIStatusIsExclusiveObservation(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			deps := testDependencies(t)
			deps.target = nil
			deps.operation = nil
			deps.secrets = nil
			deps.recover = nil
			deps.status = func(_ context.Context, _ *gorm.DB, id string) (bool, error) {
				if id != testOperationID {
					t.Fatal("wrong status ID")
				}
				return committed, nil
			}
			var stdout, stderr bytes.Buffer
			if got := run([]string{"-status", testOperationID}, nil, &stdout, &stderr, deps); got != 0 || stderr.Len() != 0 {
				t.Fatalf("exit=%d stderr=%q", got, stderr.String())
			}
			want := "status=not-found"
			if committed {
				want = "status=committed"
			}
			if !strings.Contains(stdout.String(), want) || (!committed && !strings.Contains(stdout.String(), "does not prove non-commit")) {
				t.Fatalf("bad observation: %q", stdout.String())
			}
		})
	}
}

func TestAccountRecoveryCLIPublishesIDBeforeSingleMutation(t *testing.T) {
	deps := testDependencies(t)
	var stdout, stderr bytes.Buffer
	calls := 0
	deps.recover = func(_ context.Context, _ *gorm.DB, request bootstrap.AccountRecoveryRequest) error {
		calls++
		if !strings.Contains(stdout.String(), "operation_id="+testOperationID) || !strings.Contains(stdout.String(), "id=17") || !strings.Contains(stdout.String(), "role=\"operator\"") {
			t.Fatal("mutation preceded operation ID or target display")
		}
		if request.OperationID != testOperationID || request.Username != "existing-user" || !request.ResetPassword || !request.ResetTOTP || request.NewPassword != " FAKE_PASSWORD_FOR_TEST_ONLY " || request.Confirmation != "FAKE_CONFIRMATION_FOR_TEST_ONLY" {
			t.Fatal("request changed the approved scope or raw secret input")
		}
		return errors.New("FAKE_STORAGE_SECRET_FOR_TEST_ONLY")
	}
	if got := run(append(resetArgs(), "-reset-password"), nil, &stdout, &stderr, deps); got != 1 || calls != 1 {
		t.Fatalf("exit=%d mutation calls=%d", got, calls)
	}
	if !strings.Contains(stderr.String(), "UNKNOWN") || !strings.Contains(stderr.String(), "-status "+testOperationID) || strings.Contains(stdout.String()+stderr.String(), "FAKE_PASSWORD") || strings.Contains(stderr.String(), "FAKE_STORAGE") {
		t.Fatal("unknown result was retried, misclassified, or exposed secrets")
	}
}

func TestAccountRecoveryCLIErrorsAreFixedAndRedacted(t *testing.T) {
	secret := "FAKE_CONFIG_DSN_SQL_SECRET_FOR_TEST_ONLY"
	for _, stage := range []string{"config", "open", "schema", "target", "terminal", "operation", "status"} {
		t.Run(stage, func(t *testing.T) {
			deps := testDependencies(t)
			args := resetArgs()
			sensitiveErr := errors.New(secret)
			switch stage {
			case "config":
				deps.loadConfig = func() (config.Config, error) { return config.Config{}, sensitiveErr }
			case "open":
				deps.open = func(config.Config) (*gorm.DB, error) { return nil, sensitiveErr }
			case "schema":
				deps.verify = func(*gorm.DB, string) error { return sensitiveErr }
			case "target":
				deps.target = func(context.Context, *gorm.DB, string) (targetIdentity, error) { return targetIdentity{}, sensitiveErr }
			case "terminal":
				deps.secrets = func(bool, *os.File) (string, string, error) { return "", "", sensitiveErr }
			case "operation":
				deps.operation = func() (string, error) { return secret, sensitiveErr }
			case "status":
				args = []string{"-status", testOperationID}
				deps.status = func(context.Context, *gorm.DB, string) (bool, error) { return false, sensitiveErr }
			}
			deps.recover = func(context.Context, *gorm.DB, bootstrap.AccountRecoveryRequest) error {
				t.Fatal("recovery reached after preflight failure")
				return nil
			}
			var stdout, stderr bytes.Buffer
			if got := run(args, nil, &stdout, &stderr, deps); got != 1 || stderr.Len() == 0 || strings.Contains(stdout.String()+stderr.String(), secret) {
				t.Fatalf("unsafe error: exit=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
			}
		})
	}
}

func TestAccountRecoveryCLISafeRejectionsAndUnknown(t *testing.T) {
	for _, err := range []error{bootstrap.ErrAccountRecoveryConfirmation, bootstrap.ErrAccountRecoveryPassword, bootstrap.ErrAccountRecoveryKey, bootstrap.ErrAccountRecoveryDuplicateOperation} {
		var out bytes.Buffer
		reportRecoveryError(&out, testOperationID, fmt.Errorf("FAKE_SECRET_FOR_TEST_ONLY: %w", err))
		if strings.Contains(out.String(), "UNKNOWN") || strings.Contains(out.String(), "FAKE_SECRET") || !strings.Contains(out.String(), "before credential writes") {
			t.Fatal("safe rejection was not safely reported")
		}
	}
	var out bytes.Buffer
	reportRecoveryError(&out, testOperationID, errors.New("FAKE_SQL_PASSWORD_FOR_TEST_ONLY"))
	if !strings.Contains(out.String(), "UNKNOWN") || !strings.Contains(out.String(), testOperationID) || strings.Contains(out.String(), "FAKE_SQL") {
		t.Fatal("unknown transaction outcome was not safely reported")
	}
}

func TestAccountRecoveryCLIQuietOpenFailure(t *testing.T) {
	// Exercise the actual opener: corrupt SQLite must not emit initialization
	// diagnostics outside the command's closed error messages.
	path := filepath.Join(t.TempDir(), "invalid.db")
	if err := os.WriteFile(path, []byte("FAKE_DATABASE_CONTENT_FOR_TEST_ONLY"), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	deps := dependencies{
		loadConfig: func() (config.Config, error) { return config.Config{DBType: "sqlite", SQLitePath: path}, nil },
		open:       database.OpenQuiet,
	}
	if got := run(resetArgs(), nil, &stdout, &stderr, deps); got != 1 || stderr.String() != "Database connection failed; no recovery was attempted.\n" {
		t.Fatalf("unsafe connection failure: exit=%d stderr=%q", got, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatal("unexpected connection output")
	}
}

func TestAccountRecoveryCLINoMutationWhenIDOutputFails(t *testing.T) {
	deps := testDependencies(t)
	deps.recover = func(context.Context, *gorm.DB, bootstrap.AccountRecoveryRequest) error {
		t.Fatal("mutated without publishing operation ID")
		return nil
	}
	var stderr bytes.Buffer
	if got := run(resetArgs(), nil, operationIDFailingWriter{}, &stderr, deps); got != 1 {
		t.Fatalf("exit=%d", got)
	}
}

type operationIDFailingWriter struct{}

func (operationIDFailingWriter) Write(value []byte) (int, error) {
	if bytes.HasPrefix(value, []byte("operation_id=")) {
		return 0, io.ErrClosedPipe
	}
	return len(value), nil
}

func TestAccountRecoveryCLIResetModesPreserveSelectedScope(t *testing.T) {
	for _, mode := range []struct {
		password bool
		totp     bool
	}{{false, true}, {true, false}, {true, true}} {
		t.Run(fmt.Sprintf("password=%t/totp=%t", mode.password, mode.totp), func(t *testing.T) {
			deps := testDependencies(t)
			args := []string{"-username", "existing-user", "-reason", "local recovery requested", "-offline-confirmed"}
			if mode.password {
				args = append(args, "-reset-password")
			}
			if mode.totp {
				args = append(args, "-reset-totp")
			}
			calls := 0
			deps.recover = func(_ context.Context, _ *gorm.DB, request bootstrap.AccountRecoveryRequest) error {
				calls++
				if request.ResetPassword != mode.password || request.ResetTOTP != mode.totp ||
					(!mode.password && request.NewPassword != "") {
					t.Fatal("reset scope or factor-only password changed")
				}
				return nil
			}
			var stdout, stderr bytes.Buffer
			if got := run(args, nil, &stdout, &stderr, deps); got != 0 || calls != 1 || stderr.Len() != 0 {
				t.Fatalf("exit=%d calls=%d stderr=%q", got, calls, stderr.String())
			}
			if !strings.Contains(stdout.String(), "Recovery committed: operation_id="+testOperationID) ||
				strings.Contains(stdout.String(), "FAKE_CONFIRMATION") || strings.Contains(stdout.String(), "FAKE_PASSWORD") {
				t.Fatal("missing completion or exposed secrets")
			}
			if strings.Contains(stdout.String(), "anyone who knows the current password") != (mode.totp && !mode.password) {
				t.Fatal("factor-only security warning does not match selected scope")
			}
		})
	}
}

func TestAccountRecoveryCLITargetDisplayUsesRawProjection(t *testing.T) {
	deps := testDependencies(t)
	db, err := deps.open(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, role TEXT, totp_secret TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO users (id, username, role, totp_secret) VALUES (?, ?, ?, ?)", 17, "existing-user", "operator", "FAKE_UNDECRYPTABLE_CIPHERTEXT_FOR_TEST_ONLY").Error; err != nil {
		t.Fatal(err)
	}
	target, err := readTarget(context.Background(), db, "existing-user")
	if err != nil || target.ID != 17 || target.Username != "existing-user" || target.Role != "operator" {
		t.Fatalf("raw identity projection failed: target=%#v err=%v", target, err)
	}
	if _, err := readTarget(context.Background(), db, "missing-user"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal("unknown target was not rejected")
	}
	if err := db.Exec("UPDATE users SET role = ?", "invalid-role").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := readTarget(context.Background(), db, "existing-user"); err == nil {
		t.Fatal("invalid role accepted")
	}
}
