package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// dataWithUsers makes a data directory with an admin "root", a member "bob" and
// a disabled member "dina", all with the password "a password of 12+".
func dataWithUsers(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"invite", "-data", dir, "-admin"}, mapEnv(nil), &stdout, &stderr, auth.FastHasher()); err != nil {
		t.Fatalf("invite: %+v", err)
	}
	accounts, _ := openAccounts(t, dir)
	rootToken := strings.TrimPrefix(strings.SplitN(stdout.String(), "\n", 2)[0], config.DefaultBaseURL+"/invite/")
	root, err := accounts.SignUp(t.Context(), rootToken, "root", "Root", "a password of 12+")
	if err != nil {
		t.Fatalf("SignUp: %+v", err)
	}
	admin := app.Actor{UserID: root.ID, Username: "root", Role: app.RoleAdmin}
	for _, name := range []string{"bob", "dina"} {
		inv, err := accounts.CreateInvitation(t.Context(), admin, app.RoleMember)
		if err != nil {
			t.Fatalf("CreateInvitation: %+v", err)
		}
		u, err := accounts.SignUp(t.Context(), inv.Token, name, strings.ToUpper(name[:1])+name[1:], "a password of 12+")
		if err != nil {
			t.Fatalf("SignUp %s: %+v", name, err)
		}
		if name == "dina" {
			if err := accounts.SetDisabled(t.Context(), admin, u.ID, true); err != nil {
				t.Fatalf("SetDisabled: %+v", err)
			}
		}
	}
	return dir
}

func TestRunResetPassword(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		env      map[string]string
		wantBase string
		wantUser string
	}{
		{name: "the default base URL", args: []string{"-user", "bob"}, wantBase: config.DefaultBaseURL, wantUser: "bob"},
		{name: "the name is lower-cased and trimmed", args: []string{"-user", "  BOB "}, wantBase: config.DefaultBaseURL, wantUser: "bob"},
		{name: "an admin", args: []string{"-user", "root"}, wantBase: config.DefaultBaseURL, wantUser: "root"},
		{
			name: "base URL flag", args: []string{"-user", "bob", "-base-url", "https://chat.example.com"},
			wantBase: "https://chat.example.com", wantUser: "bob",
		},
		{
			name: "base URL with a trailing slash", args: []string{"-user", "bob", "-base-url", "https://chat.example.com/team/"},
			wantBase: "https://chat.example.com/team", wantUser: "bob",
		},
		{
			name: "base URL from the environment", args: []string{"-user", "bob"},
			env: map[string]string{"CLOWDER_BASE_URL": "http://env.test:9000"}, wantBase: "http://env.test:9000", wantUser: "bob",
		},
		{
			name: "the flag beats the environment", args: []string{"-user", "bob", "-base-url", "http://flag.test"},
			env: map[string]string{"CLOWDER_BASE_URL": "http://env.test"}, wantBase: "http://flag.test", wantUser: "bob",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := dataWithUsers(t)
			args := append([]string{"reset-password", "-data", dir}, tt.args...)
			var stdout, stderr bytes.Buffer
			before := time.Now()
			if err := run(t.Context(), args, mapEnv(tt.env), &stdout, &stderr, auth.FastHasher()); err != nil {
				t.Fatalf("reset-password: %+v", err)
			}
			after := time.Now()

			lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
			if len(lines) != 2 {
				t.Fatalf("stdout = %q, want the link and the expiry", stdout.String())
			}
			prefix := tt.wantBase + "/reset/"
			if !strings.HasPrefix(lines[0], prefix) {
				t.Fatalf("link = %q, want prefix %q", lines[0], prefix)
			}
			token := strings.TrimPrefix(lines[0], prefix)
			if token == "" || strings.ContainsAny(token, "/?# ") {
				t.Errorf("token = %q", token)
			}
			expiry, ok := strings.CutPrefix(lines[1], "expires ")
			if !ok {
				t.Fatalf("second line = %q, want the expiry", lines[1])
			}
			expires, err := time.Parse(time.RFC3339, expiry)
			if err != nil {
				t.Fatalf("expiry %q: %v", expiry, err)
			}
			// 24 hours, printed to the second.
			if expires.Before(before.Add(app.ResetLinkLifetime).Truncate(time.Second)) || expires.After(after.Add(app.ResetLinkLifetime)) {
				t.Errorf("expires %v, want 24 hours after now", expires)
			}

			// The token in the link works, once, for that user, and the
			// database keeps the hash and no maker.
			accounts, st := openAccounts(t, dir)
			if err := accounts.ResetLinkValid(t.Context(), token); err != nil {
				t.Fatalf("ResetLinkValid: %+v", err)
			}
			row, err := st.PasswordResetByTokenHash(t.Context(), auth.HashToken(token))
			if err != nil {
				t.Fatal(err)
			}
			if row.CreatedBy != nil || row.TokenHash == token {
				t.Errorf("row = %+v, want no maker and a hash", row)
			}
			events, err := st.ListAuditEvents(t.Context(), store.AuditFilter{Action: store.ActionUserResetLinkCreated}, 10, 0)
			if err != nil || len(events) != 1 {
				t.Fatalf("audit events = %+v, %v", events, err)
			}
			if ev := events[0]; ev.ActorID != row.UserID || ev.EntityID != row.UserID || ev.RoomID != store.NoRoom ||
				!strings.Contains(ev.Summary, "on the command line") || !strings.Contains(ev.Details, "command line") ||
				strings.Contains(ev.Summary+ev.Details, token) {
				t.Errorf("audit event = %+v", ev)
			}
			res, err := accounts.ResetPassword(t.Context(), app.ResetInput{Token: token, NewPassword: "a brand new passphrase"})
			if err != nil {
				t.Fatalf("ResetPassword: %+v", err)
			}
			if res.User.Username != tt.wantUser {
				t.Errorf("user = %q, want %q", res.User.Username, tt.wantUser)
			}
			if _, _, err := accounts.Login(t.Context(), tt.wantUser, "a brand new passphrase", "192.0.2.1"); err != nil {
				t.Errorf("login with the new password: %+v", err)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q", stderr.String())
			}
		})
	}
}

