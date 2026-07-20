package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
)

func TestBackupProcessingHandlerBindsQueuedLocationPollCancelAndAuditToInterest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pointID, entryID, interestID := strings.Repeat("1", 32), strings.Repeat("2", 64), strings.Repeat("3", 32)
	service := &backupProcessingServiceFake{
		createResult: processing.PreviewJobResult{
			SchemaVersion: 1, JobID: interestID, State: processing.ProcessingProductQueued,
			Representation: processing.PreviewThumbnail, Capability: "image.thumbnail", Profile: "raster_thumbnail_v1",
			FallbackActions: []string{"native_preview", "download", "recovery"}, PollAfterSeconds: 2,
		},
		pollResult: processing.PreviewJobResult{
			SchemaVersion: 1, JobID: interestID, State: processing.ProcessingProductDerived, Representation: processing.PreviewThumbnail,
			Capability: "image.thumbnail", Profile: "raster_thumbnail_v1", Coverage: "complete",
			FallbackActions: []string{"native_preview", "download", "recovery"}, Terminal: true,
		},
	}
	audit := &backupAssetAuditSpy{}
	handler := NewBackupProcessingHandler(service, audit)
	router := gin.New()
	router.Use(backupProcessingActorForTest())
	base := "/api/v1/recovery-points/:id/entries/:entryId/preview-jobs"
	router.POST(base, handler.CreatePreview)
	router.GET(base+"/:jobId", handler.PollPreview)
	router.POST(base+"/:jobId/cancel", handler.CancelPreview)

	createPath := "/api/v1/recovery-points/" + pointID + "/entries/" + entryID + "/preview-jobs"
	create := httptest.NewRecorder()
	createRequest := httptest.NewRequest(http.MethodPost, createPath,
		strings.NewReader(`{"schema_version":1,"representation":"thumbnail","profile":"raster_thumbnail_v1"}`))
	createRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(create, createRequest)
	pollPath := createPath + "/" + interestID
	if create.Code != http.StatusAccepted || create.Header().Get("Location") != pollPath || create.Header().Get("Retry-After") != "2" {
		t.Fatalf("create status=%d headers=%v body=%s", create.Code, create.Header(), create.Body.String())
	}
	var createEnvelope struct {
		Data processing.PreviewJobResult `json:"data"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &createEnvelope); err != nil || createEnvelope.Data.JobID != interestID ||
		createEnvelope.Data.PollAfterSeconds != 2 {
		t.Fatalf("create envelope=%+v err=%v", createEnvelope, err)
	}

	poll := httptest.NewRecorder()
	router.ServeHTTP(poll, httptest.NewRequest(http.MethodGet, pollPath, nil))
	if poll.Code != http.StatusOK || len(service.polls) != 1 || service.polls[0].JobID != interestID ||
		!strings.Contains(poll.Body.String(), `"job_id":"`+interestID+`"`) {
		t.Fatalf("poll status=%d lookups=%+v body=%s", poll.Code, service.polls, poll.Body.String())
	}
	cancel := httptest.NewRecorder()
	router.ServeHTTP(cancel, httptest.NewRequest(http.MethodPost, pollPath+"/cancel", strings.NewReader(`{"schema_version":1}`)))
	if cancel.Code != http.StatusOK || len(service.cancels) != 1 || service.cancels[0].JobID != interestID {
		t.Fatalf("cancel status=%d lookups=%+v body=%s", cancel.Code, service.cancels, cancel.Body.String())
	}

	ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}
	if len(service.creates) != 1 || service.creates[0].Ref != ref || service.creates[0].Actor.UserID != 42 ||
		service.polls[0].Ref != ref || service.polls[0].Actor.UserID != 42 || service.cancels[0].Ref != ref {
		t.Fatalf("exact processing requests create=%+v poll=%+v cancel=%+v", service.creates, service.polls, service.cancels)
	}
	if len(audit.inputs) != 3 {
		t.Fatalf("processing audit count=%d", len(audit.inputs))
	}
	for _, input := range audit.inputs {
		if input.Action != backupasset.AuditActionPreviewJob || input.RecoveryPointID != pointID || input.EntryID != entryID {
			t.Fatalf("processing audit=%+v", input)
		}
	}
	for _, forbidden := range []string{"raster_thumbnail_v1", "image.thumbnail", "source", "path", "worker", "fence", "grant"} {
		if strings.Contains(strings.ToLower(create.Body.String()+poll.Body.String()+cancel.Body.String()), forbidden) &&
			(forbidden == "source" || forbidden == "path" || forbidden == "worker" || forbidden == "fence" || forbidden == "grant") {
			t.Fatalf("processing response leaked %q", forbidden)
		}
	}
}

func TestBackupProcessingHandlerRejectsPollResultForAnotherInterest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pointID, entryID := strings.Repeat("1", 32), strings.Repeat("2", 64)
	requestedID, returnedID := strings.Repeat("3", 32), strings.Repeat("4", 32)
	service := &backupProcessingServiceFake{pollResult: processing.PreviewJobResult{
		SchemaVersion: 1, JobID: returnedID, State: processing.ProcessingProductDerived,
		Representation: processing.PreviewThumbnail, FallbackActions: []string{"native_preview", "download"}, Terminal: true,
	}}
	handler := NewBackupProcessingHandler(service, &backupAssetAuditSpy{})
	router := gin.New()
	router.Use(backupProcessingActorForTest())
	router.GET("/api/v1/recovery-points/:id/entries/:entryId/preview-jobs/:jobId", handler.PollPreview)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/recovery-points/"+pointID+"/entries/"+entryID+"/preview-jobs/"+requestedID, nil))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), returnedID) {
		t.Fatalf("mismatched poll status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBackupProcessingHandlerReturnsExactAssetStateWithoutCreatingWork(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pointID, entryID := strings.Repeat("1", 32), strings.Repeat("2", 64)
	service := &backupProcessingServiceFake{stateResult: processing.AssetProcessingState{
		SchemaVersion: 1,
		Representations: []processing.PreviewJobResult{
			processingStateResultForTest(processing.PreviewThumbnail),
			processingStateResultForTest(processing.PreviewText),
			processingStateResultForTest(processing.PreviewDocumentPages),
			processingStateResultForTest(processing.PreviewMedia),
			processingStateResultForTest(processing.PreviewArchiveIndex),
		},
	}}
	handler := NewBackupProcessingHandler(service, &backupAssetAuditSpy{})
	router := gin.New()
	router.Use(backupProcessingActorForTest())
	router.GET("/api/v1/recovery-points/:id/entries/:entryId/processing", handler.GetState)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/recovery-points/"+pointID+"/entries/"+entryID+"/processing", nil))
	if response.Code != http.StatusOK || len(service.states) != 1 || len(service.creates) != 0 ||
		service.states[0].Ref != (backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}) {
		t.Fatalf("state status=%d requests=%+v creates=%+v body=%s", response.Code, service.states, service.creates, response.Body.String())
	}
	for _, forbidden := range []string{"job_id", "worker_id", "grant", "attempt", "fence", "blob", "path"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("state response leaked %q: %s", forbidden, response.Body.String())
		}
	}
}

func processingStateResultForTest(representation processing.PreviewRepresentation) processing.PreviewJobResult {
	return processing.PreviewJobResult{
		SchemaVersion: 1, State: processing.ProcessingProductNotDeployed,
		Representation: representation, Reason: "worker_unavailable",
		FallbackActions: []string{"native_preview", "download"}, Terminal: true,
	}
}

func TestBackupProcessingHandlerRejectsMalformedReplayAndDisabledWithoutLeaking(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pointID, entryID, interestID := strings.Repeat("1", 32), strings.Repeat("2", 64), strings.Repeat("3", 32)
	base := "/api/v1/recovery-points/" + pointID + "/entries/" + entryID + "/preview-jobs"
	tests := []struct {
		name       string
		method     string
		target     string
		body       string
		serviceErr error
		wantStatus int
	}{
		{name: "unknown field", method: http.MethodPost, target: base, body: `{"schema_version":1,"representation":"thumbnail","locator":"secret"}`, wantStatus: http.StatusBadRequest},
		{name: "duplicate field", method: http.MethodPost, target: base, body: `{"schema_version":1,"representation":"thumbnail","representation":"text"}`, wantStatus: http.StatusBadRequest},
		{name: "secret capability is not a public preview action", method: http.MethodPost, target: base, body: `{"schema_version":1,"representation":"secret.classify"}`, wantStatus: http.StatusBadRequest},
		{name: "query", method: http.MethodGet, target: base + "/" + interestID + "?source=hidden", wantStatus: http.StatusBadRequest},
		{name: "replayed handle", method: http.MethodGet, target: base + "/" + interestID, serviceErr: processing.ErrProcessingHandleNotFound, wantStatus: http.StatusNotFound},
		{name: "disabled", method: http.MethodPost, target: base, body: `{"schema_version":1,"representation":"thumbnail"}`, serviceErr: processing.ErrProcessingDisabled, wantStatus: http.StatusNotFound},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			service := &backupProcessingServiceFake{err: testCase.serviceErr}
			handler := NewBackupProcessingHandler(service, &backupAssetAuditSpy{})
			router := gin.New()
			router.Use(backupProcessingActorForTest())
			router.POST("/api/v1/recovery-points/:id/entries/:entryId/preview-jobs", handler.CreatePreview)
			router.GET("/api/v1/recovery-points/:id/entries/:entryId/preview-jobs/:jobId", handler.PollPreview)
			response := httptest.NewRecorder()
			request := httptest.NewRequest(testCase.method, testCase.target, strings.NewReader(testCase.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(response, request)
			if response.Code != testCase.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", response.Code, testCase.wantStatus, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), "private") {
				t.Fatalf("error response leaked detail: %s", response.Body.String())
			}
		})
	}
}

func backupProcessingActorForTest() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(42))
		c.Set(middleware.CtxUsername, "operator")
		c.Set(middleware.CtxRole, "operator")
		c.Next()
	}
}

type backupProcessingServiceFake struct {
	createResult processing.PreviewJobResult
	pollResult   processing.PreviewJobResult
	stateResult  processing.AssetProcessingState
	err          error
	creates      []processing.PreviewJobRequest
	polls        []processing.PreviewJobLookup
	cancels      []processing.PreviewJobLookup
	states       []processing.PreviewStateRequest
}

func (fake *backupProcessingServiceFake) RequestProcessingPreview(
	_ context.Context,
	request processing.PreviewJobRequest,
) (processing.PreviewJobResult, error) {
	fake.creates = append(fake.creates, request)
	return fake.createResult, fake.err
}

func (fake *backupProcessingServiceFake) PollProcessingPreview(
	_ context.Context,
	lookup processing.PreviewJobLookup,
) (processing.PreviewJobResult, error) {
	fake.polls = append(fake.polls, lookup)
	return fake.pollResult, fake.err
}

func (fake *backupProcessingServiceFake) CancelProcessingPreview(
	_ context.Context,
	lookup processing.PreviewJobLookup,
) error {
	fake.cancels = append(fake.cancels, lookup)
	return fake.err
}

func (fake *backupProcessingServiceFake) GetProcessingState(
	_ context.Context,
	request processing.PreviewStateRequest,
) (processing.AssetProcessingState, error) {
	fake.states = append(fake.states, request)
	return fake.stateResult, fake.err
}
