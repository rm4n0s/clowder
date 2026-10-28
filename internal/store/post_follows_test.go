package store

import (
	"reflect"
	"testing"
	"time"

	"github.com/rm4n0s/errors"
)

// followRow reads the row of a follow, and whether there is one.
func (f *fix) followRow(user *User, post *Post) (PostFollow, bool) {
	f.tb.Helper()
	var rows []PostFollow
	if err := f.s.db(f.ctx).Where("user_id = ? AND post_id = ?", user.ID, post.ID).Find(&rows).Error; err != nil {
		f.tb.Fatalf("read the follow: %v", err)
	}
	if len(rows) == 0 {
		return PostFollow{}, false
	}
	return rows[0], true
}

func TestAutoFollow(t *testing.T) {
	t.Parallel()
	f := newFix(t)
	alice, bob, carol := f.user(), f.user(), f.user()
	room := f.room(alice)
	post, other := f.post(room, alice), f.post(room, alice)

	following := func(u *User, p *Post) bool {
		t.Helper()
		got, err := f.s.FollowingPosts(f.ctx, u.ID, []uint64{p.ID})
		if err != nil {
			t.Fatalf("FollowingPosts: %+v", err)
		}
		return got[p.ID]
	}

	t.Run("makes followers", func(t *testing.T) {
		if err := f.s.AutoFollow(f.ctx, post.ID, []uint64{alice.ID, bob.ID}); err != nil {
			t.Fatalf("AutoFollow: %+v", err)
		}
		if !following(alice, post) || !following(bob, post) || following(carol, post) {
			t.Errorf("followers: alice %v, bob %v, carol %v", following(alice, post), following(bob, post), following(carol, post))
		}
		if following(alice, other) {
			t.Error("alice follows a post she was not made a follower of")
		}
		if n := f.count("post_follows"); n != 2 {
			t.Errorf("%d rows, want 2", n)
		}
	})

	t.Run("a second call changes nothing", func(t *testing.T) {
		before, _ := f.followRow(bob, post)
		if err := f.s.AutoFollow(f.ctx, post.ID, []uint64{bob.ID, carol.ID}); err != nil {
			t.Fatalf("AutoFollow: %+v", err)
		}
		after, ok := f.followRow(bob, post)
		if !ok || after.ID != before.ID {
			t.Errorf("bob's row = %+v, want the same row as %+v", after, before)
		}
		if n := f.count("post_follows"); n != 3 {
			t.Errorf("%d rows, want 3 (carol is new)", n)
		}
	})

	t.Run("a user who unfollowed stays out", func(t *testing.T) {
		if err := f.s.SetFollow(f.ctx, bob.ID, post.ID, false); err != nil {
			t.Fatalf("SetFollow: %+v", err)
		}
		if err := f.s.AutoFollow(f.ctx, post.ID, []uint64{bob.ID}); err != nil {
			t.Fatalf("AutoFollow: %+v", err)
		}
		if following(bob, post) {
			t.Error("AutoFollow brought back a follow that bob had undone")
		}
		if row, ok := f.followRow(bob, post); !ok || !row.Unfollowed {
			t.Errorf("bob's row = %+v, %v; want it kept with Unfollowed", row, ok)
		}
	})

	t.Run("no users is a no-op", func(t *testing.T) {
		before := f.count("post_follows")
		for _, ids := range [][]uint64{nil, {}} {
			if err := f.s.AutoFollow(canceled(t), post.ID, ids); err != nil {
				t.Errorf("AutoFollow(%v): %+v", ids, err)
			}
		}
		if n := f.count("post_follows"); n != before {
			t.Errorf("%d rows, want %d", n, before)
		}
	})

	failures := []struct {
		name    string
		postID  uint64
		userIDs []uint64
	}{
		{"an unknown user", post.ID, []uint64{9999}},
		{"an unknown post", 9999, []uint64{alice.ID}},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			before := f.count("post_follows")
			requireTagged(t, f.s.AutoFollow(f.ctx, tt.postID, tt.userIDs), TagQueryFailed, "Store.AutoFollow.QueryFailed", "autoFollow")
			if n := f.count("post_follows"); n != before {
				t.Errorf("%d rows after the failure, want %d", n, before)
			}
		})
	}
}

