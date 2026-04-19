package alerting

import (
	"errors"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func TestBackoffSchedule(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 30 * time.Second},
		{1, 2 * time.Minute},
		{2, 8 * time.Minute},
		{3, 30 * time.Minute},
	}
	for _, tc := range cases {
		got := backoffDuration(tc.attempt)
		if got != tc.want {
			t.Fatalf("attempt=%d: got %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestBackoffAboveMax(t *testing.T) {
	if got := backoffDuration(4); got != 30*time.Minute {
		t.Fatalf("attempt=4: got %v, want 30m (ceiling)", got)
	}
}

func TestRetryWorker_MarksFailedAfterMaxAttempts(t *testing.T) {
	db := setupTestDB(t)
	db.Create(&model.Integration{Name: "wh", Type: "webhook", Enabled: true, Endpoint: "http://example.invalid"})
	db.Create(&model.Alert{NodeID: 1, ErrorCode: "probe_down"})
	past := time.Now().Add(-time.Second)
	d := model.AlertDelivery{
		AlertID:       1,
		IntegrationID: 1,
		Status:        "retrying",
		AttemptCount:  3,
		NextRetryAt:   &past,
	}
	db.Create(&d)

	w := NewRetryWorker(db)
	w.sendFn = func(integ model.Integration, alert model.Alert) error { return errors.New("boom") }
	w.tick(time.Now())

	var got model.AlertDelivery
	db.First(&got, d.ID)
	if got.Status != "failed" {
		t.Fatalf("expected failed, got %q", got.Status)
	}
	if got.AttemptCount != 4 {
		t.Fatalf("expected attempts=4, got %d", got.AttemptCount)
	}
}

func TestRetryWorker_ReenqueuesOnFailure(t *testing.T) {
	db := setupTestDB(t)
	db.Create(&model.Integration{Name: "wh", Type: "webhook", Enabled: true, Endpoint: "http://example.invalid"})
	db.Create(&model.Alert{NodeID: 1, ErrorCode: "probe_down"})
	past := time.Now().Add(-time.Second)
	d := model.AlertDelivery{Status: "retrying", AttemptCount: 0, NextRetryAt: &past, IntegrationID: 1, AlertID: 1}
	db.Create(&d)

	w := NewRetryWorker(db)
	w.sendFn = func(integ model.Integration, alert model.Alert) error { return errors.New("boom") }
	w.tick(time.Now())

	var got model.AlertDelivery
	db.First(&got, d.ID)
	if got.Status != "retrying" {
		t.Fatalf("expected retrying, got %q", got.Status)
	}
	if got.AttemptCount != 1 {
		t.Fatalf("expected attempts=1, got %d", got.AttemptCount)
	}
	if got.NextRetryAt == nil || !got.NextRetryAt.After(time.Now()) {
		t.Fatalf("expected NextRetryAt in the future, got %v", got.NextRetryAt)
	}
}

func TestRetryWorker_ManualRetryBypassesSchedule(t *testing.T) {
	db := setupTestDB(t)
	db.Create(&model.Integration{Name: "wh", Type: "webhook", Enabled: true, Endpoint: "http://example.invalid"})
	db.Create(&model.Alert{NodeID: 1, ErrorCode: "probe_down"})
	future := time.Now().Add(20 * time.Minute)
	d := model.AlertDelivery{Status: "retrying", AttemptCount: 1, NextRetryAt: &future, IntegrationID: 1, AlertID: 1}
	db.Create(&d)

	w := NewRetryWorker(db)
	w.sendFn = func(integ model.Integration, alert model.Alert) error { return nil }
	if err := w.ManualRetry(d.ID); err != nil {
		t.Fatal(err)
	}

	var got model.AlertDelivery
	db.First(&got, d.ID)
	if got.Status != "sent" {
		t.Fatalf("expected sent, got %q", got.Status)
	}
}
