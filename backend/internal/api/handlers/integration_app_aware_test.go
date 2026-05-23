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

// setupIntegrationAppAwareTestDB 创建包含 AppCredential + Policy 表的内存数据库。
func setupIntegrationAppAwareTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
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
	if err := db.AutoMigrate(&model.AppCredential{}, &model.Policy{}, &model.RestoreDrillEvidence{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return db
}

// setupIntegrationAppAwareRouter 创建测试路由并注入 admin role。
func setupIntegrationAppAwareRouter(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("db", db)
		c.Set("role", "admin")
		c.Next()
	})
	return r
}

// ================================================================================
// Test 1: Full pipeline — host mysql profile
// ================================================================================

func TestIntegrationFullPipelineHostMySQL(t *testing.T) {
	db := setupIntegrationAppAwareTestDB(t)
	credHandler := NewAppCredentialHandler(db)
	policyHandler := NewPolicyHandler(db, nil)

	r := setupIntegrationAppAwareRouter(db)
	r.POST("/app-credentials", credHandler.Create)
	r.POST("/policies", policyHandler.Create)
	r.GET("/policies/:id", policyHandler.Get)

	// Step 1: 创建 mysql 类型凭据
	credBody := `{"type":"mysql","name":"mysql-prod","host":"10.0.0.1","port":"3306","user":"root","password":"FAKE_INTEGRATION_MYSQL_PW_FOR_TEST_ONLY"}`
	credReq := httptest.NewRequest("POST", "/app-credentials", strings.NewReader(credBody))
	credReq.Header.Set("Content-Type", "application/json")
	credW := httptest.NewRecorder()
	r.ServeHTTP(credW, credReq)

	if credW.Code != 201 {
		t.Fatalf("credential create: expected 201, got %d: %s", credW.Code, credW.Body.String())
	}

	// 验证凭据响应不含 password
	var credResp Response
	if err := json.Unmarshal(credW.Body.Bytes(), &credResp); err != nil {
		t.Fatalf("unmarshal credential: %v", err)
	}
	credData := credResp.Data.(map[string]interface{})
	cfg, _ := credData["config"].(map[string]interface{})
	if _, exists := cfg["password"]; exists {
		t.Error("API response should not contain password in config")
	}
	if hp, _ := credData["has_password"].(bool); !hp {
		t.Error("has_password should be true")
	}

	// Step 2: 创建 Policy，指定 app_profile=mysql
	policyBody := `{"name":"mysql-backup","source_path":"/data/mysql","cron_spec":"0 2 * * *","app_profile":"mysql","app_credential_id":1,"retention_days":7,"max_concurrent":1}`
	policyReq := httptest.NewRequest("POST", "/policies", strings.NewReader(policyBody))
	policyReq.Header.Set("Content-Type", "application/json")
	policyW := httptest.NewRecorder()
	r.ServeHTTP(policyW, policyReq)

	if policyW.Code != 201 {
		t.Fatalf("policy create: expected 201, got %d: %s", policyW.Code, policyW.Body.String())
	}

	// Step 3: 验证 policy 的 pre_hook / post_hook
	var policyResp Response
	if err := json.Unmarshal(policyW.Body.Bytes(), &policyResp); err != nil {
		t.Fatalf("unmarshal policy: %v", err)
	}
	policyData := policyResp.Data.(map[string]interface{})

	preHook, _ := policyData["pre_hook"].(string)
	postHook, _ := policyData["post_hook"].(string)
	appProfile, _ := policyData["app_profile"].(string)
	appCredID := policyData["app_credential_id"]

	if appProfile != "mysql" {
		t.Errorf("expected app_profile='mysql', got '%s'", appProfile)
	}
	if appCredID == nil {
		t.Error("app_credential_id should be set")
	}

	// pre_hook 应包含渲染后的 mysqldump 命令
	if !strings.Contains(preHook, "mysqldump") {
		t.Error("pre-hook should contain mysqldump")
	}
	if !strings.Contains(preHook, "-u root") {
		t.Error("pre-hook should contain -u root")
	}
	if !strings.Contains(preHook, "-h 10.0.0.1") {
		t.Error("pre-hook should contain -h 10.0.0.1")
	}
	if !strings.Contains(preHook, "-P 3306") {
		t.Error("pre-hook should contain -P 3306")
	}
	if !strings.Contains(preHook, "--single-transaction") {
		t.Error("pre-hook should contain --single-transaction")
	}
	if !strings.Contains(preHook, "--all-databases") {
		t.Error("pre-hook should contain --all-databases")
	}
	// 密码必须出现在渲染后的 hook 中（设计如此：hook 在目标节点以明文执行）
	if !strings.Contains(preHook, "FAKE_INTEGRATION_MYSQL_PW_FOR_TEST_ONLY") {
		t.Errorf("pre-hook should contain password in rendered command, got: %s", preHook)
	}

	// post_hook 应包含清理命令
	if !strings.Contains(postHook, "rm -f /tmp/xirang-mysql-backup.sql") {
		t.Errorf("post-hook should contain cleanup, got: %s", postHook)
	}

	// Step 4: GET /policies/:id 验证返回同样内容
	getReq := httptest.NewRequest("GET", "/policies/1", nil)
	getW := httptest.NewRecorder()
	r.ServeHTTP(getW, getReq)
	if getW.Code != 200 {
		t.Fatalf("policy get: expected 200, got %d", getW.Code)
	}
	var getResp Response
	_ = json.Unmarshal(getW.Body.Bytes(), &getResp)
	getData := getResp.Data.(map[string]interface{})
	getPreHook := getData["pre_hook"].(string)
	if !strings.Contains(getPreHook, "mysqldump") {
		t.Error("GET /policies/:id should return rendered pre_hook")
	}
}

