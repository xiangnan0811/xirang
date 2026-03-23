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

func (e *CommandExecutor) Run(ctx context.Context, task model.Task, logf LogFunc, _ ProgressFunc) (int, error) {
	command := strings.TrimSpace(task.Command)
	if command == "" {
		return -1, fmt.Errorf("命令不能为空")
	}

	node := task.Node
	if strings.TrimSpace(node.Host) == "" {
		return -1, fmt.Errorf("节点地址不能为空")
	}

	port := node.Port
	if port == 0 {
		port = 22
	}
	user := strings.TrimSpace(node.Username)
	if user == "" {
		user = "root"
	}

	// 构建 SSH 认证方式（复用 sshutil 已有逻辑）
	authType := strings.ToLower(strings.TrimSpace(node.AuthType))
	var authMethods []ssh.AuthMethod

	switch authType {
	case "key":
		keyContent, _, err := resolveNodePrivateKey(node)
		if err != nil {
			return -1, err
		}
		if keyContent == "" {
			return -1, fmt.Errorf("密钥认证未配置")
		}
		normalizedKey, _, err := sshutil.ValidateAndPreparePrivateKey(keyContent, sshutil.SSHKeyTypeAuto)
		if err != nil {
			return -1, fmt.Errorf("私钥校验失败")
		}
		signer, err := ssh.ParsePrivateKey([]byte(normalizedKey))
		if err != nil {
			return -1, fmt.Errorf("解析私钥失败: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	case "password":
		if node.Password == "" {
			return -1, fmt.Errorf("密码认证未配置密码")
		}
		authMethods = append(authMethods, ssh.Password(node.Password))
	default:
		return -1, fmt.Errorf("不支持的认证方式: %s", authType)
	}

	// 解析主机密钥校验策略
	hostKeyCallback, err := sshutil.ResolveSSHHostKeyCallback()
	if err != nil {
		return -1, fmt.Errorf("主机密钥配置异常: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", node.Host, port)
	logf("info", fmt.Sprintf("连接节点 %s@%s", user, addr))

	// 使用 sshutil.DialSSH 建立连接（支持 context 取消）
	client, err := sshutil.DialSSH(ctx, addr, user, authMethods, hostKeyCallback)
	if err != nil {
		return -1, err
	}
	defer client.Close()

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
