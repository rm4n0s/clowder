package web

import (
	"net/http"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

// invitationNotValid answers with the 404 page of an invitation that is used,
// expired or unknown. The page does not say which.
func (s *Server) invitationNotValid(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, views.ErrorPage(http.StatusNotFound, messageInvitationInvalid))
}

// signUpPage answers GET /invite/{token}: the sign-up form for a valid
// invitation, else the 404 page.
func (s *Server) signUpPage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if err := s.svc.Accounts.InvitationValid(r.Context(), token); err != nil {
		if isTag(err, app.TagNotFound) {
			s.invitationNotValid(w, r)
			return
		}
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, views.SignUpPage(views.SignUpData{Token: token}))
}

// signUpSubmit answers POST /invite/{token}, sent by the sign-up form with
// Datastar: it makes the user, logs the same person in with the same
// credentials, sets the session cookie and redirects to the home page. On
// failure it shows the problems next to the fields and keeps the username and
// the display name. A used, expired or unknown invitation sends the browser to
// the invitation page, which says so.
func (s *Server) signUpSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	token := r.PathValue("token")
	username := r.PostFormValue("username")
	displayName := r.PostFormValue("displayName")
	password := r.PostFormValue("password")

	_, err := s.svc.Accounts.SignUp(r.Context(), token, username, displayName, password)
	if err != nil {
		if isTag(err, app.TagNotFound) {
			if isDatastar(r) {
				s.redirect(w, r, r.URL.Path, http.StatusSeeOther)
				return
			}
			s.invitationNotValid(w, r)
			return
		}
		s.formFailed(w, r, err, func(problems views.FormErrors) formView {
			data := views.SignUpData{Token: token, Username: username, DisplayName: displayName, Errors: problems}
			return formView{fragment: views.SignUpForm(data), page: views.SignUpPage(data)}
		})
		return
	}

	login, err := s.svc.Accounts.LoginClient(r.Context(), s.browserLogin(r, username, password))
	if err != nil {
		// The account exists; only the login after it failed. The person can
		// log in by hand.
		s.logger.Warn("login after sign-up failed", "pattern", r.Pattern, "err", err)
		s.redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	stateOf(r.Context()).userID = login.User.ID
	s.setSessionCookie(w, login.Token)
	s.redirect(w, r, "/", http.StatusSeeOther)
}
