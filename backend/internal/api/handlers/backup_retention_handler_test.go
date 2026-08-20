package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/api/docs"
	"xirang/backend/internal/backupasset"
	backuprepository "xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/backupasset/retention"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	testRetentionPolicyID     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testRetentionPointID      = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testRetentionHoldID       = "cccccccccccccccccccccccccccccccc"
	testRetentionRepositoryID = "dddddddddddddddddddddddddddddddd"
	testRetentionPlanID       = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	testRetentionScopeID      = "ffffffffffffffffffffffffffffffff"
)

type fakeRetentionPolicyService struct {
	calls       int
	listRequest BackupRetentionPolicyListRequest
	createReq   BackupRetentionPolicyCreateRequest
	updateReq   BackupRetentionPolicyUpdateRequest
	deleteReq   BackupRetentionPolicyDeleteRequest
	impactReq   BackupRetentionImpactRequest
	page        BackupRetentionPolicyPage
	view        BackupRetentionPolicyView
	impact      BackupRetentionImpactView
	err         error
}

func (service *fakeRetentionPolicyService) List(_ context.Context, request BackupRetentionPolicyListRequest) (BackupRetentionPolicyPage, error) {
	service.calls++
	service.listRequest = request
	return service.page, service.err
}
func (service *fakeRetentionPolicyService) Create(_ context.Context, request BackupRetentionPolicyCreateRequest) (BackupRetentionPolicyView, error) {
	service.calls++
	service.createReq = request
	return service.view, service.err
}
func (service *fakeRetentionPolicyService) Update(_ context.Context, request BackupRetentionPolicyUpdateRequest) (BackupRetentionPolicyView, error) {
	service.calls++
	service.updateReq = request
	return service.view, service.err
}
func (service *fakeRetentionPolicyService) Delete(_ context.Context, request BackupRetentionPolicyDeleteRequest) (BackupRetentionPolicyView, error) {
	service.calls++
	service.deleteReq = request
	return service.view, service.err
}
func (service *fakeRetentionPolicyService) PreviewImpact(_ context.Context, request BackupRetentionImpactRequest) (BackupRetentionImpactView, error) {
	service.calls++
	service.impactReq = request
	return service.impact, service.err
}

type fakeRetentionHoldService struct {
	calls      int
	listReq    BackupRetentionHoldListRequest
	createReq  BackupRetentionHoldCreateRequest
	releaseReq BackupRetentionHoldReleaseRequest
	page       BackupRetentionHoldPage
	record     retention.HoldRecord
	err        error
}

func (service *fakeRetentionHoldService) List(_ context.Context, request BackupRetentionHoldListRequest) (BackupRetentionHoldPage, error) {
	service.calls++
	service.listReq = request
	if len(service.page.Items) == 0 && service.record.ID != "" {
		return BackupRetentionHoldPage{Items: []retention.HoldRecord{service.record}}, service.err
	}
	return service.page, service.err
}

func (service *fakeRetentionHoldService) Create(_ context.Context, request BackupRetentionHoldCreateRequest) (retention.HoldRecord, error) {
	service.calls++
	service.createReq = request
	return service.record, service.err
}
func (service *fakeRetentionHoldService) Release(_ context.Context, request BackupRetentionHoldReleaseRequest) (retention.HoldRecord, error) {
	service.calls++
	service.releaseReq = request
	return service.record, service.err
}

type fakeRetentionPurgeService struct {
	calls      int
	previewReq BackupRetentionPurgePreviewRequest
	planReq    BackupRetentionPurgePlanRequest
	execReq    BackupRetentionPurgeExecuteRequest
	preview    BackupRetentionPurgeImpactView
	plan       BackupRetentionPurgePlanView
	result     BackupRetentionPurgeResult
	err        error
}

func (service *fakeRetentionPurgeService) Preview(_ context.Context, request BackupRetentionPurgePreviewRequest) (BackupRetentionPurgeImpactView, error) {
	service.calls++
	service.previewReq = request
	return service.preview, service.err
}

func (service *fakeRetentionPurgeService) CreatePlan(_ context.Context, request BackupRetentionPurgePlanRequest) (BackupRetentionPurgePlanView, error) {
	service.calls++
	service.planReq = request
	return service.plan, service.err
}
func (service *fakeRetentionPurgeService) Execute(_ context.Context, request BackupRetentionPurgeExecuteRequest) (BackupRetentionPurgeResult, error) {
	service.calls++
	service.execReq = request
	return service.result, service.err
}

type recordingRetentionAudit struct {
	events []backupasset.AuditEventInput
	err    error
}

func (sink *recordingRetentionAudit) Write(_ context.Context, event backupasset.AuditEventInput) error {
	if sink.err != nil {
		return sink.err
	}
	sink.events = append(sink.events, event)
	return nil
}

