package task

import (
	"time"

	"xirang/backend/internal/model"
)

const defaultProgressThrottleWindow = 3 * time.Second

func (m *Manager) emitProgress(taskID uint, runID uint, percent int) {
	if percent <= 0 || percent > 100 {
		return
	}
	now := time.Now().UTC()
	bucket := now.Truncate(defaultProgressThrottleWindow)
	if lastRaw, ok := m.lastProgressBucketByTask.Load(taskID); ok {
		if lastBucket, castOK := lastRaw.(time.Time); castOK && !bucket.After(lastBucket) {
			return
		}
	}
	m.lastProgressBucketByTask.Store(taskID, bucket)
	m.db.Model(&model.TaskRun{}).Where("id = ?", runID).Update("progress", percent)
}
