package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var indexerTestDBSequence atomic.Uint64

func TestIndexerZeroDocumentProjectionActivatesAtomically(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	pointID, catalogID := harness.seedCatalog(t, nil)
	generation, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID, CorrelationID: "zero"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if generation.State != string(SearchGenerationComplete) || !generation.IsActive || generation.CatalogGenerationID != catalogID || generation.WrittenDocumentCount != 0 {
		t.Fatalf("invalid zero-document generation: %+v", generation)
	}
	var documents int64
	if err := harness.db.Model(&model.BackupAssetSearchDocument{}).Where("search_generation_id = ?", generation.ID).Count(&documents).Error; err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if documents != 0 {
		t.Fatalf("zero Catalog produced %d search documents", documents)
	}
}

func TestIndexerManifestlessMutableCatalogProjectionActivates(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	pointID, catalogID := harness.seedMutableCatalog(t, []model.CatalogEntry{
		{EntryID: strings.Repeat("1", 64), NormalizedPath: "alpha.txt", Name: "alpha.txt", EntryType: "file"},
		{EntryID: strings.Repeat("2", 64), NormalizedPath: "beta.txt", Name: "beta.txt", EntryType: "file"},
	})

	generation, buildErr := indexer.Build(context.Background(), BuildRequest{
		RecoveryPointID: pointID,
		CorrelationID:   "mutable-catalog",
	})
	var generationCount int64
	if err := harness.db.Model(&model.BackupAssetSearchGeneration{}).
		Where("recovery_point_id = ?", pointID).Count(&generationCount).Error; err != nil {
		t.Fatalf("count Search generations: %v", err)
	}
	if buildErr != nil {
		if !errors.Is(buildErr, ErrSearchCatalogChanged) {
			t.Fatalf("Build error=%v, want successful mutable Catalog projection", buildErr)
		}
		if generationCount != 0 {
			t.Fatalf("behavioral RED: ErrSearchCatalogChanged with %d Search generations, want zero before the fix", generationCount)
		}
		t.Fatalf("behavioral RED: ErrSearchCatalogChanged with zero Search generations; want active mutable Catalog projection")
	}
	if generationCount != 1 || generation.State != string(SearchGenerationComplete) || !generation.IsActive ||
		generation.CatalogGenerationID != catalogID || generation.ExpectedDocumentCount != 2 || generation.WrittenDocumentCount != 2 {
		t.Fatalf("mutable Catalog Search generation count=%d row=%+v", generationCount, generation)
	}
	var documents int64
	if err := harness.db.Model(&model.BackupAssetSearchDocument{}).
		Where("search_generation_id = ?", generation.ID).Count(&documents).Error; err != nil {
		t.Fatalf("count mutable Catalog Search documents: %v", err)
	}
	if documents != generation.ExpectedDocumentCount || documents != generation.WrittenDocumentCount {
		t.Fatalf("mutable Catalog Search documents=%d expected=%d written=%d", documents, generation.ExpectedDocumentCount, generation.WrittenDocumentCount)
	}
}

func TestIndexerCatalogReadinessRemainsFailClosed(t *testing.T) {
	testCases := []struct {
		name string
		seed func(*testing.T, *indexerTestHarness) string
		want error
	}{
		{
			name: "manifest-backed mutable mismatch",
			seed: func(t *testing.T, harness *indexerTestHarness) string {
				pointID, catalogID := harness.seedMutableCatalog(t, twoIndexerCatalogEntries())
				manifestID := strings.Repeat("a", 32)
				if err := harness.db.Model(&model.CatalogGeneration{}).Where("id = ?", catalogID).Updates(map[string]any{
					"manifest_id": &manifestID, "expected_entry_count": int64(1),
				}).Error; err != nil {
					t.Fatalf("make mutable Catalog manifest-backed: %v", err)
				}
				return pointID
			},
			want: ErrSearchCatalogChanged,
		},
		{
			name: "manifest-less immutable mismatch",
			seed: func(t *testing.T, harness *indexerTestHarness) string {
				pointID, catalogID := harness.seedCatalog(t, twoIndexerCatalogEntries())
				if err := harness.db.Model(&model.CatalogGeneration{}).Where("id = ?", catalogID).
					Update("expected_entry_count", int64(1)).Error; err != nil {
					t.Fatalf("mismatch immutable Catalog count: %v", err)
				}
				return pointID
			},
			want: ErrSearchCatalogChanged,
		},
		{
			name: "mutable source drift",
			seed: func(t *testing.T, harness *indexerTestHarness) string {
				pointID, _ := harness.seedMutableCatalog(t, twoIndexerCatalogEntries())
				if err := harness.db.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).
					Update("source_fingerprint", "drifted-source").Error; err != nil {
					t.Fatalf("drift mutable source: %v", err)
				}
				return pointID
			},
			want: ErrSearchCatalogChanged,
		},
		{
			name: "mutable point ineligible",
			seed: func(t *testing.T, harness *indexerTestHarness) string {
				pointID, _ := harness.seedMutableCatalog(t, twoIndexerCatalogEntries())
				if err := harness.db.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).
					Update("state", backupasset.RecoveryPointRetired).Error; err != nil {
					t.Fatalf("make mutable point ineligible: %v", err)
				}
				return pointID
			},
			want: ErrSearchSourceChanged,
		},
		{
			name: "mutable negative written count",
			seed: func(t *testing.T, harness *indexerTestHarness) string {
				pointID, catalogID := harness.seedMutableCatalog(t, twoIndexerCatalogEntries())
				if err := harness.db.Model(&model.CatalogGeneration{}).Where("id = ?", catalogID).
					Update("written_entry_count", int64(-1)).Error; err != nil {
					t.Fatalf("make mutable Catalog count negative: %v", err)
				}
				return pointID
			},
			want: ErrSearchCatalogChanged,
		},
		{
			name: "manifest-less mutable unexpected expected count",
			seed: func(t *testing.T, harness *indexerTestHarness) string {
				pointID, catalogID := harness.seedMutableCatalog(t, twoIndexerCatalogEntries())
				if err := harness.db.Model(&model.CatalogGeneration{}).Where("id = ?", catalogID).
					Update("expected_entry_count", int64(1)).Error; err != nil {
					t.Fatalf("set unexpected manifest-less expected count: %v", err)
				}
				return pointID
			},
			want: ErrSearchCatalogChanged,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			indexer, harness := newIndexerTestHarness(t)
			pointID := testCase.seed(t, harness)
			if _, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID}); !errors.Is(err, testCase.want) {
				t.Fatalf("Build error=%v, want %v", err, testCase.want)
			}
			var generations int64
			if err := harness.db.Model(&model.BackupAssetSearchGeneration{}).
				Where("recovery_point_id = ?", pointID).Count(&generations).Error; err != nil {
				t.Fatalf("count rejected Search generations: %v", err)
			}
			if generations != 0 {
				t.Fatalf("rejected Search generation count=%d, want zero", generations)
			}
		})
	}
}

