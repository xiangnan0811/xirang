package runtime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/search"
)

func TestSearchWorkerStartupPassWithConfigDoesNotReadDynamicConfig(t *testing.T) {
	backend := newSearchWorkerBackendFake()
	dynamicCalls := &atomic.Int64{}
	worker, err := NewSearchWorker(SearchWorkerDependencies{
		Config: func() (SearchWorkerConfig, error) {
			dynamicCalls.Add(1)
			return SearchWorkerConfig{}, errors.New("dynamic Search config must not be read during enable transition")
		},
		Backend: backend,
		Metrics: search.NoopMetrics{},
	})
	if err != nil {
		t.Fatalf("NewSearchWorker: %v", err)
	}

	config := SearchWorkerConfig{
		Enabled: true, ReconcileInterval: time.Minute, ReconcileBatchSize: 17, WorkerConcurrency: 2,
	}
	if err := worker.StartupPassWithConfig(context.Background(), config); err != nil {
		t.Fatalf("StartupPassWithConfig: %v", err)
	}
	if got := dynamicCalls.Load(); got != 0 {
		t.Fatalf("explicit Search startup read dynamic config %d times", got)
	}
	if got := backend.calls(); got.reconcile != 1 || got.overlay != 1 || got.list != 1 {
		t.Fatalf("explicit Search startup backend calls=%+v", got)
	}
}