func TestRunResetPasswordTwiceReplacesTheLink(t *testing.T) {
	dir := dataWithUsers(t)
	var tokens []string
	for range 2 {
		var stdout, stderr bytes.Buffer
		args := []string{"reset-password", "-data", dir, "-user", "bob"}
		if err := run(t.Context(), args, mapEnv(nil), &stdout, &stderr, auth.FastHasher()); err != nil {
			t.Fatalf("reset-password: %+v", err)
		}
		link := strings.SplitN(stdout.String(), "\n", 2)[0]
		tokens = append(tokens, strings.TrimPrefix(link, config.DefaultBaseURL+"/reset/"))
	}
	if tokens[0] == tokens[1] {
		t.Fatalf("the same link twice: %s", tokens[0])
	}
	accounts, _ := openAccounts(t, dir)
	if err := accounts.ResetLinkValid(t.Context(), tokens[0]); err == nil {
		t.Error("the first link still works")
	}
	if err := accounts.ResetLinkValid(t.Context(), tokens[1]); err != nil {
		t.Errorf("the second link: %+v", err)
	}
}

func TestRunResetPasswordErrors(t *testing.T) {
	data := dataWithUsers(t)
	// A directory where the database file should be.
	badDB := t.TempDir()
	if err := os.Mkdir(filepath.Join(badDB, databaseFile), 0o750); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		args        []string
		env         map[string]string
		wantTag     string
		wantRoute   string
		wantMessage string
	}{
		{
			name: "no user", args: []string{"reset-password", "-data", data},
			wantTag: tagResetUserMissing, wantRoute: "run->runResetPassword.ResetUserMissing",
		},
		{
			name: "a blank user", args: []string{"reset-password", "-data", data, "-user", "  "},
			wantTag: tagResetUserMissing, wantRoute: "run->runResetPassword.ResetUserMissing",
		},
		{
			name: "an unknown user", args: []string{"reset-password", "-data", data, "-user", "nobody"},
			wantTag: app.TagNotFound, wantMessage: "there is no user with this username",
			wantRoute: "run->runResetPassword->Accounts.CreateSystemResetLink->Store.Tx->DB.Transaction->Store.Tx.func1->Accounts.CreateSystemResetLink.func1.NotFound",
		},
		{
			name: "a disabled user", args: []string{"reset-password", "-data", data, "-user", "dina"},
			wantTag: app.TagConflict, wantMessage: "the user is disabled",
			wantRoute: "run->runResetPassword->Accounts.CreateSystemResetLink->Store.Tx->DB.Transaction->Store.Tx.func1->Accounts.CreateSystemResetLink.func1.Conflict",
		},
		{
			name: "unknown flag", args: []string{"reset-password", "-nope"},
			wantTag: tagFlagsInvalid, wantRoute: "run->runResetPassword.FlagsInvalid",
		},
		{
			name: "unexpected argument", args: []string{"reset-password", "-user", "bob", "extra"},
			wantTag: tagFlagsInvalid, wantRoute: "run->runResetPassword.FlagsInvalid",
		},
		{
			name: "base URL that is not absolute", args: []string{"reset-password", "-user", "bob", "-base-url", "chat.example.com"},
			wantTag: config.TagBaseURLInvalid, wantRoute: "run->runResetPassword->Config.Validate.BaseURLInvalid",
		},
		{
			name: "database URL with another scheme", args: []string{"reset-password", "-user", "bob", "-database-url", "mysql://u:secret@db/clowder"},
			wantTag: config.TagDatabaseURLInvalid, wantRoute: "run->runResetPassword->Config.Validate.DatabaseURLInvalid",
		},
		{
			name: "database cannot be opened", args: []string{"reset-password", "-user", "bob", "-data", badDB},
			wantTag: "StoreOpenFailed", wantRoute: "run->runResetPassword->openStore->openStoreWith->Open->open.StoreOpenFailed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(t.Context(), tt.args, mapEnv(tt.env), &stdout, &stderr, auth.FastHasher())
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if appErr.Tag != tt.wantTag {
				t.Errorf("tag = %q, want %q", appErr.Tag, tt.wantTag)
			}
			if !appErr.HasRoute(tt.wantRoute) {
				t.Errorf("unexpected failure path: %s, want %s", appErr.Route(), tt.wantRoute)
			}
			if tt.wantMessage != "" && appErr.Message != tt.wantMessage {
				t.Errorf("message = %q, want %q", appErr.Message, tt.wantMessage)
			}
			if stdout.Len() != 0 {
				t.Errorf("unexpected stdout: %q", stdout.String())
			}
		})
	}
	// Nothing was made for the users that were refused.
	accounts, st := openAccounts(t, data)
	_ = accounts
	events, err := st.ListAuditEvents(t.Context(), store.AuditFilter{Action: store.ActionUserResetLinkCreated}, 10, 0)
	if err != nil || len(events) != 0 {
		t.Errorf("audit events = %+v, %v; want none", events, err)
	}
}

func TestUsageListsResetPassword(t *testing.T) {
	var stderr bytes.Buffer
	err := run(t.Context(), []string{"nope"}, mapEnv(nil), &bytes.Buffer{}, &stderr, auth.FastHasher())
	if err == nil || !strings.Contains(stderr.String(), "reset-password") {
		t.Errorf("err = %v, usage = %q", err, stderr.String())
	}
}
