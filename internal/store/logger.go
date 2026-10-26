package store

import (
	"context"
	"log/slog"
	"time"

	gormlogger "gorm.io/gorm/logger"
)

// gormLogger sends GORM's log to slog. It logs one thing: a query slower than
// the threshold, as a warning. Errors are not logged here (the caller gets
// them and logs them with their route), and a statement's arguments never
// are: they may hold the text of a post.
type gormLogger struct {
	logger *slog.Logger
	slow   time.Duration
}

// newGormLogger returns the adapter that logs queries slower than slow to
// logger.
func newGormLogger(logger *slog.Logger, slow time.Duration) *gormLogger {
	return &gormLogger{logger: logger, slow: slow}
}

// LogMode ignores the level: the adapter always logs only slow queries.
func (l *gormLogger) LogMode(gormlogger.LogLevel) gormlogger.Interface {
	return l
}

// Info drops the message.
func (l *gormLogger) Info(context.Context, string, ...any) {}

// Warn drops the message.
func (l *gormLogger) Warn(context.Context, string, ...any) {}

// Error drops the message.
func (l *gormLogger) Error(context.Context, string, ...any) {}

// ParamsFilter removes the arguments of a statement before GORM builds the
// text for the log, so the log has "?" where a value was.
func (l *gormLogger) ParamsFilter(_ context.Context, sql string, _ ...any) (string, []any) {
	return sql, nil
}

// Trace logs the statement when it took longer than the threshold. begin is
// the real start time that GORM measured; the duration is wall-clock time and
// does not use the injected clock.
func (l *gormLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), _ error) {
	elapsed := time.Since(begin)
	if elapsed <= l.slow {
		return
	}
	sql, rows := fc()
	l.logger.WarnContext(ctx, "slow query", "elapsed", elapsed.String(), "rows", rows, "sql", sql)
}
