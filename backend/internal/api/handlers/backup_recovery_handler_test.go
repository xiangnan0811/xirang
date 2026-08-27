package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/api/docs"
	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/recovery"
	backupruntime "xirang/backend/internal/backupasset/runtime"
	applogger "xirang/backend/internal/logger"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRecoveryAuthorizationReceiptSecurityOverrideReplayAndConflict(t *testing.T) {
	testRecoveryAuthorizationReceiptHandlerReplayAndConflict(t, recovery.AuthorizationReceiptSecurityOverride)
}

func TestRecoverySwaggerDocumentsFullSecuredRouteAndPublicIntentMatrix(t *testing.T) {
	document := readBackupAssetSwagger(t)
	rawDocument := docs.SwaggerInfo.ReadDoc()
	for _, staleAction := range []string{
		"recovery.security_override proof", "recovery.write_authorize proof",
		"recovery.exact_mirror_delete_authorize proof", "recovery.execute proof",
		"recovery.result.download 精确 proof",
	} {
		if strings.Contains(rawDocument, staleAction) {
			t.Errorf("generated Swagger advertises nonexistent step-up action %q", staleAction)
		}
	}
	if !strings.Contains(rawDocument, "asset.recover proof") ||
		!strings.Contains(rawDocument, "recovery.result_download 精确 proof") {
		t.Error("generated Swagger omitted exact Recovery step-up action names")
	}
	operations := []struct {
		method  string
		path    string
		success string
	}{
		{"post", "/recovery-plans", "201"},
		{"get", "/recovery-plans/{id}", "200"},
		{"post", "/recovery-plans/{id}/preflights", "200"},
		{"post", "/recovery-plans/{id}/security-overrides", "200"},
		{"post", "/recovery-plans/{id}/write-authorizations", "200"},
		{"post", "/recovery-plans/{id}/execute", "202"},
		{"post", "/recovery-plans/{id}/cancel", "200"},
		{"get", "/recovery-jobs/{id}", "200"},
		{"get", "/recovery-jobs/{id}/items", "200"},
		{"get", "/recovery-jobs/{id}/results", "200"},
		{"post", "/recovery-jobs/{id}/cancel", "200"},
		{"post", "/recovery-jobs/{id}/exact-mirror-delete-authorizations", "200"},
		{"post", "/recovery-jobs/{id}/results/{resultId}/download-ticket", "200"},
		{"post", "/recovery-jobs/{id}/results/retain", "200"},
		{"post", "/recovery-jobs/{id}/results/cleanup", "200"},
		{"post", "/settings/backup-assets/recovery/target-roots", "200"},
		{"put", "/settings/backup-assets/recovery/target-roots/{nodeId}/{rootId}", "200"},
		{"delete", "/settings/backup-assets/recovery/target-roots/{nodeId}/{rootId}", "200"},
		{"get", "/settings/backup-assets/recovery/target-roots", "200"},
		{"post", "/settings/backup-assets/recovery/downgrade-readiness", "200"},
	}
	for _, expected := range operations {
		operation, ok := document.Paths[expected.path][expected.method]
		if !ok {
			t.Fatalf("generated Swagger missing %s %s", strings.ToUpper(expected.method), expected.path)
		}
		if _, ok := operation.Responses[expected.success]; !ok {
			t.Errorf("generated Swagger missing success %s for %s %s", expected.success, strings.ToUpper(expected.method), expected.path)
		}
		for _, status := range []string{"400", "401", "403", "429", "500", "503"} {
			if _, ok := operation.Responses[status]; !ok {
				t.Errorf("generated Swagger missing %s for %s %s", status, strings.ToUpper(expected.method), expected.path)
			}
		}
	}
	for _, operation := range []struct{ method, path string }{
		{"post", "/recovery-plans"},
		{"post", "/settings/backup-assets/recovery/target-roots"},
		{"get", "/settings/backup-assets/recovery/target-roots"},
	} {
		if _, ok := document.Paths[operation.path][operation.method].Responses["404"]; !ok {
			t.Errorf("generated Swagger missing hidden-object 404 for %s %s",
				strings.ToUpper(operation.method), operation.path)
		}
	}

	const prefix = "internal_api_handlers."
	create := requireBackupAssetSwaggerDefinition(t, document, prefix+"backupRecoveryCreatePlanPayload")
	requireBackupAssetSwaggerFields(t, create,
		"catalog_generation_id", "conflict_policy", "entry_ids", "recovery_point_id", "repository_id",
		"schema_version", "target_mode", "target_node_id", "target_root_id",
	)
	for _, forbidden := range []string{
		"selection", "plan", "authority_category", "estimated_items", "estimated_bytes",
		"source_revision", "target_path_digest", "root_locator_digest", "operation_set_digest",
		"security_decision_digest", "preflight_revision",
	} {
		if _, found := create.Properties[forbidden]; found {
			t.Errorf("public create Swagger exposed private field %q", forbidden)
		}
	}
	requireBackupAssetSwaggerEnum(t, create.Properties["target_mode"], "isolated", "in_place")
	requireBackupAssetSwaggerEnum(t, create.Properties["conflict_policy"],
		"fail_on_conflict", "skip_existing", "overwrite_selected", "exact_mirror")
	requireBackupAssetSwaggerBounds(t, create.Properties["entry_ids"], nil, nil, int64Pointer(1), int64Pointer(10000))

	preflight := requireBackupAssetSwaggerDefinition(t, document, prefix+"backupRecoveryRevisionPayload")
	requireBackupAssetSwaggerFields(t, preflight, "expected_revision", "schema_version")
	for _, forbidden := range []string{"input", "permit", "node_revision", "credential_revision", "target_path_digest"} {
		if _, found := preflight.Properties[forbidden]; found {
			t.Errorf("public preflight Swagger exposed private field %q", forbidden)
		}
	}

	requiredPayloads := map[string][]string{
		"backupRecoveryRetainPayload": {
			"expected_revision", "requested_deadline", "schema_version",
		},
		"backupRecoverySchemaPayload": {"schema_version"},
		"backupRecoveryDowngradePayload": {
			"reason", "schema_version",
		},
		"backupRecoverySecurityOverridePayload": {
			"expected_revision", "finding_category", "preflight_id", "reason", "schema_version",
		},
		"backupRecoveryWriteAuthorizationPayload": {
			"expected_revision", "grant_secret", "preflight_id", "reason", "schema_version",
		},
		"backupRecoveryDeleteAuthorizationPayload": {
			"attempt_id", "checkpoint_id", "expected_revision", "grant_secret", "plan_id", "reason", "schema_version",
		},
		"backupRecoveryExecutePayload": {
			"expected_revision", "grant_id", "grant_secret", "preflight_id", "schema_version",
		},
		"backupRecoveryTargetRootRegisterPayload": {
			"locator", "node_id", "overlap_policy_binding", "root_id", "safe_label", "schema_version",
		},
		"backupRecoveryTargetRootRotatePayload": {
			"locator", "overlap_policy_binding", "safe_label", "schema_version",
		},
	}
	for name, fields := range requiredPayloads {
		schema := requireBackupAssetSwaggerDefinition(t, document, prefix+name)
		requireBackupAssetSwaggerFields(t, schema, fields...)
		if name == "backupRecoveryTargetRootRegisterPayload" || name == "backupRecoveryTargetRootRotatePayload" {
			for _, optional := range []string{"reserve_bytes", "reserve_inodes"} {
				if slices.Contains(schema.Required, optional) {
					t.Errorf("%s made optional reservation field %q required", name, optional)
				}
			}
		}
	}

	resultTicket := requireBackupAssetSwaggerDefinition(t, document, prefix+"backupRecoveryResultTicketPayload")
	requireBackupAssetSwaggerFields(t, resultTicket, "schema_version")

	responseDefinitions := []struct {
		method string
		path   string
		status string
		name   string
	}{
		{"post", "/recovery-plans", "201", prefix + "backupRecoveryCreatePlanResponse"},
		{"post", "/recovery-plans/{id}/preflights", "200", "xirang_backend_internal_backupasset_recovery.RecoveryPreflightView"},
		{"get", "/recovery-jobs/{id}/items", "200", "xirang_backend_internal_backupasset_recovery.RecoveryJobItemPage"},
		{"get", "/recovery-jobs/{id}/results", "200", "xirang_backend_internal_backupasset_recovery.RecoveryResultPage"},
		{"post", "/recovery-jobs/{id}/results/retain", "200", prefix + "backupRecoveryRetainResponse"},
		{"post", "/settings/backup-assets/recovery/target-roots", "200", prefix + "backupRecoveryTargetRootResponse"},
		{"put", "/settings/backup-assets/recovery/target-roots/{nodeId}/{rootId}", "200", prefix + "backupRecoveryTargetRootResponse"},
		{"delete", "/settings/backup-assets/recovery/target-roots/{nodeId}/{rootId}", "200", prefix + "backupRecoveryTargetRootResponse"},
		{"post", "/settings/backup-assets/recovery/downgrade-readiness", "200", prefix + "backupRecoveryDowngradeResponse"},
	}
	for _, expected := range responseDefinitions {
		raw := document.Paths[expected.path][expected.method].Responses[expected.status]
		if got := backupRecoverySwaggerResponseDataRef(t, raw); got != "#/definitions/"+expected.name {
			t.Errorf("Swagger %s %s response ref=%q want=%q", strings.ToUpper(expected.method), expected.path,
				got, "#/definitions/"+expected.name)
		}
	}
	for _, name := range []string{
		"backupRecoveryCreatePlanResponse", "backupRecoveryRetainResponse",
		"backupRecoveryTargetRootResponse", "backupRecoveryDowngradeResponse",
	} {
		schema := requireBackupAssetSwaggerDefinition(t, document, prefix+name)
		if _, ok := schema.Properties["schema_version"]; !ok {
			t.Errorf("Swagger response %s omitted schema_version", name)
		}
	}

	planView := requireBackupAssetSwaggerDefinition(t, document,
		"xirang_backend_internal_backupasset_recovery.RecoveryPlanView")
	for _, field := range []string{"selection_digest", "operation_set_digest", "delete_set_digest"} {
		if _, ok := planView.Properties[field]; !ok {
			t.Errorf("safe plan Swagger omitted public summary %q", field)
		}
	}
	for _, forbidden := range []string{
		"binding_digest", "source_revision_digest", "root_locator_digest", "path_digest",
		"security_decision_digest", "credential_scope_revision",
	} {
		if _, ok := planView.Properties[forbidden]; ok {
			t.Errorf("safe plan Swagger exposed private field %q", forbidden)
		}
	}

	preflightView := requireBackupAssetSwaggerDefinition(t, document,
		"xirang_backend_internal_backupasset_recovery.RecoveryPreflightView")
	for _, field := range []string{
		"schema_version", "plan_id", "persisted", "plan_revision", "eligible", "preferred", "reasons",
		"preflight_id", "preflight_revision", "target_mode", "conflict_policy", "operation_set_digest",
		"delete_set_digest", "impact", "security", "observed_at", "expires_at",
	} {
		if _, ok := preflightView.Properties[field]; !ok {
			t.Errorf("safe preflight Swagger omitted %q", field)
		}
	}
	for _, forbidden := range []string{
		"evaluation", "snapshot", "node_revision", "source_revision_digest", "root_locator_digest",
		"path_digest", "target_revision", "root_revision", "filesystem_revision", "credential_revision",
		"capability_revision", "policy_revision", "finding_set_digest", "security_decision_digest", "rows",
	} {
		if _, ok := preflightView.Properties[forbidden]; ok {
			t.Errorf("safe preflight Swagger exposed private field %q", forbidden)
		}
	}
	impactView := requireBackupAssetSwaggerDefinition(t, document,
		"xirang_backend_internal_backupasset_recovery.RecoveryPreflightImpactView")
	if _, ok := impactView.Properties["rows"]; ok {
		t.Error("safe preflight impact Swagger exposed exact rows")
	}
	retainView := requireBackupAssetSwaggerDefinition(t, document, prefix+"backupRecoveryRetainResponse")
	for _, camel := range []string{"resultSetID", "jobID", "jobRevision", "plaintextDeadline", "hardDeadline"} {
		if _, ok := retainView.Properties[camel]; ok {
			t.Errorf("retain Swagger exposed camelCase domain field %q", camel)
		}
	}
}

