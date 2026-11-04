package repository

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	gormrepo "xirang/backend/internal/repository/gorm"
	taskservice "xirang/backend/internal/task"

	"gorm.io/gorm"
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

type rsyncVersioningInvariantSnapshot struct {
	Task          model.Task
	Revision      uint64
	Repository    model.BackupRepository
	Link          model.TaskRepositoryLink
	Binding       model.RepositoryAccessBinding
	StoredBinding storedBindingDocument
	PointCount    int64
	MutableHead   model.RecoveryPoint
	RunCount      int64
}

func captureRsyncVersioningInvariantSnapshot(t *testing.T, db *gorm.DB, taskID uint) rsyncVersioningInvariantSnapshot {
	t.Helper()
	var snap rsyncVersioningInvariantSnapshot
	if err := db.First(&snap.Task, taskID).Error; err != nil {
		t.Fatalf("load task %d: %v", taskID, err)
	}
	rev, err := managedRsyncTaskRevision(snap.Task)
	if err != nil {
		t.Fatalf("compute task revision: %v", err)
	}
	snap.Revision = rev
	if err := db.Where("task_id = ? AND unlinked_at IS NULL", taskID).First(&snap.Link).Error; err != nil {
		t.Fatalf("load active link: %v", err)
	}
	if err := db.First(&snap.Repository, "id = ?", snap.Link.RepositoryID).Error; err != nil {
		t.Fatalf("load repository: %v", err)
	}
	if err := db.Where("repository_id = ? AND status = ?", snap.Repository.ID, bindingStatusActive).First(&snap.Binding).Error; err != nil {
		t.Fatalf("load active binding: %v", err)
	}
	stored, err := decodeStoredBindingDocument(snap.Binding.EncryptedConfig)
	if err != nil {
		t.Fatalf("decode stored binding document: %v", err)
	}
	snap.StoredBinding = stored
	if err := db.Model(&model.RecoveryPoint{}).Where("repository_id = ?", snap.Repository.ID).Count(&snap.PointCount).Error; err != nil {
		t.Fatalf("count recovery points: %v", err)
	}
	if err := db.Where("repository_id = ? AND semantics = ?", snap.Repository.ID, string(backupasset.PointMutableHead)).First(&snap.MutableHead).Error; err != nil {
		t.Fatalf("load mutable_head recovery point: %v", err)
	}
	if err := db.Model(&model.TaskRun{}).Where("task_id = ?", taskID).Count(&snap.RunCount).Error; err != nil {
		t.Fatalf("count task runs: %v", err)
	}
	return snap
}

func assertRsyncVersioningInvariantsUnchanged(t *testing.T, db *gorm.DB, service *Service, before rsyncVersioningInvariantSnapshot, legacyTarget string) {
	t.Helper()
	after := captureRsyncVersioningInvariantSnapshot(t, db, before.Task.ID)

	beforeIdentity, afterIdentity := "", ""
	if before.Repository.RepositoryIdentity != nil {
		beforeIdentity = *before.Repository.RepositoryIdentity
	}
	if after.Repository.RepositoryIdentity != nil {
		afterIdentity = *after.Repository.RepositoryIdentity
	}

	// 1. Persistent bindings (BackupRepository, TaskRepositoryLink, RepositoryAccessBinding)
	if after.Repository.VersionMode != string(backupasset.VersionMutableHead) ||
		after.Repository.ImmutabilityLevel != string(backupasset.ImmutabilityMutable) ||
		afterIdentity != beforeIdentity ||
		after.Repository.CapabilityRevision != before.Repository.CapabilityRevision {
		t.Fatalf("repository mutated: before=%+v after=%+v", before.Repository, after.Repository)
	}
	if after.Link.ID != before.Link.ID ||
		after.Link.PublicationMode != string(backupasset.PublicationLegacyMutable) ||
		after.Link.EncryptedLegacyLocator != legacyTarget ||
		after.Link.UnlinkedAt != nil {
		t.Fatalf("task repository link mutated: before=%+v after=%+v", before.Link, after.Link)
	}
	if after.Binding.ID != before.Binding.ID ||
		after.Binding.BindingKind != "task_derived_v1" ||
		after.Binding.EncryptedConfig != before.Binding.EncryptedConfig ||
		after.StoredBinding.V1 == nil ||
		after.StoredBinding.ManagedRsyncV2 != nil ||
		after.StoredBinding.V1.Version != bindingDocumentVersion ||
		after.StoredBinding.V1.Locator != legacyTarget {
		t.Fatalf("repository access binding mutated: before=%+v after=%+v stored=%+v", before.Binding, after.Binding, after.StoredBinding)
	}

	// 2. Task pause state & revision
	if after.Task.Enabled != before.Task.Enabled || after.Task.SkipNext != before.Task.SkipNext ||
		after.Task.RsyncTarget != legacyTarget ||
		after.Task.ExecutorConfig != "" ||
		after.Revision != before.Revision {
		t.Fatalf("task pause state or revision mutated: before=%+v (rev=%d) after=%+v (rev=%d)", before.Task, before.Revision, after.Task, after.Revision)
	}

	// 3. Recovery points & migration TaskRuns
	if after.PointCount != 1 || after.MutableHead.ID != before.MutableHead.ID ||
		after.MutableHead.State != string(backupasset.RecoveryPointObserved) ||
		after.RunCount != 0 {
		t.Fatalf("recovery points or migration runs mutated: points=%d mutableHead=%+v runs=%d", after.PointCount, after.MutableHead, after.RunCount)
	}
	var versionedPoints int64
	if err := db.Model(&model.RecoveryPoint{}).Where("repository_id = ? AND semantics IN ?", before.Repository.ID, []string{
		string(backupasset.PointXirangManifest),
		string(backupasset.PointImportedBaseline),
	}).Count(&versionedPoints).Error; err != nil {
		t.Fatal(err)
	}
	if versionedPoints != 0 {
		t.Fatalf("unexpected versioned recovery points created: %d", versionedPoints)
	}

	// 4. Summary projection remains legacy_mutable
	summary, err := service.RsyncVersioningSummary(context.Background(), before.Task.ID)
	if err != nil {
		t.Fatalf("RsyncVersioningSummary after rejected activation: %v", err)
	}
	if summary.Mode != backupasset.PublicationLegacyMutable ||
		summary.State != backupasset.RsyncVersioningLegacy ||
		summary.ReasonCode != backupasset.RsyncVersioningReasonLegacy ||
		summary.TaskRevision != strconv.FormatUint(before.Revision, 10) {
		t.Fatalf("summary after rejected activation=%+v", summary)
	}
}

