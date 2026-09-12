package task

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/anomaly"
	"xirang/backend/internal/automation"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/policy"
	gormrepo "xirang/backend/internal/repository/gorm"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/task/scheduler"
	"xirang/backend/internal/ws"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mattn/go-sqlite3"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// nextCronRun 根据 cron 表达式计算下一次执行时间。
// 如果表达式为空或无效，返回 nil。
func nextCronRun(spec string) *time.Time {
	if spec == "" {
		return nil
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	schedule, err := parser.Parse(spec)
	if err != nil {
		return nil
	}
	next := schedule.Next(time.Now())
	return &next
}

func nextCronRunAfter(spec string, after time.Time) *time.Time {
	if spec == "" {
		return nil
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	schedule, err := parser.Parse(spec)
	if err != nil {
		return nil
	}
	next := schedule.Next(after)
	if next.IsZero() {
		return nil
	}
	next = next.UTC()
	return &next
}

func normalizeCronOccurrence(occurrence *time.Time) *time.Time {
	if occurrence == nil || occurrence.IsZero() {
		return nil
	}
	normalized := occurrence.UTC()
	return &normalized
}

func isBackupTaskRunTrigger(triggerType string) bool {
	switch strings.ToLower(strings.TrimSpace(triggerType)) {
	case "restore", "drill":
		return false
	default:
		return true
	}
}

// isLegacyMutableTask identifies compatibility writers whose destination can
// be changed in place. This is deliberately broader than the Rsync manifest
// lane: legacy Rclone also writes a mutable remote, but has no source-side
// capture manifest.
func isLegacyMutableTask(task model.Task) bool {
	if strings.TrimSpace(task.RsyncSource) == "" || strings.TrimSpace(task.RsyncTarget) == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(task.ExecutorType)) {
	case "rsync":
		config, err := ParseRsyncPublicationConfigV1(task.ExecutorConfig)
		return err == nil && config.PublicationMode == backupasset.PublicationLegacyMutable
	case "rclone":
		config, err := ParseRcloneTaskConfigV1(task.ExecutorConfig)
		return err == nil && config.PublicationMode == backupasset.PublicationLegacyMutable
	default:
		return false
	}
}

// isLegacyMutableRsyncTask identifies the only compatibility writer for which
// source-side manifest capture is available.
func isLegacyMutableRsyncTask(task model.Task) bool {
	return isLegacyMutableTask(task) &&
		strings.EqualFold(strings.TrimSpace(task.ExecutorType), "rsync")
}

func isLegacyMutableRcloneTask(task model.Task) bool {
	return isLegacyMutableTask(task) &&
		strings.EqualFold(strings.TrimSpace(task.ExecutorType), "rclone")
}

func loadTaskNodeForFingerprint(tx *gorm.DB, task *model.Task) error {
	if tx == nil || task == nil || task.NodeID == 0 {
		return nil
	}
	var node model.Node
	result := tx.Select("id", "name", "host", "port", "username", "auth_type", "ssh_key_id", "backup_dir", "use_sudo").
		Where("id = ?", task.NodeID).Limit(1).Find(&node)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		task.Node = node
	} else {
		task.Node = model.Node{ID: task.NodeID}
	}
	return nil
}

// lockTaskPolicyForFingerprint acquires the policy row associated with a task
// before the caller locks that task. Policy control transactions use the same
// Policy -> Task ordering, so the lock acquisition order cannot deadlock with
// a concurrent policy update. A task without PolicyID deliberately returns a
// nil snapshot; a non-nil but missing policy is detected after the task row is
// locked and fails closed.
func lockTaskPolicyForFingerprint(tx *gorm.DB, taskID uint) (*model.Policy, error) {
	if tx == nil || taskID == 0 {
		return nil, fmt.Errorf("任务策略快照不可用")
	}
	var policyEntity model.Policy
	query := tx.Table("policies").
		Select("policies.*").
		Joins("JOIN tasks ON tasks.policy_id = policies.id").
		Where("tasks.id = ?", taskID).
		Limit(1)
	if tx.Name() == "postgres" {
		query = query.Clauses(clause.Locking{
			Strength: clause.LockingStrengthUpdate,
			Table:    clause.Table{Name: "policies"},
		})
	}
	result := query.Find(&policyEntity)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, nil
	}
	return &policyEntity, nil
}

// ErrPolicyConcurrencyLimit is returned when a policy has no execution slot
// available. Reservation callers use it as a busy refusal: no backup run is
// created, and an already-reserved run is canceled at the execution gate
// rather than recorded as a failed transfer.
var ErrPolicyConcurrencyLimit = errors.New("policy concurrency limit reached")

// ErrMutableGenerationUnresolved marks a legacy mutable target whose previous
// write did not reach an authoritative terminal outcome. Operators must
// reconcile the remote before another write is admitted; changing the Task
// configuration does not clear this durable hold.
var ErrMutableGenerationUnresolved = errors.New("mutable target generation unresolved")

func policyMaxConcurrent(policyEntity *model.Policy) int {
	if policyEntity == nil || policyEntity.MaxConcurrent <= 0 {
		return 1
	}
	return policyEntity.MaxConcurrent
}

// rejectUnresolvedMutableGenerationTx blocks every ordinary write while a
// Task has a durable writing/unknown generation. The generation is scoped to
// the Task rather than the current executor/config fingerprint: a config/target
// edit cannot make evidence from the previous mutable target disappear or
// silently authorize a mixed remote. Restore admission has its own provenance
// check and is intentionally not routed through this gate.
func rejectUnresolvedMutableGenerationTx(tx *gorm.DB, taskEntity *model.Task) error {
	if tx == nil || taskEntity == nil || taskEntity.ID == 0 {
		return nil
	}
	var unresolved int64
	result := tx.Model(&model.TaskRun{}).
		Where(`task_id = ? AND lower(COALESCE(trigger_type, '')) NOT IN ? AND
			TRIM(COALESCE(backup_generation_state, '')) IN ?`,
			taskEntity.ID, []string{"restore", "drill"},
			[]string{model.TaskRunGenerationStateWriting, model.TaskRunGenerationStateUnknown}).
		Count(&unresolved)
	if result.Error != nil {
		return fmt.Errorf("读取未决可变目标代际失败: %w", result.Error)
	}
	if unresolved > 0 {
		return fmt.Errorf("%w: 远端目标仍需人工核验后才能继续写入", ErrMutableGenerationUnresolved)
	}
	return nil
}

