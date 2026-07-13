package backupasset

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type AuditAction string

const (
	AuditActionRepositoryList       AuditAction = "repository_list"
	AuditActionRepositoryConnect    AuditAction = "repository_connect"
	AuditActionRepositoryReconcile  AuditAction = "repository_reconcile"
	AuditActionRepositoryDisconnect AuditAction = "repository_disconnect"
	AuditActionRepositoryImport     AuditAction = "repository_import"
	AuditActionRepositoryReview     AuditAction = "repository_review"
	AuditActionRepositoryPurgePlan  AuditAction = "repository_purge_plan"
	AuditActionRepositoryPurge      AuditAction = "repository_purge"

	AuditActionRecoveryPointList     AuditAction = "recovery_point_list"
	AuditActionRecoveryPointDetail   AuditAction = "recovery_point_detail"
	AuditActionRecoveryPointEvidence AuditAction = "recovery_point_evidence"
	AuditActionRecoveryPointDiff     AuditAction = "recovery_point_diff"

	AuditActionAssetList   AuditAction = "asset_list"
	AuditActionAssetSearch AuditAction = "asset_search"

	AuditActionSavedSearchCreate AuditAction = "saved_search_create"
	AuditActionSavedSearchUpdate AuditAction = "saved_search_update"
	AuditActionSavedSearchDelete AuditAction = "saved_search_delete"
	AuditActionFavoriteAdd       AuditAction = "favorite_add"
	AuditActionFavoriteRemove    AuditAction = "favorite_remove"
	AuditActionTagCreate         AuditAction = "tag_create"
	AuditActionTagUpdate         AuditAction = "tag_update"
	AuditActionTagDelete         AuditAction = "tag_delete"
	AuditActionTagAssign         AuditAction = "tag_assign"
	AuditActionTagUnassign       AuditAction = "tag_unassign"
	AuditActionRecentClear       AuditAction = "recent_clear"

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
	AuditActionRecoveryPointList,
	AuditActionRecoveryPointDetail,
	AuditActionRecoveryPointEvidence,
	AuditActionRecoveryPointDiff,
	AuditActionAssetList,
	AuditActionAssetSearch,
	AuditActionSavedSearchCreate,
	AuditActionSavedSearchUpdate,
	AuditActionSavedSearchDelete,
	AuditActionFavoriteAdd,
	AuditActionFavoriteRemove,
	AuditActionTagCreate,
	AuditActionTagUpdate,
	AuditActionTagDelete,
	AuditActionTagAssign,
	AuditActionTagUnassign,
	AuditActionRecentClear,
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

	rawFields := make(map[string]any, len(input.Fields))
	for field, value := range input.Fields {
		rawFields[string(field)] = value
	}
	input.Fields = SanitizeAuditFields(rawFields)
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
