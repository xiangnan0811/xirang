package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/ga"
	backupruntime "xirang/backend/internal/backupasset/runtime"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRuntimeBackupAssetHandlerConfigSourceUsesFeatureLive(t *testing.T) {
	contents, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	if !strings.Contains(source, "live, err := runtime.FeatureLive()") {
		t.Fatal("handler config source must read Runtime.FeatureLive")
	}
	if !strings.Contains(source, "Enabled: live,") {
		t.Fatal("handler Enabled must come from FeatureLive, not the requested search flag")
	}
	if strings.Contains(source, "Enabled: searchConfig.Enabled") {
		t.Fatal("handler Enabled must not copy SearchOverlayConfig().Enabled")
	}
}

func TestRuntimeBackupAssetHandlerConfigSourceRequestedTrueLiveFalse(t *testing.T) {
	runtime := requestedClosedBackupAssetRuntime(t)
	source := runtimeBackupAssetHandlerConfigSource(runtime)
	config, err := source()
	if err != nil {
		t.Fatal(err)
	}
	if config.Enabled {
		t.Fatal("handler Enabled must stay false while FeatureLive is false")
	}
	searchConfig, overlayConfig, err := runtime.FoundationService().SearchOverlayConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !searchConfig.Enabled || !overlayConfig.Enabled {
		t.Fatalf("requested search/overlay enabled=%t/%t, want true", searchConfig.Enabled, overlayConfig.Enabled)
	}
}

func TestRequestedTrueFeatureLiveFalseClosesCatalogSearchOverlayContentHTTP(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	gin.SetMode(gin.TestMode)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.AuditLog{}, &model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	jwtManager := auth.NewJWTManager("FAKE_FEATURE_LIVE_HTTP_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	user := model.User{
		Username: "feature-live-http-admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
		Role: "admin", TOTPEnabled: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token, err := jwtManager.GenerateToken(user)
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(Dependencies{
		DB: db, JWTManager: jwtManager,
		BackupAssets: requestedClosedBackupAssetRuntime(t),
	})
	pointID := strings.Repeat("3", 32)
	entryID := strings.Repeat("a", 64)
	requests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/backup-repositories/" + strings.Repeat("1", 32) + "/recovery-points", ""},
		{http.MethodGet, "/api/v1/recovery-points/" + pointID + "/entries", ""},
		{http.MethodPost, "/api/v1/asset-search", `{"query":{"schema_version":1,"root":{"op":"term","field":"name","text":"safe"},"scope":{"mode":"exact_points","recovery_point_ids":["` + pointID + `"]},"sort":"relevance","limit":10}}`},
		{http.MethodGet, "/api/v1/asset-saved-searches", ""},
		{http.MethodPost, "/api/v1/recovery-points/" + pointID + "/entries/" + entryID + "/delivery-tickets", `{"schema_version":1,"action":"preview","renderer":"escaped_text","profile":"text_v1"}`},
	}
	for _, item := range requests {
		req := httptest.NewRequest(item.method, item.path, strings.NewReader(item.body))
		if item.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s status=%d body=%s", item.method, item.path, rec.Code, rec.Body.String())
		}
		if rec.Code == http.StatusGone {
			t.Fatalf("%s %s inherited leftover 410: %s", item.method, item.path, rec.Body.String())
		}
	}
}

