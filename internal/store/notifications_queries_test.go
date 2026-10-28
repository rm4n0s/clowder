package store

import (
	"reflect"
	"testing"
	"time"
)

// notify inserts a notification for the user.
func (f *fix) notify(user *User, kind NotificationKind, room *Room, post *Post, actor *User) *Notification {
	f.tb.Helper()
	n := &Notification{UserID: user.ID, Kind: kind, RoomID: &room.ID, ActorID: actor.ID}
	if post != nil {
		n.PostID = &post.ID
	}
	f.create(n)
	return n
}

func TestEnabledUsersByUsername(t *testing.T) {
	f := newFix(t)
	alice, bob, carol, dave := f.user(), f.user(), f.user(), f.user()
	visible := f.room(alice)
	private := &Room{Name: "private", Visibility: VisibilityPrivate, CreatedBy: alice.ID}
	f.create(private)
	f.create(&RoomMember{RoomID: private.ID, UserID: alice.ID})
	f.create(&RoomMember{RoomID: private.ID, UserID: bob.ID})
	f.exec("UPDATE users SET disabled_at = ? WHERE id = ?", day(0), dave.ID)
	names := []string{bob.Username, carol.Username, dave.Username, "nobody"}

	tests := []struct {
		name    string
		room    *Room
		private bool
		names   []string
		want    []string
	}{
		{"a visible room: every enabled user", visible, false, names, []string{bob.Username, carol.Username}},
		{"a private room: only the enabled members", private, true, names, []string{bob.Username}},
		{"no names", visible, false, nil, nil},
		{"only unknown names", visible, false, []string{"nobody"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users, err := f.s.EnabledUsersByUsername(f.ctx, tt.room.ID, tt.private, tt.names)
			if err != nil {
				t.Fatalf("EnabledUsersByUsername: %+v", err)
			}
			var got []string
			for _, u := range users {
				got = append(got, u.Username)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnabledReaders(t *testing.T) {
	f := newFix(t)
	alice, bob, carol, dave := f.user(), f.user(), f.user(), f.user()
	visible := f.room(alice)
	private := &Room{Name: "private", Visibility: VisibilityPrivate, CreatedBy: alice.ID}
	f.create(private)
	f.create(&RoomMember{RoomID: private.ID, UserID: alice.ID})
	f.create(&RoomMember{RoomID: private.ID, UserID: bob.ID})
	f.exec("UPDATE users SET disabled_at = ? WHERE id = ?", day(0), bob.ID)
	f.create(&RoomMember{RoomID: private.ID, UserID: dave.ID})
	ids := []uint64{alice.ID, bob.ID, carol.ID, dave.ID, 9999}

	tests := []struct {
		name    string
		room    *Room
		private bool
		ids     []uint64
		want    map[uint64]bool
	}{
		{"visible: every enabled user who exists", visible, false, ids, map[uint64]bool{alice.ID: true, carol.ID: true, dave.ID: true}},
		{"private: the enabled members", private, true, ids, map[uint64]bool{alice.ID: true, dave.ID: true}},
		{"no IDs", visible, false, nil, map[uint64]bool{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := f.s.EnabledReaders(f.ctx, tt.room.ID, tt.private, tt.ids)
			if err != nil {
				t.Fatalf("EnabledReaders: %+v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMentionQueries(t *testing.T) {
	f := newFix(t)
	alice, bob, carol := f.user(), f.user(), f.user()
	room := f.room(alice)
	p, q, none := f.post(room, alice), f.post(room, alice), f.post(room, alice)

	ids := func(postID uint64) []uint64 {
		t.Helper()
		got, err := f.s.PostMentions(f.ctx, postID)
		if err != nil {
			t.Fatalf("PostMentions: %+v", err)
		}
		var out []uint64
		for _, m := range got {
			out = append(out, m.UserID)
		}
		return out
	}
	replace := func(postID uint64, users ...*User) {
		t.Helper()
		var mentioned []MentionedUser
		for _, u := range users {
			mentioned = append(mentioned, MentionedUser{UserID: u.ID, Kind: MentionUser})
		}
		if err := f.s.ReplacePostMentions(f.ctx, postID, mentioned); err != nil {
			t.Fatalf("ReplacePostMentions: %+v", err)
		}
	}

	t.Run("replace adds, keeps and removes", func(t *testing.T) {
		replace(p.ID, bob, carol)
		if got := ids(p.ID); !reflect.DeepEqual(got, []uint64{bob.ID, carol.ID}) {
			t.Fatalf("after the first replace: %v", got)
		}
		var firstRow Mention
		if err := f.s.db(f.ctx).Where("post_id = ? AND user_id = ?", p.ID, bob.ID).First(&firstRow).Error; err != nil {
			t.Fatalf("First: %v", err)
		}
		replace(p.ID, bob, alice)
		if got := ids(p.ID); !reflect.DeepEqual(got, []uint64{bob.ID, alice.ID}) {
			t.Fatalf("after the second replace: %v", got)
		}
		var sameRow Mention
		if err := f.s.db(f.ctx).Where("post_id = ? AND user_id = ?", p.ID, bob.ID).First(&sameRow).Error; err != nil || sameRow.ID != firstRow.ID {
			t.Errorf("the mention that stays was rewritten: %v -> %v (%v)", firstRow.ID, sameRow.ID, err)
		}
		replace(p.ID)
		if got := ids(p.ID); len(got) != 0 {
			t.Errorf("after an empty replace: %v", got)
		}
	})

	t.Run("replace leaves other posts alone", func(t *testing.T) {
		replace(q.ID, bob)
		replace(p.ID, carol)
		if got := ids(q.ID); !reflect.DeepEqual(got, []uint64{bob.ID}) {
			t.Errorf("q mentions %v", got)
		}
	})

	t.Run("usernames of posts in the order saved", func(t *testing.T) {
		replace(p.ID, carol, bob)
		got, err := f.s.MentionedUsernamesOfPosts(f.ctx, []uint64{p.ID, q.ID, none.ID})
		if err != nil {
			t.Fatalf("MentionedUsernamesOfPosts: %+v", err)
		}
		want := map[uint64][]string{p.ID: {carol.Username, bob.Username}, q.ID: {bob.Username}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
		if empty, err := f.s.MentionedUsernamesOfPosts(f.ctx, nil); err != nil || len(empty) != 0 {
			t.Errorf("MentionedUsernamesOfPosts(nil) = %v, %v", empty, err)
		}
	})
}

func TestNotificationQueries(t *testing.T) {
	f := newFix(t)
	alice, bob, carol := f.user(), f.user(), f.user()
	visible := f.room(alice)
	private := &Room{Name: "private", Visibility: VisibilityPrivate, CreatedBy: alice.ID}
	f.create(private)
	f.create(&RoomMember{RoomID: private.ID, UserID: alice.ID})
	f.create(&RoomMember{RoomID: private.ID, UserID: bob.ID})
	root := f.post(visible, alice)
	comment := f.comment(root, bob)
	privatePost := f.post(private, alice)
	col := f.column(visible)
	task := f.task(root, col)
	task.Title = "Fix it"
	f.exec("UPDATE tasks SET title = ? WHERE id = ?", task.Title, task.ID)

	t.Run("CreateNotifications sets the IDs and the time", func(t *testing.T) {
		rows := []Notification{
			{UserID: bob.ID, Kind: NotificationMention, PostID: &root.ID, RoomID: &visible.ID, ActorID: alice.ID},
			{UserID: bob.ID, Kind: NotificationReply, PostID: &comment.ID, RoomID: &visible.ID, ActorID: carol.ID},
			{UserID: bob.ID, Kind: NotificationAssigned, PostID: &root.ID, TaskID: &task.ID, RoomID: &visible.ID, ActorID: alice.ID},
			{UserID: bob.ID, Kind: NotificationMention, PostID: &privatePost.ID, RoomID: &private.ID, ActorID: alice.ID},
			{UserID: carol.ID, Kind: NotificationAddedToRoom, RoomID: &visible.ID, ActorID: alice.ID},
		}
		f.clk.Advance(time.Minute)
		if err := f.s.CreateNotifications(f.ctx, rows); err != nil {
			t.Fatalf("CreateNotifications: %+v", err)
		}
		for i, r := range rows {
			if r.ID == 0 || (i > 0 && r.ID <= rows[i-1].ID) {
				t.Errorf("row %d ID = %d", i, r.ID)
			}
			requireSameTime(t, "CreatedAt", f.clk.Now(), r.CreatedAt)
		}
		if err := f.s.CreateNotifications(f.ctx, nil); err != nil {
			t.Errorf("CreateNotifications(nil): %+v", err)
		}
	})

	t.Run("ListNotifications: newest first, with what a page needs", func(t *testing.T) {
		rows, err := f.s.ListNotifications(f.ctx, bob.ID, 0, 10)
		if err != nil {
			t.Fatalf("ListNotifications: %+v", err)
		}
		if len(rows) != 4 {
			t.Fatalf("got %d rows: %+v", len(rows), rows)
		}
		for i := 1; i < len(rows); i++ {
			if rows[i].ID >= rows[i-1].ID {
				t.Errorf("not newest first: %d then %d", rows[i-1].ID, rows[i].ID)
			}
		}
		inPrivate, assigned, reply, mention := rows[0], rows[1], rows[2], rows[3]
		if inPrivate.RoomName != "private" || inPrivate.PostID != privatePost.ID || inPrivate.RootID != privatePost.ID {
			t.Errorf("in private = %+v", inPrivate)
		}
		if assigned.Kind != NotificationAssigned || assigned.TaskID != task.ID || assigned.TaskTitle != "Fix it" || assigned.RootID != root.ID {
			t.Errorf("assigned = %+v", assigned)
		}
		if reply.PostID != comment.ID || reply.RootID != root.ID || reply.ActorID != carol.ID ||
			reply.ActorUsername != carol.Username || reply.ActorDisplayName != carol.DisplayName || reply.RoomID != visible.ID {
			t.Errorf("reply = %+v", reply)
		}
		if mention.RootID != root.ID || mention.ReadAt != nil || mention.TaskID != 0 || mention.TaskTitle != "" {
			t.Errorf("mention = %+v", mention)
		}
		requireSameTime(t, "CreatedAt", f.clk.Now(), mention.CreatedAt)

		added, err := f.s.ListNotifications(f.ctx, carol.ID, 0, 10)
		if err != nil || len(added) != 1 || added[0].Kind != NotificationAddedToRoom || added[0].PostID != 0 || added[0].RootID != 0 {
			t.Errorf("carol's = %+v, %v", added, err)
		}
	})

	t.Run("ListNotifications: the cursor and the limit", func(t *testing.T) {
		first, err := f.s.ListNotifications(f.ctx, bob.ID, 0, 2)
		if err != nil || len(first) != 2 {
			t.Fatalf("first = %+v, %v", first, err)
		}
		next, err := f.s.ListNotifications(f.ctx, bob.ID, first[1].ID, 2)
		if err != nil || len(next) != 2 || next[0].ID >= first[1].ID {
			t.Fatalf("next = %+v, %v", next, err)
		}
		end, err := f.s.ListNotifications(f.ctx, bob.ID, next[1].ID, 2)
		if err != nil || len(end) != 0 {
			t.Errorf("past the end = %+v, %v", end, err)
		}
	})

	t.Run("a room the user cannot read hides its notifications", func(t *testing.T) {
		f.exec("DELETE FROM room_members WHERE room_id = ? AND user_id = ?", private.ID, bob.ID)
		rows, err := f.s.ListNotifications(f.ctx, bob.ID, 0, 10)
		if err != nil || len(rows) != 3 {
			t.Fatalf("after leaving: %d rows, %v", len(rows), err)
		}
		if n, err := f.s.CountUnreadNotifications(f.ctx, bob.ID); err != nil || n != 3 {
			t.Errorf("CountUnreadNotifications after leaving = %d, %v, want 3", n, err)
		}
		f.create(&RoomMember{RoomID: private.ID, UserID: bob.ID})
		if n, err := f.s.CountUnreadNotifications(f.ctx, bob.ID); err != nil || n != 4 {
			t.Errorf("CountUnreadNotifications after returning = %d, %v, want 4", n, err)
		}
	})

	t.Run("MarkNotificationsRead is limited by ID and by user", func(t *testing.T) {
		rows, err := f.s.ListNotifications(f.ctx, bob.ID, 0, 10)
		if err != nil {
			t.Fatalf("ListNotifications: %+v", err)
		}
		f.clk.Advance(time.Hour)
		// Up to the second oldest: the two oldest are read.
		n, err := f.s.MarkNotificationsRead(f.ctx, bob.ID, rows[2].ID)
		if err != nil || n != 2 {
			t.Fatalf("MarkNotificationsRead = %d, %v, want 2", n, err)
		}
		if n, err := f.s.CountUnreadNotifications(f.ctx, bob.ID); err != nil || n != 2 {
			t.Errorf("unread = %d, %v, want 2", n, err)
		}
		if n, err := f.s.CountUnreadNotifications(f.ctx, carol.ID); err != nil || n != 1 {
			t.Errorf("carol's unread = %d, %v, want 1", n, err)
		}
		if n, err := f.s.MarkNotificationsRead(f.ctx, bob.ID, rows[2].ID); err != nil || n != 0 {
			t.Errorf("marking again = %d, %v, want 0", n, err)
		}
		// Another user's notifications are untouched, whatever the ID.
		if n, err := f.s.MarkNotificationsRead(f.ctx, alice.ID, rows[0].ID+100); err != nil || n != 0 {
			t.Errorf("alice marked %d of other users' notifications, %v", n, err)
		}
		after, err := f.s.ListNotifications(f.ctx, bob.ID, 0, 10)
		if err != nil {
			t.Fatalf("ListNotifications: %+v", err)
		}
		for i, r := range after {
			if wantRead := i >= 2; (r.ReadAt != nil) != wantRead {
				t.Errorf("row %d ReadAt = %v", i, r.ReadAt)
			}
			if r.ReadAt != nil {
				requireSameTime(t, "ReadAt", f.clk.Now(), *r.ReadAt)
			}
		}
	})
}

func TestNotificationsCascade(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	room := f.room(alice)
	p, q := f.post(room, alice), f.post(room, alice)
	col := f.column(room)
	task := f.task(p, col)
	f.notify(bob, NotificationMention, room, p, alice)
	f.create(&Notification{UserID: bob.ID, Kind: NotificationAssigned, PostID: &p.ID, TaskID: &task.ID, RoomID: &room.ID, ActorID: alice.ID})
	f.notify(bob, NotificationMention, room, q, alice)
	f.create(&Notification{UserID: bob.ID, Kind: NotificationAddedToRoom, RoomID: &room.ID, ActorID: alice.ID})

	if err := f.s.DeleteTasks(f.ctx, []uint64{task.ID}); err != nil {
		t.Fatalf("DeleteTasks: %+v", err)
	}
	if n := f.count("notifications"); n != 3 {
		t.Errorf("%d notifications after the task went, want 3", n)
	}
	if err := f.s.DeletePostExtras(f.ctx, p.ID); err != nil {
		t.Fatalf("DeletePostExtras: %+v", err)
	}
	if n := f.count("notifications"); n != 2 {
		t.Errorf("%d notifications after the extras went, want 2", n)
	}
	if err := f.s.DeletePost(f.ctx, q.ID); err != nil {
		t.Fatalf("DeletePost: %+v", err)
	}
	if n := f.count("notifications"); n != 1 {
		t.Errorf("%d notifications after the post went, want 1", n)
	}
	if err := f.s.DeleteRoom(f.ctx, room.ID); err != nil {
		t.Fatalf("DeleteRoom: %+v", err)
	}
	if n := f.count("notifications"); n != 0 {
		t.Errorf("%d notifications after the room went, want 0", n)
	}
}

func TestNotificationQueryTags(t *testing.T) {
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
		{
			name: "EnabledUsersByUsername",
			call: func() error {
				_, err := f.s.EnabledUsersByUsername(canceled(t), room.ID, true, []string{"x"})
				return err
			},
			wantRoute: "Store.EnabledUsersByUsername.QueryFailed", wantOp: "enabledUsersByUsername",
		},
		{
			name: "EnabledReaders",
			call: func() error {
				_, err := f.s.EnabledReaders(canceled(t), room.ID, true, []uint64{alice.ID})
				return err
			},
			wantRoute: "Store.EnabledReaders.QueryFailed", wantOp: "enabledReaders",
		},
		{
			name: "PostMentions",
			call: func() error {
				_, err := f.s.PostMentions(canceled(t), post.ID)
				return err
			},
			wantRoute: "Store.PostMentions.QueryFailed", wantOp: "postMentions",
		},
		{
			name: "ReplacePostMentions",
			call: func() error {
				return f.s.ReplacePostMentions(canceled(t), post.ID, []MentionedUser{{UserID: alice.ID}})
			},
			wantRoute: "Store.ReplacePostMentions.QueryFailed", wantOp: "replacePostMentions",
		},
		{
			name:      "ReplacePostMentions of a user who does not exist",
			call:      func() error { return f.s.ReplacePostMentions(f.ctx, post.ID, []MentionedUser{{UserID: 9999}}) },
			wantRoute: "Store.ReplacePostMentions.QueryFailed", wantOp: "replacePostMentions",
		},
		{
			name: "MentionedUsernamesOfPosts",
			call: func() error {
				_, err := f.s.MentionedUsernamesOfPosts(canceled(t), []uint64{post.ID})
				return err
			},
			wantRoute: "Store.MentionedUsernamesOfPosts.QueryFailed", wantOp: "mentionedUsernamesOfPosts",
		},
		{
			name: "CreateNotifications",
			call: func() error {
				return f.s.CreateNotifications(f.ctx, []Notification{{UserID: 9999, Kind: NotificationMention, ActorID: alice.ID}})
			},
			wantRoute: "Store.CreateNotifications.QueryFailed", wantOp: "createNotifications",
		},
		{
			name: "ListNotifications",
			call: func() error {
				_, err := f.s.ListNotifications(canceled(t), alice.ID, 0, 10)
				return err
			},
			wantRoute: "Store.ListNotifications.QueryFailed", wantOp: "listNotifications",
		},
		{
			name: "CountUnreadNotifications",
			call: func() error {
				_, err := f.s.CountUnreadNotifications(canceled(t), alice.ID)
				return err
			},
			wantRoute: "Store.CountUnreadNotifications.QueryFailed", wantOp: "countUnreadNotifications",
		},
		{
			name: "MarkNotificationsRead",
			call: func() error {
				_, err := f.s.MarkNotificationsRead(canceled(t), alice.ID, 10)
				return err
			},
			wantRoute: "Store.MarkNotificationsRead.QueryFailed", wantOp: "markNotificationsRead",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTagged(t, tt.call(), TagQueryFailed, tt.wantRoute, tt.wantOp)
		})
	}
	if n := f.count("notifications") + f.count("mentions"); n != 0 {
		t.Errorf("%d rows after the failures", n)
	}
}
