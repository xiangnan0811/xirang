package processing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

func TestSinkCommitsBoundedMultiArtifactManifestAtomicallyUnderCurrentFence(t *testing.T) {
	harness := newManifestHarness(t)
	harness.moveJobToUploading(t)
	payloads := [][]byte{[]byte("passive-noop-result"), bytes.Repeat([]byte("metadata"), 64)}
	declarations := []ArtifactDeclaration{
		artifactDeclaration(0, ArtifactRoleNoop, "application/octet-stream", payloads[0]),
		artifactDeclaration(1, ArtifactRoleMetadata, "application/json", payloads[1]),
	}
	for index := range declarations {
		uploaded, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
			JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
			GrantID: harness.sinkGrantID, Artifact: declarations[index],
		}, bytes.NewReader(payloads[index]))
		if err != nil || uploaded.Ordinal != index || uploaded.BlobID == "" {
			t.Fatalf("UploadArtifact(%d): uploaded=%+v err=%v", index, uploaded, err)
		}
	}
	result, err := harness.sink.CommitManifest(context.Background(), CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: declarations,
	})
	if err != nil {
		t.Fatalf("CommitManifest: %v", err)
	}
	if result.ArtifactSetID == "" || result.ManifestDigest == "" || result.ProjectionRequired {
		t.Fatalf("manifest result invalid: %+v", result)
	}
	var set model.BackupAssetDerivedArtifactSet
	if err := harness.db.First(&set, "id = ?", result.ArtifactSetID).Error; err != nil {
		t.Fatal(err)
	}
	var artifacts, references int64
	if err := harness.db.Model(&model.BackupAssetDerivedArtifact{}).Where("artifact_set_id = ?", set.ID).Count(&artifacts).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetDerivedBlobReference{}).Where("artifact_id IN (?)",
		harness.db.Model(&model.BackupAssetDerivedArtifact{}).Select("id").Where("artifact_set_id = ?", set.ID)).Count(&references).Error; err != nil {
		t.Fatal(err)
	}
	var job model.BackupAssetProcessingJob
	var attempt model.BackupAssetProcessingAttempt
	var grant model.BackupAssetProcessingGrant
	if err := harness.db.First(&job, "id = ?", harness.lease.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&attempt, "id = ?", harness.lease.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&grant, "id = ?", harness.sinkGrantID).Error; err != nil {
		t.Fatal(err)
	}
	if artifacts != 2 || references != 2 || set.State != "active" || job.State != string(ProcessingSucceeded) || job.IsCurrent ||
		attempt.State != "succeeded" || attempt.IsCurrent || grant.State != string(GrantClosed) {
		t.Fatalf("manifest did not publish atomically: set=%+v job=%+v attempt=%+v grant=%+v artifacts=%d refs=%d",
			set, job, attempt, grant, artifacts, references)
	}
}

func TestDerivedManifestCleanupContextDetachesCancellationButStaysBounded(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	cleanup, cancelCleanup := derivedManifestCleanupContext(parent)
	defer cancelCleanup()
	if err := cleanup.Err(); err != nil {
		t.Fatalf("cleanup inherited request cancellation: %v", err)
	}
	deadline, ok := cleanup.Deadline()
	if !ok {
		t.Fatal("cleanup context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > 5*time.Second {
		t.Fatalf("cleanup deadline is not bounded: %s", remaining)
	}
}

func TestSinkConcurrentAdmissionCannotExceedAtomicTotalQuota(t *testing.T) {
	harness := newManifestHarness(t)
	harness.moveJobToUploading(t)
	payload := bytes.Repeat([]byte("q"), 1024)
	harness.sink.config = ArtifactSinkConfig{MaxArtifacts: 2, MaxArtifactBytes: int64(len(payload)), MaxTotalBytes: int64(len(payload))}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	results := make(chan error, 2)
	var releaseOnce sync.Once
	for ordinal := 0; ordinal < 2; ordinal++ {
		declaration := artifactDeclaration(ordinal, ArtifactRoleNoop, "application/octet-stream", payload)
		go func() {
			_, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
				JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
				GrantID: harness.sinkGrantID, Artifact: declaration,
			}, &barrierArtifactReader{payload: payload, started: started, release: release})
			results <- err
		}()
	}
	startedCount := 0
	completedEarly := 0
	var firstEarly error
	deadline := time.After(2 * time.Second)
	for startedCount < 2 && completedEarly == 0 {
		select {
		case <-started:
			startedCount++
		case firstEarly = <-results:
			completedEarly++
		case <-deadline:
			releaseOnce.Do(func() { close(release) })
			t.Fatal("concurrent upload admission stalled")
		}
	}
	releaseOnce.Do(func() { close(release) })
	errorsSeen := []error{}
	if completedEarly != 0 {
		errorsSeen = append(errorsSeen, firstEarly)
	}
	for len(errorsSeen) < 2 {
		errorsSeen = append(errorsSeen, <-results)
	}
	succeeded := 0
	for _, err := range errorsSeen {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, ErrDerivedQuotaExceeded) && !errors.Is(err, ErrInvalidArtifact) {
			t.Fatalf("unexpected concurrent admission error: %v", err)
		}
	}
	var count, total int64
	if err := harness.db.Model(&model.BackupAssetProcessingUpload{}).Where("grant_id = ? AND state = ?", harness.sinkGrantID, "staged").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetProcessingUpload{}).Where("grant_id = ? AND state = ?", harness.sinkGrantID, "staged").
		Select("COALESCE(SUM(declared_size), 0)").Scan(&total).Error; err != nil {
		t.Fatal(err)
	}
	if succeeded != 1 || count != 1 || total != int64(len(payload)) {
		t.Fatalf("quota over-admitted succeeded=%d count=%d total=%d errors=%v", succeeded, count, total, errorsSeen)
	}
}

