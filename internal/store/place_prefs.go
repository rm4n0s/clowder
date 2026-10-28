package store

import (
	"context"
	"time"

	"github.com/rm4n0s/errors"
	"gorm.io/gorm/clause"
)

// PlaceNotify is what a user chose to be notified about in one room or
// conversation (SPEC §7.15). PlaceNotifyDefault leaves the choice to the
// global setting.
type PlaceNotify string

const (
	// PlaceNotifyDefault follows the global setting (for a conversation:
	// every message, unless the global setting is Nothing).
	PlaceNotifyDefault PlaceNotify = "default"
	// PlaceNotifyAll notifies about every new post or message of the place.
	PlaceNotifyAll PlaceNotify = "all"
	// PlaceNotifyMentions notifies only about mentions of the user.
	PlaceNotifyMentions PlaceNotify = "mentions"
	// PlaceNotifyNothing notifies about nothing.
	PlaceNotifyNothing PlaceNotify = "nothing"
)

// PlaceSound is the choice of the sound in one place.
type PlaceSound string

const (
	// PlaceSoundDefault follows the global sound setting.
	PlaceSoundDefault PlaceSound = "default"
	// PlaceSoundOn plays the sound in this place whatever the global setting.
	PlaceSoundOn PlaceSound = "on"
	// PlaceSoundOff plays no sound in this place.
	PlaceSoundOff PlaceSound = "off"
)

// MarkUnread says what the unread count of a room counts (SPEC §7.17).
type MarkUnread string

const (
	// MarkUnreadAll counts every new post; it is the default.
	MarkUnreadAll MarkUnread = "all"
	// MarkUnreadMentions counts only the mentions of the user.
	MarkUnreadMentions MarkUnread = "mentions"
)

// PlacePref holds the notification preferences of one user in one room or one
// conversation (SPEC §7.15). Exactly one of RoomID and ConversationID is set.
// A missing row means all defaults; the row is deleted when the user leaves
// the place.
type PlacePref struct {
	ID        uint64
	CreatedAt time.Time
	// UserID is the user the preferences belong to.
	UserID uint64 `gorm:"not null;uniqueIndex:uq_place_prefs_user_room,priority:1;uniqueIndex:uq_place_prefs_user_conversation,priority:1"`
	// RoomID is the room of the preferences; nil for a conversation.
	RoomID *uint64 `gorm:"uniqueIndex:uq_place_prefs_user_room,priority:2;check:chk_place_prefs_one_place,(room_id IS NOT NULL AND conversation_id IS NULL) OR (room_id IS NULL AND conversation_id IS NOT NULL)"`
	// ConversationID is the conversation of the preferences; nil for a room.
	ConversationID *uint64 `gorm:"uniqueIndex:uq_place_prefs_user_conversation,priority:2"`
	// Notify is what the user is notified about in this place.
	Notify PlaceNotify `gorm:"not null;default:default;check:chk_place_prefs_notify,notify IN ('default','all','mentions','nothing')"`
	// Sound is the choice of the sound in this place.
	Sound PlaceSound `gorm:"not null;default:default;check:chk_place_prefs_sound,sound IN ('default','on','off')"`
	// IgnoreGroupMentions is true when @room, @all and @here are ignored in
	// this place (SPEC §7.15).
	IgnoreGroupMentions bool `gorm:"not null;default:false"`
	// MarkUnread is what the unread count of a room counts; for rooms only.
	MarkUnread MarkUnread `gorm:"not null;default:all;check:chk_place_prefs_mark_unread,mark_unread IN ('all','mentions')"`
	// Muted is true when the place is muted: nothing rings in it.
	Muted bool `gorm:"not null;default:false"`

	// Relations: declared so that GORM creates the foreign keys. The rows go
	// with the room or conversation they belong to.
	User         *User         `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT"`
	Room         *Room         `gorm:"foreignKey:RoomID;constraint:OnDelete:CASCADE"`
	Conversation *Conversation `gorm:"foreignKey:ConversationID;constraint:OnDelete:CASCADE"`
}

// PlaceKey names one place: a room or a conversation. Exactly one of the two
// IDs is not 0.
type PlaceKey struct {
	// RoomID is the room; 0 for a conversation.
	RoomID uint64
	// ConversationID is the conversation; 0 for a room.
	ConversationID uint64
}

// RoomPlace is the PlaceKey of a room.
func RoomPlace(roomID uint64) PlaceKey { return PlaceKey{RoomID: roomID} }

