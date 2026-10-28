package web

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/calls"
	"github.com/rm4n0s/errors"
)

// CallSocketPath is the pattern of the path of the signalling socket of the
// call in a direct conversation, GET /calls/{id}/ws with the ID of the
// conversation (SPEC §7.18).
const CallSocketPath = "/calls/{id}/ws"

// Close codes of the signalling socket, as in the API (SPEC D44).
const (
	callCloseSessionEnded   websocket.StatusCode = calls.CloseSessionEnded
	callCloseSessionChanged websocket.StatusCode = calls.CloseSessionChanged
)

// callSocket answers GET /calls/{id}/ws: the signalling socket of the page
// that takes part in the call of a direct conversation (SPEC §7.18). The
// session cookie has been checked by the route; the library checks the
// Origin of the handshake, so that a page of another site cannot open the
// socket. The caller may join while the call rings or is active, the callee
// once they answered. Anyone else gets the 404 page before the upgrade, and a
// group conversation, calls that are off or a callee who has not answered 409
// (the error page of a conflict). The messages are those of the API's socket
// (docs/API.md).
func (s *Server) callSocket(w http.ResponseWriter, r *http.Request) {
	id, ok := s.conversationID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	actor, _ := actorFrom(ctx)
	user, _ := userFrom(ctx)
	if s.svc.Calls == nil {
		s.fail(w, r, errors.New(app.TagCallsOff, "this server has no calls service",
			"fields", map[string]string{app.FieldForm: "Calls are turned off on this server."}))
		return
	}
	call, err := s.svc.Calls.Join(ctx, actor, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if s.svc.Media == nil {
		s.fail(w, r, errors.New(app.TagCallsOff, "this server has no media server",
			"fields", map[string]string{app.FieldForm: "Calls are turned off on this server."}))
		return
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		s.redirectToLogin(w, r)
		return
	}
	token := cookie.Value
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		// The library has answered the request.
		s.logger.Debug("call socket refused", "pattern", r.Pattern, "user_id", user.ID, "err", err)
		return
	}
	s.svc.Media.Serve(context.WithoutCancel(ctx), conn, calls.Join{
		CallID: call.ID, ConversationID: call.ConversationID, CallerID: call.Caller.ID, CalleeID: call.Callee.ID,
		UserID: user.ID,
		Home:   call.Home,
		Session: func(ctx context.Context) (websocket.StatusCode, string) {
			sess, err := s.svc.Accounts.SessionActor(ctx, token)
			switch {
			case err == nil && sess.Actor.Role == actor.Role:
				return 0, ""
			case err == nil:
				return callCloseSessionChanged, "session changed"
			case isTag(err, app.TagUnauthenticated):
				return callCloseSessionEnded, "session ended"
			default:
				return 0, ""
			}
		},
	})
}
