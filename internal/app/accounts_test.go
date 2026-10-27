package app_test

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/clowder/internal/store/storetest"
	"github.com/rm4n0s/errors"
)

const (
	lifetime        = 7 * 24 * time.Hour
	sessionLifetime = 30 * 24 * time.Hour
	goodPassword    = "correct horse battery"
)

var start = time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)

// env is a service on a fresh in-memory database with the fake clock.
type env struct {
	t      *testing.T
	svc    *app.Accounts
	st     *store.Store
	clk    *clock.Fake
	hasher auth.Hasher
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clk := clock.NewFake(start)
	st := storetest.Open(t, clk)
	hasher := auth.FastHasher()
	return &env{t: t, svc: app.NewAccounts(st, clk, hasher, app.AccountsConfig{InvitationLifetime: lifetime, SessionLifetime: sessionLifetime}), st: st, clk: clk, hasher: hasher}
}

// signUp makes a user with the role through a system invitation.
func (e *env) signUp(username string, role store.Role) app.User {
	e.t.Helper()
	inv, err := e.svc.CreateSystemInvitation(e.t.Context(), role)
	if err != nil {
		e.t.Fatalf("CreateSystemInvitation: %+v", err)
	}
	u, err := e.svc.SignUp(e.t.Context(), inv.Token, username, strings.ToUpper(username[:1])+username[1:], goodPassword)
	if err != nil {
		e.t.Fatalf("SignUp %s: %+v", username, err)
	}
	return u
}

// actor makes the Actor of a user.
func actor(u app.User) app.Actor {
	return app.Actor{UserID: u.ID, Username: u.Username, Role: u.Role}
}

// requireTag fails the test unless err is a tagged error with the tag and a
// route that contains route. It returns the error.
func requireTag(t *testing.T, err error, tag, route string) *errors.Error {
	t.Helper()
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T: %v", err, err)
	}
	if appErr.Tag != tag {
		t.Fatalf("tag = %q, want %q (route %s)", appErr.Tag, tag, appErr.Route())
	}
	if !appErr.HasRoute(route) {
		t.Fatalf("route = %s, want it to contain %s", appErr.Route(), route)
	}
	return appErr
}

// fieldsOf returns the metadata "fields" of a TagInvalidInput error.
func fieldsOf(t *testing.T, appErr *errors.Error) map[string]string {
	t.Helper()
	fields, ok := appErr.Metadata["fields"].(map[string]string)
	if !ok {
		t.Fatalf("metadata fields = %#v, want map[string]string", appErr.Metadata["fields"])
	}
	return fields
}

func TestExportedTagsMatchTheStore(t *testing.T) {
	t.Parallel()
	if app.TagNotFound != store.TagNotFound || app.TagConflict != store.TagConflict {
		t.Errorf("app tags %q, %q differ from the store's %q, %q",
			app.TagNotFound, app.TagConflict, store.TagNotFound, store.TagConflict)
	}
}

func TestActorIsAdmin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		role store.Role
		want bool
	}{
		{store.RoleAdmin, true},
		{store.RoleMember, false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			if got := (app.Actor{Role: tt.role}).IsAdmin(); got != tt.want {
				t.Errorf("IsAdmin = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCreateInvitation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.signUp("root", store.RoleAdmin)

	tests := []struct {
		name string
		make func() (app.IssuedInvitation, error)
		role store.Role
		by   *uint64
	}{
		{
			name: "admin makes a member invitation",
			make: func() (app.IssuedInvitation, error) {
				return e.svc.CreateInvitation(t.Context(), actor(admin), store.RoleMember)
			},
			role: store.RoleMember, by: &admin.ID,
		},
		{
			name: "admin makes an admin invitation",
			make: func() (app.IssuedInvitation, error) {
				return e.svc.CreateInvitation(t.Context(), actor(admin), store.RoleAdmin)
			},
			role: store.RoleAdmin, by: &admin.ID,
		},
		{
			name: "the command line makes a member invitation",
			make: func() (app.IssuedInvitation, error) {
				return e.svc.CreateSystemInvitation(t.Context(), store.RoleMember)
			},
			role: store.RoleMember, by: nil,
		},
		{
			name: "the command line makes an admin invitation",
			make: func() (app.IssuedInvitation, error) {
				return e.svc.CreateSystemInvitation(t.Context(), store.RoleAdmin)
			},
			role: store.RoleAdmin, by: nil,
		},
	}
	seen := map[string]bool{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e.clk.Advance(time.Minute)
			got, err := tt.make()
			if err != nil {
				t.Fatalf("make: %+v", err)
			}
			if got.Token == "" || seen[got.Token] {
				t.Errorf("token %q is empty or repeated", got.Token)
			}
			seen[got.Token] = true
			if got.Role != tt.role {
				t.Errorf("Role = %q, want %q", got.Role, tt.role)
			}
			if want := e.clk.Now().Add(lifetime); !got.ExpiresAt.Equal(want) {
				t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, want)
			}
			if err := e.svc.InvitationValid(t.Context(), got.Token); err != nil {
				t.Errorf("InvitationValid: %+v", err)
			}
			// The database keeps the hash of the token, the role and the maker.
			row, err := e.st.InvitationByTokenHash(t.Context(), auth.HashToken(got.Token))
			if err != nil {
				t.Fatal(err)
			}
			if row.TokenHash == got.Token || row.Role != tt.role {
				t.Errorf("row = %+v", row)
			}
			if !reflect.DeepEqual(row.CreatedBy, tt.by) {
				t.Errorf("CreatedBy = %v, want %v", row.CreatedBy, tt.by)
			}
		})
	}
}

