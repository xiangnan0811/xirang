package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestTrustedProxiesIgnoreForgedXFFWhenEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := r.SetTrustedProxies([]string{}); err != nil {
		t.Fatalf("SetTrustedProxies: %v", err)
	}
	var seen string
	r.GET("/ip", func(c *gin.Context) {
		seen = c.ClientIP()
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/ip", nil)
	req.RemoteAddr = "203.0.113.9:12345"
	req.Header.Set("X-Forwarded-For", "198.51.100.1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if seen != "203.0.113.9" {
		t.Fatalf("expected RemoteAddr IP, got %q (XFF should be ignored)", seen)
	}
}

func TestTrustedProxiesHonorXFFFromLoopback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := r.SetTrustedProxies([]string{"127.0.0.1"}); err != nil {
		t.Fatalf("SetTrustedProxies: %v", err)
	}
	var seen string
	r.GET("/ip", func(c *gin.Context) {
		seen = c.ClientIP()
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/ip", nil)
	req.RemoteAddr = "127.0.0.1:4567"
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if seen != "198.51.100.7" {
		t.Fatalf("expected XFF from trusted proxy, got %q", seen)
	}
}
