package task

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"xirang/backend/internal/task/executor"
)

func TestLegacyResticAuxiliaryCommandsInvokeBinaryFirst(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    []string
	}{
		{
			name:    "retention",
			command: buildLegacyResticRetentionCommand("restic-recorder", "/tmp/restic pw", "/repo path", "--keep-within 7d"),
			want:    []string{"--password-file", "/tmp/restic pw", "forget", "-r", "/repo path", "--keep-within", "7d", "--prune"},
		},
		{
			name:    "integrity",
			command: buildLegacyResticIntegrityCommand("restic-recorder", "/tmp/restic pw", "/repo path"),
			want:    []string{"--password-file", "/tmp/restic pw", "check", "-r", "/repo path", "--json"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder, command := newResticArgRecorder(t, test.command)
			if err := command.Run(); err != nil {
				t.Fatalf("run auxiliary command: %v", err)
			}
			gotRaw, err := os.ReadFile(recorder)
			if err != nil {
				t.Fatalf("read recorder: %v", err)
			}
			got := strings.Split(strings.TrimSuffix(string(gotRaw), "\n"), "\n")
			if !equalStringSlices(got, test.want) {
				t.Fatalf("argv=%q want=%q", got, test.want)
			}
		})
	}
}

func newResticArgRecorder(t *testing.T, command string) (string, *exec.Cmd) {
	t.Helper()
	dir := t.TempDir()
	recorder := filepath.Join(dir, "argv.txt")
	binary := filepath.Join(dir, "restic-recorder")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + executor.ShellEscape(recorder) + "\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	command = strings.Replace(command, "restic-recorder", executor.ShellEscape(binary), 1)
	return recorder, exec.Command("sh", "-c", command)
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
