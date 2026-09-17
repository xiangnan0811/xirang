package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/backuphealth"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

const publicationWakeBuffer = 1000

// ManagedCompletionRecorder is the narrow durable handoff from publication
// to backup-health evidence. Record is called after a committed point is
// observed; Replay is called on every bounded worker pass so an interrupted
// handoff is repaired without requiring a process restart.
type ManagedCompletionRecorder interface {
	RecordManagedCommitted(context.Context, string) error
	ReplayManagedCommitted(context.Context, int) error
}

type managedCompletionStore struct {
	db *gorm.DB
}

func newManagedCompletionStore(db *gorm.DB) (*managedCompletionStore, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: managed completion recorder database is unavailable", backupasset.ErrInvalidState)
	}
	return &managedCompletionStore{db: db}, nil
}

func (store *managedCompletionStore) RecordManagedCommitted(ctx context.Context, pointID string) error {
	if store == nil || store.db == nil {
		return fmt.Errorf("%w: managed completion recorder database is unavailable", backupasset.ErrInvalidState)
	}
	if backupasset.ValidateOpaqueID(pointID) != nil {
		return fmt.Errorf("%w: managed completion point identity is unverified", backuphealth.ErrUnverifiedManagedCompletion)
	}
	input, err := store.managedCompletionInput(ctx, pointID)
	if err != nil {
		return err
	}
	return backuphealth.RecordManagedCommitted(ctx, store.db, input)
}

