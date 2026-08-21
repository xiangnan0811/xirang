package handlers

import (
	"bytes"
	"context"
	"errors"
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
	"xirang/backend/internal/backupasset/overlay"
	assetsearch "xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
)

type backupAssetSearchServiceSpy struct {
	calls    int
	actor    assetsearch.SearchActor
	request  assetsearch.SearchRequest
	response assetsearch.SearchResponse
	err      error
}

func (spy *backupAssetSearchServiceSpy) Search(_ context.Context, actor assetsearch.SearchActor, request assetsearch.SearchRequest) (assetsearch.SearchResponse, error) {
	spy.calls++
	spy.actor = actor
	spy.request = request
	return spy.response, spy.err
}

type backupAssetSavedSearchUseSpy struct {
	calls int
	actor overlay.Actor
	id    string
	saved overlay.SavedSearch
	err   error
}

func (spy *backupAssetSavedSearchUseSpy) UseSavedSearch(_ context.Context, actor overlay.Actor, id string) (overlay.SavedSearch, error) {
	spy.calls++
	spy.actor, spy.id = actor, id
	return spy.saved, spy.err
}

func TestAssetSearchHandlerStrictInlineAndSavedRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pointID := strings.Repeat("1", 32)
	savedID := strings.Repeat("2", 32)
	baseQuery := assetsearch.SearchRequest{
		SchemaVersion: assetsearch.QuerySchemaVersion,
		Root:          assetsearch.QueryNode{Op: assetsearch.QueryOpTerm, Field: assetsearch.SearchFieldName, Text: "Private Report"},
		Scope:         assetsearch.SearchScope{Mode: assetsearch.SearchScopeExactPoints, RecoveryPointIDs: []string{pointID}},
		Sort:          assetsearch.SearchSortRelevance,
		Limit:         10,
	}

	for _, test := range []struct {
		name          string
		body          string
		configure     func(*backupAssetSavedSearchUseSpy)
		wantSavedCall int
		wantLimit     int
		wantCursor    string
	}{
		{
			name:      "inline",
			body:      `{"query":{"schema_version":1,"root":{"op":"term","field":"name","text":"Private Report"},"scope":{"mode":"exact_points","recovery_point_ids":["` + pointID + `"]},"sort":"relevance","limit":10}}`,
			wantLimit: 10,
		},
		{
			name: "saved",
			body: `{"saved_search_id":"` + savedID + `","limit":5,"cursor":"opaque-page"}`,
			configure: func(spy *backupAssetSavedSearchUseSpy) {
				spy.saved = overlay.SavedSearch{ID: savedID, OwnerUserID: 77, Query: baseQuery, State: overlay.SavedSearchActive}
			},
			wantSavedCall: 1,
			wantLimit:     5,
			wantCursor:    "opaque-page",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			searchSpy := &backupAssetSearchServiceSpy{response: assetsearch.SearchResponse{Items: []assetsearch.SearchHit{}}}
			savedSpy := &backupAssetSavedSearchUseSpy{}
			if test.configure != nil {
				test.configure(savedSpy)
			}
			audit := &backupAssetAuditSpy{}
			proofCalls := 0
			handler := NewBackupAssetSearchHandler(searchSpy, savedSpy, audit, backupAssetHandlerConfigEnabled,
				func(*gin.Context) (*assetsearch.SecretRevealProof, error) {
					proofCalls++
					return &assetsearch.SecretRevealProof{ID: strings.Repeat("3", 32)}, nil
				})
			router := backupAssetHandlerTestRouterWithRole("admin")
			router.POST("/asset-search", handler.Search)
			request := httptest.NewRequest(http.MethodPost, "/asset-search", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(StepUpHeaderName, "opaque-proof")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK || searchSpy.calls != 1 || savedSpy.calls != test.wantSavedCall || proofCalls != 1 {
				t.Fatalf("status=%d body=%s search=%d saved=%d proof=%d", response.Code, response.Body.String(), searchSpy.calls, savedSpy.calls, proofCalls)
			}
			if searchSpy.request.Limit != test.wantLimit || searchSpy.request.Cursor != test.wantCursor ||
				searchSpy.actor.Authorization != (catalog.AuthorizationScope{UserID: 77, Role: "admin"}) || searchSpy.actor.SecretProof == nil {
				t.Fatalf("request=%+v actor=%+v", searchSpy.request, searchSpy.actor)
			}
			expectedAudits := 1 + test.wantSavedCall
			searchAudit := audit.inputs[len(audit.inputs)-1]
			if len(audit.inputs) != expectedAudits || searchAudit.Action != backupasset.AuditActionAssetSearch || searchAudit.Fingerprints.Query == "" ||
				searchAudit.StepUpProofID != strings.Repeat("3", 32) ||
				(test.wantSavedCall == 1 && audit.inputs[0].Action != backupasset.AuditActionSavedSearchUse) {
				t.Fatalf("audit=%+v", audit.inputs)
			}
		})
	}
}

