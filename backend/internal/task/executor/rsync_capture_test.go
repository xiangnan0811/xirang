package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"xirang/backend/internal/model"
)

func TestRsyncCaptureManifestMatchesTransferAndDetectsMutation(t *testing.T) {
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	source := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "included.txt"), []byte("captured bytes"), 0o600); err != nil {
		t.Fatalf("write included file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "excluded.txt"), []byte("must not transfer"), 0o600); err != nil {
		t.Fatalf("write excluded file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(source, "empty"), 0o755); err != nil {
		t.Fatalf("create empty directory: %v", err)
	}
	if err := os.Symlink("included.txt", filepath.Join(source, "link.txt")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	task := model.Task{
		ExecutorType: "rsync",
		RsyncSource:  source + string(os.PathSeparator),
		RsyncTarget:  target,
		Policy:       &model.Policy{ExcludeRules: "excluded.txt"},
	}
	raw, err := CaptureRsyncManifest(context.Background(), task)
	if err != nil {
		t.Fatalf("capture manifest: %v", err)
	}
	manifest, err := model.DecodeRsyncCaptureManifest(raw)
	if err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.Layout != model.TaskRunCaptureLayoutDirectoryContents {
		t.Fatalf("layout = %q, want directory contents", manifest.Layout)
	}
	if len(manifest.Entries) != 4 {
		t.Fatalf("entry count = %d, want root, file, empty directory, symlink", len(manifest.Entries))
	}

	if exitCode, runErr := (&RsyncExecutor{binary: rsyncBinary}).Run(context.Background(), task, func(string, string) {}, nil); runErr != nil || exitCode != 0 {
		t.Fatalf("transfer failed: exit=%d err=%v", exitCode, runErr)
	}
	if err := VerifyRsyncCaptureManifestTarget(context.Background(), task, raw); err != nil {
		t.Fatalf("matching target rejected: %v", err)
	}
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := VerifyRsyncCaptureManifestTarget(canceledCtx, task, raw); err == nil {
		t.Fatal("canceled capture verification unexpectedly passed")
	}
	if err := os.WriteFile(filepath.Join(target, "included.txt"), []byte("mutated"), 0o600); err != nil {
		t.Fatalf("mutate target: %v", err)
	}
	if err := VerifyRsyncCaptureManifestTarget(context.Background(), task, raw); err == nil {
		t.Fatal("mutated target unexpectedly passed capture verification")
	}
}