// ================================================================================
// Test 2: Full pipeline — docker-mysql profile
// ================================================================================

func TestIntegrationFullPipelineDockerMySQL(t *testing.T) {
	db := setupIntegrationAppAwareTestDB(t)
	credHandler := NewAppCredentialHandler(db)
	policyHandler := NewPolicyHandler(db, nil)

	r := setupIntegrationAppAwareRouter(db)
	r.POST("/app-credentials", credHandler.Create)
	r.POST("/policies", policyHandler.Create)

	// Step 1: 创建 docker-mysql 类型凭据（必须提供 container_name）
	credBody := `{"type":"docker-mysql","name":"docker-mysql-prod","container_name":"my-mysql","user":"root","password":"FAKE_INTEGRATION_DOCKER_MYSQL_PW_FOR_TEST_ONLY"}`
	credReq := httptest.NewRequest("POST", "/app-credentials", strings.NewReader(credBody))
	credReq.Header.Set("Content-Type", "application/json")
	credW := httptest.NewRecorder()
	r.ServeHTTP(credW, credReq)

	if credW.Code != 201 {
		t.Fatalf("credential create: expected 201, got %d: %s", credW.Code, credW.Body.String())
	}

	// Step 2: 创建 Policy，指定 app_profile=docker-mysql
	policyBody := `{"name":"docker-mysql-backup","source_path":"/data/docker-mysql","cron_spec":"0 3 * * *","app_profile":"docker-mysql","app_credential_id":1,"retention_days":7,"max_concurrent":1}`
	policyReq := httptest.NewRequest("POST", "/policies", strings.NewReader(policyBody))
	policyReq.Header.Set("Content-Type", "application/json")
	policyW := httptest.NewRecorder()
	r.ServeHTTP(policyW, policyReq)

	if policyW.Code != 201 {
		t.Fatalf("policy create: expected 201, got %d: %s", policyW.Code, policyW.Body.String())
	}

	// Step 3: 验证容器存在性预校验注入 + docker exec
	var policyResp Response
	if err := json.Unmarshal(policyW.Body.Bytes(), &policyResp); err != nil {
		t.Fatalf("unmarshal policy: %v", err)
	}
	policyData := policyResp.Data.(map[string]interface{})
	preHook, _ := policyData["pre_hook"].(string)
	postHook, _ := policyData["post_hook"].(string)

	// 容器存在性预校验（#9）—— pre-hook 第一行必须是 docker inspect
	trimmedPre := strings.TrimSpace(preHook)
	if !strings.HasPrefix(trimmedPre, "docker inspect my-mysql") {
		t.Errorf("docker pre-hook should start with 'docker inspect' for existence check, got: %s", trimmedPre[:min(50, len(trimmedPre))])
	}
	if !strings.Contains(trimmedPre, "容器 my-mysql 不存在或未运行") {
		t.Error("docker pre-hook should contain Chinese error message for missing container")
	}

	// docker exec 命令
	if !strings.Contains(preHook, "docker exec my-mysql mysqldump") {
		t.Errorf("pre-hook should contain 'docker exec my-mysql mysqldump', got: %s", preHook)
	}

	// 用户名和密码
	if !strings.Contains(preHook, "-u root") {
		t.Error("pre-hook should contain -u root")
	}
	if !strings.Contains(preHook, "FAKE_INTEGRATION_DOCKER_MYSQL_PW_FOR_TEST_ONLY") {
		t.Error("pre-hook should contain password")
	}

	// 清理命令
	if !strings.Contains(postHook, "rm -f /tmp/xirang-docker-mysql-backup.sql") {
		t.Errorf("post-hook should contain cleanup, got: %s", postHook)
	}
}