func TestAssetSearchHandlerRejectsTransportAndFeatureBeforeProofServiceOrAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pointID := strings.Repeat("1", 32)
	valid := `{"query":{"schema_version":1,"root":{"op":"term","field":"name","text":"safe"},"scope":{"mode":"exact_points","recovery_point_ids":["` + pointID + `"]},"sort":"relevance","limit":10}}`
	savedID := strings.Repeat("2", 32)
	for _, test := range []struct {
		name    string
		body    string
		config  BackupAssetHandlerConfigSource
		want    int
		wantErr error
	}{
		{"unknown", `{"query":null,"future":true}`, backupAssetHandlerConfigEnabled, http.StatusBadRequest, nil},
		{"trailing", valid + `{}`, backupAssetHandlerConfigEnabled, http.StatusBadRequest, nil},
		{"oversize", strings.Repeat("x", (64<<10)+1), backupAssetHandlerConfigEnabled, http.StatusBadRequest, nil},
		{"oversize cursor", `{"saved_search_id":"` + savedID + `","cursor":"` + strings.Repeat("c", (8<<10)+1) + `"}`, backupAssetHandlerConfigEnabled, http.StatusBadRequest, nil},
		{"disabled", valid, func() (BackupAssetHandlerConfig, error) {
			return BackupAssetHandlerConfig{Enabled: false, QueryLimits: assetsearch.DefaultQueryLimits(), IdempotencyKeyMaxBytes: 128}, nil
		}, http.StatusServiceUnavailable, nil},
		{"config unavailable", valid, func() (BackupAssetHandlerConfig, error) {
			return BackupAssetHandlerConfig{}, errors.New("settings unavailable")
		}, http.StatusServiceUnavailable, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			searchSpy := &backupAssetSearchServiceSpy{}
			savedSpy := &backupAssetSavedSearchUseSpy{}
			audit := &backupAssetAuditSpy{}
			proofCalls := 0
			handler := NewBackupAssetSearchHandler(searchSpy, savedSpy, audit, test.config, func(*gin.Context) (*assetsearch.SecretRevealProof, error) {
				proofCalls++
				return nil, test.wantErr
			})
			router := backupAssetHandlerTestRouter()
			router.POST("/asset-search", handler.Search)
			request := httptest.NewRequest(http.MethodPost, "/asset-search", strings.NewReader(test.body))
			request.Header.Set(StepUpHeaderName, "opaque-proof")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want || searchSpy.calls != 0 || savedSpy.calls != 0 || proofCalls != 0 || len(audit.inputs) != 0 {
				t.Fatalf("status=%d want=%d body=%s search=%d saved=%d proof=%d audit=%d", response.Code, test.want,
					response.Body.String(), searchSpy.calls, savedSpy.calls, proofCalls, len(audit.inputs))
			}
		})
	}
}

func TestAssetSearchHandlerRejectsOperatorSecretRevealProof(t *testing.T) {
	gin.SetMode(gin.TestMode)
	searchSpy := &backupAssetSearchServiceSpy{}
	handler := NewBackupAssetSearchHandler(searchSpy, &backupAssetSavedSearchUseSpy{}, &backupAssetAuditSpy{}, backupAssetHandlerConfigEnabled,
		NewBackupAssetSecretProofVerifier(nil, nil))
	router := backupAssetHandlerTestRouter()
	router.POST("/asset-search", handler.Search)
	body := `{"query":{"schema_version":1,"root":{"op":"term","field":"name","text":"safe"},"scope":{"mode":"current"},"sort":"relevance","limit":10}}`
	request := httptest.NewRequest(http.MethodPost, "/asset-search", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(StepUpHeaderName, "opaque-proof")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || searchSpy.calls != 0 {
		t.Fatalf("status=%d body=%s search=%d", response.Code, response.Body.String(), searchSpy.calls)
	}
}

func TestBackupAssetSecretProofVerifierRejectsNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, role := range []string{"operator", "viewer"} {
		t.Run(role, func(t *testing.T) {
			verifier := NewBackupAssetSecretProofVerifier(nil, nil)
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			context.Request = httptest.NewRequest(http.MethodPost, "/asset-search", nil)
			context.Request.Header.Set(StepUpHeaderName, "opaque-proof")
			context.Set(middleware.CtxRole, role)
			context.Set(middleware.CtxUserID, uint(77))
			proof, err := verifier(context)
			if proof != nil || !errors.Is(err, backupasset.ErrForbidden) {
				t.Fatalf("%s verifier proof=%+v err=%v", role, proof, err)
			}
		})
	}
}

