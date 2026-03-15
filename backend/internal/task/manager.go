package task

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xirang/backend/internal/logger"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/task/scheduler"
	"xirang/backend/internal/task/verifier"
	"xirang/backend/internal/ws"

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
)

// chainContext 保存任务链的上下文信息，用于重试时恢复链路追踪
type chainContext struct {
	chainRunID string
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
	db              *gorm.DB
	stateMachine    *StateMachine
	executorFactory executor.Factory
	hub             *ws.Hub
	scheduler       *scheduler.CronScheduler
	locks           sync.Map
	strategyLocks   sync.Map
	nodeLocks       sync.Map // nodeID → *sync.Mutex, 节点级互斥（restore 与普通任务共享）
	runningCancels  sync.Map
	pendingRuns     sync.Map
	restoreNodes    sync.Map // nodeID → taskID, 持续跟踪有活跃恢复任务的节点
	retryTimers          sync.Map
	retryChainContexts   sync.Map // taskID → chainContext
	semaphore            chan struct{}
	taskWG          sync.WaitGroup

	logQueue         chan queuedTaskLog
	logBatchSize     int
	logFlushInterval time.Duration
	logWorkerCancel  context.CancelFunc
	logWorkerDone    chan struct{}

	sampleQueue            chan queuedTaskSample
	sampleBatchSize        int
	sampleFlushInterval    time.Duration
	sampleWorkerCancel     context.CancelFunc
	sampleWorkerDone       chan struct{}
	lastSampleBucketByTask sync.Map
	sampleRetentionDays    int
	lastSampleCleanupAt    time.Time
	sampleCleanupMu        sync.Mutex

	taskRunRetentionDays    int
	lastTaskRunCleanupAt    time.Time
	taskRunCleanupMu        sync.Mutex

	shuttingDown atomic.Bool
}

func NewManager(db *gorm.DB, executorFactory executor.Factory, hub *ws.Hub, scheduler *scheduler.CronScheduler, sampleRetentionDays int, taskRunRetentionDays int) *Manager {
	m := &Manager{
		db:                   db,
		stateMachine:         NewStateMachine(),
		executorFactory:      executorFactory,
		hub:                  hub,
		scheduler:            scheduler,
		semaphore:            make(chan struct{}, 8),
		logQueue:             make(chan queuedTaskLog, defaultLogQueueCapacity),
		logBatchSize:         defaultLogBatchSize,
		logFlushInterval:     defaultLogFlushInterval,
		logWorkerDone:        make(chan struct{}),
		sampleQueue:          make(chan queuedTaskSample, defaultSampleQueueCapacity),
		sampleBatchSize:      defaultSampleBatchSize,
		sampleFlushInterval:  defaultSampleFlushInterval,
		sampleWorkerDone:     make(chan struct{}),
		sampleRetentionDays:  sampleRetentionDays,
		taskRunRetentionDays: taskRunRetentionDays,
	}
	m.startLogWorker()
	m.startSampleWorker()
	return m
}

func (m *Manager) startLogWorker() {
	ctx, cancel := context.WithCancel(context.Background())
	m.logWorkerCancel = cancel
	go m.runLogWorker(ctx)
}

func (m *Manager) startSampleWorker() {
	ctx, cancel := context.WithCancel(context.Background())
	m.sampleWorkerCancel = cancel
	go m.runSampleWorker(ctx)
}

func (m *Manager) runLogWorker(ctx context.Context) {
	defer close(m.logWorkerDone)

	ticker := time.NewTicker(m.logFlushInterval)
	defer ticker.Stop()

	batch := make([]queuedTaskLog, 0, m.logBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		m.persistLogBatch(batch)
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case item := <-m.logQueue:
					batch = append(batch, item)
					if len(batch) >= m.logBatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case item := <-m.logQueue:
			batch = append(batch, item)
			if len(batch) >= m.logBatchSize {
				flush()
			}
		case <-ticker.C:
			m.cleanupExpiredTaskRuns()
			flush()
		}
	}
}

func (m *Manager) runSampleWorker(ctx context.Context) {
	defer close(m.sampleWorkerDone)

	ticker := time.NewTicker(m.sampleFlushInterval)
	defer ticker.Stop()

	batch := make([]queuedTaskSample, 0, m.sampleBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		m.persistSampleBatch(batch)
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case item := <-m.sampleQueue:
					batch = append(batch, item)
					if len(batch) >= m.sampleBatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case item := <-m.sampleQueue:
			batch = append(batch, item)
			if len(batch) >= m.sampleBatchSize {
				flush()
			}
		case <-ticker.C:
			m.cleanupExpiredTrafficSamples()
			flush()
		}
	}
}

