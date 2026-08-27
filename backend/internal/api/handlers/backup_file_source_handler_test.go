package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
)

type backupFileSourceServiceSpy struct {
	calls           int
	nodeID          uint
	backupSetID     string
	recoveryPointID string
	scope           catalog.AuthorizationScope
	request         catalog.FileSourcePageRequest
	resolution      catalog.FileSourceRecoveryPointDTO
	err             error
}

type backupFileSourceAuditSpy struct {
	inputs []backupasset.AuditEventInput
}

func (spy *backupFileSourceAuditSpy) Write(_ context.Context, input backupasset.AuditEventInput) error {
	spy.inputs = append(spy.inputs, input)
	return nil
}

func (spy *backupFileSourceServiceSpy) ListFileSourceNodes(_ context.Context, scope catalog.AuthorizationScope, request catalog.FileSourcePageRequest) (catalog.FileSourceNodePage, error) {
	spy.calls++
	spy.scope, spy.request = scope, request
	return catalog.FileSourceNodePage{Items: []catalog.FileSourceNodeDTO{}}, spy.err
}

func (spy *backupFileSourceServiceSpy) ListFileSourceBackupSets(_ context.Context, nodeID uint, scope catalog.AuthorizationScope, request catalog.FileSourcePageRequest) (catalog.FileSourceBackupSetPage, error) {
	spy.calls++
	spy.nodeID, spy.scope, spy.request = nodeID, scope, request
	return catalog.FileSourceBackupSetPage{Items: []catalog.FileSourceBackupSetDTO{}}, spy.err
}

func (spy *backupFileSourceServiceSpy) ListFileSourceVersions(_ context.Context, backupSetID string, scope catalog.AuthorizationScope, request catalog.FileSourcePageRequest) (catalog.FileSourceVersionPage, error) {
	spy.calls++
	spy.backupSetID, spy.scope, spy.request = backupSetID, scope, request
	return catalog.FileSourceVersionPage{Items: []catalog.FileSourceVersionDTO{}}, spy.err
}

func (spy *backupFileSourceServiceSpy) ResolveFileSourceRecoveryPoint(_ context.Context, recoveryPointID string, scope catalog.AuthorizationScope) (catalog.FileSourceRecoveryPointDTO, error) {
	spy.calls++
	spy.recoveryPointID, spy.scope = recoveryPointID, scope
	return spy.resolution, spy.err
}

func TestBackupFileSourceHandlerListsNodesWithStandardEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &backupFileSourceServiceSpy{}
	handler := NewBackupFileSourceHandler(service, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(77))
		c.Set(middleware.CtxRole, "operator")
	})
	router.GET("/backup-file-sources/nodes", handler.ListNodes)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/backup-file-sources/nodes?limit=2&cursor=signed-cursor", nil))
	if response.Code != http.StatusOK || service.calls != 1 || service.scope != (catalog.AuthorizationScope{Role: "operator", UserID: 77}) ||
		service.request != (catalog.FileSourcePageRequest{Limit: 2, Cursor: "signed-cursor"}) {
		t.Fatalf("status=%d body=%s calls=%d scope=%+v request=%+v", response.Code, response.Body.String(), service.calls, service.scope, service.request)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content type=%q", got)
	}
}

func TestBackupFileSourceHandlerListsNodeBackupSets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &backupFileSourceServiceSpy{}
	handler := NewBackupFileSourceHandler(service, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(78))
		c.Set(middleware.CtxRole, "admin")
	})
	router.GET("/backup-file-sources/nodes/:nodeId/sets", handler.ListBackupSets)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/backup-file-sources/nodes/6201/sets?limit=3", nil))
	if response.Code != http.StatusOK || service.calls != 1 || service.nodeID != 6201 ||
		service.scope != (catalog.AuthorizationScope{Role: "admin", UserID: 78}) || service.request.Limit != 3 {
		t.Fatalf("status=%d body=%s calls=%d node=%d scope=%+v request=%+v", response.Code, response.Body.String(), service.calls, service.nodeID, service.scope, service.request)
	}
}

func TestBackupFileSourceHandlerListsBackupSetVersions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &backupFileSourceServiceSpy{}
	handler := NewBackupFileSourceHandler(service, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(79))
		c.Set(middleware.CtxRole, "operator")
	})
	router.GET("/backup-file-sources/sets/:backupSetId/versions", handler.ListVersions)
	backupSetID := strings.Repeat("a", 32)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/backup-file-sources/sets/"+backupSetID+"/versions?cursor=next", nil))
	if response.Code != http.StatusOK || service.calls != 1 || service.backupSetID != backupSetID ||
		service.scope != (catalog.AuthorizationScope{Role: "operator", UserID: 79}) || service.request.Cursor != "next" {
		t.Fatalf("status=%d body=%s calls=%d set=%q scope=%+v request=%+v", response.Code, response.Body.String(), service.calls, service.backupSetID, service.scope, service.request)
	}
}

