package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestPrepareManagedRsyncCreatesVersionedAttemptAndChildLease(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	if execution.Mode() != publication.ModeEvidence || execution.Attempt() == nil {
		t.Fatalf("managed Rsync execution=%s attempt=%+v", execution.Mode(), execution.Attempt())
	}
	attempt, err := execution.Attempt().RsyncTreeAttempt()
	if err != nil {
		t.Fatal(err)
	}
	if attempt.RepositoryID != fixture.repository.ID || attempt.TaskRepositoryLinkID != fixture.link.ID ||
		attempt.TaskID != fixture.task.ID || attempt.TaskRunID != fixture.taskRun.ID ||
		attempt.PublicationMode != backupasset.PublicationVersionedFullCopy || attempt.StagingComponent != attempt.RecoveryPointID+"."+attempt.AttemptID ||
		attempt.FinalComponent != attempt.RecoveryPointID || attempt.PreflightID != fixture.binding.PreflightID || attempt.PreflightDigest != fixture.binding.PreflightDigest ||
		attempt.ManagedRootIdentityDigest != fixture.binding.ManagedRootIdentityDigest {
		t.Fatalf("managed Rsync attempt=%+v", attempt)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	var node model.Node
	if err := fixture.db.First(&node, fixture.task.NodeID).Error; err != nil {
		t.Fatal(err)
	}
	lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
	if err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointPreparing) || point.Semantics != string(backupasset.PointXirangManifest) ||
		point.ImmutabilityLevel != string(backupasset.ImmutabilityXirangManaged) || lineage.PublicationMode != string(backupasset.PublicationVersionedFullCopy) ||
		point.ProducingTaskNameSnapshot != fixture.task.Name || point.ProducingNodeIDSnapshot != fixture.task.NodeID || point.ProducingNodeNameSnapshot != node.Name {
		t.Fatalf("managed Rsync point=%+v lineage=%+v", point, lineage)
	}
	var leases []model.RecoveryPointLease
	if err := fixture.db.Where("recovery_point_id = ? AND status = ?", attempt.RecoveryPointID, backupasset.LeaseActive).Find(&leases).Error; err != nil {
		t.Fatal(err)
	}
	if len(leases) != 1 || leases[0].HolderType != string(backupasset.LeaseHolderPointPublication) {
		t.Fatalf("managed Rsync child leases=%+v", leases)
	}
}

func TestPrepareManagedRsyncRejectsRepositoryIdentityDrift(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	driftedIdentity := provider.ScopedIdentityPrefix(backupasset.ProviderRsync) + strings.Repeat("a", 64)
	if err := fixture.db.Model(&model.BackupRepository{}).
		Where("id = ?", fixture.repository.ID).
		Update("repository_identity", driftedIdentity).Error; err != nil {
		t.Fatal(err)
	}

	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err == nil {
		_ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned)
		t.Fatal("managed Rsync preparation accepted a drifted repository identity")
	}
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("managed Rsync identity drift error=%v, want conflict", err)
	}
}

func TestManagedRsyncProviderCommitAdvancesOnlyPointPublication(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := execution.Attempt().RsyncTreeAttempt()
	if err != nil {
		t.Fatal(err)
	}
	state, ok := execution.(*rsyncPublicationExecution)
	if !ok {
		t.Fatalf("managed Rsync execution type=%T", execution)
	}
	markerKey, err := fixture.service.rsyncMarkerKey(context.Background(), fixture.repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	commit := provider.RsyncTreeCommitV1{
		LayoutVersion: 1, RepositoryID: attempt.RepositoryID, TaskRepositoryLinkID: attempt.TaskRepositoryLinkID,
		RecoveryPointID: attempt.RecoveryPointID, AttemptID: attempt.AttemptID, PublicationMode: attempt.PublicationMode,
		ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("1", 64), ManifestEntryCount: 1, LogicalBytes: 42,
		FidelityDigest: strings.Repeat("2", 64), SourceFingerprint: managedRsyncSourceFingerprint(markerKey, fixture.binding, attempt.RecoveryPointID),
		ProviderCommittedAt: fixture.now, CommitMarkerDigest: strings.Repeat("3", 64), ChildFenceDigest: rsyncChildFenceDigest(markerKey, state.childFence),
		PointDeadlineAt: attempt.PointDeadlineAt, RenameVerified: true, DirectoryFsyncVerified: true,
	}
	outcome, err := execution.RecordProviderCommit(context.Background(), provider.NewRsyncTreeProviderCommit(commit))
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.ProviderCommitRecorded || outcome.State != backupasset.RecoveryPointVerifying || outcome.RecoveryPointID != attempt.RecoveryPointID {
		t.Fatalf("managed Rsync commit outcome=%+v", outcome)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointVerifying) || point.ManifestDigest != commit.ManifestDigest || point.EntryCount != int64(commit.ManifestEntryCount) || point.LogicalBytes != int64(commit.LogicalBytes) {
		t.Fatalf("managed Rsync committed point=%+v", point)
	}
	locator, err := decodeManagedRsyncPointLocator(point.EncryptedProviderLocator)
	if err != nil {
		t.Fatal(err)
	}
	persistedAttempt, err := provider.DecodeRsyncTreeAttemptV1(locator.TaggedAttempt)
	if err != nil {
		t.Fatal(err)
	}
	if persistedAttempt != attempt || locator.ChildFenceDigest != commit.ChildFenceDigest {
		t.Fatalf("managed Rsync final locator=%+v", locator)
	}
	var taskRun model.TaskRun
	if err := fixture.db.First(&taskRun, fixture.taskRun.ID).Error; err != nil {
		t.Fatal(err)
	}
	if taskRun.Status != "running" {
		t.Fatalf("provider publication rewrote TaskRun transfer state=%q", taskRun.Status)
	}
	var activeLeases int64
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ? AND status = ?", attempt.RecoveryPointID, backupasset.LeaseActive).Count(&activeLeases).Error; err != nil || activeLeases != 0 {
		t.Fatalf("managed Rsync active child leases=%d err=%v", activeLeases, err)
	}
	for _, scope := range []string{managedHistoryLatchScopeInstallation, managedHistoryLatchScopeRepository} {
		var count int64
		if err := fixture.db.Model(&model.BackupAssetManagedHistoryLatch{}).Where("scope = ?", scope).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("managed history latch scope=%q count=%d err=%v", scope, count, err)
		}
	}
}

