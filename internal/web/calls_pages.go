package web

import (
	"context"
	"net/http"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
)

// callsOn reports whether this server can start calls: the calls service is
// there and its media server runs.
func (s *Server) callsOn() bool {
	return s.svc.Calls != nil && s.svc.Calls.Enabled()
}

// callPanelOf makes the view of a call in progress for the user.
func callPanelOf(actor app.Actor, call app.Call) *views.CallPanelData {
	return &views.CallPanelData{
		CallID: call.ID, Video: call.Video, Active: call.State == app.CallActive, Incoming: call.Incoming,
		Other: call.Other(actor.UserID), Elapsed: call.Elapsed,
	}
}

// callData reads the calls of a conversation for the actor: the buttons that
// start one and the call in progress. A group conversation, and a server whose
// calls are off, have none (the zero value).
func (s *Server) callData(ctx context.Context, actor app.Actor, h app.ConversationHeader) (views.CallData, error) {
	if !s.callsOn() || h.Kind != app.ConversationDirect {
		return views.CallData{}, nil
	}
	d := views.CallData{ConversationID: h.ID, Enabled: true}
	call, ok, err := s.svc.Calls.Current(ctx, actor, h.ID)
	if err != nil {
		return views.CallData{}, err
	}
	if ok {
		d.Open = callPanelOf(actor, call)
	}
	return d, nil
}

// callOps are the patches of the calls of a conversation: the call area and
// the buttons that start a call.
func callOps(d views.CallData) []patchOp {
	return []patchOp{{component: views.CallArea(d)}, {component: views.CallButtons(d)}}
}

// ringingData reads the call that rings for the actor now, as the data of the
// dialog; nil when none does or calls are off. A failure to read it is
// returned.
func (s *Server) ringingData(ctx context.Context, actor app.Actor) (*views.CallInviteData, error) {
	if s.svc.Calls == nil {
		return nil, nil
	}
	inv, ok, err := s.svc.Calls.Ringing(ctx, actor)
	if err != nil || !ok {
		return nil, err
	}
	return views.CallInviteOf(inv), nil
}

// pageSidebar is sidebarData for the sidebar of a page being made: it also
// reads the call that rings for the user, which the dialog of the page shows
// at once.
func (s *Server) pageSidebar(ctx context.Context, actor app.Actor, user app.User, current uint64) (views.SidebarData, error) {
	sb, err := s.sidebarData(ctx, actor, user, current)
	if err != nil {
		return views.SidebarData{}, err
	}
	sb.Ringing, err = s.ringingData(ctx, actor)
	if err != nil {
		return views.SidebarData{}, err
	}
	return sb, nil
}

// isCallEvent reports whether the kind of an event is one of the calls.
func isCallEvent(kind string) bool {
	switch kind {
	case live.KindCallRinging, live.KindCallStarted, live.KindCallUpdated, live.KindCallEnded:
		return true
	}
	return false
}

// callDialog handles an event of the calls on the user's topic for the dialog
// of the page: a call that rings puts the dialog in its slot, and a call that
// was answered or ended takes it away. A call.updated changes nothing here.
func (ls *liveStream) callDialog(ctx context.Context, ev live.Event) error {
	if ls.s.svc.Calls == nil {
		return nil
	}
	return forgiven(ls.callDialogOf(ctx, ev))
}

// forgiven drops the failure of a user who is not served any more (disabled, or
// gone): the stream finds that out with its own check of the session, which
// sends the page to the login, and that must not be cut short by a read of the
// calls.
func forgiven(err error) error {
	if isTag(err, app.TagUnauthenticated) {
		return nil
	}
	return err
}

// callDialogOf is callDialog without the forgiveness.
func (ls *liveStream) callDialogOf(ctx context.Context, ev live.Event) error {
	switch ev.Kind {
	case live.KindCallRinging:
		inv, ok, err := ls.s.svc.Calls.Invitation(ctx, ls.actor, ev.CallID)
		if err != nil || !ok {
			return err
		}
		return ls.patch(views.CallRingingSlot(views.CallInviteOf(inv)))
	case live.KindCallStarted:
		// The call started to ring (the dialog came with call.ringing and
		// stays) or was answered (the dialog of the callee goes). The caller
		// has no dialog.
		call, err := ls.s.svc.Calls.Get(ctx, ls.actor, ev.CallID)
		if err != nil {
			return err
		}
		if !call.Incoming || call.State == app.CallRinging {
			return nil
		}
		return ls.patch(views.CallRingingSlot(nil))
	case live.KindCallEnded:
		return ls.patch(views.CallRingingSlot(nil))
	}
	return nil
}

