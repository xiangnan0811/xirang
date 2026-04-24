package nodelogs

import (
	"context"
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
}

func NewScheduler(db *gorm.DB, runner Runner) *Scheduler {
	return &Scheduler{
		db:      db,
		jobs:    make(chan CollectJob, DefaultJobQueueSize),
		workers: DefaultWorkerCount,
		tick:    DefaultTickInterval,
		fetcher: NewFetcher(runner),
		curRepo: NewCursorRepo(db),
	}
}

func (s *Scheduler) Run(ctx context.Context) {
	for i := 0; i < s.workers; i++ {
		w := &Worker{db: s.db, jobs: s.jobs, fetcher: s.fetcher, curRepo: s.curRepo}
		go w.Run(ctx)
	}
	t := time.NewTicker(s.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			close(s.jobs)
			return
		case <-t.C:
			s.enqueue(ctx)
		}
	}
}

func (s *Scheduler) enqueue(ctx context.Context) {
	var nodes []model.Node
	if err := s.db.Find(&nodes).Error; err != nil {
		logger.Module("nodelogs").Warn().Err(err).Msg("load nodes failed")
		return
	}
	for _, n := range nodes {
		if !needsCollection(&n) {
			continue
		}
		select {
		case s.jobs <- CollectJob{Node: n}:
			// Update gauge from the scheduler goroutine only — reading
			// len(channel) from concurrent workers gave a racy snapshot
			// that made the metric jitter wildly under load.
			queueDepth.Set(float64(len(s.jobs)))
		case <-ctx.Done():
			return
		default:
			logger.Module("nodelogs").Warn().
				Uint("node_id", n.ID).
				Msg("job queue full, skipping tick")
		}
	}
}

// needsCollection reports whether this node has any configured log source.
func needsCollection(n *model.Node) bool {
	return n.LogJournalctlEnabled || len(n.DecodedLogPaths()) > 0
}
