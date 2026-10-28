package app_test

import (
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/live"
)

// followers are the users of the fixture whose follow state the tests ask
// about (the five members of the lobby).
var followers = []string{"admin", "alice", "bob", "carol", "dave"}

// following asks whether the user follows the post or the thread of the
// comment, through Posts.Get.
func (e *mentionEnv) following(user string, postID uint64) bool {
	e.t.Helper()
	p, err := e.posts.Get(e.t.Context(), e.as(user), postID)
	if err != nil {
		e.t.Fatalf("Get %d as %s: %+v", postID, user, err)
	}
	return p.Following
}

// requireFollowing fails the test unless exactly the users in want follow
// the thread of the post (alice, admin, bob, carol and dave are asked).
func (e *mentionEnv) requireFollowing(postID uint64, want ...string) {
	e.t.Helper()
	for _, name := range followers {
		if got, w := e.following(name, postID), slices.Contains(want, name); got != w {
			e.t.Errorf("%s follows post %d = %v, want %v", name, postID, got, w)
		}
	}
}

// follow has the user follow, or unfollow, the post.
func (e *mentionEnv) follow(user string, postID uint64, on bool) {
	e.t.Helper()
	var err error
	if on {
		err = e.posts.Follow(e.t.Context(), e.as(user), postID)
	} else {
		err = e.posts.Unfollow(e.t.Context(), e.as(user), postID)
	}
	if err != nil {
		e.t.Fatalf("follow=%v %s: %+v", on, user, err)
	}
}

// direct puts the users on "Only direct replies".
func direct(users ...string) envStep {
	return func(e *mentionEnv) {
		for _, u := range users {
			e.settings(u, app.SettingsUpdate{Replies: ptr(app.RepliesDirect)})
		}
	}
}

func TestAutomaticFollows(t *testing.T) {
	t.Parallel()
	text := app.FormatText
	tests := []struct {
		name string
		// run does the writing and returns the top-level post to ask about.
		run  func(e *mentionEnv) uint64
		want []string
	}{
		{"writing a post follows it", func(e *mentionEnv) uint64 {
			return e.post("admin", "lobby", text, "hello").ID
		}, []string{"admin"}},
		{"commenting follows the thread", func(e *mentionEnv) uint64 {
			p := e.post("admin", "lobby", text, "hello")
			e.comment("bob", p.ID, text, "hi")
			return p.ID
		}, []string{"admin", "bob"}},
		{"commenting at any depth follows the root", func(e *mentionEnv) uint64 {
			p := e.post("admin", "lobby", text, "hello")
			c1 := e.comment("bob", p.ID, text, "one")
			c2 := e.comment("carol", c1.ID, text, "two")
			e.comment("dave", c2.ID, text, "three")
			return p.ID
		}, []string{"admin", "bob", "carol", "dave"}},
		{"Following of a comment tells the state of the root", func(e *mentionEnv) uint64 {
			p := e.post("admin", "lobby", text, "hello")
			c := e.comment("bob", p.ID, text, "one")
			// Asking about the comment answers for the root.
			if !e.following("bob", c.ID) || e.following("carol", c.ID) {
				e.t.Errorf("Following of the comment is not the root's state")
			}
			return p.ID
		}, []string{"admin", "bob"}},
		{"being named in a post follows it", func(e *mentionEnv) uint64 {
			return e.post("admin", "lobby", text, "see @bob").ID
		}, []string{"admin", "bob"}},
		{"being named in a markdown post follows it", func(e *mentionEnv) uint64 {
			return e.post("admin", "lobby", app.FormatMarkdown, "**see** @bob").ID
		}, []string{"admin", "bob"}},
		{"being named in a comment follows the root", func(e *mentionEnv) uint64 {
			p := e.post("admin", "lobby", text, "hello")
			e.comment("carol", p.ID, text, "ask @dave")
			return p.ID
		}, []string{"admin", "carol", "dave"}},
		{"being named in a deep comment follows the root", func(e *mentionEnv) uint64 {
			p := e.post("admin", "lobby", text, "hello")
			c1 := e.comment("bob", p.ID, text, "one")
			e.comment("carol", c1.ID, text, "ask @alice")
			return p.ID
		}, []string{"admin", "alice", "bob", "carol"}},
		{"a group mention does not follow", func(e *mentionEnv) uint64 {
			return e.post("admin", "lobby", text, "@room").ID
		}, []string{"admin"}},
		{"@all and @here do not follow either", func(e *mentionEnv) uint64 {
			e.connect("bob", "carol")
			return e.post("admin", "lobby", text, "@all @here").ID
		}, []string{"admin"}},
		{"a group mention in a comment does not follow", func(e *mentionEnv) uint64 {
			p := e.post("admin", "lobby", text, "hello")
			e.comment("bob", p.ID, text, "@room")
			return p.ID
		}, []string{"admin", "bob"}},
		{"a keyword does not follow", func(e *mentionEnv) uint64 {
			e.keywords("carol", "feeder")
			return e.post("admin", "lobby", text, "the feeder").ID
		}, []string{"admin"}},
		{"a name next to a group mention follows only the named", func(e *mentionEnv) uint64 {
			return e.post("admin", "lobby", text, "@room @carol").ID
		}, []string{"admin", "carol"}},
		{"editing a post to mention someone follows them", func(e *mentionEnv) uint64 {
			p := e.post("admin", "lobby", text, "hello")
			e.edit("admin", p, "hello @bob")
			return p.ID
		}, []string{"admin", "bob"}},
		{"editing a comment to mention someone follows the root", func(e *mentionEnv) uint64 {
			p := e.post("admin", "lobby", text, "hello")
			c := e.comment("carol", p.ID, text, "one")
			e.edit("carol", c, "one @dave")
			return p.ID
		}, []string{"admin", "carol", "dave"}},
		{"editing a deep comment to mention someone follows the root", func(e *mentionEnv) uint64 {
			p := e.post("admin", "lobby", text, "hello")
			c1 := e.comment("bob", p.ID, text, "one")
			c2 := e.comment("carol", c1.ID, text, "two")
			e.edit("carol", c2, "two @alice")
			return p.ID
		}, []string{"admin", "alice", "bob", "carol"}},
		{"editing a post to add a group mention does not follow", func(e *mentionEnv) uint64 {
			p := e.post("admin", "lobby", text, "hello")
			e.edit("admin", p, "hello @room")
			return p.ID
		}, []string{"admin"}},
		{"editing a post to add a keyword does not follow", func(e *mentionEnv) uint64 {
			e.keywords("dave", "feeder")
			p := e.post("admin", "lobby", text, "hello")
			e.edit("admin", p, "the feeder")
			return p.ID
		}, []string{"admin"}},
		{"removing the mention in an edit keeps the follow", func(e *mentionEnv) uint64 {
			p := e.post("admin", "lobby", text, "hello @bob")
			e.edit("admin", p, "hello")
			return p.ID
		}, []string{"admin", "bob"}},
		{"another thread is not followed", func(e *mentionEnv) uint64 {
			p := e.post("admin", "lobby", text, "one")
			other := e.post("bob", "lobby", text, "two")
			e.comment("carol", other.ID, text, "a comment elsewhere")
			return p.ID
		}, []string{"admin"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newMentionEnv(t)
			root := tt.run(e)
			e.requireFollowing(root, tt.want...)
		})
	}
}

