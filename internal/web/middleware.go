package web

import (
	"context"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

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
// logs the query string.
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
		s.logger.Info("request", attrs...)
	})
}

// maxLoggedPath is the longest path that a log entry holds.
const maxLoggedPath = 200

// pathForLog is the path of a request that no route matched (a 404, or a 405
// for a method the route does not have), as it is logged. The token of an
// invitation link is a secret and never reaches the log, and a path is cut at
// 200 bytes, so that a request cannot fill the log.
func pathForLog(path string) string {
	if strings.HasPrefix(path, "/invite/") {
		return "/invite/{token}"
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
