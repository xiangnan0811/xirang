package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/api/docs"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/processing"
	backupruntime "xirang/backend/internal/backupasset/runtime"

	"github.com/gin-gonic/gin"
)

func TestBackupWorkerHandlerReturnsOnlySanitizedAdminSummary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &backupWorkerAdminServiceFake{
		config: backupasset.ProcessingConfig{Enabled: true, LocalWorker: backupasset.ProcessingLocalWorkerConfig{Enabled: true}},
		summary: backupruntime.ProcessingAdminSummary{
			SchemaVersion: 1, Configured: true, LocalEnabled: true,
			BackfillPolicy: backupruntime.ProcessingBackfillPolicy{
				SchemaVersion: 1, Revision: strings.Repeat("9", 64), Paused: true,
				BatchSize: 50, JobsPerHour: 500, BytesPerHour: 1 << 30,
				ProviderConcurrency: 2, CapabilityConcurrency: 2,
			},
			Workers: backupruntime.ProcessingWorkerCounts{Active: 2},
			Queue: backupruntime.ProcessingQueueSummary{
				Total: 3, ByState: map[string]int64{"queued": 3},
				ByPriority: map[string]int64{"interactive": 1, "background": 2},
			},
		},
	}
	router := gin.New()
	router.GET("/admin/backup-asset-processing", NewBackupWorkerHandler(service).Get)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/backup-asset-processing", nil)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.configCalls != 1 || service.summaryCalls != 1 {
		t.Fatalf("status=%d configCalls=%d summaryCalls=%d body=%s", response.Code, service.configCalls, service.summaryCalls, response.Body.String())
	}
	var envelope struct {
		Data backupruntime.ProcessingAdminSummary `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.SchemaVersion != 1 || envelope.Data.Workers.Active != 2 || envelope.Data.Queue.Total != 3 ||
		len(envelope.Data.BackfillPolicy.Revision) != 64 || !envelope.Data.BackfillPolicy.Paused {
		t.Fatalf("summary=%+v", envelope.Data)
	}
	for _, forbidden := range []string{
		"worker_id", "job_id", "recovery_point_id", "entry_id", "source_fingerprint",
		"grant", "session", "attempt", "fence", "secret", "socket", "path", "certificate", "blob_id", "raw_error",
	} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("sanitized response leaked forbidden field %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestBackupWorkerAdminSwaggerDocumentsEveryOperation(t *testing.T) {
	var document struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal([]byte(docs.SwaggerInfo.ReadDoc()), &document); err != nil {
		t.Fatalf("decode generated Swagger: %v", err)
	}
	want := map[string][]string{
		"/admin/backup-asset-processing":                                 {"get"},
		"/admin/backup-asset-processing/capabilities":                    {"get"},
		"/admin/backup-asset-processing/coverage":                        {"get"},
		"/admin/backup-asset-processing/updater":                         {"get"},
		"/admin/backup-asset-processing/updater/offline-candidates":      {"get"},
		"/admin/backup-asset-processing/backfill-policy":                 {"patch"},
		"/admin/backup-asset-processing/updater/offline-candidates/scan": {"post"},
		"/admin/backup-asset-processing/updater/offline-imports":         {"post"},
	}
	for path, methods := range want {
		operations, ok := document.Paths[path]
		if !ok {
			t.Fatalf("generated Swagger missing path %s", path)
		}
		for _, method := range methods {
			raw, ok := operations[method]
			if !ok {
				t.Fatalf("generated Swagger missing %s %s operation", method, path)
			}
			var operation struct {
				Summary   string                     `json:"summary"`
				Security  []map[string][]string      `json:"security"`
				Responses map[string]json.RawMessage `json:"responses"`
			}
			if err := json.Unmarshal(raw, &operation); err != nil {
				t.Fatalf("decode %s %s operation: %v", method, path, err)
			}
			if operation.Summary == "" || len(operation.Security) == 0 || len(operation.Responses) == 0 {
				t.Fatalf("incomplete Swagger operation %s %s: %+v", method, path, operation)
			}
		}
	}
}

func TestBackupWorkerSwaggerPublishesClosedMutationBodyContracts(t *testing.T) {
	var document struct {
		Definitions map[string]struct {
			Properties map[string]struct {
				Type      string   `json:"type"`
				Minimum   *float64 `json:"minimum"`
				Maximum   *float64 `json:"maximum"`
				MinLength *int64   `json:"minLength"`
				MaxLength *int64   `json:"maxLength"`
				Pattern   string   `json:"x-pattern"`
				Nullable  bool     `json:"x-nullable"`
			} `json:"properties"`
			Required []string `json:"required"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal([]byte(docs.SwaggerInfo.ReadDoc()), &document); err != nil {
		t.Fatalf("decode generated Swagger: %v", err)
	}
	activation, ok := document.Definitions["internal_api_handlers.backupWorkerActivationPayload"]
	if !ok {
		t.Fatal("generated Swagger missing activation payload definition")
	}
	assertSwaggerRequiredFields(t, activation.Required, "schema_version", "candidate_id", "expected_active_fingerprint")
	assertSwaggerStringField(t, activation.Properties["candidate_id"], 32, 32, "^[0-9a-f]{32}$", false)
	assertSwaggerStringField(t, activation.Properties["expected_active_fingerprint"], 64, 64, "^[0-9a-f]{64}$", true)
	assertSwaggerNumberBounds(t, activation.Properties["schema_version"], 1, 1)

	policy, ok := document.Definitions["internal_api_handlers.backupWorkerPolicyPayload"]
	if !ok {
		t.Fatal("generated Swagger missing policy payload definition")
	}
	assertSwaggerRequiredFields(t, policy.Required, "schema_version", "expected_revision", "paused", "batch_size", "jobs_per_hour", "bytes_per_hour", "provider_concurrency", "capability_concurrency")
	assertSwaggerStringField(t, policy.Properties["expected_revision"], 64, 64, "^[0-9a-f]{64}$", false)
	assertSwaggerNumberBounds(t, policy.Properties["schema_version"], 1, 1)
	for _, field := range []string{"batch_size", "jobs_per_hour", "bytes_per_hour", "provider_concurrency", "capability_concurrency"} {
		assertSwaggerNumberMinimum(t, policy.Properties[field], 1)
	}
}

