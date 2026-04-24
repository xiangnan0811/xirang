package database

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm/logger"
)

// ctxAwareLogger wraps GORM's default logger and silences the queries whose
// error is context.Canceled or context.DeadlineExceeded. GORM's default logger
// treats these as DB errors and floods the log whenever a client aborts mid-
// query (the panel-query endpoint does this on every keystroke via
// AbortController). Everything else is forwarded unchanged.
type ctxAwareLogger struct {
	inner logger.Interface
}

func newCtxAwareLogger(inner logger.Interface) logger.Interface {
	return &ctxAwareLogger{inner: inner}
}

func (l *ctxAwareLogger) LogMode(level logger.LogLevel) logger.Interface {
	return &ctxAwareLogger{inner: l.inner.LogMode(level)}
}

func (l *ctxAwareLogger) Info(ctx context.Context, msg string, args ...interface{}) {
	l.inner.Info(ctx, msg, args...)
}

func (l *ctxAwareLogger) Warn(ctx context.Context, msg string, args ...interface{}) {
	l.inner.Warn(ctx, msg, args...)
}

func (l *ctxAwareLogger) Error(ctx context.Context, msg string, args ...interface{}) {
	l.inner.Error(ctx, msg, args...)
}

func (l *ctxAwareLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		// Client disconnected; not worth an Error-level log line. GORM's
		// slow-query threshold is orthogonal — if a canceled query was
		// genuinely slow before the cancel, ops can find it in access logs
		// via the 499 status.
		return
	}
	l.inner.Trace(ctx, begin, fc, err)
}
