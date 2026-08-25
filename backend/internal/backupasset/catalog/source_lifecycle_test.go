package catalog

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const catalogLifecycleTestTimeout = 3 * time.Second

func TestNewSourceLifecycleCatalogRejectsMismatchedIndexerDatabase(t *testing.T) {
	dbA, err := gorm.Open(sqlite.Open(t.TempDir()+"/catalog-owner-a.db?_busy_timeout=5000&_loc=UTC"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open Catalog owner database: %v", err)
	}
	dbB, err := gorm.Open(sqlite.Open(t.TempDir()+"/catalog-indexer-b.db?_busy_timeout=5000&_loc=UTC"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open Catalog indexer database: %v", err)
	}
	if owner, err := NewSourceLifecycle(dbA, &Indexer{}, time.Now, 1); owner != nil || !errors.Is(err, backupasset.ErrInvalidState) {
		t.Errorf("nil-db Catalog indexer owner=%t err=%v, want nil/invalid state", owner != nil, err)
	}
	invalidDB := &gorm.DB{Config: &gorm.Config{}}
	if owner, err := NewSourceLifecycle(invalidDB, &Indexer{db: invalidDB}, time.Now, 1); owner != nil || !errors.Is(err, backupasset.ErrInvalidState) {
		t.Errorf("unresolvable Catalog database owner=%t err=%v, want nil/invalid state", owner != nil, err)
	}
	for _, sameDatabase := range []struct {
		name string
		db   *gorm.DB
	}{
		{name: "Session", db: dbA.Session(&gorm.Session{})},
		{name: "WithContext", db: dbA.WithContext(context.Background())},
	} {
		t.Run(sameDatabase.name, func(t *testing.T) {
			if sameDatabase.db == dbA {
				t.Fatal("same-database Catalog fixture reused the owner *gorm.DB pointer")
			}
			owner, err := NewSourceLifecycle(dbA, &Indexer{db: sameDatabase.db}, time.Now, 1)
			if err != nil || owner == nil {
				t.Errorf("same-database Catalog clone owner=%t err=%v, want accepted", owner != nil, err)
			}
		})
	}

	pointID := strings.Repeat("1", 32)
	buildCtx, cancelBuild := context.WithCancel(context.Background())
	defer cancelBuild()
	indexer := &Indexer{
		db: dbB,
		attempts: map[string]activeCatalogBuild{
			pointID: {cancel: cancelBuild, done: make(chan struct{})},
		},
	}
	if owner, err := NewSourceLifecycle(dbA, indexer, time.Now, 1); owner != nil || !errors.Is(err, backupasset.ErrInvalidState) {
		t.Errorf("cross-db Catalog owner=%t err=%v, want nil/invalid state", owner != nil, err)
	}
	if buildCtx.Err() != nil {
		t.Error("rejected cross-db Catalog owner canceled the same-point builder")
	}
}

func TestLifecycleLateOutputCatalogRejectsGenerationAcrossAcquireRegisterWindow(t *testing.T) {
	fixture := newCatalogIndexerFixture(t, true, 0)
	lease := &catalogAcquireBarrierLease{
		CatalogLease: fixture.lease,
		acquired:     make(chan struct{}),
		proceed:      make(chan struct{}),
	}
	indexer, err := NewIndexer(IndexerDependencies{
		DB: fixture.db, Factory: fixture.factory(), Lease: lease, IdentityKeys: fixture.keys,
		Now: func() time.Time { return fixture.now },
		Config: IndexerConfig{
			BatchSize: 2, BuildTimeout: 30 * time.Minute, MaxEntries: 100,
		},
	})
	if err != nil {
		t.Fatalf("NewIndexer: %v", err)
	}

	buildDone := make(chan error, 1)
	buildExited := make(chan struct{})
	go func() {
		_, buildErr := indexer.Build(context.Background(), BuildRequest{
			RepositoryID: fixture.point.RepositoryID, RecoveryPointID: fixture.point.ID,
		})
		buildDone <- buildErr
		close(buildExited)
	}()
	var releaseAcquire sync.Once
	allowAcquireReturn := func() { releaseAcquire.Do(func() { close(lease.proceed) }) }
	t.Cleanup(func() {
		allowAcquireReturn()
		select {
		case <-buildExited:
		case <-time.After(catalogLifecycleTestTimeout):
		}
	})

	select {
	case <-lease.acquired:
	case <-time.After(catalogLifecycleTestTimeout):
		t.Fatal("Catalog Build did not acquire its exact point lease")
	}
	attemptID := strings.Repeat("9", 32)
	if err := fixture.db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: attemptID, RecoveryPointID: fixture.point.ID,
		Operation: string(backupasset.LifecycleMutableRetire), Phase: string(backupasset.LifecyclePhaseRevoking),
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}).Error; err != nil {
		t.Fatalf("seed lifecycle attempt in acquire/register window: %v", err)
	}
	allowAcquireReturn()

	select {
	case buildErr := <-buildDone:
		if !errors.Is(buildErr, backupasset.ErrConflict) {
			t.Errorf("late Catalog generation Build error=%v, want lifecycle conflict", buildErr)
		}
	case <-time.After(catalogLifecycleTestTimeout):
		t.Fatal("late Catalog generation Build did not return")
	}
	var generationCount int64
	if err := fixture.db.Model(&model.CatalogGeneration{}).
		Where("recovery_point_id = ?", fixture.point.ID).Count(&generationCount).Error; err != nil {
		t.Fatalf("count late Catalog generations: %v", err)
	}
	if generationCount != 0 {
		t.Errorf("late Catalog generation count=%d, want 0", generationCount)
	}
}

