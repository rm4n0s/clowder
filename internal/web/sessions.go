package web

import (
	"net/http"
	"strconv"

	"github.com/rm4n0s/clowder/internal/web/views"
)

// sessionsPage answers GET /me/sessions: the live sessions of the person,
// browsers and apps, with the one that shows the page marked (SPEC D43).
func (s *Server) sessionsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor, _ := actorFrom(ctx)
	list, err := s.svc.Accounts.Sessions(ctx, actor, sessionIDFrom(ctx))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	sb, ok := s.sidebarFor(w, r, 0)
	if !ok {
		return
	}
	s.render(w, r, http.StatusOK, views.SessionsPage(views.SessionsData{Sidebar: sb, Sessions: list}))
}

// sessionEnd answers POST /me/sessions/{id}/end: it ends one session of the
// person and sends them back to the list. A session that is not theirs, or is
// gone, is a 404. Ending the session that shows the page logs it out: the
// cookie is cleared and the person goes to the login page.
func (s *Server) sessionEnd(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor, _ := actorFrom(ctx)
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 63)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := s.svc.Accounts.EndSession(ctx, actor, id); err != nil {
		s.fail(w, r, err)
		return
	}
	if id == sessionIDFrom(ctx) {
		s.clearSessionCookie(w)
		s.redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.redirect(w, r, views.SessionsPath, http.StatusSeeOther)
}
