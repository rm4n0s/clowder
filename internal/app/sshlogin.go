package app

import (
	"context"
	"time"

	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/metrics"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// The rules of the SSH code at login (SPEC §7.16, D53).
const (
	// PendingLoginLifetime is how long a login waits for its code.
	PendingLoginLifetime = 5 * time.Minute
	// sshCodeMaxWrongTries is the number of wrong codes after which a
	// pending login is deleted.
	sshCodeMaxWrongTries = 5
)

// FieldSSHCode names the code of the second form of the login in the metadata
// "fields" of an error; the API calls it ssh_code.
const FieldSSHCode = "sshCode"

// Messages of the SSH code at login. They are fixed text, shown to people.
const (
	// MessageSignInExpired is shown when a pending login is gone: it expired,
	// had five wrong codes, or its user was disabled or changed the password.
	MessageSignInExpired = "This sign-in has expired. Log in again."
	// MessageSSHCodeWrong is the message under the field of the code.
	MessageSSHCodeWrong = "That code is not right."
	// MessageSSHCodeNotTried is the answer to turning the SSH code on before a
	// key has fetched a code.
	MessageSSHCodeNotTried = "Fetch a code with one of your keys first."
	// MessageSSHServerOff is the answer to turning the SSH code on when this
	// Clowder runs no SSH server.
	MessageSSHServerOff = "The SSH server is not running on this Clowder."
	// MessageSSHCodeNeedsKey is the answer to removing the last key while the
	// SSH code is on.
	MessageSSHCodeNeedsKey = "Turn off the SSH code before you remove your last key."
)

// errWrongCode marks, inside a transaction, that the code was not right, so
// that the transaction rolls back. It is never returned to a caller.
var errWrongCode = &errors.Error{Tag: tagSSHCodeWrongInTx}

// errPendingGone marks, inside a transaction, that the pending login was
// deleted since it was read. It is never returned to a caller.
var errPendingGone = &errors.Error{Tag: tagPendingGoneInTx}

// needsSSHCode reports whether a login of the user has to type an SSH code:
// the user turned it on and this process runs the SSH server (SPEC §7.16).
func (a *Accounts) needsSSHCode(ctx context.Context, userID uint64) (bool, error) {
	if !a.sshServer {
		return false, nil
	}
	settings, err := a.st.UserSettingsOf(ctx, userID)
	if err != nil {
		return false, err
	}
	return settings.SSHCodeRequired, nil
}

// startPendingLogin makes the pending login of a user whose password was
// right and who has to type a code. It deletes the pending logins that have
// expired in the same transaction. The result holds the raw token once.
func (a *Accounts) startPendingLogin(ctx context.Context, user store.User, next string) (LoginResult, error) {
	token, err := auth.NewToken()
	if err != nil {
		return LoginResult{}, err
	}
	now := a.clk.Now().UTC()
	row := &store.PendingLogin{
		CreatedAt: now,
		TokenHash: auth.HashToken(token),
		UserID:    user.ID,
		ExpiresAt: now.Add(PendingLoginLifetime),
		Next:      next,
	}
	err = a.st.Tx(ctx, func(tx *store.Store) error {
		return tx.CreatePendingLogin(ctx, row, now)
	})
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{
		User:    viewOf(user),
		Pending: &PendingLogin{Token: token, Username: user.Username, ExpiresAt: row.ExpiresAt, Next: next},
	}, nil
}

// PendingLoginInfo returns the pending login of the token of the cookie
// clowder_pending, without the token: what the second form of the login
// shows. A token that is empty, unknown or expired, and one of a user who is
// disabled, fails with TagUnauthenticated (message MessageSignInExpired): the
// pending login is deleted when it is found expired or its user disabled.
func (a *Accounts) PendingLoginInfo(ctx context.Context, token string) (PendingLogin, error) {
	p, user, err := a.usablePendingLogin(ctx, a.st, token)
	if err != nil {
		return PendingLogin{}, err
	}
	return PendingLogin{Username: user.Username, ExpiresAt: p.ExpiresAt, Next: p.Next}, nil
}

// usablePendingLogin finds the pending login of token in st and its user, and
// checks that both can still log in.
func (a *Accounts) usablePendingLogin(ctx context.Context, st *store.Store, token string) (store.PendingLogin, store.User, error) {
	if token == "" {
		return store.PendingLogin{}, store.User{}, errors.New(TagUnauthenticated, MessageSignInExpired, "reason", "none")
	}
	p, err := st.PendingLoginByTokenHash(ctx, auth.HashToken(token))
	if err != nil {
		if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagNotFound {
			return store.PendingLogin{}, store.User{}, errors.New(TagUnauthenticated, MessageSignInExpired, "reason", "unknown")
		}
		return store.PendingLogin{}, store.User{}, err
	}
	if !a.clk.Now().Before(p.ExpiresAt) {
		if _, err := st.DeletePendingLogin(ctx, p.ID); err != nil {
			return store.PendingLogin{}, store.User{}, err
		}
		return store.PendingLogin{}, store.User{}, errors.New(TagUnauthenticated, MessageSignInExpired,
			"reason", "expired", "pendingID", p.ID)
	}
	user, err := st.UserByID(ctx, p.UserID)
	if err != nil {
		return store.PendingLogin{}, store.User{}, err
	}
	if user.DisabledAt != nil {
		if _, err := st.DeletePendingLogin(ctx, p.ID); err != nil {
			return store.PendingLogin{}, store.User{}, err
		}
		return store.PendingLogin{}, store.User{}, errors.New(TagUnauthenticated, MessageSignInExpired,
			"reason", "disabled", "pendingID", p.ID)
	}
	return p, user, nil
}

// CancelPendingLogin deletes the pending login of the token ("Start again").
// An unknown token is not an error.
func (a *Accounts) CancelPendingLogin(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	p, err := a.st.PendingLoginByTokenHash(ctx, auth.HashToken(token))
	if err != nil {
		if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagNotFound {
			return nil
		}
		return err
	}
	_, err = a.st.DeletePendingLogin(ctx, p.ID)
	return err
}

// CompleteLogin finishes a login that waits for the SSH code (SPEC §7.16,
// D53): pendingToken is the token of the cookie clowder_pending and code what
// the person typed (a dash, spaces and the case do not matter). On a right
// code of any key of the user it starts a browser session named by userAgent,
// uses the code up and deletes the pending login, in one transaction, and
// returns the session like a login does, with the path to go to in Next.
//
// An unknown or expired token, a user who was disabled, and a pending login
// that is gone fail with TagUnauthenticated (message MessageSignInExpired). A
// username or address that the limiter blocks fails with TagRateLimited before
// the code is looked at. A wrong, used, expired, replaced or foreign code
// counts as a failed login of the username and of clientIP, counts one wrong
// try of the pending login and fails with TagSSHCodeWrong (field "sshCode");
// the fifth wrong code deletes the pending login, and that attempt and the
// ones after it fail as an expired one.
func (a *Accounts) CompleteLogin(ctx context.Context, pendingToken, code, clientIP, userAgent string) (LoginResult, error) {
	clientName, err := CleanClientName(SessionBrowser, userAgent)
	if err != nil {
		return LoginResult{}, err
	}
	p, user, err := a.usablePendingLogin(ctx, a.st, pendingToken)
	if err != nil {
		return LoginResult{}, err
	}
	if !a.limiter.Allow(ctx, user.Username, clientIP) {
		a.countLogin(metrics.LoginLimited)
		return LoginResult{}, errors.New(TagRateLimited, "too many failed login attempts",
			"username", limiterKey(user.Username), "ip", clientIP)
	}

	sess, token, err := newSession(user.ID, SessionBrowser, clientName, a.clk.Now().UTC(), a.sessionLifetime)
	if err != nil {
		return LoginResult{}, err
	}
	// A code of the wrong length cannot be right: it is not hashed.
	typed := NormalizeCode(code)
	err = a.st.Tx(ctx, func(tx *store.Store) error {
		// The pending login goes first: when a change of the password or the
		// fifth wrong code of another request deleted it, nothing is used.
		gone, err := tx.DeletePendingLogin(ctx, p.ID)
		if err != nil {
			return err
		}
		if !gone {
			return errPendingGone
		}
		if len(typed) != sshCodeLength {
			return errWrongCode
		}
		if _, err := tx.ConsumeSSHCode(ctx, user.ID, auth.HashToken(typed), sess.LastSeenAt); err != nil {
			if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagNotFound {
				return errWrongCode
			}
			return err
		}
		if err := tx.DeleteExpiredSessionsOfUser(ctx, user.ID, sess.LastSeenAt); err != nil {
			return err
		}
		return tx.CreateSession(ctx, sess)
	})
	switch {
	case err == nil:
	case errors.Is(err, errPendingGone):
		return LoginResult{}, errors.New(TagUnauthenticated, MessageSignInExpired,
			"reason", "gone", "pendingID", p.ID)
	case errors.Is(err, errWrongCode):
		a.countLogin(metrics.LoginSSHCodeWrong)
		a.limiter.Fail(ctx, user.Username, clientIP)
		gone, err := a.st.AddPendingLoginWrongTry(ctx, p.ID, sshCodeMaxWrongTries)
		if err != nil {
			return LoginResult{}, err
		}
		if gone {
			return LoginResult{}, errors.New(TagUnauthenticated, MessageSignInExpired,
				"reason", "tries", "pendingID", p.ID)
		}
		return LoginResult{}, errors.New(TagSSHCodeWrong, "the SSH sign-in code is wrong",
			"userID", user.ID, "fields", map[string]string{FieldSSHCode: MessageSSHCodeWrong})
	default:
		return LoginResult{}, err
	}
	a.countLogin(metrics.LoginOK)
	a.limiter.Reset(ctx, user.Username)
	return LoginResult{Token: token, User: viewOf(user), ExpiresAt: sess.ExpiresAt, Next: p.Next}, nil
}

// EnableSSHCode turns on "Ask for an SSH code when I log in" for the actor
// (SPEC §7.16, D53). It needs the current password, checked as Add checks it
// (a wrong one counts as a failed login of the username and of clientIP), this
// process running the SSH server, and at least one key of the user that has
// fetched a code (its last use is set), which proves that the setup works. In
// one transaction it sets the flag and writes the audit event
// user.ssh_code_enabled; when the code is on already it changes nothing and
// writes no event. After the commit it publishes live.KindSettingsChanged.
//
// Failures: TagInvalidInput for a wrong password (field "currentPassword"),
// TagRateLimited for a blocked username or address, TagSSHServerOff when the
// server is off, TagSSHCodeNotTried when no key fetched a code (both carry the
// message under the field "form") and TagUnauthenticated for a disabled or
// missing account.
func (k *SSHKeys) EnableSSHCode(ctx context.Context, actor Actor, currentPassword, clientIP string) error {
	user, err := k.accounts.activeUser(ctx, actor)
	if err != nil {
		return err
	}
	allowed, match, err := k.accounts.checkPassword(ctx, user, currentPassword, clientIP)
	if err != nil {
		return err
	}
	if !allowed {
		return errors.New(TagRateLimited, "too many failed password attempts",
			"username", limiterKey(user.Username), "ip", clientIP)
	}
	if !match {
		return errors.New(TagInvalidInput, "the current password is wrong",
			"fields", map[string]string{FieldCurrentPassword: messageWrongPassword})
	}
	if k.server.Addr == "" {
		return errors.New(TagSSHServerOff, "this Clowder runs no SSH server",
			"fields", map[string]string{FieldForm: MessageSSHServerOff})
	}
	changed := false
	err = k.st.Tx(ctx, func(tx *store.Store) error {
		changed = false
		if err := tx.LockUser(ctx, user.ID); err != nil {
			return err
		}
		if _, err := k.accounts.activeUserOn(ctx, tx, actor); err != nil {
			return err
		}
		settings, err := tx.UserSettingsOf(ctx, user.ID)
		if err != nil {
			return err
		}
		if settings.SSHCodeRequired {
			return nil
		}
		tried, err := tx.CountUsedSSHKeysOfUser(ctx, user.ID)
		if err != nil {
			return err
		}
		if tried == 0 {
			return errors.New(TagSSHCodeNotTried, "no SSH key of the user fetched a code yet",
				"userID", user.ID, "fields", map[string]string{FieldForm: MessageSSHCodeNotTried})
		}
		changed, err = tx.SetSSHCodeRequired(ctx, user.ID, true)
		if err != nil || !changed {
			return err
		}
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    user.ID,
			Action:     ActionUserSSHCodeEnabled,
			RoomID:     store.NoRoom,
			EntityType: EntityUser,
			EntityID:   user.ID,
			Summary:    user.DisplayName + " turned on the SSH code at login",
		})
	})
	if err != nil {
		return err
	}
	if changed {
		k.accounts.publishUser(user.ID, live.KindSettingsChanged)
	}
	return nil
}

