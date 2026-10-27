package main

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/store"
)

// storeOpener opens a store at a target: the path of the SQLite file, or the
// URL of the PostgreSQL database. store.Open and store.OpenPostgres are the
// two openers; tests pass fakes.
type storeOpener func(ctx context.Context, target string, clk clock.Clock, logger *slog.Logger) (*store.Store, error)

// openStore opens the database the settings name: PostgreSQL when the database
// URL is set, SQLite in <data>/clowder.db otherwise (SPEC D38). serve, invite
// and demo all open their database here.
func openStore(ctx context.Context, dataDir, databaseURL string, clk clock.Clock, logger *slog.Logger) (*store.Store, error) {
	return openStoreWith(ctx, dataDir, databaseURL, clk, logger, store.Open, store.OpenPostgres)
}

// openStoreWith is openStore with the two openers as parameters. The URL is
// logged without its password.
func openStoreWith(ctx context.Context, dataDir, databaseURL string, clk clock.Clock, logger *slog.Logger, openSQLite, openPostgres storeOpener) (*store.Store, error) {
	if databaseURL != "" {
		logger.Debug("opening the database", "database", "postgres", "url", config.RedactURL(databaseURL))
		return openPostgres(ctx, databaseURL, clk, logger)
	}
	return openSQLite(ctx, filepath.Join(dataDir, databaseFile), clk, logger)
}
