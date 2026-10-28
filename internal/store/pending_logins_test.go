package store

import (
	"testing"
	"time"

	"github.com/rm4n0s/errors"
)

func TestPendingLoginQueries(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	now := f.clk.Now()

	// A new pending login deletes the ones that have expired, and only those.
	old := &PendingLogin{TokenHash: "old", UserID: alice.ID, ExpiresAt: now.Add(-time.Second)}
	live := &PendingLogin{TokenHash: "live", UserID: bob.ID, ExpiresAt: now.Add(time.Minute)}
	f.create(old)
	f.create(live)
	fresh := &PendingLogin{TokenHash: "fresh", UserID: alice.ID, ExpiresAt: now.Add(5 * time.Minute), Next: "/rooms/1"}
	if err := f.s.CreatePendingLogin(f.ctx, fresh, now); err != nil || fresh.ID == 0 {
		t.Fatalf("CreatePendingLogin: %+v, ID %d", err, fresh.ID)
	}
	if _, err := f.s.PendingLoginByTokenHash(f.ctx, "old"); err == nil {
		t.Error("the expired pending login is still there")
	}
	got, err := f.s.PendingLoginByTokenHash(f.ctx, "fresh")
	if err != nil || got.ID != fresh.ID || got.UserID != alice.ID || got.Next != "/rooms/1" || got.WrongTries != 0 {
		t.Fatalf("PendingLoginByTokenHash = %+v, %v", got, err)
	}
	if _, err := f.s.PendingLoginByTokenHash(f.ctx, "live"); err != nil {
		t.Errorf("the pending login that has not expired was deleted: %+v", err)
	}

	// A token hash is unique.
	dup := &PendingLogin{TokenHash: "fresh", UserID: bob.ID, ExpiresAt: now.Add(time.Minute)}
	err = f.s.CreatePendingLogin(f.ctx, dup, now)
	if appErr, ok := errors.FromError(err); !ok || appErr.Tag != TagConflict {
		t.Errorf("a duplicate token hash gave %v, want TagConflict", err)
	}

	// Wrong tries: the fifth deletes the row, and a row that is gone is gone.
	for i := 1; i <= 4; i++ {
		gone, err := f.s.AddPendingLoginWrongTry(f.ctx, fresh.ID, 5)
		if err != nil || gone {
			t.Fatalf("try %d: gone = %v, %v", i, gone, err)
		}
	}
	if p, _ := f.s.PendingLoginByTokenHash(f.ctx, "fresh"); p.WrongTries != 4 {
		t.Errorf("wrong tries = %d, want 4", p.WrongTries)
	}
	if gone, err := f.s.AddPendingLoginWrongTry(f.ctx, fresh.ID, 5); err != nil || !gone {
		t.Errorf("the fifth try: gone = %v, %v, want true", gone, err)
	}
	if gone, err := f.s.AddPendingLoginWrongTry(f.ctx, fresh.ID, 5); err != nil || !gone {
		t.Errorf("a try on a deleted row: gone = %v, %v, want true", gone, err)
	}

	// Delete by ID reports whether there was a row; delete by user takes all.
	if deleted, err := f.s.DeletePendingLogin(f.ctx, live.ID); err != nil || !deleted {
		t.Errorf("DeletePendingLogin = %v, %v", deleted, err)
	}
	if deleted, err := f.s.DeletePendingLogin(f.ctx, live.ID); err != nil || deleted {
		t.Errorf("DeletePendingLogin of a gone row = %v, %v", deleted, err)
	}
	f.create(&PendingLogin{TokenHash: "a1", UserID: alice.ID, ExpiresAt: now.Add(time.Minute)})
	f.create(&PendingLogin{TokenHash: "a2", UserID: alice.ID, ExpiresAt: now.Add(time.Minute)})
	f.create(&PendingLogin{TokenHash: "b1", UserID: bob.ID, ExpiresAt: now.Add(time.Minute)})
	if err := f.s.DeletePendingLoginsOfUser(f.ctx, alice.ID); err != nil {
		t.Fatalf("DeletePendingLoginsOfUser: %+v", err)
	}
	if n := f.count("pending_logins"); n != 1 {
		t.Errorf("pending logins = %d, want only bob's", n)
	}
}

