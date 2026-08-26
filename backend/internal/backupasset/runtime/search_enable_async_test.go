package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/ga"
	"xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"
)

func TestFeatureTransitionHotEnableDoesNotOwnSearchCandidateBuild(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_ASYNC_SEARCH_HOT_ENABLE_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.WrappedDomainKey{}, &model.BackupAssetInstallation{},
		&model.BackupAssetInventoryRun{}, &model.BackupAssetRepositoryConflict{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 4, 0, 0, 0, time.UTC)
	if err := db.Create(&model.BackupAssetInstallation{
		ID: "async-search-hot-enable-install", Slot: 1, Class: string(ga.InstallationFresh),
		Readiness: string(ga.ReadinessReady), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	backend := newSearchWorkerBackendFake()
	backend.candidates = oneSearchBuildCandidate()
	configReads := make(chan struct{}, 8)
	backend.build = func(ctx context.Context, _ search.BuildRequest) error {
		<-ctx.Done()
		return ctx.Err()
	}
	worker := newAsyncSearchTestWorker(t, backend, func() SearchWorkerConfig {
		configReads <- struct{}{}
		return validAsyncSearchWorkerConfig(settingsService.GetEffective("backup_assets.enabled") == "true")
	})
	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		worker.Run(runCtx)
		close(runDone)
	}()
	waitForAsyncSearchConfigReads(t, configReads, 2)
	t.Cleanup(func() {
		cancelRun()
		waitForAsyncSearchWorkerDone(t, runDone)
	})
	events := []string{}
	ready := &atomic.Bool{}
	admission := newAdmissionControllerFixture(t, false, nil)
	admission.initialize(t)
	runtime := EnablementRuntime(readyGAEnablement(), admission.controller)
	runtime.admission = admission.controller
	runtime.transitioner = admission.controller
	runtime.foundation = backupasset.NewFoundationService(settingsService)
	runtime.settings = settingsService
	runtime.keyring = backupasset.NewKeyring(db, time.Now)
	runtime.searchWorker = worker
	runtime.searchReady = ready
	content := &asyncSearchWakeOrderingContent{
		runtimeContentManagerFake: runtimeContentManagerFake{events: &events}, worker: worker,
	}
	runtime.contentManager = content
	runtime.inventory = ga.NewInventoryService(ga.InventoryDependencies{DB: db, Now: func() time.Time { return now }})

	done := make(chan error, 1)
	go func() {
		done <- runtime.TransitionFeature(context.Background(), true, func() error {
			return settingsService.Update("backup_assets.enabled", "true")
		})
	}()
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("successful hot enable did not wake the sleeping Search worker")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("hot enable: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("hot enable waited for the worker-owned candidate Build")
	}
	if calls := backend.calls(); calls != (searchWorkerCalls{reconcile: 2, overlay: 2, list: 2, build: 1}) {
		t.Fatalf("hot-enable Search calls=%+v, want one infrastructure prepare plus one worker-owned full pass", calls)
	}
	if got := settingsService.GetEffective("backup_assets.enabled"); got != "true" {
		t.Fatalf("hot enable persisted %q, want true", got)
	}
	if !ready.Load() {
		t.Fatal("hot enable left Search unready after infrastructure preparation")
	}
	if !content.readySeen || content.wakesWhenReady != 0 {
		t.Fatalf("Content ready ordering seen=%t wakes_at_ready=%d, want ready before first wake", content.readySeen, content.wakesWhenReady)
	}
	var installation model.BackupAssetInstallation
	if err := db.Where("slot = ?", 1).Take(&installation).Error; err != nil {
		t.Fatal(err)
	}
	if installation.EnablementSucceededAt == nil {
		t.Fatal("successful hot enable woke Search without a durable enablement-success stamp")
	}
}

type asyncSearchWakeOrderingContent struct {
	runtimeContentManagerFake
	worker         *SearchWorker
	readySeen      bool
	wakesWhenReady int
}

func (content *asyncSearchWakeOrderingContent) SetReady(ready bool) {
	content.runtimeContentManagerFake.SetReady(ready)
	if ready {
		content.readySeen = true
		content.wakesWhenReady = len(content.worker.wake)
	}
}

func TestColdStartupSearchPreparationDoesNotOwnCandidateBuild(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_ASYNC_SEARCH_COLD_START_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.WrappedDomainKey{}); err != nil {
		t.Fatal(err)
	}
	backend := newSearchWorkerBackendFake()
	backend.candidates = oneSearchBuildCandidate()
	backend.build = func(ctx context.Context, _ search.BuildRequest) error {
		<-ctx.Done()
		return ctx.Err()
	}
	worker := newAsyncSearchTestWorker(t, backend, func() SearchWorkerConfig {
		return validAsyncSearchWorkerConfig(true)
	})
	ready := &atomic.Bool{}
	runtime := &Runtime{
		keyring:      backupasset.NewKeyring(db, time.Now),
		searchWorker: worker,
		searchReady:  ready,
	}
	config := testFoundationTransitionConfigs(t, false, true).Prospective

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runtime.startupSearchWithConfig(ctx, config, true) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cold Search preparation: %v", err)
		}
	case <-backend.started:
		cancel()
		err := <-done
		t.Fatalf("cold startup synchronously entered candidate Build before returning: %v", err)
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("cold Search preparation did not complete within bounded control-plane work")
	}
	if calls := backend.calls(); calls.reconcile != 1 || calls.overlay != 1 || calls.list != 1 || calls.build != 0 {
		t.Fatalf("cold Search preparation calls=%+v, want reconcile/overlay/list once and Build zero", calls)
	}
	if !ready.Load() {
		t.Fatal("cold startup left Search unready after infrastructure preparation")
	}
	if got := len(worker.wake); got != 0 {
		t.Fatalf("cold startup queued wakes=%d, want zero", got)
	}
}

