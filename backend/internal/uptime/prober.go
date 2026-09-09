// Package uptime implements HTTP/TCP uptime probing for ServiceMonitors.
// Probes run from the Xirang server itself (no SSH), collect hourly uptime
// samples, and trigger alerts on status transitions.
package uptime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AlertFn is the callback for service status transitions (up->down, down->up).
// oldStatus and newStatus are "up", "down", or "unknown".
type AlertFn func(monitor model.ServiceMonitor, oldStatus, newStatus string)

var errMonitorStale = errors.New("service monitor configuration changed")

const (
	defaultProbeWorkers = 4
	defaultProbeQueue   = 32
	minimumScanInterval = time.Second
)

type probeJob struct {
	monitor model.ServiceMonitor
}

type monitorScheduleState struct {
	initialized bool
	enabled     bool
	interval    int
	lastChecked *time.Time
	target      string
	monitorType string
	nextDue     time.Time
}

// Prober periodically probes enabled ServiceMonitors via HTTP/TCP. Scheduling
// is based on each monitor's interval rather than a global ticker. A fixed
// worker pool and bounded queue prevent an unhealthy fleet from consuming an
// unbounded number of goroutines.
type Prober struct {
	db       *gorm.DB
	interval time.Duration // retained for constructor compatibility; scan cadence is bounded below
	alertFn  AlertFn
	now      func() time.Time

	workers   int
	queue     chan probeJob
	scanEvery time.Duration

	cancelMu  sync.Mutex
	cancel    context.CancelFunc
	started   chan struct{}
	done      chan struct{}
	startOnce sync.Once

	stateMu       sync.Mutex
	queued        map[uint]struct{}
	activeTargets map[string]struct{}
	schedule      map[uint]monitorScheduleState
	revisions     map[uint]uint64
	wake          chan struct{}
}

// NewProber creates a new uptime Prober. interval is retained as a scan
// preference for callers, but due times always use ServiceMonitor.IntervalSeconds.
func NewProber(db *gorm.DB, interval time.Duration) *Prober {
	if interval <= 0 {
		interval = minimumScanInterval
	}
	scanEvery := interval
	if scanEvery > minimumScanInterval {
		scanEvery = minimumScanInterval
	}
	return &Prober{
		db:            db,
		interval:      interval,
		now:           func() time.Time { return time.Now() },
		workers:       defaultProbeWorkers,
		queue:         make(chan probeJob, defaultProbeQueue),
		scanEvery:     scanEvery,
		started:       make(chan struct{}),
		done:          make(chan struct{}),
		queued:        make(map[uint]struct{}),
		activeTargets: make(map[string]struct{}),
		schedule:      make(map[uint]monitorScheduleState),
		revisions:     make(map[uint]uint64),
		wake:          make(chan struct{}, 1),
	}
}

// SetAlertCallback configures the status-transition callback. Must be called
// before Start/Run.
func (p *Prober) SetAlertCallback(fn AlertFn) {
	p.alertFn = fn
}

// SetNowForTesting replaces the scheduler clock. It is intentionally small and
// package-owned behavior is unchanged in production.
func (p *Prober) SetNowForTesting(fn func() time.Time) {
	if fn == nil {
		p.now = func() time.Time { return time.Now() }
		return
	}
	p.now = fn
}

// NotifyMonitorChange invalidates in-flight work and wakes the scheduler after
// a CRUD mutation. Callers should invoke it only after the database commit.
func (p *Prober) NotifyMonitorChange(id uint) {
	p.stateMu.Lock()
	p.revisions[id]++
	p.stateMu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Start begins the probe loop in a background goroutine.
func (p *Prober) Start(ctx context.Context) {
	p.startOnce.Do(func() {
		probeCtx, cancel := context.WithCancel(ctx)
		p.cancelMu.Lock()
		p.cancel = cancel
		p.cancelMu.Unlock()
		close(p.started)
		go p.run(probeCtx)
	})
}

// Run starts the probe loop and blocks until ctx is done. Implements
// lifecycle.Worker.
func (p *Prober) Run(ctx context.Context) {
	p.Start(ctx)
	<-ctx.Done()
}

// Shutdown signals the prober to stop and waits for the bounded worker pool.
func (p *Prober) Shutdown(ctx context.Context) error {
	select {
	case <-p.started:
	default:
		return nil
	}
	p.cancelMu.Lock()
	cancel := p.cancel
	p.cancelMu.Unlock()
	if cancel != nil {
		cancel()
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

	workerCount := p.workers
	if workerCount <= 0 {
		workerCount = defaultProbeWorkers
	}
	var workerWG sync.WaitGroup
	workerWG.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer workerWG.Done()
			p.worker(ctx)
		}()
	}

	p.scanDue(ctx)
	ticker := time.NewTicker(p.scanEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			workerWG.Wait()
			return
		case <-ticker.C:
			p.scanDue(ctx)
		case <-p.wake:
			p.scanDue(ctx)
		}
	}
}

