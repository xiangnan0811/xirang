package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/model"
)

func TestCatalogWorkerBoundsConcurrencySerializesRepositoriesAndKeepsFairness(t *testing.T) {
	settings := workerSettingsFromEnabled(true)
	settings["backup_assets.provider_max_concurrency"] = "2"
	foundation := backupasset.NewFoundationService(settings)
	backend := newCatalogWorkerBackendFake([]catalog.BuildCandidate{
		{RepositoryID: catalogWorkerID('a'), RecoveryPointID: catalogWorkerID('1')},
		{RepositoryID: catalogWorkerID('a'), RecoveryPointID: catalogWorkerID('2')},
		{RepositoryID: catalogWorkerID('b'), RecoveryPointID: catalogWorkerID('3')},
		{RepositoryID: catalogWorkerID('c'), RecoveryPointID: catalogWorkerID('4')},
	})
	worker, err := NewCatalogWorker(CatalogWorkerDependencies{
		GC:         newCatalogWorkerGCFake(),
		Foundation: foundation, Backend: backend, Metrics: catalog.NoopMetrics{},
		Now: func() time.Time { return time.Date(2026, 7, 18, 2, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- worker.runScan(context.Background()) }()

	first := backend.nextBuild(t)
	second := backend.nextBuild(t)
	if first.candidate.RepositoryID == second.candidate.RepositoryID {
		t.Fatalf("first concurrent builds shared repository: %+v %+v", first.candidate, second.candidate)
	}
	if backend.maximumActive() != 2 || backend.overlappedRepository() {
		t.Fatalf("max active=%d repository overlap=%t", backend.maximumActive(), backend.overlappedRepository())
	}
	close(first.release)
	third := backend.nextBuild(t)
	if third.candidate.RepositoryID == first.candidate.RepositoryID {
		t.Fatalf("large repository was scheduled twice before another ready repository: first=%+v third=%+v", first.candidate, third.candidate)
	}
	close(second.release)
	fourth := backend.nextBuild(t)
	close(third.release)
	close(fourth.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if backend.reconcileCalls != 1 || backend.listCalls != 1 || backend.overlappedRepository() {
		t.Fatalf("reconcile=%d list=%d overlap=%t", backend.reconcileCalls, backend.listCalls, backend.overlappedRepository())
	}
}

func TestCatalogWorkerStartupIsAsyncPeriodicAndDynamicallyDisabled(t *testing.T) {
	settings := workerSettingsFromEnabled(true)
	settings["backup_assets.repository_reconcile_interval"] = "1m"
	foundation := backupasset.NewFoundationService(settings)
	backend := newCatalogWorkerBackendFake(nil)
	ticks := make(chan time.Time, 2)
	afterCalled := make(chan time.Duration, 2)
	metrics := &catalogWorkerMetricsFake{scans: make(chan catalog.MetricScanOutcome, 4), storage: make(chan catalog.StorageObservation, 4)}
	gc := newCatalogWorkerGCFake()
	worker, err := NewCatalogWorker(CatalogWorkerDependencies{
		GC:         gc,
		Foundation: foundation, Backend: backend, Metrics: metrics,
		After: func(duration time.Duration) <-chan time.Time {
			afterCalled <- duration
			return ticks
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		worker.Run(runCtx)
		close(runDone)
	}()
	select {
	case duration := <-afterCalled:
		if duration != time.Minute {
			t.Fatalf("periodic duration=%s", duration)
		}
	case <-time.After(time.Second):
		t.Fatal("Run blocked on startup scan before arming the periodic timer")
	}
	backend.waitForListCalls(t, 1)
	if outcome := <-metrics.scans; outcome != catalog.MetricScanSuccess {
		t.Fatalf("startup scan outcome=%q", outcome)
	}
	if gc.callCount() != 1 {
		t.Fatalf("enabled scan reclamation calls=%d want 1", gc.callCount())
	}
	settings["backup_assets.enabled"] = "false"
	ticks <- time.Now()
	if outcome := <-metrics.scans; outcome != catalog.MetricScanDisabled {
		t.Fatalf("disabled scan outcome=%q", outcome)
	}
	// A disabled scan still reclaims generations and refreshes the storage
	// gauges, but must never schedule Catalog candidates or builds.
	if gc.callCount() != 2 {
		t.Fatalf("disabled scan reclamation calls=%d want 2", gc.callCount())
	}
	if metrics.storageObservationCount() < 2 {
		t.Fatalf("disabled scan storage observations=%d want at least 2", metrics.storageObservationCount())
	}
	if backend.listCallCount() != 1 || backend.reconcileCallCount() != 1 ||
		backend.buildCallCount() != 0 || backend.maximumActive() != 0 {
		t.Fatalf("disabled scan scheduled Catalog work: list=%d reconcile=%d builds=%d",
			backend.listCallCount(), backend.reconcileCallCount(), backend.buildCallCount())
	}
	cancel()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
}

func TestCatalogWorkerWakeInterruptsLongPeriodicWaitAndCoalesces(t *testing.T) {
	backend := newCatalogWorkerBackendFake(nil)
	ticks := make(chan time.Time)
	afterEntered := make(chan struct{})
	afterRelease := make(chan struct{})
	var afterOnce sync.Once
	var releaseAfterOnce sync.Once
	after := func(time.Duration) <-chan time.Time {
		first := false
		afterOnce.Do(func() {
			first = true
			close(afterEntered)
		})
		if first {
			<-afterRelease
		}
		return ticks
	}
	releaseAfter := func() {
		releaseAfterOnce.Do(func() { close(afterRelease) })
	}
	worker, err := NewCatalogWorker(CatalogWorkerDependencies{
		GC:         newCatalogWorkerGCFake(),
		Foundation: workerFoundation(true), Backend: backend, Metrics: catalog.NoopMetrics{},
		After: after,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		worker.Run(ctx)
		close(runDone)
	}()
	defer func() {
		releaseAfter()
		cancel()
		select {
		case <-runDone:
		case <-time.After(time.Second):
			t.Error("Catalog worker did not stop during cleanup")
		}
	}()
	backend.waitForListCalls(t, 1)
	select {
	case <-afterEntered:
	case <-time.After(time.Second):
		t.Fatal("Catalog worker did not reach the periodic timer barrier")
	}

	// Hold Run before its select can consume the first wake. This makes the
	// duplicate assertion exercise the capacity-one pending-wake contract.
	if !worker.TryWake() {
		t.Fatal("first Catalog wake was not accepted")
	}
	if worker.TryWake() {
		t.Fatal("duplicate Catalog wake was not coalesced")
	}
	releaseAfter()

	// No periodic tick is delivered; the queued wake must still interrupt the
	// long wait and produce exactly one follow-up scan.
	backend.waitForListCalls(t, 2)
	if got := backend.listCallCount(); got != 2 {
		t.Fatalf("wake-triggered Catalog scans=%d, want initial plus one wake", got)
	}
}

func TestCatalogWorkerTryWakeNeverBlocksWhenQueueIsSaturated(t *testing.T) {
	worker, err := NewCatalogWorker(CatalogWorkerDependencies{
		GC:         newCatalogWorkerGCFake(),
		Foundation: workerFoundation(true), Backend: newCatalogWorkerBackendFake(nil), Metrics: catalog.NoopMetrics{},
		After: func(time.Duration) <-chan time.Time { return make(chan time.Time) },
	})
	if err != nil {
		t.Fatal(err)
	}
	worker.wake <- struct{}{}
	result := make(chan bool, 1)
	go func() { result <- worker.TryWake() }()

	select {
	case accepted := <-result:
		if accepted {
			t.Fatal("saturated Catalog wake queue accepted another request")
		}
	case <-time.After(100 * time.Millisecond):
		<-worker.wake
		<-result
		t.Fatal("TryWake blocked on a saturated Catalog wake queue")
	}
}

func TestCatalogWorkerWakeDuringScanRemainsPending(t *testing.T) {
	backend := newCatalogWorkerBackendFake([]catalog.BuildCandidate{{RepositoryID: catalogWorkerID('a'), RecoveryPointID: catalogWorkerID('1')}})
	worker, err := NewCatalogWorker(CatalogWorkerDependencies{
		GC:         newCatalogWorkerGCFake(),
		Foundation: workerFoundation(true), Backend: backend, Metrics: catalog.NoopMetrics{},
		After: func(time.Duration) <-chan time.Time { return make(chan time.Time) },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)
	first := backend.nextBuild(t)
	if !worker.TryWake() {
		t.Fatal("wake during active Catalog scan was not accepted")
	}
	close(first.release)
	second := backend.nextBuild(t)
	close(second.release)
	backend.waitForListCalls(t, 2)
}

func TestCatalogWorkerOverduePeriodicPassWinsOverSustainedWake(t *testing.T) {
	backend := newCatalogWorkerBackendFake([]catalog.BuildCandidate{{RepositoryID: catalogWorkerID('a'), RecoveryPointID: catalogWorkerID('1')}})
	ticks := make(chan time.Time)
	afterCalled := make(chan time.Duration, 4)
	worker, err := NewCatalogWorker(CatalogWorkerDependencies{
		GC:         newCatalogWorkerGCFake(),
		Foundation: workerFoundation(true), Backend: backend, Metrics: catalog.NoopMetrics{},
		After: func(duration time.Duration) <-chan time.Time {
			afterCalled <- duration
			return ticks
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)
	first := backend.nextBuild(t)
	waitForCatalogAfterCalls(t, afterCalled, 1)

	if !worker.TryWake() {
		t.Fatal("wake during initial Catalog scan was not accepted")
	}
	tickDelivered := make(chan struct{})
	go func() {
		ticks <- time.Now()
		close(tickDelivered)
	}()
	select {
	case <-tickDelivered:
	case <-time.After(time.Second):
		t.Fatal("overdue periodic tick was not observed during the active scan")
	}
	close(first.release)
	periodic := backend.nextBuild(t)
	if worker.TryWake() {
		t.Fatal("wake during overdue periodic Catalog scan was not coalesced with the pending wake")
	}
	close(periodic.release)
	waitForCatalogAfterCalls(t, afterCalled, 1)

	wakeFollowUp := backend.nextBuild(t)
	close(wakeFollowUp.release)
	backend.waitForListCalls(t, 3)
}

func TestCatalogWorkerChoosesDuePeriodicWhenScanCompletionAndTimerAreReadyTogether(t *testing.T) {
	worker, err := NewCatalogWorker(CatalogWorkerDependencies{
		GC:         newCatalogWorkerGCFake(),
		Foundation: workerFoundation(true), Backend: newCatalogWorkerBackendFake(nil), Metrics: catalog.NoopMetrics{},
		After: func(time.Duration) <-chan time.Time { return make(chan time.Time) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !worker.TryWake() {
		t.Fatal("pending Catalog wake was not accepted")
	}
	periodic := make(chan time.Time, 1)
	periodic <- time.Now()

	trigger, ready, remainingPeriodic := worker.nextIdleScan(periodic, false)
	if !ready || trigger != catalogScanPeriodic {
		t.Fatalf("simultaneously ready next scan=(%d, %t), want periodic", trigger, ready)
	}
	if remainingPeriodic != nil {
		t.Fatal("due periodic timer was not consumed")
	}
	if !worker.hasPendingWake() {
		t.Fatal("periodic priority consumed the pending wake")
	}
}

func TestCatalogWorkerPreRunWakeFoldsIntoInitialPass(t *testing.T) {
	backend := newCatalogWorkerBackendFake(nil)
	worker, err := NewCatalogWorker(CatalogWorkerDependencies{
		GC:         newCatalogWorkerGCFake(),
		Foundation: workerFoundation(true), Backend: backend, Metrics: catalog.NoopMetrics{},
		After: func(time.Duration) <-chan time.Time { return make(chan time.Time) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !worker.TryWake() {
		t.Fatal("pre-Run Catalog wake was not accepted")
	}
	if worker.TryWake() {
		t.Fatal("duplicate pre-Run Catalog wake was not coalesced")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)
	backend.waitForListCalls(t, 1)
	time.Sleep(20 * time.Millisecond)
	if got := backend.listCallCount(); got != 1 {
		t.Fatalf("pre-Run wake caused %d scans, want one initial pass", got)
	}
}

func TestCatalogWorkerShutdownRevokesCancelsAndBoundedlyJoins(t *testing.T) {
	backend := newCatalogWorkerBackendFake([]catalog.BuildCandidate{{RepositoryID: catalogWorkerID('a'), RecoveryPointID: catalogWorkerID('1')}})
	backend.ignoreCancellation = true
	metrics := &catalogWorkerMetricsFake{scans: make(chan catalog.MetricScanOutcome, 4)}
	worker, err := NewCatalogWorker(CatalogWorkerDependencies{
		GC:         newCatalogWorkerGCFake(),
		Foundation: workerFoundation(true), Backend: backend, Metrics: metrics,
		After: func(time.Duration) <-chan time.Time { return make(chan time.Time) },
	})
	if err != nil {
		t.Fatal(err)
	}
	go worker.Run(context.Background())
	call := backend.nextBuild(t)
	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		shutdownDone <- worker.Shutdown(ctx)
	}()
	backend.waitForRevocation(t)
	select {
	case <-call.canceled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the active Catalog build")
	}
	close(call.release)
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	if err := worker.Shutdown(context.Background()); err != nil {
		t.Fatalf("repeated shutdown: %v", err)
	}
	if worker.TryWake() {
		t.Fatal("stopped Catalog worker accepted a wake")
	}
	if got := metrics.activeBuildCount(); got != 0 {
		t.Fatalf("Catalog active-build gauge=%d after shutdown, want 0", got)
	}

	stuckBackend := newCatalogWorkerBackendFake([]catalog.BuildCandidate{{RepositoryID: catalogWorkerID('b'), RecoveryPointID: catalogWorkerID('2')}})
	stuckBackend.ignoreCancellation = true
	stuckWorker, err := NewCatalogWorker(CatalogWorkerDependencies{
		GC:         newCatalogWorkerGCFake(),
		Foundation: workerFoundation(true), Backend: stuckBackend, Metrics: catalog.NoopMetrics{},
		After: func(time.Duration) <-chan time.Time { return make(chan time.Time) },
	})
	if err != nil {
		t.Fatal(err)
	}
	go stuckWorker.Run(context.Background())
	stuckCall := stuckBackend.nextBuild(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := stuckWorker.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stuck shutdown error=%v", err)
	}
	close(stuckCall.release)
}

func TestCatalogWorkerActiveBuildGaugeCannotPublishStaleCountAfterJoin(t *testing.T) {
	backend := newCatalogWorkerBackendFake(nil)
	metrics := newOrderedCatalogWorkerMetricsFake()
	worker, err := NewCatalogWorker(CatalogWorkerDependencies{
		GC:         newCatalogWorkerGCFake(),
		Foundation: workerFoundation(true), Backend: backend, Metrics: metrics,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{}, 2)
	for _, candidate := range []catalog.BuildCandidate{
		{RepositoryID: catalogWorkerID('a'), RecoveryPointID: catalogWorkerID('1')},
		{RepositoryID: catalogWorkerID('b'), RecoveryPointID: catalogWorkerID('2')},
	} {
		candidate := candidate
		go func() {
			worker.buildCandidate(context.Background(), candidate)
			done <- struct{}{}
		}()
	}
	first := backend.nextBuild(t)
	second := backend.nextBuild(t)
	close(first.release)
	select {
	case <-metrics.staleOneBlocked:
	case <-time.After(time.Second):
		t.Fatal("first decrement did not reach the controlled gauge update")
	}
	close(second.release)
	select {
	case <-metrics.zeroPublished:
	case <-time.After(100 * time.Millisecond):
		// A serialized publisher correctly holds zero until the earlier update finishes.
	}
	close(metrics.releaseStaleOne)
	for range 2 {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Catalog build did not join after the gauge was released")
		}
	}
	if got := metrics.activeBuildCount(); got != 0 {
		t.Fatalf("Catalog active-build gauge=%d after concurrent builds joined, want 0", got)
	}
}

func TestCatalogRetryDelayIsDeterministicBoundedAndResets(t *testing.T) {
	config := backupasset.CatalogConfig{ReconcileInterval: 15 * time.Minute, BuildTimeout: 2 * time.Hour}
	pointID := catalogWorkerID('a')
	generationID := catalogWorkerID('b')
	first := catalogRetryDelay(config, 1, pointID, generationID)
	if first != catalogRetryDelay(config, 1, pointID, generationID) {
		t.Fatal("Catalog retry jitter was not deterministic")
	}
	base := 5 * time.Minute
	if first < time.Duration(float64(base)*0.8) || first > time.Duration(float64(base)*1.2) {
		t.Fatalf("first retry delay=%s outside jittered base", first)
	}
	capped := catalogRetryDelay(config, 99, pointID, generationID)
	capDuration := time.Hour
	if capped < time.Duration(float64(capDuration)*0.8) || capped > time.Duration(float64(capDuration)*1.2) {
		t.Fatalf("capped retry delay=%s", capped)
	}
	if reset := catalogRetryDelay(config, 0, pointID, generationID); reset != 0 {
		t.Fatalf("reset retry delay=%s want zero", reset)
	}
}

// TestCatalogWorkerPublishesReclamationOutcomes proves one scan publishes both
// reclamation counters from the collector's result, on the disabled path too, so
// an operator can see reclaim progress while the feature is off.
func TestCatalogWorkerPublishesReclamationOutcomes(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			gc := newCatalogWorkerGCFake()
			gc.result = CatalogGCResult{DeletedGenerations: 2, SkippedRestricted: 3}
			metrics := &catalogWorkerMetricsFake{scans: make(chan catalog.MetricScanOutcome, 4)}
			worker, err := NewCatalogWorker(CatalogWorkerDependencies{
				GC:         gc,
				Foundation: workerFoundation(enabled), Backend: newCatalogWorkerBackendFake(nil), Metrics: metrics,
				After: func(time.Duration) <-chan time.Time { return make(chan time.Time) },
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.runScan(context.Background()); err != nil {
				t.Fatalf("scan: %v", err)
			}
			deleted, skipped := metrics.gcCounts()
			if deleted != 2 || skipped != 3 {
				t.Fatalf("reclamation counters deleted=%d skipped=%d want 2/3", deleted, skipped)
			}
			if gc.callCount() != 1 {
				t.Fatalf("reclamation calls=%d want 1", gc.callCount())
			}
		})
	}
}

// TestCatalogWorkerCountsRebuildNotRequiredAsSkipped proves the Build
// short-circuit is a successful, non-failing scan outcome: the pass still
// succeeds and the build is recorded as skipped, never as complete or failed.
func TestCatalogWorkerCountsRebuildNotRequiredAsSkipped(t *testing.T) {
	backend := newCatalogWorkerBackendFake([]catalog.BuildCandidate{
		{RepositoryID: catalogWorkerID('a'), RecoveryPointID: catalogWorkerID('1')},
	})
	backend.buildErr = catalog.ErrCatalogRebuildNotRequired
	metrics := &catalogWorkerMetricsFake{
		scans: make(chan catalog.MetricScanOutcome, 4), builds: make(chan catalog.MetricBuildOutcome, 4),
	}
	worker, err := NewCatalogWorker(CatalogWorkerDependencies{
		GC:         newCatalogWorkerGCFake(),
		Foundation: workerFoundation(true), Backend: backend, Metrics: metrics,
		After: func(time.Duration) <-chan time.Time { return make(chan time.Time) },
	})
	if err != nil {
		t.Fatal(err)
	}
	scanDone := make(chan error, 1)
	go func() { scanDone <- worker.runScan(context.Background()) }()
	call := backend.nextBuild(t)
	if call.candidate.RecoveryPointID != catalogWorkerID('1') {
		t.Fatalf("unexpected build candidate: %+v", call.candidate)
	}
	close(call.release)
	if err := <-scanDone; err != nil {
		t.Fatalf("scan over a skipped rebuild: %v", err)
	}
	select {
	case outcome := <-metrics.builds:
		if outcome != catalog.MetricBuildSkipped {
			t.Fatalf("build outcome=%s want %s", outcome, catalog.MetricBuildSkipped)
		}
	case <-time.After(time.Second):
		t.Fatal("skipped rebuild was not recorded")
	}
	select {
	case outcome := <-metrics.scans:
		if outcome != catalog.MetricScanSuccess {
			t.Fatalf("scan outcome=%s want %s", outcome, catalog.MetricScanSuccess)
		}
	case <-time.After(time.Second):
		t.Fatal("scan outcome was not recorded")
	}
}

// TestCatalogWorkerStorageFactsFollowBothScanOutcomes proves the scan-end
// storage aggregate is published on a completed scan and on a disabled scan,
// while a disabled scan still schedules no Catalog candidate or build.
func TestCatalogWorkerStorageFactsFollowBothScanOutcomes(t *testing.T) {
	backend := newCatalogWorkerBackendFake(nil)
	backend.storage = catalog.StorageObservation{
		GenerationsByState:     map[string]int64{string(catalog.GenerationComplete): 2},
		MaxGenerationsPerPoint: 4, EntryCount: 42, SQLiteFileBytes: 1024,
	}
	metrics := &catalogWorkerMetricsFake{
		scans:   make(chan catalog.MetricScanOutcome, 4),
		storage: make(chan catalog.StorageObservation, 4),
	}
	worker, err := NewCatalogWorker(CatalogWorkerDependencies{
		GC:         newCatalogWorkerGCFake(),
		Foundation: workerFoundation(true), Backend: backend, Metrics: metrics,
		After: func(time.Duration) <-chan time.Time { return make(chan time.Time) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.runScan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	select {
	case observation := <-metrics.storage:
		if observation.MaxGenerationsPerPoint != 4 || observation.EntryCount != 42 ||
			observation.SQLiteFileBytes != 1024 || observation.GenerationsByState[string(catalog.GenerationComplete)] != 2 {
			t.Fatalf("storage observation=%+v", observation)
		}
	case <-time.After(time.Second):
		t.Fatal("scan-end storage aggregate was not published")
	}
	if backend.storageCallCount() != 1 {
		t.Fatalf("storage collection calls=%d want 1", backend.storageCallCount())
	}

	disabledBackend := newCatalogWorkerBackendFake(nil)
	disabledBackend.storage = catalog.StorageObservation{EntryCount: 7}
	disabledGC := newCatalogWorkerGCFake()
	disabledMetrics := &catalogWorkerMetricsFake{
		scans:   make(chan catalog.MetricScanOutcome, 4),
		storage: make(chan catalog.StorageObservation, 4),
	}
	disabledWorker, err := NewCatalogWorker(CatalogWorkerDependencies{
		GC:         disabledGC,
		Foundation: workerFoundation(false), Backend: disabledBackend, Metrics: disabledMetrics,
		After: func(time.Duration) <-chan time.Time { return make(chan time.Time) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := disabledWorker.runScan(context.Background()); err != nil {
		t.Fatalf("disabled scan: %v", err)
	}
	select {
	case observation := <-disabledMetrics.storage:
		if observation.EntryCount != 7 {
			t.Fatalf("disabled storage observation=%+v", observation)
		}
	case <-time.After(time.Second):
		t.Fatal("disabled scan did not publish storage facts")
	}
	if disabledBackend.storageCallCount() != 1 {
		t.Fatalf("disabled storage collection calls=%d want 1", disabledBackend.storageCallCount())
	}
	if disabledGC.callCount() != 1 {
		t.Fatalf("disabled reclamation calls=%d want 1", disabledGC.callCount())
	}
	if disabledBackend.listCallCount() != 0 || disabledBackend.buildCallCount() != 0 ||
		disabledBackend.reconcileCallCount() != 0 {
		t.Fatalf("disabled scan scheduled Catalog work: list=%d builds=%d reconcile=%d",
			disabledBackend.listCallCount(), disabledBackend.buildCallCount(), disabledBackend.reconcileCallCount())
	}
}

type catalogWorkerBuildCall struct {
	candidate catalog.BuildCandidate
	release   chan struct{}
	canceled  chan struct{}
}

type catalogWorkerBackendFake struct {
	mu                 sync.Mutex
	candidates         []catalog.BuildCandidate
	builds             chan *catalogWorkerBuildCall
	reconcileCalls     int
	listCalls          int
	buildCalls         int
	revokeCalls        int
	storageCalls       int
	storageErr         error
	storage            catalog.StorageObservation
	active             int
	maxActive          int
	activeRepositories map[string]int
	repositoryOverlap  bool
	ignoreCancellation bool
	buildErr           error
	revoked            chan struct{}
	revokeOnce         sync.Once
}

func newCatalogWorkerBackendFake(candidates []catalog.BuildCandidate) *catalogWorkerBackendFake {
	return &catalogWorkerBackendFake{
		candidates: append([]catalog.BuildCandidate(nil), candidates...), builds: make(chan *catalogWorkerBuildCall, 32),
		activeRepositories: map[string]int{}, revoked: make(chan struct{}),
	}
}

func (backend *catalogWorkerBackendFake) ListCandidates(context.Context, int, time.Time, backupasset.CatalogConfig) ([]catalog.BuildCandidate, error) {
	backend.mu.Lock()
	backend.listCalls++
	result := append([]catalog.BuildCandidate(nil), backend.candidates...)
	backend.mu.Unlock()
	return result, nil
}

func (backend *catalogWorkerBackendFake) ReconcileAbandoned(context.Context, time.Duration, int) (int, error) {
	backend.mu.Lock()
	backend.reconcileCalls++
	backend.mu.Unlock()
	return 1, nil
}

func (backend *catalogWorkerBackendFake) Build(ctx context.Context, request catalog.BuildRequest) (model.CatalogGeneration, error) {
	call := &catalogWorkerBuildCall{
		candidate: catalog.BuildCandidate{RepositoryID: request.RepositoryID, RecoveryPointID: request.RecoveryPointID},
		release:   make(chan struct{}), canceled: make(chan struct{}),
	}
	backend.mu.Lock()
	backend.active++
	backend.buildCalls++
	if backend.active > backend.maxActive {
		backend.maxActive = backend.active
	}
	backend.activeRepositories[request.RepositoryID]++
	if backend.activeRepositories[request.RepositoryID] > 1 {
		backend.repositoryOverlap = true
	}
	backend.mu.Unlock()
	backend.builds <- call
	var err error
	select {
	case <-call.release:
	case <-ctx.Done():
		close(call.canceled)
		if backend.ignoreCancellation {
			<-call.release
		}
		err = ctx.Err()
	}
	backend.mu.Lock()
	backend.active--
	backend.activeRepositories[request.RepositoryID]--
	buildErr := backend.buildErr
	backend.mu.Unlock()
	if buildErr != nil {
		return model.CatalogGeneration{ID: strings.Repeat("f", 32), State: string(catalog.GenerationComplete), IsActive: true}, errors.Join(err, buildErr)
	}
	return model.CatalogGeneration{State: string(catalog.GenerationComplete)}, err
}

func (backend *catalogWorkerBackendFake) ObserveStorageFacts(context.Context) (catalog.StorageObservation, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.storageCalls++
	if backend.storageErr != nil {
		return catalog.StorageObservation{}, backend.storageErr
	}
	return backend.storage, nil
}

func (backend *catalogWorkerBackendFake) storageCallCount() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.storageCalls
}

func (backend *catalogWorkerBackendFake) buildCallCount() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.buildCalls
}

func (backend *catalogWorkerBackendFake) RevokeActiveBuilds(context.Context) error {
	backend.mu.Lock()
	backend.revokeCalls++
	backend.mu.Unlock()
	backend.revokeOnce.Do(func() { close(backend.revoked) })
	return nil
}

func (backend *catalogWorkerBackendFake) nextBuild(t *testing.T) *catalogWorkerBuildCall {
	t.Helper()
	select {
	case call := <-backend.builds:
		return call
	case <-time.After(time.Second):
		t.Fatal("Catalog build did not start")
		return nil
	}
}

func (backend *catalogWorkerBackendFake) waitForListCalls(t *testing.T, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if backend.listCallCount() >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("list calls=%d want at least %d", backend.listCallCount(), count)
}

func (backend *catalogWorkerBackendFake) waitForRevocation(t *testing.T) {
	t.Helper()
	select {
	case <-backend.revoked:
	case <-time.After(time.Second):
		t.Fatal("active Catalog fences were not revoked")
	}
}

func (backend *catalogWorkerBackendFake) maximumActive() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.maxActive
}

func (backend *catalogWorkerBackendFake) overlappedRepository() bool {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.repositoryOverlap
}

func (backend *catalogWorkerBackendFake) listCallCount() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.listCalls
}

func (backend *catalogWorkerBackendFake) reconcileCallCount() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.reconcileCalls
}

type catalogWorkerGCFake struct {
	mu     sync.Mutex
	calls  int
	result CatalogGCResult
	err    error
}

func newCatalogWorkerGCFake() *catalogWorkerGCFake { return &catalogWorkerGCFake{} }

func (gc *catalogWorkerGCFake) Collect(context.Context) (CatalogGCResult, error) {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	gc.calls++
	return gc.result, gc.err
}

func (gc *catalogWorkerGCFake) callCount() int {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	return gc.calls
}

type catalogWorkerMetricsFake struct {
	mu                   sync.Mutex
	scans                chan catalog.MetricScanOutcome
	builds               chan catalog.MetricBuildOutcome
	storage              chan catalog.StorageObservation
	storageCalls         int
	gcDeletedGenerations int
	gcSkippedRestricted  int
	activeBuilds         int
}

type orderedCatalogWorkerMetricsFake struct {
	mu              sync.Mutex
	activeBuilds    int
	oneCalls        int
	staleOneBlocked chan struct{}
	releaseStaleOne chan struct{}
	zeroPublished   chan struct{}
	staleBlockOnce  sync.Once
	zeroOnce        sync.Once
}

func newOrderedCatalogWorkerMetricsFake() *orderedCatalogWorkerMetricsFake {
	return &orderedCatalogWorkerMetricsFake{
		staleOneBlocked: make(chan struct{}), releaseStaleOne: make(chan struct{}), zeroPublished: make(chan struct{}),
	}
}

func (*orderedCatalogWorkerMetricsFake) ObserveBuild(catalog.MetricBuildOutcome, time.Duration) {}
func (*orderedCatalogWorkerMetricsFake) ObserveScan(catalog.MetricScanOutcome)                  {}
func (*orderedCatalogWorkerMetricsFake) AddReconciledAbandoned(int)                             {}
func (*orderedCatalogWorkerMetricsFake) AddGCDeletedGenerations(int)                            {}
func (*orderedCatalogWorkerMetricsFake) AddGCSkippedRestricted(int)                             {}
func (*orderedCatalogWorkerMetricsFake) ObserveStorage(catalog.StorageObservation)              {}
func (metrics *orderedCatalogWorkerMetricsFake) SetActiveBuilds(count int) {
	metrics.mu.Lock()
	if count == 1 {
		metrics.oneCalls++
	}
	block := count == 1 && metrics.oneCalls == 2
	metrics.mu.Unlock()
	if block {
		metrics.staleBlockOnce.Do(func() { close(metrics.staleOneBlocked) })
		<-metrics.releaseStaleOne
	}
	metrics.mu.Lock()
	metrics.activeBuilds = count
	metrics.mu.Unlock()
	if count == 0 {
		metrics.zeroOnce.Do(func() { close(metrics.zeroPublished) })
	}
}

func (metrics *orderedCatalogWorkerMetricsFake) activeBuildCount() int {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	return metrics.activeBuilds
}

func (metrics *catalogWorkerMetricsFake) ObserveBuild(outcome catalog.MetricBuildOutcome, _ time.Duration) {
	if metrics.builds != nil {
		metrics.builds <- outcome
	}
}
func (metrics *catalogWorkerMetricsFake) ObserveScan(outcome catalog.MetricScanOutcome) {
	metrics.scans <- outcome
}
func (metrics *catalogWorkerMetricsFake) SetActiveBuilds(count int) {
	metrics.mu.Lock()
	metrics.activeBuilds = count
	metrics.mu.Unlock()
}
func (*catalogWorkerMetricsFake) AddReconciledAbandoned(int) {}
func (metrics *catalogWorkerMetricsFake) AddGCDeletedGenerations(count int) {
	metrics.mu.Lock()
	metrics.gcDeletedGenerations += count
	metrics.mu.Unlock()
}
func (metrics *catalogWorkerMetricsFake) AddGCSkippedRestricted(count int) {
	metrics.mu.Lock()
	metrics.gcSkippedRestricted += count
	metrics.mu.Unlock()
}

func (metrics *catalogWorkerMetricsFake) gcCounts() (int, int) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	return metrics.gcDeletedGenerations, metrics.gcSkippedRestricted
}
func (metrics *catalogWorkerMetricsFake) ObserveStorage(observation catalog.StorageObservation) {
	metrics.mu.Lock()
	metrics.storageCalls++
	metrics.mu.Unlock()
	if metrics.storage != nil {
		metrics.storage <- observation
	}
}

func (metrics *catalogWorkerMetricsFake) storageObservationCount() int {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	return metrics.storageCalls
}

func (metrics *catalogWorkerMetricsFake) activeBuildCount() int {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	return metrics.activeBuilds
}

func waitForCatalogAfterCalls(t *testing.T, calls <-chan time.Duration, want int) {
	t.Helper()
	for index := 0; index < want; index++ {
		select {
		case <-calls:
		case <-time.After(time.Second):
			t.Fatalf("Catalog periodic timer armed %d times, want at least %d", index, want)
		}
	}
}

func catalogWorkerID(character byte) string {
	return strings.Repeat(string(character), 32)
}