func TestSearchWorkerPrepareWithConfigIsInfrastructureOnlyAndUsesExplicitConfig(t *testing.T) {
	backend := newSearchWorkerBackendFake()
	backend.candidates = oneSearchBuildCandidate()
	dynamicCalls := &atomic.Int64{}
	worker, err := NewSearchWorker(SearchWorkerDependencies{
		Config: func() (SearchWorkerConfig, error) {
			dynamicCalls.Add(1)
			return SearchWorkerConfig{}, errors.New("dynamic config must not be read by explicit preparation")
		},
		Backend: backend,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := worker.PrepareWithConfig(context.Background(), validAsyncSearchWorkerConfig(true)); err != nil {
		t.Fatalf("PrepareWithConfig: %v", err)
	}
	if got := dynamicCalls.Load(); got != 0 {
		t.Fatalf("explicit preparation read dynamic config %d times", got)
	}
	if calls := backend.calls(); calls != (searchWorkerCalls{reconcile: 1, overlay: 1, list: 1}) {
		t.Fatalf("infrastructure preparation calls=%+v, want reconcile/overlay/list once and Build zero", calls)
	}
}

func TestSearchWorkerPrepareWithConfigPropagatesInfrastructureAndContextFailures(t *testing.T) {
	reconcileErr := errors.New("FAKE_ASYNC_SEARCH_RECONCILE_FAILURE_FOR_TEST_ONLY")
	overlayErr := errors.New("FAKE_ASYNC_SEARCH_OVERLAY_FAILURE_FOR_TEST_ONLY")
	listErr := errors.New("FAKE_ASYNC_SEARCH_LIST_FAILURE_FOR_TEST_ONLY")

	for _, testCase := range []struct {
		name      string
		config    SearchWorkerConfig
		configure func(*searchWorkerBackendFake)
		ctx       func() context.Context
		want      error
		wantCalls searchWorkerCalls
	}{
		{
			name: "config validation", config: SearchWorkerConfig{Enabled: true},
			want: backupasset.ErrInvalidState,
		},
		{
			name: "abandoned reconciliation", config: validAsyncSearchWorkerConfig(true),
			configure: func(backend *searchWorkerBackendFake) { backend.reconcileErr = reconcileErr },
			want:      reconcileErr, wantCalls: searchWorkerCalls{reconcile: 1},
		},
		{
			name: "overlay reconciliation", config: validAsyncSearchWorkerConfig(true),
			configure: func(backend *searchWorkerBackendFake) { backend.overlayErr = overlayErr },
			want:      overlayErr, wantCalls: searchWorkerCalls{reconcile: 1, overlay: 1},
		},
		{
			name: "candidate enumeration", config: validAsyncSearchWorkerConfig(true),
			configure: func(backend *searchWorkerBackendFake) { backend.listErr = listErr },
			want:      listErr, wantCalls: searchWorkerCalls{reconcile: 1, overlay: 1, list: 1},
		},
		{
			name: "caller cancellation", config: validAsyncSearchWorkerConfig(true),
			configure: func(backend *searchWorkerBackendFake) {
				backend.reconcile = func(ctx context.Context, _ time.Time, _ int) (int64, error) {
					return 0, ctx.Err()
				}
			},
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			want: context.Canceled, wantCalls: searchWorkerCalls{reconcile: 1},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			backend := newSearchWorkerBackendFake()
			if testCase.configure != nil {
				testCase.configure(backend)
			}
			worker := newAsyncSearchTestWorker(t, backend, func() SearchWorkerConfig {
				return validAsyncSearchWorkerConfig(true)
			})
			ctx := context.Background()
			if testCase.ctx != nil {
				ctx = testCase.ctx()
			}
			err := worker.PrepareWithConfig(ctx, testCase.config)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("PrepareWithConfig error=%v, want %v", err, testCase.want)
			}
			if calls := backend.calls(); calls != testCase.wantCalls {
				t.Fatalf("PrepareWithConfig failure calls=%+v, want %+v", calls, testCase.wantCalls)
			}
		})
	}
}

