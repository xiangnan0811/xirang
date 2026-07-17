package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
)

func TestPublicationReconciliationFactsClassifiesBothManagedRcloneModes(t *testing.T) {
	for _, mode := range []backupasset.TaskPublicationMode{
		backupasset.PublicationVersionedPrefix,
		backupasset.PublicationNativeObjectVersions,
	} {
		t.Run(string(mode), func(t *testing.T) {
			fixture := newRclonePublicationFixture(t, mode)
			execution, err := fixture.service.Prepare(context.Background(), fixture.run())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
			attempt, err := execution.Attempt().RcloneAttempt()
			if err != nil {
				t.Fatal(err)
			}
			var point model.RecoveryPoint
			if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
				t.Fatal(err)
			}
			kind, lineage, _, err := publicationReconciliationFacts(point)
			if err != nil || kind != backupasset.ProviderRclone || lineage.PublicationMode != string(mode) {
				t.Fatalf("Rclone reconciliation kind=%q lineage=%+v err=%v", kind, lineage, err)
			}
		})
	}
}

func TestRcloneRestartReconciliationMovesPreparingToVerifyingThenCommitted(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationVersionedPrefix)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
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
	fixture.strategy.reconcile = provider.RcloneReconcileV1{
		State:  provider.RcloneReconcileProviderCommitted,
		Commit: &commit,
		Manifest: &provider.RcloneManifestV1{
			ManifestIndexDigest: commit.ManifestIndexDigest, ManifestChunkDigests: append([]string(nil), commit.ManifestChunkDigests...),
			EntryCount: commit.ManifestEntryCount, LogicalBytes: commit.LogicalBytes,
			FidelityEvidenceDigest: commit.FidelityEvidenceDigest,
		},
	}
	if err := execution.Abandon(backupasset.ErrPublicationSessionAbandoned); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPointLease{}).
		Where("recovery_point_id = ? AND status = ?", attempt.RecoveryPointID, backupasset.LeaseActive).
		Updates(map[string]any{"status": backupasset.LeaseExpired, "lease_expires_at": fixture.now.Add(-time.Second)}).Error; err != nil {
		t.Fatal(err)
	}

	verifying, err := fixture.service.ProcessPoint(context.Background(), attempt.RecoveryPointID)
	if err != nil || verifying.State != backupasset.RecoveryPointVerifying || !verifying.ProviderCommitRecorded {
		t.Fatalf("preparing reconciliation outcome=%+v err=%v", verifying, err)
	}
	committed, err := fixture.service.ProcessPoint(context.Background(), attempt.RecoveryPointID)
	if err != nil || committed.State != backupasset.RecoveryPointCommitted || !committed.ProviderCommitRecorded {
		t.Fatalf("verifying reconciliation outcome=%+v err=%v", committed, err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointCommitted) || point.ManifestDigest != commit.ManifestIndexDigest ||
		point.EntryCount != int64(commit.ManifestEntryCount) || point.LogicalBytes != int64(commit.LogicalBytes) || point.CommittedAt == nil {
		t.Fatalf("committed Rclone point=%+v", point)
	}
	var manifests []model.RecoveryPointManifest
	if err := fixture.db.Where("recovery_point_id = ?", point.ID).Find(&manifests).Error; err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || !manifests[0].IsActive || manifests[0].Generator != "xirang-rclone" ||
		!strings.Contains(manifests[0].EncryptedCommitEvidence, `"provider":"rclone"`) {
		t.Fatalf("Rclone manifests=%+v", manifests)
	}
	var taskRun model.TaskRun
	if err := fixture.db.First(&taskRun, fixture.taskRun.ID).Error; err != nil {
		t.Fatal(err)
	}
	if taskRun.Status != "running" || taskRun.FinishedAt != nil || taskRun.LastError != "" {
		t.Fatalf("Rclone reconciliation rewrote TaskRun truth: %+v", taskRun)
	}
}