func TestSetFollowAndFollowingPosts(t *testing.T) {
	t.Parallel()
	f := newFix(t)
	alice, bob := f.user(), f.user()
	room := f.room(alice)
	p1, p2, p3 := f.post(room, alice), f.post(room, alice), f.post(room, alice)
	all := []uint64{p1.ID, p2.ID, p3.ID}

	set := func(u *User, p *Post, on bool) {
		t.Helper()
		if err := f.s.SetFollow(f.ctx, u.ID, p.ID, on); err != nil {
			t.Fatalf("SetFollow(%v): %+v", on, err)
		}
	}
	following := func(u *User) map[uint64]bool {
		t.Helper()
		got, err := f.s.FollowingPosts(f.ctx, u.ID, all)
		if err != nil {
			t.Fatalf("FollowingPosts: %+v", err)
		}
		return got
	}

	if got := following(alice); len(got) != 0 {
		t.Fatalf("a user with no follows: %v", got)
	}

	t.Run("follow, unfollow, follow again is one row", func(t *testing.T) {
		set(alice, p1, true)
		first, ok := f.followRow(alice, p1)
		if !ok || first.Unfollowed {
			t.Fatalf("after following: %+v, %v", first, ok)
		}
		set(alice, p1, false)
		off, ok := f.followRow(alice, p1)
		if !ok || !off.Unfollowed || off.ID != first.ID {
			t.Fatalf("after unfollowing: %+v, %v", off, ok)
		}
		if got := following(alice); len(got) != 0 {
			t.Errorf("following after unfollowing: %v", got)
		}
		set(alice, p1, true)
		on, ok := f.followRow(alice, p1)
		if !ok || on.Unfollowed || on.ID != first.ID {
			t.Fatalf("after following again: %+v, %v", on, ok)
		}
		if n := f.count("post_follows"); n != 1 {
			t.Errorf("%d rows, want 1 for the pair", n)
		}
	})

	t.Run("a first unfollow keeps a row", func(t *testing.T) {
		set(bob, p2, false)
		row, ok := f.followRow(bob, p2)
		if !ok || !row.Unfollowed {
			t.Errorf("row = %+v, %v; want one with Unfollowed", row, ok)
		}
		if got := following(bob); len(got) != 0 {
			t.Errorf("bob follows %v", got)
		}
	})

	t.Run("FollowingPosts reports only followed posts", func(t *testing.T) {
		set(alice, p3, true)
		set(alice, p2, false)
		set(bob, p2, true)
		tests := []struct {
			name string
			user *User
			ids  []uint64
			want map[uint64]bool
		}{
			{"alice, all posts", alice, all, map[uint64]bool{p1.ID: true, p3.ID: true}},
			{"bob, all posts", bob, all, map[uint64]bool{p2.ID: true}},
			{"alice, one followed post", alice, []uint64{p3.ID}, map[uint64]bool{p3.ID: true}},
			{"alice, one unfollowed post", alice, []uint64{p2.ID}, map[uint64]bool{}},
			{"alice, unknown post", alice, []uint64{9999}, map[uint64]bool{}},
			{"alice, no posts", alice, nil, map[uint64]bool{}},
			{"an unknown user", &User{ID: 9999}, all, map[uint64]bool{}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := f.s.FollowingPosts(f.ctx, tt.user.ID, tt.ids)
				if err != nil {
					t.Fatalf("FollowingPosts: %+v", err)
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Errorf("got %v, want %v", got, tt.want)
				}
			})
		}
	})

	t.Run("SetFollow fails for an unknown user or post", func(t *testing.T) {
		requireTagged(t, f.s.SetFollow(f.ctx, 9999, p1.ID, true), TagQueryFailed, "Store.SetFollow.QueryFailed", "setFollow")
		requireTagged(t, f.s.SetFollow(f.ctx, alice.ID, 9999, true), TagQueryFailed, "Store.SetFollow.QueryFailed", "setFollow")
	})
}

