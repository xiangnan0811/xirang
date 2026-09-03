package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/fileaccess"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestVisibilityFiltersSharedRepositoryByLiveCurrentTaskOwnership(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	ownedTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/shared", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	unownedTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/shared", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	archivedTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/shared", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	otherRepositoryTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/other", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	if err := db.Model(&model.Task{}).Where("id = ?", archivedTask.ID).Update("archived_at", now).Error; err != nil {
		t.Fatal(err)
	}
	const operatorID uint = 701
	if err := db.Create(&model.User{ID: operatorID, Username: "visibility-operator", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "operator", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: ownedTask.NodeID, UserID: operatorID, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}

	shared := seedVisibilityRepository(t, db, strings.Repeat("1", 32), "shared", now)
	unownedOnly := seedVisibilityRepository(t, db, strings.Repeat("2", 32), "unowned-only", now.Add(time.Second))
	seedVisibilityLink(t, db, strings.Repeat("3", 32), shared.ID, &ownedTask.ID, "owned-link", unownedTask.NodeID, now)
	// Historical link/point snapshots remain immutable after a Task moves nodes;
	// Operator authority follows the live Task's current NodeID.
	seedVisibilityLink(t, db, strings.Repeat("4", 32), shared.ID, &unownedTask.ID, "unowned-link", ownedTask.NodeID, now)
	seedVisibilityLink(t, db, strings.Repeat("5", 32), shared.ID, &archivedTask.ID, "archived-link", ownedTask.NodeID, now)
	seedVisibilityLink(t, db, strings.Repeat("6", 32), shared.ID, nil, "deleted-link", ownedTask.NodeID, now)
	seedVisibilityLink(t, db, strings.Repeat("7", 32), unownedOnly.ID, &otherRepositoryTask.ID, "other-repository-link", ownedTask.NodeID, now)
	ownedPoint := seedVisibilityPoint(t, db, strings.Repeat("8", 32), shared.ID, &ownedTask.ID, "owned-point", unownedTask.NodeID, now)
	unownedPoint := seedVisibilityPoint(t, db, strings.Repeat("9", 32), shared.ID, &unownedTask.ID, "unowned-point", ownedTask.NodeID, now)
	deletedPoint := seedVisibilityPoint(t, db, strings.Repeat("a", 32), shared.ID, nil, "deleted-point", ownedTask.NodeID, now)

	service := newVisibilityServiceForTest(t, db, now)
	adminPage, err := service.List(context.Background(), RepositoryListRequest{Limit: 20}, VisibilityScope{Role: "admin", UserID: 1}, RequestContext{})
	if err != nil || len(adminPage.Items) != 2 {
		t.Fatalf("admin page=%+v err=%v", adminPage, err)
	}
	adminDetail, err := service.Detail(context.Background(), shared.ID, VisibilityScope{Role: "admin", UserID: 1}, RequestContext{})
	if err != nil || len(adminDetail.Lineages) != 7 {
		t.Fatalf("admin detail=%+v err=%v", adminDetail, err)
	}

	operatorScope := VisibilityScope{Role: "operator", UserID: operatorID}
	operatorPage, err := service.List(context.Background(), RepositoryListRequest{Limit: 20}, operatorScope, RequestContext{})
	if err != nil || len(operatorPage.Items) != 1 || operatorPage.Items[0].ID != shared.ID {
		t.Fatalf("operator page=%+v err=%v", operatorPage, err)
	}
	operatorDetail, err := service.Detail(context.Background(), shared.ID, operatorScope, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	if len(operatorDetail.Lineages) != 2 {
		t.Fatalf("operator lineages=%+v", operatorDetail.Lineages)
	}
	payload, err := json.Marshal(operatorDetail)
	if err != nil {
		t.Fatal(err)
	}
	body := string(payload)
	for _, forbidden := range []string{
		"unowned-link", "archived-link", "deleted-link", "unowned-point", "deleted-point",
		unownedPoint.ID, deletedPoint.ID, "repository_identity", "encrypted_config", "provider_locator",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("operator detail leaked %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, "owned-link") || !strings.Contains(body, ownedPoint.ID) {
		t.Fatalf("owned lineage missing: %s", body)
	}
	if _, err := service.Detail(context.Background(), unownedOnly.ID, operatorScope, RequestContext{}); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("unowned detail error=%v", err)
	}
	if _, err := service.Detail(context.Background(), strings.Repeat("f", 32), operatorScope, RequestContext{}); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("missing detail error=%v", err)
	}
}

func TestVisibilityRequiresLiveCurrentTaskNodeAcrossRepositoryAndLineages(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		missingNode bool
	}{
		{name: "archived"},
		{name: "missing_with_stale_owner", missingNode: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newRepositoryTestDB(t)
			now := time.Date(2026, 8, 28, 19, 30, 0, 0, time.UTC)
			task := seedTask(t, db, "restic", "sftp:user@example.invalid:/unavailable-node", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
			const operatorID uint = 703
			if err := db.Create(&model.User{
				ID: operatorID, Username: "node-liveness-operator", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
				Role: "operator", CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&model.NodeOwner{NodeID: task.NodeID, UserID: operatorID, CreatedAt: now}).Error; err != nil {
				t.Fatal(err)
			}
			repository := seedVisibilityRepository(t, db, strings.Repeat("b", 32), "unavailable-current-node", now)
			seedVisibilityLink(t, db, strings.Repeat("c", 32), repository.ID, &task.ID, "retained-link", task.NodeID, now)
			point := seedVisibilityPoint(t, db, strings.Repeat("d", 32), repository.ID, &task.ID, "retained-point", task.NodeID, now)
			if testCase.missingNode {
				deleteVisibilityNodeRetainingOwner(t, db, task.NodeID)
			} else if err := db.Model(&model.Node{}).Where("id = ?", task.NodeID).Update("archived", true).Error; err != nil {
				t.Fatal(err)
			}

			service := newVisibilityServiceForTest(t, db, now)
			operatorScope := VisibilityScope{Role: "operator", UserID: operatorID}
			operatorPage, err := service.List(context.Background(), RepositoryListRequest{Limit: 20}, operatorScope, RequestContext{})
			if err != nil {
				t.Fatal(err)
			}
			if len(operatorPage.Items) != 0 {
				t.Fatalf("operator repository visibility=%+v through unavailable current node", operatorPage.Items)
			}
			operatorLineages, err := service.loadLineages(context.Background(), repository.ID, operatorScope)
			if err != nil {
				t.Fatal(err)
			}
			if len(operatorLineages) != 0 {
				t.Fatalf("operator lineages=%+v through unavailable current node", operatorLineages)
			}

			adminScope := VisibilityScope{Role: "admin", UserID: 1}
			adminPage, err := service.List(context.Background(), RepositoryListRequest{Limit: 20}, adminScope, RequestContext{})
			if err != nil || len(adminPage.Items) != 1 || adminPage.Items[0].ID != repository.ID {
				t.Fatalf("admin repository visibility=%+v err=%v", adminPage, err)
			}
			adminLineages, err := service.loadLineages(context.Background(), repository.ID, adminScope)
			if err != nil {
				t.Fatal(err)
			}
			if len(adminLineages) != 2 || adminLineages[0].TaskRepositoryLinkID == "" || adminLineages[1].RecoveryPointID != point.ID {
				t.Fatalf("admin provenance lineages=%+v", adminLineages)
			}
		})
	}
}

func deleteVisibilityNodeRetainingOwner(t *testing.T, db *gorm.DB, nodeID uint) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM nodes WHERE id = ?", nodeID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatal(err)
	}
	var owners int64
	if err := db.Model(&model.NodeOwner{}).Where("node_id = ?", nodeID).Count(&owners).Error; err != nil {
		t.Fatal(err)
	}
	if owners == 0 {
		t.Fatal("missing-node fixture did not retain stale ownership evidence")
	}
}

func TestVisibilityRejectsViewerUnknownAndInvalidOperatorScope(t *testing.T) {
	db := newRepositoryTestDB(t)
	service := newVisibilityServiceForTest(t, db, time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC))
	for _, scope := range []VisibilityScope{{Role: "viewer", UserID: 1}, {Role: "", UserID: 1}, {Role: "future", UserID: 1}, {Role: "operator", UserID: 0}} {
		if _, err := service.List(context.Background(), RepositoryListRequest{Limit: 10}, scope, RequestContext{}); !errors.Is(err, backupasset.ErrForbidden) {
			t.Fatalf("scope=%+v error=%v", scope, err)
		}
	}
}

func TestBeginManagedRsyncPointReadRejectsUncommittedPointBeforeReaderAccess(t *testing.T) {
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
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: fixture.service.registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: fixture.admission, History: fixture.service.history, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.BeginManagedRsyncPointRead(context.Background(), fixture.task.ID, attempt.RecoveryPointID)
	if !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("uncommitted managed Rsync reader error=%v, want capability unavailable", err)
	}
	var capabilityErr *CapabilityError
	if !errors.As(err, &capabilityErr) || capabilityErr.Reason.Code != backupasset.CapabilityPointNotCommitted {
		t.Fatalf("uncommitted managed Rsync reader reason=%+v", capabilityErr)
	}
	operations := fixture.admission.operations()
	if len(operations) != 2 || operations[1] != publication.ResticOperation("managed_rsync_point_read") {
		t.Fatalf("managed Rsync reader admission operations=%v", operations)
	}
	if got := fixture.admission.closedCount(); got != 1 {
		t.Fatalf("rejected managed Rsync reader left admission open: closed=%d", got)
	}
}

