package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/ga"
	"xirang/backend/internal/backupasset/overlay"
	"xirang/backend/internal/backupasset/publication"
	assetruntime "xirang/backend/internal/backupasset/runtime"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type settingsTransitionSpy struct {
	targets       []bool
	beforePersist func() error
	err           error
}

func (spy *settingsTransitionSpy) TransitionFeature(_ context.Context, enabled bool, persist func() error) error {
	spy.targets = append(spy.targets, enabled)
	if spy.err != nil {
		return spy.err
	}
	if spy.beforePersist != nil {
		if err := spy.beforePersist(); err != nil {
			return err
		}
	}
	return persist()
}

func TestSettingsFailedTransitionLeavesEnabledOverrideUnchanged(t *testing.T) {
	_, svc, _, spy, router := setupSettingsTransitionHandler(t)
	spy.err = errors.New("FAKE_TRANSITION_DRAIN_FAILURE_FOR_TEST_ONLY")

	request := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"backup_assets.enabled":"true"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "FAKE_TRANSITION_DRAIN_FAILURE_FOR_TEST_ONLY") {
		t.Fatalf("unexpected PUT leaked internal error: %s", response.Body.String())
	}
	if len(spy.targets) != 1 || !spy.targets[0] || svc.GetEffective("backup_assets.enabled") != "false" {
		t.Fatalf("failed transition targets=%v effective=%q", spy.targets, svc.GetEffective("backup_assets.enabled"))
	}
}

func TestSettingsFailedDeleteRestoreTransitionStaysInternalError(t *testing.T) {
	t.Setenv("BACKUP_ASSETS_ENABLED", "true")
	_, svc, _, spy, router := setupSettingsTransitionHandler(t)
	if err := svc.Update("backup_assets.enabled", "false"); err != nil {
		t.Fatal(err)
	}
	spy.err = errors.New("FAKE_DELETE_RESTORE_TRANSITION_FAILURE_FOR_TEST_ONLY")

	request := httptest.NewRequest(http.MethodDelete, "/settings/backup_assets.enabled", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "FAKE_DELETE_RESTORE_TRANSITION_FAILURE_FOR_TEST_ONLY") {
		t.Fatalf("unexpected delete-restore leaked internal error: %s", response.Body.String())
	}
	if len(spy.targets) != 1 || !spy.targets[0] || svc.GetEffective("backup_assets.enabled") != "false" {
		t.Fatalf("failed delete-restore targets=%v effective=%q", spy.targets, svc.GetEffective("backup_assets.enabled"))
	}
}

func (*settingsTransitionSpy) PrepareApplicationDowngrade(context.Context, func() error) error {
	return nil
}
func (*settingsTransitionSpy) PrepareSchemaDown(context.Context, func() error) error { return nil }

var _ publication.FeatureTransitioner = (*settingsTransitionSpy)(nil)

type settingsRuntimeSettingsTransitionSpy struct {
	settingsTransitionSpy
	current        map[string]string
	overlay        map[string]string
	effective      map[string]string
	config         backupasset.ExportConfig
	overlayService *overlay.Service
	calls          int
}

type settingsCanceledPersistenceRuntime struct {
	settingsTransitionSpy
	called           bool
	calls            int
	current          map[string]string
	overlay          map[string]string
	effective        map[string]string
	bundle           backupasset.FoundationTransitionConfig
	cancelPersist    bool
	failAfterPersist error
}

func (runtime *settingsCanceledPersistenceRuntime) TransitionBackupAssetSettingsContext(
	ctx context.Context,
	current map[string]string,
	overlay map[string]string,
	effective map[string]string,
	config backupasset.ExportConfig,
	persist func(context.Context) error,
) error {
	runtime.called = true
	runtime.calls++
	bundle, err := backupasset.FoundationTransitionConfigFromValues(effective)
	if err != nil {
		return err
	}
	contentConfig, err := backupasset.ContentConfigFromValues(effective)
	if err != nil {
		return err
	}
	searchConfig, overlayConfig, err := backupasset.SearchOverlayConfigFromValues(effective)
	if err != nil {
		return err
	}
	if contentConfig.PreviewTTL != bundle.Content.PreviewTTL ||
		searchConfig.PageSizeMax != bundle.Search.PageSizeMax ||
		overlayConfig.IdempotencyTTL != bundle.Overlay.IdempotencyTTL ||
		config.WorkerConcurrency != bundle.Export.WorkerConcurrency {
		return errors.New("FAKE_PROSPECTIVE_CONTENT_SEARCH_CONFIG_MISMATCH_FOR_TEST_ONLY")
	}
	runtime.current = current
	runtime.overlay = overlay
	runtime.effective = effective
	runtime.bundle = bundle
	if !runtime.cancelPersist {
		if err := persist(ctx); err != nil {
			return err
		}
		return runtime.failAfterPersist
	}
	opCtx, cancel := context.WithCancel(ctx)
	cancel()
	return persist(opCtx)
}

func (runtime *settingsCanceledPersistenceRuntime) TransitionBackupAssetSettingsContextWithRestore(
	ctx context.Context,
	current map[string]string,
	overlay map[string]string,
	effective map[string]string,
	config backupasset.ExportConfig,
	persist func(context.Context) error,
	restore func(context.Context) error,
) error {
	err := runtime.TransitionBackupAssetSettingsContext(ctx, current, overlay, effective, config, persist)
	if err == nil {
		return nil
	}
	return errors.Join(err, restore(ctx))
}

func TestSettingsPUTPersistenceHonorsRuntimeOperationCancellation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	svc := settings.NewService(db)
	runtime := &settingsCanceledPersistenceRuntime{cancelPersist: true}
	handler := NewSettingsHandler(db, svc).WithBackupAssetTransitioner(runtime)
	router := gin.New()
	router.PUT("/settings", handler.BatchUpdate)

	const key = "backup_assets.export.worker_concurrency"
	request := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"`+key+`":"3"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s, want canceled runtime persistence to return generic 500", response.Code, response.Body.String())
	}
	if !runtime.called {
		t.Fatal("handler did not use the context-aware runtime settings transition")
	}
	var envelope Response
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Code != http.StatusInternalServerError || envelope.Message != "服务器内部错误" {
		t.Fatalf("unexpected canceled persistence envelope: %+v", envelope)
	}
	var count int64
	if err := db.Model(&model.SystemSetting{}).Where("key = ?", key).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("canceled runtime persistence created %s row count=%d", key, count)
	}
}

func TestSettingsPUTUsesProspectiveContentAndSearchBundleBeforePersistence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openConfigHandlerTestDB(t)
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	svc := settings.NewService(db)
	runtime := &settingsCanceledPersistenceRuntime{}
	handler := NewSettingsHandler(db, svc).WithBackupAssetTransitioner(runtime)
	router := gin.New()
	router.PUT("/settings", handler.BatchUpdate)

	request := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{
  "backup_assets.content_preview_ttl":"3m",
  "backup_assets.search_page_size_max":"250"
}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !runtime.called || runtime.bundle.Content.PreviewTTL != 3*time.Minute || runtime.bundle.Search.PageSizeMax != 250 {
		t.Fatalf("PUT prospective Content/Search called=%t preview_ttl=%s page_size=%d", runtime.called, runtime.bundle.Content.PreviewTTL, runtime.bundle.Search.PageSizeMax)
	}
	if runtime.current["backup_assets.content_preview_ttl"] != "2m" ||
		runtime.effective["backup_assets.content_preview_ttl"] != "3m" ||
		runtime.effective["backup_assets.search_page_size_max"] != "250" {
		t.Fatalf("PUT current/effective preview=%q/%q page_size=%q", runtime.current["backup_assets.content_preview_ttl"], runtime.effective["backup_assets.content_preview_ttl"], runtime.effective["backup_assets.search_page_size_max"])
	}
	if svc.GetEffective("backup_assets.content_preview_ttl") != "3m" ||
		svc.GetEffective("backup_assets.search_page_size_max") != "250" {
		t.Fatal("PUT did not persist prospective Content/Search values")
	}
}

