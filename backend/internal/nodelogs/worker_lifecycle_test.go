package nodelogs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"xirang/backend/internal/model"
)

func TestWorkerReleasesClaimOnEveryExit(t *testing.T) {
	for _, stage := range []string{"success", "canceled", "cursor_load", "fetch", "insert", "cursor_save"} {
		t.Run(stage, func(t *testing.T) {
			db := openCursorTestDB(t)
			if err := db.AutoMigrate(&LogEntry{}); err != nil {
				t.Fatal(err)
			}
			runner := &fakeRunner{out: `{"__REALTIME_TIMESTAMP":"1700000000000000","__CURSOR":"FAKE_CURSOR_FOR_TEST_ONLY","MESSAGE":"FAKE_MESSAGE_FOR_TEST_ONLY"}` + "\n" + JournalDelim + "\n"}
			if stage == "fetch" {
				runner.err = context.DeadlineExceeded
			}
			s := NewScheduler(db, runner)
			t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
			node := seedCollector(t, s, 1)
			injected := false
			fail := func(tx *gorm.DB) { injected = true; _ = tx.AddError(errors.New("FAKE_DB_FAILURE_FOR_TEST_ONLY")) }
			if stage == "cursor_load" {
				if err := db.Callback().Query().Before("gorm:query").Register("test:fail_cursor_load", func(tx *gorm.DB) {
					if tx.Statement.Table == "node_log_cursors" {
						fail(tx)
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "insert" || stage == "cursor_save" {
				if err := db.Callback().Create().Before("gorm:create").Register("test:fail_write", func(tx *gorm.DB) {
					if stage == "insert" && tx.Statement.Table == "node_logs" || stage == "cursor_save" && tx.Statement.Table == "node_log_cursors" {
						fail(tx)
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if stage == "canceled" {
				cancel()
			}
			if !s.tryClaim(node.ID) {
				t.Fatal("initial claim failed")
			}
			w := &Worker{db: db, fetcher: s.fetcher, curRepo: s.curRepo, onStart: s.markInFlight, onDone: s.release}
			w.process(ctx, CollectJob{Node: node})
			if (stage == "cursor_load" || stage == "insert" || stage == "cursor_save") && !injected {
				t.Fatalf("failure stage %s was not reached", stage)
			}
			assertNoCollectorClaims(t, s)
			if !s.tryClaim(node.ID) {
				t.Fatal("completed node cannot be retried")
			}
			s.release(node.ID)
		})
	}
}

type recoveringCollector struct {
	entered  chan uint
	release  chan struct{}
	mu       sync.Mutex
	attempts map[uint]int
}

func (r *recoveringCollector) Run(ctx context.Context, node model.Node, _ string, _ time.Duration, _ int) (string, error) {
	r.mu.Lock()
	r.attempts[node.ID]++
	attempt := r.attempts[node.ID]
	r.mu.Unlock()
	if attempt == 1 {
		r.entered <- node.ID
		select {
		case <-r.release:
			return "", context.DeadlineExceeded
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return JournalDelim + "\n", nil
}

func TestWorkerPoolRecoversAfterAllCollectorsTimeout(t *testing.T) {
	db := openCursorTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	r := &recoveringCollector{entered: make(chan uint, 2), release: make(chan struct{}), attempts: make(map[uint]int)}
	s := NewScheduler(db, r)
	seedCollector(t, s, 1)
	seedCollector(t, s, 2)
	s.jobs = make(chan CollectJob, 2)
	completed := make(chan uint, 4)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		w := &Worker{db: db, jobs: s.jobs, fetcher: s.fetcher, curRepo: s.curRepo, onStart: s.markInFlight, onDone: func(id uint) { s.release(id); completed <- id }}
		wg.Add(1)
		go func() { defer wg.Done(); w.Run(ctx) }()
	}
	t.Cleanup(func() { cancel(); wg.Wait(); _ = s.Shutdown(context.Background()) })
	s.enqueue(ctx)
	for i := 0; i < 2; i++ {
		select {
		case <-r.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("worker did not enter")
		}
	}
	s.enqueue(ctx)
	if len(s.jobs) != 0 {
		t.Fatal("in-flight nodes were queued again")
	}
	close(r.release)
	for i := 0; i < 2; i++ {
		select {
		case <-completed:
		case <-time.After(5 * time.Second):
			t.Fatal("timed-out worker did not release")
		}
	}
	assertNoCollectorClaims(t, s)
	s.enqueue(ctx)
	for i := 0; i < 2; i++ {
		select {
		case <-completed:
		case <-time.After(5 * time.Second):
			t.Fatal("healthy retry did not complete")
		}
	}
	assertNoCollectorClaims(t, s)
}
