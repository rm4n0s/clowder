package app

import (
	"context"
	"time"

	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// NotificationKind says why a user is notified (SPEC §7.9). It is the
// store's type, so the web layer can name the kinds without importing the
// store.
type NotificationKind = store.NotificationKind

// The kinds of notification.
const (
	// NotificationMention is an "@username" in a post or comment.
	NotificationMention NotificationKind = store.NotificationMention
	// NotificationReply is a comment made directly under the user's post or
	// comment.
	NotificationReply NotificationKind = store.NotificationReply
	// NotificationAssigned is a task newly assigned to the user.
	NotificationAssigned NotificationKind = store.NotificationAssigned
	// NotificationAddedToRoom is the user's addition to a room by someone
	// else.
	NotificationAddedToRoom NotificationKind = store.NotificationAddedToRoom
	// NotificationGroupMention is an @room, @all or @here that reached the
	// user (SPEC §7.15). It is shown as a mention.
	NotificationGroupMention NotificationKind = store.NotificationGroupMention
	// NotificationKeyword is a keyword of the user found in a post or comment
	// (SPEC §7.15). It is shown as a mention.
	NotificationKeyword NotificationKind = store.NotificationKeyword
)

// The page size of the notifications (SPEC D30).
const (
	// notificationsDefaultLimit is the page size of List when the caller gives
	// none.
	notificationsDefaultLimit = 30
	// notificationsMaxLimit is the largest page List gives.
	notificationsMaxLimit = 100
)

// Notification is a notification of the asking user, with what a page needs
// to show it and to link to what it is about.
type Notification struct {
	// ID is the ID of the notification; a larger ID is a newer one.
	ID uint64
	// Kind says why the user is notified.
	Kind NotificationKind
	// Actor is the user whose action caused it.
	Actor UserRef
	// RoomID and RoomName are the room of the notification.
	RoomID   uint64
	RoomName string
	// PostID is the post or comment of a mention, a reply or an assignment; 0
	// for added_to_room. RootID is the top-level post of its thread (PostID
	// itself for a top-level post), so that a link is /posts/<RootID>#post-<PostID>.
	PostID uint64
	RootID uint64
	// TaskID and TaskTitle are the task of an assignment; 0 and empty for the
	// other kinds.
	TaskID    uint64
	TaskTitle string
	// Others is the number of other people who replied within the minute that
	// the notification took them in: "Bob and 2 others replied"; 0 for a
	// single actor and for the kinds other than reply.
	Others int
	// Read is true when the user has read the notification; ReadAt is when.
	Read   bool
	ReadAt *time.Time
	// CreatedAt is when the notification was made.
	CreatedAt time.Time
}

// NotificationPage is a page of the notifications of a user.
type NotificationPage struct {
	// Notifications are the notifications of the page, newest first.
	Notifications []Notification
	// HasNext is true when older notifications exist.
	HasNext bool
	// NextBefore is the before that asks for the next page: the ID of the last
	// notification of this page. It is 0 when HasNext is false.
	NextBefore uint64
}

// Notifications is the service of the notifications of a user (SPEC §7.9). A
// user sees only their own. Make it with NewNotifications. It is safe for
// concurrent use. The notifications themselves are made by the services of
// the actions that cause them, in the transaction of the action.
type Notifications struct {
	st  *store.Store
	pub Publisher
}

// NewNotifications makes the service. pub receives the live events of the
// changes, after they are committed; nil publishes nothing.
func NewNotifications(st *store.Store, pub Publisher) *Notifications {
	return &Notifications{st: st, pub: orNop(pub)}
}

// List returns a page of the actor's notifications, newest first. before 0
// starts from the newest; another value returns the notifications older than
// that ID. limit is the page size: 30 when it is 0 or less, at most 100.
// Notifications of a room that the actor can no longer read are left out (SPEC
// D29, D30). An actor with no ID fails with TagUnauthenticated.
func (n *Notifications) List(ctx context.Context, actor Actor, before uint64, limit int) (NotificationPage, error) {
	if actor.UserID == 0 {
		return NotificationPage{}, errors.New(TagUnauthenticated, "notifications need a login", "reason", "none")
	}
	if limit <= 0 {
		limit = notificationsDefaultLimit
	}
	limit = min(limit, notificationsMaxLimit)
	// One more than a page tells whether an older page exists.
	rows, err := n.st.ListNotifications(ctx, actor.UserID, before, limit+1)
	if err != nil {
		return NotificationPage{}, err
	}
	hasNext := len(rows) > limit
	if hasNext {
		rows = rows[:limit]
	}
	page := NotificationPage{Notifications: make([]Notification, len(rows)), HasNext: hasNext}
	for i, r := range rows {
		page.Notifications[i] = Notification{
			ID:        r.ID,
			Kind:      r.Kind,
			Actor:     UserRef{ID: r.ActorID, Username: r.ActorUsername, DisplayName: r.ActorDisplayName, AvatarKey: r.ActorAvatarKey},
			RoomID:    r.RoomID,
			RoomName:  r.RoomName,
			PostID:    r.PostID,
			RootID:    r.RootID,
			TaskID:    r.TaskID,
			TaskTitle: r.TaskTitle,
			Others:    r.Others,
			Read:      r.ReadAt != nil,
			ReadAt:    r.ReadAt,
			CreatedAt: r.CreatedAt,
		}
	}
	if hasNext {
		page.NextBefore = rows[len(rows)-1].ID
	}
	return page, nil
}

// UnreadCount returns the number of unread notifications of the actor, not
// counting those of a room that the actor can no longer read. An actor with no
// ID fails with TagUnauthenticated.
func (n *Notifications) UnreadCount(ctx context.Context, actor Actor) (int, error) {
	if actor.UserID == 0 {
		return 0, errors.New(TagUnauthenticated, "notifications need a login", "reason", "none")
	}
	return n.st.CountUnreadNotifications(ctx, actor.UserID)
}

// MarkRead marks as read the actor's unread notifications with an ID up to
// upToID; newer ones stay unread. It publishes notification.read to the actor
// when it changed something. An actor with no ID fails with TagUnauthenticated.
func (n *Notifications) MarkRead(ctx context.Context, actor Actor, upToID uint64) error {
	if actor.UserID == 0 {
		return errors.New(TagUnauthenticated, "notifications need a login", "reason", "none")
	}
	var events []live.Event
	err := n.st.Tx(ctx, func(tx *store.Store) error {
		events = nil
		changed, err := tx.MarkNotificationsRead(ctx, actor.UserID, upToID)
		if err != nil {
			return err
		}
		if changed > 0 {
			events = []live.Event{{Topic: live.UserTopic(actor.UserID), Kind: live.KindNotificationRead, UserID: actor.UserID}}
		}
		return nil
	})
	if err != nil {
		return err
	}
	publishAll(n.pub, events)
	return nil
}

// notice is a reason to notify a user of one action.
type notice struct {
	userID uint64
	kind   NotificationKind
	// taskID is the task of an assignment; 0 for the other kinds.
	taskID uint64
	// foldRoot is the top-level post of a reply that comes from following the
	// thread: such a reply is folded into the user's unread reply
	// notification of the same thread that is less than a minute old (SPEC
	// §7.15). 0 for every other notice.
	foldRoot uint64
}

// rank orders the notices of one action: when several apply to one user, the
// highest wins. SPEC D30: assigned and a direct reply win over a mention; SPEC
// §7.15: a mention wins over a reply that only comes from following the thread,
// which wins over a group mention, which wins over a keyword.
func rank(n notice) int {
	switch n.kind {
	case NotificationAssigned:
		return 6
	case NotificationReply:
		if n.foldRoot != 0 {
			return 3
		}
		return 5
	case NotificationMention:
		return 4
	case NotificationGroupMention:
		return 2
	case NotificationKeyword:
		return 1
	default:
		return 0
	}
}

// notify writes the notifications of one action of the actor in the room,
// inside the transaction of the action, and returns the live events to
// publish after it commits. postID is the post or comment of the action; 0
// when none (added_to_room). A user gets at most one notification of an
// action, the one of the highest rank; a notice for the actor is dropped (nobody
// is notified of their own action), and so is one for a user who is disabled
// or cannot read the room (SPEC D30). A reply that comes from following the
// thread is folded into the user's unread reply notification of the thread
// that is less than a minute old, which then names the actor and counts the
// others, and publishes live.KindNotificationChanged instead of a new
// notification (SPEC §7.15). It does nothing for no notices.
func notify(ctx context.Context, tx *store.Store, actor Actor, room store.Room, postID uint64, notices []notice) ([]live.Event, error) {
	best := map[uint64]notice{}
	follows := map[uint64]uint64{}
	var order []uint64
	for _, n := range notices {
		if n.userID == actor.UserID {
			continue
		}
		if n.foldRoot != 0 {
			follows[n.userID] = n.foldRoot
		}
		cur, ok := best[n.userID]
		switch {
		case !ok:
			order = append(order, n.userID)
			best[n.userID] = n
		case rank(n) > rank(cur):
			best[n.userID] = n
		}
	}
	// A user who follows the thread has a reply folded into the notification
	// of the burst, whether the reply is direct or comes from the follow.
	for id, root := range follows {
		if n := best[id]; n.kind == NotificationReply {
			n.foldRoot = root
			best[id] = n
		}
	}
	if len(order) == 0 {
		return nil, nil
	}
	readers, err := tx.EnabledReaders(ctx, room.ID, room.Visibility == VisibilityPrivate, order)
	if err != nil {
		return nil, err
	}
	var present []uint64
	for _, id := range order {
		if readers[id] {
			present = append(present, id)
		}
	}
	// One query reads the settings and the room preferences of all of them,
	// for the decision whether their notification rings (SPEC §7.15).
	recipients, err := tx.RecipientsOf(ctx, store.RoomPlace(room.ID), present)
	if err != nil {
		return nil, err
	}
	byUser := make(map[uint64]store.Recipient, len(recipients))
	for _, r := range recipients {
		byUser[r.UserID] = r
	}
	var rows []store.Notification
	var kept []uint64
	var events []live.Event
	for _, id := range present {
		n := best[id]
		if n.foldRoot != 0 && n.kind == NotificationReply {
			folded, err := foldReply(ctx, tx, id, actor.UserID, n.foldRoot)
			if err != nil {
				return nil, err
			}
			if folded.ID != 0 {
				events = append(events, live.Event{
					Topic: live.UserTopic(id), Kind: live.KindNotificationChanged,
					RoomID: room.ID, PostID: postID, UserID: id, NotificationID: folded.ID,
				})
				continue
			}
		}
		row := store.Notification{UserID: id, Kind: n.kind, RoomID: &room.ID, ActorID: actor.UserID}
		if postID != 0 {
			row.PostID = &postID
		}
		if n.taskID != 0 {
			row.TaskID = &n.taskID
		}
		rows = append(rows, row)
		kept = append(kept, id)
	}
	if err := tx.CreateNotifications(ctx, rows); err != nil {
		return nil, err
	}
	for i, id := range kept {
		ev := live.Event{
			Topic: live.UserTopic(id), Kind: live.KindNotificationCreated,
			RoomID: room.ID, PostID: postID, UserID: id, NotificationID: rows[i].ID,
		}
		if r, ok := byUser[id]; ok {
			// Every notification is a mention-like event for the decision;
			// the bell has it whatever the answer.
			ringEvent(&ev, ringFor(r.Settings, r.Place, PlaceRoom, EventNotification, true))
		}
		events = append(events, ev)
	}
	return events, nil
}

// foldReply folds a new reply of actorID in the thread rootID into the unread
// reply notification of userID for the same thread that is less than a minute
// old, if there is one: the notification names the actor and counts the other
// distinct people who replied since it began. It returns the notification, or
// the zero value when there is none to fold into.
func foldReply(ctx context.Context, tx *store.Store, userID, actorID, rootID uint64) (store.Notification, error) {
	current, found, err := tx.FoldableReply(ctx, userID, rootID)
	if err != nil || !found {
		return store.Notification{}, err
	}
	var from uint64
	if current.PostID != nil {
		from = *current.PostID
	}
	others, err := tx.DistinctRepliers(ctx, rootID, from, userID, actorID)
	if err != nil {
		return store.Notification{}, err
	}
	if err := tx.FoldReply(ctx, current.ID, actorID, others); err != nil {
		return store.Notification{}, err
	}
	return current, nil
}

// assignment is a user newly assigned to a task.
type assignment struct {
	userID, taskID uint64
}

// assignedNotices makes the notices of the assignments, in order.
func assignedNotices(assigned []assignment) []notice {
	out := make([]notice, len(assigned))
	for i, a := range assigned {
		out[i] = notice{userID: a.userID, kind: NotificationAssigned, taskID: a.taskID}
	}
	return out
}
