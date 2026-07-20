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
