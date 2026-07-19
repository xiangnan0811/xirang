package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/logger"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
)

func TestContentSafeRecoveryReturnsGenericEmpty500WithoutRequestOrPanicLeak(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	previous := logger.Log
	logger.Log = zerolog.New(&output)
	t.Cleanup(func() { logger.Log = previous })

	router := gin.New()
	router.Use(RequestID(), ContentSafeRecovery())
	router.GET("/api/v1/asset-content/:deliveryId", func(*gin.Context) {
		panic("FAKE_PANIC_SECRET_FOR_TEST_ONLY")
	})
	deliveryID := strings.Repeat("d", 32)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/asset-content/"+deliveryID+"?jwt=FAKE_QUERY_SECRET_FOR_TEST_ONLY", nil)
	request.Header.Set("Cookie", "xirang_asset_delivery=FAKE_COOKIE_SECRET_FOR_TEST_ONLY")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError || response.Body.Len() != 0 ||
		response.Header().Get("X-Content-Type-Options") != "nosniff" ||
		response.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
		t.Fatalf("status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
	logs := output.String()
	if !strings.Contains(logs, `"category":"content_panic"`) || !strings.Contains(logs, `"request_id"`) {
		t.Fatalf("safe panic log=%s", logs)
	}
	for _, forbidden := range []string{
		"FAKE_PANIC_SECRET_FOR_TEST_ONLY", "FAKE_QUERY_SECRET_FOR_TEST_ONLY", "FAKE_COOKIE_SECRET_FOR_TEST_ONLY", deliveryID,
	} {
		if strings.Contains(logs, forbidden) {
			t.Fatalf("panic log leaked %q: %s", forbidden, logs)
		}
	}
}
