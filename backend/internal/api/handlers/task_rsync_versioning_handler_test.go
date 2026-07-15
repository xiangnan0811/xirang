package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	backuprepository "xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
)

func TestTaskRsyncVersioningHandlerPreflightBindsOnlySafeTypedFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &taskRsyncVersioningServiceStub{
		preflightResult: backupasset.RsyncVersioningPreflightResult{
			PreflightID: strings.Repeat("a", 32), Mode: backupasset.PublicationVersionedFullCopy,
			State: backupasset.RsyncVersioningReady, ReasonCode: backupasset.RsyncVersioningReasonReady,
			CapabilityRevision: 7, ExpiresAt: time.Date(2026, 7, 15, 13, 0, 0, 0, time.UTC),
			CapacityEstimate: backupasset.RsyncVersioningEstimateAvailable, InodeEstimate: backupasset.RsyncVersioningEstimateConstrained,
		},
	}
	handler := NewTaskRsyncVersioningHandler(service)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: "42"}}
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tasks/42/rsync-versioning/preflights", bytes.NewBufferString(`{"expected_task_revision":9,"requested_mode":"versioned_full_copy"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.CreatePreflight(ctx)

	if recorder.Code != http.StatusOK || service.preflightRequest.TaskID != 42 || service.preflightRequest.ExpectedTaskRevision != 9 ||
		service.preflightRequest.RequestedMode != backupasset.PublicationVersionedFullCopy {
		t.Fatalf("preflight status=%d request=%+v body=%s", recorder.Code, service.preflightRequest, recorder.Body.String())
	}
	for _, unsafe := range []string{"managed_root", "locator", "argv", "repository.json", "/private/"} {
		if strings.Contains(recorder.Body.String(), unsafe) {
			t.Fatalf("safe preflight response leaked %q: %s", unsafe, recorder.Body.String())
		}
	}

	invalidRecorder := httptest.NewRecorder()
	invalidCtx, _ := gin.CreateTestContext(invalidRecorder)
	invalidCtx.Params = gin.Params{{Key: "id", Value: "42"}}
	invalidCtx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tasks/42/rsync-versioning/preflights", bytes.NewBufferString(`{"expected_task_revision":9,"requested_mode":"versioned_full_copy","managed_root":"/private/root"}`))
	invalidCtx.Request.Header.Set("Content-Type", "application/json")
	handler.CreatePreflight(invalidCtx)
	if invalidRecorder.Code != http.StatusBadRequest || service.preflightCalls != 1 {
		t.Fatalf("unknown-field preflight status=%d calls=%d body=%s", invalidRecorder.Code, service.preflightCalls, invalidRecorder.Body.String())
	}

	stringRevisionRecorder := httptest.NewRecorder()
	stringRevisionCtx, _ := gin.CreateTestContext(stringRevisionRecorder)
	stringRevisionCtx.Params = gin.Params{{Key: "id", Value: "42"}}
	stringRevisionCtx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tasks/42/rsync-versioning/preflights", bytes.NewBufferString(`{"expected_task_revision":"9007199254740993","requested_mode":"versioned_full_copy"}`))
	stringRevisionCtx.Request.Header.Set("Content-Type", "application/json")
	handler.CreatePreflight(stringRevisionCtx)
	if stringRevisionRecorder.Code != http.StatusOK || service.preflightRequest.ExpectedTaskRevision != 9007199254740993 {
		t.Fatalf("string revision status=%d request=%+v body=%s", stringRevisionRecorder.Code, service.preflightRequest, stringRevisionRecorder.Body.String())
	}
}

func TestTaskRsyncVersioningHandlerMapsActivationConflictAndRollbackSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &taskRsyncVersioningServiceStub{activateErr: backupasset.ErrConflict}
	handler := NewTaskRsyncVersioningHandler(service)

	activateRecorder := httptest.NewRecorder()
	activateCtx, _ := gin.CreateTestContext(activateRecorder)
	activateCtx.Params = gin.Params{{Key: "id", Value: "7"}}
	activateCtx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tasks/7/rsync-versioning/activate", bytes.NewBufferString(`{"expected_task_revision":3,"preflight_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","migration_choice":"imported_baseline"}`))
	activateCtx.Request.Header.Set("Content-Type", "application/json")
	handler.Activate(activateCtx)
	if activateRecorder.Code != http.StatusConflict || !errors.Is(service.activateErr, backupasset.ErrConflict) ||
		service.activationRequest.TaskID != 7 || service.activationRequest.MigrationChoice != backupasset.RsyncVersioningImportedBaseline {
		t.Fatalf("activation status=%d request=%+v body=%s", activateRecorder.Code, service.activationRequest, activateRecorder.Body.String())
	}

	service.activateErr = nil
	service.rollbackResult = backupasset.RsyncVersioningRollbackPreparationResult{Summary: backupasset.RsyncVersioningSummary{
		Mode: backupasset.PublicationVersionedHardlink, State: backupasset.RsyncVersioningRollbackPrepared,
		ReasonCode: backupasset.RsyncVersioningReasonRollbackPrepared, CapabilityRevision: 8, TaskRevision: "9007199254740994",
	}}
	rollbackRecorder := httptest.NewRecorder()
	rollbackCtx, _ := gin.CreateTestContext(rollbackRecorder)
	rollbackCtx.Params = gin.Params{{Key: "id", Value: "7"}}
	rollbackCtx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tasks/7/rsync-versioning/rollback-preparations", bytes.NewBufferString(`{"expected_task_revision":4}`))
	rollbackCtx.Request.Header.Set("Content-Type", "application/json")
	handler.PrepareRollback(rollbackCtx)
	if rollbackRecorder.Code != http.StatusOK || service.rollbackRequest.TaskID != 7 || service.rollbackRequest.ExpectedTaskRevision != 4 ||
		!strings.Contains(rollbackRecorder.Body.String(), "rollback_prepared") || !strings.Contains(rollbackRecorder.Body.String(), "9007199254740994") {
		t.Fatalf("rollback status=%d request=%+v body=%s", rollbackRecorder.Code, service.rollbackRequest, rollbackRecorder.Body.String())
	}
}

