package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	assetexport "xirang/backend/internal/backupasset/export"
	"xirang/backend/internal/backupasset/overlay"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/backupasset/recovery"
	"xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/backupasset/retention"
	"xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/task"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type runtimeTransportFake struct{ marker int }

func (*runtimeTransportFake) Run(context.Context, provider.CommandInvocation, provider.OperationLimits) (provider.CommandOutput, error) {
	return provider.CommandOutput{}, nil
}
func (*runtimeTransportFake) Open(context.Context, provider.CommandInvocation, provider.OperationLimits, int64) (provider.ReadHandle, error) {
	return nil, fmt.Errorf("not used")
}
func (*runtimeTransportFake) OpenExecution(context.Context, provider.CommandInvocation, provider.OperationLimits, int64) (provider.CommandExecution, error) {
	return &runtimeExecutionFake{Reader: strings.NewReader("")}, nil
}

type runtimeExecutionFake struct{ io.Reader }

func (*runtimeExecutionFake) Join() (provider.CommandCompletion, error) {
	return provider.CommandCompletion{ExitCode: 0, ExitCodeKnown: true}, nil
}
func (*runtimeExecutionFake) Cancel() error { return nil }

type runtimeStagedPayloadFake struct{}

type runtimeRecoverySourceNamespaceAuthorityFake struct {
	request     recovery.RecoverySourceNamespaceRequest
	pinned      provider.RsyncRestoreSource
	observation *recovery.RecoverySourceNamespaceObservation
	err         error
}

func (fake *runtimeRecoverySourceNamespaceAuthorityFake) ObserveRecoverySourceNamespace(
	_ context.Context,
	request recovery.RecoverySourceNamespaceRequest,
	pinned provider.RsyncRestoreSource,
) (*recovery.RecoverySourceNamespaceObservation, error) {
	fake.request = request
	fake.pinned = pinned
	return fake.observation, fake.err
}

func (*runtimeStagedPayloadFake) Stage(context.Context, provider.RemoteCommandAccess, provider.StagedPayloadRequest) (provider.StagedPayloadRef, error) {
	return provider.StagedPayloadRef{}, fmt.Errorf("not used")
}
func (*runtimeStagedPayloadFake) Cleanup(context.Context, provider.RemoteCommandAccess, provider.StagedPayloadRef) error {
	return nil
}
func (*runtimeStagedPayloadFake) CleanupAged(context.Context, provider.RemoteCommandAccess, time.Duration, int) error {
	return nil
}

type runtimeStopTerminalizerPass struct {
	progress assetexport.RuntimeStopTerminalizationProgress
	err      error
}

type runtimeStopTerminalizerFake struct {
	passes []runtimeStopTerminalizerPass
	calls  int
}

func (fake *runtimeStopTerminalizerFake) TerminalizeForRuntimeStopPass(
	context.Context,
	int,
) (assetexport.RuntimeStopTerminalizationProgress, error) {
	if fake.calls >= len(fake.passes) {
		return assetexport.RuntimeStopTerminalizationProgress{}, assetexport.ErrUnavailable
	}
	pass := fake.passes[fake.calls]
	fake.calls++
	return pass.progress, pass.err
}

var _ provider.CommandTransport = (*runtimeTransportFake)(nil)
var _ provider.CommandStreamTransport = (*runtimeTransportFake)(nil)

type runtimeSessionRevocationsFake struct {
	revoked bool
	err     error
}

func (fake *runtimeSessionRevocationsFake) IsSessionRevoked(string) (bool, error) {
	return fake.revoked, fake.err
}

type runtimeContentMetricsFake struct {
	content.NoopMetrics
	cache map[content.MetricCacheOutcome]int
}

func (fake *runtimeContentMetricsFake) ObserveCache(outcome content.MetricCacheOutcome) {
	fake.cache[outcome]++
}

func openRuntimeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&model.SystemSetting{}, &model.BackupAssetDeliveryGrant{}, &model.BackupAssetDeliveryRequest{},
		&model.BackupAssetDeliveryUsage{}, &model.RecoveryPointLease{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRuntimeNewReconcilesCurrentPostArmWorkBeforePermanentCleanupKeyFailure(t *testing.T) {
	testCases := []struct {
		name     string
		keyState backupasset.DomainKeyState
		want     error
	}{
		{name: "lost", keyState: backupasset.DomainKeyLost, want: backupasset.ErrKeyLost},
		{name: "unavailable", keyState: backupasset.DomainKeyVerifyOnly, want: backupasset.ErrKeyUnavailable},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			db := openRuntimeTestDB(t)
			now := time.Now().UTC().Truncate(time.Second)
			fixture := seedRuntimeCleanupKeyCurrentWork(t, db, now)
			seedRuntimeCleanupKeyState(t, db, now, testCase.keyState)

			_, err := New(runtimeCleanupKeyDependencies(db, now))
			if !errors.Is(err, testCase.want) {
				t.Fatalf("runtime cleanup-key startup error=%v, want %v", err, testCase.want)
			}
			job := loadRuntimeCleanupKeyJob(t, db, fixture.jobID)
			if job.State != "needs_attention" || job.FailureCategory != "cleanup_key_unavailable" ||
				job.WorkspacePhase != "cleanup_due" || job.TransitionRevision != 8 ||
				job.TargetChainRevision != fixture.targetChainRevision {
				t.Fatalf("runtime cleanup-key reconciliation job=%+v", job)
			}
			attempt := loadRuntimeCleanupKeyAttempt(t, db, fixture.attemptID)
			if attempt.State != "failed" || !attempt.MutationArmed || attempt.ClosedAt == nil ||
				!attempt.ClosedAt.Equal(now) || attempt.OwnerID != fixture.ownerID || attempt.Fence != fixture.fence {
				t.Fatalf("runtime cleanup-key reconciliation attempt=%+v", attempt)
			}
		})
	}
}

func TestRuntimeNewDoesNotReconcileTransientCleanupKeyStartupFailure(t *testing.T) {
	db := openRuntimeTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	fixture := seedRuntimeCleanupKeyCurrentWork(t, db, now)
	transientErr := errors.New("injected transient keyring query failure")
	const callbackName = "runtime:transient_cleanup_key_query"
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.WrappedDomainKey{}).TableName() {
			_ = tx.AddError(transientErr)
		}
	}); err != nil {
		t.Fatalf("register transient keyring failure: %v", err)
	}
	_, err := New(runtimeCleanupKeyDependencies(db, now))
	if removeErr := db.Callback().Query().Remove(callbackName); removeErr != nil {
		t.Fatalf("remove transient keyring failure: %v", removeErr)
	}
	if !errors.Is(err, transientErr) || errors.Is(err, backupasset.ErrKeyLost) || errors.Is(err, backupasset.ErrKeyUnavailable) {
		t.Fatalf("runtime transient cleanup-key error=%v", err)
	}
	job := loadRuntimeCleanupKeyJob(t, db, fixture.jobID)
	attempt := loadRuntimeCleanupKeyAttempt(t, db, fixture.attemptID)
	if job.State != "running" || job.FailureCategory != "" || job.WorkspacePhase != "reserved" ||
		job.TransitionRevision != 7 || attempt.State != "running" || attempt.ClosedAt != nil {
		t.Fatalf("transient cleanup-key failure reconciled durable work: job=%+v attempt=%+v", job, attempt)
	}
}

func TestRuntimeNewPreservesPermanentCleanupKeyErrorWhenReconciliationFails(t *testing.T) {
	db := openRuntimeTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	seedRuntimeCleanupKeyCurrentWork(t, db, now)
	seedRuntimeCleanupKeyState(t, db, now, backupasset.DomainKeyLost)
	_, wantErr := backupasset.NewKeyring(db, func() time.Time { return now }).
		Ensure(context.Background(), backupasset.KeyDomainRecoveryCleanupOwnership)
	if !errors.Is(wantErr, backupasset.ErrKeyLost) {
		t.Fatalf("probe permanent cleanup-key error: %v", wantErr)
	}
	if err := db.Migrator().DropTable(&model.BackupAssetRecoveryAttempt{}); err != nil {
		t.Fatalf("drop attempt table for reconciliation failure: %v", err)
	}

	_, err := New(runtimeCleanupKeyDependencies(db, now))
	if !errors.Is(err, backupasset.ErrKeyLost) {
		t.Fatalf("runtime hid original permanent cleanup-key error: %v", err)
	}
	if err.Error() != wantErr.Error() || errors.Is(err, recovery.ErrRecoveryWorkerUnavailable) {
		t.Fatalf("runtime replaced or joined the original cleanup-key error: got=%v want=%v", err, wantErr)
	}
}

func TestRuntimeNewReturnsExactLostCleanupKeyErrorAfterConfiguredBoundedReconciliation(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RUNTIME_RECOVERY_CLEANUP_LOST_KEK_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db := openRuntimeTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	first := seedRuntimeCleanupKeyCurrentWorkAt(t, db, now.Add(-time.Second), 0)
	second := seedRuntimeCleanupKeyCurrentWorkAt(t, db, now, 1)
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.recovery.receipt_reaper_batch_size", "1"); err != nil {
		t.Fatalf("configure cleanup-key reconciliation bound: %v", err)
	}
	ring := backupasset.NewKeyring(db, func() time.Time { return now })
	material, err := ring.Ensure(context.Background(), backupasset.KeyDomainRecoveryCleanupOwnership)
	if err != nil {
		t.Fatalf("ensure cleanup key: %v", err)
	}
	if err := ring.MarkLost(context.Background(), backupasset.KeyDomainRecoveryCleanupOwnership, material.Version); err != nil {
		t.Fatalf("mark cleanup key lost: %v", err)
	}
	_, wantErr := ring.Ensure(context.Background(), backupasset.KeyDomainRecoveryCleanupOwnership)
	if !errors.Is(wantErr, backupasset.ErrKeyLost) {
		t.Fatalf("probe lost cleanup-key error: %v", wantErr)
	}

	_, err = New(runtimeCleanupKeyDependencies(db, now))
	if !errors.Is(err, backupasset.ErrKeyLost) || err.Error() != wantErr.Error() {
		t.Fatalf("runtime cleanup-key error=%v, want exact %v", err, wantErr)
	}
	firstJob := loadRuntimeCleanupKeyJob(t, db, first.jobID)
	firstAttempt := loadRuntimeCleanupKeyAttempt(t, db, first.attemptID)
	if firstJob.State != "needs_attention" || firstJob.FailureCategory != "cleanup_key_unavailable" ||
		firstJob.WorkspacePhase != "cleanup_due" || firstAttempt.State != "failed" || firstAttempt.ClosedAt == nil {
		t.Fatalf("runtime did not reconcile first bounded cleanup-key row: job=%+v attempt=%+v", firstJob, firstAttempt)
	}
	secondJob := loadRuntimeCleanupKeyJob(t, db, second.jobID)
	secondAttempt := loadRuntimeCleanupKeyAttempt(t, db, second.attemptID)
	if secondJob.State != "running" || secondJob.FailureCategory != "" || secondJob.WorkspacePhase != "reserved" ||
		secondAttempt.State != "running" || secondAttempt.ClosedAt != nil {
		t.Fatalf("runtime exceeded configured cleanup-key reconciliation bound: job=%+v attempt=%+v", secondJob, secondAttempt)
	}
}

func TestRuntimeNewReturnsExactUnavailableCleanupKeyErrorAfterReconciliation(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RUNTIME_RECOVERY_CLEANUP_OLD_KEK_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db := openRuntimeTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	fixture := seedRuntimeCleanupKeyCurrentWork(t, db, now)
	ring := backupasset.NewKeyring(db, func() time.Time { return now })
	if _, err := ring.Ensure(context.Background(), backupasset.KeyDomainRecoveryCleanupOwnership); err != nil {
		t.Fatalf("ensure cleanup key under old master key: %v", err)
	}
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RUNTIME_RECOVERY_CLEANUP_NEW_KEK_FOR_TEST_ONLY")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	_, wantErr := ring.Ensure(context.Background(), backupasset.KeyDomainRecoveryCleanupOwnership)
	if !errors.Is(wantErr, backupasset.ErrKeyUnavailable) {
		t.Fatalf("probe unavailable cleanup-key error: %v", wantErr)
	}

	_, err := New(runtimeCleanupKeyDependencies(db, now))
	if !errors.Is(err, backupasset.ErrKeyUnavailable) || err.Error() != wantErr.Error() {
		t.Fatalf("runtime cleanup-key error=%v, want exact %v", err, wantErr)
	}
	job := loadRuntimeCleanupKeyJob(t, db, fixture.jobID)
	attempt := loadRuntimeCleanupKeyAttempt(t, db, fixture.attemptID)
	if job.State != "needs_attention" || job.FailureCategory != "cleanup_key_unavailable" ||
		job.WorkspacePhase != "cleanup_due" || attempt.State != "failed" || attempt.ClosedAt == nil {
		t.Fatalf("runtime did not reconcile unavailable cleanup-key row: job=%+v attempt=%+v", job, attempt)
	}
}

type runtimeCleanupKeyFixture struct {
	jobID               string
	attemptID           string
	ownerID             string
	fence               uint64
	targetChainRevision string
}

type runtimeCleanupKeyJobSnapshot struct {
	State                 string
	FailureCategory       string
	TransitionRevision    uint64
	WorkspacePhase        string
	TargetChainRevision   string
	EncryptedWorkspaceRaw string `gorm:"column:encrypted_workspace_relative_locator"`
}

type runtimeCleanupKeyAttemptSnapshot struct {
	OwnerID       string
	Fence         uint64
	State         string
	MutationArmed bool
	ClosedAt      *time.Time
}

func runtimeCleanupKeyDependencies(db *gorm.DB, now time.Time) Dependencies {
	transport := &runtimeTransportFake{}
	return Dependencies{
		DB: db, Settings: settings.NewService(db), Now: func() time.Time { return now },
		Transport: transport, StreamTransport: transport, Metrics: publication.NoopMetrics{},
		ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{},
	}
}

func seedRuntimeCleanupKeyCurrentWork(t *testing.T, db *gorm.DB, now time.Time) runtimeCleanupKeyFixture {
	t.Helper()
	return seedRuntimeCleanupKeyCurrentWorkAt(t, db, now, 0)
}

func seedRuntimeCleanupKeyCurrentWorkAt(
	t *testing.T,
	db *gorm.DB,
	now time.Time,
	index int,
) runtimeCleanupKeyFixture {
	t.Helper()
	if err := db.AutoMigrate(
		&model.WrappedDomainKey{}, &model.BackupAssetRecoveryPlan{}, &model.BackupAssetRecoveryJob{},
		&model.BackupAssetRecoveryAttempt{}, &model.BackupAssetRecoveryNodeLease{},
	); err != nil {
		t.Fatalf("migrate runtime cleanup-key fixture: %v", err)
	}
	fixture := runtimeCleanupKeyFixture{
		jobID: fmt.Sprintf("%032x", index+1), attemptID: fmt.Sprintf("%032x", index+101),
		ownerID: "runtime-cleanup-key-owner", fence: 11, targetChainRevision: "opaque-target-chain-before-key-loss",
	}
	plan := model.BackupAssetRecoveryPlan{
		ID: fmt.Sprintf("%032x", index+201), RecoveryPointID: fmt.Sprintf("%032x", index+301),
		State: "executed", TransitionRevision: 4, CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute),
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&plan).Error; err != nil {
		t.Fatalf("create runtime cleanup-key plan: %v", err)
	}
	job := model.BackupAssetRecoveryJob{
		ID: fixture.jobID, PlanID: plan.ID, State: "running", TransitionRevision: 7,
		WorkspacePhase: "reserved", EncryptedWorkspaceRelativeLocator: "ciphertext-must-not-be-read",
		WorkspaceMarkerBindingDigest: strings.Repeat("a", 64), WorkspaceOwner: fixture.ownerID,
		WorkspaceFence: fixture.fence, TargetMode: "isolated", TargetNodeID: uint(index + 41),
		TargetChainRevision: fixture.targetChainRevision, CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute),
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&job).Error; err != nil {
		t.Fatalf("create runtime cleanup-key job: %v", err)
	}
	expiresAt := now.Add(10 * time.Minute)
	attempt := model.BackupAssetRecoveryAttempt{
		ID: fixture.attemptID, JobID: fixture.jobID, OwnerID: fixture.ownerID, Fence: fixture.fence,
		State: "running", MutationArmed: true, LeaseExpiresAt: &expiresAt, HeartbeatAt: &now,
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute),
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&attempt).Error; err != nil {
		t.Fatalf("create runtime cleanup-key attempt: %v", err)
	}
	attemptID := fixture.attemptID
	node := model.BackupAssetRecoveryNodeLease{
		ID: fmt.Sprintf("%032x", index+401), NodeID: uint(index + 41), HolderKind: "recovery_job",
		JobID: fixture.jobID, AttemptID: &attemptID, OwnerID: fixture.ownerID, Fence: fixture.fence,
		State: "active", LeaseExpiresAt: expiresAt, CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute),
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&node).Error; err != nil {
		t.Fatalf("create runtime cleanup-key node lease: %v", err)
	}
	source := model.RecoveryPointLease{
		ID: fmt.Sprintf("%032x", index+501), RecoveryPointID: plan.RecoveryPointID,
		HolderType: string(backupasset.LeaseHolderRecoveryJob), OwnerID: fixture.jobID,
		AttemptID: fixture.attemptID, FenceToken: strings.Repeat("7", 64), Status: string(backupasset.LeaseActive),
		LeaseExpiresAt: expiresAt, AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now,
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute),
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&source).Error; err != nil {
		t.Fatalf("create runtime cleanup-key source lease: %v", err)
	}
	return fixture
}