// ConversationPlace is the PlaceKey of a conversation.
func ConversationPlace(conversationID uint64) PlaceKey {
	return PlaceKey{ConversationID: conversationID}
}

// DefaultPlacePref returns the preferences of a user who changed none in the
// place: the global notify setting, the global sound, nothing ignored, every
// new post counted, not muted. It has no ID.
func DefaultPlacePref(userID uint64, place PlaceKey) PlacePref {
	p := PlacePref{
		UserID: userID, Notify: PlaceNotifyDefault, Sound: PlaceSoundDefault, MarkUnread: MarkUnreadAll,
	}
	if place.RoomID != 0 {
		id := place.RoomID
		p.RoomID = &id
	} else {
		id := place.ConversationID
		p.ConversationID = &id
	}
	return p
}

// where returns the condition of a PlacePref query on the place of key.
func (k PlaceKey) where() (string, uint64) {
	if k.RoomID != 0 {
		return "room_id = ?", k.RoomID
	}
	return "conversation_id = ?", k.ConversationID
}

// PlacePrefOf returns the preferences of the user in the place, and whether
// the user has any: without a row it returns the defaults and false. It does
// not check that the user or the place exists.
func (s *Store) PlacePrefOf(ctx context.Context, userID uint64, place PlaceKey) (PlacePref, bool, error) {
	cond, id := place.where()
	var rows []PlacePref
	err := s.db(ctx).Where("user_id = ?", userID).Where(cond, id).Limit(1).Find(&rows).Error
	if err != nil {
		return PlacePref{}, false, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "placePrefOf", "userID", userID)
	}
	if len(rows) == 0 {
		return DefaultPlacePref(userID, place), false, nil
	}
	return rows[0], true, nil
}

// SavePlacePref writes the preferences in.UserID has in the place of in: it
// makes the row or replaces its values. It fails with TagQueryFailed when the
// user, the room or the conversation does not exist, when a value is not one
// of its choices, or when in names no place or two.
func (s *Store) SavePlacePref(ctx context.Context, in *PlacePref) error {
	target := "room_id"
	if in.RoomID == nil {
		target = "conversation_id"
	}
	err := s.db(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: target}},
		DoUpdates: clause.AssignmentColumns([]string{"notify", "sound", "ignore_group_mentions", "mark_unread", "muted"}),
	}).Create(in).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "savePlacePref")
	}
	return nil
}

// DeletePlacePref deletes the preferences of the user in the place. It does
// nothing when there are none.
func (s *Store) DeletePlacePref(ctx context.Context, userID uint64, place PlaceKey) error {
	cond, id := place.where()
	err := s.db(ctx).Where("user_id = ?", userID).Where(cond, id).Delete(&PlacePref{}).Error
	if err != nil {
		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "deletePlacePref", "userID", userID)
	}
	return nil
}

// PlacePrefsOfUser returns every row of preferences of the user, rooms first,
// then conversations, each by ID.
func (s *Store) PlacePrefsOfUser(ctx context.Context, userID uint64) ([]PlacePref, error) {
	var rows []PlacePref
	err := s.db(ctx).Where("user_id = ?", userID).
		Order("(room_id IS NULL) ASC, room_id ASC, conversation_id ASC").Find(&rows).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", "placePrefsOfUser", "userID", userID)
	}
	return rows, nil
}

// Recipient is a user who may be notified of an event of a place, with the
// settings and the preferences that decide how (SPEC §7.15).
type Recipient struct {
	// UserID is the user.
	UserID uint64
	// Username is the username of the user, lower case, for the detection of
	// @username mentions.
	Username string
	// Settings are the global settings of the user (the defaults when none
	// are saved).
	Settings UserSettings
	// Place are the preferences of the user in the place (the defaults when
	// none are saved).
	Place PlacePref
}

// recipientRow is one row of the query of the recipients; the columns of the
// two left-joined tables are nil when the user has no row there.
type recipientRow struct {
	UserID               uint64
	Username             string
	SNotify              *string
	SSound               *bool
	SSoundName           *string
	SBrowserNotification *bool
	SReplies             *string
	SKeywords            *string
	SGroupMentionsOff    *bool
	PNotify              *string
	PSound               *string
	PIgnoreGroup         *bool
	PMarkUnread          *string
	PMuted               *bool
}