func TestRsyncVersioningPreflightCreatesManagedRootThenMovedRootOrMissingControlDirActivationFailsClosedWithoutDiskOrDBSideEffects(t *testing.T) {
	db := newRepositoryTestDB(t)
	for _, choice := range []backupasset.RsyncVersioningMigrationChoice{
		backupasset.RsyncVersioningFirstNewPoint,
		backupasset.RsyncVersioningImportedBaseline,
	} {
		t.Run(string(choice), func(t *testing.T) {
			for _, scenario := range []string{"moved_managed_root", "missing_staging_dir", "missing_points_dir", "replaced_empty_root", "tampered_repository_marker"} {
				t.Run(scenario, func(t *testing.T) {
					root := t.TempDir()
					legacyTarget := filepath.Join(root, "legacy")
					source := filepath.Join(root, "source")
					managedRoot := legacyTarget + ".xirang-rsync-v1"
					movedRoot := filepath.Join(root, "moved-managed-root")
					for _, dir := range []string{legacyTarget, source} {
						if err := os.Mkdir(dir, 0o700); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.WriteFile(filepath.Join(legacyTarget, "baseline.txt"), []byte("legacy-data\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(source, "source.txt"), []byte("source-data\n"), 0o600); err != nil {
						t.Fatal(err)
					}

					taskEntity := seedTask(t, db, "rsync", legacyTarget, "")
					if err := db.Model(&model.Node{}).Where("id = ?", taskEntity.NodeID).Update("host", "").Error; err != nil {
						t.Fatal(err)
					}
					nextRun := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
					if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]any{
						"rsync_source": source,
						"enabled":      true,
						"skip_next":    true,
						"next_run_at":  nextRun,
					}).Error; err != nil {
						t.Fatal(err)
					}

					now := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
					foundation := backupasset.NewFoundationService(completeRepositoryFoundationSettings(true))
					keyring := backupasset.NewKeyring(db, func() time.Time { return now })
					registry := provider.NewRegistry()
					strategy := &rsyncPublicationStrategyStub{
						prepare: func(_ context.Context, _ provider.PublicationPrepareRequest) (provider.PreparedPublication, error) {
							t.Fatal("publication strategy prepare must not be invoked when managed root drifted")
							return provider.PreparedPublication{}, nil
						},
					}
					if err := registry.Register(backupasset.ProviderRsync, provider.Registration{
						Prober:              scopedObservationProber(backupasset.ProviderRsync),
						PublicationStrategy: strategy,
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
					if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
						t.Fatal(err)
					}

					if _, err := os.Lstat(managedRoot); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("managedRoot existed before first preflight: %v", err)
					}

					before := captureRsyncVersioningInvariantSnapshot(t, db, taskEntity.ID)
					reqCtx := RequestContext{
						Actor:         backupasset.AuditActor{UserID: 7, Username: "operator", Role: "operator"},
						CorrelationID: "rsync-round-nine-" + scenario,
					}
					preflight, err := service.CreateRsyncVersioningPreflightForRequest(context.Background(), backupasset.RsyncVersioningPreflightRequest{
						TaskID:               before.Task.ID,
						ExpectedTaskRevision: before.Revision,
						RequestedMode:        backupasset.PublicationVersionedHardlink,
					}, reqCtx)
					if err != nil {
						t.Fatalf("first valid preflight failed: %v", err)
					}

					// Verify first valid preflight created dedicated managed root and control layout.
					firstRootStat, err := os.Lstat(managedRoot)
					if err != nil || !firstRootStat.IsDir() {
						t.Fatalf("first valid preflight did not create managedRoot %s: %v", managedRoot, err)
					}
					firstSys, ok := firstRootStat.Sys().(*syscall.Stat_t)
					if !ok {
						t.Fatal("missing syscall.Stat_t for managedRoot")
					}
					for _, child := range []string{"repository.json", "staging", "points"} {
						if _, err := os.Lstat(filepath.Join(managedRoot, child)); err != nil {
							t.Fatalf("first valid preflight did not create %s: %v", child, err)
						}
					}

					// Verify repeated valid preflight reuses the existing managed root inode.
					secondPreflight, err := service.CreateRsyncVersioningPreflightForRequest(context.Background(), backupasset.RsyncVersioningPreflightRequest{
						TaskID:               before.Task.ID,
						ExpectedTaskRevision: before.Revision,
						RequestedMode:        backupasset.PublicationVersionedHardlink,
					}, reqCtx)
					if err != nil {
						t.Fatalf("second valid preflight failed: %v", err)
					}
					secondRootStat, err := os.Lstat(managedRoot)
					if err != nil {
						t.Fatal(err)
					}
					if secondRootStat.Sys().(*syscall.Stat_t).Ino != firstSys.Ino {
						t.Fatalf("second preflight replaced managedRoot inode: first=%d second=%d", firstSys.Ino, secondRootStat.Sys().(*syscall.Stat_t).Ino)
					}

					switch scenario {
					case "moved_managed_root":
						if err := os.Rename(managedRoot, movedRoot); err != nil {
							t.Fatal(err)
						}
					case "missing_staging_dir":
						if err := os.Remove(filepath.Join(managedRoot, "staging")); err != nil {
							t.Fatal(err)
						}
					case "missing_points_dir":
						if err := os.Remove(filepath.Join(managedRoot, "points")); err != nil {
							t.Fatal(err)
						}
					case "replaced_empty_root":
						if err := os.Rename(managedRoot, movedRoot); err != nil {
							t.Fatal(err)
						}
						if err := os.Mkdir(managedRoot, 0o700); err != nil {
							t.Fatal(err)
						}
					case "tampered_repository_marker":
						if err := os.WriteFile(filepath.Join(managedRoot, "repository.json"), []byte(`{"layout_version":1,"tampered":true}`), 0o600); err != nil {
							t.Fatal(err)
						}
					}

					_, activateErr := service.ActivateRsyncVersioningForRequest(context.Background(), backupasset.RsyncVersioningActivationRequest{
						TaskID:               before.Task.ID,
						ExpectedTaskRevision: before.Revision,
						PreflightID:          secondPreflight.PreflightID,
						MigrationChoice:      choice,
					}, reqCtx)
					if !errors.Is(activateErr, backupasset.ErrConflict) {
						t.Fatalf("ActivateRsyncVersioningForRequest error=%v, want %v", activateErr, backupasset.ErrConflict)
					}
					for _, anotherTicket := range []string{preflight.PreflightID} {
						_, err := service.ActivateRsyncVersioningForRequest(context.Background(), backupasset.RsyncVersioningActivationRequest{
							TaskID:               before.Task.ID,
							ExpectedTaskRevision: before.Revision,
							PreflightID:          anotherTicket,
							MigrationChoice:      choice,
						}, reqCtx)
						if !errors.Is(err, backupasset.ErrConflict) {
							t.Fatalf("ActivateRsyncVersioningForRequest with earlier ticket error=%v, want %v", err, backupasset.ErrConflict)
						}
					}

					// Verify disk state had zero recreation side effects.
					switch scenario {
					case "moved_managed_root":
						if _, err := os.Lstat(managedRoot); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("Activate recreated moved managedRoot at %s: err=%v", managedRoot, err)
						}
						movedStat, err := os.Lstat(movedRoot)
						if err != nil {
							t.Fatalf("movedRoot missing: %v", err)
						}
						if movedStat.Sys().(*syscall.Stat_t).Ino != firstSys.Ino {
							t.Fatalf("movedRoot inode changed: got %d want %d", movedStat.Sys().(*syscall.Stat_t).Ino, firstSys.Ino)
						}
					case "missing_staging_dir":
						if _, err := os.Lstat(filepath.Join(managedRoot, "staging")); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("Activate recreated missing staging dir: err=%v", err)
						}
					case "missing_points_dir":
						if _, err := os.Lstat(filepath.Join(managedRoot, "points")); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("Activate recreated missing points dir: err=%v", err)
						}
					case "replaced_empty_root":
						entries, err := os.ReadDir(managedRoot)
						if err != nil {
							t.Fatal(err)
						}
						if len(entries) != 0 {
							t.Fatalf("Activate created files inside replaced empty root: %+v", entries)
						}
					case "tampered_repository_marker":
						markerBytes, err := os.ReadFile(filepath.Join(managedRoot, "repository.json"))
						if err != nil || string(markerBytes) != `{"layout_version":1,"tampered":true}` {
							t.Fatalf("Activate overwrote tampered repository.json: err=%v bytes=%q", err, string(markerBytes))
						}
					}
					legacyBytes, err := os.ReadFile(filepath.Join(legacyTarget, "baseline.txt"))
					if err != nil || string(legacyBytes) != "legacy-data\n" {
						t.Fatalf("legacy target mutated: err=%v bytes=%q", err, string(legacyBytes))
					}

					assertRsyncVersioningInvariantsUnchanged(t, db, service, before, legacyTarget)
				})
			}
		})
	}
}

