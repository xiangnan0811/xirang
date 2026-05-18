package task

import (
	"context"
	"errors"
	"fmt"
	pathpkg "path"
	"strings"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/util"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

// validateDrillConfig 校验演习配置的合法性。
// 返回 nil 表示配置合法。
func (m *Manager) validateDrillConfig(policy *model.Policy, sandboxNode *model.Node) error {
	if sandboxNode == nil {
		return fmt.Errorf("沙箱节点不存在")
	}

	// 沙箱节点不能是备份源节点
	for _, n := range policy.Nodes {
		if n.ID == sandboxNode.ID {
			return fmt.Errorf("沙箱节点不能是备份源节点 (%s)", n.Name)
		}
	}

	// 校验恢复路径
	restorePath := strings.TrimSpace(policy.DrillRestorePath)
	if restorePath == "" {
		restorePath = "/tmp/xirang-drill"
	}
	if err := validateDrillSandboxPath(restorePath); err != nil {
		return fmt.Errorf("演习恢复路径无效: %w", err)
	}

	if policy.DrillTargetNodeID == nil || *policy.DrillTargetNodeID == 0 {
		return fmt.Errorf("未配置沙箱节点")
	}

	return nil
}

// validateDrillSandboxPath 校验恢复演练沙箱路径，禁止指向系统目录及其子路径。
func validateDrillSandboxPath(path string) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return fmt.Errorf("恢复路径不能为空")
	}
	if err := validateRestorePath(trimmed); err != nil {
		return err
	}
	cleanPath := pathpkg.Clean(trimmed)
	forbidden := []string{"/", "/etc", "/usr", "/bin", "/sbin", "/boot", "/dev", "/proc", "/sys", "/run", "/var/run"}
	for _, dir := range forbidden {
		if cleanPath == dir || strings.HasPrefix(cleanPath, dir+"/") {
			return fmt.Errorf("禁止恢复到系统目录: %s", dir)
		}
	}
	return nil
}

// TriggerDrill 手动或 cron 触发一次恢复演练。
// 返回创建的 drill TaskRun ID。
func (m *Manager) TriggerDrill(policyID uint) (uint, error) {
	if m.shuttingDown.Load() {
		return 0, fmt.Errorf("系统维护中，请稍候再试")
	}

	// 1. 加载策略 + 关联源节点 + 沙箱节点
	var policy model.Policy
	if err := m.db.Preload("Nodes").First(&policy, policyID).Error; err != nil {
		return 0, fmt.Errorf("策略不存在")
	}

	if !policy.DrillEnabled {
		return 0, fmt.Errorf("该策略未启用恢复演练")
	}

	if policy.DrillTargetNodeID == nil {
		return 0, fmt.Errorf("该策略未配置沙箱节点")
	}

	var sandboxNode model.Node
	if err := m.db.Preload("SSHKey").First(&sandboxNode, *policy.DrillTargetNodeID).Error; err != nil {
		return 0, fmt.Errorf("沙箱节点不存在")
	}

	// 2. 校验配置
	if err := m.validateDrillConfig(&policy, &sandboxNode); err != nil {
		return 0, err
	}

	// 3. 查找关联任务（取第一个有成功记录的 rsync 任务）
	task, err := m.findTaskForPolicy(policy.ID)
	if err != nil {
		return 0, err
	}

	// 4. 创建 drill TaskRun
	now := time.Now()
	run := model.TaskRun{
		TaskID:      task.ID,
		TriggerType: "drill",
		Status:      "pending",
		StartedAt:   &now,
	}
	if err := m.db.Create(&run).Error; err != nil {
		return 0, fmt.Errorf("创建演习执行记录失败: %w", err)
	}

	// 5. 异步执行演习
	m.taskWG.Add(1)
	go func() {
		defer m.taskWG.Done()
		m.executeDrill(&policy, task, sandboxNode, run.ID)
	}()

	return run.ID, nil
}

