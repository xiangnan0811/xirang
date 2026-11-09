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
	"gorm.io/gorm/clause"
)

type retirementEvidenceContextKey struct{}

// TestProcessingRetirementStagingPostgres exercises real PostgreSQL row locks;
// the controlled source and projection fixture do not represent a remote provider.
func TestProcessingRetirementStagingPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_PROCESSING_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	t.Run("Expiry", func(t *testing.T) { retirementStagingRace(t, dsn, false) })
	t.Run("Shutdown", func(t *testing.T) { retirementStagingRace(t, dsn, true) })
	t.Run("RenewalBeforeRetirementLock", func(t *testing.T) { retirementRenewalRace(t, dsn) })
	t.Run("DiscardWhileStaging", func(t *testing.T) { retirementDiscardRace(t, dsn) })
}

type retirementEvidenceFixture struct {
	processingBehaviorFixture
	store       *DerivedStore
	lifecycle   *DerivedLifecycle
	sink        *ArtifactSink
	declaration ArtifactDeclaration
	payload     []byte
}

type retirementEvidenceWork struct {
	job        WorkResult
	leased     LeasedAttempt
	descriptor WorkDescriptorV1
}

func newRetirementEvidenceFixture(t *testing.T, dsn string) retirementEvidenceFixture {
	t.Helper()
	f := openProcessingBehaviorPostgres(t, dsn)
	ctx := context.Background()
	keys := backupasset.NewKeyring(f.db, f.clock.Now)
	if _, err := keys.Ensure(ctx, backupasset.KeyDomainDerivedStore); err != nil {
		t.Fatal(err)
	}
	store, err := NewDerivedStore(ctx, f.db, keys, DerivedStoreConfig{Root: filepath.Join(t.TempDir(), "derived"), ChunkSize: 64 << 10, BlobMaxBytes: 1 << 20, GlobalMaxBytes: 8 << 20, ValidateRoot: func(context.Context, string) error { return nil }}, f.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	projection := &processingBehaviorProjection{db: f.db}
	lifecycle, err := NewDerivedLifecycle(f.db, store, projection, f.clock.Now, f.lease)
	if err != nil {
		t.Fatal(err)
	}
	d := validWorkDescriptor()
	sink, err := NewArtifactSink(f.db, f.lease, f.grants, store, lifecycle, &manifestSourceRevalidator{}, func(context.Context) (string, error) { return d.SecurityPolicyRevision, nil }, func(context.Context, string, string) (string, error) { return d.PipelineFingerprint, nil }, f.clock.Now, ArtifactSinkConfig{MaxArtifacts: 4, MaxArtifactBytes: 1 << 20, MaxTotalBytes: 4 << 20})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("retirement shared output")
	return retirementEvidenceFixture{processingBehaviorFixture: f, store: store, lifecycle: lifecycle, sink: sink, declaration: artifactDeclaration(0, ArtifactRoleNoop, "application/octet-stream", payload), payload: payload}
}

func (f retirementEvidenceFixture) prepare(t *testing.T, d WorkDescriptorV1, owner string) retirementEvidenceWork {
	t.Helper()
	ctx := context.Background()
	job, err := f.coordinator.RequestWork(ctx, WorkRequest{Descriptor: d, Interest: InterestRequest{OwnerKind: InterestSystem, OwnerKey: owner, PriorityClass: PriorityInteractive, Priority: 100}})
	if err != nil {
		t.Fatal(err)
	}
	leased, err := f.coordinator.PullAttempt(ctx, PullRequest{WorkerID: f.workerID}, f.grants)
	if err != nil || leased.Lease.JobID != job.JobID {
		t.Fatalf("pull: %+v %v", leased, err)
	}
	_, err = f.grants.Activate(ctx, ActivateGrantRequest{GrantID: leased.Grants.Sink.GrantID, Kind: GrantSink, JobID: job.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: f.workerID, Secret: leased.Grants.Sink.Secret})
	if err != nil {
		t.Fatal(err)
	}
	for i, state := range []ProcessingState{ProcessingFetching, ProcessingMaterializing, ProcessingProcessing, ProcessingUploading} {
		processingBehaviorTransition(t, f.processingBehaviorFixture, job.JobID, leased.Lease.AttemptID, state, int64(i+2))
	}
	return retirementEvidenceWork{job: job, leased: leased, descriptor: d}
}

func (f retirementEvidenceFixture) upload(ctx context.Context, w retirementEvidenceWork) (UploadedArtifact, error) {
	return f.sink.UploadArtifact(ctx, UploadArtifactRequest{JobID: w.job.JobID, AttemptID: w.leased.Lease.AttemptID, WorkerID: f.workerID, GrantID: w.leased.Grants.Sink.GrantID, Artifact: f.declaration}, bytes.NewReader(f.payload))
}

func (f retirementEvidenceFixture) publishAndRead(t *testing.T, w retirementEvidenceWork) string {
	t.Helper()
	result, err := f.sink.CommitManifest(context.Background(), CommitManifestRequest{JobID: w.job.JobID, AttemptID: w.leased.Lease.AttemptID, WorkerID: f.workerID, GrantID: w.leased.Grants.Sink.GrantID, RecoveryPointFence: w.leased.Lease.RecoveryPointFence, SecurityPolicyRevision: w.descriptor.SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{f.declaration}})
	if err != nil {
		t.Fatalf("publish survivor: %v", err)
	}
	var artifact model.BackupAssetDerivedArtifact
	if err := f.db.First(&artifact, "artifact_set_id = ?", result.ArtifactSetID).Error; err != nil {
		t.Fatal(err)
	}
	auth := DerivedArtifactAuthorization{ArtifactID: artifact.ID, RecoveryPointID: w.descriptor.Source.RecoveryPointID, CatalogGenerationID: w.descriptor.CatalogGenerationID, EntryID: w.descriptor.Source.EntryID, SourceFingerprint: w.descriptor.SourceFingerprint}
	var out bytes.Buffer
	if err := f.lifecycle.ReadAuthorized(context.Background(), auth, &out); err != nil || !bytes.Equal(out.Bytes(), f.payload) {
		t.Fatalf("survivor read: %v", err)
	}
	return artifact.BlobID
}

func retirementSecondDescriptor(t *testing.T, f retirementEvidenceFixture) WorkDescriptorV1 {
	t.Helper()
	d := validWorkDescriptor()
	d.Source.RecoveryPointID = strings.Repeat("1", 32)
	d.CatalogGenerationID = strings.Repeat("2", 32)
	d.Source.EntryID = strings.Repeat("3", 64)
	processingBehaviorSeedSecondPoint(t, f.processingBehaviorFixture, d)
	return d
}

func retirementWait(t *testing.T, ctx context.Context, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-ctx.Done():
		t.Fatalf("waiting for %s: %v", label, ctx.Err())
	}
}