// countPolicyActiveRuns is evaluated only after the Policy row has been
// locked. Pending rows are durable reservations; running rows are admitted
// executors. Recovery and restore/drill rows are intentionally outside the
// ordinary policy quota.
func countPolicyActiveRuns(tx *gorm.DB, policyID, excludeRunID uint, statuses []string) (int64, error) {
	if tx == nil || policyID == 0 {
		return 0, nil
	}
	query := tx.Model(&model.TaskRun{}).
		Joins("JOIN tasks ON tasks.id = task_runs.task_id").
		Where("tasks.policy_id = ?", policyID).
		Where("lower(COALESCE(task_runs.trigger_type, '')) NOT IN ?", []string{"restore", "drill"}).
		Where("task_runs.status IN ?", statuses)
	if excludeRunID != 0 {
		query = query.Where("task_runs.id <> ?", excludeRunID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func reservePolicySlot(tx *gorm.DB, policyEntity *model.Policy) error {
	if policyEntity == nil {
		return nil
	}
	count, err := countPolicyActiveRuns(tx, policyEntity.ID, 0,
		[]string{model.TaskRunStatusPending, model.TaskRunStatusRunning})
	if err != nil {
		return err
	}
	if count >= int64(policyMaxConcurrent(policyEntity)) {
		return ErrPolicyConcurrencyLimit
	}
	return nil
}

func admitPolicySlot(tx *gorm.DB, policyEntity *model.Policy, runID uint) error {
	if policyEntity == nil {
		return nil
	}
	count, err := countPolicyActiveRuns(tx, policyEntity.ID, runID,
		[]string{model.TaskRunStatusRunning})
	if err != nil {
		return err
	}
	if count >= int64(policyMaxConcurrent(policyEntity)) {
		return ErrPolicyConcurrencyLimit
	}
	return nil
}

func isNoProcessStartGenerationError(err error) bool {
	var noStartErr *executor.NoProcessStartError
	return errors.As(err, &noStartErr)
}

// rcloneNoStartStateForRunTx derives a no-start proof only from a currently
// persisted pending run and its Task. A pending row has not entered provider
// execution; terminal/retrying rows remain ambiguous.
func rcloneNoStartStateForRunTx(tx *gorm.DB, run *model.TaskRun) (bool, error) {
	if run == nil || run.Status != model.TaskRunStatusPending {
		return false, nil
	}
	return deriveRcloneNoStartStateForRunTx(tx, run)
}

// rcloneNoStartStateForPreProviderRunTx is a separate proof for the runner's
// same-owner cancellation boundary after durable execution admission but
// before any provider invocation. It must only be called by that boundary;
// terminal rows and armed/unknown generations are never backfilled.
func rcloneNoStartStateForPreProviderRunTx(tx *gorm.DB, run *model.TaskRun) (bool, error) {
	if run == nil || run.Status != model.TaskRunStatusRunning {
		return false, nil
	}
	return deriveRcloneNoStartStateForRunTx(tx, run)
}

func deriveRcloneNoStartStateForRunTx(tx *gorm.DB, run *model.TaskRun) (bool, error) {
	if tx == nil || run == nil || run.ID == 0 ||
		strings.TrimSpace(run.BackupGenerationState) != "" ||
		!isBackupTaskRunTrigger(run.TriggerType) {
		return false, nil
	}
	var taskEntity model.Task
	result := tx.Select("id, executor_type, executor_config, rsync_source, rsync_target").
		Where("id = ?", run.TaskID).Limit(1).Find(&taskEntity)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, nil
	}
	return isLegacyMutableRcloneTask(taskEntity), nil
}

type mutableGenerationSelection struct {
	run        model.TaskRun
	allowEmpty bool
}

// latestRcloneMutableGeneration chooses the current Rclone mutable head. A
// no_start row is an explicit proof that no child process was launched, so it
// may be skipped. Empty historical rows without that durable proof remain the
// selected ambiguous head; status text and error wording are never evidence.
func latestRcloneMutableGeneration(tx *gorm.DB, taskID, nodeID uint) (mutableGenerationSelection, error) {
	base := tx.Where(`task_id = ? AND node_id_snapshot = ? AND
		lower(COALESCE(trigger_type, '')) NOT IN ?`,
		taskID, nodeID, []string{"restore", "drill"})
	var latest model.TaskRun
	result := base.Order("id DESC").Limit(1).Find(&latest)
	if result.Error != nil {
		return mutableGenerationSelection{}, result.Error
	}
	if result.RowsAffected != 1 {
		return mutableGenerationSelection{}, nil
	}
	candidate := latest
	if strings.TrimSpace(latest.BackupGenerationState) == model.TaskRunGenerationStateNoStart {
		// Skip every positively proven no-start row in one query. The newest
		// remaining row is either a usable non-empty generation or the first
		// ambiguous empty head; never scan past that empty row.
		var prior model.TaskRun
		priorResult := base.Where(
			"TRIM(COALESCE(backup_generation_state, '')) <> ?",
			model.TaskRunGenerationStateNoStart,
		).Order("id DESC").Limit(1).Find(&prior)
		if priorResult.Error != nil {
			return mutableGenerationSelection{}, priorResult.Error
		}
		if priorResult.RowsAffected != 1 {
			return mutableGenerationSelection{}, nil
		}
		candidate = prior
	}
	if strings.TrimSpace(candidate.BackupGenerationState) != "" {
		return mutableGenerationSelection{run: candidate}, nil
	}

	var prior model.TaskRun
	priorResult := base.Where(
		"id < ? AND TRIM(COALESCE(backup_generation_state, '')) <> '' AND TRIM(COALESCE(backup_generation_state, '')) <> ?",
		candidate.ID, model.TaskRunGenerationStateNoStart,
	).Order("id DESC").Limit(1).Find(&prior)
	hasPrior := priorResult.Error == nil && priorResult.RowsAffected == 1
	if priorResult.Error != nil {
		return mutableGenerationSelection{}, priorResult.Error
	}
	if candidate.Status == model.TaskRunStatusSuccess && !hasPrior {
		// This is an old, pre-generation-tracking successful Rclone run. It is
		// usable as a best-effort mutable head, but only when no newer
		// generation-tracked run exists.
		return mutableGenerationSelection{run: candidate, allowEmpty: true}, nil
	}
	return mutableGenerationSelection{run: candidate}, nil
}

func taskPolicySnapshotMatches(task model.Task, policyEntity *model.Policy) bool {
	if task.PolicyID == nil {
		return policyEntity == nil
	}
	return policyEntity != nil && policyEntity.ID == *task.PolicyID
}

// validateTaskTargetOwnershipTx is the final durable target gate. It runs
// before node-write admission and provider invocation, and deliberately reads
// every persisted local Rsync claim, including manual tasks without a Policy.
func validateTaskTargetOwnershipTx(tx *gorm.DB, taskEntity *model.Task) error {
	if tx == nil || taskEntity == nil ||
		!policy.IsCoreLocalTarget(taskEntity.ExecutorType, taskEntity.RsyncTarget) {
		return nil
	}
	var persisted []model.Task
	query := tx.Select("id, policy_id, node_id, executor_type, rsync_target").
		Where("id <> ?", taskEntity.ID)
	if err := query.Find(&persisted).Error; err != nil {
		return fmt.Errorf("读取任务目标占用声明失败: %w", err)
	}
	owner := policy.TargetOwner{
		NodeID: taskEntity.NodeID,
		TaskID: taskEntity.ID,
		Target: taskEntity.RsyncTarget,
	}
	if taskEntity.PolicyID != nil {
		owner.PolicyID = *taskEntity.PolicyID
	}
	existing := make([]policy.TargetOwner, 0, len(persisted))
	for _, persistedTask := range persisted {
		if !policy.IsCoreLocalTarget(persistedTask.ExecutorType, persistedTask.RsyncTarget) {
			continue
		}
		persistedOwner := policy.TargetOwner{
			NodeID: persistedTask.NodeID,
			TaskID: persistedTask.ID,
			Target: persistedTask.RsyncTarget,
		}
		if persistedTask.PolicyID != nil {
			persistedOwner.PolicyID = *persistedTask.PolicyID
		}
		existing = append(existing, persistedOwner)
	}
	if _, err := policy.ValidateTargetOwnership(owner.Target, owner, existing); err != nil {
		return fmt.Errorf("任务目标路径冲突: %w", err)
	}
	return nil
}

const (
	defaultLogQueueCapacity       = 1024
	defaultLogBatchSize           = 50
	defaultLogFlushInterval       = 500 * time.Millisecond
	defaultSampleQueueCapacity    = 1024
	defaultSampleBatchSize        = 50
	defaultSampleFlushInterval    = 500 * time.Millisecond
	defaultSampleThrottleWindow   = 10 * time.Second
	defaultSampleCleanupInterval  = time.Hour
	defaultSampleCleanupBatchSize = 500
	nodeWriteReservationAttempts  = 8
	defaultDrillRecoveryLease     = 30 * time.Second
	defaultDrillRecoveryInterval  = 5 * time.Second
)

var (
	ErrNodeWriteConflict            = errors.New("node write conflict")
	ErrNodeWriteUnavailable         = errors.New("node write admission unavailable")
	ErrNodeWriteStartLost           = errors.New("node write start compare-and-swap lost")
	ErrDrillUnavailable             = errors.New("恢复演练功能暂不可用")
	ErrDrillAlreadyActive           = errors.New("该任务正在执行中，请勿重复触发")
	ErrRestoreRequiresNewBackup     = errors.New("new-backup-required")
	errCronOccurrenceHandled        = errors.New("cron occurrence already handled")
	errTaskChainRunHandled          = errors.New("chain child already handled")
	errTaskChainBusy                = errors.New("chain task is busy")
	errTaskPolicyChanged            = errors.New("task policy changed during reservation")
	errTaskRunBackupBindingMismatch = errors.New("task run backup binding mismatch")
	errTaskCancelInProgress         = errors.New("任务取消操作正在进行，请稍候再试")
	errTaskCancelConflict           = errors.New("任务状态已变化，请重试")
	errTaskCancelUnavailable        = errors.New("取消任务失败，请稍后重试")
	errTaskCancelUnsupported        = errors.New("仅支持取消待执行、重试中或运行中的任务")
)

// NodeWriteAdmission serializes ordinary and Drill TaskRun lifecycles with
// durable Recovery node leases. The caller owns the transaction, so reservation
// and execution admission commit or roll back with the corresponding run state.
type NodeWriteAdmission interface {
	AdmitTaskTx(context.Context, *gorm.DB, uint) error
	EnterTaskExecutionTx(context.Context, *gorm.DB, uint, uint, time.Time) error
	AdmitDrillTx(context.Context, *gorm.DB, uint, uint) error
	EnterDrillExecutionTx(context.Context, *gorm.DB, uint, uint, time.Time) error
}
type BackupSourceCompletionObserver interface {
	ObserveBackupSourceCompletion(context.Context, uint) error
}

// ManagerOption configures a Manager during construction.
type ManagerOption func(*Manager)

// WithRunContextFactory configures the context used by trigger-owned TaskRun
// lifecycles. Production uses context.WithTimeout; tests can inject a
// deterministic deadline context at construction time.
func WithRunContextFactory(factory func(context.Context, time.Duration) (context.Context, context.CancelFunc)) ManagerOption {
	return func(manager *Manager) {
		if factory != nil {
			manager.runContextFactory = factory
		}
	}
}

// WithDrillRestoreFunc configures the restore transport used by recovery
// drills. Production intentionally leaves this unset until a secure
// credential-agent/relay transport is available; tests and future internal
// callers can inject an explicit transport when exercising the workflow.
func WithDrillRestoreFunc(restore func(context.Context, model.Task, model.Node, string, func(string, string)) error) ManagerOption {
	return func(manager *Manager) {
		if restore != nil {
			manager.drillRestoreFunc = restore
		}
	}
}

type pendingRunOwnership struct {
	mu                           sync.Mutex
	canceled                     bool
	cancels                      []context.CancelFunc
	cancelPersistenceStarted     chan struct{}
	cancelPersistenceStartedOnce sync.Once
	cancelSettled                chan struct{}
	cancelSettledOnce            sync.Once
	drillRecoveryRunID           atomic.Uint64
	drillRecoveryCleanupClaimed  atomic.Bool
}

// taskCancelTriggerBarrier occupies the same process-local ownership slot as a
// trigger while no-owner cancellation inspects and terminalizes durable state.
// Its distinct type prevents a concurrent Cancel from treating the barrier as
// a live runner.
type taskCancelTriggerBarrier struct{}

func (ownership *pendingRunOwnership) addCancel(cancel context.CancelFunc) {
	if ownership == nil || cancel == nil {
		return
	}
	ownership.mu.Lock()
	if !ownership.canceled {
		ownership.cancels = append(ownership.cancels, cancel)
		ownership.mu.Unlock()
		return
	}
	ownership.mu.Unlock()
	cancel()
}

func (ownership *pendingRunOwnership) cancel() {
	if ownership == nil {
		return
	}
	ownership.mu.Lock()
	if ownership.canceled {
		ownership.mu.Unlock()
		return
	}
	ownership.canceled = true
	cancels := append([]context.CancelFunc(nil), ownership.cancels...)
	ownership.cancels = nil
	ownership.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}
func (ownership *pendingRunOwnership) beginCancellationPersistence() {
	if ownership == nil || ownership.cancelPersistenceStarted == nil {
		return
	}
	ownership.cancelPersistenceStartedOnce.Do(func() { close(ownership.cancelPersistenceStarted) })
}

func (ownership *pendingRunOwnership) markCancellationPersistenceSettled() {
	if ownership == nil || ownership.cancelSettled == nil {
		return
	}
	ownership.cancelSettledOnce.Do(func() { close(ownership.cancelSettled) })
}

func (ownership *pendingRunOwnership) waitCancellationPersistence() {
	if ownership == nil || ownership.cancelPersistenceStarted == nil || ownership.cancelSettled == nil {
		return
	}
	select {
	case <-ownership.cancelPersistenceStarted:
		<-ownership.cancelSettled
	default:
	}
}

func (ownership *pendingRunOwnership) handoffDrillRecovery(runID uint) {
	if ownership == nil || runID == 0 {
		return
	}
	ownership.drillRecoveryRunID.Store(uint64(runID))
}

func (ownership *pendingRunOwnership) handedOffDrillRunID() (uint, bool) {
	if ownership == nil {
		return 0, false
	}
	runID := ownership.drillRecoveryRunID.Load()
	return uint(runID), runID != 0
}

func (ownership *pendingRunOwnership) claimDrillRecoveryCleanup(runID uint) bool {
	if ownership == nil || runID == 0 || ownership.drillRecoveryRunID.Load() != uint64(runID) {
		return false
	}
	return ownership.drillRecoveryCleanupClaimed.CompareAndSwap(false, true)
}

func generateChainRunID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type queuedTaskLog struct {
	taskID    uint
	taskRunID *uint
	level     string
	message   string
	status    string
}

type queuedTaskSample struct {
	taskID         uint
	nodeID         uint
	runStartedAt   time.Time
	sampledAt      time.Time
	throughputMbps float64
}

type Manager struct {
	db                            *gorm.DB
	nodeWriteAdmission            NodeWriteAdmission
	nodeWriteRetryWait            func(context.Context, int) error
	runContextFactory             func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	stateMachine                  *StateMachine
	executorFactory               executor.Factory
	hub                           *ws.Hub
	scheduler                     *scheduler.CronScheduler
	locks                         sync.Map
	strategyLocks                 sync.Map
	nodeLocks                     sync.Map                                                         // nodeID → *sync.Mutex, 节点级互斥（restore 与普通任务共享）
	hookRunFunc                   func(ctx context.Context, task model.Task, command string) error // 可测试注入
	beforeTaskRunReservation      func()                                                           // 可测试注入：普通触发读取任务后、持久化预约前
	afterTriggerRestoreLoad       func()                                                           // 可测试注入：归档检查与 run 预约之间
	afterLegacyRsyncGenerationArm func()                                                           // 可测试注入：Rsync 代际闩锁持久化后
	drillRestoreFunc              func(ctx context.Context, srcTask model.Task, sandboxNode model.Node, drillPath string, logf func(string, string)) error
	drillSSHScriptFunc            func(ctx context.Context, node model.Node, script string) error // 可测试注入
	ensureRemoteTargetReadyFunc   func(ctx context.Context, node model.Node, targetPath string) error
	pendingRuns                   sync.Map
	pendingRecoveryMu             sync.Mutex
	pendingRecoveryCursor         uint
	restoreNodes                  sync.Map // nodeID → taskID, 持续跟踪有活跃恢复任务的节点
	semaphore                     chan struct{}
	taskWG                        sync.WaitGroup
	// Sub-components extracted from the Manager god object.
	logDispatcher *LogDispatcher
	sampleWriter  *SampleWriter
	chainRunner   *ChainRunner

	taskRunRetentionDays int
	lastTaskRunCleanupAt time.Time
	taskRunCleanupMu     sync.Mutex

	settingsSvc *settings.Service

	alertDispatcher *alerting.Dispatcher

	anomalySink          anomaly.AlertSink // optional; set via SetAnomalySink
	exactAnomalyAnalyzer ExactAnomalyFunc

	autoDispatcher *automation.Dispatcher // optional; set via SetAutomationDispatcher
	scheduleMu     sync.Mutex

	publicationCoordinator         publication.Coordinator
	lineageGuard                   publication.LineageGuard
	backupSourceCompletionObserver BackupSourceCompletionObserver
	legacyBlockRecorder            publication.LegacyBlockRecorder
	managedRetention               ManagedRecoveryPointRetention
	resticRetentionFunc            func(context.Context, model.Policy, model.Task)
	rootCtx                        context.Context    // worker goroutines 的父级 context
	rootCancel                     context.CancelFunc // 由 Shutdown 调用，通知所有 worker 退出

	drillLoopMu           sync.Mutex
	drillLoopCancel       context.CancelFunc
	drillLoopWG           sync.WaitGroup
	drillOwnerID          string
	drillRecoveryLease    time.Duration
	drillRecoveryInterval time.Duration
	drillRecoveryBlocked  atomic.Bool
	managerRunMu          sync.Mutex
	managerRunCancel      context.CancelFunc
	managerRunDone        chan struct{}

	// executionOwnerID fences ordinary TaskRun terminal writes and is renewed
	// while a runner is live. A restarted process may claim only an expired
	// lease, never a live run owned by another process.
	executionOwnerID       string
	executionLeaseDuration time.Duration

	shuttingDown atomic.Bool
}

// ExactAnomalyFunc is the managed publication callback. The identifiers are
// full native IDs held only in memory; implementations must revalidate them
// through the shared lineage guard before opening a Provider command.
type ExactAnomalyFunc func(context.Context, model.Task, uint, string, string) ([]anomaly.Finding, error)

func NewManager(db *gorm.DB, executorFactory executor.Factory, hub *ws.Hub, scheduler *scheduler.CronScheduler, settingsSvc *settings.Service, alertDispatcher *alerting.Dispatcher, sampleRetentionDays int, taskRunRetentionDays int, options ...ManagerOption) *Manager {
	if alertDispatcher == nil {
		alertDispatcher = alerting.NewDispatcher(db, nil, nil)
	}
	m := &Manager{
		db:                     db,
		nodeWriteRetryWait:     waitForNodeWriteReservationRetry,
		runContextFactory:      context.WithTimeout,
		stateMachine:           NewStateMachine(),
		executorFactory:        executorFactory,
		hub:                    hub,
		scheduler:              scheduler,
		semaphore:              make(chan struct{}, 8),
		hookRunFunc:            nil, // 初始化后设置为默认 runSSHHook
		taskRunRetentionDays:   taskRunRetentionDays,
		settingsSvc:            settingsSvc,
		alertDispatcher:        alertDispatcher,
		drillOwnerID:           generateChainRunID(),
		drillRecoveryLease:     defaultDrillRecoveryLease,
		drillRecoveryInterval:  defaultDrillRecoveryInterval,
		executionOwnerID:       generateChainRunID(),
		executionLeaseDuration: defaultTaskRunLease,
	}
	m.drillRecoveryBlocked.Store(true)
	for _, option := range options {
		if option != nil {
			option(m)
		}
	}
	m.hookRunFunc = m.runSSHHook
	m.drillSSHScriptFunc = m.runDrillSSHScript
	// The production restore transport is intentionally unavailable until a
	// credential-agent/relay implementation can move data without spreading
	// source-node credentials. Tests and future internal callers may inject
	// drillRestoreFunc explicitly.
	m.ensureRemoteTargetReadyFunc = executor.EnsureRemoteTargetReady
	m.rootCtx, m.rootCancel = context.WithCancel(context.Background())

	// Create and start sub-components.
	m.logDispatcher = NewLogDispatcher(db, hub)
	m.sampleWriter = NewSampleWriter(db, sampleRetentionDays)
	m.chainRunner = NewChainRunner()

	m.logDispatcher.Start(m.rootCtx, m.cleanupExpiredTaskRuns)
	m.sampleWriter.Start(m.rootCtx)

	return m
}

func (m *Manager) newRunContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	factory := m.runContextFactory
	if factory == nil {
		factory = context.WithTimeout
	}
	if parent == nil {
		parent = context.Background()
	}
	return factory(parent, timeout)
}

func (m *Manager) claimPendingRunOwnership(taskID uint) (context.Context, *pendingRunOwnership, bool) {
	parent := m.rootCtx
	if parent == nil {
		parent = context.Background()
	}
	launchCtx, launchCancel := context.WithCancel(parent)
	ownership := &pendingRunOwnership{
		cancelPersistenceStarted: make(chan struct{}),
		cancelSettled:            make(chan struct{}),
	}
	ownership.addCancel(launchCancel)
	if _, loaded := m.pendingRuns.LoadOrStore(taskID, ownership); loaded {
		ownership.cancel()
		return nil, nil, false
	}
	m.chainRunner.Store(taskID, ownership.cancel)
	return launchCtx, ownership, true
}

// SetAnomalySink 注入 anomaly.AlertSink，用于快照差异异常检测后的告警提升。
// 若未调用，runTask 中的快照差异检测将静默跳过。
func (m *Manager) SetAnomalySink(sink anomaly.AlertSink) {
	m.anomalySink = sink
}

// SetExactAnomalyAnalyzer installs the committed-point anomaly path. Legacy
// repository-wide anomaly detection remains available only to pristine runs.
func (m *Manager) SetExactAnomalyAnalyzer(analyzer ExactAnomalyFunc) {
	m.exactAnomalyAnalyzer = analyzer
}

// ObserveCommitted is invoked best-effort by the publication worker after a
// durable commit. It never mutates publication or TaskRun state and skips the
// first committed point because no same-Task predecessor exists.
func (m *Manager) ObserveCommitted(ctx context.Context, outcome publication.Outcome) {
	if m == nil || m.db == nil || m.exactAnomalyAnalyzer == nil || m.anomalySink == nil ||
		outcome.State != backupasset.RecoveryPointCommitted || outcome.TaskID == 0 || outcome.TaskRunID == 0 ||
		outcome.NativePointID == "" || outcome.PreviousNativePointID == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	analyzeCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var taskEntity model.Task
	if err := m.db.WithContext(analyzeCtx).First(&taskEntity, outcome.TaskID).Error; err != nil {
		logger.Module("task").Warn().Uint("task_id", outcome.TaskID).Msg("加载已提交恢复点的任务失败")
		return
	}
	findings, err := m.exactAnomalyAnalyzer(analyzeCtx, taskEntity, outcome.TaskRunID, outcome.NativePointID, outcome.PreviousNativePointID)
	if err != nil {
		logger.Module("task").Warn().Uint("task_id", outcome.TaskID).Msg("精确快照异常检测失败")
		return
	}
	for _, finding := range findings {
		if err := m.anomalySink.Raise(analyzeCtx, finding); err != nil {
			logger.Module("task").Warn().Uint("task_id", outcome.TaskID).Str("detector", finding.Detector).Str("metric", finding.Metric).Msg("提升精确快照异常告警失败")
		}
	}
}

var _ publication.CommitObserver = (*Manager)(nil)

// SetAutomationDispatcher 注入 automation.Dispatcher，用于在任务完成/失败时
// 触发自动化规则。若未调用，事件将不被派发。
func (m *Manager) SetAutomationDispatcher(dispatcher *automation.Dispatcher) {
	m.autoDispatcher = dispatcher
}

// SetPublicationCoordinator enables the Restic evidence lane. It is optional
// so existing deployments retain the legacy executor path until the shared
// backup-asset runtime is wired at startup.
func (m *Manager) SetPublicationCoordinator(coordinator publication.Coordinator) {
	m.publicationCoordinator = coordinator
}

// SetLineageGuard installs the shared Restic command-admission and lineage
// boundary. It is optional until the backup-asset runtime is composed, so
// installations without that runtime retain the existing compatibility path.
func (m *Manager) SetLineageGuard(guard publication.LineageGuard) {
	m.lineageGuard = guard
}

// SetBackupSourceCompletionObserver installs the legacy Rsync source completion
// observer. It is optional for compatibility with task managers without the
// backup-asset runtime.
func (m *Manager) SetBackupSourceCompletionObserver(observer BackupSourceCompletionObserver) {
	m.backupSourceCompletionObserver = observer
}

// SetLegacyBlockRecorder installs the typed audit/metric sink used after the
// lineage boundary blocks a legacy Restic operation.
func (m *Manager) SetLegacyBlockRecorder(recorder publication.LegacyBlockRecorder) {
	m.legacyBlockRecorder = recorder
}

// SetManagedRecoveryPointRetention installs the exact RecoveryPoint lifecycle
// authority used after the lineage guard proves a managed Task.
func (m *Manager) SetManagedRecoveryPointRetention(retention ManagedRecoveryPointRetention) {
	m.managedRetention = retention
}

// SetNodeWriteAdmission installs the durable Task/Recovery node boundary. It
// must be wired before schedules are loaded so every production trigger uses
// the same coordinator.
func (m *Manager) SetNodeWriteAdmission(admission NodeWriteAdmission) {
	m.nodeWriteAdmission = admission
}

func (m *Manager) reserveTaskRun(ctx context.Context, nodeID uint, requested model.TaskRun) (model.TaskRun, error) {
	if !model.IsTaskRunNodeSnapshotAuthoritative(nodeID) {
		return model.TaskRun{}, ErrNodeWriteStartLost
	}
	if ctx == nil {
		ctx = context.Background()
	}
	wait := m.nodeWriteRetryWait
	if wait == nil {
		wait = waitForNodeWriteReservationRetry
	}
	isChain := strings.EqualFold(strings.TrimSpace(requested.TriggerType), "chain")
	isCron := strings.EqualFold(strings.TrimSpace(requested.TriggerType), "cron")
	for attempt := range nodeWriteReservationAttempts {
		if err := ctx.Err(); err != nil {
			return model.TaskRun{}, err
		}
		candidate := requested
		candidate.ID = 0
		candidate.NodeIDSnapshot = nodeID
		candidate.ExecutionOwnerID = m.executionOwnerID
		leaseUntil := time.Now().UTC().Add(m.taskRunLeaseDuration())
		candidate.ExecutionLeaseUntil = &leaseUntil
		candidate.CreatedAt = time.Time{}
		candidate.UpdatedAt = time.Time{}
		err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			policySnapshot, err := lockTaskPolicyForFingerprint(tx, requested.TaskID)
			if err != nil {
				return err
			}
			var locked model.Task
			lock := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
				Where("id = ?", requested.TaskID).Limit(1).Find(&locked)
			if lock.Error != nil {
				return lock.Error
			}
			if lock.RowsAffected == 0 {
				return fmt.Errorf("任务不存在")
			}
			if !taskPolicySnapshotMatches(locked, policySnapshot) {
				return errTaskPolicyChanged
			}
			locked.Policy = policySnapshot
			if locked.ArchivedAt != nil {
				return ErrTaskArchived
			}
			if policySnapshot != nil && !policySnapshot.Enabled &&
				(isCron || strings.EqualFold(strings.TrimSpace(requested.TriggerType), "auto")) {
				return fmt.Errorf("策略已禁用，请先启用后再触发")
			}
			if locked.NodeID != nodeID {
				return ErrNodeWriteStartLost
			}
			if isChain && requested.UpstreamTaskRunID != nil {
				var existing model.TaskRun
				result := tx.Where(
					"task_id = ? AND upstream_task_run_id = ?",
					requested.TaskID, *requested.UpstreamTaskRunID,
				).Order("id ASC").Limit(1).Find(&existing)
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected == 1 {
					candidate = existing
					return errTaskChainRunHandled
				}
			}
			if isCron && requested.CronScheduledAt != nil {
				var existing model.TaskRun
				result := tx.Where(
					"task_id = ? AND trigger_type = ? AND cron_scheduled_at = ?",
					requested.TaskID, "cron", requested.CronScheduledAt,
				).Order("id ASC").Limit(1).Find(&existing)
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected == 1 {
					candidate = existing
					return errCronOccurrenceHandled
				}
			}
			if isBackupTaskRunTrigger(requested.TriggerType) && !locked.Enabled {
				return fmt.Errorf("任务已暂停，请先恢复后再触发")
			}
			var activeCount int64
			if err := tx.Model(&model.TaskRun{}).
				Where("task_id = ? AND status IN ?", requested.TaskID, model.TaskRunActiveStatuses()).
				Count(&activeCount).Error; err != nil {
				return err
			}
			if activeCount > 0 || ParseStatus(locked.Status) == StatusRunning {
				if isChain {
					return errTaskChainBusy
				}
				return fmt.Errorf("该任务正在执行中，请勿重复触发")
			}
			if err := loadTaskNodeForFingerprint(tx, &locked); err != nil {
				return err
			}
			if err := validateTaskTargetOwnershipTx(tx, &locked); err != nil {
				return err
			}
			if isBackupTaskRunTrigger(requested.TriggerType) {
				if err := rejectUnresolvedMutableGenerationTx(tx, &locked); err != nil {
					return err
				}
				if err := reservePolicySlot(tx, policySnapshot); err != nil {
					return err
				}
			}
			if requested.BackupSourceRunID > 0 {
				if !isLegacyMutableTask(locked) {
					return ErrRestoreRequiresNewBackup
				}
				var latestOrdinary model.TaskRun
				allowEmptyRcloneGeneration := false
				if isLegacyMutableRcloneTask(locked) {
					selection, selectionErr := latestRcloneMutableGeneration(tx, requested.TaskID, locked.NodeID)
					if selectionErr != nil {
						return selectionErr
					}
					latestOrdinary = selection.run
					allowEmptyRcloneGeneration = selection.allowEmpty
				} else {
					latestResult := tx.Where(`task_id = ? AND node_id_snapshot = ? AND
						lower(COALESCE(trigger_type, '')) NOT IN ? AND
						COALESCE(backup_generation_state, '') <> ''`,
						requested.TaskID, locked.NodeID, []string{"restore", "drill"}).
						Order("id DESC").Limit(1).Find(&latestOrdinary)
					if latestResult.Error != nil {
						return latestResult.Error
					}
				}
				if latestOrdinary.ID == 0 || latestOrdinary.ID != requested.BackupSourceRunID {
					return ErrRestoreRequiresNewBackup
				}
				sourceRun := latestOrdinary
				if sourceRun.Status != model.TaskRunStatusSuccess ||
					strings.TrimSpace(sourceRun.BackupConfigFingerprint) == "" ||
					sourceRun.BackupConfigFingerprint != model.TaskRunBackupConfigFingerprint(locked) {
					return ErrRestoreRequiresNewBackup
				}
				if isLegacyMutableRcloneTask(locked) {
					state := strings.TrimSpace(sourceRun.BackupGenerationState)
					if (state != "" && state != model.TaskRunGenerationStateVerified) ||
						(state == "" && !allowEmptyRcloneGeneration) {
						return ErrRestoreRequiresNewBackup
					}
				} else {
					if sourceRun.BackupGenerationState != model.TaskRunGenerationStateVerified ||
						strings.TrimSpace(sourceRun.BackupCaptureLayout) == "" ||
						strings.TrimSpace(sourceRun.BackupCaptureManifest) == "" {
						return ErrRestoreRequiresNewBackup
					}
					capture, decodeErr := model.DecodeRsyncCaptureManifest(sourceRun.BackupCaptureManifest)
					captureRoot, rootDecodeErr := model.DecodeRsyncCaptureRootSidecar(sourceRun.BackupCaptureRoot, capture.Version)
					if decodeErr != nil ||
						rootDecodeErr != nil ||
						capture.Layout != sourceRun.BackupCaptureLayout ||
						capture.Root != captureRoot {
						return ErrRestoreRequiresNewBackup
					}
				}
			}
			if m.nodeWriteAdmission != nil {
				if err := m.nodeWriteAdmission.AdmitTaskTx(ctx, tx, nodeID); err != nil {
					return err
				}
			}
			if isBackupTaskRunTrigger(requested.TriggerType) {
				candidate.BackupConfigFingerprint = model.TaskRunBackupConfigFingerprint(locked)
				if candidate.BackupConfigFingerprint == "" {
					return fmt.Errorf("生成任务执行绑定失败")
				}
			}
			return tx.Create(&candidate).Error
		})
		if err == nil {
			return candidate, nil
		}
		if isCron && requested.CronScheduledAt != nil {
			var existing model.TaskRun
			result := m.db.WithContext(ctx).Where(
				"task_id = ? AND trigger_type = ? AND cron_scheduled_at = ?",
				requested.TaskID, "cron", requested.CronScheduledAt,
			).Order("id ASC").Limit(1).Find(&existing)
			if result.Error == nil && result.RowsAffected == 1 {
				candidate = existing
				return candidate, errCronOccurrenceHandled
			}
		}
		if errors.Is(err, errTaskChainRunHandled) {
			return candidate, err
		}
		if errors.Is(err, errCronOccurrenceHandled) {
			return candidate, err
		}
		if errors.Is(err, ErrNodeWriteConflict) {
			return model.TaskRun{}, ErrNodeWriteConflict
		}
		if !retryableNodeWriteReservationError(err) {
			return model.TaskRun{}, err
		}
		if attempt+1 == nodeWriteReservationAttempts {
			break
		}
		if err := wait(ctx, attempt); err != nil {
			return model.TaskRun{}, err
		}
	}
	return model.TaskRun{}, ErrNodeWriteUnavailable
}

