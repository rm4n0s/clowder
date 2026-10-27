package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/web/views"
)

// streamOfBoard is the path of the stream of the board of the room, with the
// query.
func (e *roomsEnv) streamOfBoard(room string, query string) string {
	path := e.boardURL(room, "/stream")
	if query != "" {
		path += "?" + query
	}
	return path
}

// boardVersion is the version of the board of the room that the page of the
// user would tell its stream.
func (e *roomsEnv) boardVersion(user, room string) string {
	return views.BoardVersion(e.boardOf(user, room))
}

func TestBoardStreamPatchesTheColumns(t *testing.T) {
	type env = *roomsEnv
	tests := []struct {
		name string
		// act is what alice does; the streams of bob (a member) and carol
		// (who has not joined) are open.
		act  func(e env, s boardSeed)
		want []string
	}{
		{
			name: "a card moved by a drag",
			act: func(e env, s boardSeed) {
				rec := e.post("alice", e.cardPath(s.soon, "move"), url.Values{"column": {idText(s.doing)}, "position": {"0"}}, true)
				requireSSE(t, rec)
			},
			want: []string{`id="board-columns"`, `aria-label="In progress"`, `<p class="card-title">Soon</p>`},
		},
		{
			name: "a card edited",
			act: func(e env, s boardSeed) {
				rec := e.post("alice", e.cardPath(s.plain, ""), url.Values{"title": {"Plain, edited"}}, true)
				requireSSE(t, rec)
			},
			want: []string{`id="board-columns"`, "Plain, edited"},
		},
		{
			name: "a column renamed",
			act: func(e env, s boardSeed) {
				if err := e.board.RenameColumn(t.Context(), e.actor(e.alice), s.doing, "Doing"); err != nil {
					t.Fatalf("RenameColumn: %+v", err)
				}
			},
			want: []string{`id="board-columns"`, `aria-label="Doing"`},
		},
		{
			name: "a column added",
			act: func(e env, s boardSeed) {
				if _, err := e.board.AddColumn(t.Context(), e.actor(e.alice), e.general.ID, "Review"); err != nil {
					t.Fatalf("AddColumn: %+v", err)
				}
			},
			want: []string{`id="board-columns"`, `aria-label="Review"`},
		},
		{
			name: "a post with a task written",
			act: func(e env, s boardSeed) {
				e.writeTasks("alice", "general", "More", appTask("Fresh"))
			},
			want: []string{`id="board-columns"`, `<p class="card-title">Fresh</p>`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRoomsEnv(t)
			s := e.seedBoard()
			ss := e.serveStreams()
			version := "v=" + e.boardVersion("bob", "general")
			bob := ss.open("bob", e.streamOfBoard("general", version), asDatastar())
			carol := ss.open("carol", e.streamOfBoard("general", "v="+e.boardVersion("carol", "general")), asDatastar())
			if bob.status != http.StatusOK || carol.status != http.StatusOK {
				t.Fatalf("status = %d and %d, want 200", bob.status, carol.status)
			}
			tt.act(e, s)
			// The patch is for the user: a member has the controls, a person
			// who has not joined the room reads.
			requirePatch(t, bob.patches(1)[0], "", "outer", append(tt.want, `draggable="true"`, `class="column-rename"`), nil)
			requirePatch(t, carol.patches(1)[0], "", "outer", tt.want, []string{"draggable", "column-rename", "card-move"})
			bob.requireQuiet()
			carol.requireQuiet()
		})
	}
}

func TestBoardStreamShowsTheOrderOfTheServer(t *testing.T) {
	// Bob drags a card: alice's board gets the columns with the new order.
	e := newRoomsEnv(t)
	s := e.seedBoard()
	alice := e.serveStreams().open("alice", e.streamOfBoard("general", "v="+e.boardVersion("alice", "general")), asDatastar())
	rec := e.post("bob", e.cardPath(s.plain, "move"), url.Values{"column": {idText(s.done)}, "position": {"1"}}, true)
	requireSSE(t, rec)
	p := alice.patches(1)[0]
	requirePatch(t, p, "", "outer", []string{`id="board-columns"`}, nil)
	requireOrder(t, p.html, `aria-label="To do"`, "Late", "Soon", "Comment task", `aria-label="In progress"`, `aria-label="Done"`, "Finished", "Plain")
	alice.requireQuiet()
}

func TestBoardStreamIgnoresTheBoardOfAnotherRoom(t *testing.T) {
	e := newRoomsEnv(t)
	e.seedBoard()
	c := e.serveStreams().open("alice", e.streamOfBoard("general", "v="+e.boardVersion("alice", "general")), asDatastar())
	if err := e.board.RenameColumn(t.Context(), e.actor(e.alice), e.columnID("secret", 0), "Elsewhere"); err != nil {
		t.Fatalf("RenameColumn: %+v", err)
	}
	e.writePost("alice", "general", "just a post")
	c.requireQuiet()
}

