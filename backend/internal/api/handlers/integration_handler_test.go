package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"xirang/backend/internal/integration"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestValidateIntegrationEndpointRejectsInvalidScheme(t *testing.T) {
	t.Setenv("INTEGRATION_BLOCK_PRIVATE_ENDPOINTS", "false")
	if err := integration.ValidateIntegrationEndpoint("webhook", "ftp://example.com/hook"); err == nil {
		t.Fatalf("期望非 http/https endpoint 返回错误")
	}
}

func TestValidateIntegrationEndpointBlocksPrivateHostWhenEnabled(t *testing.T) {
	t.Setenv("INTEGRATION_BLOCK_PRIVATE_ENDPOINTS", "true")
	if err := integration.ValidateIntegrationEndpoint("webhook", "http://127.0.0.1/hook"); err == nil {
		t.Fatalf("期望开启私网阻断时拒绝回环地址")
	}
}

func TestValidateIntegrationEndpointAllowsPrivateHostWhenDisabled(t *testing.T) {
	t.Setenv("INTEGRATION_BLOCK_PRIVATE_ENDPOINTS", "false")
	if err := integration.ValidateIntegrationEndpoint("webhook", "http://127.0.0.1/hook"); err != nil {
		t.Fatalf("期望关闭私网阻断时允许回环地址，实际错误: %v", err)
	}
}

func TestValidateIntegrationEndpointTelegramRequiresBotTokenPath(t *testing.T) {
	t.Setenv("INTEGRATION_BLOCK_PRIVATE_ENDPOINTS", "false")
	err := integration.ValidateIntegrationEndpoint("telegram", "https://api.telegram.org/sendMessage?chat_id=1")
	if err == nil {
		t.Fatalf("期望缺少 /bot<token> 路径时返回错误")
	}
	if !strings.Contains(err.Error(), "/bot<token>") {
		t.Fatalf("期望错误提示包含 /bot<token>，实际: %v", err)
	}
}

func TestValidateIntegrationEndpointTelegramRequiresChatID(t *testing.T) {
	t.Setenv("INTEGRATION_BLOCK_PRIVATE_ENDPOINTS", "false")
	err := integration.ValidateIntegrationEndpoint("telegram", "https://api.telegram.org/bot123456:abc/sendMessage")
	if err == nil {
		t.Fatalf("期望缺少 chat_id 时返回错误")
	}
	if !strings.Contains(err.Error(), "chat_id") {
		t.Fatalf("期望错误提示包含 chat_id，实际: %v", err)
	}
}

func TestValidateIntegrationEndpointTelegramAcceptsValidEndpoint(t *testing.T) {
	t.Setenv("INTEGRATION_BLOCK_PRIVATE_ENDPOINTS", "false")
	err := integration.ValidateIntegrationEndpoint("telegram", "https://api.telegram.org/bot123456:abc/sendMessage?chat_id=-1001")
	if err != nil {
		t.Fatalf("期望合法 Telegram endpoint 校验通过，实际错误: %v", err)
	}
}

