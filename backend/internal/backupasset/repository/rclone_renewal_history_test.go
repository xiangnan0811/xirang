package repository

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/backupasset/retention"
	"xirang/backend/internal/model"
)

func TestRcloneVersioningRenewalPreservesRealHistoryAndDeadlines(t *testing.T) {
	ctx := context.Background()
	fixture := newRclonePublicationFixture(t, backupasset.PublicationVersionedPrefix)
	committedAttempt, committedPoint, manifest := completeRcloneTestPoint(t, fixture)
	// The first run has finished. Prepare a second point that will outlive its
	// original deadline, then finish its run so renewal is admitted.
	if err := fixture.db.Model(&model.TaskRun{}).Where("id = ?", fixture.taskRun.ID).Updates(map[string]any{"status": "success", "finished_at": fixture.now}).Error; err != nil {
		t.Fatal(err)
	}
	secondRun := model.TaskRun{TaskID: fixture.task.ID, NodeIDSnapshot: fixture.task.NodeID, TriggerType: "manual", Status: "running", StartedAt: timePointer(fixture.now), CreatedAt: fixture.now, UpdatedAt: fixture.now}
	if err := fixture.db.Create(&secondRun).Error; err != nil {
		t.Fatal(err)
	}
	oldExecution, err := fixture.service.Prepare(ctx, publication.Run{Task: fixture.task, TaskRunID: secondRun.ID, Trigger: "manual", StartedAt: fixture.now, Audit: fixture.run().Audit})
	if err != nil {
		t.Fatal(err)
	}
	oldAttempt, err := oldExecution.Attempt().RcloneAttempt()
	if err != nil {
		t.Fatal(err)
	}
	if err := oldExecution.Abandon(backupasset.ErrPublicationSessionAbandoned); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.TaskRun{}).Where("id = ?", secondRun.ID).Updates(map[string]any{"status": "success", "finished_at": fixture.now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.Task{}).Where("id = ?", fixture.task.ID).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	var task model.Task
	if err := fixture.db.Preload("Node").First(&task, fixture.task.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, err := managedRsyncTaskRevision(task)
	if err != nil {
		t.Fatal(err)
	}
	clock := fixture.binding.PreflightExpiresAt.Add(time.Second)
	fixture.service.now = func() time.Time { return clock }
	lease, err := backupasset.NewLeaseService(fixture.db, func() time.Time { return clock }, backupasset.LeaseConfig{Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: 168 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.lease = lease
	if _, err := fixture.service.loadExactManagedRclonePublicationRuntime(ctx, task.ID); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("expired authorization unexpectedly executable: %v", err)
	}
	admission := &rsyncVersioningManagedAdmission{publicationAdmission: &publicationAdmission{mode: publication.AdmissionManaged, generation: 1}}
	service, err := NewService(Dependencies{DB: fixture.db, Foundation: fixture.service.foundation, Registry: fixture.service.registry,
		Keyring: fixture.service.keyring, Now: func() time.Time { return clock }, Admission: admission,
		History: fixture.service.history, Metrics: publication.NoopMetrics{}, Publication: fixture.service})
	if err != nil {
		t.Fatal(err)
	}
	service.rclonePreflighter = &rcloneVersioningPreflighterStub{evidence: RcloneVersioningPreflightEvidence{
		Mode: backupasset.PublicationVersionedPrefix, CapabilityRevision: fixture.binding.CapabilityRevision,
		ConsistencyClass: backupasset.RcloneConsistencyObservationallyStable, HashFidelity: backupasset.RcloneHashDownloadVerifiedBytes,
		EstimatedReadBytes: 64, APICostClass: backupasset.RcloneCostLow, StorageCostClass: backupasset.RcloneCostLow, EgressCostClass: backupasset.RcloneCostLow,
		ManagedRootIdentityDigest: fixture.binding.ManagedRootIdentityDigest, RepositoryMarkerDigest: strings.Repeat("a", 64), EvidenceDigest: strings.Repeat("b", 64),
	}}
	ticket, err := service.CreateRcloneVersioningPreflight(ctx, backupasset.RcloneVersioningPreflightRequest{TaskID: task.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationVersionedPrefix})
	if err != nil || ticket.Purpose != backupasset.RclonePreflightPurposeRenewal {
		t.Fatalf("renewal ticket=%+v err=%v", ticket, err)
	}
	if _, err := service.ActivateRcloneVersioning(ctx, backupasset.RcloneVersioningActivationRequest{TaskID: task.ID, ExpectedTaskRevision: revision, PreflightID: ticket.PreflightID, MigrationChoice: backupasset.RcloneFirstNewPoint}); err != nil {
		t.Fatal(err)
	}
	var oldPoint model.RecoveryPoint
	if err := fixture.db.First(&oldPoint, "id = ?", oldAttempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	persistedAttempt, _, err := decodeManagedRclonePreparedAttemptRecord(oldPoint.EncryptedProviderLocator)
	if err != nil || !persistedAttempt.PointDeadlineAt.Equal(oldAttempt.PointDeadlineAt) {
		t.Fatalf("persisted attempt deadline changed: %v", err)
	}
	lineage, err := backupasset.DecodePublicationLineage(oldPoint.LineageJSON)
	if err != nil || !lineage.PointDeadlineAt.Equal(oldAttempt.PointDeadlineAt) {
		t.Fatalf("persisted lineage deadline changed: %v", err)
	}
	runtime, err := fixture.service.loadExactManagedRclonePublicationRuntime(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !runtime.binding.PreflightExpiresAt.After(oldAttempt.PointDeadlineAt) {
		t.Fatal("persisted binding expiry did not extend")
	}
	if err := validateRcloneReconcileRuntime(runtime, oldPoint, persistedAttempt); err != nil {
		t.Fatalf("historical proof changed: %v", err)
	}
	outcome, err := fixture.service.ProcessPoint(ctx, oldPoint.ID)
	if err != nil || outcome.State != backupasset.RecoveryPointFailed {
		t.Fatalf("expired attempt was revived: %+v %v", outcome, err)
	}
	var expiredPoint model.RecoveryPoint
	if err := fixture.db.First(&expiredPoint, "id = ?", oldPoint.ID).Error; err != nil {
		t.Fatal(err)
	}
	if expiredPoint.State != string(backupasset.RecoveryPointFailed) {
		t.Fatalf("expired point state=%s", expiredPoint.State)
	}
	thirdRun := model.TaskRun{TaskID: task.ID, NodeIDSnapshot: task.NodeID, TriggerType: "manual", Status: "running", StartedAt: timePointer(clock), CreatedAt: clock, UpdatedAt: clock}
	if err := fixture.db.Create(&thirdRun).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	newExecution, err := fixture.service.Prepare(ctx, publication.Run{Task: task, TaskRunID: thirdRun.ID, Trigger: "manual", StartedAt: clock, Audit: fixture.run().Audit})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = newExecution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	newAttempt, err := newExecution.Attempt().RcloneAttempt()
	if err != nil || !newAttempt.PointDeadlineAt.After(oldAttempt.PointDeadlineAt) || newAttempt.PointDeadlineAt.After(runtime.binding.PreflightExpiresAt) {
		t.Fatalf("new publication deadline=%v old=%v err=%v", newAttempt.PointDeadlineAt, oldAttempt.PointDeadlineAt, err)
	}
	assertRenewedCommittedConsumers(t, fixture, committedAttempt, committedPoint, manifest, clock)
}

func assertRenewedCommittedConsumers(t *testing.T, fixture *rclonePublicationFixture, attempt provider.RcloneAttemptV1, point model.RecoveryPoint, manifest model.RecoveryPointManifest, clock time.Time) {
	t.Helper()
	ctx := context.Background()
	inner := &catalogSessionSpy{}
	catalogReader := &catalogReaderSpy{session: inner}
	catalogRegistry := provider.NewRegistry()
	if err := catalogRegistry.Register(backupasset.ProviderRclone, provider.Registration{Prober: &scriptedProber{}, CatalogReader: catalogReader}); err != nil {
		t.Fatal(err)
	}
	catalogAdmission := &catalogAdmissionSpy{mode: publication.AdmissionManaged}
	catalogService, err := NewService(Dependencies{DB: fixture.db, Foundation: fixture.service.foundation, Registry: catalogRegistry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return clock }, Admission: catalogAdmission, Publication: fixture.service})
	if err != nil {
		t.Fatal(err)
	}
	catalogSession, err := catalogService.OpenCatalogRead(ctx, CatalogPointReadRequest{RepositoryID: fixture.repository.ID, RecoveryPointID: point.ID})
	if err != nil {
		t.Fatalf("catalog after renewal: %v", err)
	}
	if _, err := catalogSession.ListCanonical(ctx, provider.PageRequest{Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if err := catalogSession.Close(); err != nil {
		t.Fatal(err)
	}
	if catalogReader.opened != 1 || catalogReader.request.RcloneProof == nil {
		t.Fatal("catalog did not use historical immutable proof")
	}
	generation := model.CatalogGeneration{ID: strings.Repeat("c", 32), RecoveryPointID: point.ID, ManifestID: &manifest.ID, Generation: 1,
		State: string(catalog.GenerationComplete), IsActive: true, SourceFingerprint: point.SourceFingerprint,
		ExpectedEntryCount: 1, WrittenEntryCount: 1, StartedAt: fixture.now, FinishedAt: timePointer(fixture.now), CreatedAt: fixture.now, UpdatedAt: fixture.now}
	if err := fixture.db.Create(&generation).Error; err != nil {
		t.Fatal(err)
	}
	modified := fixture.now.Add(-time.Minute)
	entry := model.CatalogEntry{GenerationID: generation.ID, EntryID: strings.Repeat("b", 64), RecoveryPointID: point.ID,
		NormalizedPath: "docs/portable.txt", Name: "portable.txt", EntryType: string(backupasset.CatalogEntryFile), Size: 15, ModifiedAt: &modified,
		MimeType: "text/plain", Fingerprint: strings.Repeat("e", 64), FingerprintStrength: string(catalog.FingerprintStrong),
		EncryptedProviderLocator: `{"version":1,"native":"portable:docs/portable.txt"}`, SecurityState: "sealed", CreatedAt: fixture.now}
	if err := fixture.db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	locator, err := decodeManagedRclonePointLocator(point.EncryptedProviderLocator)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := provider.DecodeRcloneCommitV1(locator.TaggedCommit)
	if err != nil {
		t.Fatal(err)
	}
	reader := &contentSourceProviderSpy{sourceRevision: commit.DestinationObservationDigest,
		pointLocator: provider.PointLocator{Native: "managed:" + point.ID + ":" + attempt.AttemptID + ":" + point.ManifestDigest},
		entryLocator: provider.EntryLocator{Native: "docs/portable.txt"}, body: []byte("rclone-portable"),
		stat: provider.ContentStat{Size: entry.Size, ModTime: modified, SourceRevision: strings.Repeat("7", 64), MediaType: "text/plain"}}
	// The controlled content boundary must also supply the sealed Portable
	// manifest tuple and snapshot verifier now required by immutable reads.
	proof := &portableContentProofSpy{
		PublicationStrategy: fixture.strategy,
		attempt:             locator.TaggedAttempt,
		commit:              locator.TaggedCommit,
		path:                "docs/portable.txt",
		binding: provider.RclonePortableContentBinding{
			ManifestVersion: int(attempt.ManifestSchemaRevision),
			PhysicalModTime: modified,
			ValidateSnapshot: func(ctx context.Context) error {
				return ctx.Err()
			},
			Record: provider.CatalogRecord{
				NormalizedPath: entry.NormalizedPath, ParentNormalizedPath: "docs", Name: entry.Name,
				Type: backupasset.CatalogEntryFile, Size: entry.Size, ModifiedAt: &modified,
				Mode: entry.Mode, Owner: entry.Owner, Fingerprint: entry.Fingerprint, FingerprintStrength: entry.FingerprintStrength,
				ProviderLocator: provider.EntryLocator{Native: "portable:docs/portable.txt"},
			},
		},
	}
	contentRegistry := provider.NewRegistry()
	if err := contentRegistry.Register(backupasset.ProviderRclone, provider.Registration{Prober: &scriptedProber{}, EntryStatter: reader, SequentialReader: reader, RangeReader: reader, PublicationStrategy: proof}); err != nil {
		t.Fatal(err)
	}
	contentService, err := NewService(Dependencies{DB: fixture.db, Foundation: fixture.service.foundation, Registry: contentRegistry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return clock }, Admission: fixture.service.admission, Publication: fixture.service})
	if err != nil {
		t.Fatal(err)
	}
	contentSession, err := contentService.OpenContentSource(ctx, content.SourceRequest{Ref: backupasset.AssetRef{RecoveryPointID: point.ID, EntryID: entry.EntryID}, CatalogGenerationID: generation.ID,
		ExpectedSource: point.SourceFingerprint, ExpectedEntry: entry.Fingerprint, Mode: content.SourceModeSequential, MaxBytes: entry.Size})
	if err != nil {
		t.Fatalf("content after renewal: %v", err)
	}
	defer func() { _ = contentSession.Close() }()
	if contentSession.Stat().ModifiedAt == nil || !contentSession.Stat().ModifiedAt.Equal(modified) {
		t.Fatalf("historical content logical time changed: %v", contentSession.Stat().ModifiedAt)
	}
	payload, err := io.ReadAll(contentSession.Reader())
	if err != nil || string(payload) != "rclone-portable" {
		t.Fatalf("historical content=%q error=%v", payload, err)
	}
	if err := contentSession.Close(); err != nil {
		t.Fatal(err)
	}
	if proof.calls < 2 {
		t.Fatalf("historical content proof was not reacquired at close: calls=%d", proof.calls)
	}
	if err := fixture.db.AutoMigrate(&model.RecoveryPointHold{}, &model.BackupAssetPurgePlan{}); err != nil {
		t.Fatal(err)
	}
	holds, err := retention.NewHoldService(retention.HoldServiceDependencies{DB: fixture.db, Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := retention.NewCoordinator(retention.CoordinatorDependencies{DB: fixture.db, Leases: fixture.service.lease, Holds: holds, Now: func() time.Time { return clock }, LeaseOwnerID: "renewal-history-test"})
	if err != nil {
		t.Fatal(err)
	}
	purge, err := retention.NewPurgeService(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := purge.Preview(ctx, retention.PreviewPurgeRequest{Actor: backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"}, RepositoryID: fixture.repository.ID, RecoveryPointIDs: []string{point.ID}})
	if err != nil || preview.ItemCount != 1 || preview.Items[0].RecoveryPointID != point.ID {
		t.Fatalf("historical lifecycle preview=%+v err=%v", preview, err)
	}
	resolver := newLifecycleDeleteService(t, fixture.service, clock)
	request, err := resolver.ResolveLifecycleDeletePoint(ctx, strings.Repeat("e", 32), point, fixture.repository)
	if err != nil {
		t.Fatalf("resolve historical exact deletion after renewal: %v", err)
	}
	access, ok := request.Snapshot.Access.AdapterData.(provider.RclonePrefixDeletionAccess)
	if !ok || access.Attempt.AttemptID != attempt.AttemptID {
		t.Fatal("historical deletion selected a different attempt")
	}
	deleter := &renewalHistoryControlledDeleter{points: map[string]bool{request.Point.Native: true, "unrelated-history": true}}
	result, err := provider.ExecuteDeletePoint(ctx, deleter, request)
	if err != nil || deleter.calls != 1 || result.Outcome != provider.DeletePointDeleted || deleter.points[request.Point.Native] || !deleter.points["unrelated-history"] {
		t.Fatalf("controlled exact delete result=%+v calls=%d err=%v", result, deleter.calls, err)
	}
}

// This controlled provider boundary checks that the lifecycle resolver passes
// only the admitted historical point; it does not emulate Rclone itself.
type renewalHistoryControlledDeleter struct {
	points map[string]bool
	calls  int
}

func (*renewalHistoryControlledDeleter) ProviderKind() backupasset.ProviderKind {
	return backupasset.ProviderRclone
}

func (deleter *renewalHistoryControlledDeleter) DeletePoint(_ context.Context, request provider.DeletePointRequest) (provider.DeletePointResult, error) {
	deleter.calls++
	if !deleter.points[request.Point.Native] {
		return provider.DeletePointResult{}, provider.ErrDeletePointIdentityConflict
	}
	delete(deleter.points, request.Point.Native)
	return provider.DeletePointResult{Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("a", 64)}, nil
}
