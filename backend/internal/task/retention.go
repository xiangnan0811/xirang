package task

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/util"
)

// ManagedRecoveryPointRetentionRequest is the private, exact handoff from the
// Task compatibility loop to the managed RecoveryPoint lifecycle owner. It
// deliberately excludes paths, locators, credentials, and executor policy.
type ManagedRecoveryPointRetentionRequest struct {
	TaskID           uint
	PolicyID         uint
	RepositoryID     string
	RecoveryPointIDs []string
}

// ManagedRecoveryPointRetention owns retention after the lineage guard proves
// that a Task has managed immutable RecoveryPoints.
type ManagedRecoveryPointRetention interface {
	EnforceManagedRetention(context.Context, ManagedRecoveryPointRetentionRequest) error
}

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

	for _, task := range tasks {
		switch strings.ToLower(task.ExecutorType) {
		case "rsync":
			m.enforceRsyncRetention(policy, task)
		case "restic":
			m.enforceResticRetention(policy, task)
		case "rclone":
			m.enforceRcloneRetention(policy, task)
		}
	}
}

func (m *Manager) enforceRsyncRetention(policy model.Policy, task model.Task) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	session, legacy := m.beginRetentionAuthority(ctx, policy, task)
	if !legacy {
		return
	}
	if session != nil {
		defer func() { _ = session.Close() }()
	}
	m.rejectLegacyMutableRetention(ctx, task)
}

func (m *Manager) enforceResticRetention(policy model.Policy, task model.Task) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	session, legacy := m.beginRetentionAuthority(ctx, policy, task)
	if !legacy {
		return
	}
	if session != nil {
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	session, legacy := m.beginRetentionAuthority(ctx, policy, task)
	if !legacy {
		return
	}
	if session != nil {
		defer func() { _ = session.Close() }()
	}
	m.rejectLegacyMutableRetention(ctx, task)
}

func (m *Manager) beginRetentionAuthority(ctx context.Context, policy model.Policy, task model.Task) (publication.LineageSession, bool) {
	if m.lineageGuard == nil {
		return nil, true
	}
	session, err := m.lineageGuard.Begin(ctx, task.ID, publication.OperationLegacyRetention)
	if err != nil || session == nil {
		if session != nil {
			_ = session.Close()
		}
		m.blockLegacyRetention(ctx, task.ID)
		return nil, false
	}
	switch session.Mode() {
	case publication.LineageCompatibility:
		return session, true
	case publication.LineageExact:
		defer func() { _ = session.Close() }()
		if err := m.delegateManagedRetention(ctx, policy, task, session); err != nil {
			m.blockLegacyRetention(ctx, task.ID)
		}
		return nil, false
	default:
		_ = session.Close()
		m.blockLegacyRetention(ctx, task.ID)
		return nil, false
	}
}

func (m *Manager) delegateManagedRetention(ctx context.Context, policy model.Policy, task model.Task, session publication.LineageSession) error {
	if m.managedRetention == nil || task.ID == 0 || policy.ID == 0 || session == nil {
		return fmt.Errorf("managed RecoveryPoint retention authority is unavailable")
	}
	repositoryID := session.RepositoryID()
	if backupasset.ValidateOpaqueID(repositoryID) != nil {
		return fmt.Errorf("managed RecoveryPoint retention authority is unavailable")
	}
	points := session.CommittedPoints()
	pointIDs := make([]string, 0, len(points))
	for _, point := range points {
		if backupasset.ValidateOpaqueID(point.RecoveryPointID) != nil {
			return fmt.Errorf("managed RecoveryPoint retention identity is invalid")
		}
		pointIDs = append(pointIDs, point.RecoveryPointID)
	}
	sort.Strings(pointIDs)
	for index := 1; index < len(pointIDs); index++ {
		if pointIDs[index] == pointIDs[index-1] {
			return fmt.Errorf("managed RecoveryPoint retention identity is duplicated")
		}
	}
	if len(pointIDs) == 0 {
		return nil
	}
	return m.managedRetention.EnforceManagedRetention(ctx, ManagedRecoveryPointRetentionRequest{
		TaskID:           task.ID,
		PolicyID:         policy.ID,
		RepositoryID:     repositoryID,
		RecoveryPointIDs: pointIDs,
	})
}

func (m *Manager) blockLegacyRetention(ctx context.Context, taskID uint) {
	m.recordLegacyResticBlock(ctx, taskID, nil, publication.OperationLegacyRetention)
	logger.Module("task").Warn().Uint("task_id", taskID).Msg("保留清理已被安全边界阻止")
}

func (m *Manager) rejectLegacyMutableRetention(ctx context.Context, task model.Task) {
	const reason = "旧版可变备份树没有版本年龄证据，已拒绝破坏性保留清理"
	m.blockLegacyRetention(ctx, task.ID)
	if m.logDispatcher != nil {
		m.logDispatcher.Dispatch(task.ID, nil, "warn", reason, task.Status)
	}
	logger.Module("task").Warn().Uint("task_id", task.ID).Msg(reason)
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