func (p *Prober) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-p.queue:
			p.stateMu.Lock()
			delete(p.queued, job.monitor.ID)
			p.stateMu.Unlock()
			_ = p.probeOneContext(ctx, job.monitor)
			p.stateMu.Lock()
			delete(p.activeTargets, targetKey(job.monitor))
			p.stateMu.Unlock()
		}
	}
}

// probeAll is retained for package tests and compatibility. Production uses
// scanDue, which enqueues due work into the bounded pool.
func (p *Prober) probeAll() {
	p.probeAllContext(context.Background())
}

func (p *Prober) probeAllContext(ctx context.Context) {
	var monitors []model.ServiceMonitor
	if err := p.db.WithContext(ctx).Find(&monitors).Error; err != nil {
		logger.Module("uptime").Warn().Err(err).Msg("查询 service_monitors 失败")
		return
	}
	for _, monitor := range monitors {
		if err := p.probeOneContext(ctx, monitor); err != nil && !errors.Is(err, errMonitorStale) {
			logger.Module("uptime").Warn().Uint("monitor_id", monitor.ID).Err(err).Msg("探测 service monitor 失败")
		}
	}
}

func (p *Prober) scanDue(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	var monitors []model.ServiceMonitor
	if err := p.db.WithContext(ctx).Order("id asc").Find(&monitors).Error; err != nil {
		if ctx.Err() == nil {
			logger.Module("uptime").Warn().Err(err).Msg("查询 service_monitors 失败")
		}
		return
	}

	now := p.nowTime()
	seen := make(map[uint]struct{}, len(monitors))
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	for _, monitor := range monitors {
		seen[monitor.ID] = struct{}{}
		state, exists := p.schedule[monitor.ID]
		if !exists {
			state = newScheduleState(monitor, now)
		} else {
			state = updateScheduleState(state, monitor, now)
		}
		if monitor.Enabled && !state.nextDue.IsZero() && !state.nextDue.After(now) {
			if p.enqueueLocked(monitor) {
				state.nextDue = now.Add(monitorInterval(monitor))
			}
		}
		p.schedule[monitor.ID] = state
	}
	for id := range p.schedule {
		if _, ok := seen[id]; !ok {
			delete(p.schedule, id)
			delete(p.revisions, id)
		}
	}
}

func newScheduleState(monitor model.ServiceMonitor, now time.Time) monitorScheduleState {
	state := monitorScheduleState{
		initialized: true,
		enabled:     monitor.Enabled,
		interval:    monitor.IntervalSeconds,
		target:      monitor.Target,
		monitorType: monitor.Type,
	}
	state.lastChecked = cloneTime(monitor.LastCheckedAt)
	if monitor.Enabled {
		if monitor.LastCheckedAt == nil {
			state.nextDue = now
		} else {
			state.nextDue = monitor.LastCheckedAt.Add(monitorInterval(monitor))
		}
	}
	return state
}

