package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/ga"
	"xirang/backend/internal/backupasset/overlay"
	"xirang/backend/internal/backupasset/publication"
	assetruntime "xirang/backend/internal/backupasset/runtime"
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
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
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
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
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
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
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
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
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

func TestSettingsRootOnlyExportMutationPersistsWithoutRuntimeSettingsTransition(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
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
	if spy.calls != 0 || len(spy.targets) != 0 {
		t.Fatalf("root-only mutation invoked runtime transition calls=%d feature targets=%v", spy.calls, spy.targets)
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
		db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
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
		db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
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
		if response.Code != http.StatusBadRequest || svc.GetEffective(replayKey) != "30m" {
			t.Fatalf("failed reset status=%d effective=%q body=%s", response.Code, svc.GetEffective(replayKey), response.Body.String())
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
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
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
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	svc := settings.NewService(db)
	spy := &settingsTransitionSpy{}
	transitioner := assetruntime.EnablementRuntime(settingsEnablementReadiness{snapshot: snapshot}, spy)
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
