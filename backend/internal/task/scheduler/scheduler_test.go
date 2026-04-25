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
