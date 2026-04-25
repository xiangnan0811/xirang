package scheduler

import (
	"testing"
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
