package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/recovery"
	"xirang/backend/internal/logger"
)

func reconcilePermanentCleanupKeyLossBeforeReturn(
	keyErr error,
	dependencies Dependencies,
	foundation *backupasset.FoundationService,
) error {
	if !errors.Is(keyErr, backupasset.ErrKeyLost) && !errors.Is(keyErr, backupasset.ErrKeyUnavailable) {
		return keyErr
	}
	if foundation == nil || dependencies.DB == nil || dependencies.Now == nil {
		logger.Module("backupasset.recovery").Warn().
			Str("stage", "permanent_cleanup_key_reconciliation_dependencies").
			Msg("恢复清理密钥永久不可用，启动对账依赖缺失")
		return keyErr
	}
	config, err := foundation.RecoveryAuthorizationConfig()
	if err != nil {
		logger.Module("backupasset.recovery").Warn().
			Str("stage", "permanent_cleanup_key_reconciliation_config").
			Msg("恢复清理密钥永久不可用，启动对账配置不可用")
		return keyErr
	}
	if _, err := recovery.ReconcilePermanentCleanupKeyLoss(
		context.Background(), dependencies.DB, dependencies.Now().UTC(), config.ReceiptReaperBatchSize,
	); err != nil {
		logger.Module("backupasset.recovery").Warn().
			Str("stage", "permanent_cleanup_key_reconciliation").
			Msg("恢复清理密钥永久不可用，启动对账失败")
	}
	return keyErr
}

// RecoveryAuthorizationReceiptReaper is the narrow maintenance seam exposed
// by the Recovery authorization service. It intentionally carries no feature-
// admission dependency: retention must continue while admission is disabled.
type RecoveryAuthorizationReceiptReaper interface {
	ReapAuthorizationReceipts(context.Context, int) (int, error)
}

type RecoveryAuthorizationReceiptOwnerDependencies struct {
	Foundation *backupasset.FoundationService
	Reaper     RecoveryAuthorizationReceiptReaper
	After      func(time.Duration) <-chan time.Time
}

// RecoveryAuthorizationReceiptOwner owns one process-wide bounded receipt
// maintenance loop. Runtime composition starts it only after metadata
// reconciliation and joins it before schema drain.
type RecoveryAuthorizationReceiptOwner struct {
	foundation *backupasset.FoundationService
	reaper     RecoveryAuthorizationReceiptReaper
	after      func(time.Duration) <-chan time.Time
	config     backupasset.RecoveryAuthorizationConfig

	mu        sync.Mutex
	running   bool
	stopping  bool
	runCancel context.CancelFunc
	runDone   chan struct{}
}

func NewRecoveryAuthorizationReceiptOwner(
	dependencies RecoveryAuthorizationReceiptOwnerDependencies,
) (*RecoveryAuthorizationReceiptOwner, error) {
	if dependencies.Foundation == nil || dependencies.Reaper == nil {
		return nil, fmt.Errorf("%w: Recovery authorization receipt owner dependencies unavailable", backupasset.ErrInvalidState)
	}
	config, err := dependencies.Foundation.RecoveryAuthorizationConfig()
	if err != nil || !validRecoveryAuthorizationReceiptOwnerConfig(config) {
		return nil, fmt.Errorf("%w: Recovery authorization receipt owner config unavailable", backupasset.ErrInvalidState)
	}
	if dependencies.After == nil {
		dependencies.After = time.After
	}
	return &RecoveryAuthorizationReceiptOwner{
		foundation: dependencies.Foundation,
		reaper:     dependencies.Reaper,
		after:      dependencies.After,
		config:     config,
	}, nil
}

// Run performs one immediate bounded pass and then follows the dynamic
// maintenance cadence. A failed pass is non-terminal and is retried on the next
// tick. Concurrent Run calls never create a second owner loop.
func (owner *RecoveryAuthorizationReceiptOwner) Run(ctx context.Context) {
	if owner == nil {
		return
	}
	runCtx, cancel, done, ok := owner.beginRun(ctx)
	if !ok {
		return
	}
	defer owner.finishRun(cancel, done)

	config := owner.config
	owner.runPass(runCtx, config.ReceiptReaperBatchSize)
	for {
		select {
		case <-runCtx.Done():
			return
		case <-owner.after(config.ReceiptReaperCadence):
			next, err := owner.foundation.RecoveryAuthorizationConfig()
			if err == nil && validRecoveryAuthorizationReceiptOwnerConfig(next) {
				config = next
			} else {
				logger.Module("backupasset.recovery").Warn().
					Str("stage", "authorization_receipt_reaper_config").
					Msg("恢复授权回执清理配置不可用，保留上一有效快照")
			}
			owner.runPass(runCtx, config.ReceiptReaperBatchSize)
		}
	}
}

func (owner *RecoveryAuthorizationReceiptOwner) runPass(ctx context.Context, limit int) {
	_, err := owner.reaper.ReapAuthorizationReceipts(ctx, limit)
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		logger.Module("backupasset.recovery").Warn().
			Str("stage", "authorization_receipt_reaper").
			Msg("恢复授权回执清理失败")
	}
}

// Shutdown cancels the active bounded pass and joins the owner within the
// caller's deadline. It is idempotent and permanently prevents a later Run.
func (owner *RecoveryAuthorizationReceiptOwner) Shutdown(ctx context.Context) error {
	if owner == nil {
		return nil
	}
	ctx = nonNilRecoveryRuntimeContext(ctx)
	owner.mu.Lock()
	owner.stopping = true
	cancel, done := owner.runCancel, owner.runDone
	owner.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// PrepareSchemaDown enforces the required join-before-drain ordering. A failed
// join never invokes the schema callback.
func (owner *RecoveryAuthorizationReceiptOwner) PrepareSchemaDown(
	ctx context.Context,
	drain func() error,
) error {
	if drain == nil {
		return fmt.Errorf("%w: Recovery schema drain callback unavailable", backupasset.ErrInvalidState)
	}
	if err := owner.Shutdown(ctx); err != nil {
		return err
	}
	return drain()
}

func (owner *RecoveryAuthorizationReceiptOwner) beginRun(
	ctx context.Context,
) (context.Context, context.CancelFunc, chan struct{}, bool) {
	ctx = nonNilRecoveryRuntimeContext(ctx)
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.running || owner.stopping {
		return nil, nil, nil, false
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	owner.running = true
	owner.runCancel = cancel
	owner.runDone = done
	return runCtx, cancel, done, true
}

func (owner *RecoveryAuthorizationReceiptOwner) finishRun(cancel context.CancelFunc, done chan struct{}) {
	cancel()
	owner.mu.Lock()
	if owner.runDone == done {
		owner.running = false
		owner.runCancel = nil
	}
	owner.mu.Unlock()
	close(done)
}

func validRecoveryAuthorizationReceiptOwnerConfig(config backupasset.RecoveryAuthorizationConfig) bool {
	return config.ReceiptReaperCadence > 0 &&
		config.ReceiptReaperBatchSize > 0 && config.ReceiptReaperBatchSize <= 1000
}

func nonNilRecoveryRuntimeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

var _ interface {
	Run(context.Context)
	Shutdown(context.Context) error
	PrepareSchemaDown(context.Context, func() error) error
} = (*RecoveryAuthorizationReceiptOwner)(nil)