// ReserveAutomationRunTx creates an automation-owned pending TaskRun in the
// caller's transaction. The caller's TaskRunEffect row is the idempotency
// marker; effect keys are intentionally not copied into run lineage fields.
func (m *Manager) ReserveAutomationRunTx(
	ctx context.Context,
	tx *gorm.DB,
	taskID uint,
) (uint, error) {
	if m == nil || tx == nil || taskID == 0 {
		return 0, fmt.Errorf("automation task reservation unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	tx = tx.WithContext(ctx)
	policySnapshot, err := lockTaskPolicyForFingerprint(tx, taskID)
	if err != nil {
		return 0, err
	}
	var taskEntity model.Task
	result := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", taskID).Limit(1).Find(&taskEntity)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected != 1 {
		return 0, fmt.Errorf("任务不存在")
	}
	if !taskPolicySnapshotMatches(taskEntity, policySnapshot) {
		return 0, errTaskPolicyChanged
	}
	taskEntity.Policy = policySnapshot
	if taskEntity.ArchivedAt != nil {
		return 0, ErrTaskArchived
	}
	if !taskEntity.Enabled {
		return 0, fmt.Errorf("任务已暂停，请先恢复后再触发")
	}
	if policySnapshot != nil && !policySnapshot.Enabled {
		return 0, fmt.Errorf("策略已禁用，请先启用后再触发")
	}
	var activeCount int64
	if err := tx.Model(&model.TaskRun{}).
		Where("task_id = ? AND status IN ?", taskID, model.TaskRunActiveStatuses()).
		Count(&activeCount).Error; err != nil {
		return 0, err
	}
	if err := loadTaskNodeForFingerprint(tx, &taskEntity); err != nil {
		return 0, err
	}
	if err := validateTaskTargetOwnershipTx(tx, &taskEntity); err != nil {
		return 0, err
	}
	if err := rejectUnresolvedMutableGenerationTx(tx, &taskEntity); err != nil {
		return 0, err
	}
	if err := reservePolicySlot(tx, policySnapshot); err != nil {
		return 0, err
	}
	if m.nodeWriteAdmission != nil {
		if err := m.nodeWriteAdmission.AdmitTaskTx(ctx, tx, taskEntity.NodeID); err != nil {
			return 0, err
		}
	}
	run := model.TaskRun{
		TaskID:                  taskID,
		NodeIDSnapshot:          taskEntity.NodeID,
		BackupConfigFingerprint: model.TaskRunBackupConfigFingerprint(taskEntity),
		TriggerType:             "auto",
		Status:                  model.TaskRunStatusPending,
		ChainRunID:              generateChainRunID(),
		// Leave the execution owner empty. The post-commit launcher claims it;
		// a restarted manager can therefore recover a committed reservation
		// immediately instead of waiting for a crashed lease to expire.
		ExecutionOwnerID:    "",
		ExecutionLeaseUntil: nil,
	}
	if run.BackupConfigFingerprint == "" {
		return 0, fmt.Errorf("生成任务执行绑定失败")
	}
	if err := tx.Create(&run).Error; err != nil {
		return 0, err
	}
	return run.ID, nil
}

func (m *Manager) enterTaskExecution(
	ctx context.Context,
	runID uint,
	nodeID uint,
	startedAt time.Time,
	taskEntity *model.Task,
) error {
	return m.enterTaskExecutionForReason(ctx, runID, nodeID, startedAt, taskEntity, "")
}

// enterTaskExecutionForReason is the single durable gate between a queued
// TaskRun and an executor. Scheduler admission is rechecked while holding the
// Task row lock so disabling a task/policy cannot race a queued callback.
func (m *Manager) enterTaskExecutionForReason(
	ctx context.Context,
	runID uint,
	nodeID uint,
	startedAt time.Time,
	taskEntity *model.Task,
	reason string,
) error {
	if !model.IsTaskRunNodeSnapshotAuthoritative(nodeID) {
		return ErrNodeWriteStartLost
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	var taskPayload map[string]interface{}
	var expectedTaskStatus string
	if taskEntity != nil {
		expectedTaskStatus = taskEntity.Status
		from := ParseStatus(expectedTaskStatus)
		switch from {
		case StatusSuccess, StatusFailed, StatusCanceled, StatusWarning, StatusSkipped:
			if err := m.stateMachine.ValidateTransition(from, StatusPending); err != nil {
				return err
			}
			if err := m.stateMachine.ValidateTransition(StatusPending, StatusRunning); err != nil {
				return err
			}
		default:
			if err := m.stateMachine.ValidateTransition(from, StatusRunning); err != nil {
				return err
			}
		}
		taskPayload = map[string]interface{}{
			"status":      string(StatusRunning),
			"last_run_at": startedAt,
			"next_run_at": nil,
			"last_error":  "",
		}
	}
	var cronBlocked bool
	var taskBlocked bool
	var cronScheduledAt *time.Time
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var policySnapshot *model.Policy
		var err error
		if taskEntity != nil {
			policySnapshot, err = lockTaskPolicyForFingerprint(tx, taskEntity.ID)
			if err != nil {
				return err
			}
		}
		var lockedTask model.Task
		if taskEntity != nil {
			result := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
				Preload("Node").
				Preload("Node.SSHKey").
				Where("id = ?", taskEntity.ID).
				Limit(1).
				Find(&lockedTask)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrNodeWriteStartLost
			}
			if !taskPolicySnapshotMatches(lockedTask, policySnapshot) {
				return errTaskPolicyChanged
			}
			lockedTask.Policy = policySnapshot
			if lockedTask.Node.ID == 0 {
				if err := loadTaskNodeForFingerprint(tx, &lockedTask); err != nil {
					return err
				}
			}
			if isBackupTaskRunTrigger(reason) && reason != "cron" && !lockedTask.Enabled {
				if err := m.cancelQueuedCronRunTx(tx, runID, nodeID, "任务已暂停，排队执行已取消"); err != nil {
					return err
				}
				taskBlocked = true
			}
			if reason == "cron" {
				var queuedRun model.TaskRun
				runResult := tx.Select("cron_scheduled_at").
					Where("id = ? AND node_id_snapshot = ? AND status = ?", runID, nodeID, model.TaskRunStatusPending).
					Limit(1).
					Find(&queuedRun)
				if runResult.Error != nil {
					return runResult.Error
				}
				if runResult.RowsAffected != 1 {
					return ErrNodeWriteStartLost
				}
				cronScheduledAt = queuedRun.CronScheduledAt

				policyEnabled := policySnapshot == nil || policySnapshot.Enabled
				if !lockedTask.Enabled || strings.TrimSpace(lockedTask.CronSpec) == "" || !policyEnabled {
					if err := m.cancelQueuedCronRunTx(tx, runID, nodeID, "任务已暂停，定时执行已取消"); err != nil {
						return err
					}
					cronBlocked = true
				} else if lockedTask.SkipNext {
					nextRun := nextCronRun(lockedTask.CronSpec)
					if cronScheduledAt != nil {
						nextRun = nextCronRunAfter(lockedTask.CronSpec, *cronScheduledAt)
					}
					taskResult := tx.Model(&model.Task{}).
						Where("id = ? AND skip_next = ?", lockedTask.ID, true).
						Updates(map[string]interface{}{"skip_next": false, "next_run_at": nextRun})
					if taskResult.Error != nil {
						return taskResult.Error
					}
					if taskResult.RowsAffected != 1 {
						return ErrNodeWriteStartLost
					}
					if err := m.cancelQueuedCronRunTx(tx, runID, nodeID, "本次定时执行已跳过（用户设置跳过下次）"); err != nil {
						return err
					}
					cronBlocked = true
				}
			}
			if cronBlocked || taskBlocked {
				return nil
			}
			if isBackupTaskRunTrigger(reason) {
				var queuedRun model.TaskRun
				runResult := tx.Select("backup_config_fingerprint").
					Where("id = ? AND node_id_snapshot = ? AND status = ?", runID, nodeID, model.TaskRunStatusPending).
					Limit(1).
					Find(&queuedRun)
				if runResult.Error != nil {
					return runResult.Error
				}
				if runResult.RowsAffected != 1 {
					return ErrNodeWriteStartLost
				}
				fingerprint := model.TaskRunBackupConfigFingerprint(lockedTask)
				if fingerprint == "" {
					return errTaskRunBackupBindingMismatch
				}
				if strings.TrimSpace(queuedRun.BackupConfigFingerprint) == "" {
					updateResult := tx.Model(&model.TaskRun{}).
						Where("id = ? AND node_id_snapshot = ? AND status = ? AND backup_config_fingerprint = ''",
							runID, nodeID, model.TaskRunStatusPending).
						Update("backup_config_fingerprint", fingerprint)
					if updateResult.Error != nil {
						return updateResult.Error
					}
					if updateResult.RowsAffected != 1 {
						return ErrNodeWriteStartLost
					}
				} else if queuedRun.BackupConfigFingerprint != fingerprint {
					return errTaskRunBackupBindingMismatch
				}
			}
			// Use the row protected by the Task lock for the executor snapshot.
			// This prevents a concurrent config update from pairing old values
			// with a newly captured provenance fingerprint.
			*taskEntity = lockedTask
			if err := validateTaskTargetOwnershipTx(tx, &lockedTask); err != nil {
				return err
			}
			if isBackupTaskRunTrigger(reason) {
				if err := rejectUnresolvedMutableGenerationTx(tx, &lockedTask); err != nil {
					return err
				}
				if err := admitPolicySlot(tx, policySnapshot, runID); err != nil {
					return err
				}
			}
		}
		if cronBlocked {
			return nil
		}
		if m.nodeWriteAdmission != nil {
			if err := m.nodeWriteAdmission.EnterTaskExecutionTx(ctx, tx, runID, nodeID, startedAt); err != nil {
				return err
			}
		} else {
			result := tx.Model(&model.TaskRun{}).
				Where("id = ? AND node_id_snapshot = ? AND status = ?", runID, nodeID, model.TaskRunStatusPending).
				Updates(map[string]interface{}{"status": model.TaskRunStatusRunning, "started_at": &startedAt})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrNodeWriteStartLost
			}
		}

		if taskEntity == nil {
			return nil
		}
		result := tx.Model(&model.Task{}).
			Where("id = ? AND status = ?", taskEntity.ID, expectedTaskStatus).
			Updates(taskPayload)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrNodeWriteStartLost
		}
		return nil
	})
	if err != nil {
		return err
	}
	if cronBlocked {
		return errCronTaskBlocked
	}
	if taskBlocked {
		return errTaskPaused
	}
	if taskEntity != nil {
		taskEntity.Status = string(StatusRunning)
		taskEntity.LastRunAt = &startedAt
		taskEntity.NextRunAt = nil
		taskEntity.LastError = ""
	}
	return nil
}

