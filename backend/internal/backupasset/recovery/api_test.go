package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestRecoveryAPIServiceHidesObjectsAndReturnsOnlySafePlanJobViews(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"),
		&gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.BackupAssetRecoveryPlan{}, &model.BackupAssetRecoveryJob{},
		&model.BackupAssetRecoveryJobItem{}, &model.BackupAssetRecoveryAttempt{},
		&model.BackupAssetRecoveryCheckpoint{}, &model.BackupAssetRecoveryResultSet{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	planID, jobID := strings.Repeat("a", 32), strings.Repeat("b", 32)
	plan := model.BackupAssetRecoveryPlan{
		ID: planID, RequesterID: 17, State: string(PlanStateDraft), TransitionRevision: 1,
		RepositoryID: strings.Repeat("c", 32), RecoveryPointID: strings.Repeat("d", 32),
		TargetMode: string(TargetModeIsolated), TargetNodeID: 9, TargetRootID: "recovery-root",
		ConflictPolicy: string(ConflictFailOnConflict), SecurityDecision: string(SecurityDecisionAllowClean),
		SelectionDigest: strings.Repeat("1", 64), OperationSetDigest: strings.Repeat("2", 64),
		DeleteSetDigest: strings.Repeat("3", 64),
		EstimatedItems:  2, EstimatedBytes: 123,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE backup_asset_recovery_plans SET encrypted_target_root_locator = ?,
		encrypted_target_relative_path = ?, encrypted_override_reason = ? WHERE id = ?`,
		"FAKE_PRIVATE_ROOT_FOR_TEST_ONLY", "FAKE_PRIVATE_PATH_FOR_TEST_ONLY",
		"FAKE_PRIVATE_REASON_FOR_TEST_ONLY", planID).Error; err != nil {
		t.Fatal(err)
	}
	job := model.BackupAssetRecoveryJob{
		ID: jobID, PlanID: planID, State: string(JobStateQueued), TransitionRevision: 3,
		TargetMode: string(TargetModeIsolated), TargetNodeID: 9, TargetRootID: "recovery-root",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	audit := &recoveryAPIAuditSpy{}
	service, err := NewAPIService(APIServiceDependencies{DB: db, Now: func() time.Time { return now }, Audit: audit})
	if err != nil {
		t.Fatal(err)
	}

	planView, err := service.GetPlan(context.Background(), 17, planID)
	if err != nil || planView.ID != planID || planView.Revision != "1" || planView.TargetRootID != "recovery-root" ||
		planView.SelectionDigest != plan.SelectionDigest || planView.OperationSetDigest != plan.OperationSetDigest ||
		planView.DeleteSetDigest != plan.DeleteSetDigest {
		t.Fatalf("GetPlan() view=%+v err=%v", planView, err)
	}
	jobView, err := service.GetJob(context.Background(), 17, jobID)
	if err != nil || jobView.ID != jobID || jobView.Revision != "3" || jobView.PlanID != planID {
		t.Fatalf("GetJob() view=%+v err=%v", jobView, err)
	}
	projectedJob, err := service.ProjectJob(context.Background(), 17, jobID)
	if err != nil || projectedJob != jobView {
		t.Fatalf("ProjectJob() view=%+v err=%v, want no-audit projection %+v", projectedJob, err, jobView)
	}
	encoded, err := json.Marshal(struct {
		Plan RecoveryPlanView `json:"plan"`
		Job  RecoveryJobView  `json:"job"`
	}{Plan: planView, Job: jobView})
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"FAKE_PRIVATE_ROOT_FOR_TEST_ONLY", "FAKE_PRIVATE_PATH_FOR_TEST_ONLY", "FAKE_PRIVATE_REASON_FOR_TEST_ONLY"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("safe views leaked %q: %s", private, encoded)
		}
	}
	if _, err := service.GetPlan(context.Background(), 18, planID); !errors.Is(err, ErrRecoveryAPIObjectNotFound) {
		t.Fatalf("foreign plan error=%v", err)
	}
	if _, err := service.GetJob(context.Background(), 18, jobID); !errors.Is(err, ErrRecoveryAPIObjectNotFound) {
		t.Fatalf("foreign job error=%v", err)
	}

	canceled, err := service.CancelPlan(context.Background(), RecoveryPlanMutationRequest{
		RequesterID: 17, PlanID: planID, ExpectedRevision: 1,
	})
	if err != nil || canceled.State != PlanStateCanceled || canceled.Revision != "2" {
		t.Fatalf("CancelPlan() view=%+v err=%v", canceled, err)
	}
	if _, err := service.CancelPlan(context.Background(), RecoveryPlanMutationRequest{
		RequesterID: 17, PlanID: planID, ExpectedRevision: 1,
	}); !errors.Is(err, ErrRecoveryAPIConflict) {
		t.Fatalf("stale cancel error=%v", err)
	}
	if len(audit.events) != 3 || audit.events[0].Action != backupasset.AuditActionRecoveryPlan ||
		audit.events[1].Action != backupasset.AuditActionRecoveryVerify ||
		audit.events[1].RecoveryJobID != jobID || audit.events[2].Action != backupasset.AuditActionRecoveryCancel {
		t.Fatalf("Recovery API audit matrix=%+v", audit.events)
	}
	encodedAudit, err := json.Marshal(audit.events)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"FAKE_PRIVATE_ROOT_FOR_TEST_ONLY", "FAKE_PRIVATE_PATH_FOR_TEST_ONLY", "FAKE_PRIVATE_REASON_FOR_TEST_ONLY"} {
		if strings.Contains(string(encodedAudit), private) {
			t.Fatalf("audit leaked %q: %s", private, encodedAudit)
		}
	}
}

func TestRecoveryPreflightAPIProjectionExposesOnlyPublicSummary(t *testing.T) {
	now := time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC)
	result := PreflightPersistenceResult{
		PlanID: strings.Repeat("a", 32), Persisted: true, PlanTransitionRevision: 2,
		Evaluation: TargetPreflightResult{
			Eligible: true, Preferred: true, Reasons: []TargetPreflightReason{},
			Snapshot: TargetPreflightSnapshot{
				SchemaVersion: 1, ID: strings.Repeat("b", 32), Revision: "preflight-revision-1",
				TargetMode: TargetModeIsolated, ConflictPolicy: ConflictFailOnConflict,
				NodeRevision:         "FAKE_PRIVATE_NODE_REVISION_FOR_TEST_ONLY",
				SourceRevisionDigest: strings.Repeat("c", 64),
				CredentialRevision:   "FAKE_PRIVATE_CREDENTIAL_REVISION_FOR_TEST_ONLY",
				FindingSetDigest:     strings.Repeat("d", 64),
				OperationSetDigest:   strings.Repeat("e", 64), DeleteSetDigest: strings.Repeat("f", 64),
				Impact: RecoveryImpactSummary{
					CreateCount: 1, EstimatedItems: 1, EstimatedBytes: 512,
					Rows: []RecoveryImpactRow{{
						Kind: RecoveryOperationCreate, TargetPathDigest: "FAKE_PRIVATE_TARGET_PATH_DIGEST_FOR_TEST_ONLY",
					}},
				},
				ObservedAt: now, ExpiresAt: now.Add(time.Hour),
			},
			Security: PreflightSecurityDecision{
				Decision: SecurityDecision{
					Kind: SecurityDecisionAllowClean, DecisionDigest: strings.Repeat("1", 64),
					FindingSetDigest: strings.Repeat("2", 64), PolicyRevision: "FAKE_PRIVATE_POLICY_REVISION_FOR_TEST_ONLY",
				},
				FindingCount: 0,
			},
		},
	}
	view, err := ProjectPreflightResult(result)
	if err != nil || view.SchemaVersion != 1 || view.PlanID != result.PlanID || view.PlanRevision != "2" ||
		view.PreflightID != result.Evaluation.Snapshot.ID ||
		view.OperationSetDigest != result.Evaluation.Snapshot.OperationSetDigest ||
		view.DeleteSetDigest != result.Evaluation.Snapshot.DeleteSetDigest ||
		view.Impact.CreateCount != 1 || view.Impact.EstimatedBytes != 512 ||
		view.Security.Decision != SecurityDecisionAllowClean {
		t.Fatalf("ProjectPreflightResult() view=%+v err=%v", view, err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		"FAKE_PRIVATE_NODE_REVISION_FOR_TEST_ONLY", "FAKE_PRIVATE_CREDENTIAL_REVISION_FOR_TEST_ONLY",
		"FAKE_PRIVATE_TARGET_PATH_DIGEST_FOR_TEST_ONLY", "FAKE_PRIVATE_POLICY_REVISION_FOR_TEST_ONLY",
		"source_revision_digest", "credential_revision", "target_path_digest", "rows",
	} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("safe preflight view leaked %q: %s", private, encoded)
		}
	}
}

func TestRecoveryAPIServiceRequestsOwnedReadyResultCleanupByOpaqueRevision(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"),
		&gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.BackupAssetRecoveryPlan{}, &model.BackupAssetRecoveryJob{}, &model.BackupAssetRecoveryResultSet{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	planID, jobID, setID := strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32)
	if err := db.Create(&model.BackupAssetRecoveryPlan{ID: planID, RequesterID: 22}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetRecoveryJob{
		ID: jobID, PlanID: planID, TransitionRevision: 4, State: string(JobStateSucceeded),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetRecoveryResultSet{
		ID: setID, JobID: jobID, State: string(ResultSetStateReady), CleanupPhase: string(CleanupPhaseClaimed),
		PlaintextDeadline: now.Add(time.Hour), HardDeadline: now.Add(2 * time.Hour), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	audit := &recoveryAPIAuditSpy{err: errors.New("FAKE_SANITIZED_AUDIT_FAILURE_FOR_TEST_ONLY")}
	service, err := NewAPIService(APIServiceDependencies{DB: db, Now: func() time.Time { return now }, Audit: audit})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetRecoveryResultSet{}).Where("id = ?", setID).
		Update("cleanup_phase", CleanupPhaseTombstoned).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.RequestResultCleanup(context.Background(), RecoveryResultCleanupRequest{
		RequesterID: 22, JobID: jobID, ExpectedJobRevision: 4,
	}); !errors.Is(err, ErrRecoveryAPIConflict) {
		t.Fatalf("contradictory cleanup tuple error=%v, want conflict", err)
	}
	if err := db.Model(&model.BackupAssetRecoveryResultSet{}).Where("id = ?", setID).
		Update("cleanup_phase", CleanupPhaseClaimed).Error; err != nil {
		t.Fatal(err)
	}
	result, err := service.RequestResultCleanup(context.Background(), RecoveryResultCleanupRequest{
		RequesterID: 22, JobID: jobID, ExpectedJobRevision: 4,
	})
	if err != nil || result.JobID != jobID || result.ResultSetID != setID || result.State != ResultSetStateReady {
		t.Fatalf("RequestResultCleanup()=%+v err=%v", result, err)
	}
	var deadline time.Time
	if err := db.Table((model.BackupAssetRecoveryResultSet{}).TableName()).
		Select("plaintext_deadline").Where("id = ?", setID).Scan(&deadline).Error; err != nil {
		t.Fatal(err)
	}
	if !deadline.UTC().Equal(now) {
		t.Fatalf("cleanup deadline=%s want=%s", deadline, now)
	}
	if _, err := service.RequestResultCleanup(context.Background(), RecoveryResultCleanupRequest{
		RequesterID: 23, JobID: jobID, ExpectedJobRevision: 4,
	}); !errors.Is(err, ErrRecoveryAPIObjectNotFound) {
		t.Fatalf("foreign cleanup error=%v", err)
	}
	if _, err := service.RequestResultCleanup(context.Background(), RecoveryResultCleanupRequest{
		RequesterID: 22, JobID: strings.Repeat("9", 32), ExpectedJobRevision: 4,
	}); !errors.Is(err, ErrRecoveryAPIObjectNotFound) {
		t.Fatalf("missing cleanup job error=%v", err)
	}
	if len(audit.events) != 1 || audit.events[0].Action != backupasset.AuditActionRecoveryCleanup ||
		audit.events[0].RecoveryJobID != jobID || audit.events[0].Actor.UserID != 22 {
		t.Fatalf("cleanup audit=%+v", audit.events)
	}
}

func TestRecoveryAPIServiceProjectsOwnedWholeJobAndPagedRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"),
		&gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.BackupAssetRecoveryPlan{}, &model.BackupAssetRecoveryJob{},
		&model.BackupAssetRecoveryJobItem{}, &model.BackupAssetRecoveryAttempt{},
		&model.BackupAssetRecoveryCheckpoint{}, &model.BackupAssetRecoveryResultSet{},
		&model.BackupAssetRecoveryResult{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 16, 15, 0, 0, 0, time.UTC)
	planID, jobID := strings.Repeat("a", 32), strings.Repeat("b", 32)
	attemptID, checkpointID := strings.Repeat("c", 32), strings.Repeat("d", 32)
	resultPlanID, resultJobID := strings.Repeat("e", 32), strings.Repeat("f", 32)
	setID, resultID := strings.Repeat("0", 32), strings.Repeat("9", 32)
	if err := db.Create(&model.BackupAssetRecoveryPlan{
		ID: planID, RequesterID: 31, State: string(PlanStateExecuted), TransitionRevision: 8,
		RepositoryID: strings.Repeat("1", 32), RecoveryPointID: strings.Repeat("2", 32),
		TargetMode: string(TargetModeInPlace), TargetNodeID: 9, TargetRootID: "safe-root",
		ConflictPolicy: string(ConflictExactMirror), SecurityDecision: string(SecurityDecisionAllowClean),
		SelectionDigest: strings.Repeat("3", 64), OperationSetDigest: strings.Repeat("4", 64),
		DeleteSetDigest: strings.Repeat("5", 64), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetRecoveryJob{
		ID: jobID, PlanID: planID, State: string(JobStateRunning), TransitionRevision: 4,
		TargetMode: string(TargetModeInPlace), TargetNodeID: 9, TargetRootID: "safe-root",
		EstimatedItems: 2, EstimatedBytes: 21, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetRecoveryPlan{
		ID: resultPlanID, RequesterID: 31, State: string(PlanStateExecuted), TransitionRevision: 3,
		RepositoryID: strings.Repeat("1", 32), RecoveryPointID: strings.Repeat("2", 32),
		TargetMode: string(TargetModeIsolated), TargetNodeID: 9, TargetRootID: "safe-root",
		ConflictPolicy: string(ConflictFailOnConflict), SecurityDecision: string(SecurityDecisionAllowClean),
		SelectionDigest: strings.Repeat("3", 64), OperationSetDigest: strings.Repeat("4", 64),
		DeleteSetDigest: strings.Repeat("5", 64), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetRecoveryJob{
		ID: resultJobID, PlanID: resultPlanID, State: string(JobStateSucceeded), TransitionRevision: 5,
		TargetMode: string(TargetModeIsolated), TargetNodeID: 9, TargetRootID: "safe-root",
		EstimatedItems: 1, EstimatedBytes: 10, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	leaseExpiry := now.Add(10 * time.Minute)
	if err := db.Create(&model.BackupAssetRecoveryAttempt{
		ID: attemptID, JobID: jobID, OwnerID: "worker", Fence: 2,
		State: string(AttemptStateFailed), MutationArmed: true, LeaseExpiresAt: &leaseExpiry,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	deleteExpiry := now.Add(5 * time.Minute)
	if err := db.Create(&model.BackupAssetRecoveryCheckpoint{
		ID: strings.Repeat("7", 32), JobID: jobID, JobItemID: strings.Repeat("1", 32),
		AttemptID: attemptID, Sequence: 0, Phase: string(CheckpointPhaseOperation),
		AttemptFence: 2, NodeFence: 3, PlanBindingDigest: strings.Repeat("6", 64),
		SourceRevisionDigest: strings.Repeat("7", 64), PreflightID: strings.Repeat("8", 32),
		PreflightRevision: "preflight-1", PreflightExpiresAt: deleteExpiry,
		SecurityDecision: string(SecurityDecisionAllowClean), SecurityDecisionDigest: strings.Repeat("9", 64),
		SecurityFindingSetDigest: strings.Repeat("a", 64), SecurityPolicyRevision: "policy-1",
		AuthorityGrantID: strings.Repeat("b", 32), JobAuthorityCategory: string(AuthorityWrite),
		AuthorityBindingDigest: strings.Repeat("c", 64), AuthorityExpiresAt: deleteExpiry, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetRecoveryCheckpoint{
		ID: checkpointID, JobID: jobID, AttemptID: attemptID, Sequence: 1,
		Phase: string(CheckpointPhaseDeleteAuthorityRequired), AttemptFence: 2, NodeFence: 3,
		PlanBindingDigest: strings.Repeat("6", 64), SourceRevisionDigest: strings.Repeat("7", 64),
		PreflightID: strings.Repeat("8", 32), PreflightRevision: "preflight-1", PreflightExpiresAt: deleteExpiry,
		SecurityDecision: string(SecurityDecisionAllowClean), SecurityDecisionDigest: strings.Repeat("9", 64),
		SecurityFindingSetDigest: strings.Repeat("a", 64), SecurityPolicyRevision: "policy-1",
		AuthorityGrantID: strings.Repeat("b", 32), JobAuthorityCategory: string(AuthorityWrite),
		AuthorityBindingDigest: strings.Repeat("c", 64), AuthorityExpiresAt: deleteExpiry,
		DeleteNodeRevision: "node-1", DeleteRootRevision: "root-1", DeleteAuthorityExpiresAt: &deleteExpiry,
		CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	for index, outcome := range []string{"succeeded", "skipped"} {
		if err := db.Create(&model.BackupAssetRecoveryJobItem{
			ID: strings.Repeat(strconv.Itoa(index+1), 32), PlanID: planID, JobID: jobID, Ordinal: index,
			OperationKind: string([]RecoveryOperationKind{RecoveryOperationCreate, RecoveryOperationSkip}[index]),
			DisplayClass:  string(RecoveryDisplayClassRegular), Outcome: outcome,
			EstimatedBytes: int64(10 + index), BytesWritten: int64(10 - index*10),
			VerifiedSize: int64(10 + index), CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.BackupAssetRecoveryResultSet{
		ID: setID, JobID: resultJobID, State: string(ResultSetStateReady), CleanupPhase: string(CleanupPhaseClaimed),
		PlaintextDeadline: now.Add(time.Hour), HardDeadline: now.Add(2 * time.Hour), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetRecoveryResult{
		ID: resultID, ResultSetID: setID, JobID: resultJobID, ResultKind: string(RecoveryResultKindRegularFile),
		Classification: string(RecoveryResultClassificationNonSecret), ClassificationRevision: 1,
		ClassificationSourceRevision: 1, Size: 10, ModifiedAt: &now, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	service, err := NewAPIService(APIServiceDependencies{DB: db, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	job, err := service.GetJob(context.Background(), 31, jobID)
	if err != nil || job.DeleteCheckpoint != nil ||
		job.Progress.TotalItems != 2 || job.Progress.CompletedItems != 2 {
		t.Fatalf("GetJob()=%+v err=%v", job, err)
	}
	itemPage, err := service.ListJobItems(context.Background(), 31, jobID, RecoveryPageRequest{Page: 2, PageSize: 1})
	if err != nil || itemPage.Total != 2 || len(itemPage.Items) != 1 || itemPage.Items[0].Ordinal != 1 ||
		itemPage.Items[0].Outcome != RecoveryJobItemSkipped {
		t.Fatalf("ListJobItems()=%+v err=%v", itemPage, err)
	}
	if err := db.Model(&model.BackupAssetRecoveryJobItem{}).
		Where("job_id = ? AND ordinal = ?", jobID, 1).Update("operation_kind", "future_kind").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListJobItems(
		context.Background(), 31, jobID, RecoveryPageRequest{Page: 1, PageSize: 1},
	); !errors.Is(err, ErrRecoveryAPIUnavailable) {
		t.Fatalf("off-page malformed item error=%v, want unavailable", err)
	}
	if err := db.Model(&model.BackupAssetRecoveryJobItem{}).
		Where("job_id = ? AND ordinal = ?", jobID, 1).Update("operation_kind", string(RecoveryOperationSkip)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListJobItems(context.Background(), 32, jobID, RecoveryPageRequest{Page: 1, PageSize: 1}); !errors.Is(err, ErrRecoveryAPIObjectNotFound) {
		t.Fatalf("foreign item page error=%v", err)
	}
	encoded, err := json.Marshal(struct {
		Job   RecoveryJobView     `json:"job"`
		Items RecoveryJobItemPage `json:"items"`
	}{Job: job, Items: itemPage})
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		"workspace_phase", "attempt_fence", "node_fence", "plan_binding_digest", "source_revision_digest",
		"encrypted_relative_locator", "locator_digest", "classification_source_revision", "preflight_revision",
	} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("safe Recovery read product leaked %q: %s", private, encoded)
		}
	}
}

func TestRecoveryAPIProjectionRejectsContradictoryExactMirrorCheckpointHistory(t *testing.T) {
	execution := newExactMirrorOrdinaryExecutionFixture(t)
	source := newRecoveryRepositoryContractSource(t, execution.serviceFixture.db, execution.jobID)
	if err := execution.coordinator.ExecuteClaim(context.Background(), execution.claim, source, ""); err != nil {
		t.Fatalf("pause exact-mirror execution: %v", err)
	}
	service, err := NewAPIService(APIServiceDependencies{
		DB: execution.serviceFixture.db, Now: func() time.Time { return execution.serviceFixture.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	ownerID := execution.serviceFixture.request.RequesterID
	view, err := service.GetJob(context.Background(), ownerID, execution.jobID)
	if err != nil || view.DeleteCheckpoint == nil {
		t.Fatalf("valid paused projection=%+v err=%v", view, err)
	}
	if err := execution.serviceFixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("id = ?", view.DeleteCheckpoint.ID).
		Update("plan_binding_digest", strings.Repeat("f", 64)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetJob(context.Background(), ownerID, execution.jobID); !errors.Is(err, ErrRecoveryAPIUnavailable) {
		t.Fatalf("contradictory private checkpoint projection error=%v, want unavailable", err)
	}
}

func TestRecoveryAPIResultPageReusesWholePublishedResultEligibility(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)
	published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
	if err != nil || len(published.Results) < 2 {
		t.Fatalf("publish API result fixture=%+v err=%v", published, err)
	}
	service, err := NewAPIService(APIServiceDependencies{
		DB: fixture.db, Now: func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.ListPublishedResults(context.Background(), fixture.requesterID, fixture.job.ID,
		RecoveryPageRequest{Page: 1, PageSize: 1})
	if err != nil || page.Total != int64(len(published.Results)) || len(page.Items) != 1 {
		t.Fatalf("published API page=%+v err=%v", page, err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).
		Where("id = ?", published.ResultSetID).Update("cleanup_fence", 1).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetJob(context.Background(), fixture.requesterID, fixture.job.ID); !errors.Is(err, ErrRecoveryAPIUnavailable) {
		t.Fatalf("contradictory ResultSet job projection error=%v, want unavailable", err)
	}
	if _, err := service.ListPublishedResults(context.Background(), fixture.requesterID, fixture.job.ID,
		RecoveryPageRequest{Page: 1, PageSize: 1}); !errors.Is(err, ErrRecoveryAPIUnavailable) {
		t.Fatalf("contradictory ResultSet page projection error=%v, want unavailable", err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).
		Where("id = ?", published.ResultSetID).Update("cleanup_fence", 0).Error; err != nil {
		t.Fatal(err)
	}
	contradictoryItem := fixture.publishableRows[0]
	if err := fixture.db.Model(&model.BackupAssetRecoveryJobItem{}).
		Where("id = ? AND job_id = ?", contradictoryItem.ID, fixture.job.ID).
		Updates(map[string]any{"outcome": "", "bytes_written": 0, "verified_size": 0}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListPublishedResults(context.Background(), fixture.requesterID, fixture.job.ID,
		RecoveryPageRequest{Page: 1, PageSize: 1}); !errors.Is(err, ErrRecoveryAPIUnavailable) {
		t.Fatalf("partial terminal job result page error=%v, want unavailable", err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryJobItem{}).
		Where("id = ? AND job_id = ?", contradictoryItem.ID, fixture.job.ID).
		Updates(map[string]any{
			"outcome": contradictoryItem.Outcome, "bytes_written": contradictoryItem.BytesWritten,
			"verified_size": contradictoryItem.VerifiedSize,
		}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryResult{}).
		Where("id = ?", published.Results[1].ID).Update("classification_revision", 0).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListPublishedResults(context.Background(), fixture.requesterID, fixture.job.ID,
		RecoveryPageRequest{Page: 1, PageSize: 1}); !errors.Is(err, ErrRecoveryAPIUnavailable) {
		t.Fatalf("off-page ineligible result error=%v, want unavailable", err)
	}
}

func TestRecoveryAPIResultSetLifecycleTupleIsClosed(t *testing.T) {
	now := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	lease := now.Add(time.Minute)
	nodeLeaseID := strings.Repeat("b", 32)
	base := recoveryAPIResultSetRow{
		ID: strings.Repeat("a", 32), PlaintextDeadline: now.Add(time.Hour), HardDeadline: now.Add(2 * time.Hour),
		CreatedAt: now, UpdatedAt: now,
	}
	validRows := []struct {
		name string
		row  recoveryAPIResultSetRow
	}{
		{name: "ready", row: func() recoveryAPIResultSetRow {
			row := base
			row.State, row.CleanupPhase = string(ResultSetStateReady), string(CleanupPhaseClaimed)
			return row
		}()},
		{name: "cleanup_failed_drained", row: func() recoveryAPIResultSetRow {
			row := base
			row.State, row.CleanupPhase = string(ResultSetStateCleanupFailed), string(CleanupPhaseDrained)
			row.CleanupFence, row.CleanupAttempt = 2, 1
			return row
		}()},
		{name: "cleanup_failed_delete_started", row: func() recoveryAPIResultSetRow {
			row := base
			row.State, row.CleanupPhase = string(ResultSetStateCleanupFailed), string(CleanupPhaseDeleteStarted)
			row.CleanupFence, row.CleanupAttempt = 2, 1
			return row
		}()},
		{name: "cleaned", row: func() recoveryAPIResultSetRow {
			row := base
			row.State, row.CleanupPhase = string(ResultSetStateCleaned), string(CleanupPhaseTombstoned)
			row.CleanupFence, row.CleanupAttempt = 2, 1
			return row
		}()},
	}
	for _, phase := range []CleanupPhase{
		CleanupPhaseClaimed, CleanupPhaseRevoked, CleanupPhaseDrained, CleanupPhaseValidated,
		CleanupPhaseDeleteStarted, CleanupPhaseDeleted,
	} {
		row := base
		row.State, row.CleanupPhase = string(ResultSetStateRevoking), string(phase)
		row.CleanupOwner, row.CleanupLeaseExpiresAt = "worker-1", &lease
		row.CleanupFence, row.NodeLeaseID, row.NodeFence, row.CleanupAttempt = 2, &nodeLeaseID, 3, 1
		validRows = append(validRows, struct {
			name string
			row  recoveryAPIResultSetRow
		}{name: "revoking_" + string(phase), row: row})
	}
	for _, test := range validRows {
		t.Run(test.name, func(t *testing.T) {
			if !test.row.valid() {
				t.Fatalf("valid lifecycle tuple rejected: %+v", test.row)
			}
		})
	}

	invalidRows := []struct {
		name string
		row  recoveryAPIResultSetRow
	}{
		{name: "ready_with_cleanup_fence", row: func() recoveryAPIResultSetRow {
			row := validRows[0].row
			row.CleanupFence = 1
			return row
		}()},
		{name: "revoking_tombstoned", row: func() recoveryAPIResultSetRow {
			row := validRows[len(validRows)-1].row
			row.CleanupPhase = string(CleanupPhaseTombstoned)
			return row
		}()},
		{name: "revoking_without_owner", row: func() recoveryAPIResultSetRow {
			row := validRows[len(validRows)-1].row
			row.CleanupOwner = ""
			return row
		}()},
		{name: "revoking_without_node_lease", row: func() recoveryAPIResultSetRow {
			row := validRows[len(validRows)-1].row
			row.NodeLeaseID = nil
			return row
		}()},
		{name: "cleanup_failed_at_validated", row: func() recoveryAPIResultSetRow {
			row := validRows[1].row
			row.CleanupPhase = string(CleanupPhaseValidated)
			return row
		}()},
		{name: "cleanup_failed_with_lease", row: func() recoveryAPIResultSetRow {
			row := validRows[1].row
			row.CleanupLeaseExpiresAt = &lease
			return row
		}()},
		{name: "cleaned_at_deleted", row: func() recoveryAPIResultSetRow {
			row := validRows[3].row
			row.CleanupPhase = string(CleanupPhaseDeleted)
			return row
		}()},
		{name: "ready_created_at_plaintext_deadline", row: func() recoveryAPIResultSetRow {
			row := validRows[0].row
			row.CreatedAt = row.PlaintextDeadline
			return row
		}()},
	}
	for _, test := range invalidRows {
		t.Run(test.name, func(t *testing.T) {
			if test.row.valid() {
				t.Fatalf("contradictory lifecycle tuple accepted: %+v", test.row)
			}
		})
	}
}

func TestRecoveryAPIJobProductRejectsStateProgressAndOutcomeContradictions(t *testing.T) {
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	item := func(id string, operation RecoveryOperationKind, outcome RecoveryJobItemOutcome, estimated, written, verified int64) recoveryAPIJobItemRow {
		durableOutcome := string(outcome)
		if outcome == RecoveryJobItemPending {
			durableOutcome = ""
		}
		return recoveryAPIJobItemRow{
			ID: strings.Repeat(id, 32), OperationKind: string(operation), Outcome: durableOutcome,
			EstimatedBytes: estimated, BytesWritten: written, VerifiedSize: verified, CreatedAt: now, UpdatedAt: now,
		}
	}
	complete := []recoveryAPIJobItemRow{
		item("1", RecoveryOperationCreate, RecoveryJobItemSucceeded, 10, 10, 10),
		item("2", RecoveryOperationSkip, RecoveryJobItemSkipped, 5, 0, 5),
	}
	complete[1].Ordinal = 1
	for _, state := range []JobState{JobStateSucceeded, JobStateDegraded, JobStateVerifying} {
		job := recoveryAPIJobRow{State: string(state), EstimatedItems: 2, EstimatedBytes: 15}
		if _, err := validateRecoveryAPIJobProduct(job, complete); err != nil {
			t.Fatalf("valid complete %s product rejected: %v", state, err)
		}
	}
	pending := []recoveryAPIJobItemRow{item("3", RecoveryOperationCreate, RecoveryJobItemPending, 10, 0, 0)}
	failedBeforeWrite := recoveryAPIJobRow{
		State: string(JobStateFailed), FailureCategory: recoveryPreWriteDriftFailureCategory,
		EstimatedItems: 1, EstimatedBytes: 10,
	}
	if _, err := validateRecoveryAPIJobProduct(failedBeforeWrite, pending); err != nil {
		t.Fatalf("valid pre-write failed product rejected: %v", err)
	}

	failedItem := item("4", RecoveryOperationCreate, RecoveryJobItemFailed, 10, 10, 8)
	failedItem.FailureCategory = recoveryVerificationMismatchFailureCategory
	invalid := []struct {
		name string
		job  recoveryAPIJobRow
		rows []recoveryAPIJobItemRow
	}{
		{name: "succeeded_with_pending_item", job: recoveryAPIJobRow{
			State: string(JobStateSucceeded), EstimatedItems: 1, EstimatedBytes: 10,
		}, rows: pending},
		{name: "succeeded_with_failed_item", job: recoveryAPIJobRow{
			State: string(JobStateSucceeded), EstimatedItems: 1, EstimatedBytes: 10,
		}, rows: []recoveryAPIJobItemRow{failedItem}},
		{name: "degraded_with_failure_category", job: recoveryAPIJobRow{
			State: string(JobStateDegraded), FailureCategory: recoveryPostPauseFailureCategory,
			EstimatedItems: 2, EstimatedBytes: 15,
		}, rows: complete},
		{name: "failed_without_failure_category", job: recoveryAPIJobRow{
			State: string(JobStateFailed), EstimatedItems: 1, EstimatedBytes: 10,
		}, rows: pending},
		{name: "queued_with_completed_item", job: recoveryAPIJobRow{
			State: string(JobStateQueued), EstimatedItems: 1, EstimatedBytes: 10,
		}, rows: []recoveryAPIJobItemRow{item("5", RecoveryOperationCreate, RecoveryJobItemSucceeded, 10, 10, 10)}},
		{name: "create_success_byte_mismatch", job: recoveryAPIJobRow{
			State: string(JobStateRunning), EstimatedItems: 1, EstimatedBytes: 10,
		}, rows: []recoveryAPIJobItemRow{item("6", RecoveryOperationCreate, RecoveryJobItemSucceeded, 10, 10, 9)}},
		{name: "overwrite_success_byte_mismatch", job: recoveryAPIJobRow{
			State: string(JobStateRunning), EstimatedItems: 1, EstimatedBytes: 10,
		}, rows: []recoveryAPIJobItemRow{item("7", RecoveryOperationOverwrite, RecoveryJobItemSucceeded, 10, 9, 10)}},
		{name: "create_success_below_declared_bytes", job: recoveryAPIJobRow{
			State: string(JobStateRunning), EstimatedItems: 1, EstimatedBytes: 10,
		}, rows: []recoveryAPIJobItemRow{item("9", RecoveryOperationCreate, RecoveryJobItemSucceeded, 10, 9, 9)}},
		{name: "delete_success_with_bytes", job: recoveryAPIJobRow{
			State: string(JobStateRunning), EstimatedItems: 1, EstimatedBytes: 0,
		}, rows: []recoveryAPIJobItemRow{item("8", RecoveryOperationDelete, RecoveryJobItemSucceeded, 0, 0, 1)}},
		{name: "missing_job_item", job: recoveryAPIJobRow{
			State: string(JobStateRunning), EstimatedItems: 2, EstimatedBytes: 10,
		}, rows: pending},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateRecoveryAPIJobProduct(test.job, test.rows); !errors.Is(err, ErrRecoveryAPIUnavailable) {
				t.Fatalf("contradictory job product error=%v, want unavailable", err)
			}
		})
	}
}

type recoveryAPIAuditSpy struct {
	events []backupasset.AuditEventInput
	err    error
}

func (spy *recoveryAPIAuditSpy) Write(
	_ context.Context,
	input backupasset.AuditEventInput,
) (model.BackupAssetAuditEvent, error) {
	event, err := backupasset.NewAuditEvent(input)
	if err != nil {
		return model.BackupAssetAuditEvent{}, err
	}
	spy.events = append(spy.events, event.AuditEventInput)
	return model.BackupAssetAuditEvent{}, spy.err
}
