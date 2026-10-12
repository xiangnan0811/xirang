package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"

	"gorm.io/gorm"
)

func seedBatchCommandEvidenceTask(t *testing.T, db *gorm.DB) (model.Task, model.BatchCommand, model.BatchCommandDispatch, model.TaskRun) {
	t.Helper()
	node := model.Node{Name: "batch-evidence-node", Port: 22, Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create evidence node: %v", err)
	}
	batch := model.BatchCommand{
		ID:                "batch-evidence-test",
		RequesterID:       1,
		IdempotencyKey:    "batch-evidence-key",
		RequestHash:       strings.Repeat("a", 64),
		ProvenanceVersion: 1,
	}
	if err := db.Create(&batch).Error; err != nil {
		t.Fatalf("create evidence batch: %v", err)
	}
	taskEntity := model.Task{
		Name:         "batch-evidence-task",
		NodeID:       node.ID,
		Command:      "printf 'evidence'",
		ExecutorType: "command",
		Status:       string(StatusRunning),
		BatchID:      batch.ID,
		Source:       "batch",
		Enabled:      true,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("create evidence task: %v", err)
	}
	fingerprint := model.BatchCommandTaskFingerprint(taskEntity)
	if fingerprint == "" {
		t.Fatal("evidence task fingerprint is empty")
	}
	dispatch := model.BatchCommandDispatch{
		TaskID:             taskEntity.ID,
		BatchID:            batch.ID,
		NodeID:             node.ID,
		Status:             "accepted",
		CommandFingerprint: fingerprint,
	}
	if err := db.Create(&dispatch).Error; err != nil {
		t.Fatalf("create evidence dispatch: %v", err)
	}
	run := model.TaskRun{
		TaskID:               taskEntity.ID,
		NodeIDSnapshot:       node.ID,
		ExecutorTypeSnapshot: "command",
		TriggerType:          "manual",
		Status:               model.TaskRunStatusRunning,
		ExecutionOwnerID:     "evidence-owner",
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create evidence run: %v", err)
	}
	if err := db.Model(&model.BatchCommandDispatch{}).
		Where("task_id = ?", taskEntity.ID).
		Update("run_id", run.ID).Error; err != nil {
		t.Fatalf("link evidence dispatch run: %v", err)
	}
	return taskEntity, batch, dispatch, run
}

func seedBatchCommandSkipTask(t *testing.T, db *gorm.DB, batchID string, provenanceVersion int, includeBatch bool) model.Task {
	t.Helper()
	node := model.Node{Name: "batch-skip-node-" + batchID, Port: 22, Username: "root", AuthType: "key", BackupDir: "/batch-skip/" + batchID}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create skip node: %v", err)
	}
	if includeBatch {
		batch := model.BatchCommand{
			ID:                batchID,
			RequesterID:       1,
			IdempotencyKey:    batchID + "-key",
			RequestHash:       strings.Repeat("b", 64),
			ProvenanceVersion: provenanceVersion,
		}
		if err := db.Create(&batch).Error; err != nil {
			t.Fatalf("create skip batch: %v", err)
		}
	}
	taskEntity := model.Task{
		Name:         "batch-skip-task-" + batchID,
		NodeID:       node.ID,
		Command:      "printf 'skip'",
		ExecutorType: "command",
		Status:       string(StatusPending),
		BatchID:      batchID,
		Source:       "batch",
		Enabled:      true,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("create skip task: %v", err)
	}
	if includeBatch && provenanceVersion == 1 {
		fingerprint := model.BatchCommandTaskFingerprint(taskEntity)
		if fingerprint == "" {
			t.Fatal("skip task fingerprint is empty")
		}
		dispatch := model.BatchCommandDispatch{
			TaskID:             taskEntity.ID,
			BatchID:            batchID,
			NodeID:             node.ID,
			Status:             "pending",
			CommandFingerprint: fingerprint,
		}
		if err := db.Create(&dispatch).Error; err != nil {
			t.Fatalf("create skip dispatch: %v", err)
		}
	}
	return taskEntity
}

