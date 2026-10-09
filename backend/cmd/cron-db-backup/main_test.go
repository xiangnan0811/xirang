package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"xirang/backend/internal/cronbackup"
)

func TestParseRunArgsAcceptsScriptBeforeOutputDirectory(t *testing.T) {
	script, output, ok := parseRunArgs([]string{"--script", "/tmp/backup-helper.sh", "/backup/db"})
	if !ok {
		t.Fatal("parseRunArgs rejected the supported --script-before-output form")
	}
	if script != "/tmp/backup-helper.sh" || output != "/backup/db" {
		t.Fatalf("parseRunArgs returned script=%q output=%q", script, output)
	}
}

func TestParseRunArgsUsesProductionScriptDefault(t *testing.T) {
	script, output, ok := parseRunArgs([]string{"/backup/db"})
	if !ok {
		t.Fatal("parseRunArgs rejected the default-script form")
	}
	if script != defaultScriptPath || output != "/backup/db" {
		t.Fatalf("parseRunArgs returned script=%q output=%q", script, output)
	}
}

func TestReportFailureUsesSafeCodeAndAlreadyRunningExit(t *testing.T) {
	var stderr bytes.Buffer
	detail := fmt.Errorf("open /secret/private-state/run.lock: %w", cronbackup.ErrAlreadyRunning)
	if got := reportFailure(&stderr, detail); got != alreadyRunningExit {
		t.Fatalf("reportFailure exit = %d, want %d", got, alreadyRunningExit)
	}
	if got := strings.TrimSpace(stderr.String()); got != "already_running" {
		t.Fatalf("reportFailure output = %q, want fixed already_running code", got)
	}
}

func TestFailureCodeDoesNotExposeUncategorizedError(t *testing.T) {
	if got := failureCode(errors.New("open /secret/private-state/state.json: permission denied")); got != "state_unavailable" {
		t.Fatalf("failureCode = %q, want state_unavailable", got)
	}
	if got := failureCode(context.Canceled); got != "process_interrupted" {
		t.Fatalf("failureCode(context.Canceled) = %q, want process_interrupted", got)
	}
}