func TestRsyncVersioningActivationRequiresTaskRevisionPreflightAndFence(t *testing.T) {
	db := newRepositoryTestDB(t)
	root := t.TempDir()
	legacyTarget := filepath.Join(root, "legacy")
	source := filepath.Join(root, "source")
	managedRoot := legacyTarget + ".xirang-rsync-v1"
	for _, dir := range []string{legacyTarget, source} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(legacyTarget, "legacy.txt"), []byte("legacy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "source.txt"), []byte("source\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	taskEntity := seedTask(t, db, "rsync", legacyTarget, "")
	if err := db.Model(&model.Node{}).Where("id = ?", taskEntity.NodeID).Update("host", "").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]any{
		"rsync_source": source,
		"enabled":      true,
		"skip_next":    true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 4, 7, 30, 0, 0, time.UTC)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, scopedObservationProber(backupasset.ProviderRsync))
	service.now = func() time.Time { return now }
	service.keyring = backupasset.NewKeyring(db, nil)
	transitioner := &rsyncVersioningTransitioner{}
	service.admission = transitioner
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}

	before := captureRsyncVersioningInvariantSnapshot(t, db, taskEntity.ID)
	reqCtx := RequestContext{
		Actor:         backupasset.AuditActor{UserID: 7, Username: "operator", Role: "operator"},
		CorrelationID: "rsync-activation-gates",
	}

	issuePreflight := func(t *testing.T) backupasset.RsyncVersioningPreflightResult {
		t.Helper()
		snap := captureRsyncVersioningInvariantSnapshot(t, db, taskEntity.ID)
		preflight, err := service.CreateRsyncVersioningPreflightForRequest(context.Background(), backupasset.RsyncVersioningPreflightRequest{
			TaskID:               snap.Task.ID,
			ExpectedTaskRevision: snap.Revision,
			RequestedMode:        backupasset.PublicationVersionedFullCopy,
		}, reqCtx)
		if err != nil {
			t.Fatalf("CreateRsyncVersioningPreflightForRequest: %v", err)
		}
		return preflight
	}

	t.Run("request_revision_mismatch", func(t *testing.T) {
		preflight := issuePreflight(t)
		_, err := service.ActivateRsyncVersioningForRequest(context.Background(), backupasset.RsyncVersioningActivationRequest{
			TaskID:               before.Task.ID,
			ExpectedTaskRevision: before.Revision + 1,
			PreflightID:          preflight.PreflightID,
			MigrationChoice:      backupasset.RsyncVersioningFirstNewPoint,
		}, reqCtx)
		if !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("request revision mismatch error=%v, want %v", err, backupasset.ErrConflict)
		}
		assertRsyncVersioningInvariantsUnchanged(t, db, service, before, legacyTarget)
	})

	t.Run("invalid_preflight_id", func(t *testing.T) {
		_ = issuePreflight(t)
		_, err := service.ActivateRsyncVersioningForRequest(context.Background(), backupasset.RsyncVersioningActivationRequest{
			TaskID:               before.Task.ID,
			ExpectedTaskRevision: before.Revision,
			PreflightID:          "not-an-opaque-id",
			MigrationChoice:      backupasset.RsyncVersioningFirstNewPoint,
		}, reqCtx)
		if !errors.Is(err, backupasset.ErrInvalidState) {
			t.Fatalf("invalid preflight id error=%v, want %v", err, backupasset.ErrInvalidState)
		}
		assertRsyncVersioningInvariantsUnchanged(t, db, service, before, legacyTarget)
	})

	t.Run("unknown_preflight_id", func(t *testing.T) {
		_ = issuePreflight(t)
		_, err := service.ActivateRsyncVersioningForRequest(context.Background(), backupasset.RsyncVersioningActivationRequest{
			TaskID:               before.Task.ID,
			ExpectedTaskRevision: before.Revision,
			PreflightID:          strings.Repeat("f", 32),
			MigrationChoice:      backupasset.RsyncVersioningFirstNewPoint,
		}, reqCtx)
		if !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("unknown preflight id error=%v, want %v", err, backupasset.ErrConflict)
		}
		assertRsyncVersioningInvariantsUnchanged(t, db, service, before, legacyTarget)
	})

	t.Run("expired_preflight_ticket", func(t *testing.T) {
		preflight := issuePreflight(t)
		savedNow := now
		now = now.Add(defaultRsyncVersioningPreflightTTL + time.Second)
		defer func() { now = savedNow }()
		_, err := service.ActivateRsyncVersioningForRequest(context.Background(), backupasset.RsyncVersioningActivationRequest{
			TaskID:               before.Task.ID,
			ExpectedTaskRevision: before.Revision,
			PreflightID:          preflight.PreflightID,
			MigrationChoice:      backupasset.RsyncVersioningFirstNewPoint,
		}, reqCtx)
		if !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("expired preflight error=%v, want %v", err, backupasset.ErrConflict)
		}
		assertRsyncVersioningInvariantsUnchanged(t, db, service, before, legacyTarget)
	})

	t.Run("preflight_fence_digest_drift", func(t *testing.T) {
		preflight := issuePreflight(t)
		transitioner.beforePersist = func() error {
			service.preflights.mu.Lock()
			record := service.preflights.records[preflight.PreflightID]
			record.evidence.Digest = "not-a-digest"
			service.preflights.records[preflight.PreflightID] = record
			service.preflights.mu.Unlock()
			return nil
		}
		defer func() { transitioner.beforePersist = nil }()
		_, err := service.ActivateRsyncVersioningForRequest(context.Background(), backupasset.RsyncVersioningActivationRequest{
			TaskID:               before.Task.ID,
			ExpectedTaskRevision: before.Revision,
			PreflightID:          preflight.PreflightID,
			MigrationChoice:      backupasset.RsyncVersioningFirstNewPoint,
		}, reqCtx)
		if !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("preflight fence digest drift error=%v, want %v", err, backupasset.ErrConflict)
		}
		assertRsyncVersioningInvariantsUnchanged(t, db, service, before, legacyTarget)
	})

	t.Run("preflight_marker_identity_drift_in_flight", func(t *testing.T) {
		preflight := issuePreflight(t)
		transitioner.beforePersist = func() error {
			service.preflights.mu.Lock()
			record := service.preflights.records[preflight.PreflightID]
			record.evidence.RepositoryMarkerDigest = strings.Repeat("e", 64)
			service.preflights.records[preflight.PreflightID] = record
			service.preflights.mu.Unlock()
			return nil
		}
		defer func() { transitioner.beforePersist = nil }()
		_, err := service.ActivateRsyncVersioningForRequest(context.Background(), backupasset.RsyncVersioningActivationRequest{
			TaskID:               before.Task.ID,
			ExpectedTaskRevision: before.Revision,
			PreflightID:          preflight.PreflightID,
			MigrationChoice:      backupasset.RsyncVersioningFirstNewPoint,
		}, reqCtx)
		if !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("preflight marker identity drift error=%v, want %v", err, backupasset.ErrConflict)
		}
		assertRsyncVersioningInvariantsUnchanged(t, db, service, before, legacyTarget)
	})

	t.Run("persisted_task_revision_advanced_after_preflight", func(t *testing.T) {
		preflight := issuePreflight(t)
		bumpedTime := before.Task.UpdatedAt.Add(2 * time.Second).UTC()
		if err := db.Model(&model.Task{}).Where("id = ?", before.Task.ID).UpdateColumn("updated_at", bumpedTime).Error; err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = db.Model(&model.Task{}).Where("id = ?", before.Task.ID).UpdateColumn("updated_at", before.Task.UpdatedAt).Error
		}()
		_, err := service.ActivateRsyncVersioningForRequest(context.Background(), backupasset.RsyncVersioningActivationRequest{
			TaskID:               before.Task.ID,
			ExpectedTaskRevision: before.Revision,
			PreflightID:          preflight.PreflightID,
			MigrationChoice:      backupasset.RsyncVersioningFirstNewPoint,
		}, reqCtx)
		if !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("persisted task revision drift error=%v, want %v", err, backupasset.ErrConflict)
		}
		if err := db.Model(&model.Task{}).Where("id = ?", before.Task.ID).UpdateColumn("updated_at", before.Task.UpdatedAt).Error; err != nil {
			t.Fatal(err)
		}
		assertRsyncVersioningInvariantsUnchanged(t, db, service, before, legacyTarget)
	})

	t.Run("active_task_run", func(t *testing.T) {
		preflight := issuePreflight(t)
		activeRun := model.TaskRun{
			TaskID:         before.Task.ID,
			NodeIDSnapshot: before.Task.NodeID,
			TriggerType:    "manual",
			Status:         model.TaskRunStatusRunning,
			StartedAt:      &now,
		}
		if err := db.Create(&activeRun).Error; err != nil {
			t.Fatal(err)
		}
		_, err := service.ActivateRsyncVersioningForRequest(context.Background(), backupasset.RsyncVersioningActivationRequest{
			TaskID:               before.Task.ID,
			ExpectedTaskRevision: before.Revision,
			PreflightID:          preflight.PreflightID,
			MigrationChoice:      backupasset.RsyncVersioningFirstNewPoint,
		}, reqCtx)
		if !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("active task run error=%v, want %v", err, backupasset.ErrConflict)
		}
		if err := db.Delete(&model.TaskRun{}, activeRun.ID).Error; err != nil {
			t.Fatal(err)
		}
		assertRsyncVersioningInvariantsUnchanged(t, db, service, before, legacyTarget)
	})

	t.Run("persisted_link_publication_mode_drift", func(t *testing.T) {
		preflight := issuePreflight(t)
		if err := db.Model(&model.TaskRepositoryLink{}).Where("id = ?", before.Link.ID).UpdateColumn("publication_mode", string(backupasset.PublicationVersionedFullCopy)).Error; err != nil {
			t.Fatal(err)
		}
		_, err := service.ActivateRsyncVersioningForRequest(context.Background(), backupasset.RsyncVersioningActivationRequest{
			TaskID:               before.Task.ID,
			ExpectedTaskRevision: before.Revision,
			PreflightID:          preflight.PreflightID,
			MigrationChoice:      backupasset.RsyncVersioningFirstNewPoint,
		}, reqCtx)
		if !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("persisted link publication_mode drift error=%v, want %v", err, backupasset.ErrConflict)
		}
		if err := db.Model(&model.TaskRepositoryLink{}).Where("id = ?", before.Link.ID).UpdateColumn("publication_mode", before.Link.PublicationMode).Error; err != nil {
			t.Fatal(err)
		}
		assertRsyncVersioningInvariantsUnchanged(t, db, service, before, legacyTarget)
	})

	t.Run("persisted_repository_version_mode_drift", func(t *testing.T) {
		preflight := issuePreflight(t)
		if err := db.Model(&model.BackupRepository{}).Where("id = ?", before.Repository.ID).UpdateColumn("version_mode", string(backupasset.VersionFullCopyTree)).Error; err != nil {
			t.Fatal(err)
		}
		_, err := service.ActivateRsyncVersioningForRequest(context.Background(), backupasset.RsyncVersioningActivationRequest{
			TaskID:               before.Task.ID,
			ExpectedTaskRevision: before.Revision,
			PreflightID:          preflight.PreflightID,
			MigrationChoice:      backupasset.RsyncVersioningFirstNewPoint,
		}, reqCtx)
		if !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("persisted repository version_mode drift error=%v, want %v", err, backupasset.ErrConflict)
		}
		if err := db.Model(&model.BackupRepository{}).Where("id = ?", before.Repository.ID).UpdateColumn("version_mode", before.Repository.VersionMode).Error; err != nil {
			t.Fatal(err)
		}
		assertRsyncVersioningInvariantsUnchanged(t, db, service, before, legacyTarget)
	})

	if _, err := os.Lstat(managedRoot); err != nil {
		t.Fatalf("managedRoot should remain valid for subsequent clean preflight: %v", err)
	}
}

