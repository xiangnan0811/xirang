package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
)

func TestAccountRecoveryCLIQuietOpenCapturesProcessOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "FAKE_PRIVATE_DATABASE_PATH_FOR_TEST_ONLY.db")
	if err := os.WriteFile(path, []byte("FAKE_DATABASE_CONTENT_FOR_TEST_ONLY"), 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestAccountRecoveryCLIQuietOpenProcessHelper$")
	cmd.Env = append(os.Environ(), "XIRANG_TEST_RECOVERY_QUIET_HELPER=1", "XIRANG_TEST_RECOVERY_DATABASE="+path)
	output, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("unexpected child outcome: %v", err)
	}
	if string(output) != "Database connection failed; no recovery was attempted.\n" {
		// Do not include child output here: the purpose is to detect leaks, not
		// to copy a hypothetical SQL/DSN leak into the parent test output.
		t.Fatal("quiet initialization emitted unexpected process output")
	}
}

func TestAccountRecoveryCLIQuietOpenProcessHelper(t *testing.T) {
	if os.Getenv("XIRANG_TEST_RECOVERY_QUIET_HELPER") != "1" {
		return
	}
	var stdout, stderr bytes.Buffer
	deps := dependencies{
		loadConfig: func() (config.Config, error) {
			return config.Config{DBType: "sqlite", SQLitePath: os.Getenv("XIRANG_TEST_RECOVERY_DATABASE")}, nil
		},
		open: database.OpenQuiet,
	}
	code := run(resetArgs(), nil, &stdout, &stderr, deps)
	_, _ = os.Stdout.Write(stdout.Bytes())
	_, _ = os.Stderr.Write(stderr.Bytes())
	os.Exit(code)
}

func TestAccountRecoveryCLISQLiteRemovalCannotCreateDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := admitDatabase(config.Config{DBType: "sqlite", SQLitePath: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	db, err := database.OpenQuiet(cfg)
	if err == nil {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		t.Fatal("opened removed SQLite database")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("SQLite open created a database after admission")
	}
}
