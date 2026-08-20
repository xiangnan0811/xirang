package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type backupAssetRBACTestFixture struct {
	router *gin.Engine
	tokens map[string]string
	proofs map[backupAssetRBACProofKey]string
}

type backupAssetRBACProofKey struct {
	role   string
	action auth.StepUpAction
}

func setupBackupAssetRBACFixture(t *testing.T) backupAssetRBACTestFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open backup asset RBAC database: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.AuditLog{}); err != nil {
		t.Fatalf("migrate backup asset RBAC database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("open backup asset RBAC SQL database: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	jwtManager := auth.NewJWTManager("FAKE_BACKUP_ASSET_RBAC_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	tokens := make(map[string]string, 4)
	proofs := make(map[backupAssetRBACProofKey]string, 4)
	for _, role := range []string{"admin", "operator", "viewer", "unknown"} {
		user := model.User{
			Username:     "backup-asset-rbac-" + role,
			PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
			Role:         role,
			TOTPEnabled:  true,
		}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("create %s backup asset RBAC user: %v", role, err)
		}
		token, err := jwtManager.GenerateToken(user)
		if err != nil {
			t.Fatalf("generate %s backup asset RBAC token: %v", role, err)
		}
		tokens[role] = token
		actions := []auth.StepUpAction(nil)
		switch role {
		case "admin":
			actions = []auth.StepUpAction{
				auth.StepUpActionAssetDownload,
				auth.StepUpActionAssetExportCreate,
				auth.StepUpActionAssetExportDownload,
			}
		case "operator":
			actions = []auth.StepUpAction{auth.StepUpActionAssetDownload}
		}
		for _, action := range actions {
			proof, _, proofErr := jwtManager.GenerateStepUpToken(user, action)
			if proofErr != nil {
				t.Fatalf("generate %s %s backup asset proof: %v", role, action, proofErr)
			}
			proofs[backupAssetRBACProofKey{role: role, action: action}] = proof
		}
	}

	return backupAssetRBACTestFixture{
		router: NewRouter(Dependencies{DB: db, JWTManager: jwtManager}),
		tokens: tokens,
		proofs: proofs,
	}
}

func performBackupAssetRBACRequest(t *testing.T, fixture backupAssetRBACTestFixture, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	fixture.router.ServeHTTP(response, request)
	return response
}

func TestBackupProcessingAdminRouteRequiresAdminAndFocusedRateLimit(t *testing.T) {
	fixture := setupBackupAssetRBACFixture(t)
	path := "/api/v1/admin/backup-asset-processing"
	if response := performBackupAssetRBACRequest(t, fixture, http.MethodGet, path, "", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", response.Code, response.Body.String())
	}
	for _, role := range []string{"operator", "viewer", "unknown"} {
		t.Run(role, func(t *testing.T) {
			response := performBackupAssetRBACRequest(t, fixture, http.MethodGet, path, "", fixture.tokens[role])
			if response.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	for index := 0; index < 30; index++ {
		response := performBackupAssetRBACRequest(t, fixture, http.MethodGet, path, "", fixture.tokens["admin"])
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("request %d status=%d body=%s", index+1, response.Code, response.Body.String())
		}
	}
	response := performBackupAssetRBACRequest(t, fixture, http.MethodGet, path, "", fixture.tokens["admin"])
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("focused limit status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBackupProcessingAdminRouteRejectsQueryAndBody(t *testing.T) {
	fixture := setupBackupAssetRBACFixture(t)
	path := "/api/v1/admin/backup-asset-processing"
	for _, testCase := range []struct {
		name   string
		target string
		body   string
	}{
		{name: "query", target: path + "?job_id=hidden"},
		{name: "body", target: path, body: `{}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := performBackupAssetRBACRequest(t, fixture, http.MethodGet, testCase.target, testCase.body, fixture.tokens["admin"])
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestBackupAssetRBACUsesAuthAndExactRepositoryPermissionsBeforeFeatureGate(t *testing.T) {
	fixture := setupBackupAssetRBACFixture(t)
	const repositoryID = "0123456789abcdef0123456789abcdef"
	routes := []struct {
		name       string
		method     string
		path       string
		body       string
		readAccess bool
	}{
		{name: "connect", method: http.MethodPost, path: "/api/v1/backup-repositories/connect", body: `{"task_id":1}`},
		{name: "list", method: http.MethodGet, path: "/api/v1/backup-repositories", readAccess: true},
		{name: "detail", method: http.MethodGet, path: "/api/v1/backup-repositories/" + repositoryID, readAccess: true},
		{name: "reconcile", method: http.MethodPost, path: "/api/v1/backup-repositories/" + repositoryID + "/reconcile"},
		{name: "disconnect", method: http.MethodPost, path: "/api/v1/backup-repositories/" + repositoryID + "/disconnect"},
	}

	for _, route := range routes {
		t.Run(route.name+"/unauthenticated", func(t *testing.T) {
			response := performBackupAssetRBACRequest(t, fixture, route.method, route.path, route.body, "")
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
		for _, role := range []string{"admin", "operator", "viewer", "unknown"} {
			role := role
			t.Run(route.name+"/"+role, func(t *testing.T) {
				response := performBackupAssetRBACRequest(t, fixture, route.method, route.path, route.body, fixture.tokens[role])
				expected := http.StatusForbidden
				if role == "admin" || (role == "operator" && route.readAccess) {
					expected = http.StatusServiceUnavailable
				}
				if response.Code != expected {
					t.Fatalf("status=%d want=%d body=%s", response.Code, expected, response.Body.String())
				}
				if expected == http.StatusServiceUnavailable && !strings.Contains(response.Body.String(), "feature_disabled") {
					t.Fatalf("authorized request did not reach feature gate: %s", response.Body.String())
				}
			})
		}
	}
}

func TestBackupLifecycleRBACRequiresAdminManageAndPurgeBeforeFeatureGate(t *testing.T) {
	fixture := setupBackupAssetRBACFixture(t)
	const repositoryID = "0123456789abcdef0123456789abcdef"
	const policyID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const pointID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const holdID = "cccccccccccccccccccccccccccccccc"
	const candidateID = "dddddddddddddddddddddddddddddddd"
	routes := []struct {
		name   string
		method string
		path   string
		body   string
		purge  bool
	}{
		{name: "policy list", method: http.MethodGet, path: "/api/v1/backup-retention-policies"},
		{name: "policy create", method: http.MethodPost, path: "/api/v1/backup-retention-policies", body: `{"scope_kind":"repository","scope_id":"` + repositoryID + `","rules":{"version":1,"age":{"keep_days":7}}}`},
		{name: "policy update", method: http.MethodPatch, path: "/api/v1/backup-retention-policies/" + policyID, body: `{"expected_revision":1,"rules":{"version":1,"age":{"keep_days":7}}}`},
		{name: "policy delete", method: http.MethodDelete, path: "/api/v1/backup-retention-policies/" + policyID, body: `{"expected_revision":1}`},
		{name: "policy impact", method: http.MethodPost, path: "/api/v1/backup-retention-policies/" + policyID + "/impact", body: `{"expected_revision":1}`},
		{name: "hold list", method: http.MethodGet, path: "/api/v1/recovery-points/" + pointID + "/holds"},
		{name: "hold create", method: http.MethodPost, path: "/api/v1/recovery-points/" + pointID + "/holds", body: `{"hold_type":"legal","reason":"legal"}`},
		{name: "hold release", method: http.MethodPost, path: "/api/v1/recovery-points/" + pointID + "/holds/" + holdID + "/release", body: `{"reason":"done"}`},
		{name: "import scan", method: http.MethodPost, path: "/api/v1/backup-repositories/" + repositoryID + "/import-scans", body: `{"limit":1}`},
		{name: "import list", method: http.MethodGet, path: "/api/v1/backup-repositories/" + repositoryID + "/import-candidates"},
		{name: "import review", method: http.MethodPost, path: "/api/v1/backup-repositories/" + repositoryID + "/import-candidates/" + candidateID + "/reviews", body: `{"decision":"rejected"}`},
		{name: "rebuild", method: http.MethodPost, path: "/api/v1/backup-repositories/" + repositoryID + "/rebuilds", body: `{"limit":1}`},
		{name: "purge preview", method: http.MethodPost, path: "/api/v1/backup-repositories/" + repositoryID + "/purge-preview", body: `{"items":[{"recovery_point_id":"` + pointID + `","point_revision":1,"capability_revision":1}]}`, purge: true},
		{name: "purge plan", method: http.MethodPost, path: "/api/v1/backup-repositories/" + repositoryID + "/purge-plans", body: `{"expected_impact_revision":1,"items":[{"recovery_point_id":"` + pointID + `","point_revision":1,"capability_revision":1}]}`, purge: true},
		{name: "purge execute", method: http.MethodPost, path: "/api/v1/backup-repositories/" + repositoryID + "/purges", body: `{"plan_id":"` + policyID + `","expected_revision":1,"expected_impact_revision":1,"reason":"retire"}`, purge: true},
	}
	for _, route := range routes {
		t.Run(route.name+"/unauthenticated", func(t *testing.T) {
			response := performBackupAssetRBACRequest(t, fixture, route.method, route.path, route.body, "")
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
		for _, role := range []string{"admin", "operator", "viewer", "unknown"} {
			role := role
			t.Run(route.name+"/"+role, func(t *testing.T) {
				response := performBackupAssetRBACRequest(t, fixture, route.method, route.path, route.body, fixture.tokens[role])
				expected := http.StatusForbidden
				if role == "admin" {
					expected = http.StatusServiceUnavailable
					if route.name == "hold release" || route.name == "purge execute" {
						expected = http.StatusForbidden
					}
				}
				if response.Code != expected {
					t.Fatalf("status=%d want=%d body=%s", response.Code, expected, response.Body.String())
				}
				if expected == http.StatusServiceUnavailable && !strings.Contains(response.Body.String(), "feature_disabled") {
					t.Fatalf("authorized request did not reach feature gate: %s", response.Body.String())
				}
			})
		}
	}
}

func TestCatalogRoutesRequireAssetListPermissionBeforeFeatureGate(t *testing.T) {
	fixture := setupBackupAssetRBACFixture(t)
	const repositoryID = "0123456789abcdef0123456789abcdef"
	const pointID = "11111111111111111111111111111111"
	const comparePointID = "22222222222222222222222222222222"
	const entryID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	routes := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"recovery point list", http.MethodGet, "/api/v1/backup-repositories/" + repositoryID + "/recovery-points", ""},
		{"recovery point detail", http.MethodGet, "/api/v1/recovery-points/" + pointID, ""},
		{"Catalog status", http.MethodGet, "/api/v1/recovery-points/" + pointID + "/catalog-status", ""},
		{"evidence", http.MethodGet, "/api/v1/recovery-points/" + pointID + "/evidence", ""},
		{"entry list", http.MethodGet, "/api/v1/recovery-points/" + pointID + "/entries", ""},
		{"entry detail", http.MethodGet, "/api/v1/recovery-points/" + pointID + "/entries/" + entryID, ""},
		{"exact diff", http.MethodPost, "/api/v1/recovery-point-diffs", `{"base_recovery_point_id":"` + pointID + `","compare_recovery_point_id":"` + comparePointID + `"}`},
	}

	for _, route := range routes {
		route := route
		t.Run(route.name+"/unauthenticated", func(t *testing.T) {
			response := performBackupAssetRBACRequest(t, fixture, route.method, route.path, route.body, "")
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
		for _, role := range []string{"admin", "operator", "viewer", "unknown"} {
			role := role
			t.Run(route.name+"/"+role, func(t *testing.T) {
				response := performBackupAssetRBACRequest(t, fixture, route.method, route.path, route.body, fixture.tokens[role])
				expected := http.StatusForbidden
				if role == "admin" || role == "operator" {
					expected = http.StatusServiceUnavailable
				}
				if response.Code != expected {
					t.Fatalf("status=%d want=%d body=%s", response.Code, expected, response.Body.String())
				}
				if expected == http.StatusServiceUnavailable && !strings.Contains(response.Body.String(), "feature_disabled") {
					t.Fatalf("authorized request did not reach Catalog feature gate: %s", response.Body.String())
				}
			})
		}
	}
}

func TestBackupContentTicketRequiresPreviewPermissionBeforeFeatureGate(t *testing.T) {
	fixture := setupBackupAssetRBACFixture(t)
	pointID, entryID := strings.Repeat("1", 32), strings.Repeat("a", 64)
	path := "/api/v1/recovery-points/" + pointID + "/entries/" + entryID + "/delivery-tickets"
	body := `{"schema_version":1,"action":"preview","renderer":"safe_raster","profile":"raster_v1"}`
	if response := performBackupAssetRBACRequest(t, fixture, http.MethodPost, path, body, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", response.Code, response.Body.String())
	}
	for _, role := range []string{"admin", "operator", "viewer", "unknown"} {
		t.Run(role, func(t *testing.T) {
			response := performBackupAssetRBACRequest(t, fixture, http.MethodPost, path, body, fixture.tokens[role])
			want := http.StatusForbidden
			if role == "admin" || role == "operator" {
				want = http.StatusServiceUnavailable
			}
			if response.Code != want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, want, response.Body.String())
			}
		})
	}
}

func TestBackupProcessingRoutesRequirePreviewPermissionBeforeFeatureGate(t *testing.T) {
	fixture := setupBackupAssetRBACFixture(t)
	pointID, entryID, interestID := strings.Repeat("1", 32), strings.Repeat("a", 64), strings.Repeat("2", 32)
	base := "/api/v1/recovery-points/" + pointID + "/entries/" + entryID
	routes := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"create", http.MethodPost, base + "/preview-jobs", `{"schema_version":1,"representation":"thumbnail"}`},
		{"poll", http.MethodGet, base + "/preview-jobs/" + interestID, ""},
		{"cancel", http.MethodPost, base + "/preview-jobs/" + interestID + "/cancel", `{"schema_version":1}`},
		{"state", http.MethodGet, base + "/processing", ""},
	}
	for _, route := range routes {
		t.Run(route.name+"/unauthenticated", func(t *testing.T) {
			response := performBackupAssetRBACRequest(t, fixture, route.method, route.path, route.body, "")
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
		for _, role := range []string{"admin", "operator", "viewer", "unknown"} {
			t.Run(route.name+"/"+role, func(t *testing.T) {
				response := performBackupAssetRBACRequest(t, fixture, route.method, route.path, route.body, fixture.tokens[role])
				want := http.StatusForbidden
				if role == "admin" || role == "operator" {
					want = http.StatusNotFound
				}
				if response.Code != want {
					t.Fatalf("status=%d want=%d body=%s", response.Code, want, response.Body.String())
				}
			})
		}
	}
}

func TestBackupAssetExportAndArchiveRoutesUseExactRoleAndPermissionMatrix(t *testing.T) {
	fixture := setupBackupAssetRBACFixture(t)
	pointID, entryID := strings.Repeat("1", 32), strings.Repeat("a", 64)
	jobID, memberID := strings.Repeat("2", 32), strings.Repeat("3", 32)
	indexRevision := strings.Repeat("4", 64)
	base := "/api/v1/recovery-points/" + pointID + "/entries/" + entryID
	exportBody := `{"schema_version":1,"selection":{"schema_version":1,"kind":"explicit","refs":[{"recovery_point_id":"` +
		pointID + `","entry_id":"` + entryID + `"}]},"archive_format":"zip","archive_profile":"zip_deflate_v1"}`
	memberBody := `{"schema_version":1,"index_revision":"` + indexRevision + `","member_chain":["` + memberID + `"]}`
	routes := []struct {
		name         string
		method       string
		path         string
		body         string
		idempotency  bool
		proof        auth.StepUpAction
		operatorRead bool
	}{
		{name: "export create", method: http.MethodPost, path: "/api/v1/asset-exports", body: exportBody, idempotency: true, proof: auth.StepUpActionAssetExportCreate},
		{name: "export status", method: http.MethodGet, path: "/api/v1/asset-exports/" + jobID},
		{name: "export cancel", method: http.MethodPost, path: "/api/v1/asset-exports/" + jobID + "/cancel", body: `{"schema_version":1}`},
		{name: "export ticket", method: http.MethodPost, path: "/api/v1/asset-exports/" + jobID + "/download-ticket", body: `{"schema_version":1}`, proof: auth.StepUpActionAssetExportDownload},
		{name: "archive index", method: http.MethodGet, path: base + "/archive-members", operatorRead: true},
		{name: "archive create", method: http.MethodPost, path: base + "/archive-member-jobs", body: memberBody, idempotency: true, operatorRead: true},
		{name: "archive status", method: http.MethodGet, path: base + "/archive-member-jobs/" + jobID + "?index_revision=" + indexRevision, operatorRead: true},
		{name: "archive cancel", method: http.MethodPost, path: base + "/archive-member-jobs/" + jobID + "/cancel", body: `{"schema_version":1,"index_revision":"` + indexRevision + `"}`, operatorRead: true},
		{name: "archive ticket", method: http.MethodPost, path: base + "/archive-member-jobs/" + jobID + "/delivery-ticket", body: `{"schema_version":1}`, proof: auth.StepUpActionAssetDownload},
	}
	for _, route := range routes {
		t.Run(route.name+"/unauthenticated", func(t *testing.T) {
			request := httptest.NewRequest(route.method, "https://xirang.example"+route.path, strings.NewReader(route.body))
			response := httptest.NewRecorder()
			fixture.router.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
		for _, role := range []string{"admin", "operator", "viewer", "unknown"} {
			role := role
			t.Run(route.name+"/"+role, func(t *testing.T) {
				request := httptest.NewRequest(route.method, "https://xirang.example"+route.path, strings.NewReader(route.body))
				request.Header.Set("Authorization", "Bearer "+fixture.tokens[role])
				if route.body != "" {
					request.Header.Set("Content-Type", "application/json")
				}
				if route.idempotency {
					request.Header.Set("Idempotency-Key", "0123456789abcdef")
				}
				if route.proof != "" {
					proof, hasProof := fixture.proofs[backupAssetRBACProofKey{role: role, action: route.proof}]
					if hasProof {
						request.Header.Set("X-Xirang-Step-Up", proof)
					}
					if (role == "admin" || role == "operator" && route.proof == auth.StepUpActionAssetDownload) && !hasProof {
						t.Fatalf("missing %s-bound %s proof", role, route.proof)
					}
				}
				response := httptest.NewRecorder()
				fixture.router.ServeHTTP(response, request)
				allowed := role == "admin" || role == "operator" && route.operatorRead
				want := http.StatusForbidden
				if allowed {
					want = http.StatusNotFound
				}
				if response.Code != want {
					t.Fatalf("status=%d want=%d body=%s", response.Code, want, response.Body.String())
				}
			})
		}
	}
}

func TestAssetSearchOverlayRoutesRequireListPermissionBeforeFeatureGate(t *testing.T) {
	fixture := setupBackupAssetRBACFixture(t)
	const id = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const pointID = "11111111111111111111111111111111"
	const entryID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	query := `{"schema_version":1,"root":{"op":"term","field":"name","text":"report"},"scope":{"mode":"current"},"sort":"relevance","limit":10}`
	routes := []struct {
		name     string
		method   string
		path     string
		body     string
		mutation bool
	}{
		{"search", http.MethodPost, "/api/v1/asset-search", `{"query":` + query + `}`, false},
		{"saved list", http.MethodGet, "/api/v1/asset-saved-searches", "", false},
		{"saved create", http.MethodPost, "/api/v1/asset-saved-searches", `{"query":` + query + `}`, true},
		{"saved get", http.MethodGet, "/api/v1/asset-saved-searches/" + id, "", false},
		{"saved update", http.MethodPatch, "/api/v1/asset-saved-searches/" + id, `{"query":` + query + `,"expected_version":1}`, true},
		{"saved delete", http.MethodDelete, "/api/v1/asset-saved-searches/" + id, `{"expected_version":1}`, true},
		{"favorite list", http.MethodGet, "/api/v1/asset-favorites", "", false},
		{"favorite add", http.MethodPost, "/api/v1/asset-favorites", `{"ref":{"recovery_point_id":"` + pointID + `","entry_id":"` + entryID + `"}}`, true},
		{"favorite remove", http.MethodDelete, "/api/v1/asset-favorites/" + pointID + "/" + entryID, "", true},
		{"tag list", http.MethodGet, "/api/v1/asset-tags", "", false},
		{"tag create", http.MethodPost, "/api/v1/asset-tags", `{"name":"Finance"}`, true},
		{"tag update", http.MethodPatch, "/api/v1/asset-tags/" + id, `{"name":"Finance 2026","expected_version":1}`, true},
		{"tag delete", http.MethodDelete, "/api/v1/asset-tags/" + id, `{"expected_version":1}`, true},
		{"tag assign", http.MethodPost, "/api/v1/asset-tags/" + id + "/assignments", `{"ref":{"recovery_point_id":"` + pointID + `","entry_id":"` + entryID + `"}}`, true},
		{"tag unassign", http.MethodDelete, "/api/v1/asset-tags/" + id + "/assignments/" + pointID + "/" + entryID, "", true},
		{"recent list", http.MethodGet, "/api/v1/asset-recent", "", false},
		{"recent clear", http.MethodPost, "/api/v1/asset-recent/clear", "", true},
	}

	for _, route := range routes {
		route := route
		t.Run(route.name+"/unauthenticated", func(t *testing.T) {
			response := performBackupAssetRBACRequest(t, fixture, route.method, route.path, route.body, "")
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
		for _, role := range []string{"admin", "operator", "viewer", "unknown"} {
			role := role
			t.Run(route.name+"/"+role, func(t *testing.T) {
				request := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
				request.Header.Set("Authorization", "Bearer "+fixture.tokens[role])
				if route.mutation {
					request.Header.Set("Idempotency-Key", "rbac-overlay-key-01")
				}
				response := httptest.NewRecorder()
				fixture.router.ServeHTTP(response, request)
				expected := http.StatusForbidden
				if role == "admin" || role == "operator" {
					expected = http.StatusServiceUnavailable
				}
				if response.Code != expected {
					t.Fatalf("status=%d want=%d body=%s", response.Code, expected, response.Body.String())
				}
			})
		}
	}
}

func TestRsyncVersioningMigrationRoutesRequireAdminBeforeFeatureGate(t *testing.T) {
	fixture := setupBackupAssetRBACFixture(t)
	routes := []struct {
		name string
		path string
		body string
	}{
		{"preflight", "/api/v1/tasks/7/rsync-versioning/preflights", `{"expected_task_revision":1,"requested_mode":"versioned_full_copy"}`},
		{"activate", "/api/v1/tasks/7/rsync-versioning/activate", `{"expected_task_revision":1,"preflight_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","migration_choice":"first_new_point"}`},
		{"rollback", "/api/v1/tasks/7/rsync-versioning/rollback-preparations", `{"expected_task_revision":1}`},
	}
	for _, route := range routes {
		route := route
		t.Run(route.name+"/unauthenticated", func(t *testing.T) {
			response := performBackupAssetRBACRequest(t, fixture, http.MethodPost, route.path, route.body, "")
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
		for _, role := range []string{"admin", "operator", "viewer", "unknown"} {
			role := role
			t.Run(route.name+"/"+role, func(t *testing.T) {
				response := performBackupAssetRBACRequest(t, fixture, http.MethodPost, route.path, route.body, fixture.tokens[role])
				expected := http.StatusForbidden
				if role == "admin" {
					expected = http.StatusServiceUnavailable
				}
				if response.Code != expected {
					t.Fatalf("status=%d want=%d body=%s", response.Code, expected, response.Body.String())
				}
				if expected == http.StatusServiceUnavailable && !strings.Contains(response.Body.String(), "feature_disabled") {
					t.Fatalf("admin request did not reach feature gate: %s", response.Body.String())
				}
			})
		}
	}
}

func TestRcloneVersioningRoutesRequireAdminBeforeFeatureGate(t *testing.T) {
	fixture := setupBackupAssetRBACFixture(t)
	routes := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"portable setup", http.MethodPost, "/api/v1/tasks/7/rclone-versioning/portable-binding-setups", `{"expected_task_revision":"1"}`},
		{"portable binding", http.MethodPut, "/api/v1/tasks/7/rclone-versioning/portable-binding", `{"expected_task_revision":"1","expected_binding_revision":"0","setup_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","target_remote":"archive","managed_root_locator":"archive:managed","bound_config":"[archive]\\ntype = s3\\n"}`},
		{"native setup", http.MethodPost, "/api/v1/tasks/7/rclone-versioning/native-binding-setups", `{"expected_task_revision":"1"}`},
		{"native binding", http.MethodPut, "/api/v1/tasks/7/rclone-versioning/native-binding", `{"expected_task_revision":"1","expected_binding_revision":"0","setup_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","region":"us-east-1","bucket":"private-bucket","managed_prefix":"managed/","role_arn":"arn:aws:iam::123456789012:role/xirang","bootstrap":{"mode":"workload_chain"},"encryption_profile":"sse_s3"}`},
		{"preflight", http.MethodPost, "/api/v1/tasks/7/rclone-versioning/preflights", `{"expected_task_revision":"1","requested_mode":"versioned_prefix"}`},
		{"activate", http.MethodPost, "/api/v1/tasks/7/rclone-versioning/activate", `{"expected_task_revision":"1","preflight_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","migration_choice":"first_new_point"}`},
		{"clean rollback", http.MethodPost, "/api/v1/tasks/7/rclone-versioning/clean-rollbacks", `{"expected_task_revision":"1","expected_binding_revision":"1"}`},
		{"rollback preparation", http.MethodPost, "/api/v1/tasks/7/rclone-versioning/rollback-preparations", `{"expected_task_revision":"1","expected_binding_revision":"1"}`},
	}
	for _, route := range routes {
		route := route
		t.Run(route.name+"/unauthenticated", func(t *testing.T) {
			response := performBackupAssetRBACRequest(t, fixture, route.method, route.path, route.body, "")
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
		for _, role := range []string{"admin", "operator", "viewer", "unknown"} {
			role := role
			t.Run(route.name+"/"+role, func(t *testing.T) {
				response := performBackupAssetRBACRequest(t, fixture, route.method, route.path, route.body, fixture.tokens[role])
				expected := http.StatusForbidden
				if role == "admin" {
					expected = http.StatusServiceUnavailable
				}
				if response.Code != expected {
					t.Fatalf("status=%d want=%d body=%s", response.Code, expected, response.Body.String())
				}
				if expected == http.StatusServiceUnavailable && !strings.Contains(response.Body.String(), "feature_disabled") {
					t.Fatalf("admin request did not reach feature gate: %s", response.Body.String())
				}
			})
		}
	}
}