func (m *Manager) LoadSchedules(ctx context.Context) error {
	var tasks []model.Task
	if err := m.db.WithContext(ctx).Where("cron_spec <> ''").Find(&tasks).Error; err != nil {
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
	if _, loaded := m.pendingRuns.LoadOrStore(taskID, struct{}{}); loaded {
		return 0, fmt.Errorf("该任务正在执行中，请勿重复触发")
	}
	scheduled := false
	nodeIDForCleanup := uint(0)
	defer func() {
		if !scheduled {
			m.pendingRuns.Delete(taskID)
			// 如果 restoreNodes 已注册但 goroutine 未启动，需要清理
			if nodeIDForCleanup > 0 {
				m.restoreNodes.Delete(nodeIDForCleanup)
			}
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
	m.db.Model(&model.TaskRun{}).Where("task_id = ? AND status = ?", taskID, "success").Count(&successCount)
	if successCount == 0 {
		return 0, fmt.Errorf("该任务没有成功的执行记录，无法恢复")
	}

	// 恢复是破坏性操作，需要节点级互斥（比备份的策略级互斥更严格）。
	// 使用 nodeLock 保证冲突检查与 restoreNodes 注册的原子性，
	// 与 runTask() 中的 isNodeRestoring+updateStatus(running) 互斥。
	nLock := m.nodeLock(taskEntity.NodeID)
	nLock.Lock()
	// 1. 内存级检查：是否已有恢复任务正在运行
	if m.isNodeRestoring(taskEntity.NodeID) {
		nLock.Unlock()
		return 0, fmt.Errorf("同节点已有恢复任务正在运行，请稍候再试")
	}
	// 2. DB 级检查：是否有普通任务正在运行（Task.Status = running）
	conflicted, err := m.hasNodeConflictForRestore(taskEntity)
	if err != nil {
		nLock.Unlock()
		return 0, err
	}
	if conflicted {
		nLock.Unlock()
		return 0, fmt.Errorf("同节点有任务正在运行，请稍候再试")
	}
	// 在同步路径中、nodeLock 保护下标记节点正在恢复
	m.restoreNodes.Store(taskEntity.NodeID, taskID)
	nodeIDForCleanup = taskEntity.NodeID
	nLock.Unlock()

	// 确定恢复目标路径
	restoreTo := strings.TrimSpace(targetPath)
	if restoreTo == "" {
		restoreTo = taskEntity.RsyncSource // 默认恢复到原始源路径
	}
	if err := validateRestorePath(restoreTo); err != nil {
		return 0, err
	}

	// 创建恢复执行记录
	run := model.TaskRun{
		TaskID:      taskID,
		TriggerType: "restore",
		Status:      "pending",
	}
	if err := m.db.Create(&run).Error; err != nil {
		return 0, fmt.Errorf("创建恢复执行记录失败: %w", err)
	}

	scheduled = true
	m.taskWG.Add(1)
	go func() {
		defer m.taskWG.Done()
		// 恢复模式：source=备份路径(远程), target=恢复目标路径(远程)
		restoreTask := taskEntity
		restoreTask.RsyncSource = taskEntity.RsyncTarget // 备份目的地作为源
		restoreTask.RsyncTarget = restoreTo              // 恢复到目标路径
		m.runRestoreTask(taskID, run.ID, restoreTask)
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

// runRestoreTask 执行恢复任务。与 runTask 不同，恢复不影响原始 Task 的状态，
// 仅更新 TaskRun 记录。使用内存中交换了 source/target 的任务副本。
func (m *Manager) runRestoreTask(taskID uint, runID uint, restoreTask model.Task) {
	defer m.pendingRuns.Delete(taskID)

	// 尽早注册取消句柄，使排队等锁期间也能被 Cancel() 中断
	execCtx, cancel := context.WithCancel(context.Background())
	m.runningCancels.Store(taskID, cancel)
	defer m.runningCancels.Delete(taskID)
	defer cancel()

	// restoreNodes 已在 TriggerRestore 同步路径中注册，此处仅负责清理
	defer m.restoreNodes.Delete(restoreTask.NodeID)

	runCompleted := false
	defer func() {
		if !runCompleted {
			now := time.Now()
			m.db.Model(&model.TaskRun{}).Where("id = ?", runID).
				Updates(map[string]interface{}{
					"status":      "failed",
					"finished_at": &now,
					"last_error":  "恢复任务启动前异常退出",
				})
		}
	}()

	// context-aware semaphore：取消时立即返回
	select {
	case m.semaphore <- struct{}{}:
	case <-execCtx.Done():
		finishedAt := time.Now()
		m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
			"status":      "canceled",
			"finished_at": &finishedAt,
			"last_error":  "恢复任务已取消",
		})
		runCompleted = true
		return
	}
	defer func() { <-m.semaphore }()

	// context-aware lock：取消时立即返回
	lock := m.taskLock(taskID)
	if !acquireLockWithContext(execCtx, lock) {
		finishedAt := time.Now()
		m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
			"status":      "canceled",
			"finished_at": &finishedAt,
			"last_error":  "恢复任务已取消",
		})
		runCompleted = true
		return
	}
	defer lock.Unlock()

	strategyLock := m.strategyLock(restoreTask.NodeID, restoreTask.PolicyID)
	if !acquireLockWithContext(execCtx, strategyLock) {
		finishedAt := time.Now()
		m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
			"status":      "canceled",
			"finished_at": &finishedAt,
			"last_error":  "恢复任务已取消",
		})
		runCompleted = true
		return
	}
	defer strategyLock.Unlock()

	runIDPtr := &runID

	now := time.Now()
	m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
		"status":     "running",
		"started_at": &now,
	})

	m.emitLog(taskID, runIDPtr, "info", fmt.Sprintf("开始恢复任务，源: %s → 目标: %s", restoreTask.RsyncSource, restoreTask.RsyncTarget), "")

	// 恢复前检查：在远程节点上检查源路径（备份）和目标路径
	m.emitLog(taskID, runIDPtr, "info", "执行恢复前检查（目标路径、磁盘空间）", "")
	if err := executor.EnsureRemoteTargetReady(execCtx, restoreTask.Node, restoreTask.RsyncTarget); err != nil {
		// 区分取消与真实失败
		if execCtx.Err() != nil {
			finishedAt := time.Now()
			m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
				"status":      "canceled",
				"finished_at": &finishedAt,
				"last_error":  "恢复任务已取消",
			})
			runCompleted = true
			m.emitLog(taskID, runIDPtr, "warn", "恢复前检查期间任务已取消", "")
			return
		}
		errorMsg := fmt.Sprintf("恢复前检查失败（目标路径）: %s", err.Error())
		finishedAt := time.Now()
		duration := finishedAt.Sub(now).Milliseconds()
		m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
			"status":      "failed",
			"finished_at": &finishedAt,
			"duration_ms": duration,
			"last_error":  errorMsg,
		})
		runCompleted = true
		m.emitLog(taskID, runIDPtr, "error", errorMsg, "")
		alerting.RaiseTaskFailure(m.db, restoreTask, runIDPtr, errorMsg)
		return
	}
	m.emitLog(taskID, runIDPtr, "info", "恢复前检查通过", "")

	// 通过 RestoreExecutor 接口在远程节点上执行恢复
	exec := m.executorFactory.Resolve(restoreTask.ExecutorType)
	restoreExec, ok := exec.(executor.RestoreExecutor)
	if !ok {
		errorMsg := "该执行器类型不支持恢复操作"
		finishedAt := time.Now()
		duration := finishedAt.Sub(now).Milliseconds()
		m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
			"status":      "failed",
			"finished_at": &finishedAt,
			"duration_ms": duration,
			"last_error":  errorMsg,
		})
		runCompleted = true
		m.emitLog(taskID, runIDPtr, "error", errorMsg, "")
		return
	}

	_, err := restoreExec.RunRestore(execCtx, restoreTask, func(level, message string) {
		m.emitLog(taskID, runIDPtr, level, message, "running")
	}, nil)

	// 检查是否被取消
	wasCanceled := errors.Is(err, context.Canceled) || errors.Is(execCtx.Err(), context.Canceled)
	if wasCanceled {
		finishedAt := time.Now()
		m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
			"status":      "canceled",
			"finished_at": &finishedAt,
			"last_error":  "恢复任务已取消",
		})
		runCompleted = true
		m.emitLog(taskID, runIDPtr, "warn", "恢复任务已取消", "")
		return
	}

	if err != nil {
		errorMsg := err.Error()
		finishedAt := time.Now()
		duration := finishedAt.Sub(now).Milliseconds()
		m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
			"status":      "failed",
			"finished_at": &finishedAt,
			"duration_ms": duration,
			"last_error":  errorMsg,
		})
		runCompleted = true
		m.emitLog(taskID, runIDPtr, "error", fmt.Sprintf("恢复任务失败: %s", errorMsg), "")
		alerting.RaiseTaskFailure(m.db, restoreTask, runIDPtr, errorMsg)
		return
	}

	// 恢复成功后强制执行完整性校验（不再依赖 Policy.VerifyEnabled）
	verifyStatus := "none"
	sampleRate := 100 // 默认全量校验
	if restoreTask.Policy != nil && restoreTask.Policy.VerifySampleRate > 0 {
		sampleRate = restoreTask.Policy.VerifySampleRate
	}

	m.emitLog(taskID, runIDPtr, "info", fmt.Sprintf("开始恢复后完整性校验（采样率 %d%%）", sampleRate), "")
	result := verifier.Verify(execCtx, restoreTask, sampleRate, m.db, func(level, msg string) {
		m.emitLog(taskID, runIDPtr, level, msg, "")
	}, true)

	// 校验期间可能被取消
	if execCtx.Err() != nil {
		finishedAt := time.Now()
		m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
			"status":      "canceled",
			"finished_at": &finishedAt,
			"last_error":  "恢复任务已取消",
		})
		runCompleted = true
		m.emitLog(taskID, runIDPtr, "warn", "恢复校验期间任务已取消", "")
		return
	}

	verifyStatus = result.Status

	if result.Status == "warning" || result.Status == "failed" {
		finishedAt := time.Now()
		duration := finishedAt.Sub(now).Milliseconds()
		m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
			"status":        "warning",
			"finished_at":   &finishedAt,
			"duration_ms":   duration,
			"verify_status": verifyStatus,
			"last_error":    result.Message,
		})
		runCompleted = true
		m.emitLog(taskID, runIDPtr, "warn", "恢复后校验未通过: "+result.Message, "")
		alerting.RaiseVerificationFailure(m.db, restoreTask, runIDPtr, result.Message)
		return
	}
	m.emitLog(taskID, runIDPtr, "info", "恢复后完整性校验通过", "")

	finishedAt := time.Now()
	duration := finishedAt.Sub(now).Milliseconds()
	m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
		"status":        "success",
		"finished_at":   &finishedAt,
		"duration_ms":   duration,
		"verify_status": verifyStatus,
		"last_error":    "",
	})
	runCompleted = true
	m.emitLog(taskID, runIDPtr, "info", "恢复任务执行成功", "")
	if resolveErr := alerting.ResolveTaskAlerts(m.db, taskID, "恢复任务成功"); resolveErr != nil {
		logger.Module("task").Warn().Uint("task_id", taskID).Err(resolveErr).Msg("ResolveTaskAlerts 失败")
	}
}