func installBatchCommandSkipEvidenceInsertGuard(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`
		CREATE TRIGGER reject_batch_command_skip_evidence_after_terminal
		BEFORE INSERT ON batch_command_run_evidences
		WHEN EXISTS (
			SELECT 1 FROM task_runs
			WHERE id = NEW.run_id
			  AND (status NOT IN ('pending', 'running', 'retrying') OR finished_at IS NOT NULL)
		)
		BEGIN
			SELECT RAISE(ABORT, 'batch command evidence requires an active unfinished run');
		END
	`).Error; err != nil {
		t.Fatalf("install skip evidence guard: %v", err)
	}
}

func seedBatchCommandChildSkipTask(t *testing.T, db *gorm.DB, batchID string, disabled, archived bool) (model.Task, string) {
	t.Helper()
	node := model.Node{Name: "batch-child-skip-node-" + batchID, Port: 22, Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create child skip node: %v", err)
	}
	sourceBatch := model.BatchCommand{
		ID:                batchID + "-source",
		RequesterID:       1,
		IdempotencyKey:    batchID + "-source-key",
		RequestHash:       strings.Repeat("c", 64),
		ProvenanceVersion: 1,
	}
	if err := db.Create(&sourceBatch).Error; err != nil {
		t.Fatalf("create child source batch: %v", err)
	}
	sourceTask := model.Task{
		Name: "batch-child-source-task", NodeID: node.ID, ExecutorType: "command",
		Command: "printf 'source'", Status: string(StatusPending), BatchID: sourceBatch.ID,
		Source: "batch", Enabled: true,
	}
	if err := db.Create(&sourceTask).Error; err != nil {
		t.Fatalf("create child source task: %v", err)
	}
	sourceFingerprint := model.BatchCommandTaskFingerprint(sourceTask)
	if sourceFingerprint == "" {
		t.Fatal("child source fingerprint is empty")
	}
	sourceDispatch := model.BatchCommandDispatch{
		TaskID: sourceTask.ID, BatchID: sourceBatch.ID, NodeID: node.ID,
		Status: "accepted", CommandFingerprint: sourceFingerprint,
	}
	if err := db.Create(&sourceDispatch).Error; err != nil {
		t.Fatalf("create child source dispatch: %v", err)
	}
	sourceBatchID := sourceBatch.ID
	childBatch := model.BatchCommand{
		ID:                batchID,
		RequesterID:       1,
		IdempotencyKey:    batchID + "-key",
		RequestHash:       strings.Repeat("d", 64),
		ProvenanceVersion: 1,
		SourceBatchID:     &sourceBatchID,
	}
	if err := db.Create(&childBatch).Error; err != nil {
		t.Fatalf("create child batch: %v", err)
	}
	childTask := model.Task{
		Name: "batch-child-task", NodeID: node.ID, ExecutorType: "command",
		Command: "printf 'child'", Status: string(StatusPending), BatchID: childBatch.ID,
		Source: "batch", Enabled: !disabled,
	}
	if archived {
		archivedAt := time.Now().UTC()
		childTask.ArchivedAt = &archivedAt
	}
	if err := db.Create(&childTask).Error; err != nil {
		t.Fatalf("create child task: %v", err)
	}
	childFingerprint := model.BatchCommandTaskFingerprint(childTask)
	if childFingerprint == "" {
		t.Fatal("child fingerprint is empty")
	}
	retryOfTaskID := sourceTask.ID
	retryOfRunID := uint(0)
	childDispatch := model.BatchCommandDispatch{
		TaskID: childTask.ID, BatchID: childBatch.ID, NodeID: node.ID,
		Status: "pending", CommandFingerprint: childFingerprint,
		RetryOfTaskID: &retryOfTaskID, RetryOfRunID: &retryOfRunID,
	}
	if err := db.Create(&childDispatch).Error; err != nil {
		t.Fatalf("create child dispatch: %v", err)
	}
	return childTask, childFingerprint
}

