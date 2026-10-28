package store

import (
	"context"
	"strconv"
	"testing"
)

// roomReadRow is a row of room_reads as the tests read it.
type roomReadRow struct {
	UserID     uint64
	RoomID     uint64
	LastSeenID uint64
}

// roomReadRows reads every row of room_reads, ordered by room and user.
func roomReadRows(f *fix) []roomReadRow {
	f.tb.Helper()
	var rows []roomReadRow
	err := f.s.db(f.ctx).Raw("SELECT user_id, room_id, last_seen_id FROM room_reads ORDER BY room_id, user_id").Scan(&rows).Error
	if err != nil {
		f.tb.Fatalf("read room_reads: %v", err)
	}
	return rows
}

// requireRoomReadsMatchMemberships fails the test unless room_reads has one
// row for each membership and no other.
func requireRoomReadsMatchMemberships(f *fix) {
	f.tb.Helper()
	var missing, extra int64
	err := f.s.db(f.ctx).Raw(`SELECT count(*) FROM room_members m
WHERE NOT EXISTS (SELECT 1 FROM room_reads r WHERE r.user_id = m.user_id AND r.room_id = m.room_id)`).Scan(&missing).Error
	if err != nil {
		f.tb.Fatal(err)
	}
	err = f.s.db(f.ctx).Raw(`SELECT count(*) FROM room_reads r
WHERE NOT EXISTS (SELECT 1 FROM room_members m WHERE r.user_id = m.user_id AND r.room_id = m.room_id)`).Scan(&extra).Error
	if err != nil {
		f.tb.Fatal(err)
	}
	if missing != 0 || extra != 0 {
		f.tb.Errorf("room_reads: %d memberships without a row and %d rows without a membership, want none", missing, extra)
	}
}

// join makes the user a member of the room through the store, as the
// services do.
func (f *fix) join(room *Room, user *User) *RoomMember {
	f.tb.Helper()
	m := &RoomMember{RoomID: room.ID, UserID: user.ID}
	if err := f.s.CreateRoomMember(f.ctx, m); err != nil {
		f.tb.Fatalf("CreateRoomMember: %+v", err)
	}
	return m
}

// TestEveryMembershipHasARoomRead checks the invariant of SPEC §6.3: after a
// series of joins, additions, leaves and rejoins every membership has a row
// of room_reads at the room's highest ID at the time, and no other row stays.
func TestEveryMembershipHasARoomRead(t *testing.T) {
	f := newFix(t)
	alice, bob, carol := f.user(), f.user(), f.user()
	room, other := f.room(alice), f.room(alice)

	a := f.join(room, alice)
	requireRoomReadsMatchMemberships(f)
	first := f.post(room, alice)
	f.comment(first, alice)
	second := f.post(room, alice)
	f.post(other, alice)

	b := f.join(room, bob)
	adder := alice.ID
	c := &RoomMember{RoomID: room.ID, UserID: carol.ID, AddedBy: &adder}
	if err := f.s.CreateRoomMember(f.ctx, c); err != nil {
		t.Fatalf("CreateRoomMember (added): %+v", err)
	}
	requireRoomReadsMatchMemberships(f)
	f.join(other, bob)

	wantNewest := second.ID
	for _, r := range roomReadRows(f) {
		if r.RoomID == room.ID && r.UserID != alice.ID && r.LastSeenID != wantNewest {
			t.Errorf("row of user %d in the room = %d, want the newest ID %d", r.UserID, r.LastSeenID, wantNewest)
		}
	}

	// Alice's own row was made before any post: it stays at 0 until she reads.
	if got, ok, err := f.s.RoomLastSeen(f.ctx, alice.ID, room.ID); err != nil || !ok || got != 0 {
		t.Errorf("RoomLastSeen(alice) = %d, %v, %v; want 0, true, nil", got, ok, err)
	}

	// Bob leaves, a post comes, and he joins again: he starts at the new top.
	if err := f.s.DeleteRoomMember(f.ctx, b.ID); err != nil {
		t.Fatalf("DeleteRoomMember: %+v", err)
	}
	requireRoomReadsMatchMemberships(f)
	if _, ok, err := f.s.RoomLastSeen(f.ctx, bob.ID, room.ID); err != nil || ok {
		t.Errorf("RoomLastSeen(bob) after leaving = _, %v, %v; want no row", ok, err)
	}
	third := f.post(room, alice)
	f.join(room, bob)
	requireRoomReadsMatchMemberships(f)
	if got, ok, err := f.s.RoomLastSeen(f.ctx, bob.ID, room.ID); err != nil || !ok || got != third.ID {
		t.Errorf("RoomLastSeen(bob) after rejoining = %d, %v, %v; want %d", got, ok, err, third.ID)
	}

	// Everyone leaves; no row stays.
	for _, m := range []*RoomMember{a, c} {
		if err := f.s.DeleteRoomMember(f.ctx, m.ID); err != nil {
			t.Fatalf("DeleteRoomMember: %+v", err)
		}
	}
	requireRoomReadsMatchMemberships(f)

	// A room that goes takes its rows.
	if err := f.s.DeleteRoom(f.ctx, other.ID); err != nil {
		t.Fatalf("DeleteRoom: %+v", err)
	}
	requireRoomReadsMatchMemberships(f)
	for _, r := range roomReadRows(f) {
		if r.RoomID == other.ID {
			t.Errorf("the row %+v of a deleted room stays", r)
		}
	}
}

