package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
)

// Texts shown above the users table when an action on a user fails. They are
// fixed text, never the text of an error.
const (
	// messageLastAdmin is the answer to a change that would leave the
	// installation without an active admin.
	messageLastAdmin = "There must always be one active admin. Make someone else an admin first."
	// messageUserGone is the answer to an action on a user who does not exist.
	messageUserGone = "That user does not exist any more. Reload the page."
	// messageActionInvalid is the answer to an action with a value that is not
	// valid and that the service gives no message for.
	messageActionInvalid = "That change is not valid."
)

// adminOnly returns the actor and the user of an admin request. For anyone
// else it has answered with the 404 page (admin pages are invisible to
// members) and returns false.
func (s *Server) adminOnly(w http.ResponseWriter, r *http.Request) (app.Actor, app.User, bool) {
	actor, okActor := actorFrom(r.Context())
	user, okUser := userFrom(r.Context())
	if !okActor || !okUser || !actor.IsAdmin() {
		s.notFound(w, r)
		return app.Actor{}, app.User{}, false
	}
	return actor, user, true
}

// usersData reads the users and makes the data of the admin page. It has
// answered with the error page and returns false when the read fails.
func (s *Server) usersData(w http.ResponseWriter, r *http.Request, actor app.Actor, viewer app.User, invite views.InviteData) (views.AdminUsersData, bool) {
	users, err := s.svc.Accounts.Users(r.Context(), actor)
	if err != nil {
		s.fail(w, r, err)
		return views.AdminUsersData{}, false
	}
	sb, ok := s.sidebarFor(w, r, 0)
	if !ok {
		return views.AdminUsersData{}, false
	}
	if invite.Role == "" {
		invite.Role = string(app.RoleMember)
	}
	return views.AdminUsersData{Viewer: viewer, Sidebar: sb, Users: users, Invite: invite}, true
}

// adminUsers answers GET /admin/users: the users and the invitation form.
func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request) {
	actor, viewer, ok := s.adminOnly(w, r)
	if !ok {
		return
	}
	data, ok := s.usersData(w, r, actor, viewer, views.InviteData{})
	if !ok {
		return
	}
	s.render(w, r, http.StatusOK, views.AdminUsersPage(data))
}

// userChange answers the POSTs that change one user (role and disabled): it
// checks that the request is an admin's, reads the form and the ID of the
// user, and calls change. On success a Datastar request gets the table patched
// and a plain request is sent back to the page; on failure the problem is
// shown above the table.
func (s *Server) userChange(w http.ResponseWriter, r *http.Request, change func(actor app.Actor, id uint64) error) {
	actor, viewer, ok := s.adminOnly(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 63)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if !s.parseForm(w, r) {
		return
	}
	if err := change(actor, id); err != nil {
		s.userChangeFailed(w, r, err, actor, viewer)
		return
	}
	if !isDatastar(r) {
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}
	data, ok := s.usersData(w, r, actor, viewer, views.InviteData{})
	if !ok {
		return
	}
	s.patch(w, r, views.UsersPanel(data))
}

// setRole answers POST /admin/users/{id}/role with the form value "role".
func (s *Server) setRole(w http.ResponseWriter, r *http.Request) {
	s.userChange(w, r, func(actor app.Actor, id uint64) error {
		return s.svc.Accounts.SetRole(r.Context(), actor, id, app.Role(r.PostFormValue("role")))
	})
}

// setDisabled answers POST /admin/users/{id}/disabled with the form value
// "disabled" ("true" or "false").
func (s *Server) setDisabled(w http.ResponseWriter, r *http.Request) {
	s.userChange(w, r, func(actor app.Actor, id uint64) error {
		switch r.PostFormValue("disabled") {
		case "true":
			return s.svc.Accounts.SetDisabled(r.Context(), actor, id, true)
		case "false":
			return s.svc.Accounts.SetDisabled(r.Context(), actor, id, false)
		default:
			return errors.New(app.TagInvalidInput, "the disabled value is not true or false",
				"fields", map[string]string{app.FieldForm: messageActionInvalid})
		}
	})
}

// userChangeFailed shows the problem of a failed action above the table. A
// Datastar request gets the table as a patch with status 200 (the client drops
// the body of any other status); a plain request gets the whole page with the
// status of statusOf. TagUnauthenticated redirects to the login page.
func (s *Server) userChangeFailed(w http.ResponseWriter, r *http.Request, err error, actor app.Actor, viewer app.User) {
	status := statusOf(err)
	var message string
	switch {
	case status == http.StatusFound:
		s.redirectToLogin(w, r)
		return
	case isTag(err, app.TagInvalidInput):
		problems := formErrorsOf(err)
		message = firstNonEmpty(problems.Fields[app.FieldUser], problems.Fields[app.FieldRole], problems.Form, messageActionInvalid)
	case isTag(err, app.TagConflict):
		message = messageLastAdmin
	case isTag(err, app.TagNotFound):
		message = messageUserGone
	case isTag(err, app.TagForbidden):
		message = messageForbidden
	default:
		s.logFailure(r, err)
		message = messageTryAgain
	}
	data, ok := s.usersData(w, r, actor, viewer, views.InviteData{})
	if !ok {
		return
	}
	data.Message = message
	if isDatastar(r) {
		s.patch(w, r, views.UsersPanel(data))
		return
	}
	s.render(w, r, status, views.AdminUsersPage(data))
}

// firstNonEmpty returns the first of the values that is not empty.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// invite answers POST /admin/invitations with the form value "role": it makes
// an invitation and shows its link, once. A Datastar request gets the
// invitation section patched; a plain request gets the whole page with the
// link. A failure shows the problems in the form.
func (s *Server) invite(w http.ResponseWriter, r *http.Request) {
	actor, viewer, ok := s.adminOnly(w, r)
	if !ok {
		return
	}
	if !s.parseForm(w, r) {
		return
	}
	role := r.PostFormValue("role")
	inv, err := s.svc.Accounts.CreateInvitation(r.Context(), actor, app.Role(role))
	if err != nil {
		// A plain post gets the whole page back, so it needs the users and
		// the sidebar.
		var base views.AdminUsersData
		if !isDatastar(r) && !isTag(err, app.TagUnauthenticated) {
			var ok bool
			if base, ok = s.usersData(w, r, actor, viewer, views.InviteData{}); !ok {
				return
			}
		}
		s.formFailed(w, r, err, func(problems views.FormErrors) formView {
			invite := views.InviteData{Role: role, Errors: problems}
			return formView{
				fragment: views.InviteSection(invite),
				page:     views.AdminUsersPage(views.AdminUsersData{Viewer: viewer, Sidebar: base.Sidebar, Users: base.Users, Invite: invite}),
			}
		})
		return
	}
	invite := views.InviteData{
		Role:      string(inv.Role),
		Link:      s.invitationLink(inv.Token),
		ExpiresAt: inv.ExpiresAt,
	}
	if isDatastar(r) {
		s.patch(w, r, views.InviteSection(invite))
		return
	}
	data, ok := s.usersData(w, r, actor, viewer, invite)
	if !ok {
		return
	}
	s.render(w, r, http.StatusOK, views.AdminUsersPage(data))
}

// invitationLink returns the link of an invitation: <base URL>/invite/<token>.
func (s *Server) invitationLink(token string) string {
	return strings.TrimRight(s.cfg.BaseURL, "/") + "/invite/" + url.PathEscape(token)
}
