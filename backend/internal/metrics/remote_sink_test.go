package metrics

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang/snappy"
	"google.golang.org/protobuf/encoding/protowire"
)

// decodedSeries is a test-only inverse of the encodeWriteRequest /
// encodeTimeSeries / encodeLabel / encodeSample helpers in remote_sink.go.
// We hand-decode the wire format here rather than pulling in a protobuf
// library so the tests prove our encoder + decoder agree on the spec.
type decodedSeries struct {
	labels []promLabel
	value  float64
	tsMs   int64
}

func decodeRemoteWriteBody(t *testing.T, r *http.Request) []decodedSeries {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	defer r.Body.Close() //nolint:errcheck
	plain, err := snappy.Decode(nil, raw)
	if err != nil {
		t.Fatalf("snappy decode: %v", err)
	}
	var out []decodedSeries
	b := plain
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 || num != 1 || typ != protowire.BytesType {
			t.Fatalf("WriteRequest expected field 1/bytes, got num=%d typ=%v", num, typ)
		}
		b = b[n:]
		tsBytes, n := protowire.ConsumeBytes(b)
		if n < 0 {
			t.Fatalf("WriteRequest: ConsumeBytes failed")
		}
		b = b[n:]
		out = append(out, decodeTimeSeries(t, tsBytes))
	}
	return out
}

func decodeTimeSeries(t *testing.T, b []byte) decodedSeries {
	t.Helper()
	var ds decodedSeries
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			t.Fatalf("TimeSeries: ConsumeTag failed")
		}
		b = b[n:]
		switch num {
		case 1: // labels
			if typ != protowire.BytesType {
				t.Fatalf("Label expected BytesType")
			}
			lb, m := protowire.ConsumeBytes(b)
			if m < 0 {
				t.Fatalf("Label: ConsumeBytes failed")
			}
			b = b[m:]
			ds.labels = append(ds.labels, decodeLabel(t, lb))
		case 2: // sample
			if typ != protowire.BytesType {
				t.Fatalf("Sample expected BytesType")
			}
			sb, m := protowire.ConsumeBytes(b)
			if m < 0 {
				t.Fatalf("Sample: ConsumeBytes failed")
			}
			b = b[m:]
			val, ts := decodeSample(t, sb)
			ds.value = val
			ds.tsMs = ts
		default:
			t.Fatalf("TimeSeries: unknown field %d", num)
		}
	}
	return ds
}

func decodeLabel(t *testing.T, b []byte) promLabel {
	t.Helper()
	var l promLabel
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 || typ != protowire.BytesType {
			t.Fatalf("Label: bad tag")
		}
		b = b[n:]
		s, m := protowire.ConsumeString(b)
		if m < 0 {
			t.Fatalf("Label: ConsumeString failed")
		}
		b = b[m:]
		switch num {
		case 1:
			l.name = s
		case 2:
			l.value = s
		default:
			t.Fatalf("Label: unknown field %d", num)
		}
	}
	return l
}

func decodeSample(t *testing.T, b []byte) (value float64, tsMs int64) {
	t.Helper()
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			t.Fatalf("Sample: bad tag")
		}
		b = b[n:]
		switch num {
		case 1:
			if typ != protowire.Fixed64Type {
				t.Fatalf("Sample value expected Fixed64Type")
			}
			bits, m := protowire.ConsumeFixed64(b)
			if m < 0 {
				t.Fatalf("Sample value: ConsumeFixed64 failed")
			}
			b = b[m:]
			value = math.Float64frombits(bits)
		case 2:
			if typ != protowire.VarintType {
				t.Fatalf("Sample timestamp expected VarintType")
			}
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				t.Fatalf("Sample timestamp: ConsumeVarint failed")
			}
			b = b[m:]
			tsMs = int64(v)
		default:
			t.Fatalf("Sample: unknown field %d", num)
		}
	}
	return
}

func metricNames(series []decodedSeries) []string {
	out := make([]string, 0, len(series))
	for _, s := range series {
		for _, l := range s.labels {
			if l.name == "__name__" {
				out = append(out, l.value)
				break
			}
		}
	}
	return out
}

func float64Ptr(v float64) *float64 { return &v }

