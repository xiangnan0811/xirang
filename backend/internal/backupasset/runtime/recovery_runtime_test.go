package runtime

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/backupasset/recovery"
	"xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type managedRecoveryReceiptOwnerFake struct {
	mu      *sync.Mutex
	events  *[]string
	started chan struct{}
	start   sync.Once
	stop    sync.Once
	done    chan struct{}
}

func newManagedRecoveryReceiptOwnerFake(mu *sync.Mutex, events *[]string) *managedRecoveryReceiptOwnerFake {
	return &managedRecoveryReceiptOwnerFake{
		mu: mu, events: events, started: make(chan struct{}), done: make(chan struct{}),
	}
}

func (owner *managedRecoveryReceiptOwnerFake) Run(ctx context.Context) {
	owner.mu.Lock()
	*owner.events = append(*owner.events, "receipt_run")
	owner.mu.Unlock()
	owner.start.Do(func() { close(owner.started) })
	select {
	case <-ctx.Done():
	case <-owner.done:
	}
}

func (owner *managedRecoveryReceiptOwnerFake) Shutdown(context.Context) error {
	owner.mu.Lock()
	*owner.events = append(*owner.events, "receipt_shutdown")
	owner.mu.Unlock()
	owner.stop.Do(func() { close(owner.done) })
	return nil
}

func (owner *managedRecoveryReceiptOwnerFake) PrepareSchemaDown(ctx context.Context, callback func() error) error {
	if err := owner.Shutdown(ctx); err != nil {
		return err
	}
	return callback()
}

func containsManagedRecoveryOrderedEvents(events, ordered []string) bool {
	index := 0
	for _, event := range events {
		if index < len(ordered) && event == ordered[index] {
			index++
		}
	}
	return index == len(ordered)
}

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

func TestManagedRecoveryRuntimeStartupReconcilesBeforePublicationWithoutExecutingQueuedWork(t *testing.T) {
	publication := newManagedRecoveryPublication()
	var events []string
	graph := &managedRecoveryGraph{
		reconcileMetadata: func(context.Context) error {
			if publication.current() != nil {
				t.Fatal("Recovery graph was published before metadata reconciliation")
			}
			events = append(events, "reconcile")
			return nil
		},
		run: func(context.Context) {
			events = append(events, "run")
		},
	}
	manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
		Build: func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
			return graph, nil
		},
		Publication: publication,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := publication.current(); got != graph {
		t.Fatalf("published Recovery graph=%p, want %p", got, graph)
	}
	if len(events) != 1 || events[0] != "reconcile" {
		t.Fatalf("Startup events=%v, want metadata reconciliation only", events)
	}
}

func TestManagedRecoveryPublicationRejectsNilAndDuplicateGraphs(t *testing.T) {
	publication := newManagedRecoveryPublication()
	if err := publication.publish(nil); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("publish nil error=%v, want invalid state", err)
	}
	first := &managedRecoveryGraph{}
	if err := publication.publish(first); err != nil {
		t.Fatalf("publish first graph: %v", err)
	}
	if err := publication.publish(&managedRecoveryGraph{}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("publish duplicate error=%v, want conflict", err)
	}
	if got := publication.current(); got != first {
		t.Fatalf("duplicate publication replaced graph=%p, want original %p", got, first)
	}
}

