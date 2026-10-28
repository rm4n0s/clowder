package app_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/clowder/internal/store"
)

// notifEnv is the boardEnv (lobby visible with the members admin and bob; den
// private with alice and bob; a Recorder behind every service) with the
// notifications service. The notifications and events of the fixture are
// forgotten, so a test sees only its own.
type notifEnv struct {
	*boardEnv
	notes *app.Notifications
	// watermark is the largest notification ID that settle saw.
	watermark uint64
}

func newNotifEnv(t *testing.T) *notifEnv {
	t.Helper()
	e := &notifEnv{boardEnv: newBoardEnv(t)}
	e.notes = app.NewNotifications(e.st, e.rec)
	// These tests are about the rules of 1.0: the notifications of a comment
	// under your post or comment, and the mentions. They run on "Only direct
	// replies"; the follows of *Threads I follow* have their own tests
	// (follow_test.go).
	direct := app.RepliesDirect
	for name := range e.users {
		if _, err := e.svc.UpdateSettings(t.Context(), e.as(name), app.SettingsUpdate{Replies: &direct}); err != nil {
			t.Fatalf("UpdateSettings %s: %+v", name, err)
		}
	}
	// What the fixture made is read: the unread counts start at 0.
	for name := range e.users {
		if err := e.notes.MarkRead(t.Context(), e.as(name), 1<<40); err != nil {
			t.Fatalf("MarkRead %s: %+v", name, err)
		}
	}
	e.settle()
	return e
}

// settle forgets the notifications made so far and the published events.
func (e *notifEnv) settle() {
	e.t.Helper()
	e.watermark = 0
	for name := range e.users {
		for _, n := range e.list(name, 0, 50).Notifications {
			e.watermark = max(e.watermark, n.ID)
		}
	}
	e.rec.Reset()
}

// list returns a page of the notifications of the user.
func (e *notifEnv) list(user string, before uint64, limit int) app.NotificationPage {
	e.t.Helper()
	page, err := e.notes.List(e.t.Context(), e.as(user), before, limit)
	if err != nil {
		e.t.Fatalf("List %s: %+v", user, err)
	}
	return page
}

// news returns the notifications of the user made since the last settle,
// newest first.
func (e *notifEnv) news(user string) []app.Notification {
	e.t.Helper()
	var out []app.Notification
	for _, n := range e.list(user, 0, 50).Notifications {
		if n.ID > e.watermark {
			out = append(out, n)
		}
	}
	return out
}

// kinds returns the kinds of the news of the user.
func (e *notifEnv) kinds(user string) []app.NotificationKind {
	var out []app.NotificationKind
	for _, n := range e.news(user) {
		out = append(out, n.Kind)
	}
	return out
}

// requireNews fails the test unless the news of every user are exactly the
// kinds that want lists for the user (newest first); a user who is not in
// want has none.
func (e *notifEnv) requireNews(want map[string][]app.NotificationKind) {
	e.t.Helper()
	for name := range e.users {
		got := e.kinds(name)
		if len(got) == 0 && len(want[name]) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want[name]) {
			e.t.Errorf("notifications of %s = %v, want %v", name, got, want[name])
		}
	}
}

// join makes the user a member of the lobby and settles.
func (e *notifEnv) join(users ...string) {
	e.t.Helper()
	for _, u := range users {
		if err := e.rooms.Join(e.t.Context(), e.as(u), e.lobby.ID); err != nil {
			e.t.Fatalf("Join %s: %+v", u, err)
		}
	}
	e.settle()
}

// intoDen has alice add the users to the den and settles.
func (e *notifEnv) intoDen(users ...string) {
	e.t.Helper()
	for _, u := range users {
		if err := e.rooms.AddMember(e.t.Context(), e.as("alice"), e.den.ID, e.uid(u)); err != nil {
			e.t.Fatalf("AddMember %s: %+v", u, err)
		}
	}
	e.settle()
}

// disable disables the user.
func (e *notifEnv) disable(user string) {
	e.t.Helper()
	if err := e.svc.SetDisabled(e.t.Context(), e.as("admin"), e.uid(user), true); err != nil {
		e.t.Fatalf("SetDisabled %s: %+v", user, err)
	}
}

// create has the user write a post in the room.
func (e *notifEnv) create(user, room string, format app.Format, body string, tasks ...app.TaskInput) (app.Post, error) {
	e.t.Helper()
	return e.posts.Create(e.t.Context(), e.as(user), e.room(room).ID,
		app.PostInput{Format: format, Body: body, Tasks: tasks})
}

// mustCreate is create that fails the test on an error.
func (e *notifEnv) mustCreate(user, room string, format app.Format, body string, tasks ...app.TaskInput) app.Post {
	e.t.Helper()
	p, err := e.create(user, room, format, body, tasks...)
	if err != nil {
		e.t.Fatalf("Create %q: %+v", body, err)
	}
	return p
}

// mustEdit has the user edit the post to this body and these tasks.
func (e *notifEnv) mustEdit(user string, p app.Post, body string, tasks ...app.TaskInput) app.Post {
	e.t.Helper()
	got, err := e.posts.Edit(e.t.Context(), e.as(user), p.ID,
		app.PostInput{Format: p.Format, Body: body, Tasks: tasks})
	if err != nil {
		e.t.Fatalf("Edit %q: %+v", body, err)
	}
	return got
}

// mentionsOf returns the usernames the post has saved as mentions.
func (e *notifEnv) mentionsOf(postID uint64) []string {
	e.t.Helper()
	m, err := e.st.MentionedUsernamesOfPosts(e.t.Context(), []uint64{postID})
	if err != nil {
		e.t.Fatalf("MentionedUsernamesOfPosts: %+v", err)
	}
	return m[postID]
}

// assign is a task for the users.
func (e *notifEnv) assign(title string, users ...string) app.TaskInput {
	return app.TaskInput{Title: title, Assignees: e.ids(users...)}
}

// withKeep is a task of the post that stays as it is.
func withKeep(t app.TaskCard, assignees []uint64) app.TaskInput {
	return app.TaskInput{ID: t.ID, Title: t.Title, Assignees: assignees}
}

func TestNotificationKindsMatchTheStore(t *testing.T) {
	t.Parallel()
	pairs := [][2]app.NotificationKind{
		{app.NotificationMention, store.NotificationMention},
		{app.NotificationReply, store.NotificationReply},
		{app.NotificationAssigned, store.NotificationAssigned},
		{app.NotificationAddedToRoom, store.NotificationAddedToRoom},
	}
	for _, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("app kind %q differs from the store's %q", p[0], p[1])
		}
	}
}