type barrierArtifactReader struct {
	payload []byte
	started chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (reader *barrierArtifactReader) Read(destination []byte) (int, error) {
	reader.once.Do(func() {
		reader.started <- struct{}{}
		<-reader.release
	})
	if len(reader.payload) == 0 {
		return 0, io.EOF
	}
	count := copy(destination, reader.payload)
	reader.payload = reader.payload[count:]
	return count, nil
}

func TestSinkRejectsLateFenceAndLeavesNoVisibleArtifactSet(t *testing.T) {
	harness := newManifestHarness(t)
	harness.moveJobToUploading(t)
	payload := []byte("late-output")
	declaration := artifactDeclaration(0, ArtifactRoleNoop, "application/octet-stream", payload)
	if _, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, Artifact: declaration,
	}, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.RecoveryPointLease{}).Where("id = ?", harness.lease.RecoveryPointFence.LeaseID).
		Update("fence_token", strings.Repeat("e", 64)).Error; err != nil {
		t.Fatal(err)
	}
	_, err := harness.sink.CommitManifest(context.Background(), CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	})
	if !errors.Is(err, ErrManifestFenceLost) {
		t.Fatalf("late manifest got %v", err)
	}
	var count int64
	if err := harness.db.Model(&model.BackupAssetDerivedArtifactSet{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("late output became visible: count=%d err=%v", count, err)
	}
}

func TestSinkValidatesRoleMIMESizeDigestCompletenessAndPolicy(t *testing.T) {
	harness := newManifestHarness(t)
	harness.moveJobToUploading(t)
	payload := []byte("invalid-products")
	valid := artifactDeclaration(0, ArtifactRoleNoop, "application/octet-stream", payload)
	invalid := []ArtifactDeclaration{
		withArtifactRole(valid, ArtifactRole("future")),
		withArtifactMIME(valid, "text/html"),
		withArtifactSize(valid, int64(len(payload))+1),
		withArtifactDigest(valid, strings.Repeat("0", 64)),
		withArtifactCompleteness(valid, ArtifactCompleteness("unknown")),
	}
	for index, declaration := range invalid {
		if _, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
			JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
			GrantID: harness.sinkGrantID, Artifact: declaration,
		}, bytes.NewReader(payload)); !errors.Is(err, ErrInvalidArtifact) {
			t.Fatalf("invalid artifact %d got %v", index, err)
		}
	}
	if _, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, Artifact: valid,
	}, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	_, err := harness.sink.CommitManifest(context.Background(), CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: "stale-policy", Artifacts: []ArtifactDeclaration{valid},
	})
	if !errors.Is(err, ErrManifestPolicyChanged) {
		t.Fatalf("stale policy got %v", err)
	}
}