func TestManagedRecoveryAuthorizationFacadeBorrowsPublishedAdmissionAndWakesExecutedJob(t *testing.T) {
	publication := newManagedRecoveryPublication()
	facade := &managedRecoveryAuthorizationFacade{publication: publication}
	request := recovery.RecoveryAuthorizationRequest{Operation: recovery.AuthorizationReceiptExecute}
	if _, err := facade.Authorize(context.Background(), request); !errors.Is(err, recovery.ErrRecoveryPlanUnavailable) {
		t.Fatalf("unpublished authorization error=%v", err)
	}

	jobID := strings.Repeat("a", 32)
	backend := &managedRecoveryAuthorizationBackendFake{
		result: recovery.RecoveryAuthorizationResult{JobID: jobID},
	}
	worker, err := newManagedRecoveryWorker(managedRecoveryWorkerDependencies{
		Coordinator: &managedRecoveryWorkerCoordinatorFake{}, WorkerID: "recovery-facade-worker",
		WorkerConcurrency: 1, TakeoverCadence: time.Hour, RetryBase: time.Second, RetryMaxDelay: time.Minute,
		Policy: managedRecoveryWorkerPolicyForTest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	graph := &managedRecoveryGraph{admissionEnabled: true, authorization: backend, worker: worker}
	if err := publication.publish(graph); err != nil {
		t.Fatal(err)
	}
	result, err := facade.Authorize(context.Background(), request)
	if err != nil || result.JobID != jobID {
		t.Fatalf("authorize result=%+v err=%v", result, err)
	}
	select {
	case wake := <-worker.wake:
		if wake != jobID {
			t.Fatalf("worker wake=%q, want %q", wake, jobID)
		}
	default:
		t.Fatal("durable execute result did not wake the Recovery worker")
	}
	graph.admissionEnabled = false
	if _, _, err := facade.ReplayAuthorization(context.Background(), request); !errors.Is(err, recovery.ErrRecoveryPlanUnavailable) {
		t.Fatalf("disabled replay error=%v", err)
	}
}

func TestRecoveryProductionAuthorityDependencyLossClosesEffectsWithoutUnpublishingMaintenance(t *testing.T) {
	publication := newManagedRecoveryPublication()
	authorization := &managedRecoveryAuthorizationBackendFake{}
	results := &managedRecoveryResultBackendFake{}
	reconciliation := &managedRecoveryDowngradeReconcilerFake{result: recovery.RecoveryReconciliationResult{
		State: recovery.RecoveryReconciliationClear, Complete: true,
	}}
	graph := &managedRecoveryGraph{
		admissionEnabled: true, authorization: authorization,
		resultDelivery: results, downgradeReconciler: reconciliation,
	}
	if err := publication.publish(graph); err != nil {
		t.Fatal(err)
	}
	authorization.err = recovery.ErrRecoveryTargetUnavailable
	effects := []recovery.AuthorizationReceiptOperation{
		recovery.AuthorizationReceiptSecurityOverride,
		recovery.AuthorizationReceiptWriteAuthorize,
		recovery.AuthorizationReceiptDeleteAuthorize,
		recovery.AuthorizationReceiptExecute,
	}
	facade := &managedRecoveryAuthorizationFacade{publication: publication}
	for _, operation := range effects {
		if _, err := facade.Authorize(
			context.Background(), recovery.RecoveryAuthorizationRequest{Operation: operation},
		); !errors.Is(err, recovery.ErrRecoveryTargetUnavailable) {
			t.Fatalf("effect %q dependency-loss error=%v, want target unavailable", operation, err)
		}
	}
	if publication.current() != graph {
		t.Fatal("effect dependency loss unpublished the maintenance graph")
	}
	resultFacade := &managedRecoveryResultFacade{publication: publication}
	ref := content.RecoveryResultRef{
		RecoveryJobID: strings.Repeat("a", 32), ResultID: strings.Repeat("b", 32),
	}
	if _, err := resultFacade.AuthorizeRecoveryResult(
		context.Background(), content.DeliveryActor{}, ref, content.DeliveryDownload,
	); err != nil {
		t.Fatalf("result maintenance closed after authority loss: %v", err)
	}
	if _, err := reconciliation.ReconcileDowngradeReadiness(
		context.Background(), recovery.RecoveryDowngradeReconciliationRequest{
			AdmissionGeneration: "dependency-loss-generation",
		},
	); err != nil {
		t.Fatalf("logical reconciliation closed after authority loss: %v", err)
	}
}

func TestManagedRecoveryResultFacadeRemainsPublishedWhileAdmissionDisabled(t *testing.T) {
	publication := newManagedRecoveryPublication()
	resultBackend := &managedRecoveryResultBackendFake{}
	if err := publication.publish(&managedRecoveryGraph{
		admissionEnabled: false, resultDelivery: resultBackend,
	}); err != nil {
		t.Fatal(err)
	}
	facade := &managedRecoveryResultFacade{publication: publication}
	ref := content.RecoveryResultRef{RecoveryJobID: strings.Repeat("a", 32), ResultID: strings.Repeat("b", 32)}
	if _, err := facade.AuthorizeRecoveryResult(
		context.Background(), content.DeliveryActor{}, ref, content.DeliveryDownload,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := facade.OpenRecoveryResultSource(context.Background(), content.RecoveryResultSourceRequest{Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if resultBackend.authorizations.Load() != 1 || resultBackend.opens.Load() != 1 {
		t.Fatalf("result facade calls authorize=%d open=%d", resultBackend.authorizations.Load(), resultBackend.opens.Load())
	}
	publication.unpublish()
	if _, err := facade.AuthorizeRecoveryResult(
		context.Background(), content.DeliveryActor{}, ref, content.DeliveryDownload,
	); !errors.Is(err, recovery.ErrRecoveryResultUnavailable) {
		t.Fatalf("unpublished result authorization error=%v", err)
	}
}

func TestManagedRecoveryResultIssueScopeBlocksGraphDrainUntilTicketFinishes(t *testing.T) {
	publication := newManagedRecoveryPublication()
	if err := publication.publish(&managedRecoveryGraph{
		resultDelivery: &managedRecoveryResultBackendFake{},
	}); err != nil {
		t.Fatal(err)
	}
	facade := &managedRecoveryResultFacade{publication: publication}
	ref := content.RecoveryResultRef{RecoveryJobID: strings.Repeat("a", 32), ResultID: strings.Repeat("b", 32)}
	_, release, err := facade.AuthorizeRecoveryResultIssue(
		context.Background(), content.DeliveryActor{}, ref, content.DeliveryDownload,
	)
	if err != nil {
		t.Fatal(err)
	}
	publication.unpublish()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelWait()
	if err := publication.waitIdle(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		release()
		t.Fatalf("Recovery graph drain error=%v, want active ticket issue to hold publication", err)
	}
	release()
	if err := publication.waitIdle(context.Background()); err != nil {
		t.Fatalf("Recovery graph remained busy after ticket issue release: %v", err)
	}
}

func TestManagedRecoveryRuntimeDisableRetainsResultAndReconciliationFacades(t *testing.T) {
	resultBackend := &managedRecoveryResultBackendFake{}
	reconciler := &managedRecoveryDowngradeReconcilerFake{
		result: recovery.RecoveryReconciliationResult{State: recovery.RecoveryReconciliationClear, Complete: true},
	}
	builds := 0
	events := []string{}
	manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
		Build: func(_ context.Context, config backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
			builds++
			if config.Enabled {
				return &managedRecoveryGraph{
					admissionEnabled: true, reconcileMetadata: func(context.Context) error { return nil },
					resultDelivery: resultBackend, downgradeReconciler: reconciler,
					stopClaims: func() { events = append(events, "stop_claims") },
					cancelJoinAttempts: func(context.Context) error {
						events = append(events, "join_attempts")
						return nil
					},
					fenceOwnership: func(context.Context) error {
						events = append(events, "fence_ownership")
						return nil
					},
					revokeDrainDelivery: func(context.Context) error {
						events = append(events, "drain_delivery")
						return nil
					},
					shutdownLifecycle: func(context.Context) error {
						events = append(events, "shutdown_lifecycle")
						return nil
					},
				}, nil
			}
			return &managedRecoveryGraph{
				admissionEnabled: false, reconcileMetadata: func(context.Context) error { return nil },
				resultDelivery: resultBackend, downgradeReconciler: reconciler,
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.StartupWithConfig(context.Background(), backupasset.RecoveryConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := manager.TransitionSettings(
		context.Background(), backupasset.RecoveryConfig{Enabled: false}, func() error {
			events = append(events, "persist")
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	current := manager.publication.current()
	if current == nil || current.admissionEnabled || current.resultDelivery != resultBackend ||
		current.downgradeReconciler != reconciler {
		t.Fatalf("disabled graph lost maintenance services: %+v", current)
	}
	if builds != 2 {
		t.Fatalf("graph builds=%d, want enabled and disabled candidates", builds)
	}
	if got, want := fmt.Sprint(events), "[stop_claims join_attempts fence_ownership drain_delivery shutdown_lifecycle persist]"; got != want {
		t.Fatalf("disable transition events=%s, want %s", got, want)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryRuntimeRequiresBoundRsyncResolverAndTargetWriter(t *testing.T) {
	resolver := &repository.Service{}
	writer := &managedRecoveryRsyncTargetWriterFake{}
	runner := &managedRecoveryRsyncRestoreRunnerFake{}

	port, err := newManagedRecoveryRsyncRestorePort(managedRecoveryRsyncRestoreDependencies{
		Resolver: resolver,
		Writer:   writer,
		Runner:   runner,
	})
	if err != nil {
		t.Fatalf("construct bound Rsync restore port: %v", err)
	}
	if port == nil {
		t.Fatal("construct bound Rsync restore port returned nil")
	}
	if port.resolver != resolver {
		t.Fatalf("Rsync resolver=%T, want exact Repository service", port.resolver)
	}
	if port.writer != writer {
		t.Fatalf("Rsync target writer=%T, want exact Recovery-owned writer", port.writer)
	}

	for name, dependencies := range map[string]managedRecoveryRsyncRestoreDependencies{
		"resolver": {Writer: writer, Runner: runner},
		"writer":   {Resolver: resolver, Runner: runner},
		"runner":   {Resolver: resolver, Writer: writer},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newManagedRecoveryRsyncRestorePort(dependencies); !errors.Is(err, backupasset.ErrInvalidState) {
				t.Fatalf("unbound %s error=%v, want invalid state", name, err)
			}
		})
	}
}

func TestManagedRecoveryDowngradeInspectorUsesRecoverySchedulerIdentifiers(t *testing.T) {
	if got, want := managedRecoveryClaimSchedulerRowID(), recovery.ClaimSchedulerRowID(); got != want {
		t.Fatalf("claim scheduler row ID=%q, want Recovery-owned %q", got, want)
	}
	if got, want := managedRecoveryTakeoverSchedulerRowID(), recovery.TakeoverSchedulerRowID(); got != want {
		t.Fatalf("takeover scheduler row ID=%q, want Recovery-owned %q", got, want)
	}
}

type managedRecoveryRestorePortExecutionFake struct {
	requests []provider.RestoreRequest
}

func (port *managedRecoveryRestorePortExecutionFake) Execute(
	_ context.Context,
	request provider.RestoreRequest,
	_ provider.RestoreProgress,
) (provider.RestoreResult, error) {
	port.requests = append(port.requests, request)
	return provider.RestoreResult{Checkpoint: request.Checkpoint}, nil
}

type managedRecoveryRestoreRequestBuilderFake struct {
	claim    recovery.RecoveryWorkerClaim
	request  provider.RestoreRequest
	buildErr error
}

func (builder *managedRecoveryRestoreRequestBuilderFake) BuildRsyncRestoreRequest(
	_ context.Context,
	claim recovery.RecoveryWorkerClaim,
) (provider.RestoreRequest, error) {
	builder.claim = claim
	return builder.request, builder.buildErr
}

func (*managedRecoveryRestoreRequestBuilderFake) ReleaseRsyncRestoreRequest(
	recovery.RecoveryWorkerClaim,
) {
}

func TestManagedRecoveryClaimExecutorUsesRepositoryRestorePort(t *testing.T) {
	claim := recovery.RecoveryWorkerClaim{JobID: strings.Repeat("1", 32)}
	request := provider.RestoreRequest{
		Version:  provider.RestoreRequestSchemaV1,
		Provider: backupasset.ProviderRsync,
		Rsync: &provider.RsyncRestoreRequest{SourceRef: provider.RsyncRestoreSourceRef{
			PlanID: strings.Repeat("2", 32),
		}},
	}
	builder := &managedRecoveryRestoreRequestBuilderFake{request: request}
	port := &managedRecoveryRestorePortExecutionFake{}
	executor := &managedRecoveryResolvedClaimExecutor{builder: builder, restorePort: port}

	if err := executor.ExecuteResolvedClaim(context.Background(), claim); err != nil {
		t.Fatalf("execute managed Recovery claim: %v", err)
	}
	if builder.claim.JobID != claim.JobID {
		t.Fatalf("request builder claim=%q, want %q", builder.claim.JobID, claim.JobID)
	}
	if len(port.requests) != 1 || port.requests[0].Rsync == nil ||
		port.requests[0].Rsync.SourceRef.PlanID != request.Rsync.SourceRef.PlanID {
		t.Fatalf("restore port requests=%+v, want one closed Rsync request", port.requests)
	}
}

type managedRecoveryDeclaredSourceFake struct {
	payload string
	stream  provider.RsyncRestoreSourceStream
}

func (source *managedRecoveryDeclaredSourceFake) OpenDeclaredRegular(
	_ context.Context,
	_ provider.RestoreEntry,
) (provider.RsyncRestoreSourceStream, error) {
	source.stream = io.NopCloser(strings.NewReader(source.payload))
	return source.stream, nil
}

func (*managedRecoveryDeclaredSourceFake) MaterializeDeclaredEntries(
	_ context.Context,
	entries []provider.RestoreEntry,
) ([]provider.RestoreEntry, error) {
	return append([]provider.RestoreEntry(nil), entries...), nil
}

func (*managedRecoveryDeclaredSourceFake) Revalidate(context.Context) error { return nil }
func (*managedRecoveryDeclaredSourceFake) Close() error                     { return nil }

type managedRecoveryDeclaredTargetWriterFake struct {
	calls   []provider.RsyncTargetWriteCall
	payload string
}

func (writer *managedRecoveryDeclaredTargetWriterFake) WriteDeclaredRegular(
	_ context.Context,
	call provider.RsyncTargetWriteCall,
) error {
	writer.calls = append(writer.calls, call)
	payload, err := io.ReadAll(call.Source)
	if err != nil {
		return err
	}
	writer.payload = string(payload)
	return nil
}

func TestManagedRecoveryClaimRestoreBridgeRoutesPinnedStreamThroughInjectedTargetWriter(t *testing.T) {
	claim := recovery.RecoveryWorkerClaim{
		JobID: strings.Repeat("1", 32), AttemptID: strings.Repeat("2", 32),
		NodeLeaseID: strings.Repeat("3", 32), AttemptFence: 4, NodeFence: 5,
	}
	entry := provider.RestoreEntry{
		AssetRef: backupasset.AssetRef{
			RecoveryPointID: strings.Repeat("4", 32), EntryID: strings.Repeat("5", 32),
		},
		Type: backupasset.CatalogEntryFile, ExpectedSize: 7,
		ExpectedDigest: strings.Repeat("6", 64), TargetObjectDigest: strings.Repeat("7", 64),
	}
	checkpoint := provider.RestoreCheckpoint{
		ID: strings.Repeat("8", 32), OperationDigest: strings.Repeat("9", 64),
		PriorTargetRevision: "target-revision", VerifiedTargetIdentityDigest: strings.Repeat("a", 64),
		VerifiedTargetRevision: "target-revision", AttemptFence: claim.AttemptFence, NodeFence: claim.NodeFence,
	}
	source := &managedRecoveryDeclaredSourceFake{payload: "payload"}
	writer := &managedRecoveryDeclaredTargetWriterFake{}
	bridge := &managedRecoveryClaimRestoreBridge{
		coordinator: &recovery.WorkerCoordinator{},
		claims:      map[string]recovery.RecoveryWorkerClaim{managedRecoveryClaimKey(claim): claim},
	}
	call := provider.RsyncRestoreExecuteCall{
		RsyncRestoreIntent: provider.RsyncRestoreIntent{
			Source: source, TargetWriter: writer,
			Target: provider.RsyncBoundRemoteTarget{
				NodeID: 1, RootID: "root", TargetBindingDigest: strings.Repeat("b", 64),
				TargetPathDigest: strings.Repeat("c", 64), RootRevision: "root-revision",
				TargetRevision: "target-revision",
			},
			Entries: []provider.RestoreEntry{entry}, Fence: provider.RestoreFence{
				JobID: claim.JobID, AttemptID: claim.AttemptID, NodeLeaseID: claim.NodeLeaseID,
				AttemptFence: claim.AttemptFence, NodeFence: claim.NodeFence,
				ExpectedTargetRevision: "target-revision",
			},
			Checkpoint: checkpoint,
		},
		Permit: provider.TargetMutationPermit{
			TargetBindingDigest: strings.Repeat("b", 64), UseLatchID: provider.RestoreSchemaUseLatchID,
			JobID: claim.JobID, AttemptID: claim.AttemptID, NodeLeaseID: claim.NodeLeaseID,
			AttemptFence: claim.AttemptFence, NodeFence: claim.NodeFence,
			ExpectedTargetRevision: "target-revision",
		},
	}

	result, err := bridge.Execute(context.Background(), call)
	if len(writer.calls) != 1 {
		t.Fatalf("Rsync target writer calls=%d, want one declared regular write; execute error=%v", len(writer.calls), err)
	}
	if writer.calls[0].Source != source.stream || writer.calls[0].Entry != entry || writer.payload != source.payload {
		t.Fatalf("Rsync target write=%+v payload=%q, want exact pinned entry stream", writer.calls[0], writer.payload)
	}
	if err != nil {
		t.Fatalf("execute managed Rsync bridge: %v", err)
	}
	if result.Checkpoint != checkpoint || len(result.Evidence) != 0 {
		t.Fatalf("runner result=%+v, want exact checkpoint and no synthetic evidence", result)
	}
}

func TestRecoveryHeartbeatProviderSessionFreezesAbsoluteDeadline(t *testing.T) {
	now := time.Date(2026, 8, 15, 1, 2, 3, 0, time.UTC)
	claim := recovery.RecoveryWorkerClaim{
		LeaseExpiresAt: now.Add(time.Minute), AbsoluteDeadline: now.Add(time.Hour),
	}
	if got := managedRecoveryProviderSessionExpiry(claim); !got.Equal(claim.AbsoluteDeadline) {
		t.Fatalf("provider session expiry=%s, want immutable absolute deadline %s", got, claim.AbsoluteDeadline)
	}
	renewed := claim
	renewed.LeaseExpiresAt = now.Add(10 * time.Minute)
	if got := managedRecoveryProviderSessionExpiry(renewed); !got.Equal(claim.AbsoluteDeadline) {
		t.Fatalf("renewed provider session expiry=%s, want unchanged absolute deadline %s", got, claim.AbsoluteDeadline)
	}
}

func TestRecoveryProductionAuthorityBuildRequiresAuthoritiesAndBindsServices(t *testing.T) {
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.BackupRepository{}, &model.RecoveryPoint{}, &model.RecoveryPointManifest{},
		&model.CatalogGeneration{}, &model.CatalogEntry{},
		&model.BackupAssetRecoveryPlan{}, &model.BackupAssetRecoveryPlanItem{},
		&model.BackupAssetRecoveryPreflight{}, &model.BackupAssetRecoveryGrant{},
		&model.BackupAssetRecoveryJob{}, &model.BackupAssetRecoveryJobItem{},
		&model.BackupAssetRecoveryAttempt{}, &model.BackupAssetRecoveryCheckpoint{},
		&model.BackupAssetRecoveryEvidence{}, &model.BackupAssetRecoveryResultSet{},
		&model.BackupAssetRecoveryResult{}, &model.BackupAssetRecoveryNodeLease{}, &model.TaskRun{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 1, 2, 3, 0, time.UTC)
	settingsService := settings.NewService(db)
	config, err := backupasset.NewFoundationService(settingsService).RecoveryConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.Enabled = true
	lease, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: time.Minute, Heartbeat: 10 * time.Second, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	repositoryResolver := &repository.Service{}
	cleanupTimer := newManagedRecoveryTimerFake()
	dependencies := managedRecoveryGraphBuildDependencies{
		DB: db, Settings: settingsService, Now: func() time.Time { return now },
		Metrics:      recovery.NoopMetrics{},
		SourceLeases: lease, NodeAdmission: managedRecoveryNodeAdmissionFake{},
		CleanupWorkerID: "recovery-cleanup-test",
		CleanupNewTimer: func(time.Duration) managedRecoveryTimer {
			return cleanupTimer
		},
		NodeRevisions:        managedRecoveryNodeRevisionSourceFake{},
		PreflightEvidence:    managedRecoveryPreflightEvidenceAuthorityFake{},
		AuthorityRevalidator: managedRecoveryAuthorityRevalidatorFake{},
		PlanSecurity:         managedRecoveryPlanSecurityAuthorityFake{},
		WorkspaceKeys:        backupasset.NewKeyring(db, func() time.Time { return now }),
		Audit:                managedRecoveryAuditWriterFake{}, ContentLifecycle: managedRecoveryContentLifecycleFake{},
		SourceResolver:          repositoryResolver,
		Dialer:                  sshutil.NewNodeDialer(db),
		ReconciliationRevisions: managedRecoveryReconciliationRevisionSourceFake{},
		ReconciliationFindings:  managedRecoveryReconciliationFindingSinkFake{},
	}

	graph, err := buildManagedRecoveryGraph(context.Background(), config, dependencies)
	if err != nil {
		t.Fatalf("build enabled Recovery graph: %v", err)
	}
	if graph == nil || !graph.admissionEnabled || graph.application == nil || graph.plan == nil || graph.preflight == nil ||
		graph.authorization == nil || graph.target == nil || graph.workerCoordinator == nil || graph.worker == nil ||
		graph.resultLifecycle == nil || graph.resultDelivery == nil || graph.reconciliation == nil ||
		graph.rsyncRestorePort == nil || graph.revokeDrainDelivery == nil || graph.shutdownLifecycle == nil {
		t.Fatalf("incomplete enabled Recovery graph: %+v", graph)
	}
	if _, err := graph.application.CreatePlan(context.Background(), recovery.CreatePlanIntentRequest{
		RequesterID: 7, Endpoint: "/api/v1/recovery-plans", IdempotencyKey: "recovery-production-gap-key",
		RepositoryID: strings.Repeat("a", 32), RecoveryPointID: strings.Repeat("b", 32),
		CatalogGenerationID: strings.Repeat("c", 32), EntryIDs: []string{strings.Repeat("d", 64)},
		TargetMode: recovery.TargetModeInPlace, TargetNodeID: 9, TargetRootID: "recovery-root",
		ConflictPolicy: recovery.ConflictExactMirror,
	}); !errors.Is(err, recovery.ErrRecoverySourceUnavailable) {
		t.Fatalf("production create materialization error=%v, want concrete materializer to reach source freezing", err)
	}
	if _, err := graph.application.Preflight(context.Background(), recovery.RecoveryPreflightRequest{
		RequesterID: 7, PlanID: strings.Repeat("e", 32), ExpectedPlanRevision: 1,
	}); !errors.Is(err, recovery.ErrRecoveryAPIObjectNotFound) {
		t.Fatalf("production preflight materialization error=%v, want owner-scoped hidden not found", err)
	}
	claimExecutor, ok := graph.worker.executor.(*managedRecoveryResolvedClaimExecutor)
	if !ok || claimExecutor.restorePort != graph.rsyncRestorePort {
		t.Fatalf("worker executor=%T restore port=%T, want graph Repository restore port",
			graph.worker.executor, claimExecutor.restorePort)
	}
	runnerBridge, runnerOK := graph.rsyncRestorePort.runner.(*managedRecoveryClaimRestoreBridge)
	if !runnerOK || runnerBridge == nil || graph.rsyncRestorePort.writer == nil ||
		any(graph.rsyncRestorePort.writer) == any(runnerBridge) {
		t.Fatalf("Repository restore runner=%T writer=%T, want a claim bridge and separate Recovery writer adapter",
			graph.rsyncRestorePort.runner, graph.rsyncRestorePort.writer)
	}
	if graph.workerCoordinatorSourceResolver != repositoryResolver {
		t.Fatalf("worker source resolver=%T, want exact Repository service", graph.workerCoordinatorSourceResolver)
	}
	if _, ok := graph.target.(recovery.TargetReconciliationPort); !ok {
		t.Fatalf("production target=%T does not provide the separate reconciliation port", graph.target)
	}

	for name, clear := range map[string]func(*managedRecoveryGraphBuildDependencies){
		"node revisions":              func(value *managedRecoveryGraphBuildDependencies) { value.NodeRevisions = nil },
		"preflight evidence":          func(value *managedRecoveryGraphBuildDependencies) { value.PreflightEvidence = nil },
		"authority revalidator":       func(value *managedRecoveryGraphBuildDependencies) { value.AuthorityRevalidator = nil },
		"plan security":               func(value *managedRecoveryGraphBuildDependencies) { value.PlanSecurity = nil },
		"reconciliation revisions":    func(value *managedRecoveryGraphBuildDependencies) { value.ReconciliationRevisions = nil },
		"reconciliation finding sink": func(value *managedRecoveryGraphBuildDependencies) { value.ReconciliationFindings = nil },
		"metrics":                     func(value *managedRecoveryGraphBuildDependencies) { value.Metrics = nil },
	} {
		t.Run(name, func(t *testing.T) {
			missing := dependencies
			clear(&missing)
			if graph, err := buildManagedRecoveryGraph(context.Background(), config, missing); graph != nil ||
				!errors.Is(err, backupasset.ErrInvalidState) {
				t.Fatalf("missing %s graph=%p err=%v, want fail-closed invalid state", name, graph, err)
			}
		})
	}

	for name, makeUnavailable := range map[string]func(*managedRecoveryGraphBuildDependencies){
		"preflight": func(value *managedRecoveryGraphBuildDependencies) {
			value.PreflightEvidence = managedRecoveryUnavailablePreflightEvidenceAuthority{}
		},
		"live effects": func(value *managedRecoveryGraphBuildDependencies) {
			value.AuthorityRevalidator = managedRecoveryUnavailableAuthorityRevalidator{}
		},
		"reconciliation": func(value *managedRecoveryGraphBuildDependencies) {
			value.ReconciliationRevisions = managedRecoveryUnavailableReconciliationRevisionSource{}
		},
	} {
		t.Run("known unavailable "+name, func(t *testing.T) {
			unavailable := dependencies
			makeUnavailable(&unavailable)
			publication := newManagedRecoveryPublication()
			manager, managerErr := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
				Publication: publication,
				Build: func(ctx context.Context, candidate backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
					return buildManagedRecoveryGraph(ctx, candidate, unavailable)
				},
			})
			if managerErr != nil {
				t.Fatal(managerErr)
			}
			if startupErr := manager.StartupWithConfig(context.Background(), config); !errors.Is(startupErr, backupasset.ErrInvalidState) {
				t.Fatalf("known-unavailable %s startup error=%v", name, startupErr)
			}
			if publication.current() != nil {
				t.Fatalf("known-unavailable %s authority published a graph", name)
			}
		})
	}

	disabled := config
	disabled.Enabled = false
	disabledGraph, err := buildManagedRecoveryGraph(context.Background(), disabled, dependencies)
	if err != nil || disabledGraph == nil || disabledGraph.admissionEnabled ||
		disabledGraph.reconcileMetadata == nil || disabledGraph.resultLifecycle == nil || disabledGraph.cleanup == nil ||
		disabledGraph.reconciliation == nil || disabledGraph.downgradeReconciler == nil ||
		disabledGraph.revokeDrainDelivery == nil || disabledGraph.shutdownLifecycle == nil {
		graph = disabledGraph
		t.Fatalf("default-disabled maintenance graph=%+v err=%v", graph, err)
	}
	if err := disabledGraph.reconcileMetadata(context.Background()); err != nil {
		t.Fatalf("default-disabled startup reconciliation: %v", err)
	}
	if disabledGraph.currentReconciliationState() != recovery.RecoveryReconciliationBlocked {
		t.Fatalf("default-disabled reconciliation state=%q, want blocked", disabledGraph.currentReconciliationState())
	}
	if _, err := disabledGraph.downgradeReconciler.ReconcileDowngradeReadiness(
		context.Background(), recovery.RecoveryDowngradeReconciliationRequest{
			AdmissionGeneration: "disabled-reconciliation-generation",
		},
	); !errors.Is(err, recovery.ErrRecoveryReconciliationUnavailable) {
		t.Fatalf("default-disabled logical reconciliation error=%v, want unavailable", err)
	}
	if disabledGraph.cleanup.lifecycle != disabledGraph.resultLifecycle ||
		disabledGraph.cleanup.workerID != dependencies.CleanupWorkerID ||
		disabledGraph.cleanup.cadence != config.CleanupCadence ||
		disabledGraph.cleanup.batchSize != config.CleanupBatchSize ||
		disabledGraph.cleanup.retryBase != config.CleanupRetryBase ||
		disabledGraph.cleanup.retryMaxDelay != config.CleanupRetryMaxDelay {
		t.Fatalf("default-disabled cleanup owner=%+v config=%+v", disabledGraph.cleanup, config)
	}
	disabledGraph.startRun(context.Background())
	waitManagedRecoveryTimerResets(t, cleanupTimer, 1)
	if err := disabledGraph.stopRun(context.Background()); err != nil {
		t.Fatalf("stop default-disabled cleanup owner: %v", err)
	}
}

func TestManagedRecoveryDeliveryShutdownRevokesAndDrainsOnlyPublishedRecoveryJobs(t *testing.T) {
	db := openRuntimeTestDB(t)
	now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	jobA, jobB := strings.Repeat("1", 32), strings.Repeat("2", 32)
	assetPoint, assetCatalog, assetEntry := strings.Repeat("3", 32), strings.Repeat("4", 32), strings.Repeat("5", 64)
	grants := []model.BackupAssetDeliveryGrant{
		managedRecoveryDeliveryGrant(now, strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32),
			string(content.DeliveryResourceRecoveryResult), &jobA, nil, nil, nil, content.DeliveryActive),
		managedRecoveryDeliveryGrant(now, strings.Repeat("d", 32), strings.Repeat("e", 32), strings.Repeat("f", 32),
			string(content.DeliveryResourceRecoveryResult), &jobB, nil, nil, nil, content.DeliveryRevoked),
		managedRecoveryDeliveryGrant(now, strings.Repeat("6", 32), strings.Repeat("7", 32), strings.Repeat("8", 32),
			string(content.DeliveryResourceBackupAsset), nil, &assetPoint, &assetCatalog, &assetEntry, content.DeliveryActive),
	}
	if err := db.Create(&grants).Error; err != nil {
		t.Fatal(err)
	}
	lifecycle := &managedRecoveryDeliveryLifecycleRecorder{}
	graph := &managedRecoveryGraph{}
	drainTimeout := 5 * time.Second
	shutdown := managedRecoveryDeliveryShutdown(graph, db, lifecycle, func() time.Time { return now }, drainTimeout)
	if shutdown == nil {
		t.Fatal("Recovery delivery shutdown callback was not constructed")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lifecycle.callCount() != 0 {
		t.Fatalf("unpublished candidate revoked Recovery deliveries: %+v", lifecycle)
	}

	graph.deliveryShutdownActive.Store(true)
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{jobA, jobB}
	if got := lifecycle.revokedSnapshot(); !slices.Equal(got, want) {
		t.Fatalf("revoked Recovery jobs=%v, want %v", got, want)
	}
	if got := lifecycle.canceledSnapshot(); !slices.Equal(got, want) {
		t.Fatalf("canceled Recovery jobs=%v, want %v", got, want)
	}
	if got := lifecycle.drainedSnapshot(); !slices.Equal(got, want) {
		t.Fatalf("drained Recovery jobs=%v, want %v", got, want)
	}
	for _, deadline := range lifecycle.drainDeadlinesSnapshot() {
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > drainTimeout {
			t.Fatalf("Recovery drain deadline remaining=%s, want bounded by %s", remaining, drainTimeout)
		}
	}
}

func managedRecoveryDeliveryGrant(
	now time.Time,
	id string,
	deliveryID string,
	leaseID string,
	resourceKind string,
	recoveryJobID *string,
	recoveryPointID *string,
	catalogGenerationID *string,
	entryID *string,
	state content.DeliveryState,
) model.BackupAssetDeliveryGrant {
	resultID := strings.Repeat("9", 32)
	grant := model.BackupAssetDeliveryGrant{
		ID: id, DeliveryID: deliveryID, ResourceKind: resourceKind,
		RecoveryPointID: recoveryPointID, CatalogGenerationID: catalogGenerationID, EntryID: entryID,
		RecoveryJobID: recoveryJobID, OwnerUserID: 1, SessionJTI: strings.Repeat("0", 32),
		SessionRole: "admin", SessionExpiresAt: now.Add(time.Hour), Action: string(content.DeliveryDownload),
		MethodPolicy: string(content.MethodGetHead), RangePolicy: string(content.RangeSingle),
		Renderer: string(content.RendererAttachment), Profile: string(content.ProfileOriginalV1),
		Classification: string(content.ClassificationUnknown), ClassificationRevision: 1,
		ClassificationSourceRevision: 1, ProviderKind: string(backupasset.ProviderRsync),
		SourceFingerprint: "source", FingerprintStrength: "strong", RepresentationETag: `"recovery"`,
		DetectedMediaType: "application/octet-stream", CookieSecretHash: strings.Repeat("1", 64),
		State: string(state), LeaseID: leaseID, LeaseAttemptID: strings.Repeat("2", 32),
		LeaseFenceTokenHash: strings.Repeat("3", 64), AbsoluteExpiresAt: now.Add(time.Hour),
		IdleExpiresAt: now.Add(time.Minute), IdleTTLSeconds: 60, LastActivityAt: now,
		MaxBytesPerRequest: 1, MaxCumulativeBytes: 1, MaxRequests: 1, MaxInFlight: 1,
		Version: 1, AuditState: "none", CreatedAt: now, UpdatedAt: now,
	}
	if recoveryJobID != nil {
		grant.RecoveryResultID = &resultID
	}
	return grant
}

func TestManagedRecoveryDisabledGraphRunsLogicalReconciliationImmediatelyAndOnCadence(t *testing.T) {
	db := openRuntimeTestDB(t)
	now := time.Date(2026, 8, 13, 1, 2, 3, 0, time.UTC)
	settingsService := settings.NewService(db)
	config, err := backupasset.NewFoundationService(settingsService).RecoveryConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.Enabled = false
	timers := &managedRecoveryTimerFactoryFake{}
	graph, err := buildManagedRecoveryGraph(context.Background(), config, managedRecoveryGraphBuildDependencies{
		DB: db, Settings: settingsService, Now: func() time.Time { return now },
		NodeAdmission:           managedRecoveryNodeAdmissionFake{},
		CleanupWorkerID:         "recovery-disabled-reconciliation-test",
		CleanupNewTimer:         timers.New,
		NodeRevisions:           managedRecoveryNodeRevisionSourceFake{},
		WorkspaceKeys:           backupasset.NewKeyring(db, func() time.Time { return now }),
		Audit:                   managedRecoveryAuditWriterFake{},
		ContentLifecycle:        managedRecoveryContentLifecycleFake{},
		Dialer:                  sshutil.NewNodeDialer(db),
		ReconciliationRevisions: managedRecoveryUnavailableReconciliationRevisionSource{},
	})
	if err != nil {
		t.Fatalf("build disabled Recovery graph: %v", err)
	}
	startupReconciler := &managedRecoveryDowngradeReconcilerFake{result: recovery.RecoveryReconciliationResult{
		State: recovery.RecoveryReconciliationBlocked, Complete: false,
		Counts: recovery.RecoveryReconciliationCounts{ScanIncomplete: 1},
	}}
	graph.downgradeReconciler = startupReconciler
	if err := graph.reconcileMetadata(context.Background()); err != nil {
		t.Fatalf("disabled startup reconciliation: %v", err)
	}
	if calls := startupReconciler.calls.Load(); calls != 1 {
		t.Fatalf("disabled startup reconciliation calls=%d, want one blocked pass", calls)
	}
	if graph.currentReconciliationState() != recovery.RecoveryReconciliationBlocked {
		t.Fatalf("startup reconciliation state=%q, want blocked", graph.currentReconciliationState())
	}

	reconciler := &managedRecoveryDowngradeReconcilerFake{err: recovery.ErrRecoveryReconciliationUnavailable}
	graph.downgradeReconciler = reconciler
	graph.reconciliationOwner.reconciler = reconciler
	firstDone := graph.startRun(context.Background())
	secondDone := graph.startRun(context.Background())
	if firstDone != secondDone {
		t.Fatal("disabled graph started duplicate maintenance owner loops")
	}
	waitManagedRecoveryReconciliationCalls(t, reconciler, 1)
	ownedTimers := timers.waitForCount(t, 2)
	for _, timer := range ownedTimers {
		timer.ticks <- now
	}
	waitManagedRecoveryReconciliationCalls(t, reconciler, 2)
	if err := graph.stopRun(context.Background()); err != nil {
		t.Fatalf("join disabled Recovery graph: %v", err)
	}
	if graph.currentReconciliationState() != recovery.RecoveryReconciliationBlocked {
		t.Fatalf("unavailable cadence reconciliation state=%q, want blocked", graph.currentReconciliationState())
	}
	if calls := reconciler.calls.Load(); calls != 2 {
		t.Fatalf("disabled reconciliation calls=%d, want immediate plus one cadence pass", calls)
	}
}

func TestManagedRecoveryEnabledGraphRunsLogicalReconciliationImmediatelyAndOnCadence(t *testing.T) {
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.BackupAssetRecoveryPlan{}, &model.BackupAssetRecoveryPlanItem{},
		&model.BackupAssetRecoveryPreflight{}, &model.BackupAssetRecoveryGrant{},
		&model.BackupAssetRecoveryJob{}, &model.BackupAssetRecoveryJobItem{},
		&model.BackupAssetRecoveryAttempt{}, &model.BackupAssetRecoveryCheckpoint{},
		&model.BackupAssetRecoveryEvidence{}, &model.BackupAssetRecoveryResultSet{},
		&model.BackupAssetRecoveryResult{}, &model.BackupAssetRecoveryNodeLease{}, &model.TaskRun{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 1, 2, 3, 0, time.UTC)
	settingsService := settings.NewService(db)
	config, err := backupasset.NewFoundationService(settingsService).RecoveryConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.Enabled = true
	lease, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: time.Minute, Heartbeat: 10 * time.Second, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	timers := &managedRecoveryTimerFactoryFake{}
	graph, err := buildManagedRecoveryGraph(context.Background(), config, managedRecoveryGraphBuildDependencies{
		DB: db, Settings: settingsService, Now: func() time.Time { return now },
		Metrics:                 recovery.NoopMetrics{},
		SourceLeases:            lease,
		NodeAdmission:           managedRecoveryNodeAdmissionFake{},
		CleanupWorkerID:         "recovery-enabled-reconciliation-test",
		CleanupNewTimer:         timers.New,
		NodeRevisions:           managedRecoveryNodeRevisionSourceFake{},
		PreflightEvidence:       managedRecoveryPreflightEvidenceAuthorityFake{},
		AuthorityRevalidator:    managedRecoveryAuthorityRevalidatorFake{},
		PlanSecurity:            managedRecoveryPlanSecurityAuthorityFake{},
		WorkspaceKeys:           backupasset.NewKeyring(db, func() time.Time { return now }),
		Audit:                   managedRecoveryAuditWriterFake{},
		ContentLifecycle:        managedRecoveryContentLifecycleFake{},
		SourceResolver:          &repository.Service{},
		Dialer:                  sshutil.NewNodeDialer(db),
		ReconciliationRevisions: managedRecoveryReconciliationRevisionSourceFake{},
		ReconciliationFindings:  managedRecoveryReconciliationFindingSinkFake{},
	})
	if err != nil {
		t.Fatalf("build enabled Recovery graph: %v", err)
	}
	if graph.reconciliationOwner == nil {
		t.Fatal("enabled Recovery graph omitted the managed reconciliation owner")
	}
	reconciler := &managedRecoveryDowngradeReconcilerFake{result: recovery.RecoveryReconciliationResult{
		State: recovery.RecoveryReconciliationClear, Complete: true,
	}}
	graph.downgradeReconciler = reconciler
	graph.reconciliationOwner.reconciler = reconciler
	if err := graph.reconcileMetadata(context.Background()); err != nil {
		t.Fatalf("enabled startup reconciliation: %v", err)
	}
	if calls := reconciler.calls.Load(); calls != 1 {
		t.Fatalf("enabled startup reconciliation calls=%d, want one complete pass", calls)
	}
	if graph.currentReconciliationState() != recovery.RecoveryReconciliationClear {
		t.Fatalf("enabled startup reconciliation state=%q, want clear", graph.currentReconciliationState())
	}

	done := graph.startRun(context.Background())
	waitManagedRecoveryReconciliationCalls(t, reconciler, 2)
	ownedTimers := timers.waitForCount(t, 2)
	for _, timer := range ownedTimers {
		timer.ticks <- now
	}
	waitManagedRecoveryReconciliationCalls(t, reconciler, 3)
	if err := graph.stopRun(context.Background()); err != nil {
		t.Fatalf("join enabled Recovery graph: %v", err)
	}
	select {
	case <-done:
	default:
		t.Fatal("enabled Recovery graph did not join reconciliation owner")
	}
}

func TestManagedRecoveryReconciliationOwnerStopsOnContextError(t *testing.T) {
	reconciler := &managedRecoveryDowngradeReconcilerFake{err: context.Canceled}
	timer := newManagedRecoveryTimerFake()
	owner, err := newManagedRecoveryReconciliationOwner(managedRecoveryReconciliationOwnerDependencies{
		Reconciler: reconciler, Generation: "recovery-reconciliation-cancellation", Cadence: time.Minute,
		NewTimer: func(time.Duration) managedRecoveryTimer { return timer },
		Record:   func(recovery.RecoveryReconciliationState) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		owner.Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reconciliation owner swallowed context cancellation and scheduled another pass")
	}
	if resets := timer.resetsSnapshot(); len(resets) != 0 {
		t.Fatalf("context cancellation scheduled reconciliation retries=%v", resets)
	}
}

type managedRecoveryCleanupLifecycleFake struct {
	events            []string
	claimResultErrors []error
	resultProgress    recovery.RecoveryCleanupProgress
	candidates        []recovery.ScheduledCleanupCandidate
	listLimits        []int
}

func (fake *managedRecoveryCleanupLifecycleFake) ListScheduledCleanupCandidates(
	_ context.Context,
	limit int,
) ([]recovery.ScheduledCleanupCandidate, error) {
	fake.listLimits = append(fake.listLimits, limit)
	if limit > len(fake.candidates) {
		limit = len(fake.candidates)
	}
	return append([]recovery.ScheduledCleanupCandidate(nil), fake.candidates[:limit]...), nil
}

func (fake *managedRecoveryCleanupLifecycleFake) ClaimScheduledCleanup(
	_ context.Context,
	request recovery.ClaimRecoveryResultCleanupRequest,
) (recovery.RecoveryResultCleanupClaim, error) {
	fake.events = append(fake.events, "result:claim:"+request.ResultSetID+":"+request.WorkerID)
	if len(fake.claimResultErrors) > 0 {
		err := fake.claimResultErrors[0]
		fake.claimResultErrors = fake.claimResultErrors[1:]
		if err != nil {
			return recovery.RecoveryResultCleanupClaim{}, err
		}
	}
	return recovery.RecoveryResultCleanupClaim{
		ResultSetID: request.ResultSetID, JobID: strings.Repeat("9", 32), WorkerID: request.WorkerID,
		CleanupFence: 1, CleanupAttempt: 1, NodeLeaseID: strings.Repeat("8", 32), NodeFence: 1,
		LeaseExpiresAt: time.Date(2026, 8, 13, 2, 0, 0, 0, time.UTC), Phase: recovery.CleanupPhaseClaimed,
	}, nil
}

func (fake *managedRecoveryCleanupLifecycleFake) RevokeRecoveryResultCleanup(
	_ context.Context,
	claim recovery.RecoveryResultCleanupClaim,
) (recovery.RecoveryResultCleanupClaim, error) {
	fake.events = append(fake.events, "result:revoke:"+claim.ResultSetID)
	claim.Phase = recovery.CleanupPhaseRevoked
	return claim, nil
}

func (fake *managedRecoveryCleanupLifecycleFake) DrainRecoveryResultCleanup(
	_ context.Context,
	claim recovery.RecoveryResultCleanupClaim,
) (recovery.RecoveryResultCleanupClaim, error) {
	fake.events = append(fake.events, "result:drain:"+claim.ResultSetID)
	claim.Phase = recovery.CleanupPhaseDrained
	return claim, nil
}

func (fake *managedRecoveryCleanupLifecycleFake) ValidateRecoveryResultCleanup(
	_ context.Context,
	claim recovery.RecoveryResultCleanupClaim,
) (recovery.RecoveryResultCleanupClaim, error) {
	fake.events = append(fake.events, "result:validate:"+claim.ResultSetID)
	claim.Phase = recovery.CleanupPhaseValidated
	return claim, nil
}

func (fake *managedRecoveryCleanupLifecycleFake) AdvanceRecoveryResultCleanup(
	_ context.Context,
	claim recovery.RecoveryResultCleanupClaim,
) (recovery.RecoveryCleanupProgress, error) {
	fake.events = append(fake.events, "result:advance:"+claim.ResultSetID)
	if fake.resultProgress.Phase != "" {
		return fake.resultProgress, nil
	}
	return recovery.RecoveryCleanupProgress{Phase: recovery.CleanupPhaseTombstoned, Complete: true}, nil
}

func (fake *managedRecoveryCleanupLifecycleFake) ClaimWorkspaceCleanup(
	_ context.Context,
	request recovery.ClaimRecoveryWorkspaceCleanupRequest,
) (recovery.RecoveryWorkspaceCleanupClaim, error) {
	fake.events = append(fake.events, "workspace:claim:"+request.JobID+":"+request.WorkerID)
	return recovery.RecoveryWorkspaceCleanupClaim{
		JobID: request.JobID, WorkerID: request.WorkerID,
		CleanupFence: 1, CleanupAttempt: 1, NodeLeaseID: strings.Repeat("7", 32), NodeFence: 1,
		LeaseExpiresAt: time.Date(2026, 8, 13, 2, 0, 0, 0, time.UTC), Phase: recovery.CleanupPhaseClaimed,
	}, nil
}

func (fake *managedRecoveryCleanupLifecycleFake) RevokeRecoveryWorkspaceCleanup(
	_ context.Context,
	claim recovery.RecoveryWorkspaceCleanupClaim,
) (recovery.RecoveryWorkspaceCleanupClaim, error) {
	fake.events = append(fake.events, "workspace:revoke:"+claim.JobID)
	claim.Phase = recovery.CleanupPhaseRevoked
	return claim, nil
}

func (fake *managedRecoveryCleanupLifecycleFake) DrainRecoveryWorkspaceCleanup(
	_ context.Context,
	claim recovery.RecoveryWorkspaceCleanupClaim,
) (recovery.RecoveryWorkspaceCleanupClaim, error) {
	fake.events = append(fake.events, "workspace:drain:"+claim.JobID)
	claim.Phase = recovery.CleanupPhaseDrained
	return claim, nil
}

func (fake *managedRecoveryCleanupLifecycleFake) ValidateRecoveryWorkspaceCleanup(
	_ context.Context,
	claim recovery.RecoveryWorkspaceCleanupClaim,
) (recovery.RecoveryWorkspaceCleanupClaim, error) {
	fake.events = append(fake.events, "workspace:validate:"+claim.JobID)
	claim.Phase = recovery.CleanupPhaseValidated
	return claim, nil
}

func (fake *managedRecoveryCleanupLifecycleFake) AdvanceRecoveryWorkspaceCleanup(
	_ context.Context,
	claim recovery.RecoveryWorkspaceCleanupClaim,
) (recovery.RecoveryCleanupProgress, error) {
	fake.events = append(fake.events, "workspace:advance:"+claim.JobID)
	return recovery.RecoveryCleanupProgress{Phase: recovery.CleanupPhaseTombstoned, Complete: true}, nil
}

func TestManagedRecoveryCleanupOwnerProcessesOnlyBoundedDueLifecycleCandidates(t *testing.T) {
	dueResultID := strings.Repeat("1", 32)
	dueWorkspaceID := strings.Repeat("3", 32)

	lifecycle := &managedRecoveryCleanupLifecycleFake{candidates: []recovery.ScheduledCleanupCandidate{
		{Kind: recovery.ScheduledCleanupResultSet, ID: dueResultID},
		{Kind: recovery.ScheduledCleanupWorkspace, ID: dueWorkspaceID},
	}}
	owner, err := newManagedRecoveryCleanupOwner(managedRecoveryCleanupOwnerDependencies{
		Lifecycle: lifecycle, WorkerID: "recovery-cleanup-test",
		Cadence: time.Minute, BatchSize: 2, RetryBase: time.Second, RetryMaxDelay: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	processed, err := owner.runPass(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if processed != 2 {
		t.Fatalf("processed cleanup candidates=%d, want bounded batch 2", processed)
	}
	if !reflect.DeepEqual(lifecycle.listLimits, []int{2}) {
		t.Fatalf("cleanup candidate limits=%v, want configured batch 2", lifecycle.listLimits)
	}
	want := []string{
		"result:claim:" + dueResultID + ":recovery-cleanup-test",
		"result:revoke:" + dueResultID,
		"result:drain:" + dueResultID,
		"result:validate:" + dueResultID,
		"result:advance:" + dueResultID,
		"workspace:claim:" + dueWorkspaceID + ":recovery-cleanup-test",
		"workspace:revoke:" + dueWorkspaceID,
		"workspace:drain:" + dueWorkspaceID,
		"workspace:validate:" + dueWorkspaceID,
		"workspace:advance:" + dueWorkspaceID,
	}
	if !reflect.DeepEqual(lifecycle.events, want) {
		t.Fatalf("cleanup lifecycle events=%v, want %v", lifecycle.events, want)
	}
}

func TestManagedRecoveryCleanupOwnerRetriesBusyButDefersClaimConflictsToCadence(t *testing.T) {
	for _, test := range []struct {
		name      string
		claimErr  error
		wantDelay time.Duration
	}{
		{name: "node busy", claimErr: recovery.ErrRecoveryResultCleanupBusy, wantDelay: time.Second},
		{name: "claim conflict", claimErr: recovery.ErrRecoveryResultCleanupConflict, wantDelay: time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := &managedRecoveryCleanupLifecycleFake{
				claimResultErrors: []error{test.claimErr},
				candidates: []recovery.ScheduledCleanupCandidate{{
					Kind: recovery.ScheduledCleanupResultSet, ID: strings.Repeat("1", 32),
				}},
			}
			timer := newManagedRecoveryTimerFake()
			owner, err := newManagedRecoveryCleanupOwner(managedRecoveryCleanupOwnerDependencies{
				Lifecycle: lifecycle, WorkerID: "recovery-cleanup-test",
				Cadence: time.Minute, BatchSize: 1, RetryBase: time.Second, RetryMaxDelay: 4 * time.Second,
				NewTimer: func(time.Duration) managedRecoveryTimer { return timer },
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				owner.Run(ctx)
				close(done)
			}()
			waitManagedRecoveryTimerResets(t, timer, 1)
			cancel()
			<-done
			if got := timer.resetsSnapshot(); !reflect.DeepEqual(got, []time.Duration{test.wantDelay}) {
				t.Fatalf("cleanup reset delays=%v, want %v", got, test.wantDelay)
			}
		})
	}
}

func TestManagedRecoveryCleanupOwnerRunsImmediateCadenceAndBoundedRetry(t *testing.T) {
	now := time.Date(2026, 8, 13, 1, 2, 3, 0, time.UTC)
	resultSetID := strings.Repeat("1", 32)
	lifecycle := &managedRecoveryCleanupLifecycleFake{claimResultErrors: []error{
		errors.New("cleanup unavailable once"), nil,
	}, candidates: []recovery.ScheduledCleanupCandidate{{
		Kind: recovery.ScheduledCleanupResultSet, ID: resultSetID,
	}}}
	timer := newManagedRecoveryTimerFake()
	owner, err := newManagedRecoveryCleanupOwner(managedRecoveryCleanupOwnerDependencies{
		Lifecycle: lifecycle, WorkerID: "recovery-cleanup-test",
		Cadence: time.Minute, BatchSize: 1, RetryBase: time.Second, RetryMaxDelay: 4 * time.Second,
		NewTimer: func(duration time.Duration) managedRecoveryTimer {
			if duration != time.Minute {
				t.Fatalf("initial cleanup timer duration=%v, want cadence", duration)
			}
			return timer
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		owner.Run(ctx)
		close(done)
	}()
	waitManagedRecoveryTimerResets(t, timer, 1)
	if got := timer.resetsSnapshot(); !reflect.DeepEqual(got, []time.Duration{time.Second}) {
		t.Fatalf("cleanup retry resets=%v, want first configured retry", got)
	}
	timer.ticks <- now
	waitManagedRecoveryTimerResets(t, timer, 2)
	if got := timer.resetsSnapshot(); !reflect.DeepEqual(got, []time.Duration{time.Second, time.Minute}) {
		t.Fatalf("cleanup recovery resets=%v, want retry then cadence", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup owner did not join after cancellation")
	}
	if timer.stopCallsSnapshot() != 1 {
		t.Fatalf("cleanup timer Stop calls=%d, want 1", timer.stopCallsSnapshot())
	}
}

func TestManagedRecoveryCleanupOwnerAdvancesOneRemoteChunkPerCandidatePass(t *testing.T) {
	lifecycle := &managedRecoveryCleanupLifecycleFake{resultProgress: recovery.RecoveryCleanupProgress{
		Phase: recovery.CleanupPhaseDeleteStarted, Complete: false, RemovedEntries: 256,
	}}
	owner := &managedRecoveryCleanupOwner{lifecycle: lifecycle, workerID: "recovery-cleanup-test"}
	resultSetID := strings.Repeat("1", 32)

	if err := owner.processResult(context.Background(), resultSetID); err != nil {
		t.Fatal(err)
	}
	advanceCalls := 0
	for _, event := range lifecycle.events {
		if event == "result:advance:"+resultSetID {
			advanceCalls++
		}
	}
	if advanceCalls != 1 {
		t.Fatalf("cleanup advance calls=%d, want one bounded remote chunk per pass", advanceCalls)
	}
}

func TestRuntimeDependenciesKeepRecoveryAuthoritiesPrivate(t *testing.T) {
	typeOfDependencies := reflect.TypeOf(Dependencies{})
	for _, forbidden := range []string{
		"RecoveryNodeRevisions",
		"RecoveryPreflightEvidence",
		"RecoveryAuthorityRevalidator",
		"RecoveryReconciliationRevisions",
		"RecoveryReconciliationFindings",
	} {
		if _, exposed := typeOfDependencies.FieldByName(forbidden); exposed {
			t.Fatalf("runtime dependencies expose caller-controlled Recovery authority %q", forbidden)
		}
	}
	field, exists := typeOfDependencies.FieldByName("AlertDispatcher")
	if !exists || field.Type != reflect.TypeOf((*alerting.Dispatcher)(nil)) {
		t.Fatalf("runtime dependencies AlertDispatcher=%v exists=%v, want canonical dispatcher dependency", field.Type, exists)
	}
}

func TestRecoveryProductionAuthorityRuntimePublishesCompleteGraphOnceAfterReconciliation(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RUNTIME_RECOVERY_ENABLED_COMPOSITION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.WrappedDomainKey{}, &model.BackupAssetRecoveryPlan{}, &model.BackupAssetRecoveryJob{},
		&model.BackupAssetRecoveryAttempt{}, &model.BackupAssetRecoveryNodeLease{},
		&model.BackupAssetRecoveryEvidence{}, &model.Task{}, &model.TaskRepositoryLink{},
		&model.RepositoryAccessBinding{},
	); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.UpdateMany(map[string]string{
		"backup_assets.enabled":              "true",
		"backup_assets.worker_local_enabled": "true",
		"backup_assets.derived_store_root":   filepath.Join(t.TempDir(), "derived"),
		"backup_assets.recovery.enabled":     "true",
	}); err != nil {
		t.Fatal(err)
	}
	transport := &runtimeTransportFake{}
	dependencies := Dependencies{
		DB: db, Settings: settingsService, Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{},
		SessionRevocations: &runtimeSessionRevocationsFake{},
	}
	setRuntimeAlertDispatcherForTest(t, &dependencies, alerting.NewDispatcher(db, settingsService, nil))

	runtime, err := New(dependencies)
	if err != nil {
		t.Fatalf("Runtime.New with production Recovery authorities: %v", err)
	}
	if err := runtime.processingManager.Startup(context.Background()); err != nil {
		t.Fatalf("start genuinely configured Processing runtime: %v", err)
	}
	if !runtime.processingManager.recoverySecurityReady() || runtime.processingManager.malwareEvidence == nil {
		t.Fatalf("configured Processing ready=%t evidence=%T",
			runtime.processingManager.recoverySecurityReady(), runtime.processingManager.malwareEvidence)
	}
	config, err := runtime.foundation.RecoveryConfig()
	if err != nil || !config.Enabled {
		t.Fatalf("enabled Recovery config=%+v err=%v", config, err)
	}
	manager := runtime.recoveryManager.(*managedRecoveryRuntime)
	originalBuild := manager.build
	builds, reconciliations, installs := 0, 0, 0
	manager.build = func(ctx context.Context, candidate backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
		builds++
		graph, buildErr := originalBuild(ctx, candidate)
		if buildErr != nil {
			return nil, buildErr
		}
		if graph == nil || !graph.admissionEnabled || graph.authorization == nil || graph.preflight == nil ||
			graph.reconciliation == nil || graph.workerCoordinator == nil || graph.target == nil ||
			graph.workerCoordinatorSourceResolver != runtime.repository {
			return nil, fmt.Errorf("production eligibility authorities composed an incomplete graph")
		}
		graph.reconcileMetadata = func(context.Context) error {
			if manager.publication.current() != nil {
				return errors.New("Recovery graph published before metadata reconciliation")
			}
			reconciliations++
			return nil
		}
		return graph, nil
	}
	manager.install = func(publication *managedRecoveryPublication, graph *managedRecoveryGraph) error {
		installs++
		if reconciliations != 1 {
			return fmt.Errorf("Recovery publication saw %d metadata reconciliations", reconciliations)
		}
		return publication.publish(graph)
	}
	if err := manager.StartupWithConfig(context.Background(), config); err != nil {
		t.Fatalf("start enabled Recovery graph from production authorities: %v", err)
	}
	if builds != 1 || reconciliations != 1 || installs != 1 || manager.publication.current() == nil {
		t.Fatalf("production publication builds=%d reconciliations=%d installs=%d graph=%p",
			builds, reconciliations, installs, manager.publication.current())
	}
	missingDispatcher := dependencies
	setRuntimeAlertDispatcherForTest(t, &missingDispatcher, nil)
	candidate, err := New(missingDispatcher)
	if err != nil {
		t.Fatalf("construct process runtime before enabled Recovery startup: %v", err)
	}
	if err := candidate.processingManager.Startup(context.Background()); err != nil {
		t.Fatalf("start candidate Processing runtime: %v", err)
	}
	candidateManager := candidate.recoveryManager.(*managedRecoveryRuntime)
	if err := candidateManager.StartupWithConfig(context.Background(), config); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("missing canonical alert dispatcher startup error=%v, want invalid state", err)
	}
	if candidateManager.publication.current() != nil {
		t.Fatal("missing canonical alert dispatcher published a Recovery graph")
	}

	published := manager.publication.current()
	now := time.Date(2026, 8, 15, 12, 30, 0, 0, time.UTC)
	asset := content.AuthorizedAsset{
		Ref: backupasset.AssetRef{
			RecoveryPointID: strings.Repeat("1", 32), EntryID: strings.Repeat("2", 64),
		},
		CatalogGenerationID: strings.Repeat("3", 32), Provider: backupasset.ProviderRsync,
		ProviderCapabilityRevision: 7, SourceFingerprint: "production-security-source-v1",
		EntryFingerprint: strings.Repeat("4", 64), FingerprintStrength: "strong", Size: 4096,
	}
	seedProcessingRuntimeMalwareEvidence(t, db, now, asset, strings.Repeat("a", 64))
	rawReaderErr := errors.New("FAKE_PRIVATE_PROCESSING_SECURITY_READER_FAILURE")
	runtime.processingManager.mu.Lock()
	runtime.processingManager.malwareEvidence = &processingRuntimeMalwareEvidenceReaderFake{err: rawReaderErr}
	runtime.processingManager.mu.Unlock()
	if _, err := runtime.processingManager.recoverySecurityObservation(context.Background(), asset); err == nil ||
		!errors.Is(err, backupasset.ErrCapabilityUnavailable) || strings.Contains(err.Error(), rawReaderErr.Error()) {
		t.Fatalf("failed production security reader error=%v, want sanitized unavailable", err)
	}
	if manager.publication.current() != published {
		t.Fatal("production security reader failure unpublished Recovery maintenance graph")
	}
}

func TestRecoveryProductionAuthorityRuntimeRejectsEnabledPublicationWithoutReadyProcessing(t *testing.T) {
	for _, testCase := range []struct {
		name                  string
		startControlPlaneOnly bool
		throughStartupPass    bool
	}{
		{name: "Processing disabled", throughStartupPass: true},
		{name: "Processing control plane only", startControlPlaneOnly: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RUNTIME_RECOVERY_PROCESSING_READINESS_KEY")
			secure.ResetForTesting()
			t.Cleanup(secure.ResetForTesting)
			db := openRuntimeTestDB(t)
			if err := db.AutoMigrate(
				&model.WrappedDomainKey{}, &model.BackupAssetRecoveryPlan{}, &model.BackupAssetRecoveryJob{},
				&model.BackupAssetRecoveryAttempt{}, &model.BackupAssetRecoveryNodeLease{},
				&model.BackupAssetRecoveryEvidence{}, &model.Node{}, &model.Task{},
				&model.BackupRepository{}, &model.RepositoryAccessBinding{}, &model.TaskRepositoryLink{},
				&model.RecoveryPoint{}, &model.BackupAssetManagedHistoryLatch{},
			); err != nil {
				t.Fatal(err)
			}
			settingsService := settings.NewService(db)
			updates := map[string]string{"backup_assets.recovery.enabled": "true"}
			if testCase.startControlPlaneOnly {
				updates["backup_assets.enabled"] = "true"
			}
			if err := settingsService.UpdateMany(updates); err != nil {
				t.Fatal(err)
			}
			transport := &runtimeTransportFake{}
			dependencies := Dependencies{
				DB: db, Settings: settingsService, Transport: transport, StreamTransport: transport,
				StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{},
				ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{},
			}
			setRuntimeAlertDispatcherForTest(t, &dependencies, alerting.NewDispatcher(db, settingsService, nil))
			runtime, err := New(dependencies)
			if err != nil {
				t.Fatal(err)
			}
			if testCase.startControlPlaneOnly {
				if err := runtime.processingManager.Startup(context.Background()); err != nil {
					t.Fatalf("start Processing control plane: %v", err)
				}
			}
			if runtime.processingManager.ready.Load() || runtime.processingManager.malwareEvidence != nil {
				t.Fatalf("non-ready Processing ready=%t malwareEvidence=%T",
					runtime.processingManager.ready.Load(), runtime.processingManager.malwareEvidence)
			}
			manager := runtime.recoveryManager.(*managedRecoveryRuntime)
			productionBuild := manager.build
			manager.build = func(ctx context.Context, candidate backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
				graph, buildErr := productionBuild(ctx, candidate)
				if buildErr != nil || graph == nil {
					return graph, buildErr
				}
				graph.reconcileMetadata = func(context.Context) error { return nil }
				return graph, nil
			}
			config, err := runtime.foundation.RecoveryConfig()
			if err != nil || !config.Enabled {
				t.Fatalf("Recovery config=%+v err=%v", config, err)
			}
			installs := 0
			manager.install = func(publication *managedRecoveryPublication, graph *managedRecoveryGraph) error {
				installs++
				return publication.publish(graph)
			}
			if testCase.throughStartupPass {
				err = runtime.StartupPass(context.Background())
			} else {
				err = manager.StartupWithConfig(context.Background(), config)
			}
			if !errors.Is(err, backupasset.ErrInvalidState) {
				t.Fatalf("enabled Recovery with non-ready Processing error=%v, want invalid state", err)
			}
			if installs != 0 || manager.publication.current() != nil {
				t.Fatalf("non-ready Processing installs=%d publication=%p", installs, manager.publication.current())
			}
		})
	}
}

func setRuntimeAlertDispatcherForTest(
	t *testing.T,
	dependencies *Dependencies,
	dispatcher *alerting.Dispatcher,
) {
	t.Helper()
	field := reflect.ValueOf(dependencies).Elem().FieldByName("AlertDispatcher")
	if !field.IsValid() {
		return
	}
	field.Set(reflect.ValueOf(dispatcher))
}

func TestManagedRecoveryNodeRevisionSourceRequiresExactManagedRecoveryCredential(t *testing.T) {
	now := time.Date(2026, 8, 13, 2, 3, 4, 0, time.UTC)
	db, node, key := seedManagedRecoveryCredentialAuthority(t, now)
	source := newManagedRecoveryNodeRevisionSource(func() time.Time { return now })

	resolve := func(t *testing.T) recovery.RecoveryNodeRevisionSnapshot {
		t.Helper()
		var snapshot recovery.RecoveryNodeRevisionSnapshot
		err := db.Transaction(func(tx *gorm.DB) error {
			var resolveErr error
			snapshot, resolveErr = source.ResolveRecoveryNodeRevisionsTx(
				context.Background(), tx, node.ID, recovery.TargetPurposePreflight,
			)
			return resolveErr
		})
		if err != nil {
			t.Fatalf("resolve exact managed Recovery credential: %v", err)
		}
		if len(snapshot.NodeRevision) != sha256.Size*2 || len(snapshot.CredentialRevision) != sha256.Size*2 {
			t.Fatalf("invalid revision product: %+v", snapshot)
		}
		return snapshot
	}

	initial := resolve(t)
	permutedPurposes := strings.Join([]string{
		sshutil.PurposeRecoveryReconcile,
		sshutil.PurposeRecoveryCleanup,
		sshutil.PurposeRecoveryResultRead,
		sshutil.PurposeRecoveryVerify,
		sshutil.PurposeRecoveryWrite,
		sshutil.PurposeRecoveryPreflight,
		sshutil.PurposeRecoveryTargetRootRegistration,
	}, ",")
	if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).
		Update("allowed_purposes", permutedPurposes).Error; err != nil {
		t.Fatal(err)
	}
	if permuted := resolve(t); permuted != initial {
		t.Fatalf("equivalent purpose-set order changed authority: before=%+v after=%+v", initial, permuted)
	}
	if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).
		UpdateColumn("expires_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	nonExpiring := resolve(t)
	if nonExpiring.CredentialRevision == initial.CredentialRevision {
		t.Fatal("credential expiry removal did not change credential revision")
	}
	if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).
		Updates(map[string]any{"key_type": sshutil.SSHKeyTypeAuto, "username": "managed-key-metadata"}).Error; err != nil {
		t.Fatal(err)
	}
	compatibleMetadata := resolve(t)
	if compatibleMetadata.CredentialRevision == nonExpiring.CredentialRevision {
		t.Fatal("managed key metadata change did not change credential revision")
	}
	lastUsed := now.Add(10 * time.Minute)
	if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).Update("last_used_at", lastUsed).Error; err != nil {
		t.Fatal(err)
	}
	if afterUse := resolve(t); afterUse != compatibleMetadata {
		t.Fatalf("LastUsedAt changed immutable credential authority: before=%+v after=%+v", compatibleMetadata, afterUse)
	}
	if err := db.Model(&model.Node{}).Where("id = ?", node.ID).Update("host", "recovery-new.invalid").Error; err != nil {
		t.Fatal(err)
	}
	if afterHost := resolve(t); afterHost.NodeRevision == initial.NodeRevision {
		t.Fatal("node host change did not change node revision")
	}
}

func TestManagedRecoveryReconciliationFindingSinkDeduplicatesAndResolvesWithoutRootLeak(t *testing.T) {
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Alert{}, &model.Integration{}); err != nil {
		t.Fatal(err)
	}
	node := model.Node{
		ID: 91, Name: "recovery-alert-node", Host: "alert.invalid", Port: 22,
		Username: "recovery", AuthType: "key", BackupDir: "recovery-alert-node",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	sink := newManagedRecoveryReconciliationFindingSink(
		alerting.NewDispatcher(db, settings.NewService(db), nil),
	)
	rootID := "FAKE_PRIVATE_RECOVERY_ROOT_ID_FOR_TEST_ONLY"
	blocked := recovery.RecoveryReconciliationAlert{
		NodeID: node.ID, RootID: rootID, State: recovery.RecoveryReconciliationBlocked,
	}
	if err := sink.NotifyRecoveryReconciliation(context.Background(), blocked); err != nil {
		t.Fatalf("raise Recovery reconciliation finding: %v", err)
	}
	if err := db.Model(&model.Alert{}).Where("node_id = ?", node.ID).
		UpdateColumn("created_at", time.Now().UTC().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if err := sink.NotifyRecoveryReconciliation(context.Background(), blocked); err != nil {
		t.Fatalf("deduplicate Recovery reconciliation finding: %v", err)
	}
	var alerts []model.Alert
	if err := db.Order("id ASC").Find(&alerts).Error; err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0].Status != "open" || alerts[0].NodeID != node.ID {
		t.Fatalf("Recovery reconciliation alerts=%+v, want one open node alert", alerts)
	}
	serialized := fmt.Sprintf("%+v", alerts[0])
	if strings.Contains(serialized, rootID) || strings.Contains(alerts[0].ErrorCode, rootID) {
		t.Fatalf("Recovery reconciliation alert leaked root identity: %+v", alerts[0])
	}
	clearAlert := blocked
	clearAlert.State = recovery.RecoveryReconciliationClear
	if err := sink.NotifyRecoveryReconciliation(context.Background(), clearAlert); err != nil {
		t.Fatalf("resolve Recovery reconciliation finding: %v", err)
	}
	var resolved model.Alert
	if err := db.First(&resolved, alerts[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if resolved.Status != "resolved" {
		t.Fatalf("Recovery reconciliation alert status=%q, want resolved", resolved.Status)
	}
}

func TestManagedRecoveryNodeRevisionSourceFailsClosedForCredentialDrift(t *testing.T) {
	now := time.Date(2026, 8, 13, 2, 3, 4, 0, time.UTC)
	testCases := []struct {
		name   string
		mutate func(*testing.T, *gorm.DB, model.Node, model.SSHKey)
	}{
		{name: "missing managed key", mutate: func(t *testing.T, db *gorm.DB, node model.Node, _ model.SSHKey) {
			t.Helper()
			if err := db.Model(&model.Node{}).Where("id = ?", node.ID).UpdateColumn("ssh_key_id", nil).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "archived node", mutate: updateManagedRecoveryNodeColumn("archived", true)},
		{name: "password fallback", mutate: updateManagedRecoveryNodeColumn("auth_type", "password")},
		{name: "node private key fallback", mutate: func(t *testing.T, db *gorm.DB, node model.Node, _ model.SSHKey) {
			t.Helper()
			if err := db.Model(&model.Node{}).Where("id = ?", node.ID).
				Updates(map[string]any{"ssh_key_id": nil, "private_key": "inline-private-key"}).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "disabled key", mutate: updateManagedRecoveryKeyColumn("disabled", true)},
		{name: "expired key", mutate: updateManagedRecoveryKeyColumn("expires_at", now)},
		{name: "malformed private key", mutate: updateManagedRecoveryKeyColumn("private_key", "not-a-private-key")},
		{name: "fingerprint mismatch", mutate: updateManagedRecoveryKeyColumn("fingerprint", "SHA256:wrong")},
		{name: "broad purpose scope", mutate: updateManagedRecoveryKeyColumn("allowed_purposes", "")},
		{name: "cross-purpose scope", mutate: updateManagedRecoveryKeyColumn(
			"allowed_purposes", managedRecoveryPurposesForTest()+","+sshutil.PurposeTerminal,
		)},
		{name: "incomplete purpose scope", mutate: updateManagedRecoveryKeyColumn(
			"allowed_purposes", sshutil.PurposeRecoveryPreflight,
		)},
		{name: "broad node scope", mutate: updateManagedRecoveryKeyColumn("allowed_node_ids", "")},
		{name: "wrong node scope", mutate: updateManagedRecoveryKeyColumn("allowed_node_ids", "78")},
		{name: "ambiguous node scope", mutate: updateManagedRecoveryKeyColumn("allowed_node_ids", "77,78")},
		{name: "tag scope", mutate: updateManagedRecoveryKeyColumn("allowed_node_tags", "production")},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			db, node, key := seedManagedRecoveryCredentialAuthority(t, now)
			testCase.mutate(t, db, node, key)
			source := newManagedRecoveryNodeRevisionSource(func() time.Time { return now })
			err := db.Transaction(func(tx *gorm.DB) error {
				_, resolveErr := source.ResolveRecoveryNodeRevisionsTx(
					context.Background(), tx, node.ID, recovery.TargetPurposePreflight,
				)
				return resolveErr
			})
			if !errors.Is(err, recovery.ErrRecoveryTargetUnavailable) {
				t.Fatalf("credential drift error=%v, want fail-closed target unavailable", err)
			}
		})
	}
}

func seedManagedRecoveryCredentialAuthority(
	t *testing.T,
	now time.Time,
) (*gorm.DB, model.Node, model.SSHKey) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RUNTIME_RECOVERY_CREDENTIAL_AUTHORITY_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.SSHKey{}); err != nil {
		t.Fatal(err)
	}
	privateKey := managedRecoveryPrivateKeyForTest(t)
	preparedKey, keyType, err := sshutil.ValidateAndPreparePrivateKey(privateKey, sshutil.SSHKeyTypeRSA)
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := now.Add(time.Hour)
	key := model.SSHKey{
		ID: 31, Name: "recovery-authority-key", Username: "recovery", KeyType: keyType,
		PrivateKey: preparedKey, Fingerprint: managedRecoveryPrivateKeyFingerprintForTest(preparedKey),
		ExpiresAt: &expiresAt, AllowedPurposes: managedRecoveryPurposesForTest(),
		AllowedNodeIDs: "77",
	}
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	node := model.Node{
		ID: 77, Name: "recovery-node", Host: "recovery.invalid", Port: 22,
		Username: "recovery", AuthType: "key", SSHKeyID: &key.ID, BackupDir: "recovery-node",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	return db, node, key
}

func managedRecoveryPurposesForTest() string {
	return strings.Join([]string{
		sshutil.PurposeRecoveryPreflight,
		sshutil.PurposeRecoveryWrite,
		sshutil.PurposeRecoveryVerify,
		sshutil.PurposeRecoveryResultRead,
		sshutil.PurposeRecoveryCleanup,
		sshutil.PurposeRecoveryReconcile,
		sshutil.PurposeRecoveryTargetRootRegistration,
	}, ",")
}

func managedRecoveryPrivateKeyForTest(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if len(encoded) == 0 {
		t.Fatal("encode Recovery credential test key")
	}
	return string(encoded)
}

func managedRecoveryPrivateKeyFingerprintForTest(privateKey string) string {
	digest := sha256.Sum256([]byte(privateKey))
	return "SHA256:" + base64.StdEncoding.EncodeToString(digest[:])
}

func updateManagedRecoveryNodeColumn(column string, value any) func(*testing.T, *gorm.DB, model.Node, model.SSHKey) {
	return func(t *testing.T, db *gorm.DB, node model.Node, _ model.SSHKey) {
		t.Helper()
		if err := db.Model(&model.Node{}).Where("id = ?", node.ID).UpdateColumn(column, value).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func updateManagedRecoveryKeyColumn(column string, value any) func(*testing.T, *gorm.DB, model.Node, model.SSHKey) {
	return func(t *testing.T, db *gorm.DB, _ model.Node, key model.SSHKey) {
		t.Helper()
		if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).UpdateColumn(column, value).Error; err != nil {
			t.Fatal(err)
		}
	}
}

type managedRecoveryNodeAdmissionFake struct{}

func (managedRecoveryNodeAdmissionFake) AdmitRecoveryTx(context.Context, *gorm.DB, uint) error {
	return nil
}

type managedRecoveryNodeRevisionSourceFake struct{}

func (managedRecoveryNodeRevisionSourceFake) ResolveRecoveryNodeRevisionsTx(
	context.Context,
	*gorm.DB,
	uint,
	recovery.TargetPurpose,
) (recovery.RecoveryNodeRevisionSnapshot, error) {
	return recovery.RecoveryNodeRevisionSnapshot{NodeRevision: "node-revision", CredentialRevision: "credential-revision"}, nil
}

type managedRecoveryPreflightEvidenceAuthorityFake struct{}

func (managedRecoveryPreflightEvidenceAuthorityFake) ObserveRecoveryPreflightEvidence(
	context.Context,
	recovery.PreflightExternalEvidenceRequest,
) (recovery.PreflightExternalEvidenceObservation, error) {
	return recovery.PreflightExternalEvidenceObservation{}, nil
}

type managedRecoveryPlanSecurityAuthorityFake struct{}

func (managedRecoveryPlanSecurityAuthorityFake) ObserveRecoveryPlanSecurity(
	context.Context,
	recovery.RecoveryPlanSecurityRequest,
) (recovery.RecoveryPlanSecurityEvidence, error) {
	return recovery.RecoveryPlanSecurityEvidence{}, recovery.ErrRecoveryPlanUnavailable
}

type managedRecoveryAuthorityRevalidatorFake struct{}

func (managedRecoveryAuthorityRevalidatorFake) ObserveRecoveryAuthority(
	context.Context,
	recovery.RecoveryAuthorityBinding,
) (recovery.RecoveryAuthorityObservation, error) {
	return recovery.RecoveryAuthorityObservation{}, nil
}

func (managedRecoveryAuthorityRevalidatorFake) RevalidateRecoveryAuthorityTx(
	context.Context,
	*gorm.DB,
	recovery.RecoveryAuthorityBinding,
	recovery.RecoveryAuthorityObservation,
) error {
	return nil
}

type managedRecoveryReconciliationRevisionSourceFake struct{}

func (managedRecoveryReconciliationRevisionSourceFake) ResolveRecoveryReconciliationRevisionsTx(
	context.Context,
	*gorm.DB,
	uint,
	string,
) (recovery.RecoveryReconciliationRevisionSnapshot, error) {
	return recovery.RecoveryReconciliationRevisionSnapshot{
		NodeRevision: "node-revision", CredentialRevision: "credential-revision", RootRevision: "root-revision",
	}, nil
}

type managedRecoveryReconciliationFindingSinkFake struct{}

func (managedRecoveryReconciliationFindingSinkFake) NotifyRecoveryReconciliation(
	context.Context,
	recovery.RecoveryReconciliationAlert,
) error {
	return nil
}

type managedRecoveryAuditWriterFake struct{}

func (managedRecoveryAuditWriterFake) Write(
	context.Context,
	backupasset.AuditEventInput,
) (model.BackupAssetAuditEvent, error) {
	return model.BackupAssetAuditEvent{}, nil
}

type managedRecoveryContentLifecycleFake struct{}

func (managedRecoveryContentLifecycleFake) RevokeRecoveryResultGrantsTx(
	context.Context,
	*gorm.DB,
	string,
	string,
	time.Time,
) error {
	return nil
}

func (managedRecoveryContentLifecycleFake) CancelRecoveryResultReads(string) error { return nil }

func (managedRecoveryContentLifecycleFake) DrainRecoveryResult(context.Context, string) error {
	return nil
}

type managedRecoveryDeliveryLifecycleRecorder struct {
	mu             sync.Mutex
	revoked        []string
	canceled       []string
	drained        []string
	drainDeadlines []time.Time
}

func (recorder *managedRecoveryDeliveryLifecycleRecorder) RevokeRecoveryResultGrantsTx(
	_ context.Context,
	_ *gorm.DB,
	jobID string,
	reason string,
	_ time.Time,
) error {
	if reason != content.RecoveryResultCleanupReason {
		return errors.New("unexpected Recovery delivery revocation reason")
	}
	recorder.mu.Lock()
	recorder.revoked = append(recorder.revoked, jobID)
	recorder.mu.Unlock()
	return nil
}

func (recorder *managedRecoveryDeliveryLifecycleRecorder) CancelRecoveryResultReads(jobID string) error {
	recorder.mu.Lock()
	recorder.canceled = append(recorder.canceled, jobID)
	recorder.mu.Unlock()
	return nil
}

func (recorder *managedRecoveryDeliveryLifecycleRecorder) DrainRecoveryResult(
	ctx context.Context,
	jobID string,
) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("Recovery result drain context is unbounded")
	}
	recorder.mu.Lock()
	recorder.drained = append(recorder.drained, jobID)
	recorder.drainDeadlines = append(recorder.drainDeadlines, deadline)
	recorder.mu.Unlock()
	return nil
}

func (recorder *managedRecoveryDeliveryLifecycleRecorder) callCount() int {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return len(recorder.revoked) + len(recorder.canceled) + len(recorder.drained)
}

func (recorder *managedRecoveryDeliveryLifecycleRecorder) revokedSnapshot() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.revoked...)
}

func (recorder *managedRecoveryDeliveryLifecycleRecorder) canceledSnapshot() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.canceled...)
}

func (recorder *managedRecoveryDeliveryLifecycleRecorder) drainedSnapshot() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.drained...)
}

func (recorder *managedRecoveryDeliveryLifecycleRecorder) drainDeadlinesSnapshot() []time.Time {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]time.Time(nil), recorder.drainDeadlines...)
}

type managedRecoveryRsyncTargetWriterFake struct{}

func (*managedRecoveryRsyncTargetWriterFake) WriteDeclaredRegular(context.Context, provider.RsyncTargetWriteCall) error {
	return nil
}

type managedRecoveryRsyncRestoreRunnerFake struct{}

func (*managedRecoveryRsyncRestoreRunnerFake) Preflight(
	context.Context,
	provider.RsyncRestorePreflightCall,
) (provider.RsyncRestoreRunnerEvidence, error) {
	return provider.RsyncRestoreRunnerEvidence{}, nil
}

func (*managedRecoveryRsyncRestoreRunnerFake) Execute(
	context.Context,
	provider.RsyncRestoreExecuteCall,
) (provider.RsyncRestoreRunnerResult, error) {
	return provider.RsyncRestoreRunnerResult{}, nil
}

func (*managedRecoveryRsyncRestoreRunnerFake) Verify(
	context.Context,
	provider.RsyncRestoreVerifyCall,
) (provider.RsyncRestoreRunnerEvidence, error) {
	return provider.RsyncRestoreRunnerEvidence{}, nil
}

func (*managedRecoveryRsyncRestoreRunnerFake) Reconcile(
	context.Context,
	provider.RsyncRestoreReconcileCall,
) (provider.RsyncRestoreRunnerEvidence, error) {
	return provider.RsyncRestoreRunnerEvidence{}, nil
}

func TestManagedRecoveryRuntimeStopAcceptingIsStickyAndShutdownIsOrdered(t *testing.T) {
	var events []string
	graph := &managedRecoveryGraph{
		reconcileMetadata: func(context.Context) error { return nil },
		stopClaims:        func() { events = append(events, "stop_claims") },
		cancelJoinAttempts: func(context.Context) error {
			events = append(events, "join_attempts")
			return nil
		},
		fenceOwnership: func(context.Context) error {
			events = append(events, "fence_ownership")
			return nil
		},
		revokeDrainDelivery: func(context.Context) error {
			events = append(events, "drain_delivery")
			return nil
		},
		shutdownLifecycle: func(context.Context) error {
			events = append(events, "stop_lifecycle")
			return nil
		},
	}
	manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
		Build: func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error) { return graph, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager.StopAccepting()
	manager.StopAccepting()
	if manager.publication.current() != nil {
		t.Fatal("StopAccepting did not unpublish Recovery facades")
	}
	if err := manager.Startup(context.Background()); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("Startup after StopAccepting error=%v, want invalid state", err)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"stop_claims", "join_attempts", "fence_ownership", "drain_delivery", "stop_lifecycle"}
	if len(events) != len(want) {
		t.Fatalf("shutdown events=%v, want %v", events, want)
	}
	for index := range want {
		if events[index] != want[index] {
			t.Fatalf("shutdown events=%v, want %v", events, want)
		}
	}
}