func TestRecoveryPointSourceLifecycleCatalogWaitsForUnifiedBuilderTeardown(t *testing.T) {
	fixture := newCatalogIndexerFixture(t, true, 0)
	session := &catalogTeardownSession{
		source:  fixture.point.SourceFingerprint,
		entered: make(chan struct{}), canceled: make(chan struct{}), providerJoined: make(chan struct{}),
		releaseProvider: make(chan struct{}), closed: make(chan struct{}),
	}
	lease := &catalogUnifiedTeardownLease{
		CatalogLease: fixture.lease, providerEntered: session.entered, providerJoined: session.providerJoined,
		renewed: make(chan struct{}), prematureRelease: make(chan struct{}),
		finalReleaseEntered: make(chan struct{}), allowFinalRelease: make(chan struct{}),
	}
	indexer, err := NewIndexer(IndexerDependencies{
		DB: fixture.db, Factory: catalogBlockingFactory{session: session}, Lease: lease, IdentityKeys: fixture.keys,
		Now: func() time.Time { return fixture.now },
		Config: IndexerConfig{
			BatchSize: 2, BuildTimeout: 30 * time.Minute, MaxEntries: 100, HeartbeatInterval: 5 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("NewIndexer: %v", err)
	}

	buildCtx, cancelBuild := context.WithCancel(context.Background())
	buildDone := make(chan error, 1)
	buildExited := make(chan struct{})
	go func() {
		_, buildErr := indexer.Build(buildCtx, BuildRequest{
			RepositoryID: fixture.point.RepositoryID, RecoveryPointID: fixture.point.ID,
		})
		buildDone <- buildErr
		close(buildExited)
	}()
	var releaseProviderOnce, releaseLeaseOnce sync.Once
	releaseProvider := func() { releaseProviderOnce.Do(func() { close(session.releaseProvider) }) }
	releaseLease := func() { releaseLeaseOnce.Do(func() { close(lease.allowFinalRelease) }) }
	t.Cleanup(func() {
		cancelBuild()
		releaseProvider()
		releaseLease()
		select {
		case <-buildExited:
		case <-time.After(catalogLifecycleTestTimeout):
			t.Error("Catalog Build did not exit during unified-teardown cleanup")
		}
	})

	select {
	case <-session.entered:
	case <-time.After(catalogLifecycleTestTimeout):
		t.Fatal("Catalog Provider session did not enter enumeration")
	}
	indexer.attemptsMu.Lock()
	registeredAttempt, registered := indexer.attempts[fixture.point.ID]
	indexer.attemptsMu.Unlock()
	if !registered {
		t.Fatal("Catalog builder was not registered before renewal")
	}
	builderDone := registeredAttempt.done
	select {
	case <-lease.renewed:
	case <-time.After(catalogLifecycleTestTimeout):
		t.Fatal("Catalog lease renewal did not run")
	}
	select {
	case <-session.canceled:
	case <-time.After(catalogLifecycleTestTimeout):
		t.Fatal("Catalog renewal loss did not cancel the Provider session")
	}

	attemptID := strings.Repeat("9", 32)
	if err := fixture.db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: attemptID, RecoveryPointID: fixture.point.ID,
		Operation: string(backupasset.LifecycleMutableRetire), Phase: string(backupasset.LifecyclePhaseRevoking),
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}).Error; err != nil {
		t.Fatalf("seed teardown lifecycle attempt: %v", err)
	}
	releaseProvider()
	select {
	case <-session.providerJoined:
	case <-time.After(catalogLifecycleTestTimeout):
		t.Fatal("Catalog Provider session did not join after cancellation")
	}
	select {
	case <-lease.finalReleaseEntered:
	case <-time.After(catalogLifecycleTestTimeout):
		t.Fatal("Catalog builder did not enter final lease release")
	}
	select {
	case <-session.closed:
	default:
		t.Error("Catalog exact lease release began before Provider Close completed")
	}
	select {
	case <-builderDone:
		t.Error("Catalog builder completion closed before exact lease release completed")
	default:
	}

	var generation model.CatalogGeneration
	if err := fixture.db.Where("recovery_point_id = ?", fixture.point.ID).First(&generation).Error; err != nil {
		t.Fatalf("load renewal-failed Catalog generation: %v", err)
	}
	if generation.State != string(GenerationFailed) || generation.FinishedAt == nil || generation.ErrorCode == "" || generation.ErrorCode == "source_lifecycle" {
		t.Errorf("renewal-failed Catalog generation state=%q finished=%t error_code=%q, want durable builder failure", generation.State, generation.FinishedAt != nil, generation.ErrorCode)
	}
	indexer.attemptsMu.Lock()
	_, activeWhileReleasing := indexer.attempts[fixture.point.ID]
	indexer.attemptsMu.Unlock()
	if !activeWhileReleasing {
		t.Error("Catalog builder left the active registry before exact lease release completed")
	}

	owner, err := NewSourceLifecycle(fixture.db, indexer, func() time.Time { return fixture.now }, 16)
	if err != nil {
		t.Fatalf("NewSourceLifecycle: %v", err)
	}
	ownerCtx, cancelOwner := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelOwner()
	ownerDone := make(chan error, 1)
	go func() {
		ownerDone <- owner.RetireRecoveryPoint(ownerCtx, backupasset.SourceLifecycleRequest{
			RecoveryPointID: fixture.point.ID, LifecycleAttemptID: attemptID,
			Operation: backupasset.LifecycleMutableRetire, Stage: backupasset.SourceLifecyclePrepare,
		})
	}()
	var ownerErr error
	ownerReturnedWhileReleasing := false
	select {
	case ownerErr = <-ownerDone:
		ownerReturnedWhileReleasing = true
	case <-time.After(100 * time.Millisecond):
	}
	if ownerReturnedWhileReleasing {
		t.Errorf("Catalog source owner returned before exact builder lease release completed: %v", ownerErr)
	}
	var durableLease model.RecoveryPointLease
	if err := fixture.db.Where("recovery_point_id = ? AND holder_type = ? AND owner_id = ?",
		fixture.point.ID, backupasset.LeaseHolderCatalogBuild, catalogBuildOwnerPrefix+fixture.point.ID).
		First(&durableLease).Error; err != nil {
		t.Fatalf("load Catalog builder lease during final release: %v", err)
	}
	if durableLease.Status != string(backupasset.LeaseActive) || durableLease.ReleasedAt != nil {
		t.Errorf("Catalog builder lease status=%q released=%t before exact Release completed, want active", durableLease.Status, durableLease.ReleasedAt != nil)
	}

	releaseLease()
	var buildErr error
	select {
	case buildErr = <-buildDone:
	case <-time.After(catalogLifecycleTestTimeout):
		t.Fatal("Catalog Build did not finish unified teardown")
	}
	if !errors.Is(buildErr, backupasset.ErrLeaseFenceLost) {
		t.Errorf("renewal-failed Catalog Build error=%v, want lease fence lost", buildErr)
	}
	if !ownerReturnedWhileReleasing {
		select {
		case ownerErr = <-ownerDone:
		case <-time.After(catalogLifecycleTestTimeout):
			t.Fatal("Catalog source owner did not return after builder teardown")
		}
	}
	if ownerErr != nil {
		t.Errorf("Catalog source owner after unified teardown: %v", ownerErr)
	}
	if calls, exact, bounded := lease.releaseSnapshot(); calls != 1 || !exact || !bounded {
		t.Errorf("Catalog exact lease release calls=%d exact=%t bounded=%t, want 1/true/true", calls, exact, bounded)
	}
	select {
	case <-lease.prematureRelease:
		t.Error("Catalog lease release started before Provider join and durable failure evidence")
	default:
	}
	indexer.attemptsMu.Lock()
	_, activeAfterTeardown := indexer.attempts[fixture.point.ID]
	indexer.attemptsMu.Unlock()
	if activeAfterTeardown {
		t.Error("Catalog builder remained registered after unified teardown completed")
	}
	select {
	case <-builderDone:
	default:
		t.Error("Catalog builder completion did not close after unified teardown")
	}
}