// TestRoomUnreadCounts is the table of the counts of SPEC §7.17.
func TestRoomUnreadCounts(t *testing.T) {
	tests := []struct {
		name string
		// setup makes the data in a room that alice is a member of and returns
		// the counts alice must get for it.
		setup func(f *fix, alice, bob *User, room *Room) (posts, mentions int)
	}{
		{"nothing", func(f *fix, alice, bob *User, room *Room) (int, int) { return 0, 0 }},
		{"posts of others after the mark", func(f *fix, alice, bob *User, room *Room) (int, int) {
			f.post(room, bob)
			f.post(room, bob)
			return 2, 0
		}},
		{"own posts are not counted", func(f *fix, alice, bob *User, room *Room) (int, int) {
			f.post(room, alice)
			f.post(room, bob)
			return 1, 0
		}},
		{"comments are not counted", func(f *fix, alice, bob *User, room *Room) (int, int) {
			p := f.post(room, alice)
			f.comment(p, bob)
			f.comment(p, bob)
			return 0, 0
		}},
		{"a deleted post is not counted", func(f *fix, alice, bob *User, room *Room) (int, int) {
			p := f.post(room, bob)
			f.post(room, bob)
			f.exec("UPDATE posts SET deleted_at = ? WHERE id = ?", day(0), p.ID)
			return 1, 0
		}},
		{"posts before the mark are not counted", func(f *fix, alice, bob *User, room *Room) (int, int) {
			f.post(room, bob)
			last := f.post(room, bob)
			if _, err := f.s.MoveRoomRead(f.ctx, alice.ID, room.ID, last.ID); err != nil {
				f.tb.Fatal(err)
			}
			f.post(room, bob)
			return 1, 0
		}},
		{"a comment ID above the mark hides the posts below it", func(f *fix, alice, bob *User, room *Room) (int, int) {
			p := f.post(room, bob)
			c := f.comment(p, bob)
			if _, err := f.s.MoveRoomRead(f.ctx, alice.ID, room.ID, c.ID); err != nil {
				f.tb.Fatal(err)
			}
			return 0, 0
		}},
		{"the three kinds of mention are counted", func(f *fix, alice, bob *User, room *Room) (int, int) {
			p := f.post(room, bob)
			for _, k := range []NotificationKind{NotificationMention, NotificationGroupMention, NotificationKeyword} {
				f.create(&Notification{UserID: alice.ID, Kind: k, PostID: &p.ID, RoomID: &room.ID, ActorID: bob.ID})
			}
			return 1, 3
		}},
		{"other kinds are not mentions", func(f *fix, alice, bob *User, room *Room) (int, int) {
			p := f.post(room, bob)
			for _, k := range []NotificationKind{NotificationReply, NotificationAddedToRoom} {
				f.create(&Notification{UserID: alice.ID, Kind: k, PostID: &p.ID, RoomID: &room.ID, ActorID: bob.ID})
			}
			return 1, 0
		}},
		{"a read mention is not counted", func(f *fix, alice, bob *User, room *Room) (int, int) {
			p := f.post(room, bob)
			f.create(&Notification{UserID: alice.ID, Kind: NotificationMention, PostID: &p.ID, RoomID: &room.ID, ActorID: bob.ID, ReadAt: ptr(day(0))})
			f.create(&Notification{UserID: alice.ID, Kind: NotificationMention, PostID: &p.ID, RoomID: &room.ID, ActorID: bob.ID})
			return 1, 1
		}},
		{"a mention of someone else is not counted", func(f *fix, alice, bob *User, room *Room) (int, int) {
			p := f.post(room, alice)
			f.create(&Notification{UserID: bob.ID, Kind: NotificationMention, PostID: &p.ID, RoomID: &room.ID, ActorID: alice.ID})
			return 0, 0
		}},
		{"more than the cap of 100 posts", func(f *fix, alice, bob *User, room *Room) (int, int) {
			for range UnreadPostsCap + 20 {
				f.post(room, bob)
			}
			return UnreadPostsCap, 0
		}},
		{"exactly the cap", func(f *fix, alice, bob *User, room *Room) (int, int) {
			for range UnreadPostsCap {
				f.post(room, bob)
			}
			return UnreadPostsCap, 0
		}},
		{"one under the cap", func(f *fix, alice, bob *User, room *Room) (int, int) {
			for range UnreadPostsCap - 1 {
				f.post(room, bob)
			}
			return UnreadPostsCap - 1, 0
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFix(t)
			alice, bob := f.user(), f.user()
			room := f.room(alice)
			f.join(room, alice)
			f.join(room, bob)
			// A second room, with its own post, must not leak in.
			noise := f.room(bob)
			f.join(noise, alice)
			f.join(noise, bob)
			f.post(noise, bob)

			wantPosts, wantMentions := tt.setup(f, alice, bob, room)
			got, ok, err := f.s.RoomUnreadOf(f.ctx, alice.ID, room.ID)
			if err != nil || !ok {
				t.Fatalf("RoomUnreadOf = %+v, %v, %v", got, ok, err)
			}
			if got.Posts != wantPosts || got.Mentions != wantMentions {
				t.Errorf("RoomUnreadOf = %d posts, %d mentions; want %d, %d", got.Posts, got.Mentions, wantPosts, wantMentions)
			}
			all, err := f.s.RoomUnreadCounts(f.ctx, alice.ID)
			if err != nil {
				t.Fatalf("RoomUnreadCounts: %+v", err)
			}
			byRoom := map[uint64]RoomUnread{}
			for _, u := range all {
				byRoom[u.RoomID] = u
			}
			if len(all) != 2 || byRoom[room.ID] != got {
				t.Errorf("RoomUnreadCounts = %+v, want the two rooms of alice with %+v for the room", all, got)
			}
			if byRoom[noise.ID].Posts != 1 {
				t.Errorf("the other room has %d unread posts, want its 1", byRoom[noise.ID].Posts)
			}
		})
	}
}

