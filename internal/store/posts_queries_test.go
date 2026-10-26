package store

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// postIDs returns the IDs of the posts, in order.
func postIDs(posts []Post) []uint64 {
	out := []uint64{}
	for _, p := range posts {
		out = append(out, p.ID)
	}
	return out
}

func TestPostQueries(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	room := f.room(alice)

	t.Run("CreatePost and PostByID", func(t *testing.T) {
		p := &Post{RoomID: room.ID, AuthorID: alice.ID, Format: FormatMarkdown, Body: "**hi**"}
		if err := f.s.CreatePost(f.ctx, p); err != nil || p.ID == 0 {
			t.Fatalf("CreatePost = %v, ID %d", err, p.ID)
		}
		got, err := f.s.PostByID(f.ctx, p.ID)
		if err != nil || got.Body != "**hi**" || got.Format != FormatMarkdown || got.RoomID != room.ID {
			t.Fatalf("PostByID = %+v, %v", got, err)
		}
		if got.Author == nil || got.Author.Username != alice.Username {
			t.Errorf("Author = %+v, want alice", got.Author)
		}
		if got.EditedAt != nil || got.DeletedAt != nil {
			t.Errorf("EditedAt, DeletedAt = %v, %v", got.EditedAt, got.DeletedAt)
		}
		requireSameTime(t, "CreatedAt", fakeStart, got.CreatedAt)
	})

	t.Run("UpdatePostContent", func(t *testing.T) {
		p := f.post(room, alice)
		at := day(3 * time.Hour)
		if err := f.s.UpdatePostContent(f.ctx, p.ID, FormatMarkdown, "", at); err != nil {
			t.Fatalf("UpdatePostContent: %+v", err)
		}
		got, err := f.s.PostByID(f.ctx, p.ID)
		if err != nil || got.Format != FormatMarkdown || got.Body != "" || got.EditedAt == nil {
			t.Fatalf("post = %+v, %v", got, err)
		}
		requireSameTime(t, "EditedAt", at, *got.EditedAt)
	})

	t.Run("MarkPostDeleted and DeletePostExtras", func(t *testing.T) {
		p := f.post(room, alice)
		f.comment(p, bob)
		tag := f.tag()
		f.hang(p, f.column(room), bob, tag)
		if err := f.s.DeletePostExtras(f.ctx, p.ID); err != nil {
			t.Fatalf("DeletePostExtras: %+v", err)
		}
		for _, table := range []string{"post_tags", "reactions", "mentions", "attachments"} {
			var n int64
			if err := f.s.db(f.ctx).Raw("SELECT count(*) FROM "+table+" WHERE post_id = ?", p.ID).Scan(&n).Error; err != nil || n != 0 {
				t.Errorf("%s has %d rows of the post (%v)", table, n, err)
			}
		}
		at := day(time.Hour)
		if err := f.s.MarkPostDeleted(f.ctx, p.ID, at); err != nil {
			t.Fatalf("MarkPostDeleted: %+v", err)
		}
		got, err := f.s.PostByID(f.ctx, p.ID)
		if err != nil || got.Body != "" || got.DeletedAt == nil {
			t.Fatalf("post = %+v, %v", got, err)
		}
		requireSameTime(t, "DeletedAt", at, *got.DeletedAt)
		if has, err := f.s.PostHasReplies(f.ctx, p.ID); err != nil || !has {
			t.Errorf("PostHasReplies = %v, %v, want true", has, err)
		}
	})

	t.Run("DeletePost cascades", func(t *testing.T) {
		p := f.post(room, alice)
		tag := f.tag()
		f.create(&PostTag{PostID: p.ID, TagID: tag.ID})
		if has, err := f.s.PostHasReplies(f.ctx, p.ID); err != nil || has {
			t.Errorf("PostHasReplies = %v, %v, want false", has, err)
		}
		if err := f.s.DeletePost(f.ctx, p.ID); err != nil {
			t.Fatalf("DeletePost: %+v", err)
		}
		if _, err := f.s.PostByID(f.ctx, p.ID); err == nil {
			t.Error("the post is still there")
		}
		m, err := f.s.TagNamesOfPosts(f.ctx, []uint64{p.ID})
		if err != nil || len(m) != 0 {
			t.Errorf("tags of the deleted post = %v, %v", m, err)
		}
	})

	t.Run("ListFeed", func(t *testing.T) {
		g := newFix(t)
		u := g.user()
		r, o := g.room(u), g.room(u)
		var ids []uint64
		for i := 0; i < 5; i++ {
			ids = append(ids, g.post(r, u).ID)
		}
		g.post(o, u)
		g.comment(&Post{ID: ids[0], RoomID: r.ID}, u) // a comment is no post of the feed

		tests := []struct {
			name   string
			before uint64
			limit  int
			want   []uint64
		}{
			{"newest first", 0, 10, []uint64{ids[4], ids[3], ids[2], ids[1], ids[0]}},
			{"limit", 0, 2, []uint64{ids[4], ids[3]}},
			{"before", ids[3], 10, []uint64{ids[2], ids[1], ids[0]}},
			{"before and limit", ids[3], 1, []uint64{ids[2]}},
			{"before the oldest", ids[0], 10, []uint64{}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := g.s.ListFeed(g.ctx, r.ID, tt.before, tt.limit)
				if err != nil || !reflect.DeepEqual(postIDs(got), tt.want) {
					t.Errorf("ListFeed = %v, %v, want %v", postIDs(got), err, tt.want)
				}
				for _, p := range got {
					if p.Author == nil || p.Author.ID != u.ID {
						t.Errorf("post %d: Author = %+v", p.ID, p.Author)
					}
				}
			})
		}
		if none, err := g.s.ListFeed(g.ctx, 999, 0, 10); err != nil || len(none) != 0 {
			t.Errorf("ListFeed of no room = %v, %v", none, err)
		}
	})

	t.Run("tags", func(t *testing.T) {
		p, q := f.post(room, alice), f.post(room, bob)
		if err := f.s.ReplacePostTags(f.ctx, p.ID, []string{"zeta", "alpha", "mid"}); err != nil {
			t.Fatalf("ReplacePostTags: %+v", err)
		}
		if err := f.s.ReplacePostTags(f.ctx, q.ID, []string{"mid", "new"}); err != nil {
			t.Fatalf("ReplacePostTags: %+v", err)
		}
		got, err := f.s.TagNamesOfPosts(f.ctx, []uint64{p.ID, q.ID, 999})
		want := map[uint64][]string{p.ID: {"zeta", "alpha", "mid"}, q.ID: {"mid", "new"}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("TagNamesOfPosts = %v, %v, want %v", got, err, want)
		}
		// Replacing keeps the shared tag row and drops the old links.
		if err := f.s.ReplacePostTags(f.ctx, p.ID, []string{"mid"}); err != nil {
			t.Fatalf("ReplacePostTags: %+v", err)
		}
		got, err = f.s.TagNamesOfPosts(f.ctx, []uint64{p.ID})
		if err != nil || !reflect.DeepEqual(got, map[uint64][]string{p.ID: {"mid"}}) {
			t.Errorf("TagNamesOfPosts = %v, %v", got, err)
		}
		if err := f.s.ReplacePostTags(f.ctx, p.ID, nil); err != nil {
			t.Fatalf("ReplacePostTags(nil): %+v", err)
		}
		got, err = f.s.TagNamesOfPosts(f.ctx, []uint64{p.ID})
		if err != nil || len(got) != 0 {
			t.Errorf("TagNamesOfPosts after clearing = %v, %v", got, err)
		}
		if empty, err := f.s.TagNamesOfPosts(f.ctx, nil); err != nil || len(empty) != 0 {
			t.Errorf("TagNamesOfPosts(nil) = %v, %v", empty, err)
		}
	})

	t.Run("CommentCounts", func(t *testing.T) {
		p, q, none := f.post(room, alice), f.post(room, alice), f.post(room, alice)
		c1 := f.comment(p, bob)
		f.comment(c1, alice) // a deeper comment counts for the root
		f.comment(p, alice)
		f.comment(q, bob)
		got, err := f.s.CommentCounts(f.ctx, []uint64{p.ID, q.ID, none.ID})
		if err != nil || !reflect.DeepEqual(got, map[uint64]int{p.ID: 3, q.ID: 1}) {
			t.Errorf("CommentCounts = %v, %v", got, err)
		}
		if empty, err := f.s.CommentCounts(f.ctx, nil); err != nil || len(empty) != 0 {
			t.Errorf("CommentCounts(nil) = %v, %v", empty, err)
		}
	})

	t.Run("SuggestTagNames", func(t *testing.T) {
		g := newFix(t)
		a, b := g.user(), g.user()
		visible := g.room(a)
		hidden := &Room{Name: "hidden", Visibility: VisibilityPrivate, CreatedBy: a.ID}
		g.create(hidden)
		g.create(&RoomMember{RoomID: hidden.ID, UserID: a.ID})
		for _, c := range []struct {
			room *Room
			tags []string
		}{
			{visible, []string{"go", "gopher", "rust"}},
			{hidden, []string{"goal", "go"}},
		} {
			if err := g.s.ReplacePostTags(g.ctx, g.post(c.room, a).ID, c.tags); err != nil {
				t.Fatal(err)
			}
		}
		for _, tt := range []struct {
			name   string
			user   uint64
			prefix string
			limit  int
			want   []string
		}{
			{"member of the private room", a.ID, "go", 10, []string{"go", "goal", "gopher"}},
			{"not a member", b.ID, "go", 10, []string{"go", "gopher"}},
			{"limit", a.ID, "go", 2, []string{"go", "goal"}},
			{"no prefix", b.ID, "", 10, []string{"go", "gopher", "rust"}},
			{"nothing", a.ID, "x", 10, []string{}},
		} {
			got, err := g.s.SuggestTagNames(g.ctx, tt.user, tt.prefix, tt.limit)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("%s: SuggestTagNames = %v, %v, want %v", tt.name, got, err, tt.want)
			}
		}
	})
}