func TestSinkMetricsCountOnlySuccessfullyAcceptedPlaintextBytes(t *testing.T) {
	harness := newManifestHarness(t)
	harness.moveJobToUploading(t)
	metrics := &manifestMetricsFake{}
	harness.sink.SetMetrics(metrics)
	payload := []byte("bounded-sink-metrics")
	invalid := withArtifactDigest(artifactDeclaration(0, ArtifactRoleNoop, "application/octet-stream", payload), strings.Repeat("0", 64))
	if _, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, Artifact: invalid,
	}, bytes.NewReader(payload)); err == nil {
		t.Fatal("digest-mismatched upload unexpectedly succeeded")
	}
	if metrics.sinkBytes != 0 {
		t.Fatalf("failed upload counted %d Sink bytes", metrics.sinkBytes)
	}
	valid := artifactDeclaration(1, ArtifactRoleNoop, "application/octet-stream", payload)
	if _, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, Artifact: valid,
	}, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	if metrics.sinkBytes != int64(len(payload)) {
		t.Fatalf("successful upload counted %d Sink bytes, want %d", metrics.sinkBytes, len(payload))
	}
	rejected := withArtifactMIME(artifactDeclaration(2, ArtifactRoleNoop, "application/octet-stream", payload), "text/html")
	if _, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, Artifact: rejected,
	}, bytes.NewReader(payload)); err == nil {
		t.Fatal("invalid MIME upload unexpectedly succeeded")
	}
	if metrics.sinkBytes != int64(len(payload)) {
		t.Fatalf("rejected upload changed Sink bytes to %d", metrics.sinkBytes)
	}
}

func TestProjectionFailureLeavesReadableSetPendingAndJobNotSucceeded(t *testing.T) {
	harness := newManifestHarness(t)
	harness.moveJobToUploading(t)
	payload := []byte("projected-content")
	declaration := artifactDeclaration(0, ArtifactRoleContent, "text/plain", payload)
	if _, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, Artifact: declaration,
	}, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	harness.projection.onPublish = func(request DerivedProjectionPublish) error {
		var set model.BackupAssetDerivedArtifactSet
		if err := harness.db.First(&set, "id = ?", request.ArtifactSetID).Error; err != nil {
			return err
		}
		if set.State != "active" || set.ProjectionPublished {
			return errors.New("projection observed unreadable or already-published set")
		}
		return errors.New("search temporarily unavailable")
	}
	result, err := harness.sink.CommitManifest(context.Background(), CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	})
	if err == nil || !result.ProjectionRequired || harness.projection.publications != 1 {
		t.Fatalf("projection failure result=%+v publications=%d err=%v", result, harness.projection.publications, err)
	}
	var job model.BackupAssetProcessingJob
	var set model.BackupAssetDerivedArtifactSet
	if err := harness.db.First(&job, "id = ?", harness.lease.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&set, "id = ?", result.ArtifactSetID).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(ProcessingValidating) || set.State != "active" || set.ProjectionPublished {
		t.Fatalf("projection failure exposed success or destroyed forward state: job=%+v set=%+v", job, set)
	}
}

