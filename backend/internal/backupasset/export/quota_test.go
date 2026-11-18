package export

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func quotaCreateRequest(t *testing.T, harness serviceHarness, userID uint, key string) CreateRequest {
	t.Helper()
	item := frozenItemFixture()
	selection, err := FreezeSelection([]FrozenItem{item}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	harness.resolver.explicit = selection
	return CreateRequest{
		Actor:          SelectionActor{UserID: userID, Role: "admin"},
		Selection:      CreateSelectionV1{SchemaVersion: 1, Kind: SelectionExplicit, Refs: []backupasset.AssetRef{item.Ref}},
		IdempotencyKey: key, ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	}
}

func quotaReservationsForJob(t *testing.T, db *gorm.DB, jobID string) (QuotaReservation, int64) {
	t.Helper()
	var rows []struct {
		model.BackupAssetExportReservation
		Scope string
	}
	if err := db.Table("backup_asset_export_reservations AS r").
		Select("r.*, b.scope").Joins("JOIN backup_asset_export_quota_buckets AS b ON b.id = r.bucket_id").
		Where("r.job_id = ?", jobID).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("job %s reservations=%d, want four", jobID, len(rows))
	}
	var reservation QuotaReservation
	var storeBytes int64
	for _, row := range rows {
		switch row.Scope + "/" + row.Kind {
		case "global/job":
			reservation.GlobalJobID = row.ID
		case "user/job":
			reservation.UserJobID = row.ID
		case "global/store":
			reservation.GlobalStoreID = row.ID
			storeBytes = row.ReservedStoreBytes
		case "user/store":
			reservation.UserStoreID = row.ID
		default:
			t.Fatalf("unexpected quota reservation: %+v", row)
		}
	}
	if reservation.GlobalJobID == "" || reservation.UserJobID == "" ||
		reservation.GlobalStoreID == "" || reservation.UserStoreID == "" || storeBytes <= 0 {
		t.Fatalf("incomplete quota reservations: %+v store=%d", reservation, storeBytes)
	}
	return reservation, storeBytes
}

func TestCreateEnforcesGlobalAndUserCASAndRollsBack(t *testing.T) {
	harness := newServiceHarness(t)
	harness.service.config.Quota.GlobalActiveJobs = 2
	harness.service.config.Quota.UserActiveJobs = 1
	request := quotaCreateRequest(t, harness, 71, "quota-user-first")
	first, err := harness.service.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	_, peak := quotaReservationsForJob(t, harness.db, first.Job.ID)
	before := captureQuotaAdmissionState(t, harness.db)
	request.IdempotencyKey = "quota-user-overflow"
	if _, err := harness.service.Create(context.Background(), request); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("same-user overflow error=%v", err)
	}
	assertQuotaAdmissionStateUnchanged(t, harness.db, before, "user quota rejection")
	assertQuotaBucketTotals(t, harness, 1, peak)
	request.Actor.UserID = 72
	request.IdempotencyKey = "quota-global-second"
	if _, err := harness.service.Create(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	before = captureQuotaAdmissionState(t, harness.db)
	request.Actor.UserID = 73
	request.IdempotencyKey = "quota-global-overflow"
	if _, err := harness.service.Create(context.Background(), request); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("global overflow error=%v", err)
	}
	assertQuotaAdmissionStateUnchanged(t, harness.db, before, "global quota rejection")
}

func TestCreateEnforcesStoreByteLimitsAndRollsBack(t *testing.T) {
	for _, scope := range []string{"global", "user"} {
		t.Run(scope, func(t *testing.T) {
			harness := newServiceHarness(t)
			request := quotaCreateRequest(t, harness, 71, "quota-store-first")
			created, err := harness.service.Create(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			_, peak := quotaReservationsForJob(t, harness.db, created.Job.ID)
			if scope == "global" {
				harness.service.config.Quota.GlobalStoreBytes = 2*peak - 1
				request.Actor.UserID = 72
			} else {
				harness.service.config.Quota.UserStoreBytes = 2*peak - 1
			}
			before := captureQuotaAdmissionState(t, harness.db)
			request.IdempotencyKey = "quota-store-overflow"
			if _, err := harness.service.Create(context.Background(), request); !errors.Is(err, ErrQuotaExceeded) {
				t.Fatalf("%s store overflow error=%v", scope, err)
			}
			assertQuotaAdmissionStateUnchanged(t, harness.db, before, scope+" store rejection")
			for _, target := range []any{
				&model.BackupAssetExportJob{}, &model.BackupAssetExportKey{}, &model.BackupAssetExportItem{},
				&model.BackupAssetExportSourceLease{}, &model.BackupAssetExportIdempotency{},
			} {
				var count int64
				if err := harness.db.Model(target).Count(&count).Error; err != nil || count != 1 {
					t.Fatalf("%s store rejection %T count=%d err=%v", scope, target, count, err)
				}
			}
			var leases int64
			if err := harness.db.Model(&model.RecoveryPointLease{}).Where("holder_type = ?", "export_job").Count(&leases).Error; err != nil || leases != 1 {
				t.Fatalf("%s store rejection leases=%d err=%v", scope, leases, err)
			}
		})
	}
}

func TestCreateKeepsAdmissionDeadlineFixedAcrossQuotaLockWait(t *testing.T) {
	harness := newServiceHarness(t)
	initial := harness.service.now().UTC()
	clock := newQuotaAtomicClock(initial)
	harness.service.now = clock.Now
	request := quotaCreateRequest(t, harness, 1, "quota-fixed-admission-deadline")
	barrier := installQuotaPairLockBarrier(t, harness.db)
	type result struct {
		created CreateResult
		err     error
	}
	resultCh := make(chan result, 1)
	go func() {
		created, err := harness.service.Create(barrier.Context(context.Background()), request)
		resultCh <- result{created: created, err: err}
	}()
	barrier.Wait(t)
	clock.Set(initial.Add(2 * time.Hour))
	barrier.Release()
	got := waitForQuotaResult(t, resultCh)
	if got.err != nil {
		t.Fatalf("Create after quota lock wait: %v", got.err)
	}
	if !got.created.Job.AbsoluteDeadline.Equal(initial.Add(harness.config.MaxDuration)) {
		t.Fatalf("job deadline=%s, want fixed admission deadline", got.created.Job.AbsoluteDeadline)
	}
	var rows []model.BackupAssetExportReservation
	if err := harness.db.Where("job_id = ?", got.created.Job.ID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("reservation count=%d want=4", len(rows))
	}
	_, peak := quotaReservationsForJob(t, harness.db, got.created.Job.ID)
	assertQuotaBucketTotals(t, harness, 1, peak)
	for _, row := range rows {
		if row.State != "active" || row.LeaseOwner != got.created.Job.ID ||
			!row.LeaseExpiresAt.Equal(got.created.Job.AbsoluteDeadline) || !row.CreatedAt.Equal(initial) || !row.UpdatedAt.Equal(initial) {
			t.Fatalf("reservation moved across lock wait: %+v", row)
		}
		if (row.Kind == "job" && (row.ReservedSlots != 1 || row.ReservedStoreBytes != 0)) ||
			(row.Kind == "store" && (row.ReservedSlots != 0 || row.ReservedStoreBytes != peak)) {
			t.Fatalf("invalid reservation accounting: %+v", row)
		}
	}
	var buckets []model.BackupAssetExportQuotaBucket
	if err := harness.db.Find(&buckets).Error; err != nil {
		t.Fatal(err)
	}
	for _, bucket := range buckets {
		wantLifecycleNext := int64(1)
		if bucket.Scope == "global" {
			wantLifecycleNext = 2
		}
		if !bucket.CreatedAt.Equal(initial) || !bucket.UpdatedAt.Equal(initial) ||
			bucket.LifecycleNextSequence != wantLifecycleNext || bucket.LifecycleSweepCursor != 0 ||
			bucket.LifecycleSweepHighWater != 0 || bucket.LifecycleSweepRevision != 0 ||
			bucket.LifecycleSweepLeaseExpiresAt != nil || bucket.ReaderNextSequence != 1 ||
			bucket.ReaderSweepCursor != 0 || bucket.ReaderSweepHighWater != 0 ||
			bucket.ReaderSweepRevision != 0 || bucket.ReaderSweepLeaseExpiresAt != nil {
			t.Fatalf("unexpected initial quota bucket state: %+v", bucket)
		}
	}
}

func TestCreateRejectsNonPositiveCiphertextLimitWithoutMutation(t *testing.T) {
	for _, limit := range []int64{0, -1} {
		t.Run(strconv.FormatInt(limit, 10), func(t *testing.T) {
			harness := newServiceHarness(t)
			request := quotaCreateRequest(t, harness, 1, "quota-nonpositive-limit")
			harness.service.config.MaxCiphertextBytes = limit
			if _, err := harness.service.Create(context.Background(), request); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Create with ciphertext limit %d error=%v", limit, err)
			}
			assertNoExportCreateWrites(t, harness.db)
		})
	}
}

