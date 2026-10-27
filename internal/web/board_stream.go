package web

import (
	"context"
	"net/http"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/web/views"
)

// boardStream is the stream of the page of a board: its columns, its header
// and note, and the sidebar.
type boardStream struct {
	*liveStream
	id uint64
	// version is the BoardVersion of the board the page showed.
	version string
}

// boardStream answers GET /rooms/{id}/board/stream?v=<version>: a stream of
// Datastar patches that keeps the page of the board up to date for the person
// who opened it. It needs a login and a room the person may read. v is the
// BoardVersion of the board the page shows; the columns are sent again at once
// when the board changed in between. When the client opens the stream again
// (it has the header Last-Event-ID) everything the page shows is sent again.
// A change of the board patches only the columns, which hold no form: the
// form of a card is in its own panel and the form that adds a column sits
// beside them, so a live update never closes a form or clears what is typed
// in it. The page itself stops patches while a card is held (see board.js).
// The stream ends when the request ends, when the person loses the room (it
// then sends the page to the home page), or when the hub closes it.
func (s *Server) boardStream(w http.ResponseWriter, r *http.Request) {
	id, ok := s.roomID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	actor, _ := actorFrom(ctx)
	user, _ := userFrom(ctx)
	if _, err := s.boardView(ctx, actor, id); err != nil {
		s.roomFail(w, r, err, id)
		return
	}
	// Subscribe before anything is read again, so that no change falls between
	// the render and the subscription.
	sub := s.svc.Hub.Subscribe(ctx, live.BoardTopic(id), live.RoomTopic(id), live.UserTopic(actor.UserID))
	defer sub.Close()
	ls := s.newStream(w, r, actor, user)
	bs := &boardStream{liveStream: ls, id: id, version: r.URL.Query().Get("v")}
	st, err := bs.connect(ctx)
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
	ls.run(sub, bs.event)
}

// connect reads what the stream starts with. The first connection of a page
// sends the columns only when the board changed since the page was made; a
// reconnection sends everything the page shows that can be stale.
func (bs *boardStream) connect(ctx context.Context) (start, error) {
	view, err := bs.s.boardView(ctx, bs.actor, bs.id)
	if err != nil {
		return bs.lost(err)
	}
	d := views.BoardData{Board: view}
	if !bs.reconnect {
		if views.BoardVersion(view) == bs.version {
			return start{}, nil
		}
		return start{ops: memberOps(d)}, nil
	}
	sidebar, err := bs.sidebarOp(ctx, bs.id)
	if err != nil {
		return start{}, err
	}
	ops := []patchOp{sidebar, {component: views.BoardHeader(d)}}
	return start{ops: append(ops, memberOps(d)...)}, nil
}

// lost turns the error of a read into the start of a stream: a room that is
// gone or hidden sends the page to the home page.
func (bs *boardStream) lost(err error) (start, error) {
	if isTag(err, app.TagNotFound) {
		return start{redirect: "/"}, nil
	}
	return start{}, err
}

// memberOps are the patches of the parts of the board that depend on whether
// the person is a member: the note, the columns with their controls and the
// form that adds a column.
func memberOps(d views.BoardData) []patchOp {
	return []patchOp{
		{component: views.BoardNote(d)},
		{component: views.BoardAddColumn(d)},
		{component: views.BoardColumns(d)},
	}
}

// view reads the board for the user of the stream.
func (bs *boardStream) view(ctx context.Context) (views.BoardData, error) {
	view, err := bs.s.boardView(ctx, bs.actor, bs.id)
	if err != nil {
		return views.BoardData{}, err
	}
	return views.BoardData{Board: view}, nil
}

// event renders one event of the hub for the user of the stream.
func (bs *boardStream) event(ctx context.Context, ev live.Event) (end bool, err error) {
	switch ev.Topic {
	case live.UserTopic(bs.actor.UserID):
		return bs.userEvent(ctx, ev)
	case live.RoomTopic(bs.id):
		return bs.roomEvent(ctx, ev)
	}
	if ev.Kind != live.KindBoardChanged {
		return false, nil
	}
	d, err := bs.view(ctx)
	if err != nil {
		return bs.gone(err, "/")
	}
	return false, bs.patch(views.BoardColumns(d))
}

// userEvent handles an event of the user's topic: a count of the sidebar
// changes, or a membership does, then the sidebar is sent, and when the event
// is about this room the user may have lost it, or joined or left it, which
// changes what they may do on the board.
func (bs *boardStream) userEvent(ctx context.Context, ev live.Event) (end bool, err error) {
	if handled, err := bs.countsEvent(ctx, ev); handled {
		return false, err
	}
	if ev.RoomID != bs.id {
		return false, bs.sidebar(ctx, bs.id)
	}
	d, err := bs.view(ctx)
	if err != nil {
		return bs.gone(err, "/")
	}
	if err := bs.sidebar(ctx, bs.id); err != nil {
		return false, err
	}
	return false, bs.memberPatches(d)
}

// memberPatches sends the parts of the board that depend on membership. The
// form of a card is emptied when the person may no longer change the board.
func (bs *boardStream) memberPatches(d views.BoardData) error {
	for _, op := range memberOps(d) {
		if err := bs.patch(op.component, op.options...); err != nil {
			return err
		}
	}
	if !d.Board.CanEdit {
		return bs.patch(views.BoardEditor(d))
	}
	return nil
}

// roomEvent handles the events of the room that concern the board: the room is
// deleted, renamed, or the user joins or leaves it. The other events of the
// room (the posts of the feed) are not for this page.
func (bs *boardStream) roomEvent(ctx context.Context, ev live.Event) (end bool, err error) {
	switch ev.Kind {
	case live.KindRoomDeleted:
		// The room is gone: the read says so.
		if _, err := bs.s.boardView(ctx, bs.actor, bs.id); err != nil {
			return bs.gone(err, "/")
		}
	case live.KindRoomUpdated:
		d, err := bs.view(ctx)
		if err != nil {
			return bs.gone(err, "/")
		}
		if err := bs.patch(views.BoardHeader(d)); err != nil {
			return false, err
		}
		return false, bs.sidebar(ctx, bs.id)
	case live.KindMemberJoined, live.KindMemberLeft:
		if ev.UserID != bs.actor.UserID {
			return false, nil
		}
		d, err := bs.view(ctx)
		if err != nil {
			return bs.gone(err, "/")
		}
		return false, bs.memberPatches(d)
	}
	return false, nil
}