// ringingSync puts the dialog of the call that rings for the user in its slot,
// or empties the slot when none does: a stream that opens again does not know
// what it missed. A first connection sends it only when a call rings, because
// the page was made a moment ago.
func (ls *liveStream) ringingSync(ctx context.Context) error {
	inv, err := ls.s.ringingData(ctx, ls.actor)
	if err != nil {
		return forgiven(err)
	}
	if inv == nil && !ls.reconnect {
		return nil
	}
	ls.arm()
	return ls.patch(views.CallRingingSlot(inv))
}

// callEvent handles an event of the calls in the stream of a conversation
// page: the dialog, and for the calls of this conversation the call area and
// the buttons.
func (cs *conversationStream) callEvent(ctx context.Context, ev live.Event) error {
	if err := cs.callDialog(ctx, ev); err != nil {
		return err
	}
	if ev.Kind == live.KindCallUpdated || ev.ConversationID != cs.id {
		return nil
	}
	return cs.callArea(ctx)
}

// callArea sends the call area and the buttons as the conversation has them
// now. A conversation without calls sends nothing.
func (cs *conversationStream) callArea(ctx context.Context) error {
	h, err := cs.s.svc.Messages.Conversation(ctx, cs.actor, cs.id)
	if err != nil {
		_, err = cs.goneTo(err)
		return err
	}
	d, err := cs.s.callData(ctx, cs.actor, h)
	if err != nil || !d.Enabled {
		return forgiven(err)
	}
	for _, op := range callOps(d) {
		if err := cs.patch(op.component, op.options...); err != nil {
			return err
		}
	}
	return nil
}

// callTarget reads the conversation of a call route and the form of the
// request. When it fails it has answered.
func (s *Server) callTarget(w http.ResponseWriter, r *http.Request) (id uint64, ok bool) {
	id, ok = s.conversationID(w, r)
	if !ok {
		return 0, false
	}
	if !s.parseForm(w, r) {
		return 0, false
	}
	if s.svc.Calls == nil {
		s.callFailed(w, r, errors.New(app.TagCallsOff, "this server has no calls service",
			"fields", map[string]string{app.FieldForm: "Calls are turned off on this server."}), id)
		return 0, false
	}
	return id, true
}

// callFailed shows why a call action failed: a conversation that is gone or
// hidden is the 404 page, and a refusal of the calls is the message for
// people in the header of the conversation, where the buttons are.
func (s *Server) callFailed(w http.ResponseWriter, r *http.Request, err error, convID uint64) {
	if isTag(err, app.TagNotFound) || isTag(err, app.TagUnauthenticated) {
		s.conversationFail(w, r, err, convID)
		return
	}
	actor, _ := actorFrom(r.Context())
	h, readErr := s.svc.Messages.Conversation(r.Context(), actor, convID)
	if readErr != nil {
		s.conversationFail(w, r, readErr, convID)
		return
	}
	s.conversationNotice(w, r, err, h)
}

// callAnswered answers a call action that went through: a Datastar request
// gets the call area and the buttons as the conversation has them now, a plain
// post is sent to the conversation.
func (s *Server) callAnswered(w http.ResponseWriter, r *http.Request, convID uint64, extra ...patchOp) {
	if !isDatastar(r) {
		http.Redirect(w, r, views.ConversationPath(convID), http.StatusSeeOther)
		return
	}
	actor, _ := actorFrom(r.Context())
	h, err := s.svc.Messages.Conversation(r.Context(), actor, convID)
	if err != nil {
		s.conversationFail(w, r, err, convID)
		return
	}
	d, err := s.callData(r.Context(), actor, h)
	if err != nil {
		s.callFailed(w, r, err, convID)
		return
	}
	s.patchAll(w, r, append(callOps(d), extra...)...)
}

