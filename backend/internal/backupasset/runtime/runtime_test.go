package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type runtimeTransportFake struct{ marker int }

func (*runtimeTransportFake) Run(context.Context, provider.CommandInvocation, provider.OperationLimits) (provider.CommandOutput, error) {
	return provider.CommandOutput{}, nil
}
func (*runtimeTransportFake) Open(context.Context, provider.CommandInvocation, provider.OperationLimits, int64) (provider.ReadHandle, error) {
	return nil, fmt.Errorf("not used")
}
func (*runtimeTransportFake) OpenExecution(context.Context, provider.CommandInvocation, provider.OperationLimits, int64) (provider.CommandExecution, error) {
	return &runtimeExecutionFake{Reader: strings.NewReader("")}, nil
}

type runtimeExecutionFake struct{ io.Reader }

func (*runtimeExecutionFake) Join() (provider.CommandCompletion, error) {
	return provider.CommandCompletion{ExitCode: 0, ExitCodeKnown: true}, nil
}
func (*runtimeExecutionFake) Cancel() error { return nil }

var _ provider.CommandTransport = (*runtimeTransportFake)(nil)
var _ provider.CommandStreamTransport = (*runtimeTransportFake)(nil)

func openRuntimeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRuntimeExposesOneRepositoryPublicationLineageAndWorkerGraph(t *testing.T) {
	db := openRuntimeTestDB(t)
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settings.NewService(db), Transport: transport, StreamTransport: transport,
		Metrics: publication.NoopMetrics{}, Now: func() time.Time { return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("construct runtime: %v", err)
	}
	if runtime.FoundationService() == nil || runtime.RepositoryService() == nil || runtime.PublicationCoordinator() == nil ||
		runtime.PublicationReconciler() == nil || runtime.ResticPublicationStrategy() == nil ||
		runtime.RsyncTreePublicationStrategy() == nil ||
		runtime.LineageGuard() == nil || runtime.LegacyBlockRecorder() == nil || runtime.FeatureTransitioner() == nil {
		t.Fatal("runtime omitted a required shared graph port")
	}
	if runtime.ResticPublicationStrategy().Kind() != backupasset.ProviderRestic {
		t.Fatalf("publication strategy kind=%q, want %q", runtime.ResticPublicationStrategy().Kind(), backupasset.ProviderRestic)
	}
	if runtime.RsyncTreePublicationStrategy().Kind() != backupasset.ProviderRsync {
		t.Fatalf("publication strategy kind=%q, want %q", runtime.RsyncTreePublicationStrategy().Kind(), backupasset.ProviderRsync)
	}
}

func TestRuntimeRejectsMismatchedTransportFacets(t *testing.T) {
	db := openRuntimeTestDB(t)
	_, err := New(Dependencies{
		DB: db, Settings: settings.NewService(db), Transport: &runtimeTransportFake{marker: 1}, StreamTransport: &runtimeTransportFake{marker: 2}, Metrics: publication.NoopMetrics{},
	})
	if err == nil {
		t.Fatal("runtime accepted distinct transport facets")
	}
}

func TestRuntimeStartupManagedModeRequiresInterruptedRunReadiness(t *testing.T) {
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.RecoveryPoint{}, &model.RecoveryPointLease{}, &model.BackupAssetManagedHistoryLatch{}); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settingsService, Transport: transport, StreamTransport: transport, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.StartupPass(context.Background()); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("managed startup without TaskRun readiness error=%v, want invalid state", err)
	}
}

func TestRuntimeShutdownStopsAdmissionBeforeCancelingWorker(t *testing.T) {
	fixture := newAdmissionControllerFixture(t, true, nil)
	fixture.initialize(t)
	reconciler := &shutdownOrderReconciler{started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	worker, err := NewPublicationWorker(PublicationWorkerDependencies{
		Foundation: fixture.controller.foundation, Reconciler: reconciler, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	pointID := strings.Repeat("a", 32)
	go worker.process(context.Background(), pointID)
	select {
	case <-reconciler.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not begin shutdown-order fixture work")
	}
	runtime := &Runtime{admission: fixture.controller, worker: worker}
	done := make(chan error, 1)
	go func() { done <- runtime.Shutdown(context.Background()) }()
	select {
	case <-reconciler.canceled:
	case <-time.After(time.Second):
		t.Fatal("runtime shutdown did not cancel active worker work")
	}
	token, acquireErr := fixture.controller.Acquire(context.Background(), publication.OperationManifest)
	if token != nil {
		_ = token.Close()
	}
	if !errors.Is(acquireErr, ErrAdmissionStopped) {
		close(reconciler.release)
		<-done
		t.Fatalf("shutdown admitted a new publication token after worker cancellation: %v", acquireErr)
	}
	close(reconciler.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type shutdownOrderReconciler struct {
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
}

func (*shutdownOrderReconciler) ListCandidates(context.Context, int) ([]string, error) {
	return nil, nil
}
func (reconciler *shutdownOrderReconciler) ProcessPoint(ctx context.Context, pointID string) (publication.Outcome, error) {
	close(reconciler.started)
	<-ctx.Done()
	close(reconciler.canceled)
	<-reconciler.release
	return publication.Outcome{RecoveryPointID: pointID}, nil
}
func (*shutdownOrderReconciler) HasUnresolvedPublication(context.Context) (bool, error) {
	return false, nil
}