func TestManagedRsyncCatalogBuildCompletesWithFingerprintNone(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict local provider access is Linux-only")
	}
	fixture := newManagedRsyncCatalogFixture(t)
	if _, err := fixture.keyring.Ensure(context.Background(), backupasset.KeyDomainEntryIdentity); err != nil {
		t.Fatal(err)
	}
	indexer, err := catalog.NewIndexer(catalog.IndexerDependencies{
		DB: fixture.db, Factory: fixture.factory, Lease: fixture.lease, IdentityKeys: fixture.keyring,
		Now:    func() time.Time { return fixture.now },
		Config: catalog.IndexerConfig{BatchSize: 100, BuildTimeout: time.Minute, MaxEntries: 100, HeartbeatInterval: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	generation, err := indexer.Build(context.Background(), catalog.BuildRequest{
		RepositoryID: fixture.repository.ID, RecoveryPointID: fixture.point.ID,
	})
	if err != nil {
		t.Fatalf("build real managed-Rsync Catalog: %v", err)
	}
	if generation.State != string(catalog.GenerationComplete) || !generation.IsActive {
		t.Fatalf("Catalog generation=%+v, want active complete", generation)
	}
	var entry model.CatalogEntry
	if err := fixture.db.Where("generation_id = ?", generation.ID).First(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if entry.Fingerprint != "" || entry.FingerprintStrength != string(catalog.FingerprintNone) {
		t.Fatalf("persisted generic entry=%+v, want empty fingerprint with none strength", entry)
	}
}

type managedRsyncCatalogFixture struct {
	db         *gorm.DB
	factory    catalog.PointReadFactory
	lease      *backupasset.LeaseService
	keyring    *backupasset.Keyring
	now        time.Time
	repository model.BackupRepository
	point      model.RecoveryPoint
}

func newManagedRsyncCatalogFixture(t *testing.T) managedRsyncCatalogFixture {
	t.Helper()
	publicationFixture := newRsyncPublicationFixture(t)
	markerKey, err := publicationFixture.service.rsyncMarkerKey(context.Background(), publicationFixture.repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	managedRoot := filepath.Join(t.TempDir(), "managed-rsync-root")
	bootstrap, err := provider.BootstrapRsyncManagedRoot(context.Background(), provider.RsyncManagedRootBootstrapRequest{
		ManagedRoot: managedRoot, RepositoryID: publicationFixture.repository.ID, MarkerKey: markerKey, CreatedAt: publicationFixture.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	publicationFixture.binding.ManagedRootLocator = managedRoot
	publicationFixture.binding.RootMarkerDigest = bootstrap.RepositoryMarkerDigest
	publicationFixture.binding.ManagedRootIdentityDigest = bootstrap.ManagedRootIdentityDigest
	bindingPayload, err := encodeManagedRsyncBindingDocumentV2(publicationFixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := managedRsyncRepositoryIdentity(publicationFixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	var binding model.RepositoryAccessBinding
	if err := publicationFixture.db.Where("repository_id = ?", publicationFixture.repository.ID).First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	binding.EncryptedConfig = bindingPayload
	binding.UpdatedAt = publicationFixture.now
	if err := publicationFixture.db.Save(&binding).Error; err != nil {
		t.Fatal(err)
	}
	if err := publicationFixture.db.Model(&model.BackupRepository{}).Where("id = ?", publicationFixture.repository.ID).
		Update("repository_identity", identity).Error; err != nil {
		t.Fatal(err)
	}
	publicationFixture.repository.RepositoryIdentity = &identity

	execution, err := publicationFixture.service.Prepare(context.Background(), publicationFixture.run())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) })
	state, ok := execution.(*rsyncPublicationExecution)
	if !ok {
		t.Fatalf("managed Rsync execution type=%T", execution)
	}
	input, err := state.RsyncTreePublicationInput()
	if err != nil {
		t.Fatal(err)
	}
	sourceDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDirectory, "payload.txt"), []byte("managed Catalog payload\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input.Source = provider.RsyncTreeCommandSource{LocalPath: sourceDirectory}
	strategy, err := provider.NewLocalRsyncTreePublicationStrategy(func() time.Time { return publicationFixture.now })
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := strategy.Prepare(context.Background(), provider.PublicationPrepareRequest{
		Attempt: provider.NewRsyncTreePublicationAttempt(state.attempt), RsyncTreeInput: &input,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := strategy.Execute(context.Background(), prepared, provider.PublicationProgress{})
	if err != nil {
		t.Fatal(err)
	}
	commitRecord, err := strategy.RecordCommit(context.Background(), prepared, result)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := commitRecord.RsyncTreeCommit()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRsyncTreeProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	if err := publicationFixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", state.attempt.RecoveryPointID).Updates(map[string]any{
		"state": string(backupasset.RecoveryPointCommitted), "committed_at": publicationFixture.now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := publicationFixture.db.First(&point, "id = ?", state.attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: publicationFixture.db, Foundation: publicationFixture.service.foundation, Registry: publicationFixture.service.registry,
		Keyring: publicationFixture.service.keyring, Now: func() time.Time { return publicationFixture.now },
		Admission: publicationFixture.admission, History: publicationFixture.service.history, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := model.RecoveryPointManifest{
		ID: strings.Repeat("3", 32), RecoveryPointID: point.ID, Revision: 1, DigestAlgorithm: "sha256",
		Digest: point.ManifestDigest, Generator: "rsync-managed-tree", GeneratorVersion: "v1",
		Completeness: string(backupasset.ManifestComplete), EntryCount: point.EntryCount, LogicalBytes: point.LogicalBytes,
		IsActive: true, CreatedAt: publicationFixture.now, UpdatedAt: publicationFixture.now,
	}
	if err := publicationFixture.db.Create(&manifest).Error; err != nil {
		t.Fatal(err)
	}
	return managedRsyncCatalogFixture{
		db: publicationFixture.db, factory: service, lease: publicationFixture.service.lease, keyring: publicationFixture.service.keyring,
		now: publicationFixture.now, repository: publicationFixture.repository, point: point,
	}
}

// The portable resolver boundary deliberately has no caller-supplied Task ID.
// Repository must derive the producing Task from the durable RecoveryPoint.
func TestRsyncResolverDerivesProducingTaskOnly(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)

	var resolver provider.RsyncRestoreSourceResolver = fixture.service
	source, err := resolver.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if err != nil {
		t.Fatalf("resolve durable Rsync scalar ref: %v", err)
	}
	defer func() { _ = source.Close() }()
	if err := source.Revalidate(context.Background()); err != nil {
		t.Fatalf("revalidate derived pinned source: %v", err)
	}

	forged := fixture.ref
	forged.RecoveryPointID = strings.Repeat("f", 32)
	if _, err := resolver.ResolveRsyncRestoreSource(context.Background(), forged); !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("substituted scalar source ref error=%v, want invalid restore request", err)
	}
}

func TestRecoveryRsyncSourceAuthorityFailsClosedWithoutAuthenticatedNamespaceEvidence(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	request := provider.RecoverySourceAuthorityRequest{
		Provider: backupasset.ProviderRsync, RsyncRef: fixture.ref,
	}
	var authority RecoveryRsyncSourceAuthority = fixture.service
	source, observation, err := authority.ObserveRecoverySource(context.Background(), request)
	if source != nil || observation != (RecoveryRsyncSourceAuthorityObservation{}) ||
		!errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("unproved namespace source=%v observation=%+v err=%v, want capability unavailable", source, observation, err)
	}
}

func TestRecoveryRsyncSourceAuthorityReturnsCompleteTransferredSource(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	var transferred *recoveryAuthorityOwnedSource
	source, observation, err := observeRecoveryRsyncSourceWithDependencies(
		context.Background(),
		provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync, RsyncRef: fixture.ref},
		recoveryRsyncSourceAuthorityDependencies{
			resolve: fixture.service.ResolveRsyncRestoreSource,
			capture: fixture.service.captureRecoveryRsyncAuthoritySnapshot,
			observe: func(_ context.Context, request RecoverySourceNamespaceRequest, pinned provider.RsyncRestoreSource) (provider.RsyncRestoreSource, error) {
				if request.SourceRef != fixture.ref || request.ProducingTaskID == 0 ||
					!isLowerHex64(request.RepositoryBindingRevision) || !isLowerHex64(request.ProvenanceRevision) {
					t.Fatalf("incomplete Repository transfer request: %+v", request)
				}
				transferred = &recoveryAuthorityOwnedSource{pinned: pinned}
				return transferred, nil
			},
			revalidate: fixture.service.revalidateRecoveryRsyncAuthoritySnapshot,
		},
	)
	if err != nil || source == nil || transferred == nil || source != transferred ||
		observation.Provider != backupasset.ProviderRsync || observation.RepositoryID != fixture.ref.RepositoryID ||
		!isLowerHex64(observation.RepositoryBindingRevision) || !isLowerHex64(observation.ProvenanceRevision) {
		t.Fatalf("source=%T observation=%+v err=%v", source, observation, err)
	}
	materialized, err := source.MaterializeDeclaredEntries(context.Background(), []provider.RestoreEntry{fixture.entry})
	if err != nil || len(materialized) != 1 || materialized[0].AssetRef != fixture.entry.AssetRef ||
		materialized[0].Type != fixture.entry.Type || materialized[0].ExpectedSize != fixture.entry.ExpectedSize ||
		materialized[0].TargetObjectDigest != fixture.entry.TargetObjectDigest || !isLowerHex64(materialized[0].ExpectedDigest) {
		t.Fatalf("materialized=%+v err=%v", materialized, err)
	}
	if err := source.Revalidate(context.Background()); err != nil {
		t.Fatalf("revalidate complete transferred source: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if transferred.closeCalls.Load() != 1 {
		t.Fatalf("transferred source close calls=%d, want exactly one", transferred.closeCalls.Load())
	}
}

func TestRecoveryRsyncSourceAuthorityTransfersOwnershipThenRevalidates(t *testing.T) {
	ref := provider.RsyncRestoreSourceRef{
		PlanID: strings.Repeat("1", 32), PlanBindingDigest: strings.Repeat("2", 64),
		RepositoryID: strings.Repeat("3", 32), RecoveryPointID: strings.Repeat("4", 32),
		CatalogGenerationID: strings.Repeat("5", 32), SelectionDigest: strings.Repeat("6", 64),
		SourceRevisionDigest: strings.Repeat("7", 64), ManifestDigest: strings.Repeat("8", 64),
	}
	pinned := &recoveryAuthoritySourceFake{}
	closed := &recoveryAuthorityOwnedSource{pinned: pinned}
	phases := make([]string, 0, 4)
	snapshot := recoveryRsyncAuthoritySnapshot{
		producingTaskID:           42,
		repositoryBindingRevision: strings.Repeat("9", 64),
		provenanceRevision:        strings.Repeat("a", 64),
	}

	source, observation, err := observeRecoveryRsyncSourceWithDependencies(
		context.Background(),
		provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync, RsyncRef: ref},
		recoveryRsyncSourceAuthorityDependencies{
			resolve: func(context.Context, provider.RsyncRestoreSourceRef) (provider.RsyncRestoreSource, error) {
				phases = append(phases, "resolve")
				return pinned, nil
			},
			capture: func(context.Context, provider.RsyncRestoreSourceRef) (recoveryRsyncAuthoritySnapshot, error) {
				phases = append(phases, "capture")
				return snapshot, nil
			},
			observe: func(_ context.Context, request RecoverySourceNamespaceRequest, transferred provider.RsyncRestoreSource) (provider.RsyncRestoreSource, error) {
				phases = append(phases, "observe")
				if transferred != pinned || request.SourceRef != ref || request.ProducingTaskID != snapshot.producingTaskID ||
					request.RepositoryBindingRevision != snapshot.repositoryBindingRevision || request.ProvenanceRevision != snapshot.provenanceRevision {
					t.Fatalf("observer request=%+v transferred=%T", request, transferred)
				}
				return closed, nil
			},
			revalidate: func(context.Context, provider.RsyncRestoreSourceRef, recoveryRsyncAuthoritySnapshot) error {
				phases = append(phases, "revalidate")
				return nil
			},
		},
	)
	if err != nil || source != closed || observation.Provider != backupasset.ProviderRsync ||
		observation.RepositoryBindingRevision != snapshot.repositoryBindingRevision || observation.ProvenanceRevision != snapshot.provenanceRevision {
		t.Fatalf("source=%T observation=%+v err=%v", source, observation, err)
	}
	if !reflect.DeepEqual(phases, []string{"resolve", "capture", "observe", "revalidate"}) {
		t.Fatalf("phases=%v", phases)
	}
	if pinned.closeCalls.Load() != 0 || closed.closeCalls.Load() != 0 {
		t.Fatalf("success closed transferred owners early: pinned=%d closed=%d", pinned.closeCalls.Load(), closed.closeCalls.Load())
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if closed.closeCalls.Load() != 1 || pinned.closeCalls.Load() != 1 {
		t.Fatalf("caller close counts observation=%d pinned=%d, want one each", closed.closeCalls.Load(), pinned.closeCalls.Load())
	}
}

func TestRecoveryRsyncSourceAuthorityClosesObservationOnPostObservationDrift(t *testing.T) {
	pinned := &recoveryAuthoritySourceFake{}
	observed := &recoveryAuthorityOwnedSource{pinned: pinned}
	snapshot := recoveryRsyncAuthoritySnapshot{
		producingTaskID:           42,
		repositoryBindingRevision: strings.Repeat("9", 64),
		provenanceRevision:        strings.Repeat("a", 64),
	}
	source, observation, err := observeRecoveryRsyncSourceWithDependencies(
		context.Background(),
		provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync},
		recoveryRsyncSourceAuthorityDependencies{
			resolve: func(context.Context, provider.RsyncRestoreSourceRef) (provider.RsyncRestoreSource, error) {
				return pinned, nil
			},
			capture: func(context.Context, provider.RsyncRestoreSourceRef) (recoveryRsyncAuthoritySnapshot, error) {
				return snapshot, nil
			},
			observe: func(context.Context, RecoverySourceNamespaceRequest, provider.RsyncRestoreSource) (provider.RsyncRestoreSource, error) {
				return observed, nil
			},
			revalidate: func(context.Context, provider.RsyncRestoreSourceRef, recoveryRsyncAuthoritySnapshot) error {
				return errors.New("PRIVATE_POST_OBSERVATION_DRIFT_CANARY")
			},
		},
	)
	if source != nil || observation != (RecoveryRsyncSourceAuthorityObservation{}) || !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("source=%T observation=%+v err=%v", source, observation, err)
	}
	if strings.Contains(fmt.Sprint(err), "PRIVATE_POST_OBSERVATION_DRIFT_CANARY") {
		t.Fatalf("post-observation error leaked: %v", err)
	}
	if pinned.closeCalls.Load() != 1 || observed.closeCalls.Load() != 1 {
		t.Fatalf("drift ownership closes pinned=%d observed=%d, want 1/1", pinned.closeCalls.Load(), observed.closeCalls.Load())
	}
}

func TestRecoveryRsyncSourceAuthorityNilObservationClosesPinnedExactlyOnce(t *testing.T) {
	pinned := &recoveryAuthoritySourceFake{}
	snapshot := recoveryRsyncAuthoritySnapshot{
		producingTaskID:           42,
		repositoryBindingRevision: strings.Repeat("9", 64),
		provenanceRevision:        strings.Repeat("a", 64),
	}
	source, observation, err := observeRecoveryRsyncSourceWithDependencies(
		context.Background(),
		provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync},
		recoveryRsyncSourceAuthorityDependencies{
			resolve: func(context.Context, provider.RsyncRestoreSourceRef) (provider.RsyncRestoreSource, error) {
				return pinned, nil
			},
			capture: func(context.Context, provider.RsyncRestoreSourceRef) (recoveryRsyncAuthoritySnapshot, error) {
				return snapshot, nil
			},
			observe: func(context.Context, RecoverySourceNamespaceRequest, provider.RsyncRestoreSource) (provider.RsyncRestoreSource, error) {
				return nil, nil
			},
			revalidate: func(context.Context, provider.RsyncRestoreSourceRef, recoveryRsyncAuthoritySnapshot) error {
				return nil
			},
		},
	)
	if source != nil || observation != (RecoveryRsyncSourceAuthorityObservation{}) || !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("source=%T observation=%+v err=%v", source, observation, err)
	}
	if pinned.closeCalls.Load() != 1 {
		t.Fatalf("nil observation pinned close calls=%d, want exactly one", pinned.closeCalls.Load())
	}
}

func TestRecoveryRsyncSourceAuthorityRejectsIncompleteTransferredObservation(t *testing.T) {
	pinned := &recoveryAuthoritySourceFake{}
	observed := &recoveryAuthorityOwnedSource{
		pinned: pinned, revalidateErr: errors.New("PRIVATE_INCOMPLETE_OBSERVATION_CANARY"),
	}
	snapshot := recoveryRsyncAuthoritySnapshot{
		producingTaskID:           42,
		repositoryBindingRevision: strings.Repeat("9", 64),
		provenanceRevision:        strings.Repeat("a", 64),
	}
	source, observation, err := observeRecoveryRsyncSourceWithDependencies(
		context.Background(),
		provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync},
		recoveryRsyncSourceAuthorityDependencies{
			resolve: func(context.Context, provider.RsyncRestoreSourceRef) (provider.RsyncRestoreSource, error) {
				return pinned, nil
			},
			capture: func(context.Context, provider.RsyncRestoreSourceRef) (recoveryRsyncAuthoritySnapshot, error) {
				return snapshot, nil
			},
			observe: func(context.Context, RecoverySourceNamespaceRequest, provider.RsyncRestoreSource) (provider.RsyncRestoreSource, error) {
				return observed, nil
			},
			revalidate: func(context.Context, provider.RsyncRestoreSourceRef, recoveryRsyncAuthoritySnapshot) error {
				return nil
			},
		},
	)
	if source != nil || observation != (RecoveryRsyncSourceAuthorityObservation{}) || !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("source=%T observation=%+v err=%v", source, observation, err)
	}
	if strings.Contains(fmt.Sprint(err), "PRIVATE_INCOMPLETE_OBSERVATION_CANARY") {
		t.Fatalf("incomplete observation leaked: %v", err)
	}
	if observed.closeCalls.Load() != 1 || pinned.closeCalls.Load() != 1 {
		t.Fatalf("incomplete observation closes observed=%d pinned=%d, want one each", observed.closeCalls.Load(), pinned.closeCalls.Load())
	}
}

func TestRecoveryRsyncSourceAuthorityRejectsCapabilityDrift(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	var point model.RecoveryPoint
	if err := fixture.db.Where("id = ?", fixture.ref.RecoveryPointID).First(&point).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.ref.RecoveryPointID).
		Update("capability_revision", point.CapabilityRevision+1).Error; err != nil {
		t.Fatal(err)
	}
	source, _, err := fixture.service.ObserveRecoverySource(context.Background(), provider.RecoverySourceAuthorityRequest{
		Provider: backupasset.ProviderRsync, RsyncRef: fixture.ref,
	})
	if source != nil {
		_ = source.Close()
	}
	if !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("capability drift error=%v, want invalid restore request", err)
	}
}

func TestRecoveryRsyncSourceAuthorityRevalidatesClosedObservationInCallerTransaction(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	snapshot, err := fixture.service.captureRecoveryRsyncAuthoritySnapshot(context.Background(), fixture.ref)
	if err != nil {
		t.Fatalf("capture source authority: %v", err)
	}
	expected := snapshot.observation
	expected.Provider = backupasset.ProviderRsync
	expected.RepositoryID = fixture.ref.RepositoryID
	expected.RecoveryPointID = fixture.ref.RecoveryPointID
	expected.CatalogGenerationID = fixture.ref.CatalogGenerationID
	expected.SourceRevisionDigest = fixture.ref.SourceRevisionDigest
	expected.ManifestDigest = fixture.ref.ManifestDigest
	expected.RepositoryBindingRevision = snapshot.repositoryBindingRevision
	expected.ProvenanceRevision = snapshot.provenanceRevision
	request := provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync, RsyncRef: fixture.ref}

	err = fixture.db.Transaction(func(tx *gorm.DB) error {
		return fixture.service.RevalidateRecoverySourceAuthorityTx(context.Background(), tx, request, expected)
	})
	if err != nil {
		t.Fatalf("revalidate exact closed observation: %v", err)
	}
	for _, mutate := range []func(*RecoveryRsyncSourceAuthorityObservation){
		func(value *RecoveryRsyncSourceAuthorityObservation) { value.CapabilityRevision++ },
		func(value *RecoveryRsyncSourceAuthorityObservation) {
			value.RepositoryBindingRevision = strings.Repeat("0", 64)
		},
		func(value *RecoveryRsyncSourceAuthorityObservation) {
			value.ProvenanceRevision = strings.Repeat("1", 64)
		},
	} {
		candidate := expected
		mutate(&candidate)
		err = fixture.db.Transaction(func(tx *gorm.DB) error {
			return fixture.service.RevalidateRecoverySourceAuthorityTx(context.Background(), tx, request, candidate)
		})
		if !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("substituted observation error=%v, want conflict", err)
		}
	}
}

