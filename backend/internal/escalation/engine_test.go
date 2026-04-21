package escalation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openEngineDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(
		&model.EscalationPolicy{}, &model.Alert{}, &model.Task{}, &model.Policy{},
		&model.SLODefinition{}, &model.Node{}, &model.AlertEscalationEvent{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

type senderRecord struct {
	alertID uint
	ids     []uint
}

func seedPolicy(t *testing.T, s *Service, name string, levels []model.EscalationLevel, minSev string) *model.EscalationPolicy {
	t.Helper()
	in := PolicyInput{Name: name, MinSeverity: minSev, Enabled: true, Levels: levels}
	p, err := s.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	return p
}

func seedAlertOnNodeWithPolicy(t *testing.T, db *gorm.DB, nodeID, policyID uint, triggered time.Time, severity string) *model.Alert {
	t.Helper()
	db.Create(&model.Node{ID: nodeID, Name: "n", Host: "h", Username: "u", BackupDir: "/b", EscalationPolicyID: &policyID})
	a := model.Alert{
		NodeID: nodeID, NodeName: "n", Severity: severity, Status: "open",
		ErrorCode: "XR-NODE-1", Message: "m", TriggeredAt: triggered, Tags: "[]",
		LastLevelFired: -1,
	}
	db.Create(&a)
	return &a
}

func TestEngine_Level0_FiresImmediately(t *testing.T) {
	db := openEngineDB(t)
	svc := NewService(db)
	policy := seedPolicy(t, svc, "p", []model.EscalationLevel{
		{DelaySeconds: 0, IntegrationIDs: []uint{1}},
		{DelaySeconds: 300, IntegrationIDs: []uint{2}},
	}, "warning")
	triggered := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	seedAlertOnNodeWithPolicy(t, db, 10, policy.ID, triggered, "warning")

	var rec []senderRecord
	e := NewEngine(db, svc, nil, func(a model.Alert, ids []uint) {
		rec = append(rec, senderRecord{a.ID, ids})
	})
	e.SetNowFn(func() time.Time { return triggered })
	e.Tick(context.Background())

	if len(rec) != 1 || len(rec[0].ids) != 1 || rec[0].ids[0] != 1 {
		t.Fatalf("expected 1 send to [1], got %+v", rec)
	}
	var got model.Alert
	db.First(&got, rec[0].alertID)
	if got.LastLevelFired != 0 {
		t.Fatalf("last_level_fired=%d want 0", got.LastLevelFired)
	}
	var evt model.AlertEscalationEvent
	db.First(&evt, "alert_id = ?", got.ID)
	if evt.LevelIndex != 0 {
		t.Fatalf("event level=%d want 0", evt.LevelIndex)
	}
}

func TestEngine_Level1_WaitsForDelay(t *testing.T) {
	db := openEngineDB(t)
	svc := NewService(db)
	policy := seedPolicy(t, svc, "p", []model.EscalationLevel{
		{DelaySeconds: 0, IntegrationIDs: []uint{1}},
		{DelaySeconds: 300, IntegrationIDs: []uint{2}},
	}, "warning")
	triggered := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	a := seedAlertOnNodeWithPolicy(t, db, 10, policy.ID, triggered, "warning")

	var rec []senderRecord
	e := NewEngine(db, svc, nil, func(a model.Alert, ids []uint) {
		rec = append(rec, senderRecord{a.ID, ids})
	})

	// tick at T+0 → level 0 fires
	e.SetNowFn(func() time.Time { return triggered })
	e.Tick(context.Background())

	// tick at T+4min → still level 0, no level 1 yet
	e.SetNowFn(func() time.Time { return triggered.Add(4 * time.Minute) })
	e.Tick(context.Background())
	if len(rec) != 1 {
		t.Fatalf("early: expected 1, got %d", len(rec))
	}

	// tick at T+6min → level 1 fires
	e.SetNowFn(func() time.Time { return triggered.Add(6 * time.Minute) })
	e.Tick(context.Background())
	if len(rec) != 2 || rec[1].ids[0] != 2 {
		t.Fatalf("late: expected 2 sends with ids[0]=2, got %+v", rec)
	}

	var got model.Alert
	db.First(&got, a.ID)
	if got.LastLevelFired != 1 {
		t.Fatalf("last_level_fired=%d want 1", got.LastLevelFired)
	}
}

func TestEngine_AckedAlert_NotEvaluated(t *testing.T) {
	db := openEngineDB(t)
	svc := NewService(db)
	policy := seedPolicy(t, svc, "p", []model.EscalationLevel{
		{DelaySeconds: 0, IntegrationIDs: []uint{1}},
		{DelaySeconds: 300, IntegrationIDs: []uint{2}},
	}, "warning")
	triggered := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	a := seedAlertOnNodeWithPolicy(t, db, 10, policy.ID, triggered, "warning")

	// fire level 0, then ack
	e := NewEngine(db, svc, nil, func(_ model.Alert, _ []uint) {})
	e.SetNowFn(func() time.Time { return triggered })
	e.Tick(context.Background())
	db.Model(&model.Alert{}).Where("id = ?", a.ID).Update("status", "acked")

	var rec []senderRecord
	e = NewEngine(db, svc, nil, func(a model.Alert, ids []uint) {
		rec = append(rec, senderRecord{a.ID, ids})
	})
	e.SetNowFn(func() time.Time { return triggered.Add(10 * time.Minute) })
	e.Tick(context.Background())

	if len(rec) != 0 {
		t.Fatalf("acked alert should not re-fire, got %+v", rec)
	}
}

func TestEngine_BelowMinSeverity_Skipped(t *testing.T) {
	db := openEngineDB(t)
	svc := NewService(db)
	policy := seedPolicy(t, svc, "p", []model.EscalationLevel{
		{DelaySeconds: 0, IntegrationIDs: []uint{1}},
	}, "critical")
	triggered := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	seedAlertOnNodeWithPolicy(t, db, 10, policy.ID, triggered, "warning")

	var rec []senderRecord
	e := NewEngine(db, svc, nil, func(a model.Alert, ids []uint) {
		rec = append(rec, senderRecord{a.ID, ids})
	})
	e.SetNowFn(func() time.Time { return triggered })
	e.Tick(context.Background())

	if len(rec) != 0 {
		t.Fatalf("expected skip, got %+v", rec)
	}
}

func TestEngine_SeverityOverride_Applied(t *testing.T) {
	db := openEngineDB(t)
	svc := NewService(db)
	policy := seedPolicy(t, svc, "p", []model.EscalationLevel{
		{DelaySeconds: 0, IntegrationIDs: []uint{1}},
		{DelaySeconds: 60, IntegrationIDs: []uint{2}, SeverityOverride: "critical", Tags: []string{"esc"}},
	}, "warning")
	triggered := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	a := seedAlertOnNodeWithPolicy(t, db, 10, policy.ID, triggered, "warning")

	e := NewEngine(db, svc, nil, func(_ model.Alert, _ []uint) {})
	e.SetNowFn(func() time.Time { return triggered })
	e.Tick(context.Background())
	e.SetNowFn(func() time.Time { return triggered.Add(2 * time.Minute) })
	e.Tick(context.Background())

	var got model.Alert
	db.First(&got, a.ID)
	if got.Severity != "critical" {
		t.Fatalf("severity=%s want critical", got.Severity)
	}
	tags := got.DecodedTags()
	if len(tags) != 1 || tags[0] != "esc" {
		t.Fatalf("tags=%v want [esc]", tags)
	}
	var evt model.AlertEscalationEvent
	db.Order("level_index desc").First(&evt, "alert_id = ?", a.ID)
	if evt.SeverityBefore != "warning" || evt.SeverityAfter != "critical" {
		t.Fatalf("event severity: %s→%s", evt.SeverityBefore, evt.SeverityAfter)
	}
	var added []string
	_ = json.Unmarshal([]byte(evt.TagsAdded), &added)
	if len(added) != 1 || added[0] != "esc" {
		t.Fatalf("tags_added=%v", added)
	}
}

func TestEngine_PolicyDisabled_Skipped(t *testing.T) {
	db := openEngineDB(t)
	svc := NewService(db)
	policy := seedPolicy(t, svc, "p", []model.EscalationLevel{
		{DelaySeconds: 0, IntegrationIDs: []uint{1}},
	}, "warning")
	db.Model(&model.EscalationPolicy{}).Where("id = ?", policy.ID).Update("enabled", false)
	triggered := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	seedAlertOnNodeWithPolicy(t, db, 10, policy.ID, triggered, "warning")

	var rec []senderRecord
	e := NewEngine(db, svc, nil, func(a model.Alert, ids []uint) {
		rec = append(rec, senderRecord{a.ID, ids})
	})
	e.SetNowFn(func() time.Time { return triggered })
	e.Tick(context.Background())

	if len(rec) != 0 {
		t.Fatalf("disabled policy should not fire, got %+v", rec)
	}
}

func TestEngine_NoPolicyLinked_Skipped(t *testing.T) {
	db := openEngineDB(t)
	svc := NewService(db)
	db.Create(&model.Node{ID: 10, Name: "n", Host: "h", Username: "u", BackupDir: "/b"})
	db.Create(&model.Alert{
		NodeID: 10, Severity: "warning", Status: "open", ErrorCode: "x", Message: "m",
		TriggeredAt: time.Now(), Tags: "[]", LastLevelFired: -1,
	})

	var rec []senderRecord
	e := NewEngine(db, svc, nil, func(a model.Alert, ids []uint) {
		rec = append(rec, senderRecord{a.ID, ids})
	})
	e.Tick(context.Background())

	if len(rec) != 0 {
		t.Fatalf("expected no fire, got %+v", rec)
	}
}

func TestEngine_Idempotency_UniqueConstraint(t *testing.T) {
	db := openEngineDB(t)
	svc := NewService(db)
	policy := seedPolicy(t, svc, "p", []model.EscalationLevel{
		{DelaySeconds: 0, IntegrationIDs: []uint{1}},
	}, "warning")
	triggered := time.Now()
	a := seedAlertOnNodeWithPolicy(t, db, 10, policy.ID, triggered, "warning")

	// Manually insert event at level 0 to simulate prior fire with concurrent rewrite race
	db.Create(&model.AlertEscalationEvent{
		AlertID: a.ID, EscalationPolicyID: &policy.ID, LevelIndex: 0,
		IntegrationIDs: "[1]", SeverityBefore: "warning", SeverityAfter: "warning",
		TagsAdded: "[]", FiredAt: triggered,
	})
	// But leave alert.last_level_fired = -1 (simulating mid-race)
	// Engine tick should fail UNIQUE on insert and roll back without advancing alert
	var rec []senderRecord
	e := NewEngine(db, svc, nil, func(a model.Alert, ids []uint) {
		rec = append(rec, senderRecord{a.ID, ids})
	})
	e.SetNowFn(func() time.Time { return triggered })
	e.Tick(context.Background())

	// sender must NOT have been called since tx rolled back before calling sender
	if len(rec) != 0 {
		t.Fatalf("unique-conflict tick must not call sender, got %+v", rec)
	}
	var got model.Alert
	db.First(&got, a.ID)
	if got.LastLevelFired != -1 {
		t.Fatalf("last_level_fired must stay -1 on rollback, got %d", got.LastLevelFired)
	}
}
