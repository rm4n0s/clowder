package app

import (
	"context"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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

// SessionKind says where a session was made and where it is accepted: a
// browser session belongs to the pages and travels in the cookie, an API
// session to the API and travels in the Authorization header (SPEC D43). It is
// the store's type.
type SessionKind = store.SessionKind

// The two kinds of sessions.
const (
	// SessionBrowser is the kind of the sessions made by the login and
	// sign-up pages.
	SessionBrowser SessionKind = store.SessionBrowser
	// SessionAPI is the kind of the sessions made by the login of the API.
	SessionAPI SessionKind = store.SessionAPI
)

// MaxClientNameRunes is the longest name of a client that a session keeps, in
// characters.
const MaxClientNameRunes = store.MaxClientNameRunes

// ClientLogin is what a login names besides the username and the password.
type ClientLogin struct {
	// Username is the username as typed; it is lower-cased and trimmed.
	Username string
	// Password is the password as typed.
	Password string
	// ClientIP is the address of the client, for the login limiter.
	ClientIP string
	// Kind is the kind of the session to make: SessionBrowser for the pages,
	// SessionAPI for the API.
	Kind SessionKind
	// ClientName names the client. For SessionAPI it is required: 1 to 80
	// characters after trimming, without control characters. For
	// SessionBrowser it is the user agent of the browser, which is cut to 80
	// characters and cleaned of control characters, never refused; it may be
	// empty.
	ClientName string
	// SSHCode is the sign-in code of a user who turned the SSH code on, for
	// SessionAPI (the API logs in with the code in one call). A browser login
	// ignores it: the pages ask for the code in a second step
	// (CompleteLogin).
	SSHCode string
	// Next is the local path a browser goes to after the login, kept with a
	// pending login for CompleteLogin; the web layer has checked it.
	Next string
}

// LoginResult is what a successful login returns, or, with Pending set, what a
// login that waits for the SSH code returns instead of a session.
type LoginResult struct {
	// Token is the session token, shown once and never stored (only its
	// SHA-256 is). It is empty when Pending is set.
	Token string
	// User is the user who logged in.
	User User
	// ExpiresAt is when the session ends if it is not used again.
	ExpiresAt time.Time
	// Pending is set when the password was right and the user has to type an
	// SSH code before a session starts (SPEC §7.16): no session exists yet.
	// Only a SessionBrowser login gets it; the API login asks for the code in
	// the same call.
	Pending *PendingLogin
	// Next is the path stored with the pending login that CompleteLogin
	// finished; empty otherwise and for the home page.
	Next string
}

// PendingLogin is a login that waits for the SSH code (SPEC §7.16, D53).
type PendingLogin struct {
	// Token is the secret of the cookie clowder_pending. It is shown once and
	// never stored (only its SHA-256 is); PendingLoginInfo leaves it empty.
	Token string
	// Username is the username of the user, for the ssh command shown.
	Username string
	// ExpiresAt is when the pending login stops working.
	ExpiresAt time.Time
	// Next is the local path to go to after the login; empty for the home
	// page.
	Next string
}

// Login is LoginClient for a browser that sends no name: a SessionBrowser
// session with an empty client name. It returns the result of LoginClient: a
// session, or, for a user who has to type an SSH code, a pending login.
func (a *Accounts) Login(ctx context.Context, username, password, clientIP string) (LoginResult, error) {
	return a.LoginClient(ctx, ClientLogin{Username: username, Password: password, ClientIP: clientIP, Kind: SessionBrowser})
}

// CleanClientName returns the client name a session of the kind keeps, or
// fails. For SessionAPI the name is trimmed and must have 1 to 80 characters
// and no control character, else it fails with TagInvalidInput (field
// "clientName"). For SessionBrowser, the user agent, it is cleaned of control
// characters and bytes that are not UTF-8, trimmed and cut to 80 characters;
// it never fails, and may come back empty. Any other kind fails with
// TagInvalidInput (field "form").
func CleanClientName(kind SessionKind, name string) (string, error) {
	switch kind {
	case SessionBrowser:
		var b strings.Builder
		n := 0
		for _, r := range name {
			if r == utf8.RuneError || unicode.IsControl(r) {
				continue
			}
			if n == MaxClientNameRunes {
				break
			}
			b.WriteRune(r)
			n++
		}
		return strings.TrimSpace(b.String()), nil
	case SessionAPI:
		name = strings.TrimSpace(name)
		if n := utf8.RuneCountInString(name); n < 1 || n > MaxClientNameRunes ||
			strings.ContainsFunc(name, unicode.IsControl) || !utf8.ValidString(name) {
			return "", errors.New(TagInvalidInput, "the client name is not valid",
				"fields", map[string]string{FieldClientName: "Use 1 to 80 characters, without control characters."})
		}
		return name, nil
	default:
		return "", errors.New(TagInvalidInput, "the kind of session does not exist",
			"fields", map[string]string{FieldForm: "Choose browser or api."}, "kind", string(kind))
	}
}

// LoginClient checks the username and the password of a person at the client
// address and starts a session of the kind and the client name given. It
// returns the session token, which goes into the cookie or the Authorization
// header and is never stored (only its SHA-256 is), the user and when the
// session ends.
//
// An unknown kind, and for SessionAPI a client name that breaks its rules (see
// CleanClientName), fail with TagInvalidInput before the limiter and the
// password are looked at. The username is lower-cased and trimmed. A wrong
// password, an unknown username and a disabled user all fail the same way,
// with TagInvalidInput and the message under the field "form" (FieldForm), and
// cost the same time. After 5 failed attempts for the username or the address
// within 15 minutes it fails with TagRateLimited, before it looks at the
// password, until the window has passed. A successful login clears the
// failures of the username.
//
// A user who turned on the SSH code is asked for it when this process runs
// the SSH server (AccountsConfig.SSHServer), and only after the right password
// (SPEC §7.16, D53). A SessionBrowser login then makes a pending login and no
// session: the result has Pending set, and CompleteLogin finishes it. A
// SessionAPI login takes the code in in.SSHCode: without one it fails with
// TagSSHCodeRequired, with a wrong, used, expired or foreign one with
// TagUnauthenticated and a failure in the limiter, and with a right one it
// uses the code up and starts the session in one transaction. Nothing but the
// right password and the right code logs anyone in.
func (a *Accounts) LoginClient(ctx context.Context, in ClientLogin) (LoginResult, error) {
	clientName, err := CleanClientName(in.Kind, in.ClientName)
	if err != nil {
		return LoginResult{}, err
	}
	username := normalizeUsername(in.Username)
	clientIP := in.ClientIP
	if !a.limiter.Allow(ctx, username, clientIP) {
		return LoginResult{}, errors.New(TagRateLimited, "too many failed login attempts",
			"username", limiterKey(username), "ip", clientIP)
	}

	user, err := a.st.UserByUsername(ctx, username)
	known := err == nil
	if err != nil {
		if appErr, ok := errors.FromError(err); !ok || appErr.Tag != store.TagNotFound {
			return LoginResult{}, err
		}
	}
	hash := dummyPasswordHash
	if known {
		hash = user.PasswordHash
	}
	match := false
	if len(in.Password) <= passwordMaxBytes {
		match, err = a.verify(ctx, in.Password, hash)
		if err != nil {
			return LoginResult{}, err
		}
	}
	if !known || !match || user.DisabledAt != nil {
		a.limiter.Fail(ctx, username, clientIP)
		return LoginResult{}, errors.New(TagInvalidInput, "wrong username or password",
			"fields", map[string]string{FieldForm: "Wrong username or password."})
	}

	needsCode, err := a.needsSSHCode(ctx, user.ID)
	if err != nil {
		return LoginResult{}, err
	}
	if needsCode && in.Kind == SessionBrowser {
		return a.startPendingLogin(ctx, user, in.Next)
	}
	if needsCode && in.SSHCode == "" {
		return LoginResult{}, errors.New(TagSSHCodeRequired, "the user needs an SSH sign-in code",
			"userID", user.ID)
	}

	sess, token, err := newSession(user.ID, in.Kind, clientName, a.clk.Now().UTC(), a.sessionLifetime)
	if err != nil {
		return LoginResult{}, err
	}
	// The expired sessions of the user go in the same transaction that makes
	// the new one; so does the use of the code.
	wrongCode := false
	err = a.st.Tx(ctx, func(tx *store.Store) error {
		wrongCode = false
		if needsCode {
			if _, err := tx.ConsumeSSHCode(ctx, user.ID, SSHCodeHash(in.SSHCode), sess.LastSeenAt); err != nil {
				if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagNotFound {
					wrongCode = true
					return nil
				}
				return err
			}
		}
		if err := tx.DeleteExpiredSessionsOfUser(ctx, user.ID, sess.LastSeenAt); err != nil {
			return err
		}
		return tx.CreateSession(ctx, sess)
	})
	if err != nil {
		return LoginResult{}, err
	}
	if wrongCode {
		a.limiter.Fail(ctx, username, clientIP)
		return LoginResult{}, errors.New(TagUnauthenticated, "the SSH sign-in code is wrong",
			"reason", "sshCode", "userID", user.ID)
	}
	a.limiter.Reset(ctx, username)
	return LoginResult{Token: token, User: viewOf(user), ExpiresAt: sess.ExpiresAt}, nil
}

// newSession makes the row of a new session of the user, started at now, and
// its token, which is shown once and never stored.
func newSession(userID uint64, kind SessionKind, clientName string, now time.Time, lifetime time.Duration) (*store.Session, string, error) {
	token, err := auth.NewToken()
	if err != nil {
		return nil, "", err
	}
	return &store.Session{
		TokenHash:  auth.HashToken(token),
		UserID:     userID,
		ExpiresAt:  now.Add(lifetime),
		LastSeenAt: now,
		Kind:       kind,
		ClientName: clientName,
	}, token, nil
}

// Session is who a session token belongs to, as SessionActor returns it.
type Session struct {
	// SessionID is the ID of the session, for Sessions and EndSession.
	SessionID uint64
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

// SessionActor finds the browser session of the cookie token and returns who
// it belongs to. For an empty, unknown or expired token, for a session of
// another kind (an API token, SPEC D43) and for a disabled user, it fails with
// TagUnauthenticated. A session lasts the configured lifetime from its last
// use: when the last use is more than a minute old, the use and the expiry are
// written again and Refreshed is true. A session that is found expired is
// deleted; if that fails, the store's error is returned as it is.
func (a *Accounts) SessionActor(ctx context.Context, token string) (Session, error) {
	return a.sessionActor(ctx, token, SessionBrowser)
}

// APISessionActor is SessionActor for the token of the Authorization header
// of the API: it accepts only a session of kind SessionAPI, so that the
// session cookie of a browser is never a bearer token.
func (a *Accounts) APISessionActor(ctx context.Context, token string) (Session, error) {
	return a.sessionActor(ctx, token, SessionAPI)
}

// sessionActor is the one implementation of SessionActor and APISessionActor.
func (a *Accounts) sessionActor(ctx context.Context, token string, kind SessionKind) (Session, error) {
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
	if sess.Kind != kind {
		return Session{}, errors.New(TagUnauthenticated, "the session is of another kind",
			"reason", "kind", "sessionID", sess.ID, "kind", string(sess.Kind))
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
		SessionID: sess.ID,
		Actor:     Actor{UserID: user.ID, Username: user.Username, Role: user.Role},
		User:      user,
		ExpiresAt: expires,
		Refreshed: refreshed,
	}, nil
}

// Logout deletes the session of the cookie token. An unknown token is not an
// error. The call the user is in, if any, ends (session_ended, SPEC §7.18).
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
	if err := a.endCallsOf(ctx, sess.UserID, store.CallSessionEnded); err != nil {
		return err
	}
	// The other pages that are open with this session notice at once.
	a.pub.Publish(live.Event{Topic: live.UserTopic(sess.UserID), Kind: live.KindUserChanged, UserID: sess.UserID})
	return nil
}

// endCallsOf ends the open calls of the user, in a transaction of their own
// when there are any, and publishes the events after the commit. A session
// that ended is not tied to a call in the database, so the call of the user
// ends whichever session it was (SPEC §7.18).
func (a *Accounts) endCallsOf(ctx context.Context, userID uint64, reason store.CallEndReason) error {
	open, err := a.st.OpenCallsOfUsers(ctx, userID)
	if err != nil || len(open) == 0 {
		return err
	}
	var events []live.Event
	err = a.st.Tx(ctx, func(tx *store.Store) error {
		var err error
		events, err = endCallsOfUser(ctx, tx, a.clk.Now().UTC(), userID, reason)
		return err
	})
	if err != nil {
		return err
	}
	publishAll(a.pub, events)
	return nil
}

// SessionInfo is a session of a user as the user sees it in the list of
// their sessions: never the token.
type SessionInfo struct {
	// ID is the ID of the session, for EndSession.
	ID uint64
	// Kind says whether it is a browser or an API client.
	Kind SessionKind
	// ClientName is the user agent of the browser or the name the API client
	// gave; empty for a session of version 1.0.
	ClientName string
	// CreatedAt is when the session was made.
	CreatedAt time.Time
	// LastSeenAt is when the session was last used.
	LastSeenAt time.Time
	// ExpiresAt is when the session ends, counting from its last use.
	ExpiresAt time.Time
	// Current is true for the session that made the request.
	Current bool
}

// Sessions returns the live sessions of the actor, browsers and API clients,
// the newest first, never anyone else's. currentID is the ID of the session of
// the request (Session.SessionID), marked as current; 0 marks none. A session
// that has expired is not listed.
func (a *Accounts) Sessions(ctx context.Context, actor Actor, currentID uint64) ([]SessionInfo, error) {
	rows, err := a.st.SessionsOfUser(ctx, actor.UserID)
	if err != nil {
		return nil, err
	}
	now := a.clk.Now().UTC()
	out := make([]SessionInfo, 0, len(rows))
	for _, row := range rows {
		if !now.Before(row.ExpiresAt) {
			continue
		}
		out = append(out, SessionInfo{
			ID:         row.ID,
			Kind:       row.Kind,
			ClientName: row.ClientName,
			CreatedAt:  row.CreatedAt,
			LastSeenAt: row.LastSeenAt,
			ExpiresAt:  row.ExpiresAt,
			Current:    currentID != 0 && row.ID == currentID,
		})
	}
	return out, nil
}

// EndSession deletes the session sessionID of the actor, which logs that
// browser or client out. It fails with TagNotFound for a session that does not
// exist or belongs to someone else, and does not tell the two apart. Like a
// logout it publishes live.KindUserChanged on the topic of the user, so that
// the open streams of that session read it again and end (SPEC D37). The call
// the user is in, if any, ends in the same transaction (session_ended, SPEC
// §7.18): a call is not tied to a session in the database, so it ends
// whichever session was ended.
func (a *Accounts) EndSession(ctx context.Context, actor Actor, sessionID uint64) error {
	if err := a.st.DeleteSessionOfUser(ctx, actor.UserID, sessionID); err != nil {
		return err
	}
	if err := a.endCallsOf(ctx, actor.UserID, store.CallSessionEnded); err != nil {
		return err
	}
	a.pub.Publish(live.Event{Topic: live.UserTopic(actor.UserID), Kind: live.KindUserChanged, UserID: actor.UserID})
	return nil
}