func TestRecoveryRsyncSourceAuthorityRevalidatesCompleteLockedBindingAndProvenanceRows(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	tests := []struct {
		name   string
		mutate func(*testing.T, *rsyncRestoreResolverFixture)
	}{
		{
			name: "binding plaintext with stable config fingerprint",
			mutate: func(t *testing.T, fixture *rsyncRestoreResolverFixture) {
				var binding model.RepositoryAccessBinding
				if err := fixture.db.Where("repository_id = ? AND status = ?", fixture.ref.RepositoryID, bindingStatusActive).First(&binding).Error; err != nil {
					t.Fatal(err)
				}
				fingerprint := binding.ConfigFingerprint
				updatedAt := binding.UpdatedAt
				stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
				if err != nil || stored.ManagedRsyncV2 == nil {
					t.Fatalf("decode binding err=%v stored=%+v", err, stored)
				}
				document := *stored.ManagedRsyncV2
				document.PreflightDigest = strings.Repeat("d", 64)
				payload, err := encodeManagedRsyncBindingDocumentV2(document)
				if err != nil {
					t.Fatal(err)
				}
				binding.EncryptedConfig = payload
				if err := fixture.db.Save(&binding).Error; err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.Model(&model.RepositoryAccessBinding{}).Where("id = ?", binding.ID).
					UpdateColumn("updated_at", updatedAt).Error; err != nil {
					t.Fatal(err)
				}
				var current model.RepositoryAccessBinding
				if err := fixture.db.Where("id = ?", binding.ID).First(&current).Error; err != nil {
					t.Fatal(err)
				}
				if current.ConfigFingerprint != fingerprint || !current.UpdatedAt.Equal(updatedAt) {
					t.Fatalf("fixture changed non-plaintext authority: fingerprint=%q updated_at=%s", current.ConfigFingerprint, current.UpdatedAt)
				}
			},
		},
		{
			name: "point lineage",
			mutate: func(t *testing.T, fixture *rsyncRestoreResolverFixture) {
				var point model.RecoveryPoint
				if err := fixture.db.Where("id = ?", fixture.ref.RecoveryPointID).First(&point).Error; err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.ref.RecoveryPointID).
					Update("lineage_json", `{"revision":"PRIVATE_POINT_LINEAGE_CANARY"}`).Error; err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.ref.RecoveryPointID).
					UpdateColumn("updated_at", point.UpdatedAt).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newRsyncRestoreResolverFixture(t)
			var transferred *recoveryAuthorityOwnedSource
			source, observation, err := observeRecoveryRsyncSourceWithDependencies(
				context.Background(),
				provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync, RsyncRef: fixture.ref},
				recoveryRsyncSourceAuthorityDependencies{
					resolve: fixture.service.ResolveRsyncRestoreSource,
					capture: fixture.service.captureRecoveryRsyncAuthoritySnapshot,
					observe: func(_ context.Context, request RecoverySourceNamespaceRequest, pinned provider.RsyncRestoreSource) (provider.RsyncRestoreSource, error) {
						if request.ProducingTaskID == 0 || request.RepositoryBindingRevision == "" || request.ProvenanceRevision == "" ||
							request.RepositoryBindingRevision == request.ProvenanceRevision {
							t.Fatalf("incomplete namespace request: %+v", request)
						}
						testCase.mutate(t, fixture)
						transferred = &recoveryAuthorityOwnedSource{pinned: pinned}
						return transferred, nil
					},
					revalidate: fixture.service.revalidateRecoveryRsyncAuthoritySnapshot,
				},
			)
			if source != nil || observation != (RecoveryRsyncSourceAuthorityObservation{}) || !errors.Is(err, provider.ErrInvalidRestoreRequest) {
				t.Fatalf("source=%T observation=%+v err=%v", source, observation, err)
			}
			if transferred == nil || transferred.closeCalls.Load() != 1 {
				t.Fatalf("post-observation drift close calls=%v, want exactly one", transferred)
			}
			for _, canary := range []string{"PRIVATE_POINT_LINEAGE_CANARY", fixture.root, fixture.taskSource} {
				if strings.Contains(fmt.Sprint(err), canary) {
					t.Fatalf("post-observation drift leaked %q: %v", canary, err)
				}
			}
		})
	}
}

func TestRecoveryRsyncSourceAuthoritySQLiteRevalidationExcludesConcurrentWriter(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	expected, err := fixture.service.captureRecoveryRsyncAuthoritySnapshot(context.Background(), fixture.ref)
	if err != nil {
		t.Fatalf("capture Recovery source authority snapshot: %v", err)
	}

	lockReady := make(chan error, 1)
	releaseLock := make(chan struct{})
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- fixture.db.Transaction(func(tx *gorm.DB) error {
			current, loadErr := fixture.service.loadRecoveryRsyncAuthoritySnapshotTx(
				context.Background(), tx, fixture.ref,
			)
			if loadErr == nil && !reflect.DeepEqual(current, expected) {
				loadErr = errors.New("Recovery source authority snapshot changed before lock test")
			}
			lockReady <- loadErr
			if loadErr != nil {
				return loadErr
			}
			<-releaseLock
			return nil
		})
	}()
	if lockErr := <-lockReady; lockErr != nil {
		close(releaseLock)
		<-lockDone
		t.Fatalf("hold Repository revalidation transaction: %v", lockErr)
	}
	concurrentWriteErr := fixture.db.Model(&model.RecoveryPoint{}).
		Where("id = ?", fixture.ref.RecoveryPointID).
		Update("lineage_json", `{"revision":"concurrent-writer"}`).Error
	close(releaseLock)
	if lockErr := <-lockDone; lockErr != nil {
		t.Fatalf("finish Repository revalidation transaction: %v", lockErr)
	}
	if concurrentWriteErr == nil {
		t.Fatal("concurrent RecoveryPoint writer committed while Repository revalidation was active")
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).
		Where("id = ?", fixture.ref.RecoveryPointID).
		Update("lineage_json", `{"revision":"after-revalidation"}`).Error; err != nil {
		t.Fatalf("RecoveryPoint writer remained blocked after Repository revalidation committed: %v", err)
	}
}

func TestRecoveryRsyncSourceAuthorityUnsupportedProvidersAreUnavailableBeforeAccess(t *testing.T) {
	for _, kind := range []backupasset.ProviderKind{backupasset.ProviderRestic, backupasset.ProviderRclone} {
		t.Run(string(kind), func(t *testing.T) {
			source, observation, err := (*Service)(nil).ObserveRecoverySource(context.Background(), provider.RecoverySourceAuthorityRequest{
				Provider: kind,
			})
			if source != nil || observation != (RecoveryRsyncSourceAuthorityObservation{}) ||
				!errors.Is(err, backupasset.ErrCapabilityUnavailable) {
				t.Fatalf("unsupported provider source=%v observation=%+v err=%v", source, observation, err)
			}
		})
	}
}

func TestRecoveryRsyncSourceAuthorityProductsRedactPrivateEvidence(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	observation := RecoveryRsyncSourceAuthorityObservation{
		Provider: backupasset.ProviderRsync, RepositoryID: fixture.ref.RepositoryID,
		RecoveryPointID: fixture.ref.RecoveryPointID, CatalogGenerationID: fixture.ref.CatalogGenerationID,
		SourceRevisionDigest: fixture.ref.SourceRevisionDigest, ManifestDigest: fixture.ref.ManifestDigest,
		SourceAccessIdentity:      "PRIVATE_SOURCE_ACCESS_IDENTITY_CANARY",
		SourceFingerprint:         fixture.sourceFingerprint,
		ManagedRootIdentity:       "PRIVATE_MANAGED_ROOT_IDENTITY_CANARY",
		RepositoryBindingRevision: "PRIVATE_REPOSITORY_BINDING_REVISION_CANARY",
		ProvenanceRevision:        "PRIVATE_PROVENANCE_REVISION_CANARY",
	}
	sourceJSON, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	observationJSON, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	formatted := []string{
		string(sourceJSON), string(observationJSON), fmt.Sprintf("%v", source), fmt.Sprintf("%+v", source), fmt.Sprintf("%#v", source),
		fmt.Sprintf("%v", observation), fmt.Sprintf("%+v", observation), fmt.Sprintf("%#v", observation),
	}
	for _, output := range formatted {
		for _, canary := range []string{
			fixture.root, fixture.taskSource, fixture.ref.PlanID, fixture.ref.ManifestDigest,
			fixture.sourceFingerprint, observation.SourceAccessIdentity, observation.ManagedRootIdentity,
			observation.RepositoryBindingRevision, observation.ProvenanceRevision,
		} {
			if strings.Contains(output, canary) {
				t.Fatalf("private source authority product leaked %q in %q", canary, output)
			}
		}
	}
}

func TestRecoveryRsyncSourceAuthorityRevalidatesAndClosesBeforeFailingUnavailable(t *testing.T) {
	source := &recoveryAuthoritySourceFake{}
	request := provider.RecoverySourceAuthorityRequest{
		Provider: backupasset.ProviderRsync,
		RsyncRef: provider.RsyncRestoreSourceRef{
			PlanID: strings.Repeat("1", 32), PlanBindingDigest: strings.Repeat("2", 64),
			RepositoryID: strings.Repeat("3", 32), RecoveryPointID: strings.Repeat("4", 32),
			CatalogGenerationID: strings.Repeat("5", 32), SelectionDigest: strings.Repeat("6", 64),
			SourceRevisionDigest: strings.Repeat("7", 64), ManifestDigest: strings.Repeat("8", 64),
		},
	}
	resolved, observation, err := observeRecoveryRsyncSourceWithResolver(
		context.Background(), request,
		func(context.Context, provider.RsyncRestoreSourceRef) (provider.RsyncRestoreSource, error) {
			return source, nil
		},
	)
	if resolved != nil || observation != (RecoveryRsyncSourceAuthorityObservation{}) ||
		!errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("unproved namespace source=%v observation=%+v err=%v, want capability unavailable", resolved, observation, err)
	}
	if source.revalidateCalls.Load() != 1 || source.closeCalls.Load() != 1 {
		t.Fatalf("source lifecycle revalidate=%d close=%d, want exactly once", source.revalidateCalls.Load(), source.closeCalls.Load())
	}
}

func TestRecoveryRsyncSourceAuthoritySanitizesResolverFailureAndClosesReturnedSource(t *testing.T) {
	const privateFailure = "PRIVATE_SOURCE_RESOLVER_FAILURE_CANARY"
	source := &recoveryAuthoritySourceFake{}
	resolved, observation, err := observeRecoveryRsyncSourceWithResolver(
		context.Background(),
		provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync},
		func(context.Context, provider.RsyncRestoreSourceRef) (provider.RsyncRestoreSource, error) {
			return source, errors.New(privateFailure)
		},
	)
	if resolved != nil || observation != (RecoveryRsyncSourceAuthorityObservation{}) ||
		!errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("resolver failure source=%v observation=%+v err=%v, want invalid restore request", resolved, observation, err)
	}
	if strings.Contains(fmt.Sprint(err), privateFailure) {
		t.Fatalf("resolver failure leaked private dependency error: %v", err)
	}
	if source.closeCalls.Load() != 1 {
		t.Fatalf("resolver failure close calls=%d, want exactly once", source.closeCalls.Load())
	}
}

func TestRecoveryRsyncSourceAuthorityObservesOutsideTransactionsAndClosesEveryPath(t *testing.T) {
	ref := provider.RsyncRestoreSourceRef{
		PlanID: strings.Repeat("1", 32), PlanBindingDigest: strings.Repeat("2", 64),
		RepositoryID: strings.Repeat("3", 32), RecoveryPointID: strings.Repeat("4", 32),
		CatalogGenerationID: strings.Repeat("5", 32), SelectionDigest: strings.Repeat("6", 64),
		SourceRevisionDigest: strings.Repeat("7", 64), ManifestDigest: strings.Repeat("8", 64),
	}
	restoreEntry := provider.RestoreEntry{
		AssetRef: backupasset.AssetRef{RecoveryPointID: ref.RecoveryPointID, EntryID: strings.Repeat("9", 64)},
		Type:     backupasset.CatalogEntryFile, ExpectedSize: 1, ExpectedDigest: strings.Repeat("a", 64),
		TargetObjectDigest: strings.Repeat("b", 64),
	}
	strictEntry := fileaccess.Entry{Name: "payload", Type: fileaccess.EntryFile, Size: 1, SourceRevision: "source-revision"}
	snapshot := rsyncRestoreDurableSnapshot{
		declared: []rsyncRestoreDeclaredEntry{{restore: restoreEntry, name: strictEntry.Name}},
	}

	const privateFailure = "PRIVATE_RESOLVER_FAILURE_CANARY"
	tests := []struct {
		name              string
		observeErr        error
		revalidateErr     error
		treeRevalidateErr error
		wantSuccess       bool
	}{
		{name: "filesystem observation returned capability with error", observeErr: errors.New(privateFailure)},
		{name: "locked snapshot revalidation or commit failed", revalidateErr: errors.New(privateFailure)},
		{name: "pinned tree revalidation failed", treeRevalidateErr: errors.New(privateFailure)},
		{name: "success transfers ownership", wantSuccess: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			phases := make([]string, 0, 3)
			tree := &recoveryAuthorityPinnedTree{revalidateErr: testCase.treeRevalidateErr}
			dependencies := rsyncRestoreSourceResolverDependencies{
				loadSnapshot: func(context.Context, provider.RsyncRestoreSourceRef) (rsyncRestoreDurableSnapshot, error) {
					phases = append(phases, "durable_snapshot_committed")
					return snapshot, nil
				},
				observeFilesystem: func(context.Context, rsyncRestoreDurableSnapshot) (fileaccess.PinnedStrictTree, map[provider.RestoreEntry]fileaccess.Entry, []provider.RestoreEntry, error) {
					phases = append(phases, "filesystem_observation")
					return tree, map[provider.RestoreEntry]fileaccess.Entry{restoreEntry: strictEntry}, []provider.RestoreEntry{restoreEntry}, testCase.observeErr
				},
				revalidateSnapshot: func(context.Context, provider.RsyncRestoreSourceRef, rsyncRestoreDurableSnapshot, map[provider.RestoreEntry]fileaccess.Entry) error {
					phases = append(phases, "locked_revalidation_committed")
					return testCase.revalidateErr
				},
				revalidateSource: func(context.Context, provider.RsyncRestoreSourceRef, rsyncRestoreDurableSnapshot, map[provider.RestoreEntry]fileaccess.Entry) error {
					return nil
				},
			}
			source, err := resolveRsyncRestoreSourceWithDependencies(context.Background(), ref, dependencies)
			if testCase.wantSuccess {
				if err != nil || source == nil {
					t.Fatalf("resolve source=%v err=%v, want transferred ownership", source, err)
				}
				if !reflect.DeepEqual(phases, []string{"durable_snapshot_committed", "filesystem_observation", "locked_revalidation_committed"}) {
					t.Fatalf("authority phases=%v, want snapshot -> filesystem -> locked revalidation", phases)
				}
				if tree.closeCalls.Load() != 0 {
					t.Fatalf("successful source close calls=%d before transfer owner close, want 0", tree.closeCalls.Load())
				}
				if closeErr := source.Close(); closeErr != nil {
					t.Fatal(closeErr)
				}
				if closeErr := source.Close(); closeErr != nil {
					t.Fatal(closeErr)
				}
				if tree.closeCalls.Load() != 1 {
					t.Fatalf("successful source close calls=%d, want exactly 1", tree.closeCalls.Load())
				}
				return
			}
			if source != nil || !errors.Is(err, provider.ErrInvalidRestoreRequest) {
				t.Fatalf("failed resolution source=%v err=%v, want invalid restore request", source, err)
			}
			if strings.Contains(fmt.Sprint(err), privateFailure) {
				t.Fatalf("failed resolution leaked private dependency error: %v", err)
			}
			if tree.closeCalls.Load() != 1 {
				t.Fatalf("failed resolution close calls=%d, want exactly 1", tree.closeCalls.Load())
			}
		})
	}
}

