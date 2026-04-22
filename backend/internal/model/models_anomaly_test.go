package model

import "testing"

func TestAnomalyEvent_DecodedDetails(t *testing.T) {
	tests := []struct {
		name    string
		details string
		wantKey string
		wantVal any
	}{
		{"empty string", "", "", nil},
		{"whitespace", "   ", "", nil},
		{"empty object", "{}", "", nil},
		{"single pair", `{"samples":12}`, "samples", float64(12)},
		{"invalid json returns empty", `not json`, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &AnomalyEvent{Details: tt.details}
			got := e.DecodedDetails()
			if tt.wantKey == "" {
				if len(got) != 0 {
					t.Fatalf("expected empty, got %+v", got)
				}
				return
			}
			if got[tt.wantKey] != tt.wantVal {
				t.Fatalf("key %s: got %v want %v", tt.wantKey, got[tt.wantKey], tt.wantVal)
			}
		})
	}
}

func TestAnomalyEvent_Fields(t *testing.T) {
	s := 2.5
	fd := 3.7
	a := uint(42)
	e := AnomalyEvent{
		NodeID: 1, Detector: "ewma", Metric: "cpu_pct", Severity: "warning",
		ObservedValue: 85.0, BaselineValue: 30.0, Sigma: &s,
		ForecastDays: &fd, AlertID: &a, RaisedAlert: true,
		Details: `{"samples":12}`,
	}
	if e.NodeID != 1 || e.Detector != "ewma" || *e.Sigma != 2.5 || *e.ForecastDays != 3.7 || *e.AlertID != 42 {
		t.Fatalf("field mismatch: %+v", e)
	}
	if !e.RaisedAlert {
		t.Fatal("expected RaisedAlert=true")
	}
}
