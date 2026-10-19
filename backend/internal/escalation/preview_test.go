package escalation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

var previewT0 = time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

func previewFixture(t *testing.T) PreviewInput {
	t.Helper()
	levels, err := json.Marshal([]model.EscalationLevel{
		{DelaySeconds: 0, IntegrationIDs: []uint{1, 1, 2, 999}, Tags: []string{}},
		{DelaySeconds: 300, IntegrationIDs: []uint{1}, Tags: []string{"escalated"}},
		{DelaySeconds: 900, IntegrationIDs: []uint{1}, SeverityOverride: "critical", Tags: []string{"escalated", "urgent"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return PreviewInput{
		Mode: "draft", GeneratedAt: previewT0, EvaluationAt: previewT0, Source: "draft",
		Alert:        model.Alert{ID: 42, NodeID: 7, Severity: "warning", Status: "open", TriggeredAt: previewT0, ErrorCode: "XR-EXEC-7", Tags: "[]", LastLevelFired: -1},
		Policy:       &model.EscalationPolicy{ID: 5, Name: "preview policy", MinSeverity: "warning", Enabled: true, Levels: string(levels), UpdatedAt: previewT0},
		Node:         model.Node{ID: 7, Tags: "prod"},
		Integrations: []IntegrationMetadata{{ID: 1, Name: "enabled", Type: "webhook", Enabled: true}, {ID: 2, Name: "disabled", Type: "email", Enabled: false}},
	}
}

func mustPreview(t *testing.T, in PreviewInput) PreviewResult {
	t.Helper()
	got, err := Preview(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestEscalationPreviewDeterministicTimeline(t *testing.T) {
	for _, tc := range []struct {
		name       string
		evaluation time.Time
		offsets    []time.Duration
	}{
		{"on-time", previewT0, []time.Duration{0, 5 * time.Minute, 15 * time.Minute}},
		{"overdue", previewT0.Add(20 * time.Minute), []time.Duration{20 * time.Minute, 20*time.Minute + 30*time.Second, 21 * time.Minute}},
		{"fractional-grid", previewT0.Add(time.Second), []time.Duration{time.Second, 5*time.Minute + time.Second, 15*time.Minute + time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := previewFixture(t)
			in.EvaluationAt = tc.evaluation
			got := mustPreview(t, in)
			if got.Status != "projected" || len(got.Steps) != 3 || got.TickSeconds != 30 || got.Basis != "current_snapshot" || !got.ExpiresAt.Equal(previewT0.Add(time.Minute)) {
				t.Fatalf("header=%+v", got)
			}
			if got.RequestedAlertID != nil || got.Alert.ID != nil || got.Policy.ID != nil || got.Policy.UpdatedAt != nil || got.DeliveryDecision != nil || got.DeliveryCounts != nil {
				t.Fatalf("draft identity leaked: %+v", got)
			}
			for i, offset := range []time.Duration{0, 5 * time.Minute, 15 * time.Minute} {
				step := got.Steps[i]
				if step.LevelIndex != i || !step.DueAt.Equal(previewT0.Add(offset)) || step.ProjectedAt == nil || !step.ProjectedAt.Equal(previewT0.Add(tc.offsets[i])) || step.Status != "would_enqueue" {
					t.Fatalf("step %d=%+v", i, step)
				}
			}
			channels := got.Steps[0].Channels
			if len(channels) != 3 || channels[0].ID != 1 || channels[0].State != "enabled" || channels[1].ID != 2 || channels[1].State != "disabled" || channels[2].ID != 999 || channels[2].State != "missing" || channels[2].Name != nil || channels[2].Type != nil {
				t.Fatalf("channels=%+v", channels)
			}
			if got.NextLevelIndex == nil || *got.NextLevelIndex != 0 || got.NextDueAt == nil || !got.NextDueAt.Equal(previewT0) || got.NextDueNow == nil || !*got.NextDueNow {
				t.Fatalf("original next cursor=%+v", got)
			}
			if !reflect.DeepEqual(got.Steps[2].TagsAfter, []string{"escalated", "urgent"}) || got.Steps[2].SeverityAfter != "critical" {
				t.Fatalf("projection=%+v", got.Steps[2])
			}
		})
	}
	in := previewFixture(t)
	in.Alert.LastLevelFired = 0
	got := mustPreview(t, in)
	if got.NextDueNow == nil || *got.NextDueNow || len(got.Steps) != 2 || got.Steps[0].LevelIndex != 1 {
		t.Fatalf("waiting is not an exclusion: %+v", got)
	}
}

func TestEscalationPreviewSilenceUsesRealMatcherAndFutureWindows(t *testing.T) {
	for _, tc := range []struct {
		name, code, nodeTags, alertTags string
		evaluation                      time.Time
		want                            string
	}{
		{"numeric-category", "XR-EXEC-7", "prod", "[]", previewT0, "silenced"},
		{"exact-category", "XR-EXEC", "prod", "[]", previewT0, "silenced"},
		{"non-numeric-category", "XR-EXEC-X", "prod", "[]", previewT0, "would_enqueue"},
		{"unicode-digit", "XR-EXEC-７", "prod", "[]", previewT0, "would_enqueue"},
		{"alert-tags-not-node-tags", "XR-EXEC-7", "", `["prod"]`, previewT0, "would_enqueue"},
		{"end-exclusive", "XR-EXEC-7", "prod", "[]", previewT0.Add(30 * time.Second), "would_enqueue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := previewFixture(t)
			in.Alert.ErrorCode = tc.code
			in.Alert.Tags = tc.alertTags
			in.Node.Tags = tc.nodeTags
			in.EvaluationAt = tc.evaluation
			in.Silences = []model.Silence{
				{ID: 9, MatchCategory: "XR-EXEC", MatchTags: `["prod"]`, StartsAt: previewT0, EndsAt: previewT0.Add(30 * time.Second)},
				{ID: 3, MatchCategory: "XR-EXEC", MatchTags: `["prod"]`, StartsAt: previewT0, EndsAt: previewT0.Add(30 * time.Second)},
				{ID: 12, StartsAt: previewT0.Add(5 * time.Minute), EndsAt: previewT0.Add(6 * time.Minute)},
			}
			got := mustPreview(t, in)
			if got.Steps[0].Status != tc.want {
				t.Fatalf("step=%+v", got.Steps[0])
			}
			if tc.want == "silenced" && !reflect.DeepEqual(got.Steps[0].MatchedSilenceIDs, []uint{3, 9}) {
				t.Fatalf("matches=%v", got.Steps[0].MatchedSilenceIDs)
			}
			if got.Steps[1].Status != "silenced" || !reflect.DeepEqual(got.Steps[1].MatchedSilenceIDs, []uint{12}) || got.Steps[2].Status != "would_enqueue" {
				t.Fatalf("future silence/progression=%+v", got.Steps)
			}
		})
	}
}

func TestEscalationPreviewExclusionAndDowngrade(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		modify     func(*PreviewInput)
	}{
		{"no-policy", "no_policy", func(in *PreviewInput) { in.Policy = nil; in.Source = "none" }},
		{"disabled", "disabled", func(in *PreviewInput) { in.Policy.Enabled = false }},
		{"below-minimum", "below_minimum", func(in *PreviewInput) { in.Alert.Severity = "info" }},
		{"complete", "complete", func(in *PreviewInput) { in.Alert.LastLevelFired = 999 }},
		{"acked", "not_open", func(in *PreviewInput) { in.Alert.Status = "acked" }},
		{"resolved", "not_open", func(in *PreviewInput) { in.Alert.Status = "resolved" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := previewFixture(t)
			in.Mode = "alert"
			in.Source = "node"
			in.DeliveryCounts = &DeliveryCounts{Pending: 1, Sent: 1}
			in.Alert.DeliveryDecision = "direct"
			tc.modify(&in)
			got := mustPreview(t, in)
			if got.Status != tc.want || len(got.Steps) != 0 || got.NextLevelIndex != nil || got.NextDueAt != nil || got.NextDueNow != nil || got.DeliveryCounts.Pending != 1 || got.DeliveryCounts.Sent != 1 || *got.DeliveryDecision != "direct" {
				t.Fatalf("result=%+v", got)
			}
		})
	}
	in := previewFixture(t)
	levels := in.Policy.DecodedLevels()
	levels[0].SeverityOverride = "info"
	levels[0].Tags = []string{"downgraded"}
	raw, err := json.Marshal(levels)
	if err != nil {
		t.Fatal(err)
	}
	in.Policy.Levels = string(raw)
	got := mustPreview(t, in)
	if got.Status != "projected" || got.Steps[0].Status != "would_enqueue" || got.Steps[1].Status != "blocked_minimum" || got.Steps[2].Status != "blocked_prior" {
		t.Fatalf("downgrade=%+v", got)
	}
	for _, step := range got.Steps[1:] {
		if step.ProjectedAt != nil || step.SeverityBefore != "info" || step.SeverityAfter != "info" || !reflect.DeepEqual(step.TagsAfter, []string{"downgraded"}) || len(step.MatchedSilenceIDs) != 0 {
			t.Fatalf("blocked=%+v", step)
		}
	}
	in = previewFixture(t)
	in.Integrations = nil
	got = mustPreview(t, in)
	if got.Steps[0].Status != "no_enabled_channels" || got.Steps[2].SeverityAfter != "critical" {
		t.Fatalf("no-channel progression=%+v", got)
	}
}

func TestEscalationPreviewRejectsUnsupportedWithoutPartialData(t *testing.T) {
	for _, tc := range []struct {
		name, warning string
		mutate        func(*PreviewInput)
	}{
		{"severity", "unsupported_alert_state", func(in *PreviewInput) { in.Alert.Severity = "fatal" }},
		{"status", "unsupported_alert_state", func(in *PreviewInput) { in.Alert.Status = "closed" }},
		{"cursor", "unsupported_alert_state", func(in *PreviewInput) { in.Alert.LastLevelFired = -2 }},
		{"unrepresentable-cursor", "unsupported_alert_state", func(in *PreviewInput) { in.Alert.LastLevelFired = int(^uint(0) >> 1) }},
		{"retired", "unsupported_alert_state", func(in *PreviewInput) { in.Alert.DeliveryReason = "feature_retired" }},
		{"alert-json", "unsupported_alert_state", func(in *PreviewInput) { in.Alert.Tags = `["ok",7]` }},
		{"alert-null-element", "unsupported_alert_state", func(in *PreviewInput) { in.Alert.Tags = `[null]` }},
		{"policy-json", "unsupported_configuration", func(in *PreviewInput) { in.Policy.Levels = `[{` }},
		{"policy-null", "unsupported_configuration", func(in *PreviewInput) { in.Policy.Levels = `null` }},
		{"policy-empty", "unsupported_configuration", func(in *PreviewInput) { in.Policy.Levels = `[]` }},
		{"level-null", "unsupported_configuration", func(in *PreviewInput) { in.Policy.Levels = `[null]` }},
		{"channel-limit", "unsupported_configuration", func(in *PreviewInput) {
			levels := in.Policy.DecodedLevels()
			levels[0].IntegrationIDs = make([]uint, 101)
			for i := range levels[0].IntegrationIDs {
				levels[0].IntegrationIDs[i] = 1
			}
			raw, err := json.Marshal(levels)
			if err != nil {
				t.Fatal(err)
			}
			in.Policy.Levels = string(raw)
		}},
		{"unsafe-policy-id", "unsupported_configuration", func(in *PreviewInput) { in.Policy.ID = uint(1 << 53) }},
		{"unsafe-node-id", "unsupported_alert_state", func(in *PreviewInput) { in.Alert.NodeID = uint(1 << 53) }},
		{"level-null-tag", "unsupported_configuration", func(in *PreviewInput) { in.Policy.Levels = `[{"delay_seconds":0,"integration_ids":[1],"tags":[null]}]` }},
		{"silence-json", "unsupported_configuration", func(in *PreviewInput) { in.Silences = []model.Silence{{ID: 1, MatchTags: `{}`}} }},
		{"overflow-delay", "unsupported_time", func(in *PreviewInput) {
			in.Policy.Levels = `[{"delay_seconds":0,"integration_ids":[1],"tags":[]},{"delay_seconds":10000000000,"integration_ids":[1],"tags":[]}]`
		}},
		{"duration-saturation", "unsupported_time", func(in *PreviewInput) { in.Alert.TriggeredAt = time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC) }},
		{"year-overflow", "unsupported_time", func(in *PreviewInput) {
			in.Alert.TriggeredAt = time.Date(9999, 12, 31, 23, 59, 0, 0, time.UTC)
			in.EvaluationAt = in.Alert.TriggeredAt
		}},
		{"grid-overflow", "unsupported_time", func(in *PreviewInput) {
			in.Alert.TriggeredAt = previewT0
			in.EvaluationAt = previewT0.Add(-time.Duration(1<<63 - 1))
			in.Policy.Levels = `[{"delay_seconds":0,"integration_ids":[1],"tags":[]},{"delay_seconds":1,"integration_ids":[1],"tags":[]}]`
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := previewFixture(t)
			in.Mode = "alert"
			in.Source = "node"
			tc.mutate(&in)
			got := mustPreview(t, in)
			if got.Status != "unsupported_state" || got.Alert != nil || got.Policy != nil || len(got.Steps) != 0 || got.NextDueAt != nil || got.NextLevelIndex != nil || got.DeliveryCounts != nil || got.DeliveryDecision != nil || got.PolicySource != "none" || got.RequestedAlertID == nil || *got.RequestedAlertID != 42 || !reflect.DeepEqual(got.Warnings, []string{tc.warning}) {
				t.Fatalf("unsafe partial result=%+v", got)
			}
		})
	}
	for _, raw := range []string{"", " \n ", "null", "[]"} {
		t.Run(fmt.Sprintf("legacy-%q", raw), func(t *testing.T) {
			in := previewFixture(t)
			in.Alert.Tags = raw
			in.Silences = []model.Silence{{ID: 1, MatchTags: raw, StartsAt: previewT0, EndsAt: previewT0.Add(time.Second)}}
			got := mustPreview(t, in)
			if got.Status != "projected" || got.Alert.Tags == nil || got.Steps[0].Status != "silenced" {
				t.Fatalf("legacy=%+v", got)
			}
		})
	}
	in := previewFixture(t)
	in.Policy.Levels = `[{"delay_seconds":0,"integration_ids":[1],"tags":null}]`
	if got := mustPreview(t, in); got.Status != "projected" || got.Steps[0].TagsAfter == nil {
		t.Fatalf("null level tags=%+v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Preview(ctx, previewFixture(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}

// The enqueue side is deliberately the real producer of durable intents. Only
// post-commit provider dispatch is replaced, so no network operation is possible.
type previewDurableDispatcher struct {
	dispatcher *alerting.Dispatcher
	dispatches int
}

func (d *previewDurableDispatcher) EnqueueEscalationDeliveriesTx(tx *gorm.DB, a model.Alert, event model.AlertEscalationEvent, ids []uint) ([]uint, error) {
	return d.dispatcher.EnqueueEscalationDeliveriesTx(tx, a, event, ids)
}
func (d *previewDurableDispatcher) DispatchEscalationDeliveries(context.Context, model.Alert, uint, []uint) error {
	d.dispatches++
	return nil
}

func TestEscalationPreviewMatchesEngineDurableIntents(t *testing.T) {
	for _, mode := range []string{"normal", "silenced", "disabled-only", "downgrade"} {
		t.Run(mode, func(t *testing.T) {
			db := openEscalationDeliverySQLiteDB(t)
			for _, integration := range []model.Integration{{ID: 1, Name: "enabled", Type: "webhook", Enabled: true, Endpoint: "http://127.0.0.1:1"}, {ID: 2, Name: "disabled", Type: "webhook", Enabled: true, Endpoint: "http://127.0.0.1:1"}} {
				if err := db.Create(&integration).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Model(&model.Integration{}).Where("id = ?", 2).Update("enabled", false).Error; err != nil {
				t.Fatal(err)
			}
			in := previewFixture(t)
			in.Mode = "alert"
			in.Source = "node"
			levels := in.Policy.DecodedLevels()
			if mode == "disabled-only" {
				levels[0].IntegrationIDs = []uint{2, 999}
			}
			if mode == "downgrade" {
				levels[0].SeverityOverride = "info"
			}
			if mode == "silenced" {
				levels[0].SeverityOverride = "critical"
				levels[0].Tags = []string{"silenced-advance"}
			}
			policy := seedPolicy(t, NewService(db), "equivalence", levels, "warning")
			in.Policy = policy
			a := seedEscalationDeliveryAlert(t, db, policy.ID, 7, previewT0)
			if err := db.Model(a).Update("error_code", "XR-EXEC-7").Error; err != nil {
				t.Fatal(err)
			}
			a.ErrorCode = "XR-EXEC-7"
			in.Alert = *a
			if mode == "silenced" {
				in.Silences = []model.Silence{{ID: 1, MatchCategory: "XR-EXEC", StartsAt: previewT0, EndsAt: previewT0.Add(30 * time.Second)}}
			}
			got := mustPreview(t, in)
			disp := &previewDurableDispatcher{dispatcher: alerting.NewDispatcher(db, nil, nil)}
			now := previewT0
			engine := NewEngine(db, NewService(db), func(alert model.Alert) *model.Silence { return alerting.MatchSilence(alert, in.Node, in.Silences, now) }, disp)
			for _, step := range got.Steps {
				if step.ProjectedAt == nil {
					continue
				}
				now = *step.ProjectedAt
				engine.SetNowFn(func() time.Time { return now })
				engine.Tick(context.Background())
				var event model.AlertEscalationEvent
				if err := db.Where("alert_id = ? AND level_index = ?", a.ID, step.LevelIndex).First(&event).Error; err != nil {
					t.Fatal(err)
				}
				var current model.Alert
				if err := db.First(&current, a.ID).Error; err != nil {
					t.Fatal(err)
				}
				if event.SeverityBefore != step.SeverityBefore || event.SeverityAfter != step.SeverityAfter || current.LastLevelFired != step.LevelIndex || !reflect.DeepEqual(current.DecodedTags(), step.TagsAfter) {
					t.Fatalf("engine event=%+v alert=%+v preview=%+v", event, current, step)
				}
				var ids []uint
				if err := db.Model(&model.AlertDelivery{}).Where("alert_id = ? AND delivery_key LIKE ?", a.ID, fmt.Sprintf("%d:%d:%%", a.ID, event.ID)).Order("integration_id").Pluck("integration_id", &ids).Error; err != nil {
					t.Fatal(err)
				}
				want := []uint{}
				if step.Status == "would_enqueue" {
					for _, channel := range step.Channels {
						if channel.State == "enabled" {
							want = append(want, channel.ID)
						}
					}
				}
				if len(ids) != len(want) || (len(want) > 0 && !reflect.DeepEqual(ids, want)) {
					t.Fatalf("real durable targets=%v preview enabled=%v", ids, want)
				}
			}
			var events int64
			if err := db.Model(&model.AlertEscalationEvent{}).Where("alert_id = ?", a.ID).Count(&events).Error; err != nil {
				t.Fatal(err)
			}
			want := int64(3)
			if mode == "downgrade" {
				want = 1
			}
			if events != want {
				t.Fatalf("events=%d want=%d", events, want)
			}
		})
	}
}

func TestEnginePreviewExtractionPreservesOverflowRuntimeJudgment(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		triggered, evaluation time.Time
	}{
		{"wrapped-delay", previewT0, previewT0},
		{"saturated-positive", time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), previewT0},
		{"saturated-negative", time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), previewT0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openEngineDB(t)
			svc := NewService(db)
			delay := int(10000000000)
			p := seedPolicy(t, svc, "overflow", []model.EscalationLevel{{DelaySeconds: 0, IntegrationIDs: []uint{1}}, {DelaySeconds: delay, IntegrationIDs: []uint{1}}}, "warning")
			a := seedAlertOnNodeWithPolicy(t, db, 7, p.ID, tc.triggered, "warning")
			if err := db.Model(a).Update("last_level_fired", 0).Error; err != nil {
				t.Fatal(err)
			}
			a.LastLevelFired = 0
			disp := &recordingDispatcher{}
			engine := NewEngine(db, svc, nil, disp)
			engine.SetNowFn(func() time.Time { return tc.evaluation })
			engine.Tick(context.Background())
			want := 0
			if tc.evaluation.Sub(tc.triggered) >= time.Duration(delay)*time.Second {
				want = 1
			}
			var count int64
			if err := db.Model(&model.AlertEscalationEvent{}).Where("alert_id = ?", a.ID).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != int64(want) {
				t.Fatalf("production elapsed-duration semantics changed: events=%d want=%d", count, want)
			}
			in := previewFixture(t)
			in.Mode = "alert"
			in.Alert = *a
			in.Policy = p
			in.EvaluationAt = tc.evaluation
			if got := mustPreview(t, in); got.Status != "unsupported_state" {
				t.Fatalf("unsafe preview=%+v", got)
			}
		})
	}
}
