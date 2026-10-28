package store

import (
	"context"
	"time"

	"github.com/rm4n0s/errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PendingLogin is a login whose password was right and that waits for the SSH
// sign-in code (SPEC §7.16, D53). Only the SHA-256 of its token is stored; the
// token is in the cookie clowder_pending. It lasts 5 minutes and goes with a
// right code, with the fifth wrong one, with a change of the password and with
// the disabling of the user.
type PendingLogin struct {
	ID        uint64
	CreatedAt time.Time
	// TokenHash is the SHA-256 of the 32 random bytes of the token. It is
	// unique.
	TokenHash string `gorm:"not null;uniqueIndex:uq_pending_logins_token_hash"`
	// UserID is the user whose password was right.
	UserID uint64 `gorm:"not null;index:idx_pending_logins_user_id"`
	// ExpiresAt is when the pending login stops working.
	ExpiresAt time.Time `gorm:"not null;index:idx_pending_logins_expires_at"`
	// WrongTries counts the wrong codes typed so far.
	WrongTries int `gorm:"not null;default:0"`
	// Next is the local path to go to after the login, already checked by the
	// web layer; empty for the home page.
	Next string `gorm:"not null;default:''"`

	// Relation: declared so that GORM creates the foreign key.
	User *User `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
}

// CreatePendingLogin deletes the pending logins that have expired at now and
// inserts p, and sets its ID. Call it inside Tx.
func (s *Store) CreatePendingLogin(ctx context.Context, p *PendingLogin, now time.Time) error {
	err := s.db(ctx).Where("expires_at <= ?", now.UTC()).Delete(&PendingLogin{}).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createPendingLogin")
	}
	if err := s.db(ctx).Create(p).Error; err != nil {
		switch classify(err) {
		case failureConflict:
			return errors.NewErr(TagConflict, err).SetMetadata("op", "createPendingLogin")
		default:
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createPendingLogin")
		}
	}
	return nil
}

// PendingLoginByTokenHash returns the pending login whose token has the hash.
// It fails with TagNotFound when there is none. It does not look at the expiry.
func (s *Store) PendingLoginByTokenHash(ctx context.Context, tokenHash string) (PendingLogin, error) {
	var p PendingLogin
	err := s.db(ctx).Where("token_hash = ?", tokenHash).First(&p).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return PendingLogin{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "pendingLoginByTokenHash")
		default:
			return PendingLogin{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "pendingLoginByTokenHash")
		}
	}
	return p, nil
}

// DeletePendingLogin deletes the pending login with the ID, and reports
// whether there was one to delete.
func (s *Store) DeletePendingLogin(ctx context.Context, id uint64) (deleted bool, err error) {
	res := s.db(ctx).Delete(&PendingLogin{}, id)
	if res.Error != nil {
		return false, errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "deletePendingLogin")
	}
	return res.RowsAffected > 0, nil
}

// DeletePendingLoginsOfUser deletes every pending login of the user: a change
// of the password and the disabling of the user end the logins that wait for a
// code. It is not an error when there are none.
func (s *Store) DeletePendingLoginsOfUser(ctx context.Context, userID uint64) error {
	err := s.db(ctx).Where("user_id = ?", userID).Delete(&PendingLogin{}).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "deletePendingLoginsOfUser")
	}
	return nil
}

// CountPendingLogins returns the number of pending logins, expired ones
// included.
func (s *Store) CountPendingLogins(ctx context.Context) (int64, error) {
	var n int64
	if err := s.db(ctx).Model(&PendingLogin{}).Count(&n).Error; err != nil {
		return 0, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "countPendingLogins")
	}
	return n, nil
}

// AddPendingLoginWrongTry counts one more wrong code for the pending login
// and deletes the login when it has max wrong codes. It reports whether the
// login is gone: deleted now, or deleted before (by a right code, a change of
// the password or the fifth wrong code of a request that raced this one).
func (s *Store) AddPendingLoginWrongTry(ctx context.Context, id uint64, max int) (gone bool, err error) {
	res := s.db(ctx).Model(&PendingLogin{}).Where("id = ?", id).
		Update("wrong_tries", gorm.Expr("wrong_tries + 1"))
	if res.Error != nil {
		return false, errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "addPendingLoginWrongTry")
	}
	if res.RowsAffected == 0 {
		return true, nil
	}
	del := s.db(ctx).Where("id = ? AND wrong_tries >= ?", id, max).Delete(&PendingLogin{})
	if del.Error != nil {
		return false, errors.NewErr(TagQueryFailed, del.Error).SetMetadata("op", "addPendingLoginWrongTry")
	}
	return del.RowsAffected > 0, nil
}

// SetSSHCodeRequired sets whether the user is asked for an SSH code at login,
// and reports whether the value changed. It makes the user's settings row when
// there is none. It does not check that the user exists. Call it inside Tx
// when the change goes with an audit event.
func (s *Store) SetSSHCodeRequired(ctx context.Context, userID uint64, required bool) (changed bool, err error) {
	var rows []UserSettings
	err = s.db(ctx).Where("user_id = ?", userID).Limit(1).Find(&rows).Error
	if err != nil {
		return false, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "setSSHCodeRequired")
	}
	if len(rows) == 0 {
		if !required {
			return false, nil
		}
		row := DefaultUserSettings(userID)
		row.SSHCodeRequired = true
		// Two requests that make the first row together: the loser updates.
		err = s.db(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"ssh_code_required"}),
		}).Create(&row).Error
		if err != nil {
			return false, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "setSSHCodeRequired")
		}
		return true, nil
	}
	if rows[0].SSHCodeRequired == required {
		return false, nil
	}
	err = s.db(ctx).Model(&UserSettings{}).Where("user_id = ?", userID).Update("ssh_code_required", required).Error
	if err != nil {
		return false, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "setSSHCodeRequired")
	}
	return true, nil
}

// UserIDsWithSSHCode returns the IDs of the users who turned on the SSH code
// at login.
func (s *Store) UserIDsWithSSHCode(ctx context.Context) (map[uint64]bool, error) {
	var ids []uint64
	err := s.db(ctx).Model(&UserSettings{}).Where("ssh_code_required").Pluck("user_id", &ids).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "userIDsWithSSHCode")
	}
	out := make(map[uint64]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// CountUsedSSHKeysOfUser returns how many keys of the user have fetched a code
// (their last use is set).
func (s *Store) CountUsedSSHKeysOfUser(ctx context.Context, userID uint64) (int64, error) {
	var n int64
	err := s.db(ctx).Model(&SSHKey{}).Where("user_id = ? AND last_used_at IS NOT NULL", userID).Count(&n).Error
	if err != nil {
		return 0, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "countUsedSSHKeysOfUser")
	}
	return n, nil
}