// DisableSSHCode turns off "Ask for an SSH code when I log in" for the actor.
// It needs the current password, checked as EnableSSHCode checks it, and works
// with the SSH server off too. It writes user.ssh_code_disabled; when the code
// is off already it changes nothing and writes no event. It fails with
// TagInvalidInput (field "currentPassword"), TagRateLimited or
// TagUnauthenticated like EnableSSHCode.
func (k *SSHKeys) DisableSSHCode(ctx context.Context, actor Actor, currentPassword, clientIP string) error {
	user, err := k.accounts.activeUser(ctx, actor)
	if err != nil {
		return err
	}
	allowed, match, err := k.accounts.checkPassword(ctx, user, currentPassword, clientIP)
	if err != nil {
		return err
	}
	if !allowed {
		return errors.New(TagRateLimited, "too many failed password attempts",
			"username", limiterKey(user.Username), "ip", clientIP)
	}
	if !match {
		return errors.New(TagInvalidInput, "the current password is wrong",
			"fields", map[string]string{FieldCurrentPassword: messageWrongPassword})
	}
	changed := false
	err = k.st.Tx(ctx, func(tx *store.Store) error {
		changed = false
		if _, err := k.accounts.activeUserOn(ctx, tx, actor); err != nil {
			return err
		}
		var err error
		changed, err = tx.SetSSHCodeRequired(ctx, user.ID, false)
		if err != nil || !changed {
			return err
		}
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    user.ID,
			Action:     ActionUserSSHCodeDisabled,
			RoomID:     store.NoRoom,
			EntityType: EntityUser,
			EntityID:   user.ID,
			Summary:    user.DisplayName + " turned off the SSH code at login",
		})
	})
	if err != nil {
		return err
	}
	if changed {
		k.accounts.publishUser(user.ID, live.KindSettingsChanged)
	}
	return nil
}

