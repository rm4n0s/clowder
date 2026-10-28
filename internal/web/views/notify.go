package views

import (
	"strconv"
	"strings"

	"github.com/rm4n0s/clowder/internal/app"
)

// The paths of the notification preferences (SPEC §7.15).
const (
	// SettingsNotificationsPath saves the global notification preferences.
	SettingsNotificationsPath = SettingsPath + "/notifications"
	// PlacePrefsQuery is the query that opens the "Notification preferences"
	// dialog when a room or a conversation page loads: the links of the
	// settings page use it.
	PlacePrefsQuery = "prefs=1"
)

// PlacePrefsDialogID is the id of the dialog "Notification preferences" of a
// room page and of a conversation page, and PlacePrefsFormID the id of its
// form, which the server patches.
const (
	PlacePrefsDialogID = "place-prefs-dialog"
	PlacePrefsFormID   = "place-prefs-form"
)

// RoomPrefsPath is where the dialog of a room posts to.
func RoomPrefsPath(roomID uint64) string {
	return RoomPath(roomID) + "/notifications"
}

// ConversationPrefsPath is where the dialog of a conversation posts to.
func ConversationPrefsPath(convID uint64) string {
	return ConversationPath(convID) + "/notifications"
}

// PlaceKey returns the key of a place that the page script compares with the
// place of an event: "room:3" or "conversation:5".
func PlaceKey(ref app.PlaceRef) string {
	if ref.IsRoom() {
		return "room:" + strconv.FormatUint(ref.RoomID, 10)
	}
	return "conversation:" + strconv.FormatUint(ref.ConversationID, 10)
}

// NoticePlaceKey is the PlaceKey of the place of a notice.
func NoticePlaceKey(n app.Notice) string {
	if n.RoomID != 0 {
		return PlaceKey(app.RoomRef(n.RoomID))
	}
	return PlaceKey(app.ConversationRef(n.ConversationID))
}

// NoticePath returns where a notice leads when it is clicked: the post or
// comment of a new post, a mention, a reply or an assignment, in its thread;
// the room of an addition; the conversation of a message.
func NoticePath(n app.Notice) string {
	switch {
	case n.ConversationID != 0:
		return ConversationPath(n.ConversationID)
	case n.NotificationKind == app.NotificationAddedToRoom || n.PostID == 0:
		return RoomPath(n.RoomID)
	default:
		return CommentPath(n.RootID, n.PostID)
	}
}

// notifyEventID is the id of the element of a notice on the page.
func notifyEventID(n app.Notice) string {
	return "notify-" + n.ID
}

// NotifyPrefsData is what the Notifications section of the settings shows.
type NotifyPrefsData struct {
	// Settings are the settings the form shows.
	Settings app.Settings
	// Places are the rooms and conversations that have preferences of their
	// own, each with the link to its dialog.
	Places []PlaceListItem
	// Errors are the problems of the last submit.
	Errors FormErrors
	// Saved is true right after a submit that worked.
	Saved bool
	// KeywordsTyped is what the person typed in the keywords field when the
	// submit failed, to put it back; nil when the field shows the saved list.
	KeywordsTyped *string
}

// KeywordsText is the text of the keywords field: what was typed, or the
// saved keywords separated by commas.
func (d NotifyPrefsData) KeywordsText() string {
	if d.KeywordsTyped != nil {
		return *d.KeywordsTyped
	}
	return strings.Join(d.Settings.Keywords, ", ")
}

// PlaceListItem is a room or a conversation that has preferences of its own,
// as the Notifications section lists it.
type PlaceListItem struct {
	// Name is the name of the room, or the title of the conversation.
	Name string
	// Room is true for a room.
	Room bool
	// Path is the page of the place with the dialog open.
	Path string
	// Summary says in a few words what the preferences are.
	Summary string
}

// PlacePrefsData is what the dialog "Notification preferences" shows.
type PlacePrefsData struct {
	// Prefs are the preferences the form shows.
	Prefs app.PlacePrefs
	// Title is the name of the place.
	Title string
	// Action is where the form posts to; empty for a viewer who has no
	// preferences here (the dialog is then not drawn).
	Action string
	// Errors are the problems of the last submit.
	Errors FormErrors
	// Saved is true right after a submit that worked.
	Saved bool
}

// choice is one option of a group of radio buttons.
type choice struct {
	Value string
	Label string
}

// globalNotifyChoices are the choices of "Notify me about" (SPEC §7.15).
func globalNotifyChoices() []choice {
	return []choice{
		{string(app.NotifyAll), "All new activity"},
		{string(app.NotifyMentions), "Mentions, direct messages and group messages"},
		{string(app.NotifyNothing), "Nothing"},
	}
}

// soundChoices are the four sounds in the order they are offered.
func soundChoices() []choice {
	return []choice{
		{string(app.SoundPurr), "Purr"},
		{string(app.SoundChirp), "Chirp"},
		{string(app.SoundBell), "Bell"},
		{string(app.SoundMeow), "Meow"},
	}
}

// repliesChoices are the choices of "Replies".
func repliesChoices() []choice {
	return []choice{
		{string(app.RepliesFollowing), "Threads I follow"},
		{string(app.RepliesDirect), "Only direct replies"},
	}
}

// placeNotifyChoices are the choices of "Notify me about" in a place.
func placeNotifyChoices() []choice {
	return []choice{
		{string(app.PlaceNotifyDefault), "Default (what the global setting says)"},
		{string(app.PlaceNotifyAll), "All new activity"},
		{string(app.PlaceNotifyMentions), "Mentions"},
		{string(app.PlaceNotifyNothing), "Nothing"},
	}
}

// placeSoundChoices are the choices of the sound in a place.
func placeSoundChoices() []choice {
	return []choice{
		{string(app.PlaceSoundDefault), "Default"},
		{string(app.PlaceSoundOn), "On"},
		{string(app.PlaceSoundOff), "Off"},
	}
}

// markUnreadChoices are the choices of "Mark as unread" in a room.
func markUnreadChoices() []choice {
	return []choice{
		{string(app.MarkUnreadAll), "All new posts"},
		{string(app.MarkUnreadMentions), "Only mentions"},
	}
}

// PlacePrefsSummary says in a few words what the preferences of a place are,
// for the list of the settings: "Muted", "Notify: mentions, sound off".
func PlacePrefsSummary(p app.PlacePrefs) string {
	var parts []string
	if p.Muted {
		parts = append(parts, "Muted")
	}
	if p.Notify != app.PlaceNotifyDefault {
		for _, c := range placeNotifyChoices() {
			if c.Value == string(p.Notify) {
				parts = append(parts, "Notify: "+strings.ToLower(c.Label))
			}
		}
	}
	switch p.Sound {
	case app.PlaceSoundOn:
		parts = append(parts, "Sound on")
	case app.PlaceSoundOff:
		parts = append(parts, "Sound off")
	}
	if p.IgnoreGroupMentions {
		parts = append(parts, "Ignores @room and @here")
	}
	if p.MarkUnread == app.MarkUnreadMentions {
		parts = append(parts, "Counts mentions only")
	}
	return strings.Join(parts, ", ")
}
