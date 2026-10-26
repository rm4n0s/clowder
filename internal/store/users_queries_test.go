package store

import (
	"context"
	"testing"
	"time"
)

// canceled returns a context that is already canceled, which makes any query
// fail with an error that is neither "not found" nor a conflict.
func canceled(tb testing.TB) context.Context {
	tb.Helper()
	ctx, cancel := context.WithCancel(tb.Context())
	cancel()
	return ctx
}

func TestInvitationQueries(t *testing.T) {
	f := newFix(t)
	admin := f.user()
	exp := day(48 * time.Hour)

	byAdmin := &Invitation{TokenHash: "h1", Role: RoleMember, CreatedBy: &admin.ID, ExpiresAt: exp}
	if err := f.s.CreateInvitation(f.ctx, byAdmin); err != nil {
		t.Fatalf("CreateInvitation: %+v", err)
	}
	system := &Invitation{TokenHash: "h2", Role: RoleAdmin, ExpiresAt: exp}
	if err := f.s.CreateInvitation(f.ctx, system); err != nil {
		t.Fatalf("CreateInvitation without a creator: %+v", err)
	}
	if byAdmin.ID == 0 || system.ID == byAdmin.ID {
		t.Fatalf("IDs = %d, %d", byAdmin.ID, system.ID)
	}

	got, err := f.s.InvitationByTokenHash(f.ctx, "h2")
	if err != nil {
		t.Fatalf("InvitationByTokenHash: %+v", err)
	}
	if got.ID != system.ID || got.Role != RoleAdmin || got.CreatedBy != nil || got.UsedAt != nil {
		t.Errorf("invitation = %+v", got)
	}
	got, err = f.s.InvitationByTokenHash(f.ctx, "h1")
	if err != nil || got.CreatedBy == nil || *got.CreatedBy != admin.ID {
		t.Errorf("invitation = %+v, err = %v", got, err)
	}

	user := f.user()
	f.clk.Advance(time.Hour)
	if err := f.s.MarkInvitationUsed(f.ctx, byAdmin.ID, user.ID); err != nil {
		t.Fatalf("MarkInvitationUsed: %+v", err)
	}
	got, err = f.s.InvitationByTokenHash(f.ctx, "h1")
	if err != nil {
		t.Fatal(err)
	}
	if got.UsedBy == nil || *got.UsedBy != user.ID {
		t.Errorf("UsedBy = %v, want %d", got.UsedBy, user.ID)
	}
	if got.UsedAt == nil {
		t.Fatal("UsedAt is nil")
	}
	requireSameTime(t, "UsedAt", f.clk.Now(), *got.UsedAt)
}

