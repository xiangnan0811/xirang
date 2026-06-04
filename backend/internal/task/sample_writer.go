package task

import (
	"context"
	"sync"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"

	"gorm.io/gorm"
)

const defaultProgressThrottleWindow = 3 * time.Second

// SampleWriter batches and persists task traffic samples and progress updates
// asynchronously. It owns the sample queue, worker goroutine, throttle state,
// and expired-sample cleanup logic.
type SampleWriter struct {
	db                     *gorm.DB
	queue                  chan queuedTaskSample
	batchSize              int
	flushInterval          time.Duration
	cancel                 context.CancelFunc
	done                   chan struct{}
	lastSampleBucketByTask   sync.Map
	lastProgressBucketByTask sync.Map
	sampleRetentionDays    int
	lastSampleCleanupAt    time.Time
	sampleCleanupMu        sync.Mutex
}

// NewSampleWriter creates a SampleWriter. sampleRetentionDays controls how
// long traffic samples are kept before cleanup; <=0 disables cleanup.
func NewSampleWriter(db *gorm.DB, sampleRetentionDays int) *SampleWriter {
	return &SampleWriter{
		db:                  db,
		queue:               make(chan queuedTaskSample, defaultSampleQueueCapacity),
		batchSize:           defaultSampleBatchSize,
		flushInterval:       defaultSampleFlushInterval,
		done:                make(chan struct{}),
		sampleRetentionDays: sampleRetentionDays,
	}
}

// Start begins the sample worker goroutine.
func (sw *SampleWriter) Start(parentCtx context.Context) {
	ctx, cancel := context.WithCancel(parentCtx)
	sw.cancel = cancel
	go sw.run(ctx)
}

// Write enqueues a traffic sample for asynchronous persistence.
func (sw *SampleWriter) Write(taskID uint, nodeID uint, runStartedAt time.Time, sample executor.ProgressSample) {
	if sample.ThroughputMbps <= 0 {
		return
	}
	sampledAt := sample.ObservedAt.UTC()
	if sampledAt.IsZero() {
		sampledAt = time.Now().UTC()
	}
	bucket := sampledAt.Truncate(defaultSampleThrottleWindow)
	if lastRaw, ok := sw.lastSampleBucketByTask.Load(taskID); ok {
		if lastBucket, castOK := lastRaw.(time.Time); castOK && !bucket.After(lastBucket) {
			return
		}
	}
	sw.lastSampleBucketByTask.Store(taskID, bucket)

	entry := queuedTaskSample{
		taskID:         taskID,
		nodeID:         nodeID,
		runStartedAt:   runStartedAt,
		sampledAt:      sampledAt,
		throughputMbps: sample.ThroughputMbps,
	}

	if sw.queue == nil {
		sw.persistBatch([]queuedTaskSample{entry})
		return
	}

	select {
	case sw.queue <- entry:
	default:
		logger.Module("task").Warn().Uint("task_id", taskID).Msg("task traffic sample queue full, dropping sample")
	}
}

// WriteProgress throttles and persists a task progress update.
func (sw *SampleWriter) WriteProgress(taskID uint, runID uint, percent int) {
	if percent <= 0 || percent > 100 {
		return
	}
	now := time.Now().UTC()
	bucket := now.Truncate(defaultProgressThrottleWindow)
	if lastRaw, ok := sw.lastProgressBucketByTask.Load(taskID); ok {
		if lastBucket, castOK := lastRaw.(time.Time); castOK && !bucket.After(lastBucket) {
			return
		}
	}
	sw.lastProgressBucketByTask.Store(taskID, bucket)
	sw.db.Model(&model.TaskRun{}).Where("id = ?", runID).Update("progress", percent)
}

// ResetThrottle clears throttling state for a task (call when a new run starts).
func (sw *SampleWriter) ResetThrottle(taskID uint) {
	sw.lastSampleBucketByTask.Delete(taskID)
	sw.lastProgressBucketByTask.Delete(taskID)
}

// Stop cancels the sample worker and waits for it to drain.
func (sw *SampleWriter) Stop(ctx context.Context) error {
	if sw.cancel != nil {
		sw.cancel()
	}
	if sw.done != nil {
		select {
		case <-sw.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (sw *SampleWriter) run(ctx context.Context) {
	defer close(sw.done)

	ticker := time.NewTicker(sw.flushInterval)
	defer ticker.Stop()

	batch := make([]queuedTaskSample, 0, sw.batchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		sw.persistBatch(batch)
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case item := <-sw.queue:
					batch = append(batch, item)
					if len(batch) >= sw.batchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case item := <-sw.queue:
			batch = append(batch, item)
			if len(batch) >= sw.batchSize {
				flush()
			}
		case <-ticker.C:
			sw.cleanupExpired()
			flush()
		}
	}
}

func (sw *SampleWriter) persistBatch(batch []queuedTaskSample) {
	if len(batch) == 0 || sw.db == nil {
		return
	}
	sw.cleanupExpired()

	records := make([]model.TaskTrafficSample, 0, len(batch))
	for _, item := range batch {
		records = append(records, model.TaskTrafficSample{
			TaskID:         item.taskID,
			NodeID:         item.nodeID,
			RunStartedAt:   item.runStartedAt,
			SampledAt:      item.sampledAt,
			ThroughputMbps: item.throughputMbps,
		})
	}

	if err := sw.db.CreateInBatches(&records, sw.batchSize).Error; err != nil {
		logger.Module("task").Warn().Err(err).Msg("批量写入吞吐采样失败，回退单条写入")
		for i := range records {
			if oneErr := sw.db.Create(&records[i]).Error; oneErr != nil {
				logger.Module("task").Error().Uint("task_id", records[i].TaskID).Int("batch_index", i).Err(oneErr).Msg("写入吞吐采样失败")
			}
		}
	}
}

func (sw *SampleWriter) cleanupExpired() {
	if sw.sampleRetentionDays <= 0 || sw.db == nil {
		return
	}

	sw.sampleCleanupMu.Lock()
	defer sw.sampleCleanupMu.Unlock()

	now := time.Now().UTC()
	if !sw.lastSampleCleanupAt.IsZero() && now.Sub(sw.lastSampleCleanupAt) < defaultSampleCleanupInterval {
		return
	}

	cutoff := now.AddDate(0, 0, -sw.sampleRetentionDays)
	for {
		var ids []uint
		if err := sw.db.Model(&model.TaskTrafficSample{}).Where("sampled_at < ?", cutoff).Order("id asc").Limit(defaultSampleCleanupBatchSize).Pluck("id", &ids).Error; err != nil {
			logger.Module("task").Warn().Err(err).Msg("查询过期吞吐采样失败")
			return
		}
		if len(ids) == 0 {
			break
		}
		if err := sw.db.Where("id IN ?", ids).Delete(&model.TaskTrafficSample{}).Error; err != nil {
			logger.Module("task").Warn().Err(err).Msg("清理过期吞吐采样失败")
			return
		}
		if len(ids) < defaultSampleCleanupBatchSize {
			break
		}
	}
	sw.lastSampleCleanupAt = now
}