func TestSearchWorkerBackgroundRunStillReadsDynamicConfig(t *testing.T) {
	backend := newSearchWorkerBackendFake()
	configRead := make(chan struct{}, 2)
	worker, err := NewSearchWorker(SearchWorkerDependencies{
		Config: func() (SearchWorkerConfig, error) {
			configRead <- struct{}{}
			return SearchWorkerConfig{Enabled: false, ReconcileInterval: time.Hour, ReconcileBatchSize: 10, WorkerConcurrency: 1}, nil
		},
		Backend: backend,
		Metrics: search.NoopMetrics{},
	})
	if err != nil {
		t.Fatalf("NewSearchWorker: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		worker.Run(ctx)
		close(done)
	}()
	select {
	case <-configRead:
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("background Search Run did not read dynamic config")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("background Search Run did not stop after cancellation")
	}
}

func TestSearchWorkerDynamicDisableTouchesNoBackend(t *testing.T) {
	backend := newSearchWorkerBackendFake()
	config := &searchWorkerConfigFake{config: SearchWorkerConfig{Enabled: false, ReconcileInterval: time.Millisecond, ReconcileBatchSize: 10, WorkerConcurrency: 2}}
	worker, err := NewSearchWorker(SearchWorkerDependencies{Config: config.Get, Backend: backend, Metrics: search.NoopMetrics{}})
	if err != nil {
		t.Fatalf("NewSearchWorker: %v", err)
	}
	if err := worker.StartupPass(context.Background()); err != nil {
		t.Fatalf("StartupPass: %v", err)
	}
	if backend.calls() != (searchWorkerCalls{}) {
		t.Fatalf("disabled worker touched Search backend: %+v", backend.calls())
	}
}

func TestSearchWorkerSchedulesRepositoryFairAndJoinsCanceledBuilds(t *testing.T) {
	backend := newSearchWorkerBackendFake()
	backend.candidates = []search.BuildCandidate{
		{RepositoryID: "repo-a", RecoveryPointID: "point-a1"},
		{RepositoryID: "repo-a", RecoveryPointID: "point-a2"},
		{RepositoryID: "repo-b", RecoveryPointID: "point-b1"},
	}
	config := &searchWorkerConfigFake{config: SearchWorkerConfig{Enabled: true, ReconcileInterval: time.Hour, ReconcileBatchSize: 10, WorkerConcurrency: 2}}
	metrics := newSearchWorkerMetricsSpy()
	worker, err := NewSearchWorker(SearchWorkerDependencies{Config: config.Get, Backend: backend, Metrics: metrics})
	if err != nil {
		t.Fatalf("NewSearchWorker: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		worker.Run(ctx)
		close(done)
	}()
	first := backend.waitStarted(t)
	second := backend.waitStarted(t)
	if first.RepositoryID == second.RepositoryID {
		t.Fatalf("first scheduling wave starved repository fairness: first=%+v second=%+v", first, second)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Search worker did not join canceled builds")
	}
	if backend.active() != 0 {
		t.Fatalf("Search worker left %d active builds", backend.active())
	}
	if got := metrics.buildOutcomes(); !reflect.DeepEqual(got, map[search.BuildOutcome]int{search.BuildOutcomeCanceled: 2}) {
		t.Fatalf("canceled Search build outcomes=%v, want two canceled", got)
	}
	if got := metrics.active(); got != 0 {
		t.Fatalf("canceled Search active-build gauge=%d, want 0 after join", got)
	}
}

func TestSearchWorkerStartupPassIsolatesCandidateBuildFailures(t *testing.T) {
	backend := newSearchWorkerBackendFake()
	backend.candidates = []search.BuildCandidate{
		{RepositoryID: "repo-a", RecoveryPointID: "point-success"},
		{RepositoryID: "repo-b", RecoveryPointID: "point-failure"},
		{RepositoryID: "repo-c", RecoveryPointID: "point-canceled"},
		{RepositoryID: "repo-d", RecoveryPointID: "point-fenced"},
	}
	candidateErr := errors.New("FAKE_SEARCH_CANDIDATE_BUILD_FAILURE_FOR_TEST_ONLY")
	backend.build = func(_ context.Context, request search.BuildRequest) error {
		switch request.RecoveryPointID {
		case "point-success":
			return nil
		case "point-failure":
			return candidateErr
		case "point-canceled":
			return context.Canceled
		case "point-fenced":
			return backupasset.ErrLeaseFenceLost
		default:
			return fmt.Errorf("unexpected Search candidate %q", request.RecoveryPointID)
		}
	}
	metrics := newSearchWorkerMetricsSpy()
	config := SearchWorkerConfig{
		Enabled: true, ReconcileInterval: time.Minute, ReconcileBatchSize: 10, WorkerConcurrency: 4,
	}
	worker, err := NewSearchWorker(SearchWorkerDependencies{
		Config: func() (SearchWorkerConfig, error) { return config, nil }, Backend: backend, Metrics: metrics,
	})
	if err != nil {
		t.Fatalf("NewSearchWorker: %v", err)
	}
	if err := worker.StartupPassWithConfig(context.Background(), config); err != nil {
		t.Fatalf("behavioral RED: candidate-local Build failure escaped StartupPassWithConfig: %v", err)
	}
	if calls := backend.calls(); calls.build != len(backend.candidates) {
		t.Fatalf("candidate build calls=%d, want %d", calls.build, len(backend.candidates))
	}
	wantOutcomes := map[search.BuildOutcome]int{
		search.BuildOutcomeSuccess: 1, search.BuildOutcomeFailure: 1,
		search.BuildOutcomeCanceled: 1, search.BuildOutcomeFenced: 1,
	}
	if got := metrics.buildOutcomes(); !reflect.DeepEqual(got, wantOutcomes) {
		t.Fatalf("Search build outcomes=%v, want %v", got, wantOutcomes)
	}
	if got := metrics.active(); got != 0 {
		t.Fatalf("Search active-build gauge=%d, want 0 after join", got)
	}
}

func TestSearchWorkerPropagatesPassLevelFailures(t *testing.T) {
	configSourceErr := errors.New("FAKE_SEARCH_CONFIG_SOURCE_FAILURE_FOR_TEST_ONLY")
	reconcileErr := errors.New("FAKE_SEARCH_RECONCILE_FAILURE_FOR_TEST_ONLY")
	overlayErr := errors.New("FAKE_SEARCH_OVERLAY_FAILURE_FOR_TEST_ONLY")
	listErr := errors.New("FAKE_SEARCH_LIST_FAILURE_FOR_TEST_ONLY")
	validConfig := SearchWorkerConfig{
		Enabled: true, ReconcileInterval: time.Minute, ReconcileBatchSize: 10, WorkerConcurrency: 1,
	}
	for _, testCase := range []struct {
		name         string
		config       SearchWorkerConfig
		configErr    error
		reconcileErr error
		overlayErr   error
		listErr      error
		explicit     bool
		want         error
		wantCalls    searchWorkerCalls
	}{
		{name: "config source", config: validConfig, configErr: configSourceErr, want: configSourceErr},
		{name: "config validation", config: SearchWorkerConfig{Enabled: true}, explicit: true, want: backupasset.ErrInvalidState},
		{name: "abandoned reconciliation", config: validConfig, reconcileErr: reconcileErr, explicit: true, want: reconcileErr, wantCalls: searchWorkerCalls{reconcile: 1}},
		{name: "overlay reconciliation", config: validConfig, overlayErr: overlayErr, explicit: true, want: overlayErr, wantCalls: searchWorkerCalls{reconcile: 1, overlay: 1}},
		{name: "candidate list", config: validConfig, listErr: listErr, explicit: true, want: listErr, wantCalls: searchWorkerCalls{reconcile: 1, overlay: 1, list: 1}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			backend := newSearchWorkerBackendFake()
			backend.reconcileErr = testCase.reconcileErr
			backend.overlayErr = testCase.overlayErr
			backend.listErr = testCase.listErr
			config := &searchWorkerConfigFake{config: testCase.config, err: testCase.configErr}
			worker, err := NewSearchWorker(SearchWorkerDependencies{Config: config.Get, Backend: backend})
			if err != nil {
				t.Fatalf("NewSearchWorker: %v", err)
			}
			var passErr error
			if testCase.explicit {
				passErr = worker.StartupPassWithConfig(context.Background(), testCase.config)
			} else {
				passErr = worker.StartupPass(context.Background())
			}
			if !errors.Is(passErr, testCase.want) {
				t.Fatalf("Startup pass error=%v, want %v", passErr, testCase.want)
			}
			if calls := backend.calls(); calls != testCase.wantCalls {
				t.Fatalf("pass-level failure calls=%+v, want %+v", calls, testCase.wantCalls)
			}
		})
	}
}

func TestSearchWorkerReReadsConfigAndPropagatesBackendFailure(t *testing.T) {
	backend := newSearchWorkerBackendFake()
	backend.listErr = errors.New("database unavailable")
	config := &searchWorkerConfigFake{config: SearchWorkerConfig{Enabled: true, ReconcileInterval: time.Second, ReconcileBatchSize: 2, WorkerConcurrency: 1}}
	worker, _ := NewSearchWorker(SearchWorkerDependencies{Config: config.Get, Backend: backend, Metrics: search.NoopMetrics{}})
	if err := worker.StartupPass(context.Background()); !errors.Is(err, backend.listErr) {
		t.Fatalf("StartupPass got %v, want backend failure", err)
	}
	config.mu.Lock()
	config.config.Enabled = false
	config.mu.Unlock()
	backend.listErr = nil
	before := backend.calls()
	if err := worker.StartupPass(context.Background()); err != nil {
		t.Fatalf("disabled second pass: %v", err)
	}
	after := backend.calls()
	if after.list != before.list {
		t.Fatalf("dynamic disable did not stop list calls: before=%+v after=%+v", before, after)
	}
}

func TestSearchWorkerReconcilesInvalidOverlaySourcesEveryEnabledPass(t *testing.T) {
	backend := newSearchWorkerBackendFake()
	backend.overlayReconciled = 3
	config := &searchWorkerConfigFake{config: SearchWorkerConfig{
		Enabled: true, ReconcileInterval: time.Second, ReconcileBatchSize: 7, WorkerConcurrency: 1,
	}}
	worker, err := NewSearchWorker(SearchWorkerDependencies{Config: config.Get, Backend: backend, Metrics: search.NoopMetrics{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.StartupPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := backend.calls(); calls.overlay != 1 {
		t.Fatalf("enabled pass overlay reconciliation calls=%+v", calls)
	}
}

func TestSearchIndexerWorkerBackendRekeysTagsBeforeSourceLifecycle(t *testing.T) {
	overlays := &searchOverlayReconcilerFake{tagCount: 2, sourceCount: 3, recentCount: 4, idempotencyCount: 5}
	backend := searchIndexerWorkerBackend{overlays: overlays}
	count, err := backend.ReconcileOverlays(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{"tags", "sources", "recent", "idempotency"}
	if count != 14 || !reflect.DeepEqual(overlays.calls, wantCalls) {
		t.Fatalf("overlay reconciliation count=%d calls=%v", count, overlays.calls)
	}
}

type searchOverlayReconcilerFake struct {
	calls            []string
	tagCount         int64
	sourceCount      int64
	recentCount      int64
	idempotencyCount int64
}

func (fake *searchOverlayReconcilerFake) ReconcileTagKeys(context.Context, int) (int64, error) {
	fake.calls = append(fake.calls, "tags")
	return fake.tagCount, nil
}

func (fake *searchOverlayReconcilerFake) ReconcileInvalidSources(context.Context, int) (int64, error) {
	fake.calls = append(fake.calls, "sources")
	return fake.sourceCount, nil
}

func (fake *searchOverlayReconcilerFake) CleanupExpiredRecent(context.Context, int) (int64, error) {
	fake.calls = append(fake.calls, "recent")
	return fake.recentCount, nil
}

func (fake *searchOverlayReconcilerFake) CleanupIdempotency(context.Context, int) (int64, error) {
	fake.calls = append(fake.calls, "idempotency")
	return fake.idempotencyCount, nil
}

type searchWorkerConfigFake struct {
	mu     sync.Mutex
	config SearchWorkerConfig
	err    error
}

func (source *searchWorkerConfigFake) Get() (SearchWorkerConfig, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.config, source.err
}

type searchWorkerCalls struct{ list, build, reconcile, overlay int }

type searchWorkerBackendFake struct {
	mu                sync.Mutex
	candidates        []search.BuildCandidate
	build             func(context.Context, search.BuildRequest) error
	reconcileErr      error
	listErr           error
	overlayErr        error
	overlayReconciled int64
	started           chan search.BuildCandidate
	activeNow         int
	callsValue        searchWorkerCalls
}

func newSearchWorkerBackendFake() *searchWorkerBackendFake {
	return &searchWorkerBackendFake{started: make(chan search.BuildCandidate, 8)}
}

func (backend *searchWorkerBackendFake) ListCandidates(context.Context, int) ([]search.BuildCandidate, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.callsValue.list++
	return append([]search.BuildCandidate(nil), backend.candidates...), backend.listErr
}

func (backend *searchWorkerBackendFake) Build(ctx context.Context, candidate search.BuildRequest) error {
	backend.mu.Lock()
	backend.callsValue.build++
	backend.activeNow++
	build := backend.build
	backend.mu.Unlock()
	defer func() {
		backend.mu.Lock()
		backend.activeNow--
		backend.mu.Unlock()
	}()
	backend.started <- search.BuildCandidate{RepositoryID: candidate.RepositoryID, RecoveryPointID: candidate.RecoveryPointID}
	if build != nil {
		return build(ctx, candidate)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (backend *searchWorkerBackendFake) ReconcileAbandoned(context.Context, time.Time, int) (int64, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.callsValue.reconcile++
	return 0, backend.reconcileErr
}

func (backend *searchWorkerBackendFake) ReconcileOverlays(context.Context, int) (int64, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.callsValue.overlay++
	return backend.overlayReconciled, backend.overlayErr
}

func (backend *searchWorkerBackendFake) waitStarted(t *testing.T) search.BuildCandidate {
	t.Helper()
	select {
	case candidate := <-backend.started:
		return candidate
	case <-time.After(time.Second):
		t.Fatal("Search build did not start")
		return search.BuildCandidate{}
	}
}

func (backend *searchWorkerBackendFake) calls() searchWorkerCalls {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.callsValue
}

func (backend *searchWorkerBackendFake) active() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.activeNow
}

type searchWorkerMetricsSpy struct {
	mu        sync.Mutex
	builds    map[search.BuildOutcome]int
	activeNow int
}

func newSearchWorkerMetricsSpy() *searchWorkerMetricsSpy {
	return &searchWorkerMetricsSpy{builds: make(map[search.BuildOutcome]int)}
}

func (metrics *searchWorkerMetricsSpy) ObserveBuild(outcome search.BuildOutcome) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.builds[outcome]++
}

func (*searchWorkerMetricsSpy) ObserveScan(search.ScanOutcome) {}
func (*searchWorkerMetricsSpy) AddReconciledAbandoned(int64)   {}
func (*searchWorkerMetricsSpy) AddReconciledOverlays(int64)    {}

func (metrics *searchWorkerMetricsSpy) SetActiveBuilds(count int) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.activeNow = count
}

func (metrics *searchWorkerMetricsSpy) buildOutcomes() map[search.BuildOutcome]int {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	result := make(map[search.BuildOutcome]int, len(metrics.builds))
	for outcome, count := range metrics.builds {
		result[outcome] = count
	}
	return result
}

func (metrics *searchWorkerMetricsSpy) active() int {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	return metrics.activeNow
}
