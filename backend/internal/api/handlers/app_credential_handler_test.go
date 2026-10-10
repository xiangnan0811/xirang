package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
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

type appCredentialReferencesTestEnvelope struct {
	Code     int                              `json:"code"`
	Message  string                           `json:"message"`
	Data     []appCredentialReferenceResponse `json:"data"`
	Total    int64                            `json:"total"`
	Page     int                              `json:"page"`
	PageSize int                              `json:"page_size"`
}

func seedReferenceCredential(t *testing.T, db *gorm.DB, name, config string) model.AppCredential {
	t.Helper()
	credential := model.AppCredential{
		Name:   name,
		Type:   "mysql",
		Config: config,
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&credential).Error; err != nil {
		t.Fatalf("create reference credential: %v", err)
	}
	return credential
}

func seedReferencePolicy(t *testing.T, db *gorm.DB, name string, credentialID *uint, appProfile, preHook, postHook string) model.Policy {
	t.Helper()
	policy := model.Policy{
		Name:            name,
		SourcePath:      "/src",
		TargetPath:      "/dst",
		CronSpec:        "0 0 * * *",
		AppProfile:      appProfile,
		AppCredentialID: credentialID,
		PreHook:         preHook,
		PostHook:        postHook,
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&policy).Error; err != nil {
		t.Fatalf("create reference policy: %v", err)
	}
	return policy
}

func requestCredentialReferences(t *testing.T, router *gin.Engine, path string) (*httptest.ResponseRecorder, appCredentialReferencesTestEnvelope) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	var envelope appCredentialReferencesTestEnvelope
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode credential references response: %v; body=%s", err, resp.Body.String())
	}
	return resp, envelope
}

func isFullCredentialOrPolicyDestination(dest interface{}) bool {
	typ := reflect.TypeOf(dest)
	for typ != nil && (typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice) {
		typ = typ.Elem()
	}
	return typ == reflect.TypeOf(model.AppCredential{}) || typ == reflect.TypeOf(model.Policy{})
}

