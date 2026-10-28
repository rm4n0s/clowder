package store

import (
	"context"
	"strings"
	"time"

	"github.com/rm4n0s/errors"
)

// CreateInvitation inserts inv and sets its ID. It fails with TagConflict when
// the token hash is already taken.
func (s *Store) CreateInvitation(ctx context.Context, inv *Invitation) error {
	err := s.db(ctx).Create(inv).Error
	if err != nil {
		switch classify(err) {
		case failureConflict:
			return errors.NewErr(TagConflict, err).SetMetadata("op", "createInvitation")
		default:
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createInvitation")
		}
	}
	return nil
}

// InvitationByTokenHash returns the invitation whose token has the given
// SHA-256 hash. It fails with TagNotFound when there is none.
func (s *Store) InvitationByTokenHash(ctx context.Context, tokenHash string) (Invitation, error) {
	var inv Invitation
	err := s.db(ctx).Where("token_hash = ?", tokenHash).First(&inv).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return Invitation{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "invitationByTokenHash")
		default:
			return Invitation{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "invitationByTokenHash")
		}
	}
	return inv, nil
}

// MarkInvitationUsed records that the user userID used the invitation id, at
// the time of the Store's clock. It fails with TagNotFound when there is no
// such invitation or it was used already, so that one invitation never makes
// two users.
func (s *Store) MarkInvitationUsed(ctx context.Context, id, userID uint64) error {
	res := s.db(ctx).Model(&Invitation{}).
		Where("id = ? AND used_at IS NULL", id).
		Updates(map[string]any{"used_by": userID, "used_at": s.clk.Now().UTC()})
	if res.Error != nil {
		return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "markInvitationUsed")
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no unused invitation with this ID",
			"op", "markInvitationUsed", "invitationID", id)
	}
	return nil
}

// CreateUser inserts u and sets its ID. It fails with TagConflict when the
// username is taken.
func (s *Store) CreateUser(ctx context.Context, u *User) error {
	err := s.db(ctx).Create(u).Error
	if err != nil {
		switch classify(err) {
		case failureConflict:
			return errors.NewErr(TagConflict, err).SetMetadata("op", "createUser")
		default:
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createUser")
		}
	}
	return nil
}

// UserByID returns the user with the given ID. It fails with TagNotFound when
// there is none.
func (s *Store) UserByID(ctx context.Context, id uint64) (User, error) {
	var u User
	err := s.db(ctx).First(&u, id).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return User{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "userByID")
		default:
			return User{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "userByID")
		}
	}
	return u, nil
}

// UserByUsername returns the user with the given username, which the caller
// has already lower-cased. It fails with TagNotFound when there is none.
func (s *Store) UserByUsername(ctx context.Context, username string) (User, error) {
	var u User
	err := s.db(ctx).Where("username = ?", username).First(&u).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return User{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "userByUsername")
		default:
			return User{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "userByUsername")
		}
	}
	return u, nil
}

// ListUsers returns every user, ordered by username.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	var users []User
	err := s.db(ctx).Order("username ASC").Find(&users).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "listUsers")
	}
	return users, nil
}

// SetUserRole gives the user id the role. It fails with TagNotFound when
// there is no such user.
func (s *Store) SetUserRole(ctx context.Context, id uint64, role Role) error {
	res := s.db(ctx).Model(&User{}).Where("id = ?", id).Update("role", role)
	if res.Error != nil {
		return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "setUserRole")
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no user with this ID", "op", "setUserRole", "userID", id)
	}
	return nil
}

// SetUserDisabled disables the user id, at the time of the Store's clock, or
// enables it. It fails with TagNotFound when there is no such user.
func (s *Store) SetUserDisabled(ctx context.Context, id uint64, disabled bool) error {
	var disabledAt any // nil makes the column NULL
	if disabled {
		disabledAt = s.clk.Now().UTC()
	}
	res := s.db(ctx).Model(&User{}).Where("id = ?", id).Update("disabled_at", disabledAt)
	if res.Error != nil {
		return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "setUserDisabled")
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no user with this ID", "op", "setUserDisabled", "userID", id)
	}
	return nil
}

// SetUserDisplayName sets the display name of the user id. It fails with
// TagNotFound when there is no such user.
func (s *Store) SetUserDisplayName(ctx context.Context, id uint64, displayName string) error {
	res := s.db(ctx).Model(&User{}).Where("id = ?", id).Update("display_name", displayName)
	if res.Error != nil {
		return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "setUserDisplayName")
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no user with this ID", "op", "setUserDisplayName", "userID", id)
	}
	return nil
}

