package middleware

import (
	"bytes"
	"encoding/json"
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

func TestStructuredLoggerContentShapedRequestsOmitIdentityForEveryMethodAndShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	previous := logger.Log
	logger.Log = zerolog.New(&output)
	t.Cleanup(func() { logger.Log = previous })

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(RequestIDKey, "safe-request-id")
		c.Set("userID", uint(771))
		c.Next()
	}, StructuredLogger())
	router.NoRoute(func(c *gin.Context) { c.Status(http.StatusNotFound) })

	deliveryID := strings.Repeat("d", 32)
	cases := []struct {
		method string
		target string
	}{
		{method: http.MethodGet, target: "/api/v1/asset-content/" + deliveryID},
		{method: http.MethodHead, target: "/api/v1/asset-content/" + deliveryID},
		{method: http.MethodPost, target: "/api/v1/asset-content/not-an-id"},
		{method: http.MethodOptions, target: "/api/v1/asset-content/" + deliveryID + "/"},
		{method: http.MethodGet, target: "/api/v1/asset-content?query=FAKE_CONTENT_QUERY_CANARY_FOR_TEST_ONLY"},
	}
	for _, testCase := range cases {
		request := httptest.NewRequest(testCase.method, testCase.target, nil)
		request.RemoteAddr = "198.51.100.77:43210"
		request.Header.Set("X-Forwarded-For", "FAKE_XFF_CANARY_FOR_TEST_ONLY")
		request.Header.Set("X-Real-IP", "FAKE_XRI_CANARY_FOR_TEST_ONLY")
		request.Header.Set("Cookie", "FAKE_COOKIE_CANARY_FOR_TEST_ONLY")
		request.Header.Set("Referer", "https://FAKE_REFERER_CANARY_FOR_TEST_ONLY.invalid/")
		request.Header.Set("User-Agent", "FAKE_USER_AGENT_CANARY_FOR_TEST_ONLY")
		router.ServeHTTP(httptest.NewRecorder(), request)
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != len(cases) {
		t.Fatalf("log lines=%d want=%d logs=%s", len(lines), len(cases), output.String())
	}
	for index, line := range lines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode event %d: %v line=%s", index, err, line)
		}
		if event["path"] != BackupContentSafeRoute || event["method"] != cases[index].method ||
			event["request_id"] != "safe-request-id" {
			t.Fatalf("unsafe or incomplete shaped event=%v", event)
		}
		for _, required := range []string{"status", "latency_ms"} {
			if _, ok := event[required]; !ok {
				t.Fatalf("content event omitted safe field %q: %v", required, event)
			}
		}
		for _, forbidden := range []string{"client_ip", "user_id"} {
			if _, ok := event[forbidden]; ok {
				t.Fatalf("content event retained identity field %q: %v", forbidden, event)
			}
		}
	}
	for _, forbidden := range []string{
		deliveryID, "not-an-id", "FAKE_CONTENT_QUERY_CANARY_FOR_TEST_ONLY", "198.51.100.77",
		"FAKE_XFF_CANARY_FOR_TEST_ONLY", "FAKE_XRI_CANARY_FOR_TEST_ONLY", "FAKE_COOKIE_CANARY_FOR_TEST_ONLY",
		"FAKE_REFERER_CANARY_FOR_TEST_ONLY", "FAKE_USER_AGENT_CANARY_FOR_TEST_ONLY", "771",
	} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("content structured log leaked %q: %s", forbidden, output.String())
		}
	}
}

func TestStructuredLoggerOrdinaryRequestRetainsExistingIdentityFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	previous := logger.Log
	logger.Log = zerolog.New(&output)
	t.Cleanup(func() { logger.Log = previous })

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("userID", uint(771))
		c.Next()
	}, StructuredLogger())
	router.GET("/ordinary", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	request := httptest.NewRequest(http.MethodGet, "/ordinary", nil)
	request.RemoteAddr = "198.51.100.77:43210"
	router.ServeHTTP(httptest.NewRecorder(), request)

	logs := output.String()
	if !strings.Contains(logs, `"client_ip":"198.51.100.77"`) || !strings.Contains(logs, `"user_id":771`) {
		t.Fatalf("ordinary structured log changed identity fields: %s", logs)
	}
}
