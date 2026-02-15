package api

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestResolveAllowedOrigin(t *testing.T) {
	if got := resolveAllowedOrigin("https://xirang.example.com", []string{"https://xirang.example.com"}); got != "https://xirang.example.com" {
		t.Fatalf("期望返回匹配域名，实际: %s", got)
	}

	if got := resolveAllowedOrigin("https://bad.example.com", []string{"https://xirang.example.com"}); got != "" {
		t.Fatalf("期望返回空，实际: %s", got)
	}

	if got := resolveAllowedOrigin("https://foo.example.com", []string{"*"}); got != "https://foo.example.com" {
		t.Fatalf("通配符应回显 origin，实际: %s", got)
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
	if !hasRoute(routes, http.MethodPost, "/api/v1/nodes/:id/exec") {
		t.Fatalf("未注册节点远程执行接口")
	}
	if !hasRoute(routes, http.MethodPost, "/api/v1/nodes/batch-delete") {
		t.Fatalf("未注册节点批量删除接口")
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
