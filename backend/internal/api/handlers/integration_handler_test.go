package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestValidateIntegrationEndpointRejectsInvalidScheme(t *testing.T) {
	t.Setenv("INTEGRATION_BLOCK_PRIVATE_ENDPOINTS", "false")
	if err := validateIntegrationEndpoint("webhook", "ftp://example.com/hook"); err == nil {
		t.Fatalf("期望非 http/https endpoint 返回错误")
	}
}

func TestValidateIntegrationEndpointBlocksPrivateHostWhenEnabled(t *testing.T) {
	t.Setenv("INTEGRATION_BLOCK_PRIVATE_ENDPOINTS", "true")
	if err := validateIntegrationEndpoint("webhook", "http://127.0.0.1/hook"); err == nil {
		t.Fatalf("期望开启私网阻断时拒绝回环地址")
	}
}

func TestValidateIntegrationEndpointAllowsPrivateHostWhenDisabled(t *testing.T) {
	t.Setenv("INTEGRATION_BLOCK_PRIVATE_ENDPOINTS", "false")
	if err := validateIntegrationEndpoint("webhook", "http://127.0.0.1/hook"); err != nil {
		t.Fatalf("期望关闭私网阻断时允许回环地址，实际错误: %v", err)
	}
}

func TestValidateIntegrationEndpointTelegramRequiresBotTokenPath(t *testing.T) {
	t.Setenv("INTEGRATION_BLOCK_PRIVATE_ENDPOINTS", "false")
	err := validateIntegrationEndpoint("telegram", "https://api.telegram.org/sendMessage?chat_id=1")
	if err == nil {
		t.Fatalf("期望缺少 /bot<token> 路径时返回错误")
	}
	if !strings.Contains(err.Error(), "/bot<token>") {
		t.Fatalf("期望错误提示包含 /bot<token>，实际: %v", err)
	}
}

func TestValidateIntegrationEndpointTelegramRequiresChatID(t *testing.T) {
	t.Setenv("INTEGRATION_BLOCK_PRIVATE_ENDPOINTS", "false")
	err := validateIntegrationEndpoint("telegram", "https://api.telegram.org/bot123456:abc/sendMessage")
	if err == nil {
		t.Fatalf("期望缺少 chat_id 时返回错误")
	}
	if !strings.Contains(err.Error(), "chat_id") {
		t.Fatalf("期望错误提示包含 chat_id，实际: %v", err)
	}
}

func TestValidateIntegrationEndpointTelegramAcceptsValidEndpoint(t *testing.T) {
	t.Setenv("INTEGRATION_BLOCK_PRIVATE_ENDPOINTS", "false")
	err := validateIntegrationEndpoint("telegram", "https://api.telegram.org/bot123456:abc/sendMessage?chat_id=-1001")
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
	handler := NewIntegrationHandler(db)
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
	handler := NewIntegrationHandler(db)
	r.POST("/integrations/:id/test", handler.Test)

	req := httptest.NewRequest(http.MethodPost, "/integrations/999/test", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("期望状态码 404，实际: %d", resp.Code)
	}
}

func openIntegrationHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}
