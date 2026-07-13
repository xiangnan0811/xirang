package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset"
	backuprepository "xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
)

type fakeBackupRepositoryService struct {
	connectRequest   backuprepository.ConnectRequest
	listRequest      backuprepository.RepositoryListRequest
	detailID         string
	reconcileID      string
	disconnectID     string
	scope            backuprepository.VisibilityScope
	requestContext   backuprepository.RequestContext
	calls            int
	connectResult    backuprepository.ConnectResult
	listResult       backuprepository.RepositoryPage
	detailResult     backuprepository.RepositoryView
	reconcileResult  backuprepository.ConnectResult
	disconnectResult backuprepository.ConnectResult
	err              error
}

func (service *fakeBackupRepositoryService) Connect(_ context.Context, request backuprepository.ConnectRequest, requestContext backuprepository.RequestContext) (backuprepository.ConnectResult, error) {
	service.calls++
	service.connectRequest, service.requestContext = request, requestContext
	return service.connectResult, service.err
}
func (service *fakeBackupRepositoryService) List(_ context.Context, request backuprepository.RepositoryListRequest, scope backuprepository.VisibilityScope, requestContext backuprepository.RequestContext) (backuprepository.RepositoryPage, error) {
	service.calls++
	service.listRequest, service.scope, service.requestContext = request, scope, requestContext
	return service.listResult, service.err
}
func (service *fakeBackupRepositoryService) Detail(_ context.Context, id string, scope backuprepository.VisibilityScope, requestContext backuprepository.RequestContext) (backuprepository.RepositoryView, error) {
	service.calls++
	service.detailID, service.scope, service.requestContext = id, scope, requestContext
	return service.detailResult, service.err
}
func (service *fakeBackupRepositoryService) Reconcile(_ context.Context, id string, requestContext backuprepository.RequestContext) (backuprepository.ConnectResult, error) {
	service.calls++
	service.reconcileID, service.requestContext = id, requestContext
	return service.reconcileResult, service.err
}
func (service *fakeBackupRepositoryService) Disconnect(_ context.Context, id string, requestContext backuprepository.RequestContext) (backuprepository.ConnectResult, error) {
	service.calls++
	service.disconnectID, service.requestContext = id, requestContext
	return service.disconnectResult, service.err
}

func TestBackupRepositoryConnectStrictlyAcceptsOnlyTaskDerivedInput(t *testing.T) {
	service := &fakeBackupRepositoryService{}
	router := newBackupRepositoryHandlerTestRouter(service)
	valid := `{"task_id":42,"repository_id":"` + strings.Repeat("a", 32) + `","display_name":"archive","description":"safe","replace_access":true}`
	response := performBackupRepositoryHandlerRequest(t, router, http.MethodPost, "/backup-repositories/connect", valid)
	if response.Code != http.StatusOK || service.calls != 1 || service.connectRequest.TaskID != 42 || service.connectRequest.RepositoryID != strings.Repeat("a", 32) || !service.connectRequest.ReplaceAccess {
		t.Fatalf("status=%d request=%+v calls=%d body=%s", response.Code, service.connectRequest, service.calls, response.Body.String())
	}
	if service.requestContext.CorrelationID != "corr-handler" || service.requestContext.Actor.UserID != 7 || service.requestContext.Actor.Username != "admin-user" || service.requestContext.Actor.Role != "admin" {
		t.Fatalf("request context=%+v", service.requestContext)
	}

	for _, field := range []string{"provider", "path", "remote", "config", "password", "command"} {
		service.calls = 0
		body := `{"task_id":42,"` + field + `":"forbidden"}`
		response := performBackupRepositoryHandlerRequest(t, router, http.MethodPost, "/backup-repositories/connect", body)
		if response.Code != http.StatusBadRequest || service.calls != 0 {
			t.Fatalf("field=%s status=%d calls=%d body=%s", field, response.Code, service.calls, response.Body.String())
		}
	}
	service.calls = 0
	oversized := `{"task_id":42}` + strings.Repeat(" ", maxBackupRepositoryRequestBytes)
	response = performBackupRepositoryHandlerRequest(t, router, http.MethodPost, "/backup-repositories/connect", oversized)
	if response.Code != http.StatusBadRequest || service.calls != 0 {
		t.Fatalf("oversized request status=%d calls=%d", response.Code, service.calls)
	}
}

