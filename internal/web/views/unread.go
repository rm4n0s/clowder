package views

import (
	"strconv"
	"strings"

	"github.com/rm4n0s/clowder/internal/app"
)

// The two favicons: the plain one, and the one with a dot, which says that
// something is waiting (SPEC §7.17). The stream swaps one for the other by
// patching the link element of the page; no script draws anything.
const (
	// FaviconPlainPath is the favicon of a page with nothing waiting.
	FaviconPlainPath = "/static/favicon.svg"
	// FaviconDotPath is the favicon of a page with something waiting.
	FaviconDotPath = "/static/favicon-dot.svg"
	// titleSuffix follows the name of the page in its title.
	titleSuffix = " · Clowder"
)

// UnreadTotal is the number in the title of the tab: the unread notifications
// plus the conversations with unread messages that are not muted, the same
// numbers as the bell and the Messages link (SPEC §7.17).
func (d SidebarData) UnreadTotal() int {
	return d.Unread + d.UnreadMessages
}

// WithRoomRead returns the data with the unread counts of the room roomID set
// to 0: the page of the room's feed (or its board) has just marked the room as
// read, and the room being looked at shows nothing waiting. The rooms are
// copied; the receiver is not changed.
func (d SidebarData) WithRoomRead(roomID uint64) SidebarData {
	rooms := make([]app.Room, len(d.Rooms))
	copy(rooms, d.Rooms)
	for i := range rooms {
		if rooms[i].ID == roomID {
			rooms[i].UnreadPosts, rooms[i].UnreadMentions = 0, 0
		}
	}
	d.Rooms = rooms
	return d
}

// WithRoomRead returns the room with its unread counts set to 0; see
// SidebarData.WithRoomRead.
func WithRoomRead(room app.Room) app.Room {
	room.UnreadPosts, room.UnreadMentions = 0, 0
	return room
}

// unreadLabel is the text of a count: the number, or "99+" above 99.
func unreadLabel(n int) string {
	if n > 99 {
		return "99+"
	}
	return strconv.Itoa(n)
}

// roomUnreadPosts is the number of unread posts the sidebar shows next to a
// room: the count of the room, except that a muted room and a room that counts
// only mentions show none (SPEC §7.15, §7.17).
func roomUnreadPosts(room app.Room) int {
	if room.Muted || room.MentionsOnly {
		return 0
	}
	return room.UnreadPosts
}

// sidebarRoomID is the id of the list item of a room in the sidebar, which the
// stream patches when the counts of the room change.
func sidebarRoomID(roomID uint64) string {
	return "sidebar-room-" + strconv.FormatUint(roomID, 10)
}

// sidebarRoomClasses are the classes of the list item of a room: muted rooms
// are dimmed, and a room with unread posts is bold.
func sidebarRoomClasses(room app.Room) string {
	var classes []string
	if room.Muted {
		classes = append(classes, "muted")
	}
	if roomUnreadPosts(room) > 0 {
		classes = append(classes, "unread")
	}
	return strings.Join(classes, " ")
}

// unreadPostsTitle is "1 unread post", "5 unread posts" or, at the cap of the
// count (app.UnreadPostsCap, which stops counting), "More than 99 unread
// posts".
func unreadPostsTitle(n int) string {
	switch {
	case n >= app.UnreadPostsCap:
		return "More than 99 unread posts"
	case n == 1:
		return "1 unread post"
	}
	return strconv.Itoa(n) + " unread posts"
}

// unreadMentionsTitle is "1 unread mention" or "2 unread mentions".
func unreadMentionsTitle(n int) string {
	if n == 1 {
		return "1 unread mention"
	}
	return strconv.Itoa(n) + " unread mentions"
}

// TabTitle is the title of a page: "(n) " when n is above 0, then base. base is
// the name of the page with the suffix of the site, as PageTitle takes it.
func TabTitle(unread int, base string) string {
	if unread <= 0 {
		return base
	}
	return "(" + unreadLabel(unread) + ") " + base
}

// SplitTitle splits the title of a page into the label of its count of unread
// things ("3" or "99+"; empty when the title has none) and the rest, which is
// the name of the page with the name of the site. It is how a stream learns
// what the page shows from the title the page tells it.
func SplitTitle(title string) (label, base string) {
	if !strings.HasPrefix(title, "(") {
		return "", title
	}
	end := strings.Index(title, ") ")
	if end < 0 {
		return "", title
	}
	n := title[1:end]
	if n != "99+" {
		if v, err := strconv.Atoi(n); err != nil || v < 1 || v > 99 {
			return "", title
		}
	}
	return n, title[end+2:]
}

// CountLabel is the label of a count of unread things as the title shows it:
// empty for none, the number, or "99+".
func CountLabel(n int) string {
	if n <= 0 {
		return ""
	}
	return unreadLabel(n)
}

// PageBaseTitle is the title of a page without the count: the name of the page
// and the name of the site.
func PageBaseTitle(title string) string {
	return title + titleSuffix
}

// FaviconPath is the favicon for a page that has unread things waiting.
func FaviconPath(unread int) string {
	if unread > 0 {
		return FaviconDotPath
	}
	return FaviconPlainPath
}

// NewSinceID is the ID of the post after which the feed draws the line "New
// since your last visit" (SPEC §7.17): the oldest top-level post of the page
// that the viewer has not seen. posts are the posts of the first page, newest
// first; seen is the viewer's mark before it moved; a post is unseen when its
// ID is above the mark, it is not deleted and someone else wrote it, which is
// what the sidebar counts. It is 0 when no post of the page is unseen. When
// every post of the page is unseen it is the last post of the page.
func NewSinceID(posts []app.Post, seen, viewerID uint64) uint64 {
	var line uint64
	for _, p := range posts {
		if p.ID > seen && !p.Deleted && p.Author.ID != viewerID {
			line = p.ID
		}
	}
	return line
}