func TestThreadFollowers(t *testing.T) {
	t.Parallel()
	f := newFix(t)
	author := f.user()
	room := f.room(author)
	post, otherPost := f.post(room, author), f.post(room, author)

	// Users are made in ID order: the result must follow it whatever the order
	// of the follows.
	plain, direct, disabled, unfollowed, excluded, noFollow := f.user(), f.user(), f.user(), f.user(), f.user(), f.user()
	explicit := f.user()
	if err := f.s.SaveUserSettings(f.ctx, &UserSettings{
		UserID: direct.ID, Theme: ThemeSystem, Sound: true, Notify: NotifyMentions, SoundName: SoundPurr, Replies: RepliesDirect,
	}); err != nil {
		t.Fatalf("SaveUserSettings: %v", err)
	}
	if err := f.s.SaveUserSettings(f.ctx, &UserSettings{
		UserID: explicit.ID, Theme: ThemeSystem, Sound: true, Notify: NotifyMentions, SoundName: SoundPurr, Replies: RepliesFollowing,
	}); err != nil {
		t.Fatalf("SaveUserSettings: %v", err)
	}
	f.exec("UPDATE users SET disabled_at = ? WHERE id = ?", day(0), disabled.ID)
	for _, u := range []*User{explicit, excluded, plain, direct, disabled, unfollowed} {
		if err := f.s.SetFollow(f.ctx, u.ID, post.ID, true); err != nil {
			t.Fatalf("SetFollow: %+v", err)
		}
	}
	if err := f.s.SetFollow(f.ctx, unfollowed.ID, post.ID, false); err != nil {
		t.Fatalf("SetFollow: %+v", err)
	}
	if err := f.s.SetFollow(f.ctx, noFollow.ID, otherPost.ID, true); err != nil {
		t.Fatalf("SetFollow: %+v", err)
	}

	tests := []struct {
		name   string
		root   uint64
		except uint64
		want   []uint64
	}{
		{"the followers but the excluded one, by user ID", post.ID, excluded.ID, []uint64{plain.ID, explicit.ID}},
		{"nobody excluded", post.ID, 0, []uint64{plain.ID, excluded.ID, explicit.ID}},
		{"the only follower excluded", otherPost.ID, noFollow.ID, nil},
		{"a post nobody follows", 9999, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := f.s.ThreadFollowers(f.ctx, tt.root, tt.except)
			if err != nil {
				t.Fatalf("ThreadFollowers: %+v", err)
			}
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v (direct %d, disabled %d, unfollowed %d)", got, tt.want, direct.ID, disabled.ID, unfollowed.ID)
			}
		})
	}
}

