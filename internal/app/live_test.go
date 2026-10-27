package app_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/markdown"
)

// liveEnv is the postsEnv with a Recorder behind both services. The fixture
// is built without it, so the Recorder starts empty.
type liveEnv struct {
	*postsEnv
	rec *live.Recorder
}

func newLiveEnv(t *testing.T) *liveEnv {
	t.Helper()
	e := &liveEnv{postsEnv: newPostsEnv(t), rec: &live.Recorder{}}
	e.rooms = app.NewRooms(e.st, e.files, nil, e.rec)
	e.posts = app.NewPosts(e.st, e.clk, markdown.New(), e.files, app.PostsConfig{
		MaxAttachmentSize: testMaxUpload,
		MaxAttachments:    testMaxAttachments,
	}, e.rec)
	return e
}

// uid is the ID of the named user.
func (e *liveEnv) uid(name string) uint64 {
	e.t.Helper()
	return e.users[name].ID
}

// liveResult is what a case did and what it expects to have been published.
type liveResult struct {
	err error
	// tag is the tag the call must fail with; empty when it must succeed.
	tag  string
	want []live.Event
}

// liveCase is one row of a table: run sets up its own data, resets the
// Recorder, makes the call and says what it expects.
type liveCase struct {
	name string
	run  func(e *liveEnv) liveResult
}

func runLiveCases(t *testing.T, cases []liveCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newLiveEnv(t)
			res := tc.run(e)
			if res.tag == "" {
				if res.err != nil {
					t.Fatalf("got %+v, want success", res.err)
				}
			} else {
				_ = requireTag(t, res.err, res.tag, res.tag)
			}
			got := e.rec.Events()
			if len(got) == 0 && len(res.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, res.want) {
				t.Errorf("published\n got  %+v\n want %+v", got, res.want)
			}
		})
	}
}

// memberEvts are the two events of a membership change.
func memberEvts(kind string, room, user uint64) []live.Event {
	return []live.Event{
		{Topic: live.RoomTopic(room), Kind: kind, RoomID: room, UserID: user},
		{Topic: live.UserTopic(user), Kind: kind, RoomID: room, UserID: user},
	}
}