func TestRcloneNativeRestartReconciliationPersistsExactCommitVersion(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
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
	commit := validRcloneNativeRepositoryCommit(attempt, input.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
	fixture.strategy.reconcile = provider.RcloneReconcileV1{
		State: provider.RcloneReconcileProviderCommitted, Commit: &commit,
		Manifest: &provider.RcloneManifestV1{
			ManifestIndexDigest: commit.ManifestIndexDigest, ManifestChunkDigests: append([]string(nil), commit.ManifestChunkDigests...),
			EntryCount: commit.ManifestEntryCount, LogicalBytes: commit.LogicalBytes,
			FidelityEvidenceDigest: commit.FidelityEvidenceDigest,
		},
	}
	if err := execution.Abandon(backupasset.ErrPublicationSessionAbandoned); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPointLease{}).
		Where("recovery_point_id = ? AND status = ?", attempt.RecoveryPointID, backupasset.LeaseActive).
		Updates(map[string]any{"status": backupasset.LeaseExpired, "lease_expires_at": fixture.now.Add(-time.Second)}).Error; err != nil {
		t.Fatal(err)
	}
	if outcome, err := fixture.service.ProcessPoint(context.Background(), attempt.RecoveryPointID); err != nil || outcome.State != backupasset.RecoveryPointVerifying {
		t.Fatalf("native preparing reconciliation outcome=%+v err=%v", outcome, err)
	}
	var verifying model.RecoveryPoint
	if err := fixture.db.First(&verifying, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	locator, err := decodeManagedRclonePointLocator(verifying.EncryptedProviderLocator)
	if err != nil || locator.NativeCommitKey != commit.Native.CommitKey || locator.NativeCommitVersionID != commit.Native.CommitVersionID ||
		locator.PortableAttemptRoot != "" {
		t.Fatalf("native exact locator=%+v err=%v", locator, err)
	}
	if outcome, err := fixture.service.ProcessPoint(context.Background(), attempt.RecoveryPointID); err != nil || outcome.State != backupasset.RecoveryPointCommitted {
		t.Fatalf("native verifying reconciliation outcome=%+v err=%v", outcome, err)
	}
	if fixture.strategy.reconcileInput == nil || fixture.strategy.reconcileInput.NativeRequest == nil ||
		fixture.strategy.reconcileInput.NativeRequest.ExactCommitKey != commit.Native.CommitKey ||
		fixture.strategy.reconcileInput.NativeRequest.ExactCommitVersionID != commit.Native.CommitVersionID {
		t.Fatalf("native verifying reconciliation did not reopen exact commit: %+v", fixture.strategy.reconcileInput)
	}
	if fixture.nativeFactory.listCalls != 2 {
		t.Fatalf("native reconciliation recaptured B0: list calls=%d want=2", fixture.nativeFactory.listCalls)
	}
	if fixture.nativeFactory.assumeCalls != 9 {
		t.Fatalf("native sessions assume calls=%d want=9 (prepare + two restart sessions)", fixture.nativeFactory.assumeCalls)
	}
}

func validRcloneNativeRepositoryCommit(
	attempt provider.RcloneAttemptV1,
	costDigest string,
	committedAt time.Time,
) provider.RcloneCommitV1 {
	return provider.RcloneCommitV1{
		SchemaVersion: 1, LayoutVersion: attempt.LayoutVersion, MinimumRuntimeRevision: attempt.MinimumRuntimeRevision,
		RepositoryID: attempt.RepositoryID, TaskRepositoryLinkID: attempt.TaskRepositoryLinkID,
		RecoveryPointID: attempt.RecoveryPointID, AttemptID: attempt.AttemptID,
		PublicationMode: attempt.PublicationMode, PointDeadlineAt: attempt.PointDeadlineAt,
		ProviderCommittedAt: committedAt.UTC(), ManifestIndexDigest: strings.Repeat("1", 64),
		ManifestChunkDigests: []string{strings.Repeat("2", 64)}, ManifestEntryCount: 1, LogicalBytes: 5,
		SourceObservationDigest: strings.Repeat("3", 64), DestinationObservationDigest: strings.Repeat("4", 64),
		ContentProofDigest: strings.Repeat("5", 64), FidelityEvidenceDigest: strings.Repeat("6", 64),
		CostEvidenceDigest: costDigest, CapabilityEvidenceDigest: attempt.PreflightDigest, ChildFenceDigest: attempt.ChildFenceDigest,
		Native: &provider.RcloneNativeCommitV1{
			CommitKey:           "managed/v1/control/points/point/attempts/attempt/commit.json",
			CommitVersionID:     "FAKE_EXACT_COMMIT_VERSION_FOR_TEST_ONLY",
			CommitContentDigest: strings.Repeat("7", 64), ManifestControlGraphDigest: strings.Repeat("8", 64),
			PointViewDigest: strings.Repeat("9", 64), MutationLedgerDigest: strings.Repeat("a", 64),
			B0VersionGraphDigest: attempt.Native.B0VersionGraphDigest, B1VersionGraphDigest: strings.Repeat("b", 64),
			ExactReadProofDigest: strings.Repeat("c", 64), VersioningDigest: attempt.Native.VersioningDigest,
			LifecycleDigest: attempt.Native.LifecycleDigest, BucketEncryptionDigest: attempt.Native.BucketEncryptionDigest,
			EncryptionEvidenceDigest: strings.Repeat("d", 64), RoleSessionIdentityDigest: attempt.Native.RoleSessionIdentityDigest,
			CapabilityRevision: attempt.CapabilityRevision, CredentialRevision: attempt.CredentialRevision,
			SessionExpiresAt: attempt.Native.SessionExpiresAt,
		},
	}
}