func TestMentionNotifications(t *testing.T) {
	t.Parallel()
	mention := []app.NotificationKind{app.NotificationMention}
	tests := []struct {
		name string
		// run does the action and returns the ID of the post or comment whose
		// saved mentions the case checks.
		run  func(e *notifEnv) uint64
		want map[string][]app.NotificationKind
		// mentions are the usernames that post saves.
		mentions []string
	}{
		{
			name:     "a post in a visible room mentions a member",
			run:      func(e *notifEnv) uint64 { return e.mustCreate("admin", "lobby", app.FormatText, "hello @bob").ID },
			want:     map[string][]app.NotificationKind{"bob": mention},
			mentions: []string{"bob"},
		},
		{
			name:     "a visible room: a user who has not joined is mentioned too (D26)",
			run:      func(e *notifEnv) uint64 { return e.mustCreate("bob", "lobby", app.FormatText, "hello @carol").ID },
			want:     map[string][]app.NotificationKind{"carol": mention},
			mentions: []string{"carol"},
		},
		{
			name: "a markdown post",
			run: func(e *notifEnv) uint64 {
				return e.mustCreate("bob", "lobby", app.FormatMarkdown, "**hello** @carol").ID
			},
			want:     map[string][]app.NotificationKind{"carol": mention},
			mentions: []string{"carol"},
		},
		{
			name:     "the case of the name does not matter",
			run:      func(e *notifEnv) uint64 { return e.mustCreate("bob", "lobby", app.FormatText, "hello @CaRoL").ID },
			want:     map[string][]app.NotificationKind{"carol": mention},
			mentions: []string{"carol"},
		},
		{
			name: "several people, each once however often they are named",
			run: func(e *notifEnv) uint64 {
				return e.mustCreate("bob", "lobby", app.FormatText, "@carol @dave @carol and again @carol").ID
			},
			want:     map[string][]app.NotificationKind{"carol": mention, "dave": mention},
			mentions: []string{"carol", "dave"},
		},
		{
			name: "a comment mentions a user",
			run: func(e *notifEnv) uint64 {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post")
				e.settle()
				return e.say("admin", p.ID, "see @carol").ID
			},
			// bob is told of the reply, carol of the mention.
			want:     map[string][]app.NotificationKind{"bob": {app.NotificationReply}, "carol": mention},
			mentions: []string{"carol"},
		},
		{
			name: "a mention in a code span is none",
			run: func(e *notifEnv) uint64 {
				return e.mustCreate("bob", "lobby", app.FormatMarkdown, "run `@carol` now").ID
			},
			want: nil,
		},
		{
			name: "a mention in a code block is none",
			run: func(e *notifEnv) uint64 {
				return e.mustCreate("bob", "lobby", app.FormatMarkdown, "```\n@carol\n```").ID
			},
			want: nil,
		},
		{
			name: "an unknown name is none",
			run:  func(e *notifEnv) uint64 { return e.mustCreate("bob", "lobby", app.FormatText, "hello @nobody").ID },
			want: nil,
		},
		{
			name:     "a mention of oneself is saved and tells nobody",
			run:      func(e *notifEnv) uint64 { return e.mustCreate("bob", "lobby", app.FormatText, "note to @bob").ID },
			want:     nil,
			mentions: []string{"bob"},
		},
		{
			name: "an e-mail address is no mention",
			run: func(e *notifEnv) uint64 {
				return e.mustCreate("bob", "lobby", app.FormatText, "write to a@carol.example.com").ID
			},
			want: nil,
		},
		{
			name:     "a private room: a member is mentioned",
			run:      func(e *notifEnv) uint64 { return e.mustCreate("alice", "den", app.FormatText, "psst @bob").ID },
			want:     map[string][]app.NotificationKind{"bob": mention},
			mentions: []string{"bob"},
		},
		{
			name: "a private room: somebody who cannot read it is no mention",
			run:  func(e *notifEnv) uint64 { return e.mustCreate("alice", "den", app.FormatText, "psst @carol").ID },
			want: nil,
		},
		{
			name: "a private room: an admin who is not a member is no mention (D29)",
			run:  func(e *notifEnv) uint64 { return e.mustCreate("alice", "den", app.FormatText, "psst @admin").ID },
			want: nil,
		},
		{
			name:     "a private room: one who can and one who cannot",
			run:      func(e *notifEnv) uint64 { return e.mustCreate("alice", "den", app.FormatText, "@carol and @bob").ID },
			want:     map[string][]app.NotificationKind{"bob": mention},
			mentions: []string{"bob"},
		},
		{
			name: "a disabled user is no mention",
			run: func(e *notifEnv) uint64 {
				e.disable("carol")
				return e.mustCreate("bob", "lobby", app.FormatText, "hello @carol").ID
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNotifEnv(t)
			id := tt.run(e)
			e.requireNews(tt.want)
			if got := e.mentionsOf(id); !slices.Equal(got, tt.mentions) && (len(got) != 0 || len(tt.mentions) != 0) {
				t.Errorf("saved mentions = %v, want %v", got, tt.mentions)
			}
		})
	}
}

func TestMentionNotificationDetails(t *testing.T) {
	t.Parallel()
	e := newNotifEnv(t)
	e.join("alice", "carol")
	p := e.mustCreate("bob", "lobby", app.FormatText, "hi @carol")
	c := e.say("admin", p.ID, "reply to the post")
	c2 := e.say("alice", c.ID, "deeper @carol")
	got := e.news("carol")
	if len(got) != 2 {
		t.Fatalf("carol has %d notifications, want 2: %+v", len(got), got)
	}
	deep, post := got[0], got[1]
	if deep.Kind != app.NotificationMention || deep.PostID != c2.ID || deep.RootID != p.ID {
		t.Errorf("deep = %+v, want a mention of comment %d in thread %d", deep, c2.ID, p.ID)
	}
	if deep.Actor.Username != "alice" || deep.Actor.DisplayName != "Alice" || deep.Actor.ID != e.uid("alice") {
		t.Errorf("actor = %+v", deep.Actor)
	}
	if deep.RoomID != e.lobby.ID || deep.RoomName != "lobby" || deep.Read || deep.ReadAt != nil || deep.TaskID != 0 || deep.TaskTitle != "" {
		t.Errorf("deep = %+v", deep)
	}
	if !deep.CreatedAt.Equal(e.clk.Now()) {
		t.Errorf("CreatedAt = %v, want %v", deep.CreatedAt, e.clk.Now())
	}
	// A top-level post is its own root.
	if post.PostID != p.ID || post.RootID != p.ID || post.Actor.Username != "bob" {
		t.Errorf("post notification = %+v", post)
	}
}

func TestReplyNotifications(t *testing.T) {
	t.Parallel()
	reply := []app.NotificationKind{app.NotificationReply}
	tests := []struct {
		name string
		run  func(e *notifEnv)
		want map[string][]app.NotificationKind
	}{
		{
			name: "a comment under a post tells the author",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post")
				e.settle()
				e.say("admin", p.ID, "comment")
			},
			want: map[string][]app.NotificationKind{"bob": reply},
		},
		{
			name: "a comment under a comment tells that comment's author and not the post's",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post")
				c := e.say("admin", p.ID, "comment")
				e.settle()
				e.say("bob", c.ID, "answer")
			},
			want: map[string][]app.NotificationKind{"admin": reply},
		},
		{
			name: "at depth: only the direct parent is told",
			run: func(e *notifEnv) {
				e.join("alice", "carol")
				p := e.mustCreate("bob", "lobby", app.FormatText, "post")
				c1 := e.say("admin", p.ID, "one")
				c2 := e.say("alice", c1.ID, "two")
				c3 := e.say("carol", c2.ID, "three")
				e.settle()
				e.say("bob", c3.ID, "four")
			},
			want: map[string][]app.NotificationKind{"carol": reply},
		},
		{
			name: "oneself: nothing",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post")
				e.settle()
				e.say("bob", p.ID, "me again")
			},
			want: nil,
		},
		{
			name: "oneself at depth: nothing",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post")
				c := e.say("admin", p.ID, "comment")
				c2 := e.say("bob", c.ID, "answer")
				e.settle()
				e.say("bob", c2.ID, "answer to myself")
			},
			want: nil,
		},
		{
			name: "reply wins over mention: one notification",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post")
				e.settle()
				e.say("admin", p.ID, "thanks @bob")
			},
			want: map[string][]app.NotificationKind{"bob": reply},
		},
		{
			name: "reply to bob and a mention of carol",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post")
				e.settle()
				e.say("admin", p.ID, "thanks @bob, cc @carol")
			},
			want: map[string][]app.NotificationKind{"bob": reply, "carol": {app.NotificationMention}},
		},
		{
			name: "the author of the post is disabled: nobody is told",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post")
				e.disable("bob")
				e.settle()
				e.say("admin", p.ID, "comment")
			},
			want: nil,
		},
		{
			name: "a private room: the author left it, nobody is told",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "den", app.FormatText, "post")
				if err := e.rooms.Leave(e.t.Context(), e.as("bob"), e.den.ID); err != nil {
					e.t.Fatalf("Leave: %+v", err)
				}
				e.settle()
				e.say("alice", p.ID, "comment")
			},
			want: nil,
		},
		{
			name: "a private room",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "den", app.FormatText, "post")
				e.settle()
				e.say("alice", p.ID, "comment")
			},
			want: map[string][]app.NotificationKind{"bob": reply},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNotifEnv(t)
			tt.run(e)
			e.requireNews(tt.want)
		})
	}
}

