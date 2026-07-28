package export

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	configpkg "xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"

	"golang.org/x/sys/unix"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func newWorkerServiceHarness(t *testing.T) serviceHarness {
	t.Helper()
	harness := newServiceHarness(t)
	harness.config.Quota.UserStoreBytes = 128 << 20
	harness.config.Quota.GlobalStoreBytes = 256 << 20
	harness.service.config = harness.config
	return harness
}

func TestAttemptCoordinatorClaimSQLiteBusyHonorsContextWithoutMutatingState(t *testing.T) {
	fixture := newSQLiteClaimContentionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := fixture.coordinator.Claim(ctx, fixture.request)
		result <- err
	}()
	fixture.waitForClaimConnection(t)

	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("SQLite busy claim error=%v, want context deadline exceeded", err)
		}
	case <-time.After(500 * time.Millisecond):
		fixture.release()
		select {
		case <-result:
		case <-time.After(time.Second):
			t.Fatal("SQLite busy Claim did not stop after releasing the holder")
		}
		t.Fatal("SQLite busy Claim remained blocked after its context deadline")
	}
	fixture.release()
	fixture.assertNoClaimState(t)
	fixture.assertBusyTimeoutRestored(t)
}

func TestAttemptCoordinatorMaintainSourceLeasesSQLiteBusyHonorsContextWithoutMutation(t *testing.T) {
	fixture := newSQLiteClaimContentionFixture(t)
	sourceLeases, err := backupasset.NewLeaseService(fixture.db, func() time.Time {
		return time.Date(2026, 7, 26, 20, 0, 0, 0, time.UTC)
	}, backupasset.LeaseConfig{
		Duration: 15 * time.Minute, Heartbeat: 5 * time.Minute, AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.coordinator, err = NewAttemptCoordinator(fixture.db, func() time.Time {
		return time.Date(2026, 7, 26, 20, 0, 0, 0, time.UTC)
	}, sourceLeases)
	if err != nil {
		t.Fatal(err)
	}

	beforeAuthority := loadSourceAuthoritySnapshot(t, fixture.db, fixture.jobID)
	var beforeJob model.BackupAssetExportJob
	if err := fixture.db.First(&beforeJob, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := fixture.coordinator.MaintainSourceLeases(ctx, SourceLeaseMaintenanceRequest{JobID: fixture.jobID})
		result <- err
	}()
	fixture.waitForClaimConnection(t)

	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("SQLite busy source maintenance error=%v, want context deadline exceeded", err)
		}
	case <-time.After(500 * time.Millisecond):
		fixture.release()
		select {
		case <-result:
		case <-time.After(time.Second):
			t.Fatal("SQLite busy source maintenance did not stop after releasing the holder")
		}
		t.Fatal("SQLite busy source maintenance remained blocked after its context deadline")
	}
	fixture.release()
	assertSourceAuthorityUnchanged(t, fixture.db, fixture.jobID, beforeAuthority)
	var afterJob model.BackupAssetExportJob
	if err := fixture.db.First(&afterJob, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterJob, beforeJob) {
		t.Fatalf("busy source maintenance mutated job: before=%+v after=%+v", beforeJob, afterJob)
	}
	fixture.assertBusyTimeoutRestored(t)
}

func TestAttemptCoordinatorClaimSQLiteBusyRetriesAndPreservesClaimState(t *testing.T) {
	fixture := newSQLiteClaimContentionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	type claimResult struct {
		claim AttemptClaim
		err   error
	}
	result := make(chan claimResult, 1)
	go func() {
		claim, err := fixture.coordinator.Claim(ctx, fixture.request)
		result <- claimResult{claim: claim, err: err}
	}()
	fixture.waitForClaimConnection(t)
	fixture.release()

	select {
	case outcome := <-result:
		if outcome.err != nil {
			t.Fatalf("SQLite busy claim after release: %v", outcome.err)
		}
		fixture.assertClaimState(t, outcome.claim)
		fixture.assertBusyTimeoutRestored(t)
	case <-time.After(time.Second):
		t.Fatal("SQLite busy Claim did not retry after releasing the holder")
	}
}

func TestAttemptCoordinatorClaimSQLiteRestoresBusyTimeoutAfterTerminalPaths(t *testing.T) {
	t.Run("not claimable", func(t *testing.T) {
		fixture := newSQLiteClaimContentionFixture(t)
		fixture.release()
		if err := fixture.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
			Update("execution_state", string(ExecutionCanceled)).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.coordinator.Claim(context.Background(), fixture.request); !errors.Is(err, ErrAttemptNotClaimable) {
			t.Fatalf("not-claimable SQLite error=%v", err)
		}
		fixture.assertNoClaimArtifacts(t)
		fixture.assertBusyTimeoutRestored(t)
	})

	t.Run("panic", func(t *testing.T) {
		fixture := newSQLiteClaimContentionFixture(t)
		fixture.release()
		const callbackName = "test:sqlite-claim-panic-restore"
		var entered, panicEnabled bool
		panicEnabled = true
		if err := fixture.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
			if panicEnabled && tx.Statement != nil && tx.Statement.Schema != nil && tx.Statement.Schema.Table == "backup_asset_export_jobs" {
				entered = true
				panic("injected SQLite Claim panic")
			}
		}); err != nil {
			t.Fatal(err)
		}
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			_, _ = fixture.coordinator.Claim(context.Background(), fixture.request)
		}()
		panicEnabled = false
		if !entered || recovered == nil {
			t.Fatalf("SQLite Claim panic entered=%v recovered=%v", entered, recovered)
		}
		fixture.assertNoClaimState(t)
		fixture.assertBusyTimeoutRestored(t)
	})
}

type sqliteClaimContentionFixture struct {
	db            *gorm.DB
	primary       *sql.DB
	coordinator   *AttemptCoordinator
	request       AttemptClaimRequest
	jobID         string
	itemID        string
	busyTimeout   int64
	releaseHolder func()
}

func newSQLiteClaimContentionFixture(t *testing.T) sqliteClaimContentionFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claim-contention.db")
	db, err := database.Open(configpkg.Config{DBType: "sqlite", SQLitePath: path})
	if err != nil {
		t.Fatal(err)
	}
	primary, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	primary.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportAttempt{},
		&model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportSourceLease{}, &model.RecoveryPointLease{},
	); err != nil {
		t.Fatal(err)
	}
	var busyTimeout int64
	if err := db.Raw("PRAGMA busy_timeout").Scan(&busyTimeout).Error; err != nil {
		t.Fatal(err)
	}
	if busyTimeout <= 0 {
		t.Fatalf("configured SQLite busy timeout=%d, want positive", busyTimeout)
	}

	now := time.Date(2026, 7, 26, 20, 0, 0, 0, time.UTC)
	jobID := strings.Repeat("1", 32)
	itemID := strings.Repeat("2", 32)
	recoveryPointID := strings.Repeat("3", 32)
	leaseID := strings.Repeat("4", 32)
	leaseAttemptID := strings.Repeat("5", 32)
	sourceID := strings.Repeat("6", 32)
	leaseFence := "claim-contention-source-fence"
	fenceDigest := sha256.Sum256([]byte(leaseFence))
	absoluteDeadline := now.Add(2 * time.Hour)
	if err := db.Create(&model.BackupAssetExportJob{
		ID: jobID, OwnerUserID: 1, SelectionDigest: strings.Repeat("a", 64), SelectionSchemaVersion: 1,
		ArchiveFormat: string(ArchiveZIP), ArchiveProfile: ArchiveProfileZIPDeflateV1, LimitsSchemaVersion: 1,
		ChunkBytes: 65536, MaxItems: 1, MaxSourcePoints: 1, MaxItemBytes: 1, MaxLogicalBytes: 1,
		MaxProviderBytes: 1, MaxCiphertextBytes: 1024, MaxOpenReaders: 1, MaxDurationSeconds: 3600,
		MaxAttempts: 3, RetryBaseSeconds: 1, RetryMaxDelaySeconds: 1, LeaseTTLSeconds: 900,
		LeaseRenewMarginSeconds: 300, ReadyTTLSeconds: 3600, ExecutionState: string(ExecutionQueued),
		CleanupState: "active", AbsoluteDeadline: absoluteDeadline, ItemCount: 1, TransitionRevision: 1,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetExportItem{
		ID: itemID, JobID: jobID, Ordinal: 0, RecoveryPointID: recoveryPointID,
		EntryID: strings.Repeat("b", 64), CatalogGenerationID: strings.Repeat("c", 32),
		SourceFingerprint: "claim-contention-source", EntryFingerprint: "claim-contention-entry",
		FingerprintStrength: "strong", ProviderCapabilityRevision: 1, EntryType: string(backupasset.CatalogEntryFile),
		LogicalSize: 1, PathNonce: []byte{1}, PathCiphertext: []byte{2}, State: string(ItemPending),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.RecoveryPointLease{
		ID: leaseID, RecoveryPointID: recoveryPointID, HolderType: string(backupasset.LeaseHolderExportJob),
		OwnerID: jobID, AttemptID: leaseAttemptID, FenceToken: leaseFence, Status: string(backupasset.LeaseActive),
		LeaseExpiresAt: now.Add(time.Hour), AbsoluteDeadline: absoluteDeadline, LastHeartbeatAt: now,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetExportSourceLease{
		ID: sourceID, JobID: jobID, RecoveryPointID: recoveryPointID, LeaseID: leaseID,
		LeaseAttemptID: leaseAttemptID, FenceHash: hex.EncodeToString(fenceDigest[:]),
		AbsoluteDeadline: absoluteDeadline, State: "active", AcquiredAt: now, RenewedAt: now,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewAttemptCoordinator(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	locker, err := database.Open(configpkg.Config{DBType: "sqlite", SQLitePath: path})
	if err != nil {
		t.Fatal(err)
	}
	lockerDB, err := locker.DB()
	if err != nil {
		t.Fatal(err)
	}
	lockerDB.SetMaxOpenConns(1)
	holder, err := lockerDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	releaseHolder := func() {
		releaseOnce.Do(func() {
			if _, err := holder.ExecContext(context.Background(), "ROLLBACK"); err != nil {
				t.Errorf("release SQLite Claim contention holder: %v", err)
			}
			if err := holder.Close(); err != nil {
				t.Errorf("close SQLite Claim contention holder: %v", err)
			}
		})
	}
	t.Cleanup(func() {
		releaseHolder()
		if err := lockerDB.Close(); err != nil {
			t.Errorf("close SQLite Claim contention locker: %v", err)
		}
		if err := primary.Close(); err != nil {
			t.Errorf("close SQLite Claim contention database: %v", err)
		}
	})
	return sqliteClaimContentionFixture{
		db: db, primary: primary, coordinator: coordinator,
		request: AttemptClaimRequest{JobID: jobID, WorkerOwner: "sqlite-claim-contention"},
		jobID:   jobID, itemID: itemID, busyTimeout: busyTimeout, releaseHolder: releaseHolder,
	}
}

func (fixture sqliteClaimContentionFixture) release() {
	fixture.releaseHolder()
}

func (fixture sqliteClaimContentionFixture) waitForClaimConnection(t *testing.T) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for fixture.primary.Stats().InUse != 1 {
		select {
		case <-deadline.C:
			t.Fatal("SQLite Claim did not acquire the primary connection")
		default:
			runtime.Gosched()
		}
	}
}

func (fixture sqliteClaimContentionFixture) assertNoClaimState(t *testing.T) {
	t.Helper()
	var job model.BackupAssetExportJob
	if err := fixture.db.First(&job, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(ExecutionQueued) || job.CurrentAttemptID != nil || job.CurrentFenceRevision != 0 ||
		job.TransitionRevision != 1 || job.PackedCount != 0 || job.SkippedCount != 0 || job.FailedCount != 0 ||
		job.LogicalBytes != 0 || job.ProviderBytes != 0 {
		t.Fatalf("busy Claim mutated job=%+v", job)
	}
	var item model.BackupAssetExportItem
	if err := fixture.db.First(&item, "id = ?", fixture.itemID).Error; err != nil {
		t.Fatal(err)
	}
	if item.State != string(ItemPending) || item.CurrentAttemptID != nil || item.LogicalBytes != 0 ||
		item.ProviderBytes != 0 || item.ErrorCategory != "" {
		t.Fatalf("busy Claim mutated item=%+v", item)
	}
	fixture.assertNoClaimArtifacts(t)
}

func (fixture sqliteClaimContentionFixture) assertNoClaimArtifacts(t *testing.T) {
	t.Helper()
	var attemptCount, itemAttemptCount int64
	if err := fixture.db.Model(&model.BackupAssetExportAttempt{}).Where("job_id = ?", fixture.jobID).Count(&attemptCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetExportItemAttempt{}).Where("job_id = ?", fixture.jobID).Count(&itemAttemptCount).Error; err != nil {
		t.Fatal(err)
	}
	if attemptCount != 0 || itemAttemptCount != 0 {
		t.Fatalf("busy Claim created attempts=%d item_attempts=%d", attemptCount, itemAttemptCount)
	}
}

func (fixture sqliteClaimContentionFixture) assertClaimState(t *testing.T, claim AttemptClaim) {
	t.Helper()
	if claim.AttemptID == "" || claim.AttemptNumber != 1 || len(claim.FenceToken) != 32 ||
		len(claim.NoncePrefix) != 8 || claim.SupersededAttemptID != "" {
		t.Fatalf("SQLite busy claim=%+v", claim)
	}
	var job model.BackupAssetExportJob
	if err := fixture.db.First(&job, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(ExecutionRunning) || job.CurrentAttemptID == nil || *job.CurrentAttemptID != claim.AttemptID ||
		job.CurrentFenceRevision != 1 || job.TransitionRevision != 2 {
		t.Fatalf("SQLite busy claimed job=%+v claim=%+v", job, claim)
	}
	var item model.BackupAssetExportItem
	if err := fixture.db.First(&item, "id = ?", fixture.itemID).Error; err != nil {
		t.Fatal(err)
	}
	if item.State != string(ItemPending) || item.CurrentAttemptID == nil || *item.CurrentAttemptID != claim.AttemptID {
		t.Fatalf("SQLite busy claimed item=%+v claim=%+v", item, claim)
	}
	var attempts []model.BackupAssetExportAttempt
	if err := fixture.db.Where("job_id = ?", fixture.jobID).Order("attempt_number ASC").Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].ID != claim.AttemptID || attempts[0].AttemptNumber != 1 ||
		attempts[0].State != string(AttemptActive) || !attempts[0].IsCurrent {
		t.Fatalf("SQLite busy claimed attempts=%+v claim=%+v", attempts, claim)
	}
	var itemAttempts []model.BackupAssetExportItemAttempt
	if err := fixture.db.Where("job_id = ? AND attempt_id = ?", fixture.jobID, claim.AttemptID).Find(&itemAttempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(itemAttempts) != 1 || itemAttempts[0].ItemID != fixture.itemID || itemAttempts[0].State != string(ItemPending) {
		t.Fatalf("SQLite busy claimed item attempts=%+v claim=%+v", itemAttempts, claim)
	}
}

func (fixture sqliteClaimContentionFixture) assertBusyTimeoutRestored(t *testing.T) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for fixture.primary.Stats().InUse != 0 {
		select {
		case <-deadline.C:
			t.Fatal("SQLite Claim leaked its primary connection")
		default:
			runtime.Gosched()
		}
	}
	var busyTimeout int64
	if err := fixture.db.Raw("PRAGMA busy_timeout").Scan(&busyTimeout).Error; err != nil {
		t.Fatal(err)
	}
	if busyTimeout != fixture.busyTimeout {
		t.Fatalf("SQLite Claim busy timeout=%d, want restored %d", busyTimeout, fixture.busyTimeout)
	}
}

func TestValidPersistedTerminalSpoolAcceptsOnlyClosedEvidenceStates(t *testing.T) {
	validDigest := strings.Repeat("a", 64)
	validLocator := strings.Repeat("b", 32) + ".xrs"
	job := model.BackupAssetExportJob{MaxCiphertextBytes: 128}
	tests := []struct {
		name string
		row  model.BackupAssetExportItemAttempt
		want bool
	}{
		{name: "never spooled", want: true},
		{name: "live spool", row: model.BackupAssetExportItemAttempt{SpoolDigest: validDigest, SpoolSize: 64, SpoolLocator: validLocator}, want: true},
		{name: "retired spool evidence", row: model.BackupAssetExportItemAttempt{SpoolDigest: validDigest, SpoolSize: 64}, want: true},
		{name: "locator only", row: model.BackupAssetExportItemAttempt{SpoolLocator: validLocator}},
		{name: "digest only", row: model.BackupAssetExportItemAttempt{SpoolDigest: validDigest}},
		{name: "size only", row: model.BackupAssetExportItemAttempt{SpoolSize: 64}},
		{name: "digest without size and live locator", row: model.BackupAssetExportItemAttempt{SpoolDigest: validDigest, SpoolLocator: validLocator}},
		{name: "size without digest and live locator", row: model.BackupAssetExportItemAttempt{SpoolSize: 64, SpoolLocator: validLocator}},
		{name: "invalid retired digest", row: model.BackupAssetExportItemAttempt{SpoolDigest: strings.Repeat("g", 64), SpoolSize: 64}},
		{name: "negative retired size", row: model.BackupAssetExportItemAttempt{SpoolDigest: validDigest, SpoolSize: -1}},
		{name: "oversized retired evidence", row: model.BackupAssetExportItemAttempt{SpoolDigest: validDigest, SpoolSize: 129}},
		{name: "wrong live locator type", row: model.BackupAssetExportItemAttempt{SpoolDigest: validDigest, SpoolSize: 64, SpoolLocator: strings.Repeat("b", 32) + ".xre"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validPersistedTerminalSpool(test.row, job); got != test.want {
				t.Fatalf("validPersistedTerminalSpool(%+v)=%t, want %t", test.row, got, test.want)
			}
		})
	}
}

func TestNewPreHeaderSpoolFailureDoesNotAuthorizeAttemptFatalCauses(t *testing.T) {
	for _, test := range []struct {
		name  string
		cause error
	}{
		{name: "context canceled", cause: context.Canceled},
		{name: "context deadline", cause: context.DeadlineExceeded},
		{name: "Export fence", cause: ErrAttemptFenceLost},
		{name: "Export quota", cause: ErrQuotaExceeded},
		{name: "raw Foundation fence", cause: backupasset.ErrLeaseFenceLost},
		{name: "wrapped Foundation fence", cause: fmt.Errorf("wrapped Foundation fence: %w", backupasset.ErrLeaseFenceLost)},
		{name: "raw Foundation deadline", cause: backupasset.ErrLeaseDeadlineExceeded},
		{name: "wrapped Foundation deadline", cause: fmt.Errorf("wrapped Foundation deadline: %w", backupasset.ErrLeaseDeadlineExceeded)},
	} {
		t.Run(test.name, func(t *testing.T) {
			failure := NewPreHeaderSpoolFailure(test.cause)
			var marker *PreHeaderSpoolFailure
			if errors.As(failure, &marker) {
				t.Fatalf("attempt-fatal cause %v was marked recoverable: %v", test.cause, failure)
			}
			if !errors.Is(failure, test.cause) {
				t.Fatalf("attempt-fatal cause %v was not preserved: %v", test.cause, failure)
			}
		})
	}
}

func TestPreHeaderSpoolFailureAfterAuthenticationAcceptsOnlySafePurgedAbsence(t *testing.T) {
	itemID := strings.Repeat("a", 32)
	for _, test := range []struct {
		name       string
		cause      error
		wantMarker bool
	}{
		{name: "ciphertext tamper", cause: ErrCipherTampered, wantMarker: true},
		{name: "authoritative absence", cause: errors.Join(ErrStoreObjectAbsent, os.ErrNotExist), wantMarker: true},
		{name: "absence with invalid store", cause: errors.Join(ErrStoreObjectAbsent, os.ErrNotExist, ErrInvalidStore)},
		{name: "unsafe store object", cause: errors.Join(ErrStoreObjectAbsent, os.ErrNotExist, ErrStoreObjectUnsafe)},
		{name: "absence with cancellation", cause: errors.Join(ErrStoreObjectAbsent, os.ErrNotExist, context.Canceled)},
		{name: "absence with Export fence", cause: errors.Join(ErrStoreObjectAbsent, os.ErrNotExist, ErrAttemptFenceLost)},
		{name: "absence with Foundation deadline", cause: errors.Join(ErrStoreObjectAbsent, os.ErrNotExist, backupasset.ErrLeaseDeadlineExceeded)},
		{name: "ordinary local error", cause: errors.New("ordinary local failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			failure := newPreHeaderSpoolFailureAfterAuthentication(test.cause, itemID, 37)
			var marker *PreHeaderSpoolFailure
			if got := errors.As(failure, &marker); got != test.wantMarker {
				t.Fatalf("marker=%t, want %t for cause=%v", got, test.wantMarker, test.cause)
			}
			if !errors.Is(failure, test.cause) {
				t.Fatalf("cause was not preserved: %v", failure)
			}
			if marker != nil && (marker.ItemID() != itemID || marker.ProviderBytes() != 37) {
				t.Fatalf("marker=%+v, want item=%s provider_bytes=37", marker, itemID)
			}
		})
	}
}

func TestAttemptCoordinatorPersistsCheckpointAndRejectsOldFenceAfterTakeover(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 91, Role: "admin"}, Selection: selection,
		IdempotencyKey: "attempt-coordinator-create-1", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	var source model.BackupAssetExportSourceLease
	if err := harness.db.Where("job_id = ?", created.JobID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	clock := source.RenewedAt.UTC()
	sourceLeases, err := backupasset.NewLeaseService(harness.db, func() time.Time { return clock }, backupasset.LeaseConfig{
		Duration: harness.config.LeaseTTL, Heartbeat: harness.config.LeaseRenewMargin, AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock }, sourceLeases)
	if err != nil {
		t.Fatal(err)
	}
	first, err := coordinator.Claim(context.Background(), AttemptClaimRequest{
		JobID: created.JobID, WorkerOwner: "worker-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.AttemptNumber != 1 || len(first.FenceToken) != 32 || len(first.NoncePrefix) != 8 {
		t.Fatalf("first claim=%+v", first)
	}
	if first.SupersededAttemptID != "" {
		t.Fatalf("first claim superseded attempt=%q, want empty", first.SupersededAttemptID)
	}
	var item model.BackupAssetExportItem
	if err := harness.db.Where("job_id = ?", created.JobID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	var itemAttempt model.BackupAssetExportItemAttempt
	if err := harness.db.Where("job_id = ? AND attempt_id = ? AND item_id = ?", created.JobID, first.AttemptID, item.ID).
		Take(&itemAttempt).Error; err != nil {
		t.Fatal(err)
	}
	result := harness.db.Model(&model.BackupAssetExportItemAttempt{}).
		Where("id = ? AND state = ?", itemAttempt.ID, ItemPending).
		Updates(map[string]any{
			"state": string(ItemRead), "spool_digest": strings.Repeat("a", 64), "spool_size": int64(1),
			"spool_locator": strings.Repeat("b", 32) + ".xrs", "logical_bytes": item.LogicalSize, "read_at": clock,
		})
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("prepare read spool checkpoint error=%v rows=%d", result.Error, result.RowsAffected)
	}
	result = harness.db.Model(&model.BackupAssetExportItem{}).
		Where("id = ? AND current_attempt_id = ? AND state = ?", item.ID, first.AttemptID, ItemPending).
		Updates(map[string]any{
			"state": string(ItemRead), "logical_bytes": item.LogicalSize, "provider_bytes": itemAttempt.ProviderBytes, "updated_at": clock,
		})
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("prepare read item projection error=%v rows=%d", result.Error, result.RowsAffected)
	}
	if err := coordinator.Checkpoint(context.Background(), AttemptCheckpoint{
		JobID: created.JobID, AttemptID: first.AttemptID, FenceToken: first.FenceToken,
		ItemID: item.ID, State: ItemPacked, LogicalBytes: item.LogicalSize, ProviderBytes: itemAttempt.ProviderBytes,
	}); err != nil {
		t.Fatal(err)
	}

	clock = first.LeaseExpiresAt.Add(time.Second)
	if _, err := coordinator.TakeoverSourceLeases(context.Background(), SourceLeaseTakeoverRequest{JobID: created.JobID}); err != nil {
		t.Fatal(err)
	}
	second, err := coordinator.Claim(context.Background(), AttemptClaimRequest{
		JobID: created.JobID, WorkerOwner: "worker-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.AttemptNumber != 2 || second.AttemptID == first.AttemptID || second.SupersededAttemptID != first.AttemptID {
		t.Fatalf("takeover claim=%+v", second)
	}

	var job model.BackupAssetExportJob
	if err := harness.db.First(&job, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.CurrentAttemptID == nil || *job.CurrentAttemptID != second.AttemptID || job.PackedCount != 0 ||
		job.SkippedCount != 0 || job.FailedCount != 0 || job.LogicalBytes != 0 || job.ProviderBytes != 0 {
		t.Fatalf("job was not reset on takeover: %+v", job)
	}
	if err := harness.db.First(&item, "id = ?", item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item.State != string(ItemPending) || item.CurrentAttemptID == nil || *item.CurrentAttemptID != second.AttemptID ||
		item.LogicalBytes != 0 || item.ProviderBytes != 0 || item.ErrorCategory != "" {
		t.Fatalf("item was not reset on takeover: %+v", item)
	}
	var history []model.BackupAssetExportItemAttempt
	if err := harness.db.Where("job_id = ? AND item_id = ?", created.JobID, item.ID).Order("created_at ASC").Find(&history).Error; err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].State != string(ItemPacked) || history[1].State != string(ItemPending) {
		t.Fatalf("immutable attempt history=%+v", history)
	}
	if err := coordinator.Checkpoint(context.Background(), AttemptCheckpoint{
		JobID: created.JobID, AttemptID: first.AttemptID, FenceToken: first.FenceToken,
		ItemID: item.ID, State: ItemFailed, ErrorCategory: "late",
	}); !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("late checkpoint error=%v", err)
	}
}

func TestAttemptCoordinatorRejectsTerminalCheckpointsThatWouldBreakPersistentReload(t *testing.T) {
	t.Run("regular file requires read spool", func(t *testing.T) {
		harness := newWorkerServiceHarness(t)
		created, claim, itemAttempt := createClaimedExportForAttemptBudget(
			t, harness, 92, "attempt-coordinator-packed-without-read-spool",
		)
		coordinator, err := NewAttemptCoordinator(harness.db, harness.service.now)
		if err != nil {
			t.Fatal(err)
		}
		var item model.BackupAssetExportItem
		if err := harness.db.First(&item, "id = ?", itemAttempt.ItemID).Error; err != nil {
			t.Fatal(err)
		}
		var beforeAttempt model.BackupAssetExportItemAttempt
		if err := harness.db.First(&beforeAttempt, "id = ?", itemAttempt.ID).Error; err != nil {
			t.Fatal(err)
		}
		var beforeJob model.BackupAssetExportJob
		if err := harness.db.First(&beforeJob, "id = ?", created.JobID).Error; err != nil {
			t.Fatal(err)
		}

		err = coordinator.Checkpoint(context.Background(), AttemptCheckpoint{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
			ItemID: item.ID, State: ItemPacked, LogicalBytes: item.LogicalSize, ProviderBytes: beforeAttempt.ProviderBytes,
		})
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("packed without read spool error=%v, want ErrInvalidTransition", err)
		}

		var afterAttempt model.BackupAssetExportItemAttempt
		if err := harness.db.First(&afterAttempt, "id = ?", beforeAttempt.ID).Error; err != nil {
			t.Fatal(err)
		}
		var afterItem model.BackupAssetExportItem
		if err := harness.db.First(&afterItem, "id = ?", item.ID).Error; err != nil {
			t.Fatal(err)
		}
		var afterJob model.BackupAssetExportJob
		if err := harness.db.First(&afterJob, "id = ?", created.JobID).Error; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(afterAttempt, beforeAttempt) || !reflect.DeepEqual(afterItem, item) || !reflect.DeepEqual(afterJob, beforeJob) {
			t.Fatalf("rejected packed checkpoint mutated rows: attempt=%+v item=%+v job=%+v", afterAttempt, afterItem, afterJob)
		}
	})

	t.Run("non file cannot use pre-header failure", func(t *testing.T) {
		harness := newWorkerServiceHarness(t)
		directory := frozenItemFixture()
		directory.Ref.EntryID = strings.Repeat("d", 64)
		directory.EntryFingerprint = "directory-entry-fingerprint-v1"
		directory.EntryType = backupasset.CatalogEntryDirectory
		directory.LogicalSize = 0
		directory.MediaType = ""
		directory.ArchiveComponents = []string{"root", "directory"}
		selection, err := FreezeSelection([]FrozenItem{directory}, nil, harness.config.Selection)
		if err != nil {
			t.Fatal(err)
		}
		created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
			Actor: SelectionActor{UserID: 92, Role: "admin"}, Selection: selection,
			IdempotencyKey: "attempt-coordinator-directory-pre-header-failure", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
		})
		if err != nil {
			t.Fatal(err)
		}
		coordinator, err := NewAttemptCoordinator(harness.db, harness.service.now)
		if err != nil {
			t.Fatal(err)
		}
		claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "directory-pre-header-failure"})
		if err != nil {
			t.Fatal(err)
		}
		var item model.BackupAssetExportItem
		if err := harness.db.Where("job_id = ?", created.JobID).Take(&item).Error; err != nil {
			t.Fatal(err)
		}
		var beforeAttempt model.BackupAssetExportItemAttempt
		if err := harness.db.Where("job_id = ? AND attempt_id = ? AND item_id = ?", created.JobID, claim.AttemptID, item.ID).
			Take(&beforeAttempt).Error; err != nil {
			t.Fatal(err)
		}
		var beforeJob model.BackupAssetExportJob
		if err := harness.db.First(&beforeJob, "id = ?", created.JobID).Error; err != nil {
			t.Fatal(err)
		}

		err = coordinator.Checkpoint(context.Background(), AttemptCheckpoint{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
			ItemID: item.ID, State: ItemFailed, ProviderBytes: beforeAttempt.ProviderBytes, ErrorCategory: "source_changed",
		})
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("non-file pre-header failure error=%v, want ErrInvalidTransition", err)
		}

		var afterAttempt model.BackupAssetExportItemAttempt
		if err := harness.db.First(&afterAttempt, "id = ?", beforeAttempt.ID).Error; err != nil {
			t.Fatal(err)
		}
		var afterItem model.BackupAssetExportItem
		if err := harness.db.First(&afterItem, "id = ?", item.ID).Error; err != nil {
			t.Fatal(err)
		}
		var afterJob model.BackupAssetExportJob
		if err := harness.db.First(&afterJob, "id = ?", created.JobID).Error; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(afterAttempt, beforeAttempt) || !reflect.DeepEqual(afterItem, item) || !reflect.DeepEqual(afterJob, beforeJob) {
			t.Fatalf("rejected non-file failure mutated rows: attempt=%+v item=%+v job=%+v", afterAttempt, afterItem, afterJob)
		}
	})
}

func TestAttemptCoordinatorCheckpointPreservesItemAttemptDatabaseFailure(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	created, claim, itemAttempt := createClaimedExportForAttemptBudget(
		t, harness, 92, "attempt-coordinator-item-attempt-update-db-error",
	)
	coordinator, err := NewAttemptCoordinator(harness.db, harness.service.now)
	if err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetExportItem
	if err := harness.db.First(&item, "id = ?", itemAttempt.ItemID).Error; err != nil {
		t.Fatal(err)
	}
	var beforeAttempt model.BackupAssetExportItemAttempt
	if err := harness.db.First(&beforeAttempt, "id = ?", itemAttempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	var beforeJob model.BackupAssetExportJob
	if err := harness.db.First(&beforeJob, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected item-attempt checkpoint update failure")
	callbackName := "test:attempt-coordinator-item-attempt-checkpoint-db-error-" + strings.ReplaceAll(t.Name(), "/", "_")
	if err := harness.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Schema != nil && tx.Statement.Schema.Table == "backup_asset_export_item_attempts" {
			_ = tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if removeErr := harness.db.Callback().Update().Remove(callbackName); removeErr != nil {
			t.Errorf("remove item-attempt checkpoint callback: %v", removeErr)
		}
	})

	err = coordinator.Checkpoint(context.Background(), AttemptCheckpoint{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
		ItemID: item.ID, State: ItemSkipped, ProviderBytes: beforeAttempt.ProviderBytes, ErrorCategory: ItemErrorLinkMetadataUnavailable,
	})
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, injected) {
		t.Fatalf("item-attempt update error=%v, want joined ErrUnavailable and injected cause", err)
	}

	var afterAttempt model.BackupAssetExportItemAttempt
	if err := harness.db.First(&afterAttempt, "id = ?", beforeAttempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	var afterItem model.BackupAssetExportItem
	if err := harness.db.First(&afterItem, "id = ?", item.ID).Error; err != nil {
		t.Fatal(err)
	}
	var afterJob model.BackupAssetExportJob
	if err := harness.db.First(&afterJob, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterAttempt, beforeAttempt) || !reflect.DeepEqual(afterItem, item) || !reflect.DeepEqual(afterJob, beforeJob) {
		t.Fatalf("database-error checkpoint mutated rows: attempt=%+v item=%+v job=%+v", afterAttempt, afterItem, afterJob)
	}
}

func TestAttemptCoordinatorRejectsInvalidFailedCheckpointBeforePersistence(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 92, Role: "admin"}, Selection: selection,
		IdempotencyKey: "attempt-coordinator-invalid-failed-checkpoint", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewAttemptCoordinator(harness.db, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "invalid-failed-checkpoint"})
	if err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetExportItem
	if err := harness.db.Where("job_id = ?", created.JobID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	for _, checkpoint := range []AttemptCheckpoint{
		{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: item.ID,
			State: ItemFailed, ErrorCategory: "archive_failed",
		},
		{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: item.ID,
			State: ItemFailed, LogicalBytes: 1, ErrorCategory: "source_changed",
		},
	} {
		if err := coordinator.Checkpoint(context.Background(), checkpoint); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("checkpoint=%+v error=%v, want invalid transition", checkpoint, err)
		}
	}
	var persisted model.BackupAssetExportItemAttempt
	if err := harness.db.Where("job_id = ? AND attempt_id = ? AND item_id = ?", created.JobID, claim.AttemptID, item.ID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ItemPending) || persisted.LogicalBytes != 0 || persisted.ErrorCategory != "" {
		t.Fatalf("invalid checkpoint persisted state=%+v", persisted)
	}
}

func TestAttemptCoordinatorRequiresRecoveredSpoolConfirmationForFailedCheckpoint(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 92, Role: "admin"}, Selection: selection,
		IdempotencyKey: "attempt-coordinator-failed-checkpoint-after-spool", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewAttemptCoordinator(harness.db, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "failed-checkpoint-after-spool"})
	if err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetExportItem
	if err := harness.db.Where("job_id = ?", created.JobID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	var itemAttempt model.BackupAssetExportItemAttempt
	if err := harness.db.Where("job_id = ? AND attempt_id = ? AND item_id = ?", created.JobID, claim.AttemptID, item.ID).Take(&itemAttempt).Error; err != nil {
		t.Fatal(err)
	}
	itemAttemptID := itemAttempt.ID
	now := time.Now().UTC()
	result := harness.db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", itemAttempt.ID).Updates(map[string]any{
		"state": string(ItemRead), "spool_digest": strings.Repeat("a", 64), "spool_size": int64(1),
		"spool_locator": strings.Repeat("b", 32) + ".xrs", "logical_bytes": item.LogicalSize, "read_at": now,
	})
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("prepare persisted spool error=%v rows=%d", result.Error, result.RowsAffected)
	}
	result = harness.db.Model(&model.BackupAssetExportItem{}).Where("id = ?", item.ID).Updates(map[string]any{
		"state": string(ItemRead), "logical_bytes": item.LogicalSize,
	})
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("prepare item read projection error=%v rows=%d", result.Error, result.RowsAffected)
	}
	if err := coordinator.Checkpoint(context.Background(), AttemptCheckpoint{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: item.ID,
		State: ItemFailed, ErrorCategory: "source_changed",
	}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("post-spool failed checkpoint error=%v, want invalid transition", err)
	}
	itemAttempt = model.BackupAssetExportItemAttempt{}
	if err := harness.db.First(&itemAttempt, "id = ?", itemAttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if itemAttempt.State != string(ItemRead) || itemAttempt.SpoolLocator == "" || itemAttempt.ErrorCategory != "" {
		t.Fatalf("post-spool checkpoint mutated durable attempt=%+v", itemAttempt)
	}
	if err := coordinator.Checkpoint(context.Background(), AttemptCheckpoint{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: item.ID,
		State: ItemFailed, ErrorCategory: "internal_failure", PreHeaderSpoolRecovered: true,
	}); err != nil {
		t.Fatalf("confirmed recovered spool checkpoint error=%v", err)
	}
	itemAttempt = model.BackupAssetExportItemAttempt{}
	if err := harness.db.First(&itemAttempt, "id = ?", itemAttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if itemAttempt.State != string(ItemFailed) || itemAttempt.SpoolDigest != "" || itemAttempt.SpoolSize != 0 ||
		itemAttempt.SpoolLocator != "" || itemAttempt.LogicalBytes != 0 || itemAttempt.ReadAt != nil ||
		itemAttempt.PackedAt != nil || itemAttempt.FinishedAt == nil || itemAttempt.ErrorCategory != "internal_failure" {
		t.Fatalf("confirmed recovery did not clear read spool=%+v", itemAttempt)
	}
}

func TestAttemptCoordinatorRejectsPreHeaderFailureAfterPendingEvidenceChanges(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	created, claim, itemAttempt := createClaimedExportForAttemptBudget(
		t, harness, 92, "attempt-coordinator-pre-header-evidence-cas",
	)
	now := harness.service.now().UTC()
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetExportItem
	if err := harness.db.First(&item, "id = ?", itemAttempt.ItemID).Error; err != nil {
		t.Fatal(err)
	}
	if itemAttempt.State != string(ItemPending) || itemAttempt.SpoolDigest != "" || itemAttempt.SpoolSize != 0 ||
		itemAttempt.SpoolLocator != "" || itemAttempt.LogicalBytes != 0 || itemAttempt.ErrorCategory != "" ||
		itemAttempt.ReadAt != nil || itemAttempt.PackedAt != nil || itemAttempt.FinishedAt != nil {
		t.Fatalf("precheck fixture is not a clean pending attempt: %+v", itemAttempt)
	}

	readAt := now
	result := harness.db.Model(&model.BackupAssetExportItemAttempt{}).
		Where("id = ? AND state = ?", itemAttempt.ID, ItemPending).
		Updates(map[string]any{
			"spool_digest": strings.Repeat("a", 64), "spool_size": int64(96),
			"spool_locator": strings.Repeat("b", 32) + ".xrs", "read_at": readAt,
		})
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("persist post-precheck spool evidence error=%v rows=%d", result.Error, result.RowsAffected)
	}

	err = coordinator.Checkpoint(context.Background(), AttemptCheckpoint{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: item.ID,
		State: ItemFailed, ProviderBytes: itemAttempt.ProviderBytes, ErrorCategory: "source_changed",
	})
	if !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("post-precheck evidence checkpoint error=%v, want ErrAttemptFenceLost", err)
	}

	var persistedAttempt model.BackupAssetExportItemAttempt
	if err := harness.db.First(&persistedAttempt, "id = ?", itemAttempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persistedAttempt.State != string(ItemPending) || persistedAttempt.SpoolDigest == "" || persistedAttempt.SpoolSize != 96 ||
		persistedAttempt.SpoolLocator == "" || persistedAttempt.ReadAt == nil || persistedAttempt.FinishedAt != nil ||
		persistedAttempt.ErrorCategory != "" {
		t.Fatalf("checkpoint mutated post-precheck evidence=%+v", persistedAttempt)
	}
	if err := harness.db.First(&item, "id = ?", item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item.State != string(ItemPending) || item.LogicalBytes != 0 || item.ProviderBytes != 0 || item.ErrorCategory != "" {
		t.Fatalf("checkpoint mutated current item projection=%+v", item)
	}
	var job model.BackupAssetExportJob
	if err := harness.db.First(&job, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.PackedCount != 0 || job.SkippedCount != 0 || job.FailedCount != 0 ||
		job.LogicalBytes != 0 || job.ProviderBytes != 0 {
		t.Fatalf("checkpoint mutated current job aggregates=%+v", job)
	}
}

func TestAttemptCoordinatorTakeoverSourceLeasesAllowsQueuedJob(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 94, Role: "admin"}, Selection: selection,
		IdempotencyKey: "attempt-coordinator-queued-source-takeover", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	var jobBefore model.BackupAssetExportJob
	var itemBefore model.BackupAssetExportItem
	var sourceBefore model.BackupAssetExportSourceLease
	var leaseBefore model.RecoveryPointLease
	if err := harness.db.First(&jobBefore, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Where("job_id = ?", created.JobID).Take(&itemBefore).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Where("job_id = ?", created.JobID).Take(&sourceBefore).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&leaseBefore, "id = ?", sourceBefore.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	if jobBefore.ExecutionState != string(ExecutionQueued) || jobBefore.CurrentAttemptID != nil ||
		itemBefore.CurrentAttemptID != nil || itemBefore.State != string(ItemPending) {
		t.Fatalf("queued takeover fixture unexpectedly has an attempt: job=%+v item=%+v", jobBefore, itemBefore)
	}

	clock := leaseBefore.LeaseExpiresAt.UTC().Add(time.Second)
	sourceLeases, err := backupasset.NewLeaseService(harness.db, func() time.Time { return clock }, backupasset.LeaseConfig{
		Duration: harness.config.LeaseTTL, Heartbeat: harness.config.LeaseRenewMargin, AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock }, sourceLeases)
	if err != nil {
		t.Fatal(err)
	}
	taken, err := coordinator.TakeoverSourceLeases(context.Background(), SourceLeaseTakeoverRequest{JobID: created.JobID})
	if err != nil || len(taken.LeaseExpiresAt) != 1 || !taken.AbsoluteDeadline.Equal(sourceBefore.AbsoluteDeadline) {
		t.Fatalf("queued source takeover=%+v err=%v", taken, err)
	}

	var sourceAfter model.BackupAssetExportSourceLease
	var leaseAfter model.RecoveryPointLease
	var jobAfter model.BackupAssetExportJob
	var itemAfter model.BackupAssetExportItem
	if err := harness.db.First(&sourceAfter, "id = ?", sourceBefore.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&leaseAfter, "id = ?", sourceBefore.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&jobAfter, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&itemAfter, "id = ?", itemBefore.ID).Error; err != nil {
		t.Fatal(err)
	}
	fenceDigest := sha256.Sum256([]byte(leaseAfter.FenceToken))
	if sourceAfter.LeaseAttemptID == sourceBefore.LeaseAttemptID || sourceAfter.LeaseAttemptID != leaseAfter.AttemptID ||
		sourceAfter.FenceHash == sourceBefore.FenceHash || sourceAfter.FenceHash != hex.EncodeToString(fenceDigest[:]) ||
		!sourceAfter.AbsoluteDeadline.Equal(sourceBefore.AbsoluteDeadline) || !leaseAfter.AbsoluteDeadline.Equal(sourceBefore.AbsoluteDeadline) ||
		!sourceAfter.RenewedAt.Equal(clock) {
		t.Fatalf("queued source takeover drift: source_before=%+v source_after=%+v lease_before=%+v lease_after=%+v",
			sourceBefore, sourceAfter, leaseBefore, leaseAfter)
	}
	if jobAfter.ExecutionState != string(ExecutionQueued) || jobAfter.CurrentAttemptID != nil ||
		jobAfter.CurrentFenceRevision != jobBefore.CurrentFenceRevision || itemAfter.CurrentAttemptID != nil ||
		itemAfter.State != string(ItemPending) || itemAfter.LogicalBytes != 0 || itemAfter.ProviderBytes != 0 {
		t.Fatalf("queued source takeover changed attempt projections: job_before=%+v job_after=%+v item_before=%+v item_after=%+v",
			jobBefore, jobAfter, itemBefore, itemAfter)
	}
}

func TestAttemptCoordinatorTakeoverSourceLeasesPreservesDeadlineBeforeAttemptTakeover(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 95, Role: "admin"}, Selection: selection,
		IdempotencyKey: "attempt-coordinator-source-takeover", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	var sourceBefore model.BackupAssetExportSourceLease
	if err := harness.db.Where("job_id = ?", created.JobID).Take(&sourceBefore).Error; err != nil {
		t.Fatal(err)
	}
	var leaseBefore model.RecoveryPointLease
	if err := harness.db.First(&leaseBefore, "id = ?", sourceBefore.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	clock := sourceBefore.RenewedAt.UTC()
	sourceLeases, err := backupasset.NewLeaseService(harness.db, func() time.Time { return clock }, backupasset.LeaseConfig{
		Duration: harness.config.LeaseTTL, Heartbeat: harness.config.LeaseRenewMargin, AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock }, sourceLeases)
	if err != nil {
		t.Fatal(err)
	}
	first, err := coordinator.Claim(context.Background(), AttemptClaimRequest{
		JobID: created.JobID, WorkerOwner: "worker-source-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	var itemBefore model.BackupAssetExportItem
	if err := harness.db.Where("job_id = ?", created.JobID).Take(&itemBefore).Error; err != nil {
		t.Fatal(err)
	}

	clock = first.LeaseExpiresAt.Add(time.Second)
	taken, err := coordinator.TakeoverSourceLeases(context.Background(), SourceLeaseTakeoverRequest{JobID: created.JobID})
	if err != nil {
		t.Fatal(err)
	}
	if len(taken.LeaseExpiresAt) != 1 || !taken.AbsoluteDeadline.Equal(sourceBefore.AbsoluteDeadline) {
		t.Fatalf("source takeover=%+v deadline_before=%s", taken, sourceBefore.AbsoluteDeadline)
	}

	var sourceAfter model.BackupAssetExportSourceLease
	if err := harness.db.First(&sourceAfter, "id = ?", sourceBefore.ID).Error; err != nil {
		t.Fatal(err)
	}
	var leaseAfter model.RecoveryPointLease
	if err := harness.db.First(&leaseAfter, "id = ?", sourceBefore.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	fenceDigest := sha256.Sum256([]byte(leaseAfter.FenceToken))
	if sourceAfter.LeaseAttemptID == sourceBefore.LeaseAttemptID || sourceAfter.LeaseAttemptID != leaseAfter.AttemptID ||
		sourceAfter.FenceHash == sourceBefore.FenceHash || sourceAfter.FenceHash != hex.EncodeToString(fenceDigest[:]) ||
		!sourceAfter.AbsoluteDeadline.Equal(sourceBefore.AbsoluteDeadline) || !leaseAfter.AbsoluteDeadline.Equal(sourceBefore.AbsoluteDeadline) ||
		!sourceAfter.RenewedAt.Equal(clock) {
		t.Fatalf("source takeover drift: source_before=%+v source_after=%+v lease_before=%+v lease_after=%+v",
			sourceBefore, sourceAfter, leaseBefore, leaseAfter)
	}
	var jobBeforeAttemptTakeover model.BackupAssetExportJob
	if err := harness.db.First(&jobBeforeAttemptTakeover, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	var itemAfterSourceTakeover model.BackupAssetExportItem
	if err := harness.db.First(&itemAfterSourceTakeover, "id = ?", itemBefore.ID).Error; err != nil {
		t.Fatal(err)
	}
	if jobBeforeAttemptTakeover.CurrentAttemptID == nil || *jobBeforeAttemptTakeover.CurrentAttemptID != first.AttemptID ||
		itemAfterSourceTakeover.CurrentAttemptID == nil || *itemAfterSourceTakeover.CurrentAttemptID != first.AttemptID {
		t.Fatalf("source takeover changed attempt projection: job=%+v item=%+v", jobBeforeAttemptTakeover, itemAfterSourceTakeover)
	}

	second, err := coordinator.Claim(context.Background(), AttemptClaimRequest{
		JobID: created.JobID, WorkerOwner: "worker-source-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.AttemptNumber != 2 || second.AttemptID == first.AttemptID {
		t.Fatalf("attempt takeover=%+v", second)
	}
}

func TestAttemptCoordinatorTakeoverSourceLeasesOnlyReplacesExpiredOwners(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	firstItem := frozenItemFixture()
	secondItem := frozenItemFixture()
	secondItem.Ref.RecoveryPointID = strings.Repeat("d", 32)
	secondItem.Ref.EntryID = strings.Repeat("e", 64)
	secondItem.CatalogGenerationID = strings.Repeat("f", 32)
	secondItem.SourceFingerprint = "second-source-fingerprint-v1"
	secondItem.EntryFingerprint = "second-entry-fingerprint-v1"
	now := time.Now().UTC().Truncate(time.Second)
	if err := harness.db.Create(&model.RecoveryPoint{
		ID: secondItem.Ref.RecoveryPointID, RepositoryID: strings.Repeat("9", 32),
		State: string(backupasset.RecoveryPointCommitted), Semantics: string(backupasset.PointNativeSnapshot),
		SourceFingerprint: secondItem.SourceFingerprint, CapabilityRevision: int(secondItem.ProviderCapabilityRevision),
		PhysicalAvailability: string(backupasset.PhysicalOnline), ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
		HoldState: string(backupasset.HoldNone), RetentionUntil: secondItem.RetentionUntil, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	selection, err := FreezeSelection([]FrozenItem{firstItem, secondItem}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 96, Role: "admin"}, Selection: selection,
		IdempotencyKey: "attempt-source-partial-expiry", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := now
	sourceLeases, err := backupasset.NewLeaseService(harness.db, func() time.Time { return clock }, backupasset.LeaseConfig{
		Duration: harness.config.LeaseTTL, Heartbeat: harness.config.LeaseRenewMargin, AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock }, sourceLeases)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-multi-source"})
	if err != nil {
		t.Fatal(err)
	}
	var sources []model.BackupAssetExportSourceLease
	if err := harness.db.Where("job_id = ?", created.JobID).Order("recovery_point_id ASC").Find(&sources).Error; err != nil || len(sources) != 2 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	clock = claim.LeaseExpiresAt.Add(time.Second)
	activeExpiry := clock.Add(time.Minute)
	if err := harness.db.Model(&model.RecoveryPointLease{}).Where("id = ?", sources[0].LeaseID).
		Update("lease_expires_at", activeExpiry).Error; err != nil {
		t.Fatal(err)
	}
	expiredAt := clock.Add(-time.Second)
	if err := harness.db.Model(&model.RecoveryPointLease{}).Where("id = ?", sources[1].LeaseID).
		Update("lease_expires_at", expiredAt).Error; err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.TakeoverSourceLeases(context.Background(), SourceLeaseTakeoverRequest{JobID: created.JobID})
	if err != nil || len(result.LeaseExpiresAt) != 2 {
		t.Fatalf("partial source takeover=%+v err=%v", result, err)
	}
	var after []model.BackupAssetExportSourceLease
	if err := harness.db.Where("job_id = ?", created.JobID).Order("recovery_point_id ASC").Find(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after[0].LeaseAttemptID != sources[0].LeaseAttemptID || after[0].FenceHash != sources[0].FenceHash {
		t.Fatalf("active source owner changed: before=%+v after=%+v", sources[0], after[0])
	}
	if after[1].LeaseAttemptID == sources[1].LeaseAttemptID || after[1].FenceHash == sources[1].FenceHash {
		t.Fatalf("expired source owner not replaced: before=%+v after=%+v", sources[1], after[1])
	}
}

func TestAttemptCoordinatorFailsBeforeClaimWhenSourceFenceDrifts(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 92, Role: "admin"}, Selection: selection,
		IdempotencyKey: "attempt-coordinator-create-2", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var source model.BackupAssetExportSourceLease
	if err := harness.db.Where("job_id = ?", created.JobID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.RecoveryPointLease{}).Where("id = ?", source.LeaseID).
		Update("fence_token", "different-fence-token").Error; err != nil {
		t.Fatal(err)
	}

	coordinator, err := NewAttemptCoordinator(harness.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Claim(context.Background(), AttemptClaimRequest{
		JobID: created.JobID, WorkerOwner: "worker-a",
	}); !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("source-fence claim error=%v", err)
	}
	var count int64
	if err := harness.db.Model(&model.BackupAssetExportAttempt{}).Where("job_id = ?", created.JobID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("attempt count=%d err=%v", count, err)
	}
}

func TestAttemptCoordinatorClaimUsesFrozenLeaseTTL(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 93, Role: "admin"}, Selection: selection,
		IdempotencyKey: "attempt-coordinator-create-ttl", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Second)
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{
		JobID: created.JobID, WorkerOwner: "worker-ttl",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := clock.Add(harness.config.LeaseTTL)
	if !claim.LeaseExpiresAt.Equal(want) {
		t.Fatalf("claim expiry=%s want frozen lease TTL expiry=%s", claim.LeaseExpiresAt, want)
	}
}

func TestAttemptCoordinatorClaimReservesPairedDurableWorkerSlotsAtGlobalCap(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	first, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 108, Role: "admin"}, Selection: selection,
		IdempotencyKey: "worker-capacity-first", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 109, Role: "admin"}, Selection: selection,
		IdempotencyKey: "worker-capacity-second", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := harness.service.now().UTC()
	coordinator, err := NewAttemptCoordinatorWithWorkerCapacity(
		harness.db,
		func() time.Time { return clock },
		WorkerCapacityLimits{WorkerConcurrency: 1, UserActiveJobs: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: first.JobID, WorkerOwner: "worker-capacity-first"})
	if err != nil {
		t.Fatal(err)
	}
	assertDurableWorkerReservationPair(t, harness.db, first.JobID, claim.AttemptID, claim.LeaseExpiresAt, 1)
	if _, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: second.JobID, WorkerOwner: "worker-capacity-second"}); !errors.Is(err, ErrAttemptNotClaimable) {
		t.Fatalf("global worker-cap claim error=%v", err)
	}
	assertDurableWorkerReservationPair(t, harness.db, second.JobID, "", time.Time{}, 0)
}

func TestAttemptCoordinatorClaimReservesPairedDurableWorkerSlotsAtUserCap(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	first, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 115, Role: "admin"}, Selection: selection,
		IdempotencyKey: "worker-capacity-user-first", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 118, Role: "admin"}, Selection: selection,
		IdempotencyKey: "worker-capacity-user-second", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", second.JobID).
		Update("owner_user_id", uint(115)).Error; err != nil {
		t.Fatal(err)
	}
	clock := harness.service.now().UTC()
	coordinator, err := NewAttemptCoordinatorWithWorkerCapacity(
		harness.db,
		func() time.Time { return clock },
		WorkerCapacityLimits{WorkerConcurrency: 2, UserActiveJobs: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: first.JobID, WorkerOwner: "worker-capacity-user-first"})
	if err != nil {
		t.Fatal(err)
	}
	assertDurableWorkerReservationPair(t, harness.db, first.JobID, claim.AttemptID, claim.LeaseExpiresAt, 1)
	if _, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: second.JobID, WorkerOwner: "worker-capacity-user-second"}); !errors.Is(err, ErrAttemptNotClaimable) {
		t.Fatalf("user worker-cap claim error=%v", err)
	}
	assertDurableWorkerReservationPair(t, harness.db, second.JobID, "", time.Time{}, 0)
}

func TestAttemptCoordinatorHeartbeatRenewsPairedDurableWorkerSlots(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 110, Role: "admin"}, Selection: selection,
		IdempotencyKey: "worker-capacity-heartbeat", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := harness.service.now().UTC()
	sourceLeases, err := backupasset.NewLeaseService(harness.db, func() time.Time { return clock }, backupasset.LeaseConfig{
		Duration: harness.config.LeaseTTL, Heartbeat: harness.config.LeaseRenewMargin, AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewAttemptCoordinatorWithWorkerCapacity(
		harness.db,
		func() time.Time { return clock },
		WorkerCapacityLimits{WorkerConcurrency: 2, UserActiveJobs: 2},
		sourceLeases,
	)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-capacity-heartbeat"})
	if err != nil {
		t.Fatal(err)
	}
	clock = claim.LeaseExpiresAt.Add(-time.Minute)
	heartbeat, err := coordinator.Heartbeat(context.Background(), AttemptHeartbeatRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertDurableWorkerReservationPair(t, harness.db, created.JobID, claim.AttemptID, heartbeat.LeaseExpiresAt, 1)
	if err := harness.db.Model(&model.BackupAssetExportReservation{}).
		Where("job_id = ? AND attempt_id = ? AND kind = ?", created.JobID, claim.AttemptID, "worker").
		Order("id DESC").Limit(1).Update("lease_owner", strings.Repeat("f", 32)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Heartbeat(context.Background(), AttemptHeartbeatRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	}); !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("mismatched worker reservation heartbeat error=%v", err)
	}
}

func TestAttemptCoordinatorFailureReleasesPairedDurableWorkerSlots(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 111, Role: "admin"}, Selection: selection,
		IdempotencyKey: "worker-capacity-failure", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := harness.service.now().UTC()
	coordinator, err := NewAttemptCoordinatorWithWorkerCapacity(
		harness.db,
		func() time.Time { return clock },
		WorkerCapacityLimits{WorkerConcurrency: 1, UserActiveJobs: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-capacity-failure"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Fail(context.Background(), AttemptFailureRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
		Category: "archive_failed", Retryable: false,
	}); err != nil {
		t.Fatal(err)
	}
	assertReleasedDurableWorkerReservationPair(t, harness.db, created.JobID, claim.AttemptID, 0)
}

func TestAttemptCoordinatorTakeoverReleasesSupersededDurableWorkerSlots(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 112, Role: "admin"}, Selection: selection,
		IdempotencyKey: "worker-capacity-takeover", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := harness.service.now().UTC()
	sourceLeases, err := backupasset.NewLeaseService(harness.db, func() time.Time { return clock }, backupasset.LeaseConfig{
		Duration: harness.config.LeaseTTL, Heartbeat: harness.config.LeaseRenewMargin, AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewAttemptCoordinatorWithWorkerCapacity(
		harness.db,
		func() time.Time { return clock },
		WorkerCapacityLimits{WorkerConcurrency: 1, UserActiveJobs: 1},
		sourceLeases,
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-capacity-first"})
	if err != nil {
		t.Fatal(err)
	}
	clock = first.LeaseExpiresAt
	if _, err := coordinator.TakeoverSourceLeases(context.Background(), SourceLeaseTakeoverRequest{JobID: created.JobID}); err != nil {
		t.Fatal(err)
	}
	second, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-capacity-second"})
	if err != nil {
		t.Fatal(err)
	}
	if second.SupersededAttemptID != first.AttemptID {
		t.Fatalf("takeover=%+v first=%+v", second, first)
	}
	assertReleasedDurableWorkerReservationPair(t, harness.db, created.JobID, first.AttemptID, 1)
	assertDurableWorkerReservationPair(t, harness.db, created.JobID, second.AttemptID, second.LeaseExpiresAt, 1)
}

func TestAttemptCoordinatorReconcilesExpiredDurableWorkerPairOnce(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 114, Role: "admin"}, Selection: selection,
		IdempotencyKey: "worker-capacity-reconcile", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := harness.service.now().UTC()
	coordinator, err := NewAttemptCoordinatorWithWorkerCapacity(
		harness.db,
		func() time.Time { return clock },
		WorkerCapacityLimits{WorkerConcurrency: 1, UserActiveJobs: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-capacity-reconcile"})
	if err != nil {
		t.Fatal(err)
	}
	clock = claim.LeaseExpiresAt
	processed, err := coordinator.ReconcileExpiredWorkerReservations(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 {
		t.Fatalf("reconciled worker pairs=%d want=1", processed)
	}
	assertReleasedDurableWorkerReservationPair(t, harness.db, created.JobID, claim.AttemptID, 0)
	processed, err = coordinator.ReconcileExpiredWorkerReservations(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if processed != 0 {
		t.Fatalf("idempotent reconciled worker pairs=%d want=0", processed)
	}
}

func TestAttemptCoordinatorReconcilesExpiredSealingDurableWorkerPair(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 116, Role: "admin"}, Selection: selection,
		IdempotencyKey: "worker-capacity-reconcile-sealing", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := harness.service.now().UTC()
	coordinator, err := NewAttemptCoordinatorWithWorkerCapacity(
		harness.db,
		func() time.Time { return clock },
		WorkerCapacityLimits{WorkerConcurrency: 1, UserActiveJobs: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-capacity-reconcile-sealing"})
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", claim.AttemptID).
			Updates(map[string]any{"state": string(AttemptSealing)}).Error; err != nil {
			return err
		}
		return tx.Model(&model.BackupAssetExportJob{}).Where("id = ?", created.JobID).
			Updates(map[string]any{"execution_state": string(ExecutionSealing)}).Error
	}); err != nil {
		t.Fatal(err)
	}
	clock = claim.LeaseExpiresAt
	processed, err := coordinator.ReconcileExpiredWorkerReservations(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 {
		t.Fatalf("reconciled sealing worker pairs=%d want=1", processed)
	}
	assertReleasedDurableWorkerReservationPair(t, harness.db, created.JobID, claim.AttemptID, 0)
	var job model.BackupAssetExportJob
	if err := harness.db.Where("id = ?", created.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if ExecutionState(job.ExecutionState) != ExecutionRetryWait || job.CurrentAttemptID != nil ||
		job.ErrorCategory != "heartbeat_lost" {
		t.Fatalf("reconciled sealing job=%+v, want retry_wait without current attempt", job)
	}
	var attempt model.BackupAssetExportAttempt
	if err := harness.db.Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.State != string(AttemptSuperseded) || attempt.IsCurrent || attempt.FinishedAt == nil {
		t.Fatalf("reconciled sealing attempt=%+v, want retired takeover fence", attempt)
	}
}

func TestAttemptCoordinatorReconciliationFailsAtAttemptLimit(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 117, Role: "admin"}, Selection: selection,
		IdempotencyKey: "worker-capacity-reconcile-attempt-limit", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := harness.service.now().UTC()
	coordinator, err := NewAttemptCoordinatorWithWorkerCapacity(
		harness.db,
		func() time.Time { return clock },
		WorkerCapacityLimits{WorkerConcurrency: 1, UserActiveJobs: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-capacity-reconcile-attempt-limit"})
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", created.JobID).
		Update("max_attempts", claim.AttemptNumber).Error; err != nil {
		t.Fatal(err)
	}
	clock = claim.LeaseExpiresAt
	processed, err := coordinator.ReconcileExpiredWorkerReservations(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 {
		t.Fatalf("reconciled worker pairs=%d want=1", processed)
	}
	assertReleasedDurableWorkerReservationPair(t, harness.db, created.JobID, claim.AttemptID, 0)
	var job model.BackupAssetExportJob
	if err := harness.db.Where("id = ?", created.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if ExecutionState(job.ExecutionState) != ExecutionFailed || job.CurrentAttemptID != nil {
		t.Fatalf("attempt-limit reconciled job=%+v, want terminal failed without current attempt", job)
	}
}

func TestPersistentWorkerPublishReadyReleasesDurableWorkerPair(t *testing.T) {
	limits := WorkerCapacityLimits{WorkerConcurrency: 1, UserActiveJobs: 1}
	fixture := createPersistentSealedFixture(t)
	fixture.worker.workerCapacity = &limits
	if err := fixture.harness.db.Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetExportJob
		if err := tx.Where("id = ?", fixture.jobID).Take(&job).Error; err != nil {
			return err
		}
		var attempt model.BackupAssetExportAttempt
		if err := tx.Where("id = ?", fixture.attemptID).Take(&attempt).Error; err != nil {
			return err
		}
		buckets, err := ensureAndLockQuotaBucketPairTx(tx, job.OwnerUserID, fixture.clock)
		if err != nil {
			return err
		}
		return reserveAttemptWorkerCapacityTx(tx, buckets, job, attempt, limits, fixture.clock)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.worker.PublishReady(context.Background(), PersistentPublishRequest{
		JobID: fixture.jobID, AttemptID: fixture.attemptID, FenceToken: fixture.fenceToken, ArtifactID: fixture.artifactID,
	}); err != nil {
		t.Fatal(err)
	}
	assertReleasedDurableWorkerReservationPair(t, fixture.harness.db, fixture.jobID, fixture.attemptID, 0)
}

func TestPersistentWorkerAttemptWorkDrainClosesBlockedSourceReader(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	item := frozenItemFixture()
	selection, err := FreezeSelection([]FrozenItem{item}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 120, Role: "admin"}, Selection: selection,
		IdempotencyKey: "worker-attempt-work-drain", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := harness.service.now().UTC()
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-attempt-work-drain"})
	if err != nil {
		t.Fatal(err)
	}
	var itemRow model.BackupAssetExportItem
	if err := harness.db.Where("job_id = ?", created.JobID).Take(&itemRow).Error; err != nil {
		t.Fatal(err)
	}
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	source := newBlockingPersistentSourceResolver(item)
	broker, err := content.NewAttemptBroker(source, budget, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "attempt-work-drain")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	work := NewAttemptWorkRegistry()
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: harness.db, Keys: backupasset.NewKeyring(harness.db, func() time.Time { return clock }), Broker: broker,
		Metadata: &metadataValidatorFake{}, Store: store, AttemptWork: work, Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	spooled := make(chan error, 1)
	go func() {
		_, err := worker.SpoolItem(context.Background(), PersistentSpoolItemRequest{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: itemRow.ID,
		})
		spooled <- err
	}()
	select {
	case <-source.reader.started:
	case <-time.After(time.Second):
		t.Fatal("blocked source reader was not opened")
	}
	if err := work.Drain(context.Background(), created.JobID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-source.reader.closed:
	case <-time.After(time.Second):
		t.Fatal("attempt work drain did not close the blocked source reader")
	}
	if err := <-spooled; err == nil {
		t.Fatal("spool succeeded after its source work was drained")
	}
	if _, err := work.Start(context.Background(), created.JobID); !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("fenced job accepted new source work: %v", err)
	}
}

func TestPersistentWorkerSealArchiveRegistersAttemptWork(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	item := frozenItemFixture()
	item.EntryType = backupasset.CatalogEntryDirectory
	item.LogicalSize = 0
	selection, err := FreezeSelection([]FrozenItem{item}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 121, Role: "admin"}, Selection: selection,
		IdempotencyKey: "worker-seal-attempt-work", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := harness.service.now().UTC()
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{
		JobID: created.JobID, WorkerOwner: "worker-seal-attempt-work",
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "seal-attempt-work")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	metadata := newAttemptWorkBlockingMetadataValidator()
	t.Cleanup(metadata.release)
	work := NewAttemptWorkRegistry()
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: harness.db, Keys: backupasset.NewKeyring(harness.db, func() time.Time { return clock }),
		Broker: &workerBrokerFake{}, Metadata: metadata, Store: store, AttemptWork: work,
		Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed := make(chan error, 1)
	go func() {
		_, sealErr := worker.SealArchive(context.Background(), PersistentSealRequest{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
		})
		sealed <- sealErr
	}()
	select {
	case <-metadata.started:
	case <-time.After(time.Second):
		t.Fatal("seal archive did not reach metadata validation")
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := work.Drain(drainCtx, created.JobID); err != nil {
		t.Fatalf("drain seal attempt work: %v", err)
	}
	select {
	case sealErr := <-sealed:
		if !errors.Is(sealErr, context.Canceled) {
			t.Fatalf("seal error=%v, want context cancellation", sealErr)
		}
	case <-time.After(time.Second):
		t.Fatal("seal archive did not exit after attempt-work drain")
	}
}

func TestPersistentWorkerPublishReadyRegistersAttemptWork(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	lookupStarted := make(chan byVersionLookup, 1)
	releaseLookup := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseLookup) }) }
	t.Cleanup(release)
	keys := &barrierByVersionKeySource{
		Keyring: fixture.ring, lookupStarted: lookupStarted, releaseLookup: releaseLookup,
	}
	work := NewAttemptWorkRegistry()
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: fixture.harness.db, Keys: keys, Broker: fixture.broker, Metadata: fixture.metadata, Store: fixture.store,
		AttemptWork: work, Now: func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	published := make(chan error, 1)
	go func() {
		_, publishErr := worker.PublishReady(context.Background(), PersistentPublishRequest{
			JobID: fixture.jobID, AttemptID: fixture.attemptID, FenceToken: fixture.fenceToken, ArtifactID: fixture.artifactID,
		})
		published <- publishErr
	}()
	lookupCtx, cancelLookup := context.WithTimeout(context.Background(), time.Second)
	defer cancelLookup()
	_ = waitByVersionLookup(t, lookupCtx, lookupStarted)
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), time.Second)
	defer cancelDrain()
	if err := work.Drain(drainCtx, fixture.jobID); err != nil {
		t.Fatalf("drain ready publication attempt work: %v", err)
	}
	select {
	case publishErr := <-published:
		if !errors.Is(publishErr, context.Canceled) {
			t.Fatalf("publish error=%v, want context cancellation", publishErr)
		}
	case <-time.After(time.Second):
		t.Fatal("ready publication did not exit after attempt-work drain")
	}
}

func TestPersistentWorkerRequiresAttemptWorkRegistry(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "worker-capacity-registry")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	limits := WorkerCapacityLimits{WorkerConcurrency: 1, UserActiveJobs: 1}
	for _, test := range []struct {
		name     string
		capacity *WorkerCapacityLimits
	}{
		{name: "without capacity"},
		{name: "with capacity", capacity: &limits},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker, err := NewPersistentWorker(PersistentWorkerDependencies{
				DB: harness.db, Keys: backupasset.NewKeyring(harness.db, harness.service.now),
				Broker: &workerBrokerFake{}, Metadata: &metadataValidatorFake{}, Store: store,
				WorkerCapacity: test.capacity, Now: harness.service.now,
			})
			if !errors.Is(err, ErrUnavailable) || worker != nil {
				t.Fatalf("worker without attempt registry worker=%v err=%v", worker, err)
			}
		})
	}
}

func TestPersistentWorkerRequiresSharedAttemptWorkRegistryWithPersistentLifecycle(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "worker-lifecycle-attempt-registry")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	quota, err := NewQuotaService(harness.db, harness.service.now, harness.config.Quota)
	if err != nil {
		t.Fatal(err)
	}
	shared := NewAttemptWorkRegistry()
	port, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
		DB: harness.db, Delivery: exportBehaviorLifecycleDeliveryStub{}, Sources: harness.lease,
		Quota: quota, Store: store, AttemptWork: shared, Now: harness.service.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(LifecycleDependencies{DB: harness.db, Port: port, Now: harness.service.now})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		attemptWork *AttemptWorkRegistry
		wantErr     bool
	}{
		{name: "shared registry", attemptWork: shared},
		{name: "different registry", attemptWork: NewAttemptWorkRegistry(), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker, workerErr := NewPersistentWorker(PersistentWorkerDependencies{
				DB: harness.db, Keys: backupasset.NewKeyring(harness.db, harness.service.now),
				Broker: &workerBrokerFake{}, Metadata: &metadataValidatorFake{}, Store: store,
				Lifecycle: lifecycle, AttemptWork: test.attemptWork, Now: harness.service.now,
			})
			if test.wantErr {
				if !errors.Is(workerErr, ErrUnavailable) || worker != nil {
					t.Fatalf("worker with different lifecycle registry worker=%v err=%v", worker, workerErr)
				}
				return
			}
			if workerErr != nil || worker == nil {
				t.Fatalf("worker with shared lifecycle registry worker=%v err=%v", worker, workerErr)
			}
		})
	}
}

func assertReleasedDurableWorkerReservationPair(
	t *testing.T,
	db *gorm.DB,
	jobID, attemptID string,
	wantActiveWorkersPerBucket int64,
) {
	t.Helper()
	var reservations []model.BackupAssetExportReservation
	if err := db.Where("job_id = ? AND attempt_id = ? AND kind = ?", jobID, attemptID, "worker").
		Order("bucket_id ASC").Find(&reservations).Error; err != nil {
		t.Fatal(err)
	}
	if len(reservations) != 2 {
		t.Fatalf("released worker reservation count=%d want=2", len(reservations))
	}
	bucketIDs := make([]string, 0, len(reservations))
	for _, reservation := range reservations {
		if reservation.State != "released" || reservation.ReleasedAt == nil {
			t.Fatalf("worker reservation was not released: %+v", reservation)
		}
		bucketIDs = append(bucketIDs, reservation.BucketID)
	}
	var buckets []model.BackupAssetExportQuotaBucket
	if err := db.Where("id IN ?", bucketIDs).Find(&buckets).Error; err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("released worker bucket count=%d want=2", len(buckets))
	}
	for _, bucket := range buckets {
		if bucket.ActiveWorkers != wantActiveWorkersPerBucket {
			t.Fatalf("released worker bucket %s/%s active_workers=%d want=%d", bucket.Scope, bucket.Subject, bucket.ActiveWorkers, wantActiveWorkersPerBucket)
		}
	}
}

func assertDurableWorkerReservationPair(
	t *testing.T,
	db *gorm.DB,
	jobID, attemptID string,
	leaseExpiresAt time.Time,
	wantActiveWorkers int64,
) {
	t.Helper()
	if wantActiveWorkers == 0 {
		var count int64
		if err := db.Model(&model.BackupAssetExportReservation{}).
			Where("job_id = ? AND kind = ?", jobID, "worker").Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("unexpected worker reservations for unclaimed job: %d", count)
		}
		return
	}
	var reservations []model.BackupAssetExportReservation
	if err := db.Where("job_id = ? AND attempt_id = ? AND kind = ?", jobID, attemptID, "worker").
		Order("bucket_id ASC").Find(&reservations).Error; err != nil {
		t.Fatal(err)
	}
	if len(reservations) != 2 {
		t.Fatalf("worker reservation count=%d want=2", len(reservations))
	}
	bucketIDs := []string{reservations[0].BucketID, reservations[1].BucketID}
	var buckets []model.BackupAssetExportQuotaBucket
	if err := db.Where("id IN ?", bucketIDs).Order("scope ASC, subject ASC").Find(&buckets).Error; err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("worker reservation bucket count=%d want=2", len(buckets))
	}
	for _, bucket := range buckets {
		if bucket.ActiveWorkers != wantActiveWorkers {
			t.Fatalf("bucket %s/%s active_workers=%d want=%d", bucket.Scope, bucket.Subject, bucket.ActiveWorkers, wantActiveWorkers)
		}
	}
	for _, reservation := range reservations {
		if reservation.State != "active" || reservation.LeaseOwner != attemptID || reservation.ReservedSlots != 1 ||
			reservation.ReservedLogicalBytes != 0 || reservation.ReservedProviderBytes != 0 ||
			reservation.ReservedCipherBytes != 0 || reservation.ReservedStoreBytes != 0 ||
			!reservation.LeaseExpiresAt.Equal(leaseExpiresAt) {
			t.Fatalf("worker reservation=%+v", reservation)
		}
	}
}

func TestAttemptCoordinatorFailurePersistsRetryBackoffAndTerminalCap(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 109, Role: "admin"}, Selection: selection,
		IdempotencyKey: "attempt-failure-retry", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Second)
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	first, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-retry"})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := coordinator.Fail(context.Background(), AttemptFailureRequest{
		JobID: created.JobID, AttemptID: first.AttemptID, FenceToken: first.FenceToken,
		Category: "worker_unavailable", Retryable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantRetryAt := clock.Add(harness.config.RetryBase)
	if retry.ExecutionState != ExecutionRetryWait || retry.RetryAt == nil || !retry.RetryAt.Equal(wantRetryAt) {
		t.Fatalf("retry result=%+v want retry_at=%s", retry, wantRetryAt)
	}
	if _, err := coordinator.Claim(context.Background(), AttemptClaimRequest{
		JobID: created.JobID, WorkerOwner: "worker-too-early",
	}); !errors.Is(err, ErrAttemptNotClaimable) {
		t.Fatalf("claim before retry backoff error=%v", err)
	}
	clock = wantRetryAt
	second, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-second"})
	if err != nil {
		t.Fatal(err)
	}
	if second.AttemptNumber != 2 || second.AttemptID == first.AttemptID {
		t.Fatalf("second claim=%+v first=%+v", second, first)
	}
	if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", created.JobID).
		Update("max_attempts", 2).Error; err != nil {
		t.Fatal(err)
	}
	terminal, err := coordinator.Fail(context.Background(), AttemptFailureRequest{
		JobID: created.JobID, AttemptID: second.AttemptID, FenceToken: second.FenceToken,
		Category: "worker_unavailable", Retryable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if terminal.ExecutionState != ExecutionFailed || terminal.RetryAt != nil {
		t.Fatalf("terminal result=%+v", terminal)
	}
	var job model.BackupAssetExportJob
	if err := harness.db.First(&job, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.CurrentAttemptID != nil || job.ExecutionState != string(ExecutionFailed) ||
		job.ErrorCategory != "worker_unavailable" {
		t.Fatalf("terminal job=%+v", job)
	}
	var key model.BackupAssetExportKey
	if err := harness.db.First(&key, "job_id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if key.State != "active" || len(key.WrappedDEK) == 0 {
		t.Fatalf("attempt failure destroyed job key before ordered lifecycle: %+v", key)
	}
}

func TestPersistentWorkerDiscardsFailedAttemptCiphertextBeforeRetry(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 110, Role: "admin"}, Selection: selection,
		IdempotencyKey: "attempt-discard-before-retry", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Second)
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-discard"})
	if err != nil {
		t.Fatal(err)
	}
	var itemAttempt model.BackupAssetExportItemAttempt
	if err := harness.db.Where("job_id = ? AND attempt_id = ?", created.JobID, claim.AttemptID).Take(&itemAttempt).Error; err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	spool, err := store.CreateItemSpool(created.JobID, claim.AttemptID, itemAttempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.File.Write([]byte("encrypted-spool")); err != nil {
		t.Fatal(err)
	}
	spoolLocator, err := store.Seal(spool)
	if err != nil {
		t.Fatal(err)
	}
	spoolDigest := strings.Repeat("d", 64)
	spoolSize := int64(len("encrypted-spool"))
	staging, err := store.CreateStaging(created.JobID, claim.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := staging.File.Write([]byte("encrypted-final-stage")); err != nil {
		t.Fatal(err)
	}
	stagingLocator, err := store.Seal(staging)
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", itemAttempt.ID).
		Updates(map[string]any{
			"state": string(ItemRead), "spool_locator": spoolLocator,
			"spool_size": spoolSize, "spool_digest": spoolDigest,
		}).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", claim.AttemptID).
		Update("staging_locator", stagingLocator).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Fail(context.Background(), AttemptFailureRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
		Category: "archive_failed", Retryable: true,
	}); err != nil {
		t.Fatal(err)
	}
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: harness.db, Keys: backupasset.NewKeyring(harness.db, func() time.Time { return clock }),
		Broker: &workerBrokerFake{}, Metadata: &metadataValidatorFake{}, Store: store, AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.DiscardAttempt(context.Background(), PersistentDiscardAttemptRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID,
	}); err != nil {
		t.Fatal(err)
	}
	for _, locator := range []string{spoolLocator, stagingLocator} {
		if _, err := os.Lstat(filepath.Join(store.root, locator)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed attempt object %q remains: %v", locator, err)
		}
	}
	if err := harness.db.First(&itemAttempt, "id = ?", itemAttempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	var attempt model.BackupAssetExportAttempt
	if err := harness.db.First(&attempt, "id = ?", claim.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if itemAttempt.SpoolLocator != "" || itemAttempt.SpoolSize != spoolSize ||
		itemAttempt.SpoolDigest != spoolDigest || attempt.StagingLocator != "" {
		t.Fatalf("failed attempt locators remain: item=%+v attempt=%+v", itemAttempt, attempt)
	}
	var key model.BackupAssetExportKey
	if err := harness.db.First(&key, "job_id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if key.State != "active" || len(key.WrappedDEK) == 0 {
		t.Fatalf("attempt discard destroyed reusable job key: %+v", key)
	}
}

func TestPersistentWorkerDiscardsSupersededSealingArtifactAndRetainsSpoolEvidence(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	var oldAttempt model.BackupAssetExportAttempt
	if err := fixture.harness.db.First(&oldAttempt, "id = ?", fixture.attemptID).Error; err != nil {
		t.Fatal(err)
	}
	var oldItemAttempt model.BackupAssetExportItemAttempt
	if err := fixture.harness.db.Where("job_id = ? AND attempt_id = ?", fixture.jobID, fixture.attemptID).
		Take(&oldItemAttempt).Error; err != nil {
		t.Fatal(err)
	}

	spoolPayload := []byte("retired-spool-evidence")
	spool, err := fixture.store.CreateItemSpool(fixture.jobID, fixture.attemptID, oldItemAttempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.File.Write(spoolPayload); err != nil {
		t.Fatal(err)
	}
	spoolLocator, err := fixture.store.Seal(spool)
	if err != nil {
		t.Fatal(err)
	}
	spoolDigest := strings.Repeat("d", 64)
	spoolSize := int64(len(spoolPayload))
	if err := fixture.harness.db.Model(&model.BackupAssetExportItemAttempt{}).
		Where("id = ?", oldItemAttempt.ID).
		Updates(map[string]any{
			"spool_locator": spoolLocator,
			"spool_digest":  spoolDigest,
			"spool_size":    spoolSize,
		}).Error; err != nil {
		t.Fatal(err)
	}

	clock := oldAttempt.LeaseExpiresAt.UTC().Add(time.Second)
	sourceLeases, err := backupasset.NewLeaseService(fixture.harness.db, func() time.Time { return clock }, backupasset.LeaseConfig{
		Duration: fixture.harness.config.LeaseTTL, Heartbeat: fixture.harness.config.LeaseRenewMargin,
		AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return clock }, sourceLeases)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.TakeoverSourceLeases(context.Background(), SourceLeaseTakeoverRequest{JobID: fixture.jobID}); err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{
		JobID: fixture.jobID, WorkerOwner: "worker-retire-sealing",
	})
	if err != nil {
		t.Fatal(err)
	}
	if claim.SupersededAttemptID != fixture.attemptID {
		t.Fatalf("superseded attempt=%q want=%q", claim.SupersededAttemptID, fixture.attemptID)
	}

	if err := fixture.worker.DiscardAttempt(context.Background(), PersistentDiscardAttemptRequest{
		JobID: fixture.jobID, AttemptID: claim.SupersededAttemptID,
	}); err != nil {
		t.Fatal(err)
	}
	for _, locator := range []string{spoolLocator, fixture.locator} {
		if _, err := os.Lstat(filepath.Join(fixture.store.root, locator)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("superseded attempt object %q remains: %v", locator, err)
		}
	}
	var artifactCount int64
	if err := fixture.harness.db.Model(&model.BackupAssetExportArtifact{}).
		Where("id = ? AND job_id = ? AND attempt_id = ?", fixture.artifactID, fixture.jobID, fixture.attemptID).
		Count(&artifactCount).Error; err != nil {
		t.Fatal(err)
	}
	if artifactCount != 0 {
		t.Fatalf("superseded pre-ready artifact rows=%d want=0", artifactCount)
	}
	if err := fixture.harness.db.First(&oldAttempt, "id = ?", fixture.attemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.harness.db.First(&oldItemAttempt, "id = ?", oldItemAttempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if oldAttempt.StagingLocator != "" || oldItemAttempt.SpoolLocator != "" ||
		oldItemAttempt.SpoolDigest != spoolDigest || oldItemAttempt.SpoolSize != spoolSize {
		t.Fatalf("superseded attempt retirement lost immutable evidence: attempt=%+v item_attempt=%+v", oldAttempt, oldItemAttempt)
	}
	var current model.BackupAssetExportJob
	if err := fixture.harness.db.First(&current, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if current.CurrentAttemptID == nil || *current.CurrentAttemptID != claim.AttemptID ||
		current.ExecutionState != string(ExecutionRunning) {
		t.Fatalf("fresh attempt changed during old-attempt retirement: %+v", current)
	}
}

func TestPersistentWorkerDiscardSupersededSealedArtifactDebitsExactStoreQuotaOnce(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	artifact, job, before := persistentDiscardSealedArtifactQuotaFixture(t, fixture)
	claim := supersedePersistentSealedFixture(t, fixture)

	if err := fixture.worker.DiscardAttempt(context.Background(), PersistentDiscardAttemptRequest{
		JobID: fixture.jobID, AttemptID: claim.SupersededAttemptID,
	}); err != nil {
		t.Fatal(err)
	}
	assertPersistentDiscardSealedArtifactQuota(t, fixture, artifact, job, before, true)

	if err := fixture.worker.DiscardAttempt(context.Background(), PersistentDiscardAttemptRequest{
		JobID: fixture.jobID, AttemptID: claim.SupersededAttemptID,
	}); err != nil {
		t.Fatalf("idempotent superseded discard: %v", err)
	}
	assertPersistentDiscardSealedArtifactQuota(t, fixture, artifact, job, before, true)
}

func TestPersistentWorkerDiscardSupersededSealedArtifactRetainsChargeAfterPurgeFailureAndRetries(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	artifact, job, before := persistentDiscardSealedArtifactQuotaFixture(t, fixture)
	claim := supersedePersistentSealedFixture(t, fixture)

	originalIdentity := fixture.store.rootIdentity
	fixture.store.rootIdentity = storeRootIdentity{}
	err := fixture.worker.DiscardAttempt(context.Background(), PersistentDiscardAttemptRequest{
		JobID: fixture.jobID, AttemptID: claim.SupersededAttemptID,
	})
	fixture.store.rootIdentity = originalIdentity
	if err == nil {
		t.Fatal("discard succeeded despite failed physical purge proof")
	}
	assertPersistentDiscardSealedArtifactQuota(t, fixture, artifact, job, before, false)
	if _, err := os.Lstat(filepath.Join(fixture.store.root, artifact.Locator)); err != nil {
		t.Fatalf("purge failure removed sealed object: %v", err)
	}

	if err := fixture.worker.DiscardAttempt(context.Background(), PersistentDiscardAttemptRequest{
		JobID: fixture.jobID, AttemptID: claim.SupersededAttemptID,
	}); err != nil {
		t.Fatalf("retry discard after failed purge: %v", err)
	}
	assertPersistentDiscardSealedArtifactQuota(t, fixture, artifact, job, before, true)
}

func TestPersistentWorkerDiscardSupersededSealedArtifactRetainsChargeAfterDBFailureAndRetries(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	artifact, job, before := persistentDiscardSealedArtifactQuotaFixture(t, fixture)
	claim := supersedePersistentSealedFixture(t, fixture)

	injectedErr := errors.New("injected discard artifact finalization failure")
	const callbackName = "test:discard_sealed_artifact_finalization_failure"
	injected := false
	if err := fixture.harness.db.Callback().Delete().Before("gorm:delete").Register(callbackName, func(tx *gorm.DB) {
		if injected || tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != "backup_asset_export_artifacts" {
			return
		}
		injected = true
		_ = tx.AddError(injectedErr)
	}); err != nil {
		t.Fatal(err)
	}
	callbackRegistered := true
	t.Cleanup(func() {
		if callbackRegistered {
			if err := fixture.harness.db.Callback().Delete().Remove(callbackName); err != nil {
				t.Errorf("remove discard finalization callback: %v", err)
			}
		}
	})

	err := fixture.worker.DiscardAttempt(context.Background(), PersistentDiscardAttemptRequest{
		JobID: fixture.jobID, AttemptID: claim.SupersededAttemptID,
	})
	if !errors.Is(err, injectedErr) {
		t.Fatalf("discard finalization error=%v, want injected cause", err)
	}
	if !injected {
		t.Fatal("discard did not reach artifact finalization after physical purge")
	}
	if _, err := os.Lstat(filepath.Join(fixture.store.root, artifact.Locator)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("DB finalization failure retained sealed object: %v", err)
	}
	assertPersistentDiscardSealedArtifactQuota(t, fixture, artifact, job, before, false)

	if err := fixture.harness.db.Callback().Delete().Remove(callbackName); err != nil {
		t.Fatal(err)
	}
	callbackRegistered = false
	if err := fixture.worker.DiscardAttempt(context.Background(), PersistentDiscardAttemptRequest{
		JobID: fixture.jobID, AttemptID: claim.SupersededAttemptID,
	}); err != nil {
		t.Fatalf("retry discard after DB finalization failure: %v", err)
	}
	assertPersistentDiscardSealedArtifactQuota(t, fixture, artifact, job, before, true)
}

func TestPersistentWorkerDiscardAttemptDoesNotMutateCurrentOrReadyArtifact(t *testing.T) {
	for _, setup := range []struct {
		name    string
		fixture func(*testing.T) persistentSealedFixture
	}{
		{name: "current", fixture: createPersistentSealedFixture},
		{name: "ready", fixture: createPersistentReadyFixture},
	} {
		t.Run(setup.name, func(t *testing.T) {
			fixture := setup.fixture(t)
			artifact, job, before := persistentDiscardSealedArtifactQuotaFixture(t, fixture)
			var beforeAttempt model.BackupAssetExportAttempt
			if err := fixture.harness.db.Where("id = ?", fixture.attemptID).Take(&beforeAttempt).Error; err != nil {
				t.Fatal(err)
			}

			err := fixture.worker.DiscardAttempt(context.Background(), PersistentDiscardAttemptRequest{
				JobID: fixture.jobID, AttemptID: fixture.attemptID,
			})
			if !errors.Is(err, ErrAttemptFenceLost) {
				t.Fatalf("discard current/ready attempt error=%v, want ErrAttemptFenceLost", err)
			}
			assertPersistentDiscardSealedArtifactQuota(t, fixture, artifact, job, before, false)
			var afterAttempt model.BackupAssetExportAttempt
			if err := fixture.harness.db.Where("id = ?", fixture.attemptID).Take(&afterAttempt).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(afterAttempt, beforeAttempt) {
				t.Fatalf("discard mutated current/ready attempt: before=%+v after=%+v", beforeAttempt, afterAttempt)
			}
			if _, err := os.Lstat(filepath.Join(fixture.store.root, artifact.Locator)); err != nil {
				t.Fatalf("discard removed current/ready sealed object: %v", err)
			}
		})
	}
}

func TestPersistentWorkerDiscardSupersededSealedArtifactLocksQuotaBeforeRetiredRowsAndReservations(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	_, job, _ := persistentDiscardSealedArtifactQuotaFixture(t, fixture)
	claim := supersedePersistentSealedFixture(t, fixture)
	ownerSubject := fmt.Sprintf("%d", job.OwnerUserID)

	var locks []string
	const callbackName = "test:discard_sealed_artifact_quota_lock_order"
	if err := fixture.harness.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if !persistentStatementHasUpdateLock(tx) || tx.Statement == nil || tx.Statement.Schema == nil {
			return
		}
		switch tx.Statement.Schema.Table {
		case "backup_asset_export_quota_buckets":
			bucket := ""
			for _, value := range tx.Statement.Vars {
				subject, ok := value.(string)
				if !ok {
					continue
				}
				switch subject {
				case "global":
					bucket = "bucket:global"
				case ownerSubject:
					bucket = "bucket:user"
				}
			}
			if bucket != "" {
				locks = append(locks, bucket)
			}
		case "backup_asset_export_jobs":
			locks = append(locks, "job")
		case "backup_asset_export_attempts":
			locks = append(locks, "attempt")
		case "backup_asset_export_artifacts":
			locks = append(locks, "artifact")
		case "backup_asset_export_reservations":
			locks = append(locks, "reservation")
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.harness.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove discard lock-order callback: %v", err)
		}
	})

	if err := fixture.worker.DiscardAttempt(context.Background(), PersistentDiscardAttemptRequest{
		JobID: fixture.jobID, AttemptID: claim.SupersededAttemptID,
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"bucket:global", "bucket:user", "job", "attempt", "artifact", "reservation", "reservation"}
	if !slices.Equal(locks, want) {
		t.Fatalf("discard lock order=%v, want %v", locks, want)
	}
}

func TestPersistentWorkerDiscardAttemptPreservesLockedAttemptQueryError(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	artifact, job, before := persistentDiscardSealedArtifactQuotaFixture(t, fixture)
	claim := supersedePersistentSealedFixture(t, fixture)

	injected := errors.New("injected discard locked attempt query failure")
	const callbackName = "test:discard_locked_attempt_query_error"
	fired := false
	if err := fixture.harness.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if fired || tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != "backup_asset_export_attempts" || !persistentStatementHasUpdateLock(tx) {
			return
		}
		fired = true
		_ = tx.AddError(injected)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.harness.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove discard locked-attempt query callback: %v", err)
		}
	})

	err := fixture.worker.DiscardAttempt(context.Background(), PersistentDiscardAttemptRequest{
		JobID: fixture.jobID, AttemptID: claim.SupersededAttemptID,
	})
	assertPersistentDiscardDatabaseError(t, err, injected, fired)
	assertPersistentDiscardSealedArtifactQuota(t, fixture, artifact, job, before, false)
	if _, err := os.Lstat(filepath.Join(fixture.store.root, artifact.Locator)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("locked-attempt query failure retained sealed object: %v", err)
	}
}

func TestPersistentWorkerDiscardAttemptPreservesItemSpoolUpdateError(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	artifact, job, before := persistentDiscardSealedArtifactQuotaFixture(t, fixture)
	var itemAttempt model.BackupAssetExportItemAttempt
	if err := fixture.harness.db.Where("job_id = ? AND attempt_id = ?", fixture.jobID, fixture.attemptID).
		Take(&itemAttempt).Error; err != nil {
		t.Fatal(err)
	}
	spool, err := fixture.store.CreateItemSpool(fixture.jobID, fixture.attemptID, itemAttempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.File.Write([]byte("discard-error-spool")); err != nil {
		t.Fatal(err)
	}
	spoolLocator, err := fixture.store.Seal(spool)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.harness.db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", itemAttempt.ID).
		Update("spool_locator", spoolLocator).Error; err != nil {
		t.Fatal(err)
	}
	claim := supersedePersistentSealedFixture(t, fixture)

	injected := errors.New("injected discard item spool update failure")
	const callbackName = "test:discard_item_spool_update_error"
	fired := false
	if err := fixture.harness.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if fired || tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != "backup_asset_export_item_attempts" {
			return
		}
		fired = true
		_ = tx.AddError(injected)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.harness.db.Callback().Update().Remove(callbackName); err != nil {
			t.Errorf("remove discard item-spool update callback: %v", err)
		}
	})

	err = fixture.worker.DiscardAttempt(context.Background(), PersistentDiscardAttemptRequest{
		JobID: fixture.jobID, AttemptID: claim.SupersededAttemptID,
	})
	assertPersistentDiscardDatabaseError(t, err, injected, fired)
	assertPersistentDiscardSealedArtifactQuota(t, fixture, artifact, job, before, false)
	if _, err := os.Lstat(filepath.Join(fixture.store.root, artifact.Locator)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("item-spool update failure retained sealed object: %v", err)
	}
}

func TestPersistentWorkerDiscardAttemptPreservesStagingUpdateError(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	artifact, job, before := persistentDiscardSealedArtifactQuotaFixture(t, fixture)
	staging, err := fixture.store.CreateStaging(fixture.jobID, fixture.attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := staging.File.Write([]byte("discard-error-staging")); err != nil {
		t.Fatal(err)
	}
	stagingLocator, err := fixture.store.Seal(staging)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", fixture.attemptID).
		Update("staging_locator", stagingLocator).Error; err != nil {
		t.Fatal(err)
	}
	claim := supersedePersistentSealedFixture(t, fixture)

	injected := errors.New("injected discard staging update failure")
	const callbackName = "test:discard_staging_update_error"
	fired := false
	if err := fixture.harness.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if fired || tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != "backup_asset_export_attempts" {
			return
		}
		fired = true
		_ = tx.AddError(injected)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.harness.db.Callback().Update().Remove(callbackName); err != nil {
			t.Errorf("remove discard staging update callback: %v", err)
		}
	})

	err = fixture.worker.DiscardAttempt(context.Background(), PersistentDiscardAttemptRequest{
		JobID: fixture.jobID, AttemptID: claim.SupersededAttemptID,
	})
	assertPersistentDiscardDatabaseError(t, err, injected, fired)
	assertPersistentDiscardSealedArtifactQuota(t, fixture, artifact, job, before, false)
	if _, err := os.Lstat(filepath.Join(fixture.store.root, artifact.Locator)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging update failure retained sealed object: %v", err)
	}
}

func assertPersistentDiscardDatabaseError(t *testing.T, err, injected error, fired bool) {
	t.Helper()
	if !fired {
		t.Fatal("discard database callback was not reached")
	}
	if !errors.Is(err, injected) || errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("discard database error=%v, want injected infrastructure error without ErrAttemptFenceLost", err)
	}
}

func persistentDiscardSealedArtifactQuotaFixture(
	t *testing.T,
	fixture persistentSealedFixture,
) (model.BackupAssetExportArtifact, model.BackupAssetExportJob, persistentStoreQuotaPair) {
	t.Helper()
	var artifact model.BackupAssetExportArtifact
	if err := fixture.harness.db.Where("id = ? AND job_id = ? AND attempt_id = ?", fixture.artifactID, fixture.jobID, fixture.attemptID).
		Take(&artifact).Error; err != nil {
		t.Fatal(err)
	}
	if artifact.State != "sealed" || artifact.CiphertextSize <= 0 {
		t.Fatalf("discard fixture artifact=%+v", artifact)
	}
	var job model.BackupAssetExportJob
	if err := fixture.harness.db.Where("id = ?", fixture.jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	pair := loadPersistentStoreQuotaPair(t, fixture.harness.db, fixture.jobID, job.OwnerUserID)
	for _, bucket := range []model.BackupAssetExportQuotaBucket{pair.globalBucket, pair.userBucket} {
		if bucket.UsedStoreBytes < artifact.CiphertextSize {
			t.Fatalf("sealed artifact charge missing from %s quota bucket: bucket=%+v artifact=%+v", bucket.Scope, bucket, artifact)
		}
	}
	return artifact, job, pair
}

func supersedePersistentSealedFixture(t *testing.T, fixture persistentSealedFixture) AttemptClaim {
	t.Helper()
	var oldAttempt model.BackupAssetExportAttempt
	if err := fixture.harness.db.Where("id = ?", fixture.attemptID).Take(&oldAttempt).Error; err != nil {
		t.Fatal(err)
	}
	clock := oldAttempt.LeaseExpiresAt.UTC().Add(time.Second)
	sourceLeases, err := backupasset.NewLeaseService(fixture.harness.db, func() time.Time { return clock }, backupasset.LeaseConfig{
		Duration: fixture.harness.config.LeaseTTL, Heartbeat: fixture.harness.config.LeaseRenewMargin,
		AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return clock }, sourceLeases)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.TakeoverSourceLeases(context.Background(), SourceLeaseTakeoverRequest{JobID: fixture.jobID}); err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{
		JobID: fixture.jobID, WorkerOwner: "worker-retire-sealing",
	})
	if err != nil {
		t.Fatal(err)
	}
	if claim.SupersededAttemptID != fixture.attemptID {
		t.Fatalf("superseded attempt=%q want=%q", claim.SupersededAttemptID, fixture.attemptID)
	}
	return claim
}

func assertPersistentDiscardSealedArtifactQuota(
	t *testing.T,
	fixture persistentSealedFixture,
	artifact model.BackupAssetExportArtifact,
	job model.BackupAssetExportJob,
	before persistentStoreQuotaPair,
	discarded bool,
) {
	t.Helper()
	var count int64
	if err := fixture.harness.db.Model(&model.BackupAssetExportArtifact{}).
		Where("id = ? AND job_id = ? AND attempt_id = ?", artifact.ID, fixture.jobID, fixture.attemptID).
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	wantCount := int64(1)
	if discarded {
		wantCount = 0
	}
	if count != wantCount {
		t.Fatalf("sealed artifact rows=%d, want=%d", count, wantCount)
	}

	after := loadPersistentStoreQuotaPair(t, fixture.harness.db, fixture.jobID, job.OwnerUserID)
	for _, pair := range []struct {
		name   string
		before model.BackupAssetExportQuotaBucket
		after  model.BackupAssetExportQuotaBucket
	}{
		{name: "global", before: before.globalBucket, after: after.globalBucket},
		{name: "user", before: before.userBucket, after: after.userBucket},
	} {
		wantUsed := pair.before.UsedStoreBytes
		if discarded {
			wantUsed -= artifact.CiphertextSize
		}
		if pair.after.UsedStoreBytes != wantUsed || pair.after.ReservedStoreBytes != pair.before.ReservedStoreBytes {
			t.Fatalf("%s quota after discard=%+v, want used=%d reserved=%d", pair.name, pair.after,
				wantUsed, pair.before.ReservedStoreBytes)
		}
	}
	for _, pair := range []struct {
		name   string
		before model.BackupAssetExportReservation
		after  model.BackupAssetExportReservation
	}{
		{name: "global", before: before.globalReservation, after: after.globalReservation},
		{name: "user", before: before.userReservation, after: after.userReservation},
	} {
		if pair.after.State != "active" || pair.after.ReleasedAt != nil ||
			pair.after.ReservedStoreBytes != pair.before.ReservedStoreBytes {
			t.Fatalf("%s store reservation after discard=%+v, want active reservation=%+v", pair.name, pair.after, pair.before)
		}
	}
}

func TestAttemptCoordinatorHeartbeatAtomicallyRenewsAttemptAndSource(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 94, Role: "admin"}, Selection: selection,
		IdempotencyKey: "attempt-coordinator-heartbeat", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var sourceBefore model.BackupAssetExportSourceLease
	if err := harness.db.Where("job_id = ?", created.JobID).Take(&sourceBefore).Error; err != nil {
		t.Fatal(err)
	}
	clock := sourceBefore.RenewedAt.UTC()
	sourceLeases, err := backupasset.NewLeaseService(harness.db, func() time.Time { return clock }, backupasset.LeaseConfig{
		Duration: harness.config.LeaseTTL, Heartbeat: harness.config.LeaseRenewMargin, AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock }, sourceLeases)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{
		JobID: created.JobID, WorkerOwner: "worker-heartbeat",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock = claim.LeaseExpiresAt.Add(-time.Minute)
	heartbeat, err := coordinator.Heartbeat(context.Background(), AttemptHeartbeatRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantAttemptExpiry := clock.Add(harness.config.LeaseTTL)
	if !heartbeat.LeaseExpiresAt.Equal(wantAttemptExpiry) {
		t.Fatalf("attempt heartbeat expiry=%s want=%s", heartbeat.LeaseExpiresAt, wantAttemptExpiry)
	}
	if len(heartbeat.SourceLeaseExpiresAt) != 1 || !heartbeat.SourceAbsoluteDeadline.Equal(sourceBefore.AbsoluteDeadline) {
		t.Fatalf("heartbeat source result=%+v absolute_before=%s", heartbeat, sourceBefore.AbsoluteDeadline)
	}
	var sourceAfter model.BackupAssetExportSourceLease
	if err := harness.db.First(&sourceAfter, "id = ?", sourceBefore.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !sourceAfter.AbsoluteDeadline.Equal(sourceBefore.AbsoluteDeadline) || !sourceAfter.RenewedAt.Equal(clock) {
		t.Fatalf("source deadline/heartbeat drifted: before=%+v after=%+v", sourceBefore, sourceAfter)
	}
	var attempt model.BackupAssetExportAttempt
	if err := harness.db.First(&attempt, "id = ?", claim.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if !attempt.LeaseExpiresAt.Equal(wantAttemptExpiry) {
		t.Fatalf("persisted attempt expiry=%s want=%s", attempt.LeaseExpiresAt, wantAttemptExpiry)
	}

	beforeBad := attempt.LeaseExpiresAt
	badFence := append([]byte(nil), claim.FenceToken...)
	badFence[0] ^= 0xff
	clock = clock.Add(time.Second)
	if _, err := coordinator.Heartbeat(context.Background(), AttemptHeartbeatRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: badFence,
	}); !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("bad-fence heartbeat error=%v", err)
	}
	if err := harness.db.First(&attempt, "id = ?", claim.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if !attempt.LeaseExpiresAt.Equal(beforeBad) {
		t.Fatalf("bad heartbeat partially renewed attempt: before=%s after=%s", beforeBad, attempt.LeaseExpiresAt)
	}
}

func TestAttemptCoordinatorHeartbeatRejectsCommittedCancellationWithoutRenewingAnyLease(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	actor := SelectionActor{UserID: 196, Role: "admin"}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: actor, Selection: selection, IdempotencyKey: "heartbeat-after-cancel-fence",
		ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var sourceBefore model.BackupAssetExportSourceLease
	if err := harness.db.Where("job_id = ?", created.JobID).Take(&sourceBefore).Error; err != nil {
		t.Fatal(err)
	}
	var foundationBefore model.RecoveryPointLease
	if err := harness.db.First(&foundationBefore, "id = ?", sourceBefore.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	clock := sourceBefore.RenewedAt.UTC()
	sourceLeases, err := backupasset.NewLeaseService(harness.db, func() time.Time { return clock }, backupasset.LeaseConfig{
		Duration: harness.config.LeaseTTL, Heartbeat: harness.config.LeaseRenewMargin, AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock }, sourceLeases)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "heartbeat-after-cancel"})
	if err != nil {
		t.Fatal(err)
	}
	var attemptBefore model.BackupAssetExportAttempt
	if err := harness.db.First(&attemptBefore, "id = ?", claim.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := harness.service.Cancel(context.Background(), actor, created.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Heartbeat(context.Background(), AttemptHeartbeatRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	}); !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("heartbeat after committed cancellation error=%v, want ErrAttemptFenceLost", err)
	}
	var sourceAfter model.BackupAssetExportSourceLease
	var foundationAfter model.RecoveryPointLease
	var attemptAfter model.BackupAssetExportAttempt
	if err := harness.db.First(&sourceAfter, "id = ?", sourceBefore.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&foundationAfter, "id = ?", foundationBefore.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&attemptAfter, "id = ?", attemptBefore.ID).Error; err != nil {
		t.Fatal(err)
	}
	if sourceAfter.RenewedAt != sourceBefore.RenewedAt || sourceAfter.AbsoluteDeadline != sourceBefore.AbsoluteDeadline {
		t.Fatalf("export source lease renewed after cancellation: before=%+v after=%+v", sourceBefore, sourceAfter)
	}
	if foundationAfter.LeaseExpiresAt != foundationBefore.LeaseExpiresAt ||
		foundationAfter.AbsoluteDeadline != foundationBefore.AbsoluteDeadline ||
		foundationAfter.LastHeartbeatAt != foundationBefore.LastHeartbeatAt ||
		foundationAfter.AttemptID != foundationBefore.AttemptID || foundationAfter.FenceToken != foundationBefore.FenceToken {
		t.Fatalf("Foundation lease changed after cancellation: before=%+v after=%+v", foundationBefore, foundationAfter)
	}
	if attemptAfter.LeaseExpiresAt != attemptBefore.LeaseExpiresAt {
		t.Fatalf("attempt lease renewed after cancellation: before=%s after=%s", attemptBefore.LeaseExpiresAt, attemptAfter.LeaseExpiresAt)
	}
}

func TestAttemptCoordinatorHeartbeatClassifiesDeadlinesAndPreservesUnderlyingErrors(t *testing.T) {
	t.Run("execution deadline", func(t *testing.T) {
		harness := newWorkerServiceHarness(t)
		selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
		if err != nil {
			t.Fatal(err)
		}
		created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
			Actor: SelectionActor{UserID: 197, Role: "admin"}, Selection: selection,
			IdempotencyKey: "heartbeat-execution-deadline", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
		})
		if err != nil {
			t.Fatal(err)
		}
		clock := harness.service.now().UTC()
		coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock }, harness.lease)
		if err != nil {
			t.Fatal(err)
		}
		claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "heartbeat-deadline"})
		if err != nil {
			t.Fatal(err)
		}
		var job model.BackupAssetExportJob
		if err := harness.db.First(&job, "id = ?", created.JobID).Error; err != nil {
			t.Fatal(err)
		}
		clock = job.AbsoluteDeadline.UTC()
		if _, err := coordinator.Heartbeat(context.Background(), AttemptHeartbeatRequest{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
		}); !errors.Is(err, ErrExecutionDeadlineReached) || errors.Is(err, ErrAttemptFenceLost) {
			t.Fatalf("execution-deadline heartbeat error=%v", err)
		}
	})

	t.Run("source retention cap", func(t *testing.T) {
		harness := newWorkerServiceHarness(t)
		selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
		if err != nil {
			t.Fatal(err)
		}
		created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
			Actor: SelectionActor{UserID: 198, Role: "admin"}, Selection: selection,
			IdempotencyKey: "heartbeat-source-retention", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
		})
		if err != nil {
			t.Fatal(err)
		}
		clock := harness.service.now().UTC()
		coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock }, harness.lease)
		if err != nil {
			t.Fatal(err)
		}
		claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "heartbeat-source-cap"})
		if err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(time.Minute)
		if err := harness.db.Model(&model.BackupAssetExportSourceLease{}).Where("job_id = ?", created.JobID).
			Update("retention_until", clock).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := coordinator.Heartbeat(context.Background(), AttemptHeartbeatRequest{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
		}); !errors.Is(err, ErrSourceDeadlineReached) || errors.Is(err, ErrAttemptFenceLost) {
			t.Fatalf("source-cap heartbeat error=%v", err)
		}
	})

	for _, test := range []struct {
		name          string
		foundationErr error
		wantSourceCap bool
	}{
		{name: "Foundation fence", foundationErr: backupasset.ErrLeaseFenceLost},
		{name: "Foundation storage", foundationErr: errors.New("injected Foundation heartbeat failure")},
		{name: "Foundation deadline", foundationErr: backupasset.ErrLeaseDeadlineExceeded, wantSourceCap: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newWorkerServiceHarness(t)
			selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
			if err != nil {
				t.Fatal(err)
			}
			created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
				Actor: SelectionActor{UserID: 199, Role: "admin"}, Selection: selection,
				IdempotencyKey: "heartbeat-foundation-error-" + test.name, ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
			})
			if err != nil {
				t.Fatal(err)
			}
			clock := harness.service.now().UTC()
			coordinator, err := NewAttemptCoordinator(
				harness.db, func() time.Time { return clock }, sourceLeaseCoordinatorErrorFake{renewErr: test.foundationErr},
			)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "heartbeat-foundation"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = coordinator.Heartbeat(context.Background(), AttemptHeartbeatRequest{
				JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
			})
			if !errors.Is(err, test.foundationErr) || errors.Is(err, ErrAttemptFenceLost) ||
				errors.Is(err, ErrSourceDeadlineReached) != test.wantSourceCap {
				t.Fatalf("Foundation heartbeat error=%v", err)
			}
		})
	}

	t.Run("database context cancellation", func(t *testing.T) {
		harness := newWorkerServiceHarness(t)
		selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
		if err != nil {
			t.Fatal(err)
		}
		created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
			Actor: SelectionActor{UserID: 200, Role: "admin"}, Selection: selection,
			IdempotencyKey: "heartbeat-database-cancellation", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
		})
		if err != nil {
			t.Fatal(err)
		}
		clock := harness.service.now().UTC()
		coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock }, harness.lease)
		if err != nil {
			t.Fatal(err)
		}
		claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "heartbeat-canceled"})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := coordinator.Heartbeat(ctx, AttemptHeartbeatRequest{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
		}); !errors.Is(err, context.Canceled) || errors.Is(err, ErrAttemptFenceLost) {
			t.Fatalf("canceled heartbeat error=%v", err)
		}
	})
}

type persistentLoaderCancellationContextKey struct{}

type persistentLoaderBoundsContextKey struct{}

func TestPersistentAttemptLoaderBoundsEveryStableTupleQueryBeforeMaterialization(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	created, claim, _ := createClaimedExportForAttemptBudget(
		t, harness, 96, "persistent-loader-stable-tuple-query-bounds",
	)
	var job model.BackupAssetExportJob
	if err := harness.db.Select("max_items").First(&job, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}

	const marker = "stable_tuple_query_bounds"
	ctx := context.WithValue(context.Background(), persistentLoaderBoundsContextKey{}, marker)
	observed := map[string][]int{}
	tracked := map[string]struct{}{
		"backup_asset_export_quota_buckets": {},
		"backup_asset_export_items":         {},
		"backup_asset_export_item_attempts": {},
		"backup_asset_export_reservations":  {},
	}
	const callbackName = "test:persistent-loader-stable-tuple-query-bounds"
	if err := harness.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Context.Value(persistentLoaderBoundsContextKey{}) != marker {
			return
		}
		table := tx.Statement.Schema.Table
		if _, ok := tracked[table]; !ok {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		limit := -1
		if limitClause, ok := tx.Statement.Clauses["LIMIT"]; ok {
			if parsed, ok := limitClause.Expression.(clause.Limit); ok && parsed.Limit != nil {
				limit = *parsed.Limit
			}
		}
		observed[table] = append(observed[table], limit)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = harness.db.Callback().Query().Remove(callbackName) })

	clock := harness.service.now().UTC()
	loader, err := NewPersistentAttemptLoader(
		harness.db,
		backupasset.NewKeyring(harness.db, func() time.Time { return clock }),
		func() time.Time { return clock },
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loader.Load(ctx, PersistentAttemptLoadRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	}); err != nil {
		t.Fatal(err)
	}

	wants := map[string]int{
		"backup_asset_export_quota_buckets": 3,
		"backup_asset_export_items":         job.MaxItems + 1,
		"backup_asset_export_item_attempts": job.MaxItems + 1,
		"backup_asset_export_reservations":  3,
	}
	for table, want := range wants {
		if got := observed[table]; !slices.Equal(got, []int{want, want}) {
			t.Errorf("%s query limits=%v, want [%d %d]", table, got, want, want)
		}
	}
}

func TestPersistentAttemptLoaderRejectsPersistedSourceAuthorityBeforeKeyLookup(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gorm.DB, model.BackupAssetExportSourceLease) error
	}{
		{
			name: "deleted source row",
			mutate: func(db *gorm.DB, source model.BackupAssetExportSourceLease) error {
				return db.Where("id = ?", source.ID).Delete(&model.BackupAssetExportSourceLease{}).Error
			},
		},
		{
			name: "widened source retention",
			mutate: func(db *gorm.DB, source model.BackupAssetExportSourceLease) error {
				if source.RetentionUntil == nil {
					return errors.New("source retention cap is unexpectedly nil")
				}
				return db.Model(&model.BackupAssetExportSourceLease{}).Where("id = ?", source.ID).
					UpdateColumn("retention_until", source.RetentionUntil.Add(time.Minute)).Error
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newWorkerServiceHarness(t)
			created, claim, _ := createClaimedExportForAttemptBudget(
				t, harness, 96, "persistent-loader-source-authority-"+test.name,
			)
			var source model.BackupAssetExportSourceLease
			if err := harness.db.Where("job_id = ?", created.JobID).Take(&source).Error; err != nil {
				t.Fatal(err)
			}
			if err := test.mutate(harness.db, source); err != nil {
				t.Fatal(err)
			}

			clock := harness.service.now().UTC()
			keys := &byVersionKeySpy{Keyring: backupasset.NewKeyring(harness.db, func() time.Time { return clock })}
			loader, err := NewPersistentAttemptLoader(harness.db, keys, func() time.Time { return clock })
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loader.Load(context.Background(), PersistentAttemptLoadRequest{
				JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
			}); !errors.Is(err, ErrAttemptFenceLost) {
				t.Fatalf("source authority error=%v, want ErrAttemptFenceLost", err)
			}
			if len(keys.versions) != 0 {
				t.Fatalf("source authority failure called ByVersion with versions=%v", keys.versions)
			}
		})
	}
}

func TestPersistentAttemptLoaderRejectsInvalidJobCardinalityBeforeItemQueries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*gorm.DB, string, int) error
	}{
		{
			name: "max_items_above_product_cap",
			mutate: func(db *gorm.DB, jobID string, _ int) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
					UpdateColumn("max_items", 100001).Error
			},
		},
		{
			name: "zero_item_count",
			mutate: func(db *gorm.DB, jobID string, _ int) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
					UpdateColumn("item_count", 0).Error
			},
		},
		{
			name: "item_count_above_frozen_max_items",
			mutate: func(db *gorm.DB, jobID string, maxItems int) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
					UpdateColumn("item_count", int64(maxItems)+1).Error
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newWorkerServiceHarness(t)
			created, claim, _ := createClaimedExportForAttemptBudget(
				t, harness, 96, "persistent-loader-invalid-job-cardinality-"+test.name,
			)
			var job model.BackupAssetExportJob
			if err := harness.db.Select("max_items").First(&job, "id = ?", created.JobID).Error; err != nil {
				t.Fatal(err)
			}
			if err := test.mutate(harness.db, created.JobID, job.MaxItems); err != nil {
				t.Fatalf("persist SQL-legal cardinality fixture: %v", err)
			}

			marker := "invalid_job_cardinality_" + test.name
			ctx := context.WithValue(context.Background(), persistentLoaderBoundsContextKey{}, marker)
			itemQueries := 0
			itemAttemptQueries := 0
			callbackName := "test:persistent-loader-invalid-job-cardinality-" + test.name
			if err := harness.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement == nil || tx.Statement.Schema == nil ||
					tx.Statement.Context.Value(persistentLoaderBoundsContextKey{}) != marker {
					return
				}
				switch tx.Statement.Schema.Table {
				case "backup_asset_export_items":
					itemQueries++
				case "backup_asset_export_item_attempts":
					itemAttemptQueries++
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = harness.db.Callback().Query().Remove(callbackName) })

			clock := harness.service.now().UTC()
			loader, err := NewPersistentAttemptLoader(
				harness.db,
				backupasset.NewKeyring(harness.db, func() time.Time { return clock }),
				func() time.Time { return clock },
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loader.Load(ctx, PersistentAttemptLoadRequest{
				JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
			}); !errors.Is(err, ErrUnavailable) {
				t.Errorf("invalid job cardinality error=%v, want ErrUnavailable", err)
			}
			if itemQueries != 0 || itemAttemptQueries != 0 {
				t.Errorf("materialization queries: items=%d item_attempts=%d, want zero", itemQueries, itemAttemptQueries)
			}
		})
	}
}

func TestPersistentAttemptLoaderRejectsSQLLegalCardinalitySentinels(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, serviceHarness, CommitCreateResult, AttemptClaim, model.BackupAssetExportItemAttempt)
	}{
		{
			name: "extra_store_bucket_and_reservation",
			mutate: func(t *testing.T, harness serviceHarness, created CommitCreateResult, _ AttemptClaim, _ model.BackupAssetExportItemAttempt) {
				var reservation model.BackupAssetExportReservation
				if err := harness.db.Where("job_id = ? AND kind = ?", created.JobID, "store").
					Order("id ASC").Take(&reservation).Error; err != nil {
					t.Fatal(err)
				}
				bucketID, err := backupasset.NewOpaqueID()
				if err != nil {
					t.Fatal(err)
				}
				bucket := model.BackupAssetExportQuotaBucket{
					ID: bucketID, Scope: "user", Subject: "197", TransitionRevision: 1,
					CreatedAt: reservation.CreatedAt, UpdatedAt: reservation.CreatedAt,
				}
				if err := harness.db.Create(&bucket).Error; err != nil {
					t.Fatalf("create SQL-legal sentinel quota bucket: %v", err)
				}
				reservation.ID, err = backupasset.NewOpaqueID()
				if err != nil {
					t.Fatal(err)
				}
				reservation.BucketID = bucket.ID
				reservation.State = "released"
				releasedAt := reservation.CreatedAt.UTC()
				reservation.ReleasedAt = &releasedAt
				if err := harness.db.Create(&reservation).Error; err != nil {
					t.Fatalf("create SQL-legal sentinel store reservation: %v", err)
				}
			},
		},
		{
			name: "extra_item",
			mutate: func(t *testing.T, harness serviceHarness, created CommitCreateResult, _ AttemptClaim, _ model.BackupAssetExportItemAttempt) {
				var item model.BackupAssetExportItem
				if err := harness.db.Where("job_id = ?", created.JobID).Take(&item).Error; err != nil {
					t.Fatal(err)
				}
				var err error
				item.ID, err = backupasset.NewOpaqueID()
				if err != nil {
					t.Fatal(err)
				}
				item.Ordinal++
				item.RecoveryPointID = strings.Repeat("d", 32)
				item.EntryID = strings.Repeat("e", 64)
				if err := harness.db.Create(&item).Error; err != nil {
					t.Fatalf("create SQL-legal sentinel item: %v", err)
				}
			},
		},
		{
			name: "extra_current_item_attempt",
			mutate: func(t *testing.T, harness serviceHarness, created CommitCreateResult, _ AttemptClaim, itemAttempt model.BackupAssetExportItemAttempt) {
				var job model.BackupAssetExportJob
				if err := harness.db.First(&job, "id = ?", created.JobID).Error; err != nil {
					t.Fatal(err)
				}
				foreignJobID, err := backupasset.NewOpaqueID()
				if err != nil {
					t.Fatal(err)
				}
				job.ID = foreignJobID
				job.ExecutionState = string(ExecutionQueued)
				job.CurrentAttemptID = nil
				createExportTestJobWithLifecycleSequence(t, harness.db, &job)

				var foreignItem model.BackupAssetExportItem
				if err := harness.db.Where("job_id = ?", created.JobID).Take(&foreignItem).Error; err != nil {
					t.Fatal(err)
				}
				foreignItem.ID, err = backupasset.NewOpaqueID()
				if err != nil {
					t.Fatal(err)
				}
				foreignItem.JobID = job.ID
				foreignItem.CurrentAttemptID = nil
				if err := harness.db.Create(&foreignItem).Error; err != nil {
					t.Fatalf("create SQL-legal sentinel parent item: %v", err)
				}

				itemAttempt.ID, err = backupasset.NewOpaqueID()
				if err != nil {
					t.Fatal(err)
				}
				itemAttempt.ItemID = foreignItem.ID
				if err := harness.db.Create(&itemAttempt).Error; err != nil {
					t.Fatalf("create SQL-legal sentinel item attempt: %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newWorkerServiceHarness(t)
			created, claim, itemAttempt := createClaimedExportForAttemptBudget(
				t, harness, 96, "persistent-loader-cardinality-sentinel-"+test.name,
			)
			test.mutate(t, harness, created, claim, itemAttempt)

			clock := harness.service.now().UTC()
			loader, err := NewPersistentAttemptLoader(
				harness.db,
				backupasset.NewKeyring(harness.db, func() time.Time { return clock }),
				func() time.Time { return clock },
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loader.Load(context.Background(), PersistentAttemptLoadRequest{
				JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
			}); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("SQL-legal %s cardinality sentinel error=%v, want ErrUnavailable", test.name, err)
			}
		})
	}
}

func TestPersistentAttemptLoaderPreservesContextCancellationFromKeyLookup(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	created, claim, _ := createClaimedExportForAttemptBudget(
		t, harness, 96, "persistent-loader-key-cancellation",
	)
	clock := harness.service.now().UTC()
	ctx, cancel := context.WithCancel(context.Background())
	keys := &cancelingByVersionKeySource{cancel: cancel}
	loader, err := NewPersistentAttemptLoader(harness.db, keys, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	_, err = loader.Load(ctx, PersistentAttemptLoadRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	if !keys.called || !errors.Is(err, context.Canceled) {
		t.Fatalf("ByVersion cancellation called=%v error=%v, want context.Canceled", keys.called, err)
	}
}

func TestPersistentAttemptLoaderRejectsForgedFenceDigestBeforeKeyLookup(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	created, claim, _ := createClaimedExportForAttemptBudget(
		t, harness, 96, "persistent-loader-forged-fence-digest",
	)
	if err := harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", claim.AttemptID).
		Update("fence_digest", differentSpoolDigest(claim.FenceDigest)).Error; err != nil {
		t.Fatal(err)
	}
	clock := harness.service.now().UTC()
	keys := &byVersionKeySpy{Keyring: backupasset.NewKeyring(harness.db, func() time.Time { return clock })}
	loader, err := NewPersistentAttemptLoader(harness.db, keys, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}

	_, err = loader.Load(context.Background(), PersistentAttemptLoadRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	if !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("forged fence digest error=%v, want ErrAttemptFenceLost", err)
	}
	if len(keys.versions) != 0 {
		t.Fatalf("forged fence digest reached key lookup: versions=%v", keys.versions)
	}
}

type persistentLoaderKeyringErrorContextKey struct{}

func TestPersistentAttemptLoaderPreservesUnexpectedKeyringDatabaseFailure(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	created, claim, _ := createClaimedExportForAttemptBudget(
		t, harness, 96, "persistent-loader-keyring-database-failure",
	)
	clock := harness.service.now().UTC()
	ring := backupasset.NewKeyring(harness.db, func() time.Time { return clock })
	loader, err := NewPersistentAttemptLoader(harness.db, ring, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected keyring database failure")
	marker := t.Name()
	callbackName := "test:persistent-loader-keyring-database-failure-" + strings.ReplaceAll(marker, "/", "_")
	fired := false
	if err := harness.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != "wrapped_domain_keys" ||
			tx.Statement.Context.Value(persistentLoaderKeyringErrorContextKey{}) != marker {
			return
		}
		fired = true
		_ = tx.AddError(injected)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := harness.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove keyring database callback: %v", err)
		}
	})

	_, err = loader.Load(context.WithValue(context.Background(), persistentLoaderKeyringErrorContextKey{}, marker),
		PersistentAttemptLoadRequest{JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken},
	)
	if !fired {
		t.Fatal("keyring database callback was not reached")
	}
	if !errors.Is(err, injected) || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("keyring database error=%v, want injected infrastructure error", err)
	}
}

func TestPersistentAttemptLoaderPreservesContextCancellationFromDatabaseLoad(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	created, claim, _ := createClaimedExportForAttemptBudget(
		t, harness, 96, "persistent-loader-database-cancellation",
	)
	clock := harness.service.now().UTC()
	ctx, cancel := context.WithCancel(context.WithValue(
		context.Background(), persistentLoaderCancellationContextKey{}, "database_cancellation",
	))
	const callbackName = "test:persistent-loader-cancel-database-load"
	if err := harness.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "backup_asset_export_jobs" ||
			tx.Statement.Context.Value(persistentLoaderCancellationContextKey{}) != "database_cancellation" {
			return
		}
		cancel()
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = harness.db.Callback().Query().Remove(callbackName) })

	ring := backupasset.NewKeyring(harness.db, func() time.Time { return clock })
	loader, err := NewPersistentAttemptLoader(harness.db, ring, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	_, err = loader.Load(ctx, PersistentAttemptLoadRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("database-load cancellation error=%v, want context.Canceled", err)
	}
}

func TestPersistentAttemptLoaderClearsCallerOwnedKeyMaterial(t *testing.T) {
	t.Run("stable snapshot", func(t *testing.T) {
		harness := newWorkerServiceHarness(t)
		created, claim, _ := createClaimedExportForAttemptBudget(
			t, harness, 96, "persistent-loader-clear-key-material-success",
		)
		clock := harness.service.now().UTC()
		keys := &zeroTrackingReadyKeySource{inner: backupasset.NewKeyring(harness.db, func() time.Time { return clock })}
		loader, err := NewPersistentAttemptLoader(harness.db, keys, func() time.Time { return clock })
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := loader.Load(context.Background(), PersistentAttemptLoadRequest{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer clear(snapshot.DEK)
		if len(keys.returned) != 1 || !allZeroBytes(keys.returned[0]) {
			t.Fatalf("stable load retained caller-owned KEK material: calls=%d key=%x", len(keys.returned), keys.returned[0])
		}
	})

	t.Run("retry exhaustion", func(t *testing.T) {
		harness := newWorkerServiceHarness(t)
		created, claim, _ := createClaimedExportForAttemptBudget(
			t, harness, 96, "persistent-loader-clear-key-material-retry",
		)
		clock := harness.service.now().UTC()
		var attempt model.BackupAssetExportAttempt
		if err := harness.db.First(&attempt, "id = ?", claim.AttemptID).Error; err != nil {
			t.Fatal(err)
		}
		keys := &mutatingByVersionKeySource{
			Keyring: backupasset.NewKeyring(harness.db, func() time.Time { return clock }),
			afterLookup: func(call int) error {
				return harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", claim.AttemptID).
					Updates(map[string]any{
						"lease_expires_at": attempt.LeaseExpiresAt.Add(time.Duration(call) * time.Second),
						"updated_at":       attempt.UpdatedAt.Add(time.Duration(call) * time.Second),
					}).Error
			},
		}
		loader, err := NewPersistentAttemptLoader(harness.db, keys, func() time.Time { return clock })
		if err != nil {
			t.Fatal(err)
		}
		if _, err := loader.Load(context.Background(), PersistentAttemptLoadRequest{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
		}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("retry exhaustion error=%v, want ErrUnavailable", err)
		}
		if len(keys.returned) != persistentAttemptStableLoadLimit {
			t.Fatalf("retry caller-owned KEK calls=%d, want %d", len(keys.returned), persistentAttemptStableLoadLimit)
		}
		for index, material := range keys.returned {
			if !allZeroBytes(material) {
				t.Fatalf("retry %d retained caller-owned KEK material: %x", index, material)
			}
		}
	})
}

func TestPersistentAttemptLoaderRejectsCrossJobItemAttemptAndClearsKeyMaterial(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	created, claim, itemAttempt := createClaimedExportForAttemptBudget(
		t, harness, 96, "persistent-loader-cross-job-item-attempt-first",
	)
	second, _, _ := createClaimedExportForAttemptBudget(
		t, harness, 97, "persistent-loader-cross-job-item-attempt-second",
	)
	var foreignItem model.BackupAssetExportItem
	if err := harness.db.Where("job_id = ?", second.JobID).Take(&foreignItem).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", itemAttempt.ID).
		UpdateColumn("item_id", foreignItem.ID).Error; err != nil {
		t.Fatalf("persist foreign but FK-valid item attempt: %v", err)
	}

	clock := harness.service.now().UTC()
	keys := &zeroTrackingReadyKeySource{inner: backupasset.NewKeyring(harness.db, func() time.Time { return clock })}
	loader, err := NewPersistentAttemptLoader(harness.db, keys, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loader.Load(context.Background(), PersistentAttemptLoadRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("cross-job item attempt error=%v, want ErrUnavailable", err)
	}
	if len(keys.returned) != 1 || !allZeroBytes(keys.returned[0]) {
		t.Fatalf("cross-job rejection retained caller-owned KEK material: calls=%d key=%x", len(keys.returned), keys.returned)
	}
}

func TestPersistentAttemptSnapshotValidatesItemMappingsBeforeCopyingDEK(t *testing.T) {
	source, err := os.ReadFile("worker.go")
	if err != nil {
		t.Fatalf("read worker source: %v", err)
	}
	function := persistentAttemptSnapshotSource(t, string(source))
	mappingLoop := strings.Index(function, "for ordinal, row := range persisted.items {")
	copyDEK := strings.Index(function, "append([]byte(nil), dek...)")
	if mappingLoop < 0 || copyDEK < 0 {
		t.Fatalf("persistentAttemptSnapshot required boundaries missing: mapping=%d copy=%d", mappingLoop, copyDEK)
	}
	if mappingLoop > copyDEK || !strings.Contains(function[mappingLoop:copyDEK], "if !found {") {
		t.Fatal("persistentAttemptSnapshot copies the DEK before every item-to-attempt mapping is validated")
	}
	loadFunction := persistentAttemptLoadSnapshotSource(t, string(source))
	clearDEK := strings.Index(loadFunction, "defer clear(dek)")
	snapshotCall := strings.Index(loadFunction, "persistentAttemptSnapshot(reloaded, dek)")
	if clearDEK < 0 || snapshotCall < 0 || clearDEK > snapshotCall {
		t.Fatal("persistent attempt loading can reject a mapping after its unwrapped DEK is no longer registered for zeroization")
	}
}

func persistentAttemptSnapshotSource(t *testing.T, source string) string {
	return workerSourceFunction(t, source, "func persistentAttemptSnapshot(")
}

func persistentAttemptLoadSnapshotSource(t *testing.T, source string) string {
	return workerSourceFunction(t, source, "func (loader *PersistentAttemptLoader) loadSnapshotForAttempt(")
}

func workerSourceFunction(t *testing.T, source, signature string) string {
	t.Helper()
	start := strings.Index(source, signature)
	if start < 0 {
		t.Fatalf("source function %q not found", signature)
	}
	end := strings.Index(source[start+1:], "\nfunc ")
	if end < 0 {
		return source[start:]
	}
	return source[start : start+1+end]
}

func TestPersistentWorkerReloadsAttemptWithRecordedKeyVersionAndItemSessions(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	item := frozenItemFixture()
	item.LogicalSize = 0
	item.ArchiveComponents = []string{"root", "restart.txt"}
	selection, err := FreezeSelection([]FrozenItem{item}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 96, Role: "admin"}, Selection: selection,
		IdempotencyKey: "persistent-worker-reload", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	clock := time.Now().UTC().Truncate(time.Second)
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{
		JobID: created.JobID, WorkerOwner: "worker-restart",
	})
	if err != nil {
		t.Fatal(err)
	}

	ring := backupasset.NewKeyring(harness.db, func() time.Time { return clock })
	rotated, err := ring.Rotate(context.Background(), backupasset.KeyDomainExportStore, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Version != 2 {
		t.Fatalf("rotated key version=%d", rotated.Version)
	}
	keys := &byVersionKeySpy{Keyring: ring}
	loader, err := NewPersistentAttemptLoader(harness.db, keys, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := loader.Load(context.Background(), PersistentAttemptLoadRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys.versions) != 1 || keys.versions[0] != 1 || snapshot.KEKVersion != 1 || len(snapshot.DEK) != 32 {
		t.Fatalf("key versions=%v snapshot=%+v", keys.versions, snapshot)
	}
	if snapshot.JobID != created.JobID || snapshot.AttemptID != claim.AttemptID ||
		snapshot.SelectionDigest != selection.Digest || snapshot.ArchiveFormat != ArchiveZIP || snapshot.ArchiveProfile != "zip_deflate_v1" ||
		snapshot.MaxItemBytes != harness.config.MaxItemBytes {
		t.Fatalf("snapshot identity=%+v", snapshot)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].ItemAttemptID == snapshot.Items[0].ItemID ||
		backupasset.ValidateOpaqueID(snapshot.Items[0].ItemAttemptID) != nil ||
		!slices.Equal(snapshot.Items[0].Frozen.ArchiveComponents, item.ArchiveComponents) ||
		snapshot.Items[0].Frozen.Ref != item.Ref {
		t.Fatalf("snapshot items=%+v", snapshot.Items)
	}

	badFence := append([]byte(nil), claim.FenceToken...)
	badFence[0] ^= 0xff
	if _, err := loader.Load(context.Background(), PersistentAttemptLoadRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: badFence,
	}); !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("bad-fence reload error=%v", err)
	}
}

func TestPersistentAttemptLoaderRetriesStableJobKeyRewrapAcrossBucketAggregateDrift(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	item := frozenItemFixture()
	selection, err := FreezeSelection([]FrozenItem{item}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 96, Role: "admin"}, Selection: selection,
		IdempotencyKey: "persistent-worker-stable-key-rewrap", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := harness.service.now().UTC()
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{
		JobID: created.JobID, WorkerOwner: "worker-stable-key-rewrap",
	})
	if err != nil {
		t.Fatal(err)
	}
	ring := backupasset.NewKeyring(harness.db, func() time.Time { return clock })
	lookupStarted := make(chan byVersionLookup, 1)
	releaseLookup := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseLookup) }) }
	t.Cleanup(release)
	keys := &barrierByVersionKeySource{
		Keyring: ring, lookupStarted: lookupStarted, releaseLookup: releaseLookup,
	}
	loader, err := NewPersistentAttemptLoader(harness.db, keys, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type loadResult struct {
		snapshot PersistentAttemptSnapshot
		err      error
	}
	results := make(chan loadResult, 1)
	go func() {
		snapshot, err := loader.Load(ctx, PersistentAttemptLoadRequest{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
		})
		results <- loadResult{snapshot: snapshot, err: err}
	}()
	lookup := waitByVersionLookup(t, ctx, lookupStarted)
	rotated, dek := rewrapPersistedJobKeyForTest(t, harness.db, ring, created.JobID, lookup.material, clock, func(tx *gorm.DB) error {
		return tx.Model(&model.BackupAssetExportQuotaBucket{}).Where("scope = ?", "user").Updates(map[string]any{
			"active_jobs":          gorm.Expr("active_jobs + 1"),
			"reserved_store_bytes": gorm.Expr("reserved_store_bytes + 1"),
			"transition_revision":  gorm.Expr("transition_revision + 1"),
			"updated_at":           clock.Add(time.Second),
		}).Error
	})
	release()
	loaded := <-results
	if loaded.err != nil {
		t.Fatal(loaded.err)
	}
	if loaded.snapshot.KEKVersion != rotated.Version || !bytes.Equal(loaded.snapshot.DEK, dek) ||
		!slices.Equal(keys.loadedVersions(), []int{lookup.version, rotated.Version}) {
		t.Fatalf("stable rewrap snapshot=%+v key_versions=%v", loaded.snapshot, keys.loadedVersions())
	}
}

func TestPersistentAttemptLoaderRereadsPendingToSkippedProgressDuringKeyLookup(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	created, claim, itemAttempt := createClaimedExportForAttemptBudget(
		t, harness, 96, "persistent-loader-progress-during-key-lookup",
	)
	clock := harness.service.now().UTC()
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	ring := backupasset.NewKeyring(harness.db, func() time.Time { return clock })
	lookupStarted := make(chan byVersionLookup, 1)
	releaseLookup := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseLookup) }) }
	t.Cleanup(release)
	keys := &barrierByVersionKeySource{
		Keyring: ring, lookupStarted: lookupStarted, releaseLookup: releaseLookup,
	}
	loader, err := NewPersistentAttemptLoader(harness.db, keys, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type loadResult struct {
		snapshot PersistentAttemptSnapshot
		err      error
	}
	results := make(chan loadResult, 1)
	go func() {
		snapshot, err := loader.Load(ctx, PersistentAttemptLoadRequest{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
		})
		results <- loadResult{snapshot: snapshot, err: err}
	}()
	lookup := waitByVersionLookup(t, ctx, lookupStarted)
	if err := coordinator.Checkpoint(context.Background(), AttemptCheckpoint{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
		ItemID: itemAttempt.ItemID, State: ItemSkipped, ErrorCategory: "link_metadata_unavailable",
	}); err != nil {
		t.Fatal(err)
	}
	release()
	loaded := <-results
	if loaded.err != nil {
		t.Fatal(loaded.err)
	}
	if len(loaded.snapshot.Items) != 1 || loaded.snapshot.Items[0].State != ItemSkipped ||
		!slices.Equal(keys.loadedVersions(), []int{lookup.version, lookup.version}) {
		t.Fatalf("progress snapshot=%+v key_versions=%v", loaded.snapshot, keys.loadedVersions())
	}
}

func TestPersistentAttemptLoaderRereadsPendingToReadSpoolProgressDuringKeyLookup(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	created, claim, itemAttempt := createClaimedExportForAttemptBudget(
		t, harness, 96, "persistent-loader-read-spool-during-key-lookup",
	)
	clock := harness.service.now().UTC()
	ring := backupasset.NewKeyring(harness.db, func() time.Time { return clock })
	lookupStarted := make(chan byVersionLookup, 1)
	releaseLookup := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseLookup) }) }
	t.Cleanup(release)
	keys := &barrierByVersionKeySource{
		Keyring: ring, lookupStarted: lookupStarted, releaseLookup: releaseLookup,
	}
	loader, err := NewPersistentAttemptLoader(harness.db, keys, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type loadResult struct {
		snapshot PersistentAttemptSnapshot
		err      error
	}
	results := make(chan loadResult, 1)
	go func() {
		snapshot, err := loader.Load(ctx, PersistentAttemptLoadRequest{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
		})
		results <- loadResult{snapshot: snapshot, err: err}
	}()
	lookup := waitByVersionLookup(t, ctx, lookupStarted)
	const (
		spoolDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		spoolSize   = int64(96)
		provider    = int64(17)
	)
	spoolLocator := strings.Repeat("b", 32) + ".xrs"
	progressAt := clock.Add(time.Second)
	if err := harness.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.BackupAssetExportItemAttempt{}).
			Where("id = ? AND state = ?", itemAttempt.ID, ItemPending).
			Updates(map[string]any{
				"state": ItemRead, "spool_digest": spoolDigest, "spool_size": spoolSize,
				"spool_locator": spoolLocator, "logical_bytes": frozenItemFixture().LogicalSize,
				"provider_bytes": provider, "read_at": progressAt,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.New("persist item-attempt read spool progress")
		}
		result = tx.Model(&model.BackupAssetExportItem{}).
			Where("id = ? AND current_attempt_id = ? AND state = ?", itemAttempt.ItemID, claim.AttemptID, ItemPending).
			Updates(map[string]any{
				"state": ItemRead, "logical_bytes": frozenItemFixture().LogicalSize,
				"provider_bytes": provider, "updated_at": progressAt,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.New("persist item projection read spool progress")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	release()
	loaded := <-results
	if loaded.err != nil {
		t.Fatal(loaded.err)
	}
	if len(loaded.snapshot.Items) != 1 || loaded.snapshot.Items[0].State != ItemRead ||
		loaded.snapshot.Items[0].SpoolDigest != spoolDigest || loaded.snapshot.Items[0].SpoolSize != spoolSize ||
		loaded.snapshot.Items[0].SpoolLocator != spoolLocator || loaded.snapshot.Items[0].ProviderBytes != provider ||
		!slices.Equal(keys.loadedVersions(), []int{lookup.version, lookup.version}) {
		t.Fatalf("read spool snapshot=%+v key_versions=%v", loaded.snapshot, keys.loadedVersions())
	}
}

func TestPersistentAttemptLoaderRejectsReadSpoolTerminalDowngradesDuringKeyLookup(t *testing.T) {
	for _, test := range []struct {
		name     string
		state    ItemState
		category string
	}{
		{name: "failed", state: ItemFailed, category: "source_changed"},
		{name: "skipped", state: ItemSkipped, category: ItemErrorLinkMetadataUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newWorkerServiceHarness(t)
			created, claim, itemAttempt := createClaimedExportForAttemptBudget(
				t, harness, 96, "persistent-loader-reject-read-spool-"+test.name,
			)
			var item model.BackupAssetExportItem
			if err := harness.db.First(&item, "id = ?", itemAttempt.ItemID).Error; err != nil {
				t.Fatal(err)
			}
			clock := harness.service.now().UTC()
			const providerBytes = int64(17)
			spoolAt := clock.Add(time.Second)
			if err := harness.db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ? AND state = ?", itemAttempt.ID, ItemPending).
					Updates(map[string]any{
						"state": ItemRead, "spool_digest": strings.Repeat("a", 64), "spool_size": int64(96),
						"spool_locator": strings.Repeat("b", 32) + ".xrs", "logical_bytes": item.LogicalSize,
						"provider_bytes": providerBytes, "read_at": spoolAt,
					}).Error; err != nil {
					return err
				}
				return tx.Model(&model.BackupAssetExportItem{}).
					Where("id = ? AND current_attempt_id = ? AND state = ?", item.ID, claim.AttemptID, ItemPending).
					Updates(map[string]any{
						"state": ItemRead, "logical_bytes": item.LogicalSize, "provider_bytes": providerBytes, "updated_at": spoolAt,
					}).Error
			}); err != nil {
				t.Fatal(err)
			}

			ring := backupasset.NewKeyring(harness.db, func() time.Time { return clock })
			lookupStarted := make(chan byVersionLookup, 1)
			releaseLookup := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseLookup) }) }
			t.Cleanup(release)
			keys := &barrierByVersionKeySource{
				Keyring: ring, lookupStarted: lookupStarted, releaseLookup: releaseLookup,
			}
			loader, err := NewPersistentAttemptLoader(harness.db, keys, func() time.Time { return clock })
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			results := make(chan error, 1)
			go func() {
				_, err := loader.Load(ctx, PersistentAttemptLoadRequest{
					JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
				})
				results <- err
			}()
			_ = waitByVersionLookup(t, ctx, lookupStarted)

			terminalAt := spoolAt.Add(time.Second)
			if err := harness.db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ? AND state = ?", itemAttempt.ID, ItemRead).
					Updates(map[string]any{
						"state": test.state, "logical_bytes": item.LogicalSize, "provider_bytes": providerBytes,
						"error_category": test.category, "finished_at": terminalAt,
					}).Error; err != nil {
					return err
				}
				if err := tx.Model(&model.BackupAssetExportItem{}).
					Where("id = ? AND current_attempt_id = ? AND state = ?", item.ID, claim.AttemptID, ItemRead).
					Updates(map[string]any{
						"state": test.state, "logical_bytes": item.LogicalSize, "provider_bytes": providerBytes,
						"error_category": test.category, "updated_at": terminalAt,
					}).Error; err != nil {
					return err
				}
				if err := tx.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", claim.AttemptID).
					Updates(map[string]any{
						"checkpoint_ordinal": item.Ordinal, "checkpoint_item_count": int64(1),
						"checkpoint_logical_bytes": item.LogicalSize, "checkpoint_provider_bytes": providerBytes, "updated_at": terminalAt,
					}).Error; err != nil {
					return err
				}
				updates := map[string]any{
					"packed_count": int64(0), "skipped_count": int64(0), "failed_count": int64(0),
					"logical_bytes": item.LogicalSize, "provider_bytes": providerBytes,
					"transition_revision": gorm.Expr("transition_revision + 1"), "updated_at": terminalAt,
				}
				if test.state == ItemFailed {
					updates["failed_count"] = int64(1)
				} else {
					updates["skipped_count"] = int64(1)
				}
				return tx.Model(&model.BackupAssetExportJob{}).Where("id = ?", created.JobID).Updates(updates).Error
			}); err != nil {
				t.Fatal(err)
			}
			release()
			if err := <-results; !errors.Is(err, ErrUnavailable) {
				t.Fatalf("read spool -> %s loader error=%v, want ErrUnavailable", test.state, err)
			}
		})
	}
}

func TestPersistentAttemptLoaderRejectsInvalidPreHeaderFailureEvidence(t *testing.T) {
	for _, test := range []struct {
		name          string
		logicalBytes  func(model.BackupAssetExportItem) int64
		category      string
		preserveSpool bool
	}{
		{
			name:          "durable spool retained",
			logicalBytes:  func(model.BackupAssetExportItem) int64 { return 0 },
			category:      "source_changed",
			preserveSpool: true,
		},
		{
			name:         "logical bytes retained",
			logicalBytes: func(item model.BackupAssetExportItem) int64 { return item.LogicalSize },
			category:     "source_changed",
		},
		{
			name:         "non pre-header category",
			logicalBytes: func(model.BackupAssetExportItem) int64 { return 0 },
			category:     "archive_failed",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newWorkerServiceHarness(t)
			created, claim, itemAttempt := createClaimedExportForAttemptBudget(
				t, harness, 96, "persistent-loader-invalid-pre-header-"+strings.ReplaceAll(test.name, " ", "-"),
			)
			var item model.BackupAssetExportItem
			if err := harness.db.First(&item, "id = ?", itemAttempt.ItemID).Error; err != nil {
				t.Fatal(err)
			}
			clock := harness.service.now().UTC()
			const providerBytes = int64(17)
			logicalBytes := test.logicalBytes(item)
			if err := harness.db.Transaction(func(tx *gorm.DB) error {
				attemptUpdates := map[string]any{
					"state": ItemFailed, "logical_bytes": logicalBytes, "provider_bytes": providerBytes,
					"error_category": test.category, "finished_at": clock,
				}
				if test.preserveSpool {
					attemptUpdates["spool_digest"] = strings.Repeat("a", 64)
					attemptUpdates["spool_size"] = int64(96)
					attemptUpdates["spool_locator"] = strings.Repeat("b", 32) + ".xrs"
					attemptUpdates["read_at"] = clock
				}
				if err := tx.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ? AND state = ?", itemAttempt.ID, ItemPending).
					Updates(attemptUpdates).Error; err != nil {
					return err
				}
				if err := tx.Model(&model.BackupAssetExportItem{}).
					Where("id = ? AND current_attempt_id = ? AND state = ?", item.ID, claim.AttemptID, ItemPending).
					Updates(map[string]any{
						"state": ItemFailed, "logical_bytes": logicalBytes, "provider_bytes": providerBytes,
						"error_category": test.category, "updated_at": clock,
					}).Error; err != nil {
					return err
				}
				if err := tx.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", claim.AttemptID).
					Updates(map[string]any{
						"checkpoint_ordinal": item.Ordinal, "checkpoint_item_count": int64(1),
						"checkpoint_logical_bytes": logicalBytes, "checkpoint_provider_bytes": providerBytes, "updated_at": clock,
					}).Error; err != nil {
					return err
				}
				return tx.Model(&model.BackupAssetExportJob{}).Where("id = ?", created.JobID).Updates(map[string]any{
					"failed_count": int64(1), "logical_bytes": logicalBytes, "provider_bytes": providerBytes,
					"transition_revision": gorm.Expr("transition_revision + 1"), "updated_at": clock,
				}).Error
			}); err != nil {
				t.Fatal(err)
			}
			loader, err := NewPersistentAttemptLoader(
				harness.db, backupasset.NewKeyring(harness.db, func() time.Time { return clock }), func() time.Time { return clock },
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loader.Load(context.Background(), PersistentAttemptLoadRequest{
				JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
			}); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("invalid pre-header failure loader error=%v, want ErrUnavailable", err)
			}
		})
	}
}

func TestPersistentAttemptLoaderRejectsPackedRegularFileWithoutReadSpoolEvidence(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	created, claim, itemAttempt := createClaimedExportForAttemptBudget(
		t, harness, 96, "persistent-loader-packed-file-without-read-spool",
	)
	var item model.BackupAssetExportItem
	if err := harness.db.First(&item, "id = ?", itemAttempt.ItemID).Error; err != nil {
		t.Fatal(err)
	}
	now := harness.service.now().UTC()
	const providerBytes = int64(17)
	if err := harness.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.BackupAssetExportItemAttempt{}).
			Where("id = ? AND state = ?", itemAttempt.ID, ItemPending).
			Updates(map[string]any{
				"state": string(ItemPacked), "logical_bytes": item.LogicalSize,
				"provider_bytes": providerBytes, "packed_at": now, "finished_at": now,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return fmt.Errorf("persist packed attempt without read spool: %w", result.Error)
		}
		result = tx.Model(&model.BackupAssetExportItem{}).
			Where("id = ? AND current_attempt_id = ? AND state = ?", item.ID, claim.AttemptID, ItemPending).
			Updates(map[string]any{
				"state": string(ItemPacked), "logical_bytes": item.LogicalSize,
				"provider_bytes": providerBytes, "updated_at": now,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return fmt.Errorf("persist packed item without read spool: %w", result.Error)
		}
		result = tx.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", claim.AttemptID).
			Updates(map[string]any{
				"checkpoint_ordinal": item.Ordinal, "checkpoint_item_count": int64(1),
				"checkpoint_logical_bytes": item.LogicalSize, "checkpoint_provider_bytes": providerBytes,
				"updated_at": now,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return fmt.Errorf("persist packed checkpoint without read spool: %w", result.Error)
		}
		result = tx.Model(&model.BackupAssetExportJob{}).Where("id = ?", created.JobID).
			Updates(map[string]any{
				"packed_count": int64(1), "logical_bytes": item.LogicalSize, "provider_bytes": providerBytes,
				"transition_revision": gorm.Expr("transition_revision + 1"), "updated_at": now,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return fmt.Errorf("persist packed aggregates without read spool: %w", result.Error)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	loader, err := NewPersistentAttemptLoader(
		harness.db,
		backupasset.NewKeyring(harness.db, func() time.Time { return now }),
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loader.Load(context.Background(), PersistentAttemptLoadRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("packed regular file without read spool error=%v, want ErrUnavailable", err)
	}
}

func TestPersistentAttemptLoaderRereadsPendingProviderProgressDuringKeyLookup(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	created, claim, itemAttempt := createClaimedExportForAttemptBudget(
		t, harness, 96, "persistent-loader-provider-progress-during-key-lookup",
	)
	clock := harness.service.now().UTC()
	ring := backupasset.NewKeyring(harness.db, func() time.Time { return clock })
	lookupStarted := make(chan byVersionLookup, 1)
	releaseLookup := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseLookup) }) }
	t.Cleanup(release)
	keys := &barrierByVersionKeySource{
		Keyring: ring, lookupStarted: lookupStarted, releaseLookup: releaseLookup,
	}
	loader, err := NewPersistentAttemptLoader(harness.db, keys, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type loadResult struct {
		snapshot PersistentAttemptSnapshot
		err      error
	}
	results := make(chan loadResult, 1)
	go func() {
		snapshot, err := loader.Load(ctx, PersistentAttemptLoadRequest{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
		})
		results <- loadResult{snapshot: snapshot, err: err}
	}()
	lookup := waitByVersionLookup(t, ctx, lookupStarted)
	const provider = int64(17)
	result := harness.db.Model(&model.BackupAssetExportItemAttempt{}).
		Where("id = ? AND state = ?", itemAttempt.ID, ItemPending).
		UpdateColumn("provider_bytes", provider)
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("persist pending provider progress: error=%v rows=%d", result.Error, result.RowsAffected)
	}
	release()
	loaded := <-results
	if loaded.err != nil {
		t.Fatal(loaded.err)
	}
	if len(loaded.snapshot.Items) != 1 || loaded.snapshot.Items[0].State != ItemPending ||
		loaded.snapshot.Items[0].ProviderBytes != provider ||
		!slices.Equal(keys.loadedVersions(), []int{lookup.version, lookup.version}) {
		t.Fatalf("provider progress snapshot=%+v key_versions=%v", loaded.snapshot, keys.loadedVersions())
	}
}

func TestPersistentAttemptLoaderRereadsProgressWithValidKeyRewrap(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	created, claim, itemAttempt := createClaimedExportForAttemptBudget(
		t, harness, 96, "persistent-loader-progress-with-key-rewrap",
	)
	clock := harness.service.now().UTC()
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	ring := backupasset.NewKeyring(harness.db, func() time.Time { return clock })
	lookupStarted := make(chan byVersionLookup, 1)
	releaseLookup := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseLookup) }) }
	t.Cleanup(release)
	keys := &barrierByVersionKeySource{
		Keyring: ring, lookupStarted: lookupStarted, releaseLookup: releaseLookup,
	}
	loader, err := NewPersistentAttemptLoader(harness.db, keys, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type loadResult struct {
		snapshot PersistentAttemptSnapshot
		err      error
	}
	results := make(chan loadResult, 1)
	go func() {
		snapshot, err := loader.Load(ctx, PersistentAttemptLoadRequest{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
		})
		results <- loadResult{snapshot: snapshot, err: err}
	}()
	lookup := waitByVersionLookup(t, ctx, lookupStarted)
	if err := coordinator.Checkpoint(context.Background(), AttemptCheckpoint{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
		ItemID: itemAttempt.ItemID, State: ItemSkipped, ErrorCategory: "link_metadata_unavailable",
	}); err != nil {
		t.Fatal(err)
	}
	rotated, dek := rewrapPersistedJobKeyForTest(t, harness.db, ring, created.JobID, lookup.material, clock, nil)
	release()
	loaded := <-results
	if loaded.err != nil {
		t.Fatal(loaded.err)
	}
	if len(loaded.snapshot.Items) != 1 || loaded.snapshot.Items[0].State != ItemSkipped ||
		loaded.snapshot.KEKVersion != rotated.Version || !bytes.Equal(loaded.snapshot.DEK, dek) ||
		!slices.Equal(keys.loadedVersions(), []int{lookup.version, rotated.Version}) {
		t.Fatalf("progress rewrap snapshot=%+v key_versions=%v", loaded.snapshot, keys.loadedVersions())
	}
}

func TestPersistentAttemptLoaderRereadsAttemptHeartbeatDuringKeyLookup(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	created, claim, _ := createClaimedExportForAttemptBudget(
		t, harness, 96, "persistent-loader-heartbeat-during-key-lookup",
	)
	var source model.BackupAssetExportSourceLease
	if err := harness.db.Where("job_id = ?", created.JobID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	initialClock := source.RenewedAt.UTC()
	heartbeatClock := initialClock
	sourceLeases, err := backupasset.NewLeaseService(harness.db, func() time.Time { return heartbeatClock }, backupasset.LeaseConfig{
		Duration: harness.config.LeaseTTL, Heartbeat: harness.config.LeaseRenewMargin, AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return heartbeatClock }, sourceLeases)
	if err != nil {
		t.Fatal(err)
	}
	ring := backupasset.NewKeyring(harness.db, func() time.Time { return initialClock })
	lookupStarted := make(chan byVersionLookup, 1)
	releaseLookup := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseLookup) }) }
	t.Cleanup(release)
	keys := &barrierByVersionKeySource{
		Keyring: ring, lookupStarted: lookupStarted, releaseLookup: releaseLookup,
	}
	loader, err := NewPersistentAttemptLoader(harness.db, keys, func() time.Time { return initialClock })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type loadResult struct {
		snapshot PersistentAttemptSnapshot
		err      error
	}
	results := make(chan loadResult, 1)
	go func() {
		snapshot, err := loader.Load(ctx, PersistentAttemptLoadRequest{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
		})
		results <- loadResult{snapshot: snapshot, err: err}
	}()
	lookup := waitByVersionLookup(t, ctx, lookupStarted)
	heartbeatClock = claim.LeaseExpiresAt.Add(-time.Minute)
	heartbeat, err := coordinator.Heartbeat(context.Background(), AttemptHeartbeatRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	release()
	loaded := <-results
	if loaded.err != nil {
		t.Fatal(loaded.err)
	}
	if !loaded.snapshot.AttemptLeaseExpires.Equal(heartbeat.LeaseExpiresAt) ||
		!slices.Equal(keys.loadedVersions(), []int{lookup.version, lookup.version}) {
		t.Fatalf("heartbeat snapshot=%+v heartbeat=%+v key_versions=%v", loaded.snapshot, heartbeat, keys.loadedVersions())
	}
}

func TestPersistentAttemptLoaderReturnsFenceLostForPhaseDriftDuringKeyLookup(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	created, claim, _ := createClaimedExportForAttemptBudget(
		t, harness, 96, "persistent-loader-phase-drift-during-key-lookup",
	)
	clock := harness.service.now().UTC()
	ring := backupasset.NewKeyring(harness.db, func() time.Time { return clock })
	lookupStarted := make(chan byVersionLookup, 1)
	releaseLookup := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseLookup) }) }
	t.Cleanup(release)
	keys := &barrierByVersionKeySource{
		Keyring: ring, lookupStarted: lookupStarted, releaseLookup: releaseLookup,
	}
	loader, err := NewPersistentAttemptLoader(harness.db, keys, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := make(chan error, 1)
	go func() {
		_, err := loader.Load(ctx, PersistentAttemptLoadRequest{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
		})
		results <- err
	}()
	waitByVersionLookup(t, ctx, lookupStarted)
	if err := harness.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", claim.AttemptID).
			Updates(map[string]any{"state": string(AttemptSealing), "updated_at": clock.Add(time.Second)}).Error; err != nil {
			return err
		}
		return tx.Model(&model.BackupAssetExportJob{}).Where("id = ?", created.JobID).
			Updates(map[string]any{
				"execution_state": string(ExecutionSealing), "transition_revision": gorm.Expr("transition_revision + 1"),
				"updated_at": clock.Add(time.Second),
			}).Error
	}); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-results; !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("phase drift error=%v, want ErrAttemptFenceLost", err)
	}
}

func TestPersistentAttemptLoaderBoundsContinualLegalProgressRereads(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	created, claim, _ := createClaimedExportForAttemptBudget(
		t, harness, 96, "persistent-loader-continual-progress-bound",
	)
	clock := harness.service.now().UTC()
	ring := backupasset.NewKeyring(harness.db, func() time.Time { return clock })
	var attempt model.BackupAssetExportAttempt
	if err := harness.db.First(&attempt, "id = ?", claim.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	keys := &mutatingByVersionKeySource{
		Keyring: ring,
		afterLookup: func(call int) error {
			progressAt := attempt.UpdatedAt.Add(time.Duration(call) * time.Second)
			return harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", claim.AttemptID).
				Updates(map[string]any{
					"lease_expires_at": attempt.LeaseExpiresAt.Add(time.Duration(call) * time.Second),
					"updated_at":       progressAt,
				}).Error
		},
	}
	loader, err := NewPersistentAttemptLoader(harness.db, keys, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	_, err = loader.Load(context.Background(), PersistentAttemptLoadRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	if !errors.Is(err, ErrUnavailable) || keys.calls != persistentAttemptStableLoadLimit {
		t.Fatalf("continual progress calls=%d error=%v, want %d lookups and ErrUnavailable",
			keys.calls, err, persistentAttemptStableLoadLimit)
	}
}

func TestPersistentAttemptLoaderRejectsTamperedPersistedArchivePair(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		format  string
		profile string
	}{
		{name: "unknown profile", format: "zip", profile: "future_v2"},
		{name: "zip crossed with tar none", format: "zip", profile: "tar_none_v1"},
		{name: "zip crossed with tar gzip", format: "zip", profile: "tar_gzip_v1"},
		{name: "tar crossed with zip", format: "tar", profile: "zip_deflate_v1"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newWorkerServiceHarness(t)
			created, claim, _ := createClaimedExportForAttemptBudget(
				t, harness, 96, "persistent-loader-tampered-archive-pair-"+strings.ReplaceAll(testCase.name, " ", "-"),
			)
			if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", created.JobID).
				UpdateColumns(map[string]any{
					"archive_format": testCase.format, "archive_profile": testCase.profile,
				}).Error; err != nil {
				t.Fatal(err)
			}
			loader, err := NewPersistentAttemptLoader(
				harness.db,
				backupasset.NewKeyring(harness.db, func() time.Time { return harness.service.now().UTC() }),
				func() time.Time { return harness.service.now().UTC() },
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loader.Load(context.Background(), PersistentAttemptLoadRequest{
				JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
			}); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("tampered pair (%q, %q) error=%v, want ErrUnavailable", testCase.format, testCase.profile, err)
			}
		})
	}
}

func TestPersistentAttemptLoaderRejectsTamperedPersistedSelectionAndLimits(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*gorm.DB, string) error
	}{
		{
			name: "invalid selection schema version",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
					UpdateColumn("selection_schema_version", 2).Error
			},
		},
		{
			name: "invalid limits schema version",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
					UpdateColumn("limits_schema_version", 2).Error
			},
		},
		{
			name: "invalid selection digest encoding",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
					UpdateColumn("selection_digest", strings.Repeat("g", 64)).Error
			},
		},
		{
			name: "regular item exceeds max item bytes",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
					UpdateColumn("max_item_bytes", frozenItemFixture().LogicalSize-1).Error
			},
		},
		{
			name: "item logical size changes digest",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportItem{}).Where("job_id = ?", jobID).
					UpdateColumn("logical_size", frozenItemFixture().LogicalSize+1).Error
			},
		},
		{
			name: "item type changes digest",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportItem{}).Where("job_id = ?", jobID).
					Updates(map[string]any{"entry_type": string(backupasset.CatalogEntryDirectory), "logical_size": 0}).Error
			},
		},
		{
			name: "max items below persisted count",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
					UpdateColumn("max_items", 0).Error
			},
		},
		{
			name: "max source points below persisted sources",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
					UpdateColumn("max_source_points", 0).Error
			},
		},
		{
			name: "max logical bytes below persisted aggregate",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
					UpdateColumn("max_logical_bytes", frozenItemFixture().LogicalSize-1).Error
			},
		},
		{
			name: "invalid chunk bytes",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
					UpdateColumn("chunk_bytes", 0).Error
			},
		},
		{
			name: "max provider bytes below persisted logical aggregate",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
					UpdateColumn("max_provider_bytes", frozenItemFixture().LogicalSize-1).Error
			},
		},
		{
			name: "invalid max ciphertext bytes",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
					UpdateColumn("max_ciphertext_bytes", 0).Error
			},
		},
		{
			name: "peak store sizing overflows",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
					UpdateColumn("max_ciphertext_bytes", int64(math.MaxInt64)).Error
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newWorkerServiceHarness(t)
			created, claim, _ := createClaimedExportForAttemptBudget(
				t, harness, 96, "persistent-loader-selection-limits-"+strings.ReplaceAll(test.name, " ", "-"),
			)
			if err := test.mutate(harness.db, created.JobID); err != nil {
				t.Fatalf("persist legal tamper: %v", err)
			}
			clock := harness.service.now().UTC()
			loader, err := NewPersistentAttemptLoader(
				harness.db,
				backupasset.NewKeyring(harness.db, func() time.Time { return clock }),
				func() time.Time { return clock },
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loader.Load(context.Background(), PersistentAttemptLoadRequest{
				JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
			}); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("tampered persisted selection/limits error=%v, want ErrUnavailable", err)
			}
		})
	}
}

func TestPersistentAttemptLoaderRejectsTamperedStoreReservationsAndBuckets(t *testing.T) {
	firstStoreReservation := func(db *gorm.DB, jobID string) (model.BackupAssetExportReservation, error) {
		var reservation model.BackupAssetExportReservation
		err := db.Where("job_id = ? AND kind = ?", jobID, "store").Order("id ASC").Take(&reservation).Error
		return reservation, err
	}
	harnessClock := func(db *gorm.DB, jobID string) (time.Time, error) {
		var job model.BackupAssetExportJob
		if err := db.Select("created_at").First(&job, "id = ?", jobID).Error; err != nil {
			return time.Time{}, err
		}
		return job.CreatedAt.UTC(), nil
	}
	tests := []struct {
		name   string
		mutate func(*gorm.DB, string) error
	}{
		{
			name: "store reservation amount changes",
			mutate: func(db *gorm.DB, jobID string) error {
				reservation, err := firstStoreReservation(db, jobID)
				if err != nil {
					return err
				}
				return db.Model(&reservation).UpdateColumn("reserved_store_bytes", reservation.ReservedStoreBytes+1).Error
			},
		},
		{
			name: "store reservation amount is zero",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportReservation{}).
					Where("job_id = ? AND kind = ?", jobID, "store").
					UpdateColumn("reserved_store_bytes", 0).Error
			},
		},
		{
			name: "store reservation is missing",
			mutate: func(db *gorm.DB, jobID string) error {
				reservation, err := firstStoreReservation(db, jobID)
				if err != nil {
					return err
				}
				return db.Delete(&reservation).Error
			},
		},
		{
			name: "extra historical store reservation exists",
			mutate: func(db *gorm.DB, jobID string) error {
				reservation, err := firstStoreReservation(db, jobID)
				if err != nil {
					return err
				}
				reservation.ID, err = backupasset.NewOpaqueID()
				if err != nil {
					return err
				}
				releasedAt, err := harnessClock(db, jobID)
				if err != nil {
					return err
				}
				reservation.State = "released"
				reservation.ReleasedAt = &releasedAt
				reservation.UpdatedAt = releasedAt
				return db.Create(&reservation).Error
			},
		},
		{
			name: "store reservation is purge pending",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportReservation{}).
					Where("job_id = ? AND kind = ?", jobID, "store").
					UpdateColumn("state", "purge_pending").Error
			},
		},
		{
			name: "store reservation is released",
			mutate: func(db *gorm.DB, jobID string) error {
				releasedAt, err := harnessClock(db, jobID)
				if err != nil {
					return err
				}
				return db.Model(&model.BackupAssetExportReservation{}).
					Where("job_id = ? AND kind = ?", jobID, "store").
					Updates(map[string]any{"state": "released", "released_at": releasedAt, "updated_at": releasedAt}).Error
			},
		},
		{
			name: "store reservation is expired",
			mutate: func(db *gorm.DB, jobID string) error {
				expiredAt, err := harnessClock(db, jobID)
				if err != nil {
					return err
				}
				return db.Model(&model.BackupAssetExportReservation{}).
					Where("job_id = ? AND kind = ?", jobID, "store").
					Updates(map[string]any{
						"state": "expired", "lease_expires_at": expiredAt,
						"released_at": expiredAt, "updated_at": expiredAt,
					}).Error
			},
		},
		{
			name: "store reservation lease owner changes",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportReservation{}).
					Where("job_id = ? AND kind = ?", jobID, "store").
					UpdateColumn("lease_owner", strings.Repeat("f", 32)).Error
			},
		},
		{
			name: "store reservation job changes",
			mutate: func(db *gorm.DB, jobID string) error {
				reservation, err := firstStoreReservation(db, jobID)
				if err != nil {
					return err
				}
				return db.Model(&reservation).UpdateColumn("job_id", strings.Repeat("e", 32)).Error
			},
		},
		{
			name: "store reservation carries attempt",
			mutate: func(db *gorm.DB, jobID string) error {
				var attempt model.BackupAssetExportAttempt
				if err := db.Where("job_id = ? AND is_current = ?", jobID, true).Take(&attempt).Error; err != nil {
					return err
				}
				return db.Model(&model.BackupAssetExportReservation{}).
					Where("job_id = ? AND kind = ?", jobID, "store").UpdateColumn("attempt_id", attempt.ID).Error
			},
		},
		{
			name: "store reservation carries slots",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportReservation{}).
					Where("job_id = ? AND kind = ?", jobID, "store").UpdateColumn("reserved_slots", 1).Error
			},
		},
		{
			name: "store reservation carries logical bytes",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportReservation{}).
					Where("job_id = ? AND kind = ?", jobID, "store").UpdateColumn("reserved_logical_bytes", 1).Error
			},
		},
		{
			name: "store reservation carries provider bytes",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportReservation{}).
					Where("job_id = ? AND kind = ?", jobID, "store").UpdateColumn("reserved_provider_bytes", 1).Error
			},
		},
		{
			name: "store reservation carries cipher bytes",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportReservation{}).
					Where("job_id = ? AND kind = ?", jobID, "store").
					UpdateColumn("reserved_cipher_bytes", 1).Error
			},
		},
		{
			name: "store reservation deadline changes",
			mutate: func(db *gorm.DB, jobID string) error {
				var job model.BackupAssetExportJob
				if err := db.First(&job, "id = ?", jobID).Error; err != nil {
					return err
				}
				return db.Model(&model.BackupAssetExportReservation{}).
					Where("job_id = ? AND kind = ?", jobID, "store").
					UpdateColumn("lease_expires_at", job.AbsoluteDeadline.Add(time.Second)).Error
			},
		},
		{
			name: "active store reservation has released timestamp",
			mutate: func(db *gorm.DB, jobID string) error {
				return db.Model(&model.BackupAssetExportReservation{}).
					Where("job_id = ? AND kind = ?", jobID, "store").
					UpdateColumn("released_at", time.Now().UTC()).Error
			},
		},
		{
			name: "store reservation bucket changes",
			mutate: func(db *gorm.DB, jobID string) error {
				reservation, err := firstStoreReservation(db, jobID)
				if err != nil {
					return err
				}
				return db.Model(&reservation).UpdateColumn("bucket_id", strings.Repeat("d", 32)).Error
			},
		},
		{
			name: "user bucket scope changes",
			mutate: func(db *gorm.DB, _ string) error {
				return db.Model(&model.BackupAssetExportQuotaBucket{}).
					Where("scope = ?", "user").UpdateColumn("scope", "tenant").Error
			},
		},
		{
			name: "user bucket subject changes",
			mutate: func(db *gorm.DB, _ string) error {
				return db.Model(&model.BackupAssetExportQuotaBucket{}).
					Where("scope = ?", "user").UpdateColumn("subject", "97").Error
			},
		},
		{
			name: "user bucket subject is noncanonical",
			mutate: func(db *gorm.DB, _ string) error {
				return db.Model(&model.BackupAssetExportQuotaBucket{}).
					Where("scope = ?", "user").UpdateColumn("subject", "096").Error
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newWorkerServiceHarness(t)
			created, claim, _ := createClaimedExportForAttemptBudget(
				t, harness, 96, "persistent-loader-store-reservation-"+strings.ReplaceAll(test.name, " ", "-"),
			)
			if err := test.mutate(harness.db, created.JobID); err != nil {
				t.Fatalf("persist legal store reservation tamper: %v", err)
			}
			clock := harness.service.now().UTC()
			loader, err := NewPersistentAttemptLoader(
				harness.db,
				backupasset.NewKeyring(harness.db, func() time.Time { return clock }),
				func() time.Time { return clock },
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loader.Load(context.Background(), PersistentAttemptLoadRequest{
				JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
			}); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("tampered persisted store reservation error=%v, want ErrUnavailable", err)
			}
		})
	}
}

func TestPersistentAttemptLoaderEnforcesFrozenCiphertextArchiveBoundary(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	created, claim, _ := createClaimedExportForAttemptBudget(
		t, harness, 96, "persistent-loader-frozen-ciphertext-boundary",
	)
	var job model.BackupAssetExportJob
	if err := harness.db.First(&job, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	required := expectedArchiveCiphertextCapacityV1(t, job.MaxLogicalBytes, job.MaxItems, job.ChunkBytes)
	if required <= 1 {
		t.Fatalf("invalid independent fixture boundary=%d", required)
	}
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, SelectionLimits{
		MaxItems: job.MaxItems, MaxSourcePoints: job.MaxSourcePoints, MaxLogicalBytes: job.MaxLogicalBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	setCiphertextCapacity := func(maxCiphertextBytes int64) {
		t.Helper()
		peakStoreBytes, err := createPeakStoreBytes(selection.Items, ServiceConfig{
			ChunkBytes: job.ChunkBytes, MaxItemBytes: job.MaxItemBytes, MaxCiphertextBytes: maxCiphertextBytes,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := harness.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.BackupAssetExportJob{}).Where("id = ?", created.JobID).
				UpdateColumn("max_ciphertext_bytes", maxCiphertextBytes).Error; err != nil {
				return err
			}
			return tx.Model(&model.BackupAssetExportReservation{}).
				Where("job_id = ? AND kind = ?", created.JobID, "store").
				UpdateColumn("reserved_store_bytes", peakStoreBytes).Error
		}); err != nil {
			t.Fatal(err)
		}
	}
	setCiphertextCapacity(required - 1)
	clock := harness.service.now().UTC()
	loader, err := NewPersistentAttemptLoader(
		harness.db,
		backupasset.NewKeyring(harness.db, func() time.Time { return clock }),
		func() time.Time { return clock },
	)
	if err != nil {
		t.Fatal(err)
	}
	request := PersistentAttemptLoadRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	}
	if _, err := loader.Load(context.Background(), request); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ciphertext boundary-1 error=%v, want ErrUnavailable", err)
	}
	setCiphertextCapacity(required)
	snapshot, err := loader.Load(context.Background(), request)
	if err != nil || snapshot.MaxCiphertextBytes != required {
		t.Fatalf("exact ciphertext boundary snapshot=%+v err=%v", snapshot, err)
	}
}

func TestPersistentWorkerRejectsSecurityTupleDriftBeforeSpoolPersistence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*gorm.DB, string) error
	}{
		{
			name: "archive_format_profile",
			mutate: func(tx *gorm.DB, jobID string) error {
				return tx.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
					Updates(map[string]any{"archive_format": string(ArchiveTAR), "archive_profile": "tar_none_v1"}).Error
			},
		},
		{
			name: "chunk_bytes",
			mutate: func(tx *gorm.DB, jobID string) error {
				return tx.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
					UpdateColumn("chunk_bytes", 32768).Error
			},
		},
		{
			name: "store_reservation",
			mutate: func(tx *gorm.DB, jobID string) error {
				return tx.Model(&model.BackupAssetExportReservation{}).
					Where("job_id = ? AND kind = ?", jobID, "store").
					UpdateColumn("reserved_store_bytes", gorm.Expr("reserved_store_bytes + 1")).Error
			},
		},
		{
			name: "store_reservation_state",
			mutate: func(tx *gorm.DB, jobID string) error {
				return tx.Model(&model.BackupAssetExportReservation{}).
					Where("job_id = ? AND kind = ?", jobID, "store").
					UpdateColumn("state", "purge_pending").Error
			},
		},
		{
			name: "store_bucket_identity",
			mutate: func(tx *gorm.DB, _ string) error {
				return tx.Model(&model.BackupAssetExportQuotaBucket{}).
					Where("scope = ?", "user").UpdateColumn("subject", "97").Error
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newWorkerServiceHarness(t)
			item := frozenItemFixture()
			selection, err := FreezeSelection([]FrozenItem{item}, nil, harness.config.Selection)
			if err != nil {
				t.Fatal(err)
			}
			created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
				Actor: SelectionActor{UserID: 96, Role: "admin"}, Selection: selection,
				IdempotencyKey: "persistent-worker-security-tuple-drift-" + test.name,
				ArchiveFormat:  ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
			})
			if err != nil {
				t.Fatal(err)
			}
			clock := harness.service.now().UTC()
			coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
			if err != nil {
				t.Fatal(err)
			}
			claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{
				JobID: created.JobID, WorkerOwner: "worker-security-tuple-drift",
			})
			if err != nil {
				t.Fatal(err)
			}
			var itemAttempt model.BackupAssetExportItemAttempt
			if err := harness.db.Where("job_id = ? AND attempt_id = ?", created.JobID, claim.AttemptID).
				Take(&itemAttempt).Error; err != nil {
				t.Fatal(err)
			}
			ring := backupasset.NewKeyring(harness.db, func() time.Time { return clock })
			lookupStarted := make(chan byVersionLookup, 1)
			releaseLookup := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseLookup) }) }
			t.Cleanup(release)
			keys := &barrierByVersionKeySource{
				Keyring: ring, lookupStarted: lookupStarted, releaseLookup: releaseLookup,
			}
			broker := &workerBrokerFake{data: bytes.Repeat([]byte("s"), int(item.LogicalSize))}
			store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			worker, err := NewPersistentWorker(PersistentWorkerDependencies{
				DB: harness.db, Keys: keys, Broker: broker, Metadata: &metadataValidatorFake{}, Store: store,
				AttemptWork: NewAttemptWorkRegistry(),
				Now:         func() time.Time { return clock },
			})
			if err != nil {
				t.Fatal(err)
			}
			beforeStore := snapshotExportStoreTree(t, store.root)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			type spoolResult struct {
				result PersistentSpoolResult
				err    error
			}
			results := make(chan spoolResult, 1)
			go func() {
				result, err := worker.SpoolItem(ctx, PersistentSpoolItemRequest{
					JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: itemAttempt.ItemID,
				})
				results <- spoolResult{result: result, err: err}
			}()
			lookup := waitByVersionLookup(t, ctx, lookupStarted)
			_, _ = rewrapPersistedJobKeyForTest(t, harness.db, ring, created.JobID, lookup.material, clock, func(tx *gorm.DB) error {
				return test.mutate(tx, created.JobID)
			})
			release()
			spooled := <-results
			if !errors.Is(spooled.err, ErrUnavailable) {
				t.Fatalf("security tuple drift result=%+v err=%v, want ErrUnavailable", spooled.result, spooled.err)
			}
			if broker.opens != 0 {
				t.Fatalf("security tuple drift opened source %d times", broker.opens)
			}
			assertExportStoreTreeUnchanged(t, store.root, beforeStore)
			if err := harness.db.First(&itemAttempt, "id = ?", itemAttempt.ID).Error; err != nil {
				t.Fatal(err)
			}
			if itemAttempt.State != string(ItemPending) || itemAttempt.SpoolLocator != "" || itemAttempt.SpoolSize != 0 {
				t.Fatalf("security tuple drift persisted spool metadata: %+v", itemAttempt)
			}
		})
	}
}

func TestPersistentWorkerSpoolsEncryptedBeforeArchiveHeaderWithDurableItemSession(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	item := frozenItemFixture()
	payload := bytes.Repeat([]byte("x"), int(item.LogicalSize))
	selection, err := FreezeSelection([]FrozenItem{item}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 97, Role: "admin"}, Selection: selection,
		IdempotencyKey: "persistent-worker-spool", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Second)
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{
		JobID: created.JobID, WorkerOwner: "worker-spool",
	})
	if err != nil {
		t.Fatal(err)
	}
	var itemAttempt model.BackupAssetExportItemAttempt
	if err := harness.db.Where("job_id = ? AND attempt_id = ?", created.JobID, claim.AttemptID).Take(&itemAttempt).Error; err != nil {
		t.Fatal(err)
	}

	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	source := &persistentSourceResolverFake{payload: payload, providerBytes: item.LogicalSize - 5}
	contentBroker, err := content.NewAttemptBroker(source, budget, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	recordingBroker := &recordingAttemptBroker{inner: contentBroker}
	metadata := &metadataValidatorFake{}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ring := backupasset.NewKeyring(harness.db, func() time.Time { return clock })
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: harness.db, Keys: ring, Broker: recordingBroker, Metadata: metadata, Store: store,
		AttemptWork: NewAttemptWorkRegistry(),
		Now:         func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	spool, err := worker.SpoolItem(context.Background(), PersistentSpoolItemRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: itemAttempt.ItemID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recordingBroker.bindings) != 1 {
		t.Fatalf("content bindings=%d", len(recordingBroker.bindings))
	}
	binding := recordingBroker.bindings[0]
	if binding.SessionID != itemAttempt.ID || binding.Limits.MaxRequests != 3 || binding.Limits.MaxInFlight != 1 ||
		binding.Limits.MaxBytesPerRequest != item.LogicalSize || binding.Limits.MaxCumulativeBytes != item.LogicalSize {
		t.Fatalf("content binding=%+v item_attempt=%s", binding, itemAttempt.ID)
	}
	if len(source.requests) != 3 || source.requests[0].Mode != content.SourceModeStat ||
		source.requests[1].Mode != content.SourceModeSequential || source.requests[2].Mode != content.SourceModeStat {
		t.Fatalf("source requests=%+v", source.requests)
	}
	if len(metadata.items) != 1 || metadata.items[0].Ref != item.Ref {
		t.Fatalf("metadata revalidation=%+v", metadata.items)
	}
	if err := harness.db.First(&itemAttempt, "id = ?", itemAttempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if itemAttempt.State != string(ItemRead) || itemAttempt.SpoolLocator != spool.Locator ||
		itemAttempt.SpoolSize != spool.CiphertextBytes || itemAttempt.SpoolDigest != spool.CiphertextDigest ||
		itemAttempt.LogicalBytes != item.LogicalSize || itemAttempt.ProviderBytes != source.providerBytes || itemAttempt.ReadAt == nil {
		t.Fatalf("persisted item attempt=%+v spool=%+v", itemAttempt, spool)
	}
	reader, err := store.OpenSealed(spool.Locator)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			t.Errorf("close reloaded spool: %v", err)
		}
	}()
	loader, err := NewPersistentAttemptLoader(harness.db, ring, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := loader.Load(context.Background(), PersistentAttemptLoadRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded bytes.Buffer
	if _, err := DecryptStream(context.Background(), &decoded, reader, reloaded.DEK, CipherBinding{
		ExportID: created.JobID, SelectionDigest: selection.Digest,
		ArchiveProfile: "zip_deflate_v1", FormatVersion: 1, AttemptFenceDigest: claim.FenceDigest,
		Purpose: CipherPurposeItemSpool, ObjectID: itemAttempt.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.Bytes(), payload) {
		t.Fatal("authenticated spool payload mismatch")
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".xre" {
			t.Fatalf("final archive was created before spool validation: %s", entry.Name())
		}
	}
}

type persistentSpoolPersistenceErrorContextKey struct{}

func TestPersistentWorkerClassifiesReadSpoolPersistenceDatabaseFailureAsPreHeaderFailure(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	item := frozenItemFixture()
	selection, err := FreezeSelection([]FrozenItem{item}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 97, Role: "admin"}, Selection: selection,
		IdempotencyKey: "persistent-worker-spool-persistence-database-failure", ArchiveFormat: ArchiveZIP,
		ArchiveProfile: ArchiveProfileZIPDeflateV1,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Second)
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-spool-db-error"})
	if err != nil {
		t.Fatal(err)
	}
	var itemAttempt model.BackupAssetExportItemAttempt
	if err := harness.db.Where("job_id = ? AND attempt_id = ?", created.JobID, claim.AttemptID).Take(&itemAttempt).Error; err != nil {
		t.Fatal(err)
	}
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	broker, err := content.NewAttemptBroker(&persistentSourceResolverFake{
		payload: bytes.Repeat([]byte("s"), int(item.LogicalSize)), providerBytes: item.LogicalSize,
	}, budget, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	metadata := &metadataValidatorFake{}
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: harness.db, Keys: backupasset.NewKeyring(harness.db, func() time.Time { return clock }),
		Broker: broker, Metadata: metadata, Store: store, AttemptWork: NewAttemptWorkRegistry(),
		Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected spool persistence database failure")
	marker := t.Name()
	callbackName := "test:spool-persistence-database-failure-" + strings.ReplaceAll(marker, "/", "_")
	armed := false
	metadata.before = func() { armed = true }
	fired := false
	if err := harness.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != "backup_asset_export_item_attempts" ||
			tx.Statement.Context.Value(persistentSpoolPersistenceErrorContextKey{}) != marker || !armed || fired {
			return
		}
		fired = true
		_ = tx.AddError(injected)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := harness.db.Callback().Update().Remove(callbackName); err != nil {
			t.Errorf("remove spool persistence callback: %v", err)
		}
	})

	_, err = worker.SpoolItem(context.WithValue(context.Background(), persistentSpoolPersistenceErrorContextKey{}, marker),
		PersistentSpoolItemRequest{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: itemAttempt.ItemID,
		},
	)
	if !fired {
		t.Fatal("spool persistence callback was not reached")
	}
	var failure *PreHeaderSpoolFailure
	if !errors.As(err, &failure) || failure.ProviderBytes() != -1 || !errors.Is(err, injected) || errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("spool persistence error=%v, want pre-header injected infrastructure failure", err)
	}
	if err := harness.db.First(&itemAttempt, "id = ?", itemAttempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if itemAttempt.State != string(ItemPending) || itemAttempt.SpoolLocator != "" || itemAttempt.SpoolDigest != "" || itemAttempt.SpoolSize != 0 {
		t.Fatalf("spool persistence failure changed durable spool state: %+v", itemAttempt)
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stage-") || filepath.Ext(entry.Name()) == ".xrs" || filepath.Ext(entry.Name()) == ".xre" {
			t.Fatalf("spool persistence failure left object=%s", entry.Name())
		}
	}
}

func TestPersistentWorkerSpoolsZeroByteFileWithStatOnlyAccountingAndPublishesReady(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	item := frozenItemFixture()
	item.LogicalSize = 0
	item.ArchiveComponents = []string{"root", "empty.txt"}
	selection, err := FreezeSelection([]FrozenItem{item}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 97, Role: "admin"}, Selection: selection,
		IdempotencyKey: "persistent-worker-zero-byte", ArchiveFormat: ArchiveZIP, ArchiveProfile: ArchiveProfileZIPDeflateV1,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Second)
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{
		JobID: created.JobID, WorkerOwner: "worker-zero-byte",
	})
	if err != nil {
		t.Fatal(err)
	}
	var itemAttempt model.BackupAssetExportItemAttempt
	if err := harness.db.Where("job_id = ? AND attempt_id = ?", created.JobID, claim.AttemptID).
		Take(&itemAttempt).Error; err != nil {
		t.Fatal(err)
	}

	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	source := &persistentSourceResolverFake{}
	contentBroker, err := content.NewAttemptBroker(source, budget, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	recordingBroker := &recordingAttemptBroker{inner: contentBroker}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ring := backupasset.NewKeyring(harness.db, func() time.Time { return clock })
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: harness.db, Keys: ring, Broker: recordingBroker, Metadata: &metadataValidatorFake{}, Store: store,
		AttemptWork: NewAttemptWorkRegistry(),
		Now:         func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}

	spool, err := worker.SpoolItem(context.Background(), PersistentSpoolItemRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: itemAttempt.ItemID,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCiphertextBytes, err := ciphertextSizeV1(0, harness.config.ChunkBytes)
	if err != nil {
		t.Fatal(err)
	}
	if wantCiphertextBytes != 88 || spool.LogicalBytes != 0 || spool.ProviderBytes != 0 ||
		spool.CiphertextBytes != wantCiphertextBytes {
		t.Fatalf("zero-byte spool=%+v want ciphertext=%d", spool, wantCiphertextBytes)
	}
	if len(recordingBroker.bindings) != 1 {
		t.Fatalf("zero-byte bindings=%d", len(recordingBroker.bindings))
	}
	binding := recordingBroker.bindings[0]
	if !slices.Equal(binding.AllowedModes, []content.SourceMode{content.SourceModeStat}) ||
		binding.Limits.MaxBytesPerRequest != 1 || binding.Limits.MaxCumulativeBytes != 1 ||
		binding.Limits.MaxRequests != 2 || binding.Limits.MaxInFlight != 1 {
		t.Fatalf("zero-byte binding=%+v", binding)
	}
	if len(source.requests) != 2 || source.requests[0].Mode != content.SourceModeStat ||
		source.requests[1].Mode != content.SourceModeStat {
		t.Fatalf("zero-byte source requests=%+v", source.requests)
	}
	if err := harness.db.First(&itemAttempt, "id = ?", itemAttempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if itemAttempt.State != string(ItemRead) || itemAttempt.LogicalBytes != 0 || itemAttempt.ProviderBytes != 0 ||
		itemAttempt.SpoolSize != wantCiphertextBytes || itemAttempt.SpoolLocator != spool.Locator {
		t.Fatalf("persisted zero-byte item attempt=%+v", itemAttempt)
	}

	loader, err := NewPersistentAttemptLoader(harness.db, ring, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := loader.Load(context.Background(), PersistentAttemptLoadRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := store.OpenSealed(spool.Locator)
	if err != nil {
		t.Fatal(err)
	}
	var plaintext bytes.Buffer
	_, decryptErr := DecryptStream(context.Background(), &plaintext, reader, snapshot.DEK, CipherBinding{
		ExportID: created.JobID, SelectionDigest: selection.Digest, ArchiveProfile: ArchiveProfileZIPDeflateV1,
		FormatVersion: 1, AttemptFenceDigest: claim.FenceDigest, Purpose: CipherPurposeItemSpool, ObjectID: itemAttempt.ID,
	})
	closeErr := reader.Close()
	if decryptErr != nil || closeErr != nil || plaintext.Len() != 0 {
		t.Fatalf("decrypt zero-byte spool len=%d err=%v", plaintext.Len(), errors.Join(decryptErr, closeErr))
	}

	sealed, err := worker.SealArchive(context.Background(), PersistentSealRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Report.Packed != 1 || sealed.Report.LogicalBytes != 0 || sealed.Report.ProviderBytes != 0 {
		t.Fatalf("zero-byte sealed report=%+v", sealed.Report)
	}
	published, err := worker.PublishReady(context.Background(), PersistentPublishRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ArtifactID: sealed.ArtifactID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var job model.BackupAssetExportJob
	if err := harness.db.First(&job, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(ExecutionReady) || job.LogicalBytes != 0 || job.ProviderBytes != 0 ||
		published.ArtifactID != sealed.ArtifactID {
		t.Fatalf("zero-byte ready job=%+v published=%+v", job, published)
	}
}

func TestPersistentWorkerDiscardsSpoolBeforeHeaderWhenPostReadSourceDrifts(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	item := frozenItemFixture()
	payload := bytes.Repeat([]byte("y"), int(item.LogicalSize))
	selection, err := FreezeSelection([]FrozenItem{item}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 98, Role: "admin"}, Selection: selection,
		IdempotencyKey: "persistent-worker-drift", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Second)
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-drift"})
	if err != nil {
		t.Fatal(err)
	}
	var itemAttempt model.BackupAssetExportItemAttempt
	if err := harness.db.Where("job_id = ? AND attempt_id = ?", created.JobID, claim.AttemptID).Take(&itemAttempt).Error; err != nil {
		t.Fatal(err)
	}
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	source := &persistentSourceResolverFake{payload: payload, providerBytes: item.LogicalSize, driftRead: true}
	contentBroker, err := content.NewAttemptBroker(source, budget, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	metadata := &metadataValidatorFake{}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: harness.db, Keys: backupasset.NewKeyring(harness.db, func() time.Time { return clock }),
		Broker: contentBroker, Metadata: metadata, Store: store, AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.SpoolItem(context.Background(), PersistentSpoolItemRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: itemAttempt.ItemID,
	}); !errors.Is(err, content.ErrAttemptSourceChanged) {
		t.Fatalf("post-read drift error=%v", err)
	} else {
		var failure *PreHeaderSpoolFailure
		if !errors.As(err, &failure) || failure.ProviderBytes() != -1 {
			t.Fatalf("post-read drift did not retain pre-header failure marker: %v", err)
		}
	}
	if len(metadata.items) != 0 {
		t.Fatalf("metadata ran after source drift: %+v", metadata.items)
	}
	if err := harness.db.First(&itemAttempt, "id = ?", itemAttempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if itemAttempt.State != string(ItemPending) || itemAttempt.SpoolLocator != "" || itemAttempt.SpoolSize != 0 || itemAttempt.ReadAt != nil {
		t.Fatalf("drift persisted trusted spool: %+v", itemAttempt)
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stage-") || filepath.Ext(entry.Name()) == ".xrs" || filepath.Ext(entry.Name()) == ".xre" {
			t.Fatalf("drift left artifact=%s", entry.Name())
		}
	}
}

func TestPersistentWorkerDoesNotSealWhenEveryRegularItemFailedBeforeHeader(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	item := frozenItemFixture()
	selection, err := FreezeSelection([]FrozenItem{item}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 98, Role: "admin"}, Selection: selection,
		IdempotencyKey: "persistent-worker-all-pre-header-failed", ArchiveFormat: ArchiveZIP, ArchiveProfile: ArchiveProfileZIPDeflateV1,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Second)
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-all-pre-header-failed"})
	if err != nil {
		t.Fatal(err)
	}
	var itemAttempt model.BackupAssetExportItemAttempt
	if err := harness.db.Where("job_id = ? AND attempt_id = ?", created.JobID, claim.AttemptID).Take(&itemAttempt).Error; err != nil {
		t.Fatal(err)
	}
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	source := &persistentSourceResolverFake{
		payload: bytes.Repeat([]byte("y"), int(item.LogicalSize)), providerBytes: item.LogicalSize, driftRead: true,
	}
	broker, err := content.NewAttemptBroker(source, budget, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: harness.db, Keys: backupasset.NewKeyring(harness.db, func() time.Time { return clock }), Broker: broker,
		Metadata: &metadataValidatorFake{}, Store: store, AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.SpoolItem(context.Background(), PersistentSpoolItemRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: itemAttempt.ItemID,
	}); !errors.Is(err, content.ErrAttemptSourceChanged) {
		t.Fatalf("spool error=%v", err)
	}
	if err := harness.db.First(&itemAttempt, "id = ?", itemAttempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Checkpoint(context.Background(), AttemptCheckpoint{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: itemAttempt.ItemID,
		State: ItemFailed, ProviderBytes: itemAttempt.ProviderBytes, ErrorCategory: "source_changed",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.SealArchive(context.Background(), PersistentSealRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	}); !errors.Is(err, ErrArchiveLimit) {
		t.Fatalf("all-failed seal error=%v", err)
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".xre" {
			t.Fatalf("all-failed attempt sealed %q", entry.Name())
		}
	}
}

func TestPersistentWorkerRejectsSealedReportThatDowngradesReadSpool(t *testing.T) {
	for _, test := range []struct {
		name                    string
		state                   ItemState
		category                string
		preHeaderSpoolRecovered bool
	}{
		{name: "failed", state: ItemFailed, category: "source_changed"},
		{name: "forged recovered source change", state: ItemFailed, category: "source_changed", preHeaderSpoolRecovered: true},
		{name: "skipped", state: ItemSkipped, category: ItemErrorLinkMetadataUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newWorkerServiceHarness(t)
			directory := frozenItemFixture()
			directory.Ref.EntryID = strings.Repeat("b", 64)
			directory.EntryFingerprint = "directory-entry-fingerprint-v1"
			directory.EntryType = backupasset.CatalogEntryDirectory
			directory.LogicalSize = 0
			directory.MediaType = ""
			directory.ArchiveComponents = []string{"root", "directory"}
			file := frozenItemFixture()
			selection, err := FreezeSelection([]FrozenItem{directory, file}, nil, harness.config.Selection)
			if err != nil {
				t.Fatal(err)
			}
			created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
				Actor: SelectionActor{UserID: 98, Role: "admin"}, Selection: selection,
				IdempotencyKey: "persistent-worker-reject-read-spool-" + test.name,
				ArchiveFormat:  ArchiveZIP, ArchiveProfile: ArchiveProfileZIPDeflateV1,
			})
			if err != nil {
				t.Fatal(err)
			}
			clock := time.Now().UTC().Truncate(time.Second)
			coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
			if err != nil {
				t.Fatal(err)
			}
			claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{
				JobID: created.JobID, WorkerOwner: "worker-reject-read-spool-" + test.name,
			})
			if err != nil {
				t.Fatal(err)
			}
			var fileRow model.BackupAssetExportItem
			if err := harness.db.Where("job_id = ? AND entry_type = ?", created.JobID, backupasset.CatalogEntryFile).Take(&fileRow).Error; err != nil {
				t.Fatal(err)
			}
			budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
			if err != nil {
				t.Fatal(err)
			}
			broker, err := content.NewAttemptBroker(&persistentSourceResolverFake{
				payload: bytes.Repeat([]byte("r"), int(file.LogicalSize)), providerBytes: file.LogicalSize,
			}, budget, func() time.Time { return clock })
			if err != nil {
				t.Fatal(err)
			}
			store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			worker, err := NewPersistentWorker(PersistentWorkerDependencies{
				DB: harness.db, Keys: backupasset.NewKeyring(harness.db, func() time.Time { return clock }),
				Broker: broker, Metadata: &metadataValidatorFake{}, Store: store,
				AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return clock },
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := worker.SpoolItem(context.Background(), PersistentSpoolItemRequest{
				JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: fileRow.ID,
			}); err != nil {
				t.Fatal(err)
			}
			snapshot, err := worker.loader.Load(context.Background(), PersistentAttemptLoadRequest{
				JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
			})
			if err != nil {
				t.Fatal(err)
			}
			var directoryItem, readItem PersistentAttemptItem
			for _, item := range snapshot.Items {
				switch item.Frozen.EntryType {
				case backupasset.CatalogEntryDirectory:
					directoryItem = item
				case backupasset.CatalogEntryFile:
					readItem = item
				}
			}
			if directoryItem.ItemID == "" || readItem.ItemID == "" || readItem.State != ItemRead || readItem.SpoolLocator == "" {
				t.Fatalf("prepared snapshot=%+v", snapshot)
			}
			artifactID, err := backupasset.NewOpaqueID()
			if err != nil {
				t.Fatal(err)
			}
			report := ArchiveReport{
				SchemaVersion: 1, SelectionDigest: snapshot.SelectionDigest,
				ResultKind: ResultPartial, Packed: 1, LogicalBytes: 0, ProviderBytes: readItem.ProviderBytes,
				Items: []ArchiveItemReport{
					{ItemID: directoryItem.ItemID, State: ItemPacked},
					{
						ItemID: readItem.ItemID, State: test.state, ProviderBytes: readItem.ProviderBytes,
						ErrorCategory: test.category, preHeaderSpoolRecovered: test.preHeaderSpoolRecovered,
					},
				},
			}
			if test.state == ItemFailed {
				report.Failed = 1
			} else {
				report.Skipped = 1
			}
			err = worker.persistSealedArchive(context.Background(), PersistentSealRequest{
				JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
			}, snapshot, artifactID, "forged-sealed-artifact.xre", CipherResult{
				CiphertextBytes: 1, NoncePrefix: append([]byte(nil), snapshot.AttemptNoncePrefix...),
			}, report)
			if !errors.Is(err, ErrAttemptFenceLost) {
				t.Fatalf("forged %s report error=%v, want ErrAttemptFenceLost", test.state, err)
			}

			var persistedItem model.BackupAssetExportItem
			if err := harness.db.First(&persistedItem, "id = ?", readItem.ItemID).Error; err != nil {
				t.Fatal(err)
			}
			if persistedItem.State != string(ItemRead) || persistedItem.ErrorCategory != "" || persistedItem.LogicalBytes != file.LogicalSize {
				t.Fatalf("rejected report downgraded item projection=%+v", persistedItem)
			}
			var persistedAttempt model.BackupAssetExportItemAttempt
			if err := harness.db.First(&persistedAttempt, "id = ?", readItem.ItemAttemptID).Error; err != nil {
				t.Fatal(err)
			}
			if persistedAttempt.State != string(ItemRead) || persistedAttempt.SpoolLocator == "" || persistedAttempt.ErrorCategory != "" {
				t.Fatalf("rejected report downgraded durable spool=%+v", persistedAttempt)
			}
			var job model.BackupAssetExportJob
			if err := harness.db.First(&job, "id = ?", created.JobID).Error; err != nil {
				t.Fatal(err)
			}
			if job.ExecutionState != string(ExecutionRunning) || job.PackedCount != 0 || job.SkippedCount != 0 || job.FailedCount != 0 {
				t.Fatalf("rejected report changed job=%+v", job)
			}
			var artifactCount int64
			if err := harness.db.Model(&model.BackupAssetExportArtifact{}).Where("job_id = ?", created.JobID).Count(&artifactCount).Error; err != nil {
				t.Fatal(err)
			}
			if artifactCount != 0 {
				t.Fatalf("rejected report created %d artifacts", artifactCount)
			}
		})
	}
}

func TestPersistentWorkerRejectsSealedSnapshotReadEvidenceDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*gorm.DB, persistentReadSpoolFixture) error
	}{
		{
			name: "item projection logical bytes",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportItem{}).Where("id = ?", fixture.file.ItemID).
					UpdateColumn("logical_bytes", fixture.file.LogicalBytes+1).Error
			},
		},
		{
			name: "item projection provider bytes",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportItem{}).Where("id = ?", fixture.file.ItemID).
					UpdateColumn("provider_bytes", fixture.file.ProviderBytes+1).Error
			},
		},
		{
			name: "spool digest",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", fixture.file.ItemAttemptID).
					UpdateColumn("spool_digest", differentSpoolDigest(fixture.file.SpoolDigest)).Error
			},
		},
		{
			name: "spool size",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", fixture.file.ItemAttemptID).
					UpdateColumn("spool_size", fixture.file.SpoolSize+1).Error
			},
		},
		{
			name: "spool locator",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", fixture.file.ItemAttemptID).
					UpdateColumn("spool_locator", strings.Repeat("c", 32)+".xrs").Error
			},
		},
		{
			name: "item attempt logical bytes",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", fixture.file.ItemAttemptID).
					UpdateColumn("logical_bytes", fixture.file.LogicalBytes+1).Error
			},
		},
		{
			name: "item attempt provider bytes",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", fixture.file.ItemAttemptID).
					UpdateColumn("provider_bytes", fixture.file.ProviderBytes+1).Error
			},
		},
		{
			name: "read timestamp",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", fixture.file.ItemAttemptID).
					UpdateColumn("read_at", fixture.clock.Add(time.Minute)).Error
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := createPersistentReadSpoolFixture(t)
			defer fixture.snapshot.ClearKeyMaterial()
			if err := test.mutate(fixture.harness.db, fixture); err != nil {
				t.Fatal(err)
			}
			before := loadSealedPersistenceState(t, fixture)

			err := fixture.worker.persistSealedArchive(
				context.Background(), persistentSealRequest(fixture), fixture.snapshot,
				strings.Repeat("a", 32), "snapshot-drift-sealed-artifact.xre",
				persistentSealCipherResult(fixture.snapshot), persistentPackedFileReport(fixture.snapshot, fixture.file),
			)
			if !errors.Is(err, ErrAttemptFenceLost) {
				t.Fatalf("persist drift error=%v, want ErrAttemptFenceLost", err)
			}
			assertSealedPersistenceStateUnchanged(t, fixture, before)
		})
	}
}

func TestPersistentWorkerPersistSealedArchiveRejectsStoreReservationPeakDriftAfterSnapshotLoad(t *testing.T) {
	fixture := createPersistentReadSpoolFixture(t)
	var reservations []model.BackupAssetExportReservation
	if err := fixture.harness.db.Where("job_id = ? AND kind = ?", fixture.jobID, "store").
		Order("bucket_id ASC").Find(&reservations).Error; err != nil {
		t.Fatal(err)
	}
	if len(reservations) != 2 || reservations[0].ReservedStoreBytes <= 0 ||
		reservations[0].ReservedStoreBytes != reservations[1].ReservedStoreBytes {
		t.Fatalf("initial store reservations=%+v", reservations)
	}
	originalPeak := reservations[0].ReservedStoreBytes
	mutatedPeak := originalPeak + 1
	result := fixture.harness.db.Model(&model.BackupAssetExportReservation{}).
		Where("job_id = ? AND kind = ?", fixture.jobID, "store").
		UpdateColumn("reserved_store_bytes", mutatedPeak)
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if result.RowsAffected != 2 {
		t.Fatalf("mutated store reservations=%d, want two", result.RowsAffected)
	}
	before := loadSealedPersistenceState(t, fixture)

	err := fixture.worker.persistSealedArchive(
		context.Background(), persistentSealRequest(fixture), fixture.snapshot,
		strings.Repeat("a", 32), "store-peak-drift-sealed-artifact.xre",
		persistentSealCipherResult(fixture.snapshot), persistentPackedFileReport(fixture.snapshot, fixture.file),
	)
	if !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("persist store peak drift error=%v, want ErrAttemptFenceLost", err)
	}
	assertSealedPersistenceStateUnchanged(t, fixture, before)
	if err := fixture.harness.db.Where("job_id = ? AND kind = ?", fixture.jobID, "store").
		Order("bucket_id ASC").Find(&reservations).Error; err != nil {
		t.Fatal(err)
	}
	if len(reservations) != 2 {
		t.Fatalf("store reservations=%d, want two", len(reservations))
	}
	bucketIDs := make([]string, 0, len(reservations))
	for _, reservation := range reservations {
		if reservation.State != "active" || reservation.ReservedStoreBytes != mutatedPeak {
			t.Fatalf("store reservation after rejected seal=%+v, want active with peak=%d", reservation, mutatedPeak)
		}
		bucketIDs = append(bucketIDs, reservation.BucketID)
	}
	var buckets []model.BackupAssetExportQuotaBucket
	if err := fixture.harness.db.Where("id IN ?", bucketIDs).Order("scope ASC, subject ASC").Find(&buckets).Error; err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("quota buckets=%d, want two", len(buckets))
	}
	for _, bucket := range buckets {
		if bucket.ReservedStoreBytes != originalPeak || bucket.UsedStoreBytes != 0 {
			t.Fatalf("quota bucket %s/%s after rejected seal reserved=%d used=%d, want reserved=%d used=0",
				bucket.Scope, bucket.Subject, bucket.ReservedStoreBytes, bucket.UsedStoreBytes, originalPeak)
		}
	}
}

func TestPersistentWorkerPersistSealedArchiveRejectsReloadedSnapshotLeaseDriftAfterSnapshotLoad(t *testing.T) {
	fixture := createPersistentReadSpoolFixture(t)
	defer fixture.snapshot.ClearKeyMaterial()

	var attempt model.BackupAssetExportAttempt
	if err := fixture.harness.db.Where("id = ? AND job_id = ?", fixture.attemptID, fixture.jobID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	driftedLeaseExpiry := attempt.LeaseExpiresAt.Add(time.Minute)
	if !driftedLeaseExpiry.After(fixture.clock) || !driftedLeaseExpiry.Before(fixture.snapshot.AbsoluteDeadline) {
		t.Fatalf("fixture lease drift=%s, want future before deadline=%s", driftedLeaseExpiry, fixture.snapshot.AbsoluteDeadline)
	}
	if err := fixture.harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ? AND job_id = ?", fixture.attemptID, fixture.jobID).
		Update("lease_expires_at", driftedLeaseExpiry).Error; err != nil {
		t.Fatal(err)
	}
	before := loadSealedPersistenceState(t, fixture)

	err := fixture.worker.persistSealedArchive(
		context.Background(), persistentSealRequest(fixture), fixture.snapshot,
		strings.Repeat("a", 32), "snapshot-lease-drift-sealed-artifact.xre",
		persistentSealCipherResult(fixture.snapshot), persistentPackedFileReport(fixture.snapshot, fixture.file),
	)
	if !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("persist reloaded snapshot drift error=%v, want ErrAttemptFenceLost", err)
	}
	assertSealedPersistenceStateUnchanged(t, fixture, before)
}

func TestPersistentWorkerPreservesSealedArchiveDatabaseQueryError(t *testing.T) {
	fixture := createPersistentReadSpoolFixture(t)
	defer fixture.snapshot.ClearKeyMaterial()
	before := loadSealedPersistenceState(t, fixture)
	injected := errors.New("injected sealed archive item query failure")
	callbackName := "test:sealed-archive-item-query-error-" + strings.ReplaceAll(t.Name(), "/", "_")
	injectedOnce := false
	if err := fixture.harness.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != "backup_asset_export_items" || len(tx.Statement.Selects) != 0 || injectedOnce {
			return
		}
		injectedOnce = true
		_ = tx.AddError(injected)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.harness.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove sealed archive query callback: %v", err)
		}
	})

	err := fixture.worker.persistSealedArchive(
		context.Background(), persistentSealRequest(fixture), fixture.snapshot,
		strings.Repeat("a", 32), "sealed-archive-query-error.xre",
		persistentSealCipherResult(fixture.snapshot), persistentPackedFileReport(fixture.snapshot, fixture.file),
	)
	if !injectedOnce {
		t.Fatal("sealed archive item query callback was not reached")
	}
	if !errors.Is(err, injected) || errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("sealed archive database query error=%v, want injected infrastructure error without ErrAttemptFenceLost", err)
	}
	assertSealedPersistenceStateUnchanged(t, fixture, before)
}

func TestPersistentWorkerPersistSealedArchiveRejectsAuthorityExpiredAfterSourceLocks(t *testing.T) {
	fixture := createPersistentReadSpoolFixture(t)
	defer fixture.snapshot.ClearKeyMaterial()
	expiresAt := fixture.clock.Add(time.Hour)
	if err := fixture.harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", fixture.attemptID).
		Update("lease_expires_at", expiresAt).Error; err != nil {
		t.Fatal(err)
	}
	before := loadSealedPersistenceState(t, fixture)
	current := fixture.clock
	fixture.worker.now = func() time.Time { return current }

	type persistSealedSourceFenceContextKey struct{}
	marker := t.Name()
	ctx := context.WithValue(context.Background(), persistSealedSourceFenceContextKey{}, marker)
	callbackName := "test:persist-sealed-authority-time-after-source-lock-" + strings.ReplaceAll(t.Name(), "/", "_")
	sourceLocked := false
	if err := fixture.harness.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != "recovery_point_leases" ||
			tx.Statement.Context.Value(persistSealedSourceFenceContextKey{}) != marker || sourceLocked ||
			!persistentStatementHasUpdateLock(tx) {
			return
		}
		sourceLocked = true
		current = expiresAt.Add(time.Second)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.harness.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove persist sealed source-lock callback: %v", err)
		}
	})

	err := fixture.worker.persistSealedArchive(
		ctx, persistentSealRequest(fixture), fixture.snapshot,
		strings.Repeat("a", 32), "sealed-after-authority-expiry.xre",
		persistentSealCipherResult(fixture.snapshot), persistentPackedFileReport(fixture.snapshot, fixture.file),
	)
	if !sourceLocked {
		t.Fatal("source fence lock callback was not reached")
	}
	if !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("persist sealed archive after source-lock authority expiry error=%v, want ErrAttemptFenceLost", err)
	}
	assertSealedPersistenceStateUnchanged(t, fixture, before)
}

func TestPersistentWorkerRejectsSealedItemReadEvidenceCASDrift(t *testing.T) {
	fixture := createPersistentReadSpoolFixture(t)
	defer fixture.snapshot.ClearKeyMaterial()
	before := loadSealedPersistenceState(t, fixture)

	callbackName := "test:seal-item-read-evidence-cas-" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	injected := false
	if err := fixture.harness.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != "backup_asset_export_item_attempts" || injected {
			return
		}
		injected = true
		if err := tx.Exec(
			"UPDATE backup_asset_export_item_attempts SET spool_digest = ? WHERE id = ?",
			differentSpoolDigest(fixture.file.SpoolDigest), fixture.file.ItemAttemptID,
		).Error; err != nil {
			_ = tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.harness.db.Callback().Update().Remove(callbackName); err != nil {
			t.Errorf("remove sealed item evidence callback: %v", err)
		}
	})

	err := fixture.worker.persistSealedArchive(
		context.Background(), persistentSealRequest(fixture), fixture.snapshot,
		strings.Repeat("a", 32), "terminal-cas-drift-sealed-artifact.xre",
		persistentSealCipherResult(fixture.snapshot), persistentPackedFileReport(fixture.snapshot, fixture.file),
	)
	if !injected {
		t.Fatal("terminal item-attempt update callback was not reached")
	}
	if !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("persist terminal CAS drift error=%v, want ErrAttemptFenceLost", err)
	}
	assertSealedPersistenceStateUnchanged(t, fixture, before)
}

func TestPersistentWorkerRejectsSealedFailedFileItemAttemptCASDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*gorm.DB, PersistentAttemptItem, time.Time) error
	}{
		{
			name: "provider bytes",
			mutate: func(db *gorm.DB, item PersistentAttemptItem, _ time.Time) error {
				return db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", item.ItemAttemptID).
					UpdateColumn("provider_bytes", item.ProviderBytes+1).Error
			},
		},
		{
			name: "error category",
			mutate: func(db *gorm.DB, item PersistentAttemptItem, _ time.Time) error {
				return db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", item.ItemAttemptID).
					UpdateColumn("error_category", "worker_unavailable").Error
			},
		},
		{
			name: "finished timestamp",
			mutate: func(db *gorm.DB, item PersistentAttemptItem, now time.Time) error {
				return db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", item.ItemAttemptID).
					UpdateColumn("finished_at", now.Add(time.Second)).Error
			},
		},
		{
			name: "spool locator",
			mutate: func(db *gorm.DB, item PersistentAttemptItem, _ time.Time) error {
				return db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", item.ItemAttemptID).
					UpdateColumn("spool_locator", strings.Repeat("b", 32)+".xrs").Error
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := createPersistentFailedFileSealFixture(t)
			defer fixture.snapshot.ClearKeyMaterial()
			before := loadSealedPersistenceState(t, fixture)

			callbackName := "test:seal-failed-item-attempt-cas-" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
			injected := false
			if err := fixture.harness.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement == nil || tx.Statement.Schema == nil ||
					tx.Statement.Schema.Table != "backup_asset_export_item_attempts" || injected {
					return
				}
				injected = true
				if err := test.mutate(tx.Session(&gorm.Session{NewDB: true}), fixture.file, fixture.clock); err != nil {
					_ = tx.AddError(err)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := fixture.harness.db.Callback().Update().Remove(callbackName); err != nil {
					t.Errorf("remove failed item-attempt CAS callback: %v", err)
				}
			})

			err := fixture.worker.persistSealedArchive(
				context.Background(), persistentSealRequest(fixture), fixture.snapshot,
				strings.Repeat("a", 32), "failed-item-attempt-cas-sealed-artifact.xre",
				persistentSealCipherResult(fixture.snapshot), persistentCanonicalSealReport(t, fixture.snapshot),
			)
			if !injected {
				t.Fatal("terminal failed item-attempt update callback was not reached")
			}
			if !errors.Is(err, ErrAttemptFenceLost) {
				t.Fatalf("persist failed item-attempt terminal CAS drift error=%v, want ErrAttemptFenceLost", err)
			}
			assertSealedPersistenceStateUnchanged(t, fixture, before)
		})
	}
}

func TestPersistentWorkerRejectsSealedPendingNonFileItemAttemptCASDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*gorm.DB, PersistentAttemptItem, time.Time) error
	}{
		{
			name: "provider bytes",
			mutate: func(db *gorm.DB, item PersistentAttemptItem, _ time.Time) error {
				return db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", item.ItemAttemptID).
					UpdateColumn("provider_bytes", int64(1)).Error
			},
		},
		{
			name: "error category",
			mutate: func(db *gorm.DB, item PersistentAttemptItem, _ time.Time) error {
				return db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", item.ItemAttemptID).
					UpdateColumn("error_category", "source_changed").Error
			},
		},
		{
			name: "packed timestamp",
			mutate: func(db *gorm.DB, item PersistentAttemptItem, now time.Time) error {
				return db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", item.ItemAttemptID).
					UpdateColumn("packed_at", now.Add(time.Second)).Error
			},
		},
		{
			name: "spool locator",
			mutate: func(db *gorm.DB, item PersistentAttemptItem, _ time.Time) error {
				return db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", item.ItemAttemptID).
					UpdateColumn("spool_locator", strings.Repeat("c", 32)+".xrs").Error
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := persistentNonFileFixture(
				backupasset.CatalogEntryDirectory, strings.Repeat("d", 64), "terminal-cas-directory",
			)
			fixture := createPersistentReadSpoolFixture(t, directory)
			defer fixture.snapshot.ClearKeyMaterial()
			directoryItem := persistentSnapshotItemByType(t, fixture.snapshot, backupasset.CatalogEntryDirectory)
			if directoryItem.State != ItemPending {
				t.Fatalf("pending non-file snapshot=%+v", directoryItem)
			}
			before := loadSealedPersistenceState(t, fixture)

			callbackName := "test:seal-pending-non-file-item-attempt-cas-" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
			injected := false
			if err := fixture.harness.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement == nil || tx.Statement.Schema == nil ||
					tx.Statement.Schema.Table != "backup_asset_export_item_attempts" || injected {
					return
				}
				injected = true
				if err := test.mutate(tx.Session(&gorm.Session{NewDB: true}), directoryItem, fixture.clock); err != nil {
					_ = tx.AddError(err)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := fixture.harness.db.Callback().Update().Remove(callbackName); err != nil {
					t.Errorf("remove pending non-file item-attempt CAS callback: %v", err)
				}
			})

			err := fixture.worker.persistSealedArchive(
				context.Background(), persistentSealRequest(fixture), fixture.snapshot,
				strings.Repeat("a", 32), "pending-non-file-item-attempt-cas-sealed-artifact.xre",
				persistentSealCipherResult(fixture.snapshot), persistentCanonicalSealReport(t, fixture.snapshot),
			)
			if !injected {
				t.Fatal("terminal pending non-file item-attempt update callback was not reached")
			}
			if !errors.Is(err, ErrAttemptFenceLost) {
				t.Fatalf("persist pending non-file terminal CAS drift error=%v, want ErrAttemptFenceLost", err)
			}
			assertSealedPersistenceStateUnchanged(t, fixture, before)
		})
	}
}

func TestPersistentWorkerRejectsSealedNonFileFailedReport(t *testing.T) {
	directory := frozenItemFixture()
	directory.Ref.EntryID = strings.Repeat("d", 64)
	directory.EntryFingerprint = "sealed-non-file-failed-directory"
	directory.EntryType = backupasset.CatalogEntryDirectory
	directory.LogicalSize = 0
	directory.MediaType = ""
	directory.ArchiveComponents = []string{"root", "directory"}
	fixture := createPersistentReadSpoolFixture(t, directory)
	defer fixture.snapshot.ClearKeyMaterial()

	var directoryItem PersistentAttemptItem
	for _, item := range fixture.snapshot.Items {
		if item.Frozen.EntryType == backupasset.CatalogEntryDirectory {
			directoryItem = item
			break
		}
	}
	if directoryItem.ItemID == "" || directoryItem.State != ItemPending {
		t.Fatalf("directory snapshot=%+v", fixture.snapshot)
	}
	before := loadSealedPersistenceState(t, fixture)
	report := ArchiveReport{
		SchemaVersion: 1, SelectionDigest: fixture.snapshot.SelectionDigest,
		ResultKind: ResultPartial, Packed: 1, Failed: 1,
		LogicalBytes: fixture.file.LogicalBytes, ProviderBytes: fixture.file.ProviderBytes,
		Items: []ArchiveItemReport{
			{ItemID: fixture.file.ItemID, State: ItemPacked, LogicalBytes: fixture.file.LogicalBytes, ProviderBytes: fixture.file.ProviderBytes},
			{ItemID: directoryItem.ItemID, State: ItemFailed, ErrorCategory: "source_changed"},
		},
	}
	err := fixture.worker.persistSealedArchive(
		context.Background(), persistentSealRequest(fixture), fixture.snapshot,
		strings.Repeat("a", 32), "non-file-failed-sealed-artifact.xre",
		persistentSealCipherResult(fixture.snapshot), report,
	)
	if !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("persist non-file failed report error=%v, want ErrAttemptFenceLost", err)
	}
	assertSealedPersistenceStateUnchanged(t, fixture, before)
}

func TestPersistentWorkerRejectsSealedSnapshotAuthorityDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*gorm.DB, persistentReadSpoolFixture) error
	}{
		{
			name: "archive format and profile",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					Updates(map[string]any{"archive_format": string(ArchiveTAR), "archive_profile": ArchiveProfileTARNoneV1}).Error
			},
		},
		{
			name: "chunk bytes",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("chunk_bytes", fixture.snapshot.ChunkBytes*2).Error
			},
		},
		{
			name: "max items",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("max_items", fixture.snapshot.MaxItems+1).Error
			},
		},
		{
			name: "max source points",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("max_source_points", 11).Error
			},
		},
		{
			name: "max item bytes",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("max_item_bytes", fixture.snapshot.MaxItemBytes+1).Error
			},
		},
		{
			name: "max logical bytes",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("max_logical_bytes", fixture.snapshot.MaxLogicalBytes+1).Error
			},
		},
		{
			name: "max provider bytes",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("max_provider_bytes", fixture.snapshot.MaxProviderBytes+1).Error
			},
		},
		{
			name: "max ciphertext bytes",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("max_ciphertext_bytes", fixture.snapshot.MaxCiphertextBytes+1).Error
			},
		},
		{
			name: "max open readers",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("max_open_readers", 3).Error
			},
		},
		{
			name: "max duration seconds",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("max_duration_seconds", 3601).Error
			},
		},
		{
			name: "max attempts",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("max_attempts", 4).Error
			},
		},
		{
			name: "retry base seconds",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("retry_base_seconds", 2).Error
			},
		},
		{
			name: "retry max delay seconds",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("retry_max_delay_seconds", 61).Error
			},
		},
		{
			name: "lease ttl seconds",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("lease_ttl_seconds", 901).Error
			},
		},
		{
			name: "lease renew margin seconds",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("lease_renew_margin_seconds", 301).Error
			},
		},
		{
			name: "ready ttl",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("ready_ttl_seconds", int64(fixture.snapshot.ReadyTTL/time.Second)+1).Error
			},
		},
		{
			name: "absolute deadline",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("absolute_deadline", fixture.snapshot.AbsoluteDeadline.Add(time.Minute)).Error
			},
		},
		{
			name: "current fence revision",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("current_fence_revision", gorm.Expr("current_fence_revision + 1")).Error
			},
		},
		{
			name: "transition revision",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("transition_revision", gorm.Expr("transition_revision + 1")).Error
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := createPersistentReadSpoolFixture(t)
			defer fixture.snapshot.ClearKeyMaterial()
			if err := test.mutate(fixture.harness.db, fixture); err != nil {
				t.Fatal(err)
			}
			before := loadSealedPersistenceState(t, fixture)

			err := fixture.worker.persistSealedArchive(
				context.Background(), persistentSealRequest(fixture), fixture.snapshot,
				strings.Repeat("a", 32), "authority-drift-sealed-artifact.xre",
				persistentSealCipherResult(fixture.snapshot), persistentPackedFileReport(fixture.snapshot, fixture.file),
			)
			if !errors.Is(err, ErrAttemptFenceLost) {
				t.Fatalf("persist authority drift error=%v, want ErrAttemptFenceLost", err)
			}
			assertSealedPersistenceStateUnchanged(t, fixture, before)
		})
	}
}

func TestPersistentWorkerRejectsSealedItemAuthorityDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*gorm.DB, model.BackupAssetExportItem) error
	}{
		{
			name: "recovery point id",
			mutate: func(db *gorm.DB, item model.BackupAssetExportItem) error {
				return db.Model(&model.BackupAssetExportItem{}).Where("id = ?", item.ID).
					UpdateColumn("recovery_point_id", strings.Repeat("f", 32)).Error
			},
		},
		{
			name: "entry id",
			mutate: func(db *gorm.DB, item model.BackupAssetExportItem) error {
				return db.Model(&model.BackupAssetExportItem{}).Where("id = ?", item.ID).
					UpdateColumn("entry_id", strings.Repeat("b", 64)).Error
			},
		},
		{
			name: "catalog generation id",
			mutate: func(db *gorm.DB, item model.BackupAssetExportItem) error {
				return db.Model(&model.BackupAssetExportItem{}).Where("id = ?", item.ID).
					UpdateColumn("catalog_generation_id", strings.Repeat("c", 32)).Error
			},
		},
		{
			name: "source fingerprint",
			mutate: func(db *gorm.DB, item model.BackupAssetExportItem) error {
				return db.Model(&model.BackupAssetExportItem{}).Where("id = ?", item.ID).
					UpdateColumn("source_fingerprint", item.SourceFingerprint+"-drift").Error
			},
		},
		{
			name: "entry fingerprint",
			mutate: func(db *gorm.DB, item model.BackupAssetExportItem) error {
				return db.Model(&model.BackupAssetExportItem{}).Where("id = ?", item.ID).
					UpdateColumn("entry_fingerprint", item.EntryFingerprint+"-drift").Error
			},
		},
		{
			name: "entry type",
			mutate: func(db *gorm.DB, item model.BackupAssetExportItem) error {
				return db.Model(&model.BackupAssetExportItem{}).Where("id = ?", item.ID).
					UpdateColumn("entry_type", string(backupasset.CatalogEntryDirectory)).Error
			},
		},
		{
			name: "logical size",
			mutate: func(db *gorm.DB, item model.BackupAssetExportItem) error {
				return db.Model(&model.BackupAssetExportItem{}).Where("id = ?", item.ID).
					UpdateColumn("logical_size", item.LogicalSize+1).Error
			},
		},
		{
			name: "media type",
			mutate: func(db *gorm.DB, item model.BackupAssetExportItem) error {
				return db.Model(&model.BackupAssetExportItem{}).Where("id = ?", item.ID).
					UpdateColumn("media_type", "application/octet-stream").Error
			},
		},
		{
			name: "retention until",
			mutate: func(db *gorm.DB, item model.BackupAssetExportItem) error {
				if item.RetentionUntil == nil {
					return errors.New("item retention cap is unexpectedly nil")
				}
				return db.Model(&model.BackupAssetExportItem{}).Where("id = ?", item.ID).
					UpdateColumn("retention_until", item.RetentionUntil.Add(time.Minute)).Error
			},
		},
		{
			name: "selection root ordinal",
			mutate: func(db *gorm.DB, item model.BackupAssetExportItem) error {
				return db.Model(&model.BackupAssetExportItem{}).Where("id = ?", item.ID).
					UpdateColumn("selection_root_ordinal", item.SelectionRootOrdinal+1).Error
			},
		},
		{
			name: "current attempt id",
			mutate: func(db *gorm.DB, item model.BackupAssetExportItem) error {
				return db.Model(&model.BackupAssetExportItem{}).Where("id = ?", item.ID).
					UpdateColumn("current_attempt_id", strings.Repeat("e", 32)).Error
			},
		},
		{
			name: "path nonce",
			mutate: func(db *gorm.DB, item model.BackupAssetExportItem) error {
				if len(item.PathNonce) == 0 {
					return errors.New("item path nonce is unexpectedly empty")
				}
				changed := append([]byte(nil), item.PathNonce...)
				changed[0] ^= 0x80
				return db.Model(&model.BackupAssetExportItem{}).Where("id = ?", item.ID).
					UpdateColumn("path_nonce", changed).Error
			},
		},
		{
			name: "path ciphertext",
			mutate: func(db *gorm.DB, item model.BackupAssetExportItem) error {
				if len(item.PathCiphertext) == 0 {
					return errors.New("item path ciphertext is unexpectedly empty")
				}
				changed := append([]byte(nil), item.PathCiphertext...)
				changed[0] ^= 0x80
				return db.Model(&model.BackupAssetExportItem{}).Where("id = ?", item.ID).
					UpdateColumn("path_ciphertext", changed).Error
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := createPersistentReadSpoolFixture(t)
			defer fixture.snapshot.ClearKeyMaterial()
			var item model.BackupAssetExportItem
			if err := fixture.harness.db.Where("id = ?", fixture.file.ItemID).Take(&item).Error; err != nil {
				t.Fatal(err)
			}
			if err := test.mutate(fixture.harness.db, item); err != nil {
				t.Fatal(err)
			}
			before := loadSealedPersistenceState(t, fixture)

			err := fixture.worker.persistSealedArchive(
				context.Background(), persistentSealRequest(fixture), fixture.snapshot,
				strings.Repeat("a", 32), "item-authority-drift-sealed-artifact.xre",
				persistentSealCipherResult(fixture.snapshot), persistentPackedFileReport(fixture.snapshot, fixture.file),
			)
			if !errors.Is(err, ErrAttemptFenceLost) {
				t.Fatalf("persist item authority drift error=%v, want ErrAttemptFenceLost", err)
			}
			assertSealedPersistenceStateUnchanged(t, fixture, before)
		})
	}
}

func TestPersistentWorkerRejectsForgedSealedReportSemantics(t *testing.T) {
	t.Run("complete result with skipped item", func(t *testing.T) {
		link := persistentNonFileFixture(backupasset.CatalogEntrySymlink, strings.Repeat("b", 64), "report-complete-link")
		fixture := createPersistentReadSpoolFixture(t, link)
		defer fixture.snapshot.ClearKeyMaterial()
		linkItem := persistentSnapshotItemByType(t, fixture.snapshot, backupasset.CatalogEntrySymlink)
		report := persistentArchiveReport(fixture.snapshot,
			ArchiveItemReport{ItemID: fixture.file.ItemID, State: ItemPacked, LogicalBytes: fixture.file.LogicalBytes, ProviderBytes: fixture.file.ProviderBytes},
			ArchiveItemReport{ItemID: linkItem.ItemID, State: ItemSkipped, ErrorCategory: ItemErrorLinkMetadataUnavailable},
		)
		report.ResultKind = ResultComplete
		before := loadSealedPersistenceState(t, fixture)
		assertPersistentSealReportRejected(t, fixture, report, before)
	})

	tests := []struct {
		name   string
		mutate func(*ArchiveReport)
	}{
		{
			name: "partial result with every item packed",
			mutate: func(report *ArchiveReport) {
				report.ResultKind = ResultPartial
			},
		},
		{
			name: "unknown schema version",
			mutate: func(report *ArchiveReport) {
				report.SchemaVersion = 2
			},
		},
		{
			name: "malformed selection digest",
			mutate: func(report *ArchiveReport) {
				report.SelectionDigest = "not-a-selection-digest"
			},
		},
		{
			name: "negative aggregate counts and bytes",
			mutate: func(report *ArchiveReport) {
				report.Packed = 2
				report.Skipped = -1
				report.LogicalBytes = -1
				report.ProviderBytes = -1
			},
		},
		{
			name: "aggregate count disagrees with item reports",
			mutate: func(report *ArchiveReport) {
				report.ResultKind = ResultPartial
				report.Packed = 0
				report.Skipped = 1
			},
		},
		{
			name: "aggregate bytes disagree with item reports",
			mutate: func(report *ArchiveReport) {
				report.LogicalBytes = 0
				report.ProviderBytes = 0
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := createPersistentReadSpoolFixture(t)
			defer fixture.snapshot.ClearKeyMaterial()
			report := persistentPackedFileReport(fixture.snapshot, fixture.file)
			test.mutate(&report)
			before := loadSealedPersistenceState(t, fixture)
			assertPersistentSealReportRejected(t, fixture, report, before)
		})
	}
}

func TestPersistentWorkerRejectsInvalidSealedItemTypeMappings(t *testing.T) {
	for _, test := range []struct {
		name      string
		entryType backupasset.CatalogEntryType
	}{
		{name: "link packed", entryType: backupasset.CatalogEntrySymlink},
		{name: "special packed", entryType: backupasset.CatalogEntrySpecial},
	} {
		t.Run(test.name, func(t *testing.T) {
			nonFile := persistentNonFileFixture(test.entryType, strings.Repeat("b", 64), "invalid-packed-"+test.name)
			fixture := createPersistentReadSpoolFixture(t, nonFile)
			defer fixture.snapshot.ClearKeyMaterial()
			nonFileItem := persistentSnapshotItemByType(t, fixture.snapshot, test.entryType)
			report := persistentArchiveReport(fixture.snapshot,
				ArchiveItemReport{ItemID: fixture.file.ItemID, State: ItemPacked, LogicalBytes: fixture.file.LogicalBytes, ProviderBytes: fixture.file.ProviderBytes},
				ArchiveItemReport{ItemID: nonFileItem.ItemID, State: ItemPacked},
			)
			before := loadSealedPersistenceState(t, fixture)
			assertPersistentSealReportRejected(t, fixture, report, before)
		})
	}

	for _, test := range []struct {
		name      string
		entryType backupasset.CatalogEntryType
		category  string
		bytes     int64
	}{
		{
			name:      "link skipped with special category",
			entryType: backupasset.CatalogEntrySymlink,
			category:  ItemErrorSpecialFileSkipped,
		},
		{
			name:      "special skipped with link category",
			entryType: backupasset.CatalogEntrySpecial,
			category:  ItemErrorLinkMetadataUnavailable,
		},
		{
			name:      "link skipped with provider bytes",
			entryType: backupasset.CatalogEntrySymlink,
			category:  ItemErrorLinkMetadataUnavailable,
			bytes:     1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			nonFile := persistentNonFileFixture(test.entryType, strings.Repeat("c", 64), "invalid-skipped-"+test.name)
			fixture := createPersistentReadSpoolFixture(t, nonFile)
			defer fixture.snapshot.ClearKeyMaterial()
			nonFileItem := persistentSnapshotItemByType(t, fixture.snapshot, test.entryType)
			report := persistentArchiveReport(fixture.snapshot,
				ArchiveItemReport{ItemID: fixture.file.ItemID, State: ItemPacked, LogicalBytes: fixture.file.LogicalBytes, ProviderBytes: fixture.file.ProviderBytes},
				ArchiveItemReport{ItemID: nonFileItem.ItemID, State: ItemSkipped, ProviderBytes: test.bytes, ErrorCategory: test.category},
			)
			before := loadSealedPersistenceState(t, fixture)
			assertPersistentSealReportRejected(t, fixture, report, before)
		})
	}

	t.Run("regular pending becomes skipped", func(t *testing.T) {
		pending := frozenItemFixture()
		pending.Ref.EntryID = strings.Repeat("d", 64)
		pending.EntryFingerprint = "regular-pending-skipped"
		pending.ArchiveComponents = []string{"root", "pending.txt"}
		fixture := createPersistentReadSpoolFixture(t, pending)
		defer fixture.snapshot.ClearKeyMaterial()
		var pendingItem PersistentAttemptItem
		for _, item := range fixture.snapshot.Items {
			if item.ItemID != fixture.file.ItemID && item.Frozen.EntryType == backupasset.CatalogEntryFile {
				pendingItem = item
				break
			}
		}
		if pendingItem.ItemID == "" || pendingItem.State != ItemPending {
			t.Fatalf("pending regular snapshot=%+v", fixture.snapshot)
		}
		report := persistentArchiveReport(fixture.snapshot,
			ArchiveItemReport{ItemID: fixture.file.ItemID, State: ItemPacked, LogicalBytes: fixture.file.LogicalBytes, ProviderBytes: fixture.file.ProviderBytes},
			ArchiveItemReport{ItemID: pendingItem.ItemID, State: ItemSkipped, ErrorCategory: "source_changed"},
		)
		before := loadSealedPersistenceState(t, fixture)
		assertPersistentSealReportRejected(t, fixture, report, before)
	})
}

func TestPersistentWorkerAllowsLegalSealedNonFileMappings(t *testing.T) {
	directory := persistentNonFileFixture(backupasset.CatalogEntryDirectory, strings.Repeat("b", 64), "legal-directory")
	link := persistentNonFileFixture(backupasset.CatalogEntrySymlink, strings.Repeat("c", 64), "legal-link")
	special := persistentNonFileFixture(backupasset.CatalogEntrySpecial, strings.Repeat("d", 64), "legal-special")
	fixture := createPersistentReadSpoolFixture(t, directory, link, special)
	defer fixture.snapshot.ClearKeyMaterial()
	report := persistentCanonicalSealReport(t, fixture.snapshot)
	if err := fixture.worker.persistSealedArchive(
		context.Background(), persistentSealRequest(fixture), fixture.snapshot,
		strings.Repeat("a", 32), "legal-non-file-sealed-artifact.xre",
		persistentSealCipherResult(fixture.snapshot), report,
	); err != nil {
		t.Fatalf("legal non-file mappings error=%v", err)
	}
	state := loadSealedPersistenceState(t, fixture)
	if state.job.ExecutionState != string(ExecutionSealing) || state.job.PackedCount != 2 ||
		state.job.SkippedCount != 2 || state.job.FailedCount != 0 || state.artifactCount != 1 {
		t.Fatalf("legal non-file mappings state=%+v", state)
	}
}

func TestPersistentWorkerRejectsIncompletePersistedSourceSet(t *testing.T) {
	secondSource := persistentSecondSourceDirectoryFixture()
	for _, test := range []struct {
		name   string
		mutate func(*gorm.DB, model.BackupAssetExportSourceLease) error
	}{
		{
			name: "deleted source row",
			mutate: func(db *gorm.DB, source model.BackupAssetExportSourceLease) error {
				return db.Where("id = ?", source.ID).Delete(&model.BackupAssetExportSourceLease{}).Error
			},
		},
		{
			name: "inactive source row",
			mutate: func(db *gorm.DB, source model.BackupAssetExportSourceLease) error {
				return db.Model(&model.BackupAssetExportSourceLease{}).Where("id = ?", source.ID).
					UpdateColumn("state", "released").Error
			},
		},
	} {
		t.Run("seal/"+test.name, func(t *testing.T) {
			fixture := createPersistentReadSpoolFixture(t, secondSource)
			defer fixture.snapshot.ClearKeyMaterial()
			source := persistentSourceLeaseForRecoveryPoint(t, fixture, secondSource.Ref.RecoveryPointID)
			if err := test.mutate(fixture.harness.db, source); err != nil {
				t.Fatal(err)
			}
			before := loadSealedPersistenceState(t, fixture)
			err := fixture.worker.persistSealedArchive(
				context.Background(), persistentSealRequest(fixture), fixture.snapshot,
				strings.Repeat("a", 32), "incomplete-source-set-sealed-artifact.xre",
				persistentSealCipherResult(fixture.snapshot), persistentCanonicalSealReport(t, fixture.snapshot),
			)
			if !errors.Is(err, ErrAttemptFenceLost) {
				t.Fatalf("incomplete source-set seal error=%v, want ErrAttemptFenceLost", err)
			}
			assertSealedPersistenceStateUnchanged(t, fixture, before)
		})

		t.Run("checkpoint/"+test.name, func(t *testing.T) {
			fixture := createPersistentReadSpoolFixture(t, secondSource)
			defer fixture.snapshot.ClearKeyMaterial()
			source := persistentSourceLeaseForRecoveryPoint(t, fixture, secondSource.Ref.RecoveryPointID)
			if err := test.mutate(fixture.harness.db, source); err != nil {
				t.Fatal(err)
			}
			before := loadSealedPersistenceState(t, fixture)
			coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return fixture.clock })
			if err != nil {
				t.Fatal(err)
			}
			err = coordinator.Checkpoint(context.Background(), AttemptCheckpoint{
				JobID: fixture.jobID, AttemptID: fixture.attemptID, FenceToken: fixture.fenceToken,
				ItemID: fixture.file.ItemID, State: ItemPacked,
				LogicalBytes: fixture.file.LogicalBytes, ProviderBytes: fixture.file.ProviderBytes,
			})
			if !errors.Is(err, ErrAttemptFenceLost) {
				t.Fatalf("incomplete source-set checkpoint error=%v, want ErrAttemptFenceLost", err)
			}
			assertSealedPersistenceStateUnchanged(t, fixture, before)
		})
	}
}

func TestPersistentWorkerRejectsPersistedSourceRetentionWidening(t *testing.T) {
	for _, operation := range []string{"seal", "checkpoint"} {
		t.Run(operation, func(t *testing.T) {
			fixture := createPersistentReadSpoolFixture(t)
			defer fixture.snapshot.ClearKeyMaterial()
			source := persistentSourceLeaseForRecoveryPoint(t, fixture, fixture.file.Frozen.Ref.RecoveryPointID)
			if source.RetentionUntil == nil {
				t.Fatal("source retention cap is unexpectedly nil")
			}
			later := source.RetentionUntil.Add(time.Minute)
			if err := fixture.harness.db.Model(&model.BackupAssetExportSourceLease{}).Where("id = ?", source.ID).
				UpdateColumn("retention_until", later).Error; err != nil {
				t.Fatal(err)
			}
			before := loadSealedPersistenceState(t, fixture)

			switch operation {
			case "seal":
				err := fixture.worker.persistSealedArchive(
					context.Background(), persistentSealRequest(fixture), fixture.snapshot,
					strings.Repeat("a", 32), "widened-source-retention-sealed-artifact.xre",
					persistentSealCipherResult(fixture.snapshot), persistentPackedFileReport(fixture.snapshot, fixture.file),
				)
				if !errors.Is(err, ErrAttemptFenceLost) {
					t.Fatalf("widened source retention seal error=%v, want ErrAttemptFenceLost", err)
				}
			case "checkpoint":
				coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return fixture.clock })
				if err != nil {
					t.Fatal(err)
				}
				err = coordinator.Checkpoint(context.Background(), AttemptCheckpoint{
					JobID: fixture.jobID, AttemptID: fixture.attemptID, FenceToken: fixture.fenceToken,
					ItemID: fixture.file.ItemID, State: ItemPacked,
					LogicalBytes: fixture.file.LogicalBytes, ProviderBytes: fixture.file.ProviderBytes,
				})
				if !errors.Is(err, ErrAttemptFenceLost) {
					t.Fatalf("widened source retention checkpoint error=%v, want ErrAttemptFenceLost", err)
				}
			}
			assertSealedPersistenceStateUnchanged(t, fixture, before)
		})
	}
}

func TestPersistentWorkerTreatsMalformedPersistedItemCardinalityAsUnavailable(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*gorm.DB, persistentReadSpoolFixture) error
	}{
		{
			name: "extra persisted item",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				var item model.BackupAssetExportItem
				if err := db.Where("id = ?", fixture.file.ItemID).Take(&item).Error; err != nil {
					return err
				}
				id, err := backupasset.NewOpaqueID()
				if err != nil {
					return err
				}
				item.ID = id
				item.Ordinal++
				item.EntryID = strings.Repeat("b", 64)
				return db.Create(&item).Error
			},
		},
		{
			name: "job item count exceeds persisted items",
			mutate: func(db *gorm.DB, fixture persistentReadSpoolFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("item_count", int64(2)).Error
			},
		},
	}

	for _, test := range tests {
		for _, operation := range []string{"seal", "checkpoint"} {
			t.Run(operation+"/"+test.name, func(t *testing.T) {
				fixture := createPersistentReadSpoolFixture(t)
				defer fixture.snapshot.ClearKeyMaterial()
				if err := test.mutate(fixture.harness.db, fixture); err != nil {
					t.Fatalf("persist malformed item cardinality fixture: %v", err)
				}
				before := loadSealedPersistenceState(t, fixture)

				var err error
				switch operation {
				case "seal":
					err = fixture.worker.persistSealedArchive(
						context.Background(), persistentSealRequest(fixture), fixture.snapshot,
						strings.Repeat("a", 32), "malformed-cardinality-sealed-artifact.xre",
						persistentSealCipherResult(fixture.snapshot), persistentPackedFileReport(fixture.snapshot, fixture.file),
					)
				case "checkpoint":
					coordinator, coordinatorErr := NewAttemptCoordinator(
						fixture.harness.db, func() time.Time { return fixture.clock },
					)
					if coordinatorErr != nil {
						t.Fatal(coordinatorErr)
					}
					err = coordinator.Checkpoint(context.Background(), AttemptCheckpoint{
						JobID: fixture.jobID, AttemptID: fixture.attemptID, FenceToken: fixture.fenceToken,
						ItemID: fixture.file.ItemID, State: ItemPacked,
						LogicalBytes: fixture.file.LogicalBytes, ProviderBytes: fixture.file.ProviderBytes,
					})
				default:
					t.Fatalf("unknown operation %q", operation)
				}
				if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrAttemptFenceLost) {
					t.Fatalf("malformed item cardinality %s error=%v, want ErrUnavailable without ErrAttemptFenceLost", operation, err)
				}
				assertSealedPersistenceStateUnchanged(t, fixture, before)
			})
		}
	}
}

func TestPersistentWorkerPublishReadyRejectsSourceAuthorityDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*gorm.DB, model.BackupAssetExportSourceLease) error
	}{
		{
			name: "deleted source row",
			mutate: func(db *gorm.DB, source model.BackupAssetExportSourceLease) error {
				return db.Where("id = ?", source.ID).Delete(&model.BackupAssetExportSourceLease{}).Error
			},
		},
		{
			name: "inactive source row",
			mutate: func(db *gorm.DB, source model.BackupAssetExportSourceLease) error {
				return db.Model(&model.BackupAssetExportSourceLease{}).Where("id = ?", source.ID).
					UpdateColumn("state", "released").Error
			},
		},
		{
			name: "widened retention until",
			mutate: func(db *gorm.DB, source model.BackupAssetExportSourceLease) error {
				if source.RetentionUntil == nil {
					return errors.New("source retention cap is unexpectedly nil")
				}
				return db.Model(&model.BackupAssetExportSourceLease{}).Where("id = ?", source.ID).
					UpdateColumn("retention_until", source.RetentionUntil.Add(time.Minute)).Error
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := createPersistentReadSpoolFixture(t, persistentSecondSourceDirectoryFixture())
			defer fixture.snapshot.ClearKeyMaterial()
			sealed, err := fixture.worker.SealArchive(context.Background(), persistentSealRequest(fixture))
			if err != nil {
				t.Fatal(err)
			}
			before := loadSealedPersistenceStateFor(t, fixture.harness.db, fixture.jobID, fixture.attemptID)
			if len(before.sourceLeases) != 2 {
				t.Fatalf("sealed source leases=%+v, want exactly two", before.sourceLeases)
			}
			source := before.sourceLeases[0]

			callbackName := "test:publish-ready-source-authority-drift-" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
			injected := false
			var expected sealedPersistenceState
			if err := fixture.harness.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement == nil || tx.Statement.Schema == nil ||
					tx.Statement.Schema.Table != "backup_asset_export_artifacts" || injected {
					return
				}
				// Artifact authentication occurs after the loader's stable snapshot and before
				// PublishReady opens its mutation transaction.
				injected = true
				if err := test.mutate(fixture.harness.db, source); err != nil {
					_ = tx.AddError(err)
					return
				}
				expected = loadSealedPersistenceStateFor(t, fixture.harness.db, fixture.jobID, fixture.attemptID)
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := fixture.harness.db.Callback().Query().Remove(callbackName); err != nil {
					t.Errorf("remove publish ready source authority callback: %v", err)
				}
			})

			_, err = fixture.worker.PublishReady(context.Background(), PersistentPublishRequest{
				JobID: fixture.jobID, AttemptID: fixture.attemptID,
				FenceToken: fixture.fenceToken, ArtifactID: sealed.ArtifactID,
			})
			if !injected {
				t.Fatal("publish-ready source authority callback was not reached")
			}
			if !errors.Is(err, ErrAttemptFenceLost) {
				t.Fatalf("publish-ready source authority drift error=%v, want ErrAttemptFenceLost", err)
			}
			if after := loadSealedPersistenceStateFor(t, fixture.harness.db, fixture.jobID, fixture.attemptID); !reflect.DeepEqual(after, expected) {
				t.Fatalf("rejected publication mutated rows beyond source drift: expected=%+v after=%+v", expected, after)
			}
		})
	}
}

func TestPersistentWorkerSealArchiveRejectsLiveMetadataDriftInAuthorityTransaction(t *testing.T) {
	directory := persistentNonFileFixture(
		backupasset.CatalogEntryDirectory, strings.Repeat("d", 64), "seal-authority-metadata-directory",
	)
	fixture := createPersistentReadSpoolFixture(t, directory)
	defer fixture.snapshot.ClearKeyMaterial()
	metadata, ok := fixture.worker.metadata.(*metadataValidatorFake)
	if !ok {
		t.Fatalf("metadata validator type=%T, want *metadataValidatorFake", fixture.worker.metadata)
	}
	metadata.txBefore = func(tx *gorm.DB, item FrozenItem) error {
		if tx == nil || tx.Statement == nil || tx.Statement.ConnPool == nil {
			return ErrUnavailable
		}
		if item.EntryType == backupasset.CatalogEntryDirectory {
			return content.ErrAttemptSourceChanged
		}
		return nil
	}
	before := loadSealedPersistenceState(t, fixture)

	_, err := fixture.worker.SealArchive(context.Background(), persistentSealRequest(fixture))
	if !errors.Is(err, content.ErrAttemptSourceChanged) {
		t.Fatalf("seal metadata authority drift error=%v, want source changed", err)
	}
	if len(metadata.txItems) != len(fixture.snapshot.Items) {
		t.Fatalf("seal metadata transaction calls=%d, want one per item=%d", len(metadata.txItems), len(fixture.snapshot.Items))
	}
	after := loadSealedPersistenceState(t, fixture)
	if !validStoreLocator(after.attempt.StagingLocator) || !strings.HasSuffix(after.attempt.StagingLocator, ".xre") {
		t.Fatalf("metadata-drift nonce claim locator=%q", after.attempt.StagingLocator)
	}
	expected := before
	expected.attempt.StagingLocator = after.attempt.StagingLocator
	if !reflect.DeepEqual(after, expected) {
		t.Fatalf("metadata-drift seal mutated beyond nonce claim: expected=%+v after=%+v", expected, after)
	}
	if reader, openErr := fixture.worker.store.OpenSealed(after.attempt.StagingLocator); !errors.Is(openErr, ErrStoreObjectAbsent) {
		if reader != nil {
			_ = reader.Close()
		}
		t.Fatalf("rejected metadata-drift staging was published: %v", openErr)
	}
}

func TestPersistentWorkerPublishReadyRejectsLiveMetadataDriftInAuthorityTransaction(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	metadata := fixture.metadata
	metadata.txItems = nil
	metadata.txBefore = func(tx *gorm.DB, _ FrozenItem) error {
		if tx == nil || tx.Statement == nil || tx.Statement.ConnPool == nil {
			return ErrUnavailable
		}
		return content.ErrAttemptSourceChanged
	}
	before := loadSealedPersistenceStateFor(t, fixture.harness.db, fixture.jobID, fixture.attemptID)

	_, err := fixture.worker.PublishReady(context.Background(), PersistentPublishRequest{
		JobID: fixture.jobID, AttemptID: fixture.attemptID,
		FenceToken: fixture.fenceToken, ArtifactID: fixture.artifactID,
	})
	if !errors.Is(err, content.ErrAttemptSourceChanged) {
		t.Fatalf("publish metadata authority drift error=%v, want source changed", err)
	}
	if len(metadata.txItems) != 1 {
		t.Fatalf("publish metadata transaction calls=%d, want one", len(metadata.txItems))
	}
	if after := loadSealedPersistenceStateFor(t, fixture.harness.db, fixture.jobID, fixture.attemptID); !reflect.DeepEqual(after, before) {
		t.Fatalf("metadata-drift publication changed durable state: before=%+v after=%+v", before, after)
	}
}

func TestPersistentWorkerPublishReadyRejectsFenceDigestDriftAfterAuthentication(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	var attempt model.BackupAssetExportAttempt
	if err := fixture.harness.db.First(&attempt, "id = ?", fixture.attemptID).Error; err != nil {
		t.Fatal(err)
	}
	forgedDigest := differentSpoolDigest(attempt.FenceDigest)
	callbackName := "test:publish-ready-fence-digest-drift-" + strings.ReplaceAll(t.Name(), "/", "_")
	injected := false
	var expected sealedPersistenceState
	if err := fixture.harness.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "backup_asset_export_artifacts" || injected {
			return
		}
		// This is the authenticated artifact read before PublishReady begins its mutation transaction.
		injected = true
		if err := fixture.harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", fixture.attemptID).
			Update("fence_digest", forgedDigest).Error; err != nil {
			_ = tx.AddError(err)
			return
		}
		expected = loadSealedPersistenceStateFor(t, fixture.harness.db, fixture.jobID, fixture.attemptID)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.harness.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove publish-ready fence digest callback: %v", err)
		}
	})

	_, err := fixture.worker.PublishReady(context.Background(), PersistentPublishRequest{
		JobID: fixture.jobID, AttemptID: fixture.attemptID, FenceToken: fixture.fenceToken, ArtifactID: fixture.artifactID,
	})
	if !injected {
		t.Fatal("publish-ready fence digest callback was not reached")
	}
	if !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("publish-ready fence digest drift error=%v, want ErrAttemptFenceLost", err)
	}
	if after := loadSealedPersistenceStateFor(t, fixture.harness.db, fixture.jobID, fixture.attemptID); !reflect.DeepEqual(after, expected) {
		t.Fatalf("rejected publication mutated rows beyond fence digest drift: expected=%+v after=%+v", expected, after)
	}
}

func TestPersistentWorkerPublishReadyRejectsAuthorityExpiredAfterSourceLocks(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	expiresAt := fixture.clock.Add(time.Hour)
	if err := fixture.harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", fixture.attemptID).
		Update("lease_expires_at", expiresAt).Error; err != nil {
		t.Fatal(err)
	}
	before := loadSealedPersistenceStateFor(t, fixture.harness.db, fixture.jobID, fixture.attemptID)
	current := fixture.clock
	fixture.worker.loader.now = func() time.Time { return fixture.clock }
	fixture.worker.now = func() time.Time { return current }

	marker := t.Name()
	ctx := context.WithValue(context.Background(), persistentPublishReadySourceFenceContextKey{}, marker)
	barrier := installPersistentPublishReadySourceFenceBarrier(
		t, fixture.harness.db, marker,
		"test:publish-ready-authority-time-after-source-lock-"+strings.ReplaceAll(t.Name(), "/", "_"),
		func(*gorm.DB) error {
			current = expiresAt.Add(time.Second)
			return nil
		},
	)

	_, err := fixture.worker.PublishReady(ctx, PersistentPublishRequest{
		JobID: fixture.jobID, AttemptID: fixture.attemptID, FenceToken: fixture.fenceToken, ArtifactID: fixture.artifactID,
	})
	if !barrier.fired {
		t.Fatal("publish-ready source fence barrier was not reached")
	}
	if barrier.err != nil {
		t.Fatalf("publish-ready source fence barrier error: %v", barrier.err)
	}
	if !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("publish ready after source-lock authority expiry error=%v, want ErrAttemptFenceLost", err)
	}
	if after := loadSealedPersistenceStateFor(t, fixture.harness.db, fixture.jobID, fixture.attemptID); !reflect.DeepEqual(after, before) {
		t.Fatalf("publish ready after source-lock authority expiry changed durable state: before=%+v after=%+v", before, after)
	}
}

func TestPersistentWorkerPublishReadyUsesValidatedSourceSnapshotForExpiry(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	var job model.BackupAssetExportJob
	if err := fixture.harness.db.First(&job, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	var validated model.BackupAssetExportSourceLease
	if err := fixture.harness.db.Where("job_id = ? AND state = ?", fixture.jobID, "active").Take(&validated).Error; err != nil {
		t.Fatal(err)
	}
	wantExpiry, err := ComputeReadyExpiry(
		fixture.clock, time.Duration(job.ReadyTTLSeconds)*time.Second,
		[]SourceDeadline{{AbsoluteDeadline: validated.AbsoluteDeadline, RetentionUntil: validated.RetentionUntil}},
	)
	if err != nil {
		t.Fatal(err)
	}
	lateSource := createInactivePublishReadySource(t, fixture, fixture.clock.Add(time.Minute))
	lateExpiry, err := ComputeReadyExpiry(
		fixture.clock, time.Duration(job.ReadyTTLSeconds)*time.Second,
		[]SourceDeadline{
			{AbsoluteDeadline: validated.AbsoluteDeadline, RetentionUntil: validated.RetentionUntil},
			{AbsoluteDeadline: lateSource.AbsoluteDeadline, RetentionUntil: lateSource.RetentionUntil},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if lateExpiry.Equal(wantExpiry) {
		t.Fatalf("late source did not tighten ready expiry: late=%s validated=%s", lateExpiry, wantExpiry)
	}

	marker := t.Name()
	ctx := context.WithValue(context.Background(), persistentPublishReadySourceFenceContextKey{}, marker)
	activated := false
	barrier := installPersistentPublishReadySourceFenceBarrier(
		t, fixture.harness.db, marker,
		"test:publish-ready-source-snapshot-"+strings.ReplaceAll(t.Name(), "/", "_"),
		func(tx *gorm.DB) error {
			result := tx.Session(&gorm.Session{NewDB: true}).Model(&model.BackupAssetExportSourceLease{}).
				Where("id = ? AND state = ?", lateSource.ID, "released").
				Updates(map[string]any{"state": "active", "released_at": nil, "updated_at": fixture.clock})
			if result.Error != nil || result.RowsAffected != 1 {
				return fmt.Errorf("activate post-validation source lease: %w", result.Error)
			}
			activated = true
			return nil
		},
	)

	published, err := fixture.worker.PublishReady(ctx, PersistentPublishRequest{
		JobID: fixture.jobID, AttemptID: fixture.attemptID, FenceToken: fixture.fenceToken, ArtifactID: fixture.artifactID,
	})
	if !barrier.fired || !activated {
		t.Fatal("post-validation source activation callback was not reached")
	}
	if barrier.err != nil {
		t.Fatalf("post-validation source activation callback error: %v", barrier.err)
	}
	if err != nil {
		t.Fatalf("publish ready with post-validation source activation: %v", err)
	}
	if !published.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("ready expiry=%s, want prevalidated source expiry=%s", published.ExpiresAt, wantExpiry)
	}
}

func TestPersistentWorkerPublishReadyPreservesDatabaseQueryError(t *testing.T) {
	for _, stage := range []string{"authentication preflight", "transaction persisted artifact"} {
		t.Run(stage, func(t *testing.T) {
			fixture := createPersistentSealedFixture(t)
			before := loadSealedPersistenceStateFor(t, fixture.harness.db, fixture.jobID, fixture.attemptID)
			injected := errors.New("injected publish-ready artifact query failure")
			type publishReadyDatabaseQueryContextKey struct{}
			marker := t.Name()
			ctx := context.WithValue(context.Background(), publishReadyDatabaseQueryContextKey{}, marker)
			callbackName := "test:publish-ready-artifact-query-error-" + strings.ReplaceAll(t.Name(), "/", "_")
			injectedOnce := false
			preflightRead := false
			var mutationTransaction gorm.ConnPool
			if err := fixture.harness.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement == nil || tx.Statement.Schema == nil ||
					tx.Statement.Context.Value(publishReadyDatabaseQueryContextKey{}) != marker {
					return
				}
				switch tx.Statement.Schema.Table {
				case "backup_asset_export_artifacts":
					if !persistentStatementHasUpdateLock(tx) {
						preflightRead = true
						if stage != "authentication preflight" || injectedOnce {
							return
						}
						injectedOnce = true
						_ = tx.AddError(injected)
						return
					}
					if stage != "transaction persisted artifact" || injectedOnce || mutationTransaction == nil ||
						tx.Statement.ConnPool != mutationTransaction {
						return
					}
					injectedOnce = true
					_ = tx.AddError(injected)
				case "backup_asset_export_jobs":
					if stage == "transaction persisted artifact" && preflightRead && mutationTransaction == nil &&
						persistentStatementHasUpdateLock(tx) {
						mutationTransaction = tx.Statement.ConnPool
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := fixture.harness.db.Callback().Query().Remove(callbackName); err != nil {
					t.Errorf("remove publish-ready database query callback: %v", err)
				}
			})

			_, err := fixture.worker.PublishReady(ctx, PersistentPublishRequest{
				JobID: fixture.jobID, AttemptID: fixture.attemptID, FenceToken: fixture.fenceToken, ArtifactID: fixture.artifactID,
			})
			if !injectedOnce {
				t.Fatalf("publish-ready %s query callback was not reached", stage)
			}
			if !errors.Is(err, injected) || errors.Is(err, ErrAttemptFenceLost) {
				t.Fatalf("publish-ready database query error=%v, want injected infrastructure error without ErrAttemptFenceLost", err)
			}
			if after := loadSealedPersistenceStateFor(t, fixture.harness.db, fixture.jobID, fixture.attemptID); !reflect.DeepEqual(after, before) {
				t.Fatalf("publish-ready database query error changed durable state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestPersistentWorkerPublishReadyPreservesLoaderDatabaseQueryErrors(t *testing.T) {
	for _, test := range []struct {
		name  string
		table string
	}{
		{name: "job", table: "backup_asset_export_jobs"},
		{name: "attempt", table: "backup_asset_export_attempts"},
		{name: "key", table: "backup_asset_export_keys"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := createPersistentSealedFixture(t)
			before := loadSealedPersistenceStateFor(t, fixture.harness.db, fixture.jobID, fixture.attemptID)
			injected := errors.New("injected publish-ready loader " + test.name + " query failure")
			type publishReadyLoaderQueryContextKey struct{}
			marker := t.Name()
			ctx := context.WithValue(context.Background(), publishReadyLoaderQueryContextKey{}, marker)
			callbackName := "test:publish-ready-loader-query-error-" + strings.ReplaceAll(t.Name(), "/", "_")
			var loaderTransaction gorm.ConnPool
			injectedOnce := false
			if err := fixture.harness.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement == nil || tx.Statement.Schema == nil ||
					tx.Statement.Context.Value(publishReadyLoaderQueryContextKey{}) != marker {
					return
				}
				if tx.Statement.Schema.Table == "backup_asset_export_quota_buckets" && loaderTransaction == nil &&
					persistentStatementHasUpdateLock(tx) {
					loaderTransaction = tx.Statement.ConnPool
					return
				}
				if tx.Statement.Schema.Table != test.table || injectedOnce || loaderTransaction == nil ||
					tx.Statement.ConnPool != loaderTransaction || !persistentStatementHasUpdateLock(tx) {
					return
				}
				injectedOnce = true
				_ = tx.AddError(injected)
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := fixture.harness.db.Callback().Query().Remove(callbackName); err != nil {
					t.Errorf("remove publish-ready loader database query callback: %v", err)
				}
			})

			_, err := fixture.worker.PublishReady(ctx, PersistentPublishRequest{
				JobID: fixture.jobID, AttemptID: fixture.attemptID, FenceToken: fixture.fenceToken, ArtifactID: fixture.artifactID,
			})
			if loaderTransaction == nil {
				t.Fatal("publish-ready loader transaction callback was not reached")
			}
			if !injectedOnce {
				t.Fatalf("publish-ready loader %s query callback was not reached", test.name)
			}
			if !errors.Is(err, injected) || errors.Is(err, ErrAttemptFenceLost) || errors.Is(err, ErrUnavailable) {
				t.Fatalf("publish-ready loader %s database query error=%v, want injected infrastructure error without authority or availability sentinel", test.name, err)
			}
			if after := loadSealedPersistenceStateFor(t, fixture.harness.db, fixture.jobID, fixture.attemptID); !reflect.DeepEqual(after, before) {
				t.Fatalf("publish-ready loader %s database query error changed durable state: before=%+v after=%+v", test.name, before, after)
			}
		})
	}
}

func TestPersistentWorkerDiscardsZeroByteSpoolWhenPostStatSourceDrifts(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	item := frozenItemFixture()
	item.LogicalSize = 0
	item.ArchiveComponents = []string{"root", "empty-drift.txt"}
	selection, err := FreezeSelection([]FrozenItem{item}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 96, Role: "admin"}, Selection: selection,
		IdempotencyKey: "persistent-worker-zero-byte-drift", ArchiveFormat: ArchiveZIP,
		ArchiveProfile: ArchiveProfileZIPDeflateV1,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Second)
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-zero-byte-drift"})
	if err != nil {
		t.Fatal(err)
	}
	var itemAttempt model.BackupAssetExportItemAttempt
	if err := harness.db.Where("job_id = ? AND attempt_id = ?", created.JobID, claim.AttemptID).Take(&itemAttempt).Error; err != nil {
		t.Fatal(err)
	}
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	source := &persistentSourceResolverFake{statDrift: true}
	broker, err := content.NewAttemptBroker(source, budget, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: harness.db, Keys: backupasset.NewKeyring(harness.db, func() time.Time { return clock }),
		Broker: broker, Metadata: &metadataValidatorFake{}, Store: store, AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.SpoolItem(context.Background(), PersistentSpoolItemRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: itemAttempt.ItemID,
	}); !errors.Is(err, content.ErrAttemptSourceChanged) {
		t.Fatalf("zero-byte post-stat drift error=%v", err)
	}
	if len(source.requests) != 2 || source.requests[0].Mode != content.SourceModeStat || source.requests[1].Mode != content.SourceModeStat {
		t.Fatalf("zero-byte source requests=%+v", source.requests)
	}
	if err := harness.db.First(&itemAttempt, "id = ?", itemAttempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if itemAttempt.State != string(ItemPending) || itemAttempt.SpoolLocator != "" || itemAttempt.SpoolSize != 0 ||
		itemAttempt.LogicalBytes != 0 || itemAttempt.ProviderBytes != 0 {
		t.Fatalf("zero-byte drift persisted item attempt=%+v", itemAttempt)
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stage-") || filepath.Ext(entry.Name()) == ".xrs" || filepath.Ext(entry.Name()) == ".xre" {
			t.Fatalf("zero-byte drift left artifact=%s", entry.Name())
		}
	}
}

func TestPersistentWorkerSealsAndPublishesCurrentFenceWithPartialReport(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	fileItem := frozenItemFixture()
	filePayload := bytes.Repeat([]byte("z"), int(fileItem.LogicalSize))
	linkItem := frozenItemFixture()
	linkItem.Ref.EntryID = strings.Repeat("b", 64)
	linkItem.EntryFingerprint = "link-entry-fingerprint-v1"
	linkItem.EntryType = backupasset.CatalogEntrySymlink
	linkItem.LogicalSize = 0
	linkItem.MediaType = ""
	linkItem.ArchiveComponents = []string{"root", "link"}
	selection, err := FreezeSelection([]FrozenItem{linkItem, fileItem}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 99, Role: "admin"}, Selection: selection,
		IdempotencyKey: "persistent-worker-seal-publish", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Second)
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-seal"})
	if err != nil {
		t.Fatal(err)
	}
	var fileRow model.BackupAssetExportItem
	if err := harness.db.Where("job_id = ? AND entry_type = ?", created.JobID, backupasset.CatalogEntryFile).Take(&fileRow).Error; err != nil {
		t.Fatal(err)
	}
	var fileAttempt model.BackupAssetExportItemAttempt
	if err := harness.db.Where("attempt_id = ? AND item_id = ?", claim.AttemptID, fileRow.ID).Take(&fileAttempt).Error; err != nil {
		t.Fatal(err)
	}
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	source := &persistentSourceResolverFake{payload: filePayload, providerBytes: fileItem.LogicalSize - 7}
	contentBroker, err := content.NewAttemptBroker(source, budget, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	metadata := &metadataValidatorFake{}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ring := backupasset.NewKeyring(harness.db, func() time.Time { return clock })
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: harness.db, Keys: ring, Broker: contentBroker, Metadata: metadata, Store: store,
		AttemptWork: NewAttemptWorkRegistry(),
		Now:         func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.SpoolItem(context.Background(), PersistentSpoolItemRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: fileRow.ID,
	}); err != nil {
		t.Fatal(err)
	}
	var liveSpool model.BackupAssetExportItemAttempt
	if err := harness.db.First(&liveSpool, "id = ?", fileAttempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !lowerHex(liveSpool.SpoolDigest, 64) || liveSpool.SpoolSize <= 0 || !validPersistedItemSpoolLocator(liveSpool.SpoolLocator) {
		t.Fatalf("live item spool tuple=%+v", liveSpool)
	}
	sealed, err := worker.SealArchive(context.Background(), PersistentSealRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Report.ResultKind != ResultPartial || sealed.Report.Packed != 1 || sealed.Report.Skipped != 1 ||
		sealed.Report.ProviderBytes != source.providerBytes || sealed.CiphertextBytes <= 0 || sealed.Locator == "" {
		t.Fatalf("sealed=%+v", sealed)
	}
	var job model.BackupAssetExportJob
	if err := harness.db.First(&job, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(ExecutionSealing) || job.ResultKind != string(ResultPartial) || job.ReadyAt != nil {
		t.Fatalf("job after seal=%+v", job)
	}
	var artifact model.BackupAssetExportArtifact
	if err := harness.db.First(&artifact, "id = ?", sealed.ArtifactID).Error; err != nil {
		t.Fatal(err)
	}
	if artifact.State != "sealed" || artifact.AttemptID != claim.AttemptID ||
		!bytes.Equal(artifact.NoncePrefix, claim.NoncePrefix) || artifact.ExpiresAt != nil {
		t.Fatalf("artifact after seal=%+v", artifact)
	}
	var retiredSpool model.BackupAssetExportItemAttempt
	if err := harness.db.First(&retiredSpool, "id = ?", fileAttempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if retiredSpool.SpoolLocator != "" || retiredSpool.SpoolDigest != liveSpool.SpoolDigest ||
		retiredSpool.SpoolSize != liveSpool.SpoolSize {
		t.Fatalf("retired item spool evidence=%+v, live=%+v", retiredSpool, liveSpool)
	}
	loader, err := NewPersistentAttemptLoader(harness.db, ring, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := loader.Load(context.Background(), PersistentAttemptLoadRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := store.OpenSealed(sealed.Locator)
	if err != nil {
		t.Fatal(err)
	}
	var archivePlaintext bytes.Buffer
	if _, err := DecryptStream(context.Background(), &archivePlaintext, reader, snapshot.DEK, CipherBinding{
		ExportID: created.JobID, SelectionDigest: selection.Digest,
		ArchiveProfile: "zip_deflate_v1", FormatVersion: 1, AttemptFenceDigest: claim.FenceDigest,
		Purpose: CipherPurposeFinalArchive,
	}); err != nil {
		_ = reader.Close()
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	zipArchive, err := zip.NewReader(bytes.NewReader(archivePlaintext.Bytes()), int64(archivePlaintext.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zipArchive.File) != 2 || zipArchive.File[1].Name != "xirang-export-report.v1.json" {
		t.Fatalf("zip entries=%+v", zipArchive.File)
	}

	badFence := append([]byte(nil), claim.FenceToken...)
	badFence[0] ^= 0xff
	if _, err := worker.PublishReady(context.Background(), PersistentPublishRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: badFence, ArtifactID: sealed.ArtifactID,
	}); !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("bad-fence publish error=%v", err)
	}
	if err := harness.db.First(&job, "id = ?", created.JobID).Error; err != nil || job.ExecutionState != string(ExecutionSealing) {
		t.Fatalf("bad publish changed job=%+v err=%v", job, err)
	}
	restarted, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: harness.db, Keys: ring, Broker: contentBroker, Metadata: metadata, Store: store,
		AttemptWork: NewAttemptWorkRegistry(),
		Now:         func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	published, err := restarted.ReconcileJob(context.Background(), PersistentReconcileRequest{JobID: created.JobID})
	if err != nil {
		t.Fatal(err)
	}
	if published.Action != PersistentReconcilePublished || published.ArtifactID != sealed.ArtifactID {
		t.Fatalf("restart reconciliation=%+v", published)
	}
	if !published.ExpiresAt.After(clock) || published.ExpiresAt.After(clock.Add(harness.config.ReadyTTL)) {
		t.Fatalf("published expiry=%s clock=%s", published.ExpiresAt, clock)
	}
	if err := harness.db.First(&job, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(ExecutionReady) || job.ReadyAt == nil || job.ExpiresAt == nil ||
		!job.ExpiresAt.Equal(published.ExpiresAt) {
		t.Fatalf("ready job=%+v published=%+v", job, published)
	}
	var attempt model.BackupAssetExportAttempt
	if err := harness.db.First(&attempt, "id = ?", claim.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.State != string(AttemptSealed) || attempt.IsCurrent {
		t.Fatalf("published attempt=%+v", attempt)
	}
	var sourceLease model.BackupAssetExportSourceLease
	if err := harness.db.Where("job_id = ?", created.JobID).Take(&sourceLease).Error; err != nil {
		t.Fatal(err)
	}
	if sourceLease.State != "active" || sourceLease.ReleasedAt != nil {
		t.Fatalf("publish released source lease=%+v", sourceLease)
	}
}

func TestPersistentWorkerRecoversTamperedSpoolBeforeHeaderAsPartialArchive(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	first := frozenItemFixture()
	second := frozenItemFixture()
	second.Ref.EntryID = strings.Repeat("b", 64)
	second.EntryFingerprint = "second-entry-fingerprint-v1"
	second.ArchiveComponents = []string{"root", "second.txt"}
	selection, err := FreezeSelection([]FrozenItem{first, second}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 99, Role: "admin"}, Selection: selection,
		IdempotencyKey: "persistent-worker-tampered-pre-header", ArchiveFormat: ArchiveZIP, ArchiveProfile: ArchiveProfileZIPDeflateV1,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Second)
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-tampered-pre-header"})
	if err != nil {
		t.Fatal(err)
	}
	var items []model.BackupAssetExportItem
	if err := harness.db.Where("job_id = ?", created.JobID).Order("ordinal ASC").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items=%+v", items)
	}
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	source := &persistentSourceResolverFake{
		payload: bytes.Repeat([]byte("z"), int(first.LogicalSize)), providerBytes: first.LogicalSize,
	}
	broker, err := content.NewAttemptBroker(source, budget, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: harness.db, Keys: backupasset.NewKeyring(harness.db, func() time.Time { return clock }),
		Broker: broker, Metadata: &metadataValidatorFake{}, Store: store, AttemptWork: NewAttemptWorkRegistry(),
		Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if _, err := worker.SpoolItem(context.Background(), PersistentSpoolItemRequest{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: item.ID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	var itemAttempts []model.BackupAssetExportItemAttempt
	if err := harness.db.Where("job_id = ? AND attempt_id = ?", created.JobID, claim.AttemptID).
		Order("item_id ASC").Find(&itemAttempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(itemAttempts) != 2 || itemAttempts[0].State != string(ItemRead) || itemAttempts[1].State != string(ItemRead) {
		t.Fatalf("spooled item attempts=%+v", itemAttempts)
	}
	tampered := itemAttempts[0]
	tamperedPath := filepath.Join(store.root, tampered.SpoolLocator)
	spool, err := os.OpenFile(tamperedPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	var firstByte [1]byte
	if _, err := spool.ReadAt(firstByte[:], 0); err != nil {
		_ = spool.Close()
		t.Fatal(err)
	}
	firstByte[0] ^= 0x80
	if _, err := spool.WriteAt(firstByte[:], 0); err != nil {
		_ = spool.Close()
		t.Fatal(err)
	}
	if err := errors.Join(spool.Sync(), spool.Close()); err != nil {
		t.Fatal(err)
	}

	_, err = worker.SealArchive(context.Background(), PersistentSealRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	var failure *PreHeaderSpoolFailure
	if !errors.As(err, &failure) || !errors.Is(err, ErrCipherTampered) {
		t.Fatalf("tampered spool error=%v, want recoverable pre-header failure", err)
	}
	if _, err := os.Lstat(tamperedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tampered spool remains after pre-header recovery: %v", err)
	}
	if err := coordinator.Checkpoint(context.Background(), AttemptCheckpoint{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: tampered.ItemID,
		State: ItemFailed, ProviderBytes: tampered.ProviderBytes, ErrorCategory: "internal_failure", PreHeaderSpoolRecovered: true,
	}); err != nil {
		t.Fatal(err)
	}

	sealed, err := worker.SealArchive(context.Background(), PersistentSealRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Report.ResultKind != ResultPartial || sealed.Report.Packed != 1 || sealed.Report.Failed != 1 {
		t.Fatalf("sealed report=%+v", sealed.Report)
	}
	if _, err := worker.PublishReady(context.Background(), PersistentPublishRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ArtifactID: sealed.ArtifactID,
	}); err != nil {
		t.Fatal(err)
	}
	itemAttempts[0] = model.BackupAssetExportItemAttempt{}
	if err := harness.db.First(&itemAttempts[0], "id = ?", tampered.ID).Error; err != nil {
		t.Fatal(err)
	}
	if itemAttempts[0].State != string(ItemFailed) || itemAttempts[0].SpoolLocator != "" || itemAttempts[0].SpoolDigest != "" ||
		itemAttempts[0].SpoolSize != 0 || itemAttempts[0].ReadAt != nil || itemAttempts[0].FinishedAt == nil {
		t.Fatalf("failed tampered spool state=%+v", itemAttempts[0])
	}
	var job model.BackupAssetExportJob
	if err := harness.db.First(&job, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(ExecutionReady) || job.ResultKind != string(ResultPartial) || job.PackedCount != 1 || job.FailedCount != 1 {
		t.Fatalf("published partial job=%+v", job)
	}
}

func TestPersistentWorkerTreatsAuthenticatedSpoolReopenLossAsPartial(t *testing.T) {
	second := frozenItemFixture()
	second.Ref.EntryID = strings.Repeat("b", 64)
	second.EntryFingerprint = "authenticated-reopen-second-entry-v1"
	second.ArchiveComponents = []string{"root", "second.txt"}
	fixture := createPersistentReadSpoolFixture(t, second)
	defer fixture.snapshot.ClearKeyMaterial()

	var secondRow model.BackupAssetExportItem
	if err := fixture.harness.db.Where("job_id = ? AND entry_id = ?", fixture.jobID, second.Ref.EntryID).
		Take(&secondRow).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.worker.SpoolItem(context.Background(), PersistentSpoolItemRequest{
		JobID: fixture.jobID, AttemptID: fixture.attemptID, FenceToken: fixture.fenceToken, ItemID: secondRow.ID,
	}); err != nil {
		t.Fatal(err)
	}
	var target model.BackupAssetExportItemAttempt
	if err := fixture.harness.db.Where("job_id = ? AND attempt_id = ? AND item_id = ?", fixture.jobID, fixture.attemptID, secondRow.ID).
		Take(&target).Error; err != nil {
		t.Fatal(err)
	}
	if target.State != string(ItemRead) || target.SpoolLocator == "" {
		t.Fatalf("target spool=%+v", target)
	}

	store := fixture.worker.store
	originalOpen := store.openStoreEntryDescriptor
	targetOpenCount := 0
	removedAtReopen := false
	store.openStoreEntryDescriptor = func(directory int, name string, how *unix.OpenHow) (int, error) {
		if name == target.SpoolLocator {
			targetOpenCount++
			if targetOpenCount == 2 {
				if err := os.Remove(filepath.Join(store.root, name)); err != nil {
					return -1, err
				}
				removedAtReopen = true
			}
		}
		return originalOpen(directory, name, how)
	}

	sealed, err := fixture.worker.SealArchive(context.Background(), persistentSealRequest(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if !removedAtReopen || targetOpenCount != 2 {
		t.Fatalf("authenticated reopen boundary removed=%t opens=%d", removedAtReopen, targetOpenCount)
	}
	if sealed.Report.ResultKind != ResultPartial || sealed.Report.Packed != 1 || sealed.Report.Failed != 1 {
		t.Fatalf("sealed report=%+v", sealed.Report)
	}
	var failedReport ArchiveItemReport
	for _, item := range sealed.Report.Items {
		if item.ItemID == secondRow.ID {
			failedReport = item
			break
		}
	}
	if failedReport.State != ItemFailed || failedReport.MemberPath != "" || failedReport.LogicalBytes != 0 ||
		failedReport.ProviderBytes != target.ProviderBytes || failedReport.ErrorCategory != "internal_failure" {
		t.Fatalf("authenticated reopen report=%+v", failedReport)
	}
	var persisted model.BackupAssetExportItemAttempt
	if err := fixture.harness.db.First(&persisted, "id = ?", target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ItemFailed) || persisted.SpoolLocator != "" || persisted.SpoolDigest != "" ||
		persisted.SpoolSize != 0 || persisted.LogicalBytes != 0 || persisted.ProviderBytes != target.ProviderBytes ||
		persisted.ErrorCategory != "internal_failure" || persisted.ReadAt != nil || persisted.FinishedAt == nil {
		t.Fatalf("authenticated reopen persisted item=%+v", persisted)
	}
}

func TestPersistentWorkerReconcileOrphansPurgesOnlyPublishedSpoolAndArtifact(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	root := filepath.Join(t.TempDir(), "exports")
	store, err := OpenStore(StoreConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	exportID := strings.Repeat("1", 32)
	attemptID := strings.Repeat("2", 32)
	itemAttemptID := strings.Repeat("3", 32)

	beforeAnonymous := snapshotExportStoreTree(t, root)
	stage, err := store.CreateStaging(exportID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stage.File.Write([]byte("orphan-stage")); err != nil {
		t.Fatal(err)
	}
	if err := stage.File.Close(); err != nil {
		t.Fatal(err)
	}
	assertExportStoreTreeUnchanged(t, root, beforeAnonymous)
	finalStage, err := store.CreateStaging(exportID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := finalStage.File.Write([]byte("orphan-final")); err != nil {
		t.Fatal(err)
	}
	finalLocator, err := store.Seal(finalStage)
	if err != nil {
		t.Fatal(err)
	}
	spoolStage, err := store.CreateItemSpool(exportID, attemptID, itemAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spoolStage.File.Write([]byte("orphan-spool")); err != nil {
		t.Fatal(err)
	}
	spoolLocator, err := store.Seal(spoolStage)
	if err != nil {
		t.Fatal(err)
	}

	clock := time.Now().UTC().Truncate(time.Second)
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: harness.db, Keys: backupasset.NewKeyring(harness.db, func() time.Time { return clock }),
		Broker: &workerBrokerFake{}, Metadata: &metadataValidatorFake{}, Store: store,
		AttemptWork: NewAttemptWorkRegistry(),
		Now:         func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	purged, err := worker.ReconcileOrphans(context.Background())
	if err != nil || purged != 2 {
		t.Fatalf("orphan reconciliation purged=%d err=%v", purged, err)
	}
	for _, name := range []string{finalLocator, spoolLocator} {
		if _, err := os.Lstat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("orphan %q remains: %v", name, err)
		}
	}
}

type referencedStoreSnapshotContextKey struct{}

type referencedStoreSnapshotObserver struct {
	ctx       context.Context
	taken     chan struct{}
	takenOnce sync.Once
}

func installReferencedStoreSnapshotObserver(t *testing.T, db *gorm.DB) *referencedStoreSnapshotObserver {
	t.Helper()
	marker := t.Name()
	observer := &referencedStoreSnapshotObserver{
		ctx:   context.WithValue(context.Background(), referencedStoreSnapshotContextKey{}, marker),
		taken: make(chan struct{}),
	}
	callbackName := "test:referenced_store_snapshot:" + strings.ReplaceAll(marker, "/", "_")
	if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != "backup_asset_export_attempts" ||
			tx.Statement.Context.Value(referencedStoreSnapshotContextKey{}) != marker {
			return
		}
		observer.takenOnce.Do(func() { close(observer.taken) })
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove referenced Store snapshot callback: %v", err)
		}
	})
	return observer
}

func waitForReferencedStoreSnapshot(t *testing.T, observer *referencedStoreSnapshotObserver, operation string) {
	t.Helper()
	select {
	case <-observer.taken:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not load the referenced Store snapshot", operation)
	}
}

type publicationSweepGateBarrier struct {
	reached     chan struct{}
	allow       chan struct{}
	reachedOnce sync.Once
	allowOnce   sync.Once
}

func installPublicationSweepGateBarrier(t *testing.T, store *Store) *publicationSweepGateBarrier {
	t.Helper()
	barrier := &publicationSweepGateBarrier{reached: make(chan struct{}), allow: make(chan struct{})}
	original := store.beforePublicationSweepGate
	store.beforePublicationSweepGate = func() {
		if original != nil {
			original()
		}
		barrier.reachedOnce.Do(func() {
			close(barrier.reached)
			<-barrier.allow
		})
	}
	t.Cleanup(func() {
		barrier.proceed()
		store.beforePublicationSweepGate = original
	})
	return barrier
}

func (barrier *publicationSweepGateBarrier) proceed() {
	barrier.allowOnce.Do(func() { close(barrier.allow) })
}

func waitForPublicationSweepGate(t *testing.T, barrier *publicationSweepGateBarrier, operation string) {
	t.Helper()
	select {
	case <-barrier.reached:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not reach the publication sweep gate", operation)
	}
}

func waitForPublicationSweepGateWriter(t *testing.T, store *Store, operation string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		if store.publicationGate.TryRLock() {
			store.publicationGate.RUnlock()
		} else {
			return
		}
		runtime.Gosched()
		select {
		case <-deadline.C:
			t.Fatalf("%s did not wait for the publication sweep gate", operation)
		default:
		}
	}
}

func requireReferencedStoreSnapshotQueuedBehindPublication(t *testing.T, observer *referencedStoreSnapshotObserver, operation string) {
	t.Helper()
	select {
	case <-observer.taken:
		t.Fatalf("%s loaded the referenced Store snapshot before publication persisted", operation)
	default:
	}
}

type publishedDirectoryBarrier struct {
	linked      chan struct{}
	release     chan struct{}
	linkedOnce  sync.Once
	releaseOnce sync.Once
}

func installPublishedDirectoryBarrier(t *testing.T, store *Store) *publishedDirectoryBarrier {
	t.Helper()
	barrier := &publishedDirectoryBarrier{linked: make(chan struct{}), release: make(chan struct{})}
	original := store.syncPublishedDirectory
	store.syncPublishedDirectory = func(fd int) error {
		barrier.linkedOnce.Do(func() {
			close(barrier.linked)
			<-barrier.release
		})
		return original(fd)
	}
	t.Cleanup(func() {
		barrier.unblock()
		store.syncPublishedDirectory = original
	})
	return barrier
}

func (barrier *publishedDirectoryBarrier) unblock() {
	barrier.releaseOnce.Do(func() { close(barrier.release) })
}

func waitForPublishedDirectory(t *testing.T, barrier *publishedDirectoryBarrier, publication string) {
	t.Helper()
	select {
	case <-barrier.linked:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not reach the published-directory sync barrier", publication)
	}
}

func TestPersistentWorkerReconcileOrphansRetainsSpoolPublishedAfterReferenceSnapshot(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	item := frozenItemFixture()
	created, claim, itemAttempt := createClaimedExportForAttemptBudget(
		t, harness, 118, "reconcile-orphans-publication-snapshot-spool",
	)
	clock := harness.service.now().UTC()
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	broker, err := content.NewAttemptBroker(
		&persistentSourceResolverFake{payload: bytes.Repeat([]byte("s"), int(item.LogicalSize)), providerBytes: item.LogicalSize},
		budget,
		func() time.Time { return clock },
	)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: harness.db, Keys: backupasset.NewKeyring(harness.db, func() time.Time { return clock }),
		Broker: broker, Metadata: &metadataValidatorFake{}, Store: store, AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}

	type reconcileResult struct {
		purged int
		err    error
	}
	type spoolResult struct {
		result PersistentSpoolResult
		err    error
	}
	observer := installReferencedStoreSnapshotObserver(t, harness.db)
	gate := installPublicationSweepGateBarrier(t, store)
	reconciled := make(chan reconcileResult, 1)
	go func() {
		purged, reconcileErr := worker.ReconcileOrphans(observer.ctx)
		reconciled <- reconcileResult{purged: purged, err: reconcileErr}
	}()
	waitForPublicationSweepGate(t, gate, "orphan reconciliation")

	publication := installPublishedDirectoryBarrier(t, store)
	spooled := make(chan spoolResult, 1)
	go func() {
		result, spoolErr := worker.SpoolItem(context.Background(), PersistentSpoolItemRequest{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: itemAttempt.ItemID,
		})
		spooled <- spoolResult{result: result, err: spoolErr}
	}()
	waitForPublishedDirectory(t, publication, "spool publication")
	gate.proceed()
	waitForPublicationSweepGateWriter(t, store, "orphan reconciliation")
	requireReferencedStoreSnapshotQueuedBehindPublication(t, observer, "orphan reconciliation")
	publication.unblock()

	var spool spoolResult
	select {
	case spool = <-spooled:
		if spool.err != nil {
			t.Fatal(spool.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("spool publication did not resume after the directory sync barrier was released")
	}
	waitForReferencedStoreSnapshot(t, observer, "orphan reconciliation")
	select {
	case result := <-reconciled:
		if result.err != nil || result.purged != 0 {
			t.Fatalf("orphan reconciliation result=%+v, want no stale-reference purge", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("orphan reconciliation did not resume after spool publication committed")
	}
	var persisted model.BackupAssetExportItemAttempt
	if err := harness.db.First(&persisted, "id = ?", itemAttempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.SpoolLocator != spool.result.Locator || persisted.State != string(ItemRead) {
		t.Fatalf("persisted spool=%+v result=%+v", persisted, spool.result)
	}
	reader, err := store.OpenSealed(spool.result.Locator)
	if err != nil {
		t.Fatalf("spool published after the stale reconciliation snapshot was removed: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeUnreferencedStoreLoadsReferencesOnlyAfterPublicationCommit(t *testing.T) {
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	staging, err := store.CreateStaging(strings.Repeat("1", 32), strings.Repeat("2", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := staging.File.Write([]byte("published-before-reference")); err != nil {
		t.Fatal(err)
	}
	locator, releasePublication, err := store.sealWithPublicationPin(staging)
	if err != nil {
		t.Fatal(err)
	}
	defer releasePublication()

	var referenceMu sync.Mutex
	referenceLoads := 0
	publicationCommitted := false
	loadReferences := func(context.Context) (map[string]struct{}, error) {
		referenceMu.Lock()
		defer referenceMu.Unlock()
		referenceLoads++
		if !publicationCommitted {
			return map[string]struct{}{}, nil
		}
		return map[string]struct{}{locator: {}}, nil
	}

	type purgeResult struct {
		count int
		err   error
	}
	gate := installPublicationSweepGateBarrier(t, store)
	purged := make(chan purgeResult, 1)
	go func() {
		count, purgeErr := purgeUnreferencedStore(context.Background(), store, loadReferences)
		purged <- purgeResult{count: count, err: purgeErr}
	}()
	waitForPublicationSweepGate(t, gate, "orphan reconciliation")
	gate.proceed()
	waitForPublicationSweepGateWriter(t, store, "orphan reconciliation")

	referenceMu.Lock()
	if referenceLoads != 0 {
		referenceMu.Unlock()
		t.Fatalf("reference snapshot loads=%d before publication commit", referenceLoads)
	}
	publicationCommitted = true
	referenceMu.Unlock()
	releasePublication()
	select {
	case result := <-purged:
		if result.err != nil || result.count != 0 {
			t.Fatalf("orphan reconciliation result=%+v, want retained publication", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("orphan reconciliation did not resume after publication commit")
	}
	referenceMu.Lock()
	loads := referenceLoads
	referenceMu.Unlock()
	if loads != 1 {
		t.Fatalf("reference snapshot loads=%d, want one post-commit read", loads)
	}
	if _, err := os.Stat(filepath.Join(store.root, locator)); err != nil {
		t.Fatalf("committed publication was removed by stale orphan reconciliation: %v", err)
	}
}

func TestPurgeUnreferencedStoreCannotSnapshotBeforePublicationAndDeleteAfterCommit(t *testing.T) {
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	staging, err := store.CreateStaging(strings.Repeat("3", 32), strings.Repeat("4", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := staging.File.Write([]byte("publication-after-snapshot")); err != nil {
		t.Fatal(err)
	}

	snapshotTaken := make(chan struct{})
	allowStaleSnapshot := make(chan struct{})
	var allowOnce sync.Once
	allow := func() { allowOnce.Do(func() { close(allowStaleSnapshot) }) }
	t.Cleanup(allow)
	loadReferences := func(context.Context) (map[string]struct{}, error) {
		close(snapshotTaken)
		<-allowStaleSnapshot
		return map[string]struct{}{}, nil
	}
	type purgeResult struct {
		count int
		err   error
	}
	purged := make(chan purgeResult, 1)
	go func() {
		count, purgeErr := purgeUnreferencedStore(context.Background(), store, loadReferences)
		purged <- purgeResult{count: count, err: purgeErr}
	}()
	select {
	case <-snapshotTaken:
	case <-time.After(5 * time.Second):
		t.Fatal("orphan reconciliation did not take its initial reference snapshot")
	}

	type sealedPublication struct {
		locator string
		release func()
		err     error
	}
	sealed := make(chan sealedPublication, 1)
	go func() {
		locator, release, sealErr := store.sealWithPublicationPin(staging)
		sealed <- sealedPublication{locator: locator, release: release, err: sealErr}
	}()
	select {
	case publication := <-sealed:
		if publication.release != nil {
			publication.release()
		}
		allow()
		t.Fatalf("publication completed while orphan cleanup held a stale snapshot: %+v", publication)
	case <-time.After(time.Second):
	}

	allow()
	var publication sealedPublication
	select {
	case publication = <-sealed:
		if publication.err != nil {
			t.Fatal(publication.err)
		}
		if publication.locator != staging.Locator() {
			t.Fatalf("publication locator=%q want %q", publication.locator, staging.Locator())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("publication did not proceed after orphan cleanup released its snapshot gate")
	}
	publication.release()
	select {
	case result := <-purged:
		if result.err != nil || result.count != 0 {
			t.Fatalf("orphan reconciliation result=%+v, want no post-snapshot deletion", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("orphan reconciliation did not complete")
	}
	if _, err := os.Stat(filepath.Join(store.root, publication.locator)); err != nil {
		t.Fatalf("publication was deleted after stale orphan snapshot: %v", err)
	}
}

func TestPersistentWorkerRestartRevokesTamperedArtifactAndLostKeyThroughOrderedLifecycle(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, persistentSealedFixture)
	}{
		{
			name: "tampered artifact",
			mutate: func(t *testing.T, fixture persistentSealedFixture) {
				t.Helper()
				file, err := os.OpenFile(filepath.Join(fixture.store.root, fixture.locator), os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.WriteAt([]byte("!"), 0); err != nil {
					_ = file.Close()
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "lost job key",
			mutate: func(t *testing.T, fixture persistentSealedFixture) {
				t.Helper()
				destroyedAt := fixture.clock
				if err := fixture.harness.db.Model(&model.BackupAssetExportKey{}).
					Where("job_id = ?", fixture.jobID).
					Updates(map[string]any{"state": "lost", "destroyed_at": destroyedAt}).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := createPersistentSealedFixture(t)
			test.mutate(t, fixture)
			port := &lifecyclePortFake{}
			lifecycle, err := NewLifecycle(LifecycleDependencies{
				DB: fixture.harness.db, Port: port, Now: func() time.Time { return fixture.clock },
			})
			if err != nil {
				t.Fatal(err)
			}
			restarted, err := NewPersistentWorker(PersistentWorkerDependencies{
				DB: fixture.harness.db, Keys: fixture.ring, Broker: fixture.broker,
				Metadata: fixture.metadata, Store: fixture.store, Lifecycle: lifecycle,
				AttemptWork: NewAttemptWorkRegistry(),
				Now:         func() time.Time { return fixture.clock },
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := restarted.ReconcileJob(context.Background(), PersistentReconcileRequest{JobID: fixture.jobID})
			if err != nil || result.Action != PersistentReconcileRevoked {
				t.Fatalf("restart revocation=%+v err=%v", result, err)
			}
			want := []string{
				"fence_attempts", "revoke_deliveries", "drain_streams", "destroy_key",
				"release_sources", "purge_ciphertext", "release_store",
			}
			if !slices.Equal(port.calls, want) {
				t.Fatalf("restart cleanup order=%v want=%v", port.calls, want)
			}
			assertLifecycleJobState(t, fixture.harness, fixture.jobID, ExecutionFailed, CleanupPurged)
		})
	}
}

func TestPersistentWorkerRestartRevokesAuthenticatedHeaderChunkSizeMismatch(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	var artifact model.BackupAssetExportArtifact
	if err := fixture.harness.db.Where("id = ?", fixture.artifactID).Take(&artifact).Error; err != nil {
		t.Fatal(err)
	}
	driftedChunkBytes := artifact.ChunkBytes * 2
	if driftedChunkBytes <= 0 || driftedChunkBytes > 8<<20 {
		t.Fatalf("invalid drifted chunk size %d from %d", driftedChunkBytes, artifact.ChunkBytes)
	}
	if err := fixture.harness.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
			Update("chunk_bytes", driftedChunkBytes).Error; err != nil {
			return err
		}
		return tx.Model(&model.BackupAssetExportArtifact{}).Where("id = ?", fixture.artifactID).
			Update("chunk_bytes", driftedChunkBytes).Error
	}); err != nil {
		t.Fatalf("drift persisted chunk size: %v", err)
	}

	port := &lifecyclePortFake{}
	lifecycle, err := NewLifecycle(LifecycleDependencies{
		DB: fixture.harness.db, Port: port, Now: func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: fixture.harness.db, Keys: fixture.ring, Broker: fixture.broker,
		Metadata: fixture.metadata, Store: fixture.store, Lifecycle: lifecycle,
		AttemptWork: NewAttemptWorkRegistry(),
		Now:         func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := restarted.ReconcileJob(context.Background(), PersistentReconcileRequest{JobID: fixture.jobID})
	if err != nil || result.Action != PersistentReconcileRevoked {
		t.Fatalf("header/DB chunk-size reconciliation=%+v err=%v", result, err)
	}
	want := []string{
		"fence_attempts", "revoke_deliveries", "drain_streams", "destroy_key",
		"release_sources", "purge_ciphertext", "release_store",
	}
	if !slices.Equal(port.calls, want) {
		t.Fatalf("chunk-size cleanup order=%v want=%v", port.calls, want)
	}
	assertLifecycleJobState(t, fixture.harness, fixture.jobID, ExecutionFailed, CleanupPurged)
}

func TestPersistentWorkerReadyReconcileVerifiesArtifactBeforeRetainingReadyState(t *testing.T) {
	fixture := createPersistentReadyFixture(t)
	var jobBefore model.BackupAssetExportJob
	var attemptBefore model.BackupAssetExportAttempt
	var itemBefore model.BackupAssetExportItem
	var artifactBefore model.BackupAssetExportArtifact
	if err := fixture.harness.db.First(&jobBefore, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.harness.db.First(&attemptBefore, "id = ?", fixture.attemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.harness.db.Where("job_id = ?", fixture.jobID).Take(&itemBefore).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.harness.db.First(&artifactBefore, "id = ?", fixture.artifactID).Error; err != nil {
		t.Fatal(err)
	}

	restarted, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: fixture.harness.db, Keys: fixture.ring, Broker: fixture.broker,
		Metadata: fixture.metadata, Store: fixture.store, AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := restarted.ReconcileJob(context.Background(), PersistentReconcileRequest{JobID: fixture.jobID})
	if err != nil || result.ReadyIntegrity == nil {
		t.Fatalf("valid ready reconciliation=%+v err=%v", result, err)
	}
	var jobAfter model.BackupAssetExportJob
	var attemptAfter model.BackupAssetExportAttempt
	var itemAfter model.BackupAssetExportItem
	var artifactAfter model.BackupAssetExportArtifact
	if err := fixture.harness.db.First(&jobAfter, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.harness.db.First(&attemptAfter, "id = ?", fixture.attemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.harness.db.Where("job_id = ?", fixture.jobID).Take(&itemAfter).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.harness.db.First(&artifactAfter, "id = ?", fixture.artifactID).Error; err != nil {
		t.Fatal(err)
	}
	if jobAfter.ExecutionState != string(ExecutionReady) || jobAfter.CurrentAttemptID == nil ||
		*jobAfter.CurrentAttemptID != fixture.attemptID || jobAfter.LogicalBytes != jobBefore.LogicalBytes ||
		jobAfter.ProviderBytes != jobBefore.ProviderBytes || jobAfter.ArtifactBytes != jobBefore.ArtifactBytes ||
		attemptAfter.State != attemptBefore.State || attemptAfter.IsCurrent != attemptBefore.IsCurrent ||
		itemAfter.State != itemBefore.State || itemAfter.LogicalBytes != itemBefore.LogicalBytes ||
		itemAfter.ProviderBytes != itemBefore.ProviderBytes || !sameSealedArtifact(artifactAfter, artifactBefore) {
		t.Fatalf("ready tuple changed: job before/after=%+v/%+v attempt=%+v/%+v item=%+v/%+v artifact=%+v/%+v",
			jobBefore, jobAfter, attemptBefore, attemptAfter, itemBefore, itemAfter, artifactBefore, artifactAfter)
	}
}

func TestPersistentWorkerReadyReconcileRoutesMissingTamperAndLostKey(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*testing.T, persistentSealedFixture)
		category string
	}{
		{
			name: "missing artifact row", category: "artifact_missing",
			mutate: func(t *testing.T, fixture persistentSealedFixture) {
				t.Helper()
				if err := fixture.harness.db.Where("id = ?", fixture.artifactID).
					Delete(&model.BackupAssetExportArtifact{}).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "nonsealed artifact row", category: "artifact_tampered",
			mutate: func(t *testing.T, fixture persistentSealedFixture) {
				t.Helper()
				if err := fixture.harness.db.Model(&model.BackupAssetExportArtifact{}).
					Where("id = ?", fixture.artifactID).Update("state", "staged").Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "purged artifact metadata", category: "artifact_tampered",
			mutate: func(t *testing.T, fixture persistentSealedFixture) {
				t.Helper()
				if err := fixture.harness.db.Model(&model.BackupAssetExportArtifact{}).
					Where("id = ?", fixture.artifactID).Update("purged_at", fixture.clock).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "metadata mismatch", category: "artifact_tampered",
			mutate: func(t *testing.T, fixture persistentSealedFixture) {
				t.Helper()
				if err := fixture.harness.db.Model(&model.BackupAssetExportArtifact{}).
					Where("id = ?", fixture.artifactID).Update("ciphertext_size", 1).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "physical artifact missing", category: "artifact_missing",
			mutate: func(t *testing.T, fixture persistentSealedFixture) {
				t.Helper()
				if err := os.Remove(filepath.Join(fixture.store.root, fixture.locator)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unsafe artifact hardlink", category: "artifact_tampered",
			mutate: func(t *testing.T, fixture persistentSealedFixture) {
				t.Helper()
				artifactPath := filepath.Join(fixture.store.root, fixture.locator)
				if err := os.Remove(artifactPath); err != nil {
					t.Fatal(err)
				}
				external := filepath.Join(t.TempDir(), "external-ciphertext")
				if err := os.WriteFile(external, []byte("unsafe replacement ciphertext"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(external, artifactPath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "ready expiry extended beyond frozen source cap", category: "artifact_tampered",
			mutate: func(t *testing.T, fixture persistentSealedFixture) {
				t.Helper()
				var job model.BackupAssetExportJob
				if err := fixture.harness.db.First(&job, "id = ?", fixture.jobID).Error; err != nil {
					t.Fatal(err)
				}
				extended := job.ExpiresAt.Add(time.Minute)
				if err := fixture.harness.db.Transaction(func(tx *gorm.DB) error {
					if err := tx.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
						Update("expires_at", extended).Error; err != nil {
						return err
					}
					return tx.Model(&model.BackupAssetExportArtifact{}).Where("id = ?", fixture.artifactID).
						Update("expires_at", extended).Error
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "attempt fence digest mismatch", category: "artifact_tampered",
			mutate: func(t *testing.T, fixture persistentSealedFixture) {
				t.Helper()
				if err := fixture.harness.db.Model(&model.BackupAssetExportAttempt{}).
					Where("id = ?", fixture.attemptID).Update("fence_digest", strings.Repeat("f", 64)).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "ciphertext tamper", category: "artifact_tampered",
			mutate: func(t *testing.T, fixture persistentSealedFixture) {
				t.Helper()
				file, err := os.OpenFile(filepath.Join(fixture.store.root, fixture.locator), os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.WriteAt([]byte("!"), 0); err != nil {
					_ = file.Close()
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "wrapped DEK tamper", category: "key_unavailable",
			mutate: func(t *testing.T, fixture persistentSealedFixture) {
				t.Helper()
				var key model.BackupAssetExportKey
				if err := fixture.harness.db.Where("job_id = ?", fixture.jobID).Take(&key).Error; err != nil {
					t.Fatal(err)
				}
				wrapped := append([]byte(nil), key.WrappedDEK...)
				wrapped[len(wrapped)-1] ^= 0x80
				if err := fixture.harness.db.Model(&model.BackupAssetExportKey{}).
					Where("id = ?", key.ID).Update("wrapped_dek", wrapped).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "lost KEK", category: "key_unavailable",
			mutate: func(t *testing.T, fixture persistentSealedFixture) {
				t.Helper()
				var key model.BackupAssetExportKey
				if err := fixture.harness.db.Where("job_id = ?", fixture.jobID).Take(&key).Error; err != nil {
					t.Fatal(err)
				}
				if err := fixture.harness.db.Model(&model.WrappedDomainKey{}).
					Where("domain = ? AND version = ?", backupasset.KeyDomainExportStore, key.KEKVersion).
					Updates(map[string]any{"state": string(backupasset.DomainKeyLost), "lost_at": fixture.clock}).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := createPersistentReadyFixture(t)
			test.mutate(t, fixture)
			port := &lifecyclePortFake{}
			lifecycle, err := NewLifecycle(LifecycleDependencies{
				DB: fixture.harness.db, Port: port, Now: func() time.Time { return fixture.clock },
			})
			if err != nil {
				t.Fatal(err)
			}
			restarted, err := NewPersistentWorker(PersistentWorkerDependencies{
				DB: fixture.harness.db, Keys: fixture.ring, Broker: fixture.broker,
				Metadata: fixture.metadata, Store: fixture.store, Lifecycle: lifecycle,
				AttemptWork: NewAttemptWorkRegistry(),
				Now:         func() time.Time { return fixture.clock },
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := restarted.ReconcileJob(context.Background(), PersistentReconcileRequest{JobID: fixture.jobID})
			if err != nil || result.Action != PersistentReconcileRevoked {
				t.Fatalf("ready %s reconciliation=%+v err=%v", test.name, result, err)
			}
			var job model.BackupAssetExportJob
			if err := fixture.harness.db.First(&job, "id = ?", fixture.jobID).Error; err != nil {
				t.Fatal(err)
			}
			if job.ErrorCategory != test.category || job.ExecutionState != string(ExecutionExpired) ||
				job.CleanupState != string(CleanupPurged) {
				t.Fatalf("ready %s job=%+v want category=%s expired/purged", test.name, job, test.category)
			}
			if !slices.Equal(port.calls, []string{
				"fence_attempts", "revoke_deliveries", "drain_streams", "destroy_key",
				"release_sources", "purge_ciphertext", "release_store",
			}) {
				t.Fatalf("ready %s cleanup calls=%v", test.name, port.calls)
			}
		})
	}
}

func TestPersistentWorkerReadyReconcileRevokesForgedFenceBeforeSourceOrArtifactPreflight(t *testing.T) {
	type readyFencePreflightContextKey struct{}

	fixture := createPersistentReadyFixture(t)
	var attempt model.BackupAssetExportAttempt
	if err := fixture.harness.db.First(&attempt, "id = ?", fixture.attemptID).Error; err != nil {
		t.Fatal(err)
	}
	forgedDigest := differentSpoolDigest(attempt.FenceDigest)
	if forgedDigest == attempt.FenceDigest || !lowerHex(forgedDigest, 64) {
		t.Fatalf("forged ready fence digest=%q is not a distinct lower-hex SHA-256 digest", forgedDigest)
	}
	if err := fixture.harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", fixture.attemptID).
		Update("fence_digest", forgedDigest).Error; err != nil {
		t.Fatal(err)
	}

	quota, err := NewQuotaService(fixture.harness.db, func() time.Time { return fixture.clock }, fixture.harness.config.Quota)
	if err != nil {
		t.Fatal(err)
	}
	attemptWork := NewAttemptWorkRegistry()
	port, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
		DB: fixture.harness.db, Delivery: exportBehaviorLifecycleDeliveryStub{}, Sources: fixture.harness.lease,
		Quota: quota, Store: fixture.store, AttemptWork: attemptWork, Now: func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(LifecycleDependencies{
		DB: fixture.harness.db, Port: port, Now: func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	keys := &byVersionKeySpy{Keyring: fixture.ring}
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: fixture.harness.db, Keys: keys, Broker: fixture.broker,
		Metadata: fixture.metadata, Store: fixture.store, Lifecycle: lifecycle,
		AttemptWork: attemptWork, Now: func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}

	marker := t.Name()
	revocationStarted := false
	preRevocationQueries := make(map[string]int)
	queryCallbackName := "test:ready-forged-fence-preflight-query-" + strings.ReplaceAll(marker, "/", "_")
	if err := fixture.harness.db.Callback().Query().Before("gorm:query").Register(queryCallbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Context == nil ||
			tx.Statement.Context.Value(readyFencePreflightContextKey{}) != marker || revocationStarted {
			return
		}
		switch tx.Statement.Schema.Table {
		case "backup_asset_export_source_leases", "recovery_point_leases", "backup_asset_export_artifacts", "backup_asset_export_keys":
			preRevocationQueries[tx.Statement.Schema.Table]++
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.harness.db.Callback().Query().Remove(queryCallbackName); err != nil {
			t.Errorf("remove ready forged-fence query callback: %v", err)
		}
	})
	updateCallbackName := "test:ready-forged-fence-revoke-" + strings.ReplaceAll(marker, "/", "_")
	if err := fixture.harness.db.Callback().Update().Before("gorm:update").Register(updateCallbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Schema != nil && tx.Statement.Context != nil &&
			tx.Statement.Context.Value(readyFencePreflightContextKey{}) == marker &&
			tx.Statement.Schema.Table == "backup_asset_export_jobs" {
			revocationStarted = true
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.harness.db.Callback().Update().Remove(updateCallbackName); err != nil {
			t.Errorf("remove ready forged-fence update callback: %v", err)
		}
	})

	ctx := context.WithValue(context.Background(), readyFencePreflightContextKey{}, marker)
	result, err := worker.ReconcileJob(ctx, PersistentReconcileRequest{JobID: fixture.jobID})
	if err != nil || result.Action != PersistentReconcileRevoked || result.ReadyIntegrity != nil {
		t.Fatalf("forged ready fence reconciliation=%+v err=%v", result, err)
	}
	if !revocationStarted {
		t.Fatal("forged ready fence did not enter lifecycle revocation")
	}
	if len(preRevocationQueries) != 0 {
		t.Fatalf("forged ready fence reached source/artifact/key preflight before revocation: %v", preRevocationQueries)
	}
	if len(keys.versions) != 0 {
		t.Fatalf("forged ready fence reached KMS: key_lookups=%v", keys.versions)
	}

	var job model.BackupAssetExportJob
	var key model.BackupAssetExportKey
	var source model.BackupAssetExportSourceLease
	var foundation model.RecoveryPointLease
	var artifact model.BackupAssetExportArtifact
	if err := fixture.harness.db.First(&job, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.harness.db.Where("job_id = ?", fixture.jobID).Take(&key).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.harness.db.Where("job_id = ?", fixture.jobID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.harness.db.First(&foundation, "id = ?", source.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.harness.db.First(&artifact, "id = ?", fixture.artifactID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(ExecutionExpired) || job.CleanupState != string(CleanupPurged) ||
		job.ErrorCategory != "artifact_tampered" || validReadyDeliveryJob(job, fixture.clock) {
		t.Fatalf("forged ready fence remained publishable: job=%+v", job)
	}
	if key.State != "destroyed" || len(key.WrappedDEK) != 0 || len(key.EnvelopeNonce) != 0 || key.DestroyedAt == nil {
		t.Fatalf("forged ready fence left readable job key: %+v", key)
	}
	if source.State != "released" || source.ReleasedAt == nil || foundation.Status != string(backupasset.LeaseReleased) {
		t.Fatalf("forged ready fence did not release source authority: source=%+v foundation=%+v", source, foundation)
	}
	if artifact.State != "purged" || artifact.PurgedAt == nil {
		t.Fatalf("forged ready fence did not purge artifact metadata: %+v", artifact)
	}
	if _, err := os.Lstat(filepath.Join(fixture.store.root, fixture.locator)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forged ready fence left ciphertext readable: %v", err)
	}
}

func TestPersistentWorkerReadyReconcileRevokesFenceDigestDriftBeforeKMS(t *testing.T) {
	type readyFenceDriftContextKey struct{}

	fixture := createPersistentReadyFixture(t)
	port := &lifecyclePortFake{}
	lifecycle, err := NewLifecycle(LifecycleDependencies{
		DB: fixture.harness.db, Port: port, Now: func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	keys := &byVersionKeySpy{Keyring: fixture.ring}
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: fixture.harness.db, Keys: keys, Broker: fixture.broker,
		Metadata: fixture.metadata, Store: fixture.store, Lifecycle: lifecycle,
		AttemptWork: NewAttemptWorkRegistry(),
		Now:         func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}

	marker := t.Name()
	injected := false
	callbackName := "test:ready-fence-digest-drift-before-kms-" + strings.ReplaceAll(marker, "/", "_")
	if err := fixture.harness.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if injected || tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Context == nil ||
			tx.Statement.Context.Value(readyFenceDriftContextKey{}) != marker ||
			tx.Statement.Schema.Table != "backup_asset_export_keys" || persistentStatementHasUpdateLock(tx) {
			return
		}
		var attempt model.BackupAssetExportAttempt
		if err := fixture.harness.db.First(&attempt, "id = ?", fixture.attemptID).Error; err != nil {
			_ = tx.AddError(err)
			return
		}
		forgedDigest := differentSpoolDigest(attempt.FenceDigest)
		if err := fixture.harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", fixture.attemptID).
			Update("fence_digest", forgedDigest).Error; err != nil {
			_ = tx.AddError(err)
			return
		}
		injected = true
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.harness.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove ready fence-drift callback: %v", err)
		}
	})

	ctx := context.WithValue(context.Background(), readyFenceDriftContextKey{}, marker)
	result, err := worker.ReconcileJob(ctx, PersistentReconcileRequest{JobID: fixture.jobID})
	if !injected {
		t.Fatal("ready fence-digest drift injection was not reached")
	}
	if err != nil || result.Action != PersistentReconcileRevoked || result.ReadyIntegrity != nil {
		t.Errorf("late forged ready fence reconciliation=%+v err=%v, want lifecycle revocation", result, err)
	}
	if len(keys.versions) != 0 {
		t.Errorf("late forged ready fence reached KMS: key_lookups=%v", keys.versions)
	}
	if !slices.Equal(port.calls, []string{
		"fence_attempts", "revoke_deliveries", "drain_streams", "destroy_key",
		"release_sources", "purge_ciphertext", "release_store",
	}) {
		t.Errorf("late forged ready fence cleanup calls=%v", port.calls)
	}
}

func TestPersistentWorkerReadyReconcileRetriesArtifactQueryFailureWithoutCleanup(t *testing.T) {
	fixture := createPersistentReadyFixture(t)
	if err := fixture.harness.db.Migrator().DropTable(&model.BackupAssetExportArtifact{}); err != nil {
		t.Fatal(err)
	}
	port := &lifecyclePortFake{}
	lifecycle, err := NewLifecycle(LifecycleDependencies{
		DB: fixture.harness.db, Port: port, Now: func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: fixture.harness.db, Keys: fixture.ring, Broker: fixture.broker,
		Metadata: fixture.metadata, Store: fixture.store, Lifecycle: lifecycle,
		AttemptWork: NewAttemptWorkRegistry(),
		Now:         func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := restarted.ReconcileJob(context.Background(), PersistentReconcileRequest{JobID: fixture.jobID})
	if err == nil || result != (PersistentReconcileResult{}) {
		t.Fatalf("artifact query failure result=%+v err=%v", result, err)
	}
	if len(port.calls) != 0 {
		t.Fatalf("artifact query failure cleanup calls=%v", port.calls)
	}
}

func TestPersistentWorkerReadyReconcileAcceptsVerifyOnlyKEKAndStopsAtReadyExpiry(t *testing.T) {
	t.Run("verify-only KEK", func(t *testing.T) {
		fixture := createPersistentReadyFixture(t)
		if _, err := fixture.ring.Rotate(context.Background(), backupasset.KeyDomainExportStore, time.Hour); err != nil {
			t.Fatal(err)
		}
		worker, err := NewPersistentWorker(PersistentWorkerDependencies{
			DB: fixture.harness.db, Keys: fixture.ring, Broker: fixture.broker,
			Metadata: fixture.metadata, Store: fixture.store, AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return fixture.clock },
		})
		if err != nil {
			t.Fatal(err)
		}
		if result, err := worker.ReconcileJob(context.Background(), PersistentReconcileRequest{JobID: fixture.jobID}); err != nil || result.ReadyIntegrity == nil || result.Action != "" || result.ArtifactID != "" || !result.ExpiresAt.IsZero() {
			t.Fatalf("verify-only ready reconciliation=%+v err=%v", result, err)
		}
	})

	t.Run("ready expiry before key lookup", func(t *testing.T) {
		fixture := createPersistentReadyFixture(t)
		var job model.BackupAssetExportJob
		if err := fixture.harness.db.First(&job, "id = ?", fixture.jobID).Error; err != nil {
			t.Fatal(err)
		}
		keys := &byVersionKeySpy{Keyring: fixture.ring}
		worker, err := NewPersistentWorker(PersistentWorkerDependencies{
			DB: fixture.harness.db, Keys: keys, Broker: fixture.broker,
			Metadata: fixture.metadata, Store: fixture.store, AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return job.ExpiresAt.UTC() },
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := worker.ReconcileJob(context.Background(), PersistentReconcileRequest{JobID: fixture.jobID})
		if !errors.Is(err, ErrReadyExpired) || result != (PersistentReconcileResult{}) || len(keys.versions) != 0 {
			t.Fatalf("expired ready reconciliation=%+v err=%v key lookups=%v", result, err, keys.versions)
		}
	})
}

func TestReadyIntegrityTokenTreatsMissingReadyExpiryAsInvalid(t *testing.T) {
	tuple := persistentReadyTuple{job: model.BackupAssetExportJob{ID: strings.Repeat("a", 32)}}
	token, err := newReadyIntegrityToken(tuple, func(context.Context) (func() error, error) {
		return func() error { return nil }, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if token == nil || token.state == nil || !token.state.readyExpiry.IsZero() {
		t.Fatalf("missing ready expiry token=%+v", token)
	}
	if readyIntegrityTokenMatches(token, tuple.job, tuple.attempt, tuple.artifact, tuple.key, tuple.sources) {
		t.Fatal("missing ready expiry matched an integrity token")
	}
}

func TestReadyIntegrityDigestBindsEveryAuthoritativeTupleField(t *testing.T) {
	fixture := createPersistentReadyFixture(t)
	tuple, category, err := fixture.worker.loadReadyTuple(context.Background(), fixture.jobID, fixture.clock)
	if err != nil || category != "" {
		t.Fatalf("load genuine ready tuple category=%q err=%v", category, err)
	}
	if len(tuple.sources) != 1 {
		t.Fatalf("ready source count=%d, want 1", len(tuple.sources))
	}

	baseDigest, err := readyIntegrityDigest(tuple.job, tuple.attempt, tuple.artifact, tuple.key, tuple.sources)
	if err != nil {
		t.Fatal(err)
	}
	for _, modelValue := range []struct {
		name  string
		value reflect.Value
	}{
		{name: "job", value: reflect.ValueOf(&tuple.job).Elem()},
		{name: "attempt", value: reflect.ValueOf(&tuple.attempt).Elem()},
		{name: "artifact", value: reflect.ValueOf(&tuple.artifact).Elem()},
		{name: "key", value: reflect.ValueOf(&tuple.key).Elem()},
		{name: "source", value: reflect.ValueOf(&tuple.sources[0]).Elem()},
	} {
		for fieldIndex := 0; fieldIndex < modelValue.value.NumField(); fieldIndex++ {
			fieldIndex := fieldIndex
			fieldName := modelValue.value.Type().Field(fieldIndex).Name
			t.Run(modelValue.name+"/"+fieldName, func(t *testing.T) {
				field := modelValue.value.Field(fieldIndex)
				original := reflect.New(field.Type()).Elem()
				original.Set(field)
				field.Set(changedReadyIntegrityFieldValue(t, field))
				defer field.Set(original)
				got, err := readyIntegrityDigest(tuple.job, tuple.attempt, tuple.artifact, tuple.key, tuple.sources)
				if err != nil {
					t.Fatal(err)
				}
				if got == baseDigest {
					t.Fatalf("ready integrity digest did not bind %s.%s", modelValue.name, fieldName)
				}
			})
		}
	}
}

func TestReadyIntegrityDigestDoesNotCollapseInvalidCanonicalMaterial(t *testing.T) {
	first := model.BackupAssetExportJob{
		ID:        strings.Repeat("a", 32),
		CreatedAt: time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
	second := first
	second.CreatedAt = time.Date(12000, time.January, 1, 0, 0, 0, 0, time.UTC)
	if _, err := readyIntegrityDigest(first, model.BackupAssetExportAttempt{}, model.BackupAssetExportArtifact{}, model.BackupAssetExportKey{}, nil); err == nil {
		t.Fatal("out-of-range ready timestamp produced an integrity digest")
	}
	if _, err := readyIntegrityDigest(second, model.BackupAssetExportAttempt{}, model.BackupAssetExportArtifact{}, model.BackupAssetExportKey{}, nil); err == nil {
		t.Fatal("second out-of-range ready timestamp produced an integrity digest")
	}

	first.CreatedAt = time.Time{}
	second.CreatedAt = time.Time{}
	first.ArchiveProfile = string([]byte{0xff})
	second.ArchiveProfile = string([]byte{0xfe})
	if _, err := readyIntegrityDigest(first, model.BackupAssetExportAttempt{}, model.BackupAssetExportArtifact{}, model.BackupAssetExportKey{}, nil); err == nil {
		t.Fatal("invalid UTF-8 ready field produced an integrity digest")
	}
	if _, err := readyIntegrityDigest(second, model.BackupAssetExportAttempt{}, model.BackupAssetExportArtifact{}, model.BackupAssetExportKey{}, nil); err == nil {
		t.Fatal("second invalid UTF-8 ready field produced an integrity digest")
	}
}

func TestReadyIntegrityTokenAliasesShareSingleUseState(t *testing.T) {
	jobID := strings.Repeat("a", 32)
	expiresAt := time.Now().UTC().Add(time.Hour)
	verifications := 0
	token, err := newReadyIntegrityToken(
		persistentReadyTuple{job: model.BackupAssetExportJob{ID: jobID, ExpiresAt: &expiresAt}},
		func(context.Context) (func() error, error) {
			verifications++
			return func() error { return nil }, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	alias := *token
	release, err := token.consumeAndPin(context.Background(), jobID)
	if err != nil {
		t.Fatalf("consume original ready integrity token: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release original ready integrity token: %v", err)
	}
	aliasRelease, aliasErr := alias.consumeAndPin(context.Background(), jobID)
	if aliasRelease != nil {
		_ = aliasRelease()
	}
	if !errors.Is(aliasErr, ErrAttemptFenceLost) || verifications != 1 {
		t.Fatalf("copied ready integrity token replay returned_release=%t err=%v verifications=%d", aliasRelease != nil, aliasErr, verifications)
	}
}

func TestReadyIntegrityTokenDefersJobDEKLoadUntilConsumption(t *testing.T) {
	fixture := createPersistentReadyFixture(t)
	keys := &byVersionKeySpy{Keyring: fixture.ring}
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: fixture.harness.db, Keys: keys, Broker: fixture.broker,
		Metadata: fixture.metadata, Store: fixture.store, AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.ReconcileJob(context.Background(), PersistentReconcileRequest{JobID: fixture.jobID})
	if err != nil || result.ReadyIntegrity == nil {
		t.Fatalf("mint lazy ready integrity token result=%+v err=%v", result, err)
	}
	if !slices.Equal(keys.versions, []int{1}) {
		t.Fatalf("ready mint key lookups=%v, want initial authentication only", keys.versions)
	}
	release, err := result.ReadyIntegrity.consumeAndPin(context.Background(), fixture.jobID)
	if err != nil {
		t.Fatalf("consume lazy ready integrity token: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release lazy ready integrity token: %v", err)
	}
	if !slices.Equal(keys.versions, []int{1, 1}) {
		t.Fatalf("ready consume key lookups=%v, want deferred reauthentication lookup", keys.versions)
	}
}

func TestReadyIntegrityVerificationClearsCallerOwnedKeyMaterial(t *testing.T) {
	fixture := createPersistentReadyFixture(t)
	keys := &zeroTrackingReadyKeySource{inner: fixture.ring}
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: fixture.harness.db, Keys: keys, Broker: fixture.broker,
		Metadata: fixture.metadata, Store: fixture.store, AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.ReconcileJob(context.Background(), PersistentReconcileRequest{JobID: fixture.jobID})
	if err != nil || result.ReadyIntegrity == nil {
		t.Fatalf("mint zeroizing ready integrity token result=%+v err=%v", result, err)
	}
	if len(keys.returned) != 1 || !allZeroBytes(keys.returned[0]) {
		t.Fatalf("ready mint retained caller-owned KEK material: calls=%d key=%x", len(keys.returned), keys.returned[0])
	}
	release, err := result.ReadyIntegrity.consumeAndPin(context.Background(), fixture.jobID)
	if err != nil {
		t.Fatalf("consume zeroizing ready integrity token: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release zeroizing ready integrity token: %v", err)
	}
	if len(keys.returned) != 2 || !allZeroBytes(keys.returned[1]) {
		t.Fatalf("ready consume retained caller-owned KEK material: calls=%d key=%x", len(keys.returned), keys.returned[len(keys.returned)-1])
	}
}

func TestAttemptCoordinatorPanicJoinsReadyArtifactReleaseError(t *testing.T) {
	harness := newServiceHarness(t)
	jobID := strings.Repeat("a", 32)
	expiresAt := time.Now().UTC().Add(time.Hour)
	panicErr := errors.New("injected source maintenance panic")
	releaseErr := errors.New("injected ready descriptor close failure")
	releases := 0
	token, err := newReadyIntegrityToken(
		persistentReadyTuple{job: model.BackupAssetExportJob{ID: jobID, ExpiresAt: &expiresAt}},
		func(context.Context) (func() error, error) {
			return func() error {
				releases++
				return releaseErr
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { panic(panicErr) }, harness.lease)
	if err != nil {
		t.Fatal(err)
	}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = coordinator.MaintainSourceLeases(context.Background(), SourceLeaseMaintenanceRequest{
			JobID: jobID, ReadyIntegrity: token,
		})
	}()
	recoveredErr, ok := recovered.(error)
	if !ok || !errors.Is(recoveredErr, panicErr) || !errors.Is(recoveredErr, releaseErr) || releases != 1 {
		t.Fatalf("source maintenance panic=%#v releases=%d, want joined panic and release errors", recovered, releases)
	}
}

func changedReadyIntegrityFieldValue(t *testing.T, value reflect.Value) reflect.Value {
	t.Helper()
	changed := reflect.New(value.Type()).Elem()
	switch {
	case value.Type() == reflect.TypeFor[time.Time]():
		changed.Set(reflect.ValueOf(value.Interface().(time.Time).Add(time.Second)))
	case value.Kind() == reflect.String:
		changed.SetString(value.String() + "#")
	case value.Kind() >= reflect.Int && value.Kind() <= reflect.Int64:
		changed.SetInt(value.Int() + 1)
	case value.Kind() >= reflect.Uint && value.Kind() <= reflect.Uint64:
		changed.SetUint(value.Uint() + 1)
	case value.Kind() == reflect.Bool:
		changed.SetBool(!value.Bool())
	case value.Kind() == reflect.Slice && value.Type().Elem().Kind() == reflect.Uint8:
		changed.SetBytes(append(append([]byte(nil), value.Bytes()...), 0x5a))
	case value.Kind() == reflect.Pointer:
		element := reflect.New(value.Type().Elem())
		if !value.IsNil() {
			element.Elem().Set(value.Elem())
		}
		element.Elem().Set(changedReadyIntegrityFieldValue(t, element.Elem()))
		changed.Set(element)
	default:
		t.Fatalf("unsupported ready integrity field type %s", value.Type())
	}
	return changed
}

func TestAttemptCoordinatorRejectsFabricatedReadyIntegrityTokenBeforeSourceMutation(t *testing.T) {
	fixture := createPersistentReadyFixture(t)
	genuine := readyIntegrityForFixture(t, fixture)
	fabricated := &ReadyIntegrityToken{
		state: &readyIntegrityTokenState{
			jobID:       genuine.state.jobID,
			readyExpiry: genuine.state.readyExpiry,
			digest:      genuine.state.digest,
		},
	}
	before := loadSourceAuthoritySnapshot(t, fixture.harness.db, fixture.jobID)
	spy := &sourceLeaseCoordinatorSpy{inner: fixture.harness.lease}
	coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return fixture.clock }, spy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.MaintainSourceLeases(context.Background(), SourceLeaseMaintenanceRequest{
		JobID: fixture.jobID, ReadyIntegrity: fabricated,
	}); !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("fabricated ready token maintenance error=%v, want ErrAttemptFenceLost", err)
	}
	assertSourceMaintenanceNotCalled(t, spy)
	assertSourceAuthorityUnchanged(t, fixture.harness.db, fixture.jobID, before)
}

func TestAttemptCoordinatorReauthenticatesReadyArtifactBeforeEverySourceMutation(t *testing.T) {
	for _, test := range []struct {
		name              string
		mutate            func(*testing.T, persistentSealedFixture)
		wantPhysicalCause error
	}{
		{
			name:              "deleted",
			wantPhysicalCause: ErrStoreObjectAbsent,
			mutate: func(t *testing.T, fixture persistentSealedFixture) {
				t.Helper()
				if err := os.Remove(filepath.Join(fixture.store.root, fixture.locator)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "tampered",
			mutate: func(t *testing.T, fixture persistentSealedFixture) {
				t.Helper()
				artifact, err := os.OpenFile(filepath.Join(fixture.store.root, fixture.locator), os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				var first [1]byte
				if _, err := artifact.ReadAt(first[:], 0); err != nil {
					_ = artifact.Close()
					t.Fatal(err)
				}
				first[0] ^= 0x80
				if _, err := artifact.WriteAt(first[:], 0); err != nil {
					_ = artifact.Close()
					t.Fatal(err)
				}
				if err := errors.Join(artifact.Sync(), artifact.Close()); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := createPersistentReadyFixture(t)
			readyIntegrity := readyIntegrityForFixture(t, fixture)
			test.mutate(t, fixture)
			before := loadSourceAuthoritySnapshot(t, fixture.harness.db, fixture.jobID)
			spy := &sourceLeaseCoordinatorSpy{inner: fixture.harness.lease}
			coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return fixture.clock }, spy)
			if err != nil {
				t.Fatal(err)
			}
			_, maintenanceErr := coordinator.MaintainSourceLeases(context.Background(), SourceLeaseMaintenanceRequest{
				JobID: fixture.jobID, ReadyIntegrity: readyIntegrity,
			})
			if !errors.Is(maintenanceErr, ErrAttemptFenceLost) {
				t.Fatalf("%s ready artifact maintenance error=%v, want ErrAttemptFenceLost", test.name, maintenanceErr)
			}
			if test.wantPhysicalCause != nil && !errors.Is(maintenanceErr, test.wantPhysicalCause) {
				t.Fatalf("%s ready artifact maintenance error=%v, want %v preserved", test.name, maintenanceErr, test.wantPhysicalCause)
			}
			assertSourceMaintenanceNotCalled(t, spy)
			assertSourceAuthorityUnchanged(t, fixture.harness.db, fixture.jobID, before)
		})
	}
}

func TestAttemptCoordinatorRejectsCompleteReadyTupleDriftBeforeSourceMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gorm.DB, persistentSealedFixture) error
	}{
		{
			name: "archive format",
			mutate: func(db *gorm.DB, fixture persistentSealedFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("archive_format", string(ArchiveTAR)).Error
			},
		},
		{
			name: "archive profile",
			mutate: func(db *gorm.DB, fixture persistentSealedFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("archive_profile", "tar_gzip_v1").Error
			},
		},
		{
			name: "owner user",
			mutate: func(db *gorm.DB, fixture persistentSealedFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("owner_user_id", 101).Error
			},
		},
		{
			name: "cleanup state",
			mutate: func(db *gorm.DB, fixture persistentSealedFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("cleanup_state", string(CleanupRevoking)).Error
			},
		},
		{
			name: "execution state",
			mutate: func(db *gorm.DB, fixture persistentSealedFixture) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					UpdateColumn("execution_state", string(ExecutionSealing)).Error
			},
		},
		{
			name: "source renewed at",
			mutate: func(db *gorm.DB, fixture persistentSealedFixture) error {
				return db.Model(&model.BackupAssetExportSourceLease{}).Where("job_id = ?", fixture.jobID).
					UpdateColumn("renewed_at", fixture.clock.Add(time.Second)).Error
			},
		},
		{
			name: "source updated at",
			mutate: func(db *gorm.DB, fixture persistentSealedFixture) error {
				return db.Model(&model.BackupAssetExportSourceLease{}).Where("job_id = ?", fixture.jobID).
					UpdateColumn("updated_at", fixture.clock.Add(time.Second)).Error
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := createPersistentReadyFixture(t)
			readyIntegrity := readyIntegrityForFixture(t, fixture)
			if err := test.mutate(fixture.harness.db, fixture); err != nil {
				t.Fatal(err)
			}
			before := loadSourceAuthoritySnapshot(t, fixture.harness.db, fixture.jobID)
			spy := &sourceLeaseCoordinatorSpy{inner: fixture.harness.lease}
			coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return fixture.clock }, spy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := coordinator.MaintainSourceLeases(context.Background(), SourceLeaseMaintenanceRequest{
				JobID: fixture.jobID, ReadyIntegrity: readyIntegrity,
			}); !errors.Is(err, ErrAttemptFenceLost) {
				t.Fatalf("ready %s drift maintenance error=%v, want ErrAttemptFenceLost", test.name, err)
			}
			assertSourceMaintenanceNotCalled(t, spy)
			assertSourceAuthorityUnchanged(t, fixture.harness.db, fixture.jobID, before)
		})
	}
}

func TestAttemptCoordinatorReadyIntegrityTokenIsSingleHeartbeatProof(t *testing.T) {
	fixture := createPersistentReadyFixture(t)
	readyIntegrity := readyIntegrityForFixture(t, fixture)
	maintenanceNow := fixture.clock.Add(time.Minute)
	sourceLeases, err := backupasset.NewLeaseService(
		fixture.harness.db, func() time.Time { return maintenanceNow }, backupasset.LeaseConfig{
			Duration: fixture.harness.config.LeaseTTL, Heartbeat: fixture.harness.config.LeaseRenewMargin,
			AbsoluteDeadline: 2 * time.Hour,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	spy := &sourceLeaseCoordinatorSpy{inner: sourceLeases}
	coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return maintenanceNow }, spy)
	if err != nil {
		t.Fatal(err)
	}
	request := SourceLeaseMaintenanceRequest{JobID: fixture.jobID, ReadyIntegrity: readyIntegrity}
	if _, err := coordinator.MaintainSourceLeases(context.Background(), request); err != nil {
		t.Fatalf("first ready source maintenance: %v", err)
	}
	afterFirst := loadSourceAuthoritySnapshot(t, fixture.harness.db, fixture.jobID)
	firstCalls := spy.renewCalls + spy.takeoverCalls
	if firstCalls != 1 {
		t.Fatalf("first ready source maintenance calls=%d, want 1", firstCalls)
	}
	if _, err := coordinator.MaintainSourceLeases(context.Background(), request); !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("replayed ready integrity token error=%v, want ErrAttemptFenceLost", err)
	}
	if calls := spy.renewCalls + spy.takeoverCalls; calls != firstCalls {
		t.Fatalf("replayed ready integrity token reached Foundation: calls=%d want=%d", calls, firstCalls)
	}
	assertSourceAuthorityUnchanged(t, fixture.harness.db, fixture.jobID, afterFirst)
}

func TestAttemptCoordinatorPinsReadyArtifactThroughSourceMaintenanceTransaction(t *testing.T) {
	fixture := createPersistentReadyFixture(t)
	readyIntegrity := readyIntegrityForFixture(t, fixture)
	unrelatedStaging, err := fixture.store.CreateStaging(strings.Repeat("7", 32), strings.Repeat("8", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unrelatedStaging.File.Write([]byte("unrelated sealed artifact")); err != nil {
		t.Fatal(err)
	}
	unrelatedLocator, err := fixture.store.Seal(unrelatedStaging)
	if err != nil {
		t.Fatal(err)
	}
	maintenanceNow := fixture.clock.Add(time.Minute)
	sourceLeases, err := backupasset.NewLeaseService(
		fixture.harness.db, func() time.Time { return maintenanceNow }, backupasset.LeaseConfig{
			Duration: fixture.harness.config.LeaseTTL, Heartbeat: fixture.harness.config.LeaseRenewMargin,
			AbsoluteDeadline: 2 * time.Hour,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var proceedOnce sync.Once
	releaseCoordinator := func() { proceedOnce.Do(func() { close(proceed) }) }
	t.Cleanup(releaseCoordinator)
	barrier := &sourceLeaseCoordinatorBarrier{inner: sourceLeases, entered: entered, proceed: proceed}
	coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return maintenanceNow }, barrier)
	if err != nil {
		t.Fatal(err)
	}
	maintenanceDone := make(chan error, 1)
	go func() {
		_, err := coordinator.MaintainSourceLeases(context.Background(), SourceLeaseMaintenanceRequest{
			JobID: fixture.jobID, ReadyIntegrity: readyIntegrity,
		})
		maintenanceDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("source maintenance did not reach the Foundation barrier")
	}

	artifactPath := filepath.Join(fixture.store.root, fixture.locator)
	if _, err := os.Stat(artifactPath); err != nil {
		t.Fatalf("pinned ready artifact is absent before purge: %v", err)
	}
	unrelatedPurgeDone := make(chan error, 1)
	go func() { unrelatedPurgeDone <- fixture.store.Purge(unrelatedLocator) }()
	select {
	case err := <-unrelatedPurgeDone:
		if err != nil {
			releaseCoordinator()
			t.Fatalf("purge unrelated artifact while ready artifact is pinned: %v", err)
		}
	case <-time.After(time.Second):
		releaseCoordinator()
		t.Fatal("pinned ready artifact blocked unrelated artifact purge")
	}
	if _, err := os.Stat(filepath.Join(fixture.store.root, unrelatedLocator)); !errors.Is(err, os.ErrNotExist) {
		releaseCoordinator()
		t.Fatalf("unrelated artifact survived concurrent purge: %v", err)
	}
	purgeDone := make(chan error, 1)
	go func() { purgeDone <- fixture.store.Purge(fixture.locator) }()
	if _, err := os.Stat(artifactPath); err != nil {
		releaseCoordinator()
		t.Fatalf("ready artifact changed while purge waited on pin: %v", err)
	}

	releaseCoordinator()
	select {
	case err := <-maintenanceDone:
		if err != nil {
			t.Fatalf("pinned ready source maintenance: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pinned source maintenance did not complete")
	}
	select {
	case err := <-purgeDone:
		if err != nil {
			t.Fatalf("purge after ready maintenance pin release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("purge remained blocked after ready maintenance completed")
	}
	if _, err := os.Stat(artifactPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ready artifact after released purge stat error=%v, want not exist", err)
	}
	var source model.BackupAssetExportSourceLease
	if err := fixture.harness.db.Where("job_id = ?", fixture.jobID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	if !source.RenewedAt.Equal(maintenanceNow) || !source.UpdatedAt.Equal(maintenanceNow) {
		t.Fatalf("purge completed before source transaction committed: source=%+v", source)
	}
}

func TestReadyArtifactVerifierReleasesStorePinOnPanic(t *testing.T) {
	fixture := createPersistentReadyFixture(t)
	readyIntegrity := readyIntegrityForFixture(t, fixture)
	coordinator, err := NewAttemptCoordinator(
		fixture.harness.db, func() time.Time { return fixture.clock }, fixture.harness.lease,
	)
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("ready artifact verification did not propagate context panic")
			}
		}()
		_, _ = coordinator.MaintainSourceLeases(readyVerifierPanicContext{Context: context.Background()}, SourceLeaseMaintenanceRequest{
			JobID: fixture.jobID, ReadyIntegrity: readyIntegrity,
		})
	}()
	closed := make(chan error, 1)
	go func() { closed <- fixture.store.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close Store after ready verifier panic: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ready artifact verifier leaked the store pin after panic")
	}
}

func TestWithReadyArtifactReleasePreservesOperationAndCloseFailures(t *testing.T) {
	operationErr := ErrCipherTampered
	releaseErr := errors.New("injected ready descriptor close failure")
	if err := withReadyArtifactRelease(func() error { return releaseErr }, func() error { return operationErr }); !errors.Is(err, operationErr) || !errors.Is(err, releaseErr) {
		t.Fatalf("ready verification error=%v, want operation and close failures", err)
	}

	panicErr := errors.New("injected ready verification panic")
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = withReadyArtifactRelease(func() error { return releaseErr }, func() error { panic(panicErr) })
	}()
	recoveredErr, ok := recovered.(error)
	if !ok || !errors.Is(recoveredErr, panicErr) || !errors.Is(recoveredErr, releaseErr) {
		t.Fatalf("ready verification panic=%#v, want panic and close failures", recovered)
	}
}

func TestPersistentWorkerReadyReconcileRevokesSourceFenceLoss(t *testing.T) {
	fixture := createPersistentReadyFixture(t)
	if err := fixture.harness.db.Model(&model.BackupAssetExportSourceLease{}).Where("job_id = ?", fixture.jobID).
		UpdateColumn("fence_hash", strings.Repeat("0", 64)).Error; err != nil {
		t.Fatal(err)
	}
	port := &lifecyclePortFake{}
	lifecycle, err := NewLifecycle(LifecycleDependencies{
		DB: fixture.harness.db, Port: port, Now: func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: fixture.harness.db, Keys: fixture.ring, Broker: fixture.broker,
		Metadata: fixture.metadata, Store: fixture.store, Lifecycle: lifecycle,
		AttemptWork: NewAttemptWorkRegistry(),
		Now:         func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := restarted.ReconcileJob(context.Background(), PersistentReconcileRequest{JobID: fixture.jobID})
	if err != nil || result.Action != PersistentReconcileRevoked {
		t.Fatalf("ready source-fence reconciliation=%+v err=%v", result, err)
	}
	var job model.BackupAssetExportJob
	if err := fixture.harness.db.First(&job, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(ExecutionExpired) || job.ErrorCategory != "source_expired" ||
		job.CleanupState != string(CleanupPurged) {
		t.Fatalf("ready source-fence loss remained publishable: job=%+v", job)
	}
	if !slices.Equal(port.calls, []string{
		"fence_attempts", "revoke_deliveries", "drain_streams", "destroy_key",
		"release_sources", "purge_ciphertext", "release_store",
	}) {
		t.Fatalf("ready source-fence cleanup calls=%v", port.calls)
	}
}

func TestPersistentWorkerReadyReconcileRoutesSourceDeadlineToSourceCleanup(t *testing.T) {
	fixture := createPersistentReadyFixture(t)
	var source model.BackupAssetExportSourceLease
	if err := fixture.harness.db.Where("job_id = ?", fixture.jobID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	var jobBefore model.BackupAssetExportJob
	if err := fixture.harness.db.First(&jobBefore, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	cleanupClock := source.AbsoluteDeadline.UTC()
	if jobBefore.ExpiresAt == nil || !jobBefore.ExpiresAt.Equal(cleanupClock) {
		t.Fatalf("ready source-deadline fixture expiry=%v source=%s", jobBefore.ExpiresAt, cleanupClock)
	}
	port := &lifecyclePortFake{}
	lifecycle, err := NewLifecycle(LifecycleDependencies{
		DB: fixture.harness.db, Port: port, Now: func() time.Time { return cleanupClock },
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: fixture.harness.db, Keys: fixture.ring, Broker: fixture.broker,
		Metadata: fixture.metadata, Store: fixture.store, Lifecycle: lifecycle,
		AttemptWork: NewAttemptWorkRegistry(),
		Now:         func() time.Time { return cleanupClock },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := restarted.ReconcileJob(context.Background(), PersistentReconcileRequest{JobID: fixture.jobID})
	if err != nil || result.Action != PersistentReconcileRevoked {
		t.Fatalf("ready source-deadline reconciliation=%+v err=%v", result, err)
	}
	var jobAfter model.BackupAssetExportJob
	if err := fixture.harness.db.First(&jobAfter, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if jobAfter.ExecutionState != string(ExecutionExpired) || jobAfter.ErrorCategory != "source_expired" ||
		jobAfter.CleanupState != string(CleanupPurged) {
		t.Fatalf("ready source deadline used wrong cleanup route: job=%+v", jobAfter)
	}
}

func TestPersistentWorkerRestartTakesOverExpiredSourceOwnerBeforePublishingSealedArtifact(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	var sourceBefore model.BackupAssetExportSourceLease
	if err := fixture.harness.db.Where("job_id = ?", fixture.jobID).Take(&sourceBefore).Error; err != nil {
		t.Fatal(err)
	}
	past := fixture.clock.Add(-time.Second)
	if err := fixture.harness.db.Model(&model.RecoveryPointLease{}).Where("id = ?", sourceBefore.LeaseID).
		Update("lease_expires_at", past).Error; err != nil {
		t.Fatal(err)
	}
	var itemBefore model.BackupAssetExportItem
	if err := fixture.harness.db.Where("job_id = ?", fixture.jobID).Take(&itemBefore).Error; err != nil {
		t.Fatal(err)
	}
	sourceLeases, err := backupasset.NewLeaseService(fixture.harness.db, func() time.Time { return fixture.clock }, backupasset.LeaseConfig{
		Duration: fixture.harness.config.LeaseTTL, Heartbeat: fixture.harness.config.LeaseRenewMargin,
		AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: fixture.harness.db, Keys: fixture.ring, Broker: fixture.broker,
		Metadata: fixture.metadata, Store: fixture.store, SourceLeases: sourceLeases,
		AttemptWork: NewAttemptWorkRegistry(),
		Now:         func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := restarted.ReconcileJob(context.Background(), PersistentReconcileRequest{JobID: fixture.jobID})
	if err != nil || result.Action != PersistentReconcilePublished || result.ArtifactID != fixture.artifactID {
		t.Fatalf("source-owner restart reconciliation=%+v err=%v", result, err)
	}
	var sourceAfter model.BackupAssetExportSourceLease
	if err := fixture.harness.db.First(&sourceAfter, "id = ?", sourceBefore.ID).Error; err != nil {
		t.Fatal(err)
	}
	if sourceAfter.LeaseAttemptID == sourceBefore.LeaseAttemptID ||
		!sourceAfter.AbsoluteDeadline.Equal(sourceBefore.AbsoluteDeadline) {
		t.Fatalf("source takeover before=%+v after=%+v", sourceBefore, sourceAfter)
	}
	var itemAfter model.BackupAssetExportItem
	if err := fixture.harness.db.First(&itemAfter, "id = ?", itemBefore.ID).Error; err != nil {
		t.Fatal(err)
	}
	if itemAfter.State != itemBefore.State || itemAfter.LogicalBytes != itemBefore.LogicalBytes ||
		itemAfter.ProviderBytes != itemBefore.ProviderBytes {
		t.Fatalf("ready source takeover reset projection: before=%+v after=%+v", itemBefore, itemAfter)
	}
}

func TestPersistentWorkerReconcileRejectsForgedSealingFenceBeforeKeyWorkOrSourceTakeover(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	var attempt model.BackupAssetExportAttempt
	if err := fixture.harness.db.First(&attempt, "id = ?", fixture.attemptID).Error; err != nil {
		t.Fatal(err)
	}
	forgedDigest := differentSpoolDigest(attempt.FenceDigest)
	if forgedDigest == attempt.FenceDigest || !lowerHex(forgedDigest, 64) {
		t.Fatalf("forged fence digest=%q is not a distinct lower-hex SHA-256 digest", forgedDigest)
	}
	if err := fixture.harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", fixture.attemptID).
		Update("fence_digest", forgedDigest).Error; err != nil {
		t.Fatal(err)
	}

	var source model.BackupAssetExportSourceLease
	if err := fixture.harness.db.Where("job_id = ?", fixture.jobID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.harness.db.Model(&model.RecoveryPointLease{}).Where("id = ?", source.LeaseID).
		Update("lease_expires_at", fixture.clock.Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	before := loadSealedPersistenceStateFor(t, fixture.harness.db, fixture.jobID, fixture.attemptID)
	sourceAuthorityBefore := loadSourceAuthoritySnapshot(t, fixture.harness.db, fixture.jobID)

	keyQueryReached := false
	artifactQueryReached := false
	callbackName := "test:reconcile-forged-sealing-fence-key-query-" + strings.ReplaceAll(t.Name(), "/", "_")
	if err := fixture.harness.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil {
			return
		}
		switch tx.Statement.Schema.Table {
		case "backup_asset_export_keys":
			keyQueryReached = true
		case "backup_asset_export_artifacts":
			artifactQueryReached = true
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.harness.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove forged sealing fence key query callback: %v", err)
		}
	})

	sourceLeases, err := backupasset.NewLeaseService(fixture.harness.db, func() time.Time { return fixture.clock }, backupasset.LeaseConfig{
		Duration: fixture.harness.config.LeaseTTL, Heartbeat: fixture.harness.config.LeaseRenewMargin,
		AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceLeaseSpy := &sourceLeaseCoordinatorSpy{inner: sourceLeases}
	keys := &byVersionKeySpy{Keyring: fixture.ring}
	restarted, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: fixture.harness.db, Keys: keys, Broker: fixture.broker,
		Metadata: fixture.metadata, Store: fixture.store, SourceLeases: sourceLeaseSpy,
		AttemptWork: NewAttemptWorkRegistry(),
		Now:         func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := restarted.ReconcileJob(context.Background(), PersistentReconcileRequest{JobID: fixture.jobID})
	if !errors.Is(err, ErrAttemptFenceLost) || result != (PersistentReconcileResult{}) {
		t.Fatalf("forged sealing fence reconciliation=%+v err=%v, want ErrAttemptFenceLost", result, err)
	}
	if keyQueryReached || artifactQueryReached || len(keys.versions) != 0 {
		t.Errorf("forged sealing fence reached artifact/key work: artifact_query=%t key_query=%t key_lookups=%v",
			artifactQueryReached, keyQueryReached, keys.versions)
	}
	if sourceLeaseSpy.renewCalls != 0 || sourceLeaseSpy.takeoverCalls != 0 {
		t.Errorf("forged sealing fence reached Foundation coordinator: renew=%d takeover=%d",
			sourceLeaseSpy.renewCalls, sourceLeaseSpy.takeoverCalls)
	}
	if sourceAuthorityAfter := loadSourceAuthoritySnapshot(t, fixture.harness.db, fixture.jobID); !reflect.DeepEqual(sourceAuthorityAfter, sourceAuthorityBefore) {
		t.Errorf("forged sealing fence mutated source authority: before=%+v after=%+v", sourceAuthorityBefore, sourceAuthorityAfter)
	}
	if after := loadSealedPersistenceStateFor(t, fixture.harness.db, fixture.jobID, fixture.attemptID); !reflect.DeepEqual(after, before) {
		t.Errorf("forged sealing fence reconciliation mutated state: before=%+v after=%+v", before, after)
	} else if after.job.ExecutionState != string(ExecutionSealing) {
		t.Errorf("forged sealing fence transitioned job to %q, want sealing", after.job.ExecutionState)
	}
}

func TestPersistentWorkerReconcileRejectsFenceDigestDriftBeforeSourceTakeover(t *testing.T) {
	type reconcileFenceDigestDriftContextKey struct{}

	fixture := createPersistentSealedFixture(t)
	var source model.BackupAssetExportSourceLease
	if err := fixture.harness.db.Where("job_id = ?", fixture.jobID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.harness.db.Model(&model.RecoveryPointLease{}).Where("id = ?", source.LeaseID).
		Update("lease_expires_at", fixture.clock.Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	sourceAuthorityBefore := loadSourceAuthoritySnapshot(t, fixture.harness.db, fixture.jobID)

	marker := t.Name()
	injected := false
	keyQueryAfterDrift := false
	var expected sealedPersistenceState
	callbackName := "test:reconcile-fence-digest-drift-before-source-takeover-" + strings.ReplaceAll(marker, "/", "_")
	if err := fixture.harness.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Context == nil ||
			tx.Statement.Context.Value(reconcileFenceDigestDriftContextKey{}) != marker ||
			tx.Statement.Schema.Table != "backup_asset_export_keys" {
			return
		}
		if injected {
			keyQueryAfterDrift = true
			return
		}
		if persistentStatementHasUpdateLock(tx) {
			return
		}
		// This query is ReconcileJob's key-row preflight, after its early fence check and before source takeover.
		injected = true
		var attempt model.BackupAssetExportAttempt
		if err := fixture.harness.db.First(&attempt, "id = ?", fixture.attemptID).Error; err != nil {
			_ = tx.AddError(err)
			return
		}
		if err := fixture.harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", fixture.attemptID).
			Update("fence_digest", differentSpoolDigest(attempt.FenceDigest)).Error; err != nil {
			_ = tx.AddError(err)
			return
		}
		expected = loadSealedPersistenceStateFor(t, fixture.harness.db, fixture.jobID, fixture.attemptID)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.harness.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove reconcile fence-digest drift callback: %v", err)
		}
	})

	sourceLeases, err := backupasset.NewLeaseService(fixture.harness.db, func() time.Time { return fixture.clock }, backupasset.LeaseConfig{
		Duration: fixture.harness.config.LeaseTTL, Heartbeat: fixture.harness.config.LeaseRenewMargin,
		AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceLeaseSpy := &sourceLeaseCoordinatorSpy{inner: sourceLeases}
	keys := &byVersionKeySpy{Keyring: fixture.ring}
	restarted, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: fixture.harness.db, Keys: keys, Broker: fixture.broker,
		Metadata: fixture.metadata, Store: fixture.store, SourceLeases: sourceLeaseSpy,
		AttemptWork: NewAttemptWorkRegistry(),
		Now:         func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.WithValue(context.Background(), reconcileFenceDigestDriftContextKey{}, marker)
	result, err := restarted.ReconcileJob(ctx, PersistentReconcileRequest{JobID: fixture.jobID})
	if !injected {
		t.Fatal("reconcile fence-digest drift callback was not reached")
	}
	if !errors.Is(err, ErrAttemptFenceLost) || result != (PersistentReconcileResult{}) {
		t.Fatalf("post-preflight fence-digest drift reconciliation=%+v err=%v, want ErrAttemptFenceLost", result, err)
	}
	if len(keys.versions) != 0 {
		t.Errorf("post-preflight fence-digest drift reached KMS: key_lookups=%v", keys.versions)
	}
	if keyQueryAfterDrift {
		t.Error("post-preflight fence-digest drift reached an additional key-row query")
	}
	if sourceLeaseSpy.renewCalls != 0 || sourceLeaseSpy.takeoverCalls != 0 {
		t.Errorf("post-preflight fence-digest drift reached Foundation coordinator: renew=%d takeover=%d",
			sourceLeaseSpy.renewCalls, sourceLeaseSpy.takeoverCalls)
	}
	if sourceAuthorityAfter := loadSourceAuthoritySnapshot(t, fixture.harness.db, fixture.jobID); !reflect.DeepEqual(sourceAuthorityAfter, sourceAuthorityBefore) {
		t.Errorf("post-preflight fence-digest drift mutated source authority: before=%+v after=%+v", sourceAuthorityBefore, sourceAuthorityAfter)
	}
	if after := loadSealedPersistenceStateFor(t, fixture.harness.db, fixture.jobID, fixture.attemptID); !reflect.DeepEqual(after, expected) {
		t.Errorf("post-preflight fence-digest drift mutated state beyond injected drift: expected=%+v after=%+v", expected, after)
	} else if after.job.ExecutionState != string(ExecutionSealing) {
		t.Errorf("post-preflight fence-digest drift transitioned job to %q, want sealing", after.job.ExecutionState)
	}
}

func TestPersistentWorkerRestartReturnsExpiredSealingAttemptForFreshRebuild(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	expiredAt := fixture.clock.Add(-time.Second)
	if err := fixture.harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", fixture.attemptID).
		Update("lease_expires_at", expiredAt).Error; err != nil {
		t.Fatal(err)
	}
	var jobBefore model.BackupAssetExportJob
	var attemptBefore model.BackupAssetExportAttempt
	var itemAttemptsBefore []model.BackupAssetExportItemAttempt
	if err := fixture.harness.db.First(&jobBefore, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.harness.db.First(&attemptBefore, "id = ?", fixture.attemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.harness.db.Where("job_id = ? AND attempt_id = ?", fixture.jobID, fixture.attemptID).
		Order("id ASC").Find(&itemAttemptsBefore).Error; err != nil {
		t.Fatal(err)
	}
	if len(itemAttemptsBefore) == 0 {
		t.Fatal("expired sealing fixture has no immutable item-attempt history")
	}
	oldNonce := append([]byte(nil), attemptBefore.NoncePrefix...)
	oldFence := append([]byte(nil), attemptBefore.FenceToken...)
	result, err := fixture.worker.ReconcileJob(context.Background(), PersistentReconcileRequest{JobID: fixture.jobID})
	if !errors.Is(err, ErrAttemptLeaseExpired) || result != (PersistentReconcileResult{}) {
		t.Fatalf("expired sealing reconciliation=%+v err=%v", result, err)
	}
	var jobAfter model.BackupAssetExportJob
	var attemptAfter model.BackupAssetExportAttempt
	if err := fixture.harness.db.First(&jobAfter, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.harness.db.First(&attemptAfter, "id = ?", fixture.attemptID).Error; err != nil {
		t.Fatal(err)
	}
	if jobAfter.ExecutionState != string(ExecutionRetryWait) || jobAfter.CurrentAttemptID != nil ||
		jobAfter.ErrorCategory != "heartbeat_lost" || jobAfter.TransitionRevision != jobBefore.TransitionRevision+1 {
		t.Fatalf("expired sealing job before=%+v after=%+v", jobBefore, jobAfter)
	}
	if attemptAfter.State != string(AttemptFailed) || attemptAfter.IsCurrent || attemptAfter.FinishedAt == nil ||
		attemptAfter.FailureCategory != "heartbeat_lost" || attemptAfter.StagingLocator != "" {
		t.Fatalf("expired sealing attempt before=%+v after=%+v", attemptBefore, attemptAfter)
	}
	var artifactCount int64
	if err := fixture.harness.db.Model(&model.BackupAssetExportArtifact{}).
		Where("id = ? AND job_id = ? AND attempt_id = ?", fixture.artifactID, fixture.jobID, fixture.attemptID).
		Count(&artifactCount).Error; err != nil {
		t.Fatal(err)
	}
	if artifactCount != 0 {
		t.Fatalf("expired sealing artifact rows=%d, want cleanup", artifactCount)
	}
	if reader, openErr := fixture.store.OpenSealed(fixture.locator); !errors.Is(openErr, ErrStoreObjectAbsent) {
		if reader != nil {
			_ = reader.Close()
		}
		t.Fatalf("expired sealing artifact remains readable: %v", openErr)
	}
	var itemAttemptsAfter []model.BackupAssetExportItemAttempt
	if err := fixture.harness.db.Where("job_id = ? AND attempt_id = ?", fixture.jobID, fixture.attemptID).
		Order("id ASC").Find(&itemAttemptsAfter).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(itemAttemptsAfter, itemAttemptsBefore) {
		t.Fatalf("expired sealing immutable history changed: before=%+v after=%+v", itemAttemptsBefore, itemAttemptsAfter)
	}

	retryNow := fixture.clock.Add(time.Duration(jobAfter.RetryBaseSeconds)*time.Second + time.Second)
	coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return retryNow })
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := coordinator.Claim(context.Background(), AttemptClaimRequest{
		JobID: fixture.jobID, WorkerOwner: "expired-sealing-fresh-rebuild",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.AttemptID == fixture.attemptID || fresh.AttemptNumber != attemptBefore.AttemptNumber+1 ||
		bytes.Equal(fresh.FenceToken, oldFence) || bytes.Equal(fresh.NoncePrefix, oldNonce) {
		t.Fatalf("fresh sealing retry=%+v old_attempt=%s old_nonce=%x", fresh, fixture.attemptID, oldNonce)
	}
	var rebuiltJob model.BackupAssetExportJob
	if err := fixture.harness.db.First(&rebuiltJob, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if rebuiltJob.ExecutionState != string(ExecutionRunning) || rebuiltJob.CurrentAttemptID == nil ||
		*rebuiltJob.CurrentAttemptID != fresh.AttemptID || rebuiltJob.PackedCount != 0 || rebuiltJob.SkippedCount != 0 ||
		rebuiltJob.FailedCount != 0 || rebuiltJob.LogicalBytes != 0 || rebuiltJob.ProviderBytes != 0 ||
		rebuiltJob.ArtifactBytes != 0 || rebuiltJob.ResultKind != "" {
		t.Fatalf("fresh byte-zero job=%+v", rebuiltJob)
	}
	var rebuiltItems []model.BackupAssetExportItem
	if err := fixture.harness.db.Where("job_id = ?", fixture.jobID).Order("ordinal ASC").Find(&rebuiltItems).Error; err != nil {
		t.Fatal(err)
	}
	for _, item := range rebuiltItems {
		if item.State != string(ItemPending) || item.CurrentAttemptID == nil || *item.CurrentAttemptID != fresh.AttemptID ||
			item.LogicalBytes != 0 || item.ProviderBytes != 0 || item.ErrorCategory != "" {
			t.Fatalf("fresh byte-zero item=%+v", item)
		}
	}
	var historyCount int64
	if err := fixture.harness.db.Model(&model.BackupAssetExportItemAttempt{}).
		Where("job_id = ?", fixture.jobID).Count(&historyCount).Error; err != nil {
		t.Fatal(err)
	}
	if historyCount != int64(len(itemAttemptsBefore)+len(rebuiltItems)) {
		t.Fatalf("fresh retry item-attempt history=%d, want old=%d plus new=%d", historyCount, len(itemAttemptsBefore), len(rebuiltItems))
	}
}

func TestAttemptCoordinatorMaintainsReadySourceLeaseWithoutResettingResult(t *testing.T) {
	for _, test := range []struct {
		name                   string
		expireLease            bool
		afterExecutionDeadline bool
		keepLeaseActive        bool
		wantTakeover           bool
	}{
		{name: "renew active owner"},
		{name: "take over expired owner", expireLease: true, wantTakeover: true},
		{name: "take over after execution deadline", afterExecutionDeadline: true, wantTakeover: true},
		{name: "renew active owner after execution deadline", afterExecutionDeadline: true, keepLeaseActive: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := createPersistentSealedFixture(t)
			if _, err := fixture.worker.PublishReady(context.Background(), PersistentPublishRequest{
				JobID: fixture.jobID, AttemptID: fixture.attemptID,
				FenceToken: exportAttemptFenceToken(t, fixture.harness.db, fixture.attemptID), ArtifactID: fixture.artifactID,
			}); err != nil {
				t.Fatal(err)
			}
			readyIntegrity := readyIntegrityForFixture(t, fixture)
			var sourceBefore model.BackupAssetExportSourceLease
			if err := fixture.harness.db.Where("job_id = ?", fixture.jobID).Take(&sourceBefore).Error; err != nil {
				t.Fatal(err)
			}
			if test.expireLease {
				if err := fixture.harness.db.Model(&model.RecoveryPointLease{}).Where("id = ?", sourceBefore.LeaseID).
					Update("lease_expires_at", fixture.clock.Add(-time.Second)).Error; err != nil {
					t.Fatal(err)
				}
			}
			var jobBefore model.BackupAssetExportJob
			var itemBefore model.BackupAssetExportItem
			if err := fixture.harness.db.First(&jobBefore, "id = ?", fixture.jobID).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.harness.db.Where("job_id = ?", fixture.jobID).Take(&itemBefore).Error; err != nil {
				t.Fatal(err)
			}
			maintenanceNow := fixture.clock
			if test.afterExecutionDeadline {
				maintenanceNow = jobBefore.AbsoluteDeadline.UTC().Add(time.Second)
				if jobBefore.ExpiresAt == nil || !maintenanceNow.Before(jobBefore.ExpiresAt.UTC()) ||
					!maintenanceNow.Before(sourceBefore.AbsoluteDeadline.UTC()) {
					t.Fatalf("fixture does not isolate ready expiry: now=%s execution=%s ready=%v source=%s",
						maintenanceNow, jobBefore.AbsoluteDeadline, jobBefore.ExpiresAt, sourceBefore.AbsoluteDeadline)
				}
			}
			if test.keepLeaseActive {
				if err := fixture.harness.db.Model(&model.RecoveryPointLease{}).Where("id = ?", sourceBefore.LeaseID).
					Update("lease_expires_at", maintenanceNow.Add(time.Minute)).Error; err != nil {
					t.Fatal(err)
				}
			}
			sourceLeases, err := backupasset.NewLeaseService(fixture.harness.db, func() time.Time { return maintenanceNow }, backupasset.LeaseConfig{
				Duration: fixture.harness.config.LeaseTTL, Heartbeat: fixture.harness.config.LeaseRenewMargin,
				AbsoluteDeadline: 2 * time.Hour,
			})
			if err != nil {
				t.Fatal(err)
			}
			coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return maintenanceNow }, sourceLeases)
			if err != nil {
				t.Fatal(err)
			}
			maintained, err := coordinator.MaintainSourceLeases(context.Background(), SourceLeaseMaintenanceRequest{
				JobID: fixture.jobID, ReadyIntegrity: readyIntegrity,
			})
			if err != nil || maintained.TakenOver != test.wantTakeover || len(maintained.LeaseExpiresAt) != 1 ||
				!maintained.AbsoluteDeadline.Equal(sourceBefore.AbsoluteDeadline) {
				t.Fatalf("ready lease maintenance=%+v err=%v", maintained, err)
			}
			var sourceAfter model.BackupAssetExportSourceLease
			var jobAfter model.BackupAssetExportJob
			var itemAfter model.BackupAssetExportItem
			if err := fixture.harness.db.First(&sourceAfter, "id = ?", sourceBefore.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.harness.db.First(&jobAfter, "id = ?", fixture.jobID).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.harness.db.First(&itemAfter, "id = ?", itemBefore.ID).Error; err != nil {
				t.Fatal(err)
			}
			if test.wantTakeover == (sourceAfter.LeaseAttemptID == sourceBefore.LeaseAttemptID) ||
				!sourceAfter.AbsoluteDeadline.Equal(sourceBefore.AbsoluteDeadline) {
				t.Fatalf("source maintenance before=%+v after=%+v", sourceBefore, sourceAfter)
			}
			if jobAfter.ExecutionState != jobBefore.ExecutionState || jobAfter.CurrentAttemptID == nil || jobBefore.CurrentAttemptID == nil ||
				*jobAfter.CurrentAttemptID != *jobBefore.CurrentAttemptID || jobAfter.ResultKind != jobBefore.ResultKind ||
				itemAfter.State != itemBefore.State || itemAfter.LogicalBytes != itemBefore.LogicalBytes {
				t.Fatalf("ready maintenance reset result: job_before=%+v job_after=%+v item_before=%+v item_after=%+v",
					jobBefore, jobAfter, itemBefore, itemAfter)
			}
		})
	}
}

func TestAttemptCoordinatorSourceLeaseMaintenanceStopsAtExecutionAndReadyBoundaries(t *testing.T) {
	for _, test := range []struct {
		name                  string
		publishReady          bool
		missingReady          bool
		sourceAuthorityWindow time.Duration
		afterExpiry           time.Duration
		wantErr               error
	}{
		{name: "non-ready at execution deadline", wantErr: ErrExecutionDeadlineReached},
		{name: "ready at expiry", publishReady: true, sourceAuthorityWindow: 48 * time.Hour, wantErr: ErrReadyExpired},
		{name: "ready after expiry", publishReady: true, sourceAuthorityWindow: 48 * time.Hour, afterExpiry: time.Nanosecond, wantErr: ErrReadyExpired},
		{name: "ready missing expiry", publishReady: true, missingReady: true, wantErr: ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := createPersistentSealedFixtureWithSourceAuthority(t, test.sourceAuthorityWindow)
			var readyIntegrity *ReadyIntegrityToken
			if test.publishReady {
				if _, err := fixture.worker.PublishReady(context.Background(), PersistentPublishRequest{
					JobID: fixture.jobID, AttemptID: fixture.attemptID,
					FenceToken: exportAttemptFenceToken(t, fixture.harness.db, fixture.attemptID), ArtifactID: fixture.artifactID,
				}); err != nil {
					t.Fatal(err)
				}
				readyIntegrity = readyIntegrityForFixture(t, fixture)
			}

			var job model.BackupAssetExportJob
			if err := fixture.harness.db.First(&job, "id = ?", fixture.jobID).Error; err != nil {
				t.Fatal(err)
			}
			maintenanceNow := job.AbsoluteDeadline.UTC()
			if test.publishReady {
				if test.missingReady {
					if err := fixture.harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", job.ID).
						Updates(map[string]any{"ready_at": nil, "expires_at": nil}).Error; err != nil {
						t.Fatal(err)
					}
					maintenanceNow = fixture.clock
				} else {
					maintenanceNow = job.ExpiresAt.UTC().Add(test.afterExpiry)
				}
			}

			var sourceBefore model.BackupAssetExportSourceLease
			var leaseBefore model.RecoveryPointLease
			if err := fixture.harness.db.Where("job_id = ?", fixture.jobID).Take(&sourceBefore).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.harness.db.First(&leaseBefore, "id = ?", sourceBefore.LeaseID).Error; err != nil {
				t.Fatal(err)
			}
			sourceLeases, err := backupasset.NewLeaseService(fixture.harness.db, func() time.Time { return maintenanceNow }, backupasset.LeaseConfig{
				Duration: fixture.harness.config.LeaseTTL, Heartbeat: fixture.harness.config.LeaseRenewMargin,
				AbsoluteDeadline: 2 * time.Hour,
			})
			if err != nil {
				t.Fatal(err)
			}
			coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return maintenanceNow }, sourceLeases)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := coordinator.MaintainSourceLeases(context.Background(), SourceLeaseMaintenanceRequest{
				JobID: fixture.jobID, ReadyIntegrity: readyIntegrity,
			}); !errors.Is(err, test.wantErr) {
				t.Fatalf("source maintenance at closed job boundary error=%v", err)
			}
			var sourceAfter model.BackupAssetExportSourceLease
			var leaseAfter model.RecoveryPointLease
			if err := fixture.harness.db.First(&sourceAfter, "id = ?", sourceBefore.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.harness.db.First(&leaseAfter, "id = ?", leaseBefore.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(sourceAfter, sourceBefore) || !reflect.DeepEqual(leaseAfter, leaseBefore) {
				t.Fatalf("closed job boundary mutated source authority: source_before=%+v source_after=%+v lease_before=%+v lease_after=%+v",
					sourceBefore, sourceAfter, leaseBefore, leaseAfter)
			}
		})
	}
}

func TestAttemptCoordinatorReadySourceLeaseMaintenanceStopsAtSourceCaps(t *testing.T) {
	for _, test := range []struct {
		name      string
		capSource func(model.BackupAssetExportSourceLease, time.Time) (time.Time, *time.Time)
	}{
		{
			name: "absolute deadline",
			capSource: func(source model.BackupAssetExportSourceLease, _ time.Time) (time.Time, *time.Time) {
				return source.AbsoluteDeadline.UTC(), source.RetentionUntil
			},
		},
		{
			name: "retention until",
			capSource: func(source model.BackupAssetExportSourceLease, now time.Time) (time.Time, *time.Time) {
				retention := now.Add(30 * time.Minute)
				return retention, &retention
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := createPersistentSealedFixture(t)
			if _, err := fixture.worker.PublishReady(context.Background(), PersistentPublishRequest{
				JobID: fixture.jobID, AttemptID: fixture.attemptID,
				FenceToken: exportAttemptFenceToken(t, fixture.harness.db, fixture.attemptID), ArtifactID: fixture.artifactID,
			}); err != nil {
				t.Fatal(err)
			}
			var source model.BackupAssetExportSourceLease
			if err := fixture.harness.db.Where("job_id = ?", fixture.jobID).Take(&source).Error; err != nil {
				t.Fatal(err)
			}
			maintenanceNow, retentionUntil := test.capSource(source, fixture.clock)
			if retentionUntil != nil && (source.RetentionUntil == nil || !retentionUntil.Equal(source.RetentionUntil.UTC())) {
				if err := fixture.harness.db.Model(&model.BackupAssetExportSourceLease{}).Where("id = ?", source.ID).
					Update("retention_until", *retentionUntil).Error; err != nil {
					t.Fatal(err)
				}
			}
			var job model.BackupAssetExportJob
			if err := fixture.harness.db.First(&job, "id = ?", fixture.jobID).Error; err != nil {
				t.Fatal(err)
			}
			readyExpiry, err := ComputeReadyExpiry(
				*job.ReadyAt, time.Duration(job.ReadyTTLSeconds)*time.Second,
				[]SourceDeadline{{AbsoluteDeadline: source.AbsoluteDeadline, RetentionUntil: retentionUntil}},
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := fixture.harness.db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					Update("expires_at", readyExpiry).Error; err != nil {
					return err
				}
				return tx.Model(&model.BackupAssetExportArtifact{}).Where("id = ?", fixture.artifactID).
					Update("expires_at", readyExpiry).Error
			}); err != nil {
				t.Fatal(err)
			}
			readyIntegrity := readyIntegrityForFixture(t, fixture)
			var sourceBefore model.BackupAssetExportSourceLease
			var leaseBefore model.RecoveryPointLease
			if err := fixture.harness.db.First(&sourceBefore, "id = ?", source.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.harness.db.First(&leaseBefore, "id = ?", source.LeaseID).Error; err != nil {
				t.Fatal(err)
			}
			sourceLeases, err := backupasset.NewLeaseService(fixture.harness.db, func() time.Time { return maintenanceNow }, backupasset.LeaseConfig{
				Duration: fixture.harness.config.LeaseTTL, Heartbeat: fixture.harness.config.LeaseRenewMargin,
				AbsoluteDeadline: 2 * time.Hour,
			})
			if err != nil {
				t.Fatal(err)
			}
			coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return maintenanceNow }, sourceLeases)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := coordinator.MaintainSourceLeases(context.Background(), SourceLeaseMaintenanceRequest{
				JobID: fixture.jobID, ReadyIntegrity: readyIntegrity,
			}); !errors.Is(err, ErrSourceDeadlineReached) {
				t.Fatalf("ready source maintenance at %s cap error=%v", test.name, err)
			}
			var sourceAfter model.BackupAssetExportSourceLease
			var leaseAfter model.RecoveryPointLease
			if err := fixture.harness.db.First(&sourceAfter, "id = ?", sourceBefore.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.harness.db.First(&leaseAfter, "id = ?", leaseBefore.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(sourceAfter, sourceBefore) || !reflect.DeepEqual(leaseAfter, leaseBefore) {
				t.Fatalf("ready source %s cap mutated authority: source_before=%+v source_after=%+v lease_before=%+v lease_after=%+v",
					test.name, sourceBefore, sourceAfter, leaseBefore, leaseAfter)
			}
		})
	}
}

func TestAttemptCoordinatorReadySourceMaintenanceRecomputesFrozenExpiryBeforeMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gorm.DB, persistentSealedFixture, time.Time) error
	}{
		{
			name: "job expiry drift",
			mutate: func(db *gorm.DB, fixture persistentSealedFixture, drifted time.Time) error {
				return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
					Update("expires_at", drifted).Error
			},
		},
		{
			name: "artifact expiry drift",
			mutate: func(db *gorm.DB, fixture persistentSealedFixture, drifted time.Time) error {
				return db.Model(&model.BackupAssetExportArtifact{}).Where("id = ?", fixture.artifactID).
					Update("expires_at", drifted).Error
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := createPersistentSealedFixture(t)
			if _, err := fixture.worker.PublishReady(context.Background(), PersistentPublishRequest{
				JobID: fixture.jobID, AttemptID: fixture.attemptID,
				FenceToken: exportAttemptFenceToken(t, fixture.harness.db, fixture.attemptID), ArtifactID: fixture.artifactID,
			}); err != nil {
				t.Fatal(err)
			}
			var job model.BackupAssetExportJob
			if err := fixture.harness.db.First(&job, "id = ?", fixture.jobID).Error; err != nil {
				t.Fatal(err)
			}
			if job.ExpiresAt == nil || !job.ExpiresAt.After(fixture.clock.Add(time.Minute)) {
				t.Fatalf("ready fixture expiry=%v", job.ExpiresAt)
			}
			readyIntegrity := readyIntegrityForFixture(t, fixture)
			drifted := job.ExpiresAt.Add(-time.Minute)
			if err := test.mutate(fixture.harness.db, fixture, drifted); err != nil {
				t.Fatal(err)
			}

			var sourceBefore model.BackupAssetExportSourceLease
			var leaseBefore model.RecoveryPointLease
			if err := fixture.harness.db.Where("job_id = ?", fixture.jobID).Take(&sourceBefore).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.harness.db.First(&leaseBefore, "id = ?", sourceBefore.LeaseID).Error; err != nil {
				t.Fatal(err)
			}
			coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return fixture.clock }, fixture.harness.lease)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := coordinator.MaintainSourceLeases(
				context.Background(), SourceLeaseMaintenanceRequest{JobID: fixture.jobID, ReadyIntegrity: readyIntegrity},
			); !errors.Is(err, ErrAttemptFenceLost) {
				t.Fatalf("ready %s maintenance error=%v, want ErrAttemptFenceLost", test.name, err)
			}
			var sourceAfter model.BackupAssetExportSourceLease
			var leaseAfter model.RecoveryPointLease
			if err := fixture.harness.db.First(&sourceAfter, "id = ?", sourceBefore.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.harness.db.First(&leaseAfter, "id = ?", leaseBefore.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(sourceAfter, sourceBefore) || !reflect.DeepEqual(leaseAfter, leaseBefore) {
				t.Fatalf("ready %s drift mutated source authority: source_before=%+v source_after=%+v lease_before=%+v lease_after=%+v",
					test.name, sourceBefore, sourceAfter, leaseBefore, leaseAfter)
			}
		})
	}
}

func TestAttemptCoordinatorRejectsReadyArtifactDriftBeforeSourceMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gorm.DB, persistentSealedFixture) error
	}{
		{
			name: "locator",
			mutate: func(db *gorm.DB, fixture persistentSealedFixture) error {
				return db.Model(&model.BackupAssetExportArtifact{}).Where("id = ?", fixture.artifactID).
					Update("locator", strings.Repeat("0", 32)+".xre").Error
			},
		},
		{
			name: "ciphertext digest",
			mutate: func(db *gorm.DB, fixture persistentSealedFixture) error {
				return db.Model(&model.BackupAssetExportArtifact{}).Where("id = ?", fixture.artifactID).
					Update("ciphertext_digest", strings.Repeat("0", 64)).Error
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := createPersistentReadyFixture(t)
			readyIntegrity := readyIntegrityForFixture(t, fixture)
			before := loadSourceAuthoritySnapshot(t, fixture.harness.db, fixture.jobID)
			if err := test.mutate(fixture.harness.db, fixture); err != nil {
				t.Fatal(err)
			}
			spy := &sourceLeaseCoordinatorSpy{inner: fixture.harness.lease}
			coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return fixture.clock }, spy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := coordinator.MaintainSourceLeases(context.Background(), SourceLeaseMaintenanceRequest{
				JobID: fixture.jobID, ReadyIntegrity: readyIntegrity,
			}); !errors.Is(err, ErrAttemptFenceLost) {
				t.Fatalf("ready artifact %s drift maintenance error=%v, want ErrAttemptFenceLost", test.name, err)
			}
			assertSourceMaintenanceNotCalled(t, spy)
			assertSourceAuthorityUnchanged(t, fixture.harness.db, fixture.jobID, before)
		})
	}
}

func TestAttemptCoordinatorRejectsReadyJobKeyRewrapBeforeSourceMutation(t *testing.T) {
	fixture := createPersistentReadyFixture(t)
	readyIntegrity := readyIntegrityForFixture(t, fixture)
	before := loadSourceAuthoritySnapshot(t, fixture.harness.db, fixture.jobID)
	var key model.BackupAssetExportKey
	if err := fixture.harness.db.Where("job_id = ?", fixture.jobID).Take(&key).Error; err != nil {
		t.Fatal(err)
	}
	oldMaterial, err := fixture.ring.ByVersion(context.Background(), backupasset.KeyDomainExportStore, key.KEKVersion)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = rewrapPersistedJobKeyForTest(
		t, fixture.harness.db, fixture.ring, fixture.jobID, oldMaterial, fixture.clock.Add(time.Second), nil,
	)
	spy := &sourceLeaseCoordinatorSpy{inner: fixture.harness.lease}
	coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return fixture.clock }, spy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.MaintainSourceLeases(context.Background(), SourceLeaseMaintenanceRequest{
		JobID: fixture.jobID, ReadyIntegrity: readyIntegrity,
	}); !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("ready job-key rewrap maintenance error=%v, want ErrAttemptFenceLost", err)
	}
	assertSourceMaintenanceNotCalled(t, spy)
	assertSourceAuthorityUnchanged(t, fixture.harness.db, fixture.jobID, before)
}

func TestAttemptCoordinatorRejectsSealingRequestConsumedAfterReadyPublication(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	request := SourceLeaseMaintenanceRequest{JobID: fixture.jobID}
	if _, err := fixture.worker.PublishReady(context.Background(), PersistentPublishRequest{
		JobID: fixture.jobID, AttemptID: fixture.attemptID,
		FenceToken: fixture.fenceToken, ArtifactID: fixture.artifactID,
	}); err != nil {
		t.Fatal(err)
	}
	before := loadSourceAuthoritySnapshot(t, fixture.harness.db, fixture.jobID)
	spy := &sourceLeaseCoordinatorSpy{inner: fixture.harness.lease}
	coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return fixture.clock }, spy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.MaintainSourceLeases(context.Background(), request); !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("stale sealing maintenance after ready publication error=%v, want ErrAttemptFenceLost", err)
	}
	assertSourceMaintenanceNotCalled(t, spy)
	assertSourceAuthorityUnchanged(t, fixture.harness.db, fixture.jobID, before)
}

func TestAttemptCoordinatorRejectsDirectReadySourceTakeoverWithoutIntegrityProof(t *testing.T) {
	fixture := createPersistentReadyFixture(t)
	var source model.BackupAssetExportSourceLease
	if err := fixture.harness.db.Where("job_id = ?", fixture.jobID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.harness.db.Model(&model.RecoveryPointLease{}).Where("id = ?", source.LeaseID).
		Update("lease_expires_at", fixture.clock.Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	before := loadSourceAuthoritySnapshot(t, fixture.harness.db, fixture.jobID)
	spy := &sourceLeaseCoordinatorSpy{inner: fixture.harness.lease}
	coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return fixture.clock }, spy)
	if err != nil {
		t.Fatal(err)
	}

	_, err = coordinator.TakeoverSourceLeases(context.Background(), SourceLeaseTakeoverRequest{JobID: fixture.jobID})
	if !errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("direct ready source takeover error=%v, want ErrAttemptFenceLost", err)
	}
	assertSourceMaintenanceNotCalled(t, spy)
	assertSourceAuthorityUnchanged(t, fixture.harness.db, fixture.jobID, before)
}

func TestAttemptCoordinatorSourceMaintenancePreservesInfrastructureAndFoundationErrors(t *testing.T) {
	t.Run("context cancellation", func(t *testing.T) {
		fixture := createPersistentSealedFixture(t)
		coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return fixture.clock }, fixture.harness.lease)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := coordinator.MaintainSourceLeases(ctx, SourceLeaseMaintenanceRequest{JobID: fixture.jobID}); !errors.Is(err, context.Canceled) || errors.Is(err, ErrAttemptFenceLost) {
			t.Fatalf("canceled source maintenance error=%v", err)
		}
	})

	t.Run("database load", func(t *testing.T) {
		fixture := createPersistentSealedFixture(t)
		injected := errors.New("injected source maintenance database failure")
		marker := t.Name()
		type sourceMaintenanceDatabaseContextKey struct{}
		callbackName := "test:source-maintenance-database-failure:" + fixture.jobID
		if err := fixture.harness.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement == nil || tx.Statement.Schema == nil ||
				tx.Statement.Schema.Table != "backup_asset_export_jobs" ||
				tx.Statement.Context.Value(sourceMaintenanceDatabaseContextKey{}) != marker {
				return
			}
			_ = tx.AddError(injected)
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = fixture.harness.db.Callback().Query().Remove(callbackName) })
		coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return fixture.clock }, fixture.harness.lease)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.WithValue(context.Background(), sourceMaintenanceDatabaseContextKey{}, marker)
		if _, err := coordinator.MaintainSourceLeases(ctx, SourceLeaseMaintenanceRequest{JobID: fixture.jobID}); !errors.Is(err, injected) || errors.Is(err, ErrAttemptFenceLost) {
			t.Fatalf("database source maintenance error=%v", err)
		}
	})

	for _, test := range []struct {
		name               string
		foundationErr      error
		wantSourceDeadline bool
	}{
		{name: "Foundation fence", foundationErr: backupasset.ErrLeaseFenceLost},
		{name: "Foundation arbitrary error", foundationErr: errors.New("injected Foundation source maintenance failure")},
		{name: "Foundation absolute deadline", foundationErr: backupasset.ErrLeaseDeadlineExceeded, wantSourceDeadline: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := createPersistentSealedFixture(t)
			coordinator, err := NewAttemptCoordinator(
				fixture.harness.db, func() time.Time { return fixture.clock },
				sourceLeaseCoordinatorErrorFake{renewErr: test.foundationErr},
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = coordinator.MaintainSourceLeases(context.Background(), SourceLeaseMaintenanceRequest{JobID: fixture.jobID})
			if !errors.Is(err, test.foundationErr) || errors.Is(err, ErrAttemptFenceLost) ||
				errors.Is(err, ErrSourceDeadlineReached) != test.wantSourceDeadline {
				t.Fatalf("Foundation source maintenance error=%v", err)
			}
		})
	}

	t.Run("stale persisted fence", func(t *testing.T) {
		fixture := createPersistentSealedFixture(t)
		if err := fixture.harness.db.Model(&model.BackupAssetExportSourceLease{}).Where("job_id = ?", fixture.jobID).
			Update("fence_hash", strings.Repeat("0", 64)).Error; err != nil {
			t.Fatal(err)
		}
		coordinator, err := NewAttemptCoordinator(fixture.harness.db, func() time.Time { return fixture.clock }, fixture.harness.lease)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := coordinator.MaintainSourceLeases(context.Background(), SourceLeaseMaintenanceRequest{JobID: fixture.jobID}); !errors.Is(err, ErrAttemptFenceLost) || errors.Is(err, ErrSourceDeadlineReached) {
			t.Fatalf("stale source fence error=%v", err)
		}
	})
}

type sourceLeaseCoordinatorErrorFake struct {
	renewErr    error
	takeoverErr error
}

type sourceLeaseCoordinatorSpy struct {
	inner         SourceLeaseCoordinator
	renewCalls    int
	takeoverCalls int
}

type sourceLeaseCoordinatorBarrier struct {
	inner   SourceLeaseCoordinator
	entered chan<- struct{}
	proceed <-chan struct{}
	once    sync.Once
}

type readyVerifierPanicContext struct {
	context.Context
}

func (readyVerifierPanicContext) Err() error {
	panic("injected ready verifier context panic")
}

func (barrier *sourceLeaseCoordinatorBarrier) wait() {
	barrier.once.Do(func() { barrier.entered <- struct{}{} })
	<-barrier.proceed
}

func (barrier *sourceLeaseCoordinatorBarrier) RenewTx(
	ctx context.Context, tx *gorm.DB, fence backupasset.LeaseFence,
) (backupasset.Lease, error) {
	barrier.wait()
	return barrier.inner.RenewTx(ctx, tx, fence)
}

func (barrier *sourceLeaseCoordinatorBarrier) TakeoverTx(
	ctx context.Context, tx *gorm.DB, request backupasset.TakeoverLeaseRequest,
) (backupasset.Lease, error) {
	barrier.wait()
	return barrier.inner.TakeoverTx(ctx, tx, request)
}

func (spy *sourceLeaseCoordinatorSpy) RenewTx(
	ctx context.Context, tx *gorm.DB, fence backupasset.LeaseFence,
) (backupasset.Lease, error) {
	spy.renewCalls++
	return spy.inner.RenewTx(ctx, tx, fence)
}

func (spy *sourceLeaseCoordinatorSpy) TakeoverTx(
	ctx context.Context, tx *gorm.DB, request backupasset.TakeoverLeaseRequest,
) (backupasset.Lease, error) {
	spy.takeoverCalls++
	return spy.inner.TakeoverTx(ctx, tx, request)
}

type sourceAuthoritySnapshot struct {
	source     model.BackupAssetExportSourceLease
	foundation model.RecoveryPointLease
}

func loadSourceAuthoritySnapshot(t *testing.T, db *gorm.DB, jobID string) sourceAuthoritySnapshot {
	t.Helper()
	var snapshot sourceAuthoritySnapshot
	if err := db.Where("job_id = ?", jobID).Take(&snapshot.source).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&snapshot.foundation, "id = ?", snapshot.source.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertSourceMaintenanceNotCalled(t *testing.T, spy *sourceLeaseCoordinatorSpy) {
	t.Helper()
	if spy.renewCalls != 0 || spy.takeoverCalls != 0 {
		t.Fatalf("source maintenance reached Foundation coordinator: renew=%d takeover=%d", spy.renewCalls, spy.takeoverCalls)
	}
}

func assertSourceAuthorityUnchanged(
	t *testing.T, db *gorm.DB, jobID string, before sourceAuthoritySnapshot,
) {
	t.Helper()
	after := loadSourceAuthoritySnapshot(t, db, jobID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected source maintenance mutated authority: before=%+v after=%+v", before, after)
	}
}

func (fake sourceLeaseCoordinatorErrorFake) RenewTx(
	context.Context, *gorm.DB, backupasset.LeaseFence,
) (backupasset.Lease, error) {
	return backupasset.Lease{}, fake.renewErr
}

func (fake sourceLeaseCoordinatorErrorFake) TakeoverTx(
	context.Context, *gorm.DB, backupasset.TakeoverLeaseRequest,
) (backupasset.Lease, error) {
	return backupasset.Lease{}, fake.takeoverErr
}

func exportAttemptFenceToken(t *testing.T, db *gorm.DB, attemptID string) []byte {
	t.Helper()
	var attempt model.BackupAssetExportAttempt
	if err := db.First(&attempt, "id = ?", attemptID).Error; err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), attempt.FenceToken...)
}

func readyIntegrityForFixture(t *testing.T, fixture persistentSealedFixture) *ReadyIntegrityToken {
	t.Helper()
	result, err := fixture.worker.ReconcileJob(context.Background(), PersistentReconcileRequest{JobID: fixture.jobID})
	if err != nil || result.ReadyIntegrity == nil {
		t.Fatalf("ready integrity verification result=%+v err=%v", result, err)
	}
	return result.ReadyIntegrity
}

type persistentReadSpoolFixture struct {
	harness    serviceHarness
	clock      time.Time
	worker     *PersistentWorker
	jobID      string
	attemptID  string
	fenceToken []byte
	snapshot   PersistentAttemptSnapshot
	file       PersistentAttemptItem
}

type sealedPersistenceState struct {
	job           model.BackupAssetExportJob
	attempt       model.BackupAssetExportAttempt
	items         []model.BackupAssetExportItem
	itemAttempts  []model.BackupAssetExportItemAttempt
	sourceLeases  []model.BackupAssetExportSourceLease
	artifacts     []model.BackupAssetExportArtifact
	artifactCount int64
}

func createPersistentReadSpoolFixture(t *testing.T, extraItems ...FrozenItem) persistentReadSpoolFixture {
	t.Helper()
	harness := newWorkerServiceHarness(t)
	file := frozenItemFixture()
	selectionItems := append([]FrozenItem{file}, extraItems...)
	return createPersistentReadSpoolFixtureWithSelection(t, harness, selectionItems)
}

func createPersistentReadSpoolFixtureWithSelection(
	t *testing.T, harness serviceHarness, selectionItems []FrozenItem,
) persistentReadSpoolFixture {
	t.Helper()
	if len(selectionItems) == 0 || selectionItems[0].EntryType != backupasset.CatalogEntryFile {
		t.Fatal("persistent read-spool fixture requires a leading regular file")
	}
	file := selectionItems[0]
	ensurePersistentFixtureRecoveryPoints(t, harness, selectionItems)
	selection, err := FreezeSelection(selectionItems, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 98, Role: "admin"}, Selection: selection,
		IdempotencyKey: persistentFixtureIdempotencyKey(t.Name()),
		ArchiveFormat:  ArchiveZIP, ArchiveProfile: ArchiveProfileZIPDeflateV1,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Second)
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{
		JobID: created.JobID, WorkerOwner: "persistent-seal-evidence-fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	var fileRow model.BackupAssetExportItem
	if err := harness.db.Where("job_id = ? AND entry_type = ?", created.JobID, backupasset.CatalogEntryFile).Take(&fileRow).Error; err != nil {
		t.Fatal(err)
	}
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	broker, err := content.NewAttemptBroker(&persistentSourceResolverFake{
		payload: bytes.Repeat([]byte("s"), int(file.LogicalSize)), providerBytes: file.LogicalSize,
	}, budget, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: harness.db, Keys: backupasset.NewKeyring(harness.db, func() time.Time { return clock }),
		Broker: broker, Metadata: &metadataValidatorFake{}, Store: store, AttemptWork: NewAttemptWorkRegistry(),
		Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.SpoolItem(context.Background(), PersistentSpoolItemRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: fileRow.ID,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := worker.loader.Load(context.Background(), PersistentAttemptLoadRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	fileSnapshot, found := persistentItemByID(snapshot.Items, fileRow.ID)
	if !found || fileSnapshot.State != ItemRead || fileSnapshot.SpoolLocator == "" {
		t.Fatalf("read spool snapshot=%+v", snapshot)
	}
	return persistentReadSpoolFixture{
		harness: harness, clock: clock, worker: worker, jobID: created.JobID, attemptID: claim.AttemptID,
		fenceToken: append([]byte(nil), claim.FenceToken...), snapshot: snapshot, file: fileSnapshot,
	}
}

func createPersistentFailedFileSealFixture(t *testing.T) persistentReadSpoolFixture {
	t.Helper()
	directory := persistentNonFileFixture(
		backupasset.CatalogEntryDirectory, strings.Repeat("d", 64), "failed-file-terminal-cas-directory",
	)
	fixture := createPersistentReadSpoolFixture(t, directory)
	var file model.BackupAssetExportItem
	if err := fixture.harness.db.Where("id = ?", fixture.file.ItemID).Take(&file).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.harness.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.BackupAssetExportItemAttempt{}).
			Where("id = ? AND state = ?", fixture.file.ItemAttemptID, ItemRead).
			Updates(map[string]any{
				"state": string(ItemFailed), "spool_digest": "", "spool_size": int64(0), "spool_locator": "",
				"logical_bytes": int64(0), "provider_bytes": fixture.file.ProviderBytes,
				"error_category": "source_changed", "read_at": nil, "packed_at": nil, "finished_at": fixture.clock,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return fmt.Errorf("persist failed file item attempt fixture: %w", result.Error)
		}
		result = tx.Model(&model.BackupAssetExportItem{}).
			Where("id = ? AND current_attempt_id = ? AND state = ?", file.ID, fixture.attemptID, ItemRead).
			Updates(map[string]any{
				"state": string(ItemFailed), "logical_bytes": int64(0), "provider_bytes": fixture.file.ProviderBytes,
				"error_category": "source_changed", "updated_at": fixture.clock,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return fmt.Errorf("persist failed file projection fixture: %w", result.Error)
		}
		result = tx.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", fixture.attemptID).
			Updates(map[string]any{
				"checkpoint_ordinal": file.Ordinal, "checkpoint_item_count": int64(1),
				"checkpoint_logical_bytes": int64(0), "checkpoint_provider_bytes": fixture.file.ProviderBytes,
				"updated_at": fixture.clock,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return fmt.Errorf("persist failed file attempt checkpoint fixture: %w", result.Error)
		}
		result = tx.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
			Updates(map[string]any{
				"packed_count": int64(0), "skipped_count": int64(0), "failed_count": int64(1),
				"logical_bytes": int64(0), "provider_bytes": fixture.file.ProviderBytes,
				"transition_revision": gorm.Expr("transition_revision + 1"), "updated_at": fixture.clock,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return fmt.Errorf("persist failed file job aggregate fixture: %w", result.Error)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fixture.snapshot.ClearKeyMaterial()
	snapshot, err := fixture.worker.loader.Load(context.Background(), PersistentAttemptLoadRequest{
		JobID: fixture.jobID, AttemptID: fixture.attemptID, FenceToken: fixture.fenceToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	fileSnapshot, found := persistentItemByID(snapshot.Items, file.ID)
	if !found || fileSnapshot.State != ItemFailed || fileSnapshot.ErrorCategory != "source_changed" {
		t.Fatalf("failed file snapshot=%+v", snapshot)
	}
	fixture.snapshot = snapshot
	fixture.file = fileSnapshot
	return fixture
}

func ensurePersistentFixtureRecoveryPoints(t *testing.T, harness serviceHarness, items []FrozenItem) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if _, duplicate := seen[item.Ref.RecoveryPointID]; duplicate {
			continue
		}
		seen[item.Ref.RecoveryPointID] = struct{}{}
		var count int64
		if err := harness.db.Model(&model.RecoveryPoint{}).Where("id = ?", item.Ref.RecoveryPointID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			continue
		}
		if err := harness.db.Create(&model.RecoveryPoint{
			ID: item.Ref.RecoveryPointID, RepositoryID: strings.Repeat("9", 32),
			State: string(backupasset.RecoveryPointCommitted), Semantics: string(backupasset.PointNativeSnapshot),
			SourceFingerprint: item.SourceFingerprint, CapabilityRevision: int(item.ProviderCapabilityRevision),
			PhysicalAvailability: string(backupasset.PhysicalOnline), ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
			HoldState: string(backupasset.HoldNone), RetentionUntil: item.RetentionUntil,
			CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func persistentSealRequest(fixture persistentReadSpoolFixture) PersistentSealRequest {
	return PersistentSealRequest{
		JobID: fixture.jobID, AttemptID: fixture.attemptID, FenceToken: append([]byte(nil), fixture.fenceToken...),
	}
}

func persistentSealCipherResult(snapshot PersistentAttemptSnapshot) CipherResult {
	return CipherResult{CiphertextBytes: 1, NoncePrefix: append([]byte(nil), snapshot.AttemptNoncePrefix...)}
}

func persistentPackedFileReport(snapshot PersistentAttemptSnapshot, item PersistentAttemptItem) ArchiveReport {
	return ArchiveReport{
		SchemaVersion: 1, SelectionDigest: snapshot.SelectionDigest,
		ResultKind: ResultComplete, Packed: 1, LogicalBytes: item.LogicalBytes, ProviderBytes: item.ProviderBytes,
		Items: []ArchiveItemReport{{
			ItemID: item.ItemID, State: ItemPacked, LogicalBytes: item.LogicalBytes, ProviderBytes: item.ProviderBytes,
		}},
	}
}

func persistentFixtureIdempotencyKey(name string) string {
	digest := sha256.Sum256([]byte(name))
	return "persistent-seal-" + hex.EncodeToString(digest[:16])
}

func persistentArchiveReport(snapshot PersistentAttemptSnapshot, items ...ArchiveItemReport) ArchiveReport {
	report := ArchiveReport{
		SchemaVersion: 1, SelectionDigest: snapshot.SelectionDigest,
		Items: append([]ArchiveItemReport(nil), items...),
	}
	for _, item := range report.Items {
		switch item.State {
		case ItemPacked:
			report.Packed++
		case ItemSkipped:
			report.Skipped++
		case ItemFailed:
			report.Failed++
		default:
			panic("invalid test archive report item state")
		}
		report.LogicalBytes += item.LogicalBytes
		report.ProviderBytes += item.ProviderBytes
	}
	if report.Skipped != 0 || report.Failed != 0 {
		report.ResultKind = ResultPartial
	} else {
		report.ResultKind = ResultComplete
	}
	return report
}

func persistentCanonicalSealReport(t *testing.T, snapshot PersistentAttemptSnapshot) ArchiveReport {
	t.Helper()
	items := make([]ArchiveItemReport, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		report := ArchiveItemReport{ItemID: item.ItemID}
		switch item.Frozen.EntryType {
		case backupasset.CatalogEntryFile:
			switch item.State {
			case ItemRead:
				report.State = ItemPacked
				report.LogicalBytes = item.LogicalBytes
				report.ProviderBytes = item.ProviderBytes
			case ItemFailed:
				report.State = ItemFailed
				report.ProviderBytes = item.ProviderBytes
				report.ErrorCategory = item.ErrorCategory
			default:
				t.Fatalf("unsupported regular-file seal fixture state: %+v", item)
			}
		case backupasset.CatalogEntryDirectory:
			if item.State != ItemPending {
				t.Fatalf("unsupported directory seal fixture state: %+v", item)
			}
			report.State = ItemPacked
		case backupasset.CatalogEntrySymlink, backupasset.CatalogEntryHardlink:
			if item.State != ItemPending {
				t.Fatalf("unsupported link seal fixture state: %+v", item)
			}
			report.State = ItemSkipped
			report.ErrorCategory = ItemErrorLinkMetadataUnavailable
		case backupasset.CatalogEntrySpecial, backupasset.CatalogEntryUnknown:
			if item.State != ItemPending {
				t.Fatalf("unsupported special-file seal fixture state: %+v", item)
			}
			report.State = ItemSkipped
			report.ErrorCategory = ItemErrorSpecialFileSkipped
		default:
			t.Fatalf("unsupported seal fixture entry type: %+v", item)
		}
		items = append(items, report)
	}
	return persistentArchiveReport(snapshot, items...)
}

func persistentNonFileFixture(
	entryType backupasset.CatalogEntryType, entryID, fingerprint string,
) FrozenItem {
	item := frozenItemFixture()
	item.Ref.EntryID = entryID
	item.EntryFingerprint = fingerprint
	item.EntryType = entryType
	item.LogicalSize = 0
	item.MediaType = ""
	item.ArchiveComponents = []string{"root", fingerprint}
	return item
}

func persistentSecondSourceDirectoryFixture() FrozenItem {
	item := persistentNonFileFixture(
		backupasset.CatalogEntryDirectory, strings.Repeat("e", 64), "second-source-directory",
	)
	item.Ref.RecoveryPointID = strings.Repeat("d", 32)
	item.CatalogGenerationID = strings.Repeat("f", 32)
	item.SourceFingerprint = "second-source-fingerprint-v1"
	item.EntryFingerprint = "second-source-directory-entry-fingerprint-v1"
	return item
}

func persistentSnapshotItemByType(
	t *testing.T, snapshot PersistentAttemptSnapshot, entryType backupasset.CatalogEntryType,
) PersistentAttemptItem {
	t.Helper()
	var found PersistentAttemptItem
	for _, item := range snapshot.Items {
		if item.Frozen.EntryType != entryType {
			continue
		}
		if found.ItemID != "" {
			t.Fatalf("multiple %s items in snapshot=%+v", entryType, snapshot)
		}
		found = item
	}
	if found.ItemID == "" {
		t.Fatalf("missing %s item in snapshot=%+v", entryType, snapshot)
	}
	return found
}

func persistentSourceLeaseForRecoveryPoint(
	t *testing.T, fixture persistentReadSpoolFixture, recoveryPointID string,
) model.BackupAssetExportSourceLease {
	t.Helper()
	var source model.BackupAssetExportSourceLease
	if err := fixture.harness.db.Where("job_id = ? AND recovery_point_id = ?", fixture.jobID, recoveryPointID).
		Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	return source
}

func createInactivePublishReadySource(
	t *testing.T, fixture persistentSealedFixture, retentionUntil time.Time,
) model.BackupAssetExportSourceLease {
	t.Helper()
	var source model.BackupAssetExportSourceLease
	if err := fixture.harness.db.Where("job_id = ? AND state = ?", fixture.jobID, "active").Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	var lease model.RecoveryPointLease
	if err := fixture.harness.db.First(&lease, "id = ?", source.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	recoveryPointID, err := backupasset.NewOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	leaseID, err := backupasset.NewOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	sourceID, err := backupasset.NewOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	item := frozenItemFixture()
	item.Ref.RecoveryPointID = recoveryPointID
	item.RetentionUntil = &retentionUntil
	ensurePersistentFixtureRecoveryPoints(t, fixture.harness, []FrozenItem{item})

	lease.ID = leaseID
	lease.RecoveryPointID = recoveryPointID
	if err := fixture.harness.db.Create(&lease).Error; err != nil {
		t.Fatal(err)
	}
	releasedAt := fixture.clock
	source.ID = sourceID
	source.RecoveryPointID = recoveryPointID
	source.LeaseID = leaseID
	source.RetentionUntil = &retentionUntil
	source.State = "released"
	source.ReleasedAt = &releasedAt
	if err := fixture.harness.db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	return source
}

type persistentPublishReadySourceFenceContextKey struct{}

type persistentPublishReadySourceFenceBarrier struct {
	artifactAuthenticated bool
	transactionPool       gorm.ConnPool
	fired                 bool
	err                   error
}

func persistentStatementHasUpdateLock(tx *gorm.DB) bool {
	if tx == nil || tx.Statement == nil {
		return false
	}
	_, locked := tx.Statement.Clauses["FOR"]
	return locked
}

func installPersistentPublishReadySourceFenceBarrier(
	t *testing.T,
	db *gorm.DB,
	marker, callbackName string,
	action func(*gorm.DB) error,
) *persistentPublishReadySourceFenceBarrier {
	t.Helper()
	barrier := &persistentPublishReadySourceFenceBarrier{}
	if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Context.Value(persistentPublishReadySourceFenceContextKey{}) != marker {
			return
		}
		switch tx.Statement.Schema.Table {
		case "backup_asset_export_artifacts":
			if !barrier.artifactAuthenticated && barrier.transactionPool == nil && !persistentStatementHasUpdateLock(tx) {
				// This is PublishReady's pre-transaction authenticated artifact read.
				barrier.artifactAuthenticated = true
			}
		case "backup_asset_export_jobs":
			if barrier.artifactAuthenticated && barrier.transactionPool == nil && persistentStatementHasUpdateLock(tx) {
				// The next job read starts PublishReady's mutation transaction.
				barrier.transactionPool = tx.Statement.ConnPool
			}
		case "recovery_point_leases":
			if barrier.transactionPool == nil || barrier.fired || !persistentStatementHasUpdateLock(tx) ||
				tx.Statement.ConnPool != barrier.transactionPool {
				return
			}
			// The source snapshot and its Foundation lease are locked; expiry has not been computed or persisted.
			barrier.fired = true
			barrier.err = action(tx)
			if barrier.err != nil {
				_ = tx.AddError(barrier.err)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove publish-ready source fence callback: %v", err)
		}
	})
	return barrier
}

func assertPersistentSealReportRejected(
	t *testing.T,
	fixture persistentReadSpoolFixture,
	report ArchiveReport,
	before sealedPersistenceState,
) {
	t.Helper()
	err := fixture.worker.persistSealedArchive(
		context.Background(), persistentSealRequest(fixture), fixture.snapshot,
		strings.Repeat("a", 32), "forged-sealed-artifact.xre",
		persistentSealCipherResult(fixture.snapshot), report,
	)
	if err == nil {
		t.Fatal("forged sealed report was accepted")
	}
	assertSealedPersistenceStateUnchanged(t, fixture, before)
}

func differentSpoolDigest(digest string) string {
	if len(digest) != 64 {
		return strings.Repeat("f", 64)
	}
	prefix := "0"
	if digest[0] == '0' {
		prefix = "1"
	}
	return prefix + digest[1:]
}

func loadSealedPersistenceState(t *testing.T, fixture persistentReadSpoolFixture) sealedPersistenceState {
	t.Helper()
	return loadSealedPersistenceStateFor(t, fixture.harness.db, fixture.jobID, fixture.attemptID)
}

func loadSealedPersistenceStateFor(
	t *testing.T,
	db *gorm.DB,
	jobID, attemptID string,
) sealedPersistenceState {
	t.Helper()
	state := sealedPersistenceState{}
	if err := db.First(&state.job, "id = ?", jobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&state.attempt, "id = ?", attemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ?", jobID).Order("ordinal ASC").Find(&state.items).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ? AND attempt_id = ?", jobID, attemptID).
		Order("item_id ASC").Find(&state.itemAttempts).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ?", jobID).
		Order("recovery_point_id ASC").Find(&state.sourceLeases).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ?", jobID).Order("id ASC").Find(&state.artifacts).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetExportArtifact{}).Where("job_id = ?", jobID).
		Count(&state.artifactCount).Error; err != nil {
		t.Fatal(err)
	}
	return state
}

func assertSealedPersistenceStateUnchanged(
	t *testing.T,
	fixture persistentReadSpoolFixture,
	before sealedPersistenceState,
) {
	t.Helper()
	if after := loadSealedPersistenceState(t, fixture); !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected sealed persistence mutated rows: before=%+v after=%+v", before, after)
	}
}

type persistentSealedFixture struct {
	harness    serviceHarness
	clock      time.Time
	worker     *PersistentWorker
	store      *Store
	ring       *backupasset.Keyring
	broker     *content.AttemptBroker
	metadata   *metadataValidatorFake
	jobID      string
	attemptID  string
	artifactID string
	locator    string
	fenceToken []byte
}

type persistentStoreQuotaPair struct {
	globalBucket      model.BackupAssetExportQuotaBucket
	userBucket        model.BackupAssetExportQuotaBucket
	globalReservation model.BackupAssetExportReservation
	userReservation   model.BackupAssetExportReservation
}

func loadPersistentStoreQuotaPair(
	t *testing.T,
	db *gorm.DB,
	jobID string,
	ownerUserID uint,
) persistentStoreQuotaPair {
	t.Helper()
	if ownerUserID == 0 {
		t.Fatal("persistent store quota pair requires an owner")
	}

	var reservations []model.BackupAssetExportReservation
	if err := db.Where("job_id = ? AND kind = ?", jobID, "store").Order("bucket_id ASC").Find(&reservations).Error; err != nil {
		t.Fatal(err)
	}
	if len(reservations) != 2 {
		t.Fatalf("store reservations=%d, want exact global and user pair", len(reservations))
	}

	wantUserSubject := fmt.Sprintf("%d", ownerUserID)
	pair := persistentStoreQuotaPair{}
	for _, reservation := range reservations {
		if reservation.JobID == nil || *reservation.JobID != jobID || reservation.AttemptID != nil || reservation.Kind != "store" {
			t.Fatalf("invalid store reservation in pair: %+v", reservation)
		}
		var bucket model.BackupAssetExportQuotaBucket
		if err := db.Where("id = ?", reservation.BucketID).Take(&bucket).Error; err != nil {
			t.Fatal(err)
		}
		switch {
		case bucket.Scope == "global" && bucket.Subject == "global":
			if pair.globalReservation.ID != "" {
				t.Fatalf("duplicate global store reservation: %+v", reservation)
			}
			pair.globalBucket = bucket
			pair.globalReservation = reservation
		case bucket.Scope == "user" && bucket.Subject == wantUserSubject:
			if pair.userReservation.ID != "" {
				t.Fatalf("duplicate user store reservation: %+v", reservation)
			}
			pair.userBucket = bucket
			pair.userReservation = reservation
		default:
			t.Fatalf("store reservation bucket=%s/%s, want global/global or user/%s", bucket.Scope, bucket.Subject, wantUserSubject)
		}
	}
	if pair.globalReservation.ID == "" || pair.userReservation.ID == "" {
		t.Fatalf("incomplete global/user store pair: %+v", pair)
	}
	return pair
}

func createPersistentSealedFixture(t *testing.T) persistentSealedFixture {
	return createPersistentSealedFixtureWithProfile(t, ArchiveZIP, "zip_deflate_v1")
}

func TestPersistentWorkerSealArchiveChargesExactPhysicalBytesWithoutSettlingPeakReservation(t *testing.T) {
	fixture := createPersistentReadSpoolFixture(t)
	defer fixture.snapshot.ClearKeyMaterial()

	var job model.BackupAssetExportJob
	if err := fixture.harness.db.Where("id = ?", fixture.jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	expectedPeak, err := persistentAttemptSnapshotPeakStoreBytes(fixture.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	before := loadPersistentStoreQuotaPair(t, fixture.harness.db, fixture.jobID, job.OwnerUserID)
	for _, reservation := range []model.BackupAssetExportReservation{before.globalReservation, before.userReservation} {
		if reservation.State != "active" || reservation.ReservedStoreBytes != expectedPeak {
			t.Fatalf("pre-seal store reservation=%+v, want active peak=%d", reservation, expectedPeak)
		}
	}

	sealed, err := fixture.worker.SealArchive(context.Background(), persistentSealRequest(fixture))
	if err != nil {
		t.Fatal(err)
	}

	var artifact model.BackupAssetExportArtifact
	if err := fixture.harness.db.Where("id = ? AND job_id = ?", sealed.ArtifactID, fixture.jobID).Take(&artifact).Error; err != nil {
		t.Fatal(err)
	}
	if artifact.CiphertextSize != sealed.CiphertextBytes {
		t.Fatalf("sealed artifact ciphertext size=%d, want result=%d", artifact.CiphertextSize, sealed.CiphertextBytes)
	}
	if artifact.CiphertextSize <= 0 || artifact.CiphertextSize == expectedPeak {
		t.Fatalf("sealed artifact ciphertext size=%d, want positive actual size distinct from peak=%d", artifact.CiphertextSize, expectedPeak)
	}

	after := loadPersistentStoreQuotaPair(t, fixture.harness.db, fixture.jobID, job.OwnerUserID)
	for _, pair := range []struct {
		name   string
		before model.BackupAssetExportReservation
		after  model.BackupAssetExportReservation
	}{
		{name: "global", before: before.globalReservation, after: after.globalReservation},
		{name: "user", before: before.userReservation, after: after.userReservation},
	} {
		if pair.after.State != "active" || pair.after.ReservedStoreBytes != pair.before.ReservedStoreBytes ||
			pair.after.ReservedCipherBytes != pair.before.ReservedCipherBytes {
			t.Fatalf("%s store reservation changed during seal: before=%+v after=%+v", pair.name, pair.before, pair.after)
		}
	}
	for _, pair := range []struct {
		name   string
		before model.BackupAssetExportQuotaBucket
		after  model.BackupAssetExportQuotaBucket
	}{
		{name: "global", before: before.globalBucket, after: after.globalBucket},
		{name: "user", before: before.userBucket, after: after.userBucket},
	} {
		if pair.after.ReservedStoreBytes != pair.before.ReservedStoreBytes ||
			pair.after.UsedStoreBytes != pair.before.UsedStoreBytes+artifact.CiphertextSize {
			t.Fatalf("%s bucket after seal=%+v, want reserved=%d used=%d", pair.name, pair.after,
				pair.before.ReservedStoreBytes, pair.before.UsedStoreBytes+artifact.CiphertextSize)
		}
	}
}

func TestPersistentWorkerSealArchiveDurablyClaimsNonceBeforeConcurrentEncryption(t *testing.T) {
	fixture := createPersistentReadSpoolFixture(t)
	defer fixture.snapshot.ClearKeyMaterial()

	store := fixture.worker.store
	originalOpen := store.openStoreEntryDescriptor
	firstArchiveOpen := make(chan struct{})
	releaseFirstArchiveOpen := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseFirstArchiveOpen) })
	var openMu sync.Mutex
	spoolOpenCount := 0
	store.openStoreEntryDescriptor = func(directory int, name string, how *unix.OpenHow) (int, error) {
		if name == fixture.file.SpoolLocator {
			openMu.Lock()
			spoolOpenCount++
			current := spoolOpenCount
			openMu.Unlock()
			if current == 2 {
				close(firstArchiveOpen)
				<-releaseFirstArchiveOpen
			}
		}
		return originalOpen(directory, name, how)
	}

	type sealOutcome struct {
		result PersistentSealResult
		err    error
	}
	firstDone := make(chan sealOutcome, 1)
	go func() {
		result, err := fixture.worker.SealArchive(context.Background(), persistentSealRequest(fixture))
		firstDone <- sealOutcome{result: result, err: err}
	}()
	select {
	case <-firstArchiveOpen:
	case <-time.After(5 * time.Second):
		t.Fatal("first seal did not reach the authenticated spool reopen boundary")
	}

	var claimed model.BackupAssetExportAttempt
	if err := fixture.harness.db.First(&claimed, "id = ?", fixture.attemptID).Error; err != nil {
		t.Fatal(err)
	}
	durableClaimObserved := claimed.StagingLocator != ""

	secondDone := make(chan sealOutcome, 1)
	go func() {
		result, err := fixture.worker.SealArchive(context.Background(), persistentSealRequest(fixture))
		secondDone <- sealOutcome{result: result, err: err}
	}()
	var second sealOutcome
	secondRejectedBeforeEncryption := false
	select {
	case second = <-secondDone:
		secondRejectedBeforeEncryption = true
	case <-time.After(2 * time.Second):
	}
	releaseOnce.Do(func() { close(releaseFirstArchiveOpen) })

	var first sealOutcome
	select {
	case first = <-firstDone:
	case <-time.After(10 * time.Second):
		t.Fatal("first concurrent seal did not finish")
	}
	if !secondRejectedBeforeEncryption {
		select {
		case second = <-secondDone:
		case <-time.After(10 * time.Second):
			t.Fatal("second concurrent seal did not finish")
		}
	}
	outcomes := []sealOutcome{first, second}
	if !durableClaimObserved {
		t.Error("attempt nonce was not durably claimed before final archive encryption")
	}
	if !secondRejectedBeforeEncryption {
		t.Error("competing seal reached the final archive encryption boundary before losing the nonce claim")
	}
	successes := 0
	fenceLosses := 0
	for _, outcome := range outcomes {
		if outcome.err == nil {
			successes++
			if outcome.result.ArtifactID == "" {
				t.Errorf("successful concurrent seal has no artifact: %+v", outcome.result)
			}
			continue
		}
		if errors.Is(outcome.err, ErrAttemptFenceLost) {
			fenceLosses++
		}
	}
	if successes != 1 || fenceLosses != 1 {
		t.Fatalf("concurrent seal outcomes=%+v, want one success and one pre-encryption fence loss", outcomes)
	}
	var artifacts int64
	if err := fixture.harness.db.Model(&model.BackupAssetExportArtifact{}).
		Where("job_id = ? AND attempt_id = ?", fixture.jobID, fixture.attemptID).Count(&artifacts).Error; err != nil {
		t.Fatal(err)
	}
	if artifacts != 1 {
		t.Fatalf("concurrent seal artifacts=%d, want one", artifacts)
	}
}

func createPersistentSealedFixtureWithSourceAuthority(t *testing.T, authorityWindow time.Duration) persistentSealedFixture {
	t.Helper()
	return createPersistentSealedFixtureWithProfileAndSourceAuthority(t, ArchiveZIP, "zip_deflate_v1", authorityWindow)
}

func createPersistentSealedFixtureWithProfile(
	t *testing.T,
	format ArchiveFormat,
	profile string,
) persistentSealedFixture {
	return createPersistentSealedFixtureWithProfileAndSourceAuthority(t, format, profile, 0)
}

func createPersistentSealedFixtureWithProfileAndSourceAuthority(
	t *testing.T,
	format ArchiveFormat,
	profile string,
	authorityWindow time.Duration,
) persistentSealedFixture {
	t.Helper()
	harness := newWorkerServiceHarness(t)
	item := frozenItemFixture()
	if authorityWindow > 0 {
		retentionUntil := time.Now().UTC().Add(authorityWindow).Truncate(time.Second)
		item.RetentionUntil = &retentionUntil
		sourceLeases, err := backupasset.NewLeaseService(harness.db, func() time.Time { return time.Now().UTC() }, backupasset.LeaseConfig{
			Duration: harness.config.LeaseTTL, Heartbeat: harness.config.LeaseRenewMargin, AbsoluteDeadline: authorityWindow,
		})
		if err != nil {
			t.Fatal(err)
		}
		harness.lease = sourceLeases
		harness.leaseSpy.LeaseService = sourceLeases
	}
	payload := bytes.Repeat([]byte("r"), int(item.LogicalSize))
	selection, err := FreezeSelection([]FrozenItem{item}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 100, Role: "admin"}, Selection: selection,
		IdempotencyKey: "persistent-restart-sealed", ArchiveFormat: format, ArchiveProfile: profile,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Second)
	coordinator, err := NewAttemptCoordinator(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-restart"})
	if err != nil {
		t.Fatal(err)
	}
	var itemRow model.BackupAssetExportItem
	if err := harness.db.Where("job_id = ?", created.JobID).Take(&itemRow).Error; err != nil {
		t.Fatal(err)
	}
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	source := &persistentSourceResolverFake{payload: payload, providerBytes: item.LogicalSize}
	broker, err := content.NewAttemptBroker(source, budget, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	metadata := &metadataValidatorFake{}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ring := backupasset.NewKeyring(harness.db, func() time.Time { return clock })
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: harness.db, Keys: ring, Broker: broker, Metadata: metadata, Store: store,
		AttemptWork: NewAttemptWorkRegistry(),
		Now:         func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.SpoolItem(context.Background(), PersistentSpoolItemRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: itemRow.ID,
	}); err != nil {
		t.Fatal(err)
	}
	sealed, err := worker.SealArchive(context.Background(), PersistentSealRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	return persistentSealedFixture{
		harness: harness, clock: clock, worker: worker, store: store, ring: ring, broker: broker,
		metadata: metadata, jobID: created.JobID, attemptID: claim.AttemptID,
		artifactID: sealed.ArtifactID, locator: sealed.Locator, fenceToken: append([]byte(nil), claim.FenceToken...),
	}
}

func createPersistentReadyFixture(t *testing.T) persistentSealedFixture {
	t.Helper()
	fixture := createPersistentSealedFixture(t)
	if _, err := fixture.worker.PublishReady(context.Background(), PersistentPublishRequest{
		JobID: fixture.jobID, AttemptID: fixture.attemptID, FenceToken: fixture.fenceToken, ArtifactID: fixture.artifactID,
	}); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestPersistentWorkerUsesPersistedTARGzipProfileForDeterministicArchiveBytes(t *testing.T) {
	fixture := createPersistentSealedFixtureWithProfile(t, ArchiveTAR, "tar_gzip_v1")
	snapshot, err := fixture.worker.loader.Load(context.Background(), PersistentAttemptLoadRequest{
		JobID: fixture.jobID, AttemptID: fixture.attemptID, FenceToken: fixture.fenceToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := fixture.store.OpenSealed(fixture.locator)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	_, decryptErr := DecryptStream(context.Background(), &archive, encrypted, snapshot.DEK, CipherBinding{
		ExportID: snapshot.JobID, SelectionDigest: snapshot.SelectionDigest,
		ArchiveProfile: snapshot.ArchiveProfile, FormatVersion: 1,
		AttemptFenceDigest: snapshot.AttemptFenceDigest, Purpose: CipherPurposeFinalArchive,
	})
	closeErr := encrypted.Close()
	if decryptErr != nil || closeErr != nil {
		t.Fatal(errors.Join(decryptErr, closeErr))
	}
	if !bytes.HasPrefix(archive.Bytes(), []byte{0x1f, 0x8b, 0x08}) {
		t.Fatalf("tar_gzip_v1 plaintext prefix=%x, want gzip magic", archive.Bytes()[:min(8, archive.Len())])
	}
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !gzipReader.ModTime.IsZero() || gzipReader.Name != "" || gzipReader.Comment != "" ||
		len(gzipReader.Extra) != 0 || gzipReader.OS != 255 {
		t.Fatalf("non-deterministic gzip header=%+v", gzipReader.Header)
	}
	tarBytes, readErr := io.ReadAll(gzipReader)
	gzipCloseErr := gzipReader.Close()
	if readErr != nil || gzipCloseErr != nil {
		t.Fatal(errors.Join(readErr, gzipCloseErr))
	}

	var canonical bytes.Buffer
	canonicalWriter, err := gzip.NewWriterLevel(&canonical, 6)
	if err != nil {
		t.Fatal(err)
	}
	canonicalWriter.ModTime = time.Time{}
	canonicalWriter.OS = 255
	if _, err := canonicalWriter.Write(tarBytes); err != nil {
		t.Fatal(err)
	}
	if err := canonicalWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(archive.Bytes(), canonical.Bytes()) {
		t.Fatal("tar_gzip_v1 output is not the deterministic Go gzip level-6 encoding")
	}

	tarReader := tar.NewReader(bytes.NewReader(tarBytes))
	names := make([]string, 0, 2)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
		if _, err := io.Copy(io.Discard, tarReader); err != nil {
			t.Fatal(err)
		}
	}
	if len(names) != 2 || names[1] != archiveReportName {
		t.Fatalf("tar.gz members=%v", names)
	}
}

func TestWorkerUsesAttemptBrokerOnlyForRegularFiles(t *testing.T) {
	item := frozenItemFixture()
	item.LogicalSize = 5
	broker := &workerBrokerFake{data: []byte("hello")}
	metadata := &metadataValidatorFake{}
	worker, err := NewWorker(broker, metadata)
	if err != nil {
		t.Fatal(err)
	}
	link := item
	link.Ref.EntryID = strings.Repeat("b", 64)
	link.EntryType = backupasset.CatalogEntrySymlink
	link.LogicalSize = 0
	link.ArchiveComponents = []string{"link"}
	var output bytes.Buffer
	report, err := worker.WriteArchive(context.Background(), &output, WorkerArchiveRequest{
		AttemptID: "33333333333333333333333333333333", SelectionDigest: strings.Repeat("a", 64),
		Deadline: time.Now().UTC().Add(time.Hour),
		Format:   ArchiveZIP, ArchiveProfile: ArchiveProfileZIPDeflateV1,
		Limits: ArchiveLimits{MaxItems: 10, MaxLogicalBytes: 100, MaxProviderBytes: 100},
		Items: []WorkerItem{
			{ItemID: "11111111111111111111111111111111", Frozen: item},
			{ItemID: "22222222222222222222222222222222", Frozen: link},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if broker.opens != 1 || broker.session.revalidations != 1 || broker.session.closes != 1 {
		t.Fatalf("broker opens=%d session=%+v", broker.opens, broker.session)
	}
	if len(metadata.items) != 1 || metadata.items[0].EntryType != backupasset.CatalogEntrySymlink {
		t.Fatalf("metadata validations=%+v", metadata.items)
	}
	if report.ResultKind != ResultPartial || report.Skipped != 1 {
		t.Fatalf("report=%+v", report)
	}
	if _, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len())); err != nil {
		t.Fatalf("worker output is not a valid zip: %v", err)
	}
}

func TestWorkerWritesZeroByteRegularFilesWithoutSequentialProviderRead(t *testing.T) {
	for _, test := range []struct {
		name    string
		format  ArchiveFormat
		profile string
	}{
		{name: "zip", format: ArchiveZIP, profile: ArchiveProfileZIPDeflateV1},
		{name: "tar", format: ArchiveTAR, profile: ArchiveProfileTARNoneV1},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := frozenItemFixture()
			item.LogicalSize = 0
			item.ArchiveComponents = []string{"empty.txt"}
			broker := &workerBrokerFake{}
			worker, err := NewWorker(broker, &metadataValidatorFake{})
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			report, err := worker.WriteArchive(context.Background(), &output, WorkerArchiveRequest{
				AttemptID: strings.Repeat("3", 32), SelectionDigest: strings.Repeat("a", 64),
				Deadline: time.Now().UTC().Add(time.Hour), Format: test.format, ArchiveProfile: test.profile,
				Limits: ArchiveLimits{MaxItems: 1, MaxLogicalBytes: 1, MaxProviderBytes: 1},
				Items:  []WorkerItem{{ItemID: strings.Repeat("1", 32), Frozen: item}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if broker.opens != 1 || broker.session == nil || broker.session.sequentialCalls != 0 ||
				broker.session.revalidations != 1 || broker.session.closes != 1 {
				t.Fatalf("zero-byte broker opens=%d session=%+v", broker.opens, broker.session)
			}
			if len(broker.bindings) != 1 || !slices.Equal(broker.bindings[0].AllowedModes, []content.SourceMode{content.SourceModeStat}) ||
				broker.bindings[0].Limits.MaxBytesPerRequest != 1 || broker.bindings[0].Limits.MaxCumulativeBytes != 1 {
				t.Fatalf("zero-byte direct binding=%+v", broker.bindings)
			}
			if report.Packed != 1 || report.LogicalBytes != 0 || report.ProviderBytes != 0 || len(report.Items) != 1 ||
				report.Items[0].LogicalBytes != 0 || report.Items[0].ProviderBytes != 0 {
				t.Fatalf("zero-byte direct report=%+v", report)
			}
			if test.format == ArchiveZIP {
				if _, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len())); err != nil {
					t.Fatalf("zero-byte ZIP invalid: %v", err)
				}
			} else if _, err := tar.NewReader(bytes.NewReader(output.Bytes())).Next(); err != nil {
				t.Fatalf("zero-byte TAR invalid: %v", err)
			}
		})
	}
}

func TestWorkerPreservesCompositeRootForArchivePathDisambiguation(t *testing.T) {
	first := frozenItemFixture()
	first.LogicalSize = 5
	first.ArchiveComponents = []string{"Readme.txt"}
	first.SelectionRootOrdinal = 0
	second := first
	second.Ref.RecoveryPointID = strings.Repeat("d", 32)
	second.Ref.EntryID = strings.Repeat("e", 64)
	second.CatalogGenerationID = strings.Repeat("f", 32)
	second.SourceFingerprint = "second-source-fingerprint-v1"
	second.EntryFingerprint = "second-entry-fingerprint-v1"
	second.ArchiveComponents = []string{"README.TXT"}
	second.SelectionRootOrdinal = 1
	worker, err := NewWorker(&workerBrokerFake{data: []byte("hello")}, &metadataValidatorFake{})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if _, err := worker.WriteArchive(context.Background(), &output, WorkerArchiveRequest{
		AttemptID: "33333333333333333333333333333333", SelectionDigest: strings.Repeat("a", 64), Deadline: time.Now().UTC().Add(time.Hour),
		Format: ArchiveZIP, ArchiveProfile: ArchiveProfileZIPDeflateV1,
		Limits: ArchiveLimits{MaxItems: 4, MaxLogicalBytes: 16, MaxProviderBytes: 16},
		Items: []WorkerItem{
			{ItemID: "11111111111111111111111111111111", Frozen: first},
			{ItemID: "22222222222222222222222222222222", Frozen: second},
		},
	}); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
	}
	if got, want := strings.Join(names, ","), "rp-11111111/root-0/Readme.txt,rp-dddddddd/root-1/README.TXT,xirang-export-report.v1.json"; got != want {
		t.Fatalf("cross-root collision names=%q want %q", got, want)
	}
}

func TestWorkerFailsAttemptWhenPostReadRevalidationDrifts(t *testing.T) {
	broker := &workerBrokerFake{data: []byte("hello"), revalidateErr: content.ErrAttemptSourceChanged}
	worker, err := NewWorker(broker, &metadataValidatorFake{})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	item := frozenItemFixture()
	item.LogicalSize = 5
	_, err = worker.WriteArchive(context.Background(), &output, WorkerArchiveRequest{
		AttemptID: "33333333333333333333333333333333", SelectionDigest: strings.Repeat("a", 64), Deadline: time.Now().UTC().Add(time.Hour),
		Format: ArchiveZIP, ArchiveProfile: ArchiveProfileZIPDeflateV1,
		Limits: ArchiveLimits{MaxItems: 2, MaxLogicalBytes: 100, MaxProviderBytes: 100},
		Items:  []WorkerItem{{ItemID: "11111111111111111111111111111111", Frozen: item}},
	})
	if !errors.Is(err, content.ErrAttemptSourceChanged) {
		t.Fatalf("post-read drift error=%v", err)
	}
}

func TestWorkerUsesInjectedClockToRejectExpiredDeadline(t *testing.T) {
	start := time.Now().UTC().Add(time.Hour)
	item := frozenItemFixture()
	item.LogicalSize = 5
	worker, err := NewWorker(
		&workerBrokerFake{data: []byte("hello")},
		&metadataValidatorFake{},
		func() time.Time { return start },
	)
	if err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	_, err = worker.WriteArchive(context.Background(), &output, WorkerArchiveRequest{
		AttemptID: "33333333333333333333333333333333", SelectionDigest: strings.Repeat("a", 64),
		Deadline:       start.Add(-time.Second),
		Format:         ArchiveZIP,
		ArchiveProfile: ArchiveProfileZIPDeflateV1,
		Limits:         ArchiveLimits{MaxItems: 1, MaxLogicalBytes: 16, MaxProviderBytes: 16},
		Items: []WorkerItem{{
			ItemID: "44444444444444444444444444444444",
			Frozen: item,
		}},
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("WriteArchive error = %v, want injected-clock deadline rejection", err)
	}
}

type workerBrokerFake struct {
	data          []byte
	opens         int
	revalidateErr error
	session       *workerInputSessionFake
	bindings      []content.AttemptSourceBinding
}

func (fake *workerBrokerFake) OpenSession(_ context.Context, binding content.AttemptSourceBinding) (content.AttemptInputSession, content.AttemptSourceInfo, error) {
	fake.opens++
	copyBinding := binding
	copyBinding.AllowedModes = append([]content.SourceMode(nil), binding.AllowedModes...)
	fake.bindings = append(fake.bindings, copyBinding)
	fake.session = &workerInputSessionFake{data: fake.data, revalidateErr: fake.revalidateErr}
	return fake.session, content.AttemptSourceInfo{
		Size: int64(len(fake.data)), MediaType: "text/plain", FingerprintStrong: true, Sequential: true,
	}, nil
}

type workerInputSessionFake struct {
	data            []byte
	revalidations   int
	closes          int
	sequentialCalls int
	revalidateErr   error
}

func (session *workerInputSessionFake) Info() content.AttemptSourceInfo {
	return content.AttemptSourceInfo{Size: int64(len(session.data)), MediaType: "text/plain", FingerprintStrong: true, Sequential: true}
}
func (session *workerInputSessionFake) OpenSequential(_ context.Context, maxBytes int64) (content.AttemptReadHandle, error) {
	session.sequentialCalls++
	if maxBytes != int64(len(session.data)) {
		return nil, errors.New("unexpected max bytes")
	}
	return &workerReadHandle{Reader: bytes.NewReader(session.data)}, nil
}
func (*workerInputSessionFake) OpenRange(context.Context, int64, int64) (content.AttemptReadHandle, error) {
	return nil, errors.New("range not supported")
}
func (session *workerInputSessionFake) Revalidate(context.Context) error {
	session.revalidations++
	return session.revalidateErr
}
func (session *workerInputSessionFake) Close() error {
	session.closes++
	return nil
}

type workerReadHandle struct{ *bytes.Reader }

func (*workerReadHandle) Close() error { return nil }

type metadataValidatorFake struct {
	items    []FrozenItem
	txItems  []FrozenItem
	before   func()
	txBefore func(*gorm.DB, FrozenItem) error
}

func (fake *metadataValidatorFake) RevalidateMetadata(_ context.Context, item FrozenItem) error {
	fake.items = append(fake.items, item)
	if fake.before != nil {
		fake.before()
	}
	return nil
}

func (fake *metadataValidatorFake) RevalidateMetadataTx(
	_ context.Context,
	tx *gorm.DB,
	item FrozenItem,
) error {
	fake.txItems = append(fake.txItems, item)
	if fake.txBefore != nil {
		return fake.txBefore(tx, item)
	}
	return nil
}

type attemptWorkBlockingMetadataValidator struct {
	started     chan struct{}
	releaseWait chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
}

func newAttemptWorkBlockingMetadataValidator() *attemptWorkBlockingMetadataValidator {
	return &attemptWorkBlockingMetadataValidator{
		started: make(chan struct{}), releaseWait: make(chan struct{}),
	}
}

func (validator *attemptWorkBlockingMetadataValidator) RevalidateMetadata(ctx context.Context, _ FrozenItem) error {
	validator.startOnce.Do(func() { close(validator.started) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-validator.releaseWait:
		return errors.New("metadata validation released without cancellation")
	}
}

func (validator *attemptWorkBlockingMetadataValidator) RevalidateMetadataTx(
	ctx context.Context,
	_ *gorm.DB,
	item FrozenItem,
) error {
	return validator.RevalidateMetadata(ctx, item)
}

func (validator *attemptWorkBlockingMetadataValidator) release() {
	validator.releaseOnce.Do(func() { close(validator.releaseWait) })
}

var _ io.ReadCloser = (*workerReadHandle)(nil)

type byVersionKeySpy struct {
	*backupasset.Keyring
	versions []int
}

type zeroTrackingReadyKeySource struct {
	inner    ExportKeyVersionSource
	returned [][]byte
}

func (source *zeroTrackingReadyKeySource) ByVersion(
	ctx context.Context, domain backupasset.KeyDomain, version int,
) (backupasset.DomainKeyMaterial, error) {
	material, err := source.inner.ByVersion(ctx, domain, version)
	if err != nil {
		return backupasset.DomainKeyMaterial{}, err
	}
	key := append([]byte(nil), material.Key...)
	clear(material.Key)
	material.Key = key
	source.returned = append(source.returned, material.Key)
	return material, nil
}

func allZeroBytes(value []byte) bool {
	for _, current := range value {
		if current != 0 {
			return false
		}
	}
	return true
}

type byVersionLookup struct {
	version  int
	material backupasset.DomainKeyMaterial
}

type cancelingByVersionKeySource struct {
	cancel context.CancelFunc
	called bool
}

func (source *cancelingByVersionKeySource) ByVersion(
	ctx context.Context, _ backupasset.KeyDomain, _ int,
) (backupasset.DomainKeyMaterial, error) {
	source.called = true
	source.cancel()
	return backupasset.DomainKeyMaterial{}, ctx.Err()
}

type barrierByVersionKeySource struct {
	*backupasset.Keyring
	lookupStarted chan<- byVersionLookup
	releaseLookup <-chan struct{}
	once          sync.Once
	versionsMu    sync.Mutex
	versions      []int
}

type mutatingByVersionKeySource struct {
	*backupasset.Keyring
	afterLookup func(int) error
	calls       int
	returned    [][]byte
}

func (source *mutatingByVersionKeySource) ByVersion(
	ctx context.Context, domain backupasset.KeyDomain, version int,
) (backupasset.DomainKeyMaterial, error) {
	material, err := source.Keyring.ByVersion(ctx, domain, version)
	if err != nil {
		return backupasset.DomainKeyMaterial{}, err
	}
	key := append([]byte(nil), material.Key...)
	clear(material.Key)
	material.Key = key
	source.returned = append(source.returned, material.Key)
	source.calls++
	if source.afterLookup != nil {
		if err := source.afterLookup(source.calls); err != nil {
			return backupasset.DomainKeyMaterial{}, err
		}
	}
	return material, nil
}

func (source *barrierByVersionKeySource) ByVersion(
	ctx context.Context, domain backupasset.KeyDomain, version int,
) (backupasset.DomainKeyMaterial, error) {
	material, err := source.Keyring.ByVersion(ctx, domain, version)
	source.versionsMu.Lock()
	source.versions = append(source.versions, version)
	source.versionsMu.Unlock()
	if err != nil {
		return backupasset.DomainKeyMaterial{}, err
	}
	source.once.Do(func() {
		select {
		case source.lookupStarted <- byVersionLookup{version: version, material: material}:
		case <-ctx.Done():
			return
		}
		select {
		case <-source.releaseLookup:
		case <-ctx.Done():
		}
	})
	return material, nil
}

func (source *barrierByVersionKeySource) loadedVersions() []int {
	source.versionsMu.Lock()
	defer source.versionsMu.Unlock()
	return append([]int(nil), source.versions...)
}

func waitByVersionLookup(
	t *testing.T, ctx context.Context, lookups <-chan byVersionLookup,
) byVersionLookup {
	t.Helper()
	select {
	case lookup := <-lookups:
		return lookup
	case <-ctx.Done():
		t.Fatalf("wait for Export key ByVersion lookup: %v", ctx.Err())
		return byVersionLookup{}
	}
}

func rewrapPersistedJobKeyForTest(
	t *testing.T,
	db *gorm.DB,
	ring *backupasset.Keyring,
	jobID string,
	oldMaterial backupasset.DomainKeyMaterial,
	now time.Time,
	mutate func(*gorm.DB) error,
) (backupasset.DomainKeyMaterial, []byte) {
	t.Helper()
	var job model.BackupAssetExportJob
	if err := db.Where("id = ?", jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var key model.BackupAssetExportKey
	if err := db.Where("job_id = ?", jobID).Take(&key).Error; err != nil {
		t.Fatal(err)
	}
	dek, err := UnwrapJobDEK(JobKeyBinding{
		ExportID: job.ID, SelectionDigest: job.SelectionDigest, KEKVersion: key.KEKVersion, WrapAlgorithm: key.WrapAlgorithm,
	}, oldMaterial.Key, JobKeyEnvelope{Nonce: key.EnvelopeNonce, Ciphertext: key.WrappedDEK})
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := ring.Rotate(context.Background(), backupasset.KeyDomainExportStore, 0)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := WrapJobDEK(JobKeyBinding{
		ExportID: job.ID, SelectionDigest: job.SelectionDigest, KEKVersion: rotated.Version, WrapAlgorithm: key.WrapAlgorithm,
	}, rotated.Key, dek)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.BackupAssetExportKey{}).
			Where("id = ? AND job_id = ? AND state = ? AND key_revision = ?", key.ID, job.ID, "active", key.KeyRevision).
			Updates(map[string]any{
				"wrapped_dek": envelope.Ciphertext, "envelope_nonce": envelope.Nonce,
				"kek_version": rotated.Version, "key_revision": gorm.Expr("key_revision + 1"), "rewrapped_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("valid Export job key rewrap lost its revision")
		}
		if mutate != nil {
			return mutate(tx)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return rotated, dek
}

type recordingAttemptBroker struct {
	inner    AttemptSourceBroker
	bindings []content.AttemptSourceBinding
}

func (broker *recordingAttemptBroker) OpenSession(
	ctx context.Context, binding content.AttemptSourceBinding,
) (content.AttemptInputSession, content.AttemptSourceInfo, error) {
	copyBinding := binding
	copyBinding.AllowedModes = append([]content.SourceMode(nil), binding.AllowedModes...)
	broker.bindings = append(broker.bindings, copyBinding)
	return broker.inner.OpenSession(ctx, binding)
}

type persistentSourceResolverFake struct {
	payload       []byte
	providerBytes int64
	requests      []content.SourceRequest
	driftRead     bool
	statDrift     bool
}

func (source *persistentSourceResolverFake) OpenContentSource(
	_ context.Context, request content.SourceRequest,
) (content.SourceSession, error) {
	source.requests = append(source.requests, request)
	return &persistentSourceSessionFake{
		request: request, payload: source.payload, providerBytes: source.providerBytes,
		drift:     source.driftRead && request.Mode == content.SourceModeSequential,
		statDrift: source.statDrift && len(source.requests) == 2,
	}, nil
}

func (*persistentSourceResolverFake) ValidateContentCacheRoot(context.Context, string) error {
	return nil
}

type persistentSourceSessionFake struct {
	request       content.SourceRequest
	payload       []byte
	providerBytes int64
	drift         bool
	statDrift     bool
}

func (session *persistentSourceSessionFake) Stat() content.SourceStat {
	sourceFingerprint, entryFingerprint := session.request.ExpectedSource, session.request.ExpectedEntry
	if session.statDrift {
		sourceFingerprint, entryFingerprint = "drifted-source-fingerprint", "drifted-entry-fingerprint"
	}
	return content.SourceStat{
		Size: int64(len(session.payload)), MediaType: "text/plain",
		SourceFingerprint: sourceFingerprint, EntryFingerprint: entryFingerprint,
		FingerprintStrong: true,
	}
}

func (*persistentSourceSessionFake) Capabilities() content.SourceCapabilities {
	return content.SourceCapabilities{Sequential: true}
}

func (session *persistentSourceSessionFake) Reader() content.SourceReader {
	return &persistentSourceReaderFake{Reader: bytes.NewReader(session.payload), providerBytes: session.providerBytes}
}

func (session *persistentSourceSessionFake) Revalidate(context.Context) error {
	if session.drift {
		return errors.New("source drift")
	}
	return nil
}

func (*persistentSourceSessionFake) Close() error { return nil }

type persistentSourceReaderFake struct {
	*bytes.Reader
	providerBytes int64
}

func (*persistentSourceReaderFake) Close() error                { return nil }
func (reader *persistentSourceReaderFake) ProviderBytes() int64 { return reader.providerBytes }

type blockingPersistentSourceResolver struct {
	item   FrozenItem
	reader *blockingPersistentSourceReader
}

func newBlockingPersistentSourceResolver(item FrozenItem) *blockingPersistentSourceResolver {
	return &blockingPersistentSourceResolver{item: item, reader: &blockingPersistentSourceReader{
		started: make(chan struct{}), closed: make(chan struct{}),
	}}
}

func (resolver *blockingPersistentSourceResolver) OpenContentSource(
	_ context.Context, request content.SourceRequest,
) (content.SourceSession, error) {
	return &blockingPersistentSourceSession{request: request, item: resolver.item, reader: resolver.reader}, nil
}

func (*blockingPersistentSourceResolver) ValidateContentCacheRoot(context.Context, string) error {
	return nil
}

type blockingPersistentSourceSession struct {
	request content.SourceRequest
	item    FrozenItem
	reader  *blockingPersistentSourceReader
}

func (session *blockingPersistentSourceSession) Stat() content.SourceStat {
	return content.SourceStat{
		Size: session.item.LogicalSize, MediaType: session.item.MediaType,
		SourceFingerprint: session.request.ExpectedSource, EntryFingerprint: session.request.ExpectedEntry,
		FingerprintStrong: true,
	}
}

func (*blockingPersistentSourceSession) Capabilities() content.SourceCapabilities {
	return content.SourceCapabilities{Sequential: true}
}

func (session *blockingPersistentSourceSession) Reader() content.SourceReader { return session.reader }
func (*blockingPersistentSourceSession) Revalidate(context.Context) error     { return nil }
func (session *blockingPersistentSourceSession) Close() error {
	if session.request.Mode != content.SourceModeSequential {
		return nil
	}
	return session.reader.Close()
}

type blockingPersistentSourceReader struct {
	started   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func (reader *blockingPersistentSourceReader) Read([]byte) (int, error) {
	reader.startOnce.Do(func() { close(reader.started) })
	<-reader.closed
	return 0, errors.New("blocked source reader closed")
}

func (reader *blockingPersistentSourceReader) Close() error {
	reader.closeOnce.Do(func() { close(reader.closed) })
	return nil
}

func (*blockingPersistentSourceReader) ProviderBytes() int64 { return 0 }

var _ content.SourceResolver = (*persistentSourceResolverFake)(nil)
var _ content.SourceResolver = (*blockingPersistentSourceResolver)(nil)

func (spy *byVersionKeySpy) ByVersion(
	ctx context.Context, domain backupasset.KeyDomain, version int,
) (backupasset.DomainKeyMaterial, error) {
	spy.versions = append(spy.versions, version)
	return spy.Keyring.ByVersion(ctx, domain, version)
}
