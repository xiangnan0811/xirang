package task

import (
	"context"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/util"
)

// checkIntegrity runs periodic integrity checks on restic/rclone backup repositories.
func (m *Manager) checkIntegrity() {
	log := logger.Module("task")

	var policies []model.Policy
	if err := m.db.Where("enabled = ?", true).Preload("Nodes").Find(&policies).Error; err != nil {
		log.Error().Err(err).Msg("查询策略失败（完整性检查）")
		return
	}

	for _, policy := range policies {
		m.checkIntegrityForPolicy(policy)
	}
}

func (m *Manager) checkIntegrityForPolicy(policy model.Policy) {
	log := logger.Module("task")

	var tasks []model.Task
	if err := m.db.Where("policy_id = ? AND source = ?", policy.ID, "policy").
		Preload("Node").Preload("Node.SSHKey").Find(&tasks).Error; err != nil {
		log.Warn().Uint("policy_id", policy.ID).Err(err).Msg("查询策略关联任务失败（完整性检查）")
		return
	}

	for _, task := range tasks {
		switch strings.ToLower(task.ExecutorType) {
		case "restic":
			m.checkResticIntegrity(policy, task)
		case "rclone":
			m.checkRcloneIntegrity(policy, task)
		}
	}
}

func (m *Manager) checkResticIntegrity(policy model.Policy, task model.Task) {
	log := logger.Module("task")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	client, err := executor.DialSSHForNodePurpose(ctx, task.Node, sshutil.PurposeIntegrityCheck)
	if err != nil {
		log.Warn().Uint("task_id", task.ID).Err(err).Msg("restic 完整性检查: SSH 连接失败")
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
		log.Warn().Uint("task_id", task.ID).Err(err).Msg("restic 完整性检查: 创建密码临时文件失败")
		return
	}
	defer func() {
		cleanupCmd := executor.BuildCleanupResticPasswordFileCmd(pwFilePath)
		_, _ = executor.RunSSHCommandOutput(ctx, client, cleanupCmd)
	}()
	pwFileArg := executor.BuildResticPasswordFileArg(pwFilePath)

	resticBin := util.GetEnvOrDefault("RESTIC_BINARY", "restic")
	cmd := fmt.Sprintf("%s %s check -r %s --json 2>&1",
		pwFileArg, resticBin, shellEscape(repo))

	output, err := executor.RunSSHCommandOutput(ctx, client, cmd)
	if err != nil {
		errMsg := sanitizeTaskLastError(fmt.Sprintf("restic 完整性检查失败: %v, 输出: %s", err, output))
		log.Error().Uint("task_id", task.ID).Str("error", sanitizeTaskRuntimeError(err)).Str("output", sanitizeTaskRuntimeOutput(output)).Msg("restic check 执行失败")
		m.emitLog(0, nil, "error", errMsg, "")
		_ = m.alertDispatcher.RaiseIntegrityCheckFailure(policy.ID, policy.Name, task.Node.Name, task.NodeID, errMsg)
	} else {
		msg := "restic 完整性检查通过"
		log.Info().Uint("task_id", task.ID).Msg("restic 完整性检查通过")
		m.emitLog(0, nil, "info", msg, "")
	}
}

func (m *Manager) checkRcloneIntegrity(policy model.Policy, task model.Task) {
	log := logger.Module("task")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	client, err := executor.DialSSHForNodePurpose(ctx, task.Node, sshutil.PurposeIntegrityCheck)
	if err != nil {
		log.Warn().Uint("task_id", task.ID).Err(err).Msg("rclone 完整性检查: SSH 连接失败")
		return
	}
	defer client.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	source := strings.TrimSpace(task.RsyncSource)
	target := strings.TrimSpace(task.RsyncTarget)
	if source == "" || target == "" {
		return
	}

	rcloneBin := util.GetEnvOrDefault("RCLONE_BINARY", "rclone")
	cmd := fmt.Sprintf("%s check %s %s --one-way 2>&1",
		rcloneBin, shellEscape(source), shellEscape(target))

	output, err := executor.RunSSHCommandOutput(ctx, client, cmd)
	if err != nil {
		errMsg := sanitizeTaskLastError(fmt.Sprintf("rclone 完整性检查失败: %v, 输出: %s", err, output))
		log.Error().Uint("task_id", task.ID).Str("error", sanitizeTaskRuntimeError(err)).Str("output", sanitizeTaskRuntimeOutput(output)).Msg("rclone check 执行失败")
		m.emitLog(0, nil, "error", errMsg, "")
		_ = m.alertDispatcher.RaiseIntegrityCheckFailure(policy.ID, policy.Name, task.Node.Name, task.NodeID, errMsg)
	} else {
		msg := "rclone 完整性检查通过"
		log.Info().Uint("task_id", task.ID).Msg("rclone 完整性检查通过")
		m.emitLog(0, nil, "info", msg, "")
	}
}
