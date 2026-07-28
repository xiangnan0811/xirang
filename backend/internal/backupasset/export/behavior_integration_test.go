package export

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/model"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type lifecycleDestroyCallerContextKey struct{}

type lifecycleDestroyQueryEvent struct {
	caller string
	locked bool
}

type lifecycleDestroyResult struct {
	caller string
	err    error
}

func TestExportBehaviorSQLite(t *testing.T) {
	runExportBehaviorContract(t, openExportBehaviorSQLite)
}

func TestExportBehaviorPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_EXPORT_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_EXPORT_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runExportBehaviorContract(t, func(t *testing.T) exportBehaviorFixture {
		return openExportBehaviorPostgres(t, dsn)
	})
}

func runExportBehaviorContract(t *testing.T, open func(*testing.T) exportBehaviorFixture) {
	t.Helper()
	fixture := open(t)
	item := frozenItemFixture()
	selection, err := FreezeSelection([]FrozenItem{item}, nil, fixture.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	fixture.resolver.explicit = selection
	actor := SelectionActor{UserID: 41, Role: "admin"}
	request := CreateRequest{
		Actor: actor,
		Selection: CreateSelectionV1{
			SchemaVersion: 1, Kind: SelectionExplicit,
			Refs: []backupasset.AssetRef{item.Ref},
		},
		IdempotencyKey: "export-behavior-create-key-0001",
		ArchiveFormat:  ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	}
	created, err := fixture.service.Create(context.Background(), request)
	if err != nil {
		t.Fatalf("%s create Export: %v", fixture.engine, err)
	}
	replayed, err := fixture.service.Create(context.Background(), request)
	if err != nil {
		t.Fatalf("%s replay Export: %v", fixture.engine, err)
	}
	if replayed.Job.ID != created.Job.ID || !replayed.Replay {
		t.Fatalf("%s replay=%+v created=%+v", fixture.engine, replayed, created)
	}
	if len(fixture.leaseSpy.requests) != 1 || !fixture.leaseSpy.requests[0].AbsoluteDeadline.IsZero() ||
		len(fixture.leaseSpy.leases) != 1 {
		t.Fatalf("%s lease requests=%+v leases=%+v job deadline=%v",
			fixture.engine, fixture.leaseSpy.requests, fixture.leaseSpy.leases, created.Job.AbsoluteDeadline)
	}
	var sourceLease model.BackupAssetExportSourceLease
	if err := fixture.db.Where("job_id = ?", created.Job.ID).Take(&sourceLease).Error; err != nil {
		t.Fatalf("%s persisted source lease: %v", fixture.engine, err)
	}
	if !sourceLease.AbsoluteDeadline.Equal(fixture.leaseSpy.leases[0].AbsoluteDeadline) ||
		created.Job.AbsoluteDeadline.After(sourceLease.AbsoluteDeadline) {
		t.Fatalf("%s source deadline=%v returned=%v job cap=%v", fixture.engine,
			sourceLease.AbsoluteDeadline, fixture.leaseSpy.leases[0].AbsoluteDeadline, created.Job.AbsoluteDeadline)
	}
	assertExportBehaviorCount(t, fixture, &model.BackupAssetExportJob{}, 1)
	assertExportBehaviorCount(t, fixture, &model.BackupAssetExportIdempotency{}, 1)
	var latch model.BackupAssetExportQuotaBucket
	if err := fixture.db.Where("scope = ? AND subject = ?", "global", "global").Take(&latch).Error; err != nil {
		t.Fatalf("%s permanent use latch: %v", fixture.engine, err)
	}
	if latch.ActiveJobs != 1 || latch.ReservedStoreBytes <= 0 {
		t.Fatalf("%s permanent use latch counters=%+v", fixture.engine, latch)
	}
	t.Run("ReadySourceLease/MaintainsVerifiedReadyAfterExecutionDeadline", func(t *testing.T) {
		ready := createPublishedExportBehaviorReadyFixture(
			t, open, selection, actor, item, "export-behavior-ready-source-deadline",
		)
		isolated := ready.fixture
		readyCreated := ready.created
		verified, err := ready.worker.ReconcileJob(
			context.Background(), PersistentReconcileRequest{JobID: readyCreated.Job.ID},
		)
		if err != nil || verified.ReadyIntegrity == nil {
			t.Fatalf("%s verify ready tuple result=%+v err=%v", isolated.engine, verified, err)
		}

		var readyJob model.BackupAssetExportJob
		var itemBefore model.BackupAssetExportItem
		var attemptBefore model.BackupAssetExportAttempt
		var artifactBefore model.BackupAssetExportArtifact
		var keyBefore model.BackupAssetExportKey
		var sourceBefore model.BackupAssetExportSourceLease
		if err := isolated.db.First(&readyJob, "id = ?", readyCreated.Job.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := isolated.db.Where("job_id = ?", readyJob.ID).Take(&itemBefore).Error; err != nil {
			t.Fatal(err)
		}
		if err := isolated.db.Where("job_id = ?", readyJob.ID).Take(&sourceBefore).Error; err != nil {
			t.Fatal(err)
		}
		if err := isolated.db.Where("job_id = ?", readyJob.ID).Take(&attemptBefore).Error; err != nil {
			t.Fatal(err)
		}
		if err := isolated.db.Where("job_id = ?", readyJob.ID).Take(&artifactBefore).Error; err != nil {
			t.Fatal(err)
		}
		if err := isolated.db.Where("job_id = ?", readyJob.ID).Take(&keyBefore).Error; err != nil {
			t.Fatal(err)
		}
		if readyJob.ExecutionState != string(ExecutionReady) || readyJob.ResultKind != string(ResultComplete) ||
			itemBefore.State != string(ItemPacked) {
			t.Fatalf("%s genuine ready fixture job=%+v item=%+v", isolated.engine, readyJob, itemBefore)
		}
		maintenanceNow := readyJob.AbsoluteDeadline.UTC().Add(time.Second)
		if readyJob.ExpiresAt == nil || !maintenanceNow.Before(readyJob.ExpiresAt.UTC()) ||
			!maintenanceNow.Before(sourceBefore.AbsoluteDeadline.UTC()) {
			t.Fatalf("%s ready maintenance fixture does not isolate deadlines: job=%s now=%s expiry=%v source=%s",
				isolated.engine, readyJob.AbsoluteDeadline, maintenanceNow, readyJob.ExpiresAt, sourceBefore.AbsoluteDeadline)
		}
		expiredLease := maintenanceNow.Add(-time.Second)
		if err := isolated.db.Model(&model.RecoveryPointLease{}).Where("id = ?", sourceBefore.LeaseID).
			Update("lease_expires_at", expiredLease).Error; err != nil {
			t.Fatal(err)
		}
		var leaseBefore model.RecoveryPointLease
		if err := isolated.db.First(&leaseBefore, "id = ?", sourceBefore.LeaseID).Error; err != nil {
			t.Fatal(err)
		}
		sourceLeases, err := backupasset.NewLeaseService(isolated.db, func() time.Time { return maintenanceNow }, backupasset.LeaseConfig{
			Duration: isolated.config.LeaseTTL, Heartbeat: isolated.config.LeaseRenewMargin,
			AbsoluteDeadline: 2 * time.Hour,
		})
		if err != nil {
			t.Fatal(err)
		}
		coordinator, err := NewAttemptCoordinator(isolated.db, func() time.Time { return maintenanceNow }, sourceLeases)
		if err != nil {
			t.Fatal(err)
		}
		maintained, err := coordinator.MaintainSourceLeases(
			context.Background(), SourceLeaseMaintenanceRequest{
				JobID: readyJob.ID, ReadyIntegrity: verified.ReadyIntegrity,
			},
		)
		if err != nil || !maintained.TakenOver || len(maintained.LeaseExpiresAt) != 1 ||
			!maintained.AbsoluteDeadline.Equal(sourceBefore.AbsoluteDeadline.UTC()) {
			t.Fatalf("%s verified ready maintenance=%+v err=%v", isolated.engine, maintained, err)
		}
		var sourceAfter model.BackupAssetExportSourceLease
		var leaseAfter model.RecoveryPointLease
		var jobAfter model.BackupAssetExportJob
		var itemAfter model.BackupAssetExportItem
		var attemptAfter model.BackupAssetExportAttempt
		var artifactAfter model.BackupAssetExportArtifact
		var keyAfter model.BackupAssetExportKey
		if err := isolated.db.First(&sourceAfter, "id = ?", sourceBefore.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := isolated.db.First(&leaseAfter, "id = ?", sourceBefore.LeaseID).Error; err != nil {
			t.Fatal(err)
		}
		if err := isolated.db.First(&jobAfter, "id = ?", readyJob.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := isolated.db.First(&itemAfter, "id = ?", itemBefore.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := isolated.db.First(&attemptAfter, "id = ?", attemptBefore.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := isolated.db.First(&artifactAfter, "id = ?", artifactBefore.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := isolated.db.First(&keyAfter, "id = ?", keyBefore.ID).Error; err != nil {
			t.Fatal(err)
		}
		if sourceAfter.LeaseAttemptID == sourceBefore.LeaseAttemptID || sourceAfter.FenceHash == sourceBefore.FenceHash ||
			!sourceAfter.AbsoluteDeadline.Equal(sourceBefore.AbsoluteDeadline.UTC()) ||
			!leaseAfter.AbsoluteDeadline.Equal(leaseBefore.AbsoluteDeadline.UTC()) || leaseAfter.AttemptID == leaseBefore.AttemptID ||
			leaseAfter.Status != "active" || !leaseAfter.LeaseExpiresAt.Equal(maintained.LeaseExpiresAt[0]) ||
			!sourceAfter.RenewedAt.Equal(maintenanceNow) || !sourceAfter.UpdatedAt.Equal(maintenanceNow) ||
			!leaseAfter.LastHeartbeatAt.Equal(maintenanceNow) || !leaseAfter.UpdatedAt.Equal(maintenanceNow) {
			t.Fatalf("%s verified ready takeover did not preserve source cap: source_before=%+v source_after=%+v lease_before=%+v lease_after=%+v",
				isolated.engine, sourceBefore, sourceAfter, leaseBefore, leaseAfter)
		}
		normalizedSource := sourceAfter
		normalizedSource.LeaseAttemptID = sourceBefore.LeaseAttemptID
		normalizedSource.FenceHash = sourceBefore.FenceHash
		normalizedSource.RenewedAt = sourceBefore.RenewedAt
		normalizedSource.UpdatedAt = sourceBefore.UpdatedAt
		normalizedLease := leaseAfter
		normalizedLease.AttemptID = leaseBefore.AttemptID
		normalizedLease.FenceToken = leaseBefore.FenceToken
		normalizedLease.LeaseExpiresAt = leaseBefore.LeaseExpiresAt
		normalizedLease.LastHeartbeatAt = leaseBefore.LastHeartbeatAt
		normalizedLease.UpdatedAt = leaseBefore.UpdatedAt
		if !reflect.DeepEqual(jobAfter, readyJob) || !reflect.DeepEqual(itemAfter, itemBefore) ||
			!reflect.DeepEqual(attemptAfter, attemptBefore) || !reflect.DeepEqual(artifactAfter, artifactBefore) ||
			!reflect.DeepEqual(keyAfter, keyBefore) || !reflect.DeepEqual(normalizedSource, sourceBefore) ||
			!reflect.DeepEqual(normalizedLease, leaseBefore) {
			t.Fatalf("%s verified ready takeover changed frozen tuple: job=%t item=%t attempt=%t artifact=%t key=%t source=%t lease=%t",
				isolated.engine, reflect.DeepEqual(jobAfter, readyJob), reflect.DeepEqual(itemAfter, itemBefore),
				reflect.DeepEqual(attemptAfter, attemptBefore), reflect.DeepEqual(artifactAfter, artifactBefore),
				reflect.DeepEqual(keyAfter, keyBefore), reflect.DeepEqual(normalizedSource, sourceBefore),
				reflect.DeepEqual(normalizedLease, leaseBefore))
		}
	})
	t.Run("ReadySourceLease/RollsBackTwoSourceBatch", func(t *testing.T) {
		ready := createPublishedExportBehaviorReadyFixture(
			t, open, selection, actor, item, "export-behavior-ready-two-source-rollback",
		)
		isolated := ready.fixture
		var job model.BackupAssetExportJob
		if err := isolated.db.First(&job, "id = ?", ready.created.Job.ID).Error; err != nil {
			t.Fatal(err)
		}
		var firstSource model.BackupAssetExportSourceLease
		if err := isolated.db.Where("job_id = ?", job.ID).Take(&firstSource).Error; err != nil {
			t.Fatal(err)
		}
		var secondPoint model.RecoveryPoint
		if err := isolated.db.First(&secondPoint, "id = ?", firstSource.RecoveryPointID).Error; err != nil {
			t.Fatal(err)
		}
		secondRetention := ready.clock.Add(90 * time.Minute)
		secondPoint.ID = strings.Repeat("2", 32)
		secondPoint.SourceFingerprint = "second-source-fingerprint-v1"
		secondPoint.RetentionUntil = &secondRetention
		secondPoint.CreatedAt = ready.clock
		secondPoint.UpdatedAt = ready.clock
		if err := isolated.db.Create(&secondPoint).Error; err != nil {
			t.Fatal(err)
		}
		secondLease, err := isolated.lease.Acquire(context.Background(), backupasset.AcquireLeaseRequest{
			RecoveryPointID: secondPoint.ID, HolderType: backupasset.LeaseHolderExportJob,
			OwnerID: job.ID, AbsoluteDeadline: firstSource.AbsoluteDeadline,
		})
		if err != nil {
			t.Fatal(err)
		}
		secondSourceID, err := backupasset.NewOpaqueID()
		if err != nil {
			t.Fatal(err)
		}
		fenceDigest := sha256.Sum256([]byte(secondLease.Fence.FenceToken))
		secondSource := model.BackupAssetExportSourceLease{
			ID: secondSourceID, JobID: job.ID, RecoveryPointID: secondPoint.ID,
			LeaseID: secondLease.ID, LeaseAttemptID: secondLease.Fence.AttemptID,
			FenceHash: hex.EncodeToString(fenceDigest[:]), AbsoluteDeadline: secondLease.AbsoluteDeadline,
			RetentionUntil: &secondRetention, State: "active",
			AcquiredAt: secondLease.LastHeartbeatAt, RenewedAt: secondLease.LastHeartbeatAt,
			CreatedAt: secondLease.LastHeartbeatAt, UpdatedAt: secondLease.LastHeartbeatAt,
		}
		if err := isolated.db.Create(&secondSource).Error; err != nil {
			t.Fatal(err)
		}
		var sources []model.BackupAssetExportSourceLease
		if err := isolated.db.Where("job_id = ? AND state = ?", job.ID, "active").
			Order("recovery_point_id ASC").Find(&sources).Error; err != nil {
			t.Fatal(err)
		}
		if len(sources) != 2 || sources[0].RecoveryPointID != firstSource.RecoveryPointID ||
			sources[1].RecoveryPointID != secondPoint.ID {
			t.Fatalf("%s two-source order=%+v", isolated.engine, sources)
		}
		deadlines := make([]SourceDeadline, 0, len(sources))
		for _, source := range sources {
			deadlines = append(deadlines, SourceDeadline{
				AbsoluteDeadline: source.AbsoluteDeadline, RetentionUntil: source.RetentionUntil,
			})
		}
		readyExpiry, err := ComputeReadyExpiry(
			job.ReadyAt.UTC(), time.Duration(job.ReadyTTLSeconds)*time.Second, deadlines,
		)
		if err != nil || !readyExpiry.Equal(secondRetention) {
			t.Fatalf("%s two-source ready expiry=%s err=%v want=%s", isolated.engine, readyExpiry, err, secondRetention)
		}
		if err := isolated.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.BackupAssetExportJob{}).Where("id = ?", job.ID).
				Update("expires_at", readyExpiry).Error; err != nil {
				return err
			}
			return tx.Model(&model.BackupAssetExportArtifact{}).Where("id = ?", ready.artifactID).
				Update("expires_at", readyExpiry).Error
		}); err != nil {
			t.Fatal(err)
		}
		verified, err := ready.worker.ReconcileJob(context.Background(), PersistentReconcileRequest{JobID: job.ID})
		if err != nil || verified.ReadyIntegrity == nil {
			t.Fatalf("%s verify two-source ready tuple result=%+v err=%v", isolated.engine, verified, err)
		}
		sourcesBefore, leasesBefore := loadExportBehaviorSourceAuthority(t, isolated.db, job.ID)
		maintenanceNow := ready.clock.Add(time.Minute)
		realLeases, err := backupasset.NewLeaseService(
			isolated.db, func() time.Time { return maintenanceNow }, backupasset.LeaseConfig{
				Duration: isolated.config.LeaseTTL, Heartbeat: isolated.config.LeaseRenewMargin,
				AbsoluteDeadline: 2 * time.Hour,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		injected := errors.New("injected second source maintenance failure")
		failSecond := &failSecondSourceLeaseCoordinator{inner: realLeases, err: injected}
		coordinator, err := NewAttemptCoordinator(isolated.db, func() time.Time { return maintenanceNow }, failSecond)
		if err != nil {
			t.Fatal(err)
		}
		_, err = coordinator.MaintainSourceLeases(context.Background(), SourceLeaseMaintenanceRequest{
			JobID: job.ID, ReadyIntegrity: verified.ReadyIntegrity,
		})
		if !errors.Is(err, injected) {
			t.Fatalf("%s two-source maintenance error=%v, want injected failure", isolated.engine, err)
		}
		wantLeaseIDs := []string{sourcesBefore[0].LeaseID, sourcesBefore[1].LeaseID}
		if !slices.Equal(failSecond.leaseIDs, wantLeaseIDs) || len(failSecond.delegated) != 1 ||
			!failSecond.delegated[0].LastHeartbeatAt.Equal(maintenanceNow) {
			t.Fatalf("%s two-source calls=%v delegated=%+v want=%v", isolated.engine,
				failSecond.leaseIDs, failSecond.delegated, wantLeaseIDs)
		}
		sourcesAfter, leasesAfter := loadExportBehaviorSourceAuthority(t, isolated.db, job.ID)
		if !reflect.DeepEqual(sourcesAfter, sourcesBefore) || !reflect.DeepEqual(leasesAfter, leasesBefore) {
			t.Fatalf("%s two-source rollback leaked mutation: sources_before=%+v sources_after=%+v leases_before=%+v leases_after=%+v",
				isolated.engine, sourcesBefore, sourcesAfter, leasesBefore, leasesAfter)
		}
	})
	t.Run("ReadyIntegrity/SourceFenceLossRevokesAndCleans", func(t *testing.T) {
		ready := createPublishedExportBehaviorReadyFixture(
			t, open, selection, actor, item, "export-behavior-ready-source-fence-loss",
		)
		var source model.BackupAssetExportSourceLease
		if err := ready.fixture.db.Where("job_id = ?", ready.created.Job.ID).Take(&source).Error; err != nil {
			t.Fatal(err)
		}
		if err := ready.fixture.db.Model(&model.BackupAssetExportSourceLease{}).Where("id = ?", source.ID).
			UpdateColumn("fence_hash", strings.Repeat("0", 64)).Error; err != nil {
			t.Fatal(err)
		}
		port := &lifecyclePortFake{}
		lifecycle, err := NewLifecycle(LifecycleDependencies{
			DB: ready.fixture.db, Port: port, Now: func() time.Time { return ready.clock },
		})
		if err != nil {
			t.Fatal(err)
		}
		worker, err := NewPersistentWorker(PersistentWorkerDependencies{
			DB: ready.fixture.db, Keys: ready.ring, Broker: ready.worker.broker,
			Metadata: ready.worker.metadata, Store: ready.store, Lifecycle: lifecycle,
			AttemptWork: NewAttemptWorkRegistry(),
			Now:         func() time.Time { return ready.clock },
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := worker.ReconcileJob(context.Background(), PersistentReconcileRequest{JobID: ready.created.Job.ID})
		if err != nil || result.Action != PersistentReconcileRevoked || result.ReadyIntegrity != nil {
			t.Fatalf("%s ready source-fence cleanup result=%+v err=%v", ready.fixture.engine, result, err)
		}
		var job model.BackupAssetExportJob
		if err := ready.fixture.db.First(&job, "id = ?", ready.created.Job.ID).Error; err != nil {
			t.Fatal(err)
		}
		if job.ExecutionState != string(ExecutionExpired) || job.CleanupState != string(CleanupPurged) ||
			job.ErrorCategory != "source_expired" || validReadyDeliveryJob(job, ready.clock) {
			t.Fatalf("%s source-fence cleanup left ready authority: job=%+v", ready.fixture.engine, job)
		}
		if !slices.Equal(port.calls, []string{
			"fence_attempts", "revoke_deliveries", "drain_streams", "destroy_key",
			"release_sources", "purge_ciphertext", "release_store",
		}) {
			t.Fatalf("%s source-fence cleanup order=%v", ready.fixture.engine, port.calls)
		}
	})
	t.Run("ReadyIntegrity/RejectsExecutionStateDriftBeforeFoundation", func(t *testing.T) {
		ready := createPublishedExportBehaviorReadyFixture(
			t, open, selection, actor, item, "export-behavior-ready-execution-drift",
		)
		token := readyIntegrityForBehaviorFixture(t, ready)
		if err := ready.fixture.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", ready.created.Job.ID).
			Updates(map[string]any{
				"execution_state":     string(ExecutionExpiring),
				"cleanup_state":       string(CleanupRevoking),
				"transition_revision": gorm.Expr("transition_revision + 1"),
			}).Error; err != nil {
			t.Fatal(err)
		}
		beforeSources, beforeLeases := loadExportBehaviorSourceAuthority(t, ready.fixture.db, ready.created.Job.ID)
		coordinator := readyMaintenanceCoordinatorForBehavior(t, ready, ready.clock.Add(time.Minute))
		if _, err := coordinator.MaintainSourceLeases(context.Background(), SourceLeaseMaintenanceRequest{
			JobID: ready.created.Job.ID, ReadyIntegrity: token,
		}); !errors.Is(err, ErrAttemptFenceLost) {
			t.Fatalf("%s execution-state drift error=%v, want ErrAttemptFenceLost", ready.fixture.engine, err)
		}
		afterSources, afterLeases := loadExportBehaviorSourceAuthority(t, ready.fixture.db, ready.created.Job.ID)
		if !reflect.DeepEqual(afterSources, beforeSources) || !reflect.DeepEqual(afterLeases, beforeLeases) {
			t.Fatalf("%s execution-state drift reached source authority", ready.fixture.engine)
		}
	})
	t.Run("ReadyIntegrity/RejectsFabricatedAndReplayedCapability", func(t *testing.T) {
		ready := createPublishedExportBehaviorReadyFixture(
			t, open, selection, actor, item, "export-behavior-ready-capability",
		)
		beforeSources, beforeLeases := loadExportBehaviorSourceAuthority(t, ready.fixture.db, ready.created.Job.ID)
		coordinator := readyMaintenanceCoordinatorForBehavior(t, ready, ready.clock.Add(time.Minute))
		if _, err := coordinator.MaintainSourceLeases(context.Background(), SourceLeaseMaintenanceRequest{
			JobID: ready.created.Job.ID, ReadyIntegrity: &ReadyIntegrityToken{},
		}); !errors.Is(err, ErrAttemptFenceLost) {
			t.Fatalf("%s fabricated ready capability error=%v", ready.fixture.engine, err)
		}
		afterFabricationSources, afterFabricationLeases := loadExportBehaviorSourceAuthority(t, ready.fixture.db, ready.created.Job.ID)
		if !reflect.DeepEqual(afterFabricationSources, beforeSources) || !reflect.DeepEqual(afterFabricationLeases, beforeLeases) {
			t.Fatalf("%s fabricated ready capability reached source authority", ready.fixture.engine)
		}

		token := readyIntegrityForBehaviorFixture(t, ready)
		request := SourceLeaseMaintenanceRequest{JobID: ready.created.Job.ID, ReadyIntegrity: token}
		if _, err := coordinator.MaintainSourceLeases(context.Background(), request); err != nil {
			t.Fatalf("%s genuine ready capability: %v", ready.fixture.engine, err)
		}
		afterGenuineSources, afterGenuineLeases := loadExportBehaviorSourceAuthority(t, ready.fixture.db, ready.created.Job.ID)
		if _, err := coordinator.MaintainSourceLeases(context.Background(), request); !errors.Is(err, ErrAttemptFenceLost) {
			t.Fatalf("%s replayed ready capability error=%v", ready.fixture.engine, err)
		}
		afterReplaySources, afterReplayLeases := loadExportBehaviorSourceAuthority(t, ready.fixture.db, ready.created.Job.ID)
		if !reflect.DeepEqual(afterReplaySources, afterGenuineSources) || !reflect.DeepEqual(afterReplayLeases, afterGenuineLeases) {
			t.Fatalf("%s replayed ready capability reached source authority", ready.fixture.engine)
		}
	})
	t.Run("ReadyIntegrity/RejectsPhysicalCiphertextDriftBeforeFoundation", func(t *testing.T) {
		ready := createPublishedExportBehaviorReadyFixture(
			t, open, selection, actor, item, "export-behavior-ready-physical-drift",
		)
		token := readyIntegrityForBehaviorFixture(t, ready)
		artifactPath := filepath.Join(ready.store.root, readyArtifactLocatorForBehavior(t, ready))
		artifactFile, err := os.OpenFile(artifactPath, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		var firstByte [1]byte
		if _, err := artifactFile.ReadAt(firstByte[:], 0); err != nil {
			_ = artifactFile.Close()
			t.Fatal(err)
		}
		firstByte[0] ^= 0x80
		if _, err := artifactFile.WriteAt(firstByte[:], 0); err != nil {
			_ = artifactFile.Close()
			t.Fatal(err)
		}
		if err := errors.Join(artifactFile.Sync(), artifactFile.Close()); err != nil {
			t.Fatal(err)
		}
		beforeSources, beforeLeases := loadExportBehaviorSourceAuthority(t, ready.fixture.db, ready.created.Job.ID)
		coordinator := readyMaintenanceCoordinatorForBehavior(t, ready, ready.clock.Add(time.Minute))
		if _, err := coordinator.MaintainSourceLeases(context.Background(), SourceLeaseMaintenanceRequest{
			JobID: ready.created.Job.ID, ReadyIntegrity: token,
		}); !errors.Is(err, ErrAttemptFenceLost) || !errors.Is(err, ErrCipherTampered) {
			t.Fatalf("%s physical ready drift error=%v", ready.fixture.engine, err)
		}
		afterSources, afterLeases := loadExportBehaviorSourceAuthority(t, ready.fixture.db, ready.created.Job.ID)
		if !reflect.DeepEqual(afterSources, beforeSources) || !reflect.DeepEqual(afterLeases, beforeLeases) {
			t.Fatalf("%s physical ready drift reached source authority", ready.fixture.engine)
		}
	})
	t.Run("LifecycleReconcile/BoundedCandidatesSkipInertPurgedTerminalRows", func(t *testing.T) {
		isolated := open(t)
		clock := isolated.service.now().UTC()
		currentTime := clock.Add(-40 * time.Minute)
		port := &lifecyclePortFake{}
		lifecycle, err := NewLifecycle(LifecycleDependencies{
			DB: isolated.db, Port: port, Now: func() time.Time { return currentTime },
		})
		if err != nil {
			t.Fatal(err)
		}
		preparePurged := func(jobID string) model.BackupAssetExportJob {
			t.Helper()
			if state, err := lifecycle.Cleanup(context.Background(), jobID); err != nil || state != CleanupPurged {
				t.Fatalf("%s prepare purged lifecycle job=%s state=%s err=%v", isolated.engine, jobID, state, err)
			}
			var job model.BackupAssetExportJob
			if err := isolated.db.First(&job, "id = ?", jobID).Error; err != nil {
				t.Fatal(err)
			}
			return job
		}

		inertStates := []struct {
			name  string
			state ExecutionState
		}{
			{name: "failed", state: ExecutionFailed},
			{name: "source expired", state: ExecutionSourceExpired},
			{name: "canceled", state: ExecutionCanceled},
		}
		inertBefore := make([]model.BackupAssetExportJob, 0, len(inertStates))
		for index, inert := range inertStates {
			jobID := createLifecycleJobForOwner(
				t, isolated.serviceHarness, actor.UserID,
				clock.Add(-90*time.Minute+time.Duration(index)*time.Minute), inert.state, nil,
			)
			before := preparePurged(jobID)
			if before.ExecutionState != string(inert.state) || before.CleanupState != string(CleanupPurged) {
				t.Fatalf("%s older %s tombstone state=%s cleanup=%s",
					isolated.engine, inert.name, before.ExecutionState, before.CleanupState)
			}
			inertBefore = append(inertBefore, before)
		}
		assertInertUnchanged := func(stage string) {
			t.Helper()
			for index, before := range inertBefore {
				var after model.BackupAssetExportJob
				if err := isolated.db.First(&after, "id = ?", before.ID).Error; err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(after, before) {
					t.Fatalf("%s %s older %s tombstone changed:\nbefore=%+v\nafter=%+v",
						isolated.engine, stage, inertStates[index].name, before, after)
				}
			}
		}
		newerJobID := createLifecycleJobForOwner(
			t, isolated.serviceHarness, actor.UserID, clock.Add(-30*time.Minute), ExecutionFailed, nil,
		)
		cancelJobID := createLifecycleJobForOwner(
			t, isolated.serviceHarness, actor.UserID, clock.Add(-25*time.Minute), ExecutionCancelRequested, nil,
		)
		currentTime = clock.Add(-20 * time.Minute)
		cancelBefore := preparePurged(cancelJobID)
		expiresAt := clock.Add(-5 * time.Minute)
		expiringJobID := createLifecycleJobForOwner(
			t, isolated.serviceHarness, actor.UserID, clock.Add(-15*time.Minute), ExecutionExpiring, &expiresAt,
		)
		currentTime = clock.Add(-10 * time.Minute)
		expiringBefore := preparePurged(expiringJobID)

		currentTime = clock
		port.calls = nil
		count, err := lifecycle.Reconcile(context.Background(), 1)
		if err != nil || count != 1 {
			t.Fatalf("%s bounded actionable reconcile count=%d err=%v", isolated.engine, count, err)
		}
		var newerAfter model.BackupAssetExportJob
		if err := isolated.db.First(&newerAfter, "id = ?", newerJobID).Error; err != nil {
			t.Fatal(err)
		}
		if newerAfter.CleanupState != string(CleanupPurged) {
			t.Fatalf("%s newer actionable cleanup=%s; an older inert tombstone consumed limit", isolated.engine, newerAfter.CleanupState)
		}
		assertInertUnchanged("first reconcile")
		wantCalls := []string{
			"fence_attempts", "revoke_deliveries", "drain_streams", "destroy_key",
			"release_sources", "purge_ciphertext", "release_store",
		}
		if !reflect.DeepEqual(port.calls, wantCalls) {
			t.Fatalf("%s bounded actionable lifecycle calls=%v want=%v", isolated.engine, port.calls, wantCalls)
		}

		port.calls = nil
		count, err = lifecycle.Reconcile(context.Background(), 1)
		if err != nil || count != 1 {
			t.Fatalf("%s purged cancel transition count=%d err=%v", isolated.engine, count, err)
		}
		var canceled model.BackupAssetExportJob
		if err := isolated.db.First(&canceled, "id = ?", cancelJobID).Error; err != nil {
			t.Fatal(err)
		}
		if canceled.ExecutionState != string(ExecutionCanceled) || canceled.CleanupState != string(CleanupPurged) ||
			canceled.TransitionRevision != cancelBefore.TransitionRevision+1 {
			t.Fatalf("%s purged cancel transition before=%+v after=%+v", isolated.engine, cancelBefore, canceled)
		}
		if len(port.calls) != 0 {
			t.Fatalf("%s purged cancel transition made lifecycle calls=%v", isolated.engine, port.calls)
		}

		count, err = lifecycle.Reconcile(context.Background(), 1)
		if err != nil || count != 1 {
			t.Fatalf("%s purged expiry transition count=%d err=%v", isolated.engine, count, err)
		}
		var expired model.BackupAssetExportJob
		if err := isolated.db.First(&expired, "id = ?", expiringJobID).Error; err != nil {
			t.Fatal(err)
		}
		if expired.ExecutionState != string(ExecutionExpired) || expired.CleanupState != string(CleanupPurged) ||
			expired.TransitionRevision != expiringBefore.TransitionRevision+1 {
			t.Fatalf("%s purged expiry transition before=%+v after=%+v", isolated.engine, expiringBefore, expired)
		}
		if len(port.calls) != 0 {
			t.Fatalf("%s purged expiry transition made lifecycle calls=%v", isolated.engine, port.calls)
		}

		count, err = lifecycle.Reconcile(context.Background(), 1)
		if err != nil || count != 0 {
			t.Fatalf("%s drained lifecycle reconcile count=%d err=%v", isolated.engine, count, err)
		}
		if len(port.calls) != 0 {
			t.Fatalf("%s drained lifecycle reconcile made lifecycle calls=%v", isolated.engine, port.calls)
		}
		assertInertUnchanged("drained reconcile")
	})
	t.Run("LifecycleProjection/FenceAttemptsMirrorsPublicAndImmutableRows", func(t *testing.T) {
		isolated := open(t)
		runMigrationBackedFenceAttemptsProjection(t, isolated)
	})
	t.Run("ReaderSweep/PersistentFailuresProgressAcrossRestart", func(t *testing.T) {
		runReaderSweepRestartBehaviorContract(t, open(t))
	})
	if fixture.engine == "postgres" {
		t.Run("CancellationSerialization/HeartbeatHoldingJobRowPrecedesCancel", func(t *testing.T) {
			t.Setenv("ALLOW_DIRTY_STARTUP", "true")
			runPostgresHeartbeatVersusCancelBarrier(t, open(t))
		})
		t.Run("LifecycleLockOrder/FenceAttemptsVersusClaim", func(t *testing.T) {
			t.Setenv("ALLOW_DIRTY_STARTUP", "true")
			runPostgresFenceAttemptsVersusClaimBarrier(t, open(t))
		})
		t.Run("LifecycleLockOrder/ReleaseSourcesBeforeFoundationLease", func(t *testing.T) {
			t.Setenv("ALLOW_DIRTY_STARTUP", "true")
			runPostgresReleaseSourcesBeforeFoundationLeaseBarrier(t, open(t), actor, selection)
		})
		t.Run("AttemptTupleLockOrder/ReaderReserveVersusLoader", func(t *testing.T) {
			t.Setenv("ALLOW_DIRTY_STARTUP", "true")
			runPostgresReaderReserveVersusLoaderBarrier(t, open(t))
		})
		t.Run("AttemptTupleLockOrder/ReaderReserveVersusHeartbeat", func(t *testing.T) {
			t.Setenv("ALLOW_DIRTY_STARTUP", "true")
			runPostgresReaderReserveVersusHeartbeatBarrier(t, open(t))
		})
		t.Run("AttemptTupleLockOrder/ReaderReserveVersusSpoolPersistence", func(t *testing.T) {
			t.Setenv("ALLOW_DIRTY_STARTUP", "true")
			runPostgresReaderReserveVersusSpoolPersistenceBarrier(t, open(t))
		})
		t.Run("Idempotency/ConcurrentUniqueWaitDifferentIntent", func(t *testing.T) {
			t.Setenv("ALLOW_DIRTY_STARTUP", "true")
			runPostgresConcurrentDifferentIntentUniqueWait(t, open(t))
		})
		t.Run("QuotaLockOrder/CreateVersusLifecycleNonStoreRelease", func(t *testing.T) {
			t.Setenv("ALLOW_DIRTY_STARTUP", "true")
			runPostgresCreateVersusLifecycleReleaseBarrier(t, open(t), actor, selection)
		})
		t.Run("QuotaLockOrder/ReaderReserveVersusFinalize", func(t *testing.T) {
			t.Setenv("ALLOW_DIRTY_STARTUP", "true")
			runPostgresReaderReserveVersusFinalizeBarrier(t, open(t))
		})
		t.Run("QuotaLockOrder/BlockedBucketContextCancellationRollsBack", func(t *testing.T) {
			t.Setenv("ALLOW_DIRTY_STARTUP", "true")
			runPostgresBlockedQuotaContextBarrier(t, open(t), selection)
		})
		t.Run("ReaderSweep/LiveLeaseTakeoverAndStaleProgress", func(t *testing.T) {
			t.Setenv("ALLOW_DIRTY_STARTUP", "true")
			runPostgresReaderSweepLeaseBarrier(t, open(t))
		})
	}
	t.Run("PersistentLifecyclePortDestroyJobKeyAndSelectionIsMigrationBackedAndIdempotent", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Second)
		store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "export-lifecycle")})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := store.Close(); err != nil {
				t.Errorf("close Export lifecycle store: %v", err)
			}
		})
		quota, err := NewQuotaService(fixture.db, func() time.Time { return now }, fixture.config.Quota)
		if err != nil {
			t.Fatal(err)
		}
		port, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
			DB: fixture.db, Delivery: exportBehaviorLifecycleDeliveryStub{}, Sources: fixture.lease,
			Quota: quota, Store: store, AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return now },
		})
		if err != nil {
			t.Fatal(err)
		}

		t.Run("ConcurrentCallersCommitExactlyOneDestruction", func(t *testing.T) {
			var before model.BackupAssetExportKey
			if err := fixture.db.Where("job_id = ?", created.Job.ID).Take(&before).Error; err != nil {
				t.Fatal(err)
			}

			firstAt := now.Add(time.Second)
			secondAt := now.Add(2 * time.Second)
			firstPort, secondPort := *port, *port
			firstPort.now = func() time.Time { return firstAt }
			secondPort.now = func() time.Time { return secondAt }

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			queryEvents := make(chan lifecycleDestroyQueryEvent, 2)
			type destructionGate struct {
				once sync.Once
				ch   chan struct{}
			}
			gates := map[string]*destructionGate{
				"first":  {ch: make(chan struct{})},
				"second": {ch: make(chan struct{})},
			}
			openGate := func(caller string) {
				gate := gates[caller]
				gate.once.Do(func() { close(gate.ch) })
			}
			firstPIDReady := make(chan int, 1)
			secondPIDReady := make(chan int, 1)
			secondStart := make(chan struct{})
			var secondStartOnce sync.Once
			startSecond := func() { secondStartOnce.Do(func() { close(secondStart) }) }
			results := make(chan lifecycleDestroyResult, 2)
			const callbackName = "test:coordinate_export_key_destruction_queries"
			if fixture.engine == "postgres" {
				if err := fixture.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
					if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "backup_asset_export_keys" {
						return
					}
					caller, ok := tx.Statement.Context.Value(lifecycleDestroyCallerContextKey{}).(string)
					if !ok {
						return
					}
					locked := strings.Contains(strings.ToUpper(tx.Statement.SQL.String()), "FOR UPDATE")
					select {
					case queryEvents <- lifecycleDestroyQueryEvent{caller: caller, locked: locked}:
					case <-ctx.Done():
						_ = tx.AddError(ctx.Err())
						return
					}
					if caller == "first" {
						select {
						case <-gates[caller].ch:
						case <-ctx.Done():
							_ = tx.AddError(ctx.Err())
						}
					}
				}); err != nil {
					cancel()
					openGate("first")
					openGate("second")
					t.Fatal(err)
				}
			}
			var workers sync.WaitGroup
			workers.Add(2)
			workersDone := make(chan struct{})
			go func() {
				workers.Wait()
				close(workersDone)
			}()
			t.Cleanup(func() {
				cancel()
				startSecond()
				openGate("first")
				openGate("second")
				select {
				case <-workersDone:
				case <-time.After(2 * time.Second):
					t.Errorf("%s destruction goroutines did not stop", fixture.engine)
				}
				if fixture.engine == "postgres" {
					if err := fixture.db.Callback().Query().Remove(callbackName); err != nil {
						t.Errorf("remove lifecycle query coordinator: %v", err)
					}
				}
			})

			runDestroy := func(caller string, callerPort PersistentLifecyclePort, pidReady chan<- int) {
				defer workers.Done()
				err := fixture.db.Connection(func(conn *gorm.DB) error {
					if fixture.engine == "postgres" {
						var pid int
						if err := conn.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
							return err
						}
						select {
						case pidReady <- pid:
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					callerPort.db = conn
					callerCtx := context.WithValue(ctx, lifecycleDestroyCallerContextKey{}, caller)
					return callerPort.DestroyJobKeyAndSelection(callerCtx, created.Job.ID)
				})
				results <- lifecycleDestroyResult{caller: caller, err: err}
			}

			go runDestroy("first", firstPort, firstPIDReady)
			go func() {
				select {
				case <-secondStart:
					runDestroy("second", secondPort, secondPIDReady)
				case <-ctx.Done():
					workers.Done()
				}
			}()
			if fixture.engine == "postgres" {
				firstPID := waitExportQuotaBarrier(t, ctx, firstPIDReady, "first destruction backend PID")
				firstEvent := waitExportQuotaBarrier(t, ctx, queryEvents, "first destruction key query")
				if firstEvent.caller != "first" || !firstEvent.locked {
					t.Fatalf("%s first destruction query event=%+v", fixture.engine, firstEvent)
				}
				startSecond()
				secondPID := waitExportQuotaBarrier(t, ctx, secondPIDReady, "second destruction backend PID")
				if secondPID == firstPID {
					t.Fatalf("PostgreSQL destruction callers reused backend PID %d", firstPID)
				}
				waitForPostgresPIDBlockedBy(t, ctx, fixture.db, secondPID, firstPID)
				openGate("first")
				secondEvent := waitExportQuotaBarrier(t, ctx, queryEvents, "second destruction key query")
				if secondEvent.caller != "second" || !secondEvent.locked {
					t.Fatalf("%s second destruction query event=%+v", fixture.engine, secondEvent)
				}
				seen := make(map[string]bool, 2)
				for range 2 {
					result := waitExportQuotaBarrier(t, ctx, results, "PostgreSQL destruction result")
					if result.err != nil || seen[result.caller] || (result.caller != "first" && result.caller != "second") {
						t.Fatalf("%s overlapping destruction result=%+v seen=%v", fixture.engine, result, seen)
					}
					seen[result.caller] = true
				}
			} else {
				startSecond()
				seen := make(map[string]bool, 2)
				for range 2 {
					result := waitExportQuotaBarrier(t, ctx, results, "SQLite destruction result")
					if result.err != nil || seen[result.caller] || (result.caller != "first" && result.caller != "second") {
						t.Fatalf("%s concurrent destruction result=%+v seen=%v", fixture.engine, result, seen)
					}
					seen[result.caller] = true
				}
			}
			waitExportQuotaBarrier(t, ctx, workersDone, "destruction goroutine join")

			var destroyed model.BackupAssetExportKey
			if err := fixture.db.Where("job_id = ?", created.Job.ID).Take(&destroyed).Error; err != nil {
				t.Fatal(err)
			}
			var itemAfterDestroy model.BackupAssetExportItem
			if err := fixture.db.Where("job_id = ?", created.Job.ID).Take(&itemAfterDestroy).Error; err != nil {
				t.Fatal(err)
			}
			if destroyed.State != "destroyed" || len(destroyed.WrappedDEK) != 0 || len(destroyed.EnvelopeNonce) != 0 ||
				destroyed.DestroyedAt == nil || destroyed.KeyRevision != before.KeyRevision+1 ||
				(!destroyed.DestroyedAt.Equal(firstAt) && !destroyed.DestroyedAt.Equal(secondAt)) ||
				len(itemAfterDestroy.PathNonce) != 0 || len(itemAfterDestroy.PathCiphertext) != 0 {
				t.Fatalf("%s overlapping destruction mutated more than once: before=%+v after=%+v item=%+v",
					fixture.engine, before, destroyed, itemAfterDestroy)
			}
			destroyedAt := *destroyed.DestroyedAt
			if err := port.DestroyJobKeyAndSelection(context.Background(), created.Job.ID); err != nil {
				t.Fatalf("%s repeated cryptographic destruction: %v", fixture.engine, err)
			}
			var repeated model.BackupAssetExportKey
			if err := fixture.db.Where("job_id = ?", created.Job.ID).Take(&repeated).Error; err != nil {
				t.Fatal(err)
			}
			if repeated.KeyRevision != destroyed.KeyRevision || repeated.DestroyedAt == nil ||
				!repeated.DestroyedAt.Equal(destroyedAt) {
				t.Fatalf("%s repeated destruction churned key: first=%+v repeated=%+v", fixture.engine, destroyed, repeated)
			}
		})

		t.Run("LostCASReloadsAndAcceptsOnlyCompleteTerminalTombstone", func(t *testing.T) {
			lostRequest := request
			lostRequest.IdempotencyKey = "export-behavior-lost-key-0002"
			lostCreated, err := fixture.service.Create(context.Background(), lostRequest)
			if err != nil {
				t.Fatalf("%s create lost-key Export: %v", fixture.engine, err)
			}
			var before model.BackupAssetExportKey
			if err := fixture.db.Where("job_id = ?", lostCreated.Job.ID).Take(&before).Error; err != nil {
				t.Fatal(err)
			}
			lostAt := now.Add(3 * time.Second)
			var injected atomic.Bool
			const callbackName = "test:inject_export_key_destruction_cas_winner"
			if err := fixture.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "backup_asset_export_keys" ||
					!injected.CompareAndSwap(false, true) {
					return
				}
				if err := tx.Exec(`UPDATE backup_asset_export_keys
					SET state = 'lost', wrapped_dek = ?, envelope_nonce = ?, destroyed_at = ?, key_revision = key_revision + 1
					WHERE job_id = ? AND state = 'active'`, []byte{}, []byte{}, lostAt, lostCreated.Job.ID).Error; err != nil {
					_ = tx.AddError(err)
					return
				}
				if err := tx.Exec(`UPDATE backup_asset_export_items
					SET path_nonce = ?, path_ciphertext = ?, updated_at = ? WHERE job_id = ?`,
					[]byte{}, []byte{}, lostAt, lostCreated.Job.ID).Error; err != nil {
					_ = tx.AddError(err)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := fixture.db.Callback().Update().Remove(callbackName); err != nil {
					t.Errorf("remove lifecycle CAS winner callback: %v", err)
				}
			})

			if err := port.DestroyJobKeyAndSelection(context.Background(), lostCreated.Job.ID); err != nil {
				t.Fatalf("%s lost CAS must resolve as idempotent terminal destruction: %v", fixture.engine, err)
			}
			var lostAfter model.BackupAssetExportKey
			if err := fixture.db.Where("job_id = ?", lostCreated.Job.ID).Take(&lostAfter).Error; err != nil {
				t.Fatal(err)
			}
			var itemAfter model.BackupAssetExportItem
			if err := fixture.db.Where("job_id = ?", lostCreated.Job.ID).Take(&itemAfter).Error; err != nil {
				t.Fatal(err)
			}
			if lostAfter.State != "lost" || lostAfter.KeyRevision != before.KeyRevision+1 || lostAfter.DestroyedAt == nil ||
				!lostAfter.DestroyedAt.Equal(lostAt) || len(lostAfter.WrappedDEK) != 0 || len(lostAfter.EnvelopeNonce) != 0 ||
				len(itemAfter.PathNonce) != 0 || len(itemAfter.PathCiphertext) != 0 {
				t.Fatalf("%s lost CAS did not preserve the winning terminal tombstone: before=%+v after=%+v item=%+v",
					fixture.engine, before, lostAfter, itemAfter)
			}
			if err := port.DestroyJobKeyAndSelection(context.Background(), lostCreated.Job.ID); err != nil {
				t.Fatalf("%s repeated lost-key destruction: %v", fixture.engine, err)
			}
			var repeated model.BackupAssetExportKey
			if err := fixture.db.Where("job_id = ?", lostCreated.Job.ID).Take(&repeated).Error; err != nil {
				t.Fatal(err)
			}
			if repeated.KeyRevision != lostAfter.KeyRevision || repeated.DestroyedAt == nil ||
				!repeated.DestroyedAt.Equal(lostAt) {
				t.Fatalf("%s repeated lost-key destruction churned key: before=%+v after=%+v", fixture.engine, lostAfter, repeated)
			}
		})
	})

	if _, err := fixture.service.Cancel(context.Background(), SelectionActor{UserID: 99, Role: "admin"}, created.Job.ID); err == nil {
		t.Fatalf("%s foreign owner canceled Export", fixture.engine)
	}
	canceled, err := fixture.service.Cancel(context.Background(), actor, created.Job.ID)
	if err != nil || canceled.ExecutionState != ExecutionCancelRequested {
		t.Fatalf("%s cancel=%+v err=%v", fixture.engine, canceled, err)
	}
}

