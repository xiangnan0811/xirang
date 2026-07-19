package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrContentAuditUnavailable = errors.New("content audit unavailable")
	ErrContentAuditMismatch    = errors.New("content audit idempotency mismatch")
	ErrContentAuditBacklogFull = errors.New("content audit backlog full")
)

type FoundationContentAuditWriter interface {
	Write(context.Context, backupasset.AuditEventInput) (model.BackupAssetAuditEvent, error)
}

type ContentAuditDependencies struct {
	DB         *gorm.DB
	Writer     FoundationContentAuditWriter
	Now        func() time.Time
	BacklogMax int64
	Metrics    Metrics
}

type ReadAuditSummary struct {
	GrantID string
	Outcome backupasset.AuditOutcome
	Bytes   int64
	Range   bool
}

type ContentAuditService struct {
	mu         sync.Mutex
	db         *gorm.DB
	writer     FoundationContentAuditWriter
	now        func() time.Time
	backlogMax int64
	metrics    Metrics
}

func NewContentAuditService(dependencies ContentAuditDependencies) (*ContentAuditService, error) {
	if dependencies.DB == nil || dependencies.Writer == nil || dependencies.Now == nil || dependencies.BacklogMax <= 0 {
		return nil, ErrContentAuditUnavailable
	}
	if dependencies.Metrics == nil {
		dependencies.Metrics = NoopMetrics{}
	}
	return &ContentAuditService{
		db: dependencies.DB, writer: dependencies.Writer, now: dependencies.Now,
		backlogMax: dependencies.BacklogMax, metrics: dependencies.Metrics,
	}, nil
}

func (service *ContentAuditService) Write(ctx context.Context, input backupasset.AuditEventInput) error {
	if service == nil || service.writer == nil || service.db == nil {
		return ErrContentAuditUnavailable
	}
	ctx = nonNilContext(ctx)
	if _, err := service.writer.Write(ctx, input); err == nil {
		return nil
	}
	if input.GrantID == "" || input.Fingerprints.Path != "" || input.Fingerprints.Query != "" {
		return ErrContentAuditUnavailable
	}
	var existing model.BackupAssetAuditEvent
	if err := service.db.WithContext(ctx).Where("grant_id = ? AND action = ?", input.GrantID, input.Action).Take(&existing).Error; err != nil {
		return ErrContentAuditUnavailable
	}
	match, err := exactAuditProjectionMatches(input, existing)
	if err != nil {
		return ErrContentAuditUnavailable
	}
	if !match {
		return ErrContentAuditMismatch
	}
	return nil
}

