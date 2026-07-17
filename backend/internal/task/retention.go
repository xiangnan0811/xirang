package task

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/util"
)

func (m *Manager) enforceRetention() {
	log := logger.Module("task")

	var policies []model.Policy
	// GFS 模式策略也纳入保留清理（即使 retention_days=0）
	if err := m.db.Where("(retention_days > 0 OR retention_mode = 'gfs') AND enabled = ?", true).Preload("Nodes").Find(&policies).Error; err != nil {
		log.Error().Err(err).Msg("查询保留策略失败")
		return
	}

	for _, policy := range policies {
		m.enforceRetentionForPolicy(policy)
	}
}

func (m *Manager) enforceRetentionForPolicy(policy model.Policy) {
	log := logger.Module("task")

	var tasks []model.Task
	if err := m.db.Where("policy_id = ? AND source = ?", policy.ID, "policy").
		Preload("Node").Preload("Node.SSHKey").Find(&tasks).Error; err != nil {
		log.Warn().Uint("policy_id", policy.ID).Err(err).Msg("查询策略关联任务失败")
		return
	}

	cutoff := time.Now().AddDate(0, 0, -policy.RetentionDays)

	for _, task := range tasks {
		switch strings.ToLower(task.ExecutorType) {
		case "rsync":
			m.enforceRsyncRetention(policy, task, cutoff)
		case "restic":
			m.enforceResticRetention(policy, task)
		case "rclone":
			m.enforceRcloneRetention(policy, task)
		}
	}
}

// dangerousRoots 禁止执行保留清理的系统根目录
var dangerousRoots = []string{
	"/", "/etc", "/usr", "/bin", "/sbin", "/boot", "/dev", "/proc",
	"/sys", "/lib", "/lib64", "/run", "/var", "/home", "/root", "/tmp",
}

func (m *Manager) enforceRsyncRetention(policy model.Policy, task model.Task, cutoff time.Time) {
	log := logger.Module("task")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if m.lineageGuard != nil {
		session, err := m.lineageGuard.Begin(ctx, task.ID, publication.OperationLegacyRetention)
		if err != nil || session == nil || session.Mode() != publication.LineageCompatibility {
			if session != nil {
				defer func() { _ = session.Close() }()
			}
			m.recordLegacyResticBlock(ctx, task.ID, nil, publication.OperationLegacyRetention)
			log.Warn().Uint("task_id", task.ID).Msg("受管 Rsync 保留清理已被安全边界阻止")
			return
		}
		defer func() { _ = session.Close() }()
	}
	targetPath := strings.TrimSpace(policy.TargetPath)
	if targetPath == "" {
		return
	}

	// 安全检查：拒绝危险的系统根目录
	cleanedTarget := filepath.Clean(targetPath)
	for _, dangerous := range dangerousRoots {
		if cleanedTarget == dangerous {
			log.Warn().Str("path", sanitizeTaskLogMessage(targetPath)).Msg("跳过危险的备份目标路径（系统根目录），不执行保留清理")
			return
		}
	}

	entries, err := os.ReadDir(targetPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Warn().Str("path", sanitizeTaskLogMessage(targetPath)).Str("error", sanitizeTaskRuntimeError(err)).Msg("读取备份目录失败")
		}
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		subdirPath := filepath.Join(targetPath, entry.Name())

		// 安全检查：确保子目录路径在目标路径下
		rel, err := filepath.Rel(targetPath, subdirPath)
		if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			log.Warn().Str("path", sanitizeTaskLogMessage(subdirPath)).Msg("跳过不安全的子目录路径")
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().Before(cutoff) {
			log.Info().Str("path", sanitizeTaskLogMessage(subdirPath)).Time("mtime", info.ModTime()).Int("retention_days", policy.RetentionDays).Msg("清理过期备份目录")
			if err := os.RemoveAll(subdirPath); err != nil {
				errMsg := sanitizeTaskLastError(fmt.Sprintf("清理过期备份目录失败: %s: %v", subdirPath, err))
				log.Error().Str("error", sanitizeTaskRuntimeError(err)).Str("path", sanitizeTaskLogMessage(subdirPath)).Msg("清理过期备份目录失败")
				m.logDispatcher.Dispatch(0, nil, "error", errMsg, "")
				_ = m.alertDispatcher.RaiseRetentionFailure(policy.ID, policy.Name, task.Node.Name, task.NodeID, errMsg)
			} else {
				m.logDispatcher.Dispatch(0, nil, "info", fmt.Sprintf("已清理过期备份 (保留天数: %d)", policy.RetentionDays), "")
			}
		}
	}
}