func TestRecoveryPointSourceLifecycleCatalogJoinsCanceledBuilderBeforeDurableProof(t *testing.T) {
	fixture := newCatalogIndexerFixture(t, true, 0)
	session := &catalogCancellationIgnoringSession{
		source: fixture.point.SourceFingerprint, entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}),
	}
	indexer := fixture.newIndexer(t, catalogBlockingFactory{session: session})
	buildDone := make(chan struct{})
	var buildErr error
	go func() {
		_, buildErr = indexer.Build(context.Background(), BuildRequest{
			RepositoryID: fixture.point.RepositoryID, RecoveryPointID: fixture.point.ID,
		})
		close(buildDone)
	}()
	var releaseProvider sync.Once
	release := func() { releaseProvider.Do(func() { close(session.release) }) }
	t.Cleanup(func() {
		release()
		select {
		case <-buildDone:
		case <-time.After(time.Second):
		}
	})

	select {
	case <-session.entered:
	case <-time.After(time.Second):
		t.Fatal("Catalog session did not enter enumeration")
	}
	indexer.attemptsMu.Lock()
	_, active := indexer.attempts[fixture.point.ID]
	indexer.attemptsMu.Unlock()
	if !active {
		t.Fatal("Catalog builder was not installed in the active registry")
	}

	var generation model.CatalogGeneration
	if err := fixture.db.Where("recovery_point_id = ? AND state = ?", fixture.point.ID, GenerationBuilding).First(&generation).Error; err != nil {
		t.Fatalf("load building Catalog generation: %v", err)
	}
	var lease model.RecoveryPointLease
	if err := fixture.db.Where("recovery_point_id = ? AND holder_type = ? AND owner_id = ? AND status = ?",
		fixture.point.ID, backupasset.LeaseHolderCatalogBuild, catalogBuildOwnerPrefix+fixture.point.ID, backupasset.LeaseActive).
		First(&lease).Error; err != nil {
		t.Fatalf("load active Catalog lease: %v", err)
	}

	var otherRepository model.BackupRepository
	if err := fixture.db.First(&otherRepository, "id = ?", fixture.point.RepositoryID).Error; err != nil {
		t.Fatalf("load repository for unrelated point: %v", err)
	}
	otherRepository.ID = strings.Repeat("7", 32)
	otherRepository.DisplayName = "unrelated-catalog-fixture"
	if err := fixture.db.Create(&otherRepository).Error; err != nil {
		t.Fatalf("seed unrelated repository: %v", err)
	}
	otherPoint := fixture.point
	otherPoint.ID = strings.Repeat("d", 32)
	otherPoint.RepositoryID = otherRepository.ID
	otherPoint.SourceFingerprint = strings.Repeat("c", 64)
	if err := fixture.db.Create(&otherPoint).Error; err != nil {
		t.Fatalf("seed unrelated point: %v", err)
	}
	otherGeneration := model.CatalogGeneration{
		ID: strings.Repeat("e", 32), RecoveryPointID: otherPoint.ID, Generation: 1,
		State: string(GenerationBuilding), SourceFingerprint: otherPoint.SourceFingerprint,
		StartedAt: fixture.now, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&otherGeneration).Error; err != nil {
		t.Fatalf("seed unrelated Catalog generation: %v", err)
	}
	otherLease := model.RecoveryPointLease{
		ID: strings.Repeat("f", 32), RecoveryPointID: otherPoint.ID,
		HolderType: string(backupasset.LeaseHolderCatalogBuild), OwnerID: catalogBuildOwnerPrefix + otherPoint.ID,
		AttemptID: strings.Repeat("b", 32), FenceToken: strings.Repeat("a", 64), Status: string(backupasset.LeaseActive),
		LeaseExpiresAt: fixture.now.Add(time.Hour), AbsoluteDeadline: fixture.now.Add(2 * time.Hour), LastHeartbeatAt: fixture.now,
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&otherLease).Error; err != nil {
		t.Fatalf("seed unrelated Catalog lease: %v", err)
	}

	attemptID := strings.Repeat("9", 32)
	if err := fixture.db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: attemptID, RecoveryPointID: fixture.point.ID, Operation: string(backupasset.LifecycleMutableRetire),
		Phase: string(backupasset.LifecyclePhaseRevoking), CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}).Error; err != nil {
		t.Fatalf("seed lifecycle attempt: %v", err)
	}
	owner, err := NewSourceLifecycle(fixture.db, indexer, func() time.Time { return fixture.now }, 16)
	if err != nil {
		t.Fatalf("NewSourceLifecycle: %v", err)
	}
	request := backupasset.SourceLifecycleRequest{
		RecoveryPointID: fixture.point.ID, LifecycleAttemptID: attemptID,
		Operation: backupasset.LifecycleMutableRetire, Stage: backupasset.SourceLifecyclePrepare,
	}

	timeoutCtx, cancelTimeout := context.WithTimeout(context.Background(), 100*time.Millisecond)
	timeoutDone := make(chan error, 1)
	go func() { timeoutDone <- owner.RetireRecoveryPoint(timeoutCtx, request) }()
	select {
	case <-session.canceled:
	case <-time.After(time.Second):
		cancelTimeout()
		t.Fatal("Catalog source lifecycle did not cancel the active builder")
	}
	var timeoutErr error
	select {
	case timeoutErr = <-timeoutDone:
	case <-time.After(time.Second):
		cancelTimeout()
		t.Fatal("Catalog source lifecycle did not honor its context timeout")
	}
	cancelTimeout()
	if !errors.Is(timeoutErr, context.DeadlineExceeded) {
		t.Errorf("timed-out Catalog prepare error=%v, want context deadline exceeded", timeoutErr)
	}
	assertCatalogBuilderDurableState(t, fixture.db, generation.ID, lease.ID, GenerationBuilding, backupasset.LeaseActive)
	assertCatalogBuilderDurableState(t, fixture.db, otherGeneration.ID, otherLease.ID, GenerationBuilding, backupasset.LeaseActive)

	prepareCtx, cancelPrepare := context.WithTimeout(context.Background(), time.Second)
	defer cancelPrepare()
	prepareDone := make(chan error, 1)
	go func() { prepareDone <- owner.RetireRecoveryPoint(prepareCtx, request) }()
	returnedBeforeRelease := false
	var prepareErr error
	select {
	case prepareErr = <-prepareDone:
		returnedBeforeRelease = true
	case <-time.After(50 * time.Millisecond):
	}
	assertCatalogBuilderDurableState(t, fixture.db, generation.ID, lease.ID, GenerationBuilding, backupasset.LeaseActive)
	assertCatalogBuilderDurableState(t, fixture.db, otherGeneration.ID, otherLease.ID, GenerationBuilding, backupasset.LeaseActive)

	release()
	select {
	case <-buildDone:
	case <-time.After(time.Second):
		t.Fatal("Catalog Build did not join after provider release")
	}
	if !errors.Is(buildErr, context.Canceled) && !errors.Is(buildErr, backupasset.ErrLeaseFenceLost) {
		t.Fatalf("canceled Catalog Build error=%v", buildErr)
	}
	if !returnedBeforeRelease {
		select {
		case prepareErr = <-prepareDone:
		case <-time.After(time.Second):
			t.Fatal("Catalog prepare did not converge after builder join")
		}
	}
	if returnedBeforeRelease {
		t.Errorf("Catalog prepare returned before the canceled builder joined: %v", prepareErr)
	} else if prepareErr != nil {
		t.Errorf("Catalog prepare after builder join: %v", prepareErr)
	}
	assertCatalogBuilderDurableState(t, fixture.db, generation.ID, lease.ID, GenerationFailed, backupasset.LeaseReleased)
	assertCatalogBuilderDurableState(t, fixture.db, otherGeneration.ID, otherLease.ID, GenerationBuilding, backupasset.LeaseActive)
}