func TestReplyNotificationDetails(t *testing.T) {
	t.Parallel()
	e := newNotifEnv(t)
	p := e.mustCreate("bob", "lobby", app.FormatText, "post")
	c := e.say("admin", p.ID, "one")
	c2 := e.say("bob", c.ID, "two")
	got := e.news("admin")
	if len(got) != 1 {
		t.Fatalf("admin has %d notifications, want 1", len(got))
	}
	n := got[0]
	if n.Kind != app.NotificationReply || n.PostID != c2.ID || n.RootID != p.ID || n.Actor.Username != "bob" || n.RoomName != "lobby" {
		t.Errorf("notification = %+v", n)
	}
}

func TestAssignedNotifications(t *testing.T) {
	t.Parallel()
	assigned := []app.NotificationKind{app.NotificationAssigned}
	tests := []struct {
		name string
		run  func(e *notifEnv)
		want map[string][]app.NotificationKind
	}{
		{
			name: "a post with a task tells the assignees, not the actor",
			run: func(e *notifEnv) {
				e.mustCreate("bob", "lobby", app.FormatText, "post", e.assign("t", "bob", "admin"))
			},
			want: map[string][]app.NotificationKind{"admin": assigned},
		},
		{
			name: "a comment with a task",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post")
				e.settle()
				_, err := e.posts.Reply(e.t.Context(), e.as("bob"), p.ID,
					app.PostInput{Format: app.FormatText, Body: "c", Tasks: []app.TaskInput{e.assign("t", "admin")}})
				if err != nil {
					e.t.Fatalf("Reply: %+v", err)
				}
			},
			want: map[string][]app.NotificationKind{"admin": assigned},
		},
		{
			name: "a task nobody is assigned to",
			run:  func(e *notifEnv) { e.mustCreate("bob", "lobby", app.FormatText, "post", e.assign("t")) },
			want: nil,
		},
		{
			name: "assigned to oneself: nothing",
			run:  func(e *notifEnv) { e.mustCreate("bob", "lobby", app.FormatText, "post", e.assign("t", "bob")) },
			want: nil,
		},
		{
			name: "two tasks for one user: one notification",
			run: func(e *notifEnv) {
				e.mustCreate("bob", "lobby", app.FormatText, "post", e.assign("one", "admin"), e.assign("two", "admin"))
			},
			want: map[string][]app.NotificationKind{"admin": assigned},
		},
		{
			name: "assigned wins over mention: one notification",
			run: func(e *notifEnv) {
				e.mustCreate("bob", "lobby", app.FormatText, "post for @admin", e.assign("t", "admin"))
			},
			want: map[string][]app.NotificationKind{"admin": assigned},
		},
		{
			name: "assigned wins over reply: one notification",
			run: func(e *notifEnv) {
				p := e.mustCreate("admin", "lobby", app.FormatText, "post")
				e.settle()
				_, err := e.posts.Reply(e.t.Context(), e.as("bob"), p.ID,
					app.PostInput{Format: app.FormatText, Body: "c", Tasks: []app.TaskInput{e.assign("t", "admin")}})
				if err != nil {
					e.t.Fatalf("Reply: %+v", err)
				}
			},
			want: map[string][]app.NotificationKind{"admin": assigned},
		},
		{
			name: "a mention of another person stays",
			run: func(e *notifEnv) {
				e.mustCreate("bob", "lobby", app.FormatText, "post for @carol", e.assign("t", "admin"))
			},
			want: map[string][]app.NotificationKind{"admin": assigned, "carol": {app.NotificationMention}},
		},
		{
			name: "an edit that adds a task",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post")
				e.settle()
				e.mustEdit("bob", p, "post", e.assign("t", "admin"))
			},
			want: map[string][]app.NotificationKind{"admin": assigned},
		},
		{
			name: "an edit that assigns somebody new and keeps the old assignee",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post", e.assign("t", "admin"))
				e.settle()
				e.mustEdit("bob", p, "post", withKeep(p.Tasks[0], e.ids("admin", "bob")))
			},
			// bob is the actor: nobody new to tell, and admin is not told twice.
			want: nil,
		},
		{
			name: "an edit that changes only the title of a task: nobody is told",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post", e.assign("t", "admin"))
				e.settle()
				e.mustEdit("bob", p, "post", app.TaskInput{ID: p.Tasks[0].ID, Title: "renamed", Assignees: e.ids("admin")})
			},
			want: nil,
		},
		{
			name: "an edit that unassigns: nobody is told",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post", e.assign("t", "admin"))
				e.settle()
				e.mustEdit("bob", p, "post", app.TaskInput{ID: p.Tasks[0].ID, Title: "t"})
			},
			want: nil,
		},
		{
			name: "an edit of the text only: existing assignees are not told again",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post", e.assign("t", "admin"))
				e.settle()
				e.mustEdit("bob", p, "post, edited", withKeep(p.Tasks[0], e.ids("admin")))
			},
			want: nil,
		},
		{
			name: "a new task for an existing assignee: told again for the new task",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post", e.assign("one", "admin"))
				e.settle()
				e.mustEdit("bob", p, "post", withKeep(p.Tasks[0], e.ids("admin")), e.assign("two", "admin"))
			},
			want: map[string][]app.NotificationKind{"admin": assigned},
		},
		{
			name: "an edit by the author who assigns someone and mentions them",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post")
				e.settle()
				e.mustEdit("bob", p, "post for @admin", e.assign("t", "admin"))
			},
			want: map[string][]app.NotificationKind{"admin": assigned},
		},
		{
			name: "the board card editor: a new assignee",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post", e.assign("t", "bob"))
				e.settle()
				as := e.ids("bob", "admin")
				if err := e.board.UpdateTask(e.t.Context(), e.as("bob"), p.Tasks[0].ID, app.TaskPatch{Assignees: &as}); err != nil {
					e.t.Fatalf("UpdateTask: %+v", err)
				}
			},
			want: map[string][]app.NotificationKind{"admin": assigned},
		},
		{
			name: "the board card editor: an existing assignee is not told again",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post", e.assign("t", "admin"))
				e.settle()
				as := e.ids("admin")
				title := "renamed"
				if err := e.board.UpdateTask(e.t.Context(), e.as("bob"), p.Tasks[0].ID, app.TaskPatch{Title: &title, Assignees: &as}); err != nil {
					e.t.Fatalf("UpdateTask: %+v", err)
				}
			},
			want: nil,
		},
		{
			name: "the board card editor: assigning oneself",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post", e.assign("t", "admin"))
				e.settle()
				as := e.ids("bob")
				if err := e.board.UpdateTask(e.t.Context(), e.as("bob"), p.Tasks[0].ID, app.TaskPatch{Assignees: &as}); err != nil {
					e.t.Fatalf("UpdateTask: %+v", err)
				}
			},
			want: nil,
		},
		{
			name: "the board card editor: a disabled assignee is not told",
			run: func(e *notifEnv) {
				e.join("dave")
				p := e.mustCreate("bob", "lobby", app.FormatText, "post", e.assign("t", "bob"))
				e.disable("dave")
				e.settle()
				as := e.ids("bob", "dave")
				if err := e.board.UpdateTask(e.t.Context(), e.as("bob"), p.Tasks[0].ID, app.TaskPatch{Assignees: &as}); err != nil {
					e.t.Fatalf("UpdateTask: %+v", err)
				}
			},
			want: nil,
		},
		{
			name: "the board card editor: a move tells nobody",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post", e.assign("t", "admin"))
				e.settle()
				cols := e.view("bob", e.lobby.ID).Columns
				if err := e.board.MoveTask(e.t.Context(), e.as("bob"), p.Tasks[0].ID, cols[1].ID, 0); err != nil {
					e.t.Fatalf("MoveTask: %+v", err)
				}
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNotifEnv(t)
			tt.run(e)
			e.requireNews(tt.want)
		})
	}
}