func (store *managedCompletionStore) ReplayManagedCommitted(ctx context.Context, limit int) error {
	if store == nil || store.db == nil || limit <= 0 {
		return fmt.Errorf("%w: invalid managed completion replay request", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var pointIDs []string
	query := store.db.WithContext(ctx).Table("recovery_points AS point").
		Select("point.id").
		Joins(`LEFT JOIN backup_completions AS completion
			ON completion.evidence_ref = point.id
			AND completion.evidence_status IN ?`,
			[]string{
				model.BackupCompletionEvidenceVerified,
				model.BackupCompletionEvidenceUnverified,
			}).
		Where("point.state = ? AND point.semantics IN ? AND completion.id IS NULL",
			backupasset.RecoveryPointCommitted,
			[]backupasset.PointVersionSemantics{
				backupasset.PointNativeSnapshot,
				backupasset.PointXirangManifest,
				backupasset.PointImportedBaseline,
			}).
		Order("COALESCE(point.committed_at, point.captured_at, point.updated_at, point.created_at) ASC, point.id ASC").
		Limit(limit).
		Pluck("point.id", &pointIDs)
	if query.Error != nil {
		return fmt.Errorf("list managed completion replay points: %w", query.Error)
	}
	var replayErr error
	for _, pointID := range pointIDs {
		err := store.RecordManagedCommitted(ctx, pointID)
		if err == nil {
			continue
		}
		if errors.Is(err, backuphealth.ErrUnverifiedManagedCompletion) {
			if markerErr := store.markManagedUnverified(ctx, pointID); markerErr != nil && replayErr == nil {
				replayErr = markerErr
			}
			continue
		}
		if replayErr == nil {
			replayErr = fmt.Errorf("replay managed completion %s: %w", pointID, err)
		}
	}
	return replayErr
}

func (store *managedCompletionStore) markManagedUnverified(ctx context.Context, pointID string) error {
	var point struct {
		ProducingNodeIDSnapshot uint
		CreatedAt               time.Time
		UpdatedAt               time.Time
	}
	query := store.db.WithContext(ctx).Model(&model.RecoveryPoint{}).
		Select("producing_node_id_snapshot", "created_at", "updated_at").
		Where("id = ?", pointID).Take(&point)
	if errors.Is(query.Error, gorm.ErrRecordNotFound) {
		// A point deleted between the replay scan and this read no longer
		// requires a marker; it cannot become a future health fact.
		return nil
	}
	if query.Error != nil {
		return fmt.Errorf("load unverified managed completion point: %w", query.Error)
	}
	markedAt := point.UpdatedAt
	if markedAt.IsZero() {
		markedAt = point.CreatedAt
	}
	if err := backuphealth.RecordManagedUnverified(ctx, store.db, point.ProducingNodeIDSnapshot, pointID, markedAt); err != nil {
		return fmt.Errorf("record unverified managed completion %s: %w", pointID, err)
	}
	return nil
}

func (store *managedCompletionStore) managedCompletionInput(ctx context.Context, pointID string) (backuphealth.ManagedCommittedInput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	unverified := func(reason string) (backuphealth.ManagedCommittedInput, error) {
		return backuphealth.ManagedCommittedInput{}, fmt.Errorf("%w: %s", backuphealth.ErrUnverifiedManagedCompletion, reason)
	}
	var point model.RecoveryPoint
	if err := store.db.WithContext(ctx).Select(
		"id", "repository_id", "producing_task_id", "producing_task_run_id", "producing_node_id_snapshot",
		"lineage_json", "consistency_json", "semantics", "state", "committed_at",
	).Where("id = ?", pointID).Take(&point).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return unverified("managed completion point is unavailable")
		}
		return backuphealth.ManagedCommittedInput{}, fmt.Errorf("load managed completion point: %w", err)
	}
	if point.State != string(backupasset.RecoveryPointCommitted) {
		return unverified("managed completion point is not committed")
	}
	semantics := backupasset.PointVersionSemantics(point.Semantics)
	if semantics != backupasset.PointNativeSnapshot && semantics != backupasset.PointXirangManifest {
		return unverified("imported or mutable point cannot produce a new managed completion")
	}
	if point.CommittedAt == nil || point.CommittedAt.IsZero() {
		return unverified("managed completion commit timestamp is unavailable")
	}
	if !model.IsTaskRunNodeSnapshotAuthoritative(point.ProducingNodeIDSnapshot) {
		return unverified("managed completion node snapshot is unavailable")
	}

	lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
	if err != nil {
		return unverified("managed completion lineage is unavailable")
	}
	taskID, taskRunID := lineage.TaskID, lineage.TaskRunID
	if point.ProducingTaskID != nil {
		if *point.ProducingTaskID == 0 || *point.ProducingTaskID != lineage.TaskID {
			return unverified("managed completion task lineage changed")
		}
		taskID = *point.ProducingTaskID
	}
	if point.ProducingTaskRunID != nil {
		if *point.ProducingTaskRunID == 0 || *point.ProducingTaskRunID != lineage.TaskRunID {
			return unverified("managed completion TaskRun lineage changed")
		}
		taskRunID = *point.ProducingTaskRunID
	}
	if strings.EqualFold(strings.TrimSpace(lineage.Trigger), "restore") ||
		strings.EqualFold(strings.TrimSpace(lineage.Trigger), "drill") {
		return unverified("recovery and drill lineage cannot produce backup completion")
	}
	var expectedExecutor backupasset.ProviderKind
	switch semantics {
	case backupasset.PointNativeSnapshot:
		if lineage.PublicationMode != string(backupasset.PublicationNativeSnapshot) {
			return unverified("native point lineage mode is invalid")
		}
		expectedExecutor = backupasset.ProviderRestic
	case backupasset.PointXirangManifest:
		switch backupasset.TaskPublicationMode(lineage.PublicationMode) {
		case backupasset.PublicationVersionedHardlink, backupasset.PublicationVersionedFullCopy:
			expectedExecutor = backupasset.ProviderRsync
		case backupasset.PublicationVersionedPrefix, backupasset.PublicationNativeObjectVersions:
			expectedExecutor = backupasset.ProviderRclone
		default:
			return unverified("managed tree point lineage mode is invalid")
		}
	}
	var repository model.BackupRepository
	if err := store.db.WithContext(ctx).Select("id", "provider_kind").
		Where("id = ?", point.RepositoryID).Take(&repository).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return unverified("managed completion repository identity is unavailable")
		}
		return backuphealth.ManagedCommittedInput{}, fmt.Errorf("load managed completion repository: %w", err)
	}
	executor := strings.ToLower(strings.TrimSpace(repository.ProviderKind))
	if executor != string(expectedExecutor) {
		return unverified("managed point provider does not match immutable lineage")
	}
	if consistency := strings.TrimSpace(point.ConsistencyJSON); consistency != "" && consistency != "{}" {
		proof, proofErr := backupasset.DecodePublicationConsistency(consistency)
		if proofErr != nil {
			return unverified("managed completion provider proof is unavailable")
		}
		if proof.Provider != "" && strings.ToLower(strings.TrimSpace(string(proof.Provider))) != executor {
			return unverified("managed completion provider proof changed")
		}
	}
	return backuphealth.ManagedCommittedInput{
		TaskID: taskID, TaskRunID: taskRunID, NodeID: point.ProducingNodeIDSnapshot,
		ExecutorType: executor, RecoveryPointID: point.ID, CommittedAt: point.CommittedAt.UTC(),
	}, nil
}