func TestSettingsMixedPUTPostPersistFailureRestoresEveryExactOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openConfigHandlerTestDB(t)
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	priorUpdatedAt := time.Date(2026, 8, 24, 9, 30, 0, 0, time.UTC)
	for _, row := range []model.SystemSetting{
		{Key: "backup_assets.export.worker_concurrency", Value: "2", UpdatedAt: priorUpdatedAt},
		{Key: "login.captcha_enabled", Value: "false", UpdatedAt: priorUpdatedAt},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	svc := settings.NewService(db)
	_ = svc.GetEffective("backup_assets.export.worker_concurrency")
	_ = svc.GetEffective("login.captcha_enabled")
	_ = svc.GetEffective("login.second_captcha_enabled")
	runtimeFailure := errors.New("FAKE_MIXED_SETTINGS_POST_PERSIST_RUNTIME_FAILURE_FOR_TEST_ONLY")
	runtime := &settingsCanceledPersistenceRuntime{failAfterPersist: runtimeFailure}
	handler := NewSettingsHandler(db, svc).WithBackupAssetTransitioner(runtime)
	router := gin.New()
	router.PUT("/settings", handler.BatchUpdate)

	request := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{
  "backup_assets.export.worker_concurrency":"3",
  "login.captcha_enabled":"true",
  "login.second_captcha_enabled":"true"
}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), runtimeFailure.Error()) {
		t.Fatalf("mixed PUT leaked runtime failure: %s", response.Body.String())
	}

	for _, want := range []struct {
		key   string
		value string
	}{
		{key: "login.captcha_enabled", value: "false"},
		{key: "backup_assets.export.worker_concurrency", value: "2"},
	} {
		var row model.SystemSetting
		if err := db.Where("key = ?", want.key).First(&row).Error; err != nil {
			t.Fatalf("load restored %s: %v", want.key, err)
		}
		if row.Value != want.value || !row.UpdatedAt.Equal(priorUpdatedAt) {
			t.Fatalf("restored %s value=%q updated_at=%s, want value=%q updated_at=%s",
				want.key, row.Value, row.UpdatedAt, want.value, priorUpdatedAt)
		}
	}
	var absentCount int64
	if err := db.Model(&model.SystemSetting{}).Where("key = ?", "login.second_captcha_enabled").Count(&absentCount).Error; err != nil {
		t.Fatal(err)
	}
	if absentCount != 0 {
		t.Fatalf("mixed PUT rollback retained previously absent override count=%d", absentCount)
	}
	if svc.GetEffective("backup_assets.export.worker_concurrency") != "2" ||
		svc.GetEffective("login.captcha_enabled") != "false" ||
		svc.GetEffective("login.second_captcha_enabled") != "false" {
		t.Fatal("mixed PUT rollback left stale effective values in settings cache")
	}
}

func TestSettingsDELETEPersistenceHonorsRuntimeOperationCancellation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	svc := settings.NewService(db)
	const key = "backup_assets.content_preview_ttl"
	if err := svc.Update(key, "3m"); err != nil {
		t.Fatal(err)
	}
	runtime := &settingsCanceledPersistenceRuntime{cancelPersist: true}
	handler := NewSettingsHandler(db, svc).WithBackupAssetTransitioner(runtime)
	router := gin.New()
	router.DELETE("/settings/:key", handler.Delete)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/settings/"+key, nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s, want canceled runtime delete to return generic 500", response.Code, response.Body.String())
	}
	if !runtime.called || runtime.bundle.Content.PreviewTTL != 2*time.Minute || runtime.bundle.Search.PageSizeMax <= 0 {
		t.Fatalf("DELETE prospective Content/Search called=%t preview_ttl=%s page_size=%d", runtime.called, runtime.bundle.Content.PreviewTTL, runtime.bundle.Search.PageSizeMax)
	}
	var envelope Response
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode canceled DELETE response: %v", err)
	}
	if envelope.Code != http.StatusInternalServerError || envelope.Message != "服务器内部错误" {
		t.Fatalf("unexpected canceled DELETE envelope: %+v", envelope)
	}
	var row model.SystemSetting
	if err := db.Where("key = ?", key).Take(&row).Error; err != nil {
		t.Fatalf("canceled DELETE removed the exact override: %v", err)
	}
	if row.Value != "3m" || svc.GetEffective(key) != "3m" {
		t.Fatalf("canceled DELETE changed override row=%q effective=%q", row.Value, svc.GetEffective(key))
	}
}

func TestSettingsDELETEEnabledFallbackUsesProspectiveBundleAndRemovesExactOverride(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		fallback    string
		override    string
		wantEnabled bool
	}{
		{name: "fallback true", fallback: "true", override: "false", wantEnabled: true},
		{name: "fallback false", fallback: "false", override: "true", wantEnabled: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("BACKUP_ASSETS_ENABLED", testCase.fallback)
			gin.SetMode(gin.TestMode)
			db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
				t.Fatal(err)
			}
			svc := settings.NewService(db)
			const key = "backup_assets.enabled"
			if err := svc.Update(key, testCase.override); err != nil {
				t.Fatal(err)
			}
			runtime := &settingsCanceledPersistenceRuntime{}
			handler := NewSettingsHandler(db, svc).WithBackupAssetTransitioner(runtime)
			router := gin.New()
			router.DELETE("/settings/:key", handler.Delete)

			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/settings/"+key, nil))

			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if !runtime.called || runtime.bundle.Enabled != testCase.wantEnabled ||
				runtime.bundle.Content.Enabled != testCase.wantEnabled || runtime.bundle.Search.Enabled != testCase.wantEnabled {
				t.Fatalf("fallback prospective enabled global/content/search=%t/%t/%t called=%t", runtime.bundle.Enabled, runtime.bundle.Content.Enabled, runtime.bundle.Search.Enabled, runtime.called)
			}
			var count int64
			if err := db.Model(&model.SystemSetting{}).Where("key = ?", key).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 || svc.GetEffective(key) != testCase.fallback {
				t.Fatalf("DELETE exact absence count=%d effective=%q fallback=%q", count, svc.GetEffective(key), testCase.fallback)
			}
		})
	}
}

func (spy *settingsRuntimeSettingsTransitionSpy) TransitionBackupAssetSettings(
	_ context.Context,
	current map[string]string,
	overlay map[string]string,
	effective map[string]string,
	config backupasset.ExportConfig,
	persist func() error,
) error {
	spy.calls++
	spy.current = current
	spy.overlay = overlay
	spy.effective = effective
	spy.config = config
	if spy.err != nil {
		return spy.err
	}
	if spy.beforePersist != nil {
		if err := spy.beforePersist(); err != nil {
			return err
		}
	}
	_, changesIdempotencyTTL := overlay["backup_assets.idempotency_ttl"]
	_, changesIdempotencyKeyMaxBytes := overlay["backup_assets.idempotency_key_max_bytes"]
	if spy.overlayService != nil && (changesIdempotencyTTL || changesIdempotencyKeyMaxBytes) {
		return spy.overlayService.TransitionIdempotencySettings(config.IdempotencyTTL, config.IdempotencyKeyMaxBytes, persist)
	}
	return persist()
}

