package task

import (
	"context"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"
)

func (m *Manager) startSampleWorker() {
	ctx, cancel := context.WithCancel(context.Background())
	m.sampleWorkerCancel = cancel
	go m.runSampleWorker(ctx)
}

func (m *Manager) runSampleWorker(ctx context.Context) {
	defer close(m.sampleWorkerDone)

	ticker := time.NewTicker(m.sampleFlushInterval)
	defer ticker.Stop()

	batch := make([]queuedTaskSample, 0, m.sampleBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		m.persistSampleBatch(batch)
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case item := <-m.sampleQueue:
					batch = append(batch, item)
					if len(batch) >= m.sampleBatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case item := <-m.sampleQueue:
			batch = append(batch, item)
			if len(batch) >= m.sampleBatchSize {
				flush()
			}
		case <-ticker.C:
			m.cleanupExpiredTrafficSamples()
			flush()
		}
	}
}

func (m *Manager) emitTrafficSample(taskID uint, nodeID uint, runStartedAt time.Time, sample executor.ProgressSample) {
	if sample.ThroughputMbps <= 0 {
		return
	}
	sampledAt := sample.ObservedAt.UTC()
	if sampledAt.IsZero() {
		sampledAt = time.Now().UTC()
	}
	bucket := sampledAt.Truncate(defaultSampleThrottleWindow)
	if lastRaw, ok := m.lastSampleBucketByTask.Load(taskID); ok {
		if lastBucket, castOK := lastRaw.(time.Time); castOK && !bucket.After(lastBucket) {
			return
		}
	}
	m.lastSampleBucketByTask.Store(taskID, bucket)

	entry := queuedTaskSample{
		taskID:         taskID,
		nodeID:         nodeID,
		runStartedAt:   runStartedAt,
		sampledAt:      sampledAt,
		throughputMbps: sample.ThroughputMbps,
	}

	if m.sampleQueue == nil {
		m.persistSampleBatch([]queuedTaskSample{entry})
		return
	}

	select {
	case m.sampleQueue <- entry:
	default:
		logger.Module("task").Warn().Uint("task_id", taskID).Msg("task traffic sample queue full, dropping sample")
	}
}

func (m *Manager) persistSampleBatch(batch []queuedTaskSample) {
	if len(batch) == 0 || m.db == nil {
		return
	}
	m.cleanupExpiredTrafficSamples()

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

	if err := m.db.CreateInBatches(&records, m.sampleBatchSize).Error; err != nil {
		logger.Module("task").Warn().Err(err).Msg("批量写入吞吐采样失败，回退单条写入")
		for i := range records {
			if oneErr := m.db.Create(&records[i]).Error; oneErr != nil {
				logger.Module("task").Error().Uint("task_id", records[i].TaskID).Int("batch_index", i).Err(oneErr).Msg("写入吞吐采样失败")
			}
		}
	}
}

func (m *Manager) cleanupExpiredTrafficSamples() {
	if m.sampleRetentionDays <= 0 || m.db == nil {
		return
	}

	m.sampleCleanupMu.Lock()
	defer m.sampleCleanupMu.Unlock()

	now := time.Now().UTC()
	if !m.lastSampleCleanupAt.IsZero() && now.Sub(m.lastSampleCleanupAt) < defaultSampleCleanupInterval {
		return
	}

	cutoff := now.AddDate(0, 0, -m.sampleRetentionDays)
	for {
		var ids []uint
		if err := m.db.Model(&model.TaskTrafficSample{}).Where("sampled_at < ?", cutoff).Order("id asc").Limit(defaultSampleCleanupBatchSize).Pluck("id", &ids).Error; err != nil {
			logger.Module("task").Warn().Err(err).Msg("查询过期吞吐采样失败")
			return
		}
		if len(ids) == 0 {
			break
		}
		if err := m.db.Where("id IN ?", ids).Delete(&model.TaskTrafficSample{}).Error; err != nil {
			logger.Module("task").Warn().Err(err).Msg("清理过期吞吐采样失败")
			return
		}
		if len(ids) < defaultSampleCleanupBatchSize {
			break
		}
	}
	m.lastSampleCleanupAt = now
}

func (m *Manager) cleanupExpiredTaskRuns() {
	if m.taskRunRetentionDays <= 0 || m.db == nil {
		return
	}

	m.taskRunCleanupMu.Lock()
	defer m.taskRunCleanupMu.Unlock()

	now := time.Now().UTC()
	if !m.lastTaskRunCleanupAt.IsZero() && now.Sub(m.lastTaskRunCleanupAt) < defaultSampleCleanupInterval {
		return
	}

	cutoff := now.AddDate(0, 0, -m.taskRunRetentionDays)
	for {
		var ids []uint
		if err := m.db.Model(&model.TaskRun{}).Where("created_at < ?", cutoff).Order("id asc").Limit(defaultSampleCleanupBatchSize).Pluck("id", &ids).Error; err != nil {
			logger.Module("task").Warn().Err(err).Msg("查询过期执行记录失败")
			return
		}
		if len(ids) == 0 {
			break
		}
		// 级联清理：删除关联 TaskLog，清除关联 Alert 的 run 引用
		if err := m.db.Where("task_run_id IN ?", ids).Delete(&model.TaskLog{}).Error; err != nil {
			logger.Module("task").Warn().Err(err).Msg("清理过期执行记录关联日志失败")
			return
		}
		if err := m.db.Model(&model.Alert{}).Where("task_run_id IN ?", ids).Update("task_run_id", nil).Error; err != nil {
			logger.Module("task").Warn().Err(err).Msg("清除过期执行记录关联告警引用失败")
			return
		}
		if err := m.db.Where("id IN ?", ids).Delete(&model.TaskRun{}).Error; err != nil {
			logger.Module("task").Warn().Err(err).Msg("清理过期执行记录失败")
			return
		}
		if len(ids) < defaultSampleCleanupBatchSize {
			break
		}
	}
	m.lastTaskRunCleanupAt = now
}
