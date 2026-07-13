package repository

import (
	"context"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
)

type FoundationAuditWriter interface {
	Write(context.Context, backupasset.AuditEventInput) (model.BackupAssetAuditEvent, error)
}

type foundationAuditSink struct{ writer FoundationAuditWriter }

func NewAssetAuditSink(writer FoundationAuditWriter) AssetAuditSink {
	if writer == nil {
		return nil
	}
	return &foundationAuditSink{writer: writer}
}

func (sink *foundationAuditSink) Write(ctx context.Context, input backupasset.AuditEventInput) error {
	_, err := sink.writer.Write(ctx, input)
	return err
}

type RequestContext struct {
	Actor         backupasset.AuditActor
	CorrelationID string
}

func (service *Service) writeAudit(ctx context.Context, requestContext RequestContext, action backupasset.AuditAction, outcome backupasset.AuditOutcome, repositoryID string, taskID *uint, stage string, err error) {
	if service == nil || service.audit == nil {
		return
	}
	input := backupasset.AuditEventInput{
		Actor: requestContext.Actor, Action: action, Outcome: outcome, RepositoryID: repositoryID, TaskID: taskID,
		Fields: map[backupasset.AuditField]any{backupasset.AuditFieldStage: stage, backupasset.AuditFieldCorrelationID: requestContext.CorrelationID},
	}
	if err != nil {
		input.FailureCode = "operation_failed"
	}
	if auditErr := service.audit.Write(ctx, input); auditErr != nil {
		logger.Module("backup_repository").Warn().Str("action", string(action)).Str("stage", stage).Msg("备份仓库资产审计写入失败")
	}
}