func assertCatalogBuilderDurableState(
	t *testing.T,
	db *gorm.DB,
	generationID string,
	leaseID string,
	wantGeneration GenerationState,
	wantLease backupasset.LeaseStatus,
) {
	t.Helper()
	var generation model.CatalogGeneration
	if err := db.First(&generation, "id = ?", generationID).Error; err != nil {
		t.Fatalf("load Catalog generation %s: %v", generationID, err)
	}
	if generation.State != string(wantGeneration) {
		t.Errorf("Catalog generation %s state=%s, want %s", generationID, generation.State, wantGeneration)
	}
	if wantGeneration == GenerationBuilding && (generation.FinishedAt != nil || generation.ErrorCode != "") {
		t.Errorf("building Catalog generation %s has durable completion: finished_at=%v error_code=%q", generationID, generation.FinishedAt, generation.ErrorCode)
	}
	var lease model.RecoveryPointLease
	if err := db.First(&lease, "id = ?", leaseID).Error; err != nil {
		t.Fatalf("load Catalog lease %s: %v", leaseID, err)
	}
	if lease.Status != string(wantLease) {
		t.Errorf("Catalog lease %s status=%s, want %s", leaseID, lease.Status, wantLease)
	}
	if wantLease == backupasset.LeaseActive && lease.ReleasedAt != nil {
		t.Errorf("active Catalog lease %s has released_at=%v", leaseID, lease.ReleasedAt)
	}
}

