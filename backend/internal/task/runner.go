package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/anomaly"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/config"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/profile"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/task/verifier"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// defaultGlobalTaskTimeout 是任务执行超时的兜底值（24h），仅在 Policy.MaxExecutionSeconds=0
// 且环境变量 TASK_MAX_EXECUTION_SECONDS 未设置时生效。可通过 globalTaskTimeoutOverride
// 在测试中覆盖。
const defaultGlobalTaskTimeout = 24 * time.Hour

var globalTaskTimeoutOverride time.Duration // 仅供测试，0 = 不覆盖

// computeExecTimeout 计算单次任务执行的最大允许时长：
//   - 优先使用 Policy.MaxExecutionSeconds（>0 时）
//   - 否则读环境变量 TASK_MAX_EXECUTION_SECONDS（秒）
//   - 否则使用全局兜底 defaultGlobalTaskTimeout (24h)
//
// 返回值至少为 1 秒，避免 0 触发立即超时。
func computeExecTimeout(taskEntity model.Task) time.Duration {
	if taskEntity.Policy != nil && taskEntity.Policy.MaxExecutionSeconds > 0 {
		return time.Duration(taskEntity.Policy.MaxExecutionSeconds) * time.Second
	}
	if globalTaskTimeoutOverride > 0 {
		return globalTaskTimeoutOverride
	}
	if env := strings.TrimSpace(os.Getenv("TASK_MAX_EXECUTION_SECONDS")); env != "" {
		if v, err := strconv.Atoi(env); err == nil && v > 0 {
			return time.Duration(v) * time.Second
		}
	}
	return defaultGlobalTaskTimeout
}

var (
	tasksActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "xirang_tasks_active",
		Help: "Number of currently running tasks",
	})
	backupLastSuccess = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "xirang_backup_last_success_timestamp",
		Help: "Unix timestamp of last successful backup per task",
	}, []string{"task_name"})
)

