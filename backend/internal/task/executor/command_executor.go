package executor

import (
	"context"
	"fmt"
	"strings"

	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"golang.org/x/crypto/ssh"
)

// CommandExecutor 通过 SSH 在远程节点上执行命令。
type CommandExecutor struct{}

// CommandExitError reports a remote command that completed with a known,
// positive, non-signal exit status.
type CommandExitError struct {
	ExitCode int
	Err      error
}

func (e *CommandExitError) Error() string {
	if e == nil {
		return "command exited with a known non-zero status"
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("command exited with status %d", e.ExitCode)
}

func (e *CommandExitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *CommandExecutor) Run(ctx context.Context, task model.Task, logf LogFunc, _ ProgressFunc) (int, error) {
	command := strings.TrimSpace(task.Command)
	if command == "" {
		return -1, markNoProcessStart(fmt.Errorf("命令不能为空"))
	}

	node := task.Node
	if strings.TrimSpace(node.Host) == "" {
		return -1, markNoProcessStart(fmt.Errorf("节点地址不能为空"))
	}

	purpose := sshutil.PurposeTaskCommand
	if strings.TrimSpace(task.Source) == "batch" {
		purpose = sshutil.PurposeBatchCommand
	}

	// 使用共享 SSH 连接逻辑
	client, err := DialSSHForNodePurpose(ctx, task.Node, purpose)
	if err != nil {
		return -1, markNoProcessStart(err)
	}
	defer client.Close() //nolint:errcheck

	logf("info", "连接节点")

	session, err := client.NewSession()
	if err != nil {
		return -1, markNoProcessStart(fmt.Errorf("创建 SSH 会话失败: %w", err))
	}
	defer session.Close() //nolint:errcheck

	// 设置标准输出和标准错误管道
	stdout, err := session.StdoutPipe()
	if err != nil {
		return -1, markNoProcessStart(err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return -1, markNoProcessStart(err)
	}

	// sudo 包裹（用户命令可能含管道/&&，需要 sh -c 包裹）
	if NeedsSudo(task.Node) {
		command = WrapWithSudoShell(command)
	}

	// 启动命令
	logf("info", "执行命令")
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
						logf("info", sanitizeExecutorRuntimeEvidence(line))
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
						logf("error", sanitizeExecutorRuntimeEvidence(line))
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
		if exitErr, ok := waitErr.(*ssh.ExitError); ok &&
			exitErr.Signal() == "" && exitErr.ExitStatus() > 0 {
			exitCode := exitErr.ExitStatus()
			return exitCode, &CommandExitError{ExitCode: exitCode, Err: waitErr}
		}
		return -1, waitErr
	}
	return 0, nil
}
