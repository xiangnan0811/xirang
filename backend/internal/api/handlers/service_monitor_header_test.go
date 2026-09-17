package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/model"

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

func TestServiceMonitorConcurrentRetargetAndHeaderRotationRemainAtomic(t *testing.T) {
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
		`{"name":"FAKE_CONCURRENT_MONITOR_FOR_TEST_ONLY","type":"http","target":"https://receiver-a.example/health?probe=1","http_headers":%q}`,
		rotatedHeaders,
	)
	retargetBody := `{"name":"FAKE_CONCURRENT_MONITOR_RETARGETED_FOR_TEST_ONLY","type":"http","target":"https://receiver-b.example/collect?probe=2"}`

	type result struct {
		name string
		code int
		body string
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, request := range []struct {
		name string
		body string
	}{
		{name: "rotation", body: rotationBody},
		{name: "retarget", body: retargetBody},
	} {
		request := request
		go func() {
			<-start
			req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/service-monitors/%d", monitor.ID), strings.NewReader(request.body))
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			r.ServeHTTP(response, req)
			results <- result{name: request.name, code: response.Code, body: response.Body.String()}
		}()
	}
	close(start)

	var rotationResult, retargetResult result
	for range 2 {
		switch result := <-results; result.name {
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
func TestServiceMonitorRetargetRequiresExplicitHeaderDecision(t *testing.T) {
	db := openServiceMonitorTestDB(t)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewServiceMonitorHandler(db, nil)
	r.PUT("/service-monitors/:id", handler.Update)

	const secret = "FAKE_RETARGET_SECRET_FOR_TEST_ONLY"
	monitor := &model.ServiceMonitor{
		Name:               "FAKE_RETARGET_MONITOR_FOR_TEST_ONLY",
		Type:               "http",
		Target:             "https://receiver-a.example/health?probe=1",
		HTTPHeaders:        fmt.Sprintf(`{"Authorization":%q}`, secret),
		IntervalSeconds:    60,
		TimeoutSeconds:     10,
		HTTPMethod:         "GET",
		HTTPExpectedStatus: 200,
		Enabled:            true,
	}
	if err := db.Create(monitor).Error; err != nil {
		t.Fatalf("create monitor: %v", err)
	}

	retarget := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/service-monitors/%d", monitor.ID), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		r.ServeHTTP(response, req)
		return response
	}

	rejected := retarget(`{"name":"FAKE_RETARGETED_WITHOUT_HEADERS_FOR_TEST_ONLY","type":"http","target":"https://receiver-b.example/collect?probe=2"}`)
	if rejected.Code != http.StatusConflict {
		t.Fatalf("omitted retarget status=%d body=%s", rejected.Code, rejected.Body.String())
	}
	var conflict struct {
		Data struct {
			Reason struct {
				Code string `json:"code"`
			} `json:"reason"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rejected.Body.Bytes(), &conflict); err != nil {
		t.Fatalf("decode retarget conflict: %v", err)
	}
	if conflict.Data.Reason.Code != serviceMonitorHeadersRetargetedCode {
		t.Fatalf("retarget conflict code=%q want %q", conflict.Data.Reason.Code, serviceMonitorHeadersRetargetedCode)
	}
	var unchanged model.ServiceMonitor
	if err := db.First(&unchanged, monitor.ID).Error; err != nil {
		t.Fatalf("load rejected monitor: %v", err)
	}
	if unchanged.Target != monitor.Target || unchanged.HTTPHeaders != fmt.Sprintf(`{"Authorization":%q}`, secret) {
		t.Fatalf("rejected retarget changed persisted use: %+v", unchanged)
	}

	cleared := retarget(`{"name":"FAKE_RETARGETED_WITH_CLEAR_FOR_TEST_ONLY","type":"http","target":"https://receiver-b.example/collect?probe=2","http_headers":"{}"}`)
	if cleared.Code != http.StatusOK {
		t.Fatalf("explicit clear retarget status=%d body=%s", cleared.Code, cleared.Body.String())
	}
	var afterClear model.ServiceMonitor
	if err := db.First(&afterClear, monitor.ID).Error; err != nil {
		t.Fatalf("load cleared monitor: %v", err)
	}
	if afterClear.Target != "https://receiver-b.example/collect?probe=2" || afterClear.HTTPHeaders != "{}" {
		t.Fatalf("explicit clear did not commit target/header state: %+v", afterClear)
	}

	replacedHeaders := `{"Authorization":"FAKE_REPLACED_RETARGET_SECRET_FOR_TEST_ONLY"}`
	replaced := retarget(fmt.Sprintf(`{"name":"FAKE_RETARGETED_WITH_REPLACEMENT_FOR_TEST_ONLY","type":"http","target":"https://receiver-c.example/collect?probe=3","http_headers":%q}`, replacedHeaders))
	if replaced.Code != http.StatusOK {
		t.Fatalf("explicit replacement retarget status=%d body=%s", replaced.Code, replaced.Body.String())
	}
	var afterReplacement model.ServiceMonitor
	if err := db.First(&afterReplacement, monitor.ID).Error; err != nil {
		t.Fatalf("load replacement monitor: %v", err)
	}
	if afterReplacement.Target != "https://receiver-c.example/collect?probe=3" || afterReplacement.HTTPHeaders != replacedHeaders {
		t.Fatalf("explicit replacement did not commit target/header state: %+v", afterReplacement)
	}

	methodChange := retarget(`{"name":"FAKE_METHOD_CHANGE_WITHOUT_HEADERS_FOR_TEST_ONLY","type":"http","target":"https://receiver-c.example/collect?probe=3","http_method":"POST"}`)
	if methodChange.Code != http.StatusConflict {
		t.Fatalf("method-change status=%d body=%s", methodChange.Code, methodChange.Body.String())
	}
}