type settingsOverlayKeySourceUnused struct{}

func (settingsOverlayKeySourceUnused) Active(context.Context, backupasset.KeyDomain) (backupasset.DomainKeyMaterial, error) {
	return backupasset.DomainKeyMaterial{}, errors.New("FAKE_SETTINGS_OVERLAY_KEY_SOURCE_UNUSED_FOR_TEST_ONLY")
}

type settingsOverlayAuthorizerAllowAll struct{}

func (settingsOverlayAuthorizerAllowAll) AuthorizeAsset(context.Context, *gorm.DB, overlay.Actor, backupasset.AssetRef) error {
	return nil
}

func (settingsOverlayAuthorizerAllowAll) AuthorizePoints(context.Context, overlay.Actor, []string) error {
	return nil
}

func newSettingsTransitionOverlay(t *testing.T, db *gorm.DB, config overlay.Config) (*overlay.Service, time.Time) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_SETTINGS_OVERLAY_IDEMPOTENCY_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	if err := db.AutoMigrate(
		&model.BackupAssetOverlayUsage{}, &model.BackupAssetOverlayIdempotency{}, &model.BackupAssetRecentAccess{},
	); err != nil {
		t.Fatalf("migrate settings Overlay fixture: %v", err)
	}
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	service, err := overlay.NewService(overlay.ServiceDependencies{
		DB: db, Keys: settingsOverlayKeySourceUnused{}, Assets: settingsOverlayAuthorizerAllowAll{}, Points: settingsOverlayAuthorizerAllowAll{},
		Now: func() time.Time { return now }, Config: config,
		FeatureEnabled: func() (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatalf("construct settings Overlay fixture: %v", err)
	}
	return service, now
}

func assertSettingsOverlayReceiptTTL(t *testing.T, db *gorm.DB, ownerID uint, now time.Time, ttl time.Duration) {
	t.Helper()
	var receipt model.BackupAssetOverlayIdempotency
	if err := db.Where("owner_user_id = ? AND action = ?", ownerID, "recent_clear").Take(&receipt).Error; err != nil {
		t.Fatalf("load Overlay idempotency receipt: %v", err)
	}
	if want := now.Add(ttl); !receipt.ExpiresAt.Equal(want) {
		t.Fatalf("Overlay idempotency receipt expiry=%s, want %s", receipt.ExpiresAt, want)
	}
}

func TestSettingsDynamicExportMutationUsesRuntimeSettingsTransition(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	svc := settings.NewService(db)
	spy := &settingsRuntimeSettingsTransitionSpy{}
	handler := NewSettingsHandler(db, svc).WithBackupAssetTransitioner(spy)
	router := gin.New()
	router.PUT("/settings", handler.BatchUpdate)

	request := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"backup_assets.export.worker_concurrency":"3"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if spy.calls != 1 {
		t.Fatalf("runtime settings transition calls=%d, want one", spy.calls)
	}
	if spy.current["backup_assets.export.worker_concurrency"] != "2" {
		t.Fatalf("current worker concurrency=%q, want default 2", spy.current["backup_assets.export.worker_concurrency"])
	}
	if len(spy.overlay) != 1 || spy.overlay["backup_assets.export.worker_concurrency"] != "3" {
		t.Fatalf("overlay=%v", spy.overlay)
	}
	if spy.effective["backup_assets.export.worker_concurrency"] != "3" || spy.config.WorkerConcurrency != 3 {
		t.Fatalf("effective=%q config=%+v", spy.effective["backup_assets.export.worker_concurrency"], spy.config)
	}
}

func TestSettingsDynamicExportResetUsesRuntimeSettingsTransition(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	svc := settings.NewService(db)
	if err := svc.Update("backup_assets.export.worker_concurrency", "3"); err != nil {
		t.Fatal(err)
	}
	spy := &settingsRuntimeSettingsTransitionSpy{}
	handler := NewSettingsHandler(db, svc).WithBackupAssetTransitioner(spy)
	router := gin.New()
	router.DELETE("/settings/:key", handler.Delete)

	request := httptest.NewRequest(http.MethodDelete, "/settings/backup_assets.export.worker_concurrency", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if spy.calls != 1 {
		t.Fatalf("runtime settings transition calls=%d, want one", spy.calls)
	}
	if spy.current["backup_assets.export.worker_concurrency"] != "3" {
		t.Fatalf("current worker concurrency=%q, want override 3", spy.current["backup_assets.export.worker_concurrency"])
	}
	if len(spy.overlay) != 1 || spy.overlay["backup_assets.export.worker_concurrency"] != "2" {
		t.Fatalf("overlay=%v", spy.overlay)
	}
	if spy.effective["backup_assets.export.worker_concurrency"] != "2" || spy.config.WorkerConcurrency != 2 {
		t.Fatalf("effective=%q config=%+v", spy.effective["backup_assets.export.worker_concurrency"], spy.config)
	}
}

func TestSettingsSharedIdempotencyMutationUsesRuntimeSettingsTransition(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	svc := settings.NewService(db)
	overlayService, now := newSettingsTransitionOverlay(t, db, overlay.DefaultConfig())
	spy := &settingsRuntimeSettingsTransitionSpy{overlayService: overlayService}
	handler := NewSettingsHandler(db, svc).WithBackupAssetTransitioner(spy)
	router := gin.New()
	router.PUT("/settings", handler.BatchUpdate)

	request := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"backup_assets.idempotency_ttl":"48h","backup_assets.idempotency_key_max_bytes":"192"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if spy.calls != 1 || spy.config.IdempotencyTTL != 48*time.Hour || spy.config.IdempotencyKeyMaxBytes != 192 {
		t.Fatalf("runtime idempotency transition calls=%d config=%+v", spy.calls, spy.config)
	}
	if _, err := overlayService.ClearRecent(context.Background(), 771, strings.Repeat("a", 160)); err != nil {
		t.Fatalf("updated Overlay key limit rejected handler-approved key: %v", err)
	}
	assertSettingsOverlayReceiptTTL(t, db, 771, now, 48*time.Hour)
}

func TestSettingsSharedIdempotencyResetUsesRuntimeSettingsTransition(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	svc := settings.NewService(db)
	if err := svc.UpdateMany(map[string]string{
		"backup_assets.idempotency_ttl":           "48h",
		"backup_assets.idempotency_key_max_bytes": "192",
	}); err != nil {
		t.Fatal(err)
	}
	overlayConfig := overlay.DefaultConfig()
	overlayConfig.IdempotencyTTL = 48 * time.Hour
	overlayConfig.IdempotencyKeyMaxBytes = 192
	overlayService, now := newSettingsTransitionOverlay(t, db, overlayConfig)
	spy := &settingsRuntimeSettingsTransitionSpy{overlayService: overlayService}
	handler := NewSettingsHandler(db, svc).WithBackupAssetTransitioner(spy)
	router := gin.New()
	router.DELETE("/settings/:key", handler.Delete)

	for _, testCase := range []struct {
		key       string
		wantTTL   time.Duration
		wantMax   int
		wantCalls int
	}{
		{key: "backup_assets.idempotency_key_max_bytes", wantTTL: 48 * time.Hour, wantMax: 128, wantCalls: 1},
		{key: "backup_assets.idempotency_ttl", wantTTL: 24 * time.Hour, wantMax: 128, wantCalls: 2},
	} {
		request := httptest.NewRequest(http.MethodDelete, "/settings/"+testCase.key, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("reset %s status=%d body=%s", testCase.key, response.Code, response.Body.String())
		}
		if spy.calls != testCase.wantCalls || spy.config.IdempotencyTTL != testCase.wantTTL || spy.config.IdempotencyKeyMaxBytes != testCase.wantMax {
			t.Fatalf("reset %s calls=%d config=%+v", testCase.key, spy.calls, spy.config)
		}
		if testCase.key == "backup_assets.idempotency_key_max_bytes" {
			if _, err := overlayService.ClearRecent(context.Background(), 772, strings.Repeat("b", 160)); !errors.Is(err, overlay.ErrInvalidOverlay) {
				t.Fatalf("reset Overlay key limit error=%v, want invalid Overlay", err)
			}
			if _, err := overlayService.ClearRecent(context.Background(), 773, strings.Repeat("c", 128)); err != nil {
				t.Fatalf("reset Overlay key limit rejected default-bound key: %v", err)
			}
			assertSettingsOverlayReceiptTTL(t, db, 773, now, 48*time.Hour)
			continue
		}
		if _, err := overlayService.ClearRecent(context.Background(), 774, strings.Repeat("d", 128)); err != nil {
			t.Fatalf("reset Overlay ttl rejected default-bound key: %v", err)
		}
		assertSettingsOverlayReceiptTTL(t, db, 774, now, 24*time.Hour)
	}
}

func TestSettingsRootOnlyExportMutationUsesRuntimeContractAndPersists(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	svc := settings.NewService(db)
	spy := &settingsRuntimeSettingsTransitionSpy{}
	handler := NewSettingsHandler(db, svc).WithBackupAssetTransitioner(spy)
	router := gin.New()
	router.PUT("/settings", handler.BatchUpdate)

	root := "/var/lib/xirang-asset-runtime/export-next"
	request := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"backup_assets.export.root":"`+root+`"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if spy.calls != 1 || len(spy.targets) != 0 {
		t.Fatalf("root-only mutation runtime transition calls=%d feature targets=%v", spy.calls, spy.targets)
	}
	if got := svc.GetEffective("backup_assets.export.root"); got != root {
		t.Fatalf("persisted Export root=%q, want %q", got, root)
	}
}

func TestRecoveryAuthorizationReceiptSettingsTransitions(t *testing.T) {
	const (
		replayKey  = "backup_assets.recovery.receipt_replay_ttl"
		cadenceKey = "backup_assets.recovery.receipt_reaper_cadence"
	)
	drainFailure := errors.New("FAKE_RECOVERY_SETTINGS_DRAIN_FAILURE_FOR_TEST_ONLY")

	t.Run("batch update", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared"), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
			t.Fatal(err)
		}
		svc := settings.NewService(db)
		spy := &settingsRuntimeSettingsTransitionSpy{}
		spy.beforePersist = func() error {
			if got := svc.GetEffective(replayKey); got != "20m" {
				t.Fatalf("batch changed value before drain: %q", got)
			}
			return drainFailure
		}
		handler := NewSettingsHandler(db, svc).WithBackupAssetTransitioner(spy)
		router := gin.New()
		router.PUT("/settings", handler.BatchUpdate)

		request := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"backup_assets.recovery.receipt_replay_ttl":"30m"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		rolledBack, snapshotErr := svc.BackupAssetSettingsSnapshot()
		if snapshotErr != nil {
			t.Fatal(snapshotErr)
		}
		if response.Code != http.StatusInternalServerError || rolledBack[replayKey] != "20m" {
			t.Fatalf("failed batch status=%d snapshot=%q body=%s", response.Code, rolledBack[replayKey], response.Body.String())
		}

		spy.beforePersist = nil
		request = httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"backup_assets.recovery.receipt_replay_ttl":"30m"}`))
		request.Header.Set("Content-Type", "application/json")
		response = httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || spy.calls != 2 || svc.GetEffective(replayKey) != "30m" {
			t.Fatalf("successful batch status=%d calls=%d effective=%q body=%s", response.Code, spy.calls, svc.GetEffective(replayKey), response.Body.String())
		}
	})

	t.Run("delete reset", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared"), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
			t.Fatal(err)
		}
		svc := settings.NewService(db)
		if err := svc.Update(replayKey, "30m"); err != nil {
			t.Fatal(err)
		}
		spy := &settingsRuntimeSettingsTransitionSpy{}
		spy.beforePersist = func() error {
			if got := svc.GetEffective(replayKey); got != "30m" {
				t.Fatalf("reset changed snapshot before drain: %q", got)
			}
			return drainFailure
		}
		handler := NewSettingsHandler(db, svc).WithBackupAssetTransitioner(spy)
		router := gin.New()
		router.DELETE("/settings/:key", handler.Delete)

		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/settings/"+replayKey, nil))
		if response.Code != http.StatusInternalServerError || svc.GetEffective(replayKey) != "30m" {
			t.Fatalf("failed reset status=%d effective=%q body=%s", response.Code, svc.GetEffective(replayKey), response.Body.String())
		}
		if strings.Contains(response.Body.String(), "FAKE_RECOVERY_SETTINGS_DRAIN_FAILURE_FOR_TEST_ONLY") {
			t.Fatalf("unexpected delete reset leaked drain error: %s", response.Body.String())
		}

		spy.beforePersist = nil
		response = httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/settings/"+replayKey, nil))
		if response.Code != http.StatusOK || spy.calls != 2 || svc.GetEffective(replayKey) != "20m" {
			t.Fatalf("successful reset status=%d calls=%d effective=%q body=%s", response.Code, spy.calls, svc.GetEffective(replayKey), response.Body.String())
		}
	})

	t.Run("config import", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		db := openConfigHandlerTestDB(t)
		if err := db.AutoMigrate(&model.SystemSetting{}, &model.CredentialAuditEvent{}); err != nil {
			t.Fatal(err)
		}
		svc := settings.NewService(db)
		spy := &settingsRuntimeSettingsTransitionSpy{}
		spy.beforePersist = func() error {
			if got := svc.GetEffective(cadenceKey); got != "1m" {
				t.Fatalf("import changed snapshot before drain: %q", got)
			}
			return drainFailure
		}
		handler := NewConfigHandler(db, svc).WithBackupAssetTransitioner(spy)
		router := gin.New()
		router.POST("/config/import", handler.Import)
		body := `{"system_settings":[{"key":"backup_assets.recovery.receipt_reaper_cadence","value":"2m"}]}`

		request := httptest.NewRequest(http.MethodPost, "/config/import", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusInternalServerError || svc.GetEffective(cadenceKey) != "1m" {
			t.Fatalf("failed import status=%d effective=%q body=%s", response.Code, svc.GetEffective(cadenceKey), response.Body.String())
		}

		spy.beforePersist = nil
		request = httptest.NewRequest(http.MethodPost, "/config/import", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response = httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || spy.calls != 2 || svc.GetEffective(cadenceKey) != "2m" {
			t.Fatalf("successful import status=%d calls=%d effective=%q body=%s", response.Code, spy.calls, svc.GetEffective(cadenceKey), response.Body.String())
		}
	})
}

func setupSettingsTransitionHandler(t *testing.T) (*gorm.DB, *settings.Service, *SettingsHandler, *settingsTransitionSpy, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	svc := settings.NewService(db)
	spy := &settingsTransitionSpy{}
	handler := NewSettingsHandler(db, svc).WithBackupAssetTransitioner(spy)
	router := gin.New()
	router.PUT("/settings", handler.BatchUpdate)
	router.DELETE("/settings/:key", handler.Delete)
	return db, svc, handler, spy, router
}

func TestSettingsNonFoundationMutationsBypassBackupAssetTransitioner(t *testing.T) {
	_, svc, _, spy, router := setupSettingsTransitionHandler(t)
	spy.err = errors.New("backup asset runtime is compensation-fenced")

	request := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"login.captcha_enabled":"true"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", response.Code, response.Body.String())
	}
	if len(spy.targets) != 0 || svc.GetEffective("login.captcha_enabled") != "true" {
		t.Fatalf("PUT targets=%v effective=%q", spy.targets, svc.GetEffective("login.captcha_enabled"))
	}

	request = httptest.NewRequest(http.MethodDelete, "/settings/login.captcha_enabled", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("DELETE status=%d body=%s", response.Code, response.Body.String())
	}
	if len(spy.targets) != 0 || svc.GetEffective("login.captcha_enabled") != "false" {
		t.Fatalf("DELETE targets=%v effective=%q", spy.targets, svc.GetEffective("login.captcha_enabled"))
	}
}

func TestSettingsEnablementBlockedKeepsBackupAssetsDisabled(t *testing.T) {
	_, svc, _, spy, router := setupSettingsEnablementHandler(t, ga.ReadinessSnapshot{
		Class:             ga.InstallationFresh,
		Status:            ga.ReadinessBlocked,
		InventoryComplete: false,
	})

	request := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"backup_assets.enabled":"true"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assertBackupAssetEnablementBlockedConflict(t, response)
	if len(spy.targets) != 0 || svc.GetEffective("backup_assets.enabled") != "false" {
		t.Fatalf("blocked settings enablement became managed targets=%v effective=%q", spy.targets, svc.GetEffective("backup_assets.enabled"))
	}
}

func TestSettingsEnablementFreshReadyWithoutAckMayEnable(t *testing.T) {
	_, svc, _, spy, router := setupSettingsEnablementHandler(t, ga.ReadinessSnapshot{
		Class:             ga.InstallationFresh,
		Status:            ga.ReadinessReady,
		InventoryComplete: true,
		InventoryDigest:   "fresh-digest",
		ExportRootValid:   true,
		KeyDomainsReady:   true,
	})

	request := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"backup_assets.enabled":"true"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(spy.targets) != 1 || !spy.targets[0] || svc.GetEffective("backup_assets.enabled") != "true" {
		t.Fatalf("fresh ready targets=%v effective=%q", spy.targets, svc.GetEffective("backup_assets.enabled"))
	}
}

func TestSettingsEnablementExistingInstallRequiresAck(t *testing.T) {
	_, svc, _, spy, router := setupSettingsEnablementHandler(t, ga.ReadinessSnapshot{
		Class:             ga.InstallationExisting,
		Status:            ga.ReadinessReady,
		InventoryComplete: true,
		InventoryDigest:   "current-digest",
		ExportRootValid:   true,
		KeyDomainsReady:   true,
	})

	request := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"backup_assets.enabled":"true"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assertBackupAssetEnablementBlockedConflict(t, response)
	if len(spy.targets) != 0 || svc.GetEffective("backup_assets.enabled") != "false" {
		t.Fatalf("existing without ack became managed targets=%v effective=%q", spy.targets, svc.GetEffective("backup_assets.enabled"))
	}
}

func TestSettingsEnablementDeleteRestoreBlockedKeepsBackupAssetsDisabled(t *testing.T) {
	t.Setenv("BACKUP_ASSETS_ENABLED", "true")
	_, svc, _, spy, router := setupSettingsEnablementHandler(t, ga.ReadinessSnapshot{
		Class:             ga.InstallationFresh,
		Status:            ga.ReadinessBlocked,
		InventoryComplete: false,
	})
	if err := svc.Update("backup_assets.enabled", "false"); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodDelete, "/settings/backup_assets.enabled", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assertBackupAssetEnablementBlockedConflict(t, response)
	if len(spy.targets) != 0 || svc.GetEffective("backup_assets.enabled") != "false" {
		t.Fatalf("blocked delete-restore became managed targets=%v effective=%q", spy.targets, svc.GetEffective("backup_assets.enabled"))
	}
}

func setupSettingsEnablementHandler(t *testing.T, snapshot ga.ReadinessSnapshot) (*gorm.DB, *settings.Service, *SettingsHandler, *settingsTransitionSpy, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	svc := settings.NewService(db)
	spy := &settingsTransitionSpy{}
	transitioner := assetruntime.EnablementRuntime(settingsEnablementReadiness{snapshot: snapshot}, spy).WithSettings(svc)
	handler := NewSettingsHandler(db, svc).WithBackupAssetTransitioner(transitioner)
	router := gin.New()
	router.PUT("/settings", handler.BatchUpdate)
	router.DELETE("/settings/:key", handler.Delete)
	return db, svc, handler, spy, router
}

type settingsEnablementReadiness struct {
	snapshot ga.ReadinessSnapshot
}

func (source settingsEnablementReadiness) CurrentReadiness(context.Context) (ga.ReadinessSnapshot, error) {
	return source.snapshot, nil
}

func assertBackupAssetEnablementBlockedConflict(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope Response
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Code != http.StatusConflict || envelope.Message != "就绪检查未完成" {
		t.Fatalf("expected conflict 就绪检查未完成, got %+v", envelope)
	}
}

func TestSettingsEnabledTransitionDrainsBeforePersistingDatabaseValue(t *testing.T) {
	_, svc, _, spy, router := setupSettingsTransitionHandler(t)
	spy.beforePersist = func() error {
		if got := svc.GetEffective("backup_assets.enabled"); got != "false" {
			t.Fatalf("enabled DB value changed before transition drain: %q", got)
		}
		return nil
	}

	request := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"backup_assets.enabled":"true"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(spy.targets) != 1 || !spy.targets[0] || svc.GetEffective("backup_assets.enabled") != "true" {
		t.Fatalf("transition targets=%v effective=%q", spy.targets, svc.GetEffective("backup_assets.enabled"))
	}
}

func TestSettingsEnabledDeleteTransitionsToFallbackBeforeRemovingOverride(t *testing.T) {
	_, svc, _, spy, router := setupSettingsTransitionHandler(t)
	if err := svc.Update("backup_assets.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	spy.beforePersist = func() error {
		if got := svc.GetEffective("backup_assets.enabled"); got != "true" {
			t.Fatalf("enabled DB override changed before transition drain: %q", got)
		}
		return nil
	}

	request := httptest.NewRequest(http.MethodDelete, "/settings/backup_assets.enabled", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(spy.targets) != 1 || spy.targets[0] || svc.GetEffective("backup_assets.enabled") != "false" {
		t.Fatalf("transition targets=%v effective=%q", spy.targets, svc.GetEffective("backup_assets.enabled"))
	}
}

func TestSettingsDeleteFoundationOverrideRejectsInvalidFallbackWithoutMutation(t *testing.T) {
	t.Setenv("BACKUP_ASSETS_LEASE_DURATION", "30s")
	db, svc, _, _, router := setupSettingsTransitionHandler(t)
	if err := svc.Update("backup_assets.lease_duration", "5m"); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodDelete, "/settings/backup_assets.lease_duration", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var row model.SystemSetting
	if err := db.Where("key = ?", "backup_assets.lease_duration").First(&row).Error; err != nil {
		t.Fatalf("invalid fallback removed the override: %v", err)
	}
	if svc.GetEffective("backup_assets.lease_duration") != "5m" {
		t.Fatalf("invalid fallback changed effective value to %q", svc.GetEffective("backup_assets.lease_duration"))
	}
}

type threeEntrypointEnablementHarness struct {
	db           *gorm.DB
	svc          *settings.Service
	runtime      *assetruntime.Runtime
	router       *gin.Engine
	repositoryID string
	now          time.Time
}

func setupThreeEntrypointEnablementHarness(t *testing.T, snapshot ga.ReadinessSnapshot) *threeEntrypointEnablementHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 8, 24, 18, 0, 0, 0, time.UTC)
	db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.SystemSetting{},
		&model.CredentialAuditEvent{},
		&model.Node{},
		&model.Policy{},
		&model.Task{},
		&model.SSHKey{},
		&model.BackupRepository{},
		&model.RecoveryPoint{},
		&model.CatalogGeneration{},
		&model.CatalogEntry{},
		&model.WrappedDomainKey{},
		&model.BackupAssetInstallation{},
		&model.BackupAssetInventoryRun{},
		&model.BackupAssetRepositoryConflict{},
		&model.BackupAssetManagedHistoryLatch{},
	); err != nil {
		t.Fatalf("migrate three-entrypoint harness: %v", err)
	}
	initialStatus := snapshot.Status
	switch initialStatus {
	case "":
		initialStatus = ga.ReadinessBlocked
	case ga.ReadinessReady:
		// Seed unknown so allowed enablement must exercise MaterializeReadiness to persist ready.
		initialStatus = ga.ReadinessUnknown
	}
	initialClass := snapshot.Class
	if initialClass == "" {
		initialClass = ga.InstallationFresh
	}
	if err := db.Create(&model.BackupAssetInstallation{
		ID:              strings.Repeat("b", 32),
		Slot:            1,
		Class:           string(initialClass),
		Readiness:       string(initialStatus),
		InventoryDigest: snapshot.InventoryDigest,
		CreatedAt:       now,
		UpdatedAt:       now,
	}).Error; err != nil {
		t.Fatalf("seed installation: %v", err)
	}
	repoID := strings.Repeat("a", 32)
	if err := db.Create(&model.BackupRepository{
		ID:                 repoID,
		ProviderKind:       string(backupasset.ProviderRsync),
		DisplayName:        "test-repo",
		VersionMode:        "native_snapshot",
		Status:             string(backupasset.RepositoryOnline),
		CapabilityRevision: 1,
		ImmutabilityLevel:  "none",
		CreatedAt:          now,
		UpdatedAt:          now,
	}).Error; err != nil {
		t.Fatalf("seed repository: %v", err)
	}
	svc := settings.NewService(db)
	inventory := ga.NewInventoryService(ga.InventoryDependencies{DB: db, Now: func() time.Time { return now }})
	spy := &settingsTransitionSpy{}
	runtime := buildThreeEntrypointRuntime(t, db, svc, inventory, snapshot, spy, now)
	router := buildThreeEntrypointRouter(db, svc, runtime)
	return &threeEntrypointEnablementHarness{
		db:           db,
		svc:          svc,
		runtime:      runtime,
		router:       router,
		repositoryID: repoID,
		now:          now,
	}
}

func buildThreeEntrypointRuntime(
	t *testing.T,
	db *gorm.DB,
	svc *settings.Service,
	inventory *ga.InventoryService,
	snapshot ga.ReadinessSnapshot,
	transitioner publication.FeatureTransitioner,
	now time.Time,
) *assetruntime.Runtime {
	t.Helper()
	runtime := assetruntime.EnablementRuntime(settingsEnablementReadiness{snapshot: snapshot}, transitioner).
		WithSettings(svc).
		WithInventory(inventory)
	ownership, err := catalog.NewOwnership(db)
	if err != nil {
		t.Fatal(err)
	}
	keyring := backupasset.NewKeyring(db, func() time.Time { return now })
	catalogService, err := catalog.NewService(catalog.ServiceDependencies{
		DB:                db,
		Ownership:         ownership,
		Cursor:            catalog.NewCursorCodec(keyring, func() time.Time { return now }, 15*time.Minute),
		Now:               func() time.Time { return now },
		ReconcileInterval: 5 * time.Minute,
		FeatureEnabled:    func() (bool, error) { return runtime.FeatureLive() },
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime.WithCatalogService(catalogService)
}

func buildThreeEntrypointRouter(db *gorm.DB, svc *settings.Service, runtime *assetruntime.Runtime) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(1))
		c.Set(middleware.CtxUsername, "admin")
		c.Set(middleware.CtxRole, "admin")
		c.Next()
	})
	settingsHandler := NewSettingsHandler(db, svc).WithBackupAssetTransitioner(runtime)
	configHandler := NewConfigHandler(db, svc).WithBackupAssetTransitioner(runtime)
	assetHandler := NewBackupAssetHandler(runtime.CatalogService(), nil)
	router.PUT("/settings", settingsHandler.BatchUpdate)
	router.DELETE("/settings/:key", settingsHandler.Delete)
	router.POST("/config/import", configHandler.Import)
	router.GET("/backup-repositories/:id/recovery-points", assetHandler.ListRecoveryPoints)
	return router
}

func (h *threeEntrypointEnablementHarness) invokeEnableEntrypoint(t *testing.T, entrypoint string) *httptest.ResponseRecorder {
	t.Helper()
	var request *http.Request
	switch entrypoint {
	case "put_settings":
		request = httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"backup_assets.enabled":"true"}`))
		request.Header.Set("Content-Type", "application/json")
	case "delete_override":
		request = httptest.NewRequest(http.MethodDelete, "/settings/backup_assets.enabled", nil)
	case "config_import":
		request = httptest.NewRequest(http.MethodPost, "/config/import", strings.NewReader(`{"nodes":[{"name":"imported-node","host":"10.0.0.10","port":22,"username":"root","auth_type":"password"}],"system_settings":[{"key":"backup_assets.enabled","value":"true"}]}`))
		request.Header.Set("Content-Type", "application/json")
	default:
		t.Fatalf("unknown entrypoint %q", entrypoint)
	}
	response := httptest.NewRecorder()
	h.router.ServeHTTP(response, request)
	return response
}