func assertSwaggerRequiredFields(t *testing.T, actual []string, want ...string) {
	t.Helper()
	set := make(map[string]bool, len(actual))
	for _, field := range actual {
		set[field] = true
	}
	if len(set) != len(want) {
		t.Fatalf("Swagger required fields=%v want exactly=%v", actual, want)
	}
	for _, field := range want {
		if !set[field] {
			t.Fatalf("Swagger required fields=%v missing %q", actual, field)
		}
	}
}

func assertSwaggerStringField(
	t *testing.T,
	field struct {
		Type      string   `json:"type"`
		Minimum   *float64 `json:"minimum"`
		Maximum   *float64 `json:"maximum"`
		MinLength *int64   `json:"minLength"`
		MaxLength *int64   `json:"maxLength"`
		Pattern   string   `json:"x-pattern"`
		Nullable  bool     `json:"x-nullable"`
	},
	minLength, maxLength int64,
	pattern string,
	nullable bool,
) {
	t.Helper()
	if field.Type != "string" || field.MinLength == nil || *field.MinLength != minLength ||
		field.MaxLength == nil || *field.MaxLength != maxLength || field.Pattern != pattern || field.Nullable != nullable {
		t.Fatalf("Swagger string field=%+v want type=string length=%d..%d pattern=%q nullable=%t", field, minLength, maxLength, pattern, nullable)
	}
}

func assertSwaggerNumberBounds(t *testing.T, field struct {
	Type      string   `json:"type"`
	Minimum   *float64 `json:"minimum"`
	Maximum   *float64 `json:"maximum"`
	MinLength *int64   `json:"minLength"`
	MaxLength *int64   `json:"maxLength"`
	Pattern   string   `json:"x-pattern"`
	Nullable  bool     `json:"x-nullable"`
}, minimum, maximum float64) {
	t.Helper()
	if field.Type != "integer" || field.Minimum == nil || *field.Minimum != minimum || field.Maximum == nil || *field.Maximum != maximum {
		t.Fatalf("Swagger numeric field=%+v want integer minimum=%v maximum=%v", field, minimum, maximum)
	}
}

