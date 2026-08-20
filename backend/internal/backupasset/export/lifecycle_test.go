package export

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/model"

	"github.com/mattn/go-sqlite3"
	"golang.org/x/sys/unix"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestLifecycleRevokesAndDestroysKeyBeforeLeaseRelease(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	jobID := createLifecycleJob(t, harness, now, ExecutionFailed, nil)
	port := &lifecyclePortFake{}
	lifecycle, err := NewLifecycle(LifecycleDependencies{DB: harness.db, Port: port, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lifecycle.Cleanup(context.Background(), jobID)
	if err != nil || result != CleanupPurged {
		t.Fatalf("cleanup result=%s err=%v", result, err)
	}
	want := []string{
		"fence_attempts", "revoke_deliveries", "drain_streams", "destroy_key",
		"release_sources", "purge_ciphertext", "release_store",
	}
	if !reflect.DeepEqual(port.calls, want) {
		t.Fatalf("cleanup order=%v want=%v", port.calls, want)
	}
	assertLifecycleJobState(t, harness, jobID, ExecutionFailed, CleanupPurged)
}

func TestLifecycleMarksExactExportKeyVersionLostAfterRevokingAffectedJobs(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	ring := backupasset.NewKeyring(harness.db, func() time.Time { return now })

	lostJob, lostAttempt, _ := claimFenceAttemptsTestJob(t, harness, "key-loss")
	var lostKey model.BackupAssetExportKey
	if err := harness.db.Where("job_id = ?", lostJob.ID).Take(&lostKey).Error; err != nil {
		t.Fatal(err)
	}
	if lostKey.KEKVersion <= 0 {
		t.Fatalf("lost Export job has invalid KEK version: %+v", lostKey)
	}
	if _, err := ring.Rotate(context.Background(), backupasset.KeyDomainExportStore, 0); err != nil {
		t.Fatal(err)
	}
	retainedJob := commitFenceAttemptsTestJob(t, harness, "retained-key")
	var retainedKey model.BackupAssetExportKey
	if err := harness.db.Where("job_id = ?", retainedJob.ID).Take(&retainedKey).Error; err != nil {
		t.Fatal(err)
	}
	if retainedKey.KEKVersion == lostKey.KEKVersion {
		t.Fatalf("retained Export job reused lost KEK version: lost=%d retained=%d", lostKey.KEKVersion, retainedKey.KEKVersion)
	}

	port := &keyLossLifecyclePortFake{db: harness.db, now: now}
	lifecycle, err := NewLifecycle(LifecycleDependencies{DB: harness.db, Port: port, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkKeyVersionLost(context.Background(), ring, lostKey.KEKVersion, 10); err != nil {
		t.Fatalf("MarkKeyVersionLost: %v", err)
	}

	if _, err := ring.ByVersion(context.Background(), backupasset.KeyDomainExportStore, lostKey.KEKVersion); !errors.Is(err, backupasset.ErrKeyLost) {
		t.Fatalf("lost Export KEK version remained usable: %v", err)
	}
	active, err := ring.Active(context.Background(), backupasset.KeyDomainExportStore)
	if err != nil || active.Version != retainedKey.KEKVersion {
		t.Fatalf("unaffected Export KEK active=%+v err=%v want version=%d", active, err, retainedKey.KEKVersion)
	}
	assertLifecycleJobState(t, harness, lostJob.ID, ExecutionFailed, CleanupPurged)
	var afterLostKey model.BackupAssetExportKey
	if err := harness.db.Where("id = ?", lostKey.ID).Take(&afterLostKey).Error; err != nil {
		t.Fatal(err)
	}
	if afterLostKey.State != "lost" || len(afterLostKey.WrappedDEK) != 0 || len(afterLostKey.EnvelopeNonce) != 0 || afterLostKey.DestroyedAt == nil {
		t.Fatalf("lost Export job key retained cryptographic material: %+v", afterLostKey)
	}
	var afterAttempt model.BackupAssetExportAttempt
	if err := harness.db.Where("id = ?", lostAttempt.ID).Take(&afterAttempt).Error; err != nil {
		t.Fatal(err)
	}
	if afterAttempt.State != string(AttemptFailed) || afterAttempt.IsCurrent {
		t.Fatalf("active Export attempt was not fenced for key loss: %+v", afterAttempt)
	}
	var afterRetainedKey model.BackupAssetExportKey
	if err := harness.db.Where("id = ?", retainedKey.ID).Take(&afterRetainedKey).Error; err != nil {
		t.Fatal(err)
	}
	if afterRetainedKey.State != "active" || len(afterRetainedKey.WrappedDEK) == 0 {
		t.Fatalf("unaffected Export job key changed during exact key loss: %+v", afterRetainedKey)
	}
	wantCalls := []string{
		"fence_attempts:" + lostJob.ID,
		"revoke_deliveries:" + lostJob.ID,
		"drain_streams:" + lostJob.ID,
		"destroy_key:" + lostJob.ID,
		"release_sources:" + lostJob.ID,
		"purge_ciphertext:" + lostJob.ID,
		"release_store:" + lostJob.ID,
	}
	if !reflect.DeepEqual(port.calls, wantCalls) {
		t.Fatalf("key-loss cleanup calls=%v want=%v", port.calls, wantCalls)
	}
}

func TestLifecycleMarksKeyVersionLostAfterSourceExpiredCrashBoundary(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	ring := backupasset.NewKeyring(harness.db, func() time.Time { return now })

	job, _, _ := claimFenceAttemptsTestJob(t, harness, "source-expired-key-loss")
	var key model.BackupAssetExportKey
	if err := harness.db.Where("job_id = ?", job.ID).Take(&key).Error; err != nil {
		t.Fatal(err)
	}
	if key.State != "active" || len(key.WrappedDEK) == 0 || len(key.EnvelopeNonce) == 0 {
		t.Fatalf("crash-boundary Export key must remain readable before resumed cleanup: %+v", key)
	}
	selectionBefore := loadPersistentLifecycleSelectionMetadata(t, harness.db, job.ID)
	for _, item := range selectionBefore {
		if len(item.PathNonce) == 0 || len(item.PathCiphertext) == 0 {
			t.Fatalf("crash-boundary selection was not readable before cleanup: %+v", item)
		}
	}

	crashErr := errors.New("crash after durable source-expired transition")
	beforeCrashPort := &lifecyclePortFake{failAt: "fence_attempts", failure: crashErr}
	beforeCrash, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: beforeCrashPort, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := beforeCrash.FailSourceExpired(context.Background(), job.ID); !errors.Is(err, crashErr) {
		t.Fatalf("FailSourceExpired crash boundary error=%v, want %v", err, crashErr)
	}
	assertLifecycleJobState(t, harness, job.ID, ExecutionSourceExpired, CleanupRevoking)

	port := &keyLossLifecyclePortFake{db: harness.db, now: now}
	lifecycle, err := NewLifecycle(LifecycleDependencies{DB: harness.db, Port: port, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkKeyVersionLost(context.Background(), ring, key.KEKVersion, 1); err != nil {
		t.Fatalf("MarkKeyVersionLost after source-expired crash boundary: %v", err)
	}

	assertLifecycleJobState(t, harness, job.ID, ExecutionSourceExpired, CleanupPurged)
	var afterJob model.BackupAssetExportJob
	if err := harness.db.Where("id = ?", job.ID).Take(&afterJob).Error; err != nil {
		t.Fatal(err)
	}
	if afterJob.ErrorCategory != "source_expired" {
		t.Fatalf("key-loss cleanup rewrote authoritative source-expired outcome: %+v", afterJob)
	}
	var afterKey model.BackupAssetExportKey
	if err := harness.db.Where("id = ?", key.ID).Take(&afterKey).Error; err != nil {
		t.Fatal(err)
	}
	if afterKey.State != "lost" || len(afterKey.WrappedDEK) != 0 || len(afterKey.EnvelopeNonce) != 0 || afterKey.DestroyedAt == nil {
		t.Fatalf("source-expired Export key was not cryptographically destroyed: %+v", afterKey)
	}
	if _, err := ring.ByVersion(context.Background(), backupasset.KeyDomainExportStore, key.KEKVersion); !errors.Is(err, backupasset.ErrKeyLost) {
		t.Fatalf("exact source-expired Export KEK version remained usable: %v", err)
	}
	selectionAfter := loadPersistentLifecycleSelectionMetadata(t, harness.db, job.ID)
	for _, item := range selectionAfter {
		if len(item.PathNonce) != 0 || len(item.PathCiphertext) != 0 {
			t.Fatalf("source-expired selection retained decryptable metadata: %+v", item)
		}
	}
	wantCalls := []string{
		"fence_attempts:" + job.ID,
		"revoke_deliveries:" + job.ID,
		"drain_streams:" + job.ID,
		"destroy_key:" + job.ID,
		"release_sources:" + job.ID,
		"purge_ciphertext:" + job.ID,
		"release_store:" + job.ID,
	}
	if !reflect.DeepEqual(port.calls, wantCalls) {
		t.Fatalf("source-expired key-loss cleanup calls=%v want=%v", port.calls, wantCalls)
	}
}

func TestLifecycleMarkKeyVersionLostResumesAfterPostDestructionReleaseFailure(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	ring := backupasset.NewKeyring(harness.db, func() time.Time { return now })

	job, _, _ := claimFenceAttemptsTestJob(t, harness, "post-destruction-release-failure")
	var key model.BackupAssetExportKey
	if err := harness.db.Where("job_id = ?", job.ID).Take(&key).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ring.Rotate(context.Background(), backupasset.KeyDomainExportStore, 0); err != nil {
		t.Fatal(err)
	}

	transitionErr := errors.New("crash after durable source-expired transition")
	beforeCrash, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db,
		Port: &lifecyclePortFake{
			failAt: "fence_attempts", failure: transitionErr,
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := beforeCrash.FailSourceExpired(context.Background(), job.ID); !errors.Is(err, transitionErr) {
		t.Fatalf("prepare source-expired cleanup boundary error=%v, want %v", err, transitionErr)
	}
	assertLifecycleJobState(t, harness, job.ID, ExecutionSourceExpired, CleanupRevoking)

	releaseErr := errors.New("source release unavailable after durable key destruction")
	port := &keyLossLifecyclePortFake{
		db: harness.db, now: now,
		releaseSourcesFailures: 1, releaseSourcesErr: releaseErr,
	}
	lifecycle, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: port, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkKeyVersionLost(context.Background(), ring, key.KEKVersion, 1); !errors.Is(err, releaseErr) {
		t.Fatalf("first key-loss attempt error=%v, want %v", err, releaseErr)
	}
	assertLifecycleJobState(t, harness, job.ID, ExecutionSourceExpired, CleanupRevoking)
	var afterFailureJob model.BackupAssetExportJob
	if err := harness.db.Where("id = ?", job.ID).Take(&afterFailureJob).Error; err != nil {
		t.Fatal(err)
	}
	if afterFailureJob.ErrorCategory != "source_expired" {
		t.Fatalf("post-destruction failure rewrote authoritative source-expired outcome: %+v", afterFailureJob)
	}
	var destroyedKey model.BackupAssetExportKey
	if err := harness.db.Where("id = ?", key.ID).Take(&destroyedKey).Error; err != nil {
		t.Fatal(err)
	}
	if destroyedKey.State != "destroyed" || len(destroyedKey.WrappedDEK) != 0 ||
		len(destroyedKey.EnvelopeNonce) != 0 || destroyedKey.DestroyedAt == nil {
		t.Fatalf("post-destruction failure did not retain a durable unreadable key tombstone: %+v", destroyedKey)
	}
	for _, item := range loadPersistentLifecycleSelectionMetadata(t, harness.db, job.ID) {
		if len(item.PathNonce) != 0 || len(item.PathCiphertext) != 0 {
			t.Fatalf("post-destruction failure retained decryptable selection metadata: %+v", item)
		}
	}
	wantFirstCalls := []string{
		"fence_attempts:" + job.ID,
		"revoke_deliveries:" + job.ID,
		"drain_streams:" + job.ID,
		"destroy_key:" + job.ID,
		"release_sources:" + job.ID,
	}
	if !reflect.DeepEqual(port.calls, wantFirstCalls) {
		t.Fatalf("first key-loss cleanup calls=%v want=%v", port.calls, wantFirstCalls)
	}

	if err := lifecycle.MarkKeyVersionLost(context.Background(), ring, key.KEKVersion, 1); err != nil {
		t.Fatalf("retry key loss after durable destruction: %v", err)
	}
	assertLifecycleJobState(t, harness, job.ID, ExecutionSourceExpired, CleanupPurged)
	var afterRetryJob model.BackupAssetExportJob
	if err := harness.db.Where("id = ?", job.ID).Take(&afterRetryJob).Error; err != nil {
		t.Fatal(err)
	}
	if afterRetryJob.ErrorCategory != "source_expired" ||
		afterRetryJob.TransitionRevision != afterFailureJob.TransitionRevision+2 {
		t.Fatalf("key-loss retry changed the authoritative outcome or scheduling revision: before=%+v after=%+v", afterFailureJob, afterRetryJob)
	}
	var lostKey model.BackupAssetExportKey
	if err := harness.db.Where("id = ?", key.ID).Take(&lostKey).Error; err != nil {
		t.Fatal(err)
	}
	if lostKey.State != "lost" || len(lostKey.WrappedDEK) != 0 || len(lostKey.EnvelopeNonce) != 0 || lostKey.DestroyedAt == nil {
		t.Fatalf("completed key-loss retry left an invalid key tombstone: %+v", lostKey)
	}
	if _, err := ring.ByVersion(context.Background(), backupasset.KeyDomainExportStore, key.KEKVersion); !errors.Is(err, backupasset.ErrKeyLost) {
		t.Fatalf("completed key-loss retry left the exact KEK version usable: %v", err)
	}
	wantCalls := append(append([]string{}, wantFirstCalls...),
		"fence_attempts:"+job.ID,
		"revoke_deliveries:"+job.ID,
		"drain_streams:"+job.ID,
		"destroy_key:"+job.ID,
		"release_sources:"+job.ID,
		"purge_ciphertext:"+job.ID,
		"release_store:"+job.ID,
	)
	if !reflect.DeepEqual(port.calls, wantCalls) {
		t.Fatalf("retried key-loss cleanup calls=%v want=%v", port.calls, wantCalls)
	}
}

func TestLifecyclePersistsRevokingAndNeverReleasesSourceWhenKeyDestructionFails(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	jobID := createLifecycleJob(t, harness, now, ExecutionCanceled, nil)
	destroyErr := errors.New("key store unavailable")
	firstPort := &lifecyclePortFake{failAt: "destroy_key", failure: destroyErr}
	first, err := NewLifecycle(LifecycleDependencies{DB: harness.db, Port: firstPort, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := first.Cleanup(context.Background(), jobID)
	if !errors.Is(err, destroyErr) || result != CleanupRevoking {
		t.Fatalf("cleanup result=%s err=%v", result, err)
	}
	if !reflect.DeepEqual(firstPort.calls, []string{
		"fence_attempts", "revoke_deliveries", "drain_streams", "destroy_key",
	}) {
		t.Fatalf("unsafe calls=%v", firstPort.calls)
	}
	assertLifecycleJobState(t, harness, jobID, ExecutionCanceled, CleanupRevoking)

	restartPort := &lifecyclePortFake{}
	restarted, err := NewLifecycle(LifecycleDependencies{DB: harness.db, Port: restartPort, Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	result, err = restarted.Cleanup(context.Background(), jobID)
	if err != nil || result != CleanupPurged {
		t.Fatalf("restart cleanup result=%s err=%v", result, err)
	}
	want := []string{
		"fence_attempts", "revoke_deliveries", "drain_streams", "destroy_key",
		"release_sources", "purge_ciphertext", "release_store",
	}
	if !reflect.DeepEqual(restartPort.calls, want) {
		t.Fatalf("restart calls=%v want=%v", restartPort.calls, want)
	}
}

func TestLifecycleRetainsStoreChargeAndResumesPurgeAfterUnlinkFailure(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	jobID := createLifecycleJob(t, harness, now, ExecutionSourceExpired, nil)
	firstPort := &lifecyclePortFake{failAt: "purge_ciphertext", failure: errors.New("unlink failed")}
	first, _ := NewLifecycle(LifecycleDependencies{DB: harness.db, Port: firstPort, Now: func() time.Time { return now }})
	result, err := first.Cleanup(context.Background(), jobID)
	if err == nil || result != CleanupPurgeFailed {
		t.Fatalf("cleanup result=%s err=%v", result, err)
	}
	for _, call := range firstPort.calls {
		if call == "release_store" {
			t.Fatal("store charge released before ciphertext absence was proven")
		}
	}
	assertLifecycleJobState(t, harness, jobID, ExecutionSourceExpired, CleanupPurgeFailed)

	restartPort := &lifecyclePortFake{}
	restarted, _ := NewLifecycle(LifecycleDependencies{DB: harness.db, Port: restartPort, Now: func() time.Time { return now.Add(time.Second) }})
	result, err = restarted.Cleanup(context.Background(), jobID)
	if err != nil || result != CleanupPurged {
		t.Fatalf("restart cleanup result=%s err=%v", result, err)
	}
	if !reflect.DeepEqual(restartPort.calls, []string{"purge_ciphertext", "release_store"}) {
		t.Fatalf("restart repeated an already durable phase: %v", restartPort.calls)
	}
}

func TestPersistentLifecyclePortPurgeFailureUsesBoundedDetachedSafePersistence(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	quota, err := NewQuotaService(
		fixture.harness.db,
		func() time.Time { return fixture.clock },
		fixture.harness.config.Quota,
	)
	if err != nil {
		t.Fatal(err)
	}
	port, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
		DB: fixture.harness.db, Delivery: &persistentLifecycleDeliveryFake{}, Sources: fixture.harness.lease,
		Quota: quota, Store: fixture.store, AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}

	type purgeFailureContextKey struct{}
	type persistenceContextObservation struct {
		hasDeadline bool
		remaining   time.Duration
		contextErr  error
	}
	marker := t.Name()
	var persistencePhase atomic.Bool
	var observedPersistence atomic.Bool
	observations := make(chan persistenceContextObservation, 1)
	const callbackName = "test:observe_export_purge_failure_persistence_context"
	if err := fixture.harness.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != "backup_asset_export_jobs" ||
			tx.Statement.Context.Value(purgeFailureContextKey{}) != marker ||
			!persistencePhase.Load() || !observedPersistence.CompareAndSwap(false, true) {
			return
		}
		deadline, hasDeadline := tx.Statement.Context.Deadline()
		observation := persistenceContextObservation{hasDeadline: hasDeadline, contextErr: tx.Statement.Context.Err()}
		if hasDeadline {
			observation.remaining = time.Until(deadline)
		}
		observations <- observation
		if !hasDeadline {
			_ = tx.AddError(fmt.Errorf("%w: unbounded detached purge-failure persistence", ErrUnavailable))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.harness.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove purge-failure persistence context callback: %v", err)
		}
	})

	rawCause := errors.New("private wrapped-DEK purge path failure")
	originalOpen := fixture.store.openStoreEntryDescriptor
	var targetOpens atomic.Int32
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), purgeFailureContextKey{}, marker))
	t.Cleanup(cancel)
	fixture.store.openStoreEntryDescriptor = func(directoryFD int, name string, how *unix.OpenHow) (int, error) {
		fd, openErr := originalOpen(directoryFD, name, how)
		if openErr == nil && name == fixture.locator && targetOpens.Add(1) == 2 {
			if closeErr := unix.Close(fd); closeErr != nil {
				return -1, closeErr
			}
			persistencePhase.Store(true)
			cancel()
			return -1, rawCause
		}
		return fd, openErr
	}
	t.Cleanup(func() { fixture.store.openStoreEntryDescriptor = originalOpen })

	finished := make(chan error, 1)
	go func() { finished <- port.PurgeCiphertext(ctx, fixture.jobID) }()
	var purgeErr error
	select {
	case purgeErr = <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("Export purge-failure persistence exceeded its detached completion bound")
	}
	fixture.store.openStoreEntryDescriptor = originalOpen
	if targetOpens.Load() < 2 || !persistencePhase.Load() {
		t.Fatalf("physical Export purge failure was not injected: target_opens=%d persistence_phase=%t",
			targetOpens.Load(), persistencePhase.Load())
	}
	if purgeErr == nil {
		t.Fatal("physical Export purge failure returned nil")
	}
	if errors.Is(purgeErr, rawCause) || strings.Contains(purgeErr.Error(), rawCause.Error()) {
		t.Errorf("Export purge failure exposed its private raw cause: %v", purgeErr)
	}
	if !errors.Is(purgeErr, ErrInvalidStore) && !errors.Is(purgeErr, ErrUnavailable) {
		t.Errorf("Export purge failure error=%v, want a closed Export classification", purgeErr)
	}

	select {
	case observation := <-observations:
		if !observation.hasDeadline {
			t.Error("Export purge-failure persistence detached without a deadline")
		}
		if observation.contextErr != nil {
			t.Errorf("Export purge-failure persistence inherited caller cancellation: %v", observation.contextErr)
		}
		if observation.hasDeadline && (observation.remaining <= 0 || observation.remaining > 6*time.Second) {
			t.Errorf("Export purge-failure persistence deadline remaining=%s, want a live finite five-second budget", observation.remaining)
		}
	default:
		t.Error("Export purge failure never reached durable failure persistence")
	}

	var artifact model.BackupAssetExportArtifact
	if err := fixture.harness.db.Where("id = ? AND job_id = ?", fixture.artifactID, fixture.jobID).
		Take(&artifact).Error; err != nil {
		t.Fatal(err)
	}
	if artifact.State != "purge_failed" || artifact.PurgeError != "purge_failed" {
		t.Errorf("Export purge failure artifact state=%s category=%q, want purge_failed/purge_failed",
			artifact.State, artifact.PurgeError)
	}
	var reservations []model.BackupAssetExportReservation
	if err := fixture.harness.db.Where("job_id = ? AND kind = ?", fixture.jobID, "store").
		Order("bucket_id ASC").Find(&reservations).Error; err != nil {
		t.Fatal(err)
	}
	if len(reservations) != 2 {
		t.Fatalf("Export purge failure store reservations=%d, want exact global/user pair", len(reservations))
	}
	for _, reservation := range reservations {
		if reservation.State != "purge_pending" || reservation.ReleasedAt != nil {
			t.Errorf("Export purge failure reservation id=%s state=%s released=%t, want purge_pending/false",
				reservation.ID, reservation.State, reservation.ReleasedAt != nil)
		}
	}
}

func TestPersistentLifecyclePurgeFailureMovesStoreReservationPendingThenRetriesExactRelease(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	clock := fixture.clock

	var artifact model.BackupAssetExportArtifact
	if err := fixture.harness.db.Where("id = ? AND job_id = ?", fixture.artifactID, fixture.jobID).Take(&artifact).Error; err != nil {
		t.Fatal(err)
	}
	if artifact.CiphertextSize <= 0 {
		t.Fatalf("sealed artifact ciphertext size=%d, want positive physical bytes", artifact.CiphertextSize)
	}
	var targetJob model.BackupAssetExportJob
	if err := fixture.harness.db.Where("id = ?", fixture.jobID).Take(&targetJob).Error; err != nil {
		t.Fatal(err)
	}
	var originalStoreRows []model.BackupAssetExportReservation
	if err := fixture.harness.db.Where("job_id = ? AND kind = ?", fixture.jobID, "store").
		Order("bucket_id ASC").Find(&originalStoreRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(originalStoreRows) != 2 {
		t.Fatalf("store reservations=%d, want global and user rows", len(originalStoreRows))
	}
	peakBytes := originalStoreRows[0].ReservedStoreBytes

	quota, err := NewQuotaService(fixture.harness.db, func() time.Time { return clock }, fixture.harness.config.Quota)
	if err != nil {
		t.Fatal(err)
	}

	targetBeforeSeed := loadPersistentStoreQuotaPair(t, fixture.harness.db, fixture.jobID, targetJob.OwnerUserID)
	unrelatedJobID, err := backupasset.NewOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	const unrelatedUserID uint = 777
	const unrelatedReservedStoreBytes int64 = 4096
	const unrelatedUsedStoreBytes int64 = 512
	if _, err := quota.ReserveJob(context.Background(), QuotaJobRequest{
		UserID: unrelatedUserID, JobID: unrelatedJobID, StoreBytes: unrelatedReservedStoreBytes,
		ExpiresAt: clock.Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed unrelated quota reservation: %v", err)
	}
	unrelatedBeforeSeed := loadPersistentStoreQuotaPair(t, fixture.harness.db, unrelatedJobID, unrelatedUserID)
	if unrelatedBeforeSeed.globalBucket.ID != targetBeforeSeed.globalBucket.ID {
		t.Fatalf("unrelated reservation did not share global bucket: target=%s unrelated=%s",
			targetBeforeSeed.globalBucket.ID, unrelatedBeforeSeed.globalBucket.ID)
	}
	result := fixture.harness.db.Model(&model.BackupAssetExportQuotaBucket{}).
		Where("id IN ?", []string{targetBeforeSeed.globalBucket.ID, unrelatedBeforeSeed.userBucket.ID}).
		Updates(map[string]any{"used_store_bytes": gorm.Expr("used_store_bytes + ?", unrelatedUsedStoreBytes)})
	if result.Error != nil {
		t.Fatalf("seed unrelated used store bytes: %v", result.Error)
	}
	if result.RowsAffected != 2 {
		t.Fatalf("seed unrelated used store rows=%d, want 2", result.RowsAffected)
	}

	persistentPort, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
		DB: fixture.harness.db, Delivery: &persistentLifecycleDeliveryFake{}, Sources: fixture.harness.lease,
		Quota: quota, Store: fixture.store, AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	var targetBeforePurge persistentStoreQuotaPair
	var targetReservationsBeforePurge []model.BackupAssetExportReservation
	var unrelatedBeforePurge persistentStoreQuotaPair
	snapshotTaken := false
	firstPort := &persistentLifecyclePurgeSnapshotPort{
		inner: persistentPort,
		beforePurge: func() {
			if snapshotTaken {
				t.Fatal("persistent lifecycle purge snapshot was taken twice")
			}
			targetBeforePurge = loadPersistentStoreQuotaPair(t, fixture.harness.db, fixture.jobID, targetJob.OwnerUserID)
			targetReservationsBeforePurge = loadPersistentLifecycleJobReservations(t, fixture.harness.db, fixture.jobID)
			unrelatedBeforePurge = loadPersistentStoreQuotaPair(t, fixture.harness.db, unrelatedJobID, unrelatedUserID)
			snapshotTaken = true
		},
	}
	firstLifecycle, err := NewLifecycle(LifecycleDependencies{DB: fixture.harness.db, Port: firstPort, Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}

	originalOpen := fixture.store.openStoreEntryDescriptor
	var targetOpens atomic.Int32
	fixture.store.openStoreEntryDescriptor = func(directoryFD int, name string, how *unix.OpenHow) (int, error) {
		fd, openErr := originalOpen(directoryFD, name, how)
		if openErr == nil && name == fixture.locator && targetOpens.Add(1) == 2 {
			if chmodErr := os.Chmod(fixture.store.root, 0o500); chmodErr != nil {
				_ = unix.Close(fd)
				return -1, chmodErr
			}
		}
		return fd, openErr
	}
	restoreRoot := func() {
		fixture.store.openStoreEntryDescriptor = originalOpen
		if chmodErr := os.Chmod(fixture.store.root, 0o700); chmodErr != nil {
			t.Fatal(chmodErr)
		}
	}
	t.Cleanup(restoreRoot)

	if err := firstLifecycle.FailUnpublishable(context.Background(), fixture.jobID, "internal_failure"); err == nil {
		t.Fatal("lifecycle cleanup succeeded despite injected unlink failure")
	}
	restoreRoot()
	if targetOpens.Load() < 2 {
		t.Fatalf("target store object opens=%d, want unlink path", targetOpens.Load())
	}
	if _, err := os.Lstat(filepath.Join(fixture.store.root, fixture.locator)); err != nil {
		t.Fatalf("artifact disappeared despite failed unlink: %v", err)
	}

	var failedArtifact model.BackupAssetExportArtifact
	if err := fixture.harness.db.Where("id = ?", fixture.artifactID).Take(&failedArtifact).Error; err != nil {
		t.Fatal(err)
	}
	if failedArtifact.State != "purge_failed" {
		t.Fatalf("artifact state=%s, want purge_failed", failedArtifact.State)
	}
	assertLifecycleJobState(t, fixture.harness, fixture.jobID, ExecutionFailed, CleanupPurgeFailed)
	if !snapshotTaken {
		t.Fatal("persistent lifecycle did not reach the purge snapshot")
	}
	targetAfterFailure := loadPersistentStoreQuotaPair(t, fixture.harness.db, fixture.jobID, targetJob.OwnerUserID)
	targetReservationsAfterFailure := loadPersistentLifecycleJobReservations(t, fixture.harness.db, fixture.jobID)
	unrelatedAfterFailure := loadPersistentStoreQuotaPair(t, fixture.harness.db, unrelatedJobID, unrelatedUserID)
	if targetAfterFailure.globalReservation.State != "purge_pending" {
		t.Fatalf("global store reservation state=%s, want purge_pending", targetAfterFailure.globalReservation.State)
	}
	if len(targetReservationsAfterFailure) != len(targetReservationsBeforePurge) {
		t.Fatalf("target reservation count changed during failed purge: before=%d after=%d",
			len(targetReservationsBeforePurge), len(targetReservationsAfterFailure))
	}
	targetReservationsAfterFailureByID := make(map[string]model.BackupAssetExportReservation, len(targetReservationsAfterFailure))
	for _, reservation := range targetReservationsAfterFailure {
		targetReservationsAfterFailureByID[reservation.ID] = reservation
	}
	targetStoreReservationIDs := map[string]struct{}{
		targetBeforePurge.globalReservation.ID: {},
		targetBeforePurge.userReservation.ID:   {},
	}
	storeReservationCount := 0
	for _, before := range targetReservationsBeforePurge {
		after, ok := targetReservationsAfterFailureByID[before.ID]
		if !ok {
			t.Fatalf("target reservation %s disappeared during failed purge", before.ID)
		}
		if before.Kind != "store" {
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("non-store target reservation changed during failed purge:\nbefore=%+v\nafter=%+v", before, after)
			}
			continue
		}
		storeReservationCount++
		if _, ok := targetStoreReservationIDs[before.ID]; !ok {
			t.Fatalf("unexpected target store reservation during failed purge: %+v", before)
		}
		want := before
		want.State = "purge_pending"
		want.UpdatedAt = after.UpdatedAt
		if !reflect.DeepEqual(after, want) {
			t.Fatalf("target store reservation failed-purge transition:\nbefore=%+v\nafter=%+v", before, after)
		}
	}
	if storeReservationCount != len(targetStoreReservationIDs) {
		t.Fatalf("target store reservations=%d, want exact global/user pair", storeReservationCount)
	}
	for _, pair := range []struct {
		name   string
		before model.BackupAssetExportReservation
		after  model.BackupAssetExportReservation
	}{
		{name: "global", before: targetBeforePurge.globalReservation, after: targetAfterFailure.globalReservation},
		{name: "user", before: targetBeforePurge.userReservation, after: targetAfterFailure.userReservation},
	} {
		if pair.before.State != "active" || pair.after.State != "purge_pending" || pair.after.ReleasedAt != nil ||
			pair.after.ReservedStoreBytes != pair.before.ReservedStoreBytes ||
			pair.after.ReservedCipherBytes != pair.before.ReservedCipherBytes {
			t.Fatalf("%s store reservation after failed purge: before=%+v after=%+v", pair.name, pair.before, pair.after)
		}
	}
	for _, pair := range []struct {
		name   string
		before model.BackupAssetExportQuotaBucket
		after  model.BackupAssetExportQuotaBucket
	}{
		{name: "global", before: targetBeforePurge.globalBucket, after: targetAfterFailure.globalBucket},
		{name: "user", before: targetBeforePurge.userBucket, after: targetAfterFailure.userBucket},
	} {
		if !reflect.DeepEqual(pair.after, pair.before) {
			t.Fatalf("%s quota bucket changed during failed purge:\nbefore=%+v\nafter=%+v", pair.name, pair.before, pair.after)
		}
	}
	if !reflect.DeepEqual(unrelatedAfterFailure, unrelatedBeforePurge) {
		t.Fatalf("failed purge changed unrelated quota pair:\nbefore=%+v\nafter=%+v", unrelatedBeforePurge, unrelatedAfterFailure)
	}

	restartQuota, err := NewQuotaService(fixture.harness.db, func() time.Time { return clock.Add(time.Second) }, fixture.harness.config.Quota)
	if err != nil {
		t.Fatal(err)
	}
	restartPort, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
		DB: fixture.harness.db, Delivery: &persistentLifecycleDeliveryFake{}, Sources: fixture.harness.lease,
		Quota: restartQuota, Store: fixture.store, AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return clock.Add(time.Second) },
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewLifecycle(LifecycleDependencies{DB: fixture.harness.db, Port: restartPort, Now: func() time.Time { return clock.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if state, cleanupErr := restarted.Cleanup(context.Background(), fixture.jobID); cleanupErr != nil || state != CleanupPurged {
		t.Fatalf("restart cleanup state=%s err=%v, want purged", state, cleanupErr)
	}
	if _, err := os.Lstat(filepath.Join(fixture.store.root, fixture.locator)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("artifact remained after retry: %v", err)
	}
	targetAfterRelease := loadPersistentStoreQuotaPair(t, fixture.harness.db, fixture.jobID, targetJob.OwnerUserID)
	unrelatedAfterRelease := loadPersistentStoreQuotaPair(t, fixture.harness.db, unrelatedJobID, unrelatedUserID)
	for _, pair := range []struct {
		name   string
		before model.BackupAssetExportReservation
		after  model.BackupAssetExportReservation
	}{
		{name: "global", before: targetBeforePurge.globalReservation, after: targetAfterRelease.globalReservation},
		{name: "user", before: targetBeforePurge.userReservation, after: targetAfterRelease.userReservation},
	} {
		if pair.after.State != "released" || pair.after.ReleasedAt == nil ||
			pair.after.ReservedStoreBytes != pair.before.ReservedStoreBytes ||
			pair.after.ReservedCipherBytes != pair.before.ReservedCipherBytes {
			t.Fatalf("%s store reservation after successful purge: before=%+v after=%+v", pair.name, pair.before, pair.after)
		}
	}
	for _, pair := range []struct {
		name   string
		before model.BackupAssetExportQuotaBucket
		after  model.BackupAssetExportQuotaBucket
	}{
		{name: "global", before: targetBeforePurge.globalBucket, after: targetAfterRelease.globalBucket},
		{name: "user", before: targetBeforePurge.userBucket, after: targetAfterRelease.userBucket},
	} {
		if pair.after.ActiveJobs != pair.before.ActiveJobs || pair.after.ActiveWorkers != pair.before.ActiveWorkers ||
			pair.after.ActiveReaders != pair.before.ActiveReaders ||
			pair.after.ReservedStoreBytes != pair.before.ReservedStoreBytes-peakBytes ||
			pair.after.UsedStoreBytes != pair.before.UsedStoreBytes-artifact.CiphertextSize ||
			pair.after.TransitionRevision != pair.before.TransitionRevision+1 {
			t.Fatalf("%s quota bucket after successful purge: before=%+v after=%+v peak=%d ciphertext=%d",
				pair.name, pair.before, pair.after, peakBytes, artifact.CiphertextSize)
		}
	}
	if !reflect.DeepEqual(unrelatedAfterRelease.userReservation, unrelatedBeforePurge.userReservation) ||
		!reflect.DeepEqual(unrelatedAfterRelease.userBucket, unrelatedBeforePurge.userBucket) {
		t.Fatalf("successful target purge changed unrelated user quota: before=%+v after=%+v",
			unrelatedBeforePurge, unrelatedAfterRelease)
	}

	beforeRetryTarget := targetAfterRelease
	beforeRetryUnrelated := unrelatedAfterRelease
	if err := restartPort.ReleaseStoreBytes(context.Background(), fixture.jobID); err != nil {
		t.Fatalf("idempotent store release after proof: %v", err)
	}
	afterRetryTarget := loadPersistentStoreQuotaPair(t, fixture.harness.db, fixture.jobID, targetJob.OwnerUserID)
	afterRetryUnrelated := loadPersistentStoreQuotaPair(t, fixture.harness.db, unrelatedJobID, unrelatedUserID)
	if !reflect.DeepEqual(afterRetryTarget, beforeRetryTarget) || !reflect.DeepEqual(afterRetryUnrelated, beforeRetryUnrelated) {
		t.Fatalf("idempotent store release changed quota rows:\ntarget before=%+v\ntarget after=%+v\nunrelated before=%+v\nunrelated after=%+v",
			beforeRetryTarget, afterRetryTarget, beforeRetryUnrelated, afterRetryUnrelated)
	}
}

func TestQuotaServiceReleaseStoreRejectsArtifactCiphertextSizeDriftFromLockedJob(t *testing.T) {
	fixture := createPersistentSealedFixture(t)

	var job model.BackupAssetExportJob
	if err := fixture.harness.db.Where("id = ?", fixture.jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var artifact model.BackupAssetExportArtifact
	if err := fixture.harness.db.Where("id = ? AND job_id = ?", fixture.artifactID, fixture.jobID).Take(&artifact).Error; err != nil {
		t.Fatal(err)
	}
	if artifact.CiphertextSize <= 1 || job.ArtifactBytes != artifact.CiphertextSize {
		t.Fatalf("sealed job/artifact ciphertext sizes job=%d artifact=%d, want matching size > 1", job.ArtifactBytes, artifact.CiphertextSize)
	}
	originalCiphertextBytes := artifact.CiphertextSize
	tamperedCiphertextBytes := originalCiphertextBytes - 1

	if err := fixture.store.Purge(fixture.locator); err != nil {
		t.Fatalf("physically purge sealed artifact: %v", err)
	}
	result := fixture.harness.db.Model(&model.BackupAssetExportArtifact{}).
		Where("id = ? AND job_id = ?", fixture.artifactID, fixture.jobID).
		Updates(map[string]any{
			"state":           "purged",
			"purged_at":       fixture.clock,
			"ciphertext_size": tamperedCiphertextBytes,
			"updated_at":      fixture.clock,
		})
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if result.RowsAffected != 1 {
		t.Fatalf("tampered purged artifacts=%d, want one", result.RowsAffected)
	}

	var storeRows []model.BackupAssetExportReservation
	if err := fixture.harness.db.Where("job_id = ? AND kind = ?", fixture.jobID, "store").
		Order("bucket_id ASC").Find(&storeRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(storeRows) != 2 || storeRows[0].ReservedStoreBytes <= 0 ||
		storeRows[0].ReservedStoreBytes != storeRows[1].ReservedStoreBytes {
		t.Fatalf("store reservations=%+v", storeRows)
	}
	peakStoreBytes := storeRows[0].ReservedStoreBytes
	quota, err := NewQuotaService(fixture.harness.db, func() time.Time { return fixture.clock }, fixture.harness.config.Quota)
	if err != nil {
		t.Fatal(err)
	}

	err = quota.ReleaseStore(context.Background(), QuotaReservation{
		GlobalStoreID: storeRows[0].ID,
		UserStoreID:   storeRows[1].ID,
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("release tampered ciphertext size error=%v, want ErrUnavailable", err)
	}
	assertPersistentLifecycleStoreAccounting(t, fixture.harness.db, fixture.jobID, "active", peakStoreBytes, peakStoreBytes, originalCiphertextBytes)
}

func TestPersistentLifecyclePortPurgeFailureRejectsReleasedStoreReservationPair(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	clock := fixture.clock
	if err := fixture.harness.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.BackupAssetExportArtifact{}).
			Where("id = ? AND job_id = ?", fixture.artifactID, fixture.jobID).
			Updates(map[string]any{"state": "purging", "updated_at": clock})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("mark fixture artifact purging")
		}
		result = tx.Model(&model.BackupAssetExportReservation{}).
			Where("job_id = ? AND kind = ?", fixture.jobID, "store").
			Updates(map[string]any{"state": "released", "released_at": clock, "updated_at": clock})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 2 {
			return errors.New("mark fixture store reservations released")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	quota, err := NewQuotaService(fixture.harness.db, func() time.Time { return clock }, fixture.harness.config.Quota)
	if err != nil {
		t.Fatal(err)
	}
	port, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
		DB: fixture.harness.db, Delivery: &persistentLifecycleDeliveryFake{}, Sources: fixture.harness.lease,
		Quota: quota, Store: fixture.store, AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := port.markPurgeFailure(context.Background(), fixture.jobID, clock); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("mark purge failure error=%v, want ErrUnavailable", err)
	}

	var artifact model.BackupAssetExportArtifact
	if err := fixture.harness.db.Where("id = ? AND job_id = ?", fixture.artifactID, fixture.jobID).Take(&artifact).Error; err != nil {
		t.Fatal(err)
	}
	if artifact.State != "purging" {
		t.Fatalf("artifact state=%s, want purging after rejected mismatch", artifact.State)
	}
	var reservations []model.BackupAssetExportReservation
	if err := fixture.harness.db.Where("job_id = ? AND kind = ?", fixture.jobID, "store").Find(&reservations).Error; err != nil {
		t.Fatal(err)
	}
	if len(reservations) != 2 {
		t.Fatalf("store reservations=%d, want two", len(reservations))
	}
	for _, reservation := range reservations {
		if reservation.State != "released" || reservation.ReleasedAt == nil {
			t.Fatalf("store reservation changed after rejected mismatch: %+v", reservation)
		}
	}
}

func TestLifecycleQuarantinesReleasedStorePairWithUnpurgedCiphertext(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	clock := fixture.clock
	setReleasedStorePairUnpurged(t, fixture, CleanupPurging, "purging")
	if _, err := os.Lstat(filepath.Join(fixture.store.root, fixture.locator)); err != nil {
		t.Fatalf("fixture ciphertext missing before quarantine: %v", err)
	}

	quota, err := NewQuotaService(fixture.harness.db, func() time.Time { return clock }, fixture.harness.config.Quota)
	if err != nil {
		t.Fatal(err)
	}
	port, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
		DB: fixture.harness.db, Delivery: &persistentLifecycleDeliveryFake{}, Sources: fixture.harness.lease,
		Quota: quota, Store: fixture.store, AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(LifecycleDependencies{DB: fixture.harness.db, Port: port, Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}

	state, cleanupErr := lifecycle.Cleanup(context.Background(), fixture.jobID)
	if state != CleanupPurging || !errors.Is(cleanupErr, ErrUnavailable) {
		t.Fatalf("quarantined cleanup state=%s err=%v, want purging with ErrUnavailable", state, cleanupErr)
	}
	assertReleasedStorePairQuarantined(t, fixture, CleanupPurging, "purging")

	processed, reconcileErr := lifecycle.Reconcile(context.Background(), 1)
	if processed != 0 || !errors.Is(reconcileErr, ErrUnavailable) {
		t.Fatalf("quarantined reconcile processed=%d err=%v, want zero with ErrUnavailable", processed, reconcileErr)
	}
	assertReleasedStorePairQuarantined(t, fixture, CleanupPurging, "purging")
}

func TestLifecycleReconcileQuarantinesReleasedStorePairBeforePurgeRetry(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	clock := fixture.clock
	setReleasedStorePairUnpurged(t, fixture, CleanupPurgeFailed, "purge_failed")

	quota, err := NewQuotaService(fixture.harness.db, func() time.Time { return clock }, fixture.harness.config.Quota)
	if err != nil {
		t.Fatal(err)
	}
	port, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
		DB: fixture.harness.db, Delivery: &persistentLifecycleDeliveryFake{}, Sources: fixture.harness.lease,
		Quota: quota, Store: fixture.store, AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(LifecycleDependencies{DB: fixture.harness.db, Port: port, Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}

	var beforeJob model.BackupAssetExportJob
	if err := fixture.harness.db.Where("id = ?", fixture.jobID).Take(&beforeJob).Error; err != nil {
		t.Fatal(err)
	}
	var beforeArtifact model.BackupAssetExportArtifact
	if err := fixture.harness.db.Where("id = ?", fixture.artifactID).Take(&beforeArtifact).Error; err != nil {
		t.Fatal(err)
	}

	processed, reconcileErr := lifecycle.Reconcile(context.Background(), 1)
	if processed != 0 || !errors.Is(reconcileErr, ErrUnavailable) {
		t.Fatalf("quarantined retry reconcile processed=%d err=%v, want zero with ErrUnavailable", processed, reconcileErr)
	}
	assertReleasedStorePairQuarantined(t, fixture, CleanupPurgeFailed, "purge_failed")

	var afterJob model.BackupAssetExportJob
	if err := fixture.harness.db.Where("id = ?", fixture.jobID).Take(&afterJob).Error; err != nil {
		t.Fatal(err)
	}
	if afterJob.TransitionRevision != beforeJob.TransitionRevision || !afterJob.UpdatedAt.Equal(beforeJob.UpdatedAt) {
		t.Fatalf("quarantined retry rewrote an authoritative failure record: before=%+v after=%+v", beforeJob, afterJob)
	}
	var afterArtifact model.BackupAssetExportArtifact
	if err := fixture.harness.db.Where("id = ?", fixture.artifactID).Take(&afterArtifact).Error; err != nil {
		t.Fatal(err)
	}
	if !afterArtifact.UpdatedAt.Equal(beforeArtifact.UpdatedAt) {
		t.Fatalf("quarantined retry changed artifact timestamp: before=%+v after=%+v", beforeArtifact, afterArtifact)
	}
}

func setReleasedStorePairUnpurged(
	t *testing.T,
	fixture persistentSealedFixture,
	cleanupState CleanupState,
	artifactState string,
) {
	t.Helper()
	clock := fixture.clock
	if err := fixture.harness.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.BackupAssetExportJob{}).Where("id = ?", fixture.jobID).
			Updates(map[string]any{
				"execution_state": string(ExecutionFailed),
				"cleanup_state":   string(cleanupState),
				"updated_at":      clock,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("mark fixture job cleanup state")
		}
		result = tx.Model(&model.BackupAssetExportArtifact{}).
			Where("id = ? AND job_id = ?", fixture.artifactID, fixture.jobID).
			Updates(map[string]any{"state": artifactState, "updated_at": clock})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("mark fixture artifact cleanup state")
		}
		result = tx.Model(&model.BackupAssetExportReservation{}).
			Where("job_id = ? AND kind = ?", fixture.jobID, "store").
			Updates(map[string]any{"state": "released", "released_at": clock, "updated_at": clock})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 2 {
			return errors.New("mark fixture store reservations released")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertReleasedStorePairQuarantined(
	t *testing.T,
	fixture persistentSealedFixture,
	wantCleanupState CleanupState,
	wantArtifactState string,
) {
	t.Helper()
	var job model.BackupAssetExportJob
	if err := fixture.harness.db.Where("id = ?", fixture.jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(ExecutionFailed) || job.CleanupState != string(wantCleanupState) {
		t.Fatalf("quarantined job=%+v, want failed/%s", job, wantCleanupState)
	}
	var artifact model.BackupAssetExportArtifact
	if err := fixture.harness.db.Where("id = ? AND job_id = ?", fixture.artifactID, fixture.jobID).Take(&artifact).Error; err != nil {
		t.Fatal(err)
	}
	if artifact.State != wantArtifactState || artifact.PurgedAt != nil {
		t.Fatalf("quarantined artifact=%+v, want unpurged %s", artifact, wantArtifactState)
	}
	var reservations []model.BackupAssetExportReservation
	if err := fixture.harness.db.Where("job_id = ? AND kind = ?", fixture.jobID, "store").
		Order("bucket_id ASC").Find(&reservations).Error; err != nil {
		t.Fatal(err)
	}
	if len(reservations) != 2 {
		t.Fatalf("quarantined store reservations=%d, want two", len(reservations))
	}
	for _, reservation := range reservations {
		if reservation.State != "released" || reservation.ReleasedAt == nil {
			t.Fatalf("quarantined store reservation changed: %+v", reservation)
		}
	}
	if _, err := os.Lstat(filepath.Join(fixture.store.root, fixture.locator)); err != nil {
		t.Fatalf("quarantined ciphertext was touched: %v", err)
	}
}

func assertPersistentLifecycleStoreAccounting(
	t *testing.T,
	db *gorm.DB,
	jobID, wantState string,
	wantReservationStore, wantReserved, wantUsed int64,
) {
	t.Helper()
	var job model.BackupAssetExportJob
	if err := db.Where("id = ?", jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	pair := loadPersistentStoreQuotaPair(t, db, jobID, job.OwnerUserID)
	for _, reservation := range []model.BackupAssetExportReservation{pair.globalReservation, pair.userReservation} {
		if reservation.State != wantState {
			t.Fatalf("store reservation %s state=%s, want %s", reservation.ID, reservation.State, wantState)
		}
		if reservation.ReservedStoreBytes != wantReservationStore || reservation.ReservedCipherBytes != 0 {
			t.Fatalf("store reservation %s store=%d cipher=%d, want store=%d cipher=0", reservation.ID,
				reservation.ReservedStoreBytes, reservation.ReservedCipherBytes, wantReservationStore)
		}
	}
	for _, bucket := range []model.BackupAssetExportQuotaBucket{pair.globalBucket, pair.userBucket} {
		if bucket.ReservedStoreBytes != wantReserved || bucket.UsedStoreBytes != wantUsed {
			t.Fatalf("quota bucket %s/%s reserved=%d used=%d, want reserved=%d used=%d",
				bucket.Scope, bucket.Subject, bucket.ReservedStoreBytes, bucket.UsedStoreBytes, wantReserved, wantUsed)
		}
	}
}

func loadPersistentLifecycleJobReservations(
	t *testing.T,
	db *gorm.DB,
	jobID string,
) []model.BackupAssetExportReservation {
	t.Helper()
	var reservations []model.BackupAssetExportReservation
	if err := db.Where("job_id = ?", jobID).Order("kind ASC, bucket_id ASC, id ASC").Find(&reservations).Error; err != nil {
		t.Fatal(err)
	}
	return reservations
}

type persistentLifecyclePurgeSnapshotPort struct {
	inner       *PersistentLifecyclePort
	beforePurge func()
}

func (port *persistentLifecyclePurgeSnapshotPort) FenceAttempts(ctx context.Context, jobID string) error {
	return port.inner.FenceAttempts(ctx, jobID)
}

func (port *persistentLifecyclePurgeSnapshotPort) RevokeDeliveries(ctx context.Context, jobID string) error {
	return port.inner.RevokeDeliveries(ctx, jobID)
}

func (port *persistentLifecyclePurgeSnapshotPort) DrainStreams(ctx context.Context, jobID string) error {
	return port.inner.DrainStreams(ctx, jobID)
}

func (port *persistentLifecyclePurgeSnapshotPort) DestroyJobKeyAndSelection(ctx context.Context, jobID string) error {
	return port.inner.DestroyJobKeyAndSelection(ctx, jobID)
}

func (port *persistentLifecyclePurgeSnapshotPort) ReleaseSourcesAndNonStore(ctx context.Context, jobID string) error {
	return port.inner.ReleaseSourcesAndNonStore(ctx, jobID)
}

func (port *persistentLifecyclePurgeSnapshotPort) PurgeCiphertext(ctx context.Context, jobID string) error {
	if port.beforePurge != nil {
		port.beforePurge()
	}
	return port.inner.PurgeCiphertext(ctx, jobID)
}

func (port *persistentLifecyclePurgeSnapshotPort) ReleaseStoreBytes(ctx context.Context, jobID string) error {
	return port.inner.ReleaseStoreBytes(ctx, jobID)
}

func chargeLifecycleStoreBytes(t *testing.T, db *gorm.DB, jobID string, ciphertextBytes int64) {
	t.Helper()
	if ciphertextBytes < 0 {
		t.Fatalf("ciphertext bytes=%d", ciphertextBytes)
	}
	var reservations []model.BackupAssetExportReservation
	if err := db.Where("job_id = ? AND kind = ?", jobID, "store").Order("bucket_id ASC").Find(&reservations).Error; err != nil {
		t.Fatal(err)
	}
	if len(reservations) != 2 {
		t.Fatalf("store reservations=%d, want global and user rows", len(reservations))
	}
	bucketIDs := []string{reservations[0].BucketID, reservations[1].BucketID}
	result := db.Model(&model.BackupAssetExportQuotaBucket{}).Where("id IN ?", bucketIDs).
		Updates(map[string]any{"used_store_bytes": gorm.Expr("used_store_bytes + ?", ciphertextBytes)})
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if result.RowsAffected != int64(len(bucketIDs)) {
		t.Fatalf("charged quota buckets=%d, want %d", result.RowsAffected, len(bucketIDs))
	}
}

func TestLifecycleTerminalizeForRuntimeStopTransitionsActiveJobsBeforeCleanup(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	jobs := make(map[string]ExecutionState)
	for _, state := range []ExecutionState{
		ExecutionQueued,
		ExecutionRunning,
		ExecutionRetryWait,
		ExecutionSealing,
		ExecutionReady,
	} {
		var expiresAt *time.Time
		if state == ExecutionReady {
			value := now.Add(time.Hour)
			expiresAt = &value
		}
		jobs[createLifecycleJob(t, harness, now, state, expiresAt)] = state
	}
	port := &lifecyclePortFake{
		beforeCall: func(name, jobID string) error {
			if name != "fence_attempts" {
				return nil
			}
			var job model.BackupAssetExportJob
			if err := harness.db.Select("execution_state").Where("id = ?", jobID).Take(&job).Error; err != nil {
				return err
			}
			if job.ExecutionState != string(ExecutionCancelRequested) {
				return fmt.Errorf("cleanup started from execution_state=%s", job.ExecutionState)
			}
			return nil
		},
	}
	lifecycle, err := NewLifecycle(LifecycleDependencies{DB: harness.db, Port: port, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	processed, err := lifecycle.TerminalizeForRuntimeStop(context.Background(), len(jobs))
	if err != nil || processed != len(jobs) {
		t.Fatalf("terminalize processed=%d err=%v want=%d", processed, err, len(jobs))
	}
	for jobID, state := range jobs {
		assertLifecycleJobState(t, harness, jobID, ExecutionCanceled, CleanupPurged)
		if state == ExecutionReady {
			var job model.BackupAssetExportJob
			if err := harness.db.Where("id = ?", jobID).Take(&job).Error; err != nil {
				t.Fatal(err)
			}
			if job.ReadyAt == nil || job.ExpiresAt == nil {
				t.Fatalf("ready job lost its frozen lifetime while terminalizing: %+v", job)
			}
		}
	}
}

func TestLifecycleTerminalizeForRuntimeStopRetainsStoreChargeAfterPurgeFailure(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	jobID := createLifecycleJob(t, harness, now, ExecutionQueued, nil)
	purgeErr := errors.New("ciphertext purge unavailable")
	port := &lifecyclePortFake{failAt: "purge_ciphertext", failure: purgeErr}
	lifecycle, err := NewLifecycle(LifecycleDependencies{DB: harness.db, Port: port, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	processed, err := lifecycle.TerminalizeForRuntimeStop(context.Background(), 1)
	if !errors.Is(err, purgeErr) || processed != 0 {
		t.Fatalf("terminalize processed=%d err=%v want purge failure", processed, err)
	}
	assertLifecycleJobState(t, harness, jobID, ExecutionCanceled, CleanupPurgeFailed)
	for _, call := range port.calls {
		if call == "release_store" {
			t.Fatal("terminalization released store quota despite unproven physical purge")
		}
	}
}

func TestLifecycleTerminalizeForRuntimeStopPersistsFairProgressAcrossCleanupFenceFailures(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	const limit = 1
	blockedErr := errors.New("persistent runtime-stop fence failure")
	blocked := make(map[string]model.BackupAssetExportJob, lifecycleReconcileScanScale+1)
	for index := 0; index < lifecycleReconcileScanScale+1; index++ {
		updatedAt := now.Add(-4*time.Hour + time.Duration(index)*time.Minute)
		jobID := createLifecycleJob(t, harness, updatedAt, ExecutionFailed, nil)
		if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
			Updates(map[string]any{
				"cleanup_state": string(CleanupRevoking),
				"updated_at":    updatedAt,
			}).Error; err != nil {
			t.Fatal(err)
		}
		var job model.BackupAssetExportJob
		if err := harness.db.Where("id = ?", jobID).Take(&job).Error; err != nil {
			t.Fatal(err)
		}
		blocked[jobID] = job
	}
	activeID := createLifecycleJob(t, harness, now, ExecutionRunning, nil)
	port := &lifecyclePortFake{beforeCall: func(name, jobID string) error {
		if name == "fence_attempts" {
			if _, exists := blocked[jobID]; exists {
				return blockedErr
			}
		}
		return nil
	}}
	newLifecycle := func() *Lifecycle {
		t.Helper()
		lifecycle, err := NewLifecycle(LifecycleDependencies{
			DB: harness.db, Port: port, Now: func() time.Time { return now },
		})
		if err != nil {
			t.Fatal(err)
		}
		return lifecycle
	}

	processed, terminalizeErr := newLifecycle().TerminalizeForRuntimeStop(context.Background(), limit)
	if processed != 0 || !errors.Is(terminalizeErr, blockedErr) {
		t.Fatalf("first terminalize processed=%d err=%v, want bounded persistent failures", processed, terminalizeErr)
	}

	processed, terminalizeErr = newLifecycle().TerminalizeForRuntimeStop(context.Background(), limit)
	if processed != 1 || !errors.Is(terminalizeErr, blockedErr) {
		t.Fatalf("restarted terminalize processed=%d err=%v, want later active job plus persistent failure", processed, terminalizeErr)
	}
	assertLifecycleJobState(t, harness, activeID, ExecutionCanceled, CleanupPurged)

	for jobID, before := range blocked {
		var after model.BackupAssetExportJob
		if err := harness.db.Where("id = ?", jobID).Take(&after).Error; err != nil {
			t.Fatal(err)
		}
		if after.CleanupState != before.CleanupState ||
			after.TransitionRevision != before.TransitionRevision ||
			!after.UpdatedAt.Equal(before.UpdatedAt) {
			t.Fatalf("persistent fence failure was rewritten to rotate it: before=%+v after=%+v", before, after)
		}
	}

	var bucket model.BackupAssetExportQuotaBucket
	if err := harness.db.Where("scope = ? AND subject = ?", "global", "global").Take(&bucket).Error; err != nil {
		t.Fatal(err)
	}
	if bucket.LifecycleSweepCursor != bucket.LifecycleSweepHighWater || bucket.LifecycleSweepLeaseExpiresAt != nil {
		t.Fatalf("runtime-stop sweep did not durably finish its finite window: %+v", bucket)
	}
}

func TestLifecycleTerminalizeForRuntimeStopPassCompletesAtExactHighWater(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	blockedErr := errors.New("persistent exact-window fence failure")
	for index := 0; index < lifecycleReconcileScanScale; index++ {
		jobID := createLifecycleJob(t, harness, now.Add(time.Duration(index)*time.Minute), ExecutionFailed, nil)
		if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
			Updates(map[string]any{"cleanup_state": string(CleanupRevoking)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	port := &lifecyclePortFake{failAt: "fence_attempts", failure: blockedErr}
	lifecycle, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: port, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	progress, terminalizeErr := lifecycle.TerminalizeForRuntimeStopPass(context.Background(), 1)
	if !errors.Is(terminalizeErr, blockedErr) || progress.Processed != 0 ||
		progress.Advanced != lifecycleReconcileScanScale || !progress.Complete {
		t.Fatalf("exact high-water terminalization progress=%+v err=%v, want a completed finite failure window", progress, terminalizeErr)
	}
}

func TestLifecycleTerminalizeForRuntimeStopPassReturnsRetryableContention(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	jobID := createLifecycleJob(t, harness, now.Add(-time.Minute), ExecutionRunning, nil)
	holder, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: &lifecyclePortFake{}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	heldSweep, acquired, err := holder.acquireLifecycleSweep(context.Background(), now)
	if err != nil || !acquired {
		t.Fatalf("hold lifecycle sweep acquired=%v err=%v", acquired, err)
	}
	t.Cleanup(func() {
		if err := holder.persistLifecycleSweepProgress(context.Background(), &heldSweep, heldSweep.cursor, true); err != nil {
			t.Errorf("release held lifecycle sweep: %v", err)
		}
	})
	var before model.BackupAssetExportQuotaBucket
	if err := harness.db.Where("id = ?", heldSweep.bucketID).Take(&before).Error; err != nil {
		t.Fatal(err)
	}

	contender, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: &lifecyclePortFake{}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	progress, terminalizeErr := contender.TerminalizeForRuntimeStopPass(context.Background(), 1)
	if !errors.Is(terminalizeErr, ErrUnavailable) || progress != (RuntimeStopTerminalizationProgress{}) {
		t.Fatalf("contended runtime-stop progress=%+v err=%v, want bounded retryable unavailability", progress, terminalizeErr)
	}
	assertLifecycleJobState(t, harness, jobID, ExecutionRunning, CleanupNone)

	var after model.BackupAssetExportQuotaBucket
	if err := harness.db.Where("id = ?", heldSweep.bucketID).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.LifecycleSweepCursor != before.LifecycleSweepCursor ||
		after.LifecycleSweepHighWater != before.LifecycleSweepHighWater ||
		after.LifecycleSweepRevision != before.LifecycleSweepRevision ||
		after.LifecycleSweepLeaseExpiresAt == nil || before.LifecycleSweepLeaseExpiresAt == nil ||
		!after.LifecycleSweepLeaseExpiresAt.Equal(*before.LifecycleSweepLeaseExpiresAt) ||
		after.TransitionRevision != before.TransitionRevision || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("runtime-stop contention mutated the persisted sweep or quota metadata: before=%+v after=%+v", before, after)
	}
}

func TestLifecycleTerminalizeForRuntimeStopPassFinalizesPurgedCrashBoundaries(t *testing.T) {
	for _, test := range []struct {
		name      string
		from      ExecutionState
		want      ExecutionState
		expiresAt func(time.Time) *time.Time
	}{
		{name: "cancel requested", from: ExecutionCancelRequested, want: ExecutionCanceled},
		{name: "expiring", from: ExecutionExpiring, want: ExecutionExpired, expiresAt: func(now time.Time) *time.Time {
			expiresAt := now.Add(-time.Minute)
			return &expiresAt
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newServiceHarness(t)
			now := time.Now().UTC().Truncate(time.Second)
			var expiresAt *time.Time
			if test.expiresAt != nil {
				expiresAt = test.expiresAt(now)
			}
			jobID := createLifecycleJob(t, harness, now.Add(-time.Hour), test.from, expiresAt)
			port := &lifecyclePortFake{}
			cleaner, err := NewLifecycle(LifecycleDependencies{
				DB: harness.db, Port: port, Now: func() time.Time { return now.Add(-time.Second) },
			})
			if err != nil {
				t.Fatal(err)
			}
			if state, err := cleaner.Cleanup(context.Background(), jobID); err != nil || state != CleanupPurged {
				t.Fatalf("prepare runtime-stop crash boundary state=%s err=%v", state, err)
			}
			var before model.BackupAssetExportJob
			if err := harness.db.Where("id = ?", jobID).Take(&before).Error; err != nil {
				t.Fatal(err)
			}

			port.calls = nil
			terminalizer, err := NewLifecycle(LifecycleDependencies{
				DB: harness.db, Port: port, Now: func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}
			progress, terminalizeErr := terminalizer.TerminalizeForRuntimeStopPass(context.Background(), 1)
			wantProgress := RuntimeStopTerminalizationProgress{Processed: 1, Advanced: 1, Complete: true}
			if terminalizeErr != nil || progress != wantProgress {
				t.Fatalf("purged crash-boundary progress=%+v err=%v, want %+v", progress, terminalizeErr, wantProgress)
			}
			var after model.BackupAssetExportJob
			if err := harness.db.Where("id = ?", jobID).Take(&after).Error; err != nil {
				t.Fatal(err)
			}
			if after.ExecutionState != string(test.want) || after.CleanupState != string(CleanupPurged) ||
				after.TransitionRevision != before.TransitionRevision+1 {
				t.Fatalf("purged crash-boundary before=%+v after=%+v want execution=%s and one CAS revision", before, after, test.want)
			}
			if len(port.calls) != 0 {
				t.Fatalf("purged crash-boundary repeated cleanup IO: %v", port.calls)
			}
		})
	}
}

func TestLifecycleTerminalizeForRuntimeStopPassRestartsForEarlierPurgedCrashBoundary(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	earlierID := createLifecycleJob(t, harness, now.Add(-3*time.Hour), ExecutionRunning, nil)
	_ = createLifecycleJob(t, harness, now.Add(-2*time.Hour), ExecutionFailed, nil)
	laterID := createLifecycleJob(t, harness, now.Add(-time.Hour), ExecutionFailed, nil)
	port := &lifecyclePortFake{}
	reconciler, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: port, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	processed, reconcileErr := reconciler.Reconcile(context.Background(), 1)
	if reconcileErr != nil || processed != 1 {
		t.Fatalf("prepare partial lifecycle sweep processed=%d err=%v", processed, reconcileErr)
	}
	var partialSweep model.BackupAssetExportQuotaBucket
	if err := harness.db.Where("scope = ? AND subject = ?", "global", "global").Take(&partialSweep).Error; err != nil {
		t.Fatal(err)
	}
	if partialSweep.LifecycleSweepCursor <= 0 || partialSweep.LifecycleSweepCursor >= partialSweep.LifecycleSweepHighWater {
		t.Fatalf("expected a persisted partial sweep before crash-boundary terminalization: %+v", partialSweep)
	}
	if err := reconciler.requestRuntimeStop(context.Background(), earlierID); err != nil {
		t.Fatalf("request earlier runtime stop: %v", err)
	}
	if state, err := reconciler.Cleanup(context.Background(), earlierID); err != nil || state != CleanupPurged {
		t.Fatalf("prepare earlier purged crash boundary state=%s err=%v", state, err)
	}
	var before model.BackupAssetExportJob
	if err := harness.db.Where("id = ?", earlierID).Take(&before).Error; err != nil {
		t.Fatal(err)
	}

	callsByJob := make(map[string]int)
	port.calls = nil
	port.beforeCall = func(_ string, jobID string) error {
		callsByJob[jobID]++
		return nil
	}
	terminalizer, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: port, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	progress, terminalizeErr := terminalizer.TerminalizeForRuntimeStopPass(context.Background(), 10)
	wantProgress := RuntimeStopTerminalizationProgress{Processed: 2, Advanced: 2, Complete: true}
	if terminalizeErr != nil || progress != wantProgress {
		t.Fatalf("earlier purged crash-boundary progress=%+v err=%v, want %+v", progress, terminalizeErr, wantProgress)
	}
	assertLifecycleJobState(t, harness, earlierID, ExecutionCanceled, CleanupPurged)
	assertLifecycleJobState(t, harness, laterID, ExecutionFailed, CleanupPurged)
	var after model.BackupAssetExportJob
	if err := harness.db.Where("id = ?", earlierID).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.TransitionRevision != before.TransitionRevision+1 {
		t.Fatalf("earlier purged crash-boundary transition revision before=%d after=%d", before.TransitionRevision, after.TransitionRevision)
	}
	if callsByJob[earlierID] != 0 || callsByJob[laterID] != 7 {
		t.Fatalf("runtime-stop cleanup calls by job=%v, want no repeated IO for purged row and one ordered later cleanup", callsByJob)
	}
}

func TestLifecycleTerminalizeForRuntimeStopRestartsPartialSweepForEarlierActiveJob(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	activeID := createLifecycleJob(t, harness, now.Add(-3*time.Hour), ExecutionRunning, nil)
	createLifecycleJob(t, harness, now.Add(-2*time.Hour), ExecutionFailed, nil)
	createLifecycleJob(t, harness, now.Add(-time.Hour), ExecutionFailed, nil)
	port := &lifecyclePortFake{}
	lifecycle, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: port, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	processed, reconcileErr := lifecycle.Reconcile(context.Background(), 1)
	if reconcileErr != nil || processed != 1 {
		t.Fatalf("partial reconcile processed=%d err=%v", processed, reconcileErr)
	}
	var partialSweep model.BackupAssetExportQuotaBucket
	if err := harness.db.Where("scope = ? AND subject = ?", "global", "global").Take(&partialSweep).Error; err != nil {
		t.Fatal(err)
	}
	if partialSweep.LifecycleSweepCursor >= partialSweep.LifecycleSweepHighWater {
		t.Fatalf("expected partial lifecycle sweep before runtime stop: %+v", partialSweep)
	}

	terminalizer, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: port, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	processed, terminalizeErr := terminalizer.TerminalizeForRuntimeStop(context.Background(), 1)
	if terminalizeErr != nil || processed != 1 {
		t.Fatalf("terminalize processed=%d err=%v", processed, terminalizeErr)
	}
	assertLifecycleJobState(t, harness, activeID, ExecutionCanceled, CleanupPurged)
	var afterTerminalization model.BackupAssetExportQuotaBucket
	if err := harness.db.Where("id = ?", partialSweep.ID).Take(&afterTerminalization).Error; err != nil {
		t.Fatal(err)
	}
	if afterTerminalization.TransitionRevision != partialSweep.TransitionRevision ||
		!afterTerminalization.UpdatedAt.Equal(partialSweep.UpdatedAt) {
		t.Fatalf("runtime-stop scheduler rewrote quota transition metadata: before=%+v after=%+v", partialSweep, afterTerminalization)
	}
}

func TestLifecycleTerminalizeForRuntimeStopReportsEarlierTerminalCleanupBehindOrdinaryCursor(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)
	const (
		limit        = 1
		blockedCount = 32
	)
	blockedErr := errors.New("persistent cleanup fence failure behind ordinary cursor")
	blocked := make(map[string]struct{}, blockedCount)
	for index := 0; index < blockedCount; index++ {
		jobID := createLifecycleJob(t, harness, now.Add(-4*time.Hour+time.Duration(index)*time.Minute), ExecutionFailed, nil)
		if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
			UpdateColumns(map[string]any{"cleanup_state": string(CleanupRevoking)}).Error; err != nil {
			t.Fatal(err)
		}
		blocked[jobID] = struct{}{}
	}
	laterID := createLifecycleJob(t, harness, now, ExecutionRunning, nil)
	port := &lifecyclePortFake{beforeCall: func(name, jobID string) error {
		if name == "fence_attempts" {
			if _, exists := blocked[jobID]; exists {
				return blockedErr
			}
		}
		return nil
	}}
	newLifecycle := func() *Lifecycle {
		t.Helper()
		lifecycle, err := NewLifecycle(LifecycleDependencies{
			DB: harness.db, Port: port, Now: func() time.Time { return now },
		})
		if err != nil {
			t.Fatal(err)
		}
		return lifecycle
	}

	processed, reconcileErr := newLifecycle().Reconcile(context.Background(), limit)
	if processed != 0 || !errors.Is(reconcileErr, blockedErr) {
		t.Fatalf("ordinary reconcile processed=%d err=%v, want bounded persistent cleanup failures", processed, reconcileErr)
	}
	var ordinarySweep model.BackupAssetExportQuotaBucket
	if err := harness.db.Where("scope = ? AND subject = ?", "global", "global").Take(&ordinarySweep).Error; err != nil {
		t.Fatal(err)
	}
	if ordinarySweep.LifecycleSweepCursor != blockedCount ||
		ordinarySweep.LifecycleSweepHighWater != blockedCount+1 || ordinarySweep.LifecycleSweepLeaseExpiresAt != nil {
		t.Fatalf("ordinary reconcile did not durably advance beyond terminal cleanup failures: %+v", ordinarySweep)
	}
	var earlier model.BackupAssetExportJob
	if err := harness.db.Where("lifecycle_enqueue_sequence <= ? AND cleanup_state = ?", ordinarySweep.LifecycleSweepCursor, CleanupRevoking).
		Order("lifecycle_enqueue_sequence ASC").Take(&earlier).Error; err != nil {
		t.Fatal(err)
	}
	if earlier.ExecutionState != string(ExecutionFailed) {
		t.Fatalf("earlier cursor-skipped job execution=%s, want failed", earlier.ExecutionState)
	}

	progress, terminalizeErr := newLifecycle().TerminalizeForRuntimeStopPass(context.Background(), limit)
	if !progress.Complete || progress.Processed != 1 || progress.Advanced != 1 || terminalizeErr == nil {
		t.Fatalf("runtime-stop completed successfully despite earlier terminal cleanup: progress=%+v err=%v", progress, terminalizeErr)
	}
	assertLifecycleJobState(t, harness, laterID, ExecutionCanceled, CleanupPurged)
	assertLifecycleJobState(t, harness, earlier.ID, ExecutionFailed, CleanupRevoking)
}

func TestLifecycleReconcileAdvancesPastBoundedPersistentFailures(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	const limit = 2
	blockedErr := errors.New("persistent cleanup failure")
	blocked := make(map[string]model.BackupAssetExportJob, limit)
	for index := 0; index < limit; index++ {
		updatedAt := now.Add(-4*time.Hour + time.Duration(index)*time.Minute)
		jobID := createLifecycleJob(t, harness, updatedAt, ExecutionFailed, nil)
		if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
			Updates(map[string]any{
				"cleanup_state": string(CleanupRevoking),
				"updated_at":    updatedAt,
			}).Error; err != nil {
			t.Fatal(err)
		}
		var job model.BackupAssetExportJob
		if err := harness.db.Where("id = ?", jobID).Take(&job).Error; err != nil {
			t.Fatal(err)
		}
		blocked[jobID] = job
	}

	actionableID := createLifecycleJob(t, harness, now, ExecutionFailed, nil)
	port := &lifecyclePortFake{beforeCall: func(name, jobID string) error {
		if name == "fence_attempts" {
			if _, exists := blocked[jobID]; exists {
				return blockedErr
			}
		}
		return nil
	}}
	lifecycle, err := NewLifecycle(LifecycleDependencies{DB: harness.db, Port: port, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	processed, reconcileErr := lifecycle.Reconcile(context.Background(), limit)
	if processed != 1 || !errors.Is(reconcileErr, blockedErr) {
		t.Fatalf("reconcile processed=%d err=%v, want one later job plus persistent errors", processed, reconcileErr)
	}
	assertLifecycleJobState(t, harness, actionableID, ExecutionFailed, CleanupPurged)
	for jobID, before := range blocked {
		var after model.BackupAssetExportJob
		if err := harness.db.Where("id = ?", jobID).Take(&after).Error; err != nil {
			t.Fatal(err)
		}
		if after.CleanupState != string(CleanupRevoking) ||
			after.TransitionRevision != before.TransitionRevision || !after.UpdatedAt.Equal(before.UpdatedAt) {
			t.Fatalf("persistent failure did not preserve its authoritative state, revision, and timestamp: before=%+v after=%+v", before, after)
		}
	}
	if len(port.calls) > limit+7 {
		t.Fatalf("bounded reconcile made %d lifecycle calls, want at most %d", len(port.calls), limit+7)
	}
}

func TestLifecycleReconcileAdvancesSameRevokingLaneAfterRestartDespitePersistentFailureAndNewerArrivals(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	const limit = 3
	blockedErr := errors.New("persistent cleanup failure")
	blocked := make(map[string]model.BackupAssetExportJob, limit)
	for index := 0; index < limit; index++ {
		blockedUpdatedAt := now.Add(-4*time.Hour + time.Duration(index)*time.Minute)
		blockedID := createLifecycleJob(t, harness, blockedUpdatedAt, ExecutionFailed, nil)
		if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", blockedID).
			Updates(map[string]any{
				"cleanup_state": string(CleanupRevoking),
				"updated_at":    blockedUpdatedAt,
			}).Error; err != nil {
			t.Fatal(err)
		}
		var before model.BackupAssetExportJob
		if err := harness.db.Where("id = ?", blockedID).Take(&before).Error; err != nil {
			t.Fatal(err)
		}
		blocked[blockedID] = before
	}

	callsByJob := make(map[string]int)
	port := &lifecyclePortFake{beforeCall: func(name, jobID string) error {
		callsByJob[jobID]++
		if name == "fence_attempts" {
			if _, exists := blocked[jobID]; exists {
				return blockedErr
			}
		}
		return nil
	}}
	newLifecycle := func() *Lifecycle {
		t.Helper()
		lifecycle, err := NewLifecycle(LifecycleDependencies{
			DB: harness.db, Port: port, Now: func() time.Time { return now },
		})
		if err != nil {
			t.Fatal(err)
		}
		return lifecycle
	}

	processed, reconcileErr := newLifecycle().Reconcile(context.Background(), limit)
	if processed != 0 || !errors.Is(reconcileErr, blockedErr) {
		t.Fatalf("initial reconcile processed=%d err=%v, want persistent failure only", processed, reconcileErr)
	}

	sourceExpiredAt := now.Add(-2 * time.Hour)
	sourceExpiredID := createLifecycleJob(t, harness, sourceExpiredAt, ExecutionSourceExpired, nil)
	if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", sourceExpiredID).
		Updates(map[string]any{
			"cleanup_state": string(CleanupRevoking),
			"updated_at":    sourceExpiredAt,
		}).Error; err != nil {
		t.Fatal(err)
	}
	newerArrivalCount := lifecycleReconcileScanScale * limit * 2
	newerIDs := make([]string, 0, newerArrivalCount)
	for index := 0; index < newerArrivalCount; index++ {
		newerAt := now.Add(-time.Duration(newerArrivalCount-index) * time.Minute)
		jobID := createLifecycleJob(t, harness, newerAt, ExecutionFailed, nil)
		if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
			Updates(map[string]any{
				"cleanup_state": string(CleanupRevoking),
				"updated_at":    newerAt,
			}).Error; err != nil {
			t.Fatal(err)
		}
		newerIDs = append(newerIDs, jobID)
	}

	callsBeforeRestart := len(port.calls)
	processed, reconcileErr = newLifecycle().Reconcile(context.Background(), limit)
	if processed != limit || !errors.Is(reconcileErr, blockedErr) {
		t.Fatalf("restarted reconcile processed=%d err=%v, want source-expired progress plus persistent failure", processed, reconcileErr)
	}
	assertLifecycleJobState(t, harness, sourceExpiredID, ExecutionSourceExpired, CleanupPurged)
	if callsByJob[sourceExpiredID] != 7 {
		t.Fatalf("source-expired cleanup calls=%d, want ordered full cleanup", callsByJob[sourceExpiredID])
	}
	touchedNewer := 0
	for _, jobID := range newerIDs {
		if callsByJob[jobID] == 0 {
			continue
		}
		if callsByJob[jobID] != 7 {
			t.Fatalf("newer arrival %s cleanup calls=%d, want one ordered cleanup", jobID, callsByJob[jobID])
		}
		touchedNewer++
	}
	if touchedNewer != limit-1 {
		t.Fatalf("restarted reconcile touched %d newer arrivals, want bounded %d", touchedNewer, limit-1)
	}
	if restartCalls := len(port.calls) - callsBeforeRestart; restartCalls > limit*(1+7) {
		t.Fatalf("restarted reconcile made %d lifecycle calls, want at most %d", restartCalls, limit*(1+7))
	}

	for blockedID, before := range blocked {
		var after model.BackupAssetExportJob
		if err := harness.db.Where("id = ?", blockedID).Take(&after).Error; err != nil {
			t.Fatal(err)
		}
		if after.CleanupState != before.CleanupState ||
			after.TransitionRevision != before.TransitionRevision ||
			!after.UpdatedAt.Equal(before.UpdatedAt) {
			t.Fatalf("persistent failure was rewritten to rotate it: before=%+v after=%+v", before, after)
		}
	}
}

func TestLifecycleReconcilePersistsProgressPastAFullFailureWindowAcrossRestarts(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	const (
		limit                = 1
		persistentFailures   = lifecycleReconcileScanScale + 2
		maximumRestartPasses = persistentFailures + 2
	)
	blockedErr := errors.New("persistent cleanup failure")
	blocked := make(map[string]model.BackupAssetExportJob, persistentFailures*2)
	createBlocked := func(updatedAt time.Time) {
		t.Helper()
		jobID := createLifecycleJob(t, harness, updatedAt, ExecutionFailed, nil)
		if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
			Updates(map[string]any{
				"cleanup_state": string(CleanupRevoking),
				"updated_at":    updatedAt,
			}).Error; err != nil {
			t.Fatal(err)
		}
		var before model.BackupAssetExportJob
		if err := harness.db.Where("id = ?", jobID).Take(&before).Error; err != nil {
			t.Fatal(err)
		}
		blocked[jobID] = before
	}
	for index := 0; index < persistentFailures; index++ {
		createBlocked(now.Add(-4*time.Hour + time.Duration(index)*time.Minute))
	}

	actionableAt := now.Add(-2 * time.Hour)
	actionableID := createLifecycleJob(t, harness, actionableAt, ExecutionSourceExpired, nil)
	if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", actionableID).
		Updates(map[string]any{
			"cleanup_state": string(CleanupRevoking),
			"updated_at":    actionableAt,
		}).Error; err != nil {
		t.Fatal(err)
	}
	for index := 0; index < persistentFailures; index++ {
		createBlocked(now.Add(-time.Duration(persistentFailures-index) * time.Minute))
	}

	port := &lifecyclePortFake{beforeCall: func(name, jobID string) error {
		if name == "fence_attempts" {
			if _, exists := blocked[jobID]; exists {
				return blockedErr
			}
		}
		return nil
	}}
	processedActionable := false
	for pass := 0; pass < maximumRestartPasses; pass++ {
		lifecycle, err := NewLifecycle(LifecycleDependencies{
			DB: harness.db, Port: port, Now: func() time.Time { return now },
		})
		if err != nil {
			t.Fatal(err)
		}
		processed, reconcileErr := lifecycle.Reconcile(context.Background(), limit)
		if !errors.Is(reconcileErr, blockedErr) {
			t.Fatalf("restart pass %d err=%v, want persistent cleanup failure", pass, reconcileErr)
		}
		if processed > limit {
			t.Fatalf("restart pass %d processed=%d, want bounded limit %d", pass, processed, limit)
		}
		var actionable model.BackupAssetExportJob
		if err := harness.db.Where("id = ?", actionableID).Take(&actionable).Error; err != nil {
			t.Fatal(err)
		}
		if actionable.CleanupState == string(CleanupPurged) {
			processedActionable = true
			break
		}
	}
	if !processedActionable {
		t.Fatalf("actionable middle job %s remained starved across %d fresh reconcilers", actionableID, maximumRestartPasses)
	}
	assertLifecycleJobState(t, harness, actionableID, ExecutionSourceExpired, CleanupPurged)
	for blockedID, before := range blocked {
		var after model.BackupAssetExportJob
		if err := harness.db.Where("id = ?", blockedID).Take(&after).Error; err != nil {
			t.Fatal(err)
		}
		if after.CleanupState != before.CleanupState ||
			after.TransitionRevision != before.TransitionRevision ||
			!after.UpdatedAt.Equal(before.UpdatedAt) {
			t.Fatalf("persistent failure was rewritten to rotate it: before=%+v after=%+v", before, after)
		}
	}
}

func TestLifecycleReconcileRejectsAJobWithoutThePermanentGlobalLatch(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	job := quotaTestJob(t, strings.Repeat("e", 32), 1, now)
	job.LifecycleEnqueueSequence = 1
	job.ExecutionState = string(ExecutionFailed)
	if err := harness.db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	port := &lifecyclePortFake{}
	lifecycle, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: port, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	processed, reconcileErr := lifecycle.Reconcile(context.Background(), 1)
	if processed != 0 || !errors.Is(reconcileErr, ErrUnavailable) {
		t.Fatalf("reconcile without global latch processed=%d err=%v, want fail-closed invariant error", processed, reconcileErr)
	}
	var buckets int64
	if err := harness.db.Model(&model.BackupAssetExportQuotaBucket{}).Count(&buckets).Error; err != nil {
		t.Fatal(err)
	}
	if buckets != 0 || len(port.calls) != 0 {
		t.Fatalf("reconcile manufactured latch/work: buckets=%d calls=%v", buckets, port.calls)
	}
}

func TestLifecycleReconcilePristineSchemaDoesNotCreatePermanentGlobalLatch(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	port := &lifecyclePortFake{}
	lifecycle, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: port, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	processed, reconcileErr := lifecycle.Reconcile(context.Background(), 1)
	if reconcileErr != nil || processed != 0 {
		t.Fatalf("pristine reconcile processed=%d err=%v, want no work", processed, reconcileErr)
	}
	var buckets int64
	if err := harness.db.Model(&model.BackupAssetExportQuotaBucket{}).Count(&buckets).Error; err != nil {
		t.Fatal(err)
	}
	if buckets != 0 || len(port.calls) != 0 {
		t.Fatalf("pristine reconcile manufactured durable state: buckets=%d calls=%v", buckets, port.calls)
	}
}

func TestLifecycleReconcileGlobalLogicalLeaseExcludesConcurrentReconcilers(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	jobID := createLifecycleJob(t, harness, now.Add(-time.Hour), ExecutionFailed, nil)
	enteredCleanup := make(chan struct{})
	releaseCleanup := make(chan struct{})
	var enteredOnce sync.Once
	firstPort := &lifecyclePortFake{beforeCall: func(name, gotJobID string) error {
		if name != "fence_attempts" || gotJobID != jobID {
			return nil
		}
		enteredOnce.Do(func() { close(enteredCleanup) })
		<-releaseCleanup
		return nil
	}}
	first, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: firstPort, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	type reconcileResult struct {
		processed int
		err       error
	}
	firstResult := make(chan reconcileResult, 1)
	go func() {
		processed, reconcileErr := first.Reconcile(context.Background(), 1)
		firstResult <- reconcileResult{processed: processed, err: reconcileErr}
	}()
	select {
	case <-enteredCleanup:
	case <-time.After(2 * time.Second):
		t.Fatal("first reconciler did not reach cleanup callback")
	}

	secondPort := &lifecyclePortFake{}
	second, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: secondPort, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	processed, reconcileErr := second.Reconcile(context.Background(), 1)
	if reconcileErr != nil || processed != 0 || len(secondPort.calls) != 0 {
		t.Fatalf("concurrent reconciler crossed live logical lease: processed=%d err=%v calls=%v",
			processed, reconcileErr, secondPort.calls)
	}

	close(releaseCleanup)
	select {
	case result := <-firstResult:
		if result.err != nil || result.processed != 1 {
			t.Fatalf("lease owner reconcile processed=%d err=%v", result.processed, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lease owner reconcile did not finish")
	}
	assertLifecycleJobState(t, harness, jobID, ExecutionFailed, CleanupPurged)
}

func TestLifecycleSweepExpiredLeaseTakeoverRejectsStaleRevision(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	_ = createLifecycleJob(t, harness, now.Add(-time.Hour), ExecutionFailed, nil)
	first, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: &lifecyclePortFake{}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	firstSweep, acquired, err := first.acquireLifecycleSweep(context.Background(), now)
	if err != nil || !acquired {
		t.Fatalf("first sweep acquired=%v err=%v", acquired, err)
	}
	var afterFirst model.BackupAssetExportQuotaBucket
	if err := harness.db.Where("scope = ? AND subject = ?", "global", "global").Take(&afterFirst).Error; err != nil {
		t.Fatal(err)
	}

	takeoverNow := now.Add(lifecycleSweepLeaseTTL + time.Second)
	second, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: &lifecyclePortFake{}, Now: func() time.Time { return takeoverNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	secondSweep, acquired, err := second.acquireLifecycleSweep(context.Background(), takeoverNow)
	if err != nil || !acquired {
		t.Fatalf("expired sweep takeover acquired=%v err=%v", acquired, err)
	}
	if secondSweep.revision != firstSweep.revision+1 || secondSweep.cursor != firstSweep.cursor ||
		secondSweep.highWater != firstSweep.highWater {
		t.Fatalf("expired sweep takeover changed work boundary: first=%+v second=%+v", firstSweep, secondSweep)
	}
	if err := first.persistLifecycleSweepProgress(context.Background(), &firstSweep, firstSweep.cursor, true); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stale sweep revision persist err=%v, want fail-closed", err)
	}
	if err := second.persistLifecycleSweepProgress(context.Background(), &secondSweep, secondSweep.cursor, true); err != nil {
		t.Fatalf("release takeover sweep: %v", err)
	}

	var afterTakeover model.BackupAssetExportQuotaBucket
	if err := harness.db.Where("id = ?", afterFirst.ID).Take(&afterTakeover).Error; err != nil {
		t.Fatal(err)
	}
	if afterTakeover.LifecycleSweepRevision != secondSweep.revision || afterTakeover.LifecycleSweepLeaseExpiresAt != nil {
		t.Fatalf("takeover sweep was not revision-fenced and released: %+v", afterTakeover)
	}
	if afterTakeover.TransitionRevision != afterFirst.TransitionRevision ||
		!afterTakeover.UpdatedAt.Equal(afterFirst.UpdatedAt) {
		t.Fatalf("scheduler mutated quota accounting revision/timestamp: before=%+v after=%+v", afterFirst, afterTakeover)
	}
}

func TestLifecycleReconcileDefersArrivalsAboveFiniteSweepHighWater(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	firstJobID := createLifecycleJob(t, harness, now.Add(-time.Hour), ExecutionFailed, nil)
	var arrivalJobID string
	port := &lifecyclePortFake{beforeCall: func(name, jobID string) error {
		if name == "fence_attempts" && jobID == firstJobID && arrivalJobID == "" {
			arrivalJobID = createLifecycleJob(t, harness, now.Add(-time.Minute), ExecutionFailed, nil)
		}
		return nil
	}}
	first, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: port, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	processed, reconcileErr := first.Reconcile(context.Background(), 2)
	if reconcileErr != nil || processed != 1 || arrivalJobID == "" {
		t.Fatalf("finite sweep processed=%d err=%v arrival=%q", processed, reconcileErr, arrivalJobID)
	}
	assertLifecycleJobState(t, harness, firstJobID, ExecutionFailed, CleanupPurged)
	assertLifecycleJobState(t, harness, arrivalJobID, ExecutionFailed, CleanupNone)
	for _, call := range port.calls {
		if strings.Contains(call, arrivalJobID) {
			t.Fatalf("first finite sweep processed above-high-water arrival: calls=%v", port.calls)
		}
	}

	second, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: port, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	processed, reconcileErr = second.Reconcile(context.Background(), 2)
	if reconcileErr != nil || processed != 1 {
		t.Fatalf("next finite sweep processed=%d err=%v, want deferred arrival", processed, reconcileErr)
	}
	assertLifecycleJobState(t, harness, arrivalJobID, ExecutionFailed, CleanupPurged)
}

func TestLifecycleReconcileReleasesGlobalRowBeforeCleanupCallback(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	jobID := createLifecycleJob(t, harness, now.Add(-time.Hour), ExecutionFailed, nil)
	var before model.BackupAssetExportQuotaBucket
	if err := harness.db.Where("scope = ? AND subject = ?", "global", "global").Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	callbackAt := now.Add(time.Second)
	updatedGlobal := false
	port := &lifecyclePortFake{beforeCall: func(name, gotJobID string) error {
		if name != "fence_attempts" || gotJobID != jobID || updatedGlobal {
			return nil
		}
		return harness.db.Transaction(func(tx *gorm.DB) error {
			var bucket model.BackupAssetExportQuotaBucket
			result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND scope = ? AND subject = ?", before.ID, "global", "global").Take(&bucket)
			if result.Error != nil {
				return result.Error
			}
			result = tx.Model(&model.BackupAssetExportQuotaBucket{}).
				Where("id = ? AND transition_revision = ?", bucket.ID, bucket.TransitionRevision).
				Updates(map[string]any{
					"transition_revision": gorm.Expr("transition_revision + 1"),
					"updated_at":          callbackAt,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrUnavailable
			}
			updatedGlobal = true
			return nil
		})
	}}
	lifecycle, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: port, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	processed, reconcileErr := lifecycle.Reconcile(context.Background(), 1)
	if reconcileErr != nil || processed != 1 || !updatedGlobal {
		t.Fatalf("cleanup callback could not update unlocked global row: processed=%d err=%v updated=%v",
			processed, reconcileErr, updatedGlobal)
	}
	var after model.BackupAssetExportQuotaBucket
	if err := harness.db.Where("id = ?", before.ID).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.TransitionRevision != before.TransitionRevision+1 || !after.UpdatedAt.Equal(callbackAt) {
		t.Fatalf("scheduler overwrote callback quota update: before=%+v after=%+v", before, after)
	}
}

func TestLifecycleReconcileAdvancesInteriorExpiryAcrossRestartDespitePersistentFailuresAndNewerArrivals(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	const (
		limit = 1
	)
	blockedErr := errors.New("persistent cleanup failure")
	blocked := make(map[string]model.BackupAssetExportJob, lifecycleReconcileScanScale*limit)
	for index := 0; index < lifecycleReconcileScanScale*limit; index++ {
		blockedUpdatedAt := now.Add(-4*time.Hour + time.Duration(index)*time.Minute)
		blockedID := createLifecycleJob(t, harness, blockedUpdatedAt, ExecutionFailed, nil)
		if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", blockedID).
			Updates(map[string]any{
				"cleanup_state": string(CleanupRevoking),
				"updated_at":    blockedUpdatedAt,
			}).Error; err != nil {
			t.Fatal(err)
		}
		var job model.BackupAssetExportJob
		if err := harness.db.Where("id = ?", blockedID).Take(&job).Error; err != nil {
			t.Fatal(err)
		}
		blocked[blockedID] = job
	}
	interiorExpiresAt := now.Add(time.Hour)
	interiorID := createLifecycleJob(t, harness, now.Add(-2*time.Hour), ExecutionReady, &interiorExpiresAt)
	for index := 0; index < lifecycleReconcileScanScale*limit; index++ {
		newerAt := now.Add(-time.Duration(lifecycleReconcileScanScale-index) * time.Minute)
		createLifecycleJob(t, harness, newerAt, ExecutionFailed, nil)
	}

	port := &lifecyclePortFake{beforeCall: func(name, jobID string) error {
		if name == "fence_attempts" {
			if _, exists := blocked[jobID]; exists {
				return blockedErr
			}
		}
		return nil
	}}
	clock := now
	newLifecycle := func() *Lifecycle {
		t.Helper()
		lifecycle, err := NewLifecycle(LifecycleDependencies{
			DB: harness.db, Port: port, Now: func() time.Time { return clock },
		})
		if err != nil {
			t.Fatal(err)
		}
		return lifecycle
	}

	processed, reconcileErr := newLifecycle().Reconcile(context.Background(), limit)
	if processed != 0 || !errors.Is(reconcileErr, blockedErr) {
		t.Fatalf("first bounded reconcile processed=%d err=%v, want a persisted full failure window", processed, reconcileErr)
	}
	for blockedID, before := range blocked {
		var afterFirstPass model.BackupAssetExportJob
		if err := harness.db.Where("id = ?", blockedID).Take(&afterFirstPass).Error; err != nil {
			t.Fatal(err)
		}
		if afterFirstPass.CleanupState != before.CleanupState ||
			afterFirstPass.TransitionRevision != before.TransitionRevision ||
			!afterFirstPass.UpdatedAt.Equal(before.UpdatedAt) {
			t.Fatalf("first pass rewrote persistent failure %s: before=%+v after=%+v", blockedID, before, afterFirstPass)
		}
	}

	clock = now.Add(2 * time.Hour)
	callsBeforeRestart := len(port.calls)
	processed, reconcileErr = newLifecycle().Reconcile(context.Background(), limit)
	if processed != 1 || reconcileErr != nil {
		t.Fatalf("restarted reconcile processed=%d err=%v, want persisted cursor to reach interior expiry", processed, reconcileErr)
	}
	assertLifecycleJobState(t, harness, interiorID, ExecutionExpired, CleanupPurged)
	for blockedID, before := range blocked {
		var afterRestart model.BackupAssetExportJob
		if err := harness.db.Where("id = ?", blockedID).Take(&afterRestart).Error; err != nil {
			t.Fatal(err)
		}
		if afterRestart.CleanupState != before.CleanupState ||
			afterRestart.TransitionRevision != before.TransitionRevision ||
			!afterRestart.UpdatedAt.Equal(before.UpdatedAt) {
			t.Fatalf("restart rewrote persistent failure %s: before=%+v after=%+v", blockedID, before, afterRestart)
		}
	}
	if restartCalls := len(port.calls) - callsBeforeRestart; restartCalls > lifecycleReconcileScanScale*limit+7 {
		t.Fatalf("restart reconcile made %d lifecycle calls, want at most %d", restartCalls, lifecycleReconcileScanScale*limit+7)
	}
}

func TestLifecycleReconcileDoesNotLetPurgedTerminalRowsConsumeLimit(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	port := &lifecyclePortFake{}
	olderCleanupAt := now.Add(-2 * time.Minute)
	cleaner, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: port, Now: func() time.Time { return olderCleanupAt },
	})
	if err != nil {
		t.Fatal(err)
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
		jobID := createLifecycleJob(t, harness, now.Add(-4*time.Hour+time.Duration(index)*time.Minute), inert.state, nil)
		if state, err := cleaner.Cleanup(context.Background(), jobID); err != nil || state != CleanupPurged {
			t.Fatalf("clean older %s job state=%s err=%v", inert.name, state, err)
		}
		var before model.BackupAssetExportJob
		if err := harness.db.First(&before, "id = ?", jobID).Error; err != nil {
			t.Fatal(err)
		}
		if before.ExecutionState != string(inert.state) || before.CleanupState != string(CleanupPurged) {
			t.Fatalf("older %s tombstone state=%s cleanup=%s", inert.name, before.ExecutionState, before.CleanupState)
		}
		inertBefore = append(inertBefore, before)
	}
	assertInertUnchanged := func(stage string) {
		t.Helper()
		for index, before := range inertBefore {
			var after model.BackupAssetExportJob
			if err := harness.db.First(&after, "id = ?", before.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("%s older %s tombstone changed:\nbefore=%+v\nafter=%+v", stage, inertStates[index].name, before, after)
			}
		}
	}

	newerJobID := createLifecycleJob(t, harness, now.Add(-time.Minute), ExecutionFailed, nil)
	port.calls = nil
	lifecycle, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: port, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	count, err := lifecycle.Reconcile(context.Background(), 1)
	if err != nil || count != 1 {
		t.Fatalf("first reconcile count=%d err=%v", count, err)
	}
	var newerAfter model.BackupAssetExportJob
	if err := harness.db.First(&newerAfter, "id = ?", newerJobID).Error; err != nil {
		t.Fatal(err)
	}
	if newerAfter.CleanupState != string(CleanupPurged) {
		t.Fatalf("newer actionable job cleanup=%s; an older inert tombstone consumed the bounded candidate", newerAfter.CleanupState)
	}
	assertInertUnchanged("first reconcile")
	wantCalls := []string{
		"fence_attempts", "revoke_deliveries", "drain_streams", "destroy_key",
		"release_sources", "purge_ciphertext", "release_store",
	}
	if !reflect.DeepEqual(port.calls, wantCalls) {
		t.Fatalf("first reconcile calls=%v want=%v", port.calls, wantCalls)
	}

	port.calls = nil
	count, err = lifecycle.Reconcile(context.Background(), 1)
	if err != nil || count != 0 {
		t.Fatalf("second reconcile count=%d err=%v", count, err)
	}
	if len(port.calls) != 0 {
		t.Fatalf("second reconcile made lifecycle calls=%v", port.calls)
	}
	assertInertUnchanged("second reconcile")
}

func TestLifecycleReconcileFinalizesPurgedTransitionalRowsWithoutCleanupIO(t *testing.T) {
	for _, test := range []struct {
		name      string
		from      ExecutionState
		want      ExecutionState
		expiresAt func(time.Time) *time.Time
	}{
		{name: "cancel requested", from: ExecutionCancelRequested, want: ExecutionCanceled},
		{name: "expiring", from: ExecutionExpiring, want: ExecutionExpired, expiresAt: func(now time.Time) *time.Time {
			expiresAt := now.Add(-time.Minute)
			return &expiresAt
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newServiceHarness(t)
			now := time.Now().UTC().Truncate(time.Second)
			var expiresAt *time.Time
			if test.expiresAt != nil {
				expiresAt = test.expiresAt(now)
			}
			jobID := createLifecycleJob(t, harness, now.Add(-time.Hour), test.from, expiresAt)
			port := &lifecyclePortFake{}
			cleaner, err := NewLifecycle(LifecycleDependencies{
				DB: harness.db, Port: port, Now: func() time.Time { return now.Add(-time.Minute) },
			})
			if err != nil {
				t.Fatal(err)
			}
			if state, err := cleaner.Cleanup(context.Background(), jobID); err != nil || state != CleanupPurged {
				t.Fatalf("prepare purged transition state=%s err=%v", state, err)
			}
			var before model.BackupAssetExportJob
			if err := harness.db.First(&before, "id = ?", jobID).Error; err != nil {
				t.Fatal(err)
			}

			port.calls = nil
			lifecycle, err := NewLifecycle(LifecycleDependencies{
				DB: harness.db, Port: port, Now: func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}
			count, err := lifecycle.Reconcile(context.Background(), 1)
			if err != nil || count != 1 {
				t.Fatalf("reconcile purged transition count=%d err=%v", count, err)
			}
			var after model.BackupAssetExportJob
			if err := harness.db.First(&after, "id = ?", jobID).Error; err != nil {
				t.Fatal(err)
			}
			if after.ExecutionState != string(test.want) || after.CleanupState != string(CleanupPurged) ||
				after.TransitionRevision != before.TransitionRevision+1 {
				t.Fatalf("purged transition before=%+v after=%+v want execution=%s and one CAS revision", before, after, test.want)
			}
			if len(port.calls) != 0 {
				t.Fatalf("purged transition made lifecycle calls=%v", port.calls)
			}

			count, err = lifecycle.Reconcile(context.Background(), 1)
			if err != nil || count != 0 {
				t.Fatalf("terminal tombstone reconcile count=%d err=%v", count, err)
			}
			if len(port.calls) != 0 {
				t.Fatalf("terminal tombstone made lifecycle calls=%v", port.calls)
			}
		})
	}
}

type lifecycleTransitionCASTestContextKey struct{}

func TestLifecycleReconcilePurgedTransitionRejectsStaleRevisionWithoutCleanupIO(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	jobID := createLifecycleJob(t, harness, now.Add(-time.Hour), ExecutionCancelRequested, nil)
	port := &lifecyclePortFake{}
	lifecycle, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: port, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if state, err := lifecycle.Cleanup(context.Background(), jobID); err != nil || state != CleanupPurged {
		t.Fatalf("prepare purged CAS transition state=%s err=%v", state, err)
	}
	var before model.BackupAssetExportJob
	if err := harness.db.First(&before, "id = ?", jobID).Error; err != nil {
		t.Fatal(err)
	}

	marker := t.Name()
	var injected atomic.Bool
	const callbackName = "test:inject_lifecycle_transition_revision_drift"
	if err := harness.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != "backup_asset_export_jobs" ||
			tx.Statement.Context.Value(lifecycleTransitionCASTestContextKey{}) != marker ||
			!injected.CompareAndSwap(false, true) {
			return
		}
		if err := tx.Exec(`UPDATE backup_asset_export_jobs
			SET transition_revision = transition_revision + 1 WHERE id = ?`, jobID).Error; err != nil {
			_ = tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := harness.db.Callback().Update().Remove(callbackName); err != nil {
			t.Errorf("remove lifecycle transition CAS callback: %v", err)
		}
	})

	port.calls = nil
	ctx := context.WithValue(context.Background(), lifecycleTransitionCASTestContextKey{}, marker)
	count, err := lifecycle.Reconcile(ctx, 1)
	if !errors.Is(err, ErrInvalidTransition) || count != 0 {
		t.Fatalf("stale transition reconcile count=%d err=%v want ErrInvalidTransition", count, err)
	}
	if !injected.Load() {
		t.Fatal("transition revision drift was not injected")
	}
	var after model.BackupAssetExportJob
	if err := harness.db.First(&after, "id = ?", jobID).Error; err != nil {
		t.Fatal(err)
	}
	if after.ExecutionState != string(ExecutionCancelRequested) || after.CleanupState != string(CleanupPurged) ||
		after.TransitionRevision != before.TransitionRevision+1 {
		t.Fatalf("stale transition CAS state before=%+v after=%+v", before, after)
	}
	if len(port.calls) != 0 {
		t.Fatalf("stale purged transition made lifecycle calls=%v", port.calls)
	}
}

func TestLifecycleReconcileExpiresReadyWithoutSlidingFutureTTL(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	pastExpiry := now.Add(-time.Second)
	futureExpiry := now.Add(time.Hour)
	expiredJobID := createLifecycleJob(t, harness, now.Add(-time.Hour), ExecutionReady, &pastExpiry)
	futureJobID := createLifecycleJob(t, harness, now, ExecutionReady, &futureExpiry)
	port := &lifecyclePortFake{}
	lifecycle, err := NewLifecycle(LifecycleDependencies{DB: harness.db, Port: port, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	count, err := lifecycle.Reconcile(context.Background(), 10)
	if err != nil || count != 1 {
		t.Fatalf("reconcile count=%d err=%v", count, err)
	}
	assertLifecycleJobState(t, harness, expiredJobID, ExecutionExpired, CleanupPurged)
	assertLifecycleJobState(t, harness, futureJobID, ExecutionReady, CleanupNone)
	var future model.BackupAssetExportJob
	if err := harness.db.First(&future, "id = ?", futureJobID).Error; err != nil {
		t.Fatal(err)
	}
	if future.ExpiresAt == nil || !future.ExpiresAt.Equal(futureExpiry) {
		t.Fatalf("future expiry slid: got=%v want=%v", future.ExpiresAt, futureExpiry)
	}
}

func TestLifecycleReconcileCompletesCancelRequestedAfterCleanup(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	jobID := createLifecycleJob(t, harness, now, ExecutionCancelRequested, nil)
	port := &lifecyclePortFake{}
	lifecycle, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: port, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	count, err := lifecycle.Reconcile(context.Background(), 10)
	if err != nil || count != 1 {
		t.Fatalf("reconcile count=%d err=%v", count, err)
	}
	assertLifecycleJobState(t, harness, jobID, ExecutionCanceled, CleanupPurged)
	want := []string{
		"fence_attempts", "revoke_deliveries", "drain_streams", "destroy_key",
		"release_sources", "purge_ciphertext", "release_store",
	}
	if !reflect.DeepEqual(port.calls, want) {
		t.Fatalf("cancel cleanup order=%v want=%v", port.calls, want)
	}
}

func TestLifecyclePurgeFailureStillFinalizesExecutionOutcome(t *testing.T) {
	for _, test := range []struct {
		name  string
		first func(context.Context, *Lifecycle, string) error
		setup func(*testing.T, serviceHarness, time.Time) string
		want  ExecutionState
	}{
		{
			name: "cancel requested through reconcile",
			setup: func(t *testing.T, harness serviceHarness, now time.Time) string {
				return createLifecycleJob(t, harness, now, ExecutionCancelRequested, nil)
			},
			first: func(ctx context.Context, lifecycle *Lifecycle, jobID string) error {
				count, err := lifecycle.Reconcile(ctx, 1)
				if count != 0 {
					return fmt.Errorf("failed reconcile processed=%d", count)
				}
				return err
			},
			want: ExecutionCanceled,
		},
		{
			name: "expiring through unpublishable failure",
			setup: func(t *testing.T, harness serviceHarness, now time.Time) string {
				expiresAt := now.Add(time.Hour)
				return createLifecycleJob(t, harness, now, ExecutionReady, &expiresAt)
			},
			first: func(ctx context.Context, lifecycle *Lifecycle, jobID string) error {
				return lifecycle.FailUnpublishable(ctx, jobID, "artifact_tampered")
			},
			want: ExecutionExpired,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newServiceHarness(t)
			now := time.Now().UTC().Truncate(time.Second)
			jobID := test.setup(t, harness, now)
			purgeErr := errors.New("ciphertext purge unavailable")
			port := &lifecyclePortFake{failAt: "purge_ciphertext", failure: purgeErr}
			lifecycle, err := NewLifecycle(LifecycleDependencies{
				DB: harness.db, Port: port, Now: func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := test.first(context.Background(), lifecycle, jobID); !errors.Is(err, purgeErr) {
				t.Fatalf("first lifecycle error=%v want purge error %v", err, purgeErr)
			}
			assertLifecycleJobState(t, harness, jobID, test.want, CleanupPurgeFailed)

			port.failAt = ""
			port.failure = nil
			port.calls = nil
			count, err := lifecycle.Reconcile(context.Background(), 1)
			if err != nil || count != 1 {
				t.Fatalf("retry reconcile count=%d err=%v", count, err)
			}
			assertLifecycleJobState(t, harness, jobID, test.want, CleanupPurged)
		})
	}
}

func TestLifecycleFailsClosedImmediatelyWhenSourceLeaseIsLost(t *testing.T) {
	for _, test := range []struct {
		name string
		from ExecutionState
		want ExecutionState
	}{
		{name: "queued", from: ExecutionQueued, want: ExecutionSourceExpired},
		{name: "retry wait", from: ExecutionRetryWait, want: ExecutionSourceExpired},
		{name: "running", from: ExecutionRunning, want: ExecutionSourceExpired},
		{name: "sealing", from: ExecutionSealing, want: ExecutionSourceExpired},
		{name: "ready", from: ExecutionReady, want: ExecutionExpired},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newServiceHarness(t)
			now := time.Now().UTC().Truncate(time.Second)
			expiresAt := now.Add(time.Hour)
			jobID := createLifecycleJob(t, harness, now.Add(-time.Minute), test.from, &expiresAt)
			port := &lifecyclePortFake{}
			lifecycle, err := NewLifecycle(LifecycleDependencies{
				DB: harness.db, Port: port, Now: func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := lifecycle.FailSourceExpired(context.Background(), jobID); err != nil {
				t.Fatal(err)
			}
			assertLifecycleJobState(t, harness, jobID, test.want, CleanupPurged)
			var job model.BackupAssetExportJob
			if err := harness.db.First(&job, "id = ?", jobID).Error; err != nil {
				t.Fatal(err)
			}
			if job.ErrorCategory != "source_expired" {
				t.Fatalf("source failure category=%q", job.ErrorCategory)
			}
			wantCalls := []string{
				"fence_attempts", "revoke_deliveries", "drain_streams", "destroy_key",
				"release_sources", "purge_ciphertext", "release_store",
			}
			if !reflect.DeepEqual(port.calls, wantCalls) {
				t.Fatalf("source-loss cleanup calls=%v want=%v", port.calls, wantCalls)
			}
		})
	}
}

func TestLifecycleFailSourceExpiredCompletesAuthoritativeCancelWithoutReclassification(t *testing.T) {
	harness := newServiceHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	jobID := createLifecycleJob(t, harness, now, ExecutionCancelRequested, nil)
	port := &lifecyclePortFake{}
	lifecycle, err := NewLifecycle(LifecycleDependencies{DB: harness.db, Port: port, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.FailSourceExpired(context.Background(), jobID); err != nil {
		t.Fatal(err)
	}
	assertLifecycleJobState(t, harness, jobID, ExecutionCanceled, CleanupPurged)
	var job model.BackupAssetExportJob
	if err := harness.db.First(&job, "id = ?", jobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ErrorCategory == "source_expired" {
		t.Fatalf("cancel cleanup was reclassified as source_expired: %+v", job)
	}
	wantCalls := []string{"fence_attempts", "revoke_deliveries", "drain_streams", "destroy_key", "release_sources", "purge_ciphertext", "release_store"}
	if !reflect.DeepEqual(port.calls, wantCalls) {
		t.Fatalf("cancel cleanup order=%v want=%v", port.calls, wantCalls)
	}
}

func TestPersistentLifecyclePortCryptographicallyRevokesBeforePhysicalPurgeAndStoreRelease(t *testing.T) {
	harness := newServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 42, Role: "admin"}, Selection: selection,
		IdempotencyKey: "export-lifecycle-port-0001", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var job model.BackupAssetExportJob
	if err := harness.db.First(&job, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	now := job.CreatedAt.UTC()
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	attemptID, itemAttemptID, artifactID := strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32)
	fenceToken := []byte(strings.Repeat("f", 32))
	finalStaging, err := store.CreateStaging(job.ID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := finalStaging.File.Write([]byte("final-ciphertext")); err != nil {
		t.Fatal(err)
	}
	finalLocator, err := store.Seal(finalStaging)
	if err != nil {
		t.Fatal(err)
	}
	spoolStaging, err := store.CreateItemSpool(job.ID, attemptID, itemAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spoolStaging.File.Write([]byte("spool-ciphertext")); err != nil {
		t.Fatal(err)
	}
	spoolLocator, err := store.Seal(spoolStaging)
	if err != nil {
		t.Fatal(err)
	}

	var item model.BackupAssetExportItem
	var key model.BackupAssetExportKey
	if err := harness.db.First(&item, "job_id = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&key, "job_id = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	attempt := model.BackupAssetExportAttempt{
		ID: attemptID, JobID: job.ID, AttemptNumber: 1, WorkerOwner: "export-worker-test",
		State: string(AttemptActive), FenceToken: fenceToken, FenceDigest: strings.Repeat("e", 64),
		NoncePrefix: []byte("12345678"), LeaseExpiresAt: now.Add(10 * time.Minute), StagingLocator: finalLocator,
		IsCurrent: true, StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	itemAttempt := model.BackupAssetExportItemAttempt{
		ID: itemAttemptID, JobID: job.ID, ItemID: item.ID, AttemptID: attemptID, State: string(ItemRead),
		SpoolDigest: strings.Repeat("a", 64), SpoolSize: int64(len("spool-ciphertext")), SpoolLocator: spoolLocator,
		LogicalBytes: item.LogicalSize, ProviderBytes: item.LogicalSize, StartedAt: now, CreatedAt: now,
	}
	artifact := model.BackupAssetExportArtifact{
		ID: artifactID, JobID: job.ID, AttemptID: attemptID, JobKeyID: key.ID, State: "sealed", Locator: finalLocator,
		CipherVersion: 1, ChunkBytes: 65536, FormatVersion: 1, NoncePrefix: []byte("12345678"),
		CiphertextSize: int64(len("final-ciphertext")), CreatedAt: now, UpdatedAt: now,
	}
	if err := harness.db.Create(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Create(&itemAttempt).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Create(&artifact).Error; err != nil {
		t.Fatal(err)
	}
	chargeLifecycleStoreBytes(t, harness.db, job.ID, artifact.CiphertextSize)
	if err := harness.db.Model(&model.BackupAssetExportItem{}).Where("id = ?", item.ID).
		Updates(map[string]any{
			"state": string(ItemRead), "current_attempt_id": attemptID,
			"logical_bytes": item.LogicalSize, "provider_bytes": item.LogicalSize,
		}).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", job.ID).
		Updates(map[string]any{
			"execution_state": string(ExecutionFailed), "current_attempt_id": attemptID,
			"current_fence_revision": 1, "artifact_bytes": artifact.CiphertextSize,
			"error_category": "internal_failure",
		}).Error; err != nil {
		t.Fatal(err)
	}

	delivery := &persistentLifecycleDeliveryFake{}
	quota, err := NewQuotaService(harness.db, func() time.Time { return now }, harness.config.Quota)
	if err != nil {
		t.Fatal(err)
	}
	port, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
		DB: harness.db, Delivery: delivery, Sources: harness.lease, Quota: quota, Store: store,
		AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(LifecycleDependencies{DB: harness.db, Port: port, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if state, err := lifecycle.Cleanup(context.Background(), job.ID); err != nil || state != CleanupPurged {
		t.Fatalf("cleanup state=%s err=%v", state, err)
	}
	if !reflect.DeepEqual(delivery.calls, []string{"revoke:" + job.ID, "drain:" + job.ID}) {
		t.Fatalf("delivery calls=%v", delivery.calls)
	}

	if err := harness.db.First(&key, "id = ?", key.ID).Error; err != nil {
		t.Fatal(err)
	}
	if key.State != "destroyed" || len(key.WrappedDEK) != 0 || len(key.EnvelopeNonce) != 0 || key.DestroyedAt == nil {
		t.Fatalf("key not cryptographically destroyed: %+v", key)
	}
	if err := harness.db.First(&item, "id = ?", item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(item.PathNonce) != 0 || len(item.PathCiphertext) != 0 {
		t.Fatalf("selection metadata remains nonce=%d ciphertext=%d", len(item.PathNonce), len(item.PathCiphertext))
	}
	var source model.BackupAssetExportSourceLease
	if err := harness.db.First(&source, "job_id = ?", job.ID).Error; err != nil || source.State != "released" {
		t.Fatalf("source=%+v err=%v", source, err)
	}
	var recoveryLease model.RecoveryPointLease
	if err := harness.db.First(&recoveryLease, "id = ?", source.LeaseID).Error; err != nil ||
		recoveryLease.Status != string(backupasset.LeaseReleased) {
		t.Fatalf("recovery lease=%+v err=%v", recoveryLease, err)
	}
	for _, locator := range []string{finalLocator, spoolLocator} {
		if _, err := os.Lstat(filepath.Join(store.root, locator)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("ciphertext %q remains: %v", locator, err)
		}
	}
	if err := harness.db.First(&artifact, "id = ?", artifact.ID).Error; err != nil || artifact.State != "purged" || artifact.PurgedAt == nil {
		t.Fatalf("artifact=%+v err=%v", artifact, err)
	}
	var activeReservations int64
	if err := harness.db.Model(&model.BackupAssetExportReservation{}).
		Where("job_id = ? AND state <> ?", job.ID, "released").Count(&activeReservations).Error; err != nil || activeReservations != 0 {
		t.Fatalf("active reservations=%d err=%v", activeReservations, err)
	}
}

func TestPersistentLifecyclePortPurgeAndStoreReleaseProveGlobalInventory(t *testing.T) {
	harness := newServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	first, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 42, Role: "admin"}, Selection: selection,
		IdempotencyKey: "export-global-purge-first", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 42, Role: "admin"}, Selection: selection,
		IdempotencyKey: "export-global-purge-second", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var firstJob, secondJob model.BackupAssetExportJob
	if err := harness.db.First(&firstJob, "id = ?", first.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&secondJob, "id = ?", second.JobID).Error; err != nil {
		t.Fatal(err)
	}
	var firstKey, secondKey model.BackupAssetExportKey
	if err := harness.db.First(&firstKey, "job_id = ?", first.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&secondKey, "job_id = ?", second.JobID).Error; err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "exports")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	createCiphertext := func(exportID, attemptID string) string {
		t.Helper()
		staging, createErr := store.CreateStaging(exportID, attemptID)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := staging.File.Write([]byte("ciphertext")); writeErr != nil {
			t.Fatal(writeErr)
		}
		locator, sealErr := store.Seal(staging)
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		return locator
	}
	firstAttemptID := strings.Repeat("b", 32)
	secondAttemptID := strings.Repeat("c", 32)
	firstLocator := createCiphertext(firstJob.ID, firstAttemptID)
	secondLocator := createCiphertext(secondJob.ID, secondAttemptID)
	now := firstJob.CreatedAt.UTC()
	for _, artifact := range []model.BackupAssetExportArtifact{
		{
			ID: strings.Repeat("d", 32), JobID: firstJob.ID, AttemptID: firstAttemptID, JobKeyID: firstKey.ID,
			State: "sealed", Locator: firstLocator, CipherVersion: 1, ChunkBytes: 65536, FormatVersion: 1,
			NoncePrefix: []byte("12345678"), CiphertextSize: int64(len("ciphertext")), CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: strings.Repeat("e", 32), JobID: secondJob.ID, AttemptID: secondAttemptID, JobKeyID: secondKey.ID,
			State: "sealed", Locator: secondLocator, CipherVersion: 1, ChunkBytes: 65536, FormatVersion: 1,
			NoncePrefix: []byte("12345678"), CiphertextSize: int64(len("ciphertext")), CreatedAt: now, UpdatedAt: now,
		},
	} {
		if err := harness.db.Create(&artifact).Error; err != nil {
			t.Fatal(err)
		}
		chargeLifecycleStoreBytes(t, harness.db, artifact.JobID, artifact.CiphertextSize)
		result := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", artifact.JobID).
			Update("artifact_bytes", artifact.CiphertextSize)
		if result.Error != nil {
			t.Fatal(result.Error)
		}
		if result.RowsAffected != 1 {
			t.Fatalf("set job artifact bytes rows=%d, want one", result.RowsAffected)
		}
	}
	firstOrphan := strings.Repeat("f", 32) + ".xre"
	if err := os.WriteFile(filepath.Join(store.root, firstOrphan), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	quota, err := NewQuotaService(harness.db, func() time.Time { return now }, harness.config.Quota)
	if err != nil {
		t.Fatal(err)
	}
	port, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
		DB: harness.db, Delivery: &persistentLifecycleDeliveryFake{}, Sources: harness.lease, Quota: quota, Store: store,
		AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := port.PurgeCiphertext(context.Background(), firstJob.ID); err != nil {
		t.Fatal(err)
	}
	for _, locator := range []string{firstLocator, firstOrphan} {
		if _, err := os.Lstat(filepath.Join(store.root, locator)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unreferenced ciphertext %q remains: %v", locator, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(store.root, secondLocator)); err != nil {
		t.Fatalf("other job ciphertext was not retained: %v", err)
	}
	secondOrphan := strings.Repeat("a", 31) + "b.xre"
	if err := os.WriteFile(filepath.Join(store.root, secondOrphan), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := port.ReleaseStoreBytes(context.Background(), firstJob.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(store.root, secondOrphan)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("release proof retained new orphan: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(store.root, secondLocator)); err != nil {
		t.Fatalf("release proof removed referenced other-job ciphertext: %v", err)
	}
	var activeStoreReservations int64
	if err := harness.db.Model(&model.BackupAssetExportReservation{}).
		Where("job_id = ? AND kind = ? AND state = ?", firstJob.ID, "store", "active").Count(&activeStoreReservations).Error; err != nil || activeStoreReservations != 0 {
		t.Fatalf("first job active store reservations=%d err=%v", activeStoreReservations, err)
	}
}

type lifecycleReleaseStoreBarrierPort struct {
	inner          *PersistentLifecyclePort
	releaseContext context.Context
	reached        chan struct{}
	allow          chan struct{}
	reachedOnce    sync.Once
	allowOnce      sync.Once
}

func newLifecycleReleaseStoreBarrierPort(inner *PersistentLifecyclePort) *lifecycleReleaseStoreBarrierPort {
	return &lifecycleReleaseStoreBarrierPort{
		inner: inner, reached: make(chan struct{}), allow: make(chan struct{}),
	}
}

func (port *lifecycleReleaseStoreBarrierPort) FenceAttempts(ctx context.Context, jobID string) error {
	return port.inner.FenceAttempts(ctx, jobID)
}

func (port *lifecycleReleaseStoreBarrierPort) RevokeDeliveries(ctx context.Context, jobID string) error {
	return port.inner.RevokeDeliveries(ctx, jobID)
}

func (port *lifecycleReleaseStoreBarrierPort) DrainStreams(ctx context.Context, jobID string) error {
	return port.inner.DrainStreams(ctx, jobID)
}

func (port *lifecycleReleaseStoreBarrierPort) DestroyJobKeyAndSelection(ctx context.Context, jobID string) error {
	return port.inner.DestroyJobKeyAndSelection(ctx, jobID)
}

func (port *lifecycleReleaseStoreBarrierPort) ReleaseSourcesAndNonStore(ctx context.Context, jobID string) error {
	return port.inner.ReleaseSourcesAndNonStore(ctx, jobID)
}

func (port *lifecycleReleaseStoreBarrierPort) PurgeCiphertext(ctx context.Context, jobID string) error {
	return port.inner.PurgeCiphertext(ctx, jobID)
}

func (port *lifecycleReleaseStoreBarrierPort) ReleaseStoreBytes(ctx context.Context, jobID string) error {
	port.reachedOnce.Do(func() {
		close(port.reached)
		<-port.allow
	})
	if port.releaseContext != nil {
		ctx = port.releaseContext
	}
	return port.inner.ReleaseStoreBytes(ctx, jobID)
}

func (port *lifecycleReleaseStoreBarrierPort) setReleaseContext(ctx context.Context) {
	port.releaseContext = ctx
}

func (port *lifecycleReleaseStoreBarrierPort) proceed() {
	port.allowOnce.Do(func() { close(port.allow) })
}

func TestPersistentLifecyclePortReleaseStoreBytesRetainsArchivePublishedAfterGlobalSnapshot(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	item := frozenItemFixture()
	created, claim, itemAttempt := createClaimedExportForAttemptBudget(
		t, harness, 119, "lifecycle-global-snapshot-archive-publication",
	)
	clock := harness.service.now().UTC()
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	broker, err := content.NewAttemptBroker(
		&persistentSourceResolverFake{payload: bytes.Repeat([]byte("a"), int(item.LogicalSize)), providerBytes: item.LogicalSize},
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
	if _, err := worker.SpoolItem(context.Background(), PersistentSpoolItemRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: itemAttempt.ItemID,
	}); err != nil {
		t.Fatal(err)
	}

	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	cleanupActor := SelectionActor{UserID: 120, Role: "admin"}
	cleanupJob, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: cleanupActor, Selection: selection,
		IdempotencyKey: "lifecycle-global-snapshot-cleanup-job", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.service.Cancel(context.Background(), cleanupActor, cleanupJob.JobID); err != nil {
		t.Fatal(err)
	}
	quota, err := NewQuotaService(harness.db, func() time.Time { return clock }, harness.config.Quota)
	if err != nil {
		t.Fatal(err)
	}
	port, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
		DB: harness.db, Delivery: exportBehaviorLifecycleDeliveryStub{}, Sources: harness.lease, Quota: quota, Store: store,
		AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	releasePort := newLifecycleReleaseStoreBarrierPort(port)
	t.Cleanup(releasePort.proceed)
	lifecycle, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: releasePort, Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	type cleanupResult struct {
		state CleanupState
		err   error
	}
	cleanupDone := make(chan cleanupResult, 1)
	go func() {
		state, cleanupErr := lifecycle.Cleanup(context.Background(), cleanupJob.JobID)
		cleanupDone <- cleanupResult{state: state, err: cleanupErr}
	}()
	select {
	case <-releasePort.reached:
	case result := <-cleanupDone:
		t.Fatalf("lifecycle cleanup ended before ReleaseStoreBytes: state=%s err=%v", result.state, result.err)
	case <-time.After(5 * time.Second):
		t.Fatal("lifecycle cleanup did not reach ReleaseStoreBytes")
	}
	assertLifecycleJobState(t, harness, cleanupJob.JobID, ExecutionCancelRequested, CleanupPurging)

	observer := installReferencedStoreSnapshotObserver(t, harness.db)
	gate := installPublicationSweepGateBarrier(t, store)
	releasePort.setReleaseContext(observer.ctx)
	releasePort.proceed()
	waitForPublicationSweepGate(t, gate, "lifecycle global ciphertext sweep")

	publication := installPublishedDirectoryBarrier(t, store)
	type sealResult struct {
		result PersistentSealResult
		err    error
	}
	sealed := make(chan sealResult, 1)
	go func() {
		result, sealErr := worker.SealArchive(context.Background(), PersistentSealRequest{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
		})
		sealed <- sealResult{result: result, err: sealErr}
	}()
	waitForPublishedDirectory(t, publication, "archive publication")

	gate.proceed()
	waitForPublicationSweepGateWriter(t, store, "lifecycle global ciphertext sweep")
	requireReferencedStoreSnapshotQueuedBehindPublication(t, observer, "lifecycle global ciphertext sweep")
	publication.unblock()

	var archive sealResult
	select {
	case archive = <-sealed:
		if archive.err != nil {
			t.Fatal(archive.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("archive publication did not resume after the directory sync barrier was released")
	}
	waitForReferencedStoreSnapshot(t, observer, "lifecycle global ciphertext sweep")
	select {
	case result := <-cleanupDone:
		if result.err != nil || result.state != CleanupPurged {
			t.Fatalf("lifecycle cleanup result=%+v, want %s", result, CleanupPurged)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lifecycle global ciphertext sweep did not resume after archive publication committed")
	}
	assertLifecycleJobState(t, harness, cleanupJob.JobID, ExecutionCancelRequested, CleanupPurged)
	var artifact model.BackupAssetExportArtifact
	if err := harness.db.First(&artifact, "id = ?", archive.result.ArtifactID).Error; err != nil {
		t.Fatal(err)
	}
	if artifact.Locator != archive.result.Locator || artifact.State != "sealed" {
		t.Fatalf("persisted artifact=%+v result=%+v", artifact, archive.result)
	}
	reader, err := store.OpenSealed(archive.result.Locator)
	if err != nil {
		t.Fatalf("archive published after the stale lifecycle snapshot was removed: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
}

type fenceAttemptsTestContextKey struct{}

const fenceAttemptsTestMaxItemUpdateBatchSize = 400

type fenceAttemptsLockObservation struct {
	table                    string
	itemProjectionOrdered    bool
	itemProjectionScoped     bool
	itemProjectionSelectsID  bool
	itemAttemptOrdered       bool
	itemAttemptScoped        bool
	itemAttemptSelectsOnlyID bool
	itemAttemptLockForUpdate bool
}

func TestPersistentLifecyclePortFenceAttemptsPreservesSealedCancelRequestedLineage(t *testing.T) {
	harness := newServiceHarness(t)
	job, attempt, itemAttempt := claimFenceAttemptsTestJob(t, harness)
	var item model.BackupAssetExportItem
	if err := harness.db.Where("id = ? AND job_id = ?", itemAttempt.ItemID, job.ID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	finishedAt := harness.service.now().UTC().Add(-time.Second)
	if err := harness.db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", itemAttempt.ID).
		Updates(map[string]any{
			"state": string(ItemPacked), "logical_bytes": item.LogicalSize, "provider_bytes": item.LogicalSize,
			"packed_at": finishedAt, "finished_at": finishedAt,
		}).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetExportItem{}).Where("id = ?", item.ID).
		Updates(map[string]any{
			"state": string(ItemPacked), "logical_bytes": item.LogicalSize, "provider_bytes": item.LogicalSize,
		}).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", attempt.ID).
		Updates(map[string]any{"state": string(AttemptSealed), "is_current": false, "finished_at": finishedAt}).Error; err != nil {
		t.Fatal(err)
	}
	readyAt := finishedAt.Add(-time.Second)
	expiresAt := job.AbsoluteDeadline.UTC().Add(-time.Second)
	if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", job.ID).
		Updates(map[string]any{
			"execution_state": string(ExecutionCancelRequested), "result_kind": string(ResultComplete),
			"ready_at": readyAt, "expires_at": expiresAt, "packed_count": 1,
			"logical_bytes": item.LogicalSize, "provider_bytes": item.LogicalSize,
		}).Error; err != nil {
		t.Fatal(err)
	}
	job = loadFenceAttemptsJob(t, harness.db, job.ID)
	attempt = loadFenceAttemptsAttempt(t, harness.db, attempt.ID)
	itemAttempt = loadFenceAttemptsItemAttempt(t, harness.db, itemAttempt.ID)
	item = loadFenceAttemptsItems(t, harness.db, job.ID)[0]
	port := &PersistentLifecyclePort{db: harness.db, now: harness.service.now, attemptWork: NewAttemptWorkRegistry()}
	marker := t.Name()
	lockOrder := captureFenceAttemptsLockOrder(t, harness.db, marker)

	if err := port.FenceAttempts(context.WithValue(context.Background(), fenceAttemptsTestContextKey{}, marker), job.ID); err != nil {
		t.Fatalf("FenceAttempts sealed cancel-requested lineage: %v", err)
	}
	assertFenceAttemptsLockOrder(t, *lockOrder, "backup_asset_export_jobs", "backup_asset_export_attempts")
	if after := loadFenceAttemptsAttempt(t, harness.db, attempt.ID); !reflect.DeepEqual(after, attempt) {
		t.Fatalf("sealed cancellation mutated attempt observation: before=%+v after=%+v", attempt, after)
	}
	if after := loadFenceAttemptsItemAttempt(t, harness.db, itemAttempt.ID); !reflect.DeepEqual(after, itemAttempt) {
		t.Fatalf("sealed cancellation mutated item-attempt observation: before=%+v after=%+v", itemAttempt, after)
	}
	if after := loadFenceAttemptsItems(t, harness.db, job.ID)[0]; !reflect.DeepEqual(after, item) {
		t.Fatalf("sealed cancellation mutated public item projection: before=%+v after=%+v", item, after)
	}
	assertFenceAttemptsJobCleared(t, job, loadFenceAttemptsJob(t, harness.db, job.ID))
}

func TestPersistentLifecyclePortFenceAttemptsClosesActiveLineageFromLockedJobOutcome(t *testing.T) {
	for _, test := range []struct {
		name                string
		executionState      ExecutionState
		jobCategory         string
		attemptState        AttemptState
		wantAttemptState    AttemptState
		wantFailureCategory string
	}{
		{
			name: "cancel requested", executionState: ExecutionCancelRequested, attemptState: AttemptActive,
			wantAttemptState: AttemptCanceled, wantFailureCategory: "canceled",
		},
		{
			name: "failed internal failure", executionState: ExecutionFailed, jobCategory: "internal_failure",
			attemptState: AttemptSealing, wantAttemptState: AttemptFailed, wantFailureCategory: "internal_failure",
		},
		{
			name: "source expired", executionState: ExecutionSourceExpired, jobCategory: "source_expired",
			attemptState: AttemptActive, wantAttemptState: AttemptFailed, wantFailureCategory: "source_expired",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newServiceHarness(t)
			job, attempt, _ := claimFenceAttemptsTestJob(t, harness)
			fixture := seedFenceAttemptsMixedProjections(t, harness, job, attempt)
			if err := harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", attempt.ID).
				Update("state", string(test.attemptState)).Error; err != nil {
				t.Fatal(err)
			}
			if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", job.ID).
				Updates(map[string]any{
					"execution_state": string(test.executionState), "error_category": test.jobCategory,
				}).Error; err != nil {
				t.Fatal(err)
			}
			job = loadFenceAttemptsJob(t, harness.db, job.ID)
			attempt = loadFenceAttemptsAttempt(t, harness.db, attempt.ID)
			fixture = loadFenceAttemptsMixedFixture(t, harness.db, fixture)
			port := &PersistentLifecyclePort{db: harness.db, now: harness.service.now, attemptWork: NewAttemptWorkRegistry()}
			marker := t.Name()
			lockOrder := captureFenceAttemptsLockOrder(t, harness.db, marker)

			if err := port.FenceAttempts(context.WithValue(context.Background(), fenceAttemptsTestContextKey{}, marker), job.ID); err != nil {
				t.Fatalf("FenceAttempts %s lineage: %v", test.executionState, err)
			}

			afterAttempt := loadFenceAttemptsAttempt(t, harness.db, attempt.ID)
			wantAttempt := attempt
			wantAttempt.State = string(test.wantAttemptState)
			wantAttempt.IsCurrent = false
			wantAttempt.FailureCategory = test.wantFailureCategory
			wantAttempt.CheckpointOrdinal = fixture.maxOrdinal
			wantAttempt.CheckpointItemCount = int64(len(fixture.items))
			wantAttempt.CheckpointLogicalBytes = fixture.logicalBytes
			wantAttempt.CheckpointProviderBytes = fixture.providerBytes
			wantAttempt.FinishedAt = afterAttempt.FinishedAt
			wantAttempt.UpdatedAt = afterAttempt.UpdatedAt
			if afterAttempt.FinishedAt == nil || !reflect.DeepEqual(afterAttempt, wantAttempt) {
				t.Fatalf("attempt terminal projection mismatch: got=%+v want=%+v", afterAttempt, wantAttempt)
			}

			afterItems := loadFenceAttemptsItems(t, harness.db, job.ID)
			afterItemAttempts := loadFenceAttemptsItemAttempts(t, harness.db, job.ID, attempt.ID)
			assertFenceAttemptsMixedProjectionsClosed(t, fixture, afterItems, afterItemAttempts, test.wantFailureCategory)

			afterJob := loadFenceAttemptsJob(t, harness.db, job.ID)
			wantJob := job
			wantJob.CurrentAttemptID = nil
			wantJob.CurrentFenceRevision++
			wantJob.PackedCount = 1
			wantJob.SkippedCount = 0
			wantJob.FailedCount = int64(len(fixture.items) - 1)
			wantJob.LogicalBytes = fixture.logicalBytes
			wantJob.ProviderBytes = fixture.providerBytes
			wantJob.UpdatedAt = afterJob.UpdatedAt
			if !reflect.DeepEqual(afterJob, wantJob) {
				t.Fatalf("job terminal counters mismatch: got=%+v want=%+v", afterJob, wantJob)
			}
			assertFenceAttemptsLockOrder(t, *lockOrder,
				"backup_asset_export_jobs", "backup_asset_export_attempts",
				"backup_asset_export_items", "backup_asset_export_item_attempts")
		})
	}
}

func TestPersistentLifecyclePortFenceAttemptsLocksAndCASesObservedLineage(t *testing.T) {
	t.Run("nil current attempt", func(t *testing.T) {
		harness := newServiceHarness(t)
		job, attempt, itemAttempt := claimFenceAttemptsTestJob(t, harness)
		if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", job.ID).
			Update("current_attempt_id", nil).Error; err != nil {
			t.Fatal(err)
		}
		job = loadFenceAttemptsJob(t, harness.db, job.ID)
		attempt = loadFenceAttemptsAttempt(t, harness.db, attempt.ID)
		itemAttempt = loadFenceAttemptsItemAttempt(t, harness.db, itemAttempt.ID)
		port := &PersistentLifecyclePort{db: harness.db, now: harness.service.now, attemptWork: NewAttemptWorkRegistry()}
		marker := t.Name()
		lockOrder := captureFenceAttemptsLockOrder(t, harness.db, marker)

		err := port.FenceAttempts(context.WithValue(context.Background(), fenceAttemptsTestContextKey{}, marker), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertFenceAttemptsLockOrder(t, *lockOrder, "backup_asset_export_jobs")
		after := loadFenceAttemptsJob(t, harness.db, job.ID)
		if after.CurrentAttemptID != nil || after.CurrentFenceRevision != job.CurrentFenceRevision+1 ||
			after.TransitionRevision != job.TransitionRevision || after.ExecutionState != job.ExecutionState ||
			after.CleanupState != job.CleanupState {
			t.Fatalf("nil-attempt fence changed the wrong job fields: before=%+v after=%+v", job, after)
		}
		if afterAttempt := loadFenceAttemptsAttempt(t, harness.db, attempt.ID); !reflect.DeepEqual(afterAttempt, attempt) {
			t.Fatalf("nil-attempt fence discovered live attempt: before=%+v after=%+v", attempt, afterAttempt)
		}
		if afterItemAttempt := loadFenceAttemptsItemAttempt(t, harness.db, itemAttempt.ID); !reflect.DeepEqual(afterItemAttempt, itemAttempt) {
			t.Fatalf("nil-attempt fence discovered live item attempt: before=%+v after=%+v", itemAttempt, afterItemAttempt)
		}
	})

	t.Run("active attempt with zero unfinished children", func(t *testing.T) {
		harness := newServiceHarness(t)
		job, attempt, itemAttempt := claimFenceAttemptsTestJob(t, harness)
		items := loadFenceAttemptsItems(t, harness.db, job.ID)
		if len(items) != 1 {
			t.Fatalf("public item projection count=%d want=1", len(items))
		}
		finishedAt := harness.service.now().UTC().Add(-time.Second)
		if err := harness.db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", itemAttempt.ID).
			Updates(map[string]any{"state": string(ItemPacked), "packed_at": finishedAt, "finished_at": finishedAt}).Error; err != nil {
			t.Fatal(err)
		}
		if err := harness.db.Model(&model.BackupAssetExportItem{}).Where("id = ?", items[0].ID).
			Update("state", string(ItemPacked)).Error; err != nil {
			t.Fatal(err)
		}
		if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", job.ID).
			Update("execution_state", string(ExecutionCancelRequested)).Error; err != nil {
			t.Fatal(err)
		}
		job = loadFenceAttemptsJob(t, harness.db, job.ID)
		itemAttempt = loadFenceAttemptsItemAttempt(t, harness.db, itemAttempt.ID)
		items = loadFenceAttemptsItems(t, harness.db, job.ID)
		port := &PersistentLifecyclePort{db: harness.db, now: harness.service.now, attemptWork: NewAttemptWorkRegistry()}
		marker := t.Name()
		lockOrder := captureFenceAttemptsLockOrder(t, harness.db, marker)

		if err := port.FenceAttempts(context.WithValue(context.Background(), fenceAttemptsTestContextKey{}, marker), job.ID); err != nil {
			t.Fatal(err)
		}
		assertFenceAttemptsLockOrder(t, *lockOrder,
			"backup_asset_export_jobs", "backup_asset_export_attempts",
			"backup_asset_export_items", "backup_asset_export_item_attempts")
		afterAttempt := loadFenceAttemptsAttempt(t, harness.db, attempt.ID)
		if afterAttempt.State != string(AttemptCanceled) || afterAttempt.IsCurrent ||
			afterAttempt.FailureCategory != "canceled" || afterAttempt.FinishedAt == nil {
			t.Fatalf("active attempt was not fenced: %+v", afterAttempt)
		}
		if afterItem := loadFenceAttemptsItems(t, harness.db, job.ID)[0]; !reflect.DeepEqual(afterItem, items[0]) {
			t.Fatalf("terminal public item projection was mutated: before=%+v after=%+v", items[0], afterItem)
		}
		afterItemAttempt := loadFenceAttemptsItemAttempt(t, harness.db, itemAttempt.ID)
		if !reflect.DeepEqual(afterItemAttempt, itemAttempt) {
			t.Fatalf("terminal item attempt was mutated: before=%+v after=%+v", itemAttempt, afterItemAttempt)
		}
		afterJob := loadFenceAttemptsJob(t, harness.db, job.ID)
		wantJob := job
		wantJob.CurrentAttemptID = nil
		wantJob.CurrentFenceRevision++
		wantJob.PackedCount = 1
		wantJob.UpdatedAt = afterJob.UpdatedAt
		if !reflect.DeepEqual(afterJob, wantJob) {
			t.Fatalf("zero-unfinished job projection mismatch: got=%+v want=%+v", afterJob, wantJob)
		}
	})

	t.Run("current sealing attempt", func(t *testing.T) {
		harness := newServiceHarness(t)
		job, attempt, itemAttempt := claimFenceAttemptsTestJob(t, harness)
		if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", job.ID).
			Update("execution_state", string(ExecutionCancelRequested)).Error; err != nil {
			t.Fatal(err)
		}
		if err := harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", attempt.ID).
			Update("state", string(AttemptSealing)).Error; err != nil {
			t.Fatal(err)
		}
		job = loadFenceAttemptsJob(t, harness.db, job.ID)
		port := &PersistentLifecyclePort{db: harness.db, now: harness.service.now, attemptWork: NewAttemptWorkRegistry()}
		marker := t.Name()
		lockOrder := captureFenceAttemptsLockOrder(t, harness.db, marker)

		if err := port.FenceAttempts(context.WithValue(context.Background(), fenceAttemptsTestContextKey{}, marker), job.ID); err != nil {
			t.Fatal(err)
		}
		assertFenceAttemptsLockOrder(t, *lockOrder,
			"backup_asset_export_jobs", "backup_asset_export_attempts",
			"backup_asset_export_items", "backup_asset_export_item_attempts")
		afterAttempt := loadFenceAttemptsAttempt(t, harness.db, attempt.ID)
		if afterAttempt.State != string(AttemptCanceled) || afterAttempt.IsCurrent ||
			afterAttempt.FailureCategory != "canceled" || afterAttempt.FinishedAt == nil {
			t.Fatalf("sealing attempt was not fenced: %+v", afterAttempt)
		}
		afterItemAttempt := loadFenceAttemptsItemAttempt(t, harness.db, itemAttempt.ID)
		if afterItemAttempt.State != string(ItemFailed) || afterItemAttempt.ErrorCategory != "canceled" ||
			afterItemAttempt.FinishedAt == nil {
			t.Fatalf("sealing item attempt was not fenced: %+v", afterItemAttempt)
		}
		afterJob := loadFenceAttemptsJob(t, harness.db, job.ID)
		wantJob := job
		wantJob.CurrentAttemptID = nil
		wantJob.CurrentFenceRevision++
		wantJob.FailedCount = 1
		wantJob.UpdatedAt = afterJob.UpdatedAt
		if !reflect.DeepEqual(afterJob, wantJob) {
			t.Fatalf("sealing job projection mismatch: got=%+v want=%+v", afterJob, wantJob)
		}
	})

	t.Run("unfinished children use bounded update batches", func(t *testing.T) {
		harness := newServiceHarness(t)
		job, attempt, _ := claimFenceAttemptsTestJob(t, harness)
		createFenceAttemptsAdditionalPendingRows(t, harness.db, job, attempt, fenceAttemptsTestMaxItemUpdateBatchSize)
		if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", job.ID).
			Updates(map[string]any{
				"execution_state": string(ExecutionCancelRequested),
				"item_count":      fenceAttemptsTestMaxItemUpdateBatchSize + 1,
			}).Error; err != nil {
			t.Fatal(err)
		}
		port := &PersistentLifecyclePort{db: harness.db, now: harness.service.now, attemptWork: NewAttemptWorkRegistry()}
		marker := t.Name()
		lockOrder := captureFenceAttemptsLockOrder(t, harness.db, marker)
		bindCeilingErr := errors.New("FenceAttempts child update exceeded test bind ceiling")
		publicItemUpdateBatchSizes := make([]int, 0, 2)
		itemAttemptUpdateBatchSizes := make([]int, 0, 2)
		callbackName := "test:enforce_fence_attempts_item_update_bind_ceiling"
		if err := harness.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement == nil || tx.Statement.Schema == nil ||
				tx.Statement.Context.Value(fenceAttemptsTestContextKey{}) != marker {
				return
			}
			table := tx.Statement.Schema.Table
			if table != "backup_asset_export_items" && table != "backup_asset_export_item_attempts" {
				return
			}
			batchSize := fenceAttemptsIDBatchBindSize(tx.Statement.Clauses["WHERE"].Expression)
			if batchSize == 0 {
				_ = tx.AddError(fmt.Errorf("FenceAttempts %s update omitted its ID batch", table))
				return
			}
			if table == "backup_asset_export_items" {
				publicItemUpdateBatchSizes = append(publicItemUpdateBatchSizes, batchSize)
			} else {
				itemAttemptUpdateBatchSizes = append(itemAttemptUpdateBatchSizes, batchSize)
			}
			if batchSize > fenceAttemptsTestMaxItemUpdateBatchSize {
				_ = tx.AddError(bindCeilingErr)
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := harness.db.Callback().Update().Remove(callbackName); err != nil {
				t.Errorf("remove FenceAttempts bind-ceiling callback: %v", err)
			}
		})

		if err := port.FenceAttempts(context.WithValue(context.Background(), fenceAttemptsTestContextKey{}, marker), job.ID); err != nil {
			t.Fatalf("FenceAttempts bounded item updates: %v", err)
		}
		assertFenceAttemptsLockOrder(t, *lockOrder,
			"backup_asset_export_jobs", "backup_asset_export_attempts",
			"backup_asset_export_items", "backup_asset_export_item_attempts")
		wantBatches := []int{fenceAttemptsTestMaxItemUpdateBatchSize, 1}
		if !reflect.DeepEqual(publicItemUpdateBatchSizes, wantBatches) {
			t.Fatalf("FenceAttempts public-item update batches=%v want=%v", publicItemUpdateBatchSizes, wantBatches)
		}
		if !reflect.DeepEqual(itemAttemptUpdateBatchSizes, wantBatches) {
			t.Fatalf("FenceAttempts item-attempt update batches=%v want=%v", itemAttemptUpdateBatchSizes, wantBatches)
		}
		items := loadFenceAttemptsItems(t, harness.db, job.ID)
		if len(items) != fenceAttemptsTestMaxItemUpdateBatchSize+1 {
			t.Fatalf("FenceAttempts public item count=%d want=%d", len(items), fenceAttemptsTestMaxItemUpdateBatchSize+1)
		}
		for _, item := range items {
			if item.State != string(ItemFailed) || item.ErrorCategory != "canceled" {
				t.Fatalf("FenceAttempts left unfinished public item projection: %+v", item)
			}
		}
		itemAttempts := loadFenceAttemptsItemAttempts(t, harness.db, job.ID, attempt.ID)
		if len(itemAttempts) != fenceAttemptsTestMaxItemUpdateBatchSize+1 {
			t.Fatalf("FenceAttempts item-attempt count=%d want=%d", len(itemAttempts), fenceAttemptsTestMaxItemUpdateBatchSize+1)
		}
		for _, itemAttempt := range itemAttempts {
			if itemAttempt.State != string(ItemFailed) || itemAttempt.ErrorCategory != "canceled" || itemAttempt.FinishedAt == nil {
				t.Fatalf("FenceAttempts left unfinished item attempt: %+v", itemAttempt)
			}
		}
	})

	for _, test := range []struct {
		name   string
		table  string
		inject func(*gorm.DB, model.BackupAssetExportItem, model.BackupAssetExportAttempt, model.BackupAssetExportItemAttempt, time.Time) error
	}{
		{
			name:  "public item update CAS miss rolls back",
			table: "backup_asset_export_items",
			inject: func(tx *gorm.DB, item model.BackupAssetExportItem, _ model.BackupAssetExportAttempt, _ model.BackupAssetExportItemAttempt, now time.Time) error {
				return tx.Exec(`UPDATE backup_asset_export_items
					SET state = ?, error_category = ?, updated_at = ? WHERE id = ?`,
					string(ItemFailed), "injected_public_item_drift", now, item.ID).Error
			},
		},
		{
			name:  "child update CAS miss rolls back",
			table: "backup_asset_export_item_attempts",
			inject: func(tx *gorm.DB, _ model.BackupAssetExportItem, _ model.BackupAssetExportAttempt, itemAttempt model.BackupAssetExportItemAttempt, now time.Time) error {
				return tx.Exec(`UPDATE backup_asset_export_item_attempts
					SET state = ?, error_category = ?, finished_at = ? WHERE id = ?`,
					string(ItemFailed), "injected_child_drift", now, itemAttempt.ID).Error
			},
		},
		{
			name:  "attempt update CAS miss rolls back",
			table: "backup_asset_export_attempts",
			inject: func(tx *gorm.DB, _ model.BackupAssetExportItem, attempt model.BackupAssetExportAttempt, _ model.BackupAssetExportItemAttempt, now time.Time) error {
				return tx.Exec(`UPDATE backup_asset_export_attempts
					SET state = ?, is_current = ?, failure_category = ?, finished_at = ?, updated_at = ? WHERE id = ?`,
					string(AttemptFailed), false, "injected_attempt_drift", now, now, attempt.ID).Error
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newServiceHarness(t)
			job, attempt, itemAttempt := claimFenceAttemptsTestJob(t, harness)
			if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", job.ID).
				Update("execution_state", string(ExecutionCancelRequested)).Error; err != nil {
				t.Fatal(err)
			}
			job = loadFenceAttemptsJob(t, harness.db, job.ID)
			beforePublicItems := loadFenceAttemptsItems(t, harness.db, job.ID)
			if len(beforePublicItems) != 1 {
				t.Fatalf("public item projection count=%d want=1", len(beforePublicItems))
			}
			beforeItemAttempts := loadFenceAttemptsItemAttempts(t, harness.db, job.ID, attempt.ID)
			port := &PersistentLifecyclePort{db: harness.db, now: harness.service.now, attemptWork: NewAttemptWorkRegistry()}
			marker := t.Name()
			lockOrder := captureFenceAttemptsLockOrder(t, harness.db, marker)
			var injected atomic.Bool
			callbackName := "test:inject_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
			if err := harness.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != test.table ||
					tx.Statement.Context.Value(fenceAttemptsTestContextKey{}) != marker ||
					!injected.CompareAndSwap(false, true) {
					return
				}
				if err := test.inject(tx, beforePublicItems[0], attempt, itemAttempt, harness.service.now().UTC().Add(time.Second)); err != nil {
					_ = tx.AddError(err)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := harness.db.Callback().Update().Remove(callbackName); err != nil {
					t.Errorf("remove FenceAttempts CAS-miss callback: %v", err)
				}
			})

			err := port.FenceAttempts(context.WithValue(context.Background(), fenceAttemptsTestContextKey{}, marker), job.ID)
			if !errors.Is(err, ErrAttemptFenceLost) {
				t.Fatalf("FenceAttempts CAS-miss error=%v want ErrAttemptFenceLost", err)
			}
			if !injected.Load() {
				t.Fatal("FenceAttempts CAS drift was not injected")
			}
			assertFenceAttemptsLockOrder(t, *lockOrder,
				"backup_asset_export_jobs", "backup_asset_export_attempts",
				"backup_asset_export_items", "backup_asset_export_item_attempts")
			assertFenceAttemptsRollback(t, harness.db, job, attempt, beforePublicItems, beforeItemAttempts)
		})
	}

	for _, state := range []ExecutionState{ExecutionReady, ExecutionExpiring} {
		state := state
		t.Run("published lineage "+string(state), func(t *testing.T) {
			harness := newServiceHarness(t)
			job, attempt, itemAttempt := claimFenceAttemptsTestJob(t, harness)
			items := loadFenceAttemptsItems(t, harness.db, job.ID)
			if len(items) != 1 {
				t.Fatalf("published public item count=%d want=1", len(items))
			}
			finishedAt := harness.service.now().UTC().Add(-time.Second)
			if err := harness.db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", itemAttempt.ID).
				Updates(map[string]any{
					"state": string(ItemPacked), "logical_bytes": items[0].LogicalSize,
					"provider_bytes": items[0].LogicalSize, "packed_at": finishedAt, "finished_at": finishedAt,
				}).Error; err != nil {
				t.Fatal(err)
			}
			if err := harness.db.Model(&model.BackupAssetExportItem{}).Where("id = ?", items[0].ID).
				Updates(map[string]any{
					"state": string(ItemPacked), "logical_bytes": items[0].LogicalSize,
					"provider_bytes": items[0].LogicalSize,
				}).Error; err != nil {
				t.Fatal(err)
			}
			if err := harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", attempt.ID).
				Updates(map[string]any{"state": string(AttemptSealed), "is_current": false, "finished_at": finishedAt}).Error; err != nil {
				t.Fatal(err)
			}
			if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", job.ID).
				Update("execution_state", string(state)).Error; err != nil {
				t.Fatal(err)
			}
			job = loadFenceAttemptsJob(t, harness.db, job.ID)
			attempt = loadFenceAttemptsAttempt(t, harness.db, attempt.ID)
			itemAttempt = loadFenceAttemptsItemAttempt(t, harness.db, itemAttempt.ID)
			items = loadFenceAttemptsItems(t, harness.db, job.ID)
			port := &PersistentLifecyclePort{db: harness.db, now: harness.service.now, attemptWork: NewAttemptWorkRegistry()}
			marker := t.Name()
			lockOrder := captureFenceAttemptsLockOrder(t, harness.db, marker)

			if err := port.FenceAttempts(context.WithValue(context.Background(), fenceAttemptsTestContextKey{}, marker), job.ID); err != nil {
				t.Fatal(err)
			}
			assertFenceAttemptsLockOrder(t, *lockOrder, "backup_asset_export_jobs", "backup_asset_export_attempts")
			if after := loadFenceAttemptsAttempt(t, harness.db, attempt.ID); !reflect.DeepEqual(after, attempt) {
				t.Fatalf("published attempt mutated: before=%+v after=%+v", attempt, after)
			}
			if after := loadFenceAttemptsItemAttempt(t, harness.db, itemAttempt.ID); !reflect.DeepEqual(after, itemAttempt) {
				t.Fatalf("published item attempt mutated: before=%+v after=%+v", itemAttempt, after)
			}
			if after := loadFenceAttemptsItems(t, harness.db, job.ID); !reflect.DeepEqual(after, items) {
				t.Fatalf("published public item projection mutated: before=%+v after=%+v", items, after)
			}
			assertFenceAttemptsJobCleared(t, job, loadFenceAttemptsJob(t, harness.db, job.ID))
		})
	}

	for _, test := range []struct {
		name  string
		setup func(*testing.T, serviceHarness, model.BackupAssetExportJob, model.BackupAssetExportAttempt)
	}{
		{
			name: "missing pointed attempt",
			setup: func(t *testing.T, harness serviceHarness, job model.BackupAssetExportJob, _ model.BackupAssetExportAttempt) {
				t.Helper()
				missingID := strings.Repeat("9", 32)
				if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", job.ID).
					Update("current_attempt_id", missingID).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "non-current active pointed attempt",
			setup: func(t *testing.T, harness serviceHarness, _ model.BackupAssetExportJob, attempt model.BackupAssetExportAttempt) {
				t.Helper()
				if err := harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", attempt.ID).
					Update("is_current", false).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unsupported sealed lineage",
			setup: func(t *testing.T, harness serviceHarness, _ model.BackupAssetExportJob, attempt model.BackupAssetExportAttempt) {
				t.Helper()
				finishedAt := harness.service.now().UTC()
				if err := harness.db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", attempt.ID).
					Updates(map[string]any{"state": string(AttemptSealed), "is_current": false, "finished_at": finishedAt}).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newServiceHarness(t)
			job, attempt, itemAttempt := claimFenceAttemptsTestJob(t, harness)
			test.setup(t, harness, job, attempt)
			job = loadFenceAttemptsJob(t, harness.db, job.ID)
			attempt = loadFenceAttemptsAttempt(t, harness.db, attempt.ID)
			itemAttempt = loadFenceAttemptsItemAttempt(t, harness.db, itemAttempt.ID)
			port := &PersistentLifecyclePort{db: harness.db, now: harness.service.now, attemptWork: NewAttemptWorkRegistry()}
			marker := t.Name()
			lockOrder := captureFenceAttemptsLockOrder(t, harness.db, marker)

			err := port.FenceAttempts(context.WithValue(context.Background(), fenceAttemptsTestContextKey{}, marker), job.ID)
			if !errors.Is(err, ErrAttemptFenceLost) {
				t.Fatalf("FenceAttempts error=%v want ErrAttemptFenceLost", err)
			}
			assertFenceAttemptsLockOrder(t, *lockOrder, "backup_asset_export_jobs", "backup_asset_export_attempts")
			if after := loadFenceAttemptsJob(t, harness.db, job.ID); !reflect.DeepEqual(after, job) {
				t.Fatalf("rejected lineage mutated job: before=%+v after=%+v", job, after)
			}
			if after := loadFenceAttemptsAttempt(t, harness.db, attempt.ID); !reflect.DeepEqual(after, attempt) {
				t.Fatalf("rejected lineage mutated attempt: before=%+v after=%+v", attempt, after)
			}
			if after := loadFenceAttemptsItemAttempt(t, harness.db, itemAttempt.ID); !reflect.DeepEqual(after, itemAttempt) {
				t.Fatalf("rejected lineage mutated item attempt: before=%+v after=%+v", itemAttempt, after)
			}
		})
	}

	t.Run("pointed attempt belongs to another job", func(t *testing.T) {
		harness := newServiceHarness(t)
		job, ownAttempt, ownItemAttempt := claimFenceAttemptsTestJob(t, harness)
		otherJob, otherAttempt, otherItemAttempt := claimFenceAttemptsTestJob(t, harness, "other")
		if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", job.ID).
			Update("current_attempt_id", otherAttempt.ID).Error; err != nil {
			t.Fatal(err)
		}
		job = loadFenceAttemptsJob(t, harness.db, job.ID)
		ownAttempt = loadFenceAttemptsAttempt(t, harness.db, ownAttempt.ID)
		ownItemAttempt = loadFenceAttemptsItemAttempt(t, harness.db, ownItemAttempt.ID)
		otherJob = loadFenceAttemptsJob(t, harness.db, otherJob.ID)
		otherAttempt = loadFenceAttemptsAttempt(t, harness.db, otherAttempt.ID)
		otherItemAttempt = loadFenceAttemptsItemAttempt(t, harness.db, otherItemAttempt.ID)
		port := &PersistentLifecyclePort{db: harness.db, now: harness.service.now, attemptWork: NewAttemptWorkRegistry()}
		marker := t.Name()
		lockOrder := captureFenceAttemptsLockOrder(t, harness.db, marker)

		err := port.FenceAttempts(context.WithValue(context.Background(), fenceAttemptsTestContextKey{}, marker), job.ID)
		if !errors.Is(err, ErrAttemptFenceLost) {
			t.Fatalf("FenceAttempts foreign-job pointer error=%v want ErrAttemptFenceLost", err)
		}
		assertFenceAttemptsLockOrder(t, *lockOrder, "backup_asset_export_jobs", "backup_asset_export_attempts")
		if after := loadFenceAttemptsJob(t, harness.db, job.ID); !reflect.DeepEqual(after, job) {
			t.Fatalf("foreign-job pointer mutated job: before=%+v after=%+v", job, after)
		}
		if after := loadFenceAttemptsAttempt(t, harness.db, ownAttempt.ID); !reflect.DeepEqual(after, ownAttempt) {
			t.Fatalf("foreign-job pointer mutated own attempt: before=%+v after=%+v", ownAttempt, after)
		}
		if after := loadFenceAttemptsItemAttempt(t, harness.db, ownItemAttempt.ID); !reflect.DeepEqual(after, ownItemAttempt) {
			t.Fatalf("foreign-job pointer mutated own item attempt: before=%+v after=%+v", ownItemAttempt, after)
		}
		if after := loadFenceAttemptsJob(t, harness.db, otherJob.ID); !reflect.DeepEqual(after, otherJob) {
			t.Fatalf("foreign-job pointer mutated other job: before=%+v after=%+v", otherJob, after)
		}
		if after := loadFenceAttemptsAttempt(t, harness.db, otherAttempt.ID); !reflect.DeepEqual(after, otherAttempt) {
			t.Fatalf("foreign-job pointer mutated other attempt: before=%+v after=%+v", otherAttempt, after)
		}
		if after := loadFenceAttemptsItemAttempt(t, harness.db, otherItemAttempt.ID); !reflect.DeepEqual(after, otherItemAttempt) {
			t.Fatalf("foreign-job pointer mutated other item attempt: before=%+v after=%+v", otherItemAttempt, after)
		}
	})

	for _, mutation := range []struct {
		name string
		sql  string
	}{
		{name: "fence revision", sql: "current_fence_revision = current_fence_revision + 1"},
		{name: "transition revision", sql: "transition_revision = transition_revision + 1"},
		{name: "attempt pointer", sql: "current_attempt_id = NULL"},
	} {
		t.Run("final job CAS rejects "+mutation.name+" drift", func(t *testing.T) {
			harness := newServiceHarness(t)
			job, attempt, itemAttempt := claimFenceAttemptsTestJob(t, harness)
			if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", job.ID).
				Update("execution_state", string(ExecutionCancelRequested)).Error; err != nil {
				t.Fatal(err)
			}
			job = loadFenceAttemptsJob(t, harness.db, job.ID)
			items := loadFenceAttemptsItems(t, harness.db, job.ID)
			port := &PersistentLifecyclePort{db: harness.db, now: harness.service.now, attemptWork: NewAttemptWorkRegistry()}
			marker := t.Name()
			lockOrder := captureFenceAttemptsLockOrder(t, harness.db, marker)
			var injected atomic.Bool
			callbackName := "test:inject_fence_attempts_final_job_cas_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
			if err := harness.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement == nil || tx.Statement.Schema == nil ||
					tx.Statement.Schema.Table != "backup_asset_export_jobs" ||
					tx.Statement.Context.Value(fenceAttemptsTestContextKey{}) != marker ||
					!injected.CompareAndSwap(false, true) {
					return
				}
				if err := tx.Exec("UPDATE backup_asset_export_jobs SET "+mutation.sql+" WHERE id = ?", job.ID).Error; err != nil {
					_ = tx.AddError(err)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := harness.db.Callback().Update().Remove(callbackName); err != nil {
					t.Errorf("remove FenceAttempts final Job CAS callback: %v", err)
				}
			})

			err := port.FenceAttempts(context.WithValue(context.Background(), fenceAttemptsTestContextKey{}, marker), job.ID)
			if !errors.Is(err, ErrAttemptFenceLost) {
				t.Fatalf("FenceAttempts error=%v want ErrAttemptFenceLost", err)
			}
			if !injected.Load() {
				t.Fatal("final Job CAS drift was not injected")
			}
			assertFenceAttemptsLockOrder(t, *lockOrder,
				"backup_asset_export_jobs", "backup_asset_export_attempts",
				"backup_asset_export_items", "backup_asset_export_item_attempts")
			if after := loadFenceAttemptsJob(t, harness.db, job.ID); !reflect.DeepEqual(after, job) {
				t.Fatalf("failed final Job CAS did not roll back job: before=%+v after=%+v", job, after)
			}
			if after := loadFenceAttemptsAttempt(t, harness.db, attempt.ID); !reflect.DeepEqual(after, attempt) {
				t.Fatalf("failed final Job CAS did not roll back attempt: before=%+v after=%+v", attempt, after)
			}
			if after := loadFenceAttemptsItemAttempt(t, harness.db, itemAttempt.ID); !reflect.DeepEqual(after, itemAttempt) {
				t.Fatalf("failed final Job CAS did not roll back item attempt: before=%+v after=%+v", itemAttempt, after)
			}
			if after := loadFenceAttemptsItems(t, harness.db, job.ID); !reflect.DeepEqual(after, items) {
				t.Fatalf("failed final Job CAS did not roll back public items: before=%+v after=%+v", items, after)
			}
		})
	}
}

func commitFenceAttemptsTestJob(
	t *testing.T, harness serviceHarness, keySuffix ...string,
) model.BackupAssetExportJob {
	t.Helper()
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	idempotencyMaterial := t.Name() + "\x00" + strings.Join(keySuffix, "\x00")
	idempotencyDigest := sha256.Sum256([]byte(idempotencyMaterial))
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 42, Role: "admin"}, Selection: selection,
		IdempotencyKey: fmt.Sprintf("fence-attempts-%x", idempotencyDigest),
		ArchiveFormat:  ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return loadFenceAttemptsJob(t, harness.db, created.JobID)
}

func claimFenceAttemptsTestJob(
	t *testing.T, harness serviceHarness, keySuffix ...string,
) (model.BackupAssetExportJob, model.BackupAssetExportAttempt, model.BackupAssetExportItemAttempt) {
	t.Helper()
	job := commitFenceAttemptsTestJob(t, harness, keySuffix...)
	coordinator, err := NewAttemptCoordinator(harness.db, harness.service.now)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: job.ID, WorkerOwner: "fence-worker"})
	if err != nil {
		t.Fatal(err)
	}
	job = loadFenceAttemptsJob(t, harness.db, job.ID)
	attempt := loadFenceAttemptsAttempt(t, harness.db, claim.AttemptID)
	var itemAttempt model.BackupAssetExportItemAttempt
	if err := harness.db.Where("job_id = ? AND attempt_id = ?", job.ID, attempt.ID).Take(&itemAttempt).Error; err != nil {
		t.Fatal(err)
	}
	return job, attempt, itemAttempt
}

type fenceAttemptsMixedFixture struct {
	jobID         string
	attemptID     string
	items         []model.BackupAssetExportItem
	itemAttempts  []model.BackupAssetExportItemAttempt
	maxOrdinal    int
	logicalBytes  int64
	providerBytes int64
}

func seedFenceAttemptsMixedProjections(
	t *testing.T,
	harness serviceHarness,
	job model.BackupAssetExportJob,
	attempt model.BackupAssetExportAttempt,
) fenceAttemptsMixedFixture {
	t.Helper()
	createFenceAttemptsAdditionalPendingRows(t, harness.db, job, attempt, 2)
	items := loadFenceAttemptsItems(t, harness.db, job.ID)
	if len(items) != 3 {
		t.Fatalf("mixed projection item count=%d want=3", len(items))
	}
	if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", job.ID).
		Update("item_count", len(items)).Error; err != nil {
		t.Fatal(err)
	}
	itemAttempts := loadFenceAttemptsItemAttempts(t, harness.db, job.ID, attempt.ID)
	byItem := make(map[string]model.BackupAssetExportItemAttempt, len(itemAttempts))
	for _, row := range itemAttempts {
		byItem[row.ItemID] = row
	}
	packedItem, readItem := items[0], items[1]
	packedAttempt, packedOK := byItem[packedItem.ID]
	readAttempt, readOK := byItem[readItem.ID]
	if !packedOK || !readOK {
		t.Fatalf("mixed projection lineage incomplete: items=%+v item_attempts=%+v", items, itemAttempts)
	}
	finishedAt := harness.service.now().UTC().Add(-2 * time.Second)
	readAt := finishedAt.Add(time.Second)
	if err := harness.db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", packedAttempt.ID).
		Updates(map[string]any{
			"state": string(ItemPacked), "logical_bytes": packedItem.LogicalSize,
			"provider_bytes": packedItem.LogicalSize, "packed_at": finishedAt, "finished_at": finishedAt,
		}).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetExportItem{}).Where("id = ?", packedItem.ID).
		Updates(map[string]any{
			"state": string(ItemPacked), "logical_bytes": packedItem.LogicalSize,
			"provider_bytes": packedItem.LogicalSize,
		}).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetExportItemAttempt{}).Where("id = ?", readAttempt.ID).
		Updates(map[string]any{
			"state": string(ItemRead), "spool_digest": strings.Repeat("a", 64), "spool_size": 128,
			"spool_locator": strings.Repeat("b", 32) + ".xrs", "logical_bytes": readItem.LogicalSize,
			"provider_bytes": readItem.LogicalSize, "read_at": readAt,
		}).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetExportItem{}).Where("id = ?", readItem.ID).
		Updates(map[string]any{
			"state": string(ItemRead), "logical_bytes": readItem.LogicalSize,
			"provider_bytes": readItem.LogicalSize,
		}).Error; err != nil {
		t.Fatal(err)
	}
	return loadFenceAttemptsMixedFixture(t, harness.db, fenceAttemptsMixedFixture{
		jobID: job.ID, attemptID: attempt.ID,
	})
}

func loadFenceAttemptsMixedFixture(
	t *testing.T, db *gorm.DB, fixture fenceAttemptsMixedFixture,
) fenceAttemptsMixedFixture {
	t.Helper()
	fixture.items = loadFenceAttemptsItems(t, db, fixture.jobID)
	fixture.itemAttempts = loadFenceAttemptsItemAttempts(t, db, fixture.jobID, fixture.attemptID)
	fixture.maxOrdinal = 0
	fixture.logicalBytes = 0
	fixture.providerBytes = 0
	for _, item := range fixture.items {
		if item.Ordinal > fixture.maxOrdinal {
			fixture.maxOrdinal = item.Ordinal
		}
	}
	for _, itemAttempt := range fixture.itemAttempts {
		fixture.logicalBytes += itemAttempt.LogicalBytes
		fixture.providerBytes += itemAttempt.ProviderBytes
	}
	return fixture
}

func assertFenceAttemptsMixedProjectionsClosed(
	t *testing.T,
	before fenceAttemptsMixedFixture,
	afterItems []model.BackupAssetExportItem,
	afterItemAttempts []model.BackupAssetExportItemAttempt,
	category string,
) {
	t.Helper()
	itemsByID := make(map[string]model.BackupAssetExportItem, len(afterItems))
	for _, row := range afterItems {
		itemsByID[row.ID] = row
	}
	for _, row := range before.items {
		after, ok := itemsByID[row.ID]
		if !ok {
			t.Fatalf("public item projection disappeared: %+v", row)
		}
		if ItemState(row.State) == ItemPacked {
			if !reflect.DeepEqual(after, row) {
				t.Fatalf("packed public item projection mutated: before=%+v after=%+v", row, after)
			}
			continue
		}
		want := row
		want.State = string(ItemFailed)
		want.ErrorCategory = category
		want.UpdatedAt = after.UpdatedAt
		if !reflect.DeepEqual(after, want) {
			t.Fatalf("unfinished public item projection mismatch: got=%+v want=%+v", after, want)
		}
	}

	itemAttemptsByID := make(map[string]model.BackupAssetExportItemAttempt, len(afterItemAttempts))
	for _, row := range afterItemAttempts {
		itemAttemptsByID[row.ID] = row
	}
	for _, row := range before.itemAttempts {
		after, ok := itemAttemptsByID[row.ID]
		if !ok {
			t.Fatalf("immutable item-attempt observation disappeared: %+v", row)
		}
		if ItemState(row.State) == ItemPacked {
			if !reflect.DeepEqual(after, row) {
				t.Fatalf("packed immutable item-attempt observation mutated: before=%+v after=%+v", row, after)
			}
			continue
		}
		want := row
		want.State = string(ItemFailed)
		want.ErrorCategory = category
		want.FinishedAt = after.FinishedAt
		if after.FinishedAt == nil || !reflect.DeepEqual(after, want) {
			t.Fatalf("unfinished immutable item-attempt observation mismatch: got=%+v want=%+v", after, want)
		}
	}
}

func createFenceAttemptsAdditionalPendingRows(
	t *testing.T,
	db *gorm.DB,
	job model.BackupAssetExportJob,
	attempt model.BackupAssetExportAttempt,
	count int,
) {
	t.Helper()
	var baseItem model.BackupAssetExportItem
	if err := db.Where("job_id = ?", job.ID).Take(&baseItem).Error; err != nil {
		t.Fatal(err)
	}
	var baseItemAttempt model.BackupAssetExportItemAttempt
	if err := db.Where("job_id = ? AND attempt_id = ?", job.ID, attempt.ID).Take(&baseItemAttempt).Error; err != nil {
		t.Fatal(err)
	}
	items := make([]model.BackupAssetExportItem, 0, count)
	itemAttempts := make([]model.BackupAssetExportItemAttempt, 0, count)
	currentAttemptID := attempt.ID
	for index := 0; index < count; index++ {
		itemID, err := backupasset.NewOpaqueID()
		if err != nil {
			t.Fatal(err)
		}
		itemAttemptID, err := backupasset.NewOpaqueID()
		if err != nil {
			t.Fatal(err)
		}
		item := baseItem
		item.ID = itemID
		item.Ordinal = index + 1
		item.EntryID = itemID + itemID
		item.CurrentAttemptID = &currentAttemptID
		items = append(items, item)

		itemAttempt := baseItemAttempt
		itemAttempt.ID = itemAttemptID
		itemAttempt.ItemID = itemID
		itemAttempt.State = string(ItemPending)
		itemAttempt.ErrorCategory = ""
		itemAttempt.ReadAt = nil
		itemAttempt.PackedAt = nil
		itemAttempt.FinishedAt = nil
		itemAttempts = append(itemAttempts, itemAttempt)
	}
	if err := db.CreateInBatches(&items, 50).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.CreateInBatches(&itemAttempts, 50).Error; err != nil {
		t.Fatal(err)
	}
}

func captureFenceAttemptsLockOrder(
	t *testing.T, db *gorm.DB, marker string,
) *[]fenceAttemptsLockObservation {
	t.Helper()
	order := make([]fenceAttemptsLockObservation, 0, 4)
	callbackName := "test:capture_fence_attempts_lock_order_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Context.Value(fenceAttemptsTestContextKey{}) != marker {
			return
		}
		lockingClause, locked := tx.Statement.Clauses["FOR"]
		if !locked {
			return
		}
		table := tx.Statement.Table
		if tx.Statement.Schema != nil {
			table = tx.Statement.Schema.Table
		}
		observation := fenceAttemptsLockObservation{table: table}
		if locking, ok := lockingClause.Expression.(clause.Locking); ok {
			observation.itemAttemptLockForUpdate = strings.EqualFold(locking.Strength, "UPDATE")
		}
		if table == "backup_asset_export_item_attempts" {
			observation.itemAttemptOrdered = fenceAttemptsItemLockOrdered(tx.Statement.Clauses["ORDER BY"].Expression)
			observation.itemAttemptScoped = fenceAttemptsItemLockScoped(tx.Statement.Clauses["WHERE"].Expression)
			observation.itemAttemptSelectsOnlyID = len(tx.Statement.Selects) == 1 &&
				strings.EqualFold(strings.Trim(tx.Statement.Selects[0], "`\""), "id")
		}
		if table == "backup_asset_export_items" {
			observation.itemProjectionOrdered = fenceAttemptsLockOrdered(
				tx.Statement.Clauses["ORDER BY"].Expression, []string{"ordinal", "id"},
			)
			observation.itemProjectionScoped = fenceAttemptsLockScoped(
				tx.Statement.Clauses["WHERE"].Expression, "current_attempt_id",
			)
			observation.itemProjectionSelectsID = len(tx.Statement.Selects) == 1 &&
				strings.EqualFold(strings.Trim(tx.Statement.Selects[0], "`\""), "id")
		}
		order = append(order, observation)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove FenceAttempts lock-order callback: %v", err)
		}
	})
	return &order
}

func assertFenceAttemptsLockOrder(t *testing.T, got []fenceAttemptsLockObservation, want ...string) {
	t.Helper()
	tables := make([]string, 0, len(got))
	for _, observation := range got {
		tables = append(tables, observation.table)
		if !observation.itemAttemptLockForUpdate {
			t.Fatalf("FenceAttempts %s lock was not FOR UPDATE: %+v", observation.table, observation)
		}
		if observation.table == "backup_asset_export_item_attempts" &&
			(!observation.itemAttemptOrdered || !observation.itemAttemptScoped || !observation.itemAttemptSelectsOnlyID) {
			t.Fatalf("FenceAttempts item-attempt lock lacks deterministic order, scope, or ID-only projection: %+v", observation)
		}
		if observation.table == "backup_asset_export_items" &&
			(!observation.itemProjectionOrdered || !observation.itemProjectionScoped || !observation.itemProjectionSelectsID) {
			t.Fatalf("FenceAttempts public-item lock lacks deterministic order, scope, or ID-only projection: %+v", observation)
		}
	}
	if !reflect.DeepEqual(tables, want) {
		t.Fatalf("FenceAttempts FOR UPDATE order=%v want=%v", tables, want)
	}
}

func fenceAttemptsItemLockOrdered(expression clause.Expression) bool {
	return fenceAttemptsLockOrdered(expression, []string{"item_id", "id"})
}

func fenceAttemptsLockOrdered(expression clause.Expression, want []string) bool {
	orderBy, ok := expression.(clause.OrderBy)
	if !ok {
		return false
	}
	columns := make([]string, 0, 2)
	for _, ordered := range orderBy.Columns {
		if ordered.Desc {
			return false
		}
		for _, rawTerm := range strings.Split(ordered.Column.Name, ",") {
			fields := strings.Fields(strings.ToLower(strings.TrimSpace(rawTerm)))
			if len(fields) == 0 || len(fields) > 2 || len(fields) == 2 && fields[1] != "asc" {
				return false
			}
			columns = append(columns, strings.Trim(fields[0], "`\""))
		}
	}
	return reflect.DeepEqual(columns, want)
}

func fenceAttemptsItemLockScoped(expression clause.Expression) bool {
	return fenceAttemptsLockScoped(expression, "attempt_id")
}

func fenceAttemptsLockScoped(expression clause.Expression, attemptColumn string) bool {
	sql := strings.ToLower(fenceAttemptsClauseSQL(expression))
	return strings.Contains(sql, "job_id") && strings.Contains(sql, attemptColumn) &&
		strings.Contains(sql, "state") && strings.Contains(sql, " in ")
}

func fenceAttemptsClauseSQL(expression clause.Expression) string {
	var builder strings.Builder
	var visit func(clause.Expression)
	visit = func(current clause.Expression) {
		switch value := current.(type) {
		case clause.Where:
			for _, nested := range value.Exprs {
				visit(nested)
			}
		case clause.AndConditions:
			for _, nested := range value.Exprs {
				visit(nested)
			}
		case clause.OrConditions:
			for _, nested := range value.Exprs {
				visit(nested)
			}
		case clause.NotConditions:
			for _, nested := range value.Exprs {
				visit(nested)
			}
		case clause.Expr:
			builder.WriteByte(' ')
			builder.WriteString(value.SQL)
		}
	}
	visit(expression)
	return builder.String()
}

func fenceAttemptsIDBatchBindSize(expression clause.Expression) int {
	batchSize := 0
	var visit func(clause.Expression)
	visit = func(current clause.Expression) {
		if batchSize != 0 {
			return
		}
		switch value := current.(type) {
		case clause.Where:
			for _, nested := range value.Exprs {
				visit(nested)
			}
		case clause.AndConditions:
			for _, nested := range value.Exprs {
				visit(nested)
			}
		case clause.OrConditions:
			for _, nested := range value.Exprs {
				visit(nested)
			}
		case clause.NotConditions:
			for _, nested := range value.Exprs {
				visit(nested)
			}
		case clause.Expr:
			if !strings.Contains(strings.ToLower(value.SQL), "id in") {
				return
			}
			for _, variable := range value.Vars {
				if values, ok := variable.([]string); ok {
					batchSize = len(values)
					return
				}
			}
		}
	}
	visit(expression)
	return batchSize
}

func assertFenceAttemptsJobCleared(t *testing.T, before, after model.BackupAssetExportJob) {
	t.Helper()
	if after.CurrentAttemptID != nil || after.CurrentFenceRevision != before.CurrentFenceRevision+1 ||
		after.TransitionRevision != before.TransitionRevision || after.ExecutionState != before.ExecutionState ||
		after.CleanupState != before.CleanupState || after.ResultKind != before.ResultKind ||
		after.PackedCount != before.PackedCount || after.SkippedCount != before.SkippedCount ||
		after.FailedCount != before.FailedCount || after.LogicalBytes != before.LogicalBytes ||
		after.ProviderBytes != before.ProviderBytes || after.ArtifactBytes != before.ArtifactBytes {
		t.Fatalf("FenceAttempts changed non-fence job fields: before=%+v after=%+v", before, after)
	}
}

func loadFenceAttemptsJob(t *testing.T, db *gorm.DB, id string) model.BackupAssetExportJob {
	t.Helper()
	var row model.BackupAssetExportJob
	if err := db.Where("id = ?", id).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func loadFenceAttemptsItems(t *testing.T, db *gorm.DB, jobID string) []model.BackupAssetExportItem {
	t.Helper()
	var rows []model.BackupAssetExportItem
	if err := db.Where("job_id = ?", jobID).Order("ordinal ASC, id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func loadFenceAttemptsAttempt(t *testing.T, db *gorm.DB, id string) model.BackupAssetExportAttempt {
	t.Helper()
	var row model.BackupAssetExportAttempt
	if err := db.Where("id = ?", id).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func loadFenceAttemptsItemAttempt(t *testing.T, db *gorm.DB, id string) model.BackupAssetExportItemAttempt {
	t.Helper()
	var row model.BackupAssetExportItemAttempt
	if err := db.Where("id = ?", id).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func loadFenceAttemptsItemAttempts(
	t *testing.T, db *gorm.DB, jobID, attemptID string,
) []model.BackupAssetExportItemAttempt {
	t.Helper()
	var rows []model.BackupAssetExportItemAttempt
	if err := db.Where("job_id = ? AND attempt_id = ?", jobID, attemptID).
		Order("item_id ASC, id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func assertFenceAttemptsRollback(
	t *testing.T,
	db *gorm.DB,
	job model.BackupAssetExportJob,
	attempt model.BackupAssetExportAttempt,
	items []model.BackupAssetExportItem,
	itemAttempts []model.BackupAssetExportItemAttempt,
) {
	t.Helper()
	if after := loadFenceAttemptsJob(t, db, job.ID); !reflect.DeepEqual(after, job) {
		t.Fatalf("FenceAttempts rollback changed job: before=%+v after=%+v", job, after)
	}
	if after := loadFenceAttemptsAttempt(t, db, attempt.ID); !reflect.DeepEqual(after, attempt) {
		t.Fatalf("FenceAttempts rollback changed attempt: before=%+v after=%+v", attempt, after)
	}
	if after := loadFenceAttemptsItems(t, db, job.ID); !reflect.DeepEqual(after, items) {
		t.Fatalf("FenceAttempts rollback changed public items: before=%+v after=%+v", items, after)
	}
	if after := loadFenceAttemptsItemAttempts(t, db, job.ID, attempt.ID); !reflect.DeepEqual(after, itemAttempts) {
		t.Fatalf("FenceAttempts rollback changed item attempts: before=%+v after=%+v", itemAttempts, after)
	}
}

type persistentLifecycleReleaseContextKey struct{}

func TestPersistentLifecyclePortReleaseSourcesPreservesUnderlyingErrors(t *testing.T) {
	t.Run("Foundation lease query database error", func(t *testing.T) {
		sources := &persistentLifecycleSourceFailureFake{}
		harness, jobID, port, _ := newPersistentLifecycleReleaseFailureTest(t, sources)
		databaseErr := errors.New("injected Foundation lease query failure")
		marker := t.Name()
		const callbackName = "test:fail_lifecycle_foundation_lease_query"
		if err := harness.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement == nil || tx.Statement.Schema == nil ||
				tx.Statement.Schema.Table != "recovery_point_leases" ||
				tx.Statement.Context.Value(persistentLifecycleReleaseContextKey{}) != marker {
				return
			}
			_ = tx.AddError(databaseErr)
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := harness.db.Callback().Query().Remove(callbackName); err != nil {
				t.Errorf("remove lifecycle Foundation-query failure callback: %v", err)
			}
		})

		err := port.ReleaseSourcesAndNonStore(
			context.WithValue(context.Background(), persistentLifecycleReleaseContextKey{}, marker), jobID,
		)
		assertPersistentLifecycleReleaseError(t, err, databaseErr)
	})

	for _, test := range []struct {
		name        string
		takeoverErr error
	}{
		{name: "takeover context cancellation", takeoverErr: context.Canceled},
		{name: "typed takeover lease error", takeoverErr: backupasset.ErrLeaseDeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			sources := &persistentLifecycleSourceFailureFake{takeoverErr: test.takeoverErr}
			harness, jobID, port, source := newPersistentLifecycleReleaseFailureTest(t, sources)
			if err := harness.db.Model(&model.RecoveryPointLease{}).Where("id = ?", source.LeaseID).
				Update("lease_expires_at", port.now().UTC().Add(-time.Second)).Error; err != nil {
				t.Fatal(err)
			}

			err := port.ReleaseSourcesAndNonStore(context.Background(), jobID)
			assertPersistentLifecycleReleaseError(t, err, test.takeoverErr)
			if sources.takeoverCalls != 1 {
				t.Fatalf("TakeoverTx calls=%d want=1", sources.takeoverCalls)
			}
		})
	}

	t.Run("typed release lease error", func(t *testing.T) {
		sources := &persistentLifecycleSourceFailureFake{releaseErr: backupasset.ErrLeaseFenceLost}
		_, jobID, port, _ := newPersistentLifecycleReleaseFailureTest(t, sources)

		err := port.ReleaseSourcesAndNonStore(context.Background(), jobID)
		assertPersistentLifecycleReleaseError(t, err, backupasset.ErrLeaseFenceLost)
		if sources.releaseCalls != 1 {
			t.Fatalf("ReleaseTx calls=%d want=1", sources.releaseCalls)
		}
	})

	for _, test := range []struct {
		name  string
		cause error
	}{
		{name: "Foundation absolute expiry database error", cause: errors.New("injected Foundation absolute expiry failure")},
		{name: "Foundation absolute expiry context cancellation", cause: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			sources := &persistentLifecycleSourceFailureFake{}
			harness, jobID, port, source := newPersistentLifecycleReleaseFailureTest(t, sources)
			makePersistentLifecycleSourceAbsoluteDeadlineReached(t, harness.db, source, port.now().UTC())
			marker := t.Name()
			callbackName := "test:fail_lifecycle_foundation_expiry:" + test.name
			if err := harness.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement != nil && tx.Statement.Schema != nil &&
					tx.Statement.Schema.Table == "recovery_point_leases" &&
					tx.Statement.Context.Value(persistentLifecycleReleaseContextKey{}) == marker {
					_ = tx.AddError(test.cause)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := harness.db.Callback().Update().Remove(callbackName); err != nil {
					t.Errorf("remove Foundation absolute-expiry failure callback: %v", err)
				}
			})

			err := port.ReleaseSourcesAndNonStore(
				context.WithValue(context.Background(), persistentLifecycleReleaseContextKey{}, marker), jobID,
			)
			assertPersistentLifecycleReleaseError(t, err, test.cause)
			if sources.releaseCalls != 0 || sources.takeoverCalls != 0 {
				t.Fatalf("absolute-deadline expiry called lease service: release=%d takeover=%d",
					sources.releaseCalls, sources.takeoverCalls)
			}
		})
	}

	t.Run("Foundation absolute expiry CAS miss rolls back", func(t *testing.T) {
		sources := &persistentLifecycleSourceFailureFake{}
		harness, jobID, port, source := newPersistentLifecycleReleaseFailureTest(t, sources)
		makePersistentLifecycleSourceAbsoluteDeadlineReached(t, harness.db, source, port.now().UTC())
		var foundationBefore model.RecoveryPointLease
		if err := harness.db.Where("id = ?", source.LeaseID).Take(&foundationBefore).Error; err != nil {
			t.Fatal(err)
		}
		if err := harness.db.Where("id = ?", source.ID).Take(&source).Error; err != nil {
			t.Fatal(err)
		}
		marker := t.Name()
		const callbackName = "test:miss_lifecycle_foundation_absolute_expiry_cas"
		if err := harness.db.Callback().Update().After("gorm:update").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Schema != nil &&
				tx.Statement.Schema.Table == "recovery_point_leases" &&
				tx.Statement.Context.Value(persistentLifecycleReleaseContextKey{}) == marker {
				tx.RowsAffected = 0
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := harness.db.Callback().Update().Remove(callbackName); err != nil {
				t.Errorf("remove Foundation absolute-expiry CAS callback: %v", err)
			}
		})

		err := port.ReleaseSourcesAndNonStore(
			context.WithValue(context.Background(), persistentLifecycleReleaseContextKey{}, marker), jobID,
		)
		if !errors.Is(err, ErrAttemptFenceLost) {
			t.Fatalf("Foundation absolute-expiry CAS miss error=%v want ErrAttemptFenceLost", err)
		}
		var foundationAfter model.RecoveryPointLease
		if err := harness.db.Where("id = ?", source.LeaseID).Take(&foundationAfter).Error; err != nil {
			t.Fatal(err)
		}
		var sourceAfter model.BackupAssetExportSourceLease
		if err := harness.db.Where("id = ?", source.ID).Take(&sourceAfter).Error; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(foundationAfter, foundationBefore) || !reflect.DeepEqual(sourceAfter, source) {
			t.Fatalf("Foundation absolute-expiry CAS miss did not roll back: foundation before=%+v after=%+v source before=%+v after=%+v",
				foundationBefore, foundationAfter, source, sourceAfter)
		}
	})
}

func makePersistentLifecycleSourceAbsoluteDeadlineReached(
	t *testing.T,
	db *gorm.DB,
	source model.BackupAssetExportSourceLease,
	now time.Time,
) {
	t.Helper()
	if err := db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.RecoveryPointLease{}).Where("id = ?", source.LeaseID).
			Update("absolute_deadline", now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("updated %d Foundation deadlines, want 1", result.RowsAffected)
		}
		result = tx.Model(&model.BackupAssetExportSourceLease{}).Where("id = ?", source.ID).
			Update("absolute_deadline", now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("updated %d Export source deadlines, want 1", result.RowsAffected)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPersistentLifecyclePortReleaseSourcesTerminalRowsSkipFoundationAccess(t *testing.T) {
	for _, terminalState := range []string{"released", "expired"} {
		t.Run(terminalState, func(t *testing.T) {
			foundationErr := errors.New("terminal Export source must not reconcile Foundation leases")
			sources := &persistentLifecycleSourceFailureFake{reconcileErr: foundationErr}
			harness, jobID, port, source := newPersistentLifecycleReleaseFailureTest(t, sources)
			now := port.now().UTC()
			if err := harness.db.Model(&model.BackupAssetExportSourceLease{}).
				Where("id = ? AND job_id = ? AND state = ?", source.ID, jobID, "active").
				Updates(map[string]any{"state": terminalState, "released_at": now, "updated_at": now}).Error; err != nil {
				t.Fatal(err)
			}

			marker := t.Name()
			foundationQueries := 0
			foundationUpdates := 0
			queryCallback := "test:reject_terminal_source_foundation_query:" + terminalState
			updateCallback := "test:reject_terminal_source_foundation_update:" + terminalState
			if err := harness.db.Callback().Query().Before("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
				if tx.Statement != nil && tx.Statement.Schema != nil &&
					tx.Statement.Schema.Table == "recovery_point_leases" &&
					tx.Statement.Context.Value(persistentLifecycleReleaseContextKey{}) == marker {
					foundationQueries++
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := harness.db.Callback().Update().Before("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
				if tx.Statement != nil && tx.Statement.Schema != nil &&
					tx.Statement.Schema.Table == "recovery_point_leases" &&
					tx.Statement.Context.Value(persistentLifecycleReleaseContextKey{}) == marker {
					foundationUpdates++
				}
			}); err != nil {
				_ = harness.db.Callback().Query().Remove(queryCallback)
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := harness.db.Callback().Update().Remove(updateCallback); err != nil {
					t.Errorf("remove terminal-source Foundation update callback: %v", err)
				}
				if err := harness.db.Callback().Query().Remove(queryCallback); err != nil {
					t.Errorf("remove terminal-source Foundation query callback: %v", err)
				}
			})

			err := port.ReleaseSourcesAndNonStore(
				context.WithValue(context.Background(), persistentLifecycleReleaseContextKey{}, marker), jobID,
			)
			if err != nil {
				t.Fatalf("terminal source release must be idempotent despite Foundation reconciliation failure: %v", err)
			}
			if sources.reconcileCalls != 0 || sources.releaseCalls != 0 || sources.takeoverCalls != 0 ||
				foundationQueries != 0 || foundationUpdates != 0 {
				t.Fatalf("terminal source touched Foundation: reconcile=%d release=%d takeover=%d queries=%d updates=%d",
					sources.reconcileCalls, sources.releaseCalls, sources.takeoverCalls, foundationQueries, foundationUpdates)
			}
		})
	}
}

func TestPersistentLifecyclePortFenceAttemptsRetainsDurableWorkerPairUntilDrain(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 113, Role: "admin"}, Selection: selection,
		IdempotencyKey: "worker-capacity-lifecycle", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
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
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-capacity-lifecycle"})
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", created.JobID).
		Update("execution_state", string(ExecutionCancelRequested)).Error; err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "worker-capacity-lifecycle")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	quota, err := NewQuotaService(harness.db, harness.service.now, harness.config.Quota)
	if err != nil {
		t.Fatal(err)
	}
	port, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
		DB: harness.db, Delivery: exportBehaviorLifecycleDeliveryStub{}, Sources: harness.lease,
		Quota: quota, Store: store, Now: func() time.Time { return clock },
		WorkerCapacity: &WorkerCapacityLimits{WorkerConcurrency: 1, UserActiveJobs: 1}, AttemptWork: NewAttemptWorkRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := port.FenceAttempts(context.Background(), created.JobID); err != nil {
		t.Fatal(err)
	}
	assertDurableWorkerReservationPair(t, harness.db, created.JobID, claim.AttemptID, claim.LeaseExpiresAt, 1)
}

func TestPersistentLifecyclePortDrainsAttemptWorkBeforeReleasingDurableWorkerPair(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 119, Role: "admin"}, Selection: selection,
		IdempotencyKey: "worker-capacity-lifecycle-drain", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := harness.service.now().UTC()
	coordinator, err := NewAttemptCoordinatorWithWorkerCapacity(
		harness.db, func() time.Time { return clock }, WorkerCapacityLimits{WorkerConcurrency: 1, UserActiveJobs: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{JobID: created.JobID, WorkerOwner: "worker-capacity-lifecycle-drain"})
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", created.JobID).
		Update("execution_state", string(ExecutionCancelRequested)).Error; err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "worker-capacity-lifecycle-drain")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	quota, err := NewQuotaService(harness.db, harness.service.now, harness.config.Quota)
	if err != nil {
		t.Fatal(err)
	}
	work := NewAttemptWorkRegistry()
	handle, err := work.Start(context.Background(), created.JobID)
	if err != nil {
		t.Fatal(err)
	}
	workCanceled := make(chan struct{})
	go func() {
		<-handle.Context().Done()
		close(workCanceled)
		handle.Finish()
	}()
	port, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
		DB: harness.db, Delivery: exportBehaviorLifecycleDeliveryStub{}, Sources: harness.lease,
		Quota: quota, Store: store, Now: func() time.Time { return clock },
		WorkerCapacity: &WorkerCapacityLimits{WorkerConcurrency: 1, UserActiveJobs: 1}, AttemptWork: work,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := port.FenceAttempts(context.Background(), created.JobID); err != nil {
		t.Fatal(err)
	}
	assertDurableWorkerReservationPair(t, harness.db, created.JobID, claim.AttemptID, claim.LeaseExpiresAt, 1)
	if err := port.DrainStreams(context.Background(), created.JobID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-workCanceled:
	default:
		t.Fatal("attempt work was not canceled before durable worker capacity release")
	}
	assertReleasedDurableWorkerReservationPair(t, harness.db, created.JobID, claim.AttemptID, 0)
}

func TestLifecycleCleanupWaitsForBlockedAttemptBrokerReaderBeforeKeyAndSourceRelease(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	item := frozenItemFixture()
	selection, err := FreezeSelection([]FrozenItem{item}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	actor := SelectionActor{UserID: 119, Role: "admin"}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: actor, Selection: selection,
		IdempotencyKey: "lifecycle-attempt-broker-reader-drain", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := harness.service.now().UTC()
	limits := WorkerCapacityLimits{WorkerConcurrency: 1, UserActiveJobs: 1}
	coordinator, err := NewAttemptCoordinatorWithWorkerCapacity(harness.db, func() time.Time { return clock }, limits)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), AttemptClaimRequest{
		JobID: created.JobID, WorkerOwner: "lifecycle-attempt-broker-reader-drain",
	})
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
	source := newLifecycleDrainBlockingSourceResolver(item)
	t.Cleanup(source.reader.allowClose)
	broker, err := content.NewAttemptBroker(source, budget, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "lifecycle-attempt-broker-reader-drain")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	quota, err := NewQuotaService(harness.db, func() time.Time { return clock }, harness.config.Quota)
	if err != nil {
		t.Fatal(err)
	}
	work := NewAttemptWorkRegistry()
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: harness.db, Keys: backupasset.NewKeyring(harness.db, func() time.Time { return clock }),
		Broker: broker, Metadata: &metadataValidatorFake{}, Store: store,
		WorkerCapacity: &limits, AttemptWork: work, Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	port, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
		DB: harness.db, Delivery: exportBehaviorLifecycleDeliveryStub{}, Sources: harness.lease,
		Quota: quota, Store: store, WorkerCapacity: &limits, AttemptWork: work,
		Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(LifecycleDependencies{
		DB: harness.db, Port: port, Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}

	spooled := make(chan error, 1)
	go func() {
		_, spoolErr := worker.SpoolItem(context.Background(), PersistentSpoolItemRequest{
			JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: itemRow.ID,
		})
		spooled <- spoolErr
	}()
	select {
	case <-source.reader.started:
	case <-time.After(time.Second):
		t.Fatal("blocked AttemptBroker reader was not opened")
	}
	if _, err := harness.service.Cancel(context.Background(), actor, created.JobID); err != nil {
		t.Fatal(err)
	}
	type cleanupResult struct {
		state CleanupState
		err   error
	}
	cleanupDone := make(chan cleanupResult, 1)
	go func() {
		state, cleanupErr := lifecycle.Cleanup(context.Background(), created.JobID)
		cleanupDone <- cleanupResult{state: state, err: cleanupErr}
	}()
	select {
	case <-source.reader.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("attempt work drain did not close the blocked reader")
	}
	select {
	case result := <-cleanupDone:
		t.Fatalf("cleanup returned before reader close acknowledgement: state=%s err=%v", result.state, result.err)
	default:
	}
	assertDurableWorkerReservationPair(t, harness.db, created.JobID, claim.AttemptID, claim.LeaseExpiresAt, 1)
	assertLifecycleDrainKeyAndSourceRemainActive(t, harness.db, created.JobID)

	source.reader.allowClose()
	select {
	case spoolErr := <-spooled:
		if spoolErr == nil {
			t.Fatal("spool succeeded after lifecycle cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("spool did not exit after reader close acknowledgement")
	}
	select {
	case result := <-cleanupDone:
		if result.err != nil || result.state != CleanupPurged {
			t.Fatalf("cleanup state=%s err=%v", result.state, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup did not finish after reader close acknowledgement")
	}
	assertReleasedDurableWorkerReservationPair(t, harness.db, created.JobID, claim.AttemptID, 0)
	assertLifecycleDrainKeyAndSourceReleased(t, harness.db, created.JobID)
}

func assertLifecycleDrainKeyAndSourceRemainActive(t *testing.T, db *gorm.DB, jobID string) {
	t.Helper()
	var key model.BackupAssetExportKey
	if err := db.Where("job_id = ?", jobID).Take(&key).Error; err != nil {
		t.Fatal(err)
	}
	if key.State != "active" || len(key.WrappedDEK) == 0 || len(key.EnvelopeNonce) == 0 || key.DestroyedAt != nil {
		t.Fatalf("key changed before reader drain: %+v", key)
	}
	var source model.BackupAssetExportSourceLease
	if err := db.Where("job_id = ?", jobID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	if source.State != "active" || source.ReleasedAt != nil {
		t.Fatalf("source lease changed before reader drain: %+v", source)
	}
	var recoveryLease model.RecoveryPointLease
	if err := db.Where("id = ?", source.LeaseID).Take(&recoveryLease).Error; err != nil {
		t.Fatal(err)
	}
	if recoveryLease.Status != string(backupasset.LeaseActive) {
		t.Fatalf("Foundation source lease changed before reader drain: %+v", recoveryLease)
	}
}

func assertLifecycleDrainKeyAndSourceReleased(t *testing.T, db *gorm.DB, jobID string) {
	t.Helper()
	var key model.BackupAssetExportKey
	if err := db.Where("job_id = ?", jobID).Take(&key).Error; err != nil {
		t.Fatal(err)
	}
	if key.State != "destroyed" || len(key.WrappedDEK) != 0 || len(key.EnvelopeNonce) != 0 || key.DestroyedAt == nil {
		t.Fatalf("key was not destroyed after reader drain: %+v", key)
	}
	var source model.BackupAssetExportSourceLease
	if err := db.Where("job_id = ?", jobID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	if source.State != "released" || source.ReleasedAt == nil {
		t.Fatalf("source lease was not released after reader drain: %+v", source)
	}
	var recoveryLease model.RecoveryPointLease
	if err := db.Where("id = ?", source.LeaseID).Take(&recoveryLease).Error; err != nil {
		t.Fatal(err)
	}
	if recoveryLease.Status != string(backupasset.LeaseReleased) {
		t.Fatalf("Foundation source lease was not released after reader drain: %+v", recoveryLease)
	}
}

func TestPersistentLifecyclePortRequiresAttemptWorkRegistry(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "lifecycle-capacity-registry")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	quota, err := NewQuotaService(harness.db, harness.service.now, harness.config.Quota)
	if err != nil {
		t.Fatal(err)
	}
	limits := WorkerCapacityLimits{WorkerConcurrency: 1, UserActiveJobs: 1}
	for _, test := range []struct {
		name     string
		capacity *WorkerCapacityLimits
	}{
		{name: "without capacity"},
		{name: "with capacity", capacity: &limits},
	} {
		t.Run(test.name, func(t *testing.T) {
			port, portErr := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
				DB: harness.db, Delivery: exportBehaviorLifecycleDeliveryStub{}, Sources: harness.lease,
				Quota: quota, Store: store, WorkerCapacity: test.capacity, Now: harness.service.now,
			})
			if !errors.Is(portErr, ErrUnavailable) || port != nil {
				t.Fatalf("lifecycle port without attempt registry port=%v err=%v", port, portErr)
			}
		})
	}
}

type lifecycleDrainBlockingSourceResolver struct {
	item   FrozenItem
	reader *lifecycleDrainBlockingSourceReader
}

func newLifecycleDrainBlockingSourceResolver(item FrozenItem) *lifecycleDrainBlockingSourceResolver {
	return &lifecycleDrainBlockingSourceResolver{
		item: item,
		reader: &lifecycleDrainBlockingSourceReader{
			started: make(chan struct{}), closeStarted: make(chan struct{}),
			readReleased: make(chan struct{}), releaseClose: make(chan struct{}),
		},
	}
}

func (resolver *lifecycleDrainBlockingSourceResolver) OpenContentSource(
	_ context.Context, request content.SourceRequest,
) (content.SourceSession, error) {
	return &lifecycleDrainBlockingSourceSession{request: request, item: resolver.item, reader: resolver.reader}, nil
}

func (*lifecycleDrainBlockingSourceResolver) ValidateContentCacheRoot(context.Context, string) error {
	return nil
}

type lifecycleDrainBlockingSourceSession struct {
	request content.SourceRequest
	item    FrozenItem
	reader  *lifecycleDrainBlockingSourceReader
}

func (session *lifecycleDrainBlockingSourceSession) Stat() content.SourceStat {
	return content.SourceStat{
		Size: session.item.LogicalSize, MediaType: session.item.MediaType,
		SourceFingerprint: session.request.ExpectedSource, EntryFingerprint: session.request.ExpectedEntry,
		FingerprintStrong: true,
	}
}

func (*lifecycleDrainBlockingSourceSession) Capabilities() content.SourceCapabilities {
	return content.SourceCapabilities{Sequential: true}
}

func (session *lifecycleDrainBlockingSourceSession) Reader() content.SourceReader {
	return session.reader
}
func (*lifecycleDrainBlockingSourceSession) Revalidate(context.Context) error { return nil }
func (session *lifecycleDrainBlockingSourceSession) Close() error {
	if session.request.Mode != content.SourceModeSequential {
		return nil
	}
	return session.reader.Close()
}

type lifecycleDrainBlockingSourceReader struct {
	started      chan struct{}
	closeStarted chan struct{}
	readReleased chan struct{}
	releaseClose chan struct{}
	startOnce    sync.Once
	closeOnce    sync.Once
	releaseOnce  sync.Once
}

func (reader *lifecycleDrainBlockingSourceReader) Read([]byte) (int, error) {
	reader.startOnce.Do(func() { close(reader.started) })
	<-reader.readReleased
	return 0, errors.New("blocked source reader closed")
}

func (reader *lifecycleDrainBlockingSourceReader) Close() error {
	reader.closeOnce.Do(func() {
		close(reader.closeStarted)
		close(reader.readReleased)
		<-reader.releaseClose
	})
	return nil
}

func (reader *lifecycleDrainBlockingSourceReader) allowClose() {
	reader.releaseOnce.Do(func() { close(reader.releaseClose) })
}

func (*lifecycleDrainBlockingSourceReader) ProviderBytes() int64 { return 0 }

func newPersistentLifecycleReleaseFailureTest(
	t *testing.T,
	sources ExportSourceLeaseLifecycle,
) (serviceHarness, string, *PersistentLifecyclePort, model.BackupAssetExportSourceLease) {
	t.Helper()
	harness := newServiceHarness(t)
	job := commitFenceAttemptsTestJob(t, harness, "release-errors")
	var source model.BackupAssetExportSourceLease
	if err := harness.db.Where("job_id = ?", job.ID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "release-errors")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close lifecycle release error store: %v", err)
		}
	})
	quota, err := NewQuotaService(harness.db, harness.service.now, harness.config.Quota)
	if err != nil {
		t.Fatal(err)
	}
	port, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
		DB: harness.db, Delivery: exportBehaviorLifecycleDeliveryStub{}, Sources: sources,
		Quota: quota, Store: store, AttemptWork: NewAttemptWorkRegistry(), Now: harness.service.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return harness, job.ID, port, source
}

func assertPersistentLifecycleReleaseError(t *testing.T, err, cause error) {
	t.Helper()
	if !errors.Is(err, cause) {
		t.Fatalf("ReleaseSourcesAndNonStore error=%v, want errors.Is(..., %v)", err, cause)
	}
	if errors.Is(err, ErrAttemptFenceLost) {
		t.Fatalf("ReleaseSourcesAndNonStore error=%v unexpectedly classified infrastructure failure as ErrAttemptFenceLost", err)
	}
}

type persistentLifecycleSourceFailureFake struct {
	reconcileErr   error
	releaseErr     error
	takeoverErr    error
	reconcileCalls int
	releaseCalls   int
	takeoverCalls  int
}

func (fake *persistentLifecycleSourceFailureFake) ReconcileExpired(context.Context) (int64, error) {
	fake.reconcileCalls++
	return 0, fake.reconcileErr
}

func (fake *persistentLifecycleSourceFailureFake) ReleaseTx(
	context.Context, *gorm.DB, backupasset.LeaseFence,
) error {
	fake.releaseCalls++
	return fake.releaseErr
}

func (fake *persistentLifecycleSourceFailureFake) TakeoverTx(
	context.Context, *gorm.DB, backupasset.TakeoverLeaseRequest,
) (backupasset.Lease, error) {
	fake.takeoverCalls++
	return backupasset.Lease{}, fake.takeoverErr
}

func TestPersistentLifecyclePortDestroyPreservesDatabaseErrorsAcrossTransactionBoundaries(t *testing.T) {
	t.Run("load", func(t *testing.T) {
		harness, jobID, port, before := newPersistentLifecycleDestroyFailureTest(t)
		storageErr := errors.New("injected export key load failure")
		const callbackName = "test:fail_export_key_destruction_load"
		if err := harness.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "backup_asset_export_keys" {
				_ = tx.AddError(storageErr)
			}
		}); err != nil {
			t.Fatal(err)
		}
		removed := false
		t.Cleanup(func() {
			if !removed {
				if err := harness.db.Callback().Query().Remove(callbackName); err != nil {
					t.Errorf("remove lifecycle load failure callback: %v", err)
				}
			}
		})

		err := port.DestroyJobKeyAndSelection(context.Background(), jobID)
		if err := harness.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove lifecycle load failure callback: %v", err)
		}
		removed = true
		assertPersistentLifecycleDatabaseError(t, err, storageErr)
		assertPersistentLifecycleKeyUnchanged(t, harness.db, before)
	})

	t.Run("key update", func(t *testing.T) {
		harness, jobID, port, before := newPersistentLifecycleDestroyFailureTest(t)
		storageErr := errors.New("injected export key update failure")
		const callbackName = "test:fail_export_key_destruction_update"
		if err := harness.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "backup_asset_export_keys" {
				_ = tx.AddError(storageErr)
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := harness.db.Callback().Update().Remove(callbackName); err != nil {
				t.Errorf("remove lifecycle key-update failure callback: %v", err)
			}
		})

		err := port.DestroyJobKeyAndSelection(context.Background(), jobID)
		assertPersistentLifecycleDatabaseError(t, err, storageErr)
		assertPersistentLifecycleKeyUnchanged(t, harness.db, before)
	})

	t.Run("selection update", func(t *testing.T) {
		harness, jobID, port, before := newPersistentLifecycleDestroyFailureTest(t)
		beforeSelection := loadPersistentLifecycleSelectionMetadata(t, harness.db, jobID)
		storageErr := errors.New("injected export selection update failure")
		const callbackName = "test:fail_export_selection_destruction_update"
		if err := harness.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "backup_asset_export_items" {
				_ = tx.AddError(storageErr)
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := harness.db.Callback().Update().Remove(callbackName); err != nil {
				t.Errorf("remove lifecycle selection-update failure callback: %v", err)
			}
		})

		err := port.DestroyJobKeyAndSelection(context.Background(), jobID)
		assertPersistentLifecycleDatabaseError(t, err, storageErr)
		assertPersistentLifecycleKeyUnchanged(t, harness.db, before)
		assertPersistentLifecycleSelectionMetadataUnchanged(t, harness.db, jobID, beforeSelection)
	})

	t.Run("commit", func(t *testing.T) {
		harness, jobID, port, before := newPersistentLifecycleDestroyFailureTest(t)
		beforeSelection := loadPersistentLifecycleSelectionMetadata(t, harness.db, jobID)
		sqlDB, err := harness.db.DB()
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(1)
		if err := harness.db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
			t.Fatal(err)
		}
		if err := harness.db.Exec(`CREATE TABLE lifecycle_commit_guards (
			id INTEGER PRIMARY KEY,
			key_id TEXT NOT NULL,
			FOREIGN KEY (key_id) REFERENCES backup_asset_export_keys(id) DEFERRABLE INITIALLY DEFERRED
		)`).Error; err != nil {
			t.Fatal(err)
		}
		if err := harness.db.Exec(`INSERT INTO lifecycle_commit_guards (id, key_id) VALUES (1, ?)`, before.ID).Error; err != nil {
			t.Fatal(err)
		}
		var injected atomic.Bool
		const callbackName = "test:fail_export_key_destruction_commit"
		if err := harness.db.Callback().Update().After("gorm:update").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "backup_asset_export_items" ||
				!injected.CompareAndSwap(false, true) {
				return
			}
			if err := tx.Exec(`UPDATE lifecycle_commit_guards SET key_id = ? WHERE id = 1`,
				strings.Repeat("0", 32)).Error; err != nil {
				_ = tx.AddError(err)
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := harness.db.Callback().Update().Remove(callbackName); err != nil {
				t.Errorf("remove lifecycle commit failure callback: %v", err)
			}
		})

		err = port.DestroyJobKeyAndSelection(context.Background(), jobID)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("destroy commit error=%v must preserve safe sentinel", err)
		}
		var sqliteErr sqlite3.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code != sqlite3.ErrConstraint {
			t.Fatalf("destroy commit error=%v must preserve the SQLite commit constraint error", err)
		}
		assertPersistentLifecycleKeyUnchanged(t, harness.db, before)
		assertPersistentLifecycleSelectionMetadataUnchanged(t, harness.db, jobID, beforeSelection)
	})
}

func newPersistentLifecycleDestroyFailureTest(
	t *testing.T,
) (serviceHarness, string, *PersistentLifecyclePort, model.BackupAssetExportKey) {
	t.Helper()
	harness := newServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 42, Role: "admin"}, Selection: selection,
		IdempotencyKey: "export-lifecycle-db-error-0001", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var before model.BackupAssetExportKey
	if err := harness.db.Where("job_id = ?", created.JobID).Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	port := &PersistentLifecyclePort{db: harness.db, now: func() time.Time { return time.Now().UTC() }, attemptWork: NewAttemptWorkRegistry()}
	return harness, created.JobID, port, before
}

func assertPersistentLifecycleDatabaseError(t *testing.T, err, databaseErr error) {
	t.Helper()
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, databaseErr) {
		t.Fatalf("destroy error=%v must preserve safe sentinel and underlying database error=%v", err, databaseErr)
	}
}

func assertPersistentLifecycleKeyUnchanged(t *testing.T, db *gorm.DB, before model.BackupAssetExportKey) {
	t.Helper()
	var after model.BackupAssetExportKey
	if err := db.Where("id = ?", before.ID).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.State != before.State || after.KeyRevision != before.KeyRevision || after.DestroyedAt != nil ||
		!reflect.DeepEqual(after.WrappedDEK, before.WrappedDEK) || !reflect.DeepEqual(after.EnvelopeNonce, before.EnvelopeNonce) {
		t.Fatalf("failed destruction transaction changed key: before=%+v after=%+v", before, after)
	}
}

type persistentLifecycleSelectionMetadata struct {
	ID             string
	PathNonce      []byte
	PathCiphertext []byte
}

func loadPersistentLifecycleSelectionMetadata(
	t *testing.T, db *gorm.DB, jobID string,
) []persistentLifecycleSelectionMetadata {
	t.Helper()
	var rows []persistentLifecycleSelectionMetadata
	if err := db.Model(&model.BackupAssetExportItem{}).Select("id", "path_nonce", "path_ciphertext").
		Where("job_id = ?", jobID).Order("ordinal ASC, id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("lifecycle destruction fixture has no selection metadata")
	}
	return rows
}

func assertPersistentLifecycleSelectionMetadataUnchanged(
	t *testing.T,
	db *gorm.DB,
	jobID string,
	before []persistentLifecycleSelectionMetadata,
) {
	t.Helper()
	after := loadPersistentLifecycleSelectionMetadata(t, db, jobID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("failed destruction transaction changed encrypted selection metadata: before=%+v after=%+v", before, after)
	}
}

type persistentLifecycleDeliveryFake struct {
	calls []string
}

func (fake *persistentLifecycleDeliveryFake) BeginRevokeExportJob(_ context.Context, jobID, reason string) error {
	if reason != "job_failed" {
		return ErrUnavailable
	}
	fake.calls = append(fake.calls, "revoke:"+jobID)
	return nil
}

func (fake *persistentLifecycleDeliveryFake) DrainExportJob(_ context.Context, jobID string) error {
	fake.calls = append(fake.calls, "drain:"+jobID)
	return nil
}

func createLifecycleJob(
	t *testing.T,
	harness serviceHarness,
	createdAt time.Time,
	execution ExecutionState,
	expiresAt *time.Time,
) string {
	t.Helper()
	return createLifecycleJobFixture(t, harness, 1, "balanced", createdAt, execution, expiresAt)
}

func createLifecycleJobForOwner(
	t *testing.T,
	harness serviceHarness,
	ownerUserID uint,
	createdAt time.Time,
	execution ExecutionState,
	expiresAt *time.Time,
) string {
	t.Helper()
	return createLifecycleJobFixture(t, harness, ownerUserID, "zip_deflate_v1", createdAt, execution, expiresAt)
}

func createLifecycleJobFixture(
	t *testing.T,
	harness serviceHarness,
	ownerUserID uint,
	archiveProfile string,
	createdAt time.Time,
	execution ExecutionState,
	expiresAt *time.Time,
) string {
	t.Helper()
	jobID, err := backupasset.NewOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	maxCiphertextBytes, err := minimumArchiveCiphertextBytesV1(1024, 1, 65536)
	if err != nil {
		t.Fatal(err)
	}
	deadline := createdAt.Add(2 * time.Hour)
	job := model.BackupAssetExportJob{
		ID: jobID, OwnerUserID: ownerUserID, SelectionDigest: strings.Repeat("a", 64), SelectionSchemaVersion: 1,
		ArchiveFormat: string(ArchiveZIP), ArchiveProfile: archiveProfile, LimitsSchemaVersion: 1,
		ChunkBytes: 65536, MaxItems: 1, MaxSourcePoints: 1, MaxItemBytes: 1024, MaxLogicalBytes: 1024,
		MaxProviderBytes: 1024, MaxCiphertextBytes: maxCiphertextBytes, MaxOpenReaders: 1, MaxDurationSeconds: 3600,
		MaxAttempts: 3, RetryBaseSeconds: 1, RetryMaxDelaySeconds: 10, LeaseTTLSeconds: 900,
		LeaseRenewMarginSeconds: 300, ReadyTTLSeconds: 86400, ExecutionState: string(execution),
		CleanupState: string(CleanupNone), AbsoluteDeadline: deadline, ExpiresAt: expiresAt,
		TransitionRevision: 1, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if execution == ExecutionReady || execution == ExecutionExpiring || execution == ExecutionExpired {
		readyAt := createdAt.Add(time.Minute)
		job.ReadyAt = &readyAt
		job.ResultKind = string(ResultComplete)
		job.ItemCount = 1
		job.PackedCount = 1
	}
	if err := harness.db.Transaction(func(tx *gorm.DB) error {
		buckets, err := ensureAndLockQuotaBucketPairTx(tx, ownerUserID, createdAt.UTC())
		if err != nil {
			return err
		}
		sequence, err := allocateLifecycleEnqueueSequenceTx(tx, buckets.Global)
		if err != nil {
			return err
		}
		job.LifecycleEnqueueSequence = sequence
		return tx.Create(&job).Error
	}); err != nil {
		t.Fatal(err)
	}
	return jobID
}

func assertLifecycleJobState(
	t *testing.T,
	harness serviceHarness,
	jobID string,
	execution ExecutionState,
	cleanup CleanupState,
) {
	t.Helper()
	var job model.BackupAssetExportJob
	if err := harness.db.First(&job, "id = ?", jobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(execution) || job.CleanupState != string(cleanup) {
		t.Fatalf("job execution=%s cleanup=%s want=%s/%s", job.ExecutionState, job.CleanupState, execution, cleanup)
	}
}

type lifecyclePortFake struct {
	calls      []string
	failAt     string
	failure    error
	beforeCall func(string, string) error
}

func (fake *lifecyclePortFake) call(name, jobID string) error {
	fake.calls = append(fake.calls, name)
	if fake.beforeCall != nil {
		if err := fake.beforeCall(name, jobID); err != nil {
			return err
		}
	}
	if fake.failAt == name {
		return fake.failure
	}
	return nil
}
func (fake *lifecyclePortFake) FenceAttempts(_ context.Context, jobID string) error {
	return fake.call("fence_attempts", jobID)
}
func (fake *lifecyclePortFake) RevokeDeliveries(_ context.Context, jobID string) error {
	return fake.call("revoke_deliveries", jobID)
}
func (fake *lifecyclePortFake) DrainStreams(_ context.Context, jobID string) error {
	return fake.call("drain_streams", jobID)
}
func (fake *lifecyclePortFake) DestroyJobKeyAndSelection(_ context.Context, jobID string) error {
	return fake.call("destroy_key", jobID)
}
func (fake *lifecyclePortFake) ReleaseSourcesAndNonStore(_ context.Context, jobID string) error {
	return fake.call("release_sources", jobID)
}
func (fake *lifecyclePortFake) PurgeCiphertext(_ context.Context, jobID string) error {
	return fake.call("purge_ciphertext", jobID)
}
func (fake *lifecyclePortFake) ReleaseStoreBytes(_ context.Context, jobID string) error {
	return fake.call("release_store", jobID)
}

type keyLossLifecyclePortFake struct {
	db                     *gorm.DB
	now                    time.Time
	calls                  []string
	releaseSourcesFailures int
	releaseSourcesErr      error
}

func (fake *keyLossLifecyclePortFake) record(name, jobID string) {
	fake.calls = append(fake.calls, name+":"+jobID)
}

func (fake *keyLossLifecyclePortFake) FenceAttempts(_ context.Context, jobID string) error {
	fake.record("fence_attempts", jobID)
	return fake.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.BackupAssetExportAttempt{}).
			Where("job_id = ? AND state IN ?", jobID, []string{string(AttemptActive), string(AttemptSealing)}).
			Updates(map[string]any{
				"state": string(AttemptFailed), "failure_category": "key_unavailable",
				"is_current": false, "finished_at": fake.now,
			}).Error; err != nil {
			return err
		}
		return tx.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
			Updates(map[string]any{"current_attempt_id": nil, "current_fence_revision": gorm.Expr("current_fence_revision + 1")}).Error
	})
}

func (fake *keyLossLifecyclePortFake) RevokeDeliveries(_ context.Context, jobID string) error {
	fake.record("revoke_deliveries", jobID)
	return nil
}

func (fake *keyLossLifecyclePortFake) DrainStreams(_ context.Context, jobID string) error {
	fake.record("drain_streams", jobID)
	return nil
}

func (fake *keyLossLifecyclePortFake) DestroyJobKeyAndSelection(_ context.Context, jobID string) error {
	fake.record("destroy_key", jobID)
	return fake.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.BackupAssetExportKey{}).
			Where("job_id = ? AND state = ?", jobID, "active").
			Updates(map[string]any{
				"state": "destroyed", "wrapped_dek": []byte{}, "envelope_nonce": []byte{},
				"destroyed_at": fake.now, "key_revision": gorm.Expr("key_revision + 1"),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var key model.BackupAssetExportKey
			if err := tx.Where("job_id = ?", jobID).Take(&key).Error; err != nil {
				return err
			}
			if (key.State != "destroyed" && key.State != "lost") || len(key.WrappedDEK) != 0 ||
				len(key.EnvelopeNonce) != 0 || key.DestroyedAt == nil {
				return ErrUnavailable
			}
			var liveSelectionCount int64
			if err := tx.Model(&model.BackupAssetExportItem{}).
				Where("job_id = ? AND (length(path_nonce) <> 0 OR length(path_ciphertext) <> 0)", jobID).
				Count(&liveSelectionCount).Error; err != nil {
				return err
			}
			if liveSelectionCount != 0 {
				return ErrUnavailable
			}
			return nil
		}
		return tx.Model(&model.BackupAssetExportItem{}).Where("job_id = ?", jobID).
			Updates(map[string]any{"path_nonce": []byte{}, "path_ciphertext": []byte{}, "updated_at": fake.now}).Error
	})
}

func (fake *keyLossLifecyclePortFake) ReleaseSourcesAndNonStore(_ context.Context, jobID string) error {
	fake.record("release_sources", jobID)
	if fake.releaseSourcesFailures > 0 {
		fake.releaseSourcesFailures--
		if fake.releaseSourcesErr != nil {
			return fake.releaseSourcesErr
		}
		return ErrUnavailable
	}
	return nil
}

func (fake *keyLossLifecyclePortFake) PurgeCiphertext(_ context.Context, jobID string) error {
	fake.record("purge_ciphertext", jobID)
	return nil
}

func (fake *keyLossLifecyclePortFake) ReleaseStoreBytes(_ context.Context, jobID string) error {
	fake.record("release_store", jobID)
	return nil
}