func (m *Manager) enforceResticRetention(policy model.Policy, task model.Task) {
	log := logger.Module("task")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if m.lineageGuard != nil {
		session, err := m.lineageGuard.Begin(ctx, task.ID, publication.OperationLegacyRetention)
		if err != nil || session == nil || session.Mode() != publication.LineageCompatibility {
			if session != nil {
				defer func() { _ = session.Close() }()
			}
			m.recordLegacyResticBlock(ctx, task.ID, nil, publication.OperationLegacyRetention)
			log.Warn().Uint("task_id", task.ID).Msg("受管 Restic 保留清理已被安全边界阻止")
			return
		}
		defer func() { _ = session.Close() }()
	}

	legacyRetention := m.resticRetentionFunc
	if legacyRetention == nil {
		legacyRetention = m.enforceLegacyResticRetention
	}
	legacyRetention(ctx, policy, task)
}

func (m *Manager) enforceLegacyResticRetention(ctx context.Context, policy model.Policy, task model.Task) {
	log := logger.Module("task")

	client, err := executor.DialSSHForNodePurpose(ctx, task.Node, sshutil.PurposeRetention)
	if err != nil {
		log.Warn().Uint("task_id", task.ID).Err(err).Msg("restic 保留清理: SSH 连接失败")
		return
	}
	defer client.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	repo := strings.TrimSpace(task.RsyncTarget)
	if repo == "" {
		return
	}

	access := executor.ResolveResticRepositoryAccessOrEmpty(task.ExecutorConfig)

	// 生成唯一的密码临时文件路径，并在远程节点上创建
	pwFilePath := executor.BuildResticPasswordFilePath()
	createPwCmd := executor.BuildCreateResticPasswordFileCmd(pwFilePath, access)
	if _, err := executor.RunSSHCommandOutput(ctx, client, createPwCmd); err != nil {
		log.Warn().Uint("task_id", task.ID).Err(err).Msg("restic 保留清理: 创建密码临时文件失败")
		return
	}
	defer func() {
		cleanupCmd := executor.BuildCleanupResticPasswordFileCmd(pwFilePath)
		_, _ = executor.RunSSHCommandOutput(ctx, client, cleanupCmd)
	}()
	pwFileArg := executor.BuildResticPasswordFileArg(pwFilePath)

	resticBin := util.GetEnvOrDefault("RESTIC_BINARY", "restic")
	var keepArgs string
	if policy.RetentionMode == "gfs" {
		keepArgs = buildGFSKeepArgs(policy)
	} else {
		keepArgs = fmt.Sprintf("--keep-within %dd", policy.RetentionDays)
	}
	cmd := fmt.Sprintf("%s %s forget -r %s %s --prune 2>&1",
		pwFileArg, resticBin, shellEscape(repo), keepArgs)

	output, err := executor.RunSSHCommandOutput(ctx, client, cmd)
	if err != nil {
		errMsg := sanitizeTaskLastError(fmt.Sprintf("restic 保留清理失败: %v, 输出: %s", err, output))
		log.Error().Uint("task_id", task.ID).Str("error", sanitizeTaskRuntimeError(err)).Str("output", sanitizeTaskRuntimeOutput(output)).Msg("restic forget 执行失败")
		m.logDispatcher.Dispatch(0, nil, "error", errMsg, "")
		_ = m.alertDispatcher.RaiseRetentionFailure(policy.ID, policy.Name, task.Node.Name, task.NodeID, errMsg)
	} else {
		m.logDispatcher.Dispatch(0, nil, "info", fmt.Sprintf("restic 保留清理完成 (保留: %s)", keepArgs), "")
	}
}

