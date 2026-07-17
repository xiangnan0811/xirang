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

func TestTaskRcloneVersioningHandlerBindsAllEightClosedRequestsWithoutEchoingSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &taskRcloneVersioningServiceStub{
		portableSetupResult: backupasset.RcloneBindingSetupResult{
			SetupID: strings.Repeat("a", 32), ExpiresAt: time.Date(2026, 7, 17, 1, 0, 0, 0, time.UTC),
		},
		nativeSetupResult: backupasset.RcloneBindingSetupResult{
			SetupID: strings.Repeat("b", 32), ExpiresAt: time.Date(2026, 7, 17, 1, 0, 0, 0, time.UTC),
			ExternalID: "xirang-" + strings.Repeat("c", 32),
		},
		summaryResult: validRcloneHandlerSummary(),
		preflightResult: backupasset.RcloneVersioningPreflightResult{
			PreflightID: strings.Repeat("d", 32), ExpiresAt: time.Date(2026, 7, 17, 1, 0, 0, 0, time.UTC),
			Summary: validRcloneHandlerSummary(),
		},
		activateResult: backupasset.RcloneVersioningActivationResult{
			Summary: validRcloneHandlerSummary(), MigrationChoice: backupasset.RcloneFirstNewPoint,
		},
		rollbackResult: backupasset.RcloneVersioningRollbackResult{Summary: validRcloneHandlerSummary()},
	}
	handler := NewTaskRcloneVersioningHandler(service)

	invoke := func(method, path, body string, action func(*gin.Context)) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Params = gin.Params{{Key: "id", Value: "42"}}
		ctx.Request = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		action(ctx)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s %s status=%d body=%s", method, path, recorder.Code, recorder.Body.String())
		}
		return recorder
	}

	invoke(http.MethodPost, "/api/v1/tasks/42/rclone-versioning/portable-binding-setups",
		`{"expected_task_revision":"9007199254740993"}`, handler.CreatePortableBindingSetup)
	if service.portableSetupRequest.TaskID != 42 || service.portableSetupRequest.ExpectedTaskRevision != 9007199254740993 {
		t.Fatalf("portable setup request=%+v", service.portableSetupRequest)
	}

	portableSecret := "FAKE_RCLONE_PORTABLE_BOUND_CONFIG_FOR_TEST_ONLY"
	portable := invoke(http.MethodPut, "/api/v1/tasks/42/rclone-versioning/portable-binding",
		`{"expected_task_revision":"9007199254740993","expected_binding_revision":"2","setup_id":"`+strings.Repeat("a", 32)+`","target_remote":"archive","managed_root_locator":"archive:managed/v1","bound_config":"`+portableSecret+`"}`,
		handler.SetPortableBinding)
	if service.portableBindingRequest.TaskID != 42 || service.portableBindingRequest.ExpectedBindingRevision != 2 ||
		service.portableBindingRequest.TargetRemote != "archive" || service.portableBindingRequest.ManagedRootLocator != "archive:managed/v1" ||
		service.portableBindingRequest.BoundConfig != portableSecret || strings.Contains(portable.Body.String(), portableSecret) ||
		strings.Contains(portable.Body.String(), "archive:managed/v1") {
		t.Fatalf("portable binding request=%+v response=%s", service.portableBindingRequest, portable.Body.String())
	}

	nativeSetup := invoke(http.MethodPost, "/api/v1/tasks/42/rclone-versioning/native-binding-setups",
		`{"expected_task_revision":"9007199254740993"}`, handler.CreateNativeBindingSetup)
	if service.nativeSetupRequest.TaskID != 42 || !strings.Contains(nativeSetup.Body.String(), service.nativeSetupResult.ExternalID) {
		t.Fatalf("native setup request=%+v response=%s", service.nativeSetupRequest, nativeSetup.Body.String())
	}

	accessKey := "FAKE_AWS_ACCESS_KEY_ID_FOR_TEST_ONLY"
	secretKey := "FAKE_AWS_SECRET_ACCESS_KEY_FOR_TEST_ONLY"
	keyARN := "arn:aws:kms:us-east-1:123456789012:key/FAKE-RCLONE-KMS-KEY-FOR-TEST-ONLY"
	native := invoke(http.MethodPut, "/api/v1/tasks/42/rclone-versioning/native-binding",
		`{"expected_task_revision":"9007199254740993","expected_binding_revision":"3","setup_id":"`+strings.Repeat("b", 32)+`","region":"us-east-1","bucket":"xirang-private-bucket","managed_prefix":"managed/v1/","role_arn":"arn:aws:iam::123456789012:role/xirang-rclone","bootstrap":{"mode":"static_sts_bootstrap","access_key_id":"`+accessKey+`","secret_access_key":"`+secretKey+`"},"encryption_profile":"sse_kms_cmk","kms_key_arn":"`+keyARN+`"}`,
		handler.SetNativeBinding)
	if service.nativeBindingRequest.TaskID != 42 || service.nativeBindingRequest.ExpectedBindingRevision != 3 ||
		service.nativeBindingRequest.Bootstrap.Mode != backupasset.RcloneBootstrapStaticSTS ||
		service.nativeBindingRequest.Bootstrap.AccessKeyID != accessKey || service.nativeBindingRequest.Bootstrap.SecretAccessKey != secretKey ||
		service.nativeBindingRequest.KMSKeyARN != keyARN {
		t.Fatalf("native binding request=%+v", service.nativeBindingRequest)
	}
	for _, forbidden := range []string{accessKey, secretKey, keyARN, "xirang-private-bucket", "managed/v1/", "role/xirang-rclone"} {
		if strings.Contains(native.Body.String(), forbidden) {
			t.Fatalf("native response leaked %q: %s", forbidden, native.Body.String())
		}
	}

	invoke(http.MethodPost, "/api/v1/tasks/42/rclone-versioning/preflights",
		`{"expected_task_revision":"9007199254740993","requested_mode":"versioned_prefix"}`, handler.CreatePreflight)
	if service.preflightRequest.TaskID != 42 || service.preflightRequest.RequestedMode != backupasset.PublicationVersionedPrefix {
		t.Fatalf("preflight request=%+v", service.preflightRequest)
	}

	invoke(http.MethodPost, "/api/v1/tasks/42/rclone-versioning/activate",
		`{"expected_task_revision":"9007199254740993","preflight_id":"`+strings.Repeat("d", 32)+`","migration_choice":"first_new_point"}`, handler.Activate)
	if service.activationRequest.TaskID != 42 || service.activationRequest.MigrationChoice != backupasset.RcloneFirstNewPoint {
		t.Fatalf("activation request=%+v", service.activationRequest)
	}

	invoke(http.MethodPost, "/api/v1/tasks/42/rclone-versioning/clean-rollbacks",
		`{"expected_task_revision":"9007199254740993","expected_binding_revision":"4"}`, handler.CleanRollback)
	if service.cleanRollbackRequest.TaskID != 42 || service.cleanRollbackRequest.ExpectedBindingRevision != 4 {
		t.Fatalf("clean rollback request=%+v", service.cleanRollbackRequest)
	}

	invoke(http.MethodPost, "/api/v1/tasks/42/rclone-versioning/rollback-preparations",
		`{"expected_task_revision":"9007199254740993","expected_binding_revision":"5"}`, handler.PrepareRollback)
	if service.rollbackPreparationRequest.TaskID != 42 || service.rollbackPreparationRequest.ExpectedBindingRevision != 5 {
		t.Fatalf("rollback preparation request=%+v", service.rollbackPreparationRequest)
	}
}

