// Package uptime implements HTTP/TCP uptime probing for ServiceMonitors.
// Probes run from the Xirang server itself (no SSH), collect hourly uptime
// samples, and trigger alerts on status transitions.
package uptime

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// AlertFn is the callback for service status transitions (up->down, down->up).
// oldStatus and newStatus are "up", "down", or "unknown".
type AlertFn func(monitor model.ServiceMonitor, oldStatus, newStatus string)

// Prober periodically probes all enabled ServiceMonitors via HTTP/TCP.
type Prober struct {
	db       *gorm.DB
	interval time.Duration
	alertFn  AlertFn
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewProber creates a new uptime Prober. interval is the probe cycle
// duration (typically 60s).
func NewProber(db *gorm.DB, interval time.Duration) *Prober {
	return &Prober{
		db:       db,
		interval: interval,
		done:     make(chan struct{}),
	}
}

// SetAlertCallback configures the status-transition callback.
// Must be called before Start/Run.
func (p *Prober) SetAlertCallback(fn AlertFn) {
	p.alertFn = fn
}

// Start begins the probe loop in a background goroutine.
func (p *Prober) Start(ctx context.Context) {
	probeCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	go p.run(probeCtx)
}

// Run starts the probe loop and blocks until ctx is done.
// Implements lifecycle.Worker.
func (p *Prober) Run(ctx context.Context) {
	p.Start(ctx)
	<-ctx.Done()
}

// Shutdown signals the prober to stop and waits for completion.
func (p *Prober) Shutdown(ctx context.Context) error {
	if p.cancel != nil {
		p.cancel()
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Prober) run(ctx context.Context) {
	defer close(p.done)

	// Run immediately on start.
	p.probeAll()

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.probeAll()
		}
	}
}

// probeAll queries enabled monitors and probes each one.
func (p *Prober) probeAll() {
	var monitors []model.ServiceMonitor
	if err := p.db.Where("enabled = ?", true).Find(&monitors).Error; err != nil {
		logger.Module("uptime").Warn().Err(err).Msg("查询 service_monitors 失败")
		return
	}

	// Probe sequentially to keep it simple; HTTP/TCP probes are lightweight.
	for _, m := range monitors {
		mCopy := m
		p.probeOne(mCopy)
	}
}

// probeOne probes a single monitor, updates samples and status.
func (p *Prober) probeOne(monitor model.ServiceMonitor) {
	log := logger.Module("uptime").With().Uint("monitor_id", monitor.ID).Str("name", monitor.Name).Logger()

	var ok bool
	var latencyMs int64

	switch monitor.Type {
	case "http":
		ok, latencyMs, _ = p.probeHTTP(monitor)
	case "tcp":
		ok, latencyMs, _ = p.probeTCP(monitor)
	default:
		log.Warn().Str("type", monitor.Type).Msg("不支持的 monitor type")
		return
	}

	now := time.Now()
	oldStatus := monitor.LastStatus
	newStatus := "down"
	if ok {
		newStatus = "up"
	}

	// Upsert hourly sample.
	if err := p.upsertSample(monitor.ID, now, ok); err != nil {
		log.Warn().Err(err).Msg("写入 uptime sample 失败")
	}

	// Recalculate trailing 24h uptime_pct.
	uptimePct := p.calcUptimePct(monitor.ID)

	// Update monitor status row.
	updates := map[string]interface{}{
		"last_status":    newStatus,
		"last_checked_at": now,
		"uptime_pct":     uptimePct,
	}
	if err := p.db.Model(&model.ServiceMonitor{}).Where("id = ?", monitor.ID).Updates(updates).Error; err != nil {
		log.Warn().Err(err).Msg("更新 service_monitor 状态失败")
	}

	// Update in-memory copy for the alert callback.
	monitor.LastStatus = newStatus
	monitor.LastCheckedAt = &now
	monitor.UptimePct = uptimePct

	// Log probe result.
	if ok {
		log.Debug().Int64("latency_ms", latencyMs).Float64("uptime_pct", uptimePct).Msg("探测成功")
	} else {
		log.Warn().Float64("uptime_pct", uptimePct).Msg("探测失败")
	}

	// Status transition alerting.
	if oldStatus != newStatus && p.alertFn != nil {
		p.alertFn(monitor, oldStatus, newStatus)
	}
}

