package web

import (
	"net/http"

	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
)

// maxFormBytes bounds the body of a form post.
const maxFormBytes = 64 << 10

// messageBadRequest is the page of a request that cannot be read.
const messageBadRequest = "That request could not be read"

// parseForm reads the form of a POST, at most maxFormBytes of it. When it
// cannot, it has answered with the 400 page and returns false.
func (s *Server) parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		appErr := errors.NewErr(tagFormInvalid, err)
		s.logger.Info("form rejected", "pattern", r.Pattern, "err", appErr)
		s.render(w, r, http.StatusBadRequest, views.ErrorPage(http.StatusBadRequest, messageBadRequest))
		return false
	}
	return true
}

// loginPage answers GET /login: the login form, or a redirect to the home page
// for a person who is logged in already. A "next" that is a path of this site
// is carried in the form.
func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := userFrom(r.Context()); ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	data := views.LoginData{}
	if raw := r.URL.Query().Get("next"); raw != "" && safeNext(raw) == raw {
		data.Next = raw
	}
	s.render(w, r, http.StatusOK, views.LoginPage(data))
}

// loginSubmit answers POST /login, sent by the login form with Datastar. On
// success it sets the session cookie and redirects to "next" (a path of this
// site) or to the home page. On failure it shows the problems in the form and
// keeps the username; the password is never sent back.
func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	username := r.PostFormValue("username")
	next := safeNext(r.PostFormValue("next"))

	token, user, err := s.svc.Accounts.Login(r.Context(), username, r.PostFormValue("password"), clientIP(r, s.cfg.TrustProxyHeaders))
	if err != nil {
		s.formFailed(w, r, err, func(problems views.FormErrors) formView {
			data := views.LoginData{Username: username, Errors: problems}
			if next != "/" {
				data.Next = next
			}
			return formView{fragment: views.LoginForm(data), page: views.LoginPage(data)}
		})
		return
	}
	stateOf(r.Context()).userID = user.ID
	s.setSessionCookie(w, token)
	s.redirect(w, r, next, http.StatusSeeOther)
}

// logout answers POST /logout: it deletes the session, clears the cookie and
// redirects to the login page.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if err := s.svc.Accounts.Logout(r.Context(), cookie.Value); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.clearSessionCookie(w)
	s.redirect(w, r, "/login", http.StatusSeeOther)
}

const tagFormInvalid = "FormInvalid"
