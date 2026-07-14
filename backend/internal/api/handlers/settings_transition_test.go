package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
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
	if len(spy.targets) != 1 || !spy.targets[0] || svc.GetEffective("backup_assets.enabled") != "false" {
		t.Fatalf("failed transition targets=%v effective=%q", spy.targets, svc.GetEffective("backup_assets.enabled"))
	}
}

func (*settingsTransitionSpy) PrepareApplicationDowngrade(context.Context, func() error) error {
	return nil
}
func (*settingsTransitionSpy) PrepareSchemaDown(context.Context, func() error) error { return nil }

var _ publication.FeatureTransitioner = (*settingsTransitionSpy)(nil)

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
