package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset/ga"
	"xirang/backend/internal/backupasset/overlay"
	assetruntime "xirang/backend/internal/backupasset/runtime"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openConfigHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), strings.ReplaceAll(t.Name(), "/", "_")+".db")
	db, err := gorm.Open(sqlite.Open(dbPath+"?_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}

func setConfigHandlerTestEncryption(t *testing.T) {
	t.Helper()
	t.Cleanup(secure.ResetForTesting)
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_CONFIG_HANDLER_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
}

func TestConfigExportAlwaysOmitsInternalPipelineRevisions(t *testing.T) {
	for _, includeSecrets := range []bool{false, true} {
		t.Run(strconv.FormatBool(includeSecrets), func(t *testing.T) {
			db := openConfigHandlerTestDB(t)
			if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.SystemSetting{}, &model.SSHKey{}, &model.CredentialAuditEvent{}); err != nil {
				t.Fatal(err)
			}
			for _, row := range []model.SystemSetting{
				{Key: settings.ProcessingContentPipelineRevisionKey, Value: "7"},
				{Key: settings.ProcessingOCRPipelineRevisionKey, Value: "8"},
				{Key: "storage.min_free_gb", Value: "42"},
			} {
				if err := db.Create(&row).Error; err != nil {
					t.Fatal(err)
				}
			}
			handler := NewConfigHandler(db, settings.NewService(db))
			router := gin.New()
			router.GET("/config/export", func(c *gin.Context) {
				c.Set("userID", uint(1))
				c.Set("username", "admin")
				c.Set("role", "admin")
				handler.Export(c)
			})
			path := "/config/export"
			if includeSecrets {
				path += "?include_secrets=true"
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			body := response.Body.String()
			if strings.Contains(body, settings.ProcessingContentPipelineRevisionKey) ||
				strings.Contains(body, settings.ProcessingOCRPipelineRevisionKey) || !strings.Contains(body, "storage.min_free_gb") {
				t.Fatalf("internal revisions leaked or public setting disappeared: %s", body)
			}
		})
	}
}

func TestConfigImportRejectsInternalPipelineRevisions(t *testing.T) {
	for _, key := range []string{settings.ProcessingContentPipelineRevisionKey, settings.ProcessingOCRPipelineRevisionKey} {
		t.Run(key, func(t *testing.T) {
			db := openConfigHandlerTestDB(t)
			if err := db.AutoMigrate(&model.SystemSetting{}, &model.CredentialAuditEvent{}); err != nil {
				t.Fatal(err)
			}
			handler := NewConfigHandler(db, settings.NewService(db))
			router := gin.New()
			router.POST("/config/import", handler.Import)
			payload, _ := json.Marshal(map[string]any{"system_settings": []map[string]string{{"key": key, "value": "9"}}})
			request := httptest.NewRequest(http.MethodPost, "/config/import", strings.NewReader(string(payload)))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var count int64
			if err := db.Model(&model.SystemSetting{}).Where("key = ?", key).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("reserved import count=%d error=%v", count, err)
			}
		})
	}
}

func TestConfigExportAndImportExcludeRecoveryTargetRootRegistry(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	db := openConfigHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.SystemSetting{}, &model.SSHKey{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatal(err)
	}
	node := model.Node{
		ID: 81, Name: "config-recovery-node", Host: "config-recovery.invalid", Port: 22,
		Username: "tester", AuthType: "password", BackupDir: "config-recovery-backup",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	service := settings.NewService(db)
	definition := settings.RecoveryTargetRootDefinition{
		NodeID: node.ID, RootID: "config-root", SafeLabel: "FAKE_CONFIG_RECOVERY_ROOT_LABEL_FOR_TEST_ONLY",
		Locator:                 "/srv/FAKE_CONFIG_RECOVERY_ROOT_FOR_TEST_ONLY",
		AuthorityRevision:       "0123456789abcdef0123456789abcdef",
		RootObservationRevision: "FAKE_CONFIG_ROOT_OBSERVATION_FOR_TEST_ONLY",
		Policy: settings.RecoveryTargetRootPolicy{
			ReserveBytes:         4096,
			ReserveInodes:        32,
			OverlapPolicyBinding: "FAKE_CONFIG_OVERLAP_POLICY_FOR_TEST_ONLY",
		},
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := service.RegisterRecoveryTargetRootTx(context.Background(), tx, definition)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	key := settings.RecoveryTargetRootKeyPrefix + strconv.FormatUint(uint64(node.ID), 10) + "." + definition.RootID
	var ciphertext string
	if err := db.Table("system_settings").Select("value").Where("key = ?", key).Scan(&ciphertext).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ciphertext, "enc:v2:") {
		t.Fatalf("private registry row is not v2 ciphertext: %q", ciphertext)
	}

	handler := NewConfigHandler(db, service)
	for _, includeSecrets := range []bool{false, true} {
		t.Run("export-"+strconv.FormatBool(includeSecrets), func(t *testing.T) {
			router := gin.New()
			router.GET("/config/export", func(c *gin.Context) {
				c.Set("user_id", uint(1))
				c.Set("username", "admin")
				c.Set("role", "admin")
				handler.Export(c)
			})
			path := "/config/export"
			if includeSecrets {
				path += "?include_secrets=true"
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			body := response.Body.String()
			for _, private := range []string{key, ciphertext, definition.Locator, definition.SafeLabel} {
				if strings.Contains(body, private) {
					t.Fatalf("private target-root material leaked from export: %s", body)
				}
			}
		})
	}

	payload, err := json.Marshal(map[string]any{
		"system_settings": []map[string]string{{"key": key, "value": ciphertext}},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/config/import", handler.Import)
	request := httptest.NewRequest(http.MethodPost, "/config/import", strings.NewReader(string(payload)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	for _, private := range []string{key, ciphertext, definition.Locator, definition.SafeLabel} {
		if strings.Contains(response.Body.String(), private) {
			t.Fatalf("private target-root material leaked from import rejection: %s", response.Body.String())
		}
	}
	var after string
	if err := db.Table("system_settings").Select("value").Where("key = ?", key).Scan(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after != ciphertext {
		t.Fatal("rejected config import changed the registered target root")
	}
}

func TestConfigImportBlockedBackupAssetsEnabledDoesNotPersist(t *testing.T) {
	db := openConfigHandlerTestDB(t)
	if err := db.AutoMigrate(&model.SystemSetting{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("migrate import settings: %v", err)
	}
	svc := settings.NewService(db)
	spy := &settingsTransitionSpy{}
	handler := NewConfigHandler(db, svc).WithBackupAssetTransitioner(assetruntime.EnablementRuntime(
		settingsEnablementReadiness{snapshot: ga.ReadinessSnapshot{
			Class:             ga.InstallationExisting,
			Status:            ga.ReadinessBlocked,
			InventoryComplete: false,
		}},
		spy,
	))
	router := gin.New()
	router.POST("/config/import", handler.Import)

	request := httptest.NewRequest(http.MethodPost, "/config/import", strings.NewReader(`{"system_settings":[{"key":"backup_assets.enabled","value":"true"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assertBackupAssetEnablementBlockedConflict(t, response)
	var count int64
	if err := db.Model(&model.SystemSetting{}).Where("key = ?", "backup_assets.enabled").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 || svc.GetEffective("backup_assets.enabled") != "false" || len(spy.targets) != 0 {
		t.Fatalf("blocked import persisted enabled count=%d effective=%q targets=%v", count, svc.GetEffective("backup_assets.enabled"), spy.targets)
	}
}

func TestConfigImportTransitionsBackupAssetEnableBeforePersistingSettings(t *testing.T) {
	db := openConfigHandlerTestDB(t)
	if err := db.AutoMigrate(&model.SystemSetting{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("migrate import settings: %v", err)
	}
	svc := settings.NewService(db)
	spy := &settingsTransitionSpy{}
	spy.beforePersist = func() error {
		if got := svc.GetEffective("backup_assets.enabled"); got != "false" {
			t.Fatalf("import persisted enabled before transition drain: %q", got)
		}
		return nil
	}
	handler := NewConfigHandler(db, svc).WithBackupAssetTransitioner(spy)
	router := gin.New()
	router.POST("/config/import", handler.Import)

	request := httptest.NewRequest(http.MethodPost, "/config/import", strings.NewReader(`{"system_settings":[{"key":"backup_assets.enabled","value":"true"}]}`))
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

func TestConfigImportDynamicExportMutationUsesRuntimeSettingsTransition(t *testing.T) {
	db := openConfigHandlerTestDB(t)
	if err := db.AutoMigrate(&model.SystemSetting{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("migrate import settings: %v", err)
	}
	svc := settings.NewService(db)
	spy := &settingsRuntimeSettingsTransitionSpy{}
	handler := NewConfigHandler(db, svc).WithBackupAssetTransitioner(spy)
	router := gin.New()
	router.POST("/config/import", handler.Import)

	request := httptest.NewRequest(http.MethodPost, "/config/import", strings.NewReader(`{"system_settings":[{"key":"backup_assets.export.ticket_max_requests","value":"128"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if spy.calls != 1 {
		t.Fatalf("runtime settings transition calls=%d, want one", spy.calls)
	}
	if spy.current["backup_assets.export.ticket_max_requests"] != "256" {
		t.Fatalf("current max requests=%q, want default 256", spy.current["backup_assets.export.ticket_max_requests"])
	}
	if len(spy.overlay) != 1 || spy.overlay["backup_assets.export.ticket_max_requests"] != "128" {
		t.Fatalf("overlay=%v", spy.overlay)
	}
	if spy.effective["backup_assets.export.ticket_max_requests"] != "128" || spy.config.Ticket.MaxRequests != 128 {
		t.Fatalf("effective=%q config=%+v", spy.effective["backup_assets.export.ticket_max_requests"], spy.config)
	}
}

func TestConfigImportSharedIdempotencySettingsUsesRuntimeSettingsTransition(t *testing.T) {
	db := openConfigHandlerTestDB(t)
	if err := db.AutoMigrate(&model.SystemSetting{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("migrate import settings: %v", err)
	}
	svc := settings.NewService(db)
	overlayService, now := newSettingsTransitionOverlay(t, db, overlay.DefaultConfig())
	spy := &settingsRuntimeSettingsTransitionSpy{overlayService: overlayService}
	handler := NewConfigHandler(db, svc).WithBackupAssetTransitioner(spy)
	router := gin.New()
	router.POST("/config/import", handler.Import)

	request := httptest.NewRequest(http.MethodPost, "/config/import", strings.NewReader(`{"system_settings":[{"key":"backup_assets.idempotency_ttl","value":"48h"},{"key":"backup_assets.idempotency_key_max_bytes","value":"192"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if spy.calls != 1 || spy.config.IdempotencyTTL != 48*time.Hour || spy.config.IdempotencyKeyMaxBytes != 192 {
		t.Fatalf("runtime idempotency import calls=%d config=%+v", spy.calls, spy.config)
	}
	if _, err := overlayService.ClearRecent(context.Background(), 781, strings.Repeat("a", 160)); err != nil {
		t.Fatalf("imported Overlay key limit rejected handler-approved key: %v", err)
	}
	assertSettingsOverlayReceiptTTL(t, db, 781, now, 48*time.Hour)
}

func TestConfigImportFailedBackupAssetTransitionDoesNotPersistSettings(t *testing.T) {
	db := openConfigHandlerTestDB(t)
	if err := db.AutoMigrate(&model.SystemSetting{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("migrate import settings: %v", err)
	}
	svc := settings.NewService(db)
	spy := &settingsTransitionSpy{err: errors.New("FAKE_IMPORT_TRANSITION_FAILURE_FOR_TEST_ONLY")}
	handler := NewConfigHandler(db, svc).WithBackupAssetTransitioner(spy)
	router := gin.New()
	router.POST("/config/import", handler.Import)

	request := httptest.NewRequest(http.MethodPost, "/config/import", strings.NewReader(`{"system_settings":[{"key":"backup_assets.enabled","value":"true"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var count int64
	if err := db.Model(&model.SystemSetting{}).Where("key = ?", "backup_assets.enabled").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 || svc.GetEffective("backup_assets.enabled") != "false" {
		t.Fatalf("failed import persisted enabled override count=%d effective=%q", count, svc.GetEffective("backup_assets.enabled"))
	}
}

func TestConfigImportTaskCreateFailureReturnsGenericInternalErrorAndRollsBack(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	db := openConfigHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.SystemSetting{}, &model.SSHKey{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatal(err)
	}

	injectedErr := errors.New("FAKE_CONFIG_IMPORT_TASK_CREATE_FAILURE_FOR_TEST_ONLY")
	const callbackName = "test:config-import-task-create-failure"
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Schema != nil && tx.Statement.Schema.Table == "tasks" {
			_ = tx.AddError(injectedErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })

	handler := NewConfigHandler(db, nil)
	router := gin.New()
	router.POST("/config/import", handler.Import)
	body := `{
  "nodes":[{"name":"rollback-node","host":"10.0.0.8","port":22,"username":"root","auth_type":"key"}],
  "tasks":[{
    "name":"rollback-managed-task","node_name":"rollback-node","executor_type":"rsync",
    "executor_config":"{\"version\":1,\"publication_mode\":\"versioned_hardlink\",\"managed_root\":\"/foreign/managed\"}"
  }]
}`
	request := httptest.NewRequest(http.MethodPost, "/config/import", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Errorf("status=%d, want %d; body=%s", response.Code, http.StatusInternalServerError, response.Body.String())
	}
	var envelope Response
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Errorf("decode response envelope: %v", err)
	} else if envelope.Code != http.StatusInternalServerError || envelope.Message != "服务器内部错误" || envelope.Data != nil {
		t.Errorf("response envelope=%+v, want generic internal error", envelope)
	}
	if strings.Contains(response.Body.String(), injectedErr.Error()) {
		t.Errorf("response leaked injected task create error: %s", response.Body.String())
	}

	for modelType, name := range map[any]string{
		&model.Node{}: "nodes",
		&model.Task{}: "tasks",
	} {
		var count int64
		if err := db.Model(modelType).Count(&count).Error; err != nil {
			t.Errorf("count %s: %v", name, err)
		} else if count != 0 {
			t.Errorf("%s count=%d, want total rollback", name, count)
		}
	}
}

func TestConfigImportManagedRsyncTaskPausesAndDisconnectsForeignPublicationConfig(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	db := openConfigHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.SystemSetting{}, &model.SSHKey{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatal(err)
	}
	handler := NewConfigHandler(db, nil)
	router := gin.New()
	router.POST("/config/import", handler.Import)
	body := `{
  "nodes":[{"name":"import-node","host":"10.0.0.8","port":22,"username":"root","auth_type":"key"}],
  "tasks":[{
    "name":"foreign-managed-rsync","node_name":"import-node","executor_type":"rsync",
    "rsync_source":"/foreign/source","rsync_target":"/foreign/legacy",
    "executor_config":"{\"version\":1,\"publication_mode\":\"versioned_hardlink\",\"managed_root\":\"/foreign/managed\",\"preflight_id\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"}",
    "cron_spec":"*/5 * * * *","enabled":true
  }]
}`
	request := httptest.NewRequest(http.MethodPost, "/config/import", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var imported model.Task
	if err := db.Where("name = ?", "foreign-managed-rsync").First(&imported).Error; err != nil {
		t.Fatal(err)
	}
	if imported.Enabled || imported.RsyncSource != "" || imported.RsyncTarget != "" ||
		imported.ExecutorConfig != `{"version":1,"publication_mode":"legacy_mutable"}` {
		t.Fatalf("managed import was not paused and disconnected: %+v", imported)
	}
}

func TestConfigImportManagedRsyncTaskDiscardsForeignPathsBeforeValidation(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	db := openConfigHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.SystemSetting{}, &model.SSHKey{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatal(err)
	}
	handler := NewConfigHandler(db, nil)
	router := gin.New()
	router.POST("/config/import", handler.Import)
	body := `{
  "nodes":[{"name":"import-node","host":"10.0.0.8","port":22,"username":"root","auth_type":"key"}],
  "tasks":[{
    "name":"foreign-managed-rsync-paths","node_name":"import-node","executor_type":"rsync",
    "rsync_source":"relative/../../foreign","rsync_target":"not-an-absolute-target",
    "executor_config":"{\"version\":1,\"managed_root\":\"/foreign/managed\",\"publication_mode\":\"versioned_full_copy\",\"preflight_id\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"}",
    "cron_spec":"*/5 * * * *","enabled":true
  }]
}`
	request := httptest.NewRequest(http.MethodPost, "/config/import", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var imported model.Task
	if err := db.Where("name = ?", "foreign-managed-rsync-paths").First(&imported).Error; err != nil {
		t.Fatal(err)
	}
	if imported.Enabled || imported.RsyncSource != "" || imported.RsyncTarget != "" ||
		imported.ExecutorConfig != `{"version":1,"publication_mode":"legacy_mutable"}` {
		t.Fatalf("managed import retained foreign runtime inputs: %+v", imported)
	}
}

func TestConfigImportManagedRcloneTasksPauseAndDisconnectForeignPublicationConfig(t *testing.T) {
	for _, mode := range []string{"versioned_prefix", "native_object_versions"} {
		t.Run(mode, func(t *testing.T) {
			setConfigHandlerTestEncryption(t)
			db := openConfigHandlerTestDB(t)
			if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.SystemSetting{}, &model.SSHKey{}, &model.CredentialAuditEvent{}); err != nil {
				t.Fatal(err)
			}
			handler := NewConfigHandler(db, nil)
			router := gin.New()
			router.POST("/config/import", handler.Import)

			foreignConfig, err := json.Marshal(map[string]any{
				"version":          1,
				"publication_mode": mode,
				"binding_version":  3,
				"preflight_id":     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"provider_secret":  "FAKE_PROVIDER_SECRET_FOR_TEST_ONLY",
			})
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(map[string]any{
				"nodes": []map[string]any{{
					"name": "import-node", "host": "10.0.0.8", "port": 22,
					"username": "root", "auth_type": "key",
				}},
				"tasks": []map[string]any{{
					"name": "foreign-managed-rclone-" + mode, "node_name": "import-node",
					"executor_type": "rclone", "rsync_source": "/srv/trusted-source",
					"rsync_target": "foreign:bucket/private", "executor_config": string(foreignConfig),
					"cron_spec": "*/5 * * * *", "enabled": true,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}

			request := httptest.NewRequest(http.MethodPost, "/config/import", strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}

			var imported model.Task
			if err := db.Where("name = ?", "foreign-managed-rclone-"+mode).First(&imported).Error; err != nil {
				t.Fatal(err)
			}
			if imported.Enabled || imported.RsyncSource != "/srv/trusted-source" || imported.RsyncTarget != "" ||
				imported.ExecutorConfig != `{"version":1,"publication_mode":"legacy_mutable"}` {
				t.Fatalf("managed Rclone import was not safely disconnected: %+v", imported)
			}
			if strings.Contains(imported.ExecutorConfig, "preflight") || strings.Contains(imported.ExecutorConfig, "secret") {
				t.Fatalf("managed Rclone import retained foreign evidence: %q", imported.ExecutorConfig)
			}
		})
	}
}

func TestConfigExportedDataCanBeImportedBackAsDownloadedFile(t *testing.T) {
	sourceDB := openConfigHandlerTestDB(t)
	if err := sourceDB.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.SystemSetting{}, &model.SSHKey{}); err != nil {
		t.Fatalf("初始化源数据库失败: %v", err)
	}

	node := model.Node{Name: "node-a", Host: "10.0.0.1", Port: 22, Username: "root", AuthType: "key", BackupDir: "node-a"}
	if err := sourceDB.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	policy := model.Policy{Name: "policy-a", SourcePath: "/data/src", TargetPath: "/backup/node-a", CronSpec: "*/5 * * * *", Enabled: true}
	if err := sourceDB.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	taskEntity := model.Task{
		Name:         "task-a",
		NodeID:       node.ID,
		PolicyID:     &policy.ID,
		ExecutorType: "rsync",
		RsyncSource:  "/data/src",
		RsyncTarget:  "/backup/node-a",
		CronSpec:     "*/5 * * * *",
		Status:       "pending",
	}
	if err := sourceDB.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	dependentTask := model.Task{
		Name:            "task-b",
		NodeID:          node.ID,
		PolicyID:        &policy.ID,
		DependsOnTaskID: &taskEntity.ID,
		ExecutorType:    "rsync",
		RsyncSource:     "/data/dep",
		RsyncTarget:     "/backup/node-a/dep",
		Status:          "pending",
	}
	if err := sourceDB.Create(&dependentTask).Error; err != nil {
		t.Fatalf("创建依赖任务失败: %v", err)
	}

	exportHandler := NewConfigHandler(sourceDB, nil)
	exportRouter := gin.New()
	exportRouter.GET("/config/export", exportHandler.Export)

	exportResp := httptest.NewRecorder()
	exportReq := httptest.NewRequest(http.MethodGet, "/config/export", nil)
	exportRouter.ServeHTTP(exportResp, exportReq)
	if exportResp.Code != http.StatusOK {
		t.Fatalf("导出接口期望 200，实际: %d，响应: %s", exportResp.Code, exportResp.Body.String())
	}

	var exportPayload struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(exportResp.Body.Bytes(), &exportPayload); err != nil {
		t.Fatalf("解析导出响应失败: %v", err)
	}

	downloadedFile, err := json.Marshal(exportPayload.Data)
	if err != nil {
		t.Fatalf("序列化下载文件失败: %v", err)
	}

	targetDB := openConfigHandlerTestDB(t)
	if err := targetDB.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.SystemSetting{}, &model.SSHKey{}); err != nil {
		t.Fatalf("初始化目标数据库失败: %v", err)
	}

	targetNode := model.Node{Name: "node-a", Host: "10.0.0.9", Port: 22, Username: "root", AuthType: "key", BackupDir: "node-a"}
	if err := targetDB.Create(&targetNode).Error; err != nil {
		t.Fatalf("创建目标节点失败: %v", err)
	}
	targetPolicy := model.Policy{Name: "policy-a", SourcePath: "/seed/src", TargetPath: "/seed/dst", CronSpec: "0 * * * *", Enabled: false}
	if err := targetDB.Create(&targetPolicy).Error; err != nil {
		t.Fatalf("创建目标策略失败: %v", err)
	}

	importHandler := NewConfigHandler(targetDB, nil)
	importRouter := gin.New()
	importRouter.POST("/config/import", importHandler.Import)

	importReq := httptest.NewRequest(http.MethodPost, "/config/import?conflict=skip", strings.NewReader(string(downloadedFile)))
	importReq.Header.Set("Content-Type", "application/json")
	importResp := httptest.NewRecorder()
	importRouter.ServeHTTP(importResp, importReq)
	if importResp.Code != http.StatusOK {
		t.Fatalf("导入接口期望 200，实际: %d，响应: %s", importResp.Code, importResp.Body.String())
	}

	var importedTask model.Task
	if err := targetDB.Where("name = ?", "task-a").First(&importedTask).Error; err != nil {
		t.Fatalf("导入后应存在任务记录，实际错误: %v", err)
	}
	if importedTask.NodeID != targetNode.ID {
		t.Fatalf("任务应按节点名称映射到目标节点，实际 node_id=%d，期望 %d", importedTask.NodeID, targetNode.ID)
	}
	if importedTask.PolicyID == nil || *importedTask.PolicyID != targetPolicy.ID {
		t.Fatalf("任务应按策略名称映射到目标策略，实际 policy_id=%v，期望 %d", importedTask.PolicyID, targetPolicy.ID)
	}

	var importedDependent model.Task
	if err := targetDB.Where("name = ?", "task-b").First(&importedDependent).Error; err != nil {
		t.Fatalf("导入后应存在依赖任务记录，实际错误: %v", err)
	}
	if importedDependent.DependsOnTaskID == nil || *importedDependent.DependsOnTaskID != importedTask.ID {
		t.Fatalf("导入后应恢复任务依赖关系，实际 depends_on_task_id=%v，期望 %d", importedDependent.DependsOnTaskID, importedTask.ID)
	}
}

func TestConfigExportOmitsSecretsByDefaultAndWritesSafeAudit(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	db := openConfigHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.SystemSetting{}, &model.SSHKey{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("初始化数据库失败: %v", err)
	}

	key := model.SSHKey{
		Name:        "export-key",
		Username:    "deploy",
		KeyType:     "auto",
		PrivateKey:  "FAKE_SSH_PRIVATE_KEY_FOR_TEST_ONLY",
		Fingerprint: "SHA256:export-key",
	}
	if err := db.Create(&key).Error; err != nil {
		t.Fatalf("创建 SSH key 失败: %v", err)
	}
	node := model.Node{
		Name:       "export-node",
		Host:       "10.77.0.10",
		Port:       22,
		Username:   "root",
		AuthType:   "key",
		SSHKeyID:   &key.ID,
		Password:   "FAKE_NODE_PASSWORD_FOR_TEST_ONLY",
		PrivateKey: "FAKE_NODE_PRIVATE_KEY_FOR_TEST_ONLY",
		BackupDir:  "export-node",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	taskEntity := model.Task{
		Name:           "export-task",
		NodeID:         node.ID,
		ExecutorType:   "restic",
		RsyncSource:    "/data/export",
		RsyncTarget:    "/backup/export",
		ExecutorConfig: "{\"token\":\"FAKE_EXECUTOR_TOKEN_FOR_TEST_ONLY\"}",
		Status:         "pending",
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	for _, setting := range []model.SystemSetting{
		{Key: "smtp.password", Value: "FAKE_SMTP_PASSWORD_FOR_TEST_ONLY"},
		{Key: "metrics.remote_bearer_token", Value: "FAKE_METRICS_TOKEN_FOR_TEST_ONLY"},
		{Key: "storage.min_free_gb", Value: "42"},
	} {
		if err := db.Create(&setting).Error; err != nil {
			t.Fatalf("创建系统设置失败: %v", err)
		}
	}

	handler := NewConfigHandler(db, nil)
	router := gin.New()
	router.GET("/config/export", func(c *gin.Context) {
		c.Set("userID", uint(10))
		c.Set("username", "admin")
		c.Set("role", "admin")
		handler.Export(c)
	})

	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/config/export", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("导出接口期望 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	for _, forbidden := range []string{
		"FAKE_SSH_PRIVATE_KEY_FOR_TEST_ONLY",
		"FAKE_NODE_PASSWORD_FOR_TEST_ONLY",
		"FAKE_NODE_PRIVATE_KEY_FOR_TEST_ONLY",
		"FAKE_EXECUTOR_TOKEN_FOR_TEST_ONLY",
		"FAKE_SMTP_PASSWORD_FOR_TEST_ONLY",
		"FAKE_METRICS_TOKEN_FOR_TEST_ONLY",
		"metrics.remote_bearer_token",
		"private_key",
		"password",
		"executor_config",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("默认配置导出不应包含敏感字段 %q，响应: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, "storage.min_free_gb") || !strings.Contains(body, "42") {
		t.Fatalf("默认配置导出应保留非敏感系统设置，响应: %s", body)
	}

	var event model.CredentialAuditEvent
	if err := db.Where("action = ?", "config.export").First(&event).Error; err != nil {
		t.Fatalf("应写入 config.export 凭据审计事件: %v", err)
	}
	if event.Purpose != "config_export" || event.Outcome != credentialaudit.OutcomeSuccess || event.Username != "admin" || event.Role != "admin" {
		t.Fatalf("config export audit event 不符合预期: %+v", event)
	}
	if strings.Contains(event.Metadata, "FAKE_") || strings.Contains(event.Metadata, "private_key") || strings.Contains(event.Metadata, "executor_config") {
		t.Fatalf("config export audit metadata 不应包含导出载荷或密钥材料: %s", event.Metadata)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
		t.Fatalf("metadata json: %v", err)
	}
	if metadata["with_sensitive"] != false || metadata["node_count"] == nil || metadata["key_count"] == nil || metadata["task_count"] == nil {
		t.Fatalf("config export audit metadata 缺少安全计数字段: %#v", metadata)
	}
}

func TestConfigExportIncludeSecretsRequiresAdminAndAuditsBlockedAttempt(t *testing.T) {
	db := openConfigHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.SystemSetting{}, &model.SSHKey{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("初始化数据库失败: %v", err)
	}

	handler := NewConfigHandler(db, nil)
	router := gin.New()
	router.GET("/config/export", func(c *gin.Context) {
		c.Set("userID", uint(20))
		c.Set("username", "bob")
		c.Set("role", "operator")
		handler.Export(c)
	})

	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/config/export?include_secrets=true", nil))
	if resp.Code != http.StatusForbidden {
		t.Fatalf("非 admin include_secrets 应返回 403，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	var event model.CredentialAuditEvent
	if err := db.Where("action = ?", "config.export").First(&event).Error; err != nil {
		t.Fatalf("应写入 blocked config.export 审计事件: %v", err)
	}
	if event.Outcome != credentialaudit.OutcomeBlocked || event.Username != "bob" || event.Role != "operator" {
		t.Fatalf("blocked config export audit event 不符合预期: %+v", event)
	}
	if strings.Contains(event.Metadata, "secret") || strings.Contains(event.Metadata, "FAKE_") || strings.Contains(event.Metadata, "payload") {
		t.Fatalf("blocked audit metadata 不应包含敏感键/载荷: %s", event.Metadata)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
		t.Fatalf("metadata json: %v", err)
	}
	if metadata["stage"] != "authorization" || metadata["with_sensitive"] != true {
		t.Fatalf("blocked audit metadata 缺少授权阶段标记: %#v", metadata)
	}
}

func TestConfigExportIncludeSecretsAsAdminAuditsWithoutPayload(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	db := openConfigHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.SystemSetting{}, &model.SSHKey{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("初始化数据库失败: %v", err)
	}
	key := model.SSHKey{Name: "admin-export-key", Username: "deploy", KeyType: "auto", PrivateKey: "FAKE_ADMIN_EXPORT_PRIVATE_KEY_FOR_TEST_ONLY", Fingerprint: "SHA256:admin-export-key"}
	if err := db.Create(&key).Error; err != nil {
		t.Fatalf("创建 SSH key 失败: %v", err)
	}
	node := model.Node{Name: "admin-export-node", Host: "10.77.0.11", Port: 22, Username: "root", AuthType: "key", SSHKeyID: &key.ID, Password: "FAKE_ADMIN_EXPORT_PASSWORD_FOR_TEST_ONLY", BackupDir: "admin-export-node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	if err := db.Create(&model.SystemSetting{Key: "smtp.password", Value: "FAKE_ADMIN_EXPORT_SMTP_PASSWORD_FOR_TEST_ONLY"}).Error; err != nil {
		t.Fatalf("创建系统设置失败: %v", err)
	}

	handler := NewConfigHandler(db, nil)
	router := gin.New()
	router.GET("/config/export", func(c *gin.Context) {
		c.Set("userID", uint(30))
		c.Set("username", "admin")
		c.Set("role", "admin")
		handler.Export(c)
	})

	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/config/export?include_secrets=true", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("admin include_secrets 应返回 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	for _, expected := range []string{"FAKE_ADMIN_EXPORT_PRIVATE_KEY_FOR_TEST_ONLY", "FAKE_ADMIN_EXPORT_PASSWORD_FOR_TEST_ONLY", "FAKE_ADMIN_EXPORT_SMTP_PASSWORD_FOR_TEST_ONLY"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("admin include_secrets 响应应包含请求的敏感导出值 %q，响应: %s", expected, body)
		}
	}

	var event model.CredentialAuditEvent
	if err := db.Where("action = ?", "config.export").First(&event).Error; err != nil {
		t.Fatalf("应写入 config.export 审计事件: %v", err)
	}
	if event.Outcome != credentialaudit.OutcomeSuccess {
		t.Fatalf("admin include_secrets 审计事件应成功: %+v", event)
	}
	if strings.Contains(event.Metadata, "FAKE_ADMIN_EXPORT") || strings.Contains(event.Metadata, "private_key") || strings.Contains(event.Metadata, "password") {
		t.Fatalf("审计 metadata 不应复制导出载荷或敏感字段名: %s", event.Metadata)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
		t.Fatalf("metadata json: %v", err)
	}
	if metadata["with_sensitive"] != true || metadata["stage"] != "success" {
		t.Fatalf("admin include_secrets 审计 metadata 不符合预期: %#v", metadata)
	}
}

func TestConfigExportImportPreservesSSHKeyScopeMetadata(t *testing.T) {
	sourceDB := openConfigHandlerTestDB(t)
	if err := sourceDB.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.SystemSetting{}, &model.SSHKey{}); err != nil {
		t.Fatalf("初始化源数据库失败: %v", err)
	}
	future := time.Date(2026, 6, 1, 10, 30, 0, 0, time.UTC)
	key := model.SSHKey{
		Name:            "scoped-key",
		Username:        "deploy",
		KeyType:         "auto",
		PrivateKey:      "",
		Fingerprint:     "SHA256:scope",
		Disabled:        true,
		ExpiresAt:       &future,
		AllowedPurposes: "terminal,task_command",
		AllowedNodeIDs:  "1,2",
		AllowedNodeTags: "prod,db",
	}
	if err := sourceDB.Create(&key).Error; err != nil {
		t.Fatalf("创建 SSH key 失败: %v", err)
	}

	exportHandler := NewConfigHandler(sourceDB, nil)
	exportRouter := gin.New()
	exportRouter.GET("/config/export", exportHandler.Export)
	exportResp := httptest.NewRecorder()
	exportRouter.ServeHTTP(exportResp, httptest.NewRequest(http.MethodGet, "/config/export", nil))
	if exportResp.Code != http.StatusOK {
		t.Fatalf("导出接口期望 200，实际: %d，响应: %s", exportResp.Code, exportResp.Body.String())
	}

	var exportPayload struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(exportResp.Body.Bytes(), &exportPayload); err != nil {
		t.Fatalf("解析导出响应失败: %v", err)
	}
	downloadedFile, err := json.Marshal(exportPayload.Data)
	if err != nil {
		t.Fatalf("序列化下载文件失败: %v", err)
	}

	targetDB := openConfigHandlerTestDB(t)
	if err := targetDB.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.SystemSetting{}, &model.SSHKey{}); err != nil {
		t.Fatalf("初始化目标数据库失败: %v", err)
	}
	importHandler := NewConfigHandler(targetDB, nil)
	importRouter := gin.New()
	importRouter.POST("/config/import", importHandler.Import)
	importResp := httptest.NewRecorder()
	importReq := httptest.NewRequest(http.MethodPost, "/config/import?conflict=skip", strings.NewReader(string(downloadedFile)))
	importReq.Header.Set("Content-Type", "application/json")
	importRouter.ServeHTTP(importResp, importReq)
	if importResp.Code != http.StatusOK {
		t.Fatalf("导入接口期望 200，实际: %d，响应: %s", importResp.Code, importResp.Body.String())
	}

	var imported model.SSHKey
	if err := targetDB.Where("name = ?", "scoped-key").First(&imported).Error; err != nil {
		t.Fatalf("导入后应存在 SSH key，实际错误: %v", err)
	}
	if !imported.Disabled || imported.ExpiresAt == nil || !imported.ExpiresAt.Equal(future) || imported.AllowedPurposes != "terminal,task_command" || imported.AllowedNodeIDs != "1,2" || imported.AllowedNodeTags != "prod,db" {
		t.Fatalf("SSH key scope 元数据未完整保留: disabled=%v expires=%v purposes=%q nodes=%q tags=%q", imported.Disabled, imported.ExpiresAt, imported.AllowedPurposes, imported.AllowedNodeIDs, imported.AllowedNodeTags)
	}
}

func TestConfigImportAcceptsWrappedExportEnvelope(t *testing.T) {
	targetDB := openConfigHandlerTestDB(t)
	if err := targetDB.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.SystemSetting{}, &model.SSHKey{}); err != nil {
		t.Fatalf("初始化目标数据库失败: %v", err)
	}

	importHandler := NewConfigHandler(targetDB, nil)
	importRouter := gin.New()
	importRouter.POST("/config/import", importHandler.Import)

	body := `{"version":"1.0","exported_at":"2026-03-24T00:00:00Z","data":{"nodes":[{"name":"node-a","host":"10.0.0.1","port":22,"username":"root","auth_type":"key"}]}}`
	importReq := httptest.NewRequest(http.MethodPost, "/config/import?conflict=skip", strings.NewReader(body))
	importReq.Header.Set("Content-Type", "application/json")
	importResp := httptest.NewRecorder()
	importRouter.ServeHTTP(importResp, importReq)

	if importResp.Code != http.StatusOK {
		t.Fatalf("导入包裹格式期望 200，实际: %d，响应: %s", importResp.Code, importResp.Body.String())
	}

	var count int64
	if err := targetDB.Model(&model.Node{}).Where("name = ?", "node-a").Count(&count).Error; err != nil {
		t.Fatalf("统计节点失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("包裹格式导入后应创建节点，实际数量: %d", count)
	}
}
