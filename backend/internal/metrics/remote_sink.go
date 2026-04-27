package metrics

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/golang/snappy"
	"google.golang.org/protobuf/encoding/protowire"
)

// RemoteWriteSink ships every Sample to a Prometheus-compatible remote-write
// endpoint (Mimir, Cortex, VictoriaMetrics, Prometheus, Grafana Cloud).
//
// Push is synchronous per-sample. With the project's default probe cadence
// (~100 nodes / minute), the worst-case stall added to the probe loop is
// bounded by the configured timeout (default 5s). FanSink swallows errors
// from this sink, so a flaky remote endpoint cannot break DBSink.
//
// Bearer token may be empty - in which case no Authorization header is
// sent (suitable for internal Mimir/Cortex without auth).
//
// The wire format is hand-rolled against the Prometheus remote-write spec
// (snappy-compressed protobuf of WriteRequest/TimeSeries/Label/Sample) to
// avoid pulling in github.com/prometheus/prometheus as a dependency. The
// encoder uses google.golang.org/protobuf/encoding/protowire so the proto
// runtime is the only new transitive dep beyond snappy.
type RemoteWriteSink struct {
	endpoint string
	token    string
	timeout  time.Duration
	client   *http.Client
}

// NewRemoteWriteSink constructs a sink. endpoint MUST be the full URL
// including the /api/v1/push path (e.g. "https://mimir.example.com/api/v1/push").
// timeout MUST be > 0; callers should default to 5s.
func NewRemoteWriteSink(endpoint, bearerToken string, timeout time.Duration) *RemoteWriteSink {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &RemoteWriteSink{
		endpoint: endpoint,
		token:    bearerToken,
		timeout:  timeout,
		client:   &http.Client{Timeout: timeout},
	}
}

// Name identifies the sink for logging.
func (s *RemoteWriteSink) Name() string { return "remote-write" }

// promLabel and promSample are minimal hand-rolled mirrors of the
// Prometheus remote-write protobuf message types. Encoded inline below.
type promLabel struct {
	name  string
	value string
}

type promSample struct {
	value     float64
	timestamp int64 // millis since epoch
}

type promSeries struct {
	labels []promLabel
	sample promSample
}

// Write builds up to 8 TimeSeries per Sample (one per non-nil numeric
// field plus an always-emitted xirang_node_probe_ok), then POSTs them to
// the endpoint as a snappy-compressed protobuf payload. Returns the
// underlying error on any HTTP failure so observability can record it.
func (s *RemoteWriteSink) Write(ctx context.Context, sample Sample) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	tsMillis := sample.SampledAt.UnixMilli()
	commonLabels := []promLabel{
		{name: "node_id", value: fmt.Sprintf("%d", sample.NodeID)},
		{name: "node_name", value: sample.NodeName},
	}

	var series []promSeries
	add := func(metricName string, value float64) {
		labels := make([]promLabel, 0, len(commonLabels)+1)
		labels = append(labels, promLabel{name: "__name__", value: metricName})
		labels = append(labels, commonLabels...)
		series = append(series, promSeries{
			labels: labels,
			sample: promSample{value: value, timestamp: tsMillis},
		})
	}
	if sample.CPUPct != nil {
		add("xirang_node_cpu_pct", *sample.CPUPct)
	}
	if sample.MemPct != nil {
		add("xirang_node_mem_pct", *sample.MemPct)
	}
	if sample.DiskPct != nil {
		add("xirang_node_disk_pct", *sample.DiskPct)
	}
	if sample.Load1 != nil {
		add("xirang_node_load_1m", *sample.Load1)
	}
	if sample.LatencyMs != nil {
		add("xirang_node_latency_ms", *sample.LatencyMs)
	}
	if sample.DiskGBUsed != nil {
		add("xirang_node_disk_gb_used", *sample.DiskGBUsed)
	}
	if sample.DiskGBTotal != nil {
		add("xirang_node_disk_gb_total", *sample.DiskGBTotal)
	}
	probeOK := 0.0
	if sample.ProbeOK {
		probeOK = 1.0
	}
	add("xirang_node_probe_ok", probeOK)

	payload := encodeWriteRequest(series)
	compressed := snappy.Encode(nil, payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(compressed))
	if err != nil {
		remoteWriteTotal.WithLabelValues("failure").Inc()
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		remoteWriteTotal.WithLabelValues("failure").Inc()
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		remoteWriteTotal.WithLabelValues("failure").Inc()
		return fmt.Errorf("remote-write: http %d: %s", resp.StatusCode, string(body))
	}
	remoteWriteTotal.WithLabelValues("success").Inc()
	return nil
}

// encodeWriteRequest emits the Prometheus remote-write WriteRequest
// protobuf wire format:
//
//	message WriteRequest { repeated TimeSeries timeseries = 1; }
//	message TimeSeries   { repeated Label labels = 1; repeated Sample samples = 2; }
//	message Label        { string name = 1; string value = 2; }
//	message Sample       { double value = 1; int64 timestamp = 2; }
//
// Each repeated field becomes a length-delimited (wire type 2) entry per
// element; embedded messages are length-prefixed bytes of their inner
// encoding.
func encodeWriteRequest(series []promSeries) []byte {
	var out []byte
	for _, ts := range series {
		tsBytes := encodeTimeSeries(ts)
		out = protowire.AppendTag(out, 1, protowire.BytesType)
		out = protowire.AppendBytes(out, tsBytes)
	}
	return out
}

func encodeTimeSeries(ts promSeries) []byte {
	var out []byte
	for _, l := range ts.labels {
		labelBytes := encodeLabel(l)
		out = protowire.AppendTag(out, 1, protowire.BytesType)
		out = protowire.AppendBytes(out, labelBytes)
	}
	sampleBytes := encodeSample(ts.sample)
	out = protowire.AppendTag(out, 2, protowire.BytesType)
	out = protowire.AppendBytes(out, sampleBytes)
	return out
}

func encodeLabel(l promLabel) []byte {
	var out []byte
	out = protowire.AppendTag(out, 1, protowire.BytesType)
	out = protowire.AppendString(out, l.name)
	out = protowire.AppendTag(out, 2, protowire.BytesType)
	out = protowire.AppendString(out, l.value)
	return out
}

func encodeSample(s promSample) []byte {
	var out []byte
	out = protowire.AppendTag(out, 1, protowire.Fixed64Type)
	out = protowire.AppendFixed64(out, math.Float64bits(s.value))
	out = protowire.AppendTag(out, 2, protowire.VarintType)
	out = protowire.AppendVarint(out, uint64(s.timestamp))
	return out
}
