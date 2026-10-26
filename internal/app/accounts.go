package app

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// The rules of SPEC §7.1 for what a person chooses at sign-up.
const (
	usernameMinLength    = 3
	usernameMaxLength    = 32
	displayNameMaxLength = 64
	passwordMinLength    = 12
	// passwordMaxBytes bounds the work of hashing a password.
	passwordMaxBytes = 1024
)

// The names of the fields in the metadata "fields" of a TagInvalidInput error.
const (
	// FieldUsername names the username of a sign-up or a login.
	FieldUsername = "username"
	// FieldDisplayName names the display name of a sign-up.
	FieldDisplayName = "displayName"
	// FieldPassword names the password of a sign-up.
	FieldPassword = "password"
	// FieldRole names the role of an invitation or a role change.
	FieldRole = "role"
	// FieldUser names the user an admin acts on.
	FieldUser = "user"
	// FieldForm names a problem of a whole form, not of one field: a login
	// with the wrong username or password.
	FieldForm = "form"
)

// User is a user as the services show it: the data of a person without the
// password hash.
type User struct {
	// ID is the ID of the user.
	ID uint64
	// Username is the unique, lower-case name used to log in.
	Username string
	// DisplayName is the name shown to other people.
	DisplayName string
	// Role is what the user may do.
	Role store.Role
	// Disabled is true when an admin disabled the account.
	Disabled bool
	// CreatedAt is when the user signed up.
	CreatedAt time.Time
}

// IsAdmin reports whether the user has the admin role.
func (u User) IsAdmin() bool {
	return u.Role == store.RoleAdmin
}

// IssuedInvitation is a new invitation: the token, which is shown once and
// never stored, and the facts about it.
type IssuedInvitation struct {
	// Token is the secret in the link, <base URL>/invite/<token>.
	Token string
	// Role is the role of the user who signs up with it.
	Role store.Role
	// ExpiresAt is when the invitation stops working.
	ExpiresAt time.Time
}

// Accounts is the service of invitations, sign-up, login, sessions and user
// management (SPEC §7.1). Make it with NewAccounts. It is safe for concurrent
// use.
type Accounts struct {
	st                 *store.Store
	clk                clock.Clock
	hasher             auth.Hasher
	invitationLifetime time.Duration
	sessionLifetime    time.Duration
	limiter            *LoginLimiter
}

// AccountsConfig holds the settings and the injected parts of Accounts.
type AccountsConfig struct {
	// InvitationLifetime is how long an invitation made now stays valid.
	InvitationLifetime time.Duration
	// SessionLifetime is how long a session lasts from its last use.
	SessionLifetime time.Duration
	// Limiter limits the failed logins. Nil makes the default one of SPEC
	// §7.1 (5 failures in 15 minutes), on the clock of the service.
	Limiter *LoginLimiter
}

// NewAccounts makes the service. Time is counted on clk; a nil clk is the real
// clock.
func NewAccounts(st *store.Store, clk clock.Clock, hasher auth.Hasher, cfg AccountsConfig) *Accounts {
	if clk == nil {
		clk = clock.Real()
	}
	limiter := cfg.Limiter
	if limiter == nil {
		limiter = NewLoginLimiter(clk)
	}
	return &Accounts{
		st:                 st,
		clk:                clk,
		hasher:             hasher,
		invitationLifetime: cfg.InvitationLifetime,
		sessionLifetime:    cfg.SessionLifetime,
		limiter:            limiter,
	}
}

// CreateInvitation makes an invitation for a new user with the given role.
// Only admins may. It fails with TagForbidden for anyone else and with
// TagInvalidInput (field "role") for a role that does not exist.
func (a *Accounts) CreateInvitation(ctx context.Context, actor Actor, role store.Role) (IssuedInvitation, error) {
	if !actor.IsAdmin() {
		return IssuedInvitation{}, errors.New(TagForbidden, "only admins make invitations",
			"userID", actor.UserID)
	}
	if !validRole(role) {
		return IssuedInvitation{}, errors.New(TagInvalidInput, "the role does not exist",
			"fields", map[string]string{FieldRole: "Choose admin or member."})
	}
	return a.issueInvitation(ctx, &actor.UserID, role)
}

// CreateSystemInvitation makes an invitation that no user made: the one of
// "clowder invite" on the command line, which has no actor. Nothing in the web
// layer calls it. It fails with TagInvalidInput (field "role") for a role that
// does not exist.
func (a *Accounts) CreateSystemInvitation(ctx context.Context, role store.Role) (IssuedInvitation, error) {
	if !validRole(role) {
		return IssuedInvitation{}, errors.New(TagInvalidInput, "the role does not exist",
			"fields", map[string]string{FieldRole: "Choose admin or member."})
	}
	return a.issueInvitation(ctx, nil, role)
}

