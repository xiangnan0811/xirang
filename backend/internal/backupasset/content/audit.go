package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

var (
	ErrContentAuditUnavailable = errors.New("content audit unavailable")
	ErrContentAuditMismatch    = errors.New("content audit idempotency mismatch")
	ErrContentAuditBacklogFull = errors.New("content audit backlog full")
	ErrContentAuditNotReady    = errors.New("content final audit not ready")
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

type ContentAuditService struct {
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
	if grant.InFlight != 0 || (grant.State != string(DeliveryRevoked) &&
		grant.State != string(DeliveryExpired) && grant.State != string(DeliveryClosed)) {
		return ErrContentAuditNotReady
	}
	responseBytes, err := service.finalResponseBytes(ctx, grant)
	if err != nil {
		return err
	}
	input := aggregateAuditInput(grant, responseBytes)
	if err := service.Write(ctx, input); err != nil {
		return errors.Join(err, service.queueAuditRetry(ctx, grant))
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

func (service *ContentAuditService) finalResponseBytes(
	ctx context.Context,
	grant model.BackupAssetDeliveryGrant,
) (int64, error) {
	type requestSummary struct {
		RequestCount  int64 `gorm:"column:request_count"`
		ResponseBytes int64 `gorm:"column:response_bytes"`
	}
	var summary requestSummary
	result := service.db.WithContext(ctx).Model(&model.BackupAssetDeliveryRequest{}).
		Select("COUNT(*) AS request_count, COALESCE(SUM(response_bytes), 0) AS response_bytes").
		Where("grant_id = ?", grant.ID).Scan(&summary)
	if result.Error != nil || summary.RequestCount != grant.AuditRequestCount || summary.ResponseBytes < 0 {
		return 0, ErrContentAuditMismatch
	}
	var nonterminal int64
	if err := service.db.WithContext(ctx).Model(&model.BackupAssetDeliveryRequest{}).
		Where("grant_id = ? AND state NOT IN ?", grant.ID, []string{
			string(RequestSucceeded), string(RequestBlocked), string(RequestCanceled),
			string(RequestFailed), string(RequestReconciled),
		}).Count(&nonterminal).Error; err != nil || nonterminal != 0 {
		return 0, ErrContentAuditNotReady
	}
	return summary.ResponseBytes, nil
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

func (service *ContentAuditService) queueAuditRetry(ctx context.Context, grant model.BackupAssetDeliveryGrant) error {
	service.metrics.ObserveAuditRetry()
	attempt := grant.AuditAttemptCount + 1
	shift := min(attempt-1, int64(6))
	backoff := time.Duration(1<<shift) * time.Second
	next := service.now().UTC().Add(backoff)
	result := service.db.WithContext(ctx).Model(&model.BackupAssetDeliveryGrant{}).
		Where("id = ? AND version = ? AND audit_state = ?", grant.ID, grant.Version, grant.AuditState).
		Updates(map[string]any{
			"audit_state": "retry_wait", "audit_failure_code": "audit_write_failed",
			"audit_attempt_count": attempt, "audit_next_attempt_at": next,
			"updated_at": service.now().UTC(), "version": gorm.Expr("version + 1"),
		})
	service.observeBacklog(ctx)
	if result.Error != nil {
		return fmt.Errorf("%w: persist audit retry: %w", ErrContentAuditUnavailable, result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: persist audit retry rows=%d", ErrContentAuditUnavailable, result.RowsAffected)
	}
	return nil
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

func aggregateAuditInput(grant model.BackupAssetDeliveryGrant, responseBytes int64) backupasset.AuditEventInput {
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
		ByteCount: responseBytes, Range: backupasset.NewRangeSummary(grant.AuditRangeCount, grant.AuditRangeBytes),
		StepUpAction: stepUpAction, StepUpProofID: stepUpProofID, GrantID: grant.ID, FailureCode: failureCode,
		Fields: map[backupasset.AuditField]any{
			backupasset.AuditFieldRenderer: grant.Renderer, backupasset.AuditFieldProfile: grant.Profile,
			backupasset.AuditFieldSource: grant.Classification,
		},
	}
}