// TestRoomUnreadCountsOfNonMembers checks that a room the user is not a member
// of has no counts, and that a membership without a row counts every post of
// others.
func TestRoomUnreadCountsOfNonMembers(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	room := f.room(bob)
	f.join(room, bob)
	f.post(room, bob)

	if _, ok, err := f.s.RoomUnreadOf(f.ctx, alice.ID, room.ID); err != nil || ok {
		t.Errorf("RoomUnreadOf of a non-member: found %v, err %v; want not found", ok, err)
	}
	all, err := f.s.RoomUnreadCounts(f.ctx, alice.ID)
	if err != nil || len(all) != 0 {
		t.Errorf("RoomUnreadCounts of a non-member = %+v, %v; want none", all, err)
	}

	// A membership made without the store method has no row.
	f.create(&RoomMember{RoomID: room.ID, UserID: alice.ID})
	got, ok, err := f.s.RoomUnreadOf(f.ctx, alice.ID, room.ID)
	if err != nil || !ok || got.Posts != 1 {
		t.Errorf("RoomUnreadOf without a row = %+v, %v, %v; want 1 unread post", got, ok, err)
	}
}

// TestMoveRoomRead checks that the mark only moves forward and that it makes
// the row when there is none.
func TestMoveRoomRead(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	room := f.room(alice)
	f.join(room, alice)
	f.join(room, bob)
	p1, p2, p3 := f.post(room, bob), f.post(room, bob), f.post(room, bob)

	tests := []struct {
		name      string
		upTo      uint64
		wantMoved bool
		wantMark  uint64
	}{
		{"forward", p2.ID, true, p2.ID},
		{"the same place", p2.ID, false, p2.ID},
		{"back", p1.ID, false, p2.ID},
		{"to zero", 0, false, p2.ID},
		{"to the newest", p3.ID, true, p3.ID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			moved, err := f.s.MoveRoomRead(f.ctx, alice.ID, room.ID, tt.upTo)
			if err != nil {
				t.Fatalf("MoveRoomRead: %+v", err)
			}
			if moved != tt.wantMoved {
				t.Errorf("moved = %v, want %v", moved, tt.wantMoved)
			}
			if got, _, err := f.s.RoomLastSeen(f.ctx, alice.ID, room.ID); err != nil || got != tt.wantMark {
				t.Errorf("mark = %d, %v; want %d", got, err, tt.wantMark)
			}
		})
	}

	t.Run("no row yet", func(t *testing.T) {
		carol := f.user()
		f.create(&RoomMember{RoomID: room.ID, UserID: carol.ID})
		moved, err := f.s.MoveRoomRead(f.ctx, carol.ID, room.ID, p2.ID)
		if err != nil || !moved {
			t.Fatalf("MoveRoomRead = %v, %v; want a new row", moved, err)
		}
		if got, ok, err := f.s.RoomLastSeen(f.ctx, carol.ID, room.ID); err != nil || !ok || got != p2.ID {
			t.Errorf("mark = %d, %v, %v; want %d", got, ok, err, p2.ID)
		}
	})
}