// trigger 是 triggerCore 的包装，负责在重试时恢复链路上下文。
func (m *Manager) trigger(taskID uint, reason string) (uint, error) {
	chainRunID := generateChainRunID()
	if reason == "retry" {
		if val, ok := m.retryChainContexts.LoadAndDelete(taskID); ok {
			chainRunID = val.(chainContext).chainRunID
		}
	}
	return m.triggerCore(taskID, reason, chainRunID, nil)
}

func (m *Manager) triggerCore(taskID uint, reason string, chainRunID string, upstreamRunID *uint) (uint, error) {
	if m.shuttingDown.Load() {
		if reason == "retry" || reason == "cron" || reason == "chain" {
			return 0, nil
		}
		return 0, fmt.Errorf("系统维护中，请稍候再试")
	}

	if _, loaded := m.pendingRuns.LoadOrStore(taskID, struct{}{}); loaded {
		if reason == "retry" || reason == "cron" || reason == "chain" {
			return 0, nil
		}
		return 0, fmt.Errorf("该任务正在执行中，请勿重复触发")
	}
	scheduled := false
	defer func() {
		if !scheduled {
			m.pendingRuns.Delete(taskID)
		}
	}()

	var taskEntity model.Task
	result := m.db.Where("id = ?", taskID).Limit(1).Find(&taskEntity)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected == 0 {
		if reason == "retry" || reason == "cron" || reason == "chain" {
			return 0, nil
		}
		return 0, fmt.Errorf("任务不存在")
	}

	if reason == "retry" {
		current := ParseStatus(taskEntity.Status)
		if current != StatusRetrying {
			return 0, fmt.Errorf("当前任务状态不支持重试，请稍候再试")
		}
	}
	if ParseStatus(taskEntity.Status) == StatusRunning {
		return 0, fmt.Errorf("该任务正在执行中，请勿重复触发")
	}
	// 手动触发时，阻止有前置依赖的任务被直接执行，需从头节点触发
	if reason == "manual" && taskEntity.DependsOnTaskID != nil && *taskEntity.DependsOnTaskID > 0 {
		return 0, fmt.Errorf("该任务有前置依赖（任务 ID: %d），请从链头节点触发", *taskEntity.DependsOnTaskID)
	}

	conflicted, err := m.hasRunningConflict(taskEntity)
	if err != nil {
		return 0, err
	}
	if conflicted {
		return 0, fmt.Errorf("同节点有任务正在运行，请稍候再试")
	}
	// 检查同节点是否有恢复任务正在运行（恢复是破坏性操作，需要节点级互斥）
	if m.isNodeRestoring(taskEntity.NodeID) {
		return 0, fmt.Errorf("同节点有恢复任务正在运行，请稍候再试")
	}

	// 创建 TaskRun 执行记录
	run := model.TaskRun{
		TaskID:            taskID,
		TriggerType:       reason,
		Status:            "pending",
		ChainRunID:        chainRunID,
		UpstreamTaskRunID: upstreamRunID,
	}
	if err := m.db.Create(&run).Error; err != nil {
		return 0, fmt.Errorf("创建执行记录失败: %w", err)
	}

	m.stopRetryTimer(taskID)
	scheduled = true
	m.taskWG.Add(1)
	go func() {
		defer m.taskWG.Done()
		m.runTask(taskID, run.ID, reason, chainRunID)
	}()
	return run.ID, nil
}

