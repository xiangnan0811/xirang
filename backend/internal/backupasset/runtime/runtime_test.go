package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
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
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
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
		Metrics:       publication.NoopMetrics{}, Now: func() time.Time { return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC) },
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
		StagedPayload: &runtimeStagedPayloadFake{},
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
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settingsService, Transport: transport, StreamTransport: transport, StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.StartupPass(context.Background()); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("managed startup without TaskRun readiness error=%v, want invalid state", err)
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
