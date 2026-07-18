package backupasset

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestAuditActionRegistryMatchesDesignContract(t *testing.T) {
	want := []AuditAction{
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
	if !reflect.DeepEqual(AuditActions, want) {
		t.Fatalf("audit action registry drifted:\n got: %#v\nwant: %#v", AuditActions, want)
	}
	seen := make(map[AuditAction]struct{}, len(want))
	for _, action := range want {
		if _, duplicate := seen[action]; duplicate {
			t.Fatalf("duplicate audit action %q", action)
		}
		seen[action] = struct{}{}
		if !ValidAuditAction(action) {
			t.Fatalf("registered action %q is not valid", action)
		}
	}
	if ValidAuditAction(AuditAction("asset.unregistered")) {
		t.Fatal("unknown audit action was accepted")
	}
}

func TestSearchOverlayAuditActionValuesAreStable(t *testing.T) {
	want := map[AuditAction]string{
		AuditActionSavedSearchUse:         "saved_search_use",
		AuditActionSavedSearchBroken:      "saved_search_broken",
		AuditActionFavoriteTombstone:      "favorite_tombstone",
		AuditActionTagAssignmentTombstone: "tag_assignment_tombstone",
		AuditActionRecentRecord:           "recent_record",
		AuditActionOverlayCleanup:         "overlay_cleanup",
	}
	for action, value := range want {
		if string(action) != value || !ValidAuditAction(action) {
			t.Fatalf("Search/Overlay audit action drifted: action=%q want=%q valid=%t", action, value, ValidAuditAction(action))
		}
	}
}

func TestPublicationAuditActionsPermitOnlySafeTypedFields(t *testing.T) {
	actions := []AuditAction{
		AuditActionRecoveryPointPublicationPrepare,
		AuditActionRecoveryPointPublicationVerify,
		AuditActionRecoveryPointPublicationCommit,
		AuditActionRecoveryPointPublicationFail,
		AuditActionRecoveryPointPublicationReconcile,
		AuditActionResticLegacyOperationBlocked,
	}
	repositoryID := strings.Repeat("a", 32)
	pointID := strings.Repeat("b", 32)
	taskID := uint(41)
	runID := uint(42)
	for _, action := range actions {
		for _, actor := range []AuditActor{
			{UserID: 7, Username: "operator", Role: "operator"},
			{Username: "system", Role: "system"},
		} {
			fields := map[AuditField]any{
				AuditFieldStage:         "manifest",
				AuditFieldStatus:        "verifying",
				AuditFieldCode:          "evidence_missing_summary",
				AuditFieldCorrelationID: "corr.publication-42",
			}
			if action == AuditActionResticLegacyOperationBlocked {
				fields[AuditFieldOperation] = "legacy_backup"
			}
			event, err := NewAuditEvent(AuditEventInput{
				Actor: actor, Action: action, RepositoryID: repositoryID, RecoveryPointID: pointID,
				TaskID: &taskID, TaskRunID: &runID, ItemCount: 7, ByteCount: 16384, Fields: fields,
			})
			if err != nil {
				t.Fatalf("new publication audit event for %s/%+v: %v", action, actor, err)
			}
			want := map[AuditField]any{
				AuditFieldStage:         "manifest",
				AuditFieldStatus:        "verifying",
				AuditFieldCode:          "evidence_missing_summary",
				AuditFieldCorrelationID: "corr.publication-42",
			}
			if action == AuditActionResticLegacyOperationBlocked {
				want[AuditFieldOperation] = "legacy_backup"
			}
			if !reflect.DeepEqual(event.Fields, want) {
				t.Fatalf("publication audit fields for %s=%#v, want %#v", action, event.Fields, want)
			}
			if event.RepositoryID != repositoryID || event.RecoveryPointID != pointID || event.TaskID == nil || *event.TaskID != taskID || event.TaskRunID == nil || *event.TaskRunID != runID {
				t.Fatalf("publication audit typed fields drifted: %+v", event)
			}
		}
	}
	if _, err := NewAuditEvent(AuditEventInput{
		Action: AuditActionRecoveryPointPublicationPrepare,
		Fields: map[AuditField]any{AuditFieldOperation: "legacy_backup"},
	}); err == nil {
		t.Fatal("non-legacy publication action accepted an operation field")
	}
	if _, err := NewAuditEvent(AuditEventInput{
		Action: AuditActionResticLegacyOperationBlocked,
		Fields: map[AuditField]any{AuditFieldOperation: "arbitrary_label"},
	}); err == nil {
		t.Fatal("legacy operation audit action accepted an arbitrary operation")
	}
	unsafe, err := NewAuditEvent(AuditEventInput{
		Action: AuditActionRecoveryPointPublicationPrepare,
		Fields: map[AuditField]any{
			AuditFieldStage:   "manifest",
			AuditFieldStatus:  "verifying",
			AuditFieldCode:    "evidence_missing_summary",
			AuditFieldSource:  "FAKE_REPOSITORY_IDENTITY_FOR_TEST_ONLY",
			AuditFieldMode:    "FAKE_PROVIDER_LOCATOR_FOR_TEST_ONLY",
			AuditFieldProfile: "FAKE_SOURCE_AND_EXCLUDES_FOR_TEST_ONLY",
		},
	})
	if err != nil {
		t.Fatalf("sanitize unsafe publication audit input: %v", err)
	}
	if len(unsafe.Fields) != 3 {
		t.Fatalf("unsafe publication evidence reached FieldsJSON input: %#v", unsafe.Fields)
	}
}

func TestAuditRejectsUnknownActionAndField(t *testing.T) {
	if _, err := NewAuditEvent(AuditEventInput{Action: AuditAction("asset.unregistered")}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("unknown action got %v, want ErrInvalidState", err)
	}
	if _, err := NewAuditEvent(AuditEventInput{
		Action: AuditActionAssetList,
		Fields: map[AuditField]any{AuditField("path"): "/raw/path"},
	}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("unknown field got %v, want ErrInvalidState", err)
	}
}

func TestAuditSanitizerDropsForbiddenKeysAndValues(t *testing.T) {
	got := SanitizeAuditFields(map[string]any{
		"stage":            "catalog_build",
		"correlation_id":   "corr_123",
		"repository_id":    "quarterly-report.txt",
		"path":             "/private/customer/report.txt",
		"name":             "report.txt",
		"query":            "owner=alice",
		"snippet":          "first bytes",
		"content":          "file body",
		"ticket":           "FAKE_TICKET_FOR_TEST_ONLY",
		"cookie":           "FAKE_COOKIE_FOR_TEST_ONLY",
		"jwt":              "FAKE_JWT_FOR_TEST_ONLY",
		"credential":       "FAKE_CREDENTIAL_FOR_TEST_ONLY",
		"provider_locator": "s3://private-bucket/object",
		"status":           "Bearer FAKE_TOKEN_FOR_TEST_ONLY",
	})
	if got[AuditFieldStage] != "catalog_build" || got[AuditFieldCorrelationID] != "corr_123" {
		t.Fatalf("safe fields were not retained: %#v", got)
	}
	if _, ok := got[AuditFieldStatus]; ok {
		t.Fatalf("forbidden status value survived sanitization: %#v", got)
	}
	if len(got) != 2 {
		t.Fatalf("forbidden/unknown fields survived sanitization: %#v", got)
	}
}

func TestAuditRejectsNonOpaqueResourceIdentifiers(t *testing.T) {
	tests := []AuditEventInput{
		{Action: AuditActionAssetList, RepositoryID: "quarterly-report.txt"},
		{Action: AuditActionAssetList, RecoveryPointID: "customer-backup"},
		{Action: AuditActionAssetList, EntryID: "report.txt"},
		{Action: AuditActionRecoveryExecute, RecoveryJobID: "restore-customer-name"},
		{Action: AuditActionExportCreate, ExportJobID: "export-customer-name"},
	}
	for _, input := range tests {
		if _, err := NewAuditEvent(input); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("non-opaque resource identifiers got %v, want ErrInvalidState: %+v", err, input)
		}
	}
}