func (h *threeEntrypointEnablementHarness) queryProtectedCatalogStatus() int {
	request := httptest.NewRequest(http.MethodGet, "/backup-repositories/"+h.repositoryID+"/recovery-points", nil)
	response := httptest.NewRecorder()
	h.router.ServeHTTP(response, request)
	return response.Code
}

func TestBackupAssetEnablementThreeEntrypointsEnforceSameGateAndProtectedRoute(t *testing.T) {
	entrypoints := []string{"put_settings", "delete_override", "config_import"}
	cases := []struct {
		name      string
		snapshot  ga.ReadinessSnapshot
		wantAllow bool
	}{
		{
			name: "blocked",
			snapshot: ga.ReadinessSnapshot{
				Class:             ga.InstallationFresh,
				Status:            ga.ReadinessBlocked,
				InventoryComplete: false,
			},
			wantAllow: false,
		},
		{
			name: "existing_unacked",
			snapshot: ga.ReadinessSnapshot{
				Class:             ga.InstallationExisting,
				Status:            ga.ReadinessReady,
				InventoryComplete: true,
				InventoryDigest:   "current-digest",
				ExportRootValid:   true,
				KeyDomainsReady:   true,
			},
			wantAllow: false,
		},
		{
			name: "existing_stale_ack",
			snapshot: ga.ReadinessSnapshot{
				Class:              ga.InstallationExisting,
				Status:             ga.ReadinessAcknowledged,
				InventoryComplete:  true,
				InventoryDigest:    "current-digest",
				AcknowledgedDigest: "old-digest",
				ExportRootValid:    true,
				KeyDomainsReady:    true,
			},
			wantAllow: false,
		},
		{
			name: "fresh_ready_without_ack",
			snapshot: ga.ReadinessSnapshot{
				Class:             ga.InstallationFresh,
				Status:            ga.ReadinessReady,
				InventoryComplete: true,
				InventoryDigest:   "fresh-digest",
				ExportRootValid:   true,
				KeyDomainsReady:   true,
			},
			wantAllow: true,
		},
		{
			name: "existing_acked_current_digest",
			snapshot: ga.ReadinessSnapshot{
				Class:              ga.InstallationExisting,
				Status:             ga.ReadinessAcknowledged,
				InventoryComplete:  true,
				InventoryDigest:    "current-digest",
				AcknowledgedDigest: "current-digest",
				ExportRootValid:    true,
				KeyDomainsReady:    true,
			},
			wantAllow: true,
		},
	}

	for _, entrypoint := range entrypoints {
		for _, tc := range cases {
			t.Run(entrypoint+"/"+tc.name, func(t *testing.T) {
				if entrypoint == "delete_override" {
					t.Setenv("BACKUP_ASSETS_ENABLED", "true")
				}
				harness := setupThreeEntrypointEnablementHarness(t, tc.snapshot)
				if entrypoint == "delete_override" {
					if err := harness.svc.Update("backup_assets.enabled", "false"); err != nil {
						t.Fatal(err)
					}
				}

				if live, err := harness.runtime.FeatureLive(); err != nil || live {
					t.Fatalf("pre-enable FeatureLive=%t err=%v, want false", live, err)
				}
				if status := harness.queryProtectedCatalogStatus(); status != http.StatusServiceUnavailable {
					t.Fatalf("pre-enable protected Catalog status=%d, want 503", status)
				}

				response := harness.invokeEnableEntrypoint(t, entrypoint)
				var installation model.BackupAssetInstallation
				if err := harness.db.Where("slot = ?", 1).Take(&installation).Error; err != nil {
					t.Fatal(err)
				}

				if !tc.wantAllow {
					assertBackupAssetEnablementBlockedConflict(t, response)
					if got := harness.svc.GetEffective("backup_assets.enabled"); got != "false" {
						t.Fatalf("rejected enablement effective=%q, want false", got)
					}
					if entrypoint == "delete_override" {
						var row model.SystemSetting
						if err := harness.db.Where("key = ?", "backup_assets.enabled").Take(&row).Error; err != nil || row.Value != "false" {
							t.Fatalf("rejected delete_override did not retain false override row=%+v err=%v", row, err)
						}
					}
					if entrypoint == "config_import" {
						var nodeCount int64
						if err := harness.db.Model(&model.Node{}).Count(&nodeCount).Error; err != nil || nodeCount != 0 {
							t.Fatalf("rejected config_import persisted nodes=%d err=%v", nodeCount, err)
						}
					}
					if installation.EnablementSucceededAt != nil {
						t.Fatalf("rejected enablement stamped EnablementSucceededAt=%v", installation.EnablementSucceededAt)
					}
					if live, err := harness.runtime.FeatureLive(); err != nil || live {
						t.Fatalf("rejected enablement FeatureLive=%t err=%v, want false", live, err)
					}
					if status := harness.queryProtectedCatalogStatus(); status != http.StatusServiceUnavailable {
						t.Fatalf("rejected enablement protected Catalog status=%d, want 503", status)
					}
					return
				}

				if response.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
				}
				if got := harness.svc.GetEffective("backup_assets.enabled"); got != "true" {
					t.Fatalf("allowed enablement effective=%q, want true", got)
				}
				if entrypoint == "delete_override" {
					var count int64
					if err := harness.db.Model(&model.SystemSetting{}).Where("key = ?", "backup_assets.enabled").Count(&count).Error; err != nil || count != 0 {
						t.Fatalf("allowed delete_override left override row count=%d err=%v", count, err)
					}
				}
				if installation.EnablementSucceededAt == nil {
					t.Fatal("allowed enablement did not stamp EnablementSucceededAt")
				}
				if installation.Readiness != string(ga.ReadinessReady) && installation.Readiness != string(ga.ReadinessAcknowledged) {
					t.Fatalf("allowed enablement did not materialize stored readiness=%q", installation.Readiness)
				}
				if live, err := harness.runtime.FeatureLive(); err != nil || !live {
					t.Fatalf("allowed enablement FeatureLive=%t err=%v, want true", live, err)
				}
				if status := harness.queryProtectedCatalogStatus(); status != http.StatusOK {
					t.Fatalf("allowed enablement protected Catalog status=%d, want 200", status)
				}
			})
		}
	}
}