func TestIndexerActivationRevalidatesMutableCatalogAndDocumentCounts(t *testing.T) {
	testCases := []struct {
		name    string
		mutable bool
		mutate  func(*gorm.DB, string, string) error
		want    error
	}{
		{
			name:    "manifest-less expected count drift",
			mutable: true,
			mutate: func(tx *gorm.DB, _ string, catalogID string) error {
				return tx.Model(&model.CatalogGeneration{}).Where("id = ?", catalogID).
					Update("expected_entry_count", int64(1)).Error
			},
			want: ErrSearchCatalogChanged,
		},
		{
			name:    "eligible point state drift",
			mutable: false,
			mutate: func(tx *gorm.DB, pointID, _ string) error {
				return tx.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).
					Update("state", backupasset.RecoveryPointDegraded).Error
			},
			want: ErrSearchSourceChanged,
		},
		{
			name:    "eligible point semantics drift",
			mutable: false,
			mutate: func(tx *gorm.DB, pointID, _ string) error {
				return tx.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).
					Update("semantics", backupasset.PointNativeSnapshot).Error
			},
			want: ErrSearchSourceChanged,
		},
		{
			name:    "Catalog manifest identity drift",
			mutable: false,
			mutate: func(tx *gorm.DB, _ string, catalogID string) error {
				manifestID := strings.Repeat("b", 32)
				return tx.Model(&model.CatalogGeneration{}).Where("id = ?", catalogID).
					Update("manifest_id", &manifestID).Error
			},
			want: ErrSearchCatalogChanged,
		},
		{
			name:    "physical document count drift",
			mutable: false,
			mutate: func(tx *gorm.DB, pointID, _ string) error {
				return tx.Where("recovery_point_id = ?", pointID).
					Limit(1).Delete(&model.BackupAssetSearchDocument{}).Error
			},
			want: ErrSearchCatalogChanged,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, harness := newIndexerTestHarness(t)
			var pointID, catalogID string
			if testCase.mutable {
				pointID, catalogID = harness.seedMutableCatalog(t, twoIndexerCatalogEntries())
			} else {
				pointID, catalogID = harness.seedCatalog(t, twoIndexerCatalogEntries())
			}
			var validations atomic.Int32
			lease := &indexerLeaseFake{now: harness.now, validate: func(tx *gorm.DB, _ backupasset.LeaseFence) error {
				if validations.Add(1) != 3 {
					return nil
				}
				return testCase.mutate(tx, pointID, catalogID)
			}}
			indexer, err := NewIndexer(IndexerDependencies{
				DB: harness.db, Lease: lease, Keys: harness.ring,
				Now: func() time.Time { return harness.now }, Config: standardIndexerConfig(),
			})
			if err != nil {
				t.Fatalf("NewIndexer: %v", err)
			}
			if _, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID}); !errors.Is(err, testCase.want) {
				t.Fatalf("Build error=%v, want %v", err, testCase.want)
			}
			if validations.Load() != 3 {
				t.Fatalf("lease validations=%d, want activation-time mutation on third validation", validations.Load())
			}
			var active, failed int64
			if err := harness.db.Model(&model.BackupAssetSearchGeneration{}).
				Where("recovery_point_id = ? AND is_active = ?", pointID, true).Count(&active).Error; err != nil {
				t.Fatalf("count active Search generations: %v", err)
			}
			if err := harness.db.Model(&model.BackupAssetSearchGeneration{}).
				Where("recovery_point_id = ? AND state = ?", pointID, SearchGenerationFailed).Count(&failed).Error; err != nil {
				t.Fatalf("count failed Search generations: %v", err)
			}
			if active != 0 || failed != 1 {
				t.Fatalf("activation drift active=%d failed=%d, want zero active and one failed", active, failed)
			}
		})
	}
}

