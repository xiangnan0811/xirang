package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

func TestDisasterRecoveryProviderFactsRebuildOnlyAfterAuthority(t *testing.T) {
	for _, fact := range []string{"recovery_point", "catalog", "overlay", "audit", "task_relationship", "binding", "wrapped_key"} {
		if _, err := backupasset.ClassifyDisasterRecoveryFact(fact); err != nil {
			t.Fatalf("classify %s: %v", fact, err)
		}
	}
	if _, err := backupasset.ClassifyDisasterRecoveryFact("provider_locator"); err == nil {
		t.Fatal("unknown disaster-recovery fact was admitted")
	}

	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	if err := db.AutoMigrate(
		&model.BackupRetentionPolicy{},
		&model.RecoveryPointHold{},
		&model.BackupAssetFavorite{},
		&model.BackupAssetSavedSearch{},
		&model.BackupAssetTagDefinition{},
		&model.BackupAssetRecentAccess{},
		&model.BackupAssetAuditEvent{},
	); err != nil {
		t.Fatal(err)
	}
	taskEntity := seedTask(t, db, "restic", "sftp:user@example.invalid:/repository", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	nativeRepositoryID := strings.Repeat("1", 64)
	nativeSnapshotID := strings.Repeat("2", 64)
	prober := &scriptedProber{observation: testObservation(backupasset.ProviderRestic, provider.NativeResticIdentityPrefix+nativeRepositoryID)}
	lister := &importPointListerSpy{page: provider.NativePointPage{Items: []provider.NativePoint{{
		OpaqueDigest:   nativeSnapshotID,
		CapturedAt:     time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC),
		Semantics:      backupasset.PointNativeSnapshot,
		SourceRevision: nativeSnapshotID,
		Locator:        provider.PointLocator{Native: nativeSnapshotID},
	}}}}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, provider.Registration{Prober: prober, PointLister: lister}); err != nil {
		t.Fatal(err)
	}
	catalogStarter := &rebuildCatalogStarterSpy{}
	derivedQueuer := &rebuildDerivedQueuerSpy{}
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: registry,
		Now:            func() time.Time { return time.Date(2026, 8, 17, 3, 5, 0, 0, time.UTC) },
		CatalogRebuild: catalogStarter, DerivedBackfill: derivedQueuer,
	})
	if err != nil {
		t.Fatal(err)
	}
	admin := RequestContext{Actor: backupasset.AuditActor{UserID: 1, Username: "dr-admin", Role: "admin"}}
	missingID := strings.Repeat("0", 32)
	if _, err := service.RebuildAcceptedImports(context.Background(), missingID, RebuildRequest{Limit: 10}, admin); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("rebuild on fresh control plane error=%v", err)
	}

	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, admin)
	if err != nil {
		t.Fatal(err)
	}
	discovered, err := service.DiscoverImportCandidates(context.Background(), connected.Repository.ID, ImportDiscoveryRequest{Limit: 10}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(discovered.Candidates) != 1 {
		t.Fatalf("discovered=%+v", discovered)
	}
	if _, err := service.ReviewImportCandidate(context.Background(), connected.Repository.ID, discovered.Candidates[0].ID, ImportReviewRequest{
		Decision: backupasset.ImportReviewAccepted,
		AcceptAs: backupasset.ImportCandidateNativeSnapshot,
	}, admin); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := service.RebuildAcceptedImports(context.Background(), connected.Repository.ID, RebuildRequest{Limit: 10}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.Accepted != 1 || rebuilt.CatalogStarted != 1 {
		t.Fatalf("authorized rebuild=%+v", rebuilt)
	}
	var pointCount int64
	if err := db.Model(&model.RecoveryPoint{}).Where("repository_id = ?", connected.Repository.ID).Count(&pointCount).Error; err != nil {
		t.Fatal(err)
	}
	if pointCount < 1 {
		t.Fatal("authorized rebuild did not persist a RecoveryPoint")
	}
	assertZeroControlPlaneRows(t, db)

	originalBinding := ""
	if err := db.Raw("SELECT encrypted_config FROM repository_access_bindings WHERE repository_id = ? AND status = ?", connected.Repository.ID, bindingStatusActive).Scan(&originalBinding).Error; err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_WRONG_DATA_ENCRYPTION_KEY_FOR_DR")
	secure.ResetForTesting()
	if _, err := service.Reconcile(context.Background(), connected.Repository.ID, admin); err == nil {
		t.Fatal("wrong encryption key silently reconciled")
	}
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, admin); err == nil {
		t.Fatal("wrong encryption key silently reconnected")
	}
	wrongKeyRebuild, err := service.RebuildAcceptedImports(context.Background(), connected.Repository.ID, RebuildRequest{Limit: 10}, admin)
	if err == nil {
		t.Fatalf("wrong encryption key claimed rebuild success=%+v", wrongKeyRebuild)
	}
	if len(catalogStarter.requests) != 1 {
		t.Fatalf("wrong key started extra catalog rebuilds: %d", len(catalogStarter.requests))
	}
	var mutatedBinding string
	if err := db.Raw("SELECT encrypted_config FROM repository_access_bindings WHERE repository_id = ? AND status = ?", connected.Repository.ID, bindingStatusActive).Scan(&mutatedBinding).Error; err != nil {
		t.Fatal(err)
	}
	if mutatedBinding != originalBinding {
		t.Fatal("wrong encryption key replaced the stored binding")
	}

	t.Setenv("DATA_ENCRYPTION_KEY", "")
	secure.ResetForTesting()
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, admin); err == nil {
		t.Fatal("missing encryption key silently reconnected")
	}
	if _, err := service.RebuildAcceptedImports(context.Background(), connected.Repository.ID, RebuildRequest{Limit: 10}, admin); err == nil {
		t.Fatal("missing encryption key claimed rebuild success")
	}
}

func assertZeroControlPlaneRows(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, item := range []struct {
		model any
		label string
	}{
		{&model.BackupRetentionPolicy{}, "policies"},
		{&model.RecoveryPointHold{}, "holds"},
		{&model.BackupAssetFavorite{}, "favorites"},
		{&model.BackupAssetSavedSearch{}, "saved searches"},
		{&model.BackupAssetTagDefinition{}, "tags"},
		{&model.BackupAssetRecentAccess{}, "recent access"},
		{&model.BackupAssetAuditEvent{}, "audit events"},
	} {
		var count int64
		if err := db.Model(item.model).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", item.label, err)
		}
		if count != 0 {
			t.Fatalf("provider rebuild invented %s rows=%d", item.label, count)
		}
	}
}
