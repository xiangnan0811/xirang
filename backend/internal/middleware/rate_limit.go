package middleware

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
)

type rateWindow struct {
	count int
	reset time.Time
}

type loginRateLimiter struct {
	mu          sync.Mutex
	store       map[string]rateWindow
	limit       int
	window      time.Duration
	settingsSvc *settings.Service
}

func newLoginRateLimiter(limit int, window time.Duration) *loginRateLimiter {
	return newLoginRateLimiterWithContext(context.Background(), nil, limit, window)
}

func newLoginRateLimiterWithContext(ctx context.Context, svc *settings.Service, limit int, window time.Duration) *loginRateLimiter {
	rl := &loginRateLimiter{
		store:       make(map[string]rateWindow),
		limit:       limit,
		window:      window,
		settingsSvc: svc,
	}
	go rl.cleanup(ctx)
	return rl
}

func (l *loginRateLimiter) cleanup(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			l.mu.Lock()
			for ip, entry := range l.store {
				if now.After(entry.reset) {
					delete(l.store, ip)
				}
			}
			l.mu.Unlock()
		}
	}
}

func (l *loginRateLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	// 动态读取配置
	limit := l.limit
	window := l.window
	if l.settingsSvc != nil {
		if v, err := strconv.Atoi(l.settingsSvc.GetEffective("login.rate_limit")); err == nil && v > 0 {
			limit = v
		}
		if d, err := time.ParseDuration(l.settingsSvc.GetEffective("login.rate_window")); err == nil && d > 0 {
			window = d
		}
	}

	entry, ok := l.store[ip]
	if !ok || now.After(entry.reset) {
		l.store[ip] = rateWindow{count: 1, reset: now.Add(window)}
		return true
	}

	if entry.count >= limit {
		return false
	}

	entry.count++
	l.store[ip] = entry
	return true
}

func LoginRateLimit(limit int, window time.Duration) gin.HandlerFunc {
	return LoginRateLimitWithContext(context.Background(), nil, limit, window)
}

func LoginRateLimitWithContext(ctx context.Context, settingsSvc *settings.Service, limit int, window time.Duration) gin.HandlerFunc {
	limiter := newLoginRateLimiterWithContext(ctx, settingsSvc, limit, window)
	return func(c *gin.Context) {
		if limiter.allow(c.ClientIP(), time.Now()) {
			c.Next()
			return
		}
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "登录尝试过于频繁，请稍后再试"})
		c.Abort()
	}
}