// ================================================================================
// Test 3: User hook override — 用户提供的 hook 不被自动渲染覆盖
// ================================================================================

func TestIntegrationUserHookOverride(t *testing.T) {
	db := setupIntegrationAppAwareTestDB(t)
	credHandler := NewAppCredentialHandler(db)
	policyHandler := NewPolicyHandler(db, nil)

	r := setupIntegrationAppAwareRouter(db)
	r.POST("/app-credentials", credHandler.Create)
	r.POST("/policies", policyHandler.Create)
	r.GET("/policies/:id", policyHandler.Get)

	// Step 1: 创建凭据
	credBody := `{"type":"mysql","name":"override-test-cred","host":"10.0.0.1","password":"FAKE_INTEGRATION_OVERRIDE_PW_FOR_TEST_ONLY"}`
	credReq := httptest.NewRequest("POST", "/app-credentials", strings.NewReader(credBody))
	credReq.Header.Set("Content-Type", "application/json")
	credW := httptest.NewRecorder()
	r.ServeHTTP(credW, credReq)
	if credW.Code != 201 {
		t.Fatalf("credential create: expected 201, got %d", credW.Code)
	}

	// Step 2: 创建 Policy，同时提供 app_profile 和手动 pre_hook
	// 用户提供的 hook 应被保留（user override 优先）
	policyBody := `{"name":"override-policy","source_path":"/data/override","cron_spec":"0 4 * * *","app_profile":"mysql","app_credential_id":1,"pre_hook":"echo custom backup start","post_hook":"echo custom backup end","retention_days":7,"max_concurrent":1}`
	policyReq := httptest.NewRequest("POST", "/policies", strings.NewReader(policyBody))
	policyReq.Header.Set("Content-Type", "application/json")
	policyW := httptest.NewRecorder()
	r.ServeHTTP(policyW, policyReq)

	if policyW.Code != 201 {
		t.Fatalf("policy create: expected 201, got %d: %s", policyW.Code, policyW.Body.String())
	}

	var policyResp Response
	if err := json.Unmarshal(policyW.Body.Bytes(), &policyResp); err != nil {
		t.Fatalf("unmarshal policy: %v", err)
	}
	policyData := policyResp.Data.(map[string]interface{})

	preHook := policyData["pre_hook"].(string)
	postHook := policyData["post_hook"].(string)

	// 用户提供的 pre_hook 必须保留，不能被自动渲染覆盖
	if preHook != "echo custom backup start" {
		t.Errorf("pre_hook should preserve user override, got: %s", preHook)
	}
	// 用户提供的 post_hook 同样保留
	if postHook != "echo custom backup end" {
		t.Errorf("post_hook should preserve user override, got: %s", postHook)
	}

	// 不应包含自动渲染的 mysqldump
	if strings.Contains(preHook, "mysqldump") {
		t.Error("user-overridden pre_hook should NOT contain auto-rendered mysqldump")
	}

	// 验证 app_profile 仍然正确存储
	if appProfile, _ := policyData["app_profile"].(string); appProfile != "mysql" {
		t.Errorf("app_profile should be 'mysql', got '%s'", appProfile)
	}

	// Step 3: GET 验证
	getReq := httptest.NewRequest("GET", "/policies/1", nil)
	getW := httptest.NewRecorder()
	r.ServeHTTP(getW, getReq)
	if getW.Code != 200 {
		t.Fatalf("policy get: expected 200, got %d", getW.Code)
	}
	var getResp Response
	_ = json.Unmarshal(getW.Body.Bytes(), &getResp)
	getData := getResp.Data.(map[string]interface{})
	if getData["pre_hook"].(string) != "echo custom backup start" {
		t.Error("GET should return user-overridden pre_hook")
	}
}