func TestIndexerCancellationWaitsForUnifiedBoundedTeardown(t *testing.T) {
	_, harness := newIndexerTestHarness(t)
	pointID, _ := harness.seedCatalog(t, []model.CatalogEntry{{
		EntryID: strings.Repeat("c", 64), NormalizedPath: "report.txt", Name: "report.txt", EntryType: "file",
	}})

	projectionStarted := make(chan struct{})
	projectionCanceled := make(chan struct{})
	allowProjectionJoin := make(chan struct{})
	projectionJoined := make(chan struct{})
	failureWriteStarted := make(chan struct{})
	failurePersisted := make(chan struct{})
	finalReleaseStarted := make(chan struct{})
	allowFinalRelease := make(chan struct{})
	var projectionOnce sync.Once
	var failureStartOnce sync.Once
	var failurePersistOnce sync.Once
	var projectionGateOnce sync.Once
	var releaseGateOnce sync.Once
	var projectionErrorInjected atomic.Bool
	releaseProjection := func() { projectionGateOnce.Do(func() { close(allowProjectionJoin) }) }
	releaseFinalLease := func() { releaseGateOnce.Do(func() { close(allowFinalRelease) }) }
	t.Cleanup(releaseProjection)
	t.Cleanup(releaseFinalLease)

	const projectionCallback = "search:test_unified_teardown_projection"
	if err := harness.db.Callback().Query().Before("gorm:query").Register(projectionCallback, func(tx *gorm.DB) {
		if gormCallbackTable(tx) != (model.CatalogEntry{}).TableName() {
			return
		}
		projectionOnce.Do(func() {
			close(projectionStarted)
			<-tx.Statement.Context.Done()
			close(projectionCanceled)
			<-allowProjectionJoin
			close(projectionJoined)
			if err := tx.AddError(tx.Statement.Context.Err()); errors.Is(err, context.Canceled) {
				projectionErrorInjected.Store(true)
			}
		})
	}); err != nil {
		t.Fatalf("register projection barrier: %v", err)
	}
	t.Cleanup(func() {
		if err := harness.db.Callback().Query().Remove(projectionCallback); err != nil {
			t.Errorf("remove projection barrier: %v", err)
		}
	})

	var failureContextBounded atomic.Bool
	var failureContextUsable atomic.Bool
	const failureBeforeCallback = "search:test_unified_teardown_failure_before"
	if err := harness.db.Callback().Update().Before("gorm:update").Register(failureBeforeCallback, func(tx *gorm.DB) {
		if gormCallbackTable(tx) != (model.BackupAssetSearchGeneration{}).TableName() || !testSignalClosed(projectionJoined) {
			return
		}
		failureStartOnce.Do(func() {
			_, bounded := tx.Statement.Context.Deadline()
			failureContextBounded.Store(bounded)
			failureContextUsable.Store(tx.Statement.Context.Err() == nil)
			close(failureWriteStarted)
		})
	}); err != nil {
		t.Fatalf("register failure deadline observer: %v", err)
	}
	t.Cleanup(func() {
		if err := harness.db.Callback().Update().Remove(failureBeforeCallback); err != nil {
			t.Errorf("remove failure deadline observer: %v", err)
		}
	})
	const failureAfterCallback = "search:test_unified_teardown_failure_after"
	if err := harness.db.Callback().Update().After("gorm:update").Register(failureAfterCallback, func(tx *gorm.DB) {
		if gormCallbackTable(tx) == (model.BackupAssetSearchGeneration{}).TableName() && testSignalClosed(failureWriteStarted) {
			failurePersistOnce.Do(func() { close(failurePersisted) })
		}
	}); err != nil {
		t.Fatalf("register failure persistence observer: %v", err)
	}
	t.Cleanup(func() {
		if err := harness.db.Callback().Update().Remove(failureAfterCallback); err != nil {
			t.Errorf("remove failure persistence observer: %v", err)
		}
	})

	lease := &searchTeardownLeaseProbe{
		now: harness.now, projectionJoined: projectionJoined, failurePersisted: failurePersisted,
		finalReleaseStarted: finalReleaseStarted, allowFinalRelease: allowFinalRelease,
	}
	indexer, err := NewIndexer(IndexerDependencies{
		DB: harness.db, Lease: lease, Keys: harness.ring,
		Now: func() time.Time { return harness.now }, Config: standardIndexerConfig(),
	})
	if err != nil {
		t.Fatalf("NewIndexer: %v", err)
	}

	type buildOutcome struct {
		err error
	}
	buildDone := make(chan buildOutcome, 1)
	go func() {
		_, buildErr := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID})
		buildDone <- buildOutcome{err: buildErr}
	}()
	waitForIndexerTestSignal(t, projectionStarted, "projection start")

	indexer.attemptsMu.Lock()
	attempt, registered := indexer.attempts[pointID]
	indexer.attemptsMu.Unlock()
	if !registered {
		releaseProjection()
		releaseFinalLease()
		t.Fatal("Search build was not registered before projection")
	}

	ownerCtx, cancelOwner := context.WithTimeout(context.Background(), 80*time.Millisecond)
	ownerDone := make(chan error, 1)
	go func() { ownerDone <- indexer.cancelAndJoinActiveBuild(ownerCtx, pointID) }()
	waitForIndexerTestSignal(t, projectionCanceled, "projection cancellation")
	var ownerErr error
	select {
	case ownerErr = <-ownerDone:
	case <-time.After(time.Second):
		releaseProjection()
		releaseFinalLease()
		t.Fatal("Search cancellation owner exceeded its deadline")
	}
	cancelOwner()

	releaseProjection()
	waitForIndexerTestSignal(t, projectionJoined, "projection join")
	waitForIndexerTestSignal(t, failureWriteStarted, "failure evidence start")
	waitForIndexerTestSignal(t, failurePersisted, "durable failure evidence")
	waitForIndexerTestSignal(t, finalReleaseStarted, "final lease release")

	var failedGeneration model.BackupAssetSearchGeneration
	if err := harness.db.Where("recovery_point_id = ?", pointID).Take(&failedGeneration).Error; err != nil {
		releaseFinalLease()
		t.Fatalf("load failed Search generation: %v", err)
	}
	failureDurable := failedGeneration.State == string(SearchGenerationFailed) && failedGeneration.ErrorCode != ""
	doneClosedBeforeRelease := testSignalClosed(attempt.done)
	registeredDuringRelease := indexer.activeBuildExists(pointID)

	secondOwnerCtx, cancelSecondOwner := context.WithTimeout(context.Background(), time.Second)
	defer cancelSecondOwner()
	secondOwnerDone := make(chan error, 1)
	go func() { secondOwnerDone <- indexer.cancelAndJoinActiveBuild(secondOwnerCtx, pointID) }()
	secondOwnerReturnedEarly := false
	secondOwnerCompleted := false
	select {
	case <-secondOwnerDone:
		secondOwnerReturnedEarly = true
		secondOwnerCompleted = true
	case <-time.After(40 * time.Millisecond):
	}

	releaseObservations := lease.snapshotReleases()
	releaseFinalLease()
	var buildErr error
	select {
	case outcome := <-buildDone:
		buildErr = outcome.err
	case <-time.After(time.Second):
		t.Fatal("Search build did not finish after final release")
	}
	if !secondOwnerCompleted {
		select {
		case <-secondOwnerDone:
		case <-time.After(time.Second):
			t.Fatal("second cancellation owner did not join completed teardown")
		}
	}

	if !errors.Is(ownerErr, context.DeadlineExceeded) {
		t.Errorf("cancellation owner error=%v, want bounded deadline", ownerErr)
	}
	if !errors.Is(buildErr, context.Canceled) {
		t.Errorf("Build error=%v, want cancellation", buildErr)
	}
	if !projectionErrorInjected.Load() {
		t.Error("projection barrier did not inject the expected cancellation error")
	}
	if !failureContextBounded.Load() || !failureContextUsable.Load() {
		t.Errorf("failure evidence context bounded=%t usable=%t, want finite detached context", failureContextBounded.Load(), failureContextUsable.Load())
	}
	if !failureDurable {
		t.Errorf("failure evidence state=%q code_present=%t, want durable terminal failure", failedGeneration.State, failedGeneration.ErrorCode != "")
	}
	if len(releaseObservations) != 1 {
		t.Errorf("lease release calls=%d, want exactly one unified release", len(releaseObservations))
	}
	for releaseIndex, observation := range releaseObservations {
		if !observation.exactFence || !observation.afterProjectionJoin || !observation.afterFailureEvidence ||
			!observation.contextBounded || !observation.contextUsable {
			t.Errorf("lease release call=%d exact=%t after_projection_join=%t after_failure=%t bounded=%t usable=%t, want all true",
				releaseIndex+1,
				observation.exactFence, observation.afterProjectionJoin, observation.afterFailureEvidence,
				observation.contextBounded, observation.contextUsable)
		}
	}
	if doneClosedBeforeRelease || !registeredDuringRelease {
		t.Errorf("release barrier done_closed=%t registered=%t, want done open and build registered until release completes",
			doneClosedBeforeRelease, registeredDuringRelease)
	}
	if secondOwnerReturnedEarly {
		t.Error("cancellation owner returned while final lease release was blocked")
	}
	if !testSignalClosed(attempt.done) || indexer.activeBuildExists(pointID) {
		t.Error("Search build did not close done and unregister after unified teardown")
	}
}

