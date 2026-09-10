package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"xirang/backend/internal/model"
)

func TestRsyncCaptureManifestDirectoryRootAndSingleFileLayouts(t *testing.T) {
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	t.Run("directory without trailing slash", func(t *testing.T) {
		source := t.TempDir()
		target := t.TempDir()
		if err := os.WriteFile(filepath.Join(source, "child.txt"), []byte("child"), 0o600); err != nil {
			t.Fatal(err)
		}
		task := model.Task{ExecutorType: "rsync", RsyncSource: source, RsyncTarget: target}
		raw, err := CaptureRsyncManifest(context.Background(), task)
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := model.DecodeRsyncCaptureManifest(raw)
		if err != nil || manifest.Layout != model.TaskRunCaptureLayoutDirectoryRoot || manifest.Root == "" {
			t.Fatalf("directory-root manifest=%+v err=%v", manifest, err)
		}
		if exitCode, runErr := (&RsyncExecutor{binary: rsyncBinary}).Run(context.Background(), task, func(string, string) {}, nil); runErr != nil || exitCode != 0 {
			t.Fatalf("directory-root transfer exit=%d err=%v", exitCode, runErr)
		}
		if err := VerifyRsyncCaptureManifestTarget(context.Background(), task, raw); err != nil {
			t.Fatalf("directory-root target verification: %v", err)
		}
	})
	t.Run("single file into absent trailing-slash directory", func(t *testing.T) {
		sourceDir := t.TempDir()
		target := filepath.Join(t.TempDir(), "new-target") + string(os.PathSeparator)
		source := filepath.Join(sourceDir, "payload.txt")
		if err := os.WriteFile(source, []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
		task := model.Task{ExecutorType: "rsync", RsyncSource: source, RsyncTarget: target}
		raw, err := CaptureRsyncManifest(context.Background(), task)
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := model.DecodeRsyncCaptureManifest(raw)
		if err != nil || manifest.Layout != model.TaskRunCaptureLayoutSingleFile || manifest.Root == "" {
			t.Fatalf("single-file manifest=%+v err=%v", manifest, err)
		}
		if exitCode, runErr := (&RsyncExecutor{binary: rsyncBinary}).Run(context.Background(), task, func(string, string) {}, nil); runErr != nil || exitCode != 0 {
			t.Fatalf("single-file transfer exit=%d err=%v", exitCode, runErr)
		}
		if err := VerifyRsyncCaptureManifestTarget(context.Background(), task, raw); err != nil {
			t.Fatalf("single-file target verification: %v", err)
		}
	})
}
