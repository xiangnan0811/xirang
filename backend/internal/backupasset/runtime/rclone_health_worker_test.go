package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
)

type rcloneHealthServiceFake struct {
	mu         sync.Mutex
	candidates []string
	checked    []string
	result     provider.RcloneNativeHealthResult
	err        error
	started    chan struct{}
	canceled   chan struct{}
	release    chan struct{}
}

func (fake *rcloneHealthServiceFake) ListRcloneNativeHealthCandidates(context.Context, int) ([]string, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]string(nil), fake.candidates...), nil
}

func (fake *rcloneHealthServiceFake) CheckRcloneNativeHealth(ctx context.Context, repositoryID string) (provider.RcloneNativeHealthResult, error) {
	fake.mu.Lock()
	fake.checked = append(fake.checked, repositoryID)
	started, canceled, release := fake.started, fake.canceled, fake.release
	fake.mu.Unlock()
	if started != nil {
		close(started)
		<-ctx.Done()
		if canceled != nil {
			close(canceled)
		}
		if release != nil {
			<-release
		}
	}
	return fake.result, fake.err
}

func TestRcloneHealthWorkerStartupPassAcceptsPersistedRepositoryRisk(t *testing.T) {
	foundation := backupasset.NewFoundationService(runtimeFoundationSettings(true))
	service := &rcloneHealthServiceFake{
		candidates: []string{strings.Repeat("a", 32)},
		result:     provider.RcloneNativeHealthResult{Reason: backupasset.RcloneReasonKMSKeyUnavailable},
		err:        errors.New("FAKE_PERSISTED_NATIVE_RISK_FOR_TEST_ONLY"),
	}
	worker, err := NewRcloneHealthWorker(RcloneHealthWorkerDependencies{Foundation: foundation, Health: service})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.StartupPass(context.Background()); err != nil {
		t.Fatalf("persisted native repository risk blocked global startup: %v", err)
	}
	if len(service.checked) != 1 || service.checked[0] != service.candidates[0] {
		t.Fatalf("native health checks=%v", service.checked)
	}
}

func TestRcloneHealthWorkerStartupPassFailsWhenRiskWasNotPersisted(t *testing.T) {
	foundation := backupasset.NewFoundationService(runtimeFoundationSettings(true))
	service := &rcloneHealthServiceFake{
		candidates: []string{strings.Repeat("b", 32)},
		err:        errors.New("FAKE_NATIVE_HEALTH_PERSISTENCE_FAILURE_FOR_TEST_ONLY"),
	}
	worker, err := NewRcloneHealthWorker(RcloneHealthWorkerDependencies{Foundation: foundation, Health: service})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.StartupPass(context.Background()); err == nil {
		t.Fatal("unpersisted native health failure did not block startup")
	}
}

func TestRcloneHealthWorkerShutdownCancelsAndJoinsActiveCheck(t *testing.T) {
	foundation := backupasset.NewFoundationService(runtimeFoundationSettings(true))
	service := &rcloneHealthServiceFake{
		candidates: []string{strings.Repeat("c", 32)},
		started:    make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}),
	}
	worker, err := NewRcloneHealthWorker(RcloneHealthWorkerDependencies{Foundation: foundation, Health: service})
	if err != nil {
		t.Fatal(err)
	}
	donePass := make(chan error, 1)
	go func() { donePass <- worker.StartupPass(context.Background()) }()
	select {
	case <-service.started:
	case <-time.After(time.Second):
		t.Fatal("native health check did not start")
	}
	doneShutdown := make(chan error, 1)
	go func() { doneShutdown <- worker.Shutdown(context.Background()) }()
	select {
	case <-service.canceled:
	case <-time.After(time.Second):
		t.Fatal("native health shutdown did not cancel active check")
	}
	select {
	case err := <-doneShutdown:
		t.Fatalf("native health shutdown returned before active check joined: %v", err)
	default:
	}
	close(service.release)
	if err := <-doneShutdown; err != nil {
		t.Fatal(err)
	}
	if err := <-donePass; err == nil {
		t.Fatal("canceled startup health pass unexpectedly succeeded")
	}
}