func TestManagedRecoveryRuntimeShutdownKeepsLifecycleRunningThroughDeliveryDrain(t *testing.T) {
	var lifecycleStopped atomic.Bool
	lifecycleStarted := make(chan struct{})
	graph := &managedRecoveryGraph{
		reconcileMetadata: func(context.Context) error { return nil },
		stopClaims:        func() {},
		cancelJoinAttempts: func(context.Context) error {
			if lifecycleStopped.Load() {
				return errors.New("Recovery lifecycle stopped before attempts joined")
			}
			return nil
		},
		fenceOwnership: func(context.Context) error {
			if lifecycleStopped.Load() {
				return errors.New("Recovery lifecycle stopped before ownership fencing")
			}
			return nil
		},
		revokeDrainDelivery: func(context.Context) error {
			if lifecycleStopped.Load() {
				return errors.New("Recovery lifecycle stopped before delivery drain")
			}
			return nil
		},
		run: func(ctx context.Context) {
			close(lifecycleStarted)
			<-ctx.Done()
			lifecycleStopped.Store(true)
		},
	}
	graph.shutdownLifecycle = graph.stopRun
	manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
		Build: func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error) { return graph, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	runDone := make(chan struct{})
	go func() {
		manager.Run(context.Background())
		close(runDone)
	}()
	select {
	case <-lifecycleStarted:
	case <-time.After(time.Second):
		t.Fatal("Recovery lifecycle did not start")
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !lifecycleStopped.Load() {
		t.Fatal("Recovery lifecycle remained running after shutdown")
	}
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("Recovery runtime owner did not join after ordered shutdown")
	}
}