func TestBackupFileSourceHandlerResolvesExactRecoveryPointSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pointID := strings.Repeat("b", 32)
	service := &backupFileSourceServiceSpy{resolution: catalog.FileSourceRecoveryPointDTO{
		NodeID: 7, BackupSetID: strings.Repeat("a", 32), RecoveryPointID: pointID,
		RepositoryID: strings.Repeat("c", 32), ProducingTaskID: uintPointer(9),
	}}
	audit := &backupFileSourceAuditSpy{}
	handler := NewBackupFileSourceHandler(service, audit)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(79))
		c.Set(middleware.CtxRole, "operator")
	})
	router.GET("/backup-file-sources/recovery-points/:recoveryPointId/source", handler.ResolveRecoveryPointSource)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/backup-file-sources/recovery-points/"+pointID+"/source", nil))
	if response.Code != http.StatusOK || service.calls != 1 || service.recoveryPointID != pointID ||
		service.scope != (catalog.AuthorizationScope{Role: "operator", UserID: 79}) {
		t.Fatalf("status=%d body=%s calls=%d point=%q scope=%+v", response.Code, response.Body.String(), service.calls, service.recoveryPointID, service.scope)
	}
	if len(audit.inputs) != 1 || audit.inputs[0].Outcome != backupasset.AuditOutcomeSuccess || audit.inputs[0].ItemCount != 1 {
		t.Fatalf("audit=%+v", audit.inputs)
	}
	for _, forbidden := range []string{"provider_locator", "path", "content", "credential", "token", "proof"} {
		if strings.Contains(strings.ToLower(response.Body.String()), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestBackupFileSourceHandlerMapsResolverVisibilityAndInternalErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pointID := strings.Repeat("b", 32)
	for _, test := range []struct {
		name, code string
		err        error
		want       int
		outcome    backupasset.AuditOutcome
	}{
		{"hidden point", "not_found", fmt.Errorf("%w: SECRET_PROVIDER_LOCATOR", backupasset.ErrNotFound), http.StatusNotFound, backupasset.AuditOutcomeBlocked},
		{"identity collision", "internal_error", fmt.Errorf("%w: SECRET_PROVIDER_LOCATOR", catalog.ErrIdentityCollision), http.StatusInternalServerError, backupasset.AuditOutcomeFailure},
		{"invalid projection", "internal_error", fmt.Errorf("%w: SECRET_PROVIDER_LOCATOR", catalog.ErrUnknownInternalState), http.StatusInternalServerError, backupasset.AuditOutcomeFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &backupFileSourceServiceSpy{err: test.err}
			audit := &backupFileSourceAuditSpy{}
			handler := NewBackupFileSourceHandler(service, audit)
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(middleware.CtxUserID, uint(79))
				c.Set(middleware.CtxRole, "operator")
			})
			router.GET("/backup-file-sources/recovery-points/:recoveryPointId/source", handler.ResolveRecoveryPointSource)

			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/backup-file-sources/recovery-points/"+pointID+"/source", nil))
			if response.Code != test.want || service.calls != 1 || strings.Contains(response.Body.String(), "SECRET_PROVIDER_LOCATOR") {
				t.Fatalf("status=%d want=%d body=%s calls=%d", response.Code, test.want, response.Body.String(), service.calls)
			}
			if len(audit.inputs) != 1 || audit.inputs[0].Outcome != test.outcome || audit.inputs[0].FailureCode != test.code {
				t.Fatalf("audit=%+v", audit.inputs)
			}
			payload, err := json.Marshal(audit.inputs)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(payload, []byte("SECRET_PROVIDER_LOCATOR")) {
				t.Fatalf("audit leaked resolver error detail: %s", payload)
			}
		})
	}
}

