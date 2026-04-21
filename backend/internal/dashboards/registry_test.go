package dashboards

import (
	"context"
	"testing"
)

type fakeProvider struct {
	family   MetricFamily
	supports map[string]bool
}

func (f *fakeProvider) Family() MetricFamily                    { return f.family }
func (f *fakeProvider) Supports(m string) bool                  { return f.supports[m] }
func (f *fakeProvider) SupportedAggregations(m string) []string { return []string{"avg"} }
func (f *fakeProvider) Query(_ context.Context, _ QueryRequest, _ int) (*QueryResponse, error) {
	return &QueryResponse{}, nil
}

func TestRegistry_FindProvider(t *testing.T) {
	resetForTest()
	t.Cleanup(resetForTest)
	node := &fakeProvider{family: FamilyNode, supports: map[string]bool{"node.cpu": true}}
	task := &fakeProvider{family: FamilyTask, supports: map[string]bool{"task.success_rate": true}}
	Register(node)
	Register(task)
	if p, ok := findProvider("node.cpu"); !ok || p != node {
		t.Fatal("expected node provider for node.cpu")
	}
	if p, ok := findProvider("task.success_rate"); !ok || p != task {
		t.Fatal("expected task provider for task.success_rate")
	}
	if _, ok := findProvider("unknown"); ok {
		t.Fatal("expected no provider for unknown metric")
	}
}

func TestRegistry_Snapshot(t *testing.T) {
	resetForTest()
	t.Cleanup(resetForTest)
	Register(&fakeProvider{family: FamilyNode})
	Register(&fakeProvider{family: FamilyTask})
	snap := providersSnapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2, got %d", len(snap))
	}
}
