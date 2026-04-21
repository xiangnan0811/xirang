package escalation

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func TestEngine_ConcurrentTicks_OnlyOneFires(t *testing.T) {
	db := openEngineDB(t)
	svc := NewService(db)
	policy := seedPolicy(t, svc, "p", []model.EscalationLevel{
		{DelaySeconds: 0, IntegrationIDs: []uint{1}},
	}, "warning")
	triggered := time.Now()
	a := seedAlertOnNodeWithPolicy(t, db, 10, policy.ID, triggered, "warning")

	var sendCount int64
	sendFn := func(_ model.Alert, _ []uint) { atomic.AddInt64(&sendCount, 1) }

	e1 := NewEngine(db, svc, nil, sendFn)
	e2 := NewEngine(db, svc, nil, sendFn)
	e1.SetNowFn(func() time.Time { return triggered })
	e2.SetNowFn(func() time.Time { return triggered })

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); e1.Tick(context.Background()) }()
	go func() { defer wg.Done(); e2.Tick(context.Background()) }()
	wg.Wait()

	if got := atomic.LoadInt64(&sendCount); got != 1 {
		t.Fatalf("sendCount=%d want 1 (optimistic lock + UNIQUE should serialize)", got)
	}
	// Exactly one event row
	var n int64
	db.Model(&model.AlertEscalationEvent{}).Where("alert_id = ?", a.ID).Count(&n)
	if n != 1 {
		t.Fatalf("event rows=%d want 1", n)
	}
}

func TestEngine_MultipleOpenAlerts_Independent(t *testing.T) {
	db := openEngineDB(t)
	svc := NewService(db)
	policy := seedPolicy(t, svc, "p", []model.EscalationLevel{
		{DelaySeconds: 0, IntegrationIDs: []uint{1}},
	}, "warning")
	triggered := time.Now()
	seedAlertOnNodeWithPolicy(t, db, 10, policy.ID, triggered, "warning")
	// second alert on a different node with same policy
	db.Create(&model.Node{ID: 20, Name: "n2", Host: "h2", Username: "u", BackupDir: "/b2", EscalationPolicyID: &policy.ID})
	db.Create(&model.Alert{
		NodeID: 20, NodeName: "n2", Severity: "warning", Status: "open",
		ErrorCode: "XR-NODE-2", Message: "m", TriggeredAt: triggered, Tags: "[]", LastLevelFired: -1,
	})

	var rec []senderRecord
	e := NewEngine(db, svc, nil, func(a model.Alert, ids []uint) {
		rec = append(rec, senderRecord{a.ID, ids})
	})
	e.SetNowFn(func() time.Time { return triggered })
	e.Tick(context.Background())

	if len(rec) != 2 {
		t.Fatalf("expected 2 independent fires, got %d", len(rec))
	}
}