func seedRuntimeCleanupKeyState(
	t *testing.T,
	db *gorm.DB,
	now time.Time,
	state backupasset.DomainKeyState,
) {
	t.Helper()
	row := model.WrappedDomainKey{
		ID: strings.Repeat("8", 32), Domain: string(backupasset.KeyDomainRecoveryCleanupOwnership),
		Version: 1, State: string(state), WrappedKey: "unreadable-key-material",
		WrapAlgorithm: "test", WrappingKeyFingerprint: strings.Repeat("9", 64),
		ActivatedAt: now.Add(-time.Hour), CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	if state == backupasset.DomainKeyLost {
		row.LostAt = &now
	} else {
		verifyUntil := now.Add(time.Hour)
		row.VerifyUntil = &verifyUntil
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create runtime cleanup-key state %q: %v", state, err)
	}
}

func loadRuntimeCleanupKeyJob(t *testing.T, db *gorm.DB, jobID string) runtimeCleanupKeyJobSnapshot {
	t.Helper()
	var snapshot runtimeCleanupKeyJobSnapshot
	result := db.Table("backup_asset_recovery_jobs").
		Select(`state, failure_category, transition_revision, workspace_phase,
			target_chain_revision, encrypted_workspace_relative_locator`).
		Where("id = ?", jobID).Limit(1).Scan(&snapshot)
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("load runtime cleanup-key job: rows=%d err=%v", result.RowsAffected, result.Error)
	}
	return snapshot
}

func loadRuntimeCleanupKeyAttempt(
	t *testing.T,
	db *gorm.DB,
	attemptID string,
) runtimeCleanupKeyAttemptSnapshot {
	t.Helper()
	var snapshot runtimeCleanupKeyAttemptSnapshot
	result := db.Table("backup_asset_recovery_attempts").
		Select("owner_id, fence, state, mutation_armed, closed_at").
		Where("id = ?", attemptID).Limit(1).Scan(&snapshot)
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("load runtime cleanup-key attempt: rows=%d err=%v", result.RowsAffected, result.Error)
	}
	return snapshot
}

func TestTerminalizeExportRuntimeLifecycleContinuesAfterDurableCleanupFailure(t *testing.T) {
	cleanupErr := errors.New("persistent runtime-stop cleanup failure")
	terminalizer := &runtimeStopTerminalizerFake{passes: []runtimeStopTerminalizerPass{
		{
			progress: assetexport.RuntimeStopTerminalizationProgress{Advanced: 32},
			err:      cleanupErr,
		},
		{
			progress: assetexport.RuntimeStopTerminalizationProgress{Processed: 1, Advanced: 2, Complete: true},
			err:      cleanupErr,
		},
	}}
	orphans := 0
	err := terminalizeExportRuntimeLifecycle(
		context.Background(), terminalizer, 1,
		func(context.Context) (int, error) {
			orphans++
			return 0, nil
		},
	)
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("terminalization error=%v, want persistent cleanup error", err)
	}
	if terminalizer.calls != 2 {
		t.Fatalf("terminalization passes=%d, want durable follow-up pass", terminalizer.calls)
	}
	if orphans != 1 {
		t.Fatalf("orphan reconciliation calls=%d, want one after the finite terminalization sweep", orphans)
	}
}

func TestTerminalizeExportRuntimeLifecycleBoundsRetryableSweepContention(t *testing.T) {
	terminalizer := &runtimeStopTerminalizerFake{passes: []runtimeStopTerminalizerPass{{
		err: assetexport.ErrUnavailable,
	}}}
	orphans := 0
	err := terminalizeExportRuntimeLifecycle(
		context.Background(), terminalizer, 1,
		func(context.Context) (int, error) {
			orphans++
			return 0, nil
		},
	)
	if !errors.Is(err, assetexport.ErrUnavailable) {
		t.Fatalf("contended runtime-stop error=%v, want retryable unavailability", err)
	}
	if terminalizer.calls != 1 {
		t.Fatalf("contended runtime-stop passes=%d, want one bounded attempt", terminalizer.calls)
	}
	if orphans != 0 {
		t.Fatalf("orphan reconciliation calls=%d, want none before lifecycle sweep completion", orphans)
	}
}

func TestManagedRecoverySourceNamespaceAdapterTranslatesRepositoryHandoffExactly(t *testing.T) {
	pinned := &managedRecoveryDeclaredSourceFake{}
	observation := &recovery.RecoverySourceNamespaceObservation{}
	authority := &runtimeRecoverySourceNamespaceAuthorityFake{observation: observation}
	adapter := &managedRecoverySourceNamespaceAdapter{authority: authority}
	request := repository.RecoverySourceNamespaceRequest{
		SourceRef: provider.RsyncRestoreSourceRef{
			PlanID: strings.Repeat("1", 32), PlanBindingDigest: strings.Repeat("2", 64),
			RepositoryID: strings.Repeat("3", 32), RecoveryPointID: strings.Repeat("4", 32),
			CatalogGenerationID: strings.Repeat("5", 32), SelectionDigest: strings.Repeat("6", 64),
			SourceRevisionDigest: strings.Repeat("7", 64), ManifestDigest: strings.Repeat("8", 64),
		},
		ProducingTaskID: 41, RepositoryBindingRevision: "binding-revision-1",
		ProvenanceRevision: "provenance-revision-1",
	}

	got, err := adapter.ObserveRecoverySourceNamespace(context.Background(), request, pinned)
	if err != nil {
		t.Fatalf("translate Repository source namespace handoff: %v", err)
	}
	if got != observation || authority.pinned != pinned {
		t.Fatalf("adapter ownership transfer got=%T pinned=%T", got, authority.pinned)
	}
	want := recovery.RecoverySourceNamespaceRequest{
		SourceRef: request.SourceRef, ProducingTaskID: request.ProducingTaskID,
		RepositoryBindingRevision: request.RepositoryBindingRevision,
		ProvenanceRevision:        request.ProvenanceRevision,
	}
	if authority.request != want {
		t.Fatalf("translated source namespace request=%+v, want exact scalar handoff", authority.request)
	}
}

func TestRuntimeSearchExposesOneRepositoryPublicationLineageAndWorkerGraph(t *testing.T) {
	db := openRuntimeTestDB(t)
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settings.NewService(db), Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{},
		Metrics:       publication.NoopMetrics{}, ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{},
		Now: func() time.Time { return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("construct runtime: %v", err)
	}
	if runtime.FoundationService() == nil || runtime.RepositoryService() == nil || runtime.PublicationCoordinator() == nil || runtime.healthWorker == nil ||
		runtime.PublicationReconciler() == nil || runtime.ResticPublicationStrategy() == nil ||
		runtime.RsyncTreePublicationStrategy() == nil || runtime.RclonePublicationStrategy() == nil ||
		runtime.LineageGuard() == nil || runtime.LegacyBlockRecorder() == nil || runtime.FeatureTransitioner() == nil ||
		runtime.CatalogService() == nil || runtime.CatalogAuditSink() == nil || runtime.catalogIndexer == nil || runtime.catalogWorker == nil {
		t.Fatal("runtime omitted a required shared graph port")
	}
	if runtime.recoverySourceNamespace == nil {
		t.Fatal("runtime omitted the production Recovery source-namespace authority")
	}
	if runtime.SearchService() == nil || runtime.OverlayService() == nil || runtime.ContentIndexIngest() == nil || runtime.searchIndexer == nil || runtime.searchWorker == nil {
		t.Fatal("runtime omitted the Search/Overlay graph")
	}
	searchService := reflect.ValueOf(runtime.SearchService()).Elem()
	if searchService.FieldByName("excerpts").IsNil() || searchService.FieldByName("malwareSafety").IsNil() {
		t.Fatal("runtime omitted the production Search excerpt or malware release dependency")
	}
	contentBroker := runtime.ContentBroker()
	if contentBroker == nil || contentBroker != runtime.contentBroker || runtime.contentManager == nil ||
		runtime.contentBudget == nil || runtime.contentAudit == nil || runtime.contentReconciler == nil || runtime.contentReady == nil {
		t.Fatal("runtime omitted or duplicated the Content graph")
	}
	if runtime.RecoveryAuthorization() == nil || runtime.RecoveryAuthorization() != runtime.recoveryAuthorization ||
		runtime.RecoveryResults() == nil || runtime.RecoveryResults() != runtime.recoveryResults {
		t.Fatal("runtime omitted or duplicated the narrow Recovery facades")
	}
	contentBrokerValue := reflect.ValueOf(contentBroker).Elem()
	if contentBrokerValue.FieldByName("recoveryAuthorize").IsNil() || contentBrokerValue.FieldByName("recoverySource").IsNil() {
		t.Fatal("Content Broker omitted the managed Recovery result facade")
	}
	if config, configErr := runtime.ContentConfig(); configErr != nil || config.Enabled {
		t.Fatalf("default Content config=%+v err=%v, want disabled", config, configErr)
	}
	if runtime.processingManager == nil {
		t.Fatal("runtime omitted the shared Processing manager")
	}
	repositoryService := reflect.ValueOf(runtime.RepositoryService()).Elem()
	if repositoryService.FieldByName("catalogRebuild").IsNil() || repositoryService.FieldByName("derivedBackfill").IsNil() {
		t.Fatal("production runtime omitted CatalogRebuild / DerivedBackfill ports")
	}
	if runtime.archiveMemberService == nil {
		t.Fatal("runtime omitted the one-hop archive-member service")
	}
	if runtime.NodeWriteCoordinator() == nil || runtime.NodeWriteCoordinator() != runtime.nodeWriteCoordinator {
		t.Fatal("runtime omitted or duplicated the shared Task/Recovery node-write coordinator")
	}
	if runtime.exportManager == nil {
		t.Fatal("runtime omitted the managed Export graph")
	}
	exportManager, ok := runtime.exportManager.(*managedExportRuntime)
	if !ok || exportManager.Ready() || exportManager.Service() == nil || exportManager.Delivery() == nil ||
		exportManager.ArchiveMember() == nil || exportManager.publication.current() != nil {
		t.Fatal("default-disabled runtime did not expose stable unpublished Export facades")
	}
	if _, ok := runtime.processingManager.source.(*content.DerivedAttemptSourceResolver); !ok {
		t.Fatalf("Processing Worker Input source=%T, want Derived-first closed resolver", runtime.processingManager.source)
	}
	if config, configErr := runtime.ProcessingConfig(); configErr != nil || config.Enabled || config.LocalWorker.Enabled || config.RemoteWorker.Enabled {
		t.Fatalf("default Processing config=%+v err=%v, want disabled and unconfigured", config, configErr)
	}
	if runtime.WorkerProtocol() != nil {
		t.Fatal("default-disabled runtime exposed the Worker protocol")
	}
	if summary, summaryErr := runtime.ProcessingAdminSummary(context.Background()); summaryErr != nil || summary.Configured || summary.Queue.Total != 0 {
		t.Fatalf("default Processing summary=%+v err=%v, want quiet", summary, summaryErr)
	}
	if _, err := runtime.CatalogService().GetRecoveryPoint(context.Background(), strings.Repeat("f", 32), catalog.AuthorizationScope{Role: "admin", UserID: 1}); !errors.Is(err, catalog.ErrFeatureDisabled) {
		t.Fatalf("default-disabled runtime Catalog error=%v", err)
	}
	if runtime.ResticPublicationStrategy().Kind() != backupasset.ProviderRestic {
		t.Fatalf("publication strategy kind=%q, want %q", runtime.ResticPublicationStrategy().Kind(), backupasset.ProviderRestic)
	}
	if runtime.RsyncTreePublicationStrategy().Kind() != backupasset.ProviderRsync {
		t.Fatalf("publication strategy kind=%q, want %q", runtime.RsyncTreePublicationStrategy().Kind(), backupasset.ProviderRsync)
	}
	if runtime.RclonePublicationStrategy().Kind() != backupasset.ProviderRclone {
		t.Fatalf("publication strategy kind=%q, want %q", runtime.RclonePublicationStrategy().Kind(), backupasset.ProviderRclone)
	}
}

func TestRecoveryProductionAuthorityCompositionOwnsTargetRootFacade(t *testing.T) {
	db := openRuntimeTestDB(t)
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settings.NewService(db), Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{},
		ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{},
		Now: func() time.Time { return time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("construct production runtime: %v", err)
	}
	facade, ok := runtime.RecoveryTargetRoots().(*managedRecoveryTargetRootFacade)
	if !ok || facade == nil || facade != runtime.recoveryTargetRoots || facade.service == nil || facade.runtime == nil {
		t.Fatal("production Recovery composition omitted the narrow TargetRootAuthorityService facade")
	}
}

func TestRecoveryRuntimeTargetRootFacadeReturnsOnlySafeSummarySurface(t *testing.T) {
	authorityType := reflect.TypeOf((*RecoveryTargetRootAuthority)(nil)).Elem()
	register, ok := authorityType.MethodByName("Register")
	if !ok {
		t.Fatal("Recovery target-root facade has no Register operation")
	}
	safeType := reflect.TypeOf(settings.RecoveryTargetRootSummary{})
	if got := register.Type.Out(0); got != safeType {
		t.Fatalf("Register result type=%v, want reviewed safe summary %v", got, safeType)
	}
	summary := settings.RecoveryTargetRootSummary{
		NodeID: 7, RootID: "root-safe", SafeLabel: "safe label",
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	formatted := fmt.Sprintf("%v %+v %#v", summary, summary, summary)
	for _, forbidden := range []string{
		"locator", "digest", "revision", "reserve", "policy", "overlap",
	} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) ||
			strings.Contains(strings.ToLower(formatted), forbidden) {
			t.Fatalf("safe target-root summary exposed private field %q: json=%s formatted=%s", forbidden, encoded, formatted)
		}
	}
}

func TestRecoveryTargetRootFacadeAuditsClosedProductsAfterSuccessfulTransition(t *testing.T) {
	events := []string{}
	audit := &runtimeRecoveryAdministrationAuditSpy{err: errors.New("FAKE_AUDIT_FAILURE_FOR_TEST_ONLY")}
	facade := &managedRecoveryTargetRootFacade{
		service: &managedRecoveryTargetRootMutationServiceFake{events: &events},
		runtime: runtimeRecoveryImmediateTransitionFake{},
		audit:   audit,
	}
	for _, mutation := range []recovery.TargetRootMutation{
		recovery.TargetRootMutationRegister, recovery.TargetRootMutationRotate,
	} {
		if _, err := facade.Register(context.Background(), recovery.TargetRootRegistrationRequest{
			Mutation: mutation, RequesterID: 17, NodeID: 1, RootID: "root-a", SafeLabel: "safe",
			Locator: "/srv/FAKE_PRIVATE_RECOVERY_ROOT_FOR_TEST_ONLY",
			Policy:  settings.RecoveryTargetRootPolicy{OverlapPolicyBinding: "strict"},
		}); err != nil {
			t.Fatalf("%s changed committed result on audit failure: %v", mutation, err)
		}
	}
	if _, err := facade.DeleteAuthorized(context.Background(), recovery.TargetRootDeletionRequest{
		Mutation: recovery.TargetRootMutationDelete, RequesterID: 17, NodeID: 1, RootID: "root-a",
	}); err != nil {
		t.Fatalf("delete changed committed result on audit failure: %v", err)
	}
	if _, err := facade.List(context.Background(), 17, 1); err != nil {
		t.Fatalf("list changed result on audit failure: %v", err)
	}
	if len(audit.inputs) != 4 {
		t.Fatalf("administration audits=%d want=4", len(audit.inputs))
	}
	wantOperations := []string{"target_root_register", "target_root_rotate", "target_root_delete", "target_root_list"}
	for index, input := range audit.inputs {
		if input.Action != backupasset.AuditActionRecoveryAdministration || input.Actor.UserID != 17 ||
			input.Fields[backupasset.AuditFieldOperation] != wantOperations[index] ||
			strings.Contains(fmt.Sprintf("%+v", input), "FAKE_PRIVATE_RECOVERY_ROOT") {
			t.Fatalf("administration audit[%d]=%+v", index, input)
		}
	}
}

type runtimeRecoveryImmediateTransitionFake struct{}

func (runtimeRecoveryImmediateTransitionFake) TransitionCurrentWithRestore(
	_ context.Context,
	mutate func() error,
	_ func() error,
) error {
	return mutate()
}

type runtimeRecoveryAdministrationAuditSpy struct {
	inputs []backupasset.AuditEventInput
	err    error
}

func (spy *runtimeRecoveryAdministrationAuditSpy) Write(
	_ context.Context,
	input backupasset.AuditEventInput,
) (model.BackupAssetAuditEvent, error) {
	event, err := backupasset.NewAuditEvent(input)
	if err != nil {
		return model.BackupAssetAuditEvent{}, err
	}
	spy.inputs = append(spy.inputs, event.AuditEventInput)
	return model.BackupAssetAuditEvent{}, spy.err
}

