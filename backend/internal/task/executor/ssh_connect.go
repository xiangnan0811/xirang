package executor

import (
	"context"
	"fmt"
	"strings"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"golang.org/x/crypto/ssh"
)

// DialSSHForNode 为节点建立 SSH 连接（节点的 SSHKey 应已通过 Preload 加载）。
func DialSSHForNode(ctx context.Context, node model.Node) (*ssh.Client, error) {
	port := node.Port
	if port == 0 {
		port = 22
	}
	user := strings.TrimSpace(node.Username)
	if user == "" {
		user = "root"
		logger.Module("executor").Warn().Str("node", node.Name).
			Msg("节点未配置 SSH 用户名，默认使用 root")
	}

	authMethods, err := resolveSSHAuthMethods(node)
	if err != nil {
		return nil, err
	}

	hostKeyCallback, err := sshutil.ResolveSSHHostKeyCallback()
	if err != nil {
		return nil, fmt.Errorf("主机密钥配置异常: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", node.Host, port)
	return sshutil.DialSSH(ctx, addr, user, authMethods, hostKeyCallback)
}

// resolveSSHAuthMethods 根据节点认证类型解析 SSH 认证方法。
func resolveSSHAuthMethods(node model.Node) ([]ssh.AuthMethod, error) {
	authType := strings.ToLower(strings.TrimSpace(node.AuthType))
	var authMethods []ssh.AuthMethod

	switch authType {
	case "key":
		keyContent, _, err := resolveNodePrivateKey(node)
		if err != nil {
			return nil, err
		}
		if keyContent == "" {
			return nil, fmt.Errorf("密钥认证未配置")
		}
		normalizedKey, _, err := sshutil.ValidateAndPreparePrivateKey(keyContent, sshutil.SSHKeyTypeAuto)
		if err != nil {
			return nil, fmt.Errorf("私钥校验失败")
		}
		signer, err := ssh.ParsePrivateKey([]byte(normalizedKey))
		if err != nil {
			return nil, fmt.Errorf("解析私钥失败: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	case "password":
		if node.Password == "" {
			return nil, fmt.Errorf("密码认证未配置密码")
		}
		authMethods = append(authMethods, ssh.Password(node.Password))
	default:
		return nil, fmt.Errorf("不支持的认证方式: %s", authType)
	}
	return authMethods, nil
}

// ResolveSSHUser 返回节点的 SSH 用户名，空值时回退到 "root" 并记录警告。
// 用于不走 DialSSHForNode 的场景（如本地 rsync -e ssh）。
func ResolveSSHUser(node model.Node) string {
	user := strings.TrimSpace(node.Username)
	if user == "" {
		user = "root"
		logger.Module("executor").Warn().Str("node", node.Name).
			Msg("节点未配置 SSH 用户名，默认使用 root")
	}
	return user
}

// RunSSHCommandOutput 通过 SSH 执行命令并返回合并的 stdout+stderr 输出。
func RunSSHCommandOutput(ctx context.Context, client *ssh.Client, cmd string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("创建 SSH 会话失败: %w", err)
	}
	defer session.Close()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			session.Close()
		case <-done:
		}
	}()

	out, err := session.CombinedOutput(cmd)
	if ctx.Err() != nil {
		return string(out), ctx.Err()
	}
	return string(out), err
}
