package snapshot

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"xirang/backend/internal/task/executor"
)

func TestLegacyResticFindCommandInvokesBinaryFirst(t *testing.T) {
	dir := t.TempDir()
	recorder := filepath.Join(dir, "argv.txt")
	binary := filepath.Join(dir, "restic-recorder")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + executor.ShellEscape(recorder) + "\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	command := buildLegacyResticFindCommand(executor.ShellEscape(binary), "/tmp/restic pw", "snapshot-1234", "/repo path")
	if err := exec.Command("sh", "-c", command).Run(); err != nil {
		t.Fatalf("run auxiliary command: %v", err)
	}
	gotRaw, err := os.ReadFile(recorder)
	if err != nil {
		t.Fatalf("read recorder: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(gotRaw), "\n"), "\n")
	want := []string{"--password-file", "/tmp/restic pw", "find", "--json", "--long", "--path=/", "snapshot-1234", "-r", "/repo path"}
	if len(got) != len(want) {
		t.Fatalf("argv=%q want=%q", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("argv[%d]=%q want=%q (full=%q)", index, got[index], want[index], got)
		}
	}
}
