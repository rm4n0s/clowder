package web

import (
	"net/http"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
)

// resetNotValid answers with the 404 page of a reset link that is used,
// expired, unknown or of a user who is disabled. The page does not say which.
func (s *Server) resetNotValid(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, views.ErrorPage(http.StatusNotFound, messageResetInvalid))
}

// resetPage answers GET /reset/{token}: the form for a new password when the
// link can be used, else the 404 page. A person who is logged in gets the form
// too.
func (s *Server) resetPage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if err := s.svc.Accounts.ResetLinkValid(r.Context(), token); err != nil {
		if isTag(err, app.TagNotFound) {
			s.resetNotValid(w, r)
			return
		}
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, views.ResetPage(views.ResetData{Token: token}))
}

// resetSubmit answers POST /reset/{token}, sent by the reset form with
// Datastar: it checks that the two passwords are the same, sets the new
// password, which ends every session of the user, and starts a new session in
// this browser (the cookie is replaced, and the session the browser had, if it
// belonged to somebody else, ends). It redirects to the home page. On failure
// it shows the problems next to the fields. A used, expired or unknown link
// sends the browser to the link's address, which says so.
func (s *Server) resetSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	token := r.PathValue("token")
	password := r.PostFormValue("password")

	var res app.LoginResult
	var err error
	if password != r.PostFormValue("passwordConfirm") {
		// Look at the link first: a link that cannot be used is not a mistake
		// in the form.
		if err = s.svc.Accounts.ResetLinkValid(r.Context(), token); err == nil {
			err = errors.New(app.TagInvalidInput, "the two passwords differ",
				"fields", map[string]string{"passwordConfirm": messagePasswordsDiffer})
		}
	} else {
		res, err = s.svc.Accounts.ResetPassword(r.Context(), app.ResetInput{
			Token:       token,
			NewPassword: password,
			ClientName:  r.UserAgent(),
		})
	}
	if err != nil {
		if isTag(err, app.TagNotFound) {
			if isDatastar(r) {
				s.redirect(w, r, r.URL.Path, http.StatusSeeOther)
				return
			}
			s.resetNotValid(w, r)
			return
		}
		s.formFailed(w, r, err, func(problems views.FormErrors) formView {
			data := views.ResetData{Token: token, Errors: problems}
			return formView{fragment: views.ResetForm(data), page: views.ResetPage(data)}
		})
		return
	}

	// The session the browser had is replaced: it ends, if the reset did not
	// end it already (it belonged to another user).
	if cookie, cookieErr := r.Cookie(sessionCookieName); cookieErr == nil {
		if err := s.svc.Accounts.Logout(r.Context(), cookie.Value); err != nil {
			s.logger.Warn("ending the replaced session failed", "pattern", r.Pattern, "err", err)
		}
	}
	stateOf(r.Context()).userID = res.User.ID
	s.setSessionCookie(w, res.Token)
	s.redirect(w, r, "/", http.StatusSeeOther)
}