func TestCreateInvitationInvalidRole(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.signUp("root", store.RoleAdmin)
	tests := []struct {
		name  string
		call  func() error
		route string
	}{
		{
			name: "by an admin",
			call: func() error {
				_, err := e.svc.CreateInvitation(t.Context(), actor(admin), "owner")
				return err
			},
			route: "Accounts.CreateInvitation.InvalidInput",
		},
		{
			name: "from the command line",
			call: func() error {
				_, err := e.svc.CreateSystemInvitation(t.Context(), "")
				return err
			},
			route: "Accounts.CreateSystemInvitation.InvalidInput",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			appErr := requireTag(t, tt.call(), app.TagInvalidInput, tt.route)
			if _, ok := fieldsOf(t, appErr)["role"]; !ok {
				t.Errorf("fields = %v, want a role", appErr.Metadata["fields"])
			}
		})
	}
}

func TestInvitationValid(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	used, err := e.svc.CreateSystemInvitation(t.Context(), store.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SignUp(t.Context(), used.Token, "first", "First", goodPassword); err != nil {
		t.Fatal(err)
	}
	fresh, err := e.svc.CreateSystemInvitation(t.Context(), store.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(lifetime - time.Nanosecond)
	almost, err := e.svc.CreateSystemInvitation(t.Context(), store.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(time.Nanosecond) // fresh expires now; almost has a week left

	tests := []struct {
		name       string
		token      string
		wantReason string // "" for a valid one
		wantRoute  string
	}{
		{name: "unknown", token: "no-such-token", wantReason: "unknown",
			wantRoute: "Accounts.InvitationValid->Accounts.usableInvitation->Store.InvitationByTokenHash.NotFound"},
		{name: "empty", token: "", wantReason: "unknown",
			wantRoute: "Accounts.InvitationValid->Accounts.usableInvitation->Store.InvitationByTokenHash.NotFound"},
		{name: "used", token: used.Token, wantReason: "used",
			wantRoute: "Accounts.InvitationValid->Accounts.usableInvitation.NotFound"},
		{name: "expired at the very instant", token: fresh.Token, wantReason: "expired",
			wantRoute: "Accounts.InvitationValid->Accounts.usableInvitation.NotFound"},
		{name: "still valid", token: almost.Token},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := e.svc.InvitationValid(t.Context(), tt.token)
			if tt.wantRoute == "" {
				if err != nil {
					t.Fatalf("InvitationValid: %+v", err)
				}
				return
			}
			appErr := requireTag(t, err, app.TagNotFound, tt.wantRoute)
			if tt.wantReason != "unknown" && appErr.Metadata["reason"] != tt.wantReason {
				t.Errorf("reason = %v, want %q", appErr.Metadata["reason"], tt.wantReason)
			}
			// The caller cannot tell the cases apart.
			if appErr.Message != "the invitation is not valid" && tt.wantReason != "unknown" {
				t.Errorf("message = %q", appErr.Message)
			}
		})
	}

	// An expired invitation stays expired when the clock only moves forward,
	// and a used one never becomes valid.
	e.clk.Advance(lifetime)
	if err := e.svc.InvitationValid(t.Context(), almost.Token); err == nil {
		t.Error("an invitation older than its lifetime is still valid")
	}
}

func TestSignUpValidation(t *testing.T) {
	t.Parallel()
	long := func(n int, s string) string { return strings.Repeat(s, n) }
	tests := []struct {
		name        string
		username    string
		displayName string
		password    string
		// wantFields lists the fields that must be wrong; nil means valid.
		wantFields []string
		// wantUsername and wantDisplay are the stored values when valid.
		wantUsername string
		wantDisplay  string
	}{
		{name: "plain", username: "alice", displayName: "Alice", password: goodPassword,
			wantUsername: "alice", wantDisplay: "Alice"},
		{name: "upper case and spaces are normalized", username: "  AlIcE \t", displayName: "  Alice A  ", password: goodPassword,
			wantUsername: "alice", wantDisplay: "Alice A"},
		{name: "every allowed character", username: "a_b-9", displayName: "x", password: goodPassword,
			wantUsername: "a_b-9", wantDisplay: "x"},
		{name: "username of 3", username: "abc", displayName: "x", password: goodPassword,
			wantUsername: "abc", wantDisplay: "x"},
		{name: "username of 32", username: long(32, "a"), displayName: "x", password: goodPassword,
			wantUsername: long(32, "a"), wantDisplay: "x"},
		{name: "username of 2", username: "ab", displayName: "x", password: goodPassword, wantFields: []string{"username"}},
		{name: "username of 33", username: long(33, "a"), displayName: "x", password: goodPassword, wantFields: []string{"username"}},
		{name: "username empty", username: "", displayName: "x", password: goodPassword, wantFields: []string{"username"}},
		{name: "username only spaces", username: "     ", displayName: "x", password: goodPassword, wantFields: []string{"username"}},
		{name: "username with a space inside", username: "al ice", displayName: "x", password: goodPassword, wantFields: []string{"username"}},
		{name: "username with a dot", username: "al.ice", displayName: "x", password: goodPassword, wantFields: []string{"username"}},
		{name: "username with an at sign", username: "alice@x", displayName: "x", password: goodPassword, wantFields: []string{"username"}},
		{name: "username with an accent", username: "ålice", displayName: "x", password: goodPassword, wantFields: []string{"username"}},
		{name: "username in Greek", username: "άλικη", displayName: "x", password: goodPassword, wantFields: []string{"username"}},
		{name: "username with an emoji", username: "alice😀", displayName: "x", password: goodPassword, wantFields: []string{"username"}},
		{name: "username with a newline inside", username: "ali\nce", displayName: "x", password: goodPassword, wantFields: []string{"username"}},
		{name: "two accented letters are too short", username: "éé", displayName: "x", password: goodPassword, wantFields: []string{"username"}},
		{name: "display name empty", username: "alice", displayName: "", password: goodPassword, wantFields: []string{"displayName"}},
		{name: "display name only spaces", username: "alice", displayName: " \t\n ", password: goodPassword, wantFields: []string{"displayName"}},
		{name: "display name of 64 characters", username: "alice", displayName: long(64, "é"), password: goodPassword,
			wantUsername: "alice", wantDisplay: long(64, "é")},
		{name: "display name of 64 emoji (256 bytes)", username: "alice", displayName: long(64, "😀"), password: goodPassword,
			wantUsername: "alice", wantDisplay: long(64, "😀")},
		{name: "display name of 65 characters", username: "alice", displayName: long(65, "é"), password: goodPassword, wantFields: []string{"displayName"}},
		{name: "display name of 65 emoji", username: "alice", displayName: long(65, "😀"), password: goodPassword, wantFields: []string{"displayName"}},
		{name: "display name in Greek", username: "alice", displayName: "Αλίκη Παπά", password: goodPassword,
			wantUsername: "alice", wantDisplay: "Αλίκη Παπά"},
		{name: "password of 12", username: "alice", displayName: "x", password: long(12, "p"),
			wantUsername: "alice", wantDisplay: "x"},
		{name: "password of 11", username: "alice", displayName: "x", password: long(11, "p"), wantFields: []string{"password"}},
		{name: "password empty", username: "alice", displayName: "x", password: "", wantFields: []string{"password"}},
		{name: "password of 12 characters in 24 bytes", username: "alice", displayName: "x", password: long(12, "é"),
			wantUsername: "alice", wantDisplay: "x"},
		{name: "password of 11 emoji (44 bytes)", username: "alice", displayName: "x", password: long(11, "😀"), wantFields: []string{"password"}},
		{name: "password of 12 spaces is kept as it is", username: "alice", displayName: "x", password: long(12, " "),
			wantUsername: "alice", wantDisplay: "x"},
		{name: "password of 1024 bytes", username: "alice", displayName: "x", password: long(1024, "p"),
			wantUsername: "alice", wantDisplay: "x"},
		{name: "password of 1025 bytes", username: "alice", displayName: "x", password: long(1025, "p"), wantFields: []string{"password"}},
		{name: "password of 512 emoji is 2048 bytes", username: "alice", displayName: "x", password: long(512, "😀"), wantFields: []string{"password"}},
		{name: "every field wrong", username: "A!", displayName: "", password: "short", wantFields: []string{"displayName", "password", "username"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			inv, err := e.svc.CreateSystemInvitation(t.Context(), store.RoleMember)
			if err != nil {
				t.Fatal(err)
			}
			u, err := e.svc.SignUp(t.Context(), inv.Token, tt.username, tt.displayName, tt.password)
			if tt.wantFields == nil {
				if err != nil {
					t.Fatalf("SignUp: %+v", err)
				}
				if u.Username != tt.wantUsername || u.DisplayName != tt.wantDisplay {
					t.Errorf("user = %q, %q; want %q, %q", u.Username, u.DisplayName, tt.wantUsername, tt.wantDisplay)
				}
				return
			}
			appErr := requireTag(t, err, app.TagInvalidInput, "Accounts.SignUp.InvalidInput")
			fields := fieldsOf(t, appErr)
			var got []string
			for name, msg := range fields {
				if msg == "" {
					t.Errorf("field %s has no message", name)
				}
				got = append(got, name)
			}
			if !sameSet(got, tt.wantFields) {
				t.Errorf("wrong fields = %v, want %v", got, tt.wantFields)
			}
			// A mistake in the form does not use up the invitation.
			if err := e.svc.InvitationValid(t.Context(), inv.Token); err != nil {
				t.Errorf("the invitation was used up: %+v", err)
			}
			users, err := e.st.ListUsers(t.Context())
			if err != nil || len(users) != 0 {
				t.Errorf("users = %v, err = %v, want none", users, err)
			}
		})
	}
}

// sameSet reports whether a and b hold the same strings, in any order.
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func TestSignUp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		role store.Role
	}{
		{"member invitation makes a member", store.RoleMember},
		{"admin invitation makes an admin", store.RoleAdmin},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			inv, err := e.svc.CreateSystemInvitation(t.Context(), tt.role)
			if err != nil {
				t.Fatal(err)
			}
			e.clk.Advance(time.Hour)
			u, err := e.svc.SignUp(t.Context(), inv.Token, "Alice", " Alice A ", goodPassword)
			if err != nil {
				t.Fatalf("SignUp: %+v", err)
			}
			if u.ID == 0 || u.Username != "alice" || u.DisplayName != "Alice A" || u.Role != tt.role || u.Disabled {
				t.Errorf("user = %+v", u)
			}
			if !u.CreatedAt.Equal(e.clk.Now()) {
				t.Errorf("CreatedAt = %v, want %v", u.CreatedAt, e.clk.Now())
			}

			// The stored password is a hash that verifies.
			row, err := e.st.UserByUsername(t.Context(), "alice")
			if err != nil {
				t.Fatal(err)
			}
			if row.PasswordHash == goodPassword || !strings.HasPrefix(row.PasswordHash, "$argon2id$") {
				t.Errorf("PasswordHash = %q", row.PasswordHash)
			}
			if ok, err := e.hasher.Verify(goodPassword, row.PasswordHash); err != nil || !ok {
				t.Errorf("the password does not verify: %v, %v", ok, err)
			}

			// The invitation is used once, by this user, now.
			irow, err := e.st.InvitationByTokenHash(t.Context(), auth.HashToken(inv.Token))
			if err != nil {
				t.Fatal(err)
			}
			if irow.UsedBy == nil || *irow.UsedBy != u.ID || irow.UsedAt == nil || !irow.UsedAt.Equal(e.clk.Now()) {
				t.Errorf("invitation = %+v", irow)
			}
			_, err = e.svc.SignUp(t.Context(), inv.Token, "bob", "Bob", goodPassword)
			_ = requireTag(t, err, app.TagNotFound, "Accounts.SignUp->Accounts.usableInvitation.NotFound")
			if err := e.svc.InvitationValid(t.Context(), inv.Token); err == nil {
				t.Error("the invitation is still valid after its use")
			}
			users, err := e.st.ListUsers(t.Context())
			if err != nil || len(users) != 1 {
				t.Errorf("users = %v, err = %v, want one", users, err)
			}
		})
	}
}

func TestSignUpInvitationFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		prepare    func(e *env) string // returns the token to use
		wantRoute  string
		wantReason string
	}{
		{
			name:      "unknown token",
			prepare:   func(*env) string { return "nope" },
			wantRoute: "Accounts.SignUp->Accounts.usableInvitation->Store.InvitationByTokenHash.NotFound",
		},
		{
			name: "expired",
			prepare: func(e *env) string {
				inv, err := e.svc.CreateSystemInvitation(e.t.Context(), store.RoleMember)
				if err != nil {
					e.t.Fatal(err)
				}
				e.clk.Advance(lifetime)
				return inv.Token
			},
			wantRoute:  "Accounts.SignUp->Accounts.usableInvitation.NotFound",
			wantReason: "expired",
		},
		{
			name: "used",
			prepare: func(e *env) string {
				inv, err := e.svc.CreateSystemInvitation(e.t.Context(), store.RoleMember)
				if err != nil {
					e.t.Fatal(err)
				}
				if _, err := e.svc.SignUp(e.t.Context(), inv.Token, "first", "First", goodPassword); err != nil {
					e.t.Fatal(err)
				}
				return inv.Token
			},
			wantRoute:  "Accounts.SignUp->Accounts.usableInvitation.NotFound",
			wantReason: "used",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			token := tt.prepare(e)
			// Even a form full of mistakes gets "not found" first.
			_, err := e.svc.SignUp(t.Context(), token, "", "", "")
			appErr := requireTag(t, err, app.TagNotFound, tt.wantRoute)
			if tt.wantReason != "" && appErr.Metadata["reason"] != tt.wantReason {
				t.Errorf("reason = %v, want %q", appErr.Metadata["reason"], tt.wantReason)
			}
		})
	}
}

