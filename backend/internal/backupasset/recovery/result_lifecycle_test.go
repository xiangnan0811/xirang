package recovery

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/task"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const (
	recoveryResultTestDefaultTTL = 24 * time.Hour
	recoveryResultTestHardCap    = 7 * 24 * time.Hour
	recoveryResultCleanupTTL     = 5 * time.Minute
)

type recoveryResultLifecycleFixture struct {
	db               *gorm.DB
	now              time.Time
	job              model.BackupAssetRecoveryJob
	requesterID      uint
	service          *ResultLifecycleService
	nodeAdmission    *recoveryResultNodeAdmission
	contentLifecycle *recoveryResultContentLifecycleFake
	target           *recoveryCleanupValidationTargetFake
	publishableRows  []model.BackupAssetRecoveryJobItem
}

type recoveryResultTestKeySource struct {
	material backupasset.DomainKeyMaterial
}

type recoveryResultNodeAdmission struct {
	now   func() time.Time
	err   error
	calls int
	order *[]string
}

type recoveryResultContentLifecycleFake struct {
	db                  *gorm.DB
	order               []string
	revokeErr           error
	cancelErr           error
	drainErr            error
	revokeJobID         string
	revokeReason        string
	revokeAt            time.Time
	cancelJobID         string
	drainJobID          string
	cancelObservedPhase CleanupPhase
	cancelObservedLease time.Time
	drainObservedPhase  CleanupPhase
	drainObservedLease  time.Time
	drainHook           func()
}

type recoveryCleanupValidationTargetFake struct {
	closedTargetPortFake
	db                        *gorm.DB
	now                       func() time.Time
	order                     *[]string
	calls                     int
	removeCalls               int
	removedValidationCalls    int
	removeMutations           int
	plannedMutations          int
	permits                   []TargetCleanupPermit
	requests                  []ValidateOwnedJobDirRequest
	removePermits             []TargetCleanupPermit
	removeRequests            []RemoveOwnedJobDirRequest
	removedValidationPermits  []TargetCleanupPermit
	removedValidationRequests []RemoveOwnedJobDirRequest
	observedPhase             CleanupPhase
	observedLeaseExpiry       time.Time
	observedContextExpiry     time.Time
	beforeObservation         func(TargetCleanupPermit, ValidateOwnedJobDirRequest)
	afterObservation          func(TargetCleanupPermit, ValidateOwnedJobDirRequest)
	beforeRemoveMutation      func(int, TargetCleanupPermit, RemoveOwnedJobDirRequest)
	afterRemove               func(TargetCleanupPermit, RemoveOwnedJobDirRequest)
	observation               *OwnedJobDirValidation
	mutateObservation         func(*OwnedJobDirValidation)
	removal                   OwnedJobDirRemoval
	err                       error
}

func (fake *recoveryResultContentLifecycleFake) RevokeRecoveryResultGrantsTx(
	ctx context.Context,
	tx *gorm.DB,
	recoveryJobID string,
	reason string,
	revokedAt time.Time,
) error {
	fake.order = append(fake.order, "revoke")
	fake.revokeJobID = recoveryJobID
	fake.revokeReason = reason
	fake.revokeAt = revokedAt
	if err := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryResult{}).
		Where("job_id = ?", recoveryJobID).Update("modified_at", revokedAt).Error; err != nil {
		return err
	}
	return fake.revokeErr
}

func (fake *recoveryResultContentLifecycleFake) CancelRecoveryResultReads(recoveryJobID string) error {
	fake.order = append(fake.order, "cancel")
	fake.cancelJobID = recoveryJobID
	var resultSet model.BackupAssetRecoveryResultSet
	if err := fake.db.Where("job_id = ?", recoveryJobID).Take(&resultSet).Error; err == nil {
		fake.cancelObservedPhase = CleanupPhase(resultSet.CleanupPhase)
		if resultSet.CleanupLeaseExpiresAt != nil {
			fake.cancelObservedLease = resultSet.CleanupLeaseExpiresAt.UTC()
		}
	}
	return fake.cancelErr
}

func (fake *recoveryResultContentLifecycleFake) DrainRecoveryResult(
	_ context.Context,
	recoveryJobID string,
) error {
	fake.order = append(fake.order, "drain")
	fake.drainJobID = recoveryJobID
	var resultSet model.BackupAssetRecoveryResultSet
	if err := fake.db.Where("job_id = ?", recoveryJobID).Take(&resultSet).Error; err == nil {
		fake.drainObservedPhase = CleanupPhase(resultSet.CleanupPhase)
		if resultSet.CleanupLeaseExpiresAt != nil {
			fake.drainObservedLease = resultSet.CleanupLeaseExpiresAt.UTC()
		}
	}
	if fake.drainHook != nil {
		fake.drainHook()
	}
	return fake.drainErr
}

func (fake *recoveryCleanupValidationTargetFake) ValidateOwnedJobDir(
	ctx context.Context,
	permit TargetCleanupPermit,
	request ValidateOwnedJobDirRequest,
) (OwnedJobDirValidation, error) {
	fake.calls++
	fake.permits = append(fake.permits, permit)
	fake.requests = append(fake.requests, request)
	if fake.order != nil {
		*fake.order = append(*fake.order, "target")
	}
	now := fake.now().UTC()
	if err := permit.ValidateOwnedJobDirRequestAt(now, request); err != nil {
		return OwnedJobDirValidation{}, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		fake.observedContextExpiry = deadline.UTC()
	}
	if fake.beforeObservation != nil {
		fake.beforeObservation(permit, request)
	}
	switch permit.ResourceKind {
	case CleanupResourceResultSet:
		var resultSet model.BackupAssetRecoveryResultSet
		if err := fake.db.Where("id = ? AND job_id = ?", permit.ResourceID, permit.JobID).
			Take(&resultSet).Error; err != nil {
			return OwnedJobDirValidation{}, err
		}
		fake.observedPhase = CleanupPhase(resultSet.CleanupPhase)
		if resultSet.CleanupLeaseExpiresAt != nil {
			fake.observedLeaseExpiry = resultSet.CleanupLeaseExpiresAt.UTC()
		}
	case CleanupResourceWorkspace:
		var job model.BackupAssetRecoveryJob
		if err := fake.db.Where("id = ?", permit.JobID).Take(&job).Error; err != nil {
			return OwnedJobDirValidation{}, err
		}
		fake.observedPhase = CleanupPhase(job.WorkspaceCleanupPhase)
		if job.WorkspaceCleanupLeaseExpiresAt != nil {
			fake.observedLeaseExpiry = job.WorkspaceCleanupLeaseExpiresAt.UTC()
		}
	default:
		return OwnedJobDirValidation{}, ErrInvalidTargetPermit
	}
	result := OwnedJobDirValidation{
		Object: request.Object, MarkerBindingDigest: request.MarkerBindingDigest,
		RootRevision: permit.RootRevision, TargetRevision: "target-revision-cleanup-observed",
	}
	if fake.observation != nil {
		result = *fake.observation
	}
	if fake.mutateObservation != nil {
		fake.mutateObservation(&result)
	}
	if fake.afterObservation != nil {
		fake.afterObservation(permit, request)
	}
	if fake.err != nil {
		return OwnedJobDirValidation{}, fake.err
	}
	return result, nil
}

func (fake *recoveryCleanupValidationTargetFake) RemoveOwnedJobDir(
	ctx context.Context,
	permit TargetCleanupPermit,
	request RemoveOwnedJobDirRequest,
) (OwnedJobDirRemoval, error) {
	fake.removeCalls++
	fake.removePermits = append(fake.removePermits, permit)
	fake.removeRequests = append(fake.removeRequests, request)
	for mutation := 0; mutation < fake.plannedMutations; mutation++ {
		if fake.beforeRemoveMutation != nil {
			fake.beforeRemoveMutation(mutation, permit, request)
		}
		if permit.proof == nil || permit.proof.validateLive == nil {
			return OwnedJobDirRemoval{}, ErrInvalidTargetPermit
		}
		if err := permit.proof.validateLive(ctx, permit); err != nil {
			return OwnedJobDirRemoval{}, err
		}
		fake.removeMutations++
	}
	if fake.err != nil {
		if fake.afterRemove != nil {
			fake.afterRemove(permit, request)
		}
		return OwnedJobDirRemoval{}, fake.err
	}
	return fake.removal, nil
}

func (fake *recoveryCleanupValidationTargetFake) ValidateOwnedJobDirRemoved(
	ctx context.Context,
	permit TargetCleanupPermit,
	request RemoveOwnedJobDirRequest,
) (OwnedJobDirRemovalValidation, error) {
	fake.removedValidationCalls++
	fake.removedValidationPermits = append(fake.removedValidationPermits, permit)
	fake.removedValidationRequests = append(fake.removedValidationRequests, request)
	if err := permit.ValidateAt(fake.now().UTC()); err != nil {
		return OwnedJobDirRemovalValidation{}, err
	}
	if permit.Operation != TargetCleanupValidateRemovedJobDir {
		return OwnedJobDirRemovalValidation{}, ErrInvalidTargetPermit
	}
	if fake.err != nil {
		return OwnedJobDirRemovalValidation{}, fake.err
	}
	return OwnedJobDirRemovalValidation{
		Object: request.Object, RootRevision: permit.RootRevision,
		TargetRevision: "target-revision-cleanup-removed-validated",
	}, nil
}

func (admission *recoveryResultNodeAdmission) AdmitRecoveryTx(
	ctx context.Context,
	tx *gorm.DB,
	nodeID uint,
) error {
	admission.calls++
	if admission.order != nil {
		*admission.order = append(*admission.order, "admit-node")
	}
	if admission.err != nil {
		return admission.err
	}
	now := admission.now().UTC()
	expired := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("node_id = ? AND state = ? AND lease_expires_at <= ?", nodeID, "active", now).
		Updates(map[string]any{"state": "expired", "released_at": now, "updated_at": now})
	if expired.Error != nil {
		return expired.Error
	}
	var active int64
	if err := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("node_id = ? AND state = ? AND lease_expires_at > ?", nodeID, "active", now).
		Count(&active).Error; err != nil {
		return err
	}
	if active != 0 {
		return task.ErrNodeWriteConflict
	}
	return nil
}

func (source recoveryResultTestKeySource) Active(
	_ context.Context,
	domain backupasset.KeyDomain,
) (backupasset.DomainKeyMaterial, error) {
	if domain != backupasset.KeyDomainRecoveryCleanupOwnership {
		return backupasset.DomainKeyMaterial{}, fmt.Errorf("unexpected key domain %q", domain)
	}
	return cloneDomainKeyMaterial(source.material), nil
}

func (source recoveryResultTestKeySource) ByVersion(
	_ context.Context,
	domain backupasset.KeyDomain,
	version int,
) (backupasset.DomainKeyMaterial, error) {
	if domain != backupasset.KeyDomainRecoveryCleanupOwnership || version != source.material.Version {
		return backupasset.DomainKeyMaterial{}, fmt.Errorf("unexpected recovery result key version %d", version)
	}
	return cloneDomainKeyMaterial(source.material), nil
}

func TestRecoveryResultPublicationCommitsOneAtomicReadyProjection(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)

	published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
	if err != nil {
		t.Fatalf("publish terminal isolated result set: %v", err)
	}
	if published.JobID != fixture.job.ID || published.ResultSetID == "" ||
		published.JobRevision != fixture.job.TransitionRevision+1 ||
		!published.PlaintextDeadline.Equal(fixture.job.PlaintextDeadline.UTC()) ||
		!published.HardDeadline.Equal(fixture.job.PlaintextDeadline.Add(recoveryResultTestHardCap-recoveryResultTestDefaultTTL).UTC()) ||
		len(published.Results) != len(fixture.publishableRows) {
		t.Fatalf("unexpected published result product: %+v", published)
	}

	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", fixture.job.ID).Take(&job).Error; err != nil {
		t.Fatalf("reload published job: %v", err)
	}
	if job.State != string(JobStateSucceeded) || job.WorkspacePhase != string(WorkspacePhasePublished) ||
		job.TransitionRevision != published.JobRevision {
		t.Fatalf("publication barrier did not advance exactly once: %+v", job)
	}

	var resultSet model.BackupAssetRecoveryResultSet
	if err := fixture.db.Where("id = ? AND job_id = ?", published.ResultSetID, fixture.job.ID).
		Take(&resultSet).Error; err != nil {
		t.Fatalf("load ready result set: %v", err)
	}
	if resultSet.State != string(ResultSetStateReady) || resultSet.CleanupPhase != string(CleanupPhaseClaimed) ||
		resultSet.CleanupOwner != "" || resultSet.CleanupFence != 0 || resultSet.NodeLeaseID != nil ||
		resultSet.MarkerBindingDigest != fixture.job.WorkspaceMarkerBindingDigest {
		t.Fatalf("unexpected initial result-set lifecycle: %+v", resultSet)
	}

	var rows []model.BackupAssetRecoveryResult
	if err := fixture.db.Where("result_set_id = ?", resultSet.ID).Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("load published result rows: %v", err)
	}
	if len(rows) != len(fixture.publishableRows) {
		t.Fatalf("published result row count = %d, want %d", len(rows), len(fixture.publishableRows))
	}
	for _, row := range rows {
		if row.ResultKind != string(RecoveryResultKindRegularFile) ||
			!validTargetRelativeLocator(row.EncryptedRelativeLocator) || !validDigest(row.LocatorDigest) ||
			row.Size < 0 || !validDigest(row.ContentDigest) ||
			RecoveryResultClassification(row.Classification) != RecoveryResultClassificationUnknown ||
			row.ClassificationRevision != 1 || row.ClassificationSourceRevision != 1 {
			t.Fatalf("invalid published result row: %+v", row)
		}
		var ciphertext string
		if err := fixture.db.Raw(
			"SELECT encrypted_relative_locator FROM backup_asset_recovery_results WHERE id = ?", row.ID,
		).Scan(&ciphertext).Error; err != nil {
			t.Fatalf("load raw result locator: %v", err)
		}
		if !secure.IsEncrypted(ciphertext) || ciphertext == row.EncryptedRelativeLocator ||
			strings.Contains(ciphertext, row.EncryptedRelativeLocator) {
			t.Fatalf("result locator was not ciphertext at rest: %q", ciphertext)
		}
	}
}

func TestRecoveryResultPublicationRejectsUnsafeOrPartialWorkspace(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*recoveryResultLifecycleFixture)
	}{
		{name: "in place", mutate: func(fixture *recoveryResultLifecycleFixture) {
			fixture.updateJob(t, map[string]any{"target_mode": string(TargetModeInPlace)})
		}},
		{name: "failed partial", mutate: func(fixture *recoveryResultLifecycleFixture) {
			fixture.updateJob(t, map[string]any{"state": string(JobStateFailed), "workspace_phase": string(WorkspacePhaseCleanupDue)})
		}},
		{name: "needs attention partial", mutate: func(fixture *recoveryResultLifecycleFixture) {
			fixture.updateJob(t, map[string]any{"state": string(JobStateNeedsAttention), "workspace_phase": string(WorkspacePhaseCleanupDue)})
		}},
		{name: "unsealed", mutate: func(fixture *recoveryResultLifecycleFixture) {
			fixture.updateJob(t, map[string]any{"workspace_phase": string(WorkspacePhaseWriting)})
		}},
		{name: "expired plaintext", mutate: func(fixture *recoveryResultLifecycleFixture) {
			deadline := fixture.now.Add(-time.Second)
			fixture.updateJob(t, map[string]any{"plaintext_deadline": deadline})
		}},
		{name: "active attempt", mutate: func(fixture *recoveryResultLifecycleFixture) {
			if err := fixture.db.Model(&model.BackupAssetRecoveryAttempt{}).Where("job_id = ?", fixture.job.ID).
				Updates(map[string]any{"state": string(AttemptStateRunning), "closed_at": nil}).Error; err != nil {
				t.Fatalf("restore active attempt: %v", err)
			}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryResultLifecycleFixture(t)
			test.mutate(fixture)
			_, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
			if !errors.Is(err, ErrInvalidResultPublication) {
				t.Fatalf("unsafe publication error = %v, want %v", err, ErrInvalidResultPublication)
			}
			fixture.assertNoPublishedRows(t)
		})
	}
}

func TestRecoveryResultPublicationRollsBackTheWholeBarrier(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)
	injected := errors.New("injected result-row persistence failure")
	callbackName := "task7:fail_result_row_create"
	if err := fixture.db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == (model.BackupAssetRecoveryResult{}).TableName() {
			_ = tx.AddError(injected)
		}
	}); err != nil {
		t.Fatalf("register publication fault: %v", err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Create().Remove(callbackName) })

	if _, err := fixture.service.Publish(context.Background(), fixture.publishRequest()); !errors.Is(err, injected) {
		t.Fatalf("publication fault error = %v, want injected failure", err)
	}
	fixture.assertNoPublishedRows(t)
}

func TestRecoveryResultRetainExtendsReadyDeadlineWithinImmutableHardCap(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)
	published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
	if err != nil {
		t.Fatalf("publish retained recovery result: %v", err)
	}
	requestedDeadline := published.PlaintextDeadline.Add(48 * time.Hour)

	retained, err := fixture.service.Retain(context.Background(), RetainRecoveryResultsRequest{
		JobID: published.JobID, ExpectedJobRevision: published.JobRevision,
		RequestedDeadline: requestedDeadline,
		Actor:             content.DeliveryActor{UserID: fixture.requesterID, Role: "admin"},
		Permissions:       backupasset.PermissionSet{backupasset.PermissionBackupAssetsRecover: true},
		Proof: &content.StepUpProof{
			Action: auth.StepUpActionRecoveryResultRetain,
			ID:     strings.Repeat("a", 32), ExpiresAt: fixture.now.Add(5 * time.Minute),
		},
	})
	if err != nil {
		t.Fatalf("retain ready recovery result: %v", err)
	}
	if retained.ResultSetID != published.ResultSetID || retained.JobID != published.JobID ||
		retained.JobRevision != published.JobRevision ||
		!retained.PlaintextDeadline.Equal(requestedDeadline) ||
		!retained.HardDeadline.Equal(published.HardDeadline) {
		t.Fatalf("unexpected retained recovery result: %+v", retained)
	}

	var resultSet model.BackupAssetRecoveryResultSet
	if err := fixture.db.Where("id = ? AND job_id = ?", published.ResultSetID, published.JobID).
		Take(&resultSet).Error; err != nil {
		t.Fatalf("reload retained recovery result set: %v", err)
	}
	if resultSet.State != string(ResultSetStateReady) ||
		!resultSet.PlaintextDeadline.Equal(requestedDeadline) ||
		!resultSet.HardDeadline.Equal(published.HardDeadline) || resultSet.CleanupFence != 0 {
		t.Fatalf("retain changed more than the ready deadline: %+v", resultSet)
	}
}

func TestRecoveryResultRetainRequiresExactFreshOwnerAuthorization(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*recoveryResultLifecycleFixture, *RetainRecoveryResultsRequest)
	}{
		{name: "non admin", mutate: func(_ *recoveryResultLifecycleFixture, request *RetainRecoveryResultsRequest) {
			request.Actor.Role = "viewer"
		}},
		{name: "wrong owner", mutate: func(_ *recoveryResultLifecycleFixture, request *RetainRecoveryResultsRequest) {
			request.Actor.UserID++
		}},
		{name: "missing recover permission", mutate: func(_ *recoveryResultLifecycleFixture, request *RetainRecoveryResultsRequest) {
			request.Permissions = backupasset.PermissionSet{}
		}},
		{name: "wrong proof purpose", mutate: func(_ *recoveryResultLifecycleFixture, request *RetainRecoveryResultsRequest) {
			request.Proof.Action = auth.StepUpActionRecoveryResultDownload
		}},
		{name: "expired proof", mutate: func(fixture *recoveryResultLifecycleFixture, request *RetainRecoveryResultsRequest) {
			request.Proof.ExpiresAt = fixture.now
		}},
		{name: "malformed proof id", mutate: func(_ *recoveryResultLifecycleFixture, request *RetainRecoveryResultsRequest) {
			request.Proof.ID = "not-an-opaque-id"
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryResultLifecycleFixture(t)
			published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
			if err != nil {
				t.Fatalf("publish authorization fixture: %v", err)
			}
			request := fixture.retainRequest(published, published.PlaintextDeadline.Add(time.Hour))
			test.mutate(fixture, &request)
			if _, err := fixture.service.Retain(context.Background(), request); !errors.Is(err, ErrRecoveryResultRetainDenied) {
				t.Fatalf("retain authorization error = %v, want %v", err, ErrRecoveryResultRetainDenied)
			}
			fixture.assertResultSetDeadline(t, published.ResultSetID, published.PlaintextDeadline)
		})
	}
}

func TestRecoveryResultRetainRejectsStaleNonExtendingOrCleanupState(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*recoveryResultLifecycleFixture, PublishedRecoveryResultSet, *RetainRecoveryResultsRequest)
		deadline func(PublishedRecoveryResultSet) time.Time
	}{
		{name: "stale job revision", mutate: func(_ *recoveryResultLifecycleFixture, _ PublishedRecoveryResultSet, request *RetainRecoveryResultsRequest) {
			request.ExpectedJobRevision++
		}},
		{name: "same deadline", deadline: func(published PublishedRecoveryResultSet) time.Time {
			return published.PlaintextDeadline
		}},
		{name: "shorter deadline", deadline: func(published PublishedRecoveryResultSet) time.Time {
			return published.PlaintextDeadline.Add(-time.Second)
		}},
		{name: "past hard cap", deadline: func(published PublishedRecoveryResultSet) time.Time {
			return published.HardDeadline.Add(time.Nanosecond)
		}},
		{name: "expired plaintext", mutate: func(fixture *recoveryResultLifecycleFixture, published PublishedRecoveryResultSet, request *RetainRecoveryResultsRequest) {
			fixture.now = published.PlaintextDeadline.Add(time.Second)
			request.Proof.ExpiresAt = fixture.now.Add(5 * time.Minute)
		}},
		{name: "revoking", mutate: func(fixture *recoveryResultLifecycleFixture, published PublishedRecoveryResultSet, _ *RetainRecoveryResultsRequest) {
			fixture.setResultSetCleanupState(t, published, ResultSetStateRevoking)
		}},
		{name: "cleanup failed", mutate: func(fixture *recoveryResultLifecycleFixture, published PublishedRecoveryResultSet, _ *RetainRecoveryResultsRequest) {
			fixture.setResultSetCleanupState(t, published, ResultSetStateCleanupFailed)
		}},
		{name: "cleaned", mutate: func(fixture *recoveryResultLifecycleFixture, published PublishedRecoveryResultSet, _ *RetainRecoveryResultsRequest) {
			fixture.setResultSetCleanupState(t, published, ResultSetStateCleaned)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryResultLifecycleFixture(t)
			published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
			if err != nil {
				t.Fatalf("publish retain conflict fixture: %v", err)
			}
			requestedDeadline := published.PlaintextDeadline.Add(time.Hour)
			if test.deadline != nil {
				requestedDeadline = test.deadline(published)
			}
			request := fixture.retainRequest(published, requestedDeadline)
			if test.mutate != nil {
				test.mutate(fixture, published, &request)
			}
			if _, err := fixture.service.Retain(context.Background(), request); !errors.Is(err, ErrRecoveryResultRetainConflict) {
				t.Fatalf("retain conflict error = %v, want %v", err, ErrRecoveryResultRetainConflict)
			}
			fixture.assertResultSetDeadline(t, published.ResultSetID, published.PlaintextDeadline)
		})
	}
}

func TestRecoveryResultCleanupClaimLocksJobAndNodeBeforeResultSetCAS(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)
	published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
	if err != nil {
		t.Fatalf("publish cleanup claim fixture: %v", err)
	}
	var priorMaxFence int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("node_id = ?", fixture.job.TargetNodeID).
		Select("COALESCE(MAX(fence), 0)").Scan(&priorMaxFence).Error; err != nil {
		t.Fatalf("load prior node fence: %v", err)
	}

	events := make([]string, 0, 8)
	resultSetLocks := make([]bool, 0, 2)
	fixture.nodeAdmission.order = &events
	queryCallback := "task7:record_cleanup_claim_queries"
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
		switch tx.Statement.Table {
		case (model.BackupAssetRecoveryResultSet{}).TableName():
			_, locked := tx.Statement.Clauses["FOR"]
			resultSetLocks = append(resultSetLocks, locked)
			events = append(events, "query-result-set")
		case (model.BackupAssetRecoveryJob{}).TableName():
			events = append(events, "lock-job")
		}
	}); err != nil {
		t.Fatalf("register cleanup query recorder: %v", err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Query().Remove(queryCallback) })
	createCallback := "task7:record_cleanup_claim_create"
	if err := fixture.db.Callback().Create().Before("gorm:create").Register(createCallback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.BackupAssetRecoveryNodeLease{}).TableName() {
			events = append(events, "create-node-lease")
		}
	}); err != nil {
		t.Fatalf("register cleanup create recorder: %v", err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Create().Remove(createCallback) })

	claim, err := fixture.service.ClaimCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{
		ResultSetID: published.ResultSetID, WorkerID: "cleanup-worker-a",
	})
	if err != nil {
		t.Fatalf("claim ready recovery result cleanup: %v", err)
	}
	if claim.ResultSetID != published.ResultSetID || claim.JobID != published.JobID ||
		claim.WorkerID != "cleanup-worker-a" || claim.CleanupFence != 1 || claim.CleanupAttempt != 1 ||
		claim.NodeFence != uint64(priorMaxFence+1) || claim.Phase != CleanupPhaseClaimed ||
		!claim.LeaseExpiresAt.Equal(fixture.now.Add(recoveryResultCleanupTTL)) {
		t.Fatalf("unexpected cleanup claim: %+v prior_node_fence=%d", claim, priorMaxFence)
	}
	if fixture.nodeAdmission.calls != 1 {
		t.Fatalf("node admission calls = %d, want 1", fixture.nodeAdmission.calls)
	}
	if len(resultSetLocks) != 2 || resultSetLocks[0] || !resultSetLocks[1] {
		t.Fatalf("candidate/claim ResultSet lock observations = %v, want [false true]", resultSetLocks)
	}
	assertOrderedEvents(t, events,
		"query-result-set", "lock-job", "admit-node", "create-node-lease", "query-result-set")

	var resultSet model.BackupAssetRecoveryResultSet
	if err := fixture.db.Where("id = ?", published.ResultSetID).Take(&resultSet).Error; err != nil {
		t.Fatalf("load claimed recovery result set: %v", err)
	}
	if resultSet.State != string(ResultSetStateRevoking) || resultSet.CleanupPhase != string(CleanupPhaseClaimed) ||
		resultSet.CleanupOwner != claim.WorkerID || resultSet.CleanupLeaseExpiresAt == nil ||
		!resultSet.CleanupLeaseExpiresAt.Equal(claim.LeaseExpiresAt) || resultSet.CleanupFence != claim.CleanupFence ||
		resultSet.NodeLeaseID == nil || *resultSet.NodeLeaseID != claim.NodeLeaseID ||
		resultSet.NodeFence != claim.NodeFence || resultSet.CleanupAttempt != claim.CleanupAttempt {
		t.Fatalf("unexpected claimed result-set row: %+v", resultSet)
	}
	var nodeLease model.BackupAssetRecoveryNodeLease
	if err := fixture.db.Where("id = ?", claim.NodeLeaseID).Take(&nodeLease).Error; err != nil {
		t.Fatalf("load cleanup node lease: %v", err)
	}
	if nodeLease.NodeID != fixture.job.TargetNodeID || nodeLease.HolderKind != "recovery_cleanup" ||
		nodeLease.JobID != fixture.job.ID || nodeLease.AttemptID != nil || nodeLease.OwnerID != claim.WorkerID ||
		nodeLease.Fence != claim.NodeFence || nodeLease.State != "active" || nodeLease.ReleasedAt != nil ||
		!nodeLease.LeaseExpiresAt.Equal(claim.LeaseExpiresAt) {
		t.Fatalf("unexpected cleanup node lease: %+v", nodeLease)
	}
}