func TestBackupAssetEnablementThreeEntrypointsPostPersistFailureCompensationSuccessVsFailure(t *testing.T) {
	readySnapshot := ga.ReadinessSnapshot{
		Class:             ga.InstallationFresh,
		Status:            ga.ReadinessReady,
		InventoryComplete: true,
		InventoryDigest:   "fresh-digest",
		ExportRootValid:   true,
		KeyDomainsReady:   true,
	}
	entrypoints := []string{"put_settings", "delete_override", "config_import"}

	for _, entrypoint := range entrypoints {
		t.Run(entrypoint+"/compensation_succeeds_restores_prior_state_and_allows_online_retry", func(t *testing.T) {
			if entrypoint == "delete_override" {
				t.Setenv("BACKUP_ASSETS_ENABLED", "true")
			}
			harness := setupThreeEntrypointEnablementHarness(t, readySnapshot)
			if entrypoint == "delete_override" {
				if err := harness.svc.Update("backup_assets.enabled", "false"); err != nil {
					t.Fatal(err)
				}
			}

			stampErr := errors.New("FAKE_POST_PERSIST_STAMP_FAILURE_FOR_TEST_ONLY")
			failStamp := &atomic.Bool{}
			failStamp.Store(true)
			callbackName := "fail_stamp_once_" + entrypoint
			if err := harness.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement != nil && tx.Statement.Table == "backup_asset_installations" {
					if _, setsStamp := tx.Statement.Dest.(map[string]any)["enablement_succeeded_at"]; setsStamp && failStamp.CompareAndSwap(true, false) {
						_ = tx.AddError(stampErr)
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = harness.db.Callback().Update().Remove(callbackName) })

			response := harness.invokeEnableEntrypoint(t, entrypoint)
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s, want 500", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), stampErr.Error()) {
				t.Fatalf("response leaked internal error: %s", response.Body.String())
			}
			if got := harness.svc.GetEffective("backup_assets.enabled"); got != "false" {
				t.Fatalf("compensated effective backup_assets.enabled=%q, want false", got)
			}
			if entrypoint == "delete_override" {
				var row model.SystemSetting
				if err := harness.db.Where("key = ?", "backup_assets.enabled").Take(&row).Error; err != nil || row.Value != "false" {
					t.Fatalf("compensated delete_override did not restore false override row=%+v err=%v", row, err)
				}
			}
			if entrypoint == "config_import" {
				var nodeCount int64
				if err := harness.db.Model(&model.Node{}).Count(&nodeCount).Error; err != nil || nodeCount != 0 {
					t.Fatalf("compensated config_import did not rollback imported node count=%d err=%v", nodeCount, err)
				}
			}
			var installation model.BackupAssetInstallation
			if err := harness.db.Where("slot = ?", 1).Take(&installation).Error; err != nil {
				t.Fatal(err)
			}
			if installation.EnablementSucceededAt != nil {
				t.Fatalf("compensated failure left false EnablementSucceededAt=%v", installation.EnablementSucceededAt)
			}
			if live, err := harness.runtime.FeatureLive(); err != nil || live {
				t.Fatalf("compensated failure FeatureLive=%t err=%v, want false", live, err)
			}
			if status := harness.queryProtectedCatalogStatus(); status != http.StatusServiceUnavailable {
				t.Fatalf("compensated failure protected Catalog status=%d, want 503", status)
			}

			// Because compensation succeeded, the runtime is not fenced and can succeed online without restart.
			retryResponse := harness.invokeEnableEntrypoint(t, entrypoint)
			if retryResponse.Code != http.StatusOK {
				t.Fatalf("online retry status=%d body=%s, want 200", retryResponse.Code, retryResponse.Body.String())
			}
			if got := harness.svc.GetEffective("backup_assets.enabled"); got != "true" {
				t.Fatalf("online retry effective=%q, want true", got)
			}
			if live, err := harness.runtime.FeatureLive(); err != nil || !live {
				t.Fatalf("online retry FeatureLive=%t err=%v, want true", live, err)
			}
			if status := harness.queryProtectedCatalogStatus(); status != http.StatusOK {
				t.Fatalf("online retry protected Catalog status=%d, want 200", status)
			}
		})

		t.Run(entrypoint+"/compensation_fails_fences_runtime_closed_until_restart", func(t *testing.T) {
			if entrypoint == "delete_override" {
				t.Setenv("BACKUP_ASSETS_ENABLED", "true")
			}
			harness := setupThreeEntrypointEnablementHarness(t, readySnapshot)
			if entrypoint == "delete_override" {
				if err := harness.svc.Update("backup_assets.enabled", "false"); err != nil {
					t.Fatal(err)
				}
			}

			stampErr := errors.New("FAKE_FENCE_PRIMARY_STAMP_FAILURE_FOR_TEST_ONLY")
			restoreErr := errors.New("FAKE_FENCE_RESTORE_COMPENSATION_FAILURE_FOR_TEST_ONLY")
			failStamp := &atomic.Bool{}
			failRestore := &atomic.Bool{}
			failStamp.Store(true)
			updateCallback := "fail_stamp_and_restore_update_" + entrypoint
			createCallback := "fail_restore_create_" + entrypoint
			deleteCallback := "fail_restore_delete_" + entrypoint
			rawCallback := "fail_restore_raw_" + entrypoint
			if err := harness.db.Callback().Update().Before("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
				if tx.Statement == nil {
					return
				}
				if tx.Statement.Table == "backup_asset_installations" {
					if _, setsStamp := tx.Statement.Dest.(map[string]any)["enablement_succeeded_at"]; setsStamp && failStamp.CompareAndSwap(true, false) {
						failRestore.Store(true)
						_ = tx.AddError(stampErr)
					}
				}
				if failRestore.Load() && tx.Statement.Table == "system_settings" {
					_ = tx.AddError(restoreErr)
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := harness.db.Callback().Create().Before("gorm:create").Register(createCallback, func(tx *gorm.DB) {
				if failRestore.Load() && tx.Statement != nil && tx.Statement.Table == "system_settings" {
					_ = tx.AddError(restoreErr)
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := harness.db.Callback().Delete().Before("gorm:delete").Register(deleteCallback, func(tx *gorm.DB) {
				if failRestore.Load() && tx.Statement != nil && tx.Statement.Table == "system_settings" {
					_ = tx.AddError(restoreErr)
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := harness.db.Callback().Raw().Before("gorm:raw").Register(rawCallback, func(tx *gorm.DB) {
				if failRestore.Load() && tx.Statement != nil && strings.Contains(tx.Statement.SQL.String(), "system_settings") {
					_ = tx.AddError(restoreErr)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = harness.db.Callback().Update().Remove(updateCallback)
				_ = harness.db.Callback().Create().Remove(createCallback)
				_ = harness.db.Callback().Delete().Remove(deleteCallback)
				_ = harness.db.Callback().Raw().Remove(rawCallback)
			})

			response := harness.invokeEnableEntrypoint(t, entrypoint)
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s, want 500", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), stampErr.Error()) || strings.Contains(response.Body.String(), restoreErr.Error()) {
				t.Fatalf("response leaked internal error: %s", response.Body.String())
			}

			// Persisted setting remained true because DB compensation failed, yet FeatureLive and protected routes MUST stay closed.
			if got := harness.svc.GetEffective("backup_assets.enabled"); got != "true" {
				t.Fatalf("unrestored effective backup_assets.enabled=%q, want true after restore failure", got)
			}
			if live, err := harness.runtime.FeatureLive(); err != nil || live {
				t.Fatalf("fenced runtime FeatureLive=%t err=%v, want false", live, err)
			}
			if status := harness.queryProtectedCatalogStatus(); status != http.StatusServiceUnavailable {
				t.Fatalf("fenced runtime protected Catalog status=%d, want 503", status)
			}

			// Clear all injected DB errors: online retry on the same fenced runtime MUST still fail closed until restart.
			failRestore.Store(false)
			retryResponse := harness.invokeEnableEntrypoint(t, entrypoint)
			if retryResponse.Code != http.StatusInternalServerError {
				t.Fatalf("online retry on fenced runtime status=%d body=%s, want 500", retryResponse.Code, retryResponse.Body.String())
			}
			if live, err := harness.runtime.FeatureLive(); err != nil || live {
				t.Fatalf("after online retry fenced runtime FeatureLive=%t err=%v, want false", live, err)
			}
			if status := harness.queryProtectedCatalogStatus(); status != http.StatusServiceUnavailable {
				t.Fatalf("after online retry protected Catalog status=%d, want 503", status)
			}

			// Constructing a new Runtime over the same DB (simulating process restart) clears the sticky fence and recovers.
			inventory := ga.NewInventoryService(ga.InventoryDependencies{DB: harness.db, Now: func() time.Time { return harness.now }})
			restartedRuntime := buildThreeEntrypointRuntime(
				t, harness.db, harness.svc, inventory, readySnapshot, &settingsTransitionSpy{}, harness.now,
			)
			harness.runtime = restartedRuntime
			harness.router = buildThreeEntrypointRouter(harness.db, harness.svc, restartedRuntime)

			if live, err := harness.runtime.FeatureLive(); err != nil || !live {
				t.Fatalf("restarted runtime FeatureLive=%t err=%v, want true", live, err)
			}
			if status := harness.queryProtectedCatalogStatus(); status != http.StatusOK {
				t.Fatalf("restarted runtime protected Catalog status=%d, want 200", status)
			}
		})
	}
}