func retirementStagingRace(t *testing.T, dsn string, shutdown bool) {
	f := newRetirementEvidenceFixture(t, dsn)
	survivor := f.prepare(t, retirementSecondDescriptor(t, f), "retirement-survivor")
	if _, err := f.upload(context.Background(), survivor); err != nil {
		t.Fatal(err)
	}
	blobID := f.publishAndRead(t, survivor)
	target := f.prepare(t, validWorkDescriptor(), "retirement-target")
	reconciler, err := NewReconciler(f.coordinator, f.grants, f.clock.Now, ReconcilerConfig{BatchSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	control := f.db.WithContext(ctx).Begin()
	if control.Error != nil {
		t.Fatal(control.Error)
	}
	defer control.Rollback()
	var point model.RecoveryPoint
	if err := control.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", target.descriptor.Source.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	uploadReady, retireReady, releaseUpload := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var uploadOnce, retireOnce sync.Once
	var lockMu sync.Mutex
	var retirementLocks []string
	const callback = "test:retirement-staging-point"
	if err := f.db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Context == nil {
			return
		}
		table := processingCallbackTable(tx)
		if _, ok := tx.Statement.Clauses["FOR"]; !ok {
			return
		}
		if tx.Statement.Context.Value(retirementEvidenceContextKey{}) == "retire" && (table == "recovery_points" || table == "backup_asset_processing_jobs" || table == "backup_asset_processing_attempts") {
			lockMu.Lock()
			retirementLocks = append(retirementLocks, table)
			lockMu.Unlock()
		}
		if table != "recovery_points" {
			return
		}
		switch tx.Statement.Context.Value(retirementEvidenceContextKey{}) {
		case "upload":
			uploadOnce.Do(func() {
				close(uploadReady)
				select {
				case <-releaseUpload:
				case <-ctx.Done():
				}
			})
		case "retire":
			retireOnce.Do(func() { close(retireReady) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(callback) })
	uploadResult := make(chan error, 1)
	go func() {
		_, err := f.upload(context.WithValue(ctx, retirementEvidenceContextKey{}, "upload"), target)
		uploadResult <- err
	}()
	retirementWait(t, ctx, uploadReady, "upload after putReplay")
	if !shutdown {
		f.clock.Advance(31 * time.Second)
	}
	retireResult := make(chan error, 1)
	go func() {
		c := context.WithValue(ctx, retirementEvidenceContextKey{}, "retire")
		if shutdown {
			_, err := reconciler.Shutdown(c)
			retireResult <- err
			return
		}
		_, err := reconciler.Reconcile(c)
		retireResult <- err
	}()
	retirementWait(t, ctx, retireReady, "retirement point lock")
	if err := control.Commit().Error; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-retireResult:
		if err != nil {
			t.Fatalf("retire: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	lockMu.Lock()
	if len(retirementLocks) < 3 || retirementLocks[0] != "recovery_points" || retirementLocks[1] != "backup_asset_processing_jobs" || retirementLocks[2] != "backup_asset_processing_attempts" {
		t.Errorf("retirement lock order: %v", retirementLocks)
	}
	lockMu.Unlock()
	close(releaseUpload)
	select {
	case err := <-uploadResult:
		if err == nil {
			t.Fatal("retired upload staged")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var upload model.BackupAssetProcessingUpload
	if err := f.db.Where("attempt_id = ?", target.leased.Lease.AttemptID).First(&upload).Error; err != nil || upload.State != "rejected" {
		t.Fatalf("retired upload state=%s err=%v", upload.State, err)
	}
	var sets, refs int64
	if err := f.db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("job_id = ?", target.job.JobID).Count(&sets).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.BackupAssetDerivedBlobReference{}).Where("blob_id = ? AND state = ?", blobID, "active").Count(&refs).Error; err != nil {
		t.Fatal(err)
	}
	var blob model.BackupAssetDerivedBlob
	if err := f.db.First(&blob, "id = ?", blobID).Error; err != nil || sets != 0 || refs != 1 || blob.RefCount != 1 || blob.State != "active" {
		t.Fatalf("retirement damaged publication: sets=%d refs=%d blob=%+v err=%v", sets, refs, blob, err)
	}
	var artifact model.BackupAssetDerivedArtifact
	if err := f.db.First(&artifact, "blob_id = ?", blobID).Error; err != nil {
		t.Fatal(err)
	}
	auth := DerivedArtifactAuthorization{ArtifactID: artifact.ID, RecoveryPointID: survivor.descriptor.Source.RecoveryPointID, CatalogGenerationID: survivor.descriptor.CatalogGenerationID, EntryID: survivor.descriptor.Source.EntryID, SourceFingerprint: survivor.descriptor.SourceFingerprint}
	var out bytes.Buffer
	if err := f.lifecycle.ReadAuthorized(context.Background(), auth, &out); err != nil || !bytes.Equal(out.Bytes(), f.payload) {
		t.Fatalf("survivor read after retirement: %v", err)
	}
}

func retirementRenewalRace(t *testing.T, dsn string) {
	f := newRetirementEvidenceFixture(t, dsn)
	w := f.prepare(t, validWorkDescriptor(), "renew-before-retirement")
	if _, err := f.upload(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	// Renew before the original leases expire, but keep that renewal uncommitted
	// until the reconciler has observed the original deadline as expired.
	f.clock.Advance(29 * time.Second)
	renewedAt := f.clock.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	control := f.db.WithContext(ctx).Begin()
	if control.Error != nil {
		t.Fatal(control.Error)
	}
	defer control.Rollback()
	var point model.RecoveryPoint
	if err := control.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", w.descriptor.Source.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	var job model.BackupAssetProcessingJob
	if err := control.Clauses(clause.Locking{Strength: "UPDATE"}).First(&job, "id = ?", w.job.JobID).Error; err != nil {
		t.Fatal(err)
	}
	var attempt model.BackupAssetProcessingAttempt
	if err := control.Clauses(clause.Locking{Strength: "UPDATE"}).First(&attempt, "id = ?", w.leased.Lease.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	pointLease, err := f.lease.RenewTx(ctx, control, w.leased.Lease.RecoveryPointFence)
	if err != nil {
		t.Fatal(err)
	}
	workerDeadline := renewedAt.Add(30 * time.Second)
	if err := control.Model(&model.BackupAssetProcessingAttempt{}).Where("id = ?", attempt.ID).Updates(map[string]any{
		"worker_lease_expires_at": workerDeadline, "last_heartbeat_at": renewedAt, "updated_at": renewedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.grants.renewAttemptGrantsTx(ctx, control, attempt.ID, minimumTime(workerDeadline, pointLease.LeaseExpiresAt)); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(2 * time.Second)
	ready := make(chan struct{})
	var once sync.Once
	const callback = "test:retirement-renew-point"
	if err := f.db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Context == nil || tx.Statement.Context.Value(retirementEvidenceContextKey{}) != "renew" || processingCallbackTable(tx) != "recovery_points" {
			return
		}
		if _, ok := tx.Statement.Clauses["FOR"]; ok {
			once.Do(func() { close(ready) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(callback) })
	reconciler, err := NewReconciler(f.coordinator, f.grants, f.clock.Now, ReconcilerConfig{BatchSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		counts ReconcileResult
		err    error
	}
	finished := make(chan result, 1)
	go func() {
		counts, err := reconciler.Reconcile(context.WithValue(ctx, retirementEvidenceContextKey{}, "renew"))
		finished <- result{counts, err}
	}()
	retirementWait(t, ctx, ready, "expiry after stale observation")
	if err := control.Commit().Error; err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-finished:
		if got.err != nil || got.counts.ExpiredAttempts != 0 {
			t.Fatalf("renewed attempt retired: %+v %v", got.counts, got.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var current model.BackupAssetProcessingAttempt
	if err := f.db.First(&current, "id = ?", w.leased.Lease.AttemptID).Error; err != nil || current.State != "active" || !current.IsCurrent || !current.WorkerLeaseExpiresAt.Equal(workerDeadline) {
		t.Fatalf("renewal lost: %+v %v", current, err)
	}
	f.publishAndRead(t, w)
}

func retirementDiscardRace(t *testing.T, dsn string) {
	f := newRetirementEvidenceFixture(t, dsn)
	survivor := f.prepare(t, retirementSecondDescriptor(t, f), "discard-survivor")
	staged, err := f.upload(context.Background(), survivor)
	if err != nil {
		t.Fatal(err)
	}
	target := f.prepare(t, validWorkDescriptor(), "discard-target")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	control := f.db.WithContext(ctx).Begin()
	if control.Error != nil {
		t.Fatal(control.Error)
	}
	defer control.Rollback()
	var blob model.BackupAssetDerivedBlob
	if err := control.Clauses(clause.Locking{Strength: "UPDATE"}).First(&blob, "id = ?", staged.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	uploadReady, discardReady := make(chan struct{}), make(chan struct{})
	var uploadOnce, discardOnce sync.Once
	const callback = "test:discard-staging-blob"
	if err := f.db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Context == nil || processingCallbackTable(tx) != "backup_asset_derived_blobs" {
			return
		}
		if _, ok := tx.Statement.Clauses["FOR"]; !ok {
			return
		}
		switch tx.Statement.Context.Value(retirementEvidenceContextKey{}) {
		case "stage":
			uploadOnce.Do(func() { close(uploadReady) })
		case "discard":
			discardOnce.Do(func() { close(discardReady) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(callback) })
	uploadResult := make(chan error, 1)
	go func() {
		_, err := f.upload(context.WithValue(ctx, retirementEvidenceContextKey{}, "stage"), target)
		uploadResult <- err
	}()
	retirementWait(t, ctx, uploadReady, "staging blob lock")
	discardResult := make(chan error, 1)
	go func() {
		discardResult <- f.store.discardBlobIfUnreferenced(context.WithValue(ctx, retirementEvidenceContextKey{}, "discard"), staged.BlobID)
	}()
	retirementWait(t, ctx, discardReady, "discard blob lock")
	if err := control.Commit().Error; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-uploadResult:
		if err != nil {
			t.Fatalf("stage: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-discardResult:
		if err != nil {
			t.Fatalf("discard: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if id := f.publishAndRead(t, survivor); id != staged.BlobID {
		t.Fatal("surviving staged holder lost blob identity")
	}
	if id := f.publishAndRead(t, target); id != staged.BlobID {
		t.Fatal("second staged holder lost blob identity")
	}
	if err := f.db.First(&blob, "id = ?", staged.BlobID).Error; err != nil || blob.State != "active" || blob.RefCount != 2 || len(blob.WrappedDEK) == 0 {
		t.Fatalf("shared blob after discard: %+v %v", blob, err)
	}
	if _, err := os.Stat(filepath.Join(f.store.config.Root, blob.OpaqueLocator)); errors.Is(err, os.ErrNotExist) || err != nil {
		t.Fatalf("shared ciphertext missing: %v", err)
	}
}
