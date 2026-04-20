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
	// prefix "XR-NODE" matches instance code "XR-NODE-5"
	s := model.Silence{ID: 8, MatchCategory: "XR-NODE", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)}
	alert := model.Alert{NodeID: 5, ErrorCode: "XR-NODE-5"}
	node := model.Node{ID: 5}
	if got := MatchSilence(alert, node, []model.Silence{s}, now); got == nil {
		t.Fatal("expected category prefix match")
	}
}

func TestMatchSilence_CategoryMismatch(t *testing.T) {
	now := time.Now()
	// "XR-NODE" must NOT match "XR-EXEC-5" (different type)
	s := model.Silence{ID: 8, MatchCategory: "XR-NODE", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)}
	alert := model.Alert{NodeID: 5, ErrorCode: "XR-EXEC-5"}
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
		MatchCategory: "XR-NODE",
		MatchTags:     `["prod"]`,
		StartsAt:      now.Add(-time.Hour),
		EndsAt:        now.Add(time.Hour),
	}
	alert := model.Alert{NodeID: 1, ErrorCode: "XR-NODE-42"}
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

func TestMatchSilence_CategoryPrefixDoesNotMatchSiblingPrefix(t *testing.T) {
	now := time.Now()
	// "XR-NODE" must NOT match "XR-NODE-EXPIRY-5" — tail "EXPIRY-5" is not purely numeric
	s := model.Silence{MatchCategory: "XR-NODE", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)}
	alert := model.Alert{NodeID: 1, ErrorCode: "XR-NODE-EXPIRY-5"}
	if got := MatchSilence(alert, model.Node{ID: 1}, []model.Silence{s}, now); got != nil {
		t.Fatal("XR-NODE prefix must NOT match XR-NODE-EXPIRY-5")
	}
}

func TestMatchSilence_CategoryPrefixMatchesInstance(t *testing.T) {
	now := time.Now()
	cases := []struct {
		cat         string
		code        string
		shouldMatch bool
	}{
		{"XR-NODE", "XR-NODE-1", true},
		{"XR-NODE", "XR-NODE-42", true},
		{"XR-NODE", "XR-NODE-EXPIRY-5", false},  // sibling prefix, must not match
		{"XR-NODE-EXPIRY", "XR-NODE-EXPIRY-5", true},
		{"XR-EXEC", "XR-EXEC-100", true},
		{"XR-EXEC", "XR-NODE-100", false},
	}
	for _, tc := range cases {
		s := model.Silence{MatchCategory: tc.cat, StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)}
		alert := model.Alert{NodeID: 1, ErrorCode: tc.code}
		got := MatchSilence(alert, model.Node{ID: 1}, []model.Silence{s}, now)
		matched := got != nil
		if matched != tc.shouldMatch {
			t.Errorf("cat=%q code=%q: got match=%v want=%v", tc.cat, tc.code, matched, tc.shouldMatch)
		}
	}
}

func uptr(u uint) *uint { return &u }
