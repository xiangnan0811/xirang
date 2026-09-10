package task

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func TestCronOccurrenceDeduplicatesAcrossManagersAndRestart(t *testing.T) {
	db := openConcurrentManagerTestDB(t)
	if err := db.Exec(`CREATE UNIQUE INDEX idx_task_runs_cron_occurrence
		ON task_runs(task_id, cron_scheduled_at)
		WHERE trigger_type = 'cron' AND cron_scheduled_at IS NOT NULL`).Error; err != nil {
		t.Fatalf("install cron occurrence uniqueness: %v", err)
	}

	exec := &successExecutor{}
	managerOne := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	managerTwo := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, managerOne)
	shutdownManagerOnCleanup(t, managerTwo)

	taskEntity := seedTaskForManagerTest(t, db)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]any{
		"status":    string(StatusSuccess),
		"enabled":   true,
		"cron_spec": "@every 1h",
		"skip_next": true,
	}).Error; err != nil {
		t.Fatalf("prepare cron task: %v", err)
	}

	occurrence := time.Date(2026, 9, 10, 1, 2, 3, 0, time.UTC)
	firstRunID, err := managerOne.triggerCore(taskEntity.ID, "cron", generateChainRunID(), nil, &occurrence)
	if err != nil {
		t.Fatalf("trigger first cron occurrence: %v", err)
	}
	firstRun := waitTaskRunTerminal(t, db, firstRunID)
	if firstRun.Status != model.TaskRunStatusCanceled {
		t.Fatalf("first cron occurrence status=%q, want canceled skip", firstRun.Status)
	}
	if firstRun.CronScheduledAt == nil || !firstRun.CronScheduledAt.Equal(occurrence) {
		t.Fatalf("first cron occurrence timestamp=%v, want %v", firstRun.CronScheduledAt, occurrence)
	}
	if got := exec.Calls(); got != 0 {
		t.Fatalf("skipped first cron occurrence invoked executor %d time(s)", got)
	}

	// The first manager has finished and released its process-local ownership;
	// the second manager must still treat the same durable occurrence as handled.
	if err := managerTwo.TriggerFromScheduler(taskEntity.ID, occurrence); err != nil {
		t.Fatalf("duplicate skipped cron occurrence: %v", err)
	}
	var runCount int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ?", taskEntity.ID).Count(&runCount).Error; err != nil {
		t.Fatalf("count duplicate cron runs: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("duplicate skipped cron occurrence created %d runs, want 1", runCount)
	}

	laterOccurrence := occurrence.Add(time.Hour)
	laterRunID, err := managerTwo.triggerCore(taskEntity.ID, "cron", generateChainRunID(), nil, &laterOccurrence)
	if err != nil {
		t.Fatalf("trigger later cron occurrence: %v", err)
	}
	laterRun := waitTaskRunTerminal(t, db, laterRunID)
	if laterRun.Status != model.TaskRunStatusSuccess {
		t.Fatalf("later cron occurrence status=%q, want success", laterRun.Status)
	}
	if laterRun.CronScheduledAt == nil || !laterRun.CronScheduledAt.Equal(laterOccurrence) {
		t.Fatalf("later cron occurrence timestamp=%v, want %v", laterRun.CronScheduledAt, laterOccurrence)
	}
	if got := exec.Calls(); got != 1 {
		t.Fatalf("later cron occurrence executor calls=%d, want 1", got)
	}

	// A fresh manager has no process-local markers from managerTwo. Durable
	// occurrence identity must make replaying the completed occurrence a no-op.
	restarted := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, restarted)
	if err := restarted.TriggerFromScheduler(taskEntity.ID, laterOccurrence); err != nil {
		t.Fatalf("replayed cron occurrence after restart: %v", err)
	}
	if err := db.Model(&model.TaskRun{}).Where("task_id = ?", taskEntity.ID).Count(&runCount).Error; err != nil {
		t.Fatalf("count replayed cron runs: %v", err)
	}
	if runCount != 2 {
		t.Fatalf("replayed cron occurrence created %d runs, want 2 total", runCount)
	}
	if got := exec.Calls(); got != 1 {
		t.Fatalf("replayed cron occurrence executor calls=%d, want 1", got)
	}
}

