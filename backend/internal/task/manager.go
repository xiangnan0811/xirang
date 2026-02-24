package task

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/task/scheduler"
	"xirang/backend/internal/ws"

	"gorm.io/gorm"
)

const (
	defaultLogQueueCapacity = 1024
	defaultLogBatchSize     = 50
	defaultLogFlushInterval = 500 * time.Millisecond
)

type queuedTaskLog struct {
	taskID  uint
	level   string
	message string
	status  string
}

type Manager struct {
	db              *gorm.DB
	stateMachine    *StateMachine
	executorFactory executor.Factory
	hub             *ws.Hub
	scheduler       *scheduler.CronScheduler
	locks           sync.Map
	strategyLocks   sync.Map
	runningCancels  sync.Map
	pendingRuns     sync.Map
	retryTimers     sync.Map
	semaphore       chan struct{}
	taskWG          sync.WaitGroup

	logQueue         chan queuedTaskLog
	logBatchSize     int
	logFlushInterval time.Duration
	logWorkerCancel  context.CancelFunc
	logWorkerDone    chan struct{}

	shuttingDown atomic.Bool
}

func NewManager(db *gorm.DB, executorFactory executor.Factory, hub *ws.Hub, scheduler *scheduler.CronScheduler) *Manager {
	m := &Manager{
		db:               db,
		stateMachine:     NewStateMachine(),
		executorFactory:  executorFactory,
		hub:              hub,
		scheduler:        scheduler,
		semaphore:        make(chan struct{}, 8),
		logQueue:         make(chan queuedTaskLog, defaultLogQueueCapacity),
		logBatchSize:     defaultLogBatchSize,
		logFlushInterval: defaultLogFlushInterval,
		logWorkerDone:    make(chan struct{}),
	}
	m.startLogWorker()
	return m
}

func (m *Manager) startLogWorker() {
	ctx, cancel := context.WithCancel(context.Background())
	m.logWorkerCancel = cancel
	go m.runLogWorker(ctx)
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
	return m.scheduler.RegisterTask(task.ID, task.CronSpec, func() {
		if err := m.TriggerFromScheduler(task.ID); err != nil {
			log.Printf("warn: 定时触发任务失败(task_id=%d): %v", task.ID, err)
		}
	})
}

func (m *Manager) RemoveSchedule(taskID uint) {
	if m.scheduler == nil {
		return
	}
	m.scheduler.RemoveTask(taskID)
}

func (m *Manager) TriggerManual(taskID uint) error {
	return m.trigger(taskID, "manual")
}

func (m *Manager) TriggerFromScheduler(taskID uint) error {
	return m.trigger(taskID, "cron")
}

func (m *Manager) trigger(taskID uint, reason string) error {
	if m.shuttingDown.Load() {
		if reason == "retry" || reason == "cron" {
			return nil
		}
		return fmt.Errorf("服务正在关闭，任务触发已拒绝")
	}

	if _, loaded := m.pendingRuns.LoadOrStore(taskID, struct{}{}); loaded {
		if reason == "retry" || reason == "cron" {
			return nil
		}
		return fmt.Errorf("任务已在队列或运行中")
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
		return result.Error
	}
	if result.RowsAffected == 0 {
		if reason == "retry" || reason == "cron" {
			return nil
		}
		return fmt.Errorf("任务不存在")
	}

	if reason == "retry" {
		current := ParseStatus(taskEntity.Status)
		if current != StatusRetrying {
			return fmt.Errorf("任务当前状态为 %s，已跳过重试", taskEntity.Status)
		}
	}
	if ParseStatus(taskEntity.Status) == StatusRunning {
		return fmt.Errorf("任务正在运行")
	}

	conflicted, err := m.hasRunningConflict(taskEntity)
	if err != nil {
		return err
	}
	if conflicted {
		return fmt.Errorf("同节点同策略任务正在运行")
	}

	m.stopRetryTimer(taskID)
	scheduled = true
	m.taskWG.Add(1)
	go func() {
		defer m.taskWG.Done()
		m.runTask(taskID, reason)
	}()
	return nil
}

