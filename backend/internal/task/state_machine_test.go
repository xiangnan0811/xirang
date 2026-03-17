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

func TestNextAfterFailureConfigurable(t *testing.T) {
	sm := NewStateMachine()
	now := time.Date(2026, 2, 13, 10, 0, 0, 0, time.UTC)

	type testCase struct {
		name            string
		currentStatus   TaskStatus
		retryCount      int
		maxRetries      int
		baseSeconds     int
		wantStatus      TaskStatus
		wantRetryCount  int
		wantShouldRetry bool
		// 当 wantShouldRetry=true 时，检查 nextRun 落在 [now+minDelay, now+maxDelay) 区间
		minDelay time.Duration
		maxDelay time.Duration
	}

	cases := []testCase{
		// 1. 正常指数退避: base=30s
		{
			name:            "base=30s retryCount=0 → delay in [30s, 37.5s)",
			currentStatus:   StatusRunning,
			retryCount:      0,
			maxRetries:      5,
			baseSeconds:     30,
			wantStatus:      StatusRetrying,
			wantRetryCount:  1,
			wantShouldRetry: true,
			minDelay:        30 * time.Second,
			maxDelay:        30*time.Second + 30*time.Second/4, // 30s + 7.5s jitter上限
		},
		{
			name:            "base=30s retryCount=1 → delay in [60s, 67.5s)",
			currentStatus:   StatusRunning,
			retryCount:      1,
			maxRetries:      5,
			baseSeconds:     30,
			wantStatus:      StatusRetrying,
			wantRetryCount:  2,
			wantShouldRetry: true,
			minDelay:        60 * time.Second,
			maxDelay:        60*time.Second + 30*time.Second/4,
		},
		{
			name:            "base=30s retryCount=2 → delay in [120s, 127.5s)",
			currentStatus:   StatusRetrying,
			retryCount:      2,
			maxRetries:      5,
			baseSeconds:     30,
			wantStatus:      StatusRetrying,
			wantRetryCount:  3,
			wantShouldRetry: true,
			minDelay:        120 * time.Second,
			maxDelay:        120*time.Second + 30*time.Second/4,
		},

		// 2. 超过最大重试次数
		{
			name:            "retryCount == maxRetries → StatusFailed",
			currentStatus:   StatusRunning,
			retryCount:      3,
			maxRetries:      3,
			baseSeconds:     30,
			wantStatus:      StatusFailed,
			wantRetryCount:  3,
			wantShouldRetry: false,
		},
		{
			name:            "retryCount > maxRetries → StatusFailed",
			currentStatus:   StatusRunning,
			retryCount:      5,
			maxRetries:      3,
			baseSeconds:     30,
			wantStatus:      StatusFailed,
			wantRetryCount:  5,
			wantShouldRetry: false,
		},

		// 3. 非法状态
		{
			name:            "StatusPending → StatusFailed",
			currentStatus:   StatusPending,
			retryCount:      0,
			maxRetries:      5,
			baseSeconds:     30,
			wantStatus:      StatusFailed,
			wantRetryCount:  0,
			wantShouldRetry: false,
		},
		{
			name:            "StatusSuccess → StatusFailed",
			currentStatus:   StatusSuccess,
			retryCount:      0,
			maxRetries:      5,
			baseSeconds:     30,
			wantStatus:      StatusFailed,
			wantRetryCount:  0,
			wantShouldRetry: false,
		},

		// 4. maxDelay 上限: base=60s, retryCount=10 → 60*1024=61440s 远超30min，被截断为30min
		{
			name:            "delay 超过30min被截断, base=60s retryCount=10",
			currentStatus:   StatusRunning,
			retryCount:      10,
			maxRetries:      20,
			baseSeconds:     60,
			wantStatus:      StatusRetrying,
			wantRetryCount:  11,
			wantShouldRetry: true,
			minDelay:        30 * time.Minute,
			maxDelay:        30*time.Minute + 60*time.Second/4, // 30min + 15s jitter上限
		},

		// 5. maxRetries=0 → 永远直接失败
		{
			name:            "maxRetries=0 retryCount=0 → 直接失败",
			currentStatus:   StatusRunning,
			retryCount:      0,
			maxRetries:      0,
			baseSeconds:     30,
			wantStatus:      StatusFailed,
			wantRetryCount:  0,
			wantShouldRetry: false,
		},

		// 6. 大 baseSeconds: base=600s, retryCount=0 → delay in [600s, 750s)
		{
			name:            "large base=600s retryCount=0 → delay in [600s, 750s)",
			currentStatus:   StatusRunning,
			retryCount:      0,
			maxRetries:      3,
			baseSeconds:     600,
			wantStatus:      StatusRetrying,
			wantRetryCount:  1,
			wantShouldRetry: true,
			minDelay:        600 * time.Second,
			maxDelay:        600*time.Second + 600*time.Second/4, // 600s + 150s jitter上限
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotStatus, gotRetry, gotNextRun, gotShouldRetry := sm.NextAfterFailureConfigurable(
				tc.currentStatus, tc.retryCount, now,
				tc.maxRetries, tc.baseSeconds,
			)

			if gotStatus != tc.wantStatus {
				t.Fatalf("status: want %s, got %s", tc.wantStatus, gotStatus)
			}
			if gotRetry != tc.wantRetryCount {
				t.Fatalf("retryCount: want %d, got %d", tc.wantRetryCount, gotRetry)
			}
			if gotShouldRetry != tc.wantShouldRetry {
				t.Fatalf("shouldRetry: want %v, got %v", tc.wantShouldRetry, gotShouldRetry)
			}

			if !tc.wantShouldRetry {
				if !gotNextRun.IsZero() {
					t.Fatalf("shouldRetry=false 时 nextRun 应为零值, got %s", gotNextRun)
				}
				return
			}

			// shouldRetry=true: 验证 nextRun 落在 [now+minDelay, now+maxDelay) 区间
			earliest := now.Add(tc.minDelay)
			latest := now.Add(tc.maxDelay)
			if gotNextRun.Before(earliest) || !gotNextRun.Before(latest) {
				t.Fatalf("nextRun 应在 [%s, %s) 区间内, got %s (offset %s)",
					earliest, latest, gotNextRun, gotNextRun.Sub(now))
			}
		})
	}
}