func TestRecoveryResultCleanupLostCASReleasesFreshNodeLeaseInTransaction(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)
	published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
	if err != nil {
		t.Fatalf("publish cleanup lost-CAS fixture: %v", err)
	}
	injected := false
	callbackName := "task7:lose_cleanup_result_set_cas"
	if err := fixture.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if !injected && tx.Statement.Table == (model.BackupAssetRecoveryResultSet{}).TableName() {
			injected = true
			tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "1 = 0"}}})
		}
	}); err != nil {
		t.Fatalf("register cleanup CAS fault: %v", err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(callbackName) })

	_, err = fixture.service.ClaimCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{
		ResultSetID: published.ResultSetID, WorkerID: "cleanup-worker-lost-cas",
	})
	if !errors.Is(err, ErrRecoveryResultCleanupConflict) {
		t.Fatalf("lost cleanup CAS error = %v, want %v", err, ErrRecoveryResultCleanupConflict)
	}
	if !injected {
		t.Fatal("cleanup ResultSet CAS fault did not execute")
	}

	var resultSet model.BackupAssetRecoveryResultSet
	if err := fixture.db.Where("id = ?", published.ResultSetID).Take(&resultSet).Error; err != nil {
		t.Fatalf("load lost-CAS result set: %v", err)
	}
	if resultSet.State != string(ResultSetStateReady) || resultSet.CleanupFence != 0 ||
		resultSet.NodeLeaseID != nil || resultSet.CleanupAttempt != 0 {
		t.Fatalf("lost cleanup CAS changed ResultSet: %+v", resultSet)
	}
	var leases []model.BackupAssetRecoveryNodeLease
	if err := fixture.db.Where("job_id = ? AND holder_kind = ?", published.JobID, "recovery_cleanup").
		Find(&leases).Error; err != nil {
		t.Fatalf("load lost-CAS cleanup leases: %v", err)
	}
	if len(leases) != 1 || leases[0].State != "released" || leases[0].ReleasedAt == nil ||
		leases[0].OwnerID != "cleanup-worker-lost-cas" {
		t.Fatalf("lost cleanup CAS lease disposition = %+v", leases)
	}
}

func TestRecoveryResultCleanupClaimRetriesFailureAndTakesOverExpiredOwner(t *testing.T) {
	t.Run("cleanup failed restarts at claimed", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
		if err != nil {
			t.Fatalf("publish cleanup retry fixture: %v", err)
		}
		fixture.setResultSetCleanupState(t, published, ResultSetStateCleanupFailed)
		priorMaxFence := fixture.maxNodeFence(t)

		claim, err := fixture.service.ClaimCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{
			ResultSetID: published.ResultSetID, WorkerID: "cleanup-retry-worker",
		})
		if err != nil {
			t.Fatalf("retry cleanup-failed result set: %v", err)
		}
		if claim.CleanupFence != 2 || claim.CleanupAttempt != 2 ||
			claim.NodeFence != priorMaxFence+1 || claim.Phase != CleanupPhaseClaimed {
			t.Fatalf("unexpected cleanup retry claim: %+v prior_node_fence=%d", claim, priorMaxFence)
		}
		fixture.assertCleanupClaimRow(t, claim)
	})

	t.Run("expired revoking preserves durable phase", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
		if err != nil {
			t.Fatalf("publish cleanup takeover fixture: %v", err)
		}
		oldLeaseID := fixture.setResultSetCleanupState(t, published, ResultSetStateRevoking)
		oldExpiry := fixture.now.Add(-time.Minute)
		oldCreatedAt := oldExpiry.Add(-time.Minute)
		if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).Where("id = ?", oldLeaseID).
			Updates(map[string]any{
				"lease_expires_at": oldExpiry, "created_at": oldCreatedAt, "updated_at": fixture.now,
			}).Error; err != nil {
			t.Fatalf("expire old cleanup node lease: %v", err)
		}
		if err := fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).Where("id = ?", published.ResultSetID).
			Updates(map[string]any{
				"cleanup_phase": string(CleanupPhaseDrained), "cleanup_lease_expires_at": oldExpiry,
				"updated_at": fixture.now,
			}).Error; err != nil {
			t.Fatalf("expire old cleanup owner: %v", err)
		}
		priorMaxFence := fixture.maxNodeFence(t)

		claim, err := fixture.service.ClaimCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{
			ResultSetID: published.ResultSetID, WorkerID: "cleanup-takeover-worker",
		})
		if err != nil {
			t.Fatalf("take over expired cleanup owner: %v", err)
		}
		if claim.CleanupFence != 2 || claim.CleanupAttempt != 2 ||
			claim.NodeFence != priorMaxFence+1 || claim.Phase != CleanupPhaseDrained {
			t.Fatalf("unexpected cleanup takeover claim: %+v prior_node_fence=%d", claim, priorMaxFence)
		}
		fixture.assertCleanupClaimRow(t, claim)
		var oldLease model.BackupAssetRecoveryNodeLease
		if err := fixture.db.Where("id = ?", oldLeaseID).Take(&oldLease).Error; err != nil {
			t.Fatalf("load expired cleanup node lease: %v", err)
		}
		if oldLease.State != "expired" || oldLease.ReleasedAt == nil {
			t.Fatalf("old cleanup node lease was not expired: %+v", oldLease)
		}
	})
}

func TestRecoveryScheduledResultCleanupClaimRechecksCurrentPlaintextDeadline(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)
	published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.service.ClaimScheduledCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{
		ResultSetID: published.ResultSetID, WorkerID: "scheduled-cleanup-worker",
	}); !errors.Is(err, ErrRecoveryResultCleanupConflict) {
		t.Fatalf("future scheduled cleanup error=%v, want conflict", err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).
		Where("id = ?", published.ResultSetID).
		Updates(map[string]any{
			"plaintext_deadline": fixture.now.Add(-time.Minute),
			"updated_at":         fixture.now.Add(time.Second),
		}).Error; err != nil {
		t.Fatal(err)
	}
	fixture.now = fixture.now.Add(2 * time.Second)
	claim, err := fixture.service.ClaimScheduledCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{
		ResultSetID: published.ResultSetID, WorkerID: "scheduled-cleanup-worker",
	})
	if err != nil {
		t.Fatalf("due scheduled cleanup: %v", err)
	}
	if claim.ResultSetID != published.ResultSetID || claim.Phase != CleanupPhaseClaimed {
		t.Fatalf("scheduled cleanup claim=%+v", claim)
	}
}

func TestRecoveryListScheduledCleanupCandidatesIsClosedBoundedAndDueOnly(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)
	published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).
		Where("id = ?", published.ResultSetID).
		Updates(map[string]any{
			"plaintext_deadline": fixture.now.Add(-time.Minute),
			"updated_at":         fixture.now.Add(time.Second),
		}).Error; err != nil {
		t.Fatal(err)
	}
	fixture.now = fixture.now.Add(2 * time.Second)

	candidates, err := fixture.service.ListScheduledCleanupCandidates(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Kind != ScheduledCleanupResultSet ||
		candidates[0].ID != published.ResultSetID {
		t.Fatalf("scheduled cleanup candidates=%+v", candidates)
	}
	if _, err := fixture.service.ListScheduledCleanupCandidates(context.Background(), 0); !errors.Is(err, ErrInvalidResultLifecycle) {
		t.Fatalf("zero-limit scheduled candidates error=%v", err)
	}
}

func TestRecoveryListScheduledCleanupCandidatesIncludesUnpublishedWorkspace(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)
	fixture.prepareWorkspaceCleanupDue(t)

	candidates, err := fixture.service.ListScheduledCleanupCandidates(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Kind != ScheduledCleanupWorkspace ||
		candidates[0].ID != fixture.job.ID {
		t.Fatalf("scheduled workspace cleanup candidates=%+v", candidates)
	}
}

func TestRecoveryListScheduledCleanupCandidatesGloballyOrdersLifecycleKinds(t *testing.T) {
	now := time.Date(2026, 8, 13, 8, 9, 10, 0, time.UTC)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "cleanup-order.sqlite")),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.TaskRun{}, &model.BackupAssetRecoveryJob{}, &model.BackupAssetRecoveryResultSet{},
		&model.BackupAssetRecoveryNodeLease{}); err != nil {
		t.Fatal(err)
	}
	workspaceID := strings.Repeat("1", 32)
	workspaceDeadline := now.Add(time.Hour)
	workspace := model.BackupAssetRecoveryJob{
		ID: workspaceID, PlanID: strings.Repeat("2", 32), State: string(JobStateFailed),
		WorkspacePhase: string(WorkspacePhaseCleanupDue), WorkspaceCleanupPhase: string(CleanupPhaseClaimed),
		EncryptedWorkspaceRelativeLocator: "jobs/" + workspaceID,
		WorkspaceBindingDigest:            strings.Repeat("d", 64),
		WorkspaceMarkerBindingDigest:      strings.Repeat("e", 64),
		WorkspaceOwner:                    "workspace-owner", WorkspaceFence: 1,
		PlaintextDeadline: &workspaceDeadline, TargetMode: string(TargetModeIsolated), TargetNodeID: 8,
		UpdatedAt: now.Add(-2 * time.Hour), CreatedAt: now.Add(-3 * time.Hour),
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&workspace).Error; err != nil {
		t.Fatal(err)
	}
	resultSetID := strings.Repeat("3", 32)
	resultJob := model.BackupAssetRecoveryJob{
		ID: strings.Repeat("4", 32), PlanID: strings.Repeat("5", 32), State: string(JobStateSucceeded),
		WorkspacePhase: string(WorkspacePhasePublished), WorkspaceCleanupPhase: string(CleanupPhaseClaimed),
		WorkspaceMarkerBindingDigest: strings.Repeat("a", 64), TargetMode: string(TargetModeIsolated), TargetNodeID: 9,
		UpdatedAt: now.Add(-time.Hour), CreatedAt: now.Add(-2 * time.Hour),
	}
	if err := db.Create(&resultJob).Error; err != nil {
		t.Fatal(err)
	}
	resultSet := model.BackupAssetRecoveryResultSet{
		ID: resultSetID, JobID: resultJob.ID, State: string(ResultSetStateReady),
		MarkerBindingDigest: resultJob.WorkspaceMarkerBindingDigest,
		CleanupPhase:        string(CleanupPhaseClaimed), PlaintextDeadline: now.Add(-time.Hour),
		HardDeadline: now.Add(time.Hour), CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	if err := db.Create(&resultSet).Error; err != nil {
		t.Fatal(err)
	}
	malformedJob := model.BackupAssetRecoveryJob{
		ID: strings.Repeat("7", 32), PlanID: strings.Repeat("8", 32), State: string(JobStateSucceeded),
		WorkspacePhase: string(WorkspacePhasePublished), WorkspaceCleanupPhase: string(CleanupPhaseClaimed),
		WorkspaceMarkerBindingDigest: strings.Repeat("c", 64), TargetMode: string(TargetModeIsolated), TargetNodeID: 12,
		UpdatedAt: now.Add(-4 * time.Hour), CreatedAt: now.Add(-5 * time.Hour),
	}
	if err := db.Create(&malformedJob).Error; err != nil {
		t.Fatal(err)
	}
	malformed := model.BackupAssetRecoveryResultSet{
		ID: strings.Repeat("9", 32), JobID: malformedJob.ID, State: string(ResultSetStateCleanupFailed),
		MarkerBindingDigest: malformedJob.WorkspaceMarkerBindingDigest, CleanupPhase: "FAKE_INVALID_CLEANUP_PHASE",
		CleanupFence: 1, CleanupAttempt: 1,
		PlaintextDeadline: now.Add(-5 * time.Hour), HardDeadline: now.Add(time.Hour),
		CreatedAt: now.Add(-5 * time.Hour), UpdatedAt: now.Add(-4 * time.Hour),
	}
	if err := db.Create(&malformed).Error; err != nil {
		t.Fatal(err)
	}
	service := &ResultLifecycleService{db: db, now: func() time.Time { return now }}

	candidates, err := service.ListScheduledCleanupCandidates(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0] != (ScheduledCleanupCandidate{
		Kind: ScheduledCleanupWorkspace, ID: workspace.ID,
	}) {
		t.Fatalf("oldest global cleanup candidate=%+v, want workspace %s", candidates, workspace.ID)
	}

	activeLease := model.BackupAssetRecoveryNodeLease{
		ID: strings.Repeat("6", 32), NodeID: workspace.TargetNodeID, HolderKind: "recovery_job",
		JobID: workspace.ID, OwnerID: "active-writer", Fence: 1, State: "active",
		LeaseExpiresAt: now.Add(time.Hour), CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	if err := db.Create(&activeLease).Error; err != nil {
		t.Fatal(err)
	}
	candidates, err = service.ListScheduledCleanupCandidates(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0] != (ScheduledCleanupCandidate{
		Kind: ScheduledCleanupResultSet, ID: resultSet.ID,
	}) {
		t.Fatalf("busy-node filtered cleanup candidate=%+v, want result set %s", candidates, resultSet.ID)
	}
	if err := db.Delete(&activeLease).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.TaskRun{
		TaskID: 1, NodeIDSnapshot: workspace.TargetNodeID, Status: "running",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatal(err)
	}
	candidates, err = service.ListScheduledCleanupCandidates(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0] != (ScheduledCleanupCandidate{
		Kind: ScheduledCleanupResultSet, ID: resultSet.ID,
	}) {
		t.Fatalf("ordinary-writer filtered cleanup candidate=%+v, want result set %s", candidates, resultSet.ID)
	}
}

func TestRecoveryListScheduledCleanupCandidatesDurablyRotatesFailedRows(t *testing.T) {
	now := time.Date(2026, 8, 13, 9, 10, 11, 0, time.UTC)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "cleanup-restart.sqlite")),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.TaskRun{}, &model.BackupAssetRecoveryJob{}, &model.BackupAssetRecoveryResultSet{},
		&model.BackupAssetRecoveryNodeLease{}); err != nil {
		t.Fatal(err)
	}
	jobs := []model.BackupAssetRecoveryJob{
		{ID: strings.Repeat("1", 32), PlanID: strings.Repeat("3", 32), State: string(JobStateSucceeded),
			WorkspacePhase: string(WorkspacePhasePublished), WorkspaceMarkerBindingDigest: strings.Repeat("a", 64),
			TargetMode: string(TargetModeIsolated), TargetNodeID: 10, CreatedAt: now.Add(-4 * time.Hour), UpdatedAt: now.Add(-4 * time.Hour)},
		{ID: strings.Repeat("2", 32), PlanID: strings.Repeat("4", 32), State: string(JobStateSucceeded),
			WorkspacePhase: string(WorkspacePhasePublished), WorkspaceMarkerBindingDigest: strings.Repeat("b", 64),
			TargetMode: string(TargetModeIsolated), TargetNodeID: 11, CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now.Add(-3 * time.Hour)},
	}
	if err := db.Create(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	resultSets := []model.BackupAssetRecoveryResultSet{
		{ID: strings.Repeat("5", 32), JobID: jobs[0].ID, State: string(ResultSetStateCleanupFailed),
			MarkerBindingDigest: jobs[0].WorkspaceMarkerBindingDigest, CleanupPhase: string(CleanupPhaseDrained),
			CleanupFence: 1, CleanupAttempt: 1,
			PlaintextDeadline: now.Add(-3 * time.Hour), HardDeadline: now.Add(time.Hour),
			CreatedAt: now.Add(-4 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: strings.Repeat("6", 32), JobID: jobs[1].ID, State: string(ResultSetStateCleanupFailed),
			MarkerBindingDigest: jobs[1].WorkspaceMarkerBindingDigest, CleanupPhase: string(CleanupPhaseDrained),
			CleanupFence: 1, CleanupAttempt: 1,
			PlaintextDeadline: now.Add(-2 * time.Hour), HardDeadline: now.Add(time.Hour),
			CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now.Add(-time.Hour)},
	}
	if err := db.Create(&resultSets).Error; err != nil {
		t.Fatal(err)
	}
	service := &ResultLifecycleService{db: db, now: func() time.Time { return now }}
	first, err := service.ListScheduledCleanupCandidates(context.Background(), 1)
	if err != nil || len(first) != 1 || first[0].ID != resultSets[0].ID {
		t.Fatalf("first failed cleanup candidate=%+v error=%v", first, err)
	}

	now = now.Add(time.Minute)
	if err := db.Model(&model.BackupAssetRecoveryResultSet{}).Where("id = ?", resultSets[0].ID).
		Update("updated_at", now).Error; err != nil {
		t.Fatalf("persist failed cleanup retry: %v", err)
	}
	restarted := &ResultLifecycleService{db: db, now: func() time.Time { return now }}
	second, err := restarted.ListScheduledCleanupCandidates(context.Background(), 1)
	if err != nil || len(second) != 1 || second[0].ID != resultSets[1].ID {
		t.Fatalf("post-restart failed cleanup candidate=%+v error=%v, want %s", second, err, resultSets[1].ID)
	}
}

func TestRecoveryResultCleanupClaimRejectsActiveTerminalOrBusyCandidate(t *testing.T) {
	t.Run("active revoking owner", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
		if err != nil {
			t.Fatalf("publish active cleanup fixture: %v", err)
		}
		fixture.setResultSetCleanupState(t, published, ResultSetStateRevoking)
		if _, err := fixture.service.ClaimCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{
			ResultSetID: published.ResultSetID, WorkerID: "cleanup-active-loser",
		}); !errors.Is(err, ErrRecoveryResultCleanupConflict) {
			t.Fatalf("active cleanup claim error = %v, want %v", err, ErrRecoveryResultCleanupConflict)
		}
	})

	t.Run("cleaned tombstone", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
		if err != nil {
			t.Fatalf("publish cleaned cleanup fixture: %v", err)
		}
		fixture.setResultSetCleanupState(t, published, ResultSetStateCleaned)
		if _, err := fixture.service.ClaimCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{
			ResultSetID: published.ResultSetID, WorkerID: "cleanup-cleaned-loser",
		}); !errors.Is(err, ErrRecoveryResultCleanupConflict) {
			t.Fatalf("cleaned cleanup claim error = %v, want %v", err, ErrRecoveryResultCleanupConflict)
		}
	})

	t.Run("busy node", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
		if err != nil {
			t.Fatalf("publish busy cleanup fixture: %v", err)
		}
		fixture.nodeAdmission.err = task.ErrNodeWriteConflict
		if _, err := fixture.service.ClaimCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{
			ResultSetID: published.ResultSetID, WorkerID: "cleanup-busy-loser",
		}); !errors.Is(err, ErrRecoveryResultCleanupBusy) {
			t.Fatalf("busy cleanup claim error = %v, want %v", err, ErrRecoveryResultCleanupBusy)
		}
		var cleanupLeases int64
		if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).
			Where("job_id = ? AND holder_kind = ?", published.JobID, "recovery_cleanup").
			Count(&cleanupLeases).Error; err != nil {
			t.Fatalf("count busy cleanup leases: %v", err)
		}
		if cleanupLeases != 0 {
			t.Fatalf("busy cleanup claim persisted %d cleanup leases", cleanupLeases)
		}
	})
}

func TestRecoveryWorkspaceCleanupClaimLocksJobBeforeNodeLeaseAndCAS(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)
	before := fixture.prepareWorkspaceCleanupDue(t)
	priorMaxFence := fixture.maxNodeFence(t)

	events := make([]string, 0, 8)
	jobLocks := make([]bool, 0, 2)
	recording := true
	fixture.nodeAdmission.order = &events
	queryCallback := "task7:record_workspace_cleanup_claim_queries"
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
		if !recording || tx.Statement.Table != (model.BackupAssetRecoveryJob{}).TableName() {
			return
		}
		_, locked := tx.Statement.Clauses["FOR"]
		jobLocks = append(jobLocks, locked)
		if locked {
			events = append(events, "lock-job-workspace")
		} else {
			events = append(events, "query-job-candidate")
		}
	}); err != nil {
		t.Fatalf("register workspace cleanup query recorder: %v", err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Query().Remove(queryCallback) })
	createCallback := "task7:record_workspace_cleanup_claim_create"
	if err := fixture.db.Callback().Create().Before("gorm:create").Register(createCallback, func(tx *gorm.DB) {
		if recording && tx.Statement.Table == (model.BackupAssetRecoveryNodeLease{}).TableName() {
			events = append(events, "create-node-lease")
		}
	}); err != nil {
		t.Fatalf("register workspace cleanup create recorder: %v", err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Create().Remove(createCallback) })
	updateCallback := "task7:record_workspace_cleanup_claim_cas"
	if err := fixture.db.Callback().Update().Before("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
		if recording && tx.Statement.Table == (model.BackupAssetRecoveryJob{}).TableName() {
			events = append(events, "cas-job-workspace")
		}
	}); err != nil {
		t.Fatalf("register workspace cleanup CAS recorder: %v", err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(updateCallback) })

	claim, err := fixture.service.ClaimWorkspaceCleanup(context.Background(), ClaimRecoveryWorkspaceCleanupRequest{
		JobID: fixture.job.ID, WorkerID: "workspace-cleanup-worker-a",
	})
	recording = false
	if err != nil {
		t.Fatalf("claim unpublished workspace cleanup: %v", err)
	}
	if claim.JobID != fixture.job.ID || claim.WorkerID != "workspace-cleanup-worker-a" ||
		claim.CleanupFence != 1 || claim.CleanupAttempt != 1 || claim.NodeFence != priorMaxFence+1 ||
		claim.Phase != CleanupPhaseClaimed || !claim.LeaseExpiresAt.Equal(fixture.now.Add(recoveryResultCleanupTTL)) {
		t.Fatalf("unexpected workspace cleanup claim: %+v prior_node_fence=%d", claim, priorMaxFence)
	}
	if fixture.nodeAdmission.calls != 1 {
		t.Fatalf("workspace cleanup node admission calls = %d, want 1", fixture.nodeAdmission.calls)
	}
	if len(jobLocks) != 2 || jobLocks[0] || !jobLocks[1] {
		t.Fatalf("candidate/claim job lock observations = %v, want [false true]", jobLocks)
	}
	assertOrderedEvents(t, events,
		"query-job-candidate", "lock-job-workspace", "admit-node", "create-node-lease", "cas-job-workspace")

	var after model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", fixture.job.ID).Take(&after).Error; err != nil {
		t.Fatalf("load claimed workspace cleanup job: %v", err)
	}
	fixture.assertWorkspaceCleanupClaimRow(t, claim, after)
	if after.WorkspaceOwner != before.WorkspaceOwner || after.WorkspaceFence != before.WorkspaceFence ||
		after.WorkspaceMarkerValidationAttemptID != before.WorkspaceMarkerValidationAttemptID ||
		after.WorkspaceMarkerValidationAttemptFence != before.WorkspaceMarkerValidationAttemptFence ||
		after.WorkspaceMarkerValidationNodeFence != before.WorkspaceMarkerValidationNodeFence {
		t.Fatalf("workspace cleanup rewrote execution provenance: before=%+v after=%+v", before, after)
	}
	var resultSets int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).
		Where("job_id = ?", fixture.job.ID).Count(&resultSets).Error; err != nil {
		t.Fatalf("count unpublished workspace result sets: %v", err)
	}
	if resultSets != 0 {
		t.Fatalf("workspace cleanup candidate unexpectedly has %d ResultSets", resultSets)
	}
}

func TestRecoveryWorkspaceCleanupLostCASReleasesFreshNodeLeaseInTransaction(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)
	before := fixture.prepareWorkspaceCleanupDue(t)
	injected := false
	callbackName := "task7:lose_workspace_cleanup_job_cas"
	if err := fixture.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if !injected && tx.Statement.Table == (model.BackupAssetRecoveryJob{}).TableName() {
			injected = true
			tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "1 = 0"}}})
		}
	}); err != nil {
		t.Fatalf("register workspace cleanup CAS fault: %v", err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(callbackName) })

	_, err := fixture.service.ClaimWorkspaceCleanup(context.Background(), ClaimRecoveryWorkspaceCleanupRequest{
		JobID: fixture.job.ID, WorkerID: "workspace-cleanup-lost-cas",
	})
	if !errors.Is(err, ErrRecoveryResultCleanupConflict) {
		t.Fatalf("lost workspace cleanup CAS error = %v, want %v", err, ErrRecoveryResultCleanupConflict)
	}
	if !injected {
		t.Fatal("workspace cleanup job CAS fault did not execute")
	}

	var after model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", fixture.job.ID).Take(&after).Error; err != nil {
		t.Fatalf("load lost-CAS workspace job: %v", err)
	}
	if !sameRecoveryWorkspaceCleanupSnapshot(before, after) {
		t.Fatalf("lost workspace cleanup CAS changed job tuple: before=%+v after=%+v", before, after)
	}
	var leases []model.BackupAssetRecoveryNodeLease
	if err := fixture.db.Where("job_id = ? AND holder_kind = ?", fixture.job.ID, "recovery_cleanup").
		Find(&leases).Error; err != nil {
		t.Fatalf("load lost-CAS workspace cleanup leases: %v", err)
	}
	if len(leases) != 1 || leases[0].State != "released" || leases[0].ReleasedAt == nil ||
		leases[0].OwnerID != "workspace-cleanup-lost-cas" {
		t.Fatalf("lost workspace cleanup CAS lease disposition = %+v", leases)
	}
}