// findTaskForPolicy 查找策略关联的任务（优先取有成功记录的，否则取第一个 rsync 任务）。
func (m *Manager) findTaskForPolicy(policyID uint) (model.Task, error) {
	var tasks []model.Task
	if err := m.db.Preload("Node").Preload("Node.SSHKey").Preload("Policy").
		Where("policy_id = ?", policyID).
		Where("executor_type IN ?", []string{"rsync", "restic", "rclone"}).
		Find(&tasks).Error; err != nil {
		return model.Task{}, fmt.Errorf("查询关联任务失败: %w", err)
	}

	if len(tasks) == 0 {
		return model.Task{}, fmt.Errorf("该策略没有关联的备份任务")
	}

	// 优先取有成功记录的任务
	for _, t := range tasks {
		var count int64
		if err := m.db.Model(&model.TaskRun{}).
			Where("task_id = ? AND status = ?", t.ID, "success").
			Count(&count).Error; err != nil {
			return model.Task{}, fmt.Errorf("查询任务成功执行记录失败: %w", err)
		}
		if count > 0 {
			return t, nil
		}
	}

	// 没有成功记录，取第一个
	return tasks[0], nil
}

// executeDrill 执行演习的核心流程：恢复备份到沙箱 → 执行校验脚本 → 清理。
func (m *Manager) executeDrill(policy *model.Policy, task model.Task, sandboxNode model.Node, drillRunID uint) {
	drillStartedAt := time.Now()
	runIDPtr := &drillRunID
	restorePath := strings.TrimSpace(policy.DrillRestorePath)
	if restorePath == "" {
		restorePath = "/tmp/xirang-drill"
	}
	sourceRunID, err := m.latestSuccessfulRunID(task.ID)
	if err != nil {
		logger.Module("task").Warn().Uint("task_id", task.ID).Err(err).Msg("查询恢复演练来源执行记录失败")
	}
	snapshotRef := ""
	if sourceRunID != nil {
		snapshotRef = fmt.Sprintf("task_run:%d", *sourceRunID)
	}

	evidence := model.RestoreDrillEvidence{
		PolicyID:           policy.ID,
		TaskID:             task.ID,
		TaskRunID:          drillRunID,
		SourceTaskRunID:    sourceRunID,
		SnapshotRef:        snapshotRef,
		SandboxNodeID:      sandboxNode.ID,
		SandboxNodeName:    sandboxNode.Name,
		SandboxPath:        restorePath,
		Status:             "running",
		FailedStep:         "",
		ConfidenceEligible: false,
		StartedAt:          &drillStartedAt,
		RestoreStatus:      "pending",
		VerifyStatus:       "pending",
		PostVerifyStatus:   "skipped",
		CleanupStatus:      "skipped",
	}
	if err := m.db.Create(&evidence).Error; err != nil {
		logger.Module("task").Warn().Uint("task_run_id", drillRunID).Err(err).Msg("创建恢复演练证据失败")
	}

	finishRun := func(status, lastError string, finishedAt time.Time) {
		duration := drillDurationMs(drillStartedAt, finishedAt)
		updates := map[string]interface{}{
			"status":      status,
			"finished_at": &finishedAt,
			"duration_ms": duration,
			"last_error":  util.SanitizeMessage(lastError),
		}
		if err := m.db.Model(&model.TaskRun{}).Where("id = ?", drillRunID).Updates(updates).Error; err != nil {
			logger.Module("task").Warn().Uint("task_run_id", drillRunID).Err(err).Msg("更新恢复演练执行记录失败")
		}
	}
	updateEvidence := func(updates map[string]interface{}) {
		updates["updated_at"] = time.Now()
		if err := m.db.Model(&model.RestoreDrillEvidence{}).Where("task_run_id = ?", drillRunID).Updates(updates).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				logger.Module("task").Warn().Uint("task_run_id", drillRunID).Err(err).Msg("更新恢复演练证据失败")
			}
		}
	}
	failDrill := func(step string, err error, alertCode string, messagePrefix string) {
		finishedAt := time.Now()
		errorMsg := messagePrefix
		if err != nil {
			errorMsg = fmt.Sprintf("%s: %v", messagePrefix, err)
		}
		sanitizedErr := util.SanitizeMessage(errorMsg)
		finishRun("failed", sanitizedErr, finishedAt)
		updates := map[string]interface{}{
			"status":              "failed",
			"failed_step":         step,
			"confidence_eligible": false,
			"finished_at":         &finishedAt,
			"duration_ms":         drillDurationMs(drillStartedAt, finishedAt),
		}
		switch step {
		case "sandbox_precheck", "restore":
			updates["restore_status"] = "failed"
			updates["restore_finished_at"] = &finishedAt
			updates["restore_error"] = sanitizedErr
		case "pre_verify", "verify":
			updates["verify_status"] = "failed"
			updates["verify_finished_at"] = &finishedAt
			updates["verify_error"] = sanitizedErr
		case "post_verify":
			updates["post_verify_status"] = "failed"
			updates["post_verify_finished_at"] = &finishedAt
			updates["post_verify_error"] = sanitizedErr
		case "restore_path":
			updates["restore_status"] = "failed"
			updates["restore_finished_at"] = &finishedAt
			updates["restore_error"] = sanitizedErr
		case "cleanup_boundary", "cleanup":
			updates["cleanup_status"] = "failed"
			updates["cleanup_finished_at"] = &finishedAt
			updates["cleanup_error"] = sanitizedErr
		}
		updateEvidence(updates)
		m.emitLog(task.ID, runIDPtr, "error", sanitizedErr, "")
		_ = alerting.RaiseDrillFailure(m.db, policy.ID, policy.Name, sandboxNode.Name, sandboxNode.ID, alertCode, sanitizedErr)
		m.dispatchDrillFailure(policy.ID, drillRunID)
	}

	// 确保无论成败都清理状态
	drillCompleted := false
	defer func() {
		if !drillCompleted {
			finishedAt := time.Now()
			finishRun("failed", "演习执行异常终止", finishedAt)
			updateEvidence(map[string]interface{}{
				"status":              "failed",
				"failed_step":         "unknown",
				"confidence_eligible": false,
				"finished_at":         &finishedAt,
				"duration_ms":         drillDurationMs(drillStartedAt, finishedAt),
			})
		}
	}()

	// 更新 drill TaskRun 为 running
	now := time.Now()
	if err := m.db.Model(&model.TaskRun{}).Where("id = ?", drillRunID).Updates(map[string]interface{}{
		"status":     "running",
		"started_at": &now,
	}).Error; err != nil {
		logger.Module("task").Warn().Uint("task_run_id", drillRunID).Err(err).Msg("标记恢复演练执行记录运行中失败")
	}
	drillStartedAt = now
	updateEvidence(map[string]interface{}{
		"status":     "running",
		"started_at": &now,
	})

	m.emitLog(task.ID, runIDPtr, "info", fmt.Sprintf(
		"开始恢复演练: 策略=%s, 沙箱=%s(%s), 恢复路径=%s",
		policy.Name, sandboxNode.Name, sandboxNode.Host, restorePath), "")

	if err := validateDrillSandboxPath(restorePath); err != nil {
		failDrill("restore_path", err, "drill_restore_path_invalid", "演习恢复路径无效")
		drillCompleted = true
		return
	}

	// ---- Step 0: 沙箱节点连通性预检 ----
	restoreStartedAt := time.Now()
	updateEvidence(map[string]interface{}{
		"restore_status":     "running",
		"restore_started_at": &restoreStartedAt,
	})
	m.emitLog(task.ID, runIDPtr, "info", "正在检查沙箱节点连通性...", "")
	if err := m.runDrillScript(context.Background(), sandboxNode, "true"); err != nil {
		failDrill("sandbox_precheck", err, "drill_sandbox_unreachable", fmt.Sprintf("沙箱节点 (%s) 不可达", sandboxNode.Name))
		drillCompleted = true
		return
	}
	m.emitLog(task.ID, runIDPtr, "info", "沙箱节点连通性检查通过", "")

	// ---- Step 1: 恢复备份到沙箱 ----
	m.emitLog(task.ID, runIDPtr, "info", "正在恢复备份到沙箱节点...", "")
	restoreErr := m.runDrillRestore(context.Background(), task, sandboxNode, restorePath, func(level, msg string) {
		if runIDPtr != nil {
			m.emitLog(task.ID, runIDPtr, level, msg, "")
		}
	})
	restoreFinishedAt := time.Now()
	if restoreErr != nil {
		failDrill("restore", restoreErr, "drill_restore_failed", "恢复备份到沙箱失败")
		drillCompleted = true
		return
	}
	updateEvidence(map[string]interface{}{
		"restore_status":      "success",
		"restore_finished_at": &restoreFinishedAt,
		"restore_error":       "",
	})
	m.emitLog(task.ID, runIDPtr, "info", "备份恢复至沙箱完成", "")

	// ---- Step 2: 执行校验脚本 ----
	verifyStartedAt := time.Now()
	updateEvidence(map[string]interface{}{
		"verify_status":     "running",
		"verify_started_at": &verifyStartedAt,
	})
	verifyFailed := false
	failedStep := ""
	var verifyErr error

	// pre_verify
	if strings.TrimSpace(policy.DrillPreVerify) != "" {
		m.emitLog(task.ID, runIDPtr, "info", "执行 pre_verify 脚本", "")
		verifyErr = m.runDrillScript(context.Background(), sandboxNode, policy.DrillPreVerify)
		if verifyErr != nil {
			m.emitLog(task.ID, runIDPtr, "error", "pre_verify 失败: "+util.SanitizeError(verifyErr), "")
			verifyFailed = true
			failedStep = "pre_verify"
		} else {
			m.emitLog(task.ID, runIDPtr, "info", "pre_verify 成功", "")
		}
	}

	// verify（仅在 pre_verify 成功时执行）
	if !verifyFailed && strings.TrimSpace(policy.DrillVerify) != "" {
		m.emitLog(task.ID, runIDPtr, "info", "执行 verify 脚本", "")
		verifyErr = m.runDrillScript(context.Background(), sandboxNode, policy.DrillVerify)
		if verifyErr != nil {
			m.emitLog(task.ID, runIDPtr, "error", "verify 失败: "+util.SanitizeError(verifyErr), "")
			verifyFailed = true
			failedStep = "verify"
		} else {
			m.emitLog(task.ID, runIDPtr, "info", "verify 成功", "")
		}
	}

	verifyFinishedAt := time.Now()
	if verifyFailed {
		finalError := "pre_verify 执行失败"
		if failedStep == "verify" {
			finalError = "演习校验失败"
		}
		failDrill(failedStep, verifyErr, "drill_verify_failed", finalError)
		drillCompleted = true
		return
	}
	updateEvidence(map[string]interface{}{
		"verify_status":      "success",
		"verify_finished_at": &verifyFinishedAt,
		"verify_error":       "",
	})

	// post_verify（无论成败都执行；失败会使演练不可作为正向可信证据）
	postVerifyStatus := "skipped"
	postVerifyError := ""
	if strings.TrimSpace(policy.DrillPostVerify) != "" {
		m.emitLog(task.ID, runIDPtr, "info", "执行 post_verify 脚本", "")
		err := m.runDrillScript(context.Background(), sandboxNode, policy.DrillPostVerify)
		if err != nil {
			postVerifyStatus = "failed"
			postVerifyError = util.SanitizeError(err)
			m.emitLog(task.ID, runIDPtr, "error", "post_verify 失败: "+postVerifyError, "")
		} else {
			postVerifyStatus = "success"
			m.emitLog(task.ID, runIDPtr, "info", "post_verify 成功", "")
		}
		postVerifyFinishedAt := time.Now()
		updateEvidence(map[string]interface{}{
			"post_verify_status":      postVerifyStatus,
			"post_verify_finished_at": &postVerifyFinishedAt,
			"post_verify_error":       postVerifyError,
		})
	}

	// ---- Step 3: 自动清理 ----
	cleanupStatus := "skipped"
	cleanupError := ""
	var cleanupStartedAt *time.Time
	var cleanupFinishedAt *time.Time
	if policy.DrillAutoCleanup {
		started := time.Now()
		cleanupStartedAt = &started
		cleanupStatus = "running"
		updateEvidence(map[string]interface{}{
			"cleanup_status":     cleanupStatus,
			"cleanup_started_at": cleanupStartedAt,
		})
		m.emitLog(task.ID, runIDPtr, "info", "执行自动清理: "+restorePath, "")
		if err := validateDrillSandboxPath(restorePath); err != nil {
			cleanupStatus = "failed"
			cleanupError = util.SanitizeMessage("清理路径不在演习沙箱安全边界内: " + err.Error())
			m.emitLog(task.ID, runIDPtr, "error", cleanupError, "")
		} else {
			cleanupCmd := fmt.Sprintf("rm -rf %s", executor.ShellEscape(restorePath))
			if err := m.runDrillScript(context.Background(), sandboxNode, cleanupCmd); err != nil {
				cleanupStatus = "failed"
				cleanupError = util.SanitizeError(err)
				m.emitLog(task.ID, runIDPtr, "error", "清理失败: "+cleanupError, "")
			} else {
				cleanupStatus = "success"
				m.emitLog(task.ID, runIDPtr, "info", "清理完成", "")
			}
		}
		finished := time.Now()
		cleanupFinishedAt = &finished
		updateEvidence(map[string]interface{}{
			"cleanup_status":      cleanupStatus,
			"cleanup_finished_at": cleanupFinishedAt,
			"cleanup_error":       cleanupError,
		})
	}

	// ---- Step 4: 记录结果 ----
	finishedAt := time.Now()
	duration := drillDurationMs(drillStartedAt, finishedAt)

	finalStatus := "success"
	finalError := ""
	failedStep = ""
	confidenceEligible := true
	if postVerifyStatus == "failed" {
		finalStatus = "failed"
		finalError = "演习 post_verify 失败: " + postVerifyError
		failedStep = "post_verify"
		confidenceEligible = false
		_ = alerting.RaiseDrillFailure(m.db, policy.ID, policy.Name, sandboxNode.Name, sandboxNode.ID, "drill_post_verify_failed", finalError)
	}
	if cleanupStatus == "failed" {
		finalStatus = "failed"
		finalError = "演习清理失败: " + cleanupError
		failedStep = "cleanup"
		confidenceEligible = false
		_ = alerting.RaiseDrillFailure(m.db, policy.ID, policy.Name, sandboxNode.Name, sandboxNode.ID, "drill_cleanup_failed", finalError)
	}

	finishRun(finalStatus, finalError, finishedAt)
	updateEvidence(map[string]interface{}{
		"status":              finalStatus,
		"failed_step":         failedStep,
		"confidence_eligible": confidenceEligible,
		"finished_at":         &finishedAt,
		"duration_ms":         duration,
	})
	drillCompleted = true

	if finalStatus == "failed" {
		m.dispatchDrillFailure(policy.ID, drillRunID)
	}

	rtoSeconds := float64(duration) / 1000.0
	m.emitLog(task.ID, runIDPtr, "info", fmt.Sprintf(
		"恢复演练完成: status=%s, RTO=%.1fs, 沙箱=%s",
		finalStatus, rtoSeconds, sandboxNode.Name), "")
}

