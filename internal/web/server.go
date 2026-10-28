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
	"github.com/rm4n0s/clowder/internal/api"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/markdown"
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
	// idleTimeout closes a connection that waited this long for its next
	// request.
	idleTimeout = 2 * time.Minute
	// maxHeaderBytes bounds the headers of a request (a session cookie, a
	// Referer and the usual few are far below it).
	maxHeaderBytes = 64 << 10
	// formReadTimeout is how long a form (or any body that is not an upload)
	// may take to arrive; uploadReadTimeout is the same for a multipart body.
	// The deadline is lifted when the body has been read, so that it never
	// cuts a request that is being answered. There is no ReadTimeout and no
	// WriteTimeout of the whole server: they would cut the event streams.
	formReadTimeout   = time.Minute
	uploadReadTimeout = 15 * time.Minute
	// streamWriteTimeout is how long one write to an event stream may take:
	// a client that stops reading is dropped, not waited for.
	streamWriteTimeout = 30 * time.Second
	// staticPrefix is the path of the embedded static files.
	staticPrefix = "/static/"
)

// Services are the services the handlers call. Later steps add a field for
// each service they build.
type Services struct {
	// Accounts is the service of login, sessions, invitations and users.
	Accounts *app.Accounts
	// Rooms is the service of rooms and their memberships.
	Rooms *app.Rooms
	// Audit is the service that reads the audit log.
	Audit *app.Audit
	// Posts is the service of posts, their tags and their feeds.
	Posts *app.Posts
	// Board is the service of the Kanban boards. The streams read it to find
	// the posts whose tasks a change of the columns concerns.
	Board *app.Board
	// Search is the service of search.
	Search *app.Search
	// Notifications is the service of the notifications of a user: their
	// unread count in the sidebar and their page.
	Notifications *app.Notifications
	// Messages is the service of private conversations and messages: their
	// pages, their streams and the unread count in the sidebar.
	Messages *app.Messages
	// Prefs is the service of the notification preferences of a user in a
	// room or a conversation, and of the notices their streams carry.
	Prefs *app.Prefs
	// Presence tells who is online and is told when a stream opens or closes
	// (SPEC §7.15). Nil counts nobody.
	Presence *app.Presence
	// Health answers the health check, GET /healthz (SPEC D40). Nil makes
	// the check answer 503.
	Health HealthChecker
	// API is the REST API (SPEC D42). Its routes are mounted on the same mux
	// under /api/v1/ and never read the session cookie. Nil serves no API:
	// every /api/ path is then the page-not-found error page.
	API *api.Server
	// Hub delivers the live events of the services to the streams of the
	// pages (SPEC §8). The services must publish to the same hub. Nil makes a
	// hub that nothing publishes to.
	Hub *live.Hub
}

// Server is the HTTP server of Clowder: the routes, the middleware and the
// handlers. Make it with New.
type Server struct {
	cfg    config.Config
	logger *slog.Logger
	svc    Services
	// md renders the bodies of the private messages (D33).
	md              *markdown.Renderer
	handler         http.Handler
	shutdownTimeout time.Duration
	// pingInterval is how often an open stream writes a comment line, so that
	// proxies keep the connection.
	pingInterval time.Duration
	// healthTimeout is healthTimeout; the tests shorten it.
	healthTimeout time.Duration
	// formTimeout, uploadTimeout and writeTimeout are formReadTimeout,
	// uploadReadTimeout and streamWriteTimeout; the tests shorten them.
	formTimeout, uploadTimeout, writeTimeout time.Duration
	// patterns are the route patterns of the server (without the extra routes
	// of the tests); a test goes through all of them.
	patterns []string
}

// access says what a route needs from the session of the request.
type access int

const (
	// requireSession is for the routes that need a login: a request without
	// one is sent to the login page. It is the zero value, so a route that
	// does not say otherwise is protected.
	requireSession access = iota
	// optionalSession is for the public pages that behave differently for a
	// logged-in user (the login page): the session is read, never required.
	optionalSession
	// noSession is for the routes that never look at the session (the static
	// files).
	noSession
)