func TestLiveRoomsPublish(t *testing.T) {
	runLiveCases(t, []liveCase{
		{"create publishes nothing", func(e *liveEnv) liveResult {
			_, err := e.rooms.Create(e.t.Context(), e.as("carol"), app.RoomInput{Name: "new", Visibility: app.VisibilityPrivate})
			return liveResult{err: err}
		}},
		{"join", func(e *liveEnv) liveResult {
			err := e.rooms.Join(e.t.Context(), e.as("dave"), e.lobby.ID)
			return liveResult{err: err, want: memberEvts(live.KindMemberJoined, e.lobby.ID, e.uid("dave"))}
		}},
		{"join when a member already", func(e *liveEnv) liveResult {
			return liveResult{err: e.rooms.Join(e.t.Context(), e.as("bob"), e.lobby.ID)}
		}},
		{"join a private room", func(e *liveEnv) liveResult {
			return liveResult{err: e.rooms.Join(e.t.Context(), e.as("carol"), e.den.ID), tag: app.TagNotFound}
		}},
		{"join a missing room", func(e *liveEnv) liveResult {
			return liveResult{err: e.rooms.Join(e.t.Context(), e.as("carol"), 9999), tag: app.TagNotFound}
		}},
		{"leave", func(e *liveEnv) liveResult {
			err := e.rooms.Leave(e.t.Context(), e.as("bob"), e.lobby.ID)
			return liveResult{err: err, want: memberEvts(live.KindMemberLeft, e.lobby.ID, e.uid("bob"))}
		}},
		{"leave when not a member", func(e *liveEnv) liveResult {
			return liveResult{err: e.rooms.Leave(e.t.Context(), e.as("dave"), e.lobby.ID)}
		}},
		{"leave a private room of others", func(e *liveEnv) liveResult {
			return liveResult{err: e.rooms.Leave(e.t.Context(), e.as("carol"), e.den.ID), tag: app.TagNotFound}
		}},
		{"add a member", func(e *liveEnv) liveResult {
			err := e.rooms.AddMember(e.t.Context(), e.as("bob"), e.lobby.ID, e.uid("dave"))
			return liveResult{err: err, want: memberEvts(live.KindMemberJoined, e.lobby.ID, e.uid("dave"))}
		}},
		{"add a member of a private room", func(e *liveEnv) liveResult {
			err := e.rooms.AddMember(e.t.Context(), e.as("alice"), e.den.ID, e.uid("carol"))
			return liveResult{err: err, want: memberEvts(live.KindMemberJoined, e.den.ID, e.uid("carol"))}
		}},
		{"add a user who is a member already", func(e *liveEnv) liveResult {
			return liveResult{err: e.rooms.AddMember(e.t.Context(), e.as("admin"), e.lobby.ID, e.uid("bob"))}
		}},
		{"add by a non-member of a visible room", func(e *liveEnv) liveResult {
			return liveResult{err: e.rooms.AddMember(e.t.Context(), e.as("carol"), e.lobby.ID, e.uid("dave")), tag: app.TagForbidden}
		}},
		{"add by a non-member of a private room", func(e *liveEnv) liveResult {
			return liveResult{err: e.rooms.AddMember(e.t.Context(), e.as("carol"), e.den.ID, e.uid("dave")), tag: app.TagNotFound}
		}},
		{"add a user who does not exist", func(e *liveEnv) liveResult {
			return liveResult{err: e.rooms.AddMember(e.t.Context(), e.as("bob"), e.lobby.ID, 9999), tag: app.TagInvalidInput}
		}},
		{"add a disabled user", func(e *liveEnv) liveResult {
			if err := e.svc.SetDisabled(e.t.Context(), e.as("admin"), e.uid("dave"), true); err != nil {
				e.t.Fatalf("SetDisabled: %+v", err)
			}
			return liveResult{err: e.rooms.AddMember(e.t.Context(), e.as("bob"), e.lobby.ID, e.uid("dave")), tag: app.TagInvalidInput}
		}},
		{"remove a member", func(e *liveEnv) liveResult {
			err := e.rooms.RemoveMember(e.t.Context(), e.as("admin"), e.lobby.ID, e.uid("bob"))
			return liveResult{err: err, want: memberEvts(live.KindMemberLeft, e.lobby.ID, e.uid("bob"))}
		}},
		{"remove a member of a private room", func(e *liveEnv) liveResult {
			err := e.rooms.RemoveMember(e.t.Context(), e.as("root"), e.den.ID, e.uid("bob"))
			return liveResult{err: err, want: memberEvts(live.KindMemberLeft, e.den.ID, e.uid("bob"))}
		}},
		{"remove a user who is not a member", func(e *liveEnv) liveResult {
			return liveResult{err: e.rooms.RemoveMember(e.t.Context(), e.as("admin"), e.lobby.ID, e.uid("dave"))}
		}},
		{"remove by a member who is not an admin", func(e *liveEnv) liveResult {
			return liveResult{err: e.rooms.RemoveMember(e.t.Context(), e.as("bob"), e.lobby.ID, e.uid("admin")), tag: app.TagForbidden}
		}},
		{"update", func(e *liveEnv) liveResult {
			err := e.rooms.Update(e.t.Context(), e.as("admin"), e.lobby.ID, app.RoomInput{Name: "lobby2", Description: "Everyone"})
			return liveResult{err: err, want: []live.Event{{Topic: live.RoomTopic(e.lobby.ID), Kind: live.KindRoomUpdated, RoomID: e.lobby.ID}}}
		}},
		{"update the description only", func(e *liveEnv) liveResult {
			err := e.rooms.Update(e.t.Context(), e.as("alice"), e.den.ID, app.RoomInput{Name: "den", Description: "Quiet"})
			return liveResult{err: err, want: []live.Event{{Topic: live.RoomTopic(e.den.ID), Kind: live.KindRoomUpdated, RoomID: e.den.ID}}}
		}},
		{"update with nothing changed", func(e *liveEnv) liveResult {
			return liveResult{err: e.rooms.Update(e.t.Context(), e.as("admin"), e.lobby.ID, app.RoomInput{Name: "lobby", Description: "Everyone"})}
		}},
		{"update with a mistake", func(e *liveEnv) liveResult {
			return liveResult{err: e.rooms.Update(e.t.Context(), e.as("admin"), e.lobby.ID, app.RoomInput{Name: ""}), tag: app.TagInvalidInput}
		}},
		{"update with a taken name", func(e *liveEnv) liveResult {
			return liveResult{err: e.rooms.Update(e.t.Context(), e.as("admin"), e.lobby.ID, app.RoomInput{Name: "den"}), tag: app.TagInvalidInput}
		}},
		{"update by someone who may not", func(e *liveEnv) liveResult {
			return liveResult{err: e.rooms.Update(e.t.Context(), e.as("bob"), e.lobby.ID, app.RoomInput{Name: "x"}), tag: app.TagForbidden}
		}},
		{"delete tells the room and every former member", func(e *liveEnv) liveResult {
			// lobby: admin and bob, in the order of their display names.
			err := e.rooms.Delete(e.t.Context(), e.as("admin"), e.lobby.ID)
			return liveResult{err: err, want: []live.Event{
				{Topic: live.RoomTopic(e.lobby.ID), Kind: live.KindRoomDeleted, RoomID: e.lobby.ID},
				{Topic: live.UserTopic(e.uid("admin")), Kind: live.KindRoomDeleted, RoomID: e.lobby.ID, UserID: e.uid("admin")},
				{Topic: live.UserTopic(e.uid("bob")), Kind: live.KindRoomDeleted, RoomID: e.lobby.ID, UserID: e.uid("bob")},
			}}
		}},
		{"delete a private room by an admin who is not a member", func(e *liveEnv) liveResult {
			err := e.rooms.Delete(e.t.Context(), e.as("root"), e.den.ID)
			return liveResult{err: err, want: []live.Event{
				{Topic: live.RoomTopic(e.den.ID), Kind: live.KindRoomDeleted, RoomID: e.den.ID},
				{Topic: live.UserTopic(e.uid("alice")), Kind: live.KindRoomDeleted, RoomID: e.den.ID, UserID: e.uid("alice")},
				{Topic: live.UserTopic(e.uid("bob")), Kind: live.KindRoomDeleted, RoomID: e.den.ID, UserID: e.uid("bob")},
			}}
		}},
		{"delete a room with no members", func(e *liveEnv) liveResult {
			if err := e.rooms.Leave(e.t.Context(), e.as("alice"), e.den.ID); err != nil {
				e.t.Fatalf("Leave: %+v", err)
			}
			if err := e.rooms.Leave(e.t.Context(), e.as("bob"), e.den.ID); err != nil {
				e.t.Fatalf("Leave: %+v", err)
			}
			e.rec.Reset()
			err := e.rooms.Delete(e.t.Context(), e.as("root"), e.den.ID)
			return liveResult{err: err, want: []live.Event{{Topic: live.RoomTopic(e.den.ID), Kind: live.KindRoomDeleted, RoomID: e.den.ID}}}
		}},
		{"delete by someone who may not", func(e *liveEnv) liveResult {
			return liveResult{err: e.rooms.Delete(e.t.Context(), e.as("bob"), e.lobby.ID), tag: app.TagForbidden}
		}},
		{"delete a private room of others", func(e *liveEnv) liveResult {
			return liveResult{err: e.rooms.Delete(e.t.Context(), e.as("carol"), e.den.ID), tag: app.TagNotFound}
		}},
		{"delete a missing room", func(e *liveEnv) liveResult {
			return liveResult{err: e.rooms.Delete(e.t.Context(), e.as("admin"), 9999), tag: app.TagNotFound}
		}},
	})
}