func TestFoldableReply(t *testing.T) {
	t.Parallel()

	t.Run("finds the newest unread one of the thread", func(t *testing.T) {
		t.Parallel()
		g := newFix(t)
		a, b := g.user(), g.user()
		r := g.room(a)
		root := g.post(r, a)
		c := g.comment(root, b)
		older := g.notify(a, NotificationReply, r, root, b)
		g.clk.Advance(10 * time.Second)
		newer := g.notify(a, NotificationReply, r, c, b)
		got, ok, err := g.s.FoldableReply(g.ctx, a.ID, root.ID)
		if err != nil || !ok {
			t.Fatalf("FoldableReply = %+v, %v, %v", got, ok, err)
		}
		if got.ID != newer.ID || got.ID == older.ID {
			t.Errorf("got notification %d, want the newest %d", got.ID, newer.ID)
		}
		if got.Kind != NotificationReply || got.UserID != a.ID {
			t.Errorf("got %+v", got)
		}
		// The burst is counted from the creation of the newest: a minute
		// after the older one, the newer one is still foldable.
		g.clk.Advance(55 * time.Second)
		if got, ok, err := g.s.FoldableReply(g.ctx, a.ID, root.ID); err != nil || !ok || got.ID != newer.ID {
			t.Errorf("65s after the older one: %+v, %v, %v; want the newer one", got, ok, err)
		}
	})

	tests := []struct {
		name  string
		setup func(g *fix) (user *User, root *Post)
		want  bool
	}{
		{"a reply to the root post", func(g *fix) (*User, *Post) {
			a, b := g.user(), g.user()
			r := g.room(a)
			p := g.post(r, a)
			g.notify(a, NotificationReply, r, p, b)
			return a, p
		}, true},
		{"a reply to a comment under the root", func(g *fix) (*User, *Post) {
			a, b := g.user(), g.user()
			r := g.room(a)
			p := g.post(r, a)
			c := g.comment(p, b)
			g.notify(a, NotificationReply, r, c, b)
			return a, p
		}, true},
		{"just under a minute old", func(g *fix) (*User, *Post) {
			a, b := g.user(), g.user()
			r := g.room(a)
			p := g.post(r, a)
			g.notify(a, NotificationReply, r, p, b)
			g.clk.Advance(59 * time.Second)
			return a, p
		}, true},
		{"a minute old", func(g *fix) (*User, *Post) {
			a, b := g.user(), g.user()
			r := g.room(a)
			p := g.post(r, a)
			g.notify(a, NotificationReply, r, p, b)
			g.clk.Advance(time.Minute)
			return a, p
		}, false},
		{"older than a minute", func(g *fix) (*User, *Post) {
			a, b := g.user(), g.user()
			r := g.room(a)
			p := g.post(r, a)
			g.notify(a, NotificationReply, r, p, b)
			g.clk.Advance(61 * time.Second)
			return a, p
		}, false},
		{"read", func(g *fix) (*User, *Post) {
			a, b := g.user(), g.user()
			r := g.room(a)
			p := g.post(r, a)
			n := g.notify(a, NotificationReply, r, p, b)
			g.exec("UPDATE notifications SET read_at = ? WHERE id = ?", g.clk.Now(), n.ID)
			return a, p
		}, false},
		{"of another kind", func(g *fix) (*User, *Post) {
			a, b := g.user(), g.user()
			r := g.room(a)
			p := g.post(r, a)
			g.notify(a, NotificationMention, r, p, b)
			g.notify(a, NotificationKeyword, r, p, b)
			g.notify(a, NotificationGroupMention, r, p, b)
			return a, p
		}, false},
		{"of another user", func(g *fix) (*User, *Post) {
			a, b, c := g.user(), g.user(), g.user()
			r := g.room(a)
			p := g.post(r, a)
			g.notify(c, NotificationReply, r, p, b)
			return a, p
		}, false},
		{"of another thread", func(g *fix) (*User, *Post) {
			a, b := g.user(), g.user()
			r := g.room(a)
			p, q := g.post(r, a), g.post(r, a)
			g.notify(a, NotificationReply, r, q, b)
			return a, p
		}, false},
		{"of the comment of another thread", func(g *fix) (*User, *Post) {
			a, b := g.user(), g.user()
			r := g.room(a)
			p, q := g.post(r, a), g.post(r, a)
			c := g.comment(q, b)
			g.notify(a, NotificationReply, r, c, b)
			return a, p
		}, false},
		{"without a post", func(g *fix) (*User, *Post) {
			a, b := g.user(), g.user()
			r := g.room(a)
			p := g.post(r, a)
			g.create(&Notification{UserID: a.ID, Kind: NotificationReply, RoomID: &r.ID, ActorID: b.ID})
			return a, p
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := newFix(t)
			user, root := tt.setup(g)
			got, ok, err := g.s.FoldableReply(g.ctx, user.ID, root.ID)
			if err != nil {
				t.Fatalf("FoldableReply: %+v", err)
			}
			if ok != tt.want {
				t.Errorf("found = %v, want %v (%+v)", ok, tt.want, got)
			}
			if !ok && got.ID != 0 {
				t.Errorf("not found, but got %+v", got)
			}
		})
	}
}