func TestSearchWorkerTryWakeInterruptsLongTimerAndReReadsCommittedConfig(t *testing.T) {
	backend := newSearchWorkerBackendFake()
	backend.candidates = oneSearchBuildCandidate()
	enabled := &atomic.Bool{}
	configReads := make(chan struct{}, 8)
	worker := newAsyncSearchTestWorker(t, backend, func() SearchWorkerConfig {
		configReads <- struct{}{}
		return validAsyncSearchWorkerConfig(enabled.Load())
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		worker.Run(ctx)
		close(done)
	}()
	waitForAsyncSearchConfigReads(t, configReads, 2)
	enabled.Store(true)
	worker.TryWake()
	backend.waitStarted(t)
	cancel()
	waitForAsyncSearchWorkerDone(t, done)
	if calls := backend.calls(); calls != (searchWorkerCalls{reconcile: 1, overlay: 1, list: 1, build: 1}) {
		t.Fatalf("long-timer wake calls=%+v, want one committed enabled full pass", calls)
	}
}

func TestSearchWorkerPendingWakeBeforeRunIsPreservedAndCoalescedIntoInitialPass(t *testing.T) {
	backend := newSearchWorkerBackendFake()
	backend.candidates = oneSearchBuildCandidate()
	release := make(chan struct{})
	backend.build = func(context.Context, search.BuildRequest) error {
		<-release
		return nil
	}
	configReads := make(chan struct{}, 16)
	worker := newAsyncSearchTestWorker(t, backend, func() SearchWorkerConfig {
		configReads <- struct{}{}
		return validAsyncSearchWorkerConfig(true)
	})
	for range 32 {
		worker.TryWake()
	}
	if got := len(worker.wake); got != 1 {
		t.Fatalf("coalesced pending wakes=%d, want one", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		worker.Run(ctx)
		close(done)
	}()
	backend.waitStarted(t)
	close(release)
	waitForAsyncSearchConfigReads(t, configReads, 2)
	select {
	case <-configReads:
		cancel()
		waitForAsyncSearchWorkerDone(t, done)
		t.Fatal("stale pre-Run wake triggered a duplicate pass after the initial full pass")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	waitForAsyncSearchWorkerDone(t, done)
	if calls := backend.calls(); calls != (searchWorkerCalls{reconcile: 1, overlay: 1, list: 1, build: 1}) {
		t.Fatalf("coalesced initial pass calls=%+v, want exactly one full pass", calls)
	}
}

func TestSearchWorkerQueuedWakeWithCommittedDisabledConfigTouchesNoBackend(t *testing.T) {
	backend := newSearchWorkerBackendFake()
	configReads := make(chan struct{}, 8)
	worker := newAsyncSearchTestWorker(t, backend, func() SearchWorkerConfig {
		configReads <- struct{}{}
		return validAsyncSearchWorkerConfig(false)
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		worker.Run(ctx)
		close(done)
	}()
	waitForAsyncSearchConfigReads(t, configReads, 2)
	worker.TryWake()
	waitForAsyncSearchConfigReads(t, configReads, 2)
	cancel()
	waitForAsyncSearchWorkerDone(t, done)
	if calls := backend.calls(); calls != (searchWorkerCalls{}) {
		t.Fatalf("committed-disabled queued wake touched backend: %+v", calls)
	}
}

func TestSearchWorkerShutdownCancelsAndJoinsWokenBuild(t *testing.T) {
	backend := newSearchWorkerBackendFake()
	backend.candidates = oneSearchBuildCandidate()
	enabled := &atomic.Bool{}
	configReads := make(chan struct{}, 8)
	metrics := newSearchWorkerMetricsSpy()
	worker, err := NewSearchWorker(SearchWorkerDependencies{
		Config: func() (SearchWorkerConfig, error) {
			configReads <- struct{}{}
			return validAsyncSearchWorkerConfig(enabled.Load()), nil
		},
		Backend: backend,
		Metrics: metrics,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		worker.Run(context.Background())
		close(done)
	}()
	waitForAsyncSearchConfigReads(t, configReads, 2)
	enabled.Store(true)
	worker.TryWake()
	backend.waitStarted(t)

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := worker.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	waitForAsyncSearchWorkerDone(t, done)
	if backend.active() != 0 {
		t.Fatalf("shutdown left %d active woken Builds", backend.active())
	}
	if got := metrics.active(); got != 0 {
		t.Fatalf("shutdown left active-build gauge=%d, want zero", got)
	}
}

func TestFeatureTransitionFailedStagesDoNotWakeSearchWorker(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		contentErr error
		persistErr error
		cleanupErr error
	}{
		{name: "Content preparation", contentErr: errors.New("FAKE_ASYNC_CONTENT_FAILURE_FOR_TEST_ONLY")},
		{name: "persistence", persistErr: errors.New("FAKE_ASYNC_PERSIST_FAILURE_FOR_TEST_ONLY")},
		{
			name: "compensation", persistErr: errors.New("FAKE_ASYNC_PRIMARY_FAILURE_FOR_TEST_ONLY"),
			cleanupErr: errors.New("FAKE_ASYNC_COMPENSATION_FAILURE_FOR_TEST_ONLY"),
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			backend := newSearchWorkerBackendFake()
			worker := newAsyncSearchTestWorker(t, backend, func() SearchWorkerConfig {
				return validAsyncSearchWorkerConfig(true)
			})
			events := []string{}
			runtime := &Runtime{
				contentManager: &runtimeContentManagerFake{
					events: &events, prepareEnableErr: testCase.contentErr, prepareDisableErr: testCase.cleanupErr,
				},
				transitioner: &runtimeFeatureTransitionerFake{events: &events},
				enablement:   readyGAEnablement(),
				searchWorker: worker,
			}
			err := runtime.transitionFeatureWithConfigs(
				context.Background(), testFoundationTransitionConfigs(t, false, true),
				func() error { return testCase.persistErr },
			)
			if err == nil {
				t.Fatal("failed transition returned nil")
			}
			if got := len(worker.wake); got != 0 {
				t.Fatalf("failed transition queued wakes=%d, want zero", got)
			}
		})
	}
}

func TestFeatureTransitionCallerCancellationBeforeFinalWakeReturnsWithoutBuildOrWake(t *testing.T) {
	backend := newSearchWorkerBackendFake()
	backend.candidates = oneSearchBuildCandidate()
	worker := newAsyncSearchTestWorker(t, backend, func() SearchWorkerConfig {
		return validAsyncSearchWorkerConfig(true)
	})
	events := []string{}
	runtime := &Runtime{
		contentManager: &runtimeContentManagerFake{events: &events},
		transitioner:   &runtimeFeatureTransitionerFake{events: &events},
		enablement:     readyGAEnablement(),
		searchWorker:   worker,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runtime.transitionFeatureWithConfigs(
		ctx, testFoundationTransitionConfigs(t, false, true), func() error { return ctx.Err() },
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled transition error=%v, want context.Canceled", err)
	}
	if got := len(worker.wake); got != 0 {
		t.Fatalf("canceled transition queued wakes=%d, want zero", got)
	}
	if calls := backend.calls(); calls.build != 0 {
		t.Fatalf("canceled transition owned candidate Builds=%d, want zero", calls.build)
	}
}

func waitForAsyncSearchConfigReads(t *testing.T, reads <-chan struct{}, count int) {
	t.Helper()
	for range count {
		select {
		case <-reads:
		case <-time.After(time.Second):
			t.Fatalf("Search worker config reads did not reach %d", count)
		}
	}
}

func waitForAsyncSearchWorkerDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Search worker did not stop")
	}
}

func oneSearchBuildCandidate() []search.BuildCandidate {
	return []search.BuildCandidate{{RepositoryID: "repo-async", RecoveryPointID: "point-async"}}
}

func validAsyncSearchWorkerConfig(enabled bool) SearchWorkerConfig {
	return SearchWorkerConfig{
		Enabled: enabled, ReconcileInterval: time.Hour, ReconcileBatchSize: 10, WorkerConcurrency: 1,
	}
}

func newAsyncSearchTestWorker(
	t *testing.T,
	backend SearchWorkerBackend,
	config func() SearchWorkerConfig,
) *SearchWorker {
	t.Helper()
	worker, err := NewSearchWorker(SearchWorkerDependencies{
		Config:  func() (SearchWorkerConfig, error) { return config(), nil },
		Backend: backend,
	})
	if err != nil {
		t.Fatalf("NewSearchWorker: %v", err)
	}
	return worker
}
