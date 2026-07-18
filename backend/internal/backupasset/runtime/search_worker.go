package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/search"
)

type SearchWorkerConfig struct {
	Enabled            bool
	ReconcileInterval  time.Duration
	ReconcileBatchSize int
	WorkerConcurrency  int
	AbandonedAfter     time.Duration
}

type SearchWorkerBackend interface {
	ListCandidates(context.Context, int) ([]search.BuildCandidate, error)
	Build(context.Context, search.BuildRequest) error
	ReconcileAbandoned(context.Context, time.Time, int) (int64, error)
	ReconcileOverlays(context.Context, int) (int64, error)
}

type SearchWorkerDependencies struct {
	Config  func() (SearchWorkerConfig, error)
	Backend SearchWorkerBackend
	Metrics search.Metrics
	Now     func() time.Time
}

type SearchWorker struct {
	config  func() (SearchWorkerConfig, error)
	backend SearchWorkerBackend
	metrics search.Metrics
	now     func() time.Time

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewSearchWorker(dependencies SearchWorkerDependencies) (*SearchWorker, error) {
	if dependencies.Config == nil || dependencies.Backend == nil {
		return nil, fmt.Errorf("%w: invalid Search worker dependencies", backupasset.ErrInvalidState)
	}
	if dependencies.Metrics == nil {
		dependencies.Metrics = search.NoopMetrics{}
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &SearchWorker{config: dependencies.Config, backend: dependencies.Backend, metrics: dependencies.Metrics, now: dependencies.Now}, nil
}

func (worker *SearchWorker) StartupPass(ctx context.Context) error {
	if worker == nil {
		return fmt.Errorf("%w: Search worker unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return worker.runPass(ctx)
}

func (worker *SearchWorker) Run(ctx context.Context) {
	if worker == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	worker.mu.Lock()
	worker.cancel = cancel
	worker.done = done
	worker.mu.Unlock()
	defer func() {
		cancel()
		close(done)
		worker.mu.Lock()
		worker.cancel = nil
		worker.done = nil
		worker.mu.Unlock()
	}()

	for {
		_ = worker.runPass(runCtx)
		if runCtx.Err() != nil {
			return
		}
		config, err := worker.config()
		if err != nil || config.ReconcileInterval <= 0 {
			return
		}
		timer := time.NewTimer(config.ReconcileInterval)
		select {
		case <-runCtx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (worker *SearchWorker) Shutdown(ctx context.Context) error {
	if worker == nil {
		return nil
	}
	worker.mu.Lock()
	cancel, done := worker.cancel, worker.done
	worker.mu.Unlock()
	if cancel == nil || done == nil {
		return nil
	}
	cancel()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (worker *SearchWorker) runPass(ctx context.Context) error {
	config, err := worker.config()
	if err != nil {
		worker.metrics.ObserveScan(search.ScanOutcomeFailure)
		return err
	}
	if !config.Enabled {
		worker.metrics.ObserveScan(search.ScanOutcomeDisabled)
		return nil
	}
	if config.ReconcileInterval <= 0 || config.ReconcileBatchSize <= 0 || config.ReconcileBatchSize > 100000 ||
		config.WorkerConcurrency <= 0 || config.WorkerConcurrency > 256 {
		worker.metrics.ObserveScan(search.ScanOutcomeFailure)
		return fmt.Errorf("%w: invalid Search worker config", backupasset.ErrInvalidState)
	}
	abandonedAfter := config.AbandonedAfter
	if abandonedAfter <= 0 {
		abandonedAfter = 2 * config.ReconcileInterval
	}
	reconciled, err := worker.backend.ReconcileAbandoned(ctx, worker.now().UTC().Add(-abandonedAfter), config.ReconcileBatchSize)
	if err != nil {
		worker.metrics.ObserveScan(search.ScanOutcomeFailure)
		return err
	}
	worker.metrics.AddReconciledAbandoned(reconciled)
	overlays, err := worker.backend.ReconcileOverlays(ctx, config.ReconcileBatchSize)
	if err != nil {
		worker.metrics.ObserveScan(search.ScanOutcomeFailure)
		return err
	}
	worker.metrics.AddReconciledOverlays(overlays)
	candidates, err := worker.backend.ListCandidates(ctx, config.ReconcileBatchSize)
	if err != nil {
		worker.metrics.ObserveScan(search.ScanOutcomeFailure)
		return err
	}
	worker.metrics.ObserveScan(search.ScanOutcomeSuccess)
	return worker.buildCandidates(ctx, fairSearchCandidates(candidates), config.WorkerConcurrency)
}

func (worker *SearchWorker) buildCandidates(ctx context.Context, candidates []search.BuildCandidate, concurrency int) error {
	semaphore := make(chan struct{}, concurrency)
	errorsCh := make(chan error, len(candidates))
	var wait sync.WaitGroup
candidateLoop:
	for _, candidate := range candidates {
		select {
		case <-ctx.Done():
			break candidateLoop
		case semaphore <- struct{}{}:
		}
		if ctx.Err() != nil {
			<-semaphore
			break
		}
		candidate := candidate
		wait.Add(1)
		go func() {
			defer wait.Done()
			defer func() { <-semaphore }()
			worker.metrics.SetActiveBuilds(len(semaphore))
			err := worker.backend.Build(ctx, search.BuildRequest{
				RepositoryID: candidate.RepositoryID, RecoveryPointID: candidate.RecoveryPointID,
			})
			switch {
			case err == nil:
				worker.metrics.ObserveBuild(search.BuildOutcomeSuccess)
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				worker.metrics.ObserveBuild(search.BuildOutcomeCanceled)
			case errors.Is(err, backupasset.ErrLeaseFenceLost):
				worker.metrics.ObserveBuild(search.BuildOutcomeFenced)
			default:
				worker.metrics.ObserveBuild(search.BuildOutcomeFailure)
				errorsCh <- err
			}
		}()
	}
	wait.Wait()
	worker.metrics.SetActiveBuilds(0)
	close(errorsCh)
	for err := range errorsCh {
		return err
	}
	return ctx.Err()
}

func fairSearchCandidates(candidates []search.BuildCandidate) []search.BuildCandidate {
	byRepository := make(map[string][]search.BuildCandidate)
	order := make([]string, 0)
	for _, candidate := range candidates {
		if _, exists := byRepository[candidate.RepositoryID]; !exists {
			order = append(order, candidate.RepositoryID)
		}
		byRepository[candidate.RepositoryID] = append(byRepository[candidate.RepositoryID], candidate)
	}
	result := make([]search.BuildCandidate, 0, len(candidates))
	for len(result) < len(candidates) {
		for _, repositoryID := range order {
			queue := byRepository[repositoryID]
			if len(queue) == 0 {
				continue
			}
			result = append(result, queue[0])
			byRepository[repositoryID] = queue[1:]
		}
	}
	return result
}
