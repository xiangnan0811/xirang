package scheduler

import (
	"fmt"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

type CronScheduler struct {
	cron    *cron.Cron
	entries map[uint]cron.EntryID
	specs   map[uint]string
	mu      sync.Mutex
}

// occurrenceSchedule mirrors the schedule passed to robfig/cron while
// retaining each canonical activation returned by Next. Cron's Job interface
// has no timestamp argument; this small adapter carries that value to the
// callback without deriving it from callback wall-clock time.
type occurrenceSchedule struct {
	cron.Schedule
	mu      sync.Mutex
	pending []time.Time
}

func (s *occurrenceSchedule) Next(after time.Time) time.Time {
	next := s.Schedule.Next(after)
	if next.IsZero() {
		return next
	}
	s.mu.Lock()
	s.pending = append(s.pending, next)
	s.mu.Unlock()
	return next
}

func (s *occurrenceSchedule) take() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return time.Time{}, false
	}
	next := s.pending[0]
	copy(s.pending, s.pending[1:])
	s.pending = s.pending[:len(s.pending)-1]
	return next, true
}

type occurrenceJob struct {
	schedule *occurrenceSchedule
	callback func(time.Time)
}

func (j occurrenceJob) Run() {
	scheduledAt, ok := j.schedule.take()
	if !ok {
		// Never invent an occurrence timestamp. A job invocation without the
		// schedule's Next result is not safe to persist as a cron run.
		return
	}
	j.callback(scheduledAt)
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

// RegisterTask accepts the timestamp-aware callback used by the manager.
func (s *CronScheduler) RegisterTask(taskID uint, spec string, fn func(time.Time)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if fn == nil {
		return fmt.Errorf("cron callback is nil")
	}

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

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	schedule, err := parser.Parse(spec)
	if err != nil {
		return fmt.Errorf("注册 cron 任务失败: %w", err)
	}
	tracked := &occurrenceSchedule{Schedule: schedule}
	entryID := s.cron.Schedule(tracked, occurrenceJob{schedule: tracked, callback: fn})
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