func TestSignUpTakenUsername(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.signUp("alice", store.RoleMember)
	for _, name := range []string{"alice", "ALICE", " Alice "} {
		t.Run(name, func(t *testing.T) {
			inv, err := e.svc.CreateSystemInvitation(t.Context(), store.RoleMember)
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.svc.SignUp(t.Context(), inv.Token, name, "Other", goodPassword)
			appErr := requireTag(t, err, app.TagInvalidInput, "Accounts.SignUp->Store.Tx->DB.Transaction->Store.Tx.func1->Accounts.SignUp.func1.InvalidInput")
			if fields := fieldsOf(t, appErr); len(fields) != 1 || fields["username"] == "" {
				t.Errorf("fields = %v, want only the username", fields)
			}
			// The transaction rolled back: the invitation can still be used.
			if err := e.svc.InvitationValid(t.Context(), inv.Token); err != nil {
				t.Errorf("the invitation was used up: %+v", err)
			}
		})
	}
	users, err := e.st.ListUsers(t.Context())
	if err != nil || len(users) != 1 {
		t.Errorf("users = %v, err = %v, want one", users, err)
	}
}

// TestSignUpRaceUsesTheUniqueIndex runs two sign-ups with the same username at
// once. Whatever the interleaving, one succeeds and one fails with a tagged
// error, and there is one user: the check in the service and the unique index
// agree.
func TestSignUpRaceUsesTheUniqueIndex(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tokens := make([]string, 2)
	for i := range tokens {
		inv, err := e.svc.CreateSystemInvitation(t.Context(), store.RoleMember)
		if err != nil {
			t.Fatal(err)
		}
		tokens[i] = inv.Token
	}
	errs := make(chan error, len(tokens))
	for _, token := range tokens {
		go func() {
			_, err := e.svc.SignUp(t.Context(), token, "alice", "Alice", goodPassword)
			errs <- err
		}()
	}
	var failures []error
	for range tokens {
		if err := <-errs; err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) != 1 {
		t.Fatalf("%d sign-ups failed, want 1: %v", len(failures), failures)
	}
	appErr, ok := errors.FromError(failures[0])
	if !ok || (appErr.Tag != app.TagInvalidInput && appErr.Tag != app.TagConflict) {
		t.Errorf("failure = %v, want InvalidInput or Conflict", failures[0])
	}
	users, err := e.st.ListUsers(t.Context())
	if err != nil || len(users) != 1 {
		t.Errorf("users = %v, err = %v, want one", users, err)
	}
}

