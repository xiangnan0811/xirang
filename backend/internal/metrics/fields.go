package metrics

// Field identifies a metric series exposed through the node metrics API.
// Values match the JSON query parameter and the internal column names.
type Field string

const (
	FieldCPUPct       Field = "cpu_pct"
	FieldMemPct       Field = "mem_pct"
	FieldDiskPct      Field = "disk_pct"
	FieldLoad1        Field = "load1"
	FieldLatencyMs    Field = "latency_ms"
	FieldDiskGBUsed   Field = "disk_gb_used"
	FieldProbeOKRatio Field = "probe_ok_ratio"
)

// AllFields is the default set returned by GET /nodes/:id/metrics when the
// `fields` query parameter is omitted.
var AllFields = []Field{
	FieldCPUPct, FieldMemPct, FieldDiskPct, FieldLoad1,
	FieldLatencyMs, FieldDiskGBUsed, FieldProbeOKRatio,
}
