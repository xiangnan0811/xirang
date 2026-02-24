package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestResolveAllowedOrigin(t *testing.T) {
	if got := resolveAllowedOrigin("https://xirang.example.com", "xirang.example.com:8080", []string{"https://xirang.example.com"}); got != "https://xirang.example.com" {
		t.Fatalf("期望返回匹配域名，实际: %s", got)
	}

	if got := resolveAllowedOrigin("https://bad.example.com", "xirang.example.com:8080", []string{"https://xirang.example.com"}); got != "" {
		t.Fatalf("期望返回空，实际: %s", got)
	}

	if got := resolveAllowedOrigin("https://foo.example.com", "xirang.example.com:8080", []string{"*"}); got != "https://foo.example.com" {
		t.Fatalf("通配符应回显 origin，实际: %s", got)
	}

	if got := resolveAllowedOrigin("", "xirang.example.com:8080", []string{"*"}); got != "" {
		t.Fatalf("空 origin 不应回退通配符，实际: %s", got)
	}

	if got := resolveAllowedOrigin("", "xirang.example.com:8080", []string{"https://xirang.example.com"}); got != "" {
		t.Fatalf("空 origin 应返回空字符串，实际: %s", got)
	}

	if got := resolveAllowedOrigin("http://192.168.1.20:5173", "192.168.1.20:8080", nil); got != "http://192.168.1.20:5173" {
		t.Fatalf("同主机 Origin 应自动放行，实际: %s", got)
	}

	if got := resolveAllowedOrigin("null", "192.168.1.20:8080", nil); got != "" {
		t.Fatalf("非法 Origin 应拒绝，实际: %s", got)
	}

	if got := resolveAllowedOrigin("http://evil.com:5173", "192.168.1.20:8080", nil); got != "" {
		t.Fatalf("不同主机 Origin 应拒绝，实际: %s", got)
	}
}

func TestNewRouterRegisterRoutes(t *testing.T) {
	g := NewRouter(Dependencies{})
	routes := g.Routes()

	if !hasRoute(routes, http.MethodGet, "/api/v1/tasks") {
		t.Fatalf("未注册任务列表接口")
	}
	if !hasRoute(routes, http.MethodGet, "/api/v1/tasks/:id/logs") {
		t.Fatalf("未注册任务日志接口")
	}
	if !hasRoute(routes, http.MethodGet, "/api/v1/alerts/:id/deliveries") {
		t.Fatalf("未注册告警投递记录接口")
	}
	if !hasRoute(routes, http.MethodGet, "/api/v1/alerts/delivery-stats") {
		t.Fatalf("未注册告警投递统计接口")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/integrations/:id/test") {
		t.Fatalf("未注册通知通道测试接口")
	}
	if !hasRoute(routes, http.MethodGet, "/api/v1/audit-logs") {
		t.Fatalf("未注册审计日志接口")
	}
	if !hasRoute(routes, http.MethodGet, "/api/v1/audit-logs/export") {
		t.Fatalf("未注册审计导出接口")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/alerts/:id/retry-delivery") {
		t.Fatalf("未注册告警投递重发接口")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/alerts/:id/retry-failed-deliveries") {
		t.Fatalf("未注册失败投递批量重发接口")
	}
	if hasRoute(routes, http.MethodPost, "/api/v1/nodes/:id/exec") {
		t.Fatalf("不应注册节点远程执行接口")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/nodes/batch-delete") {
		t.Fatalf("未注册节点批量删除接口")
	}
}

func TestNewRouterCORSHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := NewRouter(Dependencies{
		AllowedOrigins: []string{"https://xirang.example.com"},
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://xirang.example.com")
	resp := httptest.NewRecorder()
	g.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}
	if got := resp.Header().Get("Access-Control-Allow-Origin"); got != "https://xirang.example.com" {
		t.Fatalf("期望允许 origin 被回写，实际: %s", got)
	}
	if got := resp.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("期望允许凭证头，实际: %s", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	resp = httptest.NewRecorder()
	g.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("空 origin 场景期望状态码 200，实际: %d", resp.Code)
	}
	if got := resp.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("空 origin 不应写入 Allow-Origin，实际: %s", got)
	}
	if got := resp.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("空 origin 不应写入 Allow-Credentials，实际: %s", got)
	}

	req = httptest.NewRequest(http.MethodGet, "http://192.168.1.20:8080/healthz", nil)
	req.Header.Set("Origin", "http://192.168.1.20:5173")
	resp = httptest.NewRecorder()
	g.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("同主机跨端口 Origin 应返回 200，实际: %d", resp.Code)
	}
	if got := resp.Header().Get("Access-Control-Allow-Origin"); got != "http://192.168.1.20:5173" {
		t.Fatalf("同主机 Origin 应被回写，实际: %s", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	resp = httptest.NewRecorder()
	g.ServeHTTP(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("非法 origin 应返回 403，实际: %d", resp.Code)
	}
}

func hasRoute(routes []gin.RouteInfo, method string, path string) bool {
	for _, route := range routes {
		if route.Method == method && route.Path == path {
			return true
		}
	}
	return false
}
