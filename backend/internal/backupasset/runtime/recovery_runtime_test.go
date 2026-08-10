package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

func TestRecoveryAuthorizationReceiptOwnerRunsWhileAdmissionDisabled(t *testing.T) {
	reaper := &recoveryAuthorizationReceiptReaperFake{}
	ticks := make(chan time.Time, 1)
	foundation := recoveryAuthorizationReceiptOwnerFoundation(false)
	if _, err := foundation.RecoveryAuthorizationConfig(); err != nil {
		t.Fatalf("load receipt owner fixture config: %v", err)
	}
	owner, err := NewRecoveryAuthorizationReceiptOwner(RecoveryAuthorizationReceiptOwnerDependencies{
		Foundation: foundation,
		Reaper:     reaper,
		After: func(time.Duration) <-chan time.Time {
			return ticks
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	runDone := make(chan struct{})
	go func() {
		owner.Run(context.Background())
		close(runDone)
	}()
	waitRecoveryAuthorizationReceiptCalls(t, reaper, 1)
	ticks <- time.Now()
	waitRecoveryAuthorizationReceiptCalls(t, reaper, 2)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := owner.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("receipt owner Run did not join after shutdown")
	}
	for _, limit := range reaper.limitsSnapshot() {
		if limit != 100 {
			t.Fatalf("receipt reaper limit=%d, want configured disabled-runtime batch 100", limit)
		}
	}
}

func TestRecoveryAuthorizationReceiptOwnerRetriesAfterPassFailure(t *testing.T) {
	reaper := &recoveryAuthorizationReceiptReaperFake{errs: []error{errors.New("injected database failure")}}
	ticks := make(chan time.Time, 1)
	owner, err := NewRecoveryAuthorizationReceiptOwner(RecoveryAuthorizationReceiptOwnerDependencies{
		Foundation: recoveryAuthorizationReceiptOwnerFoundation(false),
		Reaper:     reaper,
		After: func(time.Duration) <-chan time.Time {
			return ticks
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	runDone := make(chan struct{})
	go func() {
		owner.Run(context.Background())
		close(runDone)
	}()
	waitRecoveryAuthorizationReceiptCalls(t, reaper, 1)
	ticks <- time.Now()
	waitRecoveryAuthorizationReceiptCalls(t, reaper, 2)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := owner.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("receipt owner did not join after retry test")
	}
}

func TestRecoveryAuthorizationReceiptOwnerJoinsBeforeSchemaDrain(t *testing.T) {
	reaper := newBlockingRecoveryAuthorizationReceiptReaper()
	owner, err := NewRecoveryAuthorizationReceiptOwner(RecoveryAuthorizationReceiptOwnerDependencies{
		Foundation: recoveryAuthorizationReceiptOwnerFoundation(false),
		Reaper:     reaper,
	})
	if err != nil {
		t.Fatal(err)
	}

	runDone := make(chan struct{})
	go func() {
		owner.Run(context.Background())
		close(runDone)
	}()
	select {
	case <-reaper.started:
	case <-time.After(time.Second):
		t.Fatal("receipt owner did not start the initial bounded pass")
	}

	drainCalled := false
	drainCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = owner.PrepareSchemaDown(drainCtx, func() error {
		select {
		case <-reaper.finished:
			drainCalled = true
			return nil
		default:
			t.Fatal("schema drain ran before the active receipt pass joined")
			return nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !drainCalled {
		t.Fatal("schema drain callback was not called")
	}
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("receipt owner Run remained active after schema drain")
	}
}

type recoveryAuthorizationReceiptReaperFake struct {
	mu     sync.Mutex
	limits []int
	errs   []error
}

type recoveryAuthorizationReceiptOwnerSettings map[string]string

func recoveryAuthorizationReceiptOwnerFoundation(enabled bool) *backupasset.FoundationService {
	settings := recoveryAuthorizationReceiptOwnerSettings{}
	for key, value := range runtimeFoundationSettings(enabled) {
		settings[key] = value
	}
	return backupasset.NewFoundationService(settings)
}

func (settings recoveryAuthorizationReceiptOwnerSettings) GetEffective(key string) string {
	return settings[key]
}

func (settings recoveryAuthorizationReceiptOwnerSettings) BackupAssetSettingsSnapshot() (map[string]string, error) {
	result := make(map[string]string, len(settings))
	for key, value := range settings {
		result[key] = value
	}
	return result, nil
}

func (reaper *recoveryAuthorizationReceiptReaperFake) ReapAuthorizationReceipts(
	_ context.Context,
	limit int,
) (int, error) {
	reaper.mu.Lock()
	defer reaper.mu.Unlock()
	reaper.limits = append(reaper.limits, limit)
	if len(reaper.errs) == 0 {
		return 0, nil
	}
	err := reaper.errs[0]
	reaper.errs = reaper.errs[1:]
	return 0, err
}

func (reaper *recoveryAuthorizationReceiptReaperFake) limitsSnapshot() []int {
	reaper.mu.Lock()
	defer reaper.mu.Unlock()
	return append([]int(nil), reaper.limits...)
}

func waitRecoveryAuthorizationReceiptCalls(
	t *testing.T,
	reaper *recoveryAuthorizationReceiptReaperFake,
	want int,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(reaper.limitsSnapshot()) >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("receipt reaper calls=%d, want at least %d", len(reaper.limitsSnapshot()), want)
}

type blockingRecoveryAuthorizationReceiptReaper struct {
	started  chan struct{}
	finished chan struct{}
	once     sync.Once
}

func newBlockingRecoveryAuthorizationReceiptReaper() *blockingRecoveryAuthorizationReceiptReaper {
	return &blockingRecoveryAuthorizationReceiptReaper{
		started:  make(chan struct{}),
		finished: make(chan struct{}),
	}
}

func (reaper *blockingRecoveryAuthorizationReceiptReaper) ReapAuthorizationReceipts(
	ctx context.Context,
	_ int,
) (int, error) {
	reaper.once.Do(func() { close(reaper.started) })
	<-ctx.Done()
	close(reaper.finished)
	return 0, ctx.Err()
}
