//go:build linux

package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRsyncRecursiveDeleteHonorsContext(t *testing.T) {
	fixture := newRsyncDeletionFixture(t)
	pointDir := filepath.Join(fixture.root, "points", fixture.attempt.FinalComponent)
	for _, name := range []string{"alpha.bin", "bravo.bin", "charlie.bin", "delta.bin"} {
		if err := os.WriteFile(filepath.Join(pointDir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	unlinks := 0
	rsyncUnlinkTestHook = func(string) {
		unlinks++
		if unlinks == 2 {
			cancel()
		}
	}
	t.Cleanup(func() { rsyncUnlinkTestHook = nil })

	_, err := fixture.deleter.DeletePoint(ctx, fixture.request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled DeletePoint err=%v, want context.Canceled", err)
	}
	remaining := 0
	entries, err := os.ReadDir(pointDir)
	if err != nil {
		t.Fatalf("read remaining tree: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			remaining++
		}
	}
	if remaining == 0 {
		t.Fatal("recursive delete continued unlinking after context cancel")
	}
}