func TestAssignedNotificationDetails(t *testing.T) {
	t.Parallel()
	e := newNotifEnv(t)
	p := e.mustCreate("bob", "lobby", app.FormatText, "post", e.assign("Write the report", "admin"))
	c, err := e.posts.Reply(e.t.Context(), e.as("bob"), p.ID,
		app.PostInput{Format: app.FormatText, Body: "c", Tasks: []app.TaskInput{e.assign("Review it", "admin")}})
	if err != nil {
		t.Fatalf("Reply: %+v", err)
	}
	got := e.news("admin")
	if len(got) != 2 {
		t.Fatalf("admin has %d notifications, want 2", len(got))
	}
	onComment, onPost := got[0], got[1]
	if onPost.Kind != app.NotificationAssigned || onPost.TaskTitle != "Write the report" || onPost.TaskID != p.Tasks[0].ID ||
		onPost.PostID != p.ID || onPost.RootID != p.ID || onPost.Actor.Username != "bob" {
		t.Errorf("on the post = %+v", onPost)
	}
	if onComment.TaskTitle != "Review it" || onComment.PostID != c.ID || onComment.RootID != p.ID || onComment.TaskID != c.Tasks[0].ID {
		t.Errorf("on the comment = %+v", onComment)
	}
}

func TestAddedToRoomNotifications(t *testing.T) {
	t.Parallel()
	added := []app.NotificationKind{app.NotificationAddedToRoom}
	tests := []struct {
		name string
		run  func(e *notifEnv)
		want map[string][]app.NotificationKind
	}{
		{
			name: "added to a private room",
			run: func(e *notifEnv) {
				if err := e.rooms.AddMember(e.t.Context(), e.as("alice"), e.den.ID, e.uid("carol")); err != nil {
					e.t.Fatalf("AddMember: %+v", err)
				}
			},
			want: map[string][]app.NotificationKind{"carol": added},
		},
		{
			name: "added to a visible room by a member",
			run: func(e *notifEnv) {
				if err := e.rooms.AddMember(e.t.Context(), e.as("bob"), e.lobby.ID, e.uid("dave")); err != nil {
					e.t.Fatalf("AddMember: %+v", err)
				}
			},
			want: map[string][]app.NotificationKind{"dave": added},
		},
		{
			name: "joining by oneself gives nothing",
			run: func(e *notifEnv) {
				if err := e.rooms.Join(e.t.Context(), e.as("dave"), e.lobby.ID); err != nil {
					e.t.Fatalf("Join: %+v", err)
				}
			},
			want: nil,
		},
		{
			name: "adding a member again gives nothing",
			run: func(e *notifEnv) {
				if err := e.rooms.AddMember(e.t.Context(), e.as("alice"), e.den.ID, e.uid("bob")); err != nil {
					e.t.Fatalf("AddMember: %+v", err)
				}
			},
			want: nil,
		},
		{
			name: "adding oneself gives nothing",
			run: func(e *notifEnv) {
				if err := e.rooms.AddMember(e.t.Context(), e.as("alice"), e.den.ID, e.uid("alice")); err != nil {
					e.t.Fatalf("AddMember: %+v", err)
				}
			},
			want: nil,
		},
		{
			name: "a refused addition gives nothing",
			run: func(e *notifEnv) {
				if err := e.rooms.AddMember(e.t.Context(), e.as("carol"), e.den.ID, e.uid("dave")); err == nil {
					e.t.Fatalf("AddMember by a non-member succeeded")
				}
				e.disable("dave")
				if err := e.rooms.AddMember(e.t.Context(), e.as("alice"), e.den.ID, e.uid("dave")); err == nil {
					e.t.Fatalf("AddMember of a disabled user succeeded")
				}
			},
			want: nil,
		},
		{
			name: "leaving or removal gives nothing",
			run: func(e *notifEnv) {
				if err := e.rooms.RemoveMember(e.t.Context(), e.as("admin"), e.den.ID, e.uid("bob")); err != nil {
					e.t.Fatalf("RemoveMember: %+v", err)
				}
				if err := e.rooms.Leave(e.t.Context(), e.as("alice"), e.den.ID); err != nil {
					e.t.Fatalf("Leave: %+v", err)
				}
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNotifEnv(t)
			tt.run(e)
			e.requireNews(tt.want)
		})
	}
}

func TestAddedToRoomNotificationDetails(t *testing.T) {
	t.Parallel()
	e := newNotifEnv(t)
	e.intoDen("carol")
	// intoDen settled; look at the whole list.
	all := e.list("carol", 0, 10).Notifications
	if len(all) != 1 {
		t.Fatalf("carol has %d notifications, want 1", len(all))
	}
	n := all[0]
	if n.Kind != app.NotificationAddedToRoom || n.RoomID != e.den.ID || n.RoomName != "den" ||
		n.PostID != 0 || n.RootID != 0 || n.TaskID != 0 || n.Actor.Username != "alice" {
		t.Errorf("notification = %+v", n)
	}
}

