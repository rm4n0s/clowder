package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/store"
)

func TestOpenStoreChoosesTheDatabase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		dataDir      string
		databaseURL  string
		wantSQLite   string // the target the SQLite opener gets; empty: it is not called
		wantPostgres string // the target the PostgreSQL opener gets; empty: it is not called
	}{
		{"no URL: SQLite in the data directory", "/srv/clowder", "", "/srv/clowder/clowder.db", ""},
		{"postgres URL", "/srv/clowder", "postgres://u:p@db:5432/clowder", "", "postgres://u:p@db:5432/clowder"},
		{"postgresql URL", "d", "postgresql://db/clowder", "", "postgresql://db/clowder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var sqliteTarget, postgresTarget string
			fake := func(target *string) storeOpener {
				return func(_ context.Context, got string, _ clock.Clock, _ *slog.Logger) (*store.Store, error) {
					*target = got
					return nil, nil
				}
			}
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			_, err := openStoreWith(t.Context(), tt.dataDir, tt.databaseURL, clock.Real(), logger,
				fake(&sqliteTarget), fake(&postgresTarget))
			if err != nil {
				t.Fatalf("openStoreWith: %v", err)
			}
			if sqliteTarget != tt.wantSQLite || postgresTarget != tt.wantPostgres {
				t.Errorf("sqlite got %q, postgres got %q; want %q and %q", sqliteTarget, postgresTarget, tt.wantSQLite, tt.wantPostgres)
			}
			if strings.Contains(logs.String(), ":p@") {
				t.Errorf("the log has the password: %s", logs.String())
			}
		})
	}
}

func TestOpenStoreLogsTheURLWithoutItsPassword(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	noop := func(context.Context, string, clock.Clock, *slog.Logger) (*store.Store, error) { return nil, nil }
	if _, err := openStoreWith(t.Context(), "d", "postgres://clowder:hunter2@db/clowder", clock.Real(), logger, noop, noop); err != nil {
		t.Fatal(err)
	}
	if got := logs.String(); !strings.Contains(got, "postgres://clowder:xxxxx@db/clowder") || strings.Contains(got, "hunter2") {
		t.Errorf("log = %s", got)
	}
}

func TestOpenStoreOnSQLite(t *testing.T) {
	t.Parallel()
	st, err := openStore(t.Context(), t.TempDir(), "", clock.Real(), nil)
	if err != nil {
		t.Fatalf("openStore: %+v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}