func assertSwaggerNumberMinimum(t *testing.T, field struct {
	Type      string   `json:"type"`
	Minimum   *float64 `json:"minimum"`
	Maximum   *float64 `json:"maximum"`
	MinLength *int64   `json:"minLength"`
	MaxLength *int64   `json:"maxLength"`
	Pattern   string   `json:"x-pattern"`
	Nullable  bool     `json:"x-nullable"`
}, minimum float64) {
	t.Helper()
	if field.Type != "integer" || field.Minimum == nil || *field.Minimum != minimum {
		t.Fatalf("Swagger numeric field=%+v want integer minimum=%v", field, minimum)
	}
}

func TestBackupWorkerHandlerRejectsDisabledBodyQueryAndUnavailable(t *testing.T) {
	testCases := []struct {
		name        string
		target      string
		body        string
		config      backupasset.ProcessingConfig
		configErr   error
		summaryErr  error
		wantStatus  int
		wantConfig  int
		wantSummary int
	}{
		{name: "feature disabled", target: "/admin/backup-asset-processing", config: backupasset.ProcessingConfig{}, wantStatus: http.StatusNotFound, wantConfig: 1},
		{name: "query", target: "/admin/backup-asset-processing?worker_id=hidden", config: backupasset.ProcessingConfig{Enabled: true}, wantStatus: http.StatusBadRequest},
		{name: "body", target: "/admin/backup-asset-processing", body: `{}`, config: backupasset.ProcessingConfig{Enabled: true}, wantStatus: http.StatusBadRequest},
		{name: "config unavailable", target: "/admin/backup-asset-processing", configErr: errors.New("private config error"), wantStatus: http.StatusServiceUnavailable, wantConfig: 1},
		{name: "summary unavailable", target: "/admin/backup-asset-processing", config: backupasset.ProcessingConfig{Enabled: true}, summaryErr: errors.New("private database error"), wantStatus: http.StatusServiceUnavailable, wantConfig: 1, wantSummary: 1},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			service := &backupWorkerAdminServiceFake{config: testCase.config, configErr: testCase.configErr, summaryErr: testCase.summaryErr}
			router := gin.New()
			router.GET("/admin/backup-asset-processing", NewBackupWorkerHandler(service).Get)
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, testCase.target, strings.NewReader(testCase.body))
			router.ServeHTTP(response, request)
			if response.Code != testCase.wantStatus || service.configCalls != testCase.wantConfig || service.summaryCalls != testCase.wantSummary {
				t.Fatalf("status=%d configCalls=%d summaryCalls=%d body=%s", response.Code, service.configCalls, service.summaryCalls, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "private") {
				t.Fatalf("response leaked internal error: %s", response.Body.String())
			}
		})
	}
}

func TestBackupWorkerHandlerReturnsSanitizedCapabilitiesCoverageUpdaterAndCandidates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	service := &backupWorkerAdminServiceFake{
		config: backupasset.ProcessingConfig{Enabled: true, Updater: backupasset.ProcessingUpdaterConfig{Enabled: true}},
		capabilities: []processing.CapabilityInventoryItem{{
			Capability: "image.thumbnail", Schema: "image.thumbnail.v1", Profile: "raster_thumbnail_v1", Deployed: true, ReadyWorkers: 1,
		}},
		coverage: processing.CoverageSummary{SchemaVersion: 1, GeneratedAt: now, Eligible: 3, Completed: 2},
		updater: backupruntime.ProcessingUpdaterStatus{
			SchemaVersion: 1, Enabled: true,
			Active: &backupruntime.ProcessingUpdaterCandidate{
				CandidateID: strings.Repeat("1", 32), SourceKind: "admin_registered", SourceID: "offline-2026-07",
				Version: "1.2.3", ManifestDigest: strings.Repeat("2", 64), BundleFingerprint: strings.Repeat("3", 64),
				SigningKeyFingerprint: strings.Repeat("4", 64), State: "active", VerifiedAt: &now, ActivatedAt: &now,
			},
		},
		candidates: []backupruntime.ProcessingUpdaterCandidate{{
			CandidateID: strings.Repeat("5", 32), SourceKind: "admin_registered", SourceID: "offline-2026-08",
			Version: "1.2.4", ManifestDigest: strings.Repeat("6", 64), BundleFingerprint: strings.Repeat("7", 64),
			SigningKeyFingerprint: strings.Repeat("8", 64), State: "verified", VerifiedAt: &now,
		}},
	}
	handler := NewBackupWorkerHandler(service)
	router := gin.New()
	router.GET("/capabilities", handler.Capabilities)
	router.GET("/coverage", handler.Coverage)
	router.GET("/updater", handler.Updater)
	router.GET("/candidates", handler.OfflineCandidates)
	for _, path := range []string{"/capabilities", "/coverage", "/updater", "/candidates"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
		for _, forbidden := range []string{"inbox", "path", "url", "credential", "bundle_bytes", "raw_manifest", "worker_id", "uid", "pid"} {
			if strings.Contains(strings.ToLower(response.Body.String()), forbidden) {
				t.Fatalf("%s leaked %q: %s", path, forbidden, response.Body.String())
			}
		}
	}
}

