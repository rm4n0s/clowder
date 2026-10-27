package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/rm4n0s/clowder/internal/files"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// Theme is the look a user chose (SPEC §7.14). It is the store's type.
type Theme = store.Theme

// The three themes.
const (
	// ThemeSystem follows the setting of the user's device; it is the default.
	ThemeSystem = store.ThemeSystem
	// ThemeLight is the light look.
	ThemeLight = store.ThemeLight
	// ThemeDark is the dark look.
	ThemeDark = store.ThemeDark
)

// The names of the fields of the account forms in the metadata "fields" of a
// TagInvalidInput error.
const (
	// FieldCurrentPassword names the current password of a password change.
	FieldCurrentPassword = "currentPassword"
	// FieldNewPassword names the new password of a password change.
	FieldNewPassword = "newPassword"
	// FieldNewPasswordConfirm names the repetition of the new password; the
	// handlers check it, since the service takes the password once.
	FieldNewPasswordConfirm = "newPasswordConfirm"
	// FieldTheme names the theme of the settings.
	FieldTheme = "theme"
)

// Settings are the preferences of a user (SPEC §7.14, §7.15), which follow the
// user to every browser and client.
type Settings struct {
	// Theme is the look the user chose.
	Theme Theme
	// Sound is true when the user wants a sound for what is new.
	Sound bool
	// BrowserNotifications is true when the user wants notifications of the
	// browser.
	BrowserNotifications bool
}

// SettingsUpdate is a change of the settings. A nil field keeps the value.
type SettingsUpdate struct {
	// Theme is the new theme.
	Theme *Theme
	// Sound is the new value of the sound.
	Sound *bool
	// BrowserNotifications is the new value of the browser notifications.
	BrowserNotifications *bool
}

// PasswordChange is what a person gives to change their password.
type PasswordChange struct {
	// CurrentPassword is the password the person has now.
	CurrentPassword string
	// NewPassword is the password to set: at least 12 characters, and not the
	// current one.
	NewPassword string
	// ClientIP is the address of the client, for the login limiter.
	ClientIP string
}

// Messages of the account forms.
const (
	messageWrongPassword = "This is not your password."
	messageSamePassword  = "Choose a password different from the current one."
)

// activeUser returns the row of the actor's account. An account that does not
// exist or is disabled fails with TagUnauthenticated: the same answer a
// request with such a session gets.
func (a *Accounts) activeUser(ctx context.Context, actor Actor) (store.User, error) {
	return a.activeUserOn(ctx, a.st, actor)
}

// publishUser tells the open pages and clients of a user that the profile or
// the settings changed. Only the user's own topic hears it (SPEC §7.14): the
// pages of other people show the new name or avatar the next time they are
// opened.
func (a *Accounts) publishUser(userID uint64, kind string) {
	a.pub.Publish(live.Event{Topic: live.UserTopic(userID), Kind: kind, UserID: userID})
}

// UpdateProfile sets the display name of the actor, which follows the rules of
// sign-up: trimmed, 1 to 64 characters. It returns the user as it is now and
// publishes live.KindUserUpdated when the name changed. A name that breaks the
// rule fails with TagInvalidInput (field "displayName"); an account that is
// disabled or gone fails with TagUnauthenticated.
func (a *Accounts) UpdateProfile(ctx context.Context, actor Actor, displayName string) (User, error) {
	displayName = strings.TrimSpace(displayName)
	if n := utf8.RuneCountInString(displayName); n < 1 || n > displayNameMaxLength {
		return User{}, errors.New(TagInvalidInput, "the display name is not valid",
			"fields", map[string]string{FieldDisplayName: "Use 1 to 64 characters."})
	}
	var user store.User
	changed := false
	err := a.st.Tx(ctx, func(tx *store.Store) error {
		changed = false
		var err error
		user, err = a.activeUserOn(ctx, tx, actor)
		if err != nil {
			return err
		}
		if user.DisplayName == displayName {
			return nil
		}
		changed = true
		user.DisplayName = displayName
		return tx.SetUserDisplayName(ctx, user.ID, displayName)
	})
	if err != nil {
		return User{}, err
	}
	if changed {
		a.publishUser(user.ID, live.KindUserUpdated)
	}
	return viewOf(user), nil
}

// activeUserOn is activeUser on the Store of a transaction.
func (a *Accounts) activeUserOn(ctx context.Context, tx *store.Store, actor Actor) (store.User, error) {
	user, err := tx.UserByID(ctx, actor.UserID)
	if err != nil {
		if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagNotFound {
			return store.User{}, errors.New(TagUnauthenticated, "the user does not exist", "userID", actor.UserID)
		}
		return store.User{}, err
	}
	if user.DisabledAt != nil {
		return store.User{}, errors.New(TagUnauthenticated, "the user is disabled", "userID", actor.UserID)
	}
	return user, nil
}

