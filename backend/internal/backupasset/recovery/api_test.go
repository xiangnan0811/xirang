package recovery

import (
	"context"
	"encoding/json"
	"errors"
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
	if err := db.AutoMigrate(&model.BackupAssetRecoveryPlan{}, &model.BackupAssetRecoveryJob{}); err != nil {
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
