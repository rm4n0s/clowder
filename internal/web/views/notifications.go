package views

import (
	"net/url"
	"strconv"

	"github.com/rm4n0s/clowder/internal/app"
)

// MeStreamPath is the path of the stream of the pages that have no stream of
// their own: it keeps the counts and the room list of the sidebar live (SPEC
// D31).
const MeStreamPath = "/me/stream"

// NotificationsPath is the path of the notifications page.
const NotificationsPath = "/notifications"

// NotificationsReadPath is the path that marks all notifications as read.
const NotificationsReadPath = "/notifications/read"

// NotificationsData is what the notifications page shows.
type NotificationsData struct {
	// Sidebar is the sidebar of the page.
	Sidebar SidebarData
	// Items are the notifications of the page, newest first. Their Read is
	// what it was when the page was opened.
	Items []app.Notification
	// Before is the before of the page: 0 for the newest notifications.
	Before uint64
	// HasNext is true when older notifications exist; NextBefore is the
	// before that asks for them.
	HasNext    bool
	NextBefore uint64
	// Message is a problem to show, such as a failed "Mark all as read".
	Message string
}

// NewestID is the ID of the newest notification of the page, 0 for none.
func (d NotificationsData) NewestID() uint64 {
	if len(d.Items) == 0 {
		return 0
	}
	return d.Items[0].ID
}

// NotificationsStreamPath returns the path of the stream of the notifications
// page: MeStreamPath, with the ID of the newest notification the page shows
// when the page shows the newest ones, so that the stream puts newer ones on
// top. A page of older notifications keeps no list live, and takes seen 0 with
// live false.
func NotificationsStreamPath(seen uint64, live bool) string {
	if !live {
		return MeStreamPath
	}
	return MeStreamPath + "?notifications=" + strconv.FormatUint(seen, 10)
}

// NotificationsOlderPath returns the path of the notifications older than the
// notification before.
func NotificationsOlderPath(before uint64) string {
	return NotificationsPath + "?" + url.Values{"before": {strconv.FormatUint(before, 10)}}.Encode()
}

// NotificationPath returns where a notification leads: the post or comment of
// a mention, a reply or an assignment, in its thread; the room of a room
// addition.
func NotificationPath(n app.Notification) string {
	if n.Kind == app.NotificationAddedToRoom || n.PostID == 0 {
		return RoomPath(n.RoomID)
	}
	return CommentPath(n.RootID, n.PostID)
}

// NotificationSentence is what the actor did, up to the room: "mentioned you
// in", "replied to you in", `assigned you "<task>" in` or "added you to".
func NotificationSentence(n app.Notification) string {
	switch n.Kind {
	case app.NotificationMention:
		return "mentioned you in"
	case app.NotificationReply:
		return "replied to you in"
	case app.NotificationAssigned:
		return "assigned you \"" + n.TaskTitle + "\" in"
	case app.NotificationAddedToRoom:
		return "added you to"
	default:
		return "notified you in"
	}
}

// NotificationElementID is the id of the element of a notification.
func NotificationElementID(id uint64) string {
	return "notification-" + strconv.FormatUint(id, 10)
}

// unreadWhile is the Datastar expression that is true while the notification
// is shown as unread: the signal readUpTo, set by "Mark all as read", is below
// its ID.
func unreadWhile(id uint64) string {
	return "$readUpTo < " + strconv.FormatUint(id, 10)
}
