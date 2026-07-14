package sshutil

import (
	"testing"
	"time"
)

func TestCommandExecutionJoinTimeoutIsTenSeconds(t *testing.T) {
	if CommandExecutionJoinTimeout != 10*time.Second {
		t.Fatalf("command execution join timeout=%s, want 10s", CommandExecutionJoinTimeout)
	}
}