// postEvts are the events of a top-level post.
func postEvts(kind string, room, post uint64) []live.Event {
	return []live.Event{
		{Topic: live.RoomTopic(room), Kind: kind, RoomID: room, PostID: post},
		{Topic: live.PostTopic(post), Kind: kind, RoomID: room, PostID: post},
	}
}

// commentEvts are the events of a comment: its thread, and the room as an
// update of the root.
func commentEvts(kind string, room, root, comment uint64) []live.Event {
	return []live.Event{
		{Topic: live.PostTopic(root), Kind: kind, RoomID: room, PostID: comment, RootID: root},
		{Topic: live.RoomTopic(room), Kind: live.KindPostUpdated, RoomID: room, PostID: root, RootID: root},
	}
}

func textInput(body string) app.PostInput {
	return app.PostInput{Format: app.FormatText, Body: body}
}

func TestLivePostsPublish(t *testing.T) {
	runLiveCases(t, []liveCase{
		{"create a post", func(e *liveEnv) liveResult {
			p, err := e.posts.Create(e.t.Context(), e.as("bob"), e.lobby.ID, textInput("hello"))
			return liveResult{err: err, want: postEvts(live.KindPostCreated, e.lobby.ID, p.ID)}
		}},
		{"create a post in a private room", func(e *liveEnv) liveResult {
			p, err := e.posts.Create(e.t.Context(), e.as("alice"), e.den.ID, textInput("hello"))
			return liveResult{err: err, want: postEvts(live.KindPostCreated, e.den.ID, p.ID)}
		}},
		{"create with a mistake", func(e *liveEnv) liveResult {
			_, err := e.posts.Create(e.t.Context(), e.as("bob"), e.lobby.ID, textInput(""))
			return liveResult{err: err, tag: app.TagInvalidInput}
		}},
		{"create without having joined", func(e *liveEnv) liveResult {
			_, err := e.posts.Create(e.t.Context(), e.as("carol"), e.lobby.ID, textInput("hello"))
			return liveResult{err: err, tag: app.TagForbidden}
		}},
		{"create in a private room of others", func(e *liveEnv) liveResult {
			_, err := e.posts.Create(e.t.Context(), e.as("carol"), e.den.ID, textInput("hello"))
			return liveResult{err: err, tag: app.TagNotFound}
		}},
		{"reply to a post", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			e.rec.Reset()
			c, err := e.posts.Reply(e.t.Context(), e.as("admin"), p.ID, textInput("comment"))
			return liveResult{err: err, want: commentEvts(live.KindCommentCreated, e.lobby.ID, p.ID, c.ID)}
		}},
		{"reply to a comment goes to the root thread", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			c1 := e.say("admin", p.ID, "one")
			c2 := e.say("bob", c1.ID, "two")
			e.rec.Reset()
			c3, err := e.posts.Reply(e.t.Context(), e.as("admin"), c2.ID, textInput("three"))
			return liveResult{err: err, want: commentEvts(live.KindCommentCreated, e.lobby.ID, p.ID, c3.ID)}
		}},
		{"reply with a mistake", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			e.rec.Reset()
			_, err := e.posts.Reply(e.t.Context(), e.as("admin"), p.ID, textInput(""))
			return liveResult{err: err, tag: app.TagInvalidInput}
		}},
		{"reply without having joined", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			e.rec.Reset()
			_, err := e.posts.Reply(e.t.Context(), e.as("carol"), p.ID, textInput("x"))
			return liveResult{err: err, tag: app.TagForbidden}
		}},
		{"reply to a post of a private room of others", func(e *liveEnv) liveResult {
			p := e.write("alice", "den", "post")
			e.rec.Reset()
			_, err := e.posts.Reply(e.t.Context(), e.as("carol"), p.ID, textInput("x"))
			return liveResult{err: err, tag: app.TagNotFound}
		}},
		{"reply to a missing post", func(e *liveEnv) liveResult {
			_, err := e.posts.Reply(e.t.Context(), e.as("bob"), 9999, textInput("x"))
			return liveResult{err: err, tag: app.TagNotFound}
		}},
		{"edit a post", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			e.rec.Reset()
			_, err := e.posts.Edit(e.t.Context(), e.as("bob"), p.ID, textInput("edited"))
			return liveResult{err: err, want: postEvts(live.KindPostUpdated, e.lobby.ID, p.ID)}
		}},
		{"edit the tags only", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			e.rec.Reset()
			in := textInput("post")
			in.Tags = []string{"news"}
			_, err := e.posts.Edit(e.t.Context(), e.as("bob"), p.ID, in)
			return liveResult{err: err, want: postEvts(live.KindPostUpdated, e.lobby.ID, p.ID)}
		}},
		{"edit a comment", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			c := e.say("bob", p.ID, "comment")
			e.rec.Reset()
			_, err := e.posts.Edit(e.t.Context(), e.as("bob"), c.ID, textInput("edited"))
			return liveResult{err: err, want: commentEvts(live.KindCommentUpdated, e.lobby.ID, p.ID, c.ID)}
		}},
		{"edit with nothing changed", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			e.rec.Reset()
			_, err := e.posts.Edit(e.t.Context(), e.as("bob"), p.ID, textInput("post"))
			return liveResult{err: err}
		}},
		{"edit with a mistake", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			e.rec.Reset()
			_, err := e.posts.Edit(e.t.Context(), e.as("bob"), p.ID, textInput(""))
			return liveResult{err: err, tag: app.TagInvalidInput}
		}},
		{"edit by someone who is not the author", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			e.rec.Reset()
			_, err := e.posts.Edit(e.t.Context(), e.as("admin"), p.ID, textInput("edited"))
			return liveResult{err: err, tag: app.TagForbidden}
		}},
		{"edit a post of a private room of others", func(e *liveEnv) liveResult {
			p := e.write("alice", "den", "post")
			e.rec.Reset()
			_, err := e.posts.Edit(e.t.Context(), e.as("carol"), p.ID, textInput("edited"))
			return liveResult{err: err, tag: app.TagNotFound}
		}},
		{"delete a post", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			e.rec.Reset()
			err := e.posts.Delete(e.t.Context(), e.as("bob"), p.ID)
			return liveResult{err: err, want: postEvts(live.KindPostDeleted, e.lobby.ID, p.ID)}
		}},
		{"an admin deletes a post", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			e.rec.Reset()
			err := e.posts.Delete(e.t.Context(), e.as("admin"), p.ID)
			return liveResult{err: err, want: postEvts(live.KindPostDeleted, e.lobby.ID, p.ID)}
		}},
		{"delete a post that stays as a placeholder", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			e.say("admin", p.ID, "comment")
			e.rec.Reset()
			err := e.posts.Delete(e.t.Context(), e.as("bob"), p.ID)
			return liveResult{err: err, want: postEvts(live.KindPostDeleted, e.lobby.ID, p.ID)}
		}},
		{"delete a comment", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			c := e.say("bob", p.ID, "comment")
			e.rec.Reset()
			err := e.posts.Delete(e.t.Context(), e.as("bob"), c.ID)
			return liveResult{err: err, want: commentEvts(live.KindCommentDeleted, e.lobby.ID, p.ID, c.ID)}
		}},
		{"delete a comment that stays as a placeholder", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			c1 := e.say("bob", p.ID, "one")
			e.say("admin", c1.ID, "two")
			e.rec.Reset()
			err := e.posts.Delete(e.t.Context(), e.as("bob"), c1.ID)
			return liveResult{err: err, want: commentEvts(live.KindCommentDeleted, e.lobby.ID, p.ID, c1.ID)}
		}},
		{"a comment whose removal leaves the deleted root empty takes the root with it", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			c := e.say("admin", p.ID, "comment")
			if err := e.posts.Delete(e.t.Context(), e.as("bob"), p.ID); err != nil {
				e.t.Fatalf("Delete root: %+v", err)
			}
			e.rec.Reset()
			err := e.posts.Delete(e.t.Context(), e.as("admin"), c.ID)
			return liveResult{err: err, want: []live.Event{
				{Topic: live.PostTopic(p.ID), Kind: live.KindCommentDeleted, RoomID: e.lobby.ID, PostID: c.ID, RootID: p.ID},
				{Topic: live.RoomTopic(e.lobby.ID), Kind: live.KindPostDeleted, RoomID: e.lobby.ID, PostID: p.ID},
				{Topic: live.PostTopic(p.ID), Kind: live.KindPostDeleted, RoomID: e.lobby.ID, PostID: p.ID},
			}}
		}},
		{"a chain of placeholders goes up to the root", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			c1 := e.say("bob", p.ID, "one")
			c2 := e.say("admin", c1.ID, "two")
			for _, id := range []uint64{p.ID, c1.ID} {
				if err := e.posts.Delete(e.t.Context(), e.as("admin"), id); err != nil {
					e.t.Fatalf("Delete %d: %+v", id, err)
				}
			}
			e.rec.Reset()
			err := e.posts.Delete(e.t.Context(), e.as("admin"), c2.ID)
			return liveResult{err: err, want: []live.Event{
				{Topic: live.PostTopic(p.ID), Kind: live.KindCommentDeleted, RoomID: e.lobby.ID, PostID: c2.ID, RootID: p.ID},
				{Topic: live.RoomTopic(e.lobby.ID), Kind: live.KindPostDeleted, RoomID: e.lobby.ID, PostID: p.ID},
				{Topic: live.PostTopic(p.ID), Kind: live.KindPostDeleted, RoomID: e.lobby.ID, PostID: p.ID},
			}}
		}},
		{"a placeholder that still has replies stays", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			c1 := e.say("admin", p.ID, "one")
			e.say("bob", p.ID, "two")
			if err := e.posts.Delete(e.t.Context(), e.as("bob"), p.ID); err != nil {
				e.t.Fatalf("Delete root: %+v", err)
			}
			e.rec.Reset()
			err := e.posts.Delete(e.t.Context(), e.as("admin"), c1.ID)
			return liveResult{err: err, want: commentEvts(live.KindCommentDeleted, e.lobby.ID, p.ID, c1.ID)}
		}},
		{"delete by someone who is not the author", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			e.rec.Reset()
			return liveResult{err: e.posts.Delete(e.t.Context(), e.as("carol"), p.ID), tag: app.TagForbidden}
		}},
		{"delete a post of a private room of others", func(e *liveEnv) liveResult {
			p := e.write("alice", "den", "post")
			e.rec.Reset()
			return liveResult{err: e.posts.Delete(e.t.Context(), e.as("carol"), p.ID), tag: app.TagNotFound}
		}},
		{"delete a post twice", func(e *liveEnv) liveResult {
			p := e.write("bob", "lobby", "post")
			e.say("admin", p.ID, "keeps it as a placeholder")
			if err := e.posts.Delete(e.t.Context(), e.as("bob"), p.ID); err != nil {
				e.t.Fatalf("Delete: %+v", err)
			}
			e.rec.Reset()
			return liveResult{err: e.posts.Delete(e.t.Context(), e.as("bob"), p.ID), tag: app.TagNotFound}
		}},
	})
}

