package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/api/handlers"
	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type routerBackupContentSchemeService struct {
	issueRequests []content.IssueRequest
}

func (service *routerBackupContentSchemeService) Issue(
	_ context.Context,
	request content.IssueRequest,
) (content.IssuedTicket, error) {
	service.issueRequests = append(service.issueRequests, request)
	expiresAt := time.Now().UTC().Add(time.Minute)
	cookie, err := content.NewDeliveryCookie(
		strings.Repeat("d", 32), "v1."+strings.Repeat("A", 43), expiresAt, request.SecureCookie,
	)
	if err != nil {
		return content.IssuedTicket{}, err
	}
	return content.IssuedTicket{
		Descriptor: content.TicketDescriptor{
			SchemaVersion: 1, ContentURL: cookie.Path, Action: request.Action, Renderer: request.Renderer,
			Profile: request.Profile, ContentType: "image/png", ContentLength: 1, ETag: `"router-test"`,
			Range: content.RangeSingle, Classification: content.ClassificationNonSecret, ExpiresAt: expiresAt,
			IdleExpiresAt: expiresAt, FallbackActions: []content.DeliveryAction{},
		},
		Cookie: cookie,
	}, nil
}

func (*routerBackupContentSchemeService) Serve(context.Context, content.GatewayRequest, http.ResponseWriter) error {
	return content.ErrContentNotFound
}

func (*routerBackupContentSchemeService) RevokeSession(context.Context, string, string) error {
	return nil
}

func TestEveryStepUpRouteDeclaresExpectedAction(t *testing.T) {
	files := []string{
		"router.go",
		"handlers/batch_handler.go",
		"handlers/task_handler.go",
		"handlers/terminal_handler.go",
		"handlers/credential_access_grant.go",
	}
	combined := ""
	for _, path := range files {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		combined += "\n" + string(contents)
	}
	for _, actionConstant := range []string{
		"auth.StepUpActionSSHKeyExport",
		"auth.StepUpActionTerminalOpen",
		"auth.StepUpActionConfigImport",
		"auth.StepUpActionConfigExport",
		"auth.StepUpActionSnapshotRestore",
		"auth.StepUpActionTaskRestoreTrigger",
		"auth.StepUpActionTaskManualTrigger",
		"auth.StepUpActionTaskBatchTrigger",
		"auth.StepUpActionBatchCommandCreate",
	} {
		if !strings.Contains(combined, actionConstant) {
			t.Errorf("production step-up caller is missing explicit %s", actionConstant)
		}
	}
	for _, forbidden := range []string{
		`RequireStepUp(dep.DB, dep.JWTManager, "`,
		`RequireStepUpIf(dep.DB, dep.JWTManager, "`,
		`EnforceStepUp(c, h.db, h.jwtManager, CredentialGrantAction`,
		`validateStepUpProof(h.db, h.jwtManager, authMsg.StepUpProof, claims.UserID, claims.Role)`,
	} {
		if strings.Contains(combined, forbidden) {
			t.Errorf("step-up caller lacks explicit typed action: %s", forbidden)
		}
	}
}

func TestBackupAssetRuntimeUsesEffectiveAuditSettings(t *testing.T) {
	contents, err := os.ReadFile("../backupasset/runtime/runtime.go")
	if err != nil {
		t.Fatalf("read runtime.go: %v", err)
	}
	source := string(contents)
	if !strings.Contains(source, "backupasset.NewAuditWriterWithConfigSource") || !strings.Contains(source, "foundation.AuditConfig") {
		t.Fatal("backup asset runtime does not keep effective asset-audit settings dynamic")
	}
	if strings.Contains(source, "backupasset.NewAuditWriter(dep.DB, keyring") {
		t.Fatal("backup asset runtime silently hard-codes asset-audit settings")
	}
}

func TestRouterUsesInjectedSharedBackupAssetRuntime(t *testing.T) {
	contents, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	source := string(contents)
	if !strings.Contains(source, "BackupAssets") || !strings.Contains(source, "dep.BackupAssets.RepositoryService()") {
		t.Fatal("router does not consume the injected backup asset runtime")
	}
	if !strings.Contains(source, "dep.BackupAssets.FeatureTransitioner()") ||
		!strings.Contains(source, "WithBackupAssetTransitioner") {
		t.Fatal("router does not inject the backup asset feature transitioner into settings/config handlers")
	}
	for _, forbidden := range []string{
		"NewSSHCommandTransportWithConcurrencySource",
		"NewRsyncAdapterWithLimitsSource",
		"NewResticAdapterWithLimitsSource",
		"NewResticAdapterWithPublication",
		"NewRcloneAdapterWithLimitsSource",
		"newBackupRepositoryHandler",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("router constructed a fallback provider runtime: %s", forbidden)
		}
	}
}

func TestBackupAssetRuntimeKeepsProviderSettingsDynamic(t *testing.T) {
	contents, err := os.ReadFile("../backupasset/runtime/runtime.go")
	if err != nil {
		t.Fatalf("read runtime.go: %v", err)
	}
	source := string(contents)
	for _, required := range []string{
		"foundation.ProviderConfig()",
		"NewSSHCommandTransportWithConcurrencySource",
		"NewRsyncAdapterWithLimitsSource",
		"NewResticAdapterWithPublication",
		"NewRcloneAdapterWithLimitsSource",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("backup asset runtime freezes dynamic Provider setting: missing %s", required)
		}
	}
}

func TestResolveAllowedOrigin(t *testing.T) {
	if got := resolveAllowedOrigin("https://xirang.example.com", "xirang.example.com:8080", []string{"https://xirang.example.com"}); got != "https://xirang.example.com" {
		t.Fatalf("期望返回匹配域名，实际: %s", got)
	}

	if got := resolveAllowedOrigin("https://bad.example.com", "xirang.example.com:8080", []string{"https://xirang.example.com"}); got != "" {
		t.Fatalf("期望返回空，实际: %s", got)
	}

	if got := resolveAllowedOrigin("https://foo.example.com", "xirang.example.com:8080", []string{"*"}); got != "https://foo.example.com" {
		t.Fatalf("通配符应回显 origin，实际: %s", got)
	}

	if got := resolveAllowedOrigin("", "xirang.example.com:8080", []string{"*"}); got != "" {
		t.Fatalf("空 origin 不应回退通配符，实际: %s", got)
	}

	if got := resolveAllowedOrigin("", "xirang.example.com:8080", []string{"https://xirang.example.com"}); got != "" {
		t.Fatalf("空 origin 应返回空字符串，实际: %s", got)
	}

	if got := resolveAllowedOrigin("http://192.168.1.20:5173", "192.168.1.20:8080", nil); got != "http://192.168.1.20:5173" {
		t.Fatalf("同主机 Origin 应自动放行，实际: %s", got)
	}

	if got := resolveAllowedOrigin("null", "192.168.1.20:8080", nil); got != "" {
		t.Fatalf("非法 Origin 应拒绝，实际: %s", got)
	}

	if got := resolveAllowedOrigin("http://evil.com:5173", "192.168.1.20:8080", nil); got != "" {
		t.Fatalf("不同主机 Origin 应拒绝，实际: %s", got)
	}
}

