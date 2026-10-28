package app

import (
	"context"

	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// PlaceNotify is what a user chose to be notified about in one place (SPEC
// §7.15). It is the store's type.
type PlaceNotify = store.PlaceNotify

// The four choices of "Notify me about" in a place.
const (
	// PlaceNotifyDefault follows the global setting.
	PlaceNotifyDefault = store.PlaceNotifyDefault
	// PlaceNotifyAll notifies about every new post or message.
	PlaceNotifyAll = store.PlaceNotifyAll
	// PlaceNotifyMentions notifies only about mentions.
	PlaceNotifyMentions = store.PlaceNotifyMentions
	// PlaceNotifyNothing notifies about nothing.
	PlaceNotifyNothing = store.PlaceNotifyNothing
)

// PlaceSound is the choice of the sound in one place. It is the store's type.
type PlaceSound = store.PlaceSound

// The three choices of the sound in a place.
const (
	// PlaceSoundDefault follows the global sound setting.
	PlaceSoundDefault = store.PlaceSoundDefault
	// PlaceSoundOn plays the sound in the place.
	PlaceSoundOn = store.PlaceSoundOn
	// PlaceSoundOff plays no sound in the place.
	PlaceSoundOff = store.PlaceSoundOff
)

// MarkUnread says what the unread count of a room counts (SPEC §7.17). It is
// the store's type.
type MarkUnread = store.MarkUnread

// The two choices of "Mark as unread".
const (
	// MarkUnreadAll counts every new post; the default.
	MarkUnreadAll = store.MarkUnreadAll
	// MarkUnreadMentions counts only the mentions.
	MarkUnreadMentions = store.MarkUnreadMentions
)

// The names of the fields of the preferences of a place in the metadata
// "fields" of a TagInvalidInput error.
const (
	// FieldPlaceNotify names the "Notify me about" of a place.
	FieldPlaceNotify = "notify"
	// FieldPlaceSound names the sound of a place.
	FieldPlaceSound = "sound"
	// FieldIgnoreGroupMentions names "Ignore @room, @all and @here".
	FieldIgnoreGroupMentions = "ignoreGroupMentions"
	// FieldMarkUnread names "Mark as unread".
	FieldMarkUnread = "markUnread"
	// FieldMuted names the mute.
	FieldMuted = "muted"
)

// PlaceRef names a place: a room or a conversation. Exactly one of the IDs is
// not 0.
type PlaceRef struct {
	// RoomID is the room; 0 for a conversation.
	RoomID uint64
	// ConversationID is the conversation; 0 for a room.
	ConversationID uint64
}

// RoomRef is the PlaceRef of a room.
func RoomRef(roomID uint64) PlaceRef { return PlaceRef{RoomID: roomID} }

// ConversationRef is the PlaceRef of a conversation.
func ConversationRef(conversationID uint64) PlaceRef { return PlaceRef{ConversationID: conversationID} }

// IsRoom reports whether the place is a room.
func (r PlaceRef) IsRoom() bool { return r.RoomID != 0 }

// key is the PlaceKey of the store.
func (r PlaceRef) key() store.PlaceKey {
	return store.PlaceKey{RoomID: r.RoomID, ConversationID: r.ConversationID}
}

// PlacePrefs are the notification preferences of the actor in one place (SPEC
// §7.15). A place without saved preferences has the defaults.
type PlacePrefs struct {
	// Place is the room or conversation.
	Place PlaceRef
	// Direct is true for a conversation of two, which has no group mentions
	// to ignore.
	Direct bool
	// Notify is what the actor is notified about in the place.
	Notify PlaceNotify
	// Sound is the choice of the sound in the place.
	Sound PlaceSound
	// IgnoreGroupMentions is true when @room, @all and @here are ignored in
	// the place (used from step 18.4).
	IgnoreGroupMentions bool
	// MarkUnread is what the unread count of a room counts; MarkUnreadAll
	// for a conversation, which always counts all its messages.
	MarkUnread MarkUnread
	// Muted is true when the place is muted.
	Muted bool
}

// IsDefault reports whether the preferences are all defaults.
func (p PlacePrefs) IsDefault() bool {
	return p.Notify == PlaceNotifyDefault && p.Sound == PlaceSoundDefault && !p.IgnoreGroupMentions &&
		p.MarkUnread == MarkUnreadAll && !p.Muted
}

// PlaceUpdate is a change of the preferences of a place. A nil field keeps the
// value.
type PlaceUpdate struct {
	// Notify is the new "Notify me about".
	Notify *PlaceNotify
	// Sound is the new sound choice.
	Sound *PlaceSound
	// IgnoreGroupMentions is the new value of "Ignore @room, @all and @here".
	IgnoreGroupMentions *bool
	// MarkUnread is the new "Mark as unread"; rooms only.
	MarkUnread *MarkUnread
	// Muted is the new value of the mute.
	Muted *bool
}

// Prefs is the service of the notification preferences of a user in one room
// or conversation (SPEC §7.15) and of the notices that the events of the user's
// stream carry. The global preferences are the settings (Accounts.Settings
// and Accounts.UpdateSettings). Make it with NewPrefs. It is safe for
// concurrent use.
type Prefs struct {
	st  *store.Store
	pub Publisher
}

// NewPrefs makes the service. pub receives the live events of the changes,
// after they are committed; nil publishes nothing.
func NewPrefs(st *store.Store, pub Publisher) *Prefs {
	return &Prefs{st: st, pub: orNop(pub)}
}

// prefsOf makes the PlacePrefs of a row.
func prefsOf(row store.PlacePref, ref PlaceRef, direct bool) PlacePrefs {
	return PlacePrefs{
		Place: ref, Direct: direct, Notify: row.Notify, Sound: row.Sound,
		IgnoreGroupMentions: row.IgnoreGroupMentions, MarkUnread: row.MarkUnread, Muted: row.Muted,
	}
}

// checkPlace checks that the actor may have preferences in the place: the
// account is active; a room must be readable (a private room that the actor
// may not read is TagNotFound, admins included) and the actor a member of it
// (TagForbidden for a visible room that the actor has not joined); a
// conversation must have the actor as a member (TagNotFound otherwise). It
// returns whether the place is a conversation of two.
func (p *Prefs) checkPlace(ctx context.Context, tx *store.Store, actor Actor, ref PlaceRef) (direct bool, err error) {
	user, err := tx.UserByID(ctx, actor.UserID)
	if err != nil {
		if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagNotFound {
			return false, errors.New(TagUnauthenticated, "the user does not exist", "userID", actor.UserID)
		}
		return false, err
	}
	if user.DisabledAt != nil {
		return false, errors.New(TagUnauthenticated, "the user is disabled", "userID", actor.UserID)
	}
	if ref.IsRoom() {
		room, err := loadReadableRoom(ctx, tx, actor, ref.RoomID)
		if err != nil {
			return false, err
		}
		member, err := isMember(ctx, tx, actor.UserID, room.ID)
		if err != nil {
			return false, err
		}
		if !member {
			return false, errors.New(TagForbidden, "only members have notification preferences in a room",
				"userID", actor.UserID, "roomID", room.ID)
		}
		return false, nil
	}
	conv, _, err := loadConversation(ctx, tx, actor, ref.ConversationID)
	if err != nil {
		return false, err
	}
	return conv.Kind == ConversationDirect, nil
}

// Place returns the actor's notification preferences in the room or
// conversation: the defaults while none are saved. See checkPlace for who may
// ask.
func (p *Prefs) Place(ctx context.Context, actor Actor, ref PlaceRef) (PlacePrefs, error) {
	direct, err := p.checkPlace(ctx, p.st, actor, ref)
	if err != nil {
		return PlacePrefs{}, err
	}
	row, _, err := p.st.PlacePrefOf(ctx, actor.UserID, ref.key())
	if err != nil {
		return PlacePrefs{}, err
	}
	return prefsOf(row, ref, direct), nil
}

// UpdatePlace changes the actor's notification preferences in the room or
// conversation; a nil field of update keeps its value. It returns the
// preferences as they are now and publishes live.KindPlacePrefsChanged to the
// actor when something changed. Preferences that are all defaults again are
// deleted. See checkPlace for who may change them. An unknown choice, a mark
// of unread other than "all" in a conversation, and ignoring group mentions in
// a conversation of two fail with TagInvalidInput, with all the problems in
// the metadata "fields".
func (p *Prefs) UpdatePlace(ctx context.Context, actor Actor, ref PlaceRef, update PlaceUpdate) (PlacePrefs, error) {
	var out PlacePrefs
	changed := false
	err := p.st.Tx(ctx, func(tx *store.Store) error {
		changed = false
		direct, err := p.checkPlace(ctx, tx, actor, ref)
		if err != nil {
			return err
		}
		row, found, err := tx.PlacePrefOf(ctx, actor.UserID, ref.key())
		if err != nil {
			return err
		}
		before := row
		if update.Notify != nil {
			row.Notify = *update.Notify
		}
		if update.Sound != nil {
			row.Sound = *update.Sound
		}
		if update.IgnoreGroupMentions != nil {
			row.IgnoreGroupMentions = *update.IgnoreGroupMentions
		}
		if update.MarkUnread != nil {
			row.MarkUnread = *update.MarkUnread
		}
		if update.Muted != nil {
			row.Muted = *update.Muted
		}
		if fields := checkPlacePrefs(row, ref, direct); len(fields) > 0 {
			return errors.New(TagInvalidInput, "the notification preferences have mistakes", "fields", fields)
		}
		out = prefsOf(row, ref, direct)
		if row == before {
			return nil
		}
		changed = true
		if out.IsDefault() {
			if !found {
				return nil
			}
			return tx.DeletePlacePref(ctx, actor.UserID, ref.key())
		}
		return tx.SavePlacePref(ctx, &row)
	})
	if err != nil {
		return PlacePrefs{}, err
	}
	if changed {
		p.pub.Publish(live.Event{
			Topic: live.UserTopic(actor.UserID), Kind: live.KindPlacePrefsChanged,
			UserID: actor.UserID, RoomID: ref.RoomID, ConversationID: ref.ConversationID,
		})
	}
	return out, nil
}

// checkPlacePrefs returns the problems of preferences by field; it is empty
// when they are valid.
func checkPlacePrefs(row store.PlacePref, ref PlaceRef, direct bool) map[string]string {
	fields := map[string]string{}
	switch row.Notify {
	case PlaceNotifyDefault, PlaceNotifyAll, PlaceNotifyMentions, PlaceNotifyNothing:
	default:
		fields[FieldPlaceNotify] = "Choose default, all new activity, mentions or nothing."
	}
	switch row.Sound {
	case PlaceSoundDefault, PlaceSoundOn, PlaceSoundOff:
	default:
		fields[FieldPlaceSound] = "Choose default, on or off."
	}
	switch row.MarkUnread {
	case MarkUnreadAll:
	case MarkUnreadMentions:
		if !ref.IsRoom() {
			fields[FieldMarkUnread] = "A conversation always counts all its messages."
		}
	default:
		fields[FieldMarkUnread] = "Choose all new posts or only mentions."
	}
	if row.IgnoreGroupMentions && direct {
		fields[FieldIgnoreGroupMentions] = "A conversation of two has no group mentions."
	}
	return fields
}

// Places returns the places in which the actor saved preferences (a place with
// the defaults has no row), rooms first by ID and then conversations by ID. A
// place that the actor can no longer read has no row: the rows go with the
// membership. A disabled or missing account fails with TagUnauthenticated.
func (p *Prefs) Places(ctx context.Context, actor Actor) ([]PlacePrefs, error) {
	user, err := p.st.UserByID(ctx, actor.UserID)
	if err != nil {
		if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagNotFound {
			return nil, errors.New(TagUnauthenticated, "the user does not exist", "userID", actor.UserID)
		}
		return nil, err
	}
	if user.DisabledAt != nil {
		return nil, errors.New(TagUnauthenticated, "the user is disabled", "userID", actor.UserID)
	}
	rows, err := p.st.PlacePrefsOfUser(ctx, actor.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]PlacePrefs, 0, len(rows))
	for _, row := range rows {
		ref := PlaceRef{}
		if row.RoomID != nil {
			ref.RoomID = *row.RoomID
		} else if row.ConversationID != nil {
			ref.ConversationID = *row.ConversationID
		}
		out = append(out, prefsOf(row, ref, false))
	}
	return out, nil
}