func TestEditMentions(t *testing.T) {
	t.Parallel()
	mention := []app.NotificationKind{app.NotificationMention}
	tests := []struct {
		name string
		// run does the edit and returns the ID of the edited post or comment.
		run  func(e *notifEnv) uint64
		want map[string][]app.NotificationKind
		// mentions are the usernames the post has saved afterwards.
		mentions []string
	}{
		{
			name: "a new mention is notified",
			run: func(e *notifEnv) uint64 {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post")
				e.settle()
				return e.mustEdit("bob", p, "post for @carol").ID
			},
			want:     map[string][]app.NotificationKind{"carol": mention},
			mentions: []string{"carol"},
		},
		{
			name: "an existing mention is not notified again",
			run: func(e *notifEnv) uint64 {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post for @carol")
				e.settle()
				return e.mustEdit("bob", p, "post for @carol, edited").ID
			},
			want:     nil,
			mentions: []string{"carol"},
		},
		{
			name: "one old and one new: only the new one",
			run: func(e *notifEnv) uint64 {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post for @carol")
				e.settle()
				return e.mustEdit("bob", p, "post for @carol and @dave").ID
			},
			want:     map[string][]app.NotificationKind{"dave": mention},
			mentions: []string{"carol", "dave"},
		},
		{
			name: "a removed mention is dropped and tells nobody",
			run: func(e *notifEnv) uint64 {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post for @carol and @dave")
				e.settle()
				return e.mustEdit("bob", p, "post for @dave").ID
			},
			want:     nil,
			mentions: []string{"dave"},
		},
		{
			name: "removed and written again: notified again",
			run: func(e *notifEnv) uint64 {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post for @carol")
				p = e.mustEdit("bob", p, "post")
				e.settle()
				return e.mustEdit("bob", p, "post for @carol again").ID
			},
			want:     map[string][]app.NotificationKind{"carol": mention},
			mentions: []string{"carol"},
		},
		{
			name: "an edit that does not change the body keeps the mentions",
			run: func(e *notifEnv) uint64 {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post for @carol")
				e.settle()
				if _, err := e.posts.Edit(e.t.Context(), e.as("bob"), p.ID,
					app.PostInput{Format: app.FormatText, Body: "post for @carol", Tags: []string{"new"}}); err != nil {
					e.t.Fatalf("Edit: %+v", err)
				}
				return p.ID
			},
			want:     nil,
			mentions: []string{"carol"},
		},
		{
			name: "a mention moved into code is dropped",
			run: func(e *notifEnv) uint64 {
				p := e.mustCreate("bob", "lobby", app.FormatMarkdown, "post for @carol")
				e.settle()
				return e.mustEdit("bob", p, "post for `@carol`").ID
			},
			want:     nil,
			mentions: nil,
		},
		{
			name: "a new mention of a user who cannot read the private room",
			run: func(e *notifEnv) uint64 {
				p := e.mustCreate("alice", "den", app.FormatText, "post")
				e.settle()
				return e.mustEdit("alice", p, "post for @carol").ID
			},
			want:     nil,
			mentions: nil,
		},
		{
			name: "a new mention of a disabled user",
			run: func(e *notifEnv) uint64 {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post")
				e.disable("carol")
				e.settle()
				return e.mustEdit("bob", p, "post for @carol").ID
			},
			want:     nil,
			mentions: nil,
		},
		{
			name: "a mention of oneself",
			run: func(e *notifEnv) uint64 {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post")
				e.settle()
				return e.mustEdit("bob", p, "post for @bob").ID
			},
			want:     nil,
			mentions: []string{"bob"},
		},
		{
			name: "a comment edited to mention: the reply notification is not repeated",
			run: func(e *notifEnv) uint64 {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post")
				c := e.say("admin", p.ID, "comment")
				e.settle()
				return e.mustEdit("admin", c, "comment for @bob").ID
			},
			// bob already knows of the reply; the edit mentions him: a mention.
			want:     map[string][]app.NotificationKind{"bob": mention},
			mentions: []string{"bob"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNotifEnv(t)
			id := tt.run(e)
			e.requireNews(tt.want)
			if got := e.mentionsOf(id); !slices.Equal(got, tt.mentions) && (len(got) != 0 || len(tt.mentions) != 0) {
				t.Errorf("saved mentions = %v, want %v", got, tt.mentions)
			}
		})
	}
}

func TestMentionsAreLinks(t *testing.T) {
	t.Parallel()
	e := newNotifEnv(t)
	link := func(name string) string {
		return `<a href="/search?q=from%3A` + name + `" class="mention">@` + name + `</a>`
	}
	text := e.mustCreate("bob", "lobby", app.FormatText, "hello @carol and @nobody <b>")
	md := e.mustCreate("bob", "lobby", app.FormatMarkdown, "hello **@carol**, `@carol` and @nobody")
	if !strings.Contains(string(text.HTML), link("carol")) || strings.Contains(string(text.HTML), "from%3Anobody") {
		t.Errorf("text post HTML = %s", text.HTML)
	}
	if !strings.Contains(string(text.HTML), "&lt;b&gt;") {
		t.Errorf("text post is not escaped: %s", text.HTML)
	}
	if !strings.Contains(string(md.HTML), "<strong>"+link("carol")+"</strong>") ||
		!strings.Contains(string(md.HTML), "<code>@carol</code>") || strings.Contains(string(md.HTML), "from%3Anobody") {
		t.Errorf("markdown post HTML = %s", md.HTML)
	}

	// The feed, the thread and a single post show the same links.
	feed, err := e.posts.Feed(t.Context(), e.as("carol"), e.lobby.ID, 0, 10)
	if err != nil || len(feed.Posts) != 2 {
		t.Fatalf("Feed: %v %v", feed.Posts, err)
	}
	for _, p := range feed.Posts {
		if !strings.Contains(string(p.HTML), link("carol")) {
			t.Errorf("feed post %d HTML = %s", p.ID, p.HTML)
		}
	}
	c := e.say("admin", text.ID, "and you, @dave")
	th := e.thread("carol", text.ID)
	if len(th.Comments) != 1 || !strings.Contains(string(th.Comments[0].HTML), link("dave")) {
		t.Errorf("thread comments = %+v", th.Comments)
	}
	if got, err := e.posts.Get(t.Context(), e.as("carol"), c.ID); err != nil || !strings.Contains(string(got.HTML), link("dave")) {
		t.Errorf("Get HTML = %s, %v", got.HTML, err)
	}

	// An edit that drops the mention drops the link.
	edited := e.mustEdit("bob", text, "hello nobody")
	if strings.Contains(string(edited.HTML), "mention") {
		t.Errorf("edited HTML = %s", edited.HTML)
	}
}

func TestMentionOfSomeoneWhoCannotReadIsNoLink(t *testing.T) {
	t.Parallel()
	e := newNotifEnv(t)
	p := e.mustCreate("alice", "den", app.FormatText, "psst @carol and @bob")
	if strings.Contains(string(p.HTML), "from%3Acarol") || !strings.Contains(string(p.HTML), "from%3Abob") {
		t.Errorf("HTML = %s", p.HTML)
	}
	if !strings.Contains(string(p.HTML), "@carol") {
		t.Errorf("the text of the unlinked mention is gone: %s", p.HTML)
	}
}

func TestSearchSnippetsStayPlain(t *testing.T) {
	t.Parallel()
	e := newNotifEnv(t)
	e.mustCreate("bob", "lobby", app.FormatText, "deploy tonight, ask @carol")
	page, err := app.NewSearch(e.st).Query(t.Context(), e.as("admin"), "deploy", 1)
	if err != nil || len(page.Results) != 1 {
		t.Fatalf("Query: %v %v", page.Results, err)
	}
	snippet := string(page.Results[0].Snippet)
	if strings.Contains(snippet, "<a ") || !strings.Contains(snippet, "@carol") || !strings.Contains(snippet, "<mark>deploy</mark>") {
		t.Errorf("snippet = %s", snippet)
	}
}

