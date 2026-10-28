package app

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// threadTitleMax is the longest thread name in a notification text, in
// characters (SPEC §7.15).
const threadTitleMax = 40

// Notice is what the page or the client of a user needs to ring for one event
// of the user's stream (SPEC §7.15): who did what and where, never the body.
type Notice struct {
	// Kind is the kind of the live event: live.KindNotificationCreated,
	// live.KindPostCreated or live.KindMessageCreated.
	Kind string
	// ID identifies the event for the election of the tab that rings: "n<id>"
	// for a notification, "p<id>" for a post, "m<id>" for a message.
	ID string
	// Text is the text of the browser notification, for example "Bob
	// mentioned you in #general".
	Text string
	// Author is the user whose action it is.
	Author UserRef
	// PlaceName is the name of the room, or the title of the conversation.
	PlaceName string
	// RoomID is the room, 0 for a conversation.
	RoomID uint64
	// ConversationID is the conversation, 0 for a room.
	ConversationID uint64
	// PostID is the post or comment concerned; 0 when none.
	PostID uint64
	// RootID is the top-level post of the thread of PostID; 0 when none.
	RootID uint64
	// MessageID is the message concerned; 0 when none.
	MessageID uint64
	// NotificationKind is the kind of the notification of a
	// live.KindNotificationCreated event; empty for the others.
	NotificationKind NotificationKind
	// Ring, Sound and Browser are the decision of the server for this user,
	// copied from the event.
	Ring bool
	// Sound is the name of the sound to play; empty for none.
	Sound SoundName
	// Browser is true when the browser notification is wanted.
	Browser bool
}

// OthersText is the text that follows the name of the actor of a reply
// notification that took in others more people (SPEC §7.15): " and 2 others",
// " and 1 other", or nothing for none.
func OthersText(others int) string {
	switch {
	case others <= 0:
		return ""
	case others == 1:
		return " and 1 other"
	default:
		return " and " + strconv.Itoa(others) + " others"
	}
}

// ThreadTitle makes the name of a thread out of the body of its first post: the
// start of the first line that has text, without the marks of markdown that
// open a line (#, >, -, *), up to 40 characters, with an ellipsis when it was
// longer. A body with no text gives the empty string.
func ThreadTitle(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#>-*+ \t"))
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) <= threadTitleMax {
			return line
		}
		return strings.TrimSpace(string([]rune(line)[:threadTitleMax])) + "…"
	}
	return ""
}

// Notice reads what the page of the actor needs to ring for ev, an event of
// the actor's own topic with Ring set: a notification.created, a post.created
// or a message.created. It returns false, and no error, when there is nothing
// to ring for any more: the notification, post or message is gone, or the
// actor can no longer read the room or conversation. The text names the
// place, the author and, for a reply, the thread or, for an assignment, the
// task; it never holds the body of a post or message. A disabled or missing
// account fails with TagUnauthenticated.
func (p *Prefs) Notice(ctx context.Context, actor Actor, ev live.Event) (Notice, bool, error) {
	user, err := p.st.UserByID(ctx, actor.UserID)
	if err != nil {
		if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagNotFound {
			return Notice{}, false, errors.New(TagUnauthenticated, "the user does not exist", "userID", actor.UserID)
		}
		return Notice{}, false, err
	}
	if user.DisabledAt != nil {
		return Notice{}, false, errors.New(TagUnauthenticated, "the user is disabled", "userID", actor.UserID)
	}
	var n Notice
	var ok bool
	switch ev.Kind {
	case live.KindNotificationCreated:
		n, ok, err = p.noticeOfNotification(ctx, actor, ev.NotificationID)
	case live.KindPostCreated:
		n, ok, err = p.noticeOfPost(ctx, actor, ev.PostID)
	case live.KindMessageCreated:
		n, ok, err = p.noticeOfMessage(ctx, actor, ev.MessageID)
	}
	if err != nil || !ok {
		return Notice{}, false, err
	}
	n.Kind = ev.Kind
	n.Ring, n.Sound, n.Browser = ev.Ring, SoundName(ev.Sound), ev.Browser
	return n, true, nil
}

