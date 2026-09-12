package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openServiceMonitorPostgresTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN required")
	}
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_SERVICE_MONITOR_POSTGRES_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	base, err := gorm.Open(postgres.Open(withServiceMonitorPostgresTimezone(dsn)), &gorm.Config{
		Logger:  logger.Default.LogMode(logger.Silent),
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("service_monitor_race_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = base.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		if db, err := base.DB(); err == nil {
			_ = db.Close()
		}
	})

	scopedDSN := withServiceMonitorPostgresSchema(dsn, schema)
	db, err := gorm.Open(postgres.Open(scopedDSN), &gorm.Config{
		Logger:  logger.Default.LogMode(logger.Silent),
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ServiceMonitor{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func withServiceMonitorPostgresTimezone(dsn string) string {
	if strings.Contains(dsn, "://") {
		separator := "?"
		if strings.Contains(dsn, "?") {
			separator = "&"
		}
		return dsn + separator + "timezone=UTC"
	}
	return dsn + " timezone=UTC"
}

func withServiceMonitorPostgresSchema(dsn, schema string) string {
	if strings.Contains(dsn, "://") {
		separator := "?"
		if strings.Contains(dsn, "?") {
			separator = "&"
		}
		return dsn + separator + "search_path=" + schema + "&timezone=UTC"
	}
	return dsn + " search_path=" + schema + " timezone=UTC"
}

func TestServiceMonitorConcurrentRetargetAndHeaderRotationRemainAtomicPostgres(t *testing.T) {
	db := openServiceMonitorPostgresTestDB(t)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewServiceMonitorHandler(db, nil)
	r.PUT("/service-monitors/:id", handler.Update)

	const originalSecret = "FAKE_POSTGRES_ORIGINAL_MONITOR_SECRET_FOR_TEST_ONLY"
	const rotatedSecret = "FAKE_POSTGRES_ROTATED_MONITOR_SECRET_FOR_TEST_ONLY"
	originalHeaders := fmt.Sprintf(`{"Authorization":%q}`, originalSecret)
	monitor := &model.ServiceMonitor{
		Name:               "FAKE_POSTGRES_CONCURRENT_MONITOR_FOR_TEST_ONLY",
		Type:               "http",
		Target:             "https://receiver-a.example/health?probe=1",
		HTTPHeaders:        originalHeaders,
		IntervalSeconds:    60,
		TimeoutSeconds:     10,
		HTTPMethod:         "GET",
		HTTPExpectedStatus: 200,
		Enabled:            true,
	}
	if err := db.Create(monitor).Error; err != nil {
		t.Fatalf("create monitor: %v", err)
	}

	rotatedHeaders := fmt.Sprintf(`{"Authorization":%q}`, rotatedSecret)
	rotationBody := fmt.Sprintf(
		`{"name":"FAKE_POSTGRES_CONCURRENT_MONITOR_FOR_TEST_ONLY","type":"http","target":"https://receiver-a.example/health?probe=1","http_headers":%q}`,
		rotatedHeaders,
	)
	retargetBody := `{"name":"FAKE_POSTGRES_CONCURRENT_MONITOR_RETARGETED_FOR_TEST_ONLY","type":"http","target":"https://receiver-b.example/collect?probe=2"}`

	type result struct {
		name string
		code int
		body string
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	for _, request := range []struct {
		name string
		body string
	}{
		{name: "rotation", body: rotationBody},
		{name: "retarget", body: retargetBody},
	} {
		request := request
		go func() {
			defer wg.Done()
			<-start
			req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/service-monitors/%d", monitor.ID), strings.NewReader(request.body))
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			r.ServeHTTP(response, req)
			results <- result{name: request.name, code: response.Code, body: response.Body.String()}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var rotationResult, retargetResult result
	for result := range results {
		switch result.name {
		case "rotation":
			rotationResult = result
		case "retarget":
			retargetResult = result
		}
	}
	if rotationResult.code != http.StatusOK && rotationResult.code != http.StatusConflict {
		t.Fatalf("rotation status=%d body=%s", rotationResult.code, rotationResult.body)
	}
	if retargetResult.code != http.StatusConflict {
		t.Fatalf("retarget status=%d body=%s", retargetResult.code, retargetResult.body)
	}
	var conflict struct {
		Data struct {
			Reason struct {
				Code string `json:"code"`
			} `json:"reason"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(retargetResult.body), &conflict); err != nil {
		t.Fatalf("decode retarget conflict: %v", err)
	}
	if conflict.Data.Reason.Code != serviceMonitorHeadersRetargetedCode &&
		conflict.Data.Reason.Code != serviceMonitorConcurrentUpdateCode {
		t.Fatalf("retarget conflict code=%q body=%s", conflict.Data.Reason.Code, retargetResult.body)
	}

	var loaded model.ServiceMonitor
	if err := db.First(&loaded, monitor.ID).Error; err != nil {
		t.Fatalf("load monitor: %v", err)
	}
	if loaded.Target != monitor.Target {
		t.Fatalf("retarget unexpectedly committed target=%q", loaded.Target)
	}
	if loaded.HTTPHeaders != originalHeaders && loaded.HTTPHeaders != rotatedHeaders {
		t.Fatalf("concurrent updates produced an unexpected header state: got %q", loaded.HTTPHeaders)
	}
}