func TestRecoveryWorkspaceCleanupClaimRetriesFailureAndTakesOverExpiredOwner(t *testing.T) {
	t.Run("ownerless retry preserves durable phase", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		fixture.prepareWorkspaceCleanupDue(t)
		first, err := fixture.service.ClaimWorkspaceCleanup(context.Background(), ClaimRecoveryWorkspaceCleanupRequest{
			JobID: fixture.job.ID, WorkerID: "workspace-cleanup-first-owner",
		})
		if err != nil {
			t.Fatalf("claim first workspace cleanup owner: %v", err)
		}
		fixture.setWorkspaceCleanupPhase(t, CleanupPhaseRevoked)
		fixture.releaseWorkspaceCleanupForRetry(t, first)
		fixture.now = fixture.now.Add(time.Second)
		priorMaxFence := fixture.maxNodeFence(t)

		retry, err := fixture.service.ClaimWorkspaceCleanup(context.Background(), ClaimRecoveryWorkspaceCleanupRequest{
			JobID: fixture.job.ID, WorkerID: "workspace-cleanup-retry-owner",
		})
		if err != nil {
			t.Fatalf("retry workspace cleanup failure: %v", err)
		}
		if retry.CleanupFence != first.CleanupFence+1 || retry.CleanupAttempt != first.CleanupAttempt+1 ||
			retry.NodeFence != priorMaxFence+1 || retry.Phase != CleanupPhaseRevoked {
			t.Fatalf("unexpected workspace cleanup retry: first=%+v retry=%+v prior_node_fence=%d",
				first, retry, priorMaxFence)
		}
		fixture.assertWorkspaceCleanupClaim(t, retry)
	})

	t.Run("expired owner preserves durable phase", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		fixture.prepareWorkspaceCleanupDue(t)
		first, err := fixture.service.ClaimWorkspaceCleanup(context.Background(), ClaimRecoveryWorkspaceCleanupRequest{
			JobID: fixture.job.ID, WorkerID: "workspace-cleanup-expired-owner",
		})
		if err != nil {
			t.Fatalf("claim expiring workspace cleanup owner: %v", err)
		}
		fixture.setWorkspaceCleanupPhase(t, CleanupPhaseRevoked)
		fixture.setWorkspaceCleanupPhase(t, CleanupPhaseDrained)
		fixture.now = first.LeaseExpiresAt.Add(time.Second)
		priorMaxFence := fixture.maxNodeFence(t)

		takeover, err := fixture.service.ClaimWorkspaceCleanup(context.Background(), ClaimRecoveryWorkspaceCleanupRequest{
			JobID: fixture.job.ID, WorkerID: "workspace-cleanup-takeover-owner",
		})
		if err != nil {
			t.Fatalf("take over expired workspace cleanup owner: %v", err)
		}
		if takeover.CleanupFence != first.CleanupFence+1 ||
			takeover.CleanupAttempt != first.CleanupAttempt+1 || takeover.NodeFence != priorMaxFence+1 ||
			takeover.Phase != CleanupPhaseDrained {
			t.Fatalf("unexpected workspace cleanup takeover: first=%+v takeover=%+v prior_node_fence=%d",
				first, takeover, priorMaxFence)
		}
		fixture.assertWorkspaceCleanupClaim(t, takeover)
		var oldLease model.BackupAssetRecoveryNodeLease
		if err := fixture.db.Where("id = ?", first.NodeLeaseID).Take(&oldLease).Error; err != nil {
			t.Fatalf("load expired workspace cleanup node lease: %v", err)
		}
		if oldLease.State != "expired" || oldLease.ReleasedAt == nil {
			t.Fatalf("old workspace cleanup node lease was not expired: %+v", oldLease)
		}
	})
}

func TestRecoveryWorkspaceCleanupClaimRejectsActiveInvalidOrBusyCandidate(t *testing.T) {
	t.Run("active owner", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		fixture.prepareWorkspaceCleanupDue(t)
		if _, err := fixture.service.ClaimWorkspaceCleanup(context.Background(), ClaimRecoveryWorkspaceCleanupRequest{
			JobID: fixture.job.ID, WorkerID: "workspace-cleanup-active-owner",
		}); err != nil {
			t.Fatalf("claim active-owner fixture: %v", err)
		}
		if _, err := fixture.service.ClaimWorkspaceCleanup(context.Background(), ClaimRecoveryWorkspaceCleanupRequest{
			JobID: fixture.job.ID, WorkerID: "workspace-cleanup-active-loser",
		}); !errors.Is(err, ErrRecoveryResultCleanupConflict) {
			t.Fatalf("active workspace cleanup claim error = %v, want %v", err, ErrRecoveryResultCleanupConflict)
		}
		fixture.assertWorkspaceCleanupLeaseCount(t, 1)
	})

	tests := []struct {
		name   string
		mutate func(*recoveryResultLifecycleFixture)
	}{
		{name: "non cleanup phase", mutate: func(_ *recoveryResultLifecycleFixture) {}},
		{name: "in place", mutate: func(fixture *recoveryResultLifecycleFixture) {
			fixture.prepareWorkspaceCleanupDue(t)
			fixture.updateJob(t, map[string]any{"target_mode": string(TargetModeInPlace)})
		}},
		{name: "nonterminal", mutate: func(fixture *recoveryResultLifecycleFixture) {
			fixture.prepareWorkspaceCleanupDue(t)
			fixture.updateJob(t, map[string]any{"state": string(JobStateRunning)})
		}},
		{name: "published result set", mutate: func(fixture *recoveryResultLifecycleFixture) {
			if _, err := fixture.service.Publish(context.Background(), fixture.publishRequest()); err != nil {
				t.Fatalf("publish workspace cleanup rejection fixture: %v", err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryResultLifecycleFixture(t)
			test.mutate(fixture)
			if _, err := fixture.service.ClaimWorkspaceCleanup(context.Background(), ClaimRecoveryWorkspaceCleanupRequest{
				JobID: fixture.job.ID, WorkerID: "workspace-cleanup-invalid-worker",
			}); !errors.Is(err, ErrRecoveryResultCleanupConflict) {
				t.Fatalf("invalid workspace cleanup claim error = %v, want %v", err, ErrRecoveryResultCleanupConflict)
			}
			fixture.assertWorkspaceCleanupLeaseCount(t, 0)
		})
	}

	t.Run("busy node", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		fixture.prepareWorkspaceCleanupDue(t)
		fixture.nodeAdmission.err = task.ErrNodeWriteConflict
		if _, err := fixture.service.ClaimWorkspaceCleanup(context.Background(), ClaimRecoveryWorkspaceCleanupRequest{
			JobID: fixture.job.ID, WorkerID: "workspace-cleanup-busy-worker",
		}); !errors.Is(err, ErrRecoveryResultCleanupBusy) {
			t.Fatalf("busy workspace cleanup claim error = %v, want %v", err, ErrRecoveryResultCleanupBusy)
		}
		fixture.assertWorkspaceCleanupLeaseCount(t, 0)
	})
}

func TestRecoveryResultCleanupRevokeAndDrainRenewLeasesAndAdvanceDurablePhases(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)
	published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
	if err != nil {
		t.Fatalf("publish cleanup lifecycle fixture: %v", err)
	}
	claim, err := fixture.service.ClaimCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{
		ResultSetID: published.ResultSetID, WorkerID: "result-cleanup-phase-owner",
	})
	if err != nil {
		t.Fatalf("claim result cleanup lifecycle: %v", err)
	}

	fixture.now = fixture.now.Add(time.Minute)
	revoked, err := fixture.service.RevokeRecoveryResultCleanup(context.Background(), claim)
	if err != nil {
		t.Fatalf("revoke published recovery result cleanup: %v", err)
	}
	if revoked.Phase != CleanupPhaseRevoked || !revoked.LeaseExpiresAt.After(claim.LeaseExpiresAt) ||
		revoked.CleanupFence != claim.CleanupFence || revoked.NodeFence != claim.NodeFence {
		t.Fatalf("unexpected revoked cleanup claim: before=%+v after=%+v", claim, revoked)
	}
	fixture.assertCleanupClaimRow(t, revoked)
	fixture.assertCleanupNodeLease(t, revoked.NodeLeaseID, revoked.JobID, revoked.WorkerID,
		revoked.NodeFence, revoked.LeaseExpiresAt)
	contentLifecycle := fixture.contentLifecycle
	if strings.Join(contentLifecycle.order, ",") != "revoke,cancel" ||
		contentLifecycle.revokeJobID != claim.JobID ||
		contentLifecycle.revokeReason != content.RecoveryResultCleanupReason ||
		!contentLifecycle.revokeAt.Equal(fixture.now) || contentLifecycle.cancelJobID != claim.JobID ||
		contentLifecycle.cancelObservedPhase != CleanupPhaseRevoked ||
		!contentLifecycle.cancelObservedLease.Equal(revoked.LeaseExpiresAt) {
		t.Fatalf("published revoke Content boundary=%+v", contentLifecycle)
	}
	var result model.BackupAssetRecoveryResult
	if err := fixture.db.Where("job_id = ?", claim.JobID).First(&result).Error; err != nil {
		t.Fatalf("load transactionally revoked result marker: %v", err)
	}
	if result.ModifiedAt == nil || !result.ModifiedAt.Equal(fixture.now) {
		t.Fatalf("Content transaction marker modified_at=%v want=%s", result.ModifiedAt, fixture.now)
	}

	fixture.now = fixture.now.Add(time.Minute)
	contentLifecycle.drainHook = func() { fixture.now = fixture.now.Add(time.Minute) }
	drained, err := fixture.service.DrainRecoveryResultCleanup(context.Background(), revoked)
	if err != nil {
		t.Fatalf("drain published recovery result cleanup: %v", err)
	}
	if drained.Phase != CleanupPhaseDrained || !drained.LeaseExpiresAt.After(revoked.LeaseExpiresAt) ||
		!drained.LeaseExpiresAt.After(contentLifecycle.drainObservedLease) ||
		drained.CleanupFence != claim.CleanupFence || drained.NodeFence != claim.NodeFence {
		t.Fatalf("unexpected drained cleanup claim: revoked=%+v drained=%+v observed=%s",
			revoked, drained, contentLifecycle.drainObservedLease)
	}
	if strings.Join(contentLifecycle.order, ",") != "revoke,cancel,drain" ||
		contentLifecycle.drainJobID != claim.JobID ||
		contentLifecycle.drainObservedPhase != CleanupPhaseRevoked {
		t.Fatalf("published drain Content boundary=%+v", contentLifecycle)
	}
	fixture.assertCleanupClaimRow(t, drained)
	fixture.assertCleanupNodeLease(t, drained.NodeLeaseID, drained.JobID, drained.WorkerID,
		drained.NodeFence, drained.LeaseExpiresAt)
}

func TestRecoveryResultCleanupFailuresPreserveRetryableDurablePhase(t *testing.T) {
	t.Run("revoke rollback", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
		if err != nil {
			t.Fatalf("publish revoke rollback fixture: %v", err)
		}
		claim, err := fixture.service.ClaimCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{
			ResultSetID: published.ResultSetID, WorkerID: "result-cleanup-revoke-failure",
		})
		if err != nil {
			t.Fatalf("claim revoke rollback fixture: %v", err)
		}
		fixture.now = fixture.now.Add(time.Minute)
		revokeErr := errors.New("injected Content revoke failure")
		fixture.contentLifecycle.revokeErr = revokeErr
		current, err := fixture.service.RevokeRecoveryResultCleanup(context.Background(), claim)
		if !errors.Is(err, revokeErr) {
			t.Fatalf("revoke rollback error=%v want=%v", err, revokeErr)
		}
		if current != claim || strings.Join(fixture.contentLifecycle.order, ",") != "revoke" {
			t.Fatalf("revoke rollback claim=%+v calls=%v want original=%+v", current, fixture.contentLifecycle.order, claim)
		}
		fixture.assertCleanupClaimRow(t, claim)
		fixture.assertCleanupNodeLease(t, claim.NodeLeaseID, claim.JobID, claim.WorkerID,
			claim.NodeFence, claim.LeaseExpiresAt)
		var result model.BackupAssetRecoveryResult
		if err := fixture.db.Where("job_id = ?", claim.JobID).First(&result).Error; err != nil {
			t.Fatalf("load rolled-back Content marker: %v", err)
		}
		if result.ModifiedAt != nil {
			t.Fatalf("failed revoke committed Content transaction marker: %s", result.ModifiedAt)
		}
	})

	t.Run("drain failure remains revoked", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
		if err != nil {
			t.Fatalf("publish drain failure fixture: %v", err)
		}
		claim, err := fixture.service.ClaimCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{
			ResultSetID: published.ResultSetID, WorkerID: "result-cleanup-drain-failure",
		})
		if err != nil {
			t.Fatalf("claim drain failure fixture: %v", err)
		}
		fixture.now = fixture.now.Add(time.Minute)
		revoked, err := fixture.service.RevokeRecoveryResultCleanup(context.Background(), claim)
		if err != nil {
			t.Fatalf("revoke drain failure fixture: %v", err)
		}
		fixture.contentLifecycle.order = nil
		fixture.now = fixture.now.Add(time.Minute)
		drainErr := errors.New("injected Content drain failure")
		fixture.contentLifecycle.drainErr = drainErr
		current, err := fixture.service.DrainRecoveryResultCleanup(context.Background(), revoked)
		if !errors.Is(err, drainErr) {
			t.Fatalf("drain failure error=%v want=%v", err, drainErr)
		}
		if current.Phase != CleanupPhaseRevoked || !current.LeaseExpiresAt.After(revoked.LeaseExpiresAt) ||
			strings.Join(fixture.contentLifecycle.order, ",") != "drain" {
			t.Fatalf("drain failure claim=%+v calls=%v revoked=%+v", current, fixture.contentLifecycle.order, revoked)
		}
		fixture.assertCleanupClaimRow(t, current)
		fixture.assertCleanupNodeLease(t, current.NodeLeaseID, current.JobID, current.WorkerID,
			current.NodeFence, current.LeaseExpiresAt)
	})
}

func TestRecoveryResultCleanupRejectsExpiredOrReplacedFenceBeforeContent(t *testing.T) {
	tests := []struct {
		name   string
		drain  bool
		mutate func(*recoveryResultLifecycleFixture, *RecoveryResultCleanupClaim)
	}{
		{name: "stale cleanup fence", mutate: func(_ *recoveryResultLifecycleFixture, claim *RecoveryResultCleanupClaim) {
			claim.CleanupFence++
		}},
		{name: "stale node fence", mutate: func(_ *recoveryResultLifecycleFixture, claim *RecoveryResultCleanupClaim) {
			claim.NodeFence++
		}},
		{name: "expired owner", mutate: func(fixture *recoveryResultLifecycleFixture, claim *RecoveryResultCleanupClaim) {
			fixture.now = claim.LeaseExpiresAt
		}},
		{name: "drain stale node fence", drain: true, mutate: func(_ *recoveryResultLifecycleFixture, claim *RecoveryResultCleanupClaim) {
			claim.NodeFence++
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryResultLifecycleFixture(t)
			published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
			if err != nil {
				t.Fatalf("publish stale fence fixture: %v", err)
			}
			claim, err := fixture.service.ClaimCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{
				ResultSetID: published.ResultSetID, WorkerID: "result-cleanup-stale-fence",
			})
			if err != nil {
				t.Fatalf("claim stale fence fixture: %v", err)
			}
			persisted := claim
			if test.drain {
				fixture.now = fixture.now.Add(time.Minute)
				claim, err = fixture.service.RevokeRecoveryResultCleanup(context.Background(), claim)
				if err != nil {
					t.Fatalf("revoke stale drain fixture: %v", err)
				}
				persisted = claim
			}
			fixture.contentLifecycle.order = nil
			test.mutate(fixture, &claim)
			if test.drain {
				_, err = fixture.service.DrainRecoveryResultCleanup(context.Background(), claim)
			} else {
				_, err = fixture.service.RevokeRecoveryResultCleanup(context.Background(), claim)
			}
			if !errors.Is(err, ErrRecoveryResultCleanupConflict) {
				t.Fatalf("stale cleanup lifecycle error=%v want=%v", err, ErrRecoveryResultCleanupConflict)
			}
			if len(fixture.contentLifecycle.order) != 0 {
				t.Fatalf("stale cleanup fence crossed Content boundary: %v", fixture.contentLifecycle.order)
			}
			fixture.assertCleanupClaimRow(t, persisted)
		})
	}
}

func TestRecoveryResultCleanupLostFenceAfterExternalDrainDoesNotAdvancePhase(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)
	published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
	if err != nil {
		t.Fatalf("publish lost-fence drain fixture: %v", err)
	}
	claim, err := fixture.service.ClaimCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{
		ResultSetID: published.ResultSetID, WorkerID: "result-cleanup-old-drainer",
	})
	if err != nil {
		t.Fatalf("claim lost-fence drain fixture: %v", err)
	}
	fixture.now = fixture.now.Add(time.Minute)
	revoked, err := fixture.service.RevokeRecoveryResultCleanup(context.Background(), claim)
	if err != nil {
		t.Fatalf("revoke lost-fence drain fixture: %v", err)
	}
	fixture.contentLifecycle.order = nil
	var takeover RecoveryResultCleanupClaim
	var takeoverErr error
	fixture.contentLifecycle.drainHook = func() {
		var resultSet model.BackupAssetRecoveryResultSet
		if err := fixture.db.Where("id = ?", revoked.ResultSetID).Take(&resultSet).Error; err != nil {
			takeoverErr = err
			return
		}
		fixture.now = resultSet.CleanupLeaseExpiresAt.Add(time.Second)
		takeover, takeoverErr = fixture.service.ClaimCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{
			ResultSetID: revoked.ResultSetID, WorkerID: "result-cleanup-fresh-drainer",
		})
	}
	current, err := fixture.service.DrainRecoveryResultCleanup(context.Background(), revoked)
	if !errors.Is(err, ErrRecoveryResultCleanupConflict) {
		t.Fatalf("lost-fence post-drain error=%v want=%v", err, ErrRecoveryResultCleanupConflict)
	}
	if takeoverErr != nil {
		t.Fatalf("take over during external drain: %v", takeoverErr)
	}
	if current.Phase != CleanupPhaseRevoked || takeover.Phase != CleanupPhaseRevoked ||
		takeover.CleanupFence <= revoked.CleanupFence || takeover.NodeFence <= revoked.NodeFence ||
		strings.Join(fixture.contentLifecycle.order, ",") != "drain" {
		t.Fatalf("lost-fence drain current=%+v takeover=%+v calls=%v", current, takeover, fixture.contentLifecycle.order)
	}
	fixture.assertCleanupClaimRow(t, takeover)
}

func TestRecoveryWorkspaceCleanupRevokeAndDrainRenewsWithoutContent(t *testing.T) {
	fixture := newRecoveryResultLifecycleFixture(t)
	before := fixture.prepareWorkspaceCleanupDue(t)
	claim, err := fixture.service.ClaimWorkspaceCleanup(context.Background(), ClaimRecoveryWorkspaceCleanupRequest{
		JobID: fixture.job.ID, WorkerID: "workspace-cleanup-phase-owner",
	})
	if err != nil {
		t.Fatalf("claim workspace cleanup lifecycle: %v", err)
	}
	fixture.now = fixture.now.Add(time.Minute)
	revoked, err := fixture.service.RevokeRecoveryWorkspaceCleanup(context.Background(), claim)
	if err != nil {
		t.Fatalf("revoke workspace cleanup lifecycle: %v", err)
	}
	fixture.now = fixture.now.Add(time.Minute)
	drained, err := fixture.service.DrainRecoveryWorkspaceCleanup(context.Background(), revoked)
	if err != nil {
		t.Fatalf("drain workspace cleanup lifecycle: %v", err)
	}
	if revoked.Phase != CleanupPhaseRevoked || drained.Phase != CleanupPhaseDrained ||
		!revoked.LeaseExpiresAt.After(claim.LeaseExpiresAt) ||
		!drained.LeaseExpiresAt.After(revoked.LeaseExpiresAt) || len(fixture.contentLifecycle.order) != 0 {
		t.Fatalf("workspace cleanup phases claim=%+v revoked=%+v drained=%+v Content=%v",
			claim, revoked, drained, fixture.contentLifecycle.order)
	}
	fixture.assertWorkspaceCleanupClaim(t, drained)
	fixture.assertCleanupNodeLease(t, drained.NodeLeaseID, drained.JobID, drained.WorkerID,
		drained.NodeFence, drained.LeaseExpiresAt)
	var after model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&after).Error; err != nil {
		t.Fatalf("load drained workspace cleanup job: %v", err)
	}
	if after.WorkspaceOwner != before.WorkspaceOwner || after.WorkspaceFence != before.WorkspaceFence ||
		after.WorkspaceMarkerValidationAttemptID != before.WorkspaceMarkerValidationAttemptID ||
		after.WorkspaceMarkerValidationAttemptFence != before.WorkspaceMarkerValidationAttemptFence ||
		after.WorkspaceMarkerValidationNodeFence != before.WorkspaceMarkerValidationNodeFence {
		t.Fatalf("workspace cleanup rewrote execution provenance: before=%+v after=%+v", before, after)
	}
}

func TestRecoveryCleanupTargetValidationRenewsAndAdvancesPublishedAndWorkspace(t *testing.T) {
	t.Run("published result set", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		published, drained, before := fixture.preparePublishedCleanupDrained(t, "result-validation-owner")
		contentCalls := len(fixture.contentLifecycle.order)
		events := make([]string, 0, 12)
		fixture.target.order = &events
		fixture.recordCleanupValidationLocks(t, &events, true)

		fixture.now = fixture.now.Add(time.Minute)
		validated, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
		if err != nil {
			t.Fatalf("validate published cleanup target: %v", err)
		}
		if validated.ResultSetID != published.ResultSetID || validated.JobID != published.JobID ||
			validated.WorkerID != drained.WorkerID || validated.CleanupFence != drained.CleanupFence ||
			validated.CleanupAttempt != drained.CleanupAttempt || validated.NodeLeaseID != drained.NodeLeaseID ||
			validated.NodeFence != drained.NodeFence || validated.Phase != CleanupPhaseValidated ||
			!validated.LeaseExpiresAt.After(fixture.target.observedLeaseExpiry) ||
			!fixture.target.observedLeaseExpiry.After(drained.LeaseExpiresAt) {
			t.Fatalf("published validation claim=%+v drained=%+v observed_expiry=%s",
				validated, drained, fixture.target.observedLeaseExpiry)
		}
		if fixture.target.calls != 1 || fixture.target.removeCalls != 0 ||
			fixture.target.observedPhase != CleanupPhaseDrained || len(fixture.target.permits) != 1 ||
			len(fixture.target.requests) != 1 {
			t.Fatalf("published target calls=%d remove=%d phase=%s permits=%d requests=%d",
				fixture.target.calls, fixture.target.removeCalls, fixture.target.observedPhase,
				len(fixture.target.permits), len(fixture.target.requests))
		}
		permit := fixture.target.permits[0]
		request := fixture.target.requests[0]
		pathDigest, err := TargetPathDigest(
			fixture.job.TargetRootID, fixture.job.RootLocatorDigest, fixture.job.EncryptedWorkspaceRelativeLocator,
		)
		if err != nil {
			t.Fatalf("derive published cleanup path digest: %v", err)
		}
		if permit.ResourceKind != CleanupResourceResultSet || permit.ResourceID != published.ResultSetID ||
			permit.JobID != published.JobID || permit.Operation != TargetCleanupValidateOwnedJobDir ||
			permit.CleanupOwner != drained.WorkerID || permit.CleanupFence != drained.CleanupFence ||
			permit.CleanupAttempt != drained.CleanupAttempt || permit.NodeLeaseID != drained.NodeLeaseID ||
			permit.NodeFence != drained.NodeFence || permit.TargetPathDigest != pathDigest ||
			request.Object.TargetPathDigest != pathDigest ||
			request.MarkerBindingDigest != fixture.job.WorkspaceMarkerBindingDigest ||
			permit.MarkerCreatorID != fixture.job.WorkspaceOwner ||
			permit.MarkerCreatorFence != fixture.job.WorkspaceFence ||
			request.MarkerCreatorID != fixture.job.WorkspaceOwner ||
			request.MarkerCreatorFence != fixture.job.WorkspaceFence ||
			!permit.ExpiresAt.Equal(fixture.target.observedLeaseExpiry) ||
			!fixture.target.observedContextExpiry.After(fixture.now) ||
			!fixture.target.observedContextExpiry.Before(permit.ExpiresAt) {
			t.Fatalf("published cleanup permit=%+v request=%+v context_deadline=%s",
				permit, request, fixture.target.observedContextExpiry)
		}
		fixture.assertCleanupClaimRow(t, validated)
		fixture.assertCleanupNodeLease(t, validated.NodeLeaseID, validated.JobID, validated.WorkerID,
			validated.NodeFence, validated.LeaseExpiresAt)
		var after model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", validated.JobID).Take(&after).Error; err != nil {
			t.Fatalf("load validated published cleanup job: %v", err)
		}
		assertCleanupExecutionProvenanceUnchanged(t, before, after)
		assertOrderedEvents(t, events, "job", "node", "result", "target", "job", "node", "result")
		if len(fixture.contentLifecycle.order) != contentCalls {
			t.Fatalf("published validation repeated Content lifecycle: %v", fixture.contentLifecycle.order)
		}
	})

	t.Run("unpublished workspace", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		drained, before := fixture.prepareWorkspaceCleanupDrained(t, "workspace-validation-owner")
		contentCalls := len(fixture.contentLifecycle.order)
		events := make([]string, 0, 10)
		fixture.target.order = &events
		fixture.recordCleanupValidationLocks(t, &events, false)

		fixture.now = fixture.now.Add(time.Minute)
		validated, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained)
		if err != nil {
			t.Fatalf("validate unpublished cleanup target: %v", err)
		}
		if validated.JobID != drained.JobID || validated.WorkerID != drained.WorkerID ||
			validated.CleanupFence != drained.CleanupFence || validated.CleanupAttempt != drained.CleanupAttempt ||
			validated.NodeLeaseID != drained.NodeLeaseID || validated.NodeFence != drained.NodeFence ||
			validated.Phase != CleanupPhaseValidated ||
			!validated.LeaseExpiresAt.After(fixture.target.observedLeaseExpiry) ||
			!fixture.target.observedLeaseExpiry.After(drained.LeaseExpiresAt) {
			t.Fatalf("workspace validation claim=%+v drained=%+v observed_expiry=%s",
				validated, drained, fixture.target.observedLeaseExpiry)
		}
		if fixture.target.calls != 1 || fixture.target.removeCalls != 0 ||
			fixture.target.observedPhase != CleanupPhaseDrained || len(fixture.target.permits) != 1 {
			t.Fatalf("workspace target calls=%d remove=%d phase=%s permits=%d",
				fixture.target.calls, fixture.target.removeCalls, fixture.target.observedPhase,
				len(fixture.target.permits))
		}
		permit := fixture.target.permits[0]
		if permit.ResourceKind != CleanupResourceWorkspace || permit.ResourceID != drained.JobID ||
			permit.JobID != drained.JobID || permit.CleanupOwner != drained.WorkerID ||
			permit.CleanupFence != drained.CleanupFence || permit.CleanupAttempt != drained.CleanupAttempt ||
			permit.NodeLeaseID != drained.NodeLeaseID || permit.NodeFence != drained.NodeFence ||
			permit.MarkerCreatorID != fixture.job.WorkspaceOwner ||
			permit.MarkerCreatorFence != fixture.job.WorkspaceFence || len(fixture.target.requests) != 1 ||
			fixture.target.requests[0].MarkerCreatorID != fixture.job.WorkspaceOwner ||
			fixture.target.requests[0].MarkerCreatorFence != fixture.job.WorkspaceFence ||
			!permit.ExpiresAt.Equal(fixture.target.observedLeaseExpiry) ||
			!fixture.target.observedContextExpiry.After(fixture.now) ||
			!fixture.target.observedContextExpiry.Before(permit.ExpiresAt) {
			t.Fatalf("workspace cleanup permit=%+v context_deadline=%s", permit, fixture.target.observedContextExpiry)
		}
		fixture.assertWorkspaceCleanupClaim(t, validated)
		fixture.assertCleanupNodeLease(t, validated.NodeLeaseID, validated.JobID, validated.WorkerID,
			validated.NodeFence, validated.LeaseExpiresAt)
		var after model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", validated.JobID).Take(&after).Error; err != nil {
			t.Fatalf("load validated workspace cleanup job: %v", err)
		}
		assertCleanupExecutionProvenanceUnchanged(t, before, after)
		assertOrderedEvents(t, events, "job", "node", "target", "job", "node")
		if len(fixture.contentLifecycle.order) != contentCalls {
			t.Fatalf("workspace validation crossed Content lifecycle: %v", fixture.contentLifecycle.order)
		}
	})
}