func TestSSHCodeRequiredQueries(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()

	// No row yet: turning it off changes nothing and makes no row.
	if changed, err := f.s.SetSSHCodeRequired(f.ctx, alice.ID, false); err != nil || changed {
		t.Errorf("off without a row: %v, %v", changed, err)
	}
	if n := f.count("user_settings"); n != 0 {
		t.Errorf("a row was made: %d", n)
	}
	// Turning it on makes the row with the defaults of the other settings.
	if changed, err := f.s.SetSSHCodeRequired(f.ctx, alice.ID, true); err != nil || !changed {
		t.Fatalf("on without a row: %v, %v", changed, err)
	}
	row, err := f.s.UserSettingsOf(f.ctx, alice.ID)
	if err != nil || !row.SSHCodeRequired || row.Theme != ThemeSystem || !row.Sound || row.Notify != NotifyMentions {
		t.Errorf("settings = %+v, %v", row, err)
	}
	if changed, err := f.s.SetSSHCodeRequired(f.ctx, alice.ID, true); err != nil || changed {
		t.Errorf("on again: %v, %v", changed, err)
	}

	// Saving the other settings keeps the flag, and the flag keeps them.
	row.Theme = ThemeDark
	if err := f.s.SaveUserSettings(f.ctx, &row); err != nil {
		t.Fatalf("SaveUserSettings: %+v", err)
	}
	stale := DefaultUserSettings(alice.ID) // as a request that read the row before the flag was set
	stale.Sound = false
	if err := f.s.SaveUserSettings(f.ctx, &stale); err != nil {
		t.Fatalf("SaveUserSettings: %+v", err)
	}
	row, _ = f.s.UserSettingsOf(f.ctx, alice.ID)
	if !row.SSHCodeRequired || row.Sound {
		t.Errorf("after a save of the other settings: %+v", row)
	}

	// A row of other settings gets the flag by an update.
	other := DefaultUserSettings(bob.ID)
	other.Theme = ThemeLight
	if err := f.s.SaveUserSettings(f.ctx, &other); err != nil {
		t.Fatalf("SaveUserSettings: %+v", err)
	}
	if changed, err := f.s.SetSSHCodeRequired(f.ctx, bob.ID, true); err != nil || !changed {
		t.Fatalf("on with a row: %v, %v", changed, err)
	}
	row, _ = f.s.UserSettingsOf(f.ctx, bob.ID)
	if !row.SSHCodeRequired || row.Theme != ThemeLight {
		t.Errorf("settings = %+v", row)
	}
	ids, err := f.s.UserIDsWithSSHCode(f.ctx)
	if err != nil || len(ids) != 2 || !ids[alice.ID] || !ids[bob.ID] {
		t.Errorf("UserIDsWithSSHCode = %v, %v", ids, err)
	}
	if changed, err := f.s.SetSSHCodeRequired(f.ctx, alice.ID, false); err != nil || !changed {
		t.Errorf("off: %v, %v", changed, err)
	}
	if ids, _ := f.s.UserIDsWithSSHCode(f.ctx); len(ids) != 1 || !ids[bob.ID] {
		t.Errorf("UserIDsWithSSHCode after off = %v", ids)
	}
}

func TestCountUsedSSHKeysOfUser(t *testing.T) {
	f := newFix(t)
	alice := f.user()
	k1, _ := f.newKey(alice, "k1"), f.newKey(alice, "k2")
	if n, err := f.s.CountUsedSSHKeysOfUser(f.ctx, alice.ID); err != nil || n != 0 {
		t.Errorf("before = %d, %v", n, err)
	}
	if err := f.s.TouchSSHKey(f.ctx, k1.ID, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if n, err := f.s.CountUsedSSHKeysOfUser(f.ctx, alice.ID); err != nil || n != 1 {
		t.Errorf("after = %d, %v", n, err)
	}
}