func TestRestoreRequiresMatchingSuccessfulBackupFingerprint(t *testing.T) {
	db := openManagerTestDB(t)
	restoreExecutor := &trackingRestoreExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: restoreExecutor}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	manager.ensureRemoteTargetReadyFunc = func(context.Context, model.Node, string) error { return nil }

	taskEntity := seedTaskForManagerTest(t, db)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]any{
		"executor_type": "restic",
		"rsync_source":  "/source-a",
		"rsync_target":  "/target-a",
	}).Error; err != nil {
		t.Fatalf("prepare restic task with backup A: %v", err)
	}
	createSuccessfulBackupTaskRun(t, db, taskEntity.ID)

	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).
		Update("rsync_target", "/target-b").Error; err != nil {
		t.Fatalf("change task to backup B: %v", err)
	}
	if runID, err := manager.TriggerRestore(taskEntity.ID, "/restore/mismatch"); runID != 0 || !errors.Is(err, ErrRestoreRequiresNewBackup) {
		t.Fatalf("restore after config drift runID=%d error=%v, want ErrRestoreRequiresNewBackup", runID, err)
	}
	if got := atomic.LoadInt32(&restoreExecutor.calls); got != 0 {
		t.Fatalf("config-drift restore reached executor %d time(s)", got)
	}
	var restoreRuns int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND trigger_type = ?", taskEntity.ID, "restore").Count(&restoreRuns).Error; err != nil {
		t.Fatalf("count rejected restore runs: %v", err)
	}
	if restoreRuns != 0 {
		t.Fatalf("config-drift restore left %d TaskRun rows", restoreRuns)
	}

	// A successful ordinary backup made with B re-establishes restore authority.
	createSuccessfulBackupTaskRun(t, db, taskEntity.ID)
	runID, err := manager.TriggerRestore(taskEntity.ID, "/restore/matching")
	if err != nil {
		t.Fatalf("restore after matching backup: %v", err)
	}
	if runID == 0 {
		t.Fatal("restore after matching backup returned zero run ID")
	}
	run := waitTaskRunTerminal(t, db, runID)
	if run.Status != model.TaskRunStatusSuccess {
		t.Fatalf("matching restore status=%q, want success", run.Status)
	}
	if got := atomic.LoadInt32(&restoreExecutor.calls); got != 1 {
		t.Fatalf("matching restore executor calls=%d, want 1", got)
	}
}

func TestRestoreRejectsLegacyBlankBackupFingerprint(t *testing.T) {
	db := openManagerTestDB(t)
	restoreExecutor := &trackingRestoreExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: restoreExecutor}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	taskEntity := seedTaskForManagerTest(t, db)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("executor_type", "restic").Error; err != nil {
		t.Fatalf("prepare legacy restore task: %v", err)
	}
	legacyRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	if err := db.Model(&model.TaskRun{}).Where("id = ?", legacyRunID).Updates(map[string]any{
		"status":                    model.TaskRunStatusSuccess,
		"backup_config_fingerprint": "",
	}).Error; err != nil {
		t.Fatalf("persist legacy blank fingerprint: %v", err)
	}

	if runID, err := manager.TriggerRestore(taskEntity.ID, "/restore/legacy"); runID != 0 || !errors.Is(err, ErrRestoreRequiresNewBackup) {
		t.Fatalf("legacy blank fingerprint restore runID=%d error=%v, want ErrRestoreRequiresNewBackup", runID, err)
	}
	if got := atomic.LoadInt32(&restoreExecutor.calls); got != 0 {
		t.Fatalf("legacy blank fingerprint restore reached executor %d time(s)", got)
	}
}

