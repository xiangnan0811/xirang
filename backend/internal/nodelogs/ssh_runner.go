package nodelogs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"gorm.io/gorm"
)

// ErrOutputLimit means the complete collection exceeded its byte budget.
var ErrOutputLimit = sshutil.ErrCommandOutputLimit

// sshRunner is the production Runner. It dials the node each call.
type sshRunner struct {
	db *gorm.DB
}

func NewSSHRunner(db *gorm.DB) Runner { return &sshRunner{db: db} }

func (r *sshRunner) Run(ctx context.Context, node model.Node, cmd string, timeout time.Duration, maxBytes int) (string, error) {
	if timeout <= 0 || maxBytes <= 0 {
		return "", errors.New("invalid collection bounds")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	db := r.db
	if db != nil {
		db = db.WithContext(ctx)
	}

	auth, credential, err := sshutil.BuildSSHAuthForPurpose(node, db, sshutil.PurposeNodeLogs)
	if err != nil {
		r.writeCredentialAudit(ctx, node, credential, credentialaudit.OutcomeBlocked, "auth_build", err, maxBytes)
		return "", collectionError(ctx, "auth_build", err)
	}
	hostKey, err := sshutil.ResolveSSHHostKeyCallback()
	if err != nil {
		r.writeCredentialAudit(ctx, node, credential, credentialaudit.OutcomeFailure, "host_key", err, maxBytes)
		return "", collectionError(ctx, "host_key", err)
	}
	addr := net.JoinHostPort(node.Host, strconv.Itoa(node.Port))
	client, err := sshutil.DialSSH(ctx, addr, node.Username, auth, hostKey)
	if err != nil {
		r.writeCredentialAudit(ctx, node, credential, credentialaudit.OutcomeFailure, "dial", err, maxBytes)
		return "", collectionError(ctx, "dial", err)
	}
	defer func() { _ = client.Close() }()

	output, err := collectSSHOutput(ctx, sshutil.NewSSHCommandRunnerWithJoinedTransportClose(client, 1), cmd, maxBytes)
	if err != nil {
		r.writeCredentialAudit(ctx, node, credential, credentialaudit.OutcomeFailure, "execution", err, maxBytes)
	}
	return output, err
}

func collectSSHOutput(ctx context.Context, runner *sshutil.CommandRunner, cmd string, maxBytes int) (string, error) {
	stream, err := runner.OpenRawExecution(ctx, sshutil.RawCommandSpec{Command: cmd, MaxStdoutBytes: int64(maxBytes)})
	if err != nil {
		return "", collectionError(ctx, "execution", err)
	}
	output, readErr := io.ReadAll(stream)
	completion, joinErr := stream.Join()
	if readErr != nil || joinErr != nil {
		return "", collectionError(ctx, "execution", errors.Join(readErr, joinErr))
	}
	if !completion.ExitCodeKnown {
		return "", collectionError(ctx, "execution", sshutil.ErrCommandFailed)
	}
	// Explicit nonzero remote exits retain complete-output compatibility.
	return string(output), nil
}

func collectionError(ctx context.Context, stage string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("collection %s: %w", stage, ctx.Err())
	}
	if errors.Is(err, ErrOutputLimit) {
		return ErrOutputLimit
	}
	// Do not expose remote command, output, host, or credential material.
	return fmt.Errorf("collection %s failed", stage)
}

func (r *sshRunner) writeCredentialAudit(ctx context.Context, node model.Node, credential sshutil.ResolvedCredential, outcome, stage string, err error, maxBytes int) {
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
	db := r.db
	if db != nil {
		db = db.WithContext(ctx)
	}
	if writeErr := credentialaudit.Write(db, event); writeErr != nil {
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
