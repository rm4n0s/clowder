package store

import (
	"reflect"
	"testing"
)

// roomNames returns the names of the rooms, in order.
func roomNames(rooms []Room) []string {
	out := []string{}
	for _, r := range rooms {
		out = append(out, r.Name)
	}
	return out
}

func TestRoomQueries(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	mk := func(name string, v Visibility, creator *User) *Room {
		t.Helper()
		r := &Room{Name: name, Visibility: v, CreatedBy: creator.ID}
		if err := f.s.CreateRoom(f.ctx, r); err != nil {
			t.Fatalf("CreateRoom %s: %+v", name, err)
		}
		if r.ID == 0 {
			t.Fatalf("CreateRoom %s: the ID was not set", name)
		}
		return r
	}
	zed := mk("zed", VisibilityVisible, alice)
	apple := mk("apple", VisibilityVisible, alice)
	hidden := mk("Hidden", VisibilityPrivate, bob)

	t.Run("RoomByID", func(t *testing.T) {
		got, err := f.s.RoomByID(f.ctx, apple.ID)
		if err != nil || got.Name != "apple" || got.Visibility != VisibilityVisible || got.CreatedBy != alice.ID {
			t.Errorf("RoomByID = %+v, %v", got, err)
		}
		requireSameTime(t, "CreatedAt", fakeStart, got.CreatedAt)
	})

	t.Run("lists are ordered by name", func(t *testing.T) {
		all, err := f.s.ListRooms(f.ctx)
		if err != nil || !reflect.DeepEqual(roomNames(all), []string{"apple", "Hidden", "zed"}) {
			t.Errorf("ListRooms = %v, %v", roomNames(all), err)
		}
		visible, err := f.s.ListVisibleRooms(f.ctx)
		if err != nil || !reflect.DeepEqual(roomNames(visible), []string{"apple", "zed"}) {
			t.Errorf("ListVisibleRooms = %v, %v", roomNames(visible), err)
		}
	})

	t.Run("members", func(t *testing.T) {
		// Display names "User 1" and "User 2" sort by name, not by join order.
		for _, m := range []*RoomMember{
			{RoomID: zed.ID, UserID: bob.ID},
			{RoomID: zed.ID, UserID: alice.ID, AddedBy: &bob.ID},
			{RoomID: hidden.ID, UserID: bob.ID},
			{RoomID: apple.ID, UserID: alice.ID},
		} {
			if err := f.s.CreateRoomMember(f.ctx, m); err != nil {
				t.Fatalf("CreateRoomMember: %+v", err)
			}
			if m.ID == 0 {
				t.Fatal("CreateRoomMember: the ID was not set")
			}
		}
		members, err := f.s.ListRoomMembers(f.ctx, zed.ID)
		if err != nil || len(members) != 2 {
			t.Fatalf("ListRoomMembers = %v, %v", members, err)
		}
		if members[0].UserID != alice.ID || members[1].UserID != bob.ID {
			t.Errorf("order = %d, %d, want alice then bob", members[0].UserID, members[1].UserID)
		}
		if members[0].User == nil || members[0].User.Username != alice.Username {
			t.Errorf("User not loaded: %+v", members[0].User)
		}
		if members[0].Adder == nil || members[0].Adder.ID != bob.ID || members[1].Adder != nil {
			t.Errorf("Adder = %+v, %+v", members[0].Adder, members[1].Adder)
		}
		if none, err := f.s.ListRoomMembers(f.ctx, 999); err != nil || len(none) != 0 {
			t.Errorf("ListRoomMembers of no room = %v, %v", none, err)
		}

		m, err := f.s.RoomMemberOf(f.ctx, zed.ID, alice.ID)
		if err != nil || m.UserID != alice.ID || m.RoomID != zed.ID {
			t.Errorf("RoomMemberOf = %+v, %v", m, err)
		}
		for _, tt := range []struct {
			name       string
			room, user uint64
			want       bool
		}{
			{name: "alice in zed", room: zed.ID, user: alice.ID, want: true},
			{name: "alice in the private room", room: hidden.ID, user: alice.ID, want: false},
			{name: "bob in the private room", room: hidden.ID, user: bob.ID, want: true},
			{name: "nobody in no room", room: 999, user: alice.ID, want: false},
		} {
			got, err := f.s.IsRoomMember(f.ctx, tt.room, tt.user)
			if err != nil || got != tt.want {
				t.Errorf("IsRoomMember %s = %v, %v, want %v", tt.name, got, err, tt.want)
			}
		}

		rooms, err := f.s.ListRoomsOfUser(f.ctx, bob.ID)
		if err != nil || !reflect.DeepEqual(roomNames(rooms), []string{"Hidden", "zed"}) {
			t.Errorf("ListRoomsOfUser(bob) = %v, %v", roomNames(rooms), err)
		}
		ids, err := f.s.RoomIDsOfUser(f.ctx, alice.ID)
		if err != nil || len(ids) != 2 {
			t.Errorf("RoomIDsOfUser(alice) = %v, %v", ids, err)
		}

		counts, err := f.s.RoomMemberCounts(f.ctx, []uint64{zed.ID, apple.ID, hidden.ID, 999})
		want := map[uint64]int{zed.ID: 2, apple.ID: 1, hidden.ID: 1}
		if err != nil || !reflect.DeepEqual(counts, want) {
			t.Errorf("RoomMemberCounts = %v, %v, want %v", counts, err, want)
		}
		if counts, err := f.s.RoomMemberCounts(f.ctx, nil); err != nil || len(counts) != 0 {
			t.Errorf("RoomMemberCounts(nil) = %v, %v", counts, err)
		}

		if err := f.s.DeleteRoomMember(f.ctx, m.ID); err != nil {
			t.Fatalf("DeleteRoomMember: %+v", err)
		}
		if got, err := f.s.IsRoomMember(f.ctx, zed.ID, alice.ID); err != nil || got {
			t.Errorf("IsRoomMember after the delete = %v, %v", got, err)
		}
	})

	t.Run("columns", func(t *testing.T) {
		cols := []BoardColumn{
			{RoomID: apple.ID, Name: "Done", Position: 2},
			{RoomID: apple.ID, Name: "To do", Position: 0},
			{RoomID: apple.ID, Name: "In progress", Position: 1},
			{RoomID: zed.ID, Name: "Other", Position: 0},
		}
		if err := f.s.CreateBoardColumns(f.ctx, cols); err != nil {
			t.Fatalf("CreateBoardColumns: %+v", err)
		}
		if cols[0].ID == 0 {
			t.Error("CreateBoardColumns: the IDs were not set")
		}
		if err := f.s.CreateBoardColumns(f.ctx, nil); err != nil {
			t.Errorf("CreateBoardColumns(nil) = %v", err)
		}
		got, err := f.s.ListBoardColumns(f.ctx, apple.ID)
		var names []string
		for _, c := range got {
			names = append(names, c.Name)
		}
		if err != nil || !reflect.DeepEqual(names, []string{"To do", "In progress", "Done"}) {
			t.Errorf("ListBoardColumns = %v, %v", names, err)
		}
	})

	t.Run("UpdateRoomText", func(t *testing.T) {
		if err := f.s.UpdateRoomText(f.ctx, zed.ID, "Zed Two", "now with text"); err != nil {
			t.Fatalf("UpdateRoomText: %+v", err)
		}
		got, err := f.s.RoomByID(f.ctx, zed.ID)
		if err != nil || got.Name != "Zed Two" || got.Description != "now with text" || got.Visibility != VisibilityVisible {
			t.Errorf("room after the update = %+v, %v", got, err)
		}
	})

	t.Run("RoomNameTaken", func(t *testing.T) {
		tests := []struct {
			name   string
			room   string
			except uint64
			want   bool
		}{
			{name: "same", room: "apple", want: true},
			{name: "other case", room: "APPLE", want: true},
			{name: "own name is not taken by itself", room: "apple", except: apple.ID, want: false},
			{name: "own name in another case", room: "Apple", except: apple.ID, want: false},
			{name: "another room's name", room: "apple", except: zed.ID, want: true},
			{name: "free", room: "pear", want: false},
			{name: "substring is not the name", room: "appl", want: false},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := f.s.RoomNameTaken(f.ctx, tt.room, tt.except)
				if err != nil || got != tt.want {
					t.Errorf("RoomNameTaken = %v, %v, want %v", got, err, tt.want)
				}
			})
		}
		// Not only a to z: the unique index folds only those.
		uni := mk("Ünï", VisibilityVisible, alice)
		if taken, err := f.s.RoomNameTaken(f.ctx, "üNÏ", 0); err != nil || !taken {
			t.Errorf("RoomNameTaken(üNÏ) = %v, %v, want true", taken, err)
		}
		if err := f.s.DeleteRoom(f.ctx, uni.ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("DeleteRoom cascades", func(t *testing.T) {
		if err := f.s.DeleteRoom(f.ctx, apple.ID); err != nil {
			t.Fatalf("DeleteRoom: %+v", err)
		}
		if cols, err := f.s.ListBoardColumns(f.ctx, apple.ID); err != nil || len(cols) != 0 {
			t.Errorf("columns after the delete = %v, %v", cols, err)
		}
		if members, err := f.s.ListRoomMembers(f.ctx, apple.ID); err != nil || len(members) != 0 {
			t.Errorf("members after the delete = %v, %v", members, err)
		}
		if _, err := f.s.RoomByID(f.ctx, apple.ID); err == nil {
			t.Error("the room is still there")
		}
	})
}

func TestRoomQueryTags(t *testing.T) {
	f := newFix(t)
	alice := f.user()
	taken := f.room(alice)
	other := f.room(alice)
	member := &RoomMember{RoomID: taken.ID, UserID: alice.ID}
	f.create(member)

	tests := []struct {
		name      string
		call      func() error
		wantTag   string
		wantRoute string
		wantOp    string
	}{
		{
			name: "duplicate room name in another case",
			call: func() error {
				return f.s.CreateRoom(f.ctx, &Room{Name: taken.Name, Visibility: VisibilityVisible, CreatedBy: alice.ID})
			},
			wantTag: TagConflict, wantRoute: "Store.CreateRoom.Conflict", wantOp: "createRoom",
		},
		{
			name: "room of an unknown creator",
			call: func() error {
				return f.s.CreateRoom(f.ctx, &Room{Name: "x", Visibility: VisibilityVisible, CreatedBy: 999})
			},
			wantTag: TagQueryFailed, wantRoute: "Store.CreateRoom.QueryFailed", wantOp: "createRoom",
		},
		{
			name: "room with a visibility that does not exist",
			call: func() error {
				return f.s.CreateRoom(f.ctx, &Room{Name: "y", Visibility: "secret", CreatedBy: alice.ID})
			},
			wantTag: TagQueryFailed, wantRoute: "Store.CreateRoom.QueryFailed", wantOp: "createRoom",
		},
		{
			name: "unknown room",
			call: func() error {
				_, err := f.s.RoomByID(f.ctx, 999)
				return err
			},
			wantTag: TagNotFound, wantRoute: "Store.RoomByID.NotFound", wantOp: "roomByID",
		},
		{
			name: "room with a canceled context",
			call: func() error {
				_, err := f.s.RoomByID(canceled(t), taken.ID)
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.RoomByID.QueryFailed", wantOp: "roomByID",
		},
		{
			name: "name check with a canceled context",
			call: func() error {
				_, err := f.s.RoomNameTaken(canceled(t), "x", 0)
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.RoomNameTaken.QueryFailed", wantOp: "roomNameTaken",
		},
		{
			name:    "rename to a taken name",
			call:    func() error { return f.s.UpdateRoomText(f.ctx, other.ID, taken.Name, "") },
			wantTag: TagConflict, wantRoute: "Store.UpdateRoomText.Conflict", wantOp: "updateRoomText",
		},
		{
			name:    "update an unknown room",
			call:    func() error { return f.s.UpdateRoomText(f.ctx, 999, "z", "") },
			wantTag: TagNotFound, wantRoute: "Store.UpdateRoomText.NotFound", wantOp: "updateRoomText",
		},
		{
			name:    "update with a canceled context",
			call:    func() error { return f.s.UpdateRoomText(canceled(t), other.ID, "z", "") },
			wantTag: TagQueryFailed, wantRoute: "Store.UpdateRoomText.QueryFailed", wantOp: "updateRoomText",
		},
		{
			name:    "delete an unknown room",
			call:    func() error { return f.s.DeleteRoom(f.ctx, 999) },
			wantTag: TagNotFound, wantRoute: "Store.DeleteRoom.NotFound", wantOp: "deleteRoom",
		},
		{
			name:    "delete with a canceled context",
			call:    func() error { return f.s.DeleteRoom(canceled(t), other.ID) },
			wantTag: TagQueryFailed, wantRoute: "Store.DeleteRoom.QueryFailed", wantOp: "deleteRoom",
		},
		{
			name: "list rooms with a canceled context",
			call: func() error {
				_, err := f.s.ListRooms(canceled(t))
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.ListRooms.QueryFailed", wantOp: "listRooms",
		},
		{
			name: "list visible rooms with a canceled context",
			call: func() error {
				_, err := f.s.ListVisibleRooms(canceled(t))
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.ListVisibleRooms.QueryFailed", wantOp: "listVisibleRooms",
		},
		{
			name: "list rooms of a user with a canceled context",
			call: func() error {
				_, err := f.s.ListRoomsOfUser(canceled(t), alice.ID)
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.ListRoomsOfUser.QueryFailed", wantOp: "listRoomsOfUser",
		},
		{
			name:    "join twice",
			call:    func() error { return f.s.CreateRoomMember(f.ctx, &RoomMember{RoomID: taken.ID, UserID: alice.ID}) },
			wantTag: TagConflict, wantRoute: "Store.CreateRoomMember.Conflict", wantOp: "createRoomMember",
		},
		{
			name:    "member of an unknown room",
			call:    func() error { return f.s.CreateRoomMember(f.ctx, &RoomMember{RoomID: 999, UserID: alice.ID}) },
			wantTag: TagQueryFailed, wantRoute: "Store.CreateRoomMember.QueryFailed", wantOp: "createRoomMember",
		},
		{
			name: "membership of a non-member",
			call: func() error {
				_, err := f.s.RoomMemberOf(f.ctx, other.ID, alice.ID)
				return err
			},
			wantTag: TagNotFound, wantRoute: "Store.RoomMemberOf.NotFound", wantOp: "roomMemberOf",
		},
		{
			name: "membership with a canceled context",
			call: func() error {
				_, err := f.s.RoomMemberOf(canceled(t), taken.ID, alice.ID)
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.RoomMemberOf.QueryFailed", wantOp: "roomMemberOf",
		},
		{
			name: "is member with a canceled context",
			call: func() error {
				_, err := f.s.IsRoomMember(canceled(t), taken.ID, alice.ID)
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.IsRoomMember.QueryFailed", wantOp: "isRoomMember",
		},
		{
			name:    "delete an unknown membership",
			call:    func() error { return f.s.DeleteRoomMember(f.ctx, 999) },
			wantTag: TagNotFound, wantRoute: "Store.DeleteRoomMember.NotFound", wantOp: "deleteRoomMember",
		},
		{
			name:    "delete a membership with a canceled context",
			call:    func() error { return f.s.DeleteRoomMember(canceled(t), member.ID) },
			wantTag: TagQueryFailed, wantRoute: "Store.DeleteRoomMember.QueryFailed", wantOp: "deleteRoomMember",
		},
		{
			name: "list members with a canceled context",
			call: func() error {
				_, err := f.s.ListRoomMembers(canceled(t), taken.ID)
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.ListRoomMembers.QueryFailed", wantOp: "listRoomMembers",
		},
		{
			name: "member counts with a canceled context",
			call: func() error {
				_, err := f.s.RoomMemberCounts(canceled(t), []uint64{taken.ID})
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.RoomMemberCounts.QueryFailed", wantOp: "roomMemberCounts",
		},
		{
			name: "rooms of a user (IDs) with a canceled context",
			call: func() error {
				_, err := f.s.RoomIDsOfUser(canceled(t), alice.ID)
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.RoomIDsOfUser.QueryFailed", wantOp: "roomIDsOfUser",
		},
		{
			name:    "columns of an unknown room",
			call:    func() error { return f.s.CreateBoardColumns(f.ctx, []BoardColumn{{RoomID: 999, Name: "x"}}) },
			wantTag: TagQueryFailed, wantRoute: "Store.CreateBoardColumns.QueryFailed", wantOp: "createBoardColumns",
		},
		{
			name: "list columns with a canceled context",
			call: func() error {
				_, err := f.s.ListBoardColumns(canceled(t), taken.ID)
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.ListBoardColumns.QueryFailed", wantOp: "listBoardColumns",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTagged(t, tt.call(), tt.wantTag, tt.wantRoute, tt.wantOp)
		})
	}
	if n := f.count("rooms"); n != 2 {
		t.Errorf("rooms has %d rows after the failures, want 2", n)
	}
}
