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
	"xirang/backend/internal/secure"
)

type catalogReaderSpy struct {
	request provider.CatalogReadRequest
	opened  int
	session provider.CatalogReadSession
}

func (reader *catalogReaderSpy) OpenCatalogRead(_ context.Context, request provider.CatalogReadRequest) (provider.CatalogReadSession, error) {
	reader.opened++
	reader.request = request
	return reader.session, nil
}

func (*catalogReaderSpy) ListPoints(_ context.Context, snapshot provider.ReadSnapshot, _ provider.PageRequest) (provider.NativePointPage, error) {
	return provider.NativePointPage{Items: []provider.NativePoint{{
		OpaqueDigest: strings.Repeat("f", 64), CapturedAt: time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC),
		Semantics: backupasset.PointMutableHead, SourceRevision: snapshot.SourceRevision,
		Locator: provider.PointLocator{Native: "FAKE_SERVER_RESOLVED_POINT_FOR_TEST_ONLY"},
	}}}, nil
}

type catalogSessionSpy struct{ closed int }

func (*catalogSessionSpy) SourceRevision() string { return strings.Repeat("a", 64) }

func (*catalogSessionSpy) ListCanonical(context.Context, provider.PageRequest) (provider.CatalogRecordPage, error) {
	return provider.CatalogRecordPage{Items: []provider.CatalogRecord{{
		NormalizedPath: "docs/report.txt", ParentNormalizedPath: "docs", Name: "report.txt",
		Type: backupasset.CatalogEntryFile, Size: 7,
		ProviderLocator: provider.EntryLocator{Native: "FAKE_PRIVATE_PROVIDER_LOCATOR_FOR_TEST_ONLY"},
	}}}, nil
}

func (*catalogSessionSpy) Finalize(context.Context) (provider.CatalogReadProof, error) {
	return provider.CatalogReadProof{Provider: backupasset.ProviderRsync, Mode: provider.CatalogProofMutableObservation}, nil
}

func (session *catalogSessionSpy) Close() error { session.closed++; return nil }