func updateScheduleState(previous monitorScheduleState, monitor model.ServiceMonitor, now time.Time) monitorScheduleState {
	state := previous
	state.enabled = monitor.Enabled
	state.interval = monitor.IntervalSeconds
	state.target = monitor.Target
	state.monitorType = monitor.Type
	if !monitor.Enabled {
		state.nextDue = time.Time{}
		state.lastChecked = cloneTime(monitor.LastCheckedAt)
		return state
	}
	if !previous.enabled {
		state.nextDue = now // re-enable is intentionally immediate
	} else if previous.interval != monitor.IntervalSeconds {
		if monitor.LastCheckedAt == nil {
			state.nextDue = now
		} else {
			state.nextDue = monitor.LastCheckedAt.Add(monitorInterval(monitor))
		}
	} else if !sameTime(previous.lastChecked, monitor.LastCheckedAt) {
		if monitor.LastCheckedAt == nil {
			state.nextDue = now
		} else {
			state.nextDue = monitor.LastCheckedAt.Add(monitorInterval(monitor))
		}
	}
	state.lastChecked = cloneTime(monitor.LastCheckedAt)
	return state
}

func (p *Prober) enqueueLocked(monitor model.ServiceMonitor) bool {
	if _, exists := p.queued[monitor.ID]; exists {
		return false
	}
	key := targetKey(monitor)
	if _, exists := p.activeTargets[key]; exists {
		return false
	}
	if p.queue == nil {
		return false
	}
	select {
	case p.queue <- probeJob{monitor: monitor}:
		p.queued[monitor.ID] = struct{}{}
		p.activeTargets[key] = struct{}{}
		return true
	default:
		return false
	}
}

func (p *Prober) probeOne(monitor model.ServiceMonitor) {
	_ = p.probeOneContext(context.Background(), monitor)
}

func (p *Prober) probeOneContext(ctx context.Context, monitor model.ServiceMonitor) error {
	if !monitor.Enabled || ctx.Err() != nil {
		return nil
	}
	revision := p.monitorRevision(monitor.ID)
	timeout := time.Duration(monitor.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	var ok bool
	var latencyMs int64
	var probeErr error
	switch monitor.Type {
	case "http":
		ok, latencyMs, probeErr = p.probeHTTPContext(probeCtx, monitor)
	case "tcp":
		ok, latencyMs, probeErr = p.probeTCPContext(probeCtx, monitor)
	default:
		cancel()
		return fmt.Errorf("unsupported monitor type %q", monitor.Type)
	}
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if probeErr != nil {
		logger.Module("uptime").Debug().Uint("monitor_id", monitor.ID).Err(probeErr).Msg("service monitor probe failed")
	}

	now := p.nowTime()
	uptimePct, err := p.persistResult(ctx, monitor, now, ok)
	if err != nil {
		return err
	}
	if ctx.Err() != nil || p.monitorRevision(monitor.ID) != revision {
		return errMonitorStale
	}
	var current model.ServiceMonitor
	if err := p.db.WithContext(ctx).First(&current, monitor.ID).Error; err != nil {
		return errMonitorStale
	}
	if !current.Enabled || !sameMonitorConfig(current, monitor) {
		return errMonitorStale
	}
	current.LastStatus = statusForProbe(ok)
	current.LastCheckedAt = &now
	current.UptimePct = uptimePct
	// Never hand decrypted header values to alert callbacks.
	current.HTTPHeaders = ""
	log := logger.Module("uptime").With().Uint("monitor_id", monitor.ID).Str("name", monitor.Name).Logger()
	if ok {
		log.Debug().Int64("latency_ms", latencyMs).Float64("uptime_pct", uptimePct).Msg("探测成功")
	} else {
		log.Warn().Float64("uptime_pct", uptimePct).Msg("探测失败")
	}
	if monitor.LastStatus != current.LastStatus && p.alertFn != nil && p.monitorRevision(monitor.ID) == revision {
		p.alertFn(current, monitor.LastStatus, current.LastStatus)
	}
	return nil
}

func statusForProbe(ok bool) string {
	if ok {
		return "up"
	}
	return "down"
}

func (p *Prober) persistResult(ctx context.Context, monitor model.ServiceMonitor, now time.Time, ok bool) (float64, error) {
	var uptimePct float64
	err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.ServiceMonitor
		// Serialize configuration mutations with this authoritative comparison
		// and result commit; timestamp text equality is not portable to SQLite.
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, monitor.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errMonitorStale
			}
			return err
		}
		if !current.Enabled || !sameMonitorConfig(current, monitor) {
			return errMonitorStale
		}
		if err := p.upsertSampleTx(ctx, tx, monitor.ID, now, ok); err != nil {
			return err
		}
		var err error
		uptimePct, err = p.calcUptimePctTx(ctx, tx, monitor.ID, now)
		if err != nil {
			return err
		}
		result := tx.WithContext(ctx).Table("service_monitors").
			Where("id = ? AND enabled = ?", monitor.ID, true).
			Updates(map[string]interface{}{
				"last_status":     statusForProbe(ok),
				"last_checked_at": now,
				"uptime_pct":      uptimePct,
				"updated_at":      now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errMonitorStale
		}
		return nil
	})
	return uptimePct, err
}

