package store

import (
	"context"
	"time"

	"github.com/rm4n0s/errors"
)

// SSHKey is a public key of a user that may fetch sign-in codes from the SSH
// server (SPEC §7.16). Its fingerprint is unique over all users: a key
// belongs to one user only.
type SSHKey struct {
	ID        uint64
	CreatedAt time.Time
	// UserID is the user the key belongs to.
	UserID uint64 `gorm:"not null;index:idx_ssh_keys_user_id"`
	// Name is the name the user gave the key, 1 to 40 characters.
	Name string `gorm:"not null"`
	// Type is the type of the key, such as ssh-ed25519.
	Type string `gorm:"not null"`
	// Fingerprint is the SHA-256 fingerprint of the key, as
	// ssh.FingerprintSHA256 writes it.
	Fingerprint string `gorm:"not null;uniqueIndex:uq_ssh_keys_fingerprint"`
	// PublicKey is the authorized-key line of the key, normalised and without
	// a comment.
	PublicKey string `gorm:"not null"`
	// LastUsedAt is when the key last fetched a code; nil until then.
	LastUsedAt *time.Time

	// Relation: declared so that GORM creates the foreign key. The store
	// never loads or saves it.
	User *User `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
}

// SSHCode is a one-time sign-in code that the SSH server handed to a user
// (SPEC §7.16). Only the SHA-256 of the code is stored. A newer code marks the
// unused ones of the user as replaced; the rows are kept for a day, so that
// the limit of codes per user is a count of rows.
type SSHCode struct {
	ID        uint64
	CreatedAt time.Time `gorm:"not null;index:idx_ssh_codes_created_at"`
	// UserID is the user the code is for.
	UserID uint64 `gorm:"not null;index:idx_ssh_codes_user_id"`
	// KeyID is the key that fetched the code.
	KeyID uint64 `gorm:"not null"`
	// CodeHash is the SHA-256 of the 8 normalised characters of the code.
	CodeHash  string    `gorm:"not null"`
	ExpiresAt time.Time `gorm:"not null"`
	// UsedAt is when the code was used; nil until then.
	UsedAt *time.Time
	// Replaced is true once a newer code of the user made this one useless.
	Replaced bool `gorm:"not null;default:false"`

	// Relations: declared so that GORM creates the foreign keys. A code goes
	// with its key.
	User *User   `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
	Key  *SSHKey `gorm:"foreignKey:KeyID;constraint:OnDelete:CASCADE"`
}

// SSHCodeRetention is how long the rows of the codes are kept: IssueSSHCode
// deletes the older ones.
const SSHCodeRetention = 24 * time.Hour

// LockUser makes the rest of the transaction the only one that changes the
// keys and codes of the user: another transaction that calls it for the same
// user waits until this one ends. A change that checks a limit (10 keys, 10
// codes in 15 minutes) calls it first. Call it inside Tx. On SQLite it does
// nothing, because a write transaction is already alone.
func (s *Store) LockUser(ctx context.Context, userID uint64) error {
	if s.dialect != dialectPostgres {
		return nil
	}
	err := s.db(ctx).Exec("SELECT id FROM users WHERE id = ? FOR UPDATE", userID).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "lockUser")
	}
	return nil
}

// CreateSSHKey inserts k and sets its ID. It fails with TagConflict when the
// fingerprint is already registered, by anyone.
func (s *Store) CreateSSHKey(ctx context.Context, k *SSHKey) error {
	err := s.db(ctx).Create(k).Error
	if err != nil {
		switch classify(err) {
		case failureConflict:
			return errors.NewErr(TagConflict, err).SetMetadata("op", "createSSHKey")
		default:
			return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "createSSHKey")
		}
	}
	return nil
}

// SSHKeysOfUser returns the keys of the user, oldest first.
func (s *Store) SSHKeysOfUser(ctx context.Context, userID uint64) ([]SSHKey, error) {
	var keys []SSHKey
	err := s.db(ctx).Where("user_id = ?", userID).Order("id").Find(&keys).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "sshKeysOfUser")
	}
	return keys, nil
}

// CountSSHKeysOfUser returns the number of keys of the user.
func (s *Store) CountSSHKeysOfUser(ctx context.Context, userID uint64) (int64, error) {
	var n int64
	err := s.db(ctx).Model(&SSHKey{}).Where("user_id = ?", userID).Count(&n).Error
	if err != nil {
		return 0, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "countSSHKeysOfUser")
	}
	return n, nil
}

