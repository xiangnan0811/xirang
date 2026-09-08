package scheduler

import (
	"fmt"
	"sync"

	"github.com/robfig/cron/v3"
)

type CronScheduler struct {
	cron    *cron.Cron
	entries map[uint]cron.EntryID
	specs   map[uint]string
	mu      sync.Mutex
}

func NewCronScheduler() *CronScheduler {
	return &CronScheduler{
		cron:    cron.New(),
		entries: make(map[uint]cron.EntryID),
		specs:   make(map[uint]string),
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
		if spec != "" && s.specs != nil && s.specs[taskID] == spec {
			return nil
		}
		s.cron.Remove(oldID)
		delete(s.entries, taskID)
		delete(s.specs, taskID)
	}

	if spec == "" {
		return nil
	}

	entryID, err := s.cron.AddFunc(spec, fn)
	if err != nil {
		return fmt.Errorf("注册 cron 任务失败: %w", err)
	}
	if s.specs == nil {
		s.specs = make(map[uint]string)
	}
	s.entries[taskID] = entryID
	s.specs[taskID] = spec
	return nil
}

func (s *CronScheduler) RemoveTask(taskID uint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if oldID, ok := s.entries[taskID]; ok {
		s.cron.Remove(oldID)
		delete(s.entries, taskID)
		delete(s.specs, taskID)
	}
}

// RemoveTasksExcept removes scheduler entries that are not represented by the
// current durable schedule set. It is used during startup and periodic
// reconciliation to heal entries left behind by disabled or deleted tasks.
func (s *CronScheduler) RemoveTasksExcept(keep map[uint]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for taskID, entryID := range s.entries {
		if _, ok := keep[taskID]; ok {
			continue
		}
		s.cron.Remove(entryID)
		delete(s.entries, taskID)
		delete(s.specs, taskID)
	}
}

func (s *CronScheduler) HasTask(taskID uint) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.entries[taskID]
	return ok
}