func backupRecoverySwaggerResponseDataRef(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var response struct {
		Schema struct {
			AllOf []struct {
				Properties map[string]struct {
					Ref string `json:"$ref"`
				} `json:"properties"`
			} `json:"allOf"`
		} `json:"schema"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode Swagger response: %v", err)
	}
	for _, item := range response.Schema.AllOf {
		if data, ok := item.Properties["data"]; ok {
			return data.Ref
		}
	}
	return ""
}

func TestRecoveryAuthorizationReceiptWriteAuthorizeReplayAndConflict(t *testing.T) {
	testRecoveryAuthorizationReceiptHandlerReplayAndConflict(t, recovery.AuthorizationReceiptWriteAuthorize)
}

func TestRecoveryAuthorizationReceiptDeleteAuthorizeReplayAndConflict(t *testing.T) {
	testRecoveryAuthorizationReceiptHandlerReplayAndConflict(t, recovery.AuthorizationReceiptDeleteAuthorize)
}

func TestRecoveryAuthorizationReceiptExecuteReplayAndConflict(t *testing.T) {
	testRecoveryAuthorizationReceiptHandlerReplayAndConflict(t, recovery.AuthorizationReceiptExecute)
}

func TestRecoveryTargetRootAndDowngradeHandlersDelegateSafeProducts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, jwtManager, user, proof := newBackupRecoveryAuthorizationHandlerProofFixture(t)
	targetRoots := &backupRecoveryTargetRootServiceFake{
		registerResult: settings.RecoveryTargetRootSummary{NodeID: 7, RootID: "recovery-root", SafeLabel: "隔离恢复区"},
		listResult:     []settings.RecoveryTargetRootSummary{{NodeID: 7, RootID: "recovery-root", SafeLabel: "隔离恢复区"}},
	}
	downgrade := &backupRecoveryDowngradeServiceFake{result: backupruntime.RecoveryDowngradeReadiness{
		State:               backupruntime.RecoveryDowngradeBlocked,
		AdmissionGeneration: "recovery-downgrade-" + backupRecoveryHandlerFakeOpaqueID("downgrade-generation"),
		Blockers:            backupruntime.RecoveryDowngradeBlockers{Jobs: 1},
	}}
	handler := NewBackupRecoveryHandler(nil, db, jwtManager).WithRecoveryAdministration(targetRoots, downgrade)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, user.ID)
		c.Set(middleware.CtxUsername, user.Username)
		c.Set(middleware.CtxRole, user.Role)
		c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
			JTI: backupRecoveryHandlerFakeOpaqueID("target-root-session"), UserID: user.ID,
			Role: user.Role, TokenVersion: user.TokenVersion, ExpiresAt: time.Now().UTC().Add(time.Hour),
		})
		c.Next()
	})
	router.POST("/api/v1/settings/backup-assets/recovery/target-roots", handler.RegisterTargetRoot)
	router.PUT("/api/v1/settings/backup-assets/recovery/target-roots/:nodeId/:rootId", handler.RotateTargetRoot)
	router.DELETE("/api/v1/settings/backup-assets/recovery/target-roots/:nodeId/:rootId", handler.DeleteTargetRoot)
	router.GET("/api/v1/settings/backup-assets/recovery/target-roots", handler.ListTargetRoots)
	router.POST("/api/v1/settings/backup-assets/recovery/downgrade-readiness", handler.DowngradeReadiness)

	registerBody := `{"schema_version":1,"node_id":7,"root_id":"recovery-root","safe_label":"隔离恢复区",` +
		`"locator":"FAKE_RECOVERY_RAW_TARGET_ROOT_FOR_TEST_ONLY","reserve_bytes":4096,"reserve_inodes":32,` +
		`"overlap_policy_binding":"recovery-overlap-policy-v1"}`
	register := httptest.NewRequest(http.MethodPost, "/api/v1/settings/backup-assets/recovery/target-roots", strings.NewReader(registerBody))
	register.Header.Set("Content-Type", "application/json")
	register.Header.Set("Idempotency-Key", "target-root-register-key")
	register.Header.Set(StepUpHeaderName, proof)
	registerResponse := httptest.NewRecorder()
	router.ServeHTTP(registerResponse, register)
	if registerResponse.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", registerResponse.Code, registerResponse.Body.String())
	}
	if !strings.Contains(registerResponse.Body.String(), `"schema_version":1`) {
		t.Fatalf("register response omitted schema version: %s", registerResponse.Body.String())
	}
	if len(targetRoots.registerRequests) != 1 || targetRoots.registerRequests[0].Locator != "FAKE_RECOVERY_RAW_TARGET_ROOT_FOR_TEST_ONLY" {
		t.Fatalf("register requests=%+v", targetRoots.registerRequests)
	}
	if targetRoots.registerRequests[0].Mutation != recovery.TargetRootMutationRegister ||
		targetRoots.registerRequests[0].RequesterID != user.ID ||
		targetRoots.registerRequests[0].Endpoint != "/api/v1/settings/backup-assets/recovery/target-roots" ||
		targetRoots.registerRequests[0].IdempotencyKey != "target-root-register-key" ||
		targetRoots.registerRequests[0].SessionJTI == "" ||
		targetRoots.registerRequests[0].SessionTokenVersion != user.TokenVersion {
		t.Fatalf("register mutation authority=%+v", targetRoots.registerRequests[0])
	}
	if strings.Contains(registerResponse.Body.String(), "FAKE_RECOVERY_RAW_TARGET_ROOT_FOR_TEST_ONLY") ||
		strings.Contains(registerResponse.Body.String(), "recovery-overlap-policy-v1") {
		t.Fatalf("register response leaked private root authority: %s", registerResponse.Body.String())
	}
	targetRoots.replayRegisterFound = true
	targetRoots.replayRegisterResult = targetRoots.registerResult
	replayRegister := httptest.NewRequest(
		http.MethodPost, "/api/v1/settings/backup-assets/recovery/target-roots", strings.NewReader(registerBody),
	)
	replayRegister.Header.Set("Content-Type", "application/json")
	replayRegister.Header.Set("Idempotency-Key", "target-root-register-key")
	replayRegisterResponse := httptest.NewRecorder()
	router.ServeHTTP(replayRegisterResponse, replayRegister)
	if replayRegisterResponse.Code != http.StatusOK || len(targetRoots.registerRequests) != 1 ||
		len(targetRoots.replayRegisterRequests) != 2 {
		t.Fatalf("receipt-first register replay status=%d replay=%d mutation=%d body=%s",
			replayRegisterResponse.Code, len(targetRoots.replayRegisterRequests),
			len(targetRoots.registerRequests), replayRegisterResponse.Body.String())
	}
	targetRoots.replayRegisterFound = false

	targetRoots.registerResult = settings.RecoveryTargetRootSummary{NodeID: 7, RootID: "recovery-root", SafeLabel: "轮换恢复区"}
	rotateBody := `{"schema_version":1,"safe_label":"轮换恢复区",` +
		`"locator":"FAKE_ROTATED_RECOVERY_ROOT_FOR_TEST_ONLY","reserve_bytes":8192,"reserve_inodes":64,` +
		`"overlap_policy_binding":"recovery-overlap-policy-v2"}`
	rotate := httptest.NewRequest(http.MethodPut,
		"/api/v1/settings/backup-assets/recovery/target-roots/7/recovery-root", strings.NewReader(rotateBody))
	rotate.Header.Set("Content-Type", "application/json")
	rotate.Header.Set("Idempotency-Key", "target-root-rotate-key")
	rotate.Header.Set(StepUpHeaderName, proof)
	rotateResponse := httptest.NewRecorder()
	router.ServeHTTP(rotateResponse, rotate)
	if rotateResponse.Code != http.StatusOK || len(targetRoots.registerRequests) != 2 {
		t.Fatalf("rotate status=%d requests=%+v body=%s", rotateResponse.Code, targetRoots.registerRequests, rotateResponse.Body.String())
	}
	if !strings.Contains(rotateResponse.Body.String(), `"schema_version":1`) {
		t.Fatalf("rotate response omitted schema version: %s", rotateResponse.Body.String())
	}
	rotated := targetRoots.registerRequests[1]
	if rotated.Mutation != recovery.TargetRootMutationRotate || rotated.NodeID != 7 || rotated.RootID != "recovery-root" ||
		rotated.IdempotencyKey != "target-root-rotate-key" || rotated.Locator != "FAKE_ROTATED_RECOVERY_ROOT_FOR_TEST_ONLY" {
		t.Fatalf("rotate mutation=%+v", rotated)
	}
	if strings.Contains(rotateResponse.Body.String(), "FAKE_ROTATED_RECOVERY_ROOT_FOR_TEST_ONLY") ||
		strings.Contains(rotateResponse.Body.String(), "recovery-overlap-policy-v2") {
		t.Fatalf("rotate response leaked private root authority: %s", rotateResponse.Body.String())
	}

	deleteRequest := httptest.NewRequest(http.MethodDelete,
		"/api/v1/settings/backup-assets/recovery/target-roots/7/recovery-root", strings.NewReader(`{"schema_version":1}`))
	deleteRequest.Header.Set("Content-Type", "application/json")
	deleteRequest.Header.Set("Idempotency-Key", "target-root-delete-key")
	deleteRequest.Header.Set(StepUpHeaderName, proof)
	deleteResponse := httptest.NewRecorder()
	router.ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusOK || len(targetRoots.deleteRequests) != 1 {
		t.Fatalf("delete status=%d requests=%+v body=%s", deleteResponse.Code, targetRoots.deleteRequests, deleteResponse.Body.String())
	}
	if !strings.Contains(deleteResponse.Body.String(), `"schema_version":1`) {
		t.Fatalf("delete response omitted schema version: %s", deleteResponse.Body.String())
	}
	deleted := targetRoots.deleteRequests[0]
	if deleted.RequesterID != user.ID || deleted.NodeID != 7 || deleted.RootID != "recovery-root" ||
		deleted.Endpoint != "/api/v1/settings/backup-assets/recovery/target-roots/:nodeId/:rootId" ||
		deleted.IdempotencyKey != "target-root-delete-key" || deleted.SessionJTI == "" {
		t.Fatalf("delete mutation=%+v", deleted)
	}
	targetRoots.replayDeleteFound = true
	targetRoots.replayDeleteResult = targetRoots.deleteResult
	replayDelete := httptest.NewRequest(http.MethodDelete,
		"/api/v1/settings/backup-assets/recovery/target-roots/7/recovery-root", strings.NewReader(`{"schema_version":1}`))
	replayDelete.Header.Set("Content-Type", "application/json")
	replayDelete.Header.Set("Idempotency-Key", "target-root-delete-key")
	replayDeleteResponse := httptest.NewRecorder()
	router.ServeHTTP(replayDeleteResponse, replayDelete)
	if replayDeleteResponse.Code != http.StatusOK || len(targetRoots.deleteRequests) != 1 ||
		len(targetRoots.replayDeleteRequests) != 2 {
		t.Fatalf("receipt-first delete replay status=%d replay=%d mutation=%d body=%s",
			replayDeleteResponse.Code, len(targetRoots.replayDeleteRequests),
			len(targetRoots.deleteRequests), replayDeleteResponse.Body.String())
	}
	targetRoots.replayDeleteFound = false

	list := httptest.NewRequest(http.MethodGet, "/api/v1/settings/backup-assets/recovery/target-roots?node_id=7", nil)
	listResponse := httptest.NewRecorder()
	router.ServeHTTP(listResponse, list)
	if listResponse.Code != http.StatusOK || len(targetRoots.listNodeIDs) != 1 || targetRoots.listNodeIDs[0] != 7 {
		t.Fatalf("list status=%d nodes=%v body=%s", listResponse.Code, targetRoots.listNodeIDs, listResponse.Body.String())
	}

	readinessBody := `{"schema_version":1,"reason":"FAKE_PRIVATE_DOWNGRADE_REASON_FOR_TEST_ONLY"}`
	readiness := httptest.NewRequest(http.MethodPost, "/api/v1/settings/backup-assets/recovery/downgrade-readiness", strings.NewReader(readinessBody))
	readiness.Header.Set("Content-Type", "application/json")
	readiness.Header.Set("Idempotency-Key", "downgrade-readiness-key")
	readiness.Header.Set(StepUpHeaderName, proof)
	readinessResponse := httptest.NewRecorder()
	router.ServeHTTP(readinessResponse, readiness)
	if readinessResponse.Code != http.StatusOK || len(downgrade.requests) != 1 ||
		!strings.Contains(readinessResponse.Body.String(), string(backupruntime.RecoveryDowngradeBlocked)) {
		t.Fatalf("readiness status=%d calls=%d body=%s", readinessResponse.Code, len(downgrade.requests), readinessResponse.Body.String())
	}
	if !strings.Contains(readinessResponse.Body.String(), `"schema_version":1`) {
		t.Fatalf("downgrade response omitted schema version: %s", readinessResponse.Body.String())
	}
	if downgrade.requests[0].Reason != "FAKE_PRIVATE_DOWNGRADE_REASON_FOR_TEST_ONLY" ||
		strings.Contains(readinessResponse.Body.String(), downgrade.requests[0].Reason) {
		t.Fatalf("readiness request/response leaked or lost reason: request=%+v body=%s", downgrade.requests[0], readinessResponse.Body.String())
	}
	downgrade.replayFound = true
	downgrade.replayResult = downgrade.result
	replayReadiness := httptest.NewRequest(http.MethodPost,
		"/api/v1/settings/backup-assets/recovery/downgrade-readiness", strings.NewReader(readinessBody))
	replayReadiness.Header.Set("Content-Type", "application/json")
	replayReadiness.Header.Set("Idempotency-Key", "downgrade-readiness-key")
	replayReadinessResponse := httptest.NewRecorder()
	router.ServeHTTP(replayReadinessResponse, replayReadiness)
	if replayReadinessResponse.Code != http.StatusOK || len(downgrade.requests) != 1 || len(downgrade.replayRequests) != 2 {
		t.Fatalf("receipt-first readiness replay status=%d replay=%d mutation=%d body=%s",
			replayReadinessResponse.Code, len(downgrade.replayRequests), len(downgrade.requests),
			replayReadinessResponse.Body.String())
	}
}

func TestBackupRecoveryClosedLifecycleResponseMatrixSanitizesUnexpectedFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, testCase := range []struct {
		name   string
		err    error
		status int
	}{
		{name: "bad request", err: recovery.ErrInvalidRecoveryPlan, status: http.StatusBadRequest},
		{name: "invalid exact selection", err: recovery.ErrInvalidExactSelection, status: http.StatusBadRequest},
		{name: "forbidden", err: recovery.ErrRecoveryResultRetainDenied, status: http.StatusForbidden},
		{name: "hidden not found", err: recovery.ErrRecoveryAPIObjectNotFound, status: http.StatusNotFound},
		{name: "conflict", err: recovery.ErrRecoveryAPIConflict, status: http.StatusConflict},
		{name: "plan idempotency conflict", err: recovery.ErrPlanIdempotencyConflict, status: http.StatusConflict},
		{name: "source drift", err: recovery.ErrRecoverySourceChanged, status: http.StatusConflict},
		{name: "target drift", err: recovery.ErrRecoveryTargetChanged, status: http.StatusConflict},
		{name: "selection limit", err: recovery.ErrExactSelectionLimit, status: http.StatusRequestEntityTooLarge},
		{name: "operation limit", err: recovery.ErrRecoveryOperationLimit, status: http.StatusRequestEntityTooLarge},
		{name: "impact limit", err: recovery.ErrRecoveryImpactLimit, status: http.StatusRequestEntityTooLarge},
		{name: "unavailable", err: recovery.ErrRecoveryAPIUnavailable, status: http.StatusServiceUnavailable},
		{name: "preflight unavailable", err: recovery.ErrTargetPreflightUnavailable, status: http.StatusServiceUnavailable},
		{name: "source unavailable", err: recovery.ErrRecoverySourceUnavailable, status: http.StatusServiceUnavailable},
		{name: "target unavailable", err: recovery.ErrRecoveryTargetUnavailable, status: http.StatusServiceUnavailable},
		{name: "processing unavailable", err: backupasset.ErrCapabilityUnavailable, status: http.StatusServiceUnavailable},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = httptest.NewRequest(http.MethodPost, "/api/v1/recovery-plans", nil)
			respondBackupRecoveryLifecycleError(context, testCase.err)
			if response.Code != testCase.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, testCase.status, response.Body.String())
			}
			var envelope Response
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Code != testCase.status {
				t.Fatalf("envelope=%+v err=%v body=%s", envelope, err, response.Body.String())
			}
		})
	}

	privateMarker := "FAKE_PRIVATE_PROVIDER_FAILURE_MARKER_FOR_TEST_ONLY"
	var logCapture bytes.Buffer
	previousLogger := applogger.Log
	applogger.Log = zerolog.New(&logCapture)
	t.Cleanup(func() { applogger.Log = previousLogger })
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/v1/recovery-plans", nil)
	respondBackupRecoveryLifecycleError(context, errors.New(privateMarker))
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), privateMarker) ||
		strings.Contains(logCapture.String(), privateMarker) {
		t.Fatalf("unexpected failure was not sanitized: status=%d body=%s log=%s",
			response.Code, response.Body.String(), logCapture.String())
	}
}

func TestRecoveryPlanAndJobReadCancelHandlersDelegateOwnershipToRecovery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &backupRecoveryLifecycleServiceFake{
		plan: recovery.RecoveryPlanView{
			SchemaVersion: 1, ID: strings.Repeat("a", 32), State: recovery.PlanStateDraft, Revision: "7",
			RepositoryID: strings.Repeat("b", 32), RecoveryPointID: strings.Repeat("c", 32),
			TargetMode: recovery.TargetModeIsolated, TargetNodeID: 9, TargetRootID: "recovery-root",
			ConflictPolicy: recovery.ConflictFailOnConflict, Security: recovery.SecurityDecisionAllowClean,
		},
		job: recovery.RecoveryJobView{
			SchemaVersion: 1, ID: strings.Repeat("d", 32), PlanID: strings.Repeat("a", 32),
			State: recovery.JobStateQueued, Revision: "3", TargetMode: recovery.TargetModeIsolated,
			TargetNodeID: 9, TargetRootID: "recovery-root",
		},
	}
	handler := NewBackupRecoveryHandler(nil, nil, nil).WithRecoveryLifecycle(service)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(17))
		c.Set(middleware.CtxRole, "admin")
		c.Next()
	})
	router.GET("/api/v1/recovery-plans/:id", handler.GetPlan)
	router.POST("/api/v1/recovery-plans/:id/cancel", handler.CancelPlan)
	router.GET("/api/v1/recovery-jobs/:id", handler.GetJob)

	planRequest := httptest.NewRequest(http.MethodGet, "/api/v1/recovery-plans/"+strings.Repeat("a", 32), nil)
	planResponse := httptest.NewRecorder()
	router.ServeHTTP(planResponse, planRequest)
	if planResponse.Code != http.StatusOK || len(service.planReads) != 1 || service.planReads[0].RequesterID != 17 {
		t.Fatalf("plan read status=%d requests=%+v body=%s", planResponse.Code, service.planReads, planResponse.Body.String())
	}
	aliasedPlanRequest := httptest.NewRequest(
		http.MethodGet, "/api/v1/recovery-plans/%20"+strings.Repeat("a", 32)+"%20", nil,
	)
	aliasedPlanResponse := httptest.NewRecorder()
	router.ServeHTTP(aliasedPlanResponse, aliasedPlanRequest)
	if aliasedPlanResponse.Code != http.StatusBadRequest || len(service.planReads) != 1 {
		t.Fatalf("whitespace-aliased plan status=%d requests=%+v body=%s",
			aliasedPlanResponse.Code, service.planReads, aliasedPlanResponse.Body.String())
	}

	cancelRequest := httptest.NewRequest(http.MethodPost, "/api/v1/recovery-plans/"+strings.Repeat("a", 32)+"/cancel",
		strings.NewReader(`{"schema_version":1,"expected_revision":"7"}`))
	cancelRequest.Header.Set("Content-Type", "application/json")
	cancelResponse := httptest.NewRecorder()
	router.ServeHTTP(cancelResponse, cancelRequest)
	if cancelResponse.Code != http.StatusOK || len(service.planMutations) != 1 ||
		service.planMutations[0].RequesterID != 17 || service.planMutations[0].ExpectedRevision != 7 {
		t.Fatalf("plan cancel status=%d requests=%+v body=%s", cancelResponse.Code, service.planMutations, cancelResponse.Body.String())
	}

	jobRequest := httptest.NewRequest(http.MethodGet, "/api/v1/recovery-jobs/"+strings.Repeat("d", 32), nil)
	jobResponse := httptest.NewRecorder()
	router.ServeHTTP(jobResponse, jobRequest)
	if jobResponse.Code != http.StatusOK || len(service.jobReads) != 1 || service.jobReads[0].RequesterID != 17 {
		t.Fatalf("job read status=%d requests=%+v body=%s", jobResponse.Code, service.jobReads, jobResponse.Body.String())
	}
	for _, private := range []string{"locator", "path", "reason", "proof", "grant_secret"} {
		if strings.Contains(strings.ToLower(planResponse.Body.String()+jobResponse.Body.String()), private) {
			t.Fatalf("safe views exposed private field %q: plan=%s job=%s", private, planResponse.Body.String(), jobResponse.Body.String())
		}
	}
}

func TestRecoveryJobCollectionHandlersParseCanonicalServerPage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	jobID := strings.Repeat("d", 32)
	service := &backupRecoveryLifecycleServiceFake{
		itemPage: recovery.RecoveryJobItemPage{
			SchemaVersion: 1, JobID: jobID, Page: 2, PageSize: 25, Total: 30,
			Items: []recovery.RecoveryJobItemView{},
		},
		resultPage: recovery.RecoveryResultPage{
			SchemaVersion: 1, JobID: jobID, Page: 1, PageSize: 10, Total: 1,
			Items: []recovery.RecoveryResultView{},
		},
	}
	handler := NewBackupRecoveryHandler(nil, nil, nil).WithRecoveryLifecycle(service)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(middleware.CtxUserID, uint(17)); c.Next() })
	router.GET("/api/v1/recovery-jobs/:id/items", handler.GetJobItems)
	router.GET("/api/v1/recovery-jobs/:id/results", handler.GetJobResults)

	items := httptest.NewRecorder()
	router.ServeHTTP(items, httptest.NewRequest(http.MethodGet,
		"/api/v1/recovery-jobs/"+jobID+"/items?page=2&page_size=25", nil))
	if items.Code != http.StatusOK || len(service.itemPageReads) != 1 ||
		service.itemPageReads[0].RequesterID != 17 || service.itemPageReads[0].Page.Page != 2 ||
		service.itemPageReads[0].Page.PageSize != 25 {
		t.Fatalf("item page status=%d calls=%+v body=%s", items.Code, service.itemPageReads, items.Body.String())
	}
	results := httptest.NewRecorder()
	router.ServeHTTP(results, httptest.NewRequest(http.MethodGet,
		"/api/v1/recovery-jobs/"+jobID+"/results?page_size=10", nil))
	if results.Code != http.StatusOK || len(service.resultPageReads) != 1 ||
		service.resultPageReads[0].Page.Page != 0 || service.resultPageReads[0].Page.PageSize != 10 {
		t.Fatalf("result page status=%d calls=%+v body=%s", results.Code, service.resultPageReads, results.Body.String())
	}
	invalid := httptest.NewRecorder()
	router.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet,
		"/api/v1/recovery-jobs/"+jobID+"/items?page=01", nil))
	if invalid.Code != http.StatusBadRequest || len(service.itemPageReads) != 1 {
		t.Fatalf("noncanonical page status=%d calls=%+v body=%s", invalid.Code, service.itemPageReads, invalid.Body.String())
	}
}

func TestRecoveryMutationHandlersDelegateClosedCommandsToLiveRecoveryFacade(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, jwtManager, user, _ := newBackupRecoveryAuthorizationHandlerProofFixture(t)
	retainProof, _, err := jwtManager.GenerateStepUpToken(user, auth.StepUpActionRecoveryResultRetain)
	if err != nil {
		t.Fatal(err)
	}
	service := &backupRecoveryLifecycleServiceFake{
		planCreate: recovery.CreatePlanResult{PlanID: strings.Repeat("a", 32), State: recovery.PlanStateDraft},
		preflight: recovery.RecoveryPreflightView{
			SchemaVersion: 1, PlanID: strings.Repeat("a", 32), Persisted: true, PlanRevision: "2",
			Eligible: true, Reasons: []recovery.TargetPreflightReason{},
			PreflightID: strings.Repeat("9", 32), PreflightRevision: "preflight-revision-1",
			TargetMode: recovery.TargetModeIsolated, ConflictPolicy: recovery.ConflictFailOnConflict,
			OperationSetDigest: strings.Repeat("6", 64), DeleteSetDigest: strings.Repeat("5", 64),
			Impact: recovery.RecoveryPreflightImpactView{
				CreateCount: 1, EstimatedItems: 1, EstimatedBytes: 512,
			},
			Security: recovery.RecoveryPreflightSecurityView{Decision: recovery.SecurityDecisionAllowClean},
		},
		job: recovery.RecoveryJobView{
			SchemaVersion: 1, ID: strings.Repeat("d", 32), PlanID: strings.Repeat("a", 32),
			State: recovery.JobStateCanceled, Revision: "4", TargetMode: recovery.TargetModeIsolated,
			TargetNodeID: 9, TargetRootID: "recovery-root",
		},
		retained: recovery.RetainedRecoveryResultSet{
			ResultSetID: strings.Repeat("e", 32), JobID: strings.Repeat("d", 32), JobRevision: 3,
			PlaintextDeadline: time.Now().UTC().Add(time.Hour), HardDeadline: time.Now().UTC().Add(2 * time.Hour),
		},
		cleanup: recovery.RecoveryResultCleanupView{
			SchemaVersion: 1, JobID: strings.Repeat("d", 32), ResultSetID: strings.Repeat("e", 32),
			State: recovery.ResultSetStateReady, ScheduledAt: time.Now().UTC(),
		},
	}
	handler := NewBackupRecoveryHandler(nil, db, jwtManager).
		WithRecoveryLifecycle(service).
		WithRecoveryOperations(service)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, user.ID)
		c.Set(middleware.CtxUsername, user.Username)
		c.Set(middleware.CtxRole, user.Role)
		c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
			JTI: strings.Repeat("9", 32), UserID: user.ID, Role: user.Role,
			TokenVersion: user.TokenVersion, ExpiresAt: time.Now().UTC().Add(time.Hour),
		})
		c.Next()
	})
	router.POST("/api/v1/recovery-plans", handler.CreatePlan)
	router.POST("/api/v1/recovery-plans/:id/preflights", handler.Preflight)
	router.POST("/api/v1/recovery-jobs/:id/cancel", handler.CancelJob)
	router.POST("/api/v1/recovery-jobs/:id/results/retain", handler.RetainResults)
	router.POST("/api/v1/recovery-jobs/:id/results/cleanup", handler.CleanupResults)

	create := httptest.NewRequest(http.MethodPost, "/api/v1/recovery-plans", strings.NewReader(
		`{"schema_version":1,"repository_id":"`+strings.Repeat("b", 32)+`","recovery_point_id":"`+
			strings.Repeat("c", 32)+`","catalog_generation_id":"`+strings.Repeat("e", 32)+
			`","entry_ids":["`+strings.Repeat("f", 64)+`"],"target_mode":"isolated","target_node_id":9,`+
			`"target_root_id":"recovery-root","conflict_policy":"fail_on_conflict"}`))
	create.Header.Set("Content-Type", "application/json")
	create.Header.Set("Idempotency-Key", "recovery-plan-create-key")
	createResponse := httptest.NewRecorder()
	router.ServeHTTP(createResponse, create)
	if createResponse.Code != http.StatusCreated || len(service.planCreates) != 1 ||
		service.planCreates[0].RequesterID != user.ID || service.planCreates[0].IdempotencyKey != "recovery-plan-create-key" ||
		service.planCreates[0].RepositoryID != strings.Repeat("b", 32) ||
		service.planCreates[0].RecoveryPointID != strings.Repeat("c", 32) ||
		service.planCreates[0].CatalogGenerationID != strings.Repeat("e", 32) ||
		len(service.planCreates[0].EntryIDs) != 1 ||
		service.planCreates[0].EntryIDs[0] != strings.Repeat("f", 64) ||
		service.planCreates[0].TargetMode != recovery.TargetModeIsolated ||
		service.planCreates[0].ConflictPolicy != recovery.ConflictFailOnConflict {
		t.Fatalf("create status=%d requests=%+v body=%s", createResponse.Code, service.planCreates, createResponse.Body.String())
	}
	if !strings.Contains(createResponse.Body.String(), `"schema_version":1`) {
		t.Fatalf("create response omitted schema version: %s", createResponse.Body.String())
	}
	maximumEntries := make([]string, 10_000)
	for index := range maximumEntries {
		maximumEntries[index] = fmt.Sprintf("%064x", index)
	}
	maximumBody, err := json.Marshal(backupRecoveryCreatePlanPayload{
		SchemaVersion: 1, RepositoryID: strings.Repeat("b", 32), RecoveryPointID: strings.Repeat("c", 32),
		CatalogGenerationID: strings.Repeat("e", 32), EntryIDs: maximumEntries,
		TargetMode: recovery.TargetModeIsolated, TargetNodeID: 9, TargetRootID: "recovery-root",
		ConflictPolicy: recovery.ConflictFailOnConflict,
	})
	if err != nil {
		t.Fatal(err)
	}
	maximum := httptest.NewRequest(http.MethodPost, "/api/v1/recovery-plans", bytes.NewReader(maximumBody))
	maximum.Header.Set("Content-Type", "application/json")
	maximum.Header.Set("Idempotency-Key", "recovery-plan-maximum-selection-key")
	maximumResponse := httptest.NewRecorder()
	router.ServeHTTP(maximumResponse, maximum)
	if maximumResponse.Code != http.StatusCreated || len(service.planCreates) != 2 {
		t.Fatalf("maximum selection status=%d requests=%d body=%s",
			maximumResponse.Code, len(service.planCreates), maximumResponse.Body.String())
	}
	if len(service.planCreates[1].EntryIDs) != len(maximumEntries) {
		t.Fatalf("maximum selection entries=%d want=%d", len(service.planCreates[1].EntryIDs), len(maximumEntries))
	}
	overLimitBody, err := json.Marshal(backupRecoveryCreatePlanPayload{
		SchemaVersion: 1, RepositoryID: strings.Repeat("b", 32), RecoveryPointID: strings.Repeat("c", 32),
		CatalogGenerationID: strings.Repeat("e", 32), EntryIDs: append(maximumEntries, strings.Repeat("f", 64)),
		TargetMode: recovery.TargetModeIsolated, TargetNodeID: 9, TargetRootID: "recovery-root",
		ConflictPolicy: recovery.ConflictFailOnConflict,
	})
	if err != nil {
		t.Fatal(err)
	}
	overLimit := httptest.NewRequest(http.MethodPost, "/api/v1/recovery-plans", bytes.NewReader(overLimitBody))
	overLimit.Header.Set("Content-Type", "application/json")
	overLimit.Header.Set("Idempotency-Key", "recovery-plan-over-selection-key")
	overLimitResponse := httptest.NewRecorder()
	router.ServeHTTP(overLimitResponse, overLimit)
	if overLimitResponse.Code != http.StatusRequestEntityTooLarge || len(service.planCreates) != 2 {
		t.Fatalf("over-limit selection status=%d requests=%d body=%s",
			overLimitResponse.Code, len(service.planCreates), overLimitResponse.Body.String())
	}
	legacyPrivateCreate := httptest.NewRequest(http.MethodPost, "/api/v1/recovery-plans", strings.NewReader(
		`{"schema_version":1,"selection":{},"plan":{},"authority_category":"write","estimated_items":1,"estimated_bytes":2}`))
	legacyPrivateCreate.Header.Set("Content-Type", "application/json")
	legacyPrivateCreate.Header.Set("Idempotency-Key", "recovery-plan-private-key")
	legacyPrivateResponse := httptest.NewRecorder()
	router.ServeHTTP(legacyPrivateResponse, legacyPrivateCreate)
	if legacyPrivateResponse.Code != http.StatusBadRequest || len(service.planCreates) != 2 {
		t.Fatalf("private create status=%d requests=%+v body=%s",
			legacyPrivateResponse.Code, service.planCreates, legacyPrivateResponse.Body.String())
	}

	preflight := httptest.NewRequest(http.MethodPost, "/api/v1/recovery-plans/"+strings.Repeat("a", 32)+"/preflights",
		strings.NewReader(`{"schema_version":1,"expected_revision":"1"}`))
	preflight.Header.Set("Content-Type", "application/json")
	preflightResponse := httptest.NewRecorder()
	router.ServeHTTP(preflightResponse, preflight)
	if preflightResponse.Code != http.StatusOK || len(service.preflights) != 1 ||
		service.preflights[0].RequesterID != user.ID || service.preflights[0].ExpectedPlanRevision != 1 {
		t.Fatalf("preflight status=%d requests=%+v body=%s", preflightResponse.Code, service.preflights, preflightResponse.Body.String())
	}
	for _, private := range []string{
		"FAKE_PRIVATE_PREFLIGHT_NODE_REVISION_FOR_TEST_ONLY",
		"FAKE_PRIVATE_PREFLIGHT_CREDENTIAL_FOR_TEST_ONLY",
		"FAKE_PRIVATE_PREFLIGHT_TARGET_PATH_FOR_TEST_ONLY",
		"source_revision_digest", "credential_revision", "target_path_digest", "rows",
	} {
		if strings.Contains(preflightResponse.Body.String(), private) {
			t.Fatalf("preflight response leaked %q: %s", private, preflightResponse.Body.String())
		}
	}
	if !strings.Contains(preflightResponse.Body.String(), `"schema_version":1`) ||
		!strings.Contains(preflightResponse.Body.String(), `"operation_set_digest":"`+strings.Repeat("6", 64)+`"`) {
		t.Fatalf("preflight response omitted safe schema/operation summary: %s", preflightResponse.Body.String())
	}
	privatePreflight := httptest.NewRequest(http.MethodPost,
		"/api/v1/recovery-plans/"+strings.Repeat("a", 32)+"/preflights",
		strings.NewReader(`{"schema_version":1,"expected_revision":"1","target_path_digest":"FAKE_PRIVATE_DIGEST"}`))
	privatePreflight.Header.Set("Content-Type", "application/json")
	privatePreflightResponse := httptest.NewRecorder()
	router.ServeHTTP(privatePreflightResponse, privatePreflight)
	if privatePreflightResponse.Code != http.StatusBadRequest || len(service.preflights) != 1 {
		t.Fatalf("private preflight status=%d requests=%+v body=%s",
			privatePreflightResponse.Code, service.preflights, privatePreflightResponse.Body.String())
	}

	jobID := strings.Repeat("d", 32)
	cancel := httptest.NewRequest(http.MethodPost, "/api/v1/recovery-jobs/"+jobID+"/cancel",
		strings.NewReader(`{"schema_version":1,"expected_revision":"3"}`))
	cancel.Header.Set("Content-Type", "application/json")
	cancelResponse := httptest.NewRecorder()
	router.ServeHTTP(cancelResponse, cancel)
	if cancelResponse.Code != http.StatusOK || len(service.jobCancels) != 1 || service.jobCancels[0].RequesterID != user.ID {
		t.Fatalf("cancel status=%d requests=%+v body=%s", cancelResponse.Code, service.jobCancels, cancelResponse.Body.String())
	}
	aliasedCancel := httptest.NewRequest(http.MethodPost, "/api/v1/recovery-jobs/%20"+jobID+"%20/cancel",
		strings.NewReader(`{"schema_version":1,"expected_revision":"3"}`))
	aliasedCancel.Header.Set("Content-Type", "application/json")
	aliasedCancelResponse := httptest.NewRecorder()
	router.ServeHTTP(aliasedCancelResponse, aliasedCancel)
	if aliasedCancelResponse.Code != http.StatusBadRequest || len(service.jobCancels) != 1 {
		t.Fatalf("whitespace-aliased cancel status=%d requests=%+v body=%s",
			aliasedCancelResponse.Code, service.jobCancels, aliasedCancelResponse.Body.String())
	}

	retain := httptest.NewRequest(http.MethodPost, "/api/v1/recovery-jobs/"+jobID+"/results/retain",
		strings.NewReader(`{"schema_version":1,"expected_revision":"3","requested_deadline":"2030-01-02T03:04:05Z"}`))
	retain.Header.Set("Content-Type", "application/json")
	retain.Header.Set(StepUpHeaderName, retainProof)
	retainResponse := httptest.NewRecorder()
	router.ServeHTTP(retainResponse, retain)
	if retainResponse.Code != http.StatusOK || len(service.retains) != 1 ||
		service.retains[0].Actor.UserID != user.ID || service.retains[0].ExpectedJobRevision != 3 ||
		service.retains[0].Proof == nil || service.retains[0].Proof.Action != auth.StepUpActionRecoveryResultRetain {
		t.Fatalf("retain status=%d requests=%+v body=%s", retainResponse.Code, service.retains, retainResponse.Body.String())
	}
	if !strings.Contains(retainResponse.Body.String(), `"schema_version":1`) ||
		!strings.Contains(retainResponse.Body.String(), `"result_set_id"`) ||
		strings.Contains(retainResponse.Body.String(), "resultSetID") || strings.Contains(retainResponse.Body.String(), "jobID") {
		t.Fatalf("retain response is not schema-v1 snake_case: %s", retainResponse.Body.String())
	}
	wrappedProofRetain := httptest.NewRequest(http.MethodPost, "/api/v1/recovery-jobs/"+jobID+"/results/retain",
		strings.NewReader(`{"schema_version":1,"expected_revision":"3","requested_deadline":"2030-01-02T03:04:05Z"}`))
	wrappedProofRetain.Header.Set("Content-Type", "application/json")
	wrappedProofRetain.Header.Set(StepUpHeaderName, " "+retainProof+" ")
	wrappedProofResponse := httptest.NewRecorder()
	router.ServeHTTP(wrappedProofResponse, wrappedProofRetain)
	if wrappedProofResponse.Code != http.StatusForbidden || len(service.retains) != 1 {
		t.Fatalf("whitespace-wrapped retain proof status=%d requests=%+v body=%s",
			wrappedProofResponse.Code, service.retains, wrappedProofResponse.Body.String())
	}

	cleanup := httptest.NewRequest(http.MethodPost, "/api/v1/recovery-jobs/"+jobID+"/results/cleanup",
		strings.NewReader(`{"schema_version":1,"expected_revision":"3"}`))
	cleanup.Header.Set("Content-Type", "application/json")
	cleanupResponse := httptest.NewRecorder()
	router.ServeHTTP(cleanupResponse, cleanup)
	if cleanupResponse.Code != http.StatusOK || len(service.cleanups) != 1 || service.cleanups[0].RequesterID != user.ID {
		t.Fatalf("cleanup status=%d requests=%+v body=%s", cleanupResponse.Code, service.cleanups, cleanupResponse.Body.String())
	}
}