func runReaderSweepRestartBehaviorContract(t *testing.T, fixture exportBehaviorFixture) {
	t.Helper()
	clock := fixture.service.now().UTC()
	for _, userID := range []uint{141, 142, 143} {
		if err := fixture.db.Create(&model.User{
			ID: userID, Username: fmt.Sprintf("reader-sweep-%s-%d", fixture.engine, userID), PasswordHash: "hash", Role: "admin",
			Onboarded: true, CreatedAt: clock, UpdatedAt: clock,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	budget, err := NewAttemptBudgetService(fixture.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	readers := make([]attemptReadSweepFixture, 0, 3)
	for index, userID := range []uint{141, 142, 143} {
		readers = append(readers, createExpiredAttemptReadForSweep(
			t, fixture.serviceHarness, budget, userID, fmt.Sprintf("reader-sweep-%s-%d", fixture.engine, userID),
			clock.Add(-time.Duration(3-index)*time.Minute),
		))
	}
	for _, reader := range readers[:2] {
		if err := fixture.db.Model(&model.BackupAssetExportReservation{}).
			Where("id = ?", reader.reservation.ID).
			UpdateColumn("reserved_provider_bytes", reader.reservation.ReservedBytes+1).Error; err != nil {
			t.Fatal(err)
		}
	}

	processed, reconcileErr := budget.ReconcileExpiredAttemptReads(context.Background(), 2)
	if processed != 0 || !errors.Is(reconcileErr, content.ErrAttemptBudgetExceeded) {
		t.Fatalf("%s initial reader sweep processed=%d err=%v, want persistent failures", fixture.engine, processed, reconcileErr)
	}
	var afterFailures model.BackupAssetExportQuotaBucket
	if err := fixture.db.Where("scope = ? AND subject = ?", "global", "global").Take(&afterFailures).Error; err != nil {
		t.Fatal(err)
	}
	if afterFailures.ReaderSweepCursor != 2 || afterFailures.ReaderSweepHighWater != 3 ||
		afterFailures.ReaderSweepLeaseExpiresAt != nil {
		t.Fatalf("%s failed reader sweep state=%+v", fixture.engine, afterFailures)
	}

	restarted, err := NewAttemptBudgetService(fixture.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	processed, reconcileErr = restarted.ReconcileExpiredAttemptReads(context.Background(), 2)
	if processed != 1 || reconcileErr != nil {
		t.Fatalf("%s restart reader sweep processed=%d err=%v, want healthy progress", fixture.engine, processed, reconcileErr)
	}
	var released int64
	if err := fixture.db.Model(&model.BackupAssetExportReservation{}).
		Where("lease_owner = ? AND kind = ? AND state = ?", readers[2].leaseOwner, "reader", "released").
		Count(&released).Error; err != nil || released != 2 {
		t.Fatalf("%s healthy reader release rows=%d err=%v", fixture.engine, released, err)
	}
	var failedActive int64
	if err := fixture.db.Model(&model.BackupAssetExportReservation{}).
		Where("lease_owner IN ? AND kind = ? AND state = ?", []string{readers[0].leaseOwner, readers[1].leaseOwner}, "reader", "active").
		Count(&failedActive).Error; err != nil || failedActive != 4 {
		t.Fatalf("%s failed readers active=%d err=%v", fixture.engine, failedActive, err)
	}
	var global model.BackupAssetExportQuotaBucket
	if err := fixture.db.Where("scope = ? AND subject = ?", "global", "global").Take(&global).Error; err != nil {
		t.Fatal(err)
	}
	if global.ActiveReaders != 2 || global.ReaderSweepCursor != global.ReaderSweepHighWater ||
		global.ReaderSweepLeaseExpiresAt != nil {
		t.Fatalf("%s reader sweep accounting/progress after restart=%+v", fixture.engine, global)
	}
}

type postgresReaderSweepBarrierContextKey struct{}

func runPostgresReaderSweepLeaseBarrier(t *testing.T, fixture exportBehaviorFixture) {
	t.Helper()
	clock := fixture.service.now().UTC()
	if err := fixture.db.Create(&model.User{
		ID: 144, Username: "postgres-reader-sweep-admin", PasswordHash: "hash", Role: "admin",
		Onboarded: true, CreatedAt: clock, UpdatedAt: clock,
	}).Error; err != nil {
		t.Fatal(err)
	}
	first, err := NewAttemptBudgetService(fixture.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	_ = createExpiredAttemptReadForSweep(t, fixture.serviceHarness, first, 144, "postgres-reader-sweep-lease", clock.Add(-time.Minute))
	firstSweep, acquired, err := first.acquireExpiredAttemptReadSweep(context.Background(), clock)
	if err != nil || !acquired {
		t.Fatalf("PostgreSQL first reader sweep acquired=%v err=%v", acquired, err)
	}
	if processed, err := newAttemptBudgetServiceMust(t, fixture.db, clock).ReconcileExpiredAttemptReads(context.Background(), 1); err != nil || processed != 0 {
		t.Fatalf("PostgreSQL live reader sweep processed=%d err=%v", processed, err)
	}
	takeoverAt := clock.Add(31 * time.Second)
	second := newAttemptBudgetServiceMust(t, fixture.db, takeoverAt)
	secondSweep, acquired, err := second.acquireExpiredAttemptReadSweep(context.Background(), takeoverAt)
	if err != nil || !acquired {
		t.Fatalf("PostgreSQL reader sweep takeover acquired=%v err=%v", acquired, err)
	}
	if secondSweep.revision != firstSweep.revision+1 || secondSweep.cursor != firstSweep.cursor ||
		secondSweep.highWater != firstSweep.highWater {
		t.Fatalf("PostgreSQL reader sweep takeover drift first=%+v second=%+v", firstSweep, secondSweep)
	}
	if err := first.persistExpiredAttemptReadSweepProgress(context.Background(), &firstSweep, firstSweep.cursor, true); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("PostgreSQL stale reader sweep progress err=%v", err)
	}
	if err := second.persistExpiredAttemptReadSweepProgress(context.Background(), &secondSweep, secondSweep.cursor, true); err != nil {
		t.Fatalf("PostgreSQL release reader sweep takeover: %v", err)
	}

	const caller = "postgres_reader_sweep_transaction_boundary"
	const callback = "test:postgres_reader_sweep_transaction_boundary"
	var candidateLoaded atomic.Bool
	var candidateLoadedOutsideTransaction atomic.Bool
	var finalizationQuotaLockAfterCandidate atomic.Bool
	if err := fixture.db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Context.Value(postgresReaderSweepBarrierContextKey{}) != caller {
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
		if err := fixture.db.Callback().Query().Remove(callback); err != nil {
			t.Errorf("remove PostgreSQL reader sweep transaction callback: %v", err)
		}
	})
	processed, err := second.ReconcileExpiredAttemptReads(
		context.WithValue(context.Background(), postgresReaderSweepBarrierContextKey{}, caller), 1,
	)
	if err != nil || processed != 1 {
		t.Fatalf("PostgreSQL reader sweep finalization processed=%d err=%v", processed, err)
	}
	if !candidateLoadedOutsideTransaction.Load() || !finalizationQuotaLockAfterCandidate.Load() {
		t.Fatalf("PostgreSQL reader sweep transaction boundary candidate_outside_tx=%t finalization_after_candidate=%t",
			candidateLoadedOutsideTransaction.Load(), finalizationQuotaLockAfterCandidate.Load())
	}
}

type publishedExportBehaviorReadyFixture struct {
	fixture    exportBehaviorFixture
	created    CreateResult
	clock      time.Time
	worker     *PersistentWorker
	store      *Store
	ring       *backupasset.Keyring
	attemptID  string
	artifactID string
}

type failSecondSourceLeaseCoordinator struct {
	inner     SourceLeaseCoordinator
	err       error
	leaseIDs  []string
	delegated []backupasset.Lease
}

func (coordinator *failSecondSourceLeaseCoordinator) RenewTx(
	ctx context.Context, tx *gorm.DB, fence backupasset.LeaseFence,
) (backupasset.Lease, error) {
	coordinator.leaseIDs = append(coordinator.leaseIDs, fence.LeaseID)
	if len(coordinator.leaseIDs) == 2 {
		return backupasset.Lease{}, coordinator.err
	}
	maintained, err := coordinator.inner.RenewTx(ctx, tx, fence)
	if err == nil {
		coordinator.delegated = append(coordinator.delegated, maintained)
	}
	return maintained, err
}

func (coordinator *failSecondSourceLeaseCoordinator) TakeoverTx(
	ctx context.Context, tx *gorm.DB, request backupasset.TakeoverLeaseRequest,
) (backupasset.Lease, error) {
	coordinator.leaseIDs = append(coordinator.leaseIDs, request.LeaseID)
	if len(coordinator.leaseIDs) == 2 {
		return backupasset.Lease{}, coordinator.err
	}
	maintained, err := coordinator.inner.TakeoverTx(ctx, tx, request)
	if err == nil {
		coordinator.delegated = append(coordinator.delegated, maintained)
	}
	return maintained, err
}

func loadExportBehaviorSourceAuthority(
	t *testing.T, db *gorm.DB, jobID string,
) ([]model.BackupAssetExportSourceLease, []model.RecoveryPointLease) {
	t.Helper()
	var sources []model.BackupAssetExportSourceLease
	if err := db.Where("job_id = ?", jobID).Order("recovery_point_id ASC").Find(&sources).Error; err != nil {
		t.Fatal(err)
	}
	leases := make([]model.RecoveryPointLease, 0, len(sources))
	for _, source := range sources {
		var lease model.RecoveryPointLease
		if err := db.First(&lease, "id = ?", source.LeaseID).Error; err != nil {
			t.Fatal(err)
		}
		leases = append(leases, lease)
	}
	return sources, leases
}

func createPublishedExportBehaviorReadyFixture(
	t *testing.T,
	open func(*testing.T) exportBehaviorFixture,
	selection FrozenSelection,
	actor SelectionActor,
	item FrozenItem,
	idempotencyKey string,
) publishedExportBehaviorReadyFixture {
	t.Helper()
	fixture := open(t)
	fixture.resolver.explicit = selection
	created, err := fixture.service.Create(context.Background(), CreateRequest{
		Actor: actor,
		Selection: CreateSelectionV1{
			SchemaVersion: 1, Kind: SelectionExplicit,
			Refs: []backupasset.AssetRef{item.Ref},
		},
		IdempotencyKey: idempotencyKey,
		ArchiveFormat:  ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := fixture.service.now().UTC()
	claimCoordinator, err := NewAttemptCoordinator(fixture.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := claimCoordinator.Claim(context.Background(), AttemptClaimRequest{
		JobID: created.Job.ID, WorkerOwner: "export-behavior-ready-worker",
	})
	if err != nil {
		t.Fatal(err)
	}
	var itemRow model.BackupAssetExportItem
	if err := fixture.db.Where("job_id = ?", created.Job.ID).Take(&itemRow).Error; err != nil {
		t.Fatal(err)
	}
	budget, err := NewAttemptBudgetService(fixture.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	source := &persistentSourceResolverFake{
		payload: bytes.Repeat([]byte("r"), int(item.LogicalSize)), providerBytes: item.LogicalSize,
	}
	broker, err := content.NewAttemptBroker(source, budget, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "ready-source-export")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ring := backupasset.NewKeyring(fixture.db, func() time.Time { return clock })
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: fixture.db, Keys: ring,
		Broker: broker, Metadata: &metadataValidatorFake{}, Store: store, AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.SpoolItem(context.Background(), PersistentSpoolItemRequest{
		JobID: created.Job.ID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: itemRow.ID,
	}); err != nil {
		t.Fatal(err)
	}
	sealed, err := worker.SealArchive(context.Background(), PersistentSealRequest{
		JobID: created.Job.ID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.PublishReady(context.Background(), PersistentPublishRequest{
		JobID: created.Job.ID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ArtifactID: sealed.ArtifactID,
	}); err != nil {
		t.Fatal(err)
	}
	return publishedExportBehaviorReadyFixture{
		fixture: fixture, created: created, clock: clock, worker: worker, store: store, ring: ring,
		attemptID: claim.AttemptID, artifactID: sealed.ArtifactID,
	}
}

func readyIntegrityForBehaviorFixture(t *testing.T, ready publishedExportBehaviorReadyFixture) *ReadyIntegrityToken {
	t.Helper()
	result, err := ready.worker.ReconcileJob(
		context.Background(), PersistentReconcileRequest{JobID: ready.created.Job.ID},
	)
	if err != nil || result.ReadyIntegrity == nil {
		t.Fatalf("%s verify genuine ready tuple result=%+v err=%v", ready.fixture.engine, result, err)
	}
	return result.ReadyIntegrity
}

func readyMaintenanceCoordinatorForBehavior(
	t *testing.T, ready publishedExportBehaviorReadyFixture, now time.Time,
) *AttemptCoordinator {
	t.Helper()
	leases, err := backupasset.NewLeaseService(
		ready.fixture.db, func() time.Time { return now }, backupasset.LeaseConfig{
			Duration: ready.fixture.config.LeaseTTL, Heartbeat: ready.fixture.config.LeaseRenewMargin,
			AbsoluteDeadline: 2 * time.Hour,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewAttemptCoordinator(ready.fixture.db, func() time.Time { return now }, leases)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func readyArtifactLocatorForBehavior(t *testing.T, ready publishedExportBehaviorReadyFixture) string {
	t.Helper()
	var artifact model.BackupAssetExportArtifact
	if err := ready.fixture.db.First(&artifact, "id = ?", ready.artifactID).Error; err != nil {
		t.Fatal(err)
	}
	return artifact.Locator
}

func runPostgresHeartbeatVersusCancelBarrier(t *testing.T, fixture exportBehaviorFixture) {
	t.Helper()
	created, claim, _ := createClaimedExportForAttemptBudget(
		t, fixture.serviceHarness, 41, "postgres-heartbeat-versus-cancel",
	)
	actor := SelectionActor{UserID: 41, Role: "admin"}
	clock := fixture.service.now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	heartbeatGate := make(chan struct{})
	var heartbeatGateOnce sync.Once
	releaseHeartbeat := func() { heartbeatGateOnce.Do(func() { close(heartbeatGate) }) }
	heartbeatPIDReady := make(chan int, 1)
	cancelPIDReady := make(chan int, 1)
	heartbeatLocked := make(chan struct{}, 1)
	cancelLockAttempted := make(chan struct{}, 1)
	heartbeatResult := make(chan error, 1)
	cancelResult := make(chan error, 1)
	var pauseHeartbeat atomic.Bool
	const (
		heartbeatCaller = "postgres_heartbeat_before_cancel"
		cancelCaller    = "postgres_cancel_after_heartbeat"
		beforeHook      = "test:postgres_cancel_job_lock_attempt"
		afterHook       = "test:postgres_heartbeat_pause_with_job_lock"
	)
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(beforeHook, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != postgresExportJobsTable ||
			tx.Statement.Context.Value(exportQuotaBarrierContextKey{}) != cancelCaller {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; locked {
			select {
			case cancelLockAttempted <- struct{}{}:
			default:
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Callback().Query().After("gorm:query").Register(afterHook, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != postgresExportJobsTable ||
			tx.Statement.Context.Value(exportQuotaBarrierContextKey{}) != heartbeatCaller {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked || !pauseHeartbeat.CompareAndSwap(false, true) {
			return
		}
		heartbeatLocked <- struct{}{}
		select {
		case <-heartbeatGate:
		case <-ctx.Done():
			_ = tx.AddError(ctx.Err())
		}
	}); err != nil {
		_ = fixture.db.Callback().Query().Remove(beforeHook)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseHeartbeat()
		if err := fixture.db.Callback().Query().Remove(afterHook); err != nil {
			t.Errorf("remove heartbeat pause callback: %v", err)
		}
		if err := fixture.db.Callback().Query().Remove(beforeHook); err != nil {
			t.Errorf("remove cancel lock callback: %v", err)
		}
	})

	go func() {
		err := fixture.db.Connection(func(conn *gorm.DB) error {
			var pid int
			if err := conn.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				return err
			}
			heartbeatPIDReady <- pid
			sourceLeases, err := backupasset.NewLeaseService(conn, func() time.Time { return clock }, backupasset.LeaseConfig{
				Duration: fixture.config.LeaseTTL, Heartbeat: fixture.config.LeaseRenewMargin, AbsoluteDeadline: 2 * time.Hour,
			})
			if err != nil {
				return err
			}
			coordinator, err := NewAttemptCoordinator(conn, func() time.Time { return clock }, sourceLeases)
			if err != nil {
				return err
			}
			heartbeatCtx := context.WithValue(ctx, exportQuotaBarrierContextKey{}, heartbeatCaller)
			_, err = coordinator.Heartbeat(heartbeatCtx, AttemptHeartbeatRequest{
				JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
			})
			return err
		})
		heartbeatResult <- err
	}()
	heartbeatPID := waitExportQuotaBarrier(t, ctx, heartbeatPIDReady, "heartbeat backend PID")
	waitExportQuotaBarrier(t, ctx, heartbeatLocked, "heartbeat job-row lock")

	go func() {
		err := fixture.db.Connection(func(conn *gorm.DB) error {
			var pid int
			if err := conn.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				return err
			}
			cancelPIDReady <- pid
			service := *fixture.service
			service.db = conn
			cancelCtx := context.WithValue(ctx, exportQuotaBarrierContextKey{}, cancelCaller)
			_, err := service.Cancel(cancelCtx, actor, created.JobID)
			return err
		})
		cancelResult <- err
	}()
	cancelPID := waitExportQuotaBarrier(t, ctx, cancelPIDReady, "cancel backend PID")
	if cancelPID == heartbeatPID {
		t.Fatalf("PostgreSQL heartbeat and cancel reused backend PID %d", cancelPID)
	}
	waitExportQuotaBarrier(t, ctx, cancelLockAttempted, "cancel job-row lock attempt")
	waitForPostgresPIDBlockedBy(t, ctx, fixture.db, cancelPID, heartbeatPID)
	releaseHeartbeat()
	if err := waitExportQuotaBarrier(t, ctx, heartbeatResult, "heartbeat result"); err != nil {
		t.Fatalf("heartbeat holding job row: %v", err)
	}
	if err := waitExportQuotaBarrier(t, ctx, cancelResult, "cancel result"); err != nil {
		t.Fatalf("cancel after heartbeat: %v", err)
	}
	var job model.BackupAssetExportJob
	if err := fixture.db.First(&job, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(ExecutionCancelRequested) {
		t.Fatalf("serialized heartbeat/cancel final job=%+v", job)
	}
}

type exportQuotaBarrierContextKey struct{}

type exportQuotaCreateResult struct {
	result CommitCreateResult
	err    error
}

type exportQuotaReadResult struct {
	reservation content.AttemptReadReservation
	err         error
}

type postgresAttemptTupleHolder func(context.Context, *gorm.DB) error

const (
	postgresExportQuotaBucketsTable = "backup_asset_export_quota_buckets"
	postgresExportJobsTable         = "backup_asset_export_jobs"
	postgresExportAttemptsTable     = "backup_asset_export_attempts"
	postgresExportItemsTable        = "backup_asset_export_items"
	postgresExportItemAttemptsTable = "backup_asset_export_item_attempts"
)

func runMigrationBackedFenceAttemptsProjection(t *testing.T, fixture exportBehaviorFixture) {
	t.Helper()
	created, claim, _ := createClaimedExportForAttemptBudget(
		t, fixture.serviceHarness, 41, "migration-backed-active",
	)
	job := loadFenceAttemptsJob(t, fixture.db, created.JobID)
	attempt := loadFenceAttemptsAttempt(t, fixture.db, claim.AttemptID)
	mixed := seedFenceAttemptsMixedProjections(t, fixture.serviceHarness, job, attempt)
	if err := fixture.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", job.ID).
		Update("execution_state", string(ExecutionCancelRequested)).Error; err != nil {
		t.Fatal(err)
	}
	job = loadFenceAttemptsJob(t, fixture.db, job.ID)
	attempt = loadFenceAttemptsAttempt(t, fixture.db, attempt.ID)
	port := &PersistentLifecyclePort{db: fixture.db, now: fixture.service.now, attemptWork: NewAttemptWorkRegistry()}
	if err := port.FenceAttempts(context.Background(), job.ID); err != nil {
		t.Fatalf("%s migration-backed active FenceAttempts: %v", fixture.engine, err)
	}
	afterAttempt := loadFenceAttemptsAttempt(t, fixture.db, attempt.ID)
	if afterAttempt.State != string(AttemptCanceled) || afterAttempt.IsCurrent ||
		afterAttempt.FailureCategory != "canceled" || afterAttempt.FinishedAt == nil {
		t.Fatalf("%s migration-backed active attempt=%+v", fixture.engine, afterAttempt)
	}
	assertFenceAttemptsMixedProjectionsClosed(
		t, mixed, loadFenceAttemptsItems(t, fixture.db, job.ID),
		loadFenceAttemptsItemAttempts(t, fixture.db, job.ID, attempt.ID), "canceled",
	)
	afterJob := loadFenceAttemptsJob(t, fixture.db, job.ID)
	if afterJob.CurrentAttemptID != nil || afterJob.PackedCount != 1 ||
		afterJob.FailedCount != int64(len(mixed.items)-1) ||
		afterJob.LogicalBytes != mixed.logicalBytes || afterJob.ProviderBytes != mixed.providerBytes {
		t.Fatalf("%s migration-backed active counters=%+v fixture=%+v", fixture.engine, afterJob, mixed)
	}

	sealedCreated, sealedClaim, sealedItemAttempt := createClaimedExportForAttemptBudget(
		t, fixture.serviceHarness, 41, "migration-backed-sealed",
	)
	sealedJob := loadFenceAttemptsJob(t, fixture.db, sealedCreated.JobID)
	sealedAttempt := loadFenceAttemptsAttempt(t, fixture.db, sealedClaim.AttemptID)
	sealedItems := loadFenceAttemptsItems(t, fixture.db, sealedJob.ID)
	if len(sealedItems) != 1 {
		t.Fatalf("%s migration-backed sealed item count=%d want=1", fixture.engine, len(sealedItems))
	}
	finishedAt := fixture.service.now().UTC().Add(-time.Second)
	if err := fixture.db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", sealedItemAttempt.ID).
		Updates(map[string]any{
			"state": string(ItemPacked), "logical_bytes": sealedItems[0].LogicalSize,
			"provider_bytes": sealedItems[0].LogicalSize, "packed_at": finishedAt, "finished_at": finishedAt,
		}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetExportItem{}).Where("id = ?", sealedItems[0].ID).
		Updates(map[string]any{
			"state": string(ItemPacked), "logical_bytes": sealedItems[0].LogicalSize,
			"provider_bytes": sealedItems[0].LogicalSize,
		}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", sealedAttempt.ID).
		Updates(map[string]any{"state": string(AttemptSealed), "is_current": false, "finished_at": finishedAt}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", sealedJob.ID).
		Updates(map[string]any{
			"execution_state": string(ExecutionCancelRequested), "result_kind": string(ResultComplete),
			"packed_count": 1, "logical_bytes": sealedItems[0].LogicalSize,
			"provider_bytes": sealedItems[0].LogicalSize,
		}).Error; err != nil {
		t.Fatal(err)
	}
	sealedJob = loadFenceAttemptsJob(t, fixture.db, sealedJob.ID)
	sealedAttempt = loadFenceAttemptsAttempt(t, fixture.db, sealedAttempt.ID)
	sealedItemAttempt = loadFenceAttemptsItemAttempt(t, fixture.db, sealedItemAttempt.ID)
	sealedItems = loadFenceAttemptsItems(t, fixture.db, sealedJob.ID)
	if err := port.FenceAttempts(context.Background(), sealedJob.ID); err != nil {
		t.Fatalf("%s migration-backed sealed FenceAttempts: %v", fixture.engine, err)
	}
	if after := loadFenceAttemptsAttempt(t, fixture.db, sealedAttempt.ID); !reflect.DeepEqual(after, sealedAttempt) {
		t.Fatalf("%s migration-backed sealed attempt mutated: before=%+v after=%+v", fixture.engine, sealedAttempt, after)
	}
	if after := loadFenceAttemptsItemAttempt(t, fixture.db, sealedItemAttempt.ID); !reflect.DeepEqual(after, sealedItemAttempt) {
		t.Fatalf("%s migration-backed sealed item-attempt mutated: before=%+v after=%+v", fixture.engine, sealedItemAttempt, after)
	}
	if after := loadFenceAttemptsItems(t, fixture.db, sealedJob.ID); !reflect.DeepEqual(after, sealedItems) {
		t.Fatalf("%s migration-backed sealed public item mutated: before=%+v after=%+v", fixture.engine, sealedItems, after)
	}
	assertFenceAttemptsJobCleared(t, sealedJob, loadFenceAttemptsJob(t, fixture.db, sealedJob.ID))
}

func runPostgresFenceAttemptsVersusClaimBarrier(t *testing.T, fixture exportBehaviorFixture) {
	t.Helper()
	created, firstClaim, itemAttempt := createClaimedExportForAttemptBudget(
		t, fixture.serviceHarness, 41, "postgres-fence-attempts-versus-claim",
	)
	if err := fixture.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", created.JobID).
		Update("execution_state", string(ExecutionCancelRequested)).Error; err != nil {
		t.Fatal(err)
	}
	initialJob := loadFenceAttemptsJob(t, fixture.db, created.JobID)
	clock := fixture.service.now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	fencerGate := make(chan struct{})
	var fencerGateOnce sync.Once
	openFencerGate := func() { fencerGateOnce.Do(func() { close(fencerGate) }) }
	fencerPIDReady := make(chan int, 1)
	claimPIDReady := make(chan int, 1)
	fencerPaused := make(chan struct{}, 1)
	claimJobLockAttempted := make(chan struct{}, 1)
	claimAttemptLockAttempted := make(chan struct{}, 1)
	fencerResult := make(chan error, 1)
	claimResult := make(chan error, 1)
	var fencerPauseOnce atomic.Bool
	var fencerLockOrder []string
	const (
		fencerCaller = "postgres_fence_attempts_fencer"
		claimCaller  = "postgres_fence_attempts_claim"
		queryHook    = "test:postgres_fence_attempts_lock_order"
		updateHook   = "test:postgres_fence_attempts_pause_after_attempt"
	)

	if err := fixture.db.Callback().Query().Before("gorm:query").Register(queryHook, func(tx *gorm.DB) {
		if tx.Statement == nil {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		caller, _ := tx.Statement.Context.Value(exportQuotaBarrierContextKey{}).(string)
		table := tx.Statement.Table
		if tx.Statement.Schema != nil {
			table = tx.Statement.Schema.Table
		}
		switch caller {
		case fencerCaller:
			fencerLockOrder = append(fencerLockOrder, table)
		case claimCaller:
			switch table {
			case postgresExportJobsTable:
				select {
				case claimJobLockAttempted <- struct{}{}:
				default:
				}
			case postgresExportAttemptsTable:
				select {
				case claimAttemptLockAttempted <- struct{}{}:
				default:
				}
			}
		}
	}); err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := fixture.db.Callback().Update().After("gorm:update").Register(updateHook, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != postgresExportAttemptsTable ||
			tx.Statement.Context.Value(exportQuotaBarrierContextKey{}) != fencerCaller ||
			!fencerPauseOnce.CompareAndSwap(false, true) {
			return
		}
		fencerPaused <- struct{}{}
		select {
		case <-fencerGate:
		case <-ctx.Done():
			_ = tx.AddError(ctx.Err())
		}
	}); err != nil {
		cancel()
		_ = fixture.db.Callback().Query().Remove(queryHook)
		t.Fatal(err)
	}

	var workers sync.WaitGroup
	workers.Add(2)
	workersDone := make(chan struct{})
	go func() {
		workers.Wait()
		close(workersDone)
	}()
	t.Cleanup(func() {
		cancel()
		openFencerGate()
		select {
		case <-workersDone:
		case <-time.After(2 * time.Second):
			t.Errorf("PostgreSQL FenceAttempts/Claim barrier goroutines did not stop")
		}
		if err := fixture.db.Callback().Update().Remove(updateHook); err != nil {
			t.Errorf("remove PostgreSQL FenceAttempts pause callback: %v", err)
		}
		if err := fixture.db.Callback().Query().Remove(queryHook); err != nil {
			t.Errorf("remove PostgreSQL FenceAttempts lock callback: %v", err)
		}
	})

	go func() {
		defer workers.Done()
		err := fixture.db.Connection(func(conn *gorm.DB) error {
			var pid int
			if err := conn.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				return err
			}
			select {
			case fencerPIDReady <- pid:
			case <-ctx.Done():
				return ctx.Err()
			}
			fenceCtx := context.WithValue(ctx, exportQuotaBarrierContextKey{}, fencerCaller)
			port := &PersistentLifecyclePort{db: conn, now: func() time.Time { return clock }, attemptWork: NewAttemptWorkRegistry()}
			return port.FenceAttempts(fenceCtx, created.JobID)
		})
		fencerResult <- err
	}()

	fencerPID := waitExportQuotaBarrier(t, ctx, fencerPIDReady, "FenceAttempts backend PID")
	waitExportQuotaBarrier(t, ctx, fencerPaused, "FenceAttempts pause after Attempt update")

	go func() {
		defer workers.Done()
		err := fixture.db.Connection(func(conn *gorm.DB) error {
			var pid int
			if err := conn.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				return err
			}
			select {
			case claimPIDReady <- pid:
			case <-ctx.Done():
				return ctx.Err()
			}
			coordinator, err := NewAttemptCoordinator(conn, func() time.Time { return clock })
			if err != nil {
				return err
			}
			claimCtx := context.WithValue(ctx, exportQuotaBarrierContextKey{}, claimCaller)
			_, err = coordinator.Claim(claimCtx, AttemptClaimRequest{
				JobID: created.JobID, WorkerOwner: "postgres-competing-claim",
			})
			return err
		})
		claimResult <- err
	}()

	claimPID := waitExportQuotaBarrier(t, ctx, claimPIDReady, "competing Claim backend PID")
	if claimPID == fencerPID {
		openFencerGate()
		t.Fatalf("PostgreSQL FenceAttempts and Claim reused backend PID %d", claimPID)
	}
	waitExportQuotaBarrier(t, ctx, claimJobLockAttempted, "competing Claim Job lock attempt")
	waitForPostgresPIDBlockedBy(t, ctx, fixture.db, claimPID, fencerPID)
	select {
	case <-claimAttemptLockAttempted:
		openFencerGate()
		t.Fatal("competing Claim reached Attempt before FenceAttempts released the Job lock")
	default:
	}
	if want := []string{
		postgresExportJobsTable, postgresExportAttemptsTable,
		postgresExportItemsTable, postgresExportItemAttemptsTable,
	}; !slices.Equal(fencerLockOrder, want) {
		openFencerGate()
		t.Fatalf("PostgreSQL FenceAttempts lock order=%v want=%v", fencerLockOrder, want)
	}
	openFencerGate()
	if err := waitExportQuotaBarrier(t, ctx, fencerResult, "FenceAttempts result"); err != nil {
		t.Fatalf("PostgreSQL FenceAttempts: %v", err)
	}
	if err := waitExportQuotaBarrier(t, ctx, claimResult, "competing Claim result"); !errors.Is(err, ErrAttemptNotClaimable) {
		t.Fatalf("PostgreSQL competing Claim=%v want ErrAttemptNotClaimable", err)
	}
	waitExportQuotaBarrier(t, ctx, workersDone, "FenceAttempts/Claim barrier goroutine join")

	oldAttempt := loadFenceAttemptsAttempt(t, fixture.db, firstClaim.AttemptID)
	oldItemAttempt := loadFenceAttemptsItemAttempt(t, fixture.db, itemAttempt.ID)
	job := loadFenceAttemptsJob(t, fixture.db, created.JobID)
	if oldAttempt.State != string(AttemptCanceled) || oldAttempt.IsCurrent ||
		oldItemAttempt.State != string(ItemFailed) || oldItemAttempt.ErrorCategory != "canceled" ||
		job.CurrentAttemptID != nil || job.CurrentFenceRevision != initialJob.CurrentFenceRevision+1 {
		t.Fatalf("PostgreSQL FenceAttempts/Claim final lineage job=%+v attempt=%+v item_attempt=%+v",
			job, oldAttempt, oldItemAttempt)
	}
}

func runPostgresReleaseSourcesBeforeFoundationLeaseBarrier(
	t *testing.T,
	fixture exportBehaviorFixture,
	actor SelectionActor,
	selection FrozenSelection,
) {
	t.Helper()
	created, err := fixture.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: actor, Selection: selection, IdempotencyKey: "postgres-release-source-before-foundation-lease",
		ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var source model.BackupAssetExportSourceLease
	if err := fixture.db.Where("job_id = ?", created.JobID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	now := fixture.service.now().UTC()
	if err := fixture.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.RecoveryPointLease{}).
			Where("id = ? AND status = ?", source.LeaseID, backupasset.LeaseActive).
			Update("absolute_deadline", now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("made %d Foundation leases reconciliation-eligible, want 1", result.RowsAffected)
		}
		result = tx.Model(&model.BackupAssetExportSourceLease{}).
			Where("id = ? AND job_id = ? AND state = ?", source.ID, created.JobID, "active").
			Update("absolute_deadline", now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("updated %d Export source deadlines, want 1", result.RowsAffected)
		}
		return nil
	}); err != nil {
		t.Fatalf("make source-bound Foundation lease reconciliation-eligible: %v", err)
	}
	if err := fixture.db.Where("id = ?", source.ID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	var foundationBefore model.RecoveryPointLease
	if err := fixture.db.Where("id = ?", source.LeaseID).Take(&foundationBefore).Error; err != nil {
		t.Fatal(err)
	}
	fenceDigest := sha256.Sum256([]byte(foundationBefore.FenceToken))
	if foundationBefore.Status != string(backupasset.LeaseActive) || foundationBefore.AbsoluteDeadline.After(now) ||
		foundationBefore.RecoveryPointID != source.RecoveryPointID || foundationBefore.OwnerID != created.JobID ||
		foundationBefore.AttemptID != source.LeaseAttemptID || fmt.Sprintf("%x", fenceDigest[:]) != source.FenceHash ||
		!foundationBefore.AbsoluteDeadline.Equal(source.AbsoluteDeadline) {
		t.Fatalf("reconciliation-eligible Foundation/source binding drifted: foundation=%+v source=%+v now=%s",
			foundationBefore, source, now)
	}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "release-source-lock-order")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close release source lock-order store: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	type releaseQueryTraceContextKey struct{}
	type releaseQueryTraceEvent struct {
		table     string
		forUpdate bool
	}
	traceCaller := "release-source-order:" + source.ID
	traceEvents := make(chan releaseQueryTraceEvent, 8)
	var traceMu sync.Mutex
	trace := make([]releaseQueryTraceEvent, 0, 2)
	var traceOverflow atomic.Bool
	traceSnapshot := func() []releaseQueryTraceEvent {
		traceMu.Lock()
		defer traceMu.Unlock()
		return append([]releaseQueryTraceEvent(nil), trace...)
	}
	holderGate := make(chan struct{})
	var holderGateOnce sync.Once
	releaseHolder := func() { holderGateOnce.Do(func() { close(holderGate) }) }
	holderPIDReady := make(chan int, 1)
	holderLocked := make(chan struct{}, 1)
	releaserPIDReady := make(chan int, 1)
	holderResult := make(chan error, 1)
	releaserResult := make(chan error, 1)
	var workers sync.WaitGroup
	workersDone := make(chan struct{})
	var joinWorkersOnce sync.Once
	joinWorkers := func() <-chan struct{} {
		joinWorkersOnce.Do(func() {
			go func() {
				workers.Wait()
				close(workersDone)
			}()
		})
		return workersDone
	}
	traceCallback := "test:trace_release_source_foundation_order:" + source.ID
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(traceCallback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Context.Value(releaseQueryTraceContextKey{}) != traceCaller {
			return
		}
		table := tx.Statement.Table
		if tx.Statement.Schema != nil {
			table = tx.Statement.Schema.Table
		}
		if table != "backup_asset_export_source_leases" && table != "recovery_point_leases" {
			return
		}
		event := releaseQueryTraceEvent{table: table}
		if lockingClause, found := tx.Statement.Clauses["FOR"]; found {
			if locking, ok := lockingClause.Expression.(clause.Locking); ok {
				event.forUpdate = strings.EqualFold(locking.Strength, "UPDATE")
			}
		}
		traceMu.Lock()
		trace = append(trace, event)
		traceMu.Unlock()
		select {
		case traceEvents <- event:
		default:
			traceOverflow.Store(true)
		}
	}); err != nil {
		cancel()
		releaseHolder()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		releaseHolder()
		joined := false
		select {
		case <-joinWorkers():
			joined = true
		case <-time.After(2 * time.Second):
			t.Errorf("PostgreSQL source-release barrier goroutines did not stop")
		}
		if !joined {
			return
		}
		if err := fixture.db.Callback().Query().Remove(traceCallback); err != nil {
			t.Errorf("remove source-before-Foundation query trace callback: %v", err)
		}
	})

	workers.Add(1)
	go func() {
		defer workers.Done()
		err := fixture.db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
			var pid int
			if err := conn.WithContext(ctx).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				return err
			}
			select {
			case holderPIDReady <- pid:
			case <-ctx.Done():
				return ctx.Err()
			}
			return conn.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var lockedID string
				if err := tx.Raw(`SELECT id FROM backup_asset_export_source_leases
					WHERE id = ? FOR UPDATE`, source.ID).Scan(&lockedID).Error; err != nil {
					return err
				}
				if lockedID != source.ID {
					return fmt.Errorf("locked source id %q, want %q", lockedID, source.ID)
				}
				holderLocked <- struct{}{}
				select {
				case <-holderGate:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		})
		holderResult <- err
	}()

	holderPID := waitExportQuotaBarrier(t, ctx, holderPIDReady, "source-row holder backend PID")
	waitExportQuotaBarrier(t, ctx, holderLocked, "source-row holder lock")
	workers.Add(1)
	go func() {
		defer workers.Done()
		err := fixture.db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
			var pid int
			if err := conn.WithContext(ctx).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				return err
			}
			select {
			case releaserPIDReady <- pid:
			case <-ctx.Done():
				return ctx.Err()
			}
			quota, err := NewQuotaService(conn, fixture.service.now, fixture.config.Quota)
			if err != nil {
				return err
			}
			port, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
				DB: conn, Delivery: exportBehaviorLifecycleDeliveryStub{}, Sources: fixture.lease,
				Quota: quota, Store: store, AttemptWork: NewAttemptWorkRegistry(), Now: fixture.service.now,
			})
			if err != nil {
				return err
			}
			releaserCtx := context.WithValue(ctx, releaseQueryTraceContextKey{}, traceCaller)
			return port.ReleaseSourcesAndNonStore(releaserCtx, created.JobID)
		})
		releaserResult <- err
	}()

	releaserPID := waitExportQuotaBarrier(t, ctx, releaserPIDReady, "source releaser backend PID")
	if releaserPID == holderPID {
		releaseHolder()
		t.Fatalf("PostgreSQL source holder and releaser reused backend PID %d", holderPID)
	}
	sourceAttempt := waitExportQuotaBarrier(t, ctx, traceEvents, "releaser source-lock query attempt")
	if sourceAttempt.table != "backup_asset_export_source_leases" || !sourceAttempt.forUpdate {
		t.Fatalf("PostgreSQL releaser first source/Foundation query attempt=%+v, want source FOR UPDATE", sourceAttempt)
	}
	waitForPostgresPIDBlockedBy(t, ctx, fixture.db, releaserPID, holderPID)
	for _, event := range traceSnapshot() {
		if event.table == "recovery_point_leases" {
			t.Fatalf("PostgreSQL releaser attempted Foundation query before completing its blocked source lock: trace=%+v",
				traceSnapshot())
		}
	}

	var leaseProbePID int
	var foundationProbe model.RecoveryPointLease
	err = fixture.db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
		if err := conn.WithContext(ctx).Raw("SELECT pg_backend_pid()").Scan(&leaseProbePID).Error; err != nil {
			return err
		}
		return conn.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			result := tx.Raw(`SELECT * FROM recovery_point_leases
				WHERE id = ? FOR UPDATE NOWAIT`, source.LeaseID).Scan(&foundationProbe)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 || foundationProbe.ID != source.LeaseID {
				return fmt.Errorf("NOWAIT Foundation lease probe rows=%d id=%q want=%q",
					result.RowsAffected, foundationProbe.ID, source.LeaseID)
			}
			return nil
		})
	})
	finishFailedProbe := func() {
		releaseHolder()
		_ = waitExportQuotaBarrier(t, ctx, holderResult, "source-row holder result after lease-order failure")
		_ = waitExportQuotaBarrier(t, ctx, releaserResult, "source releaser result after lease-order failure")
		waitExportQuotaBarrier(t, ctx, joinWorkers(), "source-release barrier join after lease-order failure")
	}
	if leaseProbePID == holderPID || leaseProbePID == releaserPID {
		finishFailedProbe()
		t.Fatalf("PostgreSQL Foundation lease probe was not a third backend PID: holder=%d releaser=%d probe=%d",
			holderPID, releaserPID, leaseProbePID)
	}
	if err != nil {
		finishFailedProbe()
		var postgresErr *pgconn.PgError
		if errors.As(err, &postgresErr) {
			if postgresErr.Code == "55P03" {
				t.Fatalf("PostgreSQL old-order violation: releaser pid %d locked Foundation lease %s before source %s; third-pid NOWAIT probe %d failed with SQLSTATE 55P03",
					releaserPID, source.LeaseID, source.ID, leaseProbePID)
			}
			t.Fatalf("PostgreSQL Foundation NOWAIT probe returned unexpected SQLSTATE %s: %v", postgresErr.Code, err)
		}
		t.Fatalf("PostgreSQL Foundation NOWAIT probe returned unexpected non-PostgreSQL error: %T %v", err, err)
	}
	if !reflect.DeepEqual(foundationProbe, foundationBefore) {
		finishFailedProbe()
		t.Fatalf("PostgreSQL pre-lock reconciliation mutated the target Foundation lease before the ordered source lock: before=%+v probe=%+v",
			foundationBefore, foundationProbe)
	}

	releaseHolder()
	if err := waitExportQuotaBarrier(t, ctx, holderResult, "source-row holder result"); err != nil {
		t.Fatalf("release source-row holder: %v", err)
	}
	if err := waitExportQuotaBarrier(t, ctx, releaserResult, "source releaser result"); err != nil {
		t.Fatalf("release sources after source-row barrier: %v", err)
	}
	waitExportQuotaBarrier(t, ctx, joinWorkers(), "source-release barrier goroutine join")
	queryTrace := traceSnapshot()
	if traceOverflow.Load() {
		t.Fatalf("PostgreSQL source-before-Foundation query trace overflowed: trace=%+v", queryTrace)
	}
	firstSourceQuery := -1
	firstFoundationQuery := -1
	for index, event := range queryTrace {
		if event.table == "backup_asset_export_source_leases" && firstSourceQuery < 0 {
			firstSourceQuery = index
		}
		if event.table == "recovery_point_leases" && firstFoundationQuery < 0 {
			firstFoundationQuery = index
		}
	}
	if firstSourceQuery < 0 || firstFoundationQuery <= firstSourceQuery ||
		!queryTrace[firstSourceQuery].forUpdate || !queryTrace[firstFoundationQuery].forUpdate {
		t.Fatalf("PostgreSQL releaser query order=%+v, want source FOR UPDATE before first Foundation FOR UPDATE", queryTrace)
	}

	var foundationAfter model.RecoveryPointLease
	if err := fixture.db.Where("id = ?", source.LeaseID).Take(&foundationAfter).Error; err != nil {
		t.Fatal(err)
	}
	var sourceAfter model.BackupAssetExportSourceLease
	if err := fixture.db.Where("id = ?", source.ID).Take(&sourceAfter).Error; err != nil {
		t.Fatal(err)
	}
	expectedFoundation := foundationBefore
	expectedFoundation.Status = string(backupasset.LeaseExpired)
	expectedFoundation.UpdatedAt = now
	expectedSource := source
	expectedSource.State = "expired"
	expectedSource.ReleasedAt = &now
	expectedSource.UpdatedAt = now
	if !reflect.DeepEqual(foundationAfter, expectedFoundation) || !reflect.DeepEqual(sourceAfter, expectedSource) {
		t.Fatalf("PostgreSQL source-first expiry final tuple mismatch:\nfoundation got=%+v\nfoundation want=%+v\nsource got=%+v\nsource want=%+v",
			foundationAfter, expectedFoundation, sourceAfter, expectedSource)
	}
}

