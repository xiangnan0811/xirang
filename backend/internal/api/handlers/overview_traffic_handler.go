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

type taskSampleAccum struct {
	sum   float64
	count int
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
	Partial         bool    `json:"partial,omitempty"`
	// Incomplete indicates that the bucket may be missing rows due to a query cap.
	Incomplete bool `json:"incomplete,omitempty"`
}

type overviewTrafficResponse struct {
	Window         string `json:"window"`
	BucketMinutes  int    `json:"bucket_minutes"`
	HasRealSamples bool   `json:"has_real_samples"`
	// Truncated is true when sample or task-run rows exceed the server-side cap.
	Truncated   bool                   `json:"truncated,omitempty"`
	GeneratedAt string                 `json:"generated_at"`
	Points      []overviewTrafficPoint `json:"points"`
}

// trafficSampleCap bounds samples loaded for one window. Exceeding it sets Truncated.
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
// @Description  返回指定时间窗口（1h/24h/7d）内的任务吞吐量与活动趋势数据；partial 表示进行中的末尾桶，incomplete 表示可能因截断而缺少数据的桶
// @Tags         overview
// @Security     Bearer
// @Produce      json
// @Param        window  query     string  false  "时间窗口（1h/24h/7d，默认 1h）"
// @Success      200  {object}  handlers.Response{data=overviewTrafficResponse}
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
	currentBucketStart := now.Truncate(cfg.Bucket)
	windowStart := currentBucketStart.Add(-cfg.Duration)
	hasPartial := now.After(currentBucketStart)
	queryEnd := currentBucketStart
	bucketLoopEnd := currentBucketStart
	if hasPartial {
		queryEnd = now
		bucketLoopEnd = currentBucketStart.Add(cfg.Bucket)
	}

	ownedIDs, needFilter, ownErr := ownershipNodeFilter(c, h.db)
	if ownErr != nil {
		respondInternalError(c, ownErr)
		return
	}
	if needFilter && len(ownedIDs) == 0 {
		// Empty owned set → empty series for the window (including trailing partial bucket if active).
		points := buildOverviewTrafficPoints(
			windowStart, bucketLoopEnd, currentBucketStart, hasPartial, cfg, localNow.Location(),
			nil, nil, nil,
		)
		respondOK(c, overviewTrafficResponse{
			Window:         cfg.Window,
			BucketMinutes:  int(cfg.Bucket / time.Minute),
			HasRealSamples: false,
			GeneratedAt:    localNow.Format(time.RFC3339),
			Points:         points,
		})
		return
	}

	ctx := c.Request.Context()
	// Load the newest rows in each bounded query so recent buckets remain usable.
	samples := make([]model.TaskTrafficSample, 0)
	truncated := false
	var incompleteThrough time.Time
	markTruncated := func(excludedAt time.Time) {
		bucket := excludedAt.UTC().Truncate(cfg.Bucket)
		if !truncated || bucket.After(incompleteThrough) {
			incompleteThrough = bucket
		}
		truncated = true
	}
	if h.db != nil {
		var raw []model.TaskTrafficSample
		q := applyTrafficTimeWindow(h.db.WithContext(ctx), "sampled_at", windowStart, queryEnd, hasPartial).
			Order("sampled_at desc").
			Limit(trafficSampleCap + 1)
		if needFilter {
			q = q.Where("node_id IN ?", ownedIDs)
		}
		if err := q.Find(&raw).Error; err != nil {
			respondInternalError(c, err)
			return
		}
		if len(raw) > trafficSampleCap {
			markTruncated(raw[trafficSampleCap].SampledAt)
			raw = raw[:trafficSampleCap]
		}
		for _, sample := range raw {
			sampledAtUTC := sample.SampledAt.UTC()
			if !inTrafficTimeWindow(sampledAtUTC, windowStart, queryEnd, hasPartial) {
				continue
			}
			samples = append(samples, sample)
		}
	}

	// Aggregate per 10-second slice (matching SampleWriter throttle window) and
	// distinct task ID so a single task emitting 6 samples/min is not summed 6x.
	slicesByBucket := make(map[time.Time]map[time.Time]map[uint]taskSampleAccum, len(samples))
	for _, sample := range samples {
		sampledAtUTC := sample.SampledAt.UTC()
		bucketStart := sampledAtUTC.Truncate(cfg.Bucket)
		sliceStart := sampledAtUTC.Truncate(10 * time.Second)
		bucketSlices := slicesByBucket[bucketStart]
		if bucketSlices == nil {
			bucketSlices = make(map[time.Time]map[uint]taskSampleAccum)
			slicesByBucket[bucketStart] = bucketSlices
		}
		taskMap := bucketSlices[sliceStart]
		if taskMap == nil {
			taskMap = make(map[uint]taskSampleAccum)
			bucketSlices[sliceStart] = taskMap
		}
		acc := taskMap[sample.TaskID]
		acc.sum += sample.ThroughputMbps
		acc.count++
		taskMap[sample.TaskID] = acc
	}

	startedCountByBucket := make(map[time.Time]int)
	failedCountByBucket := make(map[time.Time]int)
	hasRuns, err := hasTaskRunTable(ctx, h.db)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	if hasRuns {
		// Scope task_runs via immutable node_id_snapshot for operators so task
		// migrations never reassign historical events to another operator.
		var startedRuns []model.TaskRun
		startedQ := applyTrafficTimeWindow(
			h.db.WithContext(ctx).Model(&model.TaskRun{}).Select("id", "started_at").Where("started_at IS NOT NULL"),
			"started_at", windowStart, queryEnd, hasPartial,
		).Order("started_at desc").Limit(trafficTaskRunCap + 1)
		if needFilter {
			startedQ = startedQ.Where("node_id_snapshot IN ?", ownedIDs)
		}
		if err := startedQ.Find(&startedRuns).Error; err != nil {
			respondInternalError(c, err)
			return
		}
		if len(startedRuns) > trafficTaskRunCap {
			markTruncated(*startedRuns[trafficTaskRunCap].StartedAt)
			startedRuns = startedRuns[:trafficTaskRunCap]
		}
		for _, run := range startedRuns {
			if run.StartedAt != nil && inTrafficTimeWindow(run.StartedAt.UTC(), windowStart, queryEnd, hasPartial) {
				startedCountByBucket[run.StartedAt.UTC().Truncate(cfg.Bucket)]++
			}
		}

		var failedRuns []model.TaskRun
		failedQ := applyTrafficTimeWindow(
			h.db.WithContext(ctx).Model(&model.TaskRun{}).Select("id", "finished_at").Where("status = ? AND finished_at IS NOT NULL", "failed"),
			"finished_at", windowStart, queryEnd, hasPartial,
		).Order("finished_at desc").Limit(trafficTaskRunCap + 1)
		if needFilter {
			failedQ = failedQ.Where("node_id_snapshot IN ?", ownedIDs)
		}
		if err := failedQ.Find(&failedRuns).Error; err != nil {
			respondInternalError(c, err)
			return
		}
		if len(failedRuns) > trafficTaskRunCap {
			markTruncated(*failedRuns[trafficTaskRunCap].FinishedAt)
			failedRuns = failedRuns[:trafficTaskRunCap]
		}
		for _, run := range failedRuns {
			if run.FinishedAt != nil && inTrafficTimeWindow(run.FinishedAt.UTC(), windowStart, queryEnd, hasPartial) {
				failedCountByBucket[run.FinishedAt.UTC().Truncate(cfg.Bucket)]++
			}
		}
	}

	points := buildOverviewTrafficPoints(
		windowStart, bucketLoopEnd, currentBucketStart, hasPartial, cfg, localNow.Location(),
		slicesByBucket, startedCountByBucket, failedCountByBucket,
	)
	if truncated {
		cutoffMs := incompleteThrough.UnixMilli()
		for i := range points {
			points[i].Incomplete = points[i].TimestampMs <= cutoffMs
		}
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

func applyTrafficTimeWindow(q *gorm.DB, column string, start, end time.Time, inclusiveEnd bool) *gorm.DB {
	if inclusiveEnd {
		return q.Where(column+" >= ? AND "+column+" <= ?", start, end)
	}
	return q.Where(column+" >= ? AND "+column+" < ?", start, end)
}

func inTrafficTimeWindow(ts, start, end time.Time, inclusiveEnd bool) bool {
	if ts.Before(start) {
		return false
	}
	if inclusiveEnd {
		return !ts.After(end)
	}
	return ts.Before(end)
}

func buildOverviewTrafficPoints(
	windowStart, bucketLoopEnd, currentBucketStart time.Time,
	hasPartial bool,
	cfg overviewTrafficWindowConfig,
	loc *time.Location,
	slicesByBucket map[time.Time]map[time.Time]map[uint]taskSampleAccum,
	startedCountByBucket map[time.Time]int,
	failedCountByBucket map[time.Time]int,
) []overviewTrafficPoint {
	capHint := int(cfg.Duration / cfg.Bucket)
	if hasPartial {
		capHint++
	}
	points := make([]overviewTrafficPoint, 0, capHint)
	for bucketStart := windowStart; bucketStart.Before(bucketLoopEnd); bucketStart = bucketStart.Add(cfg.Bucket) {
		slices := slicesByBucket[bucketStart]
		throughput := 0.0
		sliceCount := len(slices)
		activeMinutes := make(map[time.Time]struct{}, sliceCount)
		activeTaskCount := 0
		if sliceCount > 0 {
			totalSliceThroughput := 0.0
			for sliceStart, taskMap := range slices {
				activeMinutes[sliceStart.Truncate(time.Minute)] = struct{}{}
				if len(taskMap) > activeTaskCount {
					activeTaskCount = len(taskMap)
				}
				sliceThroughput := 0.0
				for _, acc := range taskMap {
					sliceThroughput += acc.sum / float64(acc.count)
				}
				totalSliceThroughput += sliceThroughput
			}
			throughput = math.Round((totalSliceThroughput/float64(sliceCount))*10) / 10
		}
		localBucketStart := bucketStart.In(loc)
		points = append(points, overviewTrafficPoint{
			Timestamp:       localBucketStart.Format(time.RFC3339),
			TimestampMs:     bucketStart.UnixMilli(),
			Label:           localBucketStart.Format(cfg.LabelLayout),
			ThroughputMbps:  throughput,
			SampleCount:     len(activeMinutes),
			ActiveTaskCount: activeTaskCount,
			StartedCount:    startedCountByBucket[bucketStart],
			FailedCount:     failedCountByBucket[bucketStart],
			Partial:         hasPartial && bucketStart.Equal(currentBucketStart),
		})
	}
	return points
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
