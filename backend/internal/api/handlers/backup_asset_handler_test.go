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
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type backupAssetServiceSpy struct {
	err          error
	calls        int
	repositoryID string
	pointID      string
	entryID      string
	scope        catalog.AuthorizationScope
	pointRequest catalog.RecoveryPointListRequest
	entryRequest catalog.EntryListRequest
	diffRequest  catalog.DiffRequest
}

func (spy *backupAssetServiceSpy) ListRecoveryPoints(_ context.Context, repositoryID string, scope catalog.AuthorizationScope, request catalog.RecoveryPointListRequest) (catalog.RecoveryPointPage, error) {
	spy.calls++
	spy.repositoryID, spy.scope, spy.pointRequest = repositoryID, scope, request
	return catalog.RecoveryPointPage{Items: []catalog.RecoveryPointView{}}, spy.err
}

func (spy *backupAssetServiceSpy) GetRecoveryPoint(_ context.Context, pointID string, scope catalog.AuthorizationScope) (catalog.RecoveryPointView, error) {
	spy.calls++
	spy.pointID, spy.scope = pointID, scope
	return catalog.RecoveryPointView{}, spy.err
}

func (spy *backupAssetServiceSpy) GetCatalogStatus(_ context.Context, pointID string, scope catalog.AuthorizationScope) (catalog.StatusDTO, error) {
	spy.calls++
	spy.pointID, spy.scope = pointID, scope
	return catalog.StatusDTO{}, spy.err
}

func (spy *backupAssetServiceSpy) GetEvidence(_ context.Context, pointID string, scope catalog.AuthorizationScope) (catalog.EvidenceDTO, error) {
	spy.calls++
	spy.pointID, spy.scope = pointID, scope
	return catalog.EvidenceDTO{}, spy.err
}

func (spy *backupAssetServiceSpy) ListEntries(_ context.Context, pointID string, scope catalog.AuthorizationScope, request catalog.EntryListRequest) (catalog.EntryPage, error) {
	spy.calls++
	spy.pointID, spy.scope, spy.entryRequest = pointID, scope, request
	return catalog.EntryPage{
		Items:     []catalog.EntryDTO{},
		Directory: catalog.DirectoryContextDTO{Breadcrumb: []catalog.BreadcrumbDTO{}},
	}, spy.err
}

func (spy *backupAssetServiceSpy) GetEntry(_ context.Context, pointID, entryID string, scope catalog.AuthorizationScope) (catalog.EntryDTO, error) {
	spy.calls++
	spy.pointID, spy.entryID, spy.scope = pointID, entryID, scope
	return catalog.EntryDTO{}, spy.err
}

func (spy *backupAssetServiceSpy) ListEntryVersions(_ context.Context, pointID, entryID string, scope catalog.AuthorizationScope) (catalog.EntryVersionPage, error) {
	spy.calls++
	spy.pointID, spy.entryID, spy.scope = pointID, entryID, scope
	return catalog.EntryVersionPage{Items: []catalog.EntryVersionDTO{}}, spy.err
}

func (spy *backupAssetServiceSpy) Diff(_ context.Context, scope catalog.AuthorizationScope, request catalog.DiffRequest) (catalog.DiffPage, error) {
	spy.calls++
	spy.scope, spy.diffRequest = scope, request
	return catalog.DiffPage{Items: []catalog.DiffItemDTO{}}, spy.err
}

type backupAssetAuditSpy struct {
	inputs []backupasset.AuditEventInput
	err    error
}

func (spy *backupAssetAuditSpy) Write(_ context.Context, input backupasset.AuditEventInput) error {
	spy.inputs = append(spy.inputs, input)
	return spy.err
}

