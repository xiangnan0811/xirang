package handlers

import (
	"math"
	"strings"
	"time"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type OverviewTrafficHandler struct {
	db    *gorm.DB
	nowFn func() time.Time
}

type overviewTrafficWindowConfig struct {
	Window      string
	Duration    time.Duration
	Bucket      time.Duration
	LabelLayout string
}

type overviewTrafficBucket struct {
	MinuteSums map[time.Time]float64
}

// overviewTrafficPoint represents the average total throughput per display bucket.
type overviewTrafficPoint struct {
	Timestamp       string  `json:"timestamp"`
	TimestampMs     int64   `json:"timestamp_ms"`
	Label           string  `json:"label"`
	ThroughputMbps  float64 `json:"throughput_mbps"`
	SampleCount     int     `json:"sample_count"`
	ActiveTaskCount int     `json:"active_task_count"`
	StartedCount    int     `json:"started_count"`
	FailedCount     int     `json:"failed_count"`
}

type overviewTrafficResponse struct {
	Window         string `json:"window"`
	BucketMinutes  int    `json:"bucket_minutes"`
	HasRealSamples bool   `json:"has_real_samples"`
	// Truncated is true when sample or task-run rows hit the server-side cap;
	// clients should treat incomplete buckets as partial, not as true zeros.
	Truncated   bool                   `json:"truncated,omitempty"`
	GeneratedAt string                 `json:"generated_at"`
	Points      []overviewTrafficPoint `json:"points"`
}

// trafficSampleCap bounds samples loaded for one window. Hitting the cap sets Truncated.
// Package-level var so tests can shrink the cap without inserting 100k+ rows.
var trafficSampleCap = 100000

// trafficTaskRunCap bounds started/failed TaskRun rows loaded per window query.
// Without a cap, high-volume windows can load unbounded rows into memory.
var trafficTaskRunCap = 100000

func NewOverviewTrafficHandler(db *gorm.DB, nowFn func() time.Time) *OverviewTrafficHandler {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &OverviewTrafficHandler{db: db, nowFn: nowFn}
}

// Get godoc
// @Summary      获取流量趋势
// @Description  返回指定时间窗口（1h/24h/7d）内的任务吞吐量趋势数据
// @Tags         overview
// @Security     Bearer
// @Produce      json
// @Param        window  query     string  false  "时间窗口（1h/24h/7d，默认 1h）"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /overview/traffic [get]
func (h *OverviewTrafficHandler) Get(c *gin.Context) {
	// Owner-scoped series — never allow shared caches to reuse across users.
	c.Header("Cache-Control", "private, no-store")

	cfg, ok := parseOverviewTrafficWindow(strings.TrimSpace(c.Query("window")))
	if !ok {
		respondBadRequest(c, "不支持的时间窗口，允许值: 1h、24h、7d")
		return
	}

	localNow := h.nowFn()
	now := localNow.UTC()
	windowEnd := now.Truncate(cfg.Bucket)
	windowStart := windowEnd.Add(-cfg.Duration)

	ownedIDs, needFilter, ownErr := ownershipNodeFilter(c, h.db)
	if ownErr != nil {
		respondInternalError(c, ownErr)
		return
	}
	if needFilter && len(ownedIDs) == 0 {
		// Empty owned set → empty series for the window.
		points := make([]overviewTrafficPoint, 0, int(cfg.Duration/cfg.Bucket))
		for bucketStart := windowStart; bucketStart.Before(windowEnd); bucketStart = bucketStart.Add(cfg.Bucket) {
			localBucketStart := bucketStart.In(localNow.Location())
			points = append(points, overviewTrafficPoint{
				Timestamp:   localBucketStart.Format(time.RFC3339),
				TimestampMs: bucketStart.UnixMilli(),
				Label:       localBucketStart.Format(cfg.LabelLayout),
			})
		}
		respondOK(c, overviewTrafficResponse{
			Window:         cfg.Window,
			BucketMinutes:  int(cfg.Bucket / time.Minute),
			HasRealSamples: false,
			GeneratedAt:    localNow.Format(time.RFC3339),
			Points:         points,
		})
		return
	}

	// Load samples inside the window (SQL time bounds first). Cap + Truncated
	// avoids pretending missing buckets are true zeros under high volume.
	samples := make([]model.TaskTrafficSample, 0)
	truncated := false
	if h.db != nil {
		var raw []model.TaskTrafficSample
		q := h.db.Where("sampled_at >= ? AND sampled_at < ?", windowStart, windowEnd).
			Order("sampled_at asc").
			Limit(trafficSampleCap + 1)
		if needFilter {
			q = q.Where("node_id IN ?", ownedIDs)
		}
		if err := q.Find(&raw).Error; err != nil {
			respondInternalError(c, err)
			return
		}
		if len(raw) > trafficSampleCap {
			truncated = true
			raw = raw[:trafficSampleCap]
		}
		for _, sample := range raw {
			sampledAtUTC := sample.SampledAt.UTC()
			if sampledAtUTC.Before(windowStart) || !sampledAtUTC.Before(windowEnd) {
				continue
			}
			samples = append(samples, sample)
		}
	}

	buckets := make(map[time.Time]overviewTrafficBucket, len(samples))
	activityBuckets := make(map[time.Time]map[time.Time]map[uint]struct{}, len(samples))
	for _, sample := range samples {
		bucketStart := sample.SampledAt.UTC().Truncate(cfg.Bucket)
		minuteStart := sample.SampledAt.UTC().Truncate(time.Minute)
		current := buckets[bucketStart]
		if current.MinuteSums == nil {
			current.MinuteSums = make(map[time.Time]float64)
		}
		current.MinuteSums[minuteStart] += sample.ThroughputMbps
		buckets[bucketStart] = current

		sliceStart := sample.SampledAt.UTC().Truncate(10 * time.Second)
		bucketActivity := activityBuckets[bucketStart]
		if bucketActivity == nil {
			bucketActivity = make(map[time.Time]map[uint]struct{})
		}
		taskSet := bucketActivity[sliceStart]
		if taskSet == nil {
			taskSet = make(map[uint]struct{})
		}
		taskSet[sample.TaskID] = struct{}{}
		bucketActivity[sliceStart] = taskSet
		activityBuckets[bucketStart] = bucketActivity
	}

	startedCountByBucket := make(map[time.Time]int)
	failedCountByBucket := make(map[time.Time]int)
	if h.db != nil && h.db.Migrator().HasTable(&model.TaskRun{}) {
		// Scope task_runs via tasks.node_id for operators. Cap rows + Truncated
		// so high-volume windows cannot unbounded-load TaskRun into memory.
		var startedRuns []model.TaskRun
		startedQ := h.db.Model(&model.TaskRun{}).
			Select("id", "started_at").
			Where("started_at IS NOT NULL AND started_at >= ? AND started_at < ?", windowStart, windowEnd).
			Order("started_at asc").
			Limit(trafficTaskRunCap + 1)
		if needFilter {
			startedQ = startedQ.Where("task_id IN (SELECT id FROM tasks WHERE node_id IN ?)", ownedIDs)
		}
		if err := startedQ.Find(&startedRuns).Error; err != nil {
			respondInternalError(c, err)
			return
		}
		if len(startedRuns) > trafficTaskRunCap {
			truncated = true
			startedRuns = startedRuns[:trafficTaskRunCap]
		}
		for _, run := range startedRuns {
			if run.StartedAt != nil {
				startedCountByBucket[run.StartedAt.UTC().Truncate(cfg.Bucket)]++
			}
		}
		var failedRuns []model.TaskRun
		failedQ := h.db.Model(&model.TaskRun{}).
			Select("id", "finished_at").
			Where("status = ? AND finished_at IS NOT NULL AND finished_at >= ? AND finished_at < ?", "failed", windowStart, windowEnd).
			Order("finished_at asc").
			Limit(trafficTaskRunCap + 1)
		if needFilter {
			failedQ = failedQ.Where("task_id IN (SELECT id FROM tasks WHERE node_id IN ?)", ownedIDs)
		}
		if err := failedQ.Find(&failedRuns).Error; err != nil {
			respondInternalError(c, err)
			return
		}
		if len(failedRuns) > trafficTaskRunCap {
			truncated = true
			failedRuns = failedRuns[:trafficTaskRunCap]
		}
		for _, run := range failedRuns {
			if run.FinishedAt != nil {
				failedCountByBucket[run.FinishedAt.UTC().Truncate(cfg.Bucket)]++
			}
		}
	}

	points := make([]overviewTrafficPoint, 0, int(cfg.Duration/cfg.Bucket))
	for bucketStart := windowStart; bucketStart.Before(windowEnd); bucketStart = bucketStart.Add(cfg.Bucket) {
		bucket := buckets[bucketStart]
		throughput := 0.0
		sampleCount := len(bucket.MinuteSums)
		if sampleCount > 0 {
			totalThroughput := 0.0
			for _, minuteThroughput := range bucket.MinuteSums {
				totalThroughput += minuteThroughput
			}
			throughput = math.Round((totalThroughput/float64(sampleCount))*10) / 10 // average of per-minute total throughput within the bucket
		}
		activeTaskCount := 0
		for _, taskSet := range activityBuckets[bucketStart] {
			if len(taskSet) > activeTaskCount {
				activeTaskCount = len(taskSet)
			}
		}
		localBucketStart := bucketStart.In(localNow.Location())
		points = append(points, overviewTrafficPoint{
			Timestamp:       localBucketStart.Format(time.RFC3339),
			TimestampMs:     bucketStart.UnixMilli(),
			Label:           localBucketStart.Format(cfg.LabelLayout),
			ThroughputMbps:  throughput,
			SampleCount:     sampleCount,
			ActiveTaskCount: activeTaskCount,
			StartedCount:    startedCountByBucket[bucketStart],
			FailedCount:     failedCountByBucket[bucketStart],
		})
	}

	respondOK(c, overviewTrafficResponse{
		Window:         cfg.Window,
		BucketMinutes:  int(cfg.Bucket / time.Minute),
		HasRealSamples: len(samples) > 0,
		Truncated:      truncated,
		GeneratedAt:    localNow.Format(time.RFC3339),
		Points:         points,
	})
}

func parseOverviewTrafficWindow(raw string) (overviewTrafficWindowConfig, bool) {
	switch raw {
	case "", "1h":
		return overviewTrafficWindowConfig{Window: "1h", Duration: time.Hour, Bucket: 5 * time.Minute, LabelLayout: "15:04"}, true
	case "24h":
		return overviewTrafficWindowConfig{Window: "24h", Duration: 24 * time.Hour, Bucket: 30 * time.Minute, LabelLayout: "15:04"}, true
	case "7d":
		return overviewTrafficWindowConfig{Window: "7d", Duration: 7 * 24 * time.Hour, Bucket: 3 * time.Hour, LabelLayout: "01-02 15:04"}, true
	default:
		return overviewTrafficWindowConfig{}, false
	}
}
