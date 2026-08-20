package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestManagedHistoryLatchIgnoresStateAndNullableLineage(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	repository := seedManagedHistoryRepository(t, db, strings.Repeat("a", 32), now)
	states := []backupasset.RecoveryPointState{
		backupasset.RecoveryPointPreparing,
		backupasset.RecoveryPointVerifying,
		backupasset.RecoveryPointCommitted,
		backupasset.RecoveryPointDegraded,
		backupasset.RecoveryPointExpiring,
		backupasset.RecoveryPointExpired,
		backupasset.RecoveryPointFailed,
		backupasset.RecoveryPointPurgeBlocked,
	}
	for index, state := range states {
		seedManagedHistoryPoint(t, db, fmt.Sprintf("%032x", index+1), repository.ID, backupasset.PointNativeSnapshot, state, now.Add(time.Duration(index)*time.Second))
	}
	resolver, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	repositoryHistory, err := resolver.HasRepositoryManagedHistory(context.Background(), repository.ID)
	if err != nil || !repositoryHistory {
		t.Fatalf("repository managed history=%t err=%v, want true/nil", repositoryHistory, err)
	}
	installationHistory, err := resolver.HasInstallationManagedHistory(context.Background())
	if err != nil || !installationHistory {
		t.Fatalf("installation managed history=%t err=%v, want true/nil", installationHistory, err)
	}
}

func TestManagedHistoryLatchIsRepositoryScopedWhenBindingIsExact(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	managed := seedManagedHistoryRepository(t, db, strings.Repeat("b", 32), now)
	pristine := seedManagedHistoryRepository(t, db, strings.Repeat("c", 32), now.Add(time.Second))
	seedManagedHistoryPoint(t, db, strings.Repeat("d", 32), managed.ID, backupasset.PointNativeSnapshot, backupasset.RecoveryPointCommitted, now)
	resolver, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := resolver.HasRepositoryManagedHistory(context.Background(), managed.ID); err != nil || !got {
		t.Fatalf("managed repository history=%t err=%v, want true/nil", got, err)
	}
	if got, err := resolver.HasRepositoryManagedHistory(context.Background(), pristine.ID); err != nil || got {
		t.Fatalf("pristine repository history=%t err=%v, want false/nil", got, err)
	}
}

func TestManagedHistoryLatchTreatsFutureTombstoneAsPermanent(t *testing.T) {
	db := newRepositoryTestDB(t)
	tombstones := managedHistoryTombstoneFake{repository: true, installation: true}
	resolver, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db, Tombstones: tombstones})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := resolver.HasRepositoryManagedHistory(context.Background(), strings.Repeat("e", 32)); err != nil || !got {
		t.Fatalf("tombstone-only repository history=%t err=%v, want true/nil", got, err)
	}
	if got, err := resolver.HasInstallationManagedHistory(context.Background()); err != nil || !got {
		t.Fatalf("tombstone-only installation history=%t err=%v, want true/nil", got, err)
	}
}

func TestManagedHistoryLifecycleTombstonesProveHistoryWhenOnlyTombstonesRemain(t *testing.T) {
	db := newRepositoryTestDB(t)
	if err := db.AutoMigrate(&model.RecoveryPointLifecycleTombstone{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 13, 10, 0, 0, time.UTC)
	repositoryID := strings.Repeat("e", 32)
	if err := db.Create(&model.RecoveryPointLifecycleTombstone{
		RecoveryPointID: strings.Repeat("f", 32), RepositoryID: repositoryID,
		OriginalSemantics: string(backupasset.PointNativeSnapshot),
		TerminalOperation: string(backupasset.LifecycleRetentionExpire),
		TerminalState:     string(backupasset.RecoveryPointExpired),
		ManagedHistory:    true, ResultCode: "provider_deleted", CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed lifecycle tombstone: %v", err)
	}
	tombstones, err := NewLifecycleManagedHistoryTombstones(db)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db, Tombstones: tombstones})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := resolver.HasRepositoryManagedHistory(context.Background(), repositoryID); err != nil || !got {
		t.Fatalf("tombstone-only repository history=%t err=%v, want true/nil", got, err)
	}
	if got, err := resolver.HasInstallationManagedHistory(context.Background()); err != nil || !got {
		t.Fatalf("tombstone-only installation history=%t err=%v, want true/nil", got, err)
	}
	if _, err := NewLifecycleManagedHistoryTombstones(nil); err == nil {
		t.Fatal("nil tombstone source succeeded")
	}
}

