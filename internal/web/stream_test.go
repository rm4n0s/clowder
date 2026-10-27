package web

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
)

// streamTimeout is how long a test waits for an event, or for a stream to end.
// It is only the limit after which a missing event fails the test, so a long
// one costs nothing when the event comes. An event is read from the database
// and rendered, which on PostgreSQL under the load of the parallel tests, the
// race detector and the server's own processes took longer than 3 seconds
// (step 14.2; the same load on SQLite did not).
const streamTimeout = 10 * time.Second

// quietTime is how long a test waits to see that nothing is sent. The stream
// of an event reads and renders under the load of the other tests that run in
// parallel, with the race detector on, so a short wait sees nothing yet and the
// test takes the late patch for the next one's.
const quietTime = 400 * time.Millisecond

// sseBlock is one block of an SSE stream: an event, a comment, or the id that
// the server sends first.
type sseBlock struct {
	id      string
	event   string
	comment string
	data    []string
}

// patch is the element patch of a block.
type patch struct {
	selector string
	mode     string
	html     string
}

// patchOf reads the data lines of a datastar-patch-elements block.
func patchOf(t *testing.T, b sseBlock) patch {
	t.Helper()
	if b.event != "datastar-patch-elements" {
		t.Fatalf("event = %q, want datastar-patch-elements; data: %q", b.event, b.data)
	}
	p := patch{mode: "outer"}
	var html []string
	for _, line := range b.data {
		switch {
		case strings.HasPrefix(line, "selector "):
			p.selector = strings.TrimPrefix(line, "selector ")
		case strings.HasPrefix(line, "mode "):
			p.mode = strings.TrimPrefix(line, "mode ")
		case strings.HasPrefix(line, "elements "):
			html = append(html, strings.TrimPrefix(line, "elements "))
		}
	}
	p.html = strings.Join(html, "\n")
	return p
}

// streamClient reads the stream of a request that is open.
type streamClient struct {
	t      *testing.T
	status int
	header http.Header
	blocks chan sseBlock
	cancel context.CancelFunc
	// body is the whole body of an answer that is not a stream.
	body string
}

// streamServer is a real HTTP server on the handler of a test environment.
type streamServer struct {
	*httptest.Server
	e *roomsEnv
}

// serveStreams starts a real HTTP server on the handler of the environment.
func (e *roomsEnv) serveStreams() *streamServer {
	e.t.Helper()
	ts := httptest.NewServer(e.srv.Handler())
	e.t.Cleanup(ts.Close)
	return &streamServer{Server: ts, e: e}
}

// open sends a GET as the user ("" for nobody) and returns once the response
// headers are in, which for a stream means that it has subscribed.
func (ss *streamServer) open(user, path string, opts ...reqOpt) *streamClient {
	ss.e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ss.URL+path, nil)
	if err != nil {
		cancel()
		ss.e.t.Fatal(err)
	}
	if user != "" {
		ss.e.cookies[user](req)
	}
	for _, o := range opts {
		o(req)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		ss.e.t.Fatalf("GET %s: %v", path, err)
	}
	c := &streamClient{t: ss.e.t, status: resp.StatusCode, header: resp.Header, blocks: make(chan sseBlock, 256), cancel: cancel}
	ss.e.t.Cleanup(cancel)
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		defer cancel()
		defer func() { _ = resp.Body.Close() }()
		var sb strings.Builder
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			sb.WriteString(sc.Text() + "\n")
		}
		c.body = sb.String()
		close(c.blocks)
		return c
	}
	go c.read(resp)
	return c
}

// read parses the body into blocks until it ends.
func (c *streamClient) read(resp *http.Response) {
	defer close(c.blocks)
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var b sseBlock
	empty := true
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if !empty {
				c.blocks <- b
			}
			b, empty = sseBlock{}, true
			continue
		}
		empty = false
		switch {
		case strings.HasPrefix(line, ":"):
			b.comment = strings.TrimSpace(strings.TrimPrefix(line, ":"))
		case strings.HasPrefix(line, "id: "):
			b.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			b.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			b.data = append(b.data, strings.TrimPrefix(line, "data: "))
		}
	}
}

// next returns the next event, skipping the id that opens the stream and the
// pings. It fails the test when none comes in time or the stream ended.
func (c *streamClient) next() sseBlock {
	c.t.Helper()
	for {
		select {
		case b, ok := <-c.blocks:
			if !ok {
				c.t.Fatal("the stream ended; wanted an event")
			}
			if b.event == "" {
				continue
			}
			return b
		case <-time.After(streamTimeout):
			c.t.Fatal("no event came in time")
		}
	}
}

// patches returns the next n patches.
func (c *streamClient) patches(n int) []patch {
	c.t.Helper()
	out := make([]patch, 0, n)
	for range n {
		out = append(out, patchOf(c.t, c.next()))
	}
	return out
}

