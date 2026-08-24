package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/ga"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestTransitionFeatureBlockedReadinessDoesNotBecomeManaged(t *testing.T) {
	events := []string{}
	persisted := false
	runtime := EnablementRuntime(gaStaticReadiness{snapshot: ga.ReadinessSnapshot{
		Class:             ga.InstallationFresh,
		Status:            ga.ReadinessBlocked,
		InventoryComplete: false,
	}}, &runtimeFeatureTransitionerFake{events: &events})
	runtime.contentManager = &runtimeContentManagerFake{events: &events}

	err := runtime.TransitionFeature(context.Background(), true, func() error {
		persisted = true
		return nil
	})
	if !errors.Is(err, ga.ErrEnablementBlocked) {
		t.Fatalf("error=%v, want ErrEnablementBlocked", err)
	}
	if persisted || stringsContain(events, "admission-transition-true") || stringsContain(events, "content-prepare-enable") {
		t.Fatalf("blocked enablement became managed persist=%t events=%v", persisted, events)
	}
}

func TestTransitionFeatureFreshReadyWithoutAckEnables(t *testing.T) {
	events := []string{}
	persisted := false
	runtime := EnablementRuntime(gaStaticReadiness{snapshot: ga.ReadinessSnapshot{
		Class:             ga.InstallationFresh,
		Status:            ga.ReadinessReady,
		InventoryComplete: true,
		InventoryDigest:   "fresh-digest",
		ExportRootValid:   true,
		KeyDomainsReady:   true,
	}}, &runtimeFeatureTransitionerFake{events: &events})
	runtime.contentManager = &runtimeContentManagerFake{events: &events}

	if err := runtime.TransitionFeature(context.Background(), true, func() error {
		persisted = true
		return nil
	}); err != nil {
		t.Fatalf("fresh ready enablement: %v", err)
	}
	if !persisted || !stringsContain(events, "admission-transition-true") {
		t.Fatalf("fresh ready persist=%t events=%v", persisted, events)
	}
}

func TestTransitionFeatureExistingInstallRequiresCurrentAck(t *testing.T) {
	events := []string{}
	runtime := EnablementRuntime(gaStaticReadiness{snapshot: ga.ReadinessSnapshot{
		Class:             ga.InstallationExisting,
		Status:            ga.ReadinessReady,
		InventoryComplete: true,
		InventoryDigest:   "current-digest",
		ExportRootValid:   true,
		KeyDomainsReady:   true,
	}}, &runtimeFeatureTransitionerFake{events: &events})
	runtime.contentManager = &runtimeContentManagerFake{events: &events}

	err := runtime.TransitionFeature(context.Background(), true, func() error { return nil })
	if !errors.Is(err, ga.ErrEnablementAckRequired) {
		t.Fatalf("error=%v, want ErrEnablementAckRequired", err)
	}
	if stringsContain(events, "admission-transition-true") {
		t.Fatalf("existing without ack became managed events=%v", events)
	}

	events = events[:0]
	acked := EnablementRuntime(gaStaticReadiness{snapshot: ga.ReadinessSnapshot{
		Class:              ga.InstallationExisting,
		Status:             ga.ReadinessAcknowledged,
		InventoryComplete:  true,
		InventoryDigest:    "current-digest",
		AcknowledgedDigest: "current-digest",
		ExportRootValid:    true,
		KeyDomainsReady:    true,
	}}, &runtimeFeatureTransitionerFake{events: &events})
	acked.contentManager = &runtimeContentManagerFake{events: &events}
	if err := acked.TransitionFeature(context.Background(), true, func() error { return nil }); err != nil {
		t.Fatalf("acked existing enablement: %v", err)
	}
	if !stringsContain(events, "admission-transition-true") {
		t.Fatalf("acked existing events=%v", events)
	}
}

func TestTransitionFeatureDisablementUsesExistingDrain(t *testing.T) {
	events := []string{}
	runtime := EnablementRuntime(gaStaticReadiness{snapshot: ga.ReadinessSnapshot{Status: ga.ReadinessBlocked}}, &runtimeFeatureTransitionerFake{events: &events})
	runtime.contentManager = &runtimeContentManagerFake{events: &events}

	if err := runtime.TransitionFeature(context.Background(), false, func() error {
		events = append(events, "persist-disabled")
		return nil
	}); err != nil {
		t.Fatalf("disablement: %v", err)
	}
	if !stringsContain(events, "admission-transition-false") || !stringsContain(events, "content-prepare-disable") {
		t.Fatalf("disablement skipped drain events=%v", events)
	}
}

