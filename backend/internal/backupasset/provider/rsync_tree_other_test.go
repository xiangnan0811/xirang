//go:build !linux

package provider

import (
	"errors"
	"testing"
	"time"
)

func TestRsyncManagedTreeFailsClosedOutsideLinux(t *testing.T) {
	if _, err := openRsyncManagedTree(t.TempDir()); !errors.Is(err, errRsyncManagedTreeUnsupported) {
		t.Fatalf("non-Linux managed tree error=%v, want unsupported", err)
	}
}

func TestRsyncManagedTreeCommonContractsBuildOutsideLinux(t *testing.T) {
	limits := ManifestLimits{Timeout: time.Second, MaxBytes: 1, MaxEntries: 1, MaxRecordBytes: 1, MaxDepth: 0}
	if !validRsyncTreeManifestLimits(limits) {
		t.Fatal("valid managed Rsync manifest limits were rejected")
	}
	if err := rsyncManagedTreeSystemError(errors.New("close")); !errors.Is(err, errRsyncManagedTreeUnsafe) {
		t.Fatalf("non-Linux managed tree system error=%v, want unsafe", err)
	}
}