func TestCatalogPointReadFactoryResolvesMutableSourceFromOpaqueIDsOnly(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := scopedObservationProber(backupasset.ProviderRsync)
	reader := &catalogReaderSpy{session: &catalogSessionSpy{}}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRsync, provider.Registration{
		Prober: prober, PointLister: reader, CatalogReader: reader,
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: registry,
		Now: func() time.Time { return time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil || connected.MutablePoint == nil {
		t.Fatalf("connect=%+v err=%v", connected, err)
	}
	session, err := service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
		RepositoryID: connected.Repository.ID, RecoveryPointID: connected.MutablePoint.ID,
	})
	if err != nil {
		t.Fatalf("open Catalog point: %v", err)
	}
	if reader.opened != 1 || reader.request.Provider != backupasset.ProviderRsync ||
		reader.request.RecoveryPointID != connected.MutablePoint.ID || reader.request.Snapshot.RepositoryID != connected.Repository.ID ||
		reader.request.Mode != provider.CatalogProofMutableObservation || reader.request.Point.Native == "" ||
		reader.request.Manifest != (provider.CatalogManifestProof{}) {
		t.Fatalf("resolved request=%+v opened=%d", reader.request, reader.opened)
	}
	page, err := session.ListCanonical(context.Background(), provider.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("Catalog page=%+v err=%v", page, err)
	}
	record := page.Items[0]
	if record.ProviderLocator.Native != "" || !secure.IsEncrypted(record.SealedProviderLocator) ||
		strings.Contains(record.SealedProviderLocator, "FAKE_PRIVATE_PROVIDER_LOCATOR_FOR_TEST_ONLY") {
		t.Fatalf("unsealed Catalog record=%+v", record)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogPointReadFactoryRejectsCrossRepositoryAndCommandProvider(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)
	reader := &catalogReaderSpy{session: &catalogSessionSpy{}}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRsync, provider.Registration{Prober: &scriptedProber{}, CatalogReader: reader}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{DB: db, Foundation: enabledFoundation(), Registry: registry, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	repositoryID := strings.Repeat("1", 32)
	otherRepositoryID := strings.Repeat("2", 32)
	pointID := strings.Repeat("3", 32)
	for _, repository := range []model.BackupRepository{
		{ID: repositoryID, ProviderKind: string(backupasset.ProviderCommand), DisplayName: "command", VersionMode: string(backupasset.VersionMutableHead), Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1, CapabilitiesJSON: "{}", ImmutabilityLevel: string(backupasset.ImmutabilityMutable), CreatedAt: now, UpdatedAt: now},
		{ID: otherRepositoryID, ProviderKind: string(backupasset.ProviderRsync), DisplayName: "other", VersionMode: string(backupasset.VersionMutableHead), Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1, CapabilitiesJSON: "{}", ImmutabilityLevel: string(backupasset.ImmutabilityMutable), CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.Create(&repository).Error; err != nil {
			t.Fatal(err)
		}
	}
	point := model.RecoveryPoint{
		ID: pointID, RepositoryID: repositoryID, Semantics: string(backupasset.PointMutableHead), State: string(backupasset.RecoveryPointCommitted),
		LineageJSON: "{}", ManifestDigestAlgorithm: "sha256", ConsistencyJSON: "{}", FidelityJSON: "{}",
		CapabilityRevision: 1, CapabilitiesJSON: "{}", ImmutabilityLevel: string(backupasset.ImmutabilityMutable),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	_, err = service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{RepositoryID: repositoryID, RecoveryPointID: pointID})
	var capabilityErr *CapabilityError
	if !errors.As(err, &capabilityErr) || capabilityErr.Reason.Code != backupasset.CapabilityTaskArtifactContractMissing {
		t.Fatalf("Command Catalog error=%v", err)
	}
	if _, err := service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{RepositoryID: otherRepositoryID, RecoveryPointID: pointID}); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("cross-repository Catalog error=%v", err)
	}
	if reader.opened != 0 {
		t.Fatalf("rejected request reached Catalog reader %d time(s)", reader.opened)
	}
}

func TestCatalogPointReadFactoryReconstructsExactResticPublicationProof(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	commit := fixture.commitEvidence()
	if _, err := execution.RecordProviderCommit(context.Background(), resticProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	fixture.manifest.build = func(_ context.Context, attempt provider.ResticAttemptV1, _ provider.ResticCommitV1, _ provider.ManifestLimits) (provider.ResticManifestV1, error) {
		return provider.ResticManifestV1{
			DigestAlgorithm: "sha256", Digest: strings.Repeat("d", 64), Generator: "xirang-restic-ls", GeneratorVersion: "1",
			Completeness: backupasset.ManifestComplete, EntryCount: 0, LogicalBytes: 0, Fidelity: provider.ResticManifestFidelityV1(),
			HeaderCapturedAt: commit.CaptureStartedAt, ObservedTagDigest: publicationTagDigest(attempt.RequiredTags),
		}, nil
	}
	pointID := resticAttemptForExecution(t, execution).RecoveryPointID
	if outcome, err := fixture.service.ProcessPoint(context.Background(), pointID); err != nil || outcome.State != backupasset.RecoveryPointCommitted {
		t.Fatalf("commit Restic point outcome=%+v err=%v", outcome, err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).Update("state", backupasset.RecoveryPointDegraded).Error; err != nil {
		t.Fatal(err)
	}

	reader := &catalogReaderSpy{session: &catalogSessionSpy{}}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, provider.Registration{Prober: &scriptedProber{}, CatalogReader: reader}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: fixture.admission, Publication: fixture.service,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
		RepositoryID: fixture.repository.ID, RecoveryPointID: pointID,
	})
	if err != nil {
		t.Fatalf("open immutable Restic Catalog: %v", err)
	}
	defer func() { _ = session.Close() }()

	request := reader.request
	if reader.opened != 1 || request.Provider != backupasset.ProviderRestic || request.Mode != provider.CatalogProofPublicationManifest ||
		request.RecoveryPointID != pointID || request.Point.Native != commit.NativePointID || request.ResticProof == nil ||
		request.Manifest.Digest != strings.Repeat("d", 64) || request.Manifest.EntryCount != 0 ||
		request.Manifest.SourceRevision != request.Snapshot.SourceRevision {
		t.Fatalf("immutable Restic Catalog request=%+v opened=%d", request, reader.opened)
	}
	proof := request.ResticProof
	if proof.Attempt.RepositoryID != fixture.repository.ID || proof.Attempt.RecoveryPointID != pointID ||
		proof.Attempt.TaskID != fixture.task.ID || proof.Attempt.TaskRunID != fixture.taskRun.ID ||
		proof.Attempt.Access.RepositoryID != fixture.repository.ID || proof.Commit != commit ||
		proof.Attempt.RequiredTags[0] != "xirang.link.v1."+fixture.link.ID ||
		proof.Attempt.RequiredTags[1] != "xirang.point.v1."+pointID {
		t.Fatalf("Restic Catalog proof=%+v", proof)
	}
}

func TestCatalogPointReadFactoryUsesCommittedRsyncEvidenceInsteadOfMutableRoot(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	state := execution.(*rsyncPublicationExecution)
	commit := provider.RsyncTreeCommitV1{
		LayoutVersion: 1, RepositoryID: state.attempt.RepositoryID, TaskRepositoryLinkID: state.attempt.TaskRepositoryLinkID,
		RecoveryPointID: state.attempt.RecoveryPointID, AttemptID: state.attempt.AttemptID, PublicationMode: state.attempt.PublicationMode,
		ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("1", 64), ManifestEntryCount: 1, LogicalBytes: 42,
		FidelityDigest: strings.Repeat("2", 64), SourceFingerprint: managedRsyncSourceFingerprint(state.markerKey, fixture.binding, state.attempt.RecoveryPointID),
		ProviderCommittedAt: fixture.now, CommitMarkerDigest: strings.Repeat("3", 64), ChildFenceDigest: rsyncChildFenceDigest(state.markerKey, state.childFence),
		PointDeadlineAt: state.attempt.PointDeadlineAt, RenameVerified: true, DirectoryFsyncVerified: true,
	}
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRsyncTreeProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", state.attempt.RecoveryPointID).Updates(map[string]any{
		"state": string(backupasset.RecoveryPointCommitted), "committed_at": fixture.now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	manifest := model.RecoveryPointManifest{
		ID: strings.Repeat("8", 32), RecoveryPointID: state.attempt.RecoveryPointID, Revision: 1,
		DigestAlgorithm: "sha256", Digest: commit.ManifestDigest, Generator: "xirang-rsync", GeneratorVersion: "1",
		Completeness: string(backupasset.ManifestComplete), EntryCount: int64(commit.ManifestEntryCount), LogicalBytes: int64(commit.LogicalBytes),
		FidelityJSON: "{}", EncryptedCommitEvidence: "{}", IsActive: true, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&manifest).Error; err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: fixture.service.registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: fixture.admission, Publication: fixture.service,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
		RepositoryID: fixture.repository.ID, RecoveryPointID: state.attempt.RecoveryPointID,
	})
	reason, _, ok := CapabilityFromError(err)
	if !ok || reason.Code != backupasset.CapabilityMutableSourceChanged {
		t.Fatalf("committed Rsync Catalog exact-source error=%v reason=%+v", err, reason)
	}
}