func TestBackupAssetHandlerStrictBindingAndCompositeRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repositoryID := strings.Repeat("1", 32)
	pointID := strings.Repeat("2", 32)
	compareID := strings.Repeat("3", 32)
	entryID := strings.Repeat("a", 64)
	compareEntryID := strings.Repeat("b", 64)
	for _, test := range []struct {
		name       string
		method     string
		path       string
		body       string
		register   func(*gin.Engine, *BackupAssetHandler)
		assertCall func(*testing.T, *backupAssetServiceSpy)
	}{
		{
			name: "point list", method: http.MethodGet,
			path: "/backup-repositories/" + repositoryID + "/recovery-points?limit=2&sort=captured_desc&cursor=opaque-cursor",
			register: func(router *gin.Engine, handler *BackupAssetHandler) {
				router.GET("/backup-repositories/:id/recovery-points", handler.ListRecoveryPoints)
			},
			assertCall: func(t *testing.T, spy *backupAssetServiceSpy) {
				if spy.repositoryID != repositoryID || spy.pointRequest.Limit != 2 || spy.pointRequest.Sort != catalog.RecoveryPointSortCapturedDesc || spy.pointRequest.Cursor != "opaque-cursor" {
					t.Fatalf("point list request=%+v repository=%s", spy.pointRequest, spy.repositoryID)
				}
			},
		},
		{
			name: "point detail", method: http.MethodGet, path: "/recovery-points/" + pointID,
			register: func(router *gin.Engine, handler *BackupAssetHandler) {
				router.GET("/recovery-points/:id", handler.GetRecoveryPoint)
			},
			assertCall: func(t *testing.T, spy *backupAssetServiceSpy) {
				if spy.pointID != pointID {
					t.Fatalf("point=%s", spy.pointID)
				}
			},
		},
		{
			name: "catalog status", method: http.MethodGet, path: "/recovery-points/" + pointID + "/catalog-status",
			register: func(router *gin.Engine, handler *BackupAssetHandler) {
				router.GET("/recovery-points/:id/catalog-status", handler.GetCatalogStatus)
			},
			assertCall: func(t *testing.T, spy *backupAssetServiceSpy) {
				if spy.pointID != pointID {
					t.Fatalf("point=%s", spy.pointID)
				}
			},
		},
		{
			name: "evidence", method: http.MethodGet, path: "/recovery-points/" + pointID + "/evidence",
			register: func(router *gin.Engine, handler *BackupAssetHandler) {
				router.GET("/recovery-points/:id/evidence", handler.GetEvidence)
			},
			assertCall: func(t *testing.T, spy *backupAssetServiceSpy) {
				if spy.pointID != pointID {
					t.Fatalf("point=%s", spy.pointID)
				}
			},
		},
		{
			name: "entry list", method: http.MethodGet,
			path: "/recovery-points/" + pointID + "/entries?parent=" + entryID + "&limit=3&sort=size_desc&cursor=opaque-entry-cursor",
			register: func(router *gin.Engine, handler *BackupAssetHandler) {
				router.GET("/recovery-points/:id/entries", handler.ListEntries)
			},
			assertCall: func(t *testing.T, spy *backupAssetServiceSpy) {
				if spy.pointID != pointID || spy.entryRequest.ParentEntryID != entryID || spy.entryRequest.Limit != 3 ||
					spy.entryRequest.Sort != catalog.EntrySortSizeDesc || spy.entryRequest.Cursor != "opaque-entry-cursor" {
					t.Fatalf("entry list point=%s request=%+v", spy.pointID, spy.entryRequest)
				}
			},
		},
		{
			name: "entry detail", method: http.MethodGet, path: "/recovery-points/" + pointID + "/entries/" + entryID,
			register: func(router *gin.Engine, handler *BackupAssetHandler) {
				router.GET("/recovery-points/:id/entries/:entryId", handler.GetEntry)
			},
			assertCall: func(t *testing.T, spy *backupAssetServiceSpy) {
				if spy.pointID != pointID || spy.entryID != entryID {
					t.Fatalf("point=%s entry=%s", spy.pointID, spy.entryID)
				}
			},
		},
		{
			name: "entry versions", method: http.MethodGet, path: "/recovery-points/" + pointID + "/entries/" + entryID + "/versions",
			register: func(router *gin.Engine, handler *BackupAssetHandler) {
				router.GET("/recovery-points/:id/entries/:entryId/versions", handler.ListEntryVersions)
			},
			assertCall: func(t *testing.T, spy *backupAssetServiceSpy) {
				if spy.pointID != pointID || spy.entryID != entryID {
					t.Fatalf("versions point=%s entry=%s", spy.pointID, spy.entryID)
				}
			},
		},
		{
			name: "exact diff", method: http.MethodPost, path: "/recovery-point-diffs",
			body: `{"base_recovery_point_id":"` + pointID + `","compare_recovery_point_id":"` + compareID +
				`","base_parent_entry_id":"` + entryID + `","compare_parent_entry_id":"` + compareEntryID + `","sort":"path_asc","limit":4,"cursor":"opaque-diff-cursor"}`,
			register: func(router *gin.Engine, handler *BackupAssetHandler) {
				router.POST("/recovery-point-diffs", handler.Diff)
			},
			assertCall: func(t *testing.T, spy *backupAssetServiceSpy) {
				if spy.diffRequest.BaseRecoveryPointID != pointID || spy.diffRequest.CompareRecoveryPointID != compareID ||
					spy.diffRequest.BaseParentEntryID != entryID || spy.diffRequest.CompareParentEntryID != compareEntryID ||
					spy.diffRequest.Sort != catalog.DiffSortPathAsc || spy.diffRequest.Limit != 4 || spy.diffRequest.Cursor != "opaque-diff-cursor" {
					t.Fatalf("diff request=%+v", spy.diffRequest)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &backupAssetServiceSpy{}
			audit := &backupAssetAuditSpy{}
			handler := NewBackupAssetHandler(service, audit)
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(middleware.CtxUserID, uint(77))
				c.Set(middleware.CtxRole, "operator")
				c.Set(middleware.CtxUsername, "catalog-user")
				c.Set(middleware.RequestIDKey, "catalog-request-id")
				c.Next()
			})
			test.register(router, handler)
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK || service.calls != 1 || service.scope != (catalog.AuthorizationScope{Role: "operator", UserID: 77}) {
				t.Fatalf("status=%d body=%s calls=%d scope=%+v", response.Code, response.Body.String(), service.calls, service.scope)
			}
			test.assertCall(t, service)
			if len(audit.inputs) != 1 || audit.inputs[0].Outcome != backupasset.AuditOutcomeSuccess ||
				audit.inputs[0].Fields[backupasset.AuditFieldCorrelationID] != "catalog-request-id" {
				t.Fatalf("audit inputs=%+v", audit.inputs)
			}
		})
	}
}

