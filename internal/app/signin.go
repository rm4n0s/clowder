package app

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"log/slog"
	"strings"
	"time"

	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// The rules of the sign-in codes of SPEC §7.16.
const (
	// SSHCodeLifetime is how long a code works.
	SSHCodeLifetime = 60 * time.Second
	// SSHCodeLimit is the most codes a user gets within SSHCodeLimitWindow.
	SSHCodeLimit = 10
	// SSHCodeLimitWindow is the window of SSHCodeLimit.
	SSHCodeLimitWindow = 15 * time.Minute
	// sshCodeLength is the number of characters of a code.
	sshCodeLength = 8
	// sshCodeAlphabet is Crockford's base32: no I, L, O or U.
	sshCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

// IssuedSSHCode is a new sign-in code: the code, which is shown once and never
// stored (only its SHA-256 is), and when it stops working.
type IssuedSSHCode struct {
	// Code is the 8 characters of the code, upper case and without a dash.
	Code string
	// ExpiresAt is when the code stops working.
	ExpiresAt time.Time
}

// Formatted returns the code as it is shown to people: two groups of four
// characters with a dash, K7QF-9MZP.
func (c IssuedSSHCode) Formatted() string {
	if len(c.Code) != sshCodeLength {
		return c.Code
	}
	return c.Code[:4] + "-" + c.Code[4:]
}

// SignIn is the service behind the SSH server of SPEC §7.16: it says whether a
// key may fetch a code and makes the code. Make it with NewSignIn. It is safe
// for concurrent use.
type SignIn struct {
	st     *store.Store
	clk    clock.Clock
	logger *slog.Logger
}

// NewSignIn makes the service. A nil clk is the real clock and a nil logger
// discards the log.
func NewSignIn(st *store.Store, clk clock.Clock, logger *slog.Logger) *SignIn {
	if clk == nil {
		clk = clock.Real()
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &SignIn{st: st, clk: clk, logger: logger}
}

// KeyAllowed reports whether the key with the SHA-256 fingerprint belongs to
// the user with the username, who is not disabled. It answers only true or
// false, so that an unknown user, a disabled user and the key of another user
// look the same to the caller. A failure of the database is logged and answers
// false. The service takes the fingerprint (ssh.FingerprintSHA256) and not the
// key: the SSH server computes it once for both calls.
func (s *SignIn) KeyAllowed(ctx context.Context, username, fingerprint string) bool {
	_, err := s.keyOf(ctx, s.st, username, fingerprint)
	if err == nil {
		return true
	}
	if appErr, ok := errors.FromError(err); !ok || appErr.Tag != TagNotFound {
		s.logger.Error("checking an SSH key failed", "err", err)
	}
	return false
}

// keyOf returns the key with the fingerprint when it belongs to the enabled
// user with the username, and the user. Any other case fails with TagNotFound,
// without saying which.
func (s *SignIn) keyOf(ctx context.Context, st *store.Store, username, fingerprint string) (store.SSHKey, error) {
	user, err := st.UserByUsername(ctx, normalizeUsername(username))
	if err != nil {
		return store.SSHKey{}, err
	}
	key, err := st.SSHKeyByFingerprint(ctx, fingerprint)
	if err != nil {
		return store.SSHKey{}, err
	}
	if key.UserID != user.ID || user.DisabledAt != nil {
		return store.SSHKey{}, errors.New(TagNotFound, "the key does not belong to this user",
			"op", "keyOf")
	}
	return key, nil
}

// IssueCode makes a sign-in code for the user with the username, for the key
// with the fingerprint, which addr (the address of the SSH client) asked for.
// It checks again that the key belongs to the enabled user, and fails with
// TagNotFound when it does not. A user gets at most 10 codes within 15
// minutes, replaced and used ones counted, and the eleventh fails with
// TagSSHCodeLimit. In one transaction it marks the unused codes of the user as
// replaced, deletes the codes older than a day, stores the new code (the
// SHA-256 of its 8 characters) with an expiry of 60 seconds and sets the last
// use of the key. It writes no audit event. It logs the user, the fingerprint
// and addr at Info level, never the code.
func (s *SignIn) IssueCode(ctx context.Context, username, fingerprint, addr string) (IssuedSSHCode, error) {
	var issued IssuedSSHCode
	var userID uint64
	err := s.st.Tx(ctx, func(tx *store.Store) error {
		key, err := s.keyOf(ctx, tx, username, fingerprint)
		if err != nil {
			return err
		}
		userID = key.UserID
		if err := tx.LockUser(ctx, userID); err != nil {
			return err
		}
		now := s.clk.Now().UTC()
		n, err := tx.CountSSHCodesSince(ctx, userID, now.Add(-SSHCodeLimitWindow))
		if err != nil {
			return err
		}
		if n >= SSHCodeLimit {
			return errors.New(TagSSHCodeLimit, "the user fetched too many sign-in codes lately",
				"userID", userID, "limit", SSHCodeLimit)
		}
		code, err := newSSHCode()
		if err != nil {
			return err
		}
		row := &store.SSHCode{
			CreatedAt: now,
			UserID:    userID,
			KeyID:     key.ID,
			CodeHash:  auth.HashToken(code),
			ExpiresAt: now.Add(SSHCodeLifetime),
		}
		if err := tx.IssueSSHCode(ctx, row, now.Add(-store.SSHCodeRetention)); err != nil {
			return err
		}
		if err := tx.TouchSSHKey(ctx, key.ID, now); err != nil {
			return err
		}
		issued = IssuedSSHCode{Code: code, ExpiresAt: row.ExpiresAt}
		return nil
	})
	if err != nil {
		return IssuedSSHCode{}, err
	}
	s.logger.Info("an SSH sign-in code was issued", "user", normalizeUsername(username), "userID", userID,
		"fingerprint", fingerprint, "addr", addr)
	return issued, nil
}

// newSSHCode makes a code: 8 characters of Crockford's base32 from 40 random
// bits, 5 bits for each character, so that every character is uniform.
func newSSHCode() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[3:]); err != nil {
		return "", errors.NewErr(tagSSHCodeRandomFailed, err)
	}
	n := binary.BigEndian.Uint64(raw[:])
	var b [sshCodeLength]byte
	for i := sshCodeLength - 1; i >= 0; i-- {
		b[i] = sshCodeAlphabet[n&31]
		n >>= 5
	}
	return string(b[:]), nil
}

// NormalizeCode returns the code as it is stored and compared: without dashes
// and spaces, in upper case, with Crockford's I and L read as 1 and O as 0.
// The login of step 19.2 uses it on what a person types.
func NormalizeCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		switch r {
		case '-', ' ', '\t', '\n', '\r':
			continue
		case 'I', 'L':
			r = '1'
		case 'O':
			r = '0'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// SSHCodeHash returns the hash that is stored for a code the person typed:
// the SHA-256 of the normalised code. The login of step 19.2 uses it.
func SSHCodeHash(typed string) string {
	return auth.HashToken(NormalizeCode(typed))
}

const (
	// TagSSHCodeLimit is the tag of a user who asked for more than 10 sign-in
	// codes within 15 minutes (SPEC §7.16).
	TagSSHCodeLimit = "SSHCodeLimit"

	tagSSHCodeRandomFailed = "SSHCodeRandomFailed"
)