func TestRecoveryRuntimeTargetRootFacadeOwnsRegisterRotateDeleteTransitionsAndRestoration(t *testing.T) {
	for _, operation := range []string{"register", "rotate", "delete", "delete-authorized"} {
		t.Run(operation, func(t *testing.T) {
			events := make([]string, 0, 16)
			service := &managedRecoveryTargetRootMutationServiceFake{events: &events}
			manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
				Build: func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
					events = append(events, "construct")
					return &managedRecoveryGraph{
						reconcileMetadata: func(context.Context) error {
							events = append(events, "reconcile")
							return nil
						},
						stopClaims: func() { events = append(events, "stop") },
						shutdownLifecycle: func(context.Context) error {
							events = append(events, "drain")
							return nil
						},
					}, nil
				},
				Install: func(publication *managedRecoveryPublication, graph *managedRecoveryGraph) error {
					events = append(events, "install")
					return publication.publish(graph)
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.StartupWithConfig(context.Background(), backupasset.RecoveryConfig{Enabled: true}); err != nil {
				t.Fatal(err)
			}
			events = events[:0]
			facade := &managedRecoveryTargetRootFacade{service: service, runtime: manager}
			switch operation {
			case "register", "rotate":
				_, err = facade.Register(context.Background(), recovery.TargetRootRegistrationRequest{
					NodeID: 1, RootID: "root-a", SafeLabel: operation,
					Locator: "/srv/" + operation,
					Policy:  settings.RecoveryTargetRootPolicy{OverlapPolicyBinding: "strict"},
				})
			case "delete":
				err = facade.Delete(context.Background(), 1, "root-a")
			case "delete-authorized":
				_, err = facade.DeleteAuthorized(context.Background(), recovery.TargetRootDeletionRequest{
					Mutation: recovery.TargetRootMutationDelete, RequesterID: 1,
					Endpoint:       "/api/v1/settings/backup-assets/recovery/target-roots/:nodeId/:rootId",
					IdempotencyKey: "target-root-authorized-delete-key", SessionJTI: strings.Repeat("9", 32),
					SessionRole: "admin", SessionTokenVersion: 1,
					SessionExpiresAt: time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC),
					NodeID:           1, RootID: "root-a",
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			wantMutation := "mutate:" + operation
			if operation == "rotate" {
				wantMutation = "mutate:register"
			}
			if operation == "register" {
				wantMutation = "mutate:register"
			}
			if !containsManagedRecoveryOrderedEvents(events, []string{"validate", "stop", "drain", wantMutation, "construct", "reconcile", "install"}) {
				t.Fatalf("%s transition events=%v", operation, events)
			}
		})
	}

	t.Run("post-mutation failure restores root before graph", func(t *testing.T) {
		events := make([]string, 0, 20)
		service := &managedRecoveryTargetRootMutationServiceFake{events: &events}
		installs := 0
		manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
			Build: func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
				events = append(events, "construct")
				return &managedRecoveryGraph{
					reconcileMetadata: func(context.Context) error { events = append(events, "reconcile"); return nil },
					shutdownLifecycle: func(context.Context) error { events = append(events, "drain"); return nil },
				}, nil
			},
			Install: func(publication *managedRecoveryPublication, graph *managedRecoveryGraph) error {
				installs++
				if installs == 2 {
					events = append(events, "install:failed")
					return errors.New("candidate install failed")
				}
				events = append(events, "install")
				return publication.publish(graph)
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.StartupWithConfig(context.Background(), backupasset.RecoveryConfig{Enabled: true}); err != nil {
			t.Fatal(err)
		}
		events = events[:0]
		facade := &managedRecoveryTargetRootFacade{service: service, runtime: manager}
		_, err = facade.Register(context.Background(), recovery.TargetRootRegistrationRequest{
			NodeID: 1, RootID: "root-a", SafeLabel: "rotate", Locator: "/srv/rotate",
			Policy: settings.RecoveryTargetRootPolicy{OverlapPolicyBinding: "strict"},
		})
		if err == nil {
			t.Fatal("post-mutation install failure unexpectedly succeeded")
		}
		if !containsManagedRecoveryOrderedEvents(events, []string{
			"mutate:register", "construct", "reconcile", "install:failed", "restore:root", "construct", "reconcile", "install",
		}) {
			t.Fatalf("restoration events=%v", events)
		}
	})

	t.Run("validation and mutation failures preserve authority ordering", func(t *testing.T) {
		for _, failure := range []string{"validate", "register", "delete"} {
			t.Run(failure, func(t *testing.T) {
				events := make([]string, 0, 16)
				service := &managedRecoveryTargetRootMutationServiceFake{events: &events}
				if failure == "validate" {
					service.validateErr = errors.New("invalid root definition")
				}
				if failure == "register" {
					service.registerErr = errors.New("register mutation failed")
				}
				if failure == "delete" {
					service.deleteErr = errors.New("delete mutation failed")
				}
				manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
					Build: func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
						events = append(events, "construct")
						return &managedRecoveryGraph{
							reconcileMetadata: func(context.Context) error { events = append(events, "reconcile"); return nil },
							stopClaims:        func() { events = append(events, "stop") },
							shutdownLifecycle: func(context.Context) error { events = append(events, "drain"); return nil },
						}, nil
					},
					Install: func(publication *managedRecoveryPublication, graph *managedRecoveryGraph) error {
						events = append(events, "install")
						return publication.publish(graph)
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := manager.StartupWithConfig(context.Background(), backupasset.RecoveryConfig{Enabled: true}); err != nil {
					t.Fatal(err)
				}
				events = events[:0]
				facade := &managedRecoveryTargetRootFacade{service: service, runtime: manager}
				if failure == "delete" {
					err = facade.Delete(context.Background(), 1, "root-a")
				} else {
					_, err = facade.Register(context.Background(), recovery.TargetRootRegistrationRequest{
						NodeID: 1, RootID: "root-a", SafeLabel: "safe", Locator: "/srv/safe",
						Policy: settings.RecoveryTargetRootPolicy{OverlapPolicyBinding: "strict"},
					})
				}
				if err == nil {
					t.Fatalf("%s failure unexpectedly succeeded", failure)
				}
				if failure == "validate" {
					if strings.Join(events, ",") != "validate" {
						t.Fatalf("validation failure drained or mutated: %v", events)
					}
					return
				}
				mutation := "mutate:" + failure
				if !containsManagedRecoveryOrderedEvents(events, []string{
					"stop", "drain", mutation, "construct", "reconcile", "install",
				}) || slices.Contains(events, "restore:root") {
					t.Fatalf("%s atomic mutation failure events=%v", failure, events)
				}
			})
		}
	})

	t.Run("root restoration failure leaves sticky fence", func(t *testing.T) {
		events := make([]string, 0, 16)
		service := &managedRecoveryTargetRootMutationServiceFake{
			events: &events, restoreErr: errors.New("root restoration failed"),
		}
		builds := 0
		manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
			Build: func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
				builds++
				if builds == 2 {
					return nil, errors.New("candidate construction failed")
				}
				return &managedRecoveryGraph{
					reconcileMetadata: func(context.Context) error { return nil },
					shutdownLifecycle: func(context.Context) error { return nil },
				}, nil
			},
			DowngradeInspector: &managedRecoveryDowngradeInspectorFake{},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.StartupWithConfig(context.Background(), backupasset.RecoveryConfig{Enabled: true}); err != nil {
			t.Fatal(err)
		}
		facade := &managedRecoveryTargetRootFacade{service: service, runtime: manager}
		_, err = facade.Register(context.Background(), recovery.TargetRootRegistrationRequest{
			NodeID: 1, RootID: "root-a", SafeLabel: "safe", Locator: "/srv/safe",
			Policy: settings.RecoveryTargetRootPolicy{OverlapPolicyBinding: "strict"},
		})
		if err == nil || manager.publication.current() != nil || manager.graph != nil || !manager.downgradeFenced {
			t.Fatalf("failed root restoration err=%v publication=%p graph=%p fenced=%t",
				err, manager.publication.current(), manager.graph, manager.downgradeFenced)
		}
		if _, err := manager.DowngradeReadiness(context.Background()); !errors.Is(err, backupasset.ErrInvalidState) {
			t.Fatalf("failed root restoration readiness error=%v", err)
		}
	})
}

type managedRecoveryTargetRootMutationServiceFake struct {
	events      *[]string
	validateErr error
	registerErr error
	deleteErr   error
	restoreErr  error
}

func (*managedRecoveryTargetRootMutationServiceFake) ReplayRegistration(
	context.Context,
	recovery.TargetRootRegistrationRequest,
) (settings.RecoveryTargetRootSummary, bool, error) {
	return settings.RecoveryTargetRootSummary{}, false, nil
}

func (*managedRecoveryTargetRootMutationServiceFake) ReplayDeletion(
	context.Context,
	recovery.TargetRootDeletionRequest,
) (settings.RecoveryTargetRootSummary, bool, error) {
	return settings.RecoveryTargetRootSummary{}, false, nil
}

func (fake *managedRecoveryTargetRootMutationServiceFake) ValidateRegistration(recovery.TargetRootRegistrationRequest) error {
	*fake.events = append(*fake.events, "validate")
	return fake.validateErr
}

func (fake *managedRecoveryTargetRootMutationServiceFake) ValidateDelete(uint, string) error {
	*fake.events = append(*fake.events, "validate")
	return fake.validateErr
}

func (fake *managedRecoveryTargetRootMutationServiceFake) RegisterMutation(
	context.Context,
	recovery.TargetRootRegistrationRequest,
) (settings.RecoveryTargetRootSummary, recovery.TargetRootMutationRollback, error) {
	*fake.events = append(*fake.events, "mutate:register")
	return settings.RecoveryTargetRootSummary{NodeID: 1, RootID: "root-a", SafeLabel: "safe"},
		recovery.TargetRootMutationRollback{}, fake.registerErr
}

func (fake *managedRecoveryTargetRootMutationServiceFake) DeleteMutation(
	context.Context,
	uint,
	string,
) (recovery.TargetRootMutationRollback, error) {
	*fake.events = append(*fake.events, "mutate:delete")
	return recovery.TargetRootMutationRollback{}, fake.deleteErr
}

func (fake *managedRecoveryTargetRootMutationServiceFake) DeleteAuthorizedMutation(
	context.Context,
	recovery.TargetRootDeletionRequest,
) (settings.RecoveryTargetRootSummary, recovery.TargetRootMutationRollback, error) {
	*fake.events = append(*fake.events, "mutate:delete-authorized")
	return settings.RecoveryTargetRootSummary{NodeID: 1, RootID: "root-a", SafeLabel: "safe"},
		recovery.TargetRootMutationRollback{}, fake.deleteErr
}

func (fake *managedRecoveryTargetRootMutationServiceFake) RestoreMutation(
	context.Context,
	recovery.TargetRootMutationRollback,
) error {
	*fake.events = append(*fake.events, "restore:root")
	return fake.restoreErr
}

func (*managedRecoveryTargetRootMutationServiceFake) List(
	context.Context,
	uint,
) ([]settings.RecoveryTargetRootSummary, error) {
	return nil, nil
}

func TestRecoveryProductionAuthorityProjectsOneOwnerThroughAllThreeAdapters(t *testing.T) {
	ports, err := newManagedRecoveryEligibilityAuthorities(recovery.RecoveryEligibilityAuthorityDependencies{
		DB:                openRuntimeTestDB(t),
		Source:            managedRecoveryEligibilitySourcePortFake{},
		Security:          managedRecoveryEligibilitySecurityPortFake{},
		TargetRoot:        managedRecoveryEligibilityTargetRootPortFake{},
		TargetObservation: managedRecoveryEligibilityTargetObservationPortFake{},
		Now: func() time.Time {
			return time.Date(2026, 8, 15, 12, 1, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	preflight, preflightOK := ports.preflight.(*recovery.RecoveryEligibilityAuthority)
	live, liveOK := ports.live.(*recovery.RecoveryEligibilityAuthority)
	reconciliation, reconciliationOK := ports.reconciliation.(*recovery.RecoveryEligibilityAuthority)
	if !preflightOK || !liveOK || !reconciliationOK || preflight != live || live != reconciliation {
		t.Fatal("Recovery composition projected more than one eligibility authority")
	}
}

func TestRuntimeNewStartsAndInstallsManagedExportGraph(t *testing.T) {
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RUNTIME_EXPORT_DATA_KEY_FOR_TEST_ONLY")
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.Task{}, &model.TaskRepositoryLink{}, &model.RepositoryAccessBinding{},
		&model.WrappedDomainKey{}, &model.BackupAssetExportJob{}, &model.BackupAssetExportKey{},
		&model.BackupAssetExportItem{}, &model.BackupAssetExportAttempt{}, &model.BackupAssetExportItemAttempt{},
		&model.BackupAssetExportSourceLease{}, &model.BackupAssetExportArtifact{}, &model.BackupAssetExportIdempotency{},
		&model.BackupAssetExportQuotaBucket{}, &model.BackupAssetExportReservation{},
		&model.BackupAssetExportDeliveryGrant{}, &model.BackupAssetExportDeliveryRequest{},
		&model.BackupAssetArchiveMemberRequest{},
		&model.BackupAssetProcessingJob{}, &model.BackupAssetProcessingInterest{},
		&model.BackupAssetDerivedArtifactSet{}, &model.BackupAssetDerivedArtifact{}, &model.BackupAssetDerivedBlob{},
	); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "export")
	settingsService := settings.NewService(db)
	if err := settingsService.UpdateMany(map[string]string{
		"backup_assets.enabled":                   "true",
		"backup_assets.lease_heartbeat":           "15s",
		"backup_assets.export.enabled":            "true",
		"backup_assets.export.root":               root,
		"backup_assets.export.lease_renew_margin": "40s",
	}); err != nil {
		t.Fatal(err)
	}
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settingsService, Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{},
		ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{},
		Now: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	manager, ok := runtime.exportManager.(*managedExportRuntime)
	if !ok {
		t.Fatalf("Export manager type=%T", runtime.exportManager)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatalf("start managed Export graph: %v", err)
	}
	if !manager.Ready() || manager.Service() == nil || manager.Delivery() == nil {
		t.Fatalf("ready=%v service=%p delivery=%p", manager.Ready(), manager.Service(), manager.Delivery())
	}
	if runtime.exportDelivery != manager.Delivery() || runtime.contentService.exportBranch != manager.Delivery() {
		t.Fatalf("runtime did not bind the stable Export delivery facade")
	}
	manager.mu.RLock()
	graph := manager.graph
	manager.mu.RUnlock()
	if graph == nil || graph.attempts == nil || graph.worker == nil || graph.lifecycle == nil || graph.runner == nil ||
		graph.stopAccepting == nil || graph.drain == nil || graph.run == nil || graph.shutdown == nil {
		t.Fatalf("managed Export execution graph incomplete: %+v", graph)
	}
	if graph.runner.sourceLeaseInterval != 15*time.Second || graph.runner.heartbeat != 20*time.Second {
		t.Fatalf("managed Export worker intervals: source=%s attempt=%s", graph.runner.sourceLeaseInterval, graph.runner.heartbeat)
	}
}

func TestRuntimeSearchExcerptResolverReadsOnlyCurrentCompletePublishedArtifact(t *testing.T) {
	db := openRuntimeTestDB(t)
	fixture := seedRuntimeSearchExcerptFixture(t, db)
	readCalls := 0
	resolver := &runtimeSearchExcerptResolver{
		db:           db,
		resolveAsset: runtimeDerivedSourceAssetResolver(db),
		readArtifact: func(_ context.Context, request content.DerivedArtifactRead, destination io.Writer) error {
			readCalls++
			if request.ArtifactID != fixture.artifactID || request.RecoveryPointID != fixture.ref.RecoveryPointID ||
				request.CatalogGenerationID != fixture.catalogID || request.EntryID != fixture.ref.EntryID ||
				request.SourceFingerprint != fixture.sourceFingerprint {
				t.Fatalf("Derived excerpt read request=%+v", request)
			}
			_, err := io.WriteString(destination, "before NEEDLE after")
			return err
		},
		activePipeline: func(_ context.Context, capability, profile string) (string, error) {
			if capability != capabilityspec.CapabilityTextExtract || profile != capabilityspec.ProfileBoundedTextV1 {
				t.Fatalf("active pipeline request=%q/%q", capability, profile)
			}
			return fixture.pipelineFingerprint, nil
		},
	}
	request := search.ExcerptVerifyRequest{
		Ref: fixture.ref, Field: search.SearchFieldContent, Terms: []string{"needle"}, ExcerptRef: fixture.artifactID,
	}
	snippet, verified, err := resolver.Verify(context.Background(), request)
	if err != nil || !verified || snippet.Field != search.SearchFieldContent || !strings.Contains(snippet.Text, "NEEDLE") ||
		len(snippet.Text) > runtimeSearchSnippetMaxBytes || readCalls != 1 {
		t.Fatalf("verified excerpt=%+v verified=%t reads=%d err=%v", snippet, verified, readCalls, err)
	}

	if err := db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("id = ?", fixture.setID).
		Update("projection_published", false).Error; err != nil {
		t.Fatal(err)
	}
	_, verified, err = resolver.Verify(context.Background(), request)
	if err != nil || verified || readCalls != 1 {
		t.Fatalf("unpublished excerpt verified=%t reads=%d err=%v", verified, readCalls, err)
	}
}

func TestRuntimeSearchExcerptResolverRejectsUnverifiedOrInvalidText(t *testing.T) {
	db := openRuntimeTestDB(t)
	fixture := seedRuntimeSearchExcerptFixture(t, db)
	payload := []byte("different content")
	readCalls := 0
	resolver := &runtimeSearchExcerptResolver{
		db:           db,
		resolveAsset: runtimeDerivedSourceAssetResolver(db),
		readArtifact: func(_ context.Context, _ content.DerivedArtifactRead, destination io.Writer) error {
			readCalls++
			_, err := destination.Write(payload)
			return err
		},
		activePipeline: func(context.Context, string, string) (string, error) {
			return fixture.pipelineFingerprint, nil
		},
	}
	request := search.ExcerptVerifyRequest{
		Ref: fixture.ref, Field: search.SearchFieldContent, Terms: []string{"needle"}, ExcerptRef: fixture.artifactID,
	}
	if _, verified, err := resolver.Verify(context.Background(), request); err != nil || verified {
		t.Fatalf("unverified excerpt verified=%t err=%v", verified, err)
	}
	payload = []byte{0xff, 0xfe}
	if _, verified, err := resolver.Verify(context.Background(), request); err != nil || verified {
		t.Fatalf("invalid UTF-8 excerpt verified=%t err=%v", verified, err)
	}
	updated := db.Model(&model.BackupAssetProcessingJob{}).Where("current_artifact_set_id = ?", fixture.setID).
		Updates(map[string]any{
			"capability": capabilityspec.CapabilityImageThumbnail, "capability_schema": "image.thumbnail.v1",
			"output_profile": capabilityspec.ProfileRasterThumbnailV1,
		})
	if updated.Error != nil || updated.RowsAffected != 1 {
		t.Fatalf("replace excerpt producer profile: rows=%d err=%v", updated.RowsAffected, updated.Error)
	}
	payload = []byte("before NEEDLE after")
	if _, verified, err := resolver.Verify(context.Background(), request); err != nil || verified || readCalls != 2 {
		t.Fatalf("profile-incompatible excerpt verified=%t reads=%d err=%v", verified, readCalls, err)
	}
}

type runtimeSearchExcerptFixture struct {
	ref                 backupasset.AssetRef
	catalogID           string
	sourceFingerprint   string
	pipelineFingerprint string
	setID               string
	artifactID          string
}

func seedRuntimeSearchExcerptFixture(t *testing.T, db *gorm.DB) runtimeSearchExcerptFixture {
	t.Helper()
	if err := db.AutoMigrate(
		&model.BackupRepository{}, &model.RecoveryPoint{}, &model.CatalogGeneration{}, &model.CatalogEntry{},
		&model.BackupAssetProcessingJob{}, &model.BackupAssetProcessingAttempt{},
		&model.BackupAssetDerivedArtifactSet{}, &model.BackupAssetDerivedArtifact{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	repositoryID := strings.Repeat("1", 32)
	pointID := strings.Repeat("2", 32)
	catalogID := strings.Repeat("3", 32)
	entryID := strings.Repeat("4", 64)
	jobID := strings.Repeat("5", 32)
	attemptID := strings.Repeat("6", 32)
	setID := strings.Repeat("7", 32)
	artifactID := strings.Repeat("8", 32)
	sourceFingerprint := strings.Repeat("9", 64)
	entryFingerprint := strings.Repeat("a", 64)
	pipelineFingerprint := strings.Repeat("b", 64)
	workKey := strings.Repeat("c", 64)
	currentAttemptID, currentSetID := attemptID, setID
	rows := []any{
		&model.BackupRepository{
			ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "excerpt-repository",
			VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
			CapabilityRevision: 2, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
		},
		&model.RecoveryPoint{
			ID: pointID, RepositoryID: repositoryID, Semantics: string(backupasset.PointNativeSnapshot),
			State: string(backupasset.RecoveryPointCommitted), SourceFingerprint: sourceFingerprint, CapabilityRevision: 2,
			PhysicalAvailability: string(backupasset.PhysicalOnline), CreatedAt: now, UpdatedAt: now,
		},
		&model.CatalogGeneration{
			ID: catalogID, RecoveryPointID: pointID, Generation: 1, State: string(catalog.GenerationComplete), IsActive: true,
			SourceFingerprint: sourceFingerprint, ExpectedEntryCount: 1, WrittenEntryCount: 1,
			StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
		},
		&model.CatalogEntry{
			GenerationID: catalogID, EntryID: entryID, RecoveryPointID: pointID, NormalizedPath: "notes.txt", Name: "notes.txt",
			EntryType: string(backupasset.CatalogEntryFile), Size: 19, MimeType: "text/plain", Fingerprint: entryFingerprint,
			FingerprintStrength: string(catalog.FingerprintStrong), SecurityState: "sealed", CreatedAt: now,
		},
		&model.BackupAssetProcessingJob{
			ID: jobID, WorkKey: workKey, DescriptorSchemaVersion: 1, DescriptorCanonical: []byte(`{}`),
			RecoveryPointID: pointID, CatalogGenerationID: catalogID, EntryID: entryID,
			SourceFingerprint: sourceFingerprint, EntryFingerprint: entryFingerprint, ProviderCapabilityRevision: 2,
			Capability: capabilityspec.CapabilityTextExtract, CapabilitySchema: "text.extract.v1",
			PipelineFingerprint: pipelineFingerprint, OutputProfile: capabilityspec.ProfileBoundedTextV1,
			SecurityPolicyRevision: processingSecurityPolicyRevision, PriorityClass: string(processing.PriorityBackground),
			EffectivePriority: 1, State: string(processing.ProcessingSucceeded), TransitionRevision: 2,
			CurrentAttemptID: &currentAttemptID, CurrentArtifactSetID: &currentSetID, IsCurrent: false,
			QueuedAt: now, FinishedAt: &now, AbsoluteDeadline: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
		},
		&model.BackupAssetProcessingAttempt{
			ID: attemptID, JobID: jobID, AttemptNumber: 1, WorkerID: strings.Repeat("d", 32),
			SlotClass: string(processing.PriorityBackground), State: "succeeded", WorkerLeaseExpiresAt: now.Add(time.Minute),
			LastHeartbeatAt: now, RecoveryPointLeaseID: strings.Repeat("e", 32), RecoveryPointAttemptID: strings.Repeat("f", 32),
			RecoveryPointFenceHash: strings.Repeat("0", 64), AbsoluteDeadline: now.Add(time.Hour), IsCurrent: false,
			StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
		},
		&model.BackupAssetDerivedArtifactSet{
			ID: setID, JobID: jobID, AttemptID: attemptID, WorkKey: workKey,
			RecoveryPointID: pointID, CatalogGenerationID: catalogID, EntryID: entryID,
			SourceFingerprint: sourceFingerprint, SecurityPolicyRevision: processingSecurityPolicyRevision,
			ManifestDigest: strings.Repeat("1", 64), State: "active", Completeness: string(processing.ArtifactComplete),
			ArtifactCount: 1, TotalPlaintextBytes: 19, ProjectionRequired: true, ProjectionPublished: true,
			ProjectionRevision: 1, CreatedAt: now, UpdatedAt: now,
		},
		&model.BackupAssetDerivedArtifact{
			ID: artifactID, ArtifactSetID: setID, Ordinal: 0, Role: string(processing.ArtifactRoleContent), MediaType: "text/plain",
			PlaintextSize: 19, PlaintextDigest: strings.Repeat("2", 64), Completeness: string(processing.ArtifactComplete),
			CoverageCanonical: []byte(`{"schema_version":1,"kind":"all"}`), BlobID: strings.Repeat("3", 32),
			ExcerptRef: artifactID, CreatedAt: now,
		},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", jobID).
		Update("is_current", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetProcessingAttempt{}).Where("id = ?", attemptID).
		Update("is_current", false).Error; err != nil {
		t.Fatal(err)
	}
	return runtimeSearchExcerptFixture{
		ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}, catalogID: catalogID,
		sourceFingerprint: sourceFingerprint, pipelineFingerprint: pipelineFingerprint, setID: setID, artifactID: artifactID,
	}
}

func TestRuntimeRejectsMismatchedTransportFacets(t *testing.T) {
	db := openRuntimeTestDB(t)
	_, err := New(Dependencies{
		DB: db, Settings: settings.NewService(db), Transport: &runtimeTransportFake{marker: 1}, StreamTransport: &runtimeTransportFake{marker: 2}, Metrics: publication.NoopMetrics{},
		ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{}, StagedPayload: &runtimeStagedPayloadFake{},
	})
	if err == nil {
		t.Fatal("runtime accepted distinct transport facets")
	}
}

func TestRuntimeDerivedSourceAssetResolverRequiresCurrentBoundSource(t *testing.T) {
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.BackupRepository{}, &model.RecoveryPoint{}, &model.CatalogGeneration{}, &model.CatalogEntry{},
	); err != nil {
		t.Fatal(err)
	}
	repositoryID := strings.Repeat("1", 32)
	pointID := strings.Repeat("2", 32)
	generationID := strings.Repeat("3", 32)
	entryID := strings.Repeat("5", 64)
	source := strings.Repeat("4", 64)
	entryFingerprint := strings.Repeat("6", 64)
	now := time.Date(2026, 7, 20, 8, 45, 0, 0, time.UTC)
	for _, value := range []any{
		&model.BackupRepository{
			ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "derived-provider",
			VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
			CapabilityRevision: 2, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
		},
		&model.RecoveryPoint{
			ID: pointID, RepositoryID: repositoryID, Semantics: string(backupasset.PointNativeSnapshot),
			State: string(backupasset.RecoveryPointCommitted), SourceFingerprint: source, CapabilityRevision: 2,
			PhysicalAvailability: string(backupasset.PhysicalOnline), CreatedAt: now, UpdatedAt: now,
		},
		&model.CatalogGeneration{
			ID: generationID, RecoveryPointID: pointID, Generation: 1, State: string(catalog.GenerationComplete),
			IsActive: true, SourceFingerprint: source, StartedAt: now, CreatedAt: now, UpdatedAt: now,
		},
		&model.CatalogEntry{
			GenerationID: generationID, EntryID: entryID, RecoveryPointID: pointID,
			NormalizedPath: "/safe/report.txt", Name: "report.txt", EntryType: string(backupasset.CatalogEntryFile),
			Size: 128, MimeType: "text/plain", Fingerprint: entryFingerprint,
			FingerprintStrength: string(catalog.FingerprintStrong), SecurityState: "sealed", CreatedAt: now,
		},
	} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	resolver := runtimeDerivedSourceAssetResolver(db)
	ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}
	asset, err := resolver(context.Background(), ref, generationID, source)
	if err != nil || asset.Ref != ref || asset.CatalogGenerationID != generationID ||
		asset.RepositoryID != repositoryID || asset.Provider != backupasset.ProviderRestic ||
		asset.ProviderCapabilityRevision != 2 || asset.SourceFingerprint != source ||
		asset.EntryFingerprint != entryFingerprint || asset.FingerprintStrength != string(catalog.FingerprintStrong) ||
		asset.Size != 128 || asset.MediaType != "text/plain" {
		t.Fatalf("current Derived source asset=%+v err=%v", asset, err)
	}
	if asset, err = resolver(context.Background(), ref, generationID, "stale-source"); err == nil || asset != (content.AuthorizedAsset{}) {
		t.Fatalf("stale Derived source asset=%+v err=%v", asset, err)
	}
}

func TestRuntimeStartupManagedModeRequiresInterruptedRunReadiness(t *testing.T) {
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.Node{}, &model.Task{}, &model.BackupRepository{}, &model.RepositoryAccessBinding{},
		&model.TaskRepositoryLink{}, &model.RecoveryPoint{}, &model.RecoveryPointLease{},
		&model.BackupAssetManagedHistoryLatch{}, &model.BackupAssetInstallation{},
		&model.BackupAssetInventoryRun{}, &model.BackupAssetRepositoryConflict{},
	); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := settingsService.Update("backup_assets.content_cache_enabled", "false"); err != nil {
		t.Fatal(err)
	}
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settingsService, Transport: transport, StreamTransport: transport, StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{},
		ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.enablement = readyGAEnablement()
	if err := runtime.StartupPass(context.Background()); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("managed startup without TaskRun readiness error=%v, want invalid state", err)
	}
}

type runtimeReadinessRetentionInstaller struct {
	retention task.ManagedRecoveryPointRetention
}

func (installer *runtimeReadinessRetentionInstaller) ReconcileInterruptedRuns(context.Context, int) (bool, error) {
	return false, nil
}

func (installer *runtimeReadinessRetentionInstaller) SetManagedRecoveryPointRetention(value task.ManagedRecoveryPointRetention) {
	installer.retention = value
}

func TestRuntimeInterruptedRunReadinessInstallsManagedTaskRetention(t *testing.T) {
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.BackupRepository{}, &model.RecoveryPoint{}, &model.TaskRepositoryLink{},
		&model.BackupRetentionPolicy{}, &model.RecoveryPointHold{},
		&model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLifecycleTombstone{},
		&model.BackupAssetManagedHistoryLatch{},
	); err != nil {
		t.Fatal(err)
	}
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settings.NewService(db), Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{},
		ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{},
		Now: func() time.Time { return time.Date(2026, 8, 18, 13, 20, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("construct runtime: %v", err)
	}
	installer := &runtimeReadinessRetentionInstaller{}
	if err := runtime.SetInterruptedRunReadiness(installer); err != nil {
		t.Fatal(err)
	}
	if installer.retention == nil {
		t.Fatal("interrupted-run readiness did not receive the managed Task retention facade")
	}
}

func TestRuntimeContentConstructionRequiresSessionRevocationSource(t *testing.T) {
	db := openRuntimeTestDB(t)
	transport := &runtimeTransportFake{}
	_, err := New(Dependencies{
		DB: db, Settings: settings.NewService(db), Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{}, ContentMetrics: content.NoopMetrics{},
	})
	if !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("runtime without session revocation source got %v, want invalid state", err)
	}
}

