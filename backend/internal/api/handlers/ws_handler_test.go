package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestTokenFromSubprotocol(t *testing.T) {
	header := "xirang-auth.v1, xirang-auth-token.jwt-token-123"
	if got := tokenFromSubprotocol(header); got != "jwt-token-123" {
		t.Fatalf("期望提取到子协议中的 token，实际: %q", got)
	}
}

func TestTokenFromSubprotocolNoToken(t *testing.T) {
	header := "xirang-auth.v1, another-protocol"
	if got := tokenFromSubprotocol(header); got != "" {
		t.Fatalf("期望无 token 时返回空字符串，实际: %q", got)
	}
}

func TestExtractWSTokenPrefersSubprotocol(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ws/logs?token=query-token", nil)
	req.Header.Set("Sec-WebSocket-Protocol", "xirang-auth.v1, xirang-auth-token.header-token")
	ctx.Request = req

	if got := extractWSToken(ctx); got != "header-token" {
		t.Fatalf("期望优先从子协议提取 token，实际: %q", got)
	}
}

func TestExtractWSTokenRejectsQueryToken(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/ws/logs?token=query-token", nil)

	if got := extractWSToken(ctx); got != "" {
		t.Fatalf("期望忽略 query token，实际: %q", got)
	}
}