func TestResultLifecycleAdvanceCleanupTransitionsValidatedToDeleteStarted(t *testing.T) {
	t.Run("published result set", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		published, drained, before := fixture.preparePublishedCleanupDrained(t, "result-delete-start-owner")
		fixture.now = fixture.now.Add(time.Minute)
		validated, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
		if err != nil {
			t.Fatalf("validate published cleanup before delete start: %v", err)
		}
		fixture.target.removal = OwnedJobDirRemoval{
			Complete: false, ProgressDigest: framedDigest("xirang/recovery/r55-progress/v1", published.JobID),
		}

		fixture.now = fixture.now.Add(time.Minute)
		progress, err := fixture.service.AdvanceRecoveryResultCleanup(context.Background(), validated)
		if err != nil {
			t.Fatalf("advance published cleanup to delete_started: %v", err)
		}
		if progress.Phase != CleanupPhaseDeleteStarted || progress.Complete || progress.RemovedEntries != 0 ||
			progress.ProgressDigest != fixture.target.removal.ProgressDigest {
			t.Fatalf("published cleanup progress=%+v want delete_started incomplete", progress)
		}
		if fixture.target.removeCalls != 1 || len(fixture.target.removePermits) != 1 ||
			len(fixture.target.removeRequests) != 1 {
			t.Fatalf("published remove calls=%d permits=%d requests=%d",
				fixture.target.removeCalls, len(fixture.target.removePermits), len(fixture.target.removeRequests))
		}
		permit := fixture.target.removePermits[0]
		request := fixture.target.removeRequests[0]
		if permit.ResourceKind != CleanupResourceResultSet || permit.ResourceID != published.ResultSetID ||
			permit.JobID != published.JobID || permit.Operation != TargetCleanupRemoveOwnedJobDir ||
			permit.CleanupOwner != validated.WorkerID || permit.CleanupFence != validated.CleanupFence ||
			permit.CleanupAttempt != validated.CleanupAttempt || permit.NodeLeaseID != validated.NodeLeaseID ||
			permit.NodeFence != validated.NodeFence || !permit.ExpiresAt.After(validated.LeaseExpiresAt) ||
			permit.proof == nil || permit.proof.validateLive == nil || request.Object.PrivateRelativeLocator == "" ||
			request.MarkerBindingDigest != before.WorkspaceMarkerBindingDigest {
			t.Fatalf("published delete-start permit=%+v request=%+v", permit, request)
		}
		expected := validated
		expected.Phase = CleanupPhaseDeleteStarted
		expected.LeaseExpiresAt = permit.ExpiresAt.Add(time.Second)
		fixture.assertCleanupClaimRow(t, expected)
		fixture.assertCleanupNodeLease(t, expected.NodeLeaseID, expected.JobID, expected.WorkerID,
			expected.NodeFence, expected.LeaseExpiresAt)
		var after model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", expected.JobID).Take(&after).Error; err != nil {
			t.Fatalf("load published delete-start job: %v", err)
		}
		assertCleanupExecutionProvenanceUnchanged(t, before, after)
	})

	t.Run("unpublished workspace", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		drained, before := fixture.prepareWorkspaceCleanupDrained(t, "workspace-delete-start-owner")
		fixture.now = fixture.now.Add(time.Minute)
		validated, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained)
		if err != nil {
			t.Fatalf("validate workspace cleanup before delete start: %v", err)
		}
		fixture.target.removal = OwnedJobDirRemoval{
			Complete: false, ProgressDigest: framedDigest("xirang/recovery/r55-progress/v1", validated.JobID),
		}

		fixture.now = fixture.now.Add(time.Minute)
		progress, err := fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), validated)
		if err != nil {
			t.Fatalf("advance workspace cleanup to delete_started: %v", err)
		}
		if progress.Phase != CleanupPhaseDeleteStarted || progress.Complete || progress.RemovedEntries != 0 ||
			progress.ProgressDigest != fixture.target.removal.ProgressDigest {
			t.Fatalf("workspace cleanup progress=%+v want delete_started incomplete", progress)
		}
		if fixture.target.removeCalls != 1 || len(fixture.target.removePermits) != 1 ||
			len(fixture.target.removeRequests) != 1 {
			t.Fatalf("workspace remove calls=%d permits=%d requests=%d",
				fixture.target.removeCalls, len(fixture.target.removePermits), len(fixture.target.removeRequests))
		}
		permit := fixture.target.removePermits[0]
		request := fixture.target.removeRequests[0]
		if permit.ResourceKind != CleanupResourceWorkspace || permit.ResourceID != validated.JobID ||
			permit.JobID != validated.JobID || permit.Operation != TargetCleanupRemoveOwnedJobDir ||
			permit.CleanupOwner != validated.WorkerID || permit.CleanupFence != validated.CleanupFence ||
			permit.CleanupAttempt != validated.CleanupAttempt || permit.NodeLeaseID != validated.NodeLeaseID ||
			permit.NodeFence != validated.NodeFence || !permit.ExpiresAt.After(validated.LeaseExpiresAt) ||
			permit.proof == nil || permit.proof.validateLive == nil || request.Object.PrivateRelativeLocator == "" ||
			request.MarkerBindingDigest != before.WorkspaceMarkerBindingDigest {
			t.Fatalf("workspace delete-start permit=%+v request=%+v", permit, request)
		}
		expected := validated
		expected.Phase = CleanupPhaseDeleteStarted
		expected.LeaseExpiresAt = permit.ExpiresAt.Add(time.Second)
		fixture.assertWorkspaceCleanupClaim(t, expected)
		fixture.assertCleanupNodeLease(t, expected.NodeLeaseID, expected.JobID, expected.WorkerID,
			expected.NodeFence, expected.LeaseExpiresAt)
		var after model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", expected.JobID).Take(&after).Error; err != nil {
			t.Fatalf("load workspace delete-start job: %v", err)
		}
		assertCleanupExecutionProvenanceUnchanged(t, before, after)
	})
}

func TestResultLifecycleR60CleanupAdvanceTransactionFailureRollsBack(t *testing.T) {
	tests := []struct {
		name      string
		published bool
	}{
		{name: "published result set", published: true},
		{name: "unpublished workspace", published: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryResultLifecycleFixture(t)
			var (
				validatedResult    RecoveryResultCleanupClaim
				validatedWorkspace RecoveryWorkspaceCleanupClaim
			)
			if test.published {
				_, drained, _ := fixture.preparePublishedCleanupDrained(t, "r60-advance-tx-owner")
				fixture.now = fixture.now.Add(time.Minute)
				var err error
				validatedResult, err = fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
				if err != nil {
					t.Fatalf("validate published cleanup: %v", err)
				}
			} else {
				drained, _ := fixture.prepareWorkspaceCleanupDrained(t, "r60-advance-tx-owner")
				fixture.now = fixture.now.Add(time.Minute)
				var err error
				validatedWorkspace, err = fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained)
				if err != nil {
					t.Fatalf("validate workspace cleanup: %v", err)
				}
			}

			rawErr := errors.New("RAW_R60_LIFECYCLE_UPDATE_FOR_TEST_ONLY")
			injected := false
			callbackName := "task7:r60_fail_advance_cleanup_update_" + strings.ReplaceAll(test.name, " ", "_")
			if err := fixture.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
				if !injected && fixture.target.removeCalls > 0 {
					injected = true
					_ = tx.AddError(rawErr)
				}
			}); err != nil {
				t.Fatalf("register R60 advance update callback: %v", err)
			}
			t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(callbackName) })
			if test.published {
				fixture.target.removal = OwnedJobDirRemoval{
					Complete: false, RemovedEntries: 1,
					ProgressDigest: framedDigest("xirang/recovery/r60-advance-progress/v1", validatedResult.JobID),
				}
				fixture.now = fixture.now.Add(time.Minute)
				_, err := fixture.service.AdvanceRecoveryResultCleanup(context.Background(), validatedResult)
				if !errors.Is(err, ErrRecoveryResultCleanupConflict) {
					t.Fatalf("published advance transaction error=%v, want cleanup conflict", err)
				}
				if !injected || strings.Contains(err.Error(), rawErr.Error()) {
					t.Fatalf("published injected=%t error=%v, want sanitized transaction failure", injected, err)
				}
				var resultSet model.BackupAssetRecoveryResultSet
				if loadErr := fixture.db.Where("id = ? AND job_id = ?", validatedResult.ResultSetID, validatedResult.JobID).
					Take(&resultSet).Error; loadErr != nil {
					t.Fatalf("load published failed advance state: %v", loadErr)
				}
				if CleanupPhase(resultSet.CleanupPhase) != CleanupPhaseDeleteStarted ||
					resultSet.CleanupOwner != validatedResult.WorkerID ||
					resultSet.CleanupFence != validatedResult.CleanupFence ||
					resultSet.CleanupAttempt != validatedResult.CleanupAttempt ||
					resultSet.NodeLeaseID == nil || *resultSet.NodeLeaseID != validatedResult.NodeLeaseID ||
					resultSet.NodeFence != validatedResult.NodeFence || resultSet.State != string(ResultSetStateRevoking) {
					t.Fatalf("published failed advance state=%+v, want delete_started current owner", resultSet)
				}
				if resultSet.CleanupLeaseExpiresAt == nil {
					t.Fatal("published failed advance cleanup lease is nil")
				}
				fixture.assertCleanupNodeLease(t, validatedResult.NodeLeaseID, validatedResult.JobID,
					validatedResult.WorkerID, validatedResult.NodeFence, resultSet.CleanupLeaseExpiresAt.UTC())
			} else {
				fixture.target.removal = OwnedJobDirRemoval{
					Complete: false, RemovedEntries: 1,
					ProgressDigest: framedDigest("xirang/recovery/r60-advance-progress/v1", validatedWorkspace.JobID),
				}
				fixture.now = fixture.now.Add(time.Minute)
				_, err := fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), validatedWorkspace)
				if !errors.Is(err, ErrRecoveryResultCleanupConflict) {
					t.Fatalf("workspace advance transaction error=%v, want cleanup conflict", err)
				}
				if !injected || strings.Contains(err.Error(), rawErr.Error()) {
					t.Fatalf("workspace injected=%t error=%v, want sanitized transaction failure", injected, err)
				}
				var job model.BackupAssetRecoveryJob
				if loadErr := fixture.db.Where("id = ?", validatedWorkspace.JobID).Take(&job).Error; loadErr != nil {
					t.Fatalf("load workspace failed advance state: %v", loadErr)
				}
				if CleanupPhase(job.WorkspaceCleanupPhase) != CleanupPhaseDeleteStarted ||
					job.WorkspaceCleanupOwner != validatedWorkspace.WorkerID ||
					job.WorkspaceCleanupFence != validatedWorkspace.CleanupFence ||
					job.WorkspaceCleanupAttempt != validatedWorkspace.CleanupAttempt ||
					job.WorkspaceCleanupNodeLeaseID == nil || *job.WorkspaceCleanupNodeLeaseID != validatedWorkspace.NodeLeaseID ||
					job.WorkspaceCleanupNodeFence != validatedWorkspace.NodeFence {
					t.Fatalf("workspace failed advance state=%+v, want delete_started current owner", job)
				}
				if job.WorkspaceCleanupLeaseExpiresAt == nil {
					t.Fatal("workspace failed advance cleanup lease is nil")
				}
				fixture.assertCleanupNodeLease(t, validatedWorkspace.NodeLeaseID, validatedWorkspace.JobID,
					validatedWorkspace.WorkerID, validatedWorkspace.NodeFence, job.WorkspaceCleanupLeaseExpiresAt.UTC())
			}
		})
	}
}

func TestResultLifecycleR60DeleteStartedBarrierFailurePreventsTarget(t *testing.T) {
	tests := []struct {
		name      string
		published bool
	}{
		{name: "published result set", published: true},
		{name: "unpublished workspace", published: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryResultLifecycleFixture(t)
			var (
				validatedResult    RecoveryResultCleanupClaim
				validatedWorkspace RecoveryWorkspaceCleanupClaim
			)
			if test.published {
				_, drained, _ := fixture.preparePublishedCleanupDrained(t, "r60-barrier-owner")
				fixture.now = fixture.now.Add(time.Minute)
				var err error
				validatedResult, err = fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
				if err != nil {
					t.Fatalf("validate published cleanup: %v", err)
				}
			} else {
				drained, _ := fixture.prepareWorkspaceCleanupDrained(t, "r60-barrier-owner")
				fixture.now = fixture.now.Add(time.Minute)
				var err error
				validatedWorkspace, err = fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained)
				if err != nil {
					t.Fatalf("validate workspace cleanup: %v", err)
				}
			}

			rawErr := errors.New("RAW_R60_DELETE_STARTED_BARRIER_FOR_TEST_ONLY")
			injected := false
			callbackName := "task7:r60_fail_delete_started_barrier_" + strings.ReplaceAll(test.name, " ", "_")
			if err := fixture.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
				if !injected {
					injected = true
					_ = tx.AddError(rawErr)
				}
			}); err != nil {
				t.Fatalf("register R60 delete-started callback: %v", err)
			}
			t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(callbackName) })
			fixture.target.removal = OwnedJobDirRemoval{
				Complete: false, ProgressDigest: framedDigest("xirang/recovery/r60-barrier-progress/v1", test.name),
			}
			fixture.now = fixture.now.Add(time.Minute)
			if test.published {
				_, err := fixture.service.AdvanceRecoveryResultCleanup(context.Background(), validatedResult)
				if !errors.Is(err, ErrRecoveryResultCleanupConflict) || strings.Contains(err.Error(), rawErr.Error()) {
					t.Fatalf("published barrier error=%v, want sanitized cleanup conflict", err)
				}
				fixture.assertCleanupClaimRow(t, validatedResult)
				fixture.assertCleanupNodeLease(t, validatedResult.NodeLeaseID, validatedResult.JobID,
					validatedResult.WorkerID, validatedResult.NodeFence, validatedResult.LeaseExpiresAt)
			} else {
				_, err := fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), validatedWorkspace)
				if !errors.Is(err, ErrRecoveryResultCleanupConflict) || strings.Contains(err.Error(), rawErr.Error()) {
					t.Fatalf("workspace barrier error=%v, want sanitized cleanup conflict", err)
				}
				fixture.assertWorkspaceCleanupClaim(t, validatedWorkspace)
				fixture.assertCleanupNodeLease(t, validatedWorkspace.NodeLeaseID, validatedWorkspace.JobID,
					validatedWorkspace.WorkerID, validatedWorkspace.NodeFence, validatedWorkspace.LeaseExpiresAt)
			}
			if !injected || fixture.target.removeCalls != 0 {
				t.Fatalf("barrier injected=%t target remove calls=%d, want no target access", injected, fixture.target.removeCalls)
			}
		})
	}
}

func TestResultLifecycleAdvanceCleanupPersistsIncompleteProgress(t *testing.T) {
	t.Run("published result set", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		published, drained, before := fixture.preparePublishedCleanupDrained(t, "result-progress-owner")
		fixture.now = fixture.now.Add(time.Minute)
		validated, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
		if err != nil {
			t.Fatalf("validate published cleanup before incomplete progress: %v", err)
		}
		fixture.target.plannedMutations = 3
		fixture.target.removal = OwnedJobDirRemoval{
			Complete: false, RemovedEntries: 3,
			ProgressDigest: framedDigest("xirang/recovery/r58-progress/v1", published.JobID),
		}
		fixture.target.beforeRemoveMutation = func(
			mutation int,
			_ TargetCleanupPermit,
			_ RemoveOwnedJobDirRequest,
		) {
			if mutation == 0 {
				fixture.now = fixture.now.Add(time.Minute)
			}
		}

		fixture.now = fixture.now.Add(time.Minute)
		progress, err := fixture.service.AdvanceRecoveryResultCleanup(context.Background(), validated)
		if err != nil {
			t.Fatalf("persist published incomplete cleanup progress: %v", err)
		}
		if progress.Phase != CleanupPhaseDeleteStarted || progress.Complete || progress.RemovedEntries != 3 ||
			progress.ProgressDigest != fixture.target.removal.ProgressDigest || !validDigest(progress.ProgressDigest) {
			t.Fatalf("published incomplete cleanup progress=%+v", progress)
		}
		if fixture.target.removeCalls != 1 || fixture.target.removeMutations != 3 ||
			len(fixture.target.removePermits) != 1 {
			t.Fatalf("published incomplete remove calls=%d mutations=%d permits=%d",
				fixture.target.removeCalls, fixture.target.removeMutations, len(fixture.target.removePermits))
		}
		expected := validated
		expected.Phase = CleanupPhaseDeleteStarted
		expected.LeaseExpiresAt = fixture.target.removePermits[0].ExpiresAt.Add(time.Minute)
		fixture.assertCleanupClaimRow(t, expected)
		fixture.assertCleanupNodeLease(t, expected.NodeLeaseID, expected.JobID, expected.WorkerID,
			expected.NodeFence, expected.LeaseExpiresAt)
		var resultSet model.BackupAssetRecoveryResultSet
		if err := fixture.db.Where("id = ? AND job_id = ?", expected.ResultSetID, expected.JobID).
			Take(&resultSet).Error; err != nil {
			t.Fatalf("load published incomplete cleanup state: %v", err)
		}
		if ResultSetState(resultSet.State) != ResultSetStateRevoking ||
			CleanupPhase(resultSet.CleanupPhase) != CleanupPhaseDeleteStarted {
			t.Fatalf("published incomplete cleanup state=%s phase=%s", resultSet.State, resultSet.CleanupPhase)
		}
		var after model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", expected.JobID).Take(&after).Error; err != nil {
			t.Fatalf("load published incomplete cleanup job: %v", err)
		}
		assertCleanupExecutionProvenanceUnchanged(t, before, after)
	})

	t.Run("unpublished workspace", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		drained, before := fixture.prepareWorkspaceCleanupDrained(t, "workspace-progress-owner")
		fixture.now = fixture.now.Add(time.Minute)
		validated, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained)
		if err != nil {
			t.Fatalf("validate workspace cleanup before incomplete progress: %v", err)
		}
		fixture.target.plannedMutations = 3
		fixture.target.removal = OwnedJobDirRemoval{
			Complete: false, RemovedEntries: 3,
			ProgressDigest: framedDigest("xirang/recovery/r58-progress/v1", validated.JobID),
		}
		fixture.target.beforeRemoveMutation = func(
			mutation int,
			_ TargetCleanupPermit,
			_ RemoveOwnedJobDirRequest,
		) {
			if mutation == 0 {
				fixture.now = fixture.now.Add(time.Minute)
			}
		}

		fixture.now = fixture.now.Add(time.Minute)
		progress, err := fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), validated)
		if err != nil {
			t.Fatalf("persist workspace incomplete cleanup progress: %v", err)
		}
		if progress.Phase != CleanupPhaseDeleteStarted || progress.Complete || progress.RemovedEntries != 3 ||
			progress.ProgressDigest != fixture.target.removal.ProgressDigest || !validDigest(progress.ProgressDigest) {
			t.Fatalf("workspace incomplete cleanup progress=%+v", progress)
		}
		if fixture.target.removeCalls != 1 || fixture.target.removeMutations != 3 ||
			len(fixture.target.removePermits) != 1 {
			t.Fatalf("workspace incomplete remove calls=%d mutations=%d permits=%d",
				fixture.target.removeCalls, fixture.target.removeMutations, len(fixture.target.removePermits))
		}
		expected := validated
		expected.Phase = CleanupPhaseDeleteStarted
		expected.LeaseExpiresAt = fixture.target.removePermits[0].ExpiresAt.Add(time.Minute)
		fixture.assertWorkspaceCleanupClaim(t, expected)
		fixture.assertCleanupNodeLease(t, expected.NodeLeaseID, expected.JobID, expected.WorkerID,
			expected.NodeFence, expected.LeaseExpiresAt)
		var after model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", expected.JobID).Take(&after).Error; err != nil {
			t.Fatalf("load workspace incomplete cleanup job: %v", err)
		}
		if WorkspacePhase(after.WorkspacePhase) != WorkspacePhaseCleanupDue ||
			CleanupPhase(after.WorkspaceCleanupPhase) != CleanupPhaseDeleteStarted {
			t.Fatalf("workspace incomplete cleanup state=%s phase=%s",
				after.WorkspacePhase, after.WorkspaceCleanupPhase)
		}
		assertCleanupExecutionProvenanceUnchanged(t, before, after)
	})
}

func TestResultLifecycleCompleteCleanupPersistsDeletedFirst(t *testing.T) {
	t.Run("published result set", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		published, drained, before := fixture.preparePublishedCleanupDrained(t, "result-deleted-owner")
		fixture.now = fixture.now.Add(time.Minute)
		validated, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
		if err != nil {
			t.Fatalf("validate published cleanup before deleted transition: %v", err)
		}
		validationCalls := fixture.target.calls
		fixture.target.removal = OwnedJobDirRemoval{
			Complete: true, RemovedEntries: 3,
			ProgressDigest: framedDigest("xirang/recovery/r59-deleted-progress/v1", published.JobID),
		}

		fixture.now = fixture.now.Add(time.Minute)
		progress, err := fixture.service.AdvanceRecoveryResultCleanup(context.Background(), validated)
		if err != nil {
			t.Fatalf("persist published cleanup deleted phase: %v", err)
		}
		if progress.Phase != CleanupPhaseDeleted || !progress.Complete || progress.RemovedEntries != 3 ||
			progress.ProgressDigest != fixture.target.removal.ProgressDigest || !validDigest(progress.ProgressDigest) {
			t.Fatalf("published complete cleanup progress=%+v want durable deleted", progress)
		}
		if fixture.target.removeCalls != 1 || fixture.target.calls != validationCalls ||
			len(fixture.target.removePermits) != 1 {
			t.Fatalf("published complete target remove=%d validate=%d baseline=%d permits=%d",
				fixture.target.removeCalls, fixture.target.calls, validationCalls, len(fixture.target.removePermits))
		}

		expected := validated
		expected.Phase = CleanupPhaseDeleted
		expected.LeaseExpiresAt = fixture.target.removePermits[0].ExpiresAt.Add(time.Second)
		fixture.assertCleanupClaimRow(t, expected)
		fixture.assertCleanupNodeLease(t, expected.NodeLeaseID, expected.JobID, expected.WorkerID,
			expected.NodeFence, expected.LeaseExpiresAt)
		var resultSet model.BackupAssetRecoveryResultSet
		if err := fixture.db.Where("id = ? AND job_id = ?", expected.ResultSetID, expected.JobID).
			Take(&resultSet).Error; err != nil {
			t.Fatalf("load published deleted cleanup state: %v", err)
		}
		if ResultSetState(resultSet.State) != ResultSetStateRevoking ||
			CleanupPhase(resultSet.CleanupPhase) != CleanupPhaseDeleted {
			t.Fatalf("published deleted cleanup state=%s phase=%s", resultSet.State, resultSet.CleanupPhase)
		}
		var after model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", expected.JobID).Take(&after).Error; err != nil {
			t.Fatalf("load published deleted cleanup job: %v", err)
		}
		if WorkspacePhase(after.WorkspacePhase) != WorkspacePhasePublished {
			t.Fatalf("published deleted cleanup workspace phase=%s", after.WorkspacePhase)
		}
		assertCleanupExecutionProvenanceUnchanged(t, before, after)
	})

	t.Run("unpublished workspace", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		drained, before := fixture.prepareWorkspaceCleanupDrained(t, "workspace-deleted-owner")
		fixture.now = fixture.now.Add(time.Minute)
		validated, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained)
		if err != nil {
			t.Fatalf("validate workspace cleanup before deleted transition: %v", err)
		}
		validationCalls := fixture.target.calls
		fixture.target.removal = OwnedJobDirRemoval{
			Complete: true, RemovedEntries: 3,
			ProgressDigest: framedDigest("xirang/recovery/r59-deleted-progress/v1", validated.JobID),
		}

		fixture.now = fixture.now.Add(time.Minute)
		progress, err := fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), validated)
		if err != nil {
			t.Fatalf("persist workspace cleanup deleted phase: %v", err)
		}
		if progress.Phase != CleanupPhaseDeleted || !progress.Complete || progress.RemovedEntries != 3 ||
			progress.ProgressDigest != fixture.target.removal.ProgressDigest || !validDigest(progress.ProgressDigest) {
			t.Fatalf("workspace complete cleanup progress=%+v want durable deleted", progress)
		}
		if fixture.target.removeCalls != 1 || fixture.target.calls != validationCalls ||
			len(fixture.target.removePermits) != 1 {
			t.Fatalf("workspace complete target remove=%d validate=%d baseline=%d permits=%d",
				fixture.target.removeCalls, fixture.target.calls, validationCalls, len(fixture.target.removePermits))
		}

		expected := validated
		expected.Phase = CleanupPhaseDeleted
		expected.LeaseExpiresAt = fixture.target.removePermits[0].ExpiresAt.Add(time.Second)
		fixture.assertWorkspaceCleanupClaim(t, expected)
		fixture.assertCleanupNodeLease(t, expected.NodeLeaseID, expected.JobID, expected.WorkerID,
			expected.NodeFence, expected.LeaseExpiresAt)
		var after model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", expected.JobID).Take(&after).Error; err != nil {
			t.Fatalf("load workspace deleted cleanup state: %v", err)
		}
		if WorkspacePhase(after.WorkspacePhase) != WorkspacePhaseCleanupDue ||
			CleanupPhase(after.WorkspaceCleanupPhase) != CleanupPhaseDeleted {
			t.Fatalf("workspace deleted cleanup state=%s phase=%s",
				after.WorkspacePhase, after.WorkspaceCleanupPhase)
		}
		var resultSetCount int64
		if err := fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).
			Where("job_id = ?", expected.JobID).Count(&resultSetCount).Error; err != nil {
			t.Fatalf("count unpublished cleanup result sets: %v", err)
		}
		if resultSetCount != 0 {
			t.Fatalf("unpublished cleanup result-set count=%d want 0", resultSetCount)
		}
		assertCleanupExecutionProvenanceUnchanged(t, before, after)
	})
}

