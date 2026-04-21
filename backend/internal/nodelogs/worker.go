package nodelogs

import (
	"context"

	"xirang/backend/internal/logger"

	"gorm.io/gorm"
)

type Worker struct {
	db      *gorm.DB
	jobs    <-chan CollectJob
	fetcher *Fetcher
	curRepo *CursorRepo
}

func (w *Worker) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-w.jobs:
			if !ok {
				return
			}
			w.process(ctx, job)
		}
	}
}

func (w *Worker) process(ctx context.Context, job CollectJob) {
	queueDepth.Set(float64(len(w.jobs)))

	cursors, err := w.curRepo.LoadForNode(job.Node.ID)
	if err != nil {
		logger.Module("nodelogs").Warn().
			Uint("node_id", job.Node.ID).Err(err).
			Msg("load cursors failed")
		return
	}
	entries, newCursors, err := w.fetcher.Fetch(ctx, job.Node, cursors)
	if err != nil {
		logger.Module("nodelogs").Warn().
			Uint("node_id", job.Node.ID).Err(err).
			Msg("fetch failed")
		return
	}
	if len(entries) > 0 {
		if err := w.db.CreateInBatches(&entries, InsertBatchSize).Error; err != nil {
			logger.Module("nodelogs").Warn().
				Uint("node_id", job.Node.ID).Err(err).
				Int("count", len(entries)).
				Msg("insert logs failed")
			fetchErrors.WithLabelValues(nodeIDLabel(job.Node.ID), "insert").Inc()
			return
		}
		counts := map[string]int{}
		for _, e := range entries {
			counts[e.Source]++
		}
		for src, n := range counts {
			logsIngested.WithLabelValues(nodeIDLabel(job.Node.ID), src).Add(float64(n))
		}
	}
	if len(newCursors) > 0 {
		if err := w.curRepo.SaveForNode(job.Node.ID, newCursors); err != nil {
			logger.Module("nodelogs").Warn().
				Uint("node_id", job.Node.ID).Err(err).
				Msg("save cursors failed")
		}
	}
}
