package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/errors"
)

// caller is who a request with a valid bearer token belongs to.
type caller struct {
	actor app.Actor
	user  app.User
	// sessionID is the ID of the session of the token.
	sessionID uint64
}

// callerKey is the context key of the *caller.
type callerKey struct{}

// callerFrom returns the caller of the request. A handler behind
// authenticated can rely on it.
func callerFrom(ctx context.Context) (*caller, bool) {
	c, ok := ctx.Value(callerKey{}).(*caller)
	return c, ok
}

// bearerToken returns the token of the Authorization header ("Bearer <token>";
// the scheme is case-insensitive). A missing header, another scheme and an
// empty token fail with tagBearerInvalid. The session cookie is never read.
func bearerToken(r *http.Request) (string, error) {
	scheme, token, found := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	token = strings.TrimSpace(token)
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", errors.New(tagBearerInvalid, "no bearer token in the Authorization header")
	}
	return token, nil
}

// authenticated wraps next in the check of the bearer token: the session of
// kind api that the token names must be live and its user enabled. Anything
// else is 401 unauthenticated with WWW-Authenticate. It never reads the
// session cookie, so a page's cookie cannot be used across sites through the
// API (SPEC D43).
func (s *Server) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := bearerToken(r)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		sess, err := s.svc.Accounts.APISessionActor(r.Context(), token)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		s.noteUser(r, sess.User.ID)
		c := &caller{actor: sess.Actor, user: sess.User, sessionID: sess.SessionID}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, c)))
	})
}

// pathID reads the number in the path value name. Anything that is not a
// positive number fails with tagIDInvalid, which is a 404: no resource has
// such an ID.
func pathID(r *http.Request, name string) (uint64, error) {
	id, err := strconv.ParseUint(r.PathValue(name), 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New(tagIDInvalid, "the ID in the path is not a number", "name", name)
	}
	return id, nil
}

const (
	tagBearerInvalid = "BearerInvalid"
	tagIDInvalid     = "IDInvalid"
)
