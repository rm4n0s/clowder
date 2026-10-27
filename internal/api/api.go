package api

import (
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/errors"
)

const (
	// Prefix is the path every route of the API starts with.
	Prefix = "/api/v1"
	// catchAllPattern is the pattern of the route that answers every path of
	// the API that no other route has.
	catchAllPattern = "/api/"
)

// Services are the services the handlers call. Later steps add a field for
// each service they need.
type Services struct {
	// Accounts is the service of login, sessions, invitations and users.
	Accounts *app.Accounts
	// Notifications is the service of notifications, for their unread count.
	Notifications *app.Notifications
	// Messages is the service of private messages, for the unread count.
	Messages *app.Messages
	// Rooms is the service of rooms and their members.
	Rooms *app.Rooms
	// Posts is the service of posts, comments, reactions, tags and uploads.
	Posts *app.Posts
	// Board is the service of the boards, their columns and their cards.
	Board *app.Board
	// Search is the service of search.
	Search *app.Search
}

// Options are the parts a Server is made of.
type Options struct {
	// Services are the services the handlers call.
	Services Services
	// Logger receives the log entries of the API; nil discards them. An error
	// is logged as the attribute "err"; a token, a password or a body never
	// is.
	Logger *slog.Logger
	// NoteUser is called with the request and the ID of the user once a
	// bearer token has been accepted, so that the request log of the caller
	// can name the user. Nil does nothing.
	NoteUser func(r *http.Request, userID uint64)
	// ClientIP returns the address of the client of a request, for the login
	// limiter. Nil uses the host of the connection's address; a server behind
	// a reverse proxy passes its own function.
	ClientIP func(r *http.Request) string
	// MaxUploadSize is the largest file POST /uploads takes, in bytes; it
	// must be the limit the posts service was made with. 0 means the default
	// (config.DefaultMaxAttachmentSize). The body of an upload request may be
	// a megabyte more, for the framing of the form.
	MaxUploadSize int64
}

// Route is a pattern of http.ServeMux, with the method and the whole path
// (POST /api/v1/auth/login), and its handler. The server of internal/web
// registers each Route on its mux.
type Route struct {
	// Pattern is the pattern of the route.
	Pattern string
	// Handler answers the requests that match Pattern.
	Handler http.Handler
}

// Server is the REST API. Make it with New and mount its Routes.
type Server struct {
	svc      Services
	logger   *slog.Logger
	noteUser func(r *http.Request, userID uint64)
	clientIP func(r *http.Request) string
	// maxUploadBody is the cap on the body of an upload request.
	maxUploadBody int64
	routes        []Route
	// lookup holds the routes without the catch-all, to find the methods a
	// path has.
	lookup *http.ServeMux
}

// New makes the API from its options. It fails when the services of Options
// are not all there.
func New(opts Options) (*Server, error) {
	svc := opts.Services
	if svc.Accounts == nil || svc.Notifications == nil || svc.Messages == nil || svc.Rooms == nil || svc.Posts == nil ||
		svc.Board == nil || svc.Search == nil {
		return nil, errors.New(tagServicesMissing, "the API needs the accounts, notifications, messages, rooms, posts, board and search services")
	}
	s := &Server{svc: opts.Services, logger: opts.Logger, noteUser: opts.NoteUser, clientIP: opts.ClientIP}
	maxUpload := opts.MaxUploadSize
	if maxUpload <= 0 {
		maxUpload = config.DefaultMaxAttachmentSize
	}
	s.maxUploadBody = maxUpload + uploadBodySlack
	if s.logger == nil {
		s.logger = slog.New(slog.DiscardHandler)
	}
	if s.noteUser == nil {
		s.noteUser = func(*http.Request, uint64) {}
	}
	if s.clientIP == nil {
		s.clientIP = remoteHost
	}
	s.lookup = http.NewServeMux()
	for _, rt := range s.defineRoutes() {
		s.lookup.Handle(rt.Pattern, rt.Handler)
		s.routes = append(s.routes, rt)
	}
	s.routes = append(s.routes, Route{Pattern: catchAllPattern, Handler: s.recovering(s.unknownRoute)})
	return s, nil
}

// Routes returns every route of the API, the catch-all last. The caller
// registers them on its mux; it must not change the list.
func (s *Server) Routes() []Route {
	return slices.Clone(s.routes)
}

// route makes a Route for a path under Prefix. A handler that needs a login
// is wrapped in the check of the bearer token.
func (s *Server) route(method, path string, public bool, h func(http.ResponseWriter, *http.Request)) Route {
	var handler http.Handler = http.HandlerFunc(h)
	if !public {
		handler = s.authenticated(handler)
	}
	return Route{Pattern: method + " " + Prefix + path, Handler: s.recovering(handler.ServeHTTP)}
}

// defineRoutes lists the routes of the API. A later step adds the routes of
// its group by appending the result of its own method here.
func (s *Server) defineRoutes() []Route {
	var routes []Route
	routes = append(routes, s.authRoutes()...)
	routes = append(routes, s.meRoutes()...)
	routes = append(routes, s.userRoutes()...)
	routes = append(routes, s.roomRoutes()...)
	routes = append(routes, s.postRoutes()...)
	routes = append(routes, s.uploadRoutes()...)
	routes = append(routes, s.boardRoutes()...)
	routes = append(routes, s.searchRoutes()...)
	routes = append(routes, s.notificationRoutes()...)
	routes = append(routes, s.documentRoutes()...)
	return routes
}

// unknownRoute answers a request that matched no route of the API: 405 with
// the Allow header when the path has the route in other methods, else 404,
// both as JSON.
func (s *Server) unknownRoute(w http.ResponseWriter, r *http.Request) {
	var allowed []string
	probe := r.WithContext(r.Context()) // a shallow copy, so r keeps its method
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		probe.Method = m
		if _, pattern := s.lookup.Handler(probe); pattern != "" {
			allowed = append(allowed, m)
			if m == http.MethodGet {
				allowed = append(allowed, http.MethodHead)
			}
		}
	}
	if len(allowed) > 0 {
		s.writeError(w, r, errors.New(tagMethodNotAllowed, "the path has no route in this method",
			"allow", strings.Join(allowed, ", "), "method", r.Method))
		return
	}
	s.writeError(w, r, errors.New(tagRouteNotFound, "the API has no such path"))
}

// remoteHost is the host of the connection's address.
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

const (
	tagServicesMissing  = "ServicesMissing"
	tagMethodNotAllowed = "MethodNotAllowed"
	tagRouteNotFound    = "RouteNotFound"
)
