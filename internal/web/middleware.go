package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/rm4n0s/clowder/internal/metrics"
	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
)

// statusRecorder remembers the status code of an answer and whether it has
// started. It implements Unwrap, so http.ResponseController reaches the
// underlying writer (flushing, deadlines).
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

// WriteHeader records the first final status code.
func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote && (code >= 200 || code == http.StatusSwitchingProtocols) {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

// Write records the implicit 200 of a body written without WriteHeader.
func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.status = http.StatusOK
		r.wrote = true
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap returns the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// requestState is what the middleware inside logRequests tell it about the
// request. The pointer is in the context of the request.
type requestState struct {
	// userID is the ID of the logged-in user, 0 when there is none.
	userID uint64
	// stream is true when the request became an event stream (liveStream.open
	// sets it): it is counted in the gauge of open streams while it runs, and
	// its duration stays out of the histogram (SPEC D60).
	stream bool
}

// requestStateKey is the context key of the *requestState.
type requestStateKey struct{}

// stateOf returns the requestState of the request. A request that did not go
// through logRequests gets a throw-away one, so callers never check for nil.
func stateOf(ctx context.Context) *requestState {
	if st, ok := ctx.Value(requestStateKey{}).(*requestState); ok {
		return st
	}
	return &requestState{}
}

// logRequests logs one entry per request after it ends: the method, the
// matched route pattern (the path when no pattern matched), the status, the
// duration in milliseconds and, for a logged-in user, the user ID. It never
// logs the query string. A health check that succeeded is logged at debug
// level, every other request at info.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		state := &requestState{}
		// The mux sets Pattern on the request it receives, which is this
		// copy: no middleware below copies the request before the mux.
		r = r.WithContext(context.WithValue(r.Context(), requestStateKey{}, state))
		start := time.Now()
		next.ServeHTTP(rec, r)
		pattern := r.Pattern
		if pattern == "" {
			pattern = pathForLog(r.URL.Path)
		}
		attrs := []any{
			"method", r.Method,
			"pattern", pattern,
			"status", rec.status,
			"duration_ms", float64(time.Since(start)) / float64(time.Millisecond),
		}
		if state.userID != 0 {
			attrs = append(attrs, "user_id", state.userID)
		}
		s.countRequest(r, rec.status, time.Since(start), state)
		// A successful health check comes every few seconds: it is a debug
		// entry, so that it does not flood the log.
		level := slog.LevelInfo
		if pattern == healthPattern && rec.status == http.StatusOK {
			level = slog.LevelDebug
		}
		s.logger.Log(r.Context(), level, "request", attrs...)
	})
}

// countRequest reports a request that ended into the metrics (SPEC D60): the
// counter by route pattern, method and status class, and the histogram of the
// duration. A request that was an event stream or a WebSocket (status 101) is
// counted when it ends, like any other, but is left out of the histogram:
// those are long by design. The label is the pattern the mux matched
// (r.Pattern), never the path, so it holds no ID; a request no route matched
// is "none".
func (s *Server) countRequest(r *http.Request, status int, d time.Duration, state *requestState) {
	m := s.svc.Metrics
	if m == nil {
		return
	}
	if state.stream {
		m.SSEStreams.Dec()
	}
	pattern := r.Pattern
	if pattern == "" {
		pattern = metrics.PatternNone
	}
	m.HTTPRequests.With(pattern, r.Method, statusClass(status)).Inc()
	if !state.stream && status != http.StatusSwitchingProtocols {
		m.HTTPDuration.With(pattern, r.Method).ObserveDuration(d)
	}
}

// streamStarted notes that the request became an event stream: the gauge of
// open streams goes up now, and countRequest takes it down when the handler
// returns.
func (s *Server) streamStarted(r *http.Request) {
	stateOf(r.Context()).stream = true
	if m := s.svc.Metrics; m != nil {
		m.SSEStreams.Inc()
	}
}

// statusClass returns the class of a status code, as the metrics label it:
// "1xx" to "5xx". A code outside those is "other".
func statusClass(status int) string {
	switch status / 100 {
	case 1:
		return "1xx"
	case 2:
		return "2xx"
	case 3:
		return "3xx"
	case 4:
		return "4xx"
	case 5:
		return "5xx"
	}
	return "other"
}

// maxLoggedPath is the longest path that a log entry holds.
const maxLoggedPath = 200

// pathForLog is the path of a request that no route matched (a 404, or a 405
// for a method the route does not have), as it is logged. The token of an
// invitation or reset link is a secret and never reaches the log, and a path is cut at
// 200 bytes, so that a request cannot fill the log.
func pathForLog(path string) string {
	if strings.HasPrefix(path, "/invite/") {
		return "/invite/{token}"
	}
	if strings.HasPrefix(path, "/reset/") {
		return "/reset/{token}"
	}
	if len(path) > maxLoggedPath {
		return path[:maxLoggedPath]
	}
	return path
}

// recoverPanics turns a panic of a handler into a logged HandlerPanicked error
// and a 500 error page.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer s.recoverPanic(w, r)
		next.ServeHTTP(w, r)
	})
}

// recoverPanic must be the deferred function itself, so that recover works.
func (s *Server) recoverPanic(w http.ResponseWriter, r *http.Request) {
	v := recover()
	if v == nil {
		return
	}
	// http.ErrAbortHandler is the way a handler aborts an answer on purpose;
	// net/http expects it to reach the server.
	if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
		panic(v)
	}
	appErr := errors.New(tagHandlerPanicked, "handler panicked",
		"panic", fmt.Sprint(v),
		"stack", string(debug.Stack()),
	)
	s.logger.Error("handler panicked", "err", appErr)
	if rec, ok := w.(*statusRecorder); ok && rec.wrote {
		// The answer has started: a second status cannot be sent.
		return
	}
	s.render(w, r, http.StatusInternalServerError, views.ErrorPage(http.StatusInternalServerError, messageInternalError))
}

const tagHandlerPanicked = "HandlerPanicked"