func TestBackupRepositoryListDetailAndEmptyBodyOperations(t *testing.T) {
	service := &fakeBackupRepositoryService{}
	router := newBackupRepositoryHandlerTestRouter(service)
	id := strings.Repeat("b", 32)
	response := performBackupRepositoryHandlerRequest(t, router, http.MethodGet, "/backup-repositories?limit=25&cursor=opaque-cursor", "")
	if response.Code != http.StatusOK || service.listRequest.Limit != 25 || service.listRequest.Cursor != "opaque-cursor" || service.scope.Role != "admin" || service.scope.UserID != 7 {
		t.Fatalf("list status=%d request=%+v scope=%+v", response.Code, service.listRequest, service.scope)
	}
	response = performBackupRepositoryHandlerRequest(t, router, http.MethodGet, "/backup-repositories/"+id, "")
	if response.Code != http.StatusOK || service.detailID != id {
		t.Fatalf("detail status=%d id=%q", response.Code, service.detailID)
	}
	response = performBackupRepositoryHandlerRequest(t, router, http.MethodPost, "/backup-repositories/"+id+"/reconcile", "{}")
	if response.Code != http.StatusBadRequest || service.reconcileID != "" {
		t.Fatalf("non-empty reconcile status=%d id=%q", response.Code, service.reconcileID)
	}
	response = performBackupRepositoryHandlerRequest(t, router, http.MethodPost, "/backup-repositories/"+id+"/reconcile", " \n\t")
	if response.Code != http.StatusOK || service.reconcileID != id {
		t.Fatalf("empty reconcile status=%d id=%q body=%s", response.Code, service.reconcileID, response.Body.String())
	}
	response = performBackupRepositoryHandlerRequest(t, router, http.MethodPost, "/backup-repositories/"+id+"/disconnect", `{"delete":true}`)
	if response.Code != http.StatusBadRequest || service.disconnectID != "" {
		t.Fatalf("non-empty disconnect status=%d id=%q", response.Code, service.disconnectID)
	}
}

func TestBackupRepositoryHandlerMapsTypedErrorsWithoutRawDetails(t *testing.T) {
	service := &fakeBackupRepositoryService{}
	router := newBackupRepositoryHandlerTestRouter(service)
	tests := []struct {
		err    error
		status int
	}{
		{backupasset.ErrInvalidState, http.StatusBadRequest},
		{backupasset.ErrNotFound, http.StatusNotFound},
		{backupasset.ErrConflict, http.StatusConflict},
		{&backuprepository.CapabilityError{Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityProviderProtocolIncompatible}, CorrelationID: "corr-safe"}, http.StatusNotImplemented},
		{&backuprepository.CapabilityError{Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityProviderOperationTimeout}, CorrelationID: "corr-safe"}, http.StatusServiceUnavailable},
		{errors.New("FAKE_DATABASE_PASSWORD_FOR_TEST_ONLY raw SQL failure"), http.StatusInternalServerError},
	}
	for _, test := range tests {
		service.err = test.err
		response := performBackupRepositoryHandlerRequest(t, router, http.MethodGet, "/backup-repositories", "")
		if response.Code != test.status {
			t.Fatalf("error=%v status=%d want=%d body=%s", test.err, response.Code, test.status, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "FAKE_DATABASE_PASSWORD_FOR_TEST_ONLY") || strings.Contains(response.Body.String(), "raw SQL") {
			t.Fatalf("raw error leaked: %s", response.Body.String())
		}
		if test.status == http.StatusNotImplemented || test.status == http.StatusServiceUnavailable {
			var envelope Response
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			data, ok := envelope.Data.(map[string]any)
			if !ok || data["correlation_id"] != "corr-safe" {
				t.Fatalf("typed error data=%#v", envelope.Data)
			}
		}
	}
}

func TestBackupRepositoryHandlerNilServiceFailsClosedForEveryRoute(t *testing.T) {
	router := newBackupRepositoryHandlerTestRouter(nil)
	id := strings.Repeat("b", 32)
	requests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/backup-repositories/connect", `{"task_id":42}`},
		{http.MethodGet, "/backup-repositories", ""},
		{http.MethodGet, "/backup-repositories/" + id, ""},
		{http.MethodPost, "/backup-repositories/" + id + "/reconcile", ""},
		{http.MethodPost, "/backup-repositories/" + id + "/disconnect", ""},
	}
	for _, request := range requests {
		response := performBackupRepositoryHandlerRequest(t, router, request.method, request.path, request.body)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("%s %s status=%d body=%s", request.method, request.path, response.Code, response.Body.String())
		}
	}
}

func TestBackupRepositoryHandlerStaysThinAndCommandFree(t *testing.T) {
	content, err := os.ReadFile("backup_repository_handler.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(content)
	for _, forbidden := range []string{
		"gorm.io/gorm",
		"/backupasset/provider",
		"/task/executor",
		"exec.Command",
		"CombinedOutput",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("backup repository handler contains forbidden dependency or command primitive %q", forbidden)
		}
	}
}

func newBackupRepositoryHandlerTestRouter(service BackupRepositoryService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(7))
		c.Set(middleware.CtxUsername, "admin-user")
		c.Set(middleware.CtxRole, "admin")
		c.Set(middleware.RequestIDKey, "corr-handler")
		c.Next()
	})
	handler := NewBackupRepositoryHandler(service)
	router.POST("/backup-repositories/connect", handler.Connect)
	router.GET("/backup-repositories", handler.List)
	router.GET("/backup-repositories/:id", handler.Detail)
	router.POST("/backup-repositories/:id/reconcile", handler.Reconcile)
	router.POST("/backup-repositories/:id/disconnect", handler.Disconnect)
	return router
}

func performBackupRepositoryHandlerRequest(t *testing.T, router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
