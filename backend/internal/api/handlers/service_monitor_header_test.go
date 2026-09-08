package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestServiceMonitorHeadersAreWriteOnlyAndUpdateSemanticsAreExplicit(t *testing.T) {
	db := openServiceMonitorTestDB(t)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewServiceMonitorHandler(db, nil)
	r.POST("/service-monitors", handler.Create)
	r.PUT("/service-monitors/:id", handler.Update)
	r.GET("/service-monitors/:id", handler.Get)

	const secret = "FAKE_MONITOR_HEADER_SECRET_FOR_TEST_ONLY"
	create := httptest.NewRequest(http.MethodPost, "/service-monitors", strings.NewReader(`{"name":"FAKE_WRITE_ONLY_MONITOR_FOR_TEST_ONLY","type":"http","target":"https://example.invalid","http_headers":"{\"Authorization\":\"`+secret+`\"}"}`))
	create.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	r.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), secret) || strings.Contains(created.Body.String(), "\"http_headers\":\"") || strings.Contains(created.Body.String(), "enc:v2:") {
		t.Fatalf("create response leaked header value/ciphertext: %s", created.Body.String())
	}
	var envelope struct {
		Data struct {
			ID                    uint     `json:"id"`
			HTTPHeaderNames       []string `json:"http_header_names"`
			HTTPHeadersConfigured bool     `json:"http_headers_configured"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if envelope.Data.ID == 0 || len(envelope.Data.HTTPHeaderNames) != 1 || envelope.Data.HTTPHeaderNames[0] != "Authorization" || !envelope.Data.HTTPHeadersConfigured {
		t.Fatalf("unexpected header DTO: %+v", envelope.Data)
	}

	raw := db.Session(&gorm.Session{SkipHooks: true})
	var stored string
	if err := raw.Table("service_monitors").Select("http_headers").Where("id = ?", envelope.Data.ID).Scan(&stored).Error; err != nil {
		t.Fatalf("read raw headers: %v", err)
	}
	if !strings.HasPrefix(stored, "enc:v2:") || strings.Contains(stored, secret) {
		t.Fatalf("raw header is not encrypted: %q", stored)
	}
	// Omitting http_headers must retain the existing secret.
	update := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/service-monitors/%d", envelope.Data.ID), strings.NewReader(`{"name":"FAKE_WRITE_ONLY_MONITOR_RENAMED_FOR_TEST_ONLY","type":"http","target":"https://example.invalid"}`))
	update.Header.Set("Content-Type", "application/json")
	updated := httptest.NewRecorder()
	r.ServeHTTP(updated, update)
	if updated.Code != http.StatusOK {
		t.Fatalf("preserve update status=%d body=%s", updated.Code, updated.Body.String())
	}
	if err := raw.Table("service_monitors").Select("http_headers").Where("id = ?", envelope.Data.ID).Scan(&stored).Error; err != nil {
		t.Fatalf("read preserved headers: %v", err)
	}
	if !strings.HasPrefix(stored, "enc:v2:") || strings.Contains(stored, secret) {
		t.Fatalf("preserved raw header is not encrypted: %q", stored)
	}
	var preserved model.ServiceMonitor
	if err := db.First(&preserved, envelope.Data.ID).Error; err != nil {
		t.Fatalf("load preserved monitor: %v", err)
	}
	wantHeaders := fmt.Sprintf(`{"Authorization":%q}`, secret)
	if preserved.HTTPHeaders != wantHeaders {
		t.Fatalf("omitted headers changed secret: got %q, want %q", preserved.HTTPHeaders, wantHeaders)
	}

	// Explicit {} clears the configured header set, while still storing an
	// encrypted representation rather than plaintext JSON.
	clearReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/service-monitors/%d", envelope.Data.ID), strings.NewReader(`{"name":"FAKE_WRITE_ONLY_MONITOR_RENAMED_FOR_TEST_ONLY","type":"http","target":"https://example.invalid","http_headers":"{}"}`))
	clearReq.Header.Set("Content-Type", "application/json")
	cleared := httptest.NewRecorder()
	r.ServeHTTP(cleared, clearReq)
	if cleared.Code != http.StatusOK {
		t.Fatalf("clear update status=%d body=%s", cleared.Code, cleared.Body.String())
	}
	if strings.Contains(cleared.Body.String(), secret) || strings.Contains(cleared.Body.String(), "\"http_headers\":\"") || strings.Contains(cleared.Body.String(), "enc:v2:") {
		t.Fatalf("clear response leaked header value/ciphertext: %s", cleared.Body.String())
	}
	if err := raw.Table("service_monitors").Select("http_headers").Where("id = ?", envelope.Data.ID).Scan(&stored).Error; err != nil {
		t.Fatalf("read cleared headers: %v", err)
	}
	if !strings.HasPrefix(stored, "enc:v2:") {
		t.Fatalf("cleared headers not encrypted: %q", stored)
	}
	var loaded model.ServiceMonitor
	if err := db.First(&loaded, envelope.Data.ID).Error; err != nil {
		t.Fatalf("load cleared monitor: %v", err)
	}
	if loaded.HTTPHeaders != "{}" {
		t.Fatalf("cleared headers=%q, want {}", loaded.HTTPHeaders)
	}
}

func TestServiceMonitorOmittedRenameCannotClobberConcurrentHeaderRotation(t *testing.T) {
	db := openServiceMonitorTestDB(t)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewServiceMonitorHandler(db, nil)
	r.PUT("/service-monitors/:id", handler.Update)

	const originalSecret = "FAKE_ORIGINAL_MONITOR_SECRET_FOR_TEST_ONLY"
	const rotatedSecret = "FAKE_ROTATED_MONITOR_SECRET_FOR_TEST_ONLY"
	originalHeaders := fmt.Sprintf(`{"Authorization":%q}`, originalSecret)
	monitor := &model.ServiceMonitor{
		Name:               "FAKE_CONCURRENT_MONITOR_FOR_TEST_ONLY",
		Type:               "http",
		Target:             "https://example.invalid",
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

	snapshotObserved := make(chan string, 1)
	queryEntered := make(chan struct{})
	releaseQuery := make(chan struct{})
	var pauseOnce sync.Once
	const callbackName = "test:service-monitor-rename-query-pause"
	if err := db.Callback().Query().After("gorm:after_query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "service_monitors" {
			return
		}
		snapshot, ok := tx.Statement.Dest.(*model.ServiceMonitor)
		if !ok {
			return
		}
		pauseOnce.Do(func() {
			snapshotObserved <- snapshot.HTTPHeaders
			close(queryEntered)
			<-releaseQuery
		})
	}); err != nil {
		t.Fatalf("register query callback: %v", err)
	}
	defer func() {
		if err := db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove rotation callback: %v", err)
		}
	}()

	renameDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/service-monitors/%d", monitor.ID), strings.NewReader(`{"name":"FAKE_CONCURRENT_MONITOR_RENAMED_FOR_TEST_ONLY","type":"http","target":"https://example.invalid"}`))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		r.ServeHTTP(response, req)
		renameDone <- response
	}()

	select {
	case <-queryEntered:
	case <-time.After(5 * time.Second):
		close(releaseQuery)
		t.Fatal("rename did not load the monitor before rotation")
	}
	select {
	case observed := <-snapshotObserved:
		if observed != originalHeaders {
			close(releaseQuery)
			t.Fatalf("rename loaded headers=%q, want original %q", observed, originalHeaders)
		}
	case <-time.After(5 * time.Second):
		close(releaseQuery)
		t.Fatal("rename snapshot was not observed")
	}

	wantHeaders := fmt.Sprintf(`{"Authorization":%q}`, rotatedSecret)
	rotated, err := secure.EncryptString(wantHeaders)
	if err != nil {
		close(releaseQuery)
		t.Fatalf("encrypt rotated headers: %v", err)
	}
	if result := db.Session(&gorm.Session{SkipHooks: true}).
		Table("service_monitors").
		Where("id = ?", monitor.ID).
		Update("http_headers", rotated); result.Error != nil {
		close(releaseQuery)
		t.Fatalf("rotate headers: %v", result.Error)
	}
	close(releaseQuery)

	var response *httptest.ResponseRecorder
	select {
	case response = <-renameDone:
	case <-time.After(5 * time.Second):
		t.Fatal("rename did not complete after header rotation")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", response.Code, response.Body.String())
	}

	var loaded model.ServiceMonitor
	if err := db.First(&loaded, monitor.ID).Error; err != nil {
		t.Fatalf("load rotated monitor: %v", err)
	}
	if loaded.HTTPHeaders != wantHeaders {
		t.Fatalf("omitted rename clobbered rotated headers: got %q, want %q", loaded.HTTPHeaders, wantHeaders)
	}
}
