package backupasset

import (
	"errors"
	"reflect"
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

func TestAuditRejectsRawStepUpProofInsteadOfOpaqueProofID(t *testing.T) {
	rawJWT := "eyJhbGciOiJIUzI1NiJ9.eyJwdXJwb3NlIjoic3RlcF91cCJ9.FAKE_SIGNATURE_FOR_TEST_ONLY"
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