type PublicationWorkerDependencies struct {
	Foundation *backupasset.FoundationService
	Reconciler publication.Reconciler
	Completion ManagedCompletionRecorder
	Observer   publication.CommitObserver
	Reporter   publication.InterruptedRunReporter
	Metrics    publication.Metrics
	Now        func() time.Time
}

// PublicationWorker consumes the durable preparing/verifying queue. Wakeups
// improve latency only; losing one is safe because periodic candidate scans
// always re-read the database queue.
type PublicationWorker struct {
	foundation *backupasset.FoundationService
	reconciler publication.Reconciler
	completion ManagedCompletionRecorder
	observer   publication.CommitObserver
	reporter   publication.InterruptedRunReporter
	metrics    publication.Metrics
	now        func() time.Time

	mu       sync.Mutex
	stopping bool
	active   map[string]context.CancelFunc
	observed map[string]struct{}
	running  int
	changed  chan struct{}
	wake     chan string
	stop     chan struct{}
	wg       sync.WaitGroup
}

func NewPublicationWorker(dependencies PublicationWorkerDependencies) (*PublicationWorker, error) {
	if dependencies.Foundation == nil || dependencies.Reconciler == nil || dependencies.Completion == nil || dependencies.Metrics == nil {
		return nil, fmt.Errorf("%w: publication worker dependencies are unavailable", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &PublicationWorker{
		foundation: dependencies.Foundation,
		reconciler: dependencies.Reconciler,
		completion: dependencies.Completion,
		observer:   dependencies.Observer,
		reporter:   dependencies.Reporter,
		metrics:    dependencies.Metrics,
		now:        dependencies.Now,
		active:     make(map[string]context.CancelFunc),
		observed:   make(map[string]struct{}),
		changed:    make(chan struct{}),
		wake:       make(chan string, publicationWakeBuffer),
		stop:       make(chan struct{}),
	}, nil
}

// StartupPass processes one bounded snapshot of candidates and waits for it.
// It is deliberately safe to call before Run starts and is used by runtime
// readiness before schedules become available.
func (worker *PublicationWorker) StartupPass(ctx context.Context) error {
	if worker == nil {
		return fmt.Errorf("%w: publication worker is unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := worker.requireRunning(); err != nil {
		return err
	}
	enabled, err := worker.foundation.FeatureEnabled()
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	config, err := worker.foundation.PublicationConfig()
	if err != nil {
		return err
	}
	var replayErr error
	if err := worker.completion.ReplayManagedCommitted(ctx, config.ReconcileBatchSize); err != nil {
		replayErr = err
	}
	candidates, err := worker.reconciler.ListCandidates(ctx, config.ReconcileBatchSize)
	if err != nil {
		return err
	}
	var batch sync.WaitGroup
	for _, pointID := range candidates {
		pointID := pointID
		batch.Add(1)
		go func() {
			defer batch.Done()
			worker.process(ctx, pointID)
		}()
	}
	batch.Wait()
	if replayErr != nil {
		return replayErr
	}
	return nil
}

// TryWake accepts an opaque point ID without ever blocking a TaskRun. The
// channel intentionally remains open for the worker lifetime so producers
// cannot race Shutdown with a close.
func (worker *PublicationWorker) TryWake(pointID string) bool {
	if worker == nil || backupasset.ValidateOpaqueID(pointID) != nil {
		return false
	}
	worker.mu.Lock()
	stopping := worker.stopping
	worker.mu.Unlock()
	if stopping {
		return false
	}
	select {
	case worker.wake <- pointID:
		return true
	default:
		return false
	}
}

// Run serves wakeups and dynamic periodic passes until its context ends or
// Shutdown marks the worker stopping. Each path reaches the same process gate.
func (worker *PublicationWorker) Run(ctx context.Context) {
	if worker == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	config, err := worker.foundation.PublicationConfig()
	if err != nil {
		return
	}
	worker.runLoop(ctx, config)
}

func (worker *PublicationWorker) runLoop(ctx context.Context, config backupasset.PublicationConfig) {
	timer := time.NewTimer(config.ReconcileInterval)
	defer timer.Stop()
	for {
		if worker.isStopping() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-worker.stop:
			return
		case pointID := <-worker.wake:
			go worker.process(ctx, pointID)
		case <-timer.C:
			_ = worker.StartupPass(ctx)
			nextConfig, err := worker.foundation.PublicationConfig()
			if err != nil {
				return
			}
			config = nextConfig
			timer.Reset(config.ReconcileInterval)
		}
	}
}

// Shutdown prevents new claims, cancels all in-flight work, and joins it by
// the caller's deadline. It never closes the public producer wake channel.
func (worker *PublicationWorker) Shutdown(ctx context.Context) error {
	if worker == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	worker.mu.Lock()
	if !worker.stopping {
		worker.stopping = true
		close(worker.stop)
		for _, cancel := range worker.active {
			cancel()
		}
		worker.signalLocked()
	}
	worker.mu.Unlock()
	done := make(chan struct{})
	go func() {
		worker.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (worker *PublicationWorker) process(parent context.Context, pointID string) {
	if backupasset.ValidateOpaqueID(pointID) != nil {
		return
	}
	enabled, err := worker.foundation.FeatureEnabled()
	if err != nil || !enabled {
		return
	}
	workCtx, cancel, ok := worker.beginPoint(parent, pointID)
	if !ok {
		return
	}
	defer worker.endPoint(pointID, cancel)
	if !worker.acquireSlot(workCtx) {
		return
	}
	defer worker.releaseSlot()
	if err := workCtx.Err(); err != nil {
		return
	}
	// Re-read the dynamic switch after waiting for a concurrency slot. A
	// feature transition may have drained another command while this candidate
	// was queued; it must not begin a new claim after disable.
	enabled, err = worker.foundation.FeatureEnabled()
	if err != nil || !enabled {
		return
	}
	outcome, err := worker.reconciler.ProcessPoint(workCtx, pointID)
	if err != nil || outcome.RecoveryPointID == "" {
		return
	}
	if outcome.State == backupasset.RecoveryPointCommitted {
		if err := worker.completion.RecordManagedCommitted(workCtx, outcome.RecoveryPointID); err != nil {
			return
		}
		if worker.markObserved(outcome.RecoveryPointID) && worker.observer != nil {
			worker.observer.ObserveCommitted(workCtx, outcome)
		}
	}
	if worker.reporter != nil {
		_ = worker.reporter.ReportInterruptedPublication(workCtx, outcome)
	}
}

func (worker *PublicationWorker) markObserved(pointID string) bool {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if _, exists := worker.observed[pointID]; exists {
		return false
	}
	worker.observed[pointID] = struct{}{}
	return true
}

func (worker *PublicationWorker) beginPoint(parent context.Context, pointID string) (context.Context, context.CancelFunc, bool) {
	if parent == nil {
		parent = context.Background()
	}
	workCtx, cancel := context.WithCancel(parent)
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.stopping {
		cancel()
		return nil, nil, false
	}
	if _, exists := worker.active[pointID]; exists {
		cancel()
		return nil, nil, false
	}
	worker.active[pointID] = cancel
	worker.wg.Add(1)
	return workCtx, cancel, true
}

func (worker *PublicationWorker) endPoint(pointID string, cancel context.CancelFunc) {
	cancel()
	worker.mu.Lock()
	delete(worker.active, pointID)
	worker.signalLocked()
	worker.mu.Unlock()
	worker.wg.Done()
}

func (worker *PublicationWorker) acquireSlot(ctx context.Context) bool {
	for {
		config, err := worker.foundation.PublicationConfig()
		if err != nil {
			return false
		}
		worker.mu.Lock()
		if worker.stopping {
			worker.mu.Unlock()
			return false
		}
		if worker.running < config.WorkerConcurrency {
			worker.running++
			worker.mu.Unlock()
			return true
		}
		changed := worker.changed
		worker.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-changed:
		}
	}
}

func (worker *PublicationWorker) releaseSlot() {
	worker.mu.Lock()
	if worker.running > 0 {
		worker.running--
	}
	worker.signalLocked()
	worker.mu.Unlock()
}

func (worker *PublicationWorker) requireRunning() error {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.stopping {
		return ErrAdmissionStopped
	}
	return nil
}

func (worker *PublicationWorker) isStopping() bool {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.stopping
}

func (worker *PublicationWorker) signalLocked() {
	close(worker.changed)
	worker.changed = make(chan struct{})
}

var _ interface {
	StartupPass(context.Context) error
	TryWake(string) bool
	Run(context.Context)
	Shutdown(context.Context) error
} = (*PublicationWorker)(nil)
