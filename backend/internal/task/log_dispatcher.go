package task

import (
	"context"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/ws"

	"gorm.io/gorm"
)

// LogDispatcher batches and persists task execution logs asynchronously.
// It owns the log queue, worker goroutine, and batch-persistence logic.
type LogDispatcher struct {
	db            *gorm.DB
	hub           *ws.Hub
	queue         chan queuedTaskLog
	batchSize     int
	flushInterval time.Duration
	cancel        context.CancelFunc
	done          chan struct{}
}

// NewLogDispatcher creates a LogDispatcher backed by db and hub.
// Call Start to begin the worker goroutine.
func NewLogDispatcher(db *gorm.DB, hub *ws.Hub) *LogDispatcher {
	return &LogDispatcher{
		db:            db,
		hub:           hub,
		queue:         make(chan queuedTaskLog, defaultLogQueueCapacity),
		batchSize:     defaultLogBatchSize,
		flushInterval: defaultLogFlushInterval,
		done:          make(chan struct{}),
	}
}

// Start begins the log worker goroutine. cleanupFunc is called on each tick
// (e.g., to clean up expired task runs).
func (ld *LogDispatcher) Start(parentCtx context.Context, cleanupFunc func()) {
	ctx, cancel := context.WithCancel(parentCtx)
	ld.cancel = cancel
	go ld.run(ctx, cleanupFunc)
}

// Dispatch enqueues a task log entry for asynchronous persistence.
func (ld *LogDispatcher) Dispatch(taskID uint, runID *uint, level, message, status string) {
	entry := queuedTaskLog{
		taskID:    taskID,
		taskRunID: runID,
		level:     level,
		message:   sanitizeTaskLogMessage(message),
		status:    status,
	}

	if ld.queue == nil {
		ld.persistBatch([]queuedTaskLog{entry})
		return
	}

	select {
	case ld.queue <- entry:
	default:
		logger.Module("task").Warn().Uint("task_id", taskID).Msg("task log queue full, fallback to direct write")
		ld.persistBatch([]queuedTaskLog{entry})
	}
}

// Stop cancels the log worker and waits for it to drain the queue.
func (ld *LogDispatcher) Stop(ctx context.Context) error {
	if ld.cancel != nil {
		ld.cancel()
	}
	if ld.done != nil {
		select {
		case <-ld.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (ld *LogDispatcher) run(ctx context.Context, cleanupFunc func()) {
	defer close(ld.done)

	ticker := time.NewTicker(ld.flushInterval)
	defer ticker.Stop()

	batch := make([]queuedTaskLog, 0, ld.batchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ld.persistBatch(batch)
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case item := <-ld.queue:
					batch = append(batch, item)
					if len(batch) >= ld.batchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case item := <-ld.queue:
			batch = append(batch, item)
			if len(batch) >= ld.batchSize {
				flush()
			}
		case <-ticker.C:
			if cleanupFunc != nil {
				cleanupFunc()
			}
			flush()
		}
	}
}

func (ld *LogDispatcher) persistBatch(batch []queuedTaskLog) {
	if len(batch) == 0 || ld.db == nil {
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

	if err := ld.db.CreateInBatches(&records, ld.batchSize).Error; err != nil {
		logger.Module("task").Warn().Err(err).Msg("批量写入任务日志失败，回退单条写入")
		for i, item := range batch {
			record := model.TaskLog{
				TaskID:    item.taskID,
				TaskRunID: item.taskRunID,
				Level:     item.level,
				Message:   item.message,
			}
			if oneErr := ld.db.Create(&record).Error; oneErr != nil {
				logger.Module("task").Error().Uint("task_id", item.taskID).Int("batch_index", i).Err(oneErr).Msg("写入任务日志失败")
				continue
			}
			ld.publishEvent(record, item.status)
		}
		return
	}

	for i := range records {
		ld.publishEvent(records[i], batch[i].status)
	}
}

func (ld *LogDispatcher) publishEvent(record model.TaskLog, status string) {
	if ld.hub == nil {
		return
	}
	ld.hub.Publish(ws.LogEvent{
		LogID:     record.ID,
		TaskID:    record.TaskID,
		TaskRunID: record.TaskRunID,
		Level:     record.Level,
		Message:   record.Message,
		Status:    status,
		Timestamp: record.CreatedAt,
	})
}
