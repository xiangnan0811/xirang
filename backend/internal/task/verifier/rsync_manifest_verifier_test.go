package verifier

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/rsyncconfinement"
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
	raw, err := executor.CaptureRsyncManifest(context.Background(), captureTask, executor.RsyncCaptureSourceRole)
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
	raw, err := executor.CaptureRsyncManifest(context.Background(), captureTask, executor.RsyncCaptureSourceRole)
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
func TestVerifyRsyncCaptureManifestSourceUsesTargetRootAndPinnedParents(t *testing.T) {
	allowed := t.TempDir()
	source := filepath.Join(allowed, "backup")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("captured")
	if err := os.WriteFile(filepath.Join(source, "payload.txt"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	raw, err := model.EncodeRsyncCaptureManifest(model.RsyncCaptureManifest{
		Layout: model.TaskRunCaptureLayoutDirectoryContents,
		Entries: []model.RsyncCaptureManifestEntry{
			{Path: "", Kind: "directory"},
			{Path: "payload.txt", Kind: "file", Size: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(rsyncconfinement.AllowedTargetRootsEnv, allowed)
	task := model.Task{
		ExecutorType:         "rsync",
		RsyncSource:          source,
		RsyncCaptureLayout:   model.TaskRunCaptureLayoutDirectoryContents,
		RsyncCaptureManifest: raw,
	}
	if err := executor.VerifyRsyncCaptureManifestSource(context.Background(), task, raw); err != nil {
		t.Fatalf("allowed Core source rejected: %v", err)
	}
	rootPayload := []byte("capture at configured root")
	if err := os.WriteFile(filepath.Join(allowed, "root-payload.txt"), rootPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	rootDigest := sha256.Sum256(rootPayload)
	rootRaw, err := model.EncodeRsyncCaptureManifest(model.RsyncCaptureManifest{
		Layout: model.TaskRunCaptureLayoutDirectoryContents,
		Entries: []model.RsyncCaptureManifestEntry{
			{Path: "", Kind: "directory"},
			{Path: "root-payload.txt", Kind: "file", Size: int64(len(rootPayload)), SHA256: hex.EncodeToString(rootDigest[:])},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rootTask := task
	rootTask.RsyncSource = allowed
	rootTask.RsyncCaptureManifest = rootRaw
	if err := executor.VerifyRsyncCaptureManifestSource(context.Background(), rootTask, rootRaw); err != nil {
		t.Fatalf("source equal to configured target root rejected: %v", err)
	}

	outside := t.TempDir()
	outsideSource := filepath.Join(outside, "outside")
	if err := os.MkdirAll(outsideSource, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideSource, "payload.txt"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	task.RsyncSource = outsideSource
	if err := executor.VerifyRsyncCaptureManifestSource(context.Background(), task, raw); err == nil {
		t.Fatal("Core source outside target roots unexpectedly accepted")
	}

	escapedParent := filepath.Join(allowed, "escaped")
	if err := os.Symlink(outsideSource, escapedParent); err != nil {
		t.Fatal(err)
	}
	singleRaw, err := model.EncodeRsyncCaptureManifest(model.RsyncCaptureManifest{
		Layout: model.TaskRunCaptureLayoutSingleFile,
		Entries: []model.RsyncCaptureManifestEntry{
			{Kind: "file", Size: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	singleTask := model.Task{
		ExecutorType:       "rsync",
		RsyncSource:        filepath.Join(escapedParent, "payload.txt"),
		RsyncCaptureLayout: model.TaskRunCaptureLayoutSingleFile,
	}
	if err := executor.VerifyRsyncCaptureManifestSource(context.Background(), singleTask, singleRaw); err == nil {
		t.Fatal("Core source through escaping parent symlink unexpectedly accepted")
	}
}
