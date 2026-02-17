package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"xirang/backend/internal/ws"

	"github.com/gin-gonic/gin"
)

func createWSHandlerTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/ws/logs", nil)
	return ctx, recorder
}

func TestWSHandlerServeWSNilHub(t *testing.T) {
	handler := NewWSHandler(nil, nil)
	ctx, recorder := createWSHandlerTestContext()

	handler.ServeWS(ctx)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("期望状态码 %d，实际 %d", http.StatusServiceUnavailable, recorder.Code)
	}
}

func TestWSHandlerServeWSNilJWTManager(t *testing.T) {
	handler := NewWSHandler(&ws.Hub{}, nil)
	ctx, recorder := createWSHandlerTestContext()

	handler.ServeWS(ctx)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("期望状态码 %d，实际 %d", http.StatusServiceUnavailable, recorder.Code)
	}
}