var errTaskPaused = errors.New("task execution blocked because task is paused")
var errCronTaskBlocked = errors.New("cron task execution blocked")

func (m *Manager) cancelQueuedCronRunTx(tx *gorm.DB, runID, nodeID uint, message string) error {
	now := time.Now().UTC()
	var run model.TaskRun
	loaded := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND node_id_snapshot = ?", runID, nodeID).Limit(1).Find(&run)
	if loaded.Error != nil {
		return loaded.Error
	}
	if loaded.RowsAffected != 1 || run.Status != model.TaskRunStatusPending {
		return ErrNodeWriteStartLost
	}
	if strings.TrimSpace(run.ExecutionOwnerID) != "" &&
		run.ExecutionOwnerID != m.executionOwnerID {
		return errTaskRunNotOwner
	}
	markNoStart, err := rcloneNoStartStateForRunTx(tx, &run)
	if err != nil {
		return err
	}
	updates := map[string]interface{}{
		"status":                model.TaskRunStatusCanceled,
		"started_at":            nil,
		"finished_at":           &now,
		"duration_ms":           int64(0),
		"last_error":            sanitizeTaskLastError(message),
		"execution_owner_id":    "",
		"execution_lease_until": nil,
	}
	if markNoStart {
		updates["backup_generation_state"] = model.TaskRunGenerationStateNoStart
	}
	result := tx.Model(&model.TaskRun{}).
		Where(`id = ? AND node_id_snapshot = ? AND status = ? AND
			(execution_owner_id = '' OR execution_owner_id = ?)`,
			runID, nodeID, model.TaskRunStatusPending, m.executionOwnerID).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrNodeWriteStartLost
	}
	return nil
}

func (m *Manager) cancelTaskExecutionBeforeExecutor(
	runID uint,
	taskID uint,
	nodeID uint,
	previous *model.Task,
	message string,
) error {
	canceledAt := time.Now().UTC()
	return m.db.Transaction(func(tx *gorm.DB) error {
		var run model.TaskRun
		loaded := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND task_id = ? AND node_id_snapshot = ?", runID, taskID, nodeID).
			Limit(1).Find(&run)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || run.Status != model.TaskRunStatusRunning {
			return ErrNodeWriteStartLost
		}
		if strings.TrimSpace(run.ExecutionOwnerID) != "" &&
			run.ExecutionOwnerID != m.executionOwnerID {
			return errTaskRunNotOwner
		}
		markNoStart, err := rcloneNoStartStateForPreProviderRunTx(tx, &run)
		if err != nil {
			return err
		}
		runUpdates := map[string]interface{}{
			"status":                model.TaskRunStatusCanceled,
			"started_at":            nil,
			"finished_at":           &canceledAt,
			"duration_ms":           int64(0),
			"last_error":            sanitizeTaskLastError(message),
			"execution_owner_id":    "",
			"execution_lease_until": nil,
		}
		if markNoStart && strings.TrimSpace(run.BackupGenerationState) == "" {
			runUpdates["backup_generation_state"] = model.TaskRunGenerationStateNoStart
		}
		runResult := tx.Model(&model.TaskRun{}).
			Where(`id = ? AND task_id = ? AND node_id_snapshot = ? AND status = ? AND
				(execution_owner_id = '' OR execution_owner_id = ?)`,
				runID, taskID, nodeID, model.TaskRunStatusRunning, m.executionOwnerID).
			Updates(runUpdates)
		if runResult.Error != nil {
			return runResult.Error
		}
		if runResult.RowsAffected != 1 {
			return ErrNodeWriteStartLost
		}

		if previous == nil {
			return nil
		}

		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND status = ?", previous.ID, string(StatusRunning)).
			Updates(map[string]interface{}{
				"status":      previous.Status,
				"last_run_at": previous.LastRunAt,
				"next_run_at": previous.NextRunAt,
				"last_error":  previous.LastError,
			})
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return ErrNodeWriteStartLost
		}
		return nil
	})
}

