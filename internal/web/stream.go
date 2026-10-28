package web

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/a-h/templ"
	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/web/views"
	"github.com/rm4n0s/errors"
	"github.com/starfederation/datastar-go/datastar"
)

const (
	// pingInterval is how often an open stream writes an SSE comment, so that
	// a proxy in front of the server keeps the connection (SPEC §8).
	pingInterval = 25 * time.Second
	// streamEventID is the id of the first event of every stream. Datastar
	// remembers the id of the last event it received and sends it in the
	// header Last-Event-ID when it opens the stream again, which is how the
	// server knows that a request is a reconnection and not the first
	// connection of a page.
	streamEventID = "1"
	// lastEventIDHeader is the header that carries the id of the last event.
	lastEventIDHeader = "Last-Event-ID"
	// sidebarSelector is the element that holds the sidebar of a page.
	sidebarSelector = "#sidebar"
)

// liveStream is one open Datastar SSE stream of a page. Its methods write to
// the client and are only called by the goroutine of the handler, so the
// writes are never interleaved.
type liveStream struct {
	s     *Server
	w     http.ResponseWriter
	r     *http.Request
	sse   *datastar.ServerSentEventGenerator
	actor app.Actor
	user  app.User
	// reconnect is true when the client opened the stream again after it ended
	// or failed, so that the page may be stale in any part.
	reconnect bool
}

// newStream makes the stream of a request. Nothing is written yet: the handler
// reads what the page needs first (a failed read is still an ordinary error
// answer), then opens the stream.
func (s *Server) newStream(w http.ResponseWriter, r *http.Request, actor app.Actor, user app.User) *liveStream {
	return &liveStream{
		s: s, w: w, r: r, actor: actor, user: user,
		reconnect: r.Header.Get(lastEventIDHeader) != "",
	}
}

// open starts the SSE answer and writes the id of its first event. From then
// on the client has the headers, which tells it, and the tests, that the
// handler has subscribed and has read what it sends first.
func (ls *liveStream) open() error {
	ls.arm()
	ls.sse = datastar.NewSSE(ls.w, ls.r)
	if _, err := io.WriteString(ls.w, "id: "+streamEventID+"\n\n"); err != nil {
		return errors.NewErr(tagSSEWriteFailed, err).SetMetadata("event", "id")
	}
	if err := http.NewResponseController(ls.w).Flush(); err != nil {
		return errors.NewErr(tagSSEWriteFailed, err).SetMetadata("event", "id")
	}
	return nil
}

// start is what a stream sends when it is opened: the patches that bring the
// page up to date, or the page to send the browser to when the user may not
// read what it shows.
type start struct {
	ops      []patchOp
	redirect string
}

// send writes what the stream starts with. It returns true when that was a
// redirect, which ends the stream.
func (ls *liveStream) send(st start) (end bool, err error) {
	ls.arm()
	for _, op := range st.ops {
		var err error
		if op.remove != "" {
			err = ls.remove(op.remove)
		} else {
			err = ls.patch(op.component, op.options...)
		}
		if err != nil {
			return true, err
		}
	}
	if st.redirect != "" {
		return true, ls.redirect(st.redirect)
	}
	return false, nil
}

// patch sends one element patch.
func (ls *liveStream) patch(c templ.Component, opts ...datastar.PatchElementOption) error {
	if err := ls.sse.PatchElementTempl(c, opts...); err != nil {
		return errors.NewErr(tagSSEWriteFailed, err).SetMetadata("event", "patch-elements")
	}
	return nil
}

// remove sends the removal of the elements the selector finds. The client
// logs a warning, and does nothing else, when the page has none.
func (ls *liveStream) remove(selector string) error {
	if err := ls.sse.RemoveElement(selector); err != nil {
		return errors.NewErr(tagSSEWriteFailed, err).SetMetadata("event", "patch-elements")
	}
	return nil
}

// redirect sends the browser to target.
func (ls *liveStream) redirect(target string) error {
	if err := sseRedirect(ls.sse, target); err != nil {
		return errors.NewErr(tagSSEWriteFailed, err).SetMetadata("event", "redirect")
	}
	return nil
}

// ping writes an SSE comment, which the client ignores.
func (ls *liveStream) ping() error {
	if _, err := io.WriteString(ls.w, ": ping\n\n"); err != nil {
		return errors.NewErr(tagSSEWriteFailed, err).SetMetadata("event", "ping")
	}
	if err := http.NewResponseController(ls.w).Flush(); err != nil {
		return errors.NewErr(tagSSEWriteFailed, err).SetMetadata("event", "ping")
	}
	return nil
}