// TestRoomMaxPostID checks the highest ID of a room: comments and deleted
// posts count, other rooms do not.
func TestRoomMaxPostID(t *testing.T) {
	f := newFix(t)
	alice := f.user()
	room, other := f.room(alice), f.room(alice)
	if got, err := f.s.RoomMaxPostID(f.ctx, room.ID); err != nil || got != 0 {
		t.Errorf("RoomMaxPostID of an empty room = %d, %v; want 0", got, err)
	}
	p := f.post(room, alice)
	c := f.comment(p, alice)
	f.post(other, alice)
	f.exec("UPDATE posts SET deleted_at = ? WHERE id = ?", day(0), c.ID)
	if got, err := f.s.RoomMaxPostID(f.ctx, room.ID); err != nil || got != c.ID {
		t.Errorf("RoomMaxPostID = %d, %v; want the deleted comment %d", got, err, c.ID)
	}
}

// TestMarkRoomMentionsRead checks that only the unread mentions of the user in
// the room are marked.
func TestMarkRoomMentionsRead(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	room, other := f.room(alice), f.room(alice)
	p := f.post(room, bob)
	q := f.post(other, bob)
	mention := func(u *User, k NotificationKind, r *Room, post *Post) {
		f.create(&Notification{UserID: u.ID, Kind: k, PostID: &post.ID, RoomID: &r.ID, ActorID: bob.ID})
	}
	mention(alice, NotificationMention, room, p)
	mention(alice, NotificationGroupMention, room, p)
	mention(alice, NotificationKeyword, room, p)
	mention(alice, NotificationReply, room, p)
	mention(alice, NotificationMention, other, q)
	mention(bob, NotificationMention, room, p)

	rooms, err := f.s.RoomsOfUnreadMentions(f.ctx, alice.ID, 1<<40)
	if err != nil || len(rooms) != 2 || rooms[0] != room.ID || rooms[1] != other.ID {
		t.Errorf("RoomsOfUnreadMentions = %v, %v; want both rooms", rooms, err)
	}
	n, err := f.s.MarkRoomMentionsRead(f.ctx, alice.ID, room.ID)
	if err != nil || n != 3 {
		t.Fatalf("MarkRoomMentionsRead = %d, %v; want 3", n, err)
	}
	if n, err := f.s.MarkRoomMentionsRead(f.ctx, alice.ID, room.ID); err != nil || n != 0 {
		t.Errorf("the second MarkRoomMentionsRead = %d, %v; want 0", n, err)
	}
	var unread []struct {
		UserID uint64
		RoomID uint64
		Kind   string
	}
	err = f.s.db(f.ctx).Raw("SELECT user_id, room_id, kind FROM notifications WHERE read_at IS NULL ORDER BY id").Scan(&unread).Error
	if err != nil {
		t.Fatal(err)
	}
	if len(unread) != 3 {
		t.Fatalf("unread notifications = %+v, want the reply, the mention of the other room and bob's", unread)
	}
}