func readBatchCommandEvidence(t *testing.T, db *gorm.DB, runID uint) model.BatchCommandRunEvidence {
	t.Helper()
	var evidence model.BatchCommandRunEvidence
	if err := db.Where("run_id = ?", runID).Take(&evidence).Error; err != nil {
		t.Fatalf("read batch command evidence: %v", err)
	}
	return evidence
}

func newEvidenceTerminalManager(db *gorm.DB) *Manager {
	return &Manager{db: db, stateMachine: NewStateMachine(), executionOwnerID: "evidence-owner"}
}

func TestBatchCommandRetrySkipPersistsSkippedRunAndUnknownEvidenceUnderGuard(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := seedBatchCommandSkipTask(t, db, "batch-skip-guard", 1, true)
	downstream := model.Task{
		Name:         "batch-skip-downstream",
		NodeID:       taskEntity.NodeID,
		ExecutorType: "local",
		Status:       string(StatusPending),
		Enabled:      true,
	}
	downstream.DependsOnTaskID = &taskEntity.ID
	if err := db.Create(&downstream).Error; err != nil {
		t.Fatalf("create skipped downstream task: %v", err)
	}
	installBatchCommandSkipEvidenceInsertGuard(t, db)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	if err := manager.skipTask(taskEntity, "skip-chain", nil, "前置任务失败，链式执行跳过"); err != nil {
		t.Fatalf("skip v1 batch command task: %v", err)
	}
	var run model.TaskRun
	if err := db.Where("task_id = ?", taskEntity.ID).Take(&run).Error; err != nil {
		t.Fatalf("load skipped v1 run: %v", err)
	}
	if run.Status != model.TaskRunStatusSkipped || run.FinishedAt == nil {
		t.Fatalf("skipped v1 run=%+v, want terminal skipped with finish time", run)
	}
	evidence := readBatchCommandEvidence(t, db, run.ID)
	if evidence.Outcome != "unknown" || evidence.FinishedAt == nil {
		t.Fatalf("skipped v1 evidence=%+v, want unknown terminal evidence", evidence)
	}
	var effect model.TaskRunEffect
	if err := db.Where("task_run_id = ? AND effect_type = ?", run.ID, model.TaskRunEffectTypeDownstreamSkip).
		Take(&effect).Error; err != nil {
		t.Fatalf("load skipped downstream effect: %v", err)
	}
	if effect.Status != model.TaskRunEffectStatusSucceeded {
		t.Fatalf("downstream effect status=%q, want succeeded", effect.Status)
	}
}

func TestBatchCommandRetrySkipRootDependencyShapeRecordsUnknownWithEmptyFingerprint(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := seedBatchCommandSkipTask(t, db, "batch-skip-root-mutated", 1, true)
	dependency := model.Task{
		Name:         "batch-skip-root-dependency",
		NodeID:       taskEntity.NodeID,
		ExecutorType: "local",
		Status:       string(StatusPending),
		Enabled:      true,
	}
	if err := db.Create(&dependency).Error; err != nil {
		t.Fatalf("create root dependency shape task: %v", err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).
		Update("depends_on_task_id", dependency.ID).Error; err != nil {
		t.Fatalf("mutate root dependency shape: %v", err)
	}
	installBatchCommandSkipEvidenceInsertGuard(t, db)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	if err := manager.skipTask(taskEntity, "skip-chain", nil, "root shape changed"); err != nil {
		t.Fatalf("skip mutated root v1 task: %v", err)
	}
	var run model.TaskRun
	if err := db.Where("task_id = ?", taskEntity.ID).Take(&run).Error; err != nil {
		t.Fatalf("load skipped mutated root run: %v", err)
	}
	evidence := readBatchCommandEvidence(t, db, run.ID)
	if evidence.CommandFingerprint != "" || evidence.Outcome != "unknown" || evidence.FinishedAt == nil {
		t.Fatalf("mutated root evidence=%+v, want empty fingerprint and unknown outcome", evidence)
	}
}

func TestBatchCommandRetrySkipDisabledAndArchivedChildKeepValidFingerprint(t *testing.T) {
	for _, tc := range []struct {
		name     string
		disabled bool
		archived bool
	}{
		{name: "disabled", disabled: true},
		{name: "archived", archived: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openManagerTestDB(t)
			taskEntity, fingerprint := seedBatchCommandChildSkipTask(t, db,
				"batch-child-skip-"+tc.name, tc.disabled, tc.archived)
			installBatchCommandSkipEvidenceInsertGuard(t, db)
			manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
			shutdownManagerOnCleanup(t, manager)

			if err := manager.skipTask(taskEntity, "skip-child-chain", nil, "子任务链式跳过"); err != nil {
				t.Fatalf("skip %s child task: %v", tc.name, err)
			}
			var run model.TaskRun
			if err := db.Where("task_id = ?", taskEntity.ID).Take(&run).Error; err != nil {
				t.Fatalf("load %s child skipped run: %v", tc.name, err)
			}
			evidence := readBatchCommandEvidence(t, db, run.ID)
			if evidence.CommandFingerprint != fingerprint ||
				evidence.Outcome != "unknown" || evidence.FinishedAt == nil {
				t.Fatalf("%s child evidence=%+v, want fingerprint/unknown terminal evidence", tc.name, evidence)
			}
		})
	}
}

