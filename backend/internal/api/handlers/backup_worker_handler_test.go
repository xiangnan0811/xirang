package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset"
	backupruntime "xirang/backend/internal/backupasset/runtime"

	"github.com/gin-gonic/gin"
)

func TestBackupWorkerHandlerReturnsOnlySanitizedAdminSummary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &backupWorkerAdminServiceFake{
		config: backupasset.ProcessingConfig{Enabled: true, LocalWorker: backupasset.ProcessingLocalWorkerConfig{Enabled: true}},
		summary: backupruntime.ProcessingAdminSummary{
			SchemaVersion: 1, Configured: true, LocalEnabled: true,
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
	if envelope.Data.SchemaVersion != 1 || envelope.Data.Workers.Active != 2 || envelope.Data.Queue.Total != 3 {
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

type backupWorkerAdminServiceFake struct {
	config       backupasset.ProcessingConfig
	summary      backupruntime.ProcessingAdminSummary
	configErr    error
	summaryErr   error
	configCalls  int
	summaryCalls int
}

func (service *backupWorkerAdminServiceFake) ProcessingConfig() (backupasset.ProcessingConfig, error) {
	service.configCalls++
	return service.config, service.configErr
}

func (service *backupWorkerAdminServiceFake) ProcessingAdminSummary(context.Context) (backupruntime.ProcessingAdminSummary, error) {
	service.summaryCalls++
	return service.summary, service.summaryErr
}