func (m *Manager) triggerCore(taskID uint, reason string, chainRunID string, upstreamRunID *uint) (uint, error) {
	if m.shuttingDown.Load() {
		if reason == "retry" || reason == "cron" || reason == "chain" {
			return 0, nil
		}
		return 0, fmt.Errorf("系统维护中，请稍候再试")
	}

	launchCtx, ownership, claimed := m.claimPendingRunOwnership(taskID)
	if !claimed {
		if reason == "retry" || reason == "cron" || reason == "chain" {
			return 0, nil
		}
		return 0, fmt.Errorf("该任务正在执行中，请勿重复触发")
	}
	scheduled := false
	registeredCancel := ownership.cancel
	defer func() {
		if !scheduled {
			if registeredCancel != nil {
				registeredCancel()
				m.chainRunner.Delete(taskID)
			}
			m.pendingRuns.CompareAndDelete(taskID, ownership)
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

	// 暂停检查
	if !taskEntity.Enabled {
		if reason == "cron" {
			return 0, nil
		}
		if reason == "chain" {
			if err := m.skipTask(taskEntity, chainRunID, upstreamRunID, "任务已暂停，链式执行跳过"); err != nil {
				return 0, err
			}
			return 0, nil
		}
		return 0, fmt.Errorf("任务已暂停，请先恢复后再触发")
	}

	// 跳过下次检查（仅 cron 触发）
	if reason == "cron" && taskEntity.SkipNext {
		m.db.Model(&taskEntity).Updates(map[string]interface{}{
			"skip_next":   false,
			"next_run_at": nextCronRun(taskEntity.CronSpec),
		})
	}

	if ParseStatus(taskEntity.Status) == StatusRunning {
		return 0, fmt.Errorf("该任务正在执行中，请勿重复触发")
	}
	if reason == "cron" && ParseStatus(taskEntity.Status) == StatusRetrying {
		// Durable retry delivery owns next_run_at. A cron tick must not
		// create a competing attempt while that reservation is pending.
		return 0, nil
	}
	// 手动触发时，阻止有前置依赖的任务被直接执行，需从头节点触发
	if reason == "manual" && taskEntity.DependsOnTaskID != nil && *taskEntity.DependsOnTaskID > 0 {
		return 0, fmt.Errorf("该任务有前置依赖（任务 ID: %d），请从链头节点触发", *taskEntity.DependsOnTaskID)
	}
	// Cancellation ownership precedes durable reservation. A concurrent Cancel
	// can therefore abort the caller-owned reservation transaction instead of
	// leaving a pending TaskRun whose goroutine has not registered yet.
	runCtx, runCancel := m.newRunContext(launchCtx, computeExecTimeout(taskEntity))
	ownership.addCancel(runCancel)
	if err := launchCtx.Err(); err != nil {
		return 0, err
	}
	if err := runCtx.Err(); err != nil {
		return 0, err
	}

	// Keep the process-local restore marker check atomic with the durable
	// admission plus TaskRun reservation. Cross-process exclusion is provided by
	// the coordinator's shared node-row boundary inside reserveTaskRun.
	nLock := m.nodeLock(taskEntity.NodeID)
	nLock.Lock()
	conflicted, err := m.hasRunningConflict(taskEntity)
	if err != nil {
		nLock.Unlock()
		return 0, err
	}
	if conflicted {
		nLock.Unlock()
		return 0, fmt.Errorf("同节点有任务正在运行，请稍候再试")
	}
	// 检查同节点是否有恢复任务正在运行（恢复是破坏性操作，需要节点级互斥）
	if m.isNodeRestoring(taskEntity.NodeID) {
		nLock.Unlock()
		return 0, fmt.Errorf("同节点有恢复任务正在运行，请稍候再试")
	}

	requestedRun := model.TaskRun{
		TaskID:            taskID,
		TriggerType:       reason,
		Status:            "pending",
		ChainRunID:        chainRunID,
		UpstreamTaskRunID: upstreamRunID,
	}
	run, err := m.reserveTaskRun(runCtx, taskEntity.NodeID, requestedRun)
	nLock.Unlock()
	if err != nil {
		if errors.Is(err, ErrNodeWriteConflict) {
			return 0, fmt.Errorf("同节点有恢复任务正在运行，请稍候再试: %w", err)
		}
		if errors.Is(err, ErrNodeWriteUnavailable) {
			return 0, fmt.Errorf("节点写入协调暂不可用，请稍候再试: %w", err)
		}
		return 0, fmt.Errorf("创建执行记录失败: %w", err)
	}

	scheduled = true
	m.taskWG.Add(1)
	go func() {
		defer m.taskWG.Done()
		m.runTaskWithContext(taskID, run.ID, reason, chainRunID, runCtx, ownership, ownership.cancel)
	}()
	return run.ID, nil
}

func (m *Manager) runTask(taskID uint, runID uint, reason string, chainRunID string) {
	runCtx, runCancel := context.WithCancel(context.Background())
	m.chainRunner.Store(taskID, runCancel)
	m.runTaskWithContext(taskID, runID, reason, chainRunID, runCtx, nil, runCancel)
}

func (m *Manager) runTaskWithContext(
	taskID uint,
	runID uint,
	reason string,
	chainRunID string,
	runCtx context.Context,
	ownership *pendingRunOwnership,
	runCancel context.CancelFunc,
) {
	if ownership != nil {
		defer m.pendingRuns.CompareAndDelete(taskID, ownership)
	}
	defer m.locks.Delete(taskID) // 清理任务锁，防止 sync.Map 无限增长
	defer m.chainRunner.Delete(taskID)
	defer runCancel()

	ownerClaimed, ownerErr := m.claimTaskRunOwner(context.Background(), taskID, runID)
	if ownerErr != nil {
		logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(ownerErr).Msg("claim task run execution lease")
		return
	}
	if !ownerClaimed {
		// A malformed/foreign direct-run invocation may not satisfy the
		// task_id predicate used by the owner claim. Close only the durable
		// row if it is still unowned or its lease has expired; a live owner
		// remains fenced and will recover or finish it itself.
		if err := m.failTaskRunBeforeExecutor(context.Background(), runID, "执行记录与任务不匹配"); err != nil && !errors.Is(err, errTaskRunNotOwner) {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("reject unowned TaskRun before executor")
		}
		logger.Module("task").Info().Uint("task_id", taskID).Uint("task_run_id", runID).Msg("task run execution lease owned by another process")
		return
	}
	heartbeatCancel := m.startTaskRunHeartbeat(runCtx, runID, runCancel)
	defer heartbeatCancel()

	runCompleted := false
	defer func() {
		if ownerClaimed && !runCompleted {
			if err := m.recoverTaskRunOnReturn(runCtx, taskID, runID, "任务启动前异常退出"); err != nil {
				logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("标记启动前异常 TaskRun 失败")
			}
		}
	}()

	select {
	case m.semaphore <- struct{}{}:
	case <-runCtx.Done():
		if err := m.cancelTaskRunBeforeExecutor(runID, "任务已取消"); err != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("取消排队 TaskRun 失败")
		}
		runCompleted = true
		return
	}
	defer func() { <-m.semaphore }()

	lock := m.taskLock(taskID)
	if !acquireLockWithContext(runCtx, lock) {
		if err := m.cancelTaskRunBeforeExecutor(runID, "任务已取消"); err != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("取消等待任务锁的 TaskRun 失败")
		}
		runCompleted = true
		return
	}
	defer lock.Unlock()

	runIDPtr := &runID

	var taskEntity model.Task
	if err := m.db.Preload("Node").Preload("Node.SSHKey").Preload("Policy").First(&taskEntity, taskID).Error; err != nil {
		m.logDispatcher.Dispatch(taskID, runIDPtr, "error", fmt.Sprintf("加载任务失败: %v", err), "")
		return
	}

	var currentRun struct {
		TaskID         uint
		Status         string
		NodeIDSnapshot uint
	}
	if err := m.db.Model(&model.TaskRun{}).Select("task_id", "status", "node_id_snapshot").Where("id = ?", runID).Take(&currentRun).Error; err != nil {
		m.logDispatcher.Dispatch(taskID, runIDPtr, "error", fmt.Sprintf("加载执行记录失败: %v", err), taskEntity.Status)
		return
	}
	if currentRun.TaskID != taskID || !model.IsTaskRunNodeSnapshotAuthoritative(currentRun.NodeIDSnapshot) || currentRun.NodeIDSnapshot != taskEntity.NodeID {
		message := "执行记录节点快照不具备执行权限"
		m.logDispatcher.Dispatch(taskID, runIDPtr, "error", message+"，跳过执行", taskEntity.Status)
		if err := m.failTaskRunBeforeExecutor(context.Background(), runID, message); err != nil {
			logger.Module("task").Warn().Uint("task_run_id", runID).Err(err).Msg("reject malformed TaskRun before executor")
		} else {
			runCompleted = true
		}
		return
	}
	if currentRun.Status == model.TaskRunStatusCanceled || runCtx.Err() != nil {
		if err := m.cancelTaskRunBeforeExecutor(runID, "任务已取消"); err != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("保留启动前取消状态失败")
		}
		runCompleted = true
		m.logDispatcher.Dispatch(taskID, runIDPtr, "warn", "任务已在启动前取消，跳过执行", taskEntity.Status)
		return
	}

	// 检查关联节点是否存在（Preload 时 Node 可能为 nil，如节点已被删除）
	if taskEntity.Node.ID == 0 {
		message := "关联节点不存在"
		m.logDispatcher.Dispatch(taskID, runIDPtr, "error", message+"，跳过执行", taskEntity.Status)
		finishedAt := time.Now().UTC()
		if err := m.terminalizeTaskRun(runCtx, taskID, runID, []string{model.TaskRunStatusPending}, nil, nil,
			StatusFailed, map[string]interface{}{"finished_at": &finishedAt, "last_error": message}); err != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("关联节点不存在终态保存失败")
			return
		}
		runCompleted = true
		return
	}
	// 检查节点是否已归档
	if taskEntity.Node.Archived {
		message := "节点已归档"
		m.logDispatcher.Dispatch(taskID, runIDPtr, "warn", message+"，跳过执行", taskEntity.Status)
		finishedAt := time.Now().UTC()
		if err := m.terminalizeTaskRun(runCtx, taskID, runID, []string{model.TaskRunStatusPending}, nil, nil,
			StatusCanceled, map[string]interface{}{"finished_at": &finishedAt, "last_error": message}); err != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("节点归档终态保存失败")
			return
		}
		runCompleted = true
		return
	}

	// 检查节点是否处于维护窗口
	checkTime := time.Now()
	if taskEntity.Node.MaintenanceStart != nil && taskEntity.Node.MaintenanceEnd != nil &&
		checkTime.After(*taskEntity.Node.MaintenanceStart) && checkTime.Before(*taskEntity.Node.MaintenanceEnd) {
		message := "节点处于维护窗口"
		finishedAt := time.Now().UTC()
		if err := m.terminalizeTaskRun(runCtx, taskID, runID, []string{model.TaskRunStatusPending}, nil, nil,
			StatusCanceled, map[string]interface{}{"finished_at": &finishedAt, "last_error": message}); err != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("节点维护窗口终态保存失败")
			return
		}
		runCompleted = true
		m.logDispatcher.Dispatch(taskID, runIDPtr, "warn", message+"，跳过执行", "")
		return
	}

	currentStatus := ParseStatus(taskEntity.Status)
	if currentStatus == StatusRunning {
		m.logDispatcher.Dispatch(taskID, runIDPtr, "warn", "任务已在运行，忽略重复触发", taskEntity.Status)
		return
	}

	strategyLock := m.strategyLock(taskEntity.NodeID, taskEntity.PolicyID)
	if !acquireLockWithContext(runCtx, strategyLock) {
		if err := m.cancelTaskRunBeforeExecutor(runID, "任务已取消"); err != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("取消等待策略锁的 TaskRun 失败")
		}
		runCompleted = true
		return
	}
	defer strategyLock.Unlock()

	// 使用 nodeLock 保证 isNodeRestoring 检查与 updateStatus(running) 的原子性，
	// 与 TriggerRestore() 中的 hasNodeConflictForRestore+restoreNodes.Store 互斥。
	nLock := m.nodeLock(taskEntity.NodeID)
	if !acquireLockWithContext(runCtx, nLock) {
		if err := m.cancelTaskRunBeforeExecutor(runID, "任务已取消"); err != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("取消等待节点锁的 TaskRun 失败")
		}
		runCompleted = true
		return
	}

	conflicted, err := m.hasRunningConflict(taskEntity)
	if err != nil {
		nLock.Unlock()
		m.logDispatcher.Dispatch(taskID, runIDPtr, "error", fmt.Sprintf("校验互斥冲突失败: %v", err), taskEntity.Status)
		return
	}
	if conflicted {
		nLock.Unlock()
		m.logDispatcher.Dispatch(taskID, runIDPtr, "warn", "同节点有任务正在运行，忽略重复执行", taskEntity.Status)
		return
	}
	if m.isNodeRestoring(taskEntity.NodeID) {
		nLock.Unlock()
		m.logDispatcher.Dispatch(taskID, runIDPtr, "warn", "同节点有恢复任务正在运行，忽略执行", taskEntity.Status)
		return
	}

	now := time.Now().UTC()
	m.sampleWriter.ResetThrottle(taskID)
	execTimeout := computeExecTimeout(taskEntity)
	execCtx, timeoutCancel := context.WithTimeout(runCtx, execTimeout)
	defer timeoutCancel()
	previousTaskOutcome := taskEntity
	execCtx = m.withTaskCredentialAuditContext(execCtx, taskEntity, runID, reason, map[string]any{
		"operation":            "task_run",
		"chain_run_id_present": strings.TrimSpace(chainRunID) != "",
	})
	if err := m.enterTaskExecutionForReason(execCtx, runID, taskEntity.NodeID, now, &taskEntity, reason); err != nil {
		nLock.Unlock()
		if errors.Is(err, errCronTaskBlocked) {
			runCompleted = true
			m.logDispatcher.Dispatch(taskID, runIDPtr, "info", "定时任务在执行入口被暂停或跳过", taskEntity.Status)
			return
		}
		if execCtx.Err() != nil || errors.Is(err, context.Canceled) {
			if cancelErr := m.cancelTaskRunBeforeExecutor(runID, "任务已取消"); cancelErr != nil {
				logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(cancelErr).Msg("保留执行入口取消状态失败")
			}
			runCompleted = true
			return
		}
		m.logDispatcher.Dispatch(taskID, runIDPtr, "error", fmt.Sprintf("TaskRun 执行入口拒绝: %v", err), taskEntity.Status)
		if rejectErr := m.failTaskRunBeforeExecutor(context.Background(), runID, "任务执行入口拒绝"); rejectErr != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(rejectErr).Msg("保存执行入口拒绝终态失败")
		} else {
			runCompleted = true
		}
		return
	}
	if execCtx.Err() != nil || runCtx.Err() != nil {
		if err := m.cancelTaskExecutionBeforeExecutor(
			runID,
			taskID,
			taskEntity.NodeID,
			&previousTaskOutcome,
			"任务已取消",
		); err != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("补偿未进入 executor 的 Task 执行失败")
		}
		nLock.Unlock()
		runCompleted = true
		return
	}
	nLock.Unlock()
	tasksActive.Inc()
	defer tasksActive.Dec()

	m.logDispatcher.Dispatch(taskID, runIDPtr, "info", fmt.Sprintf("任务开始执行，触发来源: %s", reason), taskEntity.Status)

	if taskEntity.Policy != nil {
		if preHook, err := secure.DecryptIfNeeded(taskEntity.Policy.PreHook); err == nil {
			taskEntity.Policy.PreHook = preHook
		}
		if postHook, err := secure.DecryptIfNeeded(taskEntity.Policy.PostHook); err == nil {
			taskEntity.Policy.PostHook = postHook
		}
	}

	if err := m.renderPolicyProfileHooks(&taskEntity); err != nil {
		errorMsg := sanitizeTaskLastError(fmt.Sprintf("渲染应用感知 hook 失败: %v", err))
		m.logDispatcher.Dispatch(taskID, runIDPtr, "error", errorMsg, taskEntity.Status)
		failedAt := time.Now().UTC()
		failedStatus := StatusFailed
		if terminalErr := m.terminalizeTaskRun(runCtx, taskID, runID, []string{model.TaskRunStatusRunning}, &failedStatus,
			map[string]interface{}{"next_run_at": nextCronRun(taskEntity.CronSpec), "last_error": errorMsg},
			StatusFailed, map[string]interface{}{"finished_at": &failedAt, "last_error": errorMsg}); terminalErr != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(terminalErr).Msg("app-profile hook render terminal persistence failed")
			return
		}
		taskEntity.Status = string(StatusFailed)
		taskEntity.LastError = errorMsg
		runCompleted = true
		return
	}

	// Pre-hook 执行
	if taskEntity.Policy != nil && taskEntity.Policy.PreHook != "" {
		if execCtx.Err() != nil || runCtx.Err() != nil {
			if err := m.cancelTaskExecutionBeforeExecutor(
				runID,
				taskID,
				taskEntity.NodeID,
				&previousTaskOutcome,
				"任务已取消",
			); err != nil {
				logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("补偿 pre-hook 前取消失败")
			}
			runCompleted = true
			return
		}
		hookTimeout := time.Duration(taskEntity.Policy.HookTimeoutSeconds) * time.Second
		if hookTimeout <= 0 {
			hookTimeout = 5 * time.Minute
		}
		hookCtx, hookCancel := context.WithTimeout(execCtx, hookTimeout)
		m.logDispatcher.Dispatch(taskID, runIDPtr, "info", "执行 pre-hook", taskEntity.Status)
		hookErr := m.hookRunFunc(hookCtx, taskEntity, taskEntity.Policy.PreHook)
		hookCancel()
		if hookErr != nil {
			errorMsg := sanitizeTaskLastError(fmt.Sprintf("pre-hook 执行失败: %v", hookErr))
			m.logDispatcher.Dispatch(taskID, runIDPtr, "error", errorMsg, taskEntity.Status)
			failedAt := time.Now().UTC()
			failedStatus := StatusFailed
			if terminalErr := m.terminalizeTaskRun(runCtx, taskID, runID, []string{model.TaskRunStatusRunning}, &failedStatus,
				map[string]interface{}{"next_run_at": nextCronRun(taskEntity.CronSpec), "last_error": errorMsg},
				StatusFailed, map[string]interface{}{"finished_at": &failedAt, "last_error": errorMsg}); terminalErr != nil {
				logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(terminalErr).Msg("pre-hook terminal persistence failed")
				return
			}
			taskEntity.Status = string(StatusFailed)
			taskEntity.LastError = errorMsg
			runCompleted = true
			return
		}
		m.logDispatcher.Dispatch(taskID, runIDPtr, "info", "pre-hook 执行成功", taskEntity.Status)
	}

	if execCtx.Err() != nil || runCtx.Err() != nil {
		if err := m.cancelTaskExecutionBeforeExecutor(
			runID,
			taskID,
			taskEntity.NodeID,
			&previousTaskOutcome,
			"任务已取消",
		); err != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("补偿 executor 前取消失败")
		}
		runCompleted = true
		return
	}
	runStartedAt := now
	providerResult := m.executeProvider(execCtx, taskEntity, runID, reason, chainRunID, func(level, message string) {
		m.logDispatcher.Dispatch(taskID, runIDPtr, level, message, string(StatusRunning))
	}, func(sample executor.ProgressSample) {
		m.sampleWriter.Write(taskID, taskEntity.NodeID, runStartedAt, sample)
		if sample.Percent > 0 {
			m.sampleWriter.WriteProgress(taskID, runID, sample.Percent)
		}
	})
	exitCode, err := providerResult.ExitCode, providerResult.Err
	suppressRetry := providerResult.SuppressRetry

	wasTimeout := !suppressRetry && (errors.Is(err, context.DeadlineExceeded) || errors.Is(execCtx.Err(), context.DeadlineExceeded))
	if wasTimeout {
		errorMsg := fmt.Sprintf("任务执行超时（>%s），已强制中止", execTimeout)
		m.logDispatcher.Dispatch(taskID, runIDPtr, "error", errorMsg, taskEntity.Status)
		failedAt := time.Now().UTC()
		failedStatus := StatusFailed
		if terminalErr := m.terminalizeTaskRun(runCtx, taskID, runID, []string{model.TaskRunStatusRunning}, &failedStatus,
			map[string]interface{}{"next_run_at": nextCronRun(taskEntity.CronSpec), "last_error": errorMsg},
			StatusFailed, map[string]interface{}{"finished_at": &failedAt, "last_error": errorMsg}); terminalErr != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(terminalErr).Msg("timeout terminal persistence failed")
			return
		}
		taskEntity.Status = string(StatusFailed)
		taskEntity.LastError = errorMsg
		runCompleted = true
		return
	}

	wasCanceled := !suppressRetry && (errors.Is(err, context.Canceled) || errors.Is(execCtx.Err(), context.Canceled) || m.isCanceled(taskID))
	if wasCanceled {
		message := "任务已取消"
		canceledStatus := StatusCanceled
		finishedAt := time.Now().UTC()
		if terminalErr := m.terminalizeTaskRun(runCtx, taskID, runID, []string{model.TaskRunStatusRunning}, &canceledStatus,
			map[string]interface{}{"next_run_at": nextCronRun(taskEntity.CronSpec), "last_error": message},
			StatusCanceled, map[string]interface{}{"finished_at": &finishedAt, "last_error": message}); terminalErr != nil {
			m.logDispatcher.Dispatch(taskID, runIDPtr, "error", fmt.Sprintf("更新 canceled 失败: %v", terminalErr), taskEntity.Status)
			return
		}
		taskEntity.Status = string(StatusCanceled)
		taskEntity.LastError = message
		runCompleted = true
		m.logDispatcher.Dispatch(taskID, runIDPtr, "warn", "任务执行已取消，进程已中断", taskEntity.Status)
		return
	}
	if err == nil && exitCode == 0 {
		// Post-hook 执行
		if taskEntity.Policy != nil && taskEntity.Policy.PostHook != "" {
			hookTimeout := time.Duration(taskEntity.Policy.HookTimeoutSeconds) * time.Second
			if hookTimeout <= 0 {
				hookTimeout = 5 * time.Minute
			}
			hookCtx, hookCancel := context.WithTimeout(execCtx, hookTimeout)
			m.logDispatcher.Dispatch(taskID, runIDPtr, "info", "执行 post-hook", taskEntity.Status)
			hookErr := m.hookRunFunc(hookCtx, taskEntity, taskEntity.Policy.PostHook)
			hookCancel()
			if hookErr != nil {
				m.logDispatcher.Dispatch(taskID, runIDPtr, "warn", sanitizeTaskLastError("post-hook 失败（不影响备份结果）: "+hookErr.Error()), taskEntity.Status)
			} else {
				m.logDispatcher.Dispatch(taskID, runIDPtr, "info", "post-hook 执行成功", taskEntity.Status)
			}
		}
		if !providerResult.Managed && taskEntity.ExecutorType == "rsync" && m.backupSourceCompletionObserver != nil {
			if observeErr := m.backupSourceCompletionObserver.ObserveBackupSourceCompletion(execCtx, taskID); observeErr != nil {
				logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(observeErr).Msg("观察 Rsync 备份源完成状态失败")
			}
		}
		if execCtx.Err() != nil || runCtx.Err() != nil {
			message := "任务已取消"
			m.logDispatcher.Dispatch(taskID, runIDPtr, "warn", "备份源完成观察期间任务已取消", taskEntity.Status)
			canceledStatus := StatusCanceled
			finishedAt := time.Now().UTC()
			if terminalErr := m.terminalizeTaskRun(runCtx, taskID, runID, []string{model.TaskRunStatusRunning}, &canceledStatus,
				map[string]interface{}{"next_run_at": nextCronRun(taskEntity.CronSpec), "last_error": message},
				StatusCanceled, map[string]interface{}{"finished_at": &finishedAt, "last_error": message}); terminalErr != nil {
				logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(terminalErr).Msg("source completion cancellation terminal persistence failed")
				return
			}
			taskEntity.Status = string(StatusCanceled)
			taskEntity.LastError = message
			runCompleted = true
			return
		}

		verifyStatus := "none"

		// 检查关联策略是否启用校验
		if shouldRunLegacyVerification(providerResult, taskEntity.Policy) {
			m.logDispatcher.Dispatch(taskID, runIDPtr, "info", "开始备份完整性校验", taskEntity.Status)
			result := verifier.VerifyWithLineageGuard(execCtx, taskEntity, taskEntity.Policy.VerifySampleRate, m.db, func(level, msg string) {
				m.logDispatcher.Dispatch(taskID, runIDPtr, level, msg, string(StatusRunning))
			}, false, m.lineageGuard)
			if result.LegacyBlocked {
				m.recordLegacyResticBlock(execCtx, taskID, runIDPtr, publication.OperationLegacyIntegrity)
			}

			// 校验期间可能被取消
			if execCtx.Err() != nil {
				m.logDispatcher.Dispatch(taskID, runIDPtr, "warn", "校验期间任务已取消", taskEntity.Status)
				message := "任务已取消"
				canceledStatus := StatusCanceled
				finishedAt := time.Now().UTC()
				if terminalErr := m.terminalizeTaskRun(runCtx, taskID, runID, []string{model.TaskRunStatusRunning}, &canceledStatus,
					map[string]interface{}{"next_run_at": nextCronRun(taskEntity.CronSpec), "last_error": message},
					StatusCanceled, map[string]interface{}{"finished_at": &finishedAt, "last_error": message}); terminalErr != nil {
					logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(terminalErr).Msg("verify cancellation terminal persistence failed")
					return
				}
				taskEntity.Status = string(StatusCanceled)
				taskEntity.LastError = message
				runCompleted = true
				return
			}

			verifyStatus = result.Status

			if result.Status == "warning" || result.Status == "failed" {
				verifyMessage := sanitizeTaskLastError(result.Message)
				warningStatus := StatusWarning
				finishedAt := time.Now().UTC()
				duration := finishedAt.Sub(now).Milliseconds()
				if terminalErr := m.terminalizeTaskRun(runCtx, taskID, runID, []string{model.TaskRunStatusRunning}, &warningStatus,
					map[string]interface{}{
						"retry_count": 0, "next_run_at": nextCronRun(taskEntity.CronSpec),
						"last_error": verifyMessage, "verify_status": result.Status,
					},
					StatusWarning, map[string]interface{}{
						"finished_at": &finishedAt, "duration_ms": duration,
						"verify_status": result.Status, "last_error": verifyMessage, "progress": 100,
					}); terminalErr != nil {
					logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(terminalErr).Msg("verify warning terminal persistence failed")
					return
				}
				taskEntity.Status = string(StatusWarning)
				taskEntity.LastError = verifyMessage
				runCompleted = true
				m.logDispatcher.Dispatch(taskID, runIDPtr, "warn", "备份校验未通过: "+verifyMessage, taskEntity.Status)
				return
			}
		}

		// 计算本次执行的平均吞吐量 before the short terminal transaction.
		finishedAt := time.Now().UTC()
		duration := finishedAt.Sub(now).Milliseconds()
		var avgThroughput float64
		m.db.Model(&model.TaskTrafficSample{}).
			Where("task_id = ? AND sampled_at BETWEEN ? AND ?", taskID, runStartedAt, finishedAt).
			Select("COALESCE(AVG(throughput_mbps), 0)").Scan(&avgThroughput)

		successStatus := StatusSuccess
		if terminalErr := m.terminalizeTaskRun(runCtx, taskID, runID, []string{model.TaskRunStatusRunning}, &successStatus,
			map[string]interface{}{
				"retry_count": 0, "next_run_at": nextCronRun(taskEntity.CronSpec),
				"last_error": "", "verify_status": verifyStatus,
			},
			StatusSuccess, map[string]interface{}{
				"finished_at": &finishedAt, "duration_ms": duration,
				"verify_status": verifyStatus, "throughput_mbps": avgThroughput,
				"last_error": "", "progress": 100,
			}); terminalErr != nil {
			m.logDispatcher.Dispatch(taskID, runIDPtr, "error", fmt.Sprintf("更新 success 失败: %v", terminalErr), taskEntity.Status)
			return
		}
		taskEntity.Status = string(StatusSuccess)
		taskEntity.LastError = ""
		runCompleted = true
		// 更新关联节点的最后备份时间
		if taskEntity.NodeID > 0 {
			backupAt := time.Now().UTC()
			m.db.Model(&model.Node{}).Where("id = ?", taskEntity.NodeID).Update("last_backup_at", &backupAt)
		}
		backupLastSuccess.WithLabelValues(taskEntity.Name).SetToCurrentTime()
		m.logDispatcher.Dispatch(taskID, runIDPtr, "info", "任务执行成功", taskEntity.Status)

		// 快照差异异常检测（异步，best-effort）
		if !providerResult.Managed && m.anomalySink != nil && taskEntity.ExecutorType == "restic" && taskEntity.PolicyID != nil {
			sink := m.anomalySink
			taskCopy := taskEntity
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				findings, err := anomaly.AnalyzeSnapshotDiff(ctx, m.db, taskCopy, runID)
				if err != nil {
					logger.Module("task").Warn().
						Err(err).
						Uint("task_id", taskID).
						Msg("快照差异异常检测失败")
					return
				}
				for _, f := range findings {
					if raiseErr := sink.Raise(ctx, f); raiseErr != nil {
						logger.Module("task").Warn().
							Err(raiseErr).
							Str("detector", f.Detector).
							Str("metric", f.Metric).
							Uint("task_id", taskID).
							Msg("提升异常告警失败")
					}
				}
			}()
		}
		return
	}

	var errorMsg string
	if err != nil {
		errorMsg = sanitizeTaskLastError(err.Error())
	} else {
		errorMsg = fmt.Sprintf("任务执行失败，退出码=%d", exitCode)
	}

	var nextStatus TaskStatus
	var retryCount int
	var nextRun time.Time
	var shouldRetry bool

	if taskEntity.Policy != nil && taskEntity.Policy.MaxRetries > 0 {
		nextStatus, retryCount, nextRun, shouldRetry = m.stateMachine.NextAfterFailureConfigurable(
			StatusRunning, taskEntity.RetryCount, time.Now(),
			taskEntity.Policy.MaxRetries, taskEntity.Policy.RetryBaseSeconds,
		)
	} else {
		nextStatus, retryCount, nextRun, shouldRetry = m.stateMachine.NextAfterFailure(StatusRunning, taskEntity.RetryCount, time.Now())
	}
	if suppressRetry {
		shouldRetry = false
	}

	// 当前 TaskRun 始终标记为 failed（即使 Task 进入 retrying）。 The
	// aggregate and run are committed together so a persistence fault cannot
	// expose a half-updated terminal pair.
	failedAt := time.Now().UTC()
	failDuration := failedAt.Sub(now).Milliseconds()
	finalStatus := nextStatus
	if !shouldRetry {
		finalStatus = StatusFailed
	}
	if terminalErr := m.terminalizeTaskRun(runCtx, taskID, runID, []string{model.TaskRunStatusRunning}, &finalStatus,
		map[string]interface{}{
			"retry_count": retryCount,
			"next_run_at": func() *time.Time {
				if shouldRetry {
					return &nextRun
				}
				return nextCronRun(taskEntity.CronSpec)
			}(),
			"last_error": errorMsg,
		},
		StatusFailed, map[string]interface{}{"finished_at": &failedAt, "duration_ms": failDuration, "last_error": errorMsg}); terminalErr != nil {
		m.logDispatcher.Dispatch(taskID, runIDPtr, "error", fmt.Sprintf("保存任务失败终态失败: %v", terminalErr), taskEntity.Status)
		return
	}
	taskEntity.Status = string(finalStatus)
	taskEntity.RetryCount = retryCount
	taskEntity.LastError = errorMsg
	runCompleted = true

	if shouldRetry {
		m.logDispatcher.Dispatch(taskID, runIDPtr, "warn", fmt.Sprintf("任务失败，计划重试 #%d，计划时间: %s", retryCount, nextRun.Local().Format(config.DisplayTimeFormatTZ)), taskEntity.Status)
		return
	}

	m.logDispatcher.Dispatch(taskID, runIDPtr, "error", fmt.Sprintf("任务最终失败: %s", errorMsg), taskEntity.Status)
}

