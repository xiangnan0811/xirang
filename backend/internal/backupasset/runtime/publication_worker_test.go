package runtime

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
)

func TestPublicationWorkerStartupPassRunsBeforePeriodicLoop(t *testing.T) {
	reconciler := &workerReconciler{candidates: []string{workerPointIDOne, workerPointIDTwo}}
	worker, err := NewPublicationWorker(PublicationWorkerDependencies{
		Foundation: workerFoundation(true), Reconciler: reconciler, Metrics: publication.NoopMetrics{},
		Now: func() time.Time { return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.StartupPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reconciler.listCalls != 1 || reconciler.listLimit != 100 {
		t.Fatalf("candidate list calls=%d limit=%d", reconciler.listCalls, reconciler.listLimit)
	}
	if got := reconciler.processedIDs(); len(got) != 2 || !containsWorkerPoint(got, workerPointIDOne) || !containsWorkerPoint(got, workerPointIDTwo) {
		t.Fatalf("startup pass processed=%v", got)
	}
}

type wakeWorkerReconciler struct {
	processed chan string
}

func (*wakeWorkerReconciler) ListCandidates(context.Context, int) ([]string, error) { return nil, nil }
func (reconciler *wakeWorkerReconciler) ProcessPoint(_ context.Context, pointID string) (publication.Outcome, error) {
	reconciler.processed <- pointID
	return publication.Outcome{RecoveryPointID: pointID, State: backupasset.RecoveryPointVerifying}, nil
}
func (*wakeWorkerReconciler) HasUnresolvedPublication(context.Context) (bool, error) {
	return false, nil
}

func TestPublicationWorkerFeatureDisableStopsNewWakeClaims(t *testing.T) {
	reconciler := &wakeWorkerReconciler{processed: make(chan string, 1)}
	worker, err := NewPublicationWorker(PublicationWorkerDependencies{
		Foundation: workerFoundation(false), Reconciler: reconciler, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		worker.Run(ctx)
		close(done)
	}()
	if !worker.TryWake(workerPointIDOne) {
		t.Fatal("wake enqueue failed before shutdown")
	}
	select {
	case pointID := <-reconciler.processed:
		t.Fatalf("disabled worker claimed wake point %q", pointID)
	case <-time.After(150 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker run did not exit")
	}
}

type committedWorkerReconciler struct {
	calls int32
}

func (*committedWorkerReconciler) ListCandidates(context.Context, int) ([]string, error) {
	return nil, nil
}
func (reconciler *committedWorkerReconciler) ProcessPoint(_ context.Context, pointID string) (publication.Outcome, error) {
	atomic.AddInt32(&reconciler.calls, 1)
	return publication.Outcome{RecoveryPointID: pointID, State: backupasset.RecoveryPointCommitted}, nil
}
func (*committedWorkerReconciler) HasUnresolvedPublication(context.Context) (bool, error) {
	return false, nil
}

type committedWorkerObserver struct{ calls int32 }

func (observer *committedWorkerObserver) ObserveCommitted(context.Context, publication.Outcome) {
	atomic.AddInt32(&observer.calls, 1)
}

func TestPublicationWorkerObserverIsAtMostOnceForOneCommittedPoint(t *testing.T) {
	reconciler := &committedWorkerReconciler{}
	observer := &committedWorkerObserver{}
	worker, err := NewPublicationWorker(PublicationWorkerDependencies{
		Foundation: workerFoundation(true), Reconciler: reconciler, Observer: observer, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	worker.process(context.Background(), workerPointIDOne)
	worker.process(context.Background(), workerPointIDOne)
	if got := atomic.LoadInt32(&observer.calls); got != 1 {
		t.Fatalf("committed observer calls=%d, want 1", got)
	}
}

func TestPublicationWorkerShutdownStopsRunWithoutWaitingForPeriodicTimer(t *testing.T) {
	worker, err := NewPublicationWorker(PublicationWorkerDependencies{
		Foundation: workerFoundation(true), Reconciler: &workerReconciler{}, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		worker.Run(context.Background())
		close(done)
	}()
	// The default reconcile interval is five minutes. Give Run time to enter its
	// timer select, then require Shutdown to wake it without parent cancellation.
	time.Sleep(50 * time.Millisecond)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown worker: %v", err)
	}
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("worker Run remained blocked on its periodic timer after Shutdown")
	}
}

func TestPublicationWorkerWakeIsNonblockingAndDurableStateRecoversLostWake(t *testing.T) {
	reconciler := &workerReconciler{candidates: []string{workerPointIDOne}}
	worker, err := NewPublicationWorker(PublicationWorkerDependencies{
		Foundation: workerFoundation(true), Reconciler: reconciler, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < publicationWakeBuffer; index++ {
		if !worker.TryWake(workerPointIDOne) {
			t.Fatalf("wake %d did not fill the bounded queue", index)
		}
	}
	started := time.Now()
	if worker.TryWake(workerPointIDTwo) {
		t.Fatal("full wake queue unexpectedly accepted another point")
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("full wake queue blocked for %s", elapsed)
	}
	if err := worker.StartupPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := reconciler.processedIDs(); len(got) != 1 || got[0] != workerPointIDOne {
		t.Fatalf("durable candidate did not recover lost wake: %v", got)
	}
}

func TestPublicationWorkerSemaphoreBoundsWakeAndCandidateProcessPointTogether(t *testing.T) {
	settings := workerSettingsFromEnabled(true)
	settings["backup_assets.publication_worker_concurrency"] = "1"
	reconciler := &blockingWorkerReconciler{candidates: []string{workerPointIDOne, workerPointIDTwo}, started: make(chan struct{}, 2), release: make(chan struct{})}
	worker, err := NewPublicationWorker(PublicationWorkerDependencies{
		Foundation: backupasset.NewFoundationService(settings), Reconciler: reconciler, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- worker.StartupPass(context.Background()) }()
	select {
	case <-reconciler.started:
	case <-time.After(time.Second):
		t.Fatal("first bounded worker process did not start")
	}
	if got := reconciler.max.Load(); got != 1 {
		t.Fatalf("worker exceeded configured concurrency: %d", got)
	}
	close(reconciler.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := reconciler.max.Load(); got != 1 || reconciler.calls.Load() != 2 {
		t.Fatalf("candidate processes max=%d calls=%d, want 1/2", got, reconciler.calls.Load())
	}
}

func TestPublicationWorkerReporterFailureDoesNotRollBackPublication(t *testing.T) {
	reconciler := &committedWorkerReconciler{}
	reporter := &workerReporterFake{err: errWorkerReporter}
	worker, err := NewPublicationWorker(PublicationWorkerDependencies{
		Foundation: workerFoundation(true), Reconciler: reconciler, Reporter: reporter, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	worker.process(context.Background(), workerPointIDOne)
	if atomic.LoadInt32(&reconciler.calls) != 1 || reporter.calls.Load() != 1 {
		t.Fatalf("reporter failure prevented committed publication process: reconcile=%d reporter=%d", atomic.LoadInt32(&reconciler.calls), reporter.calls.Load())
	}
}

func TestPublicationWorkerRestartResumesVerifyingAndPreparingPoints(t *testing.T) {
	reconciler := &workerReconciler{candidates: []string{workerPointIDOne, workerPointIDTwo}}
	for restart := 0; restart < 2; restart++ {
		worker, err := NewPublicationWorker(PublicationWorkerDependencies{
			Foundation: workerFoundation(true), Reconciler: reconciler, Metrics: publication.NoopMetrics{},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := worker.StartupPass(context.Background()); err != nil {
			t.Fatalf("restart %d startup pass: %v", restart, err)
		}
	}
	if got := reconciler.processedIDs(); len(got) != 4 {
		t.Fatalf("restart did not resume durable candidates: %v", got)
	}
}

func TestPublicationWorkerShutdownRejectsWakeCancelsAndJoinsActiveWork(t *testing.T) {
	reconciler := &cancelableWorkerReconciler{started: make(chan struct{})}
	worker, err := NewPublicationWorker(PublicationWorkerDependencies{
		Foundation: workerFoundation(true), Reconciler: reconciler, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	go worker.process(context.Background(), workerPointIDOne)
	select {
	case <-reconciler.started:
	case <-time.After(time.Second):
		t.Fatal("worker never started active reconciliation")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	if worker.TryWake(workerPointIDTwo) {
		t.Fatal("shutdown worker accepted a new wake")
	}
	select {
	case <-reconciler.canceled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel and join active work")
	}
}

const (
	workerPointIDOne = "11111111111111111111111111111111"
	workerPointIDTwo = "22222222222222222222222222222222"
)

type workerReconciler struct {
	mu         sync.Mutex
	candidates []string
	listCalls  int
	listLimit  int
	processed  []string
}

type blockingWorkerReconciler struct {
	candidates []string
	started    chan struct{}
	release    chan struct{}
	calls      atomic.Int32
	current    atomic.Int32
	max        atomic.Int32
}

func (reconciler *blockingWorkerReconciler) ListCandidates(context.Context, int) ([]string, error) {
	return append([]string(nil), reconciler.candidates...), nil
}
func (reconciler *blockingWorkerReconciler) ProcessPoint(_ context.Context, pointID string) (publication.Outcome, error) {
	current := reconciler.current.Add(1)
	for {
		maximum := reconciler.max.Load()
		if current <= maximum || reconciler.max.CompareAndSwap(maximum, current) {
			break
		}
	}
	reconciler.calls.Add(1)
	reconciler.started <- struct{}{}
	<-reconciler.release
	reconciler.current.Add(-1)
	return publication.Outcome{RecoveryPointID: pointID, State: backupasset.RecoveryPointVerifying}, nil
}
func (*blockingWorkerReconciler) HasUnresolvedPublication(context.Context) (bool, error) {
	return false, nil
}

var errWorkerReporter = fmt.Errorf("FAKE_WORKER_REPORTER_FAILURE_FOR_TEST_ONLY")

type workerReporterFake struct {
	err   error
	calls atomic.Int32
}

func (reporter *workerReporterFake) ReportInterruptedPublication(context.Context, publication.Outcome) error {
	reporter.calls.Add(1)
	return reporter.err
}

type cancelableWorkerReconciler struct {
	started  chan struct{}
	canceled chan struct{}
}

func (*cancelableWorkerReconciler) ListCandidates(context.Context, int) ([]string, error) {
	return nil, nil
}
func (reconciler *cancelableWorkerReconciler) ProcessPoint(ctx context.Context, pointID string) (publication.Outcome, error) {
	close(reconciler.started)
	<-ctx.Done()
	if reconciler.canceled == nil {
		reconciler.canceled = make(chan struct{})
	}
	close(reconciler.canceled)
	return publication.Outcome{RecoveryPointID: pointID, State: backupasset.RecoveryPointVerifying}, ctx.Err()
}
func (*cancelableWorkerReconciler) HasUnresolvedPublication(context.Context) (bool, error) {
	return false, nil
}

func (reconciler *workerReconciler) ListCandidates(_ context.Context, limit int) ([]string, error) {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	reconciler.listCalls++
	reconciler.listLimit = limit
	return append([]string(nil), reconciler.candidates...), nil
}

func (reconciler *workerReconciler) ProcessPoint(_ context.Context, pointID string) (publication.Outcome, error) {
	reconciler.mu.Lock()
	reconciler.processed = append(reconciler.processed, pointID)
	reconciler.mu.Unlock()
	return publication.Outcome{RecoveryPointID: pointID, State: backupasset.RecoveryPointVerifying}, nil
}

func (*workerReconciler) HasUnresolvedPublication(context.Context) (bool, error) { return false, nil }

func (reconciler *workerReconciler) processedIDs() []string {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	return append([]string(nil), reconciler.processed...)
}

func containsWorkerPoint(points []string, want string) bool {
	for _, point := range points {
		if point == want {
			return true
		}
	}
	return false
}

type workerSettings map[string]string

func (settings workerSettings) GetEffective(key string) string { return settings[key] }

func workerFoundation(enabled bool) *backupasset.FoundationService {
	return backupasset.NewFoundationService(workerSettingsFromEnabled(enabled))
}

func workerSettingsFromEnabled(enabled bool) workerSettings {
	value := "false"
	if enabled {
		value = "true"
	}
	return workerSettings{
		"backup_assets.enabled":                          value,
		"backup_assets.catalog_batch_size":               "2000",
		"backup_assets.catalog_build_timeout":            "30m",
		"backup_assets.repository_reconcile_interval":    "15m",
		"backup_assets.audit_segment_max_events":         "10000",
		"backup_assets.audit_segment_max_age":            "24h",
		"backup_assets.audit_detail_retention_days":      "180",
		"backup_assets.audit_checkpoint_retention_days":  "2555",
		"backup_assets.lease_duration":                   "5m",
		"backup_assets.lease_heartbeat":                  "60s",
		"backup_assets.lease_absolute_deadline":          "168h",
		"backup_assets.provider_operation_timeout":       "2m",
		"backup_assets.provider_max_concurrency":         "4",
		"backup_assets.provider_metadata_limit_bytes":    "16777216",
		"backup_assets.publication_reconcile_interval":   "5m",
		"backup_assets.publication_reconcile_batch_size": "100",
		"backup_assets.publication_worker_concurrency":   "2",
		"backup_assets.publication_missing_grace":        "30m",
		"backup_assets.publication_stream_max_bytes":     "268435456",
		"backup_assets.manifest_timeout":                 "2h",
		"backup_assets.manifest_max_bytes":               "4294967296",
		"backup_assets.manifest_max_entries":             "10000000",
		"backup_assets.manifest_max_record_bytes":        "1048576",
		"backup_assets.manifest_max_depth":               "4096",
	}
}
