package store

import (
	"testing"
	"time"
)

func TestPasswordResetQueries(t *testing.T) {
	f := newFix(t)
	admin, user, other := f.user(), f.user(), f.user()
	exp := day(24 * time.Hour)

	first := &PasswordReset{UserID: user.ID, TokenHash: "h1", CreatedBy: &admin.ID, ExpiresAt: exp}
	if err := f.s.ReplacePasswordReset(f.ctx, first); err != nil {
		t.Fatalf("ReplacePasswordReset: %+v", err)
	}
	otherLink := &PasswordReset{UserID: other.ID, TokenHash: "ho", ExpiresAt: exp}
	if err := f.s.ReplacePasswordReset(f.ctx, otherLink); err != nil {
		t.Fatalf("ReplacePasswordReset for another user: %+v", err)
	}
	if first.ID == 0 || otherLink.ID == first.ID {
		t.Fatalf("IDs = %d, %d", first.ID, otherLink.ID)
	}

	got, err := f.s.PasswordResetByTokenHash(f.ctx, "h1")
	if err != nil {
		t.Fatalf("PasswordResetByTokenHash: %+v", err)
	}
	if got.ID != first.ID || got.UserID != user.ID || got.CreatedBy == nil || *got.CreatedBy != admin.ID || got.UsedAt != nil {
		t.Errorf("link = %+v", got)
	}

	// A newer link replaces the unused one of the same user and leaves the
	// links of other users alone.
	second := &PasswordReset{UserID: user.ID, TokenHash: "h2", ExpiresAt: exp}
	if err := f.s.ReplacePasswordReset(f.ctx, second); err != nil {
		t.Fatalf("ReplacePasswordReset again: %+v", err)
	}
	requireTagged(t, firstErr(f.s.PasswordResetByTokenHash(f.ctx, "h1")), TagNotFound,
		"Store.PasswordResetByTokenHash.NotFound", "passwordResetByTokenHash")
	if got, err := f.s.PasswordResetByTokenHash(f.ctx, "h2"); err != nil || got.CreatedBy != nil {
		t.Errorf("second = %+v, %v", got, err)
	}
	if _, err := f.s.PasswordResetByTokenHash(f.ctx, "ho"); err != nil {
		t.Errorf("the link of another user is gone: %+v", err)
	}

	// A used link is kept when a newer one replaces the unused ones.
	f.clk.Advance(time.Hour)
	if err := f.s.MarkPasswordResetUsed(f.ctx, second.ID); err != nil {
		t.Fatalf("MarkPasswordResetUsed: %+v", err)
	}
	third := &PasswordReset{UserID: user.ID, TokenHash: "h3", ExpiresAt: exp}
	if err := f.s.ReplacePasswordReset(f.ctx, third); err != nil {
		t.Fatalf("ReplacePasswordReset after a use: %+v", err)
	}
	got, err = f.s.PasswordResetByTokenHash(f.ctx, "h2")
	if err != nil || got.UsedAt == nil {
		t.Fatalf("used link = %+v, %v", got, err)
	}
	requireSameTime(t, "UsedAt", f.clk.Now(), *got.UsedAt)
}

func TestPasswordResetQueryTags(t *testing.T) {
	f := newFix(t)
	user := f.user()
	link := &PasswordReset{UserID: user.ID, TokenHash: "h", ExpiresAt: day(time.Hour)}
	if err := f.s.ReplacePasswordReset(f.ctx, link); err != nil {
		t.Fatal(err)
	}
	used := &PasswordReset{UserID: f.user().ID, TokenHash: "used", ExpiresAt: day(time.Hour)}
	if err := f.s.ReplacePasswordReset(f.ctx, used); err != nil {
		t.Fatal(err)
	}
	if err := f.s.MarkPasswordResetUsed(f.ctx, used.ID); err != nil {
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
				return f.s.ReplacePasswordReset(f.ctx, &PasswordReset{UserID: f.user().ID, TokenHash: "h", ExpiresAt: day(time.Hour)})
			},
			wantTag: TagConflict, wantRoute: "Store.ReplacePasswordReset.Conflict", wantOp: "replacePasswordReset",
		},
		{
			name: "unknown user",
			call: func() error {
				return f.s.ReplacePasswordReset(f.ctx, &PasswordReset{UserID: 9999, TokenHash: "x", ExpiresAt: day(time.Hour)})
			},
			wantTag: TagQueryFailed, wantRoute: "Store.ReplacePasswordReset.QueryFailed", wantOp: "replacePasswordReset",
		},
		{
			name: "replace with a canceled context",
			call: func() error {
				return f.s.ReplacePasswordReset(canceled(t), &PasswordReset{UserID: user.ID, TokenHash: "y", ExpiresAt: day(time.Hour)})
			},
			wantTag: TagQueryFailed, wantRoute: "Store.ReplacePasswordReset.QueryFailed", wantOp: "replacePasswordReset",
		},
		{
			name: "unknown token hash",
			call: func() error {
				_, err := f.s.PasswordResetByTokenHash(f.ctx, "nope")
				return err
			},
			wantTag: TagNotFound, wantRoute: "Store.PasswordResetByTokenHash.NotFound", wantOp: "passwordResetByTokenHash",
		},
		{
			name: "lookup with a canceled context",
			call: func() error {
				_, err := f.s.PasswordResetByTokenHash(canceled(t), "h")
				return err
			},
			wantTag: TagQueryFailed, wantRoute: "Store.PasswordResetByTokenHash.QueryFailed", wantOp: "passwordResetByTokenHash",
		},
		{
			name:    "mark an unknown link",
			call:    func() error { return f.s.MarkPasswordResetUsed(f.ctx, 9999) },
			wantTag: TagNotFound, wantRoute: "Store.MarkPasswordResetUsed.NotFound", wantOp: "markPasswordResetUsed",
		},
		{
			name:    "mark a link twice",
			call:    func() error { return f.s.MarkPasswordResetUsed(f.ctx, used.ID) },
			wantTag: TagNotFound, wantRoute: "Store.MarkPasswordResetUsed.NotFound", wantOp: "markPasswordResetUsed",
		},
		{
			name:    "mark with a canceled context",
			call:    func() error { return f.s.MarkPasswordResetUsed(canceled(t), link.ID) },
			wantTag: TagQueryFailed, wantRoute: "Store.MarkPasswordResetUsed.QueryFailed", wantOp: "markPasswordResetUsed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTagged(t, tt.call(), tt.wantTag, tt.wantRoute, tt.wantOp)
		})
	}
}

// firstErr returns the error of a call that also returns a value.
func firstErr[T any](_ T, err error) error {
	return err
}