// issueInvitation stores an invitation made by createdBy (nil for the command
// line) and returns its token.
func (a *Accounts) issueInvitation(ctx context.Context, createdBy *uint64, role store.Role) (IssuedInvitation, error) {
	token, err := auth.NewToken()
	if err != nil {
		return IssuedInvitation{}, err
	}
	expires := a.clk.Now().UTC().Add(a.invitationLifetime)
	inv := &store.Invitation{
		TokenHash: auth.HashToken(token),
		Role:      role,
		CreatedBy: createdBy,
		ExpiresAt: expires,
	}
	if err := a.st.CreateInvitation(ctx, inv); err != nil {
		return IssuedInvitation{}, err
	}
	return IssuedInvitation{Token: token, Role: role, ExpiresAt: expires}, nil
}

// InvitationValid returns nil when token is the token of an invitation that is
// not used and not expired. For a used, expired or unknown one it returns
// TagNotFound, and does not tell the caller which; the metadata "reason" does,
// for the log.
func (a *Accounts) InvitationValid(ctx context.Context, token string) error {
	_, err := a.usableInvitation(ctx, a.st, token)
	return err
}

// usableInvitation finds the invitation of token in st and checks that it can
// still be used. It is the one place that decides it, for InvitationValid and
// for SignUp (which asks again inside its transaction, with the Store of the
// transaction).
func (a *Accounts) usableInvitation(ctx context.Context, st *store.Store, token string) (store.Invitation, error) {
	inv, err := st.InvitationByTokenHash(ctx, auth.HashToken(token))
	if err != nil {
		return store.Invitation{}, err
	}
	if inv.UsedAt != nil {
		return store.Invitation{}, errors.New(TagNotFound, "the invitation is not valid",
			"reason", "used", "invitationID", inv.ID)
	}
	if !a.clk.Now().Before(inv.ExpiresAt) {
		return store.Invitation{}, errors.New(TagNotFound, "the invitation is not valid",
			"reason", "expired", "invitationID", inv.ID)
	}
	return inv, nil
}

// SignUp makes a user from an invitation and uses the invitation up. The
// username is lower-cased and trimmed, the display name trimmed (SPEC §7.1). It
// fails with TagNotFound for a used, expired or unknown invitation, and with
// TagInvalidInput for input that breaks a rule, with all the problems in the
// metadata "fields" (a username that is taken is one of them). The new user
// has the role of the invitation.
func (a *Accounts) SignUp(ctx context.Context, token, username, displayName, password string) (User, error) {
	// Ask before the costly hashing, and again inside the transaction.
	if _, err := a.usableInvitation(ctx, a.st, token); err != nil {
		return User{}, err
	}
	username = normalizeUsername(username)
	displayName = strings.TrimSpace(displayName)
	if fields := signUpProblems(username, displayName, password); len(fields) > 0 {
		return User{}, errors.New(TagInvalidInput, "the sign-up form has mistakes", "fields", fields)
	}
	hash, err := a.hasher.Hash(password)
	if err != nil {
		return User{}, err
	}

	var created store.User
	err = a.st.Tx(ctx, func(tx *store.Store) error {
		inv, err := a.usableInvitation(ctx, tx, token)
		if err != nil {
			return err
		}
		// Look first, so that a taken name is a mistake in the form. The unique
		// index still decides when two sign-ups race: that one is TagConflict.
		_, err = tx.UserByUsername(ctx, username)
		if err == nil {
			return errors.New(TagInvalidInput, "the username is taken",
				"fields", map[string]string{FieldUsername: "This username is taken."})
		}
		if appErr, ok := errors.FromError(err); !ok || appErr.Tag != store.TagNotFound {
			return err
		}
		created = store.User{
			Username:     username,
			DisplayName:  displayName,
			PasswordHash: hash,
			Role:         inv.Role,
		}
		if err := tx.CreateUser(ctx, &created); err != nil {
			return err
		}
		return tx.MarkInvitationUsed(ctx, inv.ID, created.ID)
	})
	if err != nil {
		return User{}, err
	}
	return viewOf(created), nil
}