func TestResultLifecycleAdvanceDeletedCleanupUsesReadOnlyValidation(t *testing.T) {
	tests := []struct {
		name      string
		published bool
	}{
		{name: "published result set", published: true},
		{name: "unpublished workspace", published: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryResultLifecycleFixture(t)
			var publishedClaim RecoveryResultCleanupClaim
			var workspaceClaim RecoveryWorkspaceCleanupClaim
			if test.published {
				published, drained, _ := fixture.preparePublishedCleanupDrained(t, "result-deleted-validation-owner")
				fixture.now = fixture.now.Add(time.Minute)
				validated, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
				if err != nil {
					t.Fatalf("validate published cleanup: %v", err)
				}
				fixture.target.removal = OwnedJobDirRemoval{Complete: true, RemovedEntries: 2,
					ProgressDigest: framedDigest("xirang/recovery/r59-deleted-validation/v1", published.JobID)}
				fixture.now = fixture.now.Add(time.Minute)
				progress, err := fixture.service.AdvanceRecoveryResultCleanup(context.Background(), validated)
				if err != nil || progress.Phase != CleanupPhaseDeleted {
					t.Fatalf("complete published cleanup progress=%+v error=%v", progress, err)
				}
				publishedClaim = validated
				publishedClaim.Phase = CleanupPhaseDeleted
				publishedClaim.LeaseExpiresAt = fixture.target.removePermits[0].ExpiresAt.Add(time.Second)
			} else {
				drained, _ := fixture.prepareWorkspaceCleanupDrained(t, "workspace-deleted-validation-owner")
				fixture.now = fixture.now.Add(time.Minute)
				validated, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained)
				if err != nil {
					t.Fatalf("validate workspace cleanup: %v", err)
				}
				fixture.target.removal = OwnedJobDirRemoval{Complete: true, RemovedEntries: 2,
					ProgressDigest: framedDigest("xirang/recovery/r59-deleted-validation/v1", validated.JobID)}
				fixture.now = fixture.now.Add(time.Minute)
				progress, err := fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), validated)
				if err != nil || progress.Phase != CleanupPhaseDeleted {
					t.Fatalf("complete workspace cleanup progress=%+v error=%v", progress, err)
				}
				workspaceClaim = validated
				workspaceClaim.Phase = CleanupPhaseDeleted
				workspaceClaim.LeaseExpiresAt = fixture.target.removePermits[0].ExpiresAt.Add(time.Second)
			}

			removeCalls := fixture.target.removeCalls
			fixture.now = fixture.now.Add(time.Minute)
			var progress RecoveryCleanupProgress
			var err error
			if test.published {
				progress, err = fixture.service.AdvanceRecoveryResultCleanup(context.Background(), publishedClaim)
			} else {
				progress, err = fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), workspaceClaim)
			}
			if err != nil {
				t.Fatalf("advance durable deleted cleanup: %v", err)
			}
			if progress.Phase != CleanupPhaseTombstoned || !progress.Complete {
				t.Fatalf("durable deleted progress=%+v", progress)
			}
			if fixture.target.removeCalls != removeCalls || fixture.target.removedValidationCalls != 1 ||
				len(fixture.target.removedValidationPermits) != 1 || len(fixture.target.removedValidationRequests) != 1 {
				t.Fatalf("target remove=%d want=%d removed-validation=%d permits=%d requests=%d",
					fixture.target.removeCalls, removeCalls, fixture.target.removedValidationCalls,
					len(fixture.target.removedValidationPermits), len(fixture.target.removedValidationRequests))
			}
			permit := fixture.target.removedValidationPermits[0]
			if permit.Operation != TargetCleanupValidateRemovedJobDir || permit.proof == nil || permit.proof.validateLive != nil {
				t.Fatalf("removed validation permit=%+v proof=%+v", permit, permit.proof)
			}
			if test.published {
				var resultSet model.BackupAssetRecoveryResultSet
				if err := fixture.db.Where("id = ?", publishedClaim.ResultSetID).Take(&resultSet).Error; err != nil {
					t.Fatalf("load published tombstone: %v", err)
				}
				if ResultSetState(resultSet.State) != ResultSetStateCleaned ||
					CleanupPhase(resultSet.CleanupPhase) != CleanupPhaseTombstoned ||
					resultSet.CleanupOwner != "" || resultSet.CleanupLeaseExpiresAt != nil ||
					resultSet.NodeLeaseID != nil || resultSet.NodeFence != 0 {
					t.Fatalf("published tombstone row=%+v", resultSet)
				}
			} else {
				var job model.BackupAssetRecoveryJob
				if err := fixture.db.Where("id = ?", workspaceClaim.JobID).Take(&job).Error; err != nil {
					t.Fatalf("load workspace tombstone: %v", err)
				}
				if WorkspacePhase(job.WorkspacePhase) != WorkspacePhaseCleaned ||
					CleanupPhase(job.WorkspaceCleanupPhase) != CleanupPhaseTombstoned ||
					job.WorkspaceCleanupOwner != "" || job.WorkspaceCleanupLeaseExpiresAt != nil ||
					job.WorkspaceCleanupNodeLeaseID != nil || job.WorkspaceCleanupNodeFence != 0 {
					t.Fatalf("workspace tombstone job=%+v", job)
				}
			}
		})
	}
}

func TestResultLifecycleDeletedCleanupTerminalizationRollsBackOnEachFinalWrite(t *testing.T) {
	tests := []struct {
		name      string
		published bool
		table     string
	}{
		{name: "published result-set CAS", published: true, table: (model.BackupAssetRecoveryResultSet{}).TableName()},
		{name: "published node-lease release", published: true, table: (model.BackupAssetRecoveryNodeLease{}).TableName()},
		{name: "unpublished job CAS", published: false, table: (model.BackupAssetRecoveryJob{}).TableName()},
		{name: "unpublished node-lease release", published: false, table: (model.BackupAssetRecoveryNodeLease{}).TableName()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryResultLifecycleFixture(t)
			var publishedClaim RecoveryResultCleanupClaim
			var workspaceClaim RecoveryWorkspaceCleanupClaim
			if test.published {
				published, drained, _ := fixture.preparePublishedCleanupDrained(t, "result-terminal-rollback-owner")
				fixture.now = fixture.now.Add(time.Minute)
				validated, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
				if err != nil {
					t.Fatalf("validate published cleanup: %v", err)
				}
				fixture.target.removal = OwnedJobDirRemoval{Complete: true, RemovedEntries: 1,
					ProgressDigest: framedDigest("xirang/recovery/r59-terminal-rollback/v1", published.JobID)}
				fixture.now = fixture.now.Add(time.Minute)
				if _, err := fixture.service.AdvanceRecoveryResultCleanup(context.Background(), validated); err != nil {
					t.Fatalf("persist published deleted phase: %v", err)
				}
				publishedClaim = validated
				publishedClaim.Phase = CleanupPhaseDeleted
				publishedClaim.LeaseExpiresAt = fixture.target.removePermits[0].ExpiresAt.Add(time.Second)
			} else {
				drained, _ := fixture.prepareWorkspaceCleanupDrained(t, "workspace-terminal-rollback-owner")
				fixture.now = fixture.now.Add(time.Minute)
				validated, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained)
				if err != nil {
					t.Fatalf("validate workspace cleanup: %v", err)
				}
				fixture.target.removal = OwnedJobDirRemoval{Complete: true, RemovedEntries: 1,
					ProgressDigest: framedDigest("xirang/recovery/r59-terminal-rollback/v1", validated.JobID)}
				fixture.now = fixture.now.Add(time.Minute)
				if _, err := fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), validated); err != nil {
					t.Fatalf("persist workspace deleted phase: %v", err)
				}
				workspaceClaim = validated
				workspaceClaim.Phase = CleanupPhaseDeleted
				workspaceClaim.LeaseExpiresAt = fixture.target.removePermits[0].ExpiresAt.Add(time.Second)
			}

			updates := 0
			injected := errors.New("injected deleted terminalization failure")
			callbackName := "task7:fail_deleted_terminalization_" + strings.ReplaceAll(test.name, " ", "_")
			if err := fixture.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement != nil && tx.Statement.Table == test.table {
					updates++
					if updates == 2 {
						_ = tx.AddError(injected)
					}
				}
			}); err != nil {
				t.Fatalf("register terminalization fault: %v", err)
			}
			t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(callbackName) })

			fixture.now = fixture.now.Add(time.Minute)
			var err error
			if test.published {
				_, err = fixture.service.AdvanceRecoveryResultCleanup(context.Background(), publishedClaim)
			} else {
				_, err = fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), workspaceClaim)
			}
			if !errors.Is(err, ErrRecoveryResultCleanupConflict) {
				t.Fatalf("terminalization error=%v want=%v", err, ErrRecoveryResultCleanupConflict)
			}
			if updates < 2 || fixture.target.removedValidationCalls != 1 || fixture.target.removeCalls != 1 {
				t.Fatalf("terminalization updates=%d removed-validation=%d remove=%d",
					updates, fixture.target.removedValidationCalls, fixture.target.removeCalls)
			}
			if test.published {
				var resultSet model.BackupAssetRecoveryResultSet
				if err := fixture.db.Where("id = ?", publishedClaim.ResultSetID).Take(&resultSet).Error; err != nil {
					t.Fatalf("load rolled-back result-set: %v", err)
				}
				if ResultSetState(resultSet.State) != ResultSetStateRevoking || CleanupPhase(resultSet.CleanupPhase) != CleanupPhaseDeleted ||
					resultSet.CleanupOwner != publishedClaim.WorkerID || resultSet.CleanupLeaseExpiresAt == nil ||
					resultSet.NodeLeaseID == nil || *resultSet.NodeLeaseID != publishedClaim.NodeLeaseID {
					t.Fatalf("rolled-back result-set=%+v claim=%+v", resultSet, publishedClaim)
				}
			} else {
				var job model.BackupAssetRecoveryJob
				if err := fixture.db.Where("id = ?", workspaceClaim.JobID).Take(&job).Error; err != nil {
					t.Fatalf("load rolled-back workspace job: %v", err)
				}
				if WorkspacePhase(job.WorkspacePhase) != WorkspacePhaseCleanupDue || CleanupPhase(job.WorkspaceCleanupPhase) != CleanupPhaseDeleted ||
					job.WorkspaceCleanupOwner != workspaceClaim.WorkerID || job.WorkspaceCleanupLeaseExpiresAt == nil ||
					job.WorkspaceCleanupNodeLeaseID == nil || *job.WorkspaceCleanupNodeLeaseID != workspaceClaim.NodeLeaseID {
					t.Fatalf("rolled-back workspace job=%+v claim=%+v", job, workspaceClaim)
				}
			}
			var lease model.BackupAssetRecoveryNodeLease
			if err := fixture.db.Where("id = ?", func() string {
				if test.published {
					return publishedClaim.NodeLeaseID
				}
				return workspaceClaim.NodeLeaseID
			}()).Take(&lease).Error; err != nil {
				t.Fatalf("load rolled-back node lease: %v", err)
			}
			if lease.State != "active" || lease.ReleasedAt != nil {
				t.Fatalf("rolled-back node lease=%+v", lease)
			}
		})
	}
}

func TestResultLifecycleDeletedCleanupTakeoverOnlyValidatesCleanTuple(t *testing.T) {
	tests := []struct {
		name      string
		published bool
	}{
		{name: "published result set", published: true},
		{name: "unpublished workspace", published: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryResultLifecycleFixture(t)
			var oldResultClaim RecoveryResultCleanupClaim
			var oldWorkspaceClaim RecoveryWorkspaceCleanupClaim
			if test.published {
				published, drained, _ := fixture.preparePublishedCleanupDrained(t, "result-deleted-takeover-old")
				fixture.now = fixture.now.Add(time.Minute)
				validated, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
				if err != nil {
					t.Fatalf("validate published cleanup: %v", err)
				}
				fixture.target.removal = OwnedJobDirRemoval{Complete: true, RemovedEntries: 1,
					ProgressDigest: framedDigest("xirang/recovery/r59-takeover-deleted/v1", published.JobID)}
				fixture.now = fixture.now.Add(time.Minute)
				if _, err := fixture.service.AdvanceRecoveryResultCleanup(context.Background(), validated); err != nil {
					t.Fatalf("persist published deleted phase: %v", err)
				}
				var row model.BackupAssetRecoveryResultSet
				if err := fixture.db.Where("id = ?", published.ResultSetID).Take(&row).Error; err != nil {
					t.Fatalf("load published deleted row: %v", err)
				}
				if row.CleanupLeaseExpiresAt == nil || row.NodeLeaseID == nil {
					t.Fatalf("published deleted row missing authority=%+v", row)
				}
				oldResultClaim = RecoveryResultCleanupClaim{
					ResultSetID: row.ID, JobID: row.JobID, WorkerID: row.CleanupOwner,
					CleanupFence: row.CleanupFence, CleanupAttempt: row.CleanupAttempt,
					NodeLeaseID: *row.NodeLeaseID, NodeFence: row.NodeFence,
					LeaseExpiresAt: row.CleanupLeaseExpiresAt.UTC(), Phase: CleanupPhaseDeleted,
				}
				fixture.now = oldResultClaim.LeaseExpiresAt.Add(time.Minute)
				fresh, err := fixture.service.ClaimCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{
					ResultSetID: published.ResultSetID, WorkerID: "result-deleted-takeover-new",
				})
				if err != nil || fresh.Phase != CleanupPhaseDeleted || fresh.CleanupFence <= oldResultClaim.CleanupFence ||
					fresh.CleanupAttempt <= oldResultClaim.CleanupAttempt || fresh.NodeFence <= oldResultClaim.NodeFence {
					t.Fatalf("published deleted takeover fresh=%+v old=%+v err=%v", fresh, oldResultClaim, err)
				}
				calls := fixture.target.removedValidationCalls
				if _, err := fixture.service.AdvanceRecoveryResultCleanup(context.Background(), oldResultClaim); !errors.Is(err, ErrRecoveryResultCleanupConflict) {
					t.Fatalf("stale published deleted owner error=%v", err)
				}
				if fixture.target.removedValidationCalls != calls || fixture.target.removeCalls != 1 {
					t.Fatalf("stale published owner touched target removed-validation=%d remove=%d", fixture.target.removedValidationCalls, fixture.target.removeCalls)
				}
				if _, err := fixture.service.AdvanceRecoveryResultCleanup(context.Background(), fresh); err != nil {
					t.Fatalf("advance fresh published deleted takeover: %v", err)
				}
			} else {
				drained, _ := fixture.prepareWorkspaceCleanupDrained(t, "workspace-deleted-takeover-old")
				fixture.now = fixture.now.Add(time.Minute)
				validated, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained)
				if err != nil {
					t.Fatalf("validate workspace cleanup: %v", err)
				}
				fixture.target.removal = OwnedJobDirRemoval{Complete: true, RemovedEntries: 1,
					ProgressDigest: framedDigest("xirang/recovery/r59-takeover-deleted/v1", validated.JobID)}
				fixture.now = fixture.now.Add(time.Minute)
				if _, err := fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), validated); err != nil {
					t.Fatalf("persist workspace deleted phase: %v", err)
				}
				var row model.BackupAssetRecoveryJob
				if err := fixture.db.Where("id = ?", validated.JobID).Take(&row).Error; err != nil {
					t.Fatalf("load workspace deleted row: %v", err)
				}
				if row.WorkspaceCleanupLeaseExpiresAt == nil || row.WorkspaceCleanupNodeLeaseID == nil {
					t.Fatalf("workspace deleted row missing authority=%+v", row)
				}
				oldWorkspaceClaim = RecoveryWorkspaceCleanupClaim{
					JobID: row.ID, WorkerID: row.WorkspaceCleanupOwner,
					CleanupFence: row.WorkspaceCleanupFence, CleanupAttempt: row.WorkspaceCleanupAttempt,
					NodeLeaseID: *row.WorkspaceCleanupNodeLeaseID, NodeFence: row.WorkspaceCleanupNodeFence,
					LeaseExpiresAt: row.WorkspaceCleanupLeaseExpiresAt.UTC(), Phase: CleanupPhaseDeleted,
				}
				fixture.now = oldWorkspaceClaim.LeaseExpiresAt.Add(time.Minute)
				fresh, err := fixture.service.ClaimWorkspaceCleanup(context.Background(), ClaimRecoveryWorkspaceCleanupRequest{
					JobID: validated.JobID, WorkerID: "workspace-deleted-takeover-new",
				})
				if err != nil || fresh.Phase != CleanupPhaseDeleted || fresh.CleanupFence <= oldWorkspaceClaim.CleanupFence ||
					fresh.CleanupAttempt <= oldWorkspaceClaim.CleanupAttempt || fresh.NodeFence <= oldWorkspaceClaim.NodeFence {
					t.Fatalf("workspace deleted takeover fresh=%+v old=%+v err=%v", fresh, oldWorkspaceClaim, err)
				}
				calls := fixture.target.removedValidationCalls
				if _, err := fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), oldWorkspaceClaim); !errors.Is(err, ErrRecoveryResultCleanupConflict) {
					t.Fatalf("stale workspace deleted owner error=%v", err)
				}
				if fixture.target.removedValidationCalls != calls || fixture.target.removeCalls != 1 {
					t.Fatalf("stale workspace owner touched target removed-validation=%d remove=%d", fixture.target.removedValidationCalls, fixture.target.removeCalls)
				}
				if _, err := fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), fresh); err != nil {
					t.Fatalf("advance fresh workspace deleted takeover: %v", err)
				}
			}
			if fixture.target.removeCalls != 1 {
				t.Fatalf("takeover repeated destructive removal calls=%d", fixture.target.removeCalls)
			}
		})
	}
}

func TestResultLifecycleAdvanceCleanupFailureReleasesCurrentOwner(t *testing.T) {
	tests := []struct {
		name         string
		cancelCaller bool
	}{
		{name: "target unavailable"},
		{name: "caller cancellation", cancelCaller: true},
	}

	for _, test := range tests {
		t.Run("published "+test.name, func(t *testing.T) {
			fixture := newRecoveryResultLifecycleFixture(t)
			_, drained, _ := fixture.preparePublishedCleanupDrained(t, "result-advance-failure-owner")
			fixture.now = fixture.now.Add(time.Minute)
			validated, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
			if err != nil {
				t.Fatalf("validate published cleanup before advance failure: %v", err)
			}
			ctx := context.Background()
			var cancel context.CancelFunc
			if test.cancelCaller {
				ctx, cancel = context.WithCancel(ctx)
				fixture.target.afterRemove = func(TargetCleanupPermit, RemoveOwnedJobDirRequest) { cancel() }
			}
			fixture.target.err = ErrRecoveryTargetUnavailable

			fixture.now = fixture.now.Add(time.Minute)
			_, err = fixture.service.AdvanceRecoveryResultCleanup(ctx, validated)
			if test.cancelCaller {
				if err != context.Canceled {
					t.Fatalf("published cancellation error=%v want context.Canceled identity", err)
				}
			} else if !errors.Is(err, ErrRecoveryTargetUnavailable) {
				t.Fatalf("published target error=%v want=%v", err, ErrRecoveryTargetUnavailable)
			}
			if fixture.target.removeCalls != 1 || len(fixture.target.removePermits) != 1 {
				t.Fatalf("published failed advance remove calls=%d permits=%d",
					fixture.target.removeCalls, len(fixture.target.removePermits))
			}
			failed := validated
			failed.Phase = CleanupPhaseDeleteStarted
			failed.LeaseExpiresAt = fixture.target.removePermits[0].ExpiresAt
			fixture.assertPublishedCleanupAdvanceFailure(t, failed)
		})

		t.Run("workspace "+test.name, func(t *testing.T) {
			fixture := newRecoveryResultLifecycleFixture(t)
			drained, _ := fixture.prepareWorkspaceCleanupDrained(t, "workspace-advance-failure-owner")
			fixture.now = fixture.now.Add(time.Minute)
			validated, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained)
			if err != nil {
				t.Fatalf("validate workspace cleanup before advance failure: %v", err)
			}
			ctx := context.Background()
			var cancel context.CancelFunc
			if test.cancelCaller {
				ctx, cancel = context.WithCancel(ctx)
				fixture.target.afterRemove = func(TargetCleanupPermit, RemoveOwnedJobDirRequest) { cancel() }
			}
			fixture.target.err = ErrRecoveryTargetUnavailable

			fixture.now = fixture.now.Add(time.Minute)
			_, err = fixture.service.AdvanceRecoveryWorkspaceCleanup(ctx, validated)
			if test.cancelCaller {
				if err != context.Canceled {
					t.Fatalf("workspace cancellation error=%v want context.Canceled identity", err)
				}
			} else if !errors.Is(err, ErrRecoveryTargetUnavailable) {
				t.Fatalf("workspace target error=%v want=%v", err, ErrRecoveryTargetUnavailable)
			}
			if fixture.target.removeCalls != 1 || len(fixture.target.removePermits) != 1 {
				t.Fatalf("workspace failed advance remove calls=%d permits=%d",
					fixture.target.removeCalls, len(fixture.target.removePermits))
			}
			failed := validated
			failed.Phase = CleanupPhaseDeleteStarted
			failed.LeaseExpiresAt = fixture.target.removePermits[0].ExpiresAt
			fixture.assertWorkspaceCleanupAdvanceFailure(t, failed)
		})
	}

	t.Run("lost published owner leaves successor untouched", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		_, drained, _ := fixture.preparePublishedCleanupDrained(t, "result-lost-advance-owner")
		fixture.now = fixture.now.Add(time.Minute)
		validated, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
		if err != nil {
			t.Fatalf("validate published cleanup before lost-owner advance: %v", err)
		}
		fixture.target.err = ErrRecoveryTargetUnavailable
		var successor RecoveryResultCleanupClaim
		fixture.target.afterRemove = func(_ TargetCleanupPermit, _ RemoveOwnedJobDirRequest) {
			successor, err = fixture.replacePublishedCleanupAuthority(t, validated, "result-successor-owner")
		}

		fixture.now = fixture.now.Add(time.Minute)
		_, err = fixture.service.AdvanceRecoveryResultCleanup(context.Background(), validated)
		if !errors.Is(err, ErrRecoveryResultCleanupConflict) {
			t.Fatalf("published lost-owner error=%v want=%v", err, ErrRecoveryResultCleanupConflict)
		}
		if successor.WorkerID == "" {
			t.Fatal("published successor was not installed")
		}
		var resultSet model.BackupAssetRecoveryResultSet
		if err := fixture.db.Where("id = ?", validated.ResultSetID).Take(&resultSet).Error; err != nil {
			t.Fatalf("load published successor cleanup state: %v", err)
		}
		if ResultSetState(resultSet.State) != ResultSetStateRevoking ||
			CleanupPhase(resultSet.CleanupPhase) != CleanupPhaseDeleteStarted ||
			resultSet.CleanupOwner != successor.WorkerID || resultSet.NodeLeaseID == nil ||
			*resultSet.NodeLeaseID != successor.NodeLeaseID {
			t.Fatalf("published successor cleanup state=%+v successor=%+v", resultSet, successor)
		}
		fixture.assertCleanupNodeLease(t, successor.NodeLeaseID, successor.JobID, successor.WorkerID,
			successor.NodeFence, successor.LeaseExpiresAt)
	})

	t.Run("lost workspace owner leaves successor untouched", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		drained, _ := fixture.prepareWorkspaceCleanupDrained(t, "workspace-lost-advance-owner")
		fixture.now = fixture.now.Add(time.Minute)
		validated, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained)
		if err != nil {
			t.Fatalf("validate workspace cleanup before lost-owner advance: %v", err)
		}
		fixture.target.err = ErrRecoveryTargetUnavailable
		var successor RecoveryWorkspaceCleanupClaim
		fixture.target.afterRemove = func(_ TargetCleanupPermit, _ RemoveOwnedJobDirRequest) {
			successor = fixture.replaceWorkspaceCleanupAuthority(t, validated, "workspace-successor-owner")
		}

		fixture.now = fixture.now.Add(time.Minute)
		_, err = fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), validated)
		if !errors.Is(err, ErrRecoveryResultCleanupConflict) {
			t.Fatalf("workspace lost-owner error=%v want=%v", err, ErrRecoveryResultCleanupConflict)
		}
		if successor.WorkerID == "" {
			t.Fatal("workspace successor was not installed")
		}
		var job model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", validated.JobID).Take(&job).Error; err != nil {
			t.Fatalf("load workspace successor cleanup state: %v", err)
		}
		if WorkspacePhase(job.WorkspacePhase) != WorkspacePhaseCleanupDue ||
			CleanupPhase(job.WorkspaceCleanupPhase) != CleanupPhaseDeleteStarted ||
			job.WorkspaceCleanupOwner != successor.WorkerID || job.WorkspaceCleanupNodeLeaseID == nil ||
			*job.WorkspaceCleanupNodeLeaseID != successor.NodeLeaseID {
			t.Fatalf("workspace successor cleanup state=%+v successor=%+v", job, successor)
		}
		fixture.assertCleanupNodeLease(t, successor.NodeLeaseID, successor.JobID, successor.WorkerID,
			successor.NodeFence, successor.LeaseExpiresAt)
	})
}

