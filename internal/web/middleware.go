package web

import (
	"fmt"
	"net/http"
	"runtime/debug"
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

// logRequests logs one entry per request after it ends: the method, the
// matched route pattern (the path when no pattern matched), the status and the
// duration in milliseconds. It never logs the query string.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		// The mux sets r.Pattern on this same request while it routes it.
		pattern := r.Pattern
		if pattern == "" {
			pattern = r.URL.Path
		}
		s.logger.Info("request",
			"method", r.Method,
			"pattern", pattern,
			"status", rec.status,
			"duration_ms", float64(time.Since(start))/float64(time.Millisecond),
		)
	})
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