func TestNewRouterRegisterRoutes(t *testing.T) {
	g := NewRouter(Dependencies{})
	routes := g.Routes()
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/recovery-points/:id/entries/:entryId/preview-jobs"},
		{http.MethodGet, "/api/v1/recovery-points/:id/entries/:entryId/preview-jobs/:jobId"},
		{http.MethodPost, "/api/v1/recovery-points/:id/entries/:entryId/preview-jobs/:jobId/cancel"},
		{http.MethodGet, "/api/v1/recovery-points/:id/entries/:entryId/processing"},
	} {
		if !hasRoute(routes, route.method, route.path) {
			t.Fatalf("backup asset route is missing: %s %s", route.method, route.path)
		}
	}

	if !hasRoute(routes, http.MethodGet, "/api/v1/admin/backup-asset-processing") {
		t.Fatalf("未注册备份资产处理管理摘要接口")
	}

	if !hasRoute(routes, http.MethodGet, "/api/v1/tasks") {
		t.Fatalf("未注册任务列表接口")
	}
	if !hasRoute(routes, http.MethodGet, "/api/v1/tasks/:id/logs") {
		t.Fatalf("未注册任务日志接口")
	}
	if !hasRoute(routes, http.MethodGet, "/api/v1/alerts/:id/deliveries") {
		t.Fatalf("未注册告警投递记录接口")
	}
	if !hasRoute(routes, http.MethodGet, "/api/v1/alerts/delivery-stats") {
		t.Fatalf("未注册告警投递统计接口")
	}
	if !hasRoute(routes, http.MethodGet, "/api/v1/overview/traffic") {
		t.Fatalf("未注册概览流量趋势接口")
	}
	if !hasRoute(routes, http.MethodGet, "/api/v1/overview/backup-confidence") {
		t.Fatalf("未注册备份可信度接口")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/integrations/:id/test") {
		t.Fatalf("未注册通知通道测试接口")
	}
	if !hasRoute(routes, http.MethodGet, "/api/v1/audit-logs") {
		t.Fatalf("未注册审计日志接口")
	}
	if !hasRoute(routes, http.MethodGet, "/api/v1/audit-logs/export") {
		t.Fatalf("未注册审计导出接口")
	}
	if !hasRoute(routes, http.MethodGet, "/api/v1/credential-audit-events") {
		t.Fatalf("未注册凭据审计事件列表接口")
	}
	if !hasRoute(routes, http.MethodGet, "/api/v1/credential-audit-events/export") {
		t.Fatalf("未注册凭据审计事件导出接口")
	}
	if !hasRoute(routes, http.MethodGet, "/api/v1/credential-access-grants") {
		t.Fatalf("未注册凭据临时授权列表接口")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/credential-access-grants/terminal") {
		t.Fatalf("未注册终端凭据临时授权接口")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/credential-access-grants/config-import") {
		t.Fatalf("未注册配置导入凭据临时授权接口")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/credential-access-grants/config-export") {
		t.Fatalf("未注册配置导出凭据临时授权接口")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/credential-access-grants/snapshot-restore") {
		t.Fatalf("未注册快照恢复凭据临时授权接口")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/credential-access-grants/task-restore") {
		t.Fatalf("未注册任务恢复凭据临时授权接口")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/credential-access-grants/task-manual-trigger") {
		t.Fatalf("未注册任务手动触发凭据临时授权接口")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/credential-access-grants/task-batch-trigger") {
		t.Fatalf("未注册任务批量触发凭据临时授权接口")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/credential-access-grants/batch-command") {
		t.Fatalf("未注册批量命令凭据临时授权接口")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/tasks/batch-trigger") {
		t.Fatalf("未注册任务批量触发接口")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/alerts/:id/retry-delivery") {
		t.Fatalf("未注册告警投递重发接口")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/alerts/:id/retry-failed-deliveries") {
		t.Fatalf("未注册失败投递批量重发接口")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/alerts/bulk-resolve") {
		t.Fatalf("未注册告警批量解决接口")
	}
	if hasRoute(routes, http.MethodPost, "/api/v1/nodes/:id/exec") {
		t.Fatalf("不应注册节点远程执行接口")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/nodes/batch-delete") {
		t.Fatalf("未注册节点批量删除接口")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/nodes/:id/doctor") {
		t.Fatalf("未注册节点 Doctor 接口")
	}
	if !hasRoute(routes, http.MethodGet, "/api/v1/settings/security-risk-summary") {
		t.Fatalf("未注册安全风险摘要接口")
	}
	if !hasRoute(routes, http.MethodGet, "/api/v1/app-credentials/profiles") {
		t.Fatalf("未注册应用感知备份 profile 接口")
	}
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/tasks/:id/rsync-versioning/preflights"},
		{http.MethodPost, "/api/v1/tasks/:id/rsync-versioning/activate"},
		{http.MethodPost, "/api/v1/tasks/:id/rsync-versioning/rollback-preparations"},
		{http.MethodPost, "/api/v1/tasks/:id/rclone-versioning/portable-binding-setups"},
		{http.MethodPut, "/api/v1/tasks/:id/rclone-versioning/portable-binding"},
		{http.MethodPost, "/api/v1/tasks/:id/rclone-versioning/native-binding-setups"},
		{http.MethodPut, "/api/v1/tasks/:id/rclone-versioning/native-binding"},
		{http.MethodPost, "/api/v1/tasks/:id/rclone-versioning/preflights"},
		{http.MethodPost, "/api/v1/tasks/:id/rclone-versioning/activate"},
		{http.MethodPost, "/api/v1/tasks/:id/rclone-versioning/clean-rollbacks"},
		{http.MethodPost, "/api/v1/tasks/:id/rclone-versioning/rollback-preparations"},
		{http.MethodPost, "/api/v1/backup-repositories/connect"},
		{http.MethodGet, "/api/v1/backup-repositories"},
		{http.MethodGet, "/api/v1/backup-repositories/:id"},
		{http.MethodPost, "/api/v1/backup-repositories/:id/reconcile"},
		{http.MethodPost, "/api/v1/backup-repositories/:id/disconnect"},
	} {
		if !hasRoute(routes, route.method, route.path) {
			t.Fatalf("backup repository route missing: %s %s", route.method, route.path)
		}
	}
	if hasRoute(routes, http.MethodPost, "/api/v1/backup-repositories/probe") {
		t.Fatal("standalone backup repository probe route must not be registered")
	}
	deprecatedHookTemplatesPath := "/api/v1/" + "hook" + "-templates"
	if hasRoute(routes, http.MethodGet, deprecatedHookTemplatesPath) {
		t.Fatalf("不应继续注册已废弃的 hook templates 接口")
	}
}