func (m *Manager) runTask(taskID uint, runID uint, reason string, chainRunID string) {
	defer m.pendingRuns.Delete(taskID)

	runCompleted := false
	defer func() {
		if !runCompleted {
			now := time.Now()
			m.db.Model(&model.TaskRun{}).Where("id = ?", runID).
				Updates(map[string]interface{}{
					"status":      "failed",
					"finished_at": &now,
					"last_error":  "任务启动前异常退出",
				})
		}
	}()

	m.semaphore <- struct{}{}
	defer func() { <-m.semaphore }()

	lock := m.taskLock(taskID)
	lock.Lock()
	defer lock.Unlock()

	runIDPtr := &runID

	var taskEntity model.Task
	if err := m.db.Preload("Node").Preload("Node.SSHKey").Preload("Policy").First(&taskEntity, taskID).Error; err != nil {
		m.emitLog(taskID, runIDPtr, "error", fmt.Sprintf("加载任务失败: %v", err), "")
		return
	}

	// 检查节点是否处于维护窗口
	checkTime := time.Now()
	if taskEntity.Node.MaintenanceStart != nil && taskEntity.Node.MaintenanceEnd != nil &&
		checkTime.After(*taskEntity.Node.MaintenanceStart) && checkTime.Before(*taskEntity.Node.MaintenanceEnd) {
		canceledAt := time.Now()
		m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
			"status":      "canceled",
			"finished_at": &canceledAt,
			"last_error":  "节点处于维护窗口",
		})
		runCompleted = true
		m.emitLog(taskID, runIDPtr, "warn", "节点处于维护窗口，跳过执行", "")
		return
	}

	currentStatus := ParseStatus(taskEntity.Status)
	if currentStatus == StatusRunning {
		m.emitLog(taskID, runIDPtr, "warn", "任务已在运行，忽略重复触发", taskEntity.Status)
		return
	}

	strategyLock := m.strategyLock(taskEntity.NodeID, taskEntity.PolicyID)
	strategyLock.Lock()
	defer strategyLock.Unlock()

	// 使用 nodeLock 保证 isNodeRestoring 检查与 updateStatus(running) 的原子性，
	// 与 TriggerRestore() 中的 hasNodeConflictForRestore+restoreNodes.Store 互斥。
	nLock := m.nodeLock(taskEntity.NodeID)
	nLock.Lock()

	conflicted, err := m.hasRunningConflict(taskEntity)
	if err != nil {
		nLock.Unlock()
		m.emitLog(taskID, runIDPtr, "error", fmt.Sprintf("校验互斥冲突失败: %v", err), taskEntity.Status)
		return
	}
	if conflicted {
		nLock.Unlock()
		m.emitLog(taskID, runIDPtr, "warn", "同节点有任务正在运行，忽略重复执行", taskEntity.Status)
		return
	}
	if m.isNodeRestoring(taskEntity.NodeID) {
		nLock.Unlock()
		m.emitLog(taskID, runIDPtr, "warn", "同节点有恢复任务正在运行，忽略执行", taskEntity.Status)
		return
	}

	if currentStatus == StatusSuccess || currentStatus == StatusFailed || currentStatus == StatusCanceled || currentStatus == StatusWarning {
		if err := m.updateStatus(&taskEntity, StatusPending, map[string]interface{}{"last_error": ""}); err != nil {
			nLock.Unlock()
			m.emitLog(taskID, runIDPtr, "error", fmt.Sprintf("切换 pending 失败: %v", err), taskEntity.Status)
			return
		}
	}

	now := time.Now()
	m.lastSampleBucketByTask.Delete(taskID)
	if err := m.updateStatus(&taskEntity, StatusRunning, map[string]interface{}{
		"last_run_at": now,
		"next_run_at": nil,
		"last_error":  "",
	}); err != nil {
		nLock.Unlock()
		m.emitLog(taskID, runIDPtr, "error", fmt.Sprintf("切换 running 失败: %v", err), taskEntity.Status)
		return
	}
	// 同步更新 TaskRun 为 running
	m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
		"status":     "running",
		"started_at": &now,
	})
	nLock.Unlock()

	m.emitLog(taskID, runIDPtr, "info", fmt.Sprintf("任务开始执行，触发来源: %s", reason), taskEntity.Status)

	execCtx, cancel := context.WithCancel(context.Background())
	m.runningCancels.Store(taskID, cancel)
	defer m.runningCancels.Delete(taskID)

	exec := m.executorFactory.Resolve(taskEntity.ExecutorType)
	runStartedAt := now.UTC()
	exitCode, err := exec.Run(execCtx, taskEntity, func(level, message string) {
		m.emitLog(taskID, runIDPtr, level, message, string(StatusRunning))
	}, func(sample executor.ProgressSample) {
		m.emitTrafficSample(taskID, taskEntity.NodeID, runStartedAt, sample)
	})

	wasCanceled := errors.Is(err, context.Canceled) || errors.Is(execCtx.Err(), context.Canceled) || m.isCanceled(taskID)
	if wasCanceled {
		if ParseStatus(taskEntity.Status) != StatusCanceled {
			if statusErr := m.updateStatus(&taskEntity, StatusCanceled, map[string]interface{}{
				"next_run_at": nextCronRun(taskEntity.CronSpec),
				"last_error":  "任务已取消",
			}); statusErr != nil {
				m.emitLog(taskID, runIDPtr, "error", fmt.Sprintf("更新 canceled 失败: %v", statusErr), taskEntity.Status)
				return
			}
		}
		finishedAt := time.Now()
		m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
			"status":      "canceled",
			"finished_at": &finishedAt,
			"last_error":  "任务已取消",
		})
		runCompleted = true
		m.emitLog(taskID, runIDPtr, "warn", "任务执行已取消，进程已中断", taskEntity.Status)
		return
	}

	if err == nil && exitCode == 0 {
		verifyStatus := "none"

		// 检查关联策略是否启用校验
		if taskEntity.Policy != nil && taskEntity.Policy.VerifyEnabled {
			m.emitLog(taskID, runIDPtr, "info", "开始备份完整性校验", taskEntity.Status)
			result := verifier.Verify(execCtx, taskEntity, taskEntity.Policy.VerifySampleRate, m.db, func(level, msg string) {
				m.emitLog(taskID, runIDPtr, level, msg, string(StatusRunning))
			}, false)

			// 校验期间可能被取消
			if execCtx.Err() != nil {
				m.emitLog(taskID, runIDPtr, "warn", "校验期间任务已取消", taskEntity.Status)
				m.updateStatus(&taskEntity, StatusCanceled, map[string]interface{}{
					"next_run_at": nextCronRun(taskEntity.CronSpec),
					"last_error":  "任务已取消",
				})
				finishedAt := time.Now()
				m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
					"status":      "canceled",
					"finished_at": &finishedAt,
					"last_error":  "任务已取消",
				})
				runCompleted = true
				return
			}

			verifyStatus = result.Status

			if result.Status == "warning" || result.Status == "failed" {
				m.updateStatus(&taskEntity, StatusWarning, map[string]interface{}{
					"retry_count":   0,
					"next_run_at":   nextCronRun(taskEntity.CronSpec),
					"last_error":    result.Message,
					"verify_status": verifyStatus,
				})
				finishedAt := time.Now()
				duration := finishedAt.Sub(now).Milliseconds()
				m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
					"status":        "warning",
					"finished_at":   &finishedAt,
					"duration_ms":   duration,
					"verify_status": verifyStatus,
					"last_error":    result.Message,
				})
				runCompleted = true
				m.emitLog(taskID, runIDPtr, "warn", "备份校验未通过: "+result.Message, taskEntity.Status)
				alerting.RaiseVerificationFailure(m.db, taskEntity, runIDPtr, result.Message)
				m.triggerDownstreamIfAny(taskEntity, runID, chainRunID)
				return
			}
			m.emitLog(taskID, runIDPtr, "info", "备份完整性校验通过", taskEntity.Status)
		}

		if statusErr := m.updateStatus(&taskEntity, StatusSuccess, map[string]interface{}{
			"retry_count":   0,
			"next_run_at":   nextCronRun(taskEntity.CronSpec),
			"last_error":    "",
			"verify_status": verifyStatus,
		}); statusErr != nil {
			m.emitLog(taskID, runIDPtr, "error", fmt.Sprintf("更新 success 失败: %v", statusErr), taskEntity.Status)
			return
		}
		// 计算本次执行的平均吞吐量
		finishedAt := time.Now()
		duration := finishedAt.Sub(now).Milliseconds()
		var avgThroughput float64
		m.db.Model(&model.TaskTrafficSample{}).
			Where("task_id = ? AND sampled_at BETWEEN ? AND ?", taskID, runStartedAt, finishedAt).
			Select("COALESCE(AVG(throughput_mbps), 0)").Scan(&avgThroughput)

		m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
			"status":          "success",
			"finished_at":     &finishedAt,
			"duration_ms":     duration,
			"verify_status":   verifyStatus,
			"throughput_mbps": avgThroughput,
			"last_error":      "",
		})
		runCompleted = true
		// 更新关联节点的最后备份时间
		if taskEntity.NodeID > 0 {
			backupAt := time.Now()
			m.db.Model(&model.Node{}).Where("id = ?", taskEntity.NodeID).Update("last_backup_at", &backupAt)
		}
		m.emitLog(taskID, runIDPtr, "info", "任务执行成功", taskEntity.Status)
		if resolveErr := alerting.ResolveTaskAlerts(m.db, taskID, "任务恢复成功"); resolveErr != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Err(resolveErr).Msg("ResolveTaskAlerts 失败")
		}
		m.triggerDownstreamIfAny(taskEntity, runID, chainRunID)
		return
	}

	errorMsg := "任务执行失败"
	if err != nil {
		errorMsg = err.Error()
	} else {
		errorMsg = fmt.Sprintf("任务执行失败，退出码=%d", exitCode)
	}

	nextStatus, retryCount, nextRun, shouldRetry := m.stateMachine.NextAfterFailure(StatusRunning, taskEntity.RetryCount, time.Now())

	// 当前 TaskRun 始终标记为 failed（即使 Task 进入 retrying）
	failedAt := time.Now()
	failDuration := failedAt.Sub(now).Milliseconds()
	m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
		"status":      "failed",
		"finished_at": &failedAt,
		"duration_ms": failDuration,
		"last_error":  errorMsg,
	})
	runCompleted = true

	if shouldRetry {
		if statusErr := m.updateStatus(&taskEntity, nextStatus, map[string]interface{}{
			"retry_count": retryCount,
			"next_run_at": &nextRun,
			"last_error":  errorMsg,
		}); statusErr != nil {
			m.emitLog(taskID, runIDPtr, "error", fmt.Sprintf("更新 retrying 失败: %v", statusErr), taskEntity.Status)
			return
		}
		m.emitLog(taskID, runIDPtr, "warn", fmt.Sprintf("任务失败，计划重试 #%d，计划时间: %s", retryCount, nextRun.Format(time.RFC3339)), taskEntity.Status)
		// 保存链路上下文，重试时由 trigger() 恢复
		m.retryChainContexts.Store(taskID, chainContext{chainRunID: chainRunID})
		delay := time.Until(nextRun)
		if delay < 0 {
			delay = 0
		}
		timer := time.AfterFunc(delay, func() {
			m.retryTimers.Delete(taskID)
			if _, err := m.trigger(taskID, "retry"); err != nil {
				logger.Module("task").Warn().Uint("task_id", taskID).Err(err).Msg("重试触发失败")
			}
		})
		m.storeRetryTimer(taskID, timer)
		return
	}

	if statusErr := m.updateStatus(&taskEntity, StatusFailed, map[string]interface{}{
		"retry_count": retryCount,
		"next_run_at": nextCronRun(taskEntity.CronSpec),
		"last_error":  errorMsg,
	}); statusErr != nil {
		m.emitLog(taskID, runIDPtr, "error", fmt.Sprintf("更新 failed 失败: %v", statusErr), taskEntity.Status)
		return
	}
	m.emitLog(taskID, runIDPtr, "error", fmt.Sprintf("任务最终失败: %s", errorMsg), taskEntity.Status)
	if raiseErr := alerting.RaiseTaskFailure(m.db, taskEntity, runIDPtr, errorMsg); raiseErr != nil {
		logger.Module("task").Warn().Uint("task_id", taskEntity.ID).Err(raiseErr).Msg("RaiseTaskFailure 失败")
	}
	m.skipDownstreamIfAny(taskEntity, runID, chainRunID, errorMsg)
}

