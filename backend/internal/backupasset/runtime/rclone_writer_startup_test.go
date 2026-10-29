package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

func TestRcloneHistoricalWriterBlocksStartupAndReenable(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		db := openRuntimeTestDB(t)
		prepareRuntimeIdentityFixture(t, db)
		if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.BackupRepository{}, &model.RepositoryAccessBinding{},
			&model.TaskRepositoryLink{}, &model.RecoveryPoint{}, &model.RecoveryPointLease{},
			&model.BackupAssetManagedHistoryLatch{}, &model.BackupAssetInstallation{},
			&model.BackupAssetInventoryRun{}, &model.BackupAssetRepositoryConflict{}); err != nil {
			t.Fatal(err)
		}
		runRcloneHistoricalWriterRuntime(t, db)
	})
	t.Run("postgres", func(t *testing.T) {
		db := newRuntimeLifecycleAcceptancePostgresDB(t)
		runRcloneHistoricalWriterRuntime(t, db)
	})
}

func runRcloneHistoricalWriterRuntime(t *testing.T, db *gorm.DB) {
	t.Helper()
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := settingsService.Update("backup_assets.content_cache_enabled", "false"); err != nil {
		t.Fatal(err)
	}
	transport := &runtimeTransportFake{}
	dependencies := Dependencies{DB: db, Settings: settingsService, Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{},
		ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{}}
	live, err := New(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if err := live.SetInterruptedRunReadiness(&runtimeReadinessRetentionInstaller{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Shutdown(context.Background()) })
	if err := live.StartupPass(context.Background()); err != nil {
		t.Fatalf("start blocked-enable runtime: %v", err)
	}
	now := time.Now().UTC()
	repository := model.BackupRepository{ID: strings.Repeat("a", 32), ProviderKind: string(backupasset.ProviderRclone), DisplayName: "historical",
		VersionMode: string(backupasset.VersionVersionedPrefix), Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1,
		CapabilitiesJSON: "{}", ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged), CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	point := model.RecoveryPoint{ID: strings.Repeat("b", 32), RepositoryID: repository.ID, Semantics: string(backupasset.PointImportedBaseline),
		State: string(backupasset.RecoveryPointFailed), LineageJSON: "{}", ConsistencyJSON: `{"version":1}`, FidelityJSON: "{}",
		ManifestDigestAlgorithm: "sha256", CapabilityRevision: 1, CapabilitiesJSON: "{}",
		ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged), PhysicalAvailability: string(backupasset.PhysicalUnknown),
		HoldState: string(backupasset.HoldNone), CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"version":1}`, `{"version":1,"rclone_writer_quiesced":false}`, `{"version":1,"future":true}`} {
		if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).Update("consistency_json", raw).Error; err != nil {
			t.Fatal(err)
		}
		persisted := false
		if err := live.TransitionFeature(context.Background(), true, func() error { persisted = true; return nil }); err == nil || persisted {
			t.Fatalf("reenable accepted historical writer evidence %s: %v", raw, err)
		}
		restarted, err := New(dependencies)
		if err != nil {
			t.Fatal(err)
		}
		if err := restarted.SetInterruptedRunReadiness(&runtimeReadinessRetentionInstaller{}); err != nil {
			t.Fatal(err)
		}
		if err := restarted.StartupPass(context.Background()); err == nil {
			t.Fatalf("startup accepted historical writer evidence %s", raw)
		}
		if _, err := restarted.admission.CurrentMode(); err == nil {
			t.Fatal("startup initialized admission before writer inventory")
		}
	}
	valid := `{"version":1,"rclone_writer_quiesced":true}`
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).Update("consistency_json", valid).Error; err != nil {
		t.Fatal(err)
	}
	restarted, err := New(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.SetInterruptedRunReadiness(&runtimeReadinessRetentionInstaller{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Shutdown(context.Background()) })
	if err := restarted.StartupPass(context.Background()); err != nil {
		t.Fatalf("positive writer evidence still blocked startup: %v", err)
	}
	var stored model.RecoveryPoint
	if err := db.First(&stored, "id = ?", point.ID).Error; err != nil || stored.State != string(backupasset.RecoveryPointFailed) || stored.ConsistencyJSON != valid {
		t.Fatal("startup rewrote historical point")
	}
}
