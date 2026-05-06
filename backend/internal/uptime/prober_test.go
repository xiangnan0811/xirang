package uptime

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.ServiceMonitor{}, &model.ServiceUptimeSample{}, &model.Alert{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	return db
}

func TestProbeHTTP_Up(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	p := &Prober{}
	monitor := model.ServiceMonitor{
		Type:               "http",
		Target:             ts.URL,
		TimeoutSeconds:     5,
		HTTPMethod:         "GET",
		HTTPExpectedStatus: 200,
	}

	ok, latencyMs, err := p.probeHTTP(monitor)
	if err != nil {
		t.Fatalf("HTTP probe 不应失败: %v", err)
	}
	if !ok {
		t.Fatal("期望 probe 成功 (ok=true)")
	}
	if latencyMs < 0 {
		t.Errorf("latency %d < 0", latencyMs)
	}
}

func TestProbeHTTP_Down(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	p := &Prober{}
	monitor := model.ServiceMonitor{
		Type:               "http",
		Target:             ts.URL,
		TimeoutSeconds:     5,
		HTTPMethod:         "GET",
		HTTPExpectedStatus: 200,
	}

	ok, _, _ := p.probeHTTP(monitor)
	if ok {
		t.Fatal("期望 probe 失败 (ok=false)，因为 status 500 != 200")
	}
}

func TestProbeHTTP_PostMethod(t *testing.T) {
	var gotMethod string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	p := &Prober{}
	monitor := model.ServiceMonitor{
		Type:               "http",
		Target:             ts.URL,
		TimeoutSeconds:     5,
		HTTPMethod:         "POST",
		HTTPExpectedStatus: 200,
	}

	ok, _, _ := p.probeHTTP(monitor)
	if !ok {
		t.Fatal("期望 probe 成功")
	}
	if gotMethod != "POST" {
		t.Errorf("期望方法 POST，实际 %s", gotMethod)
	}
}

func TestProbeHTTP_WithHeaders(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	p := &Prober{}
	monitor := model.ServiceMonitor{
		Type:               "http",
		Target:             ts.URL,
		TimeoutSeconds:     5,
		HTTPMethod:         "GET",
		HTTPExpectedStatus: 200,
		HTTPHeaders:        `{"Authorization":"Bearer test-token"}`,
	}

	ok, _, _ := p.probeHTTP(monitor)
	if !ok {
		t.Fatal("期望 probe 成功")
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("期望 Authorization 头为 'Bearer test-token'，实际 %q", gotAuth)
	}
}

func TestProbeTCP_Up(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("创建 TCP listener 失败: %v", err)
	}
	defer ln.Close() //nolint:errcheck

	// Accept connections in background so the listener isn't blocked.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, _ := ln.Accept()
		if conn != nil {
			conn.Close() //nolint:errcheck
		}
	}()

	p := &Prober{}
	addr := ln.Addr().String()
	monitor := model.ServiceMonitor{
		Type:           "tcp",
		Target:         addr,
		TimeoutSeconds: 5,
	}

	ok, latencyMs, err := p.probeTCP(monitor)
	if err != nil {
		t.Fatalf("TCP probe 不应失败: %v", err)
	}
	if !ok {
		t.Fatal("期望 probe 成功 (ok=true)")
	}
	if latencyMs < 0 {
		t.Errorf("latency %d < 0", latencyMs)
	}
	wg.Wait()
}

func TestProbeTCP_Down(t *testing.T) {
	// Find an address that is unlikely to accept connections.
	// Use a random high port on localhost.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("创建临时 listener 失败: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close() //nolint:errcheck // Close immediately so the port is free but nothing is listening.

	p := &Prober{}
	monitor := model.ServiceMonitor{
		Type:           "tcp",
		Target:         addr,
		TimeoutSeconds: 1,
	}

	ok, _, err := p.probeTCP(monitor)
	if err == nil {
		t.Fatal("期望 TCP probe 返回 error（连接被拒绝）")
	}
	if ok {
		t.Fatal("期望 probe 失败 (ok=false)")
	}
}