func TestResultLifecycleAdvanceCleanupReentryAndExpiredTakeover(t *testing.T) {
	t.Run("published explicit reentry keeps current owner", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		_, drained, _ := fixture.preparePublishedCleanupDrained(t, "result-reentry-owner")
		fixture.now = fixture.now.Add(time.Minute)
		validated, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
		if err != nil {
			t.Fatalf("validate published cleanup before explicit reentry: %v", err)
		}
		fixture.target.removal = OwnedJobDirRemoval{
			Complete: false, RemovedEntries: 1,
			ProgressDigest: framedDigest("xirang/recovery/r58-first-progress/v1", validated.JobID),
		}
		fixture.now = fixture.now.Add(time.Minute)
		if _, err := fixture.service.AdvanceRecoveryResultCleanup(context.Background(), validated); err != nil {
			t.Fatalf("first published cleanup pass: %v", err)
		}

		resume := validated
		resume.Phase = CleanupPhaseDeleteStarted
		fixture.target.removal = OwnedJobDirRemoval{
			Complete: false, RemovedEntries: 2,
			ProgressDigest: framedDigest("xirang/recovery/r58-second-progress/v1", validated.JobID),
		}
		fixture.now = fixture.now.Add(time.Minute)
		progress, err := fixture.service.AdvanceRecoveryResultCleanup(context.Background(), resume)
		if err != nil {
			t.Fatalf("reenter published cleanup: %v", err)
		}
		if progress.Phase != CleanupPhaseDeleteStarted || progress.Complete || progress.RemovedEntries != 2 ||
			progress.ProgressDigest != fixture.target.removal.ProgressDigest || fixture.target.removeCalls != 2 ||
			len(fixture.target.removePermits) != 2 || len(fixture.target.removeRequests) != 2 {
			t.Fatalf("published reentry progress=%+v calls=%d permits=%d requests=%d", progress,
				fixture.target.removeCalls, len(fixture.target.removePermits), len(fixture.target.removeRequests))
		}
		first, second := fixture.target.removePermits[0], fixture.target.removePermits[1]
		if second.CleanupOwner != first.CleanupOwner || second.CleanupFence != first.CleanupFence ||
			second.CleanupAttempt != first.CleanupAttempt || second.NodeLeaseID != first.NodeLeaseID ||
			second.NodeFence != first.NodeFence || !second.ExpiresAt.After(first.ExpiresAt) {
			t.Fatalf("published reentry permits first=%+v second=%+v", first, second)
		}
		if fixture.target.removeRequests[0].MarkerBindingDigest != fixture.target.removeRequests[1].MarkerBindingDigest ||
			fixture.target.removeRequests[1].Object != fixture.target.removeRequests[0].Object {
			t.Fatalf("published reentry request drift first=%+v second=%+v",
				fixture.target.removeRequests[0], fixture.target.removeRequests[1])
		}
	})

	t.Run("workspace explicit reentry keeps current owner", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		drained, _ := fixture.prepareWorkspaceCleanupDrained(t, "workspace-reentry-owner")
		fixture.now = fixture.now.Add(time.Minute)
		validated, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained)
		if err != nil {
			t.Fatalf("validate workspace cleanup before explicit reentry: %v", err)
		}
		fixture.target.removal = OwnedJobDirRemoval{
			Complete: false, RemovedEntries: 1,
			ProgressDigest: framedDigest("xirang/recovery/r58-first-progress/v1", validated.JobID),
		}
		fixture.now = fixture.now.Add(time.Minute)
		if _, err := fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), validated); err != nil {
			t.Fatalf("first workspace cleanup pass: %v", err)
		}

		resume := validated
		resume.Phase = CleanupPhaseDeleteStarted
		fixture.target.removal = OwnedJobDirRemoval{
			Complete: false, RemovedEntries: 2,
			ProgressDigest: framedDigest("xirang/recovery/r58-second-progress/v1", validated.JobID),
		}
		fixture.now = fixture.now.Add(time.Minute)
		progress, err := fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), resume)
		if err != nil {
			t.Fatalf("reenter workspace cleanup: %v", err)
		}
		if progress.Phase != CleanupPhaseDeleteStarted || progress.Complete || progress.RemovedEntries != 2 ||
			progress.ProgressDigest != fixture.target.removal.ProgressDigest || fixture.target.removeCalls != 2 ||
			len(fixture.target.removePermits) != 2 || len(fixture.target.removeRequests) != 2 {
			t.Fatalf("workspace reentry progress=%+v calls=%d permits=%d requests=%d", progress,
				fixture.target.removeCalls, len(fixture.target.removePermits), len(fixture.target.removeRequests))
		}
		first, second := fixture.target.removePermits[0], fixture.target.removePermits[1]
		if second.CleanupOwner != first.CleanupOwner || second.CleanupFence != first.CleanupFence ||
			second.CleanupAttempt != first.CleanupAttempt || second.NodeLeaseID != first.NodeLeaseID ||
			second.NodeFence != first.NodeFence || !second.ExpiresAt.After(first.ExpiresAt) {
			t.Fatalf("workspace reentry permits first=%+v second=%+v", first, second)
		}
		if fixture.target.removeRequests[0].MarkerBindingDigest != fixture.target.removeRequests[1].MarkerBindingDigest ||
			fixture.target.removeRequests[1].Object != fixture.target.removeRequests[0].Object {
			t.Fatalf("workspace reentry request drift first=%+v second=%+v",
				fixture.target.removeRequests[0], fixture.target.removeRequests[1])
		}
	})

	t.Run("expired published owner is reclaimed with a fresh fence", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		published, drained, _ := fixture.preparePublishedCleanupDrained(t, "result-expired-owner")
		fixture.now = fixture.now.Add(time.Minute)
		validated, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
		if err != nil {
			t.Fatalf("validate published cleanup before takeover: %v", err)
		}
		fixture.target.removal = OwnedJobDirRemoval{Complete: false, RemovedEntries: 1,
			ProgressDigest: framedDigest("xirang/recovery/r58-takeover-first/v1", validated.JobID)}
		fixture.now = fixture.now.Add(time.Minute)
		if _, err := fixture.service.AdvanceRecoveryResultCleanup(context.Background(), validated); err != nil {
			t.Fatalf("first published cleanup pass before takeover: %v", err)
		}
		fixture.now = fixture.target.removePermits[0].ExpiresAt.Add(2 * time.Minute)
		fresh, err := fixture.service.ClaimCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{
			ResultSetID: published.ResultSetID, WorkerID: "result-takeover-owner",
		})
		if err != nil {
			t.Fatalf("claim expired published cleanup: %v", err)
		}
		if fresh.Phase != CleanupPhaseDeleteStarted || fresh.CleanupFence <= validated.CleanupFence ||
			fresh.CleanupAttempt <= validated.CleanupAttempt || fresh.NodeFence <= validated.NodeFence {
			t.Fatalf("published takeover claim=%+v prior=%+v", fresh, validated)
		}
		fixture.target.removal = OwnedJobDirRemoval{Complete: false, RemovedEntries: 2,
			ProgressDigest: framedDigest("xirang/recovery/r58-takeover-second/v1", fresh.JobID)}
		fixture.now = fixture.now.Add(time.Minute)
		if _, err := fixture.service.AdvanceRecoveryResultCleanup(context.Background(), fresh); err != nil {
			t.Fatalf("advance reclaimed published cleanup: %v", err)
		}
		if fixture.target.removeCalls != 2 || len(fixture.target.removePermits) != 2 ||
			fixture.target.removePermits[1].CleanupOwner != fresh.WorkerID ||
			fixture.target.removePermits[1].CleanupFence != fresh.CleanupFence ||
			fixture.target.removePermits[1].NodeFence != fresh.NodeFence {
			t.Fatalf("published takeover remove calls=%d permits=%+v", fixture.target.removeCalls,
				fixture.target.removePermits)
		}
	})

	t.Run("expired workspace owner is reclaimed with a fresh fence", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		drained, _ := fixture.prepareWorkspaceCleanupDrained(t, "workspace-expired-owner")
		fixture.now = fixture.now.Add(time.Minute)
		validated, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained)
		if err != nil {
			t.Fatalf("validate workspace cleanup before takeover: %v", err)
		}
		fixture.target.removal = OwnedJobDirRemoval{Complete: false, RemovedEntries: 1,
			ProgressDigest: framedDigest("xirang/recovery/r58-takeover-first/v1", validated.JobID)}
		fixture.now = fixture.now.Add(time.Minute)
		if _, err := fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), validated); err != nil {
			t.Fatalf("first workspace cleanup pass before takeover: %v", err)
		}
		fixture.now = fixture.target.removePermits[0].ExpiresAt.Add(2 * time.Minute)
		fresh, err := fixture.service.ClaimWorkspaceCleanup(context.Background(), ClaimRecoveryWorkspaceCleanupRequest{
			JobID: validated.JobID, WorkerID: "workspace-takeover-owner",
		})
		if err != nil {
			t.Fatalf("claim expired workspace cleanup: %v", err)
		}
		if fresh.Phase != CleanupPhaseDeleteStarted || fresh.CleanupFence <= validated.CleanupFence ||
			fresh.CleanupAttempt <= validated.CleanupAttempt || fresh.NodeFence <= validated.NodeFence {
			t.Fatalf("workspace takeover claim=%+v prior=%+v", fresh, validated)
		}
		fixture.target.removal = OwnedJobDirRemoval{Complete: false, RemovedEntries: 2,
			ProgressDigest: framedDigest("xirang/recovery/r58-takeover-second/v1", fresh.JobID)}
		fixture.now = fixture.now.Add(time.Minute)
		if _, err := fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), fresh); err != nil {
			t.Fatalf("advance reclaimed workspace cleanup: %v", err)
		}
		if fixture.target.removeCalls != 2 || len(fixture.target.removePermits) != 2 ||
			fixture.target.removePermits[1].CleanupOwner != fresh.WorkerID ||
			fixture.target.removePermits[1].CleanupFence != fresh.CleanupFence ||
			fixture.target.removePermits[1].NodeFence != fresh.NodeFence {
			t.Fatalf("workspace takeover remove calls=%d permits=%+v", fixture.target.removeCalls,
				fixture.target.removePermits)
		}
	})
}

func TestTargetCleanupLiveValidatorRunsBeforeEveryMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *recoveryResultLifecycleFixture, RecoveryResultCleanupClaim)
	}{
		{
			name: "cleanup owner",
			mutate: func(t *testing.T, fixture *recoveryResultLifecycleFixture, claim RecoveryResultCleanupClaim) {
				t.Helper()
				if err := fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).
					Where("id = ?", claim.ResultSetID).UpdateColumn("cleanup_owner", "replacement-owner").Error; err != nil {
					t.Fatalf("revoke cleanup owner: %v", err)
				}
			},
		},
		{
			name: "cleanup fence",
			mutate: func(t *testing.T, fixture *recoveryResultLifecycleFixture, claim RecoveryResultCleanupClaim) {
				t.Helper()
				if err := fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).
					Where("id = ?", claim.ResultSetID).UpdateColumn("cleanup_fence", claim.CleanupFence+1).Error; err != nil {
					t.Fatalf("revoke cleanup fence: %v", err)
				}
			},
		},
		{
			name: "node fence",
			mutate: func(t *testing.T, fixture *recoveryResultLifecycleFixture, claim RecoveryResultCleanupClaim) {
				t.Helper()
				if err := fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).
					Where("id = ?", claim.ResultSetID).UpdateColumn("node_fence", claim.NodeFence+1).Error; err != nil {
					t.Fatalf("revoke cleanup node fence: %v", err)
				}
			},
		},
		{
			name: "lease",
			mutate: func(t *testing.T, fixture *recoveryResultLifecycleFixture, claim RecoveryResultCleanupClaim) {
				t.Helper()
				if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).
					Where("id = ?", claim.NodeLeaseID).
					UpdateColumn("lease_expires_at", fixture.now.Add(-time.Second)).Error; err != nil {
					t.Fatalf("expire cleanup node lease: %v", err)
				}
			},
		},
		{
			name: "permanent use latch",
			mutate: func(t *testing.T, fixture *recoveryResultLifecycleFixture, _ RecoveryResultCleanupClaim) {
				t.Helper()
				if err := fixture.db.Where("id = ?", recoverySchemaUseLatchRowID).
					Delete(&model.BackupAssetRecoveryEvidence{}).Error; err != nil {
					t.Fatalf("remove permanent recovery use latch: %v", err)
				}
			},
		},
		{
			name: "cleanup phase",
			mutate: func(t *testing.T, fixture *recoveryResultLifecycleFixture, claim RecoveryResultCleanupClaim) {
				t.Helper()
				if err := fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).
					Where("id = ?", claim.ResultSetID).
					UpdateColumn("cleanup_phase", string(CleanupPhaseDeleted)).Error; err != nil {
					t.Fatalf("revoke cleanup phase: %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryResultLifecycleFixture(t)
			_, drained, _ := fixture.preparePublishedCleanupDrained(t, "cleanup-live-validator-owner")
			fixture.now = fixture.now.Add(time.Minute)
			validated, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
			if err != nil {
				t.Fatalf("validate cleanup live-validator fixture: %v", err)
			}
			fixture.target.plannedMutations = 1
			fixture.target.removal = OwnedJobDirRemoval{Complete: false}
			fixture.target.beforeRemoveMutation = func(
				_ int,
				_ TargetCleanupPermit,
				_ RemoveOwnedJobDirRequest,
			) {
				test.mutate(t, fixture, validated)
			}

			fixture.now = fixture.now.Add(time.Minute)
			_, err = fixture.service.AdvanceRecoveryResultCleanup(context.Background(), validated)
			if err == nil || (!errors.Is(err, ErrRecoveryResultCleanupConflict) &&
				!errors.Is(err, ErrInvalidTargetPermit)) {
				t.Fatalf("revoked %s advance error=%v, want stable cleanup authority error", test.name, err)
			}
			if fixture.target.removeCalls != 1 || fixture.target.removeMutations != 0 ||
				len(fixture.target.removePermits) != 1 || fixture.target.removePermits[0].proof == nil ||
				fixture.target.removePermits[0].proof.validateLive == nil {
				t.Fatalf("revoked %s remove calls=%d mutations=%d permits=%d",
					test.name, fixture.target.removeCalls, fixture.target.removeMutations,
					len(fixture.target.removePermits))
			}
		})
	}
}

func TestResultLifecycleAdvanceCleanupPostgres(t *testing.T) {
	fixture := newRecoveryResultLifecyclePostgresFixture(t)
	published, drained, _ := fixture.preparePublishedCleanupDrained(t, "postgres-delete-start-owner")
	fixture.now = fixture.now.Add(time.Minute)
	validated, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
	if err != nil {
		t.Fatalf("validate PostgreSQL cleanup before delete start: %v", err)
	}
	fixture.target.removal = OwnedJobDirRemoval{
		Complete: false, ProgressDigest: framedDigest("xirang/recovery/r55-progress/v1", published.JobID),
	}
	fixture.now = fixture.now.Add(time.Minute)
	progress, err := fixture.service.AdvanceRecoveryResultCleanup(context.Background(), validated)
	if err != nil {
		t.Fatalf("advance PostgreSQL cleanup to delete_started: %v", err)
	}
	if progress.Phase != CleanupPhaseDeleteStarted || progress.Complete || fixture.target.removeCalls != 1 ||
		len(fixture.target.removePermits) != 1 || fixture.target.removePermits[0].proof == nil ||
		fixture.target.removePermits[0].proof.validateLive == nil {
		t.Fatalf("PostgreSQL cleanup progress=%+v remove_calls=%d permits=%d", progress,
			fixture.target.removeCalls, len(fixture.target.removePermits))
	}
}

func TestResultLifecycleAdvanceCleanupR58Postgres(t *testing.T) {
	t.Run("published incomplete progress is durable", func(t *testing.T) {
		fixture := newRecoveryResultLifecyclePostgresFixture(t)
		_, drained, _ := fixture.preparePublishedCleanupDrained(t, "postgres-r58-result-owner")
		fixture.now = fixture.now.Add(time.Minute)
		validated, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
		if err != nil {
			t.Fatalf("validate PostgreSQL published cleanup: %v", err)
		}
		fixture.target.plannedMutations = 1
		fixture.target.removal = OwnedJobDirRemoval{
			Complete: false, RemovedEntries: 1,
			ProgressDigest: framedDigest("xirang/recovery/r58-postgres-progress/v1", validated.JobID),
		}
		fixture.target.beforeRemoveMutation = func(
			mutation int,
			_ TargetCleanupPermit,
			_ RemoveOwnedJobDirRequest,
		) {
			if mutation == 0 {
				fixture.now = fixture.now.Add(time.Minute)
			}
		}
		fixture.now = fixture.now.Add(time.Minute)
		progress, err := fixture.service.AdvanceRecoveryResultCleanup(context.Background(), validated)
		if err != nil {
			t.Fatalf("advance PostgreSQL published incomplete cleanup: %v", err)
		}
		if progress.Phase != CleanupPhaseDeleteStarted || progress.Complete || progress.RemovedEntries != 1 ||
			fixture.target.removeCalls != 1 || len(fixture.target.removePermits) != 1 {
			t.Fatalf("PostgreSQL published incomplete progress=%+v calls=%d permits=%d", progress,
				fixture.target.removeCalls, len(fixture.target.removePermits))
		}
		expected := validated
		expected.Phase = CleanupPhaseDeleteStarted
		expected.LeaseExpiresAt = fixture.target.removePermits[0].ExpiresAt.Add(time.Minute)
		fixture.assertCleanupClaimRow(t, expected)
		fixture.assertCleanupNodeLease(t, expected.NodeLeaseID, expected.JobID, expected.WorkerID,
			expected.NodeFence, expected.LeaseExpiresAt)
	})

	t.Run("workspace incomplete progress is durable", func(t *testing.T) {
		fixture := newRecoveryResultLifecyclePostgresFixture(t)
		drained, _ := fixture.prepareWorkspaceCleanupDrained(t, "postgres-r58-workspace-owner")
		fixture.now = fixture.now.Add(time.Minute)
		validated, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained)
		if err != nil {
			t.Fatalf("validate PostgreSQL workspace cleanup: %v", err)
		}
		fixture.target.plannedMutations = 1
		fixture.target.removal = OwnedJobDirRemoval{
			Complete: false, RemovedEntries: 1,
			ProgressDigest: framedDigest("xirang/recovery/r58-postgres-progress/v1", validated.JobID),
		}
		fixture.target.beforeRemoveMutation = func(
			mutation int,
			_ TargetCleanupPermit,
			_ RemoveOwnedJobDirRequest,
		) {
			if mutation == 0 {
				fixture.now = fixture.now.Add(time.Minute)
			}
		}
		fixture.now = fixture.now.Add(time.Minute)
		progress, err := fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), validated)
		if err != nil {
			t.Fatalf("advance PostgreSQL workspace incomplete cleanup: %v", err)
		}
		if progress.Phase != CleanupPhaseDeleteStarted || progress.Complete || progress.RemovedEntries != 1 ||
			fixture.target.removeCalls != 1 || len(fixture.target.removePermits) != 1 {
			t.Fatalf("PostgreSQL workspace incomplete progress=%+v calls=%d permits=%d", progress,
				fixture.target.removeCalls, len(fixture.target.removePermits))
		}
		expected := validated
		expected.Phase = CleanupPhaseDeleteStarted
		expected.LeaseExpiresAt = fixture.target.removePermits[0].ExpiresAt.Add(time.Minute)
		fixture.assertWorkspaceCleanupClaim(t, expected)
		fixture.assertCleanupNodeLease(t, expected.NodeLeaseID, expected.JobID, expected.WorkerID,
			expected.NodeFence, expected.LeaseExpiresAt)
	})
}

func TestResultLifecycleAdvanceCleanupR59Postgres(t *testing.T) {
	t.Run("published deleted tuple", func(t *testing.T) {
		fixture := newRecoveryResultLifecyclePostgresFixture(t)
		published, drained, _ := fixture.preparePublishedCleanupDrained(t, "postgres-r59-result-owner")
		fixture.now = fixture.now.Add(time.Minute)
		validated, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
		if err != nil {
			t.Fatalf("validate PostgreSQL published cleanup: %v", err)
		}
		fixture.target.removal = OwnedJobDirRemoval{Complete: true, RemovedEntries: 1,
			ProgressDigest: framedDigest("xirang/recovery/r59-postgres-progress/v1", published.JobID)}
		fixture.now = fixture.now.Add(time.Minute)
		deleted, err := fixture.service.AdvanceRecoveryResultCleanup(context.Background(), validated)
		if err != nil || deleted.Phase != CleanupPhaseDeleted {
			t.Fatalf("persist PostgreSQL published deleted progress=%+v error=%v", deleted, err)
		}
		var row model.BackupAssetRecoveryResultSet
		if err := fixture.db.Where("id = ?", published.ResultSetID).Take(&row).Error; err != nil {
			t.Fatalf("load PostgreSQL published deleted row: %v", err)
		}
		claim := RecoveryResultCleanupClaim{
			ResultSetID: row.ID, JobID: row.JobID, WorkerID: row.CleanupOwner,
			CleanupFence: row.CleanupFence, CleanupAttempt: row.CleanupAttempt,
			NodeLeaseID: *row.NodeLeaseID, NodeFence: row.NodeFence,
			LeaseExpiresAt: row.CleanupLeaseExpiresAt.UTC(), Phase: CleanupPhaseDeleted,
		}
		fixture.now = fixture.now.Add(time.Minute)
		finished, err := fixture.service.AdvanceRecoveryResultCleanup(context.Background(), claim)
		if err != nil || finished.Phase != CleanupPhaseTombstoned || !finished.Complete || fixture.target.removeCalls != 1 || fixture.target.removedValidationCalls != 1 {
			t.Fatalf("finish PostgreSQL published cleanup progress=%+v error=%v remove=%d removed-validation=%d",
				finished, err, fixture.target.removeCalls, fixture.target.removedValidationCalls)
		}
	})

	t.Run("workspace deleted tuple", func(t *testing.T) {
		fixture := newRecoveryResultLifecyclePostgresFixture(t)
		drained, _ := fixture.prepareWorkspaceCleanupDrained(t, "postgres-r59-workspace-owner")
		fixture.now = fixture.now.Add(time.Minute)
		validated, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained)
		if err != nil {
			t.Fatalf("validate PostgreSQL workspace cleanup: %v", err)
		}
		fixture.target.removal = OwnedJobDirRemoval{Complete: true, RemovedEntries: 1,
			ProgressDigest: framedDigest("xirang/recovery/r59-postgres-progress/v1", validated.JobID)}
		fixture.now = fixture.now.Add(time.Minute)
		deleted, err := fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), validated)
		if err != nil || deleted.Phase != CleanupPhaseDeleted {
			t.Fatalf("persist PostgreSQL workspace deleted progress=%+v error=%v", deleted, err)
		}
		var row model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", validated.JobID).Take(&row).Error; err != nil {
			t.Fatalf("load PostgreSQL workspace deleted row: %v", err)
		}
		claim := RecoveryWorkspaceCleanupClaim{
			JobID: row.ID, WorkerID: row.WorkspaceCleanupOwner,
			CleanupFence: row.WorkspaceCleanupFence, CleanupAttempt: row.WorkspaceCleanupAttempt,
			NodeLeaseID: *row.WorkspaceCleanupNodeLeaseID, NodeFence: row.WorkspaceCleanupNodeFence,
			LeaseExpiresAt: row.WorkspaceCleanupLeaseExpiresAt.UTC(), Phase: CleanupPhaseDeleted,
		}
		fixture.now = fixture.now.Add(time.Minute)
		finished, err := fixture.service.AdvanceRecoveryWorkspaceCleanup(context.Background(), claim)
		if err != nil || finished.Phase != CleanupPhaseTombstoned || !finished.Complete || fixture.target.removeCalls != 1 || fixture.target.removedValidationCalls != 1 {
			t.Fatalf("finish PostgreSQL workspace cleanup progress=%+v error=%v remove=%d removed-validation=%d",
				finished, err, fixture.target.removeCalls, fixture.target.removedValidationCalls)
		}
	})
}

func TestRecoveryCleanupValidationCarriesExactTargetSessionBinding(t *testing.T) {
	tests := []struct {
		name     string
		validate func(*recoveryResultLifecycleFixture) error
	}{
		{
			name: "published result set",
			validate: func(fixture *recoveryResultLifecycleFixture) error {
				_, drained, _ := fixture.preparePublishedCleanupDrained(t, "published-session-owner")
				fixture.now = fixture.now.Add(time.Minute)
				_, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
				return err
			},
		},
		{
			name: "unpublished workspace",
			validate: func(fixture *recoveryResultLifecycleFixture) error {
				drained, _ := fixture.prepareWorkspaceCleanupDrained(t, "workspace-session-owner")
				fixture.now = fixture.now.Add(time.Minute)
				_, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained)
				return err
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newRecoveryResultLifecycleFixture(t)
			if err := testCase.validate(fixture); err != nil {
				t.Fatalf("validate cleanup target: %v", err)
			}
			if len(fixture.target.permits) != 1 || fixture.target.permits[0].proof == nil {
				t.Fatalf("cleanup target permits = %+v, want one proof-bearing permit", fixture.target.permits)
			}
			var plan model.BackupAssetRecoveryPlan
			if err := fixture.db.Where("id = ?", fixture.job.PlanID).Take(&plan).Error; err != nil {
				t.Fatalf("load cleanup plan: %v", err)
			}
			want, err := newRecoveryTargetSessionBinding(plan)
			if err != nil {
				t.Fatalf("derive expected cleanup target session binding: %v plan=%+v", err, plan)
			}
			permit := fixture.target.permits[0]
			if permit.proof.sessionBinding != want ||
				permit.proof.bindingDigest != targetCleanupPermitBindingDigest(permit, want.bindingDigest) {
				t.Fatalf("cleanup target session proof = %+v, want exact plan binding %+v", permit.proof, want)
			}
			if fixture.target.removeCalls != 0 {
				t.Fatalf("cleanup validation remove calls = %d, want zero", fixture.target.removeCalls)
			}
		})
	}
}

func TestRecoveryCleanupValidationRejectsTargetSessionSnapshotDrift(t *testing.T) {
	t.Run("published result set", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		_, drained, _ := fixture.preparePublishedCleanupDrained(t, "published-session-drift-owner")
		var driftErr error
		fixture.target.afterObservation = func(TargetCleanupPermit, ValidateOwnedJobDirRequest) {
			driftErr = fixture.db.Table((model.BackupAssetRecoveryPlan{}).TableName()).
				Where("id = ?", fixture.job.PlanID).
				UpdateColumn("target_base_revision", "FAKE_CLOSING_NODE_REVISION_DRIFT_FOR_TEST_ONLY").Error
		}
		fixture.now = fixture.now.Add(time.Minute)
		_, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
		if driftErr != nil {
			t.Fatalf("inject published session drift: %v", driftErr)
		}
		assertSanitizedCleanupValidationFailure(t, err)
		if fixture.target.calls != 1 || fixture.target.removeCalls != 0 || len(fixture.target.permits) != 1 {
			t.Fatalf("published session drift target=%d remove=%d permits=%d",
				fixture.target.calls, fixture.target.removeCalls, len(fixture.target.permits))
		}
		fixture.assertPublishedCleanupValidationFailure(t, drained)
	})

	t.Run("unpublished workspace", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		drained, _ := fixture.prepareWorkspaceCleanupDrained(t, "workspace-session-drift-owner")
		var driftErr error
		fixture.target.afterObservation = func(TargetCleanupPermit, ValidateOwnedJobDirRequest) {
			driftErr = fixture.db.Table((model.BackupAssetRecoveryPlan{}).TableName()).
				Where("id = ?", fixture.job.PlanID).
				UpdateColumn("credential_scope_revision", "FAKE_CLOSING_CREDENTIAL_REVISION_DRIFT_FOR_TEST_ONLY").Error
		}
		fixture.now = fixture.now.Add(time.Minute)
		_, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained)
		if driftErr != nil {
			t.Fatalf("inject workspace session drift: %v", driftErr)
		}
		assertSanitizedCleanupValidationFailure(t, err)
		if fixture.target.calls != 1 || fixture.target.removeCalls != 0 || len(fixture.target.permits) != 1 {
			t.Fatalf("workspace session drift target=%d remove=%d permits=%d",
				fixture.target.calls, fixture.target.removeCalls, len(fixture.target.permits))
		}
		fixture.assertWorkspaceCleanupValidationFailure(t, drained)
	})
}

