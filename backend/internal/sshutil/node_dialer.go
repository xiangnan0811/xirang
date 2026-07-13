package sshutil

import (
	"context"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"

	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

type DialAuditContext struct {
	Action        string
	CorrelationID string
	UserID        uint
	Username      string
	Role          string
	TaskID        *uint
}

const (
	NodeDialStageAuthBuild = "auth_build"
	NodeDialStageHostKey   = "host_key"
	NodeDialStageDial      = "dial"
)

type NodeDialAttempt struct {
	Credential ResolvedCredential
	Stage      string
	LatencyMS  int64
}

type NodeDialer struct {
	db              *gorm.DB
	buildAuth       func(model.Node, *gorm.DB, string) ([]ssh.AuthMethod, ResolvedCredential, error)
	hostKeyResolver func() (ssh.HostKeyCallback, error)
	dial            func(context.Context, string, string, []ssh.AuthMethod, ssh.HostKeyCallback) (*ssh.Client, error)
	now             func() time.Time
}

func NewNodeDialer(db *gorm.DB) *NodeDialer {
	return &NodeDialer{
		db:              db,
		buildAuth:       BuildSSHAuthForPurpose,
		hostKeyResolver: ResolveSSHHostKeyCallback,
		dial:            DialSSH,
		now:             time.Now,
	}
}

func (dialer *NodeDialer) Dial(ctx context.Context, node model.Node, purpose string, auditContext DialAuditContext) (*ssh.Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if dialer == nil || dialer.buildAuth == nil || dialer.hostKeyResolver == nil || dialer.dial == nil || dialer.now == nil {
		return nil, fmt.Errorf("repository SSH dialer unavailable")
	}
	purpose = NormalizePurpose(purpose)
	action, ok := repositoryCredentialAction(purpose)
	if !ok {
		return nil, fmt.Errorf("repository SSH purpose unavailable")
	}
	if strings.TrimSpace(auditContext.Action) != "" && auditContext.Action != action {
		return nil, fmt.Errorf("repository SSH audit action mismatch")
	}
	auditContext.Action = action

	client, attempt, err := dialer.DialAttempt(ctx, node, purpose)
	if err != nil {
		outcome := credentialaudit.OutcomeFailure
		if attempt.Stage == NodeDialStageAuthBuild {
			outcome = credentialaudit.OutcomeBlocked
		}
		dialer.writeAudit(auditContext, node.ID, purpose, attempt.Credential, outcome, attempt.Stage, attempt.LatencyMS)
		switch attempt.Stage {
		case NodeDialStageAuthBuild:
			return nil, fmt.Errorf("repository SSH authentication unavailable")
		case NodeDialStageHostKey:
			return nil, fmt.Errorf("repository SSH host verification unavailable")
		default:
			if ctx.Err() != nil {
				return nil, fmt.Errorf("repository SSH dial canceled: %w", ctx.Err())
			}
			return nil, fmt.Errorf("repository SSH dial unavailable")
		}
	}
	dialer.writeAudit(auditContext, node.ID, purpose, attempt.Credential, credentialaudit.OutcomeSuccess, attempt.Stage, attempt.LatencyMS)
	return client, nil
}

func (dialer *NodeDialer) DialAttempt(ctx context.Context, node model.Node, purpose string) (*ssh.Client, NodeDialAttempt, error) {
	attempt := NodeDialAttempt{Stage: NodeDialStageAuthBuild}
	if ctx == nil {
		ctx = context.Background()
	}
	if dialer == nil || dialer.buildAuth == nil || dialer.hostKeyResolver == nil || dialer.dial == nil || dialer.now == nil {
		return nil, attempt, fmt.Errorf("SSH dialer unavailable")
	}
	purpose = NormalizePurpose(purpose)
	resolvedNode := node
	resolvedNode.AuthType = strings.ToLower(strings.TrimSpace(node.AuthType))
	authMethods, credential, err := dialer.buildAuth(resolvedNode, dialer.db, purpose)
	attempt.Credential = credential
	if err != nil {
		return nil, attempt, err
	}
	attempt.Stage = NodeDialStageHostKey
	hostKeyCallback, err := dialer.hostKeyResolver()
	if err != nil {
		return nil, attempt, err
	}
	port := node.Port
	if port == 0 {
		port = 22
	}
	user := strings.TrimSpace(node.Username)
	if user == "" {
		user = "root"
	}
	attempt.Stage = NodeDialStageDial
	startedAt := dialer.now()
	client, err := dialer.dial(ctx, fmt.Sprintf("%s:%d", node.Host, port), user, authMethods, hostKeyCallback)
	attempt.LatencyMS = dialer.now().Sub(startedAt).Milliseconds()
	if attempt.LatencyMS < 0 {
		attempt.LatencyMS = 0
	}
	if err != nil {
		return nil, attempt, err
	}
	return client, attempt, nil
}

func (dialer *NodeDialer) writeAudit(auditContext DialAuditContext, nodeID uint, purpose string, credential ResolvedCredential, outcome, stage string, latency int64) {
	metadata := map[string]any{"stage": stage}
	if auditContext.CorrelationID != "" {
		metadata["correlation_id"] = auditContext.CorrelationID
	}
	if latency > 0 {
		metadata["latency_ms"] = latency
	}
	event := credentialaudit.Event{
		UserID:           auditContext.UserID,
		Username:         auditContext.Username,
		Role:             auditContext.Role,
		Action:           auditContext.Action,
		Purpose:          purpose,
		CredentialKind:   credential.Kind,
		CredentialSource: credential.Source,
		SSHKeyID:         credential.KeyID,
		NodeID:           credentialaudit.PtrUint(nodeID),
		TaskID:           auditContext.TaskID,
		Outcome:          outcome,
		Metadata:         metadata,
	}
	if event.CredentialKind == "" {
		event.CredentialKind = "unknown"
	}
	if event.CredentialSource == "" {
		event.CredentialSource = "unknown"
	}
	if outcome != credentialaudit.OutcomeSuccess {
		event.ErrorMessage = stage + " failed"
	}
	if err := credentialaudit.Write(dialer.db, event); err != nil {
		logger.Module("credential_audit").Warn().Str("action", auditContext.Action).Str("stage", stage).Msg("仓库凭据审计事件写入失败")
	}
}

func repositoryCredentialAction(purpose string) (string, bool) {
	switch purpose {
	case PurposeRepositoryProbe:
		return "repository.probe", true
	case PurposeRepositoryList:
		return "repository.list", true
	case PurposeRepositoryRead:
		return "repository.read", true
	default:
		return "", false
	}
}
