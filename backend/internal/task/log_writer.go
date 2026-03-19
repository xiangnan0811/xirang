package task

import (
	"context"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/ws"
)

func (m *Manager) startLogWorker() {
	ctx, cancel := context.WithCancel(context.Background())
	m.logWorkerCancel = cancel
	go m.runLogWorker(ctx)
}

func (m *Manager) runLogWorker(ctx context.Context) {
	defer close(m.logWorkerDone)

	ticker := time.NewTicker(m.logFlushInterval)
	defer ticker.Stop()

	batch := make([]queuedTaskLog, 0, m.logBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		m.persistLogBatch(batch)
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case item := <-m.logQueue:
					batch = append(batch, item)
					if len(batch) >= m.logBatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case item := <-m.logQueue:
			batch = append(batch, item)
			if len(batch) >= m.logBatchSize {
				flush()
			}
		case <-ticker.C:
			m.cleanupExpiredTaskRuns()
			flush()
		}
	}
}

func (m *Manager) emitLog(taskID uint, runID *uint, level, message, status string) {
	entry := queuedTaskLog{
		taskID:    taskID,
		taskRunID: runID,
		level:     level,
		message:   message,
		status:    status,
	}

	if m.logQueue == nil {
		m.persistLogBatch([]queuedTaskLog{entry})
		return
	}

	select {
	case m.logQueue <- entry:
	default:
		logger.Module("task").Warn().Uint("task_id", taskID).Msg("task log queue full, fallback to direct write")
		m.persistLogBatch([]queuedTaskLog{entry})
	}
}

func (m *Manager) persistLogBatch(batch []queuedTaskLog) {
	if len(batch) == 0 || m.db == nil {
		return
	}

	records := make([]model.TaskLog, 0, len(batch))
	for _, item := range batch {
		records = append(records, model.TaskLog{
			TaskID:    item.taskID,
			TaskRunID: item.taskRunID,
			Level:     item.level,
			Message:   item.message,
		})
	}

	if err := m.db.CreateInBatches(&records, m.logBatchSize).Error; err != nil {
		logger.Module("task").Warn().Err(err).Msg("批量写入任务日志失败，回退单条写入")
		for i, item := range batch {
			record := model.TaskLog{
				TaskID:    item.taskID,
				TaskRunID: item.taskRunID,
				Level:     item.level,
				Message:   item.message,
			}
			if oneErr := m.db.Create(&record).Error; oneErr != nil {
				logger.Module("task").Error().Uint("task_id", item.taskID).Int("batch_index", i).Err(oneErr).Msg("写入任务日志失败")
				continue
			}
			m.publishLogEvent(record, item.status)
		}
		return
	}

	for i := range records {
		m.publishLogEvent(records[i], batch[i].status)
	}
}

func (m *Manager) publishLogEvent(record model.TaskLog, status string) {
	if m.hub == nil {
		return
	}
	m.hub.Publish(ws.LogEvent{
		LogID:     record.ID,
		TaskID:    record.TaskID,
		TaskRunID: record.TaskRunID,
		Level:     record.Level,
		Message:   record.Message,
		Status:    status,
		Timestamp: record.CreatedAt,
	})
}
