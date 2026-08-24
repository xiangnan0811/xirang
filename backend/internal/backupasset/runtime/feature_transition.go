package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// featureTransitionCeiling leaves five seconds below the server's 30-second
// write timeout for response handling while bounding every runtime transition.
const featureTransitionCeiling = 25 * time.Second

// featureTransitionCleanupReserve bounds synchronous compensation after the
// operation context is canceled while keeping operation + cleanup below 30s.
const featureTransitionCleanupReserve = 4 * time.Second

var ErrFeatureTransitionCompensation = errors.New("backup asset feature transition compensation failed")

type featureTransitionBudgetContextKey struct{}

type featureTransitionBudget struct {
	totalDeadline time.Time
	cleanupOnce   sync.Once
	cleanupAt     time.Time
}

func newFeatureTransitionContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	startedAt := time.Now()
	operationDeadline := startedAt.Add(featureTransitionCeiling)
	if callerDeadline, ok := parent.Deadline(); ok && callerDeadline.Before(operationDeadline) {
		operationDeadline = callerDeadline
	}
	operation, cancel := context.WithDeadline(parent, operationDeadline)
	totalDeadline := operationDeadline.Add(featureTransitionCleanupReserve)
	hardDeadline := startedAt.Add(featureTransitionCeiling + featureTransitionCleanupReserve)
	if hardDeadline.Before(totalDeadline) {
		totalDeadline = hardDeadline
	}
	return context.WithValue(operation, featureTransitionBudgetContextKey{}, &featureTransitionBudget{
		totalDeadline: totalDeadline,
	}), cancel
}

func newFeatureTransitionCleanupContext(operation context.Context) (context.Context, context.CancelFunc) {
	operation = withFeatureTransitionCleanupBudget(operation)
	budget, _ := operation.Value(featureTransitionBudgetContextKey{}).(*featureTransitionBudget)
	budget.cleanupOnce.Do(func() {
		budget.cleanupAt = time.Now().Add(featureTransitionCleanupReserve)
		if budget.totalDeadline.Before(budget.cleanupAt) {
			budget.cleanupAt = budget.totalDeadline
		}
	})
	return context.WithDeadline(context.WithoutCancel(operation), budget.cleanupAt)
}

func withFeatureTransitionCleanupBudget(operation context.Context) context.Context {
	if operation == nil {
		operation = context.Background()
	}
	if budget, _ := operation.Value(featureTransitionBudgetContextKey{}).(*featureTransitionBudget); budget != nil {
		return operation
	}
	now := time.Now()
	totalDeadline := now.Add(featureTransitionCeiling + featureTransitionCleanupReserve)
	if operationDeadline, ok := operation.Deadline(); ok {
		callerTotalDeadline := operationDeadline.Add(featureTransitionCleanupReserve)
		if callerTotalDeadline.Before(totalDeadline) {
			totalDeadline = callerTotalDeadline
		}
	}
	return context.WithValue(operation, featureTransitionBudgetContextKey{}, &featureTransitionBudget{
		totalDeadline: totalDeadline,
	})
}

func (runtime *Runtime) joinFeatureTransitionFailure(primary error, compensation ...error) error {
	compensationErr := errors.Join(compensation...)
	if compensationErr == nil {
		return primary
	}
	if runtime != nil {
		runtime.featureTransitionFenced.Store(true)
	}
	return errors.Join(primary, fmt.Errorf("%w: %w", ErrFeatureTransitionCompensation, compensationErr))
}

func (runtime *Runtime) featureTransitionReady() bool {
	return runtime != nil && !runtime.featureTransitionFenced.Load()
}
