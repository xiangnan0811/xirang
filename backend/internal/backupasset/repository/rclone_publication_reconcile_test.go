package repository

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
)

func TestValidateRcloneReconcileRuntimeRejectsCapabilityRevisionMismatches(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationVersionedPrefix)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	attempt, err := execution.Attempt().RcloneAttempt()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := fixture.service.loadExactManagedRclonePublicationRuntime(context.Background(), attempt.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name          string
		mutatePoint   func(*model.RecoveryPoint)
		mutateRuntime func(*managedRclonePublicationRuntime)
		mutateAttempt func(*provider.RcloneAttemptV1)
	}{
		{
			name:        "point",
			mutatePoint: func(value *model.RecoveryPoint) { value.CapabilityRevision++ },
		},
		{
			name:          "runtime",
			mutateRuntime: func(value *managedRclonePublicationRuntime) { value.repository.CapabilityRevision++ },
		},
		{
			name:          "attempt",
			mutateAttempt: func(value *provider.RcloneAttemptV1) { value.CapabilityRevision++ },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutatedPoint, mutatedRuntime, mutatedAttempt := point, runtime, attempt
			if test.mutatePoint != nil {
				test.mutatePoint(&mutatedPoint)
			}
			if test.mutateRuntime != nil {
				test.mutateRuntime(&mutatedRuntime)
			}
			if test.mutateAttempt != nil {
				test.mutateAttempt(&mutatedAttempt)
			}
			if err := validateRcloneReconcileRuntime(mutatedRuntime, mutatedPoint, mutatedAttempt); !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("capability revision mismatch error=%v, want ErrConflict", err)
			}
		})
	}
}

func TestValidateRcloneReconcileRuntimeRejectsIdentityAndLineageMismatches(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationVersionedPrefix)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	attempt, err := execution.Attempt().RcloneAttempt()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := fixture.service.loadExactManagedRclonePublicationRuntime(context.Background(), attempt.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name          string
		mutatePoint   func(*model.RecoveryPoint)
		mutateRuntime func(*managedRclonePublicationRuntime)
		mutateAttempt func(*provider.RcloneAttemptV1)
	}{
		{name: "point ID", mutatePoint: func(value *model.RecoveryPoint) { value.ID = strings.Repeat("8", 32) }},
		{name: "attempt ID", mutateAttempt: func(value *provider.RcloneAttemptV1) { value.AttemptID = strings.Repeat("9", 32) }},
		{name: "runtime repository ID", mutateRuntime: func(value *managedRclonePublicationRuntime) { value.repository.ID = strings.Repeat("0", 32) }},
		{name: "runtime task ID", mutateRuntime: func(value *managedRclonePublicationRuntime) { value.task.ID++ }},
		{name: "runtime link ID", mutateRuntime: func(value *managedRclonePublicationRuntime) { value.link.ID = strings.Repeat("0", 32) }},
		{
			name: "point lineage",
			mutatePoint: func(value *model.RecoveryPoint) {
				lineage, decodeErr := backupasset.DecodePublicationLineage(value.LineageJSON)
				if decodeErr != nil {
					t.Fatalf("decode point lineage: %v", decodeErr)
				}
				lineage.TaskID++
				value.LineageJSON, decodeErr = backupasset.EncodePublicationLineage(lineage)
				if decodeErr != nil {
					t.Fatalf("encode point lineage: %v", decodeErr)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutatedPoint, mutatedRuntime, mutatedAttempt := point, runtime, attempt
			if test.mutatePoint != nil {
				test.mutatePoint(&mutatedPoint)
			}
			if test.mutateRuntime != nil {
				test.mutateRuntime(&mutatedRuntime)
			}
			if test.mutateAttempt != nil {
				test.mutateAttempt(&mutatedAttempt)
			}
			if err := validateRcloneReconcileRuntime(mutatedRuntime, mutatedPoint, mutatedAttempt); !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("identity mismatch error=%v, want ErrConflict", err)
			}
		})
	}
}

