package nodelogs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func seedCollector(t *testing.T, s *Scheduler, id uint) model.Node {
	t.Helper()
	node := model.Node{ID: id, Name: fmt.Sprintf("FAKE_NODE_%d_FOR_TEST_ONLY", id), Host: "invalid", Username: "test", BackupDir: fmt.Sprintf("/FAKE_BACKUP_%d_FOR_TEST_ONLY", id), LogJournalctlEnabled: true}
	if err := s.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	return node
}

func TestSchedulerDeduplicatesQueuedNodes(t *testing.T) {
	s := NewScheduler(openCursorTestDB(t), &fakeRunner{})
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	seedCollector(t, s, 1)
	s.enqueue(context.Background())
	s.enqueue(context.Background())
	if got := len(s.jobs); got != 1 {
		t.Fatalf("queued %d jobs for one node", got)
	}
}

func TestSchedulerShutdownBeforeRun(t *testing.T) {
	s := NewScheduler(openCursorTestDB(t), &fakeRunner{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("idle shutdown: %v", err)
	}
	s.Run(context.Background())
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type joiningCollector struct{ entered, canceled, release chan struct{} }

func (r *joiningCollector) Run(ctx context.Context, _ model.Node, _ string, _ time.Duration, _ int) (string, error) {
	close(r.entered)
	<-ctx.Done()
	close(r.canceled)
	<-r.release
	return "", ctx.Err()
}

func TestSchedulerShutdownCancelsAndJoins(t *testing.T) {
	r := &joiningCollector{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	s := NewScheduler(openCursorTestDB(t), r)
	s.workers = 1
	s.tick = time.Hour
	seedCollector(t, s, 1)
	seedCollector(t, s, 2)
	s.enqueue(context.Background())
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(r.release) }) }
	t.Cleanup(func() { release(); cancel(); _ = s.Shutdown(context.Background()) })
	go s.Run(parent)
	select {
	case <-r.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never entered runner")
	}
	expired, stop := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer stop()
	if err := s.Shutdown(expired); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unjoined shutdown: %v", err)
	}
	select {
	case <-r.canceled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel owned runner")
	}
	select {
	case <-s.done:
		t.Fatal("done closed before runner joined")
	default:
	}
	release()
	joined, joinedCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer joinedCancel()
	if err := s.Shutdown(joined); err != nil {
		t.Fatalf("joined shutdown: %v", err)
	}
	assertNoCollectorClaims(t, s)
	if err := s.Shutdown(expired); err != nil {
		t.Fatalf("completed shutdown: %v", err)
	}
}

func TestSchedulerParentCancellationJoinsAndReleasesQueue(t *testing.T) {
	s := NewScheduler(openCursorTestDB(t), &fakeRunner{})
	seedCollector(t, s, 1)
	seedCollector(t, s, 2)
	s.enqueue(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Run(ctx)
	assertNoCollectorClaims(t, s)
	if len(s.jobs) != 0 {
		t.Fatal("canceled Run left queued jobs")
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func assertNoCollectorClaims(t *testing.T, s *Scheduler) {
	t.Helper()
	s.claimsMu.Lock()
	defer s.claimsMu.Unlock()
	if len(s.claims) != 0 {
		t.Fatalf("remaining claims: %v", s.claims)
	}
}

func TestSchedulerQueueFullRollsBackAndRecovers(t *testing.T) {
	s := NewScheduler(openCursorTestDB(t), &fakeRunner{})
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	s.jobs = make(chan CollectJob, 1)
	first := seedCollector(t, s, 1)
	second := seedCollector(t, s, 2)
	s.enqueue(context.Background())
	s.claimsMu.Lock()
	_, rejectedClaim := s.claims[second.ID]
	s.claimsMu.Unlock()
	if rejectedClaim {
		t.Fatal("full queue retained rejected node claim")
	}
	job := <-s.jobs
	if job.Node.ID != first.ID {
		t.Fatalf("unexpected queued node %d", job.Node.ID)
	}
	s.release(job.Node.ID)
	if err := s.db.Model(&first).Update("log_journalctl_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	s.enqueue(context.Background())
	if len(s.jobs) != 1 {
		t.Fatal("queue did not recover")
	}
	if job = <-s.jobs; job.Node.ID != second.ID {
		t.Fatalf("recovered node %d", job.Node.ID)
	}
	s.release(job.Node.ID)
	assertNoCollectorClaims(t, s)
}

func TestSchedulerCanceledEnqueueHasNoClaims(t *testing.T) {
	s := NewScheduler(openCursorTestDB(t), &fakeRunner{})
	seedCollector(t, s, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.enqueue(ctx)
	if len(s.jobs) != 0 {
		t.Fatal("canceled enqueue dispatched work")
	}
	assertNoCollectorClaims(t, s)
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.enqueue(context.Background())
	assertNoCollectorClaims(t, s)
}

func TestSchedulerConcurrentRunAndShutdown(t *testing.T) {
	s := NewScheduler(openCursorTestDB(t), &fakeRunner{})
	s.tick = time.Hour
	s.enqueue(context.Background())
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); <-start; s.Run(context.Background()) }()
		go func() {
			defer wg.Done()
			<-start
			if err := s.Shutdown(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent shutdown did not complete")
	}
	assertNoCollectorClaims(t, s)
}