const recipientColumns = `users.id AS user_id, users.username AS username,
user_settings.notify AS s_notify, user_settings.sound AS s_sound, user_settings.sound_name AS s_sound_name,
user_settings.browser_notifications AS s_browser_notification, user_settings.replies AS s_replies, user_settings.keywords AS s_keywords, user_settings.group_mentions_off AS s_group_mentions_off,
place_prefs.notify AS p_notify, place_prefs.sound AS p_sound, place_prefs.ignore_group_mentions AS p_ignore_group,
place_prefs.mark_unread AS p_mark_unread, place_prefs.muted AS p_muted`

// recipient makes the Recipient of a row for the place.
func (r recipientRow) recipient(place PlaceKey) Recipient {
	settings := DefaultUserSettings(r.UserID)
	if r.SNotify != nil {
		settings.Notify = NotifyLevel(*r.SNotify)
	}
	if r.SSound != nil {
		settings.Sound = *r.SSound
	}
	if r.SSoundName != nil {
		settings.SoundName = SoundName(*r.SSoundName)
	}
	if r.SBrowserNotification != nil {
		settings.BrowserNotifications = *r.SBrowserNotification
	}
	if r.SReplies != nil {
		settings.Replies = RepliesMode(*r.SReplies)
	}
	if r.SKeywords != nil {
		settings.KeywordsJSON = *r.SKeywords
	}
	if r.SGroupMentionsOff != nil {
		settings.GroupMentionsOff = *r.SGroupMentionsOff
	}
	pref := DefaultPlacePref(r.UserID, place)
	if r.PNotify != nil {
		pref.Notify = PlaceNotify(*r.PNotify)
	}
	if r.PSound != nil {
		pref.Sound = PlaceSound(*r.PSound)
	}
	if r.PIgnoreGroup != nil {
		pref.IgnoreGroupMentions = *r.PIgnoreGroup
	}
	if r.PMarkUnread != nil {
		pref.MarkUnread = MarkUnread(*r.PMarkUnread)
	}
	if r.PMuted != nil {
		pref.Muted = *r.PMuted
	}
	return Recipient{UserID: r.UserID, Username: r.Username, Settings: settings, Place: pref}
}

// recipients runs the one query that reads the settings and the preferences of
// the users that match members (a condition on users), who are not disabled.
func (s *Store) recipients(ctx context.Context, op string, place PlaceKey, members string, args ...any) ([]Recipient, error) {
	joinPlace := "LEFT JOIN place_prefs ON place_prefs.user_id = users.id AND place_prefs.conversation_id = ?"
	placeID := place.ConversationID
	if place.RoomID != 0 {
		joinPlace = "LEFT JOIN place_prefs ON place_prefs.user_id = users.id AND place_prefs.room_id = ?"
		placeID = place.RoomID
	}
	var rows []recipientRow
	err := s.db(ctx).Table("users").Select(recipientColumns).
		Joins("LEFT JOIN user_settings ON user_settings.user_id = users.id").
		Joins(joinPlace, placeID).
		Where("users.disabled_at IS NULL").
		Where(members, args...).
		Order("users.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, errors.NewErr(TagQueryFailed, err).SetMetadata("op", op)
	}
	out := make([]Recipient, len(rows))
	for i, r := range rows {
		out[i] = r.recipient(place)
	}
	return out, nil
}

// RoomMemberRecipients returns, in one query, the members of the room who are
// not disabled, except exceptUserID, with their settings and their
// preferences in the room. It is what a new post needs to decide whom it
// rings.
func (s *Store) RoomMemberRecipients(ctx context.Context, roomID, exceptUserID uint64) ([]Recipient, error) {
	return s.recipients(ctx, "roomMemberRecipients", RoomPlace(roomID),
		"users.id <> ? AND users.id IN (SELECT user_id FROM room_members WHERE room_id = ?)", exceptUserID, roomID)
}

// ConversationRecipients returns, in one query, the current members of the
// conversation who are not disabled, except exceptUserID, with their settings
// and their preferences in the conversation.
func (s *Store) ConversationRecipients(ctx context.Context, conversationID, exceptUserID uint64) ([]Recipient, error) {
	return s.recipients(ctx, "conversationRecipients", ConversationPlace(conversationID),
		"users.id <> ? AND users.id IN (SELECT user_id FROM conversation_members WHERE conversation_id = ? AND left_at IS NULL)",
		exceptUserID, conversationID)
}

// RecipientsOf returns, in one query, the given users who are not disabled
// with their settings and their preferences in the place. A user who does not
// exist is left out. It is what a notification needs.
func (s *Store) RecipientsOf(ctx context.Context, place PlaceKey, userIDs []uint64) ([]Recipient, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	return s.recipients(ctx, "recipientsOf", place, "users.id IN ?", userIDs)
}
