package executor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"golang.org/x/crypto/ssh"
)

// DialSSHForNode 为节点建立 SSH 连接（节点的 SSHKey 应已通过 Preload 加载）。
func DialSSHForNode(ctx context.Context, node model.Node) (*ssh.Client, error) {
	return DialSSHForNodePurpose(ctx, node, "")
}

// DialSSHForNodePurpose 为节点建立 SSH 连接，并对托管 SSHKey 执行用途/节点/标签范围校验。
func DialSSHForNodePurpose(ctx context.Context, node model.Node, purpose string) (*ssh.Client, error) {
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

	authMethods, credential, err := resolveSSHAuthMethodsForPurpose(node, purpose)
	if err != nil {
		writeRuntimeCredentialAudit(ctx, node, purpose, credential, credentialaudit.OutcomeBlocked, "auth_build", err, 0)
		return nil, err
	}

	hostKeyCallback, err := sshutil.ResolveSSHHostKeyCallback()
	if err != nil {
		wrapped := fmt.Errorf("主机密钥配置异常: %w", err)
		writeRuntimeCredentialAudit(ctx, node, purpose, credential, credentialaudit.OutcomeFailure, "host_key", wrapped, 0)
		return nil, wrapped
	}

	addr := fmt.Sprintf("%s:%d", node.Host, port)
	startedAt := time.Now()
	client, err := sshutil.DialSSH(ctx, addr, user, authMethods, hostKeyCallback)
	latencyMs := time.Since(startedAt).Milliseconds()
	if err != nil {
		writeRuntimeCredentialAudit(ctx, node, purpose, credential, credentialaudit.OutcomeFailure, "dial", err, latencyMs)
		return nil, err
	}
	writeRuntimeCredentialAudit(ctx, node, purpose, credential, credentialaudit.OutcomeSuccess, "dial", nil, latencyMs)
	return client, nil
}

// resolveSSHAuthMethods 根据节点认证类型解析 SSH 认证方法。
func resolveSSHAuthMethods(node model.Node) ([]ssh.AuthMethod, error) {
	authMethods, _, err := resolveSSHAuthMethodsForPurpose(node, "")
	return authMethods, err
}

func resolveSSHAuthMethodsForPurpose(node model.Node, purpose string) ([]ssh.AuthMethod, sshutil.ResolvedCredential, error) {
	authType := strings.ToLower(strings.TrimSpace(node.AuthType))
	var authMethods []ssh.AuthMethod

	switch authType {
	case "key":
		keyContent, _, credential, err := resolveNodePrivateKeyForPurpose(node, purpose)
		if err != nil {
			return nil, credential, err
		}
		if keyContent == "" {
			return nil, credential, fmt.Errorf("密钥认证未配置")
		}
		normalizedKey, _, err := sshutil.ValidateAndPreparePrivateKey(keyContent, sshutil.SSHKeyTypeAuto)
		if err != nil {
			return nil, credential, fmt.Errorf("私钥校验失败")
		}
		signer, err := ssh.ParsePrivateKey([]byte(normalizedKey))
		if err != nil {
			return nil, credential, fmt.Errorf("解析私钥失败: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
		return authMethods, credential, nil
	case "password":
		credential := sshutil.ResolvedCredential{Kind: "password", Source: "node.password"}
		if node.Password == "" {
			return nil, credential, fmt.Errorf("密码认证未配置密码")
		}
		authMethods = append(authMethods, ssh.Password(node.Password))
		return authMethods, credential, nil
	default:
		return nil, sshutil.ResolvedCredential{}, fmt.Errorf("不支持的认证方式: %s", authType)
	}
}

func writeRuntimeCredentialAudit(ctx context.Context, node model.Node, purpose string, credential sshutil.ResolvedCredential, outcome string, stage string, err error, latencyMs int64) {
	if _, ok := credentialaudit.RuntimeEvent(ctx); !ok {
		return
	}
	metadata := map[string]any{
		"stage":     stage,
		"auth_type": strings.ToLower(strings.TrimSpace(node.AuthType)),
	}
	if latencyMs > 0 {
		metadata["latency_ms"] = latencyMs
	}
	event := credentialaudit.Event{
		Action:           "task.credential.use",
		Purpose:          sshutil.NormalizePurpose(purpose),
		CredentialKind:   credential.Kind,
		CredentialSource: credential.Source,
		SSHKeyID:         credential.KeyID,
		NodeID:           credentialaudit.PtrUint(node.ID),
		Outcome:          outcome,
		Metadata:         metadata,
	}
	if strings.TrimSpace(event.Purpose) == "" {
		event.Purpose = "ssh"
	}
	if event.CredentialKind == "" {
		event.CredentialKind = "unknown"
	}
	if event.CredentialSource == "" {
		event.CredentialSource = "unknown"
	}
	if err != nil {
		event.ErrorMessage = err.Error()
	}
	if writeErr := credentialaudit.WriteRuntime(ctx, event); writeErr != nil {
		logger.Module("credential_audit").Warn().Err(writeErr).
			Str("action", event.Action).
			Str("purpose", event.Purpose).
			Msg("运行时凭据审计事件写入失败")
	}
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
	defer session.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			session.Close() //nolint:errcheck // best-effort cancel in goroutine
		case <-done:
		}
	}()

	out, err := session.CombinedOutput(cmd)
	if ctx.Err() != nil {
		return string(out), ctx.Err()
	}
	return string(out), err
}
