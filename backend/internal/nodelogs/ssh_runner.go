package nodelogs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

// sshRunner is the production Runner. It dials the node each call.
type sshRunner struct {
	db *gorm.DB
}

func NewSSHRunner(db *gorm.DB) Runner { return &sshRunner{db: db} }

func (r *sshRunner) Run(ctx context.Context, node model.Node, cmd string, timeout time.Duration, maxBytes int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	auth, credential, err := sshutil.BuildSSHAuthForPurpose(node, r.db, sshutil.PurposeNodeLogs)
	if err != nil {
		r.writeCredentialAudit(node, credential, credentialaudit.OutcomeBlocked, "auth_build", err, maxBytes)
		return "", fmt.Errorf("build auth: %w", err)
	}
	hostKey, err := sshutil.ResolveSSHHostKeyCallback()
	if err != nil {
		r.writeCredentialAudit(node, credential, credentialaudit.OutcomeFailure, "host_key", err, maxBytes)
		return "", fmt.Errorf("host key: %w", err)
	}
	addr := fmt.Sprintf("%s:%d", node.Host, node.Port)
	client, err := sshutil.DialSSH(ctx, addr, node.Username, auth, hostKey)
	if err != nil {
		r.writeCredentialAudit(node, credential, credentialaudit.OutcomeFailure, "dial", err, maxBytes)
		return "", fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = client.Close() }()

	session, err := client.NewSession()
	if err != nil {
		r.writeCredentialAudit(node, credential, credentialaudit.OutcomeFailure, "session", err, maxBytes)
		return "", fmt.Errorf("session: %w", err)
	}
	defer func() { _ = session.Close() }()

	stdout, err := session.StdoutPipe()
	if err != nil {
		r.writeCredentialAudit(node, credential, credentialaudit.OutcomeFailure, "stdout", err, maxBytes)
		return "", fmt.Errorf("stdout: %w", err)
	}
	if err := session.Start(cmd); err != nil {
		r.writeCredentialAudit(node, credential, credentialaudit.OutcomeFailure, "start", err, maxBytes)
		return "", fmt.Errorf("start: %w", err)
	}

	limited := io.LimitReader(stdout, int64(maxBytes))
	buf, err := io.ReadAll(limited)
	if err != nil {
		r.writeCredentialAudit(node, credential, credentialaudit.OutcomeFailure, "read", err, maxBytes)
		return "", fmt.Errorf("read: %w", err)
	}
	if err := session.Wait(); err != nil {
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			// Clean remote exit with non-zero status; output is still usable
			// (some shells / tail return nonzero even when stdout is complete).
			return string(buf), nil
		}
		// Missing exit status / transport error → session broke mid-stream.
		r.writeCredentialAudit(node, credential, credentialaudit.OutcomeFailure, "wait", err, maxBytes)
		return string(buf), fmt.Errorf("wait: %w", err)
	}
	return string(buf), nil
}

func (r *sshRunner) writeCredentialAudit(node model.Node, credential sshutil.ResolvedCredential, outcome, stage string, err error, maxBytes int) {
	kind, source, keyID := nodelogCredentialFallback(node, credential)
	event := credentialaudit.Event{
		Username:         "system",
		Role:             "system",
		Action:           "node_logs.collect",
		Purpose:          sshutil.PurposeNodeLogs,
		CredentialKind:   kind,
		CredentialSource: source,
		SSHKeyID:         keyID,
		NodeID:           credentialaudit.PtrUint(node.ID),
		Outcome:          outcome,
		Metadata: map[string]any{
			"stage":     stage,
			"max_bytes": maxBytes,
		},
	}
	if err != nil {
		event.ErrorMessage = strings.TrimSpace(stage) + " failed"
	}
	if writeErr := credentialaudit.Write(r.db, event); writeErr != nil {
		logger.Module("credential_audit").Warn().Err(writeErr).
			Str("action", event.Action).
			Str("purpose", event.Purpose).
			Msg("系统凭据审计事件写入失败")
	}
}

func nodelogCredentialFallback(node model.Node, credential sshutil.ResolvedCredential) (string, string, *uint) {
	kind := strings.TrimSpace(credential.Kind)
	source := strings.TrimSpace(credential.Source)
	keyID := credential.KeyID
	if kind == "" {
		switch strings.ToLower(strings.TrimSpace(node.AuthType)) {
		case "password":
			kind = "password"
		case "key":
			if node.SSHKeyID != nil && *node.SSHKeyID != 0 {
				kind = "ssh_key"
				keyID = node.SSHKeyID
			} else if strings.TrimSpace(node.PrivateKey) != "" {
				kind = "node_private_key"
			}
		}
	}
	if kind == "" {
		kind = "unknown"
	}
	if source == "" {
		switch kind {
		case "password":
			source = "node.password"
		case "ssh_key":
			if keyID != nil && *keyID != 0 {
				source = fmt.Sprintf("ssh_key_id=%d", *keyID)
			}
		case "node_private_key":
			source = "node.private_key"
		}
	}
	if source == "" {
		source = "unknown"
	}
	return kind, source, keyID
}