func testRecoveryAuthorizationReceiptHandlerReplayAndConflict(
	t *testing.T,
	operation recovery.AuthorizationReceiptOperation,
) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, jwtManager, user, proof := newBackupRecoveryAuthorizationHandlerProofFixture(t)
	service := &backupRecoveryAuthorizationServiceFake{}
	result := backupRecoveryAuthorizationHandlerResult(t, operation)
	service.authorizeResult = result
	handler := NewBackupRecoveryHandler(service, db, jwtManager)
	sessionJTI := backupRecoveryHandlerFakeOpaqueID("presenting-session")
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, user.ID)
		c.Set(middleware.CtxUsername, user.Username)
		c.Set(middleware.CtxRole, user.Role)
		c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
			JTI: sessionJTI, UserID: user.ID, Role: user.Role,
			TokenVersion: user.TokenVersion, ExpiresAt: time.Now().UTC().Add(time.Hour),
		})
		c.Next()
	})
	method, route, path, body, wantStatus := backupRecoveryAuthorizationHandlerCase(operation)
	switch operation {
	case recovery.AuthorizationReceiptSecurityOverride:
		router.POST(route, handler.SecurityOverride)
	case recovery.AuthorizationReceiptWriteAuthorize:
		router.POST(route, handler.AuthorizeWrite)
	case recovery.AuthorizationReceiptDeleteAuthorize:
		router.POST(route, handler.AuthorizeExactMirrorDelete)
	case recovery.AuthorizationReceiptExecute:
		router.POST(route, handler.Execute)
	default:
		t.Fatalf("unsupported handler test operation %q", operation)
	}

	initial := httptest.NewRequest(method, path, strings.NewReader(body))
	initial.Header.Set("Content-Type", "application/json")
	initial.Header.Set("Idempotency-Key", "authorization-receipt-handler-key")
	initial.Header.Set(StepUpHeaderName, proof)
	initialResponse := httptest.NewRecorder()
	router.ServeHTTP(initialResponse, initial)
	if initialResponse.Code != wantStatus {
		t.Fatalf("initial %s status=%d body=%s", operation, initialResponse.Code, initialResponse.Body.String())
	}
	initialPayload := decodeBackupRecoveryAuthorizationResponse(t, initialResponse)
	if len(service.replayRequests) != 1 || len(service.authorizeRequests) != 1 {
		t.Fatalf("initial %s replay/authorize calls=%d/%d, want 1/1", operation,
			len(service.replayRequests), len(service.authorizeRequests))
	}
	if service.replayRequests[0].Proof.JTI != "" {
		t.Fatalf("initial %s replay required proof claims before checking the receipt", operation)
	}
	request := service.authorizeRequests[0]
	if request.Proof.JTI == "" || request.Session.JTI != sessionJTI || request.Proof.JTI == request.Session.JTI ||
		request.Proof.Action != "asset.recover" || request.RequesterID != user.ID || request.Session.UserID != user.ID {
		t.Fatalf("initial %s proof/session composition=%+v/%+v", operation, request.Proof, request.Session)
	}

	service.replayResult = result
	service.replayFound = true
	replay := httptest.NewRequest(method, path, strings.NewReader(body))
	replay.Header.Set("Content-Type", "application/json")
	replay.Header.Set("Idempotency-Key", "authorization-receipt-handler-key")
	replayResponse := httptest.NewRecorder()
	router.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != wantStatus {
		t.Fatalf("proof-free %s replay status=%d body=%s", operation, replayResponse.Code, replayResponse.Body.String())
	}
	if len(service.authorizeRequests) != 1 {
		t.Fatalf("proof-free %s replay called effect service %d times", operation, len(service.authorizeRequests))
	}
	replayPayload := decodeBackupRecoveryAuthorizationResponse(t, replayResponse)
	if initialPayload.SchemaVersion != backupRecoveryAuthorizationSchemaVersion ||
		replayPayload.SchemaVersion != backupRecoveryAuthorizationSchemaVersion {
		t.Fatalf("%s initial/replay schema versions=%d/%d, want %d", operation,
			initialPayload.SchemaVersion, replayPayload.SchemaVersion, backupRecoveryAuthorizationSchemaVersion)
	}
	if initialPayload.Replay || !replayPayload.Replay {
		t.Fatalf("%s initial/replay flags=%t/%t", operation, initialPayload.Replay, replayPayload.Replay)
	}
	replayPayload.Replay = false
	if initialPayload != replayPayload {
		t.Fatalf("%s initial/replay public metadata differ: initial=%+v replay=%+v", operation, initialPayload, replayPayload)
	}
	assertBackupRecoveryAuthorizationGrantMetadata(t, operation, initialPayload)

	var envelope Response
	if err := json.Unmarshal(replayResponse.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(envelope.Data)
	forbiddenValues := []string{
		"FAKE_RECOVERY", proof, sessionJTI, backupRecoveryHandlerFakeGrantSecret("grant"),
		result.AttemptID, result.SourceLeaseID, result.NodeLeaseID,
	}
	for _, forbidden := range forbiddenValues {
		if forbidden == "" {
			continue
		}
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("%s replay response leaked %q: %s", operation, forbidden, encoded)
		}
	}

	service.replayFound = false
	service.replayErr = recovery.ErrAuthorizationIdempotencyConflict
	conflictBody := changedBackupRecoveryAuthorizationHandlerBody(t, operation, body)
	conflict := httptest.NewRequest(method, path, strings.NewReader(conflictBody))
	conflict.Header.Set("Content-Type", "application/json")
	conflict.Header.Set("Idempotency-Key", "authorization-receipt-handler-key")
	conflictResponse := httptest.NewRecorder()
	router.ServeHTTP(conflictResponse, conflict)
	if conflictResponse.Code != http.StatusConflict || len(service.authorizeRequests) != 1 {
		t.Fatalf("changed-intent %s status/calls=%d/%d body=%s", operation,
			conflictResponse.Code, len(service.authorizeRequests), conflictResponse.Body.String())
	}
}