func TestAssetSearchOptionalProofFailureIsClosedBeforeSearchAndAudit(t *testing.T) {
	searchSpy := &backupAssetSearchServiceSpy{}
	audit := &backupAssetAuditSpy{}
	proofCalls := 0
	handler := NewBackupAssetSearchHandler(searchSpy, &backupAssetSavedSearchUseSpy{}, audit, backupAssetHandlerConfigEnabled,
		func(*gin.Context) (*assetsearch.SecretRevealProof, error) {
			proofCalls++
			return nil, ErrStepUpVerifierUnavailable
		})
	router := backupAssetHandlerTestRouter()
	router.POST("/asset-search", handler.Search)
	body := `{"query":{"schema_version":1,"root":{"op":"term","field":"name","text":"safe"},"scope":{"mode":"current"},"sort":"relevance","limit":10}}`
	request := httptest.NewRequest(http.MethodPost, "/asset-search", strings.NewReader(body))
	request.Header.Set(StepUpHeaderName, "opaque-proof")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || proofCalls != 1 || searchSpy.calls != 0 || len(audit.inputs) != 0 {
		t.Fatalf("status=%d body=%s proof=%d search=%d audit=%d", response.Code, response.Body.String(), proofCalls, searchSpy.calls, len(audit.inputs))
	}
}

func TestAssetSearchAuditWriteFailureDoesNotReturnHits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	searchSpy := &backupAssetSearchServiceSpy{response: assetsearch.SearchResponse{Items: []assetsearch.SearchHit{{
		Asset: catalog.EntryDTO{Name: "leaked-hit.bin"},
	}}}}
	audit := &backupAssetAuditSpy{err: errors.New("audit sink down")}
	handler := NewBackupAssetSearchHandler(searchSpy, &backupAssetSavedSearchUseSpy{}, audit, backupAssetHandlerConfigEnabled, nil)
	router := backupAssetHandlerTestRouter()
	router.POST("/asset-search", handler.Search)
	body := `{"query":{"schema_version":1,"root":{"op":"term","field":"name","text":"secret.env"},"scope":{"mode":"current"},"sort":"relevance","limit":10}}`
	request := httptest.NewRequest(http.MethodPost, "/asset-search", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || searchSpy.calls != 1 {
		t.Fatalf("status=%d body=%s search=%d", response.Code, response.Body.String(), searchSpy.calls)
	}
	if strings.Contains(response.Body.String(), "leaked-hit.bin") {
		t.Fatalf("audit failure leaked search hits: %s", response.Body.String())
	}
}

func TestAssetSearchViewerRBACStopsSpyService(t *testing.T) {
	searchSpy := &backupAssetSearchServiceSpy{}
	handler := NewBackupAssetSearchHandler(searchSpy, &backupAssetSavedSearchUseSpy{}, nil, backupAssetHandlerConfigEnabled, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "viewer")
		c.Set(middleware.CtxUserID, uint(88))
		c.Next()
	})
	router.POST("/asset-search", middleware.RBAC(backupasset.PermissionBackupAssetsList), handler.Search)
	request := httptest.NewRequest(http.MethodPost, "/asset-search", strings.NewReader(`{"query":{}}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || searchSpy.calls != 0 {
		t.Fatalf("status=%d body=%s calls=%d", response.Code, response.Body.String(), searchSpy.calls)
	}
}

func backupAssetHandlerConfigEnabled() (BackupAssetHandlerConfig, error) {
	limits := assetsearch.DefaultQueryLimits()
	limits.MaxBodyBytes = 64 << 10
	limits.MaxPageSize = 200
	return BackupAssetHandlerConfig{Enabled: true, QueryLimits: limits, IdempotencyKeyMaxBytes: 128}, nil
}

func backupAssetHandlerTestRouter() *gin.Engine {
	return backupAssetHandlerTestRouterWithRole("operator")
}

func backupAssetHandlerTestRouterWithRole(role string) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(77))
		c.Set(middleware.CtxRole, role)
		c.Set(middleware.CtxUsername, "search-user")
		c.Set(middleware.RequestIDKey, "search-correlation")
		c.Next()
	})
	return router
}

func TestSearchSourceBoundaryHasNoProviderCommandOrLegacySearchDependency(t *testing.T) {
	for _, name := range []string{"backup_asset_search_handler.go", "backup_asset_overlay_handler.go"} {
		path := filepath.Join(name)
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
			for _, forbidden := range []string{"/provider", "/runner", "/executor", "/sshutil", "os/exec"} {
				if importPath == forbidden || strings.Contains(importPath, forbidden) {
					t.Fatalf("%s imports forbidden boundary %q", name, importPath)
				}
			}
		}
		for _, forbidden := range [][]byte{[]byte("SnapshotFileIndex"), []byte("ProviderLocator"), []byte("exec.Command"), []byte(" LIKE ")} {
			if bytes.Contains(payload, forbidden) {
				t.Fatalf("%s contains forbidden legacy/provider token %q", name, forbidden)
			}
		}
	}
}