// drain returns the patches of the change that the test just made: it waits
// for the first one for up to streamTimeout, and then until the stream is
// quiet for quietTime. The first wait is long because the event has to be read
// from the database and rendered, which under the load of the other tests, the
// race detector and a database server takes longer than any short quiet time
// (found in step 14.2 on PostgreSQL: a drain that waited only quietTime
// returned empty and the test took the late patch for no patch). A test that
// expects no patch uses drainQuiet.
func (c *streamClient) drain() []patch {
	c.t.Helper()
	var out []patch
	timer := time.NewTimer(streamTimeout)
	defer timer.Stop()
	for {
		select {
		case b, ok := <-c.blocks:
			if !ok {
				c.t.Fatal("the stream ended; wanted quiet")
			}
			if b.event != "" {
				out = append(out, patchOf(c.t, b))
				timer.Reset(quietTime)
			}
		case <-timer.C:
			return out
		}
	}
}

// drainQuiet returns the patches that come until the stream is quiet for
// quietTime. It is for the start of a stream, which may send nothing, and for
// a change after which nothing is expected: it cannot wait for an event that
// may not come, so it proves the absence of a patch only as far as quietTime
// reaches.
func (c *streamClient) drainQuiet() []patch {
	c.t.Helper()
	var out []patch
	timer := time.NewTimer(quietTime)
	defer timer.Stop()
	for {
		select {
		case b, ok := <-c.blocks:
			if !ok {
				c.t.Fatal("the stream ended; wanted quiet")
			}
			if b.event != "" {
				out = append(out, patchOf(c.t, b))
				timer.Reset(quietTime)
			}
		case <-timer.C:
			return out
		}
	}
}

// comment returns the text of the next comment block (a ping).
func (c *streamClient) comment() string {
	c.t.Helper()
	for {
		select {
		case b, ok := <-c.blocks:
			if !ok {
				c.t.Fatal("the stream ended; wanted a comment")
			}
			if b.comment != "" {
				return b.comment
			}
		case <-time.After(streamTimeout):
			c.t.Fatal("no comment came in time")
		}
	}
}

// requireEnd fails unless the stream ends in time, with only pings and the id
// before that.
func (c *streamClient) requireEnd() {
	c.t.Helper()
	for {
		select {
		case b, ok := <-c.blocks:
			if !ok {
				return
			}
			if b.event != "" {
				c.t.Fatalf("an event came; wanted the end: %q", b.data)
			}
		case <-time.After(streamTimeout):
			c.t.Fatal("the stream did not end in time")
		}
	}
}

// requireQuiet fails when an event comes within quietTime.
func (c *streamClient) requireQuiet() {
	c.t.Helper()
	deadline := time.After(quietTime)
	for {
		select {
		case b, ok := <-c.blocks:
			if !ok {
				c.t.Fatal("the stream ended; wanted quiet")
			}
			if b.event != "" {
				c.t.Fatalf("an event came; wanted quiet: %q", b.data)
			}
		case <-deadline:
			return
		}
	}
}

// requireRedirect fails unless the patch is the redirect to target.
func requireRedirect(t *testing.T, p patch, target string) {
	t.Helper()
	if !strings.Contains(p.html, redirectScript(target)) {
		t.Errorf("patch is not a redirect to %q: %+v", target, p)
	}
}

// requirePatch checks the selector and the mode of a patch and that its HTML
// has and lacks the strings.
func requirePatch(t *testing.T, p patch, selector, mode string, want, notWant []string) {
	t.Helper()
	if p.selector != selector || p.mode != mode {
		t.Errorf("patch has selector %q and mode %q, want %q and %q:\n%s", p.selector, p.mode, selector, mode, p.html)
	}
	requireContains(t, p.html, want, notWant)
}

// streamOfRoom is the path of the stream of the room, with the query.
func (e *roomsEnv) streamOfRoom(room string, query string) string {
	path := "/rooms/" + idText(e.room(room).ID) + "/stream"
	if query != "" {
		path += "?" + query
	}
	return path
}