func drillDurationMs(startedAt, finishedAt time.Time) int64 {
	duration := finishedAt.Sub(startedAt).Milliseconds()
	if duration < 0 {
		return 0
	}
	return duration
}

func (m *Manager) latestSuccessfulRunID(taskID uint) (*uint, error) {
	var run model.TaskRun
	err := m.db.Select("id").Where("task_id = ? AND status = ?", taskID, "success").Order("finished_at desc, id desc").First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run.ID, nil
}

func (m *Manager) runDrillScript(ctx context.Context, node model.Node, script string) error {
	if m.drillSSHScriptFunc != nil {
		return m.drillSSHScriptFunc(ctx, node, script)
	}
	return m.runDrillSSHScript(ctx, node, script)
}

func (m *Manager) runDrillRestore(ctx context.Context, srcTask model.Task, sandboxNode model.Node, drillPath string, logf func(string, string)) error {
	if m.drillRestoreFunc != nil {
		return m.drillRestoreFunc(ctx, srcTask, sandboxNode, drillPath, logf)
	}
	return m.restoreBackupToSandbox(ctx, srcTask, sandboxNode, drillPath, logf)
}

// restoreBackupToSandbox 将备份恢复到沙箱节点。
// 当前安全基线禁用旧跨节点传输路径，因此默认实现会在任何远端写操作前失败。
func (m *Manager) restoreBackupToSandbox(ctx context.Context, srcTask model.Task, sandboxNode model.Node, drillPath string, logf func(string, string)) error {
	if err := validateDrillCrossNodeTransferAllowed(srcTask.Node, sandboxNode); err != nil {
		return err
	}

	// Step A: 在源节点上将备份数据恢复到临时路径
	tempPath := fmt.Sprintf("/tmp/xirang-drill-src-%d", time.Now().UnixNano())
	logf("info", fmt.Sprintf("在源节点 (%s) 恢复到临时路径: %s", srcTask.Node.Name, tempPath))

	// 构建恢复任务（源→临时路径）
	restoreTask := srcTask
	restoreTask.RsyncSource = srcTask.RsyncTarget // 备份目的地作为源
	restoreTask.RsyncTarget = tempPath            // 临时恢复路径

	// 同步执行恢复
	if err := m.executeSyncRestore(ctx, restoreTask, logf); err != nil {
		// 清理临时目录
		_ = m.runDrillSSHScript(context.Background(), srcTask.Node, fmt.Sprintf("rm -rf %s", executor.ShellEscape(tempPath)))
		return fmt.Errorf("恢复备份到临时目录失败: %w", err)
	}

	// Step B: 将恢复的数据从源节点传输到沙箱节点
	logf("info", fmt.Sprintf("传输恢复数据到沙箱节点: %s:%s", sandboxNode.Name, drillPath))

	if err := m.transferFilesToSandbox(ctx, srcTask.Node, tempPath, sandboxNode, drillPath, logf); err != nil {
		// 清理两边的临时文件；沙箱路径必须再次通过边界校验后才允许 rm -rf。
		_ = m.runDrillSSHScript(context.Background(), srcTask.Node, fmt.Sprintf("rm -rf %s", executor.ShellEscape(tempPath)))
		if validateErr := validateDrillSandboxPath(drillPath); validateErr == nil {
			_ = m.runDrillSSHScript(context.Background(), sandboxNode, fmt.Sprintf("rm -rf %s", executor.ShellEscape(drillPath)))
		} else {
			logger.Module("task").Warn().Err(validateErr).Msg("跳过恢复演练传输失败后的沙箱清理：路径不在安全边界内")
		}
		return fmt.Errorf("传输文件到沙箱失败: %w", err)
	}

	// 清理源节点临时目录
	_ = m.runDrillSSHScript(context.Background(), srcTask.Node, fmt.Sprintf("rm -rf %s", executor.ShellEscape(tempPath)))

	return nil
}