func TestBackupWorkerHandlerUsesStrictJSONOnlyPolicyScanAndActivation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	active := strings.Repeat("a", 64)
	service := &backupWorkerAdminServiceFake{
		config: backupasset.ProcessingConfig{Enabled: true, Updater: backupasset.ProcessingUpdaterConfig{Enabled: true}},
		policy: backupruntime.ProcessingBackfillPolicy{
			SchemaVersion: 1, Revision: strings.Repeat("b", 64), Paused: true,
			BatchSize: 100, JobsPerHour: 1000, BytesPerHour: 1 << 30, ProviderConcurrency: 1, CapabilityConcurrency: 1,
		},
	}
	handler := NewBackupWorkerHandler(service)
	router := gin.New()
	router.PATCH("/policy", handler.UpdateBackfillPolicy)
	router.POST("/scan", handler.ScanOfflineCandidates)
	router.POST("/activate", handler.ActivateOfflineCandidate)

	policyBody := `{"schema_version":1,"expected_revision":"` + strings.Repeat("b", 64) +
		`","paused":false,"batch_size":50,"jobs_per_hour":500,"bytes_per_hour":1073741824,"provider_concurrency":2,"capability_concurrency":2}`
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/policy", strings.NewReader(policyBody))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(service.policyUpdates) != 1 || service.policyUpdates[0].Paused {
		t.Fatalf("policy status=%d updates=%+v body=%s", response.Code, service.policyUpdates, response.Body.String())
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/scan", nil))
	if response.Code != http.StatusAccepted || service.scanCalls != 1 {
		t.Fatalf("scan status=%d calls=%d body=%s", response.Code, service.scanCalls, response.Body.String())
	}

	activationBody := `{"schema_version":1,"candidate_id":"` + strings.Repeat("c", 32) +
		`","expected_active_fingerprint":"` + active + `"}`
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/activate", strings.NewReader(activationBody))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || len(service.activations) != 1 ||
		service.activations[0].ExpectedActiveFingerprint == nil || *service.activations[0].ExpectedActiveFingerprint != active {
		t.Fatalf("activation status=%d requests=%+v body=%s", response.Code, service.activations, response.Body.String())
	}

	bootstrapBody := `{"schema_version":1,"candidate_id":"` + strings.Repeat("d", 32) +
		`","expected_active_fingerprint":null}`
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/activate", strings.NewReader(bootstrapBody))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || len(service.activations) != 2 ||
		service.activations[1].ExpectedActiveFingerprint != nil {
		t.Fatalf("bootstrap activation status=%d requests=%+v body=%s", response.Code, service.activations, response.Body.String())
	}

	for _, invalid := range []struct {
		path string
		body string
	}{
		{"/scan", `{}`},
		{"/policy", policyBody[:len(policyBody)-1] + `,"unknown":true}`},
		{"/activate", `{"schema_version":1,"candidate_id":"` + strings.Repeat("c", 32) + `","expected_active_fingerprint":null,"url":"https://forbidden"}`},
	} {
		response = httptest.NewRecorder()
		request = httptest.NewRequest(http.MethodPost, invalid.path, strings.NewReader(invalid.body))
		if invalid.path == "/policy" {
			request.Method = http.MethodPatch
		}
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid %s status=%d body=%s", invalid.path, response.Code, response.Body.String())
		}
	}
}

