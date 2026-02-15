package scheduler

import (
	"fmt"
	"sync"

	"github.com/robfig/cron/v3"
)

type CronScheduler struct {
	cron    *cron.Cron
	entries map[uint]cron.EntryID
	mu      sync.Mutex
}

func NewCronScheduler() *CronScheduler {
	return &CronScheduler{
		cron:    cron.New(),
		entries: make(map[uint]cron.EntryID),
	}
}

func (s *CronScheduler) Start() {
	s.cron.Start()
}

func (s *CronScheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
}

func (s *CronScheduler) RegisterTask(taskID uint, spec string, fn func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if oldID, ok := s.entries[taskID]; ok {
		s.cron.Remove(oldID)
		delete(s.entries, taskID)
	}

	if spec == "" {
		return nil
	}

	entryID, err := s.cron.AddFunc(spec, fn)
	if err != nil {
		return fmt.Errorf("注册 cron 任务失败: %w", err)
	}
	s.entries[taskID] = entryID
	return nil
}

func (s *CronScheduler) RemoveTask(taskID uint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if oldID, ok := s.entries[taskID]; ok {
		s.cron.Remove(oldID)
		delete(s.entries, taskID)
	}
}