func TestBackupRetentionHandlerFeatureGateFirstBlocksEveryRoute(t *testing.T) {
	policies := &fakeRetentionPolicyService{err: errors.New("must not reach policy service")}
	holds := &fakeRetentionHoldService{err: errors.New("must not reach hold service")}
	purge := &fakeRetentionPurgeService{err: errors.New("must not reach purge service")}
	disabled, err := backupAssetHandlerConfigEnabled()
	if err != nil {
		t.Fatal(err)
	}
	disabled.Enabled = false
	router := newBackupRetentionHandlerTestRouter(policies, holds, purge, nil, func() (BackupAssetHandlerConfig, error) {
		return disabled, nil
	})
	for _, request := range retentionHandlerRouteTable() {
		policies.calls, holds.calls, purge.calls = 0, 0, 0
		response := performBackupRetentionHandlerRequest(t, router, request.method, request.path, request.body)
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "feature_disabled") {
			t.Fatalf("%s %s status=%d body=%s", request.method, request.path, response.Code, response.Body.String())
		}
		if policies.calls != 0 || holds.calls != 0 || purge.calls != 0 {
			t.Fatalf("%s %s reached a service under a disabled feature gate", request.method, request.path)
		}
	}
}

func TestBackupRetentionHandlerStrictBodiesAndCursors(t *testing.T) {
	policies := &fakeRetentionPolicyService{view: sampleRetentionPolicyView(), impact: sampleRetentionImpactView()}
	holds := &fakeRetentionHoldService{record: sampleRetentionHoldRecord()}
	purge := &fakeRetentionPurgeService{plan: sampleRetentionPurgePlanView(), result: sampleRetentionPurgeResult()}
	router := newEnabledBackupRetentionHandlerTestRouter(policies, holds, purge, nil)

	validPolicy := `{"scope_kind":"repository","scope_id":"` + testRetentionScopeID + `","rules":{"version":1,"age":{"keep_days":30}}}`
	response := performBackupRetentionHandlerRequest(t, router, http.MethodPost, "/backup-retention-policies", validPolicy)
	if response.Code != http.StatusOK || policies.calls != 1 || policies.createReq.ScopeID != testRetentionScopeID {
		t.Fatalf("create status=%d calls=%d body=%s", response.Code, policies.calls, response.Body.String())
	}

	policies.calls = 0
	response = performBackupRetentionHandlerRequest(t, router, http.MethodPost, "/backup-retention-policies", `{"scope_kind":"repository","scope_id":"`+testRetentionScopeID+`","rules":{"version":1,"age":{"keep_days":30}},"locator":"/secret"}`)
	if response.Code != http.StatusBadRequest || policies.calls != 0 {
		t.Fatalf("unknown field status=%d calls=%d body=%s", response.Code, policies.calls, response.Body.String())
	}

	policies.calls = 0
	oversized := `{"scope_kind":"repository","scope_id":"` + testRetentionScopeID + `"}` + strings.Repeat(" ", maxBackupAssetRequestBytes)
	response = performBackupRetentionHandlerRequest(t, router, http.MethodPost, "/backup-retention-policies", oversized)
	if response.Code != http.StatusBadRequest || policies.calls != 0 {
		t.Fatalf("oversized status=%d calls=%d", response.Code, policies.calls)
	}

	policies.calls = 0
	response = performBackupRetentionHandlerRequest(t, router, http.MethodGet, "/backup-retention-policies?limit=0", "")
	if response.Code != http.StatusBadRequest || policies.calls != 0 {
		t.Fatalf("invalid limit status=%d calls=%d", response.Code, policies.calls)
	}
	response = performBackupRetentionHandlerRequest(t, router, http.MethodGet, "/backup-retention-policies?limit=20&cursor="+strings.Repeat("c", maxBackupAssetCursorBytes+1), "")
	if response.Code != http.StatusBadRequest || policies.calls != 0 {
		t.Fatalf("oversized cursor status=%d calls=%d", response.Code, policies.calls)
	}

	holds.calls = 0
	response = performBackupRetentionHandlerRequest(t, router, http.MethodPost, "/recovery-points/"+testRetentionPointID+"/holds", `{"hold_type":"operational","reason":"freeze","expires_at":"`+time.Now().UTC().Add(time.Hour).Format(time.RFC3339)+`","locator":"secret"}`)
	if response.Code != http.StatusBadRequest || holds.calls != 0 {
		t.Fatalf("hold unknown field status=%d calls=%d body=%s", response.Code, holds.calls, response.Body.String())
	}

	purge.calls = 0
	response = performBackupRetentionHandlerRequest(t, router, http.MethodPost, "/backup-repositories/"+testRetentionRepositoryID+"/purges", `{"plan_id":"`+testRetentionPlanID+`","expected_revision":1,"expected_impact_revision":1,"reason":"retire","proof":"cross-purpose"}`)
	if response.Code != http.StatusBadRequest || purge.calls != 0 {
		t.Fatalf("purge unknown proof field status=%d calls=%d body=%s", response.Code, purge.calls, response.Body.String())
	}
}