func TestUpsertSample(t *testing.T) {
	db := openTestDB(t)
	p := NewProber(db, 60*time.Second)

	monitorID := uint(1)
	now := time.Now()

	// First probe — creates a new row.
	if err := p.upsertSample(monitorID, now, true); err != nil {
		t.Fatalf("首次 upsertSample 失败: %v", err)
	}

	var samples []model.ServiceUptimeSample
	if err := db.Where("monitor_id = ?", monitorID).Find(&samples).Error; err != nil {
		t.Fatalf("查询 samples 失败: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("期望 1 条 sample，实际 %d", len(samples))
	}
	s := samples[0]
	if s.ProbeCount != 1 {
		t.Errorf("期望 probe_count=1，实际 %d", s.ProbeCount)
	}
	if s.ProbeOK != 1 {
		t.Errorf("期望 probe_ok=1，实际 %d", s.ProbeOK)
	}

	// Second probe — same hour, fails.
	if err := p.upsertSample(monitorID, now, false); err != nil {
		t.Fatalf("第二次 upsertSample 失败: %v", err)
	}

	samples = nil
	if err := db.Where("monitor_id = ?", monitorID).Find(&samples).Error; err != nil {
		t.Fatalf("再次查询 samples 失败: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("期望仍是 1 条 sample，实际 %d", len(samples))
	}
	s = samples[0]
	if s.ProbeCount != 2 {
		t.Errorf("期望 probe_count=2，实际 %d", s.ProbeCount)
	}
	if s.ProbeOK != 1 {
		t.Errorf("期望 probe_ok=1 (第二次失败不增加 ok)，实际 %d", s.ProbeOK)
	}

	// Third probe — different hour.
	later := now.Add(2 * time.Hour)
	if err := p.upsertSample(monitorID, later, true); err != nil {
		t.Fatalf("不同小时 upsertSample 失败: %v", err)
	}

	samples = nil
	if err := db.Where("monitor_id = ?", monitorID).Order("hour asc").Find(&samples).Error; err != nil {
		t.Fatalf("第三次查询 samples 失败: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("期望 2 条 sample，实际 %d", len(samples))
	}
}

func TestCalcUptimePct(t *testing.T) {
	db := openTestDB(t)
	p := NewProber(db, 60*time.Second)

	monitorID := uint(1)
	now := time.Now()

	// Seed samples: 3 hours with 2/2, 1/2, 2/2 → 5/6 = 83.33%
	samples := []model.ServiceUptimeSample{
		{MonitorID: monitorID, Hour: now.Add(-3 * time.Hour).Truncate(time.Hour), ProbeCount: 2, ProbeOK: 2},
		{MonitorID: monitorID, Hour: now.Add(-2 * time.Hour).Truncate(time.Hour), ProbeCount: 2, ProbeOK: 1},
		{MonitorID: monitorID, Hour: now.Add(-1 * time.Hour).Truncate(time.Hour), ProbeCount: 2, ProbeOK: 2},
	}
	for i := range samples {
		if err := db.Create(&samples[i]).Error; err != nil {
			t.Fatalf("seed sample 失败: %v", err)
		}
	}

	pct := p.calcUptimePct(monitorID)
	expected := float64(5) / float64(6) * 100
	if pct < expected-0.01 || pct > expected+0.01 {
		t.Errorf("期望 uptime_pct ≈ %.2f，实际 %.2f", expected, pct)
	}
}

func TestCalcUptimePct_NoSamples(t *testing.T) {
	db := openTestDB(t)
	p := NewProber(db, 60*time.Second)

	pct := p.calcUptimePct(999)
	if pct != 0 {
		t.Errorf("无 sample 时期望 uptime_pct=0，实际 %.2f", pct)
	}
}

func TestCalcUptimePct_OldSamplesExcluded(t *testing.T) {
	db := openTestDB(t)
	p := NewProber(db, 60*time.Second)

	monitorID := uint(1)
	now := time.Now()

	// Old sample (30h ago, outside 24h window) — should be excluded.
	old := model.ServiceUptimeSample{
		MonitorID:  monitorID,
		Hour:       now.Add(-30 * time.Hour).Truncate(time.Hour),
		ProbeCount: 100,
		ProbeOK:    100,
	}
	if err := db.Create(&old).Error; err != nil {
		t.Fatalf("seed old sample 失败: %v", err)
	}
	// Recent sample (1h ago).
	recent := model.ServiceUptimeSample{
		MonitorID:  monitorID,
		Hour:       now.Add(-1 * time.Hour).Truncate(time.Hour),
		ProbeCount: 1,
		ProbeOK:    0,
	}
	if err := db.Create(&recent).Error; err != nil {
		t.Fatalf("seed recent sample 失败: %v", err)
	}

	pct := p.calcUptimePct(monitorID)
	if pct != 0 {
		t.Errorf("期望 uptime_pct=0 (老 sample 已被排除，新 sample 0 成功)，实际 %.2f", pct)
	}
}

func TestProbeOne_StatusTransition(t *testing.T) {
	db := openTestDB(t)
	p := NewProber(db, 60*time.Second)

	var transitions []struct {
		monitor   model.ServiceMonitor
		oldStatus string
		newStatus string
	}
	var mu sync.Mutex
	p.SetAlertCallback(func(monitor model.ServiceMonitor, oldStatus, newStatus string) {
		mu.Lock()
		defer mu.Unlock()
		transitions = append(transitions, struct {
			monitor   model.ServiceMonitor
			oldStatus string
			newStatus string
		}{monitor, oldStatus, newStatus})
	})

	// Create a monitor that is "up".
	monitor := model.ServiceMonitor{
		Name:       "test-svc",
		Type:       "tcp",
		Target:     "127.0.0.1:19999", // nothing listening here
		Enabled:    true,
		LastStatus: "up",
	}
	if err := db.Create(&monitor).Error; err != nil {
		t.Fatalf("创建 monitor 失败: %v", err)
	}

	// Probe it — should go down.
	p.probeOne(monitor)

	mu.Lock()
	if len(transitions) == 0 {
		t.Fatal("期望至少一次状态转换回调")
	}
	last := transitions[len(transitions)-1]
	if last.oldStatus != "up" || last.newStatus != "down" {
		t.Errorf("期望 up→down，实际 %s→%s", last.oldStatus, last.newStatus)
	}
	transitions = nil
	mu.Unlock()

	// Verify the monitor was updated to "down".
	var updated model.ServiceMonitor
	if err := db.First(&updated, monitor.ID).Error; err != nil {
		t.Fatalf("查询更新后的 monitor 失败: %v", err)
	}
	if updated.LastStatus != "down" {
		t.Errorf("期望 last_status=down，实际 %s", updated.LastStatus)
	}
	if updated.LastCheckedAt == nil {
		t.Error("期望 last_checked_at 不为 nil")
	}
}

func TestProbeOne_DisabledMonitorSkipped(t *testing.T) {
	db := openTestDB(t)
	p := NewProber(db, 60*time.Second)

	var called bool
	p.SetAlertCallback(func(monitor model.ServiceMonitor, oldStatus, newStatus string) {
		called = true
	})

	// Create a disabled monitor. GORM omits zero-value bool fields on INSERT,
	// so we must explicitly set enabled=0 after creation.
	monitor := model.ServiceMonitor{
		Name:       "disabled-svc",
		Type:       "tcp",
		Target:     "127.0.0.1:19999",
		Enabled:    false,
		LastStatus: "unknown",
	}
	if err := db.Create(&monitor).Error; err != nil {
		t.Fatalf("创建 disabled monitor 失败: %v", err)
	}
	// Force enabled=false because GORM's default:true overrides zero-value bool.
	if err := db.Model(&monitor).Update("enabled", false).Error; err != nil {
		t.Fatalf("禁用 monitor 失败: %v", err)
	}

	// probeAll should skip disabled monitors.
	p.probeAll()

	if called {
		t.Error("disabled monitor 不应触发状态转换回调")
	}

	// Verify monitor status unchanged.
	var updated model.ServiceMonitor
	if err := db.First(&updated, monitor.ID).Error; err != nil {
		t.Fatalf("查询 monitor 失败: %v", err)
	}
	if updated.LastStatus != "unknown" {
		t.Errorf("期望 last_status 保持 unknown，实际 %s", updated.LastStatus)
	}
}

func TestProbeHTTP_Timeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	p := &Prober{}
	monitor := model.ServiceMonitor{
		Type:               "http",
		Target:             ts.URL,
		TimeoutSeconds:     1,
		HTTPMethod:         "GET",
		HTTPExpectedStatus: 200,
	}

	ok, _, err := p.probeHTTP(monitor)
	// Either error (timeout) or return with ok=false.
	if err == nil && ok {
		t.Error("超时的 HTTP probe 不应返回成功")
	}
}

func TestSplitJSONPairs(t *testing.T) {
	tests := []struct {
		input    string
		wantKeys []string
	}{
		{`"X-Api-Key":"abc123"`, []string{"X-Api-Key"}},
		{`"Authorization":"Bearer xyz","X-Custom":"val"`, []string{"Authorization", "X-Custom"}},
		{`"key1":"val1", "key2": "val2"`, []string{"key1", "key2"}},
	}
	for _, tt := range tests {
		pairs := splitJSONPairs(tt.input)
		if len(pairs) != len(tt.wantKeys) {
			t.Errorf("splitJSONPairs(%q) 期望 %d pairs，实际 %d: %v", tt.input, len(tt.wantKeys), len(pairs), pairs)
			continue
		}
		for i, key := range tt.wantKeys {
			if !strings.Contains(pairs[i], key) {
				t.Errorf("pair %d 期望包含 %q，实际 %q", i, key, pairs[i])
			}
		}
	}
}

func TestParseHeaders(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	if err := parseHeaders(`{"Authorization":"Bearer test"}`, req); err != nil {
		t.Fatalf("parseHeaders 失败: %v", err)
	}
	if req.Header.Get("Authorization") != "Bearer test" {
		t.Errorf("期望 Authorization 头为 'Bearer test'，实际 %q", req.Header.Get("Authorization"))
	}

	// Empty JSON object should be no-op.
	req2, _ := http.NewRequest("GET", "http://example.com", nil)
	if err := parseHeaders("{}", req2); err != nil {
		t.Fatalf("parseHeaders({}) 失败: %v", err)
	}
}
