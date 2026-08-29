package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
)

const (
	catalogCandidateMinimum = 200
	catalogCandidateMaximum = 2000
	catalogAbandonedLimit   = 1000
)

type CatalogWorkerBackend interface {
	ListCandidates(context.Context, int, time.Time, backupasset.CatalogConfig) ([]catalog.BuildCandidate, error)
	ReconcileAbandoned(context.Context, time.Duration, int) (int, error)
	Build(context.Context, catalog.BuildRequest) (model.CatalogGeneration, error)
	RevokeActiveBuilds(context.Context) error
}

type CatalogWorkerDependencies struct {
	Foundation *backupasset.FoundationService
	Backend    CatalogWorkerBackend
	Metrics    catalog.Metrics
	Now        func() time.Time
	After      func(time.Duration) <-chan time.Time
}

type CatalogWorker struct {
	foundation *backupasset.FoundationService
	backend    CatalogWorkerBackend
	metrics    catalog.Metrics
	now        func() time.Time
	after      func(time.Duration) <-chan time.Time
	wake       chan struct{}

	mu          sync.Mutex
	stopping    bool
	runFinished bool
	wakePending bool
	scanning    bool
	runCancel   context.CancelFunc
	stop        chan struct{}
	wg          sync.WaitGroup

	activeBuildMu sync.Mutex
	activeBuilds  int
}

type catalogScanTrigger uint8

const (
	catalogScanInitial catalogScanTrigger = iota
	catalogScanWake
	catalogScanPeriodic
)

