package web

import (
	"io"
	"net/http"
	"strconv"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/errors"
)

// messageTooManyConnections is the text of the 429 that refuses a stream or a
// socket past the limit of the live connections of a user (SPEC D62).
const messageTooManyConnections = "Too many pages are open. Close some and try again."

// limitConnections wraps the handler of an event stream or of a signalling
// socket: it counts the request among the live connections of the user in this
// process for as long as the handler runs, and past app.MaxConnectionsPerUser
// answers 429 with Retry-After before the handler starts (SPEC D62). The
// count is given back when the handler returns, whatever the way it ends. The
// page's Datastar stream retries a 429 with a growing wait (1 s, 2 s, 4 s, up
// to 30 s), so a user over the limit does not hit the server in a tight loop.
// A request without a user is not counted.
func (s *Server) limitConnections(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, _ := actorFrom(r.Context())
		release, err := s.svc.Connections.Acquire(actor.UserID)
		if err != nil {
			s.connectionsRefused(w, r)
			return
		}
		defer release()
		next(w, r)
	}
}

// connectionsRefused answers the request that the limit of live connections
// refused: 429, Retry-After and a line of plain text. It is not a page: a
// stream drops the body of any status but 200.
func (s *Server) connectionsRefused(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", strconv.Itoa(app.ConnectionsRetryAfterSeconds))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusTooManyRequests)
	if _, err := io.WriteString(w, messageTooManyConnections+"\n"); err != nil {
		// Usually the client went away; nothing more can be sent.
		appErr := errors.NewErr(tagResponseWriteFailed, err).SetMetadata("status", http.StatusTooManyRequests)
		s.logger.Debug("response write failed", "pattern", r.Pattern, "err", appErr)
	}
}
