package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInit_StdoutOnlyWhenNoLogFile(t *testing.T) {
	t.Setenv("LOG_FILE", "")
	logFile = nil
	Init("info")
	if logFile != nil {
		t.Fatalf("expected logFile=nil when LOG_FILE empty, got %v", logFile)
	}
}

func TestInit_OpensLogFileAndDualWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "xirang.log")
	t.Setenv("LOG_FILE", path)
	logFile = nil

	Init("info")
	if logFile == nil {
		t.Fatal("expected logFile to be opened")
	}
	defer func() {
		if logFile != nil {
			_ = logFile.Close()
			logFile = nil
		}
	}()

	Log.Info().Str("k", "v").Msg("hello")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "hello") || !strings.Contains(got, `"k":"v"`) {
		t.Fatalf("log file missing expected content, got: %q", got)
	}
}

func TestInit_FallsBackWhenLogFileUnopenable(t *testing.T) {
	// 用一个不存在的目录路径让 OpenFile 失败
	bad := filepath.Join(t.TempDir(), "no", "such", "dir", "xirang.log")
	t.Setenv("LOG_FILE", bad)
	logFile = nil

	// 不应 panic；logFile 应保持 nil
	Init("info")
	if logFile != nil {
		t.Fatalf("expected logFile=nil on open failure, got %v", logFile)
	}
}
