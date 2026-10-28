package store

import (
	"testing"
	"time"
)

// newKey makes a key of the user with a fingerprint derived from name.
func (f *fix) newKey(u *User, name string) *SSHKey {
	k := &SSHKey{UserID: u.ID, Name: name, Type: "ssh-ed25519", Fingerprint: "SHA256:" + name, PublicKey: "ssh-ed25519 " + name}
	if err := f.s.CreateSSHKey(f.ctx, k); err != nil {
		f.tb.Fatalf("CreateSSHKey: %+v", err)
	}
	return k
}

func TestSSHKeyQueries(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	a1 := f.newKey(alice, "a1")
	a2 := f.newKey(alice, "a2")
	b1 := f.newKey(bob, "b1")
	if a1.ID == 0 || a2.ID <= a1.ID || b1.ID <= a2.ID {
		t.Fatalf("IDs = %d, %d, %d", a1.ID, a2.ID, b1.ID)
	}

	keys, err := f.s.SSHKeysOfUser(f.ctx, alice.ID)
	if err != nil || len(keys) != 2 || keys[0].ID != a1.ID || keys[1].ID != a2.ID {
		t.Fatalf("SSHKeysOfUser = %+v, %v", keys, err)
	}
	if n, err := f.s.CountSSHKeysOfUser(f.ctx, alice.ID); err != nil || n != 2 {
		t.Errorf("CountSSHKeysOfUser = %d, %v", n, err)
	}
	got, err := f.s.SSHKeyByFingerprint(f.ctx, "SHA256:b1")
	if err != nil || got.ID != b1.ID || got.UserID != bob.ID || got.LastUsedAt != nil {
		t.Errorf("SSHKeyByFingerprint = %+v, %v", got, err)
	}
	got, err = f.s.SSHKeyByID(f.ctx, a2.ID)
	if err != nil || got.Name != "a2" {
		t.Errorf("SSHKeyByID = %+v, %v", got, err)
	}

	f.clk.Advance(time.Hour)
	if err := f.s.TouchSSHKey(f.ctx, a1.ID, f.clk.Now()); err != nil {
		t.Fatalf("TouchSSHKey: %+v", err)
	}
	got, err = f.s.SSHKeyByID(f.ctx, a1.ID)
	if err != nil || got.LastUsedAt == nil {
		t.Fatalf("after Touch: %+v, %v", got, err)
	}
	requireSameTime(t, "LastUsedAt", f.clk.Now(), *got.LastUsedAt)

	if err := f.s.DeleteSSHKey(f.ctx, a1.ID); err != nil {
		t.Fatalf("DeleteSSHKey: %+v", err)
	}
	if n, _ := f.s.CountSSHKeysOfUser(f.ctx, alice.ID); n != 1 {
		t.Errorf("keys after delete = %d", n)
	}
}