func TestNamingSomeoneWhoCannotReadDoesNotFollow(t *testing.T) {
	t.Parallel()
	e := newMentionEnv(t)
	// carol is no member of the den, so her name is not saved as a mention.
	p := e.post("alice", "den", app.FormatText, "psst @carol @bob")
	for name, want := range map[string]bool{"alice": true, "bob": true} {
		if got := e.following(name, p.ID); got != want {
			t.Errorf("%s follows = %v, want %v", name, got, want)
		}
	}
	rows, err := e.st.FollowingPosts(t.Context(), e.uid("carol"), []uint64{p.ID})
	if err != nil || len(rows) != 0 {
		t.Errorf("carol follows %v, %v; want nothing", rows, err)
	}
}

func TestAnAutomaticFollowDoesNotUndoAnUnfollow(t *testing.T) {
	t.Parallel()
	text := app.FormatText
	tests := []struct {
		name string
		// after does what would follow the user automatically.
		after func(e *mentionEnv, p app.Post)
	}{
		{"commenting again", func(e *mentionEnv, p app.Post) { e.comment("bob", p.ID, text, "again") }},
		{"commenting deeper", func(e *mentionEnv, p app.Post) {
			c := e.comment("carol", p.ID, text, "carol")
			e.comment("bob", c.ID, text, "deeper")
		}},
		{"being named in a comment", func(e *mentionEnv, p app.Post) { e.comment("carol", p.ID, text, "@bob") }},
		{"being named in an edit", func(e *mentionEnv, p app.Post) {
			c := e.comment("carol", p.ID, text, "carol")
			e.edit("carol", c, "carol @bob")
		}},
		{"a group mention", func(e *mentionEnv, p app.Post) { e.comment("carol", p.ID, text, "@room") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newMentionEnv(t)
			p := e.post("admin", "lobby", text, "hello")
			e.comment("bob", p.ID, text, "first")
			e.requireFollowing(p.ID, "admin", "bob")
			e.follow("bob", p.ID, false)
			e.requireFollowing(p.ID, "admin")
			tt.after(e, p)
			if e.following("bob", p.ID) {
				t.Error("an automatic follow brought the follow back")
			}
			e.follow("bob", p.ID, true)
			if !e.following("bob", p.ID) {
				t.Error("Follow did not bring the follow back")
			}
		})
	}
}