func (m *Manager) runTask(taskID uint, reason string) {
	defer m.pendingRuns.Delete(taskID)

	m.semaphore <- struct{}{}
	defer func() { <-m.semaphore }()

	lock := m.taskLock(taskID)
	lock.Lock()
	defer lock.Unlock()

	var taskEntity model.Task
	if err := m.db.Preload("Node").Preload("Node.SSHKey").Preload("Policy").First(&taskEntity, taskID).Error; err != nil {
		m.emitLog(taskID, "error", fmt.Sprintf("加载任务失败: %v", err), "")
		return
	}

	currentStatus := ParseStatus(taskEntity.Status)
	if currentStatus == StatusRunning {
		m.emitLog(taskID, "warn", "任务已在运行，忽略重复触发", taskEntity.Status)
		return
	}

	strategyLock := m.strategyLock(taskEntity.NodeID, taskEntity.PolicyID)
	strategyLock.Lock()
	defer strategyLock.Unlock()

	conflicted, err := m.hasRunningConflict(taskEntity)
	if err != nil {
		m.emitLog(taskID, "error", fmt.Sprintf("校验互斥冲突失败: %v", err), taskEntity.Status)
		return
	}
	if conflicted {
		m.emitLog(taskID, "warn", "同节点同策略任务已在运行，忽略重复执行", taskEntity.Status)
		return
	}

	if currentStatus == StatusSuccess || currentStatus == StatusFailed || currentStatus == StatusCanceled {
		if err := m.updateStatus(&taskEntity, StatusPending, map[string]interface{}{"last_error": ""}); err != nil {
			m.emitLog(taskID, "error", fmt.Sprintf("切换 pending 失败: %v", err), taskEntity.Status)
			return
		}
	}

	now := time.Now()
	if err := m.updateStatus(&taskEntity, StatusRunning, map[string]interface{}{
		"last_run_at": now,
		"next_run_at": nil,
		"last_error":  "",
	}); err != nil {
		m.emitLog(taskID, "error", fmt.Sprintf("切换 running 失败: %v", err), taskEntity.Status)
		return
	}
	m.emitLog(taskID, "info", fmt.Sprintf("任务开始执行，触发来源: %s", reason), taskEntity.Status)

	execCtx, cancel := context.WithCancel(context.Background())
	m.runningCancels.Store(taskID, cancel)
	defer m.runningCancels.Delete(taskID)

	exec := m.executorFactory.Resolve(taskEntity.ExecutorType)
	exitCode, err := exec.Run(execCtx, taskEntity, func(level, message string) {
		m.emitLog(taskID, level, message, string(StatusRunning))
	})

	wasCanceled := errors.Is(err, context.Canceled) || errors.Is(execCtx.Err(), context.Canceled) || m.isCanceled(taskID)
	if wasCanceled {
		if ParseStatus(taskEntity.Status) != StatusCanceled {
			if statusErr := m.updateStatus(&taskEntity, StatusCanceled, map[string]interface{}{
				"next_run_at": nil,
				"last_error":  "任务已取消",
			}); statusErr != nil {
				m.emitLog(taskID, "error", fmt.Sprintf("更新 canceled 失败: %v", statusErr), taskEntity.Status)
				return
			}
		}
		m.emitLog(taskID, "warn", "任务执行已取消，进程已中断", taskEntity.Status)
		return
	}

	if err == nil && exitCode == 0 {
		if statusErr := m.updateStatus(&taskEntity, StatusSuccess, map[string]interface{}{
			"retry_count": 0,
			"next_run_at": nil,
			"last_error":  "",
		}); statusErr != nil {
			m.emitLog(taskID, "error", fmt.Sprintf("更新 success 失败: %v", statusErr), taskEntity.Status)
			return
		}
		m.emitLog(taskID, "info", "任务执行成功", taskEntity.Status)
		if resolveErr := alerting.ResolveTaskAlerts(m.db, taskID, "任务恢复成功"); resolveErr != nil {
			log.Printf("warn: ResolveTaskAlerts 失败(task_id=%d): %v", taskID, resolveErr)
		}
		return
	}

	errorMsg := "任务执行失败"
	if err != nil {
		errorMsg = err.Error()
	} else {
		errorMsg = fmt.Sprintf("任务执行失败，退出码=%d", exitCode)
	}

	nextStatus, retryCount, nextRun, shouldRetry := m.stateMachine.NextAfterFailure(StatusRunning, taskEntity.RetryCount, time.Now())
	if shouldRetry {
		if statusErr := m.updateStatus(&taskEntity, nextStatus, map[string]interface{}{
			"retry_count": retryCount,
			"next_run_at": &nextRun,
			"last_error":  errorMsg,
		}); statusErr != nil {
			m.emitLog(taskID, "error", fmt.Sprintf("更新 retrying 失败: %v", statusErr), taskEntity.Status)
			return
		}
		m.emitLog(taskID, "warn", fmt.Sprintf("任务失败，计划重试 #%d，计划时间: %s", retryCount, nextRun.Format(time.RFC3339)), taskEntity.Status)
		delay := time.Until(nextRun)
		if delay < 0 {
			delay = 0
		}
		timer := time.AfterFunc(delay, func() {
			m.retryTimers.Delete(taskID)
			if err := m.trigger(taskID, "retry"); err != nil {
				log.Printf("warn: 重试触发失败(task_id=%d): %v", taskID, err)
			}
		})
		m.storeRetryTimer(taskID, timer)
		return
	}

	if statusErr := m.updateStatus(&taskEntity, StatusFailed, map[string]interface{}{
		"retry_count": retryCount,
		"next_run_at": nil,
		"last_error":  errorMsg,
	}); statusErr != nil {
		m.emitLog(taskID, "error", fmt.Sprintf("更新 failed 失败: %v", statusErr), taskEntity.Status)
		return
	}
	m.emitLog(taskID, "error", fmt.Sprintf("任务最终失败: %s", errorMsg), taskEntity.Status)
	if raiseErr := alerting.RaiseTaskFailure(m.db, taskEntity, errorMsg); raiseErr != nil {
		log.Printf("warn: RaiseTaskFailure 失败(task_id=%d): %v", taskEntity.ID, raiseErr)
	}
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

func (m *Manager) emitLog(taskID uint, level, message, status string) {
	entry := queuedTaskLog{
		taskID:  taskID,
		level:   level,
		message: message,
		status:  status,
	}

	if m.logQueue == nil {
		m.persistLogBatch([]queuedTaskLog{entry})
		return
	}

	select {
	case m.logQueue <- entry:
	default:
		log.Printf("warn: task log queue full, fallback to direct write(task_id=%d)", taskID)
		m.persistLogBatch([]queuedTaskLog{entry})
	}
}

func (m *Manager) persistLogBatch(batch []queuedTaskLog) {
	if len(batch) == 0 || m.db == nil {
		return
	}

	records := make([]model.TaskLog, 0, len(batch))
	for _, item := range batch {
		records = append(records, model.TaskLog{
			TaskID:  item.taskID,
			Level:   item.level,
			Message: item.message,
		})
	}

	if err := m.db.CreateInBatches(&records, m.logBatchSize).Error; err != nil {
		log.Printf("warn: 批量写入任务日志失败，回退单条写入: %v", err)
		for i, item := range batch {
			record := model.TaskLog{
				TaskID:  item.taskID,
				Level:   item.level,
				Message: item.message,
			}
			if oneErr := m.db.Create(&record).Error; oneErr != nil {
				log.Printf("error: 写入任务日志失败(task_id=%d, batch_index=%d): %v", item.taskID, i, oneErr)
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

func (m *Manager) publishLogEvent(record model.TaskLog, status string) {
	if m.hub == nil {
		return
	}
	m.hub.Publish(ws.LogEvent{
		LogID:     record.ID,
		TaskID:    record.TaskID,
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

func (m *Manager) hasRunningConflict(taskEntity model.Task) (bool, error) {
	query := m.db.Model(&model.Task{}).
		Where("id <> ? AND node_id = ? AND status = ?", taskEntity.ID, taskEntity.NodeID, string(StatusRunning))

	if taskEntity.PolicyID == nil {
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
			"next_run_at": nil,
			"last_error":  "任务已取消",
		}); err != nil {
			return err
		}
		m.emitLog(taskID, "warn", "任务已取消", taskEntity.Status)
		return nil
	case StatusRunning:
		m.stopRetryTimer(taskID)
		if cancelRaw, ok := m.runningCancels.Load(taskID); ok {
			if cancelFn, castOK := cancelRaw.(context.CancelFunc); castOK {
				cancelFn()
			}
		}
		if err := m.updateStatus(&taskEntity, StatusCanceled, map[string]interface{}{
			"next_run_at": nil,
			"last_error":  "任务已取消",
		}); err != nil {
			return err
		}
		m.emitLog(taskID, "warn", "任务已取消，正在终止执行进程", taskEntity.Status)
		return nil
	default:
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
	return nil
}
