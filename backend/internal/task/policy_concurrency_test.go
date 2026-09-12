package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func seedQuotaPolicyTask(t *testing.T, db *gorm.DB, policyID uint) model.Task {
	t.Helper()
	source := t.TempDir()
	target := t.TempDir()
	node := model.Node{
		Name:      fmt.Sprintf("node-quota-%s", filepath.Base(source)),
		Host:      "",
		Port:      22,
		Username:  "root",
		AuthType:  "key",
		BackupDir: filepath.Join(target, "backup"),
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create quota node: %v", err)
	}
	taskEntity := model.Task{
		Name:         fmt.Sprintf("task-quota-%s", filepath.Base(source)),
		NodeID:       node.ID,
		PolicyID:     &policyID,
		ExecutorType: "rsync",
		Status:       string(StatusPending),
		RsyncSource:  source + "/",
		RsyncTarget:  target,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("create quota task: %v", err)
	}
	if err := db.Preload("Node").Preload("Policy").First(&taskEntity, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload quota task: %v", err)
	}
	return taskEntity
}

func seedQuotaPolicy(t *testing.T, db *gorm.DB, maxConcurrent int) model.Policy {
	t.Helper()
	policy := model.Policy{Name: "quota-policy", MaxConcurrent: maxConcurrent}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("create quota policy: %v", err)
	}
	if err := db.Model(&model.Policy{}).Where("id = ?", policy.ID).
		Updates(map[string]interface{}{"max_concurrent": maxConcurrent}).Error; err != nil {
		t.Fatalf("persist quota policy limit: %v", err)
	}
	return policy
}

func seedTargetClaimTask(t *testing.T, db *gorm.DB, nodeID uint, policyID *uint, name, target string) model.Task {
	t.Helper()
	source := t.TempDir()
	taskEntity := model.Task{
		Name:         name,
		NodeID:       nodeID,
		PolicyID:     policyID,
		ExecutorType: "rsync",
		Status:       string(StatusPending),
		RsyncSource:  source + "/",
		RsyncTarget:  target,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("create target claim task: %v", err)
	}
	if err := db.Preload("Node").Preload("Policy").First(&taskEntity, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload target claim task: %v", err)
	}
	return taskEntity
}

func runManualTargetOwnershipAdmission(t *testing.T, policyFirst bool) {
	t.Helper()
	db := openConcurrentManagerTestDB(t)
	node := model.Node{
		Name:     "target-ownership-node",
		Host:     "",
		Port:     22,
		Username: "root",
		AuthType: "key",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create target ownership node: %v", err)
	}
	var policyID *uint
	if policyFirst {
		policy := seedQuotaPolicy(t, db, 0)
		policyID = &policy.ID
	}
	target := filepath.Join(t.TempDir(), "shared-target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("create target ownership destination: %v", err)
	}
	first := seedTargetClaimTask(t, db, node.ID, policyID, "target-owner-first", target)
	executor := &successExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: executor}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	runID, err := manager.TriggerManual(first.ID)
	if err != nil || runID == 0 {
		t.Fatalf("first target ownership trigger run=%d err=%v", runID, err)
	}
	firstRun := waitTaskRunTerminal(t, db, runID)
	if firstRun.Status != model.TaskRunStatusSuccess {
		t.Fatalf("first target ownership run status=%q error=%q", firstRun.Status, firstRun.LastError)
	}
	second := seedTargetClaimTask(t, db, node.ID, nil, "target-owner-second", target)
	if duplicateRunID, duplicateErr := manager.TriggerManual(second.ID); duplicateErr == nil || duplicateRunID != 0 ||
		!strings.Contains(duplicateErr.Error(), "任务目标路径冲突") {
		t.Fatalf("duplicate target ownership trigger run=%d err=%v, want conflict and no run", duplicateRunID, duplicateErr)
	}
	if got := executor.Calls(); got != 1 {
		t.Fatalf("target ownership executor calls=%d, want 1", got)
	}
}

func TestManualTargetOwnershipAdmissionRejectsDuplicateRuntimeClaims(t *testing.T) {
	t.Run("manual_vs_manual", func(t *testing.T) {
		runManualTargetOwnershipAdmission(t, false)
	})
	t.Run("manual_vs_policy", func(t *testing.T) {
		runManualTargetOwnershipAdmission(t, true)
	})
}