func TestRuntimeAuthenticatedCacheUsesSharedContentMetrics(t *testing.T) {
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.Task{}, &model.TaskRepositoryLink{}, &model.RepositoryAccessBinding{}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "asset-content-cache")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, strings.Repeat("a", 64)), []byte("old process generation"), 0o600); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.content_cache_root", root); err != nil {
		t.Fatal(err)
	}
	metrics := &runtimeContentMetricsFake{cache: make(map[content.MetricCacheOutcome]int)}
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settingsService, Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{}, ContentMetrics: metrics,
		SessionRevocations: &runtimeSessionRevocationsFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager, ok := runtime.contentManager.(*managedContentRuntime)
	if !ok {
		t.Fatalf("content manager type=%T", runtime.contentManager)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	contentConfig, err := runtime.foundation.ContentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.PrepareEnable(context.Background(), contentConfig); err != nil {
		t.Fatal(err)
	}
	if manager.cache == nil || !manager.cache.Status().DiskEnabled {
		t.Fatalf("runtime cache status=%+v", manager.cache.Status())
	}
	if got := metrics.cache[content.MetricCacheKeyLoss]; got != 1 {
		t.Fatalf("runtime cache key-loss metric=%d, want 1", got)
	}
}

type runtimeManagedContentPrepareFailureProbe struct {
	*managedContentRuntime
	cleanupContexts []context.Context
}

func (probe *runtimeManagedContentPrepareFailureProbe) PrepareDisable(ctx context.Context) error {
	probe.cleanupContexts = append(probe.cleanupContexts, ctx)
	return probe.managedContentRuntime.PrepareDisable(ctx)
}

func TestFeatureEnableContentPrepareFailureRunsBoundedCleanupAndFencesFailedCompensation(t *testing.T) {
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.Task{}, &model.TaskRepositoryLink{}, &model.RepositoryAccessBinding{}); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.content_cache_root", filepath.Join(t.TempDir(), "content-cache")); err != nil {
		t.Fatal(err)
	}
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settingsService, Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{},
		ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager, ok := runtime.contentManager.(*managedContentRuntime)
	if !ok {
		t.Fatalf("content manager type=%T", runtime.contentManager)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	contentConfig, err := runtime.foundation.ContentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.PrepareEnable(context.Background(), contentConfig); err != nil {
		t.Fatal(err)
	}
	if manager.cache == nil || !manager.cacheAttached {
		t.Fatal("managed Content fixture did not attach its candidate cache")
	}
	if err := manager.broker.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	probe := &runtimeManagedContentPrepareFailureProbe{managedContentRuntime: manager}
	runtime.contentManager = probe
	runtime.transitioner = &runtimeFeatureTransitionerFake{events: &[]string{}}
	runtime.enablement = readyGAEnablement()
	runtime.inventory = nil
	persistCalled := false
	err = runtime.transitionFeatureWithConfigs(
		context.Background(), testFoundationTransitionConfigs(t, false, true),
		func() error { persistCalled = true; return nil },
	)
	if !errors.Is(err, content.ErrBrokerClosed) || !errors.Is(err, ErrFeatureTransitionCompensation) {
		t.Fatalf("Content prepare/cleanup error=%v, want primary BrokerClosed and typed compensation failure", err)
	}
	if persistCalled {
		t.Fatal("Content prepare failure reached persistence")
	}
	if len(probe.cleanupContexts) != 1 {
		t.Fatalf("Content prepare failure cleanup calls=%d, want one synchronous cleanup", len(probe.cleanupContexts))
	}
	cleanupDeadline, ok := probe.cleanupContexts[0].Deadline()
	if !ok || time.Until(cleanupDeadline) <= 0 || time.Until(cleanupDeadline) > featureTransitionCleanupReserve {
		t.Fatalf("Content prepare cleanup deadline=%s ok=%t, want live shared reserve <=%s", cleanupDeadline, ok, featureTransitionCleanupReserve)
	}
	if runtime.featureTransitionReady() || !runtime.featureTransitionFenced.Load() || manager.ready.Load() {
		t.Fatalf("failed Content compensation ready=%t fenced=%t content-ready=%t",
			runtime.featureTransitionReady(), runtime.featureTransitionFenced.Load(), manager.ready.Load())
	}
}