func TestNotificationsDeletedWithTheirPost(t *testing.T) {
	t.Parallel()
	assigned, mention, reply := app.NotificationAssigned, app.NotificationMention, app.NotificationReply
	tests := []struct {
		name string
		run  func(e *notifEnv)
		// want are the notifications that are left, by user.
		want map[string][]app.NotificationKind
	}{
		{
			name: "a post with a mention and an assignment",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "for @carol", e.assign("t", "admin"))
				e.settle()
				if err := e.posts.Delete(e.t.Context(), e.as("bob"), p.ID); err != nil {
					e.t.Fatalf("Delete: %+v", err)
				}
			},
			want: nil,
		},
		{
			name: "a comment: the reply and the mention go with it",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post")
				c := e.say("admin", p.ID, "thanks @carol")
				if err := e.posts.Delete(e.t.Context(), e.as("admin"), c.ID); err != nil {
					e.t.Fatalf("Delete: %+v", err)
				}
			},
			want: nil,
		},
		{
			name: "a post that stays as a placeholder loses its own notifications and keeps those of its replies",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "for @carol", e.assign("t", "admin"))
				e.say("admin", p.ID, "thanks @carol")
				if err := e.posts.Delete(e.t.Context(), e.as("bob"), p.ID); err != nil {
					e.t.Fatalf("Delete: %+v", err)
				}
			},
			// The comment's reply (bob) and mention (carol) are about the comment.
			want: map[string][]app.NotificationKind{"bob": {reply}, "carol": {mention}},
		},
		{
			name: "the clean-up of a placeholder: the last reply goes, the placeholder goes, nothing is left",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "for @carol", e.assign("t", "admin"))
				c := e.say("admin", p.ID, "thanks @carol")
				if err := e.posts.Delete(e.t.Context(), e.as("bob"), p.ID); err != nil {
					e.t.Fatalf("Delete post: %+v", err)
				}
				if err := e.posts.Delete(e.t.Context(), e.as("admin"), c.ID); err != nil {
					e.t.Fatalf("Delete comment: %+v", err)
				}
				if e.rowExists(p.ID) {
					e.t.Errorf("the placeholder was not cleaned up")
				}
			},
			want: nil,
		},
		{
			name: "a task removed in an edit takes its assignment notification",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post", e.assign("t", "admin"))
				p = e.mustEdit("bob", p, "post", withKeep(p.Tasks[0], e.ids("admin")), e.assign("u", "admin"))
				e.mustEdit("bob", p, "post", withKeep(p.Tasks[1], e.ids("admin")))
			},
			// Two actions made two notifications. The first task goes, with
			// its notification; the second stays.
			want: map[string][]app.NotificationKind{"admin": {assigned}},
		},
		{
			name: "all tasks removed in an edit",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post", e.assign("t", "admin"))
				e.mustEdit("bob", p, "post")
			},
			want: nil,
		},
		{
			name: "the room goes: all its notifications go",
			run: func(e *notifEnv) {
				p := e.mustCreate("alice", "den", app.FormatText, "post for @bob", e.assign("t", "bob"))
				e.say("bob", p.ID, "thanks")
				if err := e.rooms.Delete(e.t.Context(), e.as("alice"), e.den.ID); err != nil {
					e.t.Fatalf("Delete room: %+v", err)
				}
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNotifEnv(t)
			tt.run(e)
			e.requireNews(tt.want)
		})
	}
}

func TestNotificationsList(t *testing.T) {
	t.Parallel()
	e := newNotifEnv(t)
	var ids []uint64
	for i := range 7 {
		p := e.mustCreate("bob", "lobby", app.FormatText, "post "+string(rune('a'+i))+" @carol")
		ids = append(ids, p.ID)
	}
	e.mustCreate("bob", "lobby", app.FormatText, "for dave @dave")

	t.Run("newest first, paged with a cursor", func(t *testing.T) {
		first := e.list("carol", 0, 3)
		if len(first.Notifications) != 3 || !first.HasNext || first.NextBefore != first.Notifications[2].ID {
			t.Fatalf("first page = %+v", first)
		}
		var posts []uint64
		for _, n := range first.Notifications {
			posts = append(posts, n.PostID)
		}
		if want := []uint64{ids[6], ids[5], ids[4]}; !slices.Equal(posts, want) {
			t.Errorf("first page posts = %v, want %v", posts, want)
		}
		second := e.list("carol", first.NextBefore, 3)
		if len(second.Notifications) != 3 || !second.HasNext || second.Notifications[0].PostID != ids[3] {
			t.Fatalf("second page = %+v", second)
		}
		last := e.list("carol", second.NextBefore, 3)
		if len(last.Notifications) != 1 || last.HasNext || last.NextBefore != 0 || last.Notifications[0].PostID != ids[0] {
			t.Fatalf("last page = %+v", last)
		}
	})
	t.Run("30 a page by default and at most 100", func(t *testing.T) {
		e := newNotifEnv(t)
		for range 105 {
			e.mustCreate("bob", "lobby", app.FormatText, "@carol")
		}
		if page := e.list("carol", 0, 0); len(page.Notifications) != 30 || !page.HasNext {
			t.Errorf("default page = %d, next %v", len(page.Notifications), page.HasNext)
		}
		if page := e.list("carol", 0, -1); len(page.Notifications) != 30 {
			t.Errorf("negative limit page = %d", len(page.Notifications))
		}
		if page := e.list("carol", 0, 500); len(page.Notifications) != 100 || !page.HasNext {
			t.Errorf("a limit over the maximum gave %d, next %v", len(page.Notifications), page.HasNext)
		}
		if page := e.list("carol", 0, 100); len(page.Notifications) != 100 || !page.HasNext {
			t.Errorf("a limit of 100 gave %d, next %v", len(page.Notifications), page.HasNext)
		}
	})
	t.Run("only one's own", func(t *testing.T) {
		if got := e.news("dave"); len(got) != 1 || got[0].Kind != app.NotificationMention {
			t.Errorf("dave sees %+v", got)
		}
		for _, name := range []string{"alice", "bob", "admin", "root"} {
			if got := e.news(name); len(got) != 0 {
				t.Errorf("%s sees %+v", name, got)
			}
		}
	})
}

func TestNotificationsHiddenWithTheRoom(t *testing.T) {
	t.Parallel()
	e := newNotifEnv(t)
	e.intoDen("carol")
	e.mustCreate("alice", "den", app.FormatText, "psst @carol")
	e.mustCreate("bob", "lobby", app.FormatText, "hello @carol")
	if n, err := e.notes.UnreadCount(t.Context(), e.as("carol")); err != nil || n != 3 {
		t.Fatalf("UnreadCount = %d, %v, want 3 (added, mention in den, mention in lobby)", n, err)
	}

	// An admin removes carol from the private room: its notifications go from
	// the list and the count, the visible room's stay (D29, D30).
	if err := e.rooms.RemoveMember(t.Context(), e.as("admin"), e.den.ID, e.uid("carol")); err != nil {
		t.Fatalf("RemoveMember: %+v", err)
	}
	page := e.list("carol", 0, 50)
	if len(page.Notifications) != 1 || page.Notifications[0].RoomName != "lobby" {
		t.Errorf("after removal carol sees %+v", page.Notifications)
	}
	if n, err := e.notes.UnreadCount(t.Context(), e.as("carol")); err != nil || n != 1 {
		t.Errorf("UnreadCount after removal = %d, %v, want 1", n, err)
	}
	// Paging still works over the hidden ones.
	if page := e.list("carol", 0, 1); len(page.Notifications) != 1 || page.HasNext {
		t.Errorf("page of one = %+v", page)
	}

	// Added again, she sees them again.
	if err := e.rooms.AddMember(t.Context(), e.as("alice"), e.den.ID, e.uid("carol")); err != nil {
		t.Fatalf("AddMember: %+v", err)
	}
	if got := e.list("carol", 0, 50).Notifications; len(got) != 4 {
		t.Errorf("after being added again carol sees %d, want 4 (the new addition included): %+v", len(got), got)
	}
}

