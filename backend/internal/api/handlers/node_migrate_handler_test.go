package handlers

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/policy"
)

func TestRequireMigrationQuiescenceRejectsEnabledAndActiveTasks(t *testing.T) {
	db := openPolicyHandlerTestDB(t)
	enabled := model.Task{ID: 1, NodeID: 1, ExecutorType: "rsync", Enabled: true, CronSpec: "*/5 * * * *"}
	if err := requireMigrationQuiescence(db, []model.Task{enabled}); err == nil {
		t.Fatal("enabled/scheduled task was accepted for migration")
	}

	paused := model.Task{Name: "paused-migration", NodeID: 1, ExecutorType: "rsync", Status: "pending"}
	if err := db.Create(&paused).Error; err != nil {
		t.Fatalf("create paused task: %v", err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", paused.ID).Updates(map[string]any{"enabled": false, "cron_spec": ""}).Error; err != nil {
		t.Fatalf("pause task: %v", err)
	}
	if err := db.Create(&model.TaskRun{TaskID: paused.ID, Status: model.TaskRunStatusPending}).Error; err != nil {
		t.Fatalf("create active run: %v", err)
	}
	var loaded model.Task
	if err := db.First(&loaded, paused.ID).Error; err != nil {
		t.Fatalf("reload paused task: %v", err)
	}
	if err := requireMigrationQuiescence(db, []model.Task{loaded}); err == nil {
		t.Fatal("task with pending durable run was accepted for migration")
	}
	if err := db.Where("task_id = ?", paused.ID).Delete(&model.TaskRun{}).Error; err != nil {
		t.Fatalf("clear active run: %v", err)
	}
	if err := requireMigrationQuiescence(db, []model.Task{loaded}); err != nil {
		t.Fatalf("quiescent task rejected: %v", err)
	}
}

func migrationStageFixture(t *testing.T) (model.Task, model.Node, []policy.TargetOwner, string) {
	t.Helper()
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync is not installed")
	}
	base := t.TempDir()
	source := filepath.Join(t.TempDir(), "legacy-source")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "payload.txt"), []byte("migration-payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	policyID := uint(41)
	policyEntity := &model.Policy{ID: policyID, Name: "migration-policy", TargetPath: base}
	targetNode := model.Node{ID: 9, Name: "target"}
	task := model.Task{ID: 7, NodeID: 3, PolicyID: &policyID, Policy: policyEntity, ExecutorType: "rsync", RsyncTarget: source}
	owners := []policy.TargetOwner{{PolicyID: policyID, NodeID: task.NodeID, TaskID: task.ID, Target: source}}
	return task, targetNode, owners, source
}

func TestStageLocalBackupDataMigrationCopiesAndVerifiesIsolatedTarget(t *testing.T) {
	task, targetNode, owners, source := migrationStageFixture(t)
	items, targets, _, err := stageLocalBackupDataMigrationWithClaims(context.Background(), []model.Task{task}, targetNode, owners)
	if err != nil {
		t.Fatalf("stage migration: %v", err)
	}
	wantTarget := policy.PolicyNodeTargetPath(task.Policy.TargetPath, task.Policy.ID, targetNode.ID)
	if targets[task.ID] != wantTarget {
		t.Fatalf("migration target=%q, want %q", targets[task.ID], wantTarget)
	}
	if len(items) != 1 || items[0].Status != "copied" {
		t.Fatalf("migration result=%+v, want one copied item", items)
	}
	copied, err := os.ReadFile(filepath.Join(wantTarget, "nested", "payload.txt"))
	if err != nil {
		t.Fatalf("read isolated copy: %v", err)
	}
	if string(copied) != "migration-payload" {
		t.Fatalf("copied payload=%q", copied)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("historical source was removed: %v", err)
	}
}

func TestStageLocalBackupDataMigrationRejectsSharedLegacySource(t *testing.T) {
	task, targetNode, owners, source := migrationStageFixture(t)
	owners = append(owners, policy.TargetOwner{PolicyID: 99, NodeID: 8, TaskID: 99, Target: source})
	if _, _, _, err := stageLocalBackupDataMigrationWithClaims(context.Background(), []model.Task{task}, targetNode, owners); err == nil {
		t.Fatal("shared legacy source was accepted")
	}
	wantTarget := policy.PolicyNodeTargetPath(task.Policy.TargetPath, task.Policy.ID, targetNode.ID)
	if _, err := os.Stat(wantTarget); !os.IsNotExist(err) {
		t.Fatalf("unsafe source created destination %q: %v", wantTarget, err)
	}
}

