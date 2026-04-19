package alerting

import (
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func tptr(t time.Time) *time.Time { return &t }

func TestMatchSilence_NoActiveSilences(t *testing.T) {
	alert := model.Alert{NodeID: 1, ErrorCode: "probe_down"}
	node := model.Node{ID: 1, Tags: "prod,web"}
	silences := []model.Silence{}
	if got := MatchSilence(alert, node, silences, time.Now()); got != nil {
		t.Fatalf("expected no match, got %+v", got)
	}
}

func TestMatchSilence_NodeOnly(t *testing.T) {
	now := time.Now()
	s := model.Silence{ID: 7, MatchNodeID: uptr(1), StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)}
	alert := model.Alert{NodeID: 1, ErrorCode: "probe_down"}
	node := model.Node{ID: 1}
	got := MatchSilence(alert, node, []model.Silence{s}, now)
	if got == nil || got.ID != 7 {
		t.Fatalf("expected silence 7, got %+v", got)
	}
}

func TestMatchSilence_CategoryOnly(t *testing.T) {
	now := time.Now()
	s := model.Silence{ID: 8, MatchCategory: "backup_failed", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)}
	alert := model.Alert{NodeID: 5, ErrorCode: "backup_failed"}
	node := model.Node{ID: 5}
	if got := MatchSilence(alert, node, []model.Silence{s}, now); got == nil {
		t.Fatal("expected category match")
	}
}

func TestMatchSilence_CategoryMismatch(t *testing.T) {
	now := time.Now()
	s := model.Silence{ID: 8, MatchCategory: "backup_failed", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)}
	alert := model.Alert{NodeID: 5, ErrorCode: "probe_down"}
	node := model.Node{ID: 5}
	if got := MatchSilence(alert, node, []model.Silence{s}, now); got != nil {
		t.Fatalf("expected no match, got %+v", got)
	}
}

func TestMatchSilence_TagsAnyOf(t *testing.T) {
	now := time.Now()
	s := model.Silence{
		ID:        9,
		MatchTags: `["prod","staging"]`,
		StartsAt:  now.Add(-time.Hour),
		EndsAt:    now.Add(time.Hour),
	}
	alert := model.Alert{NodeID: 3}
	node := model.Node{ID: 3, Tags: "web,prod"}
	if got := MatchSilence(alert, node, []model.Silence{s}, now); got == nil {
		t.Fatal("expected tag any-of match (prod ∈ [prod,staging])")
	}
}

func TestMatchSilence_TagsDisjoint(t *testing.T) {
	now := time.Now()
	s := model.Silence{ID: 9, MatchTags: `["prod"]`, StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)}
	alert := model.Alert{NodeID: 3}
	node := model.Node{ID: 3, Tags: "staging,web"}
	if got := MatchSilence(alert, node, []model.Silence{s}, now); got != nil {
		t.Fatalf("expected no match, got %+v", got)
	}
}

func TestMatchSilence_OutsideWindow(t *testing.T) {
	now := time.Now()
	future := model.Silence{ID: 10, MatchNodeID: uptr(1), StartsAt: now.Add(time.Hour), EndsAt: now.Add(2 * time.Hour)}
	past := model.Silence{ID: 11, MatchNodeID: uptr(1), StartsAt: now.Add(-2 * time.Hour), EndsAt: now.Add(-time.Hour)}
	alert := model.Alert{NodeID: 1}
	node := model.Node{ID: 1}
	if got := MatchSilence(alert, node, []model.Silence{future, past}, now); got != nil {
		t.Fatalf("expected no match, got %+v", got)
	}
}

func TestMatchSilence_CombinedAllFields(t *testing.T) {
	now := time.Now()
	s := model.Silence{
		ID:            12,
		MatchNodeID:   uptr(1),
		MatchCategory: "probe_down",
		MatchTags:     `["prod"]`,
		StartsAt:      now.Add(-time.Hour),
		EndsAt:        now.Add(time.Hour),
	}
	alert := model.Alert{NodeID: 1, ErrorCode: "probe_down"}
	node := model.Node{ID: 1, Tags: "prod,web"}
	if got := MatchSilence(alert, node, []model.Silence{s}, now); got == nil {
		t.Fatal("expected combined match")
	}
	alertBad := alert
	alertBad.NodeID = 2
	if got := MatchSilence(alertBad, node, []model.Silence{s}, now); got != nil {
		t.Fatal("expected no match when node_id differs")
	}
}

func uptr(u uint) *uint { return &u }