func TestRoomStreamPatchesTheFeed(t *testing.T) {
	t.Parallel()
	// Each case acts after alice's stream on "general" is open; seeded posts
	// are written before. The posts are bob's, written by the seed or by act.
	type env = *roomsEnv
	tests := []struct {
		name string
		as   string
		// seed makes what exists before the stream opens and returns the
		// post the act is about.
		seed func(e env) app.Post
		act  func(e env, p app.Post) app.Post
		want func(t *testing.T, c *streamClient, p app.Post)
	}{
		{
			name: "a new post of someone else is put at the top, then its copies after the first are removed, for the viewer",
			as:   "alice",
			seed: func(env) app.Post { return app.Post{} },
			act:  func(e env, _ app.Post) app.Post { return e.writePost("bob", "general", "Hello from bob") },
			want: func(t *testing.T, c *streamClient, p app.Post) {
				got := c.patches(2)
				requirePatch(t, got[0], "#feed-posts", "prepend",
					[]string{`id="post-` + idText(p.ID) + `"`, "Hello from bob", "Bob"},
					[]string{`class="post-edit"`, "post-delete", "post-reply"})
				requirePatch(t, got[1], "#post-"+idText(p.ID)+" ~ #post-"+idText(p.ID), "remove", nil, nil)
			},
		},
		{
			name: "the author's other pages get the same post with their buttons",
			as:   "bob",
			seed: func(env) app.Post { return app.Post{} },
			act:  func(e env, _ app.Post) app.Post { return e.writePost("bob", "general", "Mine") },
			want: func(t *testing.T, c *streamClient, p app.Post) {
				got := c.patches(2)
				requirePatch(t, got[0], "#feed-posts", "prepend", []string{"Mine", `class="post-edit"`, "post-delete"}, nil)
			},
		},
		{
			name: "an edit replaces the content of the card only",
			as:   "alice",
			seed: func(e env) app.Post { return e.writePost("bob", "general", "Before") },
			act: func(e env, p app.Post) app.Post {
				edited, err := e.posts.Edit(e.t.Context(), e.actor(e.bob), p.ID, app.PostInput{Format: app.FormatText, Body: "After the edit"})
				if err != nil {
					e.t.Fatalf("Edit: %+v", err)
				}
				return edited
			},
			want: func(t *testing.T, c *streamClient, p app.Post) {
				got := c.patches(1)
				requirePatch(t, got[0], "", "outer",
					[]string{`<div id="post-content-` + idText(p.ID) + `"`, "After the edit", "edited"},
					[]string{"<article", "reply-slot", "Before"})
			},
		},
		{
			name: "a comment refreshes the count of the post",
			as:   "alice",
			seed: func(e env) app.Post { return e.writePost("alice", "general", "Thread") },
			act: func(e env, p app.Post) app.Post {
				e.commentOn(p, "bob", "a comment")
				return p
			},
			want: func(t *testing.T, c *streamClient, p app.Post) {
				got := c.patches(1)
				requirePatch(t, got[0], "", "outer", []string{`id="post-content-` + idText(p.ID) + `"`, "1 comment"}, nil)
			},
		},
		{
			name: "a deleted post without replies is removed",
			as:   "alice",
			seed: func(e env) app.Post { return e.writePost("bob", "general", "Gone soon") },
			act: func(e env, p app.Post) app.Post {
				if err := e.posts.Delete(e.t.Context(), e.actor(e.bob), p.ID); err != nil {
					e.t.Fatalf("Delete: %+v", err)
				}
				return p
			},
			want: func(t *testing.T, c *streamClient, p app.Post) {
				requirePatch(t, c.patches(1)[0], "#post-"+idText(p.ID), "remove", nil, nil)
			},
		},
		{
			name: "a deleted post with replies becomes the placeholder",
			as:   "alice",
			seed: func(e env) app.Post {
				p := e.writePost("bob", "general", "Has replies")
				e.commentOn(p, "alice", "a reply")
				return p
			},
			act: func(e env, p app.Post) app.Post {
				if err := e.posts.Delete(e.t.Context(), e.actor(e.bob), p.ID); err != nil {
					e.t.Fatalf("Delete: %+v", err)
				}
				return p
			},
			want: func(t *testing.T, c *streamClient, p app.Post) {
				requirePatch(t, c.patches(1)[0], "", "outer",
					[]string{`<article id="post-` + idText(p.ID) + `"`, "This post was deleted.", "post-deleted"},
					[]string{"Has replies"})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRoomsEnv(t)
			p := tt.seed(e)
			c := e.serveStreams().open(tt.as, e.streamOfRoom("general", "seen="+idText(newestOf(e, "general"))), asDatastar())
			if c.status != http.StatusOK {
				t.Fatalf("status = %d, want 200", c.status)
			}
			got := tt.act(e, p)
			tt.want(t, c, got)
		})
	}
}

// newestOf is the ID of the newest post of the room, 0 for none.
func newestOf(e *roomsEnv, room string) uint64 {
	posts := e.feed("admin", room)
	if len(posts) == 0 {
		return 0
	}
	return posts[0].ID
}

