package web

import (
	"context"
	"net/http"
	"strconv"

	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/starfederation/datastar-go/datastar"
)

// notificationListSelector is the list of the notifications page, and
// notificationsEmptySelector its note that there are none.
const (
	notificationListSelector   = "#notification-list"
	notificationsEmptySelector = "#notifications-empty"
)

// meStream is the stream of the pages that have no stream of their own (SPEC
// D31): it follows only the topic of the user, and keeps the counts and the
// room list of the sidebar right. The notifications page also keeps its list.
type meStream struct {
	*liveStream
	// list is true when the page is the notifications page showing the newest
	// notifications: the stream puts the ones that come on top of its list.
	list bool
	// seen is the ID of the newest notification the page shows, 0 for none.
	seen uint64
	// conversations is true when the page is the Messages page: the stream
	// keeps its list of conversations up to date.
	conversations bool
}

// meStream answers GET /me/stream, optionally with ?notifications=<id> or
// ?messages=1: a stream of Datastar patches that keeps the sidebar of a page up
// to date for the person who opened it. It needs a login. It sends the unread count at
// once, which brings a page up to date after missed events; a reconnection
// (the header Last-Event-ID) sends the whole sidebar. A notification or the
// reading of notifications patches the count, and a change of the person's
// rooms patches the room list. The notifications page gives notifications,
// the ID of the newest notification it shows: the stream then also puts the
// notifications newer than that, as they come, at the top of its list. The
// stream ends when the request ends, when the session ends, or when the hub
// closes it. The Messages page gives messages=1: the stream then also sends the
// list of conversations at once and again whenever a conversation changes or is
// read.
func (s *Server) meStream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor, _ := actorFrom(ctx)
	user, _ := userFrom(ctx)
	// Subscribe before anything is read, so that no change falls between the
	// read and the subscription.
	sub := s.svc.Hub.Subscribe(ctx, live.UserTopic(actor.UserID))
	defer sub.Close()
	ms := &meStream{liveStream: s.newStream(w, r, actor, user)}
	ms.conversations = r.URL.Query().Get("messages") == "1"
	if v, ok := r.URL.Query()["notifications"]; ok && len(v) > 0 {
		ms.list = true
		ms.seen, _ = strconv.ParseUint(v[0], 10, 63)
	}
	st, err := ms.connect(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := ms.open(); err != nil {
		s.streamFailed(r, err)
		return
	}
	if end, err := ms.send(st); err != nil || end {
		if err != nil {
			s.streamFailed(r, err)
		}
		return
	}
	ms.run(sub, ms.event)
}

// connect reads what the stream starts with: the counts, or the whole sidebar
// after a reconnection, and the notifications that came since the page was
// made.
func (ms *meStream) connect(ctx context.Context) (start, error) {
	var ops []patchOp
	if ms.reconnect {
		sidebar, err := ms.sidebarOps(ctx, 0)
		if err != nil {
			return start{}, err
		}
		ops = sidebar
	} else {
		counts, err := ms.countOps(ctx)
		if err != nil {
			return start{}, err
		}
		ops = counts
	}
	if ms.conversations {
		op, err := ms.conversationsOp(ctx)
		if err != nil {
			return start{}, err
		}
		ops = append(ops, op)
	}
	if ms.list {
		fresh, err := ms.freshOps(ctx)
		if err != nil {
			return start{}, err
		}
		ops = append(ops, fresh...)
	}
	return start{ops: ops}, nil
}

// conversationsOp is the patch of the list of conversations of the person.
func (ms *meStream) conversationsOp(ctx context.Context) (patchOp, error) {
	items, err := ms.s.svc.Messages.Conversations(ctx, ms.actor)
	if err != nil {
		return patchOp{}, err
	}
	return patchOp{component: views.ConversationList(items, ms.actor.UserID)}, nil
}

// freshOps reads the notifications newer than the newest the page shows and
// returns the patches that put them on top of the list, oldest first, so that
// the newest ends on top. Each is first removed, so that a reconnection never
// shows one twice. It remembers the newest.
func (ms *meStream) freshOps(ctx context.Context) ([]patchOp, error) {
	page, err := ms.s.svc.Notifications.List(ctx, ms.actor, 0, 0)
	if err != nil {
		return nil, err
	}
	var ops []patchOp
	for i := len(page.Notifications) - 1; i >= 0; i-- {
		n := page.Notifications[i]
		if n.ID <= ms.seen {
			continue
		}
		if len(ops) == 0 && ms.seen == 0 {
			ops = append(ops, patchOp{remove: notificationsEmptySelector})
		}
		ops = append(ops,
			patchOp{remove: "#" + views.NotificationElementID(n.ID)},
			patchOp{
				component: views.NotificationItem(n),
				options:   []datastar.PatchElementOption{datastar.WithSelector(notificationListSelector), datastar.WithModePrepend()},
			},
		)
		ms.seen = n.ID
	}
	return ops, nil
}

// event renders one event of the hub for the user of the stream.
func (ms *meStream) event(ctx context.Context, ev live.Event) (end bool, err error) {
	if ev.Kind == live.KindNotificationChanged {
		// A reply was folded into a notification: the count of the bell does
		// not change, and nothing rings (SPEC §7.15). The notifications page
		// shows the notification again, in its place.
		if ms.list {
			return false, ms.refreshNotification(ctx, ev.NotificationID)
		}
		return false, nil
	}
	if handled, err := ms.countsEvent(ctx, ev); handled {
		if err != nil {
			return false, err
		}
		switch {
		case ms.list && ev.Kind == live.KindNotificationCreated:
			return false, ms.fresh(ctx)
		case ms.conversations && (ev.Kind == live.KindConversationChanged || ev.Kind == live.KindConversationRead):
			return false, ms.conversationList(ctx)
		}
		return false, nil
	}
	// A membership or a muted place changed: the room list of the sidebar, and
	// the list of conversations of the Messages page.
	if err := ms.sidebar(ctx, 0); err != nil {
		return false, err
	}
	if ms.conversations && ev.Kind == live.KindPlacePrefsChanged {
		return false, ms.conversationList(ctx)
	}
	return false, nil
}

// conversationList puts the list of conversations of the person on the page.
func (ms *meStream) conversationList(ctx context.Context) error {
	op, err := ms.conversationsOp(ctx)
	if err != nil {
		return err
	}
	return ms.patch(op.component, op.options...)
}

// fresh puts the notifications that came on top of the list.
func (ms *meStream) fresh(ctx context.Context) error {
	ops, err := ms.freshOps(ctx)
	if err != nil {
		return err
	}
	_, err = ms.send(start{ops: ops})
	return err
}

// refreshNotification puts the notification id of the person on the list again,
// where it is: it names a newer actor and counts more people now. A
// notification that is gone, or of a room the person can no longer read,
// changes nothing.
func (ms *meStream) refreshNotification(ctx context.Context, id uint64) error {
	// The newest notification with an ID below id+1 is the notification itself.
	page, err := ms.s.svc.Notifications.List(ctx, ms.actor, id+1, 1)
	if err != nil {
		return err
	}
	if len(page.Notifications) == 0 || page.Notifications[0].ID != id {
		return nil
	}
	return ms.patch(views.NotificationItem(page.Notifications[0]))
}
