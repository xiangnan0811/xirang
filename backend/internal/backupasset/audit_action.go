package backupasset

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type AuditAction string

const (
	AuditActionRepositoryList                      AuditAction = "repository_list"
	AuditActionRepositoryConnect                   AuditAction = "repository_connect"
	AuditActionRepositoryReconcile                 AuditAction = "repository_reconcile"
	AuditActionRepositoryDisconnect                AuditAction = "repository_disconnect"
	AuditActionRepositoryImport                    AuditAction = "repository_import"
	AuditActionRepositoryReview                    AuditAction = "repository_review"
	AuditActionRepositoryPurgePlan                 AuditAction = "repository_purge_plan"
	AuditActionRepositoryPurge                     AuditAction = "repository_purge"
	AuditActionRsyncVersioningPreflight            AuditAction = "rsync_versioning_preflight"
	AuditActionRsyncVersioningActivate             AuditAction = "rsync_versioning_activate"
	AuditActionRsyncVersioningRollback             AuditAction = "rsync_versioning_rollback"
	AuditActionRcloneVersioningPortableSetup       AuditAction = "rclone_versioning_portable_setup"
	AuditActionRcloneVersioningPortableBinding     AuditAction = "rclone_versioning_portable_binding"
	AuditActionRcloneVersioningNativeSetup         AuditAction = "rclone_versioning_native_setup"
	AuditActionRcloneVersioningNativeBinding       AuditAction = "rclone_versioning_native_binding"
	AuditActionRcloneVersioningPreflight           AuditAction = "rclone_versioning_preflight"
	AuditActionRcloneVersioningActivate            AuditAction = "rclone_versioning_activate"
	AuditActionRcloneVersioningCleanRollback       AuditAction = "rclone_versioning_clean_rollback"
	AuditActionRcloneVersioningRollbackPreparation AuditAction = "rclone_versioning_rollback_preparation"

	AuditActionRecoveryPointList                 AuditAction = "recovery_point_list"
	AuditActionRecoveryPointDetail               AuditAction = "recovery_point_detail"
	AuditActionRecoveryPointEvidence             AuditAction = "recovery_point_evidence"
	AuditActionRecoveryPointDiff                 AuditAction = "recovery_point_diff"
	AuditActionRecoveryPointPublicationPrepare   AuditAction = "recovery_point_publication_prepare"
	AuditActionRecoveryPointPublicationVerify    AuditAction = "recovery_point_publication_verify"
	AuditActionRecoveryPointPublicationCommit    AuditAction = "recovery_point_publication_commit"
	AuditActionRecoveryPointPublicationFail      AuditAction = "recovery_point_publication_fail"
	AuditActionRecoveryPointPublicationReconcile AuditAction = "recovery_point_publication_reconcile"
	AuditActionResticLegacyOperationBlocked      AuditAction = "restic_legacy_operation_blocked"

	AuditActionAssetList   AuditAction = "asset_list"
	AuditActionAssetSearch AuditAction = "asset_search"

	AuditActionSavedSearchCreate      AuditAction = "saved_search_create"
	AuditActionSavedSearchUpdate      AuditAction = "saved_search_update"
	AuditActionSavedSearchDelete      AuditAction = "saved_search_delete"
	AuditActionSavedSearchUse         AuditAction = "saved_search_use"
	AuditActionSavedSearchBroken      AuditAction = "saved_search_broken"
	AuditActionFavoriteAdd            AuditAction = "favorite_add"
	AuditActionFavoriteRemove         AuditAction = "favorite_remove"
	AuditActionFavoriteTombstone      AuditAction = "favorite_tombstone"
	AuditActionTagCreate              AuditAction = "tag_create"
	AuditActionTagUpdate              AuditAction = "tag_update"
	AuditActionTagDelete              AuditAction = "tag_delete"
	AuditActionTagAssign              AuditAction = "tag_assign"
	AuditActionTagUnassign            AuditAction = "tag_unassign"
	AuditActionTagAssignmentTombstone AuditAction = "tag_assignment_tombstone"
	AuditActionRecentRecord           AuditAction = "recent_record"
	AuditActionRecentClear            AuditAction = "recent_clear"
	AuditActionOverlayCleanup         AuditAction = "overlay_cleanup"

	AuditActionPreviewJob    AuditAction = "preview_job"
	AuditActionPreviewTicket AuditAction = "preview_ticket"
	AuditActionPreviewRead   AuditAction = "preview_read"

	AuditActionAssetDownloadTicket    AuditAction = "asset_download_ticket"
	AuditActionAssetDownload          AuditAction = "asset_download"
	AuditActionProcessingPolicyUpdate AuditAction = "processing_policy_update"
	AuditActionArchiveInspect         AuditAction = "archive_inspect"
	AuditActionArchiveMember          AuditAction = "archive_member"

	AuditActionExportCreate         AuditAction = "export_create"
	AuditActionExportCancel         AuditAction = "export_cancel"
	AuditActionExportDownloadTicket AuditAction = "export_download_ticket"
	AuditActionExportDownload       AuditAction = "export_download"

	AuditActionRecoveryPlan                 AuditAction = "recovery_plan"
	AuditActionRecoveryPreflight            AuditAction = "recovery_preflight"
	AuditActionRecoveryAuthorize            AuditAction = "recovery_authorize"
	AuditActionRecoveryExecute              AuditAction = "recovery_execute"
	AuditActionRecoveryCancel               AuditAction = "recovery_cancel"
	AuditActionRecoveryVerify               AuditAction = "recovery_verify"
	AuditActionRecoveryCleanup              AuditAction = "recovery_cleanup"
	AuditActionRecoveryRetain               AuditAction = "recovery_retain"
	AuditActionRecoveryResultDownloadTicket AuditAction = "recovery_result_download_ticket"
	AuditActionRecoveryResultDownload       AuditAction = "recovery_result_download"

	AuditActionRetentionPolicyCreate AuditAction = "retention_policy_create"
	AuditActionRetentionPolicyUpdate AuditAction = "retention_policy_update"
	AuditActionRetentionPolicyDelete AuditAction = "retention_policy_delete"
	AuditActionHoldCreate            AuditAction = "hold_create"
	AuditActionHoldRelease           AuditAction = "hold_release"
)

