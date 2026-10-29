package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/coder/websocket"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/calls"
	"github.com/rm4n0s/errors"
)

// callRequest is the body of POST /conversations/{id}/call and of
// POST /conversations/{id}/call/answer: whether the user takes part with
// video.
type callRequest struct {
	Video bool `json:"video"`
}

// callRoutes are the routes of the calls (SPEC §7.18).
func (s *Server) callRoutes() []Route {
	return []Route{
		s.route(http.MethodGet, "/conversations/{id}/call", false, s.currentCall),
		s.route(http.MethodPost, "/conversations/{id}/call", false, s.startCall),
		s.route(http.MethodPost, "/conversations/{id}/call/answer", false, s.answerCall),
		s.route(http.MethodPost, "/conversations/{id}/call/decline", false, s.declineCall),
		s.route(http.MethodPost, "/conversations/{id}/call/cancel", false, s.cancelCall),
		s.route(http.MethodPost, "/conversations/{id}/call/hangup", false, s.hangUpCall),
		s.route(http.MethodGet, "/conversations/{id}/call/ws", false, s.callSocket),
	}
}

// currentCall answers GET /conversations/{id}/call: the call in progress of a
// direct conversation. The conversation of two others, a conversation that
// does not exist and a conversation with no call in progress are 404; a
// group conversation is 409.
func (s *Server) currentCall(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	call, ok, err := s.svc.Calls.Current(r.Context(), c.actor, id)
	if err == nil && !ok {
		err = errors.New(app.TagNotFound, "the conversation has no call in progress", "conversationID", id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, callOf(call))
}

// startCall answers POST /conversations/{id}/call: the caller starts a call
// with {"video": true} or audio only, and the other user's pages and clients
// ring. It is 201 with the call and a Location. A group conversation, a busy
// user (either), a disabled callee, a server whose calls are off or full are
// 409 conflict with the reason in the message.
func (s *Server) startCall(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var body callRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	call, err := s.svc.Calls.Start(r.Context(), c.actor, id, body.Video)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Location", Prefix+"/conversations/"+strconv.FormatUint(id, 10)+"/call")
	s.writeJSON(w, r, http.StatusCreated, callOf(call))
}

// answerCall answers POST /conversations/{id}/call/answer: the callee answers
// the ringing call, with video or audio only. 200 with the call. The caller
// gets 403; a call that is not ringing, or none, is 409.
func (s *Server) answerCall(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var body callRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.actOnCall(w, r, c.actor, id, func(ctx context.Context, call app.Call) (app.Call, error) {
		return s.svc.Calls.Answer(ctx, c.actor, call.ID, body.Video)
	})
}

// declineCall answers POST /conversations/{id}/call/decline: the callee
// refuses the ringing call.
func (s *Server) declineCall(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.actOnCall(w, r, c.actor, id, func(ctx context.Context, call app.Call) (app.Call, error) {
		return s.svc.Calls.Decline(ctx, c.actor, call.ID)
	})
}

// cancelCall answers POST /conversations/{id}/call/cancel: the caller ends the
// ringing call.
func (s *Server) cancelCall(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.actOnCall(w, r, c.actor, id, func(ctx context.Context, call app.Call) (app.Call, error) {
		return s.svc.Calls.Cancel(ctx, c.actor, call.ID)
	})
}

// hangUpCall answers POST /conversations/{id}/call/hangup: either side ends
// the active call.
func (s *Server) hangUpCall(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.actOnCall(w, r, c.actor, id, func(ctx context.Context, call app.Call) (app.Call, error) {
		return s.svc.Calls.HangUp(ctx, c.actor, call.ID)
	})
}

// actOnCall finds the call in progress of the conversation and applies act to
// it; the answer is the call as it is then.
func (s *Server) actOnCall(w http.ResponseWriter, r *http.Request, actor app.Actor, convID uint64,
	act func(ctx context.Context, call app.Call) (app.Call, error)) {
	call, err := s.svc.Calls.Open(r.Context(), actor, convID)
	if err == nil {
		call, err = act(r.Context(), call)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, callOf(call))
}

// callSocket answers GET /conversations/{id}/call/ws: the signalling socket of
// the call (SPEC §7.18), with the bearer token checked before the upgrade like
// the user's WebSocket. The caller may join while the call rings or is active,
// the callee once they answered; anyone else gets 404 before the upgrade, a
// group conversation or a user who may not join yet 409. The messages are the
// schemas CallClientMessage and CallServerMessage of openapi.json.
func (s *Server) callSocket(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	token, err := bearerToken(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// Counted among the live connections of the user, like the user's socket
	// (SPEC D62), before the call is joined.
	release, err := s.svc.Connections.Acquire(c.user.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer release()
	call, err := s.svc.Calls.Join(r.Context(), c.actor, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if s.svc.Media == nil {
		s.writeError(w, r, errors.New(app.TagCallsOff, "this server has no media server",
			"fields", map[string]string{app.FieldForm: "Calls are turned off on this server."}))
		return
	}
	// Counted from here, so that Drain also waits for an upgrade that is under
	// way.
	s.socketOpened()
	defer s.socketClosed()
	conn, err := s.accept(w, r)
	if err != nil {
		// The library has answered the request.
		s.logger.Debug("call socket refused", "pattern", r.Pattern, "user_id", c.user.ID, "err", err)
		return
	}
	s.svc.Media.Serve(context.WithoutCancel(r.Context()), conn, calls.Join{
		CallID: call.ID, ConversationID: call.ConversationID, CallerID: call.Caller.ID, CalleeID: call.Callee.ID,
		UserID: c.user.ID,
		Home:   call.Home,
		Session: func(ctx context.Context) (websocket.StatusCode, string) {
			sess, err := s.svc.Accounts.APISessionActor(ctx, token)
			switch {
			case err == nil && sess.Actor.Role == c.actor.Role:
				return 0, ""
			case err == nil:
				return closeSessionChanged, reasonSessionChanged
			case isTag(err, app.TagUnauthenticated):
				return closeSessionEnded, reasonSessionEnded
			default:
				return 0, ""
			}
		},
	})
}