func backupRecoveryAuthorizationHandlerCase(
	operation recovery.AuthorizationReceiptOperation,
) (method, route, path, body string, wantStatus int) {
	planID := backupRecoveryHandlerFakeOpaqueID("plan")
	preflightID := backupRecoveryHandlerFakeOpaqueID("preflight")
	jobID := backupRecoveryHandlerFakeOpaqueID("job")
	checkpointID := backupRecoveryHandlerFakeOpaqueID("checkpoint")
	attemptID := backupRecoveryHandlerFakeOpaqueID("attempt")
	grantID := backupRecoveryHandlerFakeOpaqueID("grant")
	secret := backupRecoveryHandlerFakeGrantSecret("grant")
	switch operation {
	case recovery.AuthorizationReceiptSecurityOverride:
		return http.MethodPost, "/api/v1/recovery-plans/:id/security-overrides",
			"/api/v1/recovery-plans/" + planID + "/security-overrides",
			`{"schema_version":1,"expected_revision":"1","preflight_id":"` + preflightID +
				`","finding_category":"suspicious","reason":"FAKE_RECOVERY_REASON_FOR_TEST_ONLY"}`,
			http.StatusOK
	case recovery.AuthorizationReceiptWriteAuthorize:
		return http.MethodPost, "/api/v1/recovery-plans/:id/write-authorizations",
			"/api/v1/recovery-plans/" + planID + "/write-authorizations",
			`{"schema_version":1,"expected_revision":"1","preflight_id":"` + preflightID +
				`","reason":"FAKE_RECOVERY_REASON_FOR_TEST_ONLY","grant_secret":"` + secret + `"}`,
			http.StatusOK
	case recovery.AuthorizationReceiptDeleteAuthorize:
		return http.MethodPost, "/api/v1/recovery-jobs/:id/exact-mirror-delete-authorizations",
			"/api/v1/recovery-jobs/" + jobID + "/exact-mirror-delete-authorizations",
			`{"schema_version":1,"plan_id":"` + planID + `","checkpoint_id":"` + checkpointID +
				`","attempt_id":"` + attemptID +
				`","expected_revision":"1","reason":"FAKE_RECOVERY_REASON_FOR_TEST_ONLY","grant_secret":"` + secret + `"}`,
			http.StatusOK
	case recovery.AuthorizationReceiptExecute:
		return http.MethodPost, "/api/v1/recovery-plans/:id/execute",
			"/api/v1/recovery-plans/" + planID + "/execute",
			`{"schema_version":1,"expected_revision":"1","preflight_id":"` + preflightID +
				`","grant_id":"` + grantID + `","grant_secret":"` + secret + `"}`,
			http.StatusAccepted
	default:
		return "", "", "", "", 0
	}
}

