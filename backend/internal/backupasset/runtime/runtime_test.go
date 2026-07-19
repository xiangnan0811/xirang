package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/overlay"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type runtimeTransportFake struct{ marker int }

func (*runtimeTransportFake) Run(context.Context, provider.CommandInvocation, provider.OperationLimits) (provider.CommandOutput, error) {
	return provider.CommandOutput{}, nil
}
func (*runtimeTransportFake) Open(context.Context, provider.CommandInvocation, provider.OperationLimits, int64) (provider.ReadHandle, error) {
	return nil, fmt.Errorf("not used")
}
func (*runtimeTransportFake) OpenExecution(context.Context, provider.CommandInvocation, provider.OperationLimits, int64) (provider.CommandExecution, error) {
	return &runtimeExecutionFake{Reader: strings.NewReader("")}, nil
}

type runtimeExecutionFake struct{ io.Reader }

func (*runtimeExecutionFake) Join() (provider.CommandCompletion, error) {
	return provider.CommandCompletion{ExitCode: 0, ExitCodeKnown: true}, nil
}
func (*runtimeExecutionFake) Cancel() error { return nil }

type runtimeStagedPayloadFake struct{}

func (*runtimeStagedPayloadFake) Stage(context.Context, provider.RemoteCommandAccess, provider.StagedPayloadRequest) (provider.StagedPayloadRef, error) {
	return provider.StagedPayloadRef{}, fmt.Errorf("not used")
}
func (*runtimeStagedPayloadFake) Cleanup(context.Context, provider.RemoteCommandAccess, provider.StagedPayloadRef) error {
	return nil
}
func (*runtimeStagedPayloadFake) CleanupAged(context.Context, provider.RemoteCommandAccess, time.Duration, int) error {
	return nil
}

var _ provider.CommandTransport = (*runtimeTransportFake)(nil)
var _ provider.CommandStreamTransport = (*runtimeTransportFake)(nil)

type runtimeSessionRevocationsFake struct {
	revoked bool
	err     error
}

func (fake *runtimeSessionRevocationsFake) IsSessionRevoked(string) (bool, error) {
	return fake.revoked, fake.err
}

type runtimeContentMetricsFake struct {
	content.NoopMetrics
	cache map[content.MetricCacheOutcome]int
}

func (fake *runtimeContentMetricsFake) ObserveCache(outcome content.MetricCacheOutcome) {
	fake.cache[outcome]++
}

func openRuntimeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&model.SystemSetting{}, &model.BackupAssetDeliveryGrant{}, &model.BackupAssetDeliveryRequest{},
		&model.BackupAssetDeliveryUsage{}, &model.RecoveryPointLease{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRuntimeSearchExposesOneRepositoryPublicationLineageAndWorkerGraph(t *testing.T) {
	db := openRuntimeTestDB(t)
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settings.NewService(db), Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{},
		Metrics:       publication.NoopMetrics{}, ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{},
		Now: func() time.Time { return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("construct runtime: %v", err)
	}
	if runtime.FoundationService() == nil || runtime.RepositoryService() == nil || runtime.PublicationCoordinator() == nil || runtime.healthWorker == nil ||
		runtime.PublicationReconciler() == nil || runtime.ResticPublicationStrategy() == nil ||
		runtime.RsyncTreePublicationStrategy() == nil || runtime.RclonePublicationStrategy() == nil ||
		runtime.LineageGuard() == nil || runtime.LegacyBlockRecorder() == nil || runtime.FeatureTransitioner() == nil ||
		runtime.CatalogService() == nil || runtime.CatalogAuditSink() == nil || runtime.catalogIndexer == nil || runtime.catalogWorker == nil {
		t.Fatal("runtime omitted a required shared graph port")
	}
	if runtime.SearchService() == nil || runtime.OverlayService() == nil || runtime.ContentIndexIngest() == nil || runtime.searchIndexer == nil || runtime.searchWorker == nil {
		t.Fatal("runtime omitted the Search/Overlay graph")
	}
	contentBroker := runtime.ContentBroker()
	if contentBroker == nil || contentBroker != runtime.contentBroker || runtime.contentManager == nil ||
		runtime.contentBudget == nil || runtime.contentAudit == nil || runtime.contentReconciler == nil || runtime.contentReady == nil {
		t.Fatal("runtime omitted or duplicated the Content graph")
	}
	if config, configErr := runtime.ContentConfig(); configErr != nil || config.Enabled {
		t.Fatalf("default Content config=%+v err=%v, want disabled", config, configErr)
	}
	if _, err := runtime.CatalogService().GetRecoveryPoint(context.Background(), strings.Repeat("f", 32), catalog.AuthorizationScope{Role: "admin", UserID: 1}); !errors.Is(err, catalog.ErrFeatureDisabled) {
		t.Fatalf("default-disabled runtime Catalog error=%v", err)
	}
	if runtime.ResticPublicationStrategy().Kind() != backupasset.ProviderRestic {
		t.Fatalf("publication strategy kind=%q, want %q", runtime.ResticPublicationStrategy().Kind(), backupasset.ProviderRestic)
	}
	if runtime.RsyncTreePublicationStrategy().Kind() != backupasset.ProviderRsync {
		t.Fatalf("publication strategy kind=%q, want %q", runtime.RsyncTreePublicationStrategy().Kind(), backupasset.ProviderRsync)
	}
	if runtime.RclonePublicationStrategy().Kind() != backupasset.ProviderRclone {
		t.Fatalf("publication strategy kind=%q, want %q", runtime.RclonePublicationStrategy().Kind(), backupasset.ProviderRclone)
	}
}

func TestRuntimeRejectsMismatchedTransportFacets(t *testing.T) {
	db := openRuntimeTestDB(t)
	_, err := New(Dependencies{
		DB: db, Settings: settings.NewService(db), Transport: &runtimeTransportFake{marker: 1}, StreamTransport: &runtimeTransportFake{marker: 2}, Metrics: publication.NoopMetrics{},
		ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{}, StagedPayload: &runtimeStagedPayloadFake{},
	})
	if err == nil {
		t.Fatal("runtime accepted distinct transport facets")
	}
}

func TestRuntimeStartupManagedModeRequiresInterruptedRunReadiness(t *testing.T) {
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.BackupRepository{}, &model.RepositoryAccessBinding{}, &model.TaskRepositoryLink{}, &model.RecoveryPoint{}, &model.RecoveryPointLease{}, &model.BackupAssetManagedHistoryLatch{}); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := settingsService.Update("backup_assets.content_cache_enabled", "false"); err != nil {
		t.Fatal(err)
	}
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settingsService, Transport: transport, StreamTransport: transport, StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{},
		ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.StartupPass(context.Background()); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("managed startup without TaskRun readiness error=%v, want invalid state", err)
	}
}