func TestPendingProjectionRecoveryTakesOverExpiredFenceAndCompletesSameSet(t *testing.T) {
	harness := newManifestHarness(t)
	harness.moveJobToUploading(t)
	payload := []byte("crash-safe-projected-content")
	declaration := artifactDeclaration(0, ArtifactRoleContent, "text/plain", payload)
	if _, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, Artifact: declaration,
	}, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	var publishedSetID string
	harness.projection.onPublish = func(request DerivedProjectionPublish) error {
		if publishedSetID == "" {
			publishedSetID = request.ArtifactSetID
			return errors.New("projection commit acknowledgement lost")
		}
		if request.ArtifactSetID != publishedSetID {
			return errors.New("projection replay changed artifact set identity")
		}
		return nil
	}
	result, err := harness.sink.CommitManifest(context.Background(), CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	})
	if err == nil || result.ArtifactSetID == "" || result.ArtifactSetID != publishedSetID {
		t.Fatalf("ambiguous projection commit result=%+v published=%q err=%v", result, publishedSetID, err)
	}

	harness.clock.Advance(31 * time.Second)
	recovered, err := harness.sink.ReconcilePendingProjections(context.Background(), 32)
	if err != nil || recovered != 1 {
		t.Fatalf("ReconcilePendingProjections=%d err=%v", recovered, err)
	}
	if harness.projection.publications != 2 || len(harness.projection.publishRequests) != 2 {
		t.Fatalf("projection replay count=%d requests=%+v", harness.projection.publications, harness.projection.publishRequests)
	}
	oldFence := harness.projection.publishRequests[0].RecoveryPointFence
	newFence := harness.projection.publishRequests[1].RecoveryPointFence
	if newFence.LeaseID != oldFence.LeaseID || newFence.AttemptID == oldFence.AttemptID || newFence.FenceToken == oldFence.FenceToken {
		t.Fatalf("projection recovery reused expired authority: old=%+v new=%+v", oldFence, newFence)
	}
	if err := harness.coordinator.leaseService.ValidateFence(context.Background(), oldFence); err == nil {
		t.Fatal("old projection fence remained valid after recovery takeover")
	}

	var job model.BackupAssetProcessingJob
	var attempt model.BackupAssetProcessingAttempt
	var set model.BackupAssetDerivedArtifactSet
	var lease model.RecoveryPointLease
	if err := harness.db.First(&job, "id = ?", harness.lease.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&attempt, "id = ?", harness.lease.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&set, "id = ?", result.ArtifactSetID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&lease, "id = ?", newFence.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(ProcessingSucceeded) || job.IsCurrent || attempt.State != "succeeded" || attempt.IsCurrent ||
		set.State != "active" || !set.ProjectionPublished || set.ProjectionRevision <= 0 ||
		lease.Status != string(backupasset.LeaseReleased) {
		t.Fatalf("projection recovery did not close atomically: job=%+v attempt=%+v set=%+v lease=%+v", job, attempt, set, lease)
	}
	if attempt.RecoveryPointAttemptID != newFence.AttemptID || attempt.RecoveryPointFenceHash != hashFence(newFence.FenceToken) {
		t.Fatalf("attempt did not persist takeover binding: attempt=%+v fence=%+v", attempt, newFence)
	}
	if recovered, err = harness.sink.ReconcilePendingProjections(context.Background(), 32); err != nil || recovered != 0 || harness.projection.publications != 2 {
		t.Fatalf("idempotent projection recovery=%d publications=%d err=%v", recovered, harness.projection.publications, err)
	}
}

func TestPendingProjectionRecoveryReusesTakeoverFenceAfterTransientReplayFailure(t *testing.T) {
	harness := newManifestHarness(t)
	harness.moveJobToUploading(t)
	payload := []byte("projection-takeover-retry")
	declaration := artifactDeclaration(0, ArtifactRoleContent, "text/plain", payload)
	if _, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, Artifact: declaration,
	}, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	var callMu sync.Mutex
	calls := 0
	harness.projection.onPublish = func(DerivedProjectionPublish) error {
		callMu.Lock()
		defer callMu.Unlock()
		calls++
		if calls <= 2 {
			return errors.New("projection temporarily unavailable")
		}
		return nil
	}
	result, err := harness.sink.CommitManifest(context.Background(), CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	})
	if err == nil {
		t.Fatal("first projection publication unexpectedly succeeded")
	}
	harness.clock.Advance(31 * time.Second)
	if _, err := harness.sink.ReconcilePendingProjections(context.Background(), 32); err == nil {
		t.Fatal("first takeover replay unexpectedly succeeded")
	}
	if len(harness.projection.publishRequests) != 2 {
		t.Fatalf("first takeover replay requests=%d", len(harness.projection.publishRequests))
	}
	takeoverFence := harness.projection.publishRequests[1].RecoveryPointFence
	recovered, err := harness.sink.ReconcilePendingProjections(context.Background(), 32)
	if err != nil || recovered != 1 {
		t.Fatalf("immediate takeover retry recovered=%d err=%v", recovered, err)
	}
	if len(harness.projection.publishRequests) != 3 {
		t.Fatalf("takeover retry requests=%d", len(harness.projection.publishRequests))
	}
	retryFence := harness.projection.publishRequests[2].RecoveryPointFence
	if retryFence != takeoverFence {
		t.Fatalf("transient replay failure rotated recovery authority: first=%+v retry=%+v", takeoverFence, retryFence)
	}
	var set model.BackupAssetDerivedArtifactSet
	if err := harness.db.First(&set, "id = ?", result.ArtifactSetID).Error; err != nil {
		t.Fatal(err)
	}
	if !set.ProjectionPublished {
		t.Fatalf("takeover replay retry did not publish set: %+v", set)
	}
}