// SetUserAvatarKey sets the avatar key of the user id; an empty key removes
// the avatar. It fails with TagNotFound when there is no such user.
func (s *Store) SetUserAvatarKey(ctx context.Context, id uint64, key string) error {
	res := s.db(ctx).Model(&User{}).Where("id = ?", id).Update("avatar_key", key)
	if res.Error != nil {
		return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "setUserAvatarKey")
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no user with this ID", "op", "setUserAvatarKey", "userID", id)
	}
	return nil
}

// SetUserPasswordHash replaces the password hash of the user id. It fails
// with TagNotFound when there is no such user.
func (s *Store) SetUserPasswordHash(ctx context.Context, id uint64, hash string) error {
	res := s.db(ctx).Model(&User{}).Where("id = ?", id).Update("password_hash", hash)
	if res.Error != nil {
		return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "setUserPasswordHash")
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no user with this ID", "op", "setUserPasswordHash", "userID", id)
	}
	return nil
}

// adminLockKey is the key of the PostgreSQL advisory lock that LockAdmins
// takes.
const adminLockKey int64 = 0x61646d696e73

// LockAdmins makes the rest of the transaction the only one that changes the
// admins: another transaction that calls it waits until this one ends. A
// change that must leave one active admin (SPEC §7.1) calls it first and then
// reads the admins, so that two of them, in one process or in two, cannot each
// see the other admin and both remove an admin. Call it inside Tx. On SQLite
// it does nothing, because a write transaction is already alone (BEGIN
// IMMEDIATE).
func (s *Store) LockAdmins(ctx context.Context) error {
	if s.dialect != dialectPostgres {
		return nil
	}
	if err := s.db(ctx).Exec("SELECT pg_advisory_xact_lock(?)", adminLockKey).Error; err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "lockAdmins")
	}
	return nil
}

// CountActiveAdmins returns the number of users with the admin role who are
// not disabled.
func (s *Store) CountActiveAdmins(ctx context.Context) (int64, error) {
	var n int64
	err := s.db(ctx).Model(&User{}).
		Where("role = ? AND disabled_at IS NULL", RoleAdmin).
		Count(&n).Error
	if err != nil {
		return 0, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "countActiveAdmins")
	}
	return n, nil
}

// CreateSession inserts sess and sets its ID. It fails with TagConflict when
// the token hash is already taken.
func (s *Store) CreateSession(ctx context.Context, sess *Session) error {
	err := s.db(ctx).Create(sess).Error
	if err != nil {
		switch classify(err) {
		case failureConflict:
			return errors.NewErr(TagConflict, err).SetMetadata("op", "createSession")
		default:
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createSession")
		}
	}
	return nil
}

// CountSessionsOfUser returns the number of sessions of the user, expired or
// not.
func (s *Store) CountSessionsOfUser(ctx context.Context, userID uint64) (int64, error) {
	var n int64
	err := s.db(ctx).Model(&Session{}).Where("user_id = ?", userID).Count(&n).Error
	if err != nil {
		return 0, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "countSessionsOfUser")
	}
	return n, nil
}

// DeleteSessionsOfUser deletes every session of the user, which logs the user
// out everywhere. It is not an error when there are none.
func (s *Store) DeleteSessionsOfUser(ctx context.Context, userID uint64) error {
	err := s.db(ctx).Where("user_id = ?", userID).Delete(&Session{}).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "deleteSessionsOfUser")
	}
	return nil
}

// DeleteSessionsOfUserExcept deletes every session of the user but the one
// with the ID keep, which logs the user out everywhere else. It is not an
// error when there are none.
func (s *Store) DeleteSessionsOfUserExcept(ctx context.Context, userID, keep uint64) error {
	err := s.db(ctx).Where("user_id = ? AND id <> ?", userID, keep).Delete(&Session{}).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "deleteSessionsOfUserExcept")
	}
	return nil
}

// SessionByTokenHash returns the session whose cookie token has the given
// SHA-256 hash. It fails with TagNotFound when there is none.
func (s *Store) SessionByTokenHash(ctx context.Context, tokenHash string) (Session, error) {
	var sess Session
	err := s.db(ctx).Where("token_hash = ?", tokenHash).First(&sess).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return Session{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "sessionByTokenHash")
		default:
			return Session{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "sessionByTokenHash")
		}
	}
	return sess, nil
}

// TouchSession sets when the session id was last used and when it expires. It
// fails with TagNotFound when there is no such session.
func (s *Store) TouchSession(ctx context.Context, id uint64, lastSeenAt, expiresAt time.Time) error {
	res := s.db(ctx).Model(&Session{}).Where("id = ?", id).
		Updates(map[string]any{"last_seen_at": lastSeenAt.UTC(), "expires_at": expiresAt.UTC()})
	if res.Error != nil {
		return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "touchSession")
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no session with this ID", "op", "touchSession", "sessionID", id)
	}
	return nil
}