func runPostgresReaderReserveVersusLoaderBarrier(t *testing.T, fixture exportBehaviorFixture) {
	t.Helper()
	created, claim, itemAttempt := createClaimedExportForAttemptBudget(
		t, fixture.serviceHarness, 41, "postgres-loader-reader-lock-order",
	)
	clock := claim.LeaseExpiresAt.Add(-fixture.config.LeaseRenewMargin)
	runPostgresReaderReserveVersusAttemptTupleHolder(
		t, fixture, "loader", itemAttempt.ID, postgresExportQuotaBucketsTable,
		[]string{postgresExportQuotaBucketsTable},
		map[string]struct{}{
			postgresExportQuotaBucketsTable: {},
			postgresExportJobsTable:         {},
		}, clock,
		func(ctx context.Context, conn *gorm.DB) error {
			ring := backupasset.NewKeyring(conn, func() time.Time { return clock })
			loader, err := NewPersistentAttemptLoader(conn, ring, func() time.Time { return clock })
			if err != nil {
				return err
			}
			_, err = loader.Load(ctx, PersistentAttemptLoadRequest{
				JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
			})
			return err
		},
	)
}

func runPostgresReaderReserveVersusHeartbeatBarrier(t *testing.T, fixture exportBehaviorFixture) {
	t.Helper()
	created, claim, itemAttempt := createClaimedExportForAttemptBudget(
		t, fixture.serviceHarness, 41, "postgres-heartbeat-reader-lock-order",
	)
	clock := claim.LeaseExpiresAt.Add(-time.Minute)
	runPostgresReaderReserveVersusAttemptTupleHolder(
		t, fixture, "heartbeat", itemAttempt.ID, postgresExportJobsTable,
		[]string{postgresExportQuotaBucketsTable, postgresExportJobsTable},
		map[string]struct{}{postgresExportJobsTable: {}}, clock,
		func(ctx context.Context, conn *gorm.DB) error {
			coordinator, err := NewAttemptCoordinator(conn, func() time.Time { return clock }, fixture.lease)
			if err != nil {
				return err
			}
			_, err = coordinator.Heartbeat(ctx, AttemptHeartbeatRequest{
				JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
			})
			return err
		},
	)
}