// probe is a Publisher that runs a function on every event, so that a test
// can look at the database at the moment of the call.
type probe struct {
	fn func(live.Event)
}

// Publish runs the function.
func (p *probe) Publish(ev live.Event) { p.fn(ev) }

// TestLivePublishesAfterTheCommit checks that the change can be read when the
// event arrives, which cannot be the case inside the transaction.
func TestLivePublishesAfterTheCommit(t *testing.T) {
	e := newLiveEnv(t)
	pr := &probe{}
	// Inside a transaction a read waits for the lock; the timeout makes that
	// a failure and not a hang.
	readCtx := func() context.Context {
		ctx, cancel := context.WithTimeout(e.t.Context(), 3*time.Second)
		e.t.Cleanup(cancel)
		return ctx
	}
	e.posts = app.NewPosts(e.st, e.clk, markdown.New(), e.files, app.PostsConfig{
		MaxAttachmentSize: testMaxUpload, MaxAttachments: testMaxAttachments,
	}, pr)
	e.rooms = app.NewRooms(e.st, e.files, nil, pr)

	t.Run("post", func(t *testing.T) {
		readable := 0
		pr.fn = func(ev live.Event) {
			rows, err := e.st.ListFeed(readCtx(), e.lobby.ID, 0, 10)
			if err != nil {
				t.Errorf("ListFeed inside Publish: %+v", err)
				return
			}
			if len(rows) == 1 && rows[0].ID == ev.PostID && rows[0].Body == "committed" {
				readable++
			}
		}
		if _, err := e.posts.Create(e.t.Context(), e.as("bob"), e.lobby.ID, textInput("committed")); err != nil {
			t.Fatalf("Create: %+v", err)
		}
		if readable != 2 {
			t.Errorf("the post was readable in %d of the 2 Publish calls", readable)
		}
	})
	t.Run("room", func(t *testing.T) {
		var names []string
		pr.fn = func(live.Event) {
			room, err := e.st.RoomByID(readCtx(), e.lobby.ID)
			if err != nil {
				t.Errorf("RoomByID inside Publish: %+v", err)
				return
			}
			names = append(names, room.Name)
		}
		if err := e.rooms.Update(e.t.Context(), e.as("admin"), e.lobby.ID, app.RoomInput{Name: "renamed"}); err != nil {
			t.Fatalf("Update: %+v", err)
		}
		if !reflect.DeepEqual(names, []string{"renamed"}) {
			t.Errorf("the name seen by Publish was %q, want [renamed]", names)
		}
	})
}

