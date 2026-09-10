package verifier

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"
)

func TestVerifyRsyncRestoreManifestMatchesCoreAndTargetBytes(t *testing.T) {
	core := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(core, "important.txt"), []byte("captured"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "important.txt"), []byte("captured"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(core, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(target, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	captureTask := model.Task{ExecutorType: "rsync", RsyncSource: core + "/", RsyncTarget: target}
	raw, err := executor.CaptureRsyncManifest(context.Background(), captureTask)
	if err != nil {
		t.Fatalf("capture restore evidence: %v", err)
	}
	task := captureTask
	task.RsyncCaptureLayout = model.TaskRunCaptureLayoutDirectoryContents
	task.RsyncCaptureManifest = raw
	result := verifyRsyncRestoreManifest(context.Background(), task, nil)
	if result.Status != "passed" {
		t.Fatalf("matching Core/target restore evidence status=%q message=%q", result.Status, result.Message)
	}
	if err := os.WriteFile(filepath.Join(target, "important.txt"), []byte("tampered target"), 0o600); err != nil {
		t.Fatal(err)
	}
	result = verifyRsyncRestoreManifest(context.Background(), task, nil)
	if result.Status != "warning" {
		t.Fatalf("tampered target status=%q, want warning", result.Status)
	}
}

func TestVerifyRsyncRestoreManifestRejectsCoreTamperAndMissingSelectedTarget(t *testing.T) {
	core := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(core, "selected.txt"), []byte("captured"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "selected.txt"), []byte("captured"), 0o600); err != nil {
		t.Fatal(err)
	}
	captureTask := model.Task{ExecutorType: "rsync", RsyncSource: core + "/", RsyncTarget: target}
	raw, err := executor.CaptureRsyncManifest(context.Background(), captureTask)
	if err != nil {
		t.Fatalf("capture restore evidence: %v", err)
	}
	task := captureTask
	task.RsyncCaptureLayout = model.TaskRunCaptureLayoutDirectoryContents
	task.RsyncCaptureManifest = raw
	if err := os.WriteFile(filepath.Join(core, "selected.txt"), []byte("core tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := verifyRsyncRestoreManifest(context.Background(), task, nil)
	if result.Status != "warning" {
		t.Fatalf("tampered Core status=%q, want warning", result.Status)
	}
	if err := os.WriteFile(filepath.Join(core, "selected.txt"), []byte("captured"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(target, "selected.txt")); err != nil {
		t.Fatal(err)
	}
	result = verifyRsyncRestoreManifest(context.Background(), task, nil)
	if result.Status != "warning" {
		t.Fatalf("missing selected target status=%q, want warning", result.Status)
	}
}
