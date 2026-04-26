package task

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/util"
)

func (m *Manager) enforceRetention() {
	log := logger.Module("task")

	var policies []model.Policy
	if err := m.db.Where("retention_days > 0 AND enabled = ?", true).Preload("Nodes").Find(&policies).Error; err != nil {
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
	targetPath := strings.TrimSpace(policy.TargetPath)
	if targetPath == "" {
		return
	}

	// 安全检查：拒绝危险的系统根目录
	cleanedTarget := filepath.Clean(targetPath)
	for _, dangerous := range dangerousRoots {
		if cleanedTarget == dangerous {
			log.Warn().Str("path", targetPath).Msg("跳过危险的备份目标路径（系统根目录），不执行保留清理")
			return
		}
	}

	entries, err := os.ReadDir(targetPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Warn().Str("path", targetPath).Err(err).Msg("读取备份目录失败")
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
			log.Warn().Str("path", subdirPath).Msg("跳过不安全的子目录路径")
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().Before(cutoff) {
			log.Info().Str("path", subdirPath).Time("mtime", info.ModTime()).Int("retention_days", policy.RetentionDays).Msg("清理过期备份目录")
			if err := os.RemoveAll(subdirPath); err != nil {
				errMsg := fmt.Sprintf("清理过期备份目录失败: %s: %v", subdirPath, err)
				log.Error().Err(err).Str("path", subdirPath).Msg("清理过期备份目录失败")
				m.emitLog(0, nil, "error", errMsg, "")
				_ = alerting.RaiseRetentionFailure(m.db, policy.ID, policy.Name, task.Node.Name, task.NodeID, errMsg)
			} else {
				m.emitLog(0, nil, "info", fmt.Sprintf("已清理过期备份: %s (策略: %s, 保留天数: %d)", subdirPath, policy.Name, policy.RetentionDays), "")
			}
		}
	}
}

func (m *Manager) enforceResticRetention(policy model.Policy, task model.Task) {
	log := logger.Module("task")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	client, err := executor.DialSSHForNode(ctx, task.Node)
	if err != nil {
		log.Warn().Uint("task_id", task.ID).Err(err).Msg("restic 保留清理: SSH 连接失败")
		return
	}
	defer client.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	repo := strings.TrimSpace(task.RsyncTarget)
	if repo == "" {
		return
	}

	// 解析 restic 配置获取密码
	password := ""
	if strings.TrimSpace(task.ExecutorConfig) != "" {
		// 简单解析 repository_password
		cfg := task.ExecutorConfig
		if strings.Contains(cfg, "repository_password") {
			// 使用 executor 包内的辅助方法不可行（unexported），直接构造环境变量前缀
			password = extractResticPassword(cfg)
		}
	}

	envPrefix := "RESTIC_PASSWORD=''"
	if password != "" {
		envPrefix = fmt.Sprintf("RESTIC_PASSWORD=%s", shellEscape(password))
	}

	resticBin := util.GetEnvOrDefault("RESTIC_BINARY", "restic")
	keepWithin := fmt.Sprintf("%dd", policy.RetentionDays)
	cmd := fmt.Sprintf("%s %s forget -r %s --keep-within %s --prune 2>&1",
		envPrefix, resticBin, shellEscape(repo), keepWithin)

	output, err := executor.RunSSHCommandOutput(ctx, client, cmd)
	if err != nil {
		errMsg := fmt.Sprintf("restic 保留清理失败 (策略: %s, 仓库: %s): %v", policy.Name, repo, err)
		log.Error().Uint("task_id", task.ID).Err(err).Str("output", output).Msg("restic forget 执行失败")
		m.emitLog(0, nil, "error", errMsg, "")
		_ = alerting.RaiseRetentionFailure(m.db, policy.ID, policy.Name, task.Node.Name, task.NodeID, errMsg)
	} else {
		m.emitLog(0, nil, "info", fmt.Sprintf("restic 保留清理完成 (策略: %s, 仓库: %s, 保留: %s)", policy.Name, repo, keepWithin), "")
	}
}

func (m *Manager) enforceRcloneRetention(policy model.Policy, task model.Task) {
	log := logger.Module("task")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	client, err := executor.DialSSHForNode(ctx, task.Node)
	if err != nil {
		log.Warn().Uint("task_id", task.ID).Err(err).Msg("rclone 保留清理: SSH 连接失败")
		return
	}
	defer client.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	target := strings.TrimSpace(task.RsyncTarget)
	if target == "" {
		return
	}

	rcloneBin := util.GetEnvOrDefault("RCLONE_BINARY", "rclone")
	minAge := fmt.Sprintf("%dd", policy.RetentionDays)
	cmd := fmt.Sprintf("%s delete %s --min-age %s -v 2>&1", rcloneBin, shellEscape(target), minAge)

	output, err := executor.RunSSHCommandOutput(ctx, client, cmd)
	if err != nil {
		errMsg := fmt.Sprintf("rclone 保留清理失败 (策略: %s, 目标: %s): %v", policy.Name, target, err)
		log.Error().Uint("task_id", task.ID).Err(err).Str("output", output).Msg("rclone delete 执行失败")
		m.emitLog(0, nil, "error", errMsg, "")
		_ = alerting.RaiseRetentionFailure(m.db, policy.ID, policy.Name, task.Node.Name, task.NodeID, errMsg)
	} else {
		m.emitLog(0, nil, "info", fmt.Sprintf("rclone 保留清理完成 (策略: %s, 目标: %s, 最小年龄: %s)", policy.Name, target, minAge), "")
	}
}

// shellEscape delegates to executor.ShellEscape for consistency.
func shellEscape(s string) string {
	return executor.ShellEscape(s)
}

// extractResticPassword 从 ExecutorConfig JSON 中提取 repository_password 字段
func extractResticPassword(configJSON string) string {
	var cfg struct {
		RepositoryPassword string `json:"repository_password"`
	}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return ""
	}
	return cfg.RepositoryPassword
}