func TestManagedHistoryLatchDoesNotTripFromMigrationOnlyOrMutableHead(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	repository := seedManagedHistoryRepository(t, db, strings.Repeat("f", 32), now)
	resolver, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := resolver.HasInstallationManagedHistory(context.Background()); err != nil || got {
		t.Fatalf("migration-only installation history=%t err=%v, want false/nil", got, err)
	}
	seedManagedHistoryPoint(t, db, strings.Repeat("1", 32), repository.ID, backupasset.PointMutableHead, backupasset.RecoveryPointObserved, now)
	if got, err := resolver.HasRepositoryManagedHistory(context.Background(), repository.ID); err != nil || got {
		t.Fatalf("mutable-head repository history=%t err=%v, want false/nil", got, err)
	}
}

func TestManagedHistoryLatchPersistsAfterRepositoryRowsAreRemoved(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	repository := seedManagedHistoryRepository(t, db, strings.Repeat("2", 32), now)
	pointID := strings.Repeat("3", 32)
	seedManagedHistoryPoint(t, db, pointID, repository.ID, backupasset.PointNativeSnapshot, backupasset.RecoveryPointCommitted, now)
	createManagedHistoryLatchFixtureTable(t, db)
	insertManagedHistoryLatchFixture(t, db, "installation", "", now)
	insertManagedHistoryLatchFixture(t, db, "repository", repository.ID, now)
	if err := db.Delete(&model.RecoveryPoint{}, "id = ?", pointID).Error; err != nil {
		t.Fatalf("delete managed RecoveryPoint: %v", err)
	}
	if err := db.Delete(&model.BackupRepository{}, "id = ?", repository.ID).Error; err != nil {
		t.Fatalf("delete managed repository: %v", err)
	}

	resolver, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := resolver.HasRepositoryManagedHistory(context.Background(), repository.ID); err != nil || !got {
		t.Fatalf("durable repository latch history=%t err=%v, want true/nil", got, err)
	}
	if got, err := resolver.HasInstallationManagedHistory(context.Background()); err != nil || !got {
		t.Fatalf("durable installation latch history=%t err=%v, want true/nil", got, err)
	}
}

func TestManagedHistoryResolverRecognizesRsyncManagedStatesAndParentLease(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	repository := seedManagedHistoryRepository(t, db, strings.Repeat("4", 32), now)
	for index, point := range []struct {
		semantics backupasset.PointVersionSemantics
		state     backupasset.RecoveryPointState
	}{
		{backupasset.PointXirangManifest, backupasset.RecoveryPointPreparing},
		{backupasset.PointImportedBaseline, backupasset.RecoveryPointFailed},
		{backupasset.PointXirangManifest, backupasset.RecoveryPointCommitted},
	} {
		seedManagedHistoryPoint(t, db, fmt.Sprintf("%032x", index+10), repository.ID, point.semantics, point.state, now.Add(time.Duration(index)*time.Second))
	}
	lease := model.RecoveryPointLease{
		ID: strings.Repeat("5", 32), RecoveryPointID: fmt.Sprintf("%032x", 10), HolderType: string(backupasset.LeaseHolderRsyncParent),
		OwnerID: "rsync-publication-worker", AttemptID: strings.Repeat("6", 32), FenceToken: strings.Repeat("7", 64),
		Status: string(backupasset.LeaseActive), LeaseExpiresAt: now.Add(time.Minute), AbsoluteDeadline: now.Add(time.Hour),
		LastHeartbeatAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&lease).Error; err != nil {
		t.Fatalf("create rsync parent lease: %v", err)
	}

	resolver, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := resolver.HasRepositoryManagedHistory(context.Background(), repository.ID); err != nil || !got {
		t.Fatalf("Rsync repository history=%t err=%v, want true/nil", got, err)
	}
	if got, err := resolver.HasInstallationManagedHistory(context.Background()); err != nil || !got {
		t.Fatalf("Rsync installation history=%t err=%v, want true/nil", got, err)
	}
	if got, err := resolver.HasActivePublicationLease(context.Background()); err != nil || !got {
		t.Fatalf("active rsync parent lease=%t err=%v, want true/nil", got, err)
	}
}