func TestBackupAssetHandlerRejectsUnknownDuplicatePathAndOversizeInputsBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pointID := strings.Repeat("2", 32)
	for _, test := range []struct {
		name   string
		method string
		path   string
		body   string
		route  string
		handle func(*BackupAssetHandler) gin.HandlerFunc
	}{
		{"unknown query", http.MethodGet, "/recovery-points/" + pointID + "/entries?path=/SECRET", "", "/recovery-points/:id/entries", func(handler *BackupAssetHandler) gin.HandlerFunc { return handler.ListEntries }},
		{"duplicate query", http.MethodGet, "/recovery-points/" + pointID + "/entries?limit=1&limit=2", "", "/recovery-points/:id/entries", func(handler *BackupAssetHandler) gin.HandlerFunc { return handler.ListEntries }},
		{"invalid point", http.MethodGet, "/recovery-points/latest", "", "/recovery-points/:id", func(handler *BackupAssetHandler) gin.HandlerFunc { return handler.GetRecoveryPoint }},
		{"unknown diff field", http.MethodPost, "/recovery-point-diffs", `{"base_recovery_point_id":"` + pointID + `","compare_recovery_point_id":"` + strings.Repeat("3", 32) + `","path":"/SECRET"}`, "/recovery-point-diffs", func(handler *BackupAssetHandler) gin.HandlerFunc { return handler.Diff }},
		{"trailing diff", http.MethodPost, "/recovery-point-diffs", `{}` + `{}`, "/recovery-point-diffs", func(handler *BackupAssetHandler) gin.HandlerFunc { return handler.Diff }},
		{"oversize diff", http.MethodPost, "/recovery-point-diffs", strings.Repeat("x", (64<<10)+1), "/recovery-point-diffs", func(handler *BackupAssetHandler) gin.HandlerFunc { return handler.Diff }},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &backupAssetServiceSpy{}
			handler := NewBackupAssetHandler(service, nil)
			router := gin.New()
			router.Handle(test.method, test.route, test.handle(handler))
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || service.calls != 0 {
				t.Fatalf("status=%d body=%s calls=%d", response.Code, response.Body.String(), service.calls)
			}
		})
	}
}

