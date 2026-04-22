package model

import (
	"encoding/json"
	"testing"
)

func TestDashboardPanel_DecodedFilters(t *testing.T) {
	tests := []struct {
		name    string
		filters string
		want    PanelFilters
	}{
		{"empty string", "", PanelFilters{}},
		{"whitespace", "   ", PanelFilters{}},
		{"empty object", "{}", PanelFilters{}},
		{"node_ids only", `{"node_ids":[1,2,3]}`, PanelFilters{NodeIDs: []uint{1, 2, 3}}},
		{"task_ids only", `{"task_ids":[5]}`, PanelFilters{TaskIDs: []uint{5}}},
		{"both set", `{"node_ids":[1],"task_ids":[2]}`, PanelFilters{NodeIDs: []uint{1}, TaskIDs: []uint{2}}},
		{"invalid JSON returns zero", `not json`, PanelFilters{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &DashboardPanel{Filters: tt.filters}
			got := p.DecodedFilters()
			if !equalUintSlice(got.NodeIDs, tt.want.NodeIDs) {
				t.Fatalf("NodeIDs: got %v, want %v", got.NodeIDs, tt.want.NodeIDs)
			}
			if !equalUintSlice(got.TaskIDs, tt.want.TaskIDs) {
				t.Fatalf("TaskIDs: got %v, want %v", got.TaskIDs, tt.want.TaskIDs)
			}
		})
	}
}

func TestDashboardPanel_MarshalJSON_FiltersAsObject(t *testing.T) {
	p := DashboardPanel{
		ID:          1,
		DashboardID: 2,
		Title:       "cpu",
		ChartType:   "line",
		Metric:      "node.cpu",
		Filters:     `{"node_ids":[1,2]}`,
		Aggregation: "avg",
		LayoutW:     6,
		LayoutH:     4,
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var envelope struct {
		Filters PanelFilters `json:"filters"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(envelope.Filters.NodeIDs) != 2 || envelope.Filters.NodeIDs[0] != 1 || envelope.Filters.NodeIDs[1] != 2 {
		t.Fatalf("filters decoded wrong: %+v", envelope.Filters)
	}
}

func TestDashboardPanel_MarshalJSON_EmptyFilters(t *testing.T) {
	p := DashboardPanel{Filters: "{}"}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Ensure the raw JSON does NOT contain a string-typed filters field.
	if !containsJSONKey(string(raw), `"filters":{`) {
		t.Fatalf("expected object-shaped filters, got %s", string(raw))
	}
}

func containsJSONKey(s, key string) bool {
	for i := 0; i+len(key) <= len(s); i++ {
		if s[i:i+len(key)] == key {
			return true
		}
	}
	return false
}

func equalUintSlice(a, b []uint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
