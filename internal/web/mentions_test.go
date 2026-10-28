package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
)

// mentionEnv is the rooms fixture with seven members in general (admin,
// alice, bob, carol, dave, erin and frank), so that an @room notifies six
// people, and a group conversation "wide" of the same seven.
type mentionEnv struct {
	*roomsEnv
	wide  uint64
	extra map[string]app.User
}

// user returns the user of the name, among all seven.
func (e *mentionEnv) user(name string) app.User {
	if u, ok := e.extra[name]; ok {
		return u
	}
	return e.users(name)
}

func newMentionEnv(t *testing.T) *mentionEnv {
	t.Helper()
	e := &mentionEnv{roomsEnv: newRoomsEnv(t), extra: map[string]app.User{}}
	ctx := t.Context()
	if err := e.rooms.Join(ctx, e.actor(e.carol), e.general.ID); err != nil {
		t.Fatalf("Join: %+v", err)
	}
	var others []uint64
	for _, name := range []string{"dave", "erin", "frank"} {
		u := e.addUser(name, strings.ToUpper(name[:1])+name[1:], app.RoleMember)
		if err := e.rooms.Join(ctx, e.actor(u), e.general.ID); err != nil {
			t.Fatalf("Join: %+v", err)
		}
		others = append(others, u.ID)
		e.extra[name] = u
	}
	var err error
	if e.wide, err = e.msgs.StartGroup(ctx, e.actor(e.alice), append(others, e.bob.ID, e.carol.ID, e.admin.ID), "Wide team"); err != nil {
		t.Fatalf("StartGroup: %+v", err)
	}
	return e
}

// feedLen is the number of top-level posts in the room.
func (e *mentionEnv) feedLen(room string) int {
	e.t.Helper()
	page, err := e.posts.Feed(e.t.Context(), e.actor(e.alice), e.room(room).ID, 0, 50)
	if err != nil {
		e.t.Fatalf("Feed: %+v", err)
	}
	return len(page.Posts)
}

func TestGroupMentionAsksAboveFivePeople(t *testing.T) {
	t.Parallel()
	const question = "This will notify 6 people. Send anyway?"
	tests := []struct {
		name string
		body string
		// confirm is the flag of the form.
		confirm bool
		// asks says whether the page asks and writes nothing.
		asks bool
	}{
		{"@room above five asks", "Hello @room", false, true},
		{"@all above five asks", "Hello @all", false, true},
		{"the confirmed form writes", "Hello @room", true, false},
		{"no group mention never asks", "Hello @bob", false, false},
		{"a mention in code never asks", "Hello `@room`", false, false},
	}
	for _, tt := range tests {
		for _, datastar := range []bool{true, false} {
			name := tt.name + " (plain)"
			var opts []reqOpt
			if datastar {
				name, opts = tt.name+" (datastar)", []reqOpt{asDatastar()}
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				e := newMentionEnv(t)
				form := url.Values{"body": {tt.body}, "format": {"markdown"}, "tags": {""}}
				target := "/rooms/" + idText(e.general.ID) + "/posts"
				if tt.confirm {
					target += "?confirm_group_mention=1"
				}
				rec := e.send(postForm(target, form, append([]reqOpt{e.cookies["alice"]}, opts...)...))
				wrote := e.feedLen("general")
				if tt.asks {
					if wrote != 0 {
						t.Fatalf("the page asked and wrote %d posts", wrote)
					}
					if rec.Code != http.StatusOK {
						t.Fatalf("status = %d, want 200:\n%s", rec.Code, rec.Body)
					}
					want := []string{question, `id="group-mention-confirm-new"`, `formaction="/rooms/` + idText(e.general.ID) + `/posts?confirm_group_mention=1"`, ">Send<", ">Cancel<"}
					if !datastar {
						want = append(want, "<title>general · Clowder</title>", tt.body)
					}
					requireBody(t, rec, want)
					return
				}
				if wrote != 1 {
					t.Fatalf("the page wrote %d posts, want 1 (status %d):\n%s", wrote, rec.Code, rec.Body)
				}
				if strings.Contains(rec.Body.String(), "Send anyway") {
					t.Errorf("a written post asked a question:\n%s", rec.Body)
				}
			})
		}
	}
}