func TestStageLocalBackupDataMigrationRejectsExistingDestination(t *testing.T) {
	task, targetNode, owners, _ := migrationStageFixture(t)
	wantTarget := policy.PolicyNodeTargetPath(task.Policy.TargetPath, task.Policy.ID, targetNode.ID)
	if err := os.MkdirAll(wantTarget, 0o750); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(wantTarget, "existing.txt")
	if err := os.WriteFile(marker, []byte("do-not-merge"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := stageLocalBackupDataMigrationWithClaims(context.Background(), []model.Task{task}, targetNode, owners); err == nil {
		t.Fatal("existing destination was merged")
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "do-not-merge" {
		t.Fatalf("existing destination changed: %q err=%v", got, err)
	}
}
func TestDirectoryTreesEqualRejectsSameSizeCorruption(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	if err := os.WriteFile(filepath.Join(left, "payload"), []byte("AAAA"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(right, "payload"), []byte("BBBB"), 0o600); err != nil {
		t.Fatal(err)
	}
	equal, err := directoryTreesEqual(left, right)
	if err != nil {
		t.Fatalf("compare equal-sized trees: %v", err)
	}
	if equal {
		t.Fatal("same-size files with different bytes were accepted as equal")
	}
}

func TestStageLocalBackupDataMigrationCleansEarlierDestinationOnLaterFailure(t *testing.T) {
	firstTask, targetNode, owners, firstSource := migrationStageFixture(t)
	secondPolicyID := uint(42)
	secondSource := filepath.Join(t.TempDir(), "missing-source")
	secondTask := model.Task{
		ID: 8, NodeID: 4, PolicyID: &secondPolicyID,
		Policy:       &model.Policy{ID: secondPolicyID, Name: "second-policy", TargetPath: firstTask.Policy.TargetPath},
		ExecutorType: "rsync", RsyncTarget: secondSource,
	}
	owners = append(owners, policy.TargetOwner{
		PolicyID: secondPolicyID, NodeID: secondTask.NodeID, TaskID: secondTask.ID, Target: secondSource,
	})
	if _, _, _, err := stageLocalBackupDataMigrationWithClaims(context.Background(), []model.Task{firstTask, secondTask}, targetNode, owners); err == nil {
		t.Fatal("later copy failure was accepted")
	}
	firstTarget := policy.PolicyNodeTargetPath(firstTask.Policy.TargetPath, firstTask.Policy.ID, targetNode.ID)
	if _, err := os.Stat(firstTarget); !os.IsNotExist(err) {
		t.Fatalf("earlier destination survived failed batch: %s err=%v", firstTarget, err)
	}
	if _, err := os.Stat(filepath.Join(firstSource, "nested", "payload.txt")); err != nil {
		t.Fatalf("original source was removed after failed batch: %v", err)
	}
}
func TestMigrationDestinationCleanupAfterCutoverRefusal(t *testing.T) {
	task, targetNode, owners, source := migrationStageFixture(t)
	_, targets, claims, err := stageLocalBackupDataMigrationWithClaims(context.Background(), []model.Task{task}, targetNode, owners)
	if err != nil {
		t.Fatalf("stage migration: %v", err)
	}
	cleanupOwnedMigrationDestinations(claims)
	if _, err := os.Stat(targets[task.ID]); !os.IsNotExist(err) {
		t.Fatalf("cutover refusal left owned destination: %s err=%v", targets[task.ID], err)
	}
	if _, err := os.Stat(filepath.Join(source, "nested", "payload.txt")); err != nil {
		t.Fatalf("cutover refusal removed original source: %v", err)
	}
}

func TestMigrationDestinationCleanupPreservesReplacedDirectory(t *testing.T) {
	task, targetNode, owners, _ := migrationStageFixture(t)
	_, targets, claims, err := stageLocalBackupDataMigrationWithClaims(context.Background(), []model.Task{task}, targetNode, owners)
	if err != nil {
		t.Fatalf("stage migration: %v", err)
	}
	target := targets[task.ID]
	if err := os.RemoveAll(target); err != nil {
		t.Fatalf("remove staged target: %v", err)
	}
	if err := os.MkdirAll(target, 0750); err != nil {
		t.Fatalf("create replacement target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "replacement.txt"), []byte("foreign"), 0600); err != nil {
		t.Fatalf("write replacement target: %v", err)
	}
	cleanupOwnedMigrationDestinations(claims)
	if _, err := os.Stat(filepath.Join(target, "replacement.txt")); err != nil {
		t.Fatalf("cleanup removed replacement target: %v", err)
	}
}
