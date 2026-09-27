package task

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"
	"xirang/backend/internal/model"
	taskexec "xirang/backend/internal/task/executor"
	"xirang/backend/internal/task/testutil"
)

func TestManagerRsyncStableCapture(t *testing.T) {
	for _, layout := range []string{"directory-content", "directory-self", "single-file"} {
		for _, mutation := range []string{"replace", "remove"} {
			t.Run(layout+"/"+mutation, func(t *testing.T) {
				runManagerRsyncStableCapture(t, openManagerTestDB(t), layout, mutation, false)
			})
		}
	}
}

func TestManagerRsyncStableCapturePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	if err := db.AutoMigrate(&model.RestoreDrillEvidence{}, &model.CredentialAuditEvent{}, &model.TaskLog{}, &model.Alert{}, &model.Integration{}, &model.TaskTrafficSample{}, &model.AlertDelivery{}); err != nil {
		t.Fatal(err)
	}
	runManagerRsyncStableCapture(t, db, "directory-content", "remove", false)
}

func TestManagerRsyncStableCaptureRemoteRestore(t *testing.T) {
	runManagerRsyncStableCapture(t, openManagerTestDB(t), "single-file", "remove", true)
}

func runManagerRsyncStableCapture(t *testing.T, db *gorm.DB, layout, mutation string, restore bool) {
	t.Helper()
	binary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	taskEntity := seedTaskForManagerTest(t, db)
	sourceDir := strings.TrimSuffix(taskEntity.RsyncSource, "/")
	sourceFile := filepath.Join(sourceDir, "payload")
	want := "captured content before source mutation\n"
	if err := os.WriteFile(sourceFile, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	targetFile := filepath.Join(taskEntity.RsyncTarget, "payload")
	switch layout {
	case "directory-self":
		taskEntity.RsyncSource = sourceDir
		targetFile = filepath.Join(taskEntity.RsyncTarget, filepath.Base(sourceDir), "payload")
	case "single-file":
		taskEntity.RsyncSource = sourceFile
		taskEntity.RsyncTarget = filepath.Join(taskEntity.RsyncTarget, "renamed-backup")
		targetFile = taskEntity.RsyncTarget
	}
	if restore {
		sshd, err := exec.LookPath("sshd")
		if err != nil {
			t.Skip("sshd is not installed")
		}
		t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
		t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")
		node := testutil.StartRsyncSSHServer(t, sshd)
		node.Name = "stable-capture-restore"
		node.BackupDir = t.TempDir()
		if err := db.Create(&node).Error; err != nil {
			t.Fatal(err)
		}
		taskEntity.NodeID = node.ID
	}
	if err := db.Save(&taskEntity).Error; err != nil {
		t.Fatal(err)
	}
	var original model.Task
	if err := db.Preload("Node").First(&original, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	fingerprint := model.TaskRunBackupConfigFingerprint(original)
	captureTemp := ""
	if !restore {
		captureTemp = t.TempDir()
		t.Setenv("TMPDIR", captureTemp)
	}
	manager := NewManager(db, taskexec.NewFactory(binary), nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	armed := false
	manager.afterLegacyRsyncGenerationArm = func() {
		armed = true
		if mutation == "remove" {
			if layout == "single-file" {
				err = os.Remove(sourceFile)
			} else {
				err = os.RemoveAll(sourceDir)
			}
		} else {
			err = os.WriteFile(sourceFile, []byte("changed after capture\n"), 0o644)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, runID, "manual", generateChainRunID())
	run := waitTaskRunTerminal(t, db, runID)
	if !armed || run.Status != model.TaskRunStatusSuccess || run.BackupGenerationState != model.TaskRunGenerationStateVerified || run.VerifyStatus != "passed" {
		t.Fatalf("armed=%v status=%q generation=%q verification=%q error=%q", armed, run.Status, run.BackupGenerationState, run.VerifyStatus, run.LastError)
	}
	assertStableCaptureFile(t, targetFile, want)
	if captureTemp != "" {
		assertStableCaptureCleaned(t, captureTemp)
	}
	var persisted model.Task
	if err := db.Preload("Node").First(&persisted, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.RsyncSource != original.RsyncSource || persisted.RsyncTarget != original.RsyncTarget || persisted.NodeID != original.NodeID || model.TaskRunBackupConfigFingerprint(persisted) != fingerprint || run.BackupConfigFingerprint != fingerprint || run.NodeIDSnapshot != original.NodeID {
		t.Fatal("capture staging changed the persisted task configuration or execution identity")
	}
	if _, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); err != nil {
		t.Fatalf("stable capture is not eligible for restore: %v", err)
	}
	if restore {
		manager.afterLegacyRsyncGenerationArm = nil
		restoreID, err := manager.TriggerRestore(taskEntity.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		restored := waitTaskRunTerminal(t, db, restoreID)
		manager.taskWG.Wait()
		if restored.Status != model.TaskRunStatusSuccess || restored.VerifyStatus != "passed" {
			t.Fatalf("restore status=%q verification=%q error=%q", restored.Status, restored.VerifyStatus, restored.LastError)
		}
		assertStableCaptureFile(t, sourceFile, want)
	}
}

func TestManagerRsyncStableCaptureCancellationCleansWithoutWriting(t *testing.T) {
	binary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	db := openManagerTestDB(t)
	taskEntity := seedTaskForManagerTest(t, db)
	source := filepath.Join(taskEntity.RsyncSource, "payload")
	if err := os.WriteFile(source, []byte("previous verified content"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(db, taskexec.NewFactory(binary), nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	firstID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, firstID, "manual", generateChainRunID())
	first := waitTaskRunTerminal(t, db, firstID)
	if first.Status != model.TaskRunStatusSuccess || first.BackupGenerationState != model.TaskRunGenerationStateVerified {
		t.Fatalf("initial backup failed: %+v", first)
	}
	if err := os.WriteFile(source, []byte("must not reach target"), 0o644); err != nil {
		t.Fatal(err)
	}
	captureTemp := t.TempDir()
	t.Setenv("TMPDIR", captureTemp)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	armed := false
	manager.afterLegacyRsyncGenerationArm = func() { armed = true; cancel() }
	secondID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTaskWithContext(taskEntity.ID, secondID, "manual", generateChainRunID(), ctx, nil, cancel)
	second := waitTaskRunTerminal(t, db, secondID)
	if !armed || second.Status != model.TaskRunStatusCanceled || second.BackupGenerationState != "" {
		t.Fatalf("cancellation changed write boundary: armed=%v status=%q generation=%q error=%q", armed, second.Status, second.BackupGenerationState, second.LastError)
	}
	assertStableCaptureFile(t, filepath.Join(taskEntity.RsyncTarget, "payload"), "previous verified content")
	assertStableCaptureCleaned(t, captureTemp)
	if _, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); err != nil {
		t.Fatalf("no-start cancellation displaced prior verified generation: %v", err)
	}
}

func assertStableCaptureFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s content=%q, want captured %q", path, got, want)
	}
}

func assertStableCaptureCleaned(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("capture temporary directory leaked %d entries: %v", len(entries), entries)
	}
}
