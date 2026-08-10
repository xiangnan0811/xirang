package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset/recovery"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRecoveryAuthorizationReceiptSecurityOverrideReplayAndConflict(t *testing.T) {
	testRecoveryAuthorizationReceiptHandlerReplayAndConflict(t, recovery.AuthorizationReceiptSecurityOverride)
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
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"),
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