func TestUsers(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	zed := e.signUp("zed", store.RoleMember)
	root := e.signUp("root", store.RoleAdmin)
	amy := e.signUp("amy", store.RoleMember)
	if err := e.svc.SetDisabled(t.Context(), actor(root), amy.ID, true); err != nil {
		t.Fatal(err)
	}

	got, err := e.svc.Users(t.Context(), actor(root))
	if err != nil {
		t.Fatalf("Users: %+v", err)
	}
	if len(got) != 3 {
		t.Fatalf("%d users, want 3: %+v", len(got), got)
	}
	for i, want := range []struct {
		username string
		role     store.Role
		disabled bool
	}{{"amy", store.RoleMember, true}, {"root", store.RoleAdmin, false}, {"zed", store.RoleMember, false}} {
		u := got[i]
		if u.Username != want.username || u.Role != want.role || u.Disabled != want.disabled {
			t.Errorf("user %d = %+v, want %+v", i, u, want)
		}
	}
	if got[2].ID != zed.ID || got[2].DisplayName != "Zed" {
		t.Errorf("zed = %+v", got[2])
	}
	// The view has no password hash.
	if _, ok := reflect.TypeFor[app.User]().FieldByName("PasswordHash"); ok {
		t.Error("app.User has a PasswordHash")
	}
}

func TestSetRole(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(e *env) (act app.Actor, target uint64, role store.Role)
		// wantRole is the role of the target afterwards.
		wantRole  store.Role
		wantTag   string // "" for success
		wantRoute string
	}{
		{
			name: "promote a member",
			setup: func(e *env) (app.Actor, uint64, store.Role) {
				root, m := e.signUp("root", store.RoleAdmin), e.signUp("mem", store.RoleMember)
				return actor(root), m.ID, store.RoleAdmin
			},
			wantRole: store.RoleAdmin,
		},
		{
			name: "demote an admin when another admin is active",
			setup: func(e *env) (app.Actor, uint64, store.Role) {
				root, other := e.signUp("root", store.RoleAdmin), e.signUp("other", store.RoleAdmin)
				return actor(root), other.ID, store.RoleMember
			},
			wantRole: store.RoleMember,
		},
		{
			name: "an admin demotes themselves when another admin is active",
			setup: func(e *env) (app.Actor, uint64, store.Role) {
				root := e.signUp("root", store.RoleAdmin)
				e.signUp("other", store.RoleAdmin)
				return actor(root), root.ID, store.RoleMember
			},
			wantRole: store.RoleMember,
		},
		{
			name: "the same role is not an error, even for the last admin",
			setup: func(e *env) (app.Actor, uint64, store.Role) {
				root := e.signUp("root", store.RoleAdmin)
				return actor(root), root.ID, store.RoleAdmin
			},
			wantRole: store.RoleAdmin,
		},
		{
			name: "the last admin keeps the role",
			setup: func(e *env) (app.Actor, uint64, store.Role) {
				root := e.signUp("root", store.RoleAdmin)
				e.signUp("mem", store.RoleMember)
				return actor(root), root.ID, store.RoleMember
			},
			wantRole: store.RoleAdmin, wantTag: app.TagConflict,
			wantRoute: "Accounts.SetRole->Store.Tx->DB.Transaction->Store.Tx.func1->Accounts.SetRole.func1.Conflict",
		},
		{
			name: "a disabled admin is not counted: the only active admin keeps the role",
			setup: func(e *env) (app.Actor, uint64, store.Role) {
				root, other := e.signUp("root", store.RoleAdmin), e.signUp("other", store.RoleAdmin)
				if err := e.svc.SetDisabled(e.t.Context(), actor(root), other.ID, true); err != nil {
					e.t.Fatal(err)
				}
				return actor(root), root.ID, store.RoleMember
			},
			wantRole: store.RoleAdmin, wantTag: app.TagConflict,
			wantRoute: "Accounts.SetRole->Store.Tx->DB.Transaction->Store.Tx.func1->Accounts.SetRole.func1.Conflict",
		},
		{
			name: "a disabled admin can lose the role: it changes no count",
			setup: func(e *env) (app.Actor, uint64, store.Role) {
				root, other := e.signUp("root", store.RoleAdmin), e.signUp("other", store.RoleAdmin)
				if err := e.svc.SetDisabled(e.t.Context(), actor(root), other.ID, true); err != nil {
					e.t.Fatal(err)
				}
				return actor(root), other.ID, store.RoleMember
			},
			wantRole: store.RoleMember,
		},
		{
			name: "unknown user",
			setup: func(e *env) (app.Actor, uint64, store.Role) {
				return actor(e.signUp("root", store.RoleAdmin)), 9999, store.RoleAdmin
			},
			wantTag:   app.TagNotFound,
			wantRoute: "Accounts.SetRole->Store.Tx->DB.Transaction->Store.Tx.func1->Accounts.SetRole.func1->Store.UserByID.NotFound",
		},
		{
			name: "role that does not exist",
			setup: func(e *env) (app.Actor, uint64, store.Role) {
				root := e.signUp("root", store.RoleAdmin)
				return actor(root), root.ID, "owner"
			},
			wantRole: store.RoleAdmin, wantTag: app.TagInvalidInput,
			wantRoute: "Accounts.SetRole.InvalidInput",
		},
		{
			name: "a member may not",
			setup: func(e *env) (app.Actor, uint64, store.Role) {
				e.signUp("root", store.RoleAdmin)
				m := e.signUp("mem", store.RoleMember)
				return actor(m), m.ID, store.RoleAdmin
			},
			wantRole: store.RoleMember, wantTag: app.TagForbidden,
			wantRoute: "Accounts.SetRole.Forbidden",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			act, target, role := tt.setup(e)
			err := e.svc.SetRole(t.Context(), act, target, role)
			if tt.wantTag == "" {
				if err != nil {
					t.Fatalf("SetRole: %+v", err)
				}
			} else {
				_ = requireTag(t, err, tt.wantTag, tt.wantRoute)
			}
			if tt.wantRole == "" {
				return
			}
			row, err := e.st.UserByID(t.Context(), target)
			if err != nil {
				t.Fatal(err)
			}
			if row.Role != tt.wantRole {
				t.Errorf("role = %q, want %q", row.Role, tt.wantRole)
			}
		})
	}
}