func TestInvitationQueryTags(t *testing.T) {
	f := newFix(t)
	user := f.user()
	if err := f.s.CreateInvitation(f.ctx, &Invitation{TokenHash: "h", Role: RoleMember, ExpiresAt: day(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	used := &Invitation{TokenHash: "used", Role: RoleMember, ExpiresAt: day(time.Hour)}
	if err := f.s.CreateInvitation(f.ctx, used); err != nil {
		t.Fatal(err)
	}
	if err := f.s.MarkInvitationUsed(f.ctx, used.ID, user.ID); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		call      func() error
		wantTag   string
		wantRoute string
		wantOp    string
	}{
		{
			name: "duplicate token hash",
			call: func() error {
				return f.s.CreateInvitation(f.ctx, &Invitation{TokenHash: "h", Role: RoleMember, ExpiresAt: day(time.Hour)})
			},
			wantTag: TagConflict, wantRoute: "Store.CreateInvitation.Conflict", wantOp: "createInvitation",
		},
		{
			name: "unknown creator",
			call: func() error {
				return f.s.CreateInvitation(f.ctx, &Invitation{
					TokenHash: "x", Role: RoleMember, CreatedBy: ptr(uint64(9999)), ExpiresAt: day(time.Hour),
				})
			},
			wantTag: TagQueryFailed, wantRoute: "Store.CreateInvitation.QueryFailed", wantOp: "createInvitation",
		},
		{
			name: "create with a canceled context",
			call: func() error {
				return f.s.CreateInvitation(canceled(t), &Invitation{TokenHash: "y", Role: RoleMember, ExpiresAt: day(time.Hour)})
			},
			wantTag: TagQueryFailed, wantRoute: "Store.CreateInvitation.QueryFailed", wantOp: "createInvitation",
		},
		{
			name: "unknown token hash",
			call: func() error {
				_, err := f.s.InvitationByTokenHash(f.ctx, "nope")
				return err
			},
			wantTag: TagNotFound, wantRoute: "Store.InvitationByTokenHash.NotFound", wantOp: "invitationByTokenHash",
		},
		{
			name: "lookup with a canceled context",
			call: func() error {
				_, err := f.s.InvitationByTokenHash(canceled(t), "h")
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.InvitationByTokenHash.QueryFailed", wantOp: "invitationByTokenHash",
		},
		{
			name:    "mark an unknown invitation",
			call:    func() error { return f.s.MarkInvitationUsed(f.ctx, 9999, user.ID) },
			wantTag: TagNotFound, wantRoute: "Store.MarkInvitationUsed.NotFound", wantOp: "markInvitationUsed",
		},
		{
			name:    "mark an invitation twice",
			call:    func() error { return f.s.MarkInvitationUsed(f.ctx, used.ID, user.ID) },
			wantTag: TagNotFound, wantRoute: "Store.MarkInvitationUsed.NotFound", wantOp: "markInvitationUsed",
		},
		{
			name:    "mark for an unknown user",
			call:    func() error { return f.s.MarkInvitationUsed(f.ctx, 1, 9999) },
			wantTag: TagQueryFailed, wantRoute: "Store.MarkInvitationUsed.QueryFailed", wantOp: "markInvitationUsed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTagged(t, tt.call(), tt.wantTag, tt.wantRoute, tt.wantOp)
		})
	}
}

// requireTagged fails the test unless err has the tag, the route and the "op"
// metadata.
func requireTagged(tb testing.TB, err error, tag, route, op string) {
	tb.Helper()
	appErr := requireRoute(tb, err, route)
	if appErr.Tag != tag {
		tb.Errorf("tag = %q, want %q (route %s)", appErr.Tag, tag, appErr.Route())
	}
	if appErr.Metadata["op"] != op {
		tb.Errorf("op = %v, want %q", appErr.Metadata["op"], op)
	}
}

func TestUserQueries(t *testing.T) {
	f := newFix(t)
	zed := &User{Username: "zed", DisplayName: "Zed", PasswordHash: "h", Role: RoleMember}
	amy := &User{Username: "amy", DisplayName: "Amy", PasswordHash: "h", Role: RoleAdmin}
	for _, u := range []*User{zed, amy} {
		if err := f.s.CreateUser(f.ctx, u); err != nil {
			t.Fatalf("CreateUser: %+v", err)
		}
	}
	if zed.ID == 0 || amy.ID == 0 {
		t.Fatal("IDs were not set")
	}

	byID, err := f.s.UserByID(f.ctx, amy.ID)
	if err != nil || byID.Username != "amy" || byID.Role != RoleAdmin || byID.DisabledAt != nil {
		t.Errorf("UserByID = %+v, %v", byID, err)
	}
	byName, err := f.s.UserByUsername(f.ctx, "zed")
	if err != nil || byName.ID != zed.ID || byName.DisplayName != "Zed" {
		t.Errorf("UserByUsername = %+v, %v", byName, err)
	}

	list, err := f.s.ListUsers(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Username != "amy" || list[1].Username != "zed" {
		t.Errorf("ListUsers = %+v, want amy then zed", list)
	}
}

func TestUserQueryTags(t *testing.T) {
	f := newFix(t)
	u := f.user()
	tests := []struct {
		name      string
		call      func() error
		wantTag   string
		wantRoute string
		wantOp    string
	}{
		{
			name: "duplicate username",
			call: func() error {
				return f.s.CreateUser(f.ctx, &User{Username: u.Username, Role: RoleMember})
			},
			wantTag: TagConflict, wantRoute: "Store.CreateUser.Conflict", wantOp: "createUser",
		},
		{
			name: "invalid role",
			call: func() error {
				return f.s.CreateUser(f.ctx, &User{Username: "x", Role: "owner"})
			},
			wantTag: TagQueryFailed, wantRoute: "Store.CreateUser.QueryFailed", wantOp: "createUser",
		},
		{
			name: "unknown ID",
			call: func() error {
				_, err := f.s.UserByID(f.ctx, 9999)
				return err
			},
			wantTag: TagNotFound, wantRoute: "Store.UserByID.NotFound", wantOp: "userByID",
		},
		{
			name: "ID with a canceled context",
			call: func() error {
				_, err := f.s.UserByID(canceled(t), u.ID)
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.UserByID.QueryFailed", wantOp: "userByID",
		},
		{
			name: "unknown username",
			call: func() error {
				_, err := f.s.UserByUsername(f.ctx, "nobody")
				return err
			},
			wantTag: TagNotFound, wantRoute: "Store.UserByUsername.NotFound", wantOp: "userByUsername",
		},
		{
			name: "username with a canceled context",
			call: func() error {
				_, err := f.s.UserByUsername(canceled(t), u.Username)
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.UserByUsername.QueryFailed", wantOp: "userByUsername",
		},
		{
			name: "list with a canceled context",
			call: func() error {
				_, err := f.s.ListUsers(canceled(t))
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.ListUsers.QueryFailed", wantOp: "listUsers",
		},
		{
			name:    "role of an unknown user",
			call:    func() error { return f.s.SetUserRole(f.ctx, 9999, RoleAdmin) },
			wantTag: TagNotFound, wantRoute: "Store.SetUserRole.NotFound", wantOp: "setUserRole",
		},
		{
			name:    "invalid role value",
			call:    func() error { return f.s.SetUserRole(f.ctx, u.ID, "owner") },
			wantTag: TagQueryFailed, wantRoute: "Store.SetUserRole.QueryFailed", wantOp: "setUserRole",
		},
		{
			name:    "disable an unknown user",
			call:    func() error { return f.s.SetUserDisabled(f.ctx, 9999, true) },
			wantTag: TagNotFound, wantRoute: "Store.SetUserDisabled.NotFound", wantOp: "setUserDisabled",
		},
		{
			name:    "disable with a canceled context",
			call:    func() error { return f.s.SetUserDisabled(canceled(t), u.ID, true) },
			wantTag: TagQueryFailed, wantRoute: "Store.SetUserDisabled.QueryFailed", wantOp: "setUserDisabled",
		},
		{
			name: "count admins with a canceled context",
			call: func() error {
				_, err := f.s.CountActiveAdmins(canceled(t))
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.CountActiveAdmins.QueryFailed", wantOp: "countActiveAdmins",
		},
		{
			name: "duplicate session token hash",
			call: func() error {
				s := &Session{TokenHash: "h", UserID: u.ID, ExpiresAt: day(time.Hour), LastSeenAt: day(0)}
				if err := f.s.CreateSession(f.ctx, s); err != nil {
					return err
				}
				dup := &Session{TokenHash: "h", UserID: u.ID, ExpiresAt: day(time.Hour), LastSeenAt: day(0)}
				return f.s.CreateSession(f.ctx, dup)
			},
			wantTag: TagConflict, wantRoute: "Store.CreateSession.Conflict", wantOp: "createSession",
		},
		{
			name: "session of an unknown user",
			call: func() error {
				s := &Session{TokenHash: "z", UserID: 9999, ExpiresAt: day(time.Hour), LastSeenAt: day(0)}
				return f.s.CreateSession(f.ctx, s)
			},
			wantTag: TagQueryFailed, wantRoute: "Store.CreateSession.QueryFailed", wantOp: "createSession",
		},
		{
			name: "count sessions with a canceled context",
			call: func() error {
				_, err := f.s.CountSessionsOfUser(canceled(t), u.ID)
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.CountSessionsOfUser.QueryFailed", wantOp: "countSessionsOfUser",
		},
		{
			name:    "delete sessions with a canceled context",
			call:    func() error { return f.s.DeleteSessionsOfUser(canceled(t), u.ID) },
			wantTag: TagQueryFailed, wantRoute: "Store.DeleteSessionsOfUser.QueryFailed", wantOp: "deleteSessionsOfUser",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTagged(t, tt.call(), tt.wantTag, tt.wantRoute, tt.wantOp)
		})
	}
}

func TestSetUserRoleAndDisabled(t *testing.T) {
	f := newFix(t)
	u := f.user()
	if err := f.s.SetUserRole(f.ctx, u.ID, RoleAdmin); err != nil {
		t.Fatalf("SetUserRole: %+v", err)
	}
	// Setting the value it already has is not an error.
	if err := f.s.SetUserRole(f.ctx, u.ID, RoleAdmin); err != nil {
		t.Fatalf("SetUserRole again: %+v", err)
	}
	f.clk.Advance(time.Hour)
	if err := f.s.SetUserDisabled(f.ctx, u.ID, true); err != nil {
		t.Fatalf("SetUserDisabled: %+v", err)
	}
	got, err := f.s.UserByID(f.ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != RoleAdmin || got.DisabledAt == nil {
		t.Fatalf("user = %+v", got)
	}
	requireSameTime(t, "DisabledAt", f.clk.Now(), *got.DisabledAt)

	if err := f.s.SetUserDisabled(f.ctx, u.ID, false); err != nil {
		t.Fatalf("enable: %+v", err)
	}
	got, err = f.s.UserByID(f.ctx, u.ID)
	if err != nil || got.DisabledAt != nil {
		t.Fatalf("user = %+v, err = %v, want enabled", got, err)
	}
}

func TestCountActiveAdmins(t *testing.T) {
	f := newFix(t)
	count := func() int64 {
		n, err := f.s.CountActiveAdmins(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 0 {
		t.Fatalf("empty database: %d admins", n)
	}
	member := f.user()
	a1, a2 := f.user(), f.user()
	for _, u := range []*User{a1, a2} {
		if err := f.s.SetUserRole(f.ctx, u.ID, RoleAdmin); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(); n != 2 {
		t.Errorf("two admins: %d", n)
	}
	if err := f.s.SetUserDisabled(f.ctx, a2.ID, true); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Errorf("one admin disabled: %d", n)
	}
	if err := f.s.SetUserDisabled(f.ctx, member.ID, true); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Errorf("a disabled member changes nothing: %d", n)
	}
}

func TestSessionsOfUser(t *testing.T) {
	f := newFix(t)
	u, other := f.user(), f.user()
	add := func(hash string, owner *User) {
		t.Helper()
		s := &Session{TokenHash: hash, UserID: owner.ID, ExpiresAt: day(time.Hour), LastSeenAt: day(0)}
		if err := f.s.CreateSession(f.ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	count := func(owner *User) int64 {
		t.Helper()
		n, err := f.s.CountSessionsOfUser(f.ctx, owner.ID)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	add("a", u)
	add("b", u)
	add("c", other)
	if count(u) != 2 || count(other) != 1 {
		t.Fatalf("counts = %d, %d", count(u), count(other))
	}
	if err := f.s.DeleteSessionsOfUser(f.ctx, u.ID); err != nil {
		t.Fatalf("DeleteSessionsOfUser: %+v", err)
	}
	if count(u) != 0 || count(other) != 1 {
		t.Errorf("after delete: %d, %d, want 0, 1", count(u), count(other))
	}
	if err := f.s.DeleteSessionsOfUser(f.ctx, u.ID); err != nil {
		t.Errorf("deleting none: %+v", err)
	}
}