func TestBatchCommandRetrySkipEvidenceFailureRollsBackRunAggregateAndEffects(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := seedBatchCommandSkipTask(t, db, "batch-skip-rollback", 1, true)
	injected := errors.New("BATCH_COMMAND_SKIP_EVIDENCE_WRITE_FAILURE")
	callbackName := fmt.Sprintf("test:batch-command-skip-evidence-write-failure-%d", taskEntity.ID)
	reached := false
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "batch_command_run_evidences" {
			reached = true
			_ = tx.AddError(injected)
		}
	}); err != nil {
		t.Fatalf("register skipped evidence failure callback: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	err := manager.skipTask(taskEntity, "skip-chain", nil, "skip must roll back")
	if !reached || !errors.Is(err, injected) {
		t.Fatalf("skip evidence failure error=%v reached=%v, want injected error", err, reached)
	}
	var runCount int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ?", taskEntity.ID).Count(&runCount).Error; err != nil {
		t.Fatalf("count rolled-back skipped runs: %v", err)
	}
	if runCount != 0 {
		t.Fatalf("skipped TaskRun survived evidence rollback: %d", runCount)
	}
	var evidenceCount int64
	if err := db.Model(&model.BatchCommandRunEvidence{}).Count(&evidenceCount).Error; err != nil {
		t.Fatalf("count rolled-back skipped evidence: %v", err)
	}
	if evidenceCount != 0 {
		t.Fatalf("skipped evidence survived rollback: %d", evidenceCount)
	}
	var afterTask model.Task
	if err := db.First(&afterTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload rolled-back skipped task: %v", err)
	}
	if afterTask.Status != string(StatusPending) || afterTask.LastError != "" {
		t.Fatalf("aggregate changed despite skipped evidence rollback: %+v", afterTask)
	}
	var effectCount int64
	if err := db.Model(&model.TaskRunEffect{}).Where("task_run_id IN (SELECT id FROM task_runs WHERE task_id = ?)", taskEntity.ID).
		Count(&effectCount).Error; err != nil {
		t.Fatalf("count skipped effects after rollback: %v", err)
	}
	if effectCount != 0 {
		t.Fatalf("skipped effects survived rollback: %d", effectCount)
	}
}