func TestRecoveryCleanupTargetValidationRejectsDriftAndLostFence(t *testing.T) {
	t.Run("published marker creator drift before closing CAS", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		_, drained, _ := fixture.preparePublishedCleanupDrained(t, "result-validation-creator-drift")
		jobQueries := 0
		callbackName := "task7:inject_cleanup_marker_creator_drift"
		if err := fixture.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement == nil || tx.Statement.Table != (model.BackupAssetRecoveryJob{}).TableName() {
				return
			}
			job, ok := tx.Statement.Dest.(*model.BackupAssetRecoveryJob)
			if !ok {
				return
			}
			jobQueries++
			if jobQueries == 2 {
				job.WorkspaceOwner = "drifted-marker-creator"
				job.WorkspaceFence++
			}
		}); err != nil {
			t.Fatalf("register marker creator drift: %v", err)
		}
		t.Cleanup(func() { _ = fixture.db.Callback().Query().Remove(callbackName) })

		_, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
		assertSanitizedCleanupValidationFailure(t, err)
		if jobQueries < 2 || fixture.target.calls != 1 || fixture.target.removeCalls != 0 {
			t.Fatalf("marker creator drift queries=%d target=%d remove=%d", jobQueries, fixture.target.calls, fixture.target.removeCalls)
		}
		fixture.assertPublishedCleanupValidationFailure(t, drained)
	})

	t.Run("tampered published claim is local", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		_, drained, _ := fixture.preparePublishedCleanupDrained(t, "result-validation-tampered")
		drained.CleanupFence++
		if _, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained); !errors.Is(err, ErrRecoveryResultCleanupConflict) {
			t.Fatalf("tampered published cleanup error=%v want=%v", err, ErrRecoveryResultCleanupConflict)
		}
		if fixture.target.calls != 0 || fixture.target.removeCalls != 0 {
			t.Fatalf("tampered published claim crossed target: calls=%d remove=%d",
				fixture.target.calls, fixture.target.removeCalls)
		}
	})

	t.Run("cross-resource published claim is local", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		_, drained, _ := fixture.preparePublishedCleanupDrained(t, "result-validation-cross-resource")
		drained.ResultSetID = strings.Repeat("9", 32)
		if _, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained); !errors.Is(err, ErrRecoveryResultCleanupConflict) {
			t.Fatalf("cross-resource published cleanup error=%v want=%v", err, ErrRecoveryResultCleanupConflict)
		}
		if fixture.target.calls != 0 || fixture.target.removeCalls != 0 {
			t.Fatalf("cross-resource claim crossed target: calls=%d remove=%d",
				fixture.target.calls, fixture.target.removeCalls)
		}
	})

	for _, boundary := range []string{"before observation", "after observation"} {
		t.Run("published takeover "+boundary, func(t *testing.T) {
			fixture := newRecoveryResultLifecycleFixture(t)
			_, drained, _ := fixture.preparePublishedCleanupDrained(t, "result-validation-old-owner")
			var fresh RecoveryResultCleanupClaim
			var takeoverErr error
			takeover := func(TargetCleanupPermit, ValidateOwnedJobDirRequest) {
				fresh, takeoverErr = fixture.replacePublishedCleanupAuthority(t, drained, "result-validation-fresh-owner")
			}
			if boundary == "before observation" {
				fixture.target.beforeObservation = takeover
			} else {
				fixture.target.afterObservation = takeover
			}
			if _, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained); !errors.Is(err, ErrRecoveryResultCleanupConflict) {
				t.Fatalf("published takeover error=%v want=%v", err, ErrRecoveryResultCleanupConflict)
			}
			if takeoverErr != nil {
				t.Fatalf("replace published cleanup authority: %v", takeoverErr)
			}
			if fixture.target.calls != 1 || fixture.target.removeCalls != 0 || fresh.Phase != CleanupPhaseDrained {
				t.Fatalf("published takeover target calls=%d remove=%d fresh=%+v",
					fixture.target.calls, fixture.target.removeCalls, fresh)
			}
			fixture.assertCleanupClaimRow(t, fresh)
			fixture.assertCleanupNodeLease(t, fresh.NodeLeaseID, fresh.JobID, fresh.WorkerID,
				fresh.NodeFence, fresh.LeaseExpiresAt)
		})
	}

	t.Run("published final CAS lost", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		_, drained, _ := fixture.preparePublishedCleanupDrained(t, "result-validation-lost-cas")
		updates := 0
		callbackName := "task7:lose_published_validation_final_cas"
		if err := fixture.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Table == (model.BackupAssetRecoveryResultSet{}).TableName() {
				updates++
				if updates == 2 {
					tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "1 = 0"}}})
				}
			}
		}); err != nil {
			t.Fatalf("register published validation CAS fault: %v", err)
		}
		t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(callbackName) })

		if _, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained); !errors.Is(err, ErrRecoveryResultCleanupConflict) {
			t.Fatalf("published validation lost-CAS error=%v want=%v", err, ErrRecoveryResultCleanupConflict)
		}
		if updates != 2 || fixture.target.calls != 1 || fixture.target.removeCalls != 0 {
			t.Fatalf("published validation lost-CAS updates=%d calls=%d remove=%d",
				updates, fixture.target.calls, fixture.target.removeCalls)
		}
		persisted := drained
		persisted.LeaseExpiresAt = fixture.target.observedLeaseExpiry
		fixture.assertCleanupClaimRow(t, persisted)
		fixture.assertCleanupNodeLease(t, persisted.NodeLeaseID, persisted.JobID, persisted.WorkerID,
			persisted.NodeFence, persisted.LeaseExpiresAt)
	})

	t.Run("cross-job workspace claim is local", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		drained, _ := fixture.prepareWorkspaceCleanupDrained(t, "workspace-validation-cross-job")
		drained.JobID = strings.Repeat("9", 32)
		if _, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained); !errors.Is(err, ErrRecoveryResultCleanupConflict) {
			t.Fatalf("cross-job workspace cleanup error=%v want=%v", err, ErrRecoveryResultCleanupConflict)
		}
		if fixture.target.calls != 0 || fixture.target.removeCalls != 0 {
			t.Fatalf("cross-job workspace claim crossed target: calls=%d remove=%d",
				fixture.target.calls, fixture.target.removeCalls)
		}
	})

	t.Run("workspace takeover after observation", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		drained, _ := fixture.prepareWorkspaceCleanupDrained(t, "workspace-validation-old-owner")
		var fresh RecoveryWorkspaceCleanupClaim
		fixture.target.afterObservation = func(TargetCleanupPermit, ValidateOwnedJobDirRequest) {
			fresh = fixture.replaceWorkspaceCleanupAuthority(t, drained, "workspace-validation-fresh-owner")
		}
		if _, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained); !errors.Is(err, ErrRecoveryResultCleanupConflict) {
			t.Fatalf("workspace takeover error=%v want=%v", err, ErrRecoveryResultCleanupConflict)
		}
		if fixture.target.calls != 1 || fixture.target.removeCalls != 0 || fresh.Phase != CleanupPhaseDrained {
			t.Fatalf("workspace takeover target calls=%d remove=%d fresh=%+v",
				fixture.target.calls, fixture.target.removeCalls, fresh)
		}
		fixture.assertWorkspaceCleanupClaim(t, fresh)
		fixture.assertCleanupNodeLease(t, fresh.NodeLeaseID, fresh.JobID, fresh.WorkerID,
			fresh.NodeFence, fresh.LeaseExpiresAt)
	})

	t.Run("workspace final CAS lost", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		drained, _ := fixture.prepareWorkspaceCleanupDrained(t, "workspace-validation-lost-cas")
		updates := 0
		callbackName := "task7:lose_workspace_validation_final_cas"
		if err := fixture.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Table == (model.BackupAssetRecoveryJob{}).TableName() {
				updates++
				if updates == 2 {
					tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "1 = 0"}}})
				}
			}
		}); err != nil {
			t.Fatalf("register workspace validation CAS fault: %v", err)
		}
		t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(callbackName) })

		if _, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained); !errors.Is(err, ErrRecoveryResultCleanupConflict) {
			t.Fatalf("workspace validation lost-CAS error=%v want=%v", err, ErrRecoveryResultCleanupConflict)
		}
		if updates != 2 || fixture.target.calls != 1 || fixture.target.removeCalls != 0 {
			t.Fatalf("workspace validation lost-CAS updates=%d calls=%d remove=%d",
				updates, fixture.target.calls, fixture.target.removeCalls)
		}
		persisted := drained
		persisted.LeaseExpiresAt = fixture.target.observedLeaseExpiry
		fixture.assertWorkspaceCleanupClaim(t, persisted)
		fixture.assertCleanupNodeLease(t, persisted.NodeLeaseID, persisted.JobID, persisted.WorkerID,
			persisted.NodeFence, persisted.LeaseExpiresAt)
	})
}

func TestRecoveryCleanupTargetValidationFailureReleasesLeaseAndResumesDrained(t *testing.T) {
	type failureCase struct {
		name        string
		configure   func(*testing.T, *recoveryResultLifecycleFixture)
		beforeCall  func(*testing.T, *recoveryResultLifecycleFixture)
		callContext func(*recoveryResultLifecycleFixture) context.Context
		targetCalls int
	}
	rawTargetError := func(category string) error {
		return fmt.Errorf("RAW_CLEANUP_TARGET_%s_PRIVATE_DETAIL", category)
	}
	cases := []failureCase{
		{name: "target error", configure: func(_ *testing.T, fixture *recoveryResultLifecycleFixture) {
			fixture.target.err = rawTargetError("ERROR")
		}, targetCalls: 1},
		{name: "missing directory", configure: func(_ *testing.T, fixture *recoveryResultLifecycleFixture) {
			fixture.target.err = rawTargetError("MISSING_DIRECTORY")
		}, targetCalls: 1},
		{name: "symlink or special entry", configure: func(_ *testing.T, fixture *recoveryResultLifecycleFixture) {
			fixture.target.err = rawTargetError("SYMLINK_SPECIAL")
		}, targetCalls: 1},
		{name: "marker mismatch", configure: func(_ *testing.T, fixture *recoveryResultLifecycleFixture) {
			fixture.target.mutateObservation = func(observation *OwnedJobDirValidation) {
				observation.MarkerBindingDigest = strings.Repeat("c", sha256DigestLength)
			}
		}, targetCalls: 1},
		{name: "root revision drift", configure: func(_ *testing.T, fixture *recoveryResultLifecycleFixture) {
			fixture.target.mutateObservation = func(observation *OwnedJobDirValidation) {
				observation.RootRevision = "root-revision-drifted"
			}
		}, targetCalls: 1},
		{name: "partial object", configure: func(_ *testing.T, fixture *recoveryResultLifecycleFixture) {
			fixture.target.mutateObservation = func(observation *OwnedJobDirValidation) {
				observation.Object.PrivateRelativeLocator = "jobs/partial-observation"
			}
		}, targetCalls: 1},
		{name: "empty target revision", configure: func(_ *testing.T, fixture *recoveryResultLifecycleFixture) {
			fixture.target.mutateObservation = func(observation *OwnedJobDirValidation) {
				observation.TargetRevision = ""
			}
		}, targetCalls: 1},
		{name: "timeout", configure: func(_ *testing.T, fixture *recoveryResultLifecycleFixture) {
			fixture.target.err = context.DeadlineExceeded
		}, targetCalls: 1},
		{name: "cancellation", configure: func(_ *testing.T, fixture *recoveryResultLifecycleFixture) {
			fixture.target.err = context.Canceled
		}, callContext: func(fixture *recoveryResultLifecycleFixture) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			fixture.target.beforeObservation = func(TargetCleanupPermit, ValidateOwnedJobDirRequest) {
				cancel()
			}
			return ctx
		}, targetCalls: 1},
		{name: "transport ambiguity", configure: func(_ *testing.T, fixture *recoveryResultLifecycleFixture) {
			fixture.target.err = rawTargetError("TRANSPORT_AMBIGUITY")
		}, targetCalls: 1},
		{name: "missing latch before issuance", beforeCall: func(t *testing.T, fixture *recoveryResultLifecycleFixture) {
			if err := fixture.db.Where("id = ?", recoverySchemaUseLatchRowID).
				Delete(&model.BackupAssetRecoveryEvidence{}).Error; err != nil {
				t.Fatalf("delete cleanup validation latch before issuance: %v", err)
			}
		}, targetCalls: 0},
		{name: "latch loss before closing CAS", configure: func(t *testing.T, fixture *recoveryResultLifecycleFixture) {
			fixture.target.afterObservation = func(TargetCleanupPermit, ValidateOwnedJobDirRequest) {
				if err := fixture.db.Where("id = ?", recoverySchemaUseLatchRowID).
					Delete(&model.BackupAssetRecoveryEvidence{}).Error; err != nil {
					t.Fatalf("delete cleanup validation latch after observation: %v", err)
				}
			}
		}, targetCalls: 1},
	}

	for _, test := range cases {
		for _, resource := range []string{"published", "workspace"} {
			t.Run(resource+" "+test.name, func(t *testing.T) {
				fixture := newRecoveryResultLifecycleFixture(t)
				ctx := context.Background()
				if test.callContext != nil {
					ctx = test.callContext(fixture)
				}
				if resource == "published" {
					_, drained, _ := fixture.preparePublishedCleanupDrained(t, "result-validation-failure-owner")
					contentCalls := len(fixture.contentLifecycle.order)
					if test.configure != nil {
						test.configure(t, fixture)
					}
					if test.beforeCall != nil {
						test.beforeCall(t, fixture)
					}
					_, err := fixture.service.ValidateRecoveryResultCleanup(ctx, drained)
					assertSanitizedCleanupValidationFailure(t, err)
					fixture.assertPublishedCleanupValidationFailure(t, drained)
					if len(fixture.contentLifecycle.order) != contentCalls {
						t.Fatalf("published validation failure repeated Content: %v", fixture.contentLifecycle.order)
					}
				} else {
					drained, _ := fixture.prepareWorkspaceCleanupDrained(t, "workspace-validation-failure-owner")
					contentCalls := len(fixture.contentLifecycle.order)
					if test.configure != nil {
						test.configure(t, fixture)
					}
					if test.beforeCall != nil {
						test.beforeCall(t, fixture)
					}
					_, err := fixture.service.ValidateRecoveryWorkspaceCleanup(ctx, drained)
					assertSanitizedCleanupValidationFailure(t, err)
					fixture.assertWorkspaceCleanupValidationFailure(t, drained)
					if len(fixture.contentLifecycle.order) != contentCalls {
						t.Fatalf("workspace validation failure crossed Content: %v", fixture.contentLifecycle.order)
					}
				}
				if fixture.target.calls != test.targetCalls || fixture.target.removeCalls != 0 {
					t.Fatalf("validation failure target calls=%d want=%d remove=%d",
						fixture.target.calls, test.targetCalls, fixture.target.removeCalls)
				}
			})
		}
	}

	t.Run("published retry resumes drained with fresh fences", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		published, drained, _ := fixture.preparePublishedCleanupDrained(t, "result-validation-first-owner")
		contentCalls := len(fixture.contentLifecycle.order)
		fixture.target.err = rawTargetError("RETRYABLE")
		_, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
		assertSanitizedCleanupValidationFailure(t, err)
		priorNodeFence := fixture.maxNodeFence(t)
		fresh, err := fixture.service.ClaimCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{
			ResultSetID: published.ResultSetID, WorkerID: "result-validation-retry-owner",
		})
		if err != nil {
			t.Fatalf("claim published validation retry: %v", err)
		}
		if fresh.Phase != CleanupPhaseDrained || fresh.CleanupFence != drained.CleanupFence+1 ||
			fresh.CleanupAttempt != drained.CleanupAttempt+1 || fresh.NodeFence <= priorNodeFence {
			t.Fatalf("published cleanup retry=%+v prior_node_fence=%d drained=%+v",
				fresh, priorNodeFence, drained)
		}
		fixture.target.err = nil
		validated, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), fresh)
		if err != nil {
			t.Fatalf("validate retried published cleanup: %v", err)
		}
		if validated.Phase != CleanupPhaseValidated || len(fixture.contentLifecycle.order) != contentCalls ||
			fixture.target.calls != 2 || fixture.target.removeCalls != 0 {
			t.Fatalf("published retry validated=%+v target_calls=%d remove=%d Content=%v",
				validated, fixture.target.calls, fixture.target.removeCalls, fixture.contentLifecycle.order)
		}
	})

	t.Run("workspace retry resumes drained with fresh fences", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		drained, _ := fixture.prepareWorkspaceCleanupDrained(t, "workspace-validation-first-owner")
		contentCalls := len(fixture.contentLifecycle.order)
		fixture.target.err = rawTargetError("RETRYABLE")
		_, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained)
		assertSanitizedCleanupValidationFailure(t, err)
		priorNodeFence := fixture.maxNodeFence(t)
		fresh, err := fixture.service.ClaimWorkspaceCleanup(context.Background(), ClaimRecoveryWorkspaceCleanupRequest{
			JobID: drained.JobID, WorkerID: "workspace-validation-retry-owner",
		})
		if err != nil {
			t.Fatalf("claim workspace validation retry: %v", err)
		}
		if fresh.Phase != CleanupPhaseDrained || fresh.CleanupFence != drained.CleanupFence+1 ||
			fresh.CleanupAttempt != drained.CleanupAttempt+1 || fresh.NodeFence <= priorNodeFence {
			t.Fatalf("workspace cleanup retry=%+v prior_node_fence=%d drained=%+v",
				fresh, priorNodeFence, drained)
		}
		fixture.target.err = nil
		validated, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), fresh)
		if err != nil {
			t.Fatalf("validate retried workspace cleanup: %v", err)
		}
		if validated.Phase != CleanupPhaseValidated || len(fixture.contentLifecycle.order) != contentCalls ||
			fixture.target.calls != 2 || fixture.target.removeCalls != 0 {
			t.Fatalf("workspace retry validated=%+v target_calls=%d remove=%d Content=%v",
				validated, fixture.target.calls, fixture.target.removeCalls, fixture.contentLifecycle.order)
		}
	})

	t.Run("published failure projection lost CAS keeps active authority", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		_, drained, _ := fixture.preparePublishedCleanupDrained(t, "result-validation-projection-lost")
		fixture.target.err = rawTargetError("PROJECTION_LOST")
		updates := 0
		callbackName := "task7:lose_published_validation_failure_projection"
		if err := fixture.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Table == (model.BackupAssetRecoveryResultSet{}).TableName() {
				updates++
				if updates == 2 {
					tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "1 = 0"}}})
				}
			}
		}); err != nil {
			t.Fatalf("register published failure-projection CAS fault: %v", err)
		}
		t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(callbackName) })
		_, err := fixture.service.ValidateRecoveryResultCleanup(context.Background(), drained)
		if !errors.Is(err, ErrRecoveryResultCleanupConflict) ||
			errors.Is(err, ErrRecoveryResultCleanupValidationFailed) {
			t.Fatalf("published failure-projection lost-CAS error=%v want cleanup conflict", err)
		}
		persisted := drained
		persisted.LeaseExpiresAt = fixture.target.observedLeaseExpiry
		fixture.assertCleanupClaimRow(t, persisted)
		fixture.assertCleanupNodeLease(t, persisted.NodeLeaseID, persisted.JobID, persisted.WorkerID,
			persisted.NodeFence, persisted.LeaseExpiresAt)
	})

	t.Run("workspace failure projection lost CAS keeps active authority", func(t *testing.T) {
		fixture := newRecoveryResultLifecycleFixture(t)
		drained, _ := fixture.prepareWorkspaceCleanupDrained(t, "workspace-validation-projection-lost")
		fixture.target.err = rawTargetError("PROJECTION_LOST")
		updates := 0
		callbackName := "task7:lose_workspace_validation_failure_projection"
		if err := fixture.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Table == (model.BackupAssetRecoveryJob{}).TableName() {
				updates++
				if updates == 2 {
					tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "1 = 0"}}})
				}
			}
		}); err != nil {
			t.Fatalf("register workspace failure-projection CAS fault: %v", err)
		}
		t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(callbackName) })
		_, err := fixture.service.ValidateRecoveryWorkspaceCleanup(context.Background(), drained)
		if !errors.Is(err, ErrRecoveryResultCleanupConflict) ||
			errors.Is(err, ErrRecoveryResultCleanupValidationFailed) {
			t.Fatalf("workspace failure-projection lost-CAS error=%v want cleanup conflict", err)
		}
		persisted := drained
		persisted.LeaseExpiresAt = fixture.target.observedLeaseExpiry
		fixture.assertWorkspaceCleanupClaim(t, persisted)
		fixture.assertCleanupNodeLease(t, persisted.NodeLeaseID, persisted.JobID, persisted.WorkerID,
			persisted.NodeFence, persisted.LeaseExpiresAt)
	})
}

func assertSanitizedCleanupValidationFailure(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrRecoveryResultCleanupValidationFailed) ||
		err.Error() != ErrRecoveryResultCleanupValidationFailed.Error() ||
		strings.Contains(err.Error(), "RAW_CLEANUP_TARGET") {
		t.Fatalf("cleanup validation error=%v want only sanitized sentinel", err)
	}
}

func assertCleanupExecutionProvenanceUnchanged(
	t *testing.T,
	before model.BackupAssetRecoveryJob,
	after model.BackupAssetRecoveryJob,
) {
	t.Helper()
	if after.WorkspaceOwner != before.WorkspaceOwner || after.WorkspaceFence != before.WorkspaceFence ||
		after.WorkspaceMarkerBindingDigest != before.WorkspaceMarkerBindingDigest ||
		after.WorkspaceBindingDigest != before.WorkspaceBindingDigest ||
		after.WorkspaceMarkerValidationAttemptID != before.WorkspaceMarkerValidationAttemptID ||
		after.WorkspaceMarkerValidationAttemptFence != before.WorkspaceMarkerValidationAttemptFence ||
		after.WorkspaceMarkerValidationNodeFence != before.WorkspaceMarkerValidationNodeFence {
		t.Fatalf("cleanup validation rewrote execution provenance: before=%+v after=%+v", before, after)
	}
}

func (fixture *recoveryResultLifecycleFixture) preparePublishedCleanupDrained(
	t *testing.T,
	workerID string,
) (PublishedRecoveryResultSet, RecoveryResultCleanupClaim, model.BackupAssetRecoveryJob) {
	t.Helper()
	published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
	if err != nil {
		t.Fatalf("publish validation fixture: %v", err)
	}
	claim, err := fixture.service.ClaimCleanup(context.Background(), ClaimRecoveryResultCleanupRequest{
		ResultSetID: published.ResultSetID, WorkerID: workerID,
	})
	if err != nil {
		t.Fatalf("claim published validation fixture: %v", err)
	}
	fixture.now = fixture.now.Add(time.Minute)
	revoked, err := fixture.service.RevokeRecoveryResultCleanup(context.Background(), claim)
	if err != nil {
		t.Fatalf("revoke published validation fixture: %v", err)
	}
	fixture.now = fixture.now.Add(time.Minute)
	drained, err := fixture.service.DrainRecoveryResultCleanup(context.Background(), revoked)
	if err != nil {
		t.Fatalf("drain published validation fixture: %v", err)
	}
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", drained.JobID).Take(&job).Error; err != nil {
		t.Fatalf("load drained published cleanup job: %v", err)
	}
	return published, drained, job
}

func (fixture *recoveryResultLifecycleFixture) prepareWorkspaceCleanupDrained(
	t *testing.T,
	workerID string,
) (RecoveryWorkspaceCleanupClaim, model.BackupAssetRecoveryJob) {
	t.Helper()
	fixture.prepareWorkspaceCleanupDue(t)
	claim, err := fixture.service.ClaimWorkspaceCleanup(context.Background(), ClaimRecoveryWorkspaceCleanupRequest{
		JobID: fixture.job.ID, WorkerID: workerID,
	})
	if err != nil {
		t.Fatalf("claim workspace validation fixture: %v", err)
	}
	fixture.now = fixture.now.Add(time.Minute)
	revoked, err := fixture.service.RevokeRecoveryWorkspaceCleanup(context.Background(), claim)
	if err != nil {
		t.Fatalf("revoke workspace validation fixture: %v", err)
	}
	fixture.now = fixture.now.Add(time.Minute)
	drained, err := fixture.service.DrainRecoveryWorkspaceCleanup(context.Background(), revoked)
	if err != nil {
		t.Fatalf("drain workspace validation fixture: %v", err)
	}
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", drained.JobID).Take(&job).Error; err != nil {
		t.Fatalf("load drained workspace cleanup job: %v", err)
	}
	return drained, job
}

func (fixture *recoveryResultLifecycleFixture) assertPublishedCleanupValidationFailure(
	t *testing.T,
	claim RecoveryResultCleanupClaim,
) {
	t.Helper()
	var resultSet model.BackupAssetRecoveryResultSet
	if err := fixture.db.Where("id = ? AND job_id = ?", claim.ResultSetID, claim.JobID).
		Take(&resultSet).Error; err != nil {
		t.Fatalf("load published cleanup validation failure: %v", err)
	}
	if ResultSetState(resultSet.State) != ResultSetStateCleanupFailed ||
		CleanupPhase(resultSet.CleanupPhase) != CleanupPhaseDrained || resultSet.CleanupOwner != "" ||
		resultSet.CleanupLeaseExpiresAt != nil || resultSet.CleanupFence != claim.CleanupFence ||
		resultSet.NodeLeaseID != nil || resultSet.NodeFence != 0 ||
		resultSet.CleanupAttempt != claim.CleanupAttempt {
		t.Fatalf("published cleanup validation failure row=%+v claim=%+v", resultSet, claim)
	}
	fixture.assertReleasedCleanupNodeLease(t, claim.NodeLeaseID, claim.JobID, claim.WorkerID, claim.NodeFence)
}

func (fixture *recoveryResultLifecycleFixture) assertWorkspaceCleanupValidationFailure(
	t *testing.T,
	claim RecoveryWorkspaceCleanupClaim,
) {
	t.Helper()
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatalf("load workspace cleanup validation failure: %v", err)
	}
	if WorkspacePhase(job.WorkspacePhase) != WorkspacePhaseCleanupDue ||
		CleanupPhase(job.WorkspaceCleanupPhase) != CleanupPhaseDrained || job.WorkspaceCleanupOwner != "" ||
		job.WorkspaceCleanupLeaseExpiresAt != nil || job.WorkspaceCleanupFence != claim.CleanupFence ||
		job.WorkspaceCleanupNodeLeaseID != nil || job.WorkspaceCleanupNodeFence != 0 ||
		job.WorkspaceCleanupAttempt != claim.CleanupAttempt {
		t.Fatalf("workspace cleanup validation failure row=%+v claim=%+v", job, claim)
	}
	fixture.assertReleasedCleanupNodeLease(t, claim.NodeLeaseID, claim.JobID, claim.WorkerID, claim.NodeFence)
}

func (fixture *recoveryResultLifecycleFixture) assertPublishedCleanupAdvanceFailure(
	t *testing.T,
	claim RecoveryResultCleanupClaim,
) {
	t.Helper()
	var resultSet model.BackupAssetRecoveryResultSet
	if err := fixture.db.Where("id = ? AND job_id = ?", claim.ResultSetID, claim.JobID).
		Take(&resultSet).Error; err != nil {
		t.Fatalf("load published cleanup advance failure: %v", err)
	}
	if ResultSetState(resultSet.State) != ResultSetStateCleanupFailed ||
		CleanupPhase(resultSet.CleanupPhase) != CleanupPhaseDeleteStarted || resultSet.CleanupOwner != "" ||
		resultSet.CleanupLeaseExpiresAt != nil || resultSet.CleanupFence != claim.CleanupFence ||
		resultSet.NodeLeaseID != nil || resultSet.NodeFence != 0 ||
		resultSet.CleanupAttempt != claim.CleanupAttempt {
		t.Fatalf("published cleanup advance failure row=%+v claim=%+v", resultSet, claim)
	}
	fixture.assertReleasedCleanupNodeLease(t, claim.NodeLeaseID, claim.JobID, claim.WorkerID, claim.NodeFence)
}

