package task

import (
	"testing"
	"time"
)

func TestStateMachineTransitionValidation(t *testing.T) {
	sm := NewStateMachine()

	if err := sm.ValidateTransition(StatusPending, StatusRunning); err != nil {
		t.Fatalf("pending -> running 应该合法, got err: %v", err)
	}

	if err := sm.ValidateTransition(StatusSuccess, StatusRunning); err == nil {
		t.Fatalf("success -> running 应该非法")
	}
}

func TestStateMachineFailureBackoff(t *testing.T) {
	sm := NewStateMachine()
	now := time.Date(2026, 2, 13, 10, 0, 0, 0, time.UTC)

	nextStatus, retryCount, nextRun, shouldRetry := sm.NextAfterFailure(StatusRunning, 0, now)
	if !shouldRetry {
		t.Fatalf("第1次失败后应该重试")
	}
	if nextStatus != StatusRetrying {
		t.Fatalf("第1次失败状态应为 retrying，got %s", nextStatus)
	}
	if retryCount != 1 {
		t.Fatalf("第1次失败后 retryCount 应为 1，got %d", retryCount)
	}
	if !nextRun.Equal(now.Add(30 * time.Second)) {
		t.Fatalf("第1次失败退避应为30s，got %s", nextRun)
	}

	nextStatus, retryCount, nextRun, shouldRetry = sm.NextAfterFailure(StatusRunning, 1, now)
	if !shouldRetry {
		t.Fatalf("第2次失败后应该重试")
	}
	if nextStatus != StatusRetrying {
		t.Fatalf("第2次失败状态应为 retrying，got %s", nextStatus)
	}
	if retryCount != 2 {
		t.Fatalf("第2次失败后 retryCount 应为 2，got %d", retryCount)
	}
	if !nextRun.Equal(now.Add(90 * time.Second)) {
		t.Fatalf("第2次失败退避应为90s，got %s", nextRun)
	}

	nextStatus, retryCount, nextRun, shouldRetry = sm.NextAfterFailure(StatusRunning, 2, now)
	if shouldRetry {
		t.Fatalf("第3次失败后不应该继续重试")
	}
	if nextStatus != StatusFailed {
		t.Fatalf("超过最大重试后应为 failed，got %s", nextStatus)
	}
	if retryCount != 2 {
		t.Fatalf("超过最大重试后 retryCount 应保持 2，got %d", retryCount)
	}
	if !nextRun.IsZero() {
		t.Fatalf("超过最大重试后 nextRun 应为空，got %s", nextRun)
	}
}