// RemoveSSHCode turns off the SSH code at login of the user userID, for a
// user who lost their keys (SPEC §7.16). Only admins may, for any user,
// themselves included, and it needs no password. It writes
// user.ssh_code_removed with the admin as the actor; when the code is off
// already it changes nothing and writes no event. It fails with TagForbidden
// for anyone but an admin (also for an actor whose role was taken away since
// the actor was built), TagUnauthenticated for a disabled or missing admin
// account and TagNotFound for an unknown user.
func (k *SSHKeys) RemoveSSHCode(ctx context.Context, actor Actor, userID uint64) error {
	if !actor.IsAdmin() {
		return errors.New(TagForbidden, "only admins turn off the SSH code of a user",
			"userID", actor.UserID)
	}
	changed := false
	err := k.st.Tx(ctx, func(tx *store.Store) error {
		changed = false
		admin, err := k.accounts.activeUserOn(ctx, tx, actor)
		if err != nil {
			return err
		}
		if admin.Role != store.RoleAdmin {
			return errors.New(TagForbidden, "only admins turn off the SSH code of a user",
				"userID", actor.UserID)
		}
		target, err := tx.UserByID(ctx, userID)
		if err != nil {
			return err
		}
		changed, err = tx.SetSSHCodeRequired(ctx, target.ID, false)
		if err != nil || !changed {
			return err
		}
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    admin.ID,
			Action:     ActionUserSSHCodeRemoved,
			RoomID:     store.NoRoom,
			EntityType: EntityUser,
			EntityID:   target.ID,
			Summary:    admin.DisplayName + " turned off the SSH code of " + target.DisplayName,
		})
	})
	if err != nil {
		return err
	}
	if changed {
		k.accounts.publishUser(userID, live.KindSettingsChanged)
	}
	return nil
}

const (
	// TagSSHServerOff is the tag of turning on the SSH code when this Clowder
	// runs no SSH server (SPEC §7.16). The handlers answer 409.
	TagSSHServerOff = "SSHServerOff"
	// TagSSHCodeNotTried is the tag of turning on the SSH code before one of
	// the keys of the user has fetched a code. The handlers answer 409.
	TagSSHCodeNotTried = "SSHCodeNotTried"
	// TagSSHCodeNeedsKey is the tag of removing the last SSH key of a user
	// whose SSH code is on. The handlers answer 409.
	TagSSHCodeNeedsKey = "SSHCodeNeedsKey"
	// TagSSHCodeRequired is the tag of an API login whose password was right
	// and whose user turned on the SSH code, without a code. The API answers
	// 401 with the code ssh_code_required.
	TagSSHCodeRequired = "SSHCodeRequired"
	// TagSSHCodeWrong is the tag of a wrong code in the second form of the
	// login. The pages show the message of the metadata "fields" next to the
	// code field.
	TagSSHCodeWrong = "SSHCodeWrong"

	tagSSHCodeWrongInTx = "SSHCodeWrongInTx"
	tagPendingGoneInTx  = "PendingLoginGoneInTx"
)