func TestLifecycleLateOutputRejectsSearchGenerationCreation(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	pointID, _ := harness.seedCatalog(t, nil)
	attempt := model.RecoveryPointLifecycleAttempt{
		ID: strings.Repeat("e", 32), RecoveryPointID: pointID,
		Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(backupasset.LifecyclePhaseRevoking),
	}
	if err := harness.db.Create(&attempt).Error; err != nil {
		t.Fatalf("seed lifecycle attempt: %v", err)
	}
	if _, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("late Search generation error=%v, want ErrConflict", err)
	}
	var generations int64
	if err := harness.db.Model(&model.BackupAssetSearchGeneration{}).Where("recovery_point_id = ?", pointID).Count(&generations).Error; err != nil || generations != 0 {
		t.Fatalf("late Search generation count=%d err=%v, want zero", generations, err)
	}
}

func TestIndexerBeginGenerationRejectsStaleOrReleasedFenceWithoutDurableGeneration(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		lease func(*indexerTestHarness) SearchLease
	}{
		{
			name: "stale",
			lease: func(harness *indexerTestHarness) SearchLease {
				return &indexerLeaseFake{
					now: harness.now,
					validate: func(tx *gorm.DB, fence backupasset.LeaseFence) error {
						if err := assertNoSearchGenerationBeforeFenceValidation(tx, fence.RecoveryPointID); err != nil {
							return err
						}
						return backupasset.ErrLeaseFenceLost
					},
				}
			},
		},
		{
			name: "released",
			lease: func(harness *indexerTestHarness) SearchLease {
				service, err := backupasset.NewLeaseService(
					harness.db,
					func() time.Time { return harness.now },
					backupasset.LeaseConfig{Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour},
				)
				if err != nil {
					t.Fatalf("new released-fence lease service: %v", err)
				}
				return &releaseBeforeSearchFenceValidation{delegate: service}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, harness := newIndexerTestHarness(t)
			pointID, _ := harness.seedCatalog(t, nil)
			indexer, err := NewIndexer(IndexerDependencies{
				DB: harness.db, Lease: testCase.lease(harness), Keys: harness.ring,
				Now: func() time.Time { return harness.now }, Config: standardIndexerConfig(),
			})
			if err != nil {
				t.Fatalf("NewIndexer: %v", err)
			}
			if _, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID}); !errors.Is(err, backupasset.ErrLeaseFenceLost) {
				t.Fatalf("Build error=%v, want lease fence lost", err)
			}
			var generations int64
			if err := harness.db.Model(&model.BackupAssetSearchGeneration{}).
				Where("recovery_point_id = ?", pointID).Count(&generations).Error; err != nil {
				t.Fatalf("count Search generations: %v", err)
			}
			if generations != 0 {
				t.Fatalf("Search generation count=%d, want zero after %s fence rejection", generations, testCase.name)
			}
		})
	}
}