func TestRecoveryPointSourceLifecycleCatalogSeparatesPrepareFromCleanup(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/source-lifecycle.db?_busy_timeout=5000&_loc=UTC"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if _, err := NewSourceLifecycle(db, nil, time.Now, 16); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("nil Catalog Indexer error=%v, want invalid state", err)
	}
	if err := db.AutoMigrate(&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLease{}, &model.CatalogGeneration{}, &model.CatalogEntry{}); err != nil {
		t.Fatalf("migrate source lifecycle tables: %v", err)
	}
	now := time.Date(2026, 8, 17, 14, 25, 0, 0, time.UTC)
	pointID, attemptID := strings.Repeat("1", 32), strings.Repeat("2", 32)
	buildingID, completeID, leaseID := strings.Repeat("3", 32), strings.Repeat("4", 32), strings.Repeat("5", 32)
	if err := db.Create(&model.RecoveryPoint{ID: pointID, RepositoryID: strings.Repeat("6", 32)}).Error; err != nil {
		t.Fatalf("seed point: %v", err)
	}
	if err := db.Create(&model.RecoveryPointLifecycleAttempt{ID: attemptID, RecoveryPointID: pointID, Operation: string(backupasset.LifecycleExplicitPurge), Phase: string(backupasset.LifecyclePhaseRevoking)}).Error; err != nil {
		t.Fatalf("seed lifecycle attempt: %v", err)
	}
	generations := []model.CatalogGeneration{
		{ID: buildingID, RecoveryPointID: pointID, Generation: 2, State: string(GenerationBuilding), StartedAt: now},
		{ID: completeID, RecoveryPointID: pointID, Generation: 1, State: string(GenerationComplete), IsActive: true, StartedAt: now, FinishedAt: &now},
	}
	if err := db.Create(&generations).Error; err != nil {
		t.Fatalf("seed Catalog generations: %v", err)
	}
	entry := model.CatalogEntry{GenerationID: completeID, EntryID: strings.Repeat("7", 64), RecoveryPointID: pointID, NormalizedPath: "/kept", Name: "kept", EntryType: "file"}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("seed Catalog entry: %v", err)
	}
	lease := model.RecoveryPointLease{ID: leaseID, RecoveryPointID: pointID, HolderType: string(backupasset.LeaseHolderCatalogBuild), OwnerID: catalogBuildOwnerPrefix + pointID, AttemptID: strings.Repeat("8", 32), FenceToken: strings.Repeat("9", 64), Status: string(backupasset.LeaseActive), LeaseExpiresAt: now.Add(time.Hour), AbsoluteDeadline: now.Add(2 * time.Hour), LastHeartbeatAt: now}
	if err := db.Create(&lease).Error; err != nil {
		t.Fatalf("seed Catalog lease: %v", err)
	}

	owner, err := NewSourceLifecycle(db, &Indexer{db: db, attempts: make(map[string]activeCatalogBuild)}, func() time.Time { return now }, 16)
	if err != nil {
		t.Fatalf("NewSourceLifecycle: %v", err)
	}
	request := backupasset.SourceLifecycleRequest{RecoveryPointID: pointID, LifecycleAttemptID: attemptID, Operation: backupasset.LifecycleExplicitPurge, Stage: backupasset.SourceLifecyclePrepare}
	if err := owner.RetireRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("prepare Catalog lifecycle: %v", err)
	}
	assertCatalogGeneration(t, db, buildingID, GenerationFailed, false)
	assertCatalogGeneration(t, db, completeID, GenerationComplete, true)
	assertCatalogEntryCount(t, db, completeID, 1)
	assertCatalogLeaseReleased(t, db, leaseID)
	var closedBuilding model.CatalogGeneration
	if err := db.First(&closedBuilding, "id = ?", buildingID).Error; err != nil {
		t.Fatalf("load lifecycle-closed Catalog generation: %v", err)
	}
	closedDTO, err := generationDTO(closedBuilding)
	if err != nil {
		t.Errorf("parse lifecycle-closed Catalog generation DTO: %v", err)
	} else if closedDTO.ErrorCode != GenerationErrorBuildAbandoned {
		t.Errorf("lifecycle-closed Catalog error_code=%q, want %q", closedDTO.ErrorCode, GenerationErrorBuildAbandoned)
	}
	var point model.RecoveryPoint
	if err := db.First(&point, "id = ?", pointID).Error; err != nil {
		t.Fatalf("load lifecycle Catalog point: %v", err)
	}
	statusService := &Service{db: db, now: func() time.Time { return now }, reconcileInterval: time.Minute}
	status, err := statusService.projectStatus(context.Background(), point, model.BackupRepository{})
	if err != nil {
		t.Errorf("project lifecycle-closed Catalog status: %v", err)
	} else {
		latestID := ""
		latestErrorCode := GenerationErrorNone
		if status.LatestBuild != nil {
			latestID = status.LatestBuild.ID
			latestErrorCode = status.LatestBuild.ErrorCode
		}
		if latestID != buildingID || latestErrorCode != GenerationErrorBuildAbandoned {
			t.Errorf("lifecycle-closed Catalog latest build id=%v error_code=%v, want %s/%s",
				latestID, latestErrorCode, buildingID, GenerationErrorBuildAbandoned)
		}
	}

	if err := db.Model(&model.RecoveryPointLifecycleAttempt{}).Where("id = ?", attemptID).Update("phase", backupasset.LifecyclePhaseCleaning).Error; err != nil {
		t.Fatalf("advance lifecycle: %v", err)
	}
	request.Stage = backupasset.SourceLifecycleCleanup
	if err := owner.RetireRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("cleanup Catalog lifecycle: %v", err)
	}
	if err := owner.RetireRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("idempotent Catalog cleanup: %v", err)
	}
	assertCatalogGeneration(t, db, completeID, GenerationSuperseded, false)
	assertCatalogEntryCount(t, db, completeID, 1)
}