func TestFollowingIsOnThePostsOfTheServices(t *testing.T) {
	t.Parallel()
	e := newMentionEnv(t)
	text := app.FormatText
	p := e.post("admin", "lobby", text, "thread one")
	c1 := e.comment("bob", p.ID, text, "one")
	c2 := e.comment("carol", c1.ID, text, "two")
	other := e.post("dave", "lobby", text, "thread two")

	check := func(user string, wantP, wantOther bool) {
		t.Helper()
		// Get: the post, a comment and a deep comment.
		for _, id := range []uint64{p.ID, c1.ID, c2.ID} {
			if got := e.following(user, id); got != wantP {
				t.Errorf("Get(%d) as %s: Following = %v, want %v", id, user, got, wantP)
			}
		}
		if got := e.following(user, other.ID); got != wantOther {
			t.Errorf("Get(other) as %s: Following = %v, want %v", user, got, wantOther)
		}
		// Feed.
		page, err := e.posts.Feed(t.Context(), e.as(user), e.lobby.ID, 0, 50)
		if err != nil {
			t.Fatalf("Feed: %+v", err)
		}
		seen := 0
		for _, post := range page.Posts {
			switch post.ID {
			case p.ID:
				seen++
				if post.Following != wantP {
					t.Errorf("Feed as %s: Following of the post = %v, want %v", user, post.Following, wantP)
				}
			case other.ID:
				seen++
				if post.Following != wantOther {
					t.Errorf("Feed as %s: Following of the other = %v, want %v", user, post.Following, wantOther)
				}
			}
		}
		if seen != 2 {
			t.Errorf("Feed as %s shows %d of the two posts", user, seen)
		}
		// Thread: the root and every comment tell the root's state, from the
		// root or from a comment.
		for _, from := range []uint64{p.ID, c2.ID} {
			th, err := e.posts.Thread(t.Context(), e.as(user), from)
			if err != nil {
				t.Fatalf("Thread: %+v", err)
			}
			if th.Root.Following != wantP || len(th.Comments) != 2 {
				t.Errorf("Thread(%d) as %s: root Following = %v with %d comments, want %v with 2", from, user, th.Root.Following, len(th.Comments), wantP)
			}
			for _, c := range th.Comments {
				if c.Following != wantP {
					t.Errorf("Thread(%d) as %s: comment %d Following = %v, want %v", from, user, c.ID, c.Following, wantP)
				}
			}
		}
	}
	check("admin", true, false)
	check("bob", true, false)
	check("carol", true, false)
	check("dave", false, true)
	check("alice", false, false)

	e.follow("alice", p.ID, true)
	check("alice", true, false)
	e.follow("alice", c2.ID, false) // a comment acts on its root
	check("alice", false, false)
	e.follow("dave", p.ID, true)
	check("dave", true, true)
	e.follow("dave", other.ID, false)
	check("dave", true, false)
}

func TestFollowAndUnfollow(t *testing.T) {
	t.Parallel()
	type op struct {
		user   string
		follow bool
		// onComment acts on the comment instead of the post.
		onComment bool
	}
	tests := []struct {
		name string
		ops  []op
		want []string
	}{
		{"follow", []op{{"carol", true, false}}, []string{"admin", "carol"}},
		{"follow twice", []op{{"carol", true, false}, {"carol", true, false}}, []string{"admin", "carol"}},
		{"unfollow when not following", []op{{"carol", false, false}}, []string{"admin"}},
		{"unfollow twice", []op{{"admin", false, false}, {"admin", false, false}}, nil},
		{"follow and unfollow", []op{{"carol", true, false}, {"carol", false, false}}, []string{"admin"}},
		{"unfollow, then follow again", []op{{"admin", false, false}, {"admin", true, false}}, []string{"admin"}},
		{"unfollow, follow, unfollow, follow", []op{{"admin", false, false}, {"admin", true, false}, {"admin", false, false}, {"admin", true, false}}, []string{"admin"}},
		{"follow on a comment acts on the post", []op{{"carol", true, true}}, []string{"admin", "carol"}},
		{"unfollow on a comment acts on the post", []op{{"admin", false, true}}, nil},
		{"unfollow on a comment, follow on the post", []op{{"admin", false, true}, {"admin", true, false}}, []string{"admin"}},
		{"several users", []op{{"carol", true, false}, {"dave", true, true}, {"admin", false, false}}, []string{"carol", "dave"}},
		{"one user's unfollow leaves the others", []op{{"carol", true, false}, {"carol", false, false}, {"dave", true, false}}, []string{"admin", "dave"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newMentionEnv(t)
			p := e.post("admin", "lobby", app.FormatText, "hello")
			c := e.comment("bob", p.ID, app.FormatText, "a comment")
			e.follow("bob", p.ID, false) // the comment made bob a follower
			e.settle()
			audits := len(e.events())
			for _, o := range tt.ops {
				id := p.ID
				if o.onComment {
					id = c.ID
				}
				e.follow(o.user, id, o.follow)
			}
			e.requireFollowing(p.ID, tt.want...)
			e.requireFollowing(c.ID, tt.want...)
			if n := len(e.events()); n != audits {
				t.Errorf("following wrote %d audit events", n-audits)
			}
			if ev := e.rec.Events(); len(ev) != 0 {
				t.Errorf("following published %+v", ev)
			}
			for name := range e.users {
				if n := e.news(name); len(n) != 0 {
					t.Errorf("following notified %s: %+v", name, n)
				}
			}
		})
	}
}