// sidebarOp is the patch of the sidebar of the user, as it is on the page of
// the room current (0 for no room).
func (ls *liveStream) sidebarOp(ctx context.Context, current uint64) (patchOp, error) {
	ls.refreshUser(ctx)
	sb, err := ls.s.sidebarData(ctx, ls.actor, ls.user, current)
	if err != nil {
		return patchOp{}, err
	}
	return patchOp{
		component: views.Sidebar(sb),
		options:   []datastar.PatchElementOption{datastar.WithSelector(sidebarSelector), datastar.WithModeInner()},
	}, nil
}

// refreshUser reads the user of the session again, so that the sidebar shows
// the display name and the avatar as they are now (the user changed them in
// the settings, SPEC §7.14). When the session cannot be read the old user stays:
// the keep-alive of the stream ends it if it is gone.
func (ls *liveStream) refreshUser(ctx context.Context) {
	cookie, err := ls.r.Cookie(sessionCookieName)
	if err != nil {
		return
	}
	sess, err := ls.s.svc.Accounts.SessionActor(ctx, cookie.Value)
	if err != nil {
		ls.s.logger.Debug("reading the user for the sidebar failed", "pattern", ls.r.Pattern, "err", err)
		return
	}
	ls.user = sess.User
}

// sidebar sends the sidebar of the user.
func (ls *liveStream) sidebar(ctx context.Context, current uint64) error {
	op, err := ls.sidebarOp(ctx, current)
	if err != nil {
		return err
	}
	return ls.patch(op.component, op.options...)
}

// countOps are the patches of the counts that the sidebar shows next to its
// links, as they are now for the user: the unread notifications and the
// conversations with unread messages. A count that a later step adds is added
// here, so that every stream and answer that keeps the counts live gets it.
func (s *Server) countOps(ctx context.Context, actor app.Actor) ([]patchOp, error) {
	unread, err := s.svc.Notifications.UnreadCount(ctx, actor)
	if err != nil {
		return nil, err
	}
	unreadMessages, err := s.svc.Messages.UnreadCount(ctx, actor)
	if err != nil {
		return nil, err
	}
	return []patchOp{
		{component: views.NotificationCount(unread)},
		{component: views.MessageCount(unreadMessages)},
	}, nil
}

// counts sends the counts of the sidebar of the user.
func (ls *liveStream) counts(ctx context.Context) error {
	ops, err := ls.s.countOps(ctx, ls.actor)
	if err != nil {
		return err
	}
	for _, op := range ops {
		if err := ls.patch(op.component, op.options...); err != nil {
			return err
		}
	}
	return nil
}

// countsEvent handles an event of the user's topic that only changes a count
// of the sidebar (a notification was made or read, a conversation changed or
// was read). It returns true when ev was such an event, so that the stream has
// nothing more to do with it.
func (ls *liveStream) countsEvent(ctx context.Context, ev live.Event) (handled bool, err error) {
	switch ev.Kind {
	case live.KindNotificationCreated:
		if err := ls.counts(ctx); err != nil {
			return true, err
		}
		return true, ls.ring(ctx, ev)
	case live.KindPostCreated, live.KindMessageCreated:
		// Only a user's own topic carries these two, and only when the event
		// rings for that user (SPEC §7.15).
		return true, ls.ring(ctx, ev)
	case live.KindNotificationRead, live.KindConversationChanged, live.KindConversationRead:
		return true, ls.counts(ctx)
	}
	return false, nil
}

// notifyEventsSelector is the element of every page of a logged-in user that
// the script notify.js watches: what the stream appends to it rings.
const notifyEventsSelector = "#notify-events"

// ring tells the page script to ring for an event of the user's topic that
// rings for the user: it appends the element of the notice to the page (SPEC
// §7.15). The server decided already that the event rings, from the
// preferences of the user; the page only obeys. An event that does not ring,
// and one whose subject is gone or unreadable now, add nothing. The element
// holds who did what and where, never the body.
func (ls *liveStream) ring(ctx context.Context, ev live.Event) error {
	if !ev.Ring {
		return nil
	}
	n, ok, err := ls.s.svc.Prefs.Notice(ctx, ls.actor, ev)
	if err != nil || !ok {
		return err
	}
	return ls.patch(views.NotifyEvent(n), datastar.WithSelector(notifyEventsSelector), datastar.WithModeAppend())
}

