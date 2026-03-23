package executor

import (
	"strings"

	"xirang/backend/internal/model"
)

// NeedsSudo 判断节点是否需要通过 sudo 执行命令。
// 仅当 UseSudo=true 且连接用户非 root 时启用。
func NeedsSudo(node model.Node) bool {
	user := strings.TrimSpace(node.Username)
	if user == "" || user == "root" {
		return false
	}
	return node.UseSudo
}

// WrapWithSudo 为系统生成的单条命令添加 sudo 前缀（rsync/restic/rclone 等）。
func WrapWithSudo(command string) string {
	return "sudo " + command
}

// WrapWithSudoShell 为用户编写的命令添加 sudo 包裹（支持管道、&& 等复合命令）。
// 使用 sh -c 确保整条命令在 root 上下文中执行。
func WrapWithSudoShell(command string) string {
	return "sudo sh -c " + ShellEscape(command)
}