func TestBackupRetentionHandlerMapsSafeErrorEnvelopes(t *testing.T) {
	policies := &fakeRetentionPolicyService{}
	router := newEnabledBackupRetentionHandlerTestRouter(policies, &fakeRetentionHoldService{}, &fakeRetentionPurgeService{}, nil)
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{backupasset.ErrInvalidState, http.StatusBadRequest, ""},
		{backupasset.ErrNotFound, http.StatusNotFound, ""},
		{backupasset.ErrConflict, http.StatusConflict, ""},
		{&backuprepository.CapabilityError{Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityDeletionUnavailable}}, http.StatusNotImplemented, "deletion_unavailable"},
		{&backuprepository.CapabilityError{Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityProviderOperationTimeout}}, http.StatusServiceUnavailable, "provider_operation_timeout"},
		{errors.New("FAKE_DATABASE_PASSWORD_FOR_TEST_ONLY raw SQL"), http.StatusInternalServerError, ""},
	}
	for _, test := range cases {
		policies.err = test.err
		response := performBackupRetentionHandlerRequest(t, router, http.MethodGet, "/backup-retention-policies?limit=20", "")
		if response.Code != test.status {
			t.Fatalf("err=%v status=%d want=%d body=%s", test.err, response.Code, test.status, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "FAKE_DATABASE_PASSWORD_FOR_TEST_ONLY") || strings.Contains(response.Body.String(), "raw SQL") {
			t.Fatalf("raw error leaked: %s", response.Body.String())
		}
		if test.code != "" && !strings.Contains(response.Body.String(), test.code) {
			t.Fatalf("missing capability code %s in %s", test.code, response.Body.String())
		}
	}
}

func TestBackupRetentionHandlerImpactPlanDriftLeavesZeroEffects(t *testing.T) {
	policies := &fakeRetentionPolicyService{err: backupasset.ErrConflict}
	purge := &fakeRetentionPurgeService{err: backupasset.ErrConflict}
	router := newEnabledBackupRetentionHandlerTestRouter(policies, &fakeRetentionHoldService{}, purge, nil)

	response := performBackupRetentionHandlerRequest(t, router, http.MethodPost, "/backup-retention-policies/"+testRetentionPolicyID+"/impact", `{"expected_revision":2}`)
	if response.Code != http.StatusConflict || policies.calls != 1 {
		t.Fatalf("impact drift status=%d calls=%d body=%s", response.Code, policies.calls, response.Body.String())
	}

	response = performBackupRetentionHandlerRequest(t, router, http.MethodPost, "/backup-repositories/"+testRetentionRepositoryID+"/purges", `{"plan_id":"`+testRetentionPlanID+`","expected_revision":1,"expected_impact_revision":9,"reason":"retire stale plan"}`)
	if response.Code != http.StatusConflict || purge.calls != 1 {
		t.Fatalf("purge drift status=%d calls=%d body=%s", response.Code, purge.calls, response.Body.String())
	}
	if purge.execReq.PlanID != testRetentionPlanID || purge.execReq.ExpectedImpactRevision != 9 {
		t.Fatalf("purge execute request=%+v", purge.execReq)
	}
}

func TestBackupRetentionHandlerAuditUsesActionsAndCountsOnly(t *testing.T) {
	audit := &recordingRetentionAudit{}
	policies := &fakeRetentionPolicyService{impact: sampleRetentionImpactView()}
	holds := &fakeRetentionHoldService{record: sampleRetentionHoldRecord()}
	purge := &fakeRetentionPurgeService{plan: sampleRetentionPurgePlanView(), result: sampleRetentionPurgeResult()}
	router := newEnabledBackupRetentionHandlerTestRouter(policies, holds, purge, audit)

	performBackupRetentionHandlerRequest(t, router, http.MethodPost, "/backup-retention-policies/"+testRetentionPolicyID+"/impact", `{"expected_revision":1}`)
	performBackupRetentionHandlerRequest(t, router, http.MethodPost, "/recovery-points/"+testRetentionPointID+"/holds", `{"hold_type":"legal","reason":"private hold reason"}`)
	performBackupRetentionHandlerRequest(t, router, http.MethodPost, "/backup-repositories/"+testRetentionRepositoryID+"/purge-plans", `{"expected_impact_revision":1,"items":[{"recovery_point_id":"`+testRetentionPointID+`","point_revision":1,"capability_revision":1}]}`)

	if len(audit.events) != 3 {
		t.Fatalf("audit events=%d", len(audit.events))
	}
	if audit.events[0].Action != backupasset.AuditActionRetentionPolicyUpdate || audit.events[0].ItemCount != 1 {
		t.Fatalf("impact audit=%+v", audit.events[0])
	}
	if audit.events[1].Action != backupasset.AuditActionHoldCreate || audit.events[2].Action != backupasset.AuditActionRepositoryPurgePlan {
		t.Fatalf("hold/purge audit actions=%q/%q", audit.events[1].Action, audit.events[2].Action)
	}
	for _, event := range audit.events {
		payload, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		raw := string(payload)
		for _, leaked := range []string{"private hold reason", "locator", "X-Xirang-Step-Up", "/secret", "proof"} {
			if strings.Contains(raw, leaked) {
				t.Fatalf("audit leaked %q: %s", leaked, raw)
			}
		}
		if event.Action == "" {
			t.Fatal("audit action missing")
		}
	}
}