func TestBoardStreamConnects(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		header    map[string]string
		wantQuiet bool
		n         int
		want      func(t *testing.T, got []patch)
	}{
		{
			name: "the first connection of a page that is up to date sends nothing", query: "v=CURRENT",
			wantQuiet: true,
		},
		{
			name: "the first connection of a page that is behind sends what depends on the board", query: "v=0", n: 3,
			want: func(t *testing.T, got []patch) {
				requirePatch(t, got[0], "", "outer", []string{`id="board-note"`}, []string{"Join this room"})
				requirePatch(t, got[1], "", "outer", []string{`id="board-add"`, `id="board-add-form"`}, nil)
				requirePatch(t, got[2], "", "outer", []string{`id="board-columns"`, "Late"}, nil)
			},
		},
		{
			name: "a page with no version is behind", query: "", n: 3,
			want: func(t *testing.T, got []patch) {
				requirePatch(t, got[2], "", "outer", []string{`id="board-columns"`}, nil)
			},
		},
		{
			name: "a reconnection sends everything the page shows", query: "v=CURRENT",
			header: map[string]string{"Last-Event-ID": "1"}, n: 5,
			want: func(t *testing.T, got []patch) {
				requirePatch(t, got[0], "#sidebar", "inner", []string{`data-room="general"`, `aria-current="page"`}, nil)
				requirePatch(t, got[1], "", "outer", []string{`id="board-header"`, "<h1>general</h1>"}, nil)
				requirePatch(t, got[2], "", "outer", []string{`id="board-note"`}, nil)
				requirePatch(t, got[3], "", "outer", []string{`id="board-add"`}, nil)
				requirePatch(t, got[4], "", "outer", []string{`id="board-columns"`, "Late"}, nil)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRoomsEnv(t)
			e.seedBoard()
			query := strings.ReplaceAll(tt.query, "CURRENT", e.boardVersion("alice", "general"))
			opts := []reqOpt{asDatastar()}
			for k, v := range tt.header {
				opts = append(opts, withHeader(k, v))
			}
			c := e.serveStreams().open("alice", e.streamOfBoard("general", query), opts...)
			if tt.wantQuiet {
				c.requireQuiet()
				return
			}
			tt.want(t, c.patches(tt.n))
		})
	}
}

func TestBoardStreamNoticesAChangeWhileThePageLoaded(t *testing.T) {
	// The page tells its stream the version it shows: a move that came between
	// the render and the subscription is sent at once.
	e := newRoomsEnv(t)
	s := e.seedBoard()
	version := e.boardVersion("alice", "general")
	if err := e.board.MoveTask(t.Context(), e.actor(e.bob), s.plain.ID, s.doing, 0); err != nil {
		t.Fatalf("MoveTask: %+v", err)
	}
	c := e.serveStreams().open("alice", e.streamOfBoard("general", "v="+version), asDatastar())
	got := c.patches(3)
	requireOrder(t, got[2].html, `aria-label="In progress"`, "Plain", `aria-label="Done"`)
}

