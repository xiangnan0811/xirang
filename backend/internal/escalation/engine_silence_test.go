package escalation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func TestEngine_SilencedLevel_SkipsSenderButAdvancesLevel(t *testing.T) {
	db := openEngineDB(t)
	svc := NewService(db)
	policy := seedPolicy(t, svc, "p", []model.EscalationLevel{
		{DelaySeconds: 0, IntegrationIDs: []uint{1}},
		{DelaySeconds: 60, IntegrationIDs: []uint{2}},
	}, "warning")
	triggered := time.Now().Add(-2 * time.Minute)
	a := seedAlertOnNodeWithPolicy(t, db, 10, policy.ID, triggered, "warning")

	disp := &recordingDispatcher{}
	silenceFn := func(alert model.Alert) *model.Silence {
		return &model.Silence{ID: 1, Note: "maint"} // always silenced
	}
	e := NewEngine(db, svc, silenceFn, disp)

	e.SetNowFn(func() time.Time { return triggered })
	e.Tick(context.Background())

	if len(disp.calls) != 0 {
		t.Fatalf("silenced should skip sender, got %+v", disp.calls)
	}
	// But the level must advance
	var got model.Alert
	db.First(&got, a.ID)
	if got.LastLevelFired != 0 {
		t.Fatalf("level should advance to 0, got %d", got.LastLevelFired)
	}
	// And event row must reflect silenced-skip (integration_ids = [])
	var evt model.AlertEscalationEvent
	db.First(&evt, "alert_id = ? AND level_index = 0", a.ID)
	var ids []uint
	_ = json.Unmarshal([]byte(evt.IntegrationIDs), &ids)
	if len(ids) != 0 {
		t.Fatalf("silenced event integration_ids must be [], got %v", ids)
	}
}

func TestEngine_SilenceMatchesProjectedState(t *testing.T) {
	db := openEngineDB(t)
	svc := NewService(db)
	policy := seedPolicy(t, svc, "p", []model.EscalationLevel{
		{DelaySeconds: 0, IntegrationIDs: []uint{1}, SeverityOverride: "critical", Tags: []string{"escalated"}},
	}, "warning")
	triggered := time.Now()
	seedAlertOnNodeWithPolicy(t, db, 10, policy.ID, triggered, "warning")

	var projectedSeen model.Alert
	silenceFn := func(alert model.Alert) *model.Silence {
		projectedSeen = alert
		return nil
	}
	e := NewEngine(db, svc, silenceFn, &recordingDispatcher{})
	e.SetNowFn(func() time.Time { return triggered })
	e.Tick(context.Background())

	if projectedSeen.Severity != "critical" {
		t.Fatalf("silence should see projected severity=critical, got %s", projectedSeen.Severity)
	}
	tags := projectedSeen.DecodedTags()
	if len(tags) != 1 || tags[0] != "escalated" {
		t.Fatalf("silence should see projected tags=[escalated], got %v", tags)
	}
}