func TestFeatureLiveRequestedTrueWithoutAckIsClosed(t *testing.T) {
	runtime := EnablementRuntime(gaStaticReadiness{snapshot: ga.ReadinessSnapshot{
		Class:             ga.InstallationExisting,
		Status:            ga.ReadinessReady,
		InventoryComplete: true,
		InventoryDigest:   "current-digest",
		ExportRootValid:   true,
		KeyDomainsReady:   true,
	}}, nil)
	runtime.foundation = backupasset.NewFoundationService(runtimeFoundationSettings(true))

	live, err := runtime.FeatureLive()
	if err != nil {
		t.Fatalf("FeatureLive: %v", err)
	}
	if live {
		t.Fatal("requested true without ack must not be live")
	}
	requested, err := runtime.foundation.FeatureEnabled()
	if err != nil || !requested {
		t.Fatalf("requested setting=%t err=%v, want true", requested, err)
	}
}

func TestRequestedSearchConfigEnabledStaysClosedWithoutAck(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	db := openRuntimeTestDB(t)
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	runtime := EnablementRuntime(gaStaticReadiness{snapshot: ga.ReadinessSnapshot{
		Class:             ga.InstallationExisting,
		Status:            ga.ReadinessReady,
		InventoryComplete: true,
		InventoryDigest:   "current-digest",
		ExportRootValid:   true,
		KeyDomainsReady:   true,
	}}, nil)
	runtime.foundation = backupasset.NewFoundationService(settingsService)

	live, err := runtime.FeatureLive()
	if err != nil {
		t.Fatalf("FeatureLive: %v", err)
	}
	if live {
		t.Fatal("requested search config must not open FeatureLive without ack")
	}
	searchConfig, overlayConfig, err := runtime.foundation.SearchOverlayConfig()
	if err != nil {
		t.Fatalf("SearchOverlayConfig: %v", err)
	}
	if !searchConfig.Enabled || !overlayConfig.Enabled {
		t.Fatalf("requested search/overlay enabled=%t/%t, want true while FeatureLive is false", searchConfig.Enabled, overlayConfig.Enabled)
	}
}

func TestFeatureLiveRequestedTrueWhenReadyIsLive(t *testing.T) {
	runtime := EnablementRuntime(readyGAEnablement(), nil)
	runtime.foundation = backupasset.NewFoundationService(runtimeFoundationSettings(true))

	live, err := runtime.FeatureLive()
	if err != nil || !live {
		t.Fatalf("FeatureLive=%t err=%v, want live", live, err)
	}
}

func TestFeatureLiveRequestedFalseStaysClosed(t *testing.T) {
	runtime := EnablementRuntime(readyGAEnablement(), nil)
	runtime.foundation = backupasset.NewFoundationService(runtimeFoundationSettings(false))

	live, err := runtime.FeatureLive()
	if err != nil || live {
		t.Fatalf("FeatureLive=%t err=%v, want closed", live, err)
	}
}

func TestStartupRequestedEnablementWithoutReadinessDoesNotBecomeManaged(t *testing.T) {
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.Node{}, &model.Task{}, &model.BackupRepository{}, &model.RepositoryAccessBinding{},
		&model.TaskRepositoryLink{}, &model.RecoveryPoint{}, &model.RecoveryPointLease{},
		&model.BackupAssetManagedHistoryLatch{}, &model.BackupAssetInstallation{},
		&model.BackupAssetInventoryRun{}, &model.BackupAssetRepositoryConflict{},
	); err != nil {
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
		DB: db, Settings: settingsService, Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{},
		ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = runtime.StartupPass(context.Background())
	if err != nil {
		t.Fatalf("blocked requested enablement must still boot: %v", err)
	}
	mode, modeErr := runtime.admission.CurrentMode()
	if modeErr != nil {
		t.Fatalf("blocked requested enablement left admission uninitialized: %v", modeErr)
	}
	if mode == publication.AdmissionManaged {
		t.Fatalf("blocked requested enablement became managed mode=%s", mode)
	}
	live, liveErr := runtime.FeatureLive()
	if liveErr != nil || live {
		t.Fatalf("blocked requested enablement FeatureLive=%t err=%v", live, liveErr)
	}
}

