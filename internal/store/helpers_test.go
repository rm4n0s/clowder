package store

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/errors"
)

// widget is a test-only model.
type widget struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"uniqueIndex;not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// getWidget is a test-only store method with one query: it shows how a method
// tags its errors where they first appear.
func (s *Store) getWidget(ctx context.Context, id uint) (widget, error) {
	var w widget
	err := s.db(ctx).First(&w, id).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return widget{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "getWidget")
		default:
			return widget{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "getWidget")
		}
	}
	return w, nil
}

// createWidget is a test-only store method with one query.
func (s *Store) createWidget(ctx context.Context, w *widget) error {
	err := s.db(ctx).Create(w).Error
	if err != nil {
		switch classify(err) {
		case failureConflict:
			return errors.NewErr(TagConflict, err).SetMetadata("op", "createWidget")
		default:
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createWidget")
		}
	}
	return nil
}

// brokenQuery is a test-only store method whose query fails.
func (s *Store) brokenQuery(ctx context.Context) error {
	err := s.db(ctx).Exec("SELECT * FROM no_such_table").Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return errors.NewErr(TagNotFound, err).SetMetadata("op", "brokenQuery")
		case failureConflict:
			return errors.NewErr(TagConflict, err).SetMetadata("op", "brokenQuery")
		default:
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "brokenQuery")
		}
	}
	return nil
}

// countWidgets counts the rows of widgets, failing the test on an error.
func countWidgets(tb testing.TB, s *Store) int64 {
	tb.Helper()
	var n int64
	if err := s.db(tb.Context()).Model(&widget{}).Count(&n).Error; err != nil {
		tb.Fatalf("counting widgets: %v", err)
	}
	return n
}

// fakeStart is where the fake clock of the tests starts.
var fakeStart = time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)

// testStore opens a store on a file in a temporary directory, with the widget
// model, the fake clock and a logger that writes to the returned buffer. The
// store is closed when the test ends.
func testStore(tb testing.TB, mutate func(*options)) (*Store, *clock.Fake, *bytes.Buffer) {
	tb.Helper()
	clk := clock.NewFake(fakeStart)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	opts := defaultOptions()
	opts.models = []any{&widget{}}
	// The raw migrations of the application need its tables, which this store
	// does not have.
	opts.migrations = []migration{}
	if mutate != nil {
		mutate(&opts)
	}
	path := filepath.Join(tb.TempDir(), "test.db")
	s, err := open(tb.Context(), path, clk, logger, opts)
	if err != nil {
		tb.Fatalf("open: %+v", err)
	}
	tb.Cleanup(func() {
		if err := s.Close(); err != nil {
			tb.Errorf("close: %v", err)
		}
	})
	return s, clk, &logs
}

// requireRoute fails the test unless err is a tagged error with the route.
func requireRoute(tb testing.TB, err error, route string) *errors.Error {
	tb.Helper()
	appErr, ok := errors.FromError(err)
	if !ok {
		tb.Fatalf("expected *errors.Error, got %T: %v", err, err)
	}
	if !appErr.HasRoute(route) {
		tb.Fatalf("route = %s, want it to contain %s", appErr.Route(), route)
	}
	return appErr
}

// newTestLogger returns a JSON logger that writes to w.
func newTestLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, nil))
}
