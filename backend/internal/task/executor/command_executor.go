package executor

import (
	"context"
	"fmt"
	"strings"

	"xirang/backend/internal/model"

	"golang.org/x/crypto/ssh"
)

// CommandExecutor 通过 SSH 在远程节点上执行命令。
type CommandExecutor struct{}

func (e *CommandExecutor) Run(ctx context.Context, task model.Task, logf LogFunc, _ ProgressFunc) (int, error) {
	command := strings.TrimSpace(task.Command)
	if command == "" {
		return -1, fmt.Errorf("命令不能为空")
	}

	node := task.Node
	if strings.TrimSpace(node.Host) == "" {
		return -1, fmt.Errorf("节点地址不能为空")
	}

	// 使用共享 SSH 连接逻辑
	client, err := DialSSHForNode(ctx, task.Node)
	if err != nil {
		return -1, err
	}
	defer client.Close()

	user := ResolveSSHUser(task.Node)
	addr := fmt.Sprintf("%s:%d", task.Node.Host, task.Node.Port)
	if task.Node.Port == 0 {
		addr = fmt.Sprintf("%s:22", task.Node.Host)
	}
	logf("info", fmt.Sprintf("连接节点 %s@%s", user, addr))

	session, err := client.NewSession()
	if err != nil {
		return -1, fmt.Errorf("创建 SSH 会话失败: %w", err)
	}
	defer session.Close()

	// 设置标准输出和标准错误管道
	stdout, err := session.StdoutPipe()
	if err != nil {
		return -1, err
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return -1, err
	}

	// sudo 包裹（用户命令可能含管道/&&，需要 sh -c 包裹）
	if NeedsSudo(task.Node) {
		command = WrapWithSudoShell(command)
	}

	// 启动命令
	logf("info", fmt.Sprintf("执行命令: %s", command))
	if err := session.Start(command); err != nil {
		return -1, fmt.Errorf("启动命令失败: %w", err)
	}

	// 异步读取输出流
	done := make(chan struct{})
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 4096)
		for {
			n, readErr := stdout.Read(buf)
			if n > 0 {
				for _, line := range strings.Split(strings.TrimRight(string(buf[:n]), "\n"), "\n") {
					if strings.TrimSpace(line) != "" {
						logf("info", line)
					}
				}
			}
			if readErr != nil {
				return
			}
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 4096)
		for {
			n, readErr := stderr.Read(buf)
			if n > 0 {
				for _, line := range strings.Split(strings.TrimRight(string(buf[:n]), "\n"), "\n") {
					if strings.TrimSpace(line) != "" {
						logf("error", line)
					}
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	// 等待输出读取完成
	<-done
	<-done

	// 等待命令结束，处理 context 取消
	waitErr := session.Wait()
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	if waitErr != nil {
		if exitErr, ok := waitErr.(*ssh.ExitError); ok {
			return exitErr.ExitStatus(), waitErr
		}
		return -1, waitErr
	}
	return 0, nil
}