// gone handles the error of a read that the stream makes for its user. A
// NotFound means the user may no longer read what the page shows (the room was
// deleted, or the user was removed from a private room): the page is sent to
// target and the stream ends. Any other error is returned.
func (ls *liveStream) gone(err error, target string) (end bool, retErr error) {
	if isTag(err, app.TagNotFound) {
		return true, ls.redirect(target)
	}
	return false, err
}

// eventHandler handles one event of the hub. It returns true when the stream is to
// end.
type eventHandler func(ctx context.Context, ev live.Event) (end bool, err error)

// run delivers the events of the subscription to handle until the stream ends:
// the request is done, the hub or the subscription is closed (the server stops,
// or the client was too slow and the page opens the stream again), handle says
// so, or a write fails. Every ping interval it writes a comment and checks that
// the session of the user is still valid. It returns when the stream is over.
func (ls *liveStream) run(sub *live.Subscription, handle eventHandler) {
	ctx := ls.r.Context()
	ticker := time.NewTicker(ls.s.pingInterval)
	defer ticker.Stop()
	for {
		var end bool
		var err error
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.C():
			if !ok {
				if sub.Dropped() {
					ls.s.logger.Info("stream dropped: the client was too slow", "pattern", ls.r.Pattern, "user_id", ls.actor.UserID)
				}
				return
			}
			ls.arm()
			if ev.Kind == live.KindUserChanged {
				// The account changed: it was disabled, got another role, or
				// a session ended. No page is rendered for the old actor.
				end, err = ls.checkSession(ctx)
				break
			}
			end, err = handle(ctx, ev)
		case <-ticker.C:
			ls.arm()
			end, err = ls.keepAlive(ctx)
		}
		if err != nil {
			ls.s.streamFailed(ls.r, err)
			return
		}
		if end {
			return
		}
	}
}

// keepAlive pings the client and checks the session: a user who logged out or
// was disabled is sent to the login page.
func (ls *liveStream) keepAlive(ctx context.Context) (end bool, err error) {
	if err := ls.ping(); err != nil {
		return true, err
	}
	return ls.checkSession(ctx)
}

// checkSession reads the session of the request again. When it is gone (a
// logout, a disabled user, an expired session) the person is sent to the login
// page and the stream ends. When the role of the user changed since the stream
// opened, the stream ends without a word: the page opens it again and the
// stream starts over with the new role.
func (ls *liveStream) checkSession(ctx context.Context) (end bool, err error) {
	cookie, cookieErr := ls.r.Cookie(sessionCookieName)
	if cookieErr != nil {
		return true, ls.redirect("/login")
	}
	sess, err := ls.s.svc.Accounts.SessionActor(ctx, cookie.Value)
	if err != nil {
		if isTag(err, app.TagUnauthenticated) {
			return true, ls.redirect("/login")
		}
		return true, err
	}
	return sess.Actor.Role != ls.actor.Role, nil
}

// streamFailed logs the error that ended a stream. A failed write only means
// that the client went away.
func (s *Server) streamFailed(r *http.Request, err error) {
	if isTag(err, tagSSEWriteFailed) {
		s.logger.Debug("sse write failed", "pattern", r.Pattern, "err", err)
		return
	}
	s.logFailure(r, err)
}

// postSelector is the selector of the article of a post or comment.
func postSelector(id uint64) string {
	return "#post-" + strconv.FormatUint(id, 10)
}

// duplicateCardSelector finds the copies of the card of a post that come after
// the first one. Removing them leaves one card, the first.
func duplicateCardSelector(id uint64) string {
	return postSelector(id) + " ~ " + postSelector(id)
}

// uintQuery reads the query value key as a number; a missing or wrong value is
// 0.
func uintQuery(r *http.Request, key string) uint64 {
	n, err := strconv.ParseUint(r.URL.Query().Get(key), 10, 63)
	if err != nil {
		return 0
	}
	return n
}

// roomStream is the stream of the page of a room: its feed, its header, its
// members and the sidebar.
type roomStream struct {
	*liveStream
	id uint64
	// before is the beforeID of the page of the feed; the feed of a page that
	// shows older posts is not kept up to date.
	before uint64
	// seen is the ID of the newest post the page showed, 0 for none.
	seen uint64
}

