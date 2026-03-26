package handlers

import (
	"math"
	"net/http"
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
	Window         string                 `json:"window"`
	BucketMinutes  int                    `json:"bucket_minutes"`
	HasRealSamples bool                   `json:"has_real_samples"`
	GeneratedAt    string                 `json:"generated_at"`
	Points         []overviewTrafficPoint `json:"points"`
}

func NewOverviewTrafficHandler(db *gorm.DB, nowFn func() time.Time) *OverviewTrafficHandler {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &OverviewTrafficHandler{db: db, nowFn: nowFn}
}

func (h *OverviewTrafficHandler) Get(c *gin.Context) {
	cfg, ok := parseOverviewTrafficWindow(strings.TrimSpace(c.Query("window")))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的时间窗口，允许值: 1h、24h、7d"})
		return
	}

	localNow := h.nowFn()
	now := localNow.UTC()
	windowEnd := now.Truncate(cfg.Bucket)
	windowStart := windowEnd.Add(-cfg.Duration)

	allSamples := make([]model.TaskTrafficSample, 0)
	if h.db != nil {
		if err := h.db.Order("sampled_at asc").Limit(10000).Find(&allSamples).Error; err != nil {
			respondInternalError(c, err)
			return
		}
	}
	samples := make([]model.TaskTrafficSample, 0, len(allSamples))
	for _, sample := range allSamples {
		sampledAtUTC := sample.SampledAt.UTC()
		if sampledAtUTC.Before(windowStart) || !sampledAtUTC.Before(windowEnd) {
			continue
		}
		samples = append(samples, sample)
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
		// 已开始的执行：按 started_at 聚合（排除 pending/skipped 等未真正执行的记录）
		var startedRuns []model.TaskRun
		if err := h.db.Where("started_at IS NOT NULL AND started_at >= ? AND started_at < ?", windowStart, windowEnd).
			Order("started_at asc").Limit(10000).Find(&startedRuns).Error; err == nil {
			for _, run := range startedRuns {
				if run.StartedAt != nil {
					startedCountByBucket[run.StartedAt.UTC().Truncate(cfg.Bucket)]++
				}
			}
		}
		// 失败的执行：按 finished_at 聚合（记录在失败发生的时刻，而非创建时刻）
		var failedRuns []model.TaskRun
		if err := h.db.Where("status = ? AND finished_at IS NOT NULL AND finished_at >= ? AND finished_at < ?", "failed", windowStart, windowEnd).
			Order("finished_at asc").Limit(10000).Find(&failedRuns).Error; err == nil {
			for _, run := range failedRuns {
				if run.FinishedAt != nil {
					failedCountByBucket[run.FinishedAt.UTC().Truncate(cfg.Bucket)]++
				}
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

	c.JSON(http.StatusOK, gin.H{"data": overviewTrafficResponse{
		Window:         cfg.Window,
		BucketMinutes:  int(cfg.Bucket / time.Minute),
		HasRealSamples: len(samples) > 0,
		GeneratedAt:    localNow.Format(time.RFC3339),
		Points:         points,
	}})
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
