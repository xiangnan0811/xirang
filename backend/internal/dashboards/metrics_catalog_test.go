package dashboards

import "testing"

func TestListMetrics_Count(t *testing.T) {
	if got := len(ListMetrics()); got != 8 {
		t.Fatalf("expected 8 metrics, got %d", got)
	}
}

func TestDescribeMetric_Hit(t *testing.T) {
	d := DescribeMetric("node.cpu")
	if d == nil || d.Family != FamilyNode || d.DefaultAggregation != "avg" {
		t.Fatalf("unexpected descriptor: %+v", d)
	}
}

func TestDescribeMetric_Miss(t *testing.T) {
	if d := DescribeMetric("unknown"); d != nil {
		t.Fatalf("expected nil, got %+v", d)
	}
}

func TestCatalog_AllKeysUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range catalog {
		if seen[m.Key] {
			t.Fatalf("duplicate metric key: %s", m.Key)
		}
		seen[m.Key] = true
	}
}

func TestCatalog_DefaultAggregationIsSupported(t *testing.T) {
	for _, m := range catalog {
		found := false
		for _, a := range m.SupportedAggregations {
			if a == m.DefaultAggregation {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s: default %s not in supported %v", m.Key, m.DefaultAggregation, m.SupportedAggregations)
		}
	}
}