var AuditActions = []AuditAction{
	AuditActionRepositoryList,
	AuditActionRepositoryConnect,
	AuditActionRepositoryReconcile,
	AuditActionRepositoryDisconnect,
	AuditActionRepositoryImport,
	AuditActionRepositoryReview,
	AuditActionRepositoryPurgePlan,
	AuditActionRepositoryPurge,
	AuditActionRsyncVersioningPreflight,
	AuditActionRsyncVersioningActivate,
	AuditActionRsyncVersioningRollback,
	AuditActionRcloneVersioningPortableSetup,
	AuditActionRcloneVersioningPortableBinding,
	AuditActionRcloneVersioningNativeSetup,
	AuditActionRcloneVersioningNativeBinding,
	AuditActionRcloneVersioningPreflight,
	AuditActionRcloneVersioningActivate,
	AuditActionRcloneVersioningCleanRollback,
	AuditActionRcloneVersioningRollbackPreparation,
	AuditActionRecoveryPointList,
	AuditActionRecoveryPointDetail,
	AuditActionRecoveryPointEvidence,
	AuditActionRecoveryPointDiff,
	AuditActionRecoveryPointPublicationPrepare,
	AuditActionRecoveryPointPublicationVerify,
	AuditActionRecoveryPointPublicationCommit,
	AuditActionRecoveryPointPublicationFail,
	AuditActionRecoveryPointPublicationReconcile,
	AuditActionResticLegacyOperationBlocked,
	AuditActionAssetList,
	AuditActionAssetSearch,
	AuditActionSavedSearchCreate,
	AuditActionSavedSearchUpdate,
	AuditActionSavedSearchDelete,
	AuditActionSavedSearchUse,
	AuditActionSavedSearchBroken,
	AuditActionFavoriteAdd,
	AuditActionFavoriteRemove,
	AuditActionFavoriteTombstone,
	AuditActionTagCreate,
	AuditActionTagUpdate,
	AuditActionTagDelete,
	AuditActionTagAssign,
	AuditActionTagUnassign,
	AuditActionTagAssignmentTombstone,
	AuditActionRecentRecord,
	AuditActionRecentClear,
	AuditActionOverlayCleanup,
	AuditActionPreviewJob,
	AuditActionPreviewTicket,
	AuditActionPreviewRead,
	AuditActionAssetDownloadTicket,
	AuditActionAssetDownload,
	AuditActionProcessingPolicyUpdate,
	AuditActionArchiveInspect,
	AuditActionArchiveMember,
	AuditActionExportCreate,
	AuditActionExportCancel,
	AuditActionExportDownloadTicket,
	AuditActionExportDownload,
	AuditActionRecoveryPlan,
	AuditActionRecoveryPreflight,
	AuditActionRecoveryAuthorize,
	AuditActionRecoveryExecute,
	AuditActionRecoveryCancel,
	AuditActionRecoveryVerify,
	AuditActionRecoveryCleanup,
	AuditActionRecoveryRetain,
	AuditActionRecoveryResultDownloadTicket,
	AuditActionRecoveryResultDownload,
	AuditActionRetentionPolicyCreate,
	AuditActionRetentionPolicyUpdate,
	AuditActionRetentionPolicyDelete,
	AuditActionHoldCreate,
	AuditActionHoldRelease,
}