func retryableNodeWriteReservationError(err error) bool {
	if errors.Is(err, ErrNodeWriteUnavailable) || errors.Is(err, errTaskPolicyChanged) {
		return true
	}
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

func isActiveDrillReservationConflict(err error) bool {
	var sqliteError sqlite3.Error
	if errors.As(err, &sqliteError) && sqliteError.Code == sqlite3.ErrConstraint &&
		(sqliteError.ExtendedCode == sqlite3.ErrConstraintUnique || sqliteError.ExtendedCode == sqlite3.ErrConstraintPrimaryKey) {
		return true
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		return postgresError.Code == "23505" && postgresError.ConstraintName == activeDrillRunIndex
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, activeDrillRunIndex) ||
		strings.Contains(message, "unique constraint failed: task_runs.task_id")
}

func waitForNodeWriteReservationRetry(ctx context.Context, attempt int) error {
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

func (m *Manager) LoadSchedules(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Startup is a durable recovery boundary, not just a scheduler reload.
	// Reconcile expired ordinary owners, drain terminal effects, and launch
	// committed pending automation/retry runs before accepting new cron work.
	if err := m.reconcileExpiredOrdinaryRuns(ctx); err != nil {
		return fmt.Errorf("reconcile interrupted task runs: %w", err)
	}
	if err := m.reconcileMissingRetryEffects(ctx); err != nil {
		return fmt.Errorf("reconcile missing retry effects: %w", err)
	}
	if err := m.drainReadyTaskRunEffects(ctx); err != nil {
		return fmt.Errorf("drain task terminal effects: %w", err)
	}
	if err := m.reconcilePendingDurableRuns(ctx); err != nil {
		return fmt.Errorf("reconcile pending durable task runs: %w", err)
	}
	if err := m.reconcileExpiredDrills(ctx); err != nil {
		m.drillRecoveryBlocked.Store(true)
		return fmt.Errorf("reconcile interrupted restore drills: %w", err)
	}
	if err := m.reconcileSchedules(ctx); err != nil {
		return err
	}
	m.drillRecoveryBlocked.Store(false)
	return nil
}

func (m *Manager) reconcileSchedules(ctx context.Context) error {
	if m == nil || m.db == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var tasks []model.Task
	if err := m.db.WithContext(ctx).
		Where("cron_spec <> '' AND enabled = ? AND archived_at IS NULL", true).
		Find(&tasks).Error; err != nil {
		return err
	}
	keep := make(map[uint]struct{}, len(tasks))
	for _, one := range tasks {
		keep[one.ID] = struct{}{}
		if err := m.SyncSchedule(one); err != nil {
			return err
		}
	}
	if m.scheduler != nil {
		m.scheduleMu.Lock()
		m.scheduler.RemoveTasksExcept(keep)
		m.scheduleMu.Unlock()
	}
	return nil
}

func (m *Manager) SyncSchedule(task model.Task) error {
	if m.scheduler == nil {
		return nil
	}
	m.scheduleMu.Lock()
	defer m.scheduleMu.Unlock()
	var current struct {
		ArchivedAt *time.Time `gorm:"column:archived_at"`
		Status     string     `gorm:"column:status"`
		Enabled    bool       `gorm:"column:enabled"`
		CronSpec   string     `gorm:"column:cron_spec"`
	}
	if err := m.db.Model(&model.Task{}).
		Select("archived_at, status, enabled, cron_spec").
		Where("id = ?", task.ID).Limit(1).Scan(&current).Error; err != nil {
		return fmt.Errorf("load task schedule state: %w", err)
	}
	if !current.Enabled || current.ArchivedAt != nil || current.CronSpec == "" {
		m.removeScheduleLocked(task.ID)
		return nil
	}
	// A retry reservation owns next_run_at until it is delivered. Cron
	// reconciliation must not clobber that durable retry deadline.
	if ParseStatus(current.Status) != StatusRetrying {
		if next := nextCronRun(current.CronSpec); next != nil {
			if err := m.db.Model(&model.Task{}).Where("id = ?", task.ID).Update("next_run_at", next).Error; err != nil {
				return fmt.Errorf("persist next task schedule: %w", err)
			}
		}
	}
	return m.scheduler.RegisterTask(task.ID, current.CronSpec, func(scheduledAt time.Time) {
		if err := m.TriggerFromScheduler(task.ID, scheduledAt); err != nil {
			logger.Module("task").Warn().Uint("task_id", task.ID).Err(err).Msg("定时触发任务失败")
		}
	})

}

func (m *Manager) RemoveSchedule(taskID uint) {
	if m.scheduler == nil {
		return
	}
	m.scheduleMu.Lock()
	defer m.scheduleMu.Unlock()
	m.removeScheduleLocked(taskID)
}

func (m *Manager) removeScheduleLocked(taskID uint) {
	if m.scheduler == nil {
		return
	}
	m.scheduler.RemoveTask(taskID)
}

// Archive disables the Task, unlinks its active repository binding, then
// removes the schedule after commit. It never deletes Provider bytes.
func (m *Manager) Archive(ctx context.Context, taskID uint) (ArchiveResult, error) {
	return NewArchiveService(ArchiveDependencies{
		DB: m.db,
		RemoveSchedule: func(id uint) error {
			m.RemoveSchedule(id)
			return nil
		},
		WriteTx: NewArchiveAuditWriteTx(m.db, nil),
	}).Archive(ctx, taskID)
}

func (m *Manager) TriggerManual(taskID uint) (uint, error) {
	return m.triggerCore(taskID, "manual", generateChainRunID(), nil, nil)
}

func (m *Manager) TriggerAutomation(taskID uint) (uint, error) {
	return m.triggerCore(taskID, "auto", generateChainRunID(), nil, nil)
}

func (m *Manager) PausePolicyNext(ctx context.Context, policyID uint) error {
	return policy.NewControlService(m.db, m).PauseNext(ctx, policyID)
}

func cancelPendingPolicyRunsTx(tx *gorm.DB, policyID uint, message string) error {
	if tx == nil || policyID == 0 {
		return nil
	}
	if strings.TrimSpace(message) == "" {
		message = "策略已禁用，排队执行已取消"
	}
	now := time.Now().UTC()
	var runs []model.TaskRun
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where(`task_id IN (SELECT id FROM tasks WHERE policy_id = ?)`, policyID).
		Where("status = ?", model.TaskRunStatusPending).
		Where("lower(COALESCE(trigger_type, '')) NOT IN ?", []string{"restore", "drill"}).
		Find(&runs)
	if result.Error != nil {
		return result.Error
	}
	for i := range runs {
		run := &runs[i]
		markNoStart, err := rcloneNoStartStateForRunTx(tx, run)
		if err != nil {
			return err
		}
		updates := map[string]interface{}{
			"status":                model.TaskRunStatusCanceled,
			"started_at":            nil,
			"finished_at":           &now,
			"execution_owner_id":    "",
			"execution_lease_until": nil,
			"last_error":            message,
		}
		if markNoStart {
			updates["backup_generation_state"] = model.TaskRunGenerationStateNoStart
		}
		result = tx.Model(&model.TaskRun{}).
			Where("id = ? AND status = ?", run.ID, model.TaskRunStatusPending).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errTaskRunCASLost
		}
	}
	return nil
}

func (m *Manager) DisablePolicy(ctx context.Context, policyID uint) error {
	if m == nil || m.db == nil {
		return fmt.Errorf("policy control unavailable")
	}
	var taskIDs []uint
	if err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		taskIDs, err = m.DisablePolicyTx(ctx, tx, policyID)
		return err
	}); err != nil {
		return err
	}
	return m.ReconcilePolicySchedules(policyID, taskIDs)
}

// PausePolicyNextTx applies policy control inside a caller-owned transaction.
// Scheduler state is intentionally reconciled only after that transaction
// commits.
func (m *Manager) PausePolicyNextTx(ctx context.Context, tx *gorm.DB, policyID uint) error {
	if m == nil || tx == nil {
		return fmt.Errorf("policy control unavailable")
	}
	return policy.NewControlService(m.db, m).PauseNextTx(ctx, tx, policyID)
}

// DisablePolicyTx applies policy control inside a caller-owned transaction and
// returns generated task IDs for post-commit schedule removal.
func (m *Manager) DisablePolicyTx(ctx context.Context, tx *gorm.DB, policyID uint) ([]uint, error) {
	if m == nil || tx == nil {
		return nil, fmt.Errorf("policy control unavailable")
	}
	taskIDs, err := policy.NewControlService(m.db, m).DisableTx(ctx, tx, policyID)
	if err != nil {
		return nil, err
	}
	if err := cancelPendingPolicyRunsTx(tx, policyID, "策略已禁用，排队执行已取消"); err != nil {
		return nil, err
	}
	return taskIDs, nil
}

// ReconcilePolicySchedules removes schedules after a committed policy control
// transaction. The policy ID is retained for the callback contract and does
// not affect the task-ID based removal.
func (m *Manager) ReconcilePolicySchedules(_ uint, taskIDs []uint) error {
	if m == nil {
		return fmt.Errorf("policy schedule reconciler unavailable")
	}
	return policy.RemovePolicySchedules(m.db, m, taskIDs)
}

func (m *Manager) TriggerFromScheduler(taskID uint, scheduledAt time.Time) error {
	_, err := m.triggerCore(taskID, "cron", generateChainRunID(), nil, &scheduledAt)
	return err
}

func (m *Manager) loadRestoreTaskWithProvenance(ctx context.Context, taskID uint) (model.Task, error) {
	var taskEntity model.Task
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		policySnapshot, err := lockTaskPolicyForFingerprint(tx, taskID)
		if err != nil {
			return err
		}
		result := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Preload("Node").
			Preload("Node.SSHKey").
			Where("id = ?", taskID).
			Limit(1).
			Find(&taskEntity)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("任务不存在")
		}
		if !taskPolicySnapshotMatches(taskEntity, policySnapshot) {
			return errTaskPolicyChanged
		}
		taskEntity.Policy = policySnapshot
		if taskEntity.ArchivedAt != nil {
			return ErrTaskArchived
		}
		if !model.IsTaskRunNodeSnapshotAuthoritative(taskEntity.NodeID) {
			return ErrNodeWriteStartLost
		}
		if taskEntity.Node.ID == 0 {
			if err := loadTaskNodeForFingerprint(tx, &taskEntity); err != nil {
				return err
			}
		}
		currentFingerprint := model.TaskRunBackupConfigFingerprint(taskEntity)
		if currentFingerprint == "" {
			return fmt.Errorf("%w: 该任务没有成功的执行记录与当前节点配置匹配，请先创建新的成功备份", ErrRestoreRequiresNewBackup)
		}
		legacyMutable := isLegacyMutableTask(taskEntity)
		if legacyMutable {
			var activeOrdinary int64
			activeResult := tx.Model(&model.TaskRun{}).
				Where(`task_id = ? AND node_id_snapshot = ? AND status IN ? AND
					lower(COALESCE(trigger_type, '')) NOT IN ?`,
					taskID, taskEntity.NodeID, model.TaskRunActiveStatuses(), []string{"restore", "drill"}).
				Count(&activeOrdinary)
			if activeResult.Error != nil {
				return activeResult.Error
			}
			if activeOrdinary > 0 {
				return fmt.Errorf("%w: 任务仍有未完成的写入尝试", ErrRestoreRequiresNewBackup)
			}
		}
		var latestBackup model.TaskRun
		allowEmptyRcloneGeneration := false
		if isLegacyMutableRcloneTask(taskEntity) {
			selection, selectionErr := latestRcloneMutableGeneration(tx, taskID, taskEntity.NodeID)
			if selectionErr != nil {
				return selectionErr
			}
			latestBackup = selection.run
			allowEmptyRcloneGeneration = selection.allowEmpty
		} else {
			latestQuery := tx.Where(`task_id = ? AND node_id_snapshot = ? AND
				lower(COALESCE(trigger_type, '')) NOT IN ?`,
				taskID, taskEntity.NodeID, []string{"restore", "drill"})
			if isLegacyMutableRsyncTask(taskEntity) {
				latestQuery = latestQuery.Where("COALESCE(backup_generation_state, '') <> ''")
			} else {
				latestQuery = latestQuery.Where("status = ?", model.TaskRunStatusSuccess)
			}
			runResult := latestQuery.Order("id DESC").Limit(1).Find(&latestBackup)
			if runResult.Error != nil {
				return runResult.Error
			}
		}
		if latestBackup.ID == 0 {
			return fmt.Errorf("%w: 该任务没有成功的执行记录与当前节点配置匹配，请先创建新的成功备份", ErrRestoreRequiresNewBackup)
		}
		if strings.TrimSpace(latestBackup.BackupConfigFingerprint) == "" ||
			latestBackup.BackupConfigFingerprint != currentFingerprint {
			return fmt.Errorf("%w: 该任务没有成功的执行记录与当前恢复源、目标、节点配置匹配，请先创建新的成功备份", ErrRestoreRequiresNewBackup)
		}
		if isLegacyMutableRsyncTask(taskEntity) {
			if latestBackup.Status != model.TaskRunStatusSuccess ||
				latestBackup.BackupGenerationState != model.TaskRunGenerationStateVerified ||
				strings.TrimSpace(latestBackup.BackupCaptureLayout) == "" ||
				strings.TrimSpace(latestBackup.BackupCaptureManifest) == "" {
				return fmt.Errorf("%w: 最近一次备份尚未生成可恢复的 RSync 捕获证据，请先创建新的成功备份", ErrRestoreRequiresNewBackup)
			}
			capture, decodeErr := model.DecodeRsyncCaptureManifest(latestBackup.BackupCaptureManifest)
			captureRoot, rootDecodeErr := model.DecodeRsyncCaptureRootSidecar(latestBackup.BackupCaptureRoot, capture.Version)
			if decodeErr != nil ||
				rootDecodeErr != nil ||
				capture.Layout != latestBackup.BackupCaptureLayout ||
				capture.Root != captureRoot {
				return fmt.Errorf("%w: 最近一次备份的 RSync 捕获证据无效，请先创建新的成功备份", ErrRestoreRequiresNewBackup)
			}
			taskEntity.RsyncCaptureLayout = latestBackup.BackupCaptureLayout
			taskEntity.RsyncCaptureRoot = captureRoot
			taskEntity.RsyncCaptureManifest = latestBackup.BackupCaptureManifest
			taskEntity.RsyncCaptureGenerationID = latestBackup.ID
		} else if isLegacyMutableRcloneTask(taskEntity) {
			state := strings.TrimSpace(latestBackup.BackupGenerationState)
			if latestBackup.Status != model.TaskRunStatusSuccess ||
				(state != "" && state != model.TaskRunGenerationStateVerified) ||
				(state == "" && !allowEmptyRcloneGeneration) {
				return fmt.Errorf("%w: 最近一次 RClone 写入尝试未证明完成，请先创建新的成功备份", ErrRestoreRequiresNewBackup)
			}
			// Rclone has no source-side manifest. Binding the exact durable
			// run prevents a later mutable write from silently changing the
			// restore source between validation and reservation.
			taskEntity.RsyncCaptureGenerationID = latestBackup.ID
		}
		return nil
	})
	if err != nil {
		return model.Task{}, err
	}
	return taskEntity, nil
}

