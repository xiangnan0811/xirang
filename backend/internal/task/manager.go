package task

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/task/scheduler"
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
	hookRunFunc     func(ctx context.Context, task model.Task, command string) error // 可测试注入
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

	retentionCancel  context.CancelFunc
	retentionDone    chan struct{}

	settingsSvc *settings.Service

	shuttingDown atomic.Bool
}

func NewManager(db *gorm.DB, executorFactory executor.Factory, hub *ws.Hub, scheduler *scheduler.CronScheduler, settingsSvc *settings.Service, sampleRetentionDays int, taskRunRetentionDays int) *Manager {
	m := &Manager{
		db:                   db,
		stateMachine:         NewStateMachine(),
		executorFactory:      executorFactory,
		hub:                  hub,
		scheduler:            scheduler,
		semaphore:            make(chan struct{}, 8),
		hookRunFunc:          nil, // 初始化后设置为默认 runSSHHook
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
		retentionDone:       make(chan struct{}),
		settingsSvc:          settingsSvc,
	}
	m.hookRunFunc = m.runSSHHook
	m.startLogWorker()
	m.startSampleWorker()
	m.startRetentionWorker()
	return m
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

func (m *Manager) Cancel(taskID uint) error {
	var taskEntity model.Task
	if err := m.db.First(&taskEntity, taskID).Error; err != nil {
		return err
	}

	switch ParseStatus(taskEntity.Status) {
	case StatusPending, StatusRetrying:
		m.stopRetryTimer(taskID)
		m.retryChainContexts.Delete(taskID) // 清理重试链路上下文，防止泄漏
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
	if m.retentionCancel != nil {
		m.retentionCancel()
	}
	if m.retentionDone != nil {
		select {
		case <-m.retentionDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