// noticeOfNotification makes the notice of the notification id of the actor.
func (p *Prefs) noticeOfNotification(ctx context.Context, actor Actor, id uint64) (Notice, bool, error) {
	// The newest notification with an ID below id+1 is the notification
	// itself, when it exists and the actor can still read its room.
	rows, err := p.st.ListNotifications(ctx, actor.UserID, id+1, 1)
	if err != nil {
		return Notice{}, false, err
	}
	if len(rows) == 0 || rows[0].ID != id {
		return Notice{}, false, nil
	}
	r := rows[0]
	who := r.ActorDisplayName
	n := Notice{
		ID:        "n" + strconv.FormatUint(r.ID, 10),
		Author:    UserRef{ID: r.ActorID, Username: r.ActorUsername, DisplayName: r.ActorDisplayName, AvatarKey: r.ActorAvatarKey},
		PlaceName: r.RoomName, RoomID: r.RoomID, PostID: r.PostID, RootID: r.RootID,
		NotificationKind: r.Kind,
	}
	room := "#" + r.RoomName
	switch r.Kind {
	case NotificationMention, NotificationGroupMention, NotificationKeyword:
		n.Text = who + " mentioned you in " + room
	case NotificationReply:
		title := ""
		if r.RootID != 0 {
			root, err := p.st.PostByID(ctx, r.RootID)
			if err != nil {
				if appErr, ok := errors.FromError(err); !ok || appErr.Tag != store.TagNotFound {
					return Notice{}, false, err
				}
			} else {
				title = ThreadTitle(root.Body)
			}
		}
		who += OthersText(r.Others)
		if title == "" {
			n.Text = who + " replied in " + room
		} else {
			n.Text = who + " replied in «" + title + "» in " + room
		}
	case NotificationAssigned:
		n.Text = who + " assigned you «" + r.TaskTitle + "» in " + room
	case NotificationAddedToRoom:
		n.Text = who + " added you to " + room
	default:
		return Notice{}, false, nil
	}
	return n, true, nil
}

// noticeOfPost makes the notice of the new post postID of a room the actor is
// a member of.
func (p *Prefs) noticeOfPost(ctx context.Context, actor Actor, postID uint64) (Notice, bool, error) {
	post, err := p.st.PostByID(ctx, postID)
	if err != nil {
		if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagNotFound {
			return Notice{}, false, nil
		}
		return Notice{}, false, err
	}
	if post.DeletedAt != nil {
		return Notice{}, false, nil
	}
	member, err := isMember(ctx, p.st, actor.UserID, post.RoomID)
	if err != nil || !member {
		return Notice{}, false, err
	}
	room, err := p.st.RoomByID(ctx, post.RoomID)
	if err != nil {
		return Notice{}, false, err
	}
	author := post.Author
	if author == nil {
		u, err := p.st.UserByID(ctx, post.AuthorID)
		if err != nil {
			return Notice{}, false, err
		}
		author = &u
	}
	return Notice{
		ID:        "p" + strconv.FormatUint(post.ID, 10),
		Author:    UserRef{ID: author.ID, Username: author.Username, DisplayName: author.DisplayName, AvatarKey: author.AvatarKey},
		Text:      author.DisplayName + " posted in #" + room.Name,
		PlaceName: room.Name, RoomID: room.ID, PostID: post.ID, RootID: post.ID,
	}, true, nil
}

// noticeOfMessage makes the notice of the new message msgID of a conversation
// the actor is a member of.
func (p *Prefs) noticeOfMessage(ctx context.Context, actor Actor, msgID uint64) (Notice, bool, error) {
	msg, err := p.st.MessageByID(ctx, msgID)
	if err != nil {
		if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagNotFound {
			return Notice{}, false, nil
		}
		return Notice{}, false, err
	}
	if msg.DeletedAt != nil {
		return Notice{}, false, nil
	}
	conv, _, err := loadConversation(ctx, p.st, actor, msg.ConversationID)
	if err != nil {
		if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagNotFound {
			return Notice{}, false, nil
		}
		return Notice{}, false, err
	}
	members, err := p.st.ConversationMembers(ctx, []uint64{conv.ID}, false)
	if err != nil {
		return Notice{}, false, err
	}
	var author UserRef
	for _, m := range members[conv.ID] {
		if m.UserID == msg.AuthorID {
			author = UserRef{ID: m.UserID, Username: m.Username, DisplayName: m.DisplayName, AvatarKey: m.AvatarKey}
		}
	}
	if author.ID == 0 {
		u, err := p.st.UserByID(ctx, msg.AuthorID)
		if err != nil {
			return Notice{}, false, err
		}
		author = UserRef{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, AvatarKey: u.AvatarKey}
	}
	title := conversationTitle(conv, actor.UserID, members[conv.ID])
	n := Notice{
		ID:     "m" + strconv.FormatUint(msg.ID, 10),
		Author: author, PlaceName: title, ConversationID: conv.ID, MessageID: msg.ID,
	}
	if conv.Kind == ConversationDirect {
		n.Text = author.DisplayName + " sent you a message"
	} else {
		n.Text = author.DisplayName + " wrote in " + title
	}
	return n, true, nil
}
