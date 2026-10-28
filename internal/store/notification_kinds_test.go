package store

import (
	"reflect"
	"testing"

	"github.com/rm4n0s/errors"
)

func TestMentionKinds(t *testing.T) {
	t.Parallel()
	f := newFix(t)
	alice, bob, carol, dave := f.user(), f.user(), f.user(), f.user()
	room := f.room(alice)
	p, q := f.post(room, alice), f.post(room, alice)

	replace := func(postID uint64, mentioned ...MentionedUser) {
		t.Helper()
		if err := f.s.ReplacePostMentions(f.ctx, postID, mentioned); err != nil {
			t.Fatalf("ReplacePostMentions: %+v", err)
		}
	}
	mentions := func(postID uint64) []MentionedUser {
		t.Helper()
		got, err := f.s.PostMentions(f.ctx, postID)
		if err != nil {
			t.Fatalf("PostMentions: %+v", err)
		}
		return got
	}

	t.Run("each kind is saved and read back in order", func(t *testing.T) {
		want := []MentionedUser{{bob.ID, MentionUser}, {carol.ID, MentionGroup}, {dave.ID, MentionKeyword}}
		replace(p.ID, want...)
		if got := mentions(p.ID); !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
		var raw []string
		if err := f.s.db(f.ctx).Model(&Mention{}).Where("post_id = ?", p.ID).Order("id ASC").Pluck("kind", &raw).Error; err != nil {
			t.Fatalf("Pluck: %v", err)
		}
		if !reflect.DeepEqual(raw, []string{"user", "group", "keyword"}) {
			t.Errorf("stored kinds = %v", raw)
		}
	})

	t.Run("a user who stays gets the new kind in the same row", func(t *testing.T) {
		var before Mention
		if err := f.s.db(f.ctx).Where("post_id = ? AND user_id = ?", p.ID, carol.ID).First(&before).Error; err != nil {
			t.Fatalf("First: %v", err)
		}
		replace(p.ID, MentionedUser{carol.ID, MentionKeyword}, MentionedUser{bob.ID, MentionUser})
		var after Mention
		if err := f.s.db(f.ctx).Where("post_id = ? AND user_id = ?", p.ID, carol.ID).First(&after).Error; err != nil {
			t.Fatalf("First: %v", err)
		}
		if after.ID != before.ID || after.Kind != MentionKeyword {
			t.Errorf("carol's mention = %+v, was %+v; want the same row with the kind keyword", after, before)
		}
		got := mentions(p.ID)
		if len(got) != 2 {
			t.Fatalf("mentions = %v, want dave's removed", got)
		}
		// The rows keep their order: bob's was saved before carol's.
		if got[0] != (MentionedUser{bob.ID, MentionUser}) || got[1] != (MentionedUser{carol.ID, MentionKeyword}) {
			t.Errorf("mentions = %v", got)
		}
	})

	t.Run("an empty kind is a user mention", func(t *testing.T) {
		replace(q.ID, MentionedUser{UserID: bob.ID})
		got := mentions(q.ID)
		if !reflect.DeepEqual(got, []MentionedUser{{bob.ID, MentionUser}}) {
			t.Errorf("got %v", got)
		}
		replace(q.ID, MentionedUser{UserID: bob.ID, Kind: MentionGroup}, MentionedUser{UserID: carol.ID})
		got = mentions(q.ID)
		if !reflect.DeepEqual(got, []MentionedUser{{bob.ID, MentionGroup}, {carol.ID, MentionUser}}) {
			t.Errorf("got %v", got)
		}
	})

	t.Run("only the user kind gives usernames", func(t *testing.T) {
		replace(p.ID, MentionedUser{bob.ID, MentionUser}, MentionedUser{carol.ID, MentionGroup}, MentionedUser{dave.ID, MentionKeyword})
		replace(q.ID, MentionedUser{carol.ID, MentionGroup}, MentionedUser{dave.ID, MentionKeyword})
		got, err := f.s.MentionedUsernamesOfPosts(f.ctx, []uint64{p.ID, q.ID})
		if err != nil {
			t.Fatalf("MentionedUsernamesOfPosts: %+v", err)
		}
		want := map[uint64][]string{p.ID: {bob.Username}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v (a post with only group or keyword mentions has no key)", got, want)
		}
	})

	t.Run("empty replace clears every kind", func(t *testing.T) {
		replace(p.ID)
		if got := mentions(p.ID); len(got) != 0 {
			t.Errorf("mentions = %v", got)
		}
	})

	t.Run("the row default is the user kind", func(t *testing.T) {
		f.exec("INSERT INTO mentions (post_id, user_id) VALUES (?, ?)", p.ID, alice.ID)
		if got := mentions(p.ID); !reflect.DeepEqual(got, []MentionedUser{{alice.ID, MentionUser}}) {
			t.Errorf("mentions = %v", got)
		}
	})
}

