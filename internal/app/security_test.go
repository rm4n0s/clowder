package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/live"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/clowder/internal/store/storetest"
)

// The tests of this file are the regression tests of the security review of
// step 13.2 (docs/SECURITY.md).

// newGatedEnv is an env whose service hashes one password at a time and
// publishes to rec.
func newGatedEnv(t *testing.T, rec *live.Recorder) *env {
	t.Helper()
	clk := clock.NewFake(start)
	st := storetest.Open(t, clk)
	hasher := auth.FastHasher()
	svc := app.NewAccounts(st, clk, hasher, app.AccountsConfig{
		InvitationLifetime: lifetime, SessionLifetime: sessionLifetime,
		Publisher: rec, MaxConcurrentHashes: 1,
	})
	return &env{t: t, svc: svc, st: st, clk: clk, hasher: hasher}
}

// TestHashingWaitsForItsTurn is finding S4: a flood of logins must not hash
// all their passwords at once (each takes 64 MiB). With every place taken a
// login waits, and goes on when one is free.
func TestHashingWaitsForItsTurn(t *testing.T) {
	t.Parallel()
	e := newGatedEnv(t, &live.Recorder{})
	e.signUp("alice", store.RoleMember)
	release := e.svc.FillHashSlots(t.Context())
	done := make(chan error, 1)
	go func() {
		_, _, err := e.svc.Login(t.Context(), "alice", goodPassword, loginIP)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("Login finished while every place was taken: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Login: %+v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Login did not finish after a place was free")
	}
}

// TestHashingWaitEndsWithTheRequest: a request that is canceled while it waits
// gives up with the tag HashWaitCanceled, for the login and the sign-up.
func TestHashingWaitEndsWithTheRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		call  func(ctx context.Context, e *env, token string) error
		route string
	}{
		{"login", func(ctx context.Context, e *env, _ string) error {
			_, _, err := e.svc.Login(ctx, "alice", goodPassword, loginIP)
			return err
		}, "Accounts.Login->Accounts.verify->gate.enter.HashWaitCanceled"},
		{"sign-up", func(ctx context.Context, e *env, token string) error {
			_, err := e.svc.SignUp(ctx, token, "bobby", "Bobby", goodPassword)
			return err
		}, "Accounts.SignUp->Accounts.hash->gate.enter.HashWaitCanceled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newGatedEnv(t, &live.Recorder{})
			e.signUp("alice", store.RoleMember)
			inv, err := e.svc.CreateSystemInvitation(t.Context(), store.RoleMember)
			if err != nil {
				t.Fatal(err)
			}
			release := e.svc.FillHashSlots(t.Context())
			defer release()
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			err = tt.call(ctx, e, inv.Token)
			_ = requireTag(t, err, app.TagHashWaitCanceled, tt.route)
		})
	}
}

// TestAccountChangesPublishUserChanged is finding S3: the changes that the
// open streams of a user must notice are published after they commit, and
// only when something changed.
func TestAccountChangesPublishUserChanged(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		do   func(e *env, admin, alice app.User, token string) error
		want int
	}{
		{"disabled", func(e *env, admin, alice app.User, _ string) error {
			return e.svc.SetDisabled(e.t.Context(), actor(admin), alice.ID, true)
		}, 1},
		{"disabled again", func(e *env, admin, alice app.User, _ string) error {
			if err := e.svc.SetDisabled(e.t.Context(), actor(admin), alice.ID, true); err != nil {
				return err
			}
			return e.svc.SetDisabled(e.t.Context(), actor(admin), alice.ID, true)
		}, 1},
		{"enabled", func(e *env, admin, alice app.User, _ string) error {
			if err := e.svc.SetDisabled(e.t.Context(), actor(admin), alice.ID, true); err != nil {
				return err
			}
			return e.svc.SetDisabled(e.t.Context(), actor(admin), alice.ID, false)
		}, 1},
		{"role changed", func(e *env, admin, alice app.User, _ string) error {
			return e.svc.SetRole(e.t.Context(), actor(admin), alice.ID, store.RoleAdmin)
		}, 1},
		{"same role", func(e *env, admin, alice app.User, _ string) error {
			return e.svc.SetRole(e.t.Context(), actor(admin), alice.ID, store.RoleMember)
		}, 0},
		{"refused by a member", func(e *env, _, alice app.User, _ string) error {
			err := e.svc.SetDisabled(e.t.Context(), actor(alice), alice.ID, true)
			if err == nil {
				t.Error("a member disabled a user")
			}
			return nil
		}, 0},
		{"logout", func(e *env, _, _ app.User, token string) error {
			return e.svc.Logout(e.t.Context(), token)
		}, 1},
		{"logout of an unknown token", func(e *env, _, _ app.User, _ string) error {
			return e.svc.Logout(e.t.Context(), "no such token")
		}, 0},
		{"logout twice", func(e *env, _, _ app.User, token string) error {
			if err := e.svc.Logout(e.t.Context(), token); err != nil {
				return err
			}
			return e.svc.Logout(e.t.Context(), token)
		}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &live.Recorder{}
			e := newGatedEnv(t, rec)
			admin := e.signUp("root", store.RoleAdmin)
			alice := e.signUp("alice", store.RoleMember)
			token := e.login("alice")
			rec.Reset()
			if err := tt.do(e, admin, alice, token); err != nil {
				t.Fatalf("%+v", err)
			}
			var got []live.Event
			for _, ev := range rec.Events() {
				if ev.Kind == live.KindUserChanged {
					got = append(got, ev)
				}
			}
			if len(got) != tt.want {
				t.Fatalf("%d user.changed events, want %d: %+v", len(got), tt.want, rec.Events())
			}
			for _, ev := range got {
				if ev.Topic != live.UserTopic(alice.ID) || ev.UserID != alice.ID {
					t.Errorf("event = %+v, want it on the topic of alice", ev)
				}
			}
		})
	}
}

// TestSearchPageIsBounded is finding S7: a page number far past the data is
// answered with an empty page, without the index being asked to skip that many
// rows and without the offset overflowing.
func TestSearchPageIsBounded(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	e.write("bob", "lobby", "launch plan")
	tests := []struct {
		name     string
		page     int
		wantHits int
	}{
		{"first page", 1, 1},
		{"page 1000", 1000, 0},
		{"page 1001", 1001, 0},
		{"the largest int", int(^uint(0) >> 1), 0},
		{"below 1 is page 1", -5, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := e.find("bob", "plan", tt.page)
			if len(res.Results) != tt.wantHits || res.HasNext {
				t.Errorf("page %d: %d results, hasNext %v; want %d results and no next page", tt.page, len(res.Results), res.HasNext, tt.wantHits)
			}
			if res.Results == nil {
				t.Error("Results is nil, want an empty list")
			}
		})
	}
}
