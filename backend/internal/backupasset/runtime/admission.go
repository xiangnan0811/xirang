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
	"xirang/backend/internal/model"
	"xirang/backend/internal/task"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mattn/go-sqlite3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrAdmissionStopped = errors.New("backup asset admission is stopping")

const nodeWriteAdmissionRetryAttempts = 8

var errNodeWriteBoundaryMissing = errors.New("node write boundary missing")

// NodeWriteCoordinator owns the durable exclusion boundary shared by ordinary
// Task writes and Recovery node leases. Both methods operate only through the
// caller's transaction so the caller's TaskRun or lease insert commits or rolls
// back with admission.
type NodeWriteCoordinator struct {
	db            *gorm.DB
	retryAttempts int
	retryWait     func(context.Context, int) error
	now           func() time.Time
}

func NewNodeWriteCoordinator(db *gorm.DB) (*NodeWriteCoordinator, error) {
	if db == nil || db.Dialector == nil {
		return nil, task.ErrNodeWriteUnavailable
	}
	switch db.Name() {
	case "sqlite", "postgres":
	default:
		return nil, task.ErrNodeWriteUnavailable
	}
	return &NodeWriteCoordinator{
		db:            db,
		retryAttempts: nodeWriteAdmissionRetryAttempts,
		retryWait:     waitForNodeWriteAdmissionRetry,
		now:           time.Now,
	}, nil
}

func (coordinator *NodeWriteCoordinator) AdmitTaskTx(ctx context.Context, tx *gorm.DB, nodeID uint) error {
	if err := coordinator.validateCaller(ctx, tx, nodeID); err != nil {
		return err
	}
	ctx = nonNilNodeWriteContext(ctx)
	if err := coordinator.lockNodeBoundary(ctx, tx, nodeID); err != nil {
		return err
	}
	active, err := coordinator.nodeHasActiveRecoveryLease(ctx, tx, nodeID)
	if err != nil {
		return safeNodeWriteDatabaseError(ctx)
	}
	if active {
		return task.ErrNodeWriteConflict
	}
	return nil
}

// EnterTaskExecutionTx is the sole executor-entry authority for ordinary and
// legacy restore TaskRuns. It locks the immutable snapshot node, rechecks the
// Recovery lease, then starts exactly one still-pending run with a CAS.
func (coordinator *NodeWriteCoordinator) EnterTaskExecutionTx(
	ctx context.Context,
	tx *gorm.DB,
	runID uint,
	expectedNodeID uint,
	startedAt time.Time,
) error {
	if err := coordinator.validateCaller(ctx, tx, expectedNodeID); err != nil || runID == 0 {
		if err != nil {
			return err
		}
		return task.ErrNodeWriteStartLost
	}
	ctx = nonNilNodeWriteContext(ctx)

	var runSnapshot struct {
		NodeIDSnapshot uint
	}
	result := tx.WithContext(ctx).Model(&model.TaskRun{}).
		Select("node_id_snapshot").Where("id = ?", runID).Limit(1).Find(&runSnapshot)
	if result.Error != nil {
		return safeNodeWriteDatabaseError(ctx)
	}
	if result.RowsAffected != 1 || runSnapshot.NodeIDSnapshot == 0 {
		return task.ErrNodeWriteStartLost
	}
	if err := coordinator.lockNodeBoundary(ctx, tx, runSnapshot.NodeIDSnapshot); err != nil {
		return err
	}
	active, err := coordinator.nodeHasActiveRecoveryLease(ctx, tx, runSnapshot.NodeIDSnapshot)
	if err != nil {
		return safeNodeWriteDatabaseError(ctx)
	}
	if active {
		return task.ErrNodeWriteConflict
	}
	if runSnapshot.NodeIDSnapshot != expectedNodeID {
		return task.ErrNodeWriteStartLost
	}

	result = tx.WithContext(ctx).Model(&model.TaskRun{}).
		Where("id = ? AND node_id_snapshot = ? AND status = ?", runID, expectedNodeID, "pending").
		Updates(map[string]interface{}{"status": "running", "started_at": &startedAt})
	if result.Error != nil {
		return safeNodeWriteDatabaseError(ctx)
	}
	if result.RowsAffected != 1 {
		return task.ErrNodeWriteStartLost
	}
	return nil
}