func assertCatalogGeneration(t *testing.T, db *gorm.DB, id string, state GenerationState, active bool) {
	t.Helper()
	var generation model.CatalogGeneration
	if err := db.First(&generation, "id = ?", id).Error; err != nil || generation.State != string(state) || generation.IsActive != active {
		t.Fatalf("Catalog generation state=%q active=%t err=%v, want state=%s active=%t", generation.State, generation.IsActive, err, state, active)
	}
}

func assertCatalogEntryCount(t *testing.T, db *gorm.DB, generationID string, want int64) {
	t.Helper()
	var count int64
	if err := db.Model(&model.CatalogEntry{}).Where("generation_id = ?", generationID).Count(&count).Error; err != nil || count != want {
		t.Fatalf("Catalog entry count=%d err=%v, want %d", count, err, want)
	}
}

func assertCatalogLeaseReleased(t *testing.T, db *gorm.DB, leaseID string) {
	t.Helper()
	var lease model.RecoveryPointLease
	if err := db.First(&lease, "id = ?", leaseID).Error; err != nil || lease.Status != string(backupasset.LeaseReleased) {
		t.Fatalf("Catalog lease status=%q released=%t err=%v, want released", lease.Status, lease.ReleasedAt != nil, err)
	}
}

type catalogAcquireBarrierLease struct {
	CatalogLease
	acquired chan struct{}
	proceed  chan struct{}
	once     sync.Once
}

