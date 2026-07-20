package processing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
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

func TestArtifactMediaValidationIsCapabilityProfileAware(t *testing.T) {
	tests := []struct {
		capability string
		profile    string
		role       ArtifactRole
		mediaType  string
		want       bool
	}{
		{capability: "archive.extract_entry", profile: "archive_member_v1", role: ArtifactRoleContent, mediaType: "application/octet-stream", want: true},
		{capability: "document.convert", profile: "static_pages_v1", role: ArtifactRoleContent, mediaType: "application/pdf", want: true},
		{capability: "media.transcode", profile: "browser_preview_v1", role: ArtifactRoleContent, mediaType: "video/mp4", want: true},
		{capability: "text.extract", profile: "bounded_text_v1", role: ArtifactRoleContent, mediaType: "application/octet-stream", want: false},
		{capability: "document.convert", profile: "static_pages_v1", role: ArtifactRoleContent, mediaType: "text/html", want: false},
		{capability: "image.thumbnail", profile: "raster_thumbnail_v1", role: ArtifactRoleThumbnail, mediaType: "image/svg+xml", want: false},
	}
	for _, testCase := range tests {
		descriptor := validWorkDescriptor()
		descriptor.Capability = testCase.capability
		descriptor.CapabilitySchema = testCase.capability + ".v1"
		descriptor.OutputProfile = testCase.profile
		if got := validArtifactMediaForDescriptor(descriptor, testCase.role, testCase.mediaType); got != testCase.want {
			t.Fatalf("%s/%s %s %s=%v, want %v", testCase.capability, testCase.profile, testCase.role, testCase.mediaType, got, testCase.want)
		}
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

func TestProjectionFailureRollsBackDerivedAndJobInSameTransaction(t *testing.T) {
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
	harness.projection.onPublish = func(DerivedProjectionPublish) error {
		return errors.New("search temporarily unavailable")
	}
	result, err := harness.sink.CommitManifest(context.Background(), CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	})
	if err == nil || result != (CommitManifestResult{}) || harness.projection.publications != 1 {
		t.Fatalf("projection failure result=%+v publications=%d err=%v", result, harness.projection.publications, err)
	}
	var job model.BackupAssetProcessingJob
	if err := harness.db.First(&job, "id = ?", harness.lease.JobID).Error; err != nil {
		t.Fatal(err)
	}
	var setCount int64
	if err := harness.db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("job_id = ?", harness.lease.JobID).Count(&setCount).Error; err != nil {
		t.Fatal(err)
	}
	var upload model.BackupAssetProcessingUpload
	if err := harness.db.Where("job_id = ?", harness.lease.JobID).Take(&upload).Error; err != nil {
		t.Fatal(err)
	}
	var grant model.BackupAssetProcessingGrant
	if err := harness.db.First(&grant, "id = ?", harness.sinkGrantID).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(ProcessingUploading) || setCount != 0 || upload.State != "staged" || grant.State != string(GrantActive) {
		t.Fatalf("projection failure escaped outer rollback: job=%+v sets=%d upload=%+v grant=%+v", job, setCount, upload, grant)
	}
}

func TestAtomicProjectionFailureLeavesNoPendingRecoveryAndRetrySucceeds(t *testing.T) {
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
	harness.projection.onPublish = func(DerivedProjectionPublish) error {
		return errors.New("projection transaction failed")
	}
	request := CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	}
	result, err := harness.sink.CommitManifest(context.Background(), request)
	if err == nil || result != (CommitManifestResult{}) {
		t.Fatalf("failed atomic projection result=%+v err=%v", result, err)
	}
	recovered, err := harness.sink.ReconcilePendingProjections(context.Background(), 32)
	if err != nil || recovered != 0 {
		t.Fatalf("ReconcilePendingProjections=%d err=%v", recovered, err)
	}
	harness.projection.onPublish = nil
	result, err = harness.sink.CommitManifest(context.Background(), request)
	if err != nil || result.ArtifactSetID == "" {
		t.Fatalf("retry atomic projection result=%+v err=%v", result, err)
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
	if err := harness.db.First(&lease, "id = ?", harness.lease.RecoveryPointFence.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(ProcessingSucceeded) || job.IsCurrent || attempt.State != "succeeded" || attempt.IsCurrent ||
		set.State != "active" || !set.ProjectionPublished || set.ProjectionRevision <= 0 ||
		lease.Status != string(backupasset.LeaseReleased) {
		t.Fatalf("projection recovery did not close atomically: job=%+v attempt=%+v set=%+v lease=%+v", job, attempt, set, lease)
	}
	if recovered, err = harness.sink.ReconcilePendingProjections(context.Background(), 32); err != nil || recovered != 0 || harness.projection.publications != 2 {
		t.Fatalf("idempotent pending scan=%d publications=%d err=%v", recovered, harness.projection.publications, err)
	}
}

func TestRepeatedProjectionFailuresRemainRetryableWithoutPendingState(t *testing.T) {
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
	harness.projection.onPublish = func(DerivedProjectionPublish) error {
		return errors.New("projection temporarily unavailable")
	}
	request := CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	}
	for attempt := 0; attempt < 2; attempt++ {
		if result, err := harness.sink.CommitManifest(context.Background(), request); err == nil || result != (CommitManifestResult{}) {
			t.Fatalf("failed projection attempt %d result=%+v err=%v", attempt, result, err)
		}
	}
	if len(harness.projection.publishRequests) != 2 {
		t.Fatalf("projection attempts=%d", len(harness.projection.publishRequests))
	}
	if harness.projection.publishRequests[0].RecoveryPointFence != harness.projection.publishRequests[1].RecoveryPointFence ||
		harness.projection.publishRequests[0].ArtifactSetID == harness.projection.publishRequests[1].ArtifactSetID {
		t.Fatalf("atomic retry identity invalid: %+v", harness.projection.publishRequests)
	}
	var sets int64
	if err := harness.db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("job_id = ?", harness.lease.JobID).Count(&sets).Error; err != nil {
		t.Fatal(err)
	}
	var job model.BackupAssetProcessingJob
	var upload model.BackupAssetProcessingUpload
	if err := harness.db.First(&job, "id = ?", harness.lease.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Where("job_id = ?", harness.lease.JobID).Take(&upload).Error; err != nil {
		t.Fatal(err)
	}
	if sets != 0 || job.State != string(ProcessingUploading) || upload.State != "staged" {
		t.Fatalf("failed retries left pending state: sets=%d job=%+v upload=%+v", sets, job, upload)
	}
}

func TestProjectionRetryRejectsSourceDriftWithoutVisibleDerivedState(t *testing.T) {
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
	request := CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	}
	result, err := harness.sink.CommitManifest(context.Background(), request)
	if err == nil {
		t.Fatal("first projection publication unexpectedly succeeded")
	}
	harness.source.err = errors.New("source fingerprint changed")
	harness.projection.onPublish = nil
	if retry, retryErr := harness.sink.CommitManifest(context.Background(), request); !errors.Is(retryErr, ErrManifestSourceChanged) || retry != (CommitManifestResult{}) {
		t.Fatalf("source-drift retry result=%+v err=%v", retry, retryErr)
	}
	var job model.BackupAssetProcessingJob
	var blob model.BackupAssetDerivedBlob
	if err := harness.db.First(&job, "id = ?", harness.lease.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&blob, "id = ?", uploaded.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	var sets, references int64
	if err := harness.db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("job_id = ?", harness.lease.JobID).Count(&sets).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetDerivedBlobReference{}).Where("blob_id = ?", uploaded.BlobID).Count(&references).Error; err != nil {
		t.Fatal(err)
	}
	if result != (CommitManifestResult{}) || job.State != string(ProcessingUploading) || sets != 0 || references != 0 || blob.RefCount != 0 {
		t.Fatalf("source drift exposed Derived state: result=%+v job=%+v sets=%d refs=%d blob=%+v", result, job, sets, references, blob)
	}
}

func TestProjectionRetryRejectsPolicyDriftWithoutVisibleDerivedState(t *testing.T) {
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
	request := CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	}
	result, err := harness.sink.CommitManifest(context.Background(), request)
	if err == nil {
		t.Fatal("first projection publication unexpectedly succeeded")
	}
	harness.policy.revision = "security-policy-v2"
	harness.projection.onPublish = nil
	if retry, retryErr := harness.sink.CommitManifest(context.Background(), request); !errors.Is(retryErr, ErrManifestPolicyChanged) || retry != (CommitManifestResult{}) {
		t.Fatalf("policy-drift retry result=%+v err=%v", retry, retryErr)
	}
	var job model.BackupAssetProcessingJob
	var blob model.BackupAssetDerivedBlob
	if err := harness.db.First(&job, "id = ?", harness.lease.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&blob, "id = ?", uploaded.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	var sets int64
	if err := harness.db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("job_id = ?", harness.lease.JobID).Count(&sets).Error; err != nil {
		t.Fatal(err)
	}
	if result != (CommitManifestResult{}) || job.State != string(ProcessingUploading) || sets != 0 || blob.RefCount != 0 {
		t.Fatalf("policy drift exposed Derived state: result=%+v job=%+v sets=%d blob=%+v", result, job, sets, blob)
	}
}

func TestReconcilePendingProjectionPolicyDriftUsesClosedGrantRevocationReason(t *testing.T) {
	harness := newManifestHarness(t)
	harness.moveJobToUploading(t)
	if err := harness.db.Exec(`
		CREATE TRIGGER validate_manifest_grant_revocation_reason
		BEFORE UPDATE OF revocation_reason ON backup_asset_processing_grants
		WHEN NEW.revocation_reason NOT IN ('', 'cancel', 'lease_lost', 'source_changed', 'expired', 'quarantine', 'shutdown')
		BEGIN
			SELECT RAISE(ABORT, 'invalid grant revocation_reason');
		END
	`).Error; err != nil {
		t.Fatal(err)
	}

	payload := []byte("pending-projection-policy-drift")
	declaration := artifactDeclaration(0, ArtifactRoleContent, "text/plain", payload)
	if _, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, Artifact: declaration,
	}, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	request := CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	}
	artifacts := cloneAndSortArtifacts(request.Artifacts)
	job, _, err := harness.sink.loadManifestJob(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	uploads, err := harness.sink.loadAndValidateUploads(context.Background(), request, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	setID, err := backupasset.NewOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	identities, err := newManifestArtifactIdentities(len(artifacts))
	if err != nil {
		t.Fatal(err)
	}
	manifest := CommitManifestResult{
		ArtifactSetID: setID, ManifestDigest: computeManifestDigest(artifacts), ProjectionRequired: true,
	}
	if err := harness.db.Transaction(func(tx *gorm.DB) error {
		return harness.sink.publishManifestTx(context.Background(), tx, request, job, artifacts, uploads, identities, manifest, setID)
	}); err != nil {
		t.Fatal(err)
	}

	harness.policy.revision = "security-policy-v2"
	if recovered, err := harness.sink.ReconcilePendingProjections(context.Background(), 32); err != nil || recovered != 0 {
		t.Fatalf("ReconcilePendingProjections=%d err=%v", recovered, err)
	}

	var updatedJob model.BackupAssetProcessingJob
	var attempt model.BackupAssetProcessingAttempt
	var inputGrant model.BackupAssetProcessingGrant
	var set model.BackupAssetDerivedArtifactSet
	if err := harness.db.First(&updatedJob, "id = ?", harness.lease.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&attempt, "id = ?", harness.lease.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Where("attempt_id = ? AND kind = ?", harness.lease.AttemptID, GrantInput).Take(&inputGrant).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&set, "id = ?", setID).Error; err != nil {
		t.Fatal(err)
	}
	if updatedJob.State != string(ProcessingSuperseded) || updatedJob.IsCurrent || updatedJob.SupersedeReason != string(SupersedeReasonPolicyChanged) ||
		attempt.State != "superseded" || attempt.IsCurrent || inputGrant.State != string(GrantRevoked) ||
		inputGrant.RevocationReason != "source_changed" || set.State != derivedSetRevokeState(DerivedRevokePolicyChanged) ||
		set.RevocationReason != string(DerivedRevokePolicyChanged) {
		t.Fatalf("policy supersede did not preserve closed products: job=%+v attempt=%+v input_grant=%+v set=%+v", updatedJob, attempt, inputGrant, set)
	}
}

func TestFailedAtomicProjectionHasNoArtifactSetToRevoke(t *testing.T) {
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
	if result != (CommitManifestResult{}) {
		t.Fatalf("failed projection returned visible result: %+v", result)
	}
	if err := harness.lifecycle.RevokeSet(context.Background(), strings.Repeat("f", 32), DerivedRevokePolicyChanged); !errors.Is(err, ErrDerivedUnauthorized) {
		t.Fatalf("missing failed projection revoke error=%v", err)
	}
	var job model.BackupAssetProcessingJob
	if err := harness.db.First(&job, "id = ?", harness.lease.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(ProcessingUploading) || !job.IsCurrent {
		t.Fatalf("failed projection changed job: %+v", job)
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

func TestProjectedManifestPreparesBoundedTermsAndStableExcerptArtifact(t *testing.T) {
	harness := newManifestHarness(t)
	harness.moveJobToUploading(t)
	payload := []byte("alpha alpha beta")
	declaration := artifactDeclaration(0, ArtifactRoleContent, "text/plain", payload)
	if _, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, Artifact: declaration,
	}, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	var preparedField DerivedProjectionField
	harness.projection.onPublish = func(request DerivedProjectionPublish) error {
		if len(request.Fields) != 1 {
			return fmt.Errorf("projection fields=%d", len(request.Fields))
		}
		preparedField = request.Fields[0]
		return nil
	}
	result, err := harness.sink.CommitManifest(context.Background(), CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	})
	if err != nil {
		t.Fatal(err)
	}
	if preparedField.Role != ArtifactRoleContent || preparedField.Completeness != ArtifactComplete ||
		len(preparedField.Terms) != 2 || preparedField.Terms[0] != (DerivedProjectionTerm{Term: "alpha", Frequency: 2}) ||
		preparedField.Terms[1] != (DerivedProjectionTerm{Term: "beta", Frequency: 1}) ||
		backupasset.ValidateOpaqueID(preparedField.ExcerptArtifactID) != nil {
		t.Fatalf("prepared projection field=%+v", preparedField)
	}
	var artifact model.BackupAssetDerivedArtifact
	if err := harness.db.Where("artifact_set_id = ? AND role = ?", result.ArtifactSetID, ArtifactRoleContent).Take(&artifact).Error; err != nil {
		t.Fatal(err)
	}
	if artifact.ID != preparedField.ExcerptArtifactID {
		t.Fatalf("Search excerpt=%q Derived artifact=%q", preparedField.ExcerptArtifactID, artifact.ID)
	}
}

func TestBinaryContentManifestDoesNotRequireSearchProjection(t *testing.T) {
	declaration := artifactDeclaration(0, ArtifactRoleContent, "application/pdf", []byte("pdf"))
	if manifestNeedsProjection(validWorkDescriptor(), []ArtifactDeclaration{declaration}) {
		t.Fatal("binary Derived content unexpectedly required a Search projection")
	}
}

func TestSecretClassificationEvidenceIsCanonicalClosedAndFailClosed(t *testing.T) {
	artifactID := strings.Repeat("c", 32)
	tests := []struct {
		name         string
		payload      string
		completeness ArtifactCompleteness
		want         DerivedClassificationEvidence
		wantErr      bool
	}{
		{
			name: "public complete", payload: `{"schema_version":1,"sensitivity":"public","categories":[]}`,
			completeness: ArtifactComplete,
			want:         DerivedClassificationEvidence{ArtifactID: artifactID, Sensitivity: DerivedSensitivityPublic, Categories: []string{}},
		},
		{
			name: "secret complete", payload: `{"schema_version":1,"sensitivity":"secret","categories":["credential_pattern"]}`,
			completeness: ArtifactComplete,
			want:         DerivedClassificationEvidence{ArtifactID: artifactID, Sensitivity: DerivedSensitivitySecret, Categories: []string{"credential_pattern"}},
		},
		{
			name: "unknown partial", payload: `{"schema_version":1,"sensitivity":"unknown","categories":[]}`,
			completeness: ArtifactPartial,
			want:         DerivedClassificationEvidence{ArtifactID: artifactID, Sensitivity: DerivedSensitivityUnknown, Categories: []string{}},
		},
		{name: "duplicate member", payload: `{"schema_version":1,"sensitivity":"secret","sensitivity":"public","categories":[]}`, completeness: ArtifactComplete, wantErr: true},
		{name: "unknown member", payload: `{"schema_version":1,"sensitivity":"public","categories":[],"marker":"forbidden"}`, completeness: ArtifactComplete, wantErr: true},
		{name: "non canonical", payload: `{ "schema_version": 1, "sensitivity": "public", "categories": [] }`, completeness: ArtifactComplete, wantErr: true},
		{name: "unknown category", payload: `{"schema_version":1,"sensitivity":"secret","categories":["future_category"]}`, completeness: ArtifactComplete, wantErr: true},
		{name: "duplicate category", payload: `{"schema_version":1,"sensitivity":"secret","categories":["credential_pattern","credential_pattern"]}`, completeness: ArtifactComplete, wantErr: true},
		{name: "public category", payload: `{"schema_version":1,"sensitivity":"public","categories":["credential_pattern"]}`, completeness: ArtifactComplete, wantErr: true},
		{name: "secret without category", payload: `{"schema_version":1,"sensitivity":"secret","categories":[]}`, completeness: ArtifactComplete, wantErr: true},
		{name: "public partial", payload: `{"schema_version":1,"sensitivity":"public","categories":[]}`, completeness: ArtifactPartial, wantErr: true},
		{name: "secret partial", payload: `{"schema_version":1,"sensitivity":"secret","categories":["credential_pattern"]}`, completeness: ArtifactPartial, wantErr: true},
		{name: "unknown complete", payload: `{"schema_version":1,"sensitivity":"unknown","categories":[]}`, completeness: ArtifactComplete, wantErr: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := parseSecretClassificationEvidence([]byte(testCase.payload), testCase.completeness, artifactID)
			if testCase.wantErr {
				if !errors.Is(err, ErrInvalidManifest) {
					t.Fatalf("classification evidence error=%v", err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("classification evidence=%+v err=%v want=%+v", got, err, testCase.want)
			}
		})
	}
}

func TestSecretClassificationEvidenceAndSearchPublicationShareCallerTransaction(t *testing.T) {
	harness := newManifestHarness(t)
	harness.moveJobToUploading(t)
	harness.configureSecretJob(t)
	payload := []byte(`{"schema_version":1,"sensitivity":"secret","categories":["credential_pattern"]}`)
	declaration := artifactDeclaration(0, ArtifactRoleMetadata, "application/json", payload)
	if _, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, Artifact: declaration,
	}, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	harness.projection.onPublish = func(request DerivedProjectionPublish) error {
		if len(request.Fields) != 0 {
			return fmt.Errorf("classification projection unexpectedly contains plaintext fields: %+v", request.Fields)
		}
		if request.Classification == nil || request.Classification.Sensitivity != DerivedSensitivitySecret ||
			len(request.Classification.Categories) != 1 || request.Classification.Categories[0] != "credential_pattern" ||
			backupasset.ValidateOpaqueID(request.Classification.ArtifactID) != nil {
			return fmt.Errorf("classification projection evidence=%+v", request.Classification)
		}
		return errors.New("classification Search write failed")
	}
	request := CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	}
	if result, err := harness.sink.CommitManifest(context.Background(), request); err == nil || result != (CommitManifestResult{}) {
		t.Fatalf("classification Search failure result=%+v err=%v", result, err)
	}
	var sets int64
	if err := harness.db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("job_id = ?", harness.lease.JobID).Count(&sets).Error; err != nil {
		t.Fatal(err)
	}
	var job model.BackupAssetProcessingJob
	if err := harness.db.First(&job, "id = ?", harness.lease.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if sets != 0 || job.State != string(ProcessingUploading) || job.CurrentArtifactSetID != nil {
		t.Fatalf("classification transaction leaked Derived state sets=%d job=%+v", sets, job)
	}
	harness.projection.onPublish = nil
	result, err := harness.sink.CommitManifest(context.Background(), request)
	if err != nil || !result.ProjectionRequired {
		t.Fatalf("classification retry result=%+v err=%v", result, err)
	}
	var set model.BackupAssetDerivedArtifactSet
	if err := harness.db.First(&set, "id = ?", result.ArtifactSetID).Error; err != nil {
		t.Fatal(err)
	}
	if !set.ProjectionRequired || !set.ProjectionPublished || set.State != "active" || harness.projection.publications != 2 {
		t.Fatalf("classification publication set=%+v publications=%d", set, harness.projection.publications)
	}
}

func TestConcurrentAtomicProjectionRetriesCommitExactlyOnce(t *testing.T) {
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
	harness.projection.onPublish = func(DerivedProjectionPublish) error {
		return errors.New("initial projection failure")
	}
	request := CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	}
	if _, err := harness.sink.CommitManifest(context.Background(), request); err == nil {
		t.Fatal("first projection publication unexpectedly acknowledged")
	}
	harness.projection.onPublish = nil
	harness.projection.publishRevision = 23
	start := make(chan struct{})
	outcomes := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func() {
			<-start
			_, err := harness.sink.CommitManifest(context.Background(), request)
			outcomes <- err
		}()
	}
	close(start)
	succeeded := 0
	for index := 0; index < 2; index++ {
		if err := <-outcomes; err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent atomic projection successes=%d, want one", succeeded)
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
		t.Fatalf("concurrent atomic projection terminal state invalid: job=%+v set=%+v", job, set)
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
	lifecycle, err := NewDerivedLifecycle(coordinator.db, store, projection, coordinator.clock.Now, coordinator.coordinator.leaseService)
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

func (harness *manifestHarness) configureSecretJob(t *testing.T) {
	t.Helper()
	descriptor := validWorkDescriptor()
	descriptor.Capability = "secret.classify"
	descriptor.CapabilitySchema = "secret.classify.v1"
	descriptor.PipelineFingerprint = "secret-pipeline-v1"
	descriptor.OutputProfile = "bounded_secret_v1"
	descriptor.EntryFingerprint = strings.Repeat("a", 64)
	descriptor.Parameters.Codec = "text"
	descriptor.Parameters.Language = "und"
	descriptor.Parameters.Model = "builtin-v1"
	descriptor.Parameters.MaxOutputBytes = 256 << 10
	descriptor.Parameters.MaxOutputCount = 1
	descriptor.Parameters.MaxExpandedBytes = 16 << 20
	descriptor.Parameters.RequiresMaterialization = false
	canonical, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", harness.lease.JobID).Updates(map[string]any{
		"descriptor_canonical": canonical, "entry_fingerprint": descriptor.EntryFingerprint,
		"capability": descriptor.Capability, "capability_schema": descriptor.CapabilitySchema,
		"pipeline_fingerprint": descriptor.PipelineFingerprint, "output_profile": descriptor.OutputProfile,
	}).Error; err != nil {
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
func (*manifestMetricsFake) SetCoverage(string, string, CoverageMetricState, int64)             {}
func (*manifestMetricsFake) ObserveUpdaterActivation(UpdaterActivationOutcome)                  {}

type manifestPreparedProjection struct {
	fake    *manifestProjectionFake
	request DerivedProjectionPublish
}

type manifestPreparedRevocation struct {
	fake *manifestProjectionFake
}

func (fake *manifestProjectionFake) PreparePublish(_ context.Context, request DerivedProjectionPublish) (PreparedDerivedProjection, error) {
	return &manifestPreparedProjection{fake: fake, request: request}, nil
}

func (prepared *manifestPreparedProjection) PublishTx(_ context.Context, _ *gorm.DB) (DerivedProjectionPublication, error) {
	fake := prepared.fake
	request := prepared.request
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

func (fake *manifestProjectionFake) PrepareRevoke(context.Context, DerivedProjectionRevoke) (PreparedDerivedRevocation, error) {
	return &manifestPreparedRevocation{fake: fake}, nil
}

func (prepared *manifestPreparedRevocation) RevokeTx(context.Context, *gorm.DB) error {
	fake := prepared.fake
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.revocations++
	return nil
}

var _ ProcessingSourceRevalidator = (*manifestSourceRevalidator)(nil)
var _ DerivedProjectionPort = (*manifestProjectionFake)(nil)
var _ = gorm.ErrRecordNotFound