func TestManagedRecoveryRuntimeRunsReceiptMaintenanceAfterReconciliationWhileDisabled(t *testing.T) {
	var mu sync.Mutex
	events := make([]string, 0, 4)
	receipt := newManagedRecoveryReceiptOwnerFake(&mu, &events)
	graphRun := make(chan struct{})
	manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
		Build: func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
			return &managedRecoveryGraph{
				admissionEnabled: false,
				reconcileMetadata: func(context.Context) error {
					mu.Lock()
					events = append(events, "reconcile")
					mu.Unlock()
					return nil
				},
				run: func(ctx context.Context) {
					close(graphRun)
					<-ctx.Done()
				},
			}, nil
		},
		ReceiptOwner: receipt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.StartupWithConfig(context.Background(), backupasset.RecoveryConfig{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, release, ok := manager.publication.acquireAdmission(); ok {
		release()
		t.Fatal("disabled Recovery graph admitted a new mutation facade call")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		manager.Run(ctx)
		close(done)
	}()
	select {
	case <-receipt.started:
	case <-time.After(time.Second):
		t.Fatal("disabled Recovery runtime did not start receipt maintenance")
	}
	select {
	case <-graphRun:
	case <-time.After(time.Second):
		t.Fatal("disabled Recovery runtime did not keep lifecycle graph running")
	}
	mu.Lock()
	got := append([]string(nil), events...)
	mu.Unlock()
	if len(got) < 2 || got[0] != "reconcile" || got[1] != "receipt_run" {
		t.Fatalf("runtime events=%v, want reconciliation before receipt owner", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("managed Recovery Run did not join")
	}
}

func TestManagedRecoveryRuntimeConcurrentRunHasOneOwnerAndShutdownJoinsIt(t *testing.T) {
	var starts atomic.Int32
	runStarted := make(chan struct{})
	runRelease := make(chan struct{})
	manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
		Build: func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
			return &managedRecoveryGraph{
				reconcileMetadata: func(context.Context) error { return nil },
				run: func(ctx context.Context) {
					if starts.Add(1) == 1 {
						close(runStarted)
					}
					select {
					case <-ctx.Done():
					case <-runRelease:
					}
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.StartupWithConfig(context.Background(), backupasset.RecoveryConfig{}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{}, 2)
	for index := 0; index < 2; index++ {
		go func() {
			manager.Run(context.Background())
			done <- struct{}{}
		}()
	}
	select {
	case <-runStarted:
	case <-time.After(time.Second):
		t.Fatal("Recovery runtime owner did not start")
	}
	time.Sleep(20 * time.Millisecond)
	if got := starts.Load(); got != 1 {
		close(runRelease)
		t.Fatalf("concurrent Run started graph %d times, want one owner", got)
	}
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(context.Background()) }()
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(runRelease)
		t.Fatal("Shutdown did not cancel and join the managed Run owner")
	}
	for index := 0; index < 2; index++ {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("concurrent Run caller did not return after Shutdown")
		}
	}
}

func TestManagedRecoveryRuntimeRunTransitionStopAndShutdownRace(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
			Build: func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
				return &managedRecoveryGraph{
					reconcileMetadata: func(context.Context) error { return nil },
					run: func(ctx context.Context) {
						<-ctx.Done()
					},
				}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.StartupWithConfig(context.Background(), backupasset.RecoveryConfig{Enabled: true}); err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		results := make(chan error, 4)
		go func() {
			<-start
			manager.Run(context.Background())
			results <- nil
		}()
		go func() {
			<-start
			results <- manager.TransitionSettings(
				context.Background(), backupasset.RecoveryConfig{Enabled: false}, func() error { return nil },
			)
		}()
		go func() {
			<-start
			manager.StopAccepting()
			results <- nil
		}()
		go func() {
			<-start
			results <- manager.Shutdown(context.Background())
		}()
		close(start)

		for completed := 0; completed < 4; completed++ {
			select {
			case result := <-results:
				if result != nil && !errors.Is(result, backupasset.ErrInvalidState) {
					t.Fatalf("iteration %d returned unexpected error: %v", iteration, result)
				}
			case <-time.After(time.Second):
				t.Fatalf("iteration %d did not join all lifecycle operations", iteration)
			}
		}
		if current := manager.publication.current(); current != nil {
			t.Fatalf("iteration %d left Recovery graph published", iteration)
		}
	}
}