type recoveryAuthorityPinnedTree struct {
	revalidateErr error
	closeCalls    atomic.Int64
}

type recoveryAuthoritySourceFake struct {
	revalidateCalls atomic.Int64
	closeCalls      atomic.Int64
}

type recoveryAuthorityOwnedSource struct {
	pinned        provider.RsyncRestoreSource
	revalidateErr error
	closeOnce     sync.Once
	closeCalls    atomic.Int64
	closeErr      error
}

func (source *recoveryAuthorityOwnedSource) OpenDeclaredRegular(ctx context.Context, entry provider.RestoreEntry) (provider.RsyncRestoreSourceStream, error) {
	return source.pinned.OpenDeclaredRegular(ctx, entry)
}

func (source *recoveryAuthorityOwnedSource) MaterializeDeclaredEntries(ctx context.Context, entries []provider.RestoreEntry) ([]provider.RestoreEntry, error) {
	return source.pinned.MaterializeDeclaredEntries(ctx, entries)
}

func (source *recoveryAuthorityOwnedSource) Revalidate(ctx context.Context) error {
	if source.revalidateErr != nil {
		return source.revalidateErr
	}
	return source.pinned.Revalidate(ctx)
}

func (source *recoveryAuthorityOwnedSource) Close() error {
	source.closeOnce.Do(func() {
		source.closeCalls.Add(1)
		source.closeErr = source.pinned.Close()
	})
	return source.closeErr
}

func (*recoveryAuthoritySourceFake) OpenDeclaredRegular(context.Context, provider.RestoreEntry) (provider.RsyncRestoreSourceStream, error) {
	return nil, provider.ErrRsyncRestoreSourceDrift
}

func (*recoveryAuthoritySourceFake) MaterializeDeclaredEntries(context.Context, []provider.RestoreEntry) ([]provider.RestoreEntry, error) {
	return nil, provider.ErrRsyncRestoreSourceDrift
}

func (source *recoveryAuthoritySourceFake) Revalidate(context.Context) error {
	source.revalidateCalls.Add(1)
	return nil
}

func (source *recoveryAuthoritySourceFake) Close() error {
	source.closeCalls.Add(1)
	return nil
}

func (*recoveryAuthorityPinnedTree) OpenDeclaredRegular(context.Context, fileaccess.Entry) (fileaccess.ReadHandle, fileaccess.ContentStat, error) {
	return nil, fileaccess.ContentStat{}, fileaccess.ErrSourceChanged
}

func (tree *recoveryAuthorityPinnedTree) Revalidate(context.Context) error {
	return tree.revalidateErr
}

func (tree *recoveryAuthorityPinnedTree) Close() error {
	tree.closeCalls.Add(1)
	return nil
}

func TestRsyncResolverRejectsPlanSelectionCatalogRevisionAndCiphertextDrift(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	tests := []struct {
		name   string
		mutate func(*testing.T, *rsyncRestoreResolverFixture, *provider.RsyncRestoreSourceRef)
	}{
		{name: "plan binding", mutate: func(_ *testing.T, _ *rsyncRestoreResolverFixture, ref *provider.RsyncRestoreSourceRef) {
			ref.PlanBindingDigest = strings.Repeat("e", 64)
		}},
		{name: "selection", mutate: func(_ *testing.T, _ *rsyncRestoreResolverFixture, ref *provider.RsyncRestoreSourceRef) {
			ref.SelectionDigest = strings.Repeat("e", 64)
		}},
		{name: "catalog", mutate: func(_ *testing.T, _ *rsyncRestoreResolverFixture, ref *provider.RsyncRestoreSourceRef) {
			ref.CatalogGenerationID = strings.Repeat("e", 32)
		}},
		{name: "source revision", mutate: func(_ *testing.T, _ *rsyncRestoreResolverFixture, ref *provider.RsyncRestoreSourceRef) {
			ref.SourceRevisionDigest = strings.Repeat("e", 64)
		}},
		{name: "repository capability revision", mutate: func(t *testing.T, fixture *rsyncRestoreResolverFixture, _ *provider.RsyncRestoreSourceRef) {
			if err := fixture.db.Model(&model.BackupRepository{}).
				Where("id = ?", fixture.ref.RepositoryID).
				Update("capability_revision", gorm.Expr("capability_revision + 1")).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "mutable source revision kind", mutate: func(t *testing.T, fixture *rsyncRestoreResolverFixture, _ *provider.RsyncRestoreSourceRef) {
			if err := fixture.db.Model(&model.BackupAssetRecoveryPlan{}).
				Where("id = ?", fixture.ref.PlanID).
				Update("source_revision_kind", "observation").Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "mutable plan item fingerprint", mutate: func(t *testing.T, fixture *rsyncRestoreResolverFixture, _ *provider.RsyncRestoreSourceRef) {
			if err := fixture.db.Model(&model.BackupAssetRecoveryPlanItem{}).
				Where("plan_id = ?", fixture.ref.PlanID).
				Update("source_fingerprint", strings.Repeat("e", 64)).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "manifest", mutate: func(_ *testing.T, _ *rsyncRestoreResolverFixture, ref *provider.RsyncRestoreSourceRef) {
			ref.ManifestDigest = strings.Repeat("e", 64)
		}},
		{name: "encrypted plan source", mutate: func(t *testing.T, fixture *rsyncRestoreResolverFixture, _ *provider.RsyncRestoreSourceRef) {
			var plan model.BackupAssetRecoveryPlan
			if err := fixture.db.First(&plan, "id = ?", fixture.ref.PlanID).Error; err != nil {
				t.Fatal(err)
			}
			plan.EncryptedSourceLocator = "FAKE_REPLACED_RSYNC_POINT_LOCATOR_FOR_TEST_ONLY"
			if err := fixture.db.Save(&plan).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "matching encrypted point and plan source", mutate: func(t *testing.T, fixture *rsyncRestoreResolverFixture, _ *provider.RsyncRestoreSourceRef) {
			const replacement = "FAKE_REPLACED_RSYNC_POINT_LOCATOR_FOR_TEST_ONLY"
			var plan model.BackupAssetRecoveryPlan
			if err := fixture.db.First(&plan, "id = ?", fixture.ref.PlanID).Error; err != nil {
				t.Fatal(err)
			}
			var point model.RecoveryPoint
			if err := fixture.db.First(&point, "id = ?", fixture.ref.RecoveryPointID).Error; err != nil {
				t.Fatal(err)
			}
			plan.EncryptedSourceLocator = replacement
			point.EncryptedProviderLocator = replacement
			if err := fixture.db.Save(&plan).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Save(&point).Error; err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRsyncRestoreResolverFixture(t)
			candidate := fixture.ref
			test.mutate(t, fixture, &candidate)
			if source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), candidate); !errors.Is(err, provider.ErrInvalidRestoreRequest) {
				if source != nil {
					_ = source.Close()
				}
				t.Fatalf("substituted Rsync source error=%v, want invalid restore request", err)
			}
		})
	}
}

func TestRsyncResolverRequiresExactDurablePlanItems(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	if err := fixture.db.Where("plan_id = ?", fixture.ref.PlanID).Delete(&model.BackupAssetRecoveryPlanItem{}).Error; err != nil {
		t.Fatal(err)
	}

	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if source != nil {
		_ = source.Close()
	}
	if !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("resolve without durable plan items error=%v, want invalid restore request", err)
	}
}

func TestRsyncResolverRejectsMissingSelectedCatalogEntry(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	if err := fixture.db.Where("generation_id = ?", fixture.ref.CatalogGenerationID).Delete(&model.CatalogEntry{}).Error; err != nil {
		t.Fatal(err)
	}

	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if source != nil {
		_ = source.Close()
	}
	if !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("resolve without selected catalog entry error=%v, want invalid restore request", err)
	}
}

func TestRsyncResolverRejectsImmutableLocatorDigestDrift(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	if err := fixture.db.Model(&model.BackupAssetRecoveryPlan{}).
		Where("id = ?", fixture.ref.PlanID).
		Update("immutable_locator_digest", strings.Repeat("8", 64)).Error; err != nil {
		t.Fatal(err)
	}

	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if source != nil {
		_ = source.Close()
	}
	if !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("resolve with stale immutable locator digest error=%v, want invalid restore request", err)
	}
}

type rsyncRestoreResolverFixture struct {
	service           *Service
	db                *gorm.DB
	ref               provider.RsyncRestoreSourceRef
	entry             provider.RestoreEntry
	content           string
	root              string
	taskSource        string
	sourceFingerprint string
}

func newRsyncRestoreResolverFixture(t *testing.T) *rsyncRestoreResolverFixture {
	return newRsyncRestoreResolverFixtureWithAlternate(t, false)
}