func TestQuotaAdmissionDoesNotObserveInjectedClockBeforeQuotaPairLocks(t *testing.T) {

	t.Run("ReserveAttemptRead", func(t *testing.T) {
		harness := newServiceHarness(t)
		_, claim, itemAttempt := createClaimedExportForAttemptBudget(t, harness, 2, "attempt-budget-clock-order")
		now := claim.LeaseExpiresAt.Add(-harness.config.LeaseRenewMargin)
		observer := installQuotaPairLockObserver(t, harness.db)
		var preLockCalls atomic.Int32
		budget, err := NewAttemptBudgetService(harness.db, func() time.Time {
			if !observer.PairLocked() {
				preLockCalls.Add(1)
			}
			return now
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := budget.ReserveAttemptRead(observer.Context(context.Background()), content.AttemptReadIntent{
			SessionID: itemAttempt.ID, Mode: content.SourceModeStat,
		}); err != nil {
			t.Fatal(err)
		}
		if !observer.PairLocked() {
			t.Fatal("ReserveAttemptRead did not lock the global and user quota buckets")
		}
		if calls := preLockCalls.Load(); calls != 0 {
			t.Fatalf("ReserveAttemptRead observed injected clock %d time(s) before quota pair locks", calls)
		}
	})
}

func TestPermanentUseLatchCommitsWithArchiveMemberFirstWriteAndSurvivesPurgeToEmpty(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	rollback := errors.New("rollback archive member first write")
	if err := harness.db.Transaction(func(tx *gorm.DB) error {
		if _, err := EnsurePermanentUseLatchTx(tx, now); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("rollback transaction error=%v", err)
	}
	var count int64
	if err := harness.db.Model(&model.BackupAssetExportQuotaBucket{}).
		Where("scope = ? AND subject = ?", "global", "global").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("rolled-back latch count=%d err=%v", count, err)
	}

	request := model.BackupAssetArchiveMemberRequest{
		ID: strings.Repeat("a", 32), OwnerUserID: 1, Endpoint: "archive_member_create",
		KeyDigest: strings.Repeat("b", 64), RequestIntentDigest: strings.Repeat("c", 64),
		RecoveryPointID: strings.Repeat("d", 32), EntryID: strings.Repeat("e", 64),
		CatalogGenerationID: strings.Repeat("f", 32), SourceFingerprint: "source-fingerprint-v1",
		EntryFingerprint: "entry-fingerprint-v1", IndexArtifactID: strings.Repeat("1", 32),
		IndexRevision: strings.Repeat("2", 64), MemberChainDigest: strings.Repeat("3", 64),
		ResolvedOrdinal: 0, State: "queued", AbsoluteExpiresAt: now.Add(time.Hour),
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	if err := harness.db.Transaction(func(tx *gorm.DB) error {
		if _, err := EnsurePermanentUseLatchTx(tx, now); err != nil {
			return err
		}
		return tx.Create(&request).Error
	}); err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Delete(&model.BackupAssetArchiveMemberRequest{}, "id = ?", request.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetExportQuotaBucket{}).
		Where("scope = ? AND subject = ?", "global", "global").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("purge-to-empty permanent latch count=%d err=%v", count, err)
	}
	var members int64
	if err := harness.db.Model(&model.BackupAssetArchiveMemberRequest{}).Count(&members).Error; err != nil || members != 0 {
		t.Fatalf("archive-member purge count=%d err=%v", members, err)
	}
}

func TestCreateConcurrentGlobalLimit(t *testing.T) {
	harness := openExportBehaviorSQLite(t)
	now := time.Now().UTC().Truncate(time.Second)
	request := quotaCreateRequest(t, harness.serviceHarness, 80, "quota-concurrent")
	selection := harness.resolver.explicit
	for index := range 4 {
		ownerUserID := uint(80 + index)
		if err := harness.db.Create(&model.User{
			ID: ownerUserID, Username: "export-quota-user-" + jobIDForIndex(index)[:1],
			PasswordHash: "hash", Role: "admin", Onboarded: true, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	harness.service.config.Quota.GlobalActiveJobs = 2
	start := make(chan struct{})
	results := make(chan error, 4)
	var ready sync.WaitGroup
	ready.Add(4)
	for index := 0; index < 4; index++ {
		service := *harness.service
		service.resolver = &selectionResolverStub{explicit: selection}
		service.leases = harness.lease
		workerRequest := request
		workerRequest.Actor.UserID = uint(80 + index)
		go func() {
			ready.Done()
			<-start
			_, err := service.Create(context.Background(), workerRequest)
			results <- err
		}()
	}
	ready.Wait()
	close(start)
	succeeded := 0
	for range 4 {
		err := <-results
		if err == nil {
			succeeded++
		} else if !errors.Is(err, ErrQuotaExceeded) {
			t.Fatalf("concurrent reserve error=%v", err)
		}
	}
	if succeeded != 2 {
		t.Fatalf("global limit admitted %d jobs want=2", succeeded)
	}
	for _, target := range []any{
		&model.BackupAssetExportJob{}, &model.BackupAssetExportKey{}, &model.BackupAssetExportItem{},
		&model.BackupAssetExportSourceLease{}, &model.BackupAssetExportIdempotency{}, &model.RecoveryPointLease{},
	} {
		var count int64
		if err := harness.db.Model(target).Count(&count).Error; err != nil || count != 2 {
			t.Fatalf("concurrent create %T count=%d err=%v, want two winners", target, count, err)
		}
	}
	var reservations int64
	if err := harness.db.Model(&model.BackupAssetExportReservation{}).Count(&reservations).Error; err != nil || reservations != 8 {
		t.Fatalf("concurrent create reservations=%d err=%v, want eight", reservations, err)
	}
	var global model.BackupAssetExportQuotaBucket
	if err := harness.db.Where("scope = ? AND subject = ?", "global", "global").Take(&global).Error; err != nil || global.ActiveJobs != 2 {
		t.Fatalf("concurrent global bucket=%+v err=%v", global, err)
	}
}

func quotaTestJob(t *testing.T, id string, ownerUserID uint, now time.Time) model.BackupAssetExportJob {
	t.Helper()
	maxCiphertextBytes, err := minimumArchiveCiphertextBytesV1(1024, 1, 65536)
	if err != nil {
		t.Fatal(err)
	}
	return model.BackupAssetExportJob{
		ID: id, OwnerUserID: ownerUserID, SelectionDigest: strings.Repeat("a", 64),
		SelectionSchemaVersion: 1, ArchiveFormat: string(ArchiveZIP), ArchiveProfile: "zip_deflate_v1",
		LimitsSchemaVersion: 1, ChunkBytes: 65536, MaxItems: 1, MaxSourcePoints: 1,
		MaxItemBytes: 1024, MaxLogicalBytes: 1024, MaxProviderBytes: 1024, MaxCiphertextBytes: maxCiphertextBytes,
		MaxOpenReaders: 1, MaxDurationSeconds: 3600, MaxAttempts: 3, RetryBaseSeconds: 1,
		RetryMaxDelaySeconds: 10, LeaseTTLSeconds: 900, LeaseRenewMarginSeconds: 300,
		ReadyTTLSeconds: 86400, ExecutionState: string(ExecutionQueued), CleanupState: string(CleanupNone),
		AbsoluteDeadline: now.Add(2 * time.Hour), TransitionRevision: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func TestCreateLocksGlobalAndUserBeforeFirstQuotaMutationAndRollsBack(t *testing.T) {
	harness := newServiceHarness(t)
	request := quotaCreateRequest(t, harness, 2, "create-quota-pair-order")

	var globalLocked atomic.Bool
	var userLocked atomic.Bool
	var pairLockedBeforeMutation atomic.Bool
	injectedErr := errors.New("injected CommitCreate quota mutation failure")
	const queryCallback = "test:observe_commit_create_quota_pair_locks"
	if err := harness.db.Callback().Query().After("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "backup_asset_export_quota_buckets" {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		for _, value := range tx.Statement.Vars {
			subject, ok := value.(string)
			if !ok {
				continue
			}
			switch subject {
			case "global":
				globalLocked.Store(true)
			case "2":
				userLocked.Store(true)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := harness.db.Callback().Query().Remove(queryCallback); err != nil {
			t.Errorf("remove CommitCreate quota query callback: %v", err)
		}
	})

	var injected atomic.Bool
	const updateCallback = "test:fail_first_commit_create_quota_mutation"
	if err := harness.db.Callback().Update().Before("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "backup_asset_export_quota_buckets" ||
			!injected.CompareAndSwap(false, true) {
			return
		}
		pairLockedBeforeMutation.Store(globalLocked.Load() && userLocked.Load())
		_ = tx.AddError(injectedErr)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := harness.db.Callback().Update().Remove(updateCallback); err != nil {
			t.Errorf("remove CommitCreate quota update callback: %v", err)
		}
	})

	_, err := harness.service.Create(context.Background(), request)
	if !errors.Is(err, injectedErr) {
		t.Fatalf("CommitCreate error=%v want injected cause", err)
	}
	if !pairLockedBeforeMutation.Load() {
		t.Fatal("CommitCreate quota counter mutation started before global and user bucket locks")
	}
	for _, target := range []any{
		&model.BackupAssetExportJob{}, &model.BackupAssetExportQuotaBucket{},
		&model.BackupAssetExportReservation{}, &model.BackupAssetExportIdempotency{},
	} {
		var count int64
		if err := harness.db.Model(target).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("rolled-back %T count=%d err=%v", target, count, err)
		}
	}
	var leases int64
	if err := harness.db.Model(&model.RecoveryPointLease{}).
		Where("holder_type = ?", "export_job").Count(&leases).Error; err != nil || leases != 0 {
		t.Fatalf("rolled-back Export leases=%d err=%v", leases, err)
	}
}

func TestLifecycleSeparatesNonStoreAndStoreRelease(t *testing.T) {
	harness := newServiceHarness(t)
	request := quotaCreateRequest(t, harness, 72, "quota-lifecycle-release")
	created, err := harness.service.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	reservation, peak := quotaReservationsForJob(t, harness.db, created.Job.ID)
	quota, err := NewQuotaService(harness.db, harness.service.now, harness.config.Quota)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "export")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	port, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
		DB: harness.db, Delivery: &persistentLifecycleDeliveryFake{}, Sources: harness.lease,
		Quota: quota, Store: store, AttemptWork: NewAttemptWorkRegistry(), Now: harness.service.now,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := port.ReleaseSourcesAndNonStore(context.Background(), created.Job.ID); err != nil {
		t.Fatal(err)
	}
	if err := port.ReleaseSourcesAndNonStore(context.Background(), created.Job.ID); err != nil {
		t.Fatalf("idempotent non-store release: %v", err)
	}
	assertQuotaBucketTotals(t, harness, 0, peak)
	assertQuotaReservationStates(t, harness, reservation, "released", "active")

	if err := port.ReleaseStoreBytes(context.Background(), created.Job.ID); err != nil {
		t.Fatal(err)
	}
	if err := port.ReleaseStoreBytes(context.Background(), created.Job.ID); err != nil {
		t.Fatalf("idempotent store release: %v", err)
	}
	assertQuotaBucketTotals(t, harness, 0, 0)
	assertQuotaReservationStates(t, harness, reservation, "released", "released")
	var latch model.BackupAssetExportQuotaBucket
	if err := harness.db.Where("scope = ? AND subject = ?", "global", "global").Take(&latch).Error; err != nil {
		t.Fatalf("permanent global latch missing after release: %v", err)
	}
}

func TestQuotaReleaseCanonicalizesReversedIDsBeforeReservationLockAndRollsBack(t *testing.T) {
	harness := newServiceHarness(t)
	quota, err := NewQuotaService(harness.db, harness.service.now, harness.config.Quota)
	if err != nil {
		t.Fatal(err)
	}
	request := quotaCreateRequest(t, harness, 1, "quota-release-reversed")
	created, err := harness.service.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	reservation, peak := quotaReservationsForJob(t, harness.db, created.Job.ID)

	var globalLocked atomic.Bool
	var userLocked atomic.Bool
	var pairLockedBeforeReservation atomic.Bool
	const queryCallback = "test:observe_reversed_release_quota_pair_locks"
	if err := harness.db.Callback().Query().After("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		switch tx.Statement.Schema.Table {
		case "backup_asset_export_quota_buckets":
			for _, value := range tx.Statement.Vars {
				subject, ok := value.(string)
				if !ok {
					continue
				}
				switch subject {
				case "global":
					globalLocked.Store(true)
				case "1":
					userLocked.Store(true)
				}
			}
		case "backup_asset_export_reservations":
			pairLockedBeforeReservation.Store(globalLocked.Load() && userLocked.Load())
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := harness.db.Callback().Query().Remove(queryCallback); err != nil {
			t.Errorf("remove reversed release query callback: %v", err)
		}
	})

	injectedErr := errors.New("injected release reservation failure")
	var injected atomic.Bool
	const updateCallback = "test:fail_reversed_release_reservation_update"
	if err := harness.db.Callback().Update().Before("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "backup_asset_export_reservations" ||
			!injected.CompareAndSwap(false, true) {
			return
		}
		_ = tx.AddError(injectedErr)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := harness.db.Callback().Update().Remove(updateCallback); err != nil {
			t.Errorf("remove reversed release update callback: %v", err)
		}
	})

	err = quota.release(context.Background(), []string{reservation.UserJobID, reservation.GlobalJobID})
	if !errors.Is(err, injectedErr) {
		t.Fatalf("release error=%v want injected cause", err)
	}
	if !pairLockedBeforeReservation.Load() {
		t.Fatal("release locked a reservation before the global and user quota buckets")
	}
	assertQuotaBucketTotals(t, harness, 1, peak)
	assertQuotaReservationStates(t, harness, reservation, "active", "active")
}

func TestAttemptBudgetUsesDurableItemSessionAndThreeRequestLimit(t *testing.T) {
	harness := newServiceHarness(t)
	created, claim, itemAttempt := createClaimedExportForAttemptBudget(t, harness, 73, "attempt-budget-three-requests")
	clock := claim.LeaseExpiresAt.Add(-harness.config.LeaseRenewMargin)
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}

	stat, err := budget.ReserveAttemptRead(context.Background(), content.AttemptReadIntent{
		SessionID: itemAttempt.ID, Mode: content.SourceModeStat,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := budget.FinalizeAttemptRead(context.Background(), content.AttemptReadFinalization{
		ReservationID: stat.ID, ReservedBytes: stat.ReservedBytes, EvidenceKnown: true, Succeeded: true,
	}); err != nil {
		t.Fatal(err)
	}

	var item model.BackupAssetExportItem
	if err := harness.db.First(&item, "id = ?", itemAttempt.ItemID).Error; err != nil {
		t.Fatal(err)
	}
	read, err := budget.ReserveAttemptRead(context.Background(), content.AttemptReadIntent{
		SessionID: itemAttempt.ID, Mode: content.SourceModeSequential, Bytes: item.LogicalSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	physicalBytes := item.LogicalSize - 1
	if err := budget.FinalizeAttemptRead(context.Background(), content.AttemptReadFinalization{
		ReservationID: read.ID, ReservedBytes: read.ReservedBytes, ProviderBytes: physicalBytes,
		EvidenceKnown: true, Succeeded: true,
	}); err != nil {
		t.Fatal(err)
	}
	revalidate, err := budget.ReserveAttemptRead(context.Background(), content.AttemptReadIntent{
		SessionID: itemAttempt.ID, Mode: content.SourceModeStat,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := budget.FinalizeAttemptRead(context.Background(), content.AttemptReadFinalization{
		ReservationID: revalidate.ID, ReservedBytes: revalidate.ReservedBytes, EvidenceKnown: true, Succeeded: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := budget.ReserveAttemptRead(context.Background(), content.AttemptReadIntent{
		SessionID: itemAttempt.ID, Mode: content.SourceModeStat,
	}); !errors.Is(err, content.ErrAttemptBudgetExceeded) {
		t.Fatalf("fourth request error=%v", err)
	}
	if _, err := budget.ReserveAttemptRead(context.Background(), content.AttemptReadIntent{
		SessionID: claim.AttemptID, Mode: content.SourceModeStat,
	}); !errors.Is(err, content.ErrAttemptBudgetExceeded) {
		t.Fatalf("job-attempt session error=%v", err)
	}

	if err := harness.db.First(&itemAttempt, "id = ?", itemAttempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if itemAttempt.ProviderBytes != physicalBytes {
		t.Fatalf("provider bytes=%d want=%d", itemAttempt.ProviderBytes, physicalBytes)
	}
	var activeReaders int64
	if err := harness.db.Model(&model.BackupAssetExportQuotaBucket{}).Select("COALESCE(SUM(active_readers), 0)").Scan(&activeReaders).Error; err != nil {
		t.Fatal(err)
	}
	if activeReaders != 0 {
		t.Fatalf("active readers=%d", activeReaders)
	}
	var reservations int64
	if err := harness.db.Model(&model.BackupAssetExportReservation{}).
		Where("job_id = ? AND kind = ?", created.JobID, "reader").Count(&reservations).Error; err != nil {
		t.Fatal(err)
	}
	if reservations != 6 {
		t.Fatalf("reader reservation rows=%d want=6", reservations)
	}
}

func TestAttemptBudgetReserveLocksQuotaPairBeforeAttemptTuple(t *testing.T) {
	harness := newServiceHarness(t)
	_, claim, itemAttempt := createClaimedExportForAttemptBudget(t, harness, 2, "attempt-budget-pair-order")
	clock := claim.LeaseExpiresAt.Add(-harness.config.LeaseRenewMargin)
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}

	var globalLocked atomic.Bool
	var userLocked atomic.Bool
	var pairLockedBeforeTuple atomic.Bool
	var tupleObserved atomic.Bool
	var tupleOrderMu sync.Mutex
	var tupleOrder []string
	const queryCallback = "test:observe_attempt_reserve_quota_pair_locks"
	if err := harness.db.Callback().Query().After("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		switch tx.Statement.Schema.Table {
		case "backup_asset_export_quota_buckets":
			for _, value := range tx.Statement.Vars {
				subject, ok := value.(string)
				if !ok {
					continue
				}
				switch subject {
				case "global":
					globalLocked.Store(true)
				case "2":
					userLocked.Store(true)
				}
			}
		case "backup_asset_export_item_attempts", "backup_asset_export_items",
			"backup_asset_export_attempts", "backup_asset_export_jobs":
			if tupleObserved.CompareAndSwap(false, true) {
				pairLockedBeforeTuple.Store(globalLocked.Load() && userLocked.Load())
			}
			tupleOrderMu.Lock()
			tupleOrder = append(tupleOrder, tx.Statement.Schema.Table)
			tupleOrderMu.Unlock()
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := harness.db.Callback().Query().Remove(queryCallback); err != nil {
			t.Errorf("remove attempt reserve query callback: %v", err)
		}
	})

	reservation, err := budget.ReserveAttemptRead(context.Background(), content.AttemptReadIntent{
		SessionID: itemAttempt.ID, Mode: content.SourceModeStat,
	})
	if err != nil {
		t.Fatal(err)
	}
	tupleOrderMu.Lock()
	reserveTupleOrder := append([]string(nil), tupleOrder...)
	tupleOrderMu.Unlock()
	wantTupleOrder := []string{
		"backup_asset_export_jobs",
		"backup_asset_export_attempts",
		"backup_asset_export_items",
		"backup_asset_export_item_attempts",
	}
	if !reflect.DeepEqual(reserveTupleOrder, wantTupleOrder) {
		t.Fatalf("attempt reserve tuple lock order=%v want canonical %v", reserveTupleOrder, wantTupleOrder)
	}
	if err := budget.FinalizeAttemptRead(context.Background(), content.AttemptReadFinalization{
		ReservationID: reservation.ID, ReservedBytes: reservation.ReservedBytes,
		EvidenceKnown: true, Succeeded: true,
	}); err != nil {
		t.Fatal(err)
	}
	if !pairLockedBeforeTuple.Load() {
		t.Fatal("attempt reserve locked its item/attempt/job tuple before the global and user quota buckets")
	}
	var activeReaders int64
	if err := harness.db.Model(&model.BackupAssetExportQuotaBucket{}).
		Select("COALESCE(SUM(active_readers), 0)").Scan(&activeReaders).Error; err != nil || activeReaders != 0 {
		t.Fatalf("active readers after reserve/finalize=%d err=%v", activeReaders, err)
	}
}

func TestAttemptBudgetReserveRejectsDeadlineCrossedWhileWaitingForQuotaPairLocks(t *testing.T) {
	harness := newServiceHarness(t)
	_, claim, itemAttempt := createClaimedExportForAttemptBudget(t, harness, 2, "attempt-budget-stale-clock")
	deadline := claim.LeaseExpiresAt.UTC()
	clock := newQuotaAtomicClock(deadline.Add(-time.Second))
	budget, err := NewAttemptBudgetService(harness.db, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	before := captureQuotaAdmissionState(t, harness.db)
	barrier := installQuotaPairLockBarrier(t, harness.db)
	type result struct {
		reservation content.AttemptReadReservation
		err         error
	}
	resultCh := make(chan result, 1)
	go func() {
		reservation, reserveErr := budget.ReserveAttemptRead(
			barrier.Context(context.Background()),
			content.AttemptReadIntent{SessionID: itemAttempt.ID, Mode: content.SourceModeStat},
		)
		resultCh <- result{reservation: reservation, err: reserveErr}
	}()

	barrier.Wait(t)
	clock.Set(deadline)
	barrier.Release()
	got := waitForQuotaResult(t, resultCh)
	if !errors.Is(got.err, content.ErrAttemptBudgetExceeded) {
		t.Errorf("ReserveAttemptRead after deadline error=%v, want ErrAttemptBudgetExceeded", got.err)
	}
	if got.reservation != (content.AttemptReadReservation{}) {
		t.Errorf("ReserveAttemptRead after deadline reservation=%+v, want zero", got.reservation)
	}
	assertQuotaAdmissionStateUnchanged(t, harness.db, before, "ReserveAttemptRead deadline rejection")
}

func TestAttemptBudgetReserveRejectsDeadlineCrossedWhileWaitingForAttemptTupleLock(t *testing.T) {
	harness := newServiceHarness(t)
	_, claim, itemAttempt := createClaimedExportForAttemptBudget(t, harness, 2, "attempt-budget-tuple-delay")
	deadline := claim.LeaseExpiresAt.UTC()
	clock := newQuotaAtomicClock(deadline.Add(-time.Second))
	budget, err := NewAttemptBudgetService(harness.db, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	before := captureQuotaAdmissionState(t, harness.db)
	barrier := installAttemptTupleLockBarrier(t, harness.db)
	type result struct {
		reservation content.AttemptReadReservation
		err         error
	}
	resultCh := make(chan result, 1)
	go func() {
		reservation, reserveErr := budget.ReserveAttemptRead(
			barrier.Context(context.Background()),
			content.AttemptReadIntent{SessionID: itemAttempt.ID, Mode: content.SourceModeStat},
		)
		resultCh <- result{reservation: reservation, err: reserveErr}
	}()

	barrier.Wait(t)
	clock.Set(deadline)
	barrier.Release()
	got := waitForQuotaResult(t, resultCh)
	if !errors.Is(got.err, content.ErrAttemptBudgetExceeded) {
		t.Errorf("ReserveAttemptRead after tuple delay error=%v, want ErrAttemptBudgetExceeded", got.err)
	}
	if got.reservation != (content.AttemptReadReservation{}) {
		t.Errorf("ReserveAttemptRead after tuple delay reservation=%+v, want zero", got.reservation)
	}
	assertQuotaAdmissionStateUnchanged(t, harness.db, before, "ReserveAttemptRead tuple-delay rejection")
}

func TestAttemptBudgetReserveRejectsDeadlineCrossedWhileWaitingForFinalAggregateQuery(t *testing.T) {
	harness := newServiceHarness(t)
	_, claim, itemAttempt := createClaimedExportForAttemptBudget(t, harness, 2, "attempt-budget-final-aggregate-delay")
	deadline := claim.LeaseExpiresAt.UTC()
	clock := newQuotaAtomicClock(deadline.Add(-time.Second))
	budget, err := NewAttemptBudgetService(harness.db, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	before := captureQuotaAdmissionState(t, harness.db)
	barrier := installFinalAttemptProviderAggregateBarrier(t, harness.db)
	type result struct {
		reservation content.AttemptReadReservation
		err         error
	}
	resultCh := make(chan result, 1)
	go func() {
		reservation, reserveErr := budget.ReserveAttemptRead(
			barrier.Context(context.Background()),
			content.AttemptReadIntent{SessionID: itemAttempt.ID, Mode: content.SourceModeStat},
		)
		resultCh <- result{reservation: reservation, err: reserveErr}
	}()

	barrier.Wait(t)
	clock.Set(deadline)
	barrier.Release()
	got := waitForQuotaResult(t, resultCh)
	if !errors.Is(got.err, content.ErrAttemptBudgetExceeded) {
		t.Errorf("ReserveAttemptRead after final aggregate delay error=%v, want ErrAttemptBudgetExceeded", got.err)
	}
	if got.reservation != (content.AttemptReadReservation{}) {
		t.Errorf("ReserveAttemptRead after final aggregate delay reservation=%+v, want zero", got.reservation)
	}
	assertQuotaAdmissionStateUnchanged(t, harness.db, before, "ReserveAttemptRead final-aggregate rejection")
}

func TestAttemptBudgetChargesUnknownReadConservativelyAndFinalizesIdempotently(t *testing.T) {
	harness := newServiceHarness(t)
	_, claim, itemAttempt := createClaimedExportForAttemptBudget(t, harness, 74, "attempt-budget-unknown")
	clock := claim.LeaseExpiresAt.Add(-harness.config.LeaseRenewMargin)
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetExportItem
	if err := harness.db.First(&item, "id = ?", itemAttempt.ItemID).Error; err != nil {
		t.Fatal(err)
	}
	reservation, err := budget.ReserveAttemptRead(context.Background(), content.AttemptReadIntent{
		SessionID: itemAttempt.ID, Mode: content.SourceModeSequential, Bytes: item.LogicalSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	finalization := content.AttemptReadFinalization{
		ReservationID: reservation.ID, ReservedBytes: reservation.ReservedBytes,
		ProviderBytes: 0, EvidenceKnown: false, Succeeded: false,
	}
	if err := budget.FinalizeAttemptRead(context.Background(), finalization); err != nil {
		t.Fatal(err)
	}
	if err := budget.FinalizeAttemptRead(context.Background(), finalization); err != nil {
		t.Fatalf("idempotent finalize: %v", err)
	}
	if err := harness.db.First(&itemAttempt, "id = ?", itemAttempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if itemAttempt.ProviderBytes != item.LogicalSize {
		t.Fatalf("unknown provider charge=%d want reservation=%d", itemAttempt.ProviderBytes, item.LogicalSize)
	}
}

type attemptBudgetFinalizeBarrierContextKey struct{}

func TestAttemptBudgetFinalizeAfterFenceRejectsChargeAndReleasesReaderPair(t *testing.T) {
	harness := newServiceHarness(t)
	created, claim, itemAttempt := createClaimedExportForAttemptBudget(t, harness, 76, "attempt-finalize-after-fence")
	clock := claim.LeaseExpiresAt.Add(-harness.config.LeaseRenewMargin)
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetExportItem
	if err := harness.db.Where("id = ?", itemAttempt.ItemID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	reservation, err := budget.ReserveAttemptRead(context.Background(), content.AttemptReadIntent{
		SessionID: itemAttempt.ID, Mode: content.SourceModeSequential, Bytes: item.LogicalSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", created.JobID).
		Update("execution_state", string(ExecutionCancelRequested)).Error; err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	const barrierCaller = "attempt-finalize-after-hint"
	hintReached := make(chan struct{}, 1)
	allowFinalize := make(chan struct{})
	var allowFinalizeOnce sync.Once
	openFinalize := func() { allowFinalizeOnce.Do(func() { close(allowFinalize) }) }
	t.Cleanup(openFinalize)
	budget.afterFinalizationHint = func() {
		select {
		case hintReached <- struct{}{}:
		case <-ctx.Done():
			return
		}
		select {
		case <-allowFinalize:
		case <-ctx.Done():
		}
	}
	var globalLocked atomic.Bool
	var userLocked atomic.Bool
	var pairLockedBeforeTuple atomic.Bool
	var qRecorded atomic.Bool
	var releaseRecorded atomic.Bool
	var chargeRecorded atomic.Bool
	var traceMu sync.Mutex
	trace := make([]string, 0, 7)
	appendTrace := func(event string) {
		traceMu.Lock()
		trace = append(trace, event)
		traceMu.Unlock()
	}
	const (
		queryCallback  = "test:trace_attempt_finalize_after_identity_hint"
		updateCallback = "test:trace_attempt_finalize_release_and_charge"
	)
	if err := harness.db.Callback().Query().After("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Context.Value(attemptBudgetFinalizeBarrierContextKey{}) != barrierCaller {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		table := tx.Statement.Table
		if tx.Statement.Schema != nil {
			table = tx.Statement.Schema.Table
		}
		switch table {
		case "backup_asset_export_quota_buckets":
			for _, value := range tx.Statement.Vars {
				subject, ok := value.(string)
				if !ok {
					continue
				}
				switch subject {
				case "global":
					globalLocked.Store(true)
					if qRecorded.CompareAndSwap(false, true) {
						appendTrace("Q")
					}
				case "76":
					userLocked.Store(true)
				}
			}
		case "backup_asset_export_jobs":
			pairLockedBeforeTuple.Store(globalLocked.Load() && userLocked.Load())
			appendTrace("J")
		case "backup_asset_export_attempts":
			appendTrace("A")
		case "backup_asset_export_items":
			appendTrace("I")
		case "backup_asset_export_item_attempts":
			appendTrace("IA")
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := harness.db.Callback().Query().Remove(queryCallback); err != nil {
			t.Errorf("remove attempt finalize lock trace callback: %v", err)
		}
	})
	if err := harness.db.Callback().Update().After("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Context.Value(attemptBudgetFinalizeBarrierContextKey{}) != barrierCaller {
			return
		}
		switch tx.Statement.Schema.Table {
		case "backup_asset_export_reservations":
			if releaseRecorded.CompareAndSwap(false, true) {
				appendTrace("reader release")
			}
		case "backup_asset_export_item_attempts":
			if chargeRecorded.CompareAndSwap(false, true) {
				appendTrace("charge")
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := harness.db.Callback().Update().Remove(updateCallback); err != nil {
			t.Errorf("remove attempt finalize release/charge update callback: %v", err)
		}
	})
	var workers sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		openFinalize()
		workers.Wait()
	})

	finalizeResult := make(chan error, 1)
	finalizeCtx := context.WithValue(ctx, attemptBudgetFinalizeBarrierContextKey{}, barrierCaller)
	workers.Add(1)
	go func() {
		defer workers.Done()
		finalizeResult <- budget.FinalizeAttemptRead(finalizeCtx, content.AttemptReadFinalization{
			ReservationID: reservation.ID, ReservedBytes: reservation.ReservedBytes,
			ProviderBytes: item.LogicalSize, EvidenceKnown: true, Succeeded: true,
		})
	}()
	select {
	case <-hintReached:
	case <-time.After(time.Second):
		t.Fatal("late reader finalization did not reach the post-identity pre-quota barrier")
	}

	port := &PersistentLifecyclePort{
		db: harness.db, now: func() time.Time { return clock }, attemptWork: NewAttemptWorkRegistry(),
	}
	fenceResult := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		fenceResult <- port.FenceAttempts(ctx, created.JobID)
	}()
	select {
	case err := <-fenceResult:
		if err != nil {
			t.Fatalf("FenceAttempts while finalization was paused after identity discovery before Q: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("FenceAttempts blocked while finalization was paused after identity discovery before Q")
	}
	var fencedJob model.BackupAssetExportJob
	var fencedAttempt model.BackupAssetExportAttempt
	var fencedItem model.BackupAssetExportItem
	var fencedItemAttempt model.BackupAssetExportItemAttempt
	if err := harness.db.Where("id = ?", created.JobID).Take(&fencedJob).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Where("id = ?", claim.AttemptID).Take(&fencedAttempt).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Where("id = ?", item.ID).Take(&fencedItem).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Where("id = ?", itemAttempt.ID).Take(&fencedItemAttempt).Error; err != nil {
		t.Fatal(err)
	}
	if fencedJob.CurrentAttemptID != nil || fencedAttempt.IsCurrent ||
		fencedAttempt.State != string(AttemptCanceled) || fencedItem.State != string(ItemFailed) ||
		fencedItemAttempt.State != string(ItemFailed) {
		t.Fatalf("FenceAttempts did not terminalize the reader tuple: job=%+v attempt=%+v item=%+v itemAttempt=%+v",
			fencedJob, fencedAttempt, fencedItem, fencedItemAttempt)
	}

	openFinalize()
	var finalizeErr error
	select {
	case finalizeErr = <-finalizeResult:
	case <-time.After(time.Second):
		t.Fatal("late reader finalization did not complete")
	}
	if !errors.Is(finalizeErr, content.ErrAttemptBudgetExceeded) {
		t.Fatalf("late reader finalization error=%v want %v", finalizeErr, content.ErrAttemptBudgetExceeded)
	}
	if !pairLockedBeforeTuple.Load() {
		t.Fatal("finalization locked its tuple before the global and user quota buckets")
	}
	traceMu.Lock()
	finalizeTrace := append([]string(nil), trace...)
	traceMu.Unlock()
	wantFinalizeTrace := []string{"Q", "J", "A", "I", "IA", "reader release"}
	if !reflect.DeepEqual(finalizeTrace, wantFinalizeTrace) {
		t.Fatalf("fenced finalization ordering=%v want=%v", finalizeTrace, wantFinalizeTrace)
	}

	var afterJob model.BackupAssetExportJob
	var afterAttempt model.BackupAssetExportAttempt
	var afterItem model.BackupAssetExportItem
	var afterItemAttempt model.BackupAssetExportItemAttempt
	if err := harness.db.Where("id = ?", created.JobID).Take(&afterJob).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Where("id = ?", claim.AttemptID).Take(&afterAttempt).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Where("id = ?", item.ID).Take(&afterItem).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Where("id = ?", itemAttempt.ID).Take(&afterItemAttempt).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterJob, fencedJob) || !reflect.DeepEqual(afterAttempt, fencedAttempt) ||
		!reflect.DeepEqual(afterItem, fencedItem) || !reflect.DeepEqual(afterItemAttempt, fencedItemAttempt) {
		t.Fatalf("late reader finalization changed fenced aggregate/state:\njob before=%+v after=%+v\nattempt before=%+v after=%+v\nitem before=%+v after=%+v\nitem-attempt before=%+v after=%+v",
			fencedJob, afterJob, fencedAttempt, afterAttempt, fencedItem, afterItem, fencedItemAttempt, afterItemAttempt)
	}
	if err := budget.FinalizeAttemptRead(context.Background(), content.AttemptReadFinalization{
		ReservationID: reservation.ID, ReservedBytes: reservation.ReservedBytes,
		ProviderBytes: item.LogicalSize, EvidenceKnown: true, Succeeded: true,
	}); err != nil {
		t.Fatalf("late reader finalization idempotence: %v", err)
	}
	var activeReaders int64
	if err := harness.db.Model(&model.BackupAssetExportQuotaBucket{}).
		Select("COALESCE(SUM(active_readers), 0)").Scan(&activeReaders).Error; err != nil || activeReaders != 0 {
		t.Fatalf("late reader finalization leaked active readers=%d err=%v", activeReaders, err)
	}
	var released int64
	if err := harness.db.Model(&model.BackupAssetExportReservation{}).
		Where("job_id = ? AND kind = ? AND state = ?", created.JobID, "reader", "released").Count(&released).Error; err != nil || released != 2 {
		t.Fatalf("late reader finalization released reservations=%d want=2 err=%v", released, err)
	}
}

func TestAttemptBudgetFinalizeLocksCanonicalTupleAfterQuotaPair(t *testing.T) {
	harness := newServiceHarness(t)
	_, claim, itemAttempt := createClaimedExportForAttemptBudget(t, harness, 77, "attempt-finalize-tuple-order")
	clock := claim.LeaseExpiresAt.Add(-harness.config.LeaseRenewMargin)
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := budget.ReserveAttemptRead(context.Background(), content.AttemptReadIntent{
		SessionID: itemAttempt.ID, Mode: content.SourceModeStat,
	})
	if err != nil {
		t.Fatal(err)
	}

	var globalLocked atomic.Bool
	var userLocked atomic.Bool
	var pairLockedBeforeTuple atomic.Bool
	var tupleObserved atomic.Bool
	var qRecorded atomic.Bool
	var releaseObserved atomic.Bool
	var chargeObserved atomic.Bool
	var traceMu sync.Mutex
	trace := make([]string, 0, 7)
	appendTrace := func(event string) {
		traceMu.Lock()
		trace = append(trace, event)
		traceMu.Unlock()
	}
	const (
		queryCallback  = "test:observe_attempt_finalize_canonical_tuple_locks"
		updateCallback = "test:observe_attempt_finalize_release_before_charge"
	)
	if err := harness.db.Callback().Query().After("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		switch tx.Statement.Schema.Table {
		case "backup_asset_export_quota_buckets":
			for _, value := range tx.Statement.Vars {
				subject, ok := value.(string)
				if !ok {
					continue
				}
				switch subject {
				case "global":
					globalLocked.Store(true)
					if qRecorded.CompareAndSwap(false, true) {
						appendTrace("Q")
					}
				case "77":
					userLocked.Store(true)
				}
			}
		case "backup_asset_export_jobs", "backup_asset_export_attempts", "backup_asset_export_items", "backup_asset_export_item_attempts":
			if tupleObserved.CompareAndSwap(false, true) {
				pairLockedBeforeTuple.Store(globalLocked.Load() && userLocked.Load())
			}
			switch tx.Statement.Schema.Table {
			case "backup_asset_export_jobs":
				appendTrace("J")
			case "backup_asset_export_attempts":
				appendTrace("A")
			case "backup_asset_export_items":
				appendTrace("I")
			case "backup_asset_export_item_attempts":
				appendTrace("IA")
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := harness.db.Callback().Query().Remove(queryCallback); err != nil {
			t.Errorf("remove attempt finalize canonical tuple lock callback: %v", err)
		}
	})
	if err := harness.db.Callback().Update().After("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil {
			return
		}
		switch tx.Statement.Schema.Table {
		case "backup_asset_export_reservations":
			if releaseObserved.CompareAndSwap(false, true) {
				appendTrace("reader release")
			}
		case "backup_asset_export_item_attempts":
			if chargeObserved.CompareAndSwap(false, true) {
				appendTrace("charge")
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := harness.db.Callback().Update().Remove(updateCallback); err != nil {
			t.Errorf("remove attempt finalize release-before-charge callback: %v", err)
		}
	})

	if err := budget.FinalizeAttemptRead(context.Background(), content.AttemptReadFinalization{
		ReservationID: reservation.ID, ReservedBytes: reservation.ReservedBytes,
		EvidenceKnown: true, Succeeded: true,
	}); err != nil {
		t.Fatal(err)
	}
	traceMu.Lock()
	observedTrace := append([]string(nil), trace...)
	traceMu.Unlock()
	wantTrace := []string{"Q", "J", "A", "I", "IA", "reader release", "charge"}
	if !reflect.DeepEqual(observedTrace, wantTrace) {
		t.Fatalf("attempt finalize order=%v want canonical %v", observedTrace, wantTrace)
	}
	if !pairLockedBeforeTuple.Load() {
		t.Fatal("attempt finalize locked its job/attempt/item/item-attempt tuple before the global and user quota buckets")
	}
}

func TestAttemptBudgetFinalizeLocksQuotaPairBeforeReservationsAndIsIdempotent(t *testing.T) {
	harness := newServiceHarness(t)
	_, claim, itemAttempt := createClaimedExportForAttemptBudget(t, harness, 1, "attempt-finalize-pair-order")
	clock := claim.LeaseExpiresAt.Add(-harness.config.LeaseRenewMargin)
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := budget.ReserveAttemptRead(context.Background(), content.AttemptReadIntent{
		SessionID: itemAttempt.ID, Mode: content.SourceModeStat,
	})
	if err != nil {
		t.Fatal(err)
	}

	var globalLocked atomic.Bool
	var userLocked atomic.Bool
	var pairLockedBeforeReservation atomic.Bool
	var reservationObserved atomic.Bool
	const queryCallback = "test:observe_attempt_finalize_quota_pair_locks"
	if err := harness.db.Callback().Query().After("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		switch tx.Statement.Schema.Table {
		case "backup_asset_export_quota_buckets":
			for _, value := range tx.Statement.Vars {
				subject, ok := value.(string)
				if !ok {
					continue
				}
				switch subject {
				case "global":
					globalLocked.Store(true)
				case "1":
					userLocked.Store(true)
				}
			}
		case "backup_asset_export_reservations":
			if reservationObserved.CompareAndSwap(false, true) {
				pairLockedBeforeReservation.Store(globalLocked.Load() && userLocked.Load())
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := harness.db.Callback().Query().Remove(queryCallback); err != nil {
			t.Errorf("remove attempt finalize query callback: %v", err)
		}
	})

	finalization := content.AttemptReadFinalization{
		ReservationID: reservation.ID, ReservedBytes: reservation.ReservedBytes,
		EvidenceKnown: true, Succeeded: true,
	}
	if err := budget.FinalizeAttemptRead(context.Background(), finalization); err != nil {
		t.Fatal(err)
	}
	if err := budget.FinalizeAttemptRead(context.Background(), finalization); err != nil {
		t.Fatalf("idempotent finalize: %v", err)
	}
	if !pairLockedBeforeReservation.Load() {
		t.Fatal("attempt finalize locked a reservation before the global and user quota buckets")
	}
	var activeReaders int64
	if err := harness.db.Model(&model.BackupAssetExportQuotaBucket{}).
		Select("COALESCE(SUM(active_readers), 0)").Scan(&activeReaders).Error; err != nil || activeReaders != 0 {
		t.Fatalf("active readers after idempotent finalize=%d err=%v", activeReaders, err)
	}
	var released int64
	if err := harness.db.Model(&model.BackupAssetExportReservation{}).
		Where("lease_owner LIKE ? AND kind = ? AND state = ?", itemAttempt.ID+":%", "reader", "released").
		Count(&released).Error; err != nil || released != 2 {
		t.Fatalf("released reader reservations=%d want=2 err=%v", released, err)
	}
}

func TestAttemptBudgetReconcilesExpiredReaderConservatively(t *testing.T) {
	harness := newServiceHarness(t)
	_, claim, itemAttempt := createClaimedExportForAttemptBudget(t, harness, 75, "attempt-budget-expired-reader")
	clock := claim.LeaseExpiresAt.Add(-harness.config.LeaseRenewMargin)
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetExportItem
	if err := harness.db.First(&item, "id = ?", itemAttempt.ItemID).Error; err != nil {
		t.Fatal(err)
	}
	reservation, err := budget.ReserveAttemptRead(context.Background(), content.AttemptReadIntent{
		SessionID: itemAttempt.ID, Mode: content.SourceModeSequential, Bytes: item.LogicalSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock = claim.LeaseExpiresAt.Add(time.Second)
	processed, err := budget.ReconcileExpiredAttemptReads(context.Background(), 10)
	if err != nil || processed != 1 {
		t.Fatalf("expired reader reconciliation processed=%d err=%v", processed, err)
	}
	if again, err := budget.ReconcileExpiredAttemptReads(context.Background(), 10); err != nil || again != 0 {
		t.Fatalf("idempotent reconciliation processed=%d err=%v", again, err)
	}
	if err := harness.db.First(&itemAttempt, "id = ?", itemAttempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if itemAttempt.ProviderBytes != reservation.ReservedBytes {
		t.Fatalf("expired reader provider bytes=%d want=%d", itemAttempt.ProviderBytes, reservation.ReservedBytes)
	}
	var activeReaders int64
	if err := harness.db.Model(&model.BackupAssetExportQuotaBucket{}).
		Select("COALESCE(SUM(active_readers), 0)").Scan(&activeReaders).Error; err != nil {
		t.Fatal(err)
	}
	if activeReaders != 0 {
		t.Fatalf("expired reader slots remain=%d", activeReaders)
	}
}

func TestAttemptBudgetExpiredReaderSweepPersistsProgressAcrossRestartPastFailures(t *testing.T) {
	harness := newServiceHarness(t)
	const limit = 2
	clock := harness.service.now().UTC()
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}

	type expiredReader struct {
		reservation content.AttemptReadReservation
		leaseOwner  string
	}
	readers := make([]expiredReader, 0, limit+1)
	for index := 0; index < limit+1; index++ {
		_, _, itemAttempt := createClaimedExportForAttemptBudget(
			t, harness, uint(86+index), "attempt-budget-reader-sweep-"+jobIDForIndex(index)[:1],
		)
		var item model.BackupAssetExportItem
		if err := harness.db.First(&item, "id = ?", itemAttempt.ItemID).Error; err != nil {
			t.Fatal(err)
		}
		reservation, err := budget.ReserveAttemptRead(context.Background(), content.AttemptReadIntent{
			SessionID: itemAttempt.ID, Mode: content.SourceModeSequential, Bytes: item.LogicalSize,
		})
		if err != nil {
			t.Fatal(err)
		}
		var anchor model.BackupAssetExportReservation
		if err := harness.db.Where("id = ?", reservation.ID).Take(&anchor).Error; err != nil {
			t.Fatal(err)
		}
		expiresAt := clock.Add(-time.Duration(limit+1-index) * time.Minute)
		if err := harness.db.Model(&model.BackupAssetExportReservation{}).
			Where("lease_owner = ? AND kind = ?", anchor.LeaseOwner, "reader").
			UpdateColumn("lease_expires_at", expiresAt).Error; err != nil {
			t.Fatal(err)
		}
		if err := harness.db.Model(&model.BackupAssetExportAttempt{}).
			Where("id = ?", itemAttempt.AttemptID).UpdateColumn("lease_expires_at", expiresAt).Error; err != nil {
			t.Fatal(err)
		}
		readers = append(readers, expiredReader{
			reservation: reservation, leaseOwner: anchor.LeaseOwner,
		})
	}
	for index := 0; index < limit; index++ {
		if err := harness.db.Model(&model.BackupAssetExportReservation{}).
			Where("id = ?", readers[index].reservation.ID).
			UpdateColumn("reserved_provider_bytes", readers[index].reservation.ReservedBytes+1).Error; err != nil {
			t.Fatal(err)
		}
	}

	processed, reconcileErr := budget.ReconcileExpiredAttemptReads(context.Background(), limit)
	if processed != 0 || !errors.Is(reconcileErr, content.ErrAttemptBudgetExceeded) {
		t.Fatalf("initial reader sweep processed=%d err=%v, want persistent failure window", processed, reconcileErr)
	}
	var afterFailures model.BackupAssetExportQuotaBucket
	if err := harness.db.Where("scope = ? AND subject = ?", "global", "global").Take(&afterFailures).Error; err != nil {
		t.Fatal(err)
	}
	if afterFailures.ReaderSweepCursor != limit || afterFailures.ReaderSweepHighWater != limit+1 ||
		afterFailures.ReaderSweepLeaseExpiresAt != nil {
		t.Fatalf("failed reader window did not persist and release its finite sweep: %+v", afterFailures)
	}

	restarted, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	processed, reconcileErr = restarted.ReconcileExpiredAttemptReads(context.Background(), limit)
	if processed != 1 || reconcileErr != nil {
		t.Fatalf("restarted reader sweep processed=%d err=%v, want healthy owner progress", processed, reconcileErr)
	}
	var afterRestart model.BackupAssetExportQuotaBucket
	if err := harness.db.Where("scope = ? AND subject = ?", "global", "global").Take(&afterRestart).Error; err != nil {
		t.Fatal(err)
	}
	if afterRestart.ReaderSweepCursor != afterRestart.ReaderSweepHighWater || afterRestart.ReaderSweepLeaseExpiresAt != nil {
		t.Fatalf("restart did not complete the persisted reader sweep: %+v", afterRestart)
	}

	var healthyReleased int64
	if err := harness.db.Model(&model.BackupAssetExportReservation{}).
		Where("lease_owner = ? AND kind = ? AND state = ?", readers[limit].leaseOwner, "reader", "released").
		Count(&healthyReleased).Error; err != nil || healthyReleased != 2 {
		t.Fatalf("healthy reader released rows=%d want=2 err=%v", healthyReleased, err)
	}
	var failedActive int64
	if err := harness.db.Model(&model.BackupAssetExportReservation{}).
		Where("lease_owner IN ? AND kind = ? AND state = ?",
			[]string{readers[0].leaseOwner, readers[1].leaseOwner}, "reader", "active").
		Count(&failedActive).Error; err != nil || failedActive != limit*2 {
		t.Fatalf("persistent reader active rows=%d want=%d err=%v", failedActive, limit*2, err)
	}
	var activeReaders int64
	if err := harness.db.Model(&model.BackupAssetExportQuotaBucket{}).
		Select("COALESCE(SUM(active_readers), 0)").Scan(&activeReaders).Error; err != nil || activeReaders != limit*2 {
		t.Fatalf("conservative active readers=%d want=%d err=%v", activeReaders, limit*2, err)
	}
}

func TestAttemptBudgetReaderSequenceIsSharedAtomicAndRollsBackWithoutGap(t *testing.T) {
	t.Run("shared atomic allocation", func(t *testing.T) {
		harness := newServiceHarness(t)
		_, claim, firstItemAttempt := createClaimedExportForAttemptBudget(t, harness, 91, "reader-sequence-first")
		_, _, secondItemAttempt := createClaimedExportForAttemptBudget(t, harness, 92, "reader-sequence-second")
		clock := claim.LeaseExpiresAt.Add(-harness.config.LeaseRenewMargin)

		type reserveResult struct {
			reservation content.AttemptReadReservation
			err         error
		}
		start := make(chan struct{})
		results := make(chan reserveResult, 2)
		for _, itemAttempt := range []model.BackupAssetExportItemAttempt{firstItemAttempt, secondItemAttempt} {
			itemAttempt := itemAttempt
			go func() {
				<-start
				budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
				if err != nil {
					results <- reserveResult{err: err}
					return
				}
				reservation, err := reserveAttemptReadWithSQLiteLockRetry(budget, content.AttemptReadIntent{
					SessionID: itemAttempt.ID, Mode: content.SourceModeStat,
				})
				results <- reserveResult{reservation: reservation, err: err}
			}()
		}
		close(start)

		reservations := make([]content.AttemptReadReservation, 0, 2)
		for range 2 {
			result := <-results
			if result.err != nil {
				t.Fatalf("concurrent reader allocation: %v", result.err)
			}
			reservations = append(reservations, result.reservation)
		}
		var global model.BackupAssetExportQuotaBucket
		if err := harness.db.Where("scope = ? AND subject = ?", "global", "global").Take(&global).Error; err != nil {
			t.Fatal(err)
		}
		if global.ReaderNextSequence != 3 {
			t.Fatalf("global reader next sequence=%d want=3", global.ReaderNextSequence)
		}
		var rows []model.BackupAssetExportReservation
		if err := harness.db.Where("bucket_id = ? AND kind = ?", global.ID, "reader").
			Order("reader_enqueue_sequence ASC").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 || rows[0].ReaderEnqueueSequence != 1 || rows[1].ReaderEnqueueSequence != 2 {
			t.Fatalf("global reader sequences=%+v, want 1 then 2", rows)
		}
		for _, reservation := range reservations {
			if err := newAttemptBudgetServiceMust(t, harness.db, clock).FinalizeAttemptRead(context.Background(), content.AttemptReadFinalization{
				ReservationID: reservation.ID, ReservedBytes: reservation.ReservedBytes, EvidenceKnown: true, Succeeded: true,
			}); err != nil {
				t.Fatalf("finalize shared reader allocation: %v", err)
			}
		}
	})

	t.Run("failed paired insert rolls back sequence", func(t *testing.T) {
		harness := newServiceHarness(t)
		_, claim, itemAttempt := createClaimedExportForAttemptBudget(t, harness, 93, "reader-sequence-rollback")
		clock := claim.LeaseExpiresAt.Add(-harness.config.LeaseRenewMargin)
		budget := newAttemptBudgetServiceMust(t, harness.db, clock)
		injectedErr := errors.New("inject paired reader insert failure")
		const callback = "test:rollback_reader_enqueue_sequence"
		var creates atomic.Int32
		if err := harness.db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "backup_asset_export_reservations" &&
				creates.Add(1) == 2 {
				_ = tx.AddError(injectedErr)
			}
		}); err != nil {
			t.Fatal(err)
		}
		_, err := budget.ReserveAttemptRead(context.Background(), content.AttemptReadIntent{
			SessionID: itemAttempt.ID, Mode: content.SourceModeStat,
		})
		if !errors.Is(err, injectedErr) {
			t.Fatalf("rollback reader reservation error=%v want injected error", err)
		}
		if err := harness.db.Callback().Create().Remove(callback); err != nil {
			t.Fatal(err)
		}
		var global model.BackupAssetExportQuotaBucket
		if err := harness.db.Where("scope = ? AND subject = ?", "global", "global").Take(&global).Error; err != nil {
			t.Fatal(err)
		}
		if global.ReaderNextSequence != 1 {
			t.Fatalf("rolled-back reader sequence next=%d want=1", global.ReaderNextSequence)
		}
		var count int64
		if err := harness.db.Model(&model.BackupAssetExportReservation{}).
			Where("kind = ?", "reader").Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("rolled-back reader reservations=%d err=%v", count, err)
		}
		reservation, err := budget.ReserveAttemptRead(context.Background(), content.AttemptReadIntent{
			SessionID: itemAttempt.ID, Mode: content.SourceModeStat,
		})
		if err != nil {
			t.Fatal(err)
		}
		var anchor model.BackupAssetExportReservation
		if err := harness.db.Where("id = ?", reservation.ID).Take(&anchor).Error; err != nil {
			t.Fatal(err)
		}
		if anchor.ReaderEnqueueSequence != 1 {
			t.Fatalf("retry reader sequence=%d want=1 after rollback", anchor.ReaderEnqueueSequence)
		}
	})
}

func TestAttemptBudgetReaderSweepDefersArrivalsUntilNextWrap(t *testing.T) {
	harness := newServiceHarness(t)
	clock := harness.service.now().UTC()
	budget := newAttemptBudgetServiceMust(t, harness.db, clock)
	first := createExpiredAttemptReadForSweep(t, harness, budget, 94, "reader-sweep-wrap-first", clock.Add(-3*time.Minute))
	second := createExpiredAttemptReadForSweep(t, harness, budget, 95, "reader-sweep-wrap-second", clock.Add(-2*time.Minute))
	third := createExpiredAttemptReadForSweep(t, harness, budget, 96, "reader-sweep-wrap-third", clock.Add(-time.Minute))

	processed, err := budget.ReconcileExpiredAttemptReads(context.Background(), 2)
	if err != nil || processed != 2 {
		t.Fatalf("first bounded reader sweep processed=%d err=%v", processed, err)
	}
	var afterFirst model.BackupAssetExportQuotaBucket
	if err := harness.db.Where("scope = ? AND subject = ?", "global", "global").Take(&afterFirst).Error; err != nil {
		t.Fatal(err)
	}
	if afterFirst.ReaderSweepCursor != 2 || afterFirst.ReaderSweepHighWater != 3 || afterFirst.ReaderSweepLeaseExpiresAt != nil {
		t.Fatalf("first bounded reader sweep state=%+v", afterFirst)
	}
	arrival := createExpiredAttemptReadForSweep(t, harness, budget, 97, "reader-sweep-wrap-arrival", clock.Add(-time.Second))
	if arrival.sequence != 4 {
		t.Fatalf("arrival reader sequence=%d want=4", arrival.sequence)
	}

	restarted := newAttemptBudgetServiceMust(t, harness.db, clock)
	processed, err = restarted.ReconcileExpiredAttemptReads(context.Background(), 2)
	if err != nil || processed != 1 {
		t.Fatalf("finite reader sweep processed=%d err=%v", processed, err)
	}
	assertReaderReservationState(t, harness, arrival.leaseOwner, "active")
	var afterFinite model.BackupAssetExportQuotaBucket
	if err := harness.db.Where("scope = ? AND subject = ?", "global", "global").Take(&afterFinite).Error; err != nil {
		t.Fatal(err)
	}
	if afterFinite.ReaderSweepCursor != 3 || afterFinite.ReaderSweepHighWater != 3 || afterFinite.ReaderSweepLeaseExpiresAt != nil {
		t.Fatalf("finite reader sweep completed wrong bounds: %+v", afterFinite)
	}

	processed, err = newAttemptBudgetServiceMust(t, harness.db, clock).ReconcileExpiredAttemptReads(context.Background(), 2)
	if err != nil || processed != 1 {
		t.Fatalf("wrapped reader sweep processed=%d err=%v", processed, err)
	}
	assertReaderReservationState(t, harness, arrival.leaseOwner, "released")
	for _, reader := range []attemptReadSweepFixture{first, second, third} {
		assertReaderReservationState(t, harness, reader.leaseOwner, "released")
	}
}

func TestAttemptBudgetReaderSweepLeaseTakeoverRejectsStaleProgressAndIsolatesAccounting(t *testing.T) {
	harness := newServiceHarness(t)
	clock := harness.service.now().UTC()
	budget := newAttemptBudgetServiceMust(t, harness.db, clock)
	reader := createExpiredAttemptReadForSweep(t, harness, budget, 98, "reader-sweep-takeover", clock.Add(-time.Minute))
	var before model.BackupAssetExportQuotaBucket
	if err := harness.db.Where("scope = ? AND subject = ?", "global", "global").Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	var beforeReservation model.BackupAssetExportReservation
	if err := harness.db.Where("id = ?", reader.reservation.ID).Take(&beforeReservation).Error; err != nil {
		t.Fatal(err)
	}
	if beforeReservation.JobID == nil {
		t.Fatal("reader reservation has no job")
	}
	var beforeJob model.BackupAssetExportJob
	if err := harness.db.Where("id = ?", *beforeReservation.JobID).Take(&beforeJob).Error; err != nil {
		t.Fatal(err)
	}

	firstSweep, acquired, err := budget.acquireExpiredAttemptReadSweep(context.Background(), clock)
	if err != nil || !acquired {
		t.Fatalf("first reader sweep acquired=%v err=%v", acquired, err)
	}
	assertReaderSweepAccountingUnchanged(t, harness, before, "first acquisition")
	if processed, err := newAttemptBudgetServiceMust(t, harness.db, clock).ReconcileExpiredAttemptReads(context.Background(), 1); err != nil || processed != 0 {
		t.Fatalf("live reader sweep exclusion processed=%d err=%v", processed, err)
	}

	takeoverAt := clock.Add(31 * time.Second)
	second := newAttemptBudgetServiceMust(t, harness.db, takeoverAt)
	secondSweep, acquired, err := second.acquireExpiredAttemptReadSweep(context.Background(), takeoverAt)
	if err != nil || !acquired {
		t.Fatalf("expired reader sweep takeover acquired=%v err=%v", acquired, err)
	}
	if secondSweep.revision != firstSweep.revision+1 || secondSweep.cursor != firstSweep.cursor ||
		secondSweep.highWater != firstSweep.highWater {
		t.Fatalf("reader sweep takeover changed durable bounds first=%+v second=%+v", firstSweep, secondSweep)
	}
	if err := budget.persistExpiredAttemptReadSweepProgress(context.Background(), &firstSweep, firstSweep.cursor, true); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stale reader sweep progress err=%v want fail closed", err)
	}
	if err := second.persistExpiredAttemptReadSweepProgress(context.Background(), &secondSweep, secondSweep.cursor, true); err != nil {
		t.Fatalf("release reader sweep takeover: %v", err)
	}
	assertReaderSweepAccountingUnchanged(t, harness, before, "takeover and release")
	var afterReservation model.BackupAssetExportReservation
	if err := harness.db.Where("id = ?", beforeReservation.ID).Take(&afterReservation).Error; err != nil {
		t.Fatal(err)
	}
	var afterJob model.BackupAssetExportJob
	if err := harness.db.Where("id = ?", beforeJob.ID).Take(&afterJob).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterReservation, beforeReservation) || !reflect.DeepEqual(afterJob, beforeJob) {
		t.Fatalf("scheduler-only reader sweep changed lifecycle rows:\nreservation before=%+v after=%+v\njob before=%+v after=%+v",
			beforeReservation, afterReservation, beforeJob, afterJob)
	}
}

func TestAttemptBudgetReaderSweepFailsClosedWithoutPermanentLatch(t *testing.T) {
	harness := newServiceHarness(t)
	clock := harness.service.now().UTC()
	budget := newAttemptBudgetServiceMust(t, harness.db, clock)
	if processed, err := budget.ReconcileExpiredAttemptReads(context.Background(), 1); err != nil || processed != 0 {
		t.Fatalf("pristine no-latch reader sweep processed=%d err=%v", processed, err)
	}
	orphan := model.BackupAssetExportReservation{
		ID: strings.Repeat("d", 32), BucketID: strings.Repeat("e", 32), Kind: "reader", ReaderEnqueueSequence: 1,
		ReservedSlots: 1, LeaseOwner: "orphan-reader", LeaseExpiresAt: clock.Add(-time.Minute), State: "active",
		CreatedAt: clock, UpdatedAt: clock,
	}
	if err := harness.db.Create(&orphan).Error; err != nil {
		t.Fatal(err)
	}
	if processed, err := budget.ReconcileExpiredAttemptReads(context.Background(), 1); !errors.Is(err, ErrUnavailable) || processed != 0 {
		t.Fatalf("orphan reader without latch processed=%d err=%v, want fail closed", processed, err)
	}

	t.Run("export job", func(t *testing.T) {
		jobHarness := newServiceHarness(t)
		_, _, _ = createClaimedExportForAttemptBudget(t, jobHarness, 101, "reader-sweep-no-latch-job")
		if err := jobHarness.db.Where("scope = ? AND subject = ?", "global", "global").
			Delete(&model.BackupAssetExportQuotaBucket{}).Error; err != nil {
			t.Fatal(err)
		}
		jobBudget := newAttemptBudgetServiceMust(t, jobHarness.db, jobHarness.service.now().UTC())
		if processed, err := jobBudget.ReconcileExpiredAttemptReads(context.Background(), 1); !errors.Is(err, ErrUnavailable) || processed != 0 {
			t.Fatalf("export job without latch processed=%d err=%v, want fail closed", processed, err)
		}
	})
}

func TestAttemptBudgetFinalizeRejectsMismatchedReaderSequencePair(t *testing.T) {
	harness := newServiceHarness(t)
	_, claim, itemAttempt := createClaimedExportForAttemptBudget(t, harness, 99, "reader-sequence-pair-mismatch")
	clock := claim.LeaseExpiresAt.Add(-harness.config.LeaseRenewMargin)
	budget := newAttemptBudgetServiceMust(t, harness.db, clock)
	reservation, err := budget.ReserveAttemptRead(context.Background(), content.AttemptReadIntent{
		SessionID: itemAttempt.ID, Mode: content.SourceModeStat,
	})
	if err != nil {
		t.Fatal(err)
	}
	var anchor model.BackupAssetExportReservation
	if err := harness.db.Where("id = ?", reservation.ID).Take(&anchor).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetExportReservation{}).
		Where("lease_owner = ? AND id <> ?", anchor.LeaseOwner, anchor.ID).
		UpdateColumn("reader_enqueue_sequence", anchor.ReaderEnqueueSequence+1).Error; err != nil {
		t.Fatal(err)
	}
	err = budget.FinalizeAttemptRead(context.Background(), content.AttemptReadFinalization{
		ReservationID: reservation.ID, ReservedBytes: reservation.ReservedBytes, EvidenceKnown: true, Succeeded: true,
	})
	if !errors.Is(err, content.ErrAttemptBudgetExceeded) {
		t.Fatalf("mismatched reader sequence finalization err=%v", err)
	}
	assertReaderReservationState(t, harness, anchor.LeaseOwner, "active")
}

type attemptBudgetReaderSweepBarrierContextKey struct{}

func TestAttemptBudgetReaderSweepCommitsSchedulerTransactionBeforeFinalization(t *testing.T) {
	harness := newServiceHarness(t)
	clock := harness.service.now().UTC()
	budget := newAttemptBudgetServiceMust(t, harness.db, clock)
	_ = createExpiredAttemptReadForSweep(t, harness, budget, 100, "reader-sweep-transaction-release", clock.Add(-time.Minute))
	const caller = "reader_sweep_transaction_release"
	const callback = "test:reader_sweep_transaction_release_before_finalize"
	var candidateLoaded atomic.Bool
	var candidateLoadedOutsideTransaction atomic.Bool
	var finalizationQuotaLockAfterCandidate atomic.Bool
	if err := harness.db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Context.Value(attemptBudgetReaderSweepBarrierContextKey{}) != caller {
			return
		}
		switch tx.Statement.Schema.Table {
		case "backup_asset_export_reservations":
			if _, locked := tx.Statement.Clauses["FOR"]; locked ||
				!strings.Contains(tx.Statement.SQL.String(), "reader_enqueue_sequence") {
				return
			}
			candidateLoaded.Store(true)
			_, insideTransaction := tx.Statement.ConnPool.(*sql.Tx)
			candidateLoadedOutsideTransaction.Store(!insideTransaction)
		case "backup_asset_export_quota_buckets":
			if _, locked := tx.Statement.Clauses["FOR"]; locked && candidateLoaded.Load() {
				finalizationQuotaLockAfterCandidate.Store(true)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := harness.db.Callback().Query().Remove(callback); err != nil {
			t.Errorf("remove reader sweep transaction callback: %v", err)
		}
	})

	processed, err := budget.ReconcileExpiredAttemptReads(
		context.WithValue(context.Background(), attemptBudgetReaderSweepBarrierContextKey{}, caller), 1,
	)
	if err != nil || processed != 1 {
		t.Fatalf("reader sweep after scheduler transaction release processed=%d err=%v", processed, err)
	}
	if !candidateLoadedOutsideTransaction.Load() || !finalizationQuotaLockAfterCandidate.Load() {
		t.Fatalf("reader sweep candidate/finalization transaction boundary not observed: candidate_outside_tx=%t finalization_after_candidate=%t",
			candidateLoadedOutsideTransaction.Load(), finalizationQuotaLockAfterCandidate.Load())
	}
}

func TestAttemptBudgetReservePreservesDatabaseErrorAndRollsBackPair(t *testing.T) {
	harness := newServiceHarness(t)
	created, claim, itemAttempt := createClaimedExportForAttemptBudget(t, harness, 2, "attempt-reserve-db-error")
	clock := claim.LeaseExpiresAt.Add(-harness.config.LeaseRenewMargin)
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}

	injectedErr := errors.New("injected reader bucket database failure")
	var bucketUpdates atomic.Int32
	const updateCallback = "test:fail_second_attempt_reader_bucket_update"
	if err := harness.db.Callback().Update().Before("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "backup_asset_export_quota_buckets" {
			return
		}
		if bucketUpdates.Add(1) == 2 {
			_ = tx.AddError(injectedErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := harness.db.Callback().Update().Remove(updateCallback); err != nil {
			t.Errorf("remove attempt reserve database-error callback: %v", err)
		}
	})

	_, err = budget.ReserveAttemptRead(context.Background(), content.AttemptReadIntent{
		SessionID: itemAttempt.ID, Mode: content.SourceModeStat,
	})
	if !errors.Is(err, injectedErr) {
		t.Fatalf("attempt reserve error=%v want injected database cause", err)
	}
	var activeReaders int64
	if err := harness.db.Model(&model.BackupAssetExportQuotaBucket{}).
		Select("COALESCE(SUM(active_readers), 0)").Scan(&activeReaders).Error; err != nil || activeReaders != 0 {
		t.Fatalf("rolled-back active readers=%d err=%v", activeReaders, err)
	}
	var reservations int64
	if err := harness.db.Model(&model.BackupAssetExportReservation{}).
		Where("job_id = ? AND kind = ?", created.JobID, "reader").Count(&reservations).Error; err != nil || reservations != 0 {
		t.Fatalf("rolled-back reader reservations=%d err=%v", reservations, err)
	}
}

func TestAttemptBudgetReservePreservesTupleLockDatabaseErrorsAndRollsBackPair(t *testing.T) {
	testCases := []struct {
		name  string
		table string
	}{
		{name: "ItemAttempt", table: "backup_asset_export_item_attempts"},
		{name: "Item", table: "backup_asset_export_items"},
		{name: "Attempt", table: "backup_asset_export_attempts"},
		{name: "Job", table: "backup_asset_export_jobs"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newServiceHarness(t)
			created, claim, itemAttempt := createClaimedExportForAttemptBudget(
				t, harness, 2, "attempt-tuple-lock-db-error-"+testCase.name,
			)
			clock := claim.LeaseExpiresAt.Add(-harness.config.LeaseRenewMargin)
			budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
			if err != nil {
				t.Fatal(err)
			}

			var before []model.BackupAssetExportQuotaBucket
			if err := harness.db.Order("scope ASC, subject ASC").Find(&before).Error; err != nil {
				t.Fatal(err)
			}
			injectedErr := errors.New("injected " + testCase.name + " tuple lock database failure")
			var globalLocked atomic.Bool
			var userLocked atomic.Bool
			var targetObserved atomic.Bool
			var pairLockedBeforeTarget atomic.Bool
			const queryCallback = "test:fail_attempt_tuple_lock_query"
			if err := harness.db.Callback().Query().After("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
				if tx.Statement.Schema == nil {
					return
				}
				if _, locked := tx.Statement.Clauses["FOR"]; !locked {
					return
				}
				if tx.Statement.Schema.Table == "backup_asset_export_quota_buckets" {
					for _, value := range tx.Statement.Vars {
						subject, ok := value.(string)
						if !ok {
							continue
						}
						switch subject {
						case "global":
							globalLocked.Store(true)
						case "2":
							userLocked.Store(true)
						}
					}
					return
				}
				if tx.Statement.Schema.Table != testCase.table || !targetObserved.CompareAndSwap(false, true) {
					return
				}
				pairLockedBeforeTarget.Store(globalLocked.Load() && userLocked.Load())
				_ = tx.AddError(injectedErr)
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := harness.db.Callback().Query().Remove(queryCallback); err != nil {
					t.Errorf("remove attempt tuple-lock database-error callback: %v", err)
				}
			})

			_, err = budget.ReserveAttemptRead(context.Background(), content.AttemptReadIntent{
				SessionID: itemAttempt.ID, Mode: content.SourceModeStat,
			})
			if !errors.Is(err, injectedErr) {
				t.Fatalf("attempt tuple lock error=%v want injected database cause", err)
			}
			if !targetObserved.Load() || !pairLockedBeforeTarget.Load() {
				t.Fatalf("target observed=%t pair locked first=%t", targetObserved.Load(), pairLockedBeforeTarget.Load())
			}
			var after []model.BackupAssetExportQuotaBucket
			if err := harness.db.Order("scope ASC, subject ASC").Find(&after).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("tuple lock failure changed quota buckets:\nbefore=%+v\nafter=%+v", before, after)
			}
			var activeReaders int64
			if err := harness.db.Model(&model.BackupAssetExportQuotaBucket{}).
				Select("COALESCE(SUM(active_readers), 0)").Scan(&activeReaders).Error; err != nil || activeReaders != 0 {
				t.Fatalf("tuple lock failure left active readers=%d err=%v", activeReaders, err)
			}
			var readerReservations int64
			if err := harness.db.Model(&model.BackupAssetExportReservation{}).
				Where("job_id = ? AND kind = ?", created.JobID, "reader").Count(&readerReservations).Error; err != nil || readerReservations != 0 {
				t.Fatalf("tuple lock failure left reader reservations=%d err=%v", readerReservations, err)
			}
		})
	}
}

func createClaimedExportForAttemptBudget(
	t *testing.T, harness serviceHarness, userID uint, idempotencyKey string,
) (CommitCreateResult, AttemptClaim, model.BackupAssetExportItemAttempt) {
	t.Helper()
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: userID, Role: "admin"}, Selection: selection,
		IdempotencyKey: idempotencyKey, ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := harness.service.now().UTC()
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-budget"})
	if err != nil {
		t.Fatal(err)
	}
	var itemAttempt model.BackupAssetExportItemAttempt
	if err := harness.db.Where("job_id = ? AND attempt_id = ?", created.JobID, claim.AttemptID).Take(&itemAttempt).Error; err != nil {
		t.Fatal(err)
	}
	return created, claim, itemAttempt
}

func assertQuotaBucketTotals(t *testing.T, harness serviceHarness, activeJobs, storeBytes int64) {
	t.Helper()
	var buckets []model.BackupAssetExportQuotaBucket
	if err := harness.db.Order("scope ASC").Find(&buckets).Error; err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("quota bucket count=%d want=2", len(buckets))
	}
	for _, bucket := range buckets {
		if bucket.ActiveJobs != activeJobs || bucket.ReservedStoreBytes != storeBytes {
			t.Fatalf("bucket %s/%s totals jobs=%d store=%d want jobs=%d store=%d",
				bucket.Scope, bucket.Subject, bucket.ActiveJobs, bucket.ReservedStoreBytes, activeJobs, storeBytes)
		}
	}
}

func assertQuotaReservationStates(
	t *testing.T,
	harness serviceHarness,
	reservation QuotaReservation,
	wantJob, wantStore string,
) {
	t.Helper()
	wants := map[string]string{
		reservation.GlobalJobID: wantJob, reservation.UserJobID: wantJob,
		reservation.GlobalStoreID: wantStore, reservation.UserStoreID: wantStore,
	}
	var rows []model.BackupAssetExportReservation
	if err := harness.db.Where("id IN ?", []string{
		reservation.GlobalJobID, reservation.UserJobID, reservation.GlobalStoreID, reservation.UserStoreID,
	}).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(wants) {
		t.Fatalf("quota reservation count=%d want=%d", len(rows), len(wants))
	}
	for _, row := range rows {
		if row.State != wants[row.ID] {
			t.Fatalf("reservation %s kind=%s state=%s want=%s", row.ID, row.Kind, row.State, wants[row.ID])
		}
	}
}

type quotaAdmissionState struct {
	buckets      []model.BackupAssetExportQuotaBucket
	reservations []model.BackupAssetExportReservation
}

func captureQuotaAdmissionState(t *testing.T, db *gorm.DB) quotaAdmissionState {
	t.Helper()
	var state quotaAdmissionState
	if err := db.Order("scope ASC, subject ASC").Find(&state.buckets).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Order("id ASC").Find(&state.reservations).Error; err != nil {
		t.Fatal(err)
	}
	return state
}

func assertQuotaAdmissionStateUnchanged(t *testing.T, db *gorm.DB, before quotaAdmissionState, phase string) {
	t.Helper()
	after := captureQuotaAdmissionState(t, db)
	if !reflect.DeepEqual(after.buckets, before.buckets) {
		t.Errorf("%s changed quota buckets:\nbefore=%+v\nafter=%+v", phase, before.buckets, after.buckets)
	}
	if !reflect.DeepEqual(after.reservations, before.reservations) {
		t.Errorf("%s changed quota reservations:\nbefore=%+v\nafter=%+v", phase, before.reservations, after.reservations)
	}
}

type quotaAtomicClock struct {
	unixNano atomic.Int64
}

func newQuotaAtomicClock(now time.Time) *quotaAtomicClock {
	clock := &quotaAtomicClock{}
	clock.Set(now)
	return clock
}

func (clock *quotaAtomicClock) Now() time.Time {
	return time.Unix(0, clock.unixNano.Load()).UTC()
}

func (clock *quotaAtomicClock) Set(now time.Time) {
	clock.unixNano.Store(now.UTC().UnixNano())
}

type quotaPairLockBarrierContextKey struct{}

type quotaPairLockBarrier struct {
	token       *struct{}
	entered     chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
}

func installQuotaPairLockBarrier(t *testing.T, db *gorm.DB) *quotaPairLockBarrier {
	t.Helper()
	barrier := &quotaPairLockBarrier{
		token: &struct{}{}, entered: make(chan struct{}), release: make(chan struct{}),
	}
	const callbackName = "test:block_export_quota_pair_lock"
	if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Context.Value(quotaPairLockBarrierContextKey{}) != barrier.token ||
			tx.Statement.Schema == nil || tx.Statement.Schema.Table != "backup_asset_export_quota_buckets" {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		globalLock := false
		for _, value := range tx.Statement.Vars {
			if subject, ok := value.(string); ok && subject == "global" {
				globalLock = true
				break
			}
		}
		if !globalLock {
			return
		}
		barrier.enterOnce.Do(func() {
			close(barrier.entered)
			<-barrier.release
		})
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		barrier.Release()
		if err := db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove quota-pair lock barrier callback: %v", err)
		}
	})
	return barrier
}

func (barrier *quotaPairLockBarrier) Context(parent context.Context) context.Context {
	return context.WithValue(parent, quotaPairLockBarrierContextKey{}, barrier.token)
}

func (barrier *quotaPairLockBarrier) Wait(t *testing.T) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-barrier.entered:
	case <-timer.C:
		t.Fatal("quota reservation did not reach the quota-pair lock barrier")
	}
}

func (barrier *quotaPairLockBarrier) Release() {
	barrier.releaseOnce.Do(func() { close(barrier.release) })
}

type quotaPairLockObserverContextKey struct{}

type quotaPairLockObserver struct {
	token        *struct{}
	globalLocked atomic.Bool
	userLocked   atomic.Bool
}

func installQuotaPairLockObserver(t *testing.T, db *gorm.DB) *quotaPairLockObserver {
	t.Helper()
	observer := &quotaPairLockObserver{token: &struct{}{}}
	const callbackName = "test:observe_export_quota_pair_lock"
	if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Context.Value(quotaPairLockObserverContextKey{}) != observer.token ||
			tx.Statement.Schema == nil || tx.Statement.Schema.Table != "backup_asset_export_quota_buckets" {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		for _, value := range tx.Statement.Vars {
			subject, ok := value.(string)
			if !ok {
				continue
			}
			switch subject {
			case "global":
				observer.globalLocked.Store(true)
			default:
				if _, err := strconv.ParseUint(subject, 10, 64); err == nil {
					observer.userLocked.Store(true)
				}
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove quota-pair lock observer callback: %v", err)
		}
	})
	return observer
}

func (observer *quotaPairLockObserver) Context(parent context.Context) context.Context {
	return context.WithValue(parent, quotaPairLockObserverContextKey{}, observer.token)
}

func (observer *quotaPairLockObserver) PairLocked() bool {
	return observer.globalLocked.Load() && observer.userLocked.Load()
}

type attemptTupleLockBarrierContextKey struct{}

type attemptTupleLockBarrier struct {
	token                 *struct{}
	entered               chan struct{}
	release               chan struct{}
	enterOnce             sync.Once
	releaseOnce           sync.Once
	globalLocked          atomic.Bool
	userLocked            atomic.Bool
	pairLockedBeforeTuple atomic.Bool
}

func installAttemptTupleLockBarrier(t *testing.T, db *gorm.DB) *attemptTupleLockBarrier {
	t.Helper()
	barrier := &attemptTupleLockBarrier{
		token: &struct{}{}, entered: make(chan struct{}), release: make(chan struct{}),
	}
	const observeCallback = "test:observe_attempt_tuple_barrier_quota_locks"
	if err := db.Callback().Query().After("gorm:query").Register(observeCallback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Context.Value(attemptTupleLockBarrierContextKey{}) != barrier.token ||
			tx.Statement.Schema == nil || tx.Statement.Schema.Table != "backup_asset_export_quota_buckets" {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		for _, value := range tx.Statement.Vars {
			if subject, ok := value.(string); ok {
				switch subject {
				case "global":
					barrier.globalLocked.Store(true)
				case "2":
					barrier.userLocked.Store(true)
				}
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	const barrierCallback = "test:block_first_export_attempt_tuple_lock"
	if err := db.Callback().Query().Before("gorm:query").Register(barrierCallback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Context.Value(attemptTupleLockBarrierContextKey{}) != barrier.token ||
			tx.Statement.Schema == nil || tx.Statement.Schema.Table != "backup_asset_export_jobs" {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		barrier.enterOnce.Do(func() {
			barrier.pairLockedBeforeTuple.Store(barrier.globalLocked.Load() && barrier.userLocked.Load())
			close(barrier.entered)
			<-barrier.release
		})
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		barrier.Release()
		if err := db.Callback().Query().Remove(barrierCallback); err != nil {
			t.Errorf("remove attempt tuple lock barrier callback: %v", err)
		}
		if err := db.Callback().Query().Remove(observeCallback); err != nil {
			t.Errorf("remove attempt tuple quota observer callback: %v", err)
		}
	})
	return barrier
}

func (barrier *attemptTupleLockBarrier) Context(parent context.Context) context.Context {
	return context.WithValue(parent, attemptTupleLockBarrierContextKey{}, barrier.token)
}

func (barrier *attemptTupleLockBarrier) Wait(t *testing.T) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-barrier.entered:
		if !barrier.pairLockedBeforeTuple.Load() {
			t.Fatal("attempt tuple wait began before the global and user quota buckets were locked")
		}
	case <-timer.C:
		t.Fatal("attempt reservation did not reach the first tuple-lock barrier")
	}
}

func (barrier *attemptTupleLockBarrier) Release() {
	barrier.releaseOnce.Do(func() { close(barrier.release) })
}

type finalAttemptProviderAggregateBarrierContextKey struct{}

type finalAttemptProviderAggregateBarrier struct {
	token       *struct{}
	entered     chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
}

func installFinalAttemptProviderAggregateBarrier(t *testing.T, db *gorm.DB) *finalAttemptProviderAggregateBarrier {
	t.Helper()
	barrier := &finalAttemptProviderAggregateBarrier{
		token: &struct{}{}, entered: make(chan struct{}), release: make(chan struct{}),
	}
	const callbackName = "test:block_final_attempt_provider_aggregate"
	if err := db.Callback().Row().After("gorm:row").Register(callbackName, func(tx *gorm.DB) {
		querySQL := ""
		if tx.Statement != nil {
			querySQL = strings.ToLower(tx.Statement.SQL.String())
		}
		if tx.Statement == nil ||
			tx.Statement.Context.Value(finalAttemptProviderAggregateBarrierContextKey{}) != barrier.token ||
			tx.Statement.Schema == nil || tx.Statement.Schema.Table != "backup_asset_export_reservations" ||
			!strings.Contains(querySQL, "sum") || !strings.Contains(querySQL, "reserved_provider_bytes") {
			return
		}
		barrier.enterOnce.Do(func() {
			close(barrier.entered)
			<-barrier.release
		})
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		barrier.Release()
		if err := db.Callback().Row().Remove(callbackName); err != nil {
			t.Errorf("remove final attempt provider aggregate barrier callback: %v", err)
		}
	})
	return barrier
}

func (barrier *finalAttemptProviderAggregateBarrier) Context(parent context.Context) context.Context {
	return context.WithValue(parent, finalAttemptProviderAggregateBarrierContextKey{}, barrier.token)
}

func (barrier *finalAttemptProviderAggregateBarrier) Wait(t *testing.T) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-barrier.entered:
	case <-timer.C:
		t.Fatal("attempt reservation did not reach the final provider aggregate barrier")
	}
}

func (barrier *finalAttemptProviderAggregateBarrier) Release() {
	barrier.releaseOnce.Do(func() { close(barrier.release) })
}

func waitForQuotaResult[T any](t *testing.T, resultCh <-chan T) T {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case result := <-resultCh:
		return result
	case <-timer.C:
		t.Fatal("quota reservation did not finish after releasing the quota-pair lock barrier")
		var zero T
		return zero
	}
}

