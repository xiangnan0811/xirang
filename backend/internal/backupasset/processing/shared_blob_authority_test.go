package processing

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

type sharedAuthorityContextKey struct{}

func sharedAuthorityFixture(t *testing.T, f processingBehaviorFixture) retirementEvidenceFixture {
	t.Helper()
	ctx := context.Background()
	keys := backupasset.NewKeyring(f.db, f.clock.Now)
	if _, err := keys.Ensure(ctx, backupasset.KeyDomainDerivedStore); err != nil {
		t.Fatal(err)
	}
	store, err := NewDerivedStore(ctx, f.db, keys, DerivedStoreConfig{Root: filepath.Join(t.TempDir(), "derived"), ChunkSize: 64 << 10, BlobMaxBytes: 1 << 20, GlobalMaxBytes: 8 << 20, ValidateRoot: func(context.Context, string) error { return nil }}, f.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewDerivedLifecycle(f.db, store, &processingBehaviorProjection{db: f.db}, f.clock.Now, f.lease)
	if err != nil {
		t.Fatal(err)
	}
	d := validWorkDescriptor()
	sink, err := NewArtifactSink(f.db, f.lease, f.grants, store, lifecycle, &manifestSourceRevalidator{}, func(context.Context) (string, error) { return d.SecurityPolicyRevision, nil }, func(context.Context, string, string) (string, error) { return d.PipelineFingerprint, nil }, f.clock.Now, ArtifactSinkConfig{MaxArtifacts: 4, MaxArtifactBytes: 1 << 20, MaxTotalBytes: 4 << 20})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("authority shared output")
	return retirementEvidenceFixture{processingBehaviorFixture: f, store: store, lifecycle: lifecycle, sink: sink, declaration: artifactDeclaration(0, ArtifactRoleNoop, "application/octet-stream", payload), payload: payload}
}

func sharedAuthorityDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_PROCESSING_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	return dsn
}

func TestDerivedRewrapReincarnationSQLite(t *testing.T) {
	t.Run("StaleRewrapReplacement", func(t *testing.T) {
		sharedAuthorityRewrap(t, sharedAuthorityFixture(t, openProcessingBehaviorSQLite(t)))
	})
}

func TestDerivedRewrapReincarnationPostgres(t *testing.T) {
	dsn := sharedAuthorityDSN(t)
	t.Run("StaleRewrapReplacement", func(t *testing.T) {
		sharedAuthorityRewrap(t, sharedAuthorityFixture(t, openProcessingBehaviorPostgres(t, dsn)))
	})
}

func sharedAuthorityPublication(t *testing.T, f retirementEvidenceFixture, w retirementEvidenceWork) (model.BackupAssetDerivedArtifactSet, DerivedArtifactAuthorization) {
	t.Helper()
	var set model.BackupAssetDerivedArtifactSet
	var artifact model.BackupAssetDerivedArtifact
	if err := f.db.First(&set, "job_id = ?", w.job.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&artifact, "artifact_set_id = ?", set.ID).Error; err != nil {
		t.Fatal(err)
	}
	d := w.descriptor
	return set, DerivedArtifactAuthorization{ArtifactID: artifact.ID, RecoveryPointID: d.Source.RecoveryPointID, CatalogGenerationID: d.CatalogGenerationID, EntryID: d.Source.EntryID, SourceFingerprint: d.SourceFingerprint}
}

func sharedAuthorityFence(t *testing.T, f retirementEvidenceFixture, w retirementEvidenceWork) backupasset.LeaseFence {
	t.Helper()
	lease, err := f.lease.Acquire(context.Background(), backupasset.AcquireLeaseRequest{RecoveryPointID: w.descriptor.Source.RecoveryPointID, HolderType: backupasset.LeaseHolderProcessingJob, OwnerID: w.job.JobID})
	if err != nil {
		t.Fatal(err)
	}
	return lease.Fence
}