func newRsyncRestoreResolverFixtureWithAlternate(t *testing.T, alternate bool) *rsyncRestoreResolverFixture {
	t.Helper()
	fixture := newRsyncPublicationFixture(t)
	markerKey, err := fixture.service.rsyncMarkerKey(context.Background(), fixture.repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	managedRoot := filepath.Join(t.TempDir(), "managed-rsync-root")
	bootstrap, err := provider.BootstrapRsyncManagedRoot(context.Background(), provider.RsyncManagedRootBootstrapRequest{
		ManagedRoot: managedRoot, RepositoryID: fixture.repository.ID, MarkerKey: markerKey, CreatedAt: fixture.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.binding.ManagedRootLocator = managedRoot
	fixture.binding.RootMarkerDigest = bootstrap.RepositoryMarkerDigest
	fixture.binding.ManagedRootIdentityDigest = bootstrap.ManagedRootIdentityDigest
	bindingPayload, err := encodeManagedRsyncBindingDocumentV2(fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := managedRsyncRepositoryIdentity(fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	var storedBinding model.RepositoryAccessBinding
	if err := fixture.db.Where("repository_id = ?", fixture.repository.ID).First(&storedBinding).Error; err != nil {
		t.Fatal(err)
	}
	storedBinding.EncryptedConfig = bindingPayload
	storedBinding.UpdatedAt = fixture.now
	if err := fixture.db.Save(&storedBinding).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupRepository{}).Where("id = ?", fixture.repository.ID).Update("repository_identity", identity).Error; err != nil {
		t.Fatal(err)
	}
	fixture.repository.RepositoryIdentity = &identity

	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) })
	state, ok := execution.(*rsyncPublicationExecution)
	if !ok {
		t.Fatalf("managed Rsync execution type=%T", execution)
	}
	input, err := state.RsyncTreePublicationInput()
	if err != nil {
		t.Fatal(err)
	}
	sourceDirectory := t.TempDir()
	fixture.task.RsyncSource = sourceDirectory
	if err := fixture.db.Model(&model.Task{}).Where("id = ?", fixture.task.ID).Update("rsync_source", sourceDirectory).Error; err != nil {
		t.Fatal(err)
	}
	const sourceName = "payload.txt"
	const sourceContents = "managed restore payload\n"
	if err := os.WriteFile(filepath.Join(sourceDirectory, sourceName), []byte(sourceContents), 0o600); err != nil {
		t.Fatal(err)
	}
	if alternate {
		alternateDirectory := filepath.Join(sourceDirectory, "alternate")
		if err := os.Mkdir(alternateDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(alternateDirectory, sourceName), []byte(sourceContents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	input.Source = provider.RsyncTreeCommandSource{LocalPath: sourceDirectory}
	strategy, err := provider.NewLocalRsyncTreePublicationStrategy(func() time.Time { return fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := strategy.Prepare(context.Background(), provider.PublicationPrepareRequest{
		Attempt: provider.NewRsyncTreePublicationAttempt(state.attempt), RsyncTreeInput: &input,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := strategy.Execute(context.Background(), prepared, provider.PublicationProgress{})
	if err != nil {
		t.Fatal(err)
	}
	commitRecord, err := strategy.RecordCommit(context.Background(), prepared, result)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := commitRecord.RsyncTreeCommit()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRsyncTreeProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", state.attempt.RecoveryPointID).Updates(map[string]any{
		"state": string(backupasset.RecoveryPointCommitted), "committed_at": fixture.now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.AutoMigrate(&model.BackupAssetRecoveryPlan{}, &model.BackupAssetRecoveryPlanItem{}); err != nil {
		t.Fatal(err)
	}

	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", state.attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	manifest := model.RecoveryPointManifest{
		ID: strings.Repeat("3", 32), RecoveryPointID: point.ID, Revision: 1, DigestAlgorithm: "sha256",
		Digest: point.ManifestDigest, Generator: "rsync-managed-tree", GeneratorVersion: "v1",
		Completeness: string(backupasset.ManifestComplete), EntryCount: point.EntryCount, LogicalBytes: point.LogicalBytes,
		IsActive: true, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&manifest).Error; err != nil {
		t.Fatal(err)
	}
	immutableLocatorDigest, err := publication.ImmutableLocatorDigest(
		fixture.repository.ID, backupasset.ProviderRsync, point.ID, point.EncryptedProviderLocator,
	)
	if err != nil {
		t.Fatal(err)
	}
	finishedAt := fixture.now
	generation := model.CatalogGeneration{
		ID: strings.Repeat("4", 32), RecoveryPointID: point.ID, Generation: 1, State: string(catalog.GenerationComplete), IsActive: true,
		ManifestID:        &manifest.ID,
		SourceFingerprint: point.SourceFingerprint, ExpectedEntryCount: point.EntryCount, WrittenEntryCount: point.EntryCount,
		ExpectedDigest: point.ManifestDigest, WrittenDigest: strings.Repeat("9", 64),
		StartedAt: fixture.now.Add(-time.Minute), FinishedAt: &finishedAt, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&generation).Error; err != nil {
		t.Fatal(err)
	}
	plan := model.BackupAssetRecoveryPlan{
		ID:                       strings.Repeat("5", 32),
		RequesterID:              1,
		Endpoint:                 "recovery_execute",
		IdempotencyKeyDigest:     strings.Repeat("6", 64),
		RepositoryID:             fixture.repository.ID,
		RecoveryPointID:          point.ID,
		SourceRevisionDigest:     strings.Repeat("7", 64),
		SourceRevisionKind:       "immutable",
		ImmutableLocatorDigest:   immutableLocatorDigest,
		ImmutableManifestDigest:  point.ManifestDigest,
		CatalogGenerationID:      generation.ID,
		EncryptedSourceLocator:   point.EncryptedProviderLocator,
		TargetMode:               "isolated",
		TargetNodeID:             fixture.task.NodeID,
		TargetRootID:             strings.Repeat("9", 32),
		RootLocatorDigest:        strings.Repeat("a", 64),
		PathDigest:               strings.Repeat("b", 64),
		TargetBaseRevision:       "node-revision",
		CredentialScopeRevision:  "credential-revision",
		RootRevision:             "root-revision",
		FilesystemRevision:       "filesystem-revision",
		SelectionDigest:          strings.Repeat("c", 64),
		BindingDigest:            strings.Repeat("d", 64),
		CapabilityRevision:       "capability-revision",
		ConflictPolicy:           "fail_on_conflict",
		OperationSetDigest:       strings.Repeat("e", 64),
		DeleteSetDigest:          strings.Repeat("f", 64),
		SecurityDecision:         "clean",
		SecurityDecisionDigest:   strings.Repeat("1", 64),
		SecurityFindingSetDigest: strings.Repeat("2", 64),
		SecurityPolicyRevision:   "security-revision",
		PreflightRevision:        "preflight-revision",
		PreflightExpiresAt:       fixture.now.Add(time.Hour),
		State:                    "draft",
		TransitionRevision:       1,
		CreatedAt:                fixture.now,
		UpdatedAt:                fixture.now,
	}
	if err := fixture.db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	entry := model.CatalogEntry{
		GenerationID: generation.ID, EntryID: strings.Repeat("f", 64), RecoveryPointID: point.ID,
		NormalizedPath: sourceName, Name: sourceName, EntryType: string(backupasset.CatalogEntryFile), Size: int64(len(sourceContents)),
		FingerprintStrength:      string(catalog.FingerprintNone),
		EncryptedProviderLocator: `{"version":1,"native":"` + sourceName + `"}`, SecurityState: "sealed", CreatedAt: fixture.now,
	}
	if err := fixture.db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	item := model.BackupAssetRecoveryPlanItem{
		ID: strings.Repeat("b", 32), PlanID: plan.ID, Ordinal: 0, RecoveryPointID: point.ID, CatalogGenerationID: generation.ID,
		EntryID: entry.EntryID, EntryType: entry.EntryType, RelativePathDigest: publication.RecoveryPlanItemPathDigest(
			plan.RepositoryID, point.ID, generation.ID, entry.EntryID, entry.NormalizedPath,
		), CreatedAt: fixture.now,
	}
	if err := fixture.db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: fixture.service.registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: fixture.admission, History: fixture.service.history, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &rsyncRestoreResolverFixture{service: service, db: fixture.db, ref: provider.RsyncRestoreSourceRef{
		PlanID:               plan.ID,
		PlanBindingDigest:    plan.BindingDigest,
		RepositoryID:         plan.RepositoryID,
		RecoveryPointID:      plan.RecoveryPointID,
		CatalogGenerationID:  plan.CatalogGenerationID,
		SelectionDigest:      plan.SelectionDigest,
		SourceRevisionDigest: plan.SourceRevisionDigest,
		ManifestDigest:       plan.ImmutableManifestDigest,
	}, entry: provider.RestoreEntry{
		AssetRef:           backupasset.AssetRef{RecoveryPointID: point.ID, EntryID: entry.EntryID},
		Type:               backupasset.CatalogEntryFile,
		ExpectedSize:       entry.Size,
		TargetObjectDigest: item.RelativePathDigest,
	}, content: sourceContents, root: managedRoot, taskSource: sourceDirectory,
		sourceFingerprint: point.SourceFingerprint,
	}
}

func TestRsyncResolverExposesOnlyExactDeclaredEntries(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if err != nil {
		t.Fatalf("resolve durable Rsync source: %v", err)
	}
	defer func() { _ = source.Close() }()
	materialized, err := source.MaterializeDeclaredEntries(context.Background(), []provider.RestoreEntry{fixture.entry})
	if err != nil || len(materialized) != 1 {
		t.Fatalf("materialize exact declared entry: entries=%#v err=%v", materialized, err)
	}
	strictEntry := materialized[0]

	stream, err := source.OpenDeclaredRegular(context.Background(), strictEntry)
	if err != nil {
		t.Fatalf("open exact declared entry: %v", err)
	}
	payload, readErr := io.ReadAll(stream)
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read exact declared entry: read=%v close=%v", readErr, closeErr)
	}
	if string(payload) != fixture.content {
		t.Fatalf("declared entry payload = %q, want %q", payload, fixture.content)
	}

	for _, test := range []struct {
		name   string
		mutate func(*provider.RestoreEntry)
	}{
		{name: "asset ref", mutate: func(entry *provider.RestoreEntry) { entry.AssetRef.EntryID = strings.Repeat("e", 64) }},
		{name: "type", mutate: func(entry *provider.RestoreEntry) { entry.Type = backupasset.CatalogEntryDirectory }},
		{name: "size", mutate: func(entry *provider.RestoreEntry) { entry.ExpectedSize++ }},
		{name: "digest", mutate: func(entry *provider.RestoreEntry) { entry.ExpectedDigest = strings.Repeat("e", 64) }},
		{name: "target object", mutate: func(entry *provider.RestoreEntry) { entry.TargetObjectDigest = strings.Repeat("e", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := strictEntry
			test.mutate(&candidate)
			stream, err := source.OpenDeclaredRegular(context.Background(), candidate)
			if stream != nil {
				_ = stream.Close()
			}
			if !errors.Is(err, provider.ErrRsyncRestoreSourceDrift) {
				t.Fatalf("forged declared entry error=%v, want source drift", err)
			}
		})
	}
}

func TestRsyncResolverAcceptsCatalogProjectionDigestDistinctFromManifest(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if err != nil {
		t.Fatalf("resolve Catalog-none source before strong projection: %v", err)
	}
	materialized, err := source.MaterializeDeclaredEntries(context.Background(), []provider.RestoreEntry{fixture.entry})
	closeErr := source.Close()
	if err != nil || closeErr != nil || len(materialized) != 1 {
		t.Fatalf("materialize strong projection digest: entries=%#v materialize=%v close=%v", materialized, err, closeErr)
	}
	if err := fixture.db.Model(&model.CatalogEntry{}).
		Where("generation_id = ? AND entry_id = ?", fixture.ref.CatalogGenerationID, fixture.entry.AssetRef.EntryID).
		Updates(map[string]any{
			"fingerprint": materialized[0].ExpectedDigest, "fingerprint_strength": string(catalog.FingerprintStrong),
		}).Error; err != nil {
		t.Fatal(err)
	}
	var generation model.CatalogGeneration
	if err := fixture.db.First(&generation, "id = ?", fixture.ref.CatalogGenerationID).Error; err != nil {
		t.Fatal(err)
	}
	if generation.WrittenDigest == generation.ExpectedDigest {
		t.Fatal("fixture must keep the Catalog projection digest distinct from the provider manifest digest")
	}

	source, err = fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if err != nil {
		t.Fatalf("resolve with distinct Catalog projection digest: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close resolved source: %v", err)
	}
}

func TestRsyncResolverDerivesMissingCatalogFingerprintFromAuthenticatedTree(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	var entry model.CatalogEntry
	if err := fixture.db.First(&entry,
		"generation_id = ? AND entry_id = ?", fixture.ref.CatalogGenerationID, fixture.entry.AssetRef.EntryID).Error; err != nil {
		t.Fatal(err)
	}
	if entry.Fingerprint != "" || entry.FingerprintStrength != string(catalog.FingerprintNone) {
		t.Fatalf("fixture Catalog fingerprint = %q/%q, want unavailable/none", entry.Fingerprint, entry.FingerprintStrength)
	}

	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if err != nil {
		t.Fatalf("resolve from authenticated managed-tree manifest: %v", err)
	}
	defer func() { _ = source.Close() }()
	materialized, err := source.MaterializeDeclaredEntries(context.Background(), []provider.RestoreEntry{fixture.entry})
	if err != nil || len(materialized) != 1 || materialized[0].Validate(fixture.ref.RecoveryPointID) != nil {
		t.Fatalf("materialize manifest-backed declared entry: entries=%#v err=%v", materialized, err)
	}
}

func TestRsyncRestorePortMaterializesFingerprintNoneDigestFromDurableSource(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.First(&plan, "id = ?", fixture.ref.PlanID).Error; err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetRecoveryPlanItem
	if err := fixture.db.Where("plan_id = ?", plan.ID).First(&item).Error; err != nil {
		t.Fatal(err)
	}
	var catalogEntry model.CatalogEntry
	if err := fixture.db.Where("generation_id = ? AND entry_id = ?", item.CatalogGenerationID, item.EntryID).
		First(&catalogEntry).Error; err != nil {
		t.Fatal(err)
	}
	if catalogEntry.Fingerprint != "" || catalogEntry.FingerprintStrength != string(catalog.FingerprintNone) {
		t.Fatalf("durable Catalog entry fingerprint = %q/%q, want unavailable/none", catalogEntry.Fingerprint, catalogEntry.FingerprintStrength)
	}
	ref := provider.RsyncRestoreSourceRef{
		PlanID:               plan.ID,
		PlanBindingDigest:    plan.BindingDigest,
		RepositoryID:         plan.RepositoryID,
		RecoveryPointID:      plan.RecoveryPointID,
		CatalogGenerationID:  plan.CatalogGenerationID,
		SelectionDigest:      plan.SelectionDigest,
		SourceRevisionDigest: plan.SourceRevisionDigest,
		ManifestDigest:       plan.ImmutableManifestDigest,
	}
	if err := ref.Validate(); err != nil {
		t.Fatalf("project durable Recovery plan to scalar Rsync ref: %v", err)
	}

	request := validRepositoryRsyncRestoreRequest(t)
	request.Rsync = &provider.RsyncRestoreRequest{ManifestDigest: ref.ManifestDigest, SourceRef: ref}
	request.Entries = []provider.RestoreEntry{{
		AssetRef: backupasset.AssetRef{RecoveryPointID: item.RecoveryPointID, EntryID: item.EntryID},
		Type:     backupasset.CatalogEntryType(item.EntryType), ExpectedSize: catalogEntry.Size,
		TargetObjectDigest: item.RelativePathDigest,
	}}
	durableFacts := request.Entries[0]
	if request.Entries[0].ExpectedDigest != "" {
		t.Fatal("production-path fixture invented a caller-side content digest")
	}

	now := time.Now().UTC()
	preflightPermit, err := provider.NewTargetPreflightPermit(provider.TargetObservationPermit{
		TargetBindingDigest: request.Target.BindingDigest,
		Session: provider.TargetSession{
			ID: strings.Repeat("a", 32), Purpose: provider.TargetPurposePreflight,
			CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute),
		},
	}, request.Target, now)
	if err != nil {
		t.Fatal(err)
	}
	verifyPermit, err := provider.NewTargetVerifyPermit(provider.TargetObservationPermit{
		TargetBindingDigest: request.Target.BindingDigest,
		Session: provider.TargetSession{
			ID: strings.Repeat("b", 32), Purpose: provider.TargetPurposeVerify,
			CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute),
		},
	}, request.Target, now)
	if err != nil {
		t.Fatal(err)
	}
	reconcilePermit, err := provider.NewTargetReconcilePermit(provider.TargetObservationPermit{
		TargetBindingDigest: request.Target.BindingDigest,
		Session: provider.TargetSession{
			ID: strings.Repeat("c", 32), Purpose: provider.TargetPurposeReconcile,
			CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute),
		},
	}, request.Target, now)
	if err != nil {
		t.Fatal(err)
	}

	runner := &recordingRepositoryRsyncRestoreRunner{}
	port := NewRsyncRestorePort(fixture.service, &recordingRepositoryRsyncTargetWriter{}, runner)
	if _, err := provider.PreflightRestore(context.Background(), port, provider.RestorePreflightRequest{Request: request, Permit: preflightPermit}); err != nil {
		t.Errorf("Preflight with scalar ref and durable entry facts: %v", err)
	}
	if _, err := provider.ExecuteRestore(context.Background(), port, request, provider.RestoreProgress{}); err != nil {
		t.Errorf("Execute with scalar ref and durable entry facts: %v", err)
	}
	if _, err := provider.VerifyRestore(context.Background(), port, provider.RestoreVerifyRequest{Request: request, Permit: verifyPermit}); err != nil {
		t.Errorf("Verify with scalar ref and durable entry facts: %v", err)
	}
	if _, err := provider.ReconcileRestore(context.Background(), port, provider.RestoreReconcileRequest{Request: request, Permit: reconcilePermit}); err != nil {
		t.Errorf("Reconcile with scalar ref and durable entry facts: %v", err)
	}

	phaseEntries := map[string][]provider.RestoreEntry{}
	if len(runner.preflights) == 1 {
		phaseEntries["preflight"] = runner.preflights[0].Entries
	}
	if len(runner.executes) == 1 {
		phaseEntries["execute"] = runner.executes[0].Entries
	}
	if len(runner.verifies) == 1 {
		phaseEntries["verify"] = runner.verifies[0].Entries
	}
	if len(runner.reconciles) == 1 {
		phaseEntries["reconcile"] = runner.reconciles[0].Entries
	}
	if len(phaseEntries) != 4 {
		t.Fatalf("runner phases = %v, want preflight/execute/verify/reconcile", phaseEntries)
	}
	for phase, entries := range phaseEntries {
		if len(entries) != 1 || entries[0].Validate(ref.RecoveryPointID) != nil || entries[0].ExpectedDigest == "" {
			t.Errorf("%s runner entries = %#v, want one Repository-materialized strict entry", phase, entries)
			continue
		}
		strict := entries[0]
		strict.ExpectedDigest = ""
		if strict != durableFacts {
			t.Errorf("%s runner durable entry facts = %#v, want %#v", phase, strict, durableFacts)
		}
	}
	if request.Entries[0].ExpectedDigest != "" {
		t.Fatalf("caller request learned Repository-private digest %q", request.Entries[0].ExpectedDigest)
	}
}

func TestRsyncResolverRejectsEmptyCatalogFingerprintStrength(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	if err := fixture.db.Model(&model.CatalogEntry{}).
		Where("generation_id = ? AND entry_id = ?", fixture.ref.CatalogGenerationID, fixture.entry.AssetRef.EntryID).
		Update("fingerprint_strength", "").Error; err != nil {
		t.Fatal(err)
	}

	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if source != nil {
		_ = source.Close()
	}
	if !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("empty Catalog fingerprint strength error=%v, want invalid restore request", err)
	}
}

func TestRsyncResolverRejectsStrongCatalogFingerprintMismatchAgainstAuthenticatedTree(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	if err := fixture.db.Model(&model.CatalogEntry{}).
		Where("generation_id = ? AND entry_id = ?", fixture.ref.CatalogGenerationID, fixture.entry.AssetRef.EntryID).
		Updates(map[string]any{
			"fingerprint": strings.Repeat("0", 64), "fingerprint_strength": string(catalog.FingerprintStrong),
		}).Error; err != nil {
		t.Fatal(err)
	}

	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if source != nil {
		_ = source.Close()
	}
	if !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("strong Catalog fingerprint mismatch error=%v, want invalid restore request", err)
	}
}

func TestRsyncResolverRejectsCatalogLocatorSubstitutionWithinAuthenticatedTree(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixtureWithAlternate(t, true)
	var entry model.CatalogEntry
	if err := fixture.db.First(&entry,
		"generation_id = ? AND entry_id = ?", fixture.ref.CatalogGenerationID, fixture.entry.AssetRef.EntryID).Error; err != nil {
		t.Fatal(err)
	}
	entry.EncryptedProviderLocator = `{"version":1,"native":"alternate/payload.txt"}`
	if err := fixture.db.Save(&entry).Error; err != nil {
		t.Fatal(err)
	}

	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if source != nil {
		_ = source.Close()
	}
	if !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("Catalog locator substitution error=%v, want invalid restore request", err)
	}
}

func TestRsyncResolverRejectsSameKeyCatalogEntryPathSubstitutionWithinAuthenticatedTree(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixtureWithAlternate(t, true)
	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if err != nil {
		t.Fatalf("resolve frozen Catalog entry before substitution: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close frozen Catalog source: %v", err)
	}

	var entry model.CatalogEntry
	if err := fixture.db.First(&entry,
		"generation_id = ? AND entry_id = ?", fixture.ref.CatalogGenerationID, fixture.entry.AssetRef.EntryID).Error; err != nil {
		t.Fatal(err)
	}
	entry.NormalizedPath = "alternate/payload.txt"
	entry.EncryptedProviderLocator = `{"version":1,"native":"alternate/payload.txt"}`
	if err := fixture.db.Save(&entry).Error; err != nil {
		t.Fatal(err)
	}

	source, err = fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if source != nil {
		_ = source.Close()
	}
	if !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("same-key Catalog substitution error=%v, want invalid restore request", err)
	}
	if err != nil && strings.Contains(err.Error(), "alternate/payload.txt") {
		t.Fatalf("same-key Catalog substitution leaked a private path: %v", err)
	}
}

