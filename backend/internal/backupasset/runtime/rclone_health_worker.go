package runtime

import (
	"context"
	"fmt"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
)

type RcloneNativeHealthService interface {
	ListRcloneNativeHealthCandidates(context.Context, int) ([]string, error)
	CheckRcloneNativeHealth(context.Context, string) (provider.RcloneNativeHealthResult, error)
}

type RcloneHealthWorkerDependencies struct {
	Foundation *backupasset.FoundationService
	Health     RcloneNativeHealthService
}

type RcloneHealthWorker struct {
	foundation *backupasset.FoundationService
	health     RcloneNativeHealthService

	mu       sync.Mutex
	stopping bool
	active   map[string]context.CancelFunc
	stop     chan struct{}
	wg       sync.WaitGroup
}

func NewRcloneHealthWorker(dependencies RcloneHealthWorkerDependencies) (*RcloneHealthWorker, error) {
	if dependencies.Foundation == nil || dependencies.Health == nil {
		return nil, fmt.Errorf("%w: native Rclone health worker dependencies unavailable", backupasset.ErrInvalidState)
	}
	return &RcloneHealthWorker{
		foundation: dependencies.Foundation,
		health:     dependencies.Health,
		active:     make(map[string]context.CancelFunc),
		stop:       make(chan struct{}),
	}, nil
}

func (worker *RcloneHealthWorker) StartupPass(ctx context.Context) error {
	if worker == nil {
		return fmt.Errorf("%w: native Rclone health worker unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if worker.isStopping() {
		return ErrAdmissionStopped
	}
	enabled, err := worker.foundation.FeatureEnabled()
	if err != nil || !enabled {
		return err
	}
	config, err := worker.foundation.PublicationConfig()
	if err != nil {
		return err
	}
	candidates, err := worker.health.ListRcloneNativeHealthCandidates(ctx, config.Rclone.HealthBatchSize)
	if err != nil {
		return err
	}
	if len(candidates) > config.Rclone.HealthBatchSize {
		candidates = candidates[:config.Rclone.HealthBatchSize]
	}
	for _, repositoryID := range candidates {
		if backupasset.ValidateOpaqueID(repositoryID) != nil {
			return fmt.Errorf("%w: invalid native Rclone health candidate", backupasset.ErrInvalidState)
		}
		workCtx, cancel, ok := worker.begin(ctx, repositoryID)
		if !ok {
			if worker.isStopping() {
				return ErrAdmissionStopped
			}
			continue
		}
		result, checkErr := worker.health.CheckRcloneNativeHealth(workCtx, repositoryID)
		workErr := workCtx.Err()
		worker.end(repositoryID, cancel)
		if workErr != nil {
			return workErr
		}
		// A safe reason means the repository service persisted the affected
		// repository's block/degradation fact. Only unpersisted failures stop
		// the global startup gate.
		if checkErr != nil && result.Reason == "" {
			return checkErr
		}
	}
	return nil
}

func (worker *RcloneHealthWorker) Run(ctx context.Context) {
	if worker == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if worker.isStopping() {
			return
		}
		config, err := worker.foundation.PublicationConfig()
		if err != nil {
			return
		}
		timer := time.NewTimer(config.Rclone.HealthInterval)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return
		case <-worker.stop:
			stopTimer(timer)
			return
		case <-timer.C:
			_ = worker.StartupPass(ctx)
		}
	}
}

func (worker *RcloneHealthWorker) Shutdown(ctx context.Context) error {
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

func (worker *RcloneHealthWorker) begin(parent context.Context, repositoryID string) (context.Context, context.CancelFunc, bool) {
	workCtx, cancel := context.WithCancel(parent)
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.stopping {
		cancel()
		return nil, nil, false
	}
	if _, exists := worker.active[repositoryID]; exists {
		cancel()
		return nil, nil, false
	}
	worker.active[repositoryID] = cancel
	worker.wg.Add(1)
	return workCtx, cancel, true
}

func (worker *RcloneHealthWorker) end(repositoryID string, cancel context.CancelFunc) {
	cancel()
	worker.mu.Lock()
	delete(worker.active, repositoryID)
	worker.mu.Unlock()
	worker.wg.Done()
}

func (worker *RcloneHealthWorker) isStopping() bool {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.stopping
}

func stopTimer(timer *time.Timer) {
	if timer != nil && !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

var _ interface {
	StartupPass(context.Context) error
	Run(context.Context)
	Shutdown(context.Context) error
} = (*RcloneHealthWorker)(nil)