func jobIDForIndex(index int) string {
	digit := byte('1' + index)
	value := make([]byte, 32)
	for position := range value {
		value[position] = digit
	}
	return string(value)
}

type attemptReadSweepFixture struct {
	reservation content.AttemptReadReservation
	leaseOwner  string
	sequence    int64
}

func newAttemptBudgetServiceMust(t *testing.T, db *gorm.DB, now time.Time) *AttemptBudgetService {
	t.Helper()
	service, err := NewAttemptBudgetService(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func reserveAttemptReadWithSQLiteLockRetry(
	service *AttemptBudgetService,
	intent content.AttemptReadIntent,
) (content.AttemptReadReservation, error) {
	var lastErr error
	for attempt := 0; attempt < 50; attempt++ {
		reservation, err := service.ReserveAttemptRead(context.Background(), intent)
		if err == nil {
			return reservation, nil
		}
		if !strings.Contains(err.Error(), "database table is locked") {
			return content.AttemptReadReservation{}, err
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	return content.AttemptReadReservation{}, lastErr
}

func createExpiredAttemptReadForSweep(
	t *testing.T,
	harness serviceHarness,
	budget *AttemptBudgetService,
	userID uint,
	idempotencyKey string,
	expiresAt time.Time,
) attemptReadSweepFixture {
	t.Helper()
	_, _, itemAttempt := createClaimedExportForAttemptBudget(t, harness, userID, idempotencyKey)
	var item model.BackupAssetExportItem
	if err := harness.db.Where("id = ?", itemAttempt.ItemID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	reservation, err := budget.ReserveAttemptRead(context.Background(), content.AttemptReadIntent{
		SessionID: itemAttempt.ID, Mode: content.SourceModeSequential, Bytes: item.LogicalSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	var anchor model.BackupAssetExportReservation
	if err := harness.db.Where("id = ?", reservation.ID).Take(&anchor).Error; err != nil {
		t.Fatal(err)
	}
	if anchor.ReaderEnqueueSequence <= 0 {
		t.Fatalf("reader anchor sequence=%d, want positive", anchor.ReaderEnqueueSequence)
	}
	if err := harness.db.Model(&model.BackupAssetExportReservation{}).
		Where("lease_owner = ? AND kind = ?", anchor.LeaseOwner, "reader").
		UpdateColumn("lease_expires_at", expiresAt.UTC()).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetExportAttempt{}).
		Where("id = ?", itemAttempt.AttemptID).UpdateColumn("lease_expires_at", expiresAt.UTC()).Error; err != nil {
		t.Fatal(err)
	}
	return attemptReadSweepFixture{
		reservation: reservation,
		leaseOwner:  anchor.LeaseOwner,
		sequence:    anchor.ReaderEnqueueSequence,
	}
}

func assertReaderReservationState(t *testing.T, harness serviceHarness, leaseOwner, want string) {
	t.Helper()
	var rows []model.BackupAssetExportReservation
	if err := harness.db.Where("lease_owner = ? AND kind = ?", leaseOwner, "reader").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("reader pair %q count=%d want=2", leaseOwner, len(rows))
	}
	for _, row := range rows {
		if row.State != want {
			t.Fatalf("reader pair %q row=%s state=%s want=%s", leaseOwner, row.ID, row.State, want)
		}
	}
}

func assertReaderSweepAccountingUnchanged(
	t *testing.T,
	harness serviceHarness,
	before model.BackupAssetExportQuotaBucket,
	phase string,
) {
	t.Helper()
	var after model.BackupAssetExportQuotaBucket
	if err := harness.db.Where("id = ?", before.ID).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.TransitionRevision != before.TransitionRevision || after.ActiveJobs != before.ActiveJobs ||
		after.ActiveWorkers != before.ActiveWorkers || after.ActiveReaders != before.ActiveReaders ||
		after.ReservedStoreBytes != before.ReservedStoreBytes || after.UsedStoreBytes != before.UsedStoreBytes ||
		!after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("%s reader scheduler changed quota accounting/timestamp: before=%+v after=%+v", phase, before, after)
	}
}