func (lease *catalogAcquireBarrierLease) Acquire(ctx context.Context, request backupasset.AcquireLeaseRequest) (backupasset.Lease, error) {
	acquired, err := lease.CatalogLease.Acquire(ctx, request)
	if err != nil {
		return backupasset.Lease{}, err
	}
	lease.once.Do(func() { close(lease.acquired) })
	select {
	case <-lease.proceed:
		return acquired, nil
	case <-ctx.Done():
		_ = lease.Release(context.Background(), acquired.Fence)
		return backupasset.Lease{}, ctx.Err()
	}
}

type catalogUnifiedTeardownLease struct {
	CatalogLease
	providerEntered     <-chan struct{}
	providerJoined      <-chan struct{}
	renewed             chan struct{}
	prematureRelease    chan struct{}
	finalReleaseEntered chan struct{}
	allowFinalRelease   chan struct{}

	mu                  sync.Mutex
	expectedFence       backupasset.LeaseFence
	hasExpectedFence    bool
	releaseCalls        int
	releaseFenceIsExact bool
	finalReleaseBounded bool
	renewOnce           sync.Once
	prematureOnce       sync.Once
	finalOnce           sync.Once
}

func (lease *catalogUnifiedTeardownLease) Acquire(ctx context.Context, request backupasset.AcquireLeaseRequest) (backupasset.Lease, error) {
	acquired, err := lease.CatalogLease.Acquire(ctx, request)
	if err != nil {
		return backupasset.Lease{}, err
	}
	lease.mu.Lock()
	lease.expectedFence = acquired.Fence
	lease.hasExpectedFence = true
	lease.releaseFenceIsExact = true
	lease.mu.Unlock()
	return acquired, nil
}

