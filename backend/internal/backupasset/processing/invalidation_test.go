package processing

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

func TestInvalidationMarksOnlyAffectedSetsAndSupersedesOldJobs(t *testing.T) {
	harness := newManifestHarness(t)
	harness.moveJobToUploading(t)
	payload := []byte("affected-derived-content")
	declaration := artifactDeclaration(0, ArtifactRoleContent, "text/plain", payload)
	if _, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, Artifact: declaration,
	}, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	published, err := harness.sink.CommitManifest(context.Background(), CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	})
	if err != nil {
		t.Fatal(err)
	}
	var affectedSet model.BackupAssetDerivedArtifactSet
	var affectedJob model.BackupAssetProcessingJob
	if err := harness.db.First(&affectedSet, "id = ?", published.ArtifactSetID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&affectedJob, "id = ?", harness.lease.JobID).Error; err != nil {
		t.Fatal(err)
	}
	unaffectedSet := affectedSet
	unaffectedSet.ID = "11111111111111111111111111111111"
	unaffectedSet.JobID = "22222222222222222222222222222222"
	unaffectedSet.WorkKey = "3333333333333333333333333333333333333333333333333333333333333333"
	unaffectedSet.ManifestDigest = "4444444444444444444444444444444444444444444444444444444444444444"
	unaffectedJob := affectedJob
	unaffectedJob.ID = unaffectedSet.JobID
	unaffectedJob.WorkKey = unaffectedSet.WorkKey
	unaffectedJob.Capability = "image.thumbnail"
	unaffectedJob.CapabilitySchema = "image.thumbnail.v1"
	unaffectedJob.OutputProfile = "raster_thumbnail_v1"
	unaffectedJob.PipelineFingerprint = "pipeline-stable"
	unaffectedJob.CurrentArtifactSetID = &unaffectedSet.ID
	if err := harness.db.Create(&unaffectedJob).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Create(&unaffectedSet).Error; err != nil {
		t.Fatal(err)
	}

	queued, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest:   InterestRequest{OwnerKind: InterestSystem, OwnerKey: "old-pipeline", PriorityClass: PriorityBackground, Priority: 500},
	})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := NewInvalidationController(harness.db, harness.coordinator, harness.lifecycle, harness.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	result, err := controller.Invalidate(context.Background(), InvalidationRequest{
		Targets: []InvalidationTarget{{
			Capability: "noop", OutputProfile: "noop.v1", ActivePipelineFingerprint: "pipeline-fingerprint-v2",
		}},
		BatchSize: 32, RequeuePriority: 950,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.StaleSets != 1 || result.SupersededJobs != 1 || result.RequeuedJobs != 0 || result.NotDeployed != 1 {
		t.Fatalf("invalidation result=%+v", result)
	}
	if err := harness.db.First(&affectedSet, "id = ?", affectedSet.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&unaffectedSet, "id = ?", unaffectedSet.ID).Error; err != nil {
		t.Fatal(err)
	}
	var queuedJob model.BackupAssetProcessingJob
	if err := harness.db.First(&queuedJob, "id = ?", queued.JobID).Error; err != nil {
		t.Fatal(err)
	}
	var failedCurrent int64
	if err := harness.db.Model(&model.BackupAssetProcessingJob{}).
		Where("capability = ? AND pipeline_fingerprint = ? AND state = ? AND is_current = ?", "noop", "pipeline-fingerprint-v2", ProcessingFailed, true).
		Count(&failedCurrent).Error; err != nil {
		t.Fatal(err)
	}
	if affectedSet.State != "stale" || affectedSet.ProjectionPublished || unaffectedSet.State != "active" ||
		queuedJob.State != string(ProcessingSuperseded) || queuedJob.SupersedeReason != string(SupersedeReasonPipelineChanged) ||
		queuedJob.IsCurrent || failedCurrent != 0 || harness.projection.revocations != 1 {
		t.Fatalf("invalidation state invalid: affected=%+v unaffected=%+v queued=%+v failed=%d revokes=%d",
			affectedSet, unaffectedSet, queuedJob, failedCurrent, harness.projection.revocations)
	}
}

func TestInvalidationSupersedesActiveAttemptAndRejectsOldFencePublication(t *testing.T) {
	harness := newManifestHarness(t)
	harness.moveJobToUploading(t)
	payload := []byte("staged-before-pipeline-activation")
	declaration := artifactDeclaration(0, ArtifactRoleContent, "text/plain", payload)
	if _, err := harness.sink.UploadArtifact(context.Background(), UploadArtifactRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, Artifact: declaration,
	}, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}

	controller, err := NewInvalidationController(harness.db, harness.coordinator, harness.lifecycle, harness.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	result, err := controller.Invalidate(context.Background(), InvalidationRequest{
		Targets: []InvalidationTarget{{
			Capability: "noop", OutputProfile: "noop.v1", ActivePipelineFingerprint: "pipeline-fingerprint-v2",
		}},
		BatchSize: 32, RequeuePriority: 950,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.StaleSets != 0 || result.SupersededJobs != 1 || result.RequeuedJobs != 0 || result.NotDeployed != 1 {
		t.Fatalf("active invalidation result=%+v", result)
	}

	var job model.BackupAssetProcessingJob
	var attempt model.BackupAssetProcessingAttempt
	var grants []model.BackupAssetProcessingGrant
	var lease model.RecoveryPointLease
	if err := harness.db.First(&job, "id = ?", harness.lease.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&attempt, "id = ?", harness.lease.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Where("attempt_id = ?", harness.lease.AttemptID).Order("kind ASC").Find(&grants).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&lease, "id = ?", harness.lease.RecoveryPointFence.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(ProcessingSuperseded) || job.SupersedeReason != string(SupersedeReasonPipelineChanged) ||
		job.IsCurrent || job.CurrentAttemptID != nil || attempt.State != "superseded" || attempt.IsCurrent ||
		lease.Status != string(backupasset.LeaseReleased) || len(grants) != 2 {
		t.Fatalf("active invalidation lifecycle invalid: job=%+v attempt=%+v lease=%+v grants=%+v", job, attempt, lease, grants)
	}
	for _, grant := range grants {
		if grant.State != string(GrantRevoked) || grant.RevocationReason != "source_changed" || grant.ActivationSecretHash != "" {
			t.Fatalf("active invalidation grant remained usable: %+v", grant)
		}
	}
	if err := harness.coordinator.leaseService.ValidateFence(context.Background(), harness.lease.RecoveryPointFence); err == nil {
		t.Fatal("pipeline invalidation left old RecoveryPoint fence valid")
	}
	if _, err := harness.sink.CommitManifest(context.Background(), CommitManifestRequest{
		JobID: harness.lease.JobID, AttemptID: harness.lease.AttemptID, WorkerID: harness.lease.WorkerID,
		GrantID: harness.sinkGrantID, RecoveryPointFence: harness.lease.RecoveryPointFence,
		SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
	}); err == nil || errors.Is(err, backupasset.ErrLeaseDeadlineExceeded) {
		t.Fatalf("old fence publication error=%v", err)
	}
	var sets int64
	if err := harness.db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("job_id = ?", harness.lease.JobID).Count(&sets).Error; err != nil {
		t.Fatal(err)
	}
	if sets != 0 {
		t.Fatalf("old fence publication created %d Derived sets", sets)
	}
}