// signUpProblems returns the rules of SPEC §7.1 that the (already trimmed)
// values break, as field name to message. It is empty when all hold.
func signUpProblems(username, displayName, password string) map[string]string {
	fields := map[string]string{}
	if n := utf8.RuneCountInString(username); n < usernameMinLength || n > usernameMaxLength {
		fields[FieldUsername] = "Use 3 to 32 characters."
	} else if strings.ContainsFunc(username, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-'
	}) {
		fields[FieldUsername] = "Use only the letters a to z, digits, _ and -."
	}
	if n := utf8.RuneCountInString(displayName); n < 1 || n > displayNameMaxLength {
		fields[FieldDisplayName] = "Use 1 to 64 characters."
	}
	if utf8.RuneCountInString(password) < passwordMinLength {
		fields[FieldPassword] = "Use at least 12 characters."
	} else if len(password) > passwordMaxBytes {
		fields[FieldPassword] = "Use at most 1024 bytes."
	}
	return fields
}

// Users returns every user, ordered by username. Only admins may; anyone else
// gets TagForbidden.
func (a *Accounts) Users(ctx context.Context, actor Actor) ([]User, error) {
	if !actor.IsAdmin() {
		return nil, errors.New(TagForbidden, "only admins list the users", "userID", actor.UserID)
	}
	rows, err := a.st.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	users := make([]User, len(rows))
	for i, row := range rows {
		users[i] = viewOf(row)
	}
	return users, nil
}

// SetRole gives a user a role. Only admins may; anyone else gets TagForbidden.
// It fails with TagInvalidInput (field "role") for a role that does not exist,
// with TagNotFound for an unknown user, and with TagConflict when the user is
// the last active admin and the role is not admin.
func (a *Accounts) SetRole(ctx context.Context, actor Actor, userID uint64, role store.Role) error {
	if !actor.IsAdmin() {
		return errors.New(TagForbidden, "only admins change roles", "userID", actor.UserID)
	}
	if !validRole(role) {
		return errors.New(TagInvalidInput, "the role does not exist",
			"fields", map[string]string{FieldRole: "Choose admin or member."})
	}
	return a.st.Tx(ctx, func(tx *store.Store) error {
		user, err := tx.UserByID(ctx, userID)
		if err != nil {
			return err
		}
		if user.Role == role {
			return nil
		}
		// A disabled admin is not counted, so losing the role changes nothing.
		if user.Role == store.RoleAdmin && user.DisabledAt == nil {
			admins, err := tx.CountActiveAdmins(ctx)
			if err != nil {
				return err
			}
			if admins <= 1 {
				return errors.New(TagConflict, "the last admin cannot lose the admin role",
					"userID", userID)
			}
		}
		return tx.SetUserRole(ctx, userID, role)
	})
}

// SetDisabled disables or enables a user. Disabling also deletes the sessions
// of the user, in the same transaction. Only admins may; anyone else gets
// TagForbidden. It fails with TagInvalidInput (field "user") when an admin
// disables themselves, with TagNotFound for an unknown user, and with
// TagConflict when the user is the last active admin.
func (a *Accounts) SetDisabled(ctx context.Context, actor Actor, userID uint64, disabled bool) error {
	if !actor.IsAdmin() {
		return errors.New(TagForbidden, "only admins disable users", "userID", actor.UserID)
	}
	if disabled && userID == actor.UserID {
		return errors.New(TagInvalidInput, "an admin cannot disable themselves",
			"fields", map[string]string{FieldUser: "You cannot disable your own account."})
	}
	return a.st.Tx(ctx, func(tx *store.Store) error {
		user, err := tx.UserByID(ctx, userID)
		if err != nil {
			return err
		}
		if disabled == (user.DisabledAt != nil) {
			return nil
		}
		if !disabled {
			return tx.SetUserDisabled(ctx, userID, false)
		}
		if user.Role == store.RoleAdmin {
			admins, err := tx.CountActiveAdmins(ctx)
			if err != nil {
				return err
			}
			if admins <= 1 {
				return errors.New(TagConflict, "the last admin cannot be disabled",
					"userID", userID)
			}
		}
		if err := tx.SetUserDisabled(ctx, userID, true); err != nil {
			return err
		}
		return tx.DeleteSessionsOfUser(ctx, userID)
	})
}

// normalizeUsername lower-cases and trims a username as typed.
func normalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// validRole reports whether role is one of the two roles.
func validRole(role store.Role) bool {
	return role == store.RoleAdmin || role == store.RoleMember
}

// viewOf makes the User view of a row, without the password hash.
func viewOf(u store.User) User {
	return User{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Role:        u.Role,
		Disabled:    u.DisabledAt != nil,
		CreatedAt:   u.CreatedAt,
	}
}