func TestSSHKeyQueryTags(t *testing.T) {
	f := newFix(t)
	u := f.user()
	k := f.newKey(u, "dup")
	tests := []struct {
		name      string
		call      func() error
		wantTag   string
		wantRoute string
		wantOp    string
	}{
		{
			name: "duplicate fingerprint",
			call: func() error {
				return f.s.CreateSSHKey(f.ctx, &SSHKey{UserID: f.user().ID, Name: "x", Type: "t", Fingerprint: k.Fingerprint, PublicKey: "p"})
			},
			wantTag: TagConflict, wantRoute: "Store.CreateSSHKey.Conflict", wantOp: "createSSHKey",
		},
		{
			name: "unknown user",
			call: func() error {
				return f.s.CreateSSHKey(f.ctx, &SSHKey{UserID: 9999, Name: "x", Type: "t", Fingerprint: "SHA256:y", PublicKey: "p"})
			},
			wantTag: TagQueryFailed, wantRoute: "Store.CreateSSHKey.QueryFailed", wantOp: "createSSHKey",
		},
		{
			name:    "list with a canceled context",
			call:    func() error { return firstErr(f.s.SSHKeysOfUser(canceled(t), u.ID)) },
			wantTag: TagQueryFailed, wantRoute: "Store.SSHKeysOfUser.QueryFailed", wantOp: "sshKeysOfUser",
		},
		{
			name:    "count with a canceled context",
			call:    func() error { return firstErr(f.s.CountSSHKeysOfUser(canceled(t), u.ID)) },
			wantTag: TagQueryFailed, wantRoute: "Store.CountSSHKeysOfUser.QueryFailed", wantOp: "countSSHKeysOfUser",
		},
		{
			name:    "unknown ID",
			call:    func() error { return firstErr(f.s.SSHKeyByID(f.ctx, 9999)) },
			wantTag: TagNotFound, wantRoute: "Store.SSHKeyByID.NotFound", wantOp: "sshKeyByID",
		},
		{
			name:    "ID with a canceled context",
			call:    func() error { return firstErr(f.s.SSHKeyByID(canceled(t), k.ID)) },
			wantTag: TagQueryFailed, wantRoute: "Store.SSHKeyByID.QueryFailed", wantOp: "sshKeyByID",
		},
		{
			name:    "unknown fingerprint",
			call:    func() error { return firstErr(f.s.SSHKeyByFingerprint(f.ctx, "SHA256:none")) },
			wantTag: TagNotFound, wantRoute: "Store.SSHKeyByFingerprint.NotFound", wantOp: "sshKeyByFingerprint",
		},
		{
			name:    "fingerprint with a canceled context",
			call:    func() error { return firstErr(f.s.SSHKeyByFingerprint(canceled(t), k.Fingerprint)) },
			wantTag: TagQueryFailed, wantRoute: "Store.SSHKeyByFingerprint.QueryFailed", wantOp: "sshKeyByFingerprint",
		},
		{
			name:    "delete an unknown key",
			call:    func() error { return f.s.DeleteSSHKey(f.ctx, 9999) },
			wantTag: TagNotFound, wantRoute: "Store.DeleteSSHKey.NotFound", wantOp: "deleteSSHKey",
		},
		{
			name:    "delete with a canceled context",
			call:    func() error { return f.s.DeleteSSHKey(canceled(t), k.ID) },
			wantTag: TagQueryFailed, wantRoute: "Store.DeleteSSHKey.QueryFailed", wantOp: "deleteSSHKey",
		},
		{
			name:    "touch with a canceled context",
			call:    func() error { return f.s.TouchSSHKey(canceled(t), k.ID, day(0)) },
			wantTag: TagQueryFailed, wantRoute: "Store.TouchSSHKey.QueryFailed", wantOp: "touchSSHKey",
		},
		{
			name: "issue a code for an unknown key",
			call: func() error {
				return f.s.IssueSSHCode(f.ctx, &SSHCode{UserID: u.ID, KeyID: 9999, CodeHash: "h", CreatedAt: day(0), ExpiresAt: day(time.Minute)}, day(-time.Hour))
			},
			wantTag: TagQueryFailed, wantRoute: "Store.IssueSSHCode.QueryFailed", wantOp: "issueSSHCode",
		},
		{
			name: "issue with a canceled context",
			call: func() error {
				return f.s.IssueSSHCode(canceled(t), &SSHCode{UserID: u.ID, KeyID: k.ID, CodeHash: "h", ExpiresAt: day(time.Minute)}, day(-time.Hour))
			},
			wantTag: TagQueryFailed, wantRoute: "Store.IssueSSHCode.QueryFailed", wantOp: "issueSSHCode",
		},
		{
			name:    "count codes with a canceled context",
			call:    func() error { return firstErr(f.s.CountSSHCodesSince(canceled(t), u.ID, day(0))) },
			wantTag: TagQueryFailed, wantRoute: "Store.CountSSHCodesSince.QueryFailed", wantOp: "countSSHCodesSince",
		},
		{
			name:    "consume an unknown code",
			call:    func() error { return firstErr(f.s.ConsumeSSHCode(f.ctx, u.ID, "nope", day(0))) },
			wantTag: TagNotFound, wantRoute: "Store.ConsumeSSHCode.NotFound", wantOp: "consumeSSHCode",
		},
		{
			name:    "consume with a canceled context",
			call:    func() error { return firstErr(f.s.ConsumeSSHCode(canceled(t), u.ID, "nope", day(0))) },
			wantTag: TagQueryFailed, wantRoute: "Store.ConsumeSSHCode.QueryFailed", wantOp: "consumeSSHCode",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTagged(t, tt.call(), tt.wantTag, tt.wantRoute, tt.wantOp)
		})
	}
}