func TestAppCredentialReferencesPaginationAndSafeProjection(t *testing.T) {
	db := setupCredentialTestDB(t)
	credential := seedReferenceCredential(t, db, "reference-target", "enc:v2:FAKE_INVALID_CREDENTIAL_CIPHERTEXT_FOR_TEST_ONLY")
	otherCredential := seedReferenceCredential(t, db, "reference-other", `{}`)
	const preHookSecret = "enc:v2:FAKE_POLICY_PRE_HOOK_CIPHERTEXT_FOR_TEST_ONLY"
	const postHookSecret = "enc:v2:FAKE_POLICY_POST_HOOK_CIPHERTEXT_FOR_TEST_ONLY"

	policyIDs := make([]uint, 0, 45)
	for i := 1; i <= 45; i++ {
		appProfile := ""
		preHook, postHook := "", ""
		if i == 1 {
			appProfile = "mysql"
			preHook, postHook = preHookSecret, postHookSecret
		}
		policy := seedReferencePolicy(t, db, fmt.Sprintf("reference-policy-%02d", i), uintPtr(credential.ID), appProfile, preHook, postHook)
		policyIDs = append(policyIDs, policy.ID)
	}
	seedReferencePolicy(t, db, "other-credential-policy", uintPtr(otherCredential.ID), "mysql", "", "")

	r := setupCredentialRouter(db)
	h := NewAppCredentialHandler(db)
	r.GET("/app-credentials/:id/references", h.References)
	callbackName := "test:app-credential-references-reject-full-model-destination"
	if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || !isFullCredentialOrPolicyDestination(tx.Statement.Dest) {
			return
		}
		_ = tx.AddError(fmt.Errorf("reference endpoint read a full credential or policy model"))
	}); err != nil {
		t.Fatalf("register full-model destination callback: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(callbackName)
	})
	_, defaultPage := requestCredentialReferences(t, r, fmt.Sprintf("/app-credentials/%d/references", credential.ID))
	if defaultPage.Total != 45 || defaultPage.Page != 1 || defaultPage.PageSize != 20 ||
		len(defaultPage.Data) != 20 || defaultPage.Data[0].ID != policyIDs[44] {
		t.Fatalf("default reference pagination=%+v, want newest IDs first", defaultPage)
	}

	for _, testCase := range []struct {
		page     int
		wantIDs  []uint
		wantSize int
	}{
		{page: 1, wantIDs: policyIDs[:20], wantSize: 20},
		{page: 2, wantIDs: policyIDs[20:40], wantSize: 20},
		{page: 3, wantIDs: policyIDs[40:], wantSize: 5},
		{page: 4, wantIDs: []uint{}, wantSize: 0},
	} {
		path := fmt.Sprintf(
			"/app-credentials/%d/references?page=%d&page_size=20&sort_by=id&sort_order=asc",
			credential.ID, testCase.page,
		)
		resp, envelope := requestCredentialReferences(t, r, path)
		if resp.Code != http.StatusOK {
			t.Fatalf("page %d status=%d body=%s", testCase.page, resp.Code, resp.Body.String())
		}
		if envelope.Code != http.StatusOK || envelope.Message != "ok" ||
			envelope.Total != 45 || envelope.Page != testCase.page || envelope.PageSize != 20 {
			t.Fatalf("page %d envelope=%+v", testCase.page, envelope)
		}
		if len(envelope.Data) != testCase.wantSize {
			t.Fatalf("page %d returned %d items, want %d", testCase.page, len(envelope.Data), testCase.wantSize)
		}
		for i, item := range envelope.Data {
			if item.ID != testCase.wantIDs[i] || item.Name != fmt.Sprintf("reference-policy-%02d", int(testCase.wantIDs[i])) {
				t.Fatalf("page %d item %d=%+v want id=%d", testCase.page, i, item, testCase.wantIDs[i])
			}
		}
		if strings.Contains(resp.Body.String(), preHookSecret) ||
			strings.Contains(resp.Body.String(), postHookSecret) ||
			strings.Contains(resp.Body.String(), "FAKE_INVALID_CREDENTIAL") ||
			strings.Contains(resp.Body.String(), "config") ||
			strings.Contains(resp.Body.String(), "pre_hook") ||
			strings.Contains(resp.Body.String(), "post_hook") {
			t.Fatalf("reference response leaked secret fields: %s", resp.Body.String())
		}
	}

	added := seedReferencePolicy(t, db, "reference-policy-46", uintPtr(credential.ID), "", "", "")
	_, afterAdd := requestCredentialReferences(t, r, fmt.Sprintf("/app-credentials/%d/references", credential.ID))
	if afterAdd.Total != 46 {
		t.Fatalf("total after adding reference=%d, want 46", afterAdd.Total)
	}
	if err := db.Model(&model.Policy{}).Where("id = ?", added.ID).Update("app_credential_id", otherCredential.ID).Error; err != nil {
		t.Fatalf("rebind reference policy: %v", err)
	}
	_, afterRebind := requestCredentialReferences(t, r, fmt.Sprintf("/app-credentials/%d/references", credential.ID))
	if afterRebind.Total != 45 {
		t.Fatalf("total after rebinding reference=%d, want 45", afterRebind.Total)
	}
	if err := db.Delete(&model.Policy{}, policyIDs[0]).Error; err != nil {
		t.Fatalf("delete reference policy: %v", err)
	}
	_, afterDelete := requestCredentialReferences(t, r, fmt.Sprintf("/app-credentials/%d/references", credential.ID))
	if afterDelete.Total != 44 {
		t.Fatalf("total after deleting reference=%d, want 44", afterDelete.Total)
	}
}

func TestAppCredentialReferencesDoNotAuthorizeDelete(t *testing.T) {
	db := setupCredentialTestDB(t)
	credential := seedReferenceCredential(t, db, "delete-recheck", `{}`)
	r := setupCredentialRouter(db)
	h := NewAppCredentialHandler(db)
	r.GET("/app-credentials/:id/references", h.References)
	r.DELETE("/app-credentials/:id", h.Delete)

	_, before := requestCredentialReferences(t, r, fmt.Sprintf("/app-credentials/%d/references", credential.ID))
	if before.Total != 0 || before.Data == nil {
		t.Fatalf("initial references=%+v, want empty successful page", before)
	}
	seedReferencePolicy(t, db, "delete-recheck-policy", uintPtr(credential.ID), "", "", "")

	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/app-credentials/%d", credential.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusConflict {
		t.Fatalf("delete status=%d body=%s", resp.Code, resp.Body.String())
	}
	var remaining int64
	if err := db.Table("app_credentials").Where("id = ?", credential.ID).Count(&remaining).Error; err != nil {
		t.Fatalf("check credential after rejected delete: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("credential count after rejected delete=%d, want 1", remaining)
	}
}