func TestRcloneReconcileRejectsRepositoryCapabilityRevisionDriftBeforeMutation(t *testing.T) {
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
			var commit provider.RcloneCommitV1
			switch mode {
			case backupasset.PublicationVersionedPrefix:
				commit = validRcloneRepositoryCommit(attempt, input.PortableRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
			case backupasset.PublicationNativeObjectVersions:
				commit = validRcloneNativeRepositoryCommit(attempt, input.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
			}
			if err := execution.Abandon(backupasset.ErrPublicationSessionAbandoned); err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Model(&model.RecoveryPointLease{}).
				Where("recovery_point_id = ? AND status = ?", attempt.RecoveryPointID, backupasset.LeaseActive).
				Updates(map[string]any{"status": backupasset.LeaseExpired, "lease_expires_at": fixture.now.Add(-time.Second)}).Error; err != nil {
				t.Fatal(err)
			}

			var callbackPoint model.RecoveryPoint
			var callbackLease model.RecoveryPointLease
			callbackCalled := false
			fixture.strategy.reconcileCall = func(_ context.Context, request provider.PublicationReconcileRequest) (provider.PublicationReconcileResult, error) {
				callbackCalled = true
				if request.RcloneInput == nil {
					t.Fatal("Rclone reconciliation omitted typed input")
				}
				if err := fixture.db.First(&callbackPoint, "id = ?", attempt.RecoveryPointID).Error; err != nil {
					t.Fatal(err)
				}
				if callbackPoint.State != string(backupasset.RecoveryPointPreparing) {
					t.Fatalf("claimed Rclone point state=%q, want preparing", callbackPoint.State)
				}
				if err := fixture.db.Where(
					"recovery_point_id = ? AND holder_type = ? AND owner_id = ? AND status = ?",
					attempt.RecoveryPointID, backupasset.LeaseHolderPointPublication, publicationLeaseOwner, backupasset.LeaseActive,
				).First(&callbackLease).Error; err != nil {
					t.Fatal(err)
				}
				if callbackLease.Status != string(backupasset.LeaseActive) {
					t.Fatalf("claimed Rclone lease status=%q, want active", callbackLease.Status)
				}
				if err := fixture.db.Model(&model.BackupRepository{}).Where("id = ?", fixture.repository.ID).
					UpdateColumn("capability_revision", int(attempt.CapabilityRevision)+1).Error; err != nil {
					t.Fatal(err)
				}
				fact := provider.RcloneReconcileV1{
					State: provider.RcloneReconcileProviderCommitted, Commit: &commit,
					Manifest: &provider.RcloneManifestV1{
						ManifestIndexDigest: commit.ManifestIndexDigest, ManifestChunkDigests: append([]string(nil), commit.ManifestChunkDigests...),
						EntryCount: commit.ManifestEntryCount, LogicalBytes: commit.LogicalBytes,
						FidelityEvidenceDigest: commit.FidelityEvidenceDigest,
					},
				}
				return provider.PublicationReconcileResult{Rclone: &fact}, nil
			}
			if _, err := fixture.service.ProcessPoint(context.Background(), attempt.RecoveryPointID); !callbackCalled || !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("Rclone reconciliation callback=%v error=%v, want callback and ErrConflict", callbackCalled, err)
			}
			var afterPoint model.RecoveryPoint
			if err := fixture.db.First(&afterPoint, "id = ?", callbackPoint.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(afterPoint, callbackPoint) || afterPoint.State != string(backupasset.RecoveryPointPreparing) {
				t.Fatalf("point changed after repository capability rejection: before=%+v after=%+v", callbackPoint, afterPoint)
			}
			var afterLease model.RecoveryPointLease
			if err := fixture.db.First(&afterLease, "id = ?", callbackLease.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(afterLease, callbackLease) || afterLease.Status != string(backupasset.LeaseActive) {
				t.Fatalf("claimed lease changed after repository capability rejection: before=%+v after=%+v", callbackLease, afterLease)
			}
			if mode == backupasset.PublicationNativeObjectVersions {
				var evidenceRows int64
				if err := fixture.db.Model(&model.RecoveryPointRcloneNativeVersion{}).
					Where("recovery_point_id = ?", attempt.RecoveryPointID).Count(&evidenceRows).Error; err != nil {
					t.Fatal(err)
				}
				if evidenceRows != 0 {
					t.Fatalf("native evidence rows=%d after repository capability rejection, want 0", evidenceRows)
				}
			}
		})
	}
}

