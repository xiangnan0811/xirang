package main

import (
	"os"
	"testing"
)

func TestAccountRecoveryCLIPasswordRejectsRedirectedStdin(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close() }()
	if _, err := write.WriteString("FAKE_REDIRECTED_PASSWORD_FOR_TEST_ONLY\n"); err != nil {
		_ = write.Close()
		t.Fatal(err)
	}
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	confirmation, password, err := readTerminalSecrets(true, read)
	if err == nil || confirmation != "" || password != "" {
		t.Fatal("redirected stdin accepted or returned secret material")
	}
	if confirmation, password, err := readTerminalSecrets(true, nil); err == nil || confirmation != "" || password != "" {
		t.Fatal("missing stdin accepted or returned secret material")
	}
}

func TestAccountRecoveryCLIControllingTerminalPlatforms(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "freebsd", "netbsd", "openbsd", "dragonfly", "solaris", "illumos", "aix"} {
		if !supportsControllingTerminal(goos) {
			t.Fatalf("expected supported platform %q", goos)
		}
	}
	for _, goos := range []string{"windows", "plan9", "js", "wasip1", "unknown"} {
		if supportsControllingTerminal(goos) {
			t.Fatalf("unexpected controlling terminal support for %q", goos)
		}
	}
}
