package repository

import (
	"context"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
)

func TestRcloneReconciledCommitDoesNotProveWriterExit(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { runRcloneReconciledCommitWriterGuard(t, "sqlite") })
	t.Run("postgres", func(t *testing.T) { runRcloneReconciledCommitWriterGuard(t, "postgres") })
}

func runRcloneReconciledCommitWriterGuard(t *testing.T, engine string) {
	t.Helper()
	ctx := context.Background()
	db := openProducerRetentionIntegrationDB(t, engine)
	fixture := newRclonePublicationFixtureWithDB(t, backupasset.PublicationVersionedPrefix, db)
	execution, err := fixture.service.Prepare(ctx, fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := execution.Attempt().RcloneAttempt()
	if err != nil {
		t.Fatal(err)
	}
	input, err := execution.(interface {
		RclonePublicationInput() (provider.RclonePublicationInput, error)
	}).RclonePublicationInput()
	if err != nil {
		t.Fatal(err)
	}
	commit := validRcloneRepositoryCommit(attempt, input.PortableRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
	fixture.strategy.reconcile = provider.RcloneReconcileV1{State: provider.RcloneReconcileProviderCommitted, Commit: &commit,
		Manifest: &provider.RcloneManifestV1{ManifestIndexDigest: commit.ManifestIndexDigest, ManifestChunkDigests: append([]string(nil), commit.ManifestChunkDigests...),
			EntryCount: commit.ManifestEntryCount, LogicalBytes: commit.LogicalBytes, FidelityEvidenceDigest: commit.FidelityEvidenceDigest}}
	if err := execution.Abandon(backupasset.ErrPublicationSessionAbandoned); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ? AND status = ?", attempt.RecoveryPointID, backupasset.LeaseActive).
		Updates(map[string]any{"status": backupasset.LeaseExpired, "lease_expires_at": fixture.now.Add(-time.Second)}).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		outcome, err := fixture.service.ProcessPoint(ctx, attempt.RecoveryPointID)
		if err != nil || outcome.State != backupasset.RecoveryPointVerifying {
			t.Fatalf("reconciled outcome=%+v err=%v", outcome, err)
		}
	}
	var point model.RecoveryPoint
	if err := db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil || consistency.RcloneWriterQuiesced != nil || point.State != string(backupasset.RecoveryPointVerifying) {
		t.Fatal("commit evidence incorrectly proved writer exit")
	}
	var active int64
	if err := db.Model(&model.RecoveryPointManifest{}).Where("recovery_point_id = ? AND is_active = ?", point.ID, true).Count(&active).Error; err != nil || active != 0 {
		t.Fatalf("unproven writer activated a manifest: %d %v", active, err)
	}
	setRcloneReconciliationTime(t, fixture, attempt.PointDeadlineAt)
	outcome, err := fixture.service.ProcessPoint(ctx, point.ID)
	if err != nil || outcome.State != backupasset.RecoveryPointVerifying || outcome.Code != backupasset.FailurePublicationDeadlineExceeded {
		t.Fatalf("deadline after commit=%+v err=%v", outcome, err)
	}
}
