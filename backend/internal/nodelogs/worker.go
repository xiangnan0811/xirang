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
	onStart func(uint)
	onDone  func(uint)
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
	if w.onDone != nil {
		defer w.onDone(job.Node.ID)
	}
	if w.onStart != nil {
		w.onStart(job.Node.ID)
	}
	if ctx.Err() != nil {
		return
	}
	curRepo := NewCursorRepo(w.curRepo.db.WithContext(ctx))
	cursors, err := curRepo.LoadForNode(job.Node.ID)
	if err != nil {
		logger.Module("nodelogs").Warn().
			Uint("node_id", job.Node.ID).Err(err).
			Msg("load cursors failed")
		return
	}
	entries, newCursors, err := w.fetcher.Fetch(ctx, job.Node, cursors)
	if err != nil {
		logger.Module("nodelogs").Warn().
			Uint("node_id", job.Node.ID).
			Str("error", sanitizeNodeLogError(err)).
			Msg("fetch failed")
		return
	}
	if len(entries) > 0 {
		sanitizeLogEntries(entries)
		if err := w.db.WithContext(ctx).CreateInBatches(&entries, InsertBatchSize).Error; err != nil {
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
		if err := curRepo.SaveForNode(job.Node.ID, newCursors); err != nil {
			logger.Module("nodelogs").Warn().
				Uint("node_id", job.Node.ID).Err(err).
				Msg("save cursors failed")
			return
		}
	}
	for _, c := range newCursors {
		if c.recoveryReason == "" {
			continue
		}
		journalRecoveries.WithLabelValues(c.recoveryReason).Inc()
		logger.Module("nodelogs").Warn().Uint("node_id", job.Node.ID).
			Str("reason", c.recoveryReason).Int("window_seconds", int(JournalRecoveryWindow.Seconds())).
			Int("batch_limit", JournalBatchSize).Msg("journal recovery boundary reset; historical continuity not guaranteed")
	}
}
