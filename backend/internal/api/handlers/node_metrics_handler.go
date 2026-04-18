package handlers

import (
	"net/http"
	"strconv"
	"time"

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
