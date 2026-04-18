package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/metrics"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// NodeMetricsHandler serves the richer P5a metrics APIs for a single node:
// status snapshot, time-ranged series, disk-growth forecast. Kept as a new
// type so NodeHandler.Metrics (pagination-style, used by the overview page
// today) remains untouched.
type NodeMetricsHandler struct{ db *gorm.DB }

func NewNodeMetricsHandler(db *gorm.DB) *NodeMetricsHandler {
	return &NodeMetricsHandler{db: db}
}

type nodeStatusResponse struct {
	ProbedAt     *time.Time         `json:"probed_at"`
	Online       bool               `json:"online"`
	Current      map[string]float64 `json:"current"`
	Trend1h      map[string]float64 `json:"trend_1h"`
	Trend24h     map[string]float64 `json:"trend_24h"`
	OpenAlerts   int64              `json:"open_alerts"`
	RunningTasks int64              `json:"running_tasks"`
}

// Status returns the latest sample + 1h/24h hourly-tier aggregates + counters.
func (h *NodeMetricsHandler) Status(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	resp := nodeStatusResponse{
		Current:  map[string]float64{},
		Trend1h:  map[string]float64{},
		Trend24h: map[string]float64{},
	}

	var latest model.NodeMetricSample
	if err := h.db.Where("node_id = ?", id).Order("sampled_at desc").First(&latest).Error; err == nil {
		t := latest.SampledAt
		resp.ProbedAt = &t
		resp.Online = latest.ProbeOK
		resp.Current["cpu_pct"] = latest.CpuPct
		resp.Current["mem_pct"] = latest.MemPct
		resp.Current["disk_pct"] = latest.DiskPct
		resp.Current["load1"] = latest.Load1m
		if latest.LatencyMs != nil {
			resp.Current["latency_ms"] = float64(*latest.LatencyMs)
		}
	}

	now := time.Now().UTC()
	h.fillTrend(uint(id), now.Add(-1*time.Hour), now, resp.Trend1h)
	h.fillTrend(uint(id), now.Add(-24*time.Hour), now, resp.Trend24h)

	h.db.Model(&model.Alert{}).
		Where("node_id = ? AND status = ?", id, "open").
		Count(&resp.OpenAlerts)

	// TaskRun has no direct node_id column; join through tasks table.
	h.db.Table("task_runs").
		Joins("JOIN tasks ON tasks.id = task_runs.task_id").
		Where("tasks.node_id = ? AND task_runs.status = ?", id, "running").
		Count(&resp.RunningTasks)

	c.JSON(http.StatusOK, resp)
}

// Response payload shape ------------------------------------------------------

type metricPoint struct {
	T   time.Time `json:"t"`
	Avg *float64  `json:"avg,omitempty"`
	Max *float64  `json:"max,omitempty"`
	V   *float64  `json:"v,omitempty"`
}

type metricSeries struct {
	Metric string        `json:"metric"`
	Unit   string        `json:"unit"`
	Points []metricPoint `json:"points"`
}

type metricsSeriesResponse struct {
	Granularity   string         `json:"granularity"`
	BucketSeconds int            `json:"bucket_seconds"`
	Series        []metricSeries `json:"series"`
}

var fieldUnits = map[metrics.Field]string{
	metrics.FieldCPUPct:       "percent",
	metrics.FieldMemPct:       "percent",
	metrics.FieldDiskPct:      "percent",
	metrics.FieldLoad1:        "load",
	metrics.FieldLatencyMs:    "ms",
	metrics.FieldDiskGBUsed:   "gb",
	metrics.FieldProbeOKRatio: "ratio",
}

const rawMaxPointsPerSeries = 1500