func TestRoomStreamPatchesTheRoom(t *testing.T) {
	t.Parallel()
	type env = *roomsEnv
	tests := []struct {
		name string
		as   string
		act  func(e env)
		want func(t *testing.T, got []patch)
		n    int
	}{
		{
			name: "someone joining changes the members and their count",
			as:   "alice",
			act: func(e env) {
				if err := e.rooms.Join(e.t.Context(), e.actor(e.carol), e.general.ID); err != nil {
					e.t.Fatalf("Join: %+v", err)
				}
			},
			n: 2,
			want: func(t *testing.T, got []patch) {
				requirePatch(t, got[0], "", "outer", []string{`id="members-panel"`, "@carol"}, nil)
				requirePatch(t, got[1], "", "outer", []string{`id="room-members-count"`, "4 members"}, nil)
			},
		},
		{
			name: "leaving the room changes the header, the editor and the sidebar of the one who left",
			as:   "bob",
			act: func(e env) {
				if err := e.rooms.Leave(e.t.Context(), e.actor(e.bob), e.general.ID); err != nil {
					e.t.Fatalf("Leave: %+v", err)
				}
			},
			n: 5,
			want: func(t *testing.T, got []patch) {
				requirePatch(t, got[0], "", "outer", []string{`id="members-panel"`}, []string{"@bob"})
				requirePatch(t, got[1], "", "outer", []string{`id="room-members-count"`, "2 members"}, nil)
				requirePatch(t, got[2], "", "outer", []string{`id="room-header"`, "join-form"}, []string{`id="leave-form"`})
				requirePatch(t, got[3], "", "outer", []string{`id="post-composer"`, "Join this room to post"}, []string{"post-editor"})
				requirePatch(t, got[4], "#sidebar", "inner", []string{`id="sidebar-rooms"`}, []string{`data-room="general"`})
			},
		},
		{
			name: "a rename changes the header and the sidebar",
			as:   "alice",
			act: func(e env) {
				err := e.rooms.Update(e.t.Context(), e.actor(e.admin), e.general.ID, app.RoomInput{Name: "lobby", Description: "Everything"})
				if err != nil {
					e.t.Fatalf("Update: %+v", err)
				}
			},
			n: 2,
			want: func(t *testing.T, got []patch) {
				requirePatch(t, got[0], "", "outer", []string{`id="room-header"`, "<h1>lobby</h1>"}, nil)
				requirePatch(t, got[1], "#sidebar", "inner", []string{`data-room="lobby"`}, []string{`data-room="general"`})
			},
		},
		{
			name: "being added to another room changes the sidebar",
			as:   "carol",
			act: func(e env) {
				if err := e.rooms.AddMember(e.t.Context(), e.actor(e.alice), e.secret.ID, e.carol.ID); err != nil {
					e.t.Fatalf("AddMember: %+v", err)
				}
			},
			n: 1,
			want: func(t *testing.T, got []patch) {
				requirePatch(t, got[0], "#sidebar", "inner", []string{`data-room="secret"`}, nil)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRoomsEnv(t)
			c := e.serveStreams().open(tt.as, e.streamOfRoom("general", "seen=0"), asDatastar())
			if c.status != http.StatusOK {
				t.Fatalf("status = %d, want 200", c.status)
			}
			tt.act(e)
			tt.want(t, c.patches(tt.n))
		})
	}
}

func TestRoomStreamEndsWhenTheRoomIsLost(t *testing.T) {
	t.Parallel()
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRoomsEnv(t)
			c := e.serveStreams().open(tt.as, e.streamOfRoom(tt.room, "seen=0"), asDatastar())
			tt.act(e)
			requireRedirect(t, patchOf(t, c.next()), "/")
			c.requireEnd()
		})
	}
}

func TestRoomStreamEndsWhenTheRoomIsLostOnTheUserTopic(t *testing.T) {
	t.Parallel()
	// The event on the room's topic is not the only way: the user's topic says
	// it too.
	e := newRoomsEnv(t)
	c := e.serveStreams().open("bob", e.streamOfRoom("secret", "seen=0"), asDatastar())
	if err := e.rooms.RemoveMember(e.t.Context(), e.actor(e.admin), e.secret.ID, e.bob.ID); err != nil {
		t.Fatal(err)
	}
	requireRedirect(t, patchOf(t, c.next()), "/")
	c.requireEnd()
}

func TestStreamsRefuseWhoCannotRead(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	ss := e.serveStreams()
	secretPost := e.writePost("alice", "secret", "private words")
	roomPath := e.streamOfRoom("secret", "")
	postPath := "/posts/" + idText(secretPost.ID) + "/stream"
	tests := []struct {
		name         string
		user         string
		path         string
		opts         []reqOpt
		wantStatus   int
		wantRedirect string
	}{
		{"room: a non-member of a private room, plain", "carol", roomPath, nil, http.StatusNotFound, ""},
		{"room: a non-member of a private room, datastar", "carol", roomPath, []reqOpt{asDatastar()}, http.StatusOK, "/rooms/" + idText(e.secret.ID)},
		{"room: an admin who is not a member", "admin", roomPath, nil, http.StatusNotFound, ""},
		{"room: nobody, datastar", "", roomPath, []reqOpt{asDatastar()}, http.StatusOK, "/login"},
		{"room: a room that does not exist", "alice", "/rooms/9999/stream", nil, http.StatusNotFound, ""},
		{"room: a bad id", "alice", "/rooms/x/stream", nil, http.StatusNotFound, ""},
		{"post: a non-member of a private room, plain", "carol", postPath, nil, http.StatusNotFound, ""},
		{"post: a non-member of a private room, datastar", "carol", postPath, []reqOpt{asDatastar()}, http.StatusOK, "/posts/" + idText(secretPost.ID)},
		{"post: nobody, plain", "", postPath, nil, http.StatusFound, ""},
		{"post: a post that does not exist", "alice", "/posts/9999/stream", nil, http.StatusNotFound, ""},
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
			if strings.Contains(c.body, "private words") {
				t.Errorf("the answer leaks the post:\n%s", c.body)
			}
		})
	}
}