// DeleteSessionByTokenHash deletes the session with the given token hash,
// which logs that browser out. It is not an error when there is none.
func (s *Store) DeleteSessionByTokenHash(ctx context.Context, tokenHash string) error {
	err := s.db(ctx).Where("token_hash = ?", tokenHash).Delete(&Session{}).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "deleteSessionByTokenHash")
	}
	return nil
}

// DeleteExpiredSessionsOfUser deletes the sessions of the user that expired at
// or before now (a session is over from its ExpiresAt on). The other sessions
// of the user and the sessions of other users stay. It is not an error when
// there are none.
func (s *Store) DeleteExpiredSessionsOfUser(ctx context.Context, userID uint64, now time.Time) error {
	err := s.db(ctx).Where("user_id = ? AND expires_at <= ?", userID, now.UTC()).Delete(&Session{}).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "deleteExpiredSessionsOfUser")
	}
	return nil
}

// SessionsOfUser returns the sessions of the user, expired or not, newest
// first.
func (s *Store) SessionsOfUser(ctx context.Context, userID uint64) ([]Session, error) {
	var sessions []Session
	err := s.db(ctx).Where("user_id = ?", userID).Order("created_at DESC, id DESC").Find(&sessions).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "sessionsOfUser")
	}
	return sessions, nil
}

// DeleteSessionOfUser deletes the session id when it belongs to the user. It
// fails with TagNotFound when there is no such session or it belongs to
// another user, so that a caller cannot tell the two apart.
func (s *Store) DeleteSessionOfUser(ctx context.Context, userID, id uint64) error {
	res := s.db(ctx).Where("id = ? AND user_id = ?", id, userID).Delete(&Session{})
	if res.Error != nil {
		return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "deleteSessionOfUser")
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no session of this user with this ID",
			"op", "deleteSessionOfUser", "sessionID", id)
	}
	return nil
}

// UsersByPrefix returns the users who are not disabled and whose username or
// display name starts with prefix, in any case, ordered by username, at most
// limit of them. An empty prefix matches every user. The case is ignored by
// the lower() of the database: SQLite folds only ASCII letters, PostgreSQL
// what its locale says.
func (s *Store) UsersByPrefix(ctx context.Context, prefix string, limit int) ([]User, error) {
	q := s.db(ctx).Where("disabled_at IS NULL")
	if prefix != "" {
		pattern := likeEscaper.Replace(strings.ToLower(prefix)) + "%"
		q = q.Where(`(lower(username) LIKE ? ESCAPE '\' OR lower(display_name) LIKE ? ESCAPE '\')`, pattern, pattern)
	}
	var users []User
	err := q.Order("username ASC").Limit(limit).Find(&users).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "usersByPrefix")
	}
	return users, nil
}

// likeEscaper escapes the characters that mean something in a LIKE pattern,
// with the backslash as the escape character.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// ReplacePasswordReset deletes the unused reset links of the user pr.UserID
// and inserts pr, so that a user has one unused link at most, and sets its ID.
// Call it inside Tx. It fails with TagConflict when the token hash is already
// taken.
func (s *Store) ReplacePasswordReset(ctx context.Context, pr *PasswordReset) error {
	err := s.db(ctx).Where("user_id = ? AND used_at IS NULL", pr.UserID).Delete(&PasswordReset{}).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "replacePasswordReset")
	}
	err = s.db(ctx).Create(pr).Error
	if err != nil {
		switch classify(err) {
		case failureConflict:
			return errors.NewErr(TagConflict, err).SetMetadata("op", "replacePasswordReset")
		default:
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "replacePasswordReset")
		}
	}
	return nil
}

// PasswordResetByTokenHash returns the reset link whose token has the given
// SHA-256 hash. It fails with TagNotFound when there is none.
func (s *Store) PasswordResetByTokenHash(ctx context.Context, tokenHash string) (PasswordReset, error) {
	var pr PasswordReset
	err := s.db(ctx).Where("token_hash = ?", tokenHash).First(&pr).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return PasswordReset{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "passwordResetByTokenHash")
		default:
			return PasswordReset{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "passwordResetByTokenHash")
		}
	}
	return pr, nil
}

// MarkPasswordResetUsed records that the reset link id was used, at the time
// of the Store's clock. It fails with TagNotFound when there is no such link
// or it was used already, so that one link never sets two passwords.
func (s *Store) MarkPasswordResetUsed(ctx context.Context, id uint64) error {
	res := s.db(ctx).Model(&PasswordReset{}).
		Where("id = ? AND used_at IS NULL", id).
		Update("used_at", s.clk.Now().UTC())
	if res.Error != nil {
		return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "markPasswordResetUsed")
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no unused reset link with this ID",
			"op", "markPasswordResetUsed", "resetID", id)
	}
	return nil
}