func TestBackupFileSourceHandlerRejectsInvalidInputsBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	backupSetID := strings.Repeat("a", 32)
	for _, test := range []struct {
		name, route, path string
		register          func(*gin.Engine, *BackupFileSourceHandler)
	}{
		{"unknown query", "/backup-file-sources/nodes", "/backup-file-sources/nodes?path=/SECRET", func(router *gin.Engine, handler *BackupFileSourceHandler) {
			router.GET("/backup-file-sources/nodes", handler.ListNodes)
		}},
		{"duplicate limit", "/backup-file-sources/nodes", "/backup-file-sources/nodes?limit=1&limit=2", func(router *gin.Engine, handler *BackupFileSourceHandler) {
			router.GET("/backup-file-sources/nodes", handler.ListNodes)
		}},
		{"oversize limit", "/backup-file-sources/nodes", "/backup-file-sources/nodes?limit=201", func(router *gin.Engine, handler *BackupFileSourceHandler) {
			router.GET("/backup-file-sources/nodes", handler.ListNodes)
		}},
		{"noncanonical node", "/backup-file-sources/nodes/:nodeId/sets", "/backup-file-sources/nodes/01/sets", func(router *gin.Engine, handler *BackupFileSourceHandler) {
			router.GET("/backup-file-sources/nodes/:nodeId/sets", handler.ListBackupSets)
		}},
		{"invalid Backup Set", "/backup-file-sources/sets/:backupSetId/versions", "/backup-file-sources/sets/latest/versions", func(router *gin.Engine, handler *BackupFileSourceHandler) {
			router.GET("/backup-file-sources/sets/:backupSetId/versions", handler.ListVersions)
		}},
		{"unknown version query", "/backup-file-sources/sets/:backupSetId/versions", "/backup-file-sources/sets/" + backupSetID + "/versions?repository_id=SECRET", func(router *gin.Engine, handler *BackupFileSourceHandler) {
			router.GET("/backup-file-sources/sets/:backupSetId/versions", handler.ListVersions)
		}},
		{"invalid recovery point", "/backup-file-sources/recovery-points/:recoveryPointId/source", "/backup-file-sources/recovery-points/latest/source", func(router *gin.Engine, handler *BackupFileSourceHandler) {
			router.GET("/backup-file-sources/recovery-points/:recoveryPointId/source", handler.ResolveRecoveryPointSource)
		}},
		{"unknown resolver query", "/backup-file-sources/recovery-points/:recoveryPointId/source", "/backup-file-sources/recovery-points/" + strings.Repeat("b", 32) + "/source?locator=SECRET", func(router *gin.Engine, handler *BackupFileSourceHandler) {
			router.GET("/backup-file-sources/recovery-points/:recoveryPointId/source", handler.ResolveRecoveryPointSource)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &backupFileSourceServiceSpy{}
			handler := NewBackupFileSourceHandler(service, nil)
			router := gin.New()
			test.register(router, handler)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusBadRequest || service.calls != 0 || strings.Contains(response.Body.String(), "SECRET") {
				t.Fatalf("status=%d body=%s calls=%d", response.Code, response.Body.String(), service.calls)
			}
		})
	}
}

func uintPointer(value uint) *uint { return &value }

func TestBackupFileSourceHandlerMapsFailClosedErrorsWithoutEcho(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{"invalid cursor", catalog.ErrInvalidCursor, http.StatusBadRequest},
		{"forbidden", backupasset.ErrForbidden, http.StatusForbidden},
		{"not found", backupasset.ErrNotFound, http.StatusNotFound},
		{"stale cursor", catalog.ErrStaleCursor, http.StatusConflict},
		{"projection limit", catalog.ErrOwnershipProjectionLimit, http.StatusServiceUnavailable},
		{"feature disabled", catalog.ErrFeatureDisabled, http.StatusServiceUnavailable},
		{"invalid internal state", backupasset.ErrInvalidState, http.StatusInternalServerError},
		{"invalid internal projection", catalog.ErrInvalidCatalogContract, http.StatusInternalServerError},
		{"internal", errors.New("SECRET_PROVIDER_LOCATOR"), http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &backupFileSourceServiceSpy{err: test.err}
			handler := NewBackupFileSourceHandler(service, nil)
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(middleware.CtxUserID, uint(80))
				c.Set(middleware.CtxRole, "operator")
			})
			router.GET("/backup-file-sources/nodes", handler.ListNodes)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/backup-file-sources/nodes", nil))
			if response.Code != test.want || strings.Contains(response.Body.String(), "SECRET_PROVIDER_LOCATOR") {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestBackupFileSourceHandlerTreatsInvalidServiceStateAsInternalAndDoesNotAuditCursor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var service *catalog.Service
	audit := &backupFileSourceAuditSpy{}
	handler := NewBackupFileSourceHandler(service, audit)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(81))
		c.Set(middleware.CtxRole, "operator")
	})
	router.GET("/backup-file-sources/nodes", handler.ListNodes)

	const cursorCanary = "SECRET_PROVIDER_LOCATOR_CURSOR"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/backup-file-sources/nodes?cursor="+cursorCanary, nil))
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), cursorCanary) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(audit.inputs) != 1 || audit.inputs[0].Outcome != backupasset.AuditOutcomeFailure || audit.inputs[0].FailureCode != "internal_error" {
		t.Fatalf("audit=%+v", audit.inputs)
	}
	payload, err := json.Marshal(audit.inputs)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte(cursorCanary)) {
		t.Fatalf("audit leaked request cursor: %s", payload)
	}
}

func TestBackupFileSourceHandlerHasNoProviderCommandDependency(t *testing.T) {
	path := filepath.Join("backup_file_source_handler.go")
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, payload, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range file.Imports {
		importPath := strings.Trim(spec.Path.Value, `"`)
		for _, forbidden := range []string{"/provider", "/runner", "/sshutil", "os/exec", "os"} {
			if importPath == forbidden || strings.Contains(importPath, forbidden) {
				t.Fatalf("handler imports forbidden command boundary %q", importPath)
			}
		}
	}
	for _, forbidden := range [][]byte{[]byte("exec.Command"), []byte("ProviderLocator"), []byte("EncryptedProviderLocator")} {
		if bytes.Contains(payload, forbidden) {
			t.Fatalf("handler source contains forbidden %q", forbidden)
		}
	}
}
