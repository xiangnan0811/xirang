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

func TestStructuredLoggerRedactsExactContentPathWithoutChangingOrdinaryRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	previous := logger.Log
	logger.Log = zerolog.New(&output)
	t.Cleanup(func() { logger.Log = previous })

	router := gin.New()
	router.Use(StructuredLogger())
	router.GET("/api/v1/asset-content/:deliveryId", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	router.GET("/api/v1/ordinary/:id", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	deliveryID := strings.Repeat("d", 32)
	contentRequest := httptest.NewRequest(http.MethodGet,
		"/api/v1/asset-content/"+deliveryID+"?jwt=FAKE_QUERY_SECRET_FOR_TEST_ONLY", nil)
	contentRequest.Header.Set("Cookie", "xirang_asset_delivery=FAKE_COOKIE_SECRET_FOR_TEST_ONLY")
	router.ServeHTTP(httptest.NewRecorder(), contentRequest)
	ordinaryRequest := httptest.NewRequest(http.MethodGet, "/api/v1/ordinary/7", nil)
	router.ServeHTTP(httptest.NewRecorder(), ordinaryRequest)

	logs := output.String()
	if !strings.Contains(logs, `"path":"/api/v1/asset-content/:deliveryId"`) ||
		!strings.Contains(logs, `"path":"/api/v1/ordinary/7"`) {
		t.Fatalf("structured logs=%s", logs)
	}
	for _, forbidden := range []string{deliveryID, "FAKE_QUERY_SECRET_FOR_TEST_ONLY", "FAKE_COOKIE_SECRET_FOR_TEST_ONLY"} {
		if strings.Contains(logs, forbidden) {
			t.Fatalf("structured log leaked %q: %s", forbidden, logs)
		}
	}
}