func TestGroupMentionOfFiveOrFewerNeverAsks(t *testing.T) {
	t.Parallel()
	e := newMentionEnv(t)
	// random: admin, alice and bob: an @room notifies two people.
	for _, name := range []string{"alice", "bob"} {
		if err := e.rooms.Join(t.Context(), e.actor(e.users(name)), e.random.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, user := range []string{"alice"} {
		rec := e.send(postForm("/rooms/"+idText(e.random.ID)+"/posts",
			url.Values{"body": {"Hello @room"}, "format": {"markdown"}}, e.cookies[user]))
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303:\n%s", rec.Code, rec.Body)
		}
	}
	// Exactly five people: general without frank, erin and dave leaving is not
	// needed, make a room of six members.
	six, err := e.rooms.Create(t.Context(), e.actor(e.admin), app.RoomInput{Name: "six", Visibility: app.VisibilityVisible})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "bob", "carol", "dave", "erin"} {
		if err := e.rooms.Join(t.Context(), e.actor(e.user(name)), six.ID); err != nil {
			t.Fatalf("Join: %+v", err)
		}
	}
	rec := e.send(postForm("/rooms/"+idText(six.ID)+"/posts", url.Values{"body": {"Hello @room"}, "format": {"markdown"}}, e.cookies["alice"]))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("five people: status = %d, want 303 (no question):\n%s", rec.Code, rec.Body)
	}
}

func TestGroupMentionHereCountsTheOnlineMembers(t *testing.T) {
	t.Parallel()
	e := newMentionEnv(t)
	form := url.Values{"body": {"Anyone @here?"}, "format": {"markdown"}}
	target := "/rooms/" + idText(e.general.ID) + "/posts"
	// Nobody else is online: no question.
	if rec := e.send(postForm(target, form, e.cookies["alice"])); rec.Code != http.StatusSeeOther {
		t.Fatalf("nobody online: status = %d, want 303:\n%s", rec.Code, rec.Body)
	}
	for _, name := range []string{"admin", "bob", "carol", "dave", "erin", "frank"} {
		e.presence.Connect(e.user(name).ID)
	}
	rec := e.send(postForm(target, form, e.cookies["alice"]))
	requireBody(t, rec, []string{"This will notify 6 people. Send anyway?"})
	if n := e.feedLen("general"); n != 1 {
		t.Errorf("%d posts, want only the first", n)
	}
}

func TestGroupMentionAsksOnAComment(t *testing.T) {
	t.Parallel()
	e := newMentionEnv(t)
	post := e.writePost("bob", "general", "A question")
	target := "/posts/" + idText(post.ID) + "/replies"
	form := url.Values{"body": {"Hello @room"}, "format": {"markdown"}}
	count := func() int {
		th, err := e.posts.Thread(t.Context(), e.actor(e.alice), post.ID)
		if err != nil {
			t.Fatal(err)
		}
		return len(th.Comments)
	}
	for _, opts := range [][]reqOpt{{asDatastar()}, {}} {
		rec := e.send(postForm(target, form, append([]reqOpt{e.cookies["alice"]}, opts...)...))
		requireBody(t, rec, []string{"This will notify 6 people. Send anyway?", `id="group-mention-confirm-reply-` + idText(post.ID) + `"`})
		if n := count(); n != 0 {
			t.Fatalf("the page asked and wrote %d comments", n)
		}
	}
	rec := e.send(postForm(target+"?confirm_group_mention=1", form, e.cookies["alice"]))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("confirmed: status = %d, want 303:\n%s", rec.Code, rec.Body)
	}
	if n := count(); n != 1 {
		t.Errorf("%d comments, want 1", n)
	}
}

