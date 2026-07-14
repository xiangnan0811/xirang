package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset/publication"
)

var allResticOperations = []publication.ResticOperation{
	publication.OperationLegacyBackup,
	publication.OperationLegacySnapshotList,
	publication.OperationLegacySnapshotFiles,
	publication.OperationLegacyIndex,
	publication.OperationLegacySearch,
	publication.OperationLegacyDiff,
	publication.OperationLegacySnapshotRestore,
	publication.OperationLegacyRestoreLatest,
	publication.OperationLegacyAnomaly,
	publication.OperationLegacyRetention,
	publication.OperationEvidenceBackup,
	publication.OperationManifest,
	publication.OperationReconcile,
}

func TestAdmissionTransitionDrainsEveryResticOperation(t *testing.T) {
	for _, operation := range allResticOperations {
		t.Run(string(operation), func(t *testing.T) {
			barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
			token, err := barrier.Acquire(context.Background(), operation)
			if err != nil {
				t.Fatal(err)
			}
			persisted := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- barrier.transition(context.Background(), publication.AdmissionManaged, func() error {
					close(persisted)
					return nil
				})
			}()
			select {
			case <-persisted:
				t.Fatal("transition persisted before admitted operation drained")
			case <-time.After(25 * time.Millisecond):
			}
			if err := token.Close(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAdmissionFailedDrainPreservesPriorModeAndGeneration(t *testing.T) {
	barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
	token, err := barrier.Acquire(context.Background(), publication.OperationLegacyBackup)
	if err != nil {
		t.Fatal(err)
	}
	beforeMode, beforeGeneration := barrier.current()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := barrier.transition(ctx, publication.AdmissionManaged, func() error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("transition error=%v, want deadline exceeded", err)
	}
	afterMode, afterGeneration := barrier.current()
	if afterMode != beforeMode || afterGeneration != beforeGeneration {
		t.Fatalf("failed drain changed state: before=%s/%d after=%s/%d", beforeMode, beforeGeneration, afterMode, afterGeneration)
	}
	if err := token.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAdmissionPersistFailureReopensPriorGeneration(t *testing.T) {
	barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
	beforeMode, beforeGeneration := barrier.current()
	persistErr := errors.New("persist failed")
	if err := barrier.transition(context.Background(), publication.AdmissionManaged, func() error { return persistErr }); !errors.Is(err, persistErr) {
		t.Fatalf("transition error=%v, want persist error", err)
	}
	afterMode, afterGeneration := barrier.current()
	if afterMode != beforeMode || afterGeneration != beforeGeneration {
		t.Fatalf("persist failure changed state: before=%s/%d after=%s/%d", beforeMode, beforeGeneration, afterMode, afterGeneration)
	}
	token, err := barrier.Acquire(context.Background(), publication.OperationLegacyBackup)
	if err != nil {
		t.Fatalf("prior generation did not reopen: %v", err)
	}
	defer func() { _ = token.Close() }()
}

func TestAdmissionTokenCloseIsIdempotentAndCannotUnderflow(t *testing.T) {
	barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
	token, err := barrier.Acquire(context.Background(), publication.OperationLegacyBackup)
	if err != nil {
		t.Fatal(err)
	}
	if err := token.Close(); err != nil {
		t.Fatal(err)
	}
	if err := token.Close(); err != nil {
		t.Fatal(err)
	}
	if active := barrier.activeCount(); active != 0 {
		t.Fatalf("active tokens=%d, want zero", active)
	}
}

func TestAdmissionTokenSnapshotsModeAndGenerationAcrossTransition(t *testing.T) {
	barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
	oldToken, err := barrier.Acquire(context.Background(), publication.OperationLegacyBackup)
	if err != nil {
		t.Fatal(err)
	}
	oldMode, oldGeneration := oldToken.Mode(), oldToken.Generation()
	done := make(chan error, 1)
	go func() {
		done <- barrier.transition(context.Background(), publication.AdmissionManaged, func() error { return nil })
	}()
	if err := oldToken.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if oldToken.Mode() != oldMode || oldToken.Generation() != oldGeneration {
		t.Fatalf("old token mutated across transition: mode=%s generation=%d", oldToken.Mode(), oldToken.Generation())
	}
	newToken, err := barrier.Acquire(context.Background(), publication.OperationEvidenceBackup)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = newToken.Close() }()
	if newToken.Mode() != publication.AdmissionManaged || newToken.Generation() != oldGeneration+1 {
		t.Fatalf("new token=%s/%d, want managed/%d", newToken.Mode(), newToken.Generation(), oldGeneration+1)
	}
}

func TestAdmissionStopRejectsNewTokensAndWaitsForCurrentTokens(t *testing.T) {
	barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
	token, err := barrier.Acquire(context.Background(), publication.OperationLegacyBackup)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- barrier.stop(context.Background()) }()
	deadline := time.Now().Add(time.Second)
	for !barrier.isStopping() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !barrier.isStopping() {
		t.Fatal("barrier did not enter stopping state")
	}
	if _, err := barrier.Acquire(context.Background(), publication.OperationLegacySnapshotList); err == nil {
		t.Fatal("stopping barrier admitted a new operation")
	}
	if err := token.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAdmissionStopAcceptingRejectsNewTokensWithoutWaitingForCurrentTokens(t *testing.T) {
	barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
	token, err := barrier.Acquire(context.Background(), publication.OperationLegacySnapshotList)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		barrier.stopAccepting()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop accepting waited for an active token")
	}
	if _, err := barrier.Acquire(context.Background(), publication.OperationLegacySnapshotList); !errors.Is(err, ErrAdmissionStopped) {
		t.Fatalf("new token after stop-accepting error=%v", err)
	}
	if err := token.Close(); err != nil {
		t.Fatal(err)
	}
	if err := barrier.stop(context.Background()); err != nil {
		t.Fatalf("full stop after active token closed: %v", err)
	}
}

func TestAdmissionDoesNotUpgradeAnOperationTokenIntoTransition(t *testing.T) {
	barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
	token, err := barrier.Acquire(context.Background(), publication.OperationLegacyBackup)
	if err != nil {
		t.Fatal(err)
	}
	if token.Operation() != publication.OperationLegacyBackup || token.Mode() != publication.AdmissionPristineLegacy {
		t.Fatalf("token unexpectedly carries transition authority: operation=%s mode=%s", token.Operation(), token.Mode())
	}
	if err := token.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAdmissionCanceledBeforeDrainReopensPriorGeneration(t *testing.T) {
	barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := barrier.transition(ctx, publication.AdmissionManaged, func() error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled transition error=%v, want context canceled", err)
	}
	acquireCtx, acquireCancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer acquireCancel()
	token, err := barrier.Acquire(acquireCtx, publication.OperationLegacyBackup)
	if err != nil {
		t.Fatalf("canceled transition left prior generation closed: %v", err)
	}
	defer func() { _ = token.Close() }()
	if token.Mode() != publication.AdmissionPristineLegacy {
		t.Fatalf("canceled transition changed token mode to %s", token.Mode())
	}
}

func newTestAdmission(t *testing.T, mode publication.AdmissionMode) *admissionBarrier {
	t.Helper()
	barrier, err := newAdmissionBarrier(mode)
	if err != nil {
		t.Fatal(err)
	}
	return barrier
}

func TestAdmissionConcurrentClosesDoNotLoseDrainWakeup(t *testing.T) {
	barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
	tokens := make([]publication.AdmissionToken, 0, len(allResticOperations))
	for _, operation := range allResticOperations {
		token, err := barrier.Acquire(context.Background(), operation)
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, token)
	}
	var persisted atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- barrier.transition(context.Background(), publication.AdmissionManaged, func() error { persisted.Store(true); return nil })
	}()
	for _, token := range tokens {
		go func(token publication.AdmissionToken) { _ = token.Close() }(token)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !persisted.Load() {
		t.Fatal("drained transition did not persist")
	}
}