func TestTransitionFeatureSuccessStampsEnablementSucceededAt(t *testing.T) {
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_GA_ENABLEMENT_STAMP_KEY_FOR_TEST_ONLY")
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.BackupAssetInstallation{}, &model.BackupAssetInventoryRun{}, &model.BackupAssetRepositoryConflict{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetInstallation{
		ID: strings.Repeat("a", 32), Slot: 1, Class: string(ga.InstallationFresh),
		Readiness: string(ga.ReadinessUnknown), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed installation: %v", err)
	}
	inventory := ga.NewInventoryService(ga.InventoryDependencies{DB: db, Now: func() time.Time { return now }})
	events := []string{}
	runtime := EnablementRuntime(readyGAEnablement(), &runtimeFeatureTransitionerFake{events: &events})
	runtime.inventory = inventory
	runtime.contentManager = &runtimeContentManagerFake{events: &events}
	if err := runtime.TransitionFeature(context.Background(), true, func() error { return nil }); err != nil {
		t.Fatalf("enablement: %v", err)
	}
	var installation model.BackupAssetInstallation
	if err := db.Where("slot = ?", 1).First(&installation).Error; err != nil {
		t.Fatal(err)
	}
	if installation.EnablementSucceededAt == nil || !installation.EnablementSucceededAt.Equal(now.UTC()) {
		t.Fatalf("enablement_succeeded_at=%v, want %s", installation.EnablementSucceededAt, now.UTC())
	}
}

func TestTransitionFeatureStartsSearchWithoutStartupPass(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_GA_ENABLEMENT_SEARCH_KEY_FOR_TEST_ONLY")
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.WrappedDomainKey{}); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	backend := newSearchWorkerBackendFake()
	worker, err := NewSearchWorker(SearchWorkerDependencies{
		Config: func() (SearchWorkerConfig, error) {
			return SearchWorkerConfig{Enabled: true, ReconcileInterval: time.Minute, ReconcileBatchSize: 10, WorkerConcurrency: 1}, nil
		},
		Backend: backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	ready := &atomic.Bool{}
	runtime := EnablementRuntime(readyGAEnablement(), &runtimeFeatureTransitionerFake{events: &events})
	admission := newAdmissionControllerFixture(t, false, nil)
	admission.initialize(t)
	runtime.admission = admission.controller
	runtime.transitioner = admission.controller
	runtime.foundation = backupasset.NewFoundationService(settingsService)
	runtime.settings = settingsService
	runtime.keyring = backupasset.NewKeyring(db, nil)
	runtime.searchWorker = worker
	runtime.searchReady = ready
	runtime.contentManager = &runtimeContentManagerFake{events: &events}

	if err := runtime.TransitionFeature(context.Background(), true, func() error {
		return settingsService.Update("backup_assets.enabled", "true")
	}); err != nil {
		t.Fatalf("hot enable: %v", err)
	}
	if !ready.Load() {
		t.Fatal("hot enable left search not ready")
	}
	if calls := backend.calls(); calls.reconcile != 1 {
		t.Fatalf("hot enable did not start search: %+v", calls)
	}
}

func TestTransitionFeatureSearchFailureDoesNotPersist(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_GA_ENABLEMENT_SEARCH_FAIL_KEY_FOR_TEST_ONLY")
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.WrappedDomainKey{}); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	backend := newSearchWorkerBackendFake()
	backend.overlayErr = errors.New("search overlay reconcile failed")
	worker, err := NewSearchWorker(SearchWorkerDependencies{
		Config: func() (SearchWorkerConfig, error) {
			return SearchWorkerConfig{Enabled: true, ReconcileInterval: time.Minute, ReconcileBatchSize: 10, WorkerConcurrency: 1}, nil
		},
		Backend: backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	ready := &atomic.Bool{}
	runtime := EnablementRuntime(readyGAEnablement(), &runtimeFeatureTransitionerFake{events: &events})
	admission := newAdmissionControllerFixture(t, false, nil)
	admission.initialize(t)
	runtime.admission = admission.controller
	runtime.transitioner = admission.controller
	runtime.foundation = backupasset.NewFoundationService(settingsService)
	runtime.settings = settingsService
	runtime.keyring = backupasset.NewKeyring(db, nil)
	runtime.searchWorker = worker
	runtime.searchReady = ready
	runtime.contentManager = &runtimeContentManagerFake{events: &events}

	if err := runtime.TransitionFeature(context.Background(), true, func() error {
		return settingsService.Update("backup_assets.enabled", "true")
	}); err == nil {
		t.Fatal("hot enable with search failure succeeded")
	}
	if ready.Load() {
		t.Fatal("search failure left search ready")
	}
	if calls := backend.calls(); calls.overlay != 1 {
		t.Fatalf("hot enable did not reach the configured Search failure: %+v", calls)
	}
	if settingsService.GetEffective("backup_assets.enabled") != "false" {
		t.Fatalf("search failure persisted enabled=%q", settingsService.GetEffective("backup_assets.enabled"))
	}
	live, liveErr := runtime.FeatureLive()
	if liveErr != nil || live {
		t.Fatalf("search failure FeatureLive=%t err=%v, want closed", live, liveErr)
	}
}

func TestComposeGAReadinessRejectsUnsafeExportRootAndMissingKeyDomains(t *testing.T) {
	t.Run("unsafe_export_root", func(t *testing.T) {
		t.Setenv("BACKUP_ASSETS_EXPORT_ROOT", "/data/export")
		db := openRuntimeTestDB(t)
		if err := db.AutoMigrate(&model.BackupAssetInstallation{}, &model.BackupAssetInventoryRun{}); err != nil {
			t.Fatal(err)
		}
		source := composeGAReadiness(db, settings.NewService(db), backupasset.NewKeyring(db, func() time.Time {
			return time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
		}))
		snapshot, err := source.CurrentReadiness(context.Background())
		if err != nil {
			t.Fatalf("readiness: %v", err)
		}
		if snapshot.ExportRootValid {
			t.Fatalf("unsafe export root reported valid: %+v", snapshot)
		}
	})
	t.Run("missing_key_domains", func(t *testing.T) {
		t.Setenv("DATA_ENCRYPTION_KEY", "")
		db := openRuntimeTestDB(t)
		if err := db.AutoMigrate(&model.BackupAssetInstallation{}, &model.BackupAssetInventoryRun{}); err != nil {
			t.Fatal(err)
		}
		source := composeGAReadiness(db, settings.NewService(db), backupasset.NewKeyring(db, nil))
		snapshot, err := source.CurrentReadiness(context.Background())
		if err != nil {
			t.Fatalf("readiness: %v", err)
		}
		if snapshot.KeyDomainsReady {
			t.Fatalf("missing key domains reported ready: %+v", snapshot)
		}
	})
}

func TestInventoryRuntimeComposesWithoutDetachedGoroutine(t *testing.T) {
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_GA_RUNTIME_INVENTORY_KEY_FOR_TEST_ONLY")
	now := time.Date(2026, 8, 20, 10, 20, 0, 0, time.UTC)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.Node{}, &model.Task{}, &model.SnapshotFileIndex{},
		&model.BackupRepository{}, &model.TaskRepositoryLink{},
		&model.BackupAssetManagedHistoryLatch{}, &model.RecoveryPointLifecycleTombstone{},
		&model.BackupAssetInstallation{}, &model.BackupAssetInventoryRun{}, &model.BackupAssetRepositoryConflict{},
	); err != nil {
		t.Fatal(err)
	}

	surface := &gaRuntimeMutationSurface{}
	service, err := composeGARuntime(gaRuntimeInput{
		DB: db, Now: func() time.Time { return now }, Mutations: surface,
	})
	if err != nil {
		t.Fatalf("composeGARuntime: %v", err)
	}
	if service == nil {
		t.Fatal("composeGARuntime returned nil inventory service")
	}

	document, err := service.DryRun(context.Background())
	if err != nil {
		t.Fatalf("composed dry-run: %v", err)
	}
	if document.Class != ga.InstallationFresh || document.TrustedSnapshotIndex {
		t.Fatalf("empty composed document=%+v", document)
	}
	if len(surface.calls) != 0 {
		t.Fatalf("composed dry-run issued Provider/Child-14 commands %v", surface.calls)
	}

	transport := &runtimeTransportFake{}
	constructed, err := New(Dependencies{
		DB: db, Settings: settings.NewService(db), Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{},
		ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("construct runtime: %v", err)
	}
	if constructed.Inventory() == nil {
		t.Fatal("runtime omitted the composed inventory owner")
	}
	if constructed.inventoryWorkerStarted() {
		t.Fatal("runtime started a detached inventory goroutine during compose")
	}
	runtimeDocument, err := constructed.Inventory().DryRun(context.Background())
	if err != nil {
		t.Fatalf("runtime inventory dry-run: %v", err)
	}
	if runtimeDocument.Digest == "" || runtimeDocument.Class != ga.InstallationFresh {
		t.Fatalf("runtime inventory document=%+v", runtimeDocument)
	}
	var installation model.BackupAssetInstallation
	if err := db.Where("slot = ?", 1).First(&installation).Error; err != nil {
		t.Fatalf("runtime inventory did not persist installation: %v", err)
	}
	if installation.Class != string(ga.InstallationFresh) {
		t.Fatalf("runtime installation=%+v", installation)
	}
}

type gaRuntimeMutationSurface struct {
	calls []string
}

func (surface *gaRuntimeMutationSurface) OpenProvider(_ context.Context, command string) error {
	surface.calls = append(surface.calls, "open:"+command)
	return errInventoryRuntimeMustNotMutate
}

func (surface *gaRuntimeMutationSurface) DiscoverImport(context.Context) error {
	surface.calls = append(surface.calls, "import")
	return errInventoryRuntimeMustNotMutate
}

func (surface *gaRuntimeMutationSurface) Rebuild(context.Context) error {
	surface.calls = append(surface.calls, "rebuild")
	return errInventoryRuntimeMustNotMutate
}

func (surface *gaRuntimeMutationSurface) Purge(context.Context) error {
	surface.calls = append(surface.calls, "purge")
	return errInventoryRuntimeMustNotMutate
}

var errInventoryRuntimeMustNotMutate = errors.New("inventory dry-run must not mutate provider bytes")

type gaStaticReadiness struct {
	snapshot ga.ReadinessSnapshot
	err      error
}

func (source gaStaticReadiness) CurrentReadiness(context.Context) (ga.ReadinessSnapshot, error) {
	return source.snapshot, source.err
}

func readyGAEnablement() ga.ReadinessSource {
	return gaStaticReadiness{snapshot: ga.ReadinessSnapshot{
		Class:             ga.InstallationFresh,
		Status:            ga.ReadinessReady,
		InventoryComplete: true,
		InventoryDigest:   "test-enablement-digest",
		ExportRootValid:   true,
		KeyDomainsReady:   true,
	}}
}

func TestFeatureLiveObservesRequestedAndLiveGauges(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	registry := prometheus.NewRegistry()
	metrics, err := ga.NewPrometheusMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	runtime := EnablementRuntime(ExistingInstallReadyUnacked(), nil).
		WithFoundation(backupasset.NewFoundationService(settingsService))
	runtime.gaMetrics = metrics
	live, err := runtime.FeatureLive()
	if err != nil || live {
		t.Fatalf("requested existing-unacked must stay closed live=%t err=%v", live, err)
	}
	requested := gaugeValue(t, registry, ga.FeatureRequestedMetric)
	liveValue := gaugeValue(t, registry, ga.FeatureLiveMetric)
	if requested != 1 || liveValue != 0 {
		t.Fatalf("gauges requested=%v live=%v", requested, liveValue)
	}
}

func gaugeValue(t *testing.T, registry *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != name || family.GetType() != dto.MetricType_GAUGE {
			continue
		}
		if len(family.GetMetric()) == 0 {
			t.Fatalf("metric %s has no samples", name)
		}
		return family.GetMetric()[0].GetGauge().GetValue()
	}
	t.Fatalf("missing metric %s", name)
	return 0
}

func stringsContain(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
