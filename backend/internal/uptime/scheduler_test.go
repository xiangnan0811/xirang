package uptime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func seedScheduledMonitor(t *testing.T, db *gorm.DB, id uint, name, target string, interval int, enabled bool, lastChecked *time.Time) {
	t.Helper()
	monitor := model.ServiceMonitor{
		ID: id, Name: name, Type: "http", Target: target,
		IntervalSeconds: interval, TimeoutSeconds: 1, HTTPMethod: "GET",
		HTTPExpectedStatus: http.StatusOK, Enabled: enabled, LastStatus: "unknown",
		LastCheckedAt: lastChecked,
	}
	if err := db.Create(&monitor).Error; err != nil {
		t.Fatalf("seed monitor %d: %v", id, err)
	}
	if err := db.Model(&model.ServiceMonitor{}).Where("id = ?", id).Update("enabled", enabled).Error; err != nil {
		t.Fatalf("set monitor %d enabled=%t: %v", id, enabled, err)
	}
}

func TestScanDueUsesPerMonitorIntervalsAndSkipsBacklogStorm(t *testing.T) {
	db := openTestDB(t)
	clock := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	p := NewProber(db, time.Minute)
	p.SetNowForTesting(func() time.Time { return clock })
	old := clock.Add(-24 * time.Hour)
	seedScheduledMonitor(t, db, 1, "FAKE_5S_MONITOR_FOR_TEST_ONLY", "https://one.invalid", 5, true, &old)
	seedScheduledMonitor(t, db, 2, "FAKE_60S_MONITOR_FOR_TEST_ONLY", "https://two.invalid", 60, true, &old)
	seedScheduledMonitor(t, db, 3, "FAKE_3600S_MONITOR_FOR_TEST_ONLY", "https://three.invalid", 3600, true, &old)

	p.scanDue(context.Background())
	if got := len(p.queue); got != 3 {
		t.Fatalf("first due scan queued=%d, want 3", got)
	}
	if got := len(p.queue); got != 3 {
		t.Fatalf("backlog storm queued=%d, want one job per monitor", got)
	}
	p.scanDue(context.Background())
	if got := len(p.queue); got != 3 {
		t.Fatalf("repeated scan duplicated queued jobs=%d", got)
	}
}

func TestScanDueReenableAndIntervalEditRecomputeImmediately(t *testing.T) {
	db := openTestDB(t)
	clock := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	p := NewProber(db, time.Minute)
	p.SetNowForTesting(func() time.Time { return clock })
	last := clock.Add(-30 * time.Second)
	seedScheduledMonitor(t, db, 1, "FAKE_INTERVAL_EDIT_MONITOR_FOR_TEST_ONLY", "https://edit.invalid", 60, true, &last)
	seedScheduledMonitor(t, db, 2, "FAKE_REENABLE_MONITOR_FOR_TEST_ONLY", "https://reenable.invalid", 60, false, &last)

	p.scanDue(context.Background())
	if got := len(p.queue); got != 0 {
		t.Fatalf("initial scan unexpectedly queued=%d", got)
	}
	if err := db.Model(&model.ServiceMonitor{}).Where("id = 1").Update("interval_seconds", 5).Error; err != nil {
		t.Fatalf("edit interval: %v", err)
	}
	if err := db.Model(&model.ServiceMonitor{}).Where("id = 2").Update("enabled", true).Error; err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	p.scanDue(context.Background())
	if got := len(p.queue); got != 2 {
		t.Fatalf("edited/re-enabled monitors queued=%d, want 2", got)
	}
	ids := map[uint]bool{}
	for len(p.queue) > 0 {
		job := <-p.queue
		ids[job.monitor.ID] = true
	}
	if !ids[1] || !ids[2] {
		t.Fatalf("expected interval edit and re-enable jobs, got ids=%v", ids)
	}
}

func TestProberCancellationDoesNotStartQueuedTargets(t *testing.T) {
	db := openTestDB(t)
	entered := make(chan struct{})
	var secondCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-r.Context().Done()
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer second.Close()

	seedScheduledMonitor(t, db, 1, "FAKE_CANCEL_FIRST_MONITOR_FOR_TEST_ONLY", first.URL, 5, true, nil)
	seedScheduledMonitor(t, db, 2, "FAKE_CANCEL_SECOND_MONITOR_FOR_TEST_ONLY", second.URL, 5, true, nil)
	p := NewProber(db, time.Second)
	p.workers = 1
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("first target was not started")
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := p.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown after cancellation: %v", err)
	}
	if got := secondCalls.Load(); got != 0 {
		t.Fatalf("queued target started after parent cancellation: calls=%d", got)
	}
}

func TestProberShutdownUsesBoundedContext(t *testing.T) {
	db := openTestDB(t)
	p := NewProber(db, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := p.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown should drain promptly: %v", err)
	}

}
