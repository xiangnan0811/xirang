package nodelogs

import (
	"context"
	"sync"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

type Scheduler struct {
	db      *gorm.DB
	jobs    chan CollectJob
	workers int
	tick    time.Duration
	fetcher *Fetcher
	curRepo *CursorRepo
	done    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc

	// Run (or Shutdown before Run) is the sole owner of channel closure.
	lifecycleMu sync.Mutex
	started     bool
	workersWG   sync.WaitGroup
	enqueueMu   sync.Mutex
	claimsMu    sync.Mutex
	claims      map[uint]bool // false = queued, true = in flight
}

func NewScheduler(db *gorm.DB, runner Runner) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		db:      db,
		jobs:    make(chan CollectJob, DefaultJobQueueSize),
		workers: DefaultWorkerCount,
		tick:    DefaultTickInterval,
		fetcher: NewFetcher(runner),
		curRepo: NewCursorRepo(db),
		done:    make(chan struct{}),
		ctx:     ctx,
		cancel:  cancel,
		claims:  make(map[uint]bool),
	}
}

func (s *Scheduler) Run(ctx context.Context) {
	s.lifecycleMu.Lock()
	if s.started {
		s.lifecycleMu.Unlock()
		return
	}
	s.started = true
	s.lifecycleMu.Unlock()
	stopParent := context.AfterFunc(ctx, s.cancel)
	defer stopParent()
	if ctx.Err() != nil {
		s.cancel()
	}
	defer s.finish()
	for i := 0; i < s.workers; i++ {
		w := &Worker{db: s.db, jobs: s.jobs, fetcher: s.fetcher, curRepo: s.curRepo, onStart: s.markInFlight, onDone: s.release}
		s.workersWG.Add(1)
		go func() { defer s.workersWG.Done(); w.Run(s.ctx) }()
	}
	t := time.NewTicker(s.tick)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.enqueue(s.ctx)
		}
	}
}

// Shutdown cancels collection and waits for all workers. Before Run it permanently
// stops this scheduler; repeated Run/Shutdown calls cannot restart or double-close it.
func (s *Scheduler) Shutdown(ctx context.Context) error {
	s.cancel()
	s.lifecycleMu.Lock()
	idle := !s.started
	s.started = true
	s.lifecycleMu.Unlock()
	if idle {
		s.finish()
	}
	select {
	case <-s.done:
		return nil
	default:
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		shutdownTimeouts.Inc()
		return ctx.Err()
	}
}

func (s *Scheduler) finish() {
	s.cancel()
	s.enqueueMu.Lock()
	close(s.jobs)
	// Workers stop on cancellation rather than processing queued snapshots.
	for job := range s.jobs {
		s.release(job.Node.ID)
	}
	s.enqueueMu.Unlock()
	s.workersWG.Wait()
	queueDepth.Set(0)
	close(s.done)
}

func (s *Scheduler) tryClaim(nodeID uint) bool {
	s.claimsMu.Lock()
	defer s.claimsMu.Unlock()
	if _, exists := s.claims[nodeID]; exists {
		return false
	}
	s.claims[nodeID] = false
	return true
}

func (s *Scheduler) markInFlight(nodeID uint) {
	s.claimsMu.Lock()
	defer s.claimsMu.Unlock()
	if active, exists := s.claims[nodeID]; exists && !active {
		s.claims[nodeID] = true
		inFlight.Inc()
	}
	queueDepth.Set(float64(len(s.jobs)))
}

func (s *Scheduler) release(nodeID uint) {
	s.claimsMu.Lock()
	defer s.claimsMu.Unlock()
	if s.claims[nodeID] {
		inFlight.Dec()
	}
	delete(s.claims, nodeID)
}

func (s *Scheduler) enqueue(ctx context.Context) {
	s.enqueueMu.Lock()
	defer s.enqueueMu.Unlock()
	if ctx.Err() != nil || s.ctx.Err() != nil {
		return
	}
	// An external caller's context and owned shutdown both interrupt DB work.
	queryCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	var nodes []model.Node
	if err := s.db.WithContext(queryCtx).Find(&nodes).Error; err != nil {
		logger.Module("nodelogs").Warn().Err(err).Msg("load nodes failed")
		return
	}
	rejected := 0
	defer func() {
		queueDepth.Set(float64(len(s.jobs)))
		if rejected > 0 {
			logger.Module("nodelogs").Warn().Int("rejected", rejected).
				Int("queue_capacity", cap(s.jobs)).Int("queue_depth", len(s.jobs)).
				Msg("job queue full, skipping tick")
		}
	}()
	for _, n := range nodes {
		if !needsCollection(&n) {
			continue
		}
		if queryCtx.Err() != nil || s.ctx.Err() != nil {
			return
		}
		if !s.tryClaim(n.ID) {
			jobsDeduplicated.Inc()
			continue
		}
		select {
		case s.jobs <- CollectJob{Node: n}:
		case <-queryCtx.Done():
			s.release(n.ID)
			queueRejected.WithLabelValues("shutdown").Inc()
			return
		case <-s.ctx.Done():
			s.release(n.ID)
			queueRejected.WithLabelValues("shutdown").Inc()
			return
		default:
			s.release(n.ID)
			queueRejected.WithLabelValues("full").Inc()
			rejected++
		}
	}
}

// needsCollection reports whether this node has any configured log source.
func needsCollection(n *model.Node) bool {
	return n.LogJournalctlEnabled || len(n.DecodedLogPaths()) > 0
}