func (m *Manager) enforceRcloneRetention(policy model.Policy, task model.Task) {
	log := logger.Module("task")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if m.lineageGuard != nil {
		session, err := m.lineageGuard.Begin(ctx, task.ID, publication.OperationLegacyRetention)
		if err != nil || session == nil || session.Mode() != publication.LineageCompatibility {
			if session != nil {
				defer func() { _ = session.Close() }()
			}
			m.recordLegacyResticBlock(ctx, task.ID, nil, publication.OperationLegacyRetention)
			log.Warn().Uint("task_id", task.ID).Msg("受管 Rclone 保留清理已被安全边界阻止")
			return
		}
		defer func() { _ = session.Close() }()
	}

	target := strings.TrimSpace(task.RsyncTarget)
	if target == "" {
		return
	}

	client, err := executor.DialSSHForNodePurpose(ctx, task.Node, sshutil.PurposeRetention)
	if err != nil {
		log.Warn().Uint("task_id", task.ID).Err(err).Msg("rclone 保留清理: SSH 连接失败")
		return
	}
	defer client.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	rcloneBin := util.GetEnvOrDefault("RCLONE_BINARY", "rclone")
	minAge := fmt.Sprintf("%dd", policy.RetentionDays)
	cmd := fmt.Sprintf("%s delete %s --min-age %s -v 2>&1", rcloneBin, shellEscape(target), minAge)

	output, err := executor.RunSSHCommandOutput(ctx, client, cmd)
	if err != nil {
		errMsg := sanitizeTaskLastError(fmt.Sprintf("rclone 保留清理失败: %v, 输出: %s", err, output))
		log.Error().Uint("task_id", task.ID).Str("error", sanitizeTaskRuntimeError(err)).Str("output", sanitizeTaskRuntimeOutput(output)).Msg("rclone delete 执行失败")
		m.logDispatcher.Dispatch(0, nil, "error", errMsg, "")
		_ = m.alertDispatcher.RaiseRetentionFailure(policy.ID, policy.Name, task.Node.Name, task.NodeID, errMsg)
	} else {
		m.logDispatcher.Dispatch(0, nil, "info", fmt.Sprintf("rclone 保留清理完成 (最小年龄: %s)", minAge), "")
	}
}

// shellEscape delegates to executor.ShellEscape for consistency.
func shellEscape(s string) string {
	return executor.ShellEscape(s)
}

// buildGFSKeepArgs 根据 Policy 的 GFS 保留设置构建 restic forget 参数。
// GFS 模式下忽略 retention_days，使用 --keep-daily/--keep-weekly/--keep-monthly/--keep-yearly。
// 若 GFS 模式但所有 keep 字段均为 0，回退到 --keep-within 7d（安全兜底）。
func buildGFSKeepArgs(policy model.Policy) string {
	var parts []string
	if policy.KeepDaily > 0 {
		parts = append(parts, fmt.Sprintf("--keep-daily %d", policy.KeepDaily))
	}
	if policy.KeepWeekly > 0 {
		parts = append(parts, fmt.Sprintf("--keep-weekly %d", policy.KeepWeekly))
	}
	if policy.KeepMonthly > 0 {
		parts = append(parts, fmt.Sprintf("--keep-monthly %d", policy.KeepMonthly))
	}
	if policy.KeepYearly > 0 {
		parts = append(parts, fmt.Sprintf("--keep-yearly %d", policy.KeepYearly))
	}
	if len(parts) == 0 {
		// GFS mode but no keep values → fallback to keep-within 7d
		return "--keep-within 7d"
	}
	return strings.Join(parts, " ")
}