func TestManagedRecoveryRuntimeTransitionCancelsAndJoinsOldGraphBeforePersistence(t *testing.T) {
	oldRunStarted := make(chan struct{})
	oldRunDone := make(chan struct{})
	builds := 0
	manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
		Build: func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
			builds++
			graph := &managedRecoveryGraph{reconcileMetadata: func(context.Context) error { return nil }}
			if builds == 1 {
				graph.run = func(ctx context.Context) {
					close(oldRunStarted)
					<-ctx.Done()
					close(oldRunDone)
				}
			}
			return graph, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.StartupWithConfig(context.Background(), backupasset.RecoveryConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	go manager.Run(context.Background())
	select {
	case <-oldRunStarted:
	case <-time.After(time.Second):
		t.Fatal("old Recovery graph run loop did not start")
	}

	transitionDone := make(chan error, 1)
	go func() {
		transitionDone <- manager.TransitionSettings(
			context.Background(), backupasset.RecoveryConfig{Enabled: false}, func() error {
				select {
				case <-oldRunDone:
					return nil
				default:
					return errors.New("old graph was not joined before persistence")
				}
			},
		)
	}()
	select {
	case err := <-transitionDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Recovery settings transition could not cancel and join the old graph")
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagedRecoveryRuntimeDowngradeReadinessMatrixIsDisabledStickyAndNeverRunsDown(t *testing.T) {
	tests := []struct {
		name              string
		snapshot          managedRecoveryDowngradeSnapshot
		reconciliation    recovery.RecoveryReconciliationResult
		wantState         RecoveryDowngradeReadinessState
		wantBlockers      RecoveryDowngradeBlockers
		wantReconciliates int
	}{
		{
			name:      "permanent latch dominates every cleared row",
			snapshot:  managedRecoveryDowngradeSnapshot{UseLatch: true},
			wantState: RecoveryDowngradeForwardFixOnly,
		},
		{
			name:     "durable job remains blocked",
			snapshot: managedRecoveryDowngradeSnapshot{Blockers: RecoveryDowngradeBlockers{Jobs: 1}},
			reconciliation: recovery.RecoveryReconciliationResult{
				State: recovery.RecoveryReconciliationClear, Complete: true,
			},
			wantState: RecoveryDowngradeBlocked, wantReconciliates: 1,
		},
		{
			name: "remote reconciliation backlog remains blocked",
			reconciliation: recovery.RecoveryReconciliationResult{
				State: recovery.RecoveryReconciliationBlocked, Complete: false,
				Counts: recovery.RecoveryReconciliationCounts{ScanIncomplete: 1},
			},
			wantState:         RecoveryDowngradeBlocked,
			wantBlockers:      RecoveryDowngradeBlockers{ReconciliationBacklog: 1},
			wantReconciliates: 1,
		},
		{
			name: "empty pristine state is ready",
			reconciliation: recovery.RecoveryReconciliationResult{
				State: recovery.RecoveryReconciliationClear, Complete: true,
			},
			wantState: RecoveryDowngradePristineAllowed, wantReconciliates: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inspector := &managedRecoveryDowngradeInspectorFake{snapshot: test.snapshot}
			reconciler := &managedRecoveryDowngradeReconcilerFake{result: test.reconciliation}
			manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
				Build: func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
					return &managedRecoveryGraph{
						reconcileMetadata:   func(context.Context) error { return nil },
						downgradeReconciler: reconciler,
					}, nil
				},
				DowngradeInspector: inspector,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.StartupWithConfig(context.Background(), backupasset.RecoveryConfig{Enabled: false}); err != nil {
				t.Fatal(err)
			}
			result, err := manager.DowngradeReadiness(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if result.State != test.wantState || result.AdmissionGeneration == "" {
				t.Fatalf("readiness=%+v, want state=%s with generation", result, test.wantState)
			}
			wantBlockers := test.wantBlockers
			if wantBlockers == (RecoveryDowngradeBlockers{}) {
				wantBlockers = test.snapshot.Blockers
			}
			if result.Blockers != wantBlockers && !test.snapshot.UseLatch {
				t.Fatalf("blockers=%+v, want %+v", result.Blockers, wantBlockers)
			}
			if got := reconciler.calls.Load(); got != int32(test.wantReconciliates) {
				t.Fatalf("reconciliation calls=%d, want %d", got, test.wantReconciliates)
			}
			persistCalls := 0
			err = manager.TransitionSettings(context.Background(), backupasset.RecoveryConfig{Enabled: true}, func() error {
				persistCalls++
				return nil
			})
			if !errors.Is(err, backupasset.ErrInvalidState) || persistCalls != 0 {
				t.Fatalf("sticky downgrade fence transition err=%v persist=%d", err, persistCalls)
			}
			if err := manager.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManagedRecoveryRuntimeDowngradeReadinessRejectsEnabledFeatureWithoutInstallingFence(t *testing.T) {
	manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
		Build: func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
			return &managedRecoveryGraph{reconcileMetadata: func(context.Context) error { return nil }}, nil
		},
		DowngradeInspector: &managedRecoveryDowngradeInspectorFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.StartupWithConfig(context.Background(), backupasset.RecoveryConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.DowngradeReadiness(context.Background()); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("enabled downgrade readiness error=%v", err)
	}
	persistCalls := 0
	if err := manager.TransitionSettings(context.Background(), backupasset.RecoveryConfig{Enabled: false}, func() error {
		persistCalls++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if persistCalls != 1 {
		t.Fatalf("enabled rejection installed sticky fence; persist calls=%d", persistCalls)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagedRecoveryRuntimeDowngradeReadinessFailureStillFencesReenable(t *testing.T) {
	inspector := &managedRecoveryDowngradeInspectorFake{err: errors.New("snapshot unavailable")}
	manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
		Build: func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
			return &managedRecoveryGraph{reconcileMetadata: func(context.Context) error { return nil }}, nil
		},
		DowngradeInspector: inspector,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.StartupWithConfig(context.Background(), backupasset.RecoveryConfig{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.DowngradeReadiness(context.Background()); err == nil {
		t.Fatal("failed downgrade snapshot unexpectedly succeeded")
	}
	persistCalls := 0
	err = manager.TransitionSettings(context.Background(), backupasset.RecoveryConfig{Enabled: true}, func() error {
		persistCalls++
		return nil
	})
	if !errors.Is(err, backupasset.ErrInvalidState) || persistCalls != 0 {
		t.Fatalf("failed readiness did not retain sticky fence: err=%v persist=%d", err, persistCalls)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagedRecoveryDowngradeDBInspectorMatchesPairedDownGuard(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	for _, statement := range []string{
		`CREATE TABLE backup_asset_recovery_plans (id TEXT)`,
		`CREATE TABLE backup_asset_recovery_plan_items (id TEXT)`,
		`CREATE TABLE backup_asset_recovery_preflights (id TEXT)`,
		`CREATE TABLE backup_asset_recovery_grants (id TEXT)`,
		`CREATE TABLE backup_asset_recovery_jobs (id TEXT)`,
		`CREATE TABLE backup_asset_recovery_job_items (id TEXT)`,
		`CREATE TABLE backup_asset_recovery_attempts (id TEXT)`,
		`CREATE TABLE backup_asset_recovery_checkpoints (id TEXT)`,
		`CREATE TABLE backup_asset_recovery_evidence (id TEXT, kind TEXT, scheduler_scope TEXT)`,
		`CREATE TABLE backup_asset_recovery_result_sets (id TEXT)`,
		`CREATE TABLE backup_asset_recovery_results (id TEXT)`,
		`CREATE TABLE backup_asset_recovery_node_leases (id TEXT)`,
		`CREATE TABLE recovery_point_leases (id TEXT, holder_type TEXT, status TEXT, owner_id TEXT)`,
		`CREATE TABLE backup_asset_delivery_grants (id TEXT, resource_kind TEXT)`,
		`CREATE TABLE backup_asset_delivery_requests (id TEXT, grant_id TEXT, state TEXT)`,
		`CREATE TABLE backup_asset_delivery_usage (scope_kind TEXT)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec(`INSERT INTO backup_asset_recovery_evidence(id, kind, scheduler_scope) VALUES
		(?, 'scheduler_state', 'claim'), (?, 'scheduler_state', 'takeover')`,
		"0000000000000000000000000000006a", "0000000000000000000000000000006b").Error; err != nil {
		t.Fatal(err)
	}
	inspector, err := newManagedRecoveryDowngradeDBInspector(db)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := inspector.SnapshotRecoveryDowngradeBlockers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if baseline.UseLatch || baseline.Blockers.any() {
		t.Fatalf("scheduler-only snapshot=%+v, want pristine", baseline)
	}

	statements := []string{
		`INSERT INTO backup_asset_recovery_plans VALUES ('plan')`,
		`INSERT INTO backup_asset_recovery_plan_items VALUES ('plan-item')`,
		`INSERT INTO backup_asset_recovery_preflights VALUES ('preflight')`,
		`INSERT INTO backup_asset_recovery_grants VALUES ('grant')`,
		`INSERT INTO backup_asset_recovery_jobs VALUES ('job')`,
		`INSERT INTO backup_asset_recovery_job_items VALUES ('job-item')`,
		`INSERT INTO backup_asset_recovery_attempts VALUES ('attempt')`,
		`INSERT INTO backup_asset_recovery_checkpoints VALUES ('checkpoint')`,
		`INSERT INTO backup_asset_recovery_evidence VALUES ('ordinary', 'verification', '')`,
		`INSERT INTO backup_asset_recovery_result_sets VALUES ('result-set')`,
		`INSERT INTO backup_asset_recovery_results VALUES ('result')`,
		`INSERT INTO backup_asset_recovery_node_leases VALUES ('node-lease')`,
		`INSERT INTO recovery_point_leases VALUES ('source-lease', 'recovery_job', 'active', 'job')`,
		`INSERT INTO recovery_point_leases VALUES ('content-lease', 'content_session', 'released', 'content-grant')`,
		`INSERT INTO recovery_point_leases VALUES ('asset-content-lease', 'content_session', 'active', 'asset-content-grant')`,
		`INSERT INTO backup_asset_delivery_grants VALUES ('content-grant', 'recovery_result')`,
		`INSERT INTO backup_asset_delivery_grants VALUES ('asset-content-grant', 'backup_asset')`,
		`INSERT INTO backup_asset_delivery_requests VALUES ('content-request', 'content-grant', 'streaming')`,
		`INSERT INTO backup_asset_delivery_usage VALUES ('user')`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := inspector.SnapshotRecoveryDowngradeBlockers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := RecoveryDowngradeBlockers{
		Jobs: 1, Authorities: 1, SourceLeases: 1, NodeLeases: 1, Attempts: 1,
		ResultSets: 1, Results: 1, ContentGrants: 1, ContentRequests: 1, ContentStreams: 1,
		ContentLeases: 1, OtherRecoveryRows: 6,
	}
	if snapshot.UseLatch || snapshot.Blockers != want {
		t.Fatalf("downgrade snapshot=%+v, want blockers=%+v", snapshot, want)
	}
	if err := db.Exec(`INSERT INTO backup_asset_recovery_evidence VALUES ('schema_use_latch', 'schema_use_latch', '')`).Error; err != nil {
		t.Fatal(err)
	}
	snapshot, err = inspector.SnapshotRecoveryDowngradeBlockers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.UseLatch {
		t.Fatal("permanent Recovery schema-use latch was not detected")
	}
}

func TestManagedRecoveryRuntimeTransitionSettingsDrainsPersistsInstallsAndRestoresPriorGraph(t *testing.T) {
	var mu sync.Mutex
	events := make([]string, 0, 24)
	failBuildEnabled := false
	build := func(_ context.Context, config backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
		mu.Lock()
		events = append(events, fmt.Sprintf("build:%t", config.Enabled))
		fail := config.Enabled && failBuildEnabled
		if fail {
			failBuildEnabled = false
		}
		mu.Unlock()
		if fail {
			return nil, errors.New("injected install failure")
		}
		label := fmt.Sprintf("%t", config.Enabled)
		return &managedRecoveryGraph{
			admissionEnabled: config.Enabled,
			reconcileMetadata: func(context.Context) error {
				mu.Lock()
				events = append(events, "reconcile:"+label)
				mu.Unlock()
				return nil
			},
			stopClaims: func() {
				mu.Lock()
				events = append(events, "stop_claims:"+label)
				mu.Unlock()
			},
			cancelJoinAttempts: func(context.Context) error {
				mu.Lock()
				events = append(events, "join_attempts:"+label)
				mu.Unlock()
				return nil
			},
			fenceOwnership: func(context.Context) error {
				mu.Lock()
				events = append(events, "fence:"+label)
				mu.Unlock()
				return nil
			},
			revokeDrainDelivery: func(context.Context) error {
				mu.Lock()
				events = append(events, "drain_delivery:"+label)
				mu.Unlock()
				return nil
			},
			shutdownLifecycle: func(context.Context) error {
				mu.Lock()
				events = append(events, "shutdown:"+label)
				mu.Unlock()
				return nil
			},
		}, nil
	}
	manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{Build: build})
	if err != nil {
		t.Fatal(err)
	}
	disabled := backupasset.RecoveryConfig{Enabled: false}
	enabled := backupasset.RecoveryConfig{Enabled: true}
	if err := manager.StartupWithConfig(context.Background(), disabled); err != nil {
		t.Fatal(err)
	}
	if err := manager.TransitionSettings(context.Background(), enabled, func() error {
		mu.Lock()
		events = append(events, "persist:true")
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if graph := manager.publication.current(); graph == nil || !graph.admissionEnabled {
		t.Fatal("enabled Recovery graph was not installed")
	}

	failBuildEnabled = true
	if err := manager.TransitionSettings(context.Background(), enabled, func() error {
		mu.Lock()
		events = append(events, "persist:retry")
		mu.Unlock()
		return nil
	}); err == nil {
		t.Fatal("install failure unexpectedly succeeded")
	}
	if graph := manager.publication.current(); graph == nil || !graph.admissionEnabled {
		t.Fatal("install failure did not rebuild the prior enabled graph")
	}

	mu.Lock()
	got := append([]string(nil), events...)
	mu.Unlock()
	wantOrdered := []string{
		"stop_claims:false", "join_attempts:false", "fence:false", "drain_delivery:false", "shutdown:false",
		"persist:true", "build:true", "reconcile:true",
	}
	if !containsManagedRecoveryOrderedEvents(got, wantOrdered) {
		t.Fatalf("transition events=%v, want ordered subsequence %v", got, wantOrdered)
	}
}

func TestRecoveryTransitionValidateDrainAndPersistFailuresRestoreBeforeAdmission(t *testing.T) {
	for _, stage := range []string{"validate", "drain", "persist"} {
		t.Run(stage, func(t *testing.T) {
			enabled := backupasset.RecoveryConfig{Enabled: true}
			disabled := backupasset.RecoveryConfig{Enabled: false}
			persisted := enabled
			validateCalls, buildCalls, installCalls, restoreCalls := 0, 0, 0, 0
			events := make([]string, 0, 16)
			manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
				Validate: func(_ context.Context, config backupasset.RecoveryConfig) error {
					validateCalls++
					events = append(events, fmt.Sprintf("validate:%t", config.Enabled))
					if stage == "validate" && validateCalls == 2 {
						return errors.New("injected validate failure")
					}
					return nil
				},
				Build: func(_ context.Context, config backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
					buildCalls++
					label := fmt.Sprintf("%t", config.Enabled)
					events = append(events, "build:"+label)
					return &managedRecoveryGraph{
						admissionEnabled: config.Enabled,
						reconcileMetadata: func(context.Context) error {
							events = append(events, "reconcile:"+label)
							return nil
						},
						shutdownLifecycle: func(context.Context) error {
							events = append(events, "drain:"+label)
							if stage == "drain" && label == "true" {
								return errors.New("injected drain failure")
							}
							return nil
						},
					}, nil
				},
				Install: func(publication *managedRecoveryPublication, graph *managedRecoveryGraph) error {
					installCalls++
					if installCalls > 1 && persisted != enabled {
						return errors.New("prior admission reopened before persisted config restoration")
					}
					events = append(events, fmt.Sprintf("install:%t", graph.admissionEnabled))
					return publication.publish(graph)
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.StartupWithConfig(context.Background(), enabled); err != nil {
				t.Fatal(err)
			}
			err = manager.TransitionSettingsWithRestore(
				context.Background(), disabled,
				func() error {
					events = append(events, "persist:new")
					persisted = disabled
					if stage == "persist" {
						return errors.New("injected persist failure")
					}
					return nil
				},
				func() error {
					restoreCalls++
					events = append(events, "persist:restore")
					persisted = enabled
					return nil
				},
			)
			if err == nil {
				t.Fatalf("injected %s failure unexpectedly succeeded", stage)
			}
			current := manager.publication.current()
			if stage == "drain" {
				if current != nil || manager.graph != nil || !manager.downgradeFenced || persisted != enabled {
					t.Fatalf("drain failure persisted=%+v graph=%+v runtimeGraph=%p fenced=%t",
						persisted, current, manager.graph, manager.downgradeFenced)
				}
				if !errors.Is(err, backupasset.ErrInvalidState) || strings.Contains(err.Error(), "injected drain failure") {
					t.Fatalf("drain failure error=%v, want sanitized closed invalid state", err)
				}
			} else if current == nil || !current.admissionEnabled || manager.config != enabled || persisted != enabled {
				t.Fatalf("%s failure restored persisted=%+v config=%+v graph=%+v", stage, persisted, manager.config, current)
			}
			switch stage {
			case "validate":
				if buildCalls != 1 || installCalls != 1 || restoreCalls != 0 {
					t.Fatalf("validate failure builds=%d installs=%d restores=%d", buildCalls, installCalls, restoreCalls)
				}
			case "drain":
				if buildCalls != 1 || installCalls != 1 || restoreCalls != 0 {
					t.Fatalf("drain failure builds=%d installs=%d restores=%d", buildCalls, installCalls, restoreCalls)
				}
			case "persist":
				if buildCalls != 2 || installCalls != 2 || restoreCalls != 1 ||
					!containsManagedRecoveryOrderedEvents(events, []string{"persist:new", "persist:restore", "build:true", "install:true"}) {
					t.Fatalf("persist failure events=%v builds=%d installs=%d restores=%d",
						events, buildCalls, installCalls, restoreCalls)
				}
			}
		})
	}
}

func TestRecoveryTransitionInstallFailureRestoresPersistedConfigBeforePriorGraph(t *testing.T) {
	enabled := backupasset.RecoveryConfig{Enabled: true}
	disabled := backupasset.RecoveryConfig{Enabled: false}
	persisted := enabled
	builds := 0
	installs := 0
	events := make([]string, 0, 12)
	manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
		Build: func(_ context.Context, config backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
			builds++
			events = append(events, fmt.Sprintf("build:%t", config.Enabled))
			return &managedRecoveryGraph{
				admissionEnabled: config.Enabled,
				reconcileMetadata: func(context.Context) error {
					events = append(events, fmt.Sprintf("reconcile:%t", config.Enabled))
					return nil
				},
			}, nil
		},
		Install: func(publication *managedRecoveryPublication, graph *managedRecoveryGraph) error {
			installs++
			if installs == 2 {
				events = append(events, "install:failed")
				return errors.New("injected install failure")
			}
			if installs == 3 && persisted != enabled {
				return errors.New("prior graph reopened before persisted config restoration")
			}
			events = append(events, fmt.Sprintf("install:%t", graph.admissionEnabled))
			return publication.publish(graph)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.StartupWithConfig(context.Background(), enabled); err != nil {
		t.Fatal(err)
	}
	err = manager.TransitionSettingsWithRestore(
		context.Background(), disabled,
		func() error {
			events = append(events, "persist:new")
			persisted = disabled
			return nil
		},
		func() error {
			events = append(events, "persist:restore")
			persisted = enabled
			return nil
		},
	)
	if err == nil {
		t.Fatal("injected install failure unexpectedly succeeded")
	}
	current := manager.publication.current()
	if persisted != enabled || current == nil || !current.admissionEnabled || manager.config != enabled {
		t.Fatalf("restored persisted=%+v graph=%+v config=%+v", persisted, current, manager.config)
	}
	wantOrder := []string{
		"persist:new", "build:false", "reconcile:false", "install:failed",
		"persist:restore", "build:true", "reconcile:true", "install:true",
	}
	if !containsManagedRecoveryOrderedEvents(events, wantOrder) {
		t.Fatalf("transition events=%v, want ordered restoration %v", events, wantOrder)
	}
	if builds != 3 || installs != 3 {
		t.Fatalf("builds=%d installs=%d, want startup/candidate/restoration exactly once", builds, installs)
	}
}

func TestRecoveryTransitionNonJoiningOwnerLeavesStickyFenceWithoutRestoration(t *testing.T) {
	releaseOwner := make(chan struct{})
	ownerStarted := make(chan struct{})
	builds, installs, persists, restores := 0, 0, 0, 0
	manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
		Build: func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
			builds++
			return &managedRecoveryGraph{
				reconcileMetadata: func(context.Context) error { return nil },
				run: func(context.Context) {
					close(ownerStarted)
					<-releaseOwner
				},
			}, nil
		},
		Install: func(publication *managedRecoveryPublication, graph *managedRecoveryGraph) error {
			installs++
			return publication.publish(graph)
		},
		DowngradeInspector: &managedRecoveryDowngradeInspectorFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.StartupWithConfig(context.Background(), backupasset.RecoveryConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	graph := manager.graph
	graph.startRun(context.Background())
	<-ownerStarted
	transitionCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = manager.TransitionSettingsWithRestore(
		transitionCtx,
		backupasset.RecoveryConfig{Enabled: false},
		func() error { persists++; return nil },
		func() error { restores++; return nil },
	)
	close(releaseOwner)
	if !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("non-joining transition error=%v, want closed invalid state", err)
	}
	if manager.publication.current() != nil || manager.graph != nil || !manager.downgradeFenced {
		t.Fatalf("non-joining transition publication=%p graph=%p fenced=%t",
			manager.publication.current(), manager.graph, manager.downgradeFenced)
	}
	if builds != 1 || installs != 1 || persists != 0 || restores != 0 {
		t.Fatalf("non-joining transition builds=%d installs=%d persists=%d restores=%d",
			builds, installs, persists, restores)
	}
	if err := manager.TransitionSettings(context.Background(), backupasset.RecoveryConfig{Enabled: true}, func() error {
		persists++
		return nil
	}); !errors.Is(err, backupasset.ErrInvalidState) || persists != 0 {
		t.Fatalf("sticky-fenced re-enable error=%v persists=%d", err, persists)
	}
	if _, err := manager.DowngradeReadiness(context.Background()); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("sticky-fenced readiness error=%v", err)
	}
}

func TestRecoveryRuntimeCurrentTransitionRequiresInstalledConfigBeforeMutation(t *testing.T) {
	builds, reconciliations, installs, persists, restores := 0, 0, 0, 0, 0
	manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
		Build: func(_ context.Context, config backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
			builds++
			if !config.Enabled {
				return nil, errors.New("pre-start transition inferred disabled zero-value config")
			}
			return &managedRecoveryGraph{
				admissionEnabled: config.Enabled,
				reconcileMetadata: func(context.Context) error {
					reconciliations++
					return nil
				},
			}, nil
		},
		Install: func(publication *managedRecoveryPublication, graph *managedRecoveryGraph) error {
			installs++
			return publication.publish(graph)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = manager.TransitionCurrentWithRestore(
		context.Background(),
		func() error { persists++; return nil },
		func() error { restores++; return nil },
	)
	if !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("pre-start current transition error=%v, want invalid state", err)
	}
	if persists != 0 || restores != 0 || builds != 0 || reconciliations != 0 || installs != 0 ||
		manager.publication.current() != nil {
		t.Fatalf("pre-start transition persists=%d restores=%d builds=%d reconciliations=%d installs=%d publication=%p",
			persists, restores, builds, reconciliations, installs, manager.publication.current())
	}
	if err := manager.StartupWithConfig(context.Background(), backupasset.RecoveryConfig{Enabled: true}); err != nil {
		t.Fatalf("enabled startup after rejected current transition: %v", err)
	}
	if current := manager.publication.current(); builds != 1 || reconciliations != 1 || installs != 1 ||
		current == nil || !current.admissionEnabled {
		t.Fatalf("subsequent startup builds=%d reconciliations=%d installs=%d graph=%+v",
			builds, reconciliations, installs, current)
	}
}

func TestRecoveryTransitionRestorationFailureLeavesStickyFenceClosed(t *testing.T) {
	installs := 0
	manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
		Build: func(_ context.Context, config backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
			return &managedRecoveryGraph{
				admissionEnabled:  config.Enabled,
				reconcileMetadata: func(context.Context) error { return nil },
			}, nil
		},
		Install: func(publication *managedRecoveryPublication, graph *managedRecoveryGraph) error {
			installs++
			if installs == 2 {
				return errors.New("injected install failure")
			}
			return publication.publish(graph)
		},
		DowngradeInspector: &managedRecoveryDowngradeInspectorFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.StartupWithConfig(
		context.Background(), backupasset.RecoveryConfig{Enabled: true},
	); err != nil {
		t.Fatal(err)
	}
	err = manager.TransitionSettingsWithRestore(
		context.Background(), backupasset.RecoveryConfig{Enabled: false},
		func() error { return nil },
		func() error { return errors.New("injected persisted restoration failure") },
	)
	if err == nil {
		t.Fatal("transition with failed restoration unexpectedly succeeded")
	}
	if manager.publication.current() != nil || !manager.downgradeFenced || manager.graph != nil {
		t.Fatalf("failed restoration publication=%p fenced=%t graph=%p",
			manager.publication.current(), manager.downgradeFenced, manager.graph)
	}
	persistCalls := 0
	err = manager.TransitionSettings(
		context.Background(), backupasset.RecoveryConfig{Enabled: true},
		func() error { persistCalls++; return nil },
	)
	if !errors.Is(err, backupasset.ErrInvalidState) || persistCalls != 0 {
		t.Fatalf("sticky fence transition err=%v persist=%d", err, persistCalls)
	}
	if _, err := manager.DowngradeReadiness(context.Background()); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("sticky fence downgrade readiness error=%v", err)
	}
}

func TestRecoveryTransitionConstructFailureRestoresAfterPersistAndDrain(t *testing.T) {
	for _, failure := range []string{"build", "reconcile"} {
		t.Run(failure, func(t *testing.T) {
			var mu sync.Mutex
			events := make([]string, 0, 16)
			active := &managedRecoveryGraph{
				admissionEnabled: true,
				reconcileMetadata: func(context.Context) error {
					events = append(events, "active_reconcile")
					return nil
				},
				stopClaims: func() { events = append(events, "active_stop") },
				shutdownLifecycle: func(context.Context) error {
					events = append(events, "active_shutdown")
					return nil
				},
			}
			builds := 0
			failed := false
			manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
				Build: func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
					mu.Lock()
					defer mu.Unlock()
					builds++
					if builds == 1 {
						return active, nil
					}
					if builds == 2 {
						events = append(events, "candidate_build")
					}
					if builds == 2 && failure == "build" && !failed {
						failed = true
						return nil, errors.New("candidate build failed")
					}
					return &managedRecoveryGraph{
						admissionEnabled: builds >= 3,
						reconcileMetadata: func(context.Context) error {
							if builds == 2 {
								events = append(events, "candidate_reconcile")
								if failure == "reconcile" && !failed {
									failed = true
									return errors.New("candidate reconcile failed")
								}
							}
							events = append(events, "restored_reconcile")
							return nil
						},
						shutdownLifecycle: func(context.Context) error {
							events = append(events, "candidate_shutdown")
							return nil
						},
					}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.StartupWithConfig(context.Background(), backupasset.RecoveryConfig{Enabled: true}); err != nil {
				t.Fatal(err)
			}
			persistCalls, restoreCalls := 0, 0
			err = manager.TransitionSettingsWithRestore(context.Background(), backupasset.RecoveryConfig{Enabled: false}, func() error {
				persistCalls++
				events = append(events, "persist")
				return nil
			}, func() error {
				restoreCalls++
				events = append(events, "restore_persisted")
				return nil
			})
			if err == nil {
				t.Fatal("candidate preparation failure unexpectedly succeeded")
			}
			if persistCalls != 1 || restoreCalls != 1 {
				t.Fatalf("candidate %s failure persist=%d restore=%d", failure, persistCalls, restoreCalls)
			}
			if current := manager.publication.current(); current == nil || current == active || !current.admissionEnabled {
				t.Fatalf("candidate %s failure publication=%p original=%p", failure, current, active)
			}
			if !containsManagedRecoveryOrderedEvents(events, []string{
				"active_stop", "active_shutdown", "persist", "candidate_build", "restore_persisted", "restored_reconcile",
			}) {
				t.Fatalf("candidate %s failure ordering=%v", failure, events)
			}
			if manager.downgradeFenced {
				t.Fatalf("candidate %s restored transition installed sticky fence", failure)
			}
		})
	}
}

func TestManagedRecoveryRuntimePrepareSchemaDownJoinsReceiptOwnerBeforeCallback(t *testing.T) {
	eventsMu := sync.Mutex{}
	events := make([]string, 0, 4)
	receipt := newManagedRecoveryReceiptOwnerFake(&eventsMu, &events)
	manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
		Build: func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
			return &managedRecoveryGraph{reconcileMetadata: func(context.Context) error { return nil }}, nil
		},
		ReceiptOwner: receipt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.StartupWithConfig(context.Background(), backupasset.RecoveryConfig{}); err != nil {
		t.Fatal(err)
	}
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	go manager.Run(runCtx)
	select {
	case <-receipt.started:
	case <-time.After(time.Second):
		t.Fatal("receipt owner did not start")
	}
	if err := manager.PrepareSchemaDown(context.Background(), func() error {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		events = append(events, "schema_down")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	eventsMu.Lock()
	got := append([]string(nil), events...)
	eventsMu.Unlock()
	if !containsManagedRecoveryOrderedEvents(got, []string{"receipt_shutdown", "schema_down"}) {
		t.Fatalf("schema-down events=%v, want receipt join before callback", got)
	}
}

func TestRecoveryDisabledSchemaDrainJoinsCleanupLogicalAndReceiptOwners(t *testing.T) {
	eventsMu := sync.Mutex{}
	events := make([]string, 0, 6)
	receipt := newManagedRecoveryReceiptOwnerFake(&eventsMu, &events)
	graphStarted := make(chan struct{})
	graphJoined := make(chan struct{})
	manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
		Build: func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
			return &managedRecoveryGraph{
				admissionEnabled: false,
				reconcileMetadata: func(context.Context) error {
					eventsMu.Lock()
					events = append(events, "logical_reconcile")
					eventsMu.Unlock()
					return nil
				},
				run: func(ctx context.Context) {
					close(graphStarted)
					<-ctx.Done()
					close(graphJoined)
				},
			}, nil
		},
		ReceiptOwner: receipt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.StartupWithConfig(
		context.Background(), backupasset.RecoveryConfig{Enabled: false},
	); err != nil {
		t.Fatal(err)
	}
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	runDone := make(chan struct{})
	go func() {
		manager.Run(runCtx)
		close(runDone)
	}()
	for _, started := range []<-chan struct{}{receipt.started, graphStarted} {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("disabled Recovery maintenance owner did not start")
		}
	}
	err = manager.PrepareSchemaDown(context.Background(), func() error {
		select {
		case <-graphJoined:
			eventsMu.Lock()
			events = append(events, "schema_down")
			eventsMu.Unlock()
			return nil
		default:
			return errors.New("schema drain ran before cleanup/logical owner joined")
		}
	})
	if err != nil {
		t.Fatalf("prepare disabled Recovery schema down: %v", err)
	}
	cancelRun()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("disabled Recovery runtime owner did not join")
	}
	eventsMu.Lock()
	got := append([]string(nil), events...)
	eventsMu.Unlock()
	if !containsManagedRecoveryOrderedEvents(got, []string{
		"logical_reconcile", "receipt_run", "receipt_shutdown", "schema_down",
	}) {
		t.Fatalf("disabled schema-drain events=%v", got)
	}
}

func TestManagedRecoveryRuntimeShutdownJoinsReceiptOwnerWithoutEnabledGraph(t *testing.T) {
	eventsMu := sync.Mutex{}
	events := make([]string, 0, 2)
	receipt := newManagedRecoveryReceiptOwnerFake(&eventsMu, &events)
	manager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
		Build: func(context.Context, backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
			return nil, errors.New("enabled Recovery graph unavailable")
		},
		ReceiptOwner: receipt,
	})
	if err != nil {
		t.Fatal(err)
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	runDone := make(chan struct{})
	go func() {
		manager.Run(runCtx)
		close(runDone)
	}()
	select {
	case <-receipt.started:
	case <-time.After(time.Second):
		t.Fatal("default-disabled Recovery runtime did not start receipt maintenance")
	}

	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not join receipt owner without an enabled graph")
	}
	eventsMu.Lock()
	got := append([]string(nil), events...)
	eventsMu.Unlock()
	if !containsManagedRecoveryOrderedEvents(got, []string{"receipt_run", "receipt_shutdown"}) {
		t.Fatalf("receipt lifecycle events=%v, want run then shutdown", got)
	}
}

func TestManagedRecoveryWorkerWakeAndTakeoverUseIndependentSchedules(t *testing.T) {
	coordinator := &managedRecoveryWorkerCoordinatorFake{
		claims:    make(chan struct{}, 2),
		takeovers: make(chan struct{}, 2),
	}
	timer := newManagedRecoveryTimerFake()
	worker, err := newManagedRecoveryWorker(managedRecoveryWorkerDependencies{
		Coordinator:       coordinator,
		WorkerID:          "recovery-worker-1",
		WorkerConcurrency: 1,
		TakeoverCadence:   time.Hour,
		RetryBase:         time.Second,
		RetryMaxDelay:     time.Minute,
		Policy:            managedRecoveryWorkerPolicyForTest(),
		NewTimer: func(duration time.Duration) managedRecoveryTimer {
			if duration != time.Hour {
				t.Fatalf("takeover duration=%s, want one hour", duration)
			}
			return timer
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("Recovery worker did not stop")
		}
	}()

	if !worker.TryWake(strings.Repeat("a", 32)) {
		t.Fatal("Recovery worker rejected valid nonblocking wake")
	}
	select {
	case <-coordinator.claims:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("queued Recovery work waited for takeover cadence")
	}
	select {
	case <-coordinator.takeovers:
		t.Fatal("wake incorrectly ran takeover scheduler")
	default:
	}
	timer.ticks <- time.Now()
	select {
	case <-coordinator.takeovers:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("takeover scheduler did not run on its own deadline")
	}
}

func TestManagedRecoveryWorkerExecutesEachDurableClaimThroughRecovery(t *testing.T) {
	claim := recovery.RecoveryWorkerClaim{
		JobID: strings.Repeat("a", 32), AttemptID: strings.Repeat("b", 32),
		NodeLeaseID: strings.Repeat("c", 32), WorkerID: "recovery-worker-execute",
		AttemptFence: 1, NodeFence: 1, TransitionRevision: 1,
		LeaseExpiresAt:   time.Now().UTC().Add(time.Minute),
		AbsoluteDeadline: time.Now().UTC().Add(time.Hour),
		SourceFence: backupasset.LeaseFence{
			LeaseID: strings.Repeat("d", 32), RecoveryPointID: strings.Repeat("e", 32),
			HolderType: backupasset.LeaseHolderRecoveryJob, OwnerID: strings.Repeat("a", 32),
			AttemptID: strings.Repeat("b", 32), FenceToken: strings.Repeat("f", 64),
		},
	}
	coordinator := &managedRecoveryWorkerCoordinatorFake{
		claims: make(chan struct{}, 1), takeovers: make(chan struct{}, 1),
		claim: claim, claimFound: true, executions: make(chan recovery.RecoveryWorkerClaim, 1),
	}
	worker, err := newManagedRecoveryWorker(managedRecoveryWorkerDependencies{
		Coordinator: coordinator, Executor: coordinator,
		WorkerID: claim.WorkerID, WorkerConcurrency: 1, TakeoverCadence: time.Hour,
		RetryBase: time.Second, RetryMaxDelay: time.Minute,
		Policy: managedRecoveryWorkerPolicyForTest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	if !worker.TryWake(claim.JobID) {
		t.Fatal("Recovery worker rejected execution wake")
	}
	select {
	case executed := <-coordinator.executions:
		if executed != claim {
			t.Fatalf("executed claim=%+v, want %+v", executed, claim)
		}
	case <-time.After(time.Second):
		t.Fatal("durable Recovery claim was never executed")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Recovery worker did not join")
	}
}

func TestRecoveryHeartbeatFailureCancelsClaimAndPreventsSubsequentTargetCall(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	claim := recovery.RecoveryWorkerClaim{
		JobID: strings.Repeat("a", 32), AttemptID: strings.Repeat("b", 32),
		NodeLeaseID: strings.Repeat("c", 32), WorkerID: "recovery-heartbeat-worker",
		AttemptFence: 1, NodeFence: 1, TransitionRevision: 1, LeaseExpiresAt: now.Add(time.Minute),
		AbsoluteDeadline: now.Add(time.Hour),
		SourceFence: backupasset.LeaseFence{
			LeaseID: strings.Repeat("d", 32), RecoveryPointID: strings.Repeat("e", 32),
			HolderType: backupasset.LeaseHolderRecoveryJob, OwnerID: strings.Repeat("a", 32),
			AttemptID: strings.Repeat("b", 32), FenceToken: strings.Repeat("f", 64),
		},
	}
	timer := newManagedRecoveryTimerFake()
	target := &recoveryHeartbeatTargetFake{}
	executor := &recoveryHeartbeatExecutorFake{started: make(chan struct{}), target: target}
	coordinator := &recoveryHeartbeatCoordinatorFake{
		heartbeats: make(chan recovery.RecoveryWorkerClaim, 1), fenced: make(chan string, 1),
		heartbeatErr: recovery.ErrRecoveryWorkerFenceLost,
	}
	worker, err := newManagedRecoveryWorker(managedRecoveryWorkerDependencies{
		Coordinator: coordinator, Executor: executor, WorkerID: claim.WorkerID, WorkerConcurrency: 1,
		TakeoverCadence: time.Hour, RetryBase: time.Second, RetryMaxDelay: time.Minute,
		Policy: recovery.WorkerPolicy{LeaseRenewMargin: 20 * time.Second, ExecutionTimeout: time.Hour},
		Now:    func() time.Time { return now },
		NewHeartbeatTimer: func(duration time.Duration) managedRecoveryTimer {
			if duration != 40*time.Second {
				t.Fatalf("heartbeat delay=%s, want 40s", duration)
			}
			return timer
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	worker.trackActiveClaim(claim)
	done := make(chan error, 1)
	go func() { done <- worker.executeClaim(context.Background(), claim, true, nil) }()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("claim executor did not start")
	}
	timer.ticks <- now.Add(40 * time.Second)
	select {
	case got := <-coordinator.heartbeats:
		if got != claim {
			t.Fatalf("heartbeat claim=%+v, want %+v", got, claim)
		}
	case <-time.After(time.Second):
		t.Fatal("claim heartbeat did not run")
	}
	select {
	case jobID := <-coordinator.fenced:
		if jobID != claim.JobID {
			t.Fatalf("fenced job=%q, want %q", jobID, claim.JobID)
		}
	case <-time.After(time.Second):
		t.Fatal("failed heartbeat did not fence the job")
	}
	select {
	case err := <-done:
		if !errors.Is(err, recovery.ErrRecoveryWorkerFenceLost) {
			t.Fatalf("heartbeat failure error=%v, want fence lost", err)
		}
	case <-time.After(time.Second):
		t.Fatal("claim did not stop after heartbeat failure")
	}
	if target.calls.Load() != 0 {
		t.Fatalf("target calls after heartbeat failure=%d, want zero", target.calls.Load())
	}
}

func TestRecoveryExecutionDeadlineContextUsesFrozenClaimDeadline(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	claim := recovery.RecoveryWorkerClaim{
		JobID: strings.Repeat("a", 32), AttemptID: strings.Repeat("b", 32),
		NodeLeaseID: strings.Repeat("c", 32), WorkerID: "recovery-deadline-worker",
		AttemptFence: 1, NodeFence: 1, TransitionRevision: 1,
		LeaseExpiresAt: now.Add(time.Minute), AbsoluteDeadline: now.Add(7 * time.Minute),
		SourceFence: backupasset.LeaseFence{
			LeaseID: strings.Repeat("d", 32), RecoveryPointID: strings.Repeat("e", 32),
			HolderType: backupasset.LeaseHolderRecoveryJob, OwnerID: strings.Repeat("a", 32),
			AttemptID: strings.Repeat("b", 32), FenceToken: strings.Repeat("f", 64),
		},
	}
	executor := &recoveryDeadlineExecutorFake{deadline: make(chan recoveryDeadlineObservation, 1)}
	coordinator := &recoveryHeartbeatCoordinatorFake{
		heartbeats: make(chan recovery.RecoveryWorkerClaim, 1), fenced: make(chan string, 1),
	}
	worker, err := newManagedRecoveryWorker(managedRecoveryWorkerDependencies{
		Coordinator: coordinator, Executor: executor, WorkerID: claim.WorkerID, WorkerConcurrency: 1,
		TakeoverCadence: time.Hour, RetryBase: time.Second, RetryMaxDelay: time.Minute,
		Policy:            recovery.WorkerPolicy{LeaseRenewMargin: 20 * time.Second, ExecutionTimeout: time.Hour},
		Now:               func() time.Time { return now },
		NewHeartbeatTimer: func(time.Duration) managedRecoveryTimer { return newManagedRecoveryTimerFake() },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.executeClaim(context.Background(), claim, true, nil); err != nil {
		t.Fatalf("execute claim: %v", err)
	}
	observation := <-executor.deadline
	if !observation.ok || !observation.deadline.Equal(claim.AbsoluteDeadline) {
		t.Fatalf("execution context deadline=%s/%t, want exact frozen %s", observation.deadline, observation.ok, claim.AbsoluteDeadline)
	}
}

func TestRecoveryHeartbeatSuccessfulCappedRenewalDoesNotResetAtZeroDelay(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	claim := recovery.RecoveryWorkerClaim{
		JobID: strings.Repeat("a", 32), AttemptID: strings.Repeat("b", 32),
		NodeLeaseID: strings.Repeat("c", 32), WorkerID: "recovery-capped-heartbeat-worker",
		AttemptFence: 1, NodeFence: 1, TransitionRevision: 1,
		LeaseExpiresAt: now.Add(time.Minute), AbsoluteDeadline: now.Add(time.Minute),
		SourceFence: backupasset.LeaseFence{
			LeaseID: strings.Repeat("d", 32), RecoveryPointID: strings.Repeat("e", 32),
			HolderType: backupasset.LeaseHolderRecoveryJob, OwnerID: strings.Repeat("a", 32),
			AttemptID: strings.Repeat("b", 32), FenceToken: strings.Repeat("f", 64),
		},
	}
	timer := newManagedRecoveryTimerFake()
	target := &recoveryHeartbeatTargetFake{}
	executor := &recoveryHeartbeatExecutorFake{started: make(chan struct{}), target: target}
	coordinator := &recoveryHeartbeatCoordinatorFake{
		heartbeats: make(chan recovery.RecoveryWorkerClaim, 1), fenced: make(chan string, 1),
		heartbeatResult: claim,
	}
	worker, err := newManagedRecoveryWorker(managedRecoveryWorkerDependencies{
		Coordinator: coordinator, Executor: executor, WorkerID: claim.WorkerID, WorkerConcurrency: 1,
		TakeoverCadence: time.Hour, RetryBase: time.Second, RetryMaxDelay: time.Minute,
		Policy: recovery.WorkerPolicy{LeaseRenewMargin: 20 * time.Second, ExecutionTimeout: time.Hour},
		Now:    func() time.Time { return time.Unix(0, clock.Load()).UTC() },
		NewHeartbeatTimer: func(duration time.Duration) managedRecoveryTimer {
			if duration != 40*time.Second {
				t.Fatalf("heartbeat delay=%s, want 40s", duration)
			}
			return timer
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.executeClaim(ctx, claim, true, nil) }()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("claim executor did not start")
	}
	clock.Store(now.Add(40 * time.Second).UnixNano())
	timer.ticks <- now.Add(40 * time.Second)
	select {
	case <-coordinator.heartbeats:
	case <-time.After(time.Second):
		t.Fatal("capped claim heartbeat did not run")
	}
	time.Sleep(50 * time.Millisecond)
	if resets := timer.resetsSnapshot(); len(resets) != 0 {
		t.Fatalf("capped heartbeat timer resets=%v, want none", resets)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("capped claim did not stop")
	}
}

func TestManagedRecoveryWorkerReusesAndStopsTakeoverTimer(t *testing.T) {
	coordinator := &managedRecoveryWorkerCoordinatorFake{
		claims:    make(chan struct{}, 4),
		takeovers: make(chan struct{}, 1),
	}
	timer := newManagedRecoveryTimerFake()
	allocations := make(chan time.Duration, 2)
	worker, err := newManagedRecoveryWorker(managedRecoveryWorkerDependencies{
		Coordinator:       coordinator,
		WorkerID:          "recovery-worker-timer-owner",
		WorkerConcurrency: 1,
		TakeoverCadence:   time.Hour,
		RetryBase:         time.Second,
		RetryMaxDelay:     time.Minute,
		Policy:            managedRecoveryWorkerPolicyForTest(),
		NewTimer: func(duration time.Duration) managedRecoveryTimer {
			allocations <- duration
			return timer
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()

	select {
	case duration := <-allocations:
		if duration != time.Hour {
			t.Fatalf("takeover timer duration=%s, want one hour", duration)
		}
	case <-time.After(time.Second):
		t.Fatal("Recovery worker did not allocate its takeover timer")
	}
	for index := 0; index < 3; index++ {
		if !worker.TryWake(strings.Repeat(string(rune('a'+index)), 32)) {
			t.Fatalf("Recovery worker rejected wake %d", index)
		}
		select {
		case <-coordinator.claims:
		case <-time.After(time.Second):
			t.Fatalf("Recovery worker did not process wake %d", index)
		}
	}
	select {
	case duration := <-allocations:
		t.Fatalf("wake allocated another takeover timer with duration %s", duration)
	default:
	}
	if resets := timer.resetsSnapshot(); len(resets) != 0 {
		t.Fatalf("wake reset takeover timer: %v", resets)
	}

	timer.ticks <- time.Now()
	select {
	case <-coordinator.takeovers:
	case <-time.After(time.Second):
		t.Fatal("Recovery worker did not process takeover deadline")
	}
	waitManagedRecoveryTimerResets(t, timer, 1)
	if resets := timer.resetsSnapshot(); len(resets) != 1 || resets[0] != time.Hour {
		t.Fatalf("takeover timer resets=%v, want [1h]", resets)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Recovery worker did not stop")
	}
	if timer.stopCallsSnapshot() != 1 {
		t.Fatalf("takeover timer Stop calls=%d, want 1", timer.stopCallsSnapshot())
	}
}

func TestManagedRecoveryWorkerHonorsConfiguredConcurrency(t *testing.T) {
	coordinator := newManagedRecoveryConcurrentCoordinator(3)
	worker, err := newManagedRecoveryWorker(managedRecoveryWorkerDependencies{
		Coordinator: coordinator, Executor: coordinator,
		WorkerID: "recovery-worker-concurrency", WorkerConcurrency: 2,
		TakeoverCadence: time.Hour, RetryBase: time.Second, RetryMaxDelay: time.Minute,
		Policy: managedRecoveryWorkerPolicyForTest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	defer func() {
		cancel()
		coordinator.releaseAll()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("Recovery worker did not join concurrent executions")
		}
	}()

	if !worker.TryWake(strings.Repeat("a", 32)) {
		t.Fatal("Recovery worker rejected concurrency wake")
	}
	for index := 0; index < 2; index++ {
		select {
		case <-coordinator.started:
		case <-time.After(time.Second):
			t.Fatal("configured Recovery concurrency was not reached")
		}
	}
	select {
	case claim := <-coordinator.started:
		t.Fatalf("Recovery worker exceeded configured concurrency with %+v", claim)
	case <-time.After(25 * time.Millisecond):
	}
	coordinator.releaseOne()
	select {
	case <-coordinator.started:
	case <-time.After(time.Second):
		t.Fatal("Recovery worker did not refill a released concurrency slot")
	}
	if maximum := coordinator.maximum.Load(); maximum != 2 {
		t.Fatalf("maximum concurrent Recovery executions=%d, want 2", maximum)
	}
}

func TestManagedRecoveryWorkerRetriesWithIndependentBoundedBackoff(t *testing.T) {
	coordinator := &managedRecoveryRetryCoordinator{
		calls: make(chan struct{}, 4),
		errs: []error{
			recovery.ErrRecoveryWorkerUnavailable,
			recovery.ErrRecoveryWorkerUnavailable,
			recovery.ErrRecoveryWorkerUnavailable,
			nil,
		},
	}
	takeoverTimer := newManagedRecoveryTimerFake()
	retryTimer := newManagedRecoveryTimerFake()
	allocations := make(chan time.Duration, 2)
	worker, err := newManagedRecoveryWorker(managedRecoveryWorkerDependencies{
		Coordinator: coordinator, WorkerID: "recovery-worker-retry", WorkerConcurrency: 1,
		TakeoverCadence: time.Hour, RetryBase: time.Second, RetryMaxDelay: 2 * time.Second,
		Policy: managedRecoveryWorkerPolicyForTest(),
		NewTimer: func(duration time.Duration) managedRecoveryTimer {
			allocations <- duration
			if duration == time.Hour {
				return takeoverTimer
			}
			return retryTimer
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("Recovery worker did not stop after retry test")
		}
	}()

	if !worker.TryWake(strings.Repeat("a", 32)) {
		t.Fatal("Recovery worker rejected retry wake")
	}
	waitManagedRecoveryWorkerCalls(t, coordinator.calls, 1)
	select {
	case duration := <-allocations:
		if duration != time.Hour {
			t.Fatalf("first timer=%s, want takeover cadence", duration)
		}
	case <-time.After(time.Second):
		t.Fatal("takeover timer was not allocated")
	}
	select {
	case duration := <-allocations:
		if duration != time.Second {
			t.Fatalf("retry timer=%s, want RetryBase", duration)
		}
	case <-time.After(time.Second):
		t.Fatal("retry timer was not allocated after scheduler failure")
	}

	retryTimer.ticks <- time.Now()
	waitManagedRecoveryWorkerCalls(t, coordinator.calls, 1)
	waitManagedRecoveryTimerResets(t, retryTimer, 1)
	if resets := retryTimer.resetsSnapshot(); !reflect.DeepEqual(resets, []time.Duration{2 * time.Second}) {
		t.Fatalf("retry resets=%v, want capped exponential [2s]", resets)
	}
	retryTimer.ticks <- time.Now()
	waitManagedRecoveryWorkerCalls(t, coordinator.calls, 1)
	waitManagedRecoveryTimerResets(t, retryTimer, 2)
	if resets := retryTimer.resetsSnapshot(); !reflect.DeepEqual(resets, []time.Duration{2 * time.Second, 2 * time.Second}) {
		t.Fatalf("retry resets=%v, want cap retained", resets)
	}
	retryTimer.ticks <- time.Now()
	waitManagedRecoveryWorkerCalls(t, coordinator.calls, 1)
	waitManagedRecoveryTimerStops(t, retryTimer, 1)
	if resets := takeoverTimer.resetsSnapshot(); len(resets) != 0 {
		t.Fatalf("retry scheduling reset takeover timer: %v", resets)
	}
}

func TestManagedRecoveryWorkerRetryResetDrainsExpiredTimer(t *testing.T) {
	timer := &managedRecoveryExpiredTimerFake{ticks: make(chan time.Time, 1)}
	timer.ticks <- time.Now()

	stopAndDrainManagedRecoveryTimer(timer)

	if stopCalls := timer.stopCalls.Load(); stopCalls != 1 {
		t.Fatalf("expired retry timer Stop calls=%d, want 1", stopCalls)
	}
	select {
	case <-timer.ticks:
		t.Fatal("expired retry tick remained queued after reset")
	default:
	}
}

func TestManagedRecoveryWorkerDoesNotRearmStaleRetryTickAfterSuccess(t *testing.T) {
	retryTimer := &managedRecoveryExpiredTimerFake{ticks: make(chan time.Time, 1)}
	coordinator := &managedRecoveryRetryResetCoordinator{
		calls: make(chan struct{}, 8),
		errs: []error{
			recovery.ErrRecoveryWorkerUnavailable,
			nil,
			recovery.ErrRecoveryWorkerUnavailable,
		},
		staleTimer: retryTimer,
	}
	takeoverTimer := newManagedRecoveryTimerFake()
	allocations := make(chan time.Duration, 2)
	worker, err := newManagedRecoveryWorker(managedRecoveryWorkerDependencies{
		Coordinator: coordinator, WorkerID: "recovery-worker-stale-retry", WorkerConcurrency: 1,
		TakeoverCadence: time.Hour, RetryBase: time.Second, RetryMaxDelay: 2 * time.Second,
		Policy: managedRecoveryWorkerPolicyForTest(),
		NewTimer: func(duration time.Duration) managedRecoveryTimer {
			allocations <- duration
			if duration == time.Hour {
				return takeoverTimer
			}
			return retryTimer
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("Recovery worker did not stop after stale retry test")
		}
	}()

	if !worker.TryWake(strings.Repeat("a", 32)) {
		t.Fatal("Recovery worker rejected initial wake")
	}
	waitManagedRecoveryWorkerCalls(t, coordinator.calls, 1)
	select {
	case duration := <-allocations:
		if duration != time.Hour {
			t.Fatalf("first timer=%s, want takeover cadence", duration)
		}
	case <-time.After(time.Second):
		t.Fatal("takeover timer was not allocated")
	}
	select {
	case duration := <-allocations:
		if duration != time.Second {
			t.Fatalf("retry timer=%s, want RetryBase", duration)
		}
	case <-time.After(time.Second):
		t.Fatal("retry timer was not allocated")
	}

	retryTimer.ticks <- time.Now()
	waitManagedRecoveryWorkerCalls(t, coordinator.calls, 1)
	waitManagedRecoveryExpiredTimerStops(t, retryTimer, 1)

	if !worker.TryWake(strings.Repeat("b", 32)) {
		t.Fatal("Recovery worker rejected later wake")
	}
	waitManagedRecoveryWorkerCalls(t, coordinator.calls, 1)
	select {
	case <-coordinator.calls:
		t.Fatal("stale retry tick caused an unexpected fourth claim")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestManagedRecoveryWorkerShutdownJoinsThenFencesActiveClaims(t *testing.T) {
	coordinator := newManagedRecoveryFencingCoordinator(3)
	worker, err := newManagedRecoveryWorker(managedRecoveryWorkerDependencies{
		Coordinator: coordinator, Executor: coordinator,
		WorkerID: "recovery-worker-fencing", WorkerConcurrency: 2,
		TakeoverCadence: time.Hour, RetryBase: time.Second, RetryMaxDelay: time.Minute,
		Policy: managedRecoveryWorkerPolicyForTest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	graph := &managedRecoveryGraph{
		stopClaims: worker.StopAccepting,
		run:        worker.Run,
		fenceOwnership: func(ctx context.Context) error {
			return worker.FenceActiveClaims(ctx)
		},
	}
	done := graph.startRun(context.Background())
	if !worker.TryWake(strings.Repeat("a", 32)) {
		t.Fatal("Recovery worker rejected fencing wake")
	}
	for index := 0; index < 2; index++ {
		select {
		case <-coordinator.started:
		case <-time.After(time.Second):
			t.Fatal("Recovery execution did not start before shutdown")
		}
	}
	if err := shutdownManagedRecoveryGraph(context.Background(), graph); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	default:
		t.Fatal("Recovery graph shutdown did not join worker owner")
	}
	if fenced := coordinator.fencedSnapshot(); !reflect.DeepEqual(
		fenced, []string{fmt.Sprintf("%032x", 1), fmt.Sprintf("%032x", 2)},
	) {
		t.Fatalf("fenced active claims=%v", fenced)
	}
}

func TestManagedRecoveryWorkerShutdownFencesClaimAfterNonContextExecutionError(t *testing.T) {
	coordinator := &managedRecoveryShutdownErrorCoordinator{
		started: make(chan struct{}), fenced: make(chan string, 1),
	}
	worker, err := newManagedRecoveryWorker(managedRecoveryWorkerDependencies{
		Coordinator: coordinator, Executor: coordinator,
		WorkerID: "recovery-worker-shutdown-error", WorkerConcurrency: 1,
		TakeoverCadence: time.Hour, RetryBase: time.Second, RetryMaxDelay: time.Minute,
		Policy: managedRecoveryWorkerPolicyForTest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	graph := &managedRecoveryGraph{
		stopClaims: worker.StopAccepting, run: worker.Run,
		fenceOwnership: worker.FenceActiveClaims,
	}
	graph.startRun(context.Background())
	jobID := strings.Repeat("a", 32)
	if !worker.TryWake(jobID) {
		t.Fatal("Recovery worker rejected shutdown-error wake")
	}
	select {
	case <-coordinator.started:
	case <-time.After(time.Second):
		t.Fatal("Recovery shutdown-error execution did not start")
	}
	if err := shutdownManagedRecoveryGraph(context.Background(), graph); err != nil {
		t.Fatal(err)
	}
	select {
	case fenced := <-coordinator.fenced:
		if fenced != jobID {
			t.Fatalf("fenced job=%q, want %q", fenced, jobID)
		}
	default:
		t.Fatal("non-context execution error after shutdown lost durable claim fencing")
	}
}

func TestManagedRecoveryWorkerDoesNotFenceCompletedContextError(t *testing.T) {
	coordinator := &managedRecoveryCompletedContextCoordinator{executed: make(chan struct{})}
	worker, err := newManagedRecoveryWorker(managedRecoveryWorkerDependencies{
		Coordinator: coordinator, Executor: coordinator,
		WorkerID: "recovery-worker-completed-context", WorkerConcurrency: 1,
		TakeoverCadence: time.Hour, RetryBase: time.Second, RetryMaxDelay: time.Minute,
		Policy: managedRecoveryWorkerPolicyForTest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	graph := &managedRecoveryGraph{
		stopClaims: worker.StopAccepting, run: worker.Run,
		fenceOwnership: worker.FenceActiveClaims,
	}
	graph.startRun(context.Background())
	if !worker.TryWake(strings.Repeat("a", 32)) {
		t.Fatal("Recovery worker rejected completed-context wake")
	}
	select {
	case <-coordinator.executed:
	case <-time.After(time.Second):
		t.Fatal("Recovery context-error execution did not complete")
	}
	waitManagedRecoveryActiveClaims(t, worker, 0)
	if err := shutdownManagedRecoveryGraph(context.Background(), graph); err != nil {
		t.Fatal(err)
	}
	if calls := coordinator.fenceCalls.Load(); calls != 0 {
		t.Fatalf("completed context error was fenced %d times", calls)
	}
}

func TestManagedRecoveryWorkerFencingAcceptsAlreadyLostOwnership(t *testing.T) {
	coordinator := &managedRecoveryLostFenceCoordinator{}
	worker, err := newManagedRecoveryWorker(managedRecoveryWorkerDependencies{
		Coordinator: coordinator, WorkerID: "recovery-worker-lost-fence",
		WorkerConcurrency: 1, TakeoverCadence: time.Hour,
		RetryBase: time.Second, RetryMaxDelay: time.Minute,
		Policy: managedRecoveryWorkerPolicyForTest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	worker.trackActiveClaim(recovery.RecoveryWorkerClaim{JobID: strings.Repeat("a", 32)})
	if err := worker.FenceActiveClaims(context.Background()); err != nil {
		t.Fatalf("already-lost Recovery ownership blocked shutdown: %v", err)
	}
}

type managedRecoveryWorkerCoordinatorFake struct {
	mu         sync.Mutex
	claims     chan struct{}
	takeovers  chan struct{}
	claim      recovery.RecoveryWorkerClaim
	claimFound bool
	executions chan recovery.RecoveryWorkerClaim
}

func managedRecoveryWorkerPolicyForTest() recovery.WorkerPolicy {
	return recovery.WorkerPolicy{LeaseRenewMargin: 20 * time.Second, ExecutionTimeout: time.Hour}
}

func managedRecoveryHeartbeatForTest(claim recovery.RecoveryWorkerClaim) (recovery.RecoveryWorkerClaim, error) {
	claim.LeaseExpiresAt = time.Now().UTC().Add(time.Minute)
	return claim, nil
}

type recoveryHeartbeatCoordinatorFake struct {
	heartbeats      chan recovery.RecoveryWorkerClaim
	fenced          chan string
	heartbeatResult recovery.RecoveryWorkerClaim
	heartbeatErr    error
}

func (*recoveryHeartbeatCoordinatorFake) ClaimNext(context.Context, string) (recovery.RecoveryWorkerClaim, bool, error) {
	return recovery.RecoveryWorkerClaim{}, false, nil
}

func (*recoveryHeartbeatCoordinatorFake) TakeoverExpired(context.Context, string) (recovery.RecoveryWorkerClaim, bool, error) {
	return recovery.RecoveryWorkerClaim{}, false, nil
}

func (fake *recoveryHeartbeatCoordinatorFake) Heartbeat(
	_ context.Context,
	claim recovery.RecoveryWorkerClaim,
) (recovery.RecoveryWorkerClaim, error) {
	fake.heartbeats <- claim
	return fake.heartbeatResult, fake.heartbeatErr
}

func (fake *recoveryHeartbeatCoordinatorFake) CancelJob(_ context.Context, jobID string) error {
	fake.fenced <- jobID
	return nil
}

type recoveryHeartbeatTargetFake struct{ calls atomic.Int32 }

func (target *recoveryHeartbeatTargetFake) call(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target.calls.Add(1)
	return nil
}

type recoveryHeartbeatExecutorFake struct {
	started chan struct{}
	target  *recoveryHeartbeatTargetFake
}

type recoveryDeadlineObservation struct {
	deadline time.Time
	ok       bool
}

type recoveryDeadlineExecutorFake struct {
	deadline chan recoveryDeadlineObservation
}

func (fake *recoveryDeadlineExecutorFake) ExecuteResolvedClaim(ctx context.Context, _ recovery.RecoveryWorkerClaim) error {
	deadline, ok := ctx.Deadline()
	fake.deadline <- recoveryDeadlineObservation{deadline: deadline, ok: ok}
	return nil
}

func (fake *recoveryHeartbeatExecutorFake) ExecuteResolvedClaim(ctx context.Context, _ recovery.RecoveryWorkerClaim) error {
	close(fake.started)
	<-ctx.Done()
	return fake.target.call(ctx)
}

type managedRecoveryConcurrentCoordinator struct {
	mu        sync.Mutex
	remaining int
	started   chan recovery.RecoveryWorkerClaim
	release   chan struct{}
	active    atomic.Int32
	maximum   atomic.Int32
}

func newManagedRecoveryConcurrentCoordinator(total int) *managedRecoveryConcurrentCoordinator {
	return &managedRecoveryConcurrentCoordinator{
		remaining: total, started: make(chan recovery.RecoveryWorkerClaim, total), release: make(chan struct{}, total),
	}
}

func (coordinator *managedRecoveryConcurrentCoordinator) ClaimNext(
	_ context.Context,
	workerID string,
) (recovery.RecoveryWorkerClaim, bool, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.remaining == 0 {
		return recovery.RecoveryWorkerClaim{}, false, nil
	}
	ordinal := 4 - coordinator.remaining
	coordinator.remaining--
	return recovery.RecoveryWorkerClaim{
		JobID: fmt.Sprintf("%032x", ordinal), AttemptID: fmt.Sprintf("%032x", ordinal+10),
		NodeLeaseID: fmt.Sprintf("%032x", ordinal+20), WorkerID: workerID,
		AttemptFence: 1, NodeFence: 1, TransitionRevision: 1,
		LeaseExpiresAt:   time.Now().UTC().Add(time.Minute),
		AbsoluteDeadline: time.Now().UTC().Add(time.Hour),
		SourceFence: backupasset.LeaseFence{
			LeaseID: fmt.Sprintf("%032x", ordinal+30), RecoveryPointID: fmt.Sprintf("%032x", ordinal+40),
			HolderType: backupasset.LeaseHolderRecoveryJob, OwnerID: fmt.Sprintf("%032x", ordinal),
			AttemptID: fmt.Sprintf("%032x", ordinal+10), FenceToken: strings.Repeat("f", 64),
		},
	}, true, nil
}

func (*managedRecoveryConcurrentCoordinator) TakeoverExpired(
	context.Context,
	string,
) (recovery.RecoveryWorkerClaim, bool, error) {
	return recovery.RecoveryWorkerClaim{}, false, nil
}

func (*managedRecoveryConcurrentCoordinator) Heartbeat(
	_ context.Context,
	claim recovery.RecoveryWorkerClaim,
) (recovery.RecoveryWorkerClaim, error) {
	return managedRecoveryHeartbeatForTest(claim)
}

func (*managedRecoveryConcurrentCoordinator) CancelJob(context.Context, string) error { return nil }

func (coordinator *managedRecoveryConcurrentCoordinator) ExecuteResolvedClaim(
	ctx context.Context,
	claim recovery.RecoveryWorkerClaim,
) error {
	active := coordinator.active.Add(1)
	for {
		maximum := coordinator.maximum.Load()
		if active <= maximum || coordinator.maximum.CompareAndSwap(maximum, active) {
			break
		}
	}
	coordinator.started <- claim
	select {
	case <-ctx.Done():
	case <-coordinator.release:
	}
	coordinator.active.Add(-1)
	return ctx.Err()
}

func (coordinator *managedRecoveryConcurrentCoordinator) releaseOne() {
	coordinator.release <- struct{}{}
}

func (coordinator *managedRecoveryConcurrentCoordinator) releaseAll() {
	for index := 0; index < cap(coordinator.release); index++ {
		select {
		case coordinator.release <- struct{}{}:
		default:
			return
		}
	}
}

type managedRecoveryRetryCoordinator struct {
	mu    sync.Mutex
	calls chan struct{}
	errs  []error
}

type managedRecoveryRetryResetCoordinator struct {
	mu         sync.Mutex
	calls      chan struct{}
	errs       []error
	staleTimer *managedRecoveryExpiredTimerFake
}

type managedRecoveryFencingCoordinator struct {
	*managedRecoveryConcurrentCoordinator
	mu     sync.Mutex
	fenced []string
}

type managedRecoveryCompletedContextCoordinator struct {
	mu         sync.Mutex
	claimed    bool
	executed   chan struct{}
	fenceCalls atomic.Int32
}

type managedRecoveryShutdownErrorCoordinator struct {
	mu      sync.Mutex
	claimed bool
	started chan struct{}
	fenced  chan string
}

type managedRecoveryLostFenceCoordinator struct{}

func (*managedRecoveryLostFenceCoordinator) ClaimNext(
	context.Context,
	string,
) (recovery.RecoveryWorkerClaim, bool, error) {
	return recovery.RecoveryWorkerClaim{}, false, nil
}

func (*managedRecoveryLostFenceCoordinator) TakeoverExpired(
	context.Context,
	string,
) (recovery.RecoveryWorkerClaim, bool, error) {
	return recovery.RecoveryWorkerClaim{}, false, nil
}

func (*managedRecoveryLostFenceCoordinator) CancelJob(context.Context, string) error {
	return recovery.ErrRecoveryWorkerFenceLost
}

func (*managedRecoveryLostFenceCoordinator) Heartbeat(
	_ context.Context,
	claim recovery.RecoveryWorkerClaim,
) (recovery.RecoveryWorkerClaim, error) {
	return managedRecoveryHeartbeatForTest(claim)
}

func (coordinator *managedRecoveryCompletedContextCoordinator) ClaimNext(
	_ context.Context,
	workerID string,
) (recovery.RecoveryWorkerClaim, bool, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.claimed {
		return recovery.RecoveryWorkerClaim{}, false, nil
	}
	coordinator.claimed = true
	return recovery.RecoveryWorkerClaim{
		JobID: strings.Repeat("a", 32), AttemptID: strings.Repeat("b", 32),
		NodeLeaseID: strings.Repeat("c", 32), WorkerID: workerID,
		AttemptFence: 1, NodeFence: 1, TransitionRevision: 1,
		LeaseExpiresAt:   time.Now().UTC().Add(time.Minute),
		AbsoluteDeadline: time.Now().UTC().Add(time.Hour),
		SourceFence: backupasset.LeaseFence{
			LeaseID: strings.Repeat("d", 32), RecoveryPointID: strings.Repeat("e", 32),
			HolderType: backupasset.LeaseHolderRecoveryJob, OwnerID: strings.Repeat("a", 32),
			AttemptID: strings.Repeat("b", 32), FenceToken: strings.Repeat("f", 64),
		},
	}, true, nil
}

func (*managedRecoveryCompletedContextCoordinator) TakeoverExpired(
	context.Context,
	string,
) (recovery.RecoveryWorkerClaim, bool, error) {
	return recovery.RecoveryWorkerClaim{}, false, nil
}

func (coordinator *managedRecoveryCompletedContextCoordinator) ExecuteResolvedClaim(
	context.Context,
	recovery.RecoveryWorkerClaim,
) error {
	close(coordinator.executed)
	return context.Canceled
}

func (coordinator *managedRecoveryCompletedContextCoordinator) CancelJob(context.Context, string) error {
	coordinator.fenceCalls.Add(1)
	return nil
}

func (*managedRecoveryCompletedContextCoordinator) Heartbeat(
	_ context.Context,
	claim recovery.RecoveryWorkerClaim,
) (recovery.RecoveryWorkerClaim, error) {
	return managedRecoveryHeartbeatForTest(claim)
}

func (coordinator *managedRecoveryShutdownErrorCoordinator) ClaimNext(
	_ context.Context,
	workerID string,
) (recovery.RecoveryWorkerClaim, bool, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.claimed {
		return recovery.RecoveryWorkerClaim{}, false, nil
	}
	coordinator.claimed = true
	return recovery.RecoveryWorkerClaim{
		JobID: strings.Repeat("a", 32), AttemptID: strings.Repeat("b", 32),
		NodeLeaseID: strings.Repeat("c", 32), WorkerID: workerID,
		AttemptFence: 1, NodeFence: 1, TransitionRevision: 1,
		LeaseExpiresAt:   time.Now().UTC().Add(time.Minute),
		AbsoluteDeadline: time.Now().UTC().Add(time.Hour),
		SourceFence: backupasset.LeaseFence{
			LeaseID: strings.Repeat("d", 32), RecoveryPointID: strings.Repeat("e", 32),
			HolderType: backupasset.LeaseHolderRecoveryJob, OwnerID: strings.Repeat("a", 32),
			AttemptID: strings.Repeat("b", 32), FenceToken: strings.Repeat("f", 64),
		},
	}, true, nil
}

func (*managedRecoveryShutdownErrorCoordinator) TakeoverExpired(
	context.Context,
	string,
) (recovery.RecoveryWorkerClaim, bool, error) {
	return recovery.RecoveryWorkerClaim{}, false, nil
}

func (coordinator *managedRecoveryShutdownErrorCoordinator) ExecuteResolvedClaim(
	ctx context.Context,
	_ recovery.RecoveryWorkerClaim,
) error {
	close(coordinator.started)
	<-ctx.Done()
	return recovery.ErrRecoveryWorkerUnavailable
}

func (coordinator *managedRecoveryShutdownErrorCoordinator) CancelJob(_ context.Context, jobID string) error {
	coordinator.fenced <- jobID
	return nil
}

func (*managedRecoveryShutdownErrorCoordinator) Heartbeat(
	_ context.Context,
	claim recovery.RecoveryWorkerClaim,
) (recovery.RecoveryWorkerClaim, error) {
	return managedRecoveryHeartbeatForTest(claim)
}

func waitManagedRecoveryActiveClaims(t *testing.T, worker *managedRecoveryWorker, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		worker.activeMu.Lock()
		count := len(worker.activeClaims)
		worker.activeMu.Unlock()
		if count == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	worker.activeMu.Lock()
	count := len(worker.activeClaims)
	worker.activeMu.Unlock()
	t.Fatalf("active Recovery claims=%d, want %d", count, want)
}

func newManagedRecoveryFencingCoordinator(total int) *managedRecoveryFencingCoordinator {
	return &managedRecoveryFencingCoordinator{
		managedRecoveryConcurrentCoordinator: newManagedRecoveryConcurrentCoordinator(total),
	}
}

func (coordinator *managedRecoveryFencingCoordinator) CancelJob(ctx context.Context, jobID string) error {
	if coordinator.active.Load() != 0 {
		return errors.New("Recovery claim fenced before execution joined")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.fenced = append(coordinator.fenced, jobID)
	return nil
}

func (coordinator *managedRecoveryFencingCoordinator) fencedSnapshot() []string {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return append([]string(nil), coordinator.fenced...)
}

func (coordinator *managedRecoveryRetryCoordinator) ClaimNext(
	context.Context,
	string,
) (recovery.RecoveryWorkerClaim, bool, error) {
	coordinator.calls <- struct{}{}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if len(coordinator.errs) == 0 {
		return recovery.RecoveryWorkerClaim{}, false, nil
	}
	err := coordinator.errs[0]
	coordinator.errs = coordinator.errs[1:]
	return recovery.RecoveryWorkerClaim{}, false, err
}

func (*managedRecoveryRetryCoordinator) TakeoverExpired(
	context.Context,
	string,
) (recovery.RecoveryWorkerClaim, bool, error) {
	return recovery.RecoveryWorkerClaim{}, false, nil
}

func (*managedRecoveryRetryCoordinator) Heartbeat(
	_ context.Context,
	claim recovery.RecoveryWorkerClaim,
) (recovery.RecoveryWorkerClaim, error) {
	return managedRecoveryHeartbeatForTest(claim)
}

func (*managedRecoveryRetryCoordinator) CancelJob(context.Context, string) error { return nil }

func (coordinator *managedRecoveryRetryResetCoordinator) ClaimNext(
	context.Context,
	string,
) (recovery.RecoveryWorkerClaim, bool, error) {
	coordinator.calls <- struct{}{}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if len(coordinator.errs) == 0 {
		return recovery.RecoveryWorkerClaim{}, false, nil
	}
	err := coordinator.errs[0]
	coordinator.errs = coordinator.errs[1:]
	if len(coordinator.errs) == 1 && coordinator.staleTimer != nil {
		coordinator.staleTimer.ticks <- time.Now()
	}
	return recovery.RecoveryWorkerClaim{}, false, err
}

func (*managedRecoveryRetryResetCoordinator) TakeoverExpired(
	context.Context,
	string,
) (recovery.RecoveryWorkerClaim, bool, error) {
	return recovery.RecoveryWorkerClaim{}, false, nil
}

func (*managedRecoveryRetryResetCoordinator) Heartbeat(
	_ context.Context,
	claim recovery.RecoveryWorkerClaim,
) (recovery.RecoveryWorkerClaim, error) {
	return managedRecoveryHeartbeatForTest(claim)
}

func (*managedRecoveryRetryResetCoordinator) CancelJob(context.Context, string) error { return nil }

func waitManagedRecoveryWorkerCalls(t *testing.T, calls <-chan struct{}, want int) {
	t.Helper()
	for index := 0; index < want; index++ {
		select {
		case <-calls:
		case <-time.After(time.Second):
			t.Fatalf("Recovery worker calls=%d, want %d", index, want)
		}
	}
}

type managedRecoveryDowngradeInspectorFake struct {
	snapshot managedRecoveryDowngradeSnapshot
	err      error
}

func (inspector *managedRecoveryDowngradeInspectorFake) SnapshotRecoveryDowngradeBlockers(
	context.Context,
) (managedRecoveryDowngradeSnapshot, error) {
	return inspector.snapshot, inspector.err
}

type managedRecoveryAuthorizationBackendFake struct {
	result recovery.RecoveryAuthorizationResult
	err    error
}

func (backend *managedRecoveryAuthorizationBackendFake) ReplayAuthorization(
	context.Context,
	recovery.RecoveryAuthorizationRequest,
) (recovery.RecoveryAuthorizationResult, bool, error) {
	return backend.result, true, backend.err
}

func (backend *managedRecoveryAuthorizationBackendFake) Authorize(
	context.Context,
	recovery.RecoveryAuthorizationRequest,
) (recovery.RecoveryAuthorizationResult, error) {
	return backend.result, backend.err
}

type managedRecoveryResultBackendFake struct {
	authorizations atomic.Int32
	opens          atomic.Int32
}

func (backend *managedRecoveryResultBackendFake) AuthorizeRecoveryResult(
	context.Context,
	content.DeliveryActor,
	content.RecoveryResultRef,
	content.DeliveryAction,
) (content.AuthorizedRecoveryResult, error) {
	backend.authorizations.Add(1)
	return content.AuthorizedRecoveryResult{}, nil
}

func (*managedRecoveryResultBackendFake) ReauthorizeRecoveryResult(
	context.Context,
	content.DeliveryActor,
	content.AuthorizedRecoveryResult,
	content.DeliveryAction,
) error {
	return nil
}

func (backend *managedRecoveryResultBackendFake) OpenRecoveryResultSource(
	context.Context,
	content.RecoveryResultSourceRequest,
) (content.SourceSession, error) {
	backend.opens.Add(1)
	return nil, nil
}

type managedRecoveryDowngradeReconcilerFake struct {
	result recovery.RecoveryReconciliationResult
	err    error
	calls  atomic.Int32
}

func (reconciler *managedRecoveryDowngradeReconcilerFake) ReconcileDowngradeReadiness(
	context.Context,
	recovery.RecoveryDowngradeReconciliationRequest,
) (recovery.RecoveryReconciliationResult, error) {
	reconciler.calls.Add(1)
	return reconciler.result, reconciler.err
}

type managedRecoveryTimerFake struct {
	mu        sync.Mutex
	ticks     chan time.Time
	resets    []time.Duration
	stopCalls int
}

type managedRecoveryTimerFactoryFake struct {
	mu     sync.Mutex
	timers []*managedRecoveryTimerFake
}

type managedRecoveryExpiredTimerFake struct {
	ticks     chan time.Time
	stopCalls atomic.Int32
}

func (timer *managedRecoveryExpiredTimerFake) Chan() <-chan time.Time {
	return timer.ticks
}

func (*managedRecoveryExpiredTimerFake) Reset(time.Duration) bool {
	return false
}

func (timer *managedRecoveryExpiredTimerFake) Stop() bool {
	timer.stopCalls.Add(1)
	return false
}

func waitManagedRecoveryExpiredTimerStops(
	t *testing.T,
	timer *managedRecoveryExpiredTimerFake,
	want int32,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if timer.stopCalls.Load() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("retry timer Stop calls=%d, want at least %d", timer.stopCalls.Load(), want)
}

func newManagedRecoveryTimerFake() *managedRecoveryTimerFake {
	return &managedRecoveryTimerFake{ticks: make(chan time.Time, 1)}
}

func (factory *managedRecoveryTimerFactoryFake) New(time.Duration) managedRecoveryTimer {
	timer := newManagedRecoveryTimerFake()
	factory.mu.Lock()
	factory.timers = append(factory.timers, timer)
	factory.mu.Unlock()
	return timer
}

func (factory *managedRecoveryTimerFactoryFake) waitForCount(
	t *testing.T,
	want int,
) []*managedRecoveryTimerFake {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		factory.mu.Lock()
		timers := append([]*managedRecoveryTimerFake(nil), factory.timers...)
		factory.mu.Unlock()
		if len(timers) >= want {
			return timers
		}
		time.Sleep(time.Millisecond)
	}
	factory.mu.Lock()
	timers := append([]*managedRecoveryTimerFake(nil), factory.timers...)
	factory.mu.Unlock()
	t.Fatalf("managed Recovery timers=%d, want %d independent owners", len(timers), want)
	return nil
}

func waitManagedRecoveryReconciliationCalls(
	t *testing.T,
	reconciler *managedRecoveryDowngradeReconcilerFake,
	want int32,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if reconciler.calls.Load() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("disabled reconciliation calls=%d, want at least %d", reconciler.calls.Load(), want)
}

func (timer *managedRecoveryTimerFake) Chan() <-chan time.Time {
	return timer.ticks
}

func (timer *managedRecoveryTimerFake) Reset(duration time.Duration) bool {
	timer.mu.Lock()
	defer timer.mu.Unlock()
	timer.resets = append(timer.resets, duration)
	return true
}

func (timer *managedRecoveryTimerFake) Stop() bool {
	timer.mu.Lock()
	defer timer.mu.Unlock()
	timer.stopCalls++
	return true
}

func (timer *managedRecoveryTimerFake) resetsSnapshot() []time.Duration {
	timer.mu.Lock()
	defer timer.mu.Unlock()
	return append([]time.Duration(nil), timer.resets...)
}

func (timer *managedRecoveryTimerFake) stopCallsSnapshot() int {
	timer.mu.Lock()
	defer timer.mu.Unlock()
	return timer.stopCalls
}

func waitManagedRecoveryTimerResets(t *testing.T, timer *managedRecoveryTimerFake, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(timer.resetsSnapshot()) >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("takeover timer resets=%v, want at least %d", timer.resetsSnapshot(), want)
}

func waitManagedRecoveryTimerStops(t *testing.T, timer *managedRecoveryTimerFake, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if timer.stopCallsSnapshot() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("retry timer Stop calls=%d, want at least %d", timer.stopCallsSnapshot(), want)
}

func (coordinator *managedRecoveryWorkerCoordinatorFake) ClaimNext(context.Context, string) (recovery.RecoveryWorkerClaim, bool, error) {
	select {
	case coordinator.claims <- struct{}{}:
	default:
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	found := coordinator.claimFound
	coordinator.claimFound = false
	return coordinator.claim, found, nil
}

func (coordinator *managedRecoveryWorkerCoordinatorFake) TakeoverExpired(context.Context, string) (recovery.RecoveryWorkerClaim, bool, error) {
	select {
	case coordinator.takeovers <- struct{}{}:
	default:
	}
	return recovery.RecoveryWorkerClaim{}, false, nil
}

func (*managedRecoveryWorkerCoordinatorFake) Heartbeat(
	_ context.Context,
	claim recovery.RecoveryWorkerClaim,
) (recovery.RecoveryWorkerClaim, error) {
	return managedRecoveryHeartbeatForTest(claim)
}

func (*managedRecoveryWorkerCoordinatorFake) CancelJob(context.Context, string) error { return nil }

func (coordinator *managedRecoveryWorkerCoordinatorFake) ExecuteResolvedClaim(
	_ context.Context,
	claim recovery.RecoveryWorkerClaim,
) error {
	coordinator.executions <- claim
	return nil
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
