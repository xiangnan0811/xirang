package task

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/task/testutil"
)

func TestManagerLegacyRsyncSingleFileRestoreRoundTrip(t *testing.T) {
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	sshdBinary, err := exec.LookPath("sshd")
	if err != nil {
		t.Skip("sshd is not installed")
	}
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")

	for _, tc := range []struct {
		name         string
		removeBefore bool
	}{
		{name: "existing-target", removeBefore: false},
		{name: "missing-target", removeBefore: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := testutil.StartRsyncSSHServer(t, sshdBinary)
			db := openManagerTestDB(t)
			sourceDir := t.TempDir()
			source := filepath.Join(sourceDir, "payload.txt")
			contents := []byte("manager-level single-file restore payload\n")
			if err := os.WriteFile(source, contents, 0o644); err != nil {
				t.Fatalf("write remote source fixture: %v", err)
			}
			coreRoot := t.TempDir()
			coreBackup := filepath.Join(coreRoot, "payload-backup")
			node.Name = "manager-rsync-single-file"
			if err := db.Create(&node).Error; err != nil {
				t.Fatalf("create SSH test node: %v", err)
			}
			taskEntity := model.Task{
				Name:         "manager-rsync-single-file",
				NodeID:       node.ID,
				ExecutorType: "rsync",
				RsyncSource:  source,
				RsyncTarget:  coreBackup,
				Status:       string(StatusPending),
			}
			if err := db.Create(&taskEntity).Error; err != nil {
				t.Fatalf("create Rsync task: %v", err)
			}

			manager := NewManager(db, executor.NewFactory(rsyncBinary), nil, nil, nil, nil, 8, 90)
			shutdownManagerOnCleanup(t, manager)
			backupRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
			manager.runTask(taskEntity.ID, backupRunID, "manual", generateChainRunID())
			backupRun := waitTaskRunTerminal(t, db, backupRunID)
			if backupRun.Status != model.TaskRunStatusSuccess || backupRun.BackupGenerationState != model.TaskRunGenerationStateVerified {
				t.Fatalf("backup status=%q generation=%q error=%q", backupRun.Status, backupRun.BackupGenerationState, backupRun.LastError)
			}
			if backupRun.VerifyStatus != "passed" {
				t.Fatalf("backup verify_status=%q, want passed", backupRun.VerifyStatus)
			}
			if tc.removeBefore {
				if err := os.Remove(source); err != nil {
					t.Fatalf("remove source before missing-target restore: %v", err)
				}
			}

			restoreRunID, err := manager.TriggerRestore(taskEntity.ID, "")
			if err != nil {
				t.Fatalf("trigger default-path restore: %v", err)
			}
			restoreRun := waitTaskRunTerminal(t, db, restoreRunID)
			manager.taskWG.Wait()
			if restoreRun.Status != model.TaskRunStatusSuccess {
				t.Fatalf("restore status=%q error=%q", restoreRun.Status, restoreRun.LastError)
			}
			if restoreRun.VerifyStatus != "passed" {
				t.Fatalf("restore verify_status=%q, want passed", restoreRun.VerifyStatus)
			}
			info, err := os.Lstat(source)
			if err != nil {
				t.Fatalf("stat restored target: %v", err)
			}
			if !info.Mode().IsRegular() {
				t.Fatalf("restored target mode=%s, want regular file at exact requested path", info.Mode())
			}
			digest := sha256.Sum256(contents)
			data, err := os.ReadFile(source)
			if err != nil {
				t.Fatalf("read restored target: %v", err)
			}
			got := sha256.Sum256(data)
			if hex.EncodeToString(got[:]) != hex.EncodeToString(digest[:]) {
				t.Fatalf("restored target SHA=%s, want %s", hex.EncodeToString(got[:]), hex.EncodeToString(digest[:]))
			}
		})
	}
}
