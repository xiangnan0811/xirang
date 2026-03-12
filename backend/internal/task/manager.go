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

type queuedTaskLog struct {
	taskID  uint
	level   string
	message string
	status  string
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

	sampleQueue            chan queuedTaskSample
	sampleBatchSize        int
	sampleFlushInterval    time.Duration
	sampleWorkerCancel     context.CancelFunc
	sampleWorkerDone       chan struct{}
	lastSampleBucketByTask sync.Map
	sampleRetentionDays    int
	lastSampleCleanupAt    time.Time
	sampleCleanupMu        sync.Mutex

	shuttingDown atomic.Bool
}

func NewManager(db *gorm.DB, executorFactory executor.Factory, hub *ws.Hub, scheduler *scheduler.CronScheduler, sampleRetentionDays int) *Manager {
	m := &Manager{
		db:                  db,
		stateMachine:        NewStateMachine(),
		executorFactory:     executorFactory,
		hub:                 hub,
		scheduler:           scheduler,
		semaphore:           make(chan struct{}, 8),
		logQueue:            make(chan queuedTaskLog, defaultLogQueueCapacity),
		logBatchSize:        defaultLogBatchSize,
		logFlushInterval:    defaultLogFlushInterval,
		logWorkerDone:       make(chan struct{}),
		sampleQueue:         make(chan queuedTaskSample, defaultSampleQueueCapacity),
		sampleBatchSize:     defaultSampleBatchSize,
		sampleFlushInterval: defaultSampleFlushInterval,
		sampleWorkerDone:    make(chan struct{}),
		sampleRetentionDays: sampleRetentionDays,
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
		return fmt.Errorf("系统维护中，请稍候再试")
	}

	if _, loaded := m.pendingRuns.LoadOrStore(taskID, struct{}{}); loaded {
		if reason == "retry" || reason == "cron" {
			return nil
		}
		return fmt.Errorf("该任务正在执行中，请勿重复触发")
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
			return fmt.Errorf("当前任务状态不支持重试，请稍候再试")
		}
	}
	if ParseStatus(taskEntity.Status) == StatusRunning {
		return fmt.Errorf("该任务正在执行中，请勿重复触发")
	}

	conflicted, err := m.hasRunningConflict(taskEntity)
	if err != nil {
		return err
	}
	if conflicted {
		return fmt.Errorf("同节点有任务正在运行，请稍候再试")
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
		m.emitLog(taskID, "warn", "同节点有任务正在运行，忽略重复执行", taskEntity.Status)
		return
	}

	if currentStatus == StatusSuccess || currentStatus == StatusFailed || currentStatus == StatusCanceled || currentStatus == StatusWarning {
		if err := m.updateStatus(&taskEntity, StatusPending, map[string]interface{}{"last_error": ""}); err != nil {
			m.emitLog(taskID, "error", fmt.Sprintf("切换 pending 失败: %v", err), taskEntity.Status)
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
		m.emitLog(taskID, "error", fmt.Sprintf("切换 running 失败: %v", err), taskEntity.Status)
		return
	}
	m.emitLog(taskID, "info", fmt.Sprintf("任务开始执行，触发来源: %s", reason), taskEntity.Status)

	execCtx, cancel := context.WithCancel(context.Background())
	m.runningCancels.Store(taskID, cancel)
	defer m.runningCancels.Delete(taskID)

	exec := m.executorFactory.Resolve(taskEntity.ExecutorType)
	runStartedAt := now.UTC()
	exitCode, err := exec.Run(execCtx, taskEntity, func(level, message string) {
		m.emitLog(taskID, level, message, string(StatusRunning))
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
				m.emitLog(taskID, "error", fmt.Sprintf("更新 canceled 失败: %v", statusErr), taskEntity.Status)
				return
			}
		}
		m.emitLog(taskID, "warn", "任务执行已取消，进程已中断", taskEntity.Status)
		return
	}

	if err == nil && exitCode == 0 {
		verifyStatus := "none"

		// 检查关联策略是否启用校验
		if taskEntity.Policy != nil && taskEntity.Policy.VerifyEnabled {
			m.emitLog(taskID, "info", "开始备份完整性校验", taskEntity.Status)
			result := verifier.Verify(execCtx, taskEntity, taskEntity.Policy.VerifySampleRate, m.db, func(level, msg string) {
				m.emitLog(taskID, level, msg, string(StatusRunning))
			})

			// 校验期间可能被取消
			if execCtx.Err() != nil {
				m.emitLog(taskID, "warn", "校验期间任务已取消", taskEntity.Status)
				m.updateStatus(&taskEntity, StatusCanceled, map[string]interface{}{
					"next_run_at": nextCronRun(taskEntity.CronSpec),
					"last_error":  "任务已取消",
				})
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
				m.emitLog(taskID, "warn", "备份校验未通过: "+result.Message, taskEntity.Status)
				alerting.RaiseVerificationFailure(m.db, taskEntity, result.Message)
				return
			}
			m.emitLog(taskID, "info", "备份完整性校验通过", taskEntity.Status)
		}

		if statusErr := m.updateStatus(&taskEntity, StatusSuccess, map[string]interface{}{
			"retry_count":   0,
			"next_run_at":   nextCronRun(taskEntity.CronSpec),
			"last_error":    "",
			"verify_status": verifyStatus,
		}); statusErr != nil {
			m.emitLog(taskID, "error", fmt.Sprintf("更新 success 失败: %v", statusErr), taskEntity.Status)
			return
		}
		// 更新关联节点的最后备份时间
		if taskEntity.NodeID > 0 {
			now := time.Now()
			m.db.Model(&model.Node{}).Where("id = ?", taskEntity.NodeID).Update("last_backup_at", &now)
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
		"next_run_at": nextCronRun(taskEntity.CronSpec),
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
		log.Printf("warn: task traffic sample queue full, dropping sample(task_id=%d)", taskID)
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
		log.Printf("warn: 批量写入吞吐采样失败，回退单条写入: %v", err)
		for i := range records {
			if oneErr := m.db.Create(&records[i]).Error; oneErr != nil {
				log.Printf("error: 写入吞吐采样失败(task_id=%d, batch_index=%d): %v", records[i].TaskID, i, oneErr)
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
			log.Printf("warn: 查询过期吞吐采样失败: %v", err)
			return
		}
		if len(ids) == 0 {
			break
		}
		if err := m.db.Where("id IN ?", ids).Delete(&model.TaskTrafficSample{}).Error; err != nil {
			log.Printf("warn: 清理过期吞吐采样失败: %v", err)
			return
		}
		if len(ids) < defaultSampleCleanupBatchSize {
			break
		}
	}
	m.lastSampleCleanupAt = now
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
			"next_run_at": nextCronRun(taskEntity.CronSpec),
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
			"next_run_at": nextCronRun(taskEntity.CronSpec),
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