func TestBatchCommandRetrySkipLegacyAndV0RemainEvidenceFree(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	cases := []struct {
		name              string
		batchID           string
		provenanceVersion int
		includeBatch      bool
	}{
		{name: "retained pre81", batchID: "batch-skip-retained-pre81", includeBatch: false},
		{name: "v0 receipt", batchID: "batch-skip-v0", provenanceVersion: 0, includeBatch: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			taskEntity := seedBatchCommandSkipTask(t, db, tc.batchID, tc.provenanceVersion, tc.includeBatch)
			if err := manager.skipTask(taskEntity, "skip-legacy-chain", nil, "legacy skip"); err != nil {
				t.Fatalf("legacy skip: %v", err)
			}
			var run model.TaskRun
			if err := db.Where("task_id = ?", taskEntity.ID).Take(&run).Error; err != nil {
				t.Fatalf("load legacy skipped run: %v", err)
			}
			if run.Status != model.TaskRunStatusSkipped || run.FinishedAt == nil {
				t.Fatalf("legacy skipped run=%+v", run)
			}
			var evidenceCount int64
			if err := db.Model(&model.BatchCommandRunEvidence{}).
				Where("task_id = ?", taskEntity.ID).Count(&evidenceCount).Error; err != nil {
				t.Fatalf("count legacy skip evidence: %v", err)
			}
			if evidenceCount != 0 {
				t.Fatalf("legacy skip created %d retry evidence rows", evidenceCount)
			}
		})
	}
}

func TestBatchCommandRetryTerminalWritesSuccessEvidence(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity, _, _, run := seedBatchCommandEvidenceTask(t, db)
	manager := newEvidenceTerminalManager(db)
	finishedAt := time.Now().UTC()
	success := StatusSuccess
	if err := manager.terminalizeTaskRun(context.Background(), taskEntity.ID, run.ID,
		[]string{model.TaskRunStatusRunning}, &success, nil, StatusSuccess,
		map[string]interface{}{"finished_at": &finishedAt}); err != nil {
		t.Fatalf("terminalize successful batch command: %v", err)
	}
	evidence := readBatchCommandEvidence(t, db, run.ID)
	if evidence.Outcome != "success" || evidence.ExitCode != nil || evidence.FinishedAt == nil {
		t.Fatalf("unexpected success evidence: %+v", evidence)
	}
}

