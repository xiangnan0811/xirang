package task

import (
	"context"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"

	"github.com/robfig/cron/v3"
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
	if err := validateRestorePath(restorePath); err != nil {
		return fmt.Errorf("演习恢复路径无效: %w", err)
	}

	if policy.DrillTargetNodeID == nil || *policy.DrillTargetNodeID == 0 {
		return fmt.Errorf("未配置沙箱节点")
	}

	return nil
}

// getNodePrivateKeyContent 获取节点的私钥内容（解密后）。
func getNodePrivateKeyContent(node model.Node) string {
	if node.SSHKey != nil {
		if key := strings.TrimSpace(node.SSHKey.PrivateKey); key != "" {
			return key
		}
	}
	return strings.TrimSpace(node.PrivateKey)
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
		m.db.Model(&model.TaskRun{}).
			Where("task_id = ? AND status = ?", t.ID, "success").
			Count(&count)
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

	// 确保无论成败都清理状态
	drillCompleted := false
	defer func() {
		if !drillCompleted {
			finishedAt := time.Now()
			duration := finishedAt.Sub(drillStartedAt).Milliseconds()
			m.db.Model(&model.TaskRun{}).Where("id = ?", drillRunID).Updates(map[string]interface{}{
				"status":      "failed",
				"finished_at": &finishedAt,
				"duration_ms": duration,
				"last_error":  "演习执行异常终止",
			})
		}
	}()

	// 更新 drill TaskRun 为 running
	now := time.Now()
	m.db.Model(&model.TaskRun{}).Where("id = ?", drillRunID).Updates(map[string]interface{}{
		"status":     "running",
		"started_at": &now,
	})
	drillStartedAt = now

	m.emitLog(task.ID, runIDPtr, "info", fmt.Sprintf(
		"开始恢复演练: 策略=%s, 沙箱=%s(%s), 恢复路径=%s",
		policy.Name, sandboxNode.Name, sandboxNode.Host, policy.DrillRestorePath), "")

	restorePath := strings.TrimSpace(policy.DrillRestorePath)
	if restorePath == "" {
		restorePath = "/tmp/xirang-drill"
	}

	// ---- Step 0: 沙箱节点连通性预检 ----
	m.emitLog(task.ID, runIDPtr, "info", "正在检查沙箱节点连通性...", "")
	if err := m.runDrillSSHScript(context.Background(), sandboxNode, "true"); err != nil {
		errorMsg := fmt.Sprintf("沙箱节点 (%s) 不可达: %v", sandboxNode.Name, err)
		finishedAt := time.Now()
		duration := finishedAt.Sub(drillStartedAt).Milliseconds()
		m.db.Model(&model.TaskRun{}).Where("id = ?", drillRunID).Updates(map[string]interface{}{
			"status":      "failed",
			"finished_at": &finishedAt,
			"duration_ms": duration,
			"last_error":  errorMsg,
		})
		drillCompleted = true
		m.emitLog(task.ID, runIDPtr, "error", errorMsg, "")
		// 触发沙箱不可达告警
		_ = alerting.RaiseDrillFailure(m.db, policy.ID, policy.Name, sandboxNode.Name, sandboxNode.ID,
			"drill_sandbox_unreachable", errorMsg)
		m.dispatchDrillFailure(policy.ID, drillRunID)
		return
	}
	m.emitLog(task.ID, runIDPtr, "info", "沙箱节点连通性检查通过", "")

	// ---- Step 1: 恢复备份到沙箱 ----
	m.emitLog(task.ID, runIDPtr, "info", "正在恢复备份到沙箱节点...", "")
	restoreErr := m.restoreBackupToSandbox(context.Background(), task, sandboxNode, restorePath, func(level, msg string) {
		if runIDPtr != nil {
			m.emitLog(task.ID, runIDPtr, level, msg, "")
		}
	})

	if restoreErr != nil {
		errorMsg := fmt.Sprintf("恢复备份到沙箱失败: %v", restoreErr)
		finishedAt := time.Now()
		duration := finishedAt.Sub(drillStartedAt).Milliseconds()
		m.db.Model(&model.TaskRun{}).Where("id = ?", drillRunID).Updates(map[string]interface{}{
			"status":      "failed",
			"finished_at": &finishedAt,
			"duration_ms": duration,
			"last_error":  errorMsg,
		})
		drillCompleted = true
		m.emitLog(task.ID, runIDPtr, "error", errorMsg, "")
		// 触发恢复失败告警
		_ = alerting.RaiseDrillFailure(m.db, policy.ID, policy.Name, sandboxNode.Name, sandboxNode.ID,
			"drill_restore_failed", errorMsg)
		m.dispatchDrillFailure(policy.ID, drillRunID)
		return
	}
	m.emitLog(task.ID, runIDPtr, "info", "备份恢复至沙箱完成", "")

	// ---- Step 2: 执行校验脚本 ----
	verifyFailed := false
	var verifyErr error

	// pre_verify
	if !verifyFailed && strings.TrimSpace(policy.DrillPreVerify) != "" {
		m.emitLog(task.ID, runIDPtr, "info", "执行 pre_verify: "+policy.DrillPreVerify, "")
		err := m.runDrillSSHScript(context.Background(), sandboxNode, policy.DrillPreVerify)
		if err != nil {
			m.emitLog(task.ID, runIDPtr, "error", "pre_verify 失败: "+err.Error(), "")
			verifyFailed = true
		} else {
			m.emitLog(task.ID, runIDPtr, "info", "pre_verify 成功", "")
		}
	}

	// verify（仅在 pre_verify 成功时执行）
	if !verifyFailed && strings.TrimSpace(policy.DrillVerify) != "" {
		m.emitLog(task.ID, runIDPtr, "info", "执行 verify: "+policy.DrillVerify, "")
		verifyErr = m.runDrillSSHScript(context.Background(), sandboxNode, policy.DrillVerify)
		if verifyErr != nil {
			m.emitLog(task.ID, runIDPtr, "error", "verify 失败: "+verifyErr.Error(), "")
			verifyFailed = true
		} else {
			m.emitLog(task.ID, runIDPtr, "info", "verify 成功", "")
		}
	}

	// post_verify（无论成败都执行）
	if strings.TrimSpace(policy.DrillPostVerify) != "" {
		m.emitLog(task.ID, runIDPtr, "info", "执行 post_verify: "+policy.DrillPostVerify, "")
		err := m.runDrillSSHScript(context.Background(), sandboxNode, policy.DrillPostVerify)
		if err != nil {
			m.emitLog(task.ID, runIDPtr, "warn", "post_verify 失败（不影响演习结果）: "+err.Error(), "")
		} else {
			m.emitLog(task.ID, runIDPtr, "info", "post_verify 成功", "")
		}
	}

	// ---- Step 3: 自动清理 ----
	if policy.DrillAutoCleanup {
		m.emitLog(task.ID, runIDPtr, "info", "执行自动清理: "+restorePath, "")
		cleanupCmd := fmt.Sprintf("rm -rf %s", executor.ShellEscape(restorePath))
		// 安全加固：确保路径不为空且是绝对路径
		if strings.HasPrefix(restorePath, "/") && len(restorePath) > 1 && !strings.Contains(restorePath, "..") {
			if err := m.runDrillSSHScript(context.Background(), sandboxNode, cleanupCmd); err != nil {
				m.emitLog(task.ID, runIDPtr, "warn", "清理失败（不影响演习结果）: "+err.Error(), "")
			} else {
				m.emitLog(task.ID, runIDPtr, "info", "清理完成", "")
			}
		}
	}

	// ---- Step 4: 记录结果 ----
	finishedAt := time.Now()
	duration := finishedAt.Sub(drillStartedAt).Milliseconds()

	var finalStatus string
	var finalError string
	if verifyFailed {
		finalStatus = "failed"
		if verifyErr != nil {
			finalError = fmt.Sprintf("演习校验失败: %v", verifyErr)
		} else {
			finalError = "pre_verify 执行失败"
		}
		// 触发校验失败告警
		_ = alerting.RaiseDrillFailure(m.db, policy.ID, policy.Name, sandboxNode.Name, sandboxNode.ID,
			"drill_verify_failed", finalError)
	} else {
		finalStatus = "success"
	}

	m.db.Model(&model.TaskRun{}).Where("id = ?", drillRunID).Updates(map[string]interface{}{
		"status":      finalStatus,
		"finished_at": &finishedAt,
		"duration_ms": duration,
		"last_error":  finalError,
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

// restoreBackupToSandbox 将备份恢复到沙箱节点。
// 流程：先从源节点恢复备份到临时目录，再将数据同步到沙箱。
func (m *Manager) restoreBackupToSandbox(ctx context.Context, srcTask model.Task, sandboxNode model.Node, drillPath string, logf func(string, string)) error {
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
	logf("info", fmt.Sprintf("传输恢复数据: %s -> %s@%s:%s", tempPath, sandboxNode.Username, sandboxNode.Host, drillPath))

	if err := m.transferFilesToSandbox(ctx, srcTask.Node, tempPath, sandboxNode, drillPath, logf); err != nil {
		// 清理两边的临时文件
		_ = m.runDrillSSHScript(context.Background(), srcTask.Node, fmt.Sprintf("rm -rf %s", executor.ShellEscape(tempPath)))
		_ = m.runDrillSSHScript(context.Background(), sandboxNode, fmt.Sprintf("rm -rf %s", executor.ShellEscape(drillPath)))
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

	logf("info", fmt.Sprintf("恢复: %s → %s", restoreTask.RsyncSource, restoreTask.RsyncTarget))

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

	logf("info", "执行: "+rsyncCmd)
	output, err := executor.RunSSHCommandOutput(ctx, client, rsyncCmd)
	if err != nil {
		return fmt.Errorf("恢复执行失败: %s, 输出: %s", err, output)
	}

	return nil
}

// transferFilesToSandbox 将源节点上的文件传输到沙箱节点。
// 使用 tar + SSH 管道通过 Xirang 服务器中转。
func (m *Manager) transferFilesToSandbox(ctx context.Context, srcNode model.Node, srcPath string, dstNode model.Node, dstPath string, logf func(string, string)) error {
	// 1. 在沙箱节点上创建目标目录
	ensureDirCmd := fmt.Sprintf("mkdir -p %s", executor.ShellEscape(dstPath))
	_, err := m.runSSHCommandOnNode(ctx, dstNode, ensureDirCmd)
	if err != nil {
		return fmt.Errorf("创建沙箱目标目录失败: %w", err)
	}

	// 2. 用 rsync over SSH 从源传输到沙箱
	// 策略：将源节点私钥写入沙箱节点临时文件，执行 rsync pull，完成后删除。
	srcKey := getNodePrivateKeyContent(srcNode)
	if srcKey == "" {
		return fmt.Errorf("源节点未配置 SSH 私钥，无法进行跨节点传输")
	}

	keySuffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tmpKeyPath := fmt.Sprintf("/tmp/xirang-drill-key-%s.pem", keySuffix)

	// 写入临时密钥到沙箱节点
	writeKeyCmd := fmt.Sprintf("cat > %s << 'XIRANG_KEY_EOF'\n%s\nXIRANG_KEY_EOF\nchmod 600 %s",
		tmpKeyPath, srcKey, tmpKeyPath)
	if _, err := m.runSSHCommandOnNode(ctx, dstNode, writeKeyCmd); err != nil {
		return fmt.Errorf("写入临时密钥到沙箱失败: %w", err)
	}

	// 确保清理临时密钥
	defer func() {
		_, _ = m.runSSHCommandOnNode(context.Background(), dstNode, fmt.Sprintf("rm -f %s", executor.ShellEscape(tmpKeyPath)))
	}()

	// 从沙箱执行 rsync pull
	srcUser := executor.ResolveSSHUser(srcNode)
	rsyncCmd := fmt.Sprintf(
		"rsync -avz --info=progress2 -e \"ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p %d\" -- %s@%s:%s/ %s/",
		tmpKeyPath,
		srcNode.Port,
		srcUser,
		srcNode.Host,
		executor.ShellEscape(strings.TrimRight(srcPath, "/")),
		executor.ShellEscape(dstPath),
	)

	logf("info", "在沙箱执行: "+rsyncCmd)
	output, err := m.runSSHCommandOnNode(ctx, dstNode, rsyncCmd)
	if err != nil {
		return fmt.Errorf("文件传输失败: %s, 输出: %s", err, output)
	}

	return nil
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