func TestManagedHistoryResolverBlocksActiveRcloneManagedLinkBeforeFirstPoint(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	task := seedTask(t, db, "rclone", "backup:legacy", `{"version":1,"publication_mode":"legacy_mutable"}`)
	identity := "rclone-managed-identity"
	repository := model.BackupRepository{
		ID: strings.Repeat("a", 32), ProviderKind: string(backupasset.ProviderRclone), VersionMode: string(backupasset.VersionVersionedPrefix),
		RepositoryIdentity: &identity, DisplayName: "rclone-managed",
		Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.TaskRepositoryLink{
		ID: strings.Repeat("b", 32), TaskID: &task.ID, RepositoryID: repository.ID, PublicationMode: string(backupasset.PublicationVersionedPrefix),
		EncryptedLegacyLocator: "backup:legacy", LinkedAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	resolver, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := resolver.legacyFallbackAllowed(context.Background(), task)
	if err != nil || allowed {
		t.Fatalf("active managed Rclone link fallback allowed=%t err=%v", allowed, err)
	}
}

func TestRcloneCleanRollbackWindowClosesAtFirstReservation(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 16, 8, 30, 0, 0, time.UTC)
	task := seedTask(t, db, "rclone", "backup:legacy", `{"version":1,"publication_mode":"legacy_mutable"}`)
	identity := "rclone-clean-rollback-identity"
	repository := model.BackupRepository{
		ID: strings.Repeat("c", 32), ProviderKind: string(backupasset.ProviderRclone),
		VersionMode: string(backupasset.VersionVersionedPrefix), RepositoryIdentity: &identity,
		DisplayName: "rclone-clean-rollback", Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1,
		CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.TaskRepositoryLink{
		ID: strings.Repeat("d", 32), TaskID: &task.ID, RepositoryID: repository.ID,
		PublicationMode: string(backupasset.PublicationVersionedPrefix), EncryptedLegacyLocator: "backup:legacy",
		LinkedAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	resolver, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	if available, err := resolver.rcloneCleanRollbackAvailable(context.Background(), repository.ID); err != nil || !available {
		t.Fatalf("zero-reservation clean rollback available=%t err=%v, want true/nil", available, err)
	}

	seedManagedHistoryPoint(t, db, strings.Repeat("e", 32), repository.ID, backupasset.PointXirangManifest, backupasset.RecoveryPointFailed, now)
	if available, err := resolver.rcloneCleanRollbackAvailable(context.Background(), repository.ID); err != nil || available {
		t.Fatalf("failed reservation clean rollback available=%t err=%v, want false/nil", available, err)
	}
}

func TestRcloneCleanRollbackWindowRejectsImportedBaselineLatchLeaseAndTombstone(t *testing.T) {
	for _, test := range []struct {
		name string
		seed func(*testing.T, *gorm.DB, model.BackupRepository, time.Time)
	}{
		{"imported baseline", func(t *testing.T, db *gorm.DB, repository model.BackupRepository, now time.Time) {
			seedManagedHistoryPoint(t, db, strings.Repeat("1", 32), repository.ID, backupasset.PointImportedBaseline, backupasset.RecoveryPointPreparing, now)
		}},
		{"repository latch", func(t *testing.T, db *gorm.DB, repository model.BackupRepository, now time.Time) {
			createManagedHistoryLatchFixtureTable(t, db)
			insertManagedHistoryLatchFixture(t, db, "repository", repository.ID, now)
		}},
		{"publication lease", func(t *testing.T, db *gorm.DB, repository model.BackupRepository, now time.Time) {
			pointID := strings.Repeat("2", 32)
			seedManagedHistoryPoint(t, db, pointID, repository.ID, backupasset.PointMutableHead, backupasset.RecoveryPointObserved, now)
			if err := db.Create(&model.RecoveryPointLease{
				ID: strings.Repeat("3", 32), RecoveryPointID: pointID,
				HolderType: string(backupasset.LeaseHolderPointPublication), OwnerID: "rclone-clean-window-test",
				AttemptID: strings.Repeat("4", 32), FenceToken: strings.Repeat("5", 64), Status: string(backupasset.LeaseActive),
				LeaseExpiresAt: now.Add(time.Minute), AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now,
				CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newRepositoryTestDB(t)
			now := time.Date(2026, 7, 16, 8, 45, 0, 0, time.UTC)
			identity := "rclone-clean-blocker-" + strings.ReplaceAll(test.name, " ", "-")
			repository := model.BackupRepository{
				ID: strings.Repeat("f", 32), ProviderKind: string(backupasset.ProviderRclone),
				VersionMode: string(backupasset.VersionNativeObjectVersions), RepositoryIdentity: &identity,
				DisplayName: "rclone-clean-blocker", Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1,
				CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
			}
			if err := db.Create(&repository).Error; err != nil {
				t.Fatal(err)
			}
			test.seed(t, db, repository, now)
			resolver, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
			if err != nil {
				t.Fatal(err)
			}
			if available, err := resolver.rcloneCleanRollbackAvailable(context.Background(), repository.ID); err != nil || available {
				t.Fatalf("blocked clean rollback available=%t err=%v, want false/nil", available, err)
			}
		})
	}
}

func TestManagedHistoryInstallationLatchAllowsOnlyExactPristineLegacyBinding(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	exactTask := seedTask(t, db, "rsync", t.TempDir(), "")
	connect := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, scopedObservationProber(backupasset.ProviderRsync))
	if _, err := connect.Connect(context.Background(), ConnectRequest{TaskID: exactTask.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	otherRepository := seedManagedHistoryRepository(t, db, strings.Repeat("6", 32), now)
	seedManagedHistoryPoint(t, db, strings.Repeat("7", 32), otherRepository.ID, backupasset.PointNativeSnapshot, backupasset.RecoveryPointCommitted, now)

	resolver, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	if allowed, err := resolver.legacyFallbackAllowed(context.Background(), exactTask); err != nil || !allowed {
		t.Fatalf("exact pristine legacy binding allowed=%t err=%v, want true/nil", allowed, err)
	}

	ambiguousTask := seedTask(t, db, "rsync", t.TempDir(), "")
	if allowed, err := resolver.legacyFallbackAllowed(context.Background(), ambiguousTask); err != nil || allowed {
		t.Fatalf("unlinked Task fallback allowed=%t err=%v, want false/nil", allowed, err)
	}
}

func TestManagedHistoryResolverBlocksManagedBindingWhenLegacyLinkDrifts(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, scopedObservationProber(backupasset.ProviderRsync))
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	var link model.TaskRepositoryLink
	if err := db.Where("task_id = ? AND unlinked_at IS NULL", taskEntity.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	managed := managedRsyncBindingDocumentV2{
		Version:                   managedRsyncBindingDocumentVersion,
		Provider:                  backupasset.ProviderRsync,
		IdentityClass:             provider.IdentityXirangManagedRepository,
		TaskID:                    taskEntity.ID,
		NodeID:                    taskEntity.NodeID,
		RepositoryID:              connected.Repository.ID,
		TaskRepositoryLinkID:      link.ID,
		LayoutRevision:            managedRsyncLayoutRevisionV1,
		ManagedRootLocator:        t.TempDir(),
		RootMarkerDigest:          strings.Repeat("a", 64),
		ManagedRootIdentityDigest: strings.Repeat("b", 64),
		PublicationMode:           backupasset.PublicationVersionedFullCopy,
		PreflightID:               strings.Repeat("c", 32),
		PreflightDigest:           strings.Repeat("d", 64),
		IdentitySalt:              strings.Repeat("42", provider.IdentitySaltBytes),
	}
	payload, err := encodeManagedRsyncBindingDocumentV2(managed)
	if err != nil {
		t.Fatal(err)
	}
	var binding model.RepositoryAccessBinding
	if err := db.Where("repository_id = ? AND status = ?", connected.Repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	binding.EncryptedConfig = payload
	if err := db.Save(&binding).Error; err != nil {
		t.Fatal(err)
	}

	resolver, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	if allowed, err := resolver.legacyFallbackAllowed(context.Background(), taskEntity); err != nil || allowed {
		t.Fatalf("managed binding fallback allowed=%t err=%v, want false/nil", allowed, err)
	}
}

type managedHistoryTombstoneFake struct {
	repository   bool
	installation bool
	err          error
}

func (fake managedHistoryTombstoneFake) HasRepositoryManagedHistory(_ context.Context, _ string) (bool, error) {
	return fake.repository, fake.err
}

func (fake managedHistoryTombstoneFake) HasInstallationManagedHistory(_ context.Context) (bool, error) {
	return fake.installation, fake.err
}

func seedManagedHistoryRepository(t *testing.T, db *gorm.DB, id string, now time.Time) model.BackupRepository {
	t.Helper()
	identity := "restic-native:v1:" + strings.Repeat(id[:1], 64)
	repository := model.BackupRepository{
		ID: id, ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &identity, DisplayName: "managed-history-" + id[:4],
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1,
		CapabilitiesJSON: `{"list":true,"open_sequential":true}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	return repository
}

func seedManagedHistoryPoint(t *testing.T, db *gorm.DB, id, repositoryID string, semantics backupasset.PointVersionSemantics, state backupasset.RecoveryPointState, now time.Time) {
	t.Helper()
	point := model.RecoveryPoint{
		ID: id, RepositoryID: repositoryID, Semantics: string(semantics), State: string(state),
		LineageJSON: `{}`, ConsistencyJSON: `{}`, FidelityJSON: `{}`, CapabilitiesJSON: `{}`,
		CapabilityRevision: 1, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
		PhysicalAvailability: string(backupasset.PhysicalUnknown), HoldState: string(backupasset.HoldNone), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
}

func createManagedHistoryLatchFixtureTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS backup_asset_managed_history_latches (
		id TEXT PRIMARY KEY,
		scope TEXT NOT NULL,
		repository_id TEXT,
		repository_identity_digest TEXT NOT NULL,
		first_semantics TEXT NOT NULL,
		first_origin TEXT NOT NULL,
		first_seen_at DATETIME NOT NULL,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create managed-history latch fixture table: %v", err)
	}
}

func insertManagedHistoryLatchFixture(t *testing.T, db *gorm.DB, scope, repositoryID string, now time.Time) {
	t.Helper()
	id := "managed-history-installation"
	var repositoryIDValue any
	identityDigest := ""
	if scope == "repository" {
		id = "managed-history-repository-" + repositoryID
		repositoryIDValue = repositoryID
		identityDigest = strings.Repeat("0", 32) + repositoryID
	}
	if err := db.Exec(`INSERT INTO backup_asset_managed_history_latches
		(id, scope, repository_id, repository_identity_digest, first_semantics, first_origin, first_seen_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'native_snapshot', 'migration_backfill', ?, ?, ?)`,
		id, scope, repositoryIDValue, identityDigest, now, now, now).Error; err != nil {
		t.Fatalf("insert %s managed-history latch fixture: %v", scope, err)
	}
}