func runPostgresReaderReserveVersusSpoolPersistenceBarrier(t *testing.T, fixture exportBehaviorFixture) {
	t.Helper()
	created, claim, itemAttempt := createClaimedExportForAttemptBudget(
		t, fixture.serviceHarness, 41, "postgres-spool-reader-lock-order",
	)
	clock := claim.LeaseExpiresAt.Add(-fixture.config.LeaseRenewMargin)
	ring := backupasset.NewKeyring(fixture.db, func() time.Time { return clock })
	loader, err := NewPersistentAttemptLoader(fixture.db, ring, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := loader.Load(context.Background(), PersistentAttemptLoadRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].ItemAttemptID != itemAttempt.ID {
		t.Fatalf("unexpected spool barrier snapshot: %+v", snapshot)
	}
	item := snapshot.Items[0]
	runPostgresReaderReserveVersusAttemptTupleHolder(
		t, fixture, "spool", itemAttempt.ID, postgresExportJobsTable,
		[]string{postgresExportQuotaBucketsTable, postgresExportJobsTable},
		map[string]struct{}{postgresExportJobsTable: {}}, clock,
		func(ctx context.Context, conn *gorm.DB) error {
			worker := &PersistentWorker{db: conn, now: func() time.Time { return clock }}
			return worker.persistReadSpool(ctx, PersistentSpoolItemRequest{
				JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: item.ItemID,
			}, item, strings.Repeat("c", 32), CipherResult{
				PlaintextBytes:   item.Frozen.LogicalSize,
				CiphertextBytes:  item.Frozen.LogicalSize + 64,
				CiphertextDigest: strings.Repeat("d", 64),
			})
		},
	)
}

func runPostgresReaderReserveVersusAttemptTupleHolder(
	t *testing.T,
	fixture exportBehaviorFixture,
	label, sessionID, expectedReaderFirstConflictTable string,
	expectedReaderLockPrefix []string,
	holderLockedTables map[string]struct{},
	clock time.Time,
	holder postgresAttemptTupleHolder,
) {
	t.Helper()
	if len(expectedReaderLockPrefix) == 0 ||
		expectedReaderLockPrefix[len(expectedReaderLockPrefix)-1] != expectedReaderFirstConflictTable {
		t.Fatalf("invalid PostgreSQL %s reader lock prefix=%v first conflict=%q",
			label, expectedReaderLockPrefix, expectedReaderFirstConflictTable)
	}
	if _, held := holderLockedTables[expectedReaderFirstConflictTable]; !held {
		t.Fatalf("invalid PostgreSQL %s holder lock set=%v missing first conflict=%q",
			label, holderLockedTables, expectedReaderFirstConflictTable)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	holderReady := make(chan struct{}, 1)
	holderPIDReady := make(chan int, 1)
	readerPIDReady := make(chan int, 1)
	readerFirstConflictAttempted := make(chan []string, 1)
	holderGate := make(chan struct{})
	var holderGateOnce sync.Once
	openHolderGate := func() { holderGateOnce.Do(func() { close(holderGate) }) }
	var holderPaused atomic.Bool
	var readerFirstConflictObserved atomic.Bool
	var readerLockSequence []string
	holderCaller := "attempt_tuple_holder_" + label
	readerCaller := "attempt_tuple_reader_" + label

	queryCallback := "test:postgres_attempt_tuple_lock_order_" + label
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
		if tx.Statement == nil {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		caller, _ := tx.Statement.Context.Value(exportQuotaBarrierContextKey{}).(string)
		if caller != readerCaller {
			return
		}
		lockTable := tx.Statement.Table
		if tx.Statement.Schema != nil {
			lockTable = tx.Statement.Schema.Table
		}
		if len(readerLockSequence) == 0 || readerLockSequence[len(readerLockSequence)-1] != lockTable {
			readerLockSequence = append(readerLockSequence, lockTable)
		}
		if _, held := holderLockedTables[lockTable]; !held ||
			!readerFirstConflictObserved.CompareAndSwap(false, true) {
			return
		}
		readerFirstConflictAttempted <- append([]string(nil), readerLockSequence...)
	}); err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := fixture.db.Callback().Query().After("gorm:query").Register(queryCallback+"_after", func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != postgresExportJobsTable {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		caller, _ := tx.Statement.Context.Value(exportQuotaBarrierContextKey{}).(string)
		if caller != holderCaller || !holderPaused.CompareAndSwap(false, true) {
			return
		}
		holderReady <- struct{}{}
		select {
		case <-holderGate:
		case <-ctx.Done():
			_ = tx.AddError(ctx.Err())
		}
	}); err != nil {
		cancel()
		if removeErr := fixture.db.Callback().Query().Remove(queryCallback); removeErr != nil {
			t.Fatalf("register PostgreSQL %s holder callback: %v; remove reader callback: %v", label, err, removeErr)
		}
		t.Fatalf("register PostgreSQL %s holder callback: %v", label, err)
	}

	var workers sync.WaitGroup
	workers.Add(2)
	workersDone := make(chan struct{})
	go func() {
		workers.Wait()
		close(workersDone)
	}()
	t.Cleanup(func() {
		cancel()
		openHolderGate()
		joinTimer := time.NewTimer(2 * time.Second)
		defer joinTimer.Stop()
		select {
		case <-workersDone:
		case <-joinTimer.C:
			t.Errorf("PostgreSQL %s barrier goroutines did not stop during cleanup", label)
			return
		}
		if err := fixture.db.Callback().Query().Remove(queryCallback + "_after"); err != nil {
			t.Errorf("remove PostgreSQL %s holder callback: %v", label, err)
		}
		if err := fixture.db.Callback().Query().Remove(queryCallback); err != nil {
			t.Errorf("remove PostgreSQL %s reader callback: %v", label, err)
		}
	})

	holderResults := make(chan error, 1)
	go func() {
		defer workers.Done()
		err := fixture.db.Connection(func(conn *gorm.DB) error {
			var pid int
			if err := conn.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				return err
			}
			select {
			case holderPIDReady <- pid:
			case <-ctx.Done():
				return ctx.Err()
			}
			holderCtx := context.WithValue(ctx, exportQuotaBarrierContextKey{}, holderCaller)
			return holder(holderCtx, conn)
		})
		holderResults <- err
	}()

	readerStart := make(chan struct{})
	readerResults := make(chan exportQuotaReadResult, 1)
	go func() {
		defer workers.Done()
		select {
		case <-readerStart:
		case <-ctx.Done():
			readerResults <- exportQuotaReadResult{err: ctx.Err()}
			return
		}
		var reservation content.AttemptReadReservation
		err := fixture.db.Connection(func(conn *gorm.DB) error {
			var pid int
			if err := conn.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				return err
			}
			select {
			case readerPIDReady <- pid:
			case <-ctx.Done():
				return ctx.Err()
			}
			budget, err := NewAttemptBudgetService(conn, func() time.Time { return clock })
			if err != nil {
				return err
			}
			readerCtx := context.WithValue(ctx, exportQuotaBarrierContextKey{}, readerCaller)
			reservation, err = budget.ReserveAttemptRead(readerCtx, content.AttemptReadIntent{
				SessionID: sessionID, Mode: content.SourceModeStat,
			})
			return err
		})
		readerResults <- exportQuotaReadResult{reservation: reservation, err: err}
	}()

	holderPID := waitExportQuotaBarrier(t, ctx, holderPIDReady, label+" holder backend PID")
	waitExportQuotaBarrier(t, ctx, holderReady, label+" holder job lock")
	close(readerStart)
	readerPID := waitExportQuotaBarrier(t, ctx, readerPIDReady, label+" reader backend PID")
	if readerPID == holderPID {
		t.Fatalf("PostgreSQL %s barrier reused backend PID %d", label, readerPID)
	}
	readerLockPrefix := waitExportQuotaBarrier(
		t, ctx, readerFirstConflictAttempted, label+" reader first conflicting lock attempt",
	)
	if !slices.Equal(readerLockPrefix, expectedReaderLockPrefix) {
		t.Fatalf("PostgreSQL %s reader lock prefix=%v, want %v",
			label, readerLockPrefix, expectedReaderLockPrefix)
	}
	readerFirstConflictTable := readerLockPrefix[len(readerLockPrefix)-1]
	if readerFirstConflictTable != expectedReaderFirstConflictTable {
		t.Fatalf("PostgreSQL %s reader first conflicting FOR UPDATE table=%q, want %q",
			label, readerFirstConflictTable, expectedReaderFirstConflictTable)
	}
	waitForPostgresPIDBlockedBy(t, ctx, fixture.db, readerPID, holderPID)
	openHolderGate()

	if err := waitExportQuotaBarrier(t, ctx, holderResults, label+" holder result"); err != nil {
		t.Fatalf("PostgreSQL %s tuple holder: %v", label, err)
	}
	reader := waitExportQuotaBarrier(t, ctx, readerResults, label+" reader result")
	if reader.err != nil {
		t.Fatalf("PostgreSQL reader reserve versus %s: %v", label, reader.err)
	}
	waitExportQuotaBarrier(t, ctx, workersDone, label+" barrier goroutine join")
	budget, err := NewAttemptBudgetService(fixture.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	if err := budget.FinalizeAttemptRead(context.Background(), content.AttemptReadFinalization{
		ReservationID: reader.reservation.ID, ReservedBytes: reader.reservation.ReservedBytes,
		EvidenceKnown: true, Succeeded: true,
	}); err != nil {
		t.Fatalf("finalize %s reader reservation: %v", label, err)
	}
}

