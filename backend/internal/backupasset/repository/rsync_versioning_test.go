package repository

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
)

func TestCreateRsyncVersioningPreflightKeepsLegacyBindingAndRedactsFilesystemFacts(t *testing.T) {
	db := newRepositoryTestDB(t)
	root := t.TempDir()
	legacyTarget := filepath.Join(root, "legacy")
	source := filepath.Join(root, "source")
	if err := os.Mkdir(legacyTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	taskEntity := seedTask(t, db, "rsync", legacyTarget, "")
	if err := db.Model(&model.Node{}).Where("id = ?", taskEntity.NodeID).Update("host", "").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("rsync_source", source).Error; err != nil {
		t.Fatal(err)
	}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, scopedObservationProber(backupasset.ProviderRsync))
	service.keyring = backupasset.NewKeyring(db, nil)
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	audit := &auditSpy{}
	service.audit = audit
	var current model.Task
	if err := db.First(&current, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, err := managedRsyncTaskRevision(current)
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.CreateRsyncVersioningPreflightForRequest(context.Background(), backupasset.RsyncVersioningPreflightRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationVersionedFullCopy,
	}, RequestContext{Actor: backupasset.AuditActor{UserID: 7, Username: "operator", Role: "operator"}, CorrelationID: "rsync-preflight-42"})
	if err != nil {
		t.Fatal(err)
	}
	if backupasset.ValidateOpaqueID(result.PreflightID) != nil || result.Mode != backupasset.PublicationVersionedFullCopy ||
		result.State != backupasset.RsyncVersioningReady || result.ReasonCode != backupasset.RsyncVersioningReasonReady ||
		result.CapabilityRevision == 0 || result.ExpiresAt.IsZero() {
		t.Fatalf("preflight result=%+v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{root, legacyTarget, source, "free_bytes", "free_inodes", "repository.json"} {
		if strings.Contains(string(encoded), unsafe) {
			t.Fatalf("safe preflight result leaked %q: %s", unsafe, encoded)
		}
	}
	if len(audit.inputs) != 1 {
		t.Fatalf("preflight audit inputs=%+v", audit.inputs)
	}
	auditPayload, err := json.Marshal(audit.inputs[0])
	if err != nil {
		t.Fatal(err)
	}
	if audit.inputs[0].Action != backupasset.AuditActionRsyncVersioningPreflight || audit.inputs[0].Outcome != backupasset.AuditOutcomeSuccess ||
		audit.inputs[0].TaskID == nil || *audit.inputs[0].TaskID != current.ID ||
		!strings.Contains(string(auditPayload), `"mode":"versioned_full_copy"`) || !strings.Contains(string(auditPayload), `"correlation_id":"rsync-preflight-42"`) {
		t.Fatalf("preflight audit=%+v", audit.inputs[0])
	}
	for _, unsafe := range []string{root, legacyTarget, source, "repository.json", "digest", "locator", "argv"} {
		if strings.Contains(string(auditPayload), unsafe) {
			t.Fatalf("preflight audit leaked %q: %s", unsafe, auditPayload)
		}
	}

	var link model.TaskRepositoryLink
	if err := db.Where("task_id = ? AND unlinked_at IS NULL", current.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	if link.PublicationMode != string(backupasset.PublicationLegacyMutable) || link.EncryptedLegacyLocator != legacyTarget {
		t.Fatalf("preflight mutated legacy link: %+v", link)
	}
	var binding model.RepositoryAccessBinding
	if err := db.Where("repository_id = ? AND status = ?", link.RepositoryID, bindingStatusActive).First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if stored.V1 == nil || stored.ManagedRsyncV2 != nil {
		t.Fatalf("preflight changed active binding: %+v", stored)
	}
}

func TestActivateRsyncVersioningFirstNewPointSwitchesOnlyAfterExactPreflight(t *testing.T) {
	db := newRepositoryTestDB(t)
	root := t.TempDir()
	legacyTarget := filepath.Join(root, "legacy")
	source := filepath.Join(root, "source")
	for _, path := range []string{legacyTarget, source} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	taskEntity := seedTask(t, db, "rsync", legacyTarget, "")
	if err := db.Model(&model.Node{}).Where("id = ?", taskEntity.NodeID).Update("host", "").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("rsync_source", source).Error; err != nil {
		t.Fatal(err)
	}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, scopedObservationProber(backupasset.ProviderRsync))
	service.keyring = backupasset.NewKeyring(db, nil)
	transitioner := &rsyncVersioningTransitioner{beforePersist: func() error {
		var taskBeforePersist model.Task
		if err := db.First(&taskBeforePersist, taskEntity.ID).Error; err != nil {
			return err
		}
		var linkBeforePersist model.TaskRepositoryLink
		if err := db.Where("task_id = ? AND unlinked_at IS NULL", taskEntity.ID).First(&linkBeforePersist).Error; err != nil {
			return err
		}
		if !taskBeforePersist.Enabled || linkBeforePersist.PublicationMode != string(backupasset.PublicationLegacyMutable) {
			return errors.New("Rsync versioning mutated state before admission drain")
		}
		return nil
	}}
	service.admission = transitioner
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var beforeTask model.Task
	if err := db.First(&beforeTask, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	beforeRevision, err := managedRsyncTaskRevision(beforeTask)
	if err != nil {
		t.Fatal(err)
	}
	var beforeRepository model.BackupRepository
	if err := db.Where("provider_kind = ?", backupasset.ProviderRsync).First(&beforeRepository).Error; err != nil {
		t.Fatal(err)
	}
	preflight, err := service.CreateRsyncVersioningPreflight(context.Background(), backupasset.RsyncVersioningPreflightRequest{
		TaskID: beforeTask.ID, ExpectedTaskRevision: beforeRevision, RequestedMode: backupasset.PublicationVersionedFullCopy,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.ActivateRsyncVersioning(context.Background(), backupasset.RsyncVersioningActivationRequest{
		TaskID: beforeTask.ID, ExpectedTaskRevision: beforeRevision, PreflightID: preflight.PreflightID,
		MigrationChoice: backupasset.RsyncVersioningFirstNewPoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.MigrationChoice != backupasset.RsyncVersioningFirstNewPoint || result.Summary.Mode != backupasset.PublicationVersionedFullCopy ||
		result.Summary.State != backupasset.RsyncVersioningReady || result.Summary.ReasonCode != backupasset.RsyncVersioningReasonReady ||
		result.Summary.SeedFullCopyRequired || result.Summary.CapabilityRevision != uint64(beforeRepository.CapabilityRevision+1) {
		t.Fatalf("activation result=%+v", result)
	}
	if transitioner.calls != 1 || !transitioner.enabled {
		t.Fatalf("activation admission transition calls=%d enabled=%t, want one managed transition", transitioner.calls, transitioner.enabled)
	}

	var activatedTask model.Task
	if err := db.First(&activatedTask, beforeTask.ID).Error; err != nil {
		t.Fatal(err)
	}
	activatedRevision, err := managedRsyncTaskRevision(activatedTask)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.TaskRevision != strconv.FormatUint(activatedRevision, 10) {
		t.Fatalf("activation response task revision=%q, want persisted %d", result.Summary.TaskRevision, activatedRevision)
	}
	if activatedTask.Enabled || activatedTask.SkipNext || !strings.Contains(activatedTask.ExecutorConfig, `"version":1`) ||
		!strings.Contains(activatedTask.ExecutorConfig, `"publication_mode":"versioned_full_copy"`) {
		t.Fatalf("activated Task=%+v", activatedTask)
	}
	var link model.TaskRepositoryLink
	if err := db.Where("task_id = ? AND unlinked_at IS NULL", activatedTask.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	if link.PublicationMode != string(backupasset.PublicationVersionedFullCopy) || link.EncryptedLegacyLocator != legacyTarget {
		t.Fatalf("activated link=%+v", link)
	}
	var repository model.BackupRepository
	if err := db.First(&repository, "id = ?", link.RepositoryID).Error; err != nil {
		t.Fatal(err)
	}
	if repository.VersionMode != string(backupasset.VersionFullCopyTree) || repository.ImmutabilityLevel != string(backupasset.ImmutabilityXirangManaged) ||
		repository.CapabilityRevision != beforeRepository.CapabilityRevision+1 {
		t.Fatalf("activated repository=%+v", repository)
	}
	beforeIdentity, afterIdentity := "", ""
	if beforeRepository.RepositoryIdentity != nil {
		beforeIdentity = *beforeRepository.RepositoryIdentity
	}
	if repository.RepositoryIdentity != nil {
		afterIdentity = *repository.RepositoryIdentity
	}
	if beforeIdentity == "" || afterIdentity == "" || afterIdentity == beforeIdentity ||
		strings.Contains(afterIdentity, legacyTarget) || strings.Contains(afterIdentity, root) {
		t.Fatalf("activated repository identity was not rebound safely: before=%q after=%q", beforeIdentity, afterIdentity)
	}
	var binding model.RepositoryAccessBinding
	if err := db.Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if stored.V1 != nil || stored.ManagedRsyncV2 == nil || stored.ManagedRsyncV2.PublicationMode != backupasset.PublicationVersionedFullCopy ||
		stored.ManagedRsyncV2.PreflightID != preflight.PreflightID || stored.ManagedRsyncV2.TaskRepositoryLinkID != link.ID {
		t.Fatalf("activated binding=%+v", stored)
	}
	var mutablePoints int64
	if err := db.Model(&model.RecoveryPoint{}).Where("repository_id = ? AND semantics = ?", repository.ID, backupasset.PointMutableHead).Count(&mutablePoints).Error; err != nil {
		t.Fatal(err)
	}
	if mutablePoints != 0 {
		t.Fatalf("activation retained an incompatible mutable-head point: %d", mutablePoints)
	}

	if _, err := service.ActivateRsyncVersioning(context.Background(), backupasset.RsyncVersioningActivationRequest{
		TaskID: beforeTask.ID, ExpectedTaskRevision: beforeRevision, PreflightID: preflight.PreflightID,
		MigrationChoice: backupasset.RsyncVersioningFirstNewPoint,
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("reused preflight activation error=%v, want conflict", err)
	}
}

func TestActivateRsyncVersioningHardlinkFirstNewPointRequiresFullCopySeed(t *testing.T) {
	db := newRepositoryTestDB(t)
	root := t.TempDir()
	legacyTarget := filepath.Join(root, "legacy")
	source := filepath.Join(root, "source")
	for _, path := range []string{legacyTarget, source} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	taskEntity := seedTask(t, db, "rsync", legacyTarget, "")
	if err := db.Model(&model.Node{}).Where("id = ?", taskEntity.NodeID).Update("host", "").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("rsync_source", source).Error; err != nil {
		t.Fatal(err)
	}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, scopedObservationProber(backupasset.ProviderRsync))
	service.keyring = backupasset.NewKeyring(db, nil)
	service.admission = &rsyncVersioningTransitioner{}
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var legacyTask model.Task
	if err := db.First(&legacyTask, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, err := managedRsyncTaskRevision(legacyTask)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := service.CreateRsyncVersioningPreflight(context.Background(), backupasset.RsyncVersioningPreflightRequest{
		TaskID: legacyTask.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationVersionedHardlink,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.ActivateRsyncVersioning(context.Background(), backupasset.RsyncVersioningActivationRequest{
		TaskID: legacyTask.ID, ExpectedTaskRevision: revision, PreflightID: preflight.PreflightID,
		MigrationChoice: backupasset.RsyncVersioningFirstNewPoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Mode != backupasset.PublicationVersionedHardlink || !result.Summary.SeedFullCopyRequired ||
		result.Summary.State != backupasset.RsyncVersioningReady {
		t.Fatalf("hardlink first-new-point activation=%+v", result)
	}
	var repository model.BackupRepository
	if err := db.Where("provider_kind = ?", backupasset.ProviderRsync).First(&repository).Error; err != nil {
		t.Fatal(err)
	}
	if repository.VersionMode != string(backupasset.VersionHardlinkTree) {
		t.Fatalf("hardlink first-new-point repository=%+v", repository)
	}
	var link model.TaskRepositoryLink
	if err := db.Where("task_id = ? AND unlinked_at IS NULL", legacyTask.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	if link.PublicationMode != string(backupasset.PublicationVersionedHardlink) || link.EncryptedLegacyLocator != legacyTarget {
		t.Fatalf("hardlink first-new-point link=%+v", link)
	}
}

func TestActivateRsyncVersioningImportedBaselinePublishesFullCopyAndCommitsMigrationRun(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	legacyTarget := filepath.Join(root, "legacy")
	source := filepath.Join(root, "source")
	for _, path := range []string{legacyTarget, source} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(legacyTarget, "baseline.txt"), []byte("legacy bytes stay in place"), 0o600); err != nil {
		t.Fatal(err)
	}
	taskEntity := seedTask(t, db, "rsync", legacyTarget, "")
	if err := db.Model(&model.Node{}).Where("id = ?", taskEntity.NodeID).Update("host", "").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("rsync_source", source).Error; err != nil {
		t.Fatal(err)
	}

	foundation := backupasset.NewFoundationService(completeRepositoryFoundationSettings(true))
	keyring := backupasset.NewKeyring(db, func() time.Time { return now })
	registry := provider.NewRegistry()
	strategy := &rsyncPublicationStrategyStub{}
	if err := registry.Register(backupasset.ProviderRsync, provider.Registration{
		Prober: scopedObservationProber(backupasset.ProviderRsync), PublicationStrategy: strategy,
	}); err != nil {
		t.Fatal(err)
	}
	history, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	admission := &rsyncVersioningManagedAdmission{publicationAdmission: &publicationAdmission{
		mode: publication.AdmissionPristineLegacy, generation: 1,
	}}
	lease, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: 168 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := NewPublicationService(PublicationDependencies{
		DB: db, Foundation: foundation, Registry: registry, Keyring: keyring, Lease: lease,
		Admission: admission, Metrics: publication.NoopMetrics{}, History: history, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: db, Foundation: foundation, Registry: registry, Keyring: keyring, Now: func() time.Time { return now },
		Admission: admission, History: history, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	service.publication = publisher
	if _, ok := service.admission.(publication.FeatureTransitioner); !ok {
		t.Fatalf("test admission %T does not implement FeatureTransitioner", service.admission)
	}

	var commit provider.RsyncTreeCommitV1
	strategy.prepare = func(_ context.Context, request provider.PublicationPrepareRequest) (provider.PreparedPublication, error) {
		attempt, err := request.Attempt.RsyncTreeAttempt()
		if err != nil {
			return provider.PreparedPublication{}, err
		}
		if request.RsyncTreeInput == nil || request.RsyncTreeInput.Source.LocalPath != legacyTarget || request.RsyncTreeInput.Source.Remote != nil ||
			attempt.PublicationMode != backupasset.PublicationVersionedFullCopy || !attempt.ImportedBaseline || attempt.SeedFullCopy {
			t.Fatalf("imported baseline strategy input=%+v attempt=%+v", request.RsyncTreeInput, attempt)
		}
		return provider.PreparedPublication{Attempt: request.Attempt, RsyncTreeInput: request.RsyncTreeInput}, nil
	}
	strategy.execute = func(_ context.Context, prepared provider.PreparedPublication, _ provider.PublicationProgress) (provider.ProviderExecutionResult, error) {
		attempt, err := prepared.Attempt.RsyncTreeAttempt()
		if err != nil {
			return provider.ProviderExecutionResult{}, err
		}
		commit = provider.RsyncTreeCommitV1{
			LayoutVersion: 1, RepositoryID: attempt.RepositoryID, TaskRepositoryLinkID: attempt.TaskRepositoryLinkID,
			RecoveryPointID: attempt.RecoveryPointID, AttemptID: attempt.AttemptID, PublicationMode: attempt.PublicationMode,
			ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("1", 64), ManifestEntryCount: 1, LogicalBytes: 42,
			FidelityDigest: strings.Repeat("2", 64), SourceFingerprint: prepared.RsyncTreeInput.SourceFingerprint,
			ProviderCommittedAt: now, CommitMarkerDigest: strings.Repeat("3", 64), ChildFenceDigest: prepared.RsyncTreeInput.ChildFenceDigest,
			PointDeadlineAt: attempt.PointDeadlineAt, RenameVerified: true, DirectoryFsyncVerified: true,
		}
		providerCommit := provider.NewRsyncTreeProviderCommit(commit)
		return provider.ProviderExecutionResult{ExitCode: 0, Completion: backupasset.CompletionKnownExitZero, ProviderCommit: &providerCommit}, nil
	}
	strategy.record = func(_ context.Context, _ provider.PreparedPublication, result provider.ProviderExecutionResult) (provider.ProviderCommit, error) {
		if result.ProviderCommit == nil {
			t.Fatal("imported baseline execute omitted provider commit")
		}
		return *result.ProviderCommit, nil
	}
	strategy.reconcile = func(_ context.Context, _ provider.PublicationReconcileRequest) (provider.PublicationReconcileResult, error) {
		return provider.PublicationReconcileResult{RsyncTree: &provider.RsyncTreeReconcileV1{
			State: provider.RsyncTreeReconcileFinal, Commit: &commit,
			Manifest: &provider.RsyncTreeManifestV1{
				DigestAlgorithm: commit.ManifestDigestAlgorithm, Digest: commit.ManifestDigest, EntryCount: commit.ManifestEntryCount,
				LogicalBytes: commit.LogicalBytes, FidelityDigest: commit.FidelityDigest,
			},
		}}, nil
	}

	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var legacyTask model.Task
	if err := db.First(&legacyTask, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, err := managedRsyncTaskRevision(legacyTask)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := service.CreateRsyncVersioningPreflight(context.Background(), backupasset.RsyncVersioningPreflightRequest{
		TaskID: legacyTask.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationVersionedHardlink,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.ActivateRsyncVersioning(context.Background(), backupasset.RsyncVersioningActivationRequest{
		TaskID: legacyTask.ID, ExpectedTaskRevision: revision, PreflightID: preflight.PreflightID,
		MigrationChoice: backupasset.RsyncVersioningImportedBaseline,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Mode != backupasset.PublicationVersionedHardlink || result.Summary.State != backupasset.RsyncVersioningCommitted ||
		result.Summary.SeedFullCopyRequired || result.MigrationChoice != backupasset.RsyncVersioningImportedBaseline {
		t.Fatalf("imported-baseline activation=%+v", result)
	}
	var taskRun model.TaskRun
	if err := db.Where("task_id = ? AND trigger_type = ?", taskEntity.ID, "migration").First(&taskRun).Error; err != nil {
		t.Fatal(err)
	}
	if taskRun.Status != "success" || taskRun.StartedAt == nil || taskRun.FinishedAt == nil || taskRun.LastError != "" {
		t.Fatalf("imported-baseline TaskRun=%+v", taskRun)
	}
	var point model.RecoveryPoint
	if err := db.Where("producing_task_run_id = ?", taskRun.ID).First(&point).Error; err != nil {
		t.Fatal(err)
	}
	if point.Semantics != string(backupasset.PointImportedBaseline) || point.State != string(backupasset.RecoveryPointCommitted) || point.ManifestDigest != commit.ManifestDigest {
		t.Fatalf("imported-baseline point=%+v", point)
	}
	var binding model.RepositoryAccessBinding
	if err := db.Where("repository_id = ? AND status = ?", point.RepositoryID, bindingStatusActive).First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ManagedRsyncV2 == nil || stored.ManagedRsyncV2.SeedFullCopyRequired {
		t.Fatalf("imported-baseline binding=%+v", stored)
	}
	var activatedTask model.Task
	if err := db.First(&activatedTask, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	if activatedTask.Enabled {
		t.Fatalf("imported-baseline activation enabled Task=%+v", activatedTask)
	}
	contents, err := os.ReadFile(filepath.Join(legacyTarget, "baseline.txt"))
	if err != nil || string(contents) != "legacy bytes stay in place" {
		t.Fatalf("legacy target mutated contents=%q err=%v", contents, err)
	}
}

func TestPrepareRsyncVersioningRollbackPausesSeparatedManagedTask(t *testing.T) {
	db := newRepositoryTestDB(t)
	root := t.TempDir()
	legacyTarget := filepath.Join(root, "legacy")
	source := filepath.Join(root, "source")
	for _, path := range []string{legacyTarget, source} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	taskEntity := seedTask(t, db, "rsync", legacyTarget, "")
	if err := db.Model(&model.Node{}).Where("id = ?", taskEntity.NodeID).Update("host", "").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("rsync_source", source).Error; err != nil {
		t.Fatal(err)
	}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, scopedObservationProber(backupasset.ProviderRsync))
	service.keyring = backupasset.NewKeyring(db, nil)
	transitioner := &rsyncVersioningTransitioner{}
	service.admission = transitioner
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var legacyTask model.Task
	if err := db.First(&legacyTask, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, err := managedRsyncTaskRevision(legacyTask)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := service.CreateRsyncVersioningPreflight(context.Background(), backupasset.RsyncVersioningPreflightRequest{
		TaskID: legacyTask.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationVersionedFullCopy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ActivateRsyncVersioning(context.Background(), backupasset.RsyncVersioningActivationRequest{
		TaskID: legacyTask.ID, ExpectedTaskRevision: revision, PreflightID: preflight.PreflightID,
		MigrationChoice: backupasset.RsyncVersioningFirstNewPoint,
	}); err != nil {
		t.Fatal(err)
	}
	var activated model.Task
	if err := db.First(&activated, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", activated.ID).Updates(map[string]any{
		"enabled": true, "skip_next": true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&activated, activated.ID).Error; err != nil {
		t.Fatal(err)
	}
	rollbackRevision, err := managedRsyncTaskRevision(activated)
	if err != nil {
		t.Fatal(err)
	}

	rollbacker, ok := any(service).(interface {
		PrepareRsyncVersioningRollback(context.Context, backupasset.RsyncVersioningRollbackPreparationRequest) (backupasset.RsyncVersioningRollbackPreparationResult, error)
	})
	if !ok {
		t.Fatal("repository service does not expose Rsync rollback preparation")
	}
	result, err := rollbacker.PrepareRsyncVersioningRollback(context.Background(), backupasset.RsyncVersioningRollbackPreparationRequest{
		TaskID: activated.ID, ExpectedTaskRevision: rollbackRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Mode != backupasset.PublicationVersionedFullCopy || result.Summary.State != backupasset.RsyncVersioningRollbackPrepared ||
		result.Summary.ReasonCode != backupasset.RsyncVersioningReasonRollbackPrepared || transitioner.calls != 2 || transitioner.enabled {
		t.Fatalf("rollback preparation=%+v calls=%d enabled=%t", result, transitioner.calls, transitioner.enabled)
	}
	var prepared model.Task
	if err := db.First(&prepared, activated.ID).Error; err != nil {
		t.Fatal(err)
	}
	preparedRevision, err := managedRsyncTaskRevision(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.TaskRevision != strconv.FormatUint(preparedRevision, 10) {
		t.Fatalf("rollback response task revision=%q, want persisted %d", result.Summary.TaskRevision, preparedRevision)
	}
	if prepared.Enabled || prepared.SkipNext || prepared.NextRunAt != nil || prepared.RsyncTarget != legacyTarget {
		t.Fatalf("rollback-prepared Task=%+v", prepared)
	}
}

func TestRsyncVersioningSummaryRedactsRootsAndFailsClosedForUnknownMode(t *testing.T) {
	db := newRepositoryTestDB(t)
	root := t.TempDir()
	legacyTarget := filepath.Join(root, "legacy")
	source := filepath.Join(root, "source")
	for _, path := range []string{legacyTarget, source} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	taskEntity := seedTask(t, db, "rsync", legacyTarget, "")
	if err := db.Model(&model.Node{}).Where("id = ?", taskEntity.NodeID).Update("host", "").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("rsync_source", source).Error; err != nil {
		t.Fatal(err)
	}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, scopedObservationProber(backupasset.ProviderRsync))
	service.keyring = backupasset.NewKeyring(db, nil)
	service.admission = &rsyncVersioningTransitioner{}
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var legacyTask model.Task
	if err := db.First(&legacyTask, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, err := managedRsyncTaskRevision(legacyTask)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := service.CreateRsyncVersioningPreflight(context.Background(), backupasset.RsyncVersioningPreflightRequest{
		TaskID: legacyTask.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationVersionedHardlink,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ActivateRsyncVersioning(context.Background(), backupasset.RsyncVersioningActivationRequest{
		TaskID: legacyTask.ID, ExpectedTaskRevision: revision, PreflightID: preflight.PreflightID,
		MigrationChoice: backupasset.RsyncVersioningFirstNewPoint,
	}); err != nil {
		t.Fatal(err)
	}

	summarizer, ok := any(service).(interface {
		RsyncVersioningSummary(context.Context, uint) (backupasset.RsyncVersioningSummary, error)
	})
	if !ok {
		t.Fatal("repository service does not expose a safe Rsync versioning summary")
	}
	summary, err := summarizer.RsyncVersioningSummary(context.Background(), taskEntity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Mode != backupasset.PublicationVersionedHardlink || summary.State != backupasset.RsyncVersioningReady ||
		!summary.SeedFullCopyRequired || summary.ReasonCode != backupasset.RsyncVersioningReasonReady {
		t.Fatalf("managed Rsync summary=%+v", summary)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{root, legacyTarget, source, "repository.json", "marker", "locator", "digest"} {
		if strings.Contains(string(encoded), unsafe) {
			t.Fatalf("safe summary leaked %q: %s", unsafe, encoded)
		}
	}
	if err := db.Model(&model.TaskRepositoryLink{}).Where("task_id = ?", taskEntity.ID).Update("publication_mode", "unknown_mode").Error; err != nil {
		t.Fatal(err)
	}
	summary, err = summarizer.RsyncVersioningSummary(context.Background(), taskEntity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.State != backupasset.RsyncVersioningBlocked || summary.ReasonCode != backupasset.RsyncVersioningReasonUnsupported {
		t.Fatalf("unknown persisted mode summary=%+v", summary)
	}
}

type rsyncVersioningTransitioner struct {
	calls         int
	enabled       bool
	beforePersist func() error
}

type rsyncVersioningManagedAdmission struct {
	*publicationAdmission
	calls int
}

func (admission *rsyncVersioningManagedAdmission) TransitionFeature(_ context.Context, enabled bool, persist func() error) error {
	admission.calls++
	if err := persist(); err != nil {
		return err
	}
	admission.mu.Lock()
	if enabled {
		admission.mode = publication.AdmissionManaged
	} else {
		admission.mode = publication.AdmissionRollbackSafe
	}
	admission.generation++
	admission.mu.Unlock()
	return nil
}

func (*rsyncVersioningManagedAdmission) PrepareApplicationDowngrade(context.Context, func() error) error {
	return errors.New("test admission does not prepare application downgrade")
}

func (*rsyncVersioningManagedAdmission) PrepareSchemaDown(context.Context, func() error) error {
	return errors.New("test admission does not prepare schema down")
}

func (*rsyncVersioningTransitioner) Acquire(context.Context, publication.ResticOperation) (publication.AdmissionToken, error) {
	return nil, errors.New("test transitioner does not issue operation tokens")
}

func (transitioner *rsyncVersioningTransitioner) TransitionFeature(_ context.Context, enabled bool, persist func() error) error {
	transitioner.calls++
	transitioner.enabled = enabled
	if transitioner.beforePersist != nil {
		if err := transitioner.beforePersist(); err != nil {
			return err
		}
	}
	return persist()
}

func (*rsyncVersioningTransitioner) PrepareApplicationDowngrade(context.Context, func() error) error {
	return errors.New("test transitioner does not prepare application downgrade")
}

func (*rsyncVersioningTransitioner) PrepareSchemaDown(context.Context, func() error) error {
	return errors.New("test transitioner does not prepare schema down")
}