func TestRestoreRequiresNewBackupAfterPolicyExcludeRulesChange(t *testing.T) {
	db := openManagerTestDB(t)
	restoreExecutor := &trackingRestoreExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: restoreExecutor}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	manager.ensureRemoteTargetReadyFunc = func(context.Context, model.Node, string) error { return nil }

	taskEntity := seedTaskForManagerTest(t, db)
	policy := model.Policy{
		Name:               "policy-fingerprint-exclude",
		SourcePath:         "/source-fingerprint",
		TargetPath:         "/target-fingerprint",
		CronSpec:           "@daily",
		ExcludeRules:       "*.tmp",
		VerifyEnabled:      false,
		PreHook:            "echo policy-hook",
		PostHook:           "echo policy-cleanup",
		HookTimeoutSeconds: 30,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("create fingerprint policy: %v", err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]any{
		"executor_type": "restic",
		"policy_id":     policy.ID,
	}).Error; err != nil {
		t.Fatalf("bind fingerprint policy: %v", err)
	}

	// A successful ordinary backup establishes the initial restore authority.
	createSuccessfulBackupTaskRun(t, db, taskEntity.ID)

	// Re-saving the same plaintext hooks produces fresh encrypted-at-rest
	// ciphertext. Restore must still recognize the same executed policy.
	var storedPolicy model.Policy
	if err := db.First(&storedPolicy, policy.ID).Error; err != nil {
		t.Fatalf("load fingerprint policy: %v", err)
	}
	if err := db.Save(&storedPolicy).Error; err != nil {
		t.Fatalf("re-encrypt fingerprint policy: %v", err)
	}
	stableRunID, err := manager.TriggerRestore(taskEntity.ID, "/restore/stable-policy")
	if err != nil {
		t.Fatalf("restore after equivalent encrypted policy storage: %v", err)
	}
	stableRun := waitTaskRunTerminal(t, db, stableRunID)
	// The next request tests provenance, not overlap with the prior runner
	// releasing its process-local admission after durable terminalization.
	manager.taskWG.Wait()
	if stableRun.Status != model.TaskRunStatusSuccess {
		t.Fatalf("equivalent encrypted policy restore status=%q, want success", stableRun.Status)
	}
	if got := atomic.LoadInt32(&restoreExecutor.calls); got != 1 {
		t.Fatalf("equivalent encrypted policy restore executor calls=%d, want 1", got)
	}

	// ExcludeRules is the only backup-material change. The old successful
	// backup is no longer a valid restore source and must not reach execution.
	if err := db.Model(&model.Policy{}).Where("id = ?", policy.ID).
		Update("exclude_rules", "*.tmp\ncache").Error; err != nil {
		t.Fatalf("change policy exclude rules: %v", err)
	}
	if runID, err := manager.TriggerRestore(taskEntity.ID, "/restore/mismatch"); runID != 0 || !errors.Is(err, ErrRestoreRequiresNewBackup) {
		t.Fatalf("restore after exclude-rule drift runID=%d error=%v, want ErrRestoreRequiresNewBackup", runID, err)
	}
	if got := atomic.LoadInt32(&restoreExecutor.calls); got != 1 {
		t.Fatalf("exclude-rule drift restore reached executor %d time(s)", got)
	}
	var restoreRuns int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND trigger_type = ?", taskEntity.ID, "restore").Count(&restoreRuns).Error; err != nil {
		t.Fatalf("count rejected exclude-rule restores: %v", err)
	}
	if restoreRuns != 1 {
		t.Fatalf("exclude-rule drift left %d new restore rows, want only stable restore", restoreRuns)
	}

	// A new successful ordinary backup under the changed policy re-establishes
	// restore authority.
	createSuccessfulBackupTaskRun(t, db, taskEntity.ID)
	matchingRunID, err := manager.TriggerRestore(taskEntity.ID, "/restore/matching-policy")
	if err != nil {
		t.Fatalf("restore after matching policy backup: %v", err)
	}
	matchingRun := waitTaskRunTerminal(t, db, matchingRunID)
	if matchingRun.Status != model.TaskRunStatusSuccess {
		t.Fatalf("matching policy restore status=%q, want success", matchingRun.Status)
	}
	if got := atomic.LoadInt32(&restoreExecutor.calls); got != 2 {
		t.Fatalf("matching policy restore executor calls=%d, want 2", got)
	}
}

func TestPolicyBoundTaskMissingSnapshotFailsClosed(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	taskEntity := seedTaskForManagerTest(t, db)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).
		Update("policy_id", uint(9999)).Error; err != nil {
		t.Fatalf("bind missing policy snapshot: %v", err)
	}

	runID, err := manager.TriggerManual(taskEntity.ID)
	if runID != 0 || err == nil {
		t.Fatalf("missing policy snapshot trigger runID=%d error=%v, want rejection", runID, err)
	}
	if got := exec.Calls(); got != 0 {
		t.Fatalf("missing policy snapshot reached executor %d time(s)", got)
	}
	var runCount int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ?", taskEntity.ID).Count(&runCount).Error; err != nil {
		t.Fatalf("count missing-policy TaskRuns: %v", err)
	}
	if runCount != 0 {
		t.Fatalf("missing policy snapshot left %d TaskRun rows, want 0", runCount)
	}
}