func TestBackupAssetHandlerMapsErrorsAndAuditsOnlySafeFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pointID := strings.Repeat("2", 32)
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{"invalid cursor", catalog.ErrInvalidCursor, http.StatusBadRequest},
		{"forbidden", backupasset.ErrForbidden, http.StatusForbidden},
		{"not found", backupasset.ErrNotFound, http.StatusNotFound},
		{"stale", catalog.ErrStaleCursor, http.StatusConflict},
		{"catalog unavailable", catalog.ErrCatalogUnavailable, http.StatusServiceUnavailable},
		{"projection limit", catalog.ErrOwnershipProjectionLimit, http.StatusServiceUnavailable},
		{"command unsupported", backupasset.ErrCapabilityUnavailable, http.StatusNotImplemented},
		{"invalid internal catalog contract", catalog.ErrInvalidCatalogContract, http.StatusInternalServerError},
		{"internal", errors.New("SECRET_INTERNAL_PROVIDER_PATH"), http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &backupAssetServiceSpy{err: test.err}
			audit := &backupAssetAuditSpy{}
			handler := NewBackupAssetHandler(service, audit)
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(middleware.CtxUserID, uint(9))
				c.Set(middleware.CtxRole, "operator")
				c.Set(middleware.CtxUsername, "operator")
				c.Set(middleware.RequestIDKey, "safe-correlation")
			})
			router.GET("/recovery-points/:id", handler.GetRecoveryPoint)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/recovery-points/"+pointID, nil))
			if response.Code != test.want || strings.Contains(response.Body.String(), "SECRET_INTERNAL_PROVIDER_PATH") {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
			if len(audit.inputs) != 1 {
				t.Fatalf("audit=%+v", audit.inputs)
			}
			input := audit.inputs[0]
			if input.Fingerprints != (backupasset.AuditFingerprintInput{}) {
				t.Fatalf("audit retained raw fingerprints: %+v", input.Fingerprints)
			}
			encoded, err := json.Marshal(struct {
				RepositoryID    string                         `json:"repository_id"`
				RecoveryPointID string                         `json:"recovery_point_id"`
				EntryID         string                         `json:"entry_id"`
				FailureCode     string                         `json:"failure_code"`
				Fields          map[backupasset.AuditField]any `json:"fields"`
			}{input.RepositoryID, input.RecoveryPointID, input.EntryID, input.FailureCode, input.Fields})
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"SECRET", "path", "cursor", "provider_locator", "command"} {
				if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(forbidden)) {
					t.Fatalf("audit leaked %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

func TestBackupAssetHandlerHasNoProviderCommandDependency(t *testing.T) {
	path := filepath.Join("backup_asset_handler.go")
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

func TestBackupAssetEntryPageSwaggerRequiresExplicitDirectoryContext(t *testing.T) {
	document := readBackupAssetSwagger(t)
	operation, ok := document.Paths["/recovery-points/{id}/entries"]["get"]
	if !ok {
		t.Fatal("generated Swagger is missing GET /recovery-points/{id}/entries")
	}
	if _, ok := operation.Responses["500"]; !ok {
		t.Error("generated Swagger is missing the generic 500 directory-contract response")
	}
	page := requireBackupAssetSwaggerDefinition(t, document, "xirang_backend_internal_backupasset_catalog.EntryPage")
	if _, ok := page.Properties["directory"]; !ok {
		t.Fatal("EntryPage Swagger schema is missing directory")
	}
	requireBackupAssetSwaggerFields(t, page, "directory")
	directory := requireBackupAssetSwaggerDefinition(t, document, "xirang_backend_internal_backupasset_catalog.DirectoryContextDTO")
	requireBackupAssetSwaggerFields(t, directory, "breadcrumb", "current", "parent")
	breadcrumb := requireBackupAssetSwaggerDefinition(t, document, "xirang_backend_internal_backupasset_catalog.BreadcrumbDTO")
	requireBackupAssetSwaggerFields(t, breadcrumb, "entry_id", "name", "recovery_point_id")
}

func TestBackupAssetEntryPageHandlerAlwaysSerializesRootDirectoryContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pointID := strings.Repeat("2", 32)
	handler := NewBackupAssetHandler(&backupAssetServiceSpy{}, nil)
	router := backupAssetHandlerTestRouterWithRole("operator")
	router.GET("/recovery-points/:id/entries", handler.ListEntries)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/recovery-points/"+pointID+"/entries", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data catalog.EntryPage `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Directory.Current != nil || envelope.Data.Directory.Parent != nil ||
		envelope.Data.Directory.Breadcrumb == nil || len(envelope.Data.Directory.Breadcrumb) != 0 {
		t.Fatalf("root directory context=%+v body=%s", envelope.Data.Directory, response.Body.String())
	}
}

func TestBackupAssetHandlerCatalogStatusAndEvidenceRealServiceProjections(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.User{},
		&model.Node{},
		&model.NodeOwner{},
		&model.Task{},
		&model.TaskRun{},
		&model.BackupRepository{},
		&model.TaskRepositoryLink{},
		&model.WrappedDomainKey{},
		&model.RecoveryPoint{},
		&model.RecoveryPointManifest{},
		&model.CatalogGeneration{},
		&model.RestoreDrillEvidence{},
	); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 19, 11, 0, 0, 0, time.UTC)
	agedObserved := now.Add(-2 * time.Hour)
	const operatorID uint = 77
	if err := db.Create(&model.User{
		ID: operatorID, Username: "handler-catalog-operator", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
		Role: "operator", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	node := model.Node{
		ID: 7101, Name: "handler-catalog-node", Host: "node.example.invalid", Port: 22, Username: "catalog",
		AuthType: "key", Status: "online", BackupDir: "catalog-dir", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: node.ID, UserID: operatorID, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	task := model.Task{
		ID: 7201, Name: "handler-mutable-task", NodeID: node.ID, ExecutorType: "rsync", Status: "idle",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	runStarted := now.Add(-time.Minute)
	run := model.TaskRun{
		ID: 7301, TaskID: task.ID, TriggerType: "SECRET_HTTP_TRIGGER_CANARY", Status: "SECRET_HTTP_RUN_STATUS_CANARY",
		LastError: "SECRET_HTTP_LAST_ERROR_CANARY", StartedAt: &runStarted, FinishedAt: &now,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	repository := model.BackupRepository{
		ID: strings.Repeat("1", 32), ProviderKind: string(backupasset.ProviderRsync), DisplayName: "handler-mutable-repo",
		VersionMode: string(backupasset.VersionMutableHead), Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1,
		CapabilitiesJSON: `{"list":true,"open_sequential":true}`, ImmutabilityLevel: string(backupasset.ImmutabilityMutable),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	link := model.TaskRepositoryLink{
		ID: strings.Repeat("2", 32), TaskID: &task.ID, RepositoryID: repository.ID, TaskNameSnapshot: task.Name,
		NodeIDSnapshot: node.ID, NodeNameSnapshot: node.Name, PublicationMode: string(backupasset.PublicationLegacyMutable),
		EncryptedLegacyLocator: "SECRET_HTTP_LEGACY_LOCATOR_CANARY", LinkedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	lineageBytes, err := json.Marshal(backupasset.RecoveryPointLineageSummary{ProducingTaskID: &task.ID})
	if err != nil {
		t.Fatal(err)
	}
	sourceFingerprint := strings.Repeat("a", 64)
	driftedFingerprint := strings.Repeat("b", 64)
	point := model.RecoveryPoint{
		ID: strings.Repeat("3", 32), RepositoryID: repository.ID, ProducingTaskID: &task.ID, ProducingTaskRunID: &run.ID,
		ProducingTaskNameSnapshot: task.Name, ProducingNodeIDSnapshot: node.ID, ProducingNodeNameSnapshot: node.Name,
		LineageJSON: string(lineageBytes), EncryptedProviderLocator: "SECRET_HTTP_PROVIDER_LOCATOR_CANARY",
		Semantics: string(backupasset.PointMutableHead), State: string(backupasset.RecoveryPointObserved),
		ObservedAt: &agedObserved, SourceFingerprint: sourceFingerprint,
		ConsistencyJSON:    `{"version":1,"provider":"SECRET_HTTP_PROVIDER_CANARY","completion":"SECRET_HTTP_COMPLETION_CANARY","code":"SECRET_HTTP_CODE_CANARY"}`,
		FidelityJSON:       `{"secret":"SECRET_HTTP_FIDELITY_CANARY"}`,
		CapabilityRevision: 1, CapabilitiesJSON: `{"list":true}`, ImmutabilityLevel: string(backupasset.ImmutabilityMutable),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}

	ownership, err := catalog.NewOwnership(db)
	if err != nil {
		t.Fatal(err)
	}
	keyring := backupasset.NewKeyring(db, func() time.Time { return now })
	if _, err := keyring.Ensure(context.Background(), backupasset.KeyDomainCursorSigning); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Ensure(context.Background(), backupasset.KeyDomainEntryIdentity); err != nil {
		t.Fatal(err)
	}
	service, err := catalog.NewService(catalog.ServiceDependencies{
		DB: db, Ownership: ownership, Cursor: catalog.NewCursorCodec(keyring, func() time.Time { return now }, 15*time.Minute),
		Now: func() time.Time { return now }, ReconcileInterval: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewBackupAssetHandler(service, nil)
	router := backupAssetHandlerTestRouterWithRole("operator")
	router.GET("/recovery-points/:id/catalog-status", handler.GetCatalogStatus)
	router.GET("/recovery-points/:id/evidence", handler.GetEvidence)

	getStatus := func(t *testing.T) (int, catalog.StatusDTO, string) {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/recovery-points/"+point.ID+"/catalog-status", nil))
		body := recorder.Body.String()
		for _, canary := range []string{
			sourceFingerprint, driftedFingerprint, "SECRET_HTTP_", "source_fingerprint",
		} {
			if strings.Contains(body, canary) {
				t.Fatalf("GET /catalog-status leaked %q: %s", canary, body)
			}
		}
		var envelope struct {
			Data catalog.StatusDTO `json:"data"`
		}
		if recorder.Code == http.StatusOK {
			if unmarshalErr := json.Unmarshal(recorder.Body.Bytes(), &envelope); unmarshalErr != nil {
				t.Fatalf("decode catalog-status response: %v (%s)", unmarshalErr, body)
			}
		}
		return recorder.Code, envelope.Data, body
	}

	code, status, body := getStatus(t)
	if code != http.StatusOK || status.Generation != nil || status.LatestBuild != nil ||
		status.Coverage.Status != catalog.CoverageUnavailable || status.Staleness.Status != catalog.StalenessUnknown {
		t.Fatalf("no-index HTTP status=%d dto=%+v body=%s", code, status, body)
	}

	for index, tc := range []struct {
		state        catalog.GenerationState
		errorCode    catalog.GenerationErrorCode
		wantCoverage catalog.CoverageStatus
	}{
		{state: catalog.GenerationBuilding, errorCode: catalog.GenerationErrorNone, wantCoverage: catalog.CoverageBuilding},
		{state: catalog.GenerationSuperseded, errorCode: catalog.GenerationErrorNone, wantCoverage: catalog.CoverageBuilding},
		{state: catalog.GenerationPartial, errorCode: catalog.GenerationErrorBuildIncomplete, wantCoverage: catalog.CoveragePartial},
		{state: catalog.GenerationFailed, errorCode: catalog.GenerationErrorSourceChanged, wantCoverage: catalog.CoverageFailed},
	} {
		if err := db.Where("recovery_point_id = ?", point.ID).Delete(&model.CatalogGeneration{}).Error; err != nil {
			t.Fatal(err)
		}
		var finished *time.Time
		if tc.state != catalog.GenerationBuilding {
			finished = &now
		}
		row := model.CatalogGeneration{
			ID: fmt.Sprintf("%032x", 800+index), RecoveryPointID: point.ID, Generation: index + 1,
			State: string(tc.state), IsActive: false, SourceFingerprint: sourceFingerprint,
			ExpectedEntryCount: 3, WrittenEntryCount: 1, ExpectedDigest: strings.Repeat("e", 64),
			WrittenDigest: strings.Repeat("f", 64), ErrorCode: string(tc.errorCode),
			StartedAt: now, FinishedAt: finished, CreatedAt: now, UpdatedAt: now,
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		code, status, body = getStatus(t)
		if code != http.StatusOK || status.Generation != nil || status.LatestBuild == nil ||
			status.LatestBuild.State != tc.state || status.Coverage.Status != tc.wantCoverage ||
			status.Staleness.Status != catalog.StalenessUnknown || status.Staleness.ObservedAt != nil ||
			status.Staleness.Reason != nil {
			t.Fatalf("non-active %s HTTP status=%d dto=%+v body=%s", tc.state, code, status, body)
		}
	}

	if err := db.Where("recovery_point_id = ?", point.ID).Delete(&model.CatalogGeneration{}).Error; err != nil {
		t.Fatal(err)
	}
	activeGen := model.CatalogGeneration{
		ID: strings.Repeat("4", 32), RecoveryPointID: point.ID, Generation: 1,
		State: string(catalog.GenerationComplete), IsActive: true, SourceFingerprint: sourceFingerprint,
		ExpectedEntryCount: 2, WrittenEntryCount: 2, ExpectedDigest: strings.Repeat("e", 64),
		WrittenDigest: strings.Repeat("f", 64), StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&activeGen).Error; err != nil {
		t.Fatal(err)
	}
	for _, missingObservedAt := range []any{nil, time.Time{}} {
		if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).Update("observed_at", missingObservedAt).Error; err != nil {
			t.Fatal(err)
		}
		code, status, body = getStatus(t)
		if code != http.StatusOK || status.Coverage.Status != catalog.CoverageComplete ||
			status.Staleness.Status != catalog.StalenessUnknown || status.Staleness.ObservedAt != nil ||
			status.Staleness.Reason != nil {
			t.Fatalf("missing-observation (%v) HTTP status=%d dto=%+v body=%s", missingObservedAt, code, status, body)
		}
	}

	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).Updates(map[string]any{
		"observed_at":        agedObserved,
		"source_fingerprint": sourceFingerprint,
	}).Error; err != nil {
		t.Fatal(err)
	}
	code, status, body = getStatus(t)
	if code != http.StatusOK || status.Coverage.Status != catalog.CoverageComplete ||
		status.Staleness.Status != catalog.StalenessFresh || status.Staleness.Reason != nil ||
		status.Staleness.ObservedAt == nil || !status.Staleness.ObservedAt.Equal(agedObserved) {
		t.Fatalf("aged-matching HTTP status=%d dto=%+v body=%s", code, status, body)
	}

	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).Updates(map[string]any{
		"observed_at":        agedObserved,
		"source_fingerprint": driftedFingerprint,
	}).Error; err != nil {
		t.Fatal(err)
	}
	code, status, body = getStatus(t)
	if code != http.StatusOK || status.Coverage.Status != catalog.CoverageComplete ||
		status.Staleness.Status != catalog.StalenessStale || status.Staleness.Reason == nil ||
		status.Staleness.Reason.Code != backupasset.CapabilityMutableSourceChanged ||
		status.Staleness.ObservedAt == nil || !status.Staleness.ObservedAt.Equal(agedObserved) {
		t.Fatalf("drifted-source HTTP status=%d dto=%+v body=%s", code, status, body)
	}

	for index, bad := range []struct {
		state     string
		errorCode string
		active    bool
	}{
		{state: "SECRET_HTTP_UNKNOWN_GENERATION_STATE", errorCode: "", active: false},
		{state: string(catalog.GenerationFailed), errorCode: "SECRET_HTTP_UNKNOWN_ERROR_CODE", active: false},
		{state: string(catalog.GenerationPartial), errorCode: string(catalog.GenerationErrorBuildIncomplete), active: true},
	} {
		if err := db.Where("recovery_point_id = ?", point.ID).Delete(&model.CatalogGeneration{}).Error; err != nil {
			t.Fatal(err)
		}
		badGen := model.CatalogGeneration{
			ID: fmt.Sprintf("%032x", 850+index), RecoveryPointID: point.ID, Generation: 2 + index,
			State: bad.state, IsActive: bad.active, SourceFingerprint: sourceFingerprint,
			ExpectedEntryCount: 1, WrittenEntryCount: 1, ExpectedDigest: strings.Repeat("e", 64),
			WrittenDigest: strings.Repeat("f", 64), ErrorCode: bad.errorCode,
			StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
		}
		if err := db.Create(&badGen).Error; err != nil {
			t.Fatal(err)
		}
		code, _, body = getStatus(t)
		if code != http.StatusInternalServerError {
			t.Fatalf("invalid generation (%d) HTTP status=%d, want 500 (%s)", index, code, body)
		}
	}

	manifest := model.RecoveryPointManifest{
		ID: strings.Repeat("6", 32), RecoveryPointID: point.ID, Revision: 1, DigestAlgorithm: "sha256",
		Digest: strings.Repeat("9", 64), Generator: "fixture", GeneratorVersion: "1",
		Completeness: "SECRET_HTTP_MANIFEST_COMPLETENESS_CANARY", FidelityJSON: `{"secret":"SECRET_HTTP_FIDELITY_CANARY"}`,
		EncryptedCommitEvidence: "SECRET_HTTP_COMMIT_EVIDENCE_CANARY", IsActive: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&manifest).Error; err != nil {
		t.Fatal(err)
	}
	drill := model.RestoreDrillEvidence{
		PolicyID: 1, TaskID: task.ID, TaskRunID: run.ID, SourceTaskRunID: &run.ID,
		SandboxNodeID: node.ID, SandboxPath: "SECRET_HTTP_SANDBOX_CANARY", Status: "SECRET_HTTP_DRILL_STATUS_CANARY",
		FailedStep: "SECRET_HTTP_DRILL_STEP_CANARY", RestoreError: "SECRET_HTTP_DRILL_ERROR_CANARY",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&drill).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).Update("observed_at", nil).Error; err != nil {
		t.Fatal(err)
	}

	evidenceRecorder := httptest.NewRecorder()
	router.ServeHTTP(evidenceRecorder, httptest.NewRequest(http.MethodGet, "/recovery-points/"+point.ID+"/evidence", nil))
	evidenceBody := evidenceRecorder.Body.String()
	if evidenceRecorder.Code != http.StatusOK {
		t.Fatalf("GET /evidence status=%d body=%s", evidenceRecorder.Code, evidenceBody)
	}
	if strings.Contains(evidenceBody, "SECRET_HTTP_") || strings.Contains(evidenceBody, sourceFingerprint) ||
		strings.Contains(evidenceBody, driftedFingerprint) {
		t.Fatalf("GET /evidence leaked internal value: %s", evidenceBody)
	}
	var evidenceEnvelope struct {
		Data catalog.EvidenceDTO `json:"data"`
	}
	if err := json.Unmarshal(evidenceRecorder.Body.Bytes(), &evidenceEnvelope); err != nil {
		t.Fatalf("decode evidence response: %v (%s)", err, evidenceBody)
	}
	gotEvidence := evidenceEnvelope.Data
	if gotEvidence.Lineage.Status != catalog.EvidenceInvalid || gotEvidence.Lineage.Trigger != "" || gotEvidence.Lineage.RunStatus != "" ||
		gotEvidence.Manifest.Status != catalog.EvidenceInvalid || gotEvidence.Manifest.Completeness != "" || gotEvidence.Manifest.Digest != "" ||
		gotEvidence.Publication.Status != catalog.EvidenceInvalid || gotEvidence.Publication.Provider != "" ||
		gotEvidence.Publication.Completion != "" || gotEvidence.Publication.FailureCode != "" ||
		gotEvidence.RestoreDrills.Status != catalog.EvidenceInvalid || len(gotEvidence.RestoreDrills.Items) != 0 {
		t.Fatalf("GET /evidence dto=%+v body=%s", gotEvidence, evidenceBody)
	}
}