// probeHTTP sends an HTTP request and checks the response status code.
func (p *Prober) probeHTTP(monitor model.ServiceMonitor) (ok bool, latencyMs int64, err error) {
	method := strings.ToUpper(strings.TrimSpace(monitor.HTTPMethod))
	if method == "" {
		method = "GET"
	}
	timeout := time.Duration(monitor.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	client := &http.Client{
		Timeout: timeout,
		// Don't follow redirects — we only care about the direct target.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequest(method, monitor.Target, nil)
	if err != nil {
		return false, 0, err
	}

	// Parse and set optional HTTP headers.
	if headers := strings.TrimSpace(monitor.HTTPHeaders); headers != "" && headers != "{}" {
		if err := parseHeaders(headers, req); err != nil {
			logger.Module("uptime").Warn().Uint("monitor_id", monitor.ID).Err(err).Msg("解析 http_headers 失败")
		}
	}

	start := time.Now()
	resp, doErr := client.Do(req)
	latencyMs = time.Since(start).Milliseconds()

	if doErr != nil {
		return false, latencyMs, doErr
	}
	defer resp.Body.Close()

	expectedStatus := monitor.HTTPExpectedStatus
	if expectedStatus <= 0 {
		expectedStatus = 200
	}
	if resp.StatusCode == expectedStatus {
		return true, latencyMs, nil
	}
	return false, latencyMs, fmt.Errorf("HTTP status %d (expected %d)", resp.StatusCode, expectedStatus)
}

// probeTCP dials a TCP connection to check connectivity.
func (p *Prober) probeTCP(monitor model.ServiceMonitor) (ok bool, latencyMs int64, err error) {
	timeout := time.Duration(monitor.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	start := time.Now()
	conn, dialErr := net.DialTimeout("tcp", monitor.Target, timeout)
	latencyMs = time.Since(start).Milliseconds()

	if dialErr != nil {
		return false, latencyMs, dialErr
	}
	conn.Close()
	return true, latencyMs, nil
}

// upsertSample upserts a row into service_uptime_samples for the current hour.
// It increments probe_count, and conditionally increments probe_ok.
// Uses a mutex to prevent concurrent upserts for the same monitor, since this
// code probes sequentially there is no real contention, but keeping the lock
// is defense-in-depth against future concurrent probing.
var upsertMu sync.Mutex

func (p *Prober) upsertSample(monitorID uint, now time.Time, ok bool) error {
	upsertMu.Lock()
	defer upsertMu.Unlock()

	hour := now.Truncate(time.Hour)

	var sample model.ServiceUptimeSample
	err := p.db.Where("monitor_id = ? AND hour = ?", monitorID, hour).First(&sample).Error
	if err == gorm.ErrRecordNotFound {
		// Create new row.
		count := 1
		probeOK := 0
		if ok {
			probeOK = 1
		}
		sample = model.ServiceUptimeSample{
			MonitorID:  monitorID,
			Hour:       hour,
			ProbeCount: count,
			ProbeOK:    probeOK,
		}
		return p.db.Create(&sample).Error
	}
	if err != nil {
		return err
	}

	// Update existing row.
	updates := map[string]interface{}{
		"probe_count": sample.ProbeCount + 1,
	}
	if ok {
		updates["probe_ok"] = sample.ProbeOK + 1
	}
	return p.db.Model(&sample).Updates(updates).Error
}

// calcUptimePct computes trailing 24h uptime percentage.
func (p *Prober) calcUptimePct(monitorID uint) float64 {
	cutoff := time.Now().Add(-24 * time.Hour)

	var result struct {
		Total int
		OK    int
	}
	err := p.db.Model(&model.ServiceUptimeSample{}).
		Select("COALESCE(SUM(probe_count), 0) as total, COALESCE(SUM(probe_ok), 0) as ok").
		Where("monitor_id = ? AND hour >= ?", monitorID, cutoff).
		Scan(&result).Error
	if err != nil || result.Total == 0 {
		return 0
	}
	return float64(result.OK) / float64(result.Total) * 100
}

// parseHeaders parses a JSON string like {"X-Custom": "value"} into the request headers.
func parseHeaders(raw string, req *http.Request) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil
	}
	// Simple manual parsing for safe header keys.
	// Strip outer braces.
	inner := strings.TrimSpace(raw)
	if !strings.HasPrefix(inner, "{") || !strings.HasSuffix(inner, "}") {
		return fmt.Errorf("invalid JSON object")
	}
	inner = inner[1 : len(inner)-1]
	if strings.TrimSpace(inner) == "" {
		return nil
	}
	// Split by comma, handle quoted strings simply.
	pairs := splitJSONPairs(inner)
	for _, pair := range pairs {
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		// Unquote strings.
		key = strings.Trim(key, "\"")
		value = strings.Trim(value, "\"")
		if key != "" {
			req.Header.Set(key, value)
		}
	}
	return nil
}

// splitJSONPairs is a minimal JSON key:value pair splitter that handles nested
// quotes within simple string values. Not a full JSON parser — adequate for
// the HTTP header use case.
func splitJSONPairs(s string) []string {
	var result []string
	depth := 0
	inString := false
	start := 0
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '"' && (i == 0 || s[i-1] != '\\') {
			inString = !inString
		}
		if !inString {
			if ch == '{' || ch == '[' {
				depth++
			} else if ch == '}' || ch == ']' {
				depth--
			} else if ch == ',' && depth == 0 {
				result = append(result, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	if start < len(s) {
		result = append(result, strings.TrimSpace(s[start:]))
	}
	return result
}
