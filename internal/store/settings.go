package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rm4n0s/errors"
	"gorm.io/gorm/clause"
)

// Theme is the look a user chose (SPEC §7.14, D57).
type Theme string

const (
	// ThemeSystem follows the setting of the user's device; it is the default.
	ThemeSystem Theme = "system"
	// ThemeLight is the light look.
	ThemeLight Theme = "light"
	// ThemeDark is the dark look.
	ThemeDark Theme = "dark"
)

// NotifyLevel says what notifies a user, globally or in one place (SPEC
// §7.15).
type NotifyLevel string

const (
	// NotifyAll notifies about every new post and message.
	NotifyAll NotifyLevel = "all"
	// NotifyMentions notifies about mentions, replies, assignments, additions
	// and (for conversations) every message; it is the global default.
	NotifyMentions NotifyLevel = "mentions"
	// NotifyNothing notifies about nothing.
	NotifyNothing NotifyLevel = "nothing"
)

// SoundName names one of the four sounds of notify.js (SPEC §7.15).
type SoundName string

const (
	// SoundPurr is the default sound: a low, warm rumble.
	SoundPurr SoundName = "purr"
	// SoundChirp is two quick rising notes.
	SoundChirp SoundName = "chirp"
	// SoundBell is one soft bell.
	SoundBell SoundName = "bell"
	// SoundMeow is a sweep up and down.
	SoundMeow SoundName = "meow"
)

// RepliesMode says which comments notify a user (SPEC §7.15).
type RepliesMode string

const (
	// RepliesFollowing notifies about every new comment under a post the user
	// follows; it is the default.
	RepliesFollowing RepliesMode = "following"
	// RepliesDirect notifies only about a comment under the user's own post or
	// comment.
	RepliesDirect RepliesMode = "direct"
)

// UserSettings holds the preferences of one user (SPEC D49), which follow the
// user to every browser and client. A user has no row until they save a
// setting; UserSettingsOf then returns the defaults.
type UserSettings struct {
	ID        uint64
	CreatedAt time.Time
	// UserID is the user the settings belong to. It is unique.
	UserID uint64 `gorm:"not null;uniqueIndex:uq_user_settings_user_id"`
	// Theme is the look the user chose.
	Theme Theme `gorm:"not null;check:chk_user_settings_theme,theme IN ('system','light','dark')"`
	// Sound is true when the user wants a sound for what is new (default true).
	Sound bool `gorm:"not null"`
	// BrowserNotifications is true when the user wants notifications of the
	// browser (default false).
	BrowserNotifications bool `gorm:"not null"`
	// Notify is what rings the sound and shows a browser notification: all,
	// mentions (the default) or nothing. The bell is not affected.
	Notify NotifyLevel `gorm:"not null;default:mentions;check:chk_user_settings_notify,notify IN ('all','mentions','nothing')"`
	// SoundName is which sound plays (default purr).
	SoundName SoundName `gorm:"not null;default:purr;check:chk_user_settings_sound_name,sound_name IN ('purr','chirp','bell','meow')"`
	// Replies says which comments notify the user (default following).
	Replies RepliesMode `gorm:"not null;default:following;check:chk_user_settings_replies,replies IN ('following','direct')"`
	// KeywordsJSON is the list of the mention keywords as JSON text (default
	// "[]"); use Keywords and SetKeywords.
	KeywordsJSON string `gorm:"column:keywords;not null;default:[]"`
	// GroupMentionsOff is true when the user turned off @room, @all and @here
	// (default false). It is stored inverted because GORM replaces a false
	// value by the column default when it is true; use GroupMentions.
	GroupMentionsOff bool `gorm:"not null;default:false"`

	// Relation: declared so that GORM creates the foreign key.
	User *User `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
}

// Keywords returns the mention keywords of the user, in the order they were
// saved. A damaged list is no keywords.
func (u UserSettings) Keywords() []string {
	var out []string
	if u.KeywordsJSON == "" || json.Unmarshal([]byte(u.KeywordsJSON), &out) != nil {
		return nil
	}
	return out
}

// SetKeywords sets the mention keywords. The caller has checked them.
func (u *UserSettings) SetKeywords(keywords []string) {
	if len(keywords) == 0 {
		u.KeywordsJSON = "[]"
		return
	}
	// A list of strings always encodes.
	raw, _ := json.Marshal(keywords)
	u.KeywordsJSON = string(raw)
}

// GroupMentions reports whether @room, @all and @here reach the user (default
// true).
func (u UserSettings) GroupMentions() bool { return !u.GroupMentionsOff }

// SetGroupMentions turns the group mentions on or off.
func (u *UserSettings) SetGroupMentions(on bool) { u.GroupMentionsOff = !on }

// DefaultUserSettings returns the settings of a user who saved none: the
// theme System, the sound on (Purr), the browser notifications off, notify
// about mentions and replies in followed threads.
func DefaultUserSettings(userID uint64) UserSettings {
	return UserSettings{
		UserID: userID, Theme: ThemeSystem, Sound: true,
		Notify: NotifyMentions, SoundName: SoundPurr, Replies: RepliesFollowing, KeywordsJSON: "[]",
	}
}

// UserSettingsOf returns the settings of the user, or the defaults when the
// user has saved none. It does not check that the user exists.
func (s *Store) UserSettingsOf(ctx context.Context, userID uint64) (UserSettings, error) {
	var rows []UserSettings
	err := s.db(ctx).Where("user_id = ?", userID).Limit(1).Find(&rows).Error
	if err != nil {
		return UserSettings{}, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "userSettingsOf")
	}
	if len(rows) == 0 {
		return DefaultUserSettings(userID), nil
	}
	return rows[0], nil
}

// SaveUserSettings writes the settings of in.UserID: it makes the row or
// replaces its values. It fails with TagQueryFailed when the user does not
// exist or a value is not one of its choices.
func (s *Store) SaveUserSettings(ctx context.Context, in *UserSettings) error {
	err := s.db(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"theme", "sound", "browser_notifications", "notify", "sound_name", "replies", "keywords", "group_mentions_off"}),
	}).Create(in).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "saveUserSettings")
	}
	return nil
}