func (m *Manager) updateStatus(taskEntity *model.Task, to TaskStatus, updates map[string]interface{}) error {
	from := ParseStatus(taskEntity.Status)
	if err := m.stateMachine.ValidateTransition(from, to); err != nil {
		return err
	}

	payload := map[string]interface{}{}
	for key, value := range updates {
		payload[key] = value
	}
	payload["status"] = string(to)

	if err := m.db.Model(taskEntity).Updates(payload).Error; err != nil {
		return err
	}
	taskEntity.Status = string(to)
	if value, ok := payload["retry_count"]; ok {
		if retryValue, castOK := value.(int); castOK {
			taskEntity.RetryCount = retryValue
		}
	}
	return nil
}

// triggerDownstreamIfAny 在上游任务成功（或 warning）后触发所有下游任务。
func (m *Manager) triggerDownstreamIfAny(upstream model.Task, runID uint, chainRunID string) {
	var downstreams []model.Task
	if err := m.db.Where("depends_on_task_id = ?", upstream.ID).Find(&downstreams).Error; err != nil {
		return
	}
	for _, downstream := range downstreams {
		upstreamRunID := runID
		if _, err := m.triggerCore(downstream.ID, "chain", chainRunID, &upstreamRunID); err != nil {
			logger.Module("task").Warn().
				Uint("downstream_task_id", downstream.ID).
				Uint("upstream_task_id", upstream.ID).
				Err(err).Msg("触发下游任务失败")
		}
	}
}