func TestBackupWorkerHandlerAuditsPolicyAndUpdaterMutations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	audit := &backupAssetAuditSpy{}
	service := &backupWorkerAdminServiceFake{
		config: backupasset.ProcessingConfig{Enabled: true, Updater: backupasset.ProcessingUpdaterConfig{Enabled: true}},
		policy: backupruntime.ProcessingBackfillPolicy{SchemaVersion: 1, Revision: strings.Repeat("b", 64)},
	}
	handler := NewBackupWorkerHandler(service).WithAudit(audit)
	router := gin.New()
	router.PATCH("/policy", handler.UpdateBackfillPolicy)
	router.POST("/scan", handler.ScanOfflineCandidates)
	router.POST("/activate", handler.ActivateOfflineCandidate)

	policy := `{"schema_version":1,"expected_revision":"` + strings.Repeat("b", 64) +
		`","paused":false,"batch_size":50,"jobs_per_hour":500,"bytes_per_hour":1073741824,"provider_concurrency":2,"capability_concurrency":2}`
	request := httptest.NewRequest(http.MethodPatch, "/policy", strings.NewReader(policy))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("policy status=%d body=%s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/scan", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("scan status=%d body=%s", response.Code, response.Body.String())
	}

	candidateID := strings.Repeat("c", 32)
	activation := `{"schema_version":1,"candidate_id":"` + candidateID + `","expected_active_fingerprint":null}`
	request = httptest.NewRequest(http.MethodPost, "/activate", strings.NewReader(activation))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("activation status=%d body=%s", response.Code, response.Body.String())
	}
	if len(audit.inputs) != 3 {
		t.Fatalf("audit count=%d inputs=%+v", len(audit.inputs), audit.inputs)
	}
	wantOperations := []string{"backfill_policy_update", "offline_candidate_scan", "offline_candidate_activate"}
	for index, want := range wantOperations {
		input := audit.inputs[index]
		if input.Action != backupasset.AuditActionProcessingPolicyUpdate || input.Outcome != backupasset.AuditOutcomeSuccess ||
			input.Fields[backupasset.AuditFieldMode] != want {
			t.Fatalf("audit[%d]=%+v want operation=%q", index, input, want)
		}
	}
	if audit.inputs[2].Fields[backupasset.AuditFieldCorrelationID] != candidateID {
		t.Fatalf("activation audit=%+v want candidate correlation=%q", audit.inputs[2], candidateID)
	}
}

func TestBackupWorkerHandlerAuditsMutationFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	candidateID := strings.Repeat("c", 32)
	policy := `{"schema_version":1,"expected_revision":"` + strings.Repeat("b", 64) +
		`","paused":false,"batch_size":50,"jobs_per_hour":500,"bytes_per_hour":1073741824,"provider_concurrency":2,"capability_concurrency":2}`
	activation := `{"schema_version":1,"candidate_id":"` + candidateID + `","expected_active_fingerprint":null}`
	tests := []struct {
		name              string
		method            string
		target            string
		body              string
		config            backupasset.ProcessingConfig
		serviceErr        error
		wantStatus        int
		wantMode          string
		wantOutcome       backupasset.AuditOutcome
		wantCode          string
		wantCorrelationID string
	}{
		{
			name: "policy revision conflict", method: http.MethodPatch, target: "/policy", body: policy,
			config: backupasset.ProcessingConfig{Enabled: true}, serviceErr: processing.ErrRevisionConflict,
			wantStatus: http.StatusConflict, wantMode: "backfill_policy_update", wantOutcome: backupasset.AuditOutcomeBlocked,
			wantCode: "conflict",
		},
		{
			name: "scan unavailable", method: http.MethodPost, target: "/scan",
			config: backupasset.ProcessingConfig{Enabled: true}, serviceErr: errors.New("private updater error"),
			wantStatus: http.StatusServiceUnavailable, wantMode: "offline_candidate_scan", wantOutcome: backupasset.AuditOutcomeFailure,
			wantCode: "unavailable",
		},
		{
			name: "activation invalid contract", method: http.MethodPost, target: "/activate", body: activation,
			config: backupasset.ProcessingConfig{Enabled: true}, serviceErr: processing.ErrInvalidContract,
			wantStatus: http.StatusBadRequest, wantMode: "offline_candidate_activate", wantOutcome: backupasset.AuditOutcomeBlocked,
			wantCode: "invalid_request", wantCorrelationID: candidateID,
		},
		{
			name: "scan feature disabled", method: http.MethodPost, target: "/scan",
			wantStatus: http.StatusNotFound, wantMode: "offline_candidate_scan", wantOutcome: backupasset.AuditOutcomeBlocked,
			wantCode: "not_found",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			audit := &backupAssetAuditSpy{}
			service := &backupWorkerAdminServiceFake{config: test.config, summaryErr: test.serviceErr}
			handler := NewBackupWorkerHandler(service).WithAudit(audit)
			router := gin.New()
			router.PATCH("/policy", handler.UpdateBackfillPolicy)
			router.POST("/scan", handler.ScanOfflineCandidates)
			router.POST("/activate", handler.ActivateOfflineCandidate)
			request := httptest.NewRequest(test.method, test.target, strings.NewReader(test.body))
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if len(audit.inputs) != 1 {
				t.Fatalf("audit count=%d inputs=%+v", len(audit.inputs), audit.inputs)
			}
			input := audit.inputs[0]
			if input.Action != backupasset.AuditActionProcessingPolicyUpdate || input.Outcome != test.wantOutcome ||
				input.FailureCode != test.wantCode || input.Fields[backupasset.AuditFieldMode] != test.wantMode ||
				input.Fields[backupasset.AuditFieldCode] != test.wantCode {
				t.Fatalf("audit=%+v want outcome=%q mode=%q code=%q", input, test.wantOutcome, test.wantMode, test.wantCode)
			}
			if test.wantCorrelationID != "" && input.Fields[backupasset.AuditFieldCorrelationID] != test.wantCorrelationID {
				t.Fatalf("audit=%+v want correlation=%q", input, test.wantCorrelationID)
			}
		})
	}
}