// SetAvatar makes the picture that r gives the avatar of the actor: a PNG,
// JPEG or GIF (its first frame) of at most 2 MiB and 4096 by 4096 pixels,
// checked from its header before it is decoded, cut to the centre square,
// scaled to 256 by 256 pixels and stored as a PNG under a new random key. The
// old file is removed after the commit. It returns the user with the new key
// and publishes live.KindUserUpdated. A picture that breaks a rule fails with
// TagInvalidInput (field "avatar"); an Accounts made without a store for avatars
// fails with the tag AvatarsUnavailable; a disabled or missing account fails
// with TagUnauthenticated.
func (a *Accounts) SetAvatar(ctx context.Context, actor Actor, r io.Reader) (User, error) {
	if a.avatars == nil {
		return User{}, errors.New(tagAvatarsUnavailable, "this installation stores no avatars")
	}
	if _, err := a.activeUser(ctx, actor); err != nil {
		return User{}, err
	}
	if err := a.pictures.enter(ctx); err != nil {
		return User{}, err
	}
	png, err := makeAvatar(r)
	a.pictures.leave()
	if err != nil {
		return User{}, err
	}
	saved, err := a.avatars.Save(ctx, bytes.NewReader(png), int64(len(png))+1)
	if err != nil {
		return User{}, err
	}
	var user store.User
	oldKey := ""
	err = a.st.Tx(ctx, func(tx *store.Store) error {
		var err error
		user, err = a.activeUserOn(ctx, tx, actor)
		if err != nil {
			return err
		}
		oldKey = user.AvatarKey
		user.AvatarKey = saved.Key
		return tx.SetUserAvatarKey(ctx, user.ID, saved.Key)
	})
	if err != nil {
		a.cleaner.remove(context.WithoutCancel(ctx), []string{saved.Key})
		return User{}, err
	}
	if oldKey != "" {
		a.cleaner.remove(context.WithoutCancel(ctx), []string{oldKey})
	}
	a.publishUser(user.ID, live.KindUserUpdated)
	return viewOf(user), nil
}

// RemoveAvatar removes the avatar of the actor, which brings back the initials.
// A user without an avatar is not an error. The file is removed after the
// commit, and live.KindUserUpdated is published when there was one. A disabled
// or missing account fails with TagUnauthenticated.
func (a *Accounts) RemoveAvatar(ctx context.Context, actor Actor) (User, error) {
	var user store.User
	oldKey := ""
	err := a.st.Tx(ctx, func(tx *store.Store) error {
		oldKey = ""
		var err error
		user, err = a.activeUserOn(ctx, tx, actor)
		if err != nil {
			return err
		}
		if user.AvatarKey == "" {
			return nil
		}
		oldKey = user.AvatarKey
		user.AvatarKey = ""
		return tx.SetUserAvatarKey(ctx, user.ID, "")
	})
	if err != nil {
		return User{}, err
	}
	if oldKey != "" {
		if a.avatars != nil {
			a.cleaner.remove(context.WithoutCancel(ctx), []string{oldKey})
		}
		a.publishUser(user.ID, live.KindUserUpdated)
	}
	return viewOf(user), nil
}