func TestFollowPermissions(t *testing.T) {
	t.Parallel()
	const notFnd = app.TagNotFound
	text := app.FormatText
	targets := []struct {
		name string
		// make returns the ID to follow.
		make func(e *mentionEnv) uint64
		// want is the tag by caller; a caller who is not listed succeeds. The
		// disabled user and nobody are always Unauthenticated.
		want map[string]string
	}{
		{"post of a visible room", func(e *mentionEnv) uint64 { return e.post("bob", "lobby", text, "hi").ID },
			nil},
		{"comment in a visible room", func(e *mentionEnv) uint64 {
			p := e.post("bob", "lobby", text, "hi")
			return e.comment("carol", p.ID, text, "c").ID
		}, nil},
		{"post of a private room (den: alice, bob)", func(e *mentionEnv) uint64 { return e.post("alice", "den", text, "psst").ID },
			map[string]string{"admin": notFnd, "root": notFnd, "carol": notFnd, "dave": notFnd}},
		{"comment in a private room", func(e *mentionEnv) uint64 {
			p := e.post("alice", "den", text, "psst")
			return e.comment("bob", p.ID, text, "c").ID
		}, map[string]string{"admin": notFnd, "root": notFnd, "carol": notFnd, "dave": notFnd}},
		{"a post that does not exist", func(*mentionEnv) uint64 { return 99999 },
			map[string]string{"admin": notFnd, "root": notFnd, "alice": notFnd, "bob": notFnd, "carol": notFnd, "dave": notFnd}},
		{"a deleted post that stays for its replies", func(e *mentionEnv) uint64 {
			p := e.post("bob", "lobby", text, "hi")
			e.comment("carol", p.ID, text, "c")
			if err := e.posts.Delete(e.t.Context(), e.as("bob"), p.ID); err != nil {
				e.t.Fatalf("Delete: %+v", err)
			}
			return p.ID
		}, map[string]string{"admin": notFnd, "root": notFnd, "alice": notFnd, "bob": notFnd, "carol": notFnd, "dave": notFnd}},
		{"a comment of a deleted post", func(e *mentionEnv) uint64 {
			p := e.post("bob", "lobby", text, "hi")
			c := e.comment("carol", p.ID, text, "c")
			if err := e.posts.Delete(e.t.Context(), e.as("bob"), p.ID); err != nil {
				e.t.Fatalf("Delete: %+v", err)
			}
			return c.ID
		}, map[string]string{"admin": notFnd, "root": notFnd, "alice": notFnd, "bob": notFnd, "carol": notFnd, "dave": notFnd}},
		{"a post deleted without replies", func(e *mentionEnv) uint64 {
			p := e.post("bob", "lobby", text, "hi")
			if err := e.posts.Delete(e.t.Context(), e.as("bob"), p.ID); err != nil {
				e.t.Fatalf("Delete: %+v", err)
			}
			return p.ID
		}, map[string]string{"admin": notFnd, "root": notFnd, "alice": notFnd, "bob": notFnd, "carol": notFnd, "dave": notFnd}},
	}
	for _, tg := range targets {
		for _, who := range []string{"admin", "root", "alice", "bob", "carol", "dave", "dina", "nobody"} {
			for _, follow := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/follow=%v", tg.name, who, follow), func(t *testing.T) {
					t.Parallel()
					e := newMentionEnv(t)
					id := tg.make(e)
					e.settle()
					audits := len(e.events())
					actor := app.Actor{}
					if who != "nobody" {
						actor = e.as(who)
					}
					before, berr := e.st.FollowingPosts(t.Context(), actor.UserID, []uint64{id})
					if berr != nil {
						t.Fatalf("FollowingPosts: %+v", berr)
					}
					method := "Follow"
					var err error
					if follow {
						err = e.posts.Follow(t.Context(), actor, id)
					} else {
						method = "Unfollow"
						err = e.posts.Unfollow(t.Context(), actor, id)
					}
					wantTag := tg.want[who]
					if who == "dina" || who == "nobody" {
						wantTag = app.TagUnauthenticated
					}
					if wantTag == "" {
						if err != nil {
							t.Fatalf("%s: %+v", method, err)
						}
						if got := e.following(who, id); got != follow {
							t.Errorf("Following = %v after %s, want %v", got, method, follow)
						}
					} else {
						route := "Posts." + method + "->Posts.setFollow"
						if wantTag == app.TagUnauthenticated {
							route += "->Store.Tx->DB.Transaction->Store.Tx.func1->Posts.setFollow.func1->checkActive.Unauthenticated"
						}
						_ = requireTag(t, err, wantTag, route)
						after, ferr := e.st.FollowingPosts(t.Context(), actor.UserID, []uint64{id})
						if ferr != nil || !maps.Equal(after, before) {
							t.Errorf("a refused call changed the follows from %v to %v, %v", before, after, ferr)
						}
					}
					if n := len(e.events()); n != audits {
						t.Errorf("%s wrote %d audit events", method, n-audits)
					}
					if ev := e.rec.Events(); len(ev) != 0 {
						t.Errorf("%s published %+v", method, ev)
					}
				})
			}
		}
	}
}

func TestAVisibleRoomNonMemberMayFollow(t *testing.T) {
	t.Parallel()
	e := newMentionEnv(t)
	p := e.post("bob", "lobby", app.FormatText, "hi")
	e.follow("root", p.ID, true)
	if !e.following("root", p.ID) {
		t.Error("root, who is no member of the lobby, does not follow")
	}
	// A follower who is not a member hears no reply (only members comment,
	// and the reader check is for private rooms only).
	e.settle()
	e.comment("carol", p.ID, app.FormatText, "a reply")
	if k := e.kinds("root"); !slices.Equal(k, []app.NotificationKind{app.NotificationReply}) {
		t.Errorf("root's notifications = %v, want a reply (a visible room can be read by anyone)", k)
	}
}