func TestRsyncRestorePortRevalidatesAllFourPhases(t *testing.T) {
	request := validRepositoryRsyncRestoreRequest(t)
	resolver := &recordingRepositoryRsyncRestoreSourceResolver{root: t.TempDir()}
	writer := &recordingRepositoryRsyncTargetWriter{}
	runner := &recordingRepositoryRsyncRestoreRunner{}
	port := NewRsyncRestorePort(resolver, writer, runner)
	now := time.Now().UTC()

	preflightPermit, err := provider.NewTargetPreflightPermit(provider.TargetObservationPermit{
		TargetBindingDigest: request.Target.BindingDigest,
		Session:             provider.TargetSession{ID: strings.Repeat("a", 32), Purpose: provider.TargetPurposePreflight, CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute)},
	}, request.Target, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := port.Preflight(context.Background(), provider.RestorePreflightRequest{Request: request, Permit: preflightPermit}); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if _, err := port.Execute(context.Background(), request, provider.RestoreProgress{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	verifyPermit, err := provider.NewTargetVerifyPermit(provider.TargetObservationPermit{
		TargetBindingDigest: request.Target.BindingDigest,
		Session:             provider.TargetSession{ID: strings.Repeat("b", 32), Purpose: provider.TargetPurposeVerify, CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute)},
	}, request.Target, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := port.Verify(context.Background(), provider.RestoreVerifyRequest{Request: request, Permit: verifyPermit}); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	reconcilePermit, err := provider.NewTargetReconcilePermit(provider.TargetObservationPermit{
		TargetBindingDigest: request.Target.BindingDigest,
		Session:             provider.TargetSession{ID: strings.Repeat("c", 32), Purpose: provider.TargetPurposeReconcile, CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute)},
	}, request.Target, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := port.Reconcile(context.Background(), provider.RestoreReconcileRequest{Request: request, Permit: reconcilePermit}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := len(resolver.refs); got != 4 {
		t.Fatalf("resolver calls = %d, want 4", got)
	}
	for index, ref := range resolver.refs {
		if ref != request.Rsync.SourceRef {
			t.Fatalf("resolver ref %d = %#v, want exact scalar ref %#v", index, ref, request.Rsync.SourceRef)
		}
	}
	if len(runner.preflights) != 1 || len(runner.executes) != 1 || len(runner.verifies) != 1 || len(runner.reconciles) != 1 {
		t.Fatalf("runner calls preflight=%d execute=%d verify=%d reconcile=%d, want one each", len(runner.preflights), len(runner.executes), len(runner.verifies), len(runner.reconciles))
	}
	for _, injected := range []provider.RsyncTargetWriter{
		runner.preflights[0].TargetWriter,
		runner.executes[0].TargetWriter,
		runner.verifies[0].TargetWriter,
		runner.reconciles[0].TargetWriter,
	} {
		if injected != writer {
			t.Fatalf("runner target writer = %T, want exact injected authority", injected)
		}
	}
	for index, tree := range resolver.trees {
		if tree.revalidations != 2 || tree.closes != 1 {
			t.Fatalf("source %d revalidations=%d closes=%d, want 2/1", index, tree.revalidations, tree.closes)
		}
	}
}

func TestRsyncRestorePortRejectsRequestEntrySetMismatchBeforeRunner(t *testing.T) {
	request := validRepositoryRsyncRestoreRequest(t)
	extra := request.Entries[0]
	extra.AssetRef.EntryID = strings.Repeat("e", 64)
	resolver := &recordingRepositoryRsyncRestoreSourceResolver{
		declared: []provider.RestoreEntry{request.Entries[0], extra},
	}
	runner := &recordingRepositoryRsyncRestoreRunner{}

	_, err := NewRsyncRestorePort(resolver, &recordingRepositoryRsyncTargetWriter{}, runner).
		Execute(context.Background(), request, provider.RestoreProgress{})

	if !errors.Is(err, provider.ErrRsyncRestoreSourceDrift) || !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("request entry-set mismatch error=%v, want typed source drift", err)
	}
	if len(runner.executes) != 0 {
		t.Fatalf("request entry-set mismatch reached runner %d time(s)", len(runner.executes))
	}
	if len(resolver.trees) != 1 || resolver.trees[0].entryValidations != 1 {
		t.Fatal("request entry set was not validated exactly once before runner dispatch")
	}
}

func TestRsyncRestorePortRejectsPostPhaseDurableAndMarkerDrift(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *rsyncRestoreResolverFixture)
	}{
		{
			name: "durable plan",
			mutate: func(t *testing.T, fixture *rsyncRestoreResolverFixture) {
				t.Helper()
				if err := fixture.db.Model(&model.BackupAssetRecoveryPlan{}).
					Where("id = ?", fixture.ref.PlanID).
					Update("selection_digest", strings.Repeat("e", 64)).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "authenticated commit marker",
			mutate: func(t *testing.T, fixture *rsyncRestoreResolverFixture) {
				t.Helper()
				marker := filepath.Join(fixture.root, "points", fixture.ref.RecoveryPointID, "commit.json")
				if err := os.WriteFile(marker, []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRsyncRestoreResolverFixture(t)
			request := validRepositoryRsyncRestoreRequest(t)
			request.Rsync = &provider.RsyncRestoreRequest{ManifestDigest: fixture.ref.ManifestDigest, SourceRef: fixture.ref}
			request.Entries = []provider.RestoreEntry{fixture.entry}
			runner := &recordingRepositoryRsyncRestoreRunner{executeHook: func() { test.mutate(t, fixture) }}

			_, err := NewRsyncRestorePort(fixture.service, &recordingRepositoryRsyncTargetWriter{}, runner).
				Execute(context.Background(), request, provider.RestoreProgress{})

			if !errors.Is(err, provider.ErrRsyncRestoreSourceDrift) || !errors.Is(err, provider.ErrInvalidRestoreRequest) {
				t.Fatalf("post-phase source drift error=%v, want typed source drift", err)
			}
			if len(runner.executes) != 1 {
				t.Fatalf("runner calls = %d, want one before post-phase rejection", len(runner.executes))
			}
		})
	}
}

func TestRsyncRestorePortRejectsSourceOrRootSwapBeforeRunner(t *testing.T) {
	for _, phase := range []struct {
		name   string
		invoke func(*RsyncRestorePort, provider.RestoreRequest) error
		calls  func(*recordingRepositoryRsyncRestoreRunner) int
	}{
		{
			name: "preflight",
			invoke: func(port *RsyncRestorePort, request provider.RestoreRequest) error {
				now := time.Now().UTC()
				permit, err := provider.NewTargetPreflightPermit(provider.TargetObservationPermit{TargetBindingDigest: request.Target.BindingDigest, Session: provider.TargetSession{ID: strings.Repeat("a", 32), Purpose: provider.TargetPurposePreflight, CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute)}}, request.Target, now)
				if err != nil {
					return err
				}
				_, err = port.Preflight(context.Background(), provider.RestorePreflightRequest{Request: request, Permit: permit})
				return err
			},
			calls: func(runner *recordingRepositoryRsyncRestoreRunner) int { return len(runner.preflights) },
		},
		{
			name: "execute",
			invoke: func(port *RsyncRestorePort, request provider.RestoreRequest) error {
				_, err := port.Execute(context.Background(), request, provider.RestoreProgress{})
				return err
			},
			calls: func(runner *recordingRepositoryRsyncRestoreRunner) int { return len(runner.executes) },
		},
		{
			name: "verify",
			invoke: func(port *RsyncRestorePort, request provider.RestoreRequest) error {
				now := time.Now().UTC()
				permit, err := provider.NewTargetVerifyPermit(provider.TargetObservationPermit{TargetBindingDigest: request.Target.BindingDigest, Session: provider.TargetSession{ID: strings.Repeat("b", 32), Purpose: provider.TargetPurposeVerify, CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute)}}, request.Target, now)
				if err != nil {
					return err
				}
				_, err = port.Verify(context.Background(), provider.RestoreVerifyRequest{Request: request, Permit: permit})
				return err
			},
			calls: func(runner *recordingRepositoryRsyncRestoreRunner) int { return len(runner.verifies) },
		},
		{
			name: "reconcile",
			invoke: func(port *RsyncRestorePort, request provider.RestoreRequest) error {
				now := time.Now().UTC()
				permit, err := provider.NewTargetReconcilePermit(provider.TargetObservationPermit{TargetBindingDigest: request.Target.BindingDigest, Session: provider.TargetSession{ID: strings.Repeat("c", 32), Purpose: provider.TargetPurposeReconcile, CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute)}}, request.Target, now)
				if err != nil {
					return err
				}
				_, err = port.Reconcile(context.Background(), provider.RestoreReconcileRequest{Request: request, Permit: permit})
				return err
			},
			calls: func(runner *recordingRepositoryRsyncRestoreRunner) int { return len(runner.reconciles) },
		},
	} {
		t.Run(phase.name, func(t *testing.T) {
			resolver := &recordingRepositoryRsyncRestoreSourceResolver{root: t.TempDir(), revalidateErr: fileaccess.ErrSourceChanged}
			runner := &recordingRepositoryRsyncRestoreRunner{}
			err := phase.invoke(NewRsyncRestorePort(resolver, &recordingRepositoryRsyncTargetWriter{}, runner), validRepositoryRsyncRestoreRequest(t))
			if !errors.Is(err, provider.ErrRsyncRestoreSourceDrift) || !errors.Is(err, provider.ErrInvalidRestoreRequest) {
				t.Fatalf("source/root swap error=%v, want typed source drift", err)
			}
			if calls := phase.calls(runner); calls != 0 {
				t.Fatalf("source/root swap reached %s runner %d time(s)", phase.name, calls)
			}
		})
	}
}

func TestRsyncRestorePortRevalidatesAfterRunnerError(t *testing.T) {
	const privateFailure = "FAKE_RSYNC_PRIVATE_RUNNER_FAILURE_FOR_TEST_ONLY"
	for _, phase := range []struct {
		name      string
		setError  func(*recordingRepositoryRsyncRestoreRunner, error)
		invoke    func(*RsyncRestorePort, provider.RestoreRequest) error
		callCount func(*recordingRepositoryRsyncRestoreRunner) int
	}{
		{
			name:     "preflight",
			setError: func(runner *recordingRepositoryRsyncRestoreRunner, err error) { runner.preflightErr = err },
			invoke: func(port *RsyncRestorePort, request provider.RestoreRequest) error {
				now := time.Now().UTC()
				permit, err := provider.NewTargetPreflightPermit(provider.TargetObservationPermit{TargetBindingDigest: request.Target.BindingDigest, Session: provider.TargetSession{ID: strings.Repeat("a", 32), Purpose: provider.TargetPurposePreflight, CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute)}}, request.Target, now)
				if err != nil {
					return err
				}
				_, err = port.Preflight(context.Background(), provider.RestorePreflightRequest{Request: request, Permit: permit})
				return err
			},
			callCount: func(runner *recordingRepositoryRsyncRestoreRunner) int { return len(runner.preflights) },
		},
		{
			name:     "execute",
			setError: func(runner *recordingRepositoryRsyncRestoreRunner, err error) { runner.executeErr = err },
			invoke: func(port *RsyncRestorePort, request provider.RestoreRequest) error {
				_, err := port.Execute(context.Background(), request, provider.RestoreProgress{})
				return err
			},
			callCount: func(runner *recordingRepositoryRsyncRestoreRunner) int { return len(runner.executes) },
		},
		{
			name:     "verify",
			setError: func(runner *recordingRepositoryRsyncRestoreRunner, err error) { runner.verifyErr = err },
			invoke: func(port *RsyncRestorePort, request provider.RestoreRequest) error {
				now := time.Now().UTC()
				permit, err := provider.NewTargetVerifyPermit(provider.TargetObservationPermit{TargetBindingDigest: request.Target.BindingDigest, Session: provider.TargetSession{ID: strings.Repeat("b", 32), Purpose: provider.TargetPurposeVerify, CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute)}}, request.Target, now)
				if err != nil {
					return err
				}
				_, err = port.Verify(context.Background(), provider.RestoreVerifyRequest{Request: request, Permit: permit})
				return err
			},
			callCount: func(runner *recordingRepositoryRsyncRestoreRunner) int { return len(runner.verifies) },
		},
		{
			name:     "reconcile",
			setError: func(runner *recordingRepositoryRsyncRestoreRunner, err error) { runner.reconcileErr = err },
			invoke: func(port *RsyncRestorePort, request provider.RestoreRequest) error {
				now := time.Now().UTC()
				permit, err := provider.NewTargetReconcilePermit(provider.TargetObservationPermit{TargetBindingDigest: request.Target.BindingDigest, Session: provider.TargetSession{ID: strings.Repeat("c", 32), Purpose: provider.TargetPurposeReconcile, CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute)}}, request.Target, now)
				if err != nil {
					return err
				}
				_, err = port.Reconcile(context.Background(), provider.RestoreReconcileRequest{Request: request, Permit: permit})
				return err
			},
			callCount: func(runner *recordingRepositoryRsyncRestoreRunner) int { return len(runner.reconciles) },
		},
	} {
		for _, failure := range []struct {
			name      string
			runnerErr error
			want      error
		}{
			{name: "ordinary", runnerErr: errors.New(privateFailure), want: provider.ErrRsyncRestoreSourceDrift},
			{name: "canceled", runnerErr: fmt.Errorf("%s: %w", privateFailure, context.Canceled), want: context.Canceled},
			{name: "deadline", runnerErr: fmt.Errorf("%s: %w", privateFailure, context.DeadlineExceeded), want: context.DeadlineExceeded},
		} {
			t.Run(phase.name+"/"+failure.name, func(t *testing.T) {
				resolver := &recordingRepositoryRsyncRestoreSourceResolver{
					revalidateErr:   fileaccess.ErrSourceChanged,
					revalidateErrAt: 2,
				}
				runner := &recordingRepositoryRsyncRestoreRunner{}
				phase.setError(runner, failure.runnerErr)

				err := phase.invoke(NewRsyncRestorePort(resolver, &recordingRepositoryRsyncTargetWriter{}, runner), validRepositoryRsyncRestoreRequest(t))

				if !errors.Is(err, failure.want) || strings.Contains(err.Error(), privateFailure) {
					t.Fatalf("post-runner error=%v, want sanitized %v identity", err, failure.want)
				}
				if calls := phase.callCount(runner); calls != 1 {
					t.Fatalf("%s runner calls=%d, want one", phase.name, calls)
				}
				if len(resolver.trees) != 1 {
					t.Fatalf("source trees=%d, want one", len(resolver.trees))
				}
				tree := resolver.trees[0]
				if tree.revalidations != 2 || tree.closes != 1 {
					t.Fatalf("source revalidations/closes=%d/%d, want 2/1", tree.revalidations, tree.closes)
				}
			})
		}
	}
}