// roomStream answers GET /rooms/{id}/stream?seen=<id>&before=<id>: a stream
// of Datastar patches that keeps the page of the room up to date for the
// person who opened it. It needs a login and a room the person may read.
// seen is the ID of the newest post the page shows and before is its beforeID;
// the feed is sent again at once when posts came or went in between. When the
// client opens the stream again (it has the header Last-Event-ID) everything
// the page shows is sent again. The stream ends when the request ends, when
// the person loses the room (it then sends the page to the home page), or
// when the hub closes it.
func (s *Server) roomStream(w http.ResponseWriter, r *http.Request) {
	id, ok := s.roomID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	actor, _ := actorFrom(ctx)
	user, _ := userFrom(ctx)
	if _, err := s.svc.Rooms.Get(ctx, actor, id); err != nil {
		s.roomFail(w, r, err, id)
		return
	}
	// Subscribe before anything is read again, so that no change falls between
	// the render and the subscription.
	sub := s.svc.Hub.Subscribe(ctx, live.RoomTopic(id), live.BoardTopic(id), live.UserTopic(actor.UserID))
	defer sub.Close()
	ls := s.newStream(w, r, actor, user)
	rs := &roomStream{liveStream: ls, id: id, before: beforeOf(r), seen: uintQuery(r, "seen")}
	st, err := rs.connect(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := ls.open(); err != nil {
		s.streamFailed(r, err)
		return
	}
	if end, err := ls.send(st); err != nil || end {
		if err != nil {
			s.streamFailed(r, err)
		}
		return
	}
	ls.run(sub, rs.event)
}

// connect reads what the stream starts with. The first connection of a page
// sends only the feed, and only when it changed since the page was made; a
// reconnection sends everything the page shows that can be stale.
func (rs *roomStream) connect(ctx context.Context) (start, error) {
	if rs.reconnect {
		return rs.refresh(ctx)
	}
	if rs.before != 0 {
		return start{}, nil
	}
	page, err := rs.s.svc.Posts.Feed(ctx, rs.actor, rs.id, 0, 0)
	if err != nil {
		return rs.lost(err)
	}
	d := views.FeedData{RoomID: rs.id, Page: page}
	if d.NewestID() == rs.seen {
		return start{}, nil
	}
	return start{ops: feedOps(d)}, nil
}

// lost turns the error of a read into the start of a stream: a room that is
// gone or hidden sends the page to the home page.
func (rs *roomStream) lost(err error) (start, error) {
	if isTag(err, app.TagNotFound) {
		return start{redirect: "/"}, nil
	}
	return start{}, err
}

// refresh reads every part of the page that can be stale: the sidebar, the
// header, the members, the editor and the first page of the feed. A reader of
// older pages of the feed loses them.
func (rs *roomStream) refresh(ctx context.Context) (start, error) {
	d, err := rs.view(ctx)
	if err != nil {
		return rs.lost(err)
	}
	sidebar, err := rs.sidebarOp(ctx, rs.id)
	if err != nil {
		return start{}, err
	}
	ops := []patchOp{
		sidebar,
		{component: views.RoomHeader(d)},
		{component: views.MembersPanel(d)},
		{component: views.RoomComposer(d)},
	}
	if rs.before != 0 {
		return start{ops: ops}, nil
	}
	page, err := rs.s.svc.Posts.Feed(ctx, rs.actor, rs.id, 0, 0)
	if err != nil {
		return rs.lost(err)
	}
	return start{ops: append(ops, feedOps(views.FeedData{RoomID: rs.id, Page: page})...)}, nil
}

// feedOps replace the posts of the feed by its first page.
func feedOps(d views.FeedData) []patchOp {
	return []patchOp{
		{component: views.FeedPosts(d), options: []datastar.PatchElementOption{datastar.WithSelector("#feed-posts"), datastar.WithModeInner()}},
		// Replaced, not morphed, so that the new sentinel is a new element and
		// Datastar watches it afresh.
		{component: views.FeedMore(d), options: []datastar.PatchElementOption{datastar.WithSelector("#feed-more"), datastar.WithModeReplace()}},
	}
}

// view reads the room for the user of the stream.
func (rs *roomStream) view(ctx context.Context) (views.RoomData, error) {
	d, err := rs.s.roomView(ctx, rs.actor, rs.id)
	if err != nil {
		return views.RoomData{}, err
	}
	d.Viewer = rs.user
	return d, nil
}

// event renders one event of the hub for the user of the stream.
func (rs *roomStream) event(ctx context.Context, ev live.Event) (end bool, err error) {
	if ev.Topic == live.UserTopic(rs.actor.UserID) {
		return rs.userEvent(ctx, ev)
	}
	switch ev.Kind {
	case live.KindPostCreated, live.KindPostUpdated, live.KindPostDeleted:
		return rs.postEvent(ctx, ev)
	case live.KindReactionChanged:
		return rs.reactionEvent(ctx, ev)
	case live.KindBoardChanged:
		return rs.boardEvent(ctx, ev)
	case live.KindMemberJoined, live.KindMemberLeft:
		return rs.memberEvent(ctx, ev)
	case live.KindRoomUpdated:
		return rs.roomUpdated(ctx)
	case live.KindRoomDeleted:
		// The room is gone: the read says so.
		if _, err := rs.s.svc.Rooms.Get(ctx, rs.actor, rs.id); err != nil {
			return rs.gone(err, "/")
		}
	}
	return false, nil
}

// userEvent handles an event of the user's topic: a count of the sidebar
// changes, or a membership does, then the sidebar is sent, and when the event
// is about this room the user may have lost it.
func (rs *roomStream) userEvent(ctx context.Context, ev live.Event) (end bool, err error) {
	if handled, err := rs.countsEvent(ctx, ev); handled {
		return false, err
	}
	if ev.RoomID == rs.id {
		if _, err := rs.s.svc.Rooms.Get(ctx, rs.actor, rs.id); err != nil {
			return rs.gone(err, "/")
		}
	}
	return false, rs.sidebar(ctx, rs.id)
}

// postEvent handles the creation, edit and deletion of a top-level post, and a
// change of its comments. The post is read again for the user, so that its
// buttons are theirs.
func (rs *roomStream) postEvent(ctx context.Context, ev live.Event) (end bool, err error) {
	if rs.before != 0 {
		return false, nil
	}
	post, err := rs.s.svc.Posts.Get(ctx, rs.actor, ev.PostID)
	if err != nil {
		if !isTag(err, app.TagNotFound) {
			return false, err
		}
		// Either the user lost the room, or the post is gone.
		if _, roomErr := rs.s.svc.Rooms.Get(ctx, rs.actor, rs.id); roomErr != nil {
			return rs.gone(roomErr, "/")
		}
		if ev.Kind == live.KindPostDeleted {
			return false, rs.remove(postSelector(ev.PostID))
		}
		return false, nil
	}
	if post.IsComment || post.RoomID != rs.id {
		return false, nil
	}
	card, err := rs.s.cardData(ctx, rs.actor, post, "", "")
	if err != nil {
		return false, err
	}
	switch {
	case ev.Kind == live.KindPostCreated:
		// The author's own page has the card already, or gets it from the
		// answer to the form at about the same time: the client applies the
		// patches of the two in any interleaving, so a card put in first and
		// removed after would be put in twice. The card is put at the top,
		// then the copies after the first are removed; the last of these
		// removals comes after both insertions, whatever the order.
		if err := rs.patch(views.PostCard(card), datastar.WithSelector("#feed-posts"), datastar.WithModePrepend()); err != nil {
			return false, err
		}
		return false, rs.remove(duplicateCardSelector(post.ID))
	case ev.Kind == live.KindPostUpdated && !post.Deleted:
		// Only the content: an author who is editing the post has its editor
		// in the article, and this finds nothing to replace.
		return false, rs.patch(views.PostContent(card))
	default:
		// A deleted post stays as a placeholder.
		return false, rs.patch(views.PostCard(card))
	}
}

// reactionEvent handles a change to the reactions of a post. The post is read
// again for the user, so that the chips are theirs (the ones they put are
// highlighted), and only the reaction bar is patched. A comment is not in the
// feed, and a post that is gone or hidden has nothing to patch.
func (rs *roomStream) reactionEvent(ctx context.Context, ev live.Event) (end bool, err error) {
	if rs.before != 0 {
		return false, nil
	}
	post, err := rs.s.svc.Posts.Get(ctx, rs.actor, ev.PostID)
	if err != nil {
		if isTag(err, app.TagNotFound) {
			return false, nil
		}
		return false, err
	}
	if post.IsComment || post.Deleted || post.RoomID != rs.id {
		return false, nil
	}
	return false, rs.patch(views.Reactions(views.ReactionsData{Post: post}))
}

// boardEvent handles a change to the board of the room: the tasks a post
// shows (their column names, and whether they are overdue) may be stale. The
// content of the post that the event names is patched; an event about no post
// (a column was renamed, moved or deleted) patches the content of every
// top-level post of the room that has tasks, which the board service lists.
// The stream does not know which posts the page has loaded (the person scrolls
// older pages in on their own), so a post that is not on the page is patched
// too, and the client finds nothing to replace.
func (rs *roomStream) boardEvent(ctx context.Context, ev live.Event) (end bool, err error) {
	ids := []uint64{ev.PostID}
	if ev.PostID == 0 {
		board, err := rs.s.svc.Board.Board(ctx, rs.actor, rs.id)
		if err != nil {
			return rs.gone(err, "/")
		}
		ids = ids[:0]
		for _, c := range board.Columns {
			for _, t := range c.Tasks {
				if t.PostID == t.RootID && !slices.Contains(ids, t.PostID) {
					ids = append(ids, t.PostID)
				}
			}
		}
	}
	for _, id := range ids {
		post, err := rs.s.svc.Posts.Get(ctx, rs.actor, id)
		if err != nil {
			if isTag(err, app.TagNotFound) {
				continue
			}
			return false, err
		}
		if post.IsComment || post.Deleted || post.RoomID != rs.id {
			continue
		}
		card, err := rs.s.cardData(ctx, rs.actor, post, "", "")
		if err != nil {
			return false, err
		}
		if err := rs.patch(views.PostContent(card)); err != nil {
			return false, err
		}
	}
	return false, nil
}

// memberEvent handles someone joining or leaving the room: the members and
// their count change, and when the person is the user of the stream, so does
// everything their role decides.
func (rs *roomStream) memberEvent(ctx context.Context, ev live.Event) (end bool, err error) {
	d, err := rs.view(ctx)
	if err != nil {
		return rs.gone(err, "/")
	}
	if err := rs.patch(views.MembersPanel(d)); err != nil {
		return false, err
	}
	if err := rs.patch(views.RoomMemberCount(d)); err != nil {
		return false, err
	}
	if ev.UserID != rs.actor.UserID {
		return false, nil
	}
	if err := rs.patch(views.RoomHeader(d)); err != nil {
		return false, err
	}
	return false, rs.patch(views.RoomComposer(d))
}

// roomUpdated handles a rename of the room: its header and the sidebar change.
func (rs *roomStream) roomUpdated(ctx context.Context) (end bool, err error) {
	d, err := rs.view(ctx)
	if err != nil {
		return rs.gone(err, "/")
	}
	if err := rs.patch(views.RoomHeader(d)); err != nil {
		return false, err
	}
	return false, rs.sidebar(ctx, rs.id)
}

// threadStream is the stream of the page of a post: its thread, and the
// sidebar.
type threadStream struct {
	*liveStream
	// rootID and roomID are the IDs of the top-level post of the thread and of
	// its room.
	rootID, roomID uint64
	// seen is the ID of the newest item the page showed.
	seen uint64
	// known holds the IDs of the comments the page shows, as far as the stream
	// knows: those of the last thread it sent, and those it inserted since. A
	// comment that is not in it is not on the page, and is inserted.
	known map[uint64]bool
}

// postStream answers GET /posts/{id}/stream?seen=<id>: a stream of Datastar
// patches that keeps the page of the post up to date for the person who opened
// it. It needs a login and a post in a room the person may read; the ID of a
// comment opens the stream of its thread. seen is the ID of the newest item the
// page shows; the thread is sent again at once when something came in between.
// When the client opens the stream again (it has the header Last-Event-ID) the
// thread and the sidebar are sent again. The stream ends when the request ends,
// when the person loses the room or the post is gone (it then sends the page
// away), or when the hub closes it.
func (s *Server) postStream(w http.ResponseWriter, r *http.Request) {
	id, ok := s.postID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	actor, _ := actorFrom(ctx)
	user, _ := userFrom(ctx)
	first, err := s.threadData(ctx, actor, id)
	if err != nil {
		s.postFail(w, r, err, id)
		return
	}
	// Subscribe before anything is read again, so that no change falls between
	// the render and the subscription. The room is listened to only for the
	// room being deleted and the user being removed from it.
	sub := s.svc.Hub.Subscribe(ctx,
		live.PostTopic(first.Post.ID), live.RoomTopic(first.Post.RoomID), live.BoardTopic(first.Post.RoomID), live.UserTopic(actor.UserID))
	defer sub.Close()
	ls := s.newStream(w, r, actor, user)
	ts := &threadStream{
		liveStream: ls, rootID: first.Post.ID, roomID: first.Post.RoomID,
		seen: uintQuery(r, "seen"), known: map[uint64]bool{},
	}
	st, err := ts.connect(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := ls.open(); err != nil {
		s.streamFailed(r, err)
		return
	}
	if end, err := ls.send(st); err != nil || end {
		if err != nil {
			s.streamFailed(r, err)
		}
		return
	}
	ls.run(sub, ts.event)
}

// reread reads the thread again for the user.
func (ts *threadStream) reread(ctx context.Context) (views.PostPageData, error) {
	d, err := ts.s.threadData(ctx, ts.actor, ts.rootID)
	if err != nil {
		return views.PostPageData{}, err
	}
	return d, nil
}

// lost handles the error of a read of the thread. When the thread is gone the
// page is sent to the home page if the room is lost too, else to the room. Any
// other error is returned.
func (ts *threadStream) lost(ctx context.Context, err error) (end bool, retErr error) {
	if !isTag(err, app.TagNotFound) {
		return false, err
	}
	if _, roomErr := ts.s.svc.Rooms.Get(ctx, ts.actor, ts.roomID); roomErr != nil {
		return ts.gone(roomErr, "/")
	}
	return true, ts.redirect(views.RoomPath(ts.roomID))
}

// remember notes the comments of the thread as shown on the page.
func (ts *threadStream) remember(d views.PostPageData) {
	ts.known = make(map[uint64]bool, len(d.Comments))
	for _, c := range d.Comments {
		ts.known[c.ID] = true
	}
}

// thread sends the whole thread, which replaces the one on the page.
func (ts *threadStream) thread(d views.PostPageData) error {
	ts.remember(d)
	return ts.patch(views.Thread(d))
}

// connect reads what the stream starts with. The first connection of a page
// sends the thread only when it changed since the page was made; a
// reconnection sends the thread and the sidebar.
func (ts *threadStream) connect(ctx context.Context) (start, error) {
	d, err := ts.reread(ctx)
	if err != nil {
		return ts.lostStart(ctx, err)
	}
	ts.remember(d)
	if !ts.reconnect && d.NewestID() == ts.seen {
		return start{}, nil
	}
	ops := []patchOp{{component: views.Thread(d)}}
	if ts.reconnect {
		sidebar, err := ts.sidebarOp(ctx, ts.roomID)
		if err != nil {
			return start{}, err
		}
		ops = append(ops, sidebar)
	}
	return start{ops: ops}, nil
}

// lostStart is lost for the start of a stream: where the page goes instead.
func (ts *threadStream) lostStart(ctx context.Context, err error) (start, error) {
	if !isTag(err, app.TagNotFound) {
		return start{}, err
	}
	if _, roomErr := ts.s.svc.Rooms.Get(ctx, ts.actor, ts.roomID); roomErr != nil {
		if isTag(roomErr, app.TagNotFound) {
			return start{redirect: "/"}, nil
		}
		return start{}, roomErr
	}
	return start{redirect: views.RoomPath(ts.roomID)}, nil
}

// event renders one event of the hub for the user of the stream.
func (ts *threadStream) event(ctx context.Context, ev live.Event) (end bool, err error) {
	switch ev.Topic {
	case live.UserTopic(ts.actor.UserID):
		return ts.userEvent(ctx, ev)
	case live.RoomTopic(ts.roomID):
		return ts.roomEvent(ctx, ev)
	case live.BoardTopic(ts.roomID):
		return ts.boardEvent(ctx, ev)
	}
	return ts.postEvent(ctx, ev)
}

// userEvent handles an event of the user's topic: a count of the sidebar
// changes, or a membership does, then the sidebar is sent, and when the event
// is about the room of the thread the user may have lost it.
func (ts *threadStream) userEvent(ctx context.Context, ev live.Event) (end bool, err error) {
	if handled, err := ts.countsEvent(ctx, ev); handled {
		return false, err
	}
	if ev.RoomID == ts.roomID {
		if _, err := ts.reread(ctx); err != nil {
			return ts.lost(ctx, err)
		}
	}
	return false, ts.sidebar(ctx, ts.roomID)
}

// roomEvent handles the events of the room that concern a thread: the room is
// deleted, or the user joins or leaves it, which changes who may reply. The
// other events of the room (the posts of the feed) are not for this page.
func (ts *threadStream) roomEvent(ctx context.Context, ev live.Event) (end bool, err error) {
	switch ev.Kind {
	case live.KindRoomDeleted:
		if _, err := ts.reread(ctx); err != nil {
			return ts.lost(ctx, err)
		}
	case live.KindMemberJoined, live.KindMemberLeft:
		if ev.UserID != ts.actor.UserID {
			return false, nil
		}
		d, err := ts.reread(ctx)
		if err != nil {
			return ts.lost(ctx, err)
		}
		return false, ts.thread(d)
	}
	return false, nil
}

// boardEvent handles a change to the board of the room: the tasks of the posts
// of the thread may be stale. The content of the post or comment that the event
// names is patched; an event about no post (a column was renamed, moved or
// deleted) patches the content of every item of the thread that has tasks.
func (ts *threadStream) boardEvent(ctx context.Context, ev live.Event) (end bool, err error) {
	d, err := ts.reread(ctx)
	if err != nil {
		return ts.lost(ctx, err)
	}
	cards := []views.PostCardData{d.RootCard()}
	for _, c := range d.Comments {
		cards = append(cards, d.CommentCard(c))
	}
	for _, card := range cards {
		if card.Post.Deleted {
			continue
		}
		if ev.PostID != 0 && card.Post.ID != ev.PostID || ev.PostID == 0 && len(card.Post.Tasks) == 0 {
			continue
		}
		if err := ts.patch(views.PostContent(card)); err != nil {
			return false, err
		}
	}
	return false, nil
}

// postEvent handles the events of the thread.
func (ts *threadStream) postEvent(ctx context.Context, ev live.Event) (end bool, err error) {
	d, err := ts.reread(ctx)
	if err != nil {
		return ts.lost(ctx, err)
	}
	switch ev.Kind {
	case live.KindCommentCreated:
		return false, ts.commentCreated(d)
	case live.KindCommentUpdated:
		for _, c := range d.Comments {
			if c.ID == ev.PostID {
				// Only the content: the reply editor open under the comment, or
				// the editor that replaced it, is left alone.
				return false, ts.patch(views.PostContent(d.CommentCard(c)))
			}
		}
	case live.KindCommentDeleted:
		// A deleted comment may stay as a placeholder, and the placeholders
		// that lose their last reply go: the structure may change.
		return false, ts.thread(d)
	case live.KindPostUpdated, live.KindPostDeleted:
		if d.Post.Deleted {
			return false, ts.patch(views.PostCard(d.RootCard()))
		}
		return false, ts.patch(views.PostContent(d.RootCard()))
	case live.KindReactionChanged:
		return false, ts.reactionChanged(d, ev.PostID)
	}
	return false, nil
}

// reactionChanged patches the reaction bar of the post or comment id, with the
// reactions as this user sees them. A post that is not on the page (it was
// deleted since) has nothing to patch.
func (ts *threadStream) reactionChanged(d views.PostPageData, id uint64) error {
	card := d.RootCard()
	if id != d.Post.ID {
		found := false
		for _, c := range d.Comments {
			if c.ID == id {
				card, found = d.CommentCard(c), true
				break
			}
		}
		if !found {
			return nil
		}
	}
	if card.Post.Deleted {
		return nil
	}
	return ts.patch(views.Reactions(views.ReactionsData{Post: card.Post, From: card.From}))
}

// commentCreated puts the comments that the page does not show yet at their
// place in the thread. They are those of the thread that are not known: the
// new comment, and any that came with it. Each is inserted after the card that
// precedes it in the thread, which is the last of the comments before it (the
// replies of the comment above it come before), without re-rendering the
// thread. The first comment of all replaces the note that there are none. A
// comment is removed before it is inserted, so that the page never shows it
// twice: the author's page has it already, from the answer to the form. Open
// reply editors are not touched.
func (ts *threadStream) commentCreated(d views.PostPageData) error {
	// The count of comments of the post.
	if err := ts.patch(views.PostContent(d.RootCard())); err != nil {
		return err
	}
	if len(ts.known) == 0 {
		return ts.commentsSection(d)
	}
	var prev uint64
	for _, c := range d.Comments {
		if !ts.known[c.ID] {
			if prev == 0 {
				// Nothing to insert it after: show the whole list.
				return ts.commentsSection(d)
			}
			if err := ts.insertComment(d, c, prev); err != nil {
				return err
			}
			ts.known[c.ID] = true
		}
		prev = c.ID
	}
	return nil
}

// commentsSection replaces the comments of the page by those of the thread.
func (ts *threadStream) commentsSection(d views.PostPageData) error {
	ts.remember(d)
	return ts.patch(views.CommentsSection(d))
}

// insertComment inserts the card of a comment after the card of the comment
// prev.
func (ts *threadStream) insertComment(d views.PostPageData, c app.Comment, prev uint64) error {
	if err := ts.remove(postSelector(c.ID)); err != nil {
		return err
	}
	return ts.patch(views.PostCard(d.CommentCard(c)), datastar.WithSelector(postSelector(prev)), datastar.WithModeAfter())
}