func TestCreateNotificationsKinds(t *testing.T) {
	t.Parallel()
	f := newFix(t)
	alice, bob := f.user(), f.user()
	room := f.room(alice)
	post := f.post(room, bob)

	kinds := []struct {
		kind NotificationKind
		ok   bool
	}{
		{NotificationMention, true},
		{NotificationReply, true},
		{NotificationAssigned, true},
		{NotificationAddedToRoom, true},
		{NotificationGroupMention, true},
		{NotificationKeyword, true},
		{"poke", false},
		{"", false},
		{"Keyword", false},
	}
	for _, tt := range kinds {
		t.Run(string(tt.kind), func(t *testing.T) {
			rows := []Notification{{UserID: alice.ID, Kind: tt.kind, PostID: &post.ID, RoomID: &room.ID, ActorID: bob.ID}}
			err := f.s.CreateNotifications(f.ctx, rows)
			if tt.ok {
				if err != nil {
					t.Fatalf("CreateNotifications: %+v", err)
				}
				return
			}
			appErr, ok := errors.FromError(err)
			if !ok || !appErr.HasRoute("Store.CreateNotifications.QueryFailed") {
				t.Fatalf("error = %v, want the tagged QueryFailed", err)
			}
			if !isCheckFailure(errors.Unwrap(err), "chk_notifications_kind") {
				t.Errorf("error = %v, want a failure of chk_notifications_kind", err)
			}
		})
	}
	if n := f.count("notifications"); n != 6 {
		t.Errorf("%d notifications, want the 6 of the valid kinds", n)
	}

	t.Run("the new kinds are listed", func(t *testing.T) {
		rows, err := f.s.ListNotifications(f.ctx, alice.ID, 0, 10)
		if err != nil || len(rows) != 6 {
			t.Fatalf("ListNotifications = %d rows, %v", len(rows), err)
		}
		got := map[NotificationKind]bool{}
		for _, r := range rows {
			got[r.Kind] = true
			if r.Others != 0 {
				t.Errorf("%s has others = %d, want 0", r.Kind, r.Others)
			}
		}
		if !got[NotificationGroupMention] || !got[NotificationKeyword] {
			t.Errorf("kinds listed: %v", got)
		}
	})
}

func TestListNotificationsOthers(t *testing.T) {
	t.Parallel()
	f := newFix(t)
	alice, bob, carol := f.user(), f.user(), f.user()
	room := f.room(alice)
	post := f.post(room, alice)
	reply := f.notify(alice, NotificationReply, room, post, bob)
	mention := f.notify(alice, NotificationMention, room, post, bob)

	if err := f.s.FoldReply(f.ctx, reply.ID, carol.ID, 5); err != nil {
		t.Fatalf("FoldReply: %+v", err)
	}
	rows, err := f.s.ListNotifications(f.ctx, alice.ID, 0, 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("ListNotifications = %+v, %v", rows, err)
	}
	if rows[0].ID != mention.ID || rows[0].Others != 0 || rows[0].ActorID != bob.ID {
		t.Errorf("mention row = %+v", rows[0])
	}
	if rows[1].ID != reply.ID || rows[1].Others != 5 || rows[1].ActorID != carol.ID || rows[1].ActorUsername != carol.Username {
		t.Errorf("reply row = %+v, want actor carol and 5 others", rows[1])
	}
}

func TestEnabledUserSet(t *testing.T) {
	t.Parallel()
	f := newFix(t)
	alice, bob, carol := f.user(), f.user(), f.user()
	f.exec("UPDATE users SET disabled_at = ? WHERE id = ?", day(0), carol.ID)

	tests := []struct {
		name string
		ids  []uint64
		want map[uint64]bool
	}{
		{"enabled users only", []uint64{alice.ID, bob.ID, carol.ID}, map[uint64]bool{alice.ID: true, bob.ID: true}},
		{"unknown IDs are left out", []uint64{alice.ID, 9999}, map[uint64]bool{alice.ID: true}},
		{"only a disabled user", []uint64{carol.ID}, map[uint64]bool{}},
		{"only unknown IDs", []uint64{9998, 9999}, map[uint64]bool{}},
		{"a repeated ID", []uint64{bob.ID, bob.ID}, map[uint64]bool{bob.ID: true}},
		{"nil input", nil, map[uint64]bool{}},
		{"empty input", []uint64{}, map[uint64]bool{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := f.s.EnabledUserSet(f.ctx, tt.ids)
			if err != nil {
				t.Fatalf("EnabledUserSet: %+v", err)
			}
			if got == nil || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("empty input does no query", func(t *testing.T) {
		if got, err := f.s.EnabledUserSet(canceled(t), nil); err != nil || len(got) != 0 {
			t.Errorf("EnabledUserSet(canceled, nil) = %v, %v", got, err)
		}
	})
}