func TestRecoveryRBACRouterRegistersTaskNineRouteMatrix(t *testing.T) {
	routes := NewRouter(Dependencies{}).Routes()
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/recovery-plans"},
		{http.MethodGet, "/api/v1/recovery-plans/:id"},
		{http.MethodPost, "/api/v1/recovery-plans/:id/preflights"},
		{http.MethodPost, "/api/v1/recovery-plans/:id/security-overrides"},
		{http.MethodPost, "/api/v1/recovery-plans/:id/write-authorizations"},
		{http.MethodPost, "/api/v1/recovery-plans/:id/execute"},
		{http.MethodPost, "/api/v1/recovery-plans/:id/cancel"},
		{http.MethodGet, "/api/v1/recovery-jobs/:id"},
		{http.MethodGet, "/api/v1/recovery-jobs/:id/items"},
		{http.MethodGet, "/api/v1/recovery-jobs/:id/results"},
		{http.MethodPost, "/api/v1/recovery-jobs/:id/cancel"},
		{http.MethodPost, "/api/v1/recovery-jobs/:id/exact-mirror-delete-authorizations"},
		{http.MethodPost, "/api/v1/recovery-jobs/:id/results/:resultId/download-ticket"},
		{http.MethodPost, "/api/v1/recovery-jobs/:id/results/retain"},
		{http.MethodPost, "/api/v1/recovery-jobs/:id/results/cleanup"},
		{http.MethodPost, "/api/v1/settings/backup-assets/recovery/target-roots"},
		{http.MethodPut, "/api/v1/settings/backup-assets/recovery/target-roots/:nodeId/:rootId"},
		{http.MethodDelete, "/api/v1/settings/backup-assets/recovery/target-roots/:nodeId/:rootId"},
		{http.MethodGet, "/api/v1/settings/backup-assets/recovery/target-roots"},
		{http.MethodPost, "/api/v1/settings/backup-assets/recovery/downgrade-readiness"},
	} {
		if !hasRoute(routes, route.method, route.path) {
			t.Errorf("Task 9 Recovery route missing: %s %s", route.method, route.path)
		}
	}
	for _, path := range []string{
		"/api/v1/recovery-plans/:id/authorizations",
		"/api/v1/recovery-jobs/:id/authorizations",
		"/api/v1/recovery-authorizations",
	} {
		if hasRoute(routes, http.MethodPost, path) {
			t.Errorf("undifferentiated Recovery authority route registered: POST %s", path)
		}
	}
}

func TestRecoveryRouteMatrixEnforcesAuthenticatedAdminRecoverRBACWithClosedEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.AuditLog{}); err != nil {
		t.Fatal(err)
	}
	jwtManager := auth.NewJWTManager("FAKE_RECOVERY_ROUTE_MATRIX_SIGNING_KEY_FOR_TEST_ONLY", time.Hour)
	tokens := map[string]string{}
	for _, role := range []string{"admin", "operator"} {
		user := model.User{
			Username: "recovery-route-matrix-" + role, PasswordHash: "FAKE_HASH_FOR_TEST_ONLY",
			Role: role, TokenVersion: 1,
		}
		if err := db.Create(&user).Error; err != nil {
			t.Fatal(err)
		}
		token, err := jwtManager.GenerateToken(user)
		if err != nil {
			t.Fatal(err)
		}
		tokens[role] = token
	}
	router := NewRouter(Dependencies{DB: db, JWTManager: jwtManager})
	id := strings.Repeat("a", 32)
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/recovery-plans"},
		{http.MethodGet, "/api/v1/recovery-plans/" + id},
		{http.MethodPost, "/api/v1/recovery-plans/" + id + "/preflights"},
		{http.MethodPost, "/api/v1/recovery-plans/" + id + "/security-overrides"},
		{http.MethodPost, "/api/v1/recovery-plans/" + id + "/write-authorizations"},
		{http.MethodPost, "/api/v1/recovery-plans/" + id + "/execute"},
		{http.MethodPost, "/api/v1/recovery-plans/" + id + "/cancel"},
		{http.MethodGet, "/api/v1/recovery-jobs/" + id},
		{http.MethodGet, "/api/v1/recovery-jobs/" + id + "/items?page=1&page_size=25"},
		{http.MethodGet, "/api/v1/recovery-jobs/" + id + "/results?page=1&page_size=25"},
		{http.MethodPost, "/api/v1/recovery-jobs/" + id + "/cancel"},
		{http.MethodPost, "/api/v1/recovery-jobs/" + id + "/exact-mirror-delete-authorizations"},
		{http.MethodPost, "/api/v1/recovery-jobs/" + id + "/results/" + id + "/download-ticket"},
		{http.MethodPost, "/api/v1/recovery-jobs/" + id + "/results/retain"},
		{http.MethodPost, "/api/v1/recovery-jobs/" + id + "/results/cleanup"},
		{http.MethodPost, "/api/v1/settings/backup-assets/recovery/target-roots"},
		{http.MethodPut, "/api/v1/settings/backup-assets/recovery/target-roots/1/root-a"},
		{http.MethodDelete, "/api/v1/settings/backup-assets/recovery/target-roots/1/root-a"},
		{http.MethodGet, "/api/v1/settings/backup-assets/recovery/target-roots?node_id=1"},
		{http.MethodPost, "/api/v1/settings/backup-assets/recovery/downgrade-readiness"},
	}
	for _, route := range routes {
		for _, authority := range []struct {
			name       string
			token      string
			wantStatus int
		}{
			{name: "unauthenticated", wantStatus: http.StatusUnauthorized},
			{name: "operator_without_recover", token: tokens["operator"], wantStatus: http.StatusForbidden},
			{name: "admin_with_recover", token: tokens["admin"]},
		} {
			t.Run(authority.name+"_"+route.method+"_"+strings.ReplaceAll(route.path, "/", "_"), func(t *testing.T) {
				var body *strings.Reader
				if route.method != http.MethodGet {
					body = strings.NewReader(`{}`)
				} else {
					body = strings.NewReader("")
				}
				request := httptest.NewRequest(route.method, route.path, body)
				if route.method != http.MethodGet {
					request.Header.Set("Content-Type", "application/json")
				}
				if authority.token != "" {
					request.Header.Set("Authorization", "Bearer "+authority.token)
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				if authority.wantStatus != 0 && response.Code != authority.wantStatus {
					t.Fatalf("status=%d want=%d body=%s", response.Code, authority.wantStatus, response.Body.String())
				}
				if authority.wantStatus == 0 && (response.Code == http.StatusUnauthorized || response.Code == http.StatusForbidden) {
					t.Fatalf("admin+recover rejected status=%d body=%s", response.Code, response.Body.String())
				}
				if authority.wantStatus == 0 {
					var envelope map[string]any
					if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || len(envelope) != 3 ||
						envelope["code"] == nil || envelope["message"] == nil {
						t.Fatalf("non-standard closed handler envelope status=%d body=%s err=%v", response.Code, response.Body.String(), err)
					}
				}
				for _, forbidden := range []string{authority.token, "Idempotency-Key", "proof", "secret", "ticket"} {
					if forbidden != "" && strings.Contains(response.Body.String(), forbidden) {
						t.Fatalf("RBAC envelope leaked authority %q: %s", forbidden, response.Body.String())
					}
				}
			})
		}
	}
}