// runRestoreTask 执行恢复任务。与 runTask 不同，恢复不影响原始 Task 的状态，
// 仅更新 TaskRun 记录。使用内存中交换了 source/target 的任务副本。
func (m *Manager) runRestoreTask(taskID uint, runID uint, restoreTask model.Task) {
	execCtx, cancel := context.WithTimeout(context.Background(), computeExecTimeout(restoreTask))
	execCtx = m.withTaskCredentialAuditContext(execCtx, restoreTask, runID, "restore", map[string]any{
		"operation": "restore_task",
	})
	m.chainRunner.Store(taskID, cancel)
	m.runRestoreTaskWithContext(taskID, runID, restoreTask, execCtx, nil, cancel)
}

func (m *Manager) runRestoreTaskWithContext(
	taskID uint,
	runID uint,
	restoreTask model.Task,
	execCtx context.Context,
	ownership *pendingRunOwnership,
	cancel context.CancelFunc,
) {
	if ownership != nil {
		defer m.pendingRuns.CompareAndDelete(taskID, ownership)
	}
	defer m.locks.Delete(taskID) // 清理任务锁，防止 sync.Map 无限增长
	defer m.restoreNodes.Delete(restoreTask.NodeID)
	ownerClaimed, ownerErr := m.claimTaskRunOwner(context.Background(), taskID, runID)
	if ownerErr != nil {
		logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(ownerErr).Msg("claim restore task run execution lease")
		return
	}
	if !ownerClaimed {
		if execCtx.Err() != nil {
			if ownership != nil {
				ownership.waitCancellationPersistence()
			}
			if err := m.cancelRestoreTaskRunBeforeExecutor(context.Background(), taskID, runID, "恢复任务已取消"); err != nil {
				logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("保留恢复启动前取消状态失败")
			}
		}
		logger.Module("task").Info().Uint("task_id", taskID).Uint("task_run_id", runID).Msg("restore task run execution lease owned by another process")
		return
	}
	heartbeatCancel := m.startTaskRunHeartbeat(execCtx, runID, cancel)
	defer heartbeatCancel()

	if isLegacyGuardedProvider(restoreTask.ExecutorType) && m.lineageGuard != nil {
		session, err := m.lineageGuard.Begin(execCtx, taskID, publication.OperationLegacyRestoreLatest)
		if err != nil || session == nil || session.Mode() != publication.LineageCompatibility {
			if session != nil {
				defer func() { _ = session.Close() }()
			}
			m.recordLegacyResticBlock(execCtx, taskID, &runID, publication.OperationLegacyRestoreLatest)
			m.failLegacyRestore(taskID, runID)
			return
		}
		defer func() { _ = session.Close() }()
	}

	if execCtx.Err() != nil {
		if ownership != nil {
			ownership.waitCancellationPersistence()
		}
		if err := m.cancelRestoreTaskRunBeforeExecutor(context.Background(), taskID, runID, "恢复任务已取消"); err != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("保留恢复启动前取消状态失败")
		}
		return
	}
	m.sampleWriter.ResetThrottle(taskID)

	runCompleted := false
	defer func() {
		if ownerClaimed && !runCompleted {
			if err := m.recoverRestoreTaskRunOnReturn(context.Background(), taskID, runID, "恢复任务启动前异常退出"); err != nil {
				logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("标记恢复启动前异常 TaskRun 失败")
			}
		}
	}()

	// context-aware semaphore：取消时立即返回
	select {
	case m.semaphore <- struct{}{}:
	case <-execCtx.Done():
		if ownership != nil {
			ownership.waitCancellationPersistence()
		}
		if err := m.cancelRestoreTaskRunBeforeExecutor(context.Background(), taskID, runID, "恢复任务已取消"); err != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("取消排队恢复 TaskRun 失败")
			return
		}
		runCompleted = true
		return
	}
	defer func() { <-m.semaphore }()

	lock := m.taskLock(taskID)
	if !acquireLockWithContext(execCtx, lock) {
		if ownership != nil {
			ownership.waitCancellationPersistence()
		}
		if err := m.cancelRestoreTaskRunBeforeExecutor(context.Background(), taskID, runID, "恢复任务已取消"); err != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("取消等待恢复任务锁失败")
			return
		}
		runCompleted = true
		return
	}
	defer lock.Unlock()

	strategyLock := m.strategyLock(restoreTask.NodeID, restoreTask.PolicyID)
	if !acquireLockWithContext(execCtx, strategyLock) {
		if ownership != nil {
			ownership.waitCancellationPersistence()
		}
		if err := m.cancelRestoreTaskRunBeforeExecutor(context.Background(), taskID, runID, "恢复任务已取消"); err != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("取消等待恢复策略锁失败")
			return
		}
		runCompleted = true
		return
	}
	defer strategyLock.Unlock()

	runIDPtr := &runID

	now := time.Now().UTC()
	if err := m.enterTaskExecution(execCtx, runID, restoreTask.NodeID, now, nil); err != nil {
		if errors.Is(err, errCronTaskBlocked) {
			runCompleted = true
			return
		}
		if execCtx.Err() != nil || errors.Is(err, context.Canceled) {
			if cancelErr := m.terminalizeRestoreTaskRun(context.Background(), taskID, runID, model.TaskRunActiveStatuses(), StatusCanceled,
				map[string]interface{}{"started_at": nil, "duration_ms": int64(0), "last_error": "恢复任务已取消"}); cancelErr != nil {
				logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(cancelErr).Msg("保留恢复执行入口取消状态失败")
				return
			}
			runCompleted = true
			return
		}
		m.logDispatcher.Dispatch(taskID, runIDPtr, "error", fmt.Sprintf("恢复 TaskRun 执行入口拒绝: %v", err), "")
		if rejectErr := m.failTaskRunBeforeExecutor(context.Background(), runID, "恢复任务执行入口拒绝"); rejectErr != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(rejectErr).Msg("保存恢复执行入口拒绝终态失败")
		} else {
			runCompleted = true
		}
		return
	}
	if execCtx.Err() != nil {
		if err := m.terminalizeRestoreTaskRun(context.Background(), taskID, runID, []string{model.TaskRunStatusRunning}, StatusCanceled,
			map[string]interface{}{"started_at": nil, "duration_ms": int64(0), "last_error": "恢复任务已取消"}); err != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("补偿恢复预检前取消失败")
			return
		}
		runCompleted = true
		return
	}

	m.logDispatcher.Dispatch(taskID, runIDPtr, "info", "开始恢复任务", "")

	// 恢复前检查：在远程节点上检查源路径（备份）和目标路径
	m.logDispatcher.Dispatch(taskID, runIDPtr, "info", "执行恢复前检查（目标路径、磁盘空间）", "")
	if err := m.ensureRemoteTargetReadyFunc(execCtx, restoreTask.Node, restoreTask.RsyncTarget); err != nil {
		// 区分取消与真实失败
		if execCtx.Err() != nil {
			finishedAt := time.Now().UTC()
			if terminalErr := m.terminalizeRestoreTaskRun(context.Background(), taskID, runID, []string{model.TaskRunStatusRunning}, StatusCanceled,
				map[string]interface{}{"finished_at": &finishedAt, "last_error": "恢复任务已取消"}); terminalErr != nil {
				logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(terminalErr).Msg("恢复前检查取消终态保存失败")
				return
			}
			runCompleted = true
			m.logDispatcher.Dispatch(taskID, runIDPtr, "warn", "恢复前检查期间任务已取消", "canceled")
			return
		}
		errorMsg := sanitizeTaskLastError(fmt.Sprintf("恢复前检查失败（目标路径）: %s", err.Error()))
		finishedAt := time.Now().UTC()
		duration := finishedAt.Sub(now).Milliseconds()
		if terminalErr := m.terminalizeRestoreTaskRun(execCtx, taskID, runID, []string{model.TaskRunStatusRunning}, StatusFailed,
			map[string]interface{}{"finished_at": &finishedAt, "duration_ms": duration, "last_error": errorMsg}); terminalErr != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(terminalErr).Msg("恢复前检查失败终态保存失败")
			return
		}
		runCompleted = true
		m.logDispatcher.Dispatch(taskID, runIDPtr, "error", errorMsg, "failed")
		m.alertDispatcher.RaiseTaskFailure(restoreTask, runIDPtr, errorMsg) //nolint:errcheck // best-effort alert during restore failure
		return
	}
	exec := m.executorFactory.Resolve(restoreTask.ExecutorType)
	restoreExec, ok := exec.(executor.RestoreExecutor)
	if !ok {
		errorMsg := "该执行器类型不支持恢复操作"
		finishedAt := time.Now().UTC()
		duration := finishedAt.Sub(now).Milliseconds()
		if terminalErr := m.terminalizeRestoreTaskRun(execCtx, taskID, runID, []string{model.TaskRunStatusRunning}, StatusFailed,
			map[string]interface{}{"finished_at": &finishedAt, "duration_ms": duration, "last_error": errorMsg}); terminalErr != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(terminalErr).Msg("恢复执行器不支持终态保存失败")
			return
		}
		runCompleted = true
		m.logDispatcher.Dispatch(taskID, runIDPtr, "error", errorMsg, "failed")
		return
	}

	if execCtx.Err() != nil {
		if err := m.terminalizeRestoreTaskRun(context.Background(), taskID, runID, []string{model.TaskRunStatusRunning}, StatusCanceled,
			map[string]interface{}{"last_error": "恢复任务已取消"}); err != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("补偿恢复 executor 前取消失败")
			return
		}
		runCompleted = true
		return
	}
	_, err := restoreExec.RunRestore(execCtx, restoreTask, func(level, message string) {
		m.logDispatcher.Dispatch(taskID, runIDPtr, level, message, "running")
	}, func(sample executor.ProgressSample) {
		m.sampleWriter.Write(taskID, restoreTask.NodeID, now, sample)
		if sample.Percent > 0 {
			m.sampleWriter.WriteProgress(taskID, runID, sample.Percent)
		}
	})

	// 检查是否被取消
	wasCanceled := errors.Is(err, context.Canceled) || errors.Is(execCtx.Err(), context.Canceled)
	if wasCanceled {
		finishedAt := time.Now().UTC()
		if terminalErr := m.terminalizeRestoreTaskRun(context.Background(), taskID, runID, []string{model.TaskRunStatusRunning}, StatusCanceled,
			map[string]interface{}{"finished_at": &finishedAt, "last_error": "恢复任务已取消"}); terminalErr != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(terminalErr).Msg("恢复任务取消终态保存失败")
			return
		}
		runCompleted = true
		m.logDispatcher.Dispatch(taskID, runIDPtr, "warn", "恢复任务已取消", "canceled")
		return
	}

	if err != nil {
		errorMsg := sanitizeTaskLastError(err.Error())
		finishedAt := time.Now().UTC()
		duration := finishedAt.Sub(now).Milliseconds()
		if terminalErr := m.terminalizeRestoreTaskRun(execCtx, taskID, runID, []string{model.TaskRunStatusRunning}, StatusFailed,
			map[string]interface{}{"finished_at": &finishedAt, "duration_ms": duration, "last_error": errorMsg}); terminalErr != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(terminalErr).Msg("恢复任务失败终态保存失败")
			return
		}
		runCompleted = true
		m.logDispatcher.Dispatch(taskID, runIDPtr, "error", fmt.Sprintf("恢复任务失败: %s", errorMsg), "failed")
		m.alertDispatcher.RaiseTaskFailure(restoreTask, runIDPtr, errorMsg) //nolint:errcheck // best-effort alert during restore failure
		return
	}

	// 恢复成功后强制执行完整性校验（不再依赖 Policy.VerifyEnabled）
	verifyStatus := "none" //nolint:ineffassign
	sampleRate := 100      // 默认全量校验
	if restoreTask.Policy != nil && restoreTask.Policy.VerifySampleRate > 0 {
		sampleRate = restoreTask.Policy.VerifySampleRate
	}

	m.logDispatcher.Dispatch(taskID, runIDPtr, "info", fmt.Sprintf("开始恢复后完整性校验（采样率 %d%%）", sampleRate), "")
	result := verifier.Verify(execCtx, restoreTask, sampleRate, m.db, func(level, msg string) {
		m.logDispatcher.Dispatch(taskID, runIDPtr, level, msg, "")
	}, true)

	// 校验期间可能被取消
	if execCtx.Err() != nil {
		finishedAt := time.Now().UTC()
		if terminalErr := m.terminalizeRestoreTaskRun(context.Background(), taskID, runID, []string{model.TaskRunStatusRunning}, StatusCanceled,
			map[string]interface{}{"finished_at": &finishedAt, "last_error": "恢复任务已取消"}); terminalErr != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(terminalErr).Msg("恢复校验取消终态保存失败")
			return
		}
		runCompleted = true
		m.logDispatcher.Dispatch(taskID, runIDPtr, "warn", "恢复校验期间任务已取消", "canceled")
		return
	}

	verifyStatus = result.Status

	if result.Status == "warning" || result.Status == "failed" {
		verifyMessage := sanitizeTaskLastError(result.Message)
		finishedAt := time.Now().UTC()
		duration := finishedAt.Sub(now).Milliseconds()
		if terminalErr := m.terminalizeRestoreTaskRun(execCtx, taskID, runID, []string{model.TaskRunStatusRunning}, StatusWarning,
			map[string]interface{}{
				"finished_at": &finishedAt, "duration_ms": duration,
				"verify_status": verifyStatus, "last_error": verifyMessage, "progress": 100,
			}); terminalErr != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(terminalErr).Msg("恢复校验 warning 终态保存失败")
			return
		}
		runCompleted = true
		m.logDispatcher.Dispatch(taskID, runIDPtr, "warn", "恢复后校验未通过: "+verifyMessage, "warning")
		m.alertDispatcher.RaiseVerificationFailure(restoreTask, runIDPtr, verifyMessage) //nolint:errcheck // best-effort alert during verification failure
		return
	}
	m.logDispatcher.Dispatch(taskID, runIDPtr, "info", "恢复后完整性校验通过", "")

	finishedAt := time.Now().UTC()
	duration := finishedAt.Sub(now).Milliseconds()
	if terminalErr := m.terminalizeRestoreTaskRun(execCtx, taskID, runID, []string{model.TaskRunStatusRunning}, StatusSuccess,
		map[string]interface{}{
			"finished_at": &finishedAt, "duration_ms": duration,
			"verify_status": verifyStatus, "last_error": "", "progress": 100,
		}); terminalErr != nil {
		logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(terminalErr).Msg("恢复成功终态保存失败")
		return
	}
	runCompleted = true
	m.logDispatcher.Dispatch(taskID, runIDPtr, "info", "恢复任务执行成功", "success")
	if resolveErr := m.alertDispatcher.ResolveTaskAlerts(taskID, "恢复任务成功"); resolveErr != nil {
		logger.Module("task").Warn().Uint("task_id", taskID).Err(resolveErr).Msg("ResolveTaskAlerts 失败")
	}
}

