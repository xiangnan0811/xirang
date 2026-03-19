package task

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/task/verifier"
)

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
	defer m.locks.Delete(taskID) // 清理任务锁，防止 sync.Map 无限增长

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

	// 检查节点是否已归档
	if taskEntity.Node.Archived {
		m.emitLog(taskID, runIDPtr, "warn", "节点已归档，跳过执行", taskEntity.Status)
		canceledAt := time.Now()
		m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
			"status":      "canceled",
			"finished_at": &canceledAt,
			"last_error":  "节点已归档",
		})
		runCompleted = true
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
	defer cancel()

	// Pre-hook 执行
	if taskEntity.Policy != nil && taskEntity.Policy.PreHook != "" {
		hookTimeout := time.Duration(taskEntity.Policy.HookTimeoutSeconds) * time.Second
		if hookTimeout <= 0 {
			hookTimeout = 5 * time.Minute
		}
		hookCtx, hookCancel := context.WithTimeout(execCtx, hookTimeout)
		m.emitLog(taskID, runIDPtr, "info", "执行 pre-hook: "+taskEntity.Policy.PreHook, taskEntity.Status)
		hookErr := m.hookRunFunc(hookCtx, taskEntity, taskEntity.Policy.PreHook)
		hookCancel()
		if hookErr != nil {
			m.emitLog(taskID, runIDPtr, "error", "pre-hook 失败: "+hookErr.Error(), taskEntity.Status)
			errorMsg := fmt.Sprintf("pre-hook 执行失败: %v", hookErr)
			failedAt := time.Now()
			m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
				"status":      "failed",
				"finished_at": &failedAt,
				"last_error":  errorMsg,
			})
			runCompleted = true
			m.updateStatus(&taskEntity, StatusFailed, map[string]interface{}{
				"next_run_at": nextCronRun(taskEntity.CronSpec),
				"last_error":  errorMsg,
			})
			return
		}
		m.emitLog(taskID, runIDPtr, "info", "pre-hook 执行成功", taskEntity.Status)
	}

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
		// Post-hook 执行
		if taskEntity.Policy != nil && taskEntity.Policy.PostHook != "" {
			hookTimeout := time.Duration(taskEntity.Policy.HookTimeoutSeconds) * time.Second
			if hookTimeout <= 0 {
				hookTimeout = 5 * time.Minute
			}
			hookCtx, hookCancel := context.WithTimeout(execCtx, hookTimeout)
			m.emitLog(taskID, runIDPtr, "info", "执行 post-hook: "+taskEntity.Policy.PostHook, taskEntity.Status)
			hookErr := m.hookRunFunc(hookCtx, taskEntity, taskEntity.Policy.PostHook)
			hookCancel()
			if hookErr != nil {
				m.emitLog(taskID, runIDPtr, "warn", "post-hook 失败（不影响备份结果）: "+hookErr.Error(), taskEntity.Status)
			} else {
				m.emitLog(taskID, runIDPtr, "info", "post-hook 执行成功", taskEntity.Status)
			}
		}

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

// runRestoreTask 执行恢复任务。与 runTask 不同，恢复不影响原始 Task 的状态，
// 仅更新 TaskRun 记录。使用内存中交换了 source/target 的任务副本。
func (m *Manager) runRestoreTask(taskID uint, runID uint, restoreTask model.Task) {
	defer m.pendingRuns.Delete(taskID)
	defer m.locks.Delete(taskID) // 清理任务锁，防止 sync.Map 无限增长

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

func (m *Manager) isCanceled(taskID uint) bool {
	var current struct {
		Status string
	}
	if err := m.db.Model(&model.Task{}).Select("status").Where("id = ?", taskID).Take(&current).Error; err != nil {
		return false
	}
	return ParseStatus(current.Status) == StatusCanceled
}
