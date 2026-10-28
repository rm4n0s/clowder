package app

import (
	"context"
	"time"

	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// ResetLinkLifetime is how long a password reset link stays valid (SPEC §7.1,
// D51). It is a constant, not a setting.
const ResetLinkLifetime = 24 * time.Hour

// IssuedResetLink is a new password reset link: the token, which is shown once
// and never stored, and the facts about it.
type IssuedResetLink struct {
	// Token is the secret in the link, <base URL>/reset/<token>.
	Token string
	// UserID is the user the link is for.
	UserID uint64
	// Username is the username of that user.
	Username string
	// ExpiresAt is when the link stops working.
	ExpiresAt time.Time
}

// ResetInput is what a person gives to set a new password with a reset link.
type ResetInput struct {
	// Token is the token of the link.
	Token string
	// NewPassword is the password to set: at least 12 characters, at most
	// 1024 bytes.
	NewPassword string
	// ClientName names the browser (its user agent) in the session that the
	// reset starts.
	ClientName string
}

// Messages of the reset form and the reset commands.
const (
	messageResetUserDisabled = "the user is disabled"
	messageResetUserUnknown  = "there is no user with this username"
)

// CreateResetLink makes a one-time password reset link for the user userID
// (SPEC §7.1, D51). Only admins may, for any user who is not disabled,
// themselves included. In one transaction it deletes the unused links of the
// user, stores the new one (the SHA-256 of its token; the token is returned
// once and never stored) and writes the audit event user.reset_link_created.
//
// It fails with TagForbidden for anyone but an admin (also for an actor whose
// role was taken away since the actor was built), with TagUnauthenticated when
// the actor's account is disabled or gone, with TagNotFound for an unknown
// user and with TagConflict for a user who is disabled.
func (a *Accounts) CreateResetLink(ctx context.Context, actor Actor, userID uint64) (IssuedResetLink, error) {
	if !actor.IsAdmin() {
		return IssuedResetLink{}, errors.New(TagForbidden, "only admins make reset links",
			"userID", actor.UserID)
	}
	var issued IssuedResetLink
	err := a.st.Tx(ctx, func(tx *store.Store) error {
		admin, err := a.activeUserOn(ctx, tx, actor)
		if err != nil {
			return err
		}
		if admin.Role != store.RoleAdmin {
			return errors.New(TagForbidden, "only admins make reset links", "userID", actor.UserID)
		}
		target, err := tx.UserByID(ctx, userID)
		if err != nil {
			return err
		}
		if target.DisabledAt != nil {
			return errors.New(TagConflict, messageResetUserDisabled, "userID", userID)
		}
		issued, err = a.issueResetLink(ctx, tx, target, &admin.ID)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    admin.ID,
			Action:     ActionUserResetLinkCreated,
			RoomID:     store.NoRoom,
			EntityType: EntityUser,
			EntityID:   target.ID,
			Summary:    admin.DisplayName + " made a password reset link for " + target.DisplayName,
			Details:    map[string]any{"expiresAt": issued.ExpiresAt.UTC().Format(time.RFC3339)},
		})
	})
	if err != nil {
		return IssuedResetLink{}, err
	}
	return issued, nil
}

// CreateSystemResetLink makes a password reset link that no admin made: the
// one of "clowder reset-password" on the command line, which has no actor. It
// is for the user with the username (lower-cased and trimmed); like
// CreateResetLink it replaces the unused links of the user and writes the
// audit event user.reset_link_created. Nothing in the web layer calls it.
//
// The audit event needs an actor that is a user (the table of events has a
// foreign key to the users), and the command line has none, so the event names
// the user of the link as its actor and says in its summary and in its details
// ("via": "command line") that it was made there.
//
// It fails with TagNotFound for an unknown username and with TagConflict for a
// user who is disabled.
func (a *Accounts) CreateSystemResetLink(ctx context.Context, username string) (IssuedResetLink, error) {
	username = normalizeUsername(username)
	var issued IssuedResetLink
	err := a.st.Tx(ctx, func(tx *store.Store) error {
		target, err := tx.UserByUsername(ctx, username)
		if err != nil {
			if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagNotFound {
				return errors.New(TagNotFound, messageResetUserUnknown, "username", username)
			}
			return err
		}
		if target.DisabledAt != nil {
			return errors.New(TagConflict, messageResetUserDisabled, "userID", target.ID)
		}
		issued, err = a.issueResetLink(ctx, tx, target, nil)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    target.ID,
			Action:     ActionUserResetLinkCreated,
			RoomID:     store.NoRoom,
			EntityType: EntityUser,
			EntityID:   target.ID,
			Summary:    "A password reset link for " + target.DisplayName + " was made on the command line",
			Details: map[string]any{
				"via":       "command line",
				"expiresAt": issued.ExpiresAt.UTC().Format(time.RFC3339),
			},
		})
	})
	if err != nil {
		return IssuedResetLink{}, err
	}
	return issued, nil
}

// issueResetLink stores a new reset link for target in tx, in place of the
// unused ones, made by createdBy (nil for the command line), and returns its
// token.
func (a *Accounts) issueResetLink(ctx context.Context, tx *store.Store, target store.User, createdBy *uint64) (IssuedResetLink, error) {
	token, err := auth.NewToken()
	if err != nil {
		return IssuedResetLink{}, err
	}
	expires := a.clk.Now().UTC().Add(ResetLinkLifetime)
	pr := &store.PasswordReset{
		UserID:    target.ID,
		TokenHash: auth.HashToken(token),
		CreatedBy: createdBy,
		ExpiresAt: expires,
	}
	if err := tx.ReplacePasswordReset(ctx, pr); err != nil {
		return IssuedResetLink{}, err
	}
	return IssuedResetLink{Token: token, UserID: target.ID, Username: target.Username, ExpiresAt: expires}, nil
}