func TestTaskRcloneVersioningHandlerRejectsNonCanonicalBodiesBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &taskRcloneVersioningServiceStub{summaryResult: validRcloneHandlerSummary()}
	handler := NewTaskRcloneVersioningHandler(service)
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown", body: `{"expected_task_revision":"9","unexpected":true}`},
		{name: "duplicate", body: `{"expected_task_revision":"9","expected_task_revision":"10"}`},
		{name: "null", body: `{"expected_task_revision":null}`},
		{name: "trailing", body: `{"expected_task_revision":"9"} {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Params = gin.Params{{Key: "id", Value: "7"}}
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tasks/7/rclone-versioning/portable-binding-setups", strings.NewReader(test.body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			handler.CreatePortableBindingSetup(ctx)
			if recorder.Code != http.StatusBadRequest || service.portableSetupCalls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", recorder.Code, service.portableSetupCalls, recorder.Body.String())
			}
		})
	}

	oversized := strings.Repeat("x", 64<<10+1)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: "7"}}
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/v1/tasks/7/rclone-versioning/portable-binding", strings.NewReader(
		`{"expected_task_revision":"9","expected_binding_revision":"1","setup_id":"`+strings.Repeat("a", 32)+`","target_remote":"archive","managed_root_locator":"archive:managed","bound_config":"`+oversized+`"}`,
	))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.SetPortableBinding(ctx)
	if recorder.Code != http.StatusBadRequest || service.portableBindingCalls != 0 {
		t.Fatalf("oversized config status=%d calls=%d", recorder.Code, service.portableBindingCalls)
	}
}

func TestTaskRcloneVersioningHandlerMapsStableErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "forbidden", err: backupasset.ErrForbidden, want: http.StatusForbidden},
		{name: "not found", err: backupasset.ErrNotFound, want: http.StatusNotFound},
		{name: "conflict", err: backupasset.ErrConflict, want: http.StatusConflict},
		{name: "unsupported", err: backupasset.ErrCapabilityUnavailable, want: http.StatusNotImplemented},
		{name: "deadline", err: context.DeadlineExceeded, want: http.StatusServiceUnavailable},
		{name: "unexpected", err: errors.New("FAKE_PRIVATE_PROVIDER_ERROR_FOR_TEST_ONLY"), want: http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &taskRcloneVersioningServiceStub{portableSetupErr: test.err}
			handler := NewTaskRcloneVersioningHandler(service)
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Params = gin.Params{{Key: "id", Value: "7"}}
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tasks/7/rclone-versioning/portable-binding-setups", strings.NewReader(`{"expected_task_revision":"9"}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			handler.CreatePortableBindingSetup(ctx)
			if recorder.Code != test.want || strings.Contains(recorder.Body.String(), "FAKE_PRIVATE_PROVIDER_ERROR") {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, test.want, recorder.Body.String())
			}
		})
	}
}

