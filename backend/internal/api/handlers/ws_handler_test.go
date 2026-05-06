package handlers

import (
	"encoding/json"
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
	handler := NewWSHandler(nil, nil, nil)
	ctx, recorder := createWSHandlerTestContext()

	handler.ServeWS(ctx)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("期望状态码 %d，实际 %d", http.StatusServiceUnavailable, recorder.Code)
	}
	assertServiceUnavailableEnvelope(t, recorder.Body.Bytes())
}

func TestWSHandlerServeWSNilJWTManager(t *testing.T) {
	handler := NewWSHandler(&ws.Hub{}, nil, nil)
	ctx, recorder := createWSHandlerTestContext()

	handler.ServeWS(ctx)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("期望状态码 %d，实际 %d", http.StatusServiceUnavailable, recorder.Code)
	}
	assertServiceUnavailableEnvelope(t, recorder.Body.Bytes())
}

func assertServiceUnavailableEnvelope(t *testing.T, body []byte) {
	t.Helper()
	var envelope Response
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("解析响应信封失败: %v", err)
	}
	if envelope.Code != http.StatusServiceUnavailable || envelope.Message != "websocket 服务不可用" {
		t.Fatalf("期望 websocket 服务不可用信封，实际: %+v", envelope)
	}
}