// TestRoomMemberIDs checks the members without the excepted one.
func TestRoomMemberIDs(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	room := f.room(alice)
	for _, u := range []*User{alice, bob} {
		f.join(room, u)
	}
	ids, err := f.s.RoomMemberIDs(f.ctx, room.ID, alice.ID)
	if err != nil || len(ids) != 1 || ids[0] != bob.ID {
		t.Errorf("RoomMemberIDs = %v, %v; want bob", ids, err)
	}
	ids, err = f.s.RoomMemberIDs(f.ctx, room.ID, 0)
	if err != nil || len(ids) != 2 {
		t.Errorf("RoomMemberIDs(0) = %v, %v; want both", ids, err)
	}
}

// TestBackfillAndResetRoomReads checks the helpers of the large demo: the
// backfill makes the missing rows at the room's highest ID and leaves the
// others, and the reset moves every mark to the highest ID.
func TestBackfillAndResetRoomReads(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	room := f.room(alice)
	f.join(room, alice)
	f.create(&RoomMember{RoomID: room.ID, UserID: bob.ID})
	p := f.post(room, alice)

	n, err := f.s.BackfillRoomReads(f.ctx)
	if err != nil || n != 1 {
		t.Fatalf("BackfillRoomReads = %d, %v; want 1", n, err)
	}
	if got, _, _ := f.s.RoomLastSeen(f.ctx, bob.ID, room.ID); got != p.ID {
		t.Errorf("backfilled mark = %d, want %d", got, p.ID)
	}
	if got, _, _ := f.s.RoomLastSeen(f.ctx, alice.ID, room.ID); got != 0 {
		t.Errorf("the backfill moved an existing mark to %d", got)
	}
	if n, err := f.s.BackfillRoomReads(f.ctx); err != nil || n != 0 {
		t.Errorf("the second BackfillRoomReads = %d, %v; want 0", n, err)
	}
	if err := f.s.ResetRoomReads(f.ctx); err != nil {
		t.Fatalf("ResetRoomReads: %+v", err)
	}
	if got, _, _ := f.s.RoomLastSeen(f.ctx, alice.ID, room.ID); got != p.ID {
		t.Errorf("mark after the reset = %d, want %d", got, p.ID)
	}
}

// TestRoomReadQueryTags checks the route and the op of every way the queries
// of the unread counts can fail.
func TestRoomReadQueryTags(t *testing.T) {
	f := newFix(t)
	canceled, cancel := context.WithCancel(f.ctx)
	cancel()
	tests := []struct {
		name  string
		route string
		op    string
		do    func() error
	}{
		{"counts", "Store.RoomUnreadCounts.QueryFailed", "roomUnreadCounts", func() error {
			_, err := f.s.RoomUnreadCounts(canceled, 1)
			return err
		}},
		{"counts of a room", "Store.RoomUnreadOf.QueryFailed", "roomUnreadOf", func() error {
			_, _, err := f.s.RoomUnreadOf(canceled, 1, 1)
			return err
		}},
		{"last seen", "Store.RoomLastSeen.QueryFailed", "roomLastSeen", func() error {
			_, _, err := f.s.RoomLastSeen(canceled, 1, 1)
			return err
		}},
		{"highest ID", "Store.RoomMaxPostID.QueryFailed", "roomMaxPostID", func() error {
			_, err := f.s.RoomMaxPostID(canceled, 1)
			return err
		}},
		{"move of an unknown member", "Store.MoveRoomRead.QueryFailed", "moveRoomRead", func() error {
			_, err := f.s.MoveRoomRead(f.ctx, 9999, 9999, 5)
			return err
		}},
		{"backfill", "Store.BackfillRoomReads.QueryFailed", "backfillRoomReads", func() error {
			_, err := f.s.BackfillRoomReads(canceled)
			return err
		}},
		{"reset", "Store.ResetRoomReads.QueryFailed", "resetRoomReads", func() error {
			return f.s.ResetRoomReads(canceled)
		}},
		{"mark mentions", "Store.MarkRoomMentionsRead.QueryFailed", "markRoomMentionsRead", func() error {
			_, err := f.s.MarkRoomMentionsRead(canceled, 1, 1)
			return err
		}},
		{"rooms of mentions", "Store.RoomsOfUnreadMentions.QueryFailed", "roomsOfUnreadMentions", func() error {
			_, err := f.s.RoomsOfUnreadMentions(canceled, 1, 1)
			return err
		}},
		{"row of a new member", "Store.createRoomRead.QueryFailed", "createRoomRead", func() error {
			return f.s.createRoomRead(canceled, 1, 1)
		}},
		{"row of a leaving member", "Store.DeleteRoomMember.QueryFailed", "deleteRoomMember", func() error {
			return f.s.DeleteRoomMember(canceled, 1)
		}},
		{"member IDs", "Store.RoomMemberIDs.QueryFailed", "roomMemberIDs", func() error {
			_, err := f.s.RoomMemberIDs(canceled, 1, 0)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTagged(t, tt.do(), TagQueryFailed, tt.route, tt.op)
		})
	}
}