func sameMonitorConfig(a, b model.ServiceMonitor) bool {
	aHeaders := decryptMonitorHeaders(a.HTTPHeaders)
	bHeaders := decryptMonitorHeaders(b.HTTPHeaders)
	aInterval, bInterval := normalizedInterval(a.IntervalSeconds), normalizedInterval(b.IntervalSeconds)
	aTimeout, bTimeout := normalizedTimeout(a.TimeoutSeconds), normalizedTimeout(b.TimeoutSeconds)
	aMethod, bMethod := strings.ToUpper(strings.TrimSpace(a.HTTPMethod)), strings.ToUpper(strings.TrimSpace(b.HTTPMethod))
	if aMethod == "" {
		aMethod = "GET"
	}
	if bMethod == "" {
		bMethod = "GET"
	}
	aExpected, bExpected := a.HTTPExpectedStatus, b.HTTPExpectedStatus
	if aExpected <= 0 {
		aExpected = 200
	}
	if bExpected <= 0 {
		bExpected = 200
	}
	return a.ID == b.ID && a.Name == b.Name && a.Description == b.Description &&
		a.Type == b.Type && a.Target == b.Target && aInterval == bInterval &&
		aTimeout == bTimeout && aMethod == bMethod && aExpected == bExpected &&
		aHeaders == bHeaders
}

func decryptMonitorHeaders(raw string) string {
	decrypted, err := secure.DecryptIfNeeded(raw)
	if err != nil {
		return raw
	}
	return decrypted
}

func normalizedInterval(value int) int {
	if value < 5 || value > 3600 {
		return 60
	}
	return value
}

func normalizedTimeout(value int) int {
	if value < 1 || value > 300 {
		return 10
	}
	return value
}

func (p *Prober) probeHTTP(monitor model.ServiceMonitor) (bool, int64, error) {
	timeout := time.Duration(normalizedTimeout(monitor.TimeoutSeconds)) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return p.probeHTTPContext(ctx, monitor)
}

func (p *Prober) probeHTTPContext(ctx context.Context, monitor model.ServiceMonitor) (bool, int64, error) {
	method := strings.ToUpper(strings.TrimSpace(monitor.HTTPMethod))
	if method == "" {
		method = "GET"
	}
	req, err := http.NewRequestWithContext(ctx, method, monitor.Target, nil)
	if err != nil {
		return false, 0, err
	}
	if headers := strings.TrimSpace(monitor.HTTPHeaders); headers != "" && headers != "{}" {
		if err := parseHeaders(headers, req); err != nil {
			return false, 0, err
		}
	}
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	start := time.Now()
	resp, doErr := client.Do(req)
	latencyMs := time.Since(start).Milliseconds()
	if doErr != nil {
		return false, latencyMs, doErr
	}
	defer resp.Body.Close() //nolint:errcheck
	expectedStatus := monitor.HTTPExpectedStatus
	if expectedStatus <= 0 {
		expectedStatus = 200
	}
	if resp.StatusCode == expectedStatus {
		return true, latencyMs, nil
	}
	return false, latencyMs, fmt.Errorf("HTTP status %d (expected %d)", resp.StatusCode, expectedStatus)
}

