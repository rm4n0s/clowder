package web

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/errors"
)

// sessionCookieName is the name of the cookie that holds the session token
// (SPEC D11).
const sessionCookieName = "clowder_session"

// sessionKey is the context key of the *session of a logged-in request.
type sessionKey struct{}

// session is who a logged-in request belongs to.
type session struct {
	actor app.Actor
	user  app.User
}

// withSession returns ctx with the actor and the user in it.
func withSession(ctx context.Context, actor app.Actor, user app.User) context.Context {
	return context.WithValue(ctx, sessionKey{}, &session{actor: actor, user: user})
}

// actorFrom returns the actor of the request, or false when nobody is logged
// in. Routes that need a login can rely on it.
func actorFrom(ctx context.Context) (app.Actor, bool) {
	sess, ok := ctx.Value(sessionKey{}).(*session)
	if !ok {
		return app.Actor{}, false
	}
	return sess.actor, true
}

// userFrom returns the logged-in user of the request, or false when nobody is
// logged in.
func userFrom(ctx context.Context) (app.User, bool) {
	sess, ok := ctx.Value(sessionKey{}).(*session)
	if !ok {
		return app.User{}, false
	}
	return sess.user, true
}

// withAccess wraps h in what its route needs from the session:
//
//   - requireSession: the session is read; a request without a valid one is
//     sent to the login page (an SSE redirect for a Datastar request);
//   - optionalSession: the session is read when there is one; the handler
//     checks userFrom;
//   - noSession: h runs as it is.
//
// A cookie that holds no valid session is cleared.
func (s *Server) withAccess(h http.Handler, a access) http.Handler {
	if a == noSession {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			sess, err := s.svc.Accounts.SessionActor(r.Context(), cookie.Value)
			switch {
			case err == nil:
				stateOf(r.Context()).userID = sess.User.ID
				if sess.Refreshed {
					// The session slid: so does the cookie. A handler that
					// ends the session (logout) clears it after this.
					s.setSessionCookie(w, cookie.Value)
				}
				h.ServeHTTP(w, r.WithContext(withSession(r.Context(), sess.Actor, sess.User)))
				return
			case isTag(err, app.TagUnauthenticated):
				s.clearSessionCookie(w)
			default:
				s.fail(w, r, err)
				return
			}
		}
		if a == requireSession {
			s.redirectToLogin(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// setSessionCookie sends the cookie of a new session: HttpOnly, SameSite=Lax,
// for the whole site, Secure as the configuration says, and as long-lived as
// the session.
func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(s.cfg.SessionLifetime / time.Second),
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookie tells the browser to forget the session cookie.
func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
}

// redirectToLogin sends a request without a session to the login page. A page
// request gets a 302 to /login?next=<its path>, so that the login brings the
// person back; only a GET or HEAD comes back to where it was. A Datastar
// request gets an SSE redirect, with the page it was sent from (the Referer)
// as next.
func (s *Server) redirectToLogin(w http.ResponseWriter, r *http.Request) {
	next := ""
	if isDatastar(r) {
		next = refererPath(r)
	} else if r.Method == http.MethodGet || r.Method == http.MethodHead {
		next = r.URL.RequestURI()
	}
	target := "/login"
	if next != "" && safeNext(next) == next {
		target += "?next=" + url.QueryEscape(next)
	}
	s.redirect(w, r, target, http.StatusFound)
}

// refererPath returns the path and query of the Referer when it names this
// same host, else "".
func refererPath(r *http.Request) string {
	ref, err := url.Parse(r.Referer())
	if err != nil || ref.Host != r.Host {
		return ""
	}
	return ref.RequestURI()
}

// safeNext returns next when it is a path on this site, and "/" otherwise
// (an empty value, another site, a scheme, a protocol-relative "//host", a
// backslash, a control character). It is what keeps the login from being an
// open redirect.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, `\`) {
		return "/"
	}
	for _, c := range next {
		if c < ' ' || c == 0x7f {
			return "/"
		}
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	return next
}

// clientIP returns the address of the client of the request, for the login
// limiter. It is the host of the connection's address, unless trustProxy says
// that a reverse proxy of ours stands in front: then it is the last address of
// X-Forwarded-For, the one the proxy appended itself (the ones before it are
// whatever the client claimed). A missing or malformed header falls back to the
// connection's address.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if values := r.Header.Values("X-Forwarded-For"); len(values) > 0 {
			last := values[len(values)-1]
			if i := strings.LastIndexByte(last, ','); i >= 0 {
				last = last[i+1:]
			}
			if addr, err := netip.ParseAddr(strings.TrimSpace(last)); err == nil {
				return addr.Unmap().String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isDatastar reports whether the request comes from the Datastar client.
func isDatastar(r *http.Request) bool {
	return r.Header.Get("Datastar-Request") == "true"
}

// isTag reports whether err is a tagged error with the tag.
func isTag(err error, tag string) bool {
	appErr, ok := errors.FromError(err)
	return ok && appErr.Tag == tag
}