func TestAppCredentialReferencesValidationAndDatabaseFailures(t *testing.T) {
	db := setupCredentialTestDB(t)
	credential := seedReferenceCredential(t, db, "reference-validation", `{}`)
	r := setupCredentialRouter(db)
	h := NewAppCredentialHandler(db)
	r.GET("/app-credentials/:id/references", h.References)

	for _, testCase := range []struct {
		name    string
		path    string
		want    int
		message string
	}{
		{name: "zero id", path: "/app-credentials/0/references", want: http.StatusBadRequest, message: "ID 格式错误"},
		{name: "negative id", path: "/app-credentials/-1/references", want: http.StatusBadRequest, message: "ID 格式错误"},
		{name: "non numeric id", path: "/app-credentials/nope/references", want: http.StatusBadRequest, message: "ID 格式错误"},
		{name: "overflow id", path: "/app-credentials/18446744073709551616/references", want: http.StatusBadRequest, message: "ID 格式错误"},
		{name: "unknown credential", path: "/app-credentials/999/references", want: http.StatusNotFound, message: "凭据不存在"},
	} {
		resp, _ := requestCredentialReferences(t, r, testCase.path)
		if resp.Code != testCase.want || !strings.Contains(resp.Body.String(), testCase.message) {
			t.Fatalf("%s status=%d body=%s", testCase.name, resp.Code, resp.Body.String())
		}
	}

	maxInt := int(^uint(0) >> 1)
	overflowPath := fmt.Sprintf("/app-credentials/%d/references?page=%d&page_size=500", credential.ID, maxInt)
	resp, _ := requestCredentialReferences(t, r, overflowPath)
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "分页参数不合法") {
		t.Fatalf("overflow pagination status=%d body=%s", resp.Code, resp.Body.String())
	}
	offsetOverflowPath := fmt.Sprintf("/app-credentials/%d/references?limit=1&offset=%d", credential.ID, maxInt)
	resp, _ = requestCredentialReferences(t, r, offsetOverflowPath)
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "分页参数不合法") {
		t.Fatalf("offset overflow pagination status=%d body=%s", resp.Code, resp.Body.String())
	}

	for _, testCase := range []struct {
		name     string
		query    string
		wantSize int
	}{
		{name: "invalid page size uses default", query: "?page_size=501", wantSize: 20},
		{name: "maximum page size accepted", query: "?page_size=500", wantSize: 500},
	} {
		resp, envelope := requestCredentialReferences(t, r, fmt.Sprintf("/app-credentials/%d/references%s", credential.ID, testCase.query))
		if resp.Code != http.StatusOK || envelope.PageSize != testCase.wantSize || envelope.Total != 0 || envelope.Data == nil {
			t.Fatalf("%s status=%d envelope=%+v body=%s", testCase.name, resp.Code, envelope, resp.Body.String())
		}
		if !strings.Contains(resp.Body.String(), `"data":[]`) {
			t.Fatalf("%s returned non-empty/null data: %s", testCase.name, resp.Body.String())
		}
	}

	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	cancelledRequest := httptest.NewRequestWithContext(
		cancelledContext,
		http.MethodGet,
		fmt.Sprintf("/app-credentials/%d/references", credential.ID),
		nil,
	)
	cancelledResponse := httptest.NewRecorder()
	r.ServeHTTP(cancelledResponse, cancelledRequest)
	if cancelledResponse.Code != http.StatusInternalServerError {
		t.Fatalf("cancelled request status=%d body=%s", cancelledResponse.Code, cancelledResponse.Body.String())
	}

	dbWithMissingCredentialTable := setupCredentialTestDB(t)
	missingTableCredential := seedReferenceCredential(t, dbWithMissingCredentialTable, "missing-credential-table", `{}`)
	if err := dbWithMissingCredentialTable.Migrator().DropTable(&model.AppCredential{}); err != nil {
		t.Fatalf("drop app credential table: %v", err)
	}
	missingTableRouter := setupCredentialRouter(dbWithMissingCredentialTable)
	missingTableRouter.GET("/app-credentials/:id/references", NewAppCredentialHandler(dbWithMissingCredentialTable).References)
	resp, _ = requestCredentialReferences(t, missingTableRouter, fmt.Sprintf("/app-credentials/%d/references", missingTableCredential.ID))
	if resp.Code != http.StatusInternalServerError || strings.Contains(resp.Body.String(), "no such table") {
		t.Fatalf("missing credential table status=%d body=%s", resp.Code, resp.Body.String())
	}

	dbWithMissingPolicyTable := setupCredentialTestDB(t)
	missingPolicyCredential := seedReferenceCredential(t, dbWithMissingPolicyTable, "missing-policy-table", `{}`)
	if err := dbWithMissingPolicyTable.Migrator().DropTable(&model.Policy{}); err != nil {
		t.Fatalf("drop policy table: %v", err)
	}
	missingPolicyRouter := setupCredentialRouter(dbWithMissingPolicyTable)
	missingPolicyRouter.GET("/app-credentials/:id/references", NewAppCredentialHandler(dbWithMissingPolicyTable).References)
	resp, _ = requestCredentialReferences(t, missingPolicyRouter, fmt.Sprintf("/app-credentials/%d/references", missingPolicyCredential.ID))
	if resp.Code != http.StatusInternalServerError || strings.Contains(resp.Body.String(), "no such table") {
		t.Fatalf("missing policy table status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestAppCredentialReferencesListDatabaseFailureIsGeneric(t *testing.T) {
	db := setupCredentialTestDB(t)
	credential := seedReferenceCredential(t, db, "reference-list-failure", `{}`)
	seedReferencePolicy(t, db, "reference-list-failure-policy", uintPtr(credential.ID), "", "", "")

	callbackName := "test:app-credential-reference-list-failure"
	injected := fmt.Errorf("FAKE_REFERENCE_LIST_QUERY_FAILURE_SQL_SENTINEL")
	countSeen := false
	if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != "policies" {
			return
		}
		destType := reflect.TypeOf(tx.Statement.Dest)
		if destType == reflect.TypeOf(new(int64)) {
			countSeen = true
			return
		}
		if destType != reflect.TypeOf(&[]appCredentialReferenceResponse{}) {
			return
		}
		_ = tx.AddError(injected)
	}); err != nil {
		t.Fatalf("register list failure callback: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(callbackName)
	})

	r := setupCredentialRouter(db)
	r.GET("/app-credentials/:id/references", NewAppCredentialHandler(db).References)
	resp, _ := requestCredentialReferences(t, r, fmt.Sprintf("/app-credentials/%d/references", credential.ID))
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	if !countSeen {
		t.Fatal("reference list failure did not observe a successful count query")
	}
	if strings.Contains(resp.Body.String(), injected.Error()) || strings.Contains(resp.Body.String(), "SELECT") {
		t.Fatalf("list database failure leaked SQL or sentinel: %s", resp.Body.String())
	}
}