func TestRoomStreamConnects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		query  string
		header map[string]string
		// stale makes a post after the page was made, so that the page is
		// behind.
		wantQuiet bool
		want      func(t *testing.T, got []patch)
		n         int
	}{
		{
			name: "the first connection of a page that is up to date sends nothing", query: "seen=NEWEST",
			wantQuiet: true,
		},
		{
			name: "the first connection of a page that is behind sends the feed", query: "seen=0", n: 2,
			want: func(t *testing.T, got []patch) {
				requirePatch(t, got[0], "#feed-posts", "inner", []string{"Newest post", "Older post"}, nil)
				requirePatch(t, got[1], "#feed-more", "replace", []string{`id="feed-more"`}, nil)
			},
		},
		{
			name: "the first connection of a page of older posts sends nothing", query: "seen=0&before=1",
			wantQuiet: true,
		},
		{
			name: "a reconnection sends everything the page shows", query: "seen=NEWEST",
			header: map[string]string{"Last-Event-ID": "1"}, n: 6,
			want: func(t *testing.T, got []patch) {
				requirePatch(t, got[0], "#sidebar", "inner", []string{`data-room="general"`, `aria-current="page"`}, nil)
				requirePatch(t, got[1], "", "outer", []string{`id="room-header"`, "<h1>general</h1>"}, nil)
				requirePatch(t, got[2], "", "outer", []string{`id="members-panel"`}, nil)
				requirePatch(t, got[3], "", "outer", []string{`id="post-composer"`, "post-editor-new"}, nil)
				requirePatch(t, got[4], "#feed-posts", "inner", []string{"Newest post", "Older post"}, nil)
				requirePatch(t, got[5], "#feed-more", "replace", nil, nil)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRoomsEnv(t)
			e.writePost("bob", "general", "Older post")
			newest := e.writePost("bob", "general", "Newest post")
			query := strings.ReplaceAll(tt.query, "NEWEST", idText(newest.ID))
			var opts []reqOpt
			opts = append(opts, asDatastar())
			for k, v := range tt.header {
				opts = append(opts, withHeader(k, v))
			}
			c := e.serveStreams().open("alice", e.streamOfRoom("general", query), opts...)
			if tt.wantQuiet {
				c.requireQuiet()
				return
			}
			tt.want(t, c.patches(tt.n))
		})
	}
}

func TestRoomStreamLeavesOlderPagesAlone(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	old := e.writePost("bob", "general", "Old")
	c := e.serveStreams().open("alice", e.streamOfRoom("general", "seen=0&before="+idText(old.ID+1)), asDatastar())
	e.writePost("bob", "general", "New one")
	c.requireQuiet()
}

func TestStreamKeepAlive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		path func(e *roomsEnv) string
	}{
		{"room", func(e *roomsEnv) string { return e.streamOfRoom("general", "seen=0") }},
		{"post", func(e *roomsEnv) string {
			return "/posts/" + idText(e.writePost("alice", "general", "root").ID) + "/stream?seen=0"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRoomsEnv(t)
			e.srv.pingInterval = 20 * time.Millisecond
			c := e.serveStreams().open("alice", tt.path(e), asDatastar())
			for range 2 {
				if got := c.comment(); got != "ping" {
					t.Errorf("comment = %q, want ping", got)
				}
			}
		})
	}
}

func TestStreamEndsWhenTheSessionEnds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		end  func(e *roomsEnv)
	}{
		{"the user is disabled", func(e *roomsEnv) {
			if err := e.accounts.SetDisabled(e.t.Context(), e.actor(e.admin), e.bob.ID, true); err != nil {
				e.t.Fatalf("SetDisabled: %+v", err)
			}
		}},
		{"the user logs out", func(e *roomsEnv) {
			rec := e.post("bob", "/logout", nil, true)
			if rec.Code != http.StatusOK {
				e.t.Fatalf("logout status = %d", rec.Code)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRoomsEnv(t)
			e.srv.pingInterval = 20 * time.Millisecond
			c := e.serveStreams().open("bob", e.streamOfRoom("general", "seen=0"), asDatastar())
			tt.end(e)
			requireRedirect(t, patchOf(t, c.next()), "/login")
			c.requireEnd()
		})
	}
}

func TestStreamsEndWhenTheHubCloses(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	root := e.writePost("alice", "general", "root")
	ss := e.serveStreams()
	room := ss.open("alice", e.streamOfRoom("general", "seen="+idText(root.ID)), asDatastar())
	thread := ss.open("bob", "/posts/"+idText(root.ID)+"/stream?seen="+idText(root.ID), asDatastar())
	e.hub.Close()
	room.requireEnd()
	thread.requireEnd()
	// A stream opened after that ends at once.
	ss.open("alice", e.streamOfRoom("general", "seen="+idText(root.ID)), asDatastar()).requireEnd()
}

func TestServerShutdownEndsOpenStreams(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	// The request has a context of its own: stopping the server must not be
	// what ends the stream.
	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	served := make(chan error, 1)
	go func() { served <- e.srv.serve(ctx, ln) }()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "http://"+ln.Addr().String()+e.streamOfRoom("general", "seen=0"), nil)
	if err != nil {
		t.Fatal(err)
	}
	e.cookies["alice"](req)
	req.Header.Set("Datastar-Request", "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	read := make(chan struct{})
	go func() {
		defer close(read)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
		}
	}()

	start := time.Now()
	stop()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve: %+v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not stop while a stream was open")
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("the server took %v to stop; the streams should end at once", took)
	}
	select {
	case <-read:
	case <-time.After(streamTimeout):
		t.Error("the client's stream did not end")
	}
}

// streamOfPost is the path of the stream of the thread of the post, with the
// query.
func (e *roomsEnv) streamOfPost(p app.Post, query string) string {
	path := "/posts/" + idText(p.ID) + "/stream"
	if query != "" {
		path += "?" + query
	}
	return path
}

