package web

import (
	"context"
	"net/http"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/web/views"
)

// conversationStream is the stream of the page of a conversation: its
// messages, its header and members, "Seen", and the counts of the sidebar.
type conversationStream struct {
	*liveStream
	id uint64
	// before is the before of the page of messages; the messages of a page
	// that shows older ones are not kept up to date (but a change to one of
	// them is shown).
	before uint64
	// seen is the ID of the newest message the page showed, 0 for none.
	seen uint64
}

// conversationStream answers GET /messages/{id}/stream?seen=<id>&before=<id>: a
// stream of Datastar patches that keeps the page of the conversation up to date
// for the member who opened it. It needs a login and a conversation the person
// is a member of. seen is the ID of the newest message the page shows and
// before is its before; the messages sent after seen are put at the end of the
// list at once when there are any. When the client opens the stream again (it
// has the header Last-Event-ID) everything the page shows that can be stale is
// sent again.
//
// A new message of someone else is put at the end of the list and the
// conversation is marked read up to it: an open conversation counts as read
// (D33). The stream ends when the request ends, when the person loses the
// conversation (it then sends the page to the list of conversations), when the
// session ends, or when the hub closes it.
func (s *Server) conversationStream(w http.ResponseWriter, r *http.Request) {
	id, ok := s.conversationID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	actor, _ := actorFrom(ctx)
	user, _ := userFrom(ctx)
	if _, err := s.svc.Messages.Conversation(ctx, actor, id); err != nil {
		s.conversationFail(w, r, err, id)
		return
	}
	// Subscribe before anything is read again, so that no change falls between
	// the render and the subscription.
	sub := s.svc.Hub.Subscribe(ctx, live.ConversationTopic(id), live.UserTopic(actor.UserID))
	defer sub.Close()
	ls := s.newStream(w, r, actor, user)
	cs := &conversationStream{liveStream: ls, id: id, before: beforeOf(r), seen: uintQuery(r, "seen")}
	st, err := cs.connect(ctx)
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
	ls.run(sub, cs.event)
}

// lost turns the error of a read into the start of a stream: a conversation
// the person left sends the page to the list of conversations.
func (cs *conversationStream) lost(err error) (start, error) {
	if isTag(err, app.TagNotFound) {
		return start{redirect: views.MessagesPath}, nil
	}
	return start{}, err
}

// connect reads what the stream starts with. The first connection of a page
// sends the messages that came since the page was made, and "Seen"; a
// reconnection sends everything the page shows that can be stale: the sidebar,
// the header, the members and the messages of the newest page. A message of
// someone else that the page did not show is marked read.
func (cs *conversationStream) connect(ctx context.Context) (start, error) {
	h, err := cs.s.svc.Messages.Conversation(ctx, cs.actor, cs.id)
	if err != nil {
		return cs.lost(err)
	}
	var ops []patchOp
	if cs.reconnect {
		sidebar, err := cs.sidebarOps(ctx, 0)
		if err != nil {
			return start{}, err
		}
		ops = append(ops, sidebar...)
		ops = append(ops, cs.headerOps(ctx, h)...)
		// A call may have started, been answered or ended meanwhile.
		d, err := cs.s.callData(ctx, cs.actor, h)
		if err != nil {
			return start{}, err
		}
		if d.Enabled {
			ops = append(ops, callOps(d)...)
		}
	}
	if cs.before != 0 {
		return start{ops: ops}, nil
	}
	page, err := cs.s.svc.Messages.History(ctx, cs.actor, cs.id, 0, 0)
	if err != nil {
		return cs.lost(err)
	}
	viewer := cs.s.viewerOf(cs.actor, h)
	for _, m := range page.Messages {
		item, err := viewer.data(m, 0)
		if err != nil {
			return start{}, err
		}
		switch {
		case m.ID > cs.seen:
			// Removed before it is inserted again, so that a reconnection
			// never shows it twice.
			ops = append(ops, appendMessageOps(item)...)
		case cs.reconnect:
			ops = append(ops, patchOp{component: views.MessageItem(item)})
		}
	}
	if newest := newestMessage(page.Messages); newest != nil && newest.ID > cs.seen && newest.Author.ID != cs.actor.UserID {
		if err := cs.s.svc.Messages.MarkRead(ctx, cs.actor, cs.id, newest.ID); err != nil {
			return cs.lost(err)
		}
	}
	ops = append(ops, seenOps(cs.actor, h, page.Messages)...)
	return start{ops: ops}, nil
}

// newestMessage returns the newest of the messages of a page, nil for none.
func newestMessage(ms []app.Message) *app.Message {
	if len(ms) == 0 {
		return nil
	}
	return &ms[len(ms)-1]
}

// headerOps are the patches of the header of the conversation and, for a
// group, of its members panel.
func (cs *conversationStream) headerOps(ctx context.Context, h app.ConversationHeader) []patchOp {
	panel := views.MembersPanelData{Header: h, ViewerID: cs.actor.UserID, Online: cs.s.onlineSet(ctx, cs.actor, memberIDsOf(h))}
	ops := []patchOp{{component: views.ConversationHeader(views.ConversationData{Header: h, ViewerID: cs.actor.UserID, Members: panel})}}
	if h.IsGroup {
		ops = append(ops, patchOp{component: views.ConversationMembers(panel)})
	}
	return ops
}

// seenOps are the patches of "Seen": the old mark goes, and the message the
// other member has read up to gets it. A group has none.
func seenOps(actor app.Actor, h app.ConversationHeader, messages []app.Message) []patchOp {
	if h.Kind != app.ConversationDirect {
		return nil
	}
	ops := []patchOp{{remove: messageSeenMarkSelector}}
	if id := views.SeenMessageID(actor.UserID, h, messages); id != 0 {
		ops = append(ops, patchOp{component: views.MessageSeen(id, true)})
	}
	return ops
}