func sharedAuthorityRewrap(t *testing.T, f retirementEvidenceFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a := f.prepare(t, validWorkDescriptor(), "rewrap-old")
	if _, err := f.upload(ctx, a); err != nil {
		t.Fatal(err)
	}
	blobID := f.publishAndRead(t, a)
	set, auth := sharedAuthorityPublication(t, f, a)
	var old model.BackupAssetDerivedBlob
	if err := f.db.First(&old, "id = ?", blobID).Error; err != nil {
		t.Fatal(err)
	}
	declaration := derivedDeclaration(f.payload)
	replay, err := f.store.newReplay(ctx, declaration, bytes.NewReader(f.payload))
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	if replay.spec.KEKVersion != old.DerivedKEKVersion || replay.spec.BlobID != blobID {
		t.Fatal("replacement replay does not retain the original version and identity")
	}
	newKey, err := f.store.keyring.Rotate(ctx, backupasset.KeyDomainDerivedStore, 0)
	if err != nil {
		t.Fatal(err)
	}
	ready, release := make(chan struct{}), make(chan struct{})
	var readyOnce, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	const callback = "test:shared-authority-stale-rewrap"
	if err := f.db.Callback().Update().Before("gorm:begin_transaction").Register(callback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Context.Value(sharedAuthorityContextKey{}) != "rewrap" || processingCallbackTable(tx) != "backup_asset_derived_blobs" {
			return
		}
		readyOnce.Do(func() { close(ready) })
		select {
		case <-release:
		case <-ctx.Done():
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Update().Remove(callback) })
	type rewrapResult struct {
		count int
		err   error
	}
	finished := make(chan rewrapResult, 1)
	go func() {
		count, err := f.store.RewrapBatch(context.WithValue(ctx, sharedAuthorityContextKey{}, "rewrap"), old.DerivedKEKVersion, newKey.Version, 10)
		finished <- rewrapResult{count, err}
	}()
	retirementWait(t, ctx, ready, "rewrap before update")
	fence := sharedAuthorityFence(t, f, a)
	if err := f.lifecycle.RevokeSetFenced(ctx, set.ID, DerivedRevokeExpired, fence); err != nil {
		t.Fatal(err)
	}
	if err := f.lease.Release(ctx, fence); err != nil {
		t.Fatal(err)
	}
	replacement, err := f.store.putReplay(ctx, declaration, replay)
	if err != nil {
		t.Fatal(err)
	}
	var before model.BackupAssetDerivedBlob
	if err := f.db.First(&before, "id = ?", blobID).Error; err != nil {
		t.Fatal(err)
	}
	if replacement.BlobID != blobID || replacement.OpaqueLocator == old.OpaqueLocator || before.DerivedKEKVersion != old.DerivedKEKVersion || bytes.Equal(old.WrappedDEK, before.WrappedDEK) {
		t.Fatal("replacement did not install a distinct v1 incarnation")
	}
	kek, err := f.store.keyring.ByVersion(ctx, backupasset.KeyDomainDerivedStore, old.DerivedKEKVersion)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroBytesLocal(kek.Key)
	envelope, err := newDerivedAEAD(kek.Key)
	if err != nil {
		t.Fatal(err)
	}
	spec := DerivedCryptoSpec{BlobID: old.ID, PlaintextDigest: old.PlaintextDigest, PlaintextSize: old.PlaintextSize, ChunkSize: old.ChunkSize, KEKVersion: old.DerivedKEKVersion, KEK: kek.Key}
	oldDEK, err := envelope.Open(nil, old.EnvelopeNonce, old.WrappedDEK, derivedEnvelopeAAD(spec))
	if err != nil {
		t.Fatal("old envelope authentication failed")
	}
	defer zeroBytesLocal(oldDEK)
	newDEK, err := envelope.Open(nil, before.EnvelopeNonce, before.WrappedDEK, derivedEnvelopeAAD(spec))
	if err != nil {
		t.Fatal("replacement envelope authentication failed")
	}
	defer zeroBytesLocal(newDEK)
	if bytes.Equal(oldDEK, newDEK) {
		t.Fatal("replacement reused old incarnation DEK")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case got := <-finished:
		if got.err != nil || got.count != 0 {
			t.Fatalf("stale rewrap count=%d err=%v", got.count, got.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var after model.BackupAssetDerivedBlob
	if err := f.db.First(&after, "id = ?", blobID).Error; err != nil {
		t.Fatal(err)
	}
	if after.OpaqueLocator != before.OpaqueLocator || after.DerivedKEKVersion != before.DerivedKEKVersion || !bytes.Equal(after.WrappedDEK, before.WrappedDEK) || !bytes.Equal(after.EnvelopeNonce, before.EnvelopeNonce) {
		t.Fatal("stale rewrap altered replacement metadata")
	}
	var out bytes.Buffer
	if err := f.store.readBlob(ctx, blobID, &out); err != nil || !bytes.Equal(out.Bytes(), f.payload) {
		t.Fatalf("replacement read failed: %v", err)
	}
	if err := f.lifecycle.ReadAuthorized(ctx, auth, &bytes.Buffer{}); !errors.Is(err, ErrDerivedUnauthorized) {
		t.Fatalf("revoked source regained authorization: %v", err)
	}
	var ref model.BackupAssetDerivedBlobReference
	if err := f.db.First(&ref, "artifact_id = ?", auth.ArtifactID).Error; err != nil || ref.State == "active" {
		t.Fatalf("old reference remains active: %v", err)
	}
	var revoked model.BackupAssetDerivedArtifactSet
	if err := f.db.First(&revoked, "id = ?", set.ID).Error; err != nil || revoked.State == "active" {
		t.Fatalf("old artifact set remains active: %v", err)
	}
}

func TestProcessingSharedBlobRenewalPostgres(t *testing.T) {
	dsn := sharedAuthorityDSN(t)
	t.Run("DiscardWhileRenewing", func(t *testing.T) { sharedAuthorityRenewal(t, dsn, false) })
	t.Run("RevokeWhileRenewing", func(t *testing.T) { sharedAuthorityRenewal(t, dsn, true) })
	t.Run("CleanupWinsBeforeLateHeartbeat", func(t *testing.T) { sharedAuthorityLateHeartbeat(t, dsn) })
}

func sharedAuthorityRenewal(t *testing.T, dsn string, revoke bool) {
	t.Helper()
	f := newRetirementEvidenceFixture(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var oldSet model.BackupAssetDerivedArtifactSet
	var oldAuth DerivedArtifactAuthorization
	var oldWork retirementEvidenceWork
	if revoke {
		oldWork = f.prepare(t, validWorkDescriptor(), "renewal-published")
		if _, err := f.upload(ctx, oldWork); err != nil {
			t.Fatal(err)
		}
		f.publishAndRead(t, oldWork)
		oldSet, oldAuth = sharedAuthorityPublication(t, f, oldWork)
	}
	descriptor := validWorkDescriptor()
	if revoke {
		descriptor = retirementSecondDescriptor(t, f)
	}
	survivor := f.prepare(t, descriptor, "renewal-staged")
	staged, err := f.upload(ctx, survivor)
	if err != nil {
		t.Fatal(err)
	}
	var initial model.BackupAssetDerivedBlob
	if err := f.db.First(&initial, "id = ?", staged.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	if revoke {
		var artifact model.BackupAssetDerivedArtifact
		if err := f.db.First(&artifact, "artifact_set_id = ?", oldSet.ID).Error; err != nil || artifact.BlobID != staged.BlobID || initial.RefCount != 1 {
			t.Fatalf("published and staged holders do not share one blob: %v", err)
		}
	}
	var attempt model.BackupAssetProcessingAttempt
	var grant model.BackupAssetProcessingGrant
	var pointLease model.RecoveryPointLease
	if err := f.db.First(&attempt, "id = ?", survivor.leased.Lease.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&grant, "id = ?", survivor.leased.Grants.Sink.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&pointLease, "id = ?", attempt.RecoveryPointLeaseID).Error; err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(29 * time.Second)
	var fence backupasset.LeaseFence
	if revoke {
		fence = sharedAuthorityFence(t, f, oldWork)
	}
	renewed, release, cleanupReady := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var renewOnce, releaseOnce, cleanupOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	const updateCallback = "test:shared-authority-heartbeat"
	if err := f.db.Callback().Update().After("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Context.Value(sharedAuthorityContextKey{}) != "heartbeat" || processingCallbackTable(tx) != "backup_asset_processing_grants" || tx.Error != nil || tx.RowsAffected == 0 {
			return
		}
		renewOnce.Do(func() { close(renewed) })
		select {
		case <-release:
		case <-ctx.Done():
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Update().Remove(updateCallback) })
	const queryCallback = "test:shared-authority-cleanup"
	if err := f.db.Callback().Query().Before("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Context.Value(sharedAuthorityContextKey{}) != "cleanup" || processingCallbackTable(tx) != "backup_asset_processing_jobs" {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; locked {
			cleanupOnce.Do(func() { close(cleanupReady) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(queryCallback) })
	type heartbeatResult struct {
		result HeartbeatResult
		err    error
	}
	heartbeatDone := make(chan heartbeatResult, 1)
	go func() {
		result, err := f.coordinator.HeartbeatAttempt(context.WithValue(ctx, sharedAuthorityContextKey{}, "heartbeat"), HeartbeatRequest{AttemptID: survivor.leased.Lease.AttemptID, WorkerID: f.workerID}, f.grants)
		heartbeatDone <- heartbeatResult{result, err}
	}()
	retirementWait(t, ctx, renewed, "uncommitted heartbeat grant updates")
	f.clock.Advance(2 * time.Second)
	now := f.clock.Now()
	if !now.After(attempt.WorkerLeaseExpiresAt) || !now.After(grant.ExpiresAt) || !now.After(pointLease.LeaseExpiresAt) {
		t.Fatal("clock did not advance past all original authority deadlines")
	}
	cleanupDone := make(chan error, 1)
	go func() {
		cleanupCtx := context.WithValue(ctx, sharedAuthorityContextKey{}, "cleanup")
		if revoke {
			cleanupDone <- f.lifecycle.RevokeSetFenced(cleanupCtx, oldSet.ID, DerivedRevokeExpired, fence)
		} else {
			cleanupDone <- f.store.discardBlobIfUnreferenced(cleanupCtx, staged.BlobID)
		}
	}()
	retirementWait(t, ctx, cleanupReady, "cleanup authority job lock")
	releaseOnce.Do(func() { close(release) })
	select {
	case got := <-heartbeatDone:
		if got.err != nil || !got.result.WorkerLeaseExpiresAt.After(now) || !got.result.RecoveryPointLeaseExpiresAt.After(now) || !got.result.GrantExpiresAt.After(now) {
			t.Fatalf("heartbeat did not commit live authority: %v", got.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-cleanupDone:
		if err != nil {
			t.Fatalf("cleanup failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var upload model.BackupAssetProcessingUpload
	var blob model.BackupAssetDerivedBlob
	if err := f.db.First(&upload, "id = ?", staged.UploadID).Error; err != nil || upload.State != "staged" {
		t.Fatalf("renewed upload was orphaned: %v", err)
	}
	if err := f.db.First(&blob, "id = ?", staged.BlobID).Error; err != nil || blob.State != "active" || blob.RefCount != 0 || len(blob.WrappedDEK) == 0 || blob.OpaqueLocator != initial.OpaqueLocator {
		t.Fatalf("renewed blob was destroyed or miscounted: %v", err)
	}
	if revoke {
		if err := f.lifecycle.ReadAuthorized(ctx, oldAuth, &bytes.Buffer{}); !errors.Is(err, ErrDerivedUnauthorized) {
			t.Fatalf("revoked source remains authorized: %v", err)
		}
		if err := f.lease.Release(ctx, fence); err != nil {
			t.Fatal(err)
		}
	}
	if id := f.publishAndRead(t, survivor); id != staged.BlobID {
		t.Fatal("survivor published a different blob")
	}
	if err := f.db.First(&blob, "id = ?", staged.BlobID).Error; err != nil || blob.State != "active" || blob.RefCount != 1 || len(blob.WrappedDEK) == 0 {
		t.Fatalf("published survivor has incorrect blob state: %v", err)
	}
}

// Cleanup obtains the staged holder's job lock first. A heartbeat that began
// before expiry must not extend the old authority after waiting past expiry.
func sharedAuthorityLateHeartbeat(t *testing.T, dsn string) {
	t.Helper()
	f := newRetirementEvidenceFixture(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	w := f.prepare(t, validWorkDescriptor(), "late-heartbeat")
	staged, err := f.upload(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(29 * time.Second)
	locked, waiting, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var lockedOnce, waitingOnce, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	const beforeCallback = "test:shared-authority-late-heartbeat"
	if err := f.db.Callback().Query().Before("gorm:query").Register(beforeCallback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Context.Value(sharedAuthorityContextKey{}) != "late-heartbeat" || processingCallbackTable(tx) != "backup_asset_processing_jobs" {
			return
		}
		if _, ok := tx.Statement.Clauses["FOR"]; ok {
			waitingOnce.Do(func() { close(waiting) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(beforeCallback) })
	const afterCallback = "test:shared-authority-cleanup-lock-held"
	if err := f.db.Callback().Query().After("gorm:query").Register(afterCallback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Context.Value(sharedAuthorityContextKey{}) != "cleanup-first" || processingCallbackTable(tx) != "backup_asset_processing_jobs" || tx.Error != nil || tx.RowsAffected == 0 {
			return
		}
		if _, ok := tx.Statement.Clauses["FOR"]; !ok {
			return
		}
		lockedOnce.Do(func() { close(locked) })
		select {
		case <-release:
		case <-ctx.Done():
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(afterCallback) })
	cleanupDone := make(chan error, 1)
	go func() {
		cleanupDone <- f.store.discardBlobIfUnreferenced(context.WithValue(ctx, sharedAuthorityContextKey{}, "cleanup-first"), staged.BlobID)
	}()
	retirementWait(t, ctx, locked, "cleanup holding holder job lock")
	heartbeatDone := make(chan error, 1)
	go func() {
		_, err := f.coordinator.HeartbeatAttempt(context.WithValue(ctx, sharedAuthorityContextKey{}, "late-heartbeat"), HeartbeatRequest{AttemptID: w.leased.Lease.AttemptID, WorkerID: f.workerID}, f.grants)
		heartbeatDone <- err
	}()
	retirementWait(t, ctx, waiting, "heartbeat waiting on holder job")
	f.clock.Advance(2 * time.Second)
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-cleanupDone:
		if err != nil {
			t.Fatalf("expired holder cleanup failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-heartbeatDone:
		if err == nil {
			t.Fatal("heartbeat renewed authority after its original deadline")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var blob model.BackupAssetDerivedBlob
	var upload model.BackupAssetProcessingUpload
	if err := f.db.First(&blob, "id = ?", staged.BlobID).Error; err != nil || blob.State == "active" || len(blob.WrappedDEK) != 0 {
		t.Fatalf("expired holder kept key material: %v", err)
	}
	if err := f.db.First(&upload, "id = ?", staged.UploadID).Error; err != nil || upload.State != "orphaned" {
		t.Fatalf("expired upload was not orphaned: %v", err)
	}
	if err := f.store.readBlob(ctx, staged.BlobID, &bytes.Buffer{}); err == nil {
		t.Fatal("expired blob remained readable")
	}
}