func TestCreateRsyncVersioningPreflightValidatesPathSeparationBeforeBootstrapWithoutDiskOrDBSideEffects(t *testing.T) {
	modes := []backupasset.TaskPublicationMode{
		backupasset.PublicationVersionedFullCopy,
		backupasset.PublicationVersionedHardlink,
	}

	setupNormalConnectedRsyncTask := func(t *testing.T, legacyTarget, configuredSource string) (*gorm.DB, *Service, model.Task, rsyncVersioningInvariantSnapshot) {
		t.Helper()
		db := newRepositoryTestDB(t)
		now := time.Date(2026, 11, 4, 14, 0, 0, 0, time.UTC)

		nodeRepo := gormrepo.NewNodeRepository(db)
		localNode := model.Node{Name: "local-node-" + strconv.FormatInt(time.Now().UnixNano(), 10), Host: "", Port: 22, Username: "root"}
		if err := nodeRepo.Create(context.Background(), &localNode); err != nil {
			t.Fatalf("create local node: %v", err)
		}

		taskAPI := taskservice.NewTaskApiService(
			gormrepo.NewTaskRepository(db),
			nodeRepo,
			gormrepo.NewPolicyRepository(db),
			nil,
		)
		createdTask, err := taskAPI.CreateTask(context.Background(), taskservice.CreateTaskInput{
			Name:         "normal-rsync-task-" + strconv.FormatInt(time.Now().UnixNano(), 10),
			NodeID:       localNode.ID,
			ExecutorType: "rsync",
			RsyncSource:  configuredSource,
			RsyncTarget:  legacyTarget,
		})
		if err != nil {
			t.Fatalf("CreateTask via normal task entrypoint failed: %v", err)
		}

		service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, scopedObservationProber(backupasset.ProviderRsync))
		service.now = func() time.Time { return now }
		service.keyring = backupasset.NewKeyring(db, func() time.Time { return now })
		service.admission = &rsyncVersioningTransitioner{}
		if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: createdTask.ID}, RequestContext{}); err != nil {
			t.Fatalf("Connect failed: %v", err)
		}

		var persistedTask model.Task
		if err := db.First(&persistedTask, createdTask.ID).Error; err != nil {
			t.Fatal(err)
		}
		before := captureRsyncVersioningInvariantSnapshot(t, db, persistedTask.ID)
		return db, service, persistedTask, before
	}

	for _, mode := range modes {
		t.Run("moved_overlapping_source_"+string(mode), func(t *testing.T) {
			caseDir := filepath.Join(t.TempDir(), "case")
			legacyTarget := filepath.Join(caseDir, "legacy")
			configuredSource := legacyTarget + ".xirang-rsync-v1"
			movedSource := filepath.Join(caseDir, "moved-source")
			for _, dir := range []string{legacyTarget, configuredSource} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(legacyTarget, "legacy.txt"), []byte("legacy-bytes\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(configuredSource, "source.txt"), []byte("source-bytes\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			db, service, taskEntity, before := setupNormalConnectedRsyncTask(t, legacyTarget, configuredSource)
			sourceBeforeMoveStat, err := os.Lstat(configuredSource)
			if err != nil {
				t.Fatal(err)
			}
			sourceBeforeInode := sourceBeforeMoveStat.Sys().(*syscall.Stat_t).Ino

			if err := os.Rename(configuredSource, movedSource); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(configuredSource); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("configuredSource still exists after move: %v", err)
			}

			result, err := service.CreateRsyncVersioningPreflightForRequest(context.Background(), backupasset.RsyncVersioningPreflightRequest{
				TaskID:               taskEntity.ID,
				ExpectedTaskRevision: before.Revision,
				RequestedMode:        mode,
			}, RequestContext{
				Actor:         backupasset.AuditActor{UserID: 7, Username: "operator", Role: "operator"},
				CorrelationID: "rsync-first-preflight-moved-overlap-" + string(mode),
			})
			if !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("CreateRsyncVersioningPreflightForRequest error=%v, want %v (result=%+v)", err, backupasset.ErrConflict, result)
			}
			if result.PreflightID != "" {
				t.Fatalf("unexpected preflight ID issued on conflict: %q", result.PreflightID)
			}
			service.preflights.mu.Lock()
			ticketCount := len(service.preflights.records)
			service.preflights.mu.Unlock()
			if ticketCount != 0 {
				t.Fatalf("usable preflight tickets in store=%d, want 0", ticketCount)
			}

			if _, err := os.Lstat(configuredSource); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("first preflight recreated moved source path %s: err=%v", configuredSource, err)
			}
			for _, control := range []string{"repository.json", "staging", "points"} {
				if _, err := os.Lstat(filepath.Join(configuredSource, control)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("first preflight wrote control entry %s at original source path: err=%v", control, err)
				}
			}

			movedStat, err := os.Lstat(movedSource)
			if err != nil {
				t.Fatalf("movedSource missing after preflight: %v", err)
			}
			if movedStat.Sys().(*syscall.Stat_t).Ino != sourceBeforeInode {
				t.Fatalf("movedSource inode drifted: got %d want %d", movedStat.Sys().(*syscall.Stat_t).Ino, sourceBeforeInode)
			}
			legacyBytes, err := os.ReadFile(filepath.Join(legacyTarget, "legacy.txt"))
			if err != nil || string(legacyBytes) != "legacy-bytes\n" {
				t.Fatalf("legacy target mutated: err=%v bytes=%q", err, string(legacyBytes))
			}
			assertRsyncVersioningInvariantsUnchanged(t, db, service, before, legacyTarget)
		})

		t.Run("moved_descendant_source_"+string(mode), func(t *testing.T) {
			caseDir := filepath.Join(t.TempDir(), "case")
			legacyTarget := filepath.Join(caseDir, "legacy")
			managedRoot := legacyTarget + ".xirang-rsync-v1"
			configuredSource := filepath.Join(managedRoot, "sub")
			movedRoot := filepath.Join(caseDir, "moved-root")
			for _, dir := range []string{legacyTarget, configuredSource} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(legacyTarget, "legacy.txt"), []byte("legacy-bytes\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			db, service, taskEntity, before := setupNormalConnectedRsyncTask(t, legacyTarget, configuredSource)
			if err := os.Rename(managedRoot, movedRoot); err != nil {
				t.Fatal(err)
			}

			_, err := service.CreateRsyncVersioningPreflightForRequest(context.Background(), backupasset.RsyncVersioningPreflightRequest{
				TaskID:               taskEntity.ID,
				ExpectedTaskRevision: before.Revision,
				RequestedMode:        mode,
			}, RequestContext{
				Actor:         backupasset.AuditActor{UserID: 7, Username: "operator", Role: "operator"},
				CorrelationID: "rsync-first-preflight-descendant-" + string(mode),
			})
			if !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("CreateRsyncVersioningPreflightForRequest error=%v, want %v", err, backupasset.ErrConflict)
			}
			if _, err := os.Lstat(managedRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("first preflight recreated managedRoot %s: err=%v", managedRoot, err)
			}
			assertRsyncVersioningInvariantsUnchanged(t, db, service, before, legacyTarget)
		})

		t.Run("positive_control_independent_source_"+string(mode), func(t *testing.T) {
			caseDir := filepath.Join(t.TempDir(), "case")
			legacyTarget := filepath.Join(caseDir, "legacy")
			configuredSource := filepath.Join(caseDir, "source")
			managedRoot := legacyTarget + ".xirang-rsync-v1"
			for _, dir := range []string{legacyTarget, configuredSource} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(legacyTarget, "legacy.txt"), []byte("legacy-bytes\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(configuredSource, "source.txt"), []byte("source-bytes\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			db, service, taskEntity, before := setupNormalConnectedRsyncTask(t, legacyTarget, configuredSource)
			result, err := service.CreateRsyncVersioningPreflightForRequest(context.Background(), backupasset.RsyncVersioningPreflightRequest{
				TaskID:               taskEntity.ID,
				ExpectedTaskRevision: before.Revision,
				RequestedMode:        mode,
			}, RequestContext{
				Actor:         backupasset.AuditActor{UserID: 7, Username: "operator", Role: "operator"},
				CorrelationID: "rsync-first-preflight-positive-" + string(mode),
			})
			if err != nil {
				t.Fatalf("positive control first preflight failed: %v", err)
			}
			if backupasset.ValidateOpaqueID(result.PreflightID) != nil || result.Mode != mode ||
				result.State != backupasset.RsyncVersioningReady || result.ReasonCode != backupasset.RsyncVersioningReasonReady {
				t.Fatalf("positive control preflight result=%+v", result)
			}
			service.preflights.mu.Lock()
			ticketCount := len(service.preflights.records)
			service.preflights.mu.Unlock()
			if ticketCount != 1 {
				t.Fatalf("usable preflight tickets in store=%d, want 1", ticketCount)
			}
			for _, control := range []string{"repository.json", "staging", "points"} {
				if _, err := os.Lstat(filepath.Join(managedRoot, control)); err != nil {
					t.Fatalf("positive control did not create %s in %s: %v", control, managedRoot, err)
				}
			}
			assertRsyncVersioningInvariantsUnchanged(t, db, service, before, legacyTarget)
		})
	}
}