var validAuditActions = setOf(AuditActions...)

func ValidAuditAction(action AuditAction) bool {
	return validAuditActions[action]
}

type AuditField string

const (
	AuditFieldStage           AuditField = "stage"
	AuditFieldStatus          AuditField = "status"
	AuditFieldCode            AuditField = "code"
	AuditFieldReasonCode      AuditField = "reason_code"
	AuditFieldCorrelationID   AuditField = "correlation_id"
	AuditFieldRepositoryID    AuditField = "repository_id"
	AuditFieldRecoveryPointID AuditField = "recovery_point_id"
	AuditFieldEntryID         AuditField = "entry_id"
	AuditFieldTaskID          AuditField = "task_id"
	AuditFieldTaskRunID       AuditField = "task_run_id"
	AuditFieldRecoveryJobID   AuditField = "recovery_job_id"
	AuditFieldExportJobID     AuditField = "export_job_id"
	AuditFieldGrantID         AuditField = "grant_id"
	AuditFieldStepUpProofID   AuditField = "step_up_proof_id"
	AuditFieldStepUpAction    AuditField = "step_up_action"
	AuditFieldItemCount       AuditField = "item_count"
	AuditFieldByteCount       AuditField = "byte_count"
	AuditFieldRangeCount      AuditField = "range_count"
	AuditFieldRangeBytes      AuditField = "range_bytes"
	AuditFieldRenderer        AuditField = "renderer"
	AuditFieldProfile         AuditField = "profile"
	AuditFieldFormat          AuditField = "format"
	AuditFieldMode            AuditField = "mode"
	AuditFieldSource          AuditField = "source"
	AuditFieldOperation       AuditField = "operation"
)

var AuditFields = []AuditField{
	AuditFieldStage,
	AuditFieldStatus,
	AuditFieldCode,
	AuditFieldReasonCode,
	AuditFieldCorrelationID,
	AuditFieldRepositoryID,
	AuditFieldRecoveryPointID,
	AuditFieldEntryID,
	AuditFieldTaskID,
	AuditFieldTaskRunID,
	AuditFieldRecoveryJobID,
	AuditFieldExportJobID,
	AuditFieldGrantID,
	AuditFieldStepUpProofID,
	AuditFieldStepUpAction,
	AuditFieldItemCount,
	AuditFieldByteCount,
	AuditFieldRangeCount,
	AuditFieldRangeBytes,
	AuditFieldRenderer,
	AuditFieldProfile,
	AuditFieldFormat,
	AuditFieldMode,
	AuditFieldSource,
	AuditFieldOperation,
}