func TestRepliesToFollowedThreads(t *testing.T) {
	t.Parallel()
	reply, mention, keyword := app.NotificationReply, app.NotificationMention, app.NotificationKeyword
	tests := []struct {
		name  string
		setup envStep
		// act writes the comment that the case is about. p is the post of
		// admin, c1 the comment of bob under it. The case ends when act does.
		act  func(e *mentionEnv, p, c1 app.Post)
		want map[string]app.NotificationKind
	}{
		{"following: a comment under a followed post notifies every follower", nil,
			func(e *mentionEnv, p, _ app.Post) { e.comment("carol", p.ID, app.FormatText, "hello") },
			every(reply, "admin", "bob")},
		{"following: a comment at any depth", nil,
			func(e *mentionEnv, _, c1 app.Post) { e.comment("dave", c1.ID, app.FormatText, "deep") },
			every(reply, "admin", "bob")},
		{"following: the author of the comment is not notified", nil,
			func(e *mentionEnv, p, _ app.Post) { e.comment("admin", p.ID, app.FormatText, "my own post") },
			every(reply, "bob")},
		{"following: a follower's own comment does not notify them", nil,
			func(e *mentionEnv, p, c1 app.Post) { e.comment("bob", c1.ID, app.FormatText, "to myself") },
			every(reply, "admin")},
		{"following: a follow by Follow counts", nil,
			func(e *mentionEnv, p, _ app.Post) {
				e.follow("alice", p.ID, true)
				e.settle()
				e.comment("carol", p.ID, app.FormatText, "hello")
			},
			every(reply, "admin", "alice", "bob")},
		{"direct: the follows do not notify, the parent's author does", direct("admin", "bob"),
			func(e *mentionEnv, p, _ app.Post) { e.comment("carol", p.ID, app.FormatText, "hello") },
			every(reply, "admin")},
		{"direct: a deep comment notifies only the author of its parent", direct("admin", "bob"),
			func(e *mentionEnv, _, c1 app.Post) { e.comment("dave", c1.ID, app.FormatText, "deep") },
			every(reply, "bob")},
		{"mixed: bob follows, admin only direct", direct("admin"),
			func(e *mentionEnv, p, _ app.Post) { e.comment("carol", p.ID, app.FormatText, "hello") },
			every(reply, "admin", "bob")},
		{"mixed: admin follows, bob only direct (a comment under the post)", direct("bob"),
			func(e *mentionEnv, p, _ app.Post) { e.comment("carol", p.ID, app.FormatText, "hello") },
			every(reply, "admin")},
		{"the parent's author who unfollowed still gets the direct reply, once",
			nil, func(e *mentionEnv, p, c1 app.Post) {
				e.follow("bob", p.ID, false)
				e.settle()
				e.comment("dave", c1.ID, app.FormatText, "answer")
			},
			every(reply, "admin", "bob")},
		{"an unfollower who is not the parent gets nothing", nil,
			func(e *mentionEnv, p, _ app.Post) {
				e.follow("bob", p.ID, false)
				e.settle()
				e.comment("carol", p.ID, app.FormatText, "hello")
			},
			every(reply, "admin")},
		{"the unfollowed post author still gets the reply under it", nil,
			func(e *mentionEnv, p, _ app.Post) {
				e.follow("admin", p.ID, false)
				e.settle()
				e.comment("carol", p.ID, app.FormatText, "hello")
			},
			every(reply, "admin", "bob")},
		{"a disabled follower hears nothing", nil,
			func(e *mentionEnv, p, _ app.Post) {
				e.disable("bob")
				e.comment("carol", p.ID, app.FormatText, "hello")
			},
			every(reply, "admin")},
		{"a mention wins over the follower's reply", nil,
			func(e *mentionEnv, p, _ app.Post) { e.comment("carol", p.ID, app.FormatText, "@bob hey") },
			plus(every(reply, "admin"), "bob", mention)},
		{"a mention of someone who follows nothing", nil,
			func(e *mentionEnv, p, _ app.Post) { e.comment("carol", p.ID, app.FormatText, "@dave hey") },
			plus(every(reply, "admin", "bob"), "dave", mention)},
		{"a direct reply wins over a mention (D30)", nil,
			func(e *mentionEnv, p, _ app.Post) { e.comment("carol", p.ID, app.FormatText, "@admin hey") },
			every(reply, "admin", "bob")},
		{"a follower's reply wins over a group mention", nil,
			func(e *mentionEnv, p, _ app.Post) { e.comment("carol", p.ID, app.FormatText, "@room") },
			plus(plus(every(app.NotificationGroupMention, "alice", "dave"), "admin", reply), "bob", reply)},
		{"a follower's reply wins over a keyword", withKeywords("bob", "feeder"),
			func(e *mentionEnv, p, _ app.Post) { e.comment("carol", p.ID, app.FormatText, "the feeder") },
			every(reply, "admin", "bob")},
		{"a keyword of someone who follows nothing", withKeywords("dave", "feeder"),
			func(e *mentionEnv, p, _ app.Post) { e.comment("carol", p.ID, app.FormatText, "the feeder") },
			plus(every(reply, "admin", "bob"), "dave", keyword)},
		{"a comment in another thread is not heard", nil,
			func(e *mentionEnv, _, _ app.Post) {
				other := e.post("carol", "lobby", app.FormatText, "another thread")
				e.settle()
				e.comment("dave", other.ID, app.FormatText, "hello")
			},
			every(reply, "carol")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newMentionEnv(t)
			if tt.setup != nil {
				tt.setup(e)
			}
			p := e.post("admin", "lobby", app.FormatText, "Feeder schedule")
			c1 := e.comment("bob", p.ID, app.FormatText, "my turn")
			// Past the minute in which the first reply could take the next.
			e.clk.Advance(2 * time.Minute)
			e.settle()
			tt.act(e, p, c1)
			e.requireKinds(tt.want)
		})
	}
}