func assertNoSearchGenerationBeforeFenceValidation(tx *gorm.DB, pointID string) error {
	var generations int64
	if err := tx.Model(&model.BackupAssetSearchGeneration{}).
		Where("recovery_point_id = ?", pointID).Count(&generations).Error; err != nil {
		return fmt.Errorf("count Search generations before fence validation: %w", err)
	}
	if generations != 0 {
		return fmt.Errorf("Search fence validation observed %d durable generations", generations)
	}
	return nil
}

func TestIndexerProjectionStoresOnlyHMACsAndMapsLegacySecurityUnknown(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	pointID, _ := harness.seedCatalog(t, []model.CatalogEntry{{
		EntryID: strings.Repeat("d", 64), NormalizedPath: "Docs/年度报告.TXT", Name: "年度报告.TXT",
		EntryType: "file", SecurityState: "", ModifiedAt: timePointer(harness.now.Add(-time.Hour)),
	}})
	generation, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID, CorrelationID: "projection"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var document model.BackupAssetSearchDocument
	if err := harness.db.Where("search_generation_id = ?", generation.ID).First(&document).Error; err != nil {
		t.Fatalf("load document: %v", err)
	}
	if document.Sensitivity != string(SensitivityUnknown) || document.PathSortKey == "" || document.NameSortKey == "" ||
		document.LineageToken == "" || document.PathGroupToken == "" {
		t.Fatalf("invalid projected document: %+v", document)
	}
	var postings []model.BackupAssetSearchPosting
	if err := harness.db.Where("search_generation_id = ?", generation.ID).Find(&postings).Error; err != nil {
		t.Fatalf("load postings: %v", err)
	}
	if len(postings) == 0 {
		t.Fatal("metadata projection produced no postings")
	}
	for _, posting := range postings {
		if len(posting.TokenHMAC) != 64 || strings.Contains(posting.TokenHMAC, "报告") || strings.Contains(posting.TokenHMAC, "txt") || posting.TermFrequency <= 0 {
			t.Fatalf("posting leaked plaintext or invalid frequency: %+v", posting)
		}
	}
	var fields []model.BackupAssetSearchDocumentField
	if err := harness.db.Where("search_generation_id = ?", generation.ID).Find(&fields).Error; err != nil {
		t.Fatalf("load fields: %v", err)
	}
	states := make(map[string]string, len(fields))
	for _, field := range fields {
		states[field.Field] = field.State
	}
	for _, field := range []string{"name", "path", "extension", "type", "modified_time"} {
		if states[field] != string(FieldCoverageComplete) {
			t.Fatalf("metadata field %s state=%q, want complete", field, states[field])
		}
	}
	for _, field := range []string{"content", "ocr"} {
		if states[field] != string(FieldCoverageUnavailable) {
			t.Fatalf("future field %s state=%q, want unavailable", field, states[field])
		}
	}
}

func TestIndexerInvalidSecurityFailsAndPreservesOldActiveProjection(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	pointID, catalogID := harness.seedCatalog(t, []model.CatalogEntry{{
		EntryID: strings.Repeat("e", 64), NormalizedPath: "bad.txt", Name: "bad.txt",
		EntryType: "file", SecurityState: "future_security",
	}})
	old := harness.seedActiveSearch(t, pointID, catalogID, 1)
	if _, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID, CorrelationID: "bad-security"}); !errors.Is(err, ErrInvalidSecurityState) {
		t.Fatalf("Build got %v, want ErrInvalidSecurityState", err)
	}
	var persisted model.BackupAssetSearchGeneration
	if err := harness.db.First(&persisted, "id = ?", old.ID).Error; err != nil {
		t.Fatalf("load old active: %v", err)
	}
	if !persisted.IsActive || persisted.State != string(SearchGenerationComplete) {
		t.Fatalf("failed build replaced old active projection: %+v", persisted)
	}
	var failed int64
	if err := harness.db.Model(&model.BackupAssetSearchGeneration{}).
		Where("recovery_point_id = ? AND state = ?", pointID, SearchGenerationFailed).Count(&failed).Error; err != nil {
		t.Fatalf("count failed generation: %v", err)
	}
	if failed != 1 {
		t.Fatalf("failed generation count=%d, want 1", failed)
	}
}

func TestIndexerActivationRejectsFenceAndSourceDrift(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		validate func(*gorm.DB, backupasset.LeaseFence) error
		want     error
	}{
		{name: "fence", validate: func(*gorm.DB, backupasset.LeaseFence) error { return backupasset.ErrLeaseFenceLost }, want: backupasset.ErrLeaseFenceLost},
		{name: "source", validate: func(tx *gorm.DB, fence backupasset.LeaseFence) error {
			return tx.Model(&model.RecoveryPoint{}).Where("id = ?", fence.RecoveryPointID).Update("source_fingerprint", "drifted").Error
		}, want: ErrSearchSourceChanged},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, harness := newIndexerTestHarness(t)
			pointID, _ := harness.seedCatalog(t, []model.CatalogEntry{{
				EntryID: strings.Repeat("f", 64), NormalizedPath: "safe.txt", Name: "safe.txt", EntryType: "file",
			}})
			indexer, err := NewIndexer(IndexerDependencies{
				DB: harness.db, Lease: &indexerLeaseFake{now: harness.now, validate: testCase.validate},
				Keys: harness.ring, Now: func() time.Time { return harness.now }, Config: standardIndexerConfig(),
			})
			if err != nil {
				t.Fatalf("NewIndexer: %v", err)
			}
			if _, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID}); !errors.Is(err, testCase.want) {
				t.Fatalf("Build got %v, want %v", err, testCase.want)
			}
			var active int64
			if err := harness.db.Model(&model.BackupAssetSearchGeneration{}).Where("recovery_point_id = ? AND is_active = ?", pointID, true).Count(&active).Error; err != nil {
				t.Fatal(err)
			}
			if active != 0 {
				t.Fatalf("drifted activation published %d active generations", active)
			}
		})
	}
}