func (p *Prober) probeTCP(monitor model.ServiceMonitor) (bool, int64, error) {
	timeout := time.Duration(normalizedTimeout(monitor.TimeoutSeconds)) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return p.probeTCPContext(ctx, monitor)

}
func (p *Prober) probeTCPContext(ctx context.Context, monitor model.ServiceMonitor) (bool, int64, error) {
	start := time.Now()
	dialer := &net.Dialer{}
	conn, dialErr := dialer.DialContext(ctx, "tcp", monitor.Target)
	latencyMs := time.Since(start).Milliseconds()
	if dialErr != nil {
		return false, latencyMs, dialErr
	}
	_ = conn.Close()
	return true, latencyMs, nil
}

// A process-wide semaphore keeps hourly upserts serialized even when multiple
// Prober instances share a database in tests or during a controlled reload.
var upsertMu = make(chan struct{}, 1)

func (p *Prober) upsertSample(monitorID uint, now time.Time, ok bool) error {
	return p.upsertSampleTx(context.Background(), p.db, monitorID, now, ok)
}

func (p *Prober) upsertSampleTx(ctx context.Context, db *gorm.DB, monitorID uint, now time.Time, ok bool) error {
	select {
	case upsertMu <- struct{}{}:
		defer func() { <-upsertMu }()
	case <-ctx.Done():
		return ctx.Err()
	}
	hour := now.Truncate(time.Hour)
	var sample model.ServiceUptimeSample
	err := db.WithContext(ctx).Where("monitor_id = ? AND hour = ?", monitorID, hour).First(&sample).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		probeOK := 0
		if ok {
			probeOK = 1
		}
		return db.WithContext(ctx).Create(&model.ServiceUptimeSample{
			MonitorID: monitorID, Hour: hour, ProbeCount: 1, ProbeOK: probeOK,
		}).Error
	}
	if err != nil {
		return err
	}
	updates := map[string]interface{}{"probe_count": sample.ProbeCount + 1}
	if ok {
		updates["probe_ok"] = sample.ProbeOK + 1
	}
	return db.WithContext(ctx).Model(&sample).Updates(updates).Error
}

func (p *Prober) calcUptimePct(monitorID uint) float64 {
	pct, err := p.calcUptimePctTx(context.Background(), p.db, monitorID, p.nowTime())
	if err != nil {
		return 0
	}
	return pct
}

func (p *Prober) calcUptimePctTx(ctx context.Context, db *gorm.DB, monitorID uint, now time.Time) (float64, error) {
	cutoff := now.Add(-24 * time.Hour)
	var result struct {
		Total int
		OK    int
	}
	if err := db.WithContext(ctx).Model(&model.ServiceUptimeSample{}).
		Select("COALESCE(SUM(probe_count), 0) as total, COALESCE(SUM(probe_ok), 0) as ok").
		Where("monitor_id = ? AND hour >= ?", monitorID, cutoff).
		Scan(&result).Error; err != nil {
		return 0, err
	}
	if result.Total == 0 {
		return 0, nil
	}
	return float64(result.OK) / float64(result.Total) * 100, nil
}

func (p *Prober) nowTime() time.Time {
	if p.now == nil {
		return time.Now()
	}
	return p.now()
}

func (p *Prober) monitorRevision(id uint) uint64 {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	return p.revisions[id]
}

func monitorInterval(monitor model.ServiceMonitor) time.Duration {
	seconds := monitor.IntervalSeconds
	if seconds < 5 || seconds > 3600 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

func targetKey(monitor model.ServiceMonitor) string {
	return strings.TrimSpace(monitor.Target)
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// parseHeaders parses a JSON object like {"X-Custom":"value"} into request headers.
func parseHeaders(raw string, req *http.Request) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(raw), &headers); err != nil || headers == nil {
		return fmt.Errorf("invalid JSON object")
	}
	for key, value := range headers {
		if strings.TrimSpace(key) != "" {
			req.Header.Set(key, value)
		}
	}
	return nil
}

// splitJSONPairs is retained for compatibility with package-level callers from
// older tests. Header parsing now uses encoding/json for correct escaping.
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
			switch ch {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			case ',':
				if depth == 0 {
					result = append(result, strings.TrimSpace(s[start:i]))
					start = i + 1
				}
			}
		}
	}
	if start < len(s) {
		result = append(result, strings.TrimSpace(s[start:]))
	}
	return result
}