func NewCatalogWorker(dependencies CatalogWorkerDependencies) (*CatalogWorker, error) {
	if dependencies.Foundation == nil || dependencies.Backend == nil || dependencies.Metrics == nil {
		return nil, fmt.Errorf("%w: Catalog worker dependencies unavailable", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	if dependencies.After == nil {
		dependencies.After = time.After
	}
	return &CatalogWorker{
		foundation: dependencies.Foundation, backend: dependencies.Backend, metrics: dependencies.Metrics,
		now: dependencies.Now, after: dependencies.After, wake: make(chan struct{}, 1), stop: make(chan struct{}),
	}, nil
}

// TryWake coalesces a request for the lifecycle-owned Run loop without blocking
// the caller. A wake queued before Run is absorbed by the initial pass.
func (worker *CatalogWorker) TryWake() bool {
	if worker == nil {
		return false
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.stopping || worker.runFinished || worker.wakePending {
		return false
	}
	select {
	case worker.wake <- struct{}{}:
		worker.wakePending = true
		return true
	default:
		return false
	}
}

// Run starts the first scan asynchronously so HTTP readiness is never coupled
// to Provider enumeration. Wake passes never reset the independently tracked
// periodic deadline; only completion of a periodic pass arms the next one.
func (worker *CatalogWorker) Run(ctx context.Context) {
	if worker == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	worker.mu.Lock()
	if worker.stopping {
		worker.mu.Unlock()
		cancel()
		return
	}
	worker.runCancel = cancel
	worker.mu.Unlock()
	defer func() {
		cancel()
		worker.mu.Lock()
		worker.runCancel = nil
		worker.runFinished = true
		worker.mu.Unlock()
	}()
	scanDone := make(chan catalogScanTrigger, 1)
	scanActive := worker.startScan(runCtx, catalogScanInitial, scanDone)
	periodic, ok := worker.catalogPeriodicTimer()
	if !ok {
		return
	}
	periodicDue := false
	for {
		if !scanActive {
			trigger, ready, remainingPeriodic := worker.nextIdleScan(periodic, periodicDue)
			periodic = remainingPeriodic
			if ready {
				if trigger == catalogScanPeriodic {
					periodicDue = false
				}
				scanActive = worker.startScan(runCtx, trigger, scanDone)
			}
		}
		select {
		case <-runCtx.Done():
			return
		case <-worker.stop:
			return
		case <-worker.wake:
			// wakePending remains true until the next scan atomically consumes it.
		case <-periodic:
			periodicDue = true
			periodic = nil
		case trigger := <-scanDone:
			scanActive = false
			if trigger == catalogScanPeriodic {
				var armed bool
				periodic, armed = worker.catalogPeriodicTimer()
				if !armed {
					return
				}
			}
		}
	}
}

func (worker *CatalogWorker) nextIdleScan(periodic <-chan time.Time, periodicDue bool) (catalogScanTrigger, bool, <-chan time.Time) {
	if !periodicDue && periodic != nil {
		select {
		case <-periodic:
			periodicDue = true
			periodic = nil
		default:
		}
	}
	if periodicDue {
		return catalogScanPeriodic, true, periodic
	}
	if worker.hasPendingWake() {
		return catalogScanWake, true, periodic
	}
	return catalogScanInitial, false, periodic
}

func (worker *CatalogWorker) catalogPeriodicTimer() (<-chan time.Time, bool) {
	config, err := worker.foundation.CatalogConfig()
	if err != nil {
		logger.Module("backupasset.catalog").Error().Str("stage", "schedule").Msg("Catalog 调度配置不可用")
		return nil, false
	}
	return worker.after(config.ReconcileInterval), true
}

func (worker *CatalogWorker) hasPendingWake() bool {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.wakePending
}

func (worker *CatalogWorker) startScan(ctx context.Context, trigger catalogScanTrigger, done chan<- catalogScanTrigger) bool {
	worker.mu.Lock()
	if worker.stopping || worker.scanning {
		worker.mu.Unlock()
		worker.metrics.ObserveScan(catalog.MetricScanSkipped)
		return false
	}
	if worker.wakePending && trigger != catalogScanPeriodic {
		worker.wakePending = false
		select {
		case <-worker.wake:
		default:
		}
	}
	worker.scanning = true
	worker.wg.Add(1)
	worker.mu.Unlock()
	go func() {
		defer func() {
			worker.mu.Lock()
			worker.scanning = false
			worker.mu.Unlock()
			worker.wg.Done()
			done <- trigger
		}()
		if err := worker.runScan(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Module("backupasset.catalog").Warn().Str("stage", "scan").Msg("Catalog 调度扫描失败")
		}
	}()
	return true
}

func (worker *CatalogWorker) runScan(ctx context.Context) error {
	if worker == nil || worker.foundation == nil || worker.backend == nil {
		return fmt.Errorf("%w: Catalog worker unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	config, err := worker.foundation.CatalogConfig()
	if err != nil {
		worker.metrics.ObserveScan(catalog.MetricScanFailure)
		return err
	}
	if !config.Enabled {
		worker.metrics.ObserveScan(catalog.MetricScanDisabled)
		return nil
	}
	abandonedAfter := config.BuildTimeout
	if config.Lease.AbsoluteDeadline < abandonedAfter {
		abandonedAfter = config.Lease.AbsoluteDeadline
	}
	reconciled, err := worker.backend.ReconcileAbandoned(ctx, abandonedAfter, catalogAbandonedLimit)
	if err != nil {
		worker.metrics.ObserveScan(catalog.MetricScanFailure)
		return err
	}
	worker.metrics.AddReconciledAbandoned(reconciled)
	limit := config.MaxConcurrency * 20
	if limit < catalogCandidateMinimum {
		limit = catalogCandidateMinimum
	}
	if limit > catalogCandidateMaximum {
		limit = catalogCandidateMaximum
	}
	candidates, err := worker.backend.ListCandidates(ctx, limit, worker.now().UTC(), config)
	if err != nil {
		worker.metrics.ObserveScan(catalog.MetricScanFailure)
		return err
	}
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	if err := worker.runCandidates(ctx, candidates, config.MaxConcurrency); err != nil {
		worker.metrics.ObserveScan(catalog.MetricScanFailure)
		return err
	}
	worker.metrics.ObserveScan(catalog.MetricScanSuccess)
	return nil
}

func (worker *CatalogWorker) runCandidates(ctx context.Context, candidates []catalog.BuildCandidate, concurrency int) error {
	if concurrency <= 0 {
		return fmt.Errorf("%w: invalid Catalog worker concurrency", backupasset.ErrInvalidState)
	}
	queues := make(map[string][]catalog.BuildCandidate)
	repositories := make([]string, 0)
	seenPoints := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if backupasset.ValidateOpaqueID(candidate.RepositoryID) != nil || backupasset.ValidateOpaqueID(candidate.RecoveryPointID) != nil {
			return fmt.Errorf("%w: invalid Catalog worker candidate", backupasset.ErrInvalidState)
		}
		if _, exists := seenPoints[candidate.RecoveryPointID]; exists {
			continue
		}
		seenPoints[candidate.RecoveryPointID] = struct{}{}
		if _, exists := queues[candidate.RepositoryID]; !exists {
			repositories = append(repositories, candidate.RepositoryID)
		}
		queues[candidate.RepositoryID] = append(queues[candidate.RepositoryID], candidate)
	}
	type completion struct {
		repositoryID string
	}
	completed := make(chan completion, concurrency)
	ready := append([]string(nil), repositories...)
	active := 0
	for len(ready) > 0 || active > 0 {
		for active < concurrency && len(ready) > 0 && ctx.Err() == nil {
			repositoryID := ready[0]
			ready = ready[1:]
			queue := queues[repositoryID]
			candidate := queue[0]
			queues[repositoryID] = queue[1:]
			active++
			go func() {
				worker.buildCandidate(ctx, candidate)
				completed <- completion{repositoryID: candidate.RepositoryID}
			}()
		}
		if active == 0 {
			break
		}
		finished := <-completed
		active--
		if len(queues[finished.repositoryID]) > 0 && ctx.Err() == nil {
			ready = append(ready, finished.repositoryID)
		}
	}
	return ctx.Err()
}

func (worker *CatalogWorker) buildCandidate(ctx context.Context, candidate catalog.BuildCandidate) {
	config, err := worker.foundation.CatalogConfig()
	if err != nil || !config.Enabled {
		worker.metrics.ObserveBuild(catalog.MetricBuildSkipped, 0)
		return
	}
	startedAt := worker.now().UTC()
	worker.adjustActiveBuilds(1)
	defer worker.adjustActiveBuilds(-1)
	generation, buildErr := worker.backend.Build(ctx, catalog.BuildRequest{
		RepositoryID: candidate.RepositoryID, RecoveryPointID: candidate.RecoveryPointID,
	})
	outcome := catalog.MetricBuildComplete
	switch {
	case errors.Is(buildErr, context.Canceled), errors.Is(buildErr, context.DeadlineExceeded):
		outcome = catalog.MetricBuildCanceled
	case buildErr != nil && generation.State == string(catalog.GenerationPartial):
		outcome = catalog.MetricBuildPartial
	case buildErr != nil:
		outcome = catalog.MetricBuildFailed
	}
	worker.metrics.ObserveBuild(outcome, worker.now().UTC().Sub(startedAt))
}

func (worker *CatalogWorker) adjustActiveBuilds(delta int) {
	worker.activeBuildMu.Lock()
	defer worker.activeBuildMu.Unlock()
	worker.activeBuilds += delta
	worker.metrics.SetActiveBuilds(worker.activeBuilds)
}

// Shutdown cancels schedules, revokes every active durable point fence, then
// joins in-flight scans/builds within the caller's deadline.
func (worker *CatalogWorker) Shutdown(ctx context.Context) error {
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
	}
	cancel := worker.runCancel
	worker.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	revokeErr := worker.backend.RevokeActiveBuilds(ctx)
	done := make(chan struct{})
	go func() {
		worker.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return revokeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func catalogRetryDelay(config backupasset.CatalogConfig, failureCount int, pointID, latestGenerationID string) time.Duration {
	return catalog.RetryDelay(config, failureCount, pointID, latestGenerationID)
}

var _ interface {
	Run(context.Context)
	TryWake() bool
	Shutdown(context.Context) error
} = (*CatalogWorker)(nil)
