package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/rm4n0s/clowder/internal/calls"
	"github.com/rm4n0s/clowder/internal/cluster"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/metrics"
	"github.com/rm4n0s/errors"
)

const (
	// metricsReadHeaderTimeout, metricsReadTimeout, metricsWriteTimeout and
	// metricsIdleTimeout bound the connections of the metrics listener, which
	// answers one small page to a scraper every few seconds.
	metricsReadHeaderTimeout = 5 * time.Second
	metricsReadTimeout       = 10 * time.Second
	metricsWriteTimeout      = 10 * time.Second
	metricsIdleTimeout       = time.Minute
	// metricsMaxHeaderBytes bounds the headers of a scrape.
	metricsMaxHeaderBytes = 16 << 10
	// metricsShutdownTimeout is how long a scrape that is running gets to
	// finish when serve stops.
	metricsShutdownTimeout = 5 * time.Second
)

// newMetricsSet makes the metrics of the process when -metrics-addr is set, and
// returns nil when it is not: then nothing is counted. Reporting code takes
// the pieces through reportTo, which turns the nil into a Set whose metrics are
// all nil, so that it counts into nothing at no cost.
func newMetricsSet(cfg config.Config) (*metrics.Set, error) {
	if cfg.MetricsAddr == "" {
		return nil, nil
	}
	return metrics.NewSet(metrics.SetOptions{Version: buildVersion(), Redis: cfg.RedisURL != ""})
}

// reportTo returns set, or a Set with no metrics when set is nil: every field
// of it is nil, and a nil metric accepts every call and does nothing.
func reportTo(set *metrics.Set) *metrics.Set {
	if set == nil {
		return &metrics.Set{}
	}
	return set
}

// startMetrics starts the listener of the metrics (SPEC D60) when
// cfg.MetricsAddr is set: an http.Server of its own, with timeouts, that
// serves GET /metrics and nothing else, in a goroutine that the returned
// function stops and waits for. It returns the address it listens on (so that
// ":0" shows its port), or "" when it is off, and then opens nothing. An error
// here (a port in use) stops serve before anything else runs.
func startMetrics(ctx context.Context, cfg config.Config, reg *metrics.Registry, logger *slog.Logger) (func(), string, error) {
	if cfg.MetricsAddr == "" {
		return func() {}, "", nil
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.MetricsAddr)
	if err != nil {
		return nil, "", errors.NewErr(tagMetricsListenFailed, err).SetMetadata("addr", cfg.MetricsAddr)
	}
	srv := &http.Server{
		Handler:           reg.Handler(logger),
		ReadHeaderTimeout: metricsReadHeaderTimeout,
		ReadTimeout:       metricsReadTimeout,
		WriteTimeout:      metricsWriteTimeout,
		IdleTimeout:       metricsIdleTimeout,
		MaxHeaderBytes:    metricsMaxHeaderBytes,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("the metrics listener stopped", "err", err, "addr", ln.Addr().String())
		}
	}()
	addr := ln.Addr().String()
	logger.Info("metrics listening", "addr", addr)
	var once sync.Once
	return func() {
		once.Do(func() {
			shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), metricsShutdownTimeout)
			defer cancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				logger.Warn("shutting down the metrics listener failed", "err", err)
				if closeErr := srv.Close(); closeErr != nil {
					logger.Warn("closing the metrics listener failed", "err", closeErr)
				}
			}
			<-done
		})
	}, addr, nil
}

// withMediaMetrics makes the media server count its sockets and calls in the
// metrics.
func withMediaMetrics(m *metrics.Set) func(*calls.Config) {
	return func(c *calls.Config) {
		c.SocketsOpen = m.WebSockets.With(metrics.WebSocketKindCall)
		c.Outcomes = m.CallsTotal
	}
}

// bridgeMetrics returns the counters of the bridge to Redis.
func bridgeMetrics(m *metrics.Set) cluster.BridgeMetrics {
	return cluster.BridgeMetrics{Reconnects: m.RedisReconnects, Published: m.RedisPublished, Received: m.RedisReceived}
}

const tagMetricsListenFailed = "MetricsListenFailed"