// executeSyncRestore 在源节点上同步执行备份恢复。
func (m *Manager) executeSyncRestore(ctx context.Context, restoreTask model.Task, logf func(string, string)) error {
	// 确保远程目标路径可用
	if err := executor.EnsureRemoteTargetReady(ctx, restoreTask.Node, restoreTask.RsyncTarget); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("恢复前检查已取消")
		}
		return fmt.Errorf("恢复前检查失败（目标路径）: %w", err)
	}

	logf("info", "开始执行源节点临时恢复")

	// 使用 RunSSHCommandOutput 执行 rsync 恢复（复用 runRemoteRestore 的核心逻辑）
	client, err := executor.DialSSHForNode(ctx, restoreTask.Node)
	if err != nil {
		return fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close() //nolint:errcheck

	rsyncBin := "rsync"
	if executor.NeedsSudo(restoreTask.Node) {
		rsyncBin = "sudo rsync"
	}
	rsyncCmd := fmt.Sprintf("%s -avz --info=progress2 -- %s %s",
		rsyncBin,
		executor.ShellEscape(restoreTask.RsyncSource),
		executor.ShellEscape(restoreTask.RsyncTarget))

	logf("info", "执行 rsync 恢复命令")
	output, err := executor.RunSSHCommandOutput(ctx, client, rsyncCmd)
	if err != nil {
		return fmt.Errorf("恢复执行失败: %s, 输出: %s", err, output)
	}

	return nil
}