func TestAFollowerWhoLeftAPrivateRoomHearsNothing(t *testing.T) {
	t.Parallel()
	text := app.FormatText
	e := newMentionEnv(t)
	if err := e.rooms.AddMember(t.Context(), e.as("alice"), e.den.ID, e.uid("carol")); err != nil {
		t.Fatalf("AddMember: %+v", err)
	}
	p := e.post("alice", "den", text, "psst")
	e.comment("bob", p.ID, text, "from bob")
	e.comment("carol", p.ID, text, "from carol")
	if err := e.rooms.Leave(t.Context(), e.as("bob"), e.den.ID); err != nil {
		t.Fatalf("Leave: %+v", err)
	}
	e.clk.Advance(2 * time.Minute)
	e.settle()
	e.comment("alice", p.ID, text, "later")
	e.requireKinds(every(app.NotificationReply, "carol"))
	if ev := e.userEvents("bob", live.KindNotificationCreated); len(ev) != 0 {
		t.Errorf("bob heard of the reply: %+v", ev)
	}
}

func TestRepliesFoldIntoOneNotification(t *testing.T) {
	t.Parallel()
	text := app.FormatText
	e := newMentionEnv(t)
	p := e.post("admin", "lobby", text, "Feeder schedule")
	e.settle()

	only := func(what string) app.Notification {
		t.Helper()
		got := e.news("admin")
		if len(got) != 1 {
			t.Fatalf("%s: admin has %d new notifications %+v, want 1", what, len(got), got)
		}
		return got[0]
	}
	adminEvents := func(kind string) []live.Event { return e.userEvents("admin", kind) }
	notice := func(n app.Notification) string {
		t.Helper()
		out, ok, err := e.prefs.Notice(t.Context(), e.as("admin"), live.Event{
			Topic: live.UserTopic(e.uid("admin")), Kind: live.KindNotificationCreated, NotificationID: n.ID, Ring: true,
		})
		if err != nil || !ok {
			t.Fatalf("Notice = %+v, %v, %v", out, ok, err)
		}
		return out.Text
	}

	// The first comment makes the notification.
	c1 := e.comment("bob", p.ID, text, "one")
	first := only("first")
	if first.Kind != app.NotificationReply || first.Actor.Username != "bob" || first.Others != 0 || first.Read ||
		first.PostID != c1.ID || first.RootID != p.ID || first.RoomID != e.lobby.ID {
		t.Fatalf("first = %+v", first)
	}
	created := adminEvents(live.KindNotificationCreated)
	if len(created) != 1 || created[0].NotificationID != first.ID || !created[0].Ring || len(adminEvents(live.KindNotificationChanged)) != 0 {
		t.Fatalf("events of the first: created %+v, changed %+v", created, adminEvents(live.KindNotificationChanged))
	}
	if got := notice(first); got != "Bob replied in «Feeder schedule» in #lobby" {
		t.Errorf("text = %q", got)
	}

	// Later ones, within the minute, change it.
	burst := []struct {
		by         string
		parent     func() uint64
		wantActor  string
		wantOthers int
		wantText   string
	}{
		{"carol", func() uint64 { return p.ID }, "carol", 1, "Carol and 1 other replied in «Feeder schedule» in #lobby"},
		{"dave", func() uint64 { return c1.ID }, "dave", 2, "Dave and 2 others replied in «Feeder schedule» in #lobby"},
		{"carol", func() uint64 { return p.ID }, "carol", 2, "Carol and 2 others replied in «Feeder schedule» in #lobby"},
		{"bob", func() uint64 { return p.ID }, "bob", 2, "Bob and 2 others replied in «Feeder schedule» in #lobby"},
		{"dave", func() uint64 { return p.ID }, "dave", 2, "Dave and 2 others replied in «Feeder schedule» in #lobby"},
	}
	for i, s := range burst {
		e.clk.Advance(5 * time.Second)
		e.rec.Reset()
		c := e.comment(s.by, s.parent(), text, "next")
		n := only(fmt.Sprintf("step %d", i))
		if n.ID != first.ID || n.Actor.Username != s.wantActor || n.Others != s.wantOthers || n.Read || n.Kind != app.NotificationReply ||
			n.PostID != first.PostID || !n.CreatedAt.Equal(first.CreatedAt) {
			t.Errorf("step %d (%s): notification = %+v, want actor %s, %d others, the first comment %d, created %v", i, s.by, n, s.wantActor, s.wantOthers, first.PostID, first.CreatedAt)
		}
		if ev := adminEvents(live.KindNotificationCreated); len(ev) != 0 {
			t.Errorf("step %d: a fold published notification.created: %+v", i, ev)
		}
		changed := adminEvents(live.KindNotificationChanged)
		if len(changed) != 1 || changed[0].NotificationID != first.ID || changed[0].UserID != e.uid("admin") ||
			changed[0].Topic != live.UserTopic(e.uid("admin")) || changed[0].Ring || changed[0].Sound != "" || changed[0].Browser ||
			changed[0].PostID != c.ID || changed[0].RoomID != e.lobby.ID {
			t.Errorf("step %d: events = %+v, want one notification.changed of the notification, not ringing", i, changed)
		}
		if n, err := e.notes.UnreadCount(t.Context(), e.as("admin")); err != nil || n != 1 {
			t.Errorf("step %d: unread = %d, %v; want 1", i, n, err)
		}
		if got := notice(only("again")); got != s.wantText {
			t.Errorf("step %d: text = %q, want %q", i, got, s.wantText)
		}
	}

	// The follower's own comment in between is not counted.
	e.clk.Advance(5 * time.Second)
	e.comment("admin", p.ID, text, "my answer")
	e.rec.Reset()
	e.clk.Advance(5 * time.Second)
	e.comment("carol", p.ID, text, "after the answer")
	n := only("after admin's comment")
	if n.ID != first.ID || n.Actor.Username != "carol" || n.Others != 2 {
		t.Errorf("after the follower's own comment: %+v, want carol and 2 others (bob and dave)", n)
	}
}