// TestMigrationRoomReads checks migration 0005: a database of step 19.2,
// which has memberships and posts but no marks, gets a row for every
// membership at the highest ID of its room, and a second run changes nothing.
// The old state is made by dropping the table and forgetting that the
// migration ran; AutoMigrate makes the table again before the migration runs.
func TestMigrationRoomReads(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	busy, quiet, empty := f.room(alice), f.room(alice), f.room(alice)
	for _, r := range []*Room{busy, quiet, empty} {
		f.create(&RoomMember{RoomID: r.ID, UserID: alice.ID})
	}
	f.create(&RoomMember{RoomID: busy.ID, UserID: bob.ID})
	f.create(&RoomMember{RoomID: quiet.ID, UserID: bob.ID})
	f.post(busy, bob)
	top := f.post(busy, alice)
	f.comment(top, bob)
	last := f.comment(top, alice)
	only := f.post(quiet, bob)
	f.post(f.room(bob), bob)

	f.exec("DROP TABLE room_reads")
	f.exec("DELETE FROM schema_migrations WHERE id = ?", "0005_room_reads")

	for run := 1; run <= 2; run++ {
		if err := f.s.migrate(f.ctx, allModels(), rawMigrations(f.s.dialect)); err != nil {
			t.Fatalf("migrate (run %d): %+v", run, err)
		}
		want := []roomReadRow{
			{alice.ID, busy.ID, last.ID}, {bob.ID, busy.ID, last.ID},
			{alice.ID, quiet.ID, only.ID}, {bob.ID, quiet.ID, only.ID},
			{alice.ID, empty.ID, 0},
		}
		got := roomReadRows(f)
		if len(got) != len(want) {
			t.Fatalf("run %d: %d rows, want %d: %+v", run, len(got), len(want), got)
		}
		byKey := map[[2]uint64]uint64{}
		for _, r := range got {
			byKey[[2]uint64{r.UserID, r.RoomID}] = r.LastSeenID
		}
		for _, w := range want {
			if v, ok := byKey[[2]uint64{w.UserID, w.RoomID}]; !ok || v != w.LastSeenID {
				t.Errorf("run %d: row of user %d in room %d = %d (found %v), want %d", run, w.UserID, w.RoomID, v, ok, w.LastSeenID)
			}
		}
		// Nothing is unread after the migration.
		counts, err := f.s.RoomUnreadCounts(f.ctx, alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range counts {
			if c.Posts != 0 {
				t.Errorf("run %d: room %d has %d unread posts after the migration", run, c.RoomID, c.Posts)
			}
		}
	}
}

// TestUnreadCapSQL checks that the cap written in the query is the constant.
func TestUnreadCapSQL(t *testing.T) {
	if got := strconv.Itoa(UnreadPostsCap); got != unreadCapSQL {
		t.Errorf("the query caps at %s, UnreadPostsCap is %s", unreadCapSQL, got)
	}
}
