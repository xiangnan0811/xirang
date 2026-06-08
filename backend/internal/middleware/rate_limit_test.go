package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestLoginRateLimiterAllowAndBlock(t *testing.T) {
	limiter := newLoginRateLimiter(2, time.Minute)
	now := time.Now()

	if allowed, _ := limiter.allow("127.0.0.1", now); !allowed {
		t.Fatalf("第一次请求应允许")
	}
	if allowed, _ := limiter.allow("127.0.0.1", now.Add(time.Second)); !allowed {
		t.Fatalf("第二次请求应允许")
	}
	if allowed, retryAfter := limiter.allow("127.0.0.1", now.Add(2*time.Second)); allowed || retryAfter <= 0 {
		t.Fatalf("第三次请求应被限流且返回 retry_after，allowed=%v retryAfter=%d", allowed, retryAfter)
	}
}

func TestLoginRateLimiterResetWindow(t *testing.T) {
	limiter := newLoginRateLimiter(1, time.Second)
	now := time.Now()

	if allowed, _ := limiter.allow("127.0.0.1", now); !allowed {
		t.Fatalf("首次请求应允许")
	}
	if allowed, _ := limiter.allow("127.0.0.1", now.Add(100*time.Millisecond)); allowed {
		t.Fatalf("窗口内第二次应被限流")
	}
	if allowed, _ := limiter.allow("127.0.0.1", now.Add(2*time.Second)); !allowed {
		t.Fatalf("窗口过期后应恢复允许")
	}
}

func TestLoginRateLimitRespondsWithEnvelopeAndRetryAfter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(LoginRateLimit(1, time.Minute))
	r.POST("/login", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	first := httptest.NewRecorder()
	r.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/login", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("第一次请求应允许，实际状态码 %d", first.Code)
	}

	second := httptest.NewRecorder()
	r.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/login", nil))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("第二次请求应限流，实际状态码 %d", second.Code)
	}
	if second.Header().Get("Retry-After") == "" {
		t.Fatalf("期望 Retry-After header")
	}

	var body struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			RetryAfter int `json:"retry_after"`
		} `json:"data"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析限流响应失败: %v", err)
	}
	if body.Code != http.StatusTooManyRequests || body.Message == "" || body.Data.RetryAfter <= 0 {
		t.Fatalf("限流响应 envelope 不符合预期: %+v", body)
	}
}