func TestPrepareManagedHardlinkRsyncLeasesExactCommittedParent(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	if err := fixture.db.Model(&model.BackupRepository{}).Where("id = ?", fixture.repository.ID).Update("version_mode", backupasset.VersionHardlinkTree).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.TaskRepositoryLink{}).Where("id = ?", fixture.link.ID).Update("publication_mode", backupasset.PublicationVersionedHardlink).Error; err != nil {
		t.Fatal(err)
	}
	fixture.repository.VersionMode = string(backupasset.VersionHardlinkTree)
	fixture.link.PublicationMode = string(backupasset.PublicationVersionedHardlink)
	fixture.binding.PublicationMode = backupasset.PublicationVersionedHardlink
	bindingPayload, err := encodeManagedRsyncBindingDocumentV2(fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RepositoryAccessBinding{}).Where("repository_id = ?", fixture.repository.ID).Update("encrypted_config", bindingPayload).Error; err != nil {
		t.Fatal(err)
	}

	parentRun := model.TaskRun{TaskID: fixture.task.ID, TriggerType: "manual", Status: "success", StartedAt: timePointer(fixture.now.Add(-2 * time.Minute)), CreatedAt: fixture.now.Add(-2 * time.Minute), UpdatedAt: fixture.now.Add(-time.Minute)}
	if err := fixture.db.Create(&parentRun).Error; err != nil {
		t.Fatal(err)
	}
	parentLineage, err := managedRsyncPublicationLineageForRun(fixture.link, fixture.task, parentRun, parentRun.TriggerType, "", *parentRun.StartedAt, fixture.now.Add(-time.Minute), fixture.now.Add(time.Hour), backupasset.PublicationVersionedFullCopy)
	if err != nil {
		t.Fatal(err)
	}
	encodedLineage, err := backupasset.EncodePublicationLineage(parentLineage)
	if err != nil {
		t.Fatal(err)
	}
	parentConsistency, err := backupasset.EncodePublicationConsistency(backupasset.PublicationConsistencyV1{Version: 1, Provider: backupasset.ProviderRsync, ProviderCommitDigest: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	parentID := strings.Repeat("4", 32)
	parentAttempt := provider.RsyncTreeAttemptV1{
		RepositoryID: fixture.repository.ID, TaskRepositoryLinkID: fixture.link.ID, RecoveryPointID: parentID, AttemptID: strings.Repeat("5", 32),
		TaskID: fixture.task.ID, TaskRunID: parentRun.ID, PublicationMode: backupasset.PublicationVersionedFullCopy,
		PointDeadlineAt: parentLineage.PointDeadlineAt, ExpectedTaskRevision: 1, RepositoryMarkerDigest: fixture.binding.RootMarkerDigest,
		ManagedRootIdentityDigest: fixture.binding.ManagedRootIdentityDigest, StagingComponent: parentID + "." + strings.Repeat("5", 32), FinalComponent: parentID,
		CommandProfileVersion: 1, PreflightID: fixture.binding.PreflightID, PreflightDigest: fixture.binding.PreflightDigest,
	}
	parentTaggedAttempt, err := provider.EncodePublicationAttempt(provider.NewRsyncTreePublicationAttempt(parentAttempt))
	if err != nil {
		t.Fatal(err)
	}
	parentLocator, err := encodeManagedRsyncPointLocator(managedRsyncPointLocatorV1{
		Version: managedRsyncPointLocatorVersion, Provider: string(backupasset.ProviderRsync), RepositoryID: fixture.repository.ID,
		RecoveryPointID: parentID, FinalComponent: parentID, ManagedRootIdentityDigest: fixture.binding.ManagedRootIdentityDigest, CommitMarkerDigest: strings.Repeat("b", 64),
		TaggedAttempt: parentTaggedAttempt, ChildFenceDigest: strings.Repeat("e", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	committedAt := fixture.now.Add(-30 * time.Second)
	parent := model.RecoveryPoint{
		ID: parentID, RepositoryID: fixture.repository.ID, ProducingTaskID: &fixture.task.ID, ProducingTaskRunID: &parentRun.ID,
		ProducingTaskNameSnapshot: fixture.task.Name, ProducingNodeIDSnapshot: fixture.task.NodeID, LineageJSON: encodedLineage,
		EncryptedProviderLocator: parentLocator, Semantics: string(backupasset.PointXirangManifest), State: string(backupasset.RecoveryPointCommitted),
		CapturedAt: &committedAt, CommittedAt: &committedAt, SourceFingerprint: strings.Repeat("c", 64), ManifestDigestAlgorithm: "sha256",
		ManifestDigest: strings.Repeat("d", 64), EntryCount: 1, LogicalBytes: 42, ConsistencyJSON: parentConsistency, FidelityJSON: "{}",
		CapabilityRevision: fixture.repository.CapabilityRevision, CapabilitiesJSON: fixture.repository.CapabilitiesJSON,
		ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged), PhysicalAvailability: string(backupasset.PhysicalUnknown), HoldState: string(backupasset.HoldNone),
		CreatedAt: committedAt, UpdatedAt: committedAt,
	}
	if err := fixture.db.Create(&parent).Error; err != nil {
		t.Fatal(err)
	}

	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	attempt, err := execution.Attempt().RsyncTreeAttempt()
	if err != nil {
		t.Fatal(err)
	}
	if attempt.PublicationMode != backupasset.PublicationVersionedHardlink || attempt.ParentRecoveryPointID != parentID ||
		attempt.ParentCommitDigest != strings.Repeat("a", 64) || attempt.ParentManifestDigest != strings.Repeat("d", 64) {
		t.Fatalf("managed Rsync hardlink attempt=%+v", attempt)
	}
	state, ok := execution.(*rsyncPublicationExecution)
	if !ok || state.parentFence == nil || state.parentFence.RecoveryPointID != parentID || state.parentFence.HolderType != backupasset.LeaseHolderRsyncParent {
		t.Fatalf("managed Rsync hardlink execution=%T parent fence=%+v", execution, state.parentFence)
	}
	var parentLease model.RecoveryPointLease
	if err := fixture.db.Where("recovery_point_id = ? AND status = ?", parentID, backupasset.LeaseActive).First(&parentLease).Error; err != nil {
		t.Fatal(err)
	}
	var childLease model.RecoveryPointLease
	if err := fixture.db.Where("recovery_point_id = ? AND status = ?", attempt.RecoveryPointID, backupasset.LeaseActive).First(&childLease).Error; err != nil {
		t.Fatal(err)
	}
	if parentLease.HolderType != string(backupasset.LeaseHolderRsyncParent) || childLease.HolderType != string(backupasset.LeaseHolderPointPublication) {
		t.Fatalf("managed Rsync hardlink leases: parent=%+v child=%+v", parentLease, childLease)
	}
}

func TestPrepareManagedHardlinkRsyncUsesFullCopySeedBeforeAnyParent(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	if err := fixture.db.Model(&model.BackupRepository{}).
		Where("id = ?", fixture.repository.ID).
		Update("version_mode", backupasset.VersionHardlinkTree).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.TaskRepositoryLink{}).
		Where("id = ?", fixture.link.ID).
		Update("publication_mode", backupasset.PublicationVersionedHardlink).Error; err != nil {
		t.Fatal(err)
	}
	fixture.repository.VersionMode = string(backupasset.VersionHardlinkTree)
	fixture.link.PublicationMode = string(backupasset.PublicationVersionedHardlink)
	fixture.binding.PublicationMode = backupasset.PublicationVersionedHardlink
	fixture.binding.SeedFullCopyRequired = true
	bindingPayload, err := encodeManagedRsyncBindingDocumentV2(fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RepositoryAccessBinding{}).
		Where("repository_id = ?", fixture.repository.ID).
		Update("encrypted_config", bindingPayload).Error; err != nil {
		t.Fatal(err)
	}

	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	attempt, err := execution.Attempt().RsyncTreeAttempt()
	if err != nil {
		t.Fatal(err)
	}
	if attempt.PublicationMode != backupasset.PublicationVersionedFullCopy || attempt.ParentRecoveryPointID != "" ||
		attempt.ParentCommitDigest != "" || attempt.ParentManifestDigest != "" {
		t.Fatalf("hardlink seed attempt=%+v", attempt)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
	if err != nil {
		t.Fatal(err)
	}
	if lineage.PublicationMode != string(backupasset.PublicationVersionedFullCopy) {
		t.Fatalf("hardlink seed lineage=%+v", lineage)
	}
	var parentLeases int64
	if err := fixture.db.Model(&model.RecoveryPointLease{}).
		Where("holder_type = ? AND status = ?", backupasset.LeaseHolderRsyncParent, backupasset.LeaseActive).
		Count(&parentLeases).Error; err != nil {
		t.Fatal(err)
	}
	if parentLeases != 0 {
		t.Fatalf("hardlink seed unexpectedly acquired %d parent leases", parentLeases)
	}
}

func TestCommittedManagedRsyncFullCopySeedClearsRequirementBeforeNextHardlinkAttempt(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	if err := fixture.db.Model(&model.BackupRepository{}).
		Where("id = ?", fixture.repository.ID).
		Update("version_mode", backupasset.VersionHardlinkTree).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.TaskRepositoryLink{}).
		Where("id = ?", fixture.link.ID).
		Update("publication_mode", backupasset.PublicationVersionedHardlink).Error; err != nil {
		t.Fatal(err)
	}
	fixture.repository.VersionMode = string(backupasset.VersionHardlinkTree)
	fixture.link.PublicationMode = string(backupasset.PublicationVersionedHardlink)
	fixture.binding.PublicationMode = backupasset.PublicationVersionedHardlink
	fixture.binding.SeedFullCopyRequired = true
	bindingPayload, err := encodeManagedRsyncBindingDocumentV2(fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RepositoryAccessBinding{}).
		Where("repository_id = ?", fixture.repository.ID).
		Update("encrypted_config", bindingPayload).Error; err != nil {
		t.Fatal(err)
	}

	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	state := execution.(*rsyncPublicationExecution)
	if !state.attempt.SeedFullCopy || state.attempt.PublicationMode != backupasset.PublicationVersionedFullCopy {
		t.Fatalf("seed execution attempt=%+v", state.attempt)
	}
	markerKey, err := fixture.service.rsyncMarkerKey(context.Background(), fixture.repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	commit := provider.RsyncTreeCommitV1{
		LayoutVersion: 1, RepositoryID: state.attempt.RepositoryID, TaskRepositoryLinkID: state.attempt.TaskRepositoryLinkID,
		RecoveryPointID: state.attempt.RecoveryPointID, AttemptID: state.attempt.AttemptID, PublicationMode: state.attempt.PublicationMode,
		ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("1", 64), ManifestEntryCount: 1, LogicalBytes: 42,
		FidelityDigest: strings.Repeat("2", 64), SourceFingerprint: managedRsyncSourceFingerprint(markerKey, fixture.binding, state.attempt.RecoveryPointID),
		ProviderCommittedAt: fixture.now, CommitMarkerDigest: strings.Repeat("3", 64), ChildFenceDigest: rsyncChildFenceDigest(markerKey, state.childFence),
		PointDeadlineAt: state.attempt.PointDeadlineAt, RenameVerified: true, DirectoryFsyncVerified: true,
	}
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRsyncTreeProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	fixture.strategy.reconcile = func(_ context.Context, request provider.PublicationReconcileRequest) (provider.PublicationReconcileResult, error) {
		if request.RsyncTreeInput == nil {
			t.Fatal("seed verification omitted Rsync input")
		}
		return provider.PublicationReconcileResult{RsyncTree: &provider.RsyncTreeReconcileV1{
			State: provider.RsyncTreeReconcileFinal, Commit: &commit,
			Manifest: &provider.RsyncTreeManifestV1{
				DigestAlgorithm: commit.ManifestDigestAlgorithm, Digest: commit.ManifestDigest, EntryCount: commit.ManifestEntryCount,
				LogicalBytes: commit.LogicalBytes, FidelityDigest: commit.FidelityDigest,
			},
		}}, nil
	}
	if _, err := fixture.service.ProcessPoint(context.Background(), state.attempt.RecoveryPointID); err != nil {
		t.Fatal(err)
	}
	var stored model.RepositoryAccessBinding
	if err := fixture.db.Where("repository_id = ? AND status = ?", fixture.repository.ID, bindingStatusActive).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeStoredBindingDocument(stored.EncryptedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ManagedRsyncV2 == nil || decoded.ManagedRsyncV2.SeedFullCopyRequired {
		t.Fatalf("seed requirement persisted after committed seed: %+v", decoded)
	}

	nextRun := model.TaskRun{TaskID: fixture.task.ID, TriggerType: "manual", Status: "running", StartedAt: timePointer(fixture.now), CreatedAt: fixture.now, UpdatedAt: fixture.now}
	if err := fixture.db.Create(&nextRun).Error; err != nil {
		t.Fatal(err)
	}
	fixture.taskRun = nextRun
	nextExecution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = nextExecution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	nextAttempt, err := nextExecution.Attempt().RsyncTreeAttempt()
	if err != nil {
		t.Fatal(err)
	}
	if nextAttempt.SeedFullCopy || nextAttempt.PublicationMode != backupasset.PublicationVersionedHardlink ||
		nextAttempt.ParentRecoveryPointID != state.attempt.RecoveryPointID {
		t.Fatalf("post-seed hardlink attempt=%+v", nextAttempt)
	}
}

func TestPublishImportedRsyncBaselineUsesFullCopyProviderAndCommitsImportedPoint(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	legacySource := t.TempDir()
	var commit provider.RsyncTreeCommitV1
	fixture.strategy.prepare = func(_ context.Context, request provider.PublicationPrepareRequest) (provider.PreparedPublication, error) {
		attempt, err := request.Attempt.RsyncTreeAttempt()
		if err != nil {
			return provider.PreparedPublication{}, err
		}
		if request.RsyncTreeInput == nil || request.RsyncTreeInput.Source.LocalPath != legacySource || request.RsyncTreeInput.Source.Remote != nil ||
			attempt.PublicationMode != backupasset.PublicationVersionedFullCopy {
			t.Fatalf("imported baseline strategy input=%+v attempt=%+v", request.RsyncTreeInput, attempt)
		}
		return provider.PreparedPublication{Attempt: request.Attempt, RsyncTreeInput: request.RsyncTreeInput}, nil
	}
	fixture.strategy.execute = func(_ context.Context, prepared provider.PreparedPublication, _ provider.PublicationProgress) (provider.ProviderExecutionResult, error) {
		attempt, err := prepared.Attempt.RsyncTreeAttempt()
		if err != nil {
			return provider.ProviderExecutionResult{}, err
		}
		input := prepared.RsyncTreeInput
		commit = provider.RsyncTreeCommitV1{
			LayoutVersion: 1, RepositoryID: attempt.RepositoryID, TaskRepositoryLinkID: attempt.TaskRepositoryLinkID,
			RecoveryPointID: attempt.RecoveryPointID, AttemptID: attempt.AttemptID, PublicationMode: attempt.PublicationMode,
			ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("1", 64), ManifestEntryCount: 1, LogicalBytes: 42,
			FidelityDigest: strings.Repeat("2", 64), SourceFingerprint: input.SourceFingerprint,
			ProviderCommittedAt: fixture.now, CommitMarkerDigest: strings.Repeat("3", 64), ChildFenceDigest: input.ChildFenceDigest,
			PointDeadlineAt: attempt.PointDeadlineAt, RenameVerified: true, DirectoryFsyncVerified: true,
		}
		providerCommit := provider.NewRsyncTreeProviderCommit(commit)
		return provider.ProviderExecutionResult{ExitCode: 0, Completion: backupasset.CompletionKnownExitZero, ProviderCommit: &providerCommit}, nil
	}
	fixture.strategy.record = func(_ context.Context, _ provider.PreparedPublication, result provider.ProviderExecutionResult) (provider.ProviderCommit, error) {
		if result.ProviderCommit == nil {
			t.Fatal("imported baseline execute omitted provider commit")
		}
		return *result.ProviderCommit, nil
	}
	fixture.strategy.reconcile = func(_ context.Context, _ provider.PublicationReconcileRequest) (provider.PublicationReconcileResult, error) {
		return provider.PublicationReconcileResult{RsyncTree: &provider.RsyncTreeReconcileV1{
			State: provider.RsyncTreeReconcileFinal, Commit: &commit,
			Manifest: &provider.RsyncTreeManifestV1{
				DigestAlgorithm: commit.ManifestDigestAlgorithm, Digest: commit.ManifestDigest, EntryCount: commit.ManifestEntryCount,
				LogicalBytes: commit.LogicalBytes, FidelityDigest: commit.FidelityDigest,
			},
		}}, nil
	}

	publisher, ok := any(fixture.service).(interface {
		PublishImportedRsyncBaseline(context.Context, publication.Run, provider.RsyncTreeCommandSource) (publication.Outcome, error)
	})
	if !ok {
		t.Fatal("publication service does not expose imported Rsync baseline publication")
	}
	outcome, err := publisher.PublishImportedRsyncBaseline(context.Background(), fixture.run(), provider.RsyncTreeCommandSource{LocalPath: legacySource})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != backupasset.RecoveryPointCommitted || outcome.TaskRunID != fixture.taskRun.ID || !outcome.ProviderCommitRecorded {
		t.Fatalf("imported baseline outcome=%+v", outcome)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", outcome.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.Semantics != string(backupasset.PointImportedBaseline) || point.State != string(backupasset.RecoveryPointCommitted) || point.ManifestDigest != commit.ManifestDigest {
		t.Fatalf("imported baseline point=%+v", point)
	}
}

func TestPublishImportedRsyncBaselineMarksMigrationRunFailedForNonzeroCopy(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	legacySource := t.TempDir()
	fixture.strategy.execute = func(context.Context, provider.PreparedPublication, provider.PublicationProgress) (provider.ProviderExecutionResult, error) {
		return provider.ProviderExecutionResult{ExitCode: 23, Completion: backupasset.CompletionKnownNonzero}, nil
	}

	_, err := fixture.service.PublishImportedRsyncBaseline(context.Background(), fixture.run(), provider.RsyncTreeCommandSource{LocalPath: legacySource})
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("nonzero imported baseline error=%v, want conflict", err)
	}
	var taskRun model.TaskRun
	if err := fixture.db.First(&taskRun, fixture.taskRun.ID).Error; err != nil {
		t.Fatal(err)
	}
	if taskRun.Status != "failed" || taskRun.LastError != string(backupasset.FailureProviderNonzeroExit) || taskRun.FinishedAt == nil {
		t.Fatalf("nonzero imported-baseline TaskRun=%+v", taskRun)
	}
	var point model.RecoveryPoint
	if err := fixture.db.Where("producing_task_run_id = ?", fixture.taskRun.ID).First(&point).Error; err != nil {
		t.Fatal(err)
	}
	if point.Semantics != string(backupasset.PointImportedBaseline) || point.State != string(backupasset.RecoveryPointFailed) {
		t.Fatalf("nonzero imported-baseline point=%+v", point)
	}
}

func TestPublishImportedRsyncBaselineKeepsExitZeroTaskRunWhenPublicationCannotCommit(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	legacySource := t.TempDir()
	fixture.strategy.execute = func(context.Context, provider.PreparedPublication, provider.PublicationProgress) (provider.ProviderExecutionResult, error) {
		return provider.ProviderExecutionResult{
			ExitCode: 0, Completion: backupasset.CompletionKnownExitZero, EvidenceCode: backupasset.FailurePublicationPreconditionMissing,
		}, nil
	}

	_, err := fixture.service.PublishImportedRsyncBaseline(context.Background(), fixture.run(), provider.RsyncTreeCommandSource{LocalPath: legacySource})
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("missing imported baseline commit error=%v, want conflict", err)
	}
	var taskRun model.TaskRun
	if err := fixture.db.First(&taskRun, fixture.taskRun.ID).Error; err != nil {
		t.Fatal(err)
	}
	if taskRun.Status != "success" || taskRun.LastError != "" || taskRun.FinishedAt == nil {
		t.Fatalf("exit-zero imported-baseline TaskRun=%+v", taskRun)
	}
	var point model.RecoveryPoint
	if err := fixture.db.Where("producing_task_run_id = ?", fixture.taskRun.ID).First(&point).Error; err != nil {
		t.Fatal(err)
	}
	if point.Semantics != string(backupasset.PointImportedBaseline) || point.State != string(backupasset.RecoveryPointFailed) {
		t.Fatalf("missing-commit imported-baseline point=%+v", point)
	}
}

func TestManagedRsyncExecutionBuildsBoundProviderInput(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	inputProvider, ok := execution.(interface {
		RsyncTreePublicationInput() (provider.RsyncTreePublicationInput, error)
	})
	if !ok {
		t.Fatalf("managed Rsync execution %T does not expose typed provider input", execution)
	}
	input, err := inputProvider.RsyncTreePublicationInput()
	if err != nil {
		t.Fatal(err)
	}
	state := execution.(*rsyncPublicationExecution)
	if input.ManagedRoot != fixture.binding.ManagedRootLocator || input.Source.LocalPath != "" || input.Source.Remote != nil ||
		!input.CaptureACLs || !input.CaptureXattrs || len(input.MarkerKey) == 0 ||
		input.SourceFingerprint != managedRsyncSourceFingerprint(input.MarkerKey, fixture.binding, state.attempt.RecoveryPointID) ||
		input.ChildFenceDigest != rsyncChildFenceDigest(input.MarkerKey, state.childFence) || input.ManifestLimits.Timeout <= 0 ||
		input.ManifestLimits.MaxBytes <= 0 || input.ManifestLimits.MaxBytes > provider.MaxRsyncTreeMetadataBytes || input.MaxCommandOutputBytes <= 0 {
		t.Fatalf("managed Rsync provider input=%+v", input)
	}
}

func TestPrepareManagedRsyncPersistsStrictAttemptForCrashRecovery(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	state := execution.(*rsyncPublicationExecution)
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", state.attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	attempt, childFenceDigest, err := decodeManagedRsyncPreparedAttemptRecord(point.EncryptedProviderLocator)
	if err != nil {
		t.Fatal(err)
	}
	if attempt != state.attempt || childFenceDigest != rsyncChildFenceDigest(state.markerKey, state.childFence) {
		t.Fatalf("persisted managed Rsync attempt=%+v child_fence_digest=%q", attempt, childFenceDigest)
	}
}

func TestListCandidatesIncludesExpiredManagedRsyncPreparingPoint(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	attempt, err := execution.Attempt().RsyncTreeAttempt()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPointLease{}).
		Where("recovery_point_id = ? AND holder_type = ?", attempt.RecoveryPointID, backupasset.LeaseHolderPointPublication).
		Update("lease_expires_at", fixture.now.Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	candidates, err := fixture.service.ListCandidates(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0] != attempt.RecoveryPointID {
		t.Fatalf("managed Rsync candidates=%q, want %q", candidates, attempt.RecoveryPointID)
	}
	unresolved, err := fixture.service.HasUnresolvedPublication(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !unresolved {
		t.Fatal("managed Rsync preparing point was omitted from unresolved publication readiness")
	}
}

func TestProcessPointReconcilesExactManagedRsyncFinalCommitIntoVerifying(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	state := execution.(*rsyncPublicationExecution)
	markerKey, err := fixture.service.rsyncMarkerKey(context.Background(), fixture.repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	commit := provider.RsyncTreeCommitV1{
		LayoutVersion: 1, RepositoryID: state.attempt.RepositoryID, TaskRepositoryLinkID: state.attempt.TaskRepositoryLinkID,
		RecoveryPointID: state.attempt.RecoveryPointID, AttemptID: state.attempt.AttemptID, PublicationMode: state.attempt.PublicationMode,
		ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("1", 64), ManifestEntryCount: 1, LogicalBytes: 42,
		FidelityDigest: strings.Repeat("2", 64), SourceFingerprint: managedRsyncSourceFingerprint(markerKey, fixture.binding, state.attempt.RecoveryPointID),
		ProviderCommittedAt: fixture.now, CommitMarkerDigest: strings.Repeat("3", 64), ChildFenceDigest: rsyncChildFenceDigest(markerKey, state.childFence),
		PointDeadlineAt: state.attempt.PointDeadlineAt, RenameVerified: true, DirectoryFsyncVerified: true,
	}
	fixture.strategy.reconcile = func(_ context.Context, request provider.PublicationReconcileRequest) (provider.PublicationReconcileResult, error) {
		attempt, err := request.Attempt.RsyncTreeAttempt()
		if err != nil {
			t.Fatal(err)
		}
		if attempt != state.attempt || request.RsyncTreeInput == nil || request.RsyncTreeInput.ManagedRoot != fixture.binding.ManagedRootLocator ||
			request.RsyncTreeInput.SourceFingerprint != commit.SourceFingerprint || request.RsyncTreeInput.ChildFenceDigest != commit.ChildFenceDigest {
			t.Fatalf("managed Rsync reconcile request=%+v", request)
		}
		return provider.PublicationReconcileResult{RsyncTree: &provider.RsyncTreeReconcileV1{
			State:  provider.RsyncTreeReconcileFinal,
			Commit: &commit,
			Manifest: &provider.RsyncTreeManifestV1{
				DigestAlgorithm: commit.ManifestDigestAlgorithm, Digest: commit.ManifestDigest, EntryCount: commit.ManifestEntryCount,
				LogicalBytes: commit.LogicalBytes, FidelityDigest: commit.FidelityDigest,
			},
		}}, nil
	}
	if err := fixture.db.Model(&model.RecoveryPointLease{}).
		Where("recovery_point_id = ? AND holder_type = ?", state.attempt.RecoveryPointID, backupasset.LeaseHolderPointPublication).
		Update("lease_expires_at", fixture.now.Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}

	outcome, err := fixture.service.ProcessPoint(context.Background(), state.attempt.RecoveryPointID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != backupasset.RecoveryPointVerifying || !outcome.ProviderCommitRecorded || outcome.RecoveryPointID != state.attempt.RecoveryPointID {
		t.Fatalf("managed Rsync reconcile outcome=%+v", outcome)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", state.attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointVerifying) || point.ManifestDigest != commit.ManifestDigest || point.SourceFingerprint != commit.SourceFingerprint {
		t.Fatalf("managed Rsync reconciled point=%+v", point)
	}
	var run model.TaskRun
	if err := fixture.db.First(&run, fixture.taskRun.ID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != "running" {
		t.Fatalf("managed Rsync reconciliation rewrote TaskRun transfer status=%q", run.Status)
	}
}

func TestProcessPointCommitsExactManagedRsyncVerifyingPoint(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	state := execution.(*rsyncPublicationExecution)
	markerKey, err := fixture.service.rsyncMarkerKey(context.Background(), fixture.repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	commit := provider.RsyncTreeCommitV1{
		LayoutVersion: 1, RepositoryID: state.attempt.RepositoryID, TaskRepositoryLinkID: state.attempt.TaskRepositoryLinkID,
		RecoveryPointID: state.attempt.RecoveryPointID, AttemptID: state.attempt.AttemptID, PublicationMode: state.attempt.PublicationMode,
		ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("1", 64), ManifestEntryCount: 1, LogicalBytes: 42,
		FidelityDigest: strings.Repeat("2", 64), SourceFingerprint: managedRsyncSourceFingerprint(markerKey, fixture.binding, state.attempt.RecoveryPointID),
		ProviderCommittedAt: fixture.now, CommitMarkerDigest: strings.Repeat("3", 64), ChildFenceDigest: rsyncChildFenceDigest(markerKey, state.childFence),
		PointDeadlineAt: state.attempt.PointDeadlineAt, RenameVerified: true, DirectoryFsyncVerified: true,
	}
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRsyncTreeProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	fixture.strategy.reconcile = func(_ context.Context, request provider.PublicationReconcileRequest) (provider.PublicationReconcileResult, error) {
		if request.RsyncTreeInput == nil || request.RsyncTreeInput.ChildFenceDigest != commit.ChildFenceDigest ||
			request.RsyncTreeInput.SourceFingerprint != commit.SourceFingerprint {
			t.Fatalf("managed Rsync verifying reconcile request=%+v", request)
		}
		return provider.PublicationReconcileResult{RsyncTree: &provider.RsyncTreeReconcileV1{
			State:  provider.RsyncTreeReconcileFinal,
			Commit: &commit,
			Manifest: &provider.RsyncTreeManifestV1{
				DigestAlgorithm: commit.ManifestDigestAlgorithm, Digest: commit.ManifestDigest, EntryCount: commit.ManifestEntryCount,
				LogicalBytes: commit.LogicalBytes, FidelityDigest: commit.FidelityDigest,
			},
		}}, nil
	}

	outcome, err := fixture.service.ProcessPoint(context.Background(), state.attempt.RecoveryPointID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != backupasset.RecoveryPointCommitted || !outcome.ProviderCommitRecorded || outcome.RecoveryPointID != state.attempt.RecoveryPointID {
		t.Fatalf("managed Rsync verifying outcome=%+v", outcome)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", state.attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointCommitted) || point.CommittedAt == nil || point.ManifestDigest != commit.ManifestDigest {
		t.Fatalf("managed Rsync committed point=%+v", point)
	}
	var manifests []model.RecoveryPointManifest
	if err := fixture.db.Where("recovery_point_id = ?", point.ID).Find(&manifests).Error; err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || !manifests[0].IsActive || manifests[0].Completeness != string(backupasset.ManifestComplete) || manifests[0].Digest != commit.ManifestDigest {
		t.Fatalf("managed Rsync committed manifests=%+v", manifests)
	}
	var run model.TaskRun
	if err := fixture.db.First(&run, fixture.taskRun.ID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != "running" {
		t.Fatalf("managed Rsync verification rewrote TaskRun transfer status=%q", run.Status)
	}
}

func TestProcessPointFailsManagedRsyncOwnedStagingWithoutPublishing(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	state := execution.(*rsyncPublicationExecution)
	fixture.strategy.reconcile = func(_ context.Context, request provider.PublicationReconcileRequest) (provider.PublicationReconcileResult, error) {
		if request.RsyncTreeInput == nil {
			t.Fatal("managed Rsync staging reconciliation omitted typed input")
		}
		return provider.PublicationReconcileResult{RsyncTree: &provider.RsyncTreeReconcileV1{State: provider.RsyncTreeReconcileStaging}}, nil
	}
	if err := fixture.db.Model(&model.RecoveryPointLease{}).
		Where("recovery_point_id = ? AND holder_type = ?", state.attempt.RecoveryPointID, backupasset.LeaseHolderPointPublication).
		Update("lease_expires_at", fixture.now.Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	outcome, err := fixture.service.ProcessPoint(context.Background(), state.attempt.RecoveryPointID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != backupasset.RecoveryPointFailed || outcome.Code != backupasset.FailureProviderCompletionUnproven || outcome.ProviderCommitRecorded {
		t.Fatalf("managed Rsync staging outcome=%+v", outcome)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", state.attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointFailed) || point.SourceFingerprint != "" || consistency.Code != backupasset.FailureProviderCompletionUnproven || consistency.Completion != "" {
		t.Fatalf("managed Rsync staging point=%+v consistency=%+v", point, consistency)
	}
	var active int64
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ? AND status = ?", point.ID, backupasset.LeaseActive).Count(&active).Error; err != nil || active != 0 {
		t.Fatalf("managed Rsync staging active leases=%d err=%v", active, err)
	}
}

func TestProcessPointFailsManagedRsyncVerifyingPointWhenExactFinalMarkerIsAbsent(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	state := execution.(*rsyncPublicationExecution)
	markerKey, err := fixture.service.rsyncMarkerKey(context.Background(), fixture.repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	commit := provider.RsyncTreeCommitV1{
		LayoutVersion: 1, RepositoryID: state.attempt.RepositoryID, TaskRepositoryLinkID: state.attempt.TaskRepositoryLinkID,
		RecoveryPointID: state.attempt.RecoveryPointID, AttemptID: state.attempt.AttemptID, PublicationMode: state.attempt.PublicationMode,
		ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("1", 64), ManifestEntryCount: 1, LogicalBytes: 42,
		FidelityDigest: strings.Repeat("2", 64), SourceFingerprint: managedRsyncSourceFingerprint(markerKey, fixture.binding, state.attempt.RecoveryPointID),
		ProviderCommittedAt: fixture.now, CommitMarkerDigest: strings.Repeat("3", 64), ChildFenceDigest: rsyncChildFenceDigest(markerKey, state.childFence),
		PointDeadlineAt: state.attempt.PointDeadlineAt, RenameVerified: true, DirectoryFsyncVerified: true,
	}
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRsyncTreeProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	fixture.strategy.reconcile = func(context.Context, provider.PublicationReconcileRequest) (provider.PublicationReconcileResult, error) {
		return provider.PublicationReconcileResult{RsyncTree: &provider.RsyncTreeReconcileV1{State: provider.RsyncTreeReconcileAbsent}}, nil
	}
	outcome, err := fixture.service.ProcessPoint(context.Background(), state.attempt.RecoveryPointID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != backupasset.RecoveryPointFailed || outcome.Code != backupasset.FailureManifestUnavailable || !outcome.ProviderCommitRecorded {
		t.Fatalf("missing managed Rsync final outcome=%+v", outcome)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", state.attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointFailed) || point.EncryptedProviderLocator == "" || consistency.Code != backupasset.FailureManifestUnavailable {
		t.Fatalf("missing managed Rsync final point=%+v consistency=%+v", point, consistency)
	}
}

func TestProcessPointExpiresManagedRsyncAtImmutablePointDeadlineBeforeReconcile(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	state := execution.(*rsyncPublicationExecution)
	fixture.service.now = func() time.Time { return state.attempt.PointDeadlineAt.Add(time.Second) }
	fixture.strategy.reconcile = func(context.Context, provider.PublicationReconcileRequest) (provider.PublicationReconcileResult, error) {
		t.Fatal("managed Rsync reconciliation ran after the immutable point deadline")
		return provider.PublicationReconcileResult{}, nil
	}

	outcome, err := fixture.service.ProcessPoint(context.Background(), state.attempt.RecoveryPointID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != backupasset.RecoveryPointFailed || outcome.Code != backupasset.FailurePublicationDeadlineExceeded || outcome.ProviderCommitRecorded {
		t.Fatalf("expired managed Rsync outcome=%+v", outcome)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", state.attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointFailed) {
		t.Fatalf("expired managed Rsync point state=%q, want failed", point.State)
	}
}

func TestProcessPointRejectsManagedRsyncReconcileAfterChildFenceLoss(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	state := execution.(*rsyncPublicationExecution)
	if err := fixture.db.Model(&model.RecoveryPointLease{}).
		Where("recovery_point_id = ? AND holder_type = ?", state.attempt.RecoveryPointID, backupasset.LeaseHolderPointPublication).
		Update("lease_expires_at", fixture.now.Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	markerKey, err := fixture.service.rsyncMarkerKey(context.Background(), fixture.repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	commit := provider.RsyncTreeCommitV1{
		LayoutVersion: 1, RepositoryID: state.attempt.RepositoryID, TaskRepositoryLinkID: state.attempt.TaskRepositoryLinkID,
		RecoveryPointID: state.attempt.RecoveryPointID, AttemptID: state.attempt.AttemptID, PublicationMode: state.attempt.PublicationMode,
		ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("1", 64), ManifestEntryCount: 1, LogicalBytes: 42,
		FidelityDigest: strings.Repeat("2", 64), SourceFingerprint: managedRsyncSourceFingerprint(markerKey, fixture.binding, state.attempt.RecoveryPointID),
		ProviderCommittedAt: fixture.now, CommitMarkerDigest: strings.Repeat("3", 64), ChildFenceDigest: rsyncChildFenceDigest(markerKey, state.childFence),
		PointDeadlineAt: state.attempt.PointDeadlineAt, RenameVerified: true, DirectoryFsyncVerified: true,
	}
	fixture.strategy.reconcile = func(context.Context, provider.PublicationReconcileRequest) (provider.PublicationReconcileResult, error) {
		if err := fixture.db.Model(&model.RecoveryPointLease{}).
			Where("recovery_point_id = ? AND status = ?", state.attempt.RecoveryPointID, backupasset.LeaseActive).
			Updates(map[string]any{"status": backupasset.LeaseReleased, "released_at": fixture.now}).Error; err != nil {
			t.Fatal(err)
		}
		return provider.PublicationReconcileResult{RsyncTree: &provider.RsyncTreeReconcileV1{
			State: provider.RsyncTreeReconcileFinal, Commit: &commit,
			Manifest: &provider.RsyncTreeManifestV1{
				DigestAlgorithm: commit.ManifestDigestAlgorithm, Digest: commit.ManifestDigest, EntryCount: commit.ManifestEntryCount,
				LogicalBytes: commit.LogicalBytes, FidelityDigest: commit.FidelityDigest,
			},
		}}, nil
	}

	_, err = fixture.service.ProcessPoint(context.Background(), state.attempt.RecoveryPointID)
	if !errors.Is(err, backupasset.ErrLeaseFenceLost) {
		t.Fatalf("managed Rsync stale fence reconciliation error=%v, want lease fence lost", err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", state.attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointPreparing) || point.SourceFingerprint != "" {
		t.Fatalf("stale managed Rsync fence changed point=%+v", point)
	}
}

type rsyncPublicationFixture struct {
	t          *testing.T
	db         *gorm.DB
	now        time.Time
	service    *PublicationService
	admission  *publicationAdmission
	task       model.Task
	taskRun    model.TaskRun
	repository model.BackupRepository
	link       model.TaskRepositoryLink
	binding    managedRsyncBindingDocumentV2
	strategy   *rsyncPublicationStrategyStub
}

func newRsyncPublicationFixture(t *testing.T) *rsyncPublicationFixture {
	t.Helper()
	db := newRepositoryTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	legacyTarget := t.TempDir()
	taskEntity := seedTask(t, db, "rsync", legacyTarget, "")
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("updated_at", now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&taskEntity, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	taskRun := model.TaskRun{TaskID: taskEntity.ID, TriggerType: "manual", Status: "running", StartedAt: timePointer(now.Add(-time.Minute)), CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&taskRun).Error; err != nil {
		t.Fatal(err)
	}
	binding := managedRsyncBindingDocumentV2{
		Version: managedRsyncBindingDocumentVersion, Provider: backupasset.ProviderRsync, IdentityClass: provider.IdentityXirangManagedRepository,
		TaskID: taskEntity.ID, NodeID: taskEntity.NodeID, RepositoryID: strings.Repeat("1", 32), TaskRepositoryLinkID: strings.Repeat("2", 32),
		LayoutRevision: managedRsyncLayoutRevisionV1, ManagedRootLocator: t.TempDir(), RootMarkerDigest: strings.Repeat("b", 64),
		ManagedRootIdentityDigest: strings.Repeat("c", 64), PublicationMode: backupasset.PublicationVersionedFullCopy,
		PreflightID: strings.Repeat("d", 32), PreflightDigest: strings.Repeat("e", 64), IdentitySalt: strings.Repeat("42", provider.IdentitySaltBytes),
	}
	identity, err := managedRsyncRepositoryIdentity(binding)
	if err != nil {
		t.Fatal(err)
	}
	repository := model.BackupRepository{
		ID: strings.Repeat("1", 32), ProviderKind: string(backupasset.ProviderRsync), RepositoryIdentity: &identity, DisplayName: "managed-rsync-repository",
		VersionMode: string(backupasset.VersionFullCopyTree), Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1,
		CapabilitiesJSON: `{"list":true,"open_sequential":true}`, ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	link := model.TaskRepositoryLink{
		ID: strings.Repeat("2", 32), TaskID: &taskEntity.ID, RepositoryID: repository.ID, TaskNameSnapshot: taskEntity.Name,
		NodeIDSnapshot: taskEntity.NodeID, PublicationMode: string(backupasset.PublicationVersionedFullCopy), EncryptedLegacyLocator: legacyTarget,
		LinkedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	payload, err := encodeManagedRsyncBindingDocumentV2(binding)
	if err != nil {
		t.Fatal(err)
	}
	access := model.RepositoryAccessBinding{ID: strings.Repeat("3", 32), RepositoryID: repository.ID, BindingKind: "managed_rsync_v2", EncryptedConfig: payload, ConfigFingerprint: strings.Repeat("f", 64), Status: bindingStatusActive, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&access).Error; err != nil {
		t.Fatal(err)
	}
	registry := provider.NewRegistry()
	strategy := &rsyncPublicationStrategyStub{}
	if err := registry.Register(backupasset.ProviderRsync, provider.Registration{Prober: &scriptedProber{}, PublicationStrategy: strategy}); err != nil {
		t.Fatal(err)
	}
	lease, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: 168 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	history, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	admission := &publicationAdmission{mode: publication.AdmissionManaged, generation: 1}
	service, err := NewPublicationService(PublicationDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(completeRepositoryFoundationSettings(true)), Registry: registry,
		Keyring: backupasset.NewKeyring(db, func() time.Time { return now }), Lease: lease,
		Admission: admission, Metrics: publication.NoopMetrics{}, Audit: &auditSpy{}, History: history, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &rsyncPublicationFixture{t: t, db: db, now: now, service: service, admission: admission, task: taskEntity, taskRun: taskRun, repository: repository, link: link, binding: binding, strategy: strategy}
}

func (fixture *rsyncPublicationFixture) run() publication.Run {
	return publication.Run{
		Task: fixture.task, TaskRunID: fixture.taskRun.ID, Trigger: fixture.taskRun.TriggerType, StartedAt: *fixture.taskRun.StartedAt,
		Audit: backupasset.PublicationAuditContext{Actor: backupasset.AuditActor{UserID: 9, Username: "operator", Role: "operator"}, CorrelationID: "managed-rsync-publication-prepare"},
	}
}

type rsyncPublicationStrategyStub struct {
	prepare   func(context.Context, provider.PublicationPrepareRequest) (provider.PreparedPublication, error)
	execute   func(context.Context, provider.PreparedPublication, provider.PublicationProgress) (provider.ProviderExecutionResult, error)
	record    func(context.Context, provider.PreparedPublication, provider.ProviderExecutionResult) (provider.ProviderCommit, error)
	reconcile func(context.Context, provider.PublicationReconcileRequest) (provider.PublicationReconcileResult, error)
}

func (*rsyncPublicationStrategyStub) Kind() backupasset.ProviderKind {
	return backupasset.ProviderRsync
}
func (strategy *rsyncPublicationStrategyStub) Prepare(ctx context.Context, request provider.PublicationPrepareRequest) (provider.PreparedPublication, error) {
	if strategy.prepare != nil {
		return strategy.prepare(ctx, request)
	}
	return provider.PreparedPublication{}, nil
}
func (strategy *rsyncPublicationStrategyStub) Execute(ctx context.Context, prepared provider.PreparedPublication, progress provider.PublicationProgress) (provider.ProviderExecutionResult, error) {
	if strategy.execute != nil {
		return strategy.execute(ctx, prepared, progress)
	}
	return provider.ProviderExecutionResult{}, nil
}
func (strategy *rsyncPublicationStrategyStub) RecordCommit(ctx context.Context, prepared provider.PreparedPublication, result provider.ProviderExecutionResult) (provider.ProviderCommit, error) {
	if strategy.record != nil {
		return strategy.record(ctx, prepared, result)
	}
	return provider.ProviderCommit{}, nil
}
func (*rsyncPublicationStrategyStub) VerifyOrBuildManifest(context.Context, provider.PreparedPublication, provider.ProviderCommit, provider.ManifestLimits) (provider.ManifestResult, error) {
	return provider.ManifestResult{}, nil
}
func (strategy *rsyncPublicationStrategyStub) Reconcile(ctx context.Context, request provider.PublicationReconcileRequest) (provider.PublicationReconcileResult, error) {
	if strategy.reconcile != nil {
		return strategy.reconcile(ctx, request)
	}
	return provider.PublicationReconcileResult{}, nil
}
