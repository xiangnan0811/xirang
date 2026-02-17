package task

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/task/scheduler"
	"xirang/backend/internal/ws"

	"gorm.io/gorm"
)

type Manager struct {
	db              *gorm.DB
	stateMachine    *StateMachine
	executorFactory executor.Factory
	hub             *ws.Hub
	scheduler       *scheduler.CronScheduler
	locks           sync.Map
	strategyLocks   sync.Map
	runningCancels  sync.Map
	semaphore       chan struct{}
}

func NewManager(db *gorm.DB, executorFactory executor.Factory, hub *ws.Hub, scheduler *scheduler.CronScheduler) *Manager {
	return &Manager{
		db:              db,
		stateMachine:    NewStateMachine(),
		executorFactory: executorFactory,
		hub:             hub,
		scheduler:       scheduler,
		semaphore:       make(chan struct{}, 8),
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
		_ = m.TriggerFromScheduler(task.ID)
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

	go m.runTask(taskID, reason)
	return nil
}

func (m *Manager) runTask(taskID uint, reason string) {
	m.semaphore <- struct{}{}
	defer func() { <-m.semaphore }()

	lock := m.taskLock(taskID)
	lock.Lock()
	defer lock.Unlock()
	defer m.locks.Delete(taskID)

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
	defer m.strategyLocks.Delete(buildStrategyKey(taskEntity.NodeID, taskEntity.PolicyID))

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
		_ = alerting.ResolveTaskAlerts(m.db, taskID, "任务恢复成功")
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
		time.AfterFunc(delay, func() {
			_ = m.trigger(taskID, "retry")
		})
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
	_ = alerting.RaiseTaskFailure(m.db, taskEntity, errorMsg)
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
	logRecord := model.TaskLog{
		TaskID:  taskID,
		Level:   level,
		Message: message,
	}
	_ = m.db.Create(&logRecord).Error
	if m.hub == nil {
		return
	}
	m.hub.Publish(ws.LogEvent{
		LogID:     logRecord.ID,
		TaskID:    taskID,
		Level:     level,
		Message:   message,
		Status:    status,
		Timestamp: logRecord.CreatedAt,
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

func (m *Manager) Cancel(taskID uint) error {
	var taskEntity model.Task
	if err := m.db.First(&taskEntity, taskID).Error; err != nil {
		return err
	}

	switch ParseStatus(taskEntity.Status) {
	case StatusPending, StatusRetrying:
		if err := m.updateStatus(&taskEntity, StatusCanceled, map[string]interface{}{
			"next_run_at": nil,
			"last_error":  "任务已取消",
		}); err != nil {
			return err
		}
		m.emitLog(taskID, "warn", "任务已取消", taskEntity.Status)
		return nil
	case StatusRunning:
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
