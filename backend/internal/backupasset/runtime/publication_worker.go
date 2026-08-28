package runtime

import (
	"context"
	"fmt"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
)

const publicationWakeBuffer = 1000

type PublicationWorkerDependencies struct {
	Foundation *backupasset.FoundationService
	Reconciler publication.Reconciler
	Observer   publication.CommitObserver
	Reporter   publication.InterruptedRunReporter
	Metrics    publication.Metrics
	Now        func() time.Time
}

// PublicationWorker consumes the durable preparing/verifying queue. Wakeups
// improve latency only; losing one is safe because periodic candidate scans
// always re-read the database queue.
type PublicationWorker struct {
	foundation *backupasset.FoundationService
	reconciler publication.Reconciler
	observer   publication.CommitObserver
	reporter   publication.InterruptedRunReporter
	metrics    publication.Metrics
	now        func() time.Time

	mu       sync.Mutex
	stopping bool
	active   map[string]context.CancelFunc
	observed map[string]struct{}
	running  int
	changed  chan struct{}
	wake     chan string
	stop     chan struct{}
	wg       sync.WaitGroup
}

func NewPublicationWorker(dependencies PublicationWorkerDependencies) (*PublicationWorker, error) {
	if dependencies.Foundation == nil || dependencies.Reconciler == nil || dependencies.Metrics == nil {
		return nil, fmt.Errorf("%w: publication worker dependencies are unavailable", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &PublicationWorker{
		foundation: dependencies.Foundation,
		reconciler: dependencies.Reconciler,
		observer:   dependencies.Observer,
		reporter:   dependencies.Reporter,
		metrics:    dependencies.Metrics,
		now:        dependencies.Now,
		active:     make(map[string]context.CancelFunc),
		observed:   make(map[string]struct{}),
		changed:    make(chan struct{}),
		wake:       make(chan string, publicationWakeBuffer),
		stop:       make(chan struct{}),
	}, nil
}

// StartupPass processes one bounded snapshot of candidates and waits for it.
// It is deliberately safe to call before Run starts and is used by runtime
// readiness before schedules become available.
func (worker *PublicationWorker) StartupPass(ctx context.Context) error {
	if worker == nil {
		return fmt.Errorf("%w: publication worker is unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := worker.requireRunning(); err != nil {
		return err
	}
	enabled, err := worker.foundation.FeatureEnabled()
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	config, err := worker.foundation.PublicationConfig()
	if err != nil {
		return err
	}
	candidates, err := worker.reconciler.ListCandidates(ctx, config.ReconcileBatchSize)
	if err != nil {
		return err
	}
	var batch sync.WaitGroup
	for _, pointID := range candidates {
		pointID := pointID
		batch.Add(1)
		go func() {
			defer batch.Done()
			worker.process(ctx, pointID)
		}()
	}
	batch.Wait()
	return nil
}

// TryWake accepts an opaque point ID without ever blocking a TaskRun. The
// channel intentionally remains open for the worker lifetime so producers
// cannot race Shutdown with a close.
func (worker *PublicationWorker) TryWake(pointID string) bool {
	if worker == nil || backupasset.ValidateOpaqueID(pointID) != nil {
		return false
	}
	worker.mu.Lock()
	stopping := worker.stopping
	worker.mu.Unlock()
	if stopping {
		return false
	}
	select {
	case worker.wake <- pointID:
		return true
	default:
		return false
	}
}

// Run serves wakeups and dynamic periodic passes until its context ends or
// Shutdown marks the worker stopping. Each path reaches the same process gate.
func (worker *PublicationWorker) Run(ctx context.Context) {
	if worker == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	config, err := worker.foundation.PublicationConfig()
	if err != nil {
		return
	}
	worker.runLoop(ctx, config)
}

func (worker *PublicationWorker) runLoop(ctx context.Context, config backupasset.PublicationConfig) {
	timer := time.NewTimer(config.ReconcileInterval)
	defer timer.Stop()
	for {
		if worker.isStopping() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-worker.stop:
			return
		case pointID := <-worker.wake:
			go worker.process(ctx, pointID)
		case <-timer.C:
			_ = worker.StartupPass(ctx)
			nextConfig, err := worker.foundation.PublicationConfig()
			if err != nil {
				return
			}
			config = nextConfig
			timer.Reset(config.ReconcileInterval)
		}
	}
}

// Shutdown prevents new claims, cancels all in-flight work, and joins it by
// the caller's deadline. It never closes the public producer wake channel.
func (worker *PublicationWorker) Shutdown(ctx context.Context) error {
	if worker == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	worker.mu.Lock()
	if !worker.stopping {
		worker.stopping = true
		close(worker.stop)
		for _, cancel := range worker.active {
			cancel()
		}
		worker.signalLocked()
	}
	worker.mu.Unlock()
	done := make(chan struct{})
	go func() {
		worker.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (worker *PublicationWorker) process(parent context.Context, pointID string) {
	if backupasset.ValidateOpaqueID(pointID) != nil {
		return
	}
	enabled, err := worker.foundation.FeatureEnabled()
	if err != nil || !enabled {
		return
	}
	workCtx, cancel, ok := worker.beginPoint(parent, pointID)
	if !ok {
		return
	}
	defer worker.endPoint(pointID, cancel)
	if !worker.acquireSlot(workCtx) {
		return
	}
	defer worker.releaseSlot()
	if err := workCtx.Err(); err != nil {
		return
	}
	// Re-read the dynamic switch after waiting for a concurrency slot. A
	// feature transition may have drained another command while this candidate
	// was queued; it must not begin a new claim after disable.
	enabled, err = worker.foundation.FeatureEnabled()
	if err != nil || !enabled {
		return
	}
	outcome, err := worker.reconciler.ProcessPoint(workCtx, pointID)
	if err != nil || outcome.RecoveryPointID == "" {
		return
	}
	if outcome.State == backupasset.RecoveryPointCommitted && worker.markObserved(outcome.RecoveryPointID) && worker.observer != nil {
		worker.observer.ObserveCommitted(workCtx, outcome)
	}
	if worker.reporter != nil {
		_ = worker.reporter.ReportInterruptedPublication(workCtx, outcome)
	}
}

func (worker *PublicationWorker) markObserved(pointID string) bool {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if _, exists := worker.observed[pointID]; exists {
		return false
	}
	worker.observed[pointID] = struct{}{}
	return true
}

func (worker *PublicationWorker) beginPoint(parent context.Context, pointID string) (context.Context, context.CancelFunc, bool) {
	if parent == nil {
		parent = context.Background()
	}
	workCtx, cancel := context.WithCancel(parent)
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.stopping {
		cancel()
		return nil, nil, false
	}
	if _, exists := worker.active[pointID]; exists {
		cancel()
		return nil, nil, false
	}
	worker.active[pointID] = cancel
	worker.wg.Add(1)
	return workCtx, cancel, true
}

func (worker *PublicationWorker) endPoint(pointID string, cancel context.CancelFunc) {
	cancel()
	worker.mu.Lock()
	delete(worker.active, pointID)
	worker.signalLocked()
	worker.mu.Unlock()
	worker.wg.Done()
}

func (worker *PublicationWorker) acquireSlot(ctx context.Context) bool {
	for {
		config, err := worker.foundation.PublicationConfig()
		if err != nil {
			return false
		}
		worker.mu.Lock()
		if worker.stopping {
			worker.mu.Unlock()
			return false
		}
		if worker.running < config.WorkerConcurrency {
			worker.running++
			worker.mu.Unlock()
			return true
		}
		changed := worker.changed
		worker.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-changed:
		}
	}
}

func (worker *PublicationWorker) releaseSlot() {
	worker.mu.Lock()
	if worker.running > 0 {
		worker.running--
	}
	worker.signalLocked()
	worker.mu.Unlock()
}

func (worker *PublicationWorker) requireRunning() error {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.stopping {
		return ErrAdmissionStopped
	}
	return nil
}

func (worker *PublicationWorker) isStopping() bool {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.stopping
}

func (worker *PublicationWorker) signalLocked() {
	close(worker.changed)
	worker.changed = make(chan struct{})
}

var _ interface {
	StartupPass(context.Context) error
	TryWake(string) bool
	Run(context.Context)
	Shutdown(context.Context) error
} = (*PublicationWorker)(nil)