func TestPostStreamPatchesTheThread(t *testing.T) {
	t.Parallel()
	type env = *roomsEnv
	type fixture struct{ root, first, second app.Post }
	tests := []struct {
		name string
		as   string
		// seed writes the thread before the stream opens.
		seed func(e env) fixture
		act  func(e env, f fixture) app.Post
		n    int
		want func(t *testing.T, got []patch, f fixture, made app.Post)
	}{
		{
			name: "the first comment replaces the note that there are none",
			as:   "alice",
			seed: func(e env) fixture { return fixture{root: e.writePost("alice", "general", "root")} },
			act:  func(e env, f fixture) app.Post { return e.commentOn(f.root, "bob", "my first comment") },
			n:    2,
			want: func(t *testing.T, got []patch, f fixture, made app.Post) {
				requirePatch(t, got[0], "", "outer", []string{`id="post-content-` + idText(f.root.ID) + `"`, "1 comment"}, nil)
				requirePatch(t, got[1], "", "outer",
					[]string{`<section id="comments"`, `id="post-` + idText(made.ID) + `"`, "my first comment", "depth-1", "post-reply"},
					[]string{"No comments yet"})
			},
		},
		{
			name: "a reply goes after the comment it answers, without re-rendering the thread",
			as:   "alice",
			seed: func(e env) fixture {
				f := fixture{root: e.writePost("alice", "general", "root")}
				f.first = e.commentOn(f.root, "bob", "first")
				return f
			},
			act: func(e env, f fixture) app.Post { return e.commentOn(f.first, "alice", "an answer") },
			n:   3,
			want: func(t *testing.T, got []patch, f fixture, made app.Post) {
				requirePatch(t, got[0], "", "outer", []string{`id="post-content-` + idText(f.root.ID) + `"`, "2 comments"}, nil)
				requirePatch(t, got[1], "#post-"+idText(made.ID), "remove", nil, nil)
				requirePatch(t, got[2], "#post-"+idText(f.first.ID), "after",
					[]string{`<article id="post-` + idText(made.ID) + `"`, "an answer", "depth-2"},
					[]string{`id="thread"`})
			},
		},
		{
			name: "a new top-level comment goes after the last reply of the one above it",
			as:   "alice",
			seed: func(e env) fixture {
				f := fixture{root: e.writePost("alice", "general", "root")}
				f.first = e.commentOn(f.root, "bob", "first")
				f.second = e.commentOn(f.first, "alice", "second")
				return f
			},
			act: func(e env, f fixture) app.Post { return e.commentOn(f.root, "bob", "a late comment") },
			n:   3,
			want: func(t *testing.T, got []patch, f fixture, made app.Post) {
				requirePatch(t, got[2], "#post-"+idText(f.second.ID), "after", []string{"a late comment", "depth-1"}, nil)
			},
		},
		{
			name: "an edited comment is replaced by its content only",
			as:   "alice",
			seed: func(e env) fixture {
				f := fixture{root: e.writePost("alice", "general", "root")}
				f.first = e.commentOn(f.root, "bob", "first")
				return f
			},
			act: func(e env, f fixture) app.Post {
				edited, err := e.posts.Edit(e.t.Context(), e.actor(e.bob), f.first.ID, app.PostInput{Format: app.FormatText, Body: "first, edited"})
				if err != nil {
					e.t.Fatalf("Edit: %+v", err)
				}
				return edited
			},
			n: 1,
			want: func(t *testing.T, got []patch, f fixture, made app.Post) {
				requirePatch(t, got[0], "", "outer",
					[]string{`<div id="post-content-` + idText(f.first.ID) + `"`, "first, edited", "edited"},
					[]string{"<article", "reply-slot"})
			},
		},
		{
			name: "a deleted comment re-renders the thread",
			as:   "alice",
			seed: func(e env) fixture {
				f := fixture{root: e.writePost("alice", "general", "root")}
				f.first = e.commentOn(f.root, "bob", "first")
				f.second = e.commentOn(f.root, "bob", "second")
				return f
			},
			act: func(e env, f fixture) app.Post {
				if err := e.posts.Delete(e.t.Context(), e.actor(e.bob), f.first.ID); err != nil {
					e.t.Fatalf("Delete: %+v", err)
				}
				return f.first
			},
			n: 1,
			want: func(t *testing.T, got []patch, f fixture, made app.Post) {
				requirePatch(t, got[0], "", "outer",
					[]string{`<div id="thread"`, "second", `id="post-` + idText(f.second.ID) + `"`},
					[]string{"first"})
			},
		},
		{
			name: "an edit of the post replaces its content",
			as:   "bob",
			seed: func(e env) fixture { return fixture{root: e.writePost("alice", "general", "root")} },
			act: func(e env, f fixture) app.Post {
				edited, err := e.posts.Edit(e.t.Context(), e.actor(e.alice), f.root.ID, app.PostInput{Format: app.FormatText, Body: "root, edited"})
				if err != nil {
					e.t.Fatalf("Edit: %+v", err)
				}
				return edited
			},
			n: 1,
			want: func(t *testing.T, got []patch, f fixture, made app.Post) {
				requirePatch(t, got[0], "", "outer",
					[]string{`<div id="post-content-` + idText(f.root.ID) + `"`, "root, edited", "post-reply"},
					[]string{"<article", `class="post-edit"`})
			},
		},
		{
			name: "a post deleted with its comments becomes the placeholder",
			as:   "bob",
			seed: func(e env) fixture {
				f := fixture{root: e.writePost("alice", "general", "root")}
				f.first = e.commentOn(f.root, "bob", "first")
				return f
			},
			act: func(e env, f fixture) app.Post {
				if err := e.posts.Delete(e.t.Context(), e.actor(e.alice), f.root.ID); err != nil {
					e.t.Fatalf("Delete: %+v", err)
				}
				return f.root
			},
			n: 1,
			want: func(t *testing.T, got []patch, f fixture, made app.Post) {
				requirePatch(t, got[0], "", "outer",
					[]string{`<article id="post-` + idText(f.root.ID) + `"`, "This post was deleted."}, []string{`class="post-edit"`})
			},
		},
		{
			name: "a post deleted for good sends the page to the room",
			as:   "bob",
			seed: func(e env) fixture { return fixture{root: e.writePost("alice", "general", "root")} },
			act: func(e env, f fixture) app.Post {
				if err := e.posts.Delete(e.t.Context(), e.actor(e.alice), f.root.ID); err != nil {
					e.t.Fatalf("Delete: %+v", err)
				}
				return f.root
			},
			n: 1,
			want: func(t *testing.T, got []patch, f fixture, made app.Post) {
				requireRedirect(t, got[0], "/rooms/"+idText(f.root.RoomID))
			},
		},
		{
			name: "a viewer who has not joined gets cards without Reply",
			as:   "carol",
			seed: func(e env) fixture { return fixture{root: e.writePost("alice", "general", "root")} },
			act:  func(e env, f fixture) app.Post { return e.commentOn(f.root, "bob", "hello carol") },
			n:    2,
			want: func(t *testing.T, got []patch, f fixture, made app.Post) {
				requirePatch(t, got[1], "", "outer", []string{"hello carol"}, []string{"post-reply", "reply-slot"})
			},
		},
		{
			name: "joining another room changes the sidebar",
			as:   "carol",
			seed: func(e env) fixture { return fixture{root: e.writePost("alice", "general", "root")} },
			act: func(e env, f fixture) app.Post {
				if err := e.rooms.Join(e.t.Context(), e.actor(e.carol), e.random.ID); err != nil {
					e.t.Fatalf("Join: %+v", err)
				}
				return f.root
			},
			n: 1,
			want: func(t *testing.T, got []patch, f fixture, made app.Post) {
				requirePatch(t, got[0], "#sidebar", "inner", []string{`data-room="random"`}, nil)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRoomsEnv(t)
			f := tt.seed(e)
			newest := f.root.ID
			for _, c := range []app.Post{f.first, f.second} {
				newest = max(newest, c.ID)
			}
			c := e.serveStreams().open(tt.as, e.streamOfPost(f.root, "seen="+idText(newest)), asDatastar())
			if c.status != http.StatusOK {
				t.Fatalf("status = %d, want 200", c.status)
			}
			made := tt.act(e, f)
			tt.want(t, c.patches(tt.n), f, made)
		})
	}
}