// Metrics returns a time-windowed series for one or more fields, picking a
// storage tier automatically based on the span size unless the client
// overrides via ?granularity=.
func (h *NodeMetricsHandler) Metrics(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	from, errFrom := time.Parse(time.RFC3339, c.Query("from"))
	to, errTo := time.Parse(time.RFC3339, c.Query("to"))
	if errFrom != nil || errTo != nil || !to.After(from) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid from/to"})
		return
	}
	fields := resolveFields(c.Query("fields"))

	grQuery := c.DefaultQuery("granularity", "auto")
	var chosen metrics.Granularity
	switch grQuery {
	case "auto":
		chosen = metrics.SelectGranularity(to.Sub(from))
	case "raw":
		chosen = metrics.GranularityRaw
	case "hourly":
		chosen = metrics.GranularityHourly
	case "daily":
		chosen = metrics.GranularityDaily
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid granularity"})
		return
	}

	resp := metricsSeriesResponse{Granularity: string(chosen)}
	switch chosen {
	case metrics.GranularityRaw:
		resp.BucketSeconds = 0
		resp.Series = h.rawSeries(uint(id), from, to, fields)
	case metrics.GranularityHourly:
		resp.BucketSeconds = 3600
		resp.Series = h.hourlySeries(uint(id), from, to, fields)
	case metrics.GranularityDaily:
		resp.BucketSeconds = 86400
		resp.Series = h.dailySeries(uint(id), from, to, fields)
	}
	c.JSON(http.StatusOK, resp)
}

func resolveFields(raw string) []metrics.Field {
	if raw == "" {
		return metrics.AllFields
	}
	out := []metrics.Field{}
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, metrics.Field(s))
		}
	}
	return out
}

// rawSeries returns point lists from node_metric_samples. If the window holds
// more than 1500 samples per series, applies equidistant stride downsampling
// (keeping the last point). Raw-tier points use {"t", "v"} only — no avg/max.
func (h *NodeMetricsHandler) rawSeries(nodeID uint, from, to time.Time, fields []metrics.Field) []metricSeries {
	var rows []model.NodeMetricSample
	h.db.Where("node_id = ? AND sampled_at >= ? AND sampled_at < ?", nodeID, from, to).
		Order("sampled_at ASC").Find(&rows)

	stride := 1
	if len(rows) > rawMaxPointsPerSeries {
		stride = (len(rows) + rawMaxPointsPerSeries - 1) / rawMaxPointsPerSeries
	}

	out := make([]metricSeries, 0, len(fields))
	for _, f := range fields {
		pts := make([]metricPoint, 0, len(rows)/stride+1)
		for i, r := range rows {
			if i%stride != 0 && i != len(rows)-1 {
				continue
			}
			v := rawFieldValue(r, f)
			if v == nil {
				continue
			}
			val := *v
			pts = append(pts, metricPoint{T: r.SampledAt, V: &val})
		}
		out = append(out, metricSeries{Metric: string(f), Unit: fieldUnits[f], Points: pts})
	}
	return out
}

func rawFieldValue(r model.NodeMetricSample, f metrics.Field) *float64 {
	switch f {
	case metrics.FieldCPUPct:
		v := r.CpuPct
		return &v
	case metrics.FieldMemPct:
		v := r.MemPct
		return &v
	case metrics.FieldDiskPct:
		v := r.DiskPct
		return &v
	case metrics.FieldLoad1:
		v := r.Load1m
		return &v
	case metrics.FieldLatencyMs:
		if r.LatencyMs == nil {
			return nil
		}
		v := float64(*r.LatencyMs)
		return &v
	case metrics.FieldDiskGBUsed:
		return r.DiskGBUsed
	case metrics.FieldProbeOKRatio:
		var v float64
		if r.ProbeOK {
			v = 1
		}
		return &v
	}
	return nil
}

// hourlySeries pulls from node_metric_samples_hourly. Points carry avg + max.
func (h *NodeMetricsHandler) hourlySeries(nodeID uint, from, to time.Time, fields []metrics.Field) []metricSeries {
	var rows []model.NodeMetricSampleHourly
	h.db.Where("node_id = ? AND bucket_start >= ? AND bucket_start < ?", nodeID, from, to).
		Order("bucket_start ASC").Find(&rows)
	return aggregateSeries(fields, len(rows), func(i int) (time.Time, map[metrics.Field][2]*float64) {
		r := rows[i]
		return r.BucketStart, hourlyFieldMap(r)
	})
}