func TestSetDisabled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(e *env) (act app.Actor, target uint64, disabled bool)
		// wantDisabled is the state of the target afterwards.
		wantDisabled bool
		wantTag      string // "" for success
		wantRoute    string
	}{
		{
			name: "disable a member",
			setup: func(e *env) (app.Actor, uint64, bool) {
				root, m := e.signUp("root", store.RoleAdmin), e.signUp("mem", store.RoleMember)
				return actor(root), m.ID, true
			},
			wantDisabled: true,
		},
		{
			name: "disable an admin when another admin is active",
			setup: func(e *env) (app.Actor, uint64, bool) {
				root, other := e.signUp("root", store.RoleAdmin), e.signUp("other", store.RoleAdmin)
				return actor(root), other.ID, true
			},
			wantDisabled: true,
		},
		{
			name: "enable a disabled user",
			setup: func(e *env) (app.Actor, uint64, bool) {
				root, m := e.signUp("root", store.RoleAdmin), e.signUp("mem", store.RoleMember)
				if err := e.svc.SetDisabled(e.t.Context(), actor(root), m.ID, true); err != nil {
					e.t.Fatal(err)
				}
				return actor(root), m.ID, false
			},
			wantDisabled: false,
		},
		{
			name: "disable twice",
			setup: func(e *env) (app.Actor, uint64, bool) {
				root, m := e.signUp("root", store.RoleAdmin), e.signUp("mem", store.RoleMember)
				if err := e.svc.SetDisabled(e.t.Context(), actor(root), m.ID, true); err != nil {
					e.t.Fatal(err)
				}
				return actor(root), m.ID, true
			},
			wantDisabled: true,
		},
		{
			name: "enable an active user",
			setup: func(e *env) (app.Actor, uint64, bool) {
				root, m := e.signUp("root", store.RoleAdmin), e.signUp("mem", store.RoleMember)
				return actor(root), m.ID, false
			},
			wantDisabled: false,
		},
		{
			name: "an admin cannot disable themselves",
			setup: func(e *env) (app.Actor, uint64, bool) {
				root := e.signUp("root", store.RoleAdmin)
				e.signUp("other", store.RoleAdmin)
				return actor(root), root.ID, true
			},
			wantDisabled: false, wantTag: app.TagInvalidInput,
			wantRoute: "Accounts.SetDisabled.InvalidInput",
		},
		{
			name: "the last admin cannot be disabled",
			setup: func(e *env) (app.Actor, uint64, bool) {
				// The actor is an admin whose account was disabled after the
				// request began: it is not counted.
				root, other := e.signUp("root", store.RoleAdmin), e.signUp("other", store.RoleAdmin)
				if err := e.svc.SetDisabled(e.t.Context(), actor(root), other.ID, true); err != nil {
					e.t.Fatal(err)
				}
				return actor(other), root.ID, true
			},
			wantDisabled: false, wantTag: app.TagConflict,
			wantRoute: "Accounts.SetDisabled->Store.Tx->DB.Transaction->Store.Tx.func1->Accounts.SetDisabled.func1.Conflict",
		},
		{
			name: "enabling an admin makes it count again",
			setup: func(e *env) (app.Actor, uint64, bool) {
				root, other := e.signUp("root", store.RoleAdmin), e.signUp("other", store.RoleAdmin)
				if err := e.svc.SetDisabled(e.t.Context(), actor(root), other.ID, true); err != nil {
					e.t.Fatal(err)
				}
				if err := e.svc.SetDisabled(e.t.Context(), actor(root), other.ID, false); err != nil {
					e.t.Fatal(err)
				}
				return actor(other), root.ID, true
			},
			wantDisabled: true,
		},
		{
			name: "unknown user",
			setup: func(e *env) (app.Actor, uint64, bool) {
				return actor(e.signUp("root", store.RoleAdmin)), 9999, true
			},
			wantTag:   app.TagNotFound,
			wantRoute: "Accounts.SetDisabled->Store.Tx->DB.Transaction->Store.Tx.func1->Accounts.SetDisabled.func1->Store.UserByID.NotFound",
		},
		{
			name: "a member may not",
			setup: func(e *env) (app.Actor, uint64, bool) {
				e.signUp("root", store.RoleAdmin)
				m, o := e.signUp("mem", store.RoleMember), e.signUp("oth", store.RoleMember)
				return actor(m), o.ID, true
			},
			wantTag:   app.TagForbidden,
			wantRoute: "Accounts.SetDisabled.Forbidden",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			act, target, disabled := tt.setup(e)
			err := e.svc.SetDisabled(t.Context(), act, target, disabled)
			if tt.wantTag == "" {
				if err != nil {
					t.Fatalf("SetDisabled: %+v", err)
				}
			} else {
				_ = requireTag(t, err, tt.wantTag, tt.wantRoute)
			}
			if tt.wantTag == app.TagNotFound || tt.wantTag == app.TagForbidden {
				return
			}
			row, err := e.st.UserByID(t.Context(), target)
			if err != nil {
				t.Fatal(err)
			}
			if got := row.DisabledAt != nil; got != tt.wantDisabled {
				t.Errorf("disabled = %v, want %v", got, tt.wantDisabled)
			}
		})
	}
}