// route is a pattern and its handler, added to the routes of a Server.
type route struct {
	pattern string
	handler http.Handler
	access  access
}

// New makes the server from its dependencies. Run starts it. It fails when the
// embedded static files cannot be read and hashed.
func New(cfg config.Config, logger *slog.Logger, svc Services) (*Server, error) {
	return newServer(cfg, logger, svc, static.FS)
}

// newServer is New with the static files as a parameter and extra routes,
// which the tests use to fail the hashing and to add a handler that panics or
// blocks.
func newServer(cfg config.Config, logger *slog.Logger, svc Services, staticFS fs.FS, extra ...route) (*Server, error) {
	if svc.Hub == nil {
		svc.Hub = live.New()
	}
	s := &Server{cfg: cfg, logger: logger, svc: svc, md: markdown.New(), shutdownTimeout: shutdownTimeout, pingInterval: pingInterval,
		formTimeout: formReadTimeout, uploadTimeout: uploadReadTimeout, writeTimeout: streamWriteTimeout, healthTimeout: healthTimeout}

	notFound := http.HandlerFunc(s.notFound)
	staticFiles, err := newStaticHandler(staticFS, staticPrefix, notFound)
	if err != nil {
		return nil, err
	}
	routes := []route{
		{pattern: "GET /{$}", handler: http.HandlerFunc(s.home)},
		{pattern: "GET /login", handler: http.HandlerFunc(s.loginPage), access: optionalSession},
		{pattern: "POST /login", handler: http.HandlerFunc(s.loginSubmit), access: optionalSession},
		{pattern: "POST /logout", handler: http.HandlerFunc(s.logout)},
		{pattern: "GET /invite/{token}", handler: http.HandlerFunc(s.signUpPage), access: optionalSession},
		{pattern: "POST /invite/{token}", handler: http.HandlerFunc(s.signUpSubmit), access: optionalSession},
		{pattern: "GET /reset/{token}", handler: http.HandlerFunc(s.resetPage), access: optionalSession},
		{pattern: "POST /reset/{token}", handler: http.HandlerFunc(s.resetSubmit), access: optionalSession},
		{pattern: "GET " + views.SessionsPath, handler: http.HandlerFunc(s.sessionsRedirect)},
		{pattern: "GET " + views.SettingsPath, handler: http.HandlerFunc(s.settingsPage)},
		{pattern: "POST " + views.SettingsProfilePath, handler: http.HandlerFunc(s.settingsProfile)},
		{pattern: "POST " + views.SettingsAvatarPath, handler: http.HandlerFunc(s.settingsAvatar)},
		{pattern: "POST " + views.SettingsAvatarRemovePath, handler: http.HandlerFunc(s.settingsAvatarRemove)},
		{pattern: "POST " + views.SettingsPasswordPath, handler: http.HandlerFunc(s.settingsPassword)},
		{pattern: "POST " + views.SettingsAppearancePath, handler: http.HandlerFunc(s.settingsAppearance)},
		{pattern: "POST " + views.SettingsNotificationsPath, handler: http.HandlerFunc(s.settingsNotifications)},
		{pattern: "GET /avatars/{userID}", handler: http.HandlerFunc(s.avatarFile)},
		{pattern: "POST /me/sessions/{id}/end", handler: http.HandlerFunc(s.sessionEnd)},
		{pattern: "GET /admin/users", handler: http.HandlerFunc(s.adminUsers)},
		{pattern: "POST /admin/users/{id}/role", handler: http.HandlerFunc(s.setRole)},
		{pattern: "POST /admin/users/{id}/disabled", handler: http.HandlerFunc(s.setDisabled)},
		{pattern: "POST /admin/users/{id}/reset-link", handler: http.HandlerFunc(s.resetLink)},
		{pattern: "POST /admin/invitations", handler: http.HandlerFunc(s.invite)},
		{pattern: "GET /admin/rooms", handler: http.HandlerFunc(s.adminRooms)},
		{pattern: "POST /admin/rooms/{id}/rename", handler: http.HandlerFunc(s.adminRoomRename)},
		{pattern: "POST /admin/rooms/{id}/delete", handler: http.HandlerFunc(s.adminRoomDelete)},
		{pattern: "GET /admin/rooms/{id}/members", handler: http.HandlerFunc(s.adminRoomMembers)},
		{pattern: "POST /admin/rooms/{id}/members/{userID}/remove", handler: http.HandlerFunc(s.adminMemberRemove)},
		{pattern: "GET /admin/audit", handler: http.HandlerFunc(s.adminAudit)},
		{pattern: "GET /rooms", handler: http.HandlerFunc(s.roomsBrowse)},
		{pattern: "GET /rooms/new", handler: http.HandlerFunc(s.roomNewPage)},
		{pattern: "POST /rooms", handler: http.HandlerFunc(s.roomCreate)},
		{pattern: "GET /rooms/{id}", handler: http.HandlerFunc(s.roomPage)},
		{pattern: "POST /rooms/{id}/join", handler: http.HandlerFunc(s.roomJoin)},
		{pattern: "POST /rooms/{id}/leave", handler: http.HandlerFunc(s.roomLeave)},
		{pattern: "POST /rooms/{id}/notifications", handler: http.HandlerFunc(s.roomPrefs)},
		{pattern: "POST /rooms/{id}/rename", handler: http.HandlerFunc(s.roomRename)},
		{pattern: "POST /rooms/{id}/delete", handler: http.HandlerFunc(s.roomDelete)},
		{pattern: "POST /rooms/{id}/members", handler: http.HandlerFunc(s.memberAdd)},
		{pattern: "POST /rooms/{id}/members/{userID}/remove", handler: http.HandlerFunc(s.memberRemove)},
		{pattern: "GET /rooms/{id}/feed", handler: http.HandlerFunc(s.roomFeed)},
		{pattern: "GET /rooms/{id}/stream", handler: http.HandlerFunc(s.roomStream)},
		{pattern: "GET /rooms/{id}/board", handler: http.HandlerFunc(s.boardPage)},
		{pattern: "GET /rooms/{id}/board/columns", handler: http.HandlerFunc(s.boardColumns)},
		{pattern: "GET /rooms/{id}/board/stream", handler: http.HandlerFunc(s.boardStream)},
		{pattern: "GET /rooms/{id}/board/tasks/{taskID}/edit", handler: http.HandlerFunc(s.boardTaskEdit)},
		{pattern: "POST /rooms/{id}/board/tasks/{taskID}", handler: http.HandlerFunc(s.boardTaskUpdate)},
		{pattern: "POST /rooms/{id}/board/tasks/{taskID}/move", handler: http.HandlerFunc(s.boardTaskMove)},
		{pattern: "POST /rooms/{id}/board/columns", handler: http.HandlerFunc(s.boardColumnAdd)},
		{pattern: "POST /rooms/{id}/board/columns/{columnID}/rename", handler: http.HandlerFunc(s.boardColumnRename)},
		{pattern: "POST /rooms/{id}/board/columns/{columnID}/move", handler: http.HandlerFunc(s.boardColumnMove)},
		{pattern: "POST /rooms/{id}/board/columns/{columnID}/delete", handler: http.HandlerFunc(s.boardColumnDelete)},
		{pattern: "POST /rooms/{id}/posts", handler: http.HandlerFunc(s.postCreate)},
		{pattern: "GET /posts/{id}", handler: http.HandlerFunc(s.postPage)},
		{pattern: "GET /posts/{id}/edit", handler: http.HandlerFunc(s.postEditForm)},
		{pattern: "POST /posts/{id}/edit", handler: http.HandlerFunc(s.postEditSave)},
		{pattern: "GET /posts/{id}/view", handler: http.HandlerFunc(s.postView)},
		{pattern: "GET /posts/{id}/stream", handler: http.HandlerFunc(s.postStream)},
		{pattern: "POST /posts/{id}/delete", handler: http.HandlerFunc(s.postDelete)},
		{pattern: "GET /posts/{id}/reply", handler: http.HandlerFunc(s.postReplyForm)},
		{pattern: "POST /posts/{id}/replies", handler: http.HandlerFunc(s.postReply)},
		{pattern: "POST /posts/{id}/reactions", handler: http.HandlerFunc(s.postReact)},
		{pattern: "POST /posts/{id}/follow", handler: http.HandlerFunc(s.postFollow)},
		{pattern: "POST /posts/{id}/unfollow", handler: http.HandlerFunc(s.postUnfollow)},
		{pattern: "POST /preview", handler: http.HandlerFunc(s.postPreview)},
		{pattern: "GET /files/{id}/{name}", handler: http.HandlerFunc(s.fileDownload)},
		{pattern: "GET " + views.MessagesPath, handler: http.HandlerFunc(s.messagesPage)},
		{pattern: "POST " + views.MessagesPath, handler: http.HandlerFunc(s.conversationStart)},
		{pattern: "GET /messages/{id}", handler: http.HandlerFunc(s.conversationPage)},
		{pattern: "GET /messages/{id}/older", handler: http.HandlerFunc(s.conversationOlder)},
		{pattern: "GET /messages/{id}/stream", handler: http.HandlerFunc(s.conversationStream)},
		{pattern: "POST /messages/{id}", handler: http.HandlerFunc(s.messageSend)},
		{pattern: "POST /messages/{id}/members", handler: http.HandlerFunc(s.conversationAddMembers)},
		{pattern: "POST /messages/{id}/rename", handler: http.HandlerFunc(s.conversationRename)},
		{pattern: "POST /messages/{id}/leave", handler: http.HandlerFunc(s.conversationLeave)},
		{pattern: "POST /messages/{id}/notifications", handler: http.HandlerFunc(s.conversationPrefs)},
		{pattern: "GET /messages/m/{msgID}/view", handler: http.HandlerFunc(s.messageView)},
		{pattern: "GET /messages/m/{msgID}/edit", handler: http.HandlerFunc(s.messageEditForm)},
		{pattern: "POST /messages/m/{msgID}/edit", handler: http.HandlerFunc(s.messageEditSave)},
		{pattern: "POST /messages/m/{msgID}/delete", handler: http.HandlerFunc(s.messageDelete)},
		{pattern: "POST /messages/m/{msgID}/react", handler: http.HandlerFunc(s.messageReact)},
		{pattern: "GET /messages/m/{msgID}/reply", handler: http.HandlerFunc(s.messageReplySlot)},
		{pattern: "GET /messages/files/{id}/{name}", handler: http.HandlerFunc(s.messageFileDownload)},
		{pattern: "GET /search", handler: http.HandlerFunc(s.search)},
		{pattern: "GET /notifications", handler: http.HandlerFunc(s.notificationsPage)},
		{pattern: "POST /notifications/read", handler: http.HandlerFunc(s.notificationsMarkRead)},
		{pattern: "GET " + views.MeStreamPath, handler: http.HandlerFunc(s.meStream)},
		{pattern: "GET /tags/suggest", handler: http.HandlerFunc(s.tagSuggest)},
		{pattern: "GET /editor/task-row", handler: http.HandlerFunc(s.taskRowForm)},
		{pattern: healthPattern, handler: http.HandlerFunc(s.healthz), access: noSession},
		{pattern: "GET " + staticPrefix, handler: staticFiles, access: noSession},
	}
	mux := http.NewServeMux()
	for _, rt := range routes {
		s.patterns = append(s.patterns, rt.pattern)
	}
	if svc.API != nil {
		// The API checks its own bearer tokens: its routes never look at the
		// cookie of the pages. They are not in s.patterns, which lists the
		// routes of the pages.
		for _, rt := range svc.API.Routes() {
			routes = append(routes, route{pattern: rt.Pattern, handler: rt.Handler, access: noSession})
		}
	}
	for _, rt := range append(routes, extra...) {
		mux.Handle(rt.pattern, s.withAccess(rt.handler, rt.access))
	}

	// Outermost first: the security headers, logging, the deadline of the
	// body, panic recovery, cross-origin protection.
	h := s.routeOrNotFound(mux)
	h = http.NewCrossOriginProtection().Handler(h)
	h = s.recoverPanics(h)
	h = s.bodyDeadline(h)
	h = s.logRequests(h)
	h = s.secureHeaders(h)
	s.handler = h
	return s, nil
}