// callStart answers POST /messages/{id}/call with the form value "video"
// ("true" for a video call): the user calls the other member of the direct
// conversation. The page gets "Calling Bob…" (the call area) in place of the
// buttons. A group conversation, a busy user, a disabled callee and a server
// whose calls are off are the message for people in the header, with status
// 409 for a plain post. Anyone but a member gets the 404 page.
func (s *Server) callStart(w http.ResponseWriter, r *http.Request) {
	id, ok := s.callTarget(w, r)
	if !ok {
		return
	}
	actor, _ := actorFrom(r.Context())
	if _, err := s.svc.Calls.Start(r.Context(), actor, id, r.PostFormValue("video") == "true"); err != nil {
		s.callFailed(w, r, err, id)
		return
	}
	s.callAnswered(w, r, id)
}

// callAnswer answers POST /messages/{id}/call/answer with the form value
// "video" ("true" to send video): the callee answers the ringing call. The
// person is sent to the conversation, which opens the call
// (views.CallAnsweredPath), from any page. The caller gets 403.
func (s *Server) callAnswer(w http.ResponseWriter, r *http.Request) {
	id, ok := s.callTarget(w, r)
	if !ok {
		return
	}
	actor, _ := actorFrom(r.Context())
	video := r.PostFormValue("video") == "true"
	err := s.actOnCall(r.Context(), actor, id, func(call app.Call) error {
		_, err := s.svc.Calls.Answer(r.Context(), actor, call.ID, video)
		return err
	})
	if err != nil {
		s.callFailed(w, r, err, id)
		return
	}
	s.redirect(w, r, views.CallAnsweredPath(id, video), http.StatusSeeOther)
}

// callDecline answers POST /messages/{id}/call/decline: the callee refuses the
// ringing call. A Datastar request gets the dialog taken away.
func (s *Server) callDecline(w http.ResponseWriter, r *http.Request) {
	id, ok := s.callTarget(w, r)
	if !ok {
		return
	}
	actor, _ := actorFrom(r.Context())
	err := s.actOnCall(r.Context(), actor, id, func(call app.Call) error {
		_, err := s.svc.Calls.Decline(r.Context(), actor, call.ID)
		return err
	})
	if err != nil {
		s.callFailed(w, r, err, id)
		return
	}
	if !isDatastar(r) {
		http.Redirect(w, r, views.ConversationPath(id), http.StatusSeeOther)
		return
	}
	s.patchAll(w, r, patchOp{component: views.CallRingingSlot(nil)})
}

// callCancel answers POST /messages/{id}/call/cancel: the caller ends the
// ringing call. The callee gets 403.
func (s *Server) callCancel(w http.ResponseWriter, r *http.Request) {
	id, ok := s.callTarget(w, r)
	if !ok {
		return
	}
	actor, _ := actorFrom(r.Context())
	err := s.actOnCall(r.Context(), actor, id, func(call app.Call) error {
		_, err := s.svc.Calls.Cancel(r.Context(), actor, call.ID)
		return err
	})
	if err != nil {
		s.callFailed(w, r, err, id)
		return
	}
	s.callAnswered(w, r, id)
}

// callHangUp answers POST /messages/{id}/call/hangup: either side ends the
// active call.
func (s *Server) callHangUp(w http.ResponseWriter, r *http.Request) {
	id, ok := s.callTarget(w, r)
	if !ok {
		return
	}
	actor, _ := actorFrom(r.Context())
	err := s.actOnCall(r.Context(), actor, id, func(call app.Call) error {
		_, err := s.svc.Calls.HangUp(r.Context(), actor, call.ID)
		return err
	})
	if err != nil {
		s.callFailed(w, r, err, id)
		return
	}
	s.callAnswered(w, r, id)
}

// actOnCall finds the call in progress of the conversation and applies act to
// it, as the routes of the API do.
func (s *Server) actOnCall(ctx context.Context, actor app.Actor, convID uint64, act func(call app.Call) error) error {
	call, err := s.svc.Calls.Open(ctx, actor, convID)
	if err != nil {
		return err
	}
	return act(call)
}