func TestDistinctRepliers(t *testing.T) {
	t.Parallel()
	f := newFix(t)
	owner, a, b, c, d, excludedA, excludedB := f.user(), f.user(), f.user(), f.user(), f.user(), f.user(), f.user()
	room := f.room(owner)
	root := f.post(room, owner)
	other := f.post(room, owner)

	// Below fromPostID: written by d only, so d counts only from the start.
	early := f.comment(root, d)
	from := f.comment(root, a)
	f.comment(root, a)
	f.comment(from, b)
	deletedByC := f.comment(root, c)
	f.exec("UPDATE posts SET deleted_at = ? WHERE id = ?", day(0), deletedByC.ID)
	f.comment(root, excludedA)
	f.comment(from, excludedB)
	f.comment(other, d)

	tests := []struct {
		name         string
		root, from   uint64
		exceptA      uint64
		exceptB      uint64
		wantRepliers int
	}{
		{"from the burst start: a and b", root.ID, from.ID, excludedA.ID, excludedB.ID, 2},
		{"from the first comment: d joins", root.ID, early.ID, excludedA.ID, excludedB.ID, 3},
		{"nobody excluded", root.ID, early.ID, 0, 0, 5},
		{"the same user twice", root.ID, early.ID, excludedA.ID, excludedA.ID, 4},
		{"a replier excluded", root.ID, from.ID, a.ID, excludedB.ID, 2},
		{"past the last comment", root.ID, 9999, 0, 0, 0},
		{"the other thread", other.ID, 0, 0, 0, 1},
		{"an unknown thread", 9999, 0, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, err := f.s.DistinctRepliers(f.ctx, tt.root, tt.from, tt.exceptA, tt.exceptB)
			if err != nil {
				t.Fatalf("DistinctRepliers: %+v", err)
			}
			if n != tt.wantRepliers {
				t.Errorf("got %d, want %d", n, tt.wantRepliers)
			}
		})
	}
}

func TestFoldReply(t *testing.T) {
	t.Parallel()
	f := newFix(t)
	alice, bob, carol := f.user(), f.user(), f.user()
	room := f.room(alice)
	post := f.post(room, alice)
	created := f.notify(alice, NotificationReply, room, post, bob)
	f.clk.Advance(time.Hour)
	readAt := f.clk.Now()
	f.exec("UPDATE notifications SET read_at = ? WHERE id = ?", readAt, created.ID)
	untouched := f.notify(alice, NotificationReply, room, post, bob)

	t.Run("names the actor and counts the others", func(t *testing.T) {
		if err := f.s.FoldReply(f.ctx, created.ID, carol.ID, 3); err != nil {
			t.Fatalf("FoldReply: %+v", err)
		}
		var got Notification
		if err := f.s.db(f.ctx).First(&got, created.ID).Error; err != nil {
			t.Fatalf("First: %v", err)
		}
		if got.ActorID != carol.ID || got.Others != 3 {
			t.Errorf("actor %d, others %d; want %d and 3", got.ActorID, got.Others, carol.ID)
		}
		requireSameTime(t, "CreatedAt", created.CreatedAt, got.CreatedAt)
		if got.ReadAt == nil {
			t.Fatal("ReadAt was cleared")
		}
		requireSameTime(t, "ReadAt", readAt, *got.ReadAt)
		if got.Kind != NotificationReply || got.UserID != alice.ID || got.PostID == nil || *got.PostID != post.ID {
			t.Errorf("other fields changed: %+v", got)
		}
		var other Notification
		if err := f.s.db(f.ctx).First(&other, untouched.ID).Error; err != nil || other.ActorID != bob.ID || other.Others != 0 {
			t.Errorf("another notification = %+v, %v", other, err)
		}
	})

	t.Run("a second fold overwrites the first, also with zero", func(t *testing.T) {
		if err := f.s.FoldReply(f.ctx, created.ID, bob.ID, 0); err != nil {
			t.Fatalf("FoldReply: %+v", err)
		}
		var got Notification
		if err := f.s.db(f.ctx).First(&got, created.ID).Error; err != nil || got.ActorID != bob.ID || got.Others != 0 {
			t.Errorf("notification = %+v, %v; want actor %d and 0 others", got, err, bob.ID)
		}
	})

	t.Run("a missing notification is not an error", func(t *testing.T) {
		before := f.count("notifications")
		if err := f.s.FoldReply(f.ctx, 9999, bob.ID, 1); err != nil {
			t.Errorf("FoldReply of a missing ID: %+v", err)
		}
		if n := f.count("notifications"); n != before {
			t.Errorf("%d notifications, want %d", n, before)
		}
	})

	t.Run("the folded count shows in ListNotifications", func(t *testing.T) {
		if err := f.s.FoldReply(f.ctx, untouched.ID, carol.ID, 4); err != nil {
			t.Fatalf("FoldReply: %+v", err)
		}
		rows, err := f.s.ListNotifications(f.ctx, alice.ID, 0, 10)
		if err != nil || len(rows) != 2 {
			t.Fatalf("ListNotifications = %+v, %v", rows, err)
		}
		if rows[0].ID != untouched.ID || rows[0].Others != 4 || rows[0].ActorID != carol.ID || rows[0].ActorUsername != carol.Username {
			t.Errorf("newest = %+v", rows[0])
		}
		if rows[1].Others != 0 {
			t.Errorf("older = %+v, want no others", rows[1])
		}
	})
}

