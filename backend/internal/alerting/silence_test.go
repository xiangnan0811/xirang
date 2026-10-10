package alerting

import (
	"testing"
	"time"

	"xirang/backend/internal/model"
)

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
		{"XR-NODE", "XR-NODE-EXPIRY-5", false}, // sibling prefix, must not match
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

func TestMatchSilence_XRExecNode42AtWindowBoundaries(t *testing.T) {
	start := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	silence := model.Silence{
		ID:            42,
		MatchNodeID:   uptr(42),
		MatchCategory: "XR-EXEC",
		MatchTags:     "[]",
		StartsAt:      start,
		EndsAt:        end,
	}

	match := func(now time.Time, nodeID uint, code string) *model.Silence {
		alert := model.Alert{NodeID: nodeID, ErrorCode: code, Message: "ignored"}
		node := model.Node{ID: nodeID, Tags: "prod"}
		return MatchSilence(alert, node, []model.Silence{silence}, now)
	}

	for _, code := range []string{"XR-EXEC-17", "XR-EXEC-18"} {
		if got := match(start, 42, code); got == nil || got.ID != silence.ID {
			t.Fatalf("start boundary: %s on node 42 must match silence 42, got %+v", code, got)
		}
	}
	if got := match(start, 43, "XR-EXEC-17"); got != nil {
		t.Fatalf("node 43 must not match node 42 silence, got %+v", got)
	}
	if got := match(start, 42, "XR-VRFY-17"); got != nil {
		t.Fatalf("XR-VRFY-17 must not match XR-EXEC, got %+v", got)
	}
	if got := match(start, 42, "XR-EXEC-extra-17"); got != nil {
		t.Fatalf("XR-EXEC-extra-17 must not match XR-EXEC, got %+v", got)
	}
	if got := match(end, 42, "XR-EXEC-17"); got != nil {
		t.Fatalf("end boundary must not match, got %+v", got)
	}
	if got := match(end.Add(-time.Nanosecond), 42, "XR-EXEC-17"); got == nil {
		t.Fatal("instant before end must match")
	}
}

func TestMatchSilence_DrillAndSnapshotCategoriesRejectSiblings(t *testing.T) {
	start := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	categories := []string{
		"XR-DRILL-drill_sandbox_unreachable",
		"XR-DRILL-drill_verify_failed",
		"XR-DRILL-drill_restore_failed",
		"XR-SNAPSHOT-CHURN",
		"XR-SNAPSHOT-RANSOM",
	}
	for i, category := range categories {
		silence := model.Silence{
			ID:            uint(100 + i),
			MatchNodeID:   uptr(42),
			MatchCategory: category,
			MatchTags:     "[]",
			StartsAt:      start,
			EndsAt:        start.Add(time.Hour),
		}
		own := model.Alert{NodeID: 42, ErrorCode: category + "-17"}
		if got := MatchSilence(own, model.Node{ID: 42}, []model.Silence{silence}, start); got == nil || got.ID != silence.ID {
			t.Fatalf("%s: instance 17 must match, got %+v", category, got)
		}
		extra := model.Alert{NodeID: 42, ErrorCode: category + "-extra-17"}
		if got := MatchSilence(extra, model.Node{ID: 42}, []model.Silence{silence}, start); got != nil {
			t.Fatalf("%s: extra suffix must not match, got %+v", category, got)
		}
		for _, sibling := range categories {
			if sibling == category {
				continue
			}
			alert := model.Alert{NodeID: 42, ErrorCode: sibling + "-17"}
			if got := MatchSilence(alert, model.Node{ID: 42}, []model.Silence{silence}, start); got != nil {
				t.Fatalf("%s matched sibling %s", category, sibling)
			}
		}
	}

	prefixes := []struct {
		category string
		code     string
	}{
		{"XR-DRILL", "XR-DRILL-drill_sandbox_unreachable-17"},
		{"XR-DRILL", "XR-DRILL-drill_verify_failed-17"},
		{"XR-DRILL", "XR-DRILL-drill_restore_failed-17"},
		{"XR-SNAPSHOT", "XR-SNAPSHOT-CHURN-17"},
		{"XR-SNAPSHOT", "XR-SNAPSHOT-RANSOM-17"},
	}
	for _, tc := range prefixes {
		silence := model.Silence{
			MatchNodeID:   uptr(42),
			MatchCategory: tc.category,
			MatchTags:     "[]",
			StartsAt:      start,
			EndsAt:        start.Add(time.Hour),
		}
		alert := model.Alert{NodeID: 42, ErrorCode: tc.code}
		if got := MatchSilence(alert, model.Node{ID: 42}, []model.Silence{silence}, start); got != nil {
			t.Fatalf("prefix %s must not match %s", tc.category, tc.code)
		}
	}
}

func TestMatchSilence_NilNodeIsNotPlatformLimit(t *testing.T) {
	start := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	silence := model.Silence{
		ID:            7,
		MatchNodeID:   nil,
		MatchCategory: "XR-EXEC",
		MatchTags:     "[]",
		StartsAt:      start,
		EndsAt:        start.Add(time.Hour),
	}
	cases := []model.Alert{
		{NodeID: 0, ErrorCode: "XR-EXEC-17"},
		{NodeID: 42, ErrorCode: "XR-EXEC-17"},
		{NodeID: 43, ErrorCode: "XR-EXEC-18"},
	}
	for _, alert := range cases {
		if got := MatchSilence(alert, model.Node{ID: alert.NodeID}, []model.Silence{silence}, start); got == nil || got.ID != silence.ID {
			t.Fatalf("nil node must match node %d %s, got %+v", alert.NodeID, alert.ErrorCode, got)
		}
	}

	// A pointer to node 0 matches that sentinel only.
	// Nil also matches real nodes, so it cannot stand in for a platform-only limit.
	platformOnly := silence
	platformOnly.MatchNodeID = uptr(0)
	platform := model.Alert{NodeID: 0, ErrorCode: "XR-EXEC-17"}
	node42 := model.Alert{NodeID: 42, ErrorCode: "XR-EXEC-17"}
	if got := MatchSilence(platform, model.Node{}, []model.Silence{platformOnly}, start); got == nil {
		t.Fatal("node 0 silence must match platform node 0")
	}
	if got := MatchSilence(node42, model.Node{ID: 42}, []model.Silence{platformOnly}, start); got != nil {
		t.Fatalf("node 0 silence must not match node 42, got %+v", got)
	}
}