func TestTaskHandlerProjectsOnlySafeRsyncVersioningSummary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatal(err)
	}
	node := model.Node{Name: "summary-node", Host: "127.0.0.1", Username: "root", AuthType: "password", Password: "FAKE_NODE_PASSWORD_FOR_TEST_ONLY"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	taskEntity := model.Task{Name: "summary-rsync", NodeID: node.ID, ExecutorType: "rsync", RsyncSource: "/data/source", RsyncTarget: "/data/legacy", Status: "pending"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatal(err)
	}
	service := &taskRsyncVersioningServiceStub{summaryResult: backupasset.RsyncVersioningSummary{
		Mode: backupasset.PublicationLegacyMutable, State: backupasset.RsyncVersioningBlocked,
		ReasonCode: backupasset.RsyncVersioningReasonUnsupported, CapabilityRevision: 3, TaskRevision: "9007199254740993",
	}}
	handler := NewTaskHandler(db, nil).WithRsyncVersioningService(service)
	router := gin.New()
	router.GET("/tasks/:id", handler.Get)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/tasks/"+strconv.Itoa(int(taskEntity.ID)), nil))
	if response.Code != http.StatusOK || service.summaryTaskID != taskEntity.ID {
		t.Fatalf("summary status=%d task_id=%d body=%s", response.Code, service.summaryTaskID, response.Body.String())
	}
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	publication, ok := envelope.Data["rsync_publication"]
	if !ok || !strings.Contains(string(publication), `"state":"blocked"`) || strings.Contains(string(publication), "managed_root") ||
		strings.Contains(string(publication), "locator") || strings.Contains(string(publication), "digest") {
		t.Fatalf("unsafe or missing Rsync summary=%s full=%s", publication, response.Body.String())
	}
	var summary struct {
		TaskRevision string `json:"task_revision"`
	}
	if err := json.Unmarshal(publication, &summary); err != nil || summary.TaskRevision != "9007199254740993" {
		t.Fatalf("missing safe Task revision summary=%s err=%v", publication, err)
	}
}

type taskRsyncVersioningServiceStub struct {
	preflightRequest  backupasset.RsyncVersioningPreflightRequest
	activationRequest backupasset.RsyncVersioningActivationRequest
	rollbackRequest   backupasset.RsyncVersioningRollbackPreparationRequest
	preflightResult   backupasset.RsyncVersioningPreflightResult
	activateResult    backupasset.RsyncVersioningActivationResult
	rollbackResult    backupasset.RsyncVersioningRollbackPreparationResult
	preflightErr      error
	activateErr       error
	rollbackErr       error
	summaryResult     backupasset.RsyncVersioningSummary
	summaryErr        error
	preflightCalls    int
	summaryTaskID     uint
}

func (service *taskRsyncVersioningServiceStub) CreateRsyncVersioningPreflightForRequest(_ context.Context, request backupasset.RsyncVersioningPreflightRequest, _ backuprepository.RequestContext) (backupasset.RsyncVersioningPreflightResult, error) {
	service.preflightCalls++
	service.preflightRequest = request
	return service.preflightResult, service.preflightErr
}

func (service *taskRsyncVersioningServiceStub) ActivateRsyncVersioningForRequest(_ context.Context, request backupasset.RsyncVersioningActivationRequest, _ backuprepository.RequestContext) (backupasset.RsyncVersioningActivationResult, error) {
	service.activationRequest = request
	return service.activateResult, service.activateErr
}

func (service *taskRsyncVersioningServiceStub) PrepareRsyncVersioningRollbackForRequest(_ context.Context, request backupasset.RsyncVersioningRollbackPreparationRequest, _ backuprepository.RequestContext) (backupasset.RsyncVersioningRollbackPreparationResult, error) {
	service.rollbackRequest = request
	return service.rollbackResult, service.rollbackErr
}

func (service *taskRsyncVersioningServiceStub) RsyncVersioningSummary(_ context.Context, taskID uint) (backupasset.RsyncVersioningSummary, error) {
	service.summaryTaskID = taskID
	return service.summaryResult, service.summaryErr
}