// SSHKeyByID returns the key with the ID. It fails with TagNotFound when there
// is none.
func (s *Store) SSHKeyByID(ctx context.Context, id uint64) (SSHKey, error) {
	var k SSHKey
	err := s.db(ctx).First(&k, id).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return SSHKey{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "sshKeyByID")
		default:
			return SSHKey{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "sshKeyByID")
		}
	}
	return k, nil
}

// SSHKeyByFingerprint returns the key with the fingerprint. It fails with
// TagNotFound when there is none.
func (s *Store) SSHKeyByFingerprint(ctx context.Context, fingerprint string) (SSHKey, error) {
	var k SSHKey
	err := s.db(ctx).Where("fingerprint = ?", fingerprint).First(&k).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return SSHKey{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "sshKeyByFingerprint")
		default:
			return SSHKey{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "sshKeyByFingerprint")
		}
	}
	return k, nil
}

// DeleteSSHKey deletes the key with the ID; its codes go with it. It fails
// with TagNotFound when there is none.
func (s *Store) DeleteSSHKey(ctx context.Context, id uint64) error {
	res := s.db(ctx).Delete(&SSHKey{}, id)
	if res.Error != nil {
		return errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "deleteSSHKey")
	}
	if res.RowsAffected == 0 {
		return errors.New(TagNotFound, "no SSH key with this ID", "op", "deleteSSHKey", "keyID", id)
	}
	return nil
}

// TouchSSHKey sets the last use of the key to at. It does nothing for a key
// that is gone.
func (s *Store) TouchSSHKey(ctx context.Context, id uint64, at time.Time) error {
	err := s.db(ctx).Model(&SSHKey{}).Where("id = ?", id).Update("last_used_at", at.UTC()).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "touchSSHKey")
	}
	return nil
}

// IssueSSHCode stores the code c and sets its ID. In one go it marks the
// unused codes of the user as replaced and deletes the codes made before
// olderThan (SSHCodeRetention before now). Call it inside Tx.
func (s *Store) IssueSSHCode(ctx context.Context, c *SSHCode, olderThan time.Time) error {
	err := s.db(ctx).Where("created_at < ?", olderThan.UTC()).Delete(&SSHCode{}).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "issueSSHCode")
	}
	err = s.db(ctx).Model(&SSHCode{}).
		Where("user_id = ? AND used_at IS NULL AND NOT replaced", c.UserID).
		Update("replaced", true).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "issueSSHCode")
	}
	if err := s.db(ctx).Create(c).Error; err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "issueSSHCode")
	}
	return nil
}

// CountSSHCodesSince returns how many codes of the user were made at or after
// since, replaced and used ones included.
func (s *Store) CountSSHCodesSince(ctx context.Context, userID uint64, since time.Time) (int64, error) {
	var n int64
	err := s.db(ctx).Model(&SSHCode{}).
		Where("user_id = ? AND created_at >= ?", userID, since.UTC()).
		Count(&n).Error
	if err != nil {
		return 0, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "countSSHCodesSince")
	}
	return n, nil
}

// ConsumeSSHCode marks the code of the user with the hash as used, and returns
// it, when it is unused, not replaced and not expired at now. The marking is
// one conditional update, so that a code is used once even when two requests
// race: the loser finds no row to change. It fails with TagNotFound when no
// such code exists. Nothing in the services calls it yet (step 19.2).
func (s *Store) ConsumeSSHCode(ctx context.Context, userID uint64, codeHash string, now time.Time) (SSHCode, error) {
	var code SSHCode
	err := s.db(ctx).
		Where("user_id = ? AND code_hash = ? AND used_at IS NULL AND NOT replaced AND expires_at > ?",
			userID, codeHash, now.UTC()).
		Order("id DESC").First(&code).Error
	if err != nil {
		switch classify(err) {
		case failureNotFound:
			return SSHCode{}, errors.NewErr(TagNotFound, err).SetMetadata("op", "consumeSSHCode")
		default:
			return SSHCode{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "consumeSSHCode")
		}
	}
	res := s.db(ctx).Model(&SSHCode{}).
		Where("id = ? AND used_at IS NULL AND NOT replaced AND expires_at > ?", code.ID, now.UTC()).
		Update("used_at", now.UTC())
	if res.Error != nil {
		return SSHCode{}, errors.NewErr(TagQueryFailed, res.Error).SetMetadata("op", "consumeSSHCode")
	}
	if res.RowsAffected == 0 {
		return SSHCode{}, errors.New(TagNotFound, "no usable code", "op", "consumeSSHCode")
	}
	used := now.UTC()
	code.UsedAt = &used
	return code, nil
}
