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

	mu           sync.Mutex
	stopping     bool
	scanning     bool
	activeBuilds int
	runCancel    context.CancelFunc
	stop         chan struct{}
	wg           sync.WaitGroup
}

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
		now: dependencies.Now, after: dependencies.After, stop: make(chan struct{}),
	}, nil
}

// Run starts the first scan asynchronously so HTTP readiness is never coupled
// to Provider enumeration, then re-reads the dynamic interval before each pass.
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
	defer cancel()
	worker.startScan(runCtx)
	for {
		config, err := worker.foundation.CatalogConfig()
		if err != nil {
			logger.Module("backupasset.catalog").Error().Str("stage", "schedule").Msg("Catalog 调度配置不可用")
			return
		}
		select {
		case <-runCtx.Done():
			return
		case <-worker.stop:
			return
		case <-worker.after(config.ReconcileInterval):
			worker.startScan(runCtx)
		}
	}
}

func (worker *CatalogWorker) startScan(ctx context.Context) bool {
	worker.mu.Lock()
	if worker.stopping || worker.scanning {
		worker.mu.Unlock()
		worker.metrics.ObserveScan(catalog.MetricScanSkipped)
		return false
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
	worker.mu.Lock()
	worker.activeBuilds++
	active := worker.activeBuilds
	worker.mu.Unlock()
	worker.metrics.SetActiveBuilds(active)
	defer func() {
		worker.mu.Lock()
		worker.activeBuilds--
		active := worker.activeBuilds
		worker.mu.Unlock()
		worker.metrics.SetActiveBuilds(active)
	}()
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
	Shutdown(context.Context) error
} = (*CatalogWorker)(nil)
