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
	"xirang/backend/internal/settings"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/task/scheduler"
	"xirang/backend/internal/ws"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mattn/go-sqlite3"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
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
)

var (
	ErrNodeWriteConflict    = errors.New("node write conflict")
	ErrNodeWriteUnavailable = errors.New("node write admission unavailable")
	ErrNodeWriteStartLost   = errors.New("node write start compare-and-swap lost")
)

// NodeWriteAdmission serializes TaskRun reservations with durable Recovery
// node leases. The caller owns the transaction and inserts the pending run only
// after admission succeeds.
type NodeWriteAdmission interface {
	AdmitTaskTx(context.Context, *gorm.DB, uint) error
	EnterTaskExecutionTx(context.Context, *gorm.DB, uint, uint, time.Time) error
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

// chainContext 保存任务链的上下文信息，用于重试时恢复链路追踪
type chainContext struct {
	chainRunID string
}

type pendingRunOwnership struct {
	mu       sync.Mutex
	canceled bool
	cancels  []context.CancelFunc
}

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
	db                          *gorm.DB
	nodeWriteAdmission          NodeWriteAdmission
	nodeWriteRetryWait          func(context.Context, int) error
	runContextFactory           func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	stateMachine                *StateMachine
	executorFactory             executor.Factory
	hub                         *ws.Hub
	scheduler                   *scheduler.CronScheduler
	locks                       sync.Map
	strategyLocks               sync.Map
	nodeLocks                   sync.Map                                                         // nodeID → *sync.Mutex, 节点级互斥（restore 与普通任务共享）
	hookRunFunc                 func(ctx context.Context, task model.Task, command string) error // 可测试注入
	drillSSHScriptFunc          func(ctx context.Context, node model.Node, script string) error  // 可测试注入
	drillRestoreFunc            func(ctx context.Context, srcTask model.Task, sandboxNode model.Node, drillPath string, logf func(string, string)) error
	ensureRemoteTargetReadyFunc func(ctx context.Context, node model.Node, targetPath string) error
	pendingRuns                 sync.Map
	restoreNodes                sync.Map // nodeID → taskID, 持续跟踪有活跃恢复任务的节点
	retryTimers                 sync.Map
	retryChainContexts          sync.Map // taskID → chainContext
	semaphore                   chan struct{}
	taskWG                      sync.WaitGroup

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

	publicationCoordinator publication.Coordinator
	lineageGuard           publication.LineageGuard
	legacyBlockRecorder    publication.LegacyBlockRecorder
	resticRetentionFunc    func(context.Context, model.Policy, model.Task)

	rootCtx    context.Context    // worker goroutines 的父级 context
	rootCancel context.CancelFunc // 由 Shutdown 调用，通知所有 worker 退出

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
		db:                   db,
		nodeWriteRetryWait:   waitForNodeWriteReservationRetry,
		runContextFactory:    context.WithTimeout,
		stateMachine:         NewStateMachine(),
		executorFactory:      executorFactory,
		hub:                  hub,
		scheduler:            scheduler,
		semaphore:            make(chan struct{}, 8),
		hookRunFunc:          nil, // 初始化后设置为默认 runSSHHook
		taskRunRetentionDays: taskRunRetentionDays,
		settingsSvc:          settingsSvc,
		alertDispatcher:      alertDispatcher,
	}
	for _, option := range options {
		if option != nil {
			option(m)
		}
	}
	m.hookRunFunc = m.runSSHHook
	m.drillSSHScriptFunc = m.runDrillSSHScript
	m.drillRestoreFunc = m.restoreBackupToSandbox
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
	launchCtx, launchCancel := context.WithCancel(context.Background())
	ownership := &pendingRunOwnership{}
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

// SetLegacyBlockRecorder installs the typed audit/metric sink used after the
// lineage boundary blocks a legacy Restic operation.
func (m *Manager) SetLegacyBlockRecorder(recorder publication.LegacyBlockRecorder) {
	m.legacyBlockRecorder = recorder
}

// SetNodeWriteAdmission installs the durable Task/Recovery node boundary. It
// must be wired before schedules are loaded so every production trigger uses
// the same coordinator.
func (m *Manager) SetNodeWriteAdmission(admission NodeWriteAdmission) {
	m.nodeWriteAdmission = admission
}