func TestPostStreamTwoCommentsInARowAreBothShown(t *testing.T) {
	t.Parallel()
	// The second comment may already be in the database when the event of the
	// first is handled: whichever happens, each comment is inserted once, after
	// the card above it, and the order of the thread is kept.
	e := newRoomsEnv(t)
	root := e.writePost("alice", "general", "root")
	first := e.commentOn(root, "bob", "first")
	c := e.serveStreams().open("alice", e.streamOfPost(root, "seen="+idText(first.ID)), asDatastar())
	second := e.commentOn(first, "bob", "second")
	third := e.commentOn(root, "bob", "third")

	inserted := map[string]int{}
	for _, p := range c.drain() {
		if p.mode == "after" {
			inserted[p.selector]++
			switch p.selector {
			case "#post-" + idText(first.ID):
				requireContains(t, p.html, []string{"second", "depth-2"}, []string{"third"})
			case "#post-" + idText(second.ID):
				requireContains(t, p.html, []string{"third", "depth-1"}, []string{"second"})
			default:
				t.Errorf("a card was inserted after %q", p.selector)
			}
		}
	}
	for _, sel := range []string{"#post-" + idText(first.ID), "#post-" + idText(second.ID)} {
		if inserted[sel] != 1 {
			t.Errorf("%d cards inserted after %s, want 1", inserted[sel], sel)
		}
	}
	_ = third
}