func TestManagedContentRuntimeReconcileCycleReleasesTerminalLeaseWhileFeatureDisabled(t *testing.T) {
	db, manager, leaseID := newManagedContentRuntimeTerminalLeaseHarness(t)
	stateErr, cacheErr := manager.reconcileCycle(context.Background())
	if stateErr != nil || cacheErr != nil {
		t.Fatalf("disabled reconciliation errors: state=%v cache=%v", stateErr, cacheErr)
	}
	var stored model.RecoveryPointLease
	if err := db.First(&stored, "id = ?", leaseID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != string(backupasset.LeaseReleased) || stored.ReleasedAt == nil {
		t.Fatalf("disabled runtime left terminal lease active: %+v", stored)
	}
}

func TestManagedContentRuntimeReconcileCycleStopsAtShutdownFence(t *testing.T) {
	db, manager, leaseID := newManagedContentRuntimeTerminalLeaseHarness(t)
	manager.stopped.Store(true)
	stateErr, cacheErr := manager.reconcileCycle(context.Background())
	if stateErr != nil || cacheErr != nil {
		t.Fatalf("stopped reconciliation errors: state=%v cache=%v", stateErr, cacheErr)
	}
	var stored model.RecoveryPointLease
	if err := db.First(&stored, "id = ?", leaseID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != string(backupasset.LeaseActive) || stored.ReleasedAt != nil {
		t.Fatalf("stopped runtime mutated terminal lease: %+v", stored)
	}
}

func TestManagedContentRuntimeShutdownCancelsAndJoinsRunLoop(t *testing.T) {
	db, manager, _ := newManagedContentRuntimeTerminalLeaseHarness(t)
	settingsReader := &runtimeContentRunSettings{
		service: settings.NewService(db), started: make(chan struct{}),
	}
	manager.foundation = backupasset.NewFoundationService(settingsReader)
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	done := make(chan struct{})
	go func() {
		manager.Run(runCtx)
		close(done)
	}()
	select {
	case <-settingsReader.started:
	case <-time.After(time.Second):
		t.Fatal("managed Content run loop did not start")
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-shutdownCtx.Done():
		t.Fatal("managed Content shutdown did not join its run loop")
	}
}

func newManagedContentRuntimeTerminalLeaseHarness(t *testing.T) (*gorm.DB, *managedContentRuntime, string) {
	t.Helper()
	db := openRuntimeTestDB(t)
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: time.Minute, Heartbeat: 10 * time.Second, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	grantID := strings.Repeat("a", 32)
	lease, err := leaseService.Acquire(context.Background(), backupasset.AcquireLeaseRequest{
		RecoveryPointID: strings.Repeat("b", 32),
		HolderType:      backupasset.LeaseHolderContentSession,
		OwnerID:         grantID,
	})
	if err != nil {
		t.Fatal(err)
	}
	grant := model.BackupAssetDeliveryGrant{
		ID: grantID, DeliveryID: strings.Repeat("c", 32), State: string(content.DeliveryRevoked),
		RevocationReason: "process_restarted", RevokedAt: &now, LeaseID: lease.ID,
		Version: 1, AuditState: "none", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	reconciler, err := content.NewReconciler(content.ReconcilerDependencies{
		DB: db, Budget: runtimeContentReconcilerBudgetFake{}, Audit: runtimeContentReconcilerAuditFake{},
		Lease: leaseService, Now: func() time.Time { return now }, BatchSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, &managedContentRuntime{reconciler: reconciler, ready: &atomic.Bool{}}, lease.ID
}

type runtimeContentReconcilerBudgetFake struct{}

func (runtimeContentReconcilerBudgetFake) Finalize(context.Context, content.FinalizeIntent) (content.Finalization, error) {
	return content.Finalization{}, nil
}

type runtimeContentReconcilerAuditFake struct{}

func (runtimeContentReconcilerAuditFake) FlushGrant(context.Context, string) error { return nil }

type runtimeContentRunSettings struct {
	service *settings.Service
	started chan struct{}
	once    sync.Once
}

func (reader *runtimeContentRunSettings) GetEffective(key string) string {
	return reader.service.GetEffective(key)
}

func (reader *runtimeContentRunSettings) BackupAssetSettingsSnapshot() (map[string]string, error) {
	reader.once.Do(func() { close(reader.started) })
	return reader.service.BackupAssetSettingsSnapshot()
}

func TestRuntimeContentSessionValidatorChecksRevocationRoleVersionAndExpiry(t *testing.T) {
	now := time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	user := model.User{ID: 7, Username: "content-admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin", TokenVersion: 4}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	revocations := &runtimeSessionRevocationsFake{}
	validator, err := newRuntimeContentSessionValidator(db, revocations, func() time.Time { return now })
	if err != nil {
		t.Fatalf("newRuntimeContentSessionValidator: %v", err)
	}
	session := content.DeliverySession{
		JTI: strings.Repeat("a", 32), UserID: user.ID, Role: user.Role,
		TokenVersion: user.TokenVersion, ExpiresAt: now.Add(time.Minute),
	}
	if err := validator.Validate(context.Background(), session); err != nil {
		t.Fatalf("valid content session rejected: %v", err)
	}
	for name, mutate := range map[string]func(*content.DeliverySession){
		"expired":       func(value *content.DeliverySession) { value.ExpiresAt = now },
		"wrong role":    func(value *content.DeliverySession) { value.Role = "operator" },
		"wrong version": func(value *content.DeliverySession) { value.TokenVersion++ },
		"wrong user":    func(value *content.DeliverySession) { value.UserID++ },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := session
			mutate(&candidate)
			if err := validator.Validate(context.Background(), candidate); !errors.Is(err, backupasset.ErrForbidden) {
				t.Fatalf("invalid session got %v, want forbidden", err)
			}
		})
	}
	revocations.revoked = true
	if err := validator.Validate(context.Background(), session); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("revoked session got %v, want forbidden", err)
	}
}

func TestRuntimeContentAuthorizerBindsExactActiveCatalogAndCurrentOwnership(t *testing.T) {
	now := time.Date(2026, 7, 19, 2, 3, 4, 0, time.UTC)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.BackupRepository{}, &model.RecoveryPoint{}, &model.CatalogGeneration{}, &model.CatalogEntry{},
		&model.BackupAssetSearchGeneration{}, &model.BackupAssetSearchDocument{},
	); err != nil {
		t.Fatal(err)
	}
	repositoryID := strings.Repeat("b", 32)
	pointID := strings.Repeat("c", 32)
	generationID := strings.Repeat("d", 32)
	entryID := strings.Repeat("e", 64)
	sourceFingerprint := strings.Repeat("1", 64)
	entryFingerprint := strings.Repeat("2", 64)
	if err := db.Create(&model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRclone), DisplayName: "content-repository",
		VersionMode: string(backupasset.VersionVersionedPrefix), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 3, CapabilitiesJSON: `{"open_sequential":true,"open_range":true}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.RecoveryPoint{
		ID: pointID, RepositoryID: repositoryID, Semantics: string(backupasset.PointImportedBaseline),
		State: string(backupasset.RecoveryPointCommitted), SourceFingerprint: sourceFingerprint,
		CapabilityRevision: 3, CapabilitiesJSON: `{"open_sequential":true,"open_range":true}`,
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.CatalogGeneration{
		ID: generationID, RecoveryPointID: pointID, Generation: 1, State: string(catalog.GenerationComplete),
		IsActive: true, SourceFingerprint: sourceFingerprint, ExpectedEntryCount: 1, WrittenEntryCount: 1,
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	modified := now.Add(-time.Hour)
	if err := db.Create(&model.CatalogEntry{
		GenerationID: generationID, EntryID: entryID, RecoveryPointID: pointID,
		NormalizedPath: "/safe/report.pdf", Name: "report.pdf", EntryType: string(backupasset.CatalogEntryFile),
		Size: 4096, ModifiedAt: &modified, MimeType: "application/pdf", Fingerprint: entryFingerprint,
		FingerprintStrength: string(catalog.FingerprintStrong), SecurityState: "sealed", CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	searchGenerationID := strings.Repeat("3", 32)
	if err := db.Create(&model.BackupAssetSearchGeneration{
		ID: searchGenerationID, RecoveryPointID: pointID, CatalogGenerationID: generationID,
		Generation: 1, State: string(search.SearchGenerationComplete), IsActive: true,
		SourceFingerprint: sourceFingerprint, NormalizerVersion: search.NormalizerVersion,
		SearchKeyVersion: 1, ProjectionRevision: 1, LeaseID: strings.Repeat("4", 32),
		BuildAttemptID: strings.Repeat("5", 32), FenceTokenHash: strings.Repeat("6", 64),
		ExpectedDocumentCount: 1, WrittenDocumentCount: 1, StartedAt: now, FinishedAt: &now,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetSearchDocument{
		SearchGenerationID: searchGenerationID, DocumentID: entryID, RecoveryPointID: pointID,
		CatalogGenerationID: generationID, EntryID: entryID, Sensitivity: string(search.SensitivitySecret),
		ClassificationRevision: 7, MetadataRevision: 1, EntryType: string(backupasset.CatalogEntryFile),
		ModifiedAt: &modified, LineageToken: strings.Repeat("7", 64), PathGroupToken: strings.Repeat("8", 64),
		PathSortKey: "report.pdf", NameSortKey: "report.pdf", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	ownership, err := catalog.NewOwnership(db)
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := newRuntimeContentAuthorizer(db, ownership)
	if err != nil {
		t.Fatalf("newRuntimeContentAuthorizer: %v", err)
	}
	ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}
	actor := content.DeliveryActor{UserID: 1, Username: "admin", Role: "admin"}
	asset, err := authorizer.Authorize(context.Background(), actor, ref, content.DeliveryPreview)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if asset.Ref != ref || asset.CatalogGenerationID != generationID || asset.RepositoryID != repositoryID ||
		asset.Provider != backupasset.ProviderRclone || asset.ProviderCapabilityRevision != 3 || asset.SourceFingerprint != sourceFingerprint ||
		asset.EntryFingerprint != entryFingerprint || asset.FingerprintStrength != string(catalog.FingerprintStrong) ||
		asset.Size != 4096 || asset.MediaType != "application/pdf" || asset.Path != "/safe/report.pdf" ||
		asset.Name != "report.pdf" || !asset.RangeProven || asset.ModifiedAt == nil || !asset.ModifiedAt.Equal(modified) ||
		asset.SearchClassification != content.ClassificationSecret || asset.SearchClassificationRevision != 7 {
		t.Fatalf("authorized asset binding=%+v", asset)
	}
	assertNoSearchEvidence := func(t *testing.T) {
		t.Helper()
		current, err := authorizer.Authorize(context.Background(), actor, ref, content.DeliveryPreview)
		if err != nil {
			t.Fatalf("Authorize without usable Search evidence: %v", err)
		}
		if current.SearchClassification != "" || current.SearchClassificationRevision != 0 {
			t.Fatalf("incomplete Search evidence escaped: %+v", current)
		}
	}
	for name, testCase := range map[string]struct {
		mutate  func(*testing.T)
		restore func(*testing.T)
	}{
		"unfinished generation": {
			mutate: func(t *testing.T) {
				if err := db.Model(&model.BackupAssetSearchGeneration{}).Where("id = ?", searchGenerationID).
					Update("finished_at", nil).Error; err != nil {
					t.Fatal(err)
				}
			},
			restore: func(t *testing.T) {
				if err := db.Model(&model.BackupAssetSearchGeneration{}).Where("id = ?", searchGenerationID).
					Update("finished_at", now).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		"document count mismatch": {
			mutate: func(t *testing.T) {
				if err := db.Model(&model.BackupAssetSearchGeneration{}).Where("id = ?", searchGenerationID).
					Update("expected_document_count", 2).Error; err != nil {
					t.Fatal(err)
				}
			},
			restore: func(t *testing.T) {
				if err := db.Model(&model.BackupAssetSearchGeneration{}).Where("id = ?", searchGenerationID).
					Update("expected_document_count", 1).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		"source mismatch": {
			mutate: func(t *testing.T) {
				if err := db.Model(&model.BackupAssetSearchGeneration{}).Where("id = ?", searchGenerationID).
					Update("source_fingerprint", "stale-source").Error; err != nil {
					t.Fatal(err)
				}
			},
			restore: func(t *testing.T) {
				if err := db.Model(&model.BackupAssetSearchGeneration{}).Where("id = ?", searchGenerationID).
					Update("source_fingerprint", sourceFingerprint).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		"inactive generation": {
			mutate: func(t *testing.T) {
				if err := db.Model(&model.BackupAssetSearchGeneration{}).Where("id = ?", searchGenerationID).
					Update("is_active", false).Error; err != nil {
					t.Fatal(err)
				}
			},
			restore: func(t *testing.T) {
				if err := db.Model(&model.BackupAssetSearchGeneration{}).Where("id = ?", searchGenerationID).
					Update("is_active", true).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		"non-exact document": {
			mutate: func(t *testing.T) {
				if err := db.Model(&model.BackupAssetSearchDocument{}).
					Where("search_generation_id = ? AND document_id = ?", searchGenerationID, entryID).
					Update("document_id", strings.Repeat("9", 64)).Error; err != nil {
					t.Fatal(err)
				}
			},
			restore: func(t *testing.T) {
				if err := db.Model(&model.BackupAssetSearchDocument{}).
					Where("search_generation_id = ?", searchGenerationID).
					Update("document_id", entryID).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run("ignores "+name, func(t *testing.T) {
			testCase.mutate(t)
			t.Cleanup(func() { testCase.restore(t) })
			assertNoSearchEvidence(t)
		})
	}
	if _, err := authorizer.Authorize(context.Background(), content.DeliveryActor{UserID: 2, Role: "operator"}, ref, content.DeliveryDownload); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("operator download got %v, want forbidden", err)
	}
	if _, err := authorizer.Authorize(context.Background(), content.DeliveryActor{UserID: 3, Role: "viewer"}, ref, content.DeliveryPreview); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("viewer preview got %v, want forbidden", err)
	}
	t.Run("repository drift", func(t *testing.T) {
		replacementRepositoryID := strings.Repeat("a", 32)
		if err := db.Create(&model.BackupRepository{
			ID: replacementRepositoryID, ProviderKind: string(backupasset.ProviderRclone), DisplayName: "replacement-content-repository",
			VersionMode: string(backupasset.VersionVersionedPrefix), Status: string(backupasset.RepositoryOnline),
			CapabilityRevision: 3, CapabilitiesJSON: `{"open_sequential":true,"open_range":true}`,
			ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).
			Update("repository_id", replacementRepositoryID).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).
				Update("repository_id", repositoryID).Error; err != nil {
				t.Fatal(err)
			}
		})
		if err := authorizer.Reauthorize(context.Background(), actor, asset, content.DeliveryPreview); !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("repository drift error=%v, want conflict", err)
		}
	})
	for name, update := range map[string]map[string]any{
		"Search classification": {"sensitivity": string(search.SensitivityNonSecret), "classification_revision": 8},
		"path":                  {"normalized_path": "/safe/renamed/report.pdf"},
		"name":                  {"name": "renamed.pdf"},
		"media type":            {"mime_type": "application/octet-stream"},
	} {
		t.Run(name+" drift", func(t *testing.T) {
			if name == "Search classification" {
				if err := db.Model(&model.BackupAssetSearchDocument{}).
					Where("search_generation_id = ? AND document_id = ?", searchGenerationID, entryID).
					Updates(update).Error; err != nil {
					t.Fatal(err)
				}
			} else if err := db.Model(&model.CatalogEntry{}).
				Where("generation_id = ? AND entry_id = ?", generationID, entryID).Updates(update).Error; err != nil {
				t.Fatal(err)
			}
			if err := authorizer.Reauthorize(context.Background(), actor, asset, content.DeliveryPreview); !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("reauthorization drift error=%v, want conflict", err)
			}
			if name == "Search classification" {
				if err := db.Model(&model.BackupAssetSearchDocument{}).
					Where("search_generation_id = ? AND document_id = ?", searchGenerationID, entryID).
					Updates(map[string]any{"sensitivity": string(search.SensitivitySecret), "classification_revision": 7}).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				restore := map[string]any{"normalized_path": "/safe/report.pdf", "name": "report.pdf", "mime_type": "application/pdf"}
				if err := db.Model(&model.CatalogEntry{}).
					Where("generation_id = ? AND entry_id = ?", generationID, entryID).Updates(restore).Error; err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	if err := db.Model(&model.CatalogGeneration{}).Where("id = ?", generationID).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Reauthorize(context.Background(), actor, asset, content.DeliveryPreview); err == nil {
		t.Fatal("reauthorization accepted an inactive Catalog generation")
	}
}

type runtimeContentManagerFake struct {
	events            *[]string
	prepareEnableErr  error
	prepareDisableErr error
	shutdownErr       error
	runStarted        chan struct{}
}

func (fake *runtimeContentManagerFake) Startup(context.Context) error {
	*fake.events = append(*fake.events, "content-startup")
	return nil
}

func (fake *runtimeContentManagerFake) PrepareEnable(context.Context, backupasset.ContentConfig) error {
	*fake.events = append(*fake.events, "content-prepare-enable")
	return fake.prepareEnableErr
}

func (fake *runtimeContentManagerFake) RestoreEnable(context.Context, backupasset.ContentConfig) error {
	*fake.events = append(*fake.events, "content-prepare-enable")
	return fake.prepareEnableErr
}

func (fake *runtimeContentManagerFake) PrepareDisable(context.Context) error {
	*fake.events = append(*fake.events, "content-prepare-disable")
	return fake.prepareDisableErr
}

func (fake *runtimeContentManagerFake) SetReady(ready bool) {
	*fake.events = append(*fake.events, fmt.Sprintf("content-ready-%t", ready))
}

func (fake *runtimeContentManagerFake) StopAccepting() {
	*fake.events = append(*fake.events, "content-stop-accepting")
}

func (fake *runtimeContentManagerFake) Run(ctx context.Context) {
	*fake.events = append(*fake.events, "content-run")
	if fake.runStarted != nil {
		close(fake.runStarted)
	}
	<-ctx.Done()
}

func (fake *runtimeContentManagerFake) Shutdown(context.Context) error {
	*fake.events = append(*fake.events, "content-shutdown")
	return fake.shutdownErr
}

func (fake *runtimeContentManagerFake) PrepareSchemaDown(_ context.Context, down func() error) error {
	*fake.events = append(*fake.events, "content-schema-drain")
	return down()
}

type runtimeFeatureTransitionerFake struct {
	events  *[]string
	enabled bool
}

func (fake *runtimeFeatureTransitionerFake) TransitionFeature(_ context.Context, enabled bool, persist func() error) error {
	*fake.events = append(*fake.events, fmt.Sprintf("admission-transition-%t", enabled))
	if err := persist(); err != nil {
		return err
	}
	fake.enabled = enabled
	return nil
}

func (fake *runtimeFeatureTransitionerFake) CurrentMode() (publication.AdmissionMode, error) {
	if fake.enabled {
		return publication.AdmissionManaged, nil
	}
	return publication.AdmissionPristineLegacy, nil
}

func (fake *runtimeFeatureTransitionerFake) PrepareApplicationDowngrade(_ context.Context, callback func() error) error {
	*fake.events = append(*fake.events, "admission-app-downgrade")
	return callback()
}

func (fake *runtimeFeatureTransitionerFake) PrepareSchemaDown(_ context.Context, callback func() error) error {
	*fake.events = append(*fake.events, "admission-schema-down")
	return callback()
}

func TestRuntimeContentTransitionAndSchemaDownOrdering(t *testing.T) {
	events := []string{}
	manager := &runtimeContentManagerFake{events: &events}
	transitioner := &runtimeFeatureTransitionerFake{events: &events}
	runtime := &Runtime{contentManager: manager, transitioner: transitioner, enablement: readyGAEnablement()}
	if runtime.FeatureTransitioner() != runtime {
		t.Fatal("runtime did not expose the composed Content feature transitioner")
	}
	if err := runtime.TransitionFeature(context.Background(), true, func() error {
		events = append(events, "persist-enabled")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.TransitionFeature(context.Background(), false, func() error {
		events = append(events, "persist-disabled")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.PrepareSchemaDown(context.Background(), func() error {
		events = append(events, "schema-down")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"content-prepare-enable", "admission-transition-true", "persist-enabled", "content-ready-true",
		"content-ready-false", "content-prepare-disable", "admission-transition-false", "persist-disabled",
		"content-ready-false", "content-schema-drain", "admission-schema-down", "schema-down",
	}
	if fmt.Sprint(events) != fmt.Sprint(want) {
		t.Fatalf("content transition order=%v, want %v", events, want)
	}
}

type runtimeExportSettingsManagerFake struct {
	events           *[]string
	globalEnabled    []bool
	configs          []backupasset.ExportConfig
	failAfterPersist error
	restoreCalls     int
}

type runtimeRecoveryManagerFake struct {
	events           *[]string
	configs          []backupasset.RecoveryConfig
	readiness        RecoveryDowngradeReadiness
	failAfterPersist bool
}

func (fake *runtimeRecoveryManagerFake) DowngradeReadiness(context.Context) (RecoveryDowngradeReadiness, error) {
	*fake.events = append(*fake.events, "recovery-downgrade-readiness")
	return fake.readiness, nil
}

func (fake *runtimeRecoveryManagerFake) StartupWithConfig(_ context.Context, config backupasset.RecoveryConfig) error {
	fake.configs = append(fake.configs, config)
	*fake.events = append(*fake.events, fmt.Sprintf("recovery-startup-%t", config.Enabled))
	return nil
}

func (fake *runtimeRecoveryManagerFake) TransitionSettings(
	_ context.Context,
	config backupasset.RecoveryConfig,
	persist func() error,
) error {
	fake.configs = append(fake.configs, config)
	*fake.events = append(*fake.events, fmt.Sprintf("recovery-settings-%t", config.Enabled))
	if err := persist(); err != nil {
		return err
	}
	if fake.failAfterPersist {
		return errors.New("injected Recovery install failure")
	}
	return nil
}

func (fake *runtimeRecoveryManagerFake) TransitionSettingsWithRestore(
	ctx context.Context,
	config backupasset.RecoveryConfig,
	persist func() error,
	restore func() error,
) error {
	err := fake.TransitionSettings(ctx, config, persist)
	if err != nil && restore != nil {
		return errors.Join(err, restore())
	}
	return err
}

func (fake *runtimeRecoveryManagerFake) StopAccepting() {
	*fake.events = append(*fake.events, "recovery-stop-accepting")
}

func (fake *runtimeRecoveryManagerFake) Run(context.Context) {
	*fake.events = append(*fake.events, "recovery-run")
}

func (fake *runtimeRecoveryManagerFake) Shutdown(context.Context) error {
	*fake.events = append(*fake.events, "recovery-shutdown")
	return nil
}

func (fake *runtimeRecoveryManagerFake) PrepareSchemaDown(_ context.Context, callback func() error) error {
	*fake.events = append(*fake.events, "recovery-schema-drain")
	return callback()
}

func TestRuntimeRecoveryLifecycleAndProspectiveSettingsAreCoordinated(t *testing.T) {
	events := []string{}
	recoveryManager := &runtimeRecoveryManagerFake{events: &events}
	exportManager := &runtimeExportSettingsManagerFake{events: &events}
	settingsService := settings.NewService(openRuntimeTestDB(t))
	effective, err := settingsService.BackupAssetSettingsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	current := make(map[string]string, len(effective))
	for key, value := range effective {
		current[key] = value
	}
	runtime := &Runtime{
		recoveryManager: recoveryManager,
		exportManager:   exportManager,
		transitioner:    &runtimeFeatureTransitionerFake{events: &events},
		settings:        settingsService,
	}

	effective["backup_assets.recovery.enabled"] = "true"
	effective["backup_assets.recovery.worker_concurrency"] = "3"
	if err := runtime.TransitionBackupAssetSettings(
		context.Background(), current,
		map[string]string{"backup_assets.recovery.enabled": "true", "backup_assets.recovery.worker_concurrency": "3"},
		effective, backupasset.ExportConfig{}, func() error {
			events = append(events, "persist")
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if len(recoveryManager.configs) != 1 || !recoveryManager.configs[0].Enabled ||
		recoveryManager.configs[0].WorkerConcurrency != 3 {
		t.Fatalf("Recovery transition configs=%+v", recoveryManager.configs)
	}
	if got, want := fmt.Sprint(events), "[admission-transition-false export-settings-false recovery-settings-true persist]"; got != want {
		t.Fatalf("Recovery settings order=%s want=%s", got, want)
	}

	events = events[:0]
	runtime.StopAccepting()
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !containsManagedRecoveryOrderedEvents(events, []string{
		"recovery-stop-accepting", "recovery-stop-accepting", "recovery-shutdown",
	}) {
		t.Fatalf("Recovery shutdown events=%v", events)
	}
}

func TestRecoveryTransitionRestoresPriorPersistedSettingsThroughRuntimeOwner(t *testing.T) {
	db := openRuntimeTestDB(t)
	settingsService := settings.NewService(db)
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settingsService, Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{},
		ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := settingsService.BackupAssetSettingsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	effective := make(map[string]string, len(current))
	for key, value := range current {
		effective[key] = value
	}
	effective["backup_assets.recovery.enabled"] = "true"
	events := []string{}
	runtime.recoveryManager = &runtimeRecoveryManagerFake{events: &events, failAfterPersist: true}
	runtime.exportManager = &runtimeExportSettingsManagerFake{events: &events}
	runtime.transitioner = &runtimeFeatureTransitionerFake{events: &events}

	err = runtime.TransitionBackupAssetSettings(
		context.Background(), current,
		map[string]string{"backup_assets.recovery.enabled": "true"}, effective,
		backupasset.ExportConfig{},
		func() error { return settingsService.Update("backup_assets.recovery.enabled", "true") },
	)
	if err == nil {
		t.Fatal("injected Recovery install failure unexpectedly succeeded")
	}
	if got := settingsService.GetEffective("backup_assets.recovery.enabled"); got != current["backup_assets.recovery.enabled"] {
		t.Fatalf("persisted Recovery enabled=%q, want restored prior %q", got, current["backup_assets.recovery.enabled"])
	}
}

func TestRuntimeNewOwnsDefaultDisabledRecoveryReceiptMaintenance(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RUNTIME_RECOVERY_DEFAULT_DISABLED_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.BackupAssetRecoveryEvidence{}); err != nil {
		t.Fatal(err)
	}
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settings.NewService(db), Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{},
		SessionRevocations: &runtimeSessionRevocationsFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.recoveryManager == nil {
		t.Fatal("Runtime.New did not construct Recovery lifecycle owner")
	}
	manager, ok := runtime.recoveryManager.(*managedRecoveryRuntime)
	if !ok || manager.receiptOwner == nil || manager.downgradeInspector == nil {
		t.Fatalf("Recovery manager=%T maintenance authorities unavailable", runtime.recoveryManager)
	}
	config, err := runtime.foundation.RecoveryConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Enabled {
		t.Fatal("test requires default-disabled Recovery")
	}
	if err := manager.StartupWithConfig(context.Background(), config); err != nil {
		t.Fatalf("default-disabled Recovery startup: %v", err)
	}
	installed := manager.publication.current()
	if installed == nil || installed != manager.graph || installed.resultLifecycle == nil || installed.cleanup == nil ||
		installed.cleanup.lifecycle != installed.resultLifecycle || installed.run == nil {
		t.Fatalf("default-disabled Recovery maintenance graph=%+v", installed)
	}
	if _, release, admitted := manager.publication.acquireAdmission(); admitted {
		release()
		t.Fatal("default-disabled Recovery published a mutation facade")
	}
}

func TestRuntimeExposesRecoveryDowngradeReadinessFacade(t *testing.T) {
	events := []string{}
	manager := &runtimeRecoveryManagerFake{
		events: &events,
		readiness: RecoveryDowngradeReadiness{
			State:               RecoveryDowngradePristineAllowed,
			AdmissionGeneration: "recovery-downgrade-test",
		},
	}
	runtime := &Runtime{recoveryManager: manager}

	result, err := runtime.RecoveryDowngradeReadiness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result != manager.readiness {
		t.Fatalf("readiness=%+v, want %+v", result, manager.readiness)
	}
	if got := fmt.Sprint(events); got != "[recovery-downgrade-readiness]" {
		t.Fatalf("readiness events=%s", got)
	}
}

func TestManagedRecoveryDowngradeFacadePersistsReceiptFirstReplayWithoutRawAuthority(t *testing.T) {
	db := openRuntimeTestDB(t)
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	owner := &runtimeRecoveryDowngradeTransitionFake{result: RecoveryDowngradeReadiness{
		State: RecoveryDowngradeBlocked, AdmissionGeneration: "recovery-downgrade-" + strings.Repeat("a", 32),
		Blockers: RecoveryDowngradeBlockers{Jobs: 1},
	}}
	facade, err := newManagedRecoveryDowngradeFacade(db, owner, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	audit := &runtimeRecoveryAdministrationAuditSpy{err: errors.New("FAKE_DOWNGRADE_AUDIT_FAILURE_FOR_TEST_ONLY")}
	facade.audit = audit
	request := RecoveryDowngradeReadinessRequest{
		RequesterID: 1, Endpoint: "/api/v1/settings/backup-assets/recovery/downgrade-readiness",
		IdempotencyKey: "downgrade-readiness-idempotency-key", SessionJTI: strings.Repeat("9", 32),
		SessionRole: "admin", SessionTokenVersion: 1, SessionExpiresAt: now.Add(time.Hour),
		Reason: "FAKE_PRIVATE_DOWNGRADE_REASON_FOR_TEST_ONLY",
	}
	first, err := facade.RequestRecoveryDowngradeReadiness(context.Background(), request)
	if err != nil || first.Replay || owner.calls != 1 {
		t.Fatalf("first readiness=%+v err=%v calls=%d", first, err, owner.calls)
	}
	if len(audit.inputs) != 1 || audit.inputs[0].Action != backupasset.AuditActionRecoveryAdministration ||
		audit.inputs[0].Actor.UserID != request.RequesterID ||
		audit.inputs[0].Fields[backupasset.AuditFieldOperation] != "downgrade_readiness" ||
		strings.Contains(fmt.Sprintf("%+v", audit.inputs[0]), request.Reason) {
		t.Fatalf("downgrade administration audit=%+v", audit.inputs)
	}
	replayed, err := facade.RequestRecoveryDowngradeReadiness(context.Background(), request)
	if err != nil || !replayed.Replay || owner.calls != 1 || replayed.State != first.State ||
		replayed.AdmissionGeneration != first.AdmissionGeneration || replayed.Blockers != first.Blockers {
		t.Fatalf("replayed readiness=%+v err=%v calls=%d", replayed, err, owner.calls)
	}
	if len(audit.inputs) != 1 {
		t.Fatalf("downgrade replay duplicated audit projection: %+v", audit.inputs)
	}
	changed := request
	changed.Reason = "FAKE_CHANGED_PRIVATE_DOWNGRADE_REASON_FOR_TEST_ONLY"
	if _, err := facade.RequestRecoveryDowngradeReadiness(context.Background(), changed); !errors.Is(err, ErrRecoveryDowngradeIdempotencyConflict) {
		t.Fatalf("changed-intent error=%v", err)
	}
	otherSession := request
	otherSession.SessionJTI = strings.Repeat("8", 32)
	if _, err := facade.RequestRecoveryDowngradeReadiness(context.Background(), otherSession); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("other-session error=%v", err)
	}
	now = request.SessionExpiresAt
	if _, err := facade.RequestRecoveryDowngradeReadiness(context.Background(), request); !errors.Is(err, ErrRecoveryDowngradeIdempotencyConflict) {
		t.Fatalf("expired replay error=%v", err)
	}
	var rows []model.SystemSetting
	if err := db.Where("key LIKE ?", settings.RecoveryDowngradeReceiptKeyPrefix+"%").Find(&rows).Error; err != nil || len(rows) != 1 {
		t.Fatalf("downgrade receipts=%d err=%v", len(rows), err)
	}
	for _, forbidden := range []string{request.IdempotencyKey, request.SessionJTI, request.Reason} {
		if strings.Contains(rows[0].Key+rows[0].Value, forbidden) {
			t.Fatalf("downgrade receipt leaked private authority %q", forbidden)
		}
	}
}

type runtimeRecoveryDowngradeTransitionFake struct {
	result       RecoveryDowngradeReadiness
	calls        int
	inspectCalls int
}

func (fake *runtimeRecoveryDowngradeTransitionFake) DowngradeReadiness(
	context.Context,
) (RecoveryDowngradeReadiness, error) {
	fake.calls++
	return fake.result, nil
}

func (fake *runtimeRecoveryDowngradeTransitionFake) InspectDowngradeReadiness(
	context.Context,
) (RecoveryDowngradeReadiness, bool, error) {
	fake.inspectCalls++
	return fake.result, fake.calls > 0, nil
}

type runtimeOverlayKeySourceUnused struct{}

func (runtimeOverlayKeySourceUnused) Active(context.Context, backupasset.KeyDomain) (backupasset.DomainKeyMaterial, error) {
	return backupasset.DomainKeyMaterial{}, errors.New("FAKE_RUNTIME_OVERLAY_KEY_SOURCE_UNUSED_FOR_TEST_ONLY")
}

func (*runtimeExportSettingsManagerFake) Startup(context.Context) error { return nil }
func (*runtimeExportSettingsManagerFake) Ready() bool                   { return true }
func (*runtimeExportSettingsManagerFake) Service() *managedExportServiceFacade {
	return nil
}
func (*runtimeExportSettingsManagerFake) Delivery() *managedExportDeliveryFacade { return nil }
func (*runtimeExportSettingsManagerFake) StopAccepting()                         {}
func (*runtimeExportSettingsManagerFake) Run(context.Context)                    {}
func (*runtimeExportSettingsManagerFake) Shutdown(context.Context) error         { return nil }
func (*runtimeExportSettingsManagerFake) PrepareSchemaDown(context.Context, func() error) error {
	return nil
}
func (fake *runtimeExportSettingsManagerFake) TransitionSettings(
	_ context.Context,
	globalEnabled bool,
	config backupasset.ExportConfig,
	persist func() error,
) error {
	fake.globalEnabled = append(fake.globalEnabled, globalEnabled)
	fake.configs = append(fake.configs, config)
	*fake.events = append(*fake.events, fmt.Sprintf("export-settings-%t", globalEnabled))
	if err := persist(); err != nil {
		return err
	}
	return fake.failAfterPersist
}

func (fake *runtimeExportSettingsManagerFake) TransitionSettingsWithRestore(
	ctx context.Context,
	globalEnabled bool,
	config backupasset.ExportConfig,
	persist func() error,
	restore func() error,
) error {
	err := fake.TransitionSettings(ctx, globalEnabled, config, persist)
	if err == nil {
		return nil
	}
	fake.restoreCalls++
	return errors.Join(err, restore())
}

func TestRuntimeBackupAssetSettingsTransitionCoordinatesGlobalDisable(t *testing.T) {
	settingsService := settings.NewService(openRuntimeTestDB(t))
	if err := settingsService.Update("backup_assets.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	events := []string{}
	exportManager := &runtimeExportSettingsManagerFake{events: &events}
	runtime := &Runtime{
		exportManager:  exportManager,
		contentManager: &runtimeContentManagerFake{events: &events},
		transitioner:   &runtimeFeatureTransitionerFake{events: &events},
		settings:       settingsService,
	}
	transitioner, ok := any(runtime).(interface {
		TransitionBackupAssetSettings(
			context.Context,
			map[string]string,
			map[string]string,
			map[string]string,
			backupasset.ExportConfig,
			func() error,
		) error
	})
	if !ok {
		t.Fatal("Runtime does not provide backup asset settings transition")
	}
	current := runtimeFoundationSettings(true)
	effective := runtimeFoundationSettings(false)
	effective["backup_assets.export.worker_concurrency"] = "3"
	config, err := backupasset.ExportConfigFromValues(effective)
	if err != nil {
		t.Fatal(err)
	}
	if err := transitioner.TransitionBackupAssetSettings(
		context.Background(),
		current,
		map[string]string{"backup_assets.enabled": "false"},
		effective,
		config,
		func() error {
			events = append(events, "persist")
			return settingsService.UpdateMany(map[string]string{"backup_assets.enabled": "false"})
		},
	); err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(events), "[content-ready-false content-prepare-disable admission-transition-false export-settings-false persist]"; got != want {
		t.Fatalf("global settings transition order=%s want=%s", got, want)
	}
	if got := fmt.Sprint(exportManager.globalEnabled); got != "[false]" || len(exportManager.configs) != 1 || exportManager.configs[0] != config {
		t.Fatalf("Export settings calls enabled=%v configs=%+v", exportManager.globalEnabled, exportManager.configs)
	}
}

func TestRuntimeBackupAssetSettingsTransitionCoordinatesIdempotencyAcrossExportAndOverlay(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RUNTIME_OVERLAY_IDEMPOTENCY_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.BackupAssetOverlayUsage{}, &model.BackupAssetOverlayIdempotency{}, &model.BackupAssetRecentAccess{},
	); err != nil {
		t.Fatalf("migrate runtime Overlay idempotency fixture: %v", err)
	}
	now := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	overlayService, err := overlay.NewService(overlay.ServiceDependencies{
		DB: db, Keys: runtimeOverlayKeySourceUnused{}, Assets: runtimeOverlayAuthorizationAllowAll{}, Points: runtimeOverlayAuthorizationAllowAll{},
		Now: func() time.Time { return now }, Config: overlay.DefaultConfig(),
		FeatureEnabled: func() (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatalf("construct Overlay service: %v", err)
	}
	settingsService := settings.NewService(db)
	events := []string{}
	exportManager := &runtimeExportSettingsManagerFake{events: &events}
	runtime := &Runtime{
		overlayService: overlayService,
		exportManager:  exportManager,
		transitioner:   &runtimeFeatureTransitionerFake{events: &events},
		settings:       settingsService,
	}
	current := runtimeFoundationSettings(false)
	effective := runtimeFoundationSettings(false)
	effective["backup_assets.idempotency_ttl"] = "2h"
	effective["backup_assets.idempotency_key_max_bytes"] = "32"
	config, err := backupasset.ExportConfigFromValues(effective)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.TransitionBackupAssetSettings(
		context.Background(),
		current,
		map[string]string{"backup_assets.idempotency_ttl": "2h", "backup_assets.idempotency_key_max_bytes": "32"},
		effective,
		config,
		func() error {
			events = append(events, "persist")
			return settingsService.UpdateMany(map[string]string{
				"backup_assets.idempotency_ttl":           "2h",
				"backup_assets.idempotency_key_max_bytes": "32",
			})
		},
	); err != nil {
		t.Fatalf("transition idempotency settings: %v", err)
	}
	if got, want := fmt.Sprint(events), "[admission-transition-false export-settings-false persist]"; got != want {
		t.Fatalf("idempotency transition order=%s want=%s", got, want)
	}
	if len(exportManager.configs) != 1 || exportManager.configs[0] != config {
		t.Fatalf("Export configuration=%+v, want %+v", exportManager.configs, config)
	}
	if _, err := overlayService.ClearRecent(context.Background(), 761, strings.Repeat("a", 64)); !errors.Is(err, overlay.ErrInvalidOverlay) {
		t.Fatalf("Overlay retained stale idempotency key limit: %v", err)
	}
	key := strings.Repeat("b", 32)
	if _, err := overlayService.ClearRecent(context.Background(), 761, key); err != nil {
		t.Fatalf("create receipt with transitioned Overlay settings: %v", err)
	}
	var receipt model.BackupAssetOverlayIdempotency
	if err := db.Where("owner_user_id = ? AND action = ?", 761, "recent_clear").Take(&receipt).Error; err != nil {
		t.Fatalf("load transitioned Overlay receipt: %v", err)
	}
	if want := now.Add(2 * time.Hour); !receipt.ExpiresAt.Equal(want) {
		t.Fatalf("transitioned Overlay receipt expiry=%s, want %s", receipt.ExpiresAt, want)
	}

	persistErr := errors.New("FAKE_RUNTIME_IDEMPOTENCY_PERSIST_FAILURE_FOR_TEST_ONLY")
	failedCurrent := runtimeFoundationSettings(false)
	failedCurrent["backup_assets.idempotency_ttl"] = "2h"
	failedCurrent["backup_assets.idempotency_key_max_bytes"] = "32"
	failedEffective := runtimeFoundationSettings(false)
	failedEffective["backup_assets.idempotency_ttl"] = "3h"
	failedEffective["backup_assets.idempotency_key_max_bytes"] = "64"
	if err := runtime.TransitionBackupAssetSettings(
		context.Background(), failedCurrent,
		map[string]string{"backup_assets.idempotency_ttl": "3h", "backup_assets.idempotency_key_max_bytes": "64"},
		failedEffective,
		backupasset.ExportConfig{Enabled: false, IdempotencyTTL: 3 * time.Hour, IdempotencyKeyMaxBytes: 64},
		func() error { return persistErr },
	); !errors.Is(err, persistErr) {
		t.Fatalf("failed idempotency transition error=%v, want %v", err, persistErr)
	}
	rollbackKey := strings.Repeat("c", 32)
	if _, err := overlayService.ClearRecent(context.Background(), 762, rollbackKey); err != nil {
		t.Fatalf("failed transition changed Overlay idempotency key limit: %v", err)
	}
	var rollbackReceipt model.BackupAssetOverlayIdempotency
	if err := db.Where("owner_user_id = ? AND action = ?", 762, "recent_clear").Take(&rollbackReceipt).Error; err != nil {
		t.Fatalf("load rollback Overlay receipt: %v", err)
	}
	if want := now.Add(2 * time.Hour); !rollbackReceipt.ExpiresAt.Equal(want) {
		t.Fatalf("failed transition changed Overlay receipt expiry=%s, want %s", rollbackReceipt.ExpiresAt, want)
	}
}

func TestRuntimeContentTransitionRestoresLifecycleAfterPersistenceFailure(t *testing.T) {
	persistErr := errors.New("FAKE_CONTENT_PERSIST_FAILURE_FOR_TEST_ONLY")
	t.Run("failed enable drains provisional content runtime", func(t *testing.T) {
		events := []string{}
		manager := &runtimeContentManagerFake{events: &events}
		runtime := &Runtime{contentManager: manager, transitioner: &runtimeFeatureTransitionerFake{events: &events}, enablement: readyGAEnablement()}
		err := runtime.TransitionFeature(context.Background(), true, func() error {
			events = append(events, "persist-enabled")
			return persistErr
		})
		if !errors.Is(err, persistErr) {
			t.Fatalf("enable persistence error=%v", err)
		}
		want := []string{
			"content-prepare-enable", "admission-transition-true", "persist-enabled",
			"content-ready-false", "admission-transition-false", "content-prepare-disable",
		}
		if fmt.Sprint(events) != fmt.Sprint(want) {
			t.Fatalf("failed enable lifecycle=%v, want %v", events, want)
		}
	})

	t.Run("failed disable restores content runtime", func(t *testing.T) {
		events := []string{}
		manager := &runtimeContentManagerFake{events: &events}
		runtime := &Runtime{contentManager: manager, transitioner: &runtimeFeatureTransitionerFake{events: &events}}
		err := runtime.TransitionFeature(context.Background(), false, func() error {
			events = append(events, "persist-disabled")
			return persistErr
		})
		if !errors.Is(err, persistErr) {
			t.Fatalf("disable persistence error=%v", err)
		}
		want := []string{
			"content-ready-false", "content-prepare-disable", "admission-transition-false", "persist-disabled",
			"admission-transition-true", "content-prepare-enable", "content-ready-true",
		}
		if fmt.Sprint(events) != fmt.Sprint(want) {
			t.Fatalf("failed disable lifecycle=%v, want %v", events, want)
		}
	})

	t.Run("failed disable and failed restore remain not ready", func(t *testing.T) {
		events := []string{}
		restoreErr := errors.New("FAKE_CONTENT_RESTORE_FAILURE_FOR_TEST_ONLY")
		manager := &runtimeContentManagerFake{events: &events, prepareEnableErr: restoreErr}
		runtime := &Runtime{contentManager: manager, transitioner: &runtimeFeatureTransitionerFake{events: &events}}
		err := runtime.TransitionFeature(context.Background(), false, func() error { return persistErr })
		if !errors.Is(err, persistErr) || !errors.Is(err, restoreErr) {
			t.Fatalf("joined disable/restore error=%v", err)
		}
		if got := events[len(events)-1]; got != "content-ready-false" {
			t.Fatalf("failed restore final lifecycle=%v", events)
		}
	})
}

func TestRuntimeContentStopRunAndShutdownAreAlwaysJoined(t *testing.T) {
	events := []string{}
	shutdownErr := errors.New("FAKE_CONTENT_SHUTDOWN_FAILURE_FOR_TEST_ONLY")
	manager := &runtimeContentManagerFake{events: &events, shutdownErr: shutdownErr, runStarted: make(chan struct{})}
	runtime := &Runtime{contentManager: manager}
	runtime.StopAccepting()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runtime.Run(ctx)
		close(done)
	}()
	<-manager.runStarted
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runtime did not join Content Run after cancellation")
	}
	if err := runtime.Shutdown(context.Background()); !errors.Is(err, shutdownErr) {
		t.Fatalf("runtime shutdown got %v, want Content error", err)
	}
	if fmt.Sprint(events) != fmt.Sprint([]string{"content-stop-accepting", "content-run", "content-stop-accepting", "content-shutdown"}) {
		t.Fatalf("content lifecycle events=%v", events)
	}
}

func TestRuntimeSearchStartupDisabledTouchesNoKeyOrWorker(t *testing.T) {
	db := openRuntimeTestDB(t)
	backend := newSearchWorkerBackendFake()
	worker, err := NewSearchWorker(SearchWorkerDependencies{
		Config: func() (SearchWorkerConfig, error) {
			return SearchWorkerConfig{Enabled: true, ReconcileInterval: time.Minute, ReconcileBatchSize: 10, WorkerConcurrency: 1}, nil
		},
		Backend: backend,
	})
	if err != nil {
		t.Fatalf("NewSearchWorker: %v", err)
	}
	runtime := &Runtime{
		foundation:   backupasset.NewFoundationService(settings.NewService(db)),
		keyring:      backupasset.NewKeyring(db, nil),
		searchWorker: worker,
	}
	if err := runtime.startupSearch(context.Background()); err != nil {
		t.Fatalf("disabled Search startup: %v", err)
	}
	if backend.calls() != (searchWorkerCalls{}) {
		t.Fatalf("disabled Search startup touched worker backend: %+v", backend.calls())
	}
	if db.Migrator().HasTable(&model.WrappedDomainKey{}) {
		t.Fatal("disabled Search startup created or required the key table")
	}
}

func TestRuntimeSearchStartupEnsuresKeyReconcilesAndTreatsRecordedLossAsUnavailable(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RUNTIME_SEARCH_KEK_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.WrappedDomainKey{}); err != nil {
		t.Fatalf("migrate wrapped keys: %v", err)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.enabled", "true"); err != nil {
		t.Fatalf("enable backup assets: %v", err)
	}
	backend := newSearchWorkerBackendFake()
	worker, err := NewSearchWorker(SearchWorkerDependencies{
		Config: func() (SearchWorkerConfig, error) {
			return SearchWorkerConfig{Enabled: true, ReconcileInterval: time.Minute, ReconcileBatchSize: 10, WorkerConcurrency: 1}, nil
		},
		Backend: backend,
	})
	if err != nil {
		t.Fatalf("NewSearchWorker: %v", err)
	}
	ring := backupasset.NewKeyring(db, nil)
	runtime := &Runtime{foundation: backupasset.NewFoundationService(settingsService), keyring: ring, searchWorker: worker, enablement: readyGAEnablement()}
	if err := runtime.startupSearch(context.Background()); err != nil {
		t.Fatalf("enabled Search startup: %v", err)
	}
	material, err := ring.Active(context.Background(), backupasset.KeyDomainSearchToken)
	if err != nil || material.Version != 1 {
		t.Fatalf("enabled startup did not ensure Search key: material=%+v err=%v", material, err)
	}
	if calls := backend.calls(); calls.reconcile != 1 || calls.list != 1 {
		t.Fatalf("enabled startup did not reconcile Search: %+v", calls)
	}

	if err := ring.MarkRebuildableLost(context.Background(), backupasset.KeyDomainSearchToken, material.Version, func(context.Context, *gorm.DB, backupasset.RebuildableKeyTransition) error {
		return nil
	}); err != nil {
		t.Fatalf("record Search key loss: %v", err)
	}
	before := backend.calls()
	if err := runtime.startupSearch(context.Background()); err != nil {
		t.Fatalf("intentional Search key loss should preserve Catalog runtime: %v", err)
	}
	if after := backend.calls(); after != before {
		t.Fatalf("lost Search key still ran worker: before=%+v after=%+v", before, after)
	}
	if _, err := ring.Active(context.Background(), backupasset.KeyDomainSearchToken); !errors.Is(err, backupasset.ErrKeyLost) {
		t.Fatalf("lost Search key was regenerated: %v", err)
	}
}

func TestRuntimeSearchStartupUnexpectedUnwrapFailureIsFatal(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RUNTIME_SEARCH_OLD_KEK_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.WrappedDomainKey{}); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	ring := backupasset.NewKeyring(db, nil)
	if _, err := ring.Ensure(context.Background(), backupasset.KeyDomainSearchToken); err != nil {
		t.Fatalf("seed Search key: %v", err)
	}
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RUNTIME_SEARCH_NEW_KEK_FOR_TEST_ONLY")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	worker, _ := NewSearchWorker(SearchWorkerDependencies{
		Config: func() (SearchWorkerConfig, error) {
			return SearchWorkerConfig{Enabled: true, ReconcileInterval: time.Minute, ReconcileBatchSize: 10, WorkerConcurrency: 1}, nil
		},
		Backend: newSearchWorkerBackendFake(),
	})
	runtime := &Runtime{foundation: backupasset.NewFoundationService(settingsService), keyring: ring, searchWorker: worker, enablement: readyGAEnablement()}
	if err := runtime.startupSearch(context.Background()); !errors.Is(err, backupasset.ErrKeyUnavailable) {
		t.Fatalf("unexpected Search unwrap failure got %v, want fatal key unavailable", err)
	}
}

func TestRuntimeSearchStartupIsolatesUnreadableDerivedKey(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RUNTIME_DERIVED_ISOLATION_KEK_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.WrappedDomainKey{}); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	ring := backupasset.NewKeyring(db, nil)
	derived, err := ring.Ensure(context.Background(), backupasset.KeyDomainDerivedStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.WrappedDomainKey{}).
		Where("domain = ? AND version = ?", backupasset.KeyDomainDerivedStore, derived.Version).
		Update("wrapped_key", []byte("corrupt-derived-envelope")).Error; err != nil {
		t.Fatal(err)
	}
	backend := newSearchWorkerBackendFake()
	worker, err := NewSearchWorker(SearchWorkerDependencies{
		Config: func() (SearchWorkerConfig, error) {
			return SearchWorkerConfig{Enabled: true, ReconcileInterval: time.Minute, ReconcileBatchSize: 10, WorkerConcurrency: 1}, nil
		},
		Backend: backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{foundation: backupasset.NewFoundationService(settingsService), keyring: ring, searchWorker: worker, enablement: readyGAEnablement()}
	if err := runtime.startupSearch(context.Background()); err != nil {
		t.Fatalf("Derived key failure blocked Core Search startup: %v", err)
	}
	if calls := backend.calls(); calls.reconcile != 1 || calls.list != 1 {
		t.Fatalf("Core Search did not start after isolated Derived failure: %+v", calls)
	}
	if _, err := ring.Active(context.Background(), backupasset.KeyDomainSearchToken); err != nil {
		t.Fatalf("Search key unavailable after isolated Derived failure: %v", err)
	}
	if _, err := ring.Active(context.Background(), backupasset.KeyDomainDerivedStore); !errors.Is(err, backupasset.ErrKeyUnavailable) {
		t.Fatalf("corrupt Derived key unexpectedly became readable: %v", err)
	}
}

func TestRuntimeSearchTokenOperationsCoordinateInvalidationReadinessAndLoss(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RUNTIME_SEARCH_OPERATIONS_KEK_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.WrappedDomainKey{}, &model.BackupAssetSearchGeneration{}, &model.BackupAssetTagDefinition{},
	); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	ring := backupasset.NewKeyring(db, nil)
	before, err := ring.Ensure(context.Background(), backupasset.KeyDomainSearchToken)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := search.NormalizeFieldV1(search.SearchFieldTag, "finance", search.DefaultNormalizerLimits())
	if err != nil {
		t.Fatal(err)
	}
	token, err := search.TokenHMAC(before.Key, before.Version, search.NormalizerVersion, search.SearchFieldTag, search.TokenKindExact, normalized.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.Create(&model.BackupAssetTagDefinition{
		ID: strings.Repeat("a", 32), OwnerUserID: 1, EncryptedName: normalized.Canonical,
		NameToken: token, KeyVersion: before.Version, TokenState: "active", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	authorizer := runtimeOverlayAuthorizationAllowAll{}
	overlays, err := overlay.NewService(overlay.ServiceDependencies{
		DB: db, Keys: ring, Assets: authorizer, Points: authorizer, Config: overlay.DefaultConfig(),
		FeatureEnabled: func() (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ready := &atomic.Bool{}
	ready.Store(true)
	runtime := &Runtime{
		foundation: backupasset.NewFoundationService(settingsService), keyring: ring,
		overlayService: overlays, searchReady: ready, enablement: readyGAEnablement(),
	}
	after, err := runtime.ReplaceSearchTokenForReindex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != before.Version+1 || !ready.Load() {
		t.Fatalf("replacement material=%+v ready=%t", after, ready.Load())
	}
	var tag model.BackupAssetTagDefinition
	if err := db.Where("id = ?", strings.Repeat("a", 32)).Take(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if tag.TokenState != "rekeying" || tag.KeyVersion != before.Version {
		t.Fatalf("replacement did not gate old tag token: %+v", tag)
	}
	if err := runtime.MarkSearchTokenLost(context.Background(), after.Version); err != nil {
		t.Fatal(err)
	}
	if ready.Load() {
		t.Fatal("recorded Search Token loss left worker readiness enabled")
	}
	if _, err := ring.Active(context.Background(), backupasset.KeyDomainSearchToken); !errors.Is(err, backupasset.ErrKeyLost) {
		t.Fatalf("active Search Token after recorded loss: %v", err)
	}
}

type runtimeOverlayAuthorizationAllowAll struct{}

func (runtimeOverlayAuthorizationAllowAll) AuthorizeAsset(context.Context, *gorm.DB, overlay.Actor, backupasset.AssetRef) error {
	return nil
}

func (runtimeOverlayAuthorizationAllowAll) AuthorizePoints(context.Context, overlay.Actor, []string) error {
	return nil
}

func TestRuntimeSearchShutdownStopsAdmissionAndJoinsSearchBeforePublication(t *testing.T) {
	fixture := newAdmissionControllerFixture(t, true, nil)
	fixture.initialize(t)
	searchBackend := newSearchWorkerBackendFake()
	searchBackend.candidates = []search.BuildCandidate{{RepositoryID: "repo-a", RecoveryPointID: "point-a"}}
	searchWorker, err := NewSearchWorker(SearchWorkerDependencies{
		Config: func() (SearchWorkerConfig, error) {
			return SearchWorkerConfig{Enabled: true, ReconcileInterval: time.Hour, ReconcileBatchSize: 10, WorkerConcurrency: 1}, nil
		},
		Backend: searchBackend,
	})
	if err != nil {
		t.Fatal(err)
	}
	searchCtx, searchCancel := context.WithCancel(context.Background())
	t.Cleanup(searchCancel)
	go searchWorker.Run(searchCtx)
	_ = searchBackend.waitStarted(t)
	searchActiveAtPublicationCancel := make(chan int, 1)
	reconciler := &shutdownOrderReconciler{
		started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}),
		beforeCanceled: func() { searchActiveAtPublicationCancel <- searchBackend.active() },
	}
	worker, err := NewPublicationWorker(PublicationWorkerDependencies{
		Foundation: fixture.controller.foundation, Reconciler: reconciler, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	pointID := strings.Repeat("a", 32)
	go worker.process(context.Background(), pointID)
	select {
	case <-reconciler.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not begin shutdown-order fixture work")
	}
	runtime := &Runtime{admission: fixture.controller, worker: worker, searchWorker: searchWorker}
	done := make(chan error, 1)
	go func() { done <- runtime.Shutdown(context.Background()) }()
	select {
	case <-reconciler.canceled:
	case <-time.After(time.Second):
		t.Fatal("runtime shutdown did not cancel active worker work")
	}
	if active := <-searchActiveAtPublicationCancel; active != 0 {
		close(reconciler.release)
		<-done
		t.Fatalf("runtime canceled publication before joining Search work: active=%d", active)
	}
	token, acquireErr := fixture.controller.Acquire(context.Background(), publication.OperationManifest)
	if token != nil {
		_ = token.Close()
	}
	if !errors.Is(acquireErr, ErrAdmissionStopped) {
		close(reconciler.release)
		<-done
		t.Fatalf("shutdown admitted a new publication token after worker cancellation: %v", acquireErr)
	}
	close(reconciler.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type shutdownOrderReconciler struct {
	started        chan struct{}
	canceled       chan struct{}
	release        chan struct{}
	beforeCanceled func()
}

func (*shutdownOrderReconciler) ListCandidates(context.Context, int) ([]string, error) {
	return nil, nil
}
func (reconciler *shutdownOrderReconciler) ProcessPoint(ctx context.Context, pointID string) (publication.Outcome, error) {
	close(reconciler.started)
	<-ctx.Done()
	if reconciler.beforeCanceled != nil {
		reconciler.beforeCanceled()
	}
	close(reconciler.canceled)
	<-reconciler.release
	return publication.Outcome{RecoveryPointID: pointID}, nil
}
func (*shutdownOrderReconciler) HasUnresolvedPublication(context.Context) (bool, error) {
	return false, nil
}

func TestRuntimeConstructsAndJoinsRetentionWorker(t *testing.T) {
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.BackupRepository{}, &model.RecoveryPoint{}, &model.TaskRepositoryLink{},
		&model.BackupRetentionPolicy{}, &model.RecoveryPointHold{},
		&model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLifecycleTombstone{},
		&model.BackupAssetManagedHistoryLatch{},
	); err != nil {
		t.Fatal(err)
	}
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settings.NewService(db), Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{},
		ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{},
		Now: func() time.Time { return time.Date(2026, 8, 18, 13, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("construct runtime: %v", err)
	}
	if runtime.retentionWorker == nil {
		t.Fatal("runtime omitted the retention worker")
	}
	if runtime.RetentionPolicies() == nil || runtime.RetentionHolds() == nil || runtime.RetentionPurge() == nil {
		t.Fatal("runtime omitted handler-facing policy/hold/purge facades")
	}
	if runtime.RepositoryService() == nil {
		t.Fatal("runtime omitted repository import/rebuild surface")
	}

	if err := runtime.StartupPass(context.Background()); err != nil {
		t.Fatalf("StartupPass: %v", err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.Run(runCtx)
	}()
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutdownCancel()
	if err := runtime.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown did not join retention worker: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Runtime.Run did not return after Shutdown")
	}
}

func TestRuntimeRetentionStartupPassUsesConstructedWorker(t *testing.T) {
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.User{}, &model.BackupRepository{}, &model.RecoveryPoint{},
		&model.TaskRepositoryLink{}, &model.BackupAssetManagedHistoryLatch{},
		&model.BackupRetentionPolicy{}, &model.RecoveryPointHold{},
		&model.RecoveryPointLease{}, &model.RecoveryPointLifecycleAttempt{},
		&model.RecoveryPointLifecycleTombstone{},
	); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.enabled", "false"); err != nil {
		t.Fatal(err)
	}
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settingsService, Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{},
		ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{},
		Now: func() time.Time { return time.Date(2026, 8, 18, 13, 5, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("construct runtime: %v", err)
	}
	if runtime.retentionWorker == nil {
		t.Fatal("runtime omitted the retention worker")
	}
	if err := runtime.StartupPass(context.Background()); err != nil {
		t.Fatalf("disabled retention StartupPass: %v", err)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestRuntimeRetentionInjectsTombstoneSourceBeforeAdmission(t *testing.T) {
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.BackupRepository{}, &model.RecoveryPoint{}, &model.TaskRepositoryLink{},
		&model.RecoveryPointLifecycleTombstone{}, &model.BackupAssetManagedHistoryLatch{},
		&model.RecoveryPointLease{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 13, 10, 0, 0, time.UTC)
	repositoryID := strings.Repeat("a", 32)
	pointID := strings.Repeat("b", 32)
	if err := db.Create(&model.RecoveryPointLifecycleTombstone{
		RecoveryPointID: pointID, RepositoryID: repositoryID,
		OriginalSemantics: string(backupasset.PointNativeSnapshot),
		TerminalOperation: string(backupasset.LifecycleRetentionExpire),
		TerminalState:     string(backupasset.RecoveryPointExpired),
		ManagedHistory:    true, ResultCode: "provider_deleted", CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed lifecycle tombstone: %v", err)
	}
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settings.NewService(db), Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{},
		ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("construct runtime: %v", err)
	}
	if runtime.admission == nil || runtime.admission.history == nil {
		t.Fatal("runtime omitted managed-history admission")
	}
	repositoryHistory, err := runtime.admission.history.HasRepositoryManagedHistory(context.Background(), repositoryID)
	if err != nil || !repositoryHistory {
		t.Fatalf("tombstone-only repository history=%t err=%v, want true/nil", repositoryHistory, err)
	}
	installationHistory, err := runtime.admission.history.HasInstallationManagedHistory(context.Background())
	if err != nil || !installationHistory {
		t.Fatalf("tombstone-only installation history=%t err=%v, want true/nil", installationHistory, err)
	}
}

func TestRuntimeRetentionShutdownKeepsOwnersUpUntilWorkerReturns(t *testing.T) {
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.User{}, &model.BackupRepository{}, &model.RecoveryPoint{},
		&model.TaskRepositoryLink{}, &model.BackupAssetManagedHistoryLatch{},
		&model.BackupRetentionPolicy{}, &model.RecoveryPointHold{},
		&model.RecoveryPointLease{}, &model.RecoveryPointLifecycleAttempt{},
		&model.RecoveryPointLifecycleTombstone{},
	); err != nil {
		t.Fatal(err)
	}
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settings.NewService(db), Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{},
		ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{},
		Now: func() time.Time { return time.Date(2026, 8, 18, 13, 20, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("construct runtime: %v", err)
	}

	contentDown := &atomic.Bool{}
	exportDown := &atomic.Bool{}
	recoveryDown := &atomic.Bool{}
	runtime.contentManager = &runtimeContentShutdownProbe{contentRuntimeManager: runtime.contentManager, down: contentDown}
	runtime.exportManager = &runtimeExportShutdownProbe{exportRuntimeManager: runtime.exportManager, down: exportDown}
	runtime.recoveryManager = &runtimeRecoveryShutdownProbe{recoveryRuntimeManager: runtime.recoveryManager, down: recoveryDown}

	worker, hold := newRuntimeRetentionCleanupHoldWorker(t)
	runtime.retentionWorker = worker

	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		runtime.Run(runCtx)
	}()

	passDone := make(chan error, 1)
	go func() { passDone <- worker.StartupPass(context.Background()) }()
	select {
	case <-hold.started:
	case <-time.After(2 * time.Second):
		cancelRun()
		t.Fatal("retention worker did not enter in-flight owner cleanup")
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- runtime.Shutdown(context.Background()) }()

	ownersDown := func() bool {
		return contentDown.Load() || exportDown.Load() || recoveryDown.Load() ||
			(runtime.processingManager != nil && runtime.processingManager.stopped.Load())
	}
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if ownersDown() {
			close(hold.release)
			t.Fatal("owner runtimes shut down before the in-flight retention cleanup returned")
		}
		select {
		case err := <-shutdownDone:
			t.Fatalf("Shutdown returned while retention cleanup was still held: %v", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(hold.release)
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-passDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("held StartupPass error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StartupPass did not return after retention join")
	}
	if !ownersDown() {
		t.Fatal("owner runtimes were not shut down after retention returned")
	}
	cancelRun()
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Runtime.Run did not return after production-order Shutdown")
	}
}

type runtimeRetentionCleanupHold struct {
	started chan struct{}
	release chan struct{}
}

func (hold *runtimeRetentionCleanupHold) CleanupRecoveryPoint(context.Context, retention.LifecyclePointRequest) error {
	close(hold.started)
	<-hold.release
	return nil
}

type runtimeRetentionAdmissionOK struct{}

func (*runtimeRetentionAdmissionOK) RevokeRecoveryPoint(context.Context, retention.LifecyclePointRequest) error {
	return nil
}

type runtimeRetentionWORMDeletion struct{}

func (runtimeRetentionWORMDeletion) DeleteRecoveryPoint(context.Context, retention.LifecyclePointRequest) (retention.PointDeletionResult, error) {
	return retention.PointDeletionResult{}, retention.ErrPointDeletionWORM
}

type runtimeRetentionNoopAudit struct{}

func (runtimeRetentionNoopAudit) PurgeEligibleDetails(context.Context, int) (int, error) {
	return 0, nil
}

type runtimeRetentionNoopImportRebuild struct{}

func (runtimeRetentionNoopImportRebuild) ReconcileImports(context.Context, int) (int, error) {
	return 0, nil
}
func (runtimeRetentionNoopImportRebuild) ReconcileRebuilds(context.Context, int) (int, error) {
	return 0, nil
}

func newRuntimeRetentionCleanupHoldWorker(t *testing.T) (*retention.Worker, *runtimeRetentionCleanupHold) {
	t.Helper()
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.User{}, &model.BackupRepository{}, &model.RecoveryPoint{},
		&model.TaskRepositoryLink{}, &model.BackupRetentionPolicy{}, &model.RecoveryPointHold{},
		&model.RecoveryPointLease{}, &model.RecoveryPointLifecycleAttempt{},
		&model.RecoveryPointLifecycleTombstone{},
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_recovery_point_leases_active_owner_slot
		ON recovery_point_leases(recovery_point_id, holder_type, owner_id) WHERE status = 'active'`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_recovery_point_lifecycle_attempts_active
		ON recovery_point_lifecycle_attempts(recovery_point_id) WHERE phase <> 'complete'`).Error; err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 8, 18, 13, 20, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	repositoryID := strings.Repeat("c", 32)
	pointID := strings.Repeat("d", 32)
	if err := db.Create(&model.User{ID: 1, Username: "admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "retention-shutdown",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	}).Error; err != nil {
		t.Fatal(err)
	}
	captured := clock.Add(-48 * time.Hour)
	if err := db.Create(&model.RecoveryPoint{
		ID: pointID, RepositoryID: repositoryID, Semantics: string(backupasset.PointNativeSnapshot),
		State: string(backupasset.RecoveryPointCommitted), CapturedAt: &captured, CommittedAt: &captured,
		PointRevision: 4, CapabilityRevision: 3, CapabilitiesJSON: `{}`,
		ImmutabilityLevel:    string(backupasset.ImmutabilityBackendVersioned),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
	}).Error; err != nil {
		t.Fatal(err)
	}
	leases, err := backupasset.NewLeaseService(db, now, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	holds, err := retention.NewHoldService(retention.HoldServiceDependencies{DB: db, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	hold := &runtimeRetentionCleanupHold{started: make(chan struct{}), release: make(chan struct{})}
	coordinator, err := retention.NewCoordinator(retention.CoordinatorDependencies{
		DB: db, Leases: leases, Holds: holds, Now: now,
		LeaseOwnerID: retention.RetentionWorkerLeaseOwnerID,
		Admissions:   &runtimeRetentionAdmissionOK{},
		Cleanup:      hold,
		Deleter:      runtimeRetentionWORMDeletion{},
		RetryDelay:   time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	policies, err := retention.NewPolicyService(retention.PolicyServiceDependencies{DB: db, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policies.Create(context.Background(), retention.CreatePolicyRequest{
		Actor:     backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
		ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		Rules: retention.PolicyRules{Version: retention.PolicyRulesVersion1, Age: &retention.AgeRule{KeepDays: 1}},
	}); err != nil {
		t.Fatalf("create shutdown-order policy: %v", err)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	worker, err := retention.NewWorker(retention.WorkerDependencies{
		Foundation:    backupasset.NewFoundationService(settingsService),
		Coordinator:   coordinator,
		Policies:      policies,
		Holds:         holds,
		Audit:         runtimeRetentionNoopAudit{},
		ImportRebuild: runtimeRetentionNoopImportRebuild{},
		Metrics:       retention.NoopMetrics{},
		Now:           now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker, hold
}

type runtimeContentShutdownProbe struct {
	contentRuntimeManager
	down *atomic.Bool
}

func (probe *runtimeContentShutdownProbe) Shutdown(ctx context.Context) error {
	probe.down.Store(true)
	if probe.contentRuntimeManager == nil {
		return nil
	}
	return probe.contentRuntimeManager.Shutdown(ctx)
}

type runtimeExportShutdownProbe struct {
	exportRuntimeManager
	down *atomic.Bool
}

func (probe *runtimeExportShutdownProbe) Shutdown(ctx context.Context) error {
	probe.down.Store(true)
	if probe.exportRuntimeManager == nil {
		return nil
	}
	return probe.exportRuntimeManager.Shutdown(ctx)
}

type runtimeRecoveryShutdownProbe struct {
	recoveryRuntimeManager
	down *atomic.Bool
}

func (probe *runtimeRecoveryShutdownProbe) Shutdown(ctx context.Context) error {
	probe.down.Store(true)
	if probe.recoveryRuntimeManager == nil {
		return nil
	}
	return probe.recoveryRuntimeManager.Shutdown(ctx)
}
