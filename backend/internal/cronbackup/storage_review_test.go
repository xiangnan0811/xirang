package cronbackup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestStateDescriptorInspectionFailureIsUnavailable(t *testing.T) {
	cfg := testConfig(t)
	now := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	ctx := context.Background()
	if err := Initialize(ctx, cfg, now); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.StateDirectory, cfg.Engine, stateFileName)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	realStat := stateFileStat
	t.Cleanup(func() { stateFileStat = realStat })
	stateFileStat = func(*os.File) (os.FileInfo, error) { return nil, syscall.EIO }
	if err := Initialize(ctx, cfg, now.Add(time.Hour)); !errors.Is(err, ErrStateUnavailable) {
		t.Fatalf("init error = %v", err)
	}
	observation, err := Read(ctx, cfg, func() time.Time { return now.Add(time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	if observation.Job.Status != JobStatusStateUnavailable || !observation.StatePresent() {
		t.Fatalf("read must retain unavailable/present, got %+v", observation)
	}
	called := false
	err = WithLockedObservation(ctx, cfg, func() time.Time { return now.Add(time.Hour) }, func(locked Observation) error {
		called = true
		if locked.Job.Status != JobStatusStateUnavailable || !locked.StatePresent() {
			t.Fatalf("worker snapshot = %+v", locked)
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("locked callback: called=%v err=%v", called, err)
	}
	preserved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, preserved) {
		t.Fatal("descriptor inspection error changed execution evidence")
	}
	stateFileStat = realStat
	recovered, err := Read(ctx, cfg, func() time.Time { return now.Add(time.Hour) })
	if err != nil || recovered.Job.Status != JobStatusNeverRun {
		t.Fatalf("recovered observation = %+v, err=%v", recovered, err)
	}
}