func (service *ContentAuditService) RecordRead(ctx context.Context, summary ReadAuditSummary) error {
	if service == nil || backupasset.ValidateOpaqueID(summary.GrantID) != nil || summary.Bytes < 0 ||
		(summary.Outcome != backupasset.AuditOutcomeSuccess && summary.Outcome != backupasset.AuditOutcomeBlocked &&
			summary.Outcome != backupasset.AuditOutcomeFailure) {
		return ErrContentAuditUnavailable
	}
	rangeCount, rangeBytes := int64(0), int64(0)
	if summary.Range {
		rangeCount, rangeBytes = 1, summary.Bytes
	}
	success, blocked, failure := int64(0), int64(0), int64(0)
	switch summary.Outcome {
	case backupasset.AuditOutcomeSuccess:
		success = 1
	case backupasset.AuditOutcomeBlocked:
		blocked = 1
	case backupasset.AuditOutcomeFailure:
		failure = 1
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	result := service.db.WithContext(nonNilContext(ctx)).Model(&model.BackupAssetDeliveryGrant{}).
		Where("id = ?", summary.GrantID).Updates(map[string]any{
		"audit_state":         "pending",
		"audit_request_count": boundedAuditAdd("audit_request_count", 1, backupasset.MaxAuditRangeCount),
		"audit_range_count":   boundedAuditAdd("audit_range_count", rangeCount, backupasset.MaxAuditRangeCount),
		"audit_range_bytes":   boundedAuditAdd("audit_range_bytes", rangeBytes, backupasset.MaxAuditRangeBytes),
		"audit_success_count": boundedAuditAdd("audit_success_count", success, backupasset.MaxAuditRangeCount),
		"audit_blocked_count": boundedAuditAdd("audit_blocked_count", blocked, backupasset.MaxAuditRangeCount),
		"audit_failure_count": boundedAuditAdd("audit_failure_count", failure, backupasset.MaxAuditRangeCount),
		"updated_at":          service.now().UTC(),
		"version":             gorm.Expr("version + 1"),
	})
	if result.Error != nil || result.RowsAffected != 1 {
		return fmt.Errorf("%w: aggregate read: %v rows=%d", ErrContentAuditUnavailable, result.Error, result.RowsAffected)
	}
	service.observeBacklog(ctx)
	return nil
}

func (service *ContentAuditService) FlushGrant(ctx context.Context, grantID string) error {
	if service == nil || backupasset.ValidateOpaqueID(grantID) != nil {
		return ErrContentAuditUnavailable
	}
	ctx = nonNilContext(ctx)
	var grant model.BackupAssetDeliveryGrant
	if err := service.db.WithContext(ctx).Where("id = ?", grantID).Take(&grant).Error; err != nil {
		return ErrContentAuditUnavailable
	}
	if grant.AuditState == "none" || grant.AuditState == "emitted" {
		return nil
	}
	input := aggregateAuditInput(grant)
	if err := service.Write(ctx, input); err != nil {
		service.queueAuditRetry(ctx, grant)
		return err
	}
	result := service.db.WithContext(ctx).Model(&model.BackupAssetDeliveryGrant{}).
		Where("id = ? AND audit_state IN ?", grant.ID, []string{"pending", "retry_wait", "failed"}).
		Updates(map[string]any{
			"audit_state": "emitted", "audit_failure_code": "", "audit_next_attempt_at": nil,
			"updated_at": service.now().UTC(), "version": gorm.Expr("version + 1"),
		})
	if result.Error != nil || result.RowsAffected != 1 {
		return ErrContentAuditUnavailable
	}
	service.observeBacklog(ctx)
	return nil
}

func (service *ContentAuditService) BacklogAvailable(ctx context.Context) error {
	if service == nil || service.db == nil {
		return ErrContentAuditUnavailable
	}
	var count int64
	if err := service.db.WithContext(nonNilContext(ctx)).Model(&model.BackupAssetDeliveryGrant{}).
		Where("audit_state IN ?", []string{"pending", "retry_wait", "failed"}).Count(&count).Error; err != nil {
		return ErrContentAuditUnavailable
	}
	service.metrics.SetAuditBacklog(int(min(count, int64(maxInt()))))
	if count >= service.backlogMax {
		return ErrContentAuditBacklogFull
	}
	return nil
}

func (service *ContentAuditService) queueAuditRetry(ctx context.Context, grant model.BackupAssetDeliveryGrant) {
	service.metrics.ObserveAuditRetry()
	attempt := grant.AuditAttemptCount + 1
	shift := min(attempt-1, int64(6))
	backoff := time.Duration(1<<shift) * time.Second
	next := service.now().UTC().Add(backoff)
	_ = service.db.WithContext(ctx).Model(&model.BackupAssetDeliveryGrant{}).Where("id = ?", grant.ID).
		Updates(map[string]any{
			"audit_state": "retry_wait", "audit_failure_code": "audit_write_failed",
			"audit_attempt_count": attempt, "audit_next_attempt_at": next,
			"updated_at": service.now().UTC(), "version": gorm.Expr("version + 1"),
		}).Error
	service.observeBacklog(ctx)
}

func (service *ContentAuditService) observeBacklog(ctx context.Context) {
	if service == nil || service.db == nil || service.metrics == nil {
		return
	}
	var count int64
	if err := service.db.WithContext(nonNilContext(ctx)).Model(&model.BackupAssetDeliveryGrant{}).
		Where("audit_state IN ?", []string{"pending", "retry_wait", "failed"}).Count(&count).Error; err == nil {
		service.metrics.SetAuditBacklog(int(min(count, int64(maxInt()))))
	}
}

func exactAuditProjectionMatches(input backupasset.AuditEventInput, existing model.BackupAssetAuditEvent) (bool, error) {
	prepared, err := backupasset.NewAuditEvent(input)
	if err != nil {
		return false, err
	}
	fieldsJSON, err := json.Marshal(prepared.Fields)
	if err != nil {
		return false, err
	}
	return existing.ActorUserID == prepared.Actor.UserID && existing.ActorUsername == prepared.Actor.Username &&
		existing.ActorRole == prepared.Actor.Role && existing.Action == string(prepared.Action) &&
		existing.Outcome == string(prepared.Outcome) && existing.RepositoryID == prepared.RepositoryID &&
		existing.RecoveryPointID == prepared.RecoveryPointID && existing.EntryID == prepared.EntryID &&
		reflect.DeepEqual(existing.TaskID, prepared.TaskID) && reflect.DeepEqual(existing.TaskRunID, prepared.TaskRunID) &&
		existing.RecoveryJobID == prepared.RecoveryJobID && existing.ExportJobID == prepared.ExportJobID &&
		existing.ItemCount == prepared.ItemCount && existing.ByteCount == prepared.ByteCount &&
		existing.RangeCount == prepared.Range.Count && existing.RangeBytes == prepared.Range.Bytes &&
		existing.StepUpAction == prepared.StepUpAction && existing.StepUpProofID == prepared.StepUpProofID &&
		existing.GrantID == prepared.GrantID && existing.FailureCode == prepared.FailureCode &&
		existing.FieldsJSON == string(fieldsJSON), nil
}

func aggregateAuditInput(grant model.BackupAssetDeliveryGrant) backupasset.AuditEventInput {
	action := backupasset.AuditActionPreviewRead
	if grant.Action == string(DeliveryDownload) {
		action = backupasset.AuditActionAssetDownload
	}
	outcome := backupasset.AuditOutcomeSuccess
	failureCode := ""
	if grant.AuditFailureCount > 0 {
		outcome = backupasset.AuditOutcomeFailure
		failureCode = "request_failed"
	} else if grant.AuditBlockedCount > 0 {
		outcome = backupasset.AuditOutcomeBlocked
	}
	stepUpAction, stepUpProofID := "", ""
	if grant.StepUpAction != nil {
		stepUpAction = *grant.StepUpAction
	}
	if grant.StepUpProofID != nil {
		stepUpProofID = *grant.StepUpProofID
	}
	recoveryPointID, entryID := "", ""
	if grant.RecoveryPointID != nil {
		recoveryPointID = *grant.RecoveryPointID
	}
	if grant.EntryID != nil {
		entryID = *grant.EntryID
	}
	return backupasset.AuditEventInput{
		Actor:  backupasset.AuditActor{UserID: grant.OwnerUserID, Role: grant.SessionRole},
		Action: action, Outcome: outcome, RecoveryPointID: recoveryPointID, EntryID: entryID,
		ByteCount: grant.AuditRangeBytes, Range: backupasset.NewRangeSummary(grant.AuditRangeCount, grant.AuditRangeBytes),
		StepUpAction: stepUpAction, StepUpProofID: stepUpProofID, GrantID: grant.ID, FailureCode: failureCode,
		Fields: map[backupasset.AuditField]any{
			backupasset.AuditFieldRenderer: grant.Renderer, backupasset.AuditFieldProfile: grant.Profile,
			backupasset.AuditFieldSource: grant.Classification,
		},
	}
}

func boundedAuditAdd(column string, increment, maximum int64) clause.Expr {
	return gorm.Expr("CASE WHEN ? <= ? - ? THEN ? + ? ELSE ? END", gorm.Expr(column), maximum, increment, gorm.Expr(column), increment, maximum)
}
