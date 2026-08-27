package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	applogger "xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
)

func TestSettingsPrivateNetworkContentTransportTransitionRollbackAndValueFreeAudit(t *testing.T) {
	const (
		key    = "backup_assets.content_allow_insecure_private_network"
		envVar = "BACKUP_ASSETS_CONTENT_ALLOW_INSECURE_PRIVATE_NETWORK"
	)
	t.Setenv(envVar, "false")
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
	router.DELETE("/settings/:key", handler.Delete)

	var audit bytes.Buffer
	previousLogger := applogger.Log
	applogger.Log = zerolog.New(&audit)
	t.Cleanup(func() { applogger.Log = previousLogger })

	put := func(value string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"`+key+`":"`+value+`"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}

	response := put("true")
	if response.Code != http.StatusOK || !runtime.bundle.Content.AllowInsecurePrivateNetwork {
		t.Fatalf("enable status=%d prospective=%t body=%s", response.Code, runtime.bundle.Content.AllowInsecurePrivateNetwork, response.Body.String())
	}
	var row model.SystemSetting
	if err := db.Where("key = ?", key).Take(&row).Error; err != nil || row.Value != "true" {
		t.Fatalf("enabled DB override=%+v err=%v", row, err)
	}
	var event map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(audit.Bytes()), &event); err != nil {
		t.Fatalf("decode audit event: %v log=%s", err, audit.String())
	}
	if event["action"] != "settings_update" || event["key"] != key || event["source"] != "db" {
		t.Fatalf("audit event=%v", event)
	}
	for _, forbidden := range []string{"value", "old_value", "new_value"} {
		if _, exists := event[forbidden]; exists {
			t.Fatalf("audit persisted setting value field %q: %v", forbidden, event)
		}
	}

	runtime.failAfterPersist = errors.New("FAKE_PRIVATE_NETWORK_CONTENT_APPLY_FAILURE_FOR_TEST_ONLY")
	response = put("false")
	if response.Code != http.StatusInternalServerError || runtime.bundle.Content.AllowInsecurePrivateNetwork {
		t.Fatalf("failed disable status=%d prospective=%t body=%s", response.Code, runtime.bundle.Content.AllowInsecurePrivateNetwork, response.Body.String())
	}
	row = model.SystemSetting{}
	if err := db.Where("key = ?", key).Take(&row).Error; err != nil || row.Value != "true" || svc.GetEffective(key) != "true" {
		t.Fatalf("rollback DB override=%+v effective=%q err=%v", row, svc.GetEffective(key), err)
	}

	runtime.failAfterPersist = nil
	response = put("false")
	if response.Code != http.StatusOK || runtime.bundle.Content.AllowInsecurePrivateNetwork || svc.GetEffective(key) != "false" {
		t.Fatalf("disable status=%d prospective=%t effective=%q body=%s", response.Code, runtime.bundle.Content.AllowInsecurePrivateNetwork, svc.GetEffective(key), response.Body.String())
	}

	t.Setenv(envVar, "true")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/settings/"+key, nil))
	if response.Code != http.StatusOK || !runtime.bundle.Content.AllowInsecurePrivateNetwork || svc.GetEffective(key) != "true" {
		t.Fatalf("reset status=%d prospective=%t effective=%q body=%s", response.Code, runtime.bundle.Content.AllowInsecurePrivateNetwork, svc.GetEffective(key), response.Body.String())
	}
	var count int64
	if err := db.Model(&model.SystemSetting{}).Where("key = ?", key).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("reset DB override count=%d err=%v", count, err)
	}
}
