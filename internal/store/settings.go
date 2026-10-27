package store

import (
	"context"
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

	// Relation: declared so that GORM creates the foreign key.
	User *User `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
}

// DefaultUserSettings returns the settings of a user who saved none: the
// theme System, the sound on and the browser notifications off.
func DefaultUserSettings(userID uint64) UserSettings {
	return UserSettings{UserID: userID, Theme: ThemeSystem, Sound: true}
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

// SaveUserSettings writes the theme, the sound and the browser notifications
// of the settings of in.UserID: it makes the row or replaces its values. It
// fails with TagQueryFailed when the user does not exist or the theme is not
// one of the three.
func (s *Store) SaveUserSettings(ctx context.Context, in *UserSettings) error {
	err := s.db(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"theme", "sound", "browser_notifications"}),
	}).Create(in).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "saveUserSettings")
	}
	return nil
}