// OpenAvatar opens the avatar file of the user userID for reading, with its
// key. Any user who is logged in may read any avatar. A user without an avatar,
// an unknown user and a store without avatars fail with TagNotFound, without
// telling which; a disabled or missing actor fails with TagUnauthenticated.
// The caller closes the file.
func (a *Accounts) OpenAvatar(ctx context.Context, actor Actor, userID uint64) (*os.File, string, error) {
	if _, err := a.activeUser(ctx, actor); err != nil {
		return nil, "", err
	}
	user, err := a.st.UserByID(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	if user.AvatarKey == "" || a.avatars == nil {
		return nil, "", errors.New(TagNotFound, "the user has no avatar", "userID", userID)
	}
	f, err := a.avatars.Open(user.AvatarKey)
	if err != nil {
		if appErr, ok := errors.FromError(err); ok && appErr.Tag == files.TagNotFound {
			return nil, "", errors.New(TagNotFound, "the avatar file is gone", "userID", userID)
		}
		return nil, "", err
	}
	return f, user.AvatarKey, nil
}

// ChangePassword sets a new password for the actor. The current password is
// checked through the hashing gate; a wrong one counts as a failed login of the
// username in the login limiter, and a username or address that the limiter
// blocks gets TagRateLimited before the password is looked at. In one
// transaction it stores the new hash, deletes every session of the user but
// sessionID (browsers and clients alike) and writes the audit event
// user.password_changed; after the commit it publishes live.KindUserChanged,
// which closes the sockets and streams of the other sessions.
//
// A new password that is shorter than 12 characters or longer than 1024 bytes
// fails with TagInvalidInput (field "newPassword"), as does one equal to the
// current password; a wrong current password fails with TagInvalidInput (field
// "currentPassword"). A disabled or missing account fails with
// TagUnauthenticated.
func (a *Accounts) ChangePassword(ctx context.Context, actor Actor, sessionID uint64, in PasswordChange) error {
	if n := utf8.RuneCountInString(in.NewPassword); n < passwordMinLength {
		return errors.New(TagInvalidInput, "the new password is too short",
			"fields", map[string]string{FieldNewPassword: "Use at least 12 characters."})
	}
	if len(in.NewPassword) > passwordMaxBytes {
		return errors.New(TagInvalidInput, "the new password is too long",
			"fields", map[string]string{FieldNewPassword: "Use at most 1024 bytes."})
	}
	user, err := a.activeUser(ctx, actor)
	if err != nil {
		return err
	}
	if !a.limiter.Allow(ctx, user.Username, in.ClientIP) {
		return errors.New(TagRateLimited, "too many failed password attempts",
			"username", limiterKey(user.Username), "ip", in.ClientIP)
	}
	match := false
	if len(in.CurrentPassword) <= passwordMaxBytes {
		match, err = a.verify(ctx, in.CurrentPassword, user.PasswordHash)
		if err != nil {
			return err
		}
	}
	if !match {
		a.limiter.Fail(ctx, user.Username, in.ClientIP)
		return errors.New(TagInvalidInput, "the current password is wrong",
			"fields", map[string]string{FieldCurrentPassword: messageWrongPassword})
	}
	if in.NewPassword == in.CurrentPassword {
		return errors.New(TagInvalidInput, "the new password is the current one",
			"fields", map[string]string{FieldNewPassword: messageSamePassword})
	}
	hash, err := a.hash(ctx, in.NewPassword)
	if err != nil {
		return err
	}
	err = a.st.Tx(ctx, func(tx *store.Store) error {
		if _, err := a.activeUserOn(ctx, tx, actor); err != nil {
			return err
		}
		if err := tx.SetUserPasswordHash(ctx, user.ID, hash); err != nil {
			return err
		}
		if err := tx.DeleteSessionsOfUserExcept(ctx, user.ID, sessionID); err != nil {
			return err
		}
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    user.ID,
			Action:     ActionUserPasswordChanged,
			RoomID:     store.NoRoom,
			EntityType: EntityUser,
			EntityID:   user.ID,
			Summary:    user.DisplayName + " changed their password",
		})
	})
	if err != nil {
		return err
	}
	// The other pages and clients that are open read their session again and
	// end.
	a.publishUser(user.ID, live.KindUserChanged)
	return nil
}

// Settings returns the settings of the actor: the defaults (the theme System,
// the sound on, the browser notifications off) while none are saved. A disabled
// or missing account fails with TagUnauthenticated.
func (a *Accounts) Settings(ctx context.Context, actor Actor) (Settings, error) {
	if _, err := a.activeUser(ctx, actor); err != nil {
		return Settings{}, err
	}
	row, err := a.st.UserSettingsOf(ctx, actor.UserID)
	if err != nil {
		return Settings{}, err
	}
	return settingsOf(row), nil
}

// UpdateSettings changes the settings of the actor; a nil field of update keeps
// its value. It returns the settings as they are now and publishes
// live.KindSettingsChanged when something changed. An unknown theme fails with
// TagInvalidInput (field "theme"); a disabled or missing account fails with
// TagUnauthenticated.
func (a *Accounts) UpdateSettings(ctx context.Context, actor Actor, update SettingsUpdate) (Settings, error) {
	if update.Theme != nil {
		switch *update.Theme {
		case ThemeSystem, ThemeLight, ThemeDark:
		default:
			return Settings{}, errors.New(TagInvalidInput, "the theme does not exist",
				"fields", map[string]string{FieldTheme: "Choose System, Light or Dark."})
		}
	}
	var row store.UserSettings
	changed := false
	err := a.st.Tx(ctx, func(tx *store.Store) error {
		changed = false
		if _, err := a.activeUserOn(ctx, tx, actor); err != nil {
			return err
		}
		var err error
		row, err = tx.UserSettingsOf(ctx, actor.UserID)
		if err != nil {
			return err
		}
		before := row
		if update.Theme != nil {
			row.Theme = *update.Theme
		}
		if update.Sound != nil {
			row.Sound = *update.Sound
		}
		if update.BrowserNotifications != nil {
			row.BrowserNotifications = *update.BrowserNotifications
		}
		if row.Theme == before.Theme && row.Sound == before.Sound && row.BrowserNotifications == before.BrowserNotifications {
			return nil
		}
		changed = true
		return tx.SaveUserSettings(ctx, &row)
	})
	if err != nil {
		return Settings{}, err
	}
	if changed {
		a.publishUser(actor.UserID, live.KindSettingsChanged)
	}
	return settingsOf(row), nil
}

// settingsOf makes the Settings of a row.
func settingsOf(row store.UserSettings) Settings {
	return Settings{Theme: row.Theme, Sound: row.Sound, BrowserNotifications: row.BrowserNotifications}
}

const tagAvatarsUnavailable = "AvatarsUnavailable"