func (fixture *recoveryResultLifecycleFixture) assertWorkspaceCleanupAdvanceFailure(
	t *testing.T,
	claim RecoveryWorkspaceCleanupClaim,
) {
	t.Helper()
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatalf("load workspace cleanup advance failure: %v", err)
	}
	if WorkspacePhase(job.WorkspacePhase) != WorkspacePhaseCleanupDue ||
		CleanupPhase(job.WorkspaceCleanupPhase) != CleanupPhaseDeleteStarted ||
		job.WorkspaceCleanupOwner != "" || job.WorkspaceCleanupLeaseExpiresAt != nil ||
		job.WorkspaceCleanupFence != claim.CleanupFence || job.WorkspaceCleanupNodeLeaseID != nil ||
		job.WorkspaceCleanupNodeFence != 0 || job.WorkspaceCleanupAttempt != claim.CleanupAttempt {
		t.Fatalf("workspace cleanup advance failure row=%+v claim=%+v", job, claim)
	}
	fixture.assertReleasedCleanupNodeLease(t, claim.NodeLeaseID, claim.JobID, claim.WorkerID, claim.NodeFence)
}

func (fixture *recoveryResultLifecycleFixture) assertReleasedCleanupNodeLease(
	t *testing.T,
	leaseID string,
	jobID string,
	ownerID string,
	fence uint64,
) {
	t.Helper()
	var lease model.BackupAssetRecoveryNodeLease
	if err := fixture.db.Where("id = ?", leaseID).Take(&lease).Error; err != nil {
		t.Fatalf("load released cleanup node lease: %v", err)
	}
	if lease.JobID != jobID || lease.OwnerID != ownerID || lease.Fence != fence ||
		lease.HolderKind != "recovery_cleanup" || lease.State != "released" || lease.ReleasedAt == nil {
		t.Fatalf("released cleanup node lease=%+v want job=%s owner=%s fence=%d",
			lease, jobID, ownerID, fence)
	}
}

func (fixture *recoveryResultLifecycleFixture) recordCleanupValidationLocks(
	t *testing.T,
	events *[]string,
	published bool,
) {
	t.Helper()
	callbackName := "task7:record_workspace_validation_locks"
	if published {
		callbackName = "task7:record_published_validation_locks"
	}
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		switch tx.Statement.Table {
		case (model.BackupAssetRecoveryJob{}).TableName():
			*events = append(*events, "job")
		case (model.BackupAssetRecoveryNodeLease{}).TableName():
			*events = append(*events, "node")
		case (model.BackupAssetRecoveryResultSet{}).TableName():
			*events = append(*events, "result")
		}
	}); err != nil {
		t.Fatalf("register cleanup validation lock recorder: %v", err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Query().Remove(callbackName) })
}

func (fixture *recoveryResultLifecycleFixture) replacePublishedCleanupAuthority(
	t *testing.T,
	old RecoveryResultCleanupClaim,
	workerID string,
) (RecoveryResultCleanupClaim, error) {
	t.Helper()
	now := fixture.now.UTC()
	if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).Where("id = ?", old.NodeLeaseID).
		Updates(map[string]any{"state": "expired", "released_at": now, "updated_at": now}).Error; err != nil {
		return RecoveryResultCleanupClaim{}, err
	}
	nodeLeaseID, err := backupasset.NewOpaqueID()
	if err != nil {
		return RecoveryResultCleanupClaim{}, err
	}
	nodeFence := fixture.maxNodeFence(t) + 1
	expiresAt := old.LeaseExpiresAt.Add(time.Minute).UTC()
	lease := model.BackupAssetRecoveryNodeLease{
		ID: nodeLeaseID, NodeID: fixture.job.TargetNodeID, HolderKind: "recovery_cleanup",
		JobID: old.JobID, OwnerID: workerID, Fence: nodeFence, State: "active",
		LeaseExpiresAt: expiresAt, CreatedAt: now, UpdatedAt: now,
	}
	if err := fixture.db.Create(&lease).Error; err != nil {
		return RecoveryResultCleanupClaim{}, err
	}
	fresh := RecoveryResultCleanupClaim{
		ResultSetID: old.ResultSetID, JobID: old.JobID, WorkerID: workerID,
		CleanupFence: old.CleanupFence + 1, CleanupAttempt: old.CleanupAttempt + 1,
		NodeLeaseID: nodeLeaseID, NodeFence: nodeFence, LeaseExpiresAt: expiresAt,
		Phase: CleanupPhaseDrained,
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).Where("id = ?", old.ResultSetID).
		Updates(map[string]any{
			"cleanup_owner": workerID, "cleanup_lease_expires_at": expiresAt,
			"cleanup_fence": fresh.CleanupFence, "node_lease_id": nodeLeaseID,
			"node_fence": nodeFence, "cleanup_attempt": fresh.CleanupAttempt, "updated_at": now,
		}).Error; err != nil {
		return RecoveryResultCleanupClaim{}, err
	}
	return fresh, nil
}

func (fixture *recoveryResultLifecycleFixture) replaceWorkspaceCleanupAuthority(
	t *testing.T,
	old RecoveryWorkspaceCleanupClaim,
	workerID string,
) RecoveryWorkspaceCleanupClaim {
	t.Helper()
	now := fixture.now.UTC()
	if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).Where("id = ?", old.NodeLeaseID).
		Updates(map[string]any{"state": "expired", "released_at": now, "updated_at": now}).Error; err != nil {
		t.Fatalf("expire old workspace cleanup node lease: %v", err)
	}
	nodeLeaseID, err := backupasset.NewOpaqueID()
	if err != nil {
		t.Fatalf("create fresh workspace cleanup lease ID: %v", err)
	}
	nodeFence := fixture.maxNodeFence(t) + 1
	expiresAt := old.LeaseExpiresAt.Add(time.Minute).UTC()
	lease := model.BackupAssetRecoveryNodeLease{
		ID: nodeLeaseID, NodeID: fixture.job.TargetNodeID, HolderKind: "recovery_cleanup",
		JobID: old.JobID, OwnerID: workerID, Fence: nodeFence, State: "active",
		LeaseExpiresAt: expiresAt, CreatedAt: now, UpdatedAt: now,
	}
	if err := fixture.db.Create(&lease).Error; err != nil {
		t.Fatalf("create fresh workspace cleanup lease: %v", err)
	}
	fresh := RecoveryWorkspaceCleanupClaim{
		JobID: old.JobID, WorkerID: workerID,
		CleanupFence: old.CleanupFence + 1, CleanupAttempt: old.CleanupAttempt + 1,
		NodeLeaseID: nodeLeaseID, NodeFence: nodeFence, LeaseExpiresAt: expiresAt,
		Phase: CleanupPhaseDrained,
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", old.JobID).
		Updates(map[string]any{
			"workspace_cleanup_owner": workerID, "workspace_cleanup_lease_expires_at": expiresAt,
			"workspace_cleanup_fence":         fresh.CleanupFence,
			"workspace_cleanup_node_lease_id": nodeLeaseID,
			"workspace_cleanup_node_fence":    nodeFence,
			"workspace_cleanup_attempt":       fresh.CleanupAttempt, "updated_at": now,
		}).Error; err != nil {
		t.Fatalf("replace workspace cleanup authority: %v", err)
	}
	return fresh
}

func (fixture *recoveryResultLifecycleFixture) assertCleanupNodeLease(
	t *testing.T,
	leaseID string,
	jobID string,
	ownerID string,
	fence uint64,
	expiresAt time.Time,
) {
	t.Helper()
	var lease model.BackupAssetRecoveryNodeLease
	if err := fixture.db.Where("id = ?", leaseID).Take(&lease).Error; err != nil {
		t.Fatalf("load recovery cleanup node lease: %v", err)
	}
	if lease.JobID != jobID || lease.HolderKind != "recovery_cleanup" || lease.OwnerID != ownerID ||
		lease.Fence != fence || lease.State != "active" || !lease.LeaseExpiresAt.Equal(expiresAt) {
		t.Fatalf("recovery cleanup node lease=%+v want job=%s owner=%s fence=%d expiry=%s",
			lease, jobID, ownerID, fence, expiresAt)
	}
}

func assertOrderedEvents(t *testing.T, events []string, expected ...string) {
	t.Helper()
	position := 0
	for _, event := range events {
		if position < len(expected) && event == expected[position] {
			position++
		}
	}
	if position != len(expected) {
		t.Fatalf("events %v do not contain ordered subsequence %v", events, expected)
	}
}

func newRecoveryResultLifecycleFixture(t *testing.T) *recoveryResultLifecycleFixture {
	t.Helper()
	return newRecoveryResultLifecycleFixtureFromAuthorization(
		t, newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute),
	)
}

func newRecoveryResultLifecyclePostgresFixture(t *testing.T) *recoveryResultLifecycleFixture {
	t.Helper()
	return newRecoveryResultLifecycleFixtureFromAuthorization(
		t, newAuthorizationReceiptPostgresServiceFixture(t, AuthorizationReceiptExecute),
	)
}

func newRecoveryResultLifecycleFixtureFromAuthorization(
	t *testing.T,
	authorization *authorizationReceiptServiceFixture,
) *recoveryResultLifecycleFixture {
	t.Helper()
	if !authorization.db.Migrator().HasTable(&model.TaskRun{}) {
		if err := authorization.db.AutoMigrate(&model.Task{}, &model.TaskRun{}); err != nil {
			t.Fatalf("migrate result lifecycle TaskRun fixture: %v", err)
		}
	}
	executed, err := authorization.service.Authorize(context.Background(), authorization.request)
	if err != nil {
		t.Fatalf("execute recovery result fixture: %v", err)
	}

	var job model.BackupAssetRecoveryJob
	if err := authorization.db.Where("id = ?", executed.JobID).Take(&job).Error; err != nil {
		t.Fatalf("load recovery result fixture job: %v", err)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := authorization.db.Where("id = ? AND job_id = ?", executed.AttemptID, job.ID).Take(&attempt).Error; err != nil {
		t.Fatalf("load recovery result fixture attempt: %v", err)
	}
	deadline := authorization.now.Add(recoveryResultTestDefaultTTL)
	markerDigest := framedDigest("xirang/recovery/result-test-marker/v1", job.ID)
	closedAt := authorization.now
	if err := authorization.db.Model(&model.BackupAssetRecoveryAttempt{}).Where("job_id = ?", job.ID).
		Updates(map[string]any{
			"state": string(AttemptStateCompleted), "mutation_armed": true,
			"closed_at": closedAt, "updated_at": authorization.now,
		}).Error; err != nil {
		t.Fatalf("close recovery result fixture attempt: %v", err)
	}
	if err := authorization.db.Model(&model.RecoveryPointLease{}).
		Where("holder_type = ? AND owner_id = ? AND status = ?", backupasset.LeaseHolderRecoveryJob, job.ID, "active").
		Updates(map[string]any{"status": "released", "released_at": closedAt, "updated_at": authorization.now}).Error; err != nil {
		t.Fatalf("release recovery result fixture source lease: %v", err)
	}
	if err := authorization.db.Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("job_id = ? AND state = ?", job.ID, "active").
		Updates(map[string]any{"state": "released", "released_at": closedAt, "updated_at": authorization.now}).Error; err != nil {
		t.Fatalf("release recovery result fixture node lease: %v", err)
	}

	var items []model.BackupAssetRecoveryJobItem
	if err := authorization.db.Where("job_id = ?", job.ID).Order("ordinal ASC").Find(&items).Error; err != nil {
		t.Fatalf("load recovery result fixture items: %v", err)
	}
	publishable := make([]model.BackupAssetRecoveryJobItem, 0, len(items))
	for index := range items {
		outcome := "succeeded"
		bytesWritten := items[index].ExpectedPostBytes
		verifiedSize := items[index].ExpectedPostBytes
		verifiedDigest := items[index].ExpectedPostIdentityDigest
		if RecoveryOperationKind(items[index].OperationKind) == RecoveryOperationSkip {
			outcome = "skipped"
			bytesWritten = 0
			verifiedSize = items[index].ExpectedPriorBytes
			verifiedDigest = items[index].ExpectedPriorDigest
		}
		if err := authorization.db.Model(&model.BackupAssetRecoveryJobItem{}).
			Where("id = ? AND job_id = ?", items[index].ID, job.ID).
			Updates(map[string]any{
				"outcome": outcome, "bytes_written": bytesWritten, "verified_size": verifiedSize,
				"verified_digest": verifiedDigest, "updated_at": authorization.now,
			}).Error; err != nil {
			t.Fatalf("complete recovery result fixture item %d: %v", index, err)
		}
		items[index].Outcome = outcome
		items[index].BytesWritten = bytesWritten
		items[index].VerifiedSize = verifiedSize
		items[index].VerifiedDigest = verifiedDigest
		if outcome == "succeeded" && items[index].DisplayClass == string(RecoveryDisplayClassRegular) {
			publishable = append(publishable, items[index])
		}
	}
	if len(publishable) == 0 {
		t.Fatal("recovery result fixture has no publishable regular items")
	}

	jobRevision := job.TransitionRevision + 1
	if err := authorization.db.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", job.ID).
		Updates(map[string]any{
			"state": string(JobStateSucceeded), "failure_category": "",
			"transition_revision": jobRevision, "workspace_phase": string(WorkspacePhaseSealed),
			"workspace_marker_binding_digest": markerDigest, "workspace_owner": "result-test-worker",
			"workspace_fence":                           attempt.Fence,
			"workspace_marker_validation_attempt_id":    executed.AttemptID,
			"workspace_marker_validation_attempt_fence": attempt.Fence,
			"workspace_marker_validation_node_fence":    executed.NodeLeaseFence,
			"plaintext_deadline":                        deadline, "updated_at": authorization.now,
		}).Error; err != nil {
		t.Fatalf("seal recovery result fixture job: %v", err)
	}
	if err := authorization.db.Where("id = ?", job.ID).Take(&job).Error; err != nil {
		t.Fatalf("reload sealed recovery result fixture job: %v", err)
	}
	var plan model.BackupAssetRecoveryPlan
	if err := authorization.db.Where("id = ?", job.PlanID).Take(&plan).Error; err != nil {
		t.Fatalf("load recovery result fixture plan: %v", err)
	}
	encryptedTargetRootLocator, err := secure.EncryptString(plan.EncryptedTargetRootLocator)
	if err != nil {
		t.Fatalf("seal recovery result fixture target root: %v", err)
	}
	if err := authorization.db.Model(&model.BackupAssetRecoveryPlan{}).Where("id = ?", job.PlanID).
		UpdateColumn("encrypted_target_root_locator", encryptedTargetRootLocator).Error; err != nil {
		t.Fatalf("persist recovery result fixture target root: %v", err)
	}
	if err := authorization.db.Create(&model.BackupAssetRecoveryEvidence{
		ID: recoverySchemaUseLatchRowID, Kind: RecoverySchemaUseLatchID,
		CreatedAt: authorization.now, UpdatedAt: authorization.now,
	}).Error; err != nil {
		t.Fatalf("insert recovery result fixture schema-use latch: %v", err)
	}

	keyring, ok := authorization.dependencies.LocatorKeys.(*authorizationReceiptLocatorKeys)
	if !ok || keyring == nil {
		t.Fatalf("unexpected recovery result fixture keyring %T", authorization.dependencies.LocatorKeys)
	}
	fixture := &recoveryResultLifecycleFixture{
		db: authorization.db, now: authorization.now, job: job,
		requesterID: authorization.request.RequesterID, publishableRows: publishable,
	}
	fixture.nodeAdmission = &recoveryResultNodeAdmission{now: func() time.Time { return fixture.now }}
	fixture.contentLifecycle = &recoveryResultContentLifecycleFake{db: fixture.db}
	fixture.target = &recoveryCleanupValidationTargetFake{
		db: fixture.db, now: func() time.Time { return fixture.now },
	}
	fixture.service, err = NewResultLifecycleService(ResultLifecycleDependencies{
		DB: fixture.db, Now: func() time.Time { return fixture.now },
		WorkspaceKeys:       recoveryResultTestKeySource{material: keyring.material},
		DefaultPlaintextTTL: recoveryResultTestDefaultTTL, RetainHardCap: recoveryResultTestHardCap,
		NodeAdmission: fixture.nodeAdmission, ContentLifecycle: fixture.contentLifecycle,
		Target: fixture.target, CleanupLeaseTTL: recoveryResultCleanupTTL,
	})
	if err != nil {
		t.Fatalf("construct recovery result lifecycle service: %v", err)
	}
	return fixture
}

func (fixture *recoveryResultLifecycleFixture) publishRequest() PublishRecoveryResultsRequest {
	results := make([]PublishRecoveryResultInput, len(fixture.publishableRows))
	for index := range fixture.publishableRows {
		results[index] = PublishRecoveryResultInput{
			JobItemID: fixture.publishableRows[index].ID,
			Classification: RecoveryResultClassificationBinding{
				Kind: RecoveryResultClassificationUnknown, Revision: 1, SourceRevision: 1,
			},
		}
	}
	return PublishRecoveryResultsRequest{
		JobID: fixture.job.ID, ExpectedJobRevision: fixture.job.TransitionRevision, Results: results,
	}
}

func (fixture *recoveryResultLifecycleFixture) retainRequest(
	published PublishedRecoveryResultSet,
	requestedDeadline time.Time,
) RetainRecoveryResultsRequest {
	return RetainRecoveryResultsRequest{
		JobID: published.JobID, ExpectedJobRevision: published.JobRevision,
		RequestedDeadline: requestedDeadline,
		Actor:             content.DeliveryActor{UserID: fixture.requesterID, Role: "admin"},
		Permissions:       backupasset.PermissionSet{backupasset.PermissionBackupAssetsRecover: true},
		Proof: &content.StepUpProof{
			Action: auth.StepUpActionRecoveryResultRetain,
			ID:     strings.Repeat("b", 32), ExpiresAt: fixture.now.Add(5 * time.Minute),
		},
	}
}

func (fixture *recoveryResultLifecycleFixture) assertResultSetDeadline(
	t *testing.T,
	resultSetID string,
	want time.Time,
) {
	t.Helper()
	var resultSet model.BackupAssetRecoveryResultSet
	if err := fixture.db.Where("id = ?", resultSetID).Take(&resultSet).Error; err != nil {
		t.Fatalf("load recovery result-set deadline: %v", err)
	}
	if !resultSet.PlaintextDeadline.Equal(want) {
		t.Fatalf("result-set deadline = %s, want %s", resultSet.PlaintextDeadline, want)
	}
}

func (fixture *recoveryResultLifecycleFixture) setResultSetCleanupState(
	t *testing.T,
	published PublishedRecoveryResultSet,
	state ResultSetState,
) string {
	t.Helper()
	nodeLeaseID := strings.Repeat("c", 32)
	leaseExpiry := fixture.now.Add(5 * time.Minute)
	if err := fixture.db.Create(&model.BackupAssetRecoveryNodeLease{
		ID: nodeLeaseID, NodeID: fixture.job.TargetNodeID, HolderKind: "recovery_cleanup",
		JobID: fixture.job.ID, OwnerID: "retain-state-owner", Fence: 1, State: "active",
		LeaseExpiresAt: leaseExpiry, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}).Error; err != nil {
		t.Fatalf("create retain state cleanup lease: %v", err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).Where("id = ?", published.ResultSetID).
		Updates(map[string]any{
			"state": string(ResultSetStateRevoking), "cleanup_owner": "retain-state-owner",
			"cleanup_lease_expires_at": leaseExpiry, "cleanup_fence": 1,
			"node_lease_id": nodeLeaseID, "node_fence": 1, "cleanup_attempt": 1,
			"updated_at": fixture.now,
		}).Error; err != nil {
		t.Fatalf("move retain fixture to revoking: %v", err)
	}
	if state == ResultSetStateRevoking {
		return nodeLeaseID
	}
	phase := CleanupPhaseClaimed
	if state == ResultSetStateCleaned {
		phase = CleanupPhaseTombstoned
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).Where("id = ?", published.ResultSetID).
		Updates(map[string]any{
			"state": string(state), "cleanup_phase": string(phase), "cleanup_owner": "",
			"cleanup_lease_expires_at": nil, "node_lease_id": nil, "node_fence": 0,
			"updated_at": fixture.now,
		}).Error; err != nil {
		t.Fatalf("move retain fixture to %s: %v", state, err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).Where("id = ?", nodeLeaseID).
		Updates(map[string]any{"state": "released", "released_at": fixture.now, "updated_at": fixture.now}).Error; err != nil {
		t.Fatalf("release retain state cleanup lease: %v", err)
	}
	return nodeLeaseID
}

func (fixture *recoveryResultLifecycleFixture) maxNodeFence(t *testing.T) uint64 {
	t.Helper()
	var maxFence int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("node_id = ?", fixture.job.TargetNodeID).
		Select("COALESCE(MAX(fence), 0)").Scan(&maxFence).Error; err != nil {
		t.Fatalf("load maximum recovery node fence: %v", err)
	}
	if maxFence < 0 {
		t.Fatalf("negative recovery node fence: %d", maxFence)
	}
	return uint64(maxFence)
}

func (fixture *recoveryResultLifecycleFixture) assertCleanupClaimRow(
	t *testing.T,
	claim RecoveryResultCleanupClaim,
) {
	t.Helper()
	var resultSet model.BackupAssetRecoveryResultSet
	if err := fixture.db.Where("id = ? AND job_id = ?", claim.ResultSetID, claim.JobID).
		Take(&resultSet).Error; err != nil {
		t.Fatalf("load cleanup claim ResultSet: %v", err)
	}
	if resultSet.State != string(ResultSetStateRevoking) || resultSet.CleanupPhase != string(claim.Phase) ||
		resultSet.CleanupOwner != claim.WorkerID || resultSet.CleanupLeaseExpiresAt == nil ||
		!resultSet.CleanupLeaseExpiresAt.Equal(claim.LeaseExpiresAt) || resultSet.CleanupFence != claim.CleanupFence ||
		resultSet.NodeLeaseID == nil || *resultSet.NodeLeaseID != claim.NodeLeaseID ||
		resultSet.NodeFence != claim.NodeFence || resultSet.CleanupAttempt != claim.CleanupAttempt {
		t.Fatalf("cleanup claim row mismatch: row=%+v claim=%+v", resultSet, claim)
	}
}

func (fixture *recoveryResultLifecycleFixture) prepareWorkspaceCleanupDue(
	t *testing.T,
) model.BackupAssetRecoveryJob {
	t.Helper()
	fixture.updateJob(t, map[string]any{
		"workspace_phase": string(WorkspacePhaseCleanupDue), "updated_at": fixture.now,
	})
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", fixture.job.ID).Take(&job).Error; err != nil {
		t.Fatalf("load cleanup-due workspace job: %v", err)
	}
	return job
}

func (fixture *recoveryResultLifecycleFixture) setWorkspaceCleanupPhase(t *testing.T, phase CleanupPhase) {
	t.Helper()
	if err := fixture.db.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", fixture.job.ID).
		Updates(map[string]any{"workspace_cleanup_phase": string(phase), "updated_at": fixture.now}).Error; err != nil {
		t.Fatalf("set workspace cleanup phase %s: %v", phase, err)
	}
}

func (fixture *recoveryResultLifecycleFixture) releaseWorkspaceCleanupForRetry(
	t *testing.T,
	claim RecoveryWorkspaceCleanupClaim,
) {
	t.Helper()
	if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).Where("id = ?", claim.NodeLeaseID).
		Updates(map[string]any{"state": "released", "released_at": fixture.now, "updated_at": fixture.now}).Error; err != nil {
		t.Fatalf("release workspace cleanup node lease for retry: %v", err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", claim.JobID).
		Updates(map[string]any{
			"workspace_cleanup_owner": "", "workspace_cleanup_lease_expires_at": nil,
			"workspace_cleanup_node_lease_id": nil, "workspace_cleanup_node_fence": 0,
			"updated_at": fixture.now,
		}).Error; err != nil {
		t.Fatalf("project retryable workspace cleanup failure: %v", err)
	}
}

func (fixture *recoveryResultLifecycleFixture) assertWorkspaceCleanupClaim(
	t *testing.T,
	claim RecoveryWorkspaceCleanupClaim,
) {
	t.Helper()
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatalf("load workspace cleanup claim job: %v", err)
	}
	fixture.assertWorkspaceCleanupClaimRow(t, claim, job)
}

func (fixture *recoveryResultLifecycleFixture) assertWorkspaceCleanupClaimRow(
	t *testing.T,
	claim RecoveryWorkspaceCleanupClaim,
	job model.BackupAssetRecoveryJob,
) {
	t.Helper()
	if job.WorkspacePhase != string(WorkspacePhaseCleanupDue) ||
		job.WorkspaceCleanupPhase != string(claim.Phase) || job.WorkspaceCleanupOwner != claim.WorkerID ||
		job.WorkspaceCleanupLeaseExpiresAt == nil ||
		!job.WorkspaceCleanupLeaseExpiresAt.Equal(claim.LeaseExpiresAt) ||
		job.WorkspaceCleanupFence != claim.CleanupFence || job.WorkspaceCleanupNodeLeaseID == nil ||
		*job.WorkspaceCleanupNodeLeaseID != claim.NodeLeaseID ||
		job.WorkspaceCleanupNodeFence != claim.NodeFence || job.WorkspaceCleanupAttempt != claim.CleanupAttempt {
		t.Fatalf("workspace cleanup claim mismatch: job=%+v claim=%+v", job, claim)
	}
}

func (fixture *recoveryResultLifecycleFixture) assertWorkspaceCleanupLeaseCount(t *testing.T, want int64) {
	t.Helper()
	var count int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("job_id = ? AND holder_kind = ?", fixture.job.ID, "recovery_cleanup").Count(&count).Error; err != nil {
		t.Fatalf("count workspace cleanup node leases: %v", err)
	}
	if count != want {
		t.Fatalf("workspace cleanup node lease count = %d, want %d", count, want)
	}
}

func (fixture *recoveryResultLifecycleFixture) updateJob(t *testing.T, updates map[string]any) {
	t.Helper()
	if err := fixture.db.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", fixture.job.ID).
		Updates(updates).Error; err != nil {
		t.Fatalf("mutate recovery result fixture job: %v", err)
	}
}

func (fixture *recoveryResultLifecycleFixture) assertNoPublishedRows(t *testing.T) {
	t.Helper()
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", fixture.job.ID).Take(&job).Error; err != nil {
		t.Fatalf("reload rejected publication job: %v", err)
	}
	if job.WorkspacePhase == string(WorkspacePhasePublished) {
		t.Fatalf("rejected publication advanced workspace: %+v", job)
	}
	var resultSets, results int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryResultSet{}).
		Where("job_id = ?", fixture.job.ID).Count(&resultSets).Error; err != nil {
		t.Fatalf("count rejected result sets: %v", err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryResult{}).
		Where("job_id = ?", fixture.job.ID).Count(&results).Error; err != nil {
		t.Fatalf("count rejected results: %v", err)
	}
	if resultSets != 0 || results != 0 {
		t.Fatalf("rejected publication left result rows sets=%d results=%d", resultSets, results)
	}
}