func TestSetDisabledDeletesSessions(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	root := e.signUp("root", store.RoleAdmin)
	victim := e.signUp("victim", store.RoleMember)
	bystander := e.signUp("bystander", store.RoleMember)
	addSession := func(hash string, u app.User) {
		t.Helper()
		s := &store.Session{
			TokenHash: hash, UserID: u.ID, ExpiresAt: start.Add(time.Hour), LastSeenAt: start,
		}
		if err := e.st.CreateSession(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	count := func(u app.User) int64 {
		t.Helper()
		n, err := e.st.CountSessionsOfUser(t.Context(), u.ID)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	addSession("v1", victim)
	addSession("v2", victim)
	addSession("b1", bystander)

	if err := e.svc.SetDisabled(t.Context(), actor(root), victim.ID, true); err != nil {
		t.Fatalf("SetDisabled: %+v", err)
	}
	if got := count(victim); got != 0 {
		t.Errorf("the victim has %d sessions, want 0", got)
	}
	if got := count(bystander); got != 1 {
		t.Errorf("the bystander has %d sessions, want 1", got)
	}

	// A refused change keeps the sessions: a member may not disable.
	addSession("b2", bystander)
	err := e.svc.SetDisabled(t.Context(), actor(victim), bystander.ID, true)
	_ = requireTag(t, err, app.TagForbidden, "Accounts.SetDisabled.Forbidden")
	if got := count(bystander); got != 2 {
		t.Errorf("the bystander has %d sessions, want 2", got)
	}

	// Enabling does not bring the sessions back and does not touch others.
	if err := e.svc.SetDisabled(t.Context(), actor(root), victim.ID, false); err != nil {
		t.Fatal(err)
	}
	if got := count(victim); got != 0 {
		t.Errorf("the victim has %d sessions after enabling, want 0", got)
	}
}

// TestPermissions is the table of SPEC §12 for the methods that take an actor:
// who may call each. The actors are the roles; a disabled user has no session,
// so it never becomes an Actor, and the web layer's middleware (step 2.3) is
// what refuses it.
func TestPermissions(t *testing.T) {
	t.Parallel()
	type call func(e *env, act app.Actor, member app.User) error
	methods := []struct {
		name      string
		call      call
		adminOnly bool
		route     string
	}{
		{
			name: "CreateInvitation",
			call: func(e *env, act app.Actor, _ app.User) error {
				_, err := e.svc.CreateInvitation(t.Context(), act, store.RoleMember)
				return err
			},
			route: "Accounts.CreateInvitation.Forbidden",
		},
		{
			name: "Users",
			call: func(e *env, act app.Actor, _ app.User) error {
				_, err := e.svc.Users(t.Context(), act)
				return err
			},
			route: "Accounts.Users.Forbidden",
		},
		{
			name: "SetRole",
			call: func(e *env, act app.Actor, member app.User) error {
				return e.svc.SetRole(t.Context(), act, member.ID, store.RoleMember)
			},
			route: "Accounts.SetRole.Forbidden",
		},
		{
			name: "SetDisabled",
			call: func(e *env, act app.Actor, member app.User) error {
				return e.svc.SetDisabled(t.Context(), act, member.ID, false)
			},
			route: "Accounts.SetDisabled.Forbidden",
		},
	}
	actors := []struct {
		name  string
		actor func(admin, member app.User) app.Actor
		want  bool
	}{
		{"admin", func(admin, _ app.User) app.Actor { return actor(admin) }, true},
		{"member", func(_, member app.User) app.Actor { return actor(member) }, false},
		{"empty actor", func(_, _ app.User) app.Actor { return app.Actor{} }, false},
		{"unknown role", func(admin, _ app.User) app.Actor {
			a := actor(admin)
			a.Role = "superuser"
			return a
		}, false},
	}
	for _, m := range methods {
		for _, a := range actors {
			t.Run(m.name+"/"+a.name, func(t *testing.T) {
				e := newEnv(t)
				admin := e.signUp("root", store.RoleAdmin)
				member := e.signUp("mem", store.RoleMember)
				err := m.call(e, a.actor(admin, member), member)
				if a.want {
					if err != nil {
						t.Fatalf("%s by %s: %+v", m.name, a.name, err)
					}
					return
				}
				_ = requireTag(t, err, app.TagForbidden, m.route)
			})
		}
	}
}

// TestStoreErrorsPassUp checks that a failure of the store reaches the caller
// untouched: same tag, and a route that goes down into the store.
func TestStoreErrorsPassUp(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	root := e.signUp("root", store.RoleAdmin)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	tests := []struct {
		name  string
		call  func() error
		tag   string
		route string
	}{
		{
			name: "users",
			call: func() error {
				_, err := e.svc.Users(ctx, actor(root))
				return err
			},
			tag: store.TagQueryFailed, route: "Accounts.Users->Store.ListUsers.QueryFailed",
		},
		{
			name: "invitation",
			call: func() error {
				_, err := e.svc.CreateInvitation(ctx, actor(root), store.RoleMember)
				return err
			},
			tag: store.TagQueryFailed, route: "Accounts.CreateInvitation->Accounts.issueInvitation->Store.CreateInvitation.QueryFailed",
		},
		{
			name: "system invitation",
			call: func() error {
				_, err := e.svc.CreateSystemInvitation(ctx, store.RoleMember)
				return err
			},
			tag: store.TagQueryFailed, route: "Accounts.CreateSystemInvitation->Accounts.issueInvitation->Store.CreateInvitation.QueryFailed",
		},
		{
			name:  "validity check",
			call:  func() error { return e.svc.InvitationValid(ctx, "t") },
			tag:   store.TagQueryFailed,
			route: "Accounts.InvitationValid->Accounts.usableInvitation->Store.InvitationByTokenHash.QueryFailed",
		},
		{
			name: "sign-up",
			call: func() error {
				_, err := e.svc.SignUp(ctx, "t", "alice", "Alice", goodPassword)
				return err
			},
			tag: store.TagQueryFailed, route: "Accounts.SignUp->Accounts.usableInvitation->Store.InvitationByTokenHash.QueryFailed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = requireTag(t, tt.call(), tt.tag, tt.route)
		})
	}
}

func TestUserIsAdmin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		user app.User
		want bool
	}{
		{"admin", app.User{Role: app.RoleAdmin}, true},
		{"member", app.User{Role: app.RoleMember}, false},
		{"no role", app.User{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.user.IsAdmin(); got != tt.want {
				t.Errorf("IsAdmin = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestLastAdminSurvivesChangesAtTheSameTime has two admins who demote or
// disable each other at the same moment, as requests on two processes can: one
// of the changes wins, the other is refused, and an active admin is left. It
// is a race, so it repeats; on PostgreSQL it fails without the lock of
// Store.LockAdmins.
func TestLastAdminSurvivesChangesAtTheSameTime(t *testing.T) {
	t.Parallel()
	changes := []struct {
		name string
		do   func(e *env, by app.Actor, target uint64) error
	}{
		{"demote", func(e *env, by app.Actor, target uint64) error {
			return e.svc.SetRole(e.t.Context(), by, target, store.RoleMember)
		}},
		{"disable", func(e *env, by app.Actor, target uint64) error {
			return e.svc.SetDisabled(e.t.Context(), by, target, true)
		}},
	}
	for _, c := range changes {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			for round := range 10 {
				e := newEnv(t)
				a := e.signUp("anna", store.RoleAdmin)
				b := e.signUp("boris", store.RoleAdmin)
				errs := make([]error, 2)
				var wg sync.WaitGroup
				wg.Go(func() { errs[0] = c.do(e, actor(a), b.ID) })
				wg.Go(func() { errs[1] = c.do(e, actor(b), a.ID) })
				wg.Wait()
				n, err := e.st.CountActiveAdmins(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if n < 1 {
					t.Fatalf("round %d: no active admin is left (errors: %v, %v)", round, errs[0], errs[1])
				}
				for _, err := range errs {
					if err == nil {
						continue
					}
					if appErr, ok := errors.FromError(err); !ok || appErr.Tag != app.TagConflict {
						t.Errorf("round %d: the refused change failed with %v, want TagConflict", round, err)
					}
				}
			}
		})
	}
}
