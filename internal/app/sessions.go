package app

import (
	"context"
	"time"

	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// touchInterval is how old the last use of a session must be before a request
// writes the new time: a session is not written on every request.
const touchInterval = time.Minute

// dummyPasswordHash is an argon2id hash of a random password nobody knows,
// with the parameters of auth.DefaultHasher. Login verifies against it when
// the username does not exist, so that the time of the answer does not tell
// whether it does. The result of that check is never used.
const dummyPasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$a+cndfTcWEFz1DFlJ6Mwfw$AC9fd1czLf/NwN6/zh1Vd01ke5XoN6h56jGoqq6sYc0"

// Login checks the username and the password of a person at the client
// address clientIP and starts a session. It returns the session token, which
// goes into the cookie and is never stored (only its SHA-256 is), and the user.
//
// The username is lower-cased and trimmed. A wrong password, an unknown
// username and a disabled user all fail the same way, with TagInvalidInput
// and the message under the field "form" (FieldForm), and cost the same time.
// After 5 failed attempts for the username or the address within 15 minutes
// it fails with TagRateLimited, before it looks at the password, until the
// window has passed. A successful login clears the failures of the username.
func (a *Accounts) Login(ctx context.Context, username, password, clientIP string) (string, User, error) {
	username = normalizeUsername(username)
	if !a.limiter.Allow(username, clientIP) {
		return "", User{}, errors.New(TagRateLimited, "too many failed login attempts",
			"username", limiterKey(username), "ip", clientIP)
	}

	user, err := a.st.UserByUsername(ctx, username)
	known := err == nil
	if err != nil {
		if appErr, ok := errors.FromError(err); !ok || appErr.Tag != store.TagNotFound {
			return "", User{}, err
		}
	}
	hash := dummyPasswordHash
	if known {
		hash = user.PasswordHash
	}
	match := false
	if len(password) <= passwordMaxBytes {
		match, err = a.verify(ctx, password, hash)
		if err != nil {
			return "", User{}, err
		}
	}
	if !known || !match || user.DisabledAt != nil {
		a.limiter.Fail(username, clientIP)
		return "", User{}, errors.New(TagInvalidInput, "wrong username or password",
			"fields", map[string]string{FieldForm: "Wrong username or password."})
	}

	token, err := auth.NewToken()
	if err != nil {
		return "", User{}, err
	}
	now := a.clk.Now().UTC()
	sess := &store.Session{
		TokenHash:  auth.HashToken(token),
		UserID:     user.ID,
		ExpiresAt:  now.Add(a.sessionLifetime),
		LastSeenAt: now,
	}
	// The expired sessions of the user go in the same transaction that makes
	// the new one.
	err = a.st.Tx(ctx, func(tx *store.Store) error {
		if err := tx.DeleteExpiredSessionsOfUser(ctx, user.ID, now); err != nil {
			return err
		}
		return tx.CreateSession(ctx, sess)
	})
	if err != nil {
		return "", User{}, err
	}
	a.limiter.Reset(username)
	return token, viewOf(user), nil
}

// Session is who a session cookie belongs to, as SessionActor returns it.
type Session struct {
	// Actor is the actor to pass to the services.
	Actor Actor
	// User is the logged-in user.
	User User
	// ExpiresAt is when the session ends, counting from its last use.
	ExpiresAt time.Time
	// Refreshed is true when this call moved the expiry (the last use was
	// more than a minute old): the cookie must be sent again with a new
	// Max-Age, so that it slides with the session.
	Refreshed bool
}

// SessionActor finds the session of the cookie token and returns who it
// belongs to. For an empty, unknown or expired token, and for a disabled user,
// it fails with TagUnauthenticated. A session lasts the configured lifetime
// from its last use: when the last use is more than a minute old, the use and
// the expiry are written again and Refreshed is true. A session that is found
// expired is deleted; if that fails, the store's error is returned as it is.
func (a *Accounts) SessionActor(ctx context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, errors.New(TagUnauthenticated, "no session token", "reason", "none")
	}
	tokenHash := auth.HashToken(token)
	sess, err := a.st.SessionByTokenHash(ctx, tokenHash)
	if err != nil {
		if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagNotFound {
			return Session{}, errors.New(TagUnauthenticated, "no such session", "reason", "unknown")
		}
		return Session{}, err
	}
	now := a.clk.Now().UTC()
	if !now.Before(sess.ExpiresAt) {
		if err := a.st.DeleteSessionByTokenHash(ctx, tokenHash); err != nil {
			return Session{}, err
		}
		return Session{}, errors.New(TagUnauthenticated, "the session expired",
			"reason", "expired", "sessionID", sess.ID)
	}
	row, err := a.st.UserByID(ctx, sess.UserID)
	if err != nil {
		return Session{}, err
	}
	if row.DisabledAt != nil {
		return Session{}, errors.New(TagUnauthenticated, "the user is disabled",
			"reason", "disabled", "userID", row.ID)
	}
	expires := sess.ExpiresAt
	refreshed := false
	if now.Sub(sess.LastSeenAt) > touchInterval {
		expires = now.Add(a.sessionLifetime)
		err := a.st.TouchSession(ctx, sess.ID, now, expires)
		if err != nil {
			// A session deleted since we read it (a logout) is no session.
			if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagNotFound {
				return Session{}, errors.New(TagUnauthenticated, "the session was deleted",
					"reason", "deleted", "sessionID", sess.ID)
			}
			return Session{}, err
		}
		refreshed = true
	}
	user := viewOf(row)
	return Session{
		Actor:     Actor{UserID: user.ID, Username: user.Username, Role: user.Role},
		User:      user,
		ExpiresAt: expires,
		Refreshed: refreshed,
	}, nil
}

// Logout deletes the session of the cookie token. An unknown token is not an
// error.
func (a *Accounts) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	tokenHash := auth.HashToken(token)
	sess, err := a.st.SessionByTokenHash(ctx, tokenHash)
	if err != nil {
		if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagNotFound {
			return nil
		}
		return err
	}
	if err := a.st.DeleteSessionByTokenHash(ctx, tokenHash); err != nil {
		return err
	}
	// The other pages that are open with this session notice at once.
	a.pub.Publish(live.Event{Topic: live.UserTopic(sess.UserID), Kind: live.KindUserChanged, UserID: sess.UserID})
	return nil
}