func TestRuntimeContentConstructionRequiresSessionRevocationSource(t *testing.T) {
	db := openRuntimeTestDB(t)
	transport := &runtimeTransportFake{}
	_, err := New(Dependencies{
		DB: db, Settings: settings.NewService(db), Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{}, ContentMetrics: content.NoopMetrics{},
	})
	if !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("runtime without session revocation source got %v, want invalid state", err)
	}
}

func TestRuntimeAuthenticatedCacheUsesSharedContentMetrics(t *testing.T) {
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.Task{}, &model.TaskRepositoryLink{}, &model.RepositoryAccessBinding{}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "asset-content-cache")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, strings.Repeat("a", 64)), []byte("old process generation"), 0o600); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.content_cache_root", root); err != nil {
		t.Fatal(err)
	}
	metrics := &runtimeContentMetricsFake{cache: make(map[content.MetricCacheOutcome]int)}
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settingsService, Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{}, ContentMetrics: metrics,
		SessionRevocations: &runtimeSessionRevocationsFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager, ok := runtime.contentManager.(*managedContentRuntime)
	if !ok {
		t.Fatalf("content manager type=%T", runtime.contentManager)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	if err := manager.PrepareEnable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.cache == nil || !manager.cache.Status().DiskEnabled {
		t.Fatalf("runtime cache status=%+v", manager.cache.Status())
	}
	if got := metrics.cache[content.MetricCacheKeyLoss]; got != 1 {
		t.Fatalf("runtime cache key-loss metric=%d, want 1", got)
	}
}

func TestRuntimeContentSessionValidatorChecksRevocationRoleVersionAndExpiry(t *testing.T) {
	now := time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	user := model.User{ID: 7, Username: "content-admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin", TokenVersion: 4}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	revocations := &runtimeSessionRevocationsFake{}
	validator, err := newRuntimeContentSessionValidator(db, revocations, func() time.Time { return now })
	if err != nil {
		t.Fatalf("newRuntimeContentSessionValidator: %v", err)
	}
	session := content.DeliverySession{
		JTI: strings.Repeat("a", 32), UserID: user.ID, Role: user.Role,
		TokenVersion: user.TokenVersion, ExpiresAt: now.Add(time.Minute),
	}
	if err := validator.Validate(context.Background(), session); err != nil {
		t.Fatalf("valid content session rejected: %v", err)
	}
	for name, mutate := range map[string]func(*content.DeliverySession){
		"expired":       func(value *content.DeliverySession) { value.ExpiresAt = now },
		"wrong role":    func(value *content.DeliverySession) { value.Role = "operator" },
		"wrong version": func(value *content.DeliverySession) { value.TokenVersion++ },
		"wrong user":    func(value *content.DeliverySession) { value.UserID++ },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := session
			mutate(&candidate)
			if err := validator.Validate(context.Background(), candidate); !errors.Is(err, backupasset.ErrForbidden) {
				t.Fatalf("invalid session got %v, want forbidden", err)
			}
		})
	}
	revocations.revoked = true
	if err := validator.Validate(context.Background(), session); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("revoked session got %v, want forbidden", err)
	}
}