func TestRemoteWriteSink_PostsAllNonNilFields(t *testing.T) {
	var got []decodedSeries
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = decodeRemoteWriteBody(t, r)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewRemoteWriteSink(srv.URL, "", 2*time.Second)
	sample := Sample{
		NodeID:      7,
		NodeName:    "n7",
		SampledAt:   time.Date(2026, 4, 1, 12, 30, 0, 0, time.UTC),
		CPUPct:      float64Ptr(12.5),
		MemPct:      float64Ptr(47),
		DiskPct:     float64Ptr(80),
		Load1:       float64Ptr(0.9),
		LatencyMs:   float64Ptr(15),
		DiskGBUsed:  float64Ptr(120),
		DiskGBTotal: float64Ptr(500),
		ProbeOK:     true,
	}
	if err := sink.Write(context.Background(), sample); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("server did not receive series")
	}
	names := metricNames(got)
	want := []string{
		"xirang_node_cpu_pct",
		"xirang_node_mem_pct",
		"xirang_node_disk_pct",
		"xirang_node_load_1m",
		"xirang_node_latency_ms",
		"xirang_node_disk_gb_used",
		"xirang_node_disk_gb_total",
		"xirang_node_probe_ok",
	}
	if len(names) != len(want) {
		t.Fatalf("expected %d series, got %d (%v)", len(want), len(names), names)
	}
	for i, n := range want {
		if names[i] != n {
			t.Fatalf("series[%d] = %q; want %q", i, names[i], n)
		}
	}
	gotLabels := map[string]string{}
	for _, l := range got[0].labels {
		gotLabels[l.name] = l.value
	}
	if gotLabels["node_id"] != "7" || gotLabels["node_name"] != "n7" {
		t.Fatalf("expected node_id=7, node_name=n7; got %+v", gotLabels)
	}
	if got[0].tsMs != sample.SampledAt.UnixMilli() {
		t.Fatalf("ts mismatch: got %d want %d", got[0].tsMs, sample.SampledAt.UnixMilli())
	}
	if got[0].value != 12.5 {
		t.Fatalf("first series value = %f, want 12.5", got[0].value)
	}
}

func TestRemoteWriteSink_OmitsNilFields(t *testing.T) {
	var got []decodedSeries
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = decodeRemoteWriteBody(t, r)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewRemoteWriteSink(srv.URL, "", 2*time.Second)
	sample := Sample{
		NodeID:    1,
		NodeName:  "only-cpu",
		SampledAt: time.Now(),
		CPUPct:    float64Ptr(33),
		ProbeOK:   false,
	}
	if err := sink.Write(context.Background(), sample); err != nil {
		t.Fatalf("Write: %v", err)
	}
	names := metricNames(got)
	want := []string{"xirang_node_cpu_pct", "xirang_node_probe_ok"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("expected %v, got %v", want, names)
	}
	for _, s := range got {
		var name string
		for _, l := range s.labels {
			if l.name == "__name__" {
				name = l.value
			}
		}
		if name == "xirang_node_probe_ok" && s.value != 0 {
			t.Fatalf("probe_ok value should be 0 for ProbeOK=false, got %f", s.value)
		}
	}
}

func TestRemoteWriteSink_BearerTokenHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewRemoteWriteSink(srv.URL, "secret-token-123", 2*time.Second)
	if err := sink.Write(context.Background(), Sample{NodeID: 1, NodeName: "n", SampledAt: time.Now(), ProbeOK: true}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if gotAuth != "Bearer secret-token-123" {
		t.Fatalf("expected Authorization=Bearer secret-token-123, got %q", gotAuth)
	}

	gotAuth = ""
	sink2 := NewRemoteWriteSink(srv.URL, "", 2*time.Second)
	if err := sink2.Write(context.Background(), Sample{NodeID: 1, NodeName: "n", SampledAt: time.Now(), ProbeOK: true}); err != nil {
		t.Fatalf("Write (no token): %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("expected no Authorization header without token, got %q", gotAuth)
	}
}

func TestRemoteWriteSink_TimeoutReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewRemoteWriteSink(srv.URL, "", 50*time.Millisecond)
	err := sink.Write(context.Background(), Sample{NodeID: 1, NodeName: "n", SampledAt: time.Now(), ProbeOK: true})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestRemoteWriteSink_NonOK_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	defer srv.Close()

	sink := NewRemoteWriteSink(srv.URL, "", 2*time.Second)
	err := sink.Write(context.Background(), Sample{NodeID: 1, NodeName: "n", SampledAt: time.Now(), ProbeOK: true})
	if err == nil {
		t.Fatal("expected error on HTTP 500, got nil")
	}
}