func TestBatchCommandRetryEvidenceWriteFailureRollsBackTerminalState(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity, _, _, run := seedBatchCommandEvidenceTask(t, db)
	injected := errors.New("BATCH_COMMAND_EVIDENCE_WRITE_FAILURE")
	callbackName := fmt.Sprintf("test:batch-command-evidence-write-failure-%d", run.ID)
	reached := false
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "batch_command_run_evidences" {
			reached = true
			_ = tx.AddError(injected)
		}
	}); err != nil {
		t.Fatalf("register evidence failure callback: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	manager := newEvidenceTerminalManager(db)
	success := StatusSuccess
	err := manager.terminalizeTaskRun(context.Background(), taskEntity.ID, run.ID,
		[]string{model.TaskRunStatusRunning}, &success, nil, StatusSuccess,
		map[string]interface{}{"last_error": "should roll back"})
	if !reached || !errors.Is(err, injected) {
		t.Fatalf("evidence failure error=%v reached=%v, want injected error", err, reached)
	}

	var afterTask model.Task
	var afterRun model.TaskRun
	if err := db.First(&afterTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload rolled-back task: %v", err)
	}
	if err := db.First(&afterRun, run.ID).Error; err != nil {
		t.Fatalf("reload rolled-back run: %v", err)
	}
	if afterTask.Status != string(StatusRunning) || afterTask.LastError != "" {
		t.Fatalf("Task changed despite evidence rollback: status=%q last_error=%q", afterTask.Status, afterTask.LastError)
	}
	if afterRun.Status != model.TaskRunStatusRunning || afterRun.LastError != "" ||
		afterRun.ExecutionOwnerID != "evidence-owner" || afterRun.FinishedAt != nil {
		t.Fatalf("TaskRun changed despite evidence rollback: %+v", afterRun)
	}
	var effects int64
	if err := db.Model(&model.TaskRunEffect{}).Where("task_run_id = ?", run.ID).Count(&effects).Error; err != nil {
		t.Fatalf("count effects after evidence rollback: %v", err)
	}
	if effects != 0 {
		t.Fatalf("evidence rollback left %d durable effects", effects)
	}
	evidence := readBatchCommandEvidence(t, db, run.ID)
	if evidence.Outcome != "pending" || evidence.FinishedAt != nil {
		t.Fatalf("evidence changed despite rollback: %+v", evidence)
	}
}

func TestBatchCommandRetryTerminalCASCompetitionCompletesOneEvidence(t *testing.T) {
	db := openConcurrentManagerTestDB(t)
	taskEntity, _, _, run := seedBatchCommandEvidenceTask(t, db)
	managerA := newEvidenceTerminalManager(db)
	managerB := newEvidenceTerminalManager(db)
	start := make(chan struct{})
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	for _, manager := range []*Manager{managerA, managerB} {
		wg.Add(1)
		go func(manager *Manager) {
			defer wg.Done()
			<-start
			failed := StatusFailed
			exitCode := 7
			errCh <- manager.terminalizeTaskRun(context.Background(), taskEntity.ID, run.ID,
				[]string{model.TaskRunStatusRunning}, &failed, nil, StatusFailed,
				map[string]interface{}{"last_error": "known failure"},
				taskRunTerminalOptions{CommandOutcome: &batchCommandRunOutcome{
					Fingerprint: model.BatchCommandTaskFingerprint(taskEntity),
					Outcome:     "exit_failure",
					ExitCode:    &exitCode,
				}})
		}(manager)
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil && !errors.Is(err, errTaskRunCASLost) {
			t.Fatalf("CAS competition terminalization: %v", err)
		}
	}
	evidence := readBatchCommandEvidence(t, db, run.ID)
	if evidence.Outcome != "exit_failure" || evidence.ExitCode == nil || *evidence.ExitCode != 7 {
		t.Fatalf("CAS winner did not publish known evidence: %+v", evidence)
	}
}

func TestBatchCommandRetryUnknownEvidenceCannotBePromotedByLaterKnownFailure(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity, _, _, firstRun := seedBatchCommandEvidenceTask(t, db)
	manager := newEvidenceTerminalManager(db)
	failed := StatusFailed
	if err := manager.terminalizeTaskRun(context.Background(), taskEntity.ID, firstRun.ID,
		[]string{model.TaskRunStatusRunning}, &failed, nil, StatusFailed,
		map[string]interface{}{"last_error": "transport result unknown"}); err != nil {
		t.Fatalf("terminalize unknown batch command: %v", err)
	}

	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).
		Update("status", string(StatusRunning)).Error; err != nil {
		t.Fatalf("reset task for retry evidence: %v", err)
	}
	secondRun := model.TaskRun{
		TaskID:               taskEntity.ID,
		NodeIDSnapshot:       taskEntity.NodeID,
		ExecutorTypeSnapshot: "command",
		TriggerType:          "retry",
		Status:               model.TaskRunStatusRunning,
		ExecutionOwnerID:     "evidence-owner",
	}
	if err := db.Create(&secondRun).Error; err != nil {
		t.Fatalf("create retry evidence run: %v", err)
	}
	exitCode := 9
	if err := manager.terminalizeTaskRun(context.Background(), taskEntity.ID, secondRun.ID,
		[]string{model.TaskRunStatusRunning}, &failed, nil, StatusFailed,
		map[string]interface{}{"last_error": "known exit failure"},
		taskRunTerminalOptions{CommandOutcome: &batchCommandRunOutcome{
			Fingerprint: model.BatchCommandTaskFingerprint(taskEntity),
			Outcome:     "exit_failure",
			ExitCode:    &exitCode,
		}}); err != nil {
		t.Fatalf("terminalize known retry failure: %v", err)
	}

	firstEvidence := readBatchCommandEvidence(t, db, firstRun.ID)
	secondEvidence := readBatchCommandEvidence(t, db, secondRun.ID)
	if firstEvidence.Outcome != "unknown" || secondEvidence.Outcome != "exit_failure" {
		t.Fatalf("unexpected retained evidence: first=%+v second=%+v", firstEvidence, secondEvidence)
	}
}