func TestGroupMentionAsksInAConversation(t *testing.T) {
	t.Parallel()
	e := newMentionEnv(t)
	target := "/messages/" + idText(e.wide)
	form := url.Values{"body": {"Hello @all"}}
	count := func() int {
		page, err := e.msgs.History(t.Context(), e.actor(e.alice), e.wide, 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		return len(page.Messages)
	}
	for _, opts := range [][]reqOpt{{asDatastar()}, {}} {
		rec := e.send(postForm(target, form, append([]reqOpt{e.cookies["alice"]}, opts...)...))
		requireBody(t, rec, []string{"This will notify 6 people. Send anyway?", `id="group-mention-confirm-composer"`})
		if n := count(); n != 0 {
			t.Fatalf("the page asked and sent %d messages", n)
		}
	}
	rec := e.send(postForm(target+"?confirm_group_mention=1", form, e.cookies["alice"]))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("confirmed: status = %d, want 303:\n%s", rec.Code, rec.Body)
	}
	if n := count(); n != 1 {
		t.Errorf("%d messages, want 1", n)
	}
	// A direct conversation has no group mentions: nothing is asked.
	pair, err := e.msgs.StartDirect(t.Context(), e.actor(e.alice), e.bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec = e.send(postForm("/messages/"+idText(pair), url.Values{"body": {"@room hi"}}, e.cookies["alice"]))
	if rec.Code != http.StatusSeeOther {
		t.Errorf("direct: status = %d, want 303:\n%s", rec.Code, rec.Body)
	}
}

func TestEditingNeverAsks(t *testing.T) {
	t.Parallel()
	e := newMentionEnv(t)
	post := e.writePost("alice", "general", "First")
	rec := e.send(postForm("/posts/"+idText(post.ID)+"/edit",
		url.Values{"body": {"Edited for @room"}, "format": {"markdown"}}, e.cookies["alice"]))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (editing does not ask):\n%s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "Send anyway") {
		t.Error("an edit asked a question")
	}
}

func TestMembersListShowsWhoIsOnline(t *testing.T) {
	t.Parallel()
	e := newMentionEnv(t)
	e.presence.Connect(e.bob.ID)
	rec := e.send(get("/rooms/"+idText(e.general.ID), e.cookies["alice"]))
	body := rec.Body.String()
	dot := `<span class="online-dot" role="img" aria-label="Online" title="Online"></span>`
	// Bob (connected) and alice (the viewer) have the dot; carol does not.
	for name, want := range map[string]bool{"bob": true, "alice": true, "carol": false} {
		li := itemOf(t, body, `id="member-`+idText(e.user(name).ID)+`"`)
		if strings.Contains(li, dot) != want {
			t.Errorf("%s: dot = %v, want %v:\n%s", name, !want, want, li)
		}
	}
	// The group conversation shows the same.
	rec = e.send(get("/messages/"+idText(e.wide), e.cookies["alice"]))
	body = rec.Body.String()
	for name, want := range map[string]bool{"bob": true, "dave": false} {
		li := itemOf(t, body, `id="conversation-member-`+idText(e.user(name).ID)+`"`)
		if strings.Contains(li, dot) != want {
			t.Errorf("conversation, %s: dot = %v, want %v:\n%s", name, !want, want, li)
		}
	}
}

// listItem returns the <li> of the page whose opening tag holds marker.
func itemOf(t *testing.T, body, marker string) string {
	t.Helper()
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("the page has no %s", marker)
	}
	j := strings.Index(body[i:], "</li>")
	if j < 0 {
		t.Fatalf("the item %s is not closed", marker)
	}
	return body[i : i+j]
}

func TestOpenStreamMakesTheUserOnline(t *testing.T) {
	t.Parallel()
	e := newRoomsEnv(t)
	online := func() bool {
		got, err := e.presence.Online(t.Context(), []uint64{e.bob.ID})
		if err != nil {
			t.Fatal(err)
		}
		return got[e.bob.ID]
	}
	// waitFor polls until online() is want: the stream connects and
	// disconnects in the goroutine of its handler.
	waitFor := func(want bool, what string) {
		t.Helper()
		for range 500 {
			if online() == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("bob online = %v, want %v %s", online(), want, what)
	}
	waitFor(false, "before any stream")
	c := e.serveStreams().open("bob", "/me/stream", asDatastar())
	requireCounts(t, c, 1)
	waitFor(true, "while his stream is open")
	c.cancel()
	waitFor(false, "after his stream closed")
}