func TestPendingProjectionRecoverySupersedesAndDestroysOutputAfterSourceDrift(t *testing.T) {
	harness := newManifestHarness(t)
	harness.moveJobToUploading(t)
	payload := []byte("pending-projection-source-drift")
	declaration := artifactDeclaration(0, ArtifactRoleContent, "text/plain", payload)
	uploaded, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, Artifact: declaration,
	}, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	harness.projection.onPublish = func(DerivedProjectionPublish) error {
		return errors.New("projection temporarily unavailable")
	}
	result, err := harness.sink.CommitManifest(context.Background(), CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	})
	if err == nil {
		t.Fatal("first projection publication unexpectedly succeeded")
	}
	harness.source.err = errors.New("source fingerprint changed")
	harness.projection.onPublish = nil
	if recovered, err := harness.sink.ReconcilePendingProjections(context.Background(), 32); err != nil || recovered != 0 {
		t.Fatalf("source-drift reconciliation recovered=%d err=%v", recovered, err)
	}
	var job model.BackupAssetProcessingJob
	var attempt model.BackupAssetProcessingAttempt
	var set model.BackupAssetDerivedArtifactSet
	var artifact model.BackupAssetDerivedArtifact
	var reference model.BackupAssetDerivedBlobReference
	var blob model.BackupAssetDerivedBlob
	var lease model.RecoveryPointLease
	if err := harness.db.First(&job, "id = ?", harness.lease.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&attempt, "id = ?", harness.lease.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&set, "id = ?", result.ArtifactSetID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&artifact, "artifact_set_id = ?", set.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&reference, "artifact_id = ?", artifact.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&blob, "id = ?", uploaded.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&lease, "id = ?", harness.lease.RecoveryPointFence.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(ProcessingSuperseded) || job.SupersedeReason != string(SupersedeReasonSourceChanged) || job.IsCurrent ||
		attempt.State != "superseded" || attempt.IsCurrent || set.State != "superseded" ||
		set.RevocationReason != string(DerivedRevokeSourceChanged) || set.ProjectionPublished ||
		reference.State != "revoked" || blob.State != "unavailable" || len(blob.WrappedDEK) != 0 ||
		lease.Status != string(backupasset.LeaseReleased) || harness.projection.revocations != 0 {
		t.Fatalf("source-drift product invalid: job=%+v attempt=%+v set=%+v reference=%+v blob=%+v lease=%+v revokes=%d",
			job, attempt, set, reference, blob, lease, harness.projection.revocations)
	}
	if _, err := os.Stat(filepath.Join(harness.root, blob.OpaqueLocator)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source-drift ciphertext remains: %v", err)
	}
}

func TestPendingProjectionRecoverySupersedesAndDestroysOutputAfterPolicyDrift(t *testing.T) {
	harness := newManifestHarness(t)
	harness.moveJobToUploading(t)
	payload := []byte("pending-projection-policy-drift")
	declaration := artifactDeclaration(0, ArtifactRoleContent, "text/plain", payload)
	uploaded, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, Artifact: declaration,
	}, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	harness.projection.onPublish = func(DerivedProjectionPublish) error {
		return errors.New("projection temporarily unavailable")
	}
	result, err := harness.sink.CommitManifest(context.Background(), CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	})
	if err == nil {
		t.Fatal("first projection publication unexpectedly succeeded")
	}
	harness.policy.revision = "security-policy-v2"
	harness.projection.onPublish = nil
	if recovered, err := harness.sink.ReconcilePendingProjections(context.Background(), 32); err != nil || recovered != 0 {
		t.Fatalf("policy-drift reconciliation recovered=%d err=%v", recovered, err)
	}
	var job model.BackupAssetProcessingJob
	var attempt model.BackupAssetProcessingAttempt
	var set model.BackupAssetDerivedArtifactSet
	var blob model.BackupAssetDerivedBlob
	var lease model.RecoveryPointLease
	if err := harness.db.First(&job, "id = ?", harness.lease.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&attempt, "id = ?", harness.lease.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&set, "id = ?", result.ArtifactSetID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&blob, "id = ?", uploaded.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&lease, "id = ?", harness.lease.RecoveryPointFence.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(ProcessingSuperseded) || job.SupersedeReason != string(SupersedeReasonPolicyChanged) || job.IsCurrent ||
		attempt.State != "superseded" || attempt.IsCurrent || set.State != "superseded" ||
		set.RevocationReason != string(DerivedRevokePolicyChanged) || set.ProjectionPublished ||
		blob.State != "unavailable" || len(blob.WrappedDEK) != 0 || lease.Status != string(backupasset.LeaseReleased) || harness.projection.revocations != 0 {
		t.Fatalf("policy-drift product invalid: job=%+v attempt=%+v set=%+v blob=%+v lease=%+v revokes=%d",
			job, attempt, set, blob, lease, harness.projection.revocations)
	}
}