var validAuditFields = setOf(AuditFields...)

func ValidAuditField(field AuditField) bool {
	return validAuditFields[field]
}

type AuditOutcome string

const (
	AuditOutcomeSuccess AuditOutcome = "success"
	AuditOutcomeFailure AuditOutcome = "failure"
	AuditOutcomeBlocked AuditOutcome = "blocked"
)

var validAuditOutcomes = setOf(AuditOutcomeSuccess, AuditOutcomeFailure, AuditOutcomeBlocked)

const (
	MaxAuditRangeCount int64 = 1_000_000
	MaxAuditRangeBytes int64 = 1 << 50
)

type RangeSummary struct {
	Count int64
	Bytes int64
}

func NewRangeSummary(count, bytes int64) RangeSummary {
	return RangeSummary{
		Count: clampAuditMetric(count, MaxAuditRangeCount),
		Bytes: clampAuditMetric(bytes, MaxAuditRangeBytes),
	}
}

func clampAuditMetric(value, maximum int64) int64 {
	if value < 0 {
		return 0
	}
	if value > maximum {
		return maximum
	}
	return value
}

type AuditActor struct {
	UserID   uint
	Username string
	Role     string
}

type AuditFingerprintInput struct {
	Path  string
	Query string
}

type AuditEventInput struct {
	Actor           AuditActor
	Action          AuditAction
	Outcome         AuditOutcome
	RepositoryID    string
	RecoveryPointID string
	EntryID         string
	TaskID          *uint
	TaskRunID       *uint
	RecoveryJobID   string
	ExportJobID     string
	ItemCount       int64
	ByteCount       int64
	Range           RangeSummary
	StepUpAction    string
	StepUpProofID   string
	GrantID         string
	FailureCode     string
	Fields          map[AuditField]any
	Fingerprints    AuditFingerprintInput
}

type AuditEvent struct {
	AuditEventInput
	fingerprints AuditFingerprintInput
}

func NewAuditEvent(input AuditEventInput) (AuditEvent, error) {
	if !ValidAuditAction(input.Action) {
		return AuditEvent{}, fmt.Errorf("%w: unknown audit action", ErrInvalidState)
	}
	if input.Outcome == "" {
		input.Outcome = AuditOutcomeSuccess
	}
	if !validAuditOutcomes[input.Outcome] {
		return AuditEvent{}, fmt.Errorf("%w: unknown audit outcome", ErrInvalidState)
	}
	if input.ItemCount < 0 || input.ByteCount < 0 || input.Range.Count < 0 || input.Range.Bytes < 0 {
		return AuditEvent{}, fmt.Errorf("%w: negative audit summary", ErrInvalidState)
	}
	if !validOptionalAuditOpaqueID(input.RepositoryID) ||
		!validOptionalAuditOpaqueID(input.RecoveryPointID) ||
		!validOptionalAuditEntryID(input.EntryID) ||
		!validOptionalAuditOpaqueID(input.RecoveryJobID) ||
		!validOptionalAuditOpaqueID(input.ExportJobID) ||
		!validOptionalAuditOpaqueID(input.StepUpProofID) {
		return AuditEvent{}, fmt.Errorf("%w: non-opaque audit resource identifier", ErrInvalidState)
	}
	for field := range input.Fields {
		if !ValidAuditField(field) {
			return AuditEvent{}, fmt.Errorf("%w: unknown audit field %q", ErrInvalidState, field)
		}
	}
	if err := validatePublicationAuditOperationField(input.Action, input.Fields); err != nil {
		return AuditEvent{}, err
	}

	rawFields := make(map[string]any, len(input.Fields))
	for field, value := range input.Fields {
		rawFields[string(field)] = value
	}
	input.Fields = SanitizeAuditFields(rawFields)
	if isPublicationAuditAction(input.Action) {
		input.Fields = sanitizePublicationAuditFields(input.Action, input.Fields)
	}
	input.Actor.Username = boundAuditString(input.Actor.Username, 64)
	input.Actor.Role = boundAuditString(input.Actor.Role, 32)
	input.RepositoryID = boundAuditString(input.RepositoryID, 32)
	input.RecoveryPointID = boundAuditString(input.RecoveryPointID, 32)
	input.EntryID = boundAuditString(input.EntryID, 64)
	input.RecoveryJobID = boundAuditString(input.RecoveryJobID, 32)
	input.ExportJobID = boundAuditString(input.ExportJobID, 32)
	input.StepUpAction = safeAuditLabel(input.StepUpAction, 64)
	input.StepUpProofID = safeAuditLabel(input.StepUpProofID, 64)
	input.GrantID = safeAuditLabel(input.GrantID, 32)
	input.FailureCode = safeAuditLabel(input.FailureCode, 64)
	input.Range = NewRangeSummary(input.Range.Count, input.Range.Bytes)

	fingerprints := input.Fingerprints
	input.Fingerprints = AuditFingerprintInput{}
	return AuditEvent{AuditEventInput: input, fingerprints: fingerprints}, nil
}

