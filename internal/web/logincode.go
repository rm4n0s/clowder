package web

import (
	"net/http"
	"net/url"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/web/views"
)

// pendingCookieName is the name of the cookie that holds the token of a login
// that waits for its SSH code (SPEC §7.16, D53).
const pendingCookieName = "clowder_pending"

// pendingCookiePath is the path of the pending cookie: the second form of the
// login and its "Start again" link, and the login form (which replaces it).
const pendingCookiePath = "/login"

// setPendingCookie sends the cookie of a pending login: HttpOnly,
// SameSite=Strict, for the login pages only, Secure as the session cookie is,
// and as long-lived as the pending login.
func (s *Server) setPendingCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     pendingCookieName,
		Value:    token,
		Path:     pendingCookiePath,
		MaxAge:   int(app.PendingLoginLifetime / time.Second),
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies(),
		SameSite: http.SameSiteStrictMode,
	})
}

// clearPendingCookie tells the browser to forget the pending cookie.
func (s *Server) clearPendingCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     pendingCookieName,
		Value:    "",
		Path:     pendingCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies(),
		SameSite: http.SameSiteStrictMode,
	})
}

// pendingToken returns the token of the pending cookie, "" when there is none.
func pendingToken(r *http.Request) string {
	cookie, err := r.Cookie(pendingCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// loginExpiredPath returns the login page that says the sign-in has expired,
// with the path next to go to after the next login.
func loginExpiredPath(next string) string {
	target := "/login?expired=1"
	if next != "" && next != "/" {
		target += "&next=" + url.QueryEscape(next)
	}
	return target
}

// loginCodePage answers GET /login/code: the second form of the login, which
// asks for the SSH code. Without a valid pending cookie the person is sent to
// the login page (to the one that says the sign-in has expired, when the
// cookie was there and the pending login is gone); a person who is logged in
// goes home.
func (s *Server) loginCodePage(w http.ResponseWriter, r *http.Request) {
	if _, ok := userFrom(r.Context()); ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	token := pendingToken(r)
	if token == "" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	info, err := s.svc.Accounts.PendingLoginInfo(r.Context(), token)
	if err != nil {
		if isTag(err, app.TagUnauthenticated) {
			s.clearPendingCookie(w)
			http.Redirect(w, r, loginExpiredPath(""), http.StatusFound)
			return
		}
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, views.LoginCodePage(views.LoginCodeData{Command: s.svc.SSHKeys.Server(info.Username).Command}))
}

// loginCodeSubmit answers POST /login/code, sent by the second form with
// Datastar. A right code starts the session: it sets the session cookie,
// clears the pending cookie and redirects to the path the login was going to.
// A wrong code shows the problem next to the field and keeps the form; a login
// that is gone (expired, five wrong codes, a changed password, a disabled
// user) sends the person to the login page, which says so.
func (s *Server) loginCodeSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	token := pendingToken(r)
	if token == "" {
		s.redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	info, err := s.svc.Accounts.PendingLoginInfo(r.Context(), token)
	if err != nil {
		s.loginCodeFailed(w, r, err, "", nil)
		return
	}
	res, err := s.svc.Accounts.CompleteLogin(r.Context(), token, r.PostFormValue("code"), clientIP(r, s.cfg.TrustProxyHeaders), r.UserAgent())
	if err != nil {
		s.loginCodeFailed(w, r, err, info.Next, &info)
		return
	}
	stateOf(r.Context()).userID = res.User.ID
	s.setSessionCookie(w, res.Token)
	s.clearPendingCookie(w)
	s.redirect(w, r, safeNext(res.Next), http.StatusSeeOther)
}

// loginCodeFailed answers a failed submit of the second form. A login that is
// gone clears the pending cookie and goes back to the login page; any other
// failure shows its problem in the form.
func (s *Server) loginCodeFailed(w http.ResponseWriter, r *http.Request, err error, next string, info *app.PendingLogin) {
	if isTag(err, app.TagUnauthenticated) {
		s.clearPendingCookie(w)
		s.redirect(w, r, loginExpiredPath(safeNext(next)), http.StatusSeeOther)
		return
	}
	command := ""
	if info != nil {
		command = s.svc.SSHKeys.Server(info.Username).Command
	}
	s.formFailed(w, r, err, func(problems views.FormErrors) formView {
		data := views.LoginCodeData{Command: command, Errors: problems}
		return formView{fragment: views.LoginCodeForm(data), page: views.LoginCodePage(data)}
	})
}

// loginCodeRestart answers GET /login/code/restart, the "Start again" link of
// the second form: it deletes the pending login, clears the cookie and goes
// back to the login page.
func (s *Server) loginCodeRestart(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.Accounts.CancelPendingLogin(r.Context(), pendingToken(r)); err != nil {
		s.logger.Warn("cancelling a pending login failed", "pattern", r.Pattern, "err", err)
	}
	s.clearPendingCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