func completeRcloneTestPoint(
	t *testing.T,
	fixture *rclonePublicationFixture,
) (provider.RcloneAttemptV1, model.RecoveryPoint, model.RecoveryPointManifest) {
	t.Helper()
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
	var commit provider.RcloneCommitV1
	switch attempt.PublicationMode {
	case backupasset.PublicationVersionedPrefix:
		commit = validRcloneRepositoryCommit(attempt, input.PortableRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
	case backupasset.PublicationNativeObjectVersions:
		commit = validRcloneNativeRepositoryCommit(attempt, input.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
	default:
		t.Fatalf("unsupported test Rclone mode %q", attempt.PublicationMode)
	}
	fixture.strategy.reconcile = provider.RcloneReconcileV1{
		State:  provider.RcloneReconcileProviderCommitted,
		Commit: &commit,
		Manifest: &provider.RcloneManifestV1{
			ManifestIndexDigest:    commit.ManifestIndexDigest,
			ManifestChunkDigests:   append([]string(nil), commit.ManifestChunkDigests...),
			EntryCount:             commit.ManifestEntryCount,
			LogicalBytes:           commit.LogicalBytes,
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
	if outcome, err := fixture.service.ProcessPoint(context.Background(), attempt.RecoveryPointID); err != nil ||
		outcome.State != backupasset.RecoveryPointVerifying {
		t.Fatalf("Rclone preparing reconciliation outcome=%+v err=%v", outcome, err)
	}
	if outcome, err := fixture.service.ProcessPoint(context.Background(), attempt.RecoveryPointID); err != nil ||
		outcome.State != backupasset.RecoveryPointCommitted {
		t.Fatalf("Rclone verifying reconciliation outcome=%+v err=%v", outcome, err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	var manifest model.RecoveryPointManifest
	if err := fixture.db.Where("recovery_point_id = ? AND is_active = ?", point.ID, true).First(&manifest).Error; err != nil {
		t.Fatal(err)
	}
	return attempt, point, manifest
}

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
	if err != nil || locator.FrozenNativeVersionCount == 0 || !isLowerHex64(locator.FrozenNativeVersionsDigest) ||
		!isLowerHex64(locator.FrozenNativeReferencesDigest) || locator.PortableAttemptRoot != "" {
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
	commitKey := "managed/v1/control/points/" + attempt.RecoveryPointID + "/attempts/" + attempt.AttemptID + "/commit.json"
	commitVersionID := "FAKE_EXACT_COMMIT_VERSION_FOR_TEST_ONLY"
	dataVersion := provider.RcloneNativeExactVersion{PhysicalKey: "managed/v1/data/file.bin", VersionID: "FAKE_EXACT_DATA_VERSION_FOR_TEST_ONLY"}
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
			CommitKey: commitKey, CommitVersionID: commitVersionID,
			FrozenNativeVersions: []provider.RcloneNativeExactVersion{
				dataVersion,
				{PhysicalKey: commitKey, VersionID: commitVersionID},
			},
			FrozenNativeReferences: []provider.RcloneNativeExactVersion{dataVersion},
			CommitContentDigest:    strings.Repeat("7", 64), ManifestControlGraphDigest: strings.Repeat("8", 64),
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

func TestRcloneNativeReconcileRejectsInvalidClaimedControlIdentityBeforeMutation(t *testing.T) {
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
	dataVersion := commit.Native.FrozenNativeVersions[0]
	commit.Native.CommitKey = dataVersion.PhysicalKey
	commit.Native.CommitVersionID = dataVersion.VersionID
	if err := execution.Abandon(backupasset.ErrPublicationSessionAbandoned); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPointLease{}).
		Where("recovery_point_id = ? AND status = ?", attempt.RecoveryPointID, backupasset.LeaseActive).
		Updates(map[string]any{"status": backupasset.LeaseExpired, "lease_expires_at": fixture.now.Add(-time.Second)}).Error; err != nil {
		t.Fatal(err)
	}

	var claimedPoint model.RecoveryPoint
	var claimedLease model.RecoveryPointLease
	callbackCalled := false
	fixture.strategy.reconcileCall = func(_ context.Context, request provider.PublicationReconcileRequest) (provider.PublicationReconcileResult, error) {
		callbackCalled = true
		if request.RcloneInput == nil || request.RcloneInput.NativeRequest == nil {
			t.Fatal("Rclone reconciliation omitted typed native input")
		}
		if err := fixture.db.First(&claimedPoint, "id = ?", attempt.RecoveryPointID).Error; err != nil {
			t.Fatal(err)
		}
		if claimedPoint.State != string(backupasset.RecoveryPointPreparing) {
			t.Fatalf("claimed native Rclone point state=%q, want preparing", claimedPoint.State)
		}
		if err := fixture.db.Where(
			"recovery_point_id = ? AND holder_type = ? AND owner_id = ? AND status = ?",
			attempt.RecoveryPointID, backupasset.LeaseHolderPointPublication, publicationLeaseOwner, backupasset.LeaseActive,
		).First(&claimedLease).Error; err != nil {
			t.Fatal(err)
		}
		if claimedLease.Status != string(backupasset.LeaseActive) {
			t.Fatalf("claimed native Rclone lease status=%q, want active", claimedLease.Status)
		}
		fact := provider.RcloneReconcileV1{
			State: provider.RcloneReconcileProviderCommitted, Commit: &commit,
			Manifest: &provider.RcloneManifestV1{
				ManifestIndexDigest: commit.ManifestIndexDigest, ManifestChunkDigests: append([]string(nil), commit.ManifestChunkDigests...),
				EntryCount: commit.ManifestEntryCount, LogicalBytes: commit.LogicalBytes,
				FidelityEvidenceDigest: commit.FidelityEvidenceDigest,
			},
		}
		return provider.PublicationReconcileResult{Rclone: &fact}, nil
	}
	if _, err := fixture.service.ProcessPoint(context.Background(), attempt.RecoveryPointID); !callbackCalled || !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("native reconciliation callback=%v error=%v, want callback and ErrConflict", callbackCalled, err)
	}
	var afterPoint model.RecoveryPoint
	if err := fixture.db.First(&afterPoint, "id = ?", claimedPoint.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterPoint, claimedPoint) || afterPoint.State != string(backupasset.RecoveryPointPreparing) {
		t.Fatalf("point changed after invalid native control identity: before=%+v after=%+v", claimedPoint, afterPoint)
	}
	var afterLease model.RecoveryPointLease
	if err := fixture.db.First(&afterLease, "id = ?", claimedLease.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterLease, claimedLease) || afterLease.Status != string(backupasset.LeaseActive) {
		t.Fatalf("claimed lease changed after invalid native control identity: before=%+v after=%+v", claimedLease, afterLease)
	}
	var evidenceRows int64
	if err := fixture.db.Model(&model.RecoveryPointRcloneNativeVersion{}).
		Where("recovery_point_id = ?", attempt.RecoveryPointID).Count(&evidenceRows).Error; err != nil {
		t.Fatal(err)
	}
	if evidenceRows != 0 {
		t.Fatalf("native evidence rows=%d after invalid control identity, want 0", evidenceRows)
	}
}