// ResetLinkValid returns nil when token is the token of a reset link that is
// not used, not expired and belongs to a user who is not disabled. For any
// other token it returns TagNotFound and does not tell the caller which
// reason; the metadata "reason" does, for the log.
func (a *Accounts) ResetLinkValid(ctx context.Context, token string) error {
	_, _, err := a.usableResetLink(ctx, a.st, token)
	return err
}

// usableResetLink finds the reset link of token in st and checks that it can
// still be used, and returns it with its user. It is the one place that
// decides it, for ResetLinkValid and for ResetPassword (which asks again
// inside its transaction, with the Store of the transaction).
func (a *Accounts) usableResetLink(ctx context.Context, st *store.Store, token string) (store.PasswordReset, store.User, error) {
	pr, err := st.PasswordResetByTokenHash(ctx, auth.HashToken(token))
	if err != nil {
		return store.PasswordReset{}, store.User{}, err
	}
	if pr.UsedAt != nil {
		return store.PasswordReset{}, store.User{}, errors.New(TagNotFound, "the reset link is not valid",
			"reason", "used", "resetID", pr.ID)
	}
	if !a.clk.Now().Before(pr.ExpiresAt) {
		return store.PasswordReset{}, store.User{}, errors.New(TagNotFound, "the reset link is not valid",
			"reason", "expired", "resetID", pr.ID)
	}
	user, err := st.UserByID(ctx, pr.UserID)
	if err != nil {
		return store.PasswordReset{}, store.User{}, err
	}
	if user.DisabledAt != nil {
		return store.PasswordReset{}, store.User{}, errors.New(TagNotFound, "the reset link is not valid",
			"reason", "disabled", "resetID", pr.ID)
	}
	return pr, user, nil
}

// ResetPassword sets a new password with a reset link (SPEC §7.1, D51). In one
// transaction it checks the link again, stores the new hash, deletes every
// session of the user (browsers and API clients alike), starts a new browser
// session, marks the link used and writes the audit event
// user.password_reset (the actor is the user). After the commit it clears the
// failed logins of the username and publishes live.KindUserChanged, which
// closes the sockets and streams of the sessions that ended. It returns the
// new session like a login does.
//
// An unknown, used or expired link, and a link of a user who was disabled
// since it was made, fail with TagNotFound without telling which. A password
// that breaks the rule of SPEC §7.1 fails with TagInvalidInput (field
// "password"). The link is not asked for a password check: holding it is the
// proof, so the failed logins of the limiter do not apply.
func (a *Accounts) ResetPassword(ctx context.Context, in ResetInput) (LoginResult, error) {
	// Ask before the costly hashing, and again inside the transaction.
	if _, _, err := a.usableResetLink(ctx, a.st, in.Token); err != nil {
		return LoginResult{}, err
	}
	if problem := passwordProblem(in.NewPassword); problem != "" {
		return LoginResult{}, errors.New(TagInvalidInput, "the new password breaks a rule",
			"fields", map[string]string{FieldPassword: problem})
	}
	clientName, err := CleanClientName(SessionBrowser, in.ClientName)
	if err != nil {
		return LoginResult{}, err
	}
	hash, err := a.hash(ctx, in.NewPassword)
	if err != nil {
		return LoginResult{}, err
	}
	token, err := auth.NewToken()
	if err != nil {
		return LoginResult{}, err
	}

	now := a.clk.Now().UTC()
	sess := &store.Session{
		TokenHash:  auth.HashToken(token),
		ExpiresAt:  now.Add(a.sessionLifetime),
		LastSeenAt: now,
		Kind:       SessionBrowser,
		ClientName: clientName,
	}
	var user store.User
	err = a.st.Tx(ctx, func(tx *store.Store) error {
		pr, u, err := a.usableResetLink(ctx, tx, in.Token)
		if err != nil {
			return err
		}
		user = u
		sess.UserID = u.ID
		if err := tx.SetUserPasswordHash(ctx, u.ID, hash); err != nil {
			return err
		}
		// TODO(step 19.2): the reset must not touch the SSH sign-in code of
		// the user (SPEC D51); nothing of it is read or written here.
		if err := tx.DeleteSessionsOfUser(ctx, u.ID); err != nil {
			return err
		}
		if err := tx.CreateSession(ctx, sess); err != nil {
			return err
		}
		if err := tx.MarkPasswordResetUsed(ctx, pr.ID); err != nil {
			return err
		}
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    u.ID,
			Action:     ActionUserPasswordReset,
			RoomID:     store.NoRoom,
			EntityType: EntityUser,
			EntityID:   u.ID,
			Summary:    u.DisplayName + " set a new password with a reset link",
		})
	})
	if err != nil {
		return LoginResult{}, err
	}
	// A person who forgot the password may have failed to log in often; the
	// new password starts clean.
	a.limiter.Reset(ctx, user.Username)
	// The streams and sockets of the sessions that ended read their session
	// again and close.
	a.pub.Publish(live.Event{Topic: live.UserTopic(user.ID), Kind: live.KindUserChanged, UserID: user.ID})
	return LoginResult{Token: token, User: viewOf(user), ExpiresAt: sess.ExpiresAt}, nil
}