func TestIndexerCandidateAndAbandonedProjectionReconciliation(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	pointID, catalogID := harness.seedCatalog(t, nil)
	candidates, err := indexer.ListCandidates(context.Background(), 10)
	if err != nil || len(candidates) != 1 || candidates[0].RecoveryPointID != pointID || candidates[0].CatalogGenerationID != catalogID {
		t.Fatalf("ListCandidates=%+v err=%v", candidates, err)
	}
	stale := harness.seedBuildingSearch(t, pointID, catalogID, harness.now.Add(-time.Hour))
	count, err := indexer.ReconcileAbandoned(context.Background(), harness.now.Add(-30*time.Minute), 10)
	if err != nil || count != 1 {
		t.Fatalf("ReconcileAbandoned count=%d err=%v", count, err)
	}
	var row model.BackupAssetSearchGeneration
	if err := harness.db.First(&row, "id = ?", stale.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != string(SearchGenerationFailed) || row.ErrorCode != string(SearchErrorBuildAbandoned) {
		t.Fatalf("abandoned generation=%+v", row)
	}
}

type indexerTestHarness struct {
	db   *gorm.DB
	ring *backupasset.Keyring
	now  time.Time
}

func newIndexerTestHarness(t *testing.T) (*Indexer, *indexerTestHarness) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_SEARCH_INDEXER_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	now := time.Date(2026, 7, 18, 7, 0, 0, 0, time.UTC)
	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared&_busy_timeout=5000&_txlock=immediate&_loc=UTC",
		strings.ReplaceAll(t.Name(), "/", "_"), indexerTestDBSequence.Add(1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{NowFunc: func() time.Time { return now }, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open indexer DB: %v", err)
	}
	models := []any{
		&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{}, &model.CatalogGeneration{}, &model.CatalogEntry{},
		&model.WrappedDomainKey{}, &model.RecoveryPointLease{},
		&model.BackupAssetSearchGeneration{}, &model.BackupAssetSearchDocument{},
		&model.BackupAssetSearchPosting{}, &model.BackupAssetSearchDocumentField{},
	}
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatalf("migrate indexer models: %v", err)
	}
	for _, statement := range []string{
		`CREATE UNIQUE INDEX idx_test_catalog_active ON catalog_generations(recovery_point_id) WHERE is_active = 1`,
		`CREATE UNIQUE INDEX idx_test_search_active ON backup_asset_search_generations(recovery_point_id) WHERE is_active = 1`,
		`CREATE UNIQUE INDEX idx_test_key_active ON wrapped_domain_keys(domain) WHERE state = 'active'`,
		`CREATE UNIQUE INDEX idx_test_lease_active ON recovery_point_leases(recovery_point_id, holder_type, owner_id) WHERE status = 'active'`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("create indexer test index: %v", err)
		}
	}
	ring := backupasset.NewKeyring(db, func() time.Time { return now })
	if _, err := ring.Ensure(context.Background(), backupasset.KeyDomainSearchToken); err != nil {
		t.Fatalf("ensure search token: %v", err)
	}
	lease, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour})
	if err != nil {
		t.Fatalf("new lease service: %v", err)
	}
	harness := &indexerTestHarness{db: db, ring: ring, now: now}
	indexer, err := NewIndexer(IndexerDependencies{DB: db, Lease: lease, Keys: ring, Now: func() time.Time { return now }, Config: standardIndexerConfig()})
	if err != nil {
		t.Fatalf("NewIndexer: %v", err)
	}
	return indexer, harness
}

func standardIndexerConfig() IndexerConfig {
	return IndexerConfig{BatchSize: 50, BuildTimeout: time.Minute, MaxDocuments: 1000}
}

func twoIndexerCatalogEntries() []model.CatalogEntry {
	return []model.CatalogEntry{
		{EntryID: strings.Repeat("3", 64), NormalizedPath: "first.txt", Name: "first.txt", EntryType: "file"},
		{EntryID: strings.Repeat("4", 64), NormalizedPath: "second.txt", Name: "second.txt", EntryType: "file"},
	}
}