func TestPostStreamEndsWhenTheRoomIsLost(t *testing.T) {
	t.Parallel()
	type env = *roomsEnv
	tests := []struct {
		name string
		as   string
		act  func(e env)
	}{
		{"removed from a private room", "bob", func(e env) {
			if err := e.rooms.RemoveMember(e.t.Context(), e.actor(e.admin), e.secret.ID, e.bob.ID); err != nil {
				e.t.Fatalf("RemoveMember: %+v", err)
			}
		}},
		{"the room is deleted", "bob", func(e env) {
			if err := e.rooms.Delete(e.t.Context(), e.actor(e.admin), e.secret.ID); err != nil {
				e.t.Fatalf("Delete: %+v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRoomsEnv(t)
			root := e.writePost("alice", "secret", "private")
			c := e.serveStreams().open(tt.as, e.streamOfPost(root, "seen="+idText(root.ID)), asDatastar())
			tt.act(e)
			requireRedirect(t, patchOf(t, c.next()), "/")
			c.requireEnd()
		})
	}
}

func TestPostStreamIgnoresTheFeedOfTheRoom(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	root := e.writePost("alice", "general", "root")
	c := e.serveStreams().open("bob", e.streamOfPost(root, "seen="+idText(root.ID)), asDatastar())
	e.writePost("alice", "general", "another post")
	if err := e.rooms.Join(e.t.Context(), e.actor(e.carol), e.general.ID); err != nil {
		t.Fatal(err)
	}
	c.requireQuiet()
}

func TestPostStreamConnects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		seen      string
		header    map[string]string
		wantQuiet bool
		n         int
		want      func(t *testing.T, got []patch)
	}{
		{name: "the first connection of a page that is up to date sends nothing", seen: "NEWEST", wantQuiet: true},
		{
			name: "the first connection of a page that is behind sends the thread", seen: "ROOT", n: 1,
			want: func(t *testing.T, got []patch) {
				requirePatch(t, got[0], "", "outer", []string{`<div id="thread"`, "a comment"}, nil)
			},
		},
		{
			name: "a reconnection sends the thread and the sidebar", seen: "NEWEST", header: map[string]string{"Last-Event-ID": "1"}, n: 2,
			want: func(t *testing.T, got []patch) {
				requirePatch(t, got[0], "", "outer", []string{`<div id="thread"`, "a comment"}, nil)
				requirePatch(t, got[1], "#sidebar", "inner", []string{`data-room="general"`}, nil)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newRoomsEnv(t)
			root := e.writePost("alice", "general", "root")
			comment := e.commentOn(root, "bob", "a comment")
			seen := strings.NewReplacer("NEWEST", idText(comment.ID), "ROOT", idText(root.ID)).Replace(tt.seen)
			opts := []reqOpt{asDatastar()}
			for k, v := range tt.header {
				opts = append(opts, withHeader(k, v))
			}
			c := e.serveStreams().open("alice", e.streamOfPost(root, "seen="+seen), opts...)
			if tt.wantQuiet {
				c.requireQuiet()
				return
			}
			tt.want(t, c.patches(tt.n))
		})
	}
}

func TestPostStreamOpensForAComment(t *testing.T) {
	t.Parallel()
	// The ID of a comment opens the stream of its thread.
	e := newRoomsEnv(t)
	root := e.writePost("alice", "general", "root")
	comment := e.commentOn(root, "bob", "a comment")
	c := e.serveStreams().open("alice", e.streamOfPost(comment, "seen="+idText(comment.ID)), asDatastar())
	if c.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", c.status)
	}
	e.commentOn(comment, "alice", "an answer")
	got := c.patches(3)
	requirePatch(t, got[0], "", "outer", []string{`id="post-content-` + idText(root.ID) + `"`}, nil)
	requirePatch(t, got[2], "#post-"+idText(comment.ID), "after", []string{"an answer"}, nil)
}

// TestServerShutdownClosesWebSockets checks that stopping the server closes an
// open WebSocket of the API with the close code 1001 and that serve returns
// only after that: http.Server.Shutdown does not wait for upgraded connections.
// The client is a raw connection (the WebSocket library is the API package's).
func TestServerShutdownClosesWebSockets(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	login, err := e.accounts.LoginClient(t.Context(), app.ClientLogin{
		Username: "alice", Password: testPassword, ClientIP: "192.0.2.77", Kind: app.SessionAPI, ClientName: "shutdown test",
	})
	if err != nil {
		t.Fatalf("LoginClient: %+v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	served := make(chan error, 1)
	go func() { served <- e.srv.serve(ctx, ln) }()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, err = fmt.Fprintf(conn, "GET /api/v1/ws HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nAuthorization: Bearer %s\r\n\r\n",
		ln.Addr().String(), login.Token)
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}

	stop()
	// The close frame: FIN and the opcode 8, a length, the code 1001 and the
	// reason, not masked (it comes from the server).
	header := make([]byte, 4)
	if _, err := io.ReadFull(br, header); err != nil {
		t.Fatalf("reading the close frame: %v", err)
	}
	if header[0] != 0x88 || header[2] != 0x03 || header[3] != 0xE9 {
		t.Errorf("the server sent %x, want a close frame with the code 1001", header)
	}
	// The server waits for the client's answer to the close before it ends.
	select {
	case <-served:
		t.Fatal("serve returned before the WebSocket had ended")
	case <-time.After(300 * time.Millisecond):
	}
	// The client answers the close, as the protocol asks, and the server ends.
	if _, err := conn.Write([]byte{0x88, 0x82, 0, 0, 0, 0, 0x03, 0xE9}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve: %+v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not stop")
	}
	if !e.srv.svc.API.Drain(t.Context()) {
		t.Error("a WebSocket was still open after the server stopped")
	}
}