// event renders one event of the hub for the user of the stream.
func (cs *conversationStream) event(ctx context.Context, ev live.Event) (end bool, err error) {
	if ev.Topic == live.UserTopic(cs.actor.UserID) {
		return cs.userEvent(ctx, ev)
	}
	switch ev.Kind {
	case live.KindMessageCreated:
		return cs.created(ctx, ev)
	case live.KindMessageUpdated, live.KindMessageDeleted:
		return cs.changed(ctx, ev)
	case live.KindMessageReaction:
		return cs.reacted(ctx, ev)
	case live.KindConversationUpdated:
		return cs.updated(ctx)
	case live.KindConversationRead:
		return cs.read(ctx, ev)
	}
	return false, nil
}

// userEvent handles an event of the user's topic: a count of the sidebar
// changes, or a membership of a room does, then the sidebar is sent.
func (cs *conversationStream) userEvent(ctx context.Context, ev live.Event) (end bool, err error) {
	if isCallEvent(ev.Kind) {
		return false, cs.callEvent(ctx, ev)
	}
	if handled, err := cs.countsEvent(ctx, ev); handled {
		return false, err
	}
	return false, cs.sidebar(ctx, 0)
}

// goneTo ends the stream of a person who may no longer read the conversation
// (the person left the group), sending the page to the list of conversations.
// Any other error is returned.
func (cs *conversationStream) goneTo(err error) (end bool, retErr error) {
	return cs.gone(err, views.MessagesPath)
}

// created handles a new message: it is put at the end of the list (the sender's
// own page has it already, or gets it from the answer to the form at about the
// same time, so it is put in and then the copies after the first are removed),
// and a message of someone else is marked read (D33). A page of older messages
// does not keep the end of the list.
func (cs *conversationStream) created(ctx context.Context, ev live.Event) (end bool, err error) {
	if cs.before != 0 {
		return false, nil
	}
	msg, err := cs.s.svc.Messages.Message(ctx, cs.actor, ev.MessageID)
	if err != nil {
		return cs.goneTo(err)
	}
	h, err := cs.s.svc.Messages.Conversation(ctx, cs.actor, cs.id)
	if err != nil {
		return cs.goneTo(err)
	}
	item, err := cs.s.viewerOf(cs.actor, h).data(msg, 0)
	if err != nil {
		return false, err
	}
	for _, op := range appendMessageOps(item) {
		if op.remove != "" {
			err = cs.remove(op.remove)
		} else {
			err = cs.patch(op.component, op.options...)
		}
		if err != nil {
			return false, err
		}
	}
	if msg.Author.ID != cs.actor.UserID {
		if err := cs.s.svc.Messages.MarkRead(ctx, cs.actor, cs.id, msg.ID); err != nil {
			return cs.goneTo(err)
		}
	}
	return false, nil
}

// changed handles the edit or deletion of a message: its content is replaced,
// with the buttons the person has. A deleted message may have been the one
// "Seen" showed under.
func (cs *conversationStream) changed(ctx context.Context, ev live.Event) (end bool, err error) {
	msg, err := cs.s.svc.Messages.Message(ctx, cs.actor, ev.MessageID)
	if err != nil {
		return cs.goneTo(err)
	}
	item, err := cs.s.messageContentFor(ctx, cs.actor, msg)
	if err != nil {
		return cs.goneTo(err)
	}
	if err := cs.patch(views.MessageContent(item)); err != nil {
		return false, err
	}
	if ev.Kind == live.KindMessageDeleted {
		return cs.seenMarks(ctx)
	}
	return false, nil
}

// reacted handles a change to the reactions of a message. The message is read
// again for the person, so that the chips are theirs (the ones they put are
// highlighted), and only the reaction bar is patched, not the picker next to
// it. A deleted message has no reactions to show.
func (cs *conversationStream) reacted(ctx context.Context, ev live.Event) (end bool, err error) {
	msg, err := cs.s.svc.Messages.Message(ctx, cs.actor, ev.MessageID)
	if err != nil {
		return cs.goneTo(err)
	}
	if msg.Deleted {
		return false, nil
	}
	return false, cs.patch(views.MessageReactions(views.MessageReactionsData{Message: msg}))
}

// updated handles a rename of the group or a change of its members: the header
// and the members panel change. A person who left reads the page no more.
func (cs *conversationStream) updated(ctx context.Context) (end bool, err error) {
	h, err := cs.s.svc.Messages.Conversation(ctx, cs.actor, cs.id)
	if err != nil {
		return cs.goneTo(err)
	}
	for _, op := range cs.headerOps(ctx, h) {
		if err := cs.patch(op.component, op.options...); err != nil {
			return false, err
		}
	}
	return false, nil
}

// read handles another member reading the conversation: "Seen" moves. The
// person's own reading changes nothing on the page.
func (cs *conversationStream) read(ctx context.Context, ev live.Event) (end bool, err error) {
	if ev.UserID == cs.actor.UserID {
		return false, nil
	}
	return cs.seenMarks(ctx)
}

// seenMarks sends the patches of "Seen" for the messages of the newest page.
func (cs *conversationStream) seenMarks(ctx context.Context) (end bool, err error) {
	h, err := cs.s.svc.Messages.Conversation(ctx, cs.actor, cs.id)
	if err != nil {
		return cs.goneTo(err)
	}
	page, err := cs.s.svc.Messages.History(ctx, cs.actor, cs.id, 0, 0)
	if err != nil {
		return cs.goneTo(err)
	}
	_, err = cs.send(start{ops: seenOps(cs.actor, h, page.Messages)})
	return false, err
}