func (harness *indexerTestHarness) seedCatalog(t *testing.T, entries []model.CatalogEntry) (string, string) {
	t.Helper()
	pointID := fmt.Sprintf("%032x", time.Now().UnixNano()&0xfffffff)
	catalogID := fmt.Sprintf("%032x", (time.Now().UnixNano()+1)&0xfffffff)
	point := model.RecoveryPoint{
		ID: pointID, RepositoryID: strings.Repeat("a", 32), ProducingTaskID: uintPointer(11), ProducingTaskRunID: uintPointer(12),
		LineageJSON: validIndexerLineage(t, harness.now), Semantics: string(backupasset.PointXirangManifest), State: string(backupasset.RecoveryPointCommitted),
		SourceFingerprint: "source-" + pointID, CreatedAt: harness.now, UpdatedAt: harness.now,
	}
	if err := harness.db.Create(&point).Error; err != nil {
		t.Fatalf("seed point: %v", err)
	}
	generation := model.CatalogGeneration{
		ID: catalogID, RecoveryPointID: pointID, Generation: 1, State: "complete", IsActive: true,
		SourceFingerprint: point.SourceFingerprint, ExpectedEntryCount: int64(len(entries)), WrittenEntryCount: int64(len(entries)),
		StartedAt: harness.now, FinishedAt: timePointer(harness.now), CreatedAt: harness.now, UpdatedAt: harness.now,
	}
	if err := harness.db.Create(&generation).Error; err != nil {
		t.Fatalf("seed Catalog generation: %v", err)
	}
	for index := range entries {
		entries[index].GenerationID = catalogID
		entries[index].RecoveryPointID = pointID
		entries[index].CreatedAt = harness.now
		if err := harness.db.Create(&entries[index]).Error; err != nil {
			t.Fatalf("seed Catalog entry: %v", err)
		}
	}
	return pointID, catalogID
}

func (harness *indexerTestHarness) seedMutableCatalog(t *testing.T, entries []model.CatalogEntry) (string, string) {
	t.Helper()
	pointID, catalogID := harness.seedCatalog(t, entries)
	lineage, err := json.Marshal(backupasset.RecoveryPointLineageSummary{ProducingTaskID: uintPointer(11)})
	if err != nil {
		t.Fatalf("encode mutable Catalog lineage: %v", err)
	}
	if err := harness.db.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).Updates(map[string]any{
		"producing_task_run_id": nil,
		"lineage_json":          string(lineage),
		"semantics":             backupasset.PointMutableHead,
		"state":                 backupasset.RecoveryPointObserved,
		"captured_at":           nil,
		"committed_at":          nil,
		"observed_at":           harness.now,
	}).Error; err != nil {
		t.Fatalf("make Catalog point mutable: %v", err)
	}
	if err := harness.db.Model(&model.CatalogGeneration{}).Where("id = ?", catalogID).Updates(map[string]any{
		"manifest_id":          nil,
		"expected_entry_count": 0,
		"written_entry_count":  int64(len(entries)),
	}).Error; err != nil {
		t.Fatalf("make Catalog generation manifest-less: %v", err)
	}
	return pointID, catalogID
}

func (harness *indexerTestHarness) seedActiveSearch(t *testing.T, pointID, catalogID string, generation int) model.BackupAssetSearchGeneration {
	t.Helper()
	key, _ := harness.ring.Active(context.Background(), backupasset.KeyDomainSearchToken)
	row := model.BackupAssetSearchGeneration{
		ID: fmt.Sprintf("%032x", time.Now().UnixNano()&0xfffffff), RecoveryPointID: pointID, CatalogGenerationID: catalogID,
		Generation: generation, State: string(SearchGenerationComplete), IsActive: true,
		NormalizerVersion: NormalizerVersion, SearchKeyVersion: key.Version, ProjectionRevision: 1,
		LeaseID: strings.Repeat("1", 32), BuildAttemptID: strings.Repeat("2", 32), FenceTokenHash: strings.Repeat("3", 64),
		StartedAt: harness.now, FinishedAt: timePointer(harness.now), CreatedAt: harness.now, UpdatedAt: harness.now,
	}
	if err := harness.db.Create(&row).Error; err != nil {
		t.Fatalf("seed active Search: %v", err)
	}
	return row
}

func (harness *indexerTestHarness) seedBuildingSearch(t *testing.T, pointID, catalogID string, updatedAt time.Time) model.BackupAssetSearchGeneration {
	t.Helper()
	key, _ := harness.ring.Active(context.Background(), backupasset.KeyDomainSearchToken)
	row := model.BackupAssetSearchGeneration{
		ID: fmt.Sprintf("%032x", (time.Now().UnixNano()+2)&0xfffffff), RecoveryPointID: pointID, CatalogGenerationID: catalogID,
		Generation: 2, State: string(SearchGenerationBuilding), NormalizerVersion: NormalizerVersion,
		SearchKeyVersion: key.Version, ProjectionRevision: 1, LeaseID: strings.Repeat("4", 32),
		BuildAttemptID: strings.Repeat("5", 32), FenceTokenHash: strings.Repeat("6", 64),
		StartedAt: updatedAt, CreatedAt: updatedAt, UpdatedAt: updatedAt,
	}
	if err := harness.db.Create(&row).Error; err != nil {
		t.Fatalf("seed building Search: %v", err)
	}
	return row
}

type indexerLeaseFake struct {
	now      time.Time
	validate func(*gorm.DB, backupasset.LeaseFence) error
}

type searchReleaseObservation struct {
	exactFence           bool
	afterProjectionJoin  bool
	afterFailureEvidence bool
	contextBounded       bool
	contextUsable        bool
}

type searchTeardownLeaseProbe struct {
	now                 time.Time
	projectionJoined    <-chan struct{}
	failurePersisted    <-chan struct{}
	finalReleaseStarted chan struct{}
	allowFinalRelease   <-chan struct{}

	mu               sync.Mutex
	acquiredFence    backupasset.LeaseFence
	releases         []searchReleaseObservation
	finalReleaseOnce sync.Once
}