func TestBackupRetentionHandlerSuccessfulPolicyHoldAndPurgeShapes(t *testing.T) {
	policies := &fakeRetentionPolicyService{page: BackupRetentionPolicyPage{Items: []BackupRetentionPolicyView{sampleRetentionPolicyView()}}, view: sampleRetentionPolicyView(), impact: sampleRetentionImpactView()}
	holds := &fakeRetentionHoldService{record: sampleRetentionHoldRecord()}
	purge := &fakeRetentionPurgeService{plan: sampleRetentionPurgePlanView(), result: sampleRetentionPurgeResult()}
	router := newEnabledBackupRetentionHandlerTestRouter(policies, holds, purge, nil)

	response := performBackupRetentionHandlerRequest(t, router, http.MethodGet, "/backup-retention-policies?limit=25", "")
	if response.Code != http.StatusOK || policies.listRequest.Limit != 25 {
		t.Fatalf("list status=%d request=%+v body=%s", response.Code, policies.listRequest, response.Body.String())
	}
	response = performBackupRetentionHandlerRequest(t, router, http.MethodPatch, "/backup-retention-policies/"+testRetentionPolicyID, `{"expected_revision":1,"rules":{"version":1,"count":{"keep_latest":3}}}`)
	if response.Code != http.StatusOK || policies.updateReq.PolicyID != testRetentionPolicyID || policies.updateReq.ExpectedRevision != 1 {
		t.Fatalf("update status=%d request=%+v body=%s", response.Code, policies.updateReq, response.Body.String())
	}
	response = performBackupRetentionHandlerRequest(t, router, http.MethodDelete, "/backup-retention-policies/"+testRetentionPolicyID, `{"expected_revision":2}`)
	if response.Code != http.StatusOK || policies.deleteReq.ExpectedRevision != 2 {
		t.Fatalf("delete status=%d request=%+v", response.Code, policies.deleteReq)
	}
	response = performBackupRetentionHandlerRequest(t, router, http.MethodGet, "/recovery-points/"+testRetentionPointID+"/holds", "")
	if response.Code != http.StatusOK || holds.listReq.RecoveryPointID != testRetentionPointID {
		t.Fatalf("list holds status=%d request=%+v body=%s", response.Code, holds.listReq, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"items"`) || strings.Contains(response.Body.String(), "reason") {
		t.Fatalf("list holds leaked reason or omitted items: %s", response.Body.String())
	}
	response = performBackupRetentionHandlerRequest(t, router, http.MethodPost, "/recovery-points/"+testRetentionPointID+"/holds/"+testRetentionHoldID+"/release", `{"reason":"release after review"}`)
	if response.Code != http.StatusOK || holds.releaseReq.HoldID != testRetentionHoldID || holds.releaseReq.RecoveryPointID != testRetentionPointID {
		t.Fatalf("release status=%d request=%+v body=%s", response.Code, holds.releaseReq, response.Body.String())
	}
	if holds.releaseReq.Reason == "" {
		t.Fatal("release reason must reach the hold service")
	}
}

func TestBackupRetentionHandlerListErrorsDoNotAuditCreateOrUpdate(t *testing.T) {
	audit := &recordingRetentionAudit{}
	policies := &fakeRetentionPolicyService{err: backupasset.ErrNotFound}
	holds := &fakeRetentionHoldService{err: backupasset.ErrNotFound}
	router := newEnabledBackupRetentionHandlerTestRouter(policies, holds, &fakeRetentionPurgeService{}, audit)

	policyResponse := performBackupRetentionHandlerRequest(t, router, http.MethodGet, "/backup-retention-policies?limit=20", "")
	holdResponse := performBackupRetentionHandlerRequest(t, router, http.MethodGet, "/recovery-points/"+testRetentionPointID+"/holds", "")
	if policyResponse.Code != http.StatusNotFound || holdResponse.Code != http.StatusNotFound {
		t.Fatalf("list errors status policy=%d hold=%d", policyResponse.Code, holdResponse.Code)
	}
	if len(audit.events) != 2 {
		t.Fatalf("list error audit events=%d, want 2", len(audit.events))
	}
	if audit.events[0].Action != backupasset.AuditActionRetentionPolicyList {
		t.Fatalf("policy list error audit=%q, want %q", audit.events[0].Action, backupasset.AuditActionRetentionPolicyList)
	}
	if audit.events[1].Action != backupasset.AuditActionHoldList {
		t.Fatalf("hold list error audit=%q, want %q", audit.events[1].Action, backupasset.AuditActionHoldList)
	}
	for _, event := range audit.events {
		if event.Action == backupasset.AuditActionHoldCreate || event.Action == backupasset.AuditActionRetentionPolicyCreate ||
			event.Action == backupasset.AuditActionRetentionPolicyUpdate {
			t.Fatalf("list error audited as mutation: %q", event.Action)
		}
	}
}

func TestBackupRetentionHandlerDocumentsExactRoutes(t *testing.T) {
	document := readBackupAssetSwagger(t)
	raw := docs.SwaggerInfo.ReadDoc()
	for _, name := range []string{
		"internal_api_handlers.BackupRetentionPolicyView",
		"internal_api_handlers.BackupRetentionImpactView",
		"internal_api_handlers.BackupRetentionPurgeImpactView",
		"internal_api_handlers.BackupRetentionPurgePlanView",
		"internal_api_handlers.backupRetentionHoldCreatePayload",
		"internal_api_handlers.backupRetentionPurgeExecutePayload",
		"xirang_backend_internal_backupasset_retention.HoldRecord",
	} {
		schema := requireBackupAssetSwaggerDefinition(t, document, name)
		for field := range schema.Properties {
			if strings.Contains(field, "locator") || strings.Contains(field, "encrypted") || strings.Contains(field, "proof") {
				t.Fatalf("retention schema %s advertises private field %q", name, field)
			}
		}
	}
	for _, expected := range []struct {
		method string
		path   string
	}{
		{"get", "/backup-retention-policies"},
		{"post", "/backup-retention-policies"},
		{"patch", "/backup-retention-policies/{id}"},
		{"delete", "/backup-retention-policies/{id}"},
		{"post", "/backup-retention-policies/{id}/impact"},
		{"get", "/recovery-points/{id}/holds"},
		{"post", "/recovery-points/{id}/holds"},
		{"post", "/recovery-points/{id}/holds/{holdId}/release"},
		{"post", "/backup-repositories/{id}/import-scans"},
		{"get", "/backup-repositories/{id}/import-candidates"},
		{"post", "/backup-repositories/{id}/import-candidates/{candidateId}/reviews"},
		{"post", "/backup-repositories/{id}/rebuilds"},
		{"post", "/backup-repositories/{id}/purge-preview"},
		{"post", "/backup-repositories/{id}/purge-plans"},
		{"post", "/backup-repositories/{id}/purges"},
	} {
		if _, ok := document.Paths[expected.path][expected.method]; !ok {
			t.Fatalf("generated Swagger missing %s %s", strings.ToUpper(expected.method), expected.path)
		}
	}
	if !strings.Contains(raw, "retention.hold_release") || !strings.Contains(raw, "repository.purge") {
		t.Fatal("generated Swagger omitted pairwise retention/purge step-up action names")
	}
}

func newEnabledBackupRetentionHandlerTestRouter(
	policies BackupRetentionPolicyService,
	holds BackupRetentionHoldService,
	purge BackupRetentionPurgeService,
	audit BackupAssetAuditSink,
) *gin.Engine {
	return newBackupRetentionHandlerTestRouter(policies, holds, purge, audit, backupAssetHandlerConfigEnabled)
}

func newBackupRetentionHandlerTestRouter(
	policies BackupRetentionPolicyService,
	holds BackupRetentionHoldService,
	purge BackupRetentionPurgeService,
	audit BackupAssetAuditSink,
	config BackupAssetHandlerConfigSource,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(7))
		c.Set(middleware.CtxUsername, "admin-user")
		c.Set(middleware.CtxRole, "admin")
		c.Set(middleware.RequestIDKey, "corr-retention")
		c.Next()
	})
	handler := NewBackupRetentionHandler(policies, holds, purge, audit, config)
	router.GET("/backup-retention-policies", handler.ListPolicies)
	router.POST("/backup-retention-policies", handler.CreatePolicy)
	router.PATCH("/backup-retention-policies/:id", handler.UpdatePolicy)
	router.DELETE("/backup-retention-policies/:id", handler.DeletePolicy)
	router.POST("/backup-retention-policies/:id/impact", handler.PreviewImpact)
	router.GET("/recovery-points/:id/holds", handler.ListHolds)
	router.POST("/recovery-points/:id/holds", handler.CreateHold)
	router.POST("/recovery-points/:id/holds/:holdId/release", handler.ReleaseHold)
	router.POST("/backup-repositories/:id/purge-preview", handler.PreviewPurge)
	router.POST("/backup-repositories/:id/purge-plans", handler.CreatePurgePlan)
	router.POST("/backup-repositories/:id/purges", handler.ExecutePurge)
	return router
}

func performBackupRetentionHandlerRequest(t *testing.T, router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func retentionHandlerRouteTable() []struct {
	method string
	path   string
	body   string
} {
	return []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/backup-retention-policies?limit=20", ""},
		{http.MethodPost, "/backup-retention-policies", `{"scope_kind":"repository","scope_id":"` + testRetentionScopeID + `","rules":{"version":1,"age":{"keep_days":7}}}`},
		{http.MethodPatch, "/backup-retention-policies/" + testRetentionPolicyID, `{"expected_revision":1,"rules":{"version":1,"age":{"keep_days":7}}}`},
		{http.MethodDelete, "/backup-retention-policies/" + testRetentionPolicyID, `{"expected_revision":1}`},
		{http.MethodPost, "/backup-retention-policies/" + testRetentionPolicyID + "/impact", `{"expected_revision":1}`},
		{http.MethodGet, "/recovery-points/" + testRetentionPointID + "/holds", ""},
		{http.MethodPost, "/recovery-points/" + testRetentionPointID + "/holds", `{"hold_type":"legal","reason":"legal"}`},
		{http.MethodPost, "/recovery-points/" + testRetentionPointID + "/holds/" + testRetentionHoldID + "/release", `{"reason":"done"}`},
		{http.MethodPost, "/backup-repositories/" + testRetentionRepositoryID + "/purge-preview", `{"items":[{"recovery_point_id":"` + testRetentionPointID + `","point_revision":1,"capability_revision":1}]}`},
		{http.MethodPost, "/backup-repositories/" + testRetentionRepositoryID + "/purge-plans", `{"expected_impact_revision":1,"items":[{"recovery_point_id":"` + testRetentionPointID + `","point_revision":1,"capability_revision":1}]}`},
		{http.MethodPost, "/backup-repositories/" + testRetentionRepositoryID + "/purges", `{"plan_id":"` + testRetentionPlanID + `","expected_revision":1,"expected_impact_revision":1,"reason":"retire"}`},
	}
}

func sampleRetentionPolicyView() BackupRetentionPolicyView {
	return BackupRetentionPolicyView{
		ID: testRetentionPolicyID, ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: testRetentionScopeID,
		Revision: 1, Rules: retention.PolicyRules{Version: 1, Age: &retention.AgeRule{KeepDays: 30}},
		RuleDigest: strings.Repeat("1", 64), Status: backupasset.RetentionPolicyActive,
	}
}

func sampleRetentionImpactView() BackupRetentionImpactView {
	return BackupRetentionImpactView{
		PolicyID: testRetentionPolicyID, PolicyRevision: 1, ImpactRevision: 1,
		SelectedCount: 1, HoldCount: 0, LeaseCount: 0, WORMCount: 0,
		Points: []BackupRetentionImpactPoint{{
			RecoveryPointID: testRetentionPointID, PointRevision: 1, CapabilityRevision: 1,
		}},
	}
}

func sampleRetentionHoldRecord() retention.HoldRecord {
	return retention.HoldRecord{
		ID: testRetentionHoldID, RecoveryPointID: testRetentionPointID,
		HoldType: backupasset.RecoveryPointHoldLegal, State: backupasset.HoldActive, CreatedBy: 7,
	}
}

func sampleRetentionPurgePlanView() BackupRetentionPurgePlanView {
	return BackupRetentionPurgePlanView{
		ID: testRetentionPlanID, RepositoryID: testRetentionRepositoryID, Revision: 1, ImpactRevision: 1,
		Status: backupasset.PurgePlanReady, ItemCount: 1, HoldCount: 0, LeaseCount: 0, WORMCount: 0,
		Items: []BackupRetentionPurgePlanItemView{{
			RecoveryPointID: testRetentionPointID, PointRevision: 1, CapabilityRevision: 1,
		}},
	}
}

func sampleRetentionPurgeResult() BackupRetentionPurgeResult {
	return BackupRetentionPurgeResult{PlanID: testRetentionPlanID, Claimed: 1, Blocked: 0}
}

func TestBackupRetentionHandlerAuditWriteFailureIsVisible(t *testing.T) {
	audit := &recordingRetentionAudit{err: errors.New("audit sink unavailable")}
	policies := &fakeRetentionPolicyService{view: sampleRetentionPolicyView()}
	router := newEnabledBackupRetentionHandlerTestRouter(policies, &fakeRetentionHoldService{}, &fakeRetentionPurgeService{}, audit)
	validPolicy := `{"scope_kind":"repository","scope_id":"` + testRetentionScopeID + `","rules":{"version":1,"age":{"keep_days":30}}}`
	response := performBackupRetentionHandlerRequest(t, router, http.MethodPost, "/backup-retention-policies", validPolicy)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("audit write failure status=%d body=%s, want 500", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), `"code":0`) {
		t.Fatalf("audit write failure claimed success: %s", response.Body.String())
	}
}

func TestRetentionPolicyHTTPServiceListHonorsCursor(t *testing.T) {
	db := openConfigHandlerTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.BackupRepository{}, &model.BackupRetentionPolicy{}); err != nil {
		t.Fatalf("migrate retention cursor db: %v", err)
	}
	clock := time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC)
	ids := []string{strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32)}
	next := 0
	service, err := retention.NewPolicyService(retention.PolicyServiceDependencies{
		DB: db, Now: func() time.Time { return clock },
		NewID: func() (string, error) {
			id := ids[next]
			next++
			return id, nil
		},
	})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	if err := db.Create(&model.User{ID: 1, Username: "admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin"}).Error; err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	for index, repositoryID := range []string{strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)} {
		if err := db.Create(&model.BackupRepository{
			ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "cursor-repo",
			VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
			CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
		}).Error; err != nil {
			t.Fatalf("seed repository %d: %v", index, err)
		}
		if _, err := service.Create(context.Background(), retention.CreatePolicyRequest{
			Actor:     backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
			ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
			Rules: retention.PolicyRules{Version: retention.PolicyRulesVersion1, Age: &retention.AgeRule{KeepDays: 30}},
		}); err != nil {
			t.Fatalf("create policy %d: %v", index, err)
		}
	}
	adapter := retentionPolicyHTTPService{service: service}
	router := newEnabledBackupRetentionHandlerTestRouter(adapter, &fakeRetentionHoldService{}, &fakeRetentionPurgeService{}, &recordingRetentionAudit{})
	first := performBackupRetentionHandlerRequest(t, router, http.MethodGet, "/backup-retention-policies?limit=2", "")
	if first.Code != http.StatusOK {
		t.Fatalf("first page status=%d body=%s", first.Code, first.Body.String())
	}
	var firstEnvelope struct {
		Data BackupRetentionPolicyPage `json:"data"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstEnvelope); err != nil {
		t.Fatalf("parse first page: %v", err)
	}
	if len(firstEnvelope.Data.Items) != 2 || firstEnvelope.Data.NextCursor != strings.Repeat("2", 32) {
		t.Fatalf("first page=%+v, want 2 items and next cursor", firstEnvelope.Data)
	}
	second := performBackupRetentionHandlerRequest(t, router, http.MethodGet, "/backup-retention-policies?limit=2&cursor="+firstEnvelope.Data.NextCursor, "")
	if second.Code != http.StatusOK {
		t.Fatalf("second page status=%d body=%s", second.Code, second.Body.String())
	}
	var secondEnvelope struct {
		Data BackupRetentionPolicyPage `json:"data"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondEnvelope); err != nil {
		t.Fatalf("parse second page: %v", err)
	}
	if len(secondEnvelope.Data.Items) != 1 || secondEnvelope.Data.Items[0].ID != strings.Repeat("3", 32) || secondEnvelope.Data.NextCursor != "" {
		t.Fatalf("second page=%+v, want remaining policy 3", secondEnvelope.Data)
	}
}

type failingRetentionMutationAuditor struct{}

func (failingRetentionMutationAuditor) WriteTx(context.Context, *gorm.DB, backupasset.AuditEventInput) error {
	return errors.New("mutation audit unavailable")
}

type recordingRetentionMutationAuditor struct {
	writes int
	last   backupasset.AuditEventInput
	events []backupasset.AuditEventInput
}

func (auditor *recordingRetentionMutationAuditor) WriteTx(_ context.Context, _ *gorm.DB, event backupasset.AuditEventInput) error {
	auditor.writes++
	auditor.last = event
	auditor.events = append(auditor.events, event)
	return nil
}

func TestBackupRetentionHandlerPolicyCreateAuditFailureLeavesZeroRows(t *testing.T) {
	db := openConfigHandlerTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.BackupRepository{}, &model.BackupRetentionPolicy{}); err != nil {
		t.Fatalf("migrate retention atomic db: %v", err)
	}
	clock := time.Date(2026, 8, 19, 18, 20, 0, 0, time.UTC)
	service, err := retention.NewPolicyService(retention.PolicyServiceDependencies{
		DB: db, Now: func() time.Time { return clock },
		NewID: func() (string, error) { return strings.Repeat("9", 32), nil },
		Audit: failingRetentionMutationAuditor{},
	})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	if err := db.Create(&model.User{ID: 7, Username: "admin-user", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin"}).Error; err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	if err := db.Create(&model.BackupRepository{
		ID: testRetentionScopeID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "atomic-repo",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	}).Error; err != nil {
		t.Fatalf("seed repository: %v", err)
	}
	httpAudit := &recordingRetentionAudit{}
	router := newEnabledBackupRetentionHandlerTestRouter(
		retentionPolicyHTTPService{service: service},
		&fakeRetentionHoldService{},
		&fakeRetentionPurgeService{},
		httpAudit,
	)
	response := performBackupRetentionHandlerRequest(t, router, http.MethodPost, "/backup-retention-policies",
		`{"scope_kind":"repository","scope_id":"`+testRetentionScopeID+`","rules":{"version":1,"age":{"keep_days":30}}}`)
	if response.Code == http.StatusOK {
		t.Fatalf("create status=%d body=%s, want audit failure", response.Code, response.Body.String())
	}
	var count int64
	if err := db.Model(&model.BackupRetentionPolicy{}).Count(&count).Error; err != nil {
		t.Fatalf("count policies: %v", err)
	}
	if count != 0 {
		t.Fatalf("policy rows=%d, want 0 after HTTP mutation audit failure", count)
	}
}

func TestBackupRetentionHandlerPolicyCreateSuccessSkipsDuplicateHTTPAudit(t *testing.T) {
	db := openConfigHandlerTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.BackupRepository{}, &model.BackupRetentionPolicy{}); err != nil {
		t.Fatalf("migrate retention atomic db: %v", err)
	}
	clock := time.Date(2026, 8, 19, 18, 25, 0, 0, time.UTC)
	auditor := &recordingRetentionMutationAuditor{}
	service, err := retention.NewPolicyService(retention.PolicyServiceDependencies{
		DB: db, Now: func() time.Time { return clock },
		NewID: func() (string, error) { return strings.Repeat("8", 32), nil },
		Audit: auditor,
	})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	if err := db.Create(&model.User{ID: 7, Username: "admin-user", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin"}).Error; err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	if err := db.Create(&model.BackupRepository{
		ID: testRetentionScopeID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "atomic-repo",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	}).Error; err != nil {
		t.Fatalf("seed repository: %v", err)
	}
	httpAudit := &recordingRetentionAudit{}
	router := newEnabledBackupRetentionHandlerTestRouter(
		retentionPolicyHTTPService{service: service},
		&fakeRetentionHoldService{},
		&fakeRetentionPurgeService{},
		httpAudit,
	)
	response := performBackupRetentionHandlerRequest(t, router, http.MethodPost, "/backup-retention-policies",
		`{"scope_kind":"repository","scope_id":"`+testRetentionScopeID+`","rules":{"version":1,"age":{"keep_days":30}}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var count int64
	if err := db.Model(&model.BackupRetentionPolicy{}).Count(&count).Error; err != nil {
		t.Fatalf("count policies: %v", err)
	}
	if count != 1 || auditor.writes != 1 {
		t.Fatalf("policy rows=%d mutation audits=%d, want 1/1", count, auditor.writes)
	}
	for _, event := range httpAudit.events {
		if event.Action == backupasset.AuditActionRetentionPolicyCreate && event.Outcome == backupasset.AuditOutcomeSuccess {
			t.Fatalf("HTTP layer duplicated successful mutation audit: %+v", event)
		}
	}
}

func TestBackupRetentionHandlerHoldAndPurgeMutationsAuditCorrelationID(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	db := openConfigHandlerTestDB(t)
	if err := db.AutoMigrate(
		&model.User{},
		&model.BackupRepository{},
		&model.RecoveryPoint{},
		&model.RecoveryPointHold{},
		&model.RecoveryPointLease{},
		&model.RecoveryPointLifecycleAttempt{},
		&model.RecoveryPointLifecycleTombstone{},
		&model.BackupAssetPurgePlan{},
		&model.BackupAssetPurgePlanItem{},
	); err != nil {
		t.Fatalf("migrate hold/purge correlation db: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_recovery_point_leases_active_owner_slot
		ON recovery_point_leases(recovery_point_id, holder_type, owner_id) WHERE status = 'active'`).Error; err != nil {
		t.Fatalf("create lease owner index: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_recovery_point_lifecycle_attempts_active
		ON recovery_point_lifecycle_attempts(recovery_point_id) WHERE phase <> 'complete'`).Error; err != nil {
		t.Fatalf("create active lifecycle attempt index: %v", err)
	}
	clock := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	if err := db.Create(&model.User{ID: 7, Username: "admin-user", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin"}).Error; err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	if err := db.Create(&model.BackupRepository{
		ID: testRetentionRepositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "corr-repo",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	}).Error; err != nil {
		t.Fatalf("seed repository: %v", err)
	}
	captured := clock.Add(-24 * time.Hour)
	purgePointID := strings.Repeat("9", 32)
	for _, pointID := range []string{testRetentionPointID, purgePointID} {
		if err := db.Create(&model.RecoveryPoint{
			ID: pointID, RepositoryID: testRetentionRepositoryID,
			Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointCommitted),
			CapturedAt: &captured, CommittedAt: &captured, PointRevision: 1, CapabilityRevision: 1,
			CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
			PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		}).Error; err != nil {
			t.Fatalf("seed recovery point %s: %v", pointID, err)
		}
	}

	auditor := &recordingRetentionMutationAuditor{}
	holds, err := retention.NewHoldService(retention.HoldServiceDependencies{
		DB: db, Now: now, NewID: func() (string, error) { return testRetentionHoldID, nil }, Audit: auditor,
	})
	if err != nil {
		t.Fatalf("NewHoldService: %v", err)
	}
	leases, err := backupasset.NewLeaseService(db, now, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	coordinator, err := retention.NewCoordinator(retention.CoordinatorDependencies{
		DB: db, Leases: leases, Holds: holds, Now: now, LeaseOwnerID: "retention-worker",
		RetryDelay: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	purge, err := retention.NewPurgeService(coordinator)
	if err != nil {
		t.Fatalf("NewPurgeService: %v", err)
	}
	purge.SetMutationAuditor(auditor)
	router := newEnabledBackupRetentionHandlerTestRouter(
		&fakeRetentionPolicyService{},
		NewRetentionHoldHTTPService(holds),
		NewRetentionPurgeHTTPService(purge),
		&recordingRetentionAudit{},
	)

	holdResponse := performBackupRetentionHandlerRequest(t, router, http.MethodPost, "/recovery-points/"+testRetentionPointID+"/holds",
		`{"hold_type":"legal","reason":"legal"}`)
	if holdResponse.Code != http.StatusOK {
		t.Fatalf("create hold status=%d body=%s", holdResponse.Code, holdResponse.Body.String())
	}
	if correlation := mutationAuditCorrelation(auditor, backupasset.AuditActionHoldCreate); correlation != "corr-retention" {
		t.Fatalf("hold create correlation=%q, want corr-retention", correlation)
	}

	preview := performBackupRetentionHandlerRequest(t, router, http.MethodPost, "/backup-repositories/"+testRetentionRepositoryID+"/purge-preview",
		`{"items":[{"recovery_point_id":"`+purgePointID+`","point_revision":1,"capability_revision":1}]}`)
	if preview.Code != http.StatusOK {
		t.Fatalf("purge preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	var previewEnvelope struct {
		Data BackupRetentionPurgeImpactView `json:"data"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &previewEnvelope); err != nil {
		t.Fatalf("parse purge preview: %v", err)
	}
	planResponse := performBackupRetentionHandlerRequest(t, router, http.MethodPost, "/backup-repositories/"+testRetentionRepositoryID+"/purge-plans",
		`{"expected_impact_revision":`+strconv.FormatInt(previewEnvelope.Data.ImpactRevision, 10)+`,"items":[{"recovery_point_id":"`+purgePointID+`","point_revision":1,"capability_revision":1}]}`)
	if planResponse.Code != http.StatusOK {
		t.Fatalf("create purge plan status=%d body=%s", planResponse.Code, planResponse.Body.String())
	}
	if correlation := mutationAuditCorrelation(auditor, backupasset.AuditActionRepositoryPurgePlan); correlation != "corr-retention" {
		t.Fatalf("purge plan correlation=%q, want corr-retention", correlation)
	}
}

func mutationAuditCorrelation(auditor *recordingRetentionMutationAuditor, action backupasset.AuditAction) string {
	for i := len(auditor.events) - 1; i >= 0; i-- {
		if auditor.events[i].Action == action {
			value, _ := auditor.events[i].Fields[backupasset.AuditFieldCorrelationID].(string)
			return value
		}
	}
	return ""
}