func TestCatalogPointReadFactoryReconstructsExactRcloneControlProof(t *testing.T) {
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
		State: provider.RcloneReconcileProviderCommitted, Commit: &commit,
		Manifest: &provider.RcloneManifestV1{
			ManifestIndexDigest: commit.ManifestIndexDigest, ManifestChunkDigests: append([]string(nil), commit.ManifestChunkDigests...),
			EntryCount: commit.ManifestEntryCount, LogicalBytes: commit.LogicalBytes, FidelityEvidenceDigest: commit.FidelityEvidenceDigest,
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
		t.Fatalf("Rclone preparing outcome=%+v err=%v", outcome, err)
	}
	if outcome, err := fixture.service.ProcessPoint(context.Background(), attempt.RecoveryPointID); err != nil || outcome.State != backupasset.RecoveryPointCommitted {
		t.Fatalf("Rclone verifying outcome=%+v err=%v", outcome, err)
	}

	reader := &catalogReaderSpy{session: &catalogSessionSpy{}}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRclone, provider.Registration{Prober: &scriptedProber{}, CatalogReader: reader}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: fixture.service.admission, Publication: fixture.service,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.OpenCatalogRead(context.Background(), CatalogPointReadRequest{
		RepositoryID: fixture.repository.ID, RecoveryPointID: attempt.RecoveryPointID,
	})
	if err != nil {
		t.Fatalf("open immutable Rclone Catalog: %v", err)
	}
	defer func() { _ = session.Close() }()
	request := reader.request
	if reader.opened != 1 || request.Provider != backupasset.ProviderRclone || request.Mode != provider.CatalogProofPublicationManifest ||
		request.RcloneProof == nil || request.RcloneProof.Reconcile.PortableRequest == nil || request.RcloneProof.Reconcile.NativeRequest != nil ||
		request.RcloneProof.Commit.ManifestIndexDigest != request.Manifest.Digest || request.Manifest.EntryCount != int64(commit.ManifestEntryCount) ||
		request.Point.Native == "" || request.Snapshot.Access.Locator != "" {
		t.Fatalf("immutable Rclone Catalog request=%+v opened=%d", request, reader.opened)
	}
}
