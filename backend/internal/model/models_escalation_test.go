package model

import "testing"

func TestEscalationPolicy_DecodedLevels(t *testing.T) {
	tests := []struct {
		name   string
		levels string
		want   int
	}{
		{"empty", "", 0},
		{"whitespace", "   ", 0},
		{"one level", `[{"delay_seconds":0,"integration_ids":[1],"severity_override":"","tags":[]}]`, 1},
		{"three levels", `[{"delay_seconds":0,"integration_ids":[1],"severity_override":"","tags":[]},{"delay_seconds":300,"integration_ids":[2],"severity_override":"critical","tags":["escalated"]},{"delay_seconds":900,"integration_ids":[3],"severity_override":"","tags":[]}]`, 3},
		{"invalid json", `not json`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &EscalationPolicy{Levels: tt.levels}
			got := p.DecodedLevels()
			if len(got) != tt.want {
				t.Fatalf("len=%d want %d", len(got), tt.want)
			}
		})
	}
}

func TestEscalationPolicy_DecodedLevels_FieldFidelity(t *testing.T) {
	p := &EscalationPolicy{Levels: `[{"delay_seconds":300,"integration_ids":[2,3],"severity_override":"critical","tags":["a","b"]}]`}
	levels := p.DecodedLevels()
	if len(levels) != 1 {
		t.Fatalf("len=%d", len(levels))
	}
	l := levels[0]
	if l.DelaySeconds != 300 || len(l.IntegrationIDs) != 2 || l.IntegrationIDs[0] != 2 || l.IntegrationIDs[1] != 3 ||
		l.SeverityOverride != "critical" || len(l.Tags) != 2 || l.Tags[0] != "a" || l.Tags[1] != "b" {
		t.Fatalf("field mismatch: %+v", l)
	}
}

func TestAlert_DecodedTags(t *testing.T) {
	a := &Alert{Tags: `["escalated","on-call"]`}
	got := a.DecodedTags()
	if len(got) != 2 || got[0] != "escalated" || got[1] != "on-call" {
		t.Fatalf("tags: %v", got)
	}
}

func TestAlert_DecodedTags_EmptyAndInvalid(t *testing.T) {
	if tags := (&Alert{Tags: ""}).DecodedTags(); tags != nil {
		t.Fatalf("empty: got %v", tags)
	}
	if tags := (&Alert{Tags: "not json"}).DecodedTags(); tags != nil {
		t.Fatalf("invalid: got %v", tags)
	}
}

func TestAlertEscalationEvent_DecodedFields(t *testing.T) {
	e := &AlertEscalationEvent{
		IntegrationIDs: `[7,8,9]`,
		TagsAdded:      `["esc"]`,
	}
	ids := e.DecodedIntegrationIDs()
	if len(ids) != 3 || ids[0] != 7 || ids[2] != 9 {
		t.Fatalf("ids: %v", ids)
	}
	tags := e.DecodedTagsAdded()
	if len(tags) != 1 || tags[0] != "esc" {
		t.Fatalf("tags: %v", tags)
	}
}