// ================================================================================
// Test 4: 部分用户覆盖 — pre_hook 手动提供但 post_hook 自动渲染
// ================================================================================

func TestIntegrationPartialUserOverride(t *testing.T) {
	db := setupIntegrationAppAwareTestDB(t)
	credHandler := NewAppCredentialHandler(db)
	policyHandler := NewPolicyHandler(db, nil)

	r := setupIntegrationAppAwareRouter(db)
	r.POST("/app-credentials", credHandler.Create)
	r.POST("/policies", policyHandler.Create)

	// Step 1: 创建凭据
	credBody := `{"type":"mysql","name":"partial-cred","host":"10.0.0.1","password":"FAKE_INTEGRATION_PARTIAL_PW_FOR_TEST_ONLY"}`
	credReq := httptest.NewRequest("POST", "/app-credentials", strings.NewReader(credBody))
	credReq.Header.Set("Content-Type", "application/json")
	credW := httptest.NewRecorder()
	r.ServeHTTP(credW, credReq)
	if credW.Code != 201 {
		t.Fatalf("credential create: expected 201, got %d", credW.Code)
	}

	// Step 2: 仅提供 pre_hook（不提供 post_hook）
	// pre_hook 应保留用户值，post_hook 应自动渲染
	policyBody := `{"name":"partial-override","source_path":"/data/partial","cron_spec":"0 5 * * *","app_profile":"mysql","app_credential_id":1,"pre_hook":"echo manual pre","retention_days":7,"max_concurrent":1}`
	policyReq := httptest.NewRequest("POST", "/policies", strings.NewReader(policyBody))
	policyReq.Header.Set("Content-Type", "application/json")
	policyW := httptest.NewRecorder()
	r.ServeHTTP(policyW, policyReq)

	if policyW.Code != 201 {
		t.Fatalf("policy create: expected 201, got %d: %s", policyW.Code, policyW.Body.String())
	}

	var policyResp Response
	if err := json.Unmarshal(policyW.Body.Bytes(), &policyResp); err != nil {
		t.Fatalf("unmarshal policy: %v", err)
	}
	policyData := policyResp.Data.(map[string]interface{})
	preHook := policyData["pre_hook"].(string)
	postHook := policyData["post_hook"].(string)

	if preHook != "echo manual pre" {
		t.Errorf("pre_hook should preserve user value, got: %s", preHook)
	}
	if !strings.Contains(postHook, "rm -f /tmp/xirang-mysql-backup.sql") {
		t.Errorf("post_hook should be auto-rendered (user didn't override), got: %s", postHook)
	}
}

// ================================================================================
// Test 5: Policy without app_profile stays backward compatible
// ================================================================================

func TestIntegrationPolicyWithoutProfileBackwardCompat(t *testing.T) {
	db := setupIntegrationAppAwareTestDB(t)
	policyHandler := NewPolicyHandler(db, nil)

	r := setupIntegrationAppAwareRouter(db)
	r.POST("/policies", policyHandler.Create)

	// 创建不带 app_profile 的 Policy（应完全向后兼容）
	policyBody := `{"name":"legacy-policy","source_path":"/data/legacy","cron_spec":"0 6 * * *","retention_days":7,"max_concurrent":1}`
	policyReq := httptest.NewRequest("POST", "/policies", strings.NewReader(policyBody))
	policyReq.Header.Set("Content-Type", "application/json")
	policyW := httptest.NewRecorder()
	r.ServeHTTP(policyW, policyReq)

	if policyW.Code != 201 {
		t.Fatalf("policy create: expected 201, got %d: %s", policyW.Code, policyW.Body.String())
	}

	var policyResp Response
	_ = json.Unmarshal(policyW.Body.Bytes(), &policyResp)
	policyData := policyResp.Data.(map[string]interface{})

	if appProfile, _ := policyData["app_profile"].(string); appProfile != "" {
		t.Errorf("legacy policy should have empty app_profile, got '%s'", appProfile)
	}
	if appCredID, ok := policyData["app_credential_id"]; ok && appCredID != nil {
		t.Error("legacy policy should have null app_credential_id")
	}
	if preHook, _ := policyData["pre_hook"].(string); preHook != "" {
		t.Errorf("legacy policy should have empty pre_hook, got '%s'", preHook)
	}
}