func TestUnreadCountAndMarkRead(t *testing.T) {
	t.Parallel()
	e := newNotifEnv(t)
	for range 4 {
		e.mustCreate("bob", "lobby", app.FormatText, "@carol")
	}
	e.mustCreate("bob", "lobby", app.FormatText, "@dave")
	count := func(user string) int {
		t.Helper()
		n, err := e.notes.UnreadCount(t.Context(), e.as(user))
		if err != nil {
			t.Fatalf("UnreadCount %s: %+v", user, err)
		}
		return n
	}
	if count("carol") != 4 || count("dave") != 1 || count("alice") != 0 {
		t.Fatalf("counts = %d, %d, %d", count("carol"), count("dave"), count("alice"))
	}
	page := e.list("carol", 0, 50).Notifications // newest first
	upTo := page[1].ID                           // the second newest

	e.rec.Reset()
	if err := e.notes.MarkRead(t.Context(), e.as("carol"), page[2].ID); err != nil {
		t.Fatalf("MarkRead: %+v", err)
	}
	if got := count("carol"); got != 2 {
		t.Errorf("unread after marking up to %d = %d, want 2", page[2].ID, got)
	}
	if count("dave") != 1 {
		t.Errorf("another user's count changed")
	}
	read := e.list("carol", 0, 50).Notifications
	for i, n := range read {
		if wantRead := i >= 2; n.Read != wantRead || (n.ReadAt != nil) != wantRead {
			t.Errorf("notification %d (ID %d): Read = %v, ReadAt = %v", i, n.ID, n.Read, n.ReadAt)
		}
		if n.Read && !n.ReadAt.Equal(e.clk.Now()) {
			t.Errorf("ReadAt = %v, want %v", n.ReadAt, e.clk.Now())
		}
	}
	wantEv := []live.Event{{Topic: live.UserTopic(e.uid("carol")), Kind: live.KindNotificationRead, UserID: e.uid("carol")}}
	if got := e.rec.Events(); !reflect.DeepEqual(got, wantEv) {
		t.Errorf("published %+v, want %+v", got, wantEv)
	}

	// Marking again changes nothing and publishes nothing.
	e.rec.Reset()
	if err := e.notes.MarkRead(t.Context(), e.as("carol"), page[2].ID); err != nil {
		t.Fatalf("MarkRead again: %+v", err)
	}
	if got := e.rec.Events(); len(got) != 0 {
		t.Errorf("a no-op published %+v", got)
	}

	// A user marks only their own: dave marks up to carol's newest ID.
	if err := e.notes.MarkRead(t.Context(), e.as("alice"), page[0].ID+100); err != nil {
		t.Fatalf("MarkRead by alice: %+v", err)
	}
	if count("carol") != 2 || count("dave") != 1 {
		t.Errorf("alice's MarkRead changed others: carol %d, dave %d", count("carol"), count("dave"))
	}

	// Marking up to a larger ID reads the rest; ID 0 reads nothing.
	if err := e.notes.MarkRead(t.Context(), e.as("carol"), 0); err != nil {
		t.Fatalf("MarkRead 0: %+v", err)
	}
	if count("carol") != 2 {
		t.Errorf("MarkRead(0) read something: %d", count("carol"))
	}
	if err := e.notes.MarkRead(t.Context(), e.as("carol"), upTo); err != nil {
		t.Fatalf("MarkRead: %+v", err)
	}
	if count("carol") != 1 || count("dave") != 1 {
		t.Errorf("after marking up to the second newest: carol %d, dave %d, want 1 and 1", count("carol"), count("dave"))
	}
	if err := e.notes.MarkRead(t.Context(), e.as("carol"), page[0].ID); err != nil {
		t.Fatalf("MarkRead: %+v", err)
	}
	if count("carol") != 0 || count("dave") != 1 {
		t.Errorf("carol %d, dave %d", count("carol"), count("dave"))
	}
}

func TestNotificationsPermissions(t *testing.T) {
	t.Parallel()
	e := newNotifEnv(t)
	e.mustCreate("bob", "lobby", app.FormatText, "@alice @carol @admin @root")
	someone := e.as("alice")
	calls := map[string]func(a app.Actor) error{
		"List": func(a app.Actor) error {
			_, err := e.notes.List(t.Context(), a, 0, 10)
			return err
		},
		"UnreadCount": func(a app.Actor) error {
			_, err := e.notes.UnreadCount(t.Context(), a)
			return err
		},
		"MarkRead": func(a app.Actor) error { return e.notes.MarkRead(t.Context(), a, 1000) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			for _, who := range []string{"admin", "root", "alice", "bob", "carol", "dave"} {
				if err := call(e.as(who)); err != nil {
					t.Errorf("%s as %s: %+v", name, who, err)
				}
			}
			err := call(app.Actor{})
			_ = requireTag(t, err, app.TagUnauthenticated, "Notifications."+name+".Unauthenticated")
			// An actor with a username but no ID is not logged in either.
			err = call(app.Actor{Username: someone.Username, Role: someone.Role})
			_ = requireTag(t, err, app.TagUnauthenticated, "Notifications."+name+".Unauthenticated")
		})
	}
}

func TestNotificationsOnlyOwn(t *testing.T) {
	t.Parallel()
	e := newNotifEnv(t)
	e.mustCreate("bob", "lobby", app.FormatText, "@alice @carol")
	for _, who := range []string{"admin", "root", "alice", "bob", "carol", "dave"} {
		want := map[string]int{"alice": 1, "carol": 1}[who]
		if got := e.news(who); len(got) != want {
			t.Errorf("%s sees %d new notifications, want %d", who, len(got), want)
		}
		n, err := e.notes.UnreadCount(t.Context(), e.as(who))
		if err != nil || n != want {
			t.Errorf("%s UnreadCount = %d, %v, want %d", who, n, err, want)
		}
	}
	// An admin sees nobody else's.
	if got := e.news("admin"); len(got) != 0 {
		t.Errorf("admin sees %+v", got)
	}
}

