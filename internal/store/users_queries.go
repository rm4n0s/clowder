package store

import (
	"context"
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