func TestRsyncRestorePortSanitizesResolverAndRunnerErrors(t *testing.T) {
	const privateFailure = "FAKE_RSYNC_PRIVATE_PATH_TOKEN_FOR_TEST_ONLY"
	request := validRepositoryRsyncRestoreRequest(t)
	resolver := &recordingRepositoryRsyncRestoreSourceResolver{root: t.TempDir(), resolveErr: errors.New(privateFailure)}
	if _, err := NewRsyncRestorePort(resolver, &recordingRepositoryRsyncTargetWriter{}, &recordingRepositoryRsyncRestoreRunner{}).Execute(context.Background(), request, provider.RestoreProgress{}); !errors.Is(err, provider.ErrRsyncRestoreUnavailable) || strings.Contains(err.Error(), privateFailure) {
		t.Fatalf("resolver error=%v, want sanitized unavailable", err)
	}
	resolver = &recordingRepositoryRsyncRestoreSourceResolver{root: t.TempDir()}
	runner := &recordingRepositoryRsyncRestoreRunner{executeErr: errors.New(privateFailure)}
	if _, err := NewRsyncRestorePort(resolver, &recordingRepositoryRsyncTargetWriter{}, runner).Execute(context.Background(), request, provider.RestoreProgress{}); !errors.Is(err, provider.ErrRsyncRestoreUnavailable) || strings.Contains(err.Error(), privateFailure) {
		t.Fatalf("runner error=%v, want sanitized unavailable", err)
	}
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(sentinel.Error()+" from resolver", func(t *testing.T) {
			resolver := &recordingRepositoryRsyncRestoreSourceResolver{resolveErr: fmt.Errorf("%s: %w", privateFailure, sentinel)}
			_, err := NewRsyncRestorePort(resolver, &recordingRepositoryRsyncTargetWriter{}, &recordingRepositoryRsyncRestoreRunner{}).
				Execute(context.Background(), request, provider.RestoreProgress{})
			if !errors.Is(err, sentinel) || strings.Contains(err.Error(), privateFailure) {
				t.Fatalf("resolver context error=%v, want sanitized %v identity", err, sentinel)
			}
		})
		t.Run(sentinel.Error()+" from runner", func(t *testing.T) {
			resolver := &recordingRepositoryRsyncRestoreSourceResolver{}
			runner := &recordingRepositoryRsyncRestoreRunner{executeErr: fmt.Errorf("%s: %w", privateFailure, sentinel)}
			_, err := NewRsyncRestorePort(resolver, &recordingRepositoryRsyncTargetWriter{}, runner).
				Execute(context.Background(), request, provider.RestoreProgress{})
			if !errors.Is(err, sentinel) || strings.Contains(err.Error(), privateFailure) {
				t.Fatalf("runner context error=%v, want sanitized %v identity", err, sentinel)
			}
		})
	}
}

func validRepositoryRsyncRestoreRequest(t *testing.T) provider.RestoreRequest {
	t.Helper()
	now := time.Now().UTC()
	target, err := provider.NewRestoreTarget(7, "approved-root", strings.Repeat("3", 64), strings.Repeat("4", 64), "root-revision-1", "target-revision-1")
	if err != nil {
		t.Fatal(err)
	}
	ref := provider.RsyncRestoreSourceRef{
		PlanID: strings.Repeat("1", 32), PlanBindingDigest: strings.Repeat("2", 64), RepositoryID: strings.Repeat("3", 32),
		RecoveryPointID: strings.Repeat("4", 32), CatalogGenerationID: strings.Repeat("5", 32), SelectionDigest: strings.Repeat("6", 64),
		SourceRevisionDigest: strings.Repeat("7", 64), ManifestDigest: strings.Repeat("8", 64),
	}
	fence := provider.RestoreFence{JobID: strings.Repeat("9", 32), AttemptID: strings.Repeat("a", 32), NodeLeaseID: strings.Repeat("b", 32), AttemptFence: 11, NodeFence: 13, ExpectedTargetRevision: target.TargetRevision}
	checkpoint := provider.RestoreCheckpoint{ID: strings.Repeat("c", 32), OperationDigest: strings.Repeat("d", 64), PriorTargetRevision: target.TargetRevision, VerifiedTargetIdentityDigest: strings.Repeat("e", 64), VerifiedTargetRevision: target.TargetRevision, VerifiedBytes: 17, AttemptFence: fence.AttemptFence, NodeFence: fence.NodeFence}
	return provider.RestoreRequest{
		Version: provider.RestoreRequestSchemaV1, Provider: backupasset.ProviderRsync,
		Entries: []provider.RestoreEntry{{AssetRef: backupasset.AssetRef{RecoveryPointID: ref.RecoveryPointID, EntryID: strings.Repeat("f", 64)}, Type: backupasset.CatalogEntryFile, ExpectedSize: 17, TargetObjectDigest: strings.Repeat("b", 64)}},
		Target:  target, Limits: provider.RestoreLimits{MaxEntries: 2, MaxBytes: 1024, MaxEntryBytes: 1024}, ConflictPolicy: provider.RestoreConflictFailOnConflict,
		Fence: fence, Checkpoint: checkpoint,
		MutationPermit: provider.TargetMutationPermit{TargetBindingDigest: target.BindingDigest, UseLatchID: provider.RestoreSchemaUseLatchID, JobID: fence.JobID, AttemptID: fence.AttemptID, NodeLeaseID: fence.NodeLeaseID, AttemptFence: fence.AttemptFence, NodeFence: fence.NodeFence, ExpectedTargetRevision: target.TargetRevision, Session: provider.TargetSession{ID: strings.Repeat("d", 32), Purpose: provider.TargetPurposeWrite, CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute)}},
		Rsync:          &provider.RsyncRestoreRequest{ManifestDigest: ref.ManifestDigest, SourceRef: ref},
	}
}

type recordingRepositoryRsyncRestoreSourceResolver struct {
	root            string
	refs            []provider.RsyncRestoreSourceRef
	trees           []*recordingRepositoryPinnedStrictTree
	declared        []provider.RestoreEntry
	resolveErr      error
	revalidateErr   error
	revalidateErrAt int
}

func (resolver *recordingRepositoryRsyncRestoreSourceResolver) ResolveRsyncRestoreSource(_ context.Context, ref provider.RsyncRestoreSourceRef) (provider.RsyncRestoreSource, error) {
	resolver.refs = append(resolver.refs, ref)
	if resolver.resolveErr != nil {
		return nil, resolver.resolveErr
	}
	recording := &recordingRepositoryPinnedStrictTree{
		declared: append([]provider.RestoreEntry(nil), resolver.declared...), revalidateErr: resolver.revalidateErr,
		revalidateErrAt: resolver.revalidateErrAt,
	}
	resolver.trees = append(resolver.trees, recording)
	return recording, nil
}

type recordingRepositoryPinnedStrictTree struct {
	revalidations    int
	entryValidations int
	closes           int
	declared         []provider.RestoreEntry
	revalidateErr    error
	revalidateErrAt  int
}

func (*recordingRepositoryPinnedStrictTree) OpenDeclaredRegular(context.Context, provider.RestoreEntry) (provider.RsyncRestoreSourceStream, error) {
	return nil, provider.ErrRsyncRestoreSourceDrift
}

func (tree *recordingRepositoryPinnedStrictTree) MaterializeDeclaredEntries(_ context.Context, entries []provider.RestoreEntry) ([]provider.RestoreEntry, error) {
	tree.entryValidations++
	for _, entry := range entries {
		if entry.ExpectedDigest != "" {
			return nil, provider.ErrRsyncRestoreSourceDrift
		}
	}
	if len(tree.declared) == 0 {
		materialized := append([]provider.RestoreEntry(nil), entries...)
		for index := range materialized {
			materialized[index].ExpectedDigest = strings.Repeat("a", 64)
		}
		return materialized, nil
	}
	declaredFacts := append([]provider.RestoreEntry(nil), tree.declared...)
	for index := range declaredFacts {
		declaredFacts[index].ExpectedDigest = ""
	}
	if !reflect.DeepEqual(declaredFacts, entries) {
		return nil, provider.ErrRsyncRestoreSourceDrift
	}
	materialized := append([]provider.RestoreEntry(nil), tree.declared...)
	for index := range materialized {
		if materialized[index].ExpectedDigest == "" {
			materialized[index].ExpectedDigest = strings.Repeat("a", 64)
		}
	}
	return materialized, nil
}

func (tree *recordingRepositoryPinnedStrictTree) Revalidate(ctx context.Context) error {
	tree.revalidations++
	if tree.revalidateErr != nil && (tree.revalidateErrAt == 0 || tree.revalidations == tree.revalidateErrAt) {
		return tree.revalidateErr
	}
	return nil
}

func (tree *recordingRepositoryPinnedStrictTree) Close() error {
	tree.closes++
	return nil
}

type recordingRepositoryRsyncTargetWriter struct{}

func (*recordingRepositoryRsyncTargetWriter) WriteDeclaredRegular(context.Context, provider.RsyncTargetWriteCall) error {
	return nil
}

type recordingRepositoryRsyncRestoreRunner struct {
	preflights   []provider.RsyncRestorePreflightCall
	executes     []provider.RsyncRestoreExecuteCall
	verifies     []provider.RsyncRestoreVerifyCall
	reconciles   []provider.RsyncRestoreReconcileCall
	preflightErr error
	executeErr   error
	verifyErr    error
	reconcileErr error
	executeHook  func()
}

func (runner *recordingRepositoryRsyncRestoreRunner) Preflight(_ context.Context, call provider.RsyncRestorePreflightCall) (provider.RsyncRestoreRunnerEvidence, error) {
	if call.Source == nil {
		return provider.RsyncRestoreRunnerEvidence{}, errors.New("pinned source was not provided")
	}
	runner.preflights = append(runner.preflights, call)
	if runner.preflightErr != nil {
		return provider.RsyncRestoreRunnerEvidence{}, runner.preflightErr
	}
	return provider.RsyncRestoreRunnerEvidence{TargetBindingDigest: call.Target.TargetBindingDigest, TargetRevision: call.Target.TargetRevision, Checkpoint: call.Checkpoint}, nil
}

func (runner *recordingRepositoryRsyncRestoreRunner) Execute(_ context.Context, call provider.RsyncRestoreExecuteCall) (provider.RsyncRestoreRunnerResult, error) {
	if call.Source == nil {
		return provider.RsyncRestoreRunnerResult{}, errors.New("pinned source was not provided")
	}
	runner.executes = append(runner.executes, call)
	if runner.executeHook != nil {
		runner.executeHook()
	}
	if runner.executeErr != nil {
		return provider.RsyncRestoreRunnerResult{}, runner.executeErr
	}
	return provider.RsyncRestoreRunnerResult{Checkpoint: provider.RestoreCheckpoint{ID: strings.Repeat("e", 32), OperationDigest: strings.Repeat("f", 64), PriorTargetRevision: call.Checkpoint.PriorTargetRevision, VerifiedTargetIdentityDigest: strings.Repeat("a", 64), VerifiedTargetRevision: "target-revision-next", VerifiedBytes: 17, AttemptFence: call.Fence.AttemptFence, NodeFence: call.Fence.NodeFence}}, nil
}

func (runner *recordingRepositoryRsyncRestoreRunner) Verify(_ context.Context, call provider.RsyncRestoreVerifyCall) (provider.RsyncRestoreRunnerEvidence, error) {
	if call.Source == nil {
		return provider.RsyncRestoreRunnerEvidence{}, errors.New("pinned source was not provided")
	}
	runner.verifies = append(runner.verifies, call)
	if runner.verifyErr != nil {
		return provider.RsyncRestoreRunnerEvidence{}, runner.verifyErr
	}
	return provider.RsyncRestoreRunnerEvidence{TargetBindingDigest: call.Target.TargetBindingDigest, TargetRevision: call.Target.TargetRevision, Checkpoint: call.Checkpoint}, nil
}

func (runner *recordingRepositoryRsyncRestoreRunner) Reconcile(_ context.Context, call provider.RsyncRestoreReconcileCall) (provider.RsyncRestoreRunnerEvidence, error) {
	if call.Source == nil {
		return provider.RsyncRestoreRunnerEvidence{}, errors.New("pinned source was not provided")
	}
	runner.reconciles = append(runner.reconciles, call)
	if runner.reconcileErr != nil {
		return provider.RsyncRestoreRunnerEvidence{}, runner.reconcileErr
	}
	return provider.RsyncRestoreRunnerEvidence{TargetBindingDigest: call.Target.TargetBindingDigest, TargetRevision: call.Target.TargetRevision, Checkpoint: call.Checkpoint}, nil
}

