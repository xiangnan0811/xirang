package runtime

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	worker, err := NewSearchWorker(SearchWorkerDependencies{Config: config.Get, Backend: backend, Metrics: search.NoopMetrics{}})
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
}

func (source *searchWorkerConfigFake) Get() (SearchWorkerConfig, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.config, nil
}

type searchWorkerCalls struct{ list, build, reconcile, overlay int }

type searchWorkerBackendFake struct {
	mu                sync.Mutex
	candidates        []search.BuildCandidate
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
	backend.mu.Unlock()
	backend.started <- search.BuildCandidate{RepositoryID: candidate.RepositoryID, RecoveryPointID: candidate.RecoveryPointID}
	<-ctx.Done()
	backend.mu.Lock()
	backend.activeNow--
	backend.mu.Unlock()
	return ctx.Err()
}

func (backend *searchWorkerBackendFake) ReconcileAbandoned(context.Context, time.Time, int) (int64, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.callsValue.reconcile++
	return 0, nil
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