func TestRuntimeContentAuthorizerBindsExactActiveCatalogAndCurrentOwnership(t *testing.T) {
	now := time.Date(2026, 7, 19, 2, 3, 4, 0, time.UTC)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.BackupRepository{}, &model.RecoveryPoint{}, &model.CatalogGeneration{}, &model.CatalogEntry{},
		&model.BackupAssetSearchGeneration{}, &model.BackupAssetSearchDocument{},
	); err != nil {
		t.Fatal(err)
	}
	repositoryID := strings.Repeat("b", 32)
	pointID := strings.Repeat("c", 32)
	generationID := strings.Repeat("d", 32)
	entryID := strings.Repeat("e", 64)
	sourceFingerprint := strings.Repeat("1", 64)
	entryFingerprint := strings.Repeat("2", 64)
	if err := db.Create(&model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRclone), DisplayName: "content-repository",
		VersionMode: string(backupasset.VersionVersionedPrefix), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 3, CapabilitiesJSON: `{"open_sequential":true,"open_range":true}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.RecoveryPoint{
		ID: pointID, RepositoryID: repositoryID, Semantics: string(backupasset.PointImportedBaseline),
		State: string(backupasset.RecoveryPointCommitted), SourceFingerprint: sourceFingerprint,
		CapabilityRevision: 3, CapabilitiesJSON: `{"open_sequential":true,"open_range":true}`,
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.CatalogGeneration{
		ID: generationID, RecoveryPointID: pointID, Generation: 1, State: string(catalog.GenerationComplete),
		IsActive: true, SourceFingerprint: sourceFingerprint, ExpectedEntryCount: 1, WrittenEntryCount: 1,
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	modified := now.Add(-time.Hour)
	if err := db.Create(&model.CatalogEntry{
		GenerationID: generationID, EntryID: entryID, RecoveryPointID: pointID,
		NormalizedPath: "/safe/report.pdf", Name: "report.pdf", EntryType: string(backupasset.CatalogEntryFile),
		Size: 4096, ModifiedAt: &modified, MimeType: "application/pdf", Fingerprint: entryFingerprint,
		FingerprintStrength: string(catalog.FingerprintStrong), SecurityState: "sealed", CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	searchGenerationID := strings.Repeat("3", 32)
	if err := db.Create(&model.BackupAssetSearchGeneration{
		ID: searchGenerationID, RecoveryPointID: pointID, CatalogGenerationID: generationID,
		Generation: 1, State: string(search.SearchGenerationComplete), IsActive: true,
		SourceFingerprint: sourceFingerprint, NormalizerVersion: search.NormalizerVersion,
		SearchKeyVersion: 1, ProjectionRevision: 1, LeaseID: strings.Repeat("4", 32),
		BuildAttemptID: strings.Repeat("5", 32), FenceTokenHash: strings.Repeat("6", 64),
		ExpectedDocumentCount: 1, WrittenDocumentCount: 1, StartedAt: now, FinishedAt: &now,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetSearchDocument{
		SearchGenerationID: searchGenerationID, DocumentID: entryID, RecoveryPointID: pointID,
		CatalogGenerationID: generationID, EntryID: entryID, Sensitivity: string(search.SensitivitySecret),
		ClassificationRevision: 7, MetadataRevision: 1, EntryType: string(backupasset.CatalogEntryFile),
		ModifiedAt: &modified, LineageToken: strings.Repeat("7", 64), PathGroupToken: strings.Repeat("8", 64),
		PathSortKey: "report.pdf", NameSortKey: "report.pdf", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	ownership, err := catalog.NewOwnership(db)
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := newRuntimeContentAuthorizer(db, ownership)
	if err != nil {
		t.Fatalf("newRuntimeContentAuthorizer: %v", err)
	}
	ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}
	actor := content.DeliveryActor{UserID: 1, Username: "admin", Role: "admin"}
	asset, err := authorizer.Authorize(context.Background(), actor, ref, content.DeliveryPreview)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if asset.Ref != ref || asset.CatalogGenerationID != generationID || asset.RepositoryID != repositoryID ||
		asset.Provider != backupasset.ProviderRclone || asset.SourceFingerprint != sourceFingerprint ||
		asset.EntryFingerprint != entryFingerprint || asset.FingerprintStrength != string(catalog.FingerprintStrong) ||
		asset.Size != 4096 || asset.MediaType != "application/pdf" || asset.Path != "/safe/report.pdf" ||
		asset.Name != "report.pdf" || !asset.RangeProven || asset.ModifiedAt == nil || !asset.ModifiedAt.Equal(modified) ||
		asset.SearchClassification != content.ClassificationSecret || asset.SearchClassificationRevision != 7 {
		t.Fatalf("authorized asset binding=%+v", asset)
	}
	assertNoSearchEvidence := func(t *testing.T) {
		t.Helper()
		current, err := authorizer.Authorize(context.Background(), actor, ref, content.DeliveryPreview)
		if err != nil {
			t.Fatalf("Authorize without usable Search evidence: %v", err)
		}
		if current.SearchClassification != "" || current.SearchClassificationRevision != 0 {
			t.Fatalf("incomplete Search evidence escaped: %+v", current)
		}
	}
	for name, testCase := range map[string]struct {
		mutate  func(*testing.T)
		restore func(*testing.T)
	}{
		"unfinished generation": {
			mutate: func(t *testing.T) {
				if err := db.Model(&model.BackupAssetSearchGeneration{}).Where("id = ?", searchGenerationID).
					Update("finished_at", nil).Error; err != nil {
					t.Fatal(err)
				}
			},
			restore: func(t *testing.T) {
				if err := db.Model(&model.BackupAssetSearchGeneration{}).Where("id = ?", searchGenerationID).
					Update("finished_at", now).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		"document count mismatch": {
			mutate: func(t *testing.T) {
				if err := db.Model(&model.BackupAssetSearchGeneration{}).Where("id = ?", searchGenerationID).
					Update("expected_document_count", 2).Error; err != nil {
					t.Fatal(err)
				}
			},
			restore: func(t *testing.T) {
				if err := db.Model(&model.BackupAssetSearchGeneration{}).Where("id = ?", searchGenerationID).
					Update("expected_document_count", 1).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		"source mismatch": {
			mutate: func(t *testing.T) {
				if err := db.Model(&model.BackupAssetSearchGeneration{}).Where("id = ?", searchGenerationID).
					Update("source_fingerprint", "stale-source").Error; err != nil {
					t.Fatal(err)
				}
			},
			restore: func(t *testing.T) {
				if err := db.Model(&model.BackupAssetSearchGeneration{}).Where("id = ?", searchGenerationID).
					Update("source_fingerprint", sourceFingerprint).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		"inactive generation": {
			mutate: func(t *testing.T) {
				if err := db.Model(&model.BackupAssetSearchGeneration{}).Where("id = ?", searchGenerationID).
					Update("is_active", false).Error; err != nil {
					t.Fatal(err)
				}
			},
			restore: func(t *testing.T) {
				if err := db.Model(&model.BackupAssetSearchGeneration{}).Where("id = ?", searchGenerationID).
					Update("is_active", true).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		"non-exact document": {
			mutate: func(t *testing.T) {
				if err := db.Model(&model.BackupAssetSearchDocument{}).
					Where("search_generation_id = ? AND document_id = ?", searchGenerationID, entryID).
					Update("document_id", strings.Repeat("9", 64)).Error; err != nil {
					t.Fatal(err)
				}
			},
			restore: func(t *testing.T) {
				if err := db.Model(&model.BackupAssetSearchDocument{}).
					Where("search_generation_id = ?", searchGenerationID).
					Update("document_id", entryID).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run("ignores "+name, func(t *testing.T) {
			testCase.mutate(t)
			t.Cleanup(func() { testCase.restore(t) })
			assertNoSearchEvidence(t)
		})
	}
	if _, err := authorizer.Authorize(context.Background(), content.DeliveryActor{UserID: 2, Role: "operator"}, ref, content.DeliveryDownload); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("operator download got %v, want forbidden", err)
	}
	if _, err := authorizer.Authorize(context.Background(), content.DeliveryActor{UserID: 3, Role: "viewer"}, ref, content.DeliveryPreview); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("viewer preview got %v, want forbidden", err)
	}
	t.Run("repository drift", func(t *testing.T) {
		replacementRepositoryID := strings.Repeat("a", 32)
		if err := db.Create(&model.BackupRepository{
			ID: replacementRepositoryID, ProviderKind: string(backupasset.ProviderRclone), DisplayName: "replacement-content-repository",
			VersionMode: string(backupasset.VersionVersionedPrefix), Status: string(backupasset.RepositoryOnline),
			CapabilityRevision: 3, CapabilitiesJSON: `{"open_sequential":true,"open_range":true}`,
			ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).
			Update("repository_id", replacementRepositoryID).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).
				Update("repository_id", repositoryID).Error; err != nil {
				t.Fatal(err)
			}
		})
		if err := authorizer.Reauthorize(context.Background(), actor, asset, content.DeliveryPreview); !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("repository drift error=%v, want conflict", err)
		}
	})
	for name, update := range map[string]map[string]any{
		"Search classification": {"sensitivity": string(search.SensitivityNonSecret), "classification_revision": 8},
		"path":                  {"normalized_path": "/safe/renamed/report.pdf"},
		"name":                  {"name": "renamed.pdf"},
		"media type":            {"mime_type": "application/octet-stream"},
	} {
		t.Run(name+" drift", func(t *testing.T) {
			if name == "Search classification" {
				if err := db.Model(&model.BackupAssetSearchDocument{}).
					Where("search_generation_id = ? AND document_id = ?", searchGenerationID, entryID).
					Updates(update).Error; err != nil {
					t.Fatal(err)
				}
			} else if err := db.Model(&model.CatalogEntry{}).
				Where("generation_id = ? AND entry_id = ?", generationID, entryID).Updates(update).Error; err != nil {
				t.Fatal(err)
			}
			if err := authorizer.Reauthorize(context.Background(), actor, asset, content.DeliveryPreview); !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("reauthorization drift error=%v, want conflict", err)
			}
			if name == "Search classification" {
				if err := db.Model(&model.BackupAssetSearchDocument{}).
					Where("search_generation_id = ? AND document_id = ?", searchGenerationID, entryID).
					Updates(map[string]any{"sensitivity": string(search.SensitivitySecret), "classification_revision": 7}).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				restore := map[string]any{"normalized_path": "/safe/report.pdf", "name": "report.pdf", "mime_type": "application/pdf"}
				if err := db.Model(&model.CatalogEntry{}).
					Where("generation_id = ? AND entry_id = ?", generationID, entryID).Updates(restore).Error; err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	if err := db.Model(&model.CatalogGeneration{}).Where("id = ?", generationID).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Reauthorize(context.Background(), actor, asset, content.DeliveryPreview); err == nil {
		t.Fatal("reauthorization accepted an inactive Catalog generation")
	}
}

type runtimeContentManagerFake struct {
	events            *[]string
	prepareEnableErr  error
	prepareDisableErr error
	shutdownErr       error
	runStarted        chan struct{}
}

func (fake *runtimeContentManagerFake) Startup(context.Context) error {
	*fake.events = append(*fake.events, "content-startup")
	return nil
}

func (fake *runtimeContentManagerFake) PrepareEnable(context.Context) error {
	*fake.events = append(*fake.events, "content-prepare-enable")
	return fake.prepareEnableErr
}

func (fake *runtimeContentManagerFake) PrepareDisable(context.Context) error {
	*fake.events = append(*fake.events, "content-prepare-disable")
	return fake.prepareDisableErr
}

func (fake *runtimeContentManagerFake) SetReady(ready bool) {
	*fake.events = append(*fake.events, fmt.Sprintf("content-ready-%t", ready))
}

func (fake *runtimeContentManagerFake) StopAccepting() {
	*fake.events = append(*fake.events, "content-stop-accepting")
}

func (fake *runtimeContentManagerFake) Run(ctx context.Context) {
	*fake.events = append(*fake.events, "content-run")
	if fake.runStarted != nil {
		close(fake.runStarted)
	}
	<-ctx.Done()
}

func (fake *runtimeContentManagerFake) Shutdown(context.Context) error {
	*fake.events = append(*fake.events, "content-shutdown")
	return fake.shutdownErr
}

func (fake *runtimeContentManagerFake) PrepareSchemaDown(_ context.Context, down func() error) error {
	*fake.events = append(*fake.events, "content-schema-drain")
	return down()
}

type runtimeFeatureTransitionerFake struct{ events *[]string }

func (fake *runtimeFeatureTransitionerFake) TransitionFeature(_ context.Context, enabled bool, persist func() error) error {
	*fake.events = append(*fake.events, fmt.Sprintf("admission-transition-%t", enabled))
	return persist()
}

func (fake *runtimeFeatureTransitionerFake) PrepareApplicationDowngrade(_ context.Context, callback func() error) error {
	*fake.events = append(*fake.events, "admission-app-downgrade")
	return callback()
}

func (fake *runtimeFeatureTransitionerFake) PrepareSchemaDown(_ context.Context, callback func() error) error {
	*fake.events = append(*fake.events, "admission-schema-down")
	return callback()
}

func TestRuntimeContentTransitionAndSchemaDownOrdering(t *testing.T) {
	events := []string{}
	manager := &runtimeContentManagerFake{events: &events}
	transitioner := &runtimeFeatureTransitionerFake{events: &events}
	runtime := &Runtime{contentManager: manager, transitioner: transitioner}
	if runtime.FeatureTransitioner() != runtime {
		t.Fatal("runtime did not expose the composed Content feature transitioner")
	}
	if err := runtime.TransitionFeature(context.Background(), true, func() error {
		events = append(events, "persist-enabled")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.TransitionFeature(context.Background(), false, func() error {
		events = append(events, "persist-disabled")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.PrepareSchemaDown(context.Background(), func() error {
		events = append(events, "schema-down")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"content-prepare-enable", "admission-transition-true", "persist-enabled", "content-ready-true",
		"content-ready-false", "content-prepare-disable", "admission-transition-false", "persist-disabled",
		"content-ready-false", "content-schema-drain", "admission-schema-down", "schema-down",
	}
	if fmt.Sprint(events) != fmt.Sprint(want) {
		t.Fatalf("content transition order=%v, want %v", events, want)
	}
}

func TestRuntimeContentTransitionRestoresLifecycleAfterPersistenceFailure(t *testing.T) {
	persistErr := errors.New("FAKE_CONTENT_PERSIST_FAILURE_FOR_TEST_ONLY")
	t.Run("failed enable drains provisional content runtime", func(t *testing.T) {
		events := []string{}
		manager := &runtimeContentManagerFake{events: &events}
		runtime := &Runtime{contentManager: manager, transitioner: &runtimeFeatureTransitionerFake{events: &events}}
		err := runtime.TransitionFeature(context.Background(), true, func() error {
			events = append(events, "persist-enabled")
			return persistErr
		})
		if !errors.Is(err, persistErr) {
			t.Fatalf("enable persistence error=%v", err)
		}
		want := []string{
			"content-prepare-enable", "admission-transition-true", "persist-enabled",
			"content-ready-false", "content-prepare-disable",
		}
		if fmt.Sprint(events) != fmt.Sprint(want) {
			t.Fatalf("failed enable lifecycle=%v, want %v", events, want)
		}
	})

	t.Run("failed disable restores content runtime", func(t *testing.T) {
		events := []string{}
		manager := &runtimeContentManagerFake{events: &events}
		runtime := &Runtime{contentManager: manager, transitioner: &runtimeFeatureTransitionerFake{events: &events}}
		err := runtime.TransitionFeature(context.Background(), false, func() error {
			events = append(events, "persist-disabled")
			return persistErr
		})
		if !errors.Is(err, persistErr) {
			t.Fatalf("disable persistence error=%v", err)
		}
		want := []string{
			"content-ready-false", "content-prepare-disable", "admission-transition-false", "persist-disabled",
			"content-prepare-enable", "content-ready-true",
		}
		if fmt.Sprint(events) != fmt.Sprint(want) {
			t.Fatalf("failed disable lifecycle=%v, want %v", events, want)
		}
	})

	t.Run("failed disable and failed restore remain not ready", func(t *testing.T) {
		events := []string{}
		restoreErr := errors.New("FAKE_CONTENT_RESTORE_FAILURE_FOR_TEST_ONLY")
		manager := &runtimeContentManagerFake{events: &events, prepareEnableErr: restoreErr}
		runtime := &Runtime{contentManager: manager, transitioner: &runtimeFeatureTransitionerFake{events: &events}}
		err := runtime.TransitionFeature(context.Background(), false, func() error { return persistErr })
		if !errors.Is(err, persistErr) || !errors.Is(err, restoreErr) {
			t.Fatalf("joined disable/restore error=%v", err)
		}
		if got := events[len(events)-1]; got != "content-ready-false" {
			t.Fatalf("failed restore final lifecycle=%v", events)
		}
	})
}

func TestRuntimeContentStopRunAndShutdownAreAlwaysJoined(t *testing.T) {
	events := []string{}
	shutdownErr := errors.New("FAKE_CONTENT_SHUTDOWN_FAILURE_FOR_TEST_ONLY")
	manager := &runtimeContentManagerFake{events: &events, shutdownErr: shutdownErr, runStarted: make(chan struct{})}
	runtime := &Runtime{contentManager: manager}
	runtime.StopAccepting()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runtime.Run(ctx)
		close(done)
	}()
	<-manager.runStarted
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runtime did not join Content Run after cancellation")
	}
	if err := runtime.Shutdown(context.Background()); !errors.Is(err, shutdownErr) {
		t.Fatalf("runtime shutdown got %v, want Content error", err)
	}
	if fmt.Sprint(events) != fmt.Sprint([]string{"content-stop-accepting", "content-run", "content-stop-accepting", "content-shutdown"}) {
		t.Fatalf("content lifecycle events=%v", events)
	}
}

func TestRuntimeSearchStartupDisabledTouchesNoKeyOrWorker(t *testing.T) {
	db := openRuntimeTestDB(t)
	backend := newSearchWorkerBackendFake()
	worker, err := NewSearchWorker(SearchWorkerDependencies{
		Config: func() (SearchWorkerConfig, error) {
			return SearchWorkerConfig{Enabled: true, ReconcileInterval: time.Minute, ReconcileBatchSize: 10, WorkerConcurrency: 1}, nil
		},
		Backend: backend,
	})
	if err != nil {
		t.Fatalf("NewSearchWorker: %v", err)
	}
	runtime := &Runtime{
		foundation:   backupasset.NewFoundationService(settings.NewService(db)),
		keyring:      backupasset.NewKeyring(db, nil),
		searchWorker: worker,
	}
	if err := runtime.startupSearch(context.Background()); err != nil {
		t.Fatalf("disabled Search startup: %v", err)
	}
	if backend.calls() != (searchWorkerCalls{}) {
		t.Fatalf("disabled Search startup touched worker backend: %+v", backend.calls())
	}
	if db.Migrator().HasTable(&model.WrappedDomainKey{}) {
		t.Fatal("disabled Search startup created or required the key table")
	}
}

func TestRuntimeSearchStartupEnsuresKeyReconcilesAndTreatsRecordedLossAsUnavailable(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RUNTIME_SEARCH_KEK_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.WrappedDomainKey{}); err != nil {
		t.Fatalf("migrate wrapped keys: %v", err)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.enabled", "true"); err != nil {
		t.Fatalf("enable backup assets: %v", err)
	}
	backend := newSearchWorkerBackendFake()
	worker, err := NewSearchWorker(SearchWorkerDependencies{
		Config: func() (SearchWorkerConfig, error) {
			return SearchWorkerConfig{Enabled: true, ReconcileInterval: time.Minute, ReconcileBatchSize: 10, WorkerConcurrency: 1}, nil
		},
		Backend: backend,
	})
	if err != nil {
		t.Fatalf("NewSearchWorker: %v", err)
	}
	ring := backupasset.NewKeyring(db, nil)
	runtime := &Runtime{foundation: backupasset.NewFoundationService(settingsService), keyring: ring, searchWorker: worker}
	if err := runtime.startupSearch(context.Background()); err != nil {
		t.Fatalf("enabled Search startup: %v", err)
	}
	material, err := ring.Active(context.Background(), backupasset.KeyDomainSearchToken)
	if err != nil || material.Version != 1 {
		t.Fatalf("enabled startup did not ensure Search key: material=%+v err=%v", material, err)
	}
	if calls := backend.calls(); calls.reconcile != 1 || calls.list != 1 {
		t.Fatalf("enabled startup did not reconcile Search: %+v", calls)
	}

	if err := ring.MarkRebuildableLost(context.Background(), backupasset.KeyDomainSearchToken, material.Version, func(context.Context, *gorm.DB, backupasset.RebuildableKeyTransition) error {
		return nil
	}); err != nil {
		t.Fatalf("record Search key loss: %v", err)
	}
	before := backend.calls()
	if err := runtime.startupSearch(context.Background()); err != nil {
		t.Fatalf("intentional Search key loss should preserve Catalog runtime: %v", err)
	}
	if after := backend.calls(); after != before {
		t.Fatalf("lost Search key still ran worker: before=%+v after=%+v", before, after)
	}
	if _, err := ring.Active(context.Background(), backupasset.KeyDomainSearchToken); !errors.Is(err, backupasset.ErrKeyLost) {
		t.Fatalf("lost Search key was regenerated: %v", err)
	}
}

func TestRuntimeSearchStartupUnexpectedUnwrapFailureIsFatal(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RUNTIME_SEARCH_OLD_KEK_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.WrappedDomainKey{}); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	ring := backupasset.NewKeyring(db, nil)
	if _, err := ring.Ensure(context.Background(), backupasset.KeyDomainSearchToken); err != nil {
		t.Fatalf("seed Search key: %v", err)
	}
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RUNTIME_SEARCH_NEW_KEK_FOR_TEST_ONLY")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	worker, _ := NewSearchWorker(SearchWorkerDependencies{
		Config: func() (SearchWorkerConfig, error) {
			return SearchWorkerConfig{Enabled: true, ReconcileInterval: time.Minute, ReconcileBatchSize: 10, WorkerConcurrency: 1}, nil
		},
		Backend: newSearchWorkerBackendFake(),
	})
	runtime := &Runtime{foundation: backupasset.NewFoundationService(settingsService), keyring: ring, searchWorker: worker}
	if err := runtime.startupSearch(context.Background()); !errors.Is(err, backupasset.ErrKeyUnavailable) {
		t.Fatalf("unexpected Search unwrap failure got %v, want fatal key unavailable", err)
	}
}

func TestRuntimeSearchTokenOperationsCoordinateInvalidationReadinessAndLoss(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RUNTIME_SEARCH_OPERATIONS_KEK_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.WrappedDomainKey{}, &model.BackupAssetSearchGeneration{}, &model.BackupAssetTagDefinition{},
	); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	ring := backupasset.NewKeyring(db, nil)
	before, err := ring.Ensure(context.Background(), backupasset.KeyDomainSearchToken)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := search.NormalizeFieldV1(search.SearchFieldTag, "finance", search.DefaultNormalizerLimits())
	if err != nil {
		t.Fatal(err)
	}
	token, err := search.TokenHMAC(before.Key, before.Version, search.NormalizerVersion, search.SearchFieldTag, search.TokenKindExact, normalized.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.Create(&model.BackupAssetTagDefinition{
		ID: strings.Repeat("a", 32), OwnerUserID: 1, EncryptedName: normalized.Canonical,
		NameToken: token, KeyVersion: before.Version, TokenState: "active", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	authorizer := runtimeOverlayAuthorizationAllowAll{}
	overlays, err := overlay.NewService(overlay.ServiceDependencies{
		DB: db, Keys: ring, Assets: authorizer, Points: authorizer, Config: overlay.DefaultConfig(),
		FeatureEnabled: func() (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ready := &atomic.Bool{}
	ready.Store(true)
	runtime := &Runtime{
		foundation: backupasset.NewFoundationService(settingsService), keyring: ring,
		overlayService: overlays, searchReady: ready,
	}
	after, err := runtime.ReplaceSearchTokenForReindex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != before.Version+1 || !ready.Load() {
		t.Fatalf("replacement material=%+v ready=%t", after, ready.Load())
	}
	var tag model.BackupAssetTagDefinition
	if err := db.Where("id = ?", strings.Repeat("a", 32)).Take(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if tag.TokenState != "rekeying" || tag.KeyVersion != before.Version {
		t.Fatalf("replacement did not gate old tag token: %+v", tag)
	}
	if err := runtime.MarkSearchTokenLost(context.Background(), after.Version); err != nil {
		t.Fatal(err)
	}
	if ready.Load() {
		t.Fatal("recorded Search Token loss left worker readiness enabled")
	}
	if _, err := ring.Active(context.Background(), backupasset.KeyDomainSearchToken); !errors.Is(err, backupasset.ErrKeyLost) {
		t.Fatalf("active Search Token after recorded loss: %v", err)
	}
}

type runtimeOverlayAuthorizationAllowAll struct{}

func (runtimeOverlayAuthorizationAllowAll) AuthorizeAsset(context.Context, *gorm.DB, overlay.Actor, backupasset.AssetRef) error {
	return nil
}

func (runtimeOverlayAuthorizationAllowAll) AuthorizePoints(context.Context, overlay.Actor, []string) error {
	return nil
}

func TestRuntimeSearchShutdownStopsAdmissionAndJoinsSearchBeforePublication(t *testing.T) {
	fixture := newAdmissionControllerFixture(t, true, nil)
	fixture.initialize(t)
	searchBackend := newSearchWorkerBackendFake()
	searchBackend.candidates = []search.BuildCandidate{{RepositoryID: "repo-a", RecoveryPointID: "point-a"}}
	searchWorker, err := NewSearchWorker(SearchWorkerDependencies{
		Config: func() (SearchWorkerConfig, error) {
			return SearchWorkerConfig{Enabled: true, ReconcileInterval: time.Hour, ReconcileBatchSize: 10, WorkerConcurrency: 1}, nil
		},
		Backend: searchBackend,
	})
	if err != nil {
		t.Fatal(err)
	}
	searchCtx, searchCancel := context.WithCancel(context.Background())
	t.Cleanup(searchCancel)
	go searchWorker.Run(searchCtx)
	_ = searchBackend.waitStarted(t)
	searchActiveAtPublicationCancel := make(chan int, 1)
	reconciler := &shutdownOrderReconciler{
		started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}),
		beforeCanceled: func() { searchActiveAtPublicationCancel <- searchBackend.active() },
	}
	worker, err := NewPublicationWorker(PublicationWorkerDependencies{
		Foundation: fixture.controller.foundation, Reconciler: reconciler, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	pointID := strings.Repeat("a", 32)
	go worker.process(context.Background(), pointID)
	select {
	case <-reconciler.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not begin shutdown-order fixture work")
	}
	runtime := &Runtime{admission: fixture.controller, worker: worker, searchWorker: searchWorker}
	done := make(chan error, 1)
	go func() { done <- runtime.Shutdown(context.Background()) }()
	select {
	case <-reconciler.canceled:
	case <-time.After(time.Second):
		t.Fatal("runtime shutdown did not cancel active worker work")
	}
	if active := <-searchActiveAtPublicationCancel; active != 0 {
		close(reconciler.release)
		<-done
		t.Fatalf("runtime canceled publication before joining Search work: active=%d", active)
	}
	token, acquireErr := fixture.controller.Acquire(context.Background(), publication.OperationManifest)
	if token != nil {
		_ = token.Close()
	}
	if !errors.Is(acquireErr, ErrAdmissionStopped) {
		close(reconciler.release)
		<-done
		t.Fatalf("shutdown admitted a new publication token after worker cancellation: %v", acquireErr)
	}
	close(reconciler.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type shutdownOrderReconciler struct {
	started        chan struct{}
	canceled       chan struct{}
	release        chan struct{}
	beforeCanceled func()
}

func (*shutdownOrderReconciler) ListCandidates(context.Context, int) ([]string, error) {
	return nil, nil
}
func (reconciler *shutdownOrderReconciler) ProcessPoint(ctx context.Context, pointID string) (publication.Outcome, error) {
	close(reconciler.started)
	<-ctx.Done()
	if reconciler.beforeCanceled != nil {
		reconciler.beforeCanceled()
	}
	close(reconciler.canceled)
	<-reconciler.release
	return publication.Outcome{RecoveryPointID: pointID}, nil
}
func (*shutdownOrderReconciler) HasUnresolvedPublication(context.Context) (bool, error) {
	return false, nil
}
