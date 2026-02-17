package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type rateWindow struct {
	count int
	reset time.Time
}

type loginRateLimiter struct {
	mu     sync.Mutex
	store  map[string]rateWindow
	limit  int
	window time.Duration
}

func newLoginRateLimiter(limit int, window time.Duration) *loginRateLimiter {
	rl := &loginRateLimiter{
		store:  make(map[string]rateWindow),
		limit:  limit,
		window: window,
	}
	go rl.cleanup()
	return rl
}

func (l *loginRateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
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

func (l *loginRateLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.store[ip]
	if !ok || now.After(entry.reset) {
		l.store[ip] = rateWindow{count: 1, reset: now.Add(l.window)}
		return true
	}

	if entry.count >= l.limit {
		return false
	}

	entry.count++
	l.store[ip] = entry
	return true
}

func LoginRateLimit(limit int, window time.Duration) gin.HandlerFunc {
	limiter := newLoginRateLimiter(limit, window)
	return func(c *gin.Context) {
		if limiter.allow(c.ClientIP(), time.Now()) {
			c.Next()
			return
		}
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "登录尝试过于频繁，请稍后再试"})
		c.Abort()
	}
}
