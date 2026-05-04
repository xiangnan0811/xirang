package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// newMetricsRouter 构造一个仅含 /metrics 行为所需配置的最小路由，
// 以便对鉴权 + 限流逻辑做隔离测试。
func newMetricsRouter(t *testing.T, token string, rateLimit int, rateWindow time.Duration) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return NewRouter(Dependencies{
		MetricsToken:      token,
		MetricsRateLimit:  rateLimit,
		MetricsRateWindow: rateWindow,
	})
}

func TestMetricsAuthOpenWhenTokenUnset(t *testing.T) {
	router := newMetricsRouter(t, "", 100, time.Second)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("未配置 token 时 /metrics 应返回 200，实际: %d", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "go_") && !strings.Contains(body, "http_requests_total") {
		t.Fatalf("响应体应包含 Prometheus 指标，实际前 200 字节: %q", truncate(body, 200))
	}
}

func TestMetricsAuthRejectsMissingHeader(t *testing.T) {
	router := newMetricsRouter(t, "secret-metrics-token", 100, time.Second)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("缺少 Authorization 时应返回 401，实际: %d", resp.Code)
	}
}

func TestMetricsAuthRejectsBadToken(t *testing.T) {
	router := newMetricsRouter(t, "secret-metrics-token", 100, time.Second)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer wrong-token-value")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("token 不匹配时应返回 401，实际: %d", resp.Code)
	}
}

func TestMetricsAuthRejectsNonBearerScheme(t *testing.T) {
	router := newMetricsRouter(t, "secret-metrics-token", 100, time.Second)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Basic c2VjcmV0LW1ldHJpY3MtdG9rZW4=")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("非 Bearer scheme 时应返回 401，实际: %d", resp.Code)
	}
}

func TestMetricsAuthAcceptsValidBearer(t *testing.T) {
	router := newMetricsRouter(t, "secret-metrics-token", 100, time.Second)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer secret-metrics-token")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("正确 Bearer token 应返回 200，实际: %d", resp.Code)
	}
}

func TestMetricsAuthAcceptsLowercaseBearerScheme(t *testing.T) {
	router := newMetricsRouter(t, "secret-metrics-token", 100, time.Second)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "bearer secret-metrics-token")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("scheme 大小写不应影响校验，实际: %d", resp.Code)
	}
}

func TestMetricsRateLimitTriggers429(t *testing.T) {
	limit := 3
	router := newMetricsRouter(t, "", limit, time.Minute)

	for i := 0; i < limit; i++ {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.RemoteAddr = "192.0.2.10:1234"
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("第 %d 次请求应放行，实际: %d", i+1, resp.Code)
		}
	}

	// 第 limit+1 次应被限流。
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("超过限流后应返回 429，实际: %d (limit=%d)", resp.Code, limit)
	}
}

func TestMetricsRateLimitPerIPIsolation(t *testing.T) {
	limit := 2
	router := newMetricsRouter(t, "", limit, time.Minute)

	// 用尽 IP A 的配额。
	for i := 0; i < limit+1; i++ {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.RemoteAddr = "192.0.2.20:1234"
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		_ = resp
	}

	// IP B 应仍可正常抓取。
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "192.0.2.21:1234"
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("不同 IP 应有独立配额，IP B 应返回 200，实际: %d", resp.Code)
	}
}

func TestMetricsRateLimitDefaultsWhenZero(t *testing.T) {
	// limit/window 为 0 时应回退到默认值（5 req/s），而非禁用限流或 panic。
	router := newMetricsRouter(t, "", 0, 0)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("默认配置下首次请求应放行，实际: %d", resp.Code)
	}
}

// helpers
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return fmt.Sprintf("%s...(truncated)", s[:n])
}