func TestLiveNilPublisher(t *testing.T) {
	e := newPostsEnv(t) // both services were made with a nil Publisher
	p := e.write("bob", "lobby", "post")
	e.say("admin", p.ID, "comment")
	if err := e.rooms.Join(e.t.Context(), e.as("dave"), e.lobby.ID); err != nil {
		t.Fatalf("Join: %+v", err)
	}
	if err := e.posts.Delete(e.t.Context(), e.as("bob"), p.ID); err != nil {
		t.Fatalf("Delete: %+v", err)
	}
}

// TestLiveStoreErrorPublishesNothing closes the database under every method
// that publishes: the transaction cannot begin, and nothing is published.
func TestLiveStoreErrorPublishesNothing(t *testing.T) {
	calls := []struct {
		name string
		call func(e *liveEnv, p, c app.Post) error
	}{
		{"Rooms.Join", func(e *liveEnv, _, _ app.Post) error { return e.rooms.Join(e.t.Context(), e.as("dave"), e.lobby.ID) }},
		{"Rooms.Leave", func(e *liveEnv, _, _ app.Post) error { return e.rooms.Leave(e.t.Context(), e.as("bob"), e.lobby.ID) }},
		{"Rooms.AddMember", func(e *liveEnv, _, _ app.Post) error {
			return e.rooms.AddMember(e.t.Context(), e.as("bob"), e.lobby.ID, e.uid("dave"))
		}},
		{"Rooms.RemoveMember", func(e *liveEnv, _, _ app.Post) error {
			return e.rooms.RemoveMember(e.t.Context(), e.as("admin"), e.lobby.ID, e.uid("bob"))
		}},
		{"Rooms.Update", func(e *liveEnv, _, _ app.Post) error {
			return e.rooms.Update(e.t.Context(), e.as("admin"), e.lobby.ID, app.RoomInput{Name: "renamed"})
		}},
		{"Rooms.Delete", func(e *liveEnv, _, _ app.Post) error { return e.rooms.Delete(e.t.Context(), e.as("admin"), e.lobby.ID) }},
		{"Posts.Create", func(e *liveEnv, _, _ app.Post) error {
			_, err := e.posts.Create(e.t.Context(), e.as("bob"), e.lobby.ID, textInput("x"))
			return err
		}},
		{"Posts.Reply", func(e *liveEnv, p, _ app.Post) error {
			_, err := e.posts.Reply(e.t.Context(), e.as("bob"), p.ID, textInput("x"))
			return err
		}},
		{"Posts.Edit", func(e *liveEnv, p, _ app.Post) error {
			_, err := e.posts.Edit(e.t.Context(), e.as("bob"), p.ID, textInput("edited"))
			return err
		}},
		{"Posts.Delete", func(e *liveEnv, p, _ app.Post) error { return e.posts.Delete(e.t.Context(), e.as("bob"), p.ID) }},
		{"Posts.Delete comment", func(e *liveEnv, _, c app.Post) error { return e.posts.Delete(e.t.Context(), e.as("bob"), c.ID) }},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			e := newLiveEnv(t)
			p := e.write("bob", "lobby", "post")
			c := e.say("bob", p.ID, "comment")
			e.rec.Reset()
			if err := e.st.Close(); err != nil {
				t.Fatalf("Close: %+v", err)
			}
			if err := tc.call(e, p, c); err == nil {
				t.Fatalf("the call succeeded on a closed database")
			}
			if got := e.rec.Events(); len(got) != 0 {
				t.Errorf("published %+v after a store error", got)
			}
		})
	}
}