// Handler returns the whole handler stack: the security headers, request
// logging, panic recovery, cross-origin protection and the routes.
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

// httpServer makes the http.Server that serves the handler. It has a timeout
// for the headers and one for an idle connection; the bodies and the event
// streams have their deadlines per request (bodyDeadline, liveStream.arm),
// because a ReadTimeout or WriteTimeout of the server would end the streams.
func (s *Server) httpServer() *http.Server {
	return &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}

// serve answers requests on ln until ctx is done, then shuts the server down.
// It owns the goroutine that runs http.Server.Serve and waits for it.
func (s *Server) serve(ctx context.Context, ln net.Listener) error {
	srv := s.httpServer()
	// The open event streams never end by themselves: closing the hub ends
	// them (the WebSockets of the API too), so that Shutdown does not wait for
	// them.
	srv.RegisterOnShutdown(s.svc.Hub.Close)
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
		shutdownErr := srv.Shutdown(shutdownCtx)
		// The WebSockets of the API were hijacked, so Shutdown does not wait for
		// them: closing the hub makes each close itself with 1001, and the
		// process waits for that to be done.
		if s.svc.API != nil && !s.svc.API.Drain(shutdownCtx) {
			s.logger.Warn("websockets were still open when the shutdown timeout ended")
		}
		if shutdownErr != nil {
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

// home answers GET /{$}. The route needs a login.
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.sidebarFor(w, r, 0)
	if !ok {
		return
	}
	s.render(w, r, http.StatusOK, views.Home(sb))
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
	if err := c.Render(s.pageContext(r), &buf); err != nil {
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

// pageContext returns the context to render a page in: the one of the request
// with the theme of the person who gets it, which Layout writes as data-theme.
// A request without a login, and a read of the settings that fails, get the
// page without a theme, which the browser's own setting decides.
func (s *Server) pageContext(r *http.Request) context.Context {
	ctx := r.Context()
	actor, ok := actorFrom(ctx)
	if !ok {
		return ctx
	}
	settings, err := s.svc.Accounts.Settings(ctx, actor)
	if err != nil {
		s.logger.Debug("reading the theme failed", "pattern", r.Pattern, "err", err)
		return ctx
	}
	return views.WithTheme(ctx, string(settings.Theme))
}

const (
	tagListenFailed        = "ListenFailed"
	tagServeFailed         = "ServeFailed"
	tagShutdownFailed      = "ShutdownFailed"
	tagPageRenderFailed    = "PageRenderFailed"
	tagResponseWriteFailed = "ResponseWriteFailed"
)

// NoteUser records on the request that user userID made it, so that the
// request log names the user. It is the hook the API calls once a bearer token
// is accepted (api.Options.NoteUser). A request that did not go through the
// server's handler is not changed.
func NoteUser(r *http.Request, userID uint64) {
	if st, ok := r.Context().Value(requestStateKey{}).(*requestState); ok {
		st.userID = userID
	}
}

// ClientIPFunc returns the function that finds the address of the client of a
// request, for the login limiter: the connection's address, or with
// trustProxy the last address of X-Forwarded-For (see clientIP). It is what
// the API is given as api.Options.ClientIP.
func ClientIPFunc(trustProxy bool) func(*http.Request) string {
	return func(r *http.Request) string { return clientIP(r, trustProxy) }
}