// ================================================================================
// Test 6: All 8 profiles render without error
// ================================================================================

func TestIntegrationAllEightProfilesRender(t *testing.T) {
	tests := []struct {
		id     string
		config map[string]interface{}
	}{
		{
			id: "mysql",
			config: map[string]interface{}{
				"host": "10.0.0.1", "port": "3306",
				"user": "root", "password": "FAKE_RENDER_PW_FOR_TEST_ONLY",
			},
		},
		{
			id: "postgres",
			config: map[string]interface{}{
				"host": "10.0.0.2", "port": "5432",
				"user": "postgres", "password": "FAKE_RENDER_PW_FOR_TEST_ONLY",
			},
		},
		{
			id: "mongodb",
			config: map[string]interface{}{
				"host": "10.0.0.3", "port": "27017",
				"user": "admin", "password": "FAKE_RENDER_PW_FOR_TEST_ONLY",
			},
		},
		{
			id: "redis",
			config: map[string]interface{}{
				"host": "10.0.0.4", "port": "6379",
				"password": "FAKE_RENDER_PW_FOR_TEST_ONLY",
			},
		},
		{
			id: "docker-mysql",
			config: map[string]interface{}{
				"container_name": "my-mysql",
				"user":           "root",
				"password":       "FAKE_RENDER_PW_FOR_TEST_ONLY",
			},
		},
		{
			id: "docker-postgres",
			config: map[string]interface{}{
				"container_name": "my-pg",
				"user":           "postgres",
			},
		},
		{
			id: "docker-mongodb",
			config: map[string]interface{}{
				"container_name": "my-mongo",
				"user":           "admin",
				"password":       "FAKE_RENDER_PW_FOR_TEST_ONLY",
			},
		},
		{
			id: "docker-redis",
			config: map[string]interface{}{
				"container_name": "my-redis",
				"password":       "FAKE_RENDER_PW_FOR_TEST_ONLY",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			pre, post, err := profile.RenderHooks(tc.id, tc.config)
			if err != nil {
				t.Fatalf("RenderHooks(%s): unexpected error: %v", tc.id, err)
			}
			if pre == "" {
				t.Errorf("RenderHooks(%s): pre-hook should not be empty", tc.id)
			}
			if post == "" {
				t.Errorf("RenderHooks(%s): post-hook should not be empty", tc.id)
			}

			// 验证 profile 定义存在
			p, ok := profile.GetProfile(tc.id)
			if !ok {
				t.Errorf("GetProfile(%s): not found", tc.id)
				return
			}

			// docker profile 必须包含容器存在性预校验
			if p.IsDocker {
				if !strings.Contains(pre, "docker inspect") {
					t.Errorf("docker profile %s: pre-hook should contain docker inspect", tc.id)
				}
			}

			// host profile 不应包含 docker 命令
			if !p.IsDocker {
				if strings.HasPrefix(strings.TrimSpace(pre), "docker inspect") {
					t.Errorf("host profile %s: pre-hook should NOT contain docker inspect", tc.id)
				}
			}
		})
	}
}

// ================================================================================
// Test 7: Policy with app_profile but missing credential returns error
// ================================================================================

func TestIntegrationPolicyAppProfileMissingCredential(t *testing.T) {
	db := setupIntegrationAppAwareTestDB(t)
	policyHandler := NewPolicyHandler(db, nil)

	r := setupIntegrationAppAwareRouter(db)
	r.POST("/policies", policyHandler.Create)

	// 指定 app_profile 但不提供 app_credential_id
	policyBody := `{"name":"no-cred-policy","source_path":"/data/nocred","cron_spec":"0 7 * * *","app_profile":"mysql","retention_days":7,"max_concurrent":1}`
	policyReq := httptest.NewRequest("POST", "/policies", strings.NewReader(policyBody))
	policyReq.Header.Set("Content-Type", "application/json")
	policyW := httptest.NewRecorder()
	r.ServeHTTP(policyW, policyReq)

	if policyW.Code != 400 {
		t.Fatalf("expected 400 for missing credential, got %d: %s", policyW.Code, policyW.Body.String())
	}
}

