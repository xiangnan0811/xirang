package model

import (
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newBatchCommandTaskRunModelTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "batch-command-task-run.db")), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&Task{},
		&TaskRun{},
		&BatchCommand{},
		&BatchCommandDispatch{},
		&BatchCommandRunEvidence{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedBatchCommandTaskRunModelFixture(t *testing.T, db *gorm.DB, batchID string, sourceBatchID *string, command string) (Task, BatchCommandDispatch) {
	t.Helper()
	batch := BatchCommand{
		ID:                batchID,
		RequesterID:       1,
		IdempotencyKey:    batchID + "-key",
		RequestHash:       strings.Repeat("a", 64),
		ProvenanceVersion: 1,
		SourceBatchID:     sourceBatchID,
	}
	if err := db.Create(&batch).Error; err != nil {
		t.Fatalf("create batch: %v", err)
	}
	task := Task{
		Name:         "retry-task-" + batchID,
		NodeID:       7,
		ExecutorType: "command",
		Command:      command,
		Source:       "batch",
		Status:       TaskRunStatusPending,
		BatchID:      batchID,
		Enabled:      true,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	fingerprint := BatchCommandTaskFingerprint(task)
	if fingerprint == "" {
		t.Fatal("fixture task fingerprint is empty")
	}
	dispatch := BatchCommandDispatch{
		TaskID:             task.ID,
		BatchID:            batchID,
		NodeID:             task.NodeID,
		Status:             "pending",
		CommandFingerprint: fingerprint,
	}
	if err := db.Create(&dispatch).Error; err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	return task, dispatch
}

func TestBatchCommandRetryTaskRunV0PreservesOrdinaryHook(t *testing.T) {
	db := newBatchCommandTaskRunModelTestDB(t)
	batch := BatchCommand{
		ID:             "legacy-batch",
		RequesterID:    1,
		IdempotencyKey: "legacy-key",
		RequestHash:    strings.Repeat("b", 64),
	}
	if err := db.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	task := Task{
		Name:         "legacy-batch-task",
		NodeID:       7,
		ExecutorType: "command",
		Command:      "echo legacy",
		Source:       "batch",
		Status:       TaskRunStatusPending,
		BatchID:      batch.ID,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	run := TaskRun{TaskID: task.ID, TriggerType: "manual", Status: TaskRunStatusPending}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("v0 TaskRun rejected: %v", err)
	}
	if run.BatchCommandEvidence != nil {
		t.Fatal("v0 TaskRun unexpectedly prepared retry evidence")
	}
}

func TestBatchCommandRetryTaskRunRetainedPre81BatchIDWithoutReceiptIsLegacy(t *testing.T) {
	db := newBatchCommandTaskRunModelTestDB(t)
	task := Task{
		Name:         "retained-pre81-batch-task",
		NodeID:       7,
		ExecutorType: "command",
		Command:      "echo retained",
		Source:       "batch",
		Status:       TaskRunStatusPending,
		BatchID:      "retained-pre81-batch-id",
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	run := TaskRun{TaskID: task.ID, TriggerType: "manual", Status: TaskRunStatusPending}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("retained pre-000081 TaskRun rejected: %v", err)
	}
	if run.BatchCommandEvidence != nil {
		t.Fatal("retained pre-000081 TaskRun unexpectedly prepared retry evidence")
	}
}

func TestBatchCommandRetryTaskRunMissingBatchWithV1DispatchFailsClosed(t *testing.T) {
	db := newBatchCommandTaskRunModelTestDB(t)
	task, dispatch := seedBatchCommandTaskRunModelFixture(t, db, "missing-source-batch", nil, "echo missing source")
	if err := db.Exec("DELETE FROM batch_commands WHERE id = ?", dispatch.BatchID).Error; err != nil {
		t.Fatalf("delete v1 batch source for corruption fixture: %v", err)
	}
	run := TaskRun{TaskID: task.ID, TriggerType: "manual", Status: TaskRunStatusPending}
	if err := db.Create(&run).Error; err == nil {
		t.Fatal("TaskRun succeeded after permanent v1 dispatch lost its BatchCommand source")
	}
	var count int64
	if err := db.Model(&TaskRun{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("missing-source v1 TaskRun was persisted: %d", count)
	}
}

func TestBatchCommandRetryTaskRunV1MissingDispatchFailsClosed(t *testing.T) {
	db := newBatchCommandTaskRunModelTestDB(t)
	batch := BatchCommand{
		ID:                "missing-dispatch-batch",
		RequesterID:       1,
		IdempotencyKey:    "missing-dispatch-key",
		RequestHash:       strings.Repeat("c", 64),
		ProvenanceVersion: 1,
	}
	if err := db.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	task := Task{
		Name:         "missing-dispatch-task",
		NodeID:       7,
		ExecutorType: "command",
		Command:      "echo missing",
		Source:       "batch",
		Status:       TaskRunStatusPending,
		BatchID:      batch.ID,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	run := TaskRun{TaskID: task.ID, TriggerType: "manual", Status: TaskRunStatusPending}
	if err := db.Create(&run).Error; err == nil {
		t.Fatal("v1 TaskRun succeeded without a dispatch receipt")
	}
	var count int64
	if err := db.Model(&TaskRun{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed v1 TaskRun was persisted: %d", count)
	}
}

func TestBatchCommandRetryTaskRunEvidenceInsertRollsBackRun(t *testing.T) {
	db := newBatchCommandTaskRunModelTestDB(t)
	task, _ := seedBatchCommandTaskRunModelFixture(t, db, "evidence-rollback-batch", nil, "echo rollback")
	if err := db.Exec(`
		CREATE TRIGGER reject_batch_command_run_evidence
		BEFORE INSERT ON batch_command_run_evidences
		BEGIN
			SELECT RAISE(ABORT, 'reject evidence for rollback test');
		END
	`).Error; err != nil {
		t.Fatal(err)
	}

	run := TaskRun{TaskID: task.ID, TriggerType: "manual", Status: TaskRunStatusPending}
	if err := db.Create(&run).Error; err == nil {
		t.Fatal("TaskRun succeeded after evidence insert failure")
	}
	var runCount int64
	if err := db.Model(&TaskRun{}).Count(&runCount).Error; err != nil {
		t.Fatal(err)
	}
	if runCount != 0 {
		t.Fatalf("TaskRun insert was not rolled back: %d rows", runCount)
	}
	var evidenceCount int64
	if err := db.Model(&BatchCommandRunEvidence{}).Count(&evidenceCount).Error; err != nil {
		t.Fatal(err)
	}
	if evidenceCount != 0 {
		t.Fatalf("evidence row survived failed transaction: %d rows", evidenceCount)
	}
}

func TestBatchCommandRetryTaskRunRootShapeMutationStoresEmptyFingerprint(t *testing.T) {
	db := newBatchCommandTaskRunModelTestDB(t)
	task, _ := seedBatchCommandTaskRunModelFixture(t, db, "root-shape-batch", nil, "echo original")
	if err := db.Model(&Task{}).Where("id = ?", task.ID).Updates(map[string]any{
		"command":       "",
		"executor_type": "local",
	}).Error; err != nil {
		t.Fatal(err)
	}

	run := TaskRun{TaskID: task.ID, TriggerType: "manual", Status: TaskRunStatusPending}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("root task shape mutation was rejected: %v", err)
	}
	if run.BatchCommandEvidence == nil {
		t.Fatal("root v1 TaskRun did not prepare evidence")
	}
	if run.BatchCommandEvidence.RunID != run.ID {
		t.Fatalf("transient evidence run ID=%d, want %d", run.BatchCommandEvidence.RunID, run.ID)
	}
	if run.BatchCommandEvidence.CommandFingerprint != "" {
		t.Fatalf("root shape mutation fingerprint=%q, want empty", run.BatchCommandEvidence.CommandFingerprint)
	}
	var evidence BatchCommandRunEvidence
	if err := db.Where("run_id = ?", run.ID).First(&evidence).Error; err != nil {
		t.Fatal(err)
	}
	if evidence.RunID != run.ID || evidence.CommandFingerprint != "" || evidence.Outcome != "pending" {
		t.Fatalf("unexpected root evidence: %+v", evidence)
	}
}

func TestBatchCommandRetryTaskRunChildMutationRefused(t *testing.T) {
	db := newBatchCommandTaskRunModelTestDB(t)
	root := BatchCommand{
		ID:                "child-source-batch",
		RequesterID:       1,
		IdempotencyKey:    "child-source-key",
		RequestHash:       strings.Repeat("d", 64),
		ProvenanceVersion: 1,
	}
	if err := db.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	childSourceID := root.ID
	task, _ := seedBatchCommandTaskRunModelFixture(t, db, "child-batch", &childSourceID, "echo child")
	if err := db.Model(&Task{}).Where("id = ?", task.ID).Update("command", "echo mutated").Error; err != nil {
		t.Fatal(err)
	}

	run := TaskRun{TaskID: task.ID, TriggerType: "manual", Status: TaskRunStatusPending}
	if err := db.Create(&run).Error; err == nil {
		t.Fatal("child TaskRun succeeded after command mutation")
	}
	var runCount int64
	if err := db.Model(&TaskRun{}).Count(&runCount).Error; err != nil {
		t.Fatal(err)
	}
	if runCount != 0 {
		t.Fatalf("mutated child TaskRun was persisted: %d", runCount)
	}
	var evidenceCount int64
	if err := db.Model(&BatchCommandRunEvidence{}).Count(&evidenceCount).Error; err != nil {
		t.Fatal(err)
	}
	if evidenceCount != 0 {
		t.Fatalf("mutated child created evidence: %d", evidenceCount)
	}
}

func TestBatchCommandRetryTaskRunAutomaticReservationsHaveUniqueEvidence(t *testing.T) {
	db := newBatchCommandTaskRunModelTestDB(t)
	task, _ := seedBatchCommandTaskRunModelFixture(t, db, "automatic-evidence-batch", nil, "echo automatic")
	const reservations = 4
	seen := make(map[uint]struct{}, reservations)
	for i := range reservations {
		run := TaskRun{TaskID: task.ID, TriggerType: "cron", Status: TaskRunStatusPending}
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("create automatic reservation %d: %v", i, err)
		}
		if run.ID == 0 {
			t.Fatalf("automatic reservation %d received zero run ID", i)
		}
		if _, exists := seen[run.ID]; exists {
			t.Fatalf("automatic reservation reused run ID %d", run.ID)
		}
		seen[run.ID] = struct{}{}
		if run.BatchCommandEvidence == nil || run.BatchCommandEvidence.RunID != run.ID {
			t.Fatalf("reservation %d evidence=%+v, run ID=%d", i, run.BatchCommandEvidence, run.ID)
		}
	}
	var evidenceCount int64
	if err := db.Model(&BatchCommandRunEvidence{}).Count(&evidenceCount).Error; err != nil {
		t.Fatal(err)
	}
	if evidenceCount != reservations {
		t.Fatalf("evidence rows=%d, want %d", evidenceCount, reservations)
	}
}
