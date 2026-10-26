package web

import (
	"bytes"
	"context"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/a-h/templ"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/web/static"
	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
)

// Texts of the error pages. They are fixed text, never the text of an error.
const (
	messageNotFound      = "Page not found"
	messageInternalError = "Something went wrong"
)

const (
	// shutdownTimeout is how long running requests get to finish when the
	// server stops (SPEC §8).
	shutdownTimeout = 10 * time.Second
	// readHeaderTimeout bounds how long a client may take to send its headers.
	readHeaderTimeout = 10 * time.Second
	// staticPrefix is the path of the embedded static files.
	staticPrefix = "/static/"
)

// Server is the HTTP server of Clowder: the routes, the middleware and the
// handlers. Make it with New.
type Server struct {
	cfg             config.Config
	logger          *slog.Logger
	handler         http.Handler
	shutdownTimeout time.Duration
}

// route is a pattern and its handler, added to the routes of a Server.
type route struct {
	pattern string
	handler http.Handler
}

// New makes the server from its dependencies. Run starts it. It fails when the
// embedded static files cannot be read and hashed.
// TODO(step 2.2): the services are passed here as they are added.
func New(cfg config.Config, logger *slog.Logger) (*Server, error) {
	return newServer(cfg, logger, static.FS)
}

// newServer is New with the static files as a parameter and extra routes,
// which the tests use to fail the hashing and to add a handler that panics or
// blocks.
func newServer(cfg config.Config, logger *slog.Logger, staticFS fs.FS, extra ...route) (*Server, error) {
	s := &Server{cfg: cfg, logger: logger, shutdownTimeout: shutdownTimeout}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.home)
	notFound := http.HandlerFunc(s.notFound)
	staticFiles, err := newStaticHandler(staticFS, staticPrefix, notFound)
	if err != nil {
		return nil, err
	}
	mux.Handle("GET "+staticPrefix, staticFiles)
	for _, rt := range extra {
		mux.Handle(rt.pattern, rt.handler)
	}

	// Outermost first: logging, panic recovery, cross-origin protection.
	h := s.routeOrNotFound(mux)
	h = http.NewCrossOriginProtection().Handler(h)
	h = s.recoverPanics(h)
	h = s.logRequests(h)
	s.handler = h
	return s, nil
}

// Handler returns the whole handler stack: request logging, panic recovery,
// cross-origin protection and the routes.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// Run listens on the configured address and serves until ctx is done. Then it
// stops accepting requests and waits for the running ones for 10 seconds
// (SPEC §8). It returns nil after a clean shutdown.
func (s *Server) Run(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.cfg.ListenAddr)
	if err != nil {
		return errors.NewErr(tagListenFailed, err).SetMetadata("addr", s.cfg.ListenAddr)
	}
	return s.serve(ctx, ln)
}

// serve answers requests on ln until ctx is done, then shuts the server down.
// It owns the goroutine that runs http.Server.Serve and waits for it.
func (s *Server) serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: readHeaderTimeout,
	}
	// TODO(step 7.2): the open event streams are closed with srv.RegisterOnShutdown.
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	s.logger.Info("server listening", "addr", ln.Addr().String())

	var err error
	select {
	case err = <-serveErr:
	case <-ctx.Done():
		s.logger.Info("server shutting down", "timeout", s.shutdownTimeout.String())
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
		defer cancel()
		if shutdownErr := srv.Shutdown(shutdownCtx); shutdownErr != nil {
			// The running requests did not end in time: drop them, and wait
			// for Serve so that no goroutine is left.
			if closeErr := srv.Close(); closeErr != nil {
				s.logger.Warn("closing the server failed", "err", closeErr)
			}
			<-serveErr
			return errors.NewErr(tagShutdownFailed, shutdownErr).SetMetadata("timeout", s.shutdownTimeout.String())
		}
		err = <-serveErr
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.NewErr(tagServeFailed, err).SetMetadata("addr", ln.Addr().String())
	}
	s.logger.Info("server stopped")
	return nil
}

// home answers GET /{$}.
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, views.Home())
}

// notFound answers with the 404 page.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, views.ErrorPage(http.StatusNotFound, messageNotFound))
}

// routeOrNotFound serves mux, except that a path no route matches in any
// method gets the 404 page. A path that matches a route in another method
// goes to the mux, which answers 405 with the Allow header.
func (s *Server) routeOrNotFound(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !matchesAnyMethod(mux, r) {
			s.notFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// matchesAnyMethod reports whether mux has a route for the request's path in
// its method or in any of the common ones.
func matchesAnyMethod(mux *http.ServeMux, r *http.Request) bool {
	if _, pattern := mux.Handler(r); pattern != "" {
		return true
	}
	other := r.WithContext(r.Context()) // a shallow copy, so r keeps its method
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		other.Method = m
		if _, pattern := mux.Handler(other); pattern != "" {
			return true
		}
	}
	return false
}

// render writes a templ component as an HTML page with the given status. The
// page is rendered to memory first, so a failing component does not leave a
// half-written answer.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	var buf bytes.Buffer
	if err := c.Render(r.Context(), &buf); err != nil {
		appErr := errors.NewErr(tagPageRenderFailed, err).SetMetadata("status", status)
		s.logger.Error("page render failed", "err", appErr)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write(buf.Bytes()); err != nil {
		// Usually the client went away; nothing more can be sent.
		appErr := errors.NewErr(tagResponseWriteFailed, err).SetMetadata("status", status)
		s.logger.Debug("response write failed", "err", appErr)
	}
}

const (
	tagListenFailed        = "ListenFailed"
	tagServeFailed         = "ServeFailed"
	tagShutdownFailed      = "ShutdownFailed"
	tagPageRenderFailed    = "PageRenderFailed"
	tagResponseWriteFailed = "ResponseWriteFailed"
)
