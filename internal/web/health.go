package web

import (
	"context"
	"net/http"
	"time"
)

const (
	// healthPattern is the route of the health check (SPEC D40).
	healthPattern = "GET /healthz"
	// healthTimeout is how long the database gets to answer the health check.
	healthTimeout = 2 * time.Second
	// healthOK and healthUnavailable are the only bodies the health check
	// sends: they say nothing about the cause of a failure.
	healthOK          = "ok\n"
	healthUnavailable = "unavailable\n"
)

// HealthChecker tells whether the server can do its work. app.Health is the
// one the binary uses.
type HealthChecker interface {
	// Check returns nil when the database answers within ctx.
	Check(ctx context.Context) error
}

// healthz answers GET /healthz: 200 "ok" when the database answers a ping
// within two seconds, 503 "unavailable" otherwise. It needs no login and
// reveals nothing else, not even the error. A failure is logged at warn; a
// success is not logged here, and the request log writes it at debug level
// (see logRequests).
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	status, body := http.StatusOK, healthOK
	if s.svc.Health == nil {
		status, body = http.StatusServiceUnavailable, healthUnavailable
		s.logger.Warn("health check without a health service")
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), s.healthTimeout)
		err := s.svc.Health.Check(ctx)
		cancel()
		if err != nil {
			status, body = http.StatusServiceUnavailable, healthUnavailable
			s.logger.Warn("health check failed", "err", err)
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write([]byte(body)); err != nil {
		s.logger.Debug("health check write failed", "err", err)
	}
}
