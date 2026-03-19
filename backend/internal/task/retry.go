package task

import "time"

func (m *Manager) storeRetryTimer(taskID uint, timer *time.Timer) {
	if timer == nil {
		return
	}
	if oldRaw, ok := m.retryTimers.Load(taskID); ok {
		if oldTimer, castOK := oldRaw.(*time.Timer); castOK {
			oldTimer.Stop()
		}
	}
	m.retryTimers.Store(taskID, timer)
}

func (m *Manager) stopRetryTimer(taskID uint) {
	if timerRaw, ok := m.retryTimers.LoadAndDelete(taskID); ok {
		if timer, castOK := timerRaw.(*time.Timer); castOK {
			timer.Stop()
		}
	}
}

func (m *Manager) stopAllRetryTimers() {
	m.retryTimers.Range(func(key, value interface{}) bool {
		if timer, ok := value.(*time.Timer); ok {
			timer.Stop()
		}
		m.retryTimers.Delete(key)
		return true
	})
}