func TestTaskHandlerProjectsOnlySafeRclonePublicationSummary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTaskHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatal(err)
	}
	node := model.Node{Name: "rclone-summary-node", Host: "127.0.0.1", Username: "root", AuthType: "password", Password: "FAKE_NODE_PASSWORD_FOR_TEST_ONLY"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	taskEntity := model.Task{Name: "summary-rclone", NodeID: node.ID, ExecutorType: "rclone", RsyncTarget: "archive:private", Status: "pending"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatal(err)
	}
	service := &taskRcloneVersioningServiceStub{summaryResult: validRcloneHandlerSummary()}
	handler := NewTaskHandler(db, nil).WithRcloneVersioningService(service)
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
	publication, ok := envelope.Data["rclone_publication"]
	if !ok || !strings.Contains(string(publication), `"task_revision":"9007199254740993"`) ||
		!strings.Contains(string(publication), `"state":"ready"`) {
		t.Fatalf("missing Rclone summary=%s full=%s", publication, response.Body.String())
	}
	for _, forbidden := range []string{
		"archive:private", `"target_remote":`, `"bound_config":`, `"bucket":`, `"managed_prefix":`,
		`"role_arn":`, `"kms_key_arn":`, `"version_id":`, `"digest":`,
	} {
		if strings.Contains(strings.ToLower(string(publication)), forbidden) {
			t.Fatalf("Rclone summary leaked %q: %s", forbidden, publication)
		}
	}
}

func validRcloneHandlerSummary() backupasset.RclonePublicationSummary {
	return backupasset.RclonePublicationSummary{
		Mode: backupasset.PublicationVersionedPrefix, State: backupasset.RcloneStateReady,
		ReasonCode: backupasset.RcloneReasonReady, TaskRevision: "9007199254740993", BindingRevision: "2",
		CapabilityRevision: "3", ConsistencyClass: backupasset.RcloneConsistencyObservationallyStable,
		HashFidelity: backupasset.RcloneHashDownloadVerifiedBytes, EstimatedReadBytes: "4096",
		APICostClass: backupasset.RcloneCostLow, StorageCostClass: backupasset.RcloneCostLow, EgressCostClass: backupasset.RcloneCostModerate,
		EncryptionProfile: backupasset.RcloneEncryptionNone, KMSKeyStatus: backupasset.RcloneKMSNotApplicable,
		RollbackLocatorPresent: true, RollbackCapability: backupasset.RcloneRollbackCleanAvailable,
	}
}