func runPostgresConcurrentDifferentIntentUniqueWait(t *testing.T, fixture exportBehaviorFixture) {
	t.Helper()
	item := frozenItemFixture()
	selection, err := FreezeSelection([]FrozenItem{item}, nil, fixture.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	actor := SelectionActor{UserID: 41, Role: "admin"}
	winner, err := fixture.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: actor, Selection: selection, IdempotencyKey: "postgres-unique-wait-winner-job",
		ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	const collisionKey = "postgres-concurrent-different-intent"
	keyDigest, err := IdempotencyKeyDigest(IdempotencyDomainExportCreate, actor.UserID, collisionKey)
	if err != nil {
		t.Fatal(err)
	}
	winnerIntent, err := CreateIntentDigest(CreateIntentV1{
		SchemaVersion: 1, OwnerUserID: actor.UserID, SelectionDigest: selection.Digest,
		ArchiveFormat: string(ArchiveZIP), ArchiveProfile: "zip_deflate_v1", ChunkBytes: fixture.config.ChunkBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	receiptID, err := backupasset.NewOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	now := fixture.service.now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	winnerInserted := make(chan struct{}, 1)
	winnerCommitGate := make(chan struct{})
	var winnerCommitOnce sync.Once
	commitWinner := func() { winnerCommitOnce.Do(func() { close(winnerCommitGate) }) }
	t.Cleanup(commitWinner)
	winnerResults := make(chan error, 1)
	go func() {
		err := fixture.db.Connection(func(conn *gorm.DB) error {
			return conn.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				resultJobID := winner.JobID
				row := model.BackupAssetExportIdempotency{
					ID: receiptID, OwnerUserID: actor.UserID, Endpoint: exportCreateEndpoint,
					KeyDigest: keyDigest, RequestIntentDigest: winnerIntent, State: "committed", ResultJobID: &resultJobID,
					ExpiresAt: now.Add(fixture.config.ReadyTTL), CreatedAt: now, UpdatedAt: now,
				}
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
				winnerInserted <- struct{}{}
				select {
				case <-winnerCommitGate:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		})
		winnerResults <- err
	}()
	waitExportQuotaBarrier(t, ctx, winnerInserted, "uncommitted idempotency winner")

	loserInsertAttempted := make(chan struct{}, 1)
	const loserCaller = "postgres_unique_wait_loser"
	const createCallback = "test:postgres_unique_wait_loser_insert"
	if err := fixture.db.Callback().Create().Before("gorm:create").Register(createCallback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != "backup_asset_export_idempotency" ||
			tx.Statement.Context.Value(exportQuotaBarrierContextKey{}) != loserCaller {
			return
		}
		select {
		case loserInsertAttempted <- struct{}{}:
		default:
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Create().Remove(createCallback) })

	type postgresCollisionResult struct {
		pid    int
		result CommitCreateResult
		err    error
	}
	loserPID := make(chan int, 1)
	loserResults := make(chan postgresCollisionResult, 1)
	go func() {
		var outcome postgresCollisionResult
		outcome.err = fixture.db.Connection(func(conn *gorm.DB) error {
			if err := conn.Raw("SELECT pg_backend_pid()").Scan(&outcome.pid).Error; err != nil {
				return err
			}
			loserPID <- outcome.pid
			service, err := NewService(ServiceDependencies{
				DB: conn, Now: fixture.service.now, Leases: fixture.lease, Keys: fixture.service.keys,
				Resolver: fixture.resolver, Config: fixture.config,
			})
			if err != nil {
				return err
			}
			loserCtx := context.WithValue(ctx, exportQuotaBarrierContextKey{}, loserCaller)
			outcome.result, err = service.CommitCreate(loserCtx, CommitCreateRequest{
				Actor: actor, Selection: selection, IdempotencyKey: collisionKey,
				ArchiveFormat: ArchiveTAR, ArchiveProfile: "tar_none_v1",
			})
			return err
		})
		loserResults <- outcome
	}()
	pid := waitExportQuotaBarrier(t, ctx, loserPID, "PostgreSQL collision backend PID")
	waitExportQuotaBarrier(t, ctx, loserInsertAttempted, "PostgreSQL collision insert attempt")
	waitForPostgresLockWait(t, ctx, fixture.db, pid)
	commitWinner()
	if err := waitExportQuotaBarrier(t, ctx, winnerResults, "PostgreSQL idempotency winner commit"); err != nil {
		t.Fatalf("commit PostgreSQL idempotency winner: %v", err)
	}
	loser := waitExportQuotaBarrier(t, ctx, loserResults, "PostgreSQL collision loser")
	if !errors.Is(loser.err, ErrConflict) || strings.Contains(strings.ToLower(loser.err.Error()), "unique") ||
		strings.Contains(strings.ToLower(loser.err.Error()), "sqlstate") {
		t.Fatalf("PostgreSQL collision loser pid=%d result=%+v err=%v", loser.pid, loser.result, loser.err)
	}
	var jobs int64
	if err := fixture.db.Model(&model.BackupAssetExportJob{}).Count(&jobs).Error; err != nil || jobs != 1 {
		t.Fatalf("PostgreSQL collision jobs=%d err=%v", jobs, err)
	}
}

func waitForPostgresLockWait(t *testing.T, ctx context.Context, db *gorm.DB, pid int) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var activity struct {
			WaitEventType string
			WaitEvent     string
		}
		result := db.Raw(`SELECT COALESCE(wait_event_type, '') AS wait_event_type,
			COALESCE(wait_event, '') AS wait_event FROM pg_stat_activity WHERE pid = ?`, pid).Scan(&activity)
		if result.Error != nil {
			t.Fatalf("inspect PostgreSQL lock wait for pid %d: %v", pid, result.Error)
		}
		if result.RowsAffected == 1 && activity.WaitEventType == "Lock" {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("pid %d did not enter PostgreSQL lock wait (last=%s/%s): %v",
				pid, activity.WaitEventType, activity.WaitEvent, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForPostgresPIDBlockedBy(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	blockedPID, blockerPID int,
) {
	t.Helper()
	var last struct {
		WaitEventType   string
		WaitEvent       string
		BlockedByHolder bool
	}
	err := db.Connection(func(observer *gorm.DB) error {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			result := observer.WithContext(ctx).Raw(`SELECT
				COALESCE(wait_event_type, '') AS wait_event_type,
				COALESCE(wait_event, '') AS wait_event,
				COALESCE(? = ANY(pg_blocking_pids(pid)), false) AS blocked_by_holder
				FROM pg_stat_activity WHERE pid = ?`, blockerPID, blockedPID).Scan(&last)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 1 && last.WaitEventType == "Lock" && last.BlockedByHolder {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
	})
	if err != nil {
		t.Fatalf("PostgreSQL pid %d was not lock-blocked by pid %d (last=%s/%s blocked=%t): %v",
			blockedPID, blockerPID, last.WaitEventType, last.WaitEvent, last.BlockedByHolder, err)
	}
}

func runPostgresCreateVersusLifecycleReleaseBarrier(
	t *testing.T,
	fixture exportBehaviorFixture,
	actor SelectionActor,
	selection FrozenSelection,
) {
	t.Helper()
	if _, err := fixture.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: actor, Selection: selection, IdempotencyKey: "postgres-quota-barrier-baseline",
		ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	}); err != nil {
		t.Fatal(err)
	}
	releaseJob, err := fixture.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: actor, Selection: selection, IdempotencyKey: "postgres-quota-release-job",
		ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "quota-release-store")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close quota release store: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	releaseMutationReady := make(chan struct{}, 1)
	createLockAttempted := make(chan struct{}, 1)
	prematureCreateMutation := make(chan struct{}, 1)
	releaseGate := make(chan struct{})
	var releaseGateOnce sync.Once
	releaseGateOpen := func() { releaseGateOnce.Do(func() { close(releaseGate) }) }
	t.Cleanup(releaseGateOpen)
	var releasePaused atomic.Bool
	var createLockObserved atomic.Bool
	var releaseAllowed atomic.Bool

	const queryCallback = "test:postgres_create_release_quota_lock_attempt"
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "backup_asset_export_quota_buckets" {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		caller, _ := tx.Statement.Context.Value(exportQuotaBarrierContextKey{}).(string)
		if caller == "create" && createLockObserved.CompareAndSwap(false, true) {
			createLockAttempted <- struct{}{}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Query().Remove(queryCallback); err != nil {
			t.Errorf("remove PostgreSQL create/release query callback: %v", err)
		}
	})

	const updateCallback = "test:postgres_create_release_first_quota_mutation"
	if err := fixture.db.Callback().Update().Before("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "backup_asset_export_quota_buckets" {
			return
		}
		caller, _ := tx.Statement.Context.Value(exportQuotaBarrierContextKey{}).(string)
		switch caller {
		case "release":
			if releasePaused.CompareAndSwap(false, true) {
				releaseMutationReady <- struct{}{}
				<-releaseGate
			}
		case "create":
			if !releaseAllowed.Load() {
				select {
				case prematureCreateMutation <- struct{}{}:
				default:
				}
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Update().Remove(updateCallback); err != nil {
			t.Errorf("remove PostgreSQL create/release update callback: %v", err)
		}
	})

	releaseResults := make(chan error, 1)
	go func() {
		err := fixture.db.Connection(func(conn *gorm.DB) error {
			quota, err := NewQuotaService(conn, fixture.service.now, fixture.config.Quota)
			if err != nil {
				return err
			}
			port, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
				DB: conn, Delivery: exportBehaviorLifecycleDeliveryStub{}, Sources: fixture.lease,
				Quota: quota, Store: store, AttemptWork: NewAttemptWorkRegistry(), Now: fixture.service.now,
			})
			if err != nil {
				return err
			}
			releaseCtx := context.WithValue(ctx, exportQuotaBarrierContextKey{}, "release")
			return port.ReleaseSourcesAndNonStore(releaseCtx, releaseJob.JobID)
		})
		releaseResults <- err
	}()
	waitExportQuotaBarrier(t, ctx, releaseMutationReady, "lifecycle release first quota mutation")

	createResults := make(chan exportQuotaCreateResult, 1)
	go func() {
		var result CommitCreateResult
		err := fixture.db.Connection(func(conn *gorm.DB) error {
			service, err := NewService(ServiceDependencies{
				DB: conn, Now: fixture.service.now, Leases: fixture.lease, Keys: fixture.service.keys,
				Resolver: fixture.resolver, Config: fixture.config,
			})
			if err != nil {
				return err
			}
			createCtx := context.WithValue(ctx, exportQuotaBarrierContextKey{}, "create")
			result, err = service.CommitCreate(createCtx, CommitCreateRequest{
				Actor: actor, Selection: selection, IdempotencyKey: "postgres-quota-competing-create",
				ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
			})
			return err
		})
		createResults <- exportQuotaCreateResult{result: result, err: err}
	}()

	select {
	case <-createLockAttempted:
		releaseAllowed.Store(true)
		releaseGateOpen()
	case <-prematureCreateMutation:
		releaseGateOpen()
		t.Fatal("competing create mutated a quota bucket before acquiring the canonical pair")
	case result := <-createResults:
		releaseGateOpen()
		t.Fatalf("competing create returned before waiting on lifecycle quota locks: %v", result.err)
	case <-ctx.Done():
		releaseGateOpen()
		t.Fatalf("wait for competing create quota lock: %v", ctx.Err())
	}

	if err := waitExportQuotaBarrier(t, ctx, releaseResults, "lifecycle release result"); err != nil {
		t.Fatalf("lifecycle non-store release: %v", err)
	}
	created := waitExportQuotaBarrier(t, ctx, createResults, "competing create result")
	if created.err != nil {
		t.Fatalf("competing create after lifecycle release: %v", created.err)
	}
	if created.result.JobID == "" {
		t.Fatal("competing create returned no job")
	}

	var buckets []model.BackupAssetExportQuotaBucket
	if err := fixture.db.Where("(scope = ? AND subject = ?) OR (scope = ? AND subject = ?)",
		"global", "global", "user", "41").Find(&buckets).Error; err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("quota bucket count=%d want=2", len(buckets))
	}
	for _, bucket := range buckets {
		if bucket.ActiveJobs != 2 || bucket.ReservedStoreBytes <= 0 {
			t.Fatalf("quota drift after create/release barrier: %+v", bucket)
		}
	}
	var releasedJobs int64
	if err := fixture.db.Model(&model.BackupAssetExportReservation{}).
		Where("job_id = ? AND kind = ? AND state = ?", releaseJob.JobID, "job", "released").
		Count(&releasedJobs).Error; err != nil || releasedJobs != 2 {
		t.Fatalf("released lifecycle job reservations=%d want=2 err=%v", releasedJobs, err)
	}
}

func runPostgresReaderReserveVersusFinalizeBarrier(t *testing.T, fixture exportBehaviorFixture) {
	t.Helper()
	now := fixture.service.now().UTC()
	if err := fixture.db.Create(&model.User{
		ID: 42, Username: "export-reader-barrier-admin", PasswordHash: "hash", Role: "admin",
		Onboarded: true, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	created, claim, itemAttempt := createClaimedExportForAttemptBudget(
		t, fixture.serviceHarness, 42, "postgres-reader-barrier-job",
	)
	clock := claim.LeaseExpiresAt.Add(-fixture.config.LeaseRenewMargin)
	budget, err := NewAttemptBudgetService(fixture.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	initial, err := budget.ReserveAttemptRead(context.Background(), content.AttemptReadIntent{
		SessionID: itemAttempt.ID, Mode: content.SourceModeStat,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	finalizeMutationReady := make(chan struct{}, 1)
	reserveBucketLockAttempted := make(chan struct{}, 1)
	prematureTupleLockAttempted := make(chan struct{}, 1)
	finalizeGate := make(chan struct{})
	var finalizeGateOnce sync.Once
	openFinalizeGate := func() { finalizeGateOnce.Do(func() { close(finalizeGate) }) }
	t.Cleanup(openFinalizeGate)
	var finalizePaused atomic.Bool
	var reserveFirstLockObserved atomic.Bool

	const queryCallback = "test:postgres_reader_reserve_finalize_lock_attempt"
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		caller, _ := tx.Statement.Context.Value(exportQuotaBarrierContextKey{}).(string)
		if caller != "reader_reserve" || !reserveFirstLockObserved.CompareAndSwap(false, true) {
			return
		}
		if tx.Statement.Schema.Table == "backup_asset_export_quota_buckets" {
			reserveBucketLockAttempted <- struct{}{}
			return
		}
		prematureTupleLockAttempted <- struct{}{}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Query().Remove(queryCallback); err != nil {
			t.Errorf("remove PostgreSQL reader query callback: %v", err)
		}
	})

	const updateCallback = "test:postgres_reader_finalize_first_quota_mutation"
	if err := fixture.db.Callback().Update().Before("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "backup_asset_export_quota_buckets" {
			return
		}
		caller, _ := tx.Statement.Context.Value(exportQuotaBarrierContextKey{}).(string)
		if caller == "reader_finalize" && finalizePaused.CompareAndSwap(false, true) {
			finalizeMutationReady <- struct{}{}
			<-finalizeGate
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Update().Remove(updateCallback); err != nil {
			t.Errorf("remove PostgreSQL reader update callback: %v", err)
		}
	})

	finalizeResults := make(chan error, 1)
	go func() {
		err := fixture.db.Connection(func(conn *gorm.DB) error {
			service, err := NewAttemptBudgetService(conn, func() time.Time { return clock })
			if err != nil {
				return err
			}
			finalizeCtx := context.WithValue(ctx, exportQuotaBarrierContextKey{}, "reader_finalize")
			return service.FinalizeAttemptRead(finalizeCtx, content.AttemptReadFinalization{
				ReservationID: initial.ID, ReservedBytes: initial.ReservedBytes,
				EvidenceKnown: true, Succeeded: true,
			})
		})
		finalizeResults <- err
	}()
	waitExportQuotaBarrier(t, ctx, finalizeMutationReady, "reader finalize first quota mutation")

	reserveResults := make(chan exportQuotaReadResult, 1)
	go func() {
		var reservation content.AttemptReadReservation
		err := fixture.db.Connection(func(conn *gorm.DB) error {
			service, err := NewAttemptBudgetService(conn, func() time.Time { return clock })
			if err != nil {
				return err
			}
			reserveCtx := context.WithValue(ctx, exportQuotaBarrierContextKey{}, "reader_reserve")
			reservation, err = service.ReserveAttemptRead(reserveCtx, content.AttemptReadIntent{
				SessionID: itemAttempt.ID, Mode: content.SourceModeStat,
			})
			return err
		})
		reserveResults <- exportQuotaReadResult{reservation: reservation, err: err}
	}()

	select {
	case <-reserveBucketLockAttempted:
		openFinalizeGate()
	case <-prematureTupleLockAttempted:
		openFinalizeGate()
		t.Fatal("reader reserve attempted an item/attempt/job lock before the canonical quota pair")
	case result := <-reserveResults:
		openFinalizeGate()
		t.Fatalf("reader reserve returned before waiting on finalize quota locks: %v", result.err)
	case <-ctx.Done():
		openFinalizeGate()
		t.Fatalf("wait for reader reserve quota lock: %v", ctx.Err())
	}

	if err := waitExportQuotaBarrier(t, ctx, finalizeResults, "reader finalize result"); err != nil {
		t.Fatalf("reader finalize: %v", err)
	}
	reserved := waitExportQuotaBarrier(t, ctx, reserveResults, "reader reserve result")
	if reserved.err != nil {
		t.Fatalf("reader reserve after finalize: %v", reserved.err)
	}
	if err := budget.FinalizeAttemptRead(context.Background(), content.AttemptReadFinalization{
		ReservationID: reserved.reservation.ID, ReservedBytes: reserved.reservation.ReservedBytes,
		EvidenceKnown: true, Succeeded: true,
	}); err != nil {
		t.Fatalf("cleanup reader reservation: %v", err)
	}

	var activeReaders int64
	if err := fixture.db.Model(&model.BackupAssetExportQuotaBucket{}).
		Select("COALESCE(SUM(active_readers), 0)").Scan(&activeReaders).Error; err != nil || activeReaders != 0 {
		t.Fatalf("reader counter drift=%d err=%v", activeReaders, err)
	}
	var released int64
	if err := fixture.db.Model(&model.BackupAssetExportReservation{}).
		Where("job_id = ? AND kind = ? AND state = ?", created.JobID, "reader", "released").
		Count(&released).Error; err != nil || released != 4 {
		t.Fatalf("released reader reservations=%d want=4 err=%v", released, err)
	}
}

type exportQuotaSnapshot struct {
	global       model.BackupAssetExportQuotaBucket
	user         model.BackupAssetExportQuotaBucket
	jobs         int64
	reservations int64
	idempotency  int64
	sourceLeases int64
	leases       int64
}

func runPostgresBlockedQuotaContextBarrier(
	t *testing.T, fixture exportBehaviorFixture, selection FrozenSelection,
) {
	t.Helper()
	now := fixture.service.now().UTC()
	if err := fixture.db.Create(&model.User{
		ID: 43, Username: "export-context-barrier-admin", PasswordHash: "hash", Role: "admin",
		Onboarded: true, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	actor := SelectionActor{UserID: 43, Role: "admin"}
	if _, err := fixture.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: actor, Selection: selection, IdempotencyKey: "postgres-context-baseline-job",
		ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	}); err != nil {
		t.Fatal(err)
	}
	before := loadExportQuotaSnapshot(t, fixture, 43)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	blockerReady := make(chan struct{}, 1)
	blockerResults := make(chan error, 1)
	blockerGate := make(chan struct{})
	var blockerGateOnce sync.Once
	openBlockerGate := func() { blockerGateOnce.Do(func() { close(blockerGate) }) }
	t.Cleanup(openBlockerGate)
	go func() {
		err := fixture.db.Connection(func(conn *gorm.DB) error {
			return conn.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var bucket model.BackupAssetExportQuotaBucket
				result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
					Where("scope = ? AND subject = ?", "user", "43").Limit(1).Find(&bucket)
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return ErrUnavailable
				}
				blockerReady <- struct{}{}
				select {
				case <-blockerGate:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		})
		blockerResults <- err
	}()
	waitExportQuotaBarrier(t, ctx, blockerReady, "user quota blocker")

	userLockAttempted := make(chan struct{}, 1)
	var lockAttempts atomic.Int32
	const queryCallback = "test:postgres_blocked_context_quota_lock_attempt"
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "backup_asset_export_quota_buckets" {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		caller, _ := tx.Statement.Context.Value(exportQuotaBarrierContextKey{}).(string)
		if caller == "context_create" && lockAttempts.Add(1) == 2 {
			userLockAttempted <- struct{}{}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Query().Remove(queryCallback); err != nil {
			t.Errorf("remove PostgreSQL blocked-context callback: %v", err)
		}
	})

	operationCtx, cancelOperation := context.WithCancel(ctx)
	t.Cleanup(cancelOperation)
	createResults := make(chan exportQuotaCreateResult, 1)
	go func() {
		var result CommitCreateResult
		err := fixture.db.Connection(func(conn *gorm.DB) error {
			service, err := NewService(ServiceDependencies{
				DB: conn, Now: fixture.service.now, Leases: fixture.lease, Keys: fixture.service.keys,
				Resolver: fixture.resolver, Config: fixture.config,
			})
			if err != nil {
				return err
			}
			createCtx := context.WithValue(operationCtx, exportQuotaBarrierContextKey{}, "context_create")
			result, err = service.CommitCreate(createCtx, CommitCreateRequest{
				Actor: actor, Selection: selection, IdempotencyKey: "postgres-context-canceled-create",
				ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
			})
			return err
		})
		createResults <- exportQuotaCreateResult{result: result, err: err}
	}()
	waitExportQuotaBarrier(t, ctx, userLockAttempted, "blocked user quota lock attempt")
	cancelOperation()
	created := waitExportQuotaBarrier(t, ctx, createResults, "canceled create result")
	if !errors.Is(created.err, context.Canceled) {
		t.Fatalf("blocked create error=%v want context.Canceled", created.err)
	}
	if created.result.JobID != "" {
		t.Fatalf("canceled create returned job=%s", created.result.JobID)
	}
	openBlockerGate()
	if err := waitExportQuotaBarrier(t, ctx, blockerResults, "quota blocker result"); err != nil {
		t.Fatalf("release user quota blocker: %v", err)
	}
	after := loadExportQuotaSnapshot(t, fixture, 43)
	if before != after {
		t.Fatalf("blocked create left quota/export drift:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func loadExportQuotaSnapshot(t *testing.T, fixture exportBehaviorFixture, ownerUserID uint) exportQuotaSnapshot {
	t.Helper()
	var snapshot exportQuotaSnapshot
	if err := fixture.db.Where("scope = ? AND subject = ?", "global", "global").Take(&snapshot.global).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Where("scope = ? AND subject = ?", "user", strconv.FormatUint(uint64(ownerUserID), 10)).
		Take(&snapshot.user).Error; err != nil {
		t.Fatal(err)
	}
	for target, destination := range map[any]*int64{
		&model.BackupAssetExportJob{}:         &snapshot.jobs,
		&model.BackupAssetExportReservation{}: &snapshot.reservations,
		&model.BackupAssetExportIdempotency{}: &snapshot.idempotency,
		&model.BackupAssetExportSourceLease{}: &snapshot.sourceLeases,
	} {
		if err := fixture.db.Model(target).Count(destination).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.db.Model(&model.RecoveryPointLease{}).
		Where("holder_type = ?", backupasset.LeaseHolderExportJob).Count(&snapshot.leases).Error; err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func waitExportQuotaBarrier[T any](t *testing.T, ctx context.Context, ch <-chan T, label string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-ctx.Done():
		var zero T
		t.Fatalf("wait for %s: %v", label, ctx.Err())
		return zero
	}
}

type exportBehaviorLifecycleDeliveryStub struct{}

func (exportBehaviorLifecycleDeliveryStub) BeginRevokeExportJob(context.Context, string, string) error {
	return nil
}

func (exportBehaviorLifecycleDeliveryStub) DrainExportJob(context.Context, string) error {
	return nil
}

func assertExportBehaviorCount(t *testing.T, fixture exportBehaviorFixture, value any, want int64) {
	t.Helper()
	var count int64
	if err := fixture.db.Model(value).Count(&count).Error; err != nil || count != want {
		t.Fatalf("%s %T count=%d want=%d err=%v", fixture.engine, value, count, want, err)
	}
}
