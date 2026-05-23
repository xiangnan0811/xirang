package task

import (
	"context"
	"fmt"

	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/task/executor"
)

// runSSHHook 通过 SSH 在节点上执行钩子命令。
func (m *Manager) runSSHHook(ctx context.Context, task model.Task, command string) error {
	if executor.NeedsSudo(task.Node) {
		command = executor.WrapWithSudoShell(command)
	}
	client, err := executor.DialSSHForNodePurpose(ctx, task.Node, sshutil.PurposeTaskHook)
	if err != nil {
		return fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	_, err = executor.RunSSHCommandOutput(ctx, client, command)
	if err != nil {
		return fmt.Errorf("钩子执行失败: %s", sanitizeTaskLastError(err.Error()))
	}
	return nil
}