// validateDrillCrossNodeTransferAllowed 阻断旧的恢复演练跨节点传输路径。
// 旧实现会把源节点 SSH 私钥写入沙箱文件系统，并在沙箱上关闭主机密钥校验后
// 执行 rsync pull。在安全的凭据代理/中转设计落地前，这里必须在任何远端
// 写操作前失败，避免演练把源节点凭据扩散到沙箱节点。
func validateDrillCrossNodeTransferAllowed(_ model.Node, _ model.Node) error {
	return fmt.Errorf("恢复演练跨节点传输已禁用：当前安全基线禁止将源节点 SSH 凭据写入沙箱节点，请等待后续安全传输实现后再启用该演练")
}

// transferFilesToSandbox 将源节点上的文件传输到沙箱节点。
func (m *Manager) transferFilesToSandbox(_ context.Context, srcNode model.Node, _ string, dstNode model.Node, dstPath string, _ func(string, string)) error {
	if err := validateDrillSandboxPath(dstPath); err != nil {
		return fmt.Errorf("沙箱目标路径不安全: %w", err)
	}
	return validateDrillCrossNodeTransferAllowed(srcNode, dstNode)
}

// runSSHCommandOnNode 在指定节点上通过 SSH 执行命令，返回合并输出。
func (m *Manager) runSSHCommandOnNode(ctx context.Context, node model.Node, command string) (string, error) {
	client, err := executor.DialSSHForNode(ctx, node)
	if err != nil {
		return "", fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close() //nolint:errcheck

	return executor.RunSSHCommandOutput(ctx, client, command)
}

// runDrillSSHScript 在指定节点上通过 SSH 执行一段脚本命令。
func (m *Manager) runDrillSSHScript(ctx context.Context, node model.Node, script string) error {
	client, err := executor.DialSSHForNode(ctx, node)
	if err != nil {
		return fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close() //nolint:errcheck

	output, err := executor.RunSSHCommandOutput(ctx, client, script)
	if err != nil {
		return fmt.Errorf("%s, 输出: %s", err, output)
	}
	return nil
}

// ============================================================================
// Drill Scheduler
// ============================================================================

// StartDrillLoop 启动演习 cron 扫描循环。
func (m *Manager) StartDrillLoop() {
	go m.drillLoop()
	logger.Module("task").Info().Msg("演习调度循环已启动")
}

// drillLoop 定期扫描启用了 drill 的 Policy，匹配 cron 触发演习。
func (m *Manager) drillLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		m.runDrillScan()
	}
}

