package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
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