// TriggerRestore 触发备份恢复：将备份目标反向同步回源路径（或自定义路径）。
// 要求该任务至少有一次成功的执行记录，且恢复路径通过安全校验。
func (m *Manager) TriggerRestore(taskID uint, targetPath string) (uint, error) {
	if m.shuttingDown.Load() {
		return 0, fmt.Errorf("系统维护中，请稍候再试")
	}

	// 互斥检查：防止与同任务的备份/恢复并发执行
	launchCtx, ownership, claimed := m.claimPendingRunOwnership(taskID)
	if !claimed {
		return 0, fmt.Errorf("该任务正在执行中，请勿重复触发")
	}
	scheduled := false
	nodeIDForCleanup := uint(0)
	registeredCancel := ownership.cancel
	defer func() {
		if !scheduled {
			if registeredCancel != nil {
				registeredCancel()
				m.chainRunner.Delete(taskID)
			}
			// 如果 restoreNodes 已注册但 goroutine 未启动，需要清理
			if nodeIDForCleanup > 0 {
				m.restoreNodes.Delete(nodeIDForCleanup)
			}
			m.pendingRuns.CompareAndDelete(taskID, ownership)
		}
	}()

	var taskEntity model.Task
	if err := m.db.Preload("Node").Preload("Node.SSHKey").Preload("Policy").First(&taskEntity, taskID).Error; err != nil {
		return 0, fmt.Errorf("任务不存在")
	}
	if taskEntity.ArchivedAt != nil {
		return 0, ErrTaskArchived
	}
	if !model.IsTaskRunNodeSnapshotAuthoritative(taskEntity.NodeID) {
		return 0, ErrNodeWriteStartLost
	}
	if m.afterTriggerRestoreLoad != nil {
		m.afterTriggerRestoreLoad()
	}

	validatedTask, err := m.loadRestoreTaskWithProvenance(launchCtx, taskID)
	if err != nil {
		return 0, err
	}
	taskEntity = validatedTask

	// 仅支持文件级同步执行器的恢复
	switch taskEntity.ExecutorType {
	case "rsync", "restic", "rclone":
	default:
		return 0, fmt.Errorf("该执行器类型（%s）不支持备份恢复", taskEntity.ExecutorType)
	}

	// 确定恢复目标路径
	restoreTo := strings.TrimSpace(targetPath)
	if restoreTo == "" {
		restoreTo = taskEntity.RsyncSource // 默认恢复到原始源路径
	}
	if err := validateRestorePath(restoreTo); err != nil {
		return 0, err
	}
	// Register before the durable reservation so a concurrent Cancel can abort
	// the reservation itself; registering only before goroutine launch still
	// leaves a committed-pending window.
	restoreTask := taskEntity
	restoreTask.RsyncSource = taskEntity.RsyncTarget // 备份目的地作为源
	restoreTask.RsyncTarget = restoreTo              // 恢复到目标路径
	execCtx, cancel := m.newRunContext(launchCtx, computeExecTimeout(restoreTask))
	ownership.addCancel(cancel)
	if err := launchCtx.Err(); err != nil {
		return 0, err
	}
	if err := execCtx.Err(); err != nil {
		return 0, err
	}

	// 恢复是破坏性操作，需要节点级互斥（比备份的策略级互斥更严格）。
	// nodeLock keeps the compatibility markers atomic in this process; the
	// caller-owned DB transaction below is the durable cross-process boundary.
	nLock := m.nodeLock(taskEntity.NodeID)
	nLock.Lock()
	if m.isNodeRestoring(taskEntity.NodeID) {
		nLock.Unlock()
		return 0, fmt.Errorf("同节点已有恢复任务正在运行，请稍候再试")
	}
	conflicted, err := m.hasNodeConflictForRestore(taskEntity)
	if err != nil {
		nLock.Unlock()
		return 0, err
	}
	if conflicted {
		nLock.Unlock()
		return 0, fmt.Errorf("同节点有任务正在运行，请稍候再试")
	}
	requestedRun := model.TaskRun{
		TaskID:            taskID,
		TriggerType:       "restore",
		Status:            model.TaskRunStatusPending,
		BackupSourceRunID: validatedTask.RsyncCaptureGenerationID,
	}
	run, err := m.reserveTaskRun(execCtx, taskEntity.NodeID, requestedRun)
	if err != nil {
		nLock.Unlock()
		if errors.Is(err, ErrNodeWriteConflict) {
			return 0, fmt.Errorf("同节点有恢复任务正在运行，请稍候再试: %w", err)
		}
		if errors.Is(err, ErrNodeWriteUnavailable) {
			return 0, fmt.Errorf("节点写入协调暂不可用，请稍候再试: %w", err)
		}
		return 0, fmt.Errorf("创建恢复执行记录失败: %w", err)
	}
	// Register only after the durable pending run commits. A rejected admission
	// therefore leaves neither a TaskRun nor an in-memory restore marker.
	m.restoreNodes.Store(taskEntity.NodeID, taskID)
	nodeIDForCleanup = taskEntity.NodeID
	nLock.Unlock()

	// Replace the reservation-only audit context with the exact durable TaskRun
	// binding before any credential or remote operation can begin.
	execCtx = m.withTaskCredentialAuditContext(execCtx, restoreTask, run.ID, "restore", map[string]any{
		"operation": "restore_task",
	})
	scheduled = true
	m.taskWG.Add(1)
	go func() {
		defer m.taskWG.Done()
		m.runRestoreTaskWithContext(taskID, run.ID, restoreTask, execCtx, ownership, ownership.cancel)
	}()
	return run.ID, nil
}

// validateRestorePath 校验恢复路径的安全性。
func validateRestorePath(path string) error {
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("恢复路径必须是绝对路径")
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("恢复路径不允许包含 '..'")
	}
	if strings.ContainsAny(path, ";|&$`\\\"'(){}[]<>!#~*?\n\r") {
		return fmt.Errorf("恢复路径包含非法字符")
	}
	forbidden := []string{"/", "/etc", "/usr", "/bin", "/sbin", "/boot"}
	cleanPath := strings.TrimRight(path, "/")
	if cleanPath == "" {
		cleanPath = "/"
	}
	for _, dir := range forbidden {
		if cleanPath == dir {
			return fmt.Errorf("禁止恢复到系统目录: %s", dir)
		}
	}
	return nil
}

func (m *Manager) Cancel(taskID uint) error {
	barrier := &taskCancelTriggerBarrier{}
	existing, loaded := m.pendingRuns.LoadOrStore(taskID, barrier)
	if loaded {
		if _, canceling := existing.(*taskCancelTriggerBarrier); canceling {
			return errTaskCancelInProgress
		}
		ownership, owned := existing.(*pendingRunOwnership)
		if !owned {
			return errTaskCancelInProgress
		}
		ownership.beginCancellationPersistence()
		defer ownership.markCancellationPersistenceSettled()
		if err := m.cancelLiveOwnedTask(taskID, false); err != nil {
			return err
		}
		if drillRunID, handedOff := ownership.handedOffDrillRunID(); handedOff {
			terminal, err := m.drillRunTerminal(drillRunID)
			if err != nil {
				logger.Module("task").Error().Err(err).Uint("task_id", taskID).Uint("task_run_id", drillRunID).
					Msg("验证恢复演练启动补偿终态失败")
				return errTaskCancelUnavailable
			}
			if !terminal {
				return errTaskCancelConflict
			}
			if ownership.claimDrillRecoveryCleanup(drillRunID) {
				m.chainRunner.Delete(taskID)
				m.pendingRuns.CompareAndDelete(taskID, ownership)
			}
		}
		return nil
	}

	// A legacy/direct runner may have registered only in chainRunner. Preserve
	// that live-owner path even though the barrier won pendingRuns.
	if _, owned := m.chainRunner.Load(taskID); owned {
		defer m.pendingRuns.CompareAndDelete(taskID, barrier)
		return m.cancelLiveOwnedTask(taskID, true)
	}
	defer m.pendingRuns.CompareAndDelete(taskID, barrier)

	status, err := m.reconcileOrphanTaskRuns(taskID)
	if err != nil {
		return err
	}
	m.logDispatcher.Dispatch(taskID, nil, "warn", "任务已取消", status)
	return nil
}

func (m *Manager) cancelLiveOwnedTask(taskID uint, signalBeforeRead bool) error {
	if signalBeforeRead && !m.cancelTaskRunOwner(taskID) {
		return errTaskCancelInProgress
	}
	var taskEntity model.Task
	if err := m.db.First(&taskEntity, taskID).Error; err != nil {
		return err
	}
	// Trigger-owned runners may be inside the executor-entry transaction. Keep
	// the established boundary by capturing the durable Task outcome before
	// signaling them; context-aware database transactions may otherwise abort
	// before committing the outcome cancellation must preserve and compensate.
	if !signalBeforeRead && !m.cancelTaskRunOwner(taskID) {
		return errTaskCancelInProgress
	}

	switch ParseStatus(taskEntity.Status) {
	case StatusPending, StatusRetrying:
		// Runners register their cancel function before competing for any
		// executor-entry lock. Signal it before the pending-row CAS so a start
		// transaction cannot advance after cancellation authority is observed.
		// Keep drill TaskRuns durable-canceled as well as signaling their owner.
		if _, err := m.cancelDrillTaskRuns(taskID, "任务已取消"); err != nil {
			return err
		}
		canceledRuns, err := m.cancelPendingTaskRuns(taskID, "任务已取消")
		if err != nil {
			return err
		}
		if canceledRuns == 0 && ParseStatus(taskEntity.Status) == StatusPending {
			// A retrying task may have only a failed predecessor plus a
			// durable retry effect; its cancellation still needs to move the
			// aggregate out of retrying even though no pending TaskRun exists.
			// A pending task with no runner remains owned by its entry path.
			m.logDispatcher.Dispatch(taskID, nil, "warn", "任务取消请求已发送", taskEntity.Status)
			return nil
		}
		if err := m.updateStatus(&taskEntity, StatusCanceled, map[string]interface{}{
			"next_run_at": nextCronRun(taskEntity.CronSpec),
			"last_error":  "任务已取消",
		}); err != nil {
			return err
		}
		m.logDispatcher.Dispatch(taskID, nil, "warn", "任务已取消", taskEntity.Status)
		return nil
	case StatusRunning:
		if _, err := m.cancelDrillTaskRuns(taskID, "任务已取消"); err != nil {
			return err
		}
		// Once a runner owns cancellation, it also owns the atomic terminal
		// update. An independent Task overwrite here can race its no-executor
		// compensation and destroy the exact pre-entry outcome.
		m.logDispatcher.Dispatch(taskID, nil, "warn", "任务已取消，正在终止执行进程", taskEntity.Status)
		return nil
	default:
		// Terminal-state Tasks may own either an ordinary or legacy-restore runner
		// between reservation and executor entry.
		if _, err := m.cancelDrillTaskRuns(taskID, "任务已取消"); err != nil {
			return err
		}
		if _, err := m.cancelPendingTaskRuns(taskID, "任务已取消"); err != nil {
			return err
		}
		m.logDispatcher.Dispatch(taskID, nil, "warn", "任务已取消", taskEntity.Status)
		return nil
	}
}

