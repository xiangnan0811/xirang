package task

import (
	"fmt"
	"math/rand/v2"
	"time"
)

type TaskStatus string

const (
	StatusPending  TaskStatus = "pending"
	StatusRunning  TaskStatus = "running"
	StatusSuccess  TaskStatus = "success"
	StatusFailed   TaskStatus = "failed"
	StatusRetrying TaskStatus = "retrying"
	StatusCanceled TaskStatus = "canceled"
	StatusWarning  TaskStatus = "warning"
	StatusSkipped  TaskStatus = "skipped"
)

type StateMachine struct {
	maxRetries int
	backoff    []time.Duration
}

func NewStateMachine() *StateMachine {
	return &StateMachine{
		maxRetries: 2,
		backoff: []time.Duration{
			30 * time.Second,
			90 * time.Second,
		},
	}
}

func ParseStatus(raw string) TaskStatus {
	switch TaskStatus(raw) {
	case StatusPending, StatusRunning, StatusSuccess, StatusFailed, StatusRetrying, StatusCanceled, StatusWarning, StatusSkipped:
		return TaskStatus(raw)
	default:
		return StatusPending
	}
}

func (sm *StateMachine) ValidateTransition(from, to TaskStatus) error {
	if from == to {
		return nil
	}
	allowed := map[TaskStatus]map[TaskStatus]bool{
		StatusPending: {
			StatusRunning:  true,
			StatusCanceled: true,
			StatusSkipped:  true,
		},
		StatusRunning: {
			StatusSuccess:  true,
			StatusFailed:   true,
			StatusRetrying: true,
			StatusCanceled: true,
			StatusWarning:  true,
		},
		StatusRetrying: {
			StatusRunning:  true,
			StatusFailed:   true,
			StatusCanceled: true,
		},
		StatusSuccess: {
			StatusPending: true,
		},
		StatusFailed: {
			StatusPending: true,
		},
		StatusCanceled: {
			StatusPending: true,
		},
		StatusWarning: {
			StatusPending:  true,
			StatusRunning:  true,
			StatusCanceled: true,
		},
		StatusSkipped: {
			StatusPending: true,
		},
	}
	if toAllowed, ok := allowed[from]; ok {
		if toAllowed[to] {
			return nil
		}
	}
	return fmt.Errorf("非法状态迁移: %s -> %s", from, to)
}

func (sm *StateMachine) NextAfterFailure(currentStatus TaskStatus, retryCount int, now time.Time) (TaskStatus, int, time.Time, bool) {
	if currentStatus != StatusRunning && currentStatus != StatusRetrying {
		return StatusFailed, retryCount, time.Time{}, false
	}
	if retryCount >= sm.maxRetries {
		if retryCount > sm.maxRetries {
			retryCount = sm.maxRetries
		}
		return StatusFailed, retryCount, time.Time{}, false
	}
	delay := sm.backoff[retryCount]
	return StatusRetrying, retryCount + 1, now.Add(delay), true
}

// NextAfterFailureConfigurable 使用可配置的重试参数（指数退避 + jitter）。
func (sm *StateMachine) NextAfterFailureConfigurable(
	currentStatus TaskStatus, retryCount int, now time.Time,
	maxRetries int, baseSeconds int,
) (TaskStatus, int, time.Time, bool) {
	if currentStatus != StatusRunning && currentStatus != StatusRetrying {
		return StatusFailed, retryCount, time.Time{}, false
	}
	if retryCount >= maxRetries {
		return StatusFailed, retryCount, time.Time{}, false
	}
	base := time.Duration(baseSeconds) * time.Second
	delay := base * time.Duration(1<<uint(retryCount))
	maxDelay := 30 * time.Minute
	if delay > maxDelay {
		delay = maxDelay
	}
	jitter := time.Duration(rand.Int64N(int64(base / 4)))
	return StatusRetrying, retryCount + 1, now.Add(delay + jitter), true
}
