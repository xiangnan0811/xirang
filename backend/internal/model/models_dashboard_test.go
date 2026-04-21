package model

import (
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