// AdmitRecoveryTx is the caller-owned transaction seam used by both Recovery
// job and cleanup lease claims. The caller inserts its holder-specific lease
// only after this method succeeds and before committing the same transaction.
func (coordinator *NodeWriteCoordinator) AdmitRecoveryTx(ctx context.Context, tx *gorm.DB, nodeID uint) error {
	if err := coordinator.validateCaller(ctx, tx, nodeID); err != nil {
		return err
	}
	ctx = nonNilNodeWriteContext(ctx)
	if err := coordinator.lockNodeBoundary(ctx, tx, nodeID); err != nil {
		return err
	}
	active, err := coordinator.nodeHasActiveRecoveryLease(ctx, tx, nodeID)
	if err != nil {
		return safeNodeWriteDatabaseError(ctx)
	}
	if active {
		return task.ErrNodeWriteConflict
	}
	var activeTaskRuns int64
	err = tx.WithContext(ctx).Model(&model.TaskRun{}).
		Where("node_id_snapshot = ? AND status IN ?", nodeID, []string{"pending", "running"}).
		Count(&activeTaskRuns).Error
	if err != nil {
		return safeNodeWriteDatabaseError(ctx)
	}
	if activeTaskRuns > 0 {
		return task.ErrNodeWriteConflict
	}
	return nil
}

func (coordinator *NodeWriteCoordinator) validateCaller(ctx context.Context, tx *gorm.DB, nodeID uint) error {
	if coordinator == nil || coordinator.db == nil || tx == nil || tx.Error != nil || nodeID == 0 {
		return task.ErrNodeWriteUnavailable
	}
	if err := nonNilNodeWriteContext(ctx).Err(); err != nil {
		return err
	}
	return nil
}

func (coordinator *NodeWriteCoordinator) lockNodeBoundary(ctx context.Context, tx *gorm.DB, nodeID uint) error {
	attempts := coordinator.retryAttempts
	if attempts <= 0 {
		attempts = nodeWriteAdmissionRetryAttempts
	}
	wait := coordinator.retryWait
	if wait == nil {
		wait = waitForNodeWriteAdmissionRetry
	}
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := lockNodeBoundaryOnce(ctx, tx, nodeID)
		if err == nil {
			return nil
		}
		if !retryableNodeWriteDatabaseError(err) {
			return safeNodeWriteDatabaseError(ctx)
		}
		if attempt+1 == attempts {
			break
		}
		if err := wait(ctx, attempt); err != nil {
			return err
		}
	}
	return task.ErrNodeWriteUnavailable
}

func lockNodeBoundaryOnce(ctx context.Context, tx *gorm.DB, nodeID uint) error {
	switch tx.Name() {
	case "postgres":
		var nodeBoundary struct{ ID uint }
		result := tx.WithContext(ctx).Model(&model.Node{}).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id").Where("id = ?", nodeID).Take(&nodeBoundary)
		return result.Error
	case "sqlite":
		// SQLite ignores SELECT ... FOR UPDATE. A no-op write acquires its
		// transaction-wide write reservation before either conflict query.
		result := tx.WithContext(ctx).Exec("UPDATE nodes SET id = id WHERE id = ?", nodeID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errNodeWriteBoundaryMissing
		}
		return nil
	default:
		return errNodeWriteBoundaryMissing
	}
}

func (coordinator *NodeWriteCoordinator) nodeHasActiveRecoveryLease(
	ctx context.Context,
	tx *gorm.DB,
	nodeID uint,
) (bool, error) {
	now := coordinator.currentTime()
	result := tx.WithContext(ctx).Session(&gorm.Session{SkipDefaultTransaction: true}).
		Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("node_id = ? AND state = ? AND lease_expires_at <= ?", nodeID, "active", now).
		Updates(map[string]interface{}{
			"state":       "expired",
			"released_at": now,
			"updated_at":  now,
		})
	if result.Error != nil {
		return false, result.Error
	}

	var activeLeases int64
	err := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("node_id = ? AND state = ? AND lease_expires_at > ?", nodeID, "active", now).
		Count(&activeLeases).Error
	return activeLeases > 0, err
}

