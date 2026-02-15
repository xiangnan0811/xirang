package middleware

import (
	"testing"
	"time"
)

func TestLoginRateLimiterAllowAndBlock(t *testing.T) {
	limiter := newLoginRateLimiter(2, time.Minute)
	now := time.Now()

	if !limiter.allow("127.0.0.1", now) {
		t.Fatalf("第一次请求应允许")
	}
	if !limiter.allow("127.0.0.1", now.Add(time.Second)) {
		t.Fatalf("第二次请求应允许")
	}
	if limiter.allow("127.0.0.1", now.Add(2*time.Second)) {
		t.Fatalf("第三次请求应被限流")
	}
}

func TestLoginRateLimiterResetWindow(t *testing.T) {
	limiter := newLoginRateLimiter(1, time.Second)
	now := time.Now()

	if !limiter.allow("127.0.0.1", now) {
		t.Fatalf("首次请求应允许")
	}
	if limiter.allow("127.0.0.1", now.Add(100*time.Millisecond)) {
		t.Fatalf("窗口内第二次应被限流")
	}
	if !limiter.allow("127.0.0.1", now.Add(2*time.Second)) {
		t.Fatalf("窗口过期后应恢复允许")
	}
}