func (lease *catalogUnifiedTeardownLease) Renew(ctx context.Context, _ backupasset.LeaseFence) (backupasset.Lease, error) {
	select {
	case <-lease.providerEntered:
		lease.renewOnce.Do(func() { close(lease.renewed) })
		return backupasset.Lease{}, backupasset.ErrLeaseFenceLost
	case <-ctx.Done():
		return backupasset.Lease{}, ctx.Err()
	}
}

func (lease *catalogUnifiedTeardownLease) Release(ctx context.Context, fence backupasset.LeaseFence) error {
	lease.mu.Lock()
	lease.releaseCalls++
	if !lease.hasExpectedFence || lease.expectedFence != fence {
		lease.releaseFenceIsExact = false
	}
	lease.mu.Unlock()

	select {
	case <-lease.providerJoined:
		_, bounded := ctx.Deadline()
		lease.mu.Lock()
		lease.finalReleaseBounded = bounded
		lease.mu.Unlock()
		lease.finalOnce.Do(func() { close(lease.finalReleaseEntered) })
		select {
		case <-lease.allowFinalRelease:
			return lease.CatalogLease.Release(ctx, fence)
		case <-ctx.Done():
			return ctx.Err()
		}
	default:
		lease.prematureOnce.Do(func() { close(lease.prematureRelease) })
		return errors.New("FAKE_PREMATURE_CATALOG_RELEASE_FOR_TEST_ONLY")
	}
}

func (lease *catalogUnifiedTeardownLease) releaseSnapshot() (int, bool, bool) {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return lease.releaseCalls, lease.releaseFenceIsExact, lease.finalReleaseBounded
}

type catalogTeardownSession struct {
	source          string
	entered         chan struct{}
	canceled        chan struct{}
	providerJoined  chan struct{}
	releaseProvider chan struct{}
	closed          chan struct{}
	enterOnce       sync.Once
	cancelOnce      sync.Once
	joinOnce        sync.Once
	closeOnce       sync.Once
}

func (session *catalogTeardownSession) SourceRevision() string { return session.source }

func (session *catalogTeardownSession) ListCanonical(ctx context.Context, _ provider.PageRequest) (provider.CatalogRecordPage, error) {
	session.enterOnce.Do(func() { close(session.entered) })
	<-ctx.Done()
	session.cancelOnce.Do(func() { close(session.canceled) })
	<-session.releaseProvider
	session.joinOnce.Do(func() { close(session.providerJoined) })
	return provider.CatalogRecordPage{}, ctx.Err()
}

func (*catalogTeardownSession) Finalize(context.Context) (provider.CatalogReadProof, error) {
	return provider.CatalogReadProof{}, provider.ErrCatalogSessionIncomplete
}

func (session *catalogTeardownSession) Close() error {
	session.closeOnce.Do(func() { close(session.closed) })
	return nil
}