// dailySeries pulls from node_metric_samples_daily. Shape matches hourly.
func (h *NodeMetricsHandler) dailySeries(nodeID uint, from, to time.Time, fields []metrics.Field) []metricSeries {
	var rows []model.NodeMetricSampleDaily
	h.db.Where("node_id = ? AND bucket_start >= ? AND bucket_start < ?", nodeID, from, to).
		Order("bucket_start ASC").Find(&rows)
	return aggregateSeries(fields, len(rows), func(i int) (time.Time, map[metrics.Field][2]*float64) {
		r := rows[i]
		return r.BucketStart, dailyFieldMap(r)
	})
}

// aggregateSeries is the shared shape-builder for hourly & daily rows.
// accessor returns (bucketStart, field → [avgPtr, maxPtr]) for the i-th row.
func aggregateSeries(
	fields []metrics.Field,
	n int,
	accessor func(int) (time.Time, map[metrics.Field][2]*float64),
) []metricSeries {
	out := make([]metricSeries, 0, len(fields))
	for _, f := range fields {
		pts := make([]metricPoint, 0, n)
		for i := 0; i < n; i++ {
			t, fieldMap := accessor(i)
			pair, ok := fieldMap[f]
			if !ok || (pair[0] == nil && pair[1] == nil) {
				continue
			}
			p := metricPoint{T: t}
			if pair[0] != nil {
				avg := *pair[0]
				p.Avg = &avg
			}
			if pair[1] != nil {
				max := *pair[1]
				p.Max = &max
			}
			pts = append(pts, p)
		}
		out = append(out, metricSeries{Metric: string(f), Unit: fieldUnits[f], Points: pts})
	}
	return out
}

// probeOKRatio returns ratio pointer (nil if sample_count is 0).
func probeOKRatio(ok int64, total int64) *float64 {
	if total == 0 {
		return nil
	}
	v := float64(ok) / float64(total)
	return &v
}

func hourlyFieldMap(r model.NodeMetricSampleHourly) map[metrics.Field][2]*float64 {
	ratio := probeOKRatio(r.ProbeOK, r.SampleCount)
	return map[metrics.Field][2]*float64{
		metrics.FieldCPUPct:       {r.CpuPctAvg, r.CpuPctMax},
		metrics.FieldMemPct:       {r.MemPctAvg, r.MemPctMax},
		metrics.FieldDiskPct:      {r.DiskPctAvg, r.DiskPctMax},
		metrics.FieldLoad1:        {r.Load1Avg, r.Load1Max},
		metrics.FieldLatencyMs:    {r.LatencyMsAvg, r.LatencyMsMax},
		metrics.FieldDiskGBUsed:   {r.DiskGBUsedAvg, nil},
		metrics.FieldProbeOKRatio: {ratio, nil},
	}
}

func dailyFieldMap(r model.NodeMetricSampleDaily) map[metrics.Field][2]*float64 {
	ratio := probeOKRatio(r.ProbeOK, r.SampleCount)
	return map[metrics.Field][2]*float64{
		metrics.FieldCPUPct:       {r.CpuPctAvg, r.CpuPctMax},
		metrics.FieldMemPct:       {r.MemPctAvg, r.MemPctMax},
		metrics.FieldDiskPct:      {r.DiskPctAvg, r.DiskPctMax},
		metrics.FieldLoad1:        {r.Load1Avg, r.Load1Max},
		metrics.FieldLatencyMs:    {r.LatencyMsAvg, r.LatencyMsMax},
		metrics.FieldDiskGBUsed:   {r.DiskGBUsedAvg, nil},
		metrics.FieldProbeOKRatio: {ratio, nil},
	}
}