func TestPendingProjectionRecoveryPreservesPolicyReasonAcrossCleanupCrash(t *testing.T) {
	harness := newManifestHarness(t)
	harness.moveJobToUploading(t)
	payload := []byte("pending-projection-policy-crash")
	declaration := artifactDeclaration(0, ArtifactRoleContent, "text/plain", payload)
	if _, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, Artifact: declaration,
	}, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	harness.projection.onPublish = func(DerivedProjectionPublish) error {
		return errors.New("projection temporarily unavailable")
	}
	result, err := harness.sink.CommitManifest(context.Background(), CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	})
	if err == nil {
		t.Fatal("first projection publication unexpectedly succeeded")
	}
	if err := harness.lifecycle.RevokeSet(context.Background(), result.ArtifactSetID, DerivedRevokePolicyChanged); err != nil {
		t.Fatal(err)
	}
	harness.policy.revision = validWorkDescriptor().SecurityPolicyRevision
	harness.projection.onPublish = nil
	if recovered, err := harness.sink.ReconcilePendingProjections(context.Background(), 32); err != nil || recovered != 0 {
		t.Fatalf("post-crash reconciliation recovered=%d err=%v", recovered, err)
	}
	var job model.BackupAssetProcessingJob
	if err := harness.db.First(&job, "id = ?", harness.lease.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(ProcessingSuperseded) || job.SupersedeReason != string(SupersedeReasonPolicyChanged) {
		t.Fatalf("post-crash supersede product=%+v, want policy_changed", job)
	}
}

func TestProjectionPublicationPersistsPortReceiptRevision(t *testing.T) {
	harness := newManifestHarness(t)
	harness.moveJobToUploading(t)
	payload := []byte("projection-receipt")
	declaration := artifactDeclaration(0, ArtifactRoleOCR, "text/plain", payload)
	if _, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, Artifact: declaration,
	}, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	harness.projection.publishRevision = 41
	result, err := harness.sink.CommitManifest(context.Background(), CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	})
	if err != nil {
		t.Fatal(err)
	}
	var set model.BackupAssetDerivedArtifactSet
	if err := harness.db.First(&set, "id = ?", result.ArtifactSetID).Error; err != nil {
		t.Fatal(err)
	}
	if !set.ProjectionPublished || set.ProjectionRevision != harness.projection.publishRevision {
		t.Fatalf("projection receipt was not persisted exactly: set=%+v receipt=%d", set, harness.projection.publishRevision)
	}
}

func TestConcurrentPendingProjectionRecoveryCompletesExactlyOnce(t *testing.T) {
	harness := newManifestHarness(t)
	harness.moveJobToUploading(t)
	payload := []byte("concurrent-projection-recovery")
	declaration := artifactDeclaration(0, ArtifactRoleContent, "text/plain", payload)
	if _, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, Artifact: declaration,
	}, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	var publishMu sync.Mutex
	publishCall := 0
	replaysReady := make(chan struct{})
	harness.projection.publishRevision = 23
	harness.projection.onPublish = func(DerivedProjectionPublish) error {
		publishMu.Lock()
		publishCall++
		call := publishCall
		if call == 3 {
			close(replaysReady)
		}
		publishMu.Unlock()
		if call == 1 {
			return errors.New("projection acknowledgement lost")
		}
		<-replaysReady
		return nil
	}
	if _, err := harness.sink.CommitManifest(context.Background(), CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	}); err == nil {
		t.Fatal("first projection publication unexpectedly acknowledged")
	}

	type reconcileOutcome struct {
		recovered int
		err       error
	}
	start := make(chan struct{})
	outcomes := make(chan reconcileOutcome, 2)
	for index := 0; index < 2; index++ {
		go func() {
			<-start
			recovered, err := harness.sink.ReconcilePendingProjections(context.Background(), 32)
			outcomes <- reconcileOutcome{recovered: recovered, err: err}
		}()
	}
	close(start)
	totalRecovered := 0
	for index := 0; index < 2; index++ {
		outcome := <-outcomes
		if outcome.err != nil {
			t.Fatalf("concurrent projection recovery error: %v", outcome.err)
		}
		totalRecovered += outcome.recovered
	}
	if totalRecovered != 1 {
		t.Fatalf("concurrent projection recovery count=%d, want one DB winner", totalRecovered)
	}
	var job model.BackupAssetProcessingJob
	var set model.BackupAssetDerivedArtifactSet
	if err := harness.db.First(&job, "id = ?", harness.lease.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&set, "id = ?", job.CurrentArtifactSetID).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(ProcessingSucceeded) || !set.ProjectionPublished || set.ProjectionRevision != 23 {
		t.Fatalf("concurrent projection recovery terminal state invalid: job=%+v set=%+v", job, set)
	}
}