func TestRcloneVersioningAuditActionsAreRegistered(t *testing.T) {
	for _, action := range []AuditAction{
		AuditActionRcloneVersioningPortableSetup,
		AuditActionRcloneVersioningPortableBinding,
		AuditActionRcloneVersioningNativeSetup,
		AuditActionRcloneVersioningNativeBinding,
		AuditActionRcloneVersioningPreflight,
		AuditActionRcloneVersioningActivate,
		AuditActionRcloneVersioningCleanRollback,
		AuditActionRcloneVersioningRollbackPreparation,
	} {
		if !ValidAuditAction(action) {
			t.Fatalf("Rclone versioning audit action is not registered: %q", action)
		}
	}
}

func TestAuditRejectsRawStepUpProofInsteadOfOpaqueProofID(t *testing.T) {
	rawJWT := "FAKE_JWT_HEADER_FOR_TEST_ONLY.FAKE_JWT_PAYLOAD_FOR_TEST_ONLY.FAKE_JWT_SIGNATURE_FOR_TEST_ONLY"
	if _, err := NewAuditEvent(AuditEventInput{
		Action:        AuditActionAssetDownload,
		StepUpProofID: rawJWT,
	}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("raw step-up JWT got %v, want ErrInvalidState", err)
	}

	event, err := NewAuditEvent(AuditEventInput{
		Action:        AuditActionAssetDownload,
		StepUpProofID: "0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("opaque step-up proof ID rejected: %v", err)
	}
	if event.StepUpProofID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("opaque step-up proof ID drifted: %q", event.StepUpProofID)
	}
	fields := SanitizeAuditFields(map[string]any{"step_up_proof_id": rawJWT})
	if _, ok := fields[AuditFieldStepUpProofID]; ok {
		t.Fatalf("raw step-up JWT survived field sanitization: %#v", fields)
	}
}