type diskForecastResponse struct {
	DiskGBTotal   float64  `json:"disk_gb_total"`
	DiskGBUsedNow float64  `json:"disk_gb_used_now"`
	DailyGrowthGB *float64 `json:"daily_growth_gb"`
	Forecast      struct {
		DaysToFull *float64 `json:"days_to_full"`
		DateFull   *string  `json:"date_full"`
		Confidence string   `json:"confidence"`
	} `json:"forecast"`
}

// DiskForecast returns a disk-usage projection derived from the last 30 days
// of daily aggregates for the node.
func (h *NodeMetricsHandler) DiskForecast(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
	var rows []model.NodeMetricSampleDaily
	h.db.Where("node_id = ? AND bucket_start >= ?", id, cutoff).
		Order("bucket_start ASC").Find(&rows)

	resp := diskForecastResponse{}
	if len(rows) == 0 {
		resp.Forecast.Confidence = string(metrics.ConfidenceInsufficient)
		c.JSON(http.StatusOK, resp)
		return
	}

	pts := make([]metrics.ForecastPoint, 0, len(rows))
	t0 := rows[0].BucketStart
	for _, r := range rows {
		if r.DiskGBUsedAvg == nil {
			continue
		}
		day := r.BucketStart.Sub(t0).Hours() / 24
		pts = append(pts, metrics.ForecastPoint{Day: day, DiskGBUsed: *r.DiskGBUsedAvg})
		resp.DiskGBUsedNow = *r.DiskGBUsedAvg
		if r.DiskGBTotal != nil {
			resp.DiskGBTotal = *r.DiskGBTotal
		}
	}

	f := metrics.DiskForecast(pts, resp.DiskGBTotal)
	resp.DailyGrowthGB = f.DailyGrowthGB
	resp.Forecast.Confidence = string(f.Confidence)
	if f.DaysToFull != nil && *f.DaysToFull > 0 {
		resp.Forecast.DaysToFull = f.DaysToFull
		when := time.Now().UTC().Add(time.Duration(*f.DaysToFull*24) * time.Hour).Format("2006-01-02")
		resp.Forecast.DateFull = &when
	}
	c.JSON(http.StatusOK, resp)
}

// fillTrend aggregates hourly buckets in [from, to) into a flat map of
// metric → averaged value plus probe_ok_ratio. No-op if no buckets exist.
func (h *NodeMetricsHandler) fillTrend(nodeID uint, from, to time.Time, dst map[string]float64) {
	var rows []model.NodeMetricSampleHourly
	h.db.Where("node_id = ? AND bucket_start >= ? AND bucket_start < ?", nodeID, from, to).
		Find(&rows)
	if len(rows) == 0 {
		return
	}
	var cpu, mem, disk, load, latency float64
	var cpuN, memN, diskN, loadN, latencyN int
	var okSum, totalSum int64
	for _, r := range rows {
		if r.CpuPctAvg != nil {
			cpu += *r.CpuPctAvg
			cpuN++
		}
		if r.MemPctAvg != nil {
			mem += *r.MemPctAvg
			memN++
		}
		if r.DiskPctAvg != nil {
			disk += *r.DiskPctAvg
			diskN++
		}
		if r.Load1Avg != nil {
			load += *r.Load1Avg
			loadN++
		}
		if r.LatencyMsAvg != nil {
			latency += *r.LatencyMsAvg
			latencyN++
		}
		okSum += r.ProbeOK
		totalSum += r.SampleCount
	}
	if cpuN > 0 {
		dst["cpu_pct_avg"] = cpu / float64(cpuN)
	}
	if memN > 0 {
		dst["mem_pct_avg"] = mem / float64(memN)
	}
	if diskN > 0 {
		dst["disk_pct_avg"] = disk / float64(diskN)
	}
	if loadN > 0 {
		dst["load1_avg"] = load / float64(loadN)
	}
	if latencyN > 0 {
		dst["latency_ms_avg"] = latency / float64(latencyN)
	}
	if totalSum > 0 {
		dst["probe_ok_ratio"] = float64(okSum) / float64(totalSum)
	}
}