func TestBoardStreamEndsWhenTheRoomIsLost(t *testing.T) {
	type env = *roomsEnv
	tests := []struct {
		name string
		as   string
		room string
		act  func(e env)
	}{
		{
			name: "removed from a private room",
			as:   "bob", room: "secret",
			act: func(e env) {
				if err := e.rooms.RemoveMember(e.t.Context(), e.actor(e.admin), e.secret.ID, e.bob.ID); err != nil {
					e.t.Fatalf("RemoveMember: %+v", err)
				}
			},
		},
		{
			name: "the room is deleted",
			as:   "alice", room: "general",
			act: func(e env) {
				if err := e.rooms.Delete(e.t.Context(), e.actor(e.admin), e.general.ID); err != nil {
					e.t.Fatalf("Delete: %+v", err)
				}
			},
		},
		{
			name: "the room is deleted, seen by a person who has not joined",
			as:   "carol", room: "general",
			act: func(e env) {
				if err := e.rooms.Delete(e.t.Context(), e.actor(e.admin), e.general.ID); err != nil {
					e.t.Fatalf("Delete: %+v", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRoomsEnv(t)
			c := e.serveStreams().open(tt.as, e.streamOfBoard(tt.room, "v="+e.boardVersion(tt.as, tt.room)), asDatastar())
			tt.act(e)
			requireRedirect(t, patchOf(t, c.next()), "/")
			c.requireEnd()
		})
	}
}

func TestBoardStreamFollowsTheMembershipOfTheUser(t *testing.T) {
	e := newRoomsEnv(t)
	e.seedBoard()
	c := e.serveStreams().open("carol", e.streamOfBoard("general", "v="+e.boardVersion("carol", "general")), asDatastar())

	// Carol joins: the note goes, the controls come, and the sidebar lists the room.
	if err := e.rooms.Join(t.Context(), e.actor(e.carol), e.general.ID); err != nil {
		t.Fatalf("Join: %+v", err)
	}
	var joined strings.Builder
	for _, p := range c.drain() {
		joined.WriteString(p.html)
	}
	requireContains(t, joined.String(), []string{
		`<div id="board-note"></div>`, `id="board-add-form"`, `draggable="true"`, `class="card-move"`, `data-room="general"`,
	}, nil)

	// She leaves: back to reading; a card form she had open is emptied.
	if err := e.rooms.Leave(t.Context(), e.actor(e.carol), e.general.ID); err != nil {
		t.Fatalf("Leave: %+v", err)
	}
	var left strings.Builder
	for _, p := range c.drain() {
		left.WriteString(p.html)
	}
	requireContains(t, left.String(), []string{
		"Join this room to change the board", `<div id="board-add" class="board-add"></div>`, `<div id="board-editor" class="board-editor"></div>`,
	}, []string{"draggable"})
}

func TestBoardStreamIgnoresTheMembershipOfOthers(t *testing.T) {
	e := newRoomsEnv(t)
	e.seedBoard()
	c := e.serveStreams().open("alice", e.streamOfBoard("general", "v="+e.boardVersion("alice", "general")), asDatastar())
	if err := e.rooms.Join(t.Context(), e.actor(e.carol), e.general.ID); err != nil {
		t.Fatalf("Join: %+v", err)
	}
	c.requireQuiet()
}

func TestBoardStreamFollowsTheRoom(t *testing.T) {
	e := newRoomsEnv(t)
	c := e.serveStreams().open("alice", e.streamOfBoard("general", "v="+e.boardVersion("alice", "general")), asDatastar())
	if err := e.rooms.Update(t.Context(), e.actor(e.admin), e.general.ID, appRoomInput("lobby")); err != nil {
		t.Fatalf("Update: %+v", err)
	}
	got := c.patches(2)
	requirePatch(t, got[0], "", "outer", []string{`id="board-header"`, "<h1>lobby</h1>"}, nil)
	requirePatch(t, got[1], "#sidebar", "inner", []string{`data-room="lobby"`, `aria-current="page"`}, nil)
}

func TestBoardStreamRefusesWhoCannotRead(t *testing.T) {
	e := newRoomsEnv(t)
	ss := e.serveStreams()
	secret := e.streamOfBoard("secret", "")
	tests := []struct {
		name         string
		user         string
		path         string
		opts         []reqOpt
		wantStatus   int
		wantRedirect string
	}{
		{"a non-member of a private room, plain", "carol", secret, nil, http.StatusNotFound, ""},
		{"a non-member of a private room, datastar", "carol", secret, []reqOpt{asDatastar()}, http.StatusOK, "/rooms/" + idText(e.secret.ID)},
		{"an admin who is not a member", "admin", secret, nil, http.StatusNotFound, ""},
		{"nobody, datastar", "", secret, []reqOpt{asDatastar()}, http.StatusOK, "/login"},
		{"a room that does not exist", "alice", "/rooms/9999/board/stream", nil, http.StatusNotFound, ""},
		{"a bad id", "alice", "/rooms/x/board/stream", nil, http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ss.open(tt.user, tt.path, tt.opts...)
			if c.status != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body:\n%s", c.status, tt.wantStatus, c.body)
			}
			if tt.wantRedirect != "" {
				requireRedirect(t, patchOf(t, c.next()), tt.wantRedirect)
				c.requireEnd()
			}
		})
	}
}

func TestBoardStreamKeepAliveAndEnd(t *testing.T) {
	e := newRoomsEnv(t)
	e.srv.pingInterval = 20 * time.Millisecond
	ss := e.serveStreams()
	c := ss.open("alice", e.streamOfBoard("general", "v="+e.boardVersion("alice", "general")), asDatastar())
	for range 2 {
		if got := c.comment(); got != "ping" {
			t.Errorf("comment = %q, want ping", got)
		}
	}
	// A user who is disabled is sent to the login page.
	bob := ss.open("bob", e.streamOfBoard("general", "v="+e.boardVersion("bob", "general")), asDatastar())
	if err := e.accounts.SetDisabled(t.Context(), e.actor(e.admin), e.bob.ID, true); err != nil {
		t.Fatalf("SetDisabled: %+v", err)
	}
	requireRedirect(t, patchOf(t, bob.next()), "/login")
	bob.requireEnd()
	// The hub closing ends the others.
	e.hub.Close()
	c.requireEnd()
}