func backupRecoveryAuthorizationHandlerResult(
	t *testing.T,
	operation recovery.AuthorizationReceiptOperation,
) recovery.RecoveryAuthorizationResult {
	t.Helper()
	result := recovery.RecoveryAuthorizationResult{
		ReceiptID:              backupRecoveryHandlerFakeOpaqueID("receipt"),
		PlanID:                 backupRecoveryHandlerFakeOpaqueID("plan"),
		PlanTransitionRevision: 2,
	}
	switch operation {
	case recovery.AuthorizationReceiptWriteAuthorize:
		result.GrantID = backupRecoveryHandlerFakeOpaqueID("grant")
	case recovery.AuthorizationReceiptDeleteAuthorize:
		result.GrantID = backupRecoveryHandlerFakeOpaqueID("grant")
		result.JobID = backupRecoveryHandlerFakeOpaqueID("job")
		result.AttemptID = backupRecoveryHandlerFakeOpaqueID("attempt")
	case recovery.AuthorizationReceiptExecute:
		result.GrantID = backupRecoveryHandlerFakeOpaqueID("grant")
		result.JobID = backupRecoveryHandlerFakeOpaqueID("job")
		result.AttemptID = backupRecoveryHandlerFakeOpaqueID("attempt")
		result.SourceLeaseID = backupRecoveryHandlerFakeOpaqueID("source-lease")
		result.NodeLeaseID = backupRecoveryHandlerFakeOpaqueID("node-lease")
		result.NodeLeaseFence = 1
	}
	if result.GrantID != "" {
		grantCategory := string(recovery.AuthorityWrite)
		grantStatus := "issued"
		if operation == recovery.AuthorizationReceiptDeleteAuthorize {
			grantCategory = string(recovery.AuthorityExactMirrorDelete)
		}
		if operation == recovery.AuthorizationReceiptExecute {
			grantStatus = "consumed"
		}
		bindingDigest := backupRecoveryHandlerFakeDigest("grant-binding")
		metadata, err := json.Marshal(map[string]any{
			"grant_category":       grantCategory,
			"grant_binding_digest": hex.EncodeToString(bindingDigest[:]),
			"grant_expires_at":     time.Now().UTC().Add(time.Hour),
			"grant_status":         grantStatus,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(metadata, &result); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

type backupRecoveryAuthorizationResponseJSON struct {
	SchemaVersion          int    `json:"schema_version"`
	ReceiptID              string `json:"receipt_id"`
	PlanID                 string `json:"plan_id"`
	GrantID                string `json:"grant_id"`
	JobID                  string `json:"job_id"`
	Operation              string `json:"operation"`
	Category               string `json:"category"`
	GrantCategory          string `json:"grant_category"`
	GrantBindingDigest     string `json:"grant_binding_digest"`
	GrantExpiresAt         string `json:"grant_expires_at"`
	GrantStatus            string `json:"grant_status"`
	PlanTransitionRevision string `json:"plan_transition_revision"`
	Replay                 bool   `json:"replay"`
}

func decodeBackupRecoveryAuthorizationResponse(
	t *testing.T,
	response *httptest.ResponseRecorder,
) backupRecoveryAuthorizationResponseJSON {
	t.Helper()
	var envelope struct {
		Data backupRecoveryAuthorizationResponseJSON `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func assertBackupRecoveryAuthorizationGrantMetadata(
	t *testing.T,
	operation recovery.AuthorizationReceiptOperation,
	response backupRecoveryAuthorizationResponseJSON,
) {
	t.Helper()
	wantCategory := ""
	wantStatus := ""
	switch operation {
	case recovery.AuthorizationReceiptWriteAuthorize:
		wantCategory = string(recovery.AuthorityWrite)
		wantStatus = "issued"
	case recovery.AuthorizationReceiptDeleteAuthorize:
		wantCategory = string(recovery.AuthorityExactMirrorDelete)
		wantStatus = "issued"
	case recovery.AuthorizationReceiptExecute:
		wantCategory = string(recovery.AuthorityWrite)
		wantStatus = "consumed"
	}
	if wantCategory == "" {
		if response.GrantCategory != "" || response.GrantBindingDigest != "" ||
			response.GrantExpiresAt != "" || response.GrantStatus != "" {
			t.Fatalf("security override exposed grant metadata: %+v", response)
		}
		return
	}
	if response.GrantCategory != wantCategory || response.GrantStatus != wantStatus ||
		len(response.GrantBindingDigest) != 64 || !lowerHexAPI(response.GrantBindingDigest) {
		t.Fatalf("%s grant metadata=%+v, want category=%q status=%q canonical binding", operation,
			response, wantCategory, wantStatus)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, response.GrantExpiresAt)
	if err != nil || expiresAt.Location() != time.UTC {
		t.Fatalf("%s grant expiry=%q error=%v, want UTC RFC3339", operation, response.GrantExpiresAt, err)
	}
}

func changedBackupRecoveryAuthorizationHandlerBody(
	t *testing.T,
	operation recovery.AuthorizationReceiptOperation,
	body string,
) string {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatal(err)
	}
	field := "reason"
	value := "FAKE_RECOVERY_CHANGED_REASON_FOR_TEST_ONLY"
	if operation == recovery.AuthorizationReceiptWriteAuthorize ||
		operation == recovery.AuthorizationReceiptDeleteAuthorize ||
		operation == recovery.AuthorizationReceiptExecute {
		field = "grant_secret"
		value = backupRecoveryHandlerFakeGrantSecret("changed-grant")
	}
	encodedValue, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	payload[field] = encodedValue
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func backupRecoveryHandlerFakeOpaqueID(label string) string {
	digest := backupRecoveryHandlerFakeDigest(label)
	return hex.EncodeToString(digest[:16])
}

func backupRecoveryHandlerFakeGrantSecret(label string) string {
	digest := backupRecoveryHandlerFakeDigest(label)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func backupRecoveryHandlerFakeDigest(label string) [sha256.Size]byte {
	return sha256.Sum256([]byte("FAKE_RECOVERY_HANDLER_" + label + "_FOR_TEST_ONLY"))
}

type backupRecoveryAuthorizationServiceFake struct {
	replayRequests    []recovery.RecoveryAuthorizationRequest
	authorizeRequests []recovery.RecoveryAuthorizationRequest
	replayResult      recovery.RecoveryAuthorizationResult
	replayFound       bool
	replayErr         error
	authorizeResult   recovery.RecoveryAuthorizationResult
	authorizeErr      error
}

type backupRecoveryTargetRootServiceFake struct {
	replayRegisterRequests []recovery.TargetRootRegistrationRequest
	replayRegisterResult   settings.RecoveryTargetRootSummary
	replayRegisterFound    bool
	replayRegisterErr      error
	registerRequests       []recovery.TargetRootRegistrationRequest
	registerResult         settings.RecoveryTargetRootSummary
	registerErr            error
	deleteRequests         []recovery.TargetRootDeletionRequest
	deleteResult           settings.RecoveryTargetRootSummary
	deleteErr              error
	replayDeleteRequests   []recovery.TargetRootDeletionRequest
	replayDeleteResult     settings.RecoveryTargetRootSummary
	replayDeleteFound      bool
	replayDeleteErr        error
	listNodeIDs            []uint
	listResult             []settings.RecoveryTargetRootSummary
	listErr                error
}

func (service *backupRecoveryTargetRootServiceFake) ReplayRegistration(
	_ context.Context,
	request recovery.TargetRootRegistrationRequest,
) (settings.RecoveryTargetRootSummary, bool, error) {
	service.replayRegisterRequests = append(service.replayRegisterRequests, request)
	return service.replayRegisterResult, service.replayRegisterFound, service.replayRegisterErr
}

func (service *backupRecoveryTargetRootServiceFake) Register(
	_ context.Context,
	request recovery.TargetRootRegistrationRequest,
) (settings.RecoveryTargetRootSummary, error) {
	service.registerRequests = append(service.registerRequests, request)
	return service.registerResult, service.registerErr
}

func (service *backupRecoveryTargetRootServiceFake) DeleteAuthorized(
	_ context.Context,
	request recovery.TargetRootDeletionRequest,
) (settings.RecoveryTargetRootSummary, error) {
	service.deleteRequests = append(service.deleteRequests, request)
	if service.deleteResult == (settings.RecoveryTargetRootSummary{}) {
		service.deleteResult = settings.RecoveryTargetRootSummary{
			NodeID: request.NodeID, RootID: request.RootID, SafeLabel: "deleted",
		}
	}
	return service.deleteResult, service.deleteErr
}

func (service *backupRecoveryTargetRootServiceFake) ReplayDeletion(
	_ context.Context,
	request recovery.TargetRootDeletionRequest,
) (settings.RecoveryTargetRootSummary, bool, error) {
	service.replayDeleteRequests = append(service.replayDeleteRequests, request)
	return service.replayDeleteResult, service.replayDeleteFound, service.replayDeleteErr
}

func (service *backupRecoveryTargetRootServiceFake) List(
	_ context.Context,
	_ uint,
	nodeID uint,
) ([]settings.RecoveryTargetRootSummary, error) {
	service.listNodeIDs = append(service.listNodeIDs, nodeID)
	return service.listResult, service.listErr
}

type backupRecoveryDowngradeServiceFake struct {
	replayRequests []backupruntime.RecoveryDowngradeReadinessRequest
	replayResult   backupruntime.RecoveryDowngradeReadiness
	replayFound    bool
	replayErr      error
	requests       []backupruntime.RecoveryDowngradeReadinessRequest
	result         backupruntime.RecoveryDowngradeReadiness
	err            error
	calls          int
}

type backupRecoveryObjectRequest struct {
	RequesterID uint
	ID          string
}

type backupRecoveryLifecycleServiceFake struct {
	plan            recovery.RecoveryPlanView
	job             recovery.RecoveryJobView
	planReads       []backupRecoveryObjectRequest
	jobReads        []backupRecoveryObjectRequest
	itemPage        recovery.RecoveryJobItemPage
	resultPage      recovery.RecoveryResultPage
	itemPageReads   []backupRecoveryPageRead
	resultPageReads []backupRecoveryPageRead
	planMutations   []recovery.RecoveryPlanMutationRequest
	planCreate      recovery.CreatePlanResult
	planCreates     []recovery.CreatePlanIntentRequest
	preflight       recovery.RecoveryPreflightView
	preflights      []recovery.RecoveryPreflightRequest
	jobCancels      []backupRecoveryJobMutationRequest
	retained        recovery.RetainedRecoveryResultSet
	retains         []recovery.RetainRecoveryResultsRequest
	cleanup         recovery.RecoveryResultCleanupView
	cleanups        []recovery.RecoveryResultCleanupRequest
}

type backupRecoveryPageRead struct {
	RequesterID uint
	JobID       string
	Page        recovery.RecoveryPageRequest
}

type backupRecoveryJobMutationRequest struct {
	RequesterID      uint
	JobID            string
	ExpectedRevision uint64
}

func (service *backupRecoveryLifecycleServiceFake) GetPlan(
	_ context.Context,
	requesterID uint,
	id string,
) (recovery.RecoveryPlanView, error) {
	service.planReads = append(service.planReads, backupRecoveryObjectRequest{RequesterID: requesterID, ID: id})
	return service.plan, nil
}

func (service *backupRecoveryLifecycleServiceFake) CancelPlan(
	_ context.Context,
	request recovery.RecoveryPlanMutationRequest,
) (recovery.RecoveryPlanView, error) {
	service.planMutations = append(service.planMutations, request)
	service.plan.State = recovery.PlanStateCanceled
	service.plan.Revision = "8"
	return service.plan, nil
}

func (service *backupRecoveryLifecycleServiceFake) GetJob(
	_ context.Context,
	requesterID uint,
	id string,
) (recovery.RecoveryJobView, error) {
	service.jobReads = append(service.jobReads, backupRecoveryObjectRequest{RequesterID: requesterID, ID: id})
	return service.job, nil
}

func (service *backupRecoveryLifecycleServiceFake) ListJobItems(
	_ context.Context,
	requesterID uint,
	jobID string,
	page recovery.RecoveryPageRequest,
) (recovery.RecoveryJobItemPage, error) {
	service.itemPageReads = append(service.itemPageReads, backupRecoveryPageRead{RequesterID: requesterID, JobID: jobID, Page: page})
	return service.itemPage, nil
}

func (service *backupRecoveryLifecycleServiceFake) ListPublishedResults(
	_ context.Context,
	requesterID uint,
	jobID string,
	page recovery.RecoveryPageRequest,
) (recovery.RecoveryResultPage, error) {
	service.resultPageReads = append(service.resultPageReads, backupRecoveryPageRead{RequesterID: requesterID, JobID: jobID, Page: page})
	return service.resultPage, nil
}

func (service *backupRecoveryLifecycleServiceFake) CreatePlan(
	_ context.Context,
	request recovery.CreatePlanIntentRequest,
) (recovery.CreatePlanResult, error) {
	service.planCreates = append(service.planCreates, request)
	return service.planCreate, nil
}

func (service *backupRecoveryLifecycleServiceFake) Preflight(
	_ context.Context,
	request recovery.RecoveryPreflightRequest,
) (recovery.RecoveryPreflightView, error) {
	service.preflights = append(service.preflights, request)
	return service.preflight, nil
}

func (service *backupRecoveryLifecycleServiceFake) CancelJob(
	_ context.Context,
	requesterID uint,
	jobID string,
	expectedRevision uint64,
) (recovery.RecoveryJobView, error) {
	service.jobCancels = append(service.jobCancels, backupRecoveryJobMutationRequest{
		RequesterID: requesterID, JobID: jobID, ExpectedRevision: expectedRevision,
	})
	return service.job, nil
}

func (service *backupRecoveryLifecycleServiceFake) RetainRecoveryResults(
	_ context.Context,
	request recovery.RetainRecoveryResultsRequest,
) (recovery.RetainedRecoveryResultSet, error) {
	service.retains = append(service.retains, request)
	return service.retained, nil
}

func (service *backupRecoveryLifecycleServiceFake) RequestResultCleanup(
	_ context.Context,
	request recovery.RecoveryResultCleanupRequest,
) (recovery.RecoveryResultCleanupView, error) {
	service.cleanups = append(service.cleanups, request)
	return service.cleanup, nil
}

func (service *backupRecoveryDowngradeServiceFake) RecoveryDowngradeReadiness(
	context.Context,
) (backupruntime.RecoveryDowngradeReadiness, error) {
	service.calls++
	return service.result, service.err
}

func (service *backupRecoveryDowngradeServiceFake) ReplayRecoveryDowngradeReadiness(
	_ context.Context,
	request backupruntime.RecoveryDowngradeReadinessRequest,
) (backupruntime.RecoveryDowngradeReadiness, bool, error) {
	service.replayRequests = append(service.replayRequests, request)
	return service.replayResult, service.replayFound, service.replayErr
}

func (service *backupRecoveryDowngradeServiceFake) RequestRecoveryDowngradeReadiness(
	_ context.Context,
	request backupruntime.RecoveryDowngradeReadinessRequest,
) (backupruntime.RecoveryDowngradeReadiness, error) {
	service.requests = append(service.requests, request)
	return service.result, service.err
}

func (service *backupRecoveryAuthorizationServiceFake) ReplayAuthorization(
	_ context.Context,
	request recovery.RecoveryAuthorizationRequest,
) (recovery.RecoveryAuthorizationResult, bool, error) {
	service.replayRequests = append(service.replayRequests, request)
	return service.replayResult, service.replayFound, service.replayErr
}

func (service *backupRecoveryAuthorizationServiceFake) Authorize(
	_ context.Context,
	request recovery.RecoveryAuthorizationRequest,
) (recovery.RecoveryAuthorizationResult, error) {
	service.authorizeRequests = append(service.authorizeRequests, request)
	return service.authorizeResult, service.authorizeErr
}

func newBackupRecoveryAuthorizationHandlerProofFixture(
	t *testing.T,
) (*gorm.DB, *auth.JWTManager, model.User, string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	user := model.User{
		Username: "recovery-authorization-admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
		Role: "admin", TokenVersion: 1, TOTPEnabled: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	signingKey := backupRecoveryHandlerFakeDigest("jwt-signing-key")
	manager := auth.NewJWTManager(hex.EncodeToString(signingKey[:]), time.Hour)
	proof, _, err := manager.GenerateStepUpToken(user, auth.StepUpAction("asset.recover"))
	if err != nil {
		t.Fatal(err)
	}
	return db, manager, user, proof
}

var _ RecoveryAuthorizationHandlerService = (*backupRecoveryAuthorizationServiceFake)(nil)
var _ RecoveryTargetRootHandlerService = (*backupRecoveryTargetRootServiceFake)(nil)
var _ RecoveryDowngradeHandlerService = (*backupRecoveryDowngradeServiceFake)(nil)
