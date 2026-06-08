package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/profile"
	"xirang/backend/internal/secure"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupCredentialTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("failed to get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	// 创建 app_credentials 表 + policies 表（Policy 模型用）
	if err := db.AutoMigrate(&model.AppCredential{}, &model.Policy{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return db
}

func setupCredentialRouter(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("db", db)
	})
	return r
}

func TestAppCredentialList(t *testing.T) {
	db := setupCredentialTestDB(t)
	h := NewAppCredentialHandler(db)
	// 预置两个凭据
	db.Create(&model.AppCredential{Name: "mysql-prod", Type: "mysql", Config: `{"host":"127.0.0.1","password":"FAKE_PW_FOR_TEST_ONLY"}`})
	db.Create(&model.AppCredential{Name: "pg-dev", Type: "postgres", Config: `{"host":"127.0.0.1","user":"postgres"}`})

	r := setupCredentialRouter(db)
	r.GET("/app-credentials", h.List)
	req := httptest.NewRequest("GET", "/app-credentials", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	items, ok := resp.Data.([]interface{})
	if !ok {
		t.Fatalf("expected array data, got %T", resp.Data)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
}

func TestAppCredentialCreate(t *testing.T) {
	db := setupCredentialTestDB(t)
	h := NewAppCredentialHandler(db)

	r := setupCredentialRouter(db)
	r.POST("/app-credentials", h.Create)

	body := `{"type":"mysql","name":"test-mysql","host":"10.0.0.1","port":"3306","user":"root","password":"FAKE_ROOT_PW_FOR_TEST_ONLY"}`
	req := httptest.NewRequest("POST", "/app-credentials", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 201 {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// 验证 password 已加密入库（raw query 不走 AfterFind）
	var rawConfig string
	if err := db.Raw("SELECT config FROM app_credentials WHERE id = ?", 1).Scan(&rawConfig).Error; err != nil {
		t.Fatalf("raw config query: %v", err)
	}
	if rawConfig == "" {
		t.Fatal("config should not be empty in DB")
	}
	// 加密后的 config 带有 enc:v1: 或 enc:v2: 前缀
	if !strings.HasPrefix(rawConfig, "enc:v1:") && !strings.HasPrefix(rawConfig, "enc:v2:") {
		t.Errorf("config should be encrypted in DB, got: %s...", rawConfig[:min(40, len(rawConfig))])
	}

	// 通过 handler 验证 API 响应脱敏
	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	dataMap := resp.Data.(map[string]interface{})
	if cfg, ok := dataMap["config"].(map[string]interface{}); ok {
		if _, exists := cfg["password"]; exists {
			t.Error("API response should not contain password")
		}
	}
	if hp, ok := dataMap["has_password"].(bool); !ok || !hp {
		t.Error("has_password should be true")
	}
}

func TestAppCredentialCreateMissingContainerName(t *testing.T) {
	db := setupCredentialTestDB(t)
	h := NewAppCredentialHandler(db)

	r := setupCredentialRouter(db)
	r.POST("/app-credentials", h.Create)

	body := `{"type":"docker-mysql","name":"docker-mysql","host":"127.0.0.1","user":"root","password":"FAKE_PW_FOR_TEST_ONLY"}`
	req := httptest.NewRequest("POST", "/app-credentials", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Fatalf("expected 400 for missing container_name, got %d", w.Code)
	}
}

func TestAppCredentialCreateInvalidType(t *testing.T) {
	db := setupCredentialTestDB(t)
	h := NewAppCredentialHandler(db)

	r := setupCredentialRouter(db)
	r.POST("/app-credentials", h.Create)

	body := `{"type":"oracle","name":"invalid-type"}`
	req := httptest.NewRequest("POST", "/app-credentials", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestAppCredentialUpdate(t *testing.T) {
	db := setupCredentialTestDB(t)
	h := NewAppCredentialHandler(db)

	// 先创建一个
	db.Create(&model.AppCredential{Name: "old-name", Type: "mysql", Config: `{"host":"1.2.3.4","password":"FAKE_OLD_PW_FOR_TEST_ONLY"}`})

	r := setupCredentialRouter(db)
	r.PUT("/app-credentials/:id", h.Update)

	body := `{"type":"mysql","name":"new-name","host":"5.6.7.8","port":"3307","user":"admin","password":"FAKE_NEW_PW_FOR_TEST_ONLY"}`
	req := httptest.NewRequest("PUT", "/app-credentials/1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var updated model.AppCredential
	db.First(&updated, 1)
	if updated.Name != "new-name" {
		t.Errorf("expected name 'new-name', got '%s'", updated.Name)
	}
}

func TestAppCredentialUpdateInvalidStoredConfigDoesNotExposeSecret(t *testing.T) {
	db := setupCredentialTestDB(t)
	h := NewAppCredentialHandler(db)

	if err := db.Exec("INSERT INTO app_credentials (id, name, type, config, created_at, updated_at) VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)", 1, "bad-config", "mysql", `{"password":"FAKE_BAD_CONFIG_PW_FOR_TEST_ONLY","host":"bad.internal"`).Error; err != nil {
		t.Fatalf("insert invalid credential: %v", err)
	}

	r := setupCredentialRouter(db)
	r.PUT("/app-credentials/:id", h.Update)

	body := `{"type":"mysql","name":"bad-config","host":"127.0.0.1"}`
	req := httptest.NewRequest("PUT", "/app-credentials/1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 500 {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	for _, forbidden := range []string{"FAKE_BAD_CONFIG_PW_FOR_TEST_ONLY", "bad.internal", "password", "host"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatalf("response exposed forbidden value %q in %s", forbidden, w.Body.String())
		}
	}
}

func TestAppCredentialUpdatePreservePassword(t *testing.T) {
	db := setupCredentialTestDB(t)
	h := NewAppCredentialHandler(db)

	db.Create(&model.AppCredential{Name: "keep-pw", Type: "mysql", Config: `{"host":"1.2.3.4","password":"FAKE_KEEP_PW_FOR_TEST_ONLY"}`})

	r := setupCredentialRouter(db)
	r.PUT("/app-credentials/:id", h.Update)

	// 不提供 password
	body := `{"type":"mysql","name":"keep-pw","host":"9.9.9.9"}`
	req := httptest.NewRequest("PUT", "/app-credentials/1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var updated model.AppCredential
	db.First(&updated, 1)
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(updated.Config), &cfg); err != nil {
		t.Fatalf("config json parse: %v", err)
	}
	if cfg["password"] != "FAKE_KEEP_PW_FOR_TEST_ONLY" {
		t.Error("password should be preserved when not provided")
	}
	if cfg["host"] != "9.9.9.9" {
		t.Errorf("host should be updated, got %v", cfg["host"])
	}
}

func TestAppCredentialUpdateClearsLegacyGeneratedHooksWithoutPassword(t *testing.T) {
	db := setupCredentialTestDB(t)
	h := NewAppCredentialHandler(db)

	oldCfg := map[string]interface{}{"host": "10.0.0.1", "user": "root"}
	oldCfgJSON, _ := json.Marshal(oldCfg)
	if err := db.Create(&model.AppCredential{Name: "cascade-no-pw", Type: "mysql", Config: string(oldCfgJSON)}).Error; err != nil {
		t.Fatalf("create credential: %v", err)
	}

	renderedPre, renderedPost, err := profile.RenderHooks("mysql", oldCfg)
	if err != nil {
		t.Fatalf("RenderHooks: %v", err)
	}
	if err := db.Create(&model.Policy{
		Name:            "cascade-no-pw-policy",
		AppProfile:      "mysql",
		AppCredentialID: uintPtr(1),
		SourcePath:      "/src",
		CronSpec:        "0 0 * * *",
		TargetPath:      "/dst",
		PreHook:         renderedPre,
		PostHook:        renderedPost,
	}).Error; err != nil {
		t.Fatalf("create policy: %v", err)
	}

	r := setupCredentialRouter(db)
	r.PUT("/app-credentials/:id", h.Update)

	body := `{"type":"mysql","name":"cascade-no-pw","host":"10.0.0.2","user":"root"}`
	req := httptest.NewRequest("PUT", "/app-credentials/1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var p model.Policy
	if err := db.First(&p, 1).Error; err != nil {
		t.Fatalf("load policy: %v", err)
	}
	if p.PreHook != "" || p.PostHook != "" {
		t.Fatalf("legacy auto-rendered hooks should be cleared and re-rendered at runtime, pre=%q post=%q", p.PreHook, p.PostHook)
	}
	if strings.Contains(w.Body.String(), `"password"`) {
		t.Fatalf("credential update response leaked password field: %s", w.Body.String())
	}
}

func TestAppCredentialDeleteWithRefs(t *testing.T) {
	db := setupCredentialTestDB(t)
	h := NewAppCredentialHandler(db)

	db.Create(&model.AppCredential{Name: "refed-cred", Type: "mysql", Config: `{}`})
	db.Create(&model.Policy{Name: "refed-policy", AppCredentialID: uintPtr(1), SourcePath: "/src", TargetPath: "/dst", CronSpec: "0 0 * * *"})

	r := setupCredentialRouter(db)
	r.DELETE("/app-credentials/:id", h.Delete)

	req := httptest.NewRequest("DELETE", "/app-credentials/1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 409 {
		t.Fatalf("expected 409 conflict, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAppCredentialDeleteNoRefs(t *testing.T) {
	db := setupCredentialTestDB(t)
	h := NewAppCredentialHandler(db)

	db.Create(&model.AppCredential{Name: "no-ref", Type: "mysql", Config: `{}`})

	r := setupCredentialRouter(db)
	r.DELETE("/app-credentials/:id", h.Delete)

	req := httptest.NewRequest("DELETE", "/app-credentials/1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var count int64
	db.Model(&model.AppCredential{}).Count(&count)
	if count != 0 {
		t.Error("credential should be deleted")
	}
}

func TestAppCredentialGetNotFound(t *testing.T) {
	db := setupCredentialTestDB(t)
	h := NewAppCredentialHandler(db)

	r := setupCredentialRouter(db)
	r.GET("/app-credentials/:id", h.Get)

	req := httptest.NewRequest("GET", "/app-credentials/999", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestAppCredentialListProfiles(t *testing.T) {
	db := setupCredentialTestDB(t)
	h := NewAppCredentialHandler(db)

	r := setupCredentialRouter(db)
	r.GET("/app-credentials/profiles", h.ListProfiles)

	req := httptest.NewRequest("GET", "/app-credentials/profiles", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	items, ok := resp.Data.([]interface{})
	if !ok {
		t.Fatalf("expected array data, got %T", resp.Data)
	}
	if len(items) != 8 {
		t.Fatalf("expected 8 profiles, got %d", len(items))
	}

	// 验证每个 profile 含有必要字段（schema 可用于前端表单渲染）
	for _, item := range items {
		it := item.(map[string]interface{})
		if it["id"] == nil || it["id"] == "" {
			t.Error("profile should have id")
		}
		if it["name"] == nil || it["name"] == "" {
			t.Error("profile should have name")
		}
		schema, ok := it["config_schema"].([]interface{})
		if !ok || len(schema) == 0 {
			t.Errorf("profile %v should have config_schema", it["id"])
		}
		// 验证模板字段不透出
		if _, exists := it["pre_hook_template"]; exists {
			t.Error("profile response should not expose pre_hook_template")
		}
	}

	// 验证 host profile 有 host/port/user/password schema
	for _, item := range items {
		it := item.(map[string]interface{})
		id := it["id"].(string)
		schema := it["config_schema"].([]interface{})
		schemaKeys := make(map[string]bool)
		for _, f := range schema {
			fm := f.(map[string]interface{})
			schemaKeys[fm["key"].(string)] = true
		}
		if id == "mysql" || id == "postgres" || id == "mongodb" || id == "redis" {
			if !schemaKeys["host"] {
				t.Errorf("host profile %s should have host in config_schema", id)
			}
		}
		if id == "docker-mysql" || id == "docker-postgres" || id == "docker-mongodb" || id == "docker-redis" {
			if !schemaKeys["container_name"] {
				t.Errorf("docker profile %s should have container_name in config_schema", id)
			}
		}
	}
}

func TestAppCredentialUpdateClearsLegacyGeneratedHooks(t *testing.T) {
	db := setupCredentialTestDB(t)
	h := NewAppCredentialHandler(db)

	// Step 1: 创建 credential（旧配置）
	oldCfg := map[string]interface{}{
		"host":     "10.0.0.1",
		"port":     "3306",
		"user":     "root",
		"password": "FAKE_OLD_CASCADE_PW_FOR_TEST_ONLY",
	}
	oldCfgJSON, _ := json.Marshal(oldCfg)
	db.Create(&model.AppCredential{Name: "cascade-cred", Type: "mysql", Config: string(oldCfgJSON)})

	// Step 2: 创建 policy，引用此 credential，使用 mysql profile
	// 先用 profile 渲染 hooks
	renderedPre, renderedPost, err := profile.RenderHooks("mysql", oldCfg)
	if err != nil {
		t.Fatalf("RenderHooks: %v", err)
	}
	db.Create(&model.Policy{
		Name:            "cascade-policy",
		AppProfile:      "mysql",
		AppCredentialID: uintPtr(1),
		SourcePath:      "/src",
		CronSpec:        "0 0 * * *",
		TargetPath:      "/dst",
		PreHook:         renderedPre,
		PostHook:        renderedPost,
	})

	// Step 3: 更新 credential（改密码）
	r := setupCredentialRouter(db)
	r.PUT("/app-credentials/:id", h.Update)

	body := `{"type":"mysql","name":"cascade-cred","host":"10.0.0.1","port":"3306","user":"root","password":"FAKE_NEW_CASCADE_PW_FOR_TEST_ONLY"}`
	req := httptest.NewRequest("PUT", "/app-credentials/1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Step 4: 验证旧版自动生成的 policy hook 已清空，后续任务执行时按最新凭据运行时渲染
	var p model.Policy
	db.First(&p, 1)
	if p.PreHook != "" || p.PostHook != "" {
		t.Fatalf("legacy auto-rendered hooks should be cleared instead of rewritten, pre=%q post=%q", p.PreHook, p.PostHook)
	}
}

func TestAppCredentialUpdateCascadeUserOverride(t *testing.T) {
	db := setupCredentialTestDB(t)
	h := NewAppCredentialHandler(db)

	// Step 1: 创建 credential
	cfg := map[string]interface{}{"host": "10.0.0.1", "password": "FAKE_OVERRIDE_PW_FOR_TEST_ONLY"}
	cfgJSON, _ := json.Marshal(cfg)
	db.Create(&model.AppCredential{Name: "override-cred", Type: "mysql", Config: string(cfgJSON)})

	// Step 2: 创建 policy，但手动设置 hook（用户 override）
	db.Create(&model.Policy{
		Name:            "override-policy",
		AppProfile:      "mysql",
		AppCredentialID: uintPtr(1),
		SourcePath:      "/src",
		CronSpec:        "0 0 * * *",
		TargetPath:      "/dst",
		PreHook:         "echo 'custom pre hook'",
		PostHook:        "echo 'custom post hook'",
	})

	// Step 3: 更新 credential（改密码）
	r := setupCredentialRouter(db)
	r.PUT("/app-credentials/:id", h.Update)

	body := `{"type":"mysql","name":"override-cred","host":"10.0.0.2","password":"FAKE_NEW_OVERRIDE_PW_FOR_TEST_ONLY"}`
	req := httptest.NewRequest("PUT", "/app-credentials/1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Step 4: 验证 policy 的 hook 未被修改（用户 override 保留）
	var p model.Policy
	db.First(&p, 1)
	if p.PreHook != "echo 'custom pre hook'" {
		t.Errorf("policy pre-hook should not be overwritten, got: %s", p.PreHook)
	}
	if p.PostHook != "echo 'custom post hook'" {
		t.Errorf("policy post-hook should not be overwritten, got: %s", p.PostHook)
	}
}

func uintPtr(v uint) *uint {
	return &v
}
