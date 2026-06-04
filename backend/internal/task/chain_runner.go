package task

import (
	"context"
	"sync"
)

// ChainRunner tracks running task cancel functions to support cancellation
// and graceful shutdown.
type ChainRunner struct {
	runningCancels sync.Map // taskID → context.CancelFunc
}

// NewChainRunner creates a new ChainRunner.
func NewChainRunner() *ChainRunner {
	return &ChainRunner{}
}

// Store records a cancel function for the given task.
func (cr *ChainRunner) Store(taskID uint, cancel context.CancelFunc) {
	cr.runningCancels.Store(taskID, cancel)
}

// Load retrieves the cancel function for a task, if present.
func (cr *ChainRunner) Load(taskID uint) (context.CancelFunc, bool) {
	if raw, ok := cr.runningCancels.Load(taskID); ok {
		if cancelFn, castOK := raw.(context.CancelFunc); castOK {
			return cancelFn, true
		}
	}
	return nil, false
}

// Delete removes the cancel function for a task.
func (cr *ChainRunner) Delete(taskID uint) {
	cr.runningCancels.Delete(taskID)
}

// CancelAll cancels all tracked running tasks. Used during shutdown.
func (cr *ChainRunner) CancelAll() {
	cr.runningCancels.Range(func(_, value interface{}) bool {
		if cancelFn, ok := value.(context.CancelFunc); ok {
			cancelFn()
		}
		return true
	})
}