func TestRequestedTrueFeatureLiveFalseClosesProductionCatalogAndContentHTTP(t *testing.T) {
	closedRouter, closedToken := newFeatureLiveHTTPRouter(t, "closed", backupruntime.ExistingInstallReadyUnacked(), true)
	liveRouter, liveToken := newFeatureLiveHTTPRouter(t, "live", backupruntime.FreshInstallReady(), true)
	pointID := strings.Repeat("3", 32)
	entryID := strings.Repeat("a", 64)
	catalogPath := "/api/v1/recovery-points/" + pointID + "/entries"
	contentPath := "/api/v1/recovery-points/" + pointID + "/entries/" + entryID + "/delivery-tickets"
	contentBody := `{"schema_version":1,"action":"preview","renderer":"escaped_text","profile":"text_v1"}`

	closedCatalog := serveFeatureLiveHTTP(closedRouter, http.MethodGet, catalogPath, "", closedToken)
	if closedCatalog.Code != http.StatusServiceUnavailable || !strings.Contains(closedCatalog.Body.String(), "备份资产功能未启用") {
		t.Fatalf("closed catalog status=%d body=%s", closedCatalog.Code, closedCatalog.Body.String())
	}
	closedContent := serveFeatureLiveHTTP(closedRouter, http.MethodPost, contentPath, contentBody, closedToken)
	if closedContent.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed content status=%d body=%s", closedContent.Code, closedContent.Body.String())
	}

	liveCatalog := serveFeatureLiveHTTP(liveRouter, http.MethodGet, catalogPath, "", liveToken)
	if liveCatalog.Code == http.StatusServiceUnavailable && strings.Contains(liveCatalog.Body.String(), "备份资产功能未启用") {
		t.Fatalf("live catalog stayed on FeatureDisabled stub: %s", liveCatalog.Body.String())
	}
	if liveCatalog.Code == http.StatusGone {
		t.Fatalf("live catalog inherited leftover 410: %s", liveCatalog.Body.String())
	}
	liveContent := serveFeatureLiveHTTP(liveRouter, http.MethodPost, contentPath, contentBody, liveToken)
	if strings.Contains(liveContent.Body.String(), "备份内容服务暂不可用") ||
		strings.Contains(liveContent.Body.String(), "备份资产功能未启用") {
		t.Fatalf("live content stayed feature-disabled: %s", liveContent.Body.String())
	}
}

func newFeatureLiveHTTPRouter(t *testing.T, name string, readiness ga.ReadinessSource, requested bool) (*gin.Engine, string) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	gin.SetMode(gin.TestMode)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name()+"-"+name, "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.AuditLog{}, &model.SystemSetting{}, &model.RecoveryPoint{}); err != nil {
		t.Fatal(err)
	}
	jwtManager := auth.NewJWTManager("FAKE_FEATURE_LIVE_HTTP_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	user := model.User{
		Username: "feature-live-http-" + name, PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
		Role: "admin", TOTPEnabled: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token, err := jwtManager.GenerateToken(user)
	if err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	if requested {
		if err := settingsService.Update("backup_assets.enabled", "true"); err != nil {
			t.Fatal(err)
		}
	}
	runtime := backupruntime.EnablementRuntime(readiness, nil).
		WithFoundation(backupasset.NewFoundationService(settingsService))
	catalogService := newProductionCatalogService(t, db, runtime)
	runtime = runtime.WithCatalogService(catalogService)
	broker := newProductionContentBroker(t, db, runtime)
	return NewRouter(Dependencies{
		DB: db, JWTManager: jwtManager,
		BackupAssets:  runtime,
		BackupContent: broker,
	}), token
}

