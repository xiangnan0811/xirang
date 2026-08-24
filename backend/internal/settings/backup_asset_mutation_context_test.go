package settings

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

type mutationWaitContext struct {
	context.Context
	waitObserved chan struct{}
	observed     atomic.Bool
}

func (ctx *mutationWaitContext) observeWait() {
	if ctx.observed.CompareAndSwap(false, true) {
		close(ctx.waitObserved)
	}
}

func (ctx *mutationWaitContext) Done() <-chan struct{} {
	ctx.observeWait()
	return ctx.Context.Done()
}

func (ctx *mutationWaitContext) Err() error {
	if ctx.observed.CompareAndSwap(false, true) {
		close(ctx.waitObserved)
		return nil
	}
	return ctx.Context.Err()
}

func TestWithBackupAssetMutationWaitingWriterHonorsContextCancellation(t *testing.T) {
	service := NewService(setupTestDB(t))

	ownerEntered := make(chan struct{})
	releaseOwner := make(chan struct{})
	var releaseOwnerOnce sync.Once
	release := func() { releaseOwnerOnce.Do(func() { close(releaseOwner) }) }
	t.Cleanup(release)
	ownerDone := make(chan error, 1)
	go func() {
		ownerDone <- service.WithBackupAssetMutation(context.Background(), func(map[string]string) error {
			close(ownerEntered)
			<-releaseOwner
			return nil
		})
	}()

	select {
	case <-ownerEntered:
	case <-time.After(time.Second):
		t.Fatal("settings mutation owner did not acquire the gate")
	}

	baseWaiterContext, cancelWaiter := context.WithCancel(context.Background())
	defer cancelWaiter()
	waiterContext := &mutationWaitContext{Context: baseWaiterContext, waitObserved: make(chan struct{})}
	var waiterCallbackCalled atomic.Bool
	waiterDone := make(chan error, 1)
	go func() {
		waiterDone <- service.WithBackupAssetMutation(waiterContext, func(map[string]string) error {
			waiterCallbackCalled.Store(true)
			return nil
		})
	}()
	select {
	case <-waiterContext.waitObserved:
	case <-time.After(time.Second):
		release()
		if err := <-ownerDone; err != nil {
			t.Fatalf("settings mutation owner failed: %v", err)
		}
		if err := <-waiterDone; err != nil {
			t.Fatalf("settings mutation waiter failed before cancellation: %v", err)
		}
		t.Fatal("waiting settings mutation did not observe its context")
	}
	cancelWaiter()

	var waiterErr error
	returnedBeforeOwnerRelease := false
	select {
	case waiterErr = <-waiterDone:
		returnedBeforeOwnerRelease = true
	case <-time.After(250 * time.Millisecond):
	}

	release()
	if err := <-ownerDone; err != nil {
		t.Fatalf("settings mutation owner failed: %v", err)
	}
	if !returnedBeforeOwnerRelease {
		waiterErr = <-waiterDone
	}

	if !errors.Is(waiterErr, context.Canceled) {
		t.Fatalf("waiting settings mutation error = %v, want context.Canceled", waiterErr)
	}
	if waiterCallbackCalled.Load() {
		t.Fatal("canceled settings mutation invoked its callback")
	}
	if err := service.WithBackupAssetMutation(context.Background(), func(map[string]string) error { return nil }); err != nil {
		t.Fatalf("settings mutation gate was not reusable after cancellation: %v", err)
	}
	if !returnedBeforeOwnerRelease {
		t.Fatal("canceled settings mutation remained blocked behind the current owner")
	}
}

func TestWithBackupAssetMutationFailureReleasesGateAndSnapshotSeesOnlyOldOrNewValues(t *testing.T) {
	service := NewService(setupTestDB(t))
	before, err := service.BackupAssetSettingsSnapshot()
	if err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}

	firstUpdated := make(chan struct{})
	releaseFailure := make(chan struct{})
	mutationFailure := errors.New("test mutation failure")
	failedMutationDone := make(chan error, 1)
	go func() {
		failedMutationDone <- service.WithBackupAssetMutation(context.Background(), func(map[string]string) error {
			return service.db.Transaction(func(tx *gorm.DB) error {
				if err := service.UpdateWithTx(tx, "backup_assets.search_candidate_limit", "20000"); err != nil {
					return err
				}
				close(firstUpdated)
				<-releaseFailure
				if err := service.UpdateWithTx(tx, "backup_assets.search_page_size_max", "300"); err != nil {
					return err
				}
				return mutationFailure
			})
		})
	}()

	select {
	case <-firstUpdated:
	case <-time.After(time.Second):
		t.Fatal("failed mutation did not reach its controlled boundary")
	}
	readerStarted := make(chan struct{})
	snapshotDone := make(chan map[string]string, 1)
	snapshotErr := make(chan error, 1)
	go func() {
		close(readerStarted)
		values, snapshotError := service.BackupAssetSettingsSnapshot()
		if snapshotError != nil {
			snapshotErr <- snapshotError
			return
		}
		snapshotDone <- values
	}()
	<-readerStarted
	select {
	case values := <-snapshotDone:
		t.Fatalf("snapshot escaped the exclusive mutation gate: %#v", values)
	case snapshotError := <-snapshotErr:
		t.Fatalf("snapshot failed while waiting for mutation: %v", snapshotError)
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseFailure)
	if err := <-failedMutationDone; !errors.Is(err, mutationFailure) {
		t.Fatalf("failed mutation error=%v, want test failure", err)
	}
	var afterFailure map[string]string
	select {
	case snapshotError := <-snapshotErr:
		t.Fatalf("snapshot after failed mutation: %v", snapshotError)
	case afterFailure = <-snapshotDone:
	case <-time.After(time.Second):
		t.Fatal("snapshot remained blocked after failed mutation released the gate")
	}
	if afterFailure["backup_assets.search_candidate_limit"] != before["backup_assets.search_candidate_limit"] ||
		afterFailure["backup_assets.search_page_size_max"] != before["backup_assets.search_page_size_max"] {
		t.Fatalf("snapshot observed rolled-back values after failure: %#v", afterFailure)
	}

	if err := service.WithBackupAssetMutation(context.Background(), func(map[string]string) error {
		return service.db.Transaction(func(tx *gorm.DB) error {
			if err := service.UpdateWithTx(tx, "backup_assets.search_candidate_limit", "20000"); err != nil {
				return err
			}
			return service.UpdateWithTx(tx, "backup_assets.search_page_size_max", "300")
		})
	}); err != nil {
		t.Fatalf("mutation gate was not reusable after failure: %v", err)
	}
	afterSuccess, err := service.BackupAssetSettingsSnapshot()
	if err != nil {
		t.Fatalf("snapshot after successful mutation: %v", err)
	}
	if afterSuccess["backup_assets.search_candidate_limit"] != "20000" ||
		afterSuccess["backup_assets.search_page_size_max"] != "300" {
		t.Fatalf("snapshot did not observe the complete committed mutation: %#v", afterSuccess)
	}
}
