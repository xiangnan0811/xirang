package scheduler

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestRegisterTask_HappyPath(t *testing.T) {
	s := NewCronScheduler()
	if err := s.RegisterTask(1, "@every 1m", func() {}); err != nil {
		t.Fatalf("RegisterTask: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[1]; !ok {
		t.Fatal("entry for taskID=1 not stored")
	}
}

func TestRegisterTask_ReplacesExisting(t *testing.T) {
	s := NewCronScheduler()
	if err := s.RegisterTask(7, "@every 1m", func() {}); err != nil {
		t.Fatalf("first RegisterTask: %v", err)
	}
	s.mu.Lock()
	firstID := s.entries[7]
	s.mu.Unlock()

	if err := s.RegisterTask(7, "@every 5m", func() {}); err != nil {
		t.Fatalf("second RegisterTask: %v", err)
	}
	s.mu.Lock()
	secondID := s.entries[7]
	s.mu.Unlock()
	if secondID == firstID {
		t.Fatalf("entry id should change after re-register, both = %v", firstID)
	}
}

func TestRegisterTask_EmptySpecIsNoop(t *testing.T) {
	s := NewCronScheduler()
	if err := s.RegisterTask(99, "", func() {}); err != nil {
		t.Fatalf("empty spec should not error, got %v", err)
	}
	s.mu.Lock()
	_, ok := s.entries[99]
	s.mu.Unlock()
	if ok {
		t.Fatal("empty spec should not add an entry")
	}
}

func TestRegisterTask_InvalidCronReturnsError(t *testing.T) {
	s := NewCronScheduler()
	err := s.RegisterTask(42, "this is not a cron expr", func() {})
	if err == nil {
		t.Fatal("expected error for invalid cron expression, got nil")
	}
	s.mu.Lock()
	_, ok := s.entries[42]
	s.mu.Unlock()
	if ok {
		t.Fatal("invalid cron should not write entry")
	}
}

func TestRemoveTask_RemovesExistingEntry(t *testing.T) {
	s := NewCronScheduler()
	if err := s.RegisterTask(3, "@every 1m", func() {}); err != nil {
		t.Fatalf("register: %v", err)
	}
	s.RemoveTask(3)
	s.mu.Lock()
	_, ok := s.entries[3]
	s.mu.Unlock()
	if ok {
		t.Fatal("entry still present after RemoveTask")
	}
}

func TestRemoveTask_UnknownIDIsSilentNoop(t *testing.T) {
	s := NewCronScheduler()
	// Must not panic. No assertions beyond reaching this line.
	s.RemoveTask(9999)
}

func TestStartStop_FiresRegisteredJobAtLeastOnce(t *testing.T) {
	// robfig/cron/v3 ConstantDelaySchedule rounds sub-second intervals up to 1s.
	// Use @every 1s and sleep 2200ms to guarantee ≥2 fires before Stop.
	s := NewCronScheduler()
	var fires int32
	if err := s.RegisterTask(1, "@every 1s", func() {
		atomic.AddInt32(&fires, 1)
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	s.Start()
	time.Sleep(2200 * time.Millisecond)
	s.Stop()

	got := atomic.LoadInt32(&fires)
	if got < 2 {
		t.Fatalf("expected at least 2 fires in 2200ms with @every 1s, got %d", got)
	}

	// After Stop, no further fires within a 500ms grace window.
	before := atomic.LoadInt32(&fires)
	time.Sleep(500 * time.Millisecond)
	if after := atomic.LoadInt32(&fires); after != before {
		t.Fatalf("Stop did not halt scheduler: %d fires after Stop", after-before)
	}
}