func TestContentReadManagedRsyncPointUsesDedicatedAdmissionOperation(t *testing.T) {
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
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: fixture.service.registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: fixture.admission, History: fixture.service.history, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeOperations := fixture.admission.operations()
	token := &managedRsyncPointReadTokenFake{operation: publication.OperationContentRead}
	_, err = service.beginManagedRsyncPointReadWithAdmission(
		context.Background(), fixture.task.ID, attempt.RecoveryPointID, token,
	)
	if !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("uncommitted content Rsync reader error=%v", err)
	}
	operations := fixture.admission.operations()
	if len(operations) != len(beforeOperations) || token.closed.Load() != 0 {
		t.Fatalf("content Rsync reacquired admission before=%v after=%v token_closes=%d", beforeOperations, operations, token.closed.Load())
	}
	if err := token.Close(); err != nil || token.closed.Load() != 1 {
		t.Fatalf("caller failed to release borrowed content admission: err=%v closes=%d", err, token.closed.Load())
	}
}

func TestManagedRsyncPointReadSessionRetainsAdmissionUntilReadHandleCloses(t *testing.T) {
	token := &managedRsyncPointReadTokenFake{}
	session := &ManagedRsyncPointReadSession{
		adapter: &managedRsyncPointReadAdapterFake{}, token: token,
	}
	handle, _, err := session.OpenSequential(context.Background(), provider.EntryLocator{Native: "file"}, provider.ReadRequest{MaxBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if got := token.closed.Load(); got != 0 {
		t.Fatalf("session released admission before read handle close: %d", got)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if got := token.closed.Load(); got != 1 {
		t.Fatalf("session admission close count=%d, want 1", got)
	}
	if _, _, err := session.OpenSequential(context.Background(), provider.EntryLocator{Native: "file"}, provider.ReadRequest{MaxBytes: 16}); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("closed session open error=%v, want forbidden", err)
	}
}

func TestManagedRsyncReadHandleForwardsProviderByteReporter(t *testing.T) {
	underlying := &meteredProviderReadHandleFake{Reader: strings.NewReader("data"), providerBytes: 5}
	handle := &managedRsyncPointReadHandle{underlying: underlying, session: &ManagedRsyncPointReadSession{}}
	reporter, ok := any(handle).(provider.ProviderByteReporter)
	if !ok {
		t.Fatal("managed Rsync wrapper hides ProviderByteReporter")
	}
	if payload, err := io.ReadAll(handle); err != nil || string(payload) != "data" {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
	if got := reporter.ProviderBytes(); got != 5 {
		t.Fatalf("forwarded Provider bytes=%d, want 5", got)
	}
}

type meteredProviderReadHandleFake struct {
	io.Reader
	providerBytes int64
}

func (*meteredProviderReadHandleFake) Close() error { return nil }

func (handle *meteredProviderReadHandleFake) ProviderBytes() int64 { return handle.providerBytes }

func TestManagedRsyncCommittedPointReadRequestBindsExactCommittedEvidence(t *testing.T) {
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
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: fixture.service.registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: fixture.admission, History: fixture.service.history, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := loadExactManagedRsyncPublicationRuntime(context.Background(), fixture.db, fixture.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", state.attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	request, access, err := service.managedRsyncCommittedPointReadRequest(context.Background(), runtime, point)
	if err != nil {
		t.Fatal(err)
	}
	if request.Attempt != state.attempt || request.ManagedRoot != fixture.binding.ManagedRootLocator ||
		request.CommitMarkerDigest != commit.CommitMarkerDigest || request.SourceFingerprint != point.SourceFingerprint ||
		request.ManifestDigest != point.ManifestDigest || request.ManifestEntryCount != uint64(point.EntryCount) || request.LogicalBytes != uint64(point.LogicalBytes) {
		t.Fatalf("committed Rsync reader request=%+v", request)
	}
	if access.AdapterData != nil || access.Locator != "" || access.RepositoryID != fixture.repository.ID || access.TaskID != fixture.task.ID || access.NodeID != fixture.task.NodeID {
		t.Fatalf("committed Rsync reader access=%+v", access)
	}
	driftedRuntime := runtime
	driftedRuntime.repository.CapabilityRevision++
	serviceWithoutKeyring, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: fixture.service.registry,
		Now: func() time.Time { return fixture.now }, Admission: fixture.admission, History: fixture.service.history,
		Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	driftedRequest, driftedAccess, driftErr := serviceWithoutKeyring.managedRsyncCommittedPointReadRequest(
		context.Background(), driftedRuntime, point,
	)
	if !errors.Is(driftErr, backupasset.ErrConflict) {
		t.Fatalf("capability-drift committed Rsync reader error=%v, want ErrConflict", driftErr)
	}
	if !strings.Contains(driftErr.Error(), "committed Rsync point capability revision changed") {
		t.Fatalf("capability-drift committed Rsync reader error=%v, want capability revision message", driftErr)
	}
	if !reflect.DeepEqual(driftedRequest, provider.RsyncCommittedPointReadRequest{}) ||
		!reflect.DeepEqual(driftedAccess, provider.AccessBinding{}) {
		t.Fatalf("capability-drift committed Rsync reader returned request=%+v access=%+v, want zero values", driftedRequest, driftedAccess)
	}
	if _, err := service.BeginManagedRsyncPointRead(context.Background(), fixture.task.ID, point.ID); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("unreadable committed Rsync tree error=%v, want capability unavailable", err)
	} else if reason, _, ok := CapabilityFromError(err); !ok || reason.Code != backupasset.CapabilityMutableSourceChanged {
		t.Fatalf("unreadable committed Rsync tree capability=%+v ok=%t", reason, ok)
	}

	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).Update("lineage_json", `{}`).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&point, "id = ?", point.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.managedRsyncCommittedPointReadRequest(context.Background(), runtime, point); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("drifted committed Rsync lineage error=%v, want invalid state", err)
	}
}

type managedRsyncPointReadAdapterFake struct{}

func (*managedRsyncPointReadAdapterFake) ListPoints(context.Context, provider.ReadSnapshot, provider.PageRequest) (provider.NativePointPage, error) {
	return provider.NativePointPage{}, nil
}

func (*managedRsyncPointReadAdapterFake) ListEntries(context.Context, provider.ReadSnapshot, provider.PointLocator, provider.EntryLocator, provider.PageRequest) (provider.EntryPage, error) {
	return provider.EntryPage{}, nil
}

func (*managedRsyncPointReadAdapterFake) StatEntry(context.Context, provider.ReadSnapshot, provider.PointLocator, provider.EntryLocator) (provider.Entry, error) {
	return provider.Entry{}, nil
}

func (*managedRsyncPointReadAdapterFake) OpenSequential(context.Context, provider.ReadSnapshot, provider.PointLocator, provider.EntryLocator, provider.ReadRequest) (provider.ReadHandle, provider.ContentStat, error) {
	return io.NopCloser(strings.NewReader("managed-rsync")), provider.ContentStat{}, nil
}

func (*managedRsyncPointReadAdapterFake) OpenRange(context.Context, provider.ReadSnapshot, provider.PointLocator, provider.EntryLocator, provider.ByteRange) (provider.ReadHandle, provider.ContentStat, error) {
	return io.NopCloser(strings.NewReader("managed-rsync")), provider.ContentStat{}, nil
}

type managedRsyncPointReadTokenFake struct {
	closed    atomic.Int32
	operation publication.ResticOperation
}

func (*managedRsyncPointReadTokenFake) Generation() uint64 { return 1 }
func (*managedRsyncPointReadTokenFake) Mode() publication.AdmissionMode {
	return publication.AdmissionManaged
}
func (token *managedRsyncPointReadTokenFake) Operation() publication.ResticOperation {
	if token.operation != "" {
		return token.operation
	}
	return publication.OperationManagedRsyncPointRead
}
func (token *managedRsyncPointReadTokenFake) Close() error {
	token.closed.Add(1)
	return nil
}

func TestVisibilityOwnershipQueryFailureFailsClosed(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	seedVisibilityRepository(t, db, strings.Repeat("e", 32), "must-not-leak", now)
	if err := db.Exec("DROP TABLE node_owners").Error; err != nil {
		t.Fatal(err)
	}
	service := newVisibilityServiceForTest(t, db, now)
	page, err := service.List(context.Background(), RepositoryListRequest{Limit: 10}, VisibilityScope{Role: "operator", UserID: 99}, RequestContext{})
	if err == nil || len(page.Items) != 0 {
		t.Fatalf("ownership failure returned page=%+v err=%v", page, err)
	}
}

func TestVisibilityRejectsMalformedOwnedPointBeforeRepositoryProjection(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 17, 19, 0, 0, 0, time.UTC)
	ownedTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/malformed", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	const operatorID uint = 702
	if err := db.Create(&model.User{
		ID: operatorID, Username: "malformed-operator", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
		Role: "operator", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: ownedTask.NodeID, UserID: operatorID, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	repository := seedVisibilityRepository(t, db, strings.Repeat("f", 32), "MUST_NOT_PROJECT_MALFORMED", now)
	unlinkedAt := now
	seedVisibilityLink(t, db, strings.Repeat("e", 32), repository.ID, &ownedTask.ID, "malformed-link", ownedTask.NodeID, now)
	if err := db.Model(&model.TaskRepositoryLink{}).Where("repository_id = ?", repository.ID).Update("unlinked_at", unlinkedAt).Error; err != nil {
		t.Fatal(err)
	}
	point := seedVisibilityPoint(t, db, strings.Repeat("d", 32), repository.ID, &ownedTask.ID, "malformed-point", ownedTask.NodeID, now)
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).Update("lineage_json", `{}`).Error; err != nil {
		t.Fatal(err)
	}
	service := newVisibilityServiceForTest(t, db, now)
	page, err := service.List(context.Background(), RepositoryListRequest{Limit: 10}, VisibilityScope{Role: "operator", UserID: operatorID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("malformed owned point projected repository=%+v", page.Items)
	}
	if _, err := service.Detail(context.Background(), repository.ID, VisibilityScope{Role: "operator", UserID: operatorID}, RequestContext{}); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("malformed repository detail error=%v", err)
	}
}

func TestQueryCursorIsSignedStableAndVisibilityScoped(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	seedVisibilityRepository(t, db, strings.Repeat("b", 32), "first", now)
	seedVisibilityRepository(t, db, strings.Repeat("c", 32), "second", now.Add(time.Second))
	seedVisibilityRepository(t, db, strings.Repeat("d", 32), "third", now.Add(2*time.Second))
	service := newVisibilityServiceForTest(t, db, now.Add(3*time.Second))
	scope := VisibilityScope{Role: "admin", UserID: 1}
	first, err := service.List(context.Background(), RepositoryListRequest{Limit: 1}, scope, RequestContext{})
	if err != nil || len(first.Items) != 1 || first.Items[0].ID != strings.Repeat("d", 32) || first.NextCursor == "" {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	for _, raw := range []string{"first", "second", "third", strings.Repeat("d", 32)} {
		if strings.Contains(first.NextCursor, raw) {
			t.Fatalf("cursor leaked %q: %s", raw, first.NextCursor)
		}
	}
	second, err := service.List(context.Background(), RepositoryListRequest{Limit: 1, Cursor: first.NextCursor}, scope, RequestContext{})
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != strings.Repeat("c", 32) {
		t.Fatalf("second page=%+v err=%v", second, err)
	}
	tampered := first.NextCursor[:len(first.NextCursor)-1] + "A"
	if _, err := service.List(context.Background(), RepositoryListRequest{Limit: 1, Cursor: tampered}, scope, RequestContext{}); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("tampered cursor error=%v", err)
	}
	if _, err := service.List(context.Background(), RepositoryListRequest{Limit: 1, Cursor: first.NextCursor}, VisibilityScope{Role: "admin", UserID: 2}, RequestContext{}); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("cross-scope cursor error=%v", err)
	}
}

func newVisibilityServiceForTest(t *testing.T, db *gorm.DB, now time.Time) *Service {
	t.Helper()
	service, err := NewService(Dependencies{DB: db, Foundation: enabledFoundation(), Keyring: backupasset.NewKeyring(db, func() time.Time { return now }), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func seedVisibilityRepository(t *testing.T, db *gorm.DB, id, name string, now time.Time) model.BackupRepository {
	t.Helper()
	identity := "restic-native:v1:" + strings.Repeat(string(id[0]), 64)
	repository := model.BackupRepository{
		ID: id, ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &identity, DisplayName: name,
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1,
		CapabilitiesJSON: `{"list":true,"open_sequential":true}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	return repository
}

func seedVisibilityLink(t *testing.T, db *gorm.DB, id, repositoryID string, taskID *uint, name string, nodeSnapshot uint, now time.Time) {
	t.Helper()
	link := model.TaskRepositoryLink{
		ID: id, TaskID: taskID, RepositoryID: repositoryID, TaskNameSnapshot: name, NodeIDSnapshot: nodeSnapshot,
		NodeNameSnapshot: "snapshot-node", PublicationMode: string(backupasset.PublicationNativeSnapshot),
		EncryptedLegacyLocator: "FAKE_PROVIDER_LOCATOR_FOR_TEST_ONLY", LinkedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
}

func seedVisibilityPoint(t *testing.T, db *gorm.DB, id, repositoryID string, taskID *uint, name string, nodeSnapshot uint, now time.Time) model.RecoveryPoint {
	t.Helper()
	lineage := `{}`
	var taskRunID *uint
	if taskID != nil {
		started := now.Add(-2 * time.Minute)
		run := model.TaskRun{
			TaskID: *taskID, TriggerType: "manual", Status: "success", StartedAt: &started, FinishedAt: &now,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := db.Create(&run).Error; err != nil {
			t.Fatal(err)
		}
		var link model.TaskRepositoryLink
		if err := db.Where("repository_id = ? AND task_id = ?", repositoryID, *taskID).First(&link).Error; err != nil {
			t.Fatal(err)
		}
		encoded, err := backupasset.EncodePublicationLineage(backupasset.PublicationLineageV1{
			Version: 1, TaskRepositoryLinkID: link.ID, TaskID: *taskID, TaskRunID: run.ID,
			Trigger: "manual", PublicationMode: string(backupasset.PublicationNativeSnapshot),
			PointCodecVersion: 1, TagCodecVersion: 1, StartedAt: started, PreparedAt: now.Add(-time.Minute), PointDeadlineAt: now.Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		lineage = encoded
		taskRunID = &run.ID
	}
	point := model.RecoveryPoint{
		ID: id, RepositoryID: repositoryID, ProducingTaskID: taskID, ProducingTaskRunID: taskRunID, ProducingTaskNameSnapshot: name,
		ProducingNodeIDSnapshot: nodeSnapshot, ProducingNodeNameSnapshot: "snapshot-node", LineageJSON: lineage,
		EncryptedProviderLocator: "FAKE_PROVIDER_LOCATOR_FOR_TEST_ONLY", Semantics: string(backupasset.PointNativeSnapshot),
		State: string(backupasset.RecoveryPointCommitted), CapturedAt: &now, CommittedAt: &now, SourceFingerprint: strings.Repeat(string(id[0]), 64),
		ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat(string(id[0]), 64), ConsistencyJSON: `{}`,
		FidelityJSON: `{}`, CapabilityRevision: 1, CapabilitiesJSON: `{"list":true,"open_sequential":true}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), PhysicalAvailability: string(backupasset.PhysicalOnline),
		HoldState: string(backupasset.HoldNone), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	return point
}