func (lease *searchTeardownLeaseProbe) Acquire(
	_ context.Context,
	request backupasset.AcquireLeaseRequest,
) (backupasset.Lease, error) {
	fence := backupasset.LeaseFence{
		LeaseID: strings.Repeat("a", 32), RecoveryPointID: request.RecoveryPointID, HolderType: request.HolderType,
		OwnerID: request.OwnerID, AttemptID: strings.Repeat("b", 32), FenceToken: strings.Repeat("c", 64),
	}
	lease.mu.Lock()
	lease.acquiredFence = fence
	lease.mu.Unlock()
	return backupasset.Lease{
		ID: fence.LeaseID, RecoveryPointID: request.RecoveryPointID, HolderType: request.HolderType,
		OwnerID: request.OwnerID, AbsoluteDeadline: lease.now.Add(time.Hour), Fence: fence,
	}, nil
}

func (lease *searchTeardownLeaseProbe) Release(ctx context.Context, fence backupasset.LeaseFence) error {
	_, bounded := ctx.Deadline()
	afterProjectionJoin := testSignalClosed(lease.projectionJoined)
	afterFailureEvidence := testSignalClosed(lease.failurePersisted)
	lease.mu.Lock()
	exactFence := fence == lease.acquiredFence
	lease.releases = append(lease.releases, searchReleaseObservation{
		exactFence: exactFence, afterProjectionJoin: afterProjectionJoin, afterFailureEvidence: afterFailureEvidence,
		contextBounded: bounded, contextUsable: ctx.Err() == nil,
	})
	lease.mu.Unlock()
	if afterProjectionJoin {
		lease.finalReleaseOnce.Do(func() { close(lease.finalReleaseStarted) })
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-lease.allowFinalRelease:
		return ctx.Err()
	}
}

func (*searchTeardownLeaseProbe) ReleaseTx(context.Context, *gorm.DB, backupasset.LeaseFence) error {
	return errors.New("unexpected transactional release during canceled Search build")
}

func (*searchTeardownLeaseProbe) ValidateFenceTx(context.Context, *gorm.DB, backupasset.LeaseFence) error {
	return nil
}

func (lease *searchTeardownLeaseProbe) snapshotReleases() []searchReleaseObservation {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return append([]searchReleaseObservation(nil), lease.releases...)
}

type releaseBeforeSearchFenceValidation struct {
	delegate *backupasset.LeaseService
}

func (lease *releaseBeforeSearchFenceValidation) Acquire(
	ctx context.Context,
	request backupasset.AcquireLeaseRequest,
) (backupasset.Lease, error) {
	acquired, err := lease.delegate.Acquire(ctx, request)
	if err != nil {
		return backupasset.Lease{}, err
	}
	if err := lease.delegate.Release(ctx, acquired.Fence); err != nil {
		return backupasset.Lease{}, err
	}
	return acquired, nil
}

func (lease *releaseBeforeSearchFenceValidation) Release(ctx context.Context, fence backupasset.LeaseFence) error {
	return lease.delegate.Release(ctx, fence)
}

func (lease *releaseBeforeSearchFenceValidation) ReleaseTx(
	ctx context.Context,
	tx *gorm.DB,
	fence backupasset.LeaseFence,
) error {
	return lease.delegate.ReleaseTx(ctx, tx, fence)
}

func (lease *releaseBeforeSearchFenceValidation) ValidateFenceTx(
	ctx context.Context,
	tx *gorm.DB,
	fence backupasset.LeaseFence,
) error {
	if err := assertNoSearchGenerationBeforeFenceValidation(tx, fence.RecoveryPointID); err != nil {
		return err
	}
	return lease.delegate.ValidateFenceTx(ctx, tx, fence)
}

func (lease *indexerLeaseFake) Acquire(_ context.Context, request backupasset.AcquireLeaseRequest) (backupasset.Lease, error) {
	fence := backupasset.LeaseFence{
		LeaseID: strings.Repeat("7", 32), RecoveryPointID: request.RecoveryPointID, HolderType: request.HolderType,
		OwnerID: request.OwnerID, AttemptID: strings.Repeat("8", 32), FenceToken: strings.Repeat("9", 64),
	}
	return backupasset.Lease{ID: fence.LeaseID, RecoveryPointID: request.RecoveryPointID, HolderType: request.HolderType, OwnerID: request.OwnerID, AbsoluteDeadline: lease.now.Add(time.Hour), Fence: fence}, nil
}

func (*indexerLeaseFake) Release(context.Context, backupasset.LeaseFence) error { return nil }
func (*indexerLeaseFake) ReleaseTx(context.Context, *gorm.DB, backupasset.LeaseFence) error {
	return nil
}
func (lease *indexerLeaseFake) ValidateFenceTx(_ context.Context, tx *gorm.DB, fence backupasset.LeaseFence) error {
	return lease.validate(tx, fence)
}

func gormCallbackTable(tx *gorm.DB) string {
	if tx == nil || tx.Statement == nil {
		return ""
	}
	if tx.Statement.Table != "" {
		return tx.Statement.Table
	}
	if tx.Statement.Schema != nil {
		return tx.Statement.Schema.Table
	}
	return ""
}

func testSignalClosed(signal <-chan struct{}) bool {
	select {
	case <-signal:
		return true
	default:
		return false
	}
}

func waitForIndexerTestSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func validIndexerLineage(t *testing.T, now time.Time) string {
	t.Helper()
	value, err := backupasset.EncodePublicationLineage(backupasset.PublicationLineageV1{
		Version: 1, TaskRepositoryLinkID: strings.Repeat("a", 32), TaskID: 11, TaskRunID: 12,
		Trigger: "manual", PublicationMode: string(backupasset.PublicationVersionedFullCopy), PointCodecVersion: 1,
		StartedAt: now.Add(-time.Hour), PreparedAt: now.Add(-59 * time.Minute), PointDeadlineAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("encode lineage: %v", err)
	}
	return value
}

func uintPointer(value uint) *uint           { return &value }
func timePointer(value time.Time) *time.Time { return &value }