func (m *Manager) failLegacyRestore(taskID uint, runID uint) {
	finishedAt := time.Now().UTC()
	if err := m.terminalizeRestoreTaskRun(context.Background(), taskID, runID, []string{model.TaskRunStatusPending, model.TaskRunStatusRunning}, StatusFailed,
		map[string]interface{}{"finished_at": &finishedAt, "last_error": string(backupasset.FailureLegacyOperationBlocked)}); err != nil {
		logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).Err(err).Msg("保存旧路径阻止恢复终态失败")
		return
	}
	m.logDispatcher.Dispatch(taskID, &runID, "warn", "受管备份恢复操作已被安全边界阻止", "failed")
}

func isLegacyGuardedProvider(executorType string) bool {
	switch strings.ToLower(strings.TrimSpace(executorType)) {
	case "restic", "rsync", "rclone":
		return true
	default:
		return false
	}
}

func (m *Manager) recordLegacyResticBlock(ctx context.Context, taskID uint, taskRunID *uint, operation publication.ResticOperation) {
	if m == nil || m.legacyBlockRecorder == nil {
		return
	}
	audit, err := publication.NewSystemLegacyBlockAuditContext(taskID, taskRunID, operation)
	if err != nil {
		logger.Module("task").Warn().Uint("task_id", taskID).Str("operation", string(operation)).Msg("无法构造 Restic 旧路径阻止审计上下文")
		return
	}
	if err := m.legacyBlockRecorder.RecordLegacyBlock(ctx, publication.LegacyBlock{
		TaskID: taskID, TaskRunID: taskRunID, Operation: operation, Audit: audit,
	}); err != nil {
		logger.Module("task").Warn().Uint("task_id", taskID).Str("operation", string(operation)).Msg("Restic 旧路径阻止审计未写入")
	}
}

