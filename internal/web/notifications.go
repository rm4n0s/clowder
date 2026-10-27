package web

import (
	"net/http"

	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
	"github.com/starfederation/datastar-go/datastar"
)

// notificationsPage answers GET /notifications?before=<id>: the notifications
// of the person, newest first, 30 at a time, each with the room and a link to
// what it is about (SPEC §7.9, D30). before is the ID the page starts after; a
// missing or wrong value starts at the newest. The notifications that the page
// shows are marked read after they are read, but they are shown as they were:
// the ones that were unread keep their mark on this page. The sidebar shows
// the count after the marking. A new notification is put on top of the list by
// the stream of the page (see meStream).
func (s *Server) notificationsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor, _ := actorFrom(ctx)
	before := beforeOf(r)
	page, err := s.svc.Notifications.List(ctx, actor, before, 0)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := views.NotificationsData{
		Items: page.Notifications, Before: before, HasNext: page.HasNext, NextBefore: page.NextBefore,
	}
	if newest := d.NewestID(); newest != 0 {
		if err := s.svc.Notifications.MarkRead(ctx, actor, newest); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	// The sidebar is read after the marking, so that its count is the one
	// after it.
	sb, ok := s.sidebarFor(w, r, 0)
	if !ok {
		return
	}
	d.Sidebar = sb
	s.render(w, r, http.StatusOK, views.NotificationsPage(d))
}

// notificationsMarkRead answers POST /notifications/read: every notification
// of the person up to the newest is marked read. A Datastar request gets the
// signal readUpTo set to that ID, which clears the unread marks of the page,
// and the new counts; a plain form is sent back to the page.
func (s *Server) notificationsMarkRead(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor, _ := actorFrom(ctx)
	var newest uint64
	page, err := s.svc.Notifications.List(ctx, actor, 0, 1)
	if err == nil && len(page.Notifications) > 0 {
		newest = page.Notifications[0].ID
		err = s.svc.Notifications.MarkRead(ctx, actor, newest)
	}
	if err != nil {
		s.markReadFailed(w, r, err)
		return
	}
	if !isDatastar(r) {
		http.Redirect(w, r, views.NotificationsPath, http.StatusSeeOther)
		return
	}
	ops, err := s.countOps(ctx, actor)
	if err != nil {
		s.markReadFailed(w, r, err)
		return
	}
	sse := datastar.NewSSE(w, r)
	if err := sse.MarshalAndPatchSignals(map[string]uint64{"readUpTo": newest}); err != nil {
		s.logger.Debug("sse write failed", "err", errors.NewErr(tagSSEWriteFailed, err).SetMetadata("event", "patch-signals"))
		return
	}
	for _, op := range ops {
		if err := sse.PatchElementTempl(op.component, op.options...); err != nil {
			s.logger.Debug("sse write failed", "err", errors.NewErr(tagSSEWriteFailed, err).SetMetadata("event", "patch-elements"))
			return
		}
	}
}

// markReadFailed answers a failed "Mark all as read". A login that ended goes
// to the login page; any other failure is logged and, for a Datastar request,
// shown on the page with the status 200 (the client drops the body of any
// other status); a plain form gets the error page.
func (s *Server) markReadFailed(w http.ResponseWriter, r *http.Request, err error) {
	if !isDatastar(r) || statusOf(err) == http.StatusFound {
		s.fail(w, r, err)
		return
	}
	s.logFailure(r, err)
	s.patch(w, r, views.NotificationsMessage(messageTryAgain))
}