func TestRecoveryRouteRateLimitReturnsClosed429WithoutAuthorityEvidence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.AuditLog{}); err != nil {
		t.Fatal(err)
	}
	jwtManager := auth.NewJWTManager("FAKE_RECOVERY_RATE_LIMIT_SIGNING_KEY_FOR_TEST_ONLY", time.Hour)
	user := model.User{
		Username: "recovery-rate-limit-admin", PasswordHash: "FAKE_HASH_FOR_TEST_ONLY",
		Role: "admin", TokenVersion: 1,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token, err := jwtManager.GenerateToken(user)
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(Dependencies{DB: db, JWTManager: jwtManager})
	path := "/api/v1/recovery-plans/" + strings.Repeat("a", 32)
	var response *httptest.ResponseRecorder
	for index := 0; index < 31; index++ {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response = httptest.NewRecorder()
		router.ServeHTTP(response, request)
	}
	if response == nil || response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" {
		t.Fatalf("rate limit status=%d retry-after=%q body=%s", response.Code, response.Header().Get("Retry-After"), response.Body.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || len(envelope) != 3 ||
		envelope["code"] != float64(http.StatusTooManyRequests) {
		t.Fatalf("rate-limit envelope=%v body=%s err=%v", envelope, response.Body.String(), err)
	}
	for _, forbidden := range []string{token, "proof", "secret", "ticket", strings.Repeat("a", 32)} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("rate-limit response leaked authority %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestBackupContentRoutesSplitAuthorizationFromCookieGateway(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	gin.SetMode(gin.TestMode)
	router := NewRouter(Dependencies{})
	deliveryID := strings.Repeat("d", 32)
	routes := router.Routes()
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/recovery-points/:id/entries/:entryId/delivery-tickets"},
		{http.MethodGet, "/api/v1/asset-content/:deliveryId"},
		{http.MethodHead, "/api/v1/asset-content/:deliveryId"},
	} {
		if !hasRoute(routes, route.method, route.path) {
			t.Fatalf("missing backup content route %s %s", route.method, route.path)
		}
	}

	contentPath := "/api/v1/asset-content/" + deliveryID
	for _, requestCase := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, contentPath},
		{http.MethodHead, contentPath},
		{http.MethodOptions, contentPath},
		{http.MethodPost, contentPath},
		{http.MethodGet, contentPath + "/"},
	} {
		request := httptest.NewRequest(requestCase.method, requestCase.path, nil)
		request.Header.Set("Authorization", "Bearer FAKE_CONTENT_AUTHORIZATION_FOR_TEST_ONLY")
		request.Header.Set("Origin", "https://evil.example")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code == http.StatusNoContent || response.Code == http.StatusMovedPermanently ||
			response.Code == http.StatusTemporaryRedirect || response.Code == http.StatusPermanentRedirect {
			t.Fatalf("content-shaped request bypassed safe chain: %s %s -> %d", requestCase.method, requestCase.path, response.Code)
		}
		if response.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" ||
			response.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("unsafe content headers for %s %s: %v", requestCase.method, requestCase.path, response.Header())
		}
	}
}