func TestNotificationEventsPublished(t *testing.T) {
	t.Parallel()
	notif := notifEvt
	tests := []struct {
		name string
		run  func(e *notifEnv)
		want func(e *notifEnv) []live.Event
	}{
		{
			name: "a mention publishes after the post events",
			run:  func(e *notifEnv) { e.mustCreate("bob", "lobby", app.FormatText, "@carol") },
			want: func(e *notifEnv) []live.Event {
				p := e.lastPost("lobby")
				return append(postEvts(live.KindPostCreated, e.lobby.ID, p), notif(e.uid("carol"), e.lobby.ID, p))
			},
		},
		{
			name: "one event per notified user, in the order assigned, reply, mentions",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post")
				e.settle()
				_, err := e.posts.Reply(e.t.Context(), e.as("bob"), p.ID,
					app.PostInput{Format: app.FormatText, Body: "@carol @dave", Tasks: []app.TaskInput{e.assign("t", "admin")}})
				if err != nil {
					e.t.Fatalf("Reply: %+v", err)
				}
			},
			want: func(e *notifEnv) []live.Event {
				// bob wrote the post and the comment: the reply is for himself.
				root := e.lastPost("lobby")
				c := e.thread("bob", root).Comments[0].ID
				return []live.Event{
					{Topic: live.PostTopic(root), Kind: live.KindCommentCreated, RoomID: e.lobby.ID, PostID: c, RootID: root},
					{Topic: live.RoomTopic(e.lobby.ID), Kind: live.KindPostUpdated, RoomID: e.lobby.ID, PostID: root, RootID: root},
					boardEvt(e.lobby.ID, c),
					notif(e.uid("admin"), e.lobby.ID, c),
					notif(e.uid("carol"), e.lobby.ID, c),
					notif(e.uid("dave"), e.lobby.ID, c),
				}
			},
		},
		{
			name: "nobody to notify: nothing extra",
			run:  func(e *notifEnv) { e.mustCreate("bob", "lobby", app.FormatText, "plain") },
			want: func(e *notifEnv) []live.Event {
				return postEvts(live.KindPostCreated, e.lobby.ID, e.lastPost("lobby"))
			},
		},
		{
			name: "an edit with a new mention",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post")
				e.settle()
				e.mustEdit("bob", p, "post @carol")
			},
			want: func(e *notifEnv) []live.Event {
				p := e.lastPost("lobby")
				return append(postEvts(live.KindPostUpdated, e.lobby.ID, p), notif(e.uid("carol"), e.lobby.ID, p))
			},
		},
		{
			name: "the card editor",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post", e.assign("t", "bob"))
				e.settle()
				as := e.ids("bob", "admin")
				if err := e.board.UpdateTask(e.t.Context(), e.as("bob"), p.Tasks[0].ID, app.TaskPatch{Assignees: &as}); err != nil {
					e.t.Fatalf("UpdateTask: %+v", err)
				}
			},
			want: func(e *notifEnv) []live.Event {
				p := e.lastPost("lobby")
				return append(boardEvts(e.lobby, app.Post{ID: p}), notif(e.uid("admin"), e.lobby.ID, p))
			},
		},
		{
			name: "added to a room",
			run: func(e *notifEnv) {
				if err := e.rooms.AddMember(e.t.Context(), e.as("alice"), e.den.ID, e.uid("carol")); err != nil {
					e.t.Fatalf("AddMember: %+v", err)
				}
			},
			want: func(e *notifEnv) []live.Event {
				return append(memberEvts(live.KindMemberJoined, e.den.ID, e.uid("carol")), notif(e.uid("carol"), e.den.ID, 0))
			},
		},
		{
			name: "a failed post publishes nothing and makes no notification",
			run: func(e *notifEnv) {
				_, err := e.create("bob", "lobby", app.FormatText, "@carol", app.TaskInput{Title: ""})
				_ = requireTag(e.t, err, app.TagInvalidInput, "Posts.Create")
			},
			want: func(e *notifEnv) []live.Event { return nil },
		},
		{
			name: "a refused reply publishes nothing and makes no notification",
			run: func(e *notifEnv) {
				p := e.mustCreate("bob", "lobby", app.FormatText, "post")
				e.settle()
				_, err := e.posts.Reply(e.t.Context(), e.as("carol"), p.ID, textInput("@dave"))
				_ = requireTag(e.t, err, app.TagForbidden, "Posts.Reply")
			},
			want: func(e *notifEnv) []live.Event { return nil },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNotifEnv(t)
			tt.run(e)
			want := tt.want(e)
			got := norm(e.rec.Events())
			if len(got) == 0 && len(want) == 0 {
				for _, who := range []string{"carol", "dave", "admin"} {
					if n := e.kinds(who); len(n) != 0 {
						t.Errorf("%s was notified: %v", who, n)
					}
				}
				return
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("published\n got  %+v\n want %+v", got, want)
			}
		})
	}
}

// lastPost returns the ID of the newest top-level post of the room.
func (e *notifEnv) lastPost(room string) uint64 {
	e.t.Helper()
	actor := e.as("admin")
	if room == "den" {
		actor = e.as("alice")
	}
	feed, err := e.posts.Feed(e.t.Context(), actor, e.room(room).ID, 0, 1)
	if err != nil || len(feed.Posts) == 0 {
		e.t.Fatalf("Feed: %v, %v", feed, err)
	}
	return feed.Posts[0].ID
}

// boardEvt is the live event of a change to a task of the post.
func boardEvt(room, post uint64) live.Event {
	return live.Event{Topic: live.BoardTopic(room), Kind: live.KindBoardChanged, RoomID: room, PostID: post}
}

// commitProbe is a Publisher that, for every notification.created, reads the
// notification of the user through the service: published after the commit, it
// is there.
type commitProbe struct {
	notes *app.Notifications
	as    func(userID uint64) app.Actor
	seen  map[uint64]int
	t     *testing.T
}

// Publish implements app.Publisher.
func (p *commitProbe) Publish(ev live.Event) {
	if ev.Kind != live.KindNotificationCreated {
		return
	}
	page, err := p.notes.List(p.t.Context(), p.as(ev.UserID), 0, 50)
	if err != nil {
		p.t.Errorf("List in Publish: %+v", err)
		return
	}
	p.seen[ev.UserID] = len(page.Notifications)
}

func TestNotificationEventsComeAfterTheCommit(t *testing.T) {
	t.Parallel()
	e := newNotifEnv(t)
	probe := &commitProbe{notes: e.notes, seen: map[uint64]int{}, t: t, as: func(id uint64) app.Actor {
		for name, u := range e.users {
			if u.ID == id {
				return e.as(name)
			}
		}
		t.Fatalf("no user %d", id)
		return app.Actor{}
	}}
	e.posts = app.NewPosts(e.st, e.clk, markdown.New(), e.files, app.PostsConfig{
		MaxAttachmentSize: testMaxUpload, MaxAttachments: testMaxAttachments,
	}, probe)
	e.mustCreate("bob", "lobby", app.FormatText, "@carol")
	if probe.seen[e.uid("carol")] != 1 {
		t.Errorf("when the event came, carol saw %d notifications, want 1", probe.seen[e.uid("carol")])
	}
}

func TestNotificationTimesUseTheClock(t *testing.T) {
	t.Parallel()
	e := newNotifEnv(t)
	e.clk.Advance(3 * time.Hour)
	e.mustCreate("bob", "lobby", app.FormatText, "@carol")
	got := e.news("carol")
	if len(got) != 1 || !got[0].CreatedAt.Equal(e.clk.Now()) {
		t.Errorf("notifications = %+v, want one at %v", got, e.clk.Now())
	}
}

func TestMentionsAreDeletedWithThePost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// replies is true when the post has a reply, so that it stays as a
		// placeholder.
		replies bool
	}{
		{"a post without replies disappears", false},
		{"a post with replies stays as a placeholder without mentions", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNotifEnv(t)
			p := e.mustCreate("bob", "lobby", app.FormatText, "for @carol and @dave")
			c := e.say("admin", p.ID, "thanks @carol")
			if !tt.replies {
				if err := e.posts.Delete(t.Context(), e.as("admin"), c.ID); err != nil {
					t.Fatalf("Delete comment: %+v", err)
				}
			}
			if got := e.mentionsOf(p.ID); len(got) != 2 {
				t.Fatalf("mentions before = %v", got)
			}
			if err := e.posts.Delete(t.Context(), e.as("bob"), p.ID); err != nil {
				t.Fatalf("Delete: %+v", err)
			}
			if got := e.mentionsOf(p.ID); len(got) != 0 {
				t.Errorf("mentions after = %v, want none", got)
			}
			if tt.replies {
				if got := e.mentionsOf(c.ID); !slices.Equal(got, []string{"carol"}) {
					t.Errorf("the reply lost its mentions: %v", got)
				}
			}
		})
	}
}