func newProductionCatalogService(t *testing.T, db *gorm.DB, runtime *backupruntime.Runtime) *catalog.Service {
	t.Helper()
	now := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	ownership, err := catalog.NewOwnership(db)
	if err != nil {
		t.Fatal(err)
	}
	keyring := backupasset.NewKeyring(db, func() time.Time { return now })
	service, err := catalog.NewService(catalog.ServiceDependencies{
		DB: db, Ownership: ownership,
		Cursor: catalog.NewCursorCodec(keyring, func() time.Time { return now }, 15*time.Minute),
		Now:    func() time.Time { return now }, ReconcileInterval: 5 * time.Minute,
		FeatureEnabled: func() (bool, error) { return runtime.FeatureLive() },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func newProductionContentBroker(t *testing.T, db *gorm.DB, runtime *backupruntime.Runtime) *content.Broker {
	t.Helper()
	now := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	broker, err := content.NewBroker(content.BrokerDependencies{
		DB: db, Now: func() time.Time { return now },
		FeatureEnabled: func(context.Context) (bool, error) { return runtime.FeatureLive() },
		Authorize:      featureLiveHTTPAuthorizer{},
		Session:        featureLiveHTTPSession{},
		Lease:          featureLiveHTTPLease{},
		Source:         featureLiveHTTPSource{},
		Audit:          featureLiveHTTPAudit{},
		Budget:         featureLiveHTTPBudget{},
		Config: func(context.Context) (content.BrokerConfig, error) {
			return content.BrokerConfig{
				TicketTimeout: 5 * time.Second, PreviewTTL: 2 * time.Minute, MediaTTL: 15 * time.Minute,
				IdleTTL: time.Minute, WriteIdleTimeout: 30 * time.Second, LeaseHeartbeat: time.Minute,
				MaxBytesPerRequest: 1 << 20, MaxCumulativeBytes: 4 << 20,
				MaxRequests: 100, MaxInFlight: 2,
				Classification: content.ClassificationConfig{ScanBytes: 4 << 10},
				Renderer: content.RendererConfig{
					TextBytes: 1 << 10, HexBytes: 1 << 10, RasterMaxPixels: 1 << 20,
					PDFMaxBytes: 1 << 20, MediaMaxBytes: 1 << 20,
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return broker
}

func serveFeatureLiveHTTP(router *gin.Engine, method, path, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

type featureLiveHTTPAuthorizer struct{}

func (featureLiveHTTPAuthorizer) Authorize(context.Context, content.DeliveryActor, backupasset.AssetRef, content.DeliveryAction) (content.AuthorizedAsset, error) {
	return content.AuthorizedAsset{}, backupasset.ErrNotFound
}

func (featureLiveHTTPAuthorizer) Reauthorize(context.Context, content.DeliveryActor, content.AuthorizedAsset, content.DeliveryAction) error {
	return backupasset.ErrNotFound
}

type featureLiveHTTPSession struct{}

func (featureLiveHTTPSession) Validate(context.Context, content.DeliverySession) error {
	return backupasset.ErrForbidden
}

type featureLiveHTTPLease struct{}

func (featureLiveHTTPLease) Acquire(context.Context, backupasset.AcquireLeaseRequest) (backupasset.Lease, error) {
	return backupasset.Lease{}, content.ErrInvalidContentLease
}

func (featureLiveHTTPLease) Renew(context.Context, backupasset.LeaseFence) (backupasset.Lease, error) {
	return backupasset.Lease{}, content.ErrInvalidContentLease
}

func (featureLiveHTTPLease) ValidateFence(context.Context, backupasset.LeaseFence) error {
	return content.ErrInvalidContentLease
}

func (featureLiveHTTPLease) Release(context.Context, backupasset.LeaseFence) error {
	return content.ErrInvalidContentLease
}

func (featureLiveHTTPLease) Takeover(context.Context, backupasset.TakeoverLeaseRequest) (backupasset.Lease, error) {
	return backupasset.Lease{}, content.ErrInvalidContentLease
}

type featureLiveHTTPSource struct{}

func (featureLiveHTTPSource) OpenContentSource(context.Context, content.SourceRequest) (content.SourceSession, error) {
	return nil, content.ErrContentNotFound
}

func (featureLiveHTTPSource) ValidateContentCacheRoot(context.Context, string) error {
	return nil
}

type featureLiveHTTPAudit struct{}

func (featureLiveHTTPAudit) Write(context.Context, backupasset.AuditEventInput) error {
	return nil
}

func (featureLiveHTTPAudit) BacklogAvailable(context.Context) error {
	return nil
}

type featureLiveHTTPBudget struct{}

func (featureLiveHTTPBudget) Reserve(context.Context, content.ReservationIntent) (content.Reservation, error) {
	return content.Reservation{}, content.ErrInvalidReservation
}

func (featureLiveHTTPBudget) RecordBlocked(context.Context, content.BlockedRequest) error {
	return nil
}

func (featureLiveHTTPBudget) Finalize(context.Context, content.FinalizeIntent) (content.Finalization, error) {
	return content.Finalization{}, content.ErrInvalidReservation
}

func requestedClosedBackupAssetRuntime(t *testing.T) *backupruntime.Runtime {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s-settings?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	return backupruntime.EnablementRuntime(backupruntime.ExistingInstallReadyUnacked(), nil).
		WithFoundation(backupasset.NewFoundationService(settingsService))
}