func isPublicationAuditAction(action AuditAction) bool {
	switch action {
	case AuditActionRecoveryPointPublicationPrepare,
		AuditActionRecoveryPointPublicationVerify,
		AuditActionRecoveryPointPublicationCommit,
		AuditActionRecoveryPointPublicationFail,
		AuditActionRecoveryPointPublicationReconcile,
		AuditActionResticLegacyOperationBlocked:
		return true
	default:
		return false
	}
}

func validatePublicationAuditOperationField(action AuditAction, fields map[AuditField]any) error {
	value, present := fields[AuditFieldOperation]
	if !present {
		return nil
	}
	if action == AuditActionRecoveryCleanup {
		operation, ok := value.(string)
		if !ok || operation != "recovery_reconcile" {
			return fmt.Errorf("%w: invalid recovery cleanup operation", ErrInvalidState)
		}
		return nil
	}
	if action != AuditActionResticLegacyOperationBlocked {
		return fmt.Errorf("%w: operation field is only valid for legacy Restic block audits", ErrInvalidState)
	}
	operation, ok := value.(string)
	if !ok || !validLegacyResticAuditOperation(operation) {
		return fmt.Errorf("%w: invalid legacy Restic operation", ErrInvalidState)
	}
	return nil
}

func sanitizePublicationAuditFields(action AuditAction, fields map[AuditField]any) map[AuditField]any {
	result := make(map[AuditField]any, 5)
	for _, field := range []AuditField{AuditFieldStage, AuditFieldStatus, AuditFieldCode, AuditFieldCorrelationID} {
		value, ok := fields[field]
		if !ok {
			continue
		}
		text, ok := value.(string)
		if !ok {
			continue
		}
		switch field {
		case AuditFieldCode:
			if ValidatePublicationFailureCode(PublicationFailureCode(text)) != nil && ValidatePublicationOutcomeCode(PublicationOutcomeCode(text)) != nil {
				continue
			}
		case AuditFieldCorrelationID:
			if !validPublicationCorrelationID(text) {
				continue
			}
		default:
			if safeAuditLabel(text, 32) != text {
				continue
			}
		}
		result[field] = text
	}
	if action == AuditActionResticLegacyOperationBlocked {
		if operation, ok := fields[AuditFieldOperation].(string); ok && validLegacyResticAuditOperation(operation) {
			result[AuditFieldOperation] = operation
		}
	}
	return result
}

func validLegacyResticAuditOperation(operation string) bool {
	switch operation {
	case "legacy_backup", "legacy_snapshot_list", "legacy_snapshot_files", "legacy_index", "legacy_search",
		"legacy_diff", "legacy_snapshot_restore", "legacy_restore_latest", "legacy_anomaly", "legacy_retention":
		return true
	default:
		return false
	}
}