func TestAppCredentialReferenceCountFailuresAreNotReportedAsZero(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "list", method: http.MethodGet, path: "/app-credentials"},
		{name: "get", method: http.MethodGet, path: "/app-credentials/1"},
		{name: "delete", method: http.MethodDelete, path: "/app-credentials/1"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := setupCredentialTestDB(t)
			seedReferenceCredential(t, db, "count-failure", `{}`)
			if err := db.Migrator().DropTable(&model.Policy{}); err != nil {
				t.Fatalf("drop policy table: %v", err)
			}
			r := setupCredentialRouter(db)
			h := NewAppCredentialHandler(db)
			r.GET("/app-credentials", h.List)
			r.GET("/app-credentials/:id", h.Get)
			r.DELETE("/app-credentials/:id", h.Delete)
			req := httptest.NewRequest(testCase.method, testCase.path, nil)
			resp := httptest.NewRecorder()
			r.ServeHTTP(resp, req)
			if resp.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
			}
			if strings.Contains(resp.Body.String(), `"reference_count":0`) {
				t.Fatalf("count failure was reported as zero: %s", resp.Body.String())
			}
			var remaining int64
			if err := db.Table("app_credentials").Where("id = ?", 1).Count(&remaining).Error; err != nil {
				t.Fatalf("check credential after count failure: %v", err)
			}
			if remaining != 1 {
				t.Fatalf("credential count=%d after failed %s, want 1", remaining, testCase.name)
			}
		})
	}
}