func TestFeedQueryUsesItsIndex(t *testing.T) {
	f := newFix(t)
	for _, query := range []string{
		"SELECT * FROM posts WHERE room_id = 1 AND parent_id IS NULL ORDER BY id DESC LIMIT 31",
		"SELECT * FROM posts WHERE room_id = 1 AND parent_id IS NULL AND id < 50 ORDER BY id DESC LIMIT 31",
	} {
		var plan []struct{ Detail string }
		if err := f.s.db(f.ctx).Raw("EXPLAIN QUERY PLAN " + query).Scan(&plan).Error; err != nil {
			t.Fatal(err)
		}
		var details []string
		for _, p := range plan {
			details = append(details, p.Detail)
		}
		if !slices.ContainsFunc(details, func(d string) bool { return strings.Contains(d, "idx_posts_feed") }) {
			t.Errorf("plan %q of %q does not use idx_posts_feed", details, query)
		}
		if slices.ContainsFunc(details, func(d string) bool { return strings.Contains(d, "TEMP B-TREE") }) {
			t.Errorf("plan %q of %q sorts", details, query)
		}
	}
}

func TestPostQueryTags(t *testing.T) {
	f := newFix(t)
	alice := f.user()
	room := f.room(alice)
	post := f.post(room, alice)

	tests := []struct {
		name      string
		call      func() error
		wantTag   string
		wantRoute string
		wantOp    string
	}{
		{
			name: "post of an unknown room",
			call: func() error {
				return f.s.CreatePost(f.ctx, &Post{RoomID: 999, AuthorID: alice.ID, Format: FormatText, Body: "x"})
			},
			wantTag: TagQueryFailed, wantRoute: "Store.CreatePost.QueryFailed", wantOp: "createPost",
		},
		{
			name: "post with a format that does not exist",
			call: func() error {
				return f.s.CreatePost(f.ctx, &Post{RoomID: room.ID, AuthorID: alice.ID, Format: "html", Body: "x"})
			},
			wantTag: TagQueryFailed, wantRoute: "Store.CreatePost.QueryFailed", wantOp: "createPost",
		},
		{
			name: "post with a taken ID",
			call: func() error {
				return f.s.CreatePost(f.ctx, &Post{ID: post.ID, RoomID: room.ID, AuthorID: alice.ID, Format: FormatText, Body: "x"})
			},
			wantTag: TagConflict, wantRoute: "Store.CreatePost.Conflict", wantOp: "createPost",
		},
		{
			name: "unknown post",
			call: func() error {
				_, err := f.s.PostByID(f.ctx, 999)
				return err
			},
			wantTag: TagNotFound, wantRoute: "Store.PostByID.NotFound", wantOp: "postByID",
		},
		{
			name: "post with a canceled context",
			call: func() error {
				_, err := f.s.PostByID(canceled(t), post.ID)
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.PostByID.QueryFailed", wantOp: "postByID",
		},
		{
			name: "update an unknown post",
			call: func() error {
				return f.s.UpdatePostContent(f.ctx, 999, FormatText, "x", day(0))
			},
			wantTag: TagNotFound, wantRoute: "Store.UpdatePostContent.NotFound", wantOp: "updatePostContent",
		},
		{
			name: "update with a canceled context",
			call: func() error {
				return f.s.UpdatePostContent(canceled(t), post.ID, FormatText, "x", day(0))
			},
			wantTag: TagQueryFailed, wantRoute: "Store.UpdatePostContent.QueryFailed", wantOp: "updatePostContent",
		},
		{
			name:    "mark an unknown post",
			call:    func() error { return f.s.MarkPostDeleted(f.ctx, 999, day(0)) },
			wantTag: TagNotFound, wantRoute: "Store.MarkPostDeleted.NotFound", wantOp: "markPostDeleted",
		},
		{
			name:    "mark with a canceled context",
			call:    func() error { return f.s.MarkPostDeleted(canceled(t), post.ID, day(0)) },
			wantTag: TagQueryFailed, wantRoute: "Store.MarkPostDeleted.QueryFailed", wantOp: "markPostDeleted",
		},
		{
			name:    "delete an unknown post",
			call:    func() error { return f.s.DeletePost(f.ctx, 999) },
			wantTag: TagNotFound, wantRoute: "Store.DeletePost.NotFound", wantOp: "deletePost",
		},
		{
			name:    "delete with a canceled context",
			call:    func() error { return f.s.DeletePost(canceled(t), post.ID) },
			wantTag: TagQueryFailed, wantRoute: "Store.DeletePost.QueryFailed", wantOp: "deletePost",
		},
		{
			name:    "delete the extras with a canceled context",
			call:    func() error { return f.s.DeletePostExtras(canceled(t), post.ID) },
			wantTag: TagQueryFailed, wantRoute: "Store.DeletePostExtras.QueryFailed", wantOp: "deletePostExtras",
		},
		{
			name: "replies with a canceled context",
			call: func() error {
				_, err := f.s.PostHasReplies(canceled(t), post.ID)
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.PostHasReplies.QueryFailed", wantOp: "postHasReplies",
		},
		{
			name: "feed with a canceled context",
			call: func() error {
				_, err := f.s.ListFeed(canceled(t), room.ID, 0, 10)
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.ListFeed.QueryFailed", wantOp: "listFeed",
		},
		{
			name:    "tags with a canceled context",
			call:    func() error { return f.s.ReplacePostTags(canceled(t), post.ID, []string{"a"}) },
			wantTag: TagQueryFailed, wantRoute: "Store.ReplacePostTags.QueryFailed", wantOp: "replacePostTags",
		},
		{
			name:    "tags of an unknown post",
			call:    func() error { return f.s.ReplacePostTags(f.ctx, 999, []string{"a"}) },
			wantTag: TagQueryFailed, wantRoute: "Store.ReplacePostTags.QueryFailed", wantOp: "replacePostTags",
		},
		{
			name: "tag names with a canceled context",
			call: func() error {
				_, err := f.s.TagNamesOfPosts(canceled(t), []uint64{post.ID})
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.TagNamesOfPosts.QueryFailed", wantOp: "tagNamesOfPosts",
		},
		{
			name: "comment counts with a canceled context",
			call: func() error {
				_, err := f.s.CommentCounts(canceled(t), []uint64{post.ID})
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.CommentCounts.QueryFailed", wantOp: "commentCounts",
		},
		{
			name: "suggestions with a canceled context",
			call: func() error {
				_, err := f.s.SuggestTagNames(canceled(t), alice.ID, "a", 10)
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.SuggestTagNames.QueryFailed", wantOp: "suggestTagNames",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTagged(t, tt.call(), tt.wantTag, tt.wantRoute, tt.wantOp)
		})
	}
	if n := f.count("posts"); n != 1 {
		t.Errorf("posts has %d rows after the failures, want 1", n)
	}
}