func SanitizeAuditFields(input map[string]any) map[AuditField]any {
	if len(input) == 0 {
		return map[AuditField]any{}
	}
	result := make(map[AuditField]any, len(input))
	for rawField, rawValue := range input {
		field := AuditField(strings.TrimSpace(rawField))
		if !ValidAuditField(field) || auditFieldNameForbidden(string(field)) {
			continue
		}
		value, ok := sanitizeAuditFieldValue(field, rawValue)
		if ok {
			result[field] = value
		}
	}
	return result
}

func sanitizeAuditFieldValue(field AuditField, value any) (any, bool) {
	switch field {
	case AuditFieldRepositoryID, AuditFieldRecoveryPointID, AuditFieldRecoveryJobID, AuditFieldExportJobID, AuditFieldStepUpProofID:
		text, ok := value.(string)
		text = strings.TrimSpace(text)
		if !ok || ValidateOpaqueID(text) != nil {
			return nil, false
		}
		return text, true
	case AuditFieldEntryID:
		text, ok := value.(string)
		text = strings.TrimSpace(text)
		if !ok || !isLowerHex(text, entryIDEncodedSize) {
			return nil, false
		}
		return text, true
	case AuditFieldItemCount, AuditFieldByteCount, AuditFieldRangeCount, AuditFieldRangeBytes,
		AuditFieldTaskID, AuditFieldTaskRunID:
		integer, ok := auditInteger(value)
		if !ok || integer < 0 {
			return nil, false
		}
		return integer, true
	default:
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		text = safeAuditLabel(text, 128)
		if text == "" || auditValueForbidden(text) {
			return nil, false
		}
		return text, true
	}
}

func validOptionalAuditOpaqueID(value string) bool {
	return value == "" || ValidateOpaqueID(value) == nil
}

func validOptionalAuditEntryID(value string) bool {
	return value == "" || isLowerHex(value, entryIDEncodedSize)
}

func auditInteger(value any) (int64, bool) {
	switch number := value.(type) {
	case int:
		return int64(number), true
	case int8:
		return int64(number), true
	case int16:
		return int64(number), true
	case int32:
		return int64(number), true
	case int64:
		return number, true
	case uint:
		if uint64(number) > uint64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(number), true
	case uint8:
		return int64(number), true
	case uint16:
		return int64(number), true
	case uint32:
		return int64(number), true
	case uint64:
		if number > uint64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(number), true
	default:
		return 0, false
	}
}

func auditFieldNameForbidden(value string) bool {
	lower := strings.ToLower(value)
	for _, forbidden := range auditForbiddenTerms {
		if strings.Contains(lower, forbidden) {
			return true
		}
	}
	return false
}

func auditValueForbidden(value string) bool {
	lower := strings.ToLower(value)
	for _, forbidden := range append(auditForbiddenTerms, "bearer", "authorization") {
		if strings.Contains(lower, forbidden) {
			return true
		}
	}
	return false
}

var auditForbiddenTerms = []string{
	"path",
	"name",
	"query",
	"snippet",
	"content",
	"ticket",
	"cookie",
	"jwt",
	"token",
	"secret",
	"credential",
	"config",
	"output",
	"stream",
	"command",
	"payload",
	"provider_locator",
}

func safeAuditLabel(value string, maximum int) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	for _, char := range trimmed {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || strings.ContainsRune("_.:@-", char) {
			builder.WriteRune(char)
			continue
		}
		return ""
	}
	return boundAuditString(builder.String(), maximum)
}

func boundAuditString(value string, maximum int) string {
	trimmed := strings.TrimSpace(value)
	if maximum <= 0 || utf8.RuneCountInString(trimmed) <= maximum {
		return trimmed
	}
	runes := []rune(trimmed)
	return string(runes[:maximum])
}