func TestPostFollowsCascade(t *testing.T) {
	t.Parallel()
	f := newFix(t)
	alice, bob := f.user(), f.user()
	room := f.room(alice)
	p, q := f.post(room, alice), f.post(room, alice)
	for _, post := range []*Post{p, q} {
		if err := f.s.AutoFollow(f.ctx, post.ID, []uint64{alice.ID, bob.ID}); err != nil {
			t.Fatalf("AutoFollow: %+v", err)
		}
	}
	if err := f.s.SetFollow(f.ctx, bob.ID, p.ID, false); err != nil {
		t.Fatalf("SetFollow: %+v", err)
	}
	if n := f.count("post_follows"); n != 4 {
		t.Fatalf("%d rows, want 4", n)
	}
	if err := f.s.DeletePost(f.ctx, p.ID); err != nil {
		t.Fatalf("DeletePost: %+v", err)
	}
	if n := f.count("post_follows"); n != 2 {
		t.Errorf("%d rows after the post went, want 2 (those of the other post)", n)
	}
	var left []uint64
	if err := f.s.db(f.ctx).Model(&PostFollow{}).Pluck("post_id", &left).Error; err != nil {
		t.Fatalf("Pluck: %v", err)
	}
	for _, id := range left {
		if id != q.ID {
			t.Errorf("a follow of post %d is left", id)
		}
	}
}

func TestPostFollowQueryTags(t *testing.T) {
	t.Parallel()
	f := newFix(t)
	alice := f.user()
	room := f.room(alice)
	post := f.post(room, alice)
	tests := []struct {
		name      string
		call      func() error
		wantRoute string
		wantOp    string
	}{
		{"AutoFollow", func() error { return f.s.AutoFollow(canceled(t), post.ID, []uint64{alice.ID}) },
			"Store.AutoFollow.QueryFailed", "autoFollow"},
		{"SetFollow", func() error { return f.s.SetFollow(canceled(t), alice.ID, post.ID, true) },
			"Store.SetFollow.QueryFailed", "setFollow"},
		{"FollowingPosts", func() error {
			_, err := f.s.FollowingPosts(canceled(t), alice.ID, []uint64{post.ID})
			return err
		}, "Store.FollowingPosts.QueryFailed", "followingPosts"},
		{"ThreadFollowers", func() error {
			_, err := f.s.ThreadFollowers(canceled(t), post.ID, 0)
			return err
		}, "Store.ThreadFollowers.QueryFailed", "threadFollowers"},
		{"FoldableReply", func() error {
			_, _, err := f.s.FoldableReply(canceled(t), alice.ID, post.ID)
			return err
		}, "Store.FoldableReply.QueryFailed", "foldableReply"},
		{"DistinctRepliers", func() error {
			_, err := f.s.DistinctRepliers(canceled(t), post.ID, 0, 0, 0)
			return err
		}, "Store.DistinctRepliers.QueryFailed", "distinctRepliers"},
		{"FoldReply", func() error { return f.s.FoldReply(canceled(t), 1, alice.ID, 1) },
			"Store.FoldReply.QueryFailed", "foldReply"},
		{"EnabledUserSet", func() error {
			_, err := f.s.EnabledUserSet(canceled(t), []uint64{alice.ID})
			return err
		}, "Store.EnabledUserSet.QueryFailed", "enabledUserSet"},
		{"PostMentions", func() error {
			_, err := f.s.PostMentions(canceled(t), post.ID)
			return err
		}, "Store.PostMentions.QueryFailed", "postMentions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			requireTagged(t, err, TagQueryFailed, tt.wantRoute, tt.wantOp)
			if appErr, ok := errors.FromError(err); ok && appErr.OriginalErr == nil {
				t.Error("the error keeps no cause")
			}
		})
	}
}