func TestRouterInjectsTrustedProxySchemePolicyIntoBackupContent(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	gin.SetMode(gin.TestMode)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.AuditLog{}); err != nil {
		t.Fatal(err)
	}
	admin := model.User{
		Username: "content-scheme-admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin",
	}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	jwtManager := auth.NewJWTManager("FAKE_CONTENT_SCHEME_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	token, err := jwtManager.GenerateToken(admin)
	if err != nil {
		t.Fatal(err)
	}
	service := &routerBackupContentSchemeService{}
	router := NewRouter(Dependencies{
		DB: db, JWTManager: jwtManager, BackupContent: service, TrustedProxies: []string{"127.0.0.1"},
		BackupContentConfig: func(context.Context) (handlers.BackupContentHandlerConfig, error) {
			return handlers.BackupContentHandlerConfig{TicketTimeout: 5 * time.Second}, nil
		},
	})
	pointID, entryID := strings.Repeat("1", 32), strings.Repeat("a", 64)
	target := "http://xirang.example/api/v1/recovery-points/" + pointID + "/entries/" + entryID + "/delivery-tickets"
	body := `{"schema_version":1,"action":"preview","renderer":"safe_raster","profile":"raster_v1"}`

	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:43210"
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(service.issueRequests) != 1 || !service.issueRequests[0].SecureCookie {
		t.Fatalf("trusted status=%d calls=%d body=%s", response.Code, len(service.issueRequests), response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	request.RemoteAddr = "192.0.2.10:43210"
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Forwarded-Proto", "https")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || len(service.issueRequests) != 1 {
		t.Fatalf("untrusted status=%d calls=%d body=%s", response.Code, len(service.issueRequests), response.Body.String())
	}
}

func TestSettingsSecurityRiskSummaryRouteRBAC(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.SSHKey{}, &model.SystemSetting{}, &model.CredentialAuditEvent{}, &model.AuditLog{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{}, &model.TaskRun{}, &model.RestoreDrillEvidence{}, &model.Alert{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	jwtManager := auth.NewJWTManager("settings-risk-route-signing-marker", time.Hour)
	tokens := make(map[string]string, 2)
	for _, role := range []string{"admin", "viewer"} {
		user := model.User{
			Username:     "settings-risk-rbac-" + role,
			PasswordHash: "hash-redacted",
			Role:         role,
		}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("创建 %s 用户失败: %v", role, err)
		}
		token, err := jwtManager.GenerateToken(user)
		if err != nil {
			t.Fatalf("生成 %s token 失败: %v", role, err)
		}
		tokens[role] = token
	}

	router := NewRouter(Dependencies{DB: db, JWTManager: jwtManager})
	viewerReq := httptest.NewRequest(http.MethodGet, "/api/v1/settings/security-risk-summary", nil)
	viewerReq.Header.Set("Authorization", "Bearer "+tokens["viewer"])
	viewerResp := httptest.NewRecorder()
	router.ServeHTTP(viewerResp, viewerReq)
	if viewerResp.Code != http.StatusForbidden {
		t.Fatalf("viewer 应被 settings risk summary RBAC 拒绝，实际状态码: %d，body=%s", viewerResp.Code, viewerResp.Body.String())
	}

	adminReq := httptest.NewRequest(http.MethodGet, "/api/v1/settings/security-risk-summary", nil)
	adminReq.Header.Set("Authorization", "Bearer "+tokens["admin"])
	adminResp := httptest.NewRecorder()
	router.ServeHTTP(adminResp, adminReq)
	if adminResp.Code != http.StatusOK {
		t.Fatalf("admin 应能访问安全风险摘要接口，实际状态码: %d，body=%s", adminResp.Code, adminResp.Body.String())
	}
}

func TestTaskBatchTriggerStaticRouteUsesBatchHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.NodeOwner{}, &model.Task{}, &model.TaskRun{}, &model.TaskLog{}, &model.TaskTrafficSample{}, &model.CredentialAccessGrant{}, &model.CredentialAuditEvent{}, &model.AuditLog{}, &model.SystemSetting{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	jwtManager := auth.NewJWTManager("task-batch-static-route-signing-marker", time.Hour)
	admin := model.User{Username: "task-batch-static-admin", PasswordHash: "hash-redacted", Role: "admin", TOTPEnabled: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("创建 admin 失败: %v", err)
	}
	token, err := jwtManager.GenerateToken(admin)
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}
	router := NewRouter(Dependencies{DB: db, JWTManager: jwtManager})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/batch-trigger", strings.NewReader(`{"task_ids":[999999]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("静态批量触发路由应命中 BatchTrigger 而非动态 task id 路由，实际状态码: %d，body=%s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Data struct {
			Results []struct {
				TaskID uint   `json:"task_id"`
				Error  string `json:"error"`
			} `json:"results"`
			Total        int `json:"total"`
			SuccessCount int `json:"success_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析批量触发响应失败: %v", err)
	}
	if payload.Data.Total != 1 || payload.Data.SuccessCount != 0 || len(payload.Data.Results) != 1 || payload.Data.Results[0].Error != "任务不存在" {
		t.Fatalf("批量触发静态路由响应不符合预期: %+v", payload.Data)
	}
}

func TestCredentialAccessGrantTerminalRouteRBACAndStepUp(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.CredentialAccessGrant{}, &model.CredentialAuditEvent{}, &model.AuditLog{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	jwtManager := auth.NewJWTManager("grant-route-signing-marker", time.Hour)
	tokens := make(map[string]string, 2)
	proofs := make(map[string]string, 2)
	for _, role := range []string{"admin", "viewer"} {
		user := model.User{
			Username:     "grant-route-rbac-" + role,
			PasswordHash: "hash-redacted",
			Role:         role,
			TOTPEnabled:  true,
		}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("创建 %s 用户失败: %v", role, err)
		}
		token, err := jwtManager.GenerateToken(user)
		if err != nil {
			t.Fatalf("生成 %s token 失败: %v", role, err)
		}
		proof, _, err := jwtManager.GenerateStepUpToken(user, auth.StepUpActionTerminalOpen)
		if err != nil {
			t.Fatalf("生成 %s step-up proof 失败: %v", role, err)
		}
		tokens[role] = token
		proofs[role] = proof
	}
	node := model.Node{Name: "grant-route-node", Host: "10.0.0.51", Port: 22, Username: "root", AuthType: "password", BackupDir: "grant-route-node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	router := NewRouter(Dependencies{DB: db, JWTManager: jwtManager})
	body := fmt.Sprintf(`{"node_id":%d,"reason":"维护","requested_ttl_seconds":600}`, node.ID)

	viewerReq := httptest.NewRequest(http.MethodPost, "/api/v1/credential-access-grants/terminal", strings.NewReader(body))
	viewerReq.Header.Set("Content-Type", "application/json")
	viewerReq.Header.Set("Authorization", "Bearer "+tokens["viewer"])
	viewerReq.Header.Set("X-Xirang-Step-Up", proofs["viewer"])
	viewerResp := httptest.NewRecorder()
	router.ServeHTTP(viewerResp, viewerReq)
	if viewerResp.Code != http.StatusForbidden {
		t.Fatalf("viewer 应被终端授权接口 RBAC 拒绝，实际状态码: %d，body=%s", viewerResp.Code, viewerResp.Body.String())
	}

	adminNoProofReq := httptest.NewRequest(http.MethodPost, "/api/v1/credential-access-grants/terminal", strings.NewReader(body))
	adminNoProofReq.Header.Set("Content-Type", "application/json")
	adminNoProofReq.Header.Set("Authorization", "Bearer "+tokens["admin"])
	adminNoProofResp := httptest.NewRecorder()
	router.ServeHTTP(adminNoProofResp, adminNoProofReq)
	if adminNoProofResp.Code != http.StatusForbidden || !strings.Contains(adminNoProofResp.Body.String(), "STEP_UP_REQUIRED") {
		t.Fatalf("admin 缺少 step-up proof 应被拒绝并返回机器可读字段，实际: %d，body=%s", adminNoProofResp.Code, adminNoProofResp.Body.String())
	}

	adminReq := httptest.NewRequest(http.MethodPost, "/api/v1/credential-access-grants/terminal", strings.NewReader(body))
	adminReq.Header.Set("Content-Type", "application/json")
	adminReq.Header.Set("Authorization", "Bearer "+tokens["admin"])
	adminReq.Header.Set("X-Xirang-Step-Up", proofs["admin"])
	adminResp := httptest.NewRecorder()
	router.ServeHTTP(adminResp, adminReq)
	if adminResp.Code != http.StatusCreated {
		t.Fatalf("admin 带 step-up proof 应能申请终端授权，实际状态码: %d，body=%s", adminResp.Code, adminResp.Body.String())
	}
}

func TestCredentialAccessGrantListRouteRBAC(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.CredentialAccessGrant{}, &model.CredentialAuditEvent{}, &model.AuditLog{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	jwtManager := auth.NewJWTManager("grant-list-route-signing-marker", time.Hour)
	tokens := make(map[string]string, 3)
	for _, role := range []string{"admin", "operator", "viewer"} {
		user := model.User{
			Username:     "grant-list-rbac-" + role,
			PasswordHash: "hash-redacted",
			Role:         role,
		}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("创建 %s 用户失败: %v", role, err)
		}
		token, err := jwtManager.GenerateToken(user)
		if err != nil {
			t.Fatalf("生成 %s token 失败: %v", role, err)
		}
		tokens[role] = token
	}

	if err := db.Create(&model.CredentialAccessGrant{
		RequesterUserID:     1,
		RequesterUsername:   "admin",
		RequesterRole:       "admin",
		Action:              "config.import",
		Purpose:             "config_import",
		Reason:              "例行导入",
		Status:              "active",
		RequestedTTLSeconds: 600,
		RequestedAt:         time.Now().UTC(),
		ExpiresAt:           time.Now().UTC().Add(10 * time.Minute),
	}).Error; err != nil {
		t.Fatalf("创建凭据临时授权失败: %v", err)
	}

	router := NewRouter(Dependencies{DB: db, JWTManager: jwtManager})
	for _, role := range []string{"operator", "viewer"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/credential-access-grants", nil)
		req.Header.Set("Authorization", "Bearer "+tokens[role])
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if resp.Code != http.StatusForbidden {
			t.Fatalf("%s 应被凭据临时授权列表 RBAC 拒绝，实际状态码: %d，body=%s", role, resp.Code, resp.Body.String())
		}
	}

	adminReq := httptest.NewRequest(http.MethodGet, "/api/v1/credential-access-grants", nil)
	adminReq.Header.Set("Authorization", "Bearer "+tokens["admin"])
	adminResp := httptest.NewRecorder()
	router.ServeHTTP(adminResp, adminReq)
	if adminResp.Code != http.StatusOK {
		t.Fatalf("admin 应能访问凭据临时授权列表，实际状态码: %d，body=%s", adminResp.Code, adminResp.Body.String())
	}
}

func TestCredentialAuditEventsRouteRBAC(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.CredentialAuditEvent{}, &model.AuditLog{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	jwtManager := auth.NewJWTManager("credential-audit-route-signing-marker", time.Hour)
	tokens := make(map[string]string, 3)
	for _, role := range []string{"admin", "operator", "viewer"} {
		user := model.User{
			Username:     "credential-audit-rbac-" + role,
			PasswordHash: "hash-redacted",
			Role:         role,
		}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("创建 %s 用户失败: %v", role, err)
		}
		token, err := jwtManager.GenerateToken(user)
		if err != nil {
			t.Fatalf("生成 %s token 失败: %v", role, err)
		}
		tokens[role] = token
	}

	if err := db.Create(&model.CredentialAuditEvent{
		UserID:           1,
		Username:         "admin",
		Role:             "admin",
		Action:           "ssh_key.export",
		Purpose:          "ssh_key_export",
		CredentialKind:   "ssh_key",
		CredentialSource: "ssh_key_id=1",
		Outcome:          "success",
		Metadata:         `{"format":"json"}`,
		CreatedAt:        time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("创建凭据审计事件失败: %v", err)
	}

	router := NewRouter(Dependencies{DB: db, JWTManager: jwtManager})
	for _, path := range []string{"/api/v1/credential-audit-events", "/api/v1/credential-audit-events/export"} {
		for _, role := range []string{"operator", "viewer"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer "+tokens[role])
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			if resp.Code != http.StatusForbidden {
				t.Fatalf("%s 应被凭据审计 RBAC 拒绝访问 %s，实际状态码: %d，body=%s", role, path, resp.Code, resp.Body.String())
			}
		}

		adminReq := httptest.NewRequest(http.MethodGet, path, nil)
		adminReq.Header.Set("Authorization", "Bearer "+tokens["admin"])
		adminResp := httptest.NewRecorder()
		router.ServeHTTP(adminResp, adminReq)
		if adminResp.Code != http.StatusOK {
			t.Fatalf("admin 应能访问 %s，实际状态码: %d，body=%s", path, adminResp.Code, adminResp.Body.String())
		}
	}
}

func TestBackupConfidenceRouteRBAC(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{}, &model.TaskRun{}, &model.RestoreDrillEvidence{}, &model.Alert{}, &model.AuditLog{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	jwtManager := auth.NewJWTManager("backup-confidence-route-signing-marker", time.Hour)
	tokens := make(map[string]string, 2)
	for _, role := range []string{"admin", "viewer"} {
		user := model.User{
			Username:     "backup-confidence-rbac-" + role,
			PasswordHash: "hash-redacted",
			Role:         role,
		}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("创建 %s 用户失败: %v", role, err)
		}
		token, err := jwtManager.GenerateToken(user)
		if err != nil {
			t.Fatalf("生成 %s token 失败: %v", role, err)
		}
		tokens[role] = token
	}

	router := NewRouter(Dependencies{DB: db, JWTManager: jwtManager})
	viewerReq := httptest.NewRequest(http.MethodGet, "/api/v1/overview/backup-confidence", nil)
	viewerReq.Header.Set("Authorization", "Bearer "+tokens["viewer"])
	viewerResp := httptest.NewRecorder()
	router.ServeHTTP(viewerResp, viewerReq)
	if viewerResp.Code != http.StatusOK {
		t.Fatalf("viewer 应能读取备份可信度接口，实际状态码: %d，body=%s", viewerResp.Code, viewerResp.Body.String())
	}

	noTokenReq := httptest.NewRequest(http.MethodGet, "/api/v1/overview/backup-confidence", nil)
	noTokenResp := httptest.NewRecorder()
	router.ServeHTTP(noTokenResp, noTokenReq)
	if noTokenResp.Code != http.StatusUnauthorized {
		t.Fatalf("缺少 token 应返回 401，实际状态码: %d，body=%s", noTokenResp.Code, noTokenResp.Body.String())
	}
}

func TestNodeDoctorRouteRBAC(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.NodeOwner{}, &model.AuditLog{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	jwtManager := auth.NewJWTManager("node-doctor-route-signing-marker", time.Hour)
	tokens := make(map[string]string, 3)
	userIDs := make(map[string]uint, 3)
	for _, role := range []string{"admin", "operator", "viewer"} {
		user := model.User{
			Username:     "node-doctor-rbac-" + role,
			PasswordHash: "hash-redacted",
			Role:         role,
		}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("创建 %s 用户失败: %v", role, err)
		}
		token, err := jwtManager.GenerateToken(user)
		if err != nil {
			t.Fatalf("生成 %s token 失败: %v", role, err)
		}
		tokens[role] = token
		userIDs[role] = user.ID
	}

	node := model.Node{
		Name:      "node-doctor-rbac",
		Host:      "10.0.0.41",
		Port:      22,
		Username:  "root",
		AuthType:  "password",
		Password:  "",
		BackupDir: "node-doctor-rbac",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	router := NewRouter(Dependencies{DB: db, JWTManager: jwtManager})
	path := fmt.Sprintf("/api/v1/nodes/%d/doctor", node.ID)

	viewerReq := httptest.NewRequest(http.MethodPost, path, nil)
	viewerReq.Header.Set("Authorization", "Bearer "+tokens["viewer"])
	viewerResp := httptest.NewRecorder()
	router.ServeHTTP(viewerResp, viewerReq)
	if viewerResp.Code != http.StatusForbidden {
		t.Fatalf("viewer 应被 nodes:test 拒绝，实际状态码: %d，body=%s", viewerResp.Code, viewerResp.Body.String())
	}

	operatorReq := httptest.NewRequest(http.MethodPost, path, nil)
	operatorReq.Header.Set("Authorization", "Bearer "+tokens["operator"])
	operatorResp := httptest.NewRecorder()
	router.ServeHTTP(operatorResp, operatorReq)
	if operatorResp.Code != http.StatusForbidden {
		t.Fatalf("operator 未拥有节点时应被 ownership 拒绝，实际状态码: %d，body=%s", operatorResp.Code, operatorResp.Body.String())
	}

	if err := db.Create(&model.NodeOwner{NodeID: node.ID, UserID: userIDs["operator"]}).Error; err != nil {
		t.Fatalf("创建节点 owner 失败: %v", err)
	}
	ownedOperatorReq := httptest.NewRequest(http.MethodPost, path, nil)
	ownedOperatorReq.Header.Set("Authorization", "Bearer "+tokens["operator"])
	ownedOperatorResp := httptest.NewRecorder()
	router.ServeHTTP(ownedOperatorResp, ownedOperatorReq)
	if ownedOperatorResp.Code != http.StatusOK {
		t.Fatalf("operator 拥有节点时应能访问 Doctor 接口，实际状态码: %d，body=%s", ownedOperatorResp.Code, ownedOperatorResp.Body.String())
	}

	adminReq := httptest.NewRequest(http.MethodPost, path, nil)
	adminReq.Header.Set("Authorization", "Bearer "+tokens["admin"])
	adminResp := httptest.NewRecorder()
	router.ServeHTTP(adminResp, adminReq)
	if adminResp.Code != http.StatusOK {
		t.Fatalf("admin 应能访问 Doctor 接口，实际状态码: %d，body=%s", adminResp.Code, adminResp.Body.String())
	}
}

func TestAlertBulkResolveRouteRBAC(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Alert{}, &model.AuditLog{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	jwtManager := auth.NewJWTManager("alert-bulk-resolve-route-signing-marker", time.Hour)
	tokens := make(map[string]string, 3)
	for _, role := range []string{"admin", "operator", "viewer"} {
		user := model.User{
			Username:     "alert-bulk-rbac-" + role,
			PasswordHash: "hash-redacted",
			Role:         role,
		}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("创建 %s 用户失败: %v", role, err)
		}
		token, err := jwtManager.GenerateToken(user)
		if err != nil {
			t.Fatalf("生成 %s token 失败: %v", role, err)
		}
		tokens[role] = token
	}

	alert := model.Alert{
		NodeID:      1,
		NodeName:    "node-a",
		Severity:    "critical",
		Status:      "open",
		ErrorCode:   "XR-RBAC",
		Message:     "rbac",
		Retryable:   true,
		TriggeredAt: time.Now(),
	}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}

	router := NewRouter(Dependencies{DB: db, JWTManager: jwtManager})
	body := fmt.Sprintf(`{"alert_ids":[%d]}`, alert.ID)

	viewerReq := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/bulk-resolve", strings.NewReader(body))
	viewerReq.Header.Set("Content-Type", "application/json")
	viewerReq.Header.Set("Authorization", "Bearer "+tokens["viewer"])
	viewerResp := httptest.NewRecorder()
	router.ServeHTTP(viewerResp, viewerReq)
	if viewerResp.Code != http.StatusForbidden {
		t.Fatalf("viewer 应被 RBAC 拒绝，实际状态码: %d，body=%s", viewerResp.Code, viewerResp.Body.String())
	}

	adminReq := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/bulk-resolve", strings.NewReader(body))
	adminReq.Header.Set("Content-Type", "application/json")
	adminReq.Header.Set("Authorization", "Bearer "+tokens["admin"])
	adminResp := httptest.NewRecorder()
	router.ServeHTTP(adminResp, adminReq)
	if adminResp.Code != http.StatusOK {
		t.Fatalf("admin 应能访问批量解决接口，实际状态码: %d，body=%s", adminResp.Code, adminResp.Body.String())
	}
}

func TestNewRouterCORSHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := NewRouter(Dependencies{
		AllowedOrigins: []string{"https://xirang.example.com"},
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://xirang.example.com")
	resp := httptest.NewRecorder()
	g.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}
	if got := resp.Header().Get("Access-Control-Allow-Origin"); got != "https://xirang.example.com" {
		t.Fatalf("期望允许 origin 被回写，实际: %s", got)
	}
	if got := resp.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("期望允许凭证头，实际: %s", got)
	}
	if got := resp.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Idempotency-Key") || !strings.Contains(got, "X-Xirang-Step-Up") {
		t.Fatalf("备份资产搜索/覆盖请求头未被 CORS 允许: %s", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	resp = httptest.NewRecorder()
	g.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("空 origin 场景期望状态码 200，实际: %d", resp.Code)
	}
	if got := resp.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("空 origin 不应写入 Allow-Origin，实际: %s", got)
	}
	if got := resp.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("空 origin 不应写入 Allow-Credentials，实际: %s", got)
	}

	req = httptest.NewRequest(http.MethodGet, "http://192.168.1.20:8080/healthz", nil)
	req.Header.Set("Origin", "http://192.168.1.20:5173")
	resp = httptest.NewRecorder()
	g.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("同主机跨端口 Origin 应返回 200，实际: %d", resp.Code)
	}
	if got := resp.Header().Get("Access-Control-Allow-Origin"); got != "http://192.168.1.20:5173" {
		t.Fatalf("同主机 Origin 应被回写，实际: %s", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	resp = httptest.NewRecorder()
	g.ServeHTTP(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("非法 origin 应返回 403，实际: %d", resp.Code)
	}
}

func TestRouterRegistersAssetSearchAndOverlayRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	routes := NewRouter(Dependencies{}).Routes()
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/asset-search"},
		{http.MethodGet, "/api/v1/asset-saved-searches"},
		{http.MethodPost, "/api/v1/asset-saved-searches"},
		{http.MethodGet, "/api/v1/asset-saved-searches/:id"},
		{http.MethodPatch, "/api/v1/asset-saved-searches/:id"},
		{http.MethodDelete, "/api/v1/asset-saved-searches/:id"},
		{http.MethodGet, "/api/v1/asset-favorites"},
		{http.MethodPost, "/api/v1/asset-favorites"},
		{http.MethodDelete, "/api/v1/asset-favorites/:recoveryPointId/:entryId"},
		{http.MethodGet, "/api/v1/asset-tags"},
		{http.MethodPost, "/api/v1/asset-tags"},
		{http.MethodPatch, "/api/v1/asset-tags/:id"},
		{http.MethodDelete, "/api/v1/asset-tags/:id"},
		{http.MethodPost, "/api/v1/asset-tags/:id/assignments"},
		{http.MethodDelete, "/api/v1/asset-tags/:id/assignments/:recoveryPointId/:entryId"},
		{http.MethodGet, "/api/v1/asset-recent"},
		{http.MethodPost, "/api/v1/asset-recent/clear"},
	} {
		if !hasRoute(routes, route.method, route.path) {
			t.Fatalf("missing backup asset route %s %s", route.method, route.path)
		}
	}
}

func TestRouterRegistersBackupAssetExportAndArchiveMemberRoutes(t *testing.T) {
	routes := NewRouter(Dependencies{}).Routes()
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/asset-exports"},
		{http.MethodGet, "/api/v1/asset-exports/:id"},
		{http.MethodPost, "/api/v1/asset-exports/:id/cancel"},
		{http.MethodPost, "/api/v1/asset-exports/:id/download-ticket"},
		{http.MethodGet, "/api/v1/recovery-points/:id/entries/:entryId/archive-members"},
		{http.MethodPost, "/api/v1/recovery-points/:id/entries/:entryId/archive-member-jobs"},
		{http.MethodGet, "/api/v1/recovery-points/:id/entries/:entryId/archive-member-jobs/:jobId"},
		{http.MethodPost, "/api/v1/recovery-points/:id/entries/:entryId/archive-member-jobs/:jobId/cancel"},
		{http.MethodPost, "/api/v1/recovery-points/:id/entries/:entryId/archive-member-jobs/:jobId/delivery-ticket"},
	} {
		if !hasRoute(routes, route.method, route.path) {
			t.Fatalf("missing backup asset Export/archive route %s %s", route.method, route.path)
		}
	}
}

func hasRoute(routes []gin.RouteInfo, method string, path string) bool {
	for _, route := range routes {
		if route.Method == method && route.Path == path {
			return true
		}
	}
	return false
}

func TestSwaggerUIEnabledDefaultsAndOverride(t *testing.T) {
	t.Run("production default off", func(t *testing.T) {
		t.Setenv("APP_ENV", "production")
		t.Setenv("SWAGGER_ENABLED", "")
		t.Setenv("GIN_MODE", "")
		if swaggerUIEnabled() {
			t.Fatal("生产环境默认应关闭 Swagger UI")
		}
	})
	t.Run("production force on", func(t *testing.T) {
		t.Setenv("APP_ENV", "production")
		t.Setenv("SWAGGER_ENABLED", "true")
		if !swaggerUIEnabled() {
			t.Fatal("SWAGGER_ENABLED=true 应强制开启")
		}
	})
	t.Run("development default on", func(t *testing.T) {
		t.Setenv("APP_ENV", "development")
		t.Setenv("SWAGGER_ENABLED", "")
		if !swaggerUIEnabled() {
			t.Fatal("开发环境默认应开启 Swagger UI")
		}
	})
	t.Run("development force off", func(t *testing.T) {
		t.Setenv("APP_ENV", "development")
		t.Setenv("SWAGGER_ENABLED", "false")
		if swaggerUIEnabled() {
			t.Fatal("SWAGGER_ENABLED=false 应强制关闭")
		}
	})
	t.Run("gin release without app env defaults off", func(t *testing.T) {
		t.Setenv("APP_ENV", "")
		t.Setenv("ENVIRONMENT", "")
		t.Setenv("GIN_MODE", "release")
		t.Setenv("SWAGGER_ENABLED", "")
		if swaggerUIEnabled() {
			t.Fatal("仅 GIN_MODE=release 视为生产时 Swagger 应默认关闭")
		}
	})
	t.Run("prod alias defaults off without gin release", func(t *testing.T) {
		t.Setenv("APP_ENV", "prod")
		t.Setenv("ENVIRONMENT", "")
		t.Setenv("GIN_MODE", "")
		t.Setenv("SWAGGER_ENABLED", "")
		if swaggerUIEnabled() {
			t.Fatal("APP_ENV=prod 应关闭 Swagger 默认开启")
		}
	})
	t.Run("staging alias defaults off without gin release", func(t *testing.T) {
		t.Setenv("APP_ENV", "staging")
		t.Setenv("ENVIRONMENT", "")
		t.Setenv("GIN_MODE", "debug")
		t.Setenv("SWAGGER_ENABLED", "")
		if swaggerUIEnabled() {
			t.Fatal("APP_ENV=staging 即使 GIN_MODE=debug 也应关闭 Swagger 默认开启")
		}
	})
}