func TestPolicyMaxConcurrentReservationsCountPending(t *testing.T) {
	db := openConcurrentManagerTestDB(t)
	policy := seedQuotaPolicy(t, db, 1)
	firstTask := seedQuotaPolicyTask(t, db, policy.ID)
	secondTask := seedQuotaPolicyTask(t, db, policy.ID)
	manager := &Manager{db: db, stateMachine: NewStateMachine(), executionOwnerID: "quota-reservation-test"}

	first, err := manager.reserveTaskRun(context.Background(), firstTask.NodeID, model.TaskRun{
		TaskID: firstTask.ID, TriggerType: "manual", Status: model.TaskRunStatusPending,
	})
	if err != nil {
		t.Fatalf("first reservation: %v", err)
	}
	if first.Status != model.TaskRunStatusPending {
		t.Fatalf("first reservation status=%q, want pending", first.Status)
	}
	if _, err := manager.reserveTaskRun(context.Background(), secondTask.NodeID, model.TaskRun{
		TaskID: secondTask.ID, TriggerType: "manual", Status: model.TaskRunStatusPending,
	}); !errors.Is(err, ErrPolicyConcurrencyLimit) {
		t.Fatalf("second reservation error=%v, want ErrPolicyConcurrencyLimit", err)
	}
	var activeCount int64
	if err := db.Model(&model.TaskRun{}).Where("status IN ?", []string{model.TaskRunStatusPending, model.TaskRunStatusRunning}).Count(&activeCount).Error; err != nil {
		t.Fatalf("count pending quota reservations: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("active reservation count=%d, want 1", activeCount)
	}
	if err := db.Model(&model.TaskRun{}).Where("id = ?", first.ID).Updates(map[string]interface{}{
		"status": model.TaskRunStatusCanceled, "finished_at": time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("release first reservation: %v", err)
	}
	if _, err := manager.reserveTaskRun(context.Background(), secondTask.NodeID, model.TaskRun{
		TaskID: secondTask.ID, TriggerType: "manual", Status: model.TaskRunStatusPending,
	}); err != nil {
		t.Fatalf("reservation after slot release: %v", err)
	}
}

func TestPolicyMaxConcurrentAdmissionRechecksRunningAcrossTasks(t *testing.T) {
	db := openConcurrentManagerTestDB(t)
	policy := seedQuotaPolicy(t, db, 1)
	firstTask := seedQuotaPolicyTask(t, db, policy.ID)
	secondTask := seedQuotaPolicyTask(t, db, policy.ID)
	firstRunID := createTestTaskRun(t, db, firstTask.ID, "manual")
	secondRunID := createTestTaskRun(t, db, secondTask.ID, "manual")
	manager := &Manager{db: db, stateMachine: NewStateMachine(), executionOwnerID: "quota-admission-test"}

	var loadedFirst, loadedSecond model.Task
	if err := db.First(&loadedFirst, firstTask.ID).Error; err != nil {
		t.Fatalf("load first quota task: %v", err)
	}
	if err := db.First(&loadedSecond, secondTask.ID).Error; err != nil {
		t.Fatalf("load second quota task: %v", err)
	}
	startedAt := time.Now().UTC()
	if err := manager.enterTaskExecutionForReason(context.Background(), firstRunID, loadedFirst.NodeID, startedAt, &loadedFirst, "manual"); err != nil {
		t.Fatalf("first execution admission: %v", err)
	}
	var firstRunning model.TaskRun
	if err := db.First(&firstRunning, firstRunID).Error; err != nil {
		t.Fatalf("load first running quota task: %v", err)
	}
	if firstRunning.Status != model.TaskRunStatusRunning {
		t.Fatalf("first run status=%q, want running", firstRunning.Status)
	}
	if err := manager.enterTaskExecutionForReason(context.Background(), secondRunID, loadedSecond.NodeID, startedAt, &loadedSecond, "manual"); !errors.Is(err, ErrPolicyConcurrencyLimit) {
		t.Fatalf("second execution admission error=%v, want ErrPolicyConcurrencyLimit", err)
	}
	var secondPending model.TaskRun
	if err := db.First(&secondPending, secondRunID).Error; err != nil {
		t.Fatalf("load second pending quota task: %v", err)
	}
	if secondPending.Status != model.TaskRunStatusPending {
		t.Fatalf("second run status=%q, want pending after busy refusal", secondPending.Status)
	}
	if err := db.Model(&model.TaskRun{}).Where("id = ?", firstRunID).Update("status", model.TaskRunStatusSuccess).Error; err != nil {
		t.Fatalf("release first running slot: %v", err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", firstTask.ID).Update("status", string(StatusSuccess)).Error; err != nil {
		t.Fatalf("release first task aggregate: %v", err)
	}
	if err := manager.enterTaskExecutionForReason(context.Background(), secondRunID, loadedSecond.NodeID, startedAt.Add(time.Second), &loadedSecond, "manual"); err != nil {
		t.Fatalf("second execution admission after release: %v", err)
	}
}

func TestPolicyDisableCancelsPendingQuotaReservations(t *testing.T) {
	db := openConcurrentManagerTestDB(t)
	policy := seedQuotaPolicy(t, db, 1)
	taskEntity := seedQuotaPolicyTask(t, db, policy.ID)
	manager := &Manager{db: db, stateMachine: NewStateMachine(), executionOwnerID: "quota-disable-test"}
	run, err := manager.reserveTaskRun(context.Background(), taskEntity.NodeID, model.TaskRun{
		TaskID: taskEntity.ID, TriggerType: "auto", Status: model.TaskRunStatusPending,
	})
	if err != nil {
		t.Fatalf("reserve pending automation run: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := manager.DisablePolicyTx(context.Background(), tx, policy.ID)
		return err
	}); err != nil {
		t.Fatalf("disable quota policy: %v", err)
	}
	var canceled model.TaskRun
	if err := db.First(&canceled, run.ID).Error; err != nil {
		t.Fatalf("load canceled quota run: %v", err)
	}
	if canceled.Status != model.TaskRunStatusCanceled {
		t.Fatalf("disabled pending run status=%q, want canceled", canceled.Status)
	}
}
func TestPolicyMaxConcurrentCapsActualExecutorEntries(t *testing.T) {
	for _, maxConcurrent := range []int{1, 2} {
		t.Run(fmt.Sprintf("max_%d", maxConcurrent), func(t *testing.T) {
			db := openConcurrentManagerTestDB(t)
			policy := seedQuotaPolicy(t, db, maxConcurrent)
			tasks := make([]model.Task, maxConcurrent+1)
			for i := range tasks {
				tasks[i] = seedQuotaPolicyTask(t, db, policy.ID)
			}
			blocking := newBlockingExecutor()
			manager := NewManager(db, stubExecutorFactory{executor: blocking}, nil, nil, nil, nil, 8, 90)
			shutdownManagerOnCleanup(t, manager)

			for i := range maxConcurrent {
				if runID, err := manager.TriggerManual(tasks[i].ID); err != nil || runID == 0 {
					t.Fatalf("trigger admitted task %d run=%d err=%v", i, runID, err)
				}
			}
			waitForBlockingExecutorCalls(t, blocking, maxConcurrent)

			if runID, err := manager.TriggerManual(tasks[maxConcurrent].ID); !errors.Is(err, ErrPolicyConcurrencyLimit) || runID != 0 {
				t.Fatalf("over-quota trigger run=%d err=%v, want zero and ErrPolicyConcurrencyLimit", runID, err)
			}
			if got := blocking.Calls(); got != maxConcurrent {
				t.Fatalf("executor calls=%d after over-quota trigger, want %d", got, maxConcurrent)
			}

			close(blocking.release)
			manager.taskWG.Wait()
		})
	}
}
func TestPolicyMaxConcurrentCapsActualExecutorEntriesPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	for _, maxConcurrent := range []int{1, 2} {
		t.Run(fmt.Sprintf("max_%d", maxConcurrent), func(t *testing.T) {
			db := openTaskTerminalPostgresDB(t, dsn)
			if err := db.AutoMigrate(
				&model.SSHKey{}, &model.Node{}, &model.Policy{}, &model.Task{}, &model.TaskRun{},
				&model.TaskRunEffect{}, &model.RestoreDrillEvidence{}, &model.CredentialAuditEvent{},
				&model.TaskLog{}, &model.Alert{}, &model.Integration{}, &model.TaskTrafficSample{},
				&model.AlertDelivery{},
			); err != nil {
				t.Fatalf("migrate PostgreSQL quota fixtures: %v", err)
			}
			runPolicyMaxConcurrentCapsActualExecutorEntriesAcrossManagers(t, db, maxConcurrent)
		})
	}
}

func runPolicyMaxConcurrentCapsActualExecutorEntriesAcrossManagers(t *testing.T, db *gorm.DB, maxConcurrent int) {
	t.Helper()
	policy := seedQuotaPolicy(t, db, maxConcurrent)
	tasks := make([]model.Task, maxConcurrent+1)
	for i := range tasks {
		tasks[i] = seedQuotaPolicyTask(t, db, policy.ID)
	}
	blocking := newBlockingExecutor()
	managerA := NewManager(db, stubExecutorFactory{executor: blocking}, nil, nil, nil, nil, 8, 90)
	managerB := NewManager(db, stubExecutorFactory{executor: blocking}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, managerA)
	shutdownManagerOnCleanup(t, managerB)

	for i := range maxConcurrent {
		manager := managerA
		if i%2 == 1 {
			manager = managerB
		}
		if runID, err := manager.TriggerManual(tasks[i].ID); err != nil || runID == 0 {
			t.Fatalf("trigger admitted PostgreSQL task %d run=%d err=%v", i, runID, err)
		}
	}
	waitForBlockingExecutorCalls(t, blocking, maxConcurrent)

	if runID, err := managerB.TriggerManual(tasks[maxConcurrent].ID); !errors.Is(err, ErrPolicyConcurrencyLimit) || runID != 0 {
		t.Fatalf("PostgreSQL over-quota trigger run=%d err=%v, want zero and ErrPolicyConcurrencyLimit", runID, err)
	}
	if got := blocking.Calls(); got != maxConcurrent {
		t.Fatalf("PostgreSQL executor calls=%d after over-quota trigger, want %d", got, maxConcurrent)
	}

	close(blocking.release)
	managerA.taskWG.Wait()
	managerB.taskWG.Wait()
}

func waitForBlockingExecutorCalls(t *testing.T, executor *blockingExecutor, want int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if got := executor.Calls(); got >= want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("executor calls=%d, want at least %d", executor.Calls(), want)
		case <-ticker.C:
		}
	}
}