func (m *Manager) reserveTaskRun(ctx context.Context, nodeID uint, requested model.TaskRun) (model.TaskRun, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	wait := m.nodeWriteRetryWait
	if wait == nil {
		wait = waitForNodeWriteReservationRetry
	}
	for attempt := 0; attempt < nodeWriteReservationAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return model.TaskRun{}, err
		}
		candidate := requested
		candidate.ID = 0
		candidate.NodeIDSnapshot = nodeID
		candidate.CreatedAt = time.Time{}
		candidate.UpdatedAt = time.Time{}
		err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if m.nodeWriteAdmission != nil {
				if err := m.nodeWriteAdmission.AdmitTaskTx(ctx, tx, nodeID); err != nil {
					return err
				}
			}
			return tx.Create(&candidate).Error
		})
		if err == nil {
			return candidate, nil
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

func (m *Manager) enterTaskExecution(
	ctx context.Context,
	runID uint,
	nodeID uint,
	startedAt time.Time,
	taskEntity *model.Task,
) error {
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

	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if m.nodeWriteAdmission != nil {
			if err := m.nodeWriteAdmission.EnterTaskExecutionTx(ctx, tx, runID, nodeID, startedAt); err != nil {
				return err
			}
		} else {
			result := tx.Model(&model.TaskRun{}).
				Where("id = ? AND node_id_snapshot = ? AND status = ?", runID, nodeID, "pending").
				Updates(map[string]interface{}{"status": "running", "started_at": &startedAt})
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
	if taskEntity != nil {
		taskEntity.Status = string(StatusRunning)
		taskEntity.LastRunAt = &startedAt
		taskEntity.NextRunAt = nil
		taskEntity.LastError = ""
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
		runResult := tx.Model(&model.TaskRun{}).
			Where("id = ? AND task_id = ? AND node_id_snapshot = ? AND status = ?", runID, taskID, nodeID, "running").
			Updates(map[string]interface{}{
				"status":      "canceled",
				"started_at":  nil,
				"finished_at": &canceledAt,
				"duration_ms": int64(0),
				"last_error":  message,
			})
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
	if errors.Is(err, ErrNodeWriteUnavailable) {
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
	var tasks []model.Task
	if err := m.db.WithContext(ctx).Where("cron_spec <> '' AND enabled = ?", true).Find(&tasks).Error; err != nil {
		return err
	}
	for _, one := range tasks {
		if err := m.SyncSchedule(one); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) SyncSchedule(task model.Task) error {
	if m.scheduler == nil {
		return nil
	}
	if !task.Enabled {
		m.RemoveSchedule(task.ID)
		return nil
	}
	// 持久化下次调度时间
	if next := nextCronRun(task.CronSpec); next != nil {
		m.db.Model(&model.Task{}).Where("id = ?", task.ID).Update("next_run_at", next)
	}
	return m.scheduler.RegisterTask(task.ID, task.CronSpec, func() {
		if err := m.TriggerFromScheduler(task.ID); err != nil {
			logger.Module("task").Warn().Uint("task_id", task.ID).Err(err).Msg("定时触发任务失败")
		}
	})
}

func (m *Manager) RemoveSchedule(taskID uint) {
	if m.scheduler == nil {
		return
	}
	m.scheduler.RemoveTask(taskID)
}

func (m *Manager) TriggerManual(taskID uint) (uint, error) {
	return m.triggerCore(taskID, "manual", generateChainRunID(), nil)
}

func (m *Manager) TriggerAutomation(taskID uint) (uint, error) {
	return m.triggerCore(taskID, "auto", generateChainRunID(), nil)
}

func (m *Manager) TriggerFromScheduler(taskID uint) error {
	_, err := m.triggerCore(taskID, "cron", generateChainRunID(), nil)
	return err
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
			m.pendingRuns.Delete(taskID)
		}
	}()

	var taskEntity model.Task
	if err := m.db.Preload("Node").Preload("Node.SSHKey").Preload("Policy").First(&taskEntity, taskID).Error; err != nil {
		return 0, fmt.Errorf("任务不存在")
	}

	// 仅支持文件级同步执行器的恢复
	switch taskEntity.ExecutorType {
	case "rsync", "restic", "rclone":
	default:
		return 0, fmt.Errorf("该执行器类型（%s）不支持备份恢复", taskEntity.ExecutorType)
	}

	// 校验是否有成功的执行记录
	var successCount int64
	if err := m.db.Model(&model.TaskRun{}).Where("task_id = ? AND status = ?", taskID, "success").Count(&successCount).Error; err != nil {
		return 0, err
	}
	if successCount == 0 {
		return 0, fmt.Errorf("该任务没有成功的执行记录，无法恢复")
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
		TaskID:      taskID,
		TriggerType: "restore",
		Status:      "pending",
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
		m.runRestoreTaskWithContext(taskID, run.ID, restoreTask, execCtx, ownership.cancel)
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
	var taskEntity model.Task
	if err := m.db.First(&taskEntity, taskID).Error; err != nil {
		return err
	}

	switch ParseStatus(taskEntity.Status) {
	case StatusPending, StatusRetrying:
		m.stopRetryTimer(taskID)
		m.retryChainContexts.Delete(taskID) // 清理重试链路上下文，防止泄漏
		// Runners register their cancel function before competing for any
		// executor-entry lock. Signal it before the pending-row CAS so a start
		// transaction cannot advance after cancellation authority is observed.
		runnerOwnsCancellation := m.cancelTaskRunOwner(taskID)
		canceledRuns, err := m.cancelPendingTaskRuns(taskID, "任务已取消")
		if err != nil {
			return err
		}
		if runnerOwnsCancellation && canceledRuns == 0 {
			// The runner may already have committed its atomic entry. It owns the
			// matching no-executor compensation and the captured Task snapshot.
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
		m.stopRetryTimer(taskID)
		if m.cancelTaskRunOwner(taskID) {
			// Once a runner owns cancellation, it also owns the atomic terminal
			// update. An independent Task overwrite here can race its no-executor
			// compensation and destroy the exact pre-entry outcome.
			m.logDispatcher.Dispatch(taskID, nil, "warn", "任务已取消，正在终止执行进程", taskEntity.Status)
			return nil
		}
		// Compatibility fallback for a persisted running Task with no live
		// process owner (for example after an older process crashed).
		var current struct{ Status string }
		if err := m.db.Model(&model.Task{}).Select("status").Where("id = ?", taskID).Take(&current).Error; err != nil {
			return err
		}
		// 只有在 runTask 尚未处理取消（状态仍为 running）时才主动写入 canceled。
		// runTask 检测到 ctx 取消后会将状态更新为 canceled，此处作为 fallback。
		if ParseStatus(current.Status) == StatusRunning {
			_ = m.updateStatus(&taskEntity, StatusCanceled, map[string]interface{}{
				"next_run_at": nextCronRun(taskEntity.CronSpec),
				"last_error":  "任务已取消",
			})
		}
		m.logDispatcher.Dispatch(taskID, nil, "warn", "任务已取消，正在终止执行进程", taskEntity.Status)
		return nil
	default:
		// Terminal-state Tasks may own either an ordinary or legacy-restore runner
		// between reservation and executor entry.
		if m.cancelTaskRunOwner(taskID) {
			if _, err := m.cancelPendingTaskRuns(taskID, "任务已取消"); err != nil {
				return err
			}
			m.logDispatcher.Dispatch(taskID, nil, "warn", "任务已取消", taskEntity.Status)
			return nil
		}
		return fmt.Errorf("仅支持取消待执行、重试中或运行中的任务")
	}
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
	result := m.db.Model(&model.TaskRun{}).
		Where("task_id = ? AND status = ?", taskID, "pending").
		Updates(map[string]interface{}{
			"status":      "canceled",
			"started_at":  nil,
			"finished_at": &canceledAt,
			"duration_ms": int64(0),
			"last_error":  message,
		})
	return result.RowsAffected, result.Error
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
		m.stopRetryTimer(taskID)
		m.retryChainContexts.Delete(taskID)
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

// Run blocks until ctx is done. Implements lifecycle.Worker. The Manager
// owns no goroutine of its own -- task execution is driven by the cron
// scheduler injected at construction. Run exists so main.go can drive the
// Manager through the same lifecycle.Worker slice as every other worker.
func (m *Manager) Run(ctx context.Context) {
	<-ctx.Done()
}

func (m *Manager) Shutdown(ctx context.Context) error {
	m.shuttingDown.Store(true)
	m.stopAllRetryTimers()

	m.chainRunner.CancelAll()

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

// dispatchAutomation 在任务完成/失败时向 automation.Dispatcher 派发事件。
// 若 dispatcher 未注入（nil），则静默跳过（向后兼容）。
func (m *Manager) dispatchAutomation(eventType string, taskEntity model.Task, runIDPtr *uint) {
	if m.autoDispatcher == nil {
		return
	}
	var policyID uint
	if taskEntity.PolicyID != nil {
		policyID = *taskEntity.PolicyID
	}
	var runID uint
	if runIDPtr != nil {
		runID = *runIDPtr
	}
	_ = m.autoDispatcher.Dispatch(context.Background(), automation.Event{
		Type: eventType,
		Context: map[string]interface{}{
			"task_id":       taskEntity.ID,
			"task_run_id":   runID,
			"policy_id":     policyID,
			"node_id":       taskEntity.NodeID,
			"executor_type": taskEntity.ExecutorType,
			"status":        taskEntity.Status,
		},
	})
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

// cleanupExpiredTaskRuns removes TaskRun records older than taskRunRetentionDays.
// Called periodically by LogDispatcher's worker tick.
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
		var ids []uint
		if err := m.db.Model(&model.TaskRun{}).Where("created_at < ?", cutoff).Order("id asc").Limit(defaultSampleCleanupBatchSize).Pluck("id", &ids).Error; err != nil {
			logger.Module("task").Warn().Err(err).Msg("查询过期执行记录失败")
			return
		}
		if len(ids) == 0 {
			break
		}
		// 级联清理：删除关联 TaskLog，清除关联 Alert 的 run 引用
		if err := m.db.Where("task_run_id IN ?", ids).Delete(&model.TaskLog{}).Error; err != nil {
			logger.Module("task").Warn().Err(err).Msg("清理过期执行记录关联日志失败")
			return
		}
		if err := m.db.Model(&model.Alert{}).Where("task_run_id IN ?", ids).Update("task_run_id", nil).Error; err != nil {
			logger.Module("task").Warn().Err(err).Msg("清除过期执行记录关联告警引用失败")
			return
		}
		if err := m.db.Where("id IN ?", ids).Delete(&model.TaskRun{}).Error; err != nil {
			logger.Module("task").Warn().Err(err).Msg("清理过期执行记录失败")
			return
		}
		if len(ids) < defaultSampleCleanupBatchSize {
			break
		}
	}
	m.lastTaskRunCleanupAt = now
}