type taskRcloneVersioningServiceStub struct {
	portableSetupRequest       backupasset.RcloneBindingSetupRequest
	nativeSetupRequest         backupasset.RcloneBindingSetupRequest
	portableBindingRequest     backupasset.RclonePortableBindingRequest
	nativeBindingRequest       backupasset.RcloneNativeBindingRequest
	preflightRequest           backupasset.RcloneVersioningPreflightRequest
	activationRequest          backupasset.RcloneVersioningActivationRequest
	cleanRollbackRequest       backupasset.RcloneVersioningCleanRollbackRequest
	rollbackPreparationRequest backupasset.RcloneVersioningRollbackPreparationRequest
	portableSetupResult        backupasset.RcloneBindingSetupResult
	nativeSetupResult          backupasset.RcloneBindingSetupResult
	summaryResult              backupasset.RclonePublicationSummary
	preflightResult            backupasset.RcloneVersioningPreflightResult
	activateResult             backupasset.RcloneVersioningActivationResult
	rollbackResult             backupasset.RcloneVersioningRollbackResult
	portableSetupErr           error
	portableSetupCalls         int
	portableBindingCalls       int
	summaryTaskID              uint
}

func (service *taskRcloneVersioningServiceStub) CreateRclonePortableBindingSetupForRequest(_ context.Context, request backupasset.RcloneBindingSetupRequest, _ backuprepository.RequestContext) (backupasset.RcloneBindingSetupResult, error) {
	service.portableSetupCalls++
	service.portableSetupRequest = request
	return service.portableSetupResult, service.portableSetupErr
}

func (service *taskRcloneVersioningServiceStub) SetRclonePortableBindingForRequest(_ context.Context, request backupasset.RclonePortableBindingRequest, _ backuprepository.RequestContext) (backupasset.RclonePublicationSummary, error) {
	service.portableBindingCalls++
	service.portableBindingRequest = request
	return service.summaryResult, nil
}

func (service *taskRcloneVersioningServiceStub) CreateRcloneNativeBindingSetupForRequest(_ context.Context, request backupasset.RcloneBindingSetupRequest, _ backuprepository.RequestContext) (backupasset.RcloneBindingSetupResult, error) {
	service.nativeSetupRequest = request
	return service.nativeSetupResult, nil
}

func (service *taskRcloneVersioningServiceStub) SetRcloneNativeBindingForRequest(_ context.Context, request backupasset.RcloneNativeBindingRequest, _ backuprepository.RequestContext) (backupasset.RclonePublicationSummary, error) {
	service.nativeBindingRequest = request
	return service.summaryResult, nil
}

func (service *taskRcloneVersioningServiceStub) CreateRcloneVersioningPreflightForRequest(_ context.Context, request backupasset.RcloneVersioningPreflightRequest, _ backuprepository.RequestContext) (backupasset.RcloneVersioningPreflightResult, error) {
	service.preflightRequest = request
	return service.preflightResult, nil
}

func (service *taskRcloneVersioningServiceStub) ActivateRcloneVersioningForRequest(_ context.Context, request backupasset.RcloneVersioningActivationRequest, _ backuprepository.RequestContext) (backupasset.RcloneVersioningActivationResult, error) {
	service.activationRequest = request
	return service.activateResult, nil
}

func (service *taskRcloneVersioningServiceStub) CleanRollbackRcloneVersioningForRequest(_ context.Context, request backupasset.RcloneVersioningCleanRollbackRequest, _ backuprepository.RequestContext) (backupasset.RcloneVersioningRollbackResult, error) {
	service.cleanRollbackRequest = request
	return service.rollbackResult, nil
}

func (service *taskRcloneVersioningServiceStub) PrepareRcloneVersioningRollbackForRequest(_ context.Context, request backupasset.RcloneVersioningRollbackPreparationRequest, _ backuprepository.RequestContext) (backupasset.RcloneVersioningRollbackResult, error) {
	service.rollbackPreparationRequest = request
	return service.rollbackResult, nil
}

func (service *taskRcloneVersioningServiceStub) RcloneVersioningSummary(_ context.Context, taskID uint) (backupasset.RclonePublicationSummary, error) {
	service.summaryTaskID = taskID
	return service.summaryResult, nil
}
