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
