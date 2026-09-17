package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPublicationWorkerPendingOutcomeDoesNotCreateManagedCompletion(t *testing.T) {
	db, node := openManagedCompletionWorkerDB(t)
	recorder, err := newManagedCompletionStore(db)
	if err != nil {
		t.Fatal(err)
	}
	fake := &recordingManagedCompletionRecorder{store: recorder}
	worker, err := NewPublicationWorker(PublicationWorkerDependencies{
		Foundation: workerFoundation(true),
		Reconciler: &workerReconciler{candidates: nil},
		Completion: fake,
		Metrics:    publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}

	worker.process(context.Background(), workerPointIDOne)
	var count int64
	if err := db.Model(&model.BackupCompletion{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 || fake.recordCalls.Load() != 0 {
		t.Fatalf("pending publication created durable completion: rows=%d record_calls=%d", count, fake.recordCalls.Load())
	}
	var after model.Node
	if err := db.First(&after, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.LastBackupAt == nil || !after.LastBackupAt.Equal(*node.LastBackupAt) {
		t.Fatalf("pending publication changed node freshness: %v", after.LastBackupAt)
	}
}

func TestPublicationWorkerManagedCompletionReplaysFailedHandoffAcrossPassAndRestart(t *testing.T) {
	db, node := openManagedCompletionWorkerDB(t)
	store, err := newManagedCompletionStore(db)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &recordingManagedCompletionRecorder{store: store, failNext: true}
	reconciler := &committedWorkerReconciler{}
	worker, err := NewPublicationWorker(PublicationWorkerDependencies{
		Foundation: workerFoundation(true),
		Reconciler: reconciler,
		Completion: recorder,
		Metrics:    publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The provider commit is already durable in the committed RecoveryPoint, but
	// the first worker handoff fails. A bounded startup/periodic pass must repair
	// it without requiring a process restart.
	worker.process(context.Background(), workerPointIDOne)
	var count int64
	if err := db.Model(&model.BackupCompletion{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 || recorder.recordCalls.Load() != 1 {
		t.Fatalf("failed first handoff rows=%d record_calls=%d", count, recorder.recordCalls.Load())
	}
	if err := worker.StartupPass(context.Background()); err != nil {
		t.Fatalf("periodic replay pass: %v", err)
	}
	if err := db.Model(&model.BackupCompletion{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 || recorder.replayCalls.Load() != 1 {
		t.Fatalf("periodic replay rows=%d replay_calls=%d", count, recorder.replayCalls.Load())
	}

	// A newly constructed worker sees the same durable fact and must not append
	// a duplicate on restart.
	restarted, err := NewPublicationWorker(PublicationWorkerDependencies{
		Foundation: workerFoundation(true),
		Reconciler: reconciler,
		Completion: recorder,
		Metrics:    publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.StartupPass(context.Background()); err != nil {
		t.Fatalf("restart replay pass: %v", err)
	}
	if err := db.Model(&model.BackupCompletion{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 || recorder.replayCalls.Load() != 2 {
		t.Fatalf("restart replay rows=%d replay_calls=%d", count, recorder.replayCalls.Load())
	}
	var after model.Node
	if err := db.First(&after, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	if after.LastBackupAt == nil || !after.LastBackupAt.Equal(want) {
		t.Fatalf("replayed completion freshness=%v, want %s", after.LastBackupAt, want)
	}
}

func TestPublicationWorkerReplaySkipsPoisonedOldPage(t *testing.T) {
	db, node := openManagedCompletionWorkerDB(t)
	store, err := newManagedCompletionStore(db)
	if err != nil {
		t.Fatal(err)
	}

	var original model.RecoveryPoint
	if err := db.Where("id = ?", workerPointIDOne).Take(&original).Error; err != nil {
		t.Fatalf("load worker point: %v", err)
	}
	poisonedAt := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", workerPointIDOne).
		Updates(map[string]any{
			"semantics":    string(backupasset.PointImportedBaseline),
			"committed_at": poisonedAt,
			"updated_at":   poisonedAt,
		}).Error; err != nil {
		t.Fatalf("poison worker point: %v", err)
	}
	validAt := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	valid := original
	valid.ID = workerPointIDTwo
	valid.Semantics = string(backupasset.PointNativeSnapshot)
	valid.State = string(backupasset.RecoveryPointCommitted)
	valid.CommittedAt = &validAt
	valid.UpdatedAt = validAt
	if err := db.Create(&valid).Error; err != nil {
		t.Fatalf("create later worker point: %v", err)
	}

	if err := store.ReplayManagedCommitted(context.Background(), 1); err != nil {
		t.Fatalf("replay poisoned first page: %v", err)
	}
	var poisoned model.BackupCompletion
	if err := db.Where("evidence_ref = ?", workerPointIDOne).Take(&poisoned).Error; err != nil {
		t.Fatalf("read poisoned marker: %v", err)
	}
	if poisoned.EvidenceStatus != model.BackupCompletionEvidenceUnverified ||
		poisoned.FactKind != model.BackupCompletionKindLegacyUnverified ||
		poisoned.TaskRunID != nil {
		t.Fatalf("poisoned point marker=%+v", poisoned)
	}
	var verifiedCount int64
	if err := db.Model(&model.BackupCompletion{}).
		Where("evidence_status = ?", model.BackupCompletionEvidenceVerified).Count(&verifiedCount).Error; err != nil {
		t.Fatal(err)
	}
	if verifiedCount != 0 {
		t.Fatalf("poisoned first page produced verified facts=%d", verifiedCount)
	}

	if err := store.ReplayManagedCommitted(context.Background(), 1); err != nil {
		t.Fatalf("replay valid point after poison: %v", err)
	}
	var verified model.BackupCompletion
	if err := db.Where("evidence_ref = ?", workerPointIDTwo).Take(&verified).Error; err != nil {
		t.Fatalf("read valid completion: %v", err)
	}
	if verified.EvidenceStatus != model.BackupCompletionEvidenceVerified ||
		verified.FactKind != model.BackupCompletionKindManagedCommitted ||
		verified.TaskRunID == nil {
		t.Fatalf("valid point completion=%+v", verified)
	}
	var after model.Node
	if err := db.First(&after, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.LastBackupAt == nil || !after.LastBackupAt.Equal(validAt) {
		t.Fatalf("valid point freshness=%v, want %s", after.LastBackupAt, validAt)
	}
}

type recordingManagedCompletionRecorder struct {
	store       *managedCompletionStore
	failNext    bool
	recordCalls atomic.Int32
	replayCalls atomic.Int32
}

func (recorder *recordingManagedCompletionRecorder) RecordManagedCommitted(ctx context.Context, pointID string) error {
	recorder.recordCalls.Add(1)
	if recorder.failNext {
		recorder.failNext = false
		return errors.New("injected managed completion handoff failure")
	}
	return recorder.store.RecordManagedCommitted(ctx, pointID)
}

func (recorder *recordingManagedCompletionRecorder) ReplayManagedCommitted(ctx context.Context, limit int) error {
	recorder.replayCalls.Add(1)
	return recorder.store.ReplayManagedCommitted(ctx, limit)
}

func openManagedCompletionWorkerDB(t *testing.T) (*gorm.DB, model.Node) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "worker.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open worker database: %v", err)
	}
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.TaskRun{}, &model.BackupRepository{}, &model.RecoveryPoint{}, &model.BackupCompletion{}); err != nil {
		t.Fatalf("migrate worker database: %v", err)
	}
	old := time.Date(2026, 9, 1, 4, 0, 0, 0, time.UTC)
	node := model.Node{
		Name: "managed-completion-worker-node", Host: "127.0.0.1", Port: 22, Username: "root",
		AuthType: "password", Status: "online", BackupDir: "managed-completion-worker-node",
		LastBackupAt: &old, CreatedAt: old, UpdatedAt: old,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create worker node: %v", err)
	}
	task := model.Task{
		Name: "managed-completion-worker-task", NodeID: node.ID, ExecutorType: "restic", Status: "active",
		CreatedAt: old, UpdatedAt: old,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create worker task: %v", err)
	}
	run := model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: node.ID, ExecutorTypeSnapshot: "restic",
		TriggerType: "manual", Status: "success", CreatedAt: old, UpdatedAt: old,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create worker task run: %v", err)
	}
	repositoryID := strings.Repeat("a", 32)
	repository := model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "worker-repository",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: "{}", ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged),
		CreatedAt: old, UpdatedAt: old,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatalf("create worker repository: %v", err)
	}
	lineage, err := backupasset.EncodePublicationLineage(backupasset.PublicationLineageV1{
		Version: 1, TaskRepositoryLinkID: strings.Repeat("d", 32), TaskID: task.ID, TaskRunID: run.ID,
		Trigger: "manual", PublicationMode: string(backupasset.PublicationNativeSnapshot), PointCodecVersion: 1,
		TagCodecVersion: 1, StartedAt: old.Add(-2 * time.Minute), PreparedAt: old.Add(-time.Minute), PointDeadlineAt: old.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("encode worker point lineage: %v", err)
	}
	captured := time.Date(2026, 9, 12, 8, 55, 0, 0, time.UTC)
	committed := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	taskID, runID := task.ID, run.ID
	point := model.RecoveryPoint{
		ID: workerPointIDOne, RepositoryID: repositoryID, ProducingTaskID: &taskID, ProducingTaskRunID: &runID,
		ProducingTaskNameSnapshot: task.Name, ProducingNodeIDSnapshot: node.ID, ProducingNodeNameSnapshot: node.Name,
		LineageJSON: lineage, Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointCommitted),
		CapturedAt: &captured, CommittedAt: &committed, SourceFingerprint: "worker-source", ManifestDigestAlgorithm: "sha256",
		ConsistencyJSON: "{}", FidelityJSON: "{}", CapabilityRevision: 1, CapabilitiesJSON: "{}",
		ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged), PhysicalAvailability: string(backupasset.PhysicalOnline),
		HoldState: string(backupasset.HoldNone), CreatedAt: old, UpdatedAt: old,
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("create worker recovery point: %v", err)
	}
	return db, node
}