type manifestHarness struct {
	*coordinatorHarness
	store       *DerivedStore
	lifecycle   *DerivedLifecycle
	sink        *ArtifactSink
	projection  *manifestProjectionFake
	source      *manifestSourceRevalidator
	policy      *manifestPolicyFake
	lease       AttemptLease
	sinkGrantID string
	root        string
}

func newManifestHarness(t *testing.T) *manifestHarness {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_MANIFEST_DERIVED_KEK_FOR_TEST_ONLY")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	coordinator := newCoordinatorHarness(t)
	if err := coordinator.db.AutoMigrate(
		&model.WrappedDomainKey{}, &model.BackupAssetProcessingUpload{},
		&model.BackupAssetDerivedBlob{}, &model.BackupAssetDerivedArtifactSet{},
		&model.BackupAssetDerivedArtifact{}, &model.BackupAssetDerivedBlobReference{},
	); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_manifest_key_version ON wrapped_domain_keys(domain, version)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := coordinator.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_manifest_key_active ON wrapped_domain_keys(domain) WHERE state = 'active'`).Error; err != nil {
		t.Fatal(err)
	}
	keyring := backupasset.NewKeyring(coordinator.db, coordinator.clock.Now)
	if _, err := keyring.Ensure(context.Background(), backupasset.KeyDomainDerivedStore); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "derived")
	store, err := NewDerivedStore(context.Background(), coordinator.db, keyring, DerivedStoreConfig{
		Root: root, ChunkSize: 64 * 1024, BlobMaxBytes: 4 * 1024 * 1024, GlobalMaxBytes: 16 * 1024 * 1024,
		Random: &lockedSequenceReader{}, ValidateRoot: func(context.Context, string) error { return nil },
	}, coordinator.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	projection := &manifestProjectionFake{}
	lifecycle, err := NewDerivedLifecycle(coordinator.db, store, projection, coordinator.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	workerID := coordinator.registerNoopWorker(t, "9")
	work, err := coordinator.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest:   InterestRequest{OwnerKind: InterestSystem, OwnerKey: "manifest", PriorityClass: PriorityInteractive, Priority: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := coordinator.coordinator.Pull(context.Background(), PullRequest{WorkerID: workerID})
	if err != nil || lease.JobID != work.JobID {
		t.Fatalf("Pull: %+v %v", lease, err)
	}
	grants, err := NewGrantService(coordinator.db, coordinator.coordinator.leaseService, coordinator.clock.Now, GrantConfig{
		TTL: 30 * time.Second, MaxRequests: 8, MaxBytesPerRequest: 1 << 20, MaxCumulativeBytes: 4 << 20, MaxInFlight: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	material, err := grants.IssueAttemptGrants(context.Background(), IssueGrantsRequest{
		JobID: lease.JobID, AttemptID: lease.AttemptID, WorkerID: lease.WorkerID, RecoveryPointFence: lease.RecoveryPointFence,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grants.Activate(context.Background(), ActivateGrantRequest{
		GrantID: material.Sink.GrantID, Kind: GrantSink, JobID: lease.JobID,
		AttemptID: lease.AttemptID, WorkerID: lease.WorkerID, Secret: material.Sink.Secret,
	}); err != nil {
		t.Fatal(err)
	}
	revalidator := &manifestSourceRevalidator{}
	policy := &manifestPolicyFake{revision: validWorkDescriptor().SecurityPolicyRevision}
	sink, err := NewArtifactSink(coordinator.db, coordinator.coordinator.leaseService, grants, store, lifecycle, revalidator,
		func(context.Context) (string, error) { return policy.revision, nil }, coordinator.clock.Now,
		ArtifactSinkConfig{MaxArtifacts: 8, MaxArtifactBytes: 1 << 20, MaxTotalBytes: 4 << 20})
	if err != nil {
		t.Fatal(err)
	}
	return &manifestHarness{
		coordinatorHarness: coordinator, store: store, lifecycle: lifecycle, sink: sink,
		projection: projection, source: revalidator, policy: policy, lease: lease, sinkGrantID: material.Sink.GrantID, root: root,
	}
}

func (harness *manifestHarness) moveJobToUploading(t *testing.T) {
	t.Helper()
	if err := harness.db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", harness.lease.JobID).
		Updates(map[string]any{"state": string(ProcessingUploading), "transition_revision": int64(5)}).Error; err != nil {
		t.Fatal(err)
	}
}

func artifactDeclaration(ordinal int, role ArtifactRole, mediaType string, payload []byte) ArtifactDeclaration {
	digest := sha256.Sum256(payload)
	return ArtifactDeclaration{
		Ordinal: ordinal, Role: role, MediaType: mediaType, PlaintextSize: int64(len(payload)),
		PlaintextDigest: hex.EncodeToString(digest[:]), Completeness: ArtifactComplete,
		CoverageCanonical: []byte(`{"schema_version":1,"kind":"all"}`),
	}
}

func withArtifactRole(value ArtifactDeclaration, role ArtifactRole) ArtifactDeclaration {
	value.Role = role
	return value
}

func withArtifactMIME(value ArtifactDeclaration, mediaType string) ArtifactDeclaration {
	value.MediaType = mediaType
	return value
}

func withArtifactSize(value ArtifactDeclaration, size int64) ArtifactDeclaration {
	value.PlaintextSize = size
	return value
}

func withArtifactDigest(value ArtifactDeclaration, digest string) ArtifactDeclaration {
	value.PlaintextDigest = digest
	return value
}

func withArtifactCompleteness(value ArtifactDeclaration, completeness ArtifactCompleteness) ArtifactDeclaration {
	value.Completeness = completeness
	return value
}

type manifestSourceRevalidator struct{ err error }

func (fake *manifestSourceRevalidator) RevalidateProcessingSource(context.Context, WorkDescriptorV1) error {
	return fake.err
}

type manifestPolicyFake struct{ revision string }

type manifestProjectionFake struct {
	mu              sync.Mutex
	publications    int
	revocations     int
	onPublish       func(DerivedProjectionPublish) error
	publishRequests []DerivedProjectionPublish
	publishRevision int64
}

type manifestMetricsFake struct {
	sinkBytes int64
}

func (*manifestMetricsFake) ObserveJob(PriorityClass, ProcessingState, ProcessingErrorCategory) {}
func (*manifestMetricsFake) ObserveLeaseLoss()                                                  {}
func (*manifestMetricsFake) ObserveJobDuration(PriorityClass, ProcessingState, time.Duration)   {}
func (*manifestMetricsFake) SetWorkers(WorkerTrustClass, WorkerHealthClass, int64)              {}
func (*manifestMetricsFake) SetSlots(SlotClass, SlotMetricKind, int64)                          {}
func (*manifestMetricsFake) SetQueue(PriorityClass, ProcessingState, int64, time.Duration)      {}
func (fake *manifestMetricsFake) AddSinkBytes(count int64)                                      { fake.sinkBytes += count }
func (*manifestMetricsFake) SetDerived(DerivedMetricKind, int64)                                {}
func (*manifestMetricsFake) ObserveDerived(DerivedMetricEvent)                                  {}

func (fake *manifestProjectionFake) Publish(_ context.Context, request DerivedProjectionPublish) (DerivedProjectionPublication, error) {
	fake.mu.Lock()
	fake.publications++
	fake.publishRequests = append(fake.publishRequests, request)
	onPublish := fake.onPublish
	revision := fake.publishRevision
	fake.mu.Unlock()
	if onPublish != nil {
		if err := onPublish(request); err != nil {
			return DerivedProjectionPublication{}, err
		}
	}
	if revision == 0 {
		revision = 1
	}
	return DerivedProjectionPublication{ArtifactSetID: request.ArtifactSetID, Revision: revision}, nil
}

func (fake *manifestProjectionFake) Revoke(context.Context, DerivedProjectionRevoke) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.revocations++
	return nil
}

var _ ProcessingSourceRevalidator = (*manifestSourceRevalidator)(nil)
var _ DerivedProjectionPort = (*manifestProjectionFake)(nil)
var _ = gorm.ErrRecordNotFound
