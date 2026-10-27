package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
)

// stagedSweepInterval is how often serve sweeps the uploads of the API that no
// post took (SPEC §7.12).
const stagedSweepInterval = time.Hour

// stagedSweeper removes the staged uploads older than maxAge and says how many
// it removed. *app.Posts is one.
type stagedSweeper interface {
	SweepStaged(ctx context.Context, maxAge time.Duration) (int, error)
}

// startStagedSweeper sweeps the staged uploads older than app.StagedLifetime
// at once and then every interval, in a goroutine that ctx stops. The returned
// function stops the goroutine, waits for it to end and may be called more than
// once. A sweep that fails is logged and tried again at the next tick.
func startStagedSweeper(ctx context.Context, sw stagedSweeper, interval time.Duration, logger *slog.Logger) func() {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			removed, err := sw.SweepStaged(ctx, app.StagedLifetime)
			switch {
			case ctx.Err() != nil:
				return
			case err != nil:
				logger.Error("sweeping the staged uploads failed", "err", err)
			case removed > 0:
				logger.Info("removed staged uploads that no post took", "count", removed)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}
