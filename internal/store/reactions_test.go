package store

import (
	"reflect"
	"testing"
)

// reactionKeys reduces rows to "emoji by user" in the order given.
func reactionKeys(rows []ReactionRow) [][2]string {
	out := [][2]string{}
	for _, r := range rows {
		out = append(out, [2]string{r.Emoji, r.DisplayName})
	}
	return out
}

func TestReactionQueries(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	room := f.room(alice)
	p, q, none := f.post(room, alice), f.post(room, alice), f.post(room, alice)

	t.Run("ToggleReaction adds then removes", func(t *testing.T) {
		added, err := f.s.ToggleReaction(f.ctx, p.ID, alice.ID, "\U0001F389")
		if err != nil || !added {
			t.Fatalf("first toggle = %v, %v, want added", added, err)
		}
		if n := f.count("reactions"); n != 1 {
			t.Fatalf("%d reactions after the add", n)
		}
		added, err = f.s.ToggleReaction(f.ctx, p.ID, alice.ID, "\U0001F389")
		if err != nil || added {
			t.Fatalf("second toggle = %v, %v, want removed", added, err)
		}
		if n := f.count("reactions"); n != 0 {
			t.Fatalf("%d reactions after the removal", n)
		}
	})

	t.Run("PutReaction and DeleteReaction are idempotent", func(t *testing.T) {
		steps := []struct {
			name string
			put  bool
			want bool
			rows int
		}{
			{"put adds", true, true, 1},
			{"put again changes nothing", true, false, 1},
			{"delete removes", false, true, 0},
			{"delete again changes nothing", false, false, 0},
		}
		for _, st := range steps {
			var changed bool
			var err error
			if st.put {
				changed, err = f.s.PutReaction(f.ctx, none.ID, alice.ID, "\U0001F525")
			} else {
				changed, err = f.s.DeleteReaction(f.ctx, none.ID, alice.ID, "\U0001F525")
			}
			if err != nil || changed != st.want {
				t.Fatalf("%s = %v, %v, want %v", st.name, changed, err, st.want)
			}
			rows, err := f.s.ReactionsOfPosts(f.ctx, []uint64{none.ID})
			if err != nil || len(rows[none.ID]) != st.rows {
				t.Fatalf("%s: %d rows, %v, want %d", st.name, len(rows[none.ID]), err, st.rows)
			}
		}
	})

	t.Run("ReactionsOfPosts in order of first use, with names", func(t *testing.T) {
		for _, r := range []struct {
			post  *Post
			user  *User
			emoji string
		}{
			{p, bob, "b"}, {p, alice, "a"}, {q, alice, "a"}, {p, alice, "b"},
		} {
			if added, err := f.s.ToggleReaction(f.ctx, r.post.ID, r.user.ID, r.emoji); err != nil || !added {
				t.Fatalf("toggle = %v, %v", added, err)
			}
		}
		got, err := f.s.ReactionsOfPosts(f.ctx, []uint64{p.ID, q.ID, none.ID})
		if err != nil {
			t.Fatalf("ReactionsOfPosts: %+v", err)
		}
		if _, ok := got[none.ID]; ok || len(got) != 2 {
			t.Errorf("keys = %v, want the two posts with reactions", got)
		}
		wantP := [][2]string{{"b", bob.DisplayName}, {"a", alice.DisplayName}, {"b", alice.DisplayName}}
		if !reflect.DeepEqual(reactionKeys(got[p.ID]), wantP) {
			t.Errorf("reactions of p = %v, want %v", reactionKeys(got[p.ID]), wantP)
		}
		for _, r := range got[p.ID] {
			if r.PostID != p.ID || r.ID == 0 || r.UserID == 0 {
				t.Errorf("row = %+v", r)
			}
		}
		if empty, err := f.s.ReactionsOfPosts(f.ctx, nil); err != nil || len(empty) != 0 {
			t.Errorf("ReactionsOfPosts(nil) = %v, %v", empty, err)
		}
	})
}

func TestReactionQueryTags(t *testing.T) {
	f := newFix(t)
	alice := f.user()
	post := f.post(f.room(alice), alice)
	tests := []struct {
		name      string
		call      func() error
		wantRoute string
		wantOp    string
	}{
		{
			name: "toggle on an unknown post",
			call: func() error {
				_, err := f.s.ToggleReaction(f.ctx, 999, alice.ID, "x")
				return err
			},
			wantRoute: "Store.ToggleReaction.QueryFailed", wantOp: "toggleReaction",
		},
		{
			name: "toggle by an unknown user",
			call: func() error {
				_, err := f.s.ToggleReaction(f.ctx, post.ID, 999, "x")
				return err
			},
			wantRoute: "Store.ToggleReaction.QueryFailed", wantOp: "toggleReaction",
		},
		{
			name: "toggle with a canceled context",
			call: func() error {
				_, err := f.s.ToggleReaction(canceled(t), post.ID, alice.ID, "x")
				return err
			},
			wantRoute: "Store.ToggleReaction.QueryFailed", wantOp: "toggleReaction",
		},
		{
			name: "put on an unknown post",
			call: func() error {
				_, err := f.s.PutReaction(f.ctx, 999, alice.ID, "x")
				return err
			},
			wantRoute: "Store.PutReaction.QueryFailed", wantOp: "putReaction",
		},
		{
			name: "put with a canceled context",
			call: func() error {
				_, err := f.s.PutReaction(canceled(t), post.ID, alice.ID, "x")
				return err
			},
			wantRoute: "Store.PutReaction.QueryFailed", wantOp: "putReaction",
		},
		{
			name: "delete with a canceled context",
			call: func() error {
				_, err := f.s.DeleteReaction(canceled(t), post.ID, alice.ID, "x")
				return err
			},
			wantRoute: "Store.DeleteReaction.QueryFailed", wantOp: "deleteReaction",
		},
		{
			name: "reactions with a canceled context",
			call: func() error {
				_, err := f.s.ReactionsOfPosts(canceled(t), []uint64{post.ID})
				return err
			},
			wantRoute: "Store.ReactionsOfPosts.QueryFailed", wantOp: "reactionsOfPosts",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTagged(t, tt.call(), TagQueryFailed, tt.wantRoute, tt.wantOp)
		})
	}
	if n := f.count("reactions"); n != 0 {
		t.Errorf("%d reactions after the failures", n)
	}
}