func TestIntegrationHandlerTestSuccess(t *testing.T) {
	t.Setenv("INTEGRATION_BLOCK_PRIVATE_ENDPOINTS", "false")

	var called int32
	probeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&called, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer probeServer.Close()

	db := openIntegrationHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Integration{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	item := model.Integration{
		Type:            "webhook",
		Name:            "probe-webhook",
		Endpoint:        probeServer.URL,
		Enabled:         true,
		FailThreshold:   1,
		CooldownMinutes: 1,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("创建通知通道失败: %v", err)
	}

	r := gin.New()
	handler := NewIntegrationHandler(db, integration.NewIntegrationService(db))
	r.POST("/integrations/:id/test", handler.Test)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/integrations/%d/test", item.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}
	if atomic.LoadInt32(&called) == 0 {
		t.Fatalf("期望测试发送触发 webhook 请求")
	}

	var result struct {
		Data struct {
			OK      bool   `json:"ok"`
			Message string `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !result.Data.OK {
		t.Fatalf("期望测试成功，实际响应: %s", resp.Body.String())
	}
	if !strings.Contains(result.Data.Message, "成功") {
		t.Fatalf("期望成功提示，实际: %s", result.Data.Message)
	}
}

func TestIntegrationHandlerTestNotFound(t *testing.T) {
	db := openIntegrationHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Integration{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	r := gin.New()
	handler := NewIntegrationHandler(db, integration.NewIntegrationService(db))
	r.POST("/integrations/:id/test", handler.Test)

	req := httptest.NewRequest(http.MethodPost, "/integrations/999/test", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("期望状态码 404，实际: %d", resp.Code)
	}
}

func TestIntegrationHandlerListSanitizesEndpointAndProxyURL(t *testing.T) {
	db := openIntegrationHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Integration{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	item := model.Integration{
		Type:            "webhook",
		Name:            "sensitive-webhook",
		Endpoint:        "https://hooks.example.test/services/TOKEN_A/TOKEN_B/TOKEN_C?secret=FAKE_ENDPOINT_SECRET_FOR_TEST_ONLY",
		Secret:          "FAKE_INTEGRATION_SECRET_FOR_TEST_ONLY",
		Enabled:         true,
		FailThreshold:   1,
		CooldownMinutes: 5,
		ProxyURL:        "http://proxy-user:FAKE_PROXY_PASSWORD_FOR_TEST_ONLY@proxy.example.test:8080/proxy-path?token=FAKE_PROXY_TOKEN_FOR_TEST_ONLY",
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("创建通知通道失败: %v", err)
	}

	r := gin.New()
	handler := NewIntegrationHandler(db, integration.NewIntegrationService(db))
	r.GET("/integrations", handler.List)

	req := httptest.NewRequest(http.MethodGet, "/integrations", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d: %s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	assertIntegrationResponseSanitized(t, body, []string{
		"TOKEN_A", "TOKEN_B", "TOKEN_C", "FAKE_ENDPOINT_SECRET_FOR_TEST_ONLY",
		"FAKE_INTEGRATION_SECRET_FOR_TEST_ONLY", "proxy-user", "FAKE_PROXY_PASSWORD_FOR_TEST_ONLY",
		"proxy.example.test", "proxy-path", "FAKE_PROXY_TOKEN_FOR_TEST_ONLY",
	})
	if !strings.Contains(body, `"endpoint":"https://redacted.invalid"`) {
		t.Fatalf("期望 endpoint 使用脱敏占位符，实际: %s", body)
	}
	if !strings.Contains(body, `"proxy_url":"http://redacted.invalid"`) {
		t.Fatalf("期望 proxy_url 使用脱敏占位符，实际: %s", body)
	}
	if !strings.Contains(body, `"has_secret":true`) {
		t.Fatalf("期望 has_secret 保留，实际: %s", body)
	}
}

func TestIntegrationHandlerListSanitizesNonURLValues(t *testing.T) {
	db := openIntegrationHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Integration{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	item := model.Integration{
		Type:            "email",
		Name:            "email-channel",
		Endpoint:        "ops@example.test, oncall@example.test",
		Enabled:         true,
		FailThreshold:   1,
		CooldownMinutes: 5,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("创建通知通道失败: %v", err)
	}

	r := gin.New()
	handler := NewIntegrationHandler(db, integration.NewIntegrationService(db))
	r.GET("/integrations", handler.List)

	req := httptest.NewRequest(http.MethodGet, "/integrations", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d: %s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	assertIntegrationResponseSanitized(t, body, []string{"ops@example.test", "oncall@example.test"})
	if !strings.Contains(body, `"endpoint":"[redacted]"`) {
		t.Fatalf("期望非 URL endpoint 使用通用脱敏占位符，实际: %s", body)
	}
}

func TestIntegrationHandlerGetSanitizesEndpointAndProxyURL(t *testing.T) {
	db := openIntegrationHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Integration{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	item := model.Integration{
		Type:            "slack",
		Name:            "sensitive-slack",
		Endpoint:        "https://hooks.slack.com/services/FAKE_TEAM/FAKE_BOT/FAKE_SLACK_TOKEN?debug=FAKE_DEBUG_TOKEN",
		Secret:          "FAKE_SLACK_SECRET_FOR_TEST_ONLY",
		Enabled:         true,
		FailThreshold:   2,
		CooldownMinutes: 10,
		ProxyURL:        "https://proxy-user:FAKE_PROXY_PASSWORD_FOR_TEST_ONLY@proxy.internal.example:8443/proxy-path?token=FAKE_PROXY_TOKEN_FOR_TEST_ONLY",
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("创建通知通道失败: %v", err)
	}

	r := gin.New()
	handler := NewIntegrationHandler(db, integration.NewIntegrationService(db))
	r.GET("/integrations/:id", handler.Get)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/integrations/%d", item.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d: %s", resp.Code, resp.Body.String())
	}
	assertIntegrationResponseSanitized(t, resp.Body.String(), []string{
		"FAKE_TEAM", "FAKE_BOT", "FAKE_SLACK_TOKEN", "FAKE_DEBUG_TOKEN",
		"FAKE_SLACK_SECRET_FOR_TEST_ONLY", "proxy-user", "FAKE_PROXY_PASSWORD_FOR_TEST_ONLY",
		"proxy.internal.example", "proxy-path", "FAKE_PROXY_TOKEN_FOR_TEST_ONLY",
	})
	if !strings.Contains(resp.Body.String(), `"endpoint":"https://redacted.invalid"`) || !strings.Contains(resp.Body.String(), `"proxy_url":"https://redacted.invalid"`) {
		t.Fatalf("期望详情响应返回脱敏 endpoint/proxy_url，实际: %s", resp.Body.String())
	}
}

func TestIntegrationHandlerCreateSanitizesResponse(t *testing.T) {
	t.Setenv("INTEGRATION_BLOCK_PRIVATE_ENDPOINTS", "false")
	db := openIntegrationHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Integration{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	r := gin.New()
	handler := NewIntegrationHandler(db, integration.NewIntegrationService(db))
	r.POST("/integrations", handler.Create)

	body := `{"type":"webhook","name":"created-webhook","endpoint":"https://hooks.example.test/services/FAKE_CREATED_TOKEN?token=FAKE_CREATED_QUERY","secret":"FAKE_CREATED_SECRET_FOR_TEST_ONLY","proxy_url":"socks5://proxy-user:FAKE_PROXY_SECRET@proxy.example.test:1080/path?token=FAKE_PROXY_QUERY","skip_endpoint_hint":true}`
	req := httptest.NewRequest(http.MethodPost, "/integrations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("期望状态码 201，实际: %d: %s", resp.Code, resp.Body.String())
	}
	respBody := resp.Body.String()
	assertIntegrationResponseSanitized(t, respBody, []string{"FAKE_CREATED_TOKEN", "FAKE_CREATED_QUERY", "FAKE_CREATED_SECRET_FOR_TEST_ONLY", "proxy-user", "FAKE_PROXY_SECRET", "proxy.example.test", "FAKE_PROXY_QUERY"})
	if !strings.Contains(respBody, `"endpoint":"https://redacted.invalid"`) || !strings.Contains(respBody, `"proxy_url":"socks5://redacted.invalid"`) {
		t.Fatalf("期望响应返回脱敏 endpoint/proxy_url，实际: %s", respBody)
	}
}

func TestIntegrationHandlerUpdatePreservesStoredValuesWhenResponseMasksRoundTrip(t *testing.T) {
	t.Setenv("INTEGRATION_BLOCK_PRIVATE_ENDPOINTS", "false")
	db := openIntegrationHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Integration{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	originalEndpoint := "https://hooks.example.test/services/FAKE_UPDATE_TOKEN?token=FAKE_UPDATE_QUERY"
	originalProxy := "http://proxy-user:FAKE_UPDATE_PROXY_PASSWORD@proxy.example.test:8080/proxy-path?token=FAKE_UPDATE_PROXY_QUERY"
	item := model.Integration{Type: "webhook", Name: "update-webhook", Endpoint: originalEndpoint, Secret: "FAKE_UPDATE_SECRET_FOR_TEST_ONLY", Enabled: true, FailThreshold: 1, CooldownMinutes: 5, ProxyURL: originalProxy}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("创建通知通道失败: %v", err)
	}

	r := gin.New()
	handler := NewIntegrationHandler(db, integration.NewIntegrationService(db))
	r.PUT("/integrations/:id", handler.Update)

	body := `{"type":"webhook","name":"updated-webhook","endpoint":"https://redacted.invalid","enabled":true,"fail_threshold":3,"cooldown_minutes":9,"proxy_url":"http://redacted.invalid","skip_endpoint_hint":true}`
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/integrations/%d", item.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d: %s", resp.Code, resp.Body.String())
	}
	assertIntegrationResponseSanitized(t, resp.Body.String(), []string{"FAKE_UPDATE_TOKEN", "FAKE_UPDATE_QUERY", "FAKE_UPDATE_SECRET_FOR_TEST_ONLY", "proxy-user", "FAKE_UPDATE_PROXY_PASSWORD", "proxy.example.test", "FAKE_UPDATE_PROXY_QUERY"})
	if !strings.Contains(resp.Body.String(), `"endpoint":"https://redacted.invalid"`) || !strings.Contains(resp.Body.String(), `"proxy_url":"http://redacted.invalid"`) {
		t.Fatalf("期望更新响应返回脱敏 endpoint/proxy_url，实际: %s", resp.Body.String())
	}

	var stored model.Integration
	if err := db.First(&stored, item.ID).Error; err != nil {
		t.Fatalf("查询更新后的通知通道失败: %v", err)
	}
	if stored.Endpoint != originalEndpoint {
		t.Fatalf("脱敏 endpoint 回传不应覆盖真实 endpoint，实际: %s", stored.Endpoint)
	}
	if stored.ProxyURL != originalProxy {
		t.Fatalf("脱敏 proxy_url 回传不应覆盖真实 proxy_url，实际: %s", stored.ProxyURL)
	}
	if stored.Secret != "FAKE_UPDATE_SECRET_FOR_TEST_ONLY" {
		t.Fatalf("未提供 secret 时不应覆盖原签名密钥")
	}
}

func TestIntegrationHandlerPatchSanitizesResponseAndPreservesMaskedProxyURL(t *testing.T) {
	db := openIntegrationHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Integration{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	originalEndpoint := "https://hooks.example.test/services/FAKE_PATCH_TOKEN?token=FAKE_PATCH_QUERY"
	originalProxy := "socks5://proxy-user:FAKE_PATCH_PROXY_PASSWORD@proxy.example.test:1080/proxy-path?token=FAKE_PATCH_PROXY_QUERY"
	item := model.Integration{Type: "webhook", Name: "patch-webhook", Endpoint: originalEndpoint, Secret: "FAKE_PATCH_SECRET_FOR_TEST_ONLY", Enabled: true, FailThreshold: 1, CooldownMinutes: 5, ProxyURL: originalProxy}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("创建通知通道失败: %v", err)
	}

	r := gin.New()
	handler := NewIntegrationHandler(db, integration.NewIntegrationService(db))
	r.PATCH("/integrations/:id", handler.Patch)

	body := `{"name":"patched-webhook","endpoint":"https://redacted.invalid","fail_threshold":4,"cooldown_minutes":11,"proxy_url":"socks5://redacted.invalid","skip_endpoint_hint":true}`
	req := httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/integrations/%d", item.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d: %s", resp.Code, resp.Body.String())
	}
	assertIntegrationResponseSanitized(t, resp.Body.String(), []string{"FAKE_PATCH_TOKEN", "FAKE_PATCH_QUERY", "FAKE_PATCH_SECRET_FOR_TEST_ONLY", "proxy-user", "FAKE_PATCH_PROXY_PASSWORD", "proxy.example.test", "FAKE_PATCH_PROXY_QUERY"})
	if !strings.Contains(resp.Body.String(), `"endpoint":"https://redacted.invalid"`) || !strings.Contains(resp.Body.String(), `"proxy_url":"socks5://redacted.invalid"`) {
		t.Fatalf("期望 PATCH 响应返回脱敏 endpoint/proxy_url，实际: %s", resp.Body.String())
	}

	var stored model.Integration
	if err := db.First(&stored, item.ID).Error; err != nil {
		t.Fatalf("查询 PATCH 后的通知通道失败: %v", err)
	}
	if stored.Endpoint != originalEndpoint {
		t.Fatalf("脱敏 endpoint 回传不应覆盖真实 endpoint，实际: %s", stored.Endpoint)
	}
	if stored.ProxyURL != originalProxy {
		t.Fatalf("脱敏 proxy_url 回传不应覆盖真实 proxy_url，实际: %s", stored.ProxyURL)
	}
}

func TestIntegrationHandlerTestFailureSanitizesSenderError(t *testing.T) {
	db := openIntegrationHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Integration{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	item := model.Integration{
		Type:            "webhook",
		Name:            "failing-webhook",
		Endpoint:        "https://127.0.0.1:1/services/FAKE_SEND_TOKEN?secret=FAKE_SEND_QUERY",
		Enabled:         true,
		FailThreshold:   1,
		CooldownMinutes: 1,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("创建通知通道失败: %v", err)
	}

	r := gin.New()
	handler := NewIntegrationHandler(db, integration.NewIntegrationService(db))
	r.POST("/integrations/:id/test", handler.Test)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/integrations/%d/test", item.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d: %s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	for _, forbidden := range []string{"127.0.0.1", "FAKE_SEND_TOKEN", "FAKE_SEND_QUERY", "/services/"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("测试发送失败响应泄露敏感片段 %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, "测试发送失败") {
		t.Fatalf("期望保留失败提示，实际: %s", body)
	}
}

func assertIntegrationResponseSanitized(t *testing.T, body string, forbidden []string) {
	t.Helper()
	if strings.Contains(body, `"secret"`) {
		t.Fatalf("响应不应序列化 secret 字段: %s", body)
	}
	for _, item := range forbidden {
		if strings.Contains(body, item) {
			t.Fatalf("响应泄露敏感片段 %q: %s", item, body)
		}
	}
}

func openIntegrationHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	secure.ResetForTesting()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", handlerTestDBName(t))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层数据库失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	return db
}
