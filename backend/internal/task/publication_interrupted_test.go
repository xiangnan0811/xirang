package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func setupInterruptedPublicationRun(t *testing.T) (*Manager, *gorm.DB, model.Task, uint, time.Time) {
	t.Helper()
	db := openManagerTestDB(t)
	if err := db.AutoMigrate(&model.RecoveryPoint{}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	startedAt := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]any{
		"executor_type": "restic", "status": "running", "last_run_at": &startedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	taskEntity.ExecutorType = "restic"
	taskEntity.Status = "running"
	taskEntity.LastRunAt = &startedAt
	run := model.TaskRun{TaskID: taskEntity.ID, TriggerType: "manual", Status: "running", StartedAt: &startedAt}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	return manager, db, taskEntity, run.ID, startedAt
}

func TestReportInterruptedPublicationMarksOnlyStaleExactRunWarningOrFailed(t *testing.T) {
	manager, db, taskEntity, runID, _ := setupInterruptedPublicationRun(t)
	currentID := strings.Repeat("a", 64)
	if err := manager.ReportInterruptedPublication(context.Background(), publication.Outcome{
		TaskID: taskEntity.ID, TaskRunID: runID, State: backupasset.RecoveryPointVerifying,
		NativePointID: currentID, ProviderCommitRecorded: true,
	}); err != nil {
		t.Fatalf("report committed stale run: %v", err)
	}
	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != "warning" || run.LastError != taskRunCodeInterruptedAfterProviderCommit || run.FinishedAt == nil {
		t.Fatalf("committed stale run=%+v", run)
	}

	if err := manager.ReportInterruptedPublication(context.Background(), publication.Outcome{
		TaskID: taskEntity.ID, TaskRunID: runID, State: backupasset.RecoveryPointFailed,
		Code: backupasset.FailureProviderCompletionUnproven,
	}); err != nil {
		t.Fatalf("report terminal pre-commit replay: %v", err)
	}
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != "warning" || run.LastError != taskRunCodeInterruptedAfterProviderCommit {
		t.Fatalf("terminal replay overwrote completed reporter state: %+v", run)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestReportInterruptedPublicationSkipsLiveCurrentProcessRun(t *testing.T) {
	manager, db, taskEntity, runID, _ := setupInterruptedPublicationRun(t)
	manager.pendingRuns.Store(taskEntity.ID, struct{}{})
	defer manager.pendingRuns.Delete(taskEntity.ID)
	if err := manager.ReportInterruptedPublication(context.Background(), publication.Outcome{
		TaskID: taskEntity.ID, TaskRunID: runID, State: backupasset.RecoveryPointCommitted,
		NativePointID: strings.Repeat("a", 64), ProviderCommitRecorded: true,
	}); err != nil {
		t.Fatal(err)
	}
	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != "running" || run.LastError != "" {
		t.Fatalf("live current-process run changed: %+v", run)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestReportInterruptedPublicationIgnoresPreparingAndEmptyOutcomes(t *testing.T) {
	manager, db, taskEntity, runID, _ := setupInterruptedPublicationRun(t)
	for _, outcome := range []publication.Outcome{
		{TaskID: taskEntity.ID, TaskRunID: runID, State: backupasset.RecoveryPointPreparing},
		{},
	} {
		if err := manager.ReportInterruptedPublication(context.Background(), outcome); err != nil {
			t.Fatalf("ignored outcome %+v returned %v", outcome, err)
		}
	}
	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != "running" || run.LastError != "" || run.FinishedAt != nil {
		t.Fatalf("nonterminal publication changed TaskRun: %+v", run)
	}
}

func TestReportInterruptedPublicationNeverOverwritesTerminalOrNewerRun(t *testing.T) {
	manager, db, taskEntity, runID, _ := setupInterruptedPublicationRun(t)
	if err := db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]any{"status": "success", "last_error": ""}).Error; err != nil {
		t.Fatal(err)
	}
	if err := manager.ReportInterruptedPublication(context.Background(), publication.Outcome{
		TaskID: taskEntity.ID, TaskRunID: runID, State: backupasset.RecoveryPointCommitted, ProviderCommitRecorded: true,
	}); err != nil {
		t.Fatal(err)
	}
	var terminal model.TaskRun
	if err := db.First(&terminal, runID).Error; err != nil {
		t.Fatal(err)
	}
	if terminal.Status != "success" || terminal.LastError != "" {
		t.Fatalf("terminal TaskRun was overwritten: %+v", terminal)
	}

	startedAt := time.Now().UTC()
	newer := model.TaskRun{TaskID: taskEntity.ID, TriggerType: "retry", Status: "running", StartedAt: &startedAt}
	if err := db.Create(&newer).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]any{"status": "running", "finished_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	if err := manager.ReportInterruptedPublication(context.Background(), publication.Outcome{
		TaskID: taskEntity.ID, TaskRunID: runID, State: backupasset.RecoveryPointFailed,
	}); err != nil {
		t.Fatal(err)
	}
	var taskAfter model.Task
	if err := db.First(&taskAfter, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	if taskAfter.Status != "running" {
		t.Fatalf("newer active run did not protect Task aggregate: %+v", taskAfter)
	}
}

func TestReconcileInterruptedRunsClassifiesTerminalPointAndLeavesPreparingUnresolved(t *testing.T) {
	manager, db, taskEntity, runID, startedAt := setupInterruptedPublicationRun(t)
	if err := db.AutoMigrate(&model.RecoveryPoint{}); err != nil {
		t.Fatal(err)
	}
	lineage, err := backupasset.EncodePublicationLineage(backupasset.PublicationLineageV1{
		Version: 1, TaskRepositoryLinkID: strings.Repeat("1", 32), TaskID: taskEntity.ID, TaskRunID: runID,
		Trigger: "manual", PublicationMode: string(backupasset.PublicationNativeSnapshot), PointCodecVersion: 1, TagCodecVersion: 1,
		StartedAt: startedAt, PreparedAt: startedAt.Add(time.Second), PointDeadlineAt: startedAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	finishedAt := startedAt.Add(2 * time.Second)
	consistency, err := backupasset.EncodePublicationConsistency(backupasset.PublicationConsistencyV1{
		Version: 1, PublicationRevision: 1, AttemptCount: 1, Provider: backupasset.ProviderRestic,
		CaptureStartedAt: &startedAt, CaptureFinishedAt: &finishedAt,
		RepositoryIdentityDigest: strings.Repeat("a", 64), RequestedTagDigest: strings.Repeat("b", 64), ProviderCommitDigest: strings.Repeat("c", 64),
		AdapterRevision: "restic-v1", CapabilityRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	point := model.RecoveryPoint{
		ID: strings.Repeat("2", 32), RepositoryID: strings.Repeat("3", 32), ProducingTaskID: &taskEntity.ID, ProducingTaskRunID: &runID,
		LineageJSON: lineage, EncryptedProviderLocator: `{"version":1}`, Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointVerifying),
		ConsistencyJSON: consistency, FidelityJSON: "{}", CapabilityRevision: 1, CapabilitiesJSON: "{}",
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), PhysicalAvailability: string(backupasset.PhysicalUnknown), HoldState: string(backupasset.HoldNone),
		CreatedAt: startedAt, UpdatedAt: startedAt,
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}

	unresolved, err := manager.ReconcileInterruptedRuns(context.Background(), 1)
	if err != nil || unresolved {
		t.Fatalf("reconcile exact stale run unresolved=%v err=%v", unresolved, err)
	}
	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != "warning" || run.LastError != taskRunCodeInterruptedAfterProviderCommit {
		t.Fatalf("reconciled run=%+v", run)
	}

	newRun := model.TaskRun{TaskID: taskEntity.ID, TriggerType: "manual", Status: "running", StartedAt: &startedAt}
	if err := db.Create(&newRun).Error; err != nil {
		t.Fatal(err)
	}
	unresolved, err = manager.ReconcileInterruptedRuns(context.Background(), 1)
	if err != nil || !unresolved {
		t.Fatalf("missing/preparing point must remain unresolved=%v err=%v", unresolved, err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestReconcileInterruptedRunsDetectsUnfilteredBatchRemainder(t *testing.T) {
	manager, db, taskEntity, _, startedAt := setupInterruptedPublicationRun(t)
	if err := db.AutoMigrate(&model.RecoveryPoint{}); err != nil {
		t.Fatal(err)
	}
	second := model.TaskRun{TaskID: taskEntity.ID, TriggerType: "retry", Status: "running", StartedAt: &startedAt}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	unresolved, err := manager.ReconcileInterruptedRuns(context.Background(), 1)
	if err != nil || !unresolved {
		t.Fatalf("batch-limited reconciliation hid stale remainder: unresolved=%v err=%v", unresolved, err)
	}
}

func TestReconcileInterruptedRunsQueriesOnlyTaskOwnedResticRuns(t *testing.T) {
	db := openManagerTestDB(t)
	if err := db.AutoMigrate(&model.RecoveryPoint{}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("executor_type", "rsync").Error; err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now().UTC()
	run := model.TaskRun{TaskID: taskEntity.ID, TriggerType: "manual", Status: "running", StartedAt: &startedAt}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	unresolved, err := manager.ReconcileInterruptedRuns(context.Background(), 10)
	if err != nil || unresolved {
		t.Fatalf("non-Restic TaskRun entered interrupted publication reconciliation: unresolved=%v err=%v", unresolved, err)
	}
}

func TestReconcileInterruptedRunsReportsManagedRsyncProviderCommit(t *testing.T) {
	manager, db, taskEntity, runID, startedAt := setupInterruptedPublicationRun(t)
	if err := db.AutoMigrate(&model.RecoveryPoint{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("executor_type", "rsync").Error; err != nil {
		t.Fatal(err)
	}
	linkID := strings.Repeat("1", 32)
	pointID := strings.Repeat("2", 32)
	attemptID := strings.Repeat("3", 32)
	deadline := startedAt.Add(time.Hour)
	lineage, err := backupasset.EncodePublicationLineage(backupasset.PublicationLineageV1{
		Version: 1, TaskRepositoryLinkID: linkID, TaskID: taskEntity.ID, TaskRunID: runID, Trigger: "manual",
		PublicationMode: string(backupasset.PublicationVersionedFullCopy), PointCodecVersion: 1, TagCodecVersion: 0,
		StartedAt: startedAt, PreparedAt: startedAt.Add(time.Second), PointDeadlineAt: deadline,
	})
	if err != nil {
		t.Fatal(err)
	}
	attempt := provider.RsyncTreeAttemptV1{
		RepositoryID: strings.Repeat("4", 32), TaskRepositoryLinkID: linkID, RecoveryPointID: pointID, AttemptID: attemptID,
		TaskID: taskEntity.ID, TaskRunID: runID, PublicationMode: backupasset.PublicationVersionedFullCopy, PointDeadlineAt: deadline,
		ExpectedTaskRevision: 1, RepositoryMarkerDigest: strings.Repeat("5", 64), ManagedRootIdentityDigest: strings.Repeat("6", 64),
		StagingComponent: pointID + "." + attemptID, FinalComponent: pointID, CommandProfileVersion: 1,
		PreflightID: strings.Repeat("7", 32), PreflightDigest: strings.Repeat("8", 64),
	}
	taggedAttempt, err := provider.EncodePublicationAttempt(provider.NewRsyncTreePublicationAttempt(attempt))
	if err != nil {
		t.Fatal(err)
	}
	locator, err := json.Marshal(struct {
		Version                   int    `json:"version"`
		Provider                  string `json:"provider"`
		RepositoryID              string `json:"repository_id"`
		RecoveryPointID           string `json:"recovery_point_id"`
		FinalComponent            string `json:"final_component"`
		ManagedRootIdentityDigest string `json:"managed_root_identity_digest"`
		CommitMarkerDigest        string `json:"commit_marker_digest"`
		TaggedAttempt             string `json:"tagged_attempt"`
		ChildFenceDigest          string `json:"child_fence_digest"`
	}{
		Version: 1, Provider: string(backupasset.ProviderRsync), RepositoryID: attempt.RepositoryID, RecoveryPointID: pointID,
		FinalComponent: pointID, ManagedRootIdentityDigest: attempt.ManagedRootIdentityDigest, CommitMarkerDigest: strings.Repeat("9", 64),
		TaggedAttempt: taggedAttempt, ChildFenceDigest: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	consistency, err := backupasset.EncodePublicationConsistency(backupasset.PublicationConsistencyV1{
		Version: 1, Provider: backupasset.ProviderRsync, RepositoryIdentityDigest: attempt.ManagedRootIdentityDigest,
		ProviderCommitDigest: strings.Repeat("b", 64), CapabilityRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	committedAt := startedAt.Add(2 * time.Second)
	point := model.RecoveryPoint{
		ID: pointID, RepositoryID: attempt.RepositoryID, ProducingTaskID: &taskEntity.ID, ProducingTaskRunID: &runID,
		LineageJSON: lineage, EncryptedProviderLocator: string(locator), Semantics: string(backupasset.PointXirangManifest), State: string(backupasset.RecoveryPointVerifying),
		ConsistencyJSON: consistency, FidelityJSON: `{"version":1,"digest":"` + strings.Repeat("c", 64) + `"}`,
		SourceFingerprint: strings.Repeat("d", 64), ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("e", 64), EntryCount: 1, LogicalBytes: 42,
		CapturedAt: &committedAt, CapabilityRevision: 1, CapabilitiesJSON: "{}", ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged),
		PhysicalAvailability: string(backupasset.PhysicalUnknown), HoldState: string(backupasset.HoldNone), CreatedAt: startedAt, UpdatedAt: startedAt,
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}

	unresolved, err := manager.ReconcileInterruptedRuns(context.Background(), 10)
	if err != nil || unresolved {
		t.Fatalf("managed Rsync interrupted reconciliation unresolved=%v err=%v", unresolved, err)
	}
	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != "warning" || run.LastError != taskRunCodeInterruptedAfterProviderCommit {
		t.Fatalf("managed Rsync interrupted TaskRun=%+v", run)
	}
}

func TestReconcileInterruptedRunsReportsManagedRcloneProviderCommit(t *testing.T) {
	manager, db, taskEntity, runID, startedAt := setupInterruptedPublicationRun(t)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("executor_type", "rclone").Error; err != nil {
		t.Fatal(err)
	}
	linkID := strings.Repeat("1", 32)
	pointID := strings.Repeat("2", 32)
	attemptID := strings.Repeat("3", 32)
	repositoryID := strings.Repeat("4", 32)
	deadline := startedAt.Add(time.Hour)
	lineage, err := backupasset.EncodePublicationLineage(backupasset.PublicationLineageV1{
		Version: 1, TaskRepositoryLinkID: linkID, TaskID: taskEntity.ID, TaskRunID: runID, Trigger: "manual",
		PublicationMode: string(backupasset.PublicationVersionedPrefix), PointCodecVersion: 1,
		StartedAt: startedAt, PreparedAt: startedAt.Add(time.Second), PointDeadlineAt: deadline,
	})
	if err != nil {
		t.Fatal(err)
	}
	attempt := provider.RcloneAttemptV1{
		SchemaVersion: 1, LayoutVersion: 1, MinimumRuntimeRevision: 1, Provider: backupasset.ProviderRclone,
		RepositoryID: repositoryID, TaskRepositoryLinkID: linkID, RecoveryPointID: pointID, AttemptID: attemptID,
		TaskID: taskEntity.ID, TaskRunID: runID, Trigger: "manual", PublicationMode: backupasset.PublicationVersionedPrefix,
		CaptureStartedAt: startedAt, PreparedAt: startedAt.Add(time.Second), PointDeadlineAt: deadline,
		ExpectedTaskRevision: 1, BindingRevision: 1, ConfigRevision: 1, ConfigDigest: strings.Repeat("5", 64),
		CapabilityRevision: 1, CredentialRevision: 1, PreflightID: strings.Repeat("6", 32), PreflightRevision: 1,
		PreflightDigest: strings.Repeat("7", 64), ManifestSchemaRevision: 1, ManifestLimitsRevision: 1,
		ManifestLimitsDigest: strings.Repeat("8", 64), RepositoryIdentityDigest: strings.Repeat("9", 64),
		ManagedRootIdentityDigest: strings.Repeat("a", 64), ChildFenceDigest: strings.Repeat("b", 64),
		LegacyOriginEvidenceDigest: strings.Repeat("c", 64),
		Portable: &provider.RclonePortableAttemptV1{
			AttemptComponent: pointID + "." + attemptID, DataComponent: "data", ControlComponent: "control",
			AttemptMarkerDigest: strings.Repeat("d", 64), ExpectedConsistencyClass: string(backupasset.RcloneConsistencyObservationallyStable),
			ExpectedHashFidelity: string(backupasset.RcloneHashProviderStrongChecksum),
		},
	}
	taggedAttempt, err := provider.EncodePublicationAttempt(provider.NewRclonePublicationAttempt(attempt))
	if err != nil {
		t.Fatal(err)
	}
	commit := provider.RcloneCommitV1{
		SchemaVersion: 1, LayoutVersion: 1, MinimumRuntimeRevision: 1,
		RepositoryID: repositoryID, TaskRepositoryLinkID: linkID, RecoveryPointID: pointID, AttemptID: attemptID,
		PublicationMode: backupasset.PublicationVersionedPrefix, PointDeadlineAt: deadline, ProviderCommittedAt: startedAt.Add(2 * time.Second),
		ManifestIndexDigest: strings.Repeat("e", 64), ManifestChunkDigests: []string{strings.Repeat("f", 64)}, ManifestEntryCount: 1, LogicalBytes: 42,
		SourceObservationDigest: strings.Repeat("1", 64), DestinationObservationDigest: strings.Repeat("2", 64),
		ContentProofDigest: strings.Repeat("3", 64), FidelityEvidenceDigest: strings.Repeat("4", 64), CostEvidenceDigest: strings.Repeat("5", 64),
		CapabilityEvidenceDigest: attempt.PreflightDigest, ChildFenceDigest: attempt.ChildFenceDigest,
		Portable: &provider.RclonePortableCommitV1{
			AttemptIdentityDigest: strings.Repeat("6", 64), ControlIdentityDigest: strings.Repeat("7", 64), DataIdentityDigest: strings.Repeat("8", 64),
			AttemptMarkerDigest: attempt.Portable.AttemptMarkerDigest, CommitComponent: "commit.json", CommitPayloadDigest: strings.Repeat("9", 64),
			CommitAuthenticationDigest: strings.Repeat("a", 64), ConsistencyEvidenceDigest: strings.Repeat("b", 64), HashEvidenceDigest: strings.Repeat("c", 64),
		},
	}
	taggedCommit, err := provider.EncodeProviderCommit(provider.NewRcloneProviderCommit(commit))
	if err != nil {
		t.Fatal(err)
	}
	commitDigest := sha256.Sum256([]byte(taggedCommit))
	physicalIdentity := strings.Repeat("d", 64)
	locator, err := json.Marshal(struct {
		Version                 int                             `json:"version"`
		Provider                backupasset.ProviderKind        `json:"provider"`
		RepositoryID            string                          `json:"repository_id"`
		RecoveryPointID         string                          `json:"recovery_point_id"`
		AttemptID               string                          `json:"attempt_id"`
		PublicationMode         backupasset.TaskPublicationMode `json:"publication_mode"`
		TaggedAttempt           string                          `json:"tagged_attempt"`
		TaggedCommit            string                          `json:"tagged_commit"`
		ChildFenceDigest        string                          `json:"child_fence_digest"`
		CommitPayloadDigest     string                          `json:"commit_payload_digest"`
		PortableAttemptRoot     string                          `json:"portable_attempt_root,omitempty"`
		NativeCommitKey         string                          `json:"native_commit_key,omitempty"`
		NativeCommitVersionID   string                          `json:"native_commit_version_id,omitempty"`
		PhysicalIdentityDigest  string                          `json:"physical_identity_digest"`
		ProviderCommitDigest    string                          `json:"provider_commit_digest"`
		ManifestControlIdentity string                          `json:"manifest_control_identity"`
	}{
		Version: 1, Provider: backupasset.ProviderRclone, RepositoryID: repositoryID, RecoveryPointID: pointID,
		AttemptID: attemptID, PublicationMode: backupasset.PublicationVersionedPrefix, TaggedAttempt: taggedAttempt, TaggedCommit: taggedCommit,
		ChildFenceDigest: attempt.ChildFenceDigest, CommitPayloadDigest: commit.Portable.CommitPayloadDigest,
		PortableAttemptRoot:    "backup:managed/points/" + attempt.Portable.AttemptComponent,
		PhysicalIdentityDigest: physicalIdentity, ProviderCommitDigest: hex.EncodeToString(commitDigest[:]),
		ManifestControlIdentity: commit.Portable.ControlIdentityDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	consistency, err := backupasset.EncodePublicationConsistency(backupasset.PublicationConsistencyV1{
		Version: 1, Provider: backupasset.ProviderRclone, RepositoryIdentityDigest: attempt.RepositoryIdentityDigest,
		ProviderCommitDigest: hex.EncodeToString(commitDigest[:]), CapabilityRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	committedAt := startedAt.Add(2 * time.Second)
	point := model.RecoveryPoint{
		ID: pointID, RepositoryID: repositoryID, ProducingTaskID: &taskEntity.ID, ProducingTaskRunID: &runID,
		LineageJSON: lineage, EncryptedProviderLocator: string(locator), Semantics: string(backupasset.PointXirangManifest), State: string(backupasset.RecoveryPointVerifying),
		ConsistencyJSON: consistency, FidelityJSON: `{}`, SourceFingerprint: physicalIdentity,
		ManifestDigestAlgorithm: "sha256", ManifestDigest: commit.ManifestIndexDigest, EntryCount: 1, LogicalBytes: 42,
		CapturedAt: &committedAt, CapabilityRevision: 1, CapabilitiesJSON: "{}", ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged),
		PhysicalAvailability: string(backupasset.PhysicalUnknown), HoldState: string(backupasset.HoldNone), CreatedAt: startedAt, UpdatedAt: startedAt,
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}

	unresolved, err := manager.ReconcileInterruptedRuns(context.Background(), 10)
	if err != nil || unresolved {
		t.Fatalf("managed Rclone interrupted reconciliation unresolved=%v err=%v", unresolved, err)
	}
	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != "warning" || run.LastError != taskRunCodeInterruptedAfterProviderCommit {
		t.Fatalf("managed Rclone interrupted TaskRun=%+v", run)
	}
}