// skipDownstreamIfAny 在上游任务永久失败后，将所有下游任务标记为 skipped 并递归传播。
func (m *Manager) skipDownstreamIfAny(upstream model.Task, runID uint, chainRunID string, reason string) {
	var downstreams []model.Task
	if err := m.db.Where("depends_on_task_id = ?", upstream.ID).Find(&downstreams).Error; err != nil {
		return
	}
	skipMsg := fmt.Sprintf("前置任务 [%s] 永久失败: %s", upstream.Name, reason)
	for _, downstream := range downstreams {
		upstreamRunID := runID
		m.skipTask(downstream, chainRunID, &upstreamRunID, skipMsg)
	}
}

// skipTask 创建 skipped 执行记录，更新任务状态，并递归跳过其下游任务。
func (m *Manager) skipTask(taskEntity model.Task, chainRunID string, upstreamRunID *uint, skipReason string) {
	now := time.Now()
	run := model.TaskRun{
		TaskID:            taskEntity.ID,
		TriggerType:       "chain",
		Status:            "skipped",
		ChainRunID:        chainRunID,
		UpstreamTaskRunID: upstreamRunID,
		SkipReason:        skipReason,
		StartedAt:         &now,
		FinishedAt:        &now,
	}
	if err := m.db.Create(&run).Error; err != nil {
		logger.Module("task").Warn().Uint("task_id", taskEntity.ID).Err(err).Msg("创建 skipped 执行记录失败")
		return
	}
	_ = m.updateStatus(&taskEntity, StatusSkipped, map[string]interface{}{
		"last_error": skipReason,
	})
	runIDPtr := run.ID
	m.emitLog(taskEntity.ID, &runIDPtr, "warn", skipReason, string(StatusSkipped))
	// 递归跳过下游任务
	m.skipDownstreamIfAny(taskEntity, run.ID, chainRunID, skipReason)
}

func (m *Manager) emitLog(taskID uint, runID *uint, level, message, status string) {
	entry := queuedTaskLog{
		taskID:    taskID,
		taskRunID: runID,
		level:     level,
		message:   message,
		status:    status,
	}

	if m.logQueue == nil {
		m.persistLogBatch([]queuedTaskLog{entry})
		return
	}

	select {
	case m.logQueue <- entry:
	default:
		logger.Module("task").Warn().Uint("task_id", taskID).Msg("task log queue full, fallback to direct write")
		m.persistLogBatch([]queuedTaskLog{entry})
	}
}