// ================================================================================
// Test 8: Policy with unknown app_profile returns error
// ================================================================================

func TestIntegrationPolicyUnknownAppProfile(t *testing.T) {
	db := setupIntegrationAppAwareTestDB(t)
	policyHandler := NewPolicyHandler(db, nil)

	r := setupIntegrationAppAwareRouter(db)
	r.POST("/policies", policyHandler.Create)

	policyBody := `{"name":"bad-profile","source_path":"/data/bad","cron_spec":"0 8 * * *","app_profile":"oracle","app_credential_id":1,"retention_days":7,"max_concurrent":1}`
	policyReq := httptest.NewRequest("POST", "/policies", strings.NewReader(policyBody))
	policyReq.Header.Set("Content-Type", "application/json")
	policyW := httptest.NewRecorder()
	r.ServeHTTP(policyW, policyReq)

	if policyW.Code != 400 {
		t.Fatalf("expected 400 for unknown profile, got %d: %s", policyW.Code, policyW.Body.String())
	}
}

// ================================================================================
// Test 9: Verify password appears in rendered hook (design: shell execution needs it)
// ================================================================================

func TestIntegrationPasswordInRenderedHook(t *testing.T) {
	db := setupIntegrationAppAwareTestDB(t)
	credHandler := NewAppCredentialHandler(db)
	policyHandler := NewPolicyHandler(db, nil)

	r := setupIntegrationAppAwareRouter(db)
	r.POST("/app-credentials", credHandler.Create)
	r.POST("/policies", policyHandler.Create)

	// 创建含密码的凭据
	credBody := `{"type":"mysql","name":"pw-check","host":"10.0.0.5","password":"FAKE_PASSWORD_FOR_TEST_ONLY"}`
	credReq := httptest.NewRequest("POST", "/app-credentials", strings.NewReader(credBody))
	credReq.Header.Set("Content-Type", "application/json")
	credW := httptest.NewRecorder()
	r.ServeHTTP(credW, credReq)
	if credW.Code != 201 {
		t.Fatalf("credential create: expected 201, got %d", credW.Code)
	}

	// API 响应不应暴露 password
	var credResp Response
	_ = json.Unmarshal(credW.Body.Bytes(), &credResp)
	credData := credResp.Data.(map[string]interface{})
	cfg := credData["config"].(map[string]interface{})
	if _, exists := cfg["password"]; exists {
		t.Error("credential API response should not contain password in config")
	}

	// 但渲染到 hook 中必须包含密码（设计如此）
	policyBody := `{"name":"pw-check-policy","source_path":"/data/pwcheck","cron_spec":"0 9 * * *","app_profile":"mysql","app_credential_id":1,"retention_days":7,"max_concurrent":1}`
	policyReq := httptest.NewRequest("POST", "/policies", strings.NewReader(policyBody))
	policyReq.Header.Set("Content-Type", "application/json")
	policyW := httptest.NewRecorder()
	r.ServeHTTP(policyW, policyReq)

	if policyW.Code != 201 {
		t.Fatalf("policy create: expected 201, got %d", policyW.Code)
	}

	var policyResp Response
	_ = json.Unmarshal(policyW.Body.Bytes(), &policyResp)
	policyData := policyResp.Data.(map[string]interface{})
	preHook := policyData["pre_hook"].(string)

	if !strings.Contains(preHook, "FAKE_PASSWORD_FOR_TEST_ONLY") {
		t.Errorf("password should appear in rendered pre-hook, got: %s", preHook)
	}
}

// ================================================================================
// Test 10: POST /app-credentials with missing container_name for docker type
// ================================================================================

func TestIntegrationCredentialCreateDockerMissingContainerName(t *testing.T) {
	db := setupIntegrationAppAwareTestDB(t)
	credHandler := NewAppCredentialHandler(db)

	r := setupIntegrationAppAwareRouter(db)
	r.POST("/app-credentials", credHandler.Create)

	body := `{"type":"docker-mysql","name":"no-container","user":"root"}`
	req := httptest.NewRequest("POST", "/app-credentials", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Fatalf("expected 400 for docker type missing container_name, got %d: %s", w.Code, w.Body.String())
	}
}
