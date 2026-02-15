package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAuditListFiltersAndPagination(t *testing.T) {
	db := openAuditHandlerTestDB(t)
	if err := db.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatalf("初始化审计表失败: %v", err)
	}

	now := time.Now()
	records := []model.AuditLog{
		{UserID: 1, Username: "admin", Role: "admin", Method: "POST", Path: "/api/v1/tasks/1/trigger", StatusCode: 202, ClientIP: "127.0.0.1", CreatedAt: now.Add(-2 * time.Minute)},
		{UserID: 2, Username: "operator", Role: "operator", Method: "POST", Path: "/api/v1/tasks/2/trigger", StatusCode: 403, ClientIP: "127.0.0.1", CreatedAt: now.Add(-time.Minute)},
		{UserID: 1, Username: "admin", Role: "admin", Method: "DELETE", Path: "/api/v1/tasks/3", StatusCode: 200, ClientIP: "127.0.0.1", CreatedAt: now},
	}
	for _, one := range records {
		if err := db.Create(&one).Error; err != nil {
			t.Fatalf("插入审计数据失败: %v", err)
		}
	}

	r := gin.New()
	handler := NewAuditHandler(db)
	r.GET("/audit-logs", handler.List)

	from := now.Add(-90 * time.Second).UTC().Format(time.RFC3339)
	url := "/audit-logs?username=admin&method=delete&from=" + from + "&limit=1&offset=0"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}

	var result struct {
		Data   []model.AuditLog `json:"data"`
		Total  int64            `json:"total"`
		Limit  int              `json:"limit"`
		Offset int              `json:"offset"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if result.Total != 1 || len(result.Data) != 1 {
		t.Fatalf("筛选结果不符合预期，total=%d len=%d", result.Total, len(result.Data))
	}
	if result.Data[0].Method != "DELETE" {
		t.Fatalf("期望筛选到 DELETE 审计，实际: %s", result.Data[0].Method)
	}
}

func TestAuditExportCSV(t *testing.T) {
	db := openAuditHandlerTestDB(t)
	if err := db.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatalf("初始化审计表失败: %v", err)
	}

	now := time.Now()
	records := []model.AuditLog{
		{UserID: 1, Username: "admin", Role: "admin", Method: "POST", Path: "/api/v1/tasks/1/trigger", StatusCode: 202, ClientIP: "127.0.0.1", CreatedAt: now.Add(-2 * time.Minute), UserAgent: "curl/8"},
		{UserID: 2, Username: "operator", Role: "operator", Method: "GET", Path: "/api/v1/tasks/2", StatusCode: 200, ClientIP: "127.0.0.2", CreatedAt: now.Add(-1 * time.Minute), UserAgent: "browser"},
	}
	for _, one := range records {
		if err := db.Create(&one).Error; err != nil {
			t.Fatalf("插入审计数据失败: %v", err)
		}
	}

	r := gin.New()
	handler := NewAuditHandler(db)
	r.GET("/audit-logs/export", handler.ExportCSV)

	req := httptest.NewRequest(http.MethodGet, "/audit-logs/export?method=POST", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}
	if !strings.Contains(resp.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("期望 csv content-type，实际: %s", resp.Header().Get("Content-Type"))
	}
	body := resp.Body.String()
	if !strings.Contains(body, "id,created_at,username,role,method,path,status_code,client_ip,user_agent") {
		t.Fatalf("CSV 头缺失，内容: %s", body)
	}
	if !strings.Contains(body, "/api/v1/tasks/1/trigger") {
		t.Fatalf("CSV 缺少 POST 记录，内容: %s", body)
	}
	if strings.Contains(body, "/api/v1/tasks/2") {
		t.Fatalf("CSV 过滤失败，不应包含 GET 记录，内容: %s", body)
	}
}

func openAuditHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}