func (m *Manager) reconcileOrphanTaskRuns(taskID uint) (string, error) {
	finishedAt := time.Now().UTC()
	activeStatuses := model.TaskRunActiveStatuses()
	observedTaskStatus := ""
	err := m.db.Transaction(func(tx *gorm.DB) error {
		var taskEntity model.Task
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", taskID).Limit(1).Find(&taskEntity)
		if result.Error != nil {
			logger.Module("task").Error().Err(result.Error).Uint("task_id", taskID).Msg("加载待取消任务失败")
			return errTaskCancelUnavailable
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		observedTaskStatus = taskEntity.Status
		if !model.IsTaskRunNodeSnapshotAuthoritative(taskEntity.NodeID) {
			return errTaskCancelConflict
		}

		var runs []model.TaskRun
		if err := tx.Where("task_id = ? AND node_id_snapshot = ? AND status IN ?", taskID, taskEntity.NodeID, activeStatuses).
			Order("id ASC").Find(&runs).Error; err != nil {
			logger.Module("task").Error().Err(err).Uint("task_id", taskID).Msg("加载待取消执行记录失败")
			return errTaskCancelUnavailable
		}
		for i := range runs {
			run := &runs[i]
			if owner := strings.TrimSpace(run.ExecutionOwnerID); owner != "" &&
				owner != m.executionOwnerID &&
				(run.ExecutionLeaseUntil == nil || run.ExecutionLeaseUntil.After(finishedAt)) {
				return errTaskCancelConflict
			}
		}

		taskStatus := TaskStatus(taskEntity.Status)
		taskIsActive := taskStatus == StatusPending || taskStatus == StatusRunning || taskStatus == StatusRetrying
		taskIsTerminal := taskStatus == StatusSuccess || taskStatus == StatusFailed || taskStatus == StatusCanceled ||
			taskStatus == StatusWarning || taskStatus == StatusSkipped
		if taskIsActive && len(runs) == 0 {
			if taskStatus == StatusRetrying {
				var predecessorIDs []uint
				if err := tx.Model(&model.TaskRun{}).Where("task_id = ?", taskID).Pluck("id", &predecessorIDs).Error; err != nil {
					return errTaskCancelUnavailable
				}
				if len(predecessorIDs) > 0 {
					var retryEffects int64
					if err := tx.Model(&model.TaskRunEffect{}).
						Where("task_run_id IN ? AND effect_type = ? AND status <> ?",
							predecessorIDs, model.TaskRunEffectTypeRetry, model.TaskRunEffectStatusSucceeded).
						Count(&retryEffects).Error; err != nil {
						return errTaskCancelUnavailable
					}
					if retryEffects > 0 {
						updated := tx.Model(&model.Task{}).
							Where("id = ? AND status = ?", taskID, taskEntity.Status).
							Updates(map[string]interface{}{
								"status":      string(StatusCanceled),
								"next_run_at": nil,
								"last_error":  "任务已取消",
							})
						if updated.Error != nil || updated.RowsAffected != 1 {
							return errTaskCancelUnavailable
						}
						observedTaskStatus = string(StatusCanceled)
						return nil
					}
				}
			}
			return errTaskCancelConflict
		}
		if !taskIsActive && (!taskIsTerminal || len(runs) == 0) {
			return errTaskCancelUnsupported
		}

		for i := range runs {
			run := &runs[i]
			if run.TriggerType == "drill" {
				won, err := cancelOneDrillRunTx(tx, taskID, run.ID, taskEntity.NodeID, "任务已取消", finishedAt)
				if err != nil {
					logger.Module("task").Error().Err(err).Uint("task_id", taskID).Uint("task_run_id", run.ID).Msg("取消孤立恢复演练失败")
					return errTaskCancelUnavailable
				}
				if !won {
					return errTaskCancelConflict
				}
				continue
			}
			markNoStart := false
			if run.Status == model.TaskRunStatusPending {
				var markErr error
				markNoStart, markErr = rcloneNoStartStateForRunTx(tx, run)
				if markErr != nil {
					return errTaskCancelUnavailable
				}
			}
			durationMs := int64(0)
			if run.StartedAt != nil {
				durationMs = finishedAt.Sub(run.StartedAt.UTC()).Milliseconds()
				if durationMs < 0 {
					durationMs = 0
				}
			}
			updates := map[string]any{
				"status":                model.TaskRunStatusCanceled,
				"finished_at":           &finishedAt,
				"duration_ms":           durationMs,
				"last_error":            "任务已取消",
				"execution_owner_id":    "",
				"execution_lease_until": nil,
			}
			if run.Status == model.TaskRunStatusPending {
				updates["started_at"] = nil
				updates["duration_ms"] = int64(0)
			}
			if markNoStart {
				updates["backup_generation_state"] = model.TaskRunGenerationStateNoStart
			}
			updated := tx.Model(&model.TaskRun{}).
				Where(`id = ? AND task_id = ? AND node_id_snapshot = ? AND status = ? AND
					(execution_owner_id = '' OR execution_owner_id = ? OR
						execution_lease_until IS NULL OR execution_lease_until <= ?)`,
					run.ID, taskID, taskEntity.NodeID, run.Status, m.executionOwnerID, finishedAt).
				Updates(updates)
			if updated.Error != nil {
				logger.Module("task").Error().Err(updated.Error).Uint("task_id", taskID).Uint("task_run_id", run.ID).Msg("取消孤立执行记录失败")
				return errTaskCancelUnavailable
			}
			if updated.RowsAffected != 1 {
				return errTaskCancelConflict
			}
		}

		if !taskIsActive {
			return nil
		}
		updated := tx.Model(&model.Task{}).
			Where("id = ? AND node_id = ? AND status = ?", taskID, taskEntity.NodeID, taskEntity.Status).
			Updates(map[string]any{
				"status":      string(StatusCanceled),
				"next_run_at": nextCronRun(taskEntity.CronSpec),
				"last_error":  "任务已取消",
			})
		if updated.Error != nil {
			logger.Module("task").Error().Err(updated.Error).Uint("task_id", taskID).Msg("取消任务聚合状态失败")
			return errTaskCancelUnavailable
		}
		if updated.RowsAffected != 1 {
			return errTaskCancelConflict
		}
		return nil
	})
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) &&
		!errors.Is(err, errTaskCancelConflict) && !errors.Is(err, errTaskCancelUnavailable) &&
		!errors.Is(err, errTaskCancelUnsupported) {
		logger.Module("task").Error().Err(err).Uint("task_id", taskID).Msg("取消任务事务失败")
		err = errTaskCancelUnavailable
	}
	return observedTaskStatus, err
}

func (m *Manager) cancelTaskRunOwner(taskID uint) bool {
	owned := false
	if value, ok := m.pendingRuns.Load(taskID); ok {
		if ownership, ok := value.(*pendingRunOwnership); ok {
			ownership.cancel()
			owned = true
		}
	}
	if cancel, ok := m.chainRunner.Load(taskID); ok {
		cancel()
		owned = true
	}
	return owned
}

func (m *Manager) cancelPendingTaskRuns(taskID uint, message string) (int64, error) {
	canceledAt := time.Now().UTC()
	message = sanitizeTaskLastError(message)
	var canceled int64
	err := m.db.Transaction(func(tx *gorm.DB) error {
		var runs []model.TaskRun
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(`task_id = ?
				AND node_id_snapshot > ?
				AND node_id_snapshot = (SELECT node_id FROM tasks WHERE tasks.id = task_runs.task_id)
				AND status = ?`, taskID, model.TaskRunNodeIDLegacyUnknown, model.TaskRunStatusPending).
			Find(&runs)
		if result.Error != nil {
			return result.Error
		}
		for i := range runs {
			run := &runs[i]
			markNoStart, markErr := rcloneNoStartStateForRunTx(tx, run)
			if markErr != nil {
				return markErr
			}
			updates := map[string]interface{}{
				"status":                model.TaskRunStatusCanceled,
				"started_at":            nil,
				"finished_at":           &canceledAt,
				"duration_ms":           int64(0),
				"last_error":            message,
				"execution_owner_id":    "",
				"execution_lease_until": nil,
			}
			if markNoStart {
				updates["backup_generation_state"] = model.TaskRunGenerationStateNoStart
			}
			result = tx.Model(&model.TaskRun{}).
				Where("id = ? AND status = ?", run.ID, model.TaskRunStatusPending).
				Updates(updates)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errTaskRunCASLost
			}
			canceled++
		}
		return nil
	})
	return canceled, err
}

func (m *Manager) cancelOneDrillRun(
	taskID uint,
	drillRunID uint,
	message string,
	canceledAt time.Time,
) (bool, error) {
	canceledAt = canceledAt.UTC()
	message = sanitizeTaskLastError(message)
	won := false
	err := m.db.Transaction(func(tx *gorm.DB) error {
		var err error
		won, err = cancelOneDrillRunTx(tx, taskID, drillRunID, 0, message, canceledAt)
		return err
	})
	return won, err
}

func cancelOneDrillRunTx(
	tx *gorm.DB,
	taskID uint,
	drillRunID uint,
	expectedNodeID uint,
	message string,
	canceledAt time.Time,
) (bool, error) {
	var run model.TaskRun
	query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND task_id = ? AND trigger_type = ? AND status IN ?",
			drillRunID, taskID, "drill", model.TaskRunActiveStatuses())
	if model.IsTaskRunNodeSnapshotAuthoritative(expectedNodeID) {
		query = query.Where("node_id_snapshot = ?", expectedNodeID)
	}
	result := query.Limit(1).Find(&run)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}

	var evidence model.RestoreDrillEvidence
	evidenceResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("task_run_id = ? AND task_id = ?", drillRunID, taskID).Limit(1).Find(&evidence)
	if evidenceResult.Error != nil {
		return false, evidenceResult.Error
	}
	evidenceExists := evidenceResult.RowsAffected == 1
	if evidenceExists && evidence.Status != model.TaskRunStatusPending && evidence.Status != model.TaskRunStatusRunning {
		return false, fmt.Errorf("active restore drill has terminal evidence")
	}
	if run.Status != model.TaskRunStatusPending && !evidenceExists {
		return false, fmt.Errorf("active restore drill evidence is unavailable")
	}

	durationMs := int64(0)
	startedAt := run.StartedAt
	if run.Status != model.TaskRunStatusPending {
		if startedAt == nil {
			if run.Status != model.TaskRunStatusRetrying {
				return false, fmt.Errorf("active restore drill task run has no start time")
			}
		} else {
			durationMs = drillDurationMs(startedAt.UTC(), canceledAt)
		}
	} else {
		startedAt = nil
	}

	runQuery := tx.Model(&model.TaskRun{}).
		Where("id = ? AND task_id = ? AND trigger_type = ? AND status = ?",
			run.ID, taskID, "drill", run.Status)
	if model.IsTaskRunNodeSnapshotAuthoritative(expectedNodeID) {
		runQuery = runQuery.Where("node_id_snapshot = ?", expectedNodeID)
	}
	runResult := runQuery.Updates(map[string]interface{}{
		"status":                model.TaskRunStatusCanceled,
		"started_at":            startedAt,
		"finished_at":           &canceledAt,
		"duration_ms":           durationMs,
		"last_error":            message,
		"execution_owner_id":    "",
		"execution_lease_until": nil,
	})
	if runResult.Error != nil {
		return false, runResult.Error
	}
	if runResult.RowsAffected != 1 {
		return false, nil
	}

	if evidenceExists {
		updates := map[string]interface{}{
			"status":               model.TaskRunStatusCanceled,
			"failed_step":          "",
			"confidence_eligible":  false,
			"finished_at":          &canceledAt,
			"duration_ms":          durationMs,
			"recovery_owner_id":    "",
			"recovery_lease_until": nil,
			"updated_at":           canceledAt,
		}
		if run.Status == model.TaskRunStatusPending {
			updates["started_at"] = nil
		}
		addDrillCanceledPhase(updates, evidence, message, canceledAt)
		updatedEvidence := tx.Model(&model.RestoreDrillEvidence{}).
			Where("id = ? AND task_run_id = ? AND task_id = ? AND status = ?",
				evidence.ID, drillRunID, taskID, evidence.Status).
			Updates(updates)
		if updatedEvidence.Error != nil {
			return false, updatedEvidence.Error
		}
		if updatedEvidence.RowsAffected != 1 {
			return false, fmt.Errorf("restore drill evidence cancellation transition lost")
		}
	}
	return true, nil
}

func (m *Manager) cancelDrillTaskRuns(taskID uint, message string) (int64, error) {
	var runs []model.TaskRun
	if err := m.db.Select("id").
		Where("task_id = ? AND trigger_type = ? AND status IN ?", taskID, "drill", model.TaskRunActiveStatuses()).
		Order("id ASC").Find(&runs).Error; err != nil {
		return 0, err
	}
	canceledAt := time.Now().UTC()
	canceled := int64(0)
	for i := range runs {
		won, err := m.cancelOneDrillRun(taskID, runs[i].ID, message, canceledAt)
		if err != nil {
			return canceled, err
		}
		if won {
			canceled++
		}
	}
	return canceled, nil
}

// Pause 暂停任务：停止调度、阻止触发，保留任务配置和历史。
func (m *Manager) Pause(taskID uint, cancelRunning bool) error {
	var taskEntity model.Task
	if err := m.db.First(&taskEntity, taskID).Error; err != nil {
		return fmt.Errorf("任务不存在")
	}
	if !taskEntity.Enabled {
		return nil // 幂等
	}

	updates := map[string]interface{}{
		"enabled":     false,
		"skip_next":   false,
		"next_run_at": nil,
	}
	if err := m.db.Model(&taskEntity).Updates(updates).Error; err != nil {
		return err
	}
	taskEntity.Enabled = false

	m.RemoveSchedule(taskID)

	// 如果 retrying 状态，停止重试计时器并取消
	if ParseStatus(taskEntity.Status) == StatusRetrying {
		_ = m.updateStatus(&taskEntity, StatusCanceled, map[string]interface{}{
			"last_error": "任务已暂停",
		})
		m.logDispatcher.Dispatch(taskID, nil, "warn", "任务已暂停，重试已取消", taskEntity.Status)
	}

	// 如果要求取消当前运行
	if cancelRunning && ParseStatus(taskEntity.Status) == StatusRunning {
		_ = m.Cancel(taskID)
	}

	m.logDispatcher.Dispatch(taskID, nil, "info", "任务已暂停", taskEntity.Status)
	return nil
}