func (m *Manager) withTaskCredentialAuditContext(ctx context.Context, taskEntity model.Task, runID uint, triggerType string, metadata map[string]any) context.Context {
	baseMetadata := map[string]any{
		"trigger_type":  triggerType,
		"executor_type": strings.ToLower(strings.TrimSpace(taskEntity.ExecutorType)),
		"source":        strings.TrimSpace(taskEntity.Source),
	}
	for key, value := range metadata {
		baseMetadata[key] = value
	}
	return credentialaudit.WithRuntimeContext(ctx, m.db, credentialaudit.Event{
		Username:  "system",
		Role:      "system",
		Action:    "task.credential.use",
		NodeID:    credentialaudit.PtrUint(taskEntity.NodeID),
		TaskID:    credentialaudit.PtrUint(taskEntity.ID),
		TaskRunID: credentialaudit.PtrUint(runID),
		PolicyID:  taskEntity.PolicyID,
		Metadata:  baseMetadata,
	})
}

func (m *Manager) renderPolicyProfileHooks(taskEntity *model.Task) error {
	if taskEntity.Policy == nil || strings.TrimSpace(taskEntity.Policy.AppProfile) == "" {
		return nil
	}
	if taskEntity.Policy.AppCredentialID == nil || *taskEntity.Policy.AppCredentialID == 0 {
		return fmt.Errorf("应用感知备份缺少凭据")
	}
	access, err := profile.ResolveAppProfileAccess(m.db, *taskEntity.Policy.AppCredentialID)
	if err != nil {
		return err
	}
	renderedPreHook, renderedPostHook, err := profile.RenderHooks(taskEntity.Policy.AppProfile, access.Config())
	if err != nil {
		return err
	}
	if strings.TrimSpace(taskEntity.Policy.PreHook) == "" {
		taskEntity.Policy.PreHook = renderedPreHook
	}
	if strings.TrimSpace(taskEntity.Policy.PostHook) == "" {
		taskEntity.Policy.PostHook = renderedPostHook
	}
	return nil
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

// skipTask atomically creates a skipped TaskRun and updates the aggregate Task.
// Child skips are themselves durable effects, so a crash after this commit can
// be recovered without recursively losing the dependency boundary.
func (m *Manager) skipTask(taskEntity model.Task, chainRunID string, upstreamRunID *uint, skipReason string) error {
	now := time.Now().UTC()
	var run model.TaskRun
	var created bool
	err := m.db.Transaction(func(tx *gorm.DB) error {
		var locked model.Task
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", taskEntity.ID).Limit(1).Find(&locked)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		if upstreamRunID != nil {
			existingResult := tx.Where("task_id = ? AND upstream_task_run_id = ?", locked.ID, *upstreamRunID).
				Order("id ASC").Limit(1).Find(&run)
			if existingResult.Error != nil {
				return existingResult.Error
			}
			if existingResult.RowsAffected == 1 {
				return nil
			}
		}
		run = model.TaskRun{
			TaskID: locked.ID, TriggerType: "chain", Status: model.TaskRunStatusSkipped,
			ChainRunID: chainRunID, UpstreamTaskRunID: upstreamRunID, SkipReason: skipReason,
			StartedAt: &now, FinishedAt: &now,
		}
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		created = true
		if ParseStatus(locked.Status) != StatusSkipped {
			if err := m.stateMachine.ValidateTransition(ParseStatus(locked.Status), StatusSkipped); err != nil {
				return err
			}
			taskResult := tx.Model(&model.Task{}).Where("id = ? AND status = ?", locked.ID, locked.Status).
				Updates(map[string]interface{}{"status": string(StatusSkipped), "last_error": skipReason})
			if taskResult.Error != nil {
				return taskResult.Error
			}
			if taskResult.RowsAffected != 1 {
				return errTaskRunCASLost
			}
		}
		var downstreams []model.Task
		if err := tx.Where("depends_on_task_id = ?", locked.ID).Order("id ASC").Find(&downstreams).Error; err != nil {
			return err
		}
		effects := make([]taskRunTerminalEffect, 0, len(downstreams))
		for _, downstream := range downstreams {
			payload, _ := json.Marshal(downstreamTaskRunEffect{
				TaskID: downstream.ID, UpstreamRunID: run.ID, ChainRunID: chainRunID,
			})
			effects = append(effects, taskRunTerminalEffect{
				Key:  fmt.Sprintf("downstream:%d", downstream.ID),
				Type: model.TaskRunEffectTypeDownstreamSkip, Payload: string(payload),
			})
		}
		return persistTaskRunEffectsTx(tx, run.ID, effects)
	})
	if err != nil {
		return err
	}
	if !created {
		if run.ID != 0 {
			return m.drainTaskRunEffects(context.Background(), run.ID)
		}
		return nil
	}
	runID := run.ID
	m.logDispatcher.Dispatch(taskEntity.ID, &runID, "warn", skipReason, string(StatusSkipped))
	return m.drainTaskRunEffects(context.Background(), run.ID)
}

// isNodeRestoring 检查指定节点是否有恢复任务正在运行（内存级持续互斥）。
func (m *Manager) isNodeRestoring(nodeID uint) bool {
	_, ok := m.restoreNodes.Load(nodeID)
	return ok
}

// hasNodeConflictForRestore 检查同节点上是否有任何运行中的任务。
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

func (m *Manager) cancelTaskRunBeforeExecutor(runID uint, message string) error {
	finishedAt := time.Now().UTC()
	return m.db.Model(&model.TaskRun{}).
		Where("id = ? AND status = ?", runID, "pending").
		Updates(map[string]interface{}{
			"status":      "canceled",
			"started_at":  nil,
			"finished_at": &finishedAt,
			"duration_ms": int64(0),
			"last_error":  message,
		}).Error
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