func (coordinator *NodeWriteCoordinator) currentTime() time.Time {
	if coordinator != nil && coordinator.now != nil {
		return coordinator.now().UTC()
	}
	return time.Now().UTC()
}

func retryableNodeWriteDatabaseError(err error) bool {
	var sqliteError sqlite3.Error
	if errors.As(err, &sqliteError) {
		return sqliteError.Code == sqlite3.ErrBusy || sqliteError.Code == sqlite3.ErrLocked
	}
	var sqliteCode sqlite3.ErrNo
	if errors.As(err, &sqliteCode) {
		return sqliteCode == sqlite3.ErrBusy || sqliteCode == sqlite3.ErrLocked
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		return postgresError.Code == "40001" || postgresError.Code == "40P01" || postgresError.Code == "55P03"
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "database table is locked")
}

func safeNodeWriteDatabaseError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return task.ErrNodeWriteUnavailable
}

func waitForNodeWriteAdmissionRetry(ctx context.Context, attempt int) error {
	delay := time.Duration(attempt+1) * 5 * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nonNilNodeWriteContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

type admissionBarrier struct {
	mu            sync.Mutex
	mode          publication.AdmissionMode
	generation    uint64
	active        int
	transitioning bool
	stopping      bool
	changed       chan struct{}
}

func newAdmissionBarrier(mode publication.AdmissionMode) (*admissionBarrier, error) {
	if err := publication.ValidateAdmissionMode(mode); err != nil {
		return nil, err
	}
	return &admissionBarrier{mode: mode, generation: 1, changed: make(chan struct{})}, nil
}

func (barrier *admissionBarrier) Acquire(ctx context.Context, operation publication.ResticOperation) (publication.AdmissionToken, error) {
	if barrier == nil {
		return nil, fmt.Errorf("%w: admission barrier is unavailable", backupasset.ErrInvalidState)
	}
	if err := publication.ValidateResticOperation(operation); err != nil {
		return nil, err
	}
	barrier.mu.Lock()
	defer barrier.mu.Unlock()
	for barrier.transitioning {
		if err := barrier.waitLocked(ctx); err != nil {
			return nil, err
		}
	}
	if barrier.stopping {
		return nil, fmt.Errorf("%w: new %s token rejected", ErrAdmissionStopped, operation)
	}
	barrier.active++
	return &admissionToken{
		barrier:    barrier,
		mode:       barrier.mode,
		generation: barrier.generation,
		operation:  operation,
	}, nil
}

func (barrier *admissionBarrier) transition(ctx context.Context, target publication.AdmissionMode, persist func() error) error {
	return barrier.transitionResolve(ctx, func() (publication.AdmissionMode, error) { return target, nil }, persist)
}

// transitionResolve keeps admission closed while it drains the current
// generation, rechecks safety state, persists the transition, and only then
// publishes a new immutable mode/generation to subsequent tokens.
func (barrier *admissionBarrier) transitionResolve(ctx context.Context, resolveTarget func() (publication.AdmissionMode, error), persist func() error) error {
	if barrier == nil || resolveTarget == nil || persist == nil {
		return fmt.Errorf("%w: admission transition is unavailable", backupasset.ErrInvalidState)
	}
	if err := barrier.beginTransition(ctx); err != nil {
		return err
	}
	target, err := resolveTarget()
	if err == nil {
		err = publication.ValidateAdmissionMode(target)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = persist()
	}
	barrier.finishTransition(target, err == nil)
	return err
}

func (barrier *admissionBarrier) beginTransition(ctx context.Context) error {
	barrier.mu.Lock()
	for barrier.transitioning {
		if err := barrier.waitLocked(ctx); err != nil {
			barrier.mu.Unlock()
			return err
		}
	}
	if barrier.stopping {
		barrier.mu.Unlock()
		return fmt.Errorf("%w: transition rejected", ErrAdmissionStopped)
	}
	barrier.transitioning = true
	barrier.signalLocked()
	for barrier.active > 0 {
		if err := barrier.waitLocked(ctx); err != nil {
			barrier.transitioning = false
			barrier.signalLocked()
			barrier.mu.Unlock()
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		barrier.transitioning = false
		barrier.signalLocked()
		barrier.mu.Unlock()
		return err
	}
	barrier.mu.Unlock()
	return nil
}

func (barrier *admissionBarrier) finishTransition(target publication.AdmissionMode, apply bool) {
	barrier.mu.Lock()
	if apply {
		barrier.mode = target
		barrier.generation++
	}
	barrier.transitioning = false
	barrier.signalLocked()
	barrier.mu.Unlock()
}

func (barrier *admissionBarrier) stop(ctx context.Context) error {
	if barrier == nil {
		return fmt.Errorf("%w: admission barrier is unavailable", backupasset.ErrInvalidState)
	}
	barrier.mu.Lock()
	for barrier.transitioning {
		if err := barrier.waitLocked(ctx); err != nil {
			barrier.mu.Unlock()
			return err
		}
	}
	barrier.stopping = true
	barrier.signalLocked()
	for barrier.active > 0 {
		if err := barrier.waitLocked(ctx); err != nil {
			barrier.mu.Unlock()
			return err
		}
	}
	barrier.mu.Unlock()
	return nil
}

// stopAccepting closes the admission gate immediately but deliberately does
// not wait for existing command lifecycles. Runtime shutdown performs that
// bounded drain later through stop(ctx), after HTTP producers are stopped.
func (barrier *admissionBarrier) stopAccepting() {
	if barrier == nil {
		return
	}
	barrier.mu.Lock()
	if !barrier.stopping {
		barrier.stopping = true
		barrier.signalLocked()
	}
	barrier.mu.Unlock()
}

func (barrier *admissionBarrier) waitLocked(ctx context.Context) error {
	changed := barrier.changed
	barrier.mu.Unlock()
	select {
	case <-ctx.Done():
		barrier.mu.Lock()
		return ctx.Err()
	case <-changed:
		barrier.mu.Lock()
		return nil
	}
}

func (barrier *admissionBarrier) signalLocked() {
	close(barrier.changed)
	barrier.changed = make(chan struct{})
}

func (barrier *admissionBarrier) closeToken() {
	barrier.mu.Lock()
	defer barrier.mu.Unlock()
	if barrier.active == 0 {
		return
	}
	barrier.active--
	barrier.signalLocked()
}

func (barrier *admissionBarrier) current() (publication.AdmissionMode, uint64) {
	barrier.mu.Lock()
	defer barrier.mu.Unlock()
	return barrier.mode, barrier.generation
}

func (barrier *admissionBarrier) activeCount() int {
	barrier.mu.Lock()
	defer barrier.mu.Unlock()
	return barrier.active
}

func (barrier *admissionBarrier) isStopping() bool {
	barrier.mu.Lock()
	defer barrier.mu.Unlock()
	return barrier.stopping
}

type admissionToken struct {
	barrier    *admissionBarrier
	mode       publication.AdmissionMode
	generation uint64
	operation  publication.ResticOperation
	once       sync.Once
}

func (token *admissionToken) Generation() uint64 { return token.generation }
func (token *admissionToken) Mode() publication.AdmissionMode {
	return token.mode
}
func (token *admissionToken) Operation() publication.ResticOperation { return token.operation }
func (token *admissionToken) Close() error {
	if token == nil || token.barrier == nil {
		return nil
	}
	token.once.Do(token.barrier.closeToken)
	return nil
}

var _ publication.Admission = (*admissionBarrier)(nil)
var _ publication.AdmissionToken = (*admissionToken)(nil)