// Resume 恢复任务调度。
func (m *Manager) Resume(taskID uint) error {
	var taskEntity model.Task
	if err := m.db.First(&taskEntity, taskID).Error; err != nil {
		return fmt.Errorf("任务不存在")
	}
	if taskEntity.ArchivedAt != nil {
		return ErrTaskArchived
	}
	if taskEntity.Enabled {
		return nil // 幂等
	}

	updates := map[string]interface{}{
		"enabled": true,
	}
	if err := m.db.Model(&taskEntity).Updates(updates).Error; err != nil {
		return err
	}
	taskEntity.Enabled = true

	if taskEntity.CronSpec != "" {
		if err := m.SyncSchedule(taskEntity); err != nil {
			return err
		}
	}

	m.logDispatcher.Dispatch(taskID, nil, "info", "任务已恢复", taskEntity.Status)
	return nil
}

// SetSkipNext 设置跳过下次 cron 执行。
func (m *Manager) SetSkipNext(taskID uint) error {
	var taskEntity model.Task
	if err := m.db.First(&taskEntity, taskID).Error; err != nil {
		return fmt.Errorf("任务不存在")
	}
	if taskEntity.CronSpec == "" {
		return fmt.Errorf("仅定时任务支持跳过下次执行")
	}
	if !taskEntity.Enabled {
		return fmt.Errorf("任务已暂停，无需跳过")
	}
	if taskEntity.SkipNext {
		return nil // 幂等
	}
	if err := m.db.Model(&taskEntity).Update("skip_next", true).Error; err != nil {
		return err
	}
	m.logDispatcher.Dispatch(taskID, nil, "info", "已设置跳过下次执行", taskEntity.Status)
	return nil
}

func (m *Manager) StopAccepting() {
	m.shuttingDown.Store(true)
}

// Run owns bounded recovery sweeps for process-lost ordinary runs, durable
// terminal effects, and restore drills. It is intentionally independent from
// DrillAvailable so a deployment without drill transport still converges rows
// left by an earlier process.
func (m *Manager) Run(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, runCancel := context.WithCancel(ctx)
	runDone := make(chan struct{})
	m.managerRunMu.Lock()
	if m.shuttingDown.Load() || m.managerRunCancel != nil {
		m.managerRunMu.Unlock()
		runCancel()
		return
	}
	m.managerRunCancel = runCancel
	m.managerRunDone = runDone
	m.managerRunMu.Unlock()
	defer func() {
		runCancel()
		m.managerRunMu.Lock()
		if m.managerRunDone == runDone {
			m.managerRunCancel = nil
			m.managerRunDone = nil
		}
		close(runDone)
		m.managerRunMu.Unlock()
	}()
	ctx = runCtx

	if err := m.reconcileExpiredOrdinaryRuns(ctx); err != nil {
		if ctx.Err() == nil {
			logger.Module("task").Warn().Err(err).Msg("普通任务启动对账失败")
		}
	}
	if err := m.reconcileMissingRetryEffects(ctx); err != nil && ctx.Err() == nil {
		logger.Module("task").Warn().Err(err).Msg("缺失重试副作用启动对账失败")
	}
	if err := m.drainReadyTaskRunEffects(ctx); err != nil && ctx.Err() == nil {
		logger.Module("task").Warn().Err(err).Msg("任务终态副作用启动投递失败")
	}
	if err := m.reconcilePendingDurableRuns(ctx); err != nil && ctx.Err() == nil {
		logger.Module("task").Warn().Err(err).Msg("持久化待执行任务启动对账失败")
	}
	if err := m.reconcileSchedules(ctx); err != nil && ctx.Err() == nil {
		logger.Module("task").Warn().Err(err).Msg("任务调度启动对账失败")
	}
	if err := m.reconcileExpiredDrills(ctx); err != nil {
		m.drillRecoveryBlocked.Store(true)
		if ctx.Err() == nil {
			logger.Module("task").Warn().Err(err).Msg("恢复演练启动对账失败")
		}
	} else {
		m.drillRecoveryBlocked.Store(false)
	}

	interval := m.drillRecoveryInterval
	if interval <= 0 {
		interval = defaultDrillRecoveryInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.reconcileExpiredOrdinaryRuns(ctx); err != nil && ctx.Err() == nil {
				logger.Module("task").Warn().Err(err).Msg("普通任务周期对账失败")
			}
			if err := m.reconcileMissingRetryEffects(ctx); err != nil && ctx.Err() == nil {
				logger.Module("task").Warn().Err(err).Msg("缺失重试副作用周期对账失败")
			}
			if err := m.drainReadyTaskRunEffects(ctx); err != nil && ctx.Err() == nil {
				logger.Module("task").Warn().Err(err).Msg("任务终态副作用周期投递失败")
			}
			if err := m.reconcilePendingDurableRuns(ctx); err != nil && ctx.Err() == nil {
				logger.Module("task").Warn().Err(err).Msg("持久化待执行任务周期对账失败")
			}
			if err := m.reconcileSchedules(ctx); err != nil && ctx.Err() == nil {
				logger.Module("task").Warn().Err(err).Msg("任务调度周期对账失败")
			}
			if err := m.reconcileExpiredDrills(ctx); err != nil {
				m.drillRecoveryBlocked.Store(true)
				if ctx.Err() == nil {
					logger.Module("task").Warn().Err(err).Msg("恢复演练周期对账失败")
				}
			} else {
				m.drillRecoveryBlocked.Store(false)
			}
		}
	}
}

func (m *Manager) Shutdown(ctx context.Context) error {
	m.shuttingDown.Store(true)
	m.managerRunMu.Lock()
	runCancel := m.managerRunCancel
	runDone := m.managerRunDone
	m.managerRunMu.Unlock()
	if runCancel != nil {
		runCancel()
	}
	if runDone != nil {
		select {
		case <-runDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	m.chainRunner.CancelAll()

	if err := m.stopDrillLoop(ctx); err != nil {
		return err
	}
	taskDone := make(chan struct{})
	go func() {
		m.taskWG.Wait()
		close(taskDone)
	}()

	select {
	case <-taskDone:
	case <-ctx.Done():
		return ctx.Err()
	}

	if err := m.logDispatcher.Stop(ctx); err != nil {
		return err
	}
	if err := m.sampleWriter.Stop(ctx); err != nil {
		return err
	}
	if m.rootCancel != nil {
		m.rootCancel()
	}
	return nil
}

// dispatchDrillFailure 在恢复演练失败时向 automation.Dispatcher 派发事件。
func (m *Manager) dispatchDrillFailure(policyID, taskRunID uint) {
	if m.autoDispatcher == nil {
		return
	}
	_ = m.autoDispatcher.Dispatch(context.Background(), automation.Event{
		Type: automation.EventDrillFailed,
		Context: map[string]interface{}{
			"policy_id":   policyID,
			"task_run_id": taskRunID,
		},
	})
}

// cleanupExpiredTaskRuns removes TaskRun records older than taskRunRetentionDays
// while retaining the minimum capture evidence needed by restore admission.
// Candidate selection and dependent cleanup run in one transaction. The task
// rows are locked before the candidate predicate is evaluated again so a
// concurrent reservation and cleanup have one serialized answer about whether
// a source run is still referenced.
func (m *Manager) cleanupExpiredTaskRuns() {
	if m.taskRunRetentionDays <= 0 || m.db == nil {
		return
	}

	m.taskRunCleanupMu.Lock()
	defer m.taskRunCleanupMu.Unlock()

	now := time.Now().UTC()
	if !m.lastTaskRunCleanupAt.IsZero() && now.Sub(m.lastTaskRunCleanupAt) < defaultSampleCleanupInterval {
		return
	}

	cutoff := now.AddDate(0, 0, -m.taskRunRetentionDays)
	for {
		deleted, err := m.cleanupExpiredTaskRunBatch(cutoff)
		if err != nil {
			logger.Module("task").Warn().Err(err).Msg("清理过期执行记录失败")
			return
		}
		if deleted == 0 {
			break
		}
	}
	m.lastTaskRunCleanupAt = now
}

func expiredTaskRunCleanupQuery(tx *gorm.DB, cutoff time.Time) *gorm.DB {
	return tx.Model(&model.TaskRun{}).
		Where("task_runs.created_at < ? AND task_runs.status NOT IN ?", cutoff, model.TaskRunActiveStatuses()).
		Where("NOT EXISTS (SELECT 1 FROM task_run_effects AS effect WHERE effect.task_run_id = task_runs.id AND effect.status <> ?)", model.TaskRunEffectStatusSucceeded).
		// Unresolved mutable generations are never deletion candidates: they
		// are the durable operator hold after a crash or unknown remote result.
		// For known states, an older generation is removable only after a
		// newer ordinary generation reaches a terminal status.
		Where(`(
			(
				TRIM(COALESCE(task_runs.backup_generation_state, '')) = ''
				AND NOT (
					EXISTS (
						SELECT 1
						FROM tasks AS mutable_rclone_task
						WHERE mutable_rclone_task.id = task_runs.task_id
							AND lower(trim(mutable_rclone_task.executor_type)) = 'rclone'
					)
					AND NOT EXISTS (
						SELECT 1
						FROM task_runs AS newer_rclone
						WHERE newer_rclone.task_id = task_runs.task_id
							AND newer_rclone.node_id_snapshot = task_runs.node_id_snapshot
							AND lower(COALESCE(newer_rclone.trigger_type, '')) NOT IN ?
							AND TRIM(COALESCE(newer_rclone.backup_generation_state, '')) <> ?
							AND newer_rclone.id > task_runs.id
					)
				)
			)
			OR (
				TRIM(COALESCE(task_runs.backup_generation_state, '')) NOT IN ?
				AND EXISTS (
					SELECT 1
					FROM task_runs AS newer_generation
					WHERE newer_generation.task_id = task_runs.task_id
						AND newer_generation.node_id_snapshot = task_runs.node_id_snapshot
						AND lower(COALESCE(newer_generation.trigger_type, '')) NOT IN ?
						AND TRIM(COALESCE(newer_generation.backup_generation_state, '')) <> ''
						AND TRIM(COALESCE(newer_generation.backup_generation_state, '')) <> ?
						AND newer_generation.status IN ?
						AND newer_generation.id > task_runs.id
				)
			)
		)`,
			[]string{"restore", "drill"}, model.TaskRunGenerationStateNoStart,
			[]string{model.TaskRunGenerationStateWriting, model.TaskRunGenerationStateUnknown},
			[]string{"restore", "drill"}, model.TaskRunGenerationStateNoStart,
			model.TaskRunTerminalStatuses()).
		// A restore run may still need the capture represented by this row.
		// Keep the source for every durable binding, including terminal
		// historical restore rows; it becomes eligible once that binding is
		// itself removed.
		Where(`NOT EXISTS (
			SELECT 1
			FROM task_runs AS source_reference
			WHERE source_reference.backup_source_run_id = task_runs.id
		)`).
		// Drill evidence is retained only for active recovery work. Terminal
		// drill provenance is intentionally not a retention dependency here.
		Where(`NOT EXISTS (
			SELECT 1
			FROM restore_drill_evidences AS drill_evidence
			WHERE drill_evidence.source_task_run_id = task_runs.id
				AND (
					drill_evidence.status IN ?
					OR EXISTS (
						SELECT 1
						FROM task_runs AS drill_run
						WHERE drill_run.id = drill_evidence.task_run_id
							AND lower(drill_run.trigger_type) = ?
							AND drill_run.status IN ?
					)
				)
		)`, model.TaskRunActiveStatuses(), "drill", model.TaskRunActiveStatuses())
}

func (m *Manager) cleanupExpiredTaskRunBatch(cutoff time.Time) (int64, error) {
	var deleted int64
	err := m.db.Transaction(func(tx *gorm.DB) error {
		var candidateIDs []uint
		if err := expiredTaskRunCleanupQuery(tx, cutoff).
			Select("task_runs.id").
			Order("task_runs.id").
			Limit(defaultSampleCleanupBatchSize).
			Pluck("task_runs.id", &candidateIDs).Error; err != nil {
			return err
		}
		if len(candidateIDs) == 0 {
			return nil
		}

		var taskIDs []uint
		if err := tx.Model(&model.TaskRun{}).
			Where("id IN ?", candidateIDs).
			Distinct().
			Order("task_id").
			Pluck("task_id", &taskIDs).Error; err != nil {
			return err
		}
		if err := gormrepo.LockTaskIDsForUpdate(tx, taskIDs); err != nil {
			return err
		}

		// Re-evaluate under the same task-row locks used by reservation and
		// terminal transitions. Restricting to locked tasks prevents deleting a
		// run whose task was not part of this batch.
		candidateIDs = candidateIDs[:0]
		if err := expiredTaskRunCleanupQuery(tx, cutoff).
			Where("task_runs.task_id IN ?", taskIDs).
			Select("task_runs.id").
			Order("task_runs.id").
			Limit(defaultSampleCleanupBatchSize).
			Pluck("task_runs.id", &candidateIDs).Error; err != nil {
			return err
		}
		if len(candidateIDs) == 0 {
			return nil
		}

		if err := tx.Where("task_run_id IN ?", candidateIDs).Delete(&model.TaskLog{}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Alert{}).
			Where("task_run_id IN ?", candidateIDs).
			Update("task_run_id", nil).Error; err != nil {
			return err
		}
		result := tx.Where("id IN ?", candidateIDs).Delete(&model.TaskRun{})
		if result.Error != nil {
			return result.Error
		}
		deleted = result.RowsAffected
		return nil
	})
	return deleted, err
}
