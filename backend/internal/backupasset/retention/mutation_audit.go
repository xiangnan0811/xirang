package retention

import (
	"context"
	"strings"

	"xirang/backend/internal/backupasset"

	"gorm.io/gorm"
)

type MutationAuditor interface {
	WriteTx(context.Context, *gorm.DB, backupasset.AuditEventInput) error
}

func writeMutationAuditTx(
	ctx context.Context,
	tx *gorm.DB,
	auditor MutationAuditor,
	input backupasset.AuditEventInput,
) error {
	if auditor == nil {
		return nil
	}
	return auditor.WriteTx(ctx, tx, input)
}

func mutationAuditInput(
	ctx context.Context,
	actor backupasset.AuditActor,
	action backupasset.AuditAction,
	repositoryID string,
	recoveryPointID string,
	itemCount int64,
	policyID string,
) backupasset.AuditEventInput {
	status := "success"
	if action == backupasset.AuditActionRepositoryPurge {
		status = "claimed"
	}
	fields := map[backupasset.AuditField]any{
		backupasset.AuditFieldStage:     "request",
		backupasset.AuditFieldStatus:    status,
		backupasset.AuditFieldItemCount: itemCount,
	}
	if correlationID := RequestCorrelationID(ctx); correlationID != "" {
		fields[backupasset.AuditFieldCorrelationID] = correlationID
	}
	if backupasset.ValidateOpaqueID(policyID) == nil {
		fields[backupasset.AuditFieldPolicyID] = policyID
	}
	return backupasset.AuditEventInput{
		Actor:           actor,
		Action:          action,
		Outcome:         backupasset.AuditOutcomeSuccess,
		RepositoryID:    repositoryID,
		RecoveryPointID: recoveryPointID,
		ItemCount:       itemCount,
		Fields:          fields,
	}
}

type mutationContextKey struct{}

func WithRequestCorrelationID(ctx context.Context, requestID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return ctx
	}
	return context.WithValue(ctx, mutationContextKey{}, requestID)
}

func RequestCorrelationID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(mutationContextKey{}).(string)
	return strings.TrimSpace(value)
}