func TestAFoldingBurstCountsOnlyItsOwnPeople(t *testing.T) {
	t.Parallel()
	text := app.FormatText
	e := newMentionEnv(t)
	p := e.post("admin", "lobby", text, "Feeder schedule")
	e.settle()
	e.comment("bob", p.ID, text, "one")
	e.clk.Advance(10 * time.Second)
	e.comment("carol", p.ID, text, "two")
	first := e.news("admin")
	if len(first) != 1 || first[0].Others != 1 {
		t.Fatalf("first burst = %+v", first)
	}
	// A new burst: it counts those of its own minute.
	e.clk.Advance(2 * time.Minute)
	e.comment("dave", p.ID, text, "three")
	news := e.news("admin")
	if len(news) != 2 || news[0].ID == first[0].ID || news[0].Actor.Username != "dave" || news[0].Others != 0 {
		t.Fatalf("second burst's start = %+v", news)
	}
	e.clk.Advance(10 * time.Second)
	e.comment("bob", p.ID, text, "four")
	news = e.news("admin")
	if len(news) != 2 || news[0].Actor.Username != "bob" || news[0].Others != 1 {
		t.Fatalf("second burst = %+v, want bob and 1 other (dave)", news)
	}
	if news[1].ID != first[0].ID || news[1].Actor.Username != "carol" || news[1].Others != 1 {
		t.Errorf("the first burst changed: %+v", news[1])
	}
}

func TestRepliesDoNotFoldAfterTheMinuteOrTheRead(t *testing.T) {
	t.Parallel()
	text := app.FormatText
	tests := []struct {
		name string
		// act runs after bob's first comment (which made the notification).
		act func(e *mentionEnv, p app.Post)
		// wantNew is the number of notifications admin has at the end, and
		// created the number of notification.created events after the first.
		wantNotifications int
		wantCreated       int
		wantChanged       int
		wantNewestActor   string
		wantNewestOthers  int
	}{
		{
			name: "after the notification is read",
			act: func(e *mentionEnv, p app.Post) {
				if err := e.notes.MarkRead(e.t.Context(), e.as("admin"), 1<<40); err != nil {
					e.t.Fatalf("MarkRead: %+v", err)
				}
				e.rec.Reset()
				e.comment("carol", p.ID, text, "two")
			},
			wantNotifications: 2, wantCreated: 1, wantChanged: 0, wantNewestActor: "carol", wantNewestOthers: 0,
		},
		{
			name: "after 61 seconds",
			act: func(e *mentionEnv, p app.Post) {
				e.clk.Advance(61 * time.Second)
				e.rec.Reset()
				e.comment("carol", p.ID, text, "two")
			},
			wantNotifications: 2, wantCreated: 1, wantChanged: 0, wantNewestActor: "carol", wantNewestOthers: 0,
		},
		{
			name: "the minute counts from the notification, not from the last reply",
			act: func(e *mentionEnv, p app.Post) {
				e.clk.Advance(40 * time.Second)
				e.comment("carol", p.ID, text, "two")
				e.clk.Advance(30 * time.Second)
				e.rec.Reset()
				e.comment("dave", p.ID, text, "three")
			},
			wantNotifications: 2, wantCreated: 1, wantChanged: 0, wantNewestActor: "dave", wantNewestOthers: 0,
		},
		{
			name: "within the minute it folds (control)",
			act: func(e *mentionEnv, p app.Post) {
				e.clk.Advance(40 * time.Second)
				e.rec.Reset()
				e.comment("carol", p.ID, text, "two")
			},
			wantNotifications: 1, wantCreated: 0, wantChanged: 1, wantNewestActor: "carol", wantNewestOthers: 1,
		},
		{
			name: "a reply in another thread does not fold",
			act: func(e *mentionEnv, p app.Post) {
				other := e.post("admin", "lobby", text, "Another thread")
				e.rec.Reset()
				e.comment("carol", other.ID, text, "two")
			},
			wantNotifications: 2, wantCreated: 1, wantChanged: 0, wantNewestActor: "carol", wantNewestOthers: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newMentionEnv(t)
			p := e.post("admin", "lobby", text, "Feeder schedule")
			e.settle()
			e.comment("bob", p.ID, text, "one")
			first := e.news("admin")
			if len(first) != 1 {
				t.Fatalf("first = %+v", first)
			}
			tt.act(e, p)
			news := e.news("admin")
			if len(news) != tt.wantNotifications {
				t.Fatalf("admin has %d notifications %+v, want %d", len(news), news, tt.wantNotifications)
			}
			if news[0].Actor.Username != tt.wantNewestActor || news[0].Others != tt.wantNewestOthers {
				t.Errorf("newest = %+v, want %s and %d others", news[0], tt.wantNewestActor, tt.wantNewestOthers)
			}
			if tt.wantNotifications == 2 {
				// The first stays as it was.
				if old := news[1]; old.ID != first[0].ID || !old.CreatedAt.Equal(first[0].CreatedAt) {
					t.Errorf("the first notification changed: %+v, was %+v", old, first[0])
				}
			}
			created := e.userEvents("admin", live.KindNotificationCreated)
			changed := e.userEvents("admin", live.KindNotificationChanged)
			if len(created) != tt.wantCreated || len(changed) != tt.wantChanged {
				t.Fatalf("events: created %+v, changed %+v; want %d and %d", created, changed, tt.wantCreated, tt.wantChanged)
			}
			if tt.wantCreated == 1 && (created[0].NotificationID != news[0].ID || !created[0].Ring || created[0].Sound != "purr") {
				t.Errorf("created = %+v, want it to ring for the new notification %d", created[0], news[0].ID)
			}
		})
	}
}

