package middleware

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xirang/backend/internal/logger"

	"github.com/gin-gonic/gin"
)

// metricsAuthState 跟踪未配置 token 时的告警节流，避免每次抓取都打日志。
type metricsAuthState struct {
	once          sync.Once
	lastWarnUnix  atomic.Int64
	warnIntervalS int64
}

var defaultMetricsAuthState = &metricsAuthState{warnIntervalS: 600}

// MetricsAuth 返回一个守卫 /metrics 端点的中间件。
//
// 行为：
//   - token == ""：放行，但首次请求 + 每 10 分钟在 logger 打 warning，
//     提示生产环境应该设置 METRICS_TOKEN（兼容现有公开行为）。
//   - token != ""：要求 Authorization: Bearer <token> 头匹配，否则返回 401。
//
// Prometheus 抓取支持 bearer_token / bearer_token_file 配置，与本中间件兼容。
func MetricsAuth(token string) gin.HandlerFunc {
	expected := strings.TrimSpace(token)
	state := defaultMetricsAuthState
	return func(c *gin.Context) {
		if expected == "" {
			state.warnSampled()
			c.Next()
			return
		}

		header := c.GetHeader("Authorization")
		if !validBearer(header, expected) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// warnSampled 首次启动 + 每 warnIntervalS 秒最多打一次 warn 日志，
// 避免高频 Prometheus 抓取把 warn 日志刷爆。
func (s *metricsAuthState) warnSampled() {
	s.once.Do(func() {
		logger.Module("metrics_auth").Warn().
			Msg("/metrics 未启用 token 鉴权，建议生产环境设置 METRICS_TOKEN")
		s.lastWarnUnix.Store(time.Now().Unix())
	})
	now := time.Now().Unix()
	last := s.lastWarnUnix.Load()
	if now-last < s.warnIntervalS {
		return
	}
	if !s.lastWarnUnix.CompareAndSwap(last, now) {
		return
	}
	logger.Module("metrics_auth").Warn().
		Msg("/metrics 未启用 token 鉴权，建议生产环境设置 METRICS_TOKEN")
}

// validBearer 期望 Authorization: Bearer <token>，常量时间比较 token。
func validBearer(header, expected string) bool {
	const prefix = "Bearer "
	if len(header) <= len(prefix) {
		return false
	}
	if !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	provided := strings.TrimSpace(header[len(prefix):])
	if provided == "" || len(provided) != len(expected) {
		return false
	}
	// 常量时间比较，避免 timing 攻击。
	var diff byte
	for i := 0; i < len(expected); i++ {
		diff |= provided[i] ^ expected[i]
	}
	return diff == 0
}

// MetricsRateLimit 返回 /metrics 端点的独立限流中间件（per IP）。
//
// 与 APIRateLimit 在内部复用同一实现，但维护独立的存储桶，避免与 /api 限流耦合。
// 默认参数（5 req/s）对应 Prometheus 通常 15-30s 一次的抓取频率，
// 即使有多个 scraper 也留有充足余量；超限返回 429。
func MetricsRateLimit(limit int, window time.Duration) gin.HandlerFunc {
	limiter := newAPIRateLimiter(limit, window)
	return func(c *gin.Context) {
		if limiter.allow(c.ClientIP(), time.Now()) {
			c.Next()
			return
		}
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "metrics 抓取过于频繁"})
		c.Abort()
	}
}