// runDrillScan 扫描数据库，触发 cron 到期的演习策略。
func (m *Manager) runDrillScan() {
	var policies []model.Policy
	if err := m.db.Where("drill_enabled = ?", true).Find(&policies).Error; err != nil {
		logger.Module("task").Warn().Err(err).Msg("drill scan: 查询策略失败")
		return
	}

	now := time.Now()
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

	for _, p := range policies {
		if strings.TrimSpace(p.DrillCron) == "" {
			continue
		}

		sched, err := parser.Parse(p.DrillCron)
		if err != nil {
			logger.Module("task").Warn().
				Uint("policy_id", p.ID).
				Str("drill_cron", p.DrillCron).
				Err(err).Msg("drill scan: 解析 cron 表达式失败")
			continue
		}

		// 检查 cron 是否在当前 tick 窗口内触发（匹配上次扫描到现在之间的时间点）
		nextRun := sched.Next(now.Add(-61 * time.Second))
		if nextRun.Before(now) || nextRun.Equal(now) {
			logger.Module("task").Info().
				Uint("policy_id", p.ID).
				Str("policy_name", p.Name).
				Msg("触发恢复演练 (cron)")

			if _, err := m.TriggerDrill(p.ID); err != nil {
				logger.Module("task").Warn().
					Uint("policy_id", p.ID).
					Err(err).Msg("触发恢复演练失败")
			}
		}
	}
}