func (m *Manager) emitTrafficSample(taskID uint, nodeID uint, runStartedAt time.Time, sample executor.ProgressSample) {
	if sample.ThroughputMbps <= 0 {
		return
	}
	sampledAt := sample.ObservedAt.UTC()
	if sampledAt.IsZero() {
		sampledAt = time.Now().UTC()
	}
	bucket := sampledAt.Truncate(defaultSampleThrottleWindow)
	if lastRaw, ok := m.lastSampleBucketByTask.Load(taskID); ok {
		if lastBucket, castOK := lastRaw.(time.Time); castOK && !bucket.After(lastBucket) {
			return
		}
	}
	m.lastSampleBucketByTask.Store(taskID, bucket)

	entry := queuedTaskSample{
		taskID:         taskID,
		nodeID:         nodeID,
		runStartedAt:   runStartedAt,
		sampledAt:      sampledAt,
		throughputMbps: sample.ThroughputMbps,
	}

	if m.sampleQueue == nil {
		m.persistSampleBatch([]queuedTaskSample{entry})
		return
	}

	select {
	case m.sampleQueue <- entry:
	default:
		logger.Module("task").Warn().Uint("task_id", taskID).Msg("task traffic sample queue full, dropping sample")
	}
}

func (m *Manager) persistLogBatch(batch []queuedTaskLog) {
	if len(batch) == 0 || m.db == nil {
		return
	}

	records := make([]model.TaskLog, 0, len(batch))
	for _, item := range batch {
		records = append(records, model.TaskLog{
			TaskID:    item.taskID,
			TaskRunID: item.taskRunID,
			Level:     item.level,
			Message:   item.message,
		})
	}

	if err := m.db.CreateInBatches(&records, m.logBatchSize).Error; err != nil {
		logger.Module("task").Warn().Err(err).Msg("批量写入任务日志失败，回退单条写入")
		for i, item := range batch {
			record := model.TaskLog{
				TaskID:    item.taskID,
				TaskRunID: item.taskRunID,
				Level:     item.level,
				Message:   item.message,
			}
			if oneErr := m.db.Create(&record).Error; oneErr != nil {
				logger.Module("task").Error().Uint("task_id", item.taskID).Int("batch_index", i).Err(oneErr).Msg("写入任务日志失败")
				continue
			}
			m.publishLogEvent(record, item.status)
		}
		return
	}

	for i := range records {
		m.publishLogEvent(records[i], batch[i].status)
	}
}

func (m *Manager) persistSampleBatch(batch []queuedTaskSample) {
	if len(batch) == 0 || m.db == nil {
		return
	}
	m.cleanupExpiredTrafficSamples()

	records := make([]model.TaskTrafficSample, 0, len(batch))
	for _, item := range batch {
		records = append(records, model.TaskTrafficSample{
			TaskID:         item.taskID,
			NodeID:         item.nodeID,
			RunStartedAt:   item.runStartedAt,
			SampledAt:      item.sampledAt,
			ThroughputMbps: item.throughputMbps,
		})
	}

	if err := m.db.CreateInBatches(&records, m.sampleBatchSize).Error; err != nil {
		logger.Module("task").Warn().Err(err).Msg("批量写入吞吐采样失败，回退单条写入")
		for i := range records {
			if oneErr := m.db.Create(&records[i]).Error; oneErr != nil {
				logger.Module("task").Error().Uint("task_id", records[i].TaskID).Int("batch_index", i).Err(oneErr).Msg("写入吞吐采样失败")
			}
		}
	}
}

func (m *Manager) cleanupExpiredTrafficSamples() {
	if m.sampleRetentionDays <= 0 || m.db == nil {
		return
	}

	m.sampleCleanupMu.Lock()
	defer m.sampleCleanupMu.Unlock()

	now := time.Now().UTC()
	if !m.lastSampleCleanupAt.IsZero() && now.Sub(m.lastSampleCleanupAt) < defaultSampleCleanupInterval {
		return
	}

	cutoff := now.AddDate(0, 0, -m.sampleRetentionDays)
	for {
		var ids []uint
		if err := m.db.Model(&model.TaskTrafficSample{}).Where("sampled_at < ?", cutoff).Order("id asc").Limit(defaultSampleCleanupBatchSize).Pluck("id", &ids).Error; err != nil {
			logger.Module("task").Warn().Err(err).Msg("查询过期吞吐采样失败")
			return
		}
		if len(ids) == 0 {
			break
		}
		if err := m.db.Where("id IN ?", ids).Delete(&model.TaskTrafficSample{}).Error; err != nil {
			logger.Module("task").Warn().Err(err).Msg("清理过期吞吐采样失败")
			return
		}
		if len(ids) < defaultSampleCleanupBatchSize {
			break
		}
	}
	m.lastSampleCleanupAt = now
}

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

func (m *Manager) publishLogEvent(record model.TaskLog, status string) {
	if m.hub == nil {
		return
	}
	m.hub.Publish(ws.LogEvent{
		LogID:     record.ID,
		TaskID:    record.TaskID,
		TaskRunID: record.TaskRunID,
		Level:     record.Level,
		Message:   record.Message,
		Status:    status,
		Timestamp: record.CreatedAt,
	})
}

func (m *Manager) taskLock(taskID uint) *sync.Mutex {
	lock, _ := m.locks.LoadOrStore(taskID, &sync.Mutex{})
	mutex, ok := lock.(*sync.Mutex)
	if !ok {
		return &sync.Mutex{}
	}
	return mutex
}

func (m *Manager) strategyLock(nodeID uint, policyID *uint) *sync.Mutex {
	key := buildStrategyKey(nodeID, policyID)
	lock, _ := m.strategyLocks.LoadOrStore(key, &sync.Mutex{})
	mutex, ok := lock.(*sync.Mutex)
	if !ok {
		return &sync.Mutex{}
	}
	return mutex
}

func buildStrategyKey(nodeID uint, policyID *uint) string {
	policyPart := "none"
	if policyID != nil {
		policyPart = strconv.FormatUint(uint64(*policyID), 10)
	}
	return fmt.Sprintf("%d:%s", nodeID, policyPart)
}