func TestBatchCommandRetryDirectCancellationCompletesUnknownEvidence(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity, _, _, run := seedBatchCommandEvidenceTask(t, db)
	if err := db.Model(&model.TaskRun{}).Where("id = ?", run.ID).
		Update("status", model.TaskRunStatusPending).Error; err != nil {
		t.Fatalf("prepare pending batch command: %v", err)
	}
	manager := newEvidenceTerminalManager(db)
	if err := manager.cancelTaskRunBeforeExecutor(taskEntity.ID, run.ID, "canceled before executor"); err != nil {
		t.Fatalf("cancel pending batch command: %v", err)
	}
	evidence := readBatchCommandEvidence(t, db, run.ID)
	if evidence.Outcome != "unknown" || evidence.FinishedAt == nil {
		t.Fatalf("direct cancellation left incomplete evidence: %+v", evidence)
	}
}

func TestBatchCommandRetryOutcomeClassificationIsStrict(t *testing.T) {
	taskEntity := model.Task{
		ID:           17,
		NodeID:       29,
		BatchID:      "batch-classification",
		Source:       "batch",
		ExecutorType: "command",
		Command:      "printf 'strict'",
	}
	fingerprint := model.BatchCommandTaskFingerprint(taskEntity)
	if fingerprint == "" {
		t.Fatal("classification fixture fingerprint is empty")
	}
	exitErr := &executor.CommandExitError{ExitCode: 7, Err: errors.New("remote exit")}
	known := batchCommandOutcomeForFailedRun(taskEntity, 7, fmt.Errorf("wrapped: %w", exitErr), context.Background(), context.Background())
	if known == nil || known.Outcome != "exit_failure" || known.ExitCode == nil || *known.ExitCode != 7 {
		t.Fatalf("known exit classification=%+v", known)
	}
	if mismatched := batchCommandOutcomeForFailedRun(taskEntity, 8, exitErr, context.Background(), context.Background()); mismatched != nil {
		t.Fatalf("mismatched exit code was classified: %+v", mismatched)
	}
	noStart := batchCommandOutcomeForFailedRun(taskEntity, -1,
		&executor.NoProcessStartError{Err: errors.New("dial failed")},
		context.Background(), context.Background())
	if noStart == nil || noStart.Outcome != "not_started" || noStart.ExitCode != nil {
		t.Fatalf("no-start classification=%+v", noStart)
	}
	if unknown := batchCommandOutcomeForFailedRun(taskEntity, -1, errors.New("transport unknown"),
		context.Background(), context.Background()); unknown != nil {
		t.Fatalf("unknown transport was classified: %+v", unknown)
	}
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if canceled := batchCommandOutcomeForFailedRun(taskEntity, 7, exitErr, canceledCtx, context.Background()); canceled != nil {
		t.Fatalf("canceled command was classified: %+v", canceled)
	}
}

func TestBatchCommandRetryExecutionBindingRejectsChangedCommandBeforeRunStart(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity, _, _, run := seedBatchCommandEvidenceTask(t, db)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).
		Update("command", "printf 'changed'").Error; err != nil {
		t.Fatalf("change batch command fixture: %v", err)
	}
	var lockedTask model.Task
	if err := db.First(&lockedTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("load changed task: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return validateBatchCommandExecutionBindingTx(tx, &lockedTask, run.ID)
	}); !errors.Is(err, errBatchCommandBindingMismatch) {
		t.Fatalf("binding error=%v, want errBatchCommandBindingMismatch", err)
	}
	var persisted model.TaskRun
	if err := db.First(&persisted, run.ID).Error; err != nil {
		t.Fatalf("read unchanged pending run: %v", err)
	}
	if persisted.Status != model.TaskRunStatusRunning {
		t.Fatalf("binding helper unexpectedly changed run status: %q", persisted.Status)
	}
}