func TestSSHCodes(t *testing.T) {
	f := newFix(t)
	alice, bob := f.user(), f.user()
	ka, kb := f.newKey(alice, "ka"), f.newKey(bob, "kb")
	now := day(0)
	issue := func(u *User, k *SSHKey, hash string, at time.Time) *SSHCode {
		t.Helper()
		c := &SSHCode{UserID: u.ID, KeyID: k.ID, CodeHash: hash, CreatedAt: at, ExpiresAt: at.Add(time.Minute)}
		if err := f.s.IssueSSHCode(f.ctx, c, at.Add(-SSHCodeRetention)); err != nil {
			t.Fatalf("IssueSSHCode: %+v", err)
		}
		return c
	}
	replaced := func(c *SSHCode) bool {
		var row SSHCode
		if err := f.s.db(f.ctx).First(&row, c.ID).Error; err != nil {
			t.Fatalf("reading the code: %v", err)
		}
		return row.Replaced
	}

	first := issue(alice, ka, "h1", now)
	bobs := issue(bob, kb, "hb", now)
	second := issue(alice, ka, "h2", now.Add(10*time.Second))
	if !replaced(first) || replaced(second) || replaced(bobs) {
		t.Errorf("replaced = %v, %v, %v; want true, false, false", replaced(first), replaced(second), replaced(bobs))
	}
	if n, err := f.s.CountSSHCodesSince(f.ctx, alice.ID, now); err != nil || n != 2 {
		t.Errorf("CountSSHCodesSince = %d, %v; replaced rows count", n, err)
	}
	if n, _ := f.s.CountSSHCodesSince(f.ctx, alice.ID, now.Add(5*time.Second)); n != 1 {
		t.Errorf("CountSSHCodesSince later = %d, want 1", n)
	}

	// A replaced code cannot be consumed; the newest can, once.
	at := now.Add(20 * time.Second)
	if err := firstErr(f.s.ConsumeSSHCode(f.ctx, alice.ID, "h1", at)); err == nil {
		t.Error("a replaced code was consumed")
	}
	if err := firstErr(f.s.ConsumeSSHCode(f.ctx, bob.ID, "h2", at)); err == nil {
		t.Error("a code of another user was consumed")
	}
	got, err := f.s.ConsumeSSHCode(f.ctx, alice.ID, "h2", at)
	if err != nil || got.ID != second.ID || got.UsedAt == nil || got.KeyID != ka.ID {
		t.Fatalf("ConsumeSSHCode = %+v, %v", got, err)
	}
	requireTagged(t, firstErr(f.s.ConsumeSSHCode(f.ctx, alice.ID, "h2", at)), TagNotFound,
		"Store.ConsumeSSHCode.NotFound", "consumeSSHCode")

	// A code that expired cannot be consumed, at the very second it expires
	// included.
	third := issue(alice, ka, "h3", now.Add(time.Hour))
	requireTagged(t, firstErr(f.s.ConsumeSSHCode(f.ctx, alice.ID, "h3", third.ExpiresAt)), TagNotFound,
		"Store.ConsumeSSHCode.NotFound", "consumeSSHCode")
	if err := firstErr(f.s.ConsumeSSHCode(f.ctx, alice.ID, "h3", third.ExpiresAt.Add(-time.Nanosecond))); err != nil {
		t.Errorf("a code one tick before its expiry: %+v", err)
	}

	// Rows older than a day are deleted when a code is issued.
	issue(alice, ka, "h4", now.Add(SSHCodeRetention+2*time.Hour))
	if n := f.count("ssh_codes"); n != 1 {
		// The four rows made within the first hour are older than a day by
		// then; only the new one is left.
		t.Errorf("rows after the prune = %d, want 1", n)
	}

	// A key takes its codes with it.
	if err := f.s.DeleteSSHKey(f.ctx, ka.ID); err != nil {
		t.Fatal(err)
	}
	if n := f.count("ssh_codes"); n != 0 {
		t.Errorf("rows after the key was deleted = %d, want 0", n)
	}
}