func TestADirectReplyOfAFollowerFolds(t *testing.T) {
	t.Parallel()
	text := app.FormatText
	e := newMentionEnv(t)
	p := e.post("admin", "lobby", text, "Feeder schedule")
	c1 := e.comment("bob", p.ID, text, "one") // bob follows and is the parent of what follows
	e.clk.Advance(2 * time.Minute)
	e.settle()

	e.comment("carol", c1.ID, text, "to bob")
	bob := e.news("bob")
	if len(bob) != 1 || bob[0].Actor.Username != "carol" || bob[0].Others != 0 {
		t.Fatalf("bob = %+v", bob)
	}
	e.clk.Advance(10 * time.Second)
	e.rec.Reset()
	e.comment("dave", c1.ID, text, "to bob too")
	again := e.news("bob")
	if len(again) != 1 || again[0].ID != bob[0].ID || again[0].Actor.Username != "dave" || again[0].Others != 1 {
		t.Fatalf("bob after the second = %+v, want one notification of dave and 1 other", again)
	}
	if ev := e.userEvents("bob", live.KindNotificationChanged); len(ev) != 1 {
		t.Errorf("events = %+v, want one notification.changed", ev)
	}
	if ev := e.userEvents("bob", live.KindNotificationCreated); len(ev) != 0 {
		t.Errorf("events = %+v, want no notification.created", ev)
	}
}

func TestRepliesDoNotFoldOnDirectRepliesMode(t *testing.T) {
	t.Parallel()
	text := app.FormatText
	e := newMentionEnv(t)
	direct("admin", "bob", "carol", "dave", "alice")(e)
	p := e.post("admin", "lobby", text, "Feeder schedule")
	e.settle()
	e.comment("bob", p.ID, text, "one")
	e.clk.Advance(5 * time.Second)
	e.comment("carol", p.ID, text, "two")
	e.clk.Advance(5 * time.Second)
	e.comment("dave", p.ID, text, "three")
	news := e.news("admin")
	if len(news) != 3 {
		t.Fatalf("admin has %d notifications %+v, want three", len(news), news)
	}
	for _, n := range news {
		if n.Others != 0 || n.Kind != app.NotificationReply {
			t.Errorf("notification = %+v, want a plain reply", n)
		}
	}
	if ev := e.userEvents("admin", live.KindNotificationCreated); len(ev) != 3 {
		t.Errorf("events = %+v, want three notification.created", ev)
	}
	if ev := e.userEvents("admin", live.KindNotificationChanged); len(ev) != 0 {
		t.Errorf("events = %+v, want no notification.changed", ev)
	}
}

func TestRepliesToTwoThreadsDoNotFold(t *testing.T) {
	t.Parallel()
	text := app.FormatText
	e := newMentionEnv(t)
	p1 := e.post("admin", "lobby", text, "First thread")
	p2 := e.post("admin", "lobby", text, "Second thread")
	e.settle()
	e.comment("bob", p1.ID, text, "one")
	e.comment("bob", p2.ID, text, "two")
	e.comment("carol", p1.ID, text, "three")
	news := e.news("admin")
	if len(news) != 2 {
		t.Fatalf("admin has %d notifications %+v, want one for each thread", len(news), news)
	}
	byRoot := map[uint64]app.Notification{}
	for _, n := range news {
		byRoot[n.RootID] = n
	}
	if n := byRoot[p1.ID]; n.Actor.Username != "carol" || n.Others != 1 {
		t.Errorf("first thread = %+v, want carol and 1 other", n)
	}
	if n := byRoot[p2.ID]; n.Actor.Username != "bob" || n.Others != 0 {
		t.Errorf("second thread = %+v, want bob alone", n)
	}
}

func TestOthersText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		others int
		want   string
	}{
		{-3, ""},
		{0, ""},
		{1, " and 1 other"},
		{2, " and 2 others"},
		{10, " and 10 others"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.others), func(t *testing.T) {
			t.Parallel()
			if got := app.OthersText(tt.others); got != tt.want {
				t.Errorf("OthersText(%d) = %q, want %q", tt.others, got, tt.want)
			}
		})
	}
}