func TestAppCredentialGetAndDeleteDatabaseFailuresAreNotNotFound(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			db := setupCredentialTestDB(t)
			seedReferenceCredential(t, db, "database-failure", `{}`)
			if err := db.Migrator().DropTable(&model.AppCredential{}); err != nil {
				t.Fatalf("drop app credential table: %v", err)
			}
			r := setupCredentialRouter(db)
			h := NewAppCredentialHandler(db)
			r.GET("/app-credentials/:id", h.Get)
			r.DELETE("/app-credentials/:id", h.Delete)
			req := httptest.NewRequest(method, "/app-credentials/1", nil)
			resp := httptest.NewRecorder()
			r.ServeHTTP(resp, req)
			if resp.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
			}
			if strings.Contains(resp.Body.String(), "凭据不存在") {
				t.Fatalf("database failure was reported as not found: %s", resp.Body.String())
			}
		})
	}
}

func TestAppCredentialUpdateReferenceCountFailureRollsBackTransaction(t *testing.T) {
	db := setupCredentialTestDB(t)
	oldConfig := map[string]interface{}{
		"host":     "old-host",
		"port":     "3306",
		"user":     "root",
		"password": "FAKE_OLD_UPDATE_PASSWORD_FOR_TEST_ONLY",
	}
	oldConfigJSON, err := json.Marshal(oldConfig)
	if err != nil {
		t.Fatalf("marshal old credential config: %v", err)
	}
	credential := seedReferenceCredential(t, db, "update-before", string(oldConfigJSON))
	renderedPre, renderedPost, err := profile.RenderHooks("mysql", oldConfig)
	if err != nil {
		t.Fatalf("RenderHooks: %v", err)
	}
	policy := seedReferencePolicy(t, db, "update-before-policy", uintPtr(credential.ID), "mysql", renderedPre, renderedPost)

	callbackName := "test:app-credential-reference-count-failure"
	injected := fmt.Errorf("FAKE_REFERENCE_COUNT_FAILURE_FOR_TEST_ONLY")
	if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != "policies" {
			return
		}
		destType := reflect.TypeOf(tx.Statement.Dest)
		if destType == nil || destType.String() != "*int64" {
			return
		}
		_ = tx.AddError(injected)
	}); err != nil {
		t.Fatalf("register count failure callback: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(callbackName)
	})

	r := setupCredentialRouter(db)
	h := NewAppCredentialHandler(db)
	r.PUT("/app-credentials/:id", h.Update)
	req := httptest.NewRequest(
		http.MethodPut,
		"/app-credentials/1",
		strings.NewReader(`{"type":"mysql","name":"update-after","host":"new-host"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}

	var stored model.AppCredential
	if err := db.First(&stored, 1).Error; err != nil {
		t.Fatalf("load credential after rollback: %v", err)
	}
	if stored.Name != "update-before" {
		t.Fatalf("credential name after rollback=%q, want update-before", stored.Name)
	}
	if !strings.Contains(stored.Config, "FAKE_OLD_UPDATE_PASSWORD_FOR_TEST_ONLY") {
		t.Fatalf("credential config after rollback lost old password: %q", stored.Config)
	}
	var storedPolicy model.Policy
	if err := db.First(&storedPolicy, policy.ID).Error; err != nil {
		t.Fatalf("load policy after rollback: %v", err)
	}
	if storedPolicy.PreHook != renderedPre || storedPolicy.PostHook != renderedPost {
		t.Fatalf("policy hooks changed after rollback: pre=%q post=%q", storedPolicy.PreHook, storedPolicy.PostHook)
	}

	if err := db.Callback().Query().Remove(callbackName); err != nil {
		t.Fatalf("remove count failure callback: %v", err)
	}
	successReq := httptest.NewRequest(
		http.MethodPut,
		"/app-credentials/1",
		strings.NewReader(`{"type":"mysql","name":"update-after-success","host":"new-host","port":"3306","user":"root","password":"FAKE_NEW_UPDATE_PASSWORD_FOR_TEST_ONLY"}`),
	)
	successReq.Header.Set("Content-Type", "application/json")
	successResp := httptest.NewRecorder()
	r.ServeHTTP(successResp, successReq)
	if successResp.Code != http.StatusOK {
		t.Fatalf("successful cascade status=%d body=%s", successResp.Code, successResp.Body.String())
	}
	var cascadedPolicy model.Policy
	if err := db.First(&cascadedPolicy, policy.ID).Error; err != nil {
		t.Fatalf("load policy after successful cascade: %v", err)
	}
	if cascadedPolicy.PreHook != "" || cascadedPolicy.PostHook != "" {
		t.Fatalf("successful cascade should clear rendered hooks, pre=%q post=%q", cascadedPolicy.PreHook, cascadedPolicy.PostHook)
	}
}