// hasNodeConflictForRestore 检查同节点上是否有任何运行中的任务。
func (m *Manager) nodeLock(nodeID uint) *sync.Mutex {
	lock, _ := m.nodeLocks.LoadOrStore(nodeID, &sync.Mutex{})
	mutex, ok := lock.(*sync.Mutex)
	if !ok {
		return &sync.Mutex{}
	}
	return mutex
}

// isNodeRestoring 检查指定节点是否有恢复任务正在运行（内存级持续互斥）。
func (m *Manager) isNodeRestoring(nodeID uint) bool {
	_, ok := m.restoreNodes.Load(nodeID)
	return ok
}

// 恢复操作是破坏性的，需要节点级互斥，比普通备份的策略级互斥更严格。
func (m *Manager) hasNodeConflictForRestore(taskEntity model.Task) (bool, error) {
	var conflictCount int64
	if err := m.db.Model(&model.Task{}).
		Where("id <> ? AND node_id = ? AND status = ?", taskEntity.ID, taskEntity.NodeID, string(StatusRunning)).
		Count(&conflictCount).Error; err != nil {
		return false, err
	}
	return conflictCount > 0, nil
}

func (m *Manager) hasRunningConflict(taskEntity model.Task) (bool, error) {
	query := m.db.Model(&model.Task{}).
		Where("id <> ? AND node_id = ? AND status = ?", taskEntity.ID, taskEntity.NodeID, string(StatusRunning))

	if taskEntity.ExecutorType == "command" {
		// command 任务仅与 rsync 任务互斥（不阻塞其他 command 任务并行执行）
		query = query.Where("executor_type <> ?", "command")
	} else if taskEntity.PolicyID == nil {
		query = query.Where("policy_id IS NULL")
	} else {
		query = query.Where("policy_id = ?", *taskEntity.PolicyID)
	}

	var conflictCount int64
	if err := query.Count(&conflictCount).Error; err != nil {
		return false, err
	}
	return conflictCount > 0, nil
}

// acquireLockWithContext 在获取 Mutex 锁时响应 context 取消。
// 返回 true 表示成功获取锁，false 表示 context 已取消。
func acquireLockWithContext(ctx context.Context, mu *sync.Mutex) bool {
	for {
		if mu.TryLock() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (m *Manager) storeRetryTimer(taskID uint, timer *time.Timer) {
	if timer == nil {
		return
	}
	if oldRaw, ok := m.retryTimers.Load(taskID); ok {
		if oldTimer, castOK := oldRaw.(*time.Timer); castOK {
			oldTimer.Stop()
		}
	}
	m.retryTimers.Store(taskID, timer)
}

func (m *Manager) stopRetryTimer(taskID uint) {
	if timerRaw, ok := m.retryTimers.LoadAndDelete(taskID); ok {
		if timer, castOK := timerRaw.(*time.Timer); castOK {
			timer.Stop()
		}
	}
}

func (m *Manager) stopAllRetryTimers() {
	m.retryTimers.Range(func(key, value interface{}) bool {
		if timer, ok := value.(*time.Timer); ok {
			timer.Stop()
		}
		m.retryTimers.Delete(key)
		return true
	})
}

func (m *Manager) Cancel(taskID uint) error {
	var taskEntity model.Task
	if err := m.db.First(&taskEntity, taskID).Error; err != nil {
		return err
	}

	switch ParseStatus(taskEntity.Status) {
	case StatusPending, StatusRetrying:
		m.stopRetryTimer(taskID)
		if err := m.updateStatus(&taskEntity, StatusCanceled, map[string]interface{}{
			"next_run_at": nextCronRun(taskEntity.CronSpec),
			"last_error":  "任务已取消",
		}); err != nil {
			return err
		}
		m.emitLog(taskID, nil, "warn", "任务已取消", taskEntity.Status)
		return nil
	case StatusRunning:
		m.stopRetryTimer(taskID)
		if cancelRaw, ok := m.runningCancels.Load(taskID); ok {
			if cancelFn, castOK := cancelRaw.(context.CancelFunc); castOK {
				cancelFn()
			}
		}
		if err := m.updateStatus(&taskEntity, StatusCanceled, map[string]interface{}{
			"next_run_at": nextCronRun(taskEntity.CronSpec),
			"last_error":  "任务已取消",
		}); err != nil {
			return err
		}
		m.emitLog(taskID, nil, "warn", "任务已取消，正在终止执行进程", taskEntity.Status)
		return nil
	default:
		// 检查是否有恢复操作正在运行（恢复不改变 Task.Status，但会注册 cancel）
		if cancelRaw, ok := m.runningCancels.Load(taskID); ok {
			if cancelFn, castOK := cancelRaw.(context.CancelFunc); castOK {
				cancelFn()
			}
			m.emitLog(taskID, nil, "warn", "恢复任务已取消", taskEntity.Status)
			return nil
		}
		return fmt.Errorf("仅支持取消待执行、重试中或运行中的任务")
	}
}

func (m *Manager) isCanceled(taskID uint) bool {
	var current struct {
		Status string
	}
	if err := m.db.Model(&model.Task{}).Select("status").Where("id = ?", taskID).Take(&current).Error; err != nil {
		return false
	}
	return ParseStatus(current.Status) == StatusCanceled
}

func (m *Manager) StopAccepting() {
	m.shuttingDown.Store(true)
}

func (m *Manager) Shutdown(ctx context.Context) error {
	m.shuttingDown.Store(true)
	m.stopAllRetryTimers()

	m.runningCancels.Range(func(_, value interface{}) bool {
		if cancelFn, ok := value.(context.CancelFunc); ok {
			cancelFn()
		}
		return true
	})

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

	if m.logWorkerCancel != nil {
		m.logWorkerCancel()
	}
	if m.logWorkerDone != nil {
		select {
		case <-m.logWorkerDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if m.sampleWorkerCancel != nil {
		m.sampleWorkerCancel()
	}
	if m.sampleWorkerDone != nil {
		select {
		case <-m.sampleWorkerDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
