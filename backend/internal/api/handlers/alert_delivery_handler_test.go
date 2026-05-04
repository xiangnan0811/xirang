package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// openDeliveryTestDB 返回内存 SQLite 测试数据库，已迁移 alert_deliveries / alerts / integrations 表。
func openDeliveryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Alert{}, &model.AlertDelivery{}, &model.Integration{}); err != nil {
		t.Fatalf("迁移表失败: %v", err)
	}
	return db
}

// newDeliveryRouter 构建仅含 alert-deliveries retry 路由的测试路由器，注入指定角色。
func newDeliveryRouter(db *gorm.DB, worker *alerting.RetryWorker, role string) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("role", role)
		c.Set("userID", uint(1))
		c.Next()
	})
	h := NewAlertDeliveryHandler(worker)
	r.POST("/api/v1/alert-deliveries/:id/retry", middleware.RBAC("alerts:write"), h.Retry)
	return r
}

// TestRetryDelivery_Success 验证 admin 对 retrying 状态的投递执行手动重试后返回 200，
// 且底层 webhook 成功时投递状态变为 "sent"。
// 使用 httptest.NewServer 替代 SetSendFn，避免跨包访问内部字段。
func TestRetryDelivery_Success(t *testing.T) {
	// 启动一个始终返回 200 的假 webhook 服务器
	fakeWebhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(fakeWebhook.Close)

	db := openDeliveryTestDB(t)

	// 植入 integration，Endpoint 指向假 webhook
	integ := model.Integration{
		Name:     "wh",
		Type:     "webhook",
		Enabled:  true,
		Endpoint: fakeWebhook.URL,
	}
	db.Create(&integ)
	alert := model.Alert{NodeID: 1, NodeName: "n", ErrorCode: "XR-1", Severity: "warn", Status: "open", Message: "m"}
	db.Create(&alert)

	// 植入 status=retrying 的投递记录
	delivery := model.AlertDelivery{
		AlertID:       alert.ID,
		IntegrationID: integ.ID,
		Status:        "retrying",
		AttemptCount:  1,
	}
	db.Create(&delivery)

	worker := alerting.NewRetryWorker(db)
	r := newDeliveryRouter(db, worker, "admin")
	w := doSilenceJSON(r, "POST", fmt.Sprintf("/api/v1/alert-deliveries/%d/retry", delivery.ID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际: %d — %s", w.Code, w.Body.String())
	}

	// 验证 DB 中投递状态已变为 sent
	var updated model.AlertDelivery
	db.First(&updated, delivery.ID)
	if updated.Status != "sent" {
		t.Fatalf("期望 status=sent，实际: %s", updated.Status)
	}
}

// TestRetryDelivery_RequiresAdmin 验证无 alerts:write 权限的角色收到 403。
func TestRetryDelivery_RequiresAdmin(t *testing.T) {
	db := openDeliveryTestDB(t)
	worker := alerting.NewRetryWorker(db)

	// "guest" 不在 rolePermissions 中，RBAC 中间件会返回 403
	r := newDeliveryRouter(db, worker, "guest")

	// 植入一条 delivery 供路由参数用（即使权限不足也不应到达 handler）
	delivery := model.AlertDelivery{AlertID: 1, IntegrationID: 1, Status: "retrying", AttemptCount: 1}
	db.Create(&delivery)

	w := doSilenceJSON(r, "POST", fmt.Sprintf("/api/v1/alert-deliveries/%d/retry", delivery.ID), "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("期望 403 Forbidden，实际: %d — %s", w.Code, w.Body.String())
	}
}

// TestRetryDelivery_RejectsAlreadySent 验证对 status=sent 的投递执行重试时返回 400。
func TestRetryDelivery_RejectsAlreadySent(t *testing.T) {
	db := openDeliveryTestDB(t)

	integ := model.Integration{Name: "wh", Type: "webhook", Enabled: true, Endpoint: "http://127.0.0.1:0"}
	db.Create(&integ)
	alert := model.Alert{NodeID: 1, NodeName: "n", ErrorCode: "XR-1", Severity: "warn", Status: "open", Message: "m"}
	db.Create(&alert)

	delivery := model.AlertDelivery{
		AlertID:       alert.ID,
		IntegrationID: integ.ID,
		Status:        "sent",
		AttemptCount:  1,
	}
	db.Create(&delivery)

	worker := alerting.NewRetryWorker(db)
	r := newDeliveryRouter(db, worker, "admin")
	w := doSilenceJSON(r, "POST", fmt.Sprintf("/api/v1/alert-deliveries/%d/retry", delivery.ID), "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 400 BadRequest，实际: %d — %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "already sent") {
		t.Fatalf("期望错误信息包含 'already sent'，实际: %s", w.Body.String())
	}
}

// TestRetryDelivery_NotFound 验证对不存在的 delivery ID 执行重试时返回 404。
func TestRetryDelivery_NotFound(t *testing.T) {
	db := openDeliveryTestDB(t)
	worker := alerting.NewRetryWorker(db)

	r := newDeliveryRouter(db, worker, "admin")
	w := doSilenceJSON(r, "POST", "/api/v1/alert-deliveries/99999/retry", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("期望 404 Not Found，实际: %d — %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "delivery not found") {
		t.Fatalf("期望响应包含 'delivery not found'，实际: %s", w.Body.String())
	}
}
