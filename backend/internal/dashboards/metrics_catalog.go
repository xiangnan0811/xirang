package dashboards

// MetricDescriptor describes a metric available to panels.
type MetricDescriptor struct {
	Key                   string       `json:"key"`
	Label                 string       `json:"label"`
	Family                MetricFamily `json:"family"`
	DefaultAggregation    string       `json:"default_aggregation"`
	SupportedAggregations []string     `json:"supported_aggregations"`
}

// catalog is the authoritative metric list. Add new metrics here.
var catalog = []MetricDescriptor{
	{Key: "node.cpu", Label: "CPU 使用率", Family: FamilyNode, DefaultAggregation: "avg", SupportedAggregations: []string{"avg", "max", "min"}},
	{Key: "node.memory", Label: "内存使用率", Family: FamilyNode, DefaultAggregation: "avg", SupportedAggregations: []string{"avg", "max", "min"}},
	{Key: "node.disk_pct", Label: "磁盘使用率", Family: FamilyNode, DefaultAggregation: "avg", SupportedAggregations: []string{"avg", "max", "min"}},
	{Key: "node.load", Label: "负载 1m", Family: FamilyNode, DefaultAggregation: "avg", SupportedAggregations: []string{"avg", "max", "min"}},
	{Key: "node.latency_ms", Label: "SSH 延迟 (ms)", Family: FamilyNode, DefaultAggregation: "avg", SupportedAggregations: []string{"avg", "max", "min", "p50", "p95", "p99"}},
	{Key: "task.success_rate", Label: "任务成功率", Family: FamilyTask, DefaultAggregation: "avg", SupportedAggregations: []string{"avg"}},
	{Key: "task.throughput", Label: "任务吞吐量 (Mbps)", Family: FamilyTask, DefaultAggregation: "sum", SupportedAggregations: []string{"sum", "avg"}},
	{Key: "task.duration_p95", Label: "任务时长分位", Family: FamilyTask, DefaultAggregation: "p95", SupportedAggregations: []string{"p50", "p95", "p99"}},
}

// ListMetrics returns a copy of the catalog.
func ListMetrics() []MetricDescriptor {
	out := make([]MetricDescriptor, len(catalog))
	copy(out, catalog)
	return out
}

// DescribeMetric returns the descriptor for a metric key, or nil.
func DescribeMetric(key string) *MetricDescriptor {
	for i := range catalog {
		if catalog[i].Key == key {
			return &catalog[i]
		}
	}
	return nil
}