type backupWorkerAdminServiceFake struct {
	config        backupasset.ProcessingConfig
	summary       backupruntime.ProcessingAdminSummary
	configErr     error
	summaryErr    error
	configCalls   int
	summaryCalls  int
	capabilities  []processing.CapabilityInventoryItem
	coverage      processing.CoverageSummary
	updater       backupruntime.ProcessingUpdaterStatus
	candidates    []backupruntime.ProcessingUpdaterCandidate
	policy        backupruntime.ProcessingBackfillPolicy
	policyUpdates []backupruntime.ProcessingBackfillPolicyUpdate
	activations   []backupruntime.ProcessingUpdaterActivationRequest
	scanCalls     int
}

func (service *backupWorkerAdminServiceFake) ProcessingConfig() (backupasset.ProcessingConfig, error) {
	service.configCalls++
	return service.config, service.configErr
}

func (service *backupWorkerAdminServiceFake) ProcessingAdminSummary(context.Context) (backupruntime.ProcessingAdminSummary, error) {
	service.summaryCalls++
	return service.summary, service.summaryErr
}

func (service *backupWorkerAdminServiceFake) ProcessingCapabilities(context.Context) ([]processing.CapabilityInventoryItem, error) {
	return service.capabilities, service.summaryErr
}

func (service *backupWorkerAdminServiceFake) ProcessingCoverage(context.Context) (processing.CoverageSummary, error) {
	return service.coverage, service.summaryErr
}

func (service *backupWorkerAdminServiceFake) ProcessingUpdaterStatus(context.Context) (backupruntime.ProcessingUpdaterStatus, error) {
	return service.updater, service.summaryErr
}

func (service *backupWorkerAdminServiceFake) ProcessingUpdaterCandidates(context.Context) ([]backupruntime.ProcessingUpdaterCandidate, error) {
	return service.candidates, service.summaryErr
}

func (service *backupWorkerAdminServiceFake) ProcessingBackfillPolicy() (backupruntime.ProcessingBackfillPolicy, error) {
	return service.policy, service.summaryErr
}

func (service *backupWorkerAdminServiceFake) UpdateProcessingBackfillPolicy(
	_ context.Context,
	request backupruntime.ProcessingBackfillPolicyUpdate,
) (backupruntime.ProcessingBackfillPolicy, error) {
	service.policyUpdates = append(service.policyUpdates, request)
	return service.policy, service.summaryErr
}

func (service *backupWorkerAdminServiceFake) RequestProcessingUpdaterScan(context.Context) error {
	service.scanCalls++
	return service.summaryErr
}

func (service *backupWorkerAdminServiceFake) ActivateProcessingUpdaterCandidate(
	_ context.Context,
	request backupruntime.ProcessingUpdaterActivationRequest,
) error {
	service.activations = append(service.activations, request)
	return service.summaryErr
}
